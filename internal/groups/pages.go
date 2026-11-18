// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/metrics"
)

// Page is a Page as the Timeline entry of an event of the kind escalation names it (C-32.FR-19): the people it names,
// the Notification gateway of a call, nil for a Page by Mention, and the reason a Page was not delivered. groups knows
// no package of Paging (S-169); the callers fill it. A paged by Mention also carries its pages row, the Destinations
// its Thread replies go to, which delivery fans the event out into (C-32.FR-8), and the Team or the token of a
// page_team step; it and the page_not_delivered of a Page by Mention carry the importance of a Page of people.
type Page struct {
	Users      []PagedUser
	Gateway    *PageGateway
	Reason     string
	ID         int64
	Targets    []PageTarget
	Team       *PageTeam
	Everyone   string
	Importance string
}

// PageTeam is the Team a page_team step Pages by its Team mention.
type PageTeam struct {
	ID       int64
	PublicID string
	Name     string
}

// PagedUser is a person a Page names: a User by internal id, public_id, name and login.
type PagedUser struct {
	ID       int64
	PublicID string
	Name     string
	Login    string
}

// PageGateway is the Notification gateway of a call: its Connection by internal id, public_id and name.
type PageGateway struct {
	ID       int64
	PublicID string
	Name     string
}

// The reasons of page_not_delivered for a call through a Notification gateway (C-30.FR-7, PageNotDeliveredReason):
// the gateway refused it, answered what Muster cannot classify, kept failing until delivery.transient_budget was
// used, or the person had no confirmed contact at the gateway at its first attempt, or the gateway was deleted, or is
// no Notification gateway any more, before the call was sent (C-30.FR-9), or it came due while the gateway was Broken
// (C-30.FR-8). A Page by Mention is not delivered when none of its chain's Destinations has the Alert Group's Root
// message, when the person has no Account link in the Identity space of any of them, or when every Thread reply that
// could mention them ended Not delivered, was withheld while its Destination was Broken or was sent with its line of
// Mentions leaving them out for length (C-32.FR-10, C-12.FR-11).
const (
	PageCallRefused        = "call_refused"
	PageCallUnknown        = "call_unknown"
	PageCallBudgetUsed     = "call_budget_used"
	PageNoConfirmedContact = "no_confirmed_contact"
	PageGatewayDeleted     = "gateway_deleted"
	PageGatewayBroken      = "gateway_broken"
	PageNoRootMessage      = "no_root_message"
	PageNoAccountLink      = "no_account_link"
	PageRepliesFailed      = "not_delivered"
)

// PageReasons are the reasons of page_not_delivered, a closed list.
var PageReasons = []string{PageCallRefused, PageCallUnknown, PageCallBudgetUsed, PageNoConfirmedContact,
	PageGatewayDeleted, PageGatewayBroken, PageNoRootMessage, PageNoAccountLink, PageRepliesFailed}

func (p *Page) userIDs() []int64 {
	out := make([]int64, len(p.Users))
	for i, u := range p.Users {
		out[i] = u.ID
	}
	return out
}

func (p *Page) gatewayID() pgtype.Int8 {
	if p.Gateway == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: p.Gateway.ID, Valid: true}
}

// pageQueries are the queries of the Pages, by Mention and by a call.
type pageQueries interface {
	InsertPage(ctx context.Context, arg dbgen.InsertPageParams) (int64, error)
	SetPageCall(ctx context.Context, arg dbgen.SetPageCallParams) error
	ListCallPages(ctx context.Context, arg dbgen.ListCallPagesParams) ([]dbgen.ListCallPagesRow, error)
	MissCallPageUsers(ctx context.Context, arg dbgen.MissCallPageUsersParams) ([]int64, error)
	InsertPageUser(ctx context.Context, arg dbgen.InsertPageUserParams) error
	LockPage(ctx context.Context, arg dbgen.LockPageParams) (dbgen.LockPageRow, error)
	ReachPageUsers(ctx context.Context, arg dbgen.ReachPageUsersParams) ([]int64, error)
	MissPageUsers(ctx context.Context, arg dbgen.MissPageUsersParams) ([]int64, error)
	CountPageUsers(ctx context.Context, arg dbgen.CountPageUsersParams) (dbgen.CountPageUsersRow, error)
	EndPage(ctx context.Context, arg dbgen.EndPageParams) error
}

// The sources of the run of a chain, which decide where it Pages (C-32.FR-8): its Route, and the Teams that named it.
const (
	PageSourceRoute = "route"
	PageSourceTeam  = "team"
)

// PageSource is a source of the run of a chain: its Route, or a Team by id.
type PageSource struct {
	Kind   string
	TeamID int64
}

// PageRequest is what a Page asks PageTargets for: the sources of its run, the Team or the token of a page_team step,
// and the people of a Page of people.
type PageRequest struct {
	Sources  []PageSource
	Team     int64
	Everyone string
	People   []int64
}

// PageTarget is a Destination a Page writes a Thread reply in (C-32.FR-8, FR-9): the Destination, whether it is one of
// the Route or of a Team of the chain (PageSourceRoute, PageSourceTeam), the people of the Page with an Account link in
// its Identity space, whom its reply mentions — every person for an outgoing webhook, which takes them as data —,
// those with an Account link whom its reply names by the plain name, which reaches none of them, as a bridge without
// native_mentions does (C-32.FR-11, C-22.FR-16), and whether its reply is withheld, as while it is Broken
// (C-11.FR-19).
type PageTarget struct {
	DestinationID int64
	Source        string
	Linked        []int64
	Named         []int64
	Withheld      bool
}

// PageTargets finds the Destinations of a run that can carry a Page, in the dispatcher's transaction tx after the
// lock of g: those of its sources that have the Alert Group's Root message, and for a page_team step only those whose
// Connection can mention the Team or the token. delivery implements it and internal/runtime wires it, since groups
// imports no package of delivery (ADR-0016).
type PageTargets interface {
	PageTargets(ctx context.Context, tx DBTX, g *Group, r PageRequest) ([]PageTarget, error)
}

// SetPageTargets fills where Pages go, before anything changes an Alert Group; without it no step Pages by Mention.
func (s *Service) SetPageTargets(t PageTargets) {
	s.d.pages = t
}

// StepPage is one Page by Mention that a step of a run, or an action of a Notification rule, makes (C-32.FR-3,
// C-31.FR-3): the run, the chain and the step; the sources and the owning Team of the run, which name where it Pages
// and "Chain of the Team"; the people, each by the first Mention their rule makes at that moment, or the Team or the
// token of a page_team step; the importance of the step; the Schedule of a page_on_call step; the wait until the next
// Mention of the rule, 0 for none; whether it is a later Mention of a rule and that rule run. After and Seq are its
// place among the skips and the webhooks of the same ChainProgress, which keep the order of the steps. RunStepSeq is
// the seq of the run step that made the first Mentions of a step, 0 for a later Mention of a rule, whose rule run
// carries the seq of the step that started it: the order of the steps of a run, by which the Assignees change
// (C-32.FR-12). Call makes it a Page of one person by a call through a Notification gateway instead (C-32.FR-3,
// C-31.FR-3): a step "Call through a Notification gateway", or a call action of a rule run, RuleRun.
type StepPage struct {
	ChainEntry
	Sources      []PageSource
	OwningTeam   int64
	People       []int64
	Team         int64
	Everyone     string
	Importance   string
	Schedule     int64
	AgainSeconds int
	Again        bool
	RuleRun      int64
	After        int
	Seq          int
	RunStepSeq   int
	Call         *StepCall
}

// StepCall is the call of a Page through a Notification gateway (C-30.FR-4): the gateway, and Queue, which queues the
// call of the Page, its pages row page, in the dispatcher's transaction tx once every Alert Group the change locks is
// locked (gateways, Lock order), and returns the internal id of the call it joined and what to run once tx
// committed. escalation fills it, through gateways, which groups does not import.
type StepCall struct {
	Gateway PageGateway
	Queue   func(ctx context.Context, tx DBTX, page int64) (int64, func(context.Context), error)
}

// queuedCall is a Page of the kind call written in a change, of the person user through the gateway, whose call
// ChainStep queues after its dispatches.
type queuedCall struct {
	page, gateway, user int64
	queue               func(ctx context.Context, tx DBTX, page int64) (int64, func(context.Context), error)
}

// teamPage reports whether the Page is of a page_team step: a Team or everyone in the chat.
func (p StepPage) teamPage() bool { return p.Team != 0 || p.Everyone != "" }

// chainTeam is the Team that "Chain of the Team" names: the owning Team the chain had when its run started, else the
// first Team that named the chain, 0 for a chain of the Route alone.
func (p StepPage) chainTeam() int64 {
	if p.OwningTeam != 0 {
		return p.OwningTeam
	}
	for _, s := range p.Sources {
		if s.Kind == PageSourceTeam {
			return s.TeamID
		}
	}
	return 0
}

// The outcomes of a Page and of a person it names, and the kinds of a Page: by Mention, and by a call through a
// Notification gateway.
const (
	PagePending      = "pending"
	PageDelivered    = "delivered"
	PageNotDelivered = "not_delivered"
	PageKindMention  = "mention"
	PageKindCall     = "call"
)

// pageEnd is a Page that ended, for muster_pages_total once the transaction committed; kind empty is a Page by
// Mention.
type pageEnd struct {
	chain   string
	kind    string
	outcome string
}

// countPages counts the Pages that ended once the transaction committed (C-32.FR-19); a Page whose chain was deleted
// is not counted, as its chain no longer has a public_id to name it by.
func countPages(after *committed, ended []pageEnd) {
	for _, e := range ended {
		if e.chain == "" {
			continue
		}
		kind := cmp.Or(e.kind, PageKindMention)
		after.add(func(context.Context) { metrics.Pages.With(e.chain, kind, e.outcome).Inc() })
	}
}

// pagedUsers names the people of a Page by id, in the order of ids; a User who no longer exists is left out.
func pagedUsers(ctx context.Context, q Queries, orgID int64, ids []int64) ([]PagedUser, error) {
	if len(ids) == 0 {
		return []PagedUser{}, nil
	}
	rows, err := q.ListUserRefs(ctx, dbgen.ListUserRefsParams{OrgID: orgID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("name the people of a page: %w", err)
	}
	byID := make(map[int64]dbgen.ListUserRefsRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	out := make([]PagedUser, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			out = append(out, PagedUser{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Login: r.Login})
		}
	}
	return out, nil
}

// pagedOf are the people among users whose ids are in ids, in the order of users.
func pagedOf(users []PagedUser, ids []int64) []PagedUser {
	var out []PagedUser
	for _, u := range users {
		if slices.Contains(ids, u.ID) {
			out = append(out, u)
		}
	}
	return out
}

// outcomeOf is the outcome of a person of a new Page and its reason: not delivered for no_root_message without a
// Destination, for no_account_link when no Destination's Identity space has their Account link, and for
// not_delivered when every reply that would mention them is withheld or names them by the plain name, which reaches
// nobody (PageTarget.Named); pending otherwise, until the replies end.
func outcomeOf(id int64, targets []PageTarget) (string, string) {
	if len(targets) == 0 {
		return PageNotDelivered, PageNoRootMessage
	}
	linked, open := false, false
	for _, t := range targets {
		switch {
		case slices.Contains(t.Linked, id):
			linked = true
			open = open || !t.Withheld
		case slices.Contains(t.Named, id):
			linked = true
		}
	}
	switch {
	case !linked:
		return PageNotDelivered, PageNoAccountLink
	case !open:
		return PageNotDelivered, PageRepliesFailed
	}
	return PagePending, ""
}

// page makes one Page by Mention on the locked g in the change c (C-32.FR-8 to FR-10, FR-19): it asks PageTargets for
// the Destinations of the run that can carry it. A page_team step that none can carry records escalation_step_skipped
// with no_mention_possible; a Page of people without any records page_not_delivered with no_root_message for all of
// them. Otherwise it writes the Page with its people and records the Loud paged, which delivery fans out into one
// Thread reply per Destination, then page_not_delivered for the people it cannot reach: no_account_link without an
// Account link in any Destination's Identity space, not_delivered when every reply that would mention them is
// withheld. A Page that nobody can still be reached by ends at once. ended collects the Pages that ended.
func (t chainSteps) page(ctx context.Context, q Queries, g *Group, p StepPage, c *change) error {
	chain := p.ChainEntry
	targets, err := t.targets.PageTargets(ctx, q.DB(), g, PageRequest{Sources: p.Sources, Team: p.Team,
		Everyone: p.Everyone, People: p.People})
	if err != nil {
		return fmt.Errorf("find where step %d pages on alert group #%d: %w", p.Step, g.Number, err)
	}
	if p.teamPage() && len(targets) == 0 {
		c.add(entry{Event: EventEscalationStepSkipped, Reason: SkipNoMentionPossible, Chain: &chain})
		return nil
	}
	users, err := pagedUsers(ctx, q, t.orgID, p.People)
	if err != nil {
		return err
	}
	if !p.teamPage() && len(users) == 0 {
		return nil
	}
	outcomes, reasons := make([]string, len(users)), make([]string, len(users))
	byReason := map[string][]int64{}
	pending := false
	for i, u := range users {
		outcomes[i], reasons[i] = outcomeOf(u.ID, targets)
		if outcomes[i] == PagePending {
			pending = true
			continue
		}
		byReason[reasons[i]] = append(byReason[reasons[i]], u.ID)
	}
	if p.teamPage() {
		pending = slices.ContainsFunc(targets, func(x PageTarget) bool { return !x.Withheld })
	}
	ins := dbgen.InsertPageParams{OrgID: t.orgID, AlertGroupID: g.ID, StepNumber: int32(p.Step), //nolint:gosec // G115: at most 20
		EscalationRunID:       pgtype.Int8{Int64: p.Run, Valid: p.Run != 0},
		NotificationRuleRunID: pgtype.Int8{Int64: p.RuleRun, Valid: p.RuleRun != 0},
		TeamID:                pgtype.Int8{Int64: p.Team, Valid: p.Team != 0}, Everyone: nonEmpty(p.Everyone),
		Importance: nonEmpty(p.Importance), ScheduleID: pgtype.Int8{Int64: p.Schedule, Valid: p.Schedule != 0},
		ChainTeamID:  pgtype.Int8{Int64: p.chainTeam(), Valid: p.chainTeam() != 0},
		AgainSeconds: pgtype.Int4{Int32: int32(p.AgainSeconds), Valid: p.AgainSeconds > 0}, //nolint:gosec // G115: at most an hour
		Again:        p.Again, State: PagePending, Now: t.now, Kind: PageKindMention,
		RunStepSeq: pgtype.Int4{Int32: int32(p.RunStepSeq), Valid: p.RunStepSeq > 0}} //nolint:gosec // G115: at most MaxStepsPerRun
	outcome := ""
	if !pending {
		outcome = PageNotDelivered
		ins.State, ins.EndedAt = PageNotDelivered, pgtype.Timestamptz{Time: t.now, Valid: true}
		if len(byReason) == 1 {
			for r := range byReason {
				ins.Reason = nonEmpty(r)
			}
		}
	}
	id, err := q.InsertPage(ctx, ins)
	if err != nil {
		return fmt.Errorf("write the page of step %d on alert group #%d: %w", p.Step, g.Number, err)
	}
	for i, u := range users {
		if err := q.InsertPageUser(ctx, dbgen.InsertPageUserParams{OrgID: t.orgID, PageID: id, UserID: u.ID,
			Outcome: outcomes[i], Reason: nonEmpty(reasons[i])}); err != nil {
			return fmt.Errorf("write the people of the page of step %d on alert group #%d: %w", p.Step, g.Number, err)
		}
	}
	if outcome != "" {
		*t.ended = append(*t.ended, pageEnd{chain: chain.ChainPublicID, outcome: outcome})
	}
	if len(targets) > 0 {
		page := &Page{ID: id, Users: users, Targets: targets, Everyone: p.Everyone, Importance: p.Importance}
		e := entry{Event: EventPaged, Chain: &chain, Page: page}
		if p.Team != 0 {
			page.Team = &PageTeam{ID: p.Team}
			team := p.Team
			e.TeamID = &team
		}
		c.add(e)
	}
	for _, reason := range []string{PageNoRootMessage, PageNoAccountLink, PageRepliesFailed} {
		if ids := byReason[reason]; len(ids) > 0 {
			c.add(entry{Event: EventPageNotDelivered, Reason: reason, Chain: &chain,
				Page: &Page{Users: pagedOf(users, ids), Reason: reason, ID: id, Importance: p.Importance}})
		}
	}
	return nil
}

// call makes one Page of a person by a call through a Notification gateway on the locked g in the change c
// (C-32.FR-3, FR-9, FR-19, C-31.FR-3): it writes the Page of the kind call, pending until its call ends, and records
// the Quiet paged naming the person and the gateway, which writes nothing in any Thread; ChainStep queues its call
// once the change is recorded (StepCall). A person who no longer exists is Paged by nobody.
func (t chainSteps) call(ctx context.Context, q Queries, g *Group, p StepPage, c *change) error {
	users, err := pagedUsers(ctx, q, t.orgID, p.People)
	if err != nil || len(users) == 0 {
		return err
	}
	chain, gw := p.ChainEntry, p.Call.Gateway
	id, err := q.InsertPage(ctx, dbgen.InsertPageParams{OrgID: t.orgID, AlertGroupID: g.ID,
		StepNumber:            int32(p.Step), //nolint:gosec // G115: at most 20
		EscalationRunID:       pgtype.Int8{Int64: p.Run, Valid: p.Run != 0},
		NotificationRuleRunID: pgtype.Int8{Int64: p.RuleRun, Valid: p.RuleRun != 0}, Kind: PageKindCall,
		Importance: nonEmpty(p.Importance), ScheduleID: pgtype.Int8{Int64: p.Schedule, Valid: p.Schedule != 0},
		ChainTeamID: pgtype.Int8{Int64: p.chainTeam(), Valid: p.chainTeam() != 0}, Again: p.Again,
		State: PagePending, Now: t.now, GatewayConnectionID: pgtype.Int8{Int64: gw.ID, Valid: true},
		RunStepSeq: pgtype.Int4{Int32: int32(p.RunStepSeq), Valid: p.RunStepSeq > 0}}) //nolint:gosec // G115: at most MaxStepsPerRun
	if err != nil {
		return fmt.Errorf("write the call of step %d on alert group #%d: %w", p.Step, g.Number, err)
	}
	for _, u := range users {
		if err := q.InsertPageUser(ctx, dbgen.InsertPageUserParams{OrgID: t.orgID, PageID: id, UserID: u.ID,
			Outcome: PagePending}); err != nil {
			return fmt.Errorf("write the person of the call of step %d on alert group #%d: %w", p.Step, g.Number, err)
		}
	}
	c.add(entry{Event: EventPaged, Variant: VariantCall, Chain: &chain,
		Page: &Page{ID: id, Users: users, Gateway: &gw, Importance: p.Importance}})
	*t.calls = append(*t.calls, queuedCall{page: id, gateway: gw.ID, user: users[0].ID, queue: p.Call.Queue})
	return nil
}

// PageOutcome is what the Thread replies of a Page by Mention did so far, which delivery reports (C-32.FR-10): the
// people a delivered reply mentioned, whether every reply ended — sent, Not delivered or withheld — and whether one
// was delivered, which delivers a Page of a Team or of everyone in the chat.
type PageOutcome struct {
	Page      int64
	Reached   []int64
	Ended     bool
	Delivered bool
}

// pageSettled is the system transition of PageReached and PageNotDelivered: the people a delivered reply mentioned
// are reached, which makes them Assignees of the Page's run (reachAssignees, C-32.FR-12); once every reply ended, the
// people still pending are not delivered, recording page_not_delivered with not_delivered, and the Page ends,
// delivered when it reached someone or, for a page_team step, when a reply was delivered. A Page that ended already
// changes nothing, so a second report of the same replies records nothing.
type pageSettled struct {
	o     PageOutcome
	orgID int64
	now   time.Time
	ended *[]pageEnd
}

func (pageSettled) precondition(*Group) error { return nil }

func (t pageSettled) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	p, err := q.LockPage(ctx, dbgen.LockPageParams{OrgID: t.orgID, ID: t.o.Page, AlertGroupID: g.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock page %d of alert group #%d: %w", t.o.Page, g.Number, err)
	}
	if p.State != PagePending || p.Kind != PageKindMention {
		return nil
	}
	if len(t.o.Reached) > 0 {
		reached, err := q.ReachPageUsers(ctx, dbgen.ReachPageUsersParams{OrgID: t.orgID, PageID: p.ID,
			UserIds: t.o.Reached})
		if err != nil {
			return fmt.Errorf("record whom page %d reached: %w", p.ID, err)
		}
		if err := reachAssignees(ctx, q, t.orgID, g, p, reached, t.now, c); err != nil {
			return err
		}
	}
	if !t.o.Ended {
		return nil
	}
	missed, err := q.MissPageUsers(ctx, dbgen.MissPageUsersParams{OrgID: t.orgID, PageID: p.ID})
	if err != nil {
		return fmt.Errorf("record whom page %d did not reach: %w", p.ID, err)
	}
	if len(missed) > 0 {
		users, err := pagedUsers(ctx, q, t.orgID, missed)
		if err != nil {
			return err
		}
		chain := ChainEntry{Run: p.EscalationRunID.Int64, Chain: p.ChainID.Int64, ChainPublicID: p.ChainPublicID,
			ChainName: p.ChainName, Step: int(p.StepNumber)}
		c.add(entry{Event: EventPageNotDelivered, Reason: PageRepliesFailed, Chain: &chain,
			Page: &Page{Users: users, Reason: PageRepliesFailed, ID: p.ID, Importance: p.Importance}})
	}
	n, err := q.CountPageUsers(ctx, dbgen.CountPageUsersParams{OrgID: t.orgID, PageID: p.ID})
	if err != nil {
		return fmt.Errorf("count the people of page %d: %w", p.ID, err)
	}
	outcome := PageNotDelivered
	if n.Delivered > 0 || (p.Team && t.o.Delivered) {
		outcome = PageDelivered
	}
	ep := dbgen.EndPageParams{OrgID: t.orgID, ID: p.ID, State: outcome, Now: t.now}
	if outcome == PageNotDelivered && n.Total > 0 {
		ep.Reason = nonEmpty(PageRepliesFailed)
	}
	if err := q.EndPage(ctx, ep); err != nil {
		return fmt.Errorf("end page %d: %w", p.ID, err)
	}
	*t.ended = append(*t.ended, pageEnd{chain: p.ChainPublicID, outcome: outcome})
	return nil
}

// CallOutcome is how a call through a Notification gateway ended, which the gateway sender reports in the transaction
// of its end (C-30.FR-4, FR-7, C-32.FR-12): the call by internal id; Accepted when the gateway accepted it; else the
// reason it ended Not delivered, or none when it was dropped, as every Alert Group of it stopped; the Alert Groups it
// was about, without those dropped before its first attempt; the person and the gateway; and when the call made its
// first attempt, zero for one dropped before it.
type CallOutcome struct {
	Call         int64
	Accepted     bool
	Reason       string
	Groups       []int64
	User         PagedUser
	Gateway      PageGateway
	FirstAttempt time.Time
}

// dropped reports whether the call ended without being made for anyone: every Alert Group of it stopped.
func (o CallOutcome) dropped() bool { return !o.Accepted && o.Reason == "" }

// callSettled is the system transition of PageReached and PageNotDelivered for the Pages of the kind call of an Alert
// Group that joined one call: accepted, the call reaches its person, which makes them an Assignee of the Page's run as
// a delivered Mention does (reachAssignees, C-32.FR-12), and the Page is delivered; Not delivered, the Page records
// page_not_delivered with its chain, its step, the person, the gateway and the reason, and leaves the Assignees as
// they were (C-30.FR-7, C-32.FR-10). A Page whose Alert Group the call dropped before its first attempt, of a call
// dropped whole, or whose own chain or rule stopped before the call ended (callStopped) — such as the earlier Page of
// a chain that started over and Paged again into the same call — ends not_delivered without a reason and records
// nothing, and is not counted. An Alert
// Group that a call Not delivered was about without a Page, as the hook of muster dev queues them, records
// page_not_delivered without a chain. A Page that ended already changes nothing.
type callSettled struct {
	o     CallOutcome
	pages []int64
	kept  bool
	orgID int64
	now   time.Time
	ended *[]pageEnd
}

func (callSettled) precondition(*Group) error { return nil }

func (t callSettled) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	gw := t.o.Gateway
	if len(t.pages) == 0 && t.kept && !t.o.Accepted && t.o.Reason != "" {
		c.add(entry{Event: EventPageNotDelivered, Reason: t.o.Reason, Page: &Page{Users: []PagedUser{t.o.User},
			Gateway: &gw, Reason: t.o.Reason}})
		return nil
	}
	for _, id := range t.pages {
		p, err := q.LockPage(ctx, dbgen.LockPageParams{OrgID: t.orgID, ID: id, AlertGroupID: g.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("lock page %d of alert group #%d: %w", id, g.Number, err)
		}
		if p.State != PagePending || p.Kind != PageKindCall {
			continue
		}
		end := dbgen.EndPageParams{OrgID: t.orgID, ID: p.ID, State: PageNotDelivered, Now: t.now}
		first := t.o.FirstAttempt
		if first.IsZero() {
			first = t.now
		}
		made := t.kept && !t.o.dropped() && !callStopped(p, first)
		switch {
		case !made:
		case t.o.Accepted:
			reached, err := q.ReachPageUsers(ctx, dbgen.ReachPageUsersParams{OrgID: t.orgID, PageID: p.ID,
				UserIds: []int64{t.o.User.ID}})
			if err != nil {
				return fmt.Errorf("record whom the call of page %d reached: %w", p.ID, err)
			}
			if err := reachAssignees(ctx, q, t.orgID, g, p, reached, t.now, c); err != nil {
				return err
			}
			end.State = PageDelivered
		default:
			missed, err := q.MissCallPageUsers(ctx, dbgen.MissCallPageUsersParams{OrgID: t.orgID, PageID: p.ID,
				Reason: nonEmpty(t.o.Reason)})
			if err != nil {
				return fmt.Errorf("record whom the call of page %d did not reach: %w", p.ID, err)
			}
			users, err := pagedUsers(ctx, q, t.orgID, missed)
			if err != nil {
				return err
			}
			if len(users) > 0 {
				chain := ChainEntry{Run: p.EscalationRunID.Int64, Chain: p.ChainID.Int64,
					ChainPublicID: p.ChainPublicID, ChainName: p.ChainName, Step: int(p.StepNumber)}
				c.add(entry{Event: EventPageNotDelivered, Reason: t.o.Reason, Chain: &chain,
					Page: &Page{Users: users, Gateway: &gw, Reason: t.o.Reason, ID: p.ID, Importance: p.Importance}})
			}
			end.Reason = nonEmpty(t.o.Reason)
		}
		if err := q.EndPage(ctx, end); err != nil {
			return fmt.Errorf("end page %d: %w", p.ID, err)
		}
		if made {
			*t.ended = append(*t.ended, pageEnd{chain: p.ChainPublicID, kind: PageKindCall, outcome: end.State})
		}
	}
	return nil
}

// callStopped reports whether the chain or the rule of the call Page p stopped at or before first, the call's first
// attempt: its run stopped then, or a later start of the chains superseded it then, as a start-over does, or its rule
// run stopped then. The call was then not made for that Page, even when a later Page of the same Alert Group kept the
// Alert Group in the call; a stop after the first attempt, such as an Acknowledge during the retries, leaves it made.
func callStopped(p dbgen.LockPageRow, first time.Time) bool {
	before := func(at pgtype.Timestamptz) bool { return at.Valid && !at.Time.After(first) }
	return (p.RunState == "stopped" && before(p.RunEndedAt)) ||
		(p.EscalationRunID.Valid && !p.RunSupersededAt.After(first)) ||
		(p.RuleRunState == "stopped" && before(p.RuleRunEndedAt))
}

// SettleCall records how the call o.Call through a Notification gateway ended for the Pages that joined it and its
// Alert Groups, in the transaction tx of the end of the call, through the dispatcher as system transitions with
// Muster as the actor (PageReached and PageNotDelivered, callSettled; C-30.FR-7, C-32.FR-12): the gateway sender
// calls it before it ends the call in the same transaction. It locks the Alert Groups in id order first, as every
// change of Alert Groups does, so the caller takes them before any lock of its own; one that no longer exists is
// skipped. A call ended, accepted or Not delivered, counts each Page it settled in muster_pages_total with the kind
// call. It returns what to run once tx committed.
func (s *Service) SettleCall(ctx context.Context, tx DBTX, o CallOutcome) (func(context.Context), error) {
	if o.Reason != "" && !slices.Contains(PageReasons, o.Reason) {
		return nil, fmt.Errorf("page_not_delivered with the reason %q", o.Reason)
	}
	if o.User.ID == 0 {
		return nil, errors.New("a call names nobody")
	}
	q := s.queries(tx)
	rows, err := q.ListCallPages(ctx, dbgen.ListCallPagesParams{OrgID: s.orgID,
		GatewayCallID: pgtype.Int8{Int64: o.Call, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("read the pages of the call %d: %w", o.Call, err)
	}
	pages := map[int64][]int64{}
	ids := slices.Clone(o.Groups)
	for _, r := range rows {
		pages[r.AlertGroupID] = append(pages[r.AlertGroupID], r.ID)
		ids = append(ids, r.AlertGroupID)
	}
	locked, err := lock(ctx, q, s.orgID, ids)
	if err != nil {
		return nil, err
	}
	var after committed
	var ended []pageEnd
	now := s.clock.Now().UTC()
	for _, id := range slices.Sorted(maps.Keys(locked)) {
		if err := s.d.dispatch(ctx, q, locked[id], System, callSettled{o: o, pages: pages[id],
			kept: slices.Contains(o.Groups, id), orgID: s.orgID, now: now, ended: &ended}, &after); err != nil {
			return nil, err
		}
	}
	countPages(&after, ended)
	return after.run, nil
}

// SettlePage records what the Thread replies of the Page o.Page of the Alert Group groupID did, in the transaction tx
// of delivery (PageReached, PageNotDelivered; C-32.FR-10): it locks the Alert Group, then the Page, and records
// through the dispatcher as a system transition with Muster as the actor. An Alert Group or a Page that no longer
// exists, and a Page that ended, change nothing. It returns what to run once tx committed. An outcome that reached
// someone, who may become an Assignee whom the footer of the firing Alert Group names (C-12.FR-12), first takes the
// lock of the evaluation of the Teams shared, as a Snapshot does (ShareTeamEvaluation): a batch of user_name or
// account_links takes it exclusively before it lists, so it waits for this outcome and then finds its Alert Group, or
// this outcome waits for the batch and renders the name and Account links after the change. The rows of the people
// are not locked: the changes of a User lock the active Admins before the User, in an order a lock of several people
// here could cross.
func (s *Service) SettlePage(ctx context.Context, tx DBTX, groupID int64, o PageOutcome) (func(context.Context),
	error) {
	q := s.queries(tx)
	if len(o.Reached) > 0 {
		if err := q.ShareTeamEvaluation(ctx, dbgen.ShareTeamEvaluationParams{LockClass: db.TeamEvaluationLockClass,
			OrgID: s.orgID}); err != nil {
			return nil, fmt.Errorf("wait for the open alert group updates in flight: %w", err)
		}
	}
	locked, err := lock(ctx, q, s.orgID, []int64{groupID})
	if err != nil {
		return nil, err
	}
	g := locked[groupID]
	if g == nil {
		return nil, nil
	}
	var after committed
	var ended []pageEnd
	if err := s.d.dispatch(ctx, q, g, System, pageSettled{o: o, orgID: s.orgID, now: s.clock.Now().UTC(),
		ended: &ended}, &after); err != nil {
		return nil, err
	}
	countPages(&after, ended)
	return after.run, nil
}
