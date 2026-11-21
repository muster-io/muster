// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/schedules"
)

// made are the steps the runs recorded, as "number outcome reason".
func (e *runsEnv) made() []string {
	var out []string
	for _, s := range e.q.recorded {
		out = append(out, strings.TrimSpace(strconv.Itoa(int(s.Number))+" "+s.Outcome+" "+s.Reason.String))
	}
	return out
}

// skips are the escalation_step_skipped the steps asked for, as "step reason person".
func (e *runsEnv) skips() []string {
	var out []string
	for _, p := range e.groups.progress {
		for _, s := range p.Skips {
			person := ""
			if s.Person != nil {
				person = s.Person.Login
			}
			out = append(out, strings.TrimSpace(strconv.Itoa(s.Step)+" "+s.Reason+" "+person))
		}
	}
	return out
}

// TestWaits is C-32.FR-15 and C-32.FR-3: a wait delays the next step by its wait_seconds on the business clock, from
// when the step was due; the run's timer is due at the end of the wait, and the run ran out after its last step, its
// timer removed.
func TestWaits(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain), wait(600), people(22))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	run := e.q.runs[0]
	e.clock.Advance(3 * time.Second)
	e.fire(t, run.id)
	if got := e.made(); !slices.Equal(got, []string{"1 made", "2 made"}) || run.stepIndex != 2 ||
		!e.q.timers[run.id].Equal(t0.Add(10*time.Minute)) || run.state != RunRunning {
		t.Fatalf("after the first firing: %v, step %d, timer %v", got, run.stepIndex, e.q.timers[run.id])
	}
	// A firing before the deadline, from a stale claim, changes nothing.
	if after, err := e.runs.FireStep(t.Context(), nil, run.id, t0); err != nil || after == nil {
		t.Fatalf("a stale firing: %v", err)
	}
	if len(e.made()) != 2 {
		t.Errorf("a stale firing made %v", e.made())
	}
	e.clock.Set(t0.Add(10 * time.Minute))
	e.fire(t, run.id)
	if got := e.made(); !slices.Equal(got, []string{"1 made", "2 made", "3 made"}) || run.state != RunRanOut ||
		!run.endedAt.Valid || len(e.q.timers) != 0 {
		t.Errorf("after the wait: %v %s, %d timers", got, run.state, len(e.q.timers))
	}
	if p := e.groups.progress[len(e.groups.progress)-1]; !p.RanOut || !p.Changed {
		t.Errorf("progress %+v", p)
	}
	if !strings.Contains(e.log.String(), `"event":"escalation_run_ran_out","group":"AG0000000000G1"`) {
		t.Errorf("log %s", e.log)
	}
	// Firing a run that ran out changes nothing.
	steps := len(e.q.recorded)
	if _, err := e.runs.FireStep(t.Context(), nil, run.id, t0); err != nil || len(e.q.recorded) != steps {
		t.Errorf("a run that ran out: %v", err)
	}
}

// TestRepeats is C-32.FR-15 and C-32.AC-8: "Repeat from step 3, up to 2 times" on a critical Alert Group in the
// Working hours of its Team makes steps 3 to 6 three times in all and then goes on; waits that are over by the time
// the run fires late, after downtime or a jump of the development clock, go on in the same firing.
func TestRepeats(t *testing.T) {
	e := newRunsEnv(t)
	e.g.Severity = organization.SeverityCritical
	// Monday 2026-11-16 12:00 UTC is 13:00 in Berlin, inside the Working hours of payments (Mon 11–19).
	e.chain(1, "payments", true, page(schedMain), wait(600), critical(people(21)), wait(900),
		critical(page(schedMain)), workingHours(team()), repeat(3, 2))
	e.source(1, 1, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	run := e.q.runs[0]
	e.fire(t, run.id)
	e.clock.Set(t0.Add(2 * time.Hour))
	e.fire(t, run.id)
	counts := map[string]int{}
	for _, s := range e.made() {
		counts[s]++
	}
	if counts["1 made"] != 1 || counts["2 made"] != 1 || counts["3 made"] != 3 || counts["4 made"] != 3 ||
		counts["5 made"] != 3 || counts["6 made"] != 3 || counts["7 made"] != 3 || run.state != RunRanOut {
		t.Errorf("steps %v, state %s", e.made(), run.state)
	}
	if string(run.repeats) != `{"7":2}` {
		t.Errorf("repeats %s", run.repeats)
	}
	// Late by more than an hour, each step is recorded at the time it was due, not when the firing caught up.
	var dues []time.Time
	for _, st := range e.q.recorded {
		if st.Number == 3 {
			dues = append(dues, st.At)
		}
	}
	if want := []time.Time{t0.Add(10 * time.Minute), t0.Add(25 * time.Minute), t0.Add(40 * time.Minute)}; !slices.Equal(
		dues, want) {
		t.Errorf("step 3 recorded at %v, want %v", dues, want)
	}
}

// TestRepeatOfARepeat is C-32.FR-15: a repeat that goes back to a repeat step makes that repeat afresh, its passes
// cleared, and that one goes back again.
func TestRepeatOfARepeat(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain), repeat(1, 1), repeat(2, 1))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.fire(t, e.q.runs[0].id)
	var got []string
	for _, s := range e.made() {
		got = append(got, s[:1])
	}
	if want := []string{"1", "2", "1", "2", "3", "2", "1", "2", "3"}; !slices.Equal(got, want) {
		t.Errorf("steps %v, want %v", got, want)
	}
}

// TestNestedRepeats is C-32.FR-15: each pass of an outer repeat makes its steps again, an inner repeat with its
// passes, so the inner repeat goes back again on each pass of the outer one.
func TestNestedRepeats(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain), people(21), repeat(2, 1), repeat(1, 1))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.fire(t, e.q.runs[0].id)
	var got []string
	for _, s := range e.made() {
		got = append(got, s[:1])
	}
	if want := []string{"1", "2", "3", "2", "3", "4", "1", "2", "3", "2", "3", "4"}; !slices.Equal(got, want) ||
		e.q.runs[0].state != RunRanOut {
		t.Errorf("steps %v, want %v; %s", got, want, e.q.runs[0].state)
	}
}

// TestStepLimitOfARun is C-32.FR-15: repeats nested four deep without a wait would make 11^4 passes; the run makes
// MaxStepsPerRun steps over several firings, counted from what it recorded, then runs out there as after its last
// step — the dispatcher then makes the Alert Group Unclaimed — with no timer left and escalation_run_step_limit
// logged at warn.
func TestStepLimitOfARun(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain), repeat(1, 10), repeat(1, 10), repeat(1, 10), repeat(1, 10))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	run := e.q.runs[0]
	for i := 0; i < 10 && run.state == RunRunning; i++ {
		e.fire(t, run.id)
	}
	last := e.groups.progress[len(e.groups.progress)-1]
	if run.state != RunRanOut || len(e.q.recorded) != MaxStepsPerRun || len(e.q.timers) != 0 || !last.RanOut ||
		len(e.groups.progress) != 5 {
		t.Errorf("%s after %d steps and %d firings, %d timers, last %+v", run.state, len(e.q.recorded),
			len(e.groups.progress), len(e.q.timers), last)
	}
	if !strings.Contains(e.log.String(), `"level":"WARN","event":"escalation_run_step_limit"`) ||
		!strings.Contains(e.log.String(), `"steps":4020`) ||
		!strings.Contains(e.log.String(), `"event":"escalation_run_ran_out"`) {
		t.Errorf("log %s", e.log)
	}
}

// TestManyStepsInOneFiring: repeats nested without a wait make more steps than one firing makes; the firing stops
// after MaxStepsPerFiring of them and the run goes on at the next firing, due at once, until it ran out.
func TestManyStepsInOneFiring(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain), repeat(1, 10), repeat(1, 10), repeat(1, 10))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	run := e.q.runs[0]
	// The firing comes an hour late: the next one is due at the time the step was due, not at now.
	e.clock.Set(t0.Add(time.Hour))
	e.fire(t, run.id)
	if len(e.q.recorded) != MaxStepsPerFiring || run.state != RunRunning || !e.q.timers[run.id].Equal(t0) {
		t.Fatalf("after one firing: %d steps, %s, timer %v", len(e.q.recorded), run.state, e.q.timers[run.id])
	}
	for i := 0; i < 20 && run.state == RunRunning; i++ {
		e.fire(t, run.id)
	}
	first := 0
	for _, s := range e.q.recorded {
		if s.Number == 1 {
			first++
		}
	}
	if run.state != RunRanOut || first != 11*11*11 {
		t.Errorf("%s after %d steps, step 1 made %d times", run.state, len(e.q.recorded), first)
	}
}

// TestRepeatWaitsBetweenPasses: a firing on time makes each pass up to its next wait only, so the passes keep their
// pace on the business clock.
func TestRepeatWaitsBetweenPasses(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain), wait(60), repeat(1, 1))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	run := e.q.runs[0]
	e.fire(t, run.id)
	e.clock.Set(t0.Add(time.Minute))
	e.fire(t, run.id)
	if got := e.made(); !slices.Equal(got, []string{"1 made", "2 made", "3 made", "1 made", "2 made"}) ||
		!e.q.timers[run.id].Equal(t0.Add(2*time.Minute)) {
		t.Errorf("steps %v, timer %v", got, e.q.timers[run.id])
	}
	e.clock.Set(t0.Add(2 * time.Minute))
	e.fire(t, run.id)
	if got := e.made(); len(got) != 6 || got[5] != "3 made" || run.state != RunRanOut {
		t.Errorf("steps %v, %s", got, run.state)
	}
}

// TestSkips is C-32.FR-14, C-32.AC-8, C-25.FR-9, C-32.AC-15 and C-09.FR-22: a step limited to Working hours is skipped
// outside those of the chain's owning Team — else the Organization's — and made inside them; a step whose Severity
// condition the level does not meet, a page_on_call step whose Schedule has a gap now or is gone, and each person a
// step names who became a Viewer or was disabled since are skipped with the reason, and the chain goes on with its
// next step at once; a step whose people are all skipped is skipped.
func TestSkips(t *testing.T) {
	// Tuesday 2026-11-17 03:10 in Berlin: outside every Working hours. At 12:00 UTC on that Tuesday the Organization
	// (Monday to Friday, 09:00–18:00 UTC) works and payments (Mondays only) does not.
	night := time.Date(2026, 11, 17, 2, 10, 0, 0, time.UTC)
	tuesdayNoon := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		at    time.Time
		owned bool
		step  []dbgen.SnapshotStepsRow
		made  []string
		skips []string
	}{
		{"outside the working hours of the team", night, true,
			[]dbgen.SnapshotStepsRow{workingHours(team()), page(schedMain)},
			[]string{"1 skipped outside_working_hours", "2 made"}, []string{"1 outside_working_hours"}},
		{"inside the working hours of the team", t0, true,
			[]dbgen.SnapshotStepsRow{workingHours(team())}, []string{"1 made"}, nil},
		{"outside the working hours of the organization", time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC), false,
			[]dbgen.SnapshotStepsRow{workingHours(team())},
			[]string{"1 skipped outside_working_hours"}, []string{"1 outside_working_hours"}},
		{"inside the working hours of the organization", t0, false,
			[]dbgen.SnapshotStepsRow{workingHours(team())}, []string{"1 made"}, nil},
		{"a severity condition not met", t0, false,
			[]dbgen.SnapshotStepsRow{critical(team()), page(schedMain)},
			[]string{"1 skipped severity", "2 made"}, []string{"1 severity"}},
		{"a gap of the schedule", t0, false,
			[]dbgen.SnapshotStepsRow{page(schedSecond), page("SC0000000000GN"), page(schedMain)},
			[]string{"1 skipped schedule_gap", "2 skipped schedule_gap", "3 made"},
			[]string{"1 schedule_gap", "2 schedule_gap"}},
		{"inside the working hours of the organization, outside the team's", tuesdayNoon, true,
			[]dbgen.SnapshotStepsRow{workingHours(team())}, []string{"1 skipped outside_working_hours"},
			[]string{"1 outside_working_hours"}},
		{"inside the working hours of the organization without an owning team", tuesdayNoon, false,
			[]dbgen.SnapshotStepsRow{workingHours(team())}, []string{"1 made"}, nil},
		{"a deleted person", t0, false, []dbgen.SnapshotStepsRow{people(25, 21)}, []string{"1 made"},
			[]string{"1 disabled deleted-user-25"}},
		{"a viewer and a disabled person", t0, false,
			[]dbgen.SnapshotStepsRow{people(23, 21, 24)},
			[]string{"1 made"}, []string{"1 viewer vic", "1 disabled dora"}},
		{"nobody left to page", t0, false,
			[]dbgen.SnapshotStepsRow{people(23, 99), people(22)},
			[]string{"1 skipped viewer", "2 made"}, []string{"1 viewer vic", "1 disabled"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newRunsEnv(t)
			e.onCalls.onCall[schedSecond] = 0
			e.chain(1, "chain", c.owned, slices.Clone(c.step)...)
			e.source(1, 0, ConditionAny)
			e.clock.Set(c.at)
			e.start(t, groups.ChainStart{All: true})
			e.fire(t, e.q.runs[0].id)
			if got := e.made(); !slices.Equal(got, c.made) {
				t.Errorf("steps %v, want %v", got, c.made)
			}
			if got := e.skips(); !slices.Equal(got, c.skips) {
				t.Errorf("skips %v, want %v", got, c.skips)
			}
			// A skipped step has its reason, a made one none (escalation_run_steps_reason_check).
			for _, r := range e.q.recorded {
				if r.Reason.Valid != (r.Outcome == OutcomeSkipped) || r.Reason.Valid && r.Reason.String == "" {
					t.Errorf("step %d %s recorded with the reason %+v", r.Number, r.Outcome, r.Reason)
				}
			}
			for _, p := range e.groups.progress {
				for _, s := range p.Skips {
					// A person who is not there at all is skipped without being named.
					if s.Person != nil && s.Person.PublicID == "" {
						t.Errorf("a skip names a person without an id: %+v", s)
					}
				}
			}
			for _, p := range e.groups.progress {
				for _, s := range p.Skips {
					if s.Run != e.q.runs[0].id || s.Chain != 1 || s.ChainPublicID != e.q.chains[1].PublicID ||
						s.ChainName != "chain" {
						t.Errorf("skip %+v", s)
					}
				}
			}
		})
	}
}

// TestFireStepDoesNothing: a run that no longer exists, an Alert Group gone or no longer firing, and a run stopped
// since the claim make no step.
func TestFireStepDoesNothing(t *testing.T) {
	e := newRunsEnv(t)
	e.chain(1, "devops", false, page(schedMain))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	run := e.q.runs[0]
	if after, err := e.runs.FireStep(t.Context(), nil, 9999, t0); err != nil || after != nil {
		t.Errorf("no run: %v", err)
	}
	e.groups.group = &groups.Group{ID: 2}
	e.fire(t, run.id)
	e.groups.group = e.g
	deadline := e.q.timers[run.id]
	if err := e.runs.Stop(t.Context(), nil, e.g, groups.ChainStop{Command: groups.StopSnooze}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.runs.FireStep(t.Context(), nil, run.id, deadline); err != nil {
		t.Fatal(err)
	}
	if len(e.q.recorded) != 0 {
		t.Errorf("steps %v", e.made())
	}
}

// TestFireStepStopsAStaleRun: a run whose Alert Group is no longer firing, as a replica of an earlier version leaves
// it during a rolling update, makes no step and is stopped with the Command of its status.
func TestFireStepStopsAStaleRun(t *testing.T) {
	for status, command := range map[groups.Status]string{groups.StatusAcknowledged: groups.StopAcknowledge,
		groups.StatusSnoozed: groups.StopSnooze, groups.StatusResolved: groups.StopResolve} {
		e := newRunsEnv(t)
		e.chain(1, "devops", false, page(schedMain))
		e.source(1, 0, ConditionAny)
		e.start(t, groups.ChainStart{All: true})
		run := e.q.runs[0]
		e.g.Status = status
		e.fire(t, run.id)
		if len(e.q.recorded) != 0 || run.state != RunStopped || run.stop.String != command || len(e.q.timers) != 0 {
			t.Errorf("%s: steps %v, run %+v", status, e.made(), run)
		}
		if !strings.Contains(e.log.String(), `"event":"escalation_run_stopped"`) {
			t.Errorf("%s: log %s", status, e.log)
		}
	}
}

// TestFireStepFailures: a failure of a query or of the dispatcher fails the step, which the timer worker retries.
func TestFireStepFailures(t *testing.T) {
	for _, name := range []string{"GetRunGroup", "LockRun", "InsertRunStep", "SaveRun", "UpsertRunTimer",
		"ListPeopleStates", "DeleteRunTimers", "dispatcher", "working hours", "on-call"} {
		t.Run(name, func(t *testing.T) {
			e := newRunsEnv(t)
			e.chain(1, "devops", true, workingHours(people(21)), page(schedMain), wait(60), page(schedMain))
			e.source(1, 0, ConditionAny)
			e.start(t, groups.ChainStart{All: true})
			boom := errors.New("boom")
			switch name {
			case "dispatcher":
				e.groups.err = boom
			case "working hours":
				e.store.fail["GetTeamWorkingHours"] = boom
			case "on-call":
				e.onCalls.onCall[schedMain] = -1
			default:
				e.q.fail[name] = boom
			}
			if name == "on-call" {
				e.runs.onCalls = failingOnCalls{}
			}
			run := e.q.runs[0]
			_, err := e.runs.FireStep(t.Context(), nil, run.id, e.q.timers[run.id])
			if name == "DeleteRunTimers" {
				e.clock.Advance(time.Minute)
				_, err = e.runs.FireStep(t.Context(), nil, run.id, e.q.timers[run.id])
			}
			if err == nil {
				t.Error("no error")
			}
		})
	}
}

type failingOnCalls struct{}

func (failingOnCalls) OnCallAtIn(context.Context, mentions.DBTX, string, time.Time) (schedules.Segment, error) {
	return schedules.Segment{}, errors.New("boom")
}

// TestStepPages: a step that Pages is handed to StepPages with the people it may still Page; a failure fails the step.
func TestStepPages(t *testing.T) {
	e := newRunsEnv(t)
	pages := &recordingPages{}
	e.runs.pages = pages
	e.chain(1, "devops", false, people(23, 21), page(schedMain))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.fire(t, e.q.runs[0].id)
	if len(pages.steps) != 2 || !slices.Equal(pages.people[0], []int64{21}) || pages.steps[1].Kind != KindPageOnCall ||
		pages.steps[1].ScheduleID != 11 || pages.runs[0].ChainID != 1 {
		t.Errorf("pages %+v %v", pages.steps, pages.people)
	}
	e2 := newRunsEnv(t)
	e2.runs.pages = &recordingPages{err: errors.New("boom")}
	e2.chain(1, "devops", false, page(schedMain))
	e2.source(1, 0, ConditionAny)
	e2.start(t, groups.ChainStart{All: true})
	if _, err := e2.runs.FireStep(t.Context(), nil, e2.q.runs[0].id, t0); err == nil {
		t.Error("a failing page: no error")
	}
}

type recordingPages struct {
	runs    []RunRef
	steps   []StepRef
	people  [][]int64
	made    func(step StepRef) StepMade
	stopped []int64
	err     error
	stopErr error
}

func (p *recordingPages) MakeStep(_ context.Context, _ groups.DBTX, _ *groups.Group, run RunRef, step StepRef,
	people []int64) (StepMade, error) {
	p.runs, p.steps, p.people = append(p.runs, run), append(p.steps, step), append(p.people, people)
	if p.made != nil {
		return p.made(step), p.err
	}
	return StepMade{}, p.err
}

func (p *recordingPages) StopRules(_ context.Context, _ groups.DBTX, groupID int64, _ time.Time) error {
	p.stopped = append(p.stopped, groupID)
	return p.stopErr
}

// TestStepPagesInProgress is C-32.FR-9 and FR-14 for the steps that Page: the Pages that StepPages makes join the
// progress of the firing after the skips of their step, a page_team step that StepPages skips is recorded skipped
// with its reason, and the stop of the runs stops the runs of their Notification rules (C-31.FR-3).
func TestStepPagesInProgress(t *testing.T) {
	e := newRunsEnv(t)
	pages := &recordingPages{made: func(st StepRef) StepMade {
		if st.Kind == KindPageTeam {
			return StepMade{Skip: groups.SkipNoMentionPossible}
		}
		return StepMade{Pages: []groups.StepPage{{ChainEntry: groups.ChainEntry{Step: st.Number}, People: []int64{21}}}}
	}}
	e.runs.pages = pages
	e.chain(1, "devops", false, people(23, 21), team(), page(schedMain), wait(600), page(schedMain))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.fire(t, e.q.runs[0].id)
	p := e.groups.progress[len(e.groups.progress)-1]
	var skips []string
	for _, s := range p.Skips {
		skips = append(skips, strconv.Itoa(s.Step)+"/"+s.Reason)
	}
	var made []string
	for _, pg := range p.Pages {
		made = append(made, strconv.Itoa(pg.Step)+"@"+strconv.Itoa(pg.After)+"#"+strconv.Itoa(pg.Seq))
	}
	if !slices.Equal(skips, []string{"1/viewer", "2/no_mention_possible"}) || !slices.Equal(made, []string{"1@1#0",
		"3@2#1"}) {
		t.Errorf("skips %v pages %v", skips, made)
	}
	if got := e.made(); !slices.Equal(got[:3], []string{"1 made", "2 skipped no_mention_possible", "3 made"}) {
		t.Errorf("steps %v", got)
	}
	if r := pages.runs[0]; r.ChainPublicID == "" || r.ChainName != "devops" || len(r.Sources) != 1 {
		t.Errorf("run %+v", r)
	}
	// Each step that Pages carries the seq it is recorded with, one more than the run's steps before it, so that two
	// steps of one firing are told apart (C-32.FR-12).
	var seqs []int
	for _, st := range pages.steps {
		seqs = append(seqs, st.Seq)
	}
	if !slices.Equal(seqs, []int{1, 2, 3}) {
		t.Errorf("run steps %v", seqs)
	}
	// The start stopped the rules of the Alert Group's earlier start; the stop of its runs stops them again, and so
	// does a stop after the runs ended: a rule may still repeat after its chain's run ran out.
	if err := e.runs.Stop(t.Context(), nil, e.g, groups.ChainStop{Command: groups.StopAcknowledge,
		At: e.clock.Now()}); err != nil || !slices.Equal(pages.stopped, []int64{e.g.ID, e.g.ID}) {
		t.Errorf("the rules of the stopped run: %v %v", err, pages.stopped)
	}
	if err := e.runs.Stop(t.Context(), nil, e.g, groups.ChainStop{Command: groups.StopResolve,
		At: e.clock.Now()}); err != nil || len(pages.stopped) != 3 {
		t.Errorf("the rules after the runs stopped: %v %v", err, pages.stopped)
	}
	// A repeat makes step 1 again, with the seq after the repeat's own: 1, then 3.
	er := newRunsEnv(t)
	repeated := &recordingPages{}
	er.runs.pages = repeated
	er.chain(1, "devops", false, page(schedMain), repeat(1, 1))
	er.source(1, 0, ConditionAny)
	er.start(t, groups.ChainStart{All: true})
	er.fire(t, er.q.runs[0].id)
	var again []int
	for _, st := range repeated.steps {
		again = append(again, st.Seq)
	}
	if !slices.Equal(again, []int{1, 3}) {
		t.Errorf("run steps across a repeat %v", again)
	}
	e2 := newRunsEnv(t)
	failing := &recordingPages{}
	e2.runs.pages = failing
	e2.chain(1, "devops", false, wait(600))
	e2.source(1, 0, ConditionAny)
	e2.start(t, groups.ChainStart{All: true})
	failing.stopErr = errors.New("boom")
	if err := e2.runs.Stop(t.Context(), nil, e2.g, groups.ChainStop{Command: groups.StopResolve}); err == nil {
		t.Error("a failing stop of the rules: no error")
	}
}

// TestPosition: the step a running run is at is the wait it waits out, else its next step; none once it no longer
// runs or passed its last step.
func TestPosition(t *testing.T) {
	steps := []runStep{{Number: 1, Kind: KindPageOnCall}, {Number: 2, Kind: KindWait}, {Number: 3, Kind: KindPageTeam}}
	for _, c := range []struct {
		index int
		state string
		want  string
	}{
		{0, RunRunning, "1 page_on_call"}, {2, RunRunning, "2 wait"}, {1, RunRunning, "2 wait"},
		{3, RunRunning, ""}, {2, RunStopped, ""}, {3, RunRanOut, ""},
	} {
		got := ""
		if p := position(steps, c.index, c.state); p != nil {
			got = strconv.Itoa(p.Number) + " " + p.Kind
		}
		if got != c.want {
			t.Errorf("index %d %s: %q, want %q", c.index, c.state, got, c.want)
		}
	}
	// A chain that ends with a wait: the run waits it out past its last step.
	if p := position(steps[:2], 2, RunRunning); p == nil || *p != (StepPosition{Number: 2, Kind: KindWait}) {
		t.Errorf("waiting out the last step: %+v", p)
	}
}

// TestSeqOfWebhooksAndPages is C-32.FR-12 and FR-13: the webhooks and the Pages of one firing are numbered in the
// order of their steps, so that the dispatcher records them in that order.
func TestSeqOfWebhooksAndPages(t *testing.T) {
	e := newRunsEnv(t)
	e.runs.pages = &recordingPages{made: func(st StepRef) StepMade {
		return StepMade{Pages: []groups.StepPage{{ChainEntry: groups.ChainEntry{Step: st.Number}, People: []int64{21}}}}
	}}
	e.chain(1, "devops", false, webhookStep(41), people(21), webhookStep(41))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	e.fire(t, e.q.runs[0].id)
	p := e.groups.progress[len(e.groups.progress)-1]
	var got []string
	for _, w := range p.Webhooks {
		got = append(got, "webhook "+strconv.Itoa(w.Step)+"#"+strconv.Itoa(w.Seq))
	}
	for _, pg := range p.Pages {
		got = append(got, "page "+strconv.Itoa(pg.Step)+"#"+strconv.Itoa(pg.Seq))
	}
	if want := []string{"webhook 1#0", "webhook 3#2", "page 2#1"}; !slices.Equal(got, want) {
		t.Errorf("seqs %v, want %v", got, want)
	}
}

// TestLateFiringPagesOnce is #594: a firing that comes late, after downtime, Pages each person, Team or everyone once:
// a step that would Page only whom the firing already Paged at an earlier due time waits there, from now, for the
// spacing it would have had on time, rather than Paging them again within seconds; a step that Pages someone else is
// made, a call or a step that Pages nobody it can name is made, and steps with no wait between them are made as they
// are.
func TestLateFiringPagesOnce(t *testing.T) {
	for _, c := range []struct {
		name  string
		steps []dbgen.SnapshotStepsRow
		late  time.Duration
		made  []int
		index int32
		next  time.Duration // the timer after the firing, from now; 0 for a run that ran out
		setup func(e *runsEnv)
	}{
		{"unequal waits", []dbgen.SnapshotStepsRow{people(22), wait(3600), people(21), wait(60), people(22)},
			2 * time.Hour, []int{1, 3}, 4, 61 * time.Minute, nil},
		{"both again, the shortest spacing", []dbgen.SnapshotStepsRow{people(22), wait(300), people(21), wait(300),
			people(22, 21)}, time.Hour, []int{1, 3}, 4, 5 * time.Minute, nil},
		{"a call after a wait", []dbgen.SnapshotStepsRow{people(22), wait(300), call(22)}, 30 * time.Minute,
			[]int{1, 3}, 3, 0, nil},
		{"a Schedule in a gap after a wait", []dbgen.SnapshotStepsRow{people(21), wait(300), page(schedMain)},
			30 * time.Minute, []int{1}, 3, 0, func(e *runsEnv) { e.onCalls.onCall[schedMain] = 0 }},
		{"an On-call nobody reads", []dbgen.SnapshotStepsRow{page(schedMain), wait(300), page(schedMain)},
			30 * time.Minute, []int{1, 3}, 3, 0, func(e *runsEnv) { e.runs.onCalls = nil }},
		{"the same person after a wait", []dbgen.SnapshotStepsRow{people(22), wait(300), people(22)}, 30 * time.Minute,
			[]int{1}, 2, 5 * time.Minute, nil},
		{"another person after a wait", []dbgen.SnapshotStepsRow{people(22), wait(300), people(21)}, 30 * time.Minute,
			[]int{1, 3}, 3, 0, nil},
		{"someone new among them", []dbgen.SnapshotStepsRow{people(22), wait(300), people(22, 21)}, 30 * time.Minute,
			[]int{1, 3}, 3, 0, nil},
		{"the On-call, then the same person", []dbgen.SnapshotStepsRow{page(schedMain), wait(300), people(21)},
			30 * time.Minute, []int{1}, 2, 5 * time.Minute, nil},
		{"the Team again", []dbgen.SnapshotStepsRow{team(), wait(600), team()}, time.Hour, []int{1}, 2,
			10 * time.Minute, nil},
		{"a repeat late many times", []dbgen.SnapshotStepsRow{people(22), wait(300), repeat(1, 50)}, 10 * time.Hour,
			[]int{1}, 0, 5 * time.Minute, nil},
		{"two people, then each again", []dbgen.SnapshotStepsRow{people(22), wait(300), people(21), wait(300),
			people(22), people(21)}, time.Hour, []int{1, 3}, 4, 10 * time.Minute, nil},
		{"no wait between", []dbgen.SnapshotStepsRow{people(22), people(22)}, time.Hour, []int{1, 2}, 2, 0, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newRunsEnv(t)
			pages := &recordingPages{made: func(st StepRef) StepMade {
				return StepMade{Pages: []groups.StepPage{{ChainEntry: groups.ChainEntry{Step: st.Number}}}}
			}}
			e.runs.pages = pages
			if c.setup != nil {
				c.setup(e)
			}
			e.chain(1, "devops", false, c.steps...)
			e.source(1, 0, ConditionAny)
			e.start(t, groups.ChainStart{All: true})
			run := e.q.runs[0]
			e.clock.Set(t0.Add(c.late))
			e.fire(t, run.id)
			var made []int
			for _, st := range pages.steps {
				made = append(made, st.Number)
			}
			p := e.groups.progress[len(e.groups.progress)-1]
			if !slices.Equal(made, c.made) || len(p.Pages) != len(c.made) || run.stepIndex != c.index {
				t.Fatalf("made %v with %d pages, at %d; want %v at %d", made, len(p.Pages), run.stepIndex, c.made,
					c.index)
			}
			if c.next == 0 {
				if run.state != RunRanOut {
					t.Errorf("state %s, want ran out", run.state)
				}
				return
			}
			now := e.clock.Now()
			if run.state != RunRunning || !e.q.timers[run.id].Equal(now.Add(c.next)) {
				t.Fatalf("state %s, timer %v, want %v", run.state, e.q.timers[run.id], now.Add(c.next))
			}
			// The step that waited is made at the end of that wait.
			e.clock.Set(now.Add(c.next))
			e.fire(t, run.id)
			if got := pages.steps[len(c.made)].Number; got != int(c.index)+1 {
				t.Errorf("then made step %d, want %d", got, c.index+1)
			}
		})
	}
}

func call(ids ...int64) dbgen.SnapshotStepsRow {
	return dbgen.SnapshotStepsRow{Kind: KindCall, CallTarget: pgtype.Text{String: CallPeople, Valid: true},
		UserIds: ids}
}

// TestLateFiringSkipsOnce:a step that waits in a late firing records nothing yet, so the person it skips is skipped
// once, when the step is made.
func TestLateFiringSkipsOnce(t *testing.T) {
	e := newRunsEnv(t)
	e.runs.pages = &recordingPages{}
	e.chain(1, "devops", false, people(22, 23), wait(300), people(22, 23))
	e.source(1, 0, ConditionAny)
	e.start(t, groups.ChainStart{All: true})
	run := e.q.runs[0]
	e.clock.Set(t0.Add(time.Hour))
	e.fire(t, run.id)
	if got := e.skips(); !slices.Equal(got, []string{"1 viewer vic"}) || !slices.Equal(e.made(),
		[]string{"1 made", "2 made"}) {
		t.Fatalf("skips %v, steps %v", got, e.made())
	}
	e.clock.Set(e.q.timers[run.id])
	e.fire(t, run.id)
	if got := e.skips(); !slices.Equal(got, []string{"1 viewer vic", "3 viewer vic"}) || run.state != RunRanOut {
		t.Errorf("skips %v, state %s", got, run.state)
	}
}
