// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/schedules"
	"github.com/muster-io/muster/internal/teams"
)

// FireStep is the handler of the timer of a run, claimed with its deadline (C-32.FR-5, FR-14, FR-15): it reads the run
// by the timer's escalation_run_id, locks its Alert Group through the dispatcher (ChainStep) and then the run, and does
// nothing when the run no longer runs or its timer was moved or removed since it was claimed, so that a second replica
// firing the same step changes nothing; a run whose Alert Group is no longer firing, as a replica of an earlier
// version leaves it, which stops no run, is stopped then. Otherwise it makes the steps from the current one on: a step
// that Pages or sends is skipped, recording escalation_step_skipped, outside the Working hours of its chain when it is
// limited to them, for a Severity level its condition does not meet, in a gap of the Schedule it Pages, or for a
// person it names who became a Viewer or was disabled since; it is made otherwise, by StepPages (pages.go). A
// step "Send to an outgoing webhook" is also skipped when its Destination was deleted or takes no events any more, and
// is made by recording escalation_webhook (webhook.go).
// Skips and Pages go on at once; a wait sets the timer at the end of the wait, from the deadline the step was due, on
// the business clock, and a wait that is already over by then goes on; a repeat goes back to its step until it did so
// its times. After the last step the run has run out, which ChainsRanOut checks for the whole start. It returns what
// to run once tx committed.
func (r *Runs) FireStep(ctx context.Context, tx DBTX, runID int64, deadline time.Time) (func(context.Context), error) {
	q := r.queries(tx)
	groupID, err := q.GetRunGroup(ctx, dbgen.GetRunGroupParams{OrgID: r.s.orgID, ID: runID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the run %d: %w", runID, err)
	}
	var logs []func(context.Context)
	committed, err := r.groups.ChainStep(ctx, tx, groupID, func(ctx context.Context, g *groups.Group) (
		groups.ChainProgress, error) {
		run, err := q.LockRun(ctx, dbgen.LockRunParams{OrgID: r.s.orgID, ID: runID})
		if errors.Is(err, pgx.ErrNoRows) {
			return groups.ChainProgress{}, nil
		}
		if err != nil {
			return groups.ChainProgress{}, fmt.Errorf("lock the run %d: %w", runID, err)
		}
		if run.State != RunRunning || !run.Deadline.Equal(deadline) {
			return groups.ChainProgress{}, nil
		}
		if g.Status != groups.StatusFiring {
			// A replica of an earlier version stopped being firing without stopping the runs: they stop now.
			return groups.ChainProgress{Changed: true}, r.stop(ctx, tx, q, g, groups.ChainStop{Command: stopOf(g.Status),
				At: r.s.clock.Now().UTC()}, func(f func(context.Context)) { logs = append(logs, f) })
		}
		w := &walk{r: r, tx: tx, q: q, g: g, run: run, now: r.s.clock.Now().UTC(), at: deadline.UTC(),
			made: run.StepsMade}
		p, err := w.walk(ctx)
		if err != nil {
			return groups.ChainProgress{}, err
		}
		if p.RanOut {
			group, chain, public, made, capped := g.PublicID, run.ChainPublicID.String, run.PublicID, w.made, w.capped
			logs = append(logs, func(ctx context.Context) {
				if r.log == nil {
					return
				}
				if capped {
					r.log.Log(ctx, logging.EscalationRunStepLimit, logging.F("group", group),
						logging.F("chain", chain), logging.F("run", public), logging.F("steps", made))
				}
				r.log.Log(ctx, logging.EscalationRunRanOut, logging.F("group", group), logging.F("chain", chain),
					logging.F("run", public))
			})
		}
		return p, nil
	})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) {
		if committed != nil {
			committed(ctx)
		}
		for _, f := range logs {
			f(ctx)
		}
	}, nil
}

// stopOf is the Command that left an Alert Group in a status other than firing.
func stopOf(s groups.Status) string {
	switch s {
	case groups.StatusAcknowledged:
		return groups.StopAcknowledge
	case groups.StatusSnoozed:
		return groups.StopSnooze
	case groups.StatusFiring, groups.StatusResolved:
	}
	return groups.StopResolve
}

// walk is one firing of a run's timer: the run as locked, its Alert Group, the business time now and at, the time the
// current step was due, which anchors the waits.
type walk struct {
	r     *Runs
	tx    DBTX
	q     runQueries
	g     *groups.Group
	run   dbgen.LockRunRow
	now   time.Time
	at    time.Time
	list  []runStep
	hours *teams.WorkingHours
	// made counts the steps the run made and skipped, this firing's included; capped is set once it reached
	// MaxStepsPerRun.
	made   int64
	capped bool
	// seq numbers the webhooks and the Pages of the firing, which keep the order of their steps.
	seq int
}

// MaxStepsPerFiring bounds the steps one firing makes. Repeats nested without a wait between them make many passes
// in one go; past this many steps the firing ends and the run goes on at the next one, due at once, so that one
// transaction never holds the Alert Group for long.
const MaxStepsPerFiring = 1000

// MaxStepsPerRun bounds the steps of a run, which repeats nested inside one another multiply: the steps a chain of
// escalation.limits makes when each repeat goes back its times once, MaxSteps*(1+MaxSteps*MaxRepeatTimes). A run that
// reaches it runs out there, as after its last step, so that its Alert Group becomes Unclaimed, and logs
// escalation_run_step_limit.
const MaxStepsPerRun = MaxSteps * (1 + MaxSteps*MaxRepeatTimes)

func (w *walk) walk(ctx context.Context) (groups.ChainProgress, error) {
	if err := json.Unmarshal(w.run.Steps, &w.list); err != nil {
		return groups.ChainProgress{}, fmt.Errorf("read the steps of run %s: %w", w.run.PublicID, err)
	}
	repeats := map[string]int{}
	if err := json.Unmarshal(w.run.Repeats, &repeats); err != nil {
		return groups.ChainProgress{}, fmt.Errorf("read the repeats of run %s: %w", w.run.PublicID, err)
	}
	p := groups.ChainProgress{Changed: true}
	idx := int(w.run.StepIndex)
	for range MaxStepsPerFiring {
		if idx >= len(w.list) {
			return p, w.ranOut(ctx, idx, repeats, &p)
		}
		if w.made >= MaxStepsPerRun {
			w.capped = true
			return p, w.ranOut(ctx, idx, repeats, &p)
		}
		st := w.list[idx]
		switch st.Kind {
		case KindWait:
			if err := w.record(ctx, st, OutcomeMade, ""); err != nil {
				return p, err
			}
			idx++
			next := w.at.Add(time.Duration(st.WaitSeconds) * time.Second)
			if next.After(w.now) {
				return p, w.wait(ctx, idx, repeats, next)
			}
			w.at = next
		case KindRepeat:
			if err := w.record(ctx, st, OutcomeMade, ""); err != nil {
				return p, err
			}
			key := strconv.Itoa(st.Number)
			if repeats[key] < st.RepeatTimes {
				repeats[key]++
				resetInner(w.list, repeats, st)
				idx = st.RepeatFrom - 1
			} else {
				idx++
			}
		case KindWebhook:
			skip, hook, err := w.webhook(ctx, st)
			if err != nil {
				return p, err
			}
			if skip != nil {
				p.Skips = append(p.Skips, *skip)
			}
			if hook != nil {
				hook.After, hook.Seq = len(p.Skips), w.seq
				w.seq++
				p.Webhooks = append(p.Webhooks, *hook)
			}
			idx++
		default:
			if err := w.page(ctx, st, &p); err != nil {
				return p, err
			}
			idx++
		}
	}
	return p, w.wait(ctx, idx, repeats, w.at)
}

// resetInner clears the passes of the repeat steps that the repeat st goes back over, from its step K up to itself:
// each pass makes its steps again (C-32.FR-15), so a repeat nested inside another one repeats again on every pass of
// the outer one.
func resetInner(list []runStep, repeats map[string]int, st runStep) {
	for _, inner := range list {
		if inner.Kind == KindRepeat && inner.Number >= st.RepeatFrom && inner.Number < st.Number {
			delete(repeats, strconv.Itoa(inner.Number))
		}
	}
}

// entry names the run, its chain and a step for escalation_step_skipped.
func (w *walk) entry(st runStep) groups.ChainEntry {
	return groups.ChainEntry{Run: w.run.ID, Chain: w.run.ChainID.Int64, ChainPublicID: w.run.ChainPublicID.String,
		ChainName: w.run.ChainName, Step: st.Number}
}

// page makes or skips a step that Pages or sends (C-32.FR-14), adding its skips and Pages to p: skipped for a
// Severity level its condition does not meet, outside the Working hours of its chain when limited to them, in a gap of
// the Schedule it Pages; for the people it names, each who became a Viewer or was disabled since is skipped, and the
// step with them when none is left. StepPages makes the step, its Pages after the skips of its people, or skips a
// page_team step that none of its chain's Destinations can carry (no_mention_possible, C-32.FR-9).
func (w *walk) page(ctx context.Context, st runStep, p *groups.ChainProgress) error {
	reason, err := w.skipReason(ctx, st)
	if err != nil {
		return err
	}
	if reason != "" {
		p.Skips = append(p.Skips, groups.StepSkip{ChainEntry: w.entry(st), Reason: reason})
		return w.record(ctx, st, OutcomeSkipped, reason)
	}
	people, skips, err := w.people(ctx, st)
	if err != nil {
		return err
	}
	p.Skips = append(p.Skips, skips...)
	if len(st.UserIDs) > 0 && len(people) == 0 {
		return w.record(ctx, st, OutcomeSkipped, skips[0].Reason)
	}
	if w.r.pages != nil {
		// The step is recorded next, after every step of the run before it: its seq is one more (InsertRunStep).
		made, err := w.r.pages.MakeStep(ctx, w.tx, w.g, w.ref(), StepRef{Seq: int(w.made) + 1, Number: st.Number,
			Kind: st.Kind, Importance: st.Importance, ScheduleID: st.ScheduleID, Schedule: st.Schedule,
			TeamID: st.TeamID, Everyone: st.Everyone, GatewayID: st.GatewayID}, people)
		if err != nil {
			return err
		}
		if made.Skip != "" {
			p.Skips = append(p.Skips, groups.StepSkip{ChainEntry: w.entry(st), Reason: made.Skip})
			return w.record(ctx, st, OutcomeSkipped, made.Skip)
		}
		for _, pg := range made.Pages {
			pg.After, pg.Seq = len(p.Skips), w.seq
			w.seq++
			p.Pages = append(p.Pages, pg)
		}
	}
	return w.record(ctx, st, OutcomeMade, "")
}

// ref names the run for StepPages: its chain, its owning Team when it started and its sources.
func (w *walk) ref() RunRef {
	ref := RunRef{ID: w.run.ID, PublicID: w.run.PublicID, ChainID: w.run.ChainID.Int64,
		ChainPublicID: w.run.ChainPublicID.String, ChainName: w.run.ChainName, OwningTeamID: w.run.OwningTeamID.Int64}
	var sources []source
	if json.Unmarshal(w.run.Sources, &sources) == nil {
		for _, s := range sources {
			ref.Sources = append(ref.Sources, RunSource(s))
		}
	}
	return ref
}

// skipReason is why a step that Pages or sends is skipped as a whole, empty when it is not.
func (w *walk) skipReason(ctx context.Context, st runStep) (string, error) {
	if !routing.SeverityCondition(st.Condition).Meets(string(w.g.Severity)) {
		return groups.SkipSeverity, nil
	}
	if st.WorkingHoursOnly {
		wh, err := w.workingHours(ctx)
		if err != nil {
			return "", err
		}
		if !wh.Contains(w.now) {
			return groups.SkipOutsideWorkingHours, nil
		}
	}
	if st.Schedule != "" && w.r.onCalls != nil {
		seg, err := w.r.onCalls.OnCallAtIn(ctx, w.tx, st.Schedule, w.now)
		switch {
		case errors.Is(err, schedules.ErrNotFound):
			return groups.SkipScheduleGap, nil
		case err != nil:
			return "", fmt.Errorf("read the on-call of schedule %s: %w", st.Schedule, err)
		case seg.User == nil:
			return groups.SkipScheduleGap, nil
		}
	}
	return "", nil
}

// workingHours are the Working hours of the run's chain, read once per firing: those of the owning Team the chain had
// when the run started, else the Organization's (C-25.FR-9, P-73).
func (w *walk) workingHours(ctx context.Context) (teams.WorkingHours, error) {
	if w.hours != nil {
		return *w.hours, nil
	}
	c := Chain{}
	if w.run.OwningTeamID.Valid {
		c.OwningTeam = &TeamRef{ID: w.run.OwningTeamID.Int64}
	}
	wh, err := w.r.s.workingHoursOn(ctx, w.q, c)
	if err != nil {
		return teams.WorkingHours{}, err
	}
	w.hours = &wh
	return wh, nil
}

// people are the people a step names who may still be Paged, and a skip for each who became a Viewer or was disabled
// or deleted since the step was saved (C-32.FR-14, C-32.AC-15).
func (w *walk) people(ctx context.Context, st runStep) ([]int64, []groups.StepSkip, error) {
	if len(st.UserIDs) == 0 {
		return nil, nil, nil
	}
	rows, err := w.q.ListPeopleStates(ctx, dbgen.ListPeopleStatesParams{OrgID: w.r.s.orgID, Ids: st.UserIDs})
	if err != nil {
		return nil, nil, fmt.Errorf("read the people of step %d of run %s: %w", st.Number, w.run.PublicID, err)
	}
	byID := make(map[int64]dbgen.ListPeopleStatesRow, len(rows))
	for _, u := range rows {
		byID[u.ID] = u
	}
	var ok []int64
	var skips []groups.StepSkip
	for _, id := range st.UserIDs {
		u, found := byID[id]
		reason := ""
		switch {
		case !found || u.Status == statusDisabled || u.Status == statusDeleted:
			reason = groups.SkipDisabled
		case u.Role == roleViewer:
			reason = groups.SkipViewer
		}
		if reason == "" {
			ok = append(ok, id)
			continue
		}
		skip := groups.StepSkip{ChainEntry: w.entry(st), Reason: reason}
		if found {
			// A person who is no longer there at all is skipped without being named.
			skip.Person = &groups.PagedUser{ID: id, PublicID: u.PublicID, Name: u.Name, Login: u.Login}
		}
		skips = append(skips, skip)
	}
	return ok, skips, nil
}

// record writes a step the run made or skipped, for "How it went", at the time it was due: a firing that catches up
// after downtime records each step at its own time.
func (w *walk) record(ctx context.Context, st runStep, outcome, reason string) error {
	if err := w.q.InsertRunStep(ctx, dbgen.InsertRunStepParams{OrgID: w.r.s.orgID, RunID: w.run.ID,
		Number: int32(st.Number), Kind: st.Kind, At: w.at, Outcome: outcome, //nolint:gosec // G115: at most 20
		Reason: pgtype.Text{String: reason, Valid: reason != ""}}); err != nil {
		return fmt.Errorf("record step %d of run %s: %w", st.Number, w.run.PublicID, err)
	}
	w.made++
	return nil
}

// wait saves the run at its next step and sets its timer at next, the end of a wait.
func (w *walk) wait(ctx context.Context, idx int, repeats map[string]int, next time.Time) error {
	if err := w.save(ctx, idx, repeats, RunRunning); err != nil {
		return err
	}
	if err := w.q.UpsertRunTimer(ctx, dbgen.UpsertRunTimerParams{OrgID: w.r.s.orgID, EscalationRunID: w.run.ID,
		Deadline: next, Now: w.now}); err != nil {
		return fmt.Errorf("set the timer of run %s: %w", w.run.PublicID, err)
	}
	if err := w.q.NotifyRunTimers(ctx, groups.TimersChannel); err != nil {
		return fmt.Errorf("wake the timer workers: %w", err)
	}
	return nil
}

// ranOut marks the run run out after its last step and removes its timer.
func (w *walk) ranOut(ctx context.Context, idx int, repeats map[string]int, p *groups.ChainProgress) error {
	if err := w.save(ctx, idx, repeats, RunRanOut); err != nil {
		return err
	}
	if err := w.q.DeleteRunTimers(ctx, dbgen.DeleteRunTimersParams{OrgID: w.r.s.orgID,
		RunIds: []int64{w.run.ID}}); err != nil {
		return fmt.Errorf("delete the timer of run %s: %w", w.run.PublicID, err)
	}
	p.RanOut = true
	return nil
}

func (w *walk) save(ctx context.Context, idx int, repeats map[string]int, state string) error {
	b, err := json.Marshal(repeats)
	if err != nil {
		return fmt.Errorf("encode the repeats of run %s: %w", w.run.PublicID, err)
	}
	p := dbgen.SaveRunParams{OrgID: w.r.s.orgID, ID: w.run.ID, StepIndex: int32(idx), //nolint:gosec // G115: bounded
		Repeats: b, State: state}
	if state != RunRunning {
		p.EndedAt = pgtype.Timestamptz{Time: w.now, Valid: true}
	}
	if err := w.q.SaveRun(ctx, p); err != nil {
		return fmt.Errorf("save run %s: %w", w.run.PublicID, err)
	}
	return nil
}
