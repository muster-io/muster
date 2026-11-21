// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package gateways

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/groups"
)

// roundGateway is the Notification gateway of the tests of rounds: it answers each contact as answer says, ok when it
// is nil, runs during while a call is made, and keeps the contacts called and the most calls made at once.
type roundGateway struct {
	mu       sync.Mutex
	sent     []string
	inFlight int
	most     int
	answer   func(contact string) delivery.Outcome
	during   func(contact string)
}

func (g *roundGateway) Notify(_ context.Context, _ delivery.Call, n delivery.Notification) delivery.Outcome {
	g.mu.Lock()
	g.sent = append(g.sent, n.Contact)
	g.inFlight++
	g.most = max(g.most, g.inFlight)
	g.mu.Unlock()
	if g.during != nil {
		g.during(n.Contact)
	}
	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
	if g.answer != nil {
		return g.answer(n.Contact)
	}
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n-" + n.Contact}
}

// called is the contacts called, sorted.
func (g *roundGateway) called() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Sorted(slices.Values(g.sent))
}

// bucket is the limiter of the tests of rounds: free tokens for each Connection, after which a token is due at due,
// and none while a hold lasts. asked runs for the n-th token asked for, within the preparation's transaction, once
// the token was taken or refused, and held after each hold.
type bucket struct {
	mu    sync.Mutex
	free  map[int64]int
	due   time.Time
	holds map[int64]time.Time
	n     int
	asked func(n int)
	held  func()
}

func newBucket(free int) *bucket {
	return &bucket{free: map[int64]int{10: free, 11: free}, holds: map[int64]time.Time{}}
}

func (b *bucket) ConnectionToken(_ context.Context, _ DBTX, _, connection int64, now time.Time) (bool, time.Time,
	error) {
	b.mu.Lock()
	b.n++
	n, asked := b.n, b.asked
	ok, due := true, time.Time{}
	switch until, held := b.holds[connection]; {
	case held && now.Before(until):
		ok, due = false, until
	case b.free[connection] <= 0:
		ok, due = false, b.due
	default:
		b.free[connection]--
	}
	b.mu.Unlock()
	if asked != nil {
		asked(n)
	}
	return ok, due, nil
}

func (b *bucket) HoldConnection(_ context.Context, _ DBTX, _, connection int64, until time.Time) error {
	b.mu.Lock()
	b.holds[connection] = until
	held := b.held
	b.mu.Unlock()
	if held != nil {
		held()
	}
	return nil
}

// person adds the User id, in English, with a confirmed contact at the gateway 10.
func (e *senderEnv) person(id int64) {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	e.f.users[id] = fakeCallUser{publicID: fmt.Sprintf("SR%012d", id), name: "P", login: fmt.Sprintf("p%d", id),
		status: "active"}
	e.f.contacts[[2]int64{id, 10}] = dbgen.CallContactRow{ID: 600 + id, Contact: personContact(id)}
}

// personContact is the contact of the User id that person added.
func personContact(id int64) string { return fmt.Sprintf("+2%010d", id) }

// callOf is the call of the fake with the index i, read under its lock.
func (e *senderEnv) callOf(i int) fakeCall {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	return e.f.calls[i]
}

// TestSenderRoundConcurrency: a round attempts its due calls Concurrency at once, never more, each holding its slot
// until its outcome is recorded, and returns once every attempt ended.
func TestSenderRoundConcurrency(t *testing.T) {
	e := newSender(t)
	e.s.Concurrency = 3
	gw := &roundGateway{}
	e.s.Gateway = gw
	for id := int64(4); id <= 9; id++ {
		e.person(id)
		e.page(t, id, 10, 1)
	}
	e.page(t, 1, 10, 1)
	e.page(t, 2, 10, 2)
	full := make(chan struct{})
	var once sync.Once
	gw.during = func(string) {
		gw.mu.Lock()
		n := gw.inFlight
		gw.mu.Unlock()
		if n == 3 {
			once.Do(func() { close(full) })
		}
		select {
		case <-full:
		case <-time.After(10 * time.Second): // a guard against a sender that never makes three calls at once
		}
	}
	e.round(t)
	if gw.most != 3 || len(gw.called()) != 8 {
		t.Fatalf("%d calls, at most %d at once", len(gw.called()), gw.most)
	}
	for i := range 8 {
		if c := e.callOf(i); c.state != CallAccepted || c.attempts != 1 || c.owner != "" {
			t.Errorf("the call %d %+v", i, c)
		}
	}
}

// TestSenderRoundTokenOrder is C-30.FR-4 with calls attempted at once: the preparations take the tokens of the
// gateway's limiter one after another in the order of the claim, so the call about a critical Alert Group, due last,
// gets the first token, the call due first the second, and every later call waits for a token, no attempt made.
func TestSenderRoundTokenOrder(t *testing.T) {
	e := newSender(t)
	e.s.Concurrency = 4
	gw := &roundGateway{}
	e.s.Gateway = gw
	lim := newBucket(2)
	lim.due = c0.Add(time.Minute)
	e.s.Limiter = lim
	for id := int64(4); id <= 7; id++ {
		e.person(id)
		e.page(t, id, 10, 1)
		e.advance(time.Second)
	}
	e.page(t, 1, 10, 4)
	e.advance(time.Second)
	e.page(t, 2, 10, 2)
	e.round(t)
	if got, want := gw.called(), []string{"+10000000001", personContact(4)}; !slices.Equal(got, want) {
		t.Fatalf("called %v, want %v", got, want)
	}
	// The calls in the order of their Pages: Users 4 to 7, then 1 (critical) and 2.
	for _, i := range []int{1, 2, 3, 5} {
		if c := e.callOf(i); c.state != CallWaiting || c.attempts != 0 ||
			!c.nextAttempt.Time.Equal(lim.due.Add(SenderTokenMargin)) {
			t.Errorf("the call %d without a token %+v", i, c)
		}
	}
	if first, critical := e.callOf(0), e.callOf(4); first.state != CallAccepted || critical.state != CallAccepted {
		t.Errorf("the call due first %+v, the critical call %+v", first, critical)
	}
}

// gatedRound pages A (User 1, critical) and B (User 2) through the gateway 10, C (User 1) through the gateway 11 and
// D (User 4) through the gateway 10, a second apart, and attempts them four at once with the limiter lim: A's call
// waits until the token of B's is taken, and B's preparation waits after its token until wait returns, so that B is
// prepared before A's answer is recorded.
func gatedRound(t *testing.T, e *senderEnv, lim *bucket, wait func()) *roundGateway {
	t.Helper()
	e.s.Concurrency = 4
	gw := &roundGateway{}
	e.s.Gateway, e.s.Limiter = gw, lim
	e.person(4)
	e.page(t, 1, 10, 4)
	e.advance(time.Second)
	e.page(t, 2, 10, 1)
	e.advance(time.Second)
	e.page(t, 1, 11, 2)
	e.advance(time.Second)
	e.page(t, 4, 10, 3)
	asked := make(chan struct{})
	lim.asked = func(n int) {
		if n == 2 {
			close(asked)
			wait()
		}
	}
	gw.during = func(contact string) {
		if contact == "+10000000001" {
			<-asked
		}
	}
	return gw
}

// TestSenderRoundRetryAfter is a RetryAfter among calls attempted at once: once the gateway asked one call to wait,
// no other call of the round goes through it. The call prepared before the answer was recorded is not made and waits
// for the end of the hold plus the token margin, as it would have waited for a token, with no attempt and no budget
// started; the call prepared after it gets no token; the call through another gateway goes.
func TestSenderRoundRetryAfter(t *testing.T) {
	e := newSender(t)
	lim := newBucket(10)
	held := make(chan struct{})
	lim.held = func() { close(held) }
	gw := gatedRound(t, e, lim, func() { <-held })
	gw.answer = func(contact string) delivery.Outcome {
		if contact == "+10000000001" {
			return delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: 30 * time.Second}
		}
		return delivery.Outcome{Kind: delivery.OutcomeOK}
	}
	e.round(t)
	until := c0.Add(3*time.Second + 30*time.Second)
	if got := gw.called(); !slices.Equal(got, []string{"+10000000001", "+10000000003"}) {
		t.Fatalf("called %v", got)
	}
	if a := e.callOf(0); a.attempts != 0 || a.errorClass.String != "retry_after" ||
		!a.nextAttempt.Time.Equal(until.Add(SenderTokenMargin)) || lim.holds[10] != until {
		t.Errorf("the call asked to wait %+v, holds %v", a, lim.holds)
	}
	if b := e.callOf(1); b.state != CallSending || b.attempts != 0 || b.errorClass.Valid || b.firstFailed.Valid ||
		b.owner != "" || !b.nextAttempt.Time.Equal(until.Add(SenderTokenMargin)) {
		t.Errorf("the call prepared before the answer %+v", b)
	}
	if c := e.callOf(2); c.state != CallAccepted {
		t.Errorf("the call through another gateway %+v", c)
	}
	if d := e.callOf(3); d.state != CallWaiting || d.attempts != 0 || !d.nextAttempt.Time.Equal(until.Add(SenderTokenMargin)) {
		t.Errorf("the call prepared after the answer %+v", d)
	}
}

// TestSenderRoundBroken is a Fatal answer among calls attempted at once: once one call made the gateway Broken, no
// other call of the round goes through it. The call prepared before the Broken state was recorded ends Not delivered
// with gateway_broken, unsent, as does the call prepared after it; the call through another gateway goes.
func TestSenderRoundBroken(t *testing.T) {
	e := newBrokenSender(t)
	settling := make(chan struct{})
	var once sync.Once
	e.pages.hook = func() { once.Do(func() { close(settling) }) }
	gw := gatedRound(t, e.senderEnv, newBucket(10), func() { <-settling })
	gw.answer = func(contact string) delivery.Outcome {
		if contact == "+10000000001" {
			return delivery.Outcome{Kind: delivery.OutcomeFatal, Status: 401, Error: "401 Unauthorized"}
		}
		return delivery.Outcome{Kind: delivery.OutcomeOK}
	}
	e.round(t)
	if got := gw.called(); !slices.Equal(got, []string{"+10000000001", "+10000000003"}) {
		t.Fatalf("called %v", got)
	}
	if e.b.state(10) == nil {
		t.Fatal("the gateway is not Broken")
	}
	want := []struct {
		state, reason string
		attempts      int64
	}{{CallNotDelivered, groups.PageCallRefused, 1}, {CallNotDelivered, groups.PageGatewayBroken, 0},
		{CallAccepted, "", 1}, {CallNotDelivered, groups.PageGatewayBroken, 0}}
	for i, w := range want {
		if c := e.callOf(i); c.state != w.state || c.reason.String != w.reason || c.attempts != w.attempts {
			t.Errorf("the call %d %+v, want %+v", i, c, w)
		}
	}
}

// TestSenderRoundReclaimed is NFR-4 within one replica: a call whose lease ran out during a slow call, claimed again
// by the same round, is left to the attempt that runs, which records its outcome under the renewed lease; the gateway
// is called once. One call at a time is claimed: the critical call A, then B, whose lease from a claim made before
// A's ran out has run out too by its preparation, then A again, then B again, which goes.
func TestSenderRoundReclaimed(t *testing.T) {
	e := newSender(t)
	e.s.Concurrency, e.s.Batch = 2, 1
	gw := &roundGateway{}
	e.s.Gateway = gw
	e.page(t, 1, 10, 4)
	e.page(t, 2, 10, 1)
	inCall, release := make(chan struct{}), make(chan struct{})
	gw.during = func(contact string) {
		if contact == "+10000000001" {
			close(inCall)
			<-release
		}
	}
	claims := 0
	e.f.claiming = func() {
		claims++
		switch claims {
		case 2:
			// A is being called; its lease runs out after this claim, which read the real clock before.
			<-inCall
			e.real.Advance(SenderLease)
		case 4:
			close(release)
		}
	}
	e.round(t)
	if got := gw.called(); !slices.Equal(got, []string{"+10000000001", "+10000000002"}) || claims != 5 {
		t.Fatalf("called %v, %d claims", got, claims)
	}
	if a, b := e.callOf(0), e.callOf(1); a.state != CallAccepted || a.attempts != 1 || b.state != CallAccepted {
		t.Errorf("the calls %+v %+v", a, b)
	}
}

// TestSenderRoundCancel: when ctx ends while calls are made, the round returns the end once every attempt it started
// has ended; the outcomes of the calls made are recorded, and the call that waited for a slot is left to its lease.
func TestSenderRoundCancel(t *testing.T) {
	e := newSender(t)
	e.s.Concurrency = 2
	gw := &roundGateway{}
	e.s.Gateway = gw
	e.page(t, 1, 10, 1)
	e.page(t, 2, 10, 2)
	e.page(t, 1, 11, 3)
	ctx, cancel := context.WithCancel(t.Context())
	both, release := make(chan struct{}), make(chan struct{})
	var n atomic.Int32
	gw.during = func(string) {
		if n.Add(1) == 2 {
			close(both)
		}
		<-release
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.s.Round(ctx)
		done <- err
	}()
	<-both
	cancel()
	select {
	case err := <-done:
		t.Fatalf("the round returned while its calls were made: %v", err)
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("the round ended with %v", err)
	}
	if a, b, c := e.callOf(0), e.callOf(1), e.callOf(2); a.state != CallAccepted || b.state != CallAccepted ||
		c.state != CallWaiting || len(gw.called()) != 2 {
		t.Errorf("after the end: %+v %+v %+v", a, b, c)
	}
}

// TestSenderRoundCancelAtGate: when ctx ends while an answer that held the gateway is recorded, the preparation that
// waits at the gate and the call that waits there to be made stop, unsent, and the round returns once the answer is
// recorded.
func TestSenderRoundCancelAtGate(t *testing.T) {
	e := newSender(t)
	ctx, cancel := context.WithCancel(t.Context())
	lim := newBucket(10)
	recording, release := make(chan struct{}), make(chan struct{})
	lim.held = func() {
		close(recording)
		<-release
	}
	gw := gatedRound(t, e, lim, func() { <-recording })
	gw.answer = func(contact string) delivery.Outcome {
		if contact == "+10000000001" {
			return delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: 30 * time.Second}
		}
		return delivery.Outcome{Kind: delivery.OutcomeOK}
	}
	e.s.Concurrency = 3
	done := make(chan error, 1)
	go func() {
		_, err := e.s.Round(ctx)
		done <- err
	}()
	<-recording
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("the round ended with %v", err)
	}
	if got := gw.called(); !slices.Contains(got, "+10000000001") || slices.Contains(got, "+10000000002") ||
		slices.Contains(got, personContact(4)) {
		t.Errorf("called %v", got)
	}
	if b := e.callOf(1); b.attempts != 0 || b.state != CallSending {
		t.Errorf("the call that waited at the gate %+v", b)
	}
}

// TestGate: a gate is down until every answer that raised it was recorded, each lowered once; waiting ends with ctx;
// a nil gate, outside a round, lets every call through and records nothing.
func TestGate(t *testing.T) {
	g := &gate{}
	first, second := g.raise(), g.raise()
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := g.wait(ended); !errors.Is(err, context.Canceled) {
		t.Errorf("a raised gate let a call through: %v", err)
	}
	first()
	first()
	if _, _, err := g.wait(ended); err == nil {
		t.Error("a gate lowered by one of two answers let a call through")
	}
	g.hold(c0.Add(time.Minute))
	g.hold(c0)
	g.broke()
	second()
	if breaks, until, err := g.wait(t.Context()); err != nil || breaks != 1 || !until.Equal(c0.Add(time.Minute)) {
		t.Errorf("the lowered gate: %d, %v, %v", breaks, until, err)
	}
	var none *gate
	none.raise()()
	none.hold(c0)
	none.broke()
	if breaks, until, err := none.wait(ended); err != nil || breaks != 0 || !until.IsZero() {
		t.Errorf("no gate: %d, %v, %v", breaks, until, err)
	}
	if mayStop(delivery.Outcome{Kind: delivery.OutcomeFatal, NotSent: true}) ||
		mayStop(delivery.Outcome{Kind: delivery.OutcomeOK}) || !mayStop(delivery.Outcome{Kind: delivery.OutcomeFatal}) {
		t.Error("mayStop")
	}
}
