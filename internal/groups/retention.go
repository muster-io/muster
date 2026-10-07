// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

// RetentionBatch is the most rows one delete of the Alert Group retention removes, so that each statement holds its
// row locks briefly (design/db/schema.md §6).
const RetentionBatch = 5000

// retentionQueries are the queries of the Alert Group retention.
type retentionQueries interface {
	GetRetentionPeriods(ctx context.Context, orgID int64) (dbgen.GetRetentionPeriodsRow, error)
	DeleteExpiredMemberships(ctx context.Context, arg dbgen.DeleteExpiredMembershipsParams) (int64, error)
	DeleteExpiredGroups(ctx context.Context, arg dbgen.DeleteExpiredGroupsParams) (int64, error)
}

// Purged is what one run of the retention deleted: Details, the Alerts inside Alert Groups, and Summaries, the
// summary rows.
type Purged struct {
	Details   int64
	Summaries int64
}

// Purge runs the Alert Group retention of the Organization orgID at now (C-09.FR-16): first the Alerts inside Alert
// Groups that ended retention.alert_details ago, then the summary rows resolved retention.alert_group_summaries ago
// with their Notes, timers, deliveries and queue rows by cascade, each in batches of at most RetentionBatch until a
// batch comes back short. Timeline and delivery event months are dropped by partition maintenance; the reads hide the
// details of an Alert Group from the end of retention.alert_details, whether or not they are deleted yet. Running it
// twice deletes nothing more, because each delete only takes rows that already qualify.
func Purge(ctx context.Context, q Queries, orgID int64, now time.Time) (Purged, error) {
	periods, err := q.GetRetentionPeriods(ctx, orgID)
	if err != nil {
		return Purged{}, fmt.Errorf("read the retention periods: %w", err)
	}
	now = now.UTC()
	var out Purged
	details := now.Add(-days(periods.RetentionAlertDetailsDays))
	for {
		n, err := q.DeleteExpiredMemberships(ctx, dbgen.DeleteExpiredMembershipsParams{OrgID: orgID, Cutoff: details,
			BatchSize: RetentionBatch})
		out.Details += n
		if err != nil {
			return out, fmt.Errorf("delete the expired alerts inside alert groups: %w", err)
		}
		if n < RetentionBatch {
			break
		}
	}
	summaries := now.Add(-days(periods.RetentionAlertGroupSummariesDays))
	for {
		n, err := q.DeleteExpiredGroups(ctx, dbgen.DeleteExpiredGroupsParams{OrgID: orgID, Cutoff: summaries,
			BatchSize: RetentionBatch})
		out.Summaries += n
		if err != nil {
			return out, fmt.Errorf("delete the expired alert group summaries: %w", err)
		}
		if n < RetentionBatch {
			return out, nil
		}
	}
}

// RetentionTask is Purge over q in the form of the Leader task alert_group_retention: what it deleted of each kind.
func RetentionTask(q Queries) func(ctx context.Context, orgID int64, now time.Time) (int64, int64, error) {
	return func(ctx context.Context, orgID int64, now time.Time) (int64, int64, error) {
		p, err := Purge(ctx, q, orgID, now)
		return p.Details, p.Summaries, err
	}
}

// days is a retention period in days as a duration.
func days(n int64) time.Duration { return time.Duration(n) * 24 * time.Hour }
