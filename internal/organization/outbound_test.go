// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/organization/dbgen"
	"github.com/muster-io/muster/internal/outbound"
)

// countingStore counts the reads of the outbound address policy.
type countingStore struct {
	*fakeStore
	reads int
}

func (s *countingStore) GetOutboundPolicy(ctx context.Context, orgID int64) (dbgen.GetOutboundPolicyRow, error) {
	s.reads++
	return s.fakeStore.GetOutboundPolicy(ctx, orgID)
}

// TestOutboundPoliciesCache: the policy is read once, served from the cache for less than 10 seconds of the real
// clock, then read again, so that an edit takes effect within 10 seconds.
func TestOutboundPoliciesCache(t *testing.T) {
	s := &countingStore{fakeStore: &fakeStore{policy: &dbgen.CreateOutboundPolicyParams{OrgID: 7, Policy: "standard",
		Allowed: []string{}, Denied: []string{}}}}
	m := clock.NewManual(now)
	o := NewOutboundPolicies(s, 7, m)
	read := func() outbound.Policy {
		t.Helper()
		p, err := o.OutboundPolicy(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := read(); p.Mode != outbound.ModeStandard || len(p.Allowed) != 0 || s.reads != 1 {
		t.Fatalf("policy %+v after %d reads", p, s.reads)
	}
	s.policy.Policy, s.policy.Allowed = "strict", []string{"10.0.0.0/8", "*.corp.example"}
	m.Advance(OutboundPolicyMaxAge - time.Millisecond)
	if p := read(); p.Mode != outbound.ModeStandard || s.reads != 1 {
		t.Fatalf("cached policy %+v after %d reads", p, s.reads)
	}
	m.Advance(time.Millisecond)
	if p := read(); p.Mode != outbound.ModeStrict || len(p.Allowed) != 2 || s.reads != 2 {
		t.Fatalf("refreshed policy %+v after %d reads", p, s.reads)
	}
	// A clock that went back does not keep a policy read in its future.
	m.Advance(-time.Hour)
	if read(); s.reads != 3 {
		t.Fatalf("%d reads after the clock went back", s.reads)
	}
}

// blockingStore holds each read until released.
type blockingStore struct {
	*countingStore
	started chan struct{}
	release chan struct{}
}

func (s *blockingStore) GetOutboundPolicy(ctx context.Context, orgID int64) (dbgen.GetOutboundPolicyRow, error) {
	s.started <- struct{}{}
	<-s.release
	return s.countingStore.GetOutboundPolicy(ctx, orgID)
}

// TestOutboundPoliciesOneRead: callers that arrive during a read wait for its result instead of reading too, and a
// waiter whose context ends stops waiting.
func TestOutboundPoliciesOneRead(t *testing.T) {
	s := &blockingStore{
		countingStore: &countingStore{fakeStore: &fakeStore{policy: &dbgen.CreateOutboundPolicyParams{OrgID: 7,
			Policy: "strict", Allowed: []string{}, Denied: []string{}}}},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	o := NewOutboundPolicies(s, 7, clock.NewManual(now))
	type result struct {
		p   outbound.Policy
		err error
	}
	results := make(chan result, 2)
	read := func(ctx context.Context) {
		p, err := o.OutboundPolicy(ctx)
		results <- result{p, err}
	}
	go read(context.Background())
	<-s.started
	go read(context.Background())
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.OutboundPolicy(ended); !errors.Is(err, context.Canceled) {
		t.Fatalf("ended waiter: %v", err)
	}
	close(s.release)
	for range 2 {
		if r := <-results; r.err != nil || r.p.Mode != outbound.ModeStrict {
			t.Fatalf("result %+v", r)
		}
	}
	if s.reads != 1 {
		t.Fatalf("%d reads", s.reads)
	}
}

func TestOutboundPoliciesErrors(t *testing.T) {
	s := &fakeStore{getPolicyErr: errors.New("connection refused")}
	o := NewOutboundPolicies(s, 7, clock.NewManual(now))
	if _, err := o.OutboundPolicy(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "read the outbound address policy: connection refused") {
		t.Fatalf("read error %v", err)
	}
	s.getPolicyErr = nil
	s.policy = &dbgen.CreateOutboundPolicyParams{OrgID: 7, Policy: "standard", Allowed: []string{"not a network"}}
	if _, err := o.OutboundPolicy(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "the stored outbound address policy: allowed entry 0") {
		t.Fatalf("parse error %v", err)
	}
	s.policy.Allowed = nil
	if _, err := o.OutboundPolicy(context.Background()); err != nil {
		t.Fatalf("after the errors: %v", err)
	}
}
