// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// heartbeatLive is the Heartbeat state in which the liveness clock runs (C-07.FR-3).
const heartbeatLive = "live"

// EffectiveClock is the liveness clock of an Integration at the business time at (schema.md 4.7): the stored clock,
// plus the time since the last Heartbeat signal while the Heartbeat is live and that time is at most the timeout. Time
// with the Heartbeat lost or off, and a gap longer than the timeout — Muster's own downtime among them — never counts
// (C-06.FR-9, C-07.FR-5). A time before the last signal takes the stored clock, which then runs slightly ahead.
func EffectiveClock(clockMs int64, state string, lastSignal *time.Time, timeout time.Duration, at time.Time) int64 {
	if state != heartbeatLive || lastSignal == nil {
		return clockMs
	}
	gap := at.Sub(*lastSignal)
	if gap <= 0 || gap > timeout {
		return clockMs
	}
	return clockMs + gap.Milliseconds()
}

// clockAt is the liveness clock of the Integration whose lease was renewed, at the business time at. It reads the
// Heartbeat as it is now: for a Snapshot received before a loss that processing reaches only after it, the time from
// the last signal to the receipt is not added, so its Alerts may go Stale up to one Heartbeat timeout early. That is
// accepted, as C-06.FR-9 accepts a short downtime: the error is at most the timeout against a stale_after of at least
// three repeat intervals, and processing normally follows receipt within a second.
func clockAt(info dbgen.RenewIngestLeaseRow, at time.Time) int64 {
	return EffectiveClock(info.LivenessClockMs, info.HeartbeatState, timeOf(info.HeartbeatLastSignalAt),
		time.Duration(info.HeartbeatTimeoutSeconds)*time.Second, at)
}

// StaleScan is the Leader's Stale scan of the Organization (C-06.FR-6 to FR-10, C-07.FR-5), in the Processor's
// Organization: for each Integration whose Heartbeat is live, one after the other, it takes the Integration's
// ingestion claim, so that it never runs beside the Integration's processing, and in one transaction makes Stale every
// active presence whose reference is more than stale_after of its Alertmanager route behind the liveness clock,
// resolves as Stale the Alerts left without an active presence, and ends the truncation of the groupKeys whose last
// Snapshot is as far behind. An Integration claimed by its processing, or with Stored Snapshots still waiting for it,
// waits for the next scan. Running it twice changes nothing more.
func (p *Processor) StaleScan(ctx context.Context) error {
	ids, err := p.store.ListLiveIntegrations(ctx, p.orgID)
	if err != nil {
		return fmt.Errorf("list the integrations with a live heartbeat: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := p.store.EnsureIngestClaims(ctx, dbgen.EnsureIngestClaimsParams{OrgID: p.orgID,
		IntegrationIds: ids}); err != nil {
		return fmt.Errorf("create the claim rows: %w", err)
	}
	horizon, err := p.horizon(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		// One claim at a time, so that the scan holds up the processing of one Integration at most.
		claimed, err := p.store.ClaimIntegrations(ctx, p.lease, p.orgID, []int64{id}, 1)
		if err != nil {
			errs = append(errs, fmt.Errorf("claim the integration: %w", err))
			continue
		}
		for _, c := range claimed {
			errs = append(errs, p.scan(ctx, c, horizon), p.release(ctx, c.ID))
		}
	}
	return errors.Join(errs...)
}

// scan runs the Stale scan of one claimed Integration in one transaction that holds its claim row, then counts and
// logs what it resolved.
func (p *Processor) scan(ctx context.Context, c Claimed, horizon time.Time) error {
	var changes []AlertChange
	var routed Routed
	err := p.store.InTx(ctx, func(q ProcessQueries, tx dbgen.DBTX) error {
		changes, routed = nil, Routed{}
		info, err := p.renew(ctx, q, c.ID)
		if err != nil {
			return err
		}
		if info.Builtin || info.DeletedAt.Valid || info.HeartbeatState != heartbeatLive {
			return nil
		}
		pending, err := q.HasPendingSnapshots(ctx, dbgen.HasPendingSnapshotsParams{OrgID: p.orgID,
			IntegrationID: c.ID, Horizon: horizon})
		if err != nil {
			return fmt.Errorf("look for pending snapshots: %w", err)
		}
		if pending {
			return nil
		}
		now := p.clock.Now().UTC()
		clockMs := clockAt(info, now)
		if changes, err = p.staleAlerts(ctx, q, c.ID, now, clockMs); err != nil {
			return err
		}
		if err := p.expireTruncation(ctx, q, c.ID, info.PublicID, now, clockMs); err != nil {
			return err
		}
		if p.sink != nil && len(changes) > 0 {
			if routed, err = p.sink.AlertChanges(ctx, tx, changes); err != nil {
				return fmt.Errorf("hand over the alert changes: %w", err)
			}
		}
		return nil
	})
	if errors.Is(err, errLeaseLost) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stale scan of %s: %w", c.PublicID, err)
	}
	if len(changes) > 0 {
		metrics.AlertsResolved.With(c.PublicID, ResolveStale).Add(len(changes))
		p.log.Log(ctx, logging.AlertsStale, logging.F("integration", c.PublicID), logging.F("count", len(changes)))
	}
	routed.committed(ctx)
	return nil
}

// staleAlerts makes the Stale presences of an Integration Stale at the liveness clock clockMs and resolves, with the
// reason stale and the text of C-06.FR-10, the Alerts that have no active presence left (C-06.FR-7). It returns their
// Alert changes; a Stale scan's change has no Stored Snapshot.
func (p *Processor) staleAlerts(ctx context.Context, q ProcessQueries, integrationID int64, now time.Time,
	clockMs int64) ([]AlertChange, error) {
	rows, err := q.ListStalePresences(ctx, dbgen.ListStalePresencesParams{OrgID: p.orgID,
		IntegrationID: integrationID, Factor: StaleAfterFactor, UnlearnedMs: StaleAfterUnlearned.Milliseconds(),
		ClockMs: clockMs})
	if err != nil {
		return nil, fmt.Errorf("find the stale presences: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	alerts, groups := make([]int64, len(rows)), make([]int64, len(rows))
	for i, r := range rows {
		alerts[i], groups[i] = r.AlertID, r.AlertmanagerGroupID
	}
	if err := q.MarkPresencesStale(ctx, dbgen.MarkPresencesStaleParams{OrgID: p.orgID, AlertIds: alerts,
		AlertmanagerGroupIds: groups}); err != nil {
		return nil, fmt.Errorf("record the stale presences: %w", err)
	}
	resolved, err := q.ResolveStaleAlerts(ctx, dbgen.ResolveStaleAlertsParams{OrgID: p.orgID,
		IntegrationID: integrationID, AlertIds: alerts, ResolvedAt: now, ReasonText: GoneReasonText, UpdatedAt: now})
	if err != nil {
		return nil, fmt.Errorf("resolve the stale alerts: %w", err)
	}
	changes := make([]AlertChange, len(resolved))
	for i, r := range resolved {
		changes[i] = AlertChange{Kind: ChangeResolved, AlertID: r.ID, Fingerprint: r.Fingerprint, Episode: r.Episode,
			Reason: ResolveStale, ReasonText: GoneReasonText}
	}
	return changes, nil
}

// expireTruncation ends the truncation of the groupKeys whose last Snapshot is more than stale_after behind the
// liveness clock (C-06.FR-6) and resolves MusterSnapshotTruncated when none is left.
func (p *Processor) expireTruncation(ctx context.Context, q ProcessQueries, integrationID int64, publicID string,
	now time.Time, clockMs int64) error {
	n, err := q.ExpireTruncation(ctx, dbgen.ExpireTruncationParams{OrgID: p.orgID, IntegrationID: integrationID,
		Factor: StaleAfterFactor, UnlearnedMs: StaleAfterUnlearned.Milliseconds(), ClockMs: clockMs})
	if err != nil {
		return fmt.Errorf("end the expired truncation: %w", err)
	}
	if n == 0 {
		return nil
	}
	left, err := q.CountTruncatedGroups(ctx, dbgen.CountTruncatedGroupsParams{OrgID: p.orgID,
		IntegrationID: integrationID})
	if err != nil {
		return fmt.Errorf("count the truncated groupKeys: %w", err)
	}
	if left > 0 {
		return nil
	}
	return internalError(internalalerts.SnapshotTruncated,
		p.internal.Resolve(ctx, q, now, internalalerts.SnapshotTruncated, publicID))
}
