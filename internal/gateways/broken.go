// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// Broken Notification gateways (C-30.FR-8). A call that the gateway answers with a Fatal error, or that used up
// delivery.transient_budget, makes the gateway's Connection Broken once the call ended Not delivered, in a short
// transaction of its own that locks only the Connection's row: gateway_broken_since and gateway_broken_reason, the
// first probe at now plus delivery.broken_probe_interval, MusterNotificationGatewayBroken, the Connection's hint and
// the gateway_broken line once committed. A gateway that is Broken already keeps its state with the newer reason.
// While it is Broken, every call that comes due ends Not delivered with gateway_broken, unsent, and none is sent after
// it recovers: the end of the state is kept as gateway_recovered_at, and a call whose due time lies before it ends so
// too, which closes the moment when one replica prepares a call it claimed while another ends the state. The gateway
// sender of every replica probes the Broken gateways that are due through info, in the delivery client class, each
// claimed with FOR NO KEY UPDATE SKIP LOCKED and a lease on the real clock just before it runs, in a goroutine of its
// own, at most ProbeConcurrency at once, the next probe moved on by delivery.broken_probe_interval at the claim; a
// successful probe, or a successful check of the Connection,
// ends the state, resolves the alert and logs gateway_recovered, and a failed probe records its error as the reason.
// Only calls set the state: deliveries to the Destinations of the same bridge never do, and the state never touches
// them. The Leader exports muster_notification_gateway_broken (Export) and reconciles the state (Reconcile) in its task
// delivery_queue.
//
// Lock order. Every transaction of the Broken state — a Break, an end by a probe or a check, the record of a failed
// probe, the resolve of an orphan alert — takes the advisory lock of the gateway's state first (db.GatewayStateLockClass,
// keyed by the Connection's id), then the Connection's row, by a single update, then writes the Internal alert's
// synthetic Stored Snapshot, as a Broken Destination's row goes before its alert. They never queue for the row behind
// one another, so that none holds the row while it waits for its tuple lock behind another, a deadlock that
// TestIntegrationGatewayBrokenConcurrency met before the lock. No transaction that holds an Alert Group, the person's
// lock of calls or a call takes the Connection's row or the advisory lock, so the state adds no lock to the order of
// calls.go. A deletion or a rename of the Connection, which locks its row first, ends or renames the state in its own
// transaction without the advisory lock, so that no transaction takes the two in the other order.

// The causes of a Broken gateway, and how its state ended.
const (
	causeFatal       = "fatal"
	causeUnavailable = "unavailable"
	recoveredByProbe = "probe"
	recoveredByCheck = "check"
)

// repeatedPrefix starts the reason of a gateway made Broken by a call that used up its budget, as a Destination's.
const repeatedPrefix = "unavailable after repeated failures: "

// hintConnection is the live-update hint of a Connection (internal/connections).
const hintConnection = "connection"

// BrokenProbeInterval is delivery.broken_probe_interval: how often a Broken gateway is probed through info.
const BrokenProbeInterval = delivery.BrokenProbeInterval

// Prober probes the Bridge Connection connectionID of a Broken gateway through info, read afresh in the delivery
// client class; *connections.Service implements it.
type Prober interface {
	ProbeGateway(ctx context.Context, connectionID int64) delivery.Outcome
}

// BrokenQueries are the queries of Broken gateways, with the writes of Internal alerts and the hint of a Connection,
// in one transaction or alone.
type BrokenQueries interface {
	BreakGateway(ctx context.Context, arg dbgen.BreakGatewayParams) (dbgen.BreakGatewayRow, error)
	UpdateGatewayBrokenReason(ctx context.Context, arg dbgen.UpdateGatewayBrokenReasonParams) (int64, error)
	EndGatewayBroken(ctx context.Context, arg dbgen.EndGatewayBrokenParams) (dbgen.EndGatewayBrokenRow, error)
	ClaimGatewayProbes(ctx context.Context, arg dbgen.ClaimGatewayProbesParams) ([]dbgen.ClaimGatewayProbesRow,
		error)
	FailGatewayProbe(ctx context.Context, arg dbgen.FailGatewayProbeParams) (int64, error)
	NextGatewayProbe(ctx context.Context, arg dbgen.NextGatewayProbeParams) (dbgen.NextGatewayProbeRow, error)
	ListGatewayHealth(ctx context.Context, orgID int64) ([]dbgen.ListGatewayHealthRow, error)
	ListUnmarkedFailures(ctx context.Context, arg dbgen.ListUnmarkedFailuresParams) ([]dbgen.ListUnmarkedFailuresRow,
		error)
	LockGatewayState(ctx context.Context, arg dbgen.LockGatewayStateParams) error
	GetGatewayState(ctx context.Context, arg dbgen.GetGatewayStateParams) (dbgen.GetGatewayStateRow, error)
	Notify(ctx context.Context, h db.Hint) error
	internalalerts.Store
}

// BrokenStore runs the queries of Broken gateways over the main pool, alone or in a short transaction, and over the
// transaction of a caller.
type BrokenStore interface {
	BrokenQueries
	InBrokenTx(ctx context.Context, f func(q BrokenQueries) error) error
	Over(tx DBTX) BrokenQueries
}

// NewBrokenStore is the BrokenStore over the main pool.
func NewBrokenStore(p Pool) BrokenStore {
	return pgBrokenStore{pgBrokenQueries: brokenQueriesOver(p), pool: p}
}

type pgBrokenQueries struct {
	*dbgen.Queries
	internalalerts.Store
	d DBTX
}

func brokenQueriesOver(d DBTX) pgBrokenQueries {
	return pgBrokenQueries{Queries: dbgen.New(d), Store: internalalerts.NewStore(d), d: d}
}

func (q pgBrokenQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.d, h)
}

type pgBrokenStore struct {
	pgBrokenQueries
	pool Pool
}

func (s pgBrokenStore) InBrokenTx(ctx context.Context, f func(q BrokenQueries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(brokenQueriesOver(tx)) })
}

func (s pgBrokenStore) Over(tx DBTX) BrokenQueries { return brokenQueriesOver(tx) }

// Broken keeps the Broken state of the Notification gateways of the Organization OrgID.
type Broken struct {
	OrgID int64
	Store BrokenStore
	// Lease names this replica in the lease of a probe and carries the clocks: broken_since and the probes on the
	// business clock, the lease on the real clock.
	Lease db.Lease
	// Prober probes a Broken gateway through info: *connections.Service.
	Prober Prober
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL, the base of the runbook_url of MusterNotificationGatewayBroken.
	RunbookBase string
	Log         *logging.Logger

	once    sync.Once
	slots   chan struct{}
	probing sync.WaitGroup
	// freed runs when a probe frees its slot: the sender's Wake, which claims the due probes that waited for one.
	mu    sync.Mutex
	freed func()
}

// onFreed makes f run whenever a probe frees its slot; the sender passes its Wake.
func (b *Broken) onFreed(f func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.freed = f
}

// release frees the slot of a probe and wakes the sender.
func (b *Broken) release() {
	<-b.slots
	b.mu.Lock()
	f := b.freed
	b.mu.Unlock()
	if f != nil {
		f()
	}
}

// ProbeConcurrency is how many probes of Broken gateways one replica runs at once: a probe whose info hangs holds one
// of them, and never the calls of the sender or the other probes.
const ProbeConcurrency = 4

// ReconcileWindow is how far back the Leader's reconciliation looks for calls that failed without making their
// gateway Broken: twice delivery.broken_probe_interval, many runs of its Leader task, so that it never rescans the
// history of calls.
const ReconcileWindow = 2 * BrokenProbeInterval

// brokenGateway is the gateway of a call that makes it Broken: its Connection's id and public_id.
type brokenGateway struct {
	id       int64
	publicID string
}

func (b *Broken) raiser() *internalalerts.Raiser {
	return internalalerts.NewRaiser(b.OrgID, b.RunbookBase)
}

func (b *Broken) init() {
	b.once.Do(func() { b.slots = make(chan struct{}, ProbeConcurrency) })
}

func (b *Broken) now() time.Time { return b.Lease.Clocks.Business.Now().UTC() }

// lock takes the advisory lock of the Broken state of the gateway id in the transaction of q, first, before its
// Connection's row (Lock order).
func (b *Broken) lock(ctx context.Context, q BrokenQueries, id int64) error {
	if err := q.LockGatewayState(ctx, dbgen.LockGatewayStateParams{LockClass: db.GatewayStateLockClass,
		ID: id}); err != nil {
		return fmt.Errorf("lock the broken state of the gateway %d: %w", id, err)
	}
	return nil
}

// Break makes the gateway g Broken with cause and reason, the masked error it answered, in a transaction of its own:
// MusterNotificationGatewayBroken, the Connection's hint and gateway_broken once committed. A gateway that is Broken
// already stays so with reason as its reason, and its Connection's hint is sent; a deleted Connection changes nothing.
func (b *Broken) Break(ctx context.Context, g brokenGateway, cause, reason string) error {
	return b.breakAfter(ctx, g, cause, reason, pgtype.Timestamptz{})
}

// breakAfter is Break for a call that failed at failedAt, when valid: a gateway whose last Broken state ended since
// is not made Broken again for it.
func (b *Broken) breakAfter(ctx context.Context, g brokenGateway, cause, reason string, failedAt pgtype.Timestamptz) error {
	now := b.now()
	var row dbgen.BreakGatewayRow
	broke := false
	err := b.Store.InBrokenTx(ctx, func(q BrokenQueries) error {
		if err := b.lock(ctx, q, g.id); err != nil {
			return err
		}
		var err error
		row, err = q.BreakGateway(ctx, dbgen.BreakGatewayParams{OrgID: b.OrgID, ID: g.id, Reason: reason, Now: now,
			NextProbe: now.Add(BrokenProbeInterval), FailedAt: failedAt})
		broke = err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			n, err := q.UpdateGatewayBrokenReason(ctx, dbgen.UpdateGatewayBrokenReasonParams{OrgID: b.OrgID,
				ID: g.id, Reason: reason})
			if err != nil {
				return fmt.Errorf("record the reason of the broken gateway %s: %w", g.publicID, err)
			}
			if n == 0 {
				return nil
			}
			return q.Notify(ctx, db.Hint{OrgID: b.OrgID, Type: hintConnection, ID: g.publicID})
		}
		if err != nil {
			return fmt.Errorf("make the gateway %s broken: %w", g.publicID, err)
		}
		if err := b.raiser().Raise(ctx, q, now, internalalerts.NotificationGatewayBroken,
			internalalerts.Entity{ID: row.PublicID, Name: row.Name}, nil); err != nil {
			return fmt.Errorf("raise MusterNotificationGatewayBroken of %s: %w", row.PublicID, err)
		}
		return q.Notify(ctx, db.Hint{OrgID: b.OrgID, Type: hintConnection, ID: row.PublicID})
	})
	if err != nil {
		return err
	}
	if broke {
		b.Log.Log(ctx, logging.GatewayBroken, logging.F("connection", row.PublicID), logging.F("cause", cause),
			logging.F("reason", reason))
	}
	return nil
}

// end ends the Broken state of the gateway id through q at now, by a probe or a check, which the gateway keeps as the
// end of its last Broken state, so that a call that came due before it is never sent, also by a replica that claimed
// it while the state ended (P-68): the resolve of MusterNotificationGatewayBroken, and for a Connection that is not
// deleted its hint and the gateway_recovered line, returned to log once committed. With a valid owner only while that replica still holds the probe's lease. A gateway
// that is not Broken changes nothing.
func (b *Broken) end(ctx context.Context, q BrokenQueries, id int64, owner pgtype.Text, by string) (
	func(context.Context), error) {
	now := b.now()
	row, err := q.EndGatewayBroken(ctx, dbgen.EndGatewayBrokenParams{OrgID: b.OrgID, ID: id, Owner: owner,
		Now: b.Lease.Clocks.Real.Now().UTC(), RecoveredAt: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("end the broken state of the gateway %d: %w", id, err)
	}
	if err := b.raiser().Resolve(ctx, q, now, internalalerts.NotificationGatewayBroken, row.PublicID); err != nil {
		return nil, fmt.Errorf("resolve MusterNotificationGatewayBroken of %s: %w", row.PublicID, err)
	}
	if row.Deleted {
		return nil, nil
	}
	if err := q.Notify(ctx, db.Hint{OrgID: b.OrgID, Type: hintConnection, ID: row.PublicID}); err != nil {
		return nil, err
	}
	brokenFor := max(int64(now.Sub(row.WasBrokenSince).Seconds()), 0)
	return func(ctx context.Context) {
		b.Log.Log(ctx, logging.GatewayRecovered, logging.F("connection", row.PublicID), logging.F("by", by),
			logging.F("broken_for_s", brokenFor))
	}, nil
}

// EndBroken ends the Broken state of the gateway connectionID after a successful check of its Connection
// (C-30.FR-8), in a transaction of its own, logged once committed. A gateway that is not Broken changes nothing.
func (b *Broken) EndBroken(ctx context.Context, connectionID int64) error {
	var logged func(context.Context)
	if err := b.Store.InBrokenTx(ctx, func(q BrokenQueries) error {
		if err := b.lock(ctx, q, connectionID); err != nil {
			return err
		}
		var err error
		logged, err = b.end(ctx, q, connectionID, pgtype.Text{}, recoveredByCheck)
		return err
	}); err != nil {
		return err
	}
	if logged != nil {
		logged(ctx)
	}
	return nil
}

// Deleted ends the Broken state of the deleted Connection connectionID in the transaction tx of its deletion, which
// locked its row: MusterNotificationGatewayBroken resolves, and nothing is logged.
func (b *Broken) Deleted(ctx context.Context, tx DBTX, connectionID int64) error {
	_, err := b.end(ctx, b.Store.Over(tx), connectionID, pgtype.Text{}, "")
	return err
}

// Renamed raises an open MusterNotificationGatewayBroken of the Connection publicID again with its new name, in the
// transaction tx of the rename, as the same firing.
func (b *Broken) Renamed(ctx context.Context, tx DBTX, publicID, name string) error {
	if err := b.raiser().Renamed(ctx, b.Store.Over(tx), b.now(), internalalerts.EntityConnection, publicID,
		name); err != nil {
		return fmt.Errorf("rename the internal alerts of the connection %s: %w", publicID, err)
	}
	return nil
}

// Probe starts the probes of the Broken gateways that are due, each claimed, and so leased, just before it runs, in a
// goroutine of its own, at most ProbeConcurrency at once, and returns without waiting for them: a probe whose info
// hangs delays neither the calls of the sender nor the other probes. A failed claim is the round's error; a probe
// whose outcome could not be recorded is logged as gateway_probe_failed, and the gateway is probed again after
// delivery.broken_probe_interval.
func (b *Broken) Probe(ctx context.Context) error {
	b.init()
	for {
		select {
		case b.slots <- struct{}{}:
		default:
			return nil // every slot probes; a later round claims the rest
		}
		p := b.Lease.Params(1)
		rows, err := b.Store.ClaimGatewayProbes(ctx, dbgen.ClaimGatewayProbesParams{OrgID: b.OrgID, Owner: p.Owner,
			LeaseUntil: p.LeaseUntil, NextProbe: p.Due.Add(BrokenProbeInterval), Due: p.Due, Now: p.Now,
			Lim: p.Limit})
		if err != nil || len(rows) == 0 {
			<-b.slots
			if err != nil {
				return fmt.Errorf("claim the broken gateways to probe: %w", err)
			}
			return nil
		}
		r := rows[0]
		b.probing.Go(func() {
			defer b.release()
			if err := b.probeOne(ctx, r); err != nil && ctx.Err() == nil {
				b.Log.Log(ctx, logging.GatewayProbeFailed, logging.F("connection", r.PublicID),
					logging.F("error", err.Error()))
			}
		})
	}
}

// Wait waits until the probes that Probe started have ended.
func (b *Broken) Wait() { b.probing.Wait() }

// Reconcile brings the Broken state of the Notification gateways of the Organization in line with their calls and
// their Internal alerts, as part of the Leader task delivery_queue; running it twice changes nothing more. A gateway
// that is not Broken whose call failed in a way that breaks a gateway — a Fatal outcome that reached it, or a used
// budget — within ReconcileWindow and after its last Broken state ended is made Broken: the replica that ended the call
// stopped before it did so. An open MusterNotificationGatewayBroken of a Connection that is not Broken, or is deleted
// or gone, is resolved, as one that a down migration left behind; the Connection's row is locked first, so that a
// Break or an end of the state at the same moment is either before or after it.
func (b *Broken) Reconcile(ctx context.Context) error {
	now := b.now()
	rows, err := b.Store.ListUnmarkedFailures(ctx, dbgen.ListUnmarkedFailuresParams{OrgID: b.OrgID,
		Since: now.Add(-ReconcileWindow)})
	if err != nil {
		return fmt.Errorf("list the calls that failed through gateways in order: %w", err)
	}
	var errs []error
	for _, r := range rows {
		cause, reason := causeFatal, r.Error
		if reason == "" {
			reason = r.ErrorClass
		}
		if r.Reason == groups.PageCallBudgetUsed {
			cause, reason = causeUnavailable, repeatedPrefix+reason
		}
		errs = append(errs, b.breakAfter(ctx, brokenGateway{id: r.ID, publicID: r.PublicID}, cause, reason,
			pgtype.Timestamptz{Time: r.EndedAt, Valid: true}))
	}
	return errors.Join(append(errs, b.resolveOrphans(ctx))...)
}

// resolveOrphans resolves the open MusterNotificationGatewayBroken of the Connections that are not Broken.
func (b *Broken) resolveOrphans(ctx context.Context) error {
	d := internalalerts.NotificationGatewayBroken
	open, err := b.raiser().Open(ctx, b.Store, d)
	if err != nil {
		return fmt.Errorf("list the open MusterNotificationGatewayBroken: %w", err)
	}
	var errs []error
	for _, id := range slices.Sorted(maps.Keys(open)) {
		errs = append(errs, b.Store.InBrokenTx(ctx, func(q BrokenQueries) error {
			st, err := q.GetGatewayState(ctx, dbgen.GetGatewayStateParams{OrgID: b.OrgID, PublicID: id})
			switch {
			case errors.Is(err, pgx.ErrNoRows): // no Connection that a Break could take
			case err != nil:
				return fmt.Errorf("read the connection %s: %w", id, err)
			default:
				if err := b.lock(ctx, q, st.ID); err != nil {
					return err
				}
				// Read again under the lock: a Break or an end of the state is wholly before or after it.
				st, err = q.GetGatewayState(ctx, dbgen.GetGatewayStateParams{OrgID: b.OrgID, PublicID: id})
				if err != nil {
					return fmt.Errorf("read the connection %s: %w", id, err)
				}
				if st.Broken {
					return nil
				}
			}
			still, err := b.raiser().Open(ctx, q, d)
			if err != nil {
				return fmt.Errorf("list the open MusterNotificationGatewayBroken: %w", err)
			}
			if _, ok := still[id]; !ok {
				return nil
			}
			if err := b.raiser().Resolve(ctx, q, b.now(), d, id); err != nil {
				return fmt.Errorf("resolve MusterNotificationGatewayBroken of %s: %w", id, err)
			}
			return nil
		}))
	}
	return errors.Join(errs...)
}

// probeOne probes the claimed Broken gateway r through info, in a check run of its own recorded as probe
// (C-23.FR-2): a success ends its state, a failure records its error as the reason; either only while this replica
// still holds the probe's lease.
func (b *Broken) probeOne(ctx context.Context, r dbgen.ClaimGatewayProbesRow) error {
	ctx, _ = outbound.StartCheck(ctx, outbound.CheckProbe, 0)
	out := b.Prober.ProbeGateway(ctx, r.ID)
	owner := pgtype.Text{String: b.Lease.Owner, Valid: true}
	var logged func(context.Context)
	err := b.Store.InBrokenTx(ctx, func(q BrokenQueries) error {
		logged = nil
		if err := b.lock(ctx, q, r.ID); err != nil {
			return err
		}
		if out.Kind == delivery.OutcomeOK {
			var err error
			logged, err = b.end(ctx, q, r.ID, owner, recoveredByProbe)
			return err
		}
		n, err := q.FailGatewayProbe(ctx, dbgen.FailGatewayProbeParams{OrgID: b.OrgID, ID: r.ID,
			Reason: reasonOf(out), Owner: b.Lease.Owner, Now: b.Lease.Clocks.Real.Now().UTC()})
		if err != nil || n == 0 {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: b.OrgID, Type: hintConnection, ID: r.PublicID})
	})
	if err != nil {
		return fmt.Errorf("record the probe of the gateway %s: %w", r.PublicID, err)
	}
	if logged != nil {
		logged(ctx)
	}
	return nil
}

// Next is how long until the next probe of a Broken gateway can be claimed, false for none. While every slot probes,
// a due probe cannot be claimed before one is freed, which wakes the sender, so only the end of a live lease counts.
func (b *Broken) Next(ctx context.Context) (time.Duration, bool, error) {
	b.init()
	full := len(b.slots) == cap(b.slots)
	realNow := b.Lease.Clocks.Real.Now().UTC()
	r, err := b.Store.NextGatewayProbe(ctx, dbgen.NextGatewayProbeParams{OrgID: b.OrgID, Now: realNow})
	if err != nil {
		return 0, false, fmt.Errorf("read when the next probe of a gateway is due: %w", err)
	}
	var d time.Duration
	ok := false
	if r.FreeAt.Year() > 1 && !full {
		d, ok = r.FreeAt.Sub(b.now()), true
	}
	if r.LeaseEnd.Year() > 1 {
		if l := r.LeaseEnd.Sub(realNow); !ok || l < d {
			d, ok = l, true
		}
	}
	return d, ok, nil
}

// reasonOf is the masked error of an answer, or its class when it carries none.
func reasonOf(out delivery.Outcome) string {
	if out.Error == "" {
		return string(out.Kind)
	}
	return string(out.Error)
}

// gaugeSeries are the gateways whose muster_notification_gateway_broken series this replica exports.
var (
	gaugeMu     sync.Mutex
	gaugeSeries = map[string]bool{}
)

// Export sets muster_notification_gateway_broken to 1 for each Broken gateway of the Organization that is not deleted
// and 0 for the Notification gateways in order, as part of the Leader task delivery_queue; the series of a gateway that
// was deleted or is no Notification gateway any more is removed. Running it twice sets the same values.
func (b *Broken) Export(ctx context.Context) error {
	rows, err := b.Store.ListGatewayHealth(ctx, b.OrgID)
	if err != nil {
		return fmt.Errorf("read the state of the notification gateways: %w", err)
	}
	gaugeMu.Lock()
	defer gaugeMu.Unlock()
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[r.PublicID] = true
		gaugeSeries[r.PublicID] = true
		v := 0.0
		if r.Broken {
			v = 1
		}
		metrics.NotificationGatewayBroken.With(r.PublicID).Set(v)
	}
	for id := range gaugeSeries {
		if !seen[id] {
			metrics.NotificationGatewayBroken.Delete(id)
			delete(gaugeSeries, id)
		}
	}
	return nil
}
