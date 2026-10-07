// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package timers

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/timers/dbgen"
)

var (
	business0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	real0     = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	errBoom   = errors.New("boom")
)

// fakeTimer is a timers row.
type fakeTimer struct {
	dbgen.ClaimDueTimersRow
	owner string
	until time.Time
}

// fakeQueries is the timers table in memory with the semantics of the claim: due on the business clock, free or
// expired on the real clock, earliest first, at most the limit.
type fakeQueries struct {
	mu     sync.Mutex
	rows   []*fakeTimer
	fail   map[string]error
	claims []dbgen.ClaimDueTimersParams
}

func (q *fakeQueries) ClaimDueTimers(_ context.Context, arg dbgen.ClaimDueTimersParams) ([]dbgen.ClaimDueTimersRow,
	error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claims = append(q.claims, arg)
	if err := q.fail["ClaimDueTimers"]; err != nil {
		return nil, err
	}
	slices.SortFunc(q.rows, func(a, b *fakeTimer) int { return a.Deadline.Compare(b.Deadline) })
	var out []dbgen.ClaimDueTimersRow
	for _, r := range q.rows {
		if len(out) == int(arg.Lim) {
			break
		}
		if !r.Deadline.After(arg.Due) && (r.owner == "" || !r.until.After(arg.Now)) &&
			slices.Contains(arg.Kinds, r.Kind) {
			r.owner, r.until = arg.Owner, arg.LeaseUntil
			r.Attempts++
			out = append(out, r.ClaimDueTimersRow)
		}
	}
	return out, nil
}

func (q *fakeQueries) HoldsTimer(_ context.Context, arg dbgen.HoldsTimerParams) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.rows {
		if r.ID == arg.ID {
			return r.owner == arg.Owner && r.until.After(arg.Now), q.fail["HoldsTimer"]
		}
	}
	return false, q.fail["HoldsTimer"]
}

func (q *fakeQueries) DeleteFiredTimer(_ context.Context, arg dbgen.DeleteFiredTimerParams) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.fail["DeleteFiredTimer"]; err != nil {
		return err
	}
	q.rows = slices.DeleteFunc(q.rows, func(r *fakeTimer) bool {
		return r.ID == arg.ID && r.owner == arg.Owner && r.Deadline.Equal(arg.Deadline)
	})
	return nil
}

func (q *fakeQueries) NextTimerDeadline(_ context.Context, arg dbgen.NextTimerDeadlineParams) (
	dbgen.NextTimerDeadlineRow, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.fail["NextTimerDeadline"]; err != nil {
		return dbgen.NextTimerDeadlineRow{}, err
	}
	var out dbgen.NextTimerDeadlineRow
	for _, r := range q.rows {
		if !slices.Contains(arg.Kinds, r.Kind) {
			continue
		}
		if r.owner == "" || !r.until.After(arg.Now) {
			if out.FreeDeadline.IsZero() || r.Deadline.Before(out.FreeDeadline) {
				out.FreeDeadline = r.Deadline
			}
		} else if out.LeaseEnd.IsZero() || r.until.Before(out.LeaseEnd) {
			out.LeaseEnd = r.until
		}
	}
	return out, nil
}

// fakeTx is a transaction that records whether it committed.
type fakeTx struct {
	pgx.Tx
	committed bool
}

func (t *fakeTx) Commit(context.Context) error {
	t.committed = true
	return nil
}

func (t *fakeTx) Rollback(context.Context) error { return nil }

type fakeBeginner struct {
	err error
	txs []*fakeTx
}

func (b *fakeBeginner) Begin(context.Context) (pgx.Tx, error) {
	if b.err != nil {
		return nil, b.err
	}
	tx := &fakeTx{}
	b.txs = append(b.txs, tx)
	return tx, nil
}

func (q *fakeQueries) add(id int64, kind string, deadline time.Time) {
	group := id
	q.rows = append(q.rows, &fakeTimer{ClaimDueTimersRow: dbgen.ClaimDueTimersRow{ID: id, Kind: kind,
		Deadline: deadline, AlertGroupID: pgInt8(group)}})
}

// env is a worker of one replica over the fake table, at manual clocks.
type env struct {
	q        *fakeQueries
	begin    *fakeBeginner
	business *clock.Manual
	real     *clock.Manual
	log      *bytes.Buffer
	fired    []int64
	after    []int64
	w        *Worker
}

func newEnv(q *fakeQueries, owner string) *env {
	e := &env{q: q, begin: &fakeBeginner{}, business: clock.NewManual(business0), real: clock.NewManual(real0),
		log: &bytes.Buffer{}}
	e.w = &Worker{
		Store: pgStore{begin: e.begin, queries: func(dbgen.DBTX) queries { return q }},
		Lease: db.Lease{Owner: owner, Duration: Lease, Clocks: clock.Clocks{Business: e.business, Real: e.real}},
		Organizations: func(context.Context) ([]int64, error) {
			return []int64{1}, nil
		},
		Handlers: map[string]Handler{"reopen_window_end": func(_ context.Context, _ dbgen.DBTX, org int64, t Timer) (
			func(context.Context), error) {
			if org != 1 || t.AlertGroupID == nil {
				return nil, errors.New("wrong timer")
			}
			e.fired = append(e.fired, t.ID)
			return func(context.Context) { e.after = append(e.after, t.ID) }, nil
		}},
		Log:   logging.New(e.log, logging.LevelInfo),
		Batch: 2,
	}
	return e
}

// TestRound is C-09.FR-12: a round fires every due timer once, claiming a batch at a time, deletes it, runs what its
// handler returns after the commit, and returns the earliest deadline left; overdue timers after downtime fire once.
func TestRound(t *testing.T) {
	q := &fakeQueries{fail: map[string]error{}}
	for i := range int64(5) {
		q.add(i+1, "reopen_window_end", business0.Add(-time.Duration(i)*time.Hour))
	}
	q.add(9, "reopen_window_end", business0.Add(time.Minute))
	e := newEnv(q, "r1")
	e.w.MaxWait = time.Hour
	next, err := e.w.Round(t.Context())
	if err != nil || next != time.Minute {
		t.Fatalf("round = %v, %v", next, err)
	}
	if len(e.fired) != 5 || !slices.Equal(e.fired, e.after) || len(q.rows) != 1 || len(q.claims) != 3 {
		t.Errorf("fired %v, after %v, left %d, claims %d", e.fired, e.after, len(q.rows), len(q.claims))
	}
	if c := q.claims[0]; c.Owner != "r1" || !c.Due.Equal(business0) || !c.Now.Equal(real0) ||
		!c.LeaseUntil.Equal(real0.Add(Lease)) || c.Lim != 2 || c.OrgID != 1 {
		t.Errorf("claim %+v", c)
	}
	for _, tx := range e.begin.txs {
		if !tx.committed {
			t.Error("a transaction did not commit")
		}
	}
	// The overdue ones fired once: a second round fires nothing.
	e.fired = nil
	if _, err := e.w.Round(t.Context()); err != nil || len(e.fired) != 0 {
		t.Errorf("again: %v %v", e.fired, err)
	}
	// Moving the business clock past the deadline fires it.
	e.business.Advance(time.Minute)
	if next, err := e.w.Round(t.Context()); err != nil || next != time.Hour || !slices.Equal(e.fired, []int64{9}) {
		t.Errorf("after the move: %v %v %v", e.fired, next, err)
	}
}

// TestTwoReplicas: two workers claim each due timer once; a timer whose lease ran out is claimed again by any
// replica, and a lost lease or a rescheduled timer is left alone.
func TestTwoReplicas(t *testing.T) {
	q := &fakeQueries{fail: map[string]error{}}
	for i := range int64(6) {
		q.add(i+1, "reopen_window_end", business0.Add(-time.Minute))
	}
	a, b := newEnv(q, "a"), newEnv(q, "b")
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = a.w.Round(t.Context()) })
	wg.Go(func() { _, _ = b.w.Round(t.Context()) })
	wg.Wait()
	all := slices.Concat(a.fired, b.fired)
	slices.Sort(all)
	if !slices.Equal(all, []int64{1, 2, 3, 4, 5, 6}) {
		t.Errorf("fired %v and %v", a.fired, b.fired)
	}
	// A claimed timer whose lease ran out is claimed by the other replica.
	q.add(7, "reopen_window_end", business0.Add(-time.Minute))
	claimed, err := a.w.Store.Claim(t.Context(), a.w.Lease, 1, []string{"reopen_window_end"}, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	b.real.Advance(Lease)
	if _, err := b.w.Round(t.Context()); err != nil || !slices.Contains(b.fired, 7) {
		t.Errorf("an expired lease: %v", b.fired)
	}
	// a's lease is lost: firing it changes nothing.
	after, err := a.w.Store.Fire(t.Context(), a.w.Lease, 1, claimed[0], a.w.Handlers["reopen_window_end"])
	if err != nil || after != nil || slices.Contains(a.fired, 7) {
		t.Errorf("a lost lease fired: %v, %v", a.fired, err)
	}
	// A timer rescheduled after its claim stays.
	q.add(8, "reopen_window_end", business0.Add(-time.Minute))
	claimed, _ = a.w.Store.Claim(t.Context(), a.w.Lease, 1, []string{"reopen_window_end"}, 10)
	q.rows[len(q.rows)-1].Deadline = business0.Add(time.Hour)
	if _, err := a.w.Store.Fire(t.Context(), a.w.Lease, 1, claimed[0], a.w.Handlers["reopen_window_end"]); err != nil ||
		len(q.rows) != 1 {
		t.Errorf("a rescheduled timer was deleted: %v, %d left", err, len(q.rows))
	}
}

// TestFailures: a failed timer is logged and fires again after its lease; a kind without a handler is logged; a
// failed claim or deadline read fails the round.
func TestFailures(t *testing.T) {
	q := &fakeQueries{fail: map[string]error{}}
	q.add(1, "reopen_window_end", business0.Add(-time.Minute))
	q.add(2, "snooze_end", business0.Add(-time.Minute))
	e := newEnv(q, "r1")
	e.w.Handlers["reopen_window_end"] = func(context.Context, dbgen.DBTX, int64, Timer) (func(context.Context),
		error) {
		return nil, errBoom
	}
	if _, err := e.w.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The failed timer waits for its lease to end, on the real clock, instead of being polled; a kind without a
	// handler is never claimed.
	if !strings.Contains(e.log.String(), `"event":"timer_failed","kind":"reopen_window_end","error":"boom"`) ||
		strings.Contains(e.log.String(), "snooze_end") || len(q.rows) != 2 || q.rows[1].Attempts != 0 {
		t.Errorf("log: %s", e.log.String())
	}
	e.real.Advance(10 * time.Second)
	e.w.MaxWait = time.Hour
	if next, err := e.w.Round(t.Context()); err != nil || next != Lease-10*time.Second {
		t.Errorf("wait for the lease = %v, %v", next, err)
	}
	e.w.fire(t.Context(), 1, Timer{ID: 2, Kind: "snooze_end"})
	if !strings.Contains(e.log.String(), `"event":"timer_failed","kind":"snooze_end","error":"no handler`) {
		t.Errorf("log: %s", e.log.String())
	}
	for _, name := range []string{"HoldsTimer", "DeleteFiredTimer"} {
		q := &fakeQueries{fail: map[string]error{name: errBoom}}
		q.add(1, "reopen_window_end", business0.Add(-time.Minute))
		e := newEnv(q, "r1")
		if _, err := e.w.Round(t.Context()); err != nil || !strings.Contains(e.log.String(), "timer_failed") {
			t.Errorf("%s failing: %v %s", name, err, e.log.String())
		}
	}
	for _, name := range []string{"ClaimDueTimers", "NextTimerDeadline"} {
		q := &fakeQueries{fail: map[string]error{name: errBoom}}
		e := newEnv(q, "r1")
		if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", name, err)
		}
	}
	e = newEnv(&fakeQueries{fail: map[string]error{}}, "r1")
	e.w.Organizations = func(context.Context) ([]int64, error) { return nil, errBoom }
	if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("organizations = %v", err)
	}
	e = newEnv(&fakeQueries{fail: map[string]error{}}, "r1")
	e.begin.err = errBoom
	if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("no transaction = %v", err)
	}
	// A failure while the context ends is not logged.
	q = &fakeQueries{fail: map[string]error{}}
	q.add(1, "reopen_window_end", business0.Add(-time.Minute))
	e = newEnv(q, "r1")
	ctx, cancel := context.WithCancel(t.Context())
	e.w.Handlers["reopen_window_end"] = func(context.Context, dbgen.DBTX, int64, Timer) (func(context.Context),
		error) {
		cancel()
		return nil, errBoom
	}
	_, _ = e.w.Round(ctx)
	if strings.Contains(e.log.String(), "timer_failed") {
		t.Errorf("logged at shutdown: %s", e.log.String())
	}
}

// TestRun: the worker waits for the earliest deadline on the business clock, at most MaxWait, wakes at once when
// woken — a timer was set or the development clock moved — and backs off after a failed round.
func TestRun(t *testing.T) {
	q := &fakeQueries{fail: map[string]error{}}
	q.add(1, "reopen_window_end", business0.Add(10*time.Second))
	e := newEnv(q, "r1")
	e.w.MaxWait = time.Minute
	waits := make(chan time.Duration, 10)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.w.Wait = func(ctx context.Context, d time.Duration, wake <-chan struct{}) {
		waits <- d
		select {
		case <-ctx.Done():
		case <-wake:
		}
	}
	done := make(chan struct{})
	go func() {
		e.w.Run(ctx)
		close(done)
	}()
	if d := <-waits; d != 10*time.Second {
		t.Errorf("first wait %v", d)
	}
	e.business.Advance(time.Minute)
	e.w.Wake()
	if d := <-waits; d != time.Minute {
		t.Errorf("wait without timers %v", d)
	}
	q.mu.Lock()
	q.fail["ClaimDueTimers"] = errBoom
	q.mu.Unlock()
	e.w.Wake()
	if d := <-waits; d < FailureBackoff/2 || d > FailureBackoff {
		t.Errorf("backoff %v", d)
	}
	cancel()
	<-done
	if !slices.Equal(e.fired, []int64{1}) {
		t.Errorf("fired %v", e.fired)
	}
	// Wakes that come while one is pending are merged.
	idle := &Worker{}
	idle.Wake()
	idle.Wake()
	if len(idle.wake) != 1 {
		t.Errorf("%d wakes pending", len(idle.wake))
	}
	// The default wait stops at the context's end; the default batch claims Batch rows.
	e = newEnv(&fakeQueries{fail: map[string]error{}}, "r1")
	e.w.Batch = 0
	ctx, cancel = context.WithCancel(t.Context())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	e.w.Run(ctx)
	if c := e.q.claims; len(c) == 0 || c[0].Lim != Batch {
		t.Errorf("claims %+v", c)
	}
	waitReal(t.Context(), time.Millisecond, nil)
	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	waitReal(t.Context(), time.Hour, wake)
}

func pgInt8(v int64) pgtype.Int8 { return pgtype.Int8{Int64: v, Valid: true} }

// TestNewStore: the Store over the pool runs the generated queries.
func TestNewStore(t *testing.T) {
	s, ok := NewStore(nil).(pgStore)
	if !ok || s.queries(nil) == nil {
		t.Error("NewStore")
	}
}
