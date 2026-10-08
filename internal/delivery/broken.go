// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// Broken Destinations (C-11.FR-9, FR-18): a Fatal error, or a Transient budget that ran out, makes a Destination
// Broken in the transaction that records the outcome — health, broken_since, broken_cause, broken_reason and the first
// probe at now plus delivery.broken_probe_interval, with the destination_broken delivery event, MusterDestinationBroken
// and the hint destination. Its deliveries wait: the claims skip them, new Thread replies are dropped and a first
// Publication whose Alert Group resolves is withheld. Every replica probes the Broken Destinations that are due, in the
// delivery client class: the oldest waiting delivery through the worker's path, else the adapter's Destination check,
// else, for a type without one, a mark that makes its next due delivery the probe. A success makes it healthy and
// starts the recovery.

// The health of a Destination and the causes of a Broken one.
const (
	healthHealthy    = "healthy"
	healthBroken     = "broken"
	causeFatal       = "fatal"
	causeUnavailable = "unavailable"
)

// hintDestination is the live-update hint of a Destination.
const hintDestination = "destination"

// brokenQueries are the queries of Broken Destinations, their probe and their recovery.
type brokenQueries interface {
	ClaimBrokenProbes(ctx context.Context, arg dbgen.ClaimBrokenProbesParams) ([]dbgen.ClaimBrokenProbesRow, error)
	LeaseOldestWaiting(ctx context.Context, arg dbgen.LeaseOldestWaitingParams) (dbgen.LeaseOldestWaitingRow, error)
	MarkProbeOnNextDelivery(ctx context.Context, arg dbgen.MarkProbeOnNextDeliveryParams) error
	BreakDestination(ctx context.Context, arg dbgen.BreakDestinationParams) (dbgen.BreakDestinationRow, error)
	UpdateBrokenReason(ctx context.Context, arg dbgen.UpdateBrokenReasonParams) error
	MarkDestinationHealthy(ctx context.Context, arg dbgen.MarkDestinationHealthyParams) (
		dbgen.MarkDestinationHealthyRow, error)
	ListDestinationHealth(ctx context.Context, orgID int64) ([]dbgen.ListDestinationHealthRow, error)
	recoveryQueries
}

// breakDestination makes d Broken with cause and reason in the transaction of q: the delivery event, the Internal
// alert, the hint and the destination_broken line once committed. A Destination that is Broken already stays so with
// reason as its new reason and cause as its cause. It touches no other delivery: replicas that record outcomes of the
// same Destination at once each hold their own row and wait only for the Destination's; its rows get a fresh Transient
// budget in the recovery instead.
func breakDestination(ctx context.Context, q queries, raiser *internalalerts.Raiser, org int64, d Destination, cause,
	reason string, now time.Time, logs *after) error {
	row, err := q.BreakDestination(ctx, dbgen.BreakDestinationParams{OrgID: org, ID: d.ID, Cause: cause,
		Reason: reason, Now: now, NextProbe: now.Add(BrokenProbeInterval)})
	if errors.Is(err, pgx.ErrNoRows) {
		if err := q.UpdateBrokenReason(ctx, dbgen.UpdateBrokenReasonParams{OrgID: org, ID: d.ID, Reason: reason,
			Cause: nonEmpty(cause)}); err != nil {
			return fmt.Errorf("record the reason of broken destination %s: %w", d.PublicID, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("make destination %s broken: %w", d.PublicID, err)
	}
	class := string(OutcomeTransient)
	if cause == causeFatal {
		class = string(OutcomeFatal)
	}
	if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: d.ID, Kind: EventDestinationBroken,
		ErrorClass: class, Error: outbound.Untrusted(reason), Detail: map[string]any{"cause": cause}}); err != nil {
		return err
	}
	if err := raiser.Raise(ctx, q, now, internalalerts.DestinationBroken,
		internalalerts.Entity{ID: row.PublicID, Name: row.Name}, nil); err != nil {
		return fmt.Errorf("raise MusterDestinationBroken of %s: %w", row.PublicID, err)
	}
	if err := q.Notify(ctx, db.Hint{OrgID: org, Type: hintDestination, ID: row.PublicID}); err != nil {
		return err
	}
	logs.add(func(ctx context.Context, log *logging.Logger) {
		log.Log(ctx, logging.DestinationBroken, logging.F("destination", row.PublicID), logging.F("cause", cause),
			logging.F("reason", reason))
	})
	return nil
}

// markHealthy ends the Broken state of the Destination id in the transaction of q: the delivery event, the resolve of
// MusterDestinationBroken, the hint, the recovery to the current state and the wake of the delivery workers, and the
// destination_recovered line once committed. A healthy Destination changes nothing. A deleted one, whose last final
// edits a probe reached, only resolves the Internal alert and recovers its final edits: it records no delivery event,
// sends no hint and logs nothing.
func markHealthy(ctx context.Context, q queries, raiser *internalalerts.Raiser, org, id int64, now time.Time,
	logs *after) error {
	row, err := q.MarkDestinationHealthy(ctx, dbgen.MarkDestinationHealthyParams{OrgID: org, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("make destination %d healthy: %w", id, err)
	}
	if !row.Deleted {
		if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: id,
			Kind: EventDestinationRecovered}); err != nil {
			return err
		}
	}
	if err := raiser.Resolve(ctx, q, now, internalalerts.DestinationBroken, row.PublicID); err != nil {
		return fmt.Errorf("resolve MusterDestinationBroken of %s: %w", row.PublicID, err)
	}
	if !row.Deleted {
		if err := q.Notify(ctx, db.Hint{OrgID: org, Type: hintDestination, ID: row.PublicID}); err != nil {
			return err
		}
	}
	if err := recoverDestination(ctx, q, org, id, now); err != nil {
		return fmt.Errorf("recover destination %s: %w", row.PublicID, err)
	}
	if err := q.NotifyDelivery(ctx, Channel); err != nil {
		return fmt.Errorf("wake the delivery workers: %w", err)
	}
	if row.Deleted {
		return nil
	}
	brokenFor := max(int64(now.Sub(row.WasBrokenSince).Seconds()), 0)
	logs.add(func(ctx context.Context, log *logging.Logger) {
		log.Log(ctx, logging.DestinationRecovered, logging.F("destination", row.PublicID),
			logging.F("broken_for_s", brokenFor))
	})
	return nil
}

// MarkHealthy ends the Broken state of the Destination destinationID after a successful Destination check started by
// a person (S-039, S-042) or a successful Destination test (S-047), as a successful probe does: in the caller's
// transaction tx, whose commit the caller follows with the returned function, which logs; with a nil tx in a
// transaction of its own, logged once committed. A healthy Destination changes nothing.
func (s *Service) MarkHealthy(ctx context.Context, tx dbgen.DBTX, destinationID int64) (func(context.Context),
	error) {
	var logs after
	mark := func(q queries) error {
		logs = nil
		return markHealthy(ctx, q, s.internal, s.orgID, destinationID, s.clock.Now().UTC(), &logs)
	}
	committed := func(ctx context.Context) { logs.run(ctx, s.log) }
	if tx != nil {
		if err := mark(s.store.queries(tx)); err != nil {
			return nil, err
		}
		return committed, nil
	}
	if err := s.store.inTx(ctx, mark); err != nil {
		return nil, err
	}
	committed(ctx)
	return committed, nil
}

// EndBroken is MarkHealthy in a transaction of its own, for a successful Destination check started by a person
// (C-13.FR-10): its log line follows the commit.
func (s *Service) EndBroken(ctx context.Context, destinationID int64) error {
	_, err := s.MarkHealthy(ctx, nil, destinationID)
	return err
}

// probe claims the Broken Destinations of the Organization whose probe is due, Batch at a time, and probes each.
func (w *Worker) probe(ctx context.Context, org int64) error {
	for {
		rows, err := db.Claim(ctx, w.Store.begin, w.Lease, w.batch(),
			func(ctx context.Context, tx pgx.Tx, p db.ClaimParams) ([]dbgen.ClaimBrokenProbesRow, error) {
				return w.Store.queries(tx).ClaimBrokenProbes(ctx, dbgen.ClaimBrokenProbesParams{OrgID: org,
					Due: p.Due, Now: p.Now, NextProbe: p.Due.Add(BrokenProbeInterval), Lim: p.Limit})
			})
		if err != nil {
			return fmt.Errorf("claim the broken destinations to probe: %w", err)
		}
		for _, r := range rows {
			d := Destination{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Type: r.Type,
				Connection: int8Of(r.ConnectionID)}
			if err := w.probeOne(ctx, org, d); err != nil && ctx.Err() == nil {
				w.Log.Log(ctx, logging.DeliveryWorkFailed, logging.F("work", "probe"), logging.F("error", err.Error()))
			}
		}
		if len(rows) < int(w.batch()) {
			return nil
		}
	}
}

// probeOne probes the claimed Broken Destination d: it attempts its oldest waiting delivery through the worker's path;
// with nothing waiting it runs the adapter's Destination check in the delivery client class; for a type without one
// it marks d so that its next due delivery is the probe.
func (w *Worker) probeOne(ctx context.Context, org int64, d Destination) error {
	var waiting dbgen.LeaseOldestWaitingRow
	err := w.Store.inTx(ctx, func(q queries) error {
		realNow := w.Lease.Clocks.Real.Now().UTC()
		var err error
		waiting, err = q.LeaseOldestWaiting(ctx, dbgen.LeaseOldestWaitingParams{OrgID: org, DestinationID: d.ID,
			Owner: w.Lease.Owner, LeaseUntil: realNow.Add(w.Lease.Duration), Now: realNow})
		if errors.Is(err, pgx.ErrNoRows) {
			waiting = dbgen.LeaseOldestWaitingRow{}
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("lease the oldest waiting delivery of %s: %w", d.PublicID, err)
	}
	switch {
	case waiting.ID != 0 && waiting.Free:
		w.deliver(ctx, org, waiting.ID)
		return nil
	case waiting.ID != 0:
		return nil // another replica holds it; the next probe tries again
	}
	checker, ok := w.Adapters[d.Type].(Checker)
	if !ok {
		return w.Store.inTx(ctx, func(q queries) error {
			return q.MarkProbeOnNextDelivery(ctx, dbgen.MarkProbeOnNextDeliveryParams{OrgID: org, ID: d.ID})
		})
	}
	out := checker.Check(ctx, Call{Class: outbound.ClassDelivery, Destination: d})
	var logs after
	err = w.Store.inTx(ctx, func(q queries) error {
		logs = nil
		now := w.Lease.Clocks.Business.Now().UTC()
		if out.Kind == OutcomeOK {
			return markHealthy(ctx, q, w.raiser(org), org, d.ID, now, &logs)
		}
		return q.UpdateBrokenReason(ctx, dbgen.UpdateBrokenReasonParams{OrgID: org, ID: d.ID,
			Reason: failure(out, string(out.Kind)), Cause: pgtype.Text{}})
	})
	if err != nil {
		return fmt.Errorf("record the probe of %s: %w", d.PublicID, err)
	}
	logs.run(ctx, w.Log)
	return nil
}
