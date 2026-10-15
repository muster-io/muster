// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/timers"
)

// The timer kinds of the ack timeout and the Reminders (C-17).
const (
	TimerAckTimeout = "ack_timeout"
	TimerReminder   = "reminder"
)

// timerPolicy is the timer policy of a Route as it is now (route.ack_timeout, route.reminders,
// route.auto_unacknowledge): a timer reads it when it is set and again when it fires.
type timerPolicy struct {
	ackTimeout   bool
	ackFirst     time.Duration
	reminders    bool
	reminderBase time.Duration
	reminderCap  time.Duration
	autoUnack    bool
}

// timerPolicy reads the timer policy of the Route id.
func (d *dispatcher) timerPolicy(ctx context.Context, q Queries, id int64) (timerPolicy, error) {
	r, err := q.GetTimerPolicy(ctx, dbgen.GetTimerPolicyParams{OrgID: d.orgID, ID: id})
	if err != nil {
		return timerPolicy{}, fmt.Errorf("read the timer policy of route %d: %w", id, err)
	}
	return timerPolicy{ackTimeout: r.AckTimeoutEnabled,
		ackFirst:  time.Duration(r.AckTimeoutFirstIntervalSeconds) * time.Second,
		reminders: r.RemindersEnabled, reminderBase: time.Duration(r.RemindersFirstIntervalSeconds) * time.Second,
		reminderCap: time.Duration(r.RemindersCapSeconds) * time.Second, autoUnack: r.AutoUnacknowledge}, nil
}

// timerState is what the timer sync of the dispatcher compares before and after a change.
type timerState struct {
	status    Status
	published bool
	unclaimed bool
	owner     int64
}

func timerStateOf(g *Group) timerState {
	b := timerState{status: g.Status, published: g.FirstPublishedAt != nil, unclaimed: g.Unclaimed}
	if g.OwnerUserID != nil {
		b.owner = *g.OwnerUserID
	}
	return b
}

// ackTimeoutRuns says whether the ack timeout of an Alert Group in the state runs (C-17.FR-1): only while it is
// firing, once it has been published.
func (s timerState) ackTimeoutRuns() bool { return s.status == StatusFiring && s.published }

// syncTimers keeps the ack timeout and the Reminders of g with its status after a change (ADR-0016); b is the state
// before the change.
func (d *dispatcher) syncTimers(ctx context.Context, q Queries, g *Group, b timerState, now time.Time) error {
	if err := d.syncAckTimeout(ctx, q, g, b, now); err != nil {
		return err
	}
	return d.syncReminders(ctx, q, g, b, now)
}

// syncAckTimeout keeps the ack timeout of g with its status (C-17.FR-1, FR-3): every change that makes a published
// Alert Group firing — Reopen into firing, Unacknowledge, Unsnooze, Unresolve, a Snooze ending, a rise to Urgent,
// auto-unacknowledge, the release of a disabled or deleted Owner — or that publishes a firing one starts it over at its
// time, at the first Publication for the latter; every change that stops it being firing — Acknowledge, Snooze, Resolve
// by a person or the system — stops it and ends Unclaimed.
func (d *dispatcher) syncAckTimeout(ctx context.Context, q Queries, g *Group, b timerState, now time.Time) error {
	before, after := b.ackTimeoutRuns(), timerStateOf(g).ackTimeoutRuns()
	switch {
	case after && !before:
		start := now
		if !b.published {
			start = *g.FirstPublishedAt
		}
		return d.startAckTimeout(ctx, q, g, start, now)
	case before && !after:
		g.AckTimeoutStartedAt, g.AckTimeoutNoticesSent, g.Unclaimed = nil, 0, false
		return stopTimer(ctx, q, d.orgID, g.ID, TimerAckTimeout)
	}
	return nil
}

// startAckTimeout starts the ack timeout of g at start where its Route has one: no notice sent, not Unclaimed, and
// the ack_timeout timer at notice 1.
func (d *dispatcher) startAckTimeout(ctx context.Context, q Queries, g *Group, start, now time.Time) error {
	p, err := d.timerPolicy(ctx, q, g.RouteID)
	if err != nil {
		return err
	}
	g.AckTimeoutNoticesSent, g.Unclaimed = 0, false
	if !p.ackTimeout {
		g.AckTimeoutStartedAt = nil
		return nil
	}
	g.AckTimeoutStartedAt = &start
	return setNoticeTimer(ctx, q, d.orgID, g.ID, TimerAckTimeout, timers.AckNoticeDue(start, p.ackFirst, 1), 1, now)
}

// syncReminders keeps the Reminders of g with its acknowledgement (C-17.FR-4, FR-8, C-10.FR-4): an Acknowledge from
// firing or snoozed and a Takeover start them over for the Owner — no Reminder sent or unanswered, no answer — with
// Reminder 1 one first interval later; a Reopen into acknowledged continues them, the next Reminder one interval of
// its step after the Reopen; every change that ends the acknowledgement — Unacknowledge, Snooze, Resolve, a rise to
// Urgent, auto-unacknowledge, the release of a disabled or deleted Owner — stops them, its counters kept for a Reopen.
func (d *dispatcher) syncReminders(ctx context.Context, q Queries, g *Group, b timerState, now time.Time) error {
	acknowledged, was := g.Status == StatusAcknowledged, b.status == StatusAcknowledged
	switch {
	case acknowledged && b.status == StatusResolved:
		return d.scheduleReminder(ctx, q, g, now, g.RemindersSent+1)
	case acknowledged && (!was || b.owner != *g.OwnerUserID):
		g.RemindersSent, g.RemindersUnanswered, g.LastReminderAt, g.OwnerAnsweredAt = 0, 0, nil, nil
		return d.scheduleReminder(ctx, q, g, now, 1)
	case was && !acknowledged:
		return stopTimer(ctx, q, d.orgID, g.ID, TimerReminder)
	}
	return nil
}

// scheduleReminder sets the reminder timer of g at Reminder k, one interval of its step after from, where its Route
// has Reminders; without them there is none.
func (d *dispatcher) scheduleReminder(ctx context.Context, q Queries, g *Group, from time.Time, k int64) error {
	p, err := d.timerPolicy(ctx, q, g.RouteID)
	if err != nil {
		return err
	}
	if !p.reminders {
		return stopTimer(ctx, q, d.orgID, g.ID, TimerReminder)
	}
	return setNoticeTimer(ctx, q, d.orgID, g.ID, TimerReminder,
		from.Add(timers.ReminderInterval(k, p.reminderBase, p.reminderCap)), k, from)
}

// markAnswered records that the Owner answered a Reminder (C-17.FR-5): any change of g by the User who owned it before
// the change — "Still on it", a Command that changed it or a Note. b is the state before the change.
func markAnswered(g *Group, b timerState, actor Actor, now time.Time) {
	if actor.Person.Kind == audit.ActorUser && b.owner != 0 && b.owner == actor.Person.ID {
		g.OwnerAnsweredAt = &now
	}
}

// hintUnclaimed sends the alert-groups hint when a change made g Unclaimed or ended it, as the Unclaimed filter of
// the list may now match it or not (C-09.FR-13, FR-25).
func (d *dispatcher) hintUnclaimed(ctx context.Context, q Queries, g *Group, b timerState) error {
	if b.unclaimed == g.Unclaimed {
		return nil
	}
	return q.Notify(ctx, db.Hint{OrgID: d.orgID, Type: live.HintAlertGroups})
}

// markPublished records the first Publication of an Alert Group at at; the dispatcher's timer sync then starts its
// ack timeout there when it is firing. An Alert Group published before changes nothing.
type markPublished struct {
	at time.Time
}

func (t markPublished) precondition(g *Group) error {
	if g.FirstPublishedAt != nil {
		return errNotDue
	}
	return nil
}

func (t markPublished) apply(_ context.Context, _ Queries, g *Group, c *change) error {
	g.FirstPublishedAt = &t.at
	c.written = true
	return nil
}

// MarkPublished records the first Publication of the Alert Group groupID at at, the published_at of that delivery or
// the delivered_at of its created event (C-17.FR-1, D277, D298): a system transition of the dispatcher in a
// transaction of its own, which delivery calls after the transaction that recorded the Publication committed, as that
// one locks deliveries before alert_groups and the dispatcher the other way round. It changes nothing for an Alert
// Group published before, so that a republication after the Root message was deleted never starts the ack timeout
// over and a repeated call is safe.
func (s *Service) MarkPublished(ctx context.Context, groupID int64, at time.Time) error {
	at = at.UTC().Truncate(time.Microsecond)
	var after committed
	err := s.store.InTx(ctx, func(q Queries) error {
		after = nil
		locked, err := lock(ctx, q, s.orgID, []int64{groupID})
		if err != nil {
			return err
		}
		g := locked[groupID]
		if g == nil {
			return nil
		}
		err = s.d.dispatch(ctx, q, g, System, markPublished{at: at}, &after)
		if errors.Is(err, errNotDue) {
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("mark alert group %d published: %w", groupID, err)
	}
	after.run(ctx)
	return nil
}

// ackNotice is the ack_timeout timer of an Alert Group at now (C-17.FR-2, FR-3, FR-7): the notices that came due
// since the last one sent, by the anchored schedule of the ack timeout and the Route's first interval as it is now —
// one ack_timeout, or one notices_missed for several overdue ones — then the next notice, or, after the last,
// unclaimed. sent and missed are what it recorded, for its log line. A firing replayed after it was handled finds no
// notice due and only sets the timer where the next one is due.
type ackNotice struct {
	now    time.Time
	policy timerPolicy
	orgID  int64
	sent   int64
	missed int64
}

func (t *ackNotice) precondition(g *Group) error {
	if g.Status != StatusFiring || g.AckTimeoutStartedAt == nil ||
		g.AckTimeoutNoticesSent >= timers.AckTimeoutNotices || !t.policy.ackTimeout {
		return errNotDue
	}
	return nil
}

func (t *ackNotice) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	start, first, sent := *g.AckTimeoutStartedAt, t.policy.ackFirst, g.AckTimeoutNoticesSent
	k := timers.DueAckNotices(start, first, sent, t.now)
	if k == 0 {
		return setNoticeTimer(ctx, q, t.orgID, g.ID, TimerAckTimeout, timers.AckNoticeDue(start, first, sent+1),
			sent+1, t.now)
	}
	sent += k
	if k == 1 {
		c.add(entry{Event: EventAckTimeout, NoticeNumber: ptr(sent)})
	} else {
		c.add(entry{Event: EventNoticesMissed, NoticeNumber: ptr(sent), MissedCount: ptr(k),
			Mentions: []Mention{MentionAckTimeout}})
		t.missed = k
	}
	g.AckTimeoutNoticesSent, t.sent = sent, sent
	if sent >= timers.AckTimeoutNotices {
		g.Unclaimed = true
		c.add(entry{Event: EventUnclaimed})
		return stopTimer(ctx, q, t.orgID, g.ID, TimerAckTimeout)
	}
	next := timers.AckNoticeDue(start, first, sent+1)
	if k > 1 {
		next = t.now.Add(timers.AckNoticeGap(first, sent+1))
	}
	return setNoticeTimer(ctx, q, t.orgID, g.ID, TimerAckTimeout, next, sent+1, t.now)
}

// FireAckTimeout is the timer ack_timeout of an Alert Group (C-17.FR-2, FR-3, FR-7), through the dispatcher as a
// system transition in the timer's transaction tx. An Alert Group that is no longer firing, whose ack timeout ended
// or whose Route turned it off changes nothing, and the timer is then removed without a notice; the notices due are
// counted on the locked row, so the timer is safe to fire twice. It returns what to run once that transaction
// committed.
func (s *Service) FireAckTimeout(ctx context.Context, tx dbgen.DBTX, groupID int64) (func(context.Context), error) {
	q := s.queries(tx)
	locked, err := lock(ctx, q, s.orgID, []int64{groupID})
	if err != nil {
		return nil, err
	}
	g := locked[groupID]
	if g == nil {
		return nil, nil
	}
	policy, err := s.d.timerPolicy(ctx, q, g.RouteID)
	if err != nil {
		return nil, err
	}
	t := &ackNotice{now: s.clock.Now().UTC(), policy: policy, orgID: s.orgID}
	var after committed
	err = s.d.dispatch(ctx, q, g, System, t, &after)
	if errors.Is(err, errNotDue) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.sent > 0 {
		group, notice, missed := g.PublicID, t.sent, t.missed
		after.add(func(ctx context.Context) {
			s.log.Log(ctx, logging.TimerFired, logging.F("kind", TimerAckTimeout), logging.F("group", group),
				logging.F("notice", notice), logging.F("missed", missed))
		})
	}
	return after.run, nil
}

// ReasonNoAnswer is the reason of an auto_unacknowledged entry (C-17.FR-5); ReasonCodeNoAnswer is its code.
const (
	ReasonNoAnswer     = "No answer to the last two Reminders"
	ReasonCodeNoAnswer = "reminders_unanswered"
)

// StatusReasonCode is the code of the reason of an automatic status change, as the API gives it beside the reason
// (reason_code): the reason itself where it is one already — why an Alert resolved, why an Owner was released — and
// ReasonCodeNoAnswer for ReasonNoAnswer; "" for any other reason.
func StatusReasonCode(reason string) string {
	switch {
	case reason == ReasonNoAnswer:
		return ReasonCodeNoAnswer
	case reason == ReasonOwnerDisabled, reason == ReasonOwnerDeleted, slices.Contains(ingest.ResolveReasons, reason):
		return reason
	}
	return ""
}

// The reasons of reminder_skipped.
const (
	skippedNotAcknowledged = "not_acknowledged"
	skippedPolicyOff       = "policy_off"
)

// ReminderCheck reports whether the Reminder recorded as the lifecycle event seq of the Alert Group groupID reached at
// least one Destination, read through db: a Thread reply of it sent, or its reminder event delivered to an events-mode
// outgoing webhook. delivery.ReminderReached is the one of the runtime, injected because delivery imports this package.
type ReminderCheck func(ctx context.Context, db DBTX, groupID, seq int64) (bool, error)

// SetReminderReached fills the answer rule's check of the Reminders; without it no Reminder counts as reached, so none
// is unanswered and auto-unacknowledge never happens.
func (s *Service) SetReminderReached(f ReminderCheck) {
	s.d.reached = f
}

// reminderDue is the reminder timer of an acknowledged Alert Group at now, claimed with its deadline (C-17.FR-4, FR-5,
// FR-7): the answer rule first — the last Reminder counts as unanswered when it reached a Destination and the Owner
// has not answered since — then, with auto-unacknowledge on and timers.auto_unacknowledge_after unanswered in a row,
// firing without an Owner instead of a Reminder; otherwise the Reminders due — one reminder, or one notices_missed for
// several overdue ones, which counts as one Reminder for the answer rule — and the timer at the next one. A timer
// moved since it was claimed changes nothing. notice and missed are what it did, for its log line.
type reminderDue struct {
	now, deadline time.Time
	policy        timerPolicy
	orgID         int64
	reached       ReminderCheck
	notice        int64
	missed        int64
}

func (t *reminderDue) precondition(g *Group) error {
	if g.Status != StatusAcknowledged || !t.policy.reminders {
		return errNotDue
	}
	return nil
}

func (t *reminderDue) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	deadline, err := q.GetGroupTimer(ctx, dbgen.GetGroupTimerParams{OrgID: t.orgID, AlertGroupID: g.ID,
		Kind: TimerReminder})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the reminder timer: %w", err)
	}
	if !deadline.Equal(t.deadline) {
		return nil
	}
	base, limit, sent := t.policy.reminderBase, t.policy.reminderCap, g.RemindersSent
	k, _ := timers.DueReminders(t.deadline, sent+1, base, limit, t.now)
	if k == 0 {
		return setNoticeTimer(ctx, q, t.orgID, g.ID, TimerReminder, t.deadline, sent+1, t.now)
	}
	if sent > 0 {
		unanswered, err := t.unanswered(ctx, q, g)
		if err != nil {
			return err
		}
		if unanswered {
			g.RemindersUnanswered++
		} else {
			g.RemindersUnanswered = 0
		}
	}
	if t.policy.autoUnack && g.RemindersUnanswered >= timers.AutoUnacknowledgeAfter {
		previous := g.OwnerUserID
		g.Status, g.OwnerUserID, g.AcknowledgedAt = StatusFiring, nil, nil
		c.reason = string(EventAutoUnacknowledged)
		c.add(entry{Event: EventAutoUnacknowledged, From: StatusAcknowledged, To: StatusFiring, Reason: ReasonNoAnswer,
			PreviousOwner: previous})
		t.notice = sent + 1
		return nil
	}
	sent += k
	if k == 1 {
		c.add(entry{Event: EventReminder, NoticeNumber: ptr(sent), OwnerUserID: g.OwnerUserID})
	} else {
		c.add(entry{Event: EventNoticesMissed, NoticeNumber: ptr(sent), MissedCount: ptr(k),
			Mentions: []Mention{MentionOwner}, OwnerUserID: g.OwnerUserID})
		t.missed = k
	}
	g.RemindersSent, g.LastReminderAt, t.notice = sent, ptr(t.now), sent
	next := t.deadline
	if k > 1 {
		next = t.now
	}
	return setNoticeTimer(ctx, q, t.orgID, g.ID, TimerReminder, next.Add(timers.ReminderInterval(sent+1, base, limit)),
		sent+1, t.now)
}

// unanswered is the answer rule for the last Reminder of g (C-17.FR-5): it reached at least one Destination and the
// Owner has not answered since it was sent. A Reminder that reached nobody — no Destination, or only Broken ones —
// never counts.
func (t *reminderDue) unanswered(ctx context.Context, q Queries, g *Group) (bool, error) {
	if t.reached == nil || (g.OwnerAnsweredAt != nil && g.LastReminderAt != nil &&
		g.OwnerAnsweredAt.After(*g.LastReminderAt)) {
		return false, nil
	}
	seq, err := q.GetLastReminderSeq(ctx, dbgen.GetLastReminderSeqParams{OrgID: t.orgID, AlertGroupID: g.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the last reminder: %w", err)
	}
	reached, err := t.reached(ctx, q.DB(), g.ID, seq)
	if err != nil {
		return false, fmt.Errorf("check whether the last reminder reached a destination: %w", err)
	}
	return reached, nil
}

// FireReminder is the timer reminder of an Alert Group, claimed with its deadline (C-17.FR-4, FR-5, FR-7), through the
// dispatcher as a system transition in the timer's transaction tx. One that comes due on an Alert Group that is no
// longer acknowledged, or whose Route turned the Reminders off, changes nothing and logs reminder_skipped, and the
// timer is then removed. It returns what to run once that transaction committed.
func (s *Service) FireReminder(ctx context.Context, tx dbgen.DBTX, groupID int64, deadline time.Time) (
	func(context.Context), error) {
	q := s.queries(tx)
	locked, err := lock(ctx, q, s.orgID, []int64{groupID})
	if err != nil {
		return nil, err
	}
	g := locked[groupID]
	if g == nil {
		return nil, nil
	}
	policy, err := s.d.timerPolicy(ctx, q, g.RouteID)
	if err != nil {
		return nil, err
	}
	group := g.PublicID
	if reason := skipReason(g, policy); reason != "" {
		return func(ctx context.Context) {
			s.log.Log(ctx, logging.ReminderSkipped, logging.F("group", group), logging.F("reason", reason))
		}, nil
	}
	t := &reminderDue{now: s.clock.Now().UTC(), deadline: deadline.UTC(), policy: policy, orgID: s.orgID,
		reached: s.d.reached}
	var after committed
	if err := s.d.dispatch(ctx, q, g, System, t, &after); err != nil {
		return nil, err
	}
	if t.notice > 0 {
		notice, missed := t.notice, t.missed
		after.add(func(ctx context.Context) {
			s.log.Log(ctx, logging.TimerFired, logging.F("kind", TimerReminder), logging.F("group", group),
				logging.F("notice", notice), logging.F("missed", missed))
		})
	}
	return after.run, nil
}

// skipReason is why a Reminder that came due on g is dropped, empty when it is not.
func skipReason(g *Group, p timerPolicy) string {
	switch {
	case g.Status != StatusAcknowledged:
		return skippedNotAcknowledged
	case !p.reminders:
		return skippedPolicyOff
	}
	return ""
}

// stillOnIt is "Still on it" by the User user, 0 for a Service account (C-17.FR-10, C-04.FR-2): refused, in this
// order, for a Service account, for anyone but the Owner and while no Reminder waits for an answer; it records
// reminder_answered and the answer, and leaves the Reminder schedule as it is.
type stillOnIt struct {
	user int64
	now  time.Time
}

func (t stillOnIt) precondition(g *Group) error {
	switch {
	case t.user == 0:
		return refusal(CodeOwnerMustBeUser)
	case g.Status != StatusAcknowledged || g.OwnerUserID == nil || *g.OwnerUserID != t.user:
		return refusal(CodeNotOwner)
	case !reminderPending(g):
		return refusal(CodeNoReminderPending)
	}
	return nil
}

func (t stillOnIt) apply(_ context.Context, _ Queries, g *Group, c *change) error {
	g.OwnerAnsweredAt = ptr(t.now)
	c.action = ActionReminderAnswered
	c.add(entry{Event: EventReminderAnswered, OwnerUserID: g.OwnerUserID})
	return nil
}

// reminderPending reports whether a Reminder of g waits for its Owner's answer: one was sent and the Owner has not
// answered since the last one.
func reminderPending(g *Group) bool {
	return g.RemindersSent > 0 && g.LastReminderAt != nil &&
		(g.OwnerAnsweredAt == nil || g.OwnerAnsweredAt.Before(*g.LastReminderAt))
}
