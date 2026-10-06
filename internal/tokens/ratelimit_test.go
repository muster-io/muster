// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package tokens

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
)

// TestLimiter is S-016 step 6 and C-04.AC-3: a token gets the burst at once and then RateLimit a second; a request
// over it is refused with the wait until the next one, rounded up to whole seconds for Retry-After, while another
// token is unaffected.
func TestLimiter(t *testing.T) {
	realClock := clock.NewManual(t0)
	l := NewLimiter(realClock)
	for i := range RateBurst {
		if err := l.Allow(1); err != nil {
			t.Fatalf("request %d of the burst: %v", i, err)
		}
	}
	err := l.Allow(1)
	rl, ok := errors.AsType[*RateLimitedError](err)
	if !ok || rl.RetryAfter != time.Second/RateLimit || rl.Seconds() != 1 || rl.Error() == "" {
		t.Fatalf("over the burst: %v", err)
	}
	if err := l.Allow(2); err != nil {
		t.Errorf("another token: %v", err)
	}
	// The refill: one request each 50 ms.
	realClock.Advance(time.Second / RateLimit)
	if err := l.Allow(1); err != nil {
		t.Errorf("after 50 ms: %v", err)
	}
	if err := l.Allow(1); err == nil {
		t.Error("a second request after 50 ms")
	}
	// A second refills RateLimit requests, never more than the burst.
	realClock.Advance(time.Second)
	for i := range RateLimit {
		if err := l.Allow(1); err != nil {
			t.Fatalf("request %d after a second: %v", i, err)
		}
	}
	if err := l.Allow(1); err == nil {
		t.Error("more than RateLimit requests after a second")
	}
	realClock.Advance(time.Hour)
	for range RateBurst {
		_ = l.Allow(1)
	}
	if err := l.Allow(1); err == nil {
		t.Error("more than the burst after an hour")
	}
}

// TestRetryAfterSeconds: Retry-After is the wait rounded up, at least one second.
func TestRetryAfterSeconds(t *testing.T) {
	for d, want := range map[time.Duration]int{0: 1, time.Millisecond: 1, time.Second: 1,
		time.Second + time.Millisecond: 2, 3 * time.Second: 3} {
		if got := (&RateLimitedError{RetryAfter: d}).Seconds(); got != want {
			t.Errorf("%v = %d, want %d", d, got, want)
		}
	}
}

// TestLimiterSweep: buckets that refilled completely are forgotten once a minute, which changes nothing for their
// tokens.
func TestLimiterSweep(t *testing.T) {
	realClock := clock.NewManual(t0)
	l := newLimiter(realClock, 1, 2)
	_ = l.Allow(1)
	_ = l.Allow(1)
	_ = l.Allow(2)
	realClock.Advance(sweepInterval)
	_ = l.Allow(3)
	if len(l.buckets) != 1 {
		t.Errorf("%d buckets after the sweep", len(l.buckets))
	}
	if err := l.Allow(1); err != nil {
		t.Errorf("a forgotten token: %v", err)
	}
}

// TestLimiterConcurrent: the buckets are safe for concurrent requests, and exactly the burst passes at once.
func TestLimiterConcurrent(t *testing.T) {
	l := NewLimiter(clock.NewManual(t0))
	var mu sync.Mutex
	passed := 0
	var wg sync.WaitGroup
	for range 4 * RateBurst {
		wg.Go(func() {
			if l.Allow(9) == nil {
				mu.Lock()
				passed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if passed != RateBurst {
		t.Errorf("%d requests passed, want %d", passed, RateBurst)
	}
}
