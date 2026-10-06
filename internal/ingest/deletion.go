// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/internalalerts"
)

// DeletedReasonText is the reason of an Alert resolved because its Integration was deleted (C-06.FR-16).
func DeletedReasonText(name string) string {
	return "Integration " + name + " deleted"
}

// applyDeletion processes the marker that deleteIntegration wrote into the deleted Integration's queue, after the
// Stored Snapshots the Integration accepted before (C-05.FR-8, C-06.FR-16): every Alert of the Integration that still
// fires resolves with the reason integration_deleted, and its Internal alerts that fire or whose raise waits for
// processing resolve. Its truncation ends with it; a deleted Integration raises no Internal alert afterwards, also in
// a replay, so a replayed marker resolves nothing again.
func (p *Processor) applyDeletion(ctx context.Context, q ProcessQueries, tx dbgen.DBTX, integrationID int64,
	in snapshotIn, d internalalerts.Deletion) (processedSnapshot, error) {
	text := DeletedReasonText(d.Name)
	rows, err := q.ResolveIntegrationAlerts(ctx, dbgen.ResolveIntegrationAlertsParams{OrgID: p.orgID,
		IntegrationID: integrationID, ResolvedAt: in.ReceivedAt, ReasonText: text, UpdatedAt: p.clock.Now().UTC()})
	if err != nil {
		return processedSnapshot{}, fmt.Errorf("resolve the alerts of the deleted integration: %w", err)
	}
	slices.SortFunc(rows, func(a, b dbgen.ResolveIntegrationAlertsRow) int { return cmp.Compare(a.ID, b.ID) })
	out := processedSnapshot{Stats: Stats{Deleted: len(rows)}, Changes: make([]AlertChange, len(rows))}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		out.Changes[i] = AlertChange{Kind: ChangeResolved, AlertID: r.ID, Fingerprint: r.Fingerprint,
			Episode: r.Episode, StoredSnapshotID: in.StoredSnapshotID, Reason: ResolveIntegrationDeleted,
			ReasonText: text}
	}
	if len(ids) > 0 {
		if err := q.EndPresences(ctx, dbgen.EndPresencesParams{OrgID: p.orgID, AlertIds: ids}); err != nil {
			return processedSnapshot{}, fmt.Errorf("end the presences of the resolved alerts: %w", err)
		}
	}
	if _, err := q.ClearTruncation(ctx, dbgen.ClearTruncationParams{OrgID: p.orgID,
		IntegrationID: integrationID}); err != nil {
		return processedSnapshot{}, fmt.Errorf("end the truncation of the deleted integration: %w", err)
	}
	err = p.internal.ResolveAbout(ctx, q, in.ReceivedAt, internalalerts.EntityIntegration, in.Integration.ID)
	if errors.Is(err, internalalerts.ErrNoBuiltin) {
		err = processingError(err)
	}
	if err != nil {
		return processedSnapshot{}, fmt.Errorf("resolve the internal alerts of the deleted integration: %w", err)
	}
	if p.sink != nil && len(out.Changes) > 0 {
		if err := p.sink.AlertChanges(ctx, tx, out.Changes); err != nil {
			return processedSnapshot{}, fmt.Errorf("hand over the alert changes: %w", err)
		}
	}
	return out, nil
}
