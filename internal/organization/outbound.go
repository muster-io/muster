// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/outbound"
)

// OutboundPolicyMaxAge is how long a replica keeps the outbound address policy it read before reading it again.
const OutboundPolicyMaxAge = 10 * time.Second

// OutboundPolicies is the Organization's outbound address policy as the outbound package reads it: read from
// outbound_policies and cached per replica for at most OutboundPolicyMaxAge of the real clock.
type OutboundPolicies struct {
	store Store
	orgID int64
	clock clock.Clock

	mu     sync.Mutex
	policy outbound.Policy
	readAt time.Time
	cached bool
	// read is the read in progress, which other callers wait for instead of reading too.
	read *policyRead
}

type policyRead struct {
	done   chan struct{}
	policy outbound.Policy
	err    error
}

// NewOutboundPolicies reads the policy of the Organization orgID through s; c is the real clock.
func NewOutboundPolicies(s Store, orgID int64, c clock.Clock) *OutboundPolicies {
	return &OutboundPolicies{store: s, orgID: orgID, clock: c}
}

// OutboundPolicy returns the cached policy, or reads it again when the cached one is older than
// OutboundPolicyMaxAge. One caller reads while the others wait for its result, each within its own context, outside
// the lock. An error leaves the cache empty, so the requests it serves fail and the next one reads again.
func (o *OutboundPolicies) OutboundPolicy(ctx context.Context) (outbound.Policy, error) {
	o.mu.Lock()
	now := o.clock.Now()
	if o.cached && now.Sub(o.readAt) < OutboundPolicyMaxAge && !now.Before(o.readAt) {
		p := o.policy
		o.mu.Unlock()
		return p, nil
	}
	if r := o.read; r != nil {
		o.mu.Unlock()
		select {
		case <-r.done:
			return r.policy, r.err
		case <-ctx.Done():
			return outbound.Policy{}, ctx.Err()
		}
	}
	r := &policyRead{done: make(chan struct{})}
	o.read, o.cached = r, false
	o.mu.Unlock()

	r.policy, r.err = o.load(ctx)
	o.mu.Lock()
	if r.err == nil {
		o.policy, o.readAt, o.cached = r.policy, now, true
	}
	o.read = nil
	o.mu.Unlock()
	close(r.done)
	return r.policy, r.err
}

func (o *OutboundPolicies) load(ctx context.Context) (outbound.Policy, error) {
	row, err := o.store.GetOutboundPolicy(ctx, o.orgID)
	if err != nil {
		return outbound.Policy{}, fmt.Errorf("read the outbound address policy: %w", err)
	}
	p, err := outbound.ParsePolicy(row.Policy, row.Allowed, row.Denied)
	if err != nil {
		return outbound.Policy{}, fmt.Errorf("the stored outbound address policy: %w", err)
	}
	return p, nil
}
