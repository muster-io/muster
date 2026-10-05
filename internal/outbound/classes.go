// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"context"
	"math/rand/v2"
	"time"
)

// Class is a client class (ADR-0015): it follows who waits for the result and sets the retry policy.
type Class string

const (
	// ClassDelivery makes one attempt; the classified outcome goes back to the caller's delivery row.
	ClassDelivery Class = "delivery"
	// ClassInteractive makes one attempt within the budget the caller's context gives.
	ClassInteractive Class = "interactive"
	// ClassBackground retries with exponential backoff, jitter and a cap until its context ends.
	ClassBackground Class = "background"
	// ClassHeartbeat makes one attempt with a short timeout; the next tick is the retry.
	ClassHeartbeat Class = "heartbeat"
)

// Classes are the client classes, the closed set of the metric label client.
var Classes = []Class{ClassDelivery, ClassInteractive, ClassBackground, ClassHeartbeat}

const (
	// backoffBase and backoffCap bound the waits of the background class between attempts.
	backoffBase = time.Second
	backoffCap  = time.Minute
)

// retries says whether the class makes another attempt after outcome. Only the background class retries, and only
// outcomes that a later attempt can change; a blocked request, a redirect and a fatal or unknown answer never are.
func (c Class) retries(o Outcome) bool {
	return c == ClassBackground && (o == OutcomeTransient || o == OutcomeRetryAfter)
}

// backoff is the wait before attempt n+1 after n failed attempts: exponential from backoffBase up to backoffCap,
// with equal jitter, so that it lies between half the exponential value and the whole of it.
func backoff(n int, jitter func(time.Duration) time.Duration) time.Duration {
	d := backoffBase
	for i := 1; i < n && d < backoffCap; i++ {
		d *= 2
	}
	d = min(d, backoffCap)
	return d/2 + jitter(d/2)
}

// randomJitter returns a random duration in [0, d].
func randomJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return rand.N(d + 1) //nolint:gosec // G404: jitter spreads retries; it needs no cryptographic randomness
}

// sleepContext waits for d or until ctx ends, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
