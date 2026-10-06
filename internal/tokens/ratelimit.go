// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package tokens

import (
	"fmt"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
)

// api.rate_limit (C-04.FR-5, P-09): a token bucket per token on each replica that refills RateLimit requests a second
// and holds at most RateBurst; with several replicas a token can reach the limit on each.
const (
	RateLimit = 20
	RateBurst = 100
	// sweepInterval is how often at most the buckets that refilled completely are forgotten.
	sweepInterval = time.Minute
)

// RateLimitedError refuses a request of a token whose bucket is empty; RetryAfter is when the next one is let through.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("the token exceeded the rate limit: retry after %d s", e.Seconds())
}

// Seconds is RetryAfter rounded up to whole seconds, at least one, as the Retry-After header carries it.
func (e *RateLimitedError) Seconds() int {
	return max(1, int((e.RetryAfter+time.Second-1)/time.Second))
}

// bucket is the token bucket of one API token: tokens left at the time it was last counted.
type bucket struct {
	tokens float64
	at     time.Time
}

// Limiter is the token buckets of the API tokens on this replica, on the real clock: a development clock advance
// never refills them. Web sessions and ingestion have none.
type Limiter struct {
	realClock clock.Clock
	rate      float64
	burst     float64

	mu      sync.Mutex
	buckets map[int64]*bucket
	swept   time.Time
}

// NewLimiter returns the Limiter of api.rate_limit.
func NewLimiter(realClock clock.Clock) *Limiter {
	return newLimiter(realClock, RateLimit, RateBurst)
}

func newLimiter(realClock clock.Clock, rate, burst float64) *Limiter {
	return &Limiter{realClock: realClock, rate: rate, burst: burst, buckets: map[int64]*bucket{}, swept: realClock.Now()}
}

// Allow takes one request from the bucket of the token id; an empty bucket is a RateLimitedError with the wait until
// the next request is let through.
func (l *Limiter) Allow(id int64) error {
	now := l.realClock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	b, ok := l.buckets[id]
	if !ok {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[id] = b
	}
	if elapsed := now.Sub(b.at); elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+elapsed.Seconds()*l.rate)
	}
	b.at = now
	if b.tokens < 1 {
		return &RateLimitedError{RetryAfter: time.Duration((1 - b.tokens) / l.rate * float64(time.Second))}
	}
	b.tokens--
	return nil
}

// sweep forgets, once a sweepInterval, the buckets that have refilled completely since they were last counted: a new
// bucket starts full, so nothing changes for their tokens, and the map holds only the tokens in recent use.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < sweepInterval {
		return
	}
	l.swept = now
	full := time.Duration(l.burst / l.rate * float64(time.Second))
	for id, b := range l.buckets {
		if now.Sub(b.at) >= full {
			delete(l.buckets, id)
		}
	}
}
