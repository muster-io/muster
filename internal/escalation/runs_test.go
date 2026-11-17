// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/schedules"
)

// fakeRun is a row of escalation_runs in memory.
type fakeRun struct {
	dbgen.InsertRunParams
	id        int64
	stepIndex int32
	repeats   []byte
	state     string
	endedAt   pgtype.Timestamptz
	stop      pgtype.Text
	stopSeq   pgtype.Int8
}

// fakeRuns are the queries of the runs in memory: the chains the Route and the Teams of the Alert Group name, the
// chains with their steps, the runs with their timers and steps, and the people.
type fakeRuns struct {
	sources  []dbgen.ListChainSourcesRow
	chains   map[int64]dbgen.ShareChainsRow
	steps    map[int64][]dbgen.SnapshotStepsRow
	runs     []*fakeRun
	timers   map[int64]time.Time
	recorded []dbgen.InsertRunStepParams
	people   map[int64]dbgen.ListPeopleStatesRow
	groups   map[string]fakeGroup
	// resolved is when the Alert Group of every run resolved, none when zero, under the retention.alert_details of
	// 90 days.
	resolved time.Time
	// order are the calls that lock chains and write runs, in their order (Lock order).
	order []string
	// store answers the Working hours, as the transaction reads them.
	store    *fakeStore
	notified int
	fail     map[string]error
	nextID   int64
}

// fakeGroup is an Alert Group as the reads of the runs know it.
type fakeGroup struct {
	id, generation int64
}

func newRuns() *fakeRuns {
	return &fakeRuns{chains: map[int64]dbgen.ShareChainsRow{}, steps: map[int64][]dbgen.SnapshotStepsRow{},
		timers: map[int64]time.Time{}, groups: map[string]fakeGroup{}, fail: map[string]error{}, nextID: 500,
		people: map[int64]dbgen.ListPeopleStatesRow{
			21: {ID: 21, PublicID: userPetr, Name: "Petr", Login: "petr", Role: "responder", Status: "active"},
			22: {ID: 22, PublicID: userIvan, Name: "Ivan", Login: "ivan", Role: "admin", Status: "active"},
			23: {ID: 23, PublicID: userVic, Name: "Vic", Login: "vic", Role: "viewer", Status: "active"},
			24: {ID: 24, PublicID: userDora, Name: "Dora", Login: "dora", Role: "responder", Status: "disabled"},
			25: {ID: 25, PublicID: userGone, Name: "deleted-user-25", Login: "deleted-user-25", Role: "responder",
				Status: "deleted"},
		}}
}

func (f *fakeRuns) run(id int64) *fakeRun {
	for _, r := range f.runs {
		if r.id == id {
			return r
		}
	}
	return nil
}

func (f *fakeRuns) GetOrganizationWorkingHours(ctx context.Context, orgID int64) (
	dbgen.GetOrganizationWorkingHoursRow, error) {
	return f.store.GetOrganizationWorkingHours(ctx, orgID)
}

func (f *fakeRuns) GetTeamWorkingHours(ctx context.Context, arg dbgen.GetTeamWorkingHoursParams) (
	dbgen.GetTeamWorkingHoursRow, error) {
	return f.store.GetTeamWorkingHours(ctx, arg)
}

func (f *fakeRuns) ListChainSources(context.Context, dbgen.ListChainSourcesParams) ([]dbgen.ListChainSourcesRow,
	error) {
	return slices.Clone(f.sources), f.fail["ListChainSources"]
}

func (f *fakeRuns) ShareChains(_ context.Context, arg dbgen.ShareChainsParams) ([]dbgen.ShareChainsRow, error) {
	f.order = append(f.order, "ShareChains")
	var out []dbgen.ShareChainsRow
	for _, id := range arg.Ids {
		if c, ok := f.chains[id]; ok {
			out = append(out, c)
		}
	}
	return out, f.fail["ShareChains"]
}

func (f *fakeRuns) SnapshotSteps(_ context.Context, arg dbgen.SnapshotStepsParams) ([]dbgen.SnapshotStepsRow, error) {
	var out []dbgen.SnapshotStepsRow
	for _, id := range arg.ChainIds {
		out = append(out, f.steps[id]...)
	}
	return out, f.fail["SnapshotSteps"]
}

func (f *fakeRuns) ListGenerationRuns(_ context.Context, arg dbgen.ListGenerationRunsParams) (
	[]dbgen.ListGenerationRunsRow, error) {
	var out []dbgen.ListGenerationRunsRow
	for _, r := range f.runs {
		if r.AlertGroupID == arg.AlertGroupID && r.Generation == arg.Generation {
			out = append(out, dbgen.ListGenerationRunsRow{ID: r.id, PublicID: r.PublicID, ChainID: r.ChainID,
				State: r.state, Sources: r.Sources})
		}
	}
	return out, f.fail["ListGenerationRuns"]
}

func (f *fakeRuns) InsertRun(_ context.Context, arg dbgen.InsertRunParams) (int64, error) {
	if err := f.fail["InsertRun"]; err != nil {
		return 0, err
	}
	f.nextID++
	f.runs = append(f.runs, &fakeRun{InsertRunParams: arg, id: f.nextID, repeats: []byte("{}"), state: RunRunning})
	return f.nextID, nil
}

func (f *fakeRuns) SetRunSources(_ context.Context, arg dbgen.SetRunSourcesParams) error {
	f.order = append(f.order, "SetRunSources")
	f.run(arg.ID).Sources = arg.Sources
	return f.fail["SetRunSources"]
}

func (f *fakeRuns) StopRuns(_ context.Context, arg dbgen.StopRunsParams) ([]dbgen.StopRunsRow, error) {
	if err := f.fail["StopRuns"]; err != nil {
		return nil, err
	}
	var out []dbgen.StopRunsRow
	for _, r := range f.runs {
		if r.AlertGroupID == arg.AlertGroupID && r.state == RunRunning {
			r.state, r.endedAt, r.stop, r.stopSeq = RunStopped, arg.Now, arg.StopCommand, arg.StopEventSeq
			out = append(out, dbgen.StopRunsRow{ID: r.id, PublicID: r.PublicID, ChainPublicID: "EC" + r.ChainName})
		}
	}
	return out, nil
}

func (f *fakeRuns) DeleteRunTimers(_ context.Context, arg dbgen.DeleteRunTimersParams) error {
	for _, id := range arg.RunIds {
		delete(f.timers, id)
	}
	return f.fail["DeleteRunTimers"]
}

func (f *fakeRuns) UpsertRunTimer(_ context.Context, arg dbgen.UpsertRunTimerParams) error {
	f.timers[arg.EscalationRunID] = arg.Deadline
	return f.fail["UpsertRunTimer"]
}

func (f *fakeRuns) NotifyRunTimers(_ context.Context, channel string) error {
	if channel != groups.TimersChannel {
		return errors.New("wrong channel " + channel)
	}
	f.notified++
	return f.fail["NotifyRunTimers"]
}

func (f *fakeRuns) GetRunGroup(_ context.Context, arg dbgen.GetRunGroupParams) (int64, error) {
	r := f.run(arg.ID)
	if r == nil {
		return 0, pgx.ErrNoRows
	}
	return r.AlertGroupID, f.fail["GetRunGroup"]
}

func (f *fakeRuns) LockRun(_ context.Context, arg dbgen.LockRunParams) (dbgen.LockRunRow, error) {
	if err := f.fail["LockRun"]; err != nil {
		return dbgen.LockRunRow{}, err
	}
	r := f.run(arg.ID)
	if r == nil {
		return dbgen.LockRunRow{}, pgx.ErrNoRows
	}
	out := dbgen.LockRunRow{ID: r.id, PublicID: r.PublicID, AlertGroupID: r.AlertGroupID, ChainID: r.ChainID,
		ChainName: r.ChainName, Steps: r.Steps, StepIndex: r.stepIndex, Repeats: r.repeats, State: r.state,
		Generation: r.Generation, Deadline: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), Sources: r.Sources}
	if c, ok := f.chains[r.ChainID.Int64]; ok && r.ChainID.Valid {
		out.ChainPublicID = pgtype.Text{String: c.PublicID, Valid: true}
	}
	out.OwningTeamID = r.OwningTeamID
	if d, ok := f.timers[r.id]; ok {
		out.Deadline = d
	}
	for _, s := range f.recorded {
		if s.RunID == r.id {
			out.StepsMade++
		}
	}
	return out, nil
}

func (f *fakeRuns) SaveRun(_ context.Context, arg dbgen.SaveRunParams) error {
	r := f.run(arg.ID)
	r.stepIndex, r.repeats, r.state, r.endedAt = arg.StepIndex, arg.Repeats, arg.State, arg.EndedAt
	return f.fail["SaveRun"]
}

func (f *fakeRuns) InsertRunStep(_ context.Context, arg dbgen.InsertRunStepParams) error {
	f.recorded = append(f.recorded, arg)
	return f.fail["InsertRunStep"]
}

func (f *fakeRuns) CountGenerationRuns(_ context.Context, arg dbgen.CountGenerationRunsParams) (
	dbgen.CountGenerationRunsRow, error) {
	var c dbgen.CountGenerationRunsRow
	for _, r := range f.runs {
		if r.AlertGroupID != arg.AlertGroupID || r.Generation != arg.Generation {
			continue
		}
		c.Total++
		switch r.state {
		case RunRanOut:
			c.RanOut++
		case RunStopped:
			c.Stopped++
		}
	}
	return c, f.fail["CountGenerationRuns"]
}

func (f *fakeRuns) ListPeopleStates(_ context.Context, arg dbgen.ListPeopleStatesParams) (
	[]dbgen.ListPeopleStatesRow, error) {
	var out []dbgen.ListPeopleStatesRow
	for _, id := range arg.Ids {
		if p, ok := f.people[id]; ok {
			out = append(out, p)
		}
	}
	return out, f.fail["ListPeopleStates"]
}

func (f *fakeRuns) ListGroupRuns(_ context.Context, arg dbgen.ListGroupRunsParams) ([]dbgen.ListGroupRunsRow, error) {
	var out []dbgen.ListGroupRunsRow
	for _, pid := range arg.PublicIds {
		g, ok := f.groups[pid]
		if !ok {
			continue
		}
		for _, r := range f.runs {
			if r.AlertGroupID != g.id || r.Generation != g.generation {
				continue
			}
			row := dbgen.ListGroupRunsRow{GroupPublicID: pid, ID: r.id, PublicID: r.PublicID, ChainID: r.ChainID,
				ChainName: r.ChainName, Sources: r.Sources, Steps: r.Steps, StepIndex: r.stepIndex, State: r.state,
				StartedAt: r.Now, Deadline: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)}
			if c, ok := f.chains[r.ChainID.Int64]; ok && r.ChainID.Valid {
				row.ChainPublicID = pgtype.Text{String: c.PublicID, Valid: true}
				row.CurrentChainName = pgtype.Text{String: c.Name, Valid: true}
				if c.OwningTeamID.Valid {
					row.TeamPublicID = pgtype.Text{String: teamPay, Valid: true}
					row.TeamName = pgtype.Text{String: "payments", Valid: true}
					row.TeamSlug = pgtype.Text{String: "payments", Valid: true}
				}
			}
			if d, ok := f.timers[r.id]; ok {
				row.Deadline = d
			}
			out = append(out, row)
		}
	}
	return out, f.fail["ListGroupRuns"]
}

func (f *fakeRuns) ListTeamRefs(_ context.Context, arg dbgen.ListTeamRefsParams) ([]dbgen.ListTeamRefsRow, error) {
	var out []dbgen.ListTeamRefsRow
	for _, id := range arg.Ids {
		if id == 1 {
			out = append(out, dbgen.ListTeamRefsRow{ID: 1, PublicID: teamPay, Name: "payments", Slug: "payments"})
		}
	}
	return out, f.fail["ListTeamRefs"]
}

func (f *fakeRuns) GetChainID(_ context.Context, arg dbgen.GetChainIDParams) (int64, error) {
	for _, c := range f.chains {
		if c.PublicID == arg.PublicID {
			return c.ID, f.fail["GetChainID"]
		}
	}
	return 0, pgx.ErrNoRows
}

func (f *fakeRuns) ListChainRuns(_ context.Context, arg dbgen.ListChainRunsParams) ([]dbgen.ListChainRunsRow, error) {
	var out []dbgen.ListChainRunsRow
	for i := len(f.runs) - 1; i >= 0; i-- {
		r := f.runs[i]
		if r.ChainID != arg.ChainID || (arg.AfterID.Valid && r.id >= arg.AfterID.Int64) {
			continue
		}
		row := dbgen.ListChainRunsRow{ID: r.id, PublicID: r.PublicID, State: r.state, StartedAt: r.Now,
			EndedAt: r.endedAt, StopCommand: r.stop, GroupPublicID: "AG0000000000G1", GroupNumber: 1,
			GroupTitle: "OOM", GroupSeverity: "warning", RetentionAlertDetailsDays: 90,
			GroupResolvedAt: pgtype.Timestamptz{Time: f.resolved, Valid: !f.resolved.IsZero()}}
		if r.stopSeq.Valid {
			row.ActorKind, row.UserPublicID = "user", pgtype.Text{String: userIvan, Valid: true}
			row.UserName, row.UserLogin = pgtype.Text{String: "Ivan", Valid: true}, pgtype.Text{String: "ivan", Valid: true}
		}
		out = append(out, row)
		if len(out) == int(arg.PageSize) {
			break
		}
	}
	return out, f.fail["ListChainRuns"]
}

func (f *fakeRuns) ListRunSteps(_ context.Context, arg dbgen.ListRunStepsParams) ([]dbgen.ListRunStepsRow, error) {
	var out []dbgen.ListRunStepsRow
	for i, s := range f.recorded {
		if slices.Contains(arg.RunIds, s.RunID) {
			out = append(out, dbgen.ListRunStepsRow{RunID: s.RunID, Seq: int32(i + 1), Number: s.Number, Kind: s.Kind,
				At: s.At, Outcome: s.Outcome, Reason: s.Reason})
		}
	}
	return out, f.fail["ListRunSteps"]
}

// fakeDispatcher is the dispatcher of the steps: it runs a step on its Alert Group and keeps what the step did.
type fakeDispatcher struct {
	group    *groups.Group
	progress []groups.ChainProgress
	err      error
}

func (d *fakeDispatcher) ChainStep(ctx context.Context, _ groups.DBTX, groupID int64,
	step func(ctx context.Context, g *groups.Group) (groups.ChainProgress, error)) (func(context.Context), error) {
	if d.err != nil {
		return nil, d.err
	}
	if d.group == nil || d.group.ID != groupID {
		return nil, nil
	}
	p, err := step(ctx, d.group)
	if err != nil {
		return nil, err
	}
	d.progress = append(d.progress, p)
	return func(context.Context) {}, nil
}

// fakeOnCalls is the On-call of the Schedules: the person of each Schedule by public_id, a gap for none.
type fakeOnCalls struct {
	onCall map[string]int64
}

func (o *fakeOnCalls) OnCallAtIn(_ context.Context, _ mentions.DBTX, scheduleID string, _ time.Time) (
	schedules.Segment, error) {
	id, ok := o.onCall[scheduleID]
	if !ok {
		return schedules.Segment{}, schedules.ErrNotFound
	}
	if id == 0 {
		return schedules.Segment{}, nil
	}
	return schedules.Segment{User: &schedules.Person{ID: id}}, nil
}

// runsEnv is a Service of chains with its runs over the fakes, its business clock, the log and an Alert Group #1,
// firing, warning, in its first start of the chains.
type runsEnv struct {
	runs    *Runs
	q       *fakeRuns
	store   *fakeStore
	clock   *clock.Manual
	groups  *fakeDispatcher
	onCalls *fakeOnCalls
	log     *bytes.Buffer
	g       *groups.Group
}

func newRunsEnv(t *testing.T) *runsEnv {
	t.Helper()
	s, store := newService(t)
	e := &runsEnv{q: newRuns(), store: store, clock: s.clock.(*clock.Manual), log: &bytes.Buffer{},
		onCalls: &fakeOnCalls{onCall: map[string]int64{schedMain: 21}}}
	e.g = &groups.Group{ID: 1, PublicID: "AG0000000000G1", Number: 1, RouteID: 2, Status: groups.StatusFiring,
		Severity: organization.SeverityWarning, ChainGeneration: 1}
	e.groups = &fakeDispatcher{group: e.g}
	e.runs = s.Runs(RunsConfig{Groups: e.groups, OnCalls: e.onCalls, Log: logging.New(e.log, logging.LevelInfo)})
	e.runs.queries = func(DBTX) runQueries { return e.q }
	e.q.store = store
	e.q.groups[e.g.PublicID] = fakeGroup{id: 1, generation: 1}
	return e
}

// chain adds the chain id named name, owned by the Team 1 when owned, with steps, named by the Route with the
// condition route and by the Team team with teamCondition, each when not empty.
func (e *runsEnv) chain(id int64, name string, owned bool, steps ...dbgen.SnapshotStepsRow) {
	c := dbgen.ShareChainsRow{ID: id, PublicID: fmt.Sprintf("EC%012d", id), Name: name}
	if owned {
		c.OwningTeamID = pgtype.Int8{Int64: 1, Valid: true}
	}
	e.q.chains[id] = c
	for i := range steps {
		steps[i].ChainID, steps[i].Position = id, int32(i+1)
		if steps[i].SeverityCondition == "" {
			steps[i].SeverityCondition = ConditionAny
		}
	}
	e.q.steps[id] = steps
}

// source names the chain on the Route (team 0) or a Team, with a condition.
func (e *runsEnv) source(chain, team int64, condition string) {
	kind := SourceRoute
	if team != 0 {
		kind = SourceTeam
	}
	e.q.sources = append(e.q.sources, dbgen.ListChainSourcesRow{Kind: kind, TeamID: team, ChainID: chain,
		SeverityCondition: condition})
}

func (e *runsEnv) start(t *testing.T, s groups.ChainStart) int {
	t.Helper()
	if s.At.IsZero() {
		s.At = e.clock.Now()
	}
	n, err := e.runs.Start(t.Context(), nil, e.g, s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fire fires the timer of the run at its deadline, as the timer worker does.
func (e *runsEnv) fire(t *testing.T, run int64) {
	t.Helper()
	after, err := e.runs.FireStep(t.Context(), nil, run, e.q.timers[run])
	if err != nil {
		t.Fatal(err)
	}
	if after != nil {
		after(t.Context())
	}
}

func page(schedule string) dbgen.SnapshotStepsRow {
	return dbgen.SnapshotStepsRow{Kind: KindPageOnCall, Importance: pgtype.Text{String: ImportanceNormal, Valid: true},
		ScheduleID: pgtype.Int8{Int64: 11, Valid: true}, SchedulePublicID: pgtype.Text{String: schedule, Valid: true}}
}

func wait(seconds int32) dbgen.SnapshotStepsRow {
	return dbgen.SnapshotStepsRow{Kind: KindWait, WaitSeconds: pgtype.Int4{Int32: seconds, Valid: true}}
}

func people(ids ...int64) dbgen.SnapshotStepsRow {
	return dbgen.SnapshotStepsRow{Kind: KindPagePeople, Importance: pgtype.Text{String: ImportanceImportant,
		Valid: true}, UserIds: ids}
}

func team() dbgen.SnapshotStepsRow {
	return dbgen.SnapshotStepsRow{Kind: KindPageTeam}
}

func repeat(from, times int32) dbgen.SnapshotStepsRow {
	return dbgen.SnapshotStepsRow{Kind: KindRepeat, RepeatFrom: pgtype.Int4{Int32: from, Valid: true},
		RepeatTimes: pgtype.Int4{Int32: times, Valid: true}}
}

func critical(s dbgen.SnapshotStepsRow) dbgen.SnapshotStepsRow {
	s.SeverityCondition = ConditionCritical
	return s
}

func workingHours(s dbgen.SnapshotStepsRow) dbgen.SnapshotStepsRow {
	s.WorkingHoursOnly = true
	return s
}

// sourcesOf are the sources of a run.
func sourcesOf(t *testing.T, r *fakeRun) []source {
	t.Helper()
	var out []source
	if err := json.Unmarshal(r.Sources, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestStartWhichChains is C-32.FR-4, C-26.FR-2, C-32.AC-12 and P-70: a start runs each chain of the Route and of the
// Teams whose Severity condition the level meets, a chain the Route and a Team both name once with both as its
// sources, each run at its first step with its timer due at once; a chain whose condition is not met does not start.
func TestStartWhichChains(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain))
	e.chain(2, "payments", true, page(schedMain))
	e.chain(3, "critical-only", false, page(schedMain))
	e.source(1, 0, ConditionAny)
	e.source(2, 0, ConditionBelowCritical)
	e.source(2, 1, ConditionAny)
	e.source(3, 0, ConditionCritical)
	if n := e.start(t, groups.ChainStart{All: true}); n != 2 || len(e.q.runs) != 2 || e.q.notified != 1 {
		t.Fatalf("%d started, %d runs, %d notifications", n, len(e.q.runs), e.q.notified)
	}
	devops, payments := e.q.runs[0], e.q.runs[1]
	if devops.ChainName != "devops" || payments.ChainName != "payments" || devops.Generation != 1 ||
		!strings.HasPrefix(devops.PublicID, "ER") || len(devops.PublicID) != 14 {
		t.Errorf("runs %+v %+v", devops, payments)
	}
	if got := sourcesOf(t, payments); !slices.Equal(got, []source{{Kind: SourceRoute}, {Kind: SourceTeam, TeamID: 1}}) {
		t.Errorf("sources of payments %+v", got)
	}
	if !e.q.timers[devops.id].Equal(t0) || devops.stepIndex != 0 {
		t.Errorf("timer %v, step %d", e.q.timers[devops.id], devops.stepIndex)
	}
	if !strings.Contains(e.log.String(), `"event":"escalation_run_started","group":"AG0000000000G1","chain":"EC000000000001"`) {
		t.Errorf("log %s", e.log)
	}
	// A start-over stops a run still running from an earlier start as started_over.
	e.g.ChainGeneration = 2
	e.q.groups[e.g.PublicID] = fakeGroup{id: 1, generation: 2}
	if n := e.start(t, groups.ChainStart{All: true}); n != 2 || devops.state != RunStopped ||
		devops.stop.String != groups.StopStartedOver || len(e.q.timers) != 2 {
		t.Errorf("start-over: %d started, old run %s %s, %d timers", n, devops.state, devops.stop.String, len(e.q.timers))
	}
}

// TestStartWhileRunning is C-32.FR-5 and P-70: while the chains run, a rise starts only the chains whose condition it
// newly meets and a Team that joins only its chains; a chain that already runs takes the new source and is not started
// again.
func TestStartWhileRunning(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain))
	e.chain(2, "payments", true, page(schedMain))
	e.chain(3, "critical-only", false, page(schedMain))
	e.chain(4, "new on the route", false, page(schedMain))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.source(3, 0, ConditionCritical)
	e.source(4, 0, ConditionAny)
	e.source(1, 1, ConditionCritical)
	e.g.Severity = organization.SeverityCritical
	e.q.order = nil
	if n := e.start(t, groups.ChainStart{Rise: true, Before: organization.SeverityWarning}); n != 1 ||
		len(e.q.runs) != 2 || e.q.runs[1].ChainName != "critical-only" {
		t.Fatalf("rise: %d started, %d runs", n, len(e.q.runs))
	}
	if got := sourcesOf(t, e.q.runs[0]); len(got) != 2 {
		t.Errorf("the running chain's sources %+v", got)
	}
	// The chains are locked before any run is written, the sources added to a running one included: within this
	// start, the lock comes first.
	if !slices.Equal(e.q.order, []string{"ShareChains", "SetRunSources"}) {
		t.Errorf("order %v", e.q.order)
	}
	e.source(2, 1, ConditionAny)
	if n := e.start(t, groups.ChainStart{Teams: []int64{1}}); n != 1 || e.q.runs[2].ChainName != "payments" {
		t.Errorf("a team joined: %d started, %d runs", n, len(e.q.runs))
	}
	if n := e.start(t, groups.ChainStart{Teams: []int64{9}}); n != 0 {
		t.Errorf("another team: %d started", n)
	}
	// No source at all starts nothing.
	e.q.sources = nil
	if n := e.start(t, groups.ChainStart{All: true}); n != 0 {
		t.Errorf("no source: %d started", n)
	}
}

// TestStartAfterAQuietPublication is C-32.FR-5 and C-32.AC-3: in the first start of the chains of an Alert Group
// published Quietly by a change of configuration — no chain started at its Publication — a rise by Alerts starts only
// the chains whose condition it newly meets, not those for any level, and a Team that joins only the Team's chains;
// the run takes the start the dispatcher opened (generation 1).
func TestStartAfterAQuietPublication(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "any", false, page(schedMain))
	e.chain(2, "critical-only", false, page(schedMain))
	e.chain(3, "payments", true, page(schedMain))
	e.source(1, 0, ConditionAny)
	e.source(2, 0, ConditionCritical)
	e.source(3, 1, ConditionAny)
	e.g.Severity = organization.SeverityCritical
	if n := e.start(t, groups.ChainStart{Rise: true, Before: organization.SeverityWarning}); n != 1 ||
		e.q.runs[0].ChainName != "critical-only" || e.q.runs[0].Generation != 1 {
		t.Fatalf("rise: %d started, %+v", n, e.q.runs)
	}
	if n := e.start(t, groups.ChainStart{Teams: []int64{1}}); n != 1 || e.q.runs[1].ChainName != "payments" {
		t.Errorf("team: %d started, %d runs", n, len(e.q.runs))
	}
}

// TestStartOverWithoutChains: a start-over where no chain applies any more still stops the runs still running from the
// earlier start, as started_over.
func TestStartOverWithoutChains(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.q.sources = nil
	e.g.ChainGeneration = 2
	if n := e.start(t, groups.ChainStart{All: true}); n != 0 || e.q.runs[0].state != RunStopped ||
		e.q.runs[0].stop.String != groups.StopStartedOver || len(e.q.timers) != 0 {
		t.Errorf("%d started, run %+v, %d timers", n, e.q.runs[0], len(e.q.timers))
	}
}

// TestStopRuns is C-32.FR-5 and C-10.FR-1: Stop stops only the runs that still run, with the Command and its
// lifecycle event, and deletes their timers; a run that ran out stays as it is.
func TestStopRuns(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain))
	e.chain(2, "payments", true, page(schedMain), wait(600), page(schedMain))
	e.source(1, 0, ConditionAny)
	e.source(2, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	devops, payments := e.q.runs[0], e.q.runs[1]
	e.fire(t, devops.id)
	e.fire(t, payments.id)
	if devops.state != RunRanOut || payments.state != RunRunning {
		t.Fatalf("states %s %s", devops.state, payments.state)
	}
	if err := e.runs.Stop(t.Context(), nil, e.g, groups.ChainStop{Command: groups.StopAcknowledge, EventSeq: 7,
		At: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if devops.state != RunRanOut || payments.state != RunStopped || payments.stop.String != groups.StopAcknowledge ||
		payments.stopSeq.Int64 != 7 || len(e.q.timers) != 0 || !payments.endedAt.Valid {
		t.Errorf("after the stop: %s, %+v, %d timers", devops.state, payments, len(e.q.timers))
	}
	if !strings.Contains(e.log.String(), `"event":"escalation_run_stopped"`) ||
		!strings.Contains(e.log.String(), `"command":"acknowledge"`) {
		t.Errorf("log %s", e.log)
	}
	ok, err := e.runs.Started(t.Context(), nil, e.g)
	out, err2 := e.runs.RanOut(t.Context(), nil, e.g)
	if !ok || out || err != nil || err2 != nil {
		t.Errorf("started %v, ran out %v: %v %v", ok, out, err, err2)
	}
	// Nothing left to stop.
	if err := e.runs.Stop(t.Context(), nil, e.g, groups.ChainStop{Command: groups.StopResolve}); err != nil {
		t.Error(err)
	}
	e.g.ChainGeneration = 0
	if ok, _ := e.runs.Started(t.Context(), nil, e.g); ok {
		t.Error("started before any start")
	}
}

// TestRunKeepsItsSteps is C-32.FR-16, C-32.AC-16 and P-73: a run keeps the steps its chain had when it started; a
// change of the chain while it runs leaves it on its old steps, and the next run takes the new ones.
func TestRunKeepsItsSteps(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain), wait(600), page(schedMain))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	first := e.q.runs[0]
	e.fire(t, first.id)
	// The chain changes: one step only, a Page of people.
	e.chain(1, "devops", false, people(21))
	e.clock.Advance(10 * time.Minute)
	e.fire(t, first.id)
	var kinds []string
	for _, s := range e.q.recorded {
		kinds = append(kinds, s.Kind)
	}
	if first.state != RunRanOut || !slices.Equal(kinds, []string{KindPageOnCall, KindWait, KindPageOnCall}) {
		t.Errorf("the running run: %s %v", first.state, kinds)
	}
	e.g.ChainGeneration = 2
	e.start(t, groups.ChainStart{All: true})
	next := e.q.runs[1]
	e.fire(t, next.id)
	if got := e.q.recorded[len(e.q.recorded)-1]; next.state != RunRanOut || got.Kind != KindPagePeople ||
		got.RunID != next.id {
		t.Errorf("the next run: %s %+v", next.state, got)
	}
}

// TestGroupChains is C-32.FR-18 and C-09.FR-1: the read of an Alert Group gives the runs of the current start of its
// chains with their chains, sources, states, the step each is at and when the next is due.
func TestGroupChains(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain), wait(600), page(schedMain))
	e.chain(2, "payments", true, page(schedMain))
	e.source(1, 0, ConditionAny)
	e.source(2, 1, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	got, err := e.runs.GroupChains(t.Context(), []string{e.g.PublicID})
	if err != nil {
		t.Fatal(err)
	}
	if c := got[e.g.PublicID]; len(c) != 2 || c[0].Step.Number != 1 || c[0].Step.Kind != KindPageOnCall ||
		!c[0].NextStepAt.Equal(t0) || c[0].Chain.Name != "devops" || c[1].Sources[0].Team.PublicID != teamPay ||
		c[1].Chain.OwningTeam == nil {
		t.Fatalf("before a step: %+v", c)
	}
	e.fire(t, e.q.runs[0].id)
	e.fire(t, e.q.runs[1].id)
	got, _ = e.runs.GroupChains(t.Context(), []string{e.g.PublicID, "AG00000000NONE"})
	c := got[e.g.PublicID]
	if c[0].State != RunRunning || c[0].Step.Number != 2 || c[0].Step.Kind != KindWait ||
		!c[0].NextStepAt.Equal(t0.Add(10*time.Minute)) || c[1].State != RunRanOut || c[1].Step != nil ||
		c[1].NextStepAt != nil || len(got["AG00000000NONE"]) != 0 {
		t.Errorf("after a step: %+v", c)
	}
	if out, err := e.runs.GroupChains(t.Context(), nil); err != nil || len(out) != 0 {
		t.Errorf("no alert group: %v %v", out, err)
	}
	// A run whose chain was deleted keeps its name.
	e.q.runs[1].ChainID = pgtype.Int8{}
	got, _ = e.runs.GroupChains(t.Context(), []string{e.g.PublicID})
	if c := got[e.g.PublicID][1]; c.Chain != nil || c.ChainName != "payments" {
		t.Errorf("a deleted chain: %+v", c)
	}
}

// TestChainRuns is C-32.FR-18 and C-32.AC-17: "How it went" lists the runs of a chain newest first, each with its
// Alert Group, its steps made and skipped with their reasons, and what stopped it — the Command and its actor — or
// that it ran out, in pages; an unknown chain is ErrNotFound.
func TestChainRuns(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "payments", true, page(schedMain), critical(people(21)), wait(600), page(schedMain))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.fire(t, e.q.runs[0].id)
	if err := e.runs.Stop(t.Context(), nil, e.g, groups.ChainStop{Command: groups.StopAcknowledge, EventSeq: 4,
		At: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	e.g.ChainGeneration = 2
	e.start(t, groups.ChainStart{All: true})
	page1, err := e.runs.ChainRuns(t.Context(), e.q.chains[1].PublicID, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Runs) != 1 || page1.Runs[0].State != RunRunning || page1.Runs[0].StoppedBy != nil ||
		page1.Next == nil || len(page1.Runs[0].Steps) != 0 {
		t.Fatalf("first page %+v", page1)
	}
	page2, err := e.runs.ChainRuns(t.Context(), e.q.chains[1].PublicID, page1.Next, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := page2.Runs[0]
	if r.State != RunStopped || r.StoppedBy == nil || r.StoppedBy.Command != groups.StopAcknowledge ||
		r.StoppedBy.Actor == nil || r.StoppedBy.Actor.PublicID != userIvan || r.EndedAt == nil ||
		r.Group.Number != 1 || len(r.Steps) != 3 || r.Steps[1].Outcome != OutcomeSkipped ||
		r.Steps[1].Reason != groups.SkipSeverity || r.Steps[0].Outcome != OutcomeMade || page2.Next != nil {
		t.Errorf("second page %+v", page2)
	}
	if _, err := e.runs.ChainRuns(t.Context(), "EC00000000NONE", nil, 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown chain: %v", err)
	}
}

// TestChainRunsHideRemovedDetails is C-09.FR-16 and C-32.FR-18: "How it went" gives no steps for a run whose Alert
// Group resolved retention.alert_details ago, whether or not the retention deleted them yet, and gives them for one
// resolved since.
func TestChainRunsHideRemovedDetails(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain), wait(600), page(schedMain))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.fire(t, e.q.runs[0].id)
	for _, c := range []struct {
		resolved time.Time
		steps    int
	}{{time.Time{}, 2}, {t0.Add(-89 * 24 * time.Hour), 2}, {t0.Add(-91 * 24 * time.Hour), 0}} {
		e.q.resolved = c.resolved
		page, err := e.runs.ChainRuns(t.Context(), e.q.chains[1].PublicID, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Runs) != 1 || len(page.Runs[0].Steps) != c.steps || page.Runs[0].Steps == nil {
			t.Errorf("resolved %v: %+v", c.resolved, page.Runs)
		}
	}
}

// TestRunsFailures: a failure of a query fails the start, the stop or the read that made it.
func TestRunsFailures(t *testing.T) {
	for _, name := range []string{"ListChainSources", "ListGenerationRuns", "ShareChains", "SnapshotSteps",
		"InsertRun", "UpsertRunTimer", "NotifyRunTimers", "StopRuns"} {
		t.Run(name, func(t *testing.T) {
			e := newRunsEnv(t)
			e.chain(1, "devops", false, page(schedMain))
			e.source(1, 0, ConditionAny)
			e.q.fail[name] = errors.New("boom")
			if _, err := e.runs.Start(t.Context(), nil, e.g, groups.ChainStart{All: true, At: t0}); err == nil {
				t.Error("no error")
			}
		})
	}
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.q.fail["DeleteRunTimers"] = errors.New("boom")
	if err := e.runs.Stop(t.Context(), nil, e.g, groups.ChainStop{Command: groups.StopResolve}); err == nil {
		t.Error("stop: no error")
	}
	e.q.fail["CountGenerationRuns"] = errors.New("boom")
	if _, err := e.runs.RanOut(t.Context(), nil, e.g); err == nil {
		t.Error("ran out: no error")
	}
	e.q.fail["ListGroupRuns"] = errors.New("boom")
	if _, err := e.runs.GroupChains(t.Context(), []string{e.g.PublicID}); err == nil {
		t.Error("group chains: no error")
	}
	e.q.fail["GetChainID"] = errors.New("boom")
	if _, err := e.runs.ChainRuns(t.Context(), e.q.chains[1].PublicID, nil, 10); err == nil {
		t.Error("chain runs: no error")
	}
	e.q.fail["GetChainID"] = nil
	e.q.fail["ListChainRuns"] = errors.New("boom")
	if _, err := e.runs.ChainRuns(t.Context(), e.q.chains[1].PublicID, nil, 10); err == nil {
		t.Error("chain runs: no error")
	}
}
