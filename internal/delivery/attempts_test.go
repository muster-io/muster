// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

// trial is the claims and attempts of the tests of batchOnce: the rows each claim leases, a gate per row that its
// attempt waits at after its preparation, and what happened, in order.
type trial struct {
	mu     sync.Mutex
	claims [][]claimedRow
	gates  map[int64]chan struct{}
	events []string
	// busy is how many claims and attempts run, preparations included, and most the most at once.
	busy, most int
}

func newTrial(claims ...[]claimedRow) *trial {
	tr := &trial{claims: claims, gates: map[int64]chan struct{}{}}
	for _, c := range claims {
		for _, r := range c {
			tr.gates[r.id] = make(chan struct{})
		}
	}
	return tr
}

// row is the claimed delivery id, whose attempt is keyed by the Root message root.
func row(id, root int64) claimedRow { return claimedRow{id: id, key: attemptKey{id: root}} }

func (tr *trial) enter(event string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.busy++
	tr.most = max(tr.most, tr.busy)
	tr.events = append(tr.events, event)
}

func (tr *trial) leave(event string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.busy--
	tr.events = append(tr.events, event)
}

func (tr *trial) claim(context.Context, int64) ([]claimedRow, error) {
	tr.enter("claim")
	defer tr.leave("claimed")
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.claims) == 0 {
		return nil, nil
	}
	c := tr.claims[0]
	tr.claims = tr.claims[1:]
	return c, nil
}

// work prepares the row id and returns the rest of its attempt, which waits at the row's gate until it is opened or
// ctx ends.
func (tr *trial) work(_ context.Context, _, id int64) func(context.Context) {
	tr.enter("prepare " + strconv.FormatInt(id, 10))
	return func(ctx context.Context) {
		select {
		case <-tr.gates[id]:
		case <-ctx.Done():
		}
		tr.leave("end " + strconv.FormatInt(id, 10))
	}
}

func (tr *trial) open(id int64) { close(tr.gates[id]) }

func (tr *trial) state() (busy, most int, events []string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.busy, tr.most, slices.Clone(tr.events)
}

// prepared are the rows prepared, in order.
func prepared(events []string) []string {
	var out []string
	for _, e := range events {
		if id, ok := strings.CutPrefix(e, "prepare "); ok {
			out = append(out, id)
		}
	}
	return out
}

// claims is how many claims ran.
func claims(events []string) int {
	n := 0
	for _, e := range events {
		if e == "claimed" {
			n++
		}
	}
	return n
}

// TestFlightBoundsTheAttemptsAtOnce: a round makes at most Concurrency attempts at once, its claims included: with
// three slots and a batch of ten rows whose attempts hang, three are prepared in the order of the claim and the fourth
// and the next claim wait for a slot; once the attempts end, the rest follow, never more than three at once. One slot
// more would let a fourth attempt in, which the bound refuses.
func TestFlightBoundsTheAttemptsAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const n = 3
		var batch []claimedRow
		for i := range int64(10) {
			batch = append(batch, row(i+1, 100+i))
		}
		tr := newTrial(batch, []claimedRow{row(11, 111)})
		f, w := newFlight(n), &Worker{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 2 {
				if _, err := w.batchOnce(t.Context(), f, 1, tr.claim, tr.work); err != nil {
					t.Error(err)
				}
			}
		}()
		synctest.Wait()
		busy, most, events := tr.state()
		if busy != n || most != n || claims(events) != 1 || !slices.Equal(prepared(events), []string{"1", "2", "3"}) {
			t.Fatalf("with the attempts hanging: %d busy, at most %d, events %v", busy, most, events)
		}
		for i := range int64(11) {
			tr.open(i + 1)
		}
		<-done
		f.wait()
		busy, most, events = tr.state()
		want := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}
		if busy != 0 || most != n || claims(events) != 2 || !slices.Equal(prepared(events), want) {
			t.Fatalf("after the attempts: %d busy, at most %d, events %v", busy, most, events)
		}
	})
}

// TestFlightClaimHoldsASlot: a claim waits for a free slot as an attempt does, so that the claims and attempts of a
// round hold at most Concurrency connections at once.
func TestFlightClaimHoldsASlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := newTrial([]claimedRow{row(1, 101), row(2, 102)}, []claimedRow{row(3, 103)})
		f, w := newFlight(2), &Worker{}
		if _, err := w.batchOnce(t.Context(), f, 1, tr.claim, tr.work); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := w.batchOnce(t.Context(), f, 1, tr.claim, tr.work); err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		if _, _, events := tr.state(); claims(events) != 1 {
			t.Fatalf("a second claim with both slots in use: %v", events)
		}
		tr.open(1)
		synctest.Wait()
		if _, _, events := tr.state(); claims(events) != 2 || !slices.Equal(prepared(events), []string{"1", "2", "3"}) {
			t.Fatalf("once a slot was free: %v", events)
		}
		tr.open(2)
		tr.open(3)
		<-done
		f.wait()
	})
}

// TestNarrowFlightWidensAtTheSecondFullClaim: a narrow flight, a round's of the default Concurrency, makes one attempt
// at a time, each row and the next claim waiting for the attempt before it, until a second claim comes back full, and
// up to its concurrency at once from then on (backlogClaims): with a batch of three, a short claim's two rows and a
// full claim's three go one after another, then the second full claim's three and the next claim's row go four at
// once. A flight that never narrowed, or widened at the first full claim, would overlap the first five, and one that
// never widened, or only after the second full claim, would make the last four one at a time.
func TestNarrowFlightWidensAtTheSecondFullClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := newTrial([]claimedRow{row(1, 101), row(2, 102)}, []claimedRow{row(3, 103), row(4, 104), row(5, 105)},
			[]claimedRow{row(6, 106), row(7, 107), row(8, 108)}, []claimedRow{row(9, 109)})
		f, w := newFlight(4).narrow(), &Worker{Batch: 3}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 4 {
				if _, err := w.batchOnce(t.Context(), f, 1, tr.claim, tr.work); err != nil {
					t.Error(err)
				}
			}
		}()
		var want []string
		for i, claimed := range []int{1, 1, 2, 2, 2} {
			want = append(want, strconv.Itoa(i+1))
			synctest.Wait()
			if busy, most, events := tr.state(); busy != 1 || most != 1 || claims(events) != claimed ||
				!slices.Equal(prepared(events), want) {
				t.Fatalf("one at a time, row %d: %d busy, at most %d, events %v", i+1, busy, most, events)
			}
			tr.open(int64(i + 1))
		}
		synctest.Wait()
		if busy, most, events := tr.state(); busy != 4 || most != 4 || claims(events) != 4 ||
			!slices.Equal(prepared(events), []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}) {
			t.Fatalf("after the second full claim: %d busy, at most %d, events %v", busy, most, events)
		}
		for i := range int64(4) {
			tr.open(i + 6)
		}
		<-done
		f.wait()
		if busy, _, _ := tr.state(); busy != 0 {
			t.Fatalf("%d busy after the attempts", busy)
		}
	})
}

// TestFlightKeepsARootMessageInOrder: a row whose Root message has an attempt in flight is prepared once that attempt
// ended — the next Thread reply of a delivery reads the `thread_ref` the one before stored — while the attempts of
// other Root messages go on beside it, and the rows after it in the claim wait for it, so that the order of the claim
// holds.
func TestFlightKeepsARootMessageInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := newTrial([]claimedRow{row(1, 100), row(2, 200), row(3, 100), row(4, 300)})
		f, w := newFlight(4), &Worker{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := w.batchOnce(t.Context(), f, 1, tr.claim, tr.work); err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		if _, _, events := tr.state(); !slices.Equal(prepared(events), []string{"1", "2"}) {
			t.Fatalf("with the attempt of Root message 100 in flight: %v", events)
		}
		tr.open(2)
		synctest.Wait()
		if _, _, events := tr.state(); !slices.Equal(prepared(events), []string{"1", "2"}) {
			t.Fatalf("row 3 did not wait for row 1: %v", events)
		}
		tr.open(1)
		synctest.Wait()
		_, _, events := tr.state()
		if !slices.Equal(prepared(events), []string{"1", "2", "3", "4"}) ||
			slices.Index(events, "end 1") > slices.Index(events, "prepare 3") {
			t.Fatalf("after row 1 ended: %v", events)
		}
		tr.open(3)
		tr.open(4)
		<-done
		f.wait()
	})
}

// TestFlightKeepsWebhookEventsOfAnAlertGroupInOrder: the events of one Alert Group in one outgoing webhook go one after
// another, while those of another Alert Group, or of the same Alert Group in another webhook, and a delivery whose id
// equals the Destination's do not wait.
func TestFlightKeepsWebhookEventsOfAnAlertGroupInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		event := func(id, dest, group int64) claimedRow {
			return claimedRow{id: id, key: attemptKey{event: true, id: dest, group: group}}
		}
		tr := newTrial([]claimedRow{event(1, 12, 21), event(2, 12, 22), event(3, 13, 21), row(4, 12),
			event(5, 12, 21)})
		f, w := newFlight(8), &Worker{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := w.batchOnce(t.Context(), f, 1, tr.claim, tr.work); err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		if _, _, events := tr.state(); !slices.Equal(prepared(events), []string{"1", "2", "3", "4"}) {
			t.Fatalf("with the event of Alert Group 21 in webhook 12 in flight: %v", events)
		}
		tr.open(1)
		synctest.Wait()
		if _, _, events := tr.state(); !slices.Equal(prepared(events), []string{"1", "2", "3", "4", "5"}) {
			t.Fatalf("after it ended: %v", events)
		}
		for _, id := range []int64{2, 3, 4, 5} {
			tr.open(id)
		}
		<-done
		f.wait()
	})
}

// TestFlightStopsWhenCtxEnds: the end of ctx stops a round that waits for the attempt of a row's Root message or for a
// slot of its next claim, with ctx's error.
func TestFlightStopsWhenCtxEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := newTrial([]claimedRow{row(1, 100), row(2, 100)}, []claimedRow{row(3, 300)})
		f, w := newFlight(1), &Worker{}
		ctx, cancel := context.WithCancel(t.Context())
		errs := make(chan error, 2)
		go func() {
			_, err := w.batchOnce(ctx, f, 1, tr.claim, tr.work)
			errs <- err
			_, err = w.batchOnce(ctx, f, 1, tr.claim, tr.work)
			errs <- err
		}()
		synctest.Wait()
		cancel()
		first, second := <-errs, <-errs
		f.wait()
		_, _, events := tr.state()
		if !errors.Is(first, context.Canceled) || !errors.Is(second, context.Canceled) ||
			!slices.Equal(prepared(events), []string{"1"}) || claims(events) != 1 {
			t.Fatalf("%v, %v, events %v", first, second, events)
		}
	})
}

// TestFlightWaitsForEveryAttempt: wait, which ends a reconcile, returns only once every attempt the round started
// ended, also when ctx ends while a row waits for a slot: the round stops preparing, with ctx's error, and the row it
// did not prepare keeps its lease.
func TestFlightWaitsForEveryAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := newTrial([]claimedRow{row(1, 101), row(2, 102), row(3, 103), row(4, 104)})
		f, w := newFlight(2), &Worker{}
		ctx, cancel := context.WithCancel(t.Context())
		var err error
		claimed := make(chan struct{})
		go func() {
			defer close(claimed)
			_, err = w.batchOnce(ctx, f, 1, tr.claim, tr.work)
		}()
		waited := make(chan struct{})
		go func() {
			<-claimed
			f.wait()
			close(waited)
		}()
		synctest.Wait()
		tr.open(1)
		synctest.Wait()
		select {
		case <-waited:
			t.Fatal("wait returned with two attempts in flight")
		default:
		}
		if _, _, events := tr.state(); !slices.Equal(prepared(events), []string{"1", "2", "3"}) {
			t.Fatalf("once row 1 ended: %v", events)
		}
		cancel()
		<-waited
		busy, _, events := tr.state()
		if !errors.Is(err, context.Canceled) || busy != 0 || !slices.Equal(prepared(events), []string{"1", "2", "3"}) {
			t.Fatalf("after the end of ctx: %v, %d busy, events %v", err, busy, events)
		}
	})
}
