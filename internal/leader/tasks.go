// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package leader

import (
	"context"
	"time"
)

// MaintenanceInterval is how often the hourly Leader tasks run: partition maintenance and replica pruning.
const MaintenanceInterval = time.Hour

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
}

// Tasks returns the closed list of Leader tasks (ADR-0007): partition maintenance and retention, the alive mark and
// the pruning of replica records. Later capabilities add theirs here: the Heartbeat lost and Stale checks, Telegram
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
		}
	}
}
