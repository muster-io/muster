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
	kindPublication  = "publication"
	kindUpdate       = "update"
	kindReply        = "thread_reply"
	kindStormSummary = "storm_summary"
	kindFinalEdit    = "final_edit"
)

// Worker is the delivery worker of one replica (C-11.FR-1, FR-15; schema.md §5): it probes the Broken Destinations
// that are due, claims the due deliveries of healthy Destinations, Urgent first, and the due Thread replies with FOR
// UPDATE SKIP LOCKED and a lease, re-reads each in a short transaction, takes the limiter tokens, calls the adapter
// outside any transaction in the delivery client class, and records the outcome by its rule (outcomes.go) in another
// short transaction. It is woken by NOTIFY, by the earliest due time and, in development mode, by a move of the
// development clock.
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
	// Random is uniform in [0, 1) and jitters delivery.transient_backoff; nil is math/rand/v2.
	Random func() float64
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL, the base of the runbook_url of MusterDestinationBroken.
	RunbookBase string
	// PublicURL is MUSTER_PUBLIC_URL, the base of the link to an Alert Group in a final edit.
	PublicURL string

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

// Round probes every Broken Destination whose probe is due, then attempts every due delivery, then every due Thread
// reply, of every Organization, claiming Batch at a time, and returns how long until the next one can be claimed, at
// most MaxWait. A row that fails is logged and attempted again once its lease runs out; the round goes on.
func (w *Worker) Round(ctx context.Context) (time.Duration, error) {
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list the organizations for delivery: %w", err)
	}
	next := w.maxWait()
	var errs []error
	for _, org := range orgs {
		errs = append(errs, w.probe(ctx, org), w.drain(ctx, org, w.claimDeliveries, w.deliver),
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

// attempt is a delivery that has its tokens and is about to be called; late says that its Publication carries the
// late note.
type attempt struct {
	row         dbgen.GetLeasedDeliveryRow
	destination Destination
	publication bool
	late        bool
	message     Message
	at          time.Time
}

// kind is the kind of the attempt's call: the final edit, a call of a Storm summary, a Publication or an edit.
func (a attempt) kind() string {
	switch {
	case a.row.DesiredRetire:
		return kindFinalEdit
	case a.row.StormID.Valid:
		return kindStormSummary
	case a.publication:
		return kindPublication
	}
	return kindUpdate
}

// group is the Alert Group a delivery event of the attempt concerns, nil for a Storm summary.
func (a attempt) group() *int64 { return groupOf(a.row) }

// storm is the Storm of a Storm summary, nil for an Alert Group.
func (a attempt) storm() *int64 { return int8Of(a.row.StormID) }

// groupOf is the Alert Group of a leased delivery, nil for a Storm summary.
func groupOf(row dbgen.GetLeasedDeliveryRow) *int64 {
	if row.StormID.Valid {
		return nil
	}
	id := row.AlertGroupID
	return &id
}

// deliver attempts one claimed delivery; a failure is logged and the row is attempted again once its lease runs out.
func (w *Worker) deliver(ctx context.Context, org, id int64) {
	a, ok, logs, err := w.prepare(ctx, org, id)
	if err == nil {
		logs.run(ctx, w.Log)
	}
	if err == nil && ok {
		err = w.call(ctx, org, a)
	}
	if err != nil && ctx.Err() == nil {
		w.Log.Log(ctx, logging.DeliveryWorkFailed, logging.F("work", "delivery"), logging.F("error", err.Error()))
	}
}

// prepare re-reads a claimed delivery in a short transaction, while its lease is still held: a delivery whose actual
// message already shows the Desired state is delivered with no call; one without tokens waits until they are due plus
// TokenMargin; otherwise the tokens are taken, the lease is renewed for the call and, before a Publication, a possible
// duplicate and the start of the Publication are recorded and its notes added. ok says whether to call; logs are the
// lines to log now that it committed.
func (w *Worker) prepare(ctx context.Context, org, id int64) (attempt, bool, after, error) {
	var a attempt
	var logs after
	ok := false
	err := w.Store.inTx(ctx, func(q queries) error {
		ok, logs = false, nil
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
		if row.DesiredRetire && !row.MessageID.Valid {
			// Its Destination left before its Root message was ever published there.
			if err := q.WithholdLeased(ctx, dbgen.WithholdLeasedParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				Now: now}); err != nil {
				return fmt.Errorf("withhold the delivery: %w", err)
			}
			return wipeSecrets(ctx, q, org, row.DestinationID)
		}
		if !row.DesiredRetire && bytes.Equal(row.ActualHash, row.DesiredHash) {
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
		if row.DesiredRetire {
			msg = finalEdit(msg, w.link(row.AlertGroupPublicID))
		}
		a = attempt{row: row, destination: d, publication: !row.MessageID.Valid, message: msg, at: now}
		if err := q.RenewDeliveryLease(ctx, dbgen.RenewDeliveryLeaseParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			LeaseUntil: realNow.Add(w.Lease.Duration)}); err != nil {
			return fmt.Errorf("renew the lease of the delivery: %w", err)
		}
		if a.publication {
			if row.PublicationStartedAt.Valid {
				if err := w.possibleDuplicate(ctx, q, org, row, now, &logs); err != nil {
					return err
				}
			}
			if err := q.StartPublication(ctx, dbgen.StartPublicationParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				Now: now}); err != nil {
				return fmt.Errorf("record the start of the publication: %w", err)
			}
			if a.message, a.late, err = publicationNotes(ctx, q, org, row, a.message); err != nil {
				return err
			}
		}
		ok = true
		return nil
	})
	return a, ok, logs, err
}

// publicationLoudness is the loudness and Mentions of a first Publication: Loud with new_alert_group when it comes
// from `created`, a recovery finds its Alert Group firing or it is a Storm summary; Quiet otherwise — in a Destination
// added to the Route, after a Storm, late, published again after a deletion, or recovered while not firing
// (C-11.FR-6, FR-11, FR-13, FR-14, FR-19).
func publicationLoudness(loud pgtype.Bool, summary bool) (groups.Loudness, []groups.Mention) {
	switch {
	case summary && loud.Valid && loud.Bool:
		r := deliveryEvent(DeliveryStormSummary)
		return r.Loudness, r.Mentions
	case loud.Valid && loud.Bool:
		r := deliveryEvent(DeliveryAfterRecovery)
		return r.Loudness, r.Mentions
	}
	return groups.Quiet, nil
}

// call makes the adapter call of a prepared delivery outside any transaction and records its outcome; a markup the
// messenger rejects is sent again without markup in the same attempt.
func (w *Worker) call(ctx context.Context, org int64, a attempt) error {
	c := Call{Class: outbound.ClassDelivery, Destination: a.destination, Loudness: groups.Quiet}
	kind := a.kind()
	if a.publication {
		c.Loudness, c.Mentions = publicationLoudness(a.row.PublicationLoud, a.row.StormID.Valid)
	}
	start := w.Lease.Clocks.Real.Now()
	out := w.send(ctx, a, c)
	var rejected *Outcome
	if out.Kind == OutcomeMarkupRejected {
		first := out
		rejected = &first
		w.attempted(ctx, a.destination, a.row.Number, kind, out, a.row.Attempts+1, w.Lease.Clocks.Real.Now().Sub(start))
		c.Plain = true
		out = plain(func() Outcome { return w.send(ctx, a, c) })
	}
	took := w.Lease.Clocks.Real.Now().Sub(start)
	recorded := false
	var logs after
	err := w.Store.inTx(ctx, func(q queries) error {
		logs = nil
		var err error
		recorded, err = w.record(ctx, q, org, a, c, out, rejected, &logs)
		return err
	})
	w.attempted(ctx, a.destination, a.row.Number, kind, out, a.row.Attempts+1, took)
	if err == nil {
		logs.run(ctx, w.Log)
	}
	if err == nil && recorded && out.Kind == OutcomeOK && a.row.DesiredReceivedAt.Valid {
		metrics.DeliveryLatency.With(a.destination.PublicID).Update(
			max(a.at.Sub(a.row.DesiredReceivedAt.Time).Seconds(), 0))
	}
	return err
}

// send is one adapter call of a prepared delivery: a Publication or an edit; a type no adapter serves is an unknown
// response (D277).
func (w *Worker) send(ctx context.Context, a attempt, c Call) Outcome {
	switch adapter := w.Adapters[a.destination.Type]; {
	case adapter == nil:
		return Outcome{Kind: OutcomeUnknown, Error: outbound.Untrusted("no adapter for the destination type " +
			a.destination.Type)}
	case a.publication:
		return adapter.Publish(ctx, c, a.message)
	default:
		return adapter.Update(ctx, c, a.row.MessageID.String, a.message)
	}
}

// attempted counts an attempt and writes its delivery_attempt line; a Storm summary, number 0, names no Alert Group.
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
	group := ""
	if number > 0 {
		group = "#" + strconv.FormatInt(number, 10)
	}
	w.Log.Log(ctx, logging.DeliveryAttempt, logging.F("destination", d.PublicID), logging.F("group", group), logging.F("kind", kind), logging.F("outcome", outcome),
		logging.F("attempt", n), logging.F("duration_ms", took.Milliseconds()), logging.F("retry_after_ms", retry))
}
