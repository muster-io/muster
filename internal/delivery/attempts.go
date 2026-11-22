// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// flight is what the attempts of one reconcile share (Round): a slot for each claim and attempt made at once, the
// attempts in flight by what they keep in order (attemptKey), and the gate of the limiters (gate). The round's own
// goroutine claims and prepares; the rest of each prepared attempt — its call, the record of its outcome and what runs
// once that committed — goes on in a goroutine of its own, which holds the slot its preparation took until it ends. A
// narrow flight keeps all its slots but one taken (held) until it widens, at its backlogClaims-th full claim (full
// counts them).
type flight struct {
	slots    chan struct{}
	held     int
	full     int
	attempts sync.WaitGroup
	mu       sync.Mutex
	running  map[attemptKey]chan struct{}
	gate     *gate
}

// attemptKey is what an attempt keeps in order: a Root message (id, its delivery), whose own attempts and those of its
// Thread replies go one at a time, so that a reply sends the `thread_ref` the reply before it stored; or the events of
// an Alert Group (group) in an outgoing webhook (id, the Destination), whose claim takes the head event only. It also
// keeps a row whose lease ran out during a slow call from being attempted twice at once by one replica.
type attemptKey struct {
	event bool
	id    int64
	group int64
}

// claimedRow is a row that a claim leased, with the key of its attempt.
type claimedRow struct {
	id  int64
	key attemptKey
}

// claimFunc claims one batch of an Organization's due rows of one rank, in the order they are attempted.
type claimFunc func(ctx context.Context, org int64) ([]claimedRow, error)

// workFunc prepares one claimed row and returns the rest of its attempt, nil when there is nothing to call; it logs its
// failures itself.
type workFunc func(ctx context.Context, org, id int64) func(ctx context.Context)

// newFlight is the flight of a reconcile that makes up to concurrency attempts at once, at least one.
func newFlight(concurrency int) *flight {
	return &flight{slots: make(chan struct{}, max(concurrency, 1)), running: map[attemptKey]chan struct{}{},
		gate: newGate()}
}

// take waits until a slot is free and takes it; false when ctx ended first.
func (f *flight) take(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case f.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// free gives a slot back.
func (f *flight) free() { <-f.slots }

// narrow makes a new flight f claim and attempt one row at a time, its other slots held, until it widens; it returns
// f. Only the round's goroutine narrows and widens a flight.
func (f *flight) narrow() *flight {
	for ; f.held < cap(f.slots)-1; f.held++ {
		f.slots <- struct{}{}
	}
	return f
}

// widen gives back the slots that narrow held, so that f makes up to its concurrency attempts at once from now on.
func (f *flight) widen() {
	for ; f.held > 0; f.held-- {
		<-f.slots
	}
}

// backlogClaims is the full claims of a round that make a narrow flight widen: more is due than that many batches, a
// backlog that attempts made one at a time would clear only slowly. One full claim is no backlog yet: a round of one
// attempt at a time often finds a batch due while the worker keeps up — on the L1 load test, at about three quarters
// of its pace, widening at the first full claim still ran 16 and 32 % more transactions than one attempt at a time in
// two runs, and the processing of a Snapshot was 5 and 7 % slower. Widening at the second, six runs against six of one
// at a time processed a Snapshot as fast (−0.5 %, 95 % CI −2.5 to +1.5 %), and on the L2 load scenario two runs made
// 91 and 92 attempts a second and delivered the Root messages within 12.6 and 12.2 s at the 95th percentile, as four at
// once from the start did (92, 11.9 s), against 73 and 38.2 s with one at a time.
const backlogClaims = 2

// fullClaim counts a full claim of f, and widens a narrow f at the backlogClaims-th.
func (f *flight) fullClaim() {
	if f.full++; f.full >= backlogClaims {
		f.widen()
	}
}

// await waits until the attempt of key in flight, if any, ended, and runs waiting first when it has to wait; false when
// ctx ended first. Only the round's goroutine starts attempts, so none of key starts meanwhile.
func (f *flight) await(ctx context.Context, key attemptKey, waiting func()) bool {
	f.mu.Lock()
	done := f.running[key]
	f.mu.Unlock()
	if done == nil {
		return true
	}
	waiting()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// start runs finish, the rest of the attempt of key, whose preparation holds a slot, in a goroutine of its own, which
// marks the attempt ended and gives the slot back once finish returned.
func (f *flight) start(ctx context.Context, key attemptKey, finish func(ctx context.Context)) {
	done := make(chan struct{})
	f.mu.Lock()
	f.running[key] = done
	f.mu.Unlock()
	f.attempts.Go(func() {
		defer func() {
			f.mu.Lock()
			delete(f.running, key)
			f.mu.Unlock()
			close(done)
			f.free()
		}()
		finish(ctx)
	})
}

// wait waits until every attempt started has ended.
func (f *flight) wait() { f.attempts.Wait() }

// batchOnce claims one batch with claim, holding a slot of f, and works its rows with work in the order of the claim:
// each waits until no attempt of its key is in flight and a slot is free, is prepared on this goroutine — the tokens it
// takes and the refusals it notes in the memo follow that order — and the rest of its attempt goes on in a goroutine
// of f. A full claim counts towards the backlog that widens a narrow f (fullClaim). It returns how many rows the claim
// took, once each was prepared, without waiting for their calls; when ctx ends first, it stops with its error, and a
// row it did not prepare is claimed again once its lease runs out.
func (w *Worker) batchOnce(ctx context.Context, f *flight, org int64, claim claimFunc, work workFunc) (int, error) {
	if !f.take(ctx) {
		return 0, fmt.Errorf("wait for a claim: %w", ctx.Err())
	}
	rows, err := claim(ctx, org)
	f.free()
	if err != nil {
		return 0, err
	}
	if len(rows) >= int(w.batch()) {
		f.fullClaim()
	}
	hook := hookOf(ctx)
	for _, r := range rows {
		if !f.await(ctx, r.key, func() { hook(roundWaits, r.id) }) || !f.take(ctx) {
			return len(rows), fmt.Errorf("wait to prepare a claimed row: %w", ctx.Err())
		}
		finish := work(ctx, org, r.id)
		if finish == nil {
			f.free()
			continue
		}
		f.start(ctx, r.key, func(ctx context.Context) {
			hook(roundRuns, r.id)
			finish(ctx)
		})
		hook(roundPrepared, r.id)
	}
	return len(rows), nil
}

// The events of a round that a hook in its context sees (withHook): on the round's goroutine, a claimed row waits for
// the attempt of its key in flight before its preparation, or was prepared and its attempt started; on the goroutine
// of that attempt, the rest of it runs, before its call waits for its turn.
const (
	roundWaits    = "waits"
	roundPrepared = "prepared"
	roundRuns     = "runs"
)

type hookKey struct{}

// withHook is ctx with the hook that sees the events of its rounds, for the tests that need to know when a row was
// prepared or waits.
func withHook(ctx context.Context, hook func(event string, id int64)) context.Context {
	return context.WithValue(ctx, hookKey{}, hook)
}

// hookOf is the hook of ctx, one that does nothing without one.
func hookOf(ctx context.Context) func(event string, id int64) {
	if h, ok := ctx.Value(hookKey{}).(func(event string, id int64)); ok {
		return h
	}
	return func(string, int64) {}
}

// drain claims Batch rows at a time with claim and works each with work until a claim comes back short.
func (w *Worker) drain(ctx context.Context, f *flight, org int64, claim claimFunc, work workFunc) error {
	for {
		n, err := w.batchOnce(ctx, f, org, claim, work)
		if err != nil || n < int(w.batch()) {
			return err
		}
	}
}

// gate keeps the calls of a round that makes several attempts at once to their limiters as attempts made one at a
// time keep them (C-11.FR-8): a RetryAfter answer holds the limiter it names — its Destination's, or its Connection's —
// from the moment it arrives, before its record holds the bucket, until the end of the wait it asked for on the
// business clock; and the calls through one Connection, or to one Destination without one, take turns on its line, in
// the order the round prepared them (reserve), so that a call is made only once the answer before it on the line is
// known. A call whose limiter is held when its turn comes is not made: the RetryAfter the hold stands for is its outcome
// (held), recorded by the rules of outcomes.go as that answer, so that the row waits until the same end, counts no
// attempt, and keeps the start of its Publication only when it was started before this preparation. A nil gate,
// outside a round, holds nothing and lets every call through at once.
type gate struct {
	mu    sync.Mutex
	holds map[limiterKey]heldLimiter
	lines map[limiterKey]*line
}

// heldLimiter is a limiter that a RetryAfter answer, out, holds until until on the business clock.
type heldLimiter struct {
	until time.Time
	out   Outcome
}

// line is the turns of the calls through one Connection, or to one Destination without one: the last ticket issued,
// the ticket whose turn it is, the tickets given up before their turn, which it skips, and a channel closed when the
// turn moves on. Its fields are guarded by the gate's mutex.
type line struct {
	issued, next uint64
	gone         map[uint64]bool
	moved        chan struct{}
}

// ticket is a place on a line; the zero ticket has none.
type ticket struct {
	line *line
	n    uint64
}

func newGate() *gate {
	return &gate{holds: map[limiterKey]heldLimiter{}, lines: map[limiterKey]*line{}}
}

// reserve takes the next place on the line of the calls to d. The round's goroutine reserves one for each attempt it
// prepared, right after the preparation, so that the turns of a line follow the order of the preparations. A nil gate
// gives none.
func (g *gate) reserve(d Destination) ticket {
	if g == nil {
		return ticket{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	l := g.lines[lineOf(d)]
	if l == nil {
		l = &line{next: 1, gone: map[uint64]bool{}, moved: make(chan struct{})}
		g.lines[lineOf(d)] = l
	}
	l.issued++
	return ticket{line: l, n: l.issued}
}

// pass ends the turn of the ticket t, or gives up its place before its turn: the line moves on to the next ticket that
// was not given up.
func (g *gate) pass(t ticket) {
	g.mu.Lock()
	defer g.mu.Unlock()
	l := t.line
	if l.next != t.n {
		l.gone[t.n] = true
		return
	}
	for l.next++; l.gone[l.next]; l.next++ {
		delete(l.gone, l.next)
	}
	close(l.moved)
	l.moved = make(chan struct{})
}

type gateKey struct{}

// withGate is ctx with the gate g of its round.
func withGate(ctx context.Context, g *gate) context.Context {
	return context.WithValue(ctx, gateKey{}, g)
}

// gateOf is the gate of the round of ctx, nil outside one.
func gateOf(ctx context.Context) *gate {
	g, _ := ctx.Value(gateKey{}).(*gate)
	return g
}

// lineOf is the line of the calls to d: its Connection's, or its own without one.
func lineOf(d Destination) limiterKey {
	if d.Connection != nil {
		return limiterKey{connection: true, id: *d.Connection}
	}
	return limiterKey{id: d.ID}
}

// enter waits for the turn of the ticket t of a call to d, taking a place at the end of the line when t has none, and
// returns the outcome that stands for the call when a limiter of d is held at the time now gives once the turn came,
// nil when the call is to be made, and the function that ends the turn, once the answer was handled (answered). When
// ctx ends before the turn came, the place is given up and its error returned.
func (g *gate) enter(ctx context.Context, t ticket, d Destination, now func() time.Time) (*Outcome, func(), error) {
	if g == nil {
		return nil, func() {}, nil
	}
	if t.line == nil {
		t = g.reserve(d)
	}
	for {
		g.mu.Lock()
		turn, moved := t.line.next == t.n, t.line.moved
		g.mu.Unlock()
		if turn {
			break
		}
		select {
		case <-moved:
		case <-ctx.Done():
			g.pass(t)
			return nil, nil, fmt.Errorf("wait for the turn of the call: %w", ctx.Err())
		}
	}
	return g.held(d, now()), sync.OnceFunc(func() { g.pass(t) }), nil
}

// held is the outcome that stands for a call to d at now while its limiter or its Connection's is held: the RetryAfter
// of the hold that lasts longest, for what is left of it, with the scope of its limiter and the error of its answer;
// nil when neither is held.
func (g *gate) held(d Destination, now time.Time) *Outcome {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	keys := []limiterKey{{id: d.ID}}
	if d.Connection != nil {
		keys = append(keys, limiterKey{connection: true, id: *d.Connection})
	}
	var out *Outcome
	var until time.Time
	for _, k := range keys {
		h, ok := g.holds[k]
		if !ok || !h.until.After(now) || !h.until.After(until) {
			continue
		}
		scope := ScopeDestination
		if k.connection {
			scope = ScopeConnection
		}
		until = h.until
		out = &Outcome{Kind: OutcomeRetryAfter, RetryAfter: h.until.Sub(now), Scope: scope, Error: h.out.Error,
			heldUntil: h.until}
	}
	return out
}

// answered holds, from now, the limiter that the RetryAfter answer out of a call to d names, as holdBucket does, until
// the end of the wait it asks for; a longer hold already in place stays, and any other answer holds nothing. It
// returns the end the limiter is held until, which the answer's record takes as is (heldUntil), so that the row that
// got the answer and the rows the gate holds become due together; the zero time when it holds nothing.
func (g *gate) answered(d Destination, out Outcome, now time.Time) time.Time {
	if g == nil || out.Kind != OutcomeRetryAfter {
		return time.Time{}
	}
	k := limiterKey{id: d.ID}
	if out.Scope == ScopeConnection && d.Connection != nil {
		k = limiterKey{connection: true, id: *d.Connection}
	}
	until := now.Add(out.RetryAfter)
	g.mu.Lock()
	defer g.mu.Unlock()
	if h, ok := g.holds[k]; !ok || until.After(h.until) {
		g.holds[k] = heldLimiter{until: until, out: out}
	}
	return g.holds[k].until
}
