// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package notificationrules

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/notificationrules/dbgen"
)

var runsT0 = time.Date(2026, 11, 17, 3, 10, 0, 0, time.UTC)

// fakeRuleRun is a notification_rule_runs row.
type fakeRuleRun struct {
	dbgen.InsertRuleRunParams
	id int64
}

// fakeRunQueries are the queries of the rule runs in memory: the rules of the Users, the Organization's, the rule
// runs, their timers and the run of their chain.
type fakeRunQueries struct {
	rules    map[int64]dbgen.GetRulesRow
	org      []byte
	runs     []*fakeRuleRun
	timers   map[int64]time.Time
	runState string
	sources  []byte
	notified int
	fail     map[string]error
}

func newFakeRunQueries() *fakeRunQueries {
	return &fakeRunQueries{rules: map[int64]dbgen.GetRulesRow{}, org: Defaults().JSON(), timers: map[int64]time.Time{},
		runState: "running", sources: []byte(`[{"kind":"route"},{"kind":"team","team_id":3}]`), fail: map[string]error{}}
}

func (f *fakeRunQueries) run(id int64) *fakeRuleRun {
	for _, r := range f.runs {
		if r.id == id {
			return r
		}
	}
	return nil
}

func (f *fakeRunQueries) GetRules(_ context.Context, arg dbgen.GetRulesParams) (dbgen.GetRulesRow, error) {
	if err := f.fail["GetRules"]; err != nil {
		return dbgen.GetRulesRow{}, err
	}
	r, ok := f.rules[arg.UserID]
	if !ok {
		return r, pgx.ErrNoRows
	}
	return r, nil
}

func (f *fakeRunQueries) GetOrganizationRules(context.Context, int64) (dbgen.GetOrganizationRulesRow, error) {
	return dbgen.GetOrganizationRulesRow{NotificationRules: f.org}, f.fail["GetOrganizationRules"]
}

func (f *fakeRunQueries) InsertRuleRun(_ context.Context, arg dbgen.InsertRuleRunParams) (int64, error) {
	if err := f.fail["InsertRuleRun"]; err != nil {
		return 0, err
	}
	r := &fakeRuleRun{InsertRuleRunParams: arg, id: int64(70 + len(f.runs))}
	f.runs = append(f.runs, r)
	return r.id, nil
}

func (f *fakeRunQueries) UpsertRuleTimer(_ context.Context, arg dbgen.UpsertRuleTimerParams) error {
	if err := f.fail["UpsertRuleTimer"]; err != nil {
		return err
	}
	f.timers[arg.NotificationRuleRunID] = arg.Deadline
	return nil
}

func (f *fakeRunQueries) DeleteRuleTimers(_ context.Context, arg dbgen.DeleteRuleTimersParams) error {
	if err := f.fail["DeleteRuleTimers"]; err != nil {
		return err
	}
	for _, id := range arg.Ids {
		delete(f.timers, id)
	}
	return nil
}

func (f *fakeRunQueries) NotifyRuleTimers(context.Context, string) error {
	f.notified++
	return f.fail["NotifyRuleTimers"]
}

func (f *fakeRunQueries) StopRuleRuns(_ context.Context, arg dbgen.StopRuleRunsParams) ([]int64, error) {
	if err := f.fail["StopRuleRuns"]; err != nil {
		return nil, err
	}
	var out []int64
	for _, r := range f.runs {
		if r.AlertGroupID == arg.AlertGroupID && r.State == RunRunning {
			r.State, r.EndedAt = RunStopped, pgtype.Timestamptz{Time: arg.Now, Valid: true}
			out = append(out, r.id)
		}
	}
	return out, nil
}

func (f *fakeRunQueries) GetRuleRunGroup(_ context.Context, arg dbgen.GetRuleRunGroupParams) (int64, error) {
	if err := f.fail["GetRuleRunGroup"]; err != nil {
		return 0, err
	}
	r := f.run(arg.ID)
	if r == nil {
		return 0, pgx.ErrNoRows
	}
	return r.AlertGroupID, nil
}

func (f *fakeRunQueries) LockRuleRun(_ context.Context, arg dbgen.LockRuleRunParams) (dbgen.LockRuleRunRow, error) {
	if err := f.fail["LockRuleRun"]; err != nil {
		return dbgen.LockRuleRunRow{}, err
	}
	r := f.run(arg.ID)
	if r == nil {
		return dbgen.LockRuleRunRow{}, pgx.ErrNoRows
	}
	out := dbgen.LockRuleRunRow{ID: r.id, AlertGroupID: r.AlertGroupID, EscalationRunID: r.EscalationRunID,
		StepNumber: r.StepNumber, UserID: r.UserID, Importance: r.Importance, Actions: r.Actions, Position: r.Position,
		State: r.State, RunState: f.runState, ChainID: 1, ChainName: "devops", Sources: f.sources, OwningTeamID: 7,
		ChainPublicID: "EC000000000001", ScheduleID: 11, Deadline: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)}
	if d, ok := f.timers[r.id]; ok {
		out.Deadline = d
	}
	return out, nil
}

func (f *fakeRunQueries) SaveRuleRun(_ context.Context, arg dbgen.SaveRuleRunParams) error {
	if err := f.fail["SaveRuleRun"]; err != nil {
		return err
	}
	r := f.run(arg.ID)
	r.Position, r.State, r.EndedAt = arg.Position, arg.State, arg.EndedAt
	return nil
}

// fakeCalls records the calls of the rule runs.
type fakeCalls struct {
	calls []string
	err   error
}

func (c *fakeCalls) QueueCall(_ context.Context, _ DBTX, _, _ int64, gateway string) error {
	c.calls = append(c.calls, gateway)
	return c.err
}

// runsOver are the runs of the rules over the fake at the manual clock.
func runsOver(q *fakeRunQueries, c *clock.Manual, calls CallPages) *Runs {
	r := New(Config{OrgID: 1}).Runs(RunsConfig{Business: c, TimersChannel: "muster_timers", Calls: calls})
	r.queries = func(DBTX) runQueries { return q }
	return r
}

// TestRuleRuns is C-31.FR-3 and C-31.AC-1 with the default rules: a normal Page Mentions the person once and the run
// is done; an important Page Mentions at once and 5 minutes later, with one timer per person on the rule run — two
// people Paged by one step whose important rules both repeat keep two timers — and none after the run stops, whose
// action is then not made.
func TestRuleRuns(t *testing.T) {
	c := clock.NewManual(runsT0)
	q := newFakeRunQueries()
	r := runsOver(q, c, nil)
	normal, err := r.Start(t.Context(), nil, Start{Group: 9, Run: 501, Step: 1, Importance: "normal",
		Users: []int64{21}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(normal.Mentioned, []int64{21}) || normal.AgainSeconds != 0 || q.runs[0].State != RunDone ||
		len(q.timers) != 0 || q.notified != 0 {
		t.Fatalf("normal %+v %+v", normal, q.runs[0])
	}
	important, err := r.Start(t.Context(), nil, Start{Group: 9, Run: 502, Step: 3, Importance: ImportanceImportant,
		Users: []int64{21, 22}, RunStepSeq: 6})
	if err != nil {
		t.Fatal(err)
	}
	// A rule run keeps the run step that started it, which its later Mentions belong to (C-32.FR-12); none without.
	if q.runs[0].RunStepSeq.Valid || q.runs[1].RunStepSeq.Int32 != 6 || q.runs[2].RunStepSeq.Int32 != 6 {
		t.Errorf("run steps %+v %+v %+v", q.runs[0].RunStepSeq, q.runs[1].RunStepSeq, q.runs[2].RunStepSeq)
	}
	if !slices.Equal(important.Mentioned, []int64{21, 22}) || important.AgainSeconds != 300 || len(q.timers) != 2 ||
		!q.timers[71].Equal(runsT0.Add(5*time.Minute)) || q.runs[1].Position != 2 || q.runs[1].State != RunRunning ||
		q.notified != 1 {
		t.Fatalf("important %+v %v %+v", important, q.timers, q.runs[1])
	}
	c.Advance(5 * time.Minute)
	due, err := r.Advance(t.Context(), nil, 71, runsT0.Add(5*time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	if due == nil || !due.Again || due.User != 21 || due.RuleRun != 71 || due.Run != 502 || due.Step != 3 ||
		due.Schedule != 11 ||
		due.OwningTeam != 7 || len(due.Sources) != 2 || due.Sources[1].TeamID != 3 || q.runs[1].State != RunDone ||
		len(q.timers) != 1 {
		t.Fatalf("later Mention %+v %+v %v", due, q.runs[1], q.timers)
	}
	// A second replica firing the same action changes nothing.
	if due, err := r.Advance(t.Context(), nil, 71, runsT0.Add(5*time.Minute), true); err != nil || due != nil {
		t.Errorf("twice %+v %v", due, err)
	}
	// The chain stops: the other rule run stops with its timer, and its action is not made.
	if err := r.StopForGroup(t.Context(), nil, 9, c.Now()); err != nil {
		t.Fatal(err)
	}
	if q.runs[2].State != RunStopped || len(q.timers) != 0 {
		t.Errorf("stopped %+v %v", q.runs[2], q.timers)
	}
	if due, err := r.Advance(t.Context(), nil, 72, runsT0.Add(5*time.Minute), true); err != nil || due != nil {
		t.Errorf("after the stop %+v %v", due, err)
	}
	if err := r.StopForGroup(t.Context(), nil, 9, c.Now()); err != nil {
		t.Error(err)
	}
}

// TestRuleRunStops: an action of a rule run whose Alert Group is no longer firing, or whose chain's run stopped, stops
// the rule run without a Mention; an action of a timer that moved since it was claimed changes nothing.
func TestRuleRunStops(t *testing.T) {
	for _, c := range []struct {
		name     string
		firing   bool
		runState string
	}{{"acknowledged", false, "running"}, {"chain stopped", true, "stopped"}} {
		t.Run(c.name, func(t *testing.T) {
			clk := clock.NewManual(runsT0)
			q := newFakeRunQueries()
			q.runState = c.runState
			r := runsOver(q, clk, nil)
			if _, err := r.Start(t.Context(), nil, Start{Group: 9, Run: 501, Step: 1, Importance: ImportanceImportant,
				Users: []int64{21}}); err != nil {
				t.Fatal(err)
			}
			if due, err := r.Advance(t.Context(), nil, 70, runsT0.Add(time.Minute), c.firing); err != nil || due != nil {
				t.Errorf("a moved timer %+v %v", due, err)
			}
			clk.Advance(5 * time.Minute)
			due, err := r.Advance(t.Context(), nil, 70, runsT0.Add(5*time.Minute), c.firing)
			if err != nil || due != nil || q.runs[0].State != RunStopped || len(q.timers) != 0 {
				t.Errorf("%+v %v %+v %v", due, err, q.runs[0], q.timers)
			}
		})
	}
}

// TestMissedMentions is the collapse of the Mentions missed while Muster was down: a rule run whose timer comes due late,
// with several Mentions overdue, makes one Page of them, and its next wait counts from now; a single overdue Mention
// keeps the rule's schedule.
func TestMissedMentions(t *testing.T) {
	c := clock.NewManual(runsT0)
	q := newFakeRunQueries()
	q.rules[21] = dbgen.GetRulesRow{State: StateCustom, Normal: []byte(`[{"kind":"mention"}]`),
		Important: []byte(`[{"kind":"mention"},{"kind":"wait","wait_seconds":300},{"kind":"mention"},` +
			`{"kind":"wait","wait_seconds":300},{"kind":"mention"},{"kind":"wait","wait_seconds":600},` +
			`{"kind":"mention"}]`)}
	r := runsOver(q, c, nil)
	started, err := r.Start(t.Context(), nil, Start{Group: 9, Run: 501, Step: 1, Importance: ImportanceImportant,
		Users: []int64{21}})
	if err != nil || started.AgainSeconds != 300 {
		t.Fatalf("%+v %v", started, err)
	}
	// Down for 12 minutes: the Mentions due at 5 and 10 minutes are one Page, and the last one 10 minutes from now.
	c.Advance(12 * time.Minute)
	due, err := r.Advance(t.Context(), nil, 70, runsT0.Add(5*time.Minute), true)
	if err != nil || due == nil {
		t.Fatalf("%+v %v", due, err)
	}
	if q.runs[0].Position != 6 || !q.timers[70].Equal(c.Now().Add(10*time.Minute)) {
		t.Errorf("collapsed %+v %v", q.runs[0], q.timers)
	}
	if m := walk(mustRule(t, q.rules[21].Important), 2, runsT0.Add(5*time.Minute), runsT0.Add(6*time.Minute)); m.mentions != 1 ||
		!m.next.Equal(runsT0.Add(10*time.Minute)) || m.again != 300 {
		t.Errorf("one overdue Mention %+v", m)
	}
	// Exactly at the end of a wait the wait is over: the Mention after it comes due with the one before.
	if m := walk(mustRule(t, q.rules[21].Important), 2, runsT0.Add(5*time.Minute), runsT0.Add(10*time.Minute)); m.mentions != 2 ||
		m.position != 6 || !m.next.Equal(runsT0.Add(20*time.Minute)) {
		t.Errorf("a wait that ends now %+v", m)
	}
	if got := againAfter([]Action{{Kind: KindWait, WaitSeconds: 60}, {Kind: KindCall}}, 0); got != 0 {
		t.Errorf("no next Mention: %d", got)
	}
}

// TestRuleRunsAgain is "again" in the Pages of rules (C-32.FR-9): the one Page of a step that names several people
// says when their rules Mention them again only when every one of them has a rule whose next Mention follows after
// the same wait; a Mention after a wait is a later one, "again", only when the rule Mentioned the person before it.
func TestRuleRunsAgain(t *testing.T) {
	rule := func(wait int) []byte {
		if wait == 0 {
			return []byte(`[{"kind":"mention"}]`)
		}
		return []byte(fmt.Sprintf(`[{"kind":"mention"},{"kind":"wait","wait_seconds":%d},{"kind":"mention"}]`, wait))
	}
	for _, c := range []struct {
		name  string
		waits [3]int
		want  int
	}{
		{"the same repeat", [3]int{300, 300, 300}, 300},
		{"a longer repeat", [3]int{300, 600, 300}, 0},
		{"a shorter repeat", [3]int{600, 300, 600}, 0},
		{"one never repeats", [3]int{300, 300, 0}, 0},
		{"the first never repeats", [3]int{0, 300, 300}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newFakeRunQueries()
			for i, w := range c.waits {
				q.rules[int64(21+i)] = dbgen.GetRulesRow{State: StateCustom, Normal: rule(w), Important: rule(w)}
			}
			started, err := runsOver(q, clock.NewManual(runsT0), nil).Start(t.Context(), nil, Start{Group: 9,
				Run: 501, Step: 1, Importance: ImportanceImportant, Users: []int64{21, 22, 23}})
			if err != nil || len(started.Mentioned) != 3 || started.AgainSeconds != c.want {
				t.Errorf("%+v %v, want again %d", started, err, c.want)
			}
		})
	}
	c := clock.NewManual(runsT0)
	q := newFakeRunQueries()
	q.rules[21] = dbgen.GetRulesRow{State: StateCustom, Normal: []byte(`[{"kind":"mention"}]`),
		Important: []byte(`[{"kind":"call","gateway_id":"CN0000000000GW"},{"kind":"wait","wait_seconds":300},` +
			`{"kind":"mention"},{"kind":"wait","wait_seconds":300},{"kind":"mention"}]`)}
	r := runsOver(q, c, nil)
	if _, err := r.Start(t.Context(), nil, Start{Group: 9, Run: 501, Step: 1, Importance: ImportanceImportant,
		Users: []int64{21}}); err != nil {
		t.Fatal(err)
	}
	c.Advance(5 * time.Minute)
	if due, err := r.Advance(t.Context(), nil, 70, runsT0.Add(5*time.Minute), true); err != nil || due == nil ||
		due.Again {
		t.Errorf("the first Mention after the call %+v %v", due, err)
	}
	c.Advance(5 * time.Minute)
	if due, err := r.Advance(t.Context(), nil, 70, runsT0.Add(10*time.Minute), true); err != nil || due == nil ||
		!due.Again {
		t.Errorf("the second Mention %+v %v", due, err)
	}
}

func mustRule(t *testing.T, raw []byte) []Action {
	t.Helper()
	r, err := parseRule(raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestRuleRunCalls: the call actions of a rule go to CallPages, at once and after a wait; without it none is made.
func TestRuleRunCalls(t *testing.T) {
	c := clock.NewManual(runsT0)
	q := newFakeRunQueries()
	q.rules[21] = dbgen.GetRulesRow{State: StateCustom, Normal: []byte(`[{"kind":"call","gateway_id":"CN0000000000G1"},` +
		`{"kind":"wait","wait_seconds":60},{"kind":"call","gateway_id":"CN0000000000G1"}]`),
		Important: []byte(`[{"kind":"mention"}]`)}
	calls := &fakeCalls{}
	r := runsOver(q, c, calls)
	started, err := r.Start(t.Context(), nil, Start{Group: 9, Run: 501, Step: 1, Importance: "normal",
		Users: []int64{21}})
	if err != nil || len(started.Mentioned) != 0 || len(calls.calls) != 1 {
		t.Fatalf("%+v %v %v", started, err, calls.calls)
	}
	c.Advance(time.Minute)
	if due, err := r.Advance(t.Context(), nil, 70, runsT0.Add(time.Minute), true); err != nil || due != nil ||
		len(calls.calls) != 2 {
		t.Errorf("%+v %v %v", due, err, calls.calls)
	}
	calls.err = errors.New("boom")
	if _, err := r.Start(t.Context(), nil, Start{Group: 9, Run: 502, Step: 1, Importance: "normal",
		Users: []int64{21}}); err == nil {
		t.Error("a failing call: no error")
	}
}

// TestRuleRunFailures: a failure of a query fails the start, the stop or the action.
func TestRuleRunFailures(t *testing.T) {
	errBoom := errors.New("boom")
	start := Start{Group: 9, Run: 501, Step: 1, Importance: ImportanceImportant, Users: []int64{21}}
	for _, name := range []string{"GetRules", "GetOrganizationRules", "InsertRuleRun", "UpsertRuleTimer",
		"NotifyRuleTimers"} {
		q := newFakeRunQueries()
		q.fail[name] = errBoom
		if _, err := runsOver(q, clock.NewManual(runsT0), nil).Start(t.Context(), nil, start); !errors.Is(err, errBoom) {
			t.Errorf("start %s: %v", name, err)
		}
	}
	q := newFakeRunQueries()
	q.rules[22] = dbgen.GetRulesRow{Normal: []byte(`{`)}
	if _, err := runsOver(q, clock.NewManual(runsT0), nil).Start(t.Context(), nil, Start{Users: []int64{22}}); err == nil {
		t.Error("a broken rule: no error")
	}
	q.rules[22] = dbgen.GetRulesRow{Normal: []byte(`[]`), Important: []byte(`{`)}
	if _, err := runsOver(q, clock.NewManual(runsT0), nil).Start(t.Context(), nil, Start{Users: []int64{22}}); err == nil {
		t.Error("a broken important rule: no error")
	}
	for _, name := range []string{"StopRuleRuns", "DeleteRuleTimers"} {
		q := newFakeRunQueries()
		r := runsOver(q, clock.NewManual(runsT0), nil)
		if _, err := r.Start(t.Context(), nil, start); err != nil {
			t.Fatal(err)
		}
		q.fail[name] = errBoom
		if err := r.StopForGroup(t.Context(), nil, 9, runsT0); !errors.Is(err, errBoom) {
			t.Errorf("stop %s: %v", name, err)
		}
	}
	for _, name := range []string{"LockRuleRun", "SaveRuleRun", "UpsertRuleTimer", "NotifyRuleTimers",
		"DeleteRuleTimers", "GetRuleRunGroup"} {
		clk := clock.NewManual(runsT0)
		q := newFakeRunQueries()
		r := runsOver(q, clk, nil)
		important := Start{Group: 9, Run: 501, Step: 1, Importance: ImportanceImportant, Users: []int64{21}}
		if name == "UpsertRuleTimer" || name == "NotifyRuleTimers" {
			q.rules[21] = dbgen.GetRulesRow{Normal: []byte(`[]`), Important: []byte(`[{"kind":"wait","wait_seconds":60},` +
				`{"kind":"mention"},{"kind":"wait","wait_seconds":60},{"kind":"mention"}]`)}
		}
		if _, err := r.Start(t.Context(), nil, important); err != nil {
			t.Fatal(err)
		}
		deadline := q.timers[70]
		clk.Set(deadline)
		q.fail[name] = errBoom
		var err error
		if name == "GetRuleRunGroup" {
			_, _, err = r.Group(t.Context(), nil, 70)
		} else {
			_, err = r.Advance(t.Context(), nil, 70, deadline, true)
		}
		if !errors.Is(err, errBoom) {
			t.Errorf("advance %s: %v", name, err)
		}
	}
	q = newFakeRunQueries()
	r := runsOver(q, clock.NewManual(runsT0), nil)
	if _, ok, err := r.Group(t.Context(), nil, 99); ok || err != nil {
		t.Errorf("a missing rule run: %v %v", ok, err)
	}
	if due, err := r.Advance(t.Context(), nil, 99, runsT0, true); due != nil || err != nil {
		t.Errorf("a missing rule run: %+v %v", due, err)
	}
	if _, err := r.Start(t.Context(), nil, start); err != nil {
		t.Fatal(err)
	}
	q.runs[0].Actions = []byte(`{`)
	if _, err := r.Advance(t.Context(), nil, 70, q.timers[70], true); err == nil {
		t.Error("broken actions: no error")
	}
	q.runs[0].Actions, q.sources = ruleJSON(Defaults().Important), []byte(`{`)
	if _, err := r.Advance(t.Context(), nil, 70, q.timers[70], true); err == nil {
		t.Error("broken sources: no error")
	}
}
