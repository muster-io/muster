// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/logging"
)

// PollWait is bridge.poll_wait: the wait of each pollEvents call, how long the bridge holds the call open while no
// event comes.
const PollWait = 25 * time.Second

// PollMargin is how much longer than its wait Muster waits for the answer of a pollEvents call, so that a long poll is
// never cut short as a timeout.
const PollMargin = 15 * time.Second

const (
	// ReadInterval is how often the poller reads the Bridge Connections again when no change woke it.
	ReadInterval = 30 * time.Second
	// failureBase and failureCap bound the back-off after a failed poll, counted per call.
	failureBase = time.Second
	failureCap  = time.Minute
	// failureLogEvery: bridge_poll_failed is logged at the first failure in a row and at every tenth.
	failureLogEvery = 10
	// emptyPause is the wait after a poll that brought no event, so that a bridge that answers at once whatever the
	// wait makes no tight loop.
	emptyPause = time.Second
)

// Poller polls the events of an Organization's Bridge Connections in poll mode (C-22.FR-11), the Leader task
// bridge_polling (ADR-0007): one loop per Connection calls pollEvents with the stored cursor and Wait, gives each call
// Wait plus PollMargin, hands the events of the batch to Events one at a time, in order, and then stores next_cursor,
// so that a new Leader goes on where the old one stopped. A cursor the bridge refuses as invalid is dropped, and the
// next poll starts without one. A failed call backs off from 1 s to 1 min, counted per call, and logs bridge_poll_failed
// at the first failure in a row and every tenth; it is no delivery outcome and makes nothing Broken. A batch whose
// handling fails keeps the cursor, so that it is polled again.
//
// Every call runs under the context of the run, so that losing leadership or stopping the replica cancels a poll in
// flight at once. Running it on a frozen old Leader and its successor at once is safe: an event polled twice is handled
// once by its id, and a cursor an old Leader stores late only makes the bridge send events again.
type Poller struct {
	Source Connections
	Events *Events
	Log    *logging.Logger
	// Wait is bridge.poll_wait; zero, or longer, is PollWait, so that the long-poll client's timeout is never shorter
	// than a call's deadline.
	Wait time.Duration
	// Every is how often the Connections are read again without a wake; zero is ReadInterval.
	Every time.Duration

	// sleep waits for d unless ctx ends first, jitter spreads a back-off and poll makes one call; tests replace them.
	sleep  func(ctx context.Context, d time.Duration)
	jitter func(d time.Duration) time.Duration
	poll   func(ctx context.Context, c Conn, cursor *string, wait time.Duration) (Batch, Result)

	wakeOnce sync.Once
	wake     chan struct{}
}

func (p *Poller) wakes() chan struct{} {
	p.wakeOnce.Do(func() { p.wake = make(chan struct{}, 1) })
	return p.wake
}

// Wake reads the Connections again at once: a Connection changed, maybe on another replica.
func (p *Poller) Wake() {
	select {
	case p.wakes() <- struct{}{}:
	default:
	}
}

// pollWorker is the loop of one version of a Connection.
type pollWorker struct {
	version int64
	cancel  context.CancelFunc
	done    chan struct{}
}

// Run polls until ctx ends, which is when the Leader loses the lock or stops, and stops every loop before it returns:
// a loop starts for each Bridge Connection in poll mode, starts again when the Connection's version changes and stops
// when it leaves poll mode or is deleted. A failed read of the Connections stops them too and returns its error, so
// that the Leader task runs again.
func (p *Poller) Run(ctx context.Context) error {
	running := map[int64]*pollWorker{}
	stop := func(id int64) {
		w := running[id]
		w.cancel()
		<-w.done
		delete(running, id)
	}
	defer func() {
		for id := range running {
			stop(id)
		}
	}()
	every := p.Every
	if every <= 0 {
		every = ReadInterval
	}
	for {
		list, err := p.Source.PollingConnections(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		want := make(map[int64]Polled, len(list))
		for _, c := range list {
			want[c.Conn.ID] = c
		}
		for id, w := range running {
			if c, ok := want[id]; !ok || c.Conn.Version != w.version {
				stop(id)
			}
		}
		for id, c := range want {
			if _, ok := running[id]; ok {
				continue
			}
			wctx, cancel := context.WithCancel(ctx)
			w := &pollWorker{version: c.Conn.Version, cancel: cancel, done: make(chan struct{})}
			running[id] = w
			go func() {
				defer close(w.done)
				p.loop(wctx, c)
			}()
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-p.wakes():
		case <-t.C:
		}
		t.Stop()
	}
}

// wait is the wait of each call: Wait, at most PollWait.
func (p *Poller) wait() time.Duration {
	if p.Wait <= 0 || p.Wait > PollWait {
		return PollWait
	}
	return p.Wait
}

// loop polls the Connection c until ctx ends.
func (p *Poller) loop(ctx context.Context, c Polled) {
	cursor, wait := c.Cursor, p.wait()
	failures := 0
	for ctx.Err() == nil {
		callCtx, cancel := context.WithTimeout(ctx, wait+PollMargin)
		batch, r := p.pollEvents(callCtx, c.Conn, cursor, wait)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if !r.OK() {
			if cursor != nil && refusedCursor(r) {
				// The bridge no longer knows the cursor: poll again at once without one.
				cursor = nil
				if err := p.Source.StoreEventsCursor(ctx, c.Conn, nil); err != nil && ctx.Err() == nil {
					failures++
					p.failed(ctx, c.Conn, err.Error(), failures)
				}
				continue
			}
			failures++
			p.failed(ctx, c.Conn, string(r.Error), failures)
			continue
		}
		if !p.handle(ctx, c.Conn, batch) {
			if ctx.Err() != nil {
				return
			}
			// Events logged bridge_event_failed; the cursor stays, and the batch is polled again after a back-off.
			failures++
			p.pause(ctx, p.backoff(failures))
			continue
		}
		stored := true
		if cursor == nil || *cursor != batch.NextCursor {
			next := batch.NextCursor
			if err := p.Source.StoreEventsCursor(ctx, c.Conn, &next); err != nil {
				if ctx.Err() != nil {
					return
				}
				// The events are handled: this loop goes on from next, and a new Leader receives them again from the
				// stored cursor, once each by its id.
				failures++
				stored = false
				p.failed(ctx, c.Conn, err.Error(), failures)
			}
			cursor = &next
		}
		if stored {
			failures = 0
		}
		if len(batch.Events) == 0 {
			p.pause(ctx, emptyPause)
		}
	}
}

// refusedCursor reports whether the bridge refused a call as invalid: its answer is an error of the class invalid,
// which for pollEvents is a cursor it no longer knows.
func refusedCursor(r Result) bool {
	return r.Status >= 400 && r.Classification.Class == ClassInvalid
}

// pollEvents makes one pollEvents call through the client of c.
func (p *Poller) pollEvents(ctx context.Context, c Conn, cursor *string, wait time.Duration) (Batch, Result) {
	if p.poll != nil {
		return p.poll(ctx, c, cursor, wait)
	}
	return c.Client.PollEvents(ctx, cursor, wait)
}

// handle hands the events of batch to Events one at a time, in order; an entry that is no event is skipped with
// bridge_event_rejected, and the batch goes on. It reports whether every event was handled.
func (p *Poller) handle(ctx context.Context, c Conn, batch Batch) bool {
	for _, raw := range batch.Events {
		ev, err := DecodeEvent(raw)
		if err != nil {
			p.Log.Log(ctx, logging.BridgeEventRejected, logging.F("connection", c.PublicID),
				logging.F("reason", reasonNotAnEvent))
			continue
		}
		if err := p.Events.Handle(ctx, c, ev); err != nil {
			return false
		}
	}
	return true
}

// failed logs the failures-th failure in a row of the Connection c at the first and every tenth, and backs off.
func (p *Poller) failed(ctx context.Context, c Conn, text string, failures int) {
	if failures == 1 || failures%failureLogEvery == 0 {
		p.Log.Log(ctx, logging.BridgePollFailed, logging.F("connection", c.PublicID),
			logging.F("failures", failures), logging.F("error", text))
	}
	p.pause(ctx, p.backoff(failures))
}

// backoff is the wait after n failures in a row: exponential from 1 s up to 1 min, with equal jitter.
func (p *Poller) backoff(n int) time.Duration {
	d := failureBase
	for i := 1; i < n && d < failureCap; i++ {
		d *= 2
	}
	d = min(d, failureCap)
	jitter := p.jitter
	if jitter == nil {
		jitter = randomJitter
	}
	return d/2 + jitter(d/2)
}

// pause waits for d unless ctx ends first.
func (p *Poller) pause(ctx context.Context, d time.Duration) {
	if p.sleep != nil {
		p.sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// randomJitter returns a random duration in [0, d].
func randomJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return rand.N(d + 1) //nolint:gosec // G404: jitter spreads retries; it needs no cryptographic randomness
}
