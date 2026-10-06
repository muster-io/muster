// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package leader

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// MaintenanceInterval is how often the hourly Leader tasks run: partition maintenance, replica pruning and short-lived
// pruning.
const MaintenanceInterval = time.Hour

// PruneBatch is the most rows one delete of short-lived pruning removes, so that a large backlog is deleted in short
// statements that hold their row locks briefly.
const PruneBatch = 1000

// Task is one Leader task: it runs when the lock is taken and then every Every, until the lock is lost. Every Leader
// task is safe to run twice, because a frozen old Leader can overlap with its successor for a moment (ADR-0007).
type Task struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
}

// Work is what the Leader tasks of this release act on.
type Work struct {
	// Alive keeps the alive mark, records downtime and opens the recovery window.
	Alive *Alive
	// MaintainPartitions creates and drops partitions; it logs its own failures.
	MaintainPartitions func(ctx context.Context) error
	// PruneReplicas removes the records of replicas gone for replica.prune_after.
	PruneReplicas func(ctx context.Context) error

	// Short-lived pruning deletes the rows of short-lived tables that can no longer be used (schema.md, section 6),
	// per Organization, on the business clock, and logs and counts them.
	Organizations func(ctx context.Context) ([]int64, error)
	Business      clock.Clock
	Log           *logging.Logger
	// PruneAuth are the short-lived tables of internal/auth: sessions and sign-in throttles. A story that adds a
	// short-lived table adds a field for its domain here, lists it in shortLived and adds the table name to
	// metrics.ShortLivedTables.
	PruneAuth []PruneTable
	// PruneUsers are the short-lived tables of internal/users: password setup links.
	PruneUsers []PruneTable
	// PruneOIDC are the short-lived tables of internal/oidc: the OIDC redirects in flight.
	PruneOIDC []PruneTable
}

// PruneTable is one short-lived table of short-lived pruning.
type PruneTable struct {
	// Name is the table, as the log line and the table label of the metric name it.
	Name string
	// Delete deletes at most limit rows of the Organization orgID that can no longer be used at now and returns how
	// many it deleted. It skips rows that are locked, so a busy row waits for the next run.
	Delete func(ctx context.Context, orgID int64, now time.Time, limit int32) (int64, error)
}

func (w Work) shortLived() []PruneTable {
	return slices.Concat(w.PruneAuth, w.PruneUsers, w.PruneOIDC)
}

// pruneShortLived runs every short-lived table in every Organization at the same now, deleting in batches until a
// batch comes back short. A failed table does not stop the others. Running it twice deletes nothing more, because
// each delete only takes rows that already qualify.
func (w Work) pruneShortLived(ctx context.Context) error {
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations to prune: %w", err)
	}
	now := w.Business.Now()
	var errs []error
	for _, t := range w.shortLived() {
		var rows int64
		for _, org := range orgs {
			n, err := pruneTable(ctx, t, org, now)
			rows += n
			if err != nil {
				errs = append(errs, fmt.Errorf("prune %s: %w", t.Name, err))
				break
			}
		}
		if rows > 0 {
			metrics.ShortLivedRowsPruned.With(t.Name).AddInt64(rows)
			w.Log.Log(ctx, logging.ShortLivedPruned, logging.F("table", t.Name), logging.F("rows", rows))
		}
	}
	return errors.Join(errs...)
}

func pruneTable(ctx context.Context, t PruneTable, orgID int64, now time.Time) (int64, error) {
	var rows int64
	for {
		n, err := t.Delete(ctx, orgID, now, PruneBatch)
		rows += n
		if err != nil || n < PruneBatch {
			return rows, err
		}
	}
}

// Tasks returns the closed list of Leader tasks (ADR-0007): partition maintenance and retention, the alive mark, the
// pruning of replica records and the pruning of short-lived state. Later capabilities add theirs here: the Heartbeat lost and Stale checks, Telegram
// polling, the outgoing heartbeat and the OIDC client secret expiry check. The Keeper calls the result at every
// leadership, so each one starts with a takeover.
func Tasks(w Work) func() []Task {
	return func() []Task {
		takenOver := false
		return []Task{
			{Name: "partition_maintenance", Every: MaintenanceInterval, Run: func(ctx context.Context) error {
				_ = w.MaintainPartitions(ctx) // logged as partition_maintenance_failed and retried at the next run
				return nil
			}},
			{Name: "alive_mark", Every: AliveMarkInterval, Run: func(ctx context.Context) error {
				if takenOver {
					return w.Alive.Mark(ctx)
				}
				if err := w.Alive.TakeOver(ctx); err != nil {
					return err
				}
				takenOver = true
				return nil
			}},
			{Name: "replica_pruning", Every: MaintenanceInterval, Run: w.PruneReplicas},
			{Name: "short_lived_pruning", Every: MaintenanceInterval, Run: w.pruneShortLived},
		}
	}
}
