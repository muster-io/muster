// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/heartbeat/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// Checker is the Leader's Heartbeat check (C-07.FR-4, C-07.FR-8): a live Integration whose last signal is older than
// its timeout becomes Heartbeat lost and raises MusterHeartbeatLost, and muster_heartbeat_lost follows the states.
// The check is safe to run twice and beside another Leader's: each change locks the Integration and checks again.
type Checker struct {
	Store Store
	// Business is the business clock, which the signals are recorded on.
	Business clock.Clock
	Log      *logging.Logger
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL.
	RunbookBase string

	mu sync.Mutex
	// exported are the integrations of the muster_heartbeat_lost series this replica exports.
	exported map[string]bool
}

// Check runs the Heartbeat check in every Organization of orgs at the same now. A failed Organization does not stop
// the others.
func (c *Checker) Check(ctx context.Context, orgs []int64) error {
	now := c.Business.Now().UTC()
	from, err := c.measuredFrom(ctx)
	if err != nil {
		return err
	}
	var errs []error
	states, complete := map[string]string{}, true
	for _, org := range orgs {
		errs = append(errs, c.checkOrganization(ctx, org, now, from))
		rows, err := c.Store.ListHeartbeats(ctx, org)
		if err != nil {
			errs, complete = append(errs, fmt.Errorf("list the heartbeats: %w", err)), false
			continue
		}
		for _, r := range rows {
			states[r.PublicID] = r.HeartbeatState
		}
	}
	c.export(states, complete)
	return errors.Join(errs...)
}

// measuredFrom is the moment the current Leader started leading when it recorded a downtime on taking over, or nil:
// after Muster itself was down, the timeout counts from there, so that the outage does not make every Integration
// Heartbeat lost (C-07.FR-4).
func (c *Checker) measuredFrom(ctx context.Context) (*time.Time, error) {
	row, err := c.Store.GetLeaderStart(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read when the leader started: %w", err)
	case !row.AfterDowntime || !row.LeaderSince.Valid:
		return nil, nil
	}
	t := row.LeaderSince.Time.UTC()
	return &t, nil
}

// overdue reports whether a live Heartbeat whose last signal was at last is overdue at now, measured from the later
// of last and from.
func overdue(last, from *time.Time, timeout time.Duration, now time.Time) bool {
	start := last
	if from != nil && (start == nil || from.After(*start)) {
		start = from
	}
	return start != nil && now.Sub(*start) > timeout
}

func (c *Checker) checkOrganization(ctx context.Context, org int64, now time.Time, from *time.Time) error {
	p := dbgen.ListOverdueParams{OrgID: org, Now: now}
	if from != nil {
		p.MeasuredFrom = pgtype.Timestamptz{Time: *from, Valid: true}
	}
	ids, err := c.Store.ListOverdue(ctx, p)
	if err != nil {
		return fmt.Errorf("list the overdue heartbeats: %w", err)
	}
	raiser := internalalerts.NewRaiser(org, c.RunbookBase)
	var errs []error
	for _, id := range ids {
		errs = append(errs, c.lose(ctx, raiser, org, id, now, from))
	}
	return errors.Join(errs...)
}

// lose makes the Heartbeat of the Integration id lost when it is still live and overdue under the lock, with
// heartbeat_lost_since its last signal, and raises MusterHeartbeatLost with the Integration's Static labels.
func (c *Checker) lose(ctx context.Context, raiser *internalalerts.Raiser, org, id int64, now time.Time,
	from *time.Time) error {
	var (
		publicID string
		last     *time.Time
	)
	err := c.Store.InTx(ctx, func(q Queries) error {
		publicID = ""
		row, err := q.LockHeartbeat(ctx, dbgen.LockHeartbeatParams{OrgID: org, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock the heartbeat of integration %d: %w", id, err)
		}
		last = timeOf(row.HeartbeatLastSignalAt)
		timeout := time.Duration(row.HeartbeatTimeoutSeconds) * time.Second
		if row.HeartbeatState != StateLive || last == nil || !overdue(last, from, timeout, now) {
			return nil
		}
		if err := q.MarkLost(ctx, dbgen.MarkLostParams{OrgID: org, ID: id, LostSince: *last}); err != nil {
			return fmt.Errorf("mark the heartbeat of %s lost: %w", row.PublicID, err)
		}
		static := map[string]string{}
		if err := json.Unmarshal(row.StaticLabels, &static); err != nil {
			return fmt.Errorf("read the static labels of %s: %w", row.PublicID, err)
		}
		if err := raiser.Raise(ctx, q, now, internalalerts.HeartbeatLost,
			internalalerts.Entity{ID: row.PublicID, Name: row.Name}, static); err != nil {
			return fmt.Errorf("raise MusterHeartbeatLost of %s: %w", row.PublicID, err)
		}
		publicID = row.PublicID
		return q.Notify(ctx, db.Hint{OrgID: org, Type: integrations.Hint, ID: row.PublicID})
	})
	if err != nil || publicID == "" {
		return err
	}
	c.Log.Log(ctx, logging.HeartbeatLost, logging.F("integration", publicID), logging.F("last_signal_at", *last))
	return nil
}

// export sets muster_heartbeat_lost to 1 for the lost Heartbeats and 0 for the others that are on and, when states
// is complete, removes the series of Integrations whose Heartbeat is off or that are deleted.
func (c *Checker) export(states map[string]string, complete bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.exported {
		if _, ok := states[id]; !ok && complete {
			metrics.HeartbeatLost.Delete(id)
			delete(c.exported, id)
		}
	}
	if c.exported == nil {
		c.exported = map[string]bool{}
	}
	for id, state := range states {
		v := 0.0
		if state == StateLost {
			v = 1
		}
		metrics.HeartbeatLost.With(id).Set(v)
		c.exported[id] = true
	}
}
