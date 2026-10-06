// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"errors"
	"fmt"

	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/internalalerts"
)

// truncation marks the groupKey truncated from a Snapshot with truncatedAlerts > 0 until its next untruncated
// Snapshot (C-06.FR-6); the window of a truncated Snapshot is marked by the window itself. The end of truncation after
// stale_after without Snapshots is the Stale scan's (S-023), which reads last_snapshot_clock_ms.
func (e *engine) truncation() {
	g, t := e.group, e.in.ReceivedAt
	if e.in.Payload.TruncatedAlerts > 0 {
		if !g.Truncated {
			g.Truncated, g.TruncatedSince = true, &t
		}
		return
	}
	g.Truncated, g.TruncatedSince = false, nil
}

// truncationChanged raises MusterSnapshotTruncated when the Integration's count of truncated groupKeys went from 0 to
// 1 and resolves it when the count returned to 0 (C-06.FR-6, C-06.AC-5); it runs in the Snapshot's transaction, after
// the groupKey was written, only when the Snapshot changed the groupKey's truncation.
func (p *Processor) truncationChanged(ctx context.Context, q ProcessQueries, integrationID int64, in snapshotIn,
	truncated bool) error {
	n, err := q.CountTruncatedGroups(ctx, dbgen.CountTruncatedGroupsParams{OrgID: p.orgID,
		IntegrationID: integrationID})
	if err != nil {
		return fmt.Errorf("count the truncated groupKeys: %w", err)
	}
	switch {
	case truncated && n == 1:
		entity := in.Integration
		if entity.Name, err = q.LockIntegrationName(ctx, dbgen.LockIntegrationNameParams{OrgID: p.orgID,
			IntegrationID: integrationID}); err != nil {
			return fmt.Errorf("read the name of the integration: %w", err)
		}
		err = p.internal.Raise(ctx, q, in.ReceivedAt, internalalerts.SnapshotTruncated, entity, nil)
	case !truncated && n == 0:
		err = p.internal.Resolve(ctx, q, in.ReceivedAt, internalalerts.SnapshotTruncated, in.Integration.ID)
	}
	return internalError(internalalerts.SnapshotTruncated, err)
}

// internalError names the Internal alert whose raise or resolve failed; a missing built-in Integration fails the
// Snapshot instead of holding up the Integration's queue.
func internalError(d *internalalerts.Definition, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, internalalerts.ErrNoBuiltin):
		return processingError(fmt.Errorf("%s: %w", d.Name, err))
	default:
		return fmt.Errorf("%s: %w", d.Name, err)
	}
}

// internalChangeOf is an Internal alert that fired or resolved, named by its alertname and its entity's id label.
func internalChangeOf(resolved bool, fingerprint string, labels map[string]string) internalChange {
	c := internalChange{Resolved: resolved, Alertname: labels["alertname"], Fingerprint: fingerprint}
	if d := internalalerts.Lookup(c.Alertname); d != nil && d.Entity != "" {
		c.Entity = labels[d.Entity]
	}
	return c
}
