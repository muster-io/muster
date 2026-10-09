// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// The rules of the outcomes of an adapter call (C-11.FR-8, FR-10), for a delivery and for a Thread reply alike:
//
//   - ok: delivered; a Broken Destination it reaches, which only a probe does, is healthy again.
//   - retry_after: wait exactly as asked, not an attempt.
//   - transient: an attempt of the budget, retried after delivery.transient_backoff; when the attempt or the time
//     budget of delivery.transient_budget runs out, the Destination becomes Broken as unavailable and the row waits.
//   - fatal: the Destination becomes Broken at once and the row waits.
//   - unknown, and a type no adapter serves: the row ends Not delivered with the error; the Destination stays healthy.
//   - markup_rejected: the same text is sent again without markup in the same attempt.
//   - gone: the deleted Root message flow (publication.go); a Publication cannot be gone and is Not delivered; a final
//     edit that finds its Root message gone retires the delivery all the same.
//   - thread_lost: retried after TransientFirstStep until S-042 gives it its rule.
//
// A failure of a call to a Destination that is Broken already, which only a probe makes, keeps it Broken with that
// error as the reason.

// outcomeQueries are the queries of the outcome rules, the late Publication, the possible duplicate and the deleted
// Root message.
type outcomeQueries interface {
	RecordNotDelivered(ctx context.Context, arg dbgen.RecordNotDeliveredParams) (int64, error)
	RecordReplyNotDelivered(ctx context.Context, arg dbgen.RecordReplyNotDeliveredParams) (int64, error)
	MarkPossibleDuplicate(ctx context.Context, arg dbgen.MarkPossibleDuplicateParams) error
	GetLatestRepublishedAt(ctx context.Context, arg dbgen.GetLatestRepublishedAtParams) (time.Time, error)
	ResetForRepublish(ctx context.Context, arg dbgen.ResetForRepublishParams) (int64, error)
	MarkDeletedInMessenger(ctx context.Context, arg dbgen.MarkDeletedInMessengerParams) (int64, error)
	DropPendingReplies(ctx context.Context, arg dbgen.DropPendingRepliesParams) error
	SettleResolvedPublication(ctx context.Context, arg dbgen.SettleResolvedPublicationParams) error
}

// The texts an outcome without an error of its own records.
const (
	unknownText    = "unknown response"
	plainRejected  = "the messenger rejected the text without markup"
	repeatedPrefix = "unavailable after repeated failures: "
)

// after are the lines to log once the transaction that recorded them commits.
type after []func(ctx context.Context, log *logging.Logger)

func (a *after) add(f func(ctx context.Context, log *logging.Logger)) { *a = append(*a, f) }

func (a *after) run(ctx context.Context, log *logging.Logger) {
	for _, f := range *a {
		f(ctx, log)
	}
}

// failure is the masked error text of an outcome, or fallback when the adapter gave none.
func failure(out Outcome, fallback string) string {
	if out.Error == "" {
		return fallback
	}
	return string(out.Error)
}

// exhausted reports whether the Transient budget of delivery.transient_budget ran out: the attempts reached the
// attempt budget, or the first failure lies more than the time budget before now.
func exhausted(attempts int64, firstFailed pgtype.Timestamptz, now time.Time) bool {
	return attempts >= TransientBudgetAttempts || (firstFailed.Valid && now.Sub(firstFailed.Time) > TransientBudgetTime)
}

// backoff is the wait after the n-th Transient attempt: delivery.transient_backoff, exponential with equal jitter. The
// step doubles from TransientFirstStep up to TransientBackoffMax; the wait is half the step plus a random part of the
// other half, and never shorter than TransientFirstStep.
func (w *Worker) backoff(n int64) time.Duration {
	random := w.Random
	if random == nil {
		random = rand.Float64
	}
	step := TransientFirstStep
	for i := int64(1); i < n && step < TransientBackoffMax; i++ {
		step *= 2
	}
	step = min(step, TransientBackoffMax)
	return max(step/2+time.Duration(random()*float64(step/2)), TransientFirstStep)
}

// raiser is the Raiser of the Internal alerts of the Organization org.
func (w *Worker) raiser(org int64) *internalalerts.Raiser {
	return internalalerts.NewRaiser(org, w.RunbookBase)
}

// plain completes a call whose markup the messenger rejected: resend makes the same call without markup, and a second
// rejection is an unknown response.
func plain(resend func() Outcome) Outcome {
	out := resend()
	if out.Kind == OutcomeMarkupRejected {
		return Outcome{Kind: OutcomeUnknown, Error: outbound.Untrusted(failure(out, plainRejected))}
	}
	return out
}

// record records the outcome of a delivery's call in the transaction of q; recorded is false when the lease went to
// another replica meanwhile. rejected is the first answer of a call whose markup the messenger rejected.
//
// After every recorded outcome the secrets of the Destination are wiped when it is deleted and nothing of it is pending
// any more: the Destination may have been deleted while the call was in flight, whatever the call was, and the query
// changes nothing otherwise.
func (w *Worker) record(ctx context.Context, q queries, org int64, a attempt, c Call, out Outcome, rejected *Outcome,
	logs *after) (bool, error) {
	recorded, err := w.recordOutcome(ctx, q, org, a, c, out, logs)
	if err != nil || !recorded {
		return recorded, err
	}
	if rejected != nil {
		if err := RecordEvent(ctx, q, org, Event{At: w.Lease.Clocks.Business.Now().UTC(),
			DestinationID: a.destination.ID, AlertGroupID: a.group(), StormID: a.storm(), Kind: EventMarkupRejected,
			Error: rejected.Error}); err != nil {
			return true, err
		}
	}
	return true, wipeSecrets(ctx, q, org, a.destination.ID)
}

func (w *Worker) recordOutcome(ctx context.Context, q queries, org int64, a attempt, c Call, out Outcome,
	logs *after) (bool, error) {
	now := w.Lease.Clocks.Business.Now().UTC()
	id := a.row.ID
	switch out.Kind {
	case OutcomeOK:
		if a.row.DesiredRetire {
			return w.retired(ctx, q, org, a, now, true, logs)
		}
		return w.delivered(ctx, q, org, a, c, out, now, logs)
	case OutcomeRetryAfter:
		until := now.Add(out.RetryAfter)
		ok, err := w.retry(ctx, q, org, id, until.Add(TokenMargin), false, out, now)
		if err != nil || !ok {
			return ok, err
		}
		return true, holdBucket(ctx, q, org, a.destination, out, until)
	case OutcomeTransient:
		r, err := q.RecordDeliveryRetry(ctx, dbgen.RecordDeliveryRetryParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			At: now.Add(w.backoff(a.row.Attempts + 1)), Counted: true, ErrorClass: nonEmpty(errorClass(out.Kind)),
			Error: nonEmpty(string(out.Error)), Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("record the transient error: %w", err)
		}
		return true, w.transient(ctx, q, org, a.destination, a.row.DestinationHealth == healthBroken, r.Attempts,
			r.FirstFailedAt, out, now, logs)
	case OutcomeFatal:
		ok, err := w.retry(ctx, q, org, id, now, false, out, now)
		if err != nil || !ok {
			return ok, err
		}
		return true, breakDestination(ctx, q, w.raiser(org), org, a.destination, causeFatal,
			failure(out, "fatal error"), now, logs)
	case OutcomeGone:
		switch {
		case a.row.DesiredRetire:
			return w.retired(ctx, q, org, a, now, false, logs)
		case a.publication:
			return w.notDelivered(ctx, q, org, a, out, now, logs)
		}
		return true, rootGone(ctx, q, org, rootOf{delivery: id, group: a.group(), storm: a.storm(),
			destination: a.destination.ID, groupPublicID: a.row.AlertGroupPublicID,
			resolved:    a.row.GroupStatus == string(groups.StatusResolved),
			republished: a.row.RepublishedAfterDelete}, now)
	case OutcomeThreadLost:
		return w.retry(ctx, q, org, id, now.Add(TransientFirstStep), false, out, now)
	default:
		return w.notDelivered(ctx, q, org, a, out, now, logs)
	}
}

// retired records the final edit of a delivery, made or found gone: the row is retired, with the final_edit delivery
// event when the edit was made, and a Broken Destination that a probe reached is healthy again.
func (w *Worker) retired(ctx context.Context, q queries, org int64, a attempt, now time.Time, edited bool,
	logs *after) (bool, error) {
	_, err := q.RecordRetired(ctx, dbgen.RecordRetiredParams{OrgID: org, ID: a.row.ID, Owner: w.Lease.Owner, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record the final edit: %w", err)
	}
	if err := hintGroup(ctx, q, org, a.row.AlertGroupPublicID); err != nil {
		return false, err
	}
	if edited {
		r := deliveryEvent(DeliveryFinalEdit)
		if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: a.destination.ID, AlertGroupID: a.group(),
			Kind: EventFinalEdit, Loudness: r.Loudness, Mentions: r.Mentions}); err != nil {
			return false, err
		}
	}
	if edited && a.row.DestinationHealth == healthBroken {
		return true, markHealthy(ctx, q, w.raiser(org), org, a.destination.ID, now, logs)
	}
	return true, nil
}

// delivered records a call the messenger accepted: the delivery event of a first Publication — storm_summary for a
// Storm summary — and of a late one, and the end of the Broken state of a Destination that a probe reached.
func (w *Worker) delivered(ctx context.Context, q queries, org int64, a attempt, c Call, out Outcome, now time.Time,
	logs *after) (bool, error) {
	_, err := q.RecordDelivered(ctx, dbgen.RecordDeliveredParams{OrgID: org, ID: a.row.ID, Owner: w.Lease.Owner,
		Version: a.row.DesiredVersion, Hash: a.row.DesiredHash, MessageID: nonEmpty(out.MessageID),
		MessageUrl: nonEmpty(out.MessageURL), Published: a.publication, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record the delivered message: %w", err)
	}
	if err := hintGroup(ctx, q, org, a.row.AlertGroupPublicID); err != nil {
		return false, err
	}
	if a.publication {
		kind := EventPublication
		if a.row.StormID.Valid {
			kind = EventStormSummary
		}
		if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: a.destination.ID, AlertGroupID: a.group(),
			StormID: a.storm(), Kind: kind, Loudness: c.Loudness, Mentions: c.Mentions}); err != nil {
			return false, err
		}
		if a.late {
			r := deliveryEvent(DeliveryLate)
			if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: a.destination.ID,
				AlertGroupID: a.group(), Kind: EventDeliveredLate, Loudness: r.Loudness}); err != nil {
				return false, err
			}
		}
	}
	if a.row.DestinationHealth == healthBroken {
		return true, markHealthy(ctx, q, w.raiser(org), org, a.destination.ID, now, logs)
	}
	return true, nil
}

// retry records an outcome retried at at; ok is false when the lease went to another replica.
func (w *Worker) retry(ctx context.Context, q queries, org, id int64, at time.Time, counted bool, out Outcome,
	now time.Time) (bool, error) {
	_, err := q.RecordDeliveryRetry(ctx, dbgen.RecordDeliveryRetryParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
		At: at, Counted: counted, ErrorClass: nonEmpty(errorClass(out.Kind)), Error: nonEmpty(string(out.Error)),
		Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record the %s outcome: %w", out.Kind, err)
	}
	return true, nil
}

// transient applies the Transient budget after a counted attempt: a Destination that is Broken already keeps the error
// as its reason; one whose budget ran out becomes Broken as unavailable.
func (w *Worker) transient(ctx context.Context, q queries, org int64, d Destination, broken bool, attempts int64,
	firstFailed pgtype.Timestamptz, out Outcome, now time.Time, logs *after) error {
	text := failure(out, "transient error")
	switch {
	case broken:
		return breakDestination(ctx, q, w.raiser(org), org, d, causeUnavailable, text, now, logs)
	case exhausted(attempts, firstFailed, now):
		return breakDestination(ctx, q, w.raiser(org), org, d, causeUnavailable, repeatedPrefix+text, now, logs)
	}
	return nil
}

// notDelivered ends a delivery as Not delivered with the error, records the not_delivered delivery event and logs it
// once the transaction commits (C-11.FR-10, AC-8, AC-13).
func (w *Worker) notDelivered(ctx context.Context, q queries, org int64, a attempt, out Outcome, now time.Time,
	logs *after) (bool, error) {
	text := failure(out, unknownText)
	_, err := q.RecordNotDelivered(ctx, dbgen.RecordNotDeliveredParams{OrgID: org, ID: a.row.ID,
		Owner: w.Lease.Owner, ErrorClass: string(OutcomeUnknown), Error: text, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record the delivery as not delivered: %w", err)
	}
	if err := hintGroup(ctx, q, org, a.row.AlertGroupPublicID); err != nil {
		return false, err
	}
	if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: a.destination.ID, AlertGroupID: a.group(),
		StormID: a.storm(), Kind: EventNotDelivered, ErrorClass: string(OutcomeUnknown),
		Error: outbound.Untrusted(text)}); err != nil {
		return false, err
	}
	logs.add(notDeliveredLine(a.destination.PublicID, a.row.AlertGroupPublicID, a.kind()))
	return true, nil
}

// notDeliveredLine is the delivery_not_delivered line of a delivery or a Thread reply.
func notDeliveredLine(destination, group, kind string) func(ctx context.Context, log *logging.Logger) {
	return func(ctx context.Context, log *logging.Logger) {
		log.Log(ctx, logging.DeliveryNotDelivered, logging.F("destination", destination),
			logging.F("group", group), logging.F("kind", kind), logging.F("error_class", string(OutcomeUnknown)))
	}
}

// recordReply records the outcome of a Thread reply's call in the transaction of q, by the rules above.
func (w *Worker) recordReply(ctx context.Context, q queries, org int64, a replyAttempt, out Outcome,
	rejected *Outcome, logs *after) error {
	recorded, err := w.recordReplyOutcome(ctx, q, org, a, out, logs)
	if err != nil || !recorded || rejected == nil {
		return err
	}
	return RecordEvent(ctx, q, org, Event{At: w.Lease.Clocks.Business.Now().UTC(), DestinationID: a.destination.ID,
		AlertGroupID: &a.row.AlertGroupID, Kind: EventMarkupRejected, Error: rejected.Error,
		Detail: map[string]any{"kind": kindReply}})
}

func (w *Worker) recordReplyOutcome(ctx context.Context, q queries, org int64, a replyAttempt, out Outcome,
	logs *after) (bool, error) {
	now := w.Lease.Clocks.Business.Now().UTC()
	id := a.row.ID
	switch out.Kind {
	case OutcomeOK:
		if err := q.RecordReplySent(ctx, dbgen.RecordReplySentParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			MessageID: nonEmpty(out.MessageID), Now: now}); err != nil {
			return false, fmt.Errorf("record the sent thread reply: %w", err)
		}
		return true, nil
	case OutcomeRetryAfter:
		until := now.Add(out.RetryAfter)
		if _, ok, err := w.retryReply(ctx, q, org, id, until.Add(TokenMargin), false, out, now); err != nil || !ok {
			return ok, err
		}
		return true, holdBucket(ctx, q, org, a.destination, out, until)
	case OutcomeTransient:
		r, ok, err := w.retryReply(ctx, q, org, id, now.Add(w.backoff(a.row.Attempts+1)), true, out, now)
		if err != nil || !ok {
			return ok, err
		}
		return true, w.transient(ctx, q, org, a.destination, false, r.Attempts, r.FirstFailedAt, out, now, logs)
	case OutcomeFatal:
		if _, ok, err := w.retryReply(ctx, q, org, id, now, false, out, now); err != nil || !ok {
			return ok, err
		}
		return true, breakDestination(ctx, q, w.raiser(org), org, a.destination, causeFatal,
			failure(out, "fatal error"), now, logs)
	case OutcomeGone:
		// The reply waits for the Root message published again, or is dropped with the rest of its Thread.
		if err := q.RescheduleReply(ctx, dbgen.RescheduleReplyParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			At: now}); err != nil {
			return false, fmt.Errorf("release the thread reply: %w", err)
		}
		group := a.row.AlertGroupID
		return true, rootGone(ctx, q, org, rootOf{delivery: a.row.DeliveryID, group: &group,
			destination: a.destination.ID, groupPublicID: a.row.AlertGroupPublicID,
			resolved:    a.row.Status == string(groups.StatusResolved),
			republished: a.row.RepublishedAfterDelete}, now)
	case OutcomeThreadLost:
		_, ok, err := w.retryReply(ctx, q, org, id, now.Add(TransientFirstStep), false, out, now)
		return ok, err
	default:
		text := failure(out, unknownText)
		_, err := q.RecordReplyNotDelivered(ctx, dbgen.RecordReplyNotDeliveredParams{OrgID: org, ID: id,
			Owner: w.Lease.Owner, ErrorClass: string(OutcomeUnknown), Error: text})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("record the thread reply as not delivered: %w", err)
		}
		if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: a.destination.ID,
			AlertGroupID: &a.row.AlertGroupID, Kind: EventNotDelivered, ErrorClass: string(OutcomeUnknown),
			Error: outbound.Untrusted(text), Detail: map[string]any{"kind": kindReply, "event": a.row.Event}}); err != nil {
			return false, err
		}
		logs.add(notDeliveredLine(a.destination.PublicID, a.row.AlertGroupPublicID, kindReply))
		return true, nil
	}
}

// retryReply records an outcome of a Thread reply retried at at; ok is false when the lease went to another replica.
func (w *Worker) retryReply(ctx context.Context, q queries, org, id int64, at time.Time, counted bool, out Outcome,
	now time.Time) (dbgen.RecordReplyRetryRow, bool, error) {
	r, err := q.RecordReplyRetry(ctx, dbgen.RecordReplyRetryParams{OrgID: org, ID: id, Owner: w.Lease.Owner, At: at,
		Counted: counted, ErrorClass: nonEmpty(errorClass(out.Kind)), Error: nonEmpty(string(out.Error)), Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("record the %s outcome of the thread reply: %w", out.Kind, err)
	}
	return r, true, nil
}
