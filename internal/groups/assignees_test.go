// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/metrics"
)

// fakeAssignee is an alert_group_assignees row.
type fakeAssignee struct {
	group, run, user int64
	step             int32
	seq              pgtype.Int4
	team             pgtype.Int8
	since            time.Time
}

func (f *fakeDB) ListRunAssignees(_ context.Context, arg dbgen.ListRunAssigneesParams) ([]dbgen.ListRunAssigneesRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListRunAssignees"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRunAssigneesRow
	for _, a := range f.pages.assignees {
		if a.group == arg.AlertGroupID && a.run == arg.EscalationRunID && arg.OrgID == orgID {
			out = append(out, dbgen.ListRunAssigneesRow{UserID: a.user, RunStepSeq: a.seq})
		}
	}
	return out, nil
}

func (f *fakeDB) UpsertAssignee(_ context.Context, arg dbgen.UpsertAssigneeParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("UpsertAssignee"); err != nil {
		return err
	}
	for _, a := range f.pages.assignees {
		if a.group == arg.AlertGroupID && a.run == arg.EscalationRunID && a.user == arg.UserID {
			a.step, a.seq = arg.StepNumber, arg.RunStepSeq
			return nil
		}
	}
	f.pages.assignees = append(f.pages.assignees, &fakeAssignee{group: arg.AlertGroupID, run: arg.EscalationRunID,
		user: arg.UserID, step: arg.StepNumber, seq: arg.RunStepSeq, team: arg.TeamID, since: arg.Since})
	return nil
}

// deleteAssignees removes the rows that drop selects and counts them.
func (f *fakeDB) deleteAssignees(name string, drop func(a *fakeAssignee) bool) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call(name); err != nil {
		return 0, err
	}
	n := len(f.pages.assignees)
	f.pages.assignees = slices.DeleteFunc(f.pages.assignees, drop)
	return int64(n - len(f.pages.assignees)), nil
}

func (f *fakeDB) DeleteRunAssigneesExcept(_ context.Context, arg dbgen.DeleteRunAssigneesExceptParams) (int64, error) {
	return f.deleteAssignees("DeleteRunAssigneesExcept", func(a *fakeAssignee) bool {
		return a.group == arg.AlertGroupID && a.run == arg.EscalationRunID && !slices.Contains(arg.Keep, a.user)
	})
}

func (f *fakeDB) DeleteGroupAssignees(_ context.Context, arg dbgen.DeleteGroupAssigneesParams) (int64, error) {
	return f.deleteAssignees("DeleteGroupAssignees", func(a *fakeAssignee) bool {
		return a.group == arg.AlertGroupID && arg.OrgID == orgID
	})
}

func (f *fakeDB) ListGroupAssignees(_ context.Context, arg dbgen.ListGroupAssigneesParams) (
	[]dbgen.ListGroupAssigneesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupAssignees"); err != nil {
		return nil, err
	}
	var out []dbgen.ListGroupAssigneesRow
	for _, a := range f.pages.assignees {
		if slices.Contains(arg.AlertGroupIds, a.group) && arg.OrgID == orgID && f.groups[a.group] != nil &&
			f.groups[a.group].Status == string(StatusFiring) {
			out = append(out, dbgen.ListGroupAssigneesRow{AlertGroupID: a.group, EscalationRunID: a.run,
				UserID: a.user, TeamID: a.team, Since: a.since})
		}
	}
	slices.SortFunc(out, func(a, b dbgen.ListGroupAssigneesRow) int {
		return cmp.Or(cmp.Compare(a.AlertGroupID, b.AlertGroupID), cmp.Compare(a.EscalationRunID, b.EscalationRunID),
			a.Since.Compare(b.Since), cmp.Compare(a.UserID, b.UserID))
	})
	return out, nil
}

// The second run of the Assignee tests, of a chain owned by the Team devops, and the Teams of the runs.
const (
	devopsRun  = 90
	devopsTeam = 31
	payTeam    = 32
)

// assigneeHarness is a page harness whose runs runID (payments) and devopsRun have the owning Teams payments and
// devops, where every person is linked in the Destination of the Pages.
func assigneeHarness(t *testing.T) (*harness, *dbgen.LockGroupsRow, *[]Rendering) {
	t.Helper()
	h, g, targets, renders := pageHarness(t)
	targets.targets = []PageTarget{{DestinationID: 11, Linked: []int64{owner, bobID, carolID}}}
	h.db.pages.owningTeams = map[int64]int64{runID: payTeam, devopsRun: devopsTeam}
	h.db.teamsOf().refs[payTeam] = dbgen.ListTeamRefsRow{ID: payTeam, PublicID: "TM0000000000PA", Name: "Payments",
		Slug: "payments"}
	h.db.teamsOf().refs[devopsTeam] = dbgen.ListTeamRefsRow{ID: devopsTeam, PublicID: "TM0000000000DV",
		Name: "DevOps", Slug: "devops"}
	return h, g, renders
}

// page makes one Page of people of the step of the run on g and returns its id: its first Mentions, made by the run
// step whose seq is the step's number, or, for a rule run other than 0, a later Mention of that rule run, whose run
// step ruleSeqs gives.
func (h *harness) page(t *testing.T, g *dbgen.LockGroupsRow, run int64, step int, rule int64, people ...int64) int64 {
	t.Helper()
	return h.pageAt(t, g, run, step, step, rule, people...)
}

// pageAt is page with the seq of the run step that made the first Mentions, 0 for none, as before migration 0050.
func (h *harness) pageAt(t *testing.T, g *dbgen.LockGroupsRow, run int64, step, seq int, rule int64,
	people ...int64) int64 {
	t.Helper()
	p := StepPage{ChainEntry: ChainEntry{Run: run, Chain: 5, ChainPublicID: "EC0000000000PA", ChainName: "payments",
		Step: step}, People: people, Importance: "normal", RunStepSeq: seq}
	if rule != 0 {
		p.RuleRun, p.Again, p.RunStepSeq = rule, true, 0
	}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{p}}); err != nil {
		t.Fatal(err)
	}
	return h.db.pages.list[len(h.db.pages.list)-1].id
}

// reach reports that the delivered replies of the Page id mentioned the people.
func (h *harness) reach(t *testing.T, g *dbgen.LockGroupsRow, id int64, people ...int64) {
	t.Helper()
	h.clock.Advance(time.Second)
	after, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: id, Reached: people})
	if err != nil {
		t.Fatal(err)
	}
	after(t.Context())
}

// assignees are the Assignees of g as run:person.
func (h *harness) assignees(g *dbgen.LockGroupsRow) []string {
	var out []string
	for _, a := range h.db.pages.assignees {
		if a.group == g.ID {
			out = append(out, map[int64]string{runID: "pay", devopsRun: "dev"}[a.run]+":"+
				map[int64]string{owner: "alice", bobID: "bob", carolID: "carol"}[a.user])
		}
	}
	slices.Sort(out)
	return out
}

// changes are the assignees_changed entries of g with the people after each change.
func (h *harness) assigneeChanges(t *testing.T, g *dbgen.LockGroupsRow) []string {
	t.Helper()
	var out []string
	for _, e := range h.entriesOf(g) {
		if e.Event.String != string(EventAssigneesChanged) {
			continue
		}
		var recs []assigneeRecord
		if err := json.Unmarshal(e.Assignees, &recs); err != nil {
			t.Fatal(err)
		}
		var people []string
		for _, r := range recs {
			people = append(people, map[int64]string{owner: "alice", bobID: "bob", carolID: "carol"}[r.UserID])
		}
		// A Page's outcome records it as Muster; Acknowledge, Snooze and Resolve in their change, as their actor.
		if e.Kind != string(KindEscalation) || e.Loudness.String != string(Quiet) || len(e.Mentions) != 0 ||
			(len(recs) > 0 && e.ActorKind != "system") {
			t.Errorf("assignees_changed %+v", e.InsertTimelineEntryParams)
		}
		out = append(out, strings.Join(people, ","))
	}
	return out
}

// TestAssigneesByPages is C-32.FR-12, C-32.AC-6 and C-32.AC-11 for the outcomes of Pages by Mention: a delivered
// reply makes its people Assignees of their chain with its owning Team, each chain on its own; another reply of the
// same step and a later Mention of that step's rules add; a later step that reaches someone replaces; a late reply of
// an earlier step, a later Mention of an earlier step's rule, a step that reaches nobody, a page_team step, a person
// not delivered, a stopped run and an Alert Group no longer firing change nothing. Each change records the Quiet
// assignees_changed with the Assignees after it.
func TestAssigneesByPages(t *testing.T) {
	h, g, renders := assigneeHarness(t)
	// The rule runs 71 and 72 started with steps 1 and 2, their run steps 1 and 2.
	h.db.pages.ruleSeqs = map[int64]int32{71: 1, 72: 2}
	step1 := h.page(t, g, runID, 1, 0, owner, bobID, carolID)
	h.reach(t, g, step1, owner)
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:alice"}) {
		t.Fatalf("after the first reply: %v", got)
	}
	last := (*renders)[len(*renders)-1]
	if len(last.Events) != 1 || last.Events[0].Event != EventAssigneesChanged || last.Events[0].Loudness != Quiet {
		t.Errorf("rendering %+v", last.Events)
	}
	// The same reply settled again reaches nobody new, and the second chain has Assignees of its own.
	h.reach(t, g, step1, owner)
	dev1 := h.page(t, g, devopsRun, 1, 0, carolID)
	h.reach(t, g, dev1, carolID)
	// Another reply of the same step adds bob.
	h.reach(t, g, step1, bobID)
	if got := h.assignees(g); !slices.Equal(got, []string{"dev:carol", "pay:alice", "pay:bob"}) {
		t.Fatalf("two chains: %v", got)
	}
	if a := h.db.pages.assignees[0]; a.team.Int64 != payTeam || a.step != 1 || a.seq.Int32 != 1 {
		t.Errorf("row %+v", a)
	}
	// Step 2 of payments reaches carol: she replaces alice and bob in that chain alone.
	step2 := h.page(t, g, runID, 2, 0, carolID, owner)
	h.reach(t, g, step2, carolID)
	if got := h.assignees(g); !slices.Equal(got, []string{"dev:carol", "pay:carol"}) {
		t.Fatalf("a later step: %v", got)
	}
	// A late reply of step 1 and a later Mention of the rule step 1 started change nothing: step 2 replaced them.
	h.reach(t, g, step1, carolID)
	again1 := h.page(t, g, runID, 1, 71, bobID)
	h.reach(t, g, again1, bobID)
	if got := h.assignees(g); !slices.Equal(got, []string{"dev:carol", "pay:carol"}) {
		t.Fatalf("a reach by step 1 after step 2: %v", got)
	}
	// Step 2 reaching alice later adds her, and a later Mention of the rule step 2 started adds bob.
	h.reach(t, g, step2, owner)
	again2 := h.page(t, g, runID, 2, 72, bobID)
	h.reach(t, g, again2, bobID)
	if got := h.assignees(g); !slices.Equal(got, []string{"dev:carol", "pay:alice", "pay:bob", "pay:carol"}) {
		t.Fatalf("a rule's later mention: %v", got)
	}
	// Step 3 reaches none of its people: a reply that ended without mentioning anyone leaves them.
	step3 := h.page(t, g, runID, 3, 0, owner)
	after, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: step3, Ended: true})
	if err != nil {
		t.Fatal(err)
	}
	after(t.Context())
	// A page_team step changes nothing, even delivered.
	targets := &fakeTargets{targets: []PageTarget{{DestinationID: 11}}}
	h.svc.SetPageTargets(targets)
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{{ChainEntry: ChainEntry{Run: runID, Step: 4},
		Team: 3}}}); err != nil {
		t.Fatal(err)
	}
	team := h.db.pages.list[len(h.db.pages.list)-1].id
	h.reach(t, g, team, owner)
	// A stopped run changes nothing.
	h.svc.SetPageTargets(&fakeTargets{targets: []PageTarget{{DestinationID: 11, Linked: []int64{owner, bobID}}}})
	stopped := h.page(t, g, devopsRun, 2, 0, owner)
	h.db.pages.runStates = map[int64]string{devopsRun: "stopped"}
	h.reach(t, g, stopped, owner)
	if got := h.assignees(g); !slices.Equal(got, []string{"dev:carol", "pay:alice", "pay:bob", "pay:carol"}) {
		t.Fatalf("after the steps that change nothing: %v", got)
	}
	if got := h.assigneeChanges(t, g); !slices.Equal(got, []string{"alice", "alice,carol", "alice,bob,carol",
		"carol,carol", "carol,alice,carol", "carol,alice,bob,carol"}) {
		t.Errorf("assignees_changed %q", got)
	}
	// An Alert Group no longer firing takes no Assignee.
	h.db.pages.runStates = nil
	next := h.page(t, g, devopsRun, 3, 0, bobID)
	h.db.groups[g.ID].Status = "snoozed"
	h.reach(t, g, next, bobID)
	if got := h.assignees(g); !slices.Equal(got, []string{"dev:carol", "pay:alice", "pay:bob", "pay:carol"}) {
		t.Errorf("snoozed: %v", got)
	}
	// Nor do the reads show the rows of an Alert Group no longer firing, as a replica without migration 0050 leaves
	// them after an acknowledgement.
	if v, err := h.svc.Get(t.Context(), g.PublicID); err != nil || len(v.Assignees) != 0 {
		t.Errorf("the read of a snoozed alert group: %+v %v", v.Assignees, err)
	}
}

// TestAssigneesInOneFiring is C-32.FR-12 for steps without a wait between them, made by one firing at one time: the
// run steps order them, so a reach by the second replaces the first's Assignees, and a late reply of the first changes
// nothing.
func TestAssigneesInOneFiring(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	entry := func(step int) ChainEntry {
		return ChainEntry{Run: runID, Chain: 5, ChainPublicID: "EC0000000000PA", ChainName: "payments", Step: step}
	}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{
		{ChainEntry: entry(1), People: []int64{owner, carolID}, Importance: "normal", RunStepSeq: 1},
		{ChainEntry: entry(2), People: []int64{bobID}, Importance: "normal", RunStepSeq: 2, Seq: 1}}}); err != nil {
		t.Fatal(err)
	}
	first, second := h.db.pages.list[0], h.db.pages.list[1]
	if !first.Now.Equal(second.Now) {
		t.Fatalf("the pages of one firing at %v and %v", first.Now, second.Now)
	}
	h.reach(t, g, first.id, owner)
	h.reach(t, g, second.id, bobID)
	h.reach(t, g, first.id, carolID)
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:bob"}) {
		t.Errorf("assignees %v", got)
	}
}

// TestAssigneesRuleWaitingFirst is C-32.FR-12 for a step whose rules all wait before their first Mention: the step
// makes no first Mentions, and the later Mentions of its rules, by the run step that started them, replace step 1's
// Assignees and then add to them, whenever they come.
func TestAssigneesRuleWaitingFirst(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	step1 := h.page(t, g, runID, 1, 0, owner)
	h.reach(t, g, step1, owner)
	h.db.pages.ruleSeqs = map[int64]int32{81: 2, 82: 2}
	bob := h.page(t, g, runID, 2, 81, bobID)
	h.reach(t, g, bob, bobID)
	carol := h.page(t, g, runID, 2, 82, carolID)
	h.reach(t, g, carol, carolID)
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:bob", "pay:carol"}) {
		t.Errorf("assignees %v", got)
	}
}

// TestAssigneesWithoutRunStep is C-32.FR-12 for the Pages of an earlier version, without a run step: such a reach adds
// to a run without Assignees or whose Assignees have none, and changes nothing once a Page with a run step made them;
// a Page with a run step replaces the Assignees of one without.
func TestAssigneesWithoutRunStep(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	old := h.pageAt(t, g, runID, 1, 0, 0, owner, bobID)
	h.reach(t, g, old, owner)
	h.reach(t, g, old, bobID)
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:alice", "pay:bob"}) {
		t.Fatalf("pages without a run step %v", got)
	}
	step2 := h.page(t, g, runID, 2, 0, carolID)
	h.reach(t, g, step2, carolID)
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:carol"}) {
		t.Fatalf("a page with a run step %v", got)
	}
	late := h.pageAt(t, g, runID, 1, 0, 0, owner)
	h.reach(t, g, late, owner)
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:carol"}) {
		t.Errorf("a page without a run step after one with %v", got)
	}
}

// TestAssigneesByRunStep is C-32.FR-12 for a run whose Assignees run step 3 made: a reach by a greater run step
// replaces them, recording assignees_changed when it removes or adds someone and none when it reaches exactly them; a
// reach by run step 3 adds to them; a reach by a smaller run step changes nothing.
func TestAssigneesByRunStep(t *testing.T) {
	name := map[int64]string{owner: "alice", bobID: "bob", carolID: "carol"}
	for _, c := range []struct {
		name    string
		seq     int
		reached []int64
		want    []string
		changes []string
		deletes int
	}{
		{"a greater run step reaching someone new replaces them", 4, []int64{carolID}, []string{"carol:4"},
			[]string{"carol"}, 1},
		{"a greater run step reaching one of them removes the other", 4, []int64{owner}, []string{"alice:4"},
			[]string{"alice"}, 1},
		{"a greater run step reaching exactly them records nothing", 4, []int64{owner, bobID},
			[]string{"alice:4", "bob:4"}, nil, 1},
		{"the run step that made them adds", 3, []int64{carolID}, []string{"alice:3", "bob:3", "carol:3"},
			[]string{"alice,bob,carol"}, 0},
		{"a smaller run step changes nothing", 2, []int64{carolID}, []string{"alice:3", "bob:3"}, nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, g, _ := assigneeHarness(t)
			seeded := func(user int64, seq int32) *fakeAssignee {
				return &fakeAssignee{group: g.ID, run: runID, user: user, step: seq,
					seq: pgtype.Int4{Int32: seq, Valid: true}, team: pgtype.Int8{Int64: payTeam, Valid: true}, since: t0}
			}
			h.db.pages.assignees = []*fakeAssignee{seeded(owner, 3), seeded(bobID, 3)}
			id := h.pageAt(t, g, runID, c.seq, c.seq, 0, c.reached...)
			deletes := h.db.calls["DeleteRunAssigneesExcept"]
			h.reach(t, g, id, c.reached...)
			var got []string
			for _, a := range h.db.pages.assignees {
				got = append(got, fmt.Sprintf("%s:%d", name[a.user], a.seq.Int32))
			}
			slices.Sort(got)
			if !slices.Equal(got, c.want) {
				t.Errorf("assignees %v, want %v", got, c.want)
			}
			if got := h.assigneeChanges(t, g); !slices.Equal(got, c.changes) {
				t.Errorf("assignees_changed %q, want %q", got, c.changes)
			}
			if n := h.db.calls["DeleteRunAssigneesExcept"] - deletes; n != c.deletes {
				t.Errorf("%d replacements, want %d", n, c.deletes)
			}
		})
	}
}

// TestAssigneesByMessenger is C-32.FR-11 and FR-12 for the Destinations of Pages beyond Mattermost and Slack: a person
// a reply only names by the plain name, as a bridge without native_mentions does (PageTarget.Named), is reached
// nowhere and never becomes an Assignee, while a person a delivered reply reaches — a Telegram comment, a bridge's
// marker, an outgoing webhook's data, each Linked — does, with the run step of the Page like any other.
func TestAssigneesByMessenger(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	h.svc.SetPageTargets(&fakeTargets{targets: []PageTarget{{DestinationID: 11, Linked: []int64{owner}},
		{DestinationID: 12, Named: []int64{bobID}}}})
	id := h.page(t, g, runID, 1, 0, owner, bobID)
	after, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: id, Reached: []int64{owner}, Ended: true,
		Delivered: true})
	if err != nil {
		t.Fatal(err)
	}
	after(t.Context())
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:alice"}) || h.db.pages.assignees[0].seq.Int32 != 1 {
		t.Errorf("assignees %v", got)
	}
	if got := h.escalationEntries(g); !slices.Contains(got, "page_not_delivered/not_delivered/bob") {
		t.Errorf("entries %v", got)
	}
}

// TestAssigneesNotDelivered is C-32.AC-6: a person a Page did not reach — no Account link, replies Not delivered —
// never becomes an Assignee.
func TestAssigneesNotDelivered(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	h.svc.SetPageTargets(&fakeTargets{targets: []PageTarget{{DestinationID: 11, Linked: []int64{owner}}}})
	id := h.page(t, g, runID, 1, 0, owner, bobID)
	after, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: id, Reached: []int64{bobID},
		Ended: true})
	if err != nil {
		t.Fatal(err)
	}
	after(t.Context())
	if got := h.assignees(g); len(got) != 0 || len(h.assigneeChanges(t, g)) != 0 {
		t.Errorf("assignees %v", got)
	}
}

// TestAssigneesCleared is C-32.FR-12, C-32.AC-2 and C-10.FR-1: Acknowledge, Snooze and Resolve clear the Assignees in
// their transaction with assignees_changed after their own event, both in one rendering; a chain that ran out keeps
// them (C-32.AC-10), and a Command that leaves no Assignee records none.
func TestAssigneesCleared(t *testing.T) {
	for _, cmd := range []Command{CommandAcknowledge, CommandSnooze, CommandResolve} {
		t.Run(string(cmd), func(t *testing.T) {
			h, g, renders := assigneeHarness(t)
			chains := h.withChains()
			chains.ranOut = true
			id := h.page(t, g, runID, 1, 0, owner)
			h.reach(t, g, id, owner)
			if err := h.step(t, g, ChainProgress{RanOut: true}); err != nil {
				t.Fatal(err)
			}
			if !h.db.groups[g.ID].Unclaimed || len(h.assignees(g)) != 1 {
				t.Fatalf("run out: unclaimed %v, assignees %v", h.db.groups[g.ID].Unclaimed, h.assignees(g))
			}
			if _, err := h.do(bob, cmd, g); err != nil {
				t.Fatal(err)
			}
			if got := h.assignees(g); len(got) != 0 {
				t.Errorf("assignees %v", got)
			}
			if got := h.assigneeChanges(t, g); !slices.Equal(got, []string{"alice", ""}) {
				t.Errorf("assignees_changed %q", got)
			}
			last := (*renders)[len(*renders)-1]
			if n := len(last.Events); n < 2 || last.Events[n-1].Event != EventAssigneesChanged ||
				rowOf(last.Events[n-2].Event, last.Events[n-2].Variant).Kind != KindStatus {
				t.Errorf("rendering %+v", last.Events)
			}
			// Back to firing and acknowledged again, without Assignees: no assignees_changed.
			if _, err := h.do(bob, map[Command]Command{CommandAcknowledge: CommandUnacknowledge,
				CommandSnooze: CommandUnsnooze, CommandResolve: CommandUnresolve}[cmd], g); err != nil {
				t.Fatal(err)
			}
			if _, err := h.do(bob, cmd, g); err != nil {
				t.Fatal(err)
			}
			if got := h.assigneeChanges(t, g); len(got) != 2 {
				t.Errorf("assignees_changed without assignees %q", got)
			}
		})
	}
}

// TestAssigneesRead is C-32.FR-18: the Alert Group's read and list name its Assignees with their User, the owning Team
// of the chain and the chain, a chain deleted since as none, and the Timeline entry of assignees_changed the Assignees
// after the change, none once cleared.
func TestAssigneesRead(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	id := h.page(t, g, runID, 1, 0, owner)
	h.reach(t, g, id, owner)
	dev := h.page(t, g, deletedRunID, 1, 0, bobID)
	h.reach(t, g, dev, bobID)
	v, err := h.svc.Get(t.Context(), g.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Assignees) != 2 || v.Assignees[0].User.Name != "Alice" || v.Assignees[0].Team == nil ||
		v.Assignees[0].Team.Slug != "payments" || v.Assignees[0].Chain == nil ||
		v.Assignees[0].Chain.PublicID != "EC0000000000PA" || v.Assignees[0].Since.IsZero() ||
		v.Assignees[1].User.Name != "Bob" || v.Assignees[1].Team != nil || v.Assignees[1].Chain != nil {
		t.Errorf("assignees %+v", v.Assignees)
	}
	page, err := h.svc.List(t.Context(), ListRequest{Limit: 10})
	if err != nil || len(page.Groups) != 1 || len(page.Groups[0].Assignees) != 2 {
		t.Errorf("list %+v %v", page.Groups, err)
	}
	if _, err := h.do(bob, CommandAcknowledge, g); err != nil {
		t.Fatal(err)
	}
	if v, err := h.svc.Get(t.Context(), g.PublicID); err != nil || v.Assignees == nil || len(v.Assignees) != 0 {
		t.Errorf("after the acknowledgement %+v %v", v.Assignees, err)
	}
	tl, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Kinds: []Kind{KindEscalation}, Ascending: true,
		Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for _, e := range tl.Entries {
		if e.Event != string(EventAssigneesChanged) {
			continue
		}
		var people []string
		for _, a := range e.Assignees {
			people = append(people, a.User.Login)
		}
		got = append(got, people)
	}
	if len(got) != 3 || !slices.Equal(got[0], []string{"alice"}) || !slices.Equal(got[1], []string{"alice", "bob"}) ||
		len(got[2]) != 0 {
		t.Errorf("timeline %q", got)
	}
	// A User deleted since reads as deactivated.
	delete(h.db.users, owner)
	name, err := h.svc.assigneeNames(t.Context(), []assigneeRecord{{UserID: owner, RunID: runID}})
	if err != nil || !name(assigneeRecord{UserID: owner, RunID: runID}).User.Deactivated {
		t.Errorf("a deleted user: %v", err)
	}
}

// TestFilterByAssignee is C-32.FR-18 and C-32.AC-10 on the parameters of the list: no Assignee does not filter, a
// User's public_id or me selects that User, me for a Service account and an unknown User are refused at
// /query/assignee.
func TestFilterByAssignee(t *testing.T) {
	h := newHarness(t)
	h.people()
	resolve := func(assignee string, c Caller) (query, error) {
		return h.svc.resolve(t.Context(), Filter{Assignee: assignee, Me: c.Actor}, t0)
	}
	if q, err := resolve("", bob); err != nil || q.assignee.Valid {
		t.Errorf("no assignee: %+v %v", q.assignee, err)
	}
	if q, err := resolve(OwnerMe, bob); err != nil || q.assignee.Int64 != bobID {
		t.Errorf("me: %+v %v", q.assignee, err)
	}
	if q, err := resolve("SRAAAAAAAAAAA9", bob); err != nil || q.assignee.Int64 != owner {
		t.Errorf("a user: %+v %v", q.assignee, err)
	}
	for _, c := range []struct {
		value  string
		caller Caller
		code   string
	}{{OwnerMe, robot, CodeUnsupported}, {"SR0000000000ZZ", bob, CodeUnknownID}, {"not-an-id", bob, CodeUnknownID}} {
		var fe *FieldError
		if _, err := resolve(c.value, c.caller); !errors.As(err, &fe) || fe.Pointer != "/query/assignee" ||
			fe.Code != c.code {
			t.Errorf("%s: %v", c.value, err)
		}
	}
	// The counts take the filter too: Alert Groups of the list's other filters where the User is an Assignee.
	ha, g, _ := assigneeHarness(t)
	id := ha.page(t, g, runID, 1, 0, bobID)
	ha.reach(t, g, id, bobID)
	for _, c := range []struct {
		caller Caller
		want   int64
	}{{bob, 1}, {alice, 0}} {
		counts, err := ha.svc.Counts(t.Context(), Filter{Assignee: OwnerMe, Me: c.caller.Actor})
		if err != nil || counts.Firing != c.want || counts.All != c.want {
			t.Errorf("counts for %d: %+v %v", c.caller.Actor.ID, counts, err)
		}
	}
	if ha.db.calls["CountGroups"] == 0 {
		t.Error("the counts never read the database")
	}
	if _, err := ha.svc.Counts(t.Context(), Filter{Assignee: OwnerMe, Me: robot.Actor}); err == nil {
		t.Error("counts for me as a service account")
	}
}

// TestSettlePageSharesTheEvaluation is #628, C-12.FR-12 for the footer of a firing Alert Group: the outcome of a Page
// that reached someone takes the lock of the evaluation of the Teams shared, once, before it locks the Alert Group
// whose Assignees it makes them, as a Snapshot does, so that a batch of user_name or account_links, which takes it
// exclusively before it lists, sees the Assignees; it locks no row of a User. An outcome that reached nobody takes no
// such lock, and a failing lock fails the outcome before it changes anything.
func TestSettlePageSharesTheEvaluation(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	id := h.page(t, g, runID, 1, 0, owner, bobID, carolID)
	shares, users := h.db.calls["ShareTeamEvaluation"], h.db.calls["ShareUser"]
	before := -1
	h.db.before["LockGroups"] = func() { before = h.db.calls["ShareTeamEvaluation"] - shares }
	if _, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: id,
		Reached: []int64{carolID, owner}}); err != nil {
		t.Fatal(err)
	}
	if n := h.db.calls["ShareTeamEvaluation"] - shares; before != 1 || n != 1 || h.db.calls["ShareUser"] != users {
		t.Errorf("%d shared locks before the alert group, %d in all, %d user locks", before, n,
			h.db.calls["ShareUser"]-users)
	}
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:alice", "pay:carol"}) {
		t.Errorf("assignees %v", got)
	}
	shares = h.db.calls["ShareTeamEvaluation"]
	if _, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: id}); err != nil ||
		h.db.calls["ShareTeamEvaluation"] != shares {
		t.Errorf("an outcome that reached nobody: %v, %d shared locks", err, h.db.calls["ShareTeamEvaluation"]-shares)
	}
	errBoom := errors.New("boom")
	h.db.fail["ShareTeamEvaluation"] = errBoom
	locks := h.db.calls["LockGroups"]
	if _, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: id, Reached: []int64{bobID}}); !errors.Is(err,
		errBoom) || h.db.calls["LockGroups"] != locks || len(h.assignees(g)) != 2 {
		t.Errorf("a failing lock: %v, %d alert group locks, assignees %v", err, h.db.calls["LockGroups"]-locks,
			h.assignees(g))
	}
	delete(h.db.fail, "ShareTeamEvaluation")
}

// TestAssigneeFailures: a failure of each read and write of the Assignees fails the change or the read.
func TestAssigneeFailures(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	errBoom := errors.New("boom")
	for _, name := range []string{"ListRunAssignees", "DeleteRunAssigneesExcept", "UpsertAssignee",
		"ListGroupAssignees"} {
		id := h.page(t, g, runID, len(h.db.pages.list)+1, 0, owner)
		h.db.fail[name] = errBoom
		if _, err := h.svc.SettlePage(t.Context(), nil, g.ID, PageOutcome{Page: id, Reached: []int64{owner}}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
		delete(h.db.fail, name)
	}
	for _, name := range []string{"ListGroupAssignees", "ListTeamRefs", "ListTimelineChains", "ListUserRefs"} {
		h.db.fail[name] = errBoom
		if _, err := h.svc.Get(t.Context(), g.PublicID); !errors.Is(err, errBoom) {
			t.Errorf("get %s: %v", name, err)
		}
		delete(h.db.fail, name)
	}
	h.db.fail["ListGroupAssignees"] = errBoom
	if _, err := h.svc.List(t.Context(), ListRequest{Limit: 10}); !errors.Is(err, errBoom) {
		t.Errorf("list: %v", err)
	}
	delete(h.db.fail, "ListGroupAssignees")
	h.db.fail["DeleteGroupAssignees"] = errBoom
	if _, err := h.do(bob, CommandAcknowledge, g); !errors.Is(err, errBoom) {
		t.Errorf("clear: %v", err)
	}
	delete(h.db.fail, "DeleteGroupAssignees")
	e := h.db.entries[len(h.db.entries)-1]
	e.Kind, e.Assignees = string(KindEscalation), []byte("{")
	if _, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 50}); err == nil {
		t.Error("a broken entry")
	}
	e.Assignees = []byte(`[{"user_id": 9, "run_id": 88}]`)
	h.db.fail["ListTimelineChains"] = errBoom
	if _, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 50}); !errors.Is(err, errBoom) {
		t.Errorf("timeline names: %v", err)
	}
}

// fakeQueue is the queue of calls of the call Pages: it records the Pages it queued, joins each to the call call and
// counts its log lines.
type fakeQueue struct {
	pages  []int64
	call   int64
	logged int
	err    error
}

func (q *fakeQueue) queue(_ context.Context, _ DBTX, page int64) (int64, func(context.Context), error) {
	q.pages = append(q.pages, page)
	return q.call, func(context.Context) { q.logged++ }, q.err
}

// callStep makes the step of the run runID on g a call of the person through the gateway gatewayID, joined to the
// call that q gives, and returns the id of its Page.
func (h *harness) callStep(t *testing.T, g *dbgen.LockGroupsRow, step int, q *fakeQueue, person int64) int64 {
	t.Helper()
	p := StepPage{ChainEntry: ChainEntry{Run: runID, Chain: 5, ChainPublicID: "EC0000000000PA", ChainName: "payments",
		Step: step}, People: []int64{person}, RunStepSeq: step,
		Call: &StepCall{Gateway: PageGateway{ID: gatewayID, PublicID: "CN00000000GW01", Name: "Gateway"},
			Queue: q.queue}}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{p}}); err != nil {
		t.Fatal(err)
	}
	return h.db.pages.list[len(h.db.pages.list)-1].id
}

// settleCall reports how a call ended, as the gateway sender does, and runs what it returns.
func (h *harness) settleCall(t *testing.T, o CallOutcome) {
	t.Helper()
	h.clock.Advance(time.Second)
	after, err := h.svc.SettleCall(t.Context(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	after(t.Context())
}

// TestAssigneesByCalls is C-32.FR-3, FR-10, FR-12, C-30.FR-4, FR-7 and C-32.AC-11 for the Pages by a call through a
// Notification gateway: a call step writes a Page of the kind call of one person and records the Quiet paged with the
// person and the gateway, which writes no Thread reply, and queues the call after its dispatch; a call that the
// gateway accepted reaches the person, who becomes an Assignee of the chain, replacing those of an earlier step as a
// delivered Mention does, and counts as delivered; one that ended Not delivered records page_not_delivered with the
// chain, the step, the gateway and the reason, and leaves the Assignees as they were; a call dropped as its chain
// stopped, and a Page whose Alert Group a call dropped before its first attempt, end without a record; an Alert Group
// that a call of the hook of muster dev was about records page_not_delivered without a chain.
func TestAssigneesByCalls(t *testing.T) {
	h, g, renders := assigneeHarness(t)
	h.db.users[owner] = dbgen.ListUserRefsRow{ID: owner, PublicID: "SRAAAAAAAAAAA9", Name: "Alice", Login: "alice",
		Status: "active"}
	gw := PageGateway{ID: gatewayID, PublicID: "CN00000000GW01", Name: "Gateway"}
	user := func(id int64) PagedUser { return PagedUser{ID: id} }
	step1 := h.page(t, g, runID, 1, 0, owner)
	h.reach(t, g, step1, owner)

	delivered := metrics.Pages.With("EC0000000000PA", "call", "delivered").Get()
	notDelivered := metrics.Pages.With("EC0000000000PA", "call", "not_delivered").Get()
	q := &fakeQueue{call: 501}
	*renders = nil
	step2 := h.callStep(t, g, 2, q, bobID)
	p := h.db.pages.byID(step2)
	if p.Kind != PageKindCall || p.GatewayConnectionID.Int64 != gatewayID || p.State != PagePending || p.call != 501 ||
		len(p.users) != 1 || p.users[0].id != bobID || p.users[0].outcome != PagePending || p.RunStepSeq.Int32 != 2 {
		t.Fatalf("the call page %+v %+v", p, p.users)
	}
	if !slices.Equal(q.pages, []int64{step2}) || q.logged != 1 {
		t.Errorf("queued %v, logged %d", q.pages, q.logged)
	}
	var paged *fakeEntry
	for _, e := range h.entriesOf(g) {
		if e.Event.String == string(EventPaged) && e.GatewayConnectionID.Valid {
			paged = e
		}
	}
	if paged == nil || paged.Loudness.String != string(Quiet) || len(paged.Mentions) != 0 ||
		!slices.Equal(paged.PagedUserIds, []int64{bobID}) || paged.StepNumber.Int32 != 2 ||
		paged.EscalationRunID.Int64 != runID {
		t.Fatalf("paged of the call %+v", paged)
	}
	if r := *renders; len(r) != 1 || len(r[0].Events) != 1 || r[0].Events[0].Variant != VariantCall ||
		r[0].Events[0].Loudness != Quiet || r[0].Events[0].Page.Gateway == nil || len(r[0].Events[0].Page.Targets) != 0 {
		t.Errorf("the rendering of the call %+v", r)
	}

	// The gateway accepted the call: bob replaces alice, as step 2 is later than step 1.
	h.settleCall(t, CallOutcome{Call: 501, Accepted: true, Groups: []int64{g.ID}, User: user(bobID), Gateway: gw})
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:bob"}) {
		t.Fatalf("after an accepted call: %v", got)
	}
	if p.State != PageDelivered || p.users[0].outcome != PageDelivered {
		t.Errorf("the accepted page %+v %+v", p.InsertPageParams, p.users[0])
	}
	if n := metrics.Pages.With("EC0000000000PA", "call", "delivered").Get() - delivered; n != 1 {
		t.Errorf("%d delivered calls counted", n)
	}
	// Settled again, it changes nothing.
	h.settleCall(t, CallOutcome{Call: 501, Accepted: true, Groups: []int64{g.ID}, User: user(bobID), Gateway: gw})

	// Step 3 calls carol, and the call ends Not delivered: page_not_delivered with the chain and the step, no change
	// of the Assignees.
	q.call = 502
	step3 := h.callStep(t, g, 3, q, carolID)
	before := len(h.assigneeChanges(t, g))
	h.settleCall(t, CallOutcome{Call: 502, Reason: PageCallRefused, Groups: []int64{g.ID}, User: user(carolID),
		Gateway: gw})
	if got := h.assignees(g); !slices.Equal(got, []string{"pay:bob"}) || len(h.assigneeChanges(t, g)) != before {
		t.Fatalf("after a call not delivered: %v", got)
	}
	var missed *fakeEntry
	for _, e := range h.entriesOf(g) {
		if e.Event.String == string(EventPageNotDelivered) {
			missed = e
		}
	}
	if missed == nil || missed.Reason.String != PageCallRefused || missed.StepNumber.Int32 != 3 ||
		missed.EscalationRunID.Int64 != runID || missed.GatewayConnectionID.Int64 != gatewayID ||
		!slices.Equal(missed.PagedUserIds, []int64{carolID}) {
		t.Fatalf("page_not_delivered %+v", missed)
	}
	if p := h.db.pages.byID(step3); p.State != PageNotDelivered || p.Reason.String != PageCallRefused ||
		p.users[0].reason != PageCallRefused {
		t.Errorf("the page not delivered %+v %+v", p.InsertPageParams, p.users[0])
	}
	if n := metrics.Pages.With("EC0000000000PA", "call", "not_delivered").Get() - notDelivered; n != 1 {
		t.Errorf("%d calls not delivered counted", n)
	}

	// A call dropped whole, and a Page whose Alert Group an accepted call dropped before its first attempt, end
	// without a record.
	entries := len(h.entriesOf(g))
	q.call = 503
	step4 := h.callStep(t, g, 4, q, owner)
	q.call = 504
	step5 := h.callStep(t, g, 5, q, carolID)
	entries += 2
	h.settleCall(t, CallOutcome{Call: 503, User: user(owner), Gateway: gw})
	h.settleCall(t, CallOutcome{Call: 504, Accepted: true, User: user(carolID), Gateway: gw})
	for _, id := range []int64{step4, step5} {
		if p := h.db.pages.byID(id); p.State != PageNotDelivered || p.Reason.Valid || p.users[0].outcome != PagePending {
			t.Errorf("the dropped page %d %+v", id, p.InsertPageParams)
		}
	}
	if got := len(h.entriesOf(g)); got != entries || !slices.Equal(h.assignees(g), []string{"pay:bob"}) {
		t.Errorf("dropped calls recorded %v", h.events(g))
	}
	if n := metrics.Pages.With("EC0000000000PA", "call", "not_delivered").Get() - notDelivered; n != 1 {
		t.Errorf("dropped calls counted: %d", n)
	}

	// A call of the hook, without a Page, records page_not_delivered without a chain.
	h.settleCall(t, CallOutcome{Call: 999, Reason: PageCallBudgetUsed, Groups: []int64{g.ID, 4242},
		User: user(owner), Gateway: gw})
	last := h.entriesOf(g)[len(h.entriesOf(g))-1]
	if last.Event.String != string(EventPageNotDelivered) || last.EscalationRunID.Valid ||
		last.Reason.String != PageCallBudgetUsed {
		t.Errorf("the hook's call %+v", last.InsertTimelineEntryParams)
	}

	for _, o := range []CallOutcome{{Call: 1, Reason: "rang_out", User: user(owner)}, {Call: 1}} {
		if _, err := h.svc.SettleCall(t.Context(), nil, o); err == nil {
			t.Errorf("%+v was settled", o)
		}
	}
	for _, name := range []string{"ListCallPages", "LockGroups", "LockPage", "ReachPageUsers", "MissCallPageUsers",
		"EndPage"} {
		q.call = 600
		id := h.callStep(t, g, 6, q, bobID)
		h.db.fail[name] = errBoom
		o := CallOutcome{Call: 600, Accepted: name != "MissCallPageUsers", Groups: []int64{g.ID}, User: user(bobID),
			Gateway: gw}
		if !o.Accepted {
			o.Reason = PageCallRefused
		}
		if _, err := h.svc.SettleCall(t.Context(), nil, o); !errors.Is(err, errBoom) {
			t.Errorf("%s failing: %v", name, err)
		}
		delete(h.db.fail, name)
		h.db.pages.byID(id).State = PageDelivered
	}
}

// TestCallNotDeliveredToNobody is C-30.FR-7 and C-32.FR-10: a call that ended Not delivered for a person who no longer
// exists records no page_not_delivered, which would name nobody, and the Page still ends not_delivered with the reason
// of the call.
func TestCallNotDeliveredToNobody(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	id := h.callStep(t, g, 1, &fakeQueue{call: 501}, carolID)
	p := h.db.pages.byID(id)
	delete(h.db.users, carolID)
	h.settleCall(t, CallOutcome{Call: 501, Reason: PageCallRefused, Groups: []int64{g.ID},
		User: PagedUser{ID: carolID}, Gateway: PageGateway{ID: gatewayID}})
	for _, e := range h.entriesOf(g) {
		if e.Event.String == string(EventPageNotDelivered) {
			t.Errorf("page_not_delivered %+v", e.InsertTimelineEntryParams)
		}
	}
	if p.State != PageNotDelivered || p.Reason.String != PageCallRefused {
		t.Errorf("the page %+v", p.InsertPageParams)
	}
}

// TestCallStepFailures: a call names one person and its gateway, and a call whose queue or whose record of the call
// fails fails the step.
func TestCallStepFailures(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	q := &fakeQueue{call: 501}
	call := &StepCall{Gateway: PageGateway{ID: gatewayID}, Queue: q.queue}
	for _, p := range []StepPage{{People: []int64{owner, bobID}, Call: call}, {People: []int64{owner},
		Call: &StepCall{Gateway: PageGateway{ID: gatewayID}}}, {People: []int64{owner}, Call: &StepCall{Queue: q.queue}}} {
		p.ChainEntry = ChainEntry{Run: runID, Step: 1}
		if err := h.step(t, g, ChainProgress{Pages: []StepPage{p}}); err == nil {
			t.Errorf("%+v was made", p)
		}
	}
	q.err = errBoom
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{{ChainEntry: ChainEntry{Run: runID, Step: 1},
		People: []int64{owner}, Call: call}}}); !errors.Is(err, errBoom) {
		t.Errorf("a failing queue: %v", err)
	}
	q.err = nil
	for _, name := range []string{"SetPageCall", "InsertPage", "InsertPageUser"} {
		h.db.fail[name] = errBoom
		if err := h.step(t, g, ChainProgress{Pages: []StepPage{{ChainEntry: ChainEntry{Run: runID, Step: 1},
			People: []int64{owner}, Call: call}}}); !errors.Is(err, errBoom) {
			t.Errorf("%s failing: %v", name, err)
		}
		delete(h.db.fail, name)
	}
	// A person who no longer exists is called by nobody.
	n := len(h.db.pages.list)
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{{ChainEntry: ChainEntry{Run: runID, Step: 1},
		People: []int64{4242}, Call: call}}}); err != nil || len(h.db.pages.list) != n {
		t.Errorf("a person who is gone: %v", err)
	}
	// A person skipped by id alone, as a call skips one without a confirmed contact, is named for the webhooks.
	var renders []Rendering
	h.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
		renders = append(renders, r)
		return nil
	})
	skip := StepSkip{ChainEntry: ChainEntry{Run: runID, Step: 2}, Reason: SkipNoConfirmedContact,
		Person: &PagedUser{ID: bobID}}
	if err := h.step(t, g, ChainProgress{Skips: []StepSkip{skip}}); err != nil || len(renders) != 1 ||
		renders[0].Events[0].Page.Users[0].Login != "bob" || renders[0].Events[0].Reason != SkipNoConfirmedContact {
		t.Errorf("a skip named by id: %v %+v", err, renders)
	}
	h.db.fail["ListUserRefs"] = errBoom
	if err := h.step(t, g, ChainProgress{Skips: []StepSkip{skip}}); !errors.Is(err, errBoom) {
		t.Errorf("a failing name: %v", err)
	}
}

// TestCallsOfStoppedChains: a Page of the kind call whose own run stopped, whose run a later start of the chains
// superseded, or whose rule run stopped, at or before the call's first attempt — as the earlier Page of a chain that
// started over and Paged again into the same call — ends not_delivered without a reason, records nothing and is not
// counted, whether the call was accepted or ended Not delivered; a Page whose run was acknowledged after the first
// attempt, during the retries, was called about and is settled like the Page of the current run.
func TestCallsOfStoppedChains(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		h, g, _ := assigneeHarness(t)
		q := &fakeQueue{call: 501}
		stopped, older, rule, current, late := h.callStep(t, g, 1, q, bobID), h.callStep(t, g, 2, q, bobID),
			h.callStep(t, g, 3, q, bobID), h.callStep(t, g, 4, q, bobID), h.callStep(t, g, 5, q, bobID)
		first := h.clock.Now().Add(time.Minute)
		h.db.pages.byID(rule).NotificationRuleRunID = pgtype.Int8{Int64: 71, Valid: true}
		h.db.pages.ruleStates = map[int64]string{71: "stopped"}
		h.db.pages.ruleEnded = map[int64]time.Time{71: first.Add(-time.Second)}
		h.db.pages.byID(stopped).EscalationRunID = pgtype.Int8{Int64: devopsRun, Valid: true}
		h.db.pages.byID(late).EscalationRunID = pgtype.Int8{Int64: 93, Valid: true}
		h.db.pages.runStates = map[int64]string{devopsRun: "stopped", 93: "stopped"}
		h.db.pages.runEnded = map[int64]time.Time{devopsRun: first, 93: first.Add(time.Second)}
		h.db.pages.byID(older).EscalationRunID = pgtype.Int8{Int64: 92, Valid: true}
		h.db.pages.superseded = map[int64]time.Time{92: first.Add(-time.Minute)}
		outcome, counted := "delivered", metrics.Pages.With("EC0000000000PA", "call", "delivered")
		o := CallOutcome{Call: 501, Accepted: true, Groups: []int64{g.ID}, User: PagedUser{ID: bobID},
			Gateway: PageGateway{ID: gatewayID}, FirstAttempt: first}
		if !accepted {
			o.Accepted, o.Reason = false, PageCallRefused
			outcome, counted = "not_delivered", metrics.Pages.With("EC0000000000PA", "call", "not_delivered")
		}
		before := counted.Get()
		h.settleCall(t, o)
		for _, id := range []int64{stopped, older, rule} {
			if p := h.db.pages.byID(id); p.State != PageNotDelivered || p.Reason.Valid {
				t.Errorf("accepted %t: the page %d of a stopped chain %+v", accepted, id, p.InsertPageParams)
			}
		}
		for _, id := range []int64{current, late} {
			if p := h.db.pages.byID(id); p.State != outcome {
				t.Errorf("accepted %t: the page %d that was called about %+v", accepted, id, p.InsertPageParams)
			}
		}
		if n := counted.Get() - before; n != 2 {
			t.Errorf("accepted %t: %d calls counted", accepted, n)
		}
		n := 0
		for _, e := range h.entriesOf(g) {
			if e.Event.String == string(EventPageNotDelivered) {
				n++
			}
		}
		if want := map[bool]int{true: 0, false: 2}[accepted]; n != want {
			t.Errorf("accepted %t: %d page_not_delivered", accepted, n)
		}
	}
}

// TestCallQueueOrder: the calls of one step are queued by gateway and then person, whatever the order of its Pages,
// so that two steps calling the same people take their locks in one order.
func TestCallQueueOrder(t *testing.T) {
	h, g, _ := assigneeHarness(t)
	q := &fakeQueue{call: 501}
	page := func(gateway, person int64) StepPage {
		return StepPage{ChainEntry: ChainEntry{Run: runID, Step: 1}, People: []int64{person},
			Call: &StepCall{Gateway: PageGateway{ID: gateway}, Queue: q.queue}}
	}
	if err := h.step(t, g, ChainProgress{Pages: []StepPage{page(gatewayID+1, owner), page(gatewayID, carolID),
		page(gatewayID, bobID), page(gatewayID+1, bobID)}}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, id := range q.pages {
		p := h.db.pages.byID(id)
		got = append(got, fmt.Sprintf("%d/%d", p.GatewayConnectionID.Int64, p.users[0].id))
	}
	want := []string{fmt.Sprintf("%d/%d", gatewayID, bobID), fmt.Sprintf("%d/%d", gatewayID, carolID),
		fmt.Sprintf("%d/%d", gatewayID+1, owner), fmt.Sprintf("%d/%d", gatewayID+1, bobID)}
	if owner > bobID {
		want[2], want[3] = want[3], want[2]
	}
	if !slices.Equal(got, want) {
		t.Errorf("queued %v, want %v", got, want)
	}
}
