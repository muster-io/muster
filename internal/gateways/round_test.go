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
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/gateways/dbgen"
	"github.com/muster-io/muster/internal/groups"
)

// roundGateway is the Notification gateway of the tests of rounds: it answers each contact as answer says, ok when it
// is nil, runs during while a call is made, and keeps the contacts called and the calls made at the moment.
type roundGateway struct {
	mu       sync.Mutex
	sent     []string
	inFlight int
	answer   func(contact string) delivery.Outcome
	during   func(contact string)
}

func (g *roundGateway) Notify(_ context.Context, _ delivery.Call, n delivery.Notification) delivery.Outcome {
	g.mu.Lock()
	g.sent = append(g.sent, n.Contact)
	g.inFlight++
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

// asks is how many tokens were asked for.
func (b *bucket) asks() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
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

// unstarted reports whether c waits for its first attempt as before its preparation: no start, no lease, no attempt.
func unstarted(c fakeCall) bool {
	return c.state == CallWaiting && !c.firstAttempt.Valid && !c.text.Valid && !c.language.Valid &&
		!c.contactID.Valid && !c.actionData.Valid && !c.buttonKeyID.Valid && c.owner == "" && c.attempts == 0 &&
		!c.firstFailed.Valid && !c.errorClass.Valid
}

// TestSenderRoundConcurrency: a round attempts its due calls Concurrency at once, never more: while Concurrency calls
// are being made, the round prepares no other — it takes no token — until one of them ended; it returns once every
// attempt ended. In a synctest bubble, Wait shows the moment every goroutine waits: the calls made for their release,
// the round for a free slot.
func TestSenderRoundConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const calls, concurrency = 8, 3
		e := newSender(t)
		e.s.Concurrency = concurrency
		gw := &roundGateway{}
		lim := newBucket(calls)
		e.s.Gateway, e.s.Limiter = gw, lim
		for id := int64(4); id <= 9; id++ {
			e.person(id)
			e.page(t, id, 10, 1)
		}
		e.page(t, 1, 10, 1)
		e.page(t, 2, 10, 2)
		release := make(chan struct{})
		gw.during = func(string) { <-release }
		done := make(chan error)
		go func() {
			_, err := e.s.Round(t.Context())
			done <- err
		}()
		for released := range calls {
			synctest.Wait()
			gw.mu.Lock()
			inFlight := gw.inFlight
			gw.mu.Unlock()
			if want := min(concurrency, calls-released); inFlight != want || lim.asks() != released+want {
				t.Errorf("%d calls ended: %d made at once and %d prepared, want %d and %d", released, inFlight,
					lim.asks(), want, released+want)
			}
			release <- struct{}{}
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		for i := range calls {
			if c := e.callOf(i); c.state != CallAccepted || c.attempts != 1 || c.owner != "" {
				t.Errorf("the call %d %+v", i, c)
			}
		}
	})
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

// answerA makes the gateway answer the first call to A of gatedRound with out, and every other call ok.
func answerA(out delivery.Outcome) func(string) delivery.Outcome {
	var answered atomic.Bool
	return func(c string) delivery.Outcome {
		if c == "+10000000001" && answered.CompareAndSwap(false, true) {
			return out
		}
		return delivery.Outcome{Kind: delivery.OutcomeOK}
	}
}

// TestSenderRoundRetryAfter is a RetryAfter among calls attempted at once: once the round recorded that the gateway
// asked one call to wait, no call of the round prepared before goes through it. The call that its preparation started
// before the answer was recorded is not made and waits again, unstarted, until the end of the hold plus the token
// margin, as it would have waited for a token; the call prepared after it gets no token; the call through another
// gateway goes.
func TestSenderRoundRetryAfter(t *testing.T) {
	e := newSender(t)
	lim := newBucket(10)
	held := make(chan struct{})
	lim.held = func() { close(held) }
	gw := gatedRound(t, e, lim, func() { <-held })
	gw.answer = answerA(delivery.Outcome{Kind: delivery.OutcomeRetryAfter,
		RetryAfter: 30 * time.Second})
	e.round(t)
	until := c0.Add(3*time.Second + 30*time.Second)
	if got := gw.called(); !slices.Equal(got, []string{"+10000000001", "+10000000003"}) {
		t.Fatalf("called %v", got)
	}
	if a := e.callOf(0); a.attempts != 0 || a.errorClass.String != "retry_after" ||
		!a.nextAttempt.Time.Equal(until.Add(SenderTokenMargin)) || lim.holds[10] != until {
		t.Errorf("the call asked to wait %+v, holds %v", a, lim.holds)
	}
	if b := e.callOf(1); !unstarted(b) || !b.nextAttempt.Time.Equal(until.Add(SenderTokenMargin)) {
		t.Errorf("the call prepared before the answer %+v", b)
	}
	if c := e.callOf(2); c.state != CallAccepted {
		t.Errorf("the call through another gateway %+v", c)
	}
	if d := e.callOf(3); !unstarted(d) || !d.nextAttempt.Time.Equal(until.Add(SenderTokenMargin)) {
		t.Errorf("the call prepared after the answer %+v", d)
	}
}

// TestSenderRoundHeldCall is C-30.FR-4 for a call stopped at the gate by a hold: as it waits unstarted, a Page that
// comes due during the hold joins it, rather than waiting behind it for gateway.combine_window; an Alert Group of it
// that stops during the hold is dropped right before its first attempt; and its first attempt, which its Pages are
// settled with, is the one made after the hold.
func TestSenderRoundHeldCall(t *testing.T) {
	e := newSender(t)
	lim := newBucket(10)
	held := make(chan struct{})
	lim.held = func() { close(held) }
	gw := gatedRound(t, e, lim, func() { <-held })
	gw.answer = answerA(delivery.Outcome{Kind: delivery.OutcomeRetryAfter,
		RetryAfter: 30 * time.Second})
	e.round(t)
	lim.held = nil
	e.f.group(5, "Disk5", "warning")
	e.page(t, 2, 10, 5)
	e.f.setStatus(1, "acknowledged")
	if b := e.callOf(1); len(e.f.calls) != 4 || !unstarted(b) || !slices.Equal(e.f.itemsOf(b.id),
		[]string{"+Disk1", "+Disk5"}) {
		t.Fatalf("the Page during the hold: %d calls, %+v about %v", len(e.f.calls), b, e.f.itemsOf(b.id))
	}
	e.advance(30*time.Second + SenderTokenMargin)
	first := e.business.Now()
	e.round(t)
	b := e.callOf(1)
	if b.state != CallAccepted || !slices.Equal(e.f.itemsOf(b.id), []string{"+Disk5", "-Disk1"}) {
		t.Fatalf("the call after the hold %+v about %v", b, e.f.itemsOf(b.id))
	}
	i := slices.IndexFunc(e.pages.outcomes, func(o groups.CallOutcome) bool { return o.Call == b.id })
	if i < 0 || !e.pages.outcomes[i].FirstAttempt.Equal(first) || !slices.Equal(e.pages.outcomes[i].Groups,
		[]int64{5}) {
		t.Errorf("settled %+v", e.pages.outcomes)
	}
	if got := gw.called(); len(got) != 5 || !slices.Contains(got, "+10000000002") {
		t.Errorf("called %v", got)
	}
}

// TestSenderRoundBroken is a Fatal answer among calls attempted at once: once the round recorded that one call made the
// gateway Broken, no call of the round prepared before goes through it. The call that its preparation started before
// is not made and waits again, unstarted, due at once, and the sender is woken: its next preparation reads the Broken
// state and ends it Not delivered with gateway_broken, unsent, as it ends the call prepared after the Break; the call
// through another gateway goes.
func TestSenderRoundBroken(t *testing.T) {
	e := newBrokenSender(t)
	settling := make(chan struct{})
	var once sync.Once
	e.pages.hook = func() { once.Do(func() { close(settling) }) }
	gw := gatedRound(t, e.senderEnv, newBucket(10), func() { <-settling })
	gw.answer = answerA(delivery.Outcome{Kind: delivery.OutcomeFatal, Status: 401,
		Error: "401 Unauthorized"})
	e.round(t)
	if got := gw.called(); !slices.Equal(got, []string{"+10000000001", "+10000000003"}) {
		t.Fatalf("called %v", got)
	}
	if e.b.state(10) == nil {
		t.Fatal("the gateway is not Broken")
	}
	if b := e.callOf(1); !unstarted(b) || !b.nextAttempt.Time.Equal(c0.Add(3*time.Second)) || len(e.s.wake) != 1 {
		t.Fatalf("the call prepared before the Break %+v, woken %d", b, len(e.s.wake))
	}
	e.round(t)
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
	if got := gw.called(); len(got) != 2 {
		t.Errorf("called %v", got)
	}
}

// TestSenderRoundRetried: a call being retried that the gate stops keeps what its first attempt fixed — after a hold
// it waits until the hold ends plus the token margin, still started, with no attempt counted; after a Broken state it
// ends Not delivered with gateway_broken, unsent.
func TestSenderRoundRetried(t *testing.T) {
	retried := func(e *senderEnv) {
		e.f.mu.Lock()
		defer e.f.mu.Unlock()
		b := &e.f.calls[1]
		b.state, b.attempts = CallSending, 1
		b.firstAttempt = pgtype.Timestamptz{Time: c0, Valid: true}
		b.language = pgtype.Text{String: "en", Valid: true}
		b.text = pgtype.Text{String: "the text", Valid: true}
		b.contactID = pgtype.Int8{Int64: 502, Valid: true}
	}
	e := newSender(t)
	lim := newBucket(10)
	held := make(chan struct{})
	lim.held = func() { close(held) }
	gw := gatedRound(t, e, lim, func() { <-held })
	gw.answer = answerA(delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: 30 * time.Second})
	retried(e)
	e.round(t)
	until := c0.Add(3*time.Second + 30*time.Second)
	if b := e.callOf(1); b.state != CallSending || b.attempts != 1 || b.text.String != "the text" || b.owner != "" ||
		!b.nextAttempt.Time.Equal(until.Add(SenderTokenMargin)) || slices.Contains(gw.called(), "+10000000002") {
		t.Errorf("the call being retried after a hold %+v", b)
	}

	be := newBrokenSender(t)
	settling := make(chan struct{})
	var once sync.Once
	be.pages.hook = func() { once.Do(func() { close(settling) }) }
	gw = gatedRound(t, be.senderEnv, newBucket(10), func() { <-settling })
	gw.answer = answerA(delivery.Outcome{Kind: delivery.OutcomeFatal, Status: 401})
	retried(be.senderEnv)
	be.round(t)
	if b := be.callOf(1); b.state != CallNotDelivered || b.reason.String != groups.PageGatewayBroken ||
		b.attempts != 1 || slices.Contains(gw.called(), "+10000000002") {
		t.Errorf("the call being retried after a Break %+v", b)
	}
}

// TestSenderUnstart: the undo of a start keeps the start when a Page queued a call behind the started call meanwhile —
// the call then waits until the given time, as one whose first attempt was asked to wait — and changes nothing once
// the lease went to another replica; a failing query fails it.
func TestSenderUnstart(t *testing.T) {
	e := newSender(t)
	e.page(t, 1, 10, 1)
	rows, err := e.f.ClaimDueCalls(t.Context(), claimParams(e.s))
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	c, err := e.s.prepare(t.Context(), rows[0])
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	e.page(t, 1, 10, 2)
	at := c0.Add(time.Minute)
	if err := e.s.unstart(t.Context(), c, at); err != nil {
		t.Fatal(err)
	}
	if started, behind := e.callOf(0), e.callOf(1); started.state != CallSending || !started.firstAttempt.Valid ||
		!started.nextAttempt.Time.Equal(at) || started.owner != "" || behind.state != CallWaiting ||
		behind.notBefore.Valid {
		t.Errorf("with a call behind it: %+v, behind %+v", started, behind)
	}
	for _, q := range []string{"LockCallPerson", "UnstartCall", "RescheduleCall"} {
		e.f.fail[q] = errors.New("boom")
		if err := e.s.unstart(t.Context(), c, at); err == nil {
			t.Errorf("%s did not fail", q)
		}
		delete(e.f.fail, q)
	}
}

// TestSenderRoundCancel: when ctx ends while calls are made, the round returns the end once every attempt it started
// has ended; the outcomes of the calls made are recorded, and the call that waited for a slot is left to its lease.
func TestSenderRoundCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newSender(t)
		e.s.Concurrency = 2
		gw := &roundGateway{}
		e.s.Gateway = gw
		e.page(t, 1, 10, 1)
		e.page(t, 2, 10, 2)
		e.page(t, 1, 11, 3)
		ctx, cancel := context.WithCancel(t.Context())
		release := make(chan struct{})
		gw.during = func(string) { <-release }
		done := make(chan error, 1)
		go func() {
			_, err := e.s.Round(ctx)
			done <- err
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
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
	})
}

// TestSenderRoundCancelAtGate: when ctx ends while an answer that held the gateway is recorded, the preparation that
// waits at the gate stops, and the call that waits there to be made stops too, unsent, and waits again, unstarted, due
// at once; the round returns once the answer is recorded.
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
	gw.answer = answerA(delivery.Outcome{Kind: delivery.OutcomeRetryAfter,
		RetryAfter: 30 * time.Second})
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
	if b := e.callOf(1); !unstarted(b) || !b.nextAttempt.Time.Equal(c0.Add(3*time.Second)) {
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
