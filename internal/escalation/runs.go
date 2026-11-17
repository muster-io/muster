// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/schedules"
)

// The runs of Escalation chains on Alert Groups (C-32.FR-4 to FR-7, FR-14 to FR-16, FR-18). A run is one chain on one
// Alert Group from a start of its chains (alert_groups.chain_generation): its sources — the Route and the Teams that
// named the chain, one run however many name it (P-70) —, the steps the chain had when it started, kept until it
// stops or starts over (P-73), where it is, the passes of its repeats, and its state: running, ran_out once it passed
// its last step, stopped by Acknowledge, Snooze or Resolve. Its step is a timers row of the kind escalation_step whose
// subject is the run (schema.md §4.10), which any replica claims; the step runs as a system transition of the
// dispatcher (engine.go).
//
// Lock order of runs. Every write of a run happens in the dispatcher's transaction, after the lock of its Alert Group:
// then the chains a start runs FOR KEY SHARE, in id order, before any run is written — the deletion of a chain locks it
// FOR UPDATE and then sets chain_id of its runs to null, so it waits for the start or the start finds it gone, never
// both holding what the other waits for — then the runs, then their timers. A step locks its Alert Group, then its run,
// then writes its timer; a webhook step reads its Destination without a lock (webhook.go). No transaction locks a run
// before its Alert Group.

// The states of a run, and the outcomes of its steps.
const (
	RunRunning     = "running"
	RunRanOut      = "ran_out"
	RunStopped     = "stopped"
	OutcomeMade    = "made"
	OutcomeSkipped = "skipped"
)

// The kinds of the source of a run.
const (
	SourceRoute = "route"
	SourceTeam  = "team"
)

// TimerKind is the kind of the timer of a run.
const TimerKind = "escalation_step"

// source is what named the chain of a run: its Route, or one of the Teams of the Alert Group by id.
type source struct {
	Kind   string `json:"kind"`
	TeamID int64  `json:"team_id,omitempty"`
}

// runStep is a step of a run as the chain had it when the run started (P-73), by the internal ids of what it names, the
// Schedule also by public_id.
type runStep struct {
	Number           int     `json:"number"`
	Kind             string  `json:"kind"`
	Importance       string  `json:"importance,omitempty"`
	ScheduleID       int64   `json:"schedule_id,omitempty"`
	Schedule         string  `json:"schedule,omitempty"`
	UserIDs          []int64 `json:"user_ids,omitempty"`
	TeamID           int64   `json:"team_id,omitempty"`
	Everyone         string  `json:"everyone,omitempty"`
	CallTarget       string  `json:"call_target,omitempty"`
	GatewayID        int64   `json:"gateway_id,omitempty"`
	WaitSeconds      int     `json:"wait_seconds,omitempty"`
	DestinationID    int64   `json:"destination_id,omitempty"`
	RepeatFrom       int     `json:"repeat_from,omitempty"`
	RepeatTimes      int     `json:"repeat_times,omitempty"`
	WorkingHoursOnly bool    `json:"working_hours_only,omitempty"`
	Condition        string  `json:"severity_condition,omitempty"`
}

func stepOfSnapshot(r dbgen.SnapshotStepsRow) runStep {
	return runStep{Number: int(r.Position), Kind: r.Kind, Importance: r.Importance.String, ScheduleID: r.ScheduleID.Int64,
		Schedule: r.SchedulePublicID.String, UserIDs: r.UserIds, TeamID: r.TeamID.Int64, Everyone: r.Everyone.String,
		CallTarget: r.CallTarget.String, GatewayID: r.GatewayConnectionID.Int64, WaitSeconds: int(r.WaitSeconds.Int32),
		DestinationID: r.DestinationID.Int64, RepeatFrom: int(r.RepeatFrom.Int32),
		RepeatTimes: int(r.RepeatTimes.Int32), WorkingHoursOnly: r.WorkingHoursOnly, Condition: r.SeverityCondition}
}

// runQueries are the queries of the runs, in the dispatcher's transaction.
type runQueries interface {
	workingHoursQueries
	ListChainSources(ctx context.Context, arg dbgen.ListChainSourcesParams) ([]dbgen.ListChainSourcesRow, error)
	ShareChains(ctx context.Context, arg dbgen.ShareChainsParams) ([]dbgen.ShareChainsRow, error)
	SnapshotSteps(ctx context.Context, arg dbgen.SnapshotStepsParams) ([]dbgen.SnapshotStepsRow, error)
	ListGenerationRuns(ctx context.Context, arg dbgen.ListGenerationRunsParams) ([]dbgen.ListGenerationRunsRow, error)
	InsertRun(ctx context.Context, arg dbgen.InsertRunParams) (int64, error)
	SetRunSources(ctx context.Context, arg dbgen.SetRunSourcesParams) error
	StopRuns(ctx context.Context, arg dbgen.StopRunsParams) ([]dbgen.StopRunsRow, error)
	DeleteRunTimers(ctx context.Context, arg dbgen.DeleteRunTimersParams) error
	UpsertRunTimer(ctx context.Context, arg dbgen.UpsertRunTimerParams) error
	NotifyRunTimers(ctx context.Context, channel string) error
	GetRunGroup(ctx context.Context, arg dbgen.GetRunGroupParams) (int64, error)
	LockRun(ctx context.Context, arg dbgen.LockRunParams) (dbgen.LockRunRow, error)
	SaveRun(ctx context.Context, arg dbgen.SaveRunParams) error
	InsertRunStep(ctx context.Context, arg dbgen.InsertRunStepParams) error
	CountGenerationRuns(ctx context.Context, arg dbgen.CountGenerationRunsParams) (dbgen.CountGenerationRunsRow, error)
	ListPeopleStates(ctx context.Context, arg dbgen.ListPeopleStatesParams) ([]dbgen.ListPeopleStatesRow, error)
	GetStepDestination(ctx context.Context, arg dbgen.GetStepDestinationParams) (dbgen.GetStepDestinationRow, error)
	ListGroupRuns(ctx context.Context, arg dbgen.ListGroupRunsParams) ([]dbgen.ListGroupRunsRow, error)
	ListTeamRefs(ctx context.Context, arg dbgen.ListTeamRefsParams) ([]dbgen.ListTeamRefsRow, error)
	GetChainID(ctx context.Context, arg dbgen.GetChainIDParams) (int64, error)
	ListChainRuns(ctx context.Context, arg dbgen.ListChainRunsParams) ([]dbgen.ListChainRunsRow, error)
	ListRunSteps(ctx context.Context, arg dbgen.ListRunStepsParams) ([]dbgen.ListRunStepsRow, error)
}

// Dispatcher is the dispatcher of Alert Groups through which a step changes its Alert Group:
// (*groups.Service).ChainStep.
type Dispatcher interface {
	ChainStep(ctx context.Context, tx groups.DBTX, groupID int64,
		step func(ctx context.Context, g *groups.Group) (groups.ChainProgress, error)) (func(context.Context), error)
}

// OnCalls finds the person On-call in a Schedule at a time, nil in a gap, read through the step's transaction:
// (*schedules.Service).OnCallAtIn (S-131).
type OnCalls interface {
	OnCallAtIn(ctx context.Context, db mentions.DBTX, scheduleID string, t time.Time) (schedules.Segment, error)
}

// StepPages makes what a step that Pages does — a Page of the On-call, of chosen people or of a whole Team, a call —
// once the run decided to make it, with the people it may Page: S-155 and S-157 implement it. Until they do none is
// wired, so such a step is made without Paging anyone. A webhook step does not reach it: the run records its
// escalation_webhook itself (webhook.go).
type StepPages interface {
	MakeStep(ctx context.Context, tx groups.DBTX, g *groups.Group, run RunRef, step StepRef, people []int64) error
}

// RunRef names a run and its chain for StepPages.
type RunRef struct {
	ID       int64
	PublicID string
	ChainID  int64
	Sources  []RunSource
}

// RunSource is a source of a run: the Route, or a Team by id.
type RunSource struct {
	Kind   string
	TeamID int64
}

// StepRef is the step of a run that StepPages makes, as the chain had it when the run started.
type StepRef struct {
	Number     int
	Kind       string
	Importance string
	ScheduleID int64
	TeamID     int64
	Everyone   string
	GatewayID  int64
}

// Runs holds the runs of the Organization's Escalation chains and implements groups.ChainRuns. Its zero value runs
// nothing; RunsConfig fills it.
type Runs struct {
	s       *Service
	queries func(DBTX) runQueries
	db      DBTX
	groups  Dispatcher
	onCalls OnCalls
	pages   StepPages
	log     *logging.Logger
}

// RunsConfig is what the runs need: the main pool for the reads, the dispatcher, the On-call of Schedules and the log;
// Pages is nil until S-155.
type RunsConfig struct {
	DB      DBTX
	Groups  Dispatcher
	OnCalls OnCalls
	Pages   StepPages
	Log     *logging.Logger
}

// Runs returns the runs of the chains of s.
func (s *Service) Runs(cfg RunsConfig) *Runs {
	return &Runs{s: s, queries: func(d DBTX) runQueries { return queriesOver(d) }, db: cfg.DB, groups: cfg.Groups,
		onCalls: cfg.OnCalls, pages: cfg.Pages, log: cfg.Log}
}

// candidate is a chain that applies to an Alert Group: the sources that name it whose condition the level meets,
// whether one of them met the level before a rise, and whether one is a Team that joined.
type candidate struct {
	sources   []source
	metBefore bool
	joined    bool
}

// Start starts the chains that apply to g in its current start (groups.ChainRuns): those its Route names and, where the
// Route delivers to the Teams and runs their chains, those its Teams name, each whose Severity condition its level
// meets, one run per chain however many name it (C-32.FR-4, P-70). At a start or a start-over (All) every such chain
// starts, after any run still running from an earlier start is stopped as started_over; otherwise only those whose
// condition a rise from Before newly meets and those a Team that joined names, and a chain that already runs in this
// start takes the sources that now name it. Each run takes the chain's steps as they are (P-73) and its timer at its
// first step, due at once.
func (r *Runs) Start(ctx context.Context, tx groups.DBTX, g *groups.Group, st groups.ChainStart) (int, error) {
	q, org := r.queries(tx), r.s.orgID
	rows, err := q.ListChainSources(ctx, dbgen.ListChainSourcesParams{OrgID: org, RouteID: g.RouteID,
		AlertGroupID: g.ID})
	if err != nil {
		return 0, fmt.Errorf("list the chains of alert group #%d: %w", g.Number, err)
	}
	cands := map[int64]*candidate{}
	for _, row := range rows {
		cond := routing.SeverityCondition(row.SeverityCondition)
		if !cond.Meets(string(g.Severity)) {
			continue
		}
		c := cands[row.ChainID]
		if c == nil {
			c = &candidate{}
			cands[row.ChainID] = c
		}
		src := source{Kind: row.Kind, TeamID: row.TeamID}
		c.sources = append(c.sources, src)
		c.metBefore = c.metBefore || (st.Rise && cond.Meets(string(st.Before)))
		c.joined = c.joined || (row.Kind == SourceTeam && slices.Contains(st.Teams, row.TeamID))
	}
	if len(cands) == 0 && !st.All {
		return 0, nil
	}
	existing, err := q.ListGenerationRuns(ctx, dbgen.ListGenerationRunsParams{OrgID: org, AlertGroupID: g.ID,
		Generation: g.ChainGeneration})
	if err != nil {
		return 0, fmt.Errorf("list the runs of alert group #%d: %w", g.Number, err)
	}
	running := map[int64]dbgen.ListGenerationRunsRow{}
	for _, run := range existing {
		if run.ChainID.Valid {
			running[run.ChainID.Int64] = run
		}
	}
	var start, more []int64
	for _, id := range slices.Sorted(maps.Keys(cands)) {
		c := cands[id]
		if _, ok := running[id]; ok {
			more = append(more, id)
			continue
		}
		if st.All || (st.Rise && !c.metBefore) || c.joined {
			start = append(start, id)
		}
	}
	// The chains are read FOR KEY SHARE before any run is written — the sources added to a run of this start and the
	// stop of the runs of an earlier start included (Lock order).
	var chains []dbgen.ShareChainsRow
	if len(start) > 0 {
		if chains, err = q.ShareChains(ctx, dbgen.ShareChainsParams{OrgID: org, Ids: start}); err != nil {
			return 0, fmt.Errorf("lock the chains of alert group #%d: %w", g.Number, err)
		}
	}
	for _, id := range more {
		if err := r.addSources(ctx, q, running[id], cands[id].sources); err != nil {
			return 0, err
		}
	}
	if st.All {
		if err := r.stop(ctx, q, g, groups.ChainStop{Command: groups.StopStartedOver, At: st.At}, st.After); err != nil {
			return 0, err
		}
	}
	if len(chains) == 0 {
		return 0, nil
	}
	snapshot, err := q.SnapshotSteps(ctx, dbgen.SnapshotStepsParams{OrgID: org, ChainIds: start})
	if err != nil {
		return 0, fmt.Errorf("read the steps of the chains of alert group #%d: %w", g.Number, err)
	}
	steps := map[int64][]runStep{}
	for _, row := range snapshot {
		steps[row.ChainID] = append(steps[row.ChainID], stepOfSnapshot(row))
	}
	for _, c := range chains {
		if err := r.startRun(ctx, q, g, c, cands[c.ID].sources, steps[c.ID], st); err != nil {
			return 0, err
		}
	}
	if err := q.NotifyRunTimers(ctx, groups.TimersChannel); err != nil {
		return 0, fmt.Errorf("wake the timer workers: %w", err)
	}
	return len(chains), nil
}

// startRun writes the run of the chain c on g with its sources and steps, and its timer at its first step.
func (r *Runs) startRun(ctx context.Context, q runQueries, g *groups.Group, c dbgen.ShareChainsRow, sources []source,
	steps []runStep, st groups.ChainStart) error {
	if steps == nil {
		steps = []runStep{}
	}
	src, err := json.Marshal(sources)
	if err != nil {
		return fmt.Errorf("encode the sources of chain %s: %w", c.PublicID, err)
	}
	snap, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("encode the steps of chain %s: %w", c.PublicID, err)
	}
	publicID := publicid.New(publicid.EscalationRun)
	id, err := q.InsertRun(ctx, dbgen.InsertRunParams{OrgID: r.s.orgID, PublicID: publicID, AlertGroupID: g.ID,
		ChainID: pgtype.Int8{Int64: c.ID, Valid: true}, ChainName: c.Name, OwningTeamID: c.OwningTeamID, Sources: src,
		Steps:      snap,
		Generation: g.ChainGeneration, Now: st.At})
	if err != nil {
		return fmt.Errorf("start chain %s on alert group #%d: %w", c.PublicID, g.Number, err)
	}
	if err := q.UpsertRunTimer(ctx, dbgen.UpsertRunTimerParams{OrgID: r.s.orgID, EscalationRunID: id,
		Deadline: st.At, Now: st.At}); err != nil {
		return fmt.Errorf("set the timer of the run of chain %s: %w", c.PublicID, err)
	}
	group, chain := g.PublicID, c.PublicID
	r.after(ctx, st.After, func(ctx context.Context) {
		r.log.Log(ctx, logging.EscalationRunStarted, logging.F("group", group), logging.F("chain", chain),
			logging.F("run", publicID))
	})
	return nil
}

// addSources adds to a run of the current start the sources that now name its chain too, each once.
func (r *Runs) addSources(ctx context.Context, q runQueries, run dbgen.ListGenerationRunsRow, more []source) error {
	var have []source
	if err := json.Unmarshal(run.Sources, &have); err != nil {
		return fmt.Errorf("read the sources of run %s: %w", run.PublicID, err)
	}
	n := len(have)
	for _, s := range more {
		if !slices.Contains(have, s) {
			have = append(have, s)
		}
	}
	if len(have) == n {
		return nil
	}
	b, err := json.Marshal(have)
	if err != nil {
		return fmt.Errorf("encode the sources of run %s: %w", run.PublicID, err)
	}
	if err := q.SetRunSources(ctx, dbgen.SetRunSourcesParams{OrgID: r.s.orgID, ID: run.ID, Sources: b}); err != nil {
		return fmt.Errorf("add the sources of run %s: %w", run.PublicID, err)
	}
	return nil
}

// Stop stops every run of g that still runs, with the Command and the lifecycle event that stopped it, and deletes
// their timers (groups.ChainRuns); runs that ran out stay as they are (C-32.FR-5).
func (r *Runs) Stop(ctx context.Context, tx groups.DBTX, g *groups.Group, st groups.ChainStop) error {
	return r.stop(ctx, r.queries(tx), g, st, st.After)
}

func (r *Runs) stop(ctx context.Context, q runQueries, g *groups.Group, st groups.ChainStop,
	after func(func(context.Context))) error {
	p := dbgen.StopRunsParams{OrgID: r.s.orgID, AlertGroupID: g.ID, Now: pgtype.Timestamptz{Time: st.At, Valid: true},
		StopCommand: pgtype.Text{String: st.Command, Valid: true}}
	if st.EventSeq != 0 {
		p.StopEventSeq = pgtype.Int8{Int64: st.EventSeq, Valid: true}
	}
	stopped, err := q.StopRuns(ctx, p)
	if err != nil {
		return fmt.Errorf("stop the runs of alert group #%d: %w", g.Number, err)
	}
	if len(stopped) == 0 {
		return nil
	}
	ids := make([]int64, len(stopped))
	for i, run := range stopped {
		ids[i] = run.ID
	}
	if err := q.DeleteRunTimers(ctx, dbgen.DeleteRunTimersParams{OrgID: r.s.orgID, RunIds: ids}); err != nil {
		return fmt.Errorf("delete the timers of the runs of alert group #%d: %w", g.Number, err)
	}
	group, command := g.PublicID, st.Command
	runs := slices.Clone(stopped)
	r.after(ctx, after, func(ctx context.Context) {
		for _, run := range runs {
			r.log.Log(ctx, logging.EscalationRunStopped, logging.F("group", group), logging.F("chain", run.ChainPublicID),
				logging.F("run", run.PublicID), logging.F("command", command))
		}
	})
	return nil
}

// after runs f once the transaction committed, through the dispatcher's collector, or at once without one.
func (r *Runs) after(ctx context.Context, collect func(func(context.Context)), f func(context.Context)) {
	if r.log == nil {
		return
	}
	if collect == nil {
		f(ctx)
		return
	}
	collect(f)
}

// Started reports whether chains started in the current start of g (groups.ChainRuns).
func (r *Runs) Started(ctx context.Context, tx groups.DBTX, g *groups.Group) (bool, error) {
	c, err := r.count(ctx, tx, g)
	return c.Total > 0, err
}

// RanOut reports whether every run of the current start of g ran out: at least one, none running and none stopped
// (groups.ChainRuns, C-32.FR-7).
func (r *Runs) RanOut(ctx context.Context, tx groups.DBTX, g *groups.Group) (bool, error) {
	c, err := r.count(ctx, tx, g)
	return c.Total > 0 && c.RanOut == c.Total, err
}

func (r *Runs) count(ctx context.Context, tx groups.DBTX, g *groups.Group) (dbgen.CountGenerationRunsRow, error) {
	if g.ChainGeneration == 0 {
		return dbgen.CountGenerationRunsRow{}, nil
	}
	c, err := r.queries(tx).CountGenerationRuns(ctx, dbgen.CountGenerationRunsParams{OrgID: r.s.orgID,
		AlertGroupID: g.ID, Generation: g.ChainGeneration})
	if err != nil {
		return c, fmt.Errorf("count the runs of alert group #%d: %w", g.Number, err)
	}
	return c, nil
}

// GroupChain is a run of the current start of an Alert Group's chains as its read shows it (AlertGroupChain): its
// chain, nil once deleted, with the name it had when the run started, its sources, its state, its current step while
// it runs and when the next is due.
type GroupChain struct {
	RunID      string
	Chain      *ChainRef
	ChainName  string
	Sources    []SourceRef
	State      string
	Step       *StepPosition
	NextStepAt *time.Time
	StartedAt  time.Time
}

// ChainRef names a chain with its owning Team.
type ChainRef struct {
	PublicID   string
	Name       string
	OwningTeam *TeamRef
}

// SourceRef is a source of a run as the API names it: the Route, or a Team.
type SourceRef struct {
	Kind string
	Team *TeamRef
}

// StepPosition is the step a run is at: its number and kind.
type StepPosition struct {
	Number int
	Kind   string
}

// position is the step a running run is at: the wait it waits out when the step before its next one is a wait whose
// timer is pending, else its next step; nil for a run that no longer runs or passed its last step.
func position(steps []runStep, index int, state string) *StepPosition {
	if state != RunRunning || index < 0 || index > len(steps) {
		return nil
	}
	if index > 0 {
		if prev := steps[index-1]; prev.Kind == KindWait { //nolint:gosec // G602: 0 < index <= len(steps), checked above
			return &StepPosition{Number: prev.Number, Kind: KindWait}
		}
	}
	if index == len(steps) {
		return nil
	}
	next := steps[index]
	return &StepPosition{Number: next.Number, Kind: next.Kind}
}

// GroupChains reads, for each of the Alert Groups publicIDs, the runs of the current start of its chains, in the
// order they started (C-32.FR-18, C-09.FR-1): none before its chains first started.
func (r *Runs) GroupChains(ctx context.Context, publicIDs []string) (map[string][]GroupChain, error) {
	out := make(map[string][]GroupChain, len(publicIDs))
	if len(publicIDs) == 0 {
		return out, nil
	}
	q := r.queries(r.db)
	rows, err := q.ListGroupRuns(ctx, dbgen.ListGroupRunsParams{OrgID: r.s.orgID, PublicIds: publicIDs})
	if err != nil {
		return nil, fmt.Errorf("read the escalation chains of the alert groups: %w", err)
	}
	var teamIDs []int64
	sources := make([][]source, len(rows))
	chains := make([]GroupChain, len(rows))
	for i, row := range rows {
		var steps []runStep
		if err := json.Unmarshal(row.Steps, &steps); err != nil {
			return nil, fmt.Errorf("read the steps of run %s: %w", row.PublicID, err)
		}
		if err := json.Unmarshal(row.Sources, &sources[i]); err != nil {
			return nil, fmt.Errorf("read the sources of run %s: %w", row.PublicID, err)
		}
		for _, s := range sources[i] {
			if s.TeamID != 0 {
				teamIDs = append(teamIDs, s.TeamID)
			}
		}
		c := GroupChain{RunID: row.PublicID, ChainName: row.ChainName, State: row.State,
			Step: position(steps, int(row.StepIndex), row.State), StartedAt: row.StartedAt.UTC(),
			Sources: []SourceRef{}}
		if row.ChainPublicID.Valid {
			c.Chain = &ChainRef{PublicID: row.ChainPublicID.String, Name: row.CurrentChainName.String}
			if row.TeamPublicID.Valid {
				c.Chain.OwningTeam = &TeamRef{PublicID: row.TeamPublicID.String, Name: row.TeamName.String,
					Slug: row.TeamSlug.String, Deleted: row.TeamDeleted}
			}
		}
		if row.State == RunRunning && row.Deadline.Year() > 1 {
			at := row.Deadline.UTC()
			c.NextStepAt = &at
		}
		chains[i] = c
	}
	teams, err := r.teamRefs(ctx, q, teamIDs)
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		for _, s := range sources[i] {
			ref := SourceRef{Kind: s.Kind}
			if t, ok := teams[s.TeamID]; ok {
				ref.Team = &t
			}
			chains[i].Sources = append(chains[i].Sources, ref)
		}
		out[row.GroupPublicID] = append(out[row.GroupPublicID], chains[i])
	}
	return out, nil
}

// teamRefs names the Teams by id, deleted ones included.
func (r *Runs) teamRefs(ctx context.Context, q runQueries, ids []int64) (map[int64]TeamRef, error) {
	out := map[int64]TeamRef{}
	if len(ids) == 0 {
		return out, nil
	}
	slices.Sort(ids)
	rows, err := q.ListTeamRefs(ctx, dbgen.ListTeamRefsParams{OrgID: r.s.orgID, Ids: slices.Compact(ids)})
	if err != nil {
		return nil, fmt.Errorf("name the teams of the runs: %w", err)
	}
	for _, t := range rows {
		out[t.ID] = TeamRef{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Slug: t.Slug, Deleted: t.Deleted}
	}
	return out, nil
}

// ChainRun is a run of a chain as "How it went" lists it (EscalationRun): its Alert Group, its state, when it started
// and ended, what stopped it, and the steps it made and skipped.
type ChainRun struct {
	PublicID  string
	Group     RunGroup
	State     string
	StartedAt time.Time
	EndedAt   *time.Time
	StoppedBy *StoppedBy
	Steps     []RunStepView
}

// RunGroup is the Alert Group of a run.
type RunGroup struct {
	PublicID string
	Number   int64
	Title    string
	Severity string
}

// StoppedBy is what stopped a run: the Command, and the person who ran it, nil for Muster (a Resolve by its Alerts).
type StoppedBy struct {
	Command string
	Actor   *Actor
}

// Actor is a User or a Service account who stopped a run.
type Actor struct {
	Kind        string
	PublicID    string
	Name        string
	Login       string
	Deactivated bool
}

// RunStepView is a step a run made or skipped, with the reason of a skip.
type RunStepView struct {
	Number  int
	Kind    string
	At      time.Time
	Outcome string
	Reason  string
}

// RunPosition is the place of a run in the list of a chain's runs, newest first.
type RunPosition struct {
	At time.Time `json:"a"`
	ID int64     `json:"i"`
}

// RunPage is a page of the runs of a chain; Next is nil on the last page.
type RunPage struct {
	Runs []ChainRun
	Next *RunPosition
}

// ChainRuns lists a page of the recent runs of the chain publicID, newest first, after the position after (C-32.FR-18,
// "How it went"), without the steps of a run whose Alert Group's details are removed (C-09.FR-16); an unknown chain is
// ErrNotFound.
func (r *Runs) ChainRuns(ctx context.Context, publicID string, after *RunPosition, limit int) (RunPage, error) {
	q := r.queries(r.db)
	id, err := q.GetChainID(ctx, dbgen.GetChainIDParams{OrgID: r.s.orgID, PublicID: publicID})
	if errors.Is(err, pgx.ErrNoRows) {
		return RunPage{}, ErrNotFound
	}
	if err != nil {
		return RunPage{}, fmt.Errorf("read the escalation chain %s: %w", publicID, err)
	}
	p := dbgen.ListChainRunsParams{OrgID: r.s.orgID, ChainID: pgtype.Int8{Int64: id, Valid: true},
		PageSize: int32(limit + 1)} //nolint:gosec // G115: the limit is at most the page size
	if after != nil {
		p.AfterAt = pgtype.Timestamptz{Time: after.At, Valid: true}
		p.AfterID = pgtype.Int8{Int64: after.ID, Valid: true}
	}
	rows, err := q.ListChainRuns(ctx, p)
	if err != nil {
		return RunPage{}, fmt.Errorf("list the runs of the escalation chain %s: %w", publicID, err)
	}
	var page RunPage
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.Next = &RunPosition{At: last.StartedAt.UTC(), ID: last.ID}
	}
	ids := make([]int64, 0, len(rows))
	index := make(map[int64]int, len(rows))
	page.Runs = make([]ChainRun, len(rows))
	now := r.s.clock.Now()
	for i, row := range rows {
		page.Runs[i] = chainRunOf(row)
		// The steps are details of the Alert Group: hidden once retention.alert_details passed since it resolved,
		// whether or not the retention deleted them yet (C-09.FR-16).
		if detailsRemoved(row.GroupResolvedAt, row.RetentionAlertDetailsDays, now) {
			continue
		}
		ids = append(ids, row.ID)
		index[row.ID] = i
	}
	if len(ids) == 0 {
		return page, nil
	}
	steps, err := q.ListRunSteps(ctx, dbgen.ListRunStepsParams{OrgID: r.s.orgID, RunIds: ids})
	if err != nil {
		return RunPage{}, fmt.Errorf("list the steps of the runs of the escalation chain %s: %w", publicID, err)
	}
	for _, st := range steps {
		run := &page.Runs[index[st.RunID]]
		run.Steps = append(run.Steps, RunStepView{Number: int(st.Number), Kind: st.Kind, At: st.At.UTC(),
			Outcome: st.Outcome, Reason: st.Reason.String})
	}
	return page, nil
}

// detailsRemoved reports whether the details of an Alert Group resolved at resolvedAt are removed at now, as the reads
// of internal/groups decide it: retention.alert_details, days, passed since it resolved.
func detailsRemoved(resolvedAt pgtype.Timestamptz, days int64, now time.Time) bool {
	return resolvedAt.Valid && resolvedAt.Time.Before(now.Add(-time.Duration(days)*24*time.Hour))
}

func chainRunOf(row dbgen.ListChainRunsRow) ChainRun {
	run := ChainRun{PublicID: row.PublicID, State: row.State, StartedAt: row.StartedAt.UTC(),
		Group: RunGroup{PublicID: row.GroupPublicID, Number: row.GroupNumber, Title: row.GroupTitle,
			Severity: row.GroupSeverity}, Steps: []RunStepView{}}
	if row.EndedAt.Valid {
		at := row.EndedAt.Time.UTC()
		run.EndedAt = &at
	}
	if !row.StopCommand.Valid {
		return run
	}
	run.StoppedBy = &StoppedBy{Command: row.StopCommand.String}
	switch {
	case row.UserPublicID.Valid:
		run.StoppedBy.Actor = &Actor{Kind: "user", PublicID: row.UserPublicID.String, Name: row.UserName.String,
			Login: row.UserLogin.String, Deactivated: row.UserDeleted}
	case row.ServiceAccountPublicID.Valid:
		run.StoppedBy.Actor = &Actor{Kind: "service_account", PublicID: row.ServiceAccountPublicID.String,
			Name: row.ServiceAccountName.String, Deactivated: row.ServiceAccountDeleted}
	}
	return run
}
