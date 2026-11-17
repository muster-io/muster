// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/organization"
)

// EventEscalationStepSkipped is the lifecycle event of C-32.FR-14 (revised C-09.FR-22): a step of an Escalation chain
// that was skipped — outside the Working hours of its chain, for a Severity level its condition does not meet, in a
// gap of the Schedule it Pages, or for a person it names who became a Viewer or was disabled since it was saved — Quiet,
// without Mentions, of the Timeline kind escalation, nothing in a messenger. Its entry names the run, the chain, the
// step and the reason, and the person for a skip of a person.
const EventEscalationStepSkipped Event = "escalation_step_skipped"

// EventEscalationWebhook is the lifecycle event of C-32.FR-13 (revised C-09.FR-22): a step "Send to an outgoing
// webhook" that was made — Quiet, without Mentions, of the Timeline kind escalation, nothing in a messenger, and, unlike
// every other event, sent only to the outgoing webhook Destination the step names (C-15.FR-2 revised). Its entry names
// the run, the chain, the step and that Destination.
const EventEscalationWebhook Event = "escalation_webhook"

// The reasons of escalation_step_skipped (EscalationSkipReason); S-157 adds its own. A webhook step whose
// Destination was deleted, or is no longer an outgoing webhook in the mode events or both, is skipped with
// SkipDestinationDeleted or SkipWebhookEventsRequired; a page_team step that none of its chain's Destinations can
// carry, such as one whose only Destination is a Telegram channel, with SkipNoMentionPossible (C-32.FR-9).
const (
	SkipOutsideWorkingHours   = "outside_working_hours"
	SkipSeverity              = "severity"
	SkipScheduleGap           = "schedule_gap"
	SkipViewer                = "viewer"
	SkipDisabled              = "disabled"
	SkipDestinationDeleted    = "destination_deleted"
	SkipWebhookEventsRequired = "webhook_events_required" //nolint:gosec // G101: a skip reason, not a credential
	SkipNoMentionPossible     = "no_mention_possible"
)

// SkipReasons are the reasons of escalation_step_skipped, a closed list.
var SkipReasons = []string{SkipOutsideWorkingHours, SkipSeverity, SkipScheduleGap, SkipViewer, SkipDisabled,
	SkipDestinationDeleted, SkipWebhookEventsRequired, SkipNoMentionPossible}

// The Commands that stop the runs of the chains of an Alert Group (stop_command), and started_over for a run that a
// start-over found still running.
const (
	StopAcknowledge = "acknowledge"
	StopSnooze      = "snooze"
	StopResolve     = "resolve"
	StopStartedOver = "started_over"
)

// ChainRuns are the runs of the Escalation chains of Alert Groups (C-32.FR-4 to FR-7), which the dispatcher starts and
// stops in its transaction tx, after the Alert Group's lock: escalation implements it and internal/runtime wires it,
// since groups imports no package of Paging (ADR-0016).
type ChainRuns interface {
	// Start starts the chains that apply to g, in its current start g.ChainGeneration, as s says, and returns how
	// many runs it started; a chain that already runs in that start takes the new sources that name it (P-70).
	Start(ctx context.Context, tx DBTX, g *Group, s ChainStart) (int, error)
	// Stop stops every run of g that still runs, with what stopped it; a run that ran out stays as it is.
	Stop(ctx context.Context, tx DBTX, g *Group, s ChainStop) error
	// Started reports whether chains started in the current start of g, running, run out or stopped.
	Started(ctx context.Context, tx DBTX, g *Group) (bool, error)
	// RanOut reports whether every run of the current start of g ran out, at least one and none stopped (C-32.FR-7).
	RanOut(ctx context.Context, tx DBTX, g *Group) (bool, error)
}

// ChainStart says which chains a point of C-32.FR-5 starts: every chain that applies, at a start or a start-over; or,
// while the chains run, only those whose condition a rise of the Severity level from Before newly meets (Rise) and
// those the Teams that joined name (Teams).
type ChainStart struct {
	All    bool
	Rise   bool
	Before organization.SeverityLevel
	Teams  []int64
	At     time.Time
	// After queues a function, such as a log line, to run once the transaction committed; nil runs it at once.
	After func(f func(ctx context.Context))
}

// ChainStop is what stopped the runs of an Alert Group: the Command (StopAcknowledge, StopSnooze, StopResolve or
// StopStartedOver), the lifecycle event of the change that stopped them, 0 for none, and when.
type ChainStop struct {
	Command  string
	EventSeq int64
	At       time.Time
	// After queues a function to run once the transaction committed; nil runs it at once.
	After func(f func(ctx context.Context))
}

// SetChainRuns fills the runs of the Escalation chains, before anything changes an Alert Group; without them no chain
// runs and every Alert Group keeps the ack timeout of C-17.
func (s *Service) SetChainRuns(r ChainRuns) {
	s.d.chains = r
}

// chainsRun says whether the chains of an Alert Group in the state run (C-32.FR-5): only while it is firing, once it
// has been published or its chains started without a Destination to publish it.
func (s timerState) chainsRun() bool {
	return s.status == StatusFiring && (s.published || s.generation > 0)
}

// chainSync is what the dispatcher's sync of the chains did: whether a chain started in this change, and whether
// chains run in the current start of the Alert Group, started now or before.
type chainSync struct {
	started bool
	running bool
}

// syncChains starts the chains of g at the points of C-32.FR-5 that this change reached, before g is saved; b is the
// state before the change. A change that makes the chains run — the first Publication by an Alert or a Command, every
// change that makes g firing again — opens a new start of its chains (chain_generation) and starts every chain that
// applies; while they run, a rise of the Severity level by Alerts starts the chains whose condition it newly meets, and
// a Team that its Alerts bring starts that Team's chains; a change of configuration — a Quiet Publication that a raised
// Minimum severity released, the evaluation of the Teams on a save, a rise the Minimum severity made — starts none
// (ADR-0018). A chain that starts makes g no longer Unclaimed (C-32.FR-7). The runs are stopped after the change is
// recorded (stopChains), as they name its lifecycle event.
func (d *dispatcher) syncChains(ctx context.Context, q Queries, g *Group, b timerState, c *change, now time.Time,
	after *committed) (chainSync, error) {
	if d.chains == nil {
		return chainSync{}, nil
	}
	before, running := b.chainsRun(), timerStateOf(g).chainsRun()
	var start ChainStart
	switch {
	case !running || c.configuration || c.noChains:
		return chainSync{}, nil
	case !before:
		g.ChainGeneration++
		start = ChainStart{All: true, At: now, After: after.add}
	default:
		start = ChainStart{Before: b.severity, At: now, After: after.add}
		for _, e := range c.entries {
			switch {
			case (e.Event == EventSeverityRaised || e.Event == EventUrgencyRaised) && e.Reason != ReasonMinimumSeverity:
				start.Rise = true
			case e.Event == EventTeamJoined && e.TeamID != nil:
				start.Teams = append(start.Teams, *e.TeamID)
			}
		}
		if (!start.Rise || rank(g.Severity) <= rank(b.severity)) && len(start.Teams) == 0 {
			return chainSync{}, nil
		}
		if g.ChainGeneration == 0 {
			// Published Quietly by a change of configuration, the chains never started: this point opens their start.
			g.ChainGeneration = 1
		}
	}
	n, err := d.chains.Start(ctx, q.DB(), g, start)
	if err != nil {
		return chainSync{}, fmt.Errorf("start the escalation chains of alert group #%d: %w", g.Number, err)
	}
	if n == 0 {
		return chainSync{}, nil
	}
	g.Unclaimed = false
	return chainSync{started: true, running: true}, nil
}

// chainsRunning reports whether chains run in the current start of g, so that its ack timeout does not start: when
// one started in this change, or before it, as at its creation without a Destination before its first Publication.
func (d *dispatcher) chainsRunning(ctx context.Context, q Queries, g *Group, s chainSync) (bool, error) {
	if s.running {
		return true, nil
	}
	if d.chains == nil || g.ChainGeneration == 0 {
		return false, nil
	}
	ok, err := d.chains.Started(ctx, q.DB(), g)
	if err != nil {
		return false, fmt.Errorf("read the escalation chains of alert group #%d: %w", g.Number, err)
	}
	return ok, nil
}

// stopChains stops the runs of g when the change made it stop being firing — Acknowledge, Snooze or Resolve, by a
// person or the system (C-32.FR-5, C-10.FR-1) — naming the lifecycle event of the change that did; b is the state
// before the change.
func (d *dispatcher) stopChains(ctx context.Context, q Queries, g *Group, b timerState, recorded []Recorded,
	now time.Time, after *committed) error {
	if d.chains == nil || !b.chainsRun() || g.Status == StatusFiring {
		return nil
	}
	command := StopResolve
	switch g.Status {
	case StatusAcknowledged:
		command = StopAcknowledge
	case StatusSnoozed:
		command = StopSnooze
	case StatusFiring, StatusResolved:
	}
	var seq int64
	for _, r := range recorded {
		if rowOf(r.Event, r.Variant).Kind == KindStatus {
			seq = r.Seq
		}
	}
	if err := d.chains.Stop(ctx, q.DB(), g, ChainStop{Command: command, EventSeq: seq, At: now,
		After: after.add}); err != nil {
		return fmt.Errorf("stop the escalation chains of alert group #%d: %w", g.Number, err)
	}
	return nil
}

// startUnreached starts the chains of g at its creation when the re-render step found no Destination that reaches it,
// or only Broken ones (C-32.FR-5): a new start of its chains, written after the dispatcher saved g. Its ack timeout
// still waits for its first Publication (C-17.FR-1).
func (d *dispatcher) startUnreached(ctx context.Context, q Queries, g *Group, now time.Time, after *committed) error {
	if d.chains == nil || g.Status != StatusFiring || g.FirstPublishedAt != nil || g.ChainGeneration != 0 {
		return nil
	}
	g.ChainGeneration = 1
	if err := q.SetChainGeneration(ctx, dbgen.SetChainGenerationParams{OrgID: d.orgID, ID: g.ID,
		ChainGeneration: g.ChainGeneration}); err != nil {
		return fmt.Errorf("start the escalation chains of alert group #%d: %w", g.Number, err)
	}
	if _, err := d.chains.Start(ctx, q.DB(), g, ChainStart{All: true, At: now, After: after.add}); err != nil {
		return fmt.Errorf("start the escalation chains of alert group #%d: %w", g.Number, err)
	}
	return nil
}

// ChainEntry names the run, the chain and the step of an escalation entry by internal ids, with the chain's public_id
// and name for the outgoing webhook events; Chain is 0 and ChainPublicID empty once the chain was deleted.
type ChainEntry struct {
	Run           int64
	Chain         int64
	ChainPublicID string
	ChainName     string
	Step          int
}

// StepSkip is one escalation_step_skipped: the run, the chain and the step, and the reason, with the person of a skip
// of a person (SkipViewer, SkipDisabled).
type StepSkip struct {
	ChainEntry
	Reason string
	Person *PagedUser
}

// StepWebhook is one escalation_webhook (C-32.FR-13): the run, the chain and the step, and the outgoing webhook
// Destination it is sent to, which the run checked in the step's transaction. After is its place among the skips of
// the same ChainProgress: it was made after the first After of them, so that the events keep the order of the steps;
// Seq orders it among the Pages with the same After.
type StepWebhook struct {
	ChainEntry
	Destination JoinedDestination
	After       int
	Seq         int
}

// ChainProgress is what one step of a run, or one action of a Notification rule, did to its Alert Group: the skips,
// the webhooks and the Pages by Mention to record, whether the run ran out, and whether anything of the run changed,
// which the Alert Group's read shows.
type ChainProgress struct {
	Skips    []StepSkip
	Webhooks []StepWebhook
	Pages    []StepPage
	RanOut   bool
	Changed  bool
}

// chainSteps records, in the order of the steps, the Quiet escalation_step_skipped of each skip (C-32.FR-14), the
// Quiet escalation_webhook of each step that sent to an outgoing webhook (C-32.FR-13) and the Pages by Mention with
// their paged and page_not_delivered (C-32.FR-9, FR-10, pages.go), which ChainStep dispatches, whatever the status: a
// system transition that changes no status. ended collects the Pages that ended at once.
type chainSteps struct {
	skips    []StepSkip
	webhooks []StepWebhook
	pages    []StepPage
	targets  PageTargets
	orgID    int64
	now      time.Time
	ended    *[]pageEnd
}

func (chainSteps) precondition(*Group) error { return nil }

// stepItem is a webhook or a Page of a ChainProgress, at its place among the skips.
type stepItem struct {
	after, seq int
	webhook    *StepWebhook
	page       *StepPage
}

func (t chainSteps) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	next := 0
	skipsTo := func(n int) {
		for ; next < n && next < len(t.skips); next++ {
			s := t.skips[next]
			chain := s.ChainEntry
			e := entry{Event: EventEscalationStepSkipped, Reason: s.Reason, Chain: &chain}
			if s.Person != nil {
				e.Page = &Page{Users: []PagedUser{*s.Person}, Reason: s.Reason}
			}
			c.add(e)
		}
	}
	items := make([]stepItem, 0, len(t.webhooks)+len(t.pages))
	for i := range t.webhooks {
		items = append(items, stepItem{after: t.webhooks[i].After, seq: t.webhooks[i].Seq, webhook: &t.webhooks[i]})
	}
	for i := range t.pages {
		items = append(items, stepItem{after: t.pages[i].After, seq: t.pages[i].Seq, page: &t.pages[i]})
	}
	slices.SortStableFunc(items, func(a, b stepItem) int {
		return cmp.Or(cmp.Compare(a.after, b.after), cmp.Compare(a.seq, b.seq))
	})
	for _, it := range items {
		skipsTo(it.after)
		if w := it.webhook; w != nil {
			chain, dest := w.ChainEntry, w.Destination
			c.add(entry{Event: EventEscalationWebhook, Chain: &chain, Destination: &dest})
			continue
		}
		if t.targets == nil {
			continue
		}
		if err := t.page(ctx, q, g, *it.page, c); err != nil {
			return err
		}
	}
	skipsTo(len(t.skips))
	return nil
}

// chainsRanOut makes a firing Alert Group Unclaimed once every chain started on it in its current start ran out
// without an acknowledgement, recording unclaimed (C-32.FR-7, C-17.FR-11): once each time it becomes Unclaimed, so a
// second run of the same step, or a chain that runs out after another chain already made it Unclaimed, records nothing.
type chainsRanOut struct {
	chains ChainRuns
	tx     DBTX
}

func (t chainsRanOut) precondition(g *Group) error {
	if g.Status != StatusFiring || g.Unclaimed {
		return errNotDue
	}
	return nil
}

func (t chainsRanOut) apply(ctx context.Context, _ Queries, g *Group, c *change) error {
	out, err := t.chains.RanOut(ctx, t.tx, g)
	if err != nil {
		return fmt.Errorf("read the escalation chains of alert group #%d: %w", g.Number, err)
	}
	if !out {
		return nil
	}
	g.Unclaimed = true
	c.add(entry{Event: EventUnclaimed})
	return nil
}

// ChainStep makes one step of a run of an Escalation chain on the Alert Group groupID in the timer's transaction tx
// (C-32.FR-5, FR-13, FR-14): it locks the Alert Group first, as every change of it does, runs step on it — which reads
// and writes the run under that lock — and records what step did through the dispatcher as system transitions with
// Muster as the actor: escalation_step_skipped for each skip, escalation_webhook for each step that sent to an
// outgoing webhook and paged and page_not_delivered for each Page by Mention, in the order of the steps (chainSteps);
// the timer of a Notification rule's next action goes through it too. When the run ran out, unclaimed once every run of the
// current start has (ChainsRanOut). An Alert Group that no longer exists changes nothing. It returns what to run once
// tx committed.
func (s *Service) ChainStep(ctx context.Context, tx DBTX, groupID int64,
	step func(ctx context.Context, g *Group) (ChainProgress, error)) (func(context.Context), error) {
	q := s.queries(tx)
	locked, err := lock(ctx, q, s.orgID, []int64{groupID})
	if err != nil {
		return nil, err
	}
	g := locked[groupID]
	if g == nil {
		return nil, nil
	}
	p, err := step(ctx, g)
	if err != nil {
		return nil, err
	}
	for _, sk := range p.Skips {
		if !slices.Contains(SkipReasons, sk.Reason) {
			return nil, fmt.Errorf("escalation_step_skipped with the reason %q", sk.Reason)
		}
	}
	for _, w := range p.Webhooks {
		if w.Destination.ID == 0 {
			return nil, fmt.Errorf("escalation_webhook of step %d without its destination", w.Step)
		}
	}
	for _, pg := range p.Pages {
		if len(pg.People) == 0 && !pg.teamPage() {
			return nil, fmt.Errorf("a page of step %d names nobody", pg.Step)
		}
	}
	var after committed
	if len(p.Skips) > 0 || len(p.Webhooks) > 0 || len(p.Pages) > 0 {
		var ended []pageEnd
		if err := s.d.dispatch(ctx, q, g, System, chainSteps{skips: p.Skips, webhooks: p.Webhooks, pages: p.Pages,
			targets: s.d.pages, orgID: s.orgID, now: s.clock.Now().UTC(), ended: &ended}, &after); err != nil {
			return nil, err
		}
		countPages(&after, ended)
	}
	if p.RanOut && s.d.chains != nil {
		err := s.d.dispatch(ctx, q, g, System, chainsRanOut{chains: s.d.chains, tx: tx}, &after)
		if err != nil && !errors.Is(err, errNotDue) {
			return nil, err
		}
	}
	if p.Changed {
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: live.HintAlertGroup, ID: g.PublicID}); err != nil {
			return nil, err
		}
	}
	return after.run, nil
}
