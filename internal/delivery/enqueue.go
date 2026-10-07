// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
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
// variant. moved_to_default_route is S-035's: until then delivery leaves the Alert Group where it was.
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
// transaction tx. For each Destination of the Alert Group's Route that is not deleted it creates the delivery when it is
// missing, renders the Root message and, when what it shows changed, stores it as the next Desired state, due now; the
// lifecycle events whose form is a Thread reply queue one, new Alerts within the Thread batching window. A delivery
// created by `created` is a Loud Publication. It wakes the delivery workers once tx commits.
func (s *Service) Enqueue(ctx context.Context, tx groups.DBTX, r groups.Rendering) error {
	if slices.ContainsFunc(r.Events, func(e groups.Recorded) bool { return e.Event == groups.EventMovedToDefaultRoute }) {
		return nil
	}
	q := s.store.queries(tx)
	g := r.Group
	dests, err := q.ListRouteDestinations(ctx, dbgen.ListRouteDestinationsParams{OrgID: s.orgID, RouteID: g.RouteID})
	if err != nil {
		return fmt.Errorf("list the destinations of alert group #%d: %w", g.Number, err)
	}
	if len(dests) == 0 {
		return nil
	}
	route, err := q.GetRouteDelivery(ctx, dbgen.GetRouteDeliveryParams{OrgID: s.orgID, ID: g.RouteID})
	if err != nil {
		return fmt.Errorf("read the route of alert group #%d: %w", g.Number, err)
	}
	now := s.clock.Now().UTC()
	var replies []reply
	created := false
	for _, e := range r.Events {
		row, ok := rowOf(e, r.Actor)
		if !ok {
			return fmt.Errorf("alert group #%d: the lifecycle event %s (%s) has no row in the delivery table", g.Number,
				e.Event, e.Variant)
		}
		created = created || row.Form == FormPublication
		if row.Form.replies() {
			replies = append(replies, reply{event: e, row: row})
		}
	}
	window := time.Duration(route.ThreadBatchingWindowSeconds) * time.Second
	for _, d := range dests {
		dst := Destination{ID: d.ID, PublicID: d.PublicID, Name: d.Name, Type: d.Type, Connection: int8Of(d.ConnectionID)}
		if err := s.enqueue(ctx, q, r, dst, route.Language, created, replies, now, window); err != nil {
			return err
		}
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

// enqueue sets the Desired state of the Alert Group in one Destination and queues its Thread replies.
func (s *Service) enqueue(ctx context.Context, q queries, r groups.Rendering, d Destination, language string,
	created bool, replies []reply, now time.Time, window time.Duration) error {
	g := r.Group
	p := dbgen.EnsureDeliveryParams{OrgID: s.orgID, DestinationID: d.ID,
		AlertGroupID: pgtype.Int8{Int64: g.ID, Valid: true}, Urgent: g.Urgent, Now: now}
	if created {
		p.PublicationLoud = pgtype.Bool{Bool: true, Valid: true}
	}
	row, err := q.EnsureDelivery(ctx, p)
	if err != nil {
		return fmt.Errorf("create the delivery of alert group #%d to %s: %w", g.Number, d.PublicID, err)
	}
	msg := encode(s.renderer.Render(viewOf(g), d, language))
	if !bytes.Equal(row.DesiredHash, msg.hash) {
		var received pgtype.Timestamptz
		if r.ReceivedAt != nil {
			received = pgtype.Timestamptz{Time: r.ReceivedAt.UTC(), Valid: true}
		}
		if err := q.SetDesired(ctx, dbgen.SetDesiredParams{OrgID: s.orgID, ID: row.ID, DesiredText: msg.text,
			DesiredPayload: msg.payload, DesiredHash: msg.hash, ReceivedAt: received, Urgent: g.Urgent,
			Now: now}); err != nil {
			return fmt.Errorf("set the desired state of alert group #%d in %s: %w", g.Number, d.PublicID, err)
		}
	} else if err := q.SetDeliveryUrgent(ctx, dbgen.SetDeliveryUrgentParams{OrgID: s.orgID, ID: row.ID,
		Urgent: g.Urgent, Now: now}); err != nil {
		return fmt.Errorf("set the urgency of alert group #%d in %s: %w", g.Number, d.PublicID, err)
	}
	until := timeOf(row.ThreadBatchUntil)
	for _, rp := range replies {
		var err error
		if until, err = s.queueReply(ctx, q, row.ID, g.ID, d, rp, until, now, window); err != nil {
			return fmt.Errorf("queue the %s reply of alert group #%d in %s: %w", rp.event.Event, g.Number, d.PublicID,
				err)
		}
	}
	return nil
}

// queueReply queues the Thread reply of one lifecycle event and returns the end of the Thread batching window
// (C-11.FR-4). New Alerts with no open window are replied to at once and open one; within the window they join the
// delivery's one collecting batch, which is due when the window ends, and a new batch moves the end of the window to
// its due time plus the window, so that Alerts that keep arriving make one reply per window. Other events are replied
// to at once.
func (s *Service) queueReply(ctx context.Context, q queries, deliveryID, groupID int64, d Destination, rp reply,
	until *time.Time, now time.Time, window time.Duration) (*time.Time, error) {
	e := rp.event
	fingerprints := e.Fingerprints
	if fingerprints == nil {
		fingerprints = []string{}
	}
	mentions := rp.row.mentionsOf(e)
	if e.Event != groups.EventAlertsAdded {
		return until, q.InsertThreadReply(ctx, dbgen.InsertThreadReplyParams{OrgID: s.orgID, DeliveryID: deliveryID,
			AlertGroupID: groupID, DestinationID: d.ID, Event: string(e.Event), EventSeqs: []int64{e.Seq},
			Loudness: string(rp.row.Loudness), Mentions: mentions, Fingerprints: fingerprints, Due: now, Now: now})
	}
	if until == nil || !until.After(now) {
		end := now.Add(window)
		if err := q.InsertThreadReply(ctx, dbgen.InsertThreadReplyParams{OrgID: s.orgID, DeliveryID: deliveryID,
			AlertGroupID: groupID, DestinationID: d.ID, Event: string(e.Event), EventSeqs: []int64{e.Seq},
			Loudness: string(rp.row.Loudness), Mentions: mentions, Fingerprints: fingerprints, Due: now,
			Now: now}); err != nil {
			return nil, err
		}
		return &end, s.setBatchUntil(ctx, q, deliveryID, end, now)
	}
	inserted, err := q.CollectAlerts(ctx, dbgen.CollectAlertsParams{OrgID: s.orgID, DeliveryID: deliveryID,
		AlertGroupID: groupID, DestinationID: d.ID, EventSeqs: []int64{e.Seq}, Loudness: string(rp.row.Loudness),
		Mentions: mentions, Fingerprints: fingerprints, Due: *until, Now: now})
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
