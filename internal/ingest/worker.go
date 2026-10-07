// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// The pace of the processing worker (C-06.FR-1).
const (
	// Poll is how often the worker looks for pending Stored Snapshots when no notification woke it.
	Poll = 5 * time.Second
	// Lease is how long a claim holds an Integration on the real clock; processing renews it with every Snapshot.
	Lease = 30 * time.Second
	// Concurrency is how many Integrations one replica processes at the same time, so that a backlog of one never
	// holds up the others.
	Concurrency = 4
	// maxBackoff bounds the wait after failed rounds.
	maxBackoff = time.Minute
	// releaseTimeout bounds the release of a lease at shutdown.
	releaseTimeout = 5 * time.Second
	// maxErrorLength bounds the processing_error that is stored.
	maxErrorLength = 2000
	// maxClaim bounds the Integrations one claim leases.
	maxClaim = 1000
)

// errLeaseLost is a lease another replica took over; this replica stops processing the Integration.
var errLeaseLost = errors.New("the lease of the integration went to another replica")

// ProcessQueries are the queries of processing and of the Alerts view; *dbgen.Queries implements them.
type ProcessQueries interface {
	GetRetention(ctx context.Context, orgID int64) (int64, error)
	ListPendingIntegrations(ctx context.Context, arg dbgen.ListPendingIntegrationsParams) ([]int64, error)
	EnsureIngestClaims(ctx context.Context, arg dbgen.EnsureIngestClaimsParams) error
	ReleaseIngestClaim(ctx context.Context, arg dbgen.ReleaseIngestClaimParams) error
	RenewIngestLease(ctx context.Context, arg dbgen.RenewIngestLeaseParams) (dbgen.RenewIngestLeaseRow, error)
	NextPendingSnapshot(ctx context.Context, arg dbgen.NextPendingSnapshotParams) (dbgen.NextPendingSnapshotRow, error)
	FinishSnapshot(ctx context.Context, arg dbgen.FinishSnapshotParams) (bool, error)
	CountSnapshot(ctx context.Context, arg dbgen.CountSnapshotParams) error
	UpsertAlertmanagerRoute(ctx context.Context, arg dbgen.UpsertAlertmanagerRouteParams) (
		dbgen.UpsertAlertmanagerRouteRow, error)
	UpsertAlertmanagerGroup(ctx context.Context, arg dbgen.UpsertAlertmanagerGroupParams) (
		dbgen.UpsertAlertmanagerGroupRow, error)
	UpdateAlertmanagerGroup(ctx context.Context, arg dbgen.UpdateAlertmanagerGroupParams) error
	UpdateRepeatInterval(ctx context.Context, arg dbgen.UpdateRepeatIntervalParams) error
	ListSnapshotAlerts(ctx context.Context, arg dbgen.ListSnapshotAlertsParams) ([]dbgen.ListSnapshotAlertsRow, error)
	ListActivePresences(ctx context.Context, arg dbgen.ListActivePresencesParams) ([]dbgen.ListActivePresencesRow,
		error)
	InsertAlerts(ctx context.Context, arg dbgen.InsertAlertsParams) ([]dbgen.InsertAlertsRow, error)
	UpdateAlerts(ctx context.Context, arg dbgen.UpdateAlertsParams) error
	UpsertListedPresences(ctx context.Context, arg dbgen.UpsertListedPresencesParams) error
	MarkPresencesMissed(ctx context.Context, arg dbgen.MarkPresencesMissedParams) error
	EndPresences(ctx context.Context, arg dbgen.EndPresencesParams) error
	CountPendingSnapshots(ctx context.Context, orgID int64) (int64, error)
	CountTruncatedGroups(ctx context.Context, arg dbgen.CountTruncatedGroupsParams) (int64, error)
	ResolveIntegrationAlerts(ctx context.Context, arg dbgen.ResolveIntegrationAlertsParams) (
		[]dbgen.ResolveIntegrationAlertsRow, error)
	ClearTruncation(ctx context.Context, arg dbgen.ClearTruncationParams) (int64, error)
	LockIntegrationName(ctx context.Context, arg dbgen.LockIntegrationNameParams) (string, error)
	ListLiveIntegrations(ctx context.Context, orgID int64) ([]int64, error)
	HasPendingSnapshots(ctx context.Context, arg dbgen.HasPendingSnapshotsParams) (bool, error)
	ListStalePresences(ctx context.Context, arg dbgen.ListStalePresencesParams) ([]dbgen.ListStalePresencesRow, error)
	MarkPresencesStale(ctx context.Context, arg dbgen.MarkPresencesStaleParams) error
	ResolveStaleAlerts(ctx context.Context, arg dbgen.ResolveStaleAlertsParams) ([]dbgen.ResolveStaleAlertsRow, error)
	ExpireTruncation(ctx context.Context, arg dbgen.ExpireTruncationParams) (int64, error)
	viewQueries
	routeQueries
	retentionQueries
	internalalerts.Store
}

// processQueries are the queries of processing over one pool or transaction: those of the package and those that
// raise and resolve Internal alerts.
type processQueries struct {
	*dbgen.Queries
	internalalerts.Store
}

func newProcessQueries(d dbgen.DBTX) processQueries {
	return processQueries{Queries: dbgen.New(d), Store: internalalerts.NewStore(d)}
}

// ProcessStore runs the queries of processing alone or in one transaction, and claims Integrations.
type ProcessStore interface {
	ProcessQueries
	// InTx runs f in one transaction; tx is the transaction itself, for the Sink.
	InTx(ctx context.Context, f func(q ProcessQueries, tx dbgen.DBTX) error) error
	// ClaimIntegrations leases at most limit of the Integrations ids of the Organization orgID with the shared claim
	// of internal/db and returns those it leased.
	ClaimIntegrations(ctx context.Context, l db.Lease, orgID int64, ids []int64, limit int32) ([]Claimed, error)
}

// Claimed is an Integration that this replica leased: its id and public_id.
type Claimed struct {
	ID       int64
	PublicID string
}

// NewProcessStore is the ProcessStore over the main pool.
func NewProcessStore(pool *pgxpool.Pool) ProcessStore {
	return pgProcessStore{processQueries: newProcessQueries(pool), pool: pool}
}

type pgProcessStore struct {
	processQueries
	pool *pgxpool.Pool
}

func (s pgProcessStore) InTx(ctx context.Context, f func(ProcessQueries, dbgen.DBTX) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(newProcessQueries(tx), tx)
	})
}

func (s pgProcessStore) ClaimIntegrations(ctx context.Context, l db.Lease, orgID int64, ids []int64,
	limit int32) ([]Claimed, error) {
	return db.Claim(ctx, s.pool, l, limit, func(ctx context.Context, tx pgx.Tx, p db.ClaimParams) ([]Claimed, error) {
		rows, err := dbgen.New(tx).ClaimIntegrations(ctx, dbgen.ClaimIntegrationsParams{OrgID: orgID,
			Owner: pgtype.Text{String: p.Owner, Valid: true}, LeaseUntil: pgtype.Timestamptz{Time: p.LeaseUntil,
				Valid: true}, IntegrationIds: ids, Now: p.Now, BatchSize: p.Limit})
		out := make([]Claimed, len(rows))
		for i, r := range rows {
			out[i] = Claimed{ID: r.IntegrationID, PublicID: r.PublicID}
		}
		return out, err
	})
}

// ProcessorConfig is what a Processor works with.
type ProcessorConfig struct {
	OrgID int64
	Store ProcessStore
	// Business is the business clock: receipt times, processing times and retention.
	Business clock.Clock
	// Lease names this replica and holds a claimed Integration on the real clock.
	Lease db.Lease
	Log   *logging.Logger
	// Sink takes the Alert changes; nil records them on the Alerts only.
	Sink Sink
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL, the base of the runbook_url of the Internal alerts processing raises.
	RunbookBase string
}

// Processor processes the Stored Snapshots of one Organization (C-06.FR-1): per Integration under a lease, in
// arrival order, one transaction per Snapshot.
type Processor struct {
	orgID int64
	store ProcessStore
	clock clock.Clock
	lease db.Lease
	log   *logging.Logger
	sink  Sink
	// internal raises and resolves the Internal alerts of processing.
	internal *internalalerts.Raiser
}

// NewProcessor returns the Processor of an Organization.
func NewProcessor(cfg ProcessorConfig) *Processor {
	return &Processor{orgID: cfg.OrgID, store: cfg.Store, clock: cfg.Business, lease: cfg.Lease, log: cfg.Log,
		sink: cfg.Sink, internal: internalalerts.NewRaiser(cfg.OrgID, cfg.RunbookBase)}
}

// horizon is the oldest receipt time that can still be pending: retention.stored_snapshots before now.
func (p *Processor) horizon(ctx context.Context) (time.Time, error) {
	days, err := p.store.GetRetention(ctx, p.orgID)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the retention of stored snapshots: %w", err)
	}
	return p.clock.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour), nil
}

// claim finds the Integrations with pending Stored Snapshots, apart from those skip names, creates their claim rows
// when missing, and leases at most limit of them.
func (p *Processor) claim(ctx context.Context, skip func(int64) bool, limit int) ([]Claimed, error) {
	horizon, err := p.horizon(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := p.store.ListPendingIntegrations(ctx, dbgen.ListPendingIntegrationsParams{OrgID: p.orgID,
		Horizon: horizon})
	if err != nil {
		return nil, fmt.Errorf("find the integrations with pending snapshots: %w", err)
	}
	var ids []int64
	for _, id := range pending {
		if !skip(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || limit <= 0 {
		return nil, nil
	}
	// The worker of another replica starts from another one, so that two replicas spread over the Integrations.
	rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] }) //nolint:gosec // G404: spreads work, guards nothing
	if err := p.store.EnsureIngestClaims(ctx, dbgen.EnsureIngestClaimsParams{OrgID: p.orgID,
		IntegrationIds: ids}); err != nil {
		return nil, fmt.Errorf("create the claim rows: %w", err)
	}
	claimed, err := p.store.ClaimIntegrations(ctx, p.lease, p.orgID, ids, int32(min(limit, len(ids), maxClaim)))
	if err != nil {
		return nil, fmt.Errorf("claim the integrations: %w", err)
	}
	return claimed, nil
}

// release frees the lease of an Integration that this replica holds.
func (p *Processor) release(ctx context.Context, integrationID int64) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := p.store.ReleaseIngestClaim(ctx, dbgen.ReleaseIngestClaimParams{OrgID: p.orgID,
		IntegrationID: integrationID, Owner: pgtype.Text{String: p.lease.Owner, Valid: true}}); err != nil {
		return fmt.Errorf("release the integration: %w", err)
	}
	return nil
}

// Drain processes every pending Stored Snapshot of the Organization that this replica can claim, one Integration
// after the other, and returns once none is left; tests and the worker's rounds use it.
func (p *Processor) Drain(ctx context.Context) (int, error) {
	claimed, err := p.claim(ctx, func(int64) bool { return false }, 1<<20)
	if err != nil {
		return 0, err
	}
	total := 0
	var errs []error
	for _, c := range claimed {
		n, err := p.ProcessPending(ctx, c.ID)
		total += n
		errs = append(errs, err, p.release(ctx, c.ID))
	}
	return total, errors.Join(errs...)
}

// ProcessPending processes the pending Stored Snapshots of an Integration whose lease this replica holds, oldest
// first, until none is left, the lease is lost or ctx ends, and returns how many it processed or marked failed. A
// Snapshot that cannot be processed is marked failed and does not stop the ones behind it (C-06.FR-20); a lost
// connection leaves it pending.
func (p *Processor) ProcessPending(ctx context.Context, integrationID int64) (int, error) {
	horizon, err := p.horizon(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for ctx.Err() == nil {
		done, err := p.processNext(ctx, integrationID, horizon)
		switch {
		case errors.Is(err, errLeaseLost):
			return n, nil
		case err != nil:
			return n, err
		case !done:
			return n, nil
		}
		n++
	}
	return n, ctx.Err()
}

// attempt is one Stored Snapshot in processing; internal marks a synthetic one (source internal).
type attempt struct {
	integration string
	id          int64
	publicID    string
	receivedAt  time.Time
	internal    bool
	payload     *Payload
	result      processedSnapshot
}

// processNext processes the oldest pending Stored Snapshot of the Integration in one transaction that holds the
// claim row, and reports whether there was one.
func (p *Processor) processNext(ctx context.Context, integrationID int64, horizon time.Time) (bool, error) {
	start := p.lease.Clocks.Real.Now()
	var a *attempt
	err := p.store.InTx(ctx, func(q ProcessQueries, tx dbgen.DBTX) error {
		a = nil
		info, err := p.renew(ctx, q, integrationID)
		if err != nil {
			return err
		}
		row, err := q.NextPendingSnapshot(ctx, dbgen.NextPendingSnapshotParams{OrgID: p.orgID,
			IntegrationID: integrationID, Horizon: horizon})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the next pending snapshot: %w", err)
		}
		a = &attempt{integration: info.PublicID, id: row.ID, publicID: row.PublicID, receivedAt: row.ReceivedAt.UTC(),
			internal: row.Source == SourceInternal}
		payload, err := ParsePayload(row.Body)
		if err != nil {
			return err
		}
		a.payload = &payload
		in, err := snapshotOf(a, payload, info)
		if err != nil {
			return err
		}
		d, marker := internalalerts.DeletionOf(row.Body)
		switch {
		case marker && a.internal:
			a.result, err = p.applyDeletion(ctx, q, tx, integrationID, in, d)
		case !a.internal && info.DeletedAt.Valid && !a.receivedAt.Before(info.DeletedAt.Time):
			// A request that authenticated before the deletion and was stored after it: the marker may already have
			// resolved the Integration's Alerts, so nothing fires again.
			return processingError(fmt.Errorf("the integration was deleted before the snapshot was received"))
		default:
			if a.internal {
				if in.AlertsSince, err = p.alertsSince(ctx, q); err != nil {
					return err
				}
			}
			a.result, err = p.applySnapshot(ctx, q, tx, integrationID, in)
		}
		if err != nil {
			return err
		}
		return p.finish(ctx, q, integrationID, a, StateProcessed, "")
	})
	switch {
	case err == nil && a == nil:
		return false, nil
	case err == nil:
		p.processed(ctx, a, p.lease.Clocks.Real.Now().Sub(start))
		return true, nil
	case a == nil || transient(ctx, err):
		return false, err
	}
	return true, p.fail(ctx, integrationID, a, err)
}

// alertsSince is the oldest startsAt an Internal alert can have and still be in the Alerts view:
// retention.alert_details before now. A replayed raise older than that never fires an Alert that retention removed.
func (p *Processor) alertsSince(ctx context.Context, q ProcessQueries) (time.Time, error) {
	days, err := q.GetAlertRetention(ctx, p.orgID)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the retention of alert details: %w", err)
	}
	return p.clock.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour), nil
}

// renew extends the lease and reads what processing needs of the Integration.
func (p *Processor) renew(ctx context.Context, q ProcessQueries, integrationID int64) (dbgen.RenewIngestLeaseRow,
	error) {
	params := p.lease.Params(1)
	info, err := q.RenewIngestLease(ctx, dbgen.RenewIngestLeaseParams{OrgID: p.orgID, IntegrationID: integrationID,
		Owner:      pgtype.Text{String: p.lease.Owner, Valid: true},
		LeaseUntil: pgtype.Timestamptz{Time: params.LeaseUntil, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return info, errLeaseLost
	}
	if err != nil {
		return info, fmt.Errorf("renew the lease: %w", err)
	}
	return info, nil
}

func snapshotOf(a *attempt, payload Payload, info dbgen.RenewIngestLeaseRow) (snapshotIn, error) {
	static := map[string]string{}
	if err := json.Unmarshal(info.StaticLabels, &static); err != nil {
		return snapshotIn{}, processingError(fmt.Errorf("read the static labels: %w", err))
	}
	return snapshotIn{StoredSnapshotID: a.id, ReceivedAt: a.receivedAt, Payload: payload, StaticLabels: static,
		DuplicateWindow: time.Duration(info.DuplicateWindowSeconds) * time.Second, ClockMs: clockAt(info, a.receivedAt),
		Internal: a.internal, Integration: internalalerts.Entity{ID: info.PublicID, Name: info.Name},
		Deleted: info.DeletedAt.Valid}, nil
}

// finish marks the Stored Snapshot processed or failed with what processing read of its payload, and counts it on
// its Integration when it leaves pending for the first time, so that a replay never counts it again.
func (p *Processor) finish(ctx context.Context, q ProcessQueries, integrationID int64, a *attempt, state,
	reason string) error {
	params := dbgen.FinishSnapshotParams{OrgID: p.orgID, ID: a.id, ReceivedAt: a.receivedAt, State: state,
		ProcessedAt: pgtype.Timestamptz{Time: p.clock.Now().UTC(), Valid: true}, RouteIds: []int64{}}
	if state == StateProcessed && a.result.Routed.IDs != nil {
		// A failed Snapshot rolled its routing back, so it names no Route.
		params.RouteIds = a.result.Routed.IDs
	}
	if reason != "" {
		params.ProcessingError = pgtype.Text{String: reason, Valid: true}
	}
	if a.payload != nil {
		params.GroupKey = pgtype.Text{String: a.payload.GroupKey, Valid: true}
		params.AlertCount = pgtype.Int8{Int64: int64(len(a.payload.Alerts)), Valid: true}
		params.TruncatedAlerts = pgtype.Int8{Int64: a.payload.TruncatedAlerts, Valid: true}
	}
	first, err := q.FinishSnapshot(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return processingError(fmt.Errorf("the snapshot %s is no longer pending", a.publicID))
	}
	if err != nil {
		return fmt.Errorf("mark the snapshot %s: %w", state, err)
	}
	if !first {
		return nil
	}
	if err := q.CountSnapshot(ctx, dbgen.CountSnapshotParams{OrgID: p.orgID, IntegrationID: integrationID,
		ReceivedAt: a.receivedAt}); err != nil {
		return fmt.Errorf("count the snapshot: %w", err)
	}
	return nil
}

// fail marks a Stored Snapshot that could not be processed failed with the error, in a transaction of its own
// after the processing one rolled back; it is never retried by itself (C-06.FR-20).
func (p *Processor) fail(ctx context.Context, integrationID int64, a *attempt, cause error) error {
	reason := errorText(cause)
	mark := func() error {
		return p.store.InTx(ctx, func(q ProcessQueries, _ dbgen.DBTX) error {
			if _, err := p.renew(ctx, q, integrationID); err != nil {
				return err
			}
			return p.finish(ctx, q, integrationID, a, StateFailed, reason)
		})
	}
	err := mark()
	if err != nil && a.payload != nil && !errors.Is(err, errLeaseLost) && !transient(ctx, err) {
		// What processing read of the payload cannot be stored either: the Snapshot fails without it, so that it
		// never blocks the Snapshots behind it.
		a.payload = nil
		err = mark()
	}
	if err != nil {
		return err
	}
	metrics.IngestFailedSnapshots.With(a.integration).Inc()
	p.observeDelay(a)
	p.log.Log(ctx, logging.SnapshotFailed, logging.F("integration", a.integration),
		logging.F("stored_snapshot", a.publicID), logging.F("error", reason))
	return nil
}

// processed counts and logs a processed Stored Snapshot once its transaction committed: one line per Snapshot,
// never one per Alert (C-06.FR-15).
func (p *Processor) processed(ctx context.Context, a *attempt, took time.Duration) {
	s := a.result.Stats
	p.observeDelay(a)
	if s.Resolved > 0 {
		metrics.AlertsResolved.With(a.integration, ResolveResolved).Add(s.Resolved)
	}
	if s.Gone > 0 {
		metrics.AlertsResolved.With(a.integration, ResolveGone).Add(s.Gone)
	}
	if s.Deleted > 0 {
		metrics.AlertsResolved.With(a.integration, ResolveIntegrationDeleted).Add(s.Deleted)
	}
	if s.Dropped > 0 {
		metrics.IngestResolvedDropped.With(a.integration).Add(s.Dropped)
	}
	if s.Truncated > 0 {
		metrics.IngestTruncatedSnapshots.With(a.integration).Inc()
	}
	p.log.Log(ctx, logging.SnapshotProcessed, logging.F("integration", a.integration),
		logging.F("stored_snapshot", a.publicID), logging.F("group_key", a.payload.GroupKey),
		logging.F("alerts", s.Alerts), logging.F("fired", s.Fired), logging.F("resolved", s.Resolved+s.Deleted),
		logging.F("gone", s.Gone), logging.F("continued", s.Continued), logging.F("dropped", s.Dropped),
		logging.F("truncated", s.Truncated), logging.F("routes", routesOf(a.result.Routed)),
		logging.F("alert_groups", alertGroupsOf(a.result.Routed)), logging.F("duration_ms", took.Milliseconds()))
	a.result.Routed.committed(ctx)
	for _, c := range a.result.Internal {
		event := logging.InternalAlertRaised
		if c.Resolved {
			event = logging.InternalAlertResolved
		}
		p.log.Log(ctx, event, logging.F("alertname", c.Alertname), logging.F("fingerprint", c.Fingerprint),
			logging.F("entity", c.Entity))
	}
}

// routesOf are the public_ids of the Routes that took Alerts of a Snapshot, an empty list when none did.
func routesOf(r Routed) []string {
	if r.PublicIDs == nil {
		return []string{}
	}
	return r.PublicIDs
}

// alertGroupsOf are the #N of the Alert Groups a Snapshot created or changed, an empty list when none.
func alertGroupsOf(r Routed) []int64 {
	if r.AlertGroups == nil {
		return []int64{}
	}
	return r.AlertGroups
}

// observeDelay observes the time from receipt to the end of processing on the business clock.
func (p *Processor) observeDelay(a *attempt) {
	metrics.IngestProcessingDelay.With(a.integration).Update(max(p.clock.Now().Sub(a.receivedAt).Seconds(), 0))
}

// processingErr is an error of the processing code itself rather than of the connection: the Snapshot fails.
type processingErr struct{ err error }

func (e *processingErr) Error() string { return e.err.Error() }

func (e *processingErr) Unwrap() error { return e.err }

func processingError(err error) error { return &processingErr{err: err} }

// transient reports an error that leaves the Stored Snapshot pending, to be processed again: the end of ctx, a lost
// connection, or a server error of a class that a retry can clear (connection, transaction rollback, insufficient
// resources, operator intervention, system error). Errors of the payload, of the processing code and other server
// errors mark it failed.
func transient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if _, ok := errors.AsType[*PayloadError](err); ok {
		return false
	}
	if _, ok := errors.AsType[*processingErr](err); ok {
		return false
	}
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pe.Code[:2] {
		case "08", "40", "53", "57", "58":
			return true
		}
		return false
	}
	return true
}

// errorText is the processing_error of a failed Stored Snapshot: the error, at most maxErrorLength bytes of it.
func errorText(err error) string {
	s := strings.ToValidUTF8(strings.ReplaceAll(err.Error(), "\x00", `\x00`), "\uFFFD")
	if len(s) <= maxErrorLength {
		return s
	}
	cut := maxErrorLength
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Backlog sets muster_ingest_backlog to the pending Stored Snapshots of the Organizations, as a Leader task.
func Backlog(ctx context.Context, q ProcessQueries, orgs []int64) error {
	var total int64
	for _, org := range orgs {
		n, err := q.CountPendingSnapshots(ctx, org)
		if err != nil {
			return fmt.Errorf("count the pending snapshots: %w", err)
		}
		total += n
	}
	metrics.IngestBacklog.With().Set(float64(total))
	return nil
}

// Worker is the processing worker of a replica (C-06.FR-1): woken by the notification of each stored Snapshot and
// polling every Poll as a fallback, it claims Integrations with pending Stored Snapshots in every Organization and
// processes up to Concurrency of them at the same time, each until its pending Snapshots are done; then it releases
// the lease. Shutdown stops the claims and releases the leases it holds.
type Worker struct {
	// Organizations lists the Organizations; Processor is the Processor of one, false to skip it.
	Organizations func(ctx context.Context) ([]int64, error)
	Processor     func(orgID int64) (*Processor, bool)
	Log           *logging.Logger
	// Poll and Concurrency default to Poll and Concurrency.
	Poll        time.Duration
	Concurrency int
	// Real is the real clock that paces the retries of an Integration whose processing failed; nil is the system
	// time.
	Real clock.Clock

	once sync.Once
	wake chan struct{}
	mu   sync.Mutex
	held map[[2]int64]bool
	// retry holds the Integrations whose processing failed: how often in a row, and when to try again.
	retry map[[2]int64]retryState
}

type retryState struct {
	failures  int
	notBefore time.Time
}

func (w *Worker) init() {
	w.once.Do(func() {
		w.wake = make(chan struct{}, 1)
		w.held = map[[2]int64]bool{}
		w.retry = map[[2]int64]retryState{}
		if w.Real == nil {
			w.Real = clock.Real{}
		}
		if w.Poll <= 0 {
			w.Poll = Poll
		}
		if w.Concurrency <= 0 {
			w.Concurrency = Concurrency
		}
	})
}

// Wake makes the worker look for pending Stored Snapshots now; wakes that come while it looks are merged.
func (w *Worker) Wake() {
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run runs rounds until ctx ends, then waits for the Integrations in processing to stop and release their leases.
func (w *Worker) Run(ctx context.Context) {
	w.init()
	slots := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	failures := 0
	for ctx.Err() == nil {
		if err := w.round(ctx, slots, &wg); err != nil && ctx.Err() == nil {
			failures++
			w.Log.Log(ctx, logging.SnapshotProcessingInterrupted, logging.F("integration", ""),
				logging.F("error", err.Error()))
		} else {
			failures = 0
		}
		wait := w.Poll
		if failures > 0 {
			wait = db.Backoff(failures, w.Poll, maxBackoff, rand.Float64)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
		case <-w.wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// round claims Integrations with pending Stored Snapshots for the free slots and starts processing each.
func (w *Worker) round(ctx context.Context, slots chan struct{}, wg *sync.WaitGroup) error {
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		p, ok := w.Processor(org)
		if !ok {
			continue
		}
		claimed, err := p.claim(ctx, func(id int64) bool { return w.skips(org, id) }, cap(slots)-len(slots))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, c := range claimed {
			w.hold(org, c.ID, true)
			slots <- struct{}{}
			wg.Go(func() {
				defer func() { <-slots }()
				w.process(ctx, p, org, c)
			})
		}
	}
	return errors.Join(errs...)
}

// process processes one claimed Integration, then releases it; a round follows at once when it processed anything,
// so that Snapshots stored meanwhile do not wait for the poll. An Integration whose processing failed waits a
// growing pause before it is claimed again.
func (w *Worker) process(ctx context.Context, p *Processor, org int64, c Claimed) {
	n, err := p.ProcessPending(ctx, c.ID)
	if err != nil && ctx.Err() == nil {
		w.Log.Log(ctx, logging.SnapshotProcessingInterrupted, logging.F("integration", c.PublicID),
			logging.F("error", err.Error()))
	}
	w.settle(org, c.ID, err != nil && ctx.Err() == nil)
	if err := p.release(ctx, c.ID); err != nil {
		w.Log.Log(ctx, logging.SnapshotProcessingInterrupted, logging.F("integration", c.PublicID),
			logging.F("error", err.Error()))
	}
	w.hold(org, c.ID, false)
	if n > 0 {
		w.Wake()
	}
}

func (w *Worker) holds(org, id int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.held[[2]int64{org, id}]
}

// skips reports an Integration that this replica processes now or that waits after a failure.
func (w *Worker) skips(org, id int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := [2]int64{org, id}
	return w.held[key] || w.Real.Now().Before(w.retry[key].notBefore)
}

// settle records how processing an Integration ended: a failure pauses it, the pause growing with each failure in a
// row; a success forgets them.
func (w *Worker) settle(org, id int64, failed bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := [2]int64{org, id}
	if !failed {
		delete(w.retry, key)
		return
	}
	r := w.retry[key]
	r.failures++
	r.notBefore = w.Real.Now().Add(db.Backoff(r.failures, w.Poll, maxBackoff, rand.Float64))
	w.retry[key] = r
}

func (w *Worker) hold(org, id int64, held bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if held {
		w.held[[2]int64{org, id}] = true
	} else {
		delete(w.held, [2]int64{org, id})
	}
}
