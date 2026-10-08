// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package deliverytest_test

import (
	"context"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/outbound"
)

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func unlimited() *delivery.Interactive {
	return deliverytest.Unlimited(1, clock.Clocks{Business: clock.NewManual(t0), Real: clock.NewManual(t0)})
}

// TestUnlimitedGrants: the interactive path without a database grants a token at once and makes the call in the
// interactive client class, for a Connection and for a Destination.
func TestUnlimitedGrants(t *testing.T) {
	in := unlimited()
	conn := int64(4)
	for _, s := range []delivery.Subject{{Connection: &conn},
		{Destination: &delivery.Destination{ID: 9, Type: delivery.TypeMattermost, Connection: &conn}}} {
		calls := 0
		out, err := in.Do(t.Context(), s, delivery.ReadOp(func(_ context.Context, c delivery.Call) delivery.Outcome {
			calls++
			if c.Class != outbound.ClassInteractive {
				t.Errorf("class %s", c.Class)
			}
			return delivery.Outcome{Kind: delivery.OutcomeOK}
		}))
		if err != nil || out.Kind != delivery.OutcomeOK || calls != 1 {
			t.Errorf("%+v: %+v, %v, %d calls", s, out, err, calls)
		}
	}
}

// TestUnlimitedRetryAfter: a RetryAfter the call answers holds its bucket without an error, for the scope of the
// Connection and of the Destination.
func TestUnlimitedRetryAfter(t *testing.T) {
	in := unlimited()
	conn := int64(4)
	dest := delivery.Destination{ID: 9, Type: delivery.TypeMattermost, Connection: &conn}
	for _, tc := range []struct {
		s     delivery.Subject
		scope delivery.Scope
	}{
		{delivery.Subject{Connection: &conn}, delivery.ScopeConnection},
		{delivery.Subject{Destination: &dest}, delivery.ScopeConnection},
		{delivery.Subject{Destination: &dest}, delivery.ScopeDestination},
	} {
		out, err := in.Do(t.Context(), tc.s, delivery.ReadOp(func(context.Context, delivery.Call) delivery.Outcome {
			return delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: 2 * time.Second, Scope: tc.scope}
		}))
		if err != nil || out.Kind != delivery.OutcomeRetryAfter || out.RetryAfter != 2*time.Second {
			t.Errorf("%s: %+v, %v", tc.scope, out, err)
		}
	}
	if _, err := in.Do(t.Context(), delivery.Subject{}, delivery.ReadOp(func(context.Context,
		delivery.Call) delivery.Outcome {
		t.Error("called without a subject")
		return delivery.Outcome{}
	})); err == nil {
		t.Error("a call without a subject")
	}
}
