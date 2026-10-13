// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
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

// blockingStore holds each read until released; released reads run one at a time.
type blockingStore struct {
	*countingStore
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
}

func (s *blockingStore) GetOutboundPolicy(ctx context.Context, orgID int64) (dbgen.GetOutboundPolicyRow, error) {
	s.started <- struct{}{}
	<-s.release
	s.mu.Lock()
	defer s.mu.Unlock()
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

// TestOutboundPoliciesInvalidate is C-20.FR-3 on one replica: Invalidate drops the cached policy at once, without
// waiting for its age, and closes the idle outbound connections once: the read after it finds another policy but does
// not close them again.
func TestOutboundPoliciesInvalidate(t *testing.T) {
	s := &countingStore{fakeStore: &fakeStore{policy: &dbgen.CreateOutboundPolicyParams{OrgID: 7, Policy: "standard",
		Allowed: []string{"127.0.0.0/8"}, Denied: []string{}}}}
	o := NewOutboundPolicies(s, 7, clock.NewManual(now))
	closed := 0
	o.closeIdle = func() { closed++ }
	if p, err := o.OutboundPolicy(t.Context()); err != nil || len(p.Allowed) != 1 || s.reads != 1 || closed != 0 {
		t.Fatalf("first read %+v, %v, %d reads, %d closes", p, err, s.reads, closed)
	}
	s.policy.Allowed = []string{}
	o.Invalidate()
	if p, err := o.OutboundPolicy(t.Context()); err != nil || len(p.Allowed) != 0 || s.reads != 2 {
		t.Fatalf("after Invalidate %+v, %v, %d reads", p, err, s.reads)
	}
	if closed != 1 {
		t.Errorf("Invalidate and the read after it closed the idle connections %d times, want 1", closed)
	}
	if NewOutboundPolicies(s, 7, clock.NewManual(now)).closeIdle == nil {
		t.Error("the default closes nothing")
	}
}

// TestOutboundPoliciesInvalidateDuringRead: a caller after Invalidate does not wait for a read that started before it,
// whose result is not cached.
func TestOutboundPoliciesInvalidateDuringRead(t *testing.T) {
	s := &blockingStore{
		countingStore: &countingStore{fakeStore: &fakeStore{policy: &dbgen.CreateOutboundPolicyParams{OrgID: 7,
			Policy: "standard", Allowed: []string{"127.0.0.0/8"}, Denied: []string{}}}},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	o := NewOutboundPolicies(s, 7, clock.NewManual(now))
	o.closeIdle = func() {}
	first := make(chan outbound.Policy, 1)
	go func() {
		p, _ := o.OutboundPolicy(context.Background())
		first <- p
	}()
	<-s.started
	o.Invalidate()
	second := make(chan outbound.Policy, 1)
	go func() {
		p, _ := o.OutboundPolicy(context.Background())
		second <- p
	}()
	<-s.started
	// Both reads see the row as it is when they are released; the first one is not cached.
	s.policy.Allowed = []string{}
	s.release <- struct{}{}
	s.release <- struct{}{}
	<-first
	if p := <-second; len(p.Allowed) != 0 {
		t.Errorf("the read after Invalidate = %+v", p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reads != 2 {
		t.Errorf("%d reads", s.reads)
	}
	if p, err := o.OutboundPolicy(t.Context()); err != nil || len(p.Allowed) != 0 || s.reads != 2 {
		t.Errorf("cached %+v, %v after %d reads", p, err, s.reads)
	}
}

// TestOutboundPoliciesRereadDiffers: a re-read after the age that finds another policy — a missed notification —
// closes the idle connections; the same policy and the first read close nothing.
func TestOutboundPoliciesRereadDiffers(t *testing.T) {
	s := &countingStore{fakeStore: &fakeStore{policy: &dbgen.CreateOutboundPolicyParams{OrgID: 7, Policy: "standard",
		Allowed: []string{"127.0.0.0/8"}, Denied: []string{}}}}
	m := clock.NewManual(now)
	o := NewOutboundPolicies(s, 7, m)
	closed := 0
	o.closeIdle = func() { closed++ }
	read := func() {
		t.Helper()
		if _, err := o.OutboundPolicy(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	read()
	m.Advance(OutboundPolicyMaxAge)
	read()
	if closed != 0 || s.reads != 2 {
		t.Fatalf("the same policy: %d closes, %d reads", closed, s.reads)
	}
	for _, change := range []func(){
		func() { s.policy.Allowed = []string{} },
		func() { s.policy.Denied = []string{"10.0.0.0/8"} },
		func() { s.policy.Policy = "strict" },
	} {
		before := closed
		change()
		m.Advance(OutboundPolicyMaxAge)
		read()
		if closed != before+1 {
			t.Errorf("a changed policy closed %d times", closed-before)
		}
	}
}

func outboundService(t *testing.T) (*Service, *fakeSettings, *bytes.Buffer) {
	t.Helper()
	s := newFakeSettings(t)
	var log bytes.Buffer
	svc := NewService(7, s, audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), clock.NewManual(t0)),
		clock.NewManual(t0)).WithLog(logging.New(&log, logging.LevelInfo))
	return svc, s, &log
}

// TestOutboundPolicyRead: the policy with its version and the rules that always apply.
func TestOutboundPolicyRead(t *testing.T) {
	svc, s, _ := outboundService(t)
	s.policy.Denied = nil
	p, err := svc.OutboundPolicy(t.Context())
	if err != nil || p.Mode != OutboundStandard || !slices.Equal(p.Allowed, []string{"127.0.0.0/8"}) ||
		p.Denied == nil || p.Version != 1 || !slices.Equal(p.AlwaysBlocked, outbound.AlwaysBlocked()) ||
		len(p.AlwaysBlocked) == 0 {
		t.Errorf("OutboundPolicy = %+v, %v", p, err)
	}
	s.fail["GetOutboundPolicySettings"] = errors.New("db down")
	if _, err := svc.OutboundPolicy(t.Context()); err == nil {
		t.Error("a failed read: no error")
	}
}

// TestUpdateOutboundPolicy is C-20.FR-3 and C-02.FR-21: an update with If-Match replaces the policy and its lists
// with trimmed entries, moves the version, is recorded as outbound_policy.updated with its diff, sends the
// organization hint and NOTIFY muster_outbound_policy in its transaction, runs the local hook after the commit and is
// logged; a stale version changes nothing, and an update that changes nothing writes nothing.
func TestUpdateOutboundPolicy(t *testing.T) {
	svc, s, log := outboundService(t)
	hooks := 0
	svc.WithOutboundPolicyChanged(func() { hooks++ })
	stale := int64(5)
	in := OutboundPolicyInput{Mode: OutboundStandard, Allowed: []string{}, Denied: []string{" 10.0.0.0/8 "}}
	if _, err := svc.UpdateOutboundPolicy(t.Context(), actor, audit.TransportAPI, netip.Addr{}, &stale,
		in); !errors.Is(err, ErrVersionMismatch) || len(s.policyUps) != 0 || hooks != 0 {
		t.Errorf("a stale version: %v", err)
	}
	v := int64(1)
	p, err := svc.UpdateOutboundPolicy(t.Context(), actor, audit.TransportAPI, netip.Addr{}, &v, in)
	if err != nil || p.Version != 2 || len(p.Allowed) != 0 || !slices.Equal(p.Denied, []string{"10.0.0.0/8"}) ||
		len(p.AlwaysBlocked) == 0 {
		t.Fatalf("Update = %+v, %v", p, err)
	}
	if u := s.policyUps[0]; u.OrgID != 7 || u.Policy != "standard" || !u.UpdatedAt.Equal(t0) {
		t.Errorf("update %+v", u)
	}
	if len(s.audit) != 1 || s.audit[0].Action != ActionOutboundPolicyUpdated ||
		s.audit[0].ResourceType.String != ResourceOutboundPolicy || string(s.audit[0].Diff) !=
		`[{"pointer":"/allowed","before":["127.0.0.0/8"],"after":[]},{"pointer":"/denied","before":[],"after":["10.0.0.0/8"]}]` {
		t.Errorf("audit %+v", s.audit)
	}
	if !slices.Equal(s.hints, []db.Hint{{OrgID: 7, Type: HintOrganization}}) || !slices.Equal(s.notified, []int64{7}) ||
		hooks != 1 {
		t.Errorf("hints %v, notified %v, hooks %d", s.hints, s.notified, hooks)
	}
	if !strings.Contains(log.String(), `"event":"outbound_policy_updated"`) {
		t.Errorf("log %s", log.String())
	}
	log.Reset()
	if p, err := svc.UpdateOutboundPolicy(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil,
		in); err != nil || p.Version != 2 || len(s.policyUps) != 1 || hooks != 1 || log.Len() != 0 {
		t.Errorf("no change = %+v, %v", p, err)
	}
	in.Mode = OutboundStrict
	if p, err := svc.UpdateOutboundPolicy(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil,
		in); err != nil || p.Mode != OutboundStrict || string(s.audit[1].Diff) !=
		`[{"pointer":"/policy","before":"standard","after":"strict"}]` {
		t.Errorf("strict = %+v, %v", p, err)
	}
	for _, method := range []string{"InTx", "LockOutboundPolicy", "GetOutboundPolicySettings", "UpdateOutboundPolicy",
		"InsertAuditEntry", "Notify", "NotifyOutboundPolicy"} {
		svc, s, _ := outboundService(t)
		s.fail[method] = errors.New("db down")
		if _, err := svc.UpdateOutboundPolicy(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil,
			OutboundPolicyInput{Mode: OutboundStrict}); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
}

// TestUpdateOutboundPolicyValidation: an entry must parse as the policy parses it and appear once per list, at its
// pointer; nothing is written.
func TestUpdateOutboundPolicyValidation(t *testing.T) {
	for _, c := range []struct {
		pointer, code string
		in            OutboundPolicyInput
	}{
		{"/policy", CodeInvalidFormat, OutboundPolicyInput{Mode: "open"}},
		{"/allowed/1", CodeInvalidFormat, OutboundPolicyInput{Mode: OutboundStandard,
			Allowed: []string{"127.0.0.0/8", "10.0.0.0/33"}}},
		{"/allowed/0", CodeInvalidFormat, OutboundPolicyInput{Mode: OutboundStandard, Allowed: []string{"bad name!"}}},
		{"/allowed/0", CodeInvalidFormat, OutboundPolicyInput{Mode: OutboundStrict, Allowed: []string{" "}}},
		{"/denied/2", CodeDuplicate, OutboundPolicyInput{Mode: OutboundStandard,
			Denied: []string{"*.corp.example", "10.0.0.0/8", "*.CORP.example."}}},
		{"/allowed/1", CodeDuplicate, OutboundPolicyInput{Mode: OutboundStandard,
			Allowed: []string{"chat.example.org", " chat.example.org"}}},
		{"/allowed/1", CodeDuplicate, OutboundPolicyInput{Mode: OutboundStandard,
			Allowed: []string{"10.0.0.0/8", "10.0.0.1/8"}}},
		{"/denied/1", CodeDuplicate, OutboundPolicyInput{Mode: OutboundStandard, Denied: []string{"::1", "0:0::1"}}},
		{"/denied/1", CodeDuplicate, OutboundPolicyInput{Mode: OutboundStandard,
			Denied: []string{"10.0.0.1", "::ffff:10.0.0.1"}}},
		{"/allowed/2", CodeDuplicate, OutboundPolicyInput{Mode: OutboundStandard,
			Allowed: []string{"192.0.2.7", "2001:db8::/32", "192.0.2.7/32"}}},
		{"/allowed/1", CodeDuplicate, OutboundPolicyInput{Mode: OutboundStandard,
			Allowed: []string{"2001:DB8::/32", "2001:db8:0::/32"}}},
	} {
		svc, s, _ := outboundService(t)
		_, err := svc.UpdateOutboundPolicy(t.Context(), actor, audit.TransportAPI, netip.Addr{}, nil, c.in)
		if f, ok := errors.AsType[*FieldError](err); !ok || f.Pointer != c.pointer || f.Code != c.code {
			t.Errorf("%s %s: %v", c.pointer, c.code, err)
		}
		if len(s.policyUps) != 0 || len(s.audit) != 0 {
			t.Errorf("%s: written", c.pointer)
		}
	}
}

// SetCloseIdle replaces how o closes the idle outbound connections, for the tests of package organization_test.
func (o *OutboundPolicies) SetCloseIdle(f func()) { o.closeIdle = f }
