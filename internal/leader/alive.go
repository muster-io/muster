// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package leader

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/leader/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

const (
	// AliveMarkInterval is leader.alive_mark_interval: how often the Leader writes the alive mark.
	AliveMarkInterval = 30 * time.Second
	// AbsenceNotice is leader.absence_notice: an alive mark older than this means that no replica leads, and a gap
	// longer than this, with no replica running, is downtime.
	AbsenceNotice = 2 * time.Minute

	// The recovery window after downtime, recovery.banner_duration (P-01): until the longest repeat interval
	// learned from Alertmanager has passed, at most RecoveryMax; RecoveryUnlearned when nothing is learned.
	RecoveryMax       = time.Hour
	RecoveryUnlearned = 15 * time.Minute
)

// Queries are the Leader's queries; *dbgen.Queries implements them.
type Queries interface {
	EnsureRuntimeState(ctx context.Context, updatedAt time.Time) error
	LockRuntimeState(ctx context.Context) (dbgen.LockRuntimeStateRow, error)
	GetRuntimeState(ctx context.Context) (dbgen.GetRuntimeStateRow, error)
	LatestOtherReplicaRefresh(ctx context.Context, arg dbgen.LatestOtherReplicaRefreshParams) (time.Time, error)
	RecordDowntime(ctx context.Context, arg dbgen.RecordDowntimeParams) error
	LatestDowntimeEnd(ctx context.Context) (time.Time, error)
	TakeOver(ctx context.Context, arg dbgen.TakeOverParams) error
	MarkAlive(ctx context.Context, arg dbgen.MarkAliveParams) (int64, error)
	ListOrganizationIDs(ctx context.Context) ([]int64, error)
	LongestLearnedRepeatInterval(ctx context.Context, orgID int64) (int64, error)
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{Queries: dbgen.New(pool), pool: pool}
}

type pgStore struct {
	*dbgen.Queries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(dbgen.New(tx)) })
}

// Alive keeps the alive mark of the Leader in runtime_state, on the business clock, which downtime and the recovery
// window follow too.
type Alive struct {
	store   Store
	clocks  clock.Clocks
	log     *logging.Logger
	replica string
}

// NewAlive returns the alive mark of the replica id.
func NewAlive(s Store, clocks clock.Clocks, log *logging.Logger, replica string) *Alive {
	return &Alive{store: s, clocks: clocks, log: log, replica: replica}
}

// Mark refreshes the alive mark.
func (a *Alive) Mark(ctx context.Context) error {
	now := a.clocks.Business.Now()
	n, err := a.store.MarkAlive(ctx, dbgen.MarkAliveParams{LeaderReplicaID: a.replica, Now: now})
	if err != nil {
		return fmt.Errorf("write the alive mark: %w", err)
	}
	if n == 0 {
		// The row is written at takeover; without it, take over again.
		return a.TakeOver(ctx)
	}
	return nil
}

// Downtime is a period during which Muster was unavailable.
type Downtime struct {
	Start, End    time.Time
	RecoveryUntil time.Time
}

// TakeOver records this replica as the Leader. When neither a Leader nor any other replica was alive for longer
// than AbsenceNotice, the gap is downtime: it is stored in downtime_periods, logged as downtime_recorded and opens
// the recovery window. It runs in one transaction that locks runtime_state, so two Leaders that overlap record one
// downtime.
func (a *Alive) TakeOver(ctx context.Context) error {
	var recorded *Downtime
	err := a.store.InTx(ctx, func(q Queries) error {
		recorded = nil
		now := a.clocks.Business.Now()
		if err := q.EnsureRuntimeState(ctx, now); err != nil {
			return fmt.Errorf("create the runtime state: %w", err)
		}
		row, err := q.LockRuntimeState(ctx)
		if err != nil {
			return fmt.Errorf("read the runtime state: %w", err)
		}
		d, err := a.downtime(ctx, q, row, now)
		if err != nil {
			return err
		}
		params := dbgen.TakeOverParams{LeaderReplicaID: a.replica, Now: now}
		if d != nil {
			if err := q.RecordDowntime(ctx, dbgen.RecordDowntimeParams{StartedAt: d.Start, EndedAt: d.End,
				RecordedAt: now}); err != nil {
				return fmt.Errorf("record the downtime: %w", err)
			}
			params.RecoveryUntil = pgtype.Timestamptz{Time: d.RecoveryUntil, Valid: true}
		}
		if err := q.TakeOver(ctx, params); err != nil {
			return fmt.Errorf("record the Leader: %w", err)
		}
		recorded = d
		return nil
	})
	if err != nil {
		return err
	}
	if recorded != nil {
		a.log.Log(ctx, logging.DowntimeRecorded, logging.F("started_at", recorded.Start.UTC()),
			logging.F("ended_at", recorded.End.UTC()),
			logging.F("duration_seconds", recorded.End.Sub(recorded.Start).Seconds()))
	}
	return nil
}

// downtime is the gap before now that counts as downtime, or nil. The gap starts at the later of the last alive mark
// and the last key-record refresh of any other replica that ran across it: while a replica ran, Muster ingested and
// delivered even without a Leader. A replica counts when it started no later than AbsenceNotice after the mark, so
// that replicas starting together after an outage do not hide it. A database without an alive mark has never had a
// Leader and has no downtime.
func (a *Alive) downtime(ctx context.Context, q Queries, row dbgen.LockRuntimeStateRow, now time.Time) (*Downtime,
	error,
) {
	if !row.AliveAt.Valid {
		return nil, nil
	}
	start := row.AliveAt.Time
	// Replica records are on the real clock; the alive mark is on the business clock.
	offset := a.clocks.Business.Now().Sub(a.clocks.Real.Now())
	refreshed, err := q.LatestOtherReplicaRefresh(ctx, dbgen.LatestOtherReplicaRefreshParams{
		ReplicaID: a.replica, StartedBefore: start.Add(AbsenceNotice - offset)})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("read the replica records: %w", err)
	default:
		if refreshed = refreshed.Add(offset); refreshed.After(start) {
			start = refreshed
		}
	}
	if now.Sub(start) <= AbsenceNotice {
		return nil, nil
	}
	window, err := a.recoveryWindow(ctx, q)
	if err != nil {
		return nil, err
	}
	return &Downtime{Start: start, End: now, RecoveryUntil: now.Add(window)}, nil
}

// recoveryWindow is recovery.banner_duration: the longest repeat interval learned from any Organization's
// Alertmanager routes, at most RecoveryMax, or RecoveryUnlearned when nothing is learned.
func (a *Alive) recoveryWindow(ctx context.Context, q Queries) (time.Duration, error) {
	orgs, err := q.ListOrganizationIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("list the organizations: %w", err)
	}
	var longest time.Duration
	for _, org := range orgs {
		ms, err := q.LongestLearnedRepeatInterval(ctx, org)
		if err != nil {
			return 0, fmt.Errorf("read the learned repeat intervals: %w", err)
		}
		longest = max(longest, time.Duration(ms)*time.Millisecond)
	}
	if longest == 0 {
		return RecoveryUnlearned, nil
	}
	return min(longest, RecoveryMax), nil
}

// Notices reads the state of the Organization-wide notices at now, a business time: "recovering after downtime"
// until runtime_state.recovery_until, and "no replica is leading" while the alive mark is older than AbsenceNotice.
func Notices(ctx context.Context, q Queries, now time.Time) ([]organization.Notice, error) {
	var st organization.RuntimeState
	row, err := q.GetRuntimeState(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("read the runtime state: %w", err)
	default:
		st.AliveAt = timeOf(row.AliveAt)
		st.RecoveryUntil = timeOf(row.RecoveryUntil)
	}
	end, err := q.LatestDowntimeEnd(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("read the downtime periods: %w", err)
	default:
		st.DowntimeEnd = end
	}
	return organization.ActiveNotices(st, now, AbsenceNotice), nil
}

func timeOf(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
