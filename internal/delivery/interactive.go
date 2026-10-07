// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/outbound"
)

// Interactive is the interactive path (C-11.FR-2, ADR-0005): the one way a call that a person waits for — an answer to
// a button press, an ephemeral or account-linking message, a check or a test started by a person — reaches a
// messenger. It takes a token from the same limiters as the delivery worker, polling them during its budget, and
// since a delivery without a token waits TokenMargin past the time its tokens are due, it takes the next token ahead
// of waiting deliveries. It never queues and never writes deliveries or Thread replies; it calls in the interactive
// client class. The Broken probe never uses it (S-035).
type Interactive struct {
	OrgID int64
	Store *Store
	// Clocks: the limiters follow the business clock, the budget the real clock.
	Clocks clock.Clocks
	// Budget is delivery.interactive_budget; zero is InteractiveBudget.
	Budget time.Duration
	// Sleep waits for d unless ctx ends first, and reports whether the whole wait passed; nil sleeps on the real
	// clock.
	Sleep func(ctx context.Context, d time.Duration) bool
}

// pollStep is the shortest wait between two polls of the limiters.
const pollStep = 10 * time.Millisecond

// Subject is what an interactive call is limited by: a Destination with its Connection, or a Connection alone.
type Subject struct {
	Destination *Destination
	Connection  *int64
}

// LimitedError is an interactive call that found no limiter token within the budget: nothing was sent, and a token
// is due after RetryAfter. The API answers it with 503 interactive-budget-exhausted and Retry-After, a Destination
// test records the error class limited (S-047).
type LimitedError struct {
	RetryAfter time.Duration
}

func (e *LimitedError) Error() string {
	return fmt.Sprintf("no limiter token was free within the interactive budget; retry after %s", e.RetryAfter)
}

// Seconds is the wait in whole seconds, rounded up, at least 1, for Retry-After.
func (e *LimitedError) Seconds() int {
	return max(int(math.Ceil(e.RetryAfter.Seconds())), 1)
}

// Do makes call once a token of s is taken, in the interactive client class. When no token frees within the budget it
// returns a *LimitedError and calls nothing. A RetryAfter the call answers holds the bucket of its scope, as for the
// worker.
func (i *Interactive) Do(ctx context.Context, s Subject, call func(ctx context.Context, c Call) Outcome) (Outcome,
	error) {
	budget := i.Budget
	if budget <= 0 {
		budget = InteractiveBudget
	}
	sleep := i.Sleep
	if sleep == nil {
		sleep = sleepReal
	}
	if s.Destination == nil && s.Connection == nil {
		return Outcome{}, errors.New("an interactive call needs a destination or a connection")
	}
	sub := subject{connection: s.Connection}
	c := Call{Class: outbound.ClassInteractive}
	if s.Destination != nil {
		c.Destination = *s.Destination
		sub = subjectOf(*s.Destination)
	}
	end := i.Clocks.Real.Now().Add(budget)
	for {
		var t taken
		err := i.Store.inTx(ctx, func(q queries) error {
			var err error
			t, err = takeTokens(ctx, q, i.OrgID, sub, i.Clocks.Business.Now().UTC())
			return err
		})
		if err != nil {
			return Outcome{}, err
		}
		if t.ok {
			break
		}
		wait := max(t.due.Sub(i.Clocks.Business.Now()), 0)
		left := end.Sub(i.Clocks.Real.Now())
		if left <= 0 {
			return Outcome{}, &LimitedError{RetryAfter: wait}
		}
		if !sleep(ctx, min(max(wait, pollStep), left)) {
			return Outcome{}, ctx.Err()
		}
	}
	out := call(ctx, c)
	if out.Kind != OutcomeRetryAfter || (s.Destination == nil && out.Scope != ScopeConnection) {
		return out, nil
	}
	d := c.Destination
	if s.Destination == nil {
		d = Destination{Connection: s.Connection}
	}
	err := i.Store.inTx(ctx, func(q queries) error {
		return holdBucket(ctx, q, i.OrgID, d, out, i.Clocks.Business.Now().UTC().Add(out.RetryAfter))
	})
	return out, err
}

// sleepReal waits for d unless ctx ends first.
func sleepReal(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
