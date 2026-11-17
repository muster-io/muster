// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/metrics"
)

// Page is a Page as the Timeline entry of an event of the kind escalation names it (C-32.FR-19): the people it names,
// the Notification gateway of a call, nil for a Page by Mention, and the reason a Page was not delivered. groups knows
// no package of Paging (S-169); the callers fill it. A paged by Mention also carries its pages row, the Destinations
// its Thread replies go to, which delivery fans the event out into (C-32.FR-8), the Team or the token of a page_team
// step and the importance of a Page of people.
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
// could mention them ended Not delivered or was withheld while its Destination was Broken (C-32.FR-10).
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

// pageNotDelivered records the Quiet page_not_delivered of a Page on an Alert Group, whatever its status: a system
// transition that changes no status.
type pageNotDelivered struct {
	page *Page
}

func (pageNotDelivered) precondition(*Group) error { return nil }

func (t pageNotDelivered) apply(_ context.Context, _ Queries, _ *Group, c *change) error {
	c.add(entry{Event: EventPageNotDelivered, Reason: t.page.Reason, Page: t.page})
	return nil
}

// PageNotDelivered records page_not_delivered on each of the Alert Groups ids, through the dispatcher as a system
// transition in the transaction tx of its caller (C-30.FR-7, C-09.FR-22): the gateway sender, for a call that ended
// Not delivered, before it ends the call in the same transaction. The Alert Groups are locked in id order first, as
// every change of Alert Groups locks them, so the caller takes them before any lock of its own; one that no longer
// exists is skipped. It returns what to run once tx committed.
func (s *Service) PageNotDelivered(ctx context.Context, tx DBTX, ids []int64, p Page) (func(context.Context),
	error) {
	if !slices.Contains(PageReasons, p.Reason) {
		return nil, fmt.Errorf("page_not_delivered with the reason %q", p.Reason)
	}
	if len(p.Users) == 0 {
		return nil, errors.New("page_not_delivered names nobody")
	}
	q := s.queries(tx)
	locked, err := lock(ctx, q, s.orgID, ids)
	if err != nil {
		return nil, err
	}
	var after committed
	for _, id := range slices.Sorted(maps.Keys(locked)) {
		page := p
		if err := s.d.dispatch(ctx, q, locked[id], System, pageNotDelivered{page: &page}, &after); err != nil {
			return nil, err
		}
	}
	return after.run, nil
}

// pageQueries are the queries of the Pages by Mention.
type pageQueries interface {
	InsertPage(ctx context.Context, arg dbgen.InsertPageParams) (int64, error)
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
// its Identity space, whom its reply mentions, and whether its reply is withheld, as while it is Broken (C-11.FR-19).
type PageTarget struct {
	DestinationID int64
	Source        string
	Linked        []int64
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
// place among the skips and the webhooks of the same ChainProgress, which keep the order of the steps.
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

// The outcomes of a Page and of a person it names, and the kind of a Page by Mention.
const (
	PagePending      = "pending"
	PageDelivered    = "delivered"
	PageNotDelivered = "not_delivered"
	PageKindMention  = "mention"
)

// pageEnd is a Page that ended, for muster_pages_total once the transaction committed.
type pageEnd struct {
	chain   string
	outcome string
}

// countPages counts the Pages that ended once the transaction committed (C-32.FR-19); a Page whose chain was deleted
// is not counted, as its chain no longer has a public_id to name it by.
func countPages(after *committed, ended []pageEnd) {
	for _, e := range ended {
		if e.chain == "" {
			continue
		}
		after.add(func(context.Context) { metrics.Pages.With(e.chain, PageKindMention, e.outcome).Inc() })
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
// not_delivered when every reply that would mention them is withheld; pending otherwise, until the replies end.
func outcomeOf(id int64, targets []PageTarget) (string, string) {
	if len(targets) == 0 {
		return PageNotDelivered, PageNoRootMessage
	}
	linked, open := false, false
	for _, t := range targets {
		if slices.Contains(t.Linked, id) {
			linked = true
			open = open || !t.Withheld
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
		Again:        p.Again, State: PagePending, Now: t.now}
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
				Page: &Page{Users: pagedOf(users, ids), Reason: reason, ID: id}})
		}
	}
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
// are reached; once every reply ended, the people still pending are not delivered, recording page_not_delivered with
// not_delivered, and the Page ends, delivered when it reached someone or, for a page_team step, when a reply was
// delivered. A Page that ended already changes nothing, so a second report of the same replies records nothing.
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
	if p.State != PagePending {
		return nil
	}
	if len(t.o.Reached) > 0 {
		if _, err := q.ReachPageUsers(ctx, dbgen.ReachPageUsersParams{OrgID: t.orgID, PageID: p.ID,
			UserIds: t.o.Reached}); err != nil {
			return fmt.Errorf("record whom page %d reached: %w", p.ID, err)
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
			Page: &Page{Users: users, Reason: PageRepliesFailed, ID: p.ID}})
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

// SettlePage records what the Thread replies of the Page o.Page of the Alert Group groupID did, in the transaction tx
// of delivery (PageReached, PageNotDelivered; C-32.FR-10): it locks the Alert Group first, as every change of it does,
// then the Page, and records through the dispatcher as a system transition with Muster as the actor. An Alert Group
// or a Page that no longer exists, and a Page that ended, change nothing. It returns what to run once tx committed.
func (s *Service) SettlePage(ctx context.Context, tx DBTX, groupID int64, o PageOutcome) (func(context.Context),
	error) {
	q := s.queries(tx)
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
