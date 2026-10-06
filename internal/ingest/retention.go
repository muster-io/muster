// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/ingest/dbgen"
)

// RetentionBatch is the most Alerts one delete of the Alerts view retention removes, so that each statement holds its
// row locks briefly.
const RetentionBatch = 5000

// retentionQueries are the queries of the Alerts view retention.
type retentionQueries interface {
	GetAlertRetention(ctx context.Context, orgID int64) (int64, error)
	DeleteExpiredAlerts(ctx context.Context, arg dbgen.DeleteExpiredAlertsParams) (int64, error)
}

// PruneAlerts deletes, in batches of at most RetentionBatch, the Alerts of the Organization orgID resolved longer
// than retention.alert_details before now, their presences going with them (C-06.FR-19, design/db/schema.md §6), and
// returns how many it deleted. A Leader task runs it hourly; running it twice deletes nothing more, because each
// delete only takes rows that already qualify.
func PruneAlerts(ctx context.Context, q ProcessQueries, orgID int64, now time.Time) (int64, error) {
	days, err := q.GetAlertRetention(ctx, orgID)
	if err != nil {
		return 0, fmt.Errorf("read the retention of alert details: %w", err)
	}
	cutoff := now.UTC().Add(-time.Duration(days) * 24 * time.Hour)
	var total int64
	for {
		n, err := q.DeleteExpiredAlerts(ctx, dbgen.DeleteExpiredAlertsParams{OrgID: orgID, Cutoff: cutoff,
			BatchSize: RetentionBatch})
		total += n
		if err != nil {
			return total, fmt.Errorf("delete the expired alerts: %w", err)
		}
		if n < RetentionBatch {
			return total, nil
		}
	}
}
