// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package gateways

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
)

// raceGateway is a Notification gateway that answers the contact first, once ready is closed, with answer, and every
// other contact ok; it keeps the contacts called, each call's contact and text, and every key called while a call with
// the same key was in flight.
type raceGateway struct {
	mu     sync.Mutex
	sent   []string
	texts  []string
	flying map[string]bool
	twice  []string
	first  string
	ready  chan struct{}
	answer delivery.Outcome
}

func (g *raceGateway) Notify(_ context.Context, c delivery.Call, n delivery.Notification) delivery.Outcome {
	g.mu.Lock()
	if g.flying == nil {
		g.flying = map[string]bool{}
	}
	if g.flying[c.Key] {
		g.twice = append(g.twice, c.Key)
	}
	g.flying[c.Key] = true
	g.sent = append(g.sent, n.Contact)
	g.texts = append(g.texts, n.Contact+" "+n.Text)
	first := g.first
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.flying, c.Key)
		g.mu.Unlock()
	}()
	if first != "" && n.Contact == first {
		<-g.ready
		return g.answer
	}
	runtime.Gosched()
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n-" + c.Key}
}

// called is the contacts called, sorted.
func (g *raceGateway) called() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Sorted(slices.Values(g.sent))
}

// answerAll makes the gateway answer every contact ok from now on.
func (g *raceGateway) answerAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.first = ""
}

// madeTo is the contact and the text of each call made to contact.
func (g *raceGateway) madeTo(contact string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, t := range g.texts {
		if strings.HasPrefix(t, contact+" ") {
			out = append(out, t)
		}
	}
	return out
}

// racingLimiter is the limiter of the database that, once it took the second token, in the preparation's transaction,
// lets the gateway answer the first call (ready) and waits until settled returns, so that the second call is prepared
// before the answer to the first one is recorded.
type racingLimiter struct {
	ConnectionLimiter
	n       atomic.Int32
	ready   chan struct{}
	settled func()
}

func (l *racingLimiter) ConnectionToken(ctx context.Context, d DBTX, orgID, connection int64, now time.Time) (bool,
	time.Time, error) {
	ok, due, err := l.ConnectionLimiter.ConnectionToken(ctx, d, orgID, connection, now)
	if l.n.Add(1) == 2 {
		close(l.ready)
		l.settled()
	}
	return ok, due, err
}

// waitFor polls cond until it holds, failing the test when it never does. It polls because what it waits for — a
// backend waiting for a lock, a commit of another session — sends no event a test could wait on, as
// TestNotDeliveredAgainstPageLockOrder does; the deadline only bounds a failing test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Errorf("%s never happened", what)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestIntegrationRoundLimiter is C-30.FR-4 and C-11.FR-3 on PostgreSQL with calls attempted at once: through a gateway
// whose limiter allows 3 calls a minute, a round with four attempts at once makes exactly 3 calls — the one about a
// critical Alert Group, due last, and the two due first, as the preparations take the tokens in the order of the claim
// — and the 5 others wait for the next token plus the token margin, no attempt made. A minute later, two replicas that
// attempt four calls at once each make 3 calls between them, never more, and then the last 2; each key once.
func TestIntegrationRoundLimiter(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		const people = 8
		e := setupCalls(t, srv, people)
		e.exec(t, `UPDATE connections SET limiter_limit = 3, limiter_per_seconds = 60 WHERE id = $1`, e.gw)
		for _, u := range e.users[1:] {
			e.page(t, u, e.group(t, "Disk", "warning"))
			e.business.Advance(time.Second)
		}
		e.page(t, e.users[0], e.group(t, "Down", "critical"))
		gw := &raceGateway{}
		s := e.sender("r1", gw)
		s.Concurrency = 4
		e.round(t, s)
		if got, want := gw.called(), []string{"+10000000001", "+10000000002", "+10000000003"}; !slices.Equal(got,
			want) {
			t.Fatalf("called %v, want %v", got, want)
		}
		next := e.business.Now().Add(20*time.Second + SenderTokenMargin)
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'waiting' AND attempts = 0
			AND first_attempt_at IS NULL AND lease_owner IS NULL AND next_attempt_at = $1`, next); n != people-3 {
			t.Errorf("%d calls wait for the next token", n)
		}

		r2 := e.sender("r2", gw)
		r2.Concurrency = 4
		for _, want := range []int{6, people} {
			e.business.Advance(time.Minute)
			var wg sync.WaitGroup
			for _, x := range []*Sender{s, r2} {
				wg.Go(func() {
					if _, err := x.Round(t.Context()); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			if got := len(gw.called()); got != want {
				t.Errorf("%d calls after the next minute, want %d", got, want)
			}
		}
		if got := gw.called(); len(slices.Compact(got)) != people || len(gw.twice) != 0 {
			t.Errorf("called %v, at once twice %v", got, gw.twice)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'accepted' AND attempts = 1`); n != people {
			t.Errorf("%d accepted", n)
		}
	})
}

// TestIntegrationRoundRetryAfter is a RetryAfter among calls attempted at once on PostgreSQL: B's preparation takes
// its token while the gateway asks A's call to wait, and B's call is made only after that answer was recorded, which
// waits for the bucket B's preparation holds. B is not made: it waits again, unstarted, until the hold ends plus the
// token margin, with no attempt and no budget started; the bucket is held until the end of the wait A was asked for.
// A Page that comes due during the hold joins B, an Alert Group of B acknowledged during the hold is dropped right
// before B's first attempt, which is made after the hold.
func TestIntegrationRoundRetryAfter(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		e := setupCalls(t, srv, 2)
		gb := e.group(t, "Disk", "warning")
		e.page(t, e.users[1], gb)
		e.page(t, e.users[0], e.group(t, "Down", "critical"))
		ready := make(chan struct{})
		gw := &raceGateway{first: "+10000000001", ready: ready,
			answer: delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: 30 * time.Second}}
		s := e.sender("r1", gw)
		s.Concurrency = 2
		s.Limiter = &racingLimiter{ready: ready, settled: func() {
			// A's record waits for the bucket that B's preparation took its token from.
			waitFor(t, "a wait for a lock", func() bool {
				return e.count(t, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
					WHERE NOT l.granted AND a.datname = current_database()`) > 0
			})
		}}
		e.round(t, s)
		until := e.business.Now().Add(30 * time.Second)
		if got := gw.called(); !slices.Equal(got, []string{"+10000000001"}) {
			t.Fatalf("called %v", got)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'sending'
			AND attempts = 0 AND last_error_class = 'retry_after' AND next_attempt_at = $2`, e.users[0],
			until.Add(SenderTokenMargin)); n != 1 {
			t.Error("A does not wait as asked")
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'waiting'
			AND attempts = 0 AND last_error_class IS NULL AND first_failed_at IS NULL AND lease_owner IS NULL
			AND first_attempt_at IS NULL AND text IS NULL AND language IS NULL AND contact_id IS NULL
			AND action_data IS NULL AND button_key_id IS NULL AND next_attempt_at = $2`, e.users[1],
			until.Add(SenderTokenMargin)); n != 1 {
			t.Error("B, prepared before the answer, does not wait unstarted for the end of the hold")
		}
		if n := e.count(t, `SELECT count(*) FROM rate_limit_buckets WHERE subject_kind = 'connection'
			AND subject_id = $1 AND refilled_at = $2`, e.gw, until); n != 1 {
			t.Error("the bucket is not held")
		}

		fresh := e.group(t, "Fresh", "warning")
		e.page(t, e.users[1], fresh)
		e.ack(t, gb)
		if n := e.count(t, `SELECT count(*) FROM gateway_call_items i JOIN gateway_calls c ON c.id = i.call_id
			WHERE c.user_id = $1 AND c.state = 'waiting'`, e.users[1]); n != 2 ||
			e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1`, e.users[1]) != 1 {
			t.Fatal("the Page during the hold did not join B")
		}
		gw.answerAll()
		e.business.Advance(30*time.Second + SenderTokenMargin)
		e.round(t, s)
		made := gw.madeTo("+10000000002")
		if len(made) != 1 || !strings.Contains(made[0], "#3 Fresh, warning") || strings.Contains(made[0], "Disk") {
			t.Errorf("B after the hold %q", made)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'accepted'
			AND attempts = 1 AND first_attempt_at = $2`, e.users[1], e.business.Now()); n != 1 {
			t.Error("B's first attempt is not the one after the hold")
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_call_items WHERE alert_group_id = $1
			AND dropped_at IS NOT NULL`, gb); n != 1 {
			t.Error("the Alert Group acknowledged during the hold was not dropped")
		}
	})
}

// TestIntegrationUnstart is the undo of a start on PostgreSQL (UnstartCall): a started call that was never attempted
// waits again, unstarted, and a Page joins it; a started call behind which a Page queued a call meanwhile stays
// started, as the person waits in one call at most, and waits until the given time; a lease another replica holds
// changes nothing.
func TestIntegrationUnstart(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		ctx := t.Context()
		e := setupCalls(t, srv, 2)
		s := e.sender("r1", e.gateway)
		e.page(t, e.users[0], e.group(t, "Disk1", "warning"))
		e.page(t, e.users[1], e.group(t, "Disk2", "warning"))
		rows, err := s.Store.ClaimDueCalls(ctx, claimParams(s))
		if err != nil || len(rows) != 2 {
			t.Fatal(rows, err)
		}
		var started []*call
		for _, r := range rows {
			c, err := s.prepare(ctx, r)
			if err != nil || c == nil {
				t.Fatal(c, err)
			}
			started = append(started, c)
		}
		at := e.business.Now().Add(time.Minute)
		other := e.sender("r2", e.gateway)
		for _, c := range started {
			if err := other.unstart(ctx, c, at); err != nil {
				t.Fatal(err)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'sending' AND lease_owner = 'r1'`); n != 2 {
			t.Fatalf("another replica's undo changed %d calls", 2-n)
		}
		e.page(t, e.users[0], e.group(t, "Behind", "warning"))
		for _, c := range started {
			if err := s.unstart(ctx, c, at); err != nil {
				t.Fatal(err)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'sending'
			AND first_attempt_at IS NOT NULL AND lease_owner IS NULL AND next_attempt_at = $2`, e.users[0], at); n != 1 ||
			e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'waiting'
				AND not_before IS NULL`, e.users[0]) != 1 {
			t.Error("the call with a call behind it did not stay started")
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'waiting'
			AND first_attempt_at IS NULL AND text IS NULL AND language IS NULL AND contact_id IS NULL
			AND action_data IS NULL AND button_key_id IS NULL AND lease_owner IS NULL AND next_attempt_at = $2`,
			e.users[1], at); n != 1 {
			t.Error("the call was not undone")
		}
		e.page(t, e.users[1], e.group(t, "Joins", "warning"))
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1`, e.users[1]); n != 1 {
			t.Errorf("the Page made %d calls instead of joining the undone one", n)
		}
	})
}

// TestIntegrationRoundBroken is a Fatal answer among calls attempted at once on PostgreSQL: B's preparation takes its
// token while the gateway refuses A's call, and B's call is made only after the gateway was made Broken. B is not made:
// it waits again, unstarted, due at once, and its next preparation ends it Not delivered with gateway_broken, no
// attempt counted, and records page_not_delivered with it.
func TestIntegrationRoundBroken(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		e := setupBroken(t, srv, 2)
		gb := e.group(t, "Disk", "warning")
		e.page(t, e.users[1], gb)
		e.page(t, e.users[0], e.group(t, "Down", "critical"))
		ready := make(chan struct{})
		gw := &raceGateway{first: "+10000000001", ready: ready,
			answer: delivery.Outcome{Kind: delivery.OutcomeFatal, Status: 401, Error: "401 Unauthorized"}}
		s := e.brokenSender("r1", gw)
		s.Concurrency = 2
		s.Limiter = &racingLimiter{ready: ready, settled: func() {
			waitFor(t, "the Broken state", func() bool {
				return e.count(t, `SELECT count(*) FROM connections WHERE id = $1
					AND gateway_broken_since IS NOT NULL`, e.gw) == 1
			})
		}}
		e.round(t, s)
		if got := gw.called(); !slices.Equal(got, []string{"+10000000001"}) {
			t.Fatalf("called %v", got)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'waiting'
			AND first_attempt_at IS NULL AND text IS NULL AND lease_owner IS NULL`, e.users[1]); n != 1 {
			t.Fatal("B, prepared before the Broken state, does not wait again unstarted")
		}
		e.round(t, s)
		if got := gw.called(); !slices.Equal(got, []string{"+10000000001"}) {
			t.Fatalf("called %v", got)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'not_delivered'
			AND reason = 'call_refused' AND attempts = 1`, e.users[0]); n != 1 {
			t.Error("A is not refused")
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE user_id = $1 AND state = 'not_delivered'
			AND reason = 'gateway_broken' AND attempts = 0`, e.users[1]); n != 1 {
			t.Error("B, prepared before the Broken state, does not end gateway_broken")
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries WHERE alert_group_id = $1
			AND event = 'page_not_delivered' AND reason = 'gateway_broken'`, gb); n != 1 {
			t.Errorf("%d entries of page_not_delivered with gateway_broken", n)
		}
	})
}

// TestIntegrationRoundTwoReplicas is NFR-4 on PostgreSQL with calls attempted at once: two replicas, each attempting
// four calls at once three per claim, call each of 24 people once, never one key while a call with it is in flight,
// and every call ends accepted after one attempt.
func TestIntegrationRoundTwoReplicas(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, srv dbtest.Server) {
		const people = 24
		e := setupCalls(t, srv, people)
		for _, u := range e.users {
			e.page(t, u, e.group(t, "Disk", "warning"))
		}
		gw := &raceGateway{}
		a, b := e.sender("r1", gw), e.sender("r2", gw)
		a.Batch, b.Batch = 3, 3
		var wg sync.WaitGroup
		for _, s := range []*Sender{a, b} {
			wg.Go(func() {
				for range 3 {
					if _, err := s.Round(t.Context()); err != nil {
						t.Error(err)
					}
				}
			})
		}
		wg.Wait()
		if got := gw.called(); len(got) != people || len(slices.Compact(got)) != people || len(gw.twice) != 0 {
			t.Errorf("%d calls, at once twice %v", len(got), gw.twice)
		}
		if n := e.count(t, `SELECT count(*) FROM gateway_calls WHERE state = 'accepted' AND attempts = 1
			AND lease_owner IS NULL`); n != people {
			t.Errorf("%d accepted", n)
		}
	})
}
