// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package notificationrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/notificationrules/dbgen"
)

// The runs of Notification rules (C-31.FR-3). When a step Pages a person normally or importantly, Muster runs that
// rule for them as it is at that moment (Effective): a Mention action is a Page by Mention of the step in its chain's
// Threads, a call action a call through a Notification gateway, which the runs return (Started.Calls, Due.Calls) and
// internal/escalation queues through internal/gateways as a Page of the step, a wait delays the next action.
// The actions with no wait between them come due together, and the first actions of the people one step Pages that
// come due together are one Page naming them all. A wait is a timers row of the kind notification_rule_action whose
// subject is the rule run (schema.md §4.10), set by this package's own upsert and never by UpsertTimer, which any
// replica claims. A rule run stops when the chains of its Alert Group stop or start over — the Alert Group
// acknowledged, snoozed or resolved — in the dispatcher's transaction (StopForGroup), even when the run of its own chain
// ran out already, and an action that comes due after that is not made. A run whose timer comes due late, after Muster
// was down, with several Mentions overdue makes one Page of them instead of a burst, as notices_missed stands for
// overdue notices, and its next wait counts from then.
//
// This package imports no package of Alert Groups (internal/organization imports it, and internal/groups imports
// internal/organization): the steps of internal/escalation start the runs in the dispatcher's transaction, and the
// handler of their timers, escalation's FireRule, takes the Alert Group's lock through the dispatcher before Advance
// locks the rule run. Lock order: the Alert Group, then the rule run, then its timer.

// TimerKind is the kind of the timer of a rule run.
const TimerKind = "notification_rule_action"

// The states of a rule run.
const (
	RunRunning = "running"
	RunDone    = "done"
	RunStopped = "stopped"
)

// Call is a call action of a rule run that came due: the person, the rule run, and its Notification gateway by
// public_id (C-31.FR-3, C-30.FR-4).
type Call struct {
	User    int64
	RuleRun int64
	Gateway string
}

// runQueries are the queries of the rule runs, in the dispatcher's transaction.
type runQueries interface {
	GetRules(ctx context.Context, arg dbgen.GetRulesParams) (dbgen.GetRulesRow, error)
	GetOrganizationRules(ctx context.Context, orgID int64) (dbgen.GetOrganizationRulesRow, error)
	InsertRuleRun(ctx context.Context, arg dbgen.InsertRuleRunParams) (int64, error)
	UpsertRuleTimer(ctx context.Context, arg dbgen.UpsertRuleTimerParams) error
	DeleteRuleTimers(ctx context.Context, arg dbgen.DeleteRuleTimersParams) error
	NotifyRuleTimers(ctx context.Context, channel string) error
	StopRuleRuns(ctx context.Context, arg dbgen.StopRuleRunsParams) ([]int64, error)
	GetRuleRunGroup(ctx context.Context, arg dbgen.GetRuleRunGroupParams) (int64, error)
	LockRuleRun(ctx context.Context, arg dbgen.LockRuleRunParams) (dbgen.LockRuleRunRow, error)
	SaveRuleRun(ctx context.Context, arg dbgen.SaveRuleRunParams) error
}

// Runs holds the runs of the Notification rules of the Organization's Users.
type Runs struct {
	orgID   int64
	clock   clock.Clock
	channel string
	queries func(DBTX) runQueries
}

// RunsConfig is what the runs need: the business clock and the channel that wakes the timer workers.
type RunsConfig struct {
	Business      clock.Clock
	TimersChannel string
}

// Runs returns the runs of the rules of s.
func (s *Service) Runs(cfg RunsConfig) *Runs {
	return &Runs{orgID: s.cfg.OrgID, clock: cfg.Business, channel: cfg.TimersChannel,
		queries: func(d DBTX) runQueries { return dbgen.New(d) }}
}

// Start is a step that Pages people by their rules on an Alert Group: the Alert Group, the run of its chain, the
// step, its importance and the people, and RunStepSeq, the seq of the run step that makes it, which the later Mentions
// of the rule runs belong to (C-32.FR-12), 0 for none.
type Start struct {
	Group      int64
	Run        int64
	Step       int
	Importance string
	Users      []int64
	RunStepSeq int
}

// Started is what the start of the rule runs made at once: the people whose rule Mentioned them at once, in their
// order, and the wait until their next Mentions when every one of them has a rule whose next Mention follows after
// the same wait, 0 otherwise: the one Page that names them all says "again in N min" only when it holds for each; and
// the call actions that came due at once, each a Page of its own.
type Started struct {
	Mentioned    []int64
	AgainSeconds int
	Calls        []Call
}

// made is what the actions of a rule run from a position did: how many Mentions it made, the gateways of the calls,
// the position after them, the time its next action is due, zero once it ran out, and the wait until its next
// Mention, 0 for none.
type made struct {
	mentions int
	calls    []string
	position int
	next     time.Time
	again    int
}

// walk makes the actions of the rule from position, due at at, up to the first wait that ends after now. The Mentions
// and calls after a wait that is over by now come due too, collapsed into one Page, and when more than one Mention was
// collapsed the next wait counts from now rather than from when it was due (C-31.FR-3; notices_missed, C-17.FR-7).
func walk(actions []Action, position int, at, now time.Time) made {
	m := made{position: position}
	for m.position < len(actions) {
		a := actions[m.position]
		m.position++
		switch a.Kind {
		case KindMention:
			m.mentions++
		case KindCall:
			m.calls = append(m.calls, a.GatewayID)
		case KindWait:
			wait := time.Duration(a.WaitSeconds) * time.Second
			due := at.Add(wait)
			if due.After(now) {
				if m.mentions > 1 {
					due = now.Add(wait)
				}
				m.next = due
				m.again = againAfter(actions, m.position-1)
				return m
			}
			at = due
		}
	}
	return m
}

// againAfter is the wait from the action at i until the next Mention of the rule, 0 when none follows.
func againAfter(actions []Action, i int) int {
	wait := 0
	for _, a := range actions[i:] {
		switch a.Kind {
		case KindWait:
			wait += a.WaitSeconds
		case KindMention:
			return wait
		}
	}
	return 0
}

// ImportanceImportant is the importance of a Page by the important rule; any other Pages by the normal one.
const ImportanceImportant = "important"

// actionsOf is the rule of the importance in the set.
func actionsOf(s Set, importance string) []Action {
	if importance == ImportanceImportant {
		return s.Important
	}
	return s.Normal
}

// Start starts the run of each person's rule of the step's importance, in the dispatcher's transaction tx after the
// lock of the Alert Group (C-31.FR-3): the rule as it is now (Effective), its actions due at once made — the Mentions
// returned, for one Page of the step naming everyone they Mention, and the calls returned, each with its rule run —
// and its timer at the end of its first wait; a rule that ran out is done.
func (r *Runs) Start(ctx context.Context, tx DBTX, s Start) (Started, error) {
	q := r.queries(tx)
	now := r.clock.Now().UTC()
	var out Started
	timers := false
	for _, user := range s.Users {
		set, err := effectiveIn(ctx, q, r.orgID, user)
		if err != nil {
			return Started{}, err
		}
		actions := actionsOf(set, s.Importance)
		m := walk(actions, 0, now, now)
		p := dbgen.InsertRuleRunParams{OrgID: r.orgID, AlertGroupID: s.Group, EscalationRunID: s.Run,
			StepNumber: int32(s.Step), UserID: user, Importance: s.Importance, //nolint:gosec // G115: at most 20
			Actions: ruleJSON(actions), Position: int32(m.position), State: RunRunning, Now: now, //nolint:gosec // G115: at most MaxActions
			RunStepSeq: pgtype.Int4{Int32: int32(s.RunStepSeq), Valid: s.RunStepSeq > 0}} //nolint:gosec // G115: at most a run's steps
		if m.next.IsZero() {
			p.State, p.EndedAt = RunDone, pgtype.Timestamptz{Time: now, Valid: true}
		}
		id, err := q.InsertRuleRun(ctx, p)
		if err != nil {
			return Started{}, fmt.Errorf("start the notification rule of user %d: %w", user, err)
		}
		if !m.next.IsZero() {
			if err := q.UpsertRuleTimer(ctx, dbgen.UpsertRuleTimerParams{OrgID: r.orgID, NotificationRuleRunID: id,
				Deadline: m.next, Now: now}); err != nil {
				return Started{}, fmt.Errorf("set the timer of the notification rule of user %d: %w", user, err)
			}
			timers = true
		}
		for _, gw := range m.calls {
			out.Calls = append(out.Calls, Call{User: user, RuleRun: id, Gateway: gw})
		}
		if m.mentions > 0 {
			if len(out.Mentioned) == 0 {
				out.AgainSeconds = m.again
			} else if m.again != out.AgainSeconds {
				out.AgainSeconds = 0
			}
			out.Mentioned = append(out.Mentioned, user)
		}
	}
	if timers {
		if err := q.NotifyRuleTimers(ctx, r.channel); err != nil {
			return Started{}, fmt.Errorf("wake the timer workers: %w", err)
		}
	}
	return out, nil
}

// StopForGroup stops the rule runs of the Alert Group groupID that still run, and deletes their timers, in the
// dispatcher's transaction tx (C-31.FR-3): its chains stopped — acknowledged, snoozed, resolved — or started over,
// those that ran out included, whose rules may still repeat.
func (r *Runs) StopForGroup(ctx context.Context, tx DBTX, groupID int64, at time.Time) error {
	q := r.queries(tx)
	ids, err := q.StopRuleRuns(ctx, dbgen.StopRuleRunsParams{OrgID: r.orgID, AlertGroupID: groupID, Now: at})
	if err != nil {
		return fmt.Errorf("stop the notification rule runs: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := q.DeleteRuleTimers(ctx, dbgen.DeleteRuleTimersParams{OrgID: r.orgID, Ids: ids}); err != nil {
		return fmt.Errorf("delete the timers of the notification rule runs: %w", err)
	}
	return nil
}

// Group is the Alert Group of the rule run id, which the action of its timer locks first; false once the rule run is
// gone.
func (r *Runs) Group(ctx context.Context, tx DBTX, id int64) (int64, bool, error) {
	g, err := r.queries(tx).GetRuleRunGroup(ctx, dbgen.GetRuleRunGroupParams{OrgID: r.orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read the notification rule run %d: %w", id, err)
	}
	return g, true, nil
}

// Source is a source of the run of a chain as escalation_runs.sources stores it: its Route, or a Team by id.
type Source struct {
	Kind   string `json:"kind"`
	TeamID int64  `json:"team_id,omitempty"`
}

// Due is what a rule run made after a wait (Advance): the person, the run of the chain with its chain, sources and
// owning Team, the step with its importance and Schedule; Mention when it made a Mention, and whether the rule run
// Mentioned the person before, which makes it a later Mention, "again"; and the gateways of the call actions it made,
// by public_id, each a Page of its own.
type Due struct {
	Mention       bool
	Calls         []string
	Again         bool
	RuleRun       int64
	User          int64
	Run           int64
	Chain         int64
	ChainPublicID string
	ChainName     string
	Sources       []Source
	OwningTeam    int64
	Step          int
	Importance    string
	Schedule      int64
}

// Advance makes the actions of the rule run id that came due at the deadline its timer was claimed with, in the
// transaction tx after the dispatcher locked its Alert Group (C-31.FR-3); firing is whether the Alert Group is firing.
// It locks the rule run and does nothing when it no longer runs or its timer was moved or removed since it was
// claimed, so that a second replica firing the same action changes nothing. A rule run whose Alert Group is no longer
// firing, or whose chain's run stopped, as a replica of an earlier version may leave it, is stopped then. Otherwise it
// makes the actions due — the overdue Mentions after an outage as one — sets the timer at the next wait, or ends the
// rule run after its last action, and returns the Mention and the calls to Page, each call overdue by then included,
// nil when it made none.
func (r *Runs) Advance(ctx context.Context, tx DBTX, id int64, deadline time.Time, firing bool) (*Due, error) {
	q := r.queries(tx)
	run, err := q.LockRuleRun(ctx, dbgen.LockRuleRunParams{OrgID: r.orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock the notification rule run %d: %w", id, err)
	}
	if run.State != RunRunning || !run.Deadline.Equal(deadline) {
		return nil, nil
	}
	now := r.clock.Now().UTC()
	if !firing || run.RunState == RunStopped {
		return nil, r.save(ctx, q, run.ID, int(run.Position), RunStopped, now)
	}
	actions, err := parseRule(run.Actions)
	if err != nil {
		return nil, err
	}
	m := walk(actions, int(run.Position), deadline.UTC(), now)
	state := RunRunning
	if m.next.IsZero() {
		state = RunDone
	}
	if err := r.save(ctx, q, run.ID, m.position, state, now); err != nil {
		return nil, err
	}
	if !m.next.IsZero() {
		if err := q.UpsertRuleTimer(ctx, dbgen.UpsertRuleTimerParams{OrgID: r.orgID, NotificationRuleRunID: run.ID,
			Deadline: m.next, Now: now}); err != nil {
			return nil, fmt.Errorf("set the timer of the notification rule run %d: %w", id, err)
		}
		if err := q.NotifyRuleTimers(ctx, r.channel); err != nil {
			return nil, fmt.Errorf("wake the timer workers: %w", err)
		}
	}
	if m.mentions == 0 && len(m.calls) == 0 {
		return nil, nil
	}
	d := &Due{Mention: m.mentions > 0, Calls: m.calls, RuleRun: run.ID, User: run.UserID, Run: run.EscalationRunID,
		Chain:         run.ChainID,
		ChainPublicID: run.ChainPublicID, ChainName: run.ChainName, OwningTeam: run.OwningTeamID,
		Step: int(run.StepNumber), Importance: run.Importance, Schedule: run.ScheduleID,
		Again: mentionedBefore(actions, int(run.Position))}
	if err := json.Unmarshal(run.Sources, &d.Sources); err != nil {
		return nil, fmt.Errorf("read the sources of the run of notification rule run %d: %w", id, err)
	}
	return d, nil
}

// mentionedBefore reports whether an action of the rule before position is a Mention, which the rule run made.
func mentionedBefore(actions []Action, position int) bool {
	return slices.ContainsFunc(actions[:min(position, len(actions))], func(a Action) bool {
		return a.Kind == KindMention
	})
}

// save writes where a rule run is after its actions; a rule run that ended loses its timer.
func (r *Runs) save(ctx context.Context, q runQueries, id int64, position int, state string, now time.Time) error {
	p := dbgen.SaveRuleRunParams{OrgID: r.orgID, ID: id, Position: int32(position), State: state} //nolint:gosec // G115: at most MaxActions
	if state != RunRunning {
		p.EndedAt = pgtype.Timestamptz{Time: now, Valid: true}
		if err := q.DeleteRuleTimers(ctx, dbgen.DeleteRuleTimersParams{OrgID: r.orgID, Ids: []int64{id}}); err != nil {
			return fmt.Errorf("delete the timer of the notification rule run %d: %w", id, err)
		}
	}
	if err := q.SaveRuleRun(ctx, p); err != nil {
		return fmt.Errorf("save the notification rule run %d: %w", id, err)
	}
	return nil
}

// effectiveIn is the rule set in effect for the User userID read through q, the dispatcher's transaction: the
// Organization's current rules in the state organization, the stored ones otherwise (Effective).
func effectiveIn(ctx context.Context, q runQueries, orgID, userID int64) (Set, error) {
	row, err := q.GetRules(ctx, dbgen.GetRulesParams{OrgID: orgID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		org, err := q.GetOrganizationRules(ctx, orgID)
		if err != nil {
			return Set{}, fmt.Errorf("read the organization's notification rules: %w", err)
		}
		return ParseSet(org.NotificationRules)
	}
	if err != nil {
		return Set{}, fmt.Errorf("read the notification rules of user %d: %w", userID, err)
	}
	var s Set
	if s.Normal, err = parseRule(row.Normal); err != nil {
		return Set{}, err
	}
	if s.Important, err = parseRule(row.Important); err != nil {
		return Set{}, err
	}
	return s, nil
}
