// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package leader

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// MaintenanceInterval is how often the hourly Leader tasks run: partition maintenance, replica pruning, short-lived
// pruning and the retention of the Alerts view.
const MaintenanceInterval = time.Hour

// BacklogInterval is how often the Leader counts the pending Stored Snapshots for muster_ingest_backlog.
const BacklogInterval = 15 * time.Second

// AlertGroupGaugeInterval is how often the Leader counts the open Alert Groups for muster_alert_groups.
const AlertGroupGaugeInterval = 15 * time.Second

// HeartbeatCheckInterval is how often the Leader runs the Heartbeat check, and StaleScanInterval the Stale scan.
const (
	HeartbeatCheckInterval = 10 * time.Second
	StaleScanInterval      = 30 * time.Second
)

// PruneBatch is the most rows one delete of short-lived pruning removes, so that a large backlog is deleted in short
// statements that hold their row locks briefly.
const PruneBatch = 1000

// Task is one Leader task: it runs when the lock is taken and then every Every, and at once when Wake delivers,
// until the lock is lost. Every Leader task is safe to run twice, because a frozen old Leader can overlap with its
// successor for a moment (ADR-0007).
type Task struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
	// Wake, when set, runs the task out of its interval.
	Wake <-chan struct{}
}

// Wakes runs Leader tasks out of their interval: in development mode, the development clock moved, and the
// Heartbeat check and the Stale scan act at the new time at once instead of at their next tick.
type Wakes struct {
	mu    sync.Mutex
	chans []chan struct{}
}

// Wake runs every task that listens now, or right after its current run; wakes that come meanwhile are merged.
func (w *Wakes) Wake() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.chans {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// channel is a new channel that Wake delivers to; nil, which never delivers, for nil Wakes.
func (w *Wakes) channel() <-chan struct{} {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	c := make(chan struct{}, 1)
	w.chans = append(w.chans, c)
	return c
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

	// IngestBacklog sets muster_ingest_backlog from the pending Stored Snapshots of the Organizations.
	IngestBacklog func(ctx context.Context, orgs []int64) error
	// AlertRetention deletes, in batches, the Alerts of the Organization orgID resolved longer than
	// retention.alert_details before now (C-06.FR-19) and returns how many it deleted.
	AlertRetention func(ctx context.Context, orgID int64, now time.Time) (int64, error)

	// HeartbeatCheck makes the overdue live Heartbeats of the Organizations lost and exports muster_heartbeat_lost
	// (C-07.FR-4, C-07.FR-8).
	HeartbeatCheck func(ctx context.Context, orgs []int64) error
	// StaleScan runs the Stale scan of the Organization orgID (C-06.FR-9, C-07.FR-5).
	StaleScan func(ctx context.Context, orgID int64) error
	// AlertGroupGauges sets muster_alert_groups from the open Alert Groups of the Organization orgID (C-09).
	AlertGroupGauges func(ctx context.Context, orgID int64) error
	// ClockMoved, in development mode, wakes the Heartbeat check and the Stale scan when the development clock moved;
	// nil otherwise.
	ClockMoved *Wakes
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
// pruning of replica records, the pruning of short-lived state, the ingestion backlog, the retention of the Alerts
// view, the Heartbeat check, the Stale scan and the count of open Alert Groups. Later capabilities add theirs here: Telegram polling, the outgoing
// heartbeat and the OIDC client secret expiry check. The Keeper calls the result at every leadership, so each one
// starts with a takeover; the Heartbeat check waits for it, so that it measures the timeouts from the end of a
// downtime the takeover records (C-07.FR-4).
func Tasks(w Work) func() []Task {
	heartbeatWake, staleWake := w.ClockMoved.channel(), w.ClockMoved.channel()
	return func() []Task {
		var takenOver atomic.Bool
		return []Task{
			{Name: "partition_maintenance", Every: MaintenanceInterval, Run: func(ctx context.Context) error {
				_ = w.MaintainPartitions(ctx) // logged as partition_maintenance_failed and retried at the next run
				return nil
			}},
			{Name: "alive_mark", Every: AliveMarkInterval, Run: func(ctx context.Context) error {
				if takenOver.Load() {
					return w.Alive.Mark(ctx)
				}
				if err := w.Alive.TakeOver(ctx); err != nil {
					return err
				}
				takenOver.Store(true)
				return nil
			}},
			{Name: "replica_pruning", Every: MaintenanceInterval, Run: w.PruneReplicas},
			{Name: "short_lived_pruning", Every: MaintenanceInterval, Run: w.pruneShortLived},
			{Name: "ingest_backlog", Every: BacklogInterval, Run: w.ingestBacklog},
			{Name: "alert_retention", Every: MaintenanceInterval, Run: w.alertRetention},
			{Name: "heartbeat_check", Every: HeartbeatCheckInterval, Wake: heartbeatWake,
				Run: func(ctx context.Context) error {
					if !takenOver.Load() {
						return nil // the next run, once the takeover recorded any downtime
					}
					return w.heartbeatCheck(ctx)
				}},
			{Name: "stale_scan", Every: StaleScanInterval, Wake: staleWake, Run: w.staleScan},
			{Name: "alert_group_gauges", Every: AlertGroupGaugeInterval, Run: w.alertGroupGauges},
		}
	}
}

// alertGroupGauges counts the open Alert Groups of every Organization; counting twice sets the same values.
func (w Work) alertGroupGauges(ctx context.Context) error {
	if w.AlertGroupGauges == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations to count the alert groups of: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		errs = append(errs, w.AlertGroupGauges(ctx, org))
	}
	return errors.Join(errs...)
}

// heartbeatCheck runs the Heartbeat check over every Organization.
func (w Work) heartbeatCheck(ctx context.Context) error {
	if w.HeartbeatCheck == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for the heartbeat check: %w", err)
	}
	return w.HeartbeatCheck(ctx, orgs)
}

// staleScan runs the Stale scan in every Organization; a failed Organization does not stop the others.
func (w Work) staleScan(ctx context.Context) error {
	if w.StaleScan == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for the stale scan: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		errs = append(errs, w.StaleScan(ctx, org))
	}
	return errors.Join(errs...)
}

// ingestBacklog counts the pending Stored Snapshots of every Organization; counting twice sets the same value.
func (w Work) ingestBacklog(ctx context.Context) error {
	if w.IngestBacklog == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations to count the backlog of: %w", err)
	}
	return w.IngestBacklog(ctx, orgs)
}

// alertRetention runs the retention of the Alerts view in every Organization at the same now, on the business clock;
// a failed Organization does not stop the others. Running it twice deletes nothing more.
func (w Work) alertRetention(ctx context.Context) error {
	if w.AlertRetention == nil {
		return nil
	}
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return fmt.Errorf("list the organizations for the alert retention: %w", err)
	}
	now := w.Business.Now()
	var errs []error
	for _, org := range orgs {
		if _, err := w.AlertRetention(ctx, org, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
