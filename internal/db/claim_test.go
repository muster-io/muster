// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
)

var (
	business0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	real0     = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
)

// TestLeaseParams: due times come from the business clock, the lease from the real clock.
func TestLeaseParams(t *testing.T) {
	l := Lease{Owner: "r1", Duration: time.Minute,
		Clocks: clock.Clocks{Business: clock.NewManual(business0), Real: clock.NewManual(real0)}}
	p := l.Params(8)
	if p.Owner != "r1" || !p.Due.Equal(business0) || !p.Now.Equal(real0) || !p.LeaseUntil.Equal(real0.Add(time.Minute)) ||
		p.Limit != 8 {
		t.Errorf("Params = %+v", p)
	}
	if !Held(p.LeaseUntil, real0) || Held(p.LeaseUntil, p.LeaseUntil) {
		t.Error("Held")
	}
}

// fakeTx is a transaction that records whether it committed.
type fakeTx struct {
	pgx.Tx
	committed, rolledBack bool
}

func (t *fakeTx) Commit(context.Context) error {
	t.committed = true
	return nil
}

func (t *fakeTx) Rollback(context.Context) error {
	if !t.committed {
		t.rolledBack = true
	}
	return nil
}

type fakeBeginner struct {
	tx  *fakeTx
	err error
}

func (b *fakeBeginner) Begin(context.Context) (pgx.Tx, error) {
	if b.err != nil {
		return nil, b.err
	}
	b.tx = &fakeTx{}
	return b.tx, nil
}

// TestClaim: the claim runs in one transaction with the values of the lease, committed on success and rolled back on
// a failure.
func TestClaim(t *testing.T) {
	l := Lease{Owner: "r1", Duration: time.Minute,
		Clocks: clock.Clocks{Business: clock.NewManual(business0), Real: clock.NewManual(real0)}}
	b := &fakeBeginner{}
	var got ClaimParams
	rows, err := Claim(t.Context(), b, l, 3, func(_ context.Context, tx pgx.Tx, p ClaimParams) ([]int64, error) {
		if tx != b.tx {
			t.Error("the claim ran outside the transaction")
		}
		got = p
		return []int64{1, 2}, nil
	})
	if err != nil || len(rows) != 2 || !b.tx.committed || got.Limit != 3 || got.Owner != "r1" {
		t.Fatalf("Claim = %v, %v, params %+v", rows, err, got)
	}
	rows, err = Claim(t.Context(), b, l, 3, func(context.Context, pgx.Tx, ClaimParams) ([]int64, error) {
		return []int64{9}, errors.New("boom")
	})
	if err == nil || rows != nil || !b.tx.rolledBack {
		t.Errorf("a failed claim = %v, %v", rows, err)
	}
	b.err = errors.New("no connection")
	if _, err := Claim(t.Context(), b, l, 3, func(context.Context, pgx.Tx, ClaimParams) ([]int64, error) {
		return nil, nil
	}); err == nil {
		t.Error("a claim without a transaction")
	}
}

// TestBackoff: exponential from base up to the limit, with full jitter, never below base/2.
func TestBackoff(t *testing.T) {
	for _, c := range []struct {
		failures int
		random   float64
		want     time.Duration
	}{
		{1, 0.999, 1998 * time.Millisecond},
		{1, 0, time.Second},
		{3, 0.5, 4 * time.Second},
		{10, 0.5, 30 * time.Second},
		{10, 0.999, 59940 * time.Millisecond},
	} {
		got := Backoff(c.failures, 2*time.Second, time.Minute, func() float64 { return c.random })
		if got.Round(time.Millisecond) != c.want {
			t.Errorf("Backoff(%d, %v) = %v, want %v", c.failures, c.random, got, c.want)
		}
	}
}

// TestPoller: a round every interval with jitter, the backoff after failed rounds, and the end with the context.
func TestPoller(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var waits []time.Duration
	results := []error{nil, errors.New("down"), errors.New("down"), nil}
	p := Poller{Interval: 2 * time.Second, MaxBackoff: time.Minute, Random: func() float64 { return 0.75 },
		Wait: func(d time.Duration) <-chan time.Time {
			waits = append(waits, d)
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		}}
	rounds := 0
	p.Run(ctx, func(context.Context) error {
		// A round that races the end of the context may run once more; it changes nothing.
		if rounds >= len(results) {
			return nil
		}
		err := results[rounds]
		rounds++
		if rounds == len(results) {
			cancel()
		}
		return err
	})
	want := []time.Duration{2100 * time.Millisecond, 2100 * time.Millisecond, 1500 * time.Millisecond,
		3 * time.Second}
	if rounds != 4 || len(waits) < len(want) {
		t.Fatalf("%d rounds, waits %v", rounds, waits)
	}
	for i, w := range want {
		if waits[i] != w {
			t.Errorf("wait %d = %v, want %v", i, waits[i], w)
		}
	}
	// The defaults: time.After and math/rand, stopped by the context before the first round.
	stopped, stop := context.WithCancel(t.Context())
	stop()
	Poller{Interval: time.Hour}.Run(stopped, func(context.Context) error {
		t.Error("a round ran after the context ended")
		return nil
	})
}
