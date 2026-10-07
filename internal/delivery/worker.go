// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// The kinds of an attempt, the kind label of muster_delivery_attempts_total.
const (
	kindPublication = "publication"
	kindUpdate      = "update"
	kindReply       = "thread_reply"
)

// Worker is the delivery worker of one replica (C-11.FR-1, FR-15; schema.md §5): it claims the due deliveries of
// healthy Destinations, Urgent first, and the due Thread replies with FOR UPDATE SKIP LOCKED and a lease, re-reads
// each in a short transaction, takes the limiter tokens, calls the adapter outside any transaction in the delivery
// client class, and records the outcome in another short transaction. It is woken by NOTIFY, by the earliest due
// time and, in development mode, by a move of the development clock.
type Worker struct {
	Store *Store
	// Lease names this replica in lease_owner and carries the clocks: due times and limiters on the business clock,
	// leases and the duration of calls on the real clock.
	Lease db.Lease
	// Organizations lists the Organizations whose deliveries it makes: one in L1.
	Organizations func(ctx context.Context) ([]int64, error)
	Adapters      Adapters
	Renderer      Renderer
	Log           *logging.Logger
	// MaxWait bounds the wait between rounds, and Batch the rows one claim takes; zero is the default.
	MaxWait time.Duration
	Batch   int32
	// Wait waits for d or until a wake arrives; nil waits on the real clock.
	Wait func(ctx context.Context, d time.Duration, wake <-chan struct{})

	once sync.Once
	wake chan struct{}
}

func (w *Worker) init() {
	w.once.Do(func() { w.wake = make(chan struct{}, 1) })
}

// Wake makes the worker run a round at once: a Desired state changed, a Thread reply was queued, the development
// clock moved, or the LISTEN is back.
func (w *Worker) Wake() {
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run delivers until ctx ends: a round, then a wait until the next row can be claimed — its due time on the business
// clock or the end of its lease on the real clock — between MinWait and MaxWait, or until woken; after a failed
// round, a backoff.
func (w *Worker) Run(ctx context.Context) {
	w.init()
	wait := w.Wait
	if wait == nil {
		wait = waitReal
	}
	failures := 0
	for ctx.Err() == nil {
		next, err := w.Round(ctx)
		d := max(next, MinWait)
		if err != nil {
			failures++
			d = db.Backoff(failures, FailureBackoff, w.maxWait(), rand.Float64)
		} else {
			failures = 0
		}
		wait(ctx, d, w.wake)
	}
}

func (w *Worker) maxWait() time.Duration {
	if w.MaxWait <= 0 {
		return MaxWait
	}
	return w.MaxWait
}

func (w *Worker) batch() int32 {
	if w.Batch <= 0 {
		return Batch
	}
	return w.Batch
}

func waitReal(ctx context.Context, d time.Duration, wake <-chan struct{}) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-t.C:
	}
}

// Round attempts every due delivery, then every due Thread reply, of every Organization, claiming Batch at a time,
// and returns how long until the next one can be claimed, at most MaxWait. A row that fails is logged and attempted
// again once its lease runs out; the round goes on.
func (w *Worker) Round(ctx context.Context) (time.Duration, error) {
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list the organizations for delivery: %w", err)
	}
	next := w.maxWait()
	var errs []error
	for _, org := range orgs {
		errs = append(errs, w.drain(ctx, org, w.claimDeliveries, w.deliver),
			w.drain(ctx, org, w.claimReplies, w.reply))
		n, err := w.next(ctx, org, next)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		next = min(next, n)
	}
	return next, errors.Join(errs...)
}

// drain claims Batch rows at a time with claim and works each with work until a claim comes back short.
func (w *Worker) drain(ctx context.Context, org int64,
	claim func(ctx context.Context, org int64) ([]int64, error), work func(ctx context.Context, org, id int64)) error {
	for {
		ids, err := claim(ctx, org)
		if err != nil {
			return err
		}
		for _, id := range ids {
			work(ctx, org, id)
		}
		if len(ids) < int(w.batch()) {
			return nil
		}
	}
}

// claimDeliveries leases the due deliveries of the Organization, Urgent first.
func (w *Worker) claimDeliveries(ctx context.Context, org int64) ([]int64, error) {
	rows, err := db.Claim(ctx, w.Store.begin, w.Lease, w.batch(),
		func(ctx context.Context, tx pgx.Tx, p db.ClaimParams) ([]dbgen.ClaimDueDeliveriesRow, error) {
			return w.Store.queries(tx).ClaimDueDeliveries(ctx, dbgen.ClaimDueDeliveriesParams{OrgID: org,
				Owner: p.Owner, LeaseUntil: p.LeaseUntil, Due: p.Due, Now: p.Now, Lim: p.Limit})
		})
	if err != nil {
		return nil, fmt.Errorf("claim the due deliveries: %w", err)
	}
	slices.SortFunc(rows, func(a, b dbgen.ClaimDueDeliveriesRow) int {
		if a.Urgent != b.Urgent {
			if a.Urgent {
				return -1
			}
			return 1
		}
		if c := a.NextAttemptAt.Compare(b.NextAttemptAt); c != 0 {
			return c
		}
		if a.LastDeliveredAt.Valid != b.LastDeliveredAt.Valid {
			if !a.LastDeliveredAt.Valid {
				return -1
			}
			return 1
		}
		return cmp.Or(a.LastDeliveredAt.Time.Compare(b.LastDeliveredAt.Time), cmp.Compare(a.ID, b.ID))
	})
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// next is how long until a delivery or a Thread reply of the Organization can be claimed, at most limit.
func (w *Worker) next(ctx context.Context, org int64, limit time.Duration) (time.Duration, error) {
	realNow := w.Lease.Clocks.Real.Now().UTC()
	r, err := w.Store.q().NextDeliveryWork(ctx, dbgen.NextDeliveryWorkParams{OrgID: org, Now: realNow})
	if err != nil {
		return 0, fmt.Errorf("read when the next delivery is due: %w", err)
	}
	d := limit
	if r.FreeAt.Year() > 1 {
		d = min(d, r.FreeAt.Sub(w.Lease.Clocks.Business.Now()))
	}
	if r.LeaseEnd.Year() > 1 {
		d = min(d, r.LeaseEnd.Sub(realNow))
	}
	return d, nil
}

// attempt is a delivery that has its tokens and is about to be called.
type attempt struct {
	row         dbgen.GetLeasedDeliveryRow
	destination Destination
	publication bool
	message     Message
	at          time.Time
}

// deliver attempts one claimed delivery; a failure is logged and the row is attempted again once its lease runs out.
func (w *Worker) deliver(ctx context.Context, org, id int64) {
	a, ok, err := w.prepare(ctx, org, id)
	if err == nil && ok {
		err = w.call(ctx, org, a)
	}
	if err != nil && ctx.Err() == nil {
		w.Log.Log(ctx, logging.DeliveryWorkFailed, logging.F("work", "delivery"), logging.F("error", err.Error()))
	}
}

// prepare re-reads a claimed delivery in a short transaction, while its lease is still held: a delivery whose actual
// message already shows the Desired state is delivered with no call; one without tokens waits until they are due plus
// TokenMargin; otherwise the tokens are taken, the lease is renewed for the call and, before a Publication, the start
// of the Publication is recorded. ok says whether to call.
func (w *Worker) prepare(ctx context.Context, org, id int64) (attempt, bool, error) {
	var a attempt
	ok := false
	err := w.Store.inTx(ctx, func(q queries) error {
		ok = false
		realNow := w.Lease.Clocks.Real.Now().UTC()
		row, err := q.GetLeasedDelivery(ctx, dbgen.GetLeasedDeliveryParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			Now: realNow})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the lease ran out: any replica claims it again
		}
		if err != nil {
			return fmt.Errorf("read the delivery: %w", err)
		}
		now := w.Lease.Clocks.Business.Now().UTC()
		if bytes.Equal(row.ActualHash, row.DesiredHash) {
			if err := q.MarkDelivered(ctx, dbgen.MarkDeliveredParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				Version: row.DesiredVersion, Now: now}); err != nil {
				return fmt.Errorf("mark the delivery delivered: %w", err)
			}
			return nil
		}
		d := Destination{ID: row.DestinationID, PublicID: row.DestinationPublicID, Name: row.DestinationName,
			Type: row.DestinationType, Connection: int8Of(row.ConnectionID)}
		t, err := takeTokens(ctx, q, org, subjectOf(d), now)
		if err != nil {
			return err
		}
		if !t.ok {
			if err := q.RescheduleDelivery(ctx, dbgen.RescheduleDeliveryParams{OrgID: org, ID: id,
				Owner: w.Lease.Owner, At: t.due.Add(TokenMargin), Now: now}); err != nil {
				return fmt.Errorf("reschedule the delivery: %w", err)
			}
			return nil
		}
		msg, err := decode(row.DesiredPayload)
		if err != nil {
			return err
		}
		a = attempt{row: row, destination: d, publication: !row.MessageID.Valid, message: msg, at: now}
		if err := q.RenewDeliveryLease(ctx, dbgen.RenewDeliveryLeaseParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			LeaseUntil: realNow.Add(w.Lease.Duration)}); err != nil {
			return fmt.Errorf("renew the lease of the delivery: %w", err)
		}
		if a.publication {
			if err := q.StartPublication(ctx, dbgen.StartPublicationParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				Now: now}); err != nil {
				return fmt.Errorf("record the start of the publication: %w", err)
			}
		}
		ok = true
		return nil
	})
	return a, ok, err
}

// publicationLoudness is the loudness and Mentions of a first Publication: Loud with new_alert_group when it comes
// from `created`; S-035 decides the other cases, Quiet until then.
func publicationLoudness(loud pgtype.Bool) (groups.Loudness, []groups.Mention) {
	if loud.Valid && loud.Bool {
		return groups.Loud, []groups.Mention{groups.MentionNewAlertGroup}
	}
	return groups.Quiet, nil
}

// call makes the adapter call of a prepared delivery outside any transaction and records its outcome.
func (w *Worker) call(ctx context.Context, org int64, a attempt) error {
	c := Call{Class: outbound.ClassDelivery, Destination: a.destination, Loudness: groups.Quiet}
	kind := kindUpdate
	if a.publication {
		kind = kindPublication
		c.Loudness, c.Mentions = publicationLoudness(a.row.PublicationLoud)
	}
	start := w.Lease.Clocks.Real.Now()
	var out Outcome
	switch adapter := w.Adapters[a.destination.Type]; {
	case adapter == nil:
		out = Outcome{Kind: OutcomeUnknown, Error: outbound.Untrusted("no adapter for the destination type " +
			a.destination.Type)}
	case a.publication:
		out = adapter.Publish(ctx, c, a.message)
	default:
		out = adapter.Update(ctx, c, a.row.MessageID.String, a.message)
	}
	took := w.Lease.Clocks.Real.Now().Sub(start)
	recorded := false
	err := w.Store.inTx(ctx, func(q queries) error {
		var err error
		recorded, err = w.record(ctx, q, org, a, c, out)
		return err
	})
	w.attempted(ctx, a.destination, a.row.Number, kind, out, a.row.Attempts+1, took)
	if err == nil && recorded && out.Kind == OutcomeOK && a.row.DesiredReceivedAt.Valid {
		metrics.DeliveryLatency.With(a.destination.PublicID).Update(
			max(a.at.Sub(a.row.DesiredReceivedAt.Time).Seconds(), 0))
	}
	return err
}

// record records the outcome of a delivery's call in the transaction of q; recorded is false when the lease went to
// another replica meanwhile.
func (w *Worker) record(ctx context.Context, q queries, org int64, a attempt, c Call, out Outcome) (bool, error) {
	now := w.Lease.Clocks.Business.Now().UTC()
	id := a.row.ID
	switch out.Kind {
	case OutcomeOK:
		_, err := q.RecordDelivered(ctx, dbgen.RecordDeliveredParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			Version: a.row.DesiredVersion, Hash: a.row.DesiredHash, MessageID: nonEmpty(out.MessageID),
			MessageUrl: nonEmpty(out.MessageURL), Published: a.publication, Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("record the delivered message: %w", err)
		}
		if a.publication {
			return true, RecordEvent(ctx, q, org, Event{At: now, DestinationID: a.destination.ID,
				AlertGroupID: &a.row.AlertGroupID, Kind: EventPublication, Loudness: c.Loudness,
				Mentions: c.Mentions})
		}
		return true, nil
	case OutcomeRetryAfter:
		until := now.Add(out.RetryAfter)
		if err := q.RecordDeliveryRetry(ctx, dbgen.RecordDeliveryRetryParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			At: until.Add(TokenMargin), ErrorClass: nonEmpty(errorClass(out.Kind)), Error: nonEmpty(string(out.Error)),
			Now: now}); err != nil {
			return false, fmt.Errorf("record the retry after: %w", err)
		}
		return true, holdBucket(ctx, q, org, a.destination, out, until)
	default:
		if err := q.RecordDeliveryRetry(ctx, dbgen.RecordDeliveryRetryParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			At: now.Add(TransientFirstStep), ErrorClass: nonEmpty(errorClass(out.Kind)),
			Error: nonEmpty(string(out.Error)), Now: now}); err != nil {
			return false, fmt.Errorf("record the failed call: %w", err)
		}
		return true, nil
	}
}

// attempted counts an attempt and writes its delivery_attempt line.
func (w *Worker) attempted(ctx context.Context, d Destination, number int64, kind string, out Outcome, n int64,
	took time.Duration) {
	if o := metricOutcome(out.Kind); o != "" {
		metrics.DeliveryAttempts.With(d.PublicID, kind, o).Inc()
	}
	outcome := string(out.Kind)
	if out.Kind == OutcomeOK {
		outcome = "delivered"
	}
	var retry int64
	if out.Kind == OutcomeRetryAfter {
		retry = out.RetryAfter.Milliseconds()
	}
	w.Log.Log(ctx, logging.DeliveryAttempt, logging.F("destination", d.PublicID),
		logging.F("group", "#"+strconv.FormatInt(number, 10)), logging.F("kind", kind), logging.F("outcome", outcome),
		logging.F("attempt", n), logging.F("duration_ms", took.Milliseconds()), logging.F("retry_after_ms", retry))
}
