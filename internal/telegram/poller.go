// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/logging"
)

const (
	// ReadInterval is how often the poller reads the Connections again when no change woke it.
	ReadInterval = 30 * time.Second
	// pollBudget bounds one poll with the retries of the background class: the long poll and the attempts after a
	// failure that answered nothing; then the poller logs the failure and backs off.
	pollBudget = PollTimeout + pollMargin + 30*time.Second
	// failureBase and failureCap bound the back-off after a failed poll; conflictBase and conflictCap after a 409.
	failureBase  = time.Second
	failureCap   = time.Minute
	conflictBase = 5 * time.Second
	conflictCap  = time.Minute
	// emptyPause is the wait after a poll that brought no update.
	emptyPause = time.Second
)

// Polled is a Telegram Connection in the long-polling mode: its client, its version, which restarts its poller when
// it changes, and its stored offset.
type Polled struct {
	Conn
	Version int64
	Offset  *int64
}

// Source lists the Telegram Connections of the Organization that are not deleted and are in the long-polling mode;
// *connections.Service implements it.
type Source interface {
	Polling(ctx context.Context) ([]Polled, error)
}

// Poller is the long polling of an Organization's Telegram Connections (C-14.FR-1), the Leader task telegram_polling
// (C-02.FR-10): one poller per Connection in the long-polling mode calls getUpdates with the stored offset, the
// long-poll timeout and AllowedUpdates, hands the updates to the router, which stores the offset after each, so that a
// new Leader resumes where the old one stopped. A 409 from a second poller or a set webhook only backs off with jitter
// and logs telegram_poll_conflict (F-018); any other failure logs telegram_poll_failed and backs off. Neither is a
// delivery outcome or makes anything Broken. Running it on two replicas at once is safe: Telegram answers one of the
// pollers 409, and the router handles an update once.
type Poller struct {
	Source Source
	Router *Router
	Log    *logging.Logger
	// Every is how often the Connections are read again without a wake; 0 is ReadInterval.
	Every time.Duration

	// sleep waits for d unless ctx ends first and jitter spreads a back-off; tests replace both.
	sleep  func(ctx context.Context, d time.Duration) bool
	jitter func(d time.Duration) time.Duration

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

// worker is the poller of one Connection version.
type worker struct {
	version int64
	cancel  context.CancelFunc
	done    chan struct{}
}

// Run polls until ctx ends, which is when the Leader loses the lock or stops, and stops every poller before it
// returns. A failed read of the Connections stops them too and returns its error, so that the Leader task runs again.
func (p *Poller) Run(ctx context.Context) error {
	running := map[int64]*worker{}
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
		list, err := p.Source.Polling(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		want := map[int64]Polled{}
		for _, c := range list {
			want[c.ID] = c
		}
		for id, w := range running {
			if c, ok := want[id]; !ok || c.Version != w.version {
				stop(id)
			}
		}
		for id, c := range want {
			if _, ok := running[id]; ok {
				continue
			}
			wctx, cancel := context.WithCancel(ctx)
			w := &worker{version: c.Version, cancel: cancel, done: make(chan struct{})}
			running[id] = w
			go func() {
				defer close(w.done)
				p.poll(wctx, c)
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

// poll is the loop of one Connection until ctx ends.
func (p *Poller) poll(ctx context.Context, c Polled) {
	offset := c.Offset
	var failures, conflicts int
	for ctx.Err() == nil {
		callCtx, cancel := context.WithTimeout(ctx, pollBudget)
		updates, r := c.Client.GetUpdates(callCtx, offset, PollTimeout)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if r.Conflict() {
			conflicts++
			failures = 0
			d := p.backoff(conflicts, conflictBase, conflictCap)
			p.Log.Log(ctx, logging.TelegramPollConflict, logging.F("connection", c.PublicID),
				logging.F("backoff_ms", d.Milliseconds()))
			p.wait(ctx, d)
			continue
		}
		if !r.OK() {
			failures++
			p.failed(ctx, c, string(r.Outcome.Error), failures)
			continue
		}
		conflicts = 0
		if len(updates) == 0 {
			// A Bot API holds an empty long poll for its timeout; one that answers at once must not make a tight loop.
			p.wait(ctx, emptyPause)
		}
		routed := true
		for _, u := range updates {
			if _, err := p.Router.Route(ctx, c.Conn, u); err != nil {
				if ctx.Err() != nil {
					return
				}
				failures++
				p.failed(ctx, c, err.Error(), failures)
				routed = false
				break
			}
			next := u.UpdateID + 1
			offset = &next
		}
		if routed {
			failures = 0
		}
	}
}

// failed logs a failed poll and backs off.
func (p *Poller) failed(ctx context.Context, c Polled, text string, failures int) {
	p.Log.Log(ctx, logging.TelegramPollFailed, logging.F("connection", c.PublicID), logging.F("error", text))
	p.wait(ctx, p.backoff(failures, failureBase, failureCap))
}

// backoff is the wait after n failures in a row: exponential from base up to limit, with equal jitter.
func (p *Poller) backoff(n int, base, limit time.Duration) time.Duration {
	d := base
	for i := 1; i < n && d < limit; i++ {
		d *= 2
	}
	d = min(d, limit)
	jitter := p.jitter
	if jitter == nil {
		jitter = randomJitter
	}
	return d/2 + jitter(d/2)
}

func (p *Poller) wait(ctx context.Context, d time.Duration) {
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
