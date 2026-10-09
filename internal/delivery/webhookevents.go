// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// The events mode of outgoing webhooks (C-15.FR-2, ADR-0005): Enqueue queues one webhook_events row per lifecycle
// event — every row of the lifecycle event tables, Quiet ones and those with nothing to show in a messenger included,
// never a delivery event — for each events-mode Destination of the Alert Group's Route, with a new webhook-id and the
// version 1 body rendered at that moment, whether the Destination is Broken or a Storm holds the Alert Group. The
// worker claims only the head event of each Alert Group and Destination, so that the events of an Alert Group are
// sent in order and at least once, never collapsed, while other Alert Groups do not wait; a retry keeps the webhook-id
// and the body. The outcomes follow the rules of outcomes.go, with the events kept through a Broken period, whatever
// their age, and an unknown answer ending the event as Not delivered so that the next one goes. Deleting the
// Destination ends its waiting events as Not delivered.

// The modes of an outgoing webhook Destination.
const (
	webhookEvents = "events"
	webhookBoth   = "both"
)

// kindWebhookEvent is the kind of an events-mode request, the kind label of muster_delivery_attempts_total.
const kindWebhookEvent = "webhook_event"

// destinationDeleted is the error of the events a deleted Destination abandons.
const destinationDeleted = "the Destination was deleted"

// webhookEventQueries are the queries of outgoing webhook events.
type webhookEventQueries interface {
	InsertWebhookEvent(ctx context.Context, arg dbgen.InsertWebhookEventParams) error
	ClaimDueWebhookEvents(ctx context.Context, arg dbgen.ClaimDueWebhookEventsParams) (
		[]dbgen.ClaimDueWebhookEventsRow, error)
	GetLeasedWebhookEvent(ctx context.Context, arg dbgen.GetLeasedWebhookEventParams) (
		dbgen.GetLeasedWebhookEventRow, error)
	RenewWebhookEventLease(ctx context.Context, arg dbgen.RenewWebhookEventLeaseParams) error
	RescheduleWebhookEvent(ctx context.Context, arg dbgen.RescheduleWebhookEventParams) error
	RecordWebhookEventDelivered(ctx context.Context, arg dbgen.RecordWebhookEventDeliveredParams) (int64, error)
	RecordWebhookEventRetry(ctx context.Context, arg dbgen.RecordWebhookEventRetryParams) (
		dbgen.RecordWebhookEventRetryRow, error)
	RecordWebhookEventNotDelivered(ctx context.Context, arg dbgen.RecordWebhookEventNotDeliveredParams) (int64, error)
	ReleaseWebhookEventLease(ctx context.Context, arg dbgen.ReleaseWebhookEventLeaseParams) (int64, error)
	LeaseOldestWaitingEvent(ctx context.Context, arg dbgen.LeaseOldestWaitingEventParams) (
		dbgen.LeaseOldestWaitingEventRow, error)
	AbandonWebhookEvents(ctx context.Context, arg dbgen.AbandonWebhookEventsParams) (
		[]dbgen.AbandonWebhookEventsRow, error)
	ResetWebhookEventBudgets(ctx context.Context, arg dbgen.ResetWebhookEventBudgetsParams) error
	DeleteExpiredWebhookEvents(ctx context.Context, arg dbgen.DeleteExpiredWebhookEventsParams) (int64, error)
}

// sendsEvents reports whether a Destination of a Route receives outgoing webhook events: an outgoing webhook in the
// mode events or both.
func sendsEvents(d dbgen.ListRouteDestinationsRow) bool {
	return d.Type == TypeWebhook && (d.WebhookMode.String == webhookEvents || d.WebhookMode.String == webhookBoth)
}

// splitDestinations separates the Destinations of a Route that have deliveries — messengers and outgoing webhooks
// with a template — from those that receive events; an outgoing webhook in mode both is in both.
func splitDestinations(all []dbgen.ListRouteDestinationsRow) (deliveries, events []dbgen.ListRouteDestinationsRow) {
	for _, d := range all {
		if sendsEvents(d) {
			events = append(events, d)
		}
		if d.Type != TypeWebhook || d.WebhookMode.String != webhookEvents {
			deliveries = append(deliveries, d)
		}
	}
	return deliveries, events
}

// newWebhookID is a new webhook-id: msg_ and 26 random characters.
func newWebhookID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return "msg_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// queueEvents queues, in the dispatcher's transaction tx, one event per lifecycle event of the rendering for each
// events-mode Destination of the Alert Group's Route (C-15.FR-2): notify is the loudness of its row and a Loud one
// carries its Mentions in that Destination as data (C-15.FR-11); the body is rendered now. Neither a Broken Destination
// nor a Storm holds them back (C-11.FR-6).
func (s *Service) queueEvents(ctx context.Context, tx groups.DBTX, q queries, r groups.Rendering,
	dests []dbgen.ListRouteDestinationsRow, route dbgen.GetRouteDeliveryRow, now time.Time) error {
	if s.bodies == nil || len(dests) == 0 || len(r.Events) == 0 {
		return nil
	}
	g := r.Group
	render, err := s.bodies.EventBodies(ctx, tx, EventSource{Group: g, RoutePublicID: route.PublicID,
		RouteName: route.Name, Actor: r.Actor})
	if err != nil {
		return fmt.Errorf("read alert group #%d for its webhook events: %w", g.Number, err)
	}
	var received pgtype.Timestamptz
	if r.ReceivedAt != nil {
		received = pgtype.Timestamptz{Time: r.ReceivedAt.UTC(), Valid: true}
	}
	for _, d := range dests {
		for _, e := range r.Events {
			row, _ := rowOf(e, r.Actor) // Enqueue refused an event without a row before
			loud := row.Loudness == groups.Loud
			var targets []mentions.Target
			if names := row.mentionsOf(e); loud && s.mentions != nil && len(names) > 0 {
				if targets, err = s.mentions.Resolve(ctx, tx, s.orgID, mentions.Request{DestinationID: d.ID,
					DestinationType: d.Type, AlertGroupID: g.ID, Seq: e.Seq, Mentions: names}); err != nil {
					return fmt.Errorf("resolve the mentions of the %s event of alert group #%d in %s: %w", e.Event,
						g.Number, d.PublicID, err)
				}
			}
			body, err := render(EventBody{Event: e, Notify: loud, Mentions: targets, OccurredAt: now})
			if err != nil {
				return fmt.Errorf("render the %s event of alert group #%d: %w", e.Event, g.Number, err)
			}
			if err := q.InsertWebhookEvent(ctx, dbgen.InsertWebhookEventParams{OrgID: s.orgID, DestinationID: d.ID,
				AlertGroupID: g.ID, Sequence: e.Seq, WebhookID: newWebhookID(), Event: string(e.Event), Notify: loud,
				Now: now, Body: body, ReceivedAt: received}); err != nil {
				return fmt.Errorf("queue the %s event of alert group #%d to %s: %w", e.Event, g.Number, d.PublicID,
					err)
			}
		}
	}
	return nil
}

// abandonEvents ends the waiting events of the deleted Destination d as Not delivered in the transaction that deletes
// it (C-15.FR-12, C-11.FR-14), each with its not_delivered delivery event and the hint of its Alert Group; no final
// event is sent.
func (s *Service) abandonEvents(ctx context.Context, q queries, d int64, now time.Time) error {
	rows, err := q.AbandonWebhookEvents(ctx, dbgen.AbandonWebhookEventsParams{OrgID: s.orgID, DestinationID: d,
		Error: destinationDeleted})
	if err != nil {
		return fmt.Errorf("abandon the webhook events of destination %d: %w", d, err)
	}
	hinted := map[string]bool{}
	for _, r := range rows {
		group := r.AlertGroupID
		if err := RecordEvent(ctx, q, s.orgID, Event{At: now, DestinationID: d, AlertGroupID: &group,
			Kind: EventNotDelivered, ErrorClass: string(OutcomeUnknown), Error: destinationDeleted,
			Detail: map[string]any{"kind": kindWebhookEvent, "event": r.Event}}); err != nil {
			return err
		}
		if !hinted[r.AlertGroupPublicID] {
			hinted[r.AlertGroupPublicID] = true
			if err := hintGroup(ctx, q, s.orgID, r.AlertGroupPublicID); err != nil {
				return err
			}
		}
	}
	return nil
}

// PruneWebhookEvents deletes, in batches of at most RetentionBatch, the outgoing webhook events of the Organization
// that are delivered or Not delivered and were created retention.alert_details before now (schema.md §6), until a
// batch comes back short, and returns how many it deleted. It then wipes the secrets of the deleted Destinations whose
// last call in flight ended without wiping them, because its replica stopped. Running it twice deletes nothing more.
func (s *Service) PruneWebhookEvents(ctx context.Context, now time.Time) (int64, error) {
	q := s.store.q()
	days, err := q.GetRetentionDetailsDays(ctx, s.orgID)
	if err != nil {
		return 0, fmt.Errorf("read retention.alert_details: %w", err)
	}
	cutoff := now.UTC().Add(-time.Duration(days) * 24 * time.Hour)
	var total int64
	for {
		n, err := q.DeleteExpiredWebhookEvents(ctx, dbgen.DeleteExpiredWebhookEventsParams{OrgID: s.orgID,
			Cutoff: cutoff, BatchSize: RetentionBatch})
		total += n
		if err != nil {
			return total, fmt.Errorf("delete the expired webhook events: %w", err)
		}
		if n < RetentionBatch {
			break
		}
	}
	if err := q.WipeDestinationSecrets(ctx, dbgen.WipeDestinationSecretsParams{OrgID: s.orgID,
		Now: s.real.Now().UTC()}); err != nil {
		return total, fmt.Errorf("wipe the secrets of the deleted destinations: %w", err)
	}
	return total, nil
}

// claimEvents leases the due head events of the Organization.
func (w *Worker) claimEvents(ctx context.Context, org int64) ([]int64, error) {
	rows, err := db.Claim(ctx, w.Store.begin, w.Lease, w.batch(),
		func(ctx context.Context, tx pgx.Tx, p db.ClaimParams) ([]dbgen.ClaimDueWebhookEventsRow, error) {
			return w.Store.queries(tx).ClaimDueWebhookEvents(ctx, dbgen.ClaimDueWebhookEventsParams{OrgID: org,
				Owner: p.Owner, LeaseUntil: p.LeaseUntil, Due: p.Due, Now: p.Now, Lim: p.Limit})
		})
	if err != nil {
		return nil, fmt.Errorf("claim the due webhook events: %w", err)
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// eventAttempt is an event that has its token and is about to be sent.
type eventAttempt struct {
	row         dbgen.GetLeasedWebhookEventRow
	destination Destination
	at          time.Time
}

// sendEvent attempts one claimed event; a failure is logged and the event is attempted again once its lease runs out.
func (w *Worker) sendEvent(ctx context.Context, org, id int64) {
	a, ok, err := w.prepareEvent(ctx, org, id)
	if err == nil && ok {
		err = w.callEvent(ctx, org, a)
	}
	if err != nil && ctx.Err() == nil {
		w.Log.Log(ctx, logging.DeliveryWorkFailed, logging.F("work", kindWebhookEvent), logging.F("error", err.Error()))
	}
}

// prepareEvent re-reads a claimed event in a short transaction while its lease is still held: one without a token of
// its Destination's limiter waits until it is due plus TokenMargin; otherwise the token is taken and the lease is
// renewed for the call.
func (w *Worker) prepareEvent(ctx context.Context, org, id int64) (eventAttempt, bool, error) {
	var a eventAttempt
	ok := false
	err := w.Store.inTx(ctx, func(q queries) error {
		ok = false
		realNow := w.Lease.Clocks.Real.Now().UTC()
		row, err := q.GetLeasedWebhookEvent(ctx, dbgen.GetLeasedWebhookEventParams{OrgID: org, ID: id,
			Owner: w.Lease.Owner, Now: realNow})
		if errors.Is(err, pgx.ErrNoRows) {
			// The lease ran out, or the Destination was deleted since the claim, which ended the event and left the
			// lease, so that its secrets wait for this replica.
			return w.releaseEvent(ctx, q, org, id, realNow)
		}
		if err != nil {
			return fmt.Errorf("read the webhook event: %w", err)
		}
		now := w.Lease.Clocks.Business.Now().UTC()
		d := Destination{ID: row.DestinationID, PublicID: row.DestinationPublicID, Name: row.DestinationName,
			Type: row.DestinationType}
		t, err := takeTokens(ctx, q, org, subjectOf(d), now)
		if err != nil {
			return err
		}
		if !t.ok {
			if err := q.RescheduleWebhookEvent(ctx, dbgen.RescheduleWebhookEventParams{OrgID: org, ID: id,
				Owner: w.Lease.Owner, At: t.due.Add(TokenMargin)}); err != nil {
				return fmt.Errorf("reschedule the webhook event: %w", err)
			}
			return nil
		}
		if err := q.RenewWebhookEventLease(ctx, dbgen.RenewWebhookEventLeaseParams{OrgID: org, ID: id,
			Owner: w.Lease.Owner, LeaseUntil: realNow.Add(w.Lease.Duration)}); err != nil {
			return fmt.Errorf("renew the lease of the webhook event: %w", err)
		}
		a, ok = eventAttempt{row: row, destination: d, at: now}, true
		return nil
	})
	return a, ok, err
}

// callEvent sends a prepared event outside any transaction and records its outcome.
func (w *Worker) callEvent(ctx context.Context, org int64, a eventAttempt) error {
	start := w.Lease.Clocks.Real.Now()
	out := Outcome{Kind: OutcomeUnknown, Error: "no sender for outgoing webhook events"}
	if w.Events != nil {
		out = w.Events.SendEvent(ctx, EventCall{Class: outbound.ClassDelivery, Destination: a.destination,
			WebhookID: a.row.WebhookID, Body: a.row.Body})
	}
	took := w.Lease.Clocks.Real.Now().Sub(start)
	recorded := false
	var logs after
	err := w.Store.inTx(ctx, func(q queries) error {
		logs = nil
		var err error
		recorded, err = w.recordEvent(ctx, q, org, a, out, &logs)
		return err
	})
	w.attempted(ctx, a.destination, a.row.Number, kindWebhookEvent, out, a.row.Attempts+1, took)
	if err == nil {
		logs.run(ctx, w.Log)
	}
	if err == nil && recorded && out.Kind == OutcomeOK && a.row.ReceivedAt.Valid {
		metrics.DeliveryLatency.With(a.destination.PublicID).Update(
			max(a.at.Sub(a.row.ReceivedAt.Time).Seconds(), 0))
	}
	return err
}

// recordEvent records the outcome of an event's call by the rules of outcomes.go in the transaction of q; recorded is
// false when the lease went to another replica or the event ended meanwhile, whose lease it then gives up.
func (w *Worker) recordEvent(ctx context.Context, q queries, org int64, a eventAttempt, out Outcome,
	logs *after) (bool, error) {
	recorded, err := w.recordEventOutcome(ctx, q, org, a, out, w.Lease.Clocks.Business.Now().UTC(), logs)
	if err != nil || recorded {
		return recorded, err
	}
	return false, w.releaseEvent(ctx, q, org, a.row.ID, w.Lease.Clocks.Real.Now().UTC())
}

// releaseEvent gives up this replica's lease of an event that ended before or while its call was made, and wipes the
// secrets of its Destination when that was the last call of a deleted one (C-15.FR-12); it changes nothing when the
// lease is another replica's.
func (w *Worker) releaseEvent(ctx context.Context, q queries, org, id int64, realNow time.Time) error {
	dest, err := q.ReleaseWebhookEventLease(ctx, dbgen.ReleaseWebhookEventLeaseParams{OrgID: org, ID: id,
		Owner: w.Lease.Owner})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("release the webhook event %d: %w", id, err)
	}
	return wipeSecrets(ctx, q, org, dest, realNow)
}

func (w *Worker) recordEventOutcome(ctx context.Context, q queries, org int64, a eventAttempt, out Outcome,
	now time.Time, logs *after) (bool, error) {
	id, d := a.row.ID, a.destination
	broken := a.row.DestinationHealth == healthBroken
	switch out.Kind {
	case OutcomeOK:
		_, err := q.RecordWebhookEventDelivered(ctx, dbgen.RecordWebhookEventDeliveredParams{OrgID: org, ID: id,
			Owner: w.Lease.Owner, Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("record the delivered webhook event: %w", err)
		}
		if broken {
			return true, markHealthy(ctx, q, w.raiser(org), org, d.ID, now, logs)
		}
		return true, nil
	case OutcomeRetryAfter:
		// Retried exactly as asked; unlike a messenger's, the answer holds back only this event, so that the events of
		// other Alert Groups do not wait (C-15.AC-6).
		_, ok, err := w.retryEvent(ctx, q, org, id, now.Add(out.RetryAfter), false, out, now)
		return ok, err
	case OutcomeTransient:
		r, ok, err := w.retryEvent(ctx, q, org, id, now.Add(w.backoff(a.row.Attempts+1)), true, out, now)
		if err != nil || !ok {
			return ok, err
		}
		return true, w.transient(ctx, q, org, d, broken, r.Attempts, r.FirstFailedAt, out, now, logs)
	case OutcomeFatal:
		if _, ok, err := w.retryEvent(ctx, q, org, id, now, false, out, now); err != nil || !ok {
			return ok, err
		}
		return true, breakDestination(ctx, q, w.raiser(org), org, d, causeFatal, failure(out, "fatal error"), now,
			logs)
	case OutcomeUnknown, OutcomeTemplateError, OutcomeMarkupRejected, OutcomeGone, OutcomeThreadLost:
		// The answers a messenger gives and an outgoing webhook does not are unknown too.
	}
	class := string(OutcomeUnknown)
	if out.Kind == OutcomeTemplateError {
		class = string(OutcomeTemplateError)
	}
	text := failure(out, unknownText)
	_, err := q.RecordWebhookEventNotDelivered(ctx, dbgen.RecordWebhookEventNotDeliveredParams{OrgID: org, ID: id,
		Owner: w.Lease.Owner, ErrorClass: class, Error: text})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record the webhook event as not delivered: %w", err)
	}
	group := a.row.AlertGroupID
	if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: d.ID, AlertGroupID: &group,
		Kind: EventNotDelivered, ErrorClass: class, Error: outbound.Untrusted(text),
		Detail: map[string]any{"kind": kindWebhookEvent, "event": a.row.Event}}); err != nil {
		return false, err
	}
	if err := hintGroup(ctx, q, org, a.row.AlertGroupPublicID); err != nil {
		return false, err
	}
	status := ""
	if out.Status > 0 {
		status = strconv.Itoa(out.Status)
	}
	logs.add(func(ctx context.Context, log *logging.Logger) {
		log.Log(ctx, logging.WebhookEventNotDelivered, logging.F("destination", d.PublicID),
			logging.F("group", a.row.AlertGroupPublicID), logging.F("lifecycle_event", a.row.Event), logging.F("status", status))
	})
	return true, nil
}

// retryEvent records an outcome of an event retried at at; ok is false when the lease went to another replica or the
// event ended.
func (w *Worker) retryEvent(ctx context.Context, q queries, org, id int64, at time.Time, counted bool, out Outcome,
	now time.Time) (dbgen.RecordWebhookEventRetryRow, bool, error) {
	r, err := q.RecordWebhookEventRetry(ctx, dbgen.RecordWebhookEventRetryParams{OrgID: org, ID: id,
		Owner: w.Lease.Owner, At: at, Counted: counted, ErrorClass: nonEmpty(errorClass(out.Kind)),
		Error: nonEmpty(string(out.Error)), Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("record the %s outcome of the webhook event: %w", out.Kind, err)
	}
	return r, true, nil
}

// probeEvent is the probe of a Broken outgoing webhook through its oldest waiting event (C-11.FR-9): attempted through
// the worker's path when its lease is free; handled reports whether an event waits at all.
func (w *Worker) probeEvent(ctx context.Context, org int64, d Destination) (bool, error) {
	var waiting dbgen.LeaseOldestWaitingEventRow
	err := w.Store.inTx(ctx, func(q queries) error {
		realNow := w.Lease.Clocks.Real.Now().UTC()
		var err error
		waiting, err = q.LeaseOldestWaitingEvent(ctx, dbgen.LeaseOldestWaitingEventParams{OrgID: org,
			DestinationID: d.ID, Owner: w.Lease.Owner, LeaseUntil: realNow.Add(w.Lease.Duration), Now: realNow,
			Due: w.Lease.Clocks.Business.Now().UTC()})
		if errors.Is(err, pgx.ErrNoRows) {
			waiting = dbgen.LeaseOldestWaitingEventRow{}
			return nil
		}
		return err
	})
	if err != nil {
		return false, fmt.Errorf("lease the oldest waiting webhook event of %s: %w", d.PublicID, err)
	}
	if waiting.ID == 0 {
		return false, nil
	}
	if waiting.Free {
		w.sendEvent(ctx, org, waiting.ID)
	}
	return true, nil
}
