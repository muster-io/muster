// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// watch is an adapter over the recorder that keeps how many calls are in flight at once, at most, and the Root
// messages — a Destination and an Alert Group — called while a call to them was in flight. Each call tells entered,
// when it has room, and with hold set waits until hold is closed — only the calls of holdMethod when it is set; during
// runs inside each call, before its answer.
type watch struct {
	*deliverytest.Recorder
	hold       chan struct{}
	holdMethod string
	entered    chan struct{}
	during     func(ctx context.Context, method string, root [2]int64)

	mu    sync.Mutex
	now   int
	most  int
	roots map[[2]int64]int
	twice [][2]int64
}

func newWatch(rec *deliverytest.Recorder) *watch {
	return &watch{Recorder: rec, roots: map[[2]int64]int{}}
}

func (w *watch) call(ctx context.Context, method string, f func() delivery.Outcome) delivery.Outcome {
	l := outbound.LinksOf(ctx)
	root := [2]int64{l.Destination, l.AlertGroup}
	w.mu.Lock()
	w.now++
	w.most = max(w.most, w.now)
	w.roots[root]++
	if w.roots[root] > 1 {
		w.twice = append(w.twice, root)
	}
	w.mu.Unlock()
	select {
	case w.entered <- struct{}{}:
	default:
	}
	if w.hold != nil && (w.holdMethod == "" || w.holdMethod == method) {
		<-w.hold
	}
	if w.during != nil {
		w.during(ctx, method, root)
	}
	out := f()
	w.mu.Lock()
	w.now--
	w.roots[root]--
	w.mu.Unlock()
	return out
}

func (w *watch) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	return w.call(ctx, deliverytest.MethodPublish, func() delivery.Outcome { return w.Recorder.Publish(ctx, c, m) })
}

func (w *watch) Update(ctx context.Context, c delivery.Call, id string, m delivery.Message) delivery.Outcome {
	return w.call(ctx, deliverytest.MethodUpdate, func() delivery.Outcome { return w.Recorder.Update(ctx, c, id, m) })
}

func (w *watch) Reply(ctx context.Context, c delivery.Call, root delivery.Root, m delivery.Message) delivery.Outcome {
	return w.call(ctx, deliverytest.MethodReply, func() delivery.Outcome { return w.Recorder.Reply(ctx, c, root, m) })
}

// seen is the most calls in flight at once and the Root messages called twice at once.
func (w *watch) seen() (int, [][2]int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.most, slices.Clone(w.twice)
}

// concurrent is a worker of the replica owner that makes delivery.Concurrency attempts at once from the start of each
// round, without waiting for a full claim as the default does, through a.
func (l *live) concurrent(owner string, a delivery.Adapter) *delivery.Worker {
	w := l.worker(owner)
	w.Adapters = delivery.Adapters{delivery.TypeMattermost: a}
	w.Concurrency = delivery.Concurrency
	return w
}

// first429 is an adapter over the recorder whose first call of method — a Publication or a Thread reply — waits until
// release is closed; the recorder's script answers it.
type first429 struct {
	*deliverytest.Recorder
	method  string
	release chan struct{}
	once    sync.Once
}

func (f *first429) wait(method string) {
	first := false
	if method == f.method {
		f.once.Do(func() { first = true })
	}
	if first {
		<-f.release
	}
}

func (f *first429) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	f.wait(deliverytest.MethodPublish)
	return f.Recorder.Publish(ctx, c, m)
}

func (f *first429) Reply(ctx context.Context, c delivery.Call, root delivery.Root, m delivery.Message) delivery.Outcome {
	f.wait(deliverytest.MethodReply)
	return f.Recorder.Reply(ctx, c, root, m)
}

// roundEvent is an event a round hook saw (delivery.WithRoundHook).
type roundEvent struct {
	event string
	id    int64
}

// hooked is ctx with a round hook that sends every event of the rounds run under it to the channel it returns.
func hooked(ctx context.Context) (context.Context, chan roundEvent) {
	events := make(chan roundEvent, 1024)
	return delivery.WithRoundHook(ctx, func(event string, id int64) { events <- roundEvent{event, id} }), events
}

// awaitPrepared waits until the rounds of events prepared n rows: their attempts started, and each that calls through
// a Connection holds its place on the Connection's line.
func awaitPrepared(events chan roundEvent, n int) {
	for n > 0 {
		if e := <-events; e.event == delivery.RoundPrepared {
			n--
		}
	}
}

// idempotent429 is first429 for a messenger that keeps idempotency keys (C-22.FR-6).
type idempotent429 struct{ *first429 }

func (idempotent429) PublicationKey(d delivery.Destination, id, n int64) string {
	return fmt.Sprintf("%s/%d/%d", d.PublicID, id, n)
}

func (idempotent429) ReplyKey(d delivery.Destination, id int64) string {
	return fmt.Sprintf("%s/r/%d", d.PublicID, id)
}

// together runs one round of each worker at the same time, as replicas do, and returns how they failed.
func together(ctx context.Context, ws ...*delivery.Worker) error {
	var wg sync.WaitGroup
	errs := make([]error, len(ws))
	for i, w := range ws {
		wg.Go(func() { _, errs[i] = w.Round(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// fireMany fires one Alert Group per name prefix%02d/a, n of them, and returns them in order.
func (l *live) fireMany(t *testing.T, prefix string, n int) []*groups.Group {
	t.Helper()
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s%02d/a", prefix, i)
	}
	l.fire(t, prefix, names...)
	out := make([]*groups.Group, n)
	for i, name := range names {
		g := &groups.Group{RouteID: l.routeID, Status: groups.StatusFiring}
		if err := l.d.Pool.QueryRow(t.Context(), `SELECT id, public_id, number, title FROM alert_groups
			WHERE title LIKE $1 || '%' ORDER BY id DESC LIMIT 1`, name[:len(name)-2]).Scan(&g.ID, &g.PublicID,
			&g.Number, &g.Title); err != nil {
			t.Fatalf("the alert group of %s: %v", name, err)
		}
		out[i] = g
	}
	return out
}

// settled reports the deliveries that are not delivered at their latest Desired state, or whose lease is held.
func (l *live) settled(t *testing.T) int64 {
	t.Helper()
	return l.count(t, `SELECT count(*) FROM deliveries WHERE state <> 'delivered' OR actual_version <> desired_version
		OR lease_owner IS NOT NULL`)
}

// TestIntegrationConcurrentAttempts is the delivery worker making delivery.Concurrency attempts at once on PostgreSQL
// (C-11.FR-1, FR-3, FR-4; C-22.FR-6): the guarantees of attempts made one at a time hold.
func TestIntegrationConcurrentAttempts(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)

		t.Run("replies_keep_the_thread_ref_chain", func(t *testing.T) {
			// The Thread replies of one delivery are sent one after another, each with the `thread_ref` the reply
			// before it stored, while those of the other delivery go beside them: four replies of each of two Root
			// messages through one Connection due together — a closing batch of new Alerts and three pending replies —
			// the batch and the reply after it next to each other in the same claim; and the calls through the
			// Connection are made one at a time.
			l.fresh(t, 1000, 1)
			wa := newWatch(l.rec)
			w := l.concurrent("r1", wa)
			gs := l.fireMany(t, "Chain", 2)
			l.round(t, w)
			if n := l.rec.Count(deliverytest.MethodPublish); n != len(gs) {
				t.Fatalf("%d publications", n)
			}
			for _, g := range gs {
				for seq := int64(2); seq <= 5; seq++ {
					if err := pgxTx(t, l, func(tx groups.DBTX) error {
						return l.svc.Enqueue(t.Context(), tx, groups.Rendering{Group: g, Actor: groups.System,
							Events: []groups.Recorded{{Seq: seq, Event: groups.EventAlertReplaced,
								Variant: groups.VariantAny, Loudness: groups.Quiet}}})
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			// A pending reply does not wait for a collecting batch before it: a claim takes both.
			l.exec(t, `UPDATE thread_replies SET next_attempt_at = $1`, l.business.Now())
			l.exec(t, `UPDATE thread_replies SET state = 'collecting'
				WHERE id IN (SELECT min(id) FROM thread_replies GROUP BY delivery_id)`)
			if n := l.count(t, `SELECT count(*) FROM thread_replies WHERE state IN ('collecting', 'pending')`); n != 8 {
				t.Fatalf("%d replies", n)
			}
			answers := make([]deliverytest.Answer, 8)
			for i := range answers {
				answers[i] = deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
					ThreadRef: fmt.Sprintf("ref-%d", i+1)}}
			}
			l.rec.Script(deliverytest.MethodReply, answers...)
			for range 10 {
				if l.count(t, `SELECT count(*) FROM thread_replies WHERE state <> 'sent'`) == 0 {
					break
				}
				l.round(t, w)
			}
			chains := map[string][]deliverytest.Call{}
			for _, c := range l.rec.Calls() {
				if c.Method == deliverytest.MethodReply {
					chains[c.Root.MessageID] = append(chains[c.Root.MessageID], c)
				}
			}
			if len(chains) != len(gs) {
				t.Fatalf("replies under %d Root messages", len(chains))
			}
			for root, calls := range chains {
				prev := ""
				for i, c := range calls {
					if c.Root.ThreadAnchorID != prev {
						t.Errorf("%s: reply %d went with %q, after the reply that returned %q", root, i+1,
							c.Root.ThreadAnchorID, prev)
					}
					prev = c.Answered.ThreadRef
				}
				if len(calls) != 4 || l.count(t, `SELECT count(*) FROM deliveries WHERE message_id = $1
					AND thread_anchor_id = $2`, root, prev) != 1 {
					t.Errorf("%s: %d replies, the last returned %q", root, len(calls), prev)
				}
			}
			most, twice := wa.seen()
			if most != 1 || len(twice) > 0 {
				t.Errorf("at most %d calls through the Connection at once, Root messages called twice at once %v", most,
					twice)
			}
			t.Log("8 replies of 2 Root messages through one Connection: each Root message's chain of thread_ref intact; " +
				"one call at a time")
		})

		t.Run("two_replicas_attempt_once", func(t *testing.T) {
			// Two workers, each making delivery.Concurrency attempts at once, on 40 Root messages and their edits:
			// one Publication each, no Root message called twice at once, and every one at its latest state. While
			// the first calls hang, each worker has delivery.Concurrency attempts prepared, and one call through the
			// Connection, whose calls take turns.
			l.fresh(t, 1000, 1)
			l.attach(t, l.dests[0], l.dests[1])
			wa := newWatch(l.rec)
			wa.hold, wa.entered = make(chan struct{}), make(chan struct{}, 64)
			a, b := l.concurrent("r1", wa), l.concurrent("r2", wa)
			gs := l.fireMany(t, "Twin", 20)
			first := make(chan error, 1)
			ctx, events := hooked(t.Context())
			go func() { first <- together(ctx, a, b) }()
			<-wa.entered
			<-wa.entered
			awaitPrepared(events, 2*delivery.Concurrency)
			if most, twice := wa.seen(); most != 2 || len(twice) > 0 {
				t.Errorf("with the first calls hanging: %d in flight, twice at once %v", most, twice)
			}
			close(wa.hold)
			if err := <-first; err != nil {
				t.Fatal(err)
			}
			rounds := func(n int) {
				for range n {
					if err := together(t.Context(), a, b); err != nil {
						t.Fatal(err)
					}
				}
			}
			rounds(2)
			for _, g := range gs {
				if _, err := l.groups.Acknowledge(t.Context(), l.alice, g.PublicID); err != nil {
					t.Fatal(err)
				}
			}
			rounds(3)
			published, last := map[[2]int64]int{}, map[[2]int64]string{}
			for _, c := range l.rec.Calls() {
				root := [2]int64{c.Links.Destination, c.Links.AlertGroup}
				switch c.Method {
				case deliverytest.MethodPublish:
					published[root]++
				case deliverytest.MethodUpdate:
					last[root] = c.Message.Colour
				}
			}
			for root, n := range published {
				if n != 1 || last[root] != messages.ColourAcknowledged {
					t.Errorf("%v: %d publications, last edit %q", root, n, last[root])
				}
			}
			most, twice := wa.seen()
			if len(published) != 40 || l.settled(t) != 0 || most > 2*delivery.Concurrency || len(twice) > 0 {
				t.Errorf("%d Root messages published, %d not settled, at most %d calls at once, twice at once %v",
					len(published), l.settled(t), most, twice)
			}
			t.Logf("two workers, 40 Root messages: one publication each, the acknowledged state last, at most %d "+
				"calls in flight at once, none twice", most)
		})

		t.Run("limiters_never_exceeded", func(t *testing.T) {
			// The limiter of a Connection, 3 a minute, shared by two Destinations, and then the limiter of a
			// Destination, 6 a minute, each worked by two workers making delivery.Concurrency attempts at once: the
			// adapter sees the rate of one limiter, as one attempt at a time does (limiter_two_replicas).
			for _, c := range []struct {
				name                   string
				dest, destPer          int
				conn, connPer, perStep int
				dests                  []int64
			}{
				{"connection", 1000, 1, 3, 60, 20, l.dests},
				{"destination", 6, 60, 1000, 1, 10, l.dests[:1]},
			} {
				l.fresh(t, c.dest, c.destPer)
				l.attach(t, c.dests...)
				l.exec(t, `UPDATE connections SET limiter_limit = $1, limiter_per_seconds = $2 WHERE id = $3`, c.conn,
					c.connPer, l.conn)
				a, b := l.concurrent("r1", l.rec), l.concurrent("r2", l.rec)
				l.fireMany(t, "Lim"+c.name, 15)
				burst := min(c.dest*len(c.dests), c.conn)
				for step := range 62 {
					if err := together(t.Context(), a, b); err != nil {
						t.Fatal(err)
					}
					if want := burst + max(step-1, 0)/c.perStep; len(l.rec.Calls()) != want {
						t.Errorf("%s: by %d s, %d calls, want %d", c.name, step, len(l.rec.Calls()), want)
					}
					l.business.Advance(time.Second)
				}
				t.Logf("%s limiter: %d calls at once, %d after a minute", c.name, burst, len(l.rec.Calls()))
			}
			l.exec(t, `UPDATE connections SET limiter_limit = 1000, limiter_per_seconds = 1 WHERE id = $1`, l.conn)
		})

		t.Run("urgent_never_overtaken", func(t *testing.T) {
			// One token every 10 s, three Urgent and four other Publications on one Destination: every Urgent one
			// goes before the others, each round's token to the earliest rank, though later rows are claimed while
			// earlier calls are in flight.
			l.fresh(t, 1, 10)
			w := l.concurrent("r1", l.rec)
			gs := l.fireMany(t, "Rank", 7)
			urgent := map[int64]bool{}
			for _, g := range gs[4:] {
				l.exec(t, `UPDATE deliveries SET urgent = true WHERE alert_group_id = $1`, g.ID)
				urgent[g.ID] = true
			}
			for range 8 {
				l.round(t, w)
				l.business.Advance(10*time.Second + delivery.TokenMargin)
			}
			calls := l.rec.Calls()
			if len(calls) != len(gs) {
				t.Fatalf("%d calls", len(calls))
			}
			for i, c := range calls {
				if urgent[c.Links.AlertGroup] != (i < 3) {
					t.Errorf("call %d to Alert Group %d, Urgent %v", i+1, c.Links.AlertGroup, urgent[c.Links.AlertGroup])
				}
			}
			t.Log("one token every 10 s: the three Urgent Publications took the first three tokens, the others " +
				"the next four")
		})

		t.Run("latest_state_after_a_change_in_flight", func(t *testing.T) {
			// C-11.FR-1: an Alert Group resolved while the edit of its acknowledgement is in flight gets the resolved
			// state in the next call, never an older one after it, for each of eight Root messages edited at once.
			l.fresh(t, 1000, 1)
			wa := newWatch(l.rec)
			w := l.concurrent("r1", wa)
			gs := l.fireMany(t, "Late", 8)
			l.round(t, w)
			byID := map[int64]*groups.Group{}
			for _, g := range gs {
				byID[g.ID] = g
			}
			var mu sync.Mutex
			resolved := map[int64]bool{}
			wa.during = func(ctx context.Context, method string, root [2]int64) {
				if method != deliverytest.MethodUpdate {
					return
				}
				mu.Lock()
				first := !resolved[root[1]]
				resolved[root[1]] = true
				mu.Unlock()
				if first {
					if _, err := l.groups.Resolve(ctx, l.alice, byID[root[1]].PublicID, nil); err != nil {
						t.Error(err)
					}
				}
			}
			for _, g := range gs {
				if _, err := l.groups.Acknowledge(t.Context(), l.alice, g.PublicID); err != nil {
					t.Fatal(err)
				}
			}
			for range 4 {
				l.round(t, w)
			}
			edits := map[int64][]string{}
			for _, c := range l.rec.Calls() {
				if c.Method == deliverytest.MethodUpdate {
					edits[c.Links.AlertGroup] = append(edits[c.Links.AlertGroup], c.Message.Colour)
				}
			}
			for _, g := range gs {
				if e := edits[g.ID]; !slices.Equal(e, []string{messages.ColourAcknowledged, messages.ColourResolved}) {
					t.Errorf("#%d: edits %v", g.Number, e)
				}
			}
			if n := l.settled(t); n != 0 {
				t.Errorf("%d deliveries not at their latest state", n)
			}
			t.Log("eight Alert Groups resolved during the edit of their acknowledgement: each Root message got the " +
				"acknowledged state, then the resolved one")
		})

		t.Run("round_ends_after_its_attempts", func(t *testing.T) {
			// A round returns only once every attempt it started ended — its call, its record and what runs once
			// that committed — while it makes delivery.Concurrency of them at once and prepares no more: one call
			// hangs, and the three prepared after it wait for their turn on the Connection's line.
			l.fresh(t, 1000, 1)
			wa := newWatch(l.rec)
			wa.hold, wa.entered = make(chan struct{}), make(chan struct{}, 16)
			w := l.concurrent("r1", wa)
			var marked atomic.Int64
			w.Published = func(context.Context, int64, time.Time, bool) error {
				marked.Add(1)
				return nil
			}
			l.fireMany(t, "Ends", 6)
			done := make(chan struct{})
			var marks int64
			ctx, events := hooked(t.Context())
			go func() {
				defer close(done)
				if _, err := w.Round(ctx); err != nil {
					t.Error(err)
				}
				marks = marked.Load()
			}()
			<-wa.entered
			awaitPrepared(events, delivery.Concurrency)
			started := l.count(t, `SELECT count(*) FROM deliveries WHERE publication_started_at IS NOT NULL`)
			select {
			case <-done:
				t.Fatal("the round returned with its attempts in flight")
			default:
			}
			close(wa.hold)
			<-done
			if most, _ := wa.seen(); started != delivery.Concurrency || most != 1 || marks != 6 || l.settled(t) != 0 {
				t.Errorf("%d prepared and at most %d calls while the first hung, %d marked published when the round "+
					"returned, %d not settled", started, most, marks, l.settled(t))
			}
			t.Logf("%d attempts at once while the first call hung, the others waiting for their turn, and no other "+
				"delivery prepared; the round returned after all 6 publications were recorded and marked",
				delivery.Concurrency)
		})

		t.Run("retry_after_holds_the_prepared_calls", func(t *testing.T) {
			// C-13.AC-15, C-24.AC-8, C-22.AC-7 with attempts made at once: of four Publications prepared through one
			// Connection, the first call is answered 429 with Retry-After 1 s for the Connection while the other three
			// wait for their turn. None of them, nor any later delivery, reaches the messenger before the hold ends,
			// and the three end as the answered one does — pending, due at the end of the hold plus TokenMargin, no
			// attempt, the start of their Publication undone — and as it, for an Idempotent adapter, a start made
			// before this round is kept (C-22.FR-6). After the hold every delivery is published.
			for _, c := range []struct {
				prefix     string
				idempotent bool
			}{{"HeldPlain", false}, {"HeldKeyed", true}} {
				l.fresh(t, 1000, 1)
				l.attach(t, l.dests[0], l.dests[1])
				ad := &first429{Recorder: l.rec, method: deliverytest.MethodPublish, release: make(chan struct{})}
				var a delivery.Adapter = ad
				if c.idempotent {
					a = idempotent429{ad}
				}
				w := l.concurrent("r1", a)
				l.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(time.Second, delivery.ScopeConnection))
				l.fireMany(t, c.prefix, 4)
				var ids []int64
				if err := l.d.Pool.QueryRow(t.Context(), `SELECT array_agg(id ORDER BY id) FROM deliveries`).Scan(
					&ids); err != nil || len(ids) != 8 {
					t.Fatalf("deliveries %v, %v", ids, err)
				}
				start, earlier := l.business.Now(), l.business.Now().Add(-time.Hour)
				if c.idempotent {
					l.exec(t, `UPDATE deliveries SET publication_started_at = $1 WHERE id = $2`, earlier, ids[1])
				}
				done := make(chan error, 1)
				ctx, events := hooked(t.Context())
				go func() {
					_, err := w.Round(ctx)
					done <- err
				}()
				// The four slots are taken once four deliveries are prepared: the first call waits, and the next three
				// wait for their turn on the Connection's line.
				awaitPrepared(events, delivery.Concurrency)
				close(ad.release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if n := len(l.rec.Calls()); n != 1 {
					t.Fatalf("%s: %d calls before the hold ended", c.prefix, n)
				}
				type state struct {
					state           string
					attempts        int64
					class, err      string
					due             time.Time
					leased, started bool
					earlierStart    bool
				}
				read := func(id int64) state {
					var s state
					if err := l.d.Pool.QueryRow(t.Context(), `SELECT state, attempts, coalesce(last_error_class, ''),
						coalesce(last_error, ''), next_attempt_at, lease_owner IS NOT NULL,
						publication_started_at IS NOT NULL, publication_started_at IS NOT DISTINCT FROM $2
						FROM deliveries WHERE id = $1`, id, earlier).Scan(&s.state, &s.attempts, &s.class, &s.err, &s.due,
						&s.leased, &s.started, &s.earlierStart); err != nil {
						t.Fatal(err)
					}
					s.due = s.due.UTC()
					return s
				}
				for i, id := range ids[:delivery.Concurrency] {
					want := state{state: "pending", class: "retry_after", err: "Too Many Requests",
						due: start.Add(time.Second + delivery.TokenMargin)}
					if c.idempotent && i == 1 {
						want.started, want.earlierStart = true, true
					}
					if got := read(id); got != want {
						t.Errorf("%s: delivery %d of the four prepared: %+v, want %+v", c.prefix, i+1, got, want)
					}
				}
				l.business.Advance(time.Second + delivery.TokenMargin)
				for range 3 {
					l.round(t, w)
				}
				calls := l.rec.Calls()
				if len(calls) != 9 || l.settled(t) != 0 {
					t.Fatalf("%s: %d calls after the hold, %d not settled", c.prefix, len(calls), l.settled(t))
				}
				for _, call := range calls[1:] {
					if call.At.Before(start.Add(time.Second)) {
						t.Errorf("%s: a call at %v, before the hold ended", c.prefix, call.At)
					}
				}
				t.Logf("%s: a 429 for the Connection on the first of four prepared Publications; the other three ended "+
					"as it did without a call, the later ones waited for their token; all 8 published after the hold",
					c.prefix)
			}
		})

		t.Run("retry_after_holds_the_prepared_replies", func(t *testing.T) {
			// The same for the Thread replies of Pages: of four Page replies prepared through one Connection, the first
			// is answered 429 for the Connection while the other three wait for their turn; none of them reaches the
			// messenger before the hold ends, and the three end as the answered one does — pending, due at the end of
			// the hold plus TokenMargin, no attempt — their Pages waiting for them.
			l.fresh(t, 1000, 1)
			ad := &first429{Recorder: l.rec, method: deliverytest.MethodReply, release: make(chan struct{})}
			w := l.concurrent("r1", ad)
			gs := l.fireMany(t, "PageHeld", 4)
			l.round(t, w)
			for _, g := range gs {
				var page int64
				if err := l.d.Pool.QueryRow(t.Context(), `INSERT INTO pages (org_id, alert_group_id, step_number, kind,
					everyone, state, created_at) VALUES ($1, $2, 1, 'mention', 'here', 'pending', now()) RETURNING id`,
					l.orgID, g.ID).Scan(&page); err != nil {
					t.Fatal(err)
				}
				if err := pgxTx(t, l, func(tx groups.DBTX) error {
					return l.svc.Enqueue(t.Context(), tx, groups.Rendering{Group: g, Actor: groups.System,
						Events: []groups.Recorded{{Seq: 2, Event: groups.EventPaged, Variant: groups.VariantAny,
							Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionPaged},
							Page: &groups.Page{ID: page, Everyone: "here", Targets: []groups.PageTarget{{
								DestinationID: l.dests[0], Source: groups.PageSourceRoute}}}}}})
				}); err != nil {
					t.Fatal(err)
				}
			}
			if n := l.count(t, `SELECT count(*) FROM thread_replies WHERE state = 'pending' AND page_id IS NOT NULL`); n != 4 {
				t.Fatalf("%d page replies", n)
			}
			l.rec.Reset()
			l.rec.Script(deliverytest.MethodReply, deliverytest.RetryAfter(time.Second, delivery.ScopeConnection))
			start := l.business.Now()
			done := make(chan error, 1)
			ctx, events := hooked(t.Context())
			go func() {
				_, err := w.Round(ctx)
				done <- err
			}()
			awaitPrepared(events, 4)
			close(ad.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if n := len(l.rec.Calls()); n != 1 {
				t.Fatalf("%d calls before the hold ended", n)
			}
			if n := l.count(t, `SELECT count(*) FROM thread_replies WHERE state = 'pending' AND attempts = 0
				AND last_error_class = 'retry_after' AND last_error = 'Too Many Requests' AND lease_owner IS NULL
				AND next_attempt_at = $1`, start.Add(time.Second+delivery.TokenMargin)); n != 4 {
				t.Errorf("%d of the four page replies ended as the answered one", n)
			}
			if n := l.count(t, `SELECT count(*) FROM pages WHERE state = 'pending'`); n != 4 {
				t.Errorf("%d pages waiting", n)
			}
			l.business.Advance(time.Second + delivery.TokenMargin)
			for range 3 {
				l.round(t, w)
			}
			calls := l.rec.Calls()
			if len(calls) != 5 || l.count(t, `SELECT count(*) FROM thread_replies WHERE state <> 'sent'`) != 0 {
				t.Fatalf("%d calls after the hold", len(calls))
			}
			for _, call := range calls[1:] {
				if call.At.Before(start.Add(time.Second)) {
					t.Errorf("a reply at %v, before the hold ended", call.At)
				}
			}
			t.Log("a 429 for the Connection on the first of four prepared Page replies; the other three ended as it " +
				"did without a call; all four sent after the hold")
		})

		t.Run("reply_waits_for_the_edit_of_its_root_message", func(t *testing.T) {
			// A Thread reply whose Root message's edit is in flight is prepared only once that edit ended, though the
			// claim took it while the edit's call hung: it waits on the key of its Root message.
			l.fresh(t, 1000, 1)
			wa := newWatch(l.rec)
			w := l.concurrent("r1", wa)
			g := l.fireMany(t, "WaitEdit", 1)[0]
			l.round(t, w)
			l.exec(t, `UPDATE alert_groups SET title = 'WaitEdit changed' WHERE id = $1`, g.ID)
			if err := pgxTx(t, l, func(tx groups.DBTX) error {
				return l.svc.Enqueue(t.Context(), tx, groups.Rendering{Group: g, Actor: groups.System,
					Events: []groups.Recorded{{Seq: 2, Event: groups.EventAlertReplaced, Variant: groups.VariantAny,
						Loudness: groups.Quiet}}})
			}); err != nil {
				t.Fatal(err)
			}
			reply := l.count(t, `SELECT id FROM thread_replies WHERE alert_group_id = $1 AND state = 'pending'`, g.ID)
			wa.hold, wa.holdMethod, wa.entered = make(chan struct{}), deliverytest.MethodUpdate, make(chan struct{}, 16)
			done := make(chan error, 1)
			ctx, events := hooked(t.Context())
			go func() {
				_, err := w.Round(ctx)
				done <- err
			}()
			<-wa.entered
			for e := range events {
				if e.id != reply {
					continue
				}
				if e.event != delivery.RoundWaits {
					t.Errorf("the reply was %s while the edit of its Root message was in flight", e.event)
				}
				break
			}
			close(wa.hold)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if got := methods(l.rec.Calls()); got != "publish,update,reply" {
				t.Fatalf("calls %s", got)
			}
			t.Log("the reply waited for the edit of its Root message in flight, and went after it")
		})

		t.Run("lease_lost_while_waiting_for_the_turn", func(t *testing.T) {
			// An edit whose lease runs out while it waits for its turn behind a hanging call is not sent: another
			// replica claimed it and sent the newer state meanwhile, and the state its preparation read never follows.
			// The worker makes two attempts at once, so that its round claims nothing more while the call hangs.
			l.fresh(t, 1000, 1)
			wa := newWatch(l.rec)
			w := l.concurrent("r1", wa)
			w.Concurrency = 2
			gs := l.fireMany(t, "Lost", 2)
			l.round(t, w)
			for _, g := range gs {
				if _, err := l.groups.Acknowledge(t.Context(), l.alice, g.PublicID); err != nil {
					t.Fatal(err)
				}
			}
			wa.hold, wa.holdMethod, wa.entered = make(chan struct{}), deliverytest.MethodUpdate, make(chan struct{}, 16)
			done := make(chan error, 1)
			ctx, events := hooked(t.Context())
			go func() {
				_, err := w.Round(ctx)
				done <- err
			}()
			awaitPrepared(events, 2)
			<-wa.entered
			if _, err := l.groups.Resolve(t.Context(), l.alice, gs[1].PublicID, nil); err != nil {
				t.Fatal(err)
			}
			l.real.Advance(delivery.Lease + time.Second)
			l.round(t, l.worker("r2"))
			close(wa.hold)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var edits []string
			for _, c := range l.rec.Calls() {
				if c.Method == deliverytest.MethodUpdate && c.Links.AlertGroup == gs[1].ID {
					edits = append(edits, c.Message.Colour)
				}
			}
			if !slices.Equal(edits, []string{messages.ColourResolved}) || l.settled(t) != 0 {
				t.Fatalf("edits of the second Root message %v, %d not settled", edits, l.settled(t))
			}
			t.Log("the lease of a waiting edit ran out; another replica sent the resolved state, and the acknowledged " +
				"state prepared before was not sent")
		})
	})
}
