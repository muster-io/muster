// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
)

// Form is how a lifecycle event appears in a messenger (the last column of the lifecycle event tables): a new Root
// message, an update of it, a Thread reply, both, or nothing. The Root message follows its Desired state whatever the
// form; a Loud event is always a new message, and an edit is always Quiet (C-11.FR-7).
type Form string

// The messenger forms of the lifecycle events.
const (
	FormPublication    Form = "publication"
	FormUpdate         Form = "update"
	FormReply          Form = "reply"
	FormReplyAndUpdate Form = "reply_and_update"
	FormNothing        Form = "nothing"
)

// The lifecycle events of the timers (C-17.FR-11), which the timers record from S-049 on.
const (
	EventAckTimeout         groups.Event = "ack_timeout"
	EventUnclaimed          groups.Event = "unclaimed"
	EventReminder           groups.Event = "reminder"
	EventReminderAnswered   groups.Event = "reminder_answered"
	EventAutoUnacknowledged groups.Event = "auto_unacknowledged"
	EventNoticesMissed      groups.Event = "notices_missed"
)

// VariantSystem is resolved by the system (C-09.FR-22), whose row differs from a person's Resolve (C-10.FR-15, the
// variant groups.VariantCommand); the groups package records both as one event.
const VariantSystem groups.Variant = "system"

// Row is one row of the lifecycle event tables as delivery reads it: its event and variant, its messenger form, its
// loudness and its symbolic Mentions. EventMentions takes the Mentions the event carries instead: those of the notices
// that notices_missed stands for.
type Row struct {
	Event         groups.Event
	Variant       groups.Variant
	Form          Form
	Loudness      groups.Loudness
	Mentions      []groups.Mention
	EventMentions bool
}

// Table is the closed table of the lifecycle events of C-09.FR-22, C-10.FR-15 and C-17.FR-11 that delivery turns into
// messages (C-11.FR-20), keyed by event and variant: an event whose rows differ by actor or reason has a row per
// variant. moved_to_default_route changes the Destinations of the Alert Group (C-09.FR-19): the final edit in those it
// leaves, a Quiet Publication in those it joins, an update in those of both.
var Table = []Row{
	{groups.EventCreated, groups.VariantAny, FormPublication, groups.Loud, []groups.Mention{groups.MentionNewAlertGroup},
		false},
	{groups.EventAlertsAdded, groups.VariantFiring, FormReply, groups.Loud, []groups.Mention{groups.MentionNewAlerts},
		false},
	{groups.EventAlertsAdded, groups.VariantAcknowledged, FormReplyAndUpdate, groups.Quiet, nil, false},
	{groups.EventAlertsAdded, groups.VariantSnoozed, FormUpdate, groups.Quiet, nil, false},
	{groups.EventAlertReplaced, groups.VariantAny, FormReply, groups.Quiet, nil, false},
	{groups.EventAlertResolved, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventAlertContinued, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventAnnotationsChanged, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventSeverityRaised, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventUrgencyRaised, groups.VariantRemoves, FormReply, groups.Loud,
		[]groups.Mention{groups.MentionOwner, groups.MentionRiseToUrgent}, false},
	{groups.EventUrgencyRaised, groups.VariantRemovesNone, FormUpdate, groups.Quiet, nil, false},
	{groups.EventReopened, groups.VariantFiring, FormReply, groups.Loud, []groups.Mention{groups.MentionReopen}, false},
	{groups.EventReopened, groups.VariantAcknowledged, FormReply, groups.Loud, []groups.Mention{groups.MentionOwner},
		false},
	{groups.EventReopened, groups.VariantSnoozed, FormUpdate, groups.Quiet, nil, false},
	{groups.EventSnoozeEnded, groups.VariantAny, FormReply, groups.Loud, []groups.Mention{groups.MentionSnoozeEnded},
		false},
	{groups.EventResolved, VariantSystem, FormReplyAndUpdate, groups.Quiet, nil, false},
	{groups.EventMovedToDefaultRoute, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventUnacknowledged, groups.VariantOwnerReleased, FormReplyAndUpdate, groups.Loud, nil, false},
	{groups.EventAcknowledged, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventTakeover, groups.VariantAny, FormReply, groups.Loud, []groups.Mention{groups.MentionPreviousOwner},
		false},
	{groups.EventUnacknowledged, groups.VariantCommand, FormUpdate, groups.Quiet, nil, false},
	{groups.EventResolved, groups.VariantCommand, FormUpdate, groups.Quiet, nil, false},
	{groups.EventUnresolved, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventSnoozed, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventUnsnoozed, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventNoteAdded, groups.VariantAny, FormNothing, groups.Quiet, nil, false},
	{EventAckTimeout, groups.VariantAny, FormReply, groups.Loud, []groups.Mention{groups.MentionAckTimeout}, false},
	{EventUnclaimed, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{EventReminder, groups.VariantAny, FormReply, groups.Loud, []groups.Mention{groups.MentionOwner}, false},
	{EventReminderAnswered, groups.VariantAny, FormNothing, groups.Quiet, nil, false},
	{EventAutoUnacknowledged, groups.VariantAny, FormReply, groups.Loud, []groups.Mention{groups.MentionOwner}, false},
	{EventNoticesMissed, groups.VariantAny, FormReply, groups.Loud, nil, true},
}

// EventRow is a row of the Loud/Quiet table of delivery events (reference.md, Loud and Quiet): what delivery
// itself sends to one Destination, apart from the lifecycle events — a new message, an edit, or nothing — with its
// loudness and symbolic Mentions. LoudWhenFiring is the row that is Loud only for an Alert Group firing at that moment.
type EventRow struct {
	Name           string
	Form           Form
	Loudness       groups.Loudness
	Mentions       []groups.Mention
	LoudWhenFiring bool
}

// The names of the rows of DeliveryEvents.
const (
	DeliveryAddedDestination   = "publication_into_added_destination"
	DeliveryStormSummary       = "storm_summary_first_publication"
	DeliveryStormUpdate        = "storm_summary_update"
	DeliveryAfterStorm         = "gradual_publication_after_storm"
	DeliveryLate               = "delivered_late"
	DeliveryAfterRecovery      = "publication_after_recovery"
	DeliveryUpdateRecovery     = "update_after_recovery"
	DeliveryRepliesWhileBroken = "thread_replies_while_broken"
	DeliveryResolvedBroken     = "resolved_while_broken"
	DeliveryRepublication      = "republication_after_deletion"
	DeliveryFinalEdit          = "final_edit"
)

// DeliveryEvents is the closed Loud/Quiet table of delivery events (C-11.FR-20, reference.md): every message delivery
// sends of its own, or decides not to send, reads its loudness and Mentions here.
var DeliveryEvents = []EventRow{
	{DeliveryAddedDestination, FormPublication, groups.Quiet, nil, false},
	{DeliveryStormSummary, FormPublication, groups.Loud, []groups.Mention{groups.MentionNewAlertGroup}, false},
	{DeliveryStormUpdate, FormUpdate, groups.Quiet, nil, false},
	{DeliveryAfterStorm, FormPublication, groups.Quiet, nil, false},
	{DeliveryLate, FormPublication, groups.Quiet, nil, false},
	{DeliveryAfterRecovery, FormPublication, groups.Loud, []groups.Mention{groups.MentionNewAlertGroup}, true},
	{DeliveryUpdateRecovery, FormUpdate, groups.Quiet, nil, false},
	{DeliveryRepliesWhileBroken, FormNothing, groups.Quiet, nil, false},
	{DeliveryResolvedBroken, FormNothing, groups.Quiet, nil, false},
	{DeliveryRepublication, FormPublication, groups.Quiet, nil, false},
	{DeliveryFinalEdit, FormUpdate, groups.Quiet, nil, false},
}

// deliveryEvent is the row name of DeliveryEvents; a name outside the table is a programming error.
func deliveryEvent(name string) EventRow {
	for _, r := range DeliveryEvents {
		if r.Name == name {
			return r
		}
	}
	panic("delivery: no row " + name + " in the table of delivery events")
}

// loud says whether the row is Loud for an Alert Group that is firing or not.
func (r EventRow) loud(firing bool) bool {
	return r.Loudness == groups.Loud && (!r.LoudWhenFiring || firing)
}

// rowOf is the row of a recorded event made by actor; false for an event outside the table.
func rowOf(e groups.Recorded, actor groups.Actor) (Row, bool) {
	v := e.Variant
	if e.Event == groups.EventResolved {
		v = groups.VariantCommand
		if actor.Kind == audit.ActorSystem {
			v = VariantSystem
		}
	}
	for _, r := range Table {
		if r.Event == e.Event && r.Variant == v {
			return r, true
		}
	}
	return Row{}, false
}

// replies says whether the form queues a Thread reply.
func (f Form) replies() bool { return f == FormReply || f == FormReplyAndUpdate }

// mentionsOf is the symbolic Mentions a row gives a recorded event.
func (r Row) mentionsOf(e groups.Recorded) []string {
	ms := r.Mentions
	if r.EventMentions {
		ms = e.Mentions
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, string(m))
	}
	return out
}

// Enqueue is the re-render step of the dispatcher (C-11.FR-1, FR-20; groups.Rerender), in the dispatcher's
// transaction tx. It renders the Root message once per markup of the Route's Destinations, reading through tx, and
// reports each Route template that failed, whose message is the Fallback template, through r.Fallback (C-12.FR-6).
// For each Destination of the Alert Group's Route that is not deleted it creates the delivery when it is missing and,
// when what its Root message shows changed, stores it as the next Desired state, due now; the
// lifecycle events whose form is a Thread reply queue one, new Alerts within the Thread batching window, or drop it
// while the Destination is Broken, a Storm holds the delivery or it ended. A delivery created by `created` is a Loud
// Publication unless a Storm of the Route holds it (C-11.FR-6); the first Publication of an Alert Group that resolves
// is settled by the rules of late Publications and Broken Destinations; an Alert Group moved to the Default route gets
// the final edit in the Destinations it leaves and a Quiet Publication in those it joins (C-09.FR-19). An outgoing
// webhook in the events mode has no Root message: each lifecycle event is queued for it as one event (C-15.FR-2),
// whether it is Broken or a Storm holds the Alert Group. It wakes the delivery workers once tx commits.
//
// It first takes the Route's membership lock shared (ShareRouteMembership), which a change of the Route's Destinations
// and the deletion of one of them take exclusively, so that an Enqueue and such a change serialize: neither misses a
// delivery the other creates or retires. The lock order cannot deadlock: grouping holds the Route's row FOR SHARE and
// takes this lock after it, and a change of the Destinations takes the row before this lock, so it waits for grouping
// before it takes the lock; a Command or a timer holds only its Alert Group, which a change of the Destinations never
// locks; the move to the Default route holds the Route's row and then the Alert Groups, but never this lock, which an
// Enqueue takes without the row. While it waits nobody waits for a delivery row it holds: an Enqueue locks the
// deliveries of its Route only after the lock, and a change of the Destinations locks those of its Route only after it.
func (s *Service) Enqueue(ctx context.Context, tx groups.DBTX, r groups.Rendering) error {
	q := s.store.queries(tx)
	g := r.Group
	if err := q.ShareRouteMembership(ctx, dbgen.ShareRouteMembershipParams{LockClass: db.RouteMembershipLockClass,
		RouteID: g.RouteID}); err != nil {
		return fmt.Errorf("lock the destinations of the route of alert group #%d: %w", g.Number, err)
	}
	later := r.After
	if later == nil {
		later = func(f func(ctx context.Context)) { f(ctx) }
	}
	var replies []reply
	created, moved := false, false
	for _, e := range r.Events {
		row, ok := rowOf(e, r.Actor)
		if !ok {
			return fmt.Errorf("alert group #%d: the lifecycle event %s (%s) has no row in the delivery table", g.Number,
				e.Event, e.Variant)
		}
		created = created || row.Form == FormPublication
		moved = moved || e.Event == groups.EventMovedToDefaultRoute
		if row.Form.replies() {
			replies = append(replies, reply{event: e, row: row})
		}
	}
	all, err := q.ListRouteDestinations(ctx, dbgen.ListRouteDestinationsParams{OrgID: s.orgID, RouteID: g.RouteID})
	if err != nil {
		return fmt.Errorf("list the destinations of alert group #%d: %w", g.Number, err)
	}
	dests, eventDests := splitDestinations(all)
	now := s.clock.Now().UTC()
	left := 0
	if moved {
		if left, err = s.leave(ctx, q, g, all, now); err != nil {
			return err
		}
	}
	if len(all) == 0 && !created {
		return s.wake(ctx, q, left > 0)
	}
	route, err := q.GetRouteDelivery(ctx, dbgen.GetRouteDeliveryParams{OrgID: s.orgID, ID: g.RouteID})
	if err != nil {
		return fmt.Errorf("read the route of alert group #%d: %w", g.Number, err)
	}
	var st *storm
	if created {
		if st, err = s.joinStorm(ctx, q, g, route, now, later); err != nil {
			return err
		}
	}
	if err := s.queueEvents(ctx, tx, q, r, eventDests, route, now); err != nil {
		return err
	}
	if len(dests) == 0 {
		return s.wake(ctx, q, left > 0 || len(eventDests) > 0)
	}
	window := time.Duration(route.ThreadBatchingWindowSeconds) * time.Second
	types := make([]string, len(dests))
	for i, d := range dests {
		types[i] = d.Type
	}
	roots, err := s.renderer.Roots(ctx, tx, viewOf(g), markupsOf(types))
	if err != nil {
		return fmt.Errorf("render alert group #%d: %w", g.Number, err)
	}
	if roots.After != nil {
		later(roots.After)
	}
	for _, f := range roots.Failures {
		if r.Fallback != nil {
			r.Fallback(groups.TemplateFailure{Template: f.Template, Detail: f.Detail()})
		}
	}
	how := enqueueing{created: created, moved: moved, replies: replies, now: now, window: window, roots: roots, tx: tx}
	if st != nil && !g.Urgent {
		how.heldBy = &st.id
	}
	for _, d := range dests {
		dst := Destination{ID: d.ID, PublicID: d.PublicID, Name: d.Name, Type: d.Type, Connection: int8Of(d.ConnectionID)}
		h := how
		if sendsRequests(d.Type) && !d.WebhookReplies {
			h.replies = nil // an outgoing webhook without a "reply in thread" request has no Thread
		}
		if err := s.enqueue(ctx, q, r, dst, d.Health == healthBroken, h); err != nil {
			return err
		}
	}
	if st != nil {
		if err := s.renderSummaries(ctx, q, st, stormRoute{publicID: route.PublicID, name: route.Name,
			language: route.Language}, dests, now); err != nil {
			return err
		}
	}
	return s.wake(ctx, q, true)
}

// wake wakes the delivery workers once the transaction commits, when changed.
func (s *Service) wake(ctx context.Context, q queries, changed bool) error {
	if !changed {
		return nil
	}
	if err := q.NotifyDelivery(ctx, Channel); err != nil {
		return fmt.Errorf("wake the delivery workers: %w", err)
	}
	return nil
}

// reply is a lifecycle event of a rendering whose form queues a Thread reply.
type reply struct {
	event groups.Recorded
	row   Row
}

// enqueueing is how a rendering reaches each Destination: its Root messages by markup, whether the Alert Group was
// created or moved, the Storm that holds a new one, its Thread replies, the time, the Thread batching window and the
// transaction of the change, which the request of an outgoing webhook is rendered through.
type enqueueing struct {
	roots          Roots
	created, moved bool
	heldBy         *int64
	replies        []reply
	now            time.Time
	window         time.Duration
	tx             dbgen.DBTX
}

// enqueue sets the Desired state of the Alert Group in one Destination and queues its Thread replies; while the
// Destination is Broken, while a Storm holds the delivery, or once the delivery ended, they are created dropped
// (C-11.FR-6, FR-13, FR-19). An Urgent Alert Group that a Storm holds is released at once.
func (s *Service) enqueue(ctx context.Context, q queries, r groups.Rendering, d Destination, broken bool,
	how enqueueing) error {
	g, now := r.Group, how.now
	p := dbgen.EnsureDeliveryParams{OrgID: s.orgID, DestinationID: d.ID,
		AlertGroupID: pgtype.Int8{Int64: g.ID, Valid: true}, HeldByStormID: nullInt(how.heldBy), Urgent: g.Urgent,
		Now: now}
	switch {
	case how.created:
		p.PublicationLoud = pgtype.Bool{Bool: true, Valid: true}
	case how.moved:
		p.PublicationLoud = pgtype.Bool{Bool: deliveryEvent(DeliveryAddedDestination).loud(false), Valid: true}
	}
	row, err := q.EnsureDelivery(ctx, p)
	if err != nil {
		return fmt.Errorf("create the delivery of alert group #%d to %s: %w", g.Number, d.PublicID, err)
	}
	state := row.State
	if how.moved && !row.Inserted {
		n, err := q.RejoinDelivery(ctx, dbgen.RejoinDeliveryParams{OrgID: s.orgID, AlertGroupID: g.ID,
			DestinationID: d.ID, Loud: deliveryEvent(DeliveryAddedDestination).loud(false), Now: now})
		if err != nil {
			return fmt.Errorf("publish alert group #%d in %s again: %w", g.Number, d.PublicID, err)
		}
		if n > 0 {
			state = statePending
		}
	}
	held := row.HeldByStormID.Valid
	if held && g.Urgent {
		if err := q.ReleaseHeld(ctx, dbgen.ReleaseHeldParams{OrgID: s.orgID, ID: row.ID, Now: now}); err != nil {
			return fmt.Errorf("release the urgent alert group #%d from its storm: %w", g.Number, err)
		}
		held = false
	}
	msg, err := s.desiredOf(ctx, how.tx, d, how.roots)
	if err != nil {
		return fmt.Errorf("render the request of alert group #%d in %s: %w", g.Number, d.PublicID, err)
	}
	if !bytes.Equal(row.DesiredHash, msg.hash) {
		var received pgtype.Timestamptz
		if r.ReceivedAt != nil {
			received = pgtype.Timestamptz{Time: r.ReceivedAt.UTC(), Valid: true}
		}
		if state, err = q.SetDesired(ctx, dbgen.SetDesiredParams{OrgID: s.orgID, ID: row.ID, DesiredText: msg.text,
			DesiredPayload: msg.payload, DesiredHash: msg.hash, ButtonKeyID: nonEmpty(msg.keyID), ReceivedAt: received,
			Open:   g.Status != groups.StatusResolved,
			Firing: deliveryEvent(DeliveryAfterRecovery).loud(g.Status == groups.StatusFiring), Urgent: g.Urgent,
			Now: now}); err != nil {
			return fmt.Errorf("set the desired state of alert group #%d in %s: %w", g.Number, d.PublicID, err)
		}
	} else if err := q.SetDeliveryUrgent(ctx, dbgen.SetDeliveryUrgentParams{OrgID: s.orgID, ID: row.ID,
		Urgent: g.Urgent, Now: now}); err != nil {
		return fmt.Errorf("set the urgency of alert group #%d in %s: %w", g.Number, d.PublicID, err)
	}
	if g.Status == groups.StatusResolved {
		if err := s.settleResolved(ctx, q, row.ID, now); err != nil {
			return fmt.Errorf("settle the publication of alert group #%d in %s: %w", g.Number, d.PublicID, err)
		}
	}
	if broken || held || ended(state) {
		for _, rp := range how.replies {
			if err := q.InsertThreadReply(ctx, s.replyParams(row.ID, g.ID, d, rp, replyDropped, now)); err != nil {
				return fmt.Errorf("drop the %s reply of alert group #%d in %s: %w", rp.event.Event, g.Number,
					d.PublicID, err)
			}
		}
		return nil
	}
	until := timeOf(row.ThreadBatchUntil)
	for _, rp := range how.replies {
		var err error
		if until, err = s.queueReply(ctx, q, row.ID, g.ID, d, rp, until, now, how.window); err != nil {
			return fmt.Errorf("queue the %s reply of alert group #%d in %s: %w", rp.event.Event, g.Number, d.PublicID,
				err)
		}
	}
	return nil
}

// ended reports whether a delivery is in a state that a change of the Desired state does not revive: withheld,
// deleted in the messenger or retired.
func ended(state string) bool {
	return state == "withheld" || state == "deleted_in_messenger" || state == "retired"
}

// statePending is a delivery with work to do.
const statePending = "pending"

// The states a Thread reply is queued in.
const (
	replyPending = "pending"
	replyDropped = "dropped"
)

// replyParams queue the Thread reply of one lifecycle event in a state, due now.
func (s *Service) replyParams(deliveryID, groupID int64, d Destination, rp reply, state string,
	now time.Time) dbgen.InsertThreadReplyParams {
	fingerprints := rp.event.Fingerprints
	if fingerprints == nil {
		fingerprints = []string{}
	}
	return dbgen.InsertThreadReplyParams{OrgID: s.orgID, DeliveryID: deliveryID, AlertGroupID: groupID,
		DestinationID: d.ID, Event: string(rp.event.Event), EventSeqs: []int64{rp.event.Seq},
		Loudness: string(rp.row.Loudness), Mentions: rp.row.mentionsOf(rp.event), Fingerprints: fingerprints,
		State: state, Due: now, Now: now}
}

// queueReply queues the Thread reply of one lifecycle event and returns the end of the Thread batching window
// (C-11.FR-4). New Alerts with no open window are replied to at once and open one; within the window they join the
// delivery's one collecting batch, which is due when the window ends, and a new batch moves the end of the window to
// its due time plus the window, so that Alerts that keep arriving make one reply per window. Other events are replied
// to at once.
func (s *Service) queueReply(ctx context.Context, q queries, deliveryID, groupID int64, d Destination, rp reply,
	until *time.Time, now time.Time, window time.Duration) (*time.Time, error) {
	e := rp.event
	p := s.replyParams(deliveryID, groupID, d, rp, replyPending, now)
	if e.Event != groups.EventAlertsAdded {
		return until, q.InsertThreadReply(ctx, p)
	}
	if until == nil || !until.After(now) {
		end := now.Add(window)
		if err := q.InsertThreadReply(ctx, p); err != nil {
			return nil, err
		}
		return &end, s.setBatchUntil(ctx, q, deliveryID, end, now)
	}
	inserted, err := q.CollectAlerts(ctx, dbgen.CollectAlertsParams{OrgID: s.orgID, DeliveryID: deliveryID,
		AlertGroupID: groupID, DestinationID: d.ID, EventSeqs: p.EventSeqs, Loudness: p.Loudness,
		Mentions: p.Mentions, Fingerprints: p.Fingerprints, Due: *until, Now: now})
	if err != nil || !inserted {
		return until, err
	}
	end := until.Add(window)
	return &end, s.setBatchUntil(ctx, q, deliveryID, end, now)
}

func (s *Service) setBatchUntil(ctx context.Context, q queries, deliveryID int64, until, now time.Time) error {
	return q.SetThreadBatchUntil(ctx, dbgen.SetThreadBatchUntilParams{OrgID: s.orgID, ID: deliveryID, Until: until,
		Now: now})
}
