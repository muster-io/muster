// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

var gate0 = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

// tooMany is a RetryAfter of 3 s scoped to scope.
func tooMany(scope Scope) Outcome {
	return Outcome{Kind: OutcomeRetryAfter, RetryAfter: 3 * time.Second, Scope: scope, Error: "Too Many Requests"}
}

// TestGateHoldsTheLineUntilTheAnswer: the calls through one Connection take turns, so that the call after a RetryAfter
// answer is not made: its turn comes once the answer held the Connection, and its outcome is the RetryAfter of the hold
// for what is left of it, with the hold's scope and the answer's error. A call through another Connection does not
// wait. Without the turns, or without the hold, the second call would be made.
func TestGateHoldsTheLineUntilTheAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, clk := newGate(), clock.NewManual(gate0)
		conn, other := int64(5), int64(6)
		first, second := Destination{ID: 11, Connection: &conn}, Destination{ID: 12, Connection: &conn}
		elsewhere := Destination{ID: 13, Connection: &other}
		firstTurn, secondTurn := g.reserve(first), g.reserve(second)
		held, leave, err := g.enter(t.Context(), firstTurn, first, clk.Now)
		if held != nil || err != nil {
			t.Fatalf("the first call: %+v, %v", held, err)
		}
		turn := make(chan *Outcome, 1)
		go func() {
			held, leave, err := g.enter(t.Context(), secondTurn, second, clk.Now)
			if err != nil {
				t.Error(err)
				return
			}
			turn <- held
			leave()
		}()
		if held, leave, err := g.enter(t.Context(), ticket{}, elsewhere, clk.Now); held != nil || err != nil {
			t.Fatalf("a call through another Connection: %+v, %v", held, err)
		} else {
			leave()
		}
		synctest.Wait()
		select {
		case h := <-turn:
			t.Fatalf("the second call took its turn before the first was answered: %+v", h)
		default:
		}
		g.answered(first, tooMany(ScopeConnection), clk.Now())
		clk.Advance(time.Second)
		leave()
		got := <-turn
		if got == nil || got.Kind != OutcomeRetryAfter || got.RetryAfter != 2*time.Second ||
			got.Scope != ScopeConnection || got.Error != "Too Many Requests" || got.Unanswered ||
			!got.heldUntil.Equal(gate0.Add(3*time.Second)) {
			t.Fatalf("the second call: %+v", got)
		}
		clk.Advance(2 * time.Second)
		if held := g.held(second, clk.Now()); held != nil {
			t.Fatalf("held after the end of the hold: %+v", held)
		}
	})
}

// TestGateScopes: a RetryAfter scoped to the Destination holds that Destination only, one scoped to the Connection
// every Destination of it, a Destination without a Connection by itself; a shorter hold leaves a longer one in place,
// and any other answer holds nothing; a nil gate, outside a round, holds nothing and lets every call through.
func TestGateScopes(t *testing.T) {
	g, now := newGate(), gate0
	conn := int64(5)
	a, b := Destination{ID: 11, Connection: &conn}, Destination{ID: 12, Connection: &conn}
	hook := Destination{ID: 14}
	g.answered(a, Outcome{Kind: OutcomeTransient}, now)
	g.answered(a, tooMany(ScopeDestination), now)
	g.answered(hook, tooMany(ScopeConnection), now)
	if g.held(a, now) == nil || g.held(b, now) != nil || g.held(hook, now) == nil {
		t.Fatalf("destination scope: %+v %+v %+v", g.held(a, now), g.held(b, now), g.held(hook, now))
	}
	g.answered(b, Outcome{Kind: OutcomeRetryAfter, RetryAfter: 10 * time.Second, Scope: ScopeConnection}, now)
	g.answered(b, tooMany(ScopeConnection), now)
	if h := g.held(a, now.Add(5*time.Second)); h == nil || h.Scope != ScopeConnection || h.RetryAfter != 5*time.Second {
		t.Fatalf("connection scope: %+v", h)
	}
	var none *gate
	held, leave, err := none.enter(t.Context(), none.reserve(a), a, func() time.Time { return now })
	none.answered(a, tooMany(ScopeConnection), now)
	if held != nil || err != nil || none.held(a, now) != nil {
		t.Fatalf("a nil gate: %+v, %v", held, err)
	}
	leave()
}

// TestGateServesALineInTheOrderOfThePreparations: the calls of a line take their turns in the order their places were
// reserved, which the round does right after each preparation, whatever the order their goroutines reach the line; a
// place given up before its turn is skipped, and one given up at its turn passes it on. Served in the order the
// goroutines arrive, the third call would go before the second.
func TestGateServesALineInTheOrderOfThePreparations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, conn := newGate(), int64(5)
		d := Destination{ID: 11, Connection: &conn}
		now := func() time.Time { return gate0 }
		var turns []ticket
		for range 5 {
			turns = append(turns, g.reserve(d))
		}
		var mu sync.Mutex
		var order []uint64
		run := func(ctx context.Context, i int) chan error {
			done := make(chan error, 1)
			go func() {
				_, leave, err := g.enter(ctx, turns[i], d, now)
				if err == nil {
					mu.Lock()
					order = append(order, turns[i].n)
					mu.Unlock()
					leave()
				}
				done <- err
			}()
			return done
		}
		_, leaveFirst, _ := g.enter(t.Context(), turns[0], d, now)
		gone, cancel := context.WithCancel(t.Context())
		fifth := run(t.Context(), 4)
		synctest.Wait()
		third := run(gone, 2)
		synctest.Wait()
		fourth := run(t.Context(), 3)
		synctest.Wait()
		second := run(t.Context(), 1)
		synctest.Wait()
		cancel()
		if err := <-third; !errors.Is(err, context.Canceled) {
			t.Fatalf("the third call: %v", err)
		}
		leaveFirst()
		for _, done := range []chan error{second, fourth, fifth} {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Equal(order, []uint64{2, 4, 5}) {
			t.Fatalf("turns %v", order)
		}
		_, leave, err := g.enter(t.Context(), ticket{}, d, now)
		if err != nil {
			t.Fatal(err)
		}
		leave()
	})
}

// TestGateStopsWhenCtxEnds: a call that waits for its turn stops when ctx ends, with its error, and gives its place up;
// a call whose turn has come when it gives up passes the turn on.
func TestGateStopsWhenCtxEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, conn := newGate(), int64(5)
		d := Destination{ID: 11, Connection: &conn}
		now := func() time.Time { return gate0 }
		first, second, third := g.reserve(d), g.reserve(d), g.reserve(d)
		_, leave, _ := g.enter(t.Context(), first, d, now)
		ctx, cancel := context.WithCancel(t.Context())
		errs := make(chan error, 1)
		go func() {
			_, _, err := g.enter(ctx, second, d, now)
			errs <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
		leave()
		if _, _, err := g.enter(ctx, third, d, now); err != nil {
			t.Fatalf("the third call's turn, come before the end of its ctx: %v", err)
		}
		g.pass(third)
		if _, leave, err := g.enter(t.Context(), ticket{}, d, now); err != nil {
			t.Fatalf("after a turn given up: %v", err)
		} else {
			leave()
		}
	})
}

// heldStore records what the record of a held call writes; any other query fails the test with a nil dereference.
type heldStore struct {
	queries
	retries      []dbgen.RecordDeliveryRetryParams
	replyRetries []dbgen.RecordReplyRetryParams
	eventRetries []dbgen.RecordWebhookEventRetryParams
	holds        []dbgen.HoldBucketParams
}

func (s *heldStore) RecordDeliveryRetry(_ context.Context, arg dbgen.RecordDeliveryRetryParams) (
	dbgen.RecordDeliveryRetryRow, error) {
	s.retries = append(s.retries, arg)
	return dbgen.RecordDeliveryRetryRow{}, nil
}

func (s *heldStore) RecordReplyRetry(_ context.Context, arg dbgen.RecordReplyRetryParams) (dbgen.RecordReplyRetryRow,
	error) {
	s.replyRetries = append(s.replyRetries, arg)
	return dbgen.RecordReplyRetryRow{}, nil
}

func (s *heldStore) RecordWebhookEventRetry(_ context.Context, arg dbgen.RecordWebhookEventRetryParams) (
	dbgen.RecordWebhookEventRetryRow, error) {
	s.eventRetries = append(s.eventRetries, arg)
	return dbgen.RecordWebhookEventRetryRow{}, nil
}

func (s *heldStore) HoldBucket(_ context.Context, arg dbgen.HoldBucketParams) error {
	s.holds = append(s.holds, arg)
	return nil
}

func (s *heldStore) RecordRepublished(context.Context, dbgen.RecordRepublishedParams) (int64, error) {
	return 1, nil
}

func (s *heldStore) InsertDeliveryEvent(context.Context, dbgen.InsertDeliveryEventParams) error {
	return nil
}

func (s *heldStore) LockReplyDelivery(context.Context, dbgen.LockReplyDeliveryParams) error {
	return nil
}

func (s *heldStore) WipeDestinationSecrets(context.Context, dbgen.WipeDestinationSecretsParams) error {
	return nil
}

type heldTx struct{ pgx.Tx }

func (heldTx) Commit(context.Context) error   { return nil }
func (heldTx) Rollback(context.Context) error { return nil }

// heldBeginner begins fake transactions, running began first.
type heldBeginner struct{ began func() }

func (b heldBeginner) Begin(context.Context) (pgx.Tx, error) {
	b.began()
	return heldTx{}, nil
}

// noCalls is an adapter and an event sender that fail the test when called.
type noCalls struct{ t *testing.T }

func (n noCalls) Publish(context.Context, Call, Message) Outcome {
	n.t.Error("published while held")
	return Outcome{}
}

func (n noCalls) Update(context.Context, Call, string, Message) Outcome {
	n.t.Error("edited while held")
	return Outcome{}
}

func (n noCalls) Reply(context.Context, Call, Root, Message) Outcome {
	n.t.Error("replied while held")
	return Outcome{}
}

func (n noCalls) LengthLimit() int { return 4000 }

func (n noCalls) SendEvent(context.Context, EventCall) Outcome {
	n.t.Error("sent an event while held")
	return Outcome{}
}

// TestHeldCallsAreRecordedAsTheirRetryAfter: a call that the gate holds is not made; its outcome is recorded as the
// RetryAfter answer it stands for, through the rules of outcomes.go — due at the end of the hold plus TokenMargin, an
// event too, as the held bucket would refuse it, no attempt counted, the start of a Publication that this preparation
// made undone and one made before it kept for an Idempotent adapter, the bucket held again until the very end of the
// hold, measured once, though time passed before the record — and it is neither counted in
// muster_delivery_attempts_total nor logged as a delivery_attempt.
func TestHeldCallsAreRecordedAsTheirRetryAfter(t *testing.T) {
	clk, conn := clock.NewManual(gate0), int64(5)
	d := Destination{ID: 11, PublicID: "DSAAAAAAAAAAG1", Type: TypeMattermost, Connection: &conn}
	hook := Destination{ID: 12, PublicID: "DSAAAAAAAAAAG2", Type: TypeWebhook}
	s := &heldStore{}
	var log bytes.Buffer
	// Each record begins 100 ms after the turn of its call.
	w := &Worker{Store: &Store{begin: heldBeginner{func() { clk.Advance(100 * time.Millisecond) }},
		queries: func(dbgen.DBTX) queries { return s }},
		Lease:    db.Lease{Owner: "r1", Duration: Lease, Clocks: clock.Clocks{Business: clk, Real: clk}},
		Adapters: Adapters{TypeMattermost: noCalls{t}}, Events: noCalls{t}, Log: logging.New(&log, logging.LevelInfo)}
	g := newGate()
	g.answered(d, tooMany(ScopeConnection), clk.Now())
	g.answered(hook, tooMany(ScopeDestination), clk.Now())
	clk.Advance(time.Second)
	ctx := withGate(t.Context(), g)
	end := gate0.Add(3 * time.Second)
	for _, a := range []attempt{
		{row: dbgen.GetLeasedDeliveryRow{ID: 1, Attempts: 2}, destination: d, publication: true},
		{row: dbgen.GetLeasedDeliveryRow{ID: 2}, destination: d, publication: true, idempotent: true},
		{row: dbgen.GetLeasedDeliveryRow{ID: 3}, destination: d, publication: true, idempotent: true, started: true},
	} {
		if err := w.call(ctx, 1, a); err != nil {
			t.Fatal(err)
		}
	}
	for i, keep := range []bool{false, false, true} {
		r := s.retries[i]
		if !r.At.Equal(end.Add(TokenMargin)) || r.Counted || r.KeepStarted != keep ||
			r.ErrorClass.String != string(OutcomeRetryAfter) || r.Error.String != "Too Many Requests" {
			t.Errorf("delivery %d: %+v", i+1, r)
		}
	}
	ended, err := w.sendReply(ctx, 1, replyAttempt{row: dbgen.GetLeasedReplyRow{ID: 9, DeliveryID: 1}, destination: d})
	if err != nil || ended || len(s.replyRetries) != 1 || !s.replyRetries[0].At.Equal(end.Add(TokenMargin)) ||
		s.replyRetries[0].Counted {
		t.Errorf("the reply: ended %v, %v, %+v", ended, err, s.replyRetries)
	}
	if err := w.callEvent(ctx, 1, eventAttempt{row: dbgen.GetLeasedWebhookEventRow{ID: 4}, destination: hook,
		at: clk.Now()}); err != nil || len(s.eventRetries) != 1 || !s.eventRetries[0].At.Equal(end.Add(TokenMargin)) ||
		s.eventRetries[0].Counted {
		t.Errorf("the event: %v, %+v", err, s.eventRetries)
	}
	if len(s.holds) != 4 {
		t.Fatalf("holds %+v", s.holds)
	}
	for _, h := range s.holds {
		if !h.Until.Equal(end) || h.SubjectKind != string(ScopeConnection) || h.SubjectID != conn {
			t.Errorf("hold %+v", h)
		}
	}
	if strings.Contains(log.String(), logging.DeliveryAttempt.Name()) {
		t.Errorf("logged as an attempt: %s", log.String())
	}
}

// answering is an adapter that answers every call with out, a republication's pointer reply with pointer, and counts
// the calls.
type answering struct {
	out, pointer Outcome
	calls        int
}

func (a *answering) Publish(context.Context, Call, Message) Outcome {
	a.calls++
	return a.out
}

func (a *answering) Update(context.Context, Call, string, Message) Outcome {
	a.calls++
	return a.out
}

func (a *answering) Reply(_ context.Context, call Call, _ Root, _ Message) Outcome {
	a.calls++
	if call.Key == "pointer" {
		return a.pointer
	}
	return a.out
}

func (a *answering) LengthLimit() int                                       { return 4000 }
func (a *answering) EditWindow(context.Context, Call) (time.Duration, bool) { return 0, false }
func (a *answering) PointerKey(Destination, int64, int64) string            { return "pointer" }
func (a *answering) FinalKey(Destination, int64) string                     { return "final" }

// TestOneHoldEndForEveryRowOfAHold is C-11.FR-3 and FR-8 with attempts made at once: the end of a hold is measured
// once, when the RetryAfter answer arrives — to a delivery's call, to a Thread reply or to a republication's pointer
// reply — and the record of the row that got it takes that end, as the records of the rows the gate then held do,
// though time passed before each record: they are due at that end plus TokenMargin and hold the bucket until it.
// Measured again at its record, the row that got the answer would come due after the rows it held, and a held row of
// a later rank could take the next token before it.
func TestOneHoldEndForEveryRowOfAHold(t *testing.T) {
	conn := int64(5)
	d := Destination{ID: 11, PublicID: "DSAAAAAAAAAAH1", Type: TypeBridge, Connection: &conn}
	other := Destination{ID: 12, PublicID: "DSAAAAAAAAAAH2", Type: TypeBridge, Connection: &conn}
	limited := Outcome{Kind: OutcomeRetryAfter, RetryAfter: 7 * time.Second, Scope: ScopeConnection,
		Error: "rate_limited"}
	for _, c := range []struct {
		name    string
		adapter *answering
		end     time.Time
		answer  func(ctx context.Context, w *Worker) error
	}{
		{"delivery", &answering{out: tooMany(ScopeConnection)}, gate0.Add(3 * time.Second),
			func(ctx context.Context, w *Worker) error {
				return w.call(ctx, 1, attempt{row: dbgen.GetLeasedDeliveryRow{ID: 1}, destination: d,
					leaseUntil: gate0.Add(Lease)})
			}},
		{"thread reply", &answering{out: tooMany(ScopeConnection)}, gate0.Add(3 * time.Second),
			func(ctx context.Context, w *Worker) error {
				_, err := w.sendReply(ctx, 1, replyAttempt{row: dbgen.GetLeasedReplyRow{ID: 8, DeliveryID: 1},
					destination: d, leaseUntil: gate0.Add(Lease)})
				return err
			}},
		{"pointer reply", &answering{out: Outcome{Kind: OutcomeOK, MessageID: "m2"}, pointer: limited},
			gate0.Add(7 * time.Second), func(ctx context.Context, w *Worker) error {
				return w.call(ctx, 1, attempt{row: dbgen.GetLeasedDeliveryRow{ID: 1}, destination: d, republish: true,
					leaseUntil: gate0.Add(Lease)})
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			clk, s := clock.NewManual(gate0), &heldStore{}
			// Each record begins 100 ms after the call it records, the answer's included.
			w := &Worker{Store: &Store{begin: heldBeginner{func() { clk.Advance(100 * time.Millisecond) }},
				queries: func(dbgen.DBTX) queries { return s }},
				Lease:    db.Lease{Owner: "r1", Duration: Lease, Clocks: clock.Clocks{Business: clk, Real: clk}},
				Adapters: Adapters{TypeBridge: c.adapter}, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo)}
			ctx := withGate(t.Context(), newGate())
			if err := c.answer(ctx, w); err != nil {
				t.Fatal(err)
			}
			calls := c.adapter.calls
			if err := w.call(ctx, 1, attempt{row: dbgen.GetLeasedDeliveryRow{ID: 2}, destination: other}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.sendReply(ctx, 1, replyAttempt{row: dbgen.GetLeasedReplyRow{ID: 9, DeliveryID: 2},
				destination: other}); err != nil {
				t.Fatal(err)
			}
			if c.adapter.calls != calls || !clk.Now().After(gate0.Add(200*time.Millisecond)) {
				t.Fatalf("%d calls while held, the records began by %v", c.adapter.calls-calls, clk.Now())
			}
			due := c.end.Add(TokenMargin)
			for _, r := range s.retries {
				if !r.At.Equal(due) || r.Counted {
					t.Errorf("delivery %d due at %v, want %v", r.ID, r.At, due)
				}
			}
			for _, r := range s.replyRetries {
				if !r.At.Equal(due) || r.Counted {
					t.Errorf("thread reply %d due at %v, want %v", r.ID, r.At, due)
				}
			}
			if n := len(s.retries) + len(s.replyRetries); n != 3 && (c.name != "pointer reply" || n != 2) {
				t.Errorf("%d rows due again: %+v %+v", n, s.retries, s.replyRetries)
			}
			if len(s.holds) != 3 {
				t.Fatalf("holds %+v", s.holds)
			}
			for _, h := range s.holds {
				if !h.Until.Equal(c.end) || h.SubjectKind != string(ScopeConnection) || h.SubjectID != conn {
					t.Errorf("hold %+v, want the Connection until %v", h, c.end)
				}
			}
		})
	}
}

// leaseStore answers the guarded renewal of a lease at the turn of a call with rows, and keeps what it was asked.
type leaseStore struct {
	queries
	rows       int64
	deliveries []dbgen.RenewHeldDeliveryLeaseParams
	replies    []dbgen.RenewHeldReplyLeaseParams
}

func (s *leaseStore) RenewHeldDeliveryLease(_ context.Context, arg dbgen.RenewHeldDeliveryLeaseParams) (int64, error) {
	s.deliveries = append(s.deliveries, arg)
	return s.rows, nil
}

func (s *leaseStore) RenewHeldReplyLease(_ context.Context, arg dbgen.RenewHeldReplyLeaseParams) (int64, error) {
	s.replies = append(s.replies, arg)
	return s.rows, nil
}

// counted is an adapter that counts its calls and answers each ok, the pointer reply of a republication with a
// RetryAfter of the Connection.
type counted struct{ calls int }

func (c *counted) Publish(context.Context, Call, Message) Outcome {
	c.calls++
	return Outcome{Kind: OutcomeOK, MessageID: "m"}
}

func (c *counted) Update(context.Context, Call, string, Message) Outcome {
	c.calls++
	return Outcome{Kind: OutcomeOK}
}

func (c *counted) Reply(_ context.Context, call Call, _ Root, _ Message) Outcome {
	c.calls++
	if call.Key == "pointer" {
		return Outcome{Kind: OutcomeRetryAfter, RetryAfter: 7 * time.Second, Scope: ScopeConnection,
			Error: "rate_limited"}
	}
	return Outcome{Kind: OutcomeOK}
}

func (c *counted) LengthLimit() int                                       { return 4000 }
func (c *counted) EditWindow(context.Context, Call) (time.Duration, bool) { return 0, false }
func (c *counted) PointerKey(Destination, int64, int64) string            { return "pointer" }
func (c *counted) FinalKey(Destination, int64) string                     { return "final" }

type noDatabase struct{}

func (noDatabase) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("no database") }

// TestLateTurnRenewsTheLease: a call whose turn comes with at least turnBudget of its lease left — its three requests
// within outbound.CallTimeout and one more, 40 s of the 60 s Lease — is made without a statement; one whose turn comes
// later, though more than half the lease is left, renews its lease first, guarded by it, and is made under the renewed
// lease; and one whose lease ran out while it waited is neither made nor recorded, as another replica may hold the row
// and send a newer state — a delivery and a Thread reply alike. Without the renewal, the call whose lease ran out would
// send the state its preparation read, and one made with half a lease left could outlast it.
func TestLateTurnRenewsTheLease(t *testing.T) {
	if turnBudget != 40*time.Second || turnBudget >= Lease {
		t.Fatalf("the budget of a call at its turn is %v of a %v lease", turnBudget, Lease)
	}
	clk, conn := clock.NewManual(gate0), int64(5)
	d := Destination{ID: 11, PublicID: "DSAAAAAAAAAAL1", Type: TypeMattermost, Connection: &conn}
	s, adapter := &leaseStore{rows: 1}, &counted{}
	w := &Worker{Store: &Store{begin: noDatabase{}, queries: func(dbgen.DBTX) queries { return s }},
		Lease:    db.Lease{Owner: "r1", Duration: Lease, Clocks: clock.Clocks{Business: clk, Real: clk}},
		Adapters: Adapters{TypeMattermost: adapter}, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo)}
	ctx := withGate(t.Context(), newGate())
	now := clk.Now()
	for _, c := range []struct {
		name       string
		leaseUntil time.Time
		rows       int64
		renewed    bool
		called     bool
	}{
		{"early", now.Add(turnBudget), 1, false, true},
		{"late", now.Add(turnBudget - time.Second), 1, true, true},
		{"half a lease left", now.Add(Lease/2 + time.Second), 1, true, true},
		{"lost", now.Add(-time.Second), 0, true, false},
	} {
		s.rows, s.deliveries, s.replies, adapter.calls = c.rows, nil, nil, 0
		err := w.call(ctx, 1, attempt{row: dbgen.GetLeasedDeliveryRow{ID: 1}, destination: d, leaseUntil: c.leaseUntil})
		if (err == nil) == c.called || len(s.deliveries) > 0 != c.renewed || (adapter.calls > 0) != c.called {
			t.Errorf("delivery, %s: %v, renewed %+v, %d calls", c.name, err, s.deliveries, adapter.calls)
		}
		if c.renewed && (!s.deliveries[0].Now.Equal(now) || !s.deliveries[0].LeaseUntil.Equal(now.Add(Lease)) ||
			s.deliveries[0].Owner != "r1") {
			t.Errorf("delivery, %s: renewed %+v", c.name, s.deliveries[0])
		}
		_, err = w.sendReply(ctx, 1, replyAttempt{row: dbgen.GetLeasedReplyRow{ID: 9}, destination: d,
			leaseUntil: c.leaseUntil})
		if (err == nil) == c.called || len(s.replies) > 0 != c.renewed || (adapter.calls > 1) != c.called {
			t.Errorf("reply, %s: %v, renewed %+v, %d calls", c.name, err, s.replies, adapter.calls)
		}
	}
}

// TestPointerReplyRetryAfterHoldsTheGate: the pointer reply of a republication past a bridge's edit window, answered
// RetryAfter for the Connection, holds the Connection in the round's gate, as its record holds the bucket, so that the
// calls prepared before that record are not made.
func TestPointerReplyRetryAfterHoldsTheGate(t *testing.T) {
	clk, conn := clock.NewManual(gate0), int64(5)
	d := Destination{ID: 11, PublicID: "DSAAAAAAAAAAP1", Type: TypeBridge, Connection: &conn}
	other := Destination{ID: 12, PublicID: "DSAAAAAAAAAAP2", Type: TypeBridge, Connection: &conn}
	w := &Worker{Store: &Store{begin: noDatabase{}},
		Lease:    db.Lease{Owner: "r1", Duration: Lease, Clocks: clock.Clocks{Business: clk, Real: clk}},
		Adapters: Adapters{TypeBridge: &counted{}}, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo)}
	g := newGate()
	_ = w.call(withGate(t.Context(), g), 1, attempt{row: dbgen.GetLeasedDeliveryRow{ID: 1}, destination: d,
		republish: true, leaseUntil: clk.Now().Add(Lease)})
	if h := g.held(other, clk.Now()); h == nil || h.Scope != ScopeConnection {
		t.Fatalf("the pointer reply's RetryAfter does not hold the Connection: %+v", h)
	}
}

// TestConcurrencyDefault: a Worker without a Concurrency makes up to Concurrency attempts at once once a second claim
// of its round came back full (TestRoundMakesAttemptsAtOnceWithABacklog), and one with its own makes that many.
func TestConcurrencyDefault(t *testing.T) {
	if got := (&Worker{}).concurrency(); got != Concurrency {
		t.Errorf("default %d", got)
	}
	if got := (&Worker{Concurrency: 2}).concurrency(); got != 2 {
		t.Errorf("own %d", got)
	}
}
