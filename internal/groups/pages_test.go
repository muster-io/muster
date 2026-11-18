// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/metrics"
)

// fakePage is a pages row with its people.
type fakePage struct {
	dbgen.InsertPageParams
	id    int64
	users []*fakePageUser
}

// fakePageUser is a page_users row.
type fakePageUser struct {
	id      int64
	outcome string
	reason  string
}

// fakePages are the Pages of the fake database.
type fakePages struct {
	list []*fakePage
}

func (p *fakePages) byID(id int64) *fakePage {
	for _, x := range p.list {
		if x.id == id {
			return x
		}
	}
	return nil
}

func (f *fakeDB) InsertPage(_ context.Context, arg dbgen.InsertPageParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertPage"); err != nil {
		return 0, err
	}
	p := &fakePage{InsertPageParams: arg, id: f.id()}
	f.pages.list = append(f.pages.list, p)
	return p.id, nil
}

func (f *fakeDB) InsertPageUser(_ context.Context, arg dbgen.InsertPageUserParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("InsertPageUser"); err != nil {
		return err
	}
	p := f.pages.byID(arg.PageID)
	p.users = append(p.users, &fakePageUser{id: arg.UserID, outcome: arg.Outcome, reason: arg.Reason.String})
	return nil
}

func (f *fakeDB) LockPage(_ context.Context, arg dbgen.LockPageParams) (dbgen.LockPageRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockPage"); err != nil {
		return dbgen.LockPageRow{}, err
	}
	p := f.pages.byID(arg.ID)
	if p == nil || p.AlertGroupID != arg.AlertGroupID || arg.OrgID != orgID {
		return dbgen.LockPageRow{}, pgx.ErrNoRows
	}
	return dbgen.LockPageRow{ID: p.id, AlertGroupID: p.AlertGroupID, State: p.State, StepNumber: p.StepNumber,
		Team: p.TeamID.Valid || p.Everyone.Valid, Importance: p.Importance.String, EscalationRunID: p.EscalationRunID,
		ChainID: pgtype.Int8{Int64: 5, Valid: true}, ChainPublicID: "EC0000000000PA", ChainName: "payments"}, nil
}

// pageUsers marks the people of the Page pending among ids, all when ids is nil, with the outcome, and returns them.
func (f *fakeDB) pageUsers(name string, page int64, ids []int64, outcome, reason string) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call(name); err != nil {
		return nil, err
	}
	var out []int64
	for _, u := range f.pages.byID(page).users {
		if u.outcome == PagePending && (ids == nil || slices.Contains(ids, u.id)) {
			u.outcome, u.reason = outcome, reason
			out = append(out, u.id)
		}
	}
	return out, nil
}

func (f *fakeDB) ReachPageUsers(_ context.Context, arg dbgen.ReachPageUsersParams) ([]int64, error) {
	return f.pageUsers("ReachPageUsers", arg.PageID, arg.UserIds, PageDelivered, "")
}

func (f *fakeDB) MissPageUsers(_ context.Context, arg dbgen.MissPageUsersParams) ([]int64, error) {
	return f.pageUsers("MissPageUsers", arg.PageID, nil, PageNotDelivered, PageRepliesFailed)
}

func (f *fakeDB) CountPageUsers(_ context.Context, arg dbgen.CountPageUsersParams) (dbgen.CountPageUsersRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("CountPageUsers"); err != nil {
		return dbgen.CountPageUsersRow{}, err
	}
	var out dbgen.CountPageUsersRow
	for _, u := range f.pages.byID(arg.PageID).users {
		out.Total++
		if u.outcome == PageDelivered {
			out.Delivered++
		}
	}
	return out, nil
}

func (f *fakeDB) EndPage(_ context.Context, arg dbgen.EndPageParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("EndPage"); err != nil {
		return err
	}
	if p := f.pages.byID(arg.ID); p != nil && p.State == PagePending {
		p.State, p.Reason, p.EndedAt = arg.State, arg.Reason, pgtype.Timestamptz{Time: arg.Now, Valid: true}
	}
	return nil
}

// fakeTargets are where the Pages go, by the request.
type fakeTargets struct {
	targets []PageTarget
	err     error
	reqs    []PageRequest
}

func (f *fakeTargets) PageTargets(_ context.Context, _ DBTX, _ *Group, r PageRequest) ([]PageTarget, error) {
	f.reqs = append(f.reqs, r)
	return slices.Clone(f.targets), f.err
}

// pageHarness is a harness with the people, the chains, a published firing Alert Group, where Pages go, and the
// renderings the dispatcher hands to delivery.
func pageHarness(t *testing.T) (*harness, *dbgen.LockGroupsRow, *fakeTargets, *[]Rendering) {
	t.Helper()
	h := newHarness(t)
	h.people()
	h.withChains()
	g := h.firing(t, "x")
	h.publish(t, g, t0)
	targets := &fakeTargets{}
	h.svc.SetPageTargets(targets)
	var renders []Rendering
	h.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
		renders = append(renders, r)
		return nil
	})
	return h, g, targets, &renders
}

// step makes one ChainStep of g with the progress p.
func (h *harness) step(t *testing.T, g *dbgen.LockGroupsRow, p ChainProgress) error {
	t.Helper()
	after, err := h.svc.ChainStep(t.Context(), nil, g.ID, func(context.Context, *Group) (ChainProgress, error) {
		return p, nil
	})
	if err == nil && after != nil {
		after(t.Context())
	}
	return err
}

// escalationEntries are the entries of g of the kind escalation as event/reason/people.
func (h *harness) escalationEntries(g *dbgen.LockGroupsRow) []string {
	var out []string
	for _, e := range h.entriesOf(g) {
		if e.Kind != string(KindEscalation) {
			continue
		}
		s := e.Event.String + "/" + e.Reason.String
		for _, id := range e.PagedUserIds {
			s += "/" + map[int64]string{owner: "alice", bobID: "bob", carolID: "carol"}[id]
		}
		out = append(out, s)
	}
	return out
}

var paymentsStep = ChainEntry{Run: runID, Chain: 5, ChainPublicID: "EC0000000000PA", ChainName: "payments", Step: 1}

// TestPagePeople is C-32.FR-9, FR-10, FR-19 and C-32.AC-6, AC-11 for a Page of people: one Loud paged with the
// Mention paged, its people, its importance and its Destinations for delivery, and page_not_delivered no_account_link
// for the person linked in none of them; delivered replies reach the linked person, and once every reply ended the
// Page is delivered and counted.
func TestPagePeople(t *testing.T) {
	h, g, targets, renders := pageHarness(t)
	targets.targets = []PageTarget{{DestinationID: 11, Source: PageSourceRoute, Linked: []int64{owner}},
		{DestinationID: 12, Source: PageSourceTeam}}
	before := metrics.Pages.With("EC0000000000PA", "mention", "delivered").Get()
	err := h.step(t, g, ChainProgress{Changed: true, Pages: []StepPage{{ChainEntry: paymentsStep,
		Sources: []PageSource{{Kind: PageSourceRoute}, {Kind: PageSourceTeam, TeamID: 3}}, People: []int64{owner, bobID},
		Importance: "important", Schedule: 8, AgainSeconds: 300}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.escalationEntries(g); !slices.Equal(got, []string{"paged//alice/bob", "page_not_delivered/no_account_link/bob"}) {
		t.Fatalf("entries %v", got)
	}
	var paged *fakeEntry
	for _, e := range h.entriesOf(g) {
		if e.Event.String == string(EventPaged) {
			paged = e
		}
	}
	if paged.Loudness.String != "loud" || !slices.Equal(paged.Mentions, []string{"paged"}) ||
		paged.PageImportance.String != "important" || paged.EscalationRunID.Int64 != runID || paged.StepNumber.Int32 != 1 {
		t.Errorf("paged %+v", paged)
	}
	if len(targets.reqs) != 1 || !slices.Equal(targets.reqs[0].People, []int64{owner, bobID}) ||
		len(targets.reqs[0].Sources) != 2 {
		t.Errorf("requests %+v", targets.reqs)
	}
	last := (*renders)[len(*renders)-1]
	if len(last.Events) != 2 || last.Events[0].Event != EventPaged || last.Events[0].Page == nil ||
		len(last.Events[0].Page.Targets) != 2 || last.Events[0].Page.ID == 0 {
		t.Fatalf("rendering %+v", last.Events)
	}
	p := h.db.pages.list[0]
	if p.State != PagePending || p.Importance.String != "important" || p.ScheduleID.Int64 != 8 ||
		p.AgainSeconds.Int32 != 300 || p.ChainTeamID.Int64 != 3 || len(p.users) != 2 || p.users[0].outcome != PagePending ||
		p.users[1].outcome != PageNotDelivered || p.users[1].reason != PageNoAccountLink {
		t.Fatalf("page %+v %+v %+v", p, p.users[0], p.users[1])
	}
	settle := func(o PageOutcome) {
		t.Helper()
		after, err := h.svc.SettlePage(t.Context(), nil, g.ID, o)
		if err != nil {
			t.Fatal(err)
		}
		after(t.Context())
	}
	settle(PageOutcome{Page: p.id, Reached: []int64{owner}})
	if p.users[0].outcome != PageDelivered || p.State != PagePending {
		t.Errorf("reached %+v %+v", p, p.users[0])
	}
	settle(PageOutcome{Page: p.id, Reached: []int64{owner}, Ended: true, Delivered: true})
	settle(PageOutcome{Page: p.id, Ended: true})
	if p.State != PageDelivered || p.Reason.Valid {
		t.Errorf("ended %+v", p)
	}
	if got := h.escalationEntries(g); len(got) != 2 {
		t.Errorf("entries after the outcome %v", got)
	}
	if n := metrics.Pages.With("EC0000000000PA", "mention", "delivered").Get() - before; n != 1 {
		t.Errorf("muster_pages_total delivered +%d", n)
	}
}

// TestMentionPageNotDelivered is C-32.FR-10 and C-32.AC-7: a Page without any Destination records
// page_not_delivered no_root_message for all its people and no paged; one whose replies are all withheld records
// not_delivered; one whose pending replies all end without reaching its people records not_delivered once they ended.
func TestMentionPageNotDelivered(t *testing.T) {
	h, g, targets, _ := pageHarness(t)
	before := metrics.Pages.With("EC0000000000PA", "mention", "not_delivered").Get()
	page := StepPage{ChainEntry: paymentsStep, People: []int64{owner, bobID}, Importance: "normal",
		Sources: []PageSource{{Kind: PageSourceRoute}}}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{page}}); err != nil {
		t.Fatal(err)
	}
	if got := h.escalationEntries(g); !slices.Equal(got, []string{"page_not_delivered/no_root_message/alice/bob"}) {
		t.Errorf("no destination: %v", got)
	}
	if p := h.db.pages.list[0]; p.State != PageNotDelivered || p.Reason.String != PageNoRootMessage {
		t.Errorf("page %+v", p)
	}
	targets.targets = []PageTarget{{DestinationID: 11, Linked: []int64{owner, bobID}, Withheld: true}}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{page}}); err != nil {
		t.Fatal(err)
	}
	if got := h.escalationEntries(g); !slices.Equal(got[1:], []string{"paged//alice/bob",
		"page_not_delivered/not_delivered/alice/bob"}) {
		t.Errorf("withheld: %v", got)
	}
	if p := h.db.pages.list[1]; p.State != PageNotDelivered || p.Reason.String != PageRepliesFailed {
		t.Errorf("withheld page %+v", p)
	}
	targets.targets = []PageTarget{{DestinationID: 11, Linked: []int64{owner}}}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{page}}); err != nil {
		t.Fatal(err)
	}
	p := h.db.pages.list[2]
	after, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: p.id, Ended: true})
	if err != nil {
		t.Fatal(err)
	}
	after(t.Context())
	if got := h.escalationEntries(g); !slices.Equal(got[3:], []string{"paged//alice/bob",
		"page_not_delivered/no_account_link/bob", "page_not_delivered/not_delivered/alice"}) {
		t.Errorf("replies not delivered: %v", got)
	}
	if p.State != PageNotDelivered || p.Reason.String != PageRepliesFailed {
		t.Errorf("page %+v", p)
	}
	if n := metrics.Pages.With("EC0000000000PA", "mention", "not_delivered").Get() - before; n != 3 {
		t.Errorf("muster_pages_total not_delivered +%d", n)
	}
	// A Page of a chain deleted while it ran is not counted: the chain has no public_id left to name it by.
	deleted := metrics.Pages.With("", "mention", "not_delivered").Get()
	targets.targets = nil
	gone := page
	gone.Chain, gone.ChainPublicID = 0, ""
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{gone}}); err != nil {
		t.Fatal(err)
	}
	if n := metrics.Pages.With("", "mention", "not_delivered").Get() - deleted; n != 0 {
		t.Errorf("muster_pages_total of a deleted chain +%d", n)
	}
}

// TestPageNamedByThePlainName is C-32.FR-10 and FR-11 for a bridge without native_mentions: a person whom every reply
// names by the plain name, which reaches nobody, records page_not_delivered not_delivered at once, not
// no_account_link, while a person linked in another Destination stays pending; the page_not_delivered of a step,
// at once or once the replies ended, carries the importance of the Page as paged does.
func TestPageNamedByThePlainName(t *testing.T) {
	h, g, targets, renders := pageHarness(t)
	targets.targets = []PageTarget{{DestinationID: 11, Source: PageSourceRoute, Named: []int64{owner, bobID}},
		{DestinationID: 12, Source: PageSourceRoute, Linked: []int64{bobID}}}
	err := h.step(t, g, ChainProgress{Changed: true, Pages: []StepPage{{ChainEntry: paymentsStep,
		Sources: []PageSource{{Kind: PageSourceRoute}}, People: []int64{owner, bobID}, Importance: "important"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.escalationEntries(g); !slices.Equal(got, []string{"paged//alice/bob",
		"page_not_delivered/not_delivered/alice"}) {
		t.Fatalf("entries %v", got)
	}
	p := h.db.pages.list[0]
	if p.State != PagePending || p.users[0].outcome != PageNotDelivered || p.users[0].reason != PageRepliesFailed ||
		p.users[1].outcome != PagePending {
		t.Fatalf("page %+v %+v %+v", p, p.users[0], p.users[1])
	}
	importance := func(r Rendering) []string {
		var out []string
		for _, e := range r.Events {
			if e.Page != nil {
				out = append(out, string(e.Event)+"/"+e.Page.Importance)
			}
		}
		return out
	}
	if got := importance((*renders)[len(*renders)-1]); !slices.Equal(got, []string{"paged/important",
		"page_not_delivered/important"}) {
		t.Errorf("importance %v", got)
	}
	after, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: p.id, Ended: true, Delivered: true})
	if err != nil {
		t.Fatal(err)
	}
	after(t.Context())
	if got := importance((*renders)[len(*renders)-1]); !slices.Equal(got, []string{"page_not_delivered/important"}) {
		t.Errorf("importance once the replies ended %v", got)
	}
}

// TestPageTeam is C-32.FR-9 and C-32.AC-13 for a page_team step: none of its chain's Destinations can mention the
// Team, and it records escalation_step_skipped no_mention_possible; otherwise paged with the Team, delivered once a
// reply was; a token for everyone in the chat is recorded in page_everyone, and not delivered when no reply was.
func TestPageTeam(t *testing.T) {
	h, g, targets, _ := pageHarness(t)
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{{ChainEntry: paymentsStep, Team: 3}}}); err != nil {
		t.Fatal(err)
	}
	if got := h.escalationEntries(g); !slices.Equal(got, []string{"escalation_step_skipped/no_mention_possible"}) {
		t.Errorf("no destination: %v", got)
	}
	targets.targets = []PageTarget{{DestinationID: 11, Source: PageSourceTeam}}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{{ChainEntry: paymentsStep, Team: 3},
		{ChainEntry: paymentsStep, Everyone: "here", Seq: 1}}}); err != nil {
		t.Fatal(err)
	}
	var team, here *fakeEntry
	for _, e := range h.entriesOf(g) {
		switch {
		case e.Event.String == string(EventPaged) && e.TeamID.Valid:
			team = e
		case e.Event.String == string(EventPaged):
			here = e
		}
	}
	if team == nil || team.TeamID.Int64 != 3 || here == nil || here.PageEveryone.String != "here" {
		t.Fatalf("team %+v here %+v", team, here)
	}
	for i, delivered := range []bool{true, false} {
		p := h.db.pages.list[i]
		after, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: p.id, Ended: true, Delivered: delivered})
		if err != nil {
			t.Fatal(err)
		}
		after(t.Context())
	}
	if h.db.pages.list[0].State != PageDelivered || h.db.pages.list[1].State != PageNotDelivered ||
		h.db.pages.list[1].Reason.Valid {
		t.Errorf("pages %+v %+v", h.db.pages.list[0], h.db.pages.list[1])
	}
}

// TestPagesInStepOrder: the skips, webhooks and Pages of one firing are recorded in the order of their steps.
func TestPagesInStepOrder(t *testing.T) {
	h, g, targets, _ := pageHarness(t)
	targets.targets = []PageTarget{{DestinationID: 11, Linked: []int64{owner}}}
	step := func(n int) ChainEntry { e := paymentsStep; e.Step = n; return e }
	err := h.step(t, g, ChainProgress{
		Skips: []StepSkip{{ChainEntry: step(1), Reason: SkipSeverity}, {ChainEntry: step(4), Reason: SkipSeverity}},
		Webhooks: []StepWebhook{{ChainEntry: step(3), After: 1, Seq: 1,
			Destination: JoinedDestination{ID: 40, PublicID: "DSAAAAAAAAAA40", Name: "hook", Type: "webhook"}}},
		Pages: []StepPage{{ChainEntry: step(2), People: []int64{owner}, After: 1, Seq: 0},
			{ChainEntry: step(5), People: []int64{owner}, After: 2, Seq: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range h.entriesOf(g) {
		if e.Kind == string(KindEscalation) {
			got = append(got, e.Event.String+"@"+strconv.Itoa(int(e.StepNumber.Int32)))
		}
	}
	want := []string{"escalation_step_skipped@1", "paged@2", "escalation_webhook@3", "escalation_step_skipped@4",
		"paged@5"}
	if !slices.Equal(got, want) {
		t.Errorf("order %v", got)
	}
}

// TestPageFailures: a Page that names nobody is refused, a Page with only unknown people records nothing, a failure
// of where Pages go or of writing the Page fails the step, and without PageTargets a Page is left out; a Page that
// is gone or ended settles nothing, and a failure of the settle fails it.
func TestPageFailures(t *testing.T) {
	h, g, targets, _ := pageHarness(t)
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{{ChainEntry: paymentsStep}}}); err == nil ||
		!strings.Contains(err.Error(), "names nobody") {
		t.Errorf("nobody: %v", err)
	}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{{ChainEntry: paymentsStep, People: []int64{999}}}}); err != nil ||
		len(h.escalationEntries(g)) != 0 || len(h.db.pages.list) != 0 {
		t.Errorf("unknown people: %v %v", err, h.escalationEntries(g))
	}
	errBoom := errors.New("boom")
	targets.err = errBoom
	page := StepPage{ChainEntry: paymentsStep, People: []int64{owner}}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{page}}); !errors.Is(err, errBoom) {
		t.Errorf("targets: %v", err)
	}
	targets.err = nil
	targets.targets = []PageTarget{{DestinationID: 11, Linked: []int64{owner}}}
	for _, name := range []string{"ListUserRefs", "InsertPage", "InsertPageUser"} {
		h.db.fail[name] = errBoom
		if err := h.step(t, g, ChainProgress{Pages: []StepPage{page}}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
		delete(h.db.fail, name)
	}
	h.svc.SetPageTargets(nil)
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{page}}); err != nil {
		t.Errorf("without targets: %v", err)
	}
	h.svc.SetPageTargets(targets)
	n := len(h.db.pages.list)
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{page}}); err != nil {
		t.Fatal(err)
	}
	p := h.db.pages.list[n]
	if after, err := h.svc.SettlePage(t.Context(), nil, 9999, PageOutcome{Page: p.id}); err != nil || after != nil {
		t.Errorf("a missing alert group: %v", err)
	}
	if _, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: 9999, Ended: true}); err != nil {
		t.Errorf("a missing page: %v", err)
	}
	// The fake does not roll back: each failure starts from the pending Page again.
	reset := func() { p.State, p.users[0].outcome, p.users[0].reason = PagePending, PagePending, "" }
	for _, name := range []string{"LockPage", "ReachPageUsers", "MissPageUsers", "CountPageUsers", "EndPage"} {
		h.db.fail[name] = errBoom
		if _, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: p.id, Reached: []int64{bobID},
			Ended: true}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
		delete(h.db.fail, name)
		reset()
	}
	h.db.fail["LockGroups"] = errBoom
	if _, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: p.id}); !errors.Is(err, errBoom) {
		t.Errorf("lock: %v", err)
	}
	delete(h.db.fail, "LockGroups")
	h.db.fail["ListUserRefs"] = errBoom
	if _, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: p.id, Ended: true}); !errors.Is(err, errBoom) {
		t.Errorf("names: %v", err)
	}
	delete(h.db.fail, "ListUserRefs")
	reset()
	p.State = PageDelivered
	if _, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: p.id, Ended: true}); err != nil ||
		p.users[0].outcome != PagePending {
		t.Errorf("an ended page: %v %+v", err, p.users[0])
	}
}

// TestChainTeamOfPage: "Chain of the Team" names the owning Team the run had, else the first Team that named it.
func TestChainTeamOfPage(t *testing.T) {
	for _, c := range []struct {
		p    StepPage
		want int64
	}{
		{StepPage{OwningTeam: 4, Sources: []PageSource{{Kind: PageSourceTeam, TeamID: 3}}}, 4},
		{StepPage{Sources: []PageSource{{Kind: PageSourceRoute}, {Kind: PageSourceTeam, TeamID: 3}}}, 3},
		{StepPage{Sources: []PageSource{{Kind: PageSourceRoute}}}, 0},
	} {
		if got := c.p.chainTeam(); got != c.want {
			t.Errorf("%+v: %d", c.p, got)
		}
	}
}
