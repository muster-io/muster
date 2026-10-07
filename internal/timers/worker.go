// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package timers is the worker of the timer rows (C-09.FR-12, ADR-0006, ADR-0007): on every replica it claims the
// due timers of each Organization with FOR UPDATE SKIP LOCKED and a lease, fires each in a transaction of its own
// through the handler of its kind, and waits for the earliest deadline, a notification of a new timer or a move of
// the development clock. Deadlines are business times and leases real times; a timer overdue after downtime fires
// once, and a timer whose lease ran out is claimed again by any replica.
package timers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/timers/dbgen"
)

// The defaults of the worker: the lease of a claimed timer, the rows one claim takes, the longest wait between rounds
// when no deadline is nearer, which bounds what a missed notification costs, the shortest one, so that a due timer
// another replica is claiming right now is not polled in a tight loop, and the first wait after a failed round.
const (
	Lease          = time.Minute
	Batch          = 100
	MaxWait        = 30 * time.Second
	MinWait        = 100 * time.Millisecond
	FailureBackoff = time.Second
)

// Timer is a claimed timer row.
type Timer struct {
	ID           int64
	AlertGroupID *int64
	StormID      *int64
	Kind         string
	Deadline     time.Time
	Attempts     int64
}

// Handler fires one timer of its kind in the transaction tx of the timer, for the Organization orgID, and returns
// what to run once that transaction committed, or nil. A handler checks the state of its subject itself: a timer
// that is no longer due for it changes nothing.
type Handler func(ctx context.Context, tx dbgen.DBTX, orgID int64, t Timer) (func(context.Context), error)

// queries are the queries of the worker.
type queries interface {
	ClaimDueTimers(ctx context.Context, arg dbgen.ClaimDueTimersParams) ([]dbgen.ClaimDueTimersRow, error)
	HoldsTimer(ctx context.Context, arg dbgen.HoldsTimerParams) (bool, error)
	DeleteFiredTimer(ctx context.Context, arg dbgen.DeleteFiredTimerParams) error
	NextTimerDeadline(ctx context.Context, arg dbgen.NextTimerDeadlineParams) (dbgen.NextTimerDeadlineRow, error)
}

// Store claims, fires and schedules the timers.
type Store interface {
	// Claim leases at most limit due timers of the kinds of the Organization orgID.
	Claim(ctx context.Context, l db.Lease, orgID int64, kinds []string, limit int32) ([]Timer, error)
	// Fire runs h for a claimed timer in a transaction that still holds its lease, and returns what h returns.
	Fire(ctx context.Context, l db.Lease, orgID int64, t Timer, h Handler) (func(context.Context), error)
	// Next is how long until a timer of the kinds of the Organization can be claimed, at most limit: until the
	// earliest deadline of a free one on the business clock, or the end of the earliest lease on the real clock.
	Next(ctx context.Context, l db.Lease, orgID int64, kinds []string, limit time.Duration) (time.Duration, error)
}

// pgStore is the Store over the database.
type pgStore struct {
	begin   db.Beginner
	db      dbgen.DBTX
	queries func(dbgen.DBTX) queries
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{begin: pool, db: pool, queries: func(d dbgen.DBTX) queries { return dbgen.New(d) }}
}

// Claim leases at most limit due timers of the Organization in a short transaction.
func (s pgStore) Claim(ctx context.Context, l db.Lease, orgID int64, kinds []string, limit int32) ([]Timer, error) {
	return db.Claim(ctx, s.begin, l, limit, func(ctx context.Context, tx pgx.Tx, p db.ClaimParams) ([]Timer, error) {
		rows, err := s.queries(tx).ClaimDueTimers(ctx, dbgen.ClaimDueTimersParams{OrgID: orgID, Owner: p.Owner,
			LeaseUntil: p.LeaseUntil, Due: p.Due, Now: p.Now, Kinds: kinds, Lim: p.Limit})
		if err != nil {
			return nil, err
		}
		out := make([]Timer, len(rows))
		for i, r := range rows {
			out[i] = Timer{ID: r.ID, AlertGroupID: int8Of(r.AlertGroupID), StormID: int8Of(r.StormID), Kind: r.Kind,
				Deadline: r.Deadline.UTC(), Attempts: r.Attempts}
		}
		return out, nil
	})
}

// Fire runs the handler of a claimed timer in one transaction that still holds its lease, and deletes the timer
// unless it was rescheduled meanwhile; a timer whose lease was lost, or that was rescheduled, is left alone.
func (s pgStore) Fire(ctx context.Context, l db.Lease, orgID int64, t Timer, h Handler) (func(context.Context),
	error) {
	var after func(context.Context)
	err := pgx.BeginFunc(ctx, s.begin, func(tx pgx.Tx) error {
		after = nil
		q := s.queries(tx)
		held, err := q.HoldsTimer(ctx, dbgen.HoldsTimerParams{OrgID: orgID, ID: t.ID, Owner: l.Owner,
			Now: l.Clocks.Real.Now().UTC()})
		if err != nil {
			return fmt.Errorf("check the lease: %w", err)
		}
		if !held {
			return nil
		}
		if after, err = h(ctx, tx, orgID, t); err != nil {
			return err
		}
		if err := q.DeleteFiredTimer(ctx, dbgen.DeleteFiredTimerParams{OrgID: orgID, ID: t.ID, Owner: l.Owner,
			Deadline: t.Deadline}); err != nil {
			return fmt.Errorf("delete the fired timer: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return after, nil
}

// Next is how long until a timer of the kinds can be claimed, at most limit.
func (s pgStore) Next(ctx context.Context, l db.Lease, orgID int64, kinds []string, limit time.Duration) (
	time.Duration, error) {
	realNow := l.Clocks.Real.Now().UTC()
	r, err := s.queries(s.db).NextTimerDeadline(ctx, dbgen.NextTimerDeadlineParams{OrgID: orgID, Kinds: kinds,
		Now: realNow})
	if err != nil {
		return 0, fmt.Errorf("read the next timer deadline: %w", err)
	}
	d := limit
	if r.FreeDeadline.Year() > 1 {
		d = min(d, r.FreeDeadline.Sub(l.Clocks.Business.Now()))
	}
	if r.LeaseEnd.Year() > 1 {
		d = min(d, r.LeaseEnd.Sub(realNow))
	}
	return d, nil
}

// Worker fires the due timers of this replica.
type Worker struct {
	Store Store
	// Lease names this replica in lease_owner and carries the clocks: due times on the business clock, leases on
	// the real clock.
	Lease db.Lease
	// Organizations lists the Organizations whose timers it fires: one in L1.
	Organizations func(ctx context.Context) ([]int64, error)
	// Handlers fire each kind; the worker claims only these kinds.
	Handlers map[string]Handler
	Log      *logging.Logger
	// MaxWait bounds the wait between rounds, and Batch the timers one claim takes; zero is the default.
	MaxWait time.Duration
	Batch   int32
	// Wait waits for d or until a wake arrives; nil waits on the real clock.
	Wait func(ctx context.Context, d time.Duration, wake <-chan struct{})

	once sync.Once
	wake chan struct{}
}

func (w *Worker) init() {
	w.once.Do(func() { w.wake = make(chan struct{}, 1) })
}

// Wake makes the worker run a round at once: a timer was set, the development clock moved, or the LISTEN is back.
func (w *Worker) Wake() {
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run fires timers until ctx ends: a round, then a wait until the next timer can be claimed — its deadline on the
// business clock or the end of its lease on the real clock — between MinWait and MaxWait, or until woken; after a
// failed round, a backoff.
func (w *Worker) Run(ctx context.Context) {
	w.init()
	wait := w.Wait
	if wait == nil {
		wait = waitReal
	}
	failures := 0
	for ctx.Err() == nil {
		next, err := w.Round(ctx)
		d := max(next, MinWait)
		if err != nil {
			failures++
			d = db.Backoff(failures, FailureBackoff, w.maxWait(), rand.Float64)
		} else {
			failures = 0
		}
		wait(ctx, d, w.wake)
	}
}

func (w *Worker) maxWait() time.Duration {
	if w.MaxWait <= 0 {
		return MaxWait
	}
	return w.MaxWait
}

func waitReal(ctx context.Context, d time.Duration, wake <-chan struct{}) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-t.C:
	}
}

// Round fires every due timer of the kinds it has handlers for in every Organization, claiming Batch at a time, and
// returns how long until the next one can be claimed, at most MaxWait. A timer that fails is logged and fires again
// once its lease runs out; the round goes on. Timers of other kinds wait for the release that fires them.
func (w *Worker) Round(ctx context.Context) (time.Duration, error) {
	orgs, err := w.Organizations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list the organizations for the timers: %w", err)
	}
	batch := w.Batch
	if batch <= 0 {
		batch = Batch
	}
	kinds := slices.Sorted(maps.Keys(w.Handlers))
	next := w.maxWait()
	var errs []error
	for _, org := range orgs {
		for {
			claimed, err := w.Store.Claim(ctx, w.Lease, org, kinds, batch)
			if err != nil {
				errs = append(errs, err)
				break
			}
			for _, t := range claimed {
				w.fire(ctx, org, t)
			}
			if len(claimed) < int(batch) {
				break
			}
		}
		n, err := w.Store.Next(ctx, w.Lease, org, kinds, next)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		next = min(next, n)
	}
	return next, errors.Join(errs...)
}

// fire fires one claimed timer and runs what it returns once committed; a failure is logged.
func (w *Worker) fire(ctx context.Context, org int64, t Timer) {
	h := w.Handlers[t.Kind]
	if h == nil {
		w.Log.Log(ctx, logging.TimerFailed, logging.F("kind", t.Kind),
			logging.F("error", "no handler for the timer kind"))
		return
	}
	after, err := w.Store.Fire(ctx, w.Lease, org, t, h)
	if err != nil {
		if ctx.Err() == nil {
			w.Log.Log(ctx, logging.TimerFailed, logging.F("kind", t.Kind), logging.F("error", err.Error()))
		}
		return
	}
	if after != nil {
		after(ctx)
	}
}

func int8Of(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
