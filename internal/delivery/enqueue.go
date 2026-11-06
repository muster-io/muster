// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/routing"
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

// Table is the closed table of the lifecycle events of C-09.FR-22, C-10.FR-15, C-17.FR-11, C-25 and C-26 that delivery
// turns into messages (C-11.FR-20), keyed by event and variant: an event whose rows differ by actor or reason has a row
// per variant. moved_to_default_route changes the Destinations of the Alert Group (C-09.FR-19): the final edit in those
// it leaves, a Quiet Publication in those it joins, an update in those of both. destination_joined is a new Root message
// in the Destination it names only (C-26.FR-5, C-11.FR-20 revised); the others see an update.
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
	{groups.EventTeamJoined, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
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
	{groups.EventUnknownTeamValue, groups.VariantAny, FormNothing, groups.Quiet, nil, false},
	{groups.EventDestinationJoined, groups.VariantFiring, FormPublication, groups.Loud,
		[]groups.Mention{groups.MentionNewAlertGroup}, false},
	{groups.EventDestinationJoined, groups.VariantAcknowledged, FormPublication, groups.Quiet, nil, false},
	{groups.EventDestinationJoined, groups.VariantSnoozed, FormPublication, groups.Quiet, nil, false},
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
	{groups.EventAckTimeout, groups.VariantAny, FormReply, groups.Loud, []groups.Mention{groups.MentionAckTimeout},
		false},
	{groups.EventUnclaimed, groups.VariantAny, FormUpdate, groups.Quiet, nil, false},
	{groups.EventReminder, groups.VariantAny, FormReply, groups.Loud, []groups.Mention{groups.MentionOwner}, false},
	{groups.EventReminderAnswered, groups.VariantAny, FormNothing, groups.Quiet, nil, false},
	{groups.EventAutoUnacknowledged, groups.VariantAny, FormReply, groups.Loud,
		[]groups.Mention{groups.MentionOwner}, false},
	{groups.EventNoticesMissed, groups.VariantAny, FormReply, groups.Loud, nil, true},
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
	DeliveryPastEditWindow     = "republication_past_edit_window"
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
	{DeliveryPastEditWindow, FormPublication, groups.Quiet, nil, false},
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

// Enqueue is the re-render step of the dispatcher (C-11.FR-1, FR-20; groups.Rerender), in the dispatcher's transaction
// tx. It renders the Root message once per markup of the Destinations the Alert Group reaches, reading through tx, and
// reports each Route template that failed, whose message is the Fallback template, through r.Fallback (C-12.FR-6). The
// Destinations are those of its reach — its Route's entries whose Severity condition its level meets and, with "Also to
// the Teams from the label", those of its Teams (routing.Reach, C-26.FR-2) — and those it reached before, which keep
// their deliveries and are kept current until their final state (C-26.FR-4). For each it creates the delivery when it
// is missing, with the entries that reach it appended (C-26.FR-3), and, when what its Root message shows changed,
// stores it as the next Desired state, due now; the lifecycle events whose form is a Thread reply queue one, new Alerts
// within the Thread batching window, or drop it while the Destination is Broken, a Storm holds the delivery or it
// ended. A delivery created by `created` is a Loud Publication unless a Storm of the Route holds it (C-11.FR-6), and so
// is one a Team or a rise adds before the first Publication, as part of it; one created by destination_joined is the
// Publication of its row, Loud while the Alert Group fires, held during a Storm of the Route unless the Alert Group is
// Urgent (C-26.FR-5); a missing delivery of a change of configuration, such as a raised Minimum severity or a Team
// evaluated on a save (C-26.FR-9, C-25.FR-13), is a Quiet Publication, held where a Storm holds the Alert Group; none
// has the Thread replies of its change. Only the causes by which the Destinations grow create deliveries (grows): a
// fall of the level, a resolution or a Command updates those the Alert Group has. The first Publication of an Alert
// Group that resolves is settled by the rules of late Publications and Broken Destinations; an Alert Group moved to the
// Default route gets the final edit in the Destinations it leaves and a Quiet Publication in those it joins
// (C-09.FR-19). An outgoing webhook in the events mode has no Root message: each lifecycle event is queued for it as
// one event (C-15.FR-2), whether it is Broken or a Storm holds the Alert Group. It wakes the delivery workers once tx
// commits.
//
// It first takes the Route's membership lock shared (ShareRouteMembership), which a change of the Route's Destinations
// and the deletion of one of them take exclusively, so that an Enqueue and such a change serialize: neither misses a
// delivery the other creates or retires. The dispatcher took the same lock before it read the reach it recorded joined
// Destinations from, so the reach applied here is that one. The membership lock closes no cycle: grouping holds the
// Route's row FOR SHARE and takes this lock after it, and a change of the Destinations takes the row before this lock,
// so it waits for grouping before it takes the lock; a Command or a timer holds only its Alert Group, FOR NO KEY
// UPDATE, which a change of the Destinations never locks in a mode that waits for it — the foreign-key checks of its
// deliveries take FOR KEY SHARE; the move to the Default route holds the Route's row and then the Alert Groups, but
// never this lock, which an Enqueue takes without the row. While it waits nobody waits for a delivery row it holds: an
// Enqueue locks the deliveries of its Route only after the lock, and a change of the Destinations locks those of its
// Route only after it. When the Route delivers to the Teams, it then takes the membership lock of each Team of the
// Alert Group shared, by Team id (ShareTeamMembership), which a change of the Team's Destination entries, its deletion
// and the deletion of a Destination it names take exclusively, before it reads the Teams' entries: an Enqueue and such
// a change serialize as they do for the Route, and the change takes none of the rows an Enqueue holds before it takes
// the lock (lock order of worker.go). Then it takes the Route's Storm, before any delivery: the creation joins or
// starts it, any other rendering locks the active one FOR SHARE, as a change of the Route's Destinations does. The
// deliveries and Thread replies it then locks follow the lock order of worker.go.
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
		created = created || e.Event == groups.EventCreated
		moved = moved || e.Event == groups.EventMovedToDefaultRoute
		if row.Form.replies() {
			replies = append(replies, reply{event: e, row: row})
		}
	}
	route, err := q.GetRouteDelivery(ctx, dbgen.GetRouteDeliveryParams{OrgID: s.orgID, ID: g.RouteID})
	if err != nil {
		return fmt.Errorf("read the route of alert group #%d: %w", g.Number, err)
	}
	if route.DeliverToTeams {
		if err := s.shareTeams(ctx, q, r.Teams); err != nil {
			return fmt.Errorf("alert group #%d: %w", g.Number, err)
		}
	}
	reached, err := s.reachOf(ctx, q, r, route.DeliverToTeams)
	if err != nil {
		return err
	}
	if reached, err = s.withJoined(ctx, q, r, reached); err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	// The Route's Storm comes before every delivery the rendering locks (worker.go): the creation joins or starts it,
	// any other rendering takes the active one FOR SHARE.
	var st *storm
	if created {
		st, err = s.joinStorm(ctx, q, g, route, now, later)
	} else {
		st, err = s.activeStorm(ctx, q, g.RouteID)
	}
	if err != nil {
		return err
	}
	left := 0
	if moved {
		rows := make([]dbgen.ListRouteDestinationsRow, len(reached))
		for i, t := range reached {
			rows[i] = t.dest
		}
		if left, err = s.leave(ctx, q, g, rows, now); err != nil {
			return err
		}
	}
	had, err := q.ListGroupDestinations(ctx, dbgen.ListGroupDestinationsParams{OrgID: s.orgID, AlertGroupID: g.ID})
	if err != nil {
		return fmt.Errorf("list the destinations alert group #%d reached: %w", g.Number, err)
	}
	// An Alert Group that has no delivery anywhere yet is published wherever it reaches, as its first Publication.
	// The whole reach is published by a creation, a move, a first Publication and a rise; a Team that joined adds its
	// own entries, destination_joined its Destination and a change of a Team's entries the Destination it touched.
	adds := growth{joined: r.Joined(), teams: joinedTeams(r), grows: r.Grows}
	targets := withAdded(withReached(reached, had, created || moved || len(had) == 0 || grows(r)), reached, adds)
	if len(targets) == 0 && !created {
		return s.wake(ctx, q, left > 0)
	}
	var dests []target
	var eventDests []dbgen.ListRouteDestinationsRow
	for _, t := range targets {
		if sendsEvents(t.dest) {
			eventDests = append(eventDests, t.dest)
		}
		if hasRoot(t.dest) {
			dests = append(dests, t)
		}
	}
	if err := s.queueEvents(ctx, tx, q, r, eventDests, route, now); err != nil {
		return err
	}
	if len(dests) == 0 {
		return s.wake(ctx, q, left > 0 || len(eventDests) > 0)
	}
	joined := r.Joined()
	window := time.Duration(route.ThreadBatchingWindowSeconds) * time.Second
	types := make([]string, len(dests))
	for i, t := range dests {
		types[i] = t.dest.Type
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
	held := heldBy(had)
	var summaries []stormDest
	for _, t := range dests {
		d := t.dest
		dst := Destination{ID: d.ID, PublicID: d.PublicID, Name: d.Name, Type: d.Type, Connection: int8Of(d.ConnectionID),
			Buttons: buttonSetOf(d.Buttons, d.OrganizationButtons)}
		h := how
		if sendsRequests(d.Type) && !d.WebhookReplies {
			h.replies = nil // an outgoing webhook without a "reply in thread" request has no Thread
		}
		if h.entries, err = json.Marshal(t.entries); err != nil {
			return fmt.Errorf("encode the entries of alert group #%d in %s: %w", g.Number, d.PublicID, err)
		}
		// A Destination the rendering adds again, whose delivery got its final edit or was withheld, publishes there
		// afresh: Loud or Quiet as its destination_joined, Quiet for a change of configuration (C-26.FR-4, FR-5). One
		// whose final edit still waits keeps its Root message, current again, and the final edit is not made: the
		// dispatcher counts it as delivered and records no destination_joined for it.
		h.rejoin = !moved && gone(t.had) && len(t.entries) > 0 && (grows(r) || adds.adds(t))
		h.loud, h.heldBy = publicationOf(r, g, created, moved, joined[d.ID], st, held)
		if h.heldBy != nil && (created || t.had == nil || (h.rejoin && gone(t.had))) {
			summaries = append(summaries, stormDest{id: d.ID, publicID: d.PublicID, typ: d.Type})
		}
		if err := s.enqueue(ctx, q, r, dst, d.Health == healthBroken, h); err != nil {
			return err
		}
	}
	if st != nil && created {
		if summaries, err = s.withSummaries(ctx, q, st, dests); err != nil {
			return err
		}
	}
	if st != nil && len(summaries) > 0 {
		if err := s.renderSummaries(ctx, q, st, stormRoute{publicID: route.PublicID, name: route.Name,
			language: route.Language}, summaries, now); err != nil {
			return err
		}
	}
	return s.wake(ctx, q, true)
}

// reachQueries are the queries of the reach of an Alert Group (C-26.FR-2 to FR-4).
type reachQueries interface {
	ListTeamDestinations(ctx context.Context, arg dbgen.ListTeamDestinationsParams) (
		[]dbgen.ListTeamDestinationsRow, error)
	ListGroupDestinations(ctx context.Context, arg dbgen.ListGroupDestinationsParams) (
		[]dbgen.ListGroupDestinationsRow, error)
	ListEntryTeams(ctx context.Context, arg dbgen.ListEntryTeamsParams) ([]dbgen.ListEntryTeamsRow, error)
	ListDestinationsByID(ctx context.Context, arg dbgen.ListDestinationsByIDParams) (
		[]dbgen.ListDestinationsByIDRow, error)
}

// target is a Destination of a rendering: its row, the entries that reach it now — none for a Destination the Alert
// Group reached before and no longer reaches — and the delivery it had, nil when it had none.
type target struct {
	dest    dbgen.ListRouteDestinationsRow
	entries []routing.Reaching
	had     *dbgen.ListGroupDestinationsRow
}

// reachOf is the reach of the rendered Alert Group (C-26.FR-2, FR-3): the entries of its Route and, with toTeams, of
// its Teams, given by the dispatcher, whose Severity condition its level meets, by Destination in id order.
func (s *Service) reachOf(ctx context.Context, q queries, r groups.Rendering, toTeams bool) ([]target, error) {
	g := r.Group
	rows, err := q.ListRouteDestinations(ctx, dbgen.ListRouteDestinationsParams{OrgID: s.orgID, RouteID: g.RouteID})
	if err != nil {
		return nil, fmt.Errorf("list the destinations of alert group #%d: %w", g.Number, err)
	}
	byID := make(map[int64]dbgen.ListRouteDestinationsRow, len(rows))
	route := make([]routing.Entry, len(rows))
	for i, d := range rows {
		byID[d.ID] = d
		route[i] = routing.Entry{DestinationID: d.ID, Condition: routing.SeverityCondition(d.SeverityCondition)}
	}
	var teams []routing.TeamEntries
	if toTeams && len(r.Teams) > 0 {
		trs, err := q.ListTeamDestinations(ctx, dbgen.ListTeamDestinationsParams{OrgID: s.orgID, TeamIds: r.Teams})
		if err != nil {
			return nil, fmt.Errorf("list the destinations of the teams of alert group #%d: %w", g.Number, err)
		}
		by := map[int64][]routing.Entry{}
		for _, t := range trs {
			if _, ok := byID[t.ID]; !ok {
				byID[t.ID] = dbgen.ListRouteDestinationsRow{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Type: t.Type,
					ConnectionID: t.ConnectionID, Health: t.Health, WebhookMode: t.WebhookMode,
					WebhookReplies: t.WebhookReplies, Buttons: t.Buttons, OrganizationButtons: t.OrganizationButtons}
			}
			by[t.TeamID] = append(by[t.TeamID], routing.Entry{DestinationID: t.ID,
				Condition: routing.SeverityCondition(t.SeverityCondition)})
		}
		for id, es := range by {
			teams = append(teams, routing.TeamEntries{TeamID: id, Entries: es})
		}
	}
	reached := routing.Reach(string(g.Severity), route, toTeams, teams)
	out := make([]target, len(reached))
	for i, rc := range reached {
		out[i] = target{dest: byID[rc.DestinationID], entries: rc.Entries}
	}
	return out, nil
}

// withJoined adds to the reach the Destinations the dispatcher recorded destination_joined for that the reach read
// here no longer has, with the entries the dispatcher read: a destination_joined must not stay in the Timeline and the
// outgoing webhook events without its Root message. Both reads are under the membership locks of the Route and its
// Teams, which no change of their Destinations commits between, so this is only a guard. A Destination deleted
// meanwhile is left out.
func (s *Service) withJoined(ctx context.Context, q queries, r groups.Rendering, reached []target) ([]target, error) {
	var missing []int64
	entries := map[int64][]routing.Reaching{}
	for id, e := range r.Joined() {
		if !slices.ContainsFunc(reached, func(t target) bool { return t.dest.ID == id }) {
			missing = append(missing, id)
			entries[id] = e.Destination.Entries
		}
	}
	if len(missing) == 0 {
		return reached, nil
	}
	slices.Sort(missing)
	rows, err := q.ListDestinationsByID(ctx, dbgen.ListDestinationsByIDParams{OrgID: s.orgID, Ids: missing})
	if err != nil {
		return nil, fmt.Errorf("read the destinations alert group #%d joined: %w", r.Group.Number, err)
	}
	for _, d := range rows {
		reached = append(reached, target{dest: dbgen.ListRouteDestinationsRow{ID: d.ID, PublicID: d.PublicID,
			Name: d.Name, Type: d.Type, ConnectionID: d.ConnectionID, Health: d.Health, WebhookMode: d.WebhookMode,
			WebhookReplies: d.WebhookReplies, Buttons: d.Buttons, OrganizationButtons: d.OrganizationButtons},
			entries: entries[d.ID]})
	}
	slices.SortFunc(reached, func(a, b target) int { return cmp.Compare(a.dest.ID, b.dest.ID) })
	return reached, nil
}

// withReached are the Destinations of a rendering in id order: those reached, with the delivery each had, and those
// the Alert Group reached before and reaches no longer, whose delivery has not ended and waits for no final edit, and
// the outgoing webhooks in the events mode it sent events to (C-26.FR-4). A rendering that cannot grow the
// Destinations (grow false) keeps of the reached ones only those the Alert Group had.
func withReached(reached []target, had []dbgen.ListGroupDestinationsRow, grow bool) []target {
	out := slices.Clone(reached)
	if !grow {
		out = slices.DeleteFunc(out, func(t target) bool {
			return !slices.ContainsFunc(had, func(h dbgen.ListGroupDestinationsRow) bool { return h.ID == t.dest.ID })
		})
	}
	index := make(map[int64]int, len(out))
	for i, t := range out {
		index[t.dest.ID] = i
	}
	for i := range had {
		h := &had[i]
		if j, ok := index[h.ID]; ok {
			out[j].had = h
			continue
		}
		if h.Delivered && (ended(h.State) || h.Retiring) {
			continue
		}
		t := target{dest: dbgen.ListRouteDestinationsRow{ID: h.ID, PublicID: h.PublicID, Name: h.Name, Type: h.Type,
			ConnectionID: h.ConnectionID, Health: h.Health, WebhookMode: h.WebhookMode, WebhookReplies: h.WebhookReplies,
			Buttons: h.Buttons, OrganizationButtons: h.OrganizationButtons}, entries: []routing.Reaching{}}
		if h.Delivered {
			t.had = h
		}
		out = append(out, t)
	}
	for i := range out {
		if out[i].entries == nil {
			out[i].entries = []routing.Reaching{}
		}
	}
	slices.SortFunc(out, func(a, b target) int { return cmp.Compare(a.dest.ID, b.dest.ID) })
	return out
}

// withAdded adds to the targets the Destinations of the reach the growth a adds that are not among them (growth),
// in id order.
func withAdded(targets, reached []target, a growth) []target {
	out := targets
	for _, t := range reached {
		if a.adds(t) && !slices.ContainsFunc(out, func(x target) bool { return x.dest.ID == t.dest.ID }) {
			out = append(slices.Clone(out), t)
		}
	}
	if len(out) != len(targets) {
		slices.SortFunc(out, func(a, b target) int { return cmp.Compare(a.dest.ID, b.dest.ID) })
	}
	return out
}

// grownBy reports whether the target is the Destination of the growth g and an entry of its Team reaches it now.
func grownBy(t target, g *groups.Growth) bool {
	return g != nil && t.dest.ID == g.Destination && slices.ContainsFunc(t.entries, func(e routing.Reaching) bool {
		return e.Kind == routing.EntryTeam && e.TeamID == g.Team
	})
}

// gone reports whether the delivery a target had left its Destination: its final edit is done or waits, or it was
// withheld, so that publishing it again needs a Storm summary where a Storm holds it.
func gone(had *dbgen.ListGroupDestinationsRow) bool {
	return had != nil && had.Delivered && (ended(had.State) || had.Retiring)
}

// heldBy is the Storm that holds the Alert Group in a Destination it has a delivery in, nil when none does.
func heldBy(had []dbgen.ListGroupDestinationsRow) *int64 {
	for _, h := range had {
		if h.HeldByStormID.Valid {
			id := h.HeldByStormID.Int64
			return &id
		}
	}
	return nil
}

// unpublished reports whether the Alert Group's first Publication is still to come: a Destination that a Team or a
// rise adds before it is part of it (C-26.FR-5).
func unpublishedGroup(g *groups.Group) bool {
	return g.FirstPublishedAt == nil && g.Status != groups.StatusResolved
}

// grows reports whether a rendering may create the deliveries of every Destination the Alert Group reaches without
// one (C-26.FR-4, FR-9): a rise of its level, by an Alert or by the Route's Minimum severity, after which it reaches
// what its level now meets. A Team that joined it, by its Alerts or by the evaluation of the Teams on a save, adds only
// the Destinations of its own entries, destination_joined only its Destination, and a change of a Team's Destination
// entries only the Destination of the entry it added or changed (withAdded), so that none brings back a Destination
// below critical that a fall of the level did not bring. Any other rendering — a fall of the level, a resolution, a
// Command, Alerts that bring neither a Team nor a rise, or the re-render of a change of configuration such as a
// renamed Team or a button set — updates the deliveries the Alert Group has and creates none; only an Alert Group
// without any delivery yet is published wherever it reaches, as its first Publication.
func grows(r groups.Rendering) bool {
	return slices.ContainsFunc(r.Events, func(e groups.Recorded) bool {
		return e.Event == groups.EventSeverityRaised || e.Event == groups.EventUrgencyRaised
	})
}

// joinedTeams are the Teams that joined the Alert Group in the rendering.
func joinedTeams(r groups.Rendering) []int64 {
	var out []int64
	for _, e := range r.Events {
		if e.Event == groups.EventTeamJoined && e.Team != 0 {
			out = append(out, e.Team)
		}
	}
	return out
}

// growth is what a rendering adds to the Destinations of the Alert Group besides a rise (grows): those of its
// destination_joined events, those an entry of a Team that joined reaches, and the one of a change of a Team's
// Destination entries.
type growth struct {
	joined map[int64]groups.Recorded
	teams  []int64
	grows  *groups.Growth
}

// adds reports whether the growth adds the target.
func (a growth) adds(t target) bool {
	if _, ok := a.joined[t.dest.ID]; ok || grownBy(t, a.grows) {
		return true
	}
	return slices.ContainsFunc(t.entries, func(e routing.Reaching) bool {
		return e.Kind == routing.EntryTeam && slices.Contains(a.teams, e.TeamID)
	})
}

// activeStorm locks the active Storm of the Route FOR SHARE, before any delivery it holds is written, and returns it,
// nil when the Route has none.
func (s *Service) activeStorm(ctx context.Context, q queries, routeID int64) (*storm, error) {
	active, err := q.LockActiveStorm(ctx, dbgen.LockActiveStormParams{OrgID: s.orgID, RouteID: routeID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock the storm of route %d: %w", routeID, err)
	}
	return &storm{id: active.ID, started: active.StartedAt, count: active.AlertGroupCount,
		urgent: active.UrgentCount}, nil
}

// publicationOf is the loudness of the Publication of a delivery that the rendering creates, and the Storm that holds
// it. st is the Route's active Storm, which an Urgent Alert Group is never held by, and held the Storm that holds the
// Alert Group in a Destination it already has. Loud at the creation of the Alert Group, held by its Storm; Quiet in a
// Destination it joins by a move; the loudness of its destination_joined in a Destination it joined, held by the
// Route's Storm (C-26.FR-5); Quiet for a change of configuration (C-26.FR-9, ADR-0018); Loud as part of a first
// Publication still to come; and Quiet otherwise. Outside the creation and destination_joined, a delivery is held when
// the active Storm holds the Alert Group elsewhere or the Alert Group was created since the Storm started, so that a
// Destination new to a held Alert Group holds it too, as a Destination added to the Route does (C-11.FR-6). A delivery that exists keeps its own loudness.
func publicationOf(r groups.Rendering, g *groups.Group, created, moved bool, joined groups.Recorded, st *storm,
	held *int64) (pgtype.Bool, *int64) {
	quiet := pgtype.Bool{Bool: deliveryEvent(DeliveryAddedDestination).loud(false), Valid: true}
	loud := pgtype.Bool{Bool: true, Valid: true}
	var storm, holds *int64
	if st != nil && !g.Urgent {
		storm = &st.id
		// The Storm holds an Alert Group created since it started, as the change of a Route's Destinations reads it
		// (ListOpenGroupsOfRoute), even where a final edit cleared its hold, and one it holds elsewhere.
		if (held != nil && *held == st.id) || !g.CreatedAt.Before(st.started) {
			holds = storm
		}
	}
	switch {
	case created:
		return loud, storm
	case moved:
		return quiet, nil
	case joined.Destination != nil:
		return pgtype.Bool{Bool: joined.Loudness == groups.Loud, Valid: true}, storm
	case configurationOnly(r):
		return quiet, holds
	case unpublishedGroup(g):
		return loud, holds
	}
	return quiet, holds
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
// created or moved or the delivery joins again, the loudness of the Publication of a delivery it creates and the Storm that holds it, the entries
// that reach the Destination, encoded, its Thread replies, the time, the Thread batching window and the transaction of
// the change, which the request of an outgoing webhook is rendered through.
type enqueueing struct {
	roots          Roots
	created, moved bool
	// rejoin publishes again, Quietly, a delivery that left its Destination, in the Destination a change of a Team's
	// Destination entries added or changed the Team's entry of (groups.Rendering.Grows).
	rejoin  bool
	loud    pgtype.Bool
	heldBy  *int64
	entries []byte
	replies []reply
	now     time.Time
	window  time.Duration
	tx      dbgen.DBTX
}

// enqueue sets the Desired state of the Alert Group in one Destination and queues its Thread replies; while the
// Destination is Broken, while a Storm holds the delivery, or once the delivery ended, they are created dropped
// (C-11.FR-6, FR-13, FR-19). A delivery the rendering creates outside the creation of the Alert Group has none: its
// Publication shows the current state. An Urgent Alert Group that a Storm holds is released at once.
func (s *Service) enqueue(ctx context.Context, q queries, r groups.Rendering, d Destination, broken bool,
	how enqueueing) error {
	g, now := r.Group, how.now
	entries := how.entries
	if entries == nil {
		entries = []byte("[]")
	}
	row, err := q.EnsureDelivery(ctx, dbgen.EnsureDeliveryParams{OrgID: s.orgID, DestinationID: d.ID,
		AlertGroupID: pgtype.Int8{Int64: g.ID, Valid: true}, HeldByStormID: nullInt(how.heldBy), Urgent: g.Urgent,
		PublicationLoud: how.loud, Entries: entries, Now: now})
	if err != nil {
		return fmt.Errorf("create the delivery of alert group #%d to %s: %w", g.Number, d.PublicID, err)
	}
	if row.Inserted && !how.created {
		how.replies = nil
	}
	state := row.State
	if (how.moved || how.rejoin) && !row.Inserted {
		p := dbgen.RejoinDeliveryParams{OrgID: s.orgID, AlertGroupID: g.ID, DestinationID: d.ID,
			Loud: deliveryEvent(DeliveryAddedDestination).loud(false), Now: now}
		if how.rejoin {
			p.Loud, p.HeldByStormID = how.loud.Bool, nullInt(how.heldBy)
		}
		n, err := q.RejoinDelivery(ctx, p)
		if err != nil {
			return fmt.Errorf("publish alert group #%d in %s again: %w", g.Number, d.PublicID, err)
		}
		if n > 0 {
			state = statePending
			// Published afresh, the Root message shows the current state: no Thread reply of the change.
			how.replies = nil
			if r.Changed != nil {
				r.Changed()
			}
		}
	}
	held := row.HeldByStormID.Valid
	if held && g.Urgent {
		// A change of configuration publishes Quietly (C-26.FR-9, ADR-0018).
		if err := q.ReleaseHeld(ctx, dbgen.ReleaseHeldParams{OrgID: s.orgID, ID: row.ID, Now: now,
			Loud: !configurationOnly(r)}); err != nil {
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
			Quiet: configurationOnly(r), Now: now}); err != nil {
			return fmt.Errorf("set the desired state of alert group #%d in %s: %w", g.Number, d.PublicID, err)
		}
		if r.Changed != nil {
			r.Changed()
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

// configurationOnly reports whether a rendering comes from a change of configuration, not from an Alert or a
// Command: the dispatcher marks it so (groups.Rendering.Configuration), as the evaluation of the Teams of the open
// Alert Groups on a save (C-25.FR-13) and the re-render of a changed button set, or it has no lifecycle event, or
// only rises that the Route's Minimum severity made (groups.ReasonMinimumSeverity). Such a change publishes Quietly.
func configurationOnly(r groups.Rendering) bool {
	if r.Configuration {
		return true
	}
	for _, e := range r.Events {
		if e.Reason != groups.ReasonMinimumSeverity ||
			(e.Event != groups.EventUrgencyRaised && e.Event != groups.EventSeverityRaised) {
			return false
		}
	}
	return true
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
