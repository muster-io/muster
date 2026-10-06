// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package db

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
)

// The claim of ADR-0006 and schema.md §5, shared by every worker that takes rows with a deadline — OIDC re-checks,
// ingestion, timers and delivery: claim due rows with FOR UPDATE SKIP LOCKED and set a lease in a short transaction,
// commit, do the slow work (HTTP calls) outside any transaction, then record the outcome in another short transaction
// that still holds the lease. A row whose lease ran out is claimed again by any replica. Due times follow the business
// clock, which the development clock moves; leases follow the real clock, which agrees across replicas.

// Lease is how a worker of one replica claims rows: Owner names the replica in lease_owner, and a claim holds a row
// for Duration on the real clock.
type Lease struct {
	Owner    string
	Duration time.Duration
	Clocks   clock.Clocks
}

// ClaimParams are the values one claim compares and sets: rows due at or before Due (business clock) whose lease is
// free or ran out at or before Now (real clock) get Owner and LeaseUntil, at most Limit of them.
type ClaimParams struct {
	Owner      string
	Due        time.Time
	Now        time.Time
	LeaseUntil time.Time
	Limit      int32
}

// Params are the values of a claim of at most limit rows, read from the clocks now.
func (l Lease) Params(limit int32) ClaimParams {
	now := l.Clocks.Real.Now().UTC()
	return ClaimParams{Owner: l.Owner, Due: l.Clocks.Business.Now().UTC(), Now: now, LeaseUntil: now.Add(l.Duration),
		Limit: limit}
}

// Held reports whether a lease that runs until until is still held at the real time now.
func Held(until, now time.Time) bool {
	return now.Before(until)
}

// Beginner starts the short transaction of a claim; the main pool implements it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Claim runs claim in one short transaction with the values of l for at most limit rows and returns the rows it
// claimed; they are committed, and so leased, when Claim returns. claim selects the due rows FOR UPDATE SKIP LOCKED
// and sets their lease, so that two claimers never take the same row.
func Claim[T any](ctx context.Context, b Beginner, l Lease, limit int32,
	claim func(ctx context.Context, tx pgx.Tx, p ClaimParams) ([]T, error)) ([]T, error) {
	var rows []T
	err := pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error {
		var err error
		rows, err = claim(ctx, tx, l.Params(limit))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("claim due rows: %w", err)
	}
	return rows, nil
}

// Backoff is the wait after failures consecutive failed rounds: exponential from base, at most limit, with full
// jitter (a uniform share of the bound that random, in [0, 1), picks), and never below base/2, so that replicas that
// failed together do not retry together and none spins.
func Backoff(failures int, base, limit time.Duration, random func() float64) time.Duration {
	bound := base
	for i := 1; i < failures && bound < limit; i++ {
		bound *= 2
	}
	bound = min(bound, limit)
	return max(base/2, time.Duration(random()*float64(bound)))
}

// Poller runs the rounds of a worker: one round every Interval, give or take a tenth for jitter, and after a failed
// round the Backoff from Interval up to MaxBackoff.
type Poller struct {
	Interval   time.Duration
	MaxBackoff time.Duration
	// Wait waits d; nil is time.After.
	Wait func(d time.Duration) <-chan time.Time
	// Random is uniform in [0, 1); nil is math/rand/v2.
	Random func() float64
}

// Run calls round until ctx ends; round reports whether it failed, and logs why itself.
func (p Poller) Run(ctx context.Context, round func(ctx context.Context) error) {
	wait, random := p.Wait, p.Random
	if wait == nil {
		wait = time.After
	}
	if random == nil {
		random = rand.Float64
	}
	failures := 0
	for {
		d := p.Interval + time.Duration((random()-0.5)*float64(p.Interval)/5)
		if failures > 0 {
			d = Backoff(failures, p.Interval, max(p.MaxBackoff, p.Interval), random)
		}
		select {
		case <-ctx.Done():
			return
		case <-wait(d):
		}
		if err := round(ctx); err != nil && ctx.Err() == nil {
			failures++
		} else {
			failures = 0
		}
	}
}
