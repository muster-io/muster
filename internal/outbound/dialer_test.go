// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeResolver answers from a map, one answer after another for a name with several; a missing name is not found.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][][]string
	calls   int
	err     error
}

func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	answers, ok := r.answers[host]
	if !ok || len(answers) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	answer := answers[0]
	if len(answers) > 1 {
		r.answers[host] = answers[1:]
	}
	var out []netip.Addr
	for _, a := range answer {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

// listen accepts and closes connections on loopback and returns its port.
func listen(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

type failingPolicy struct{}

func (failingPolicy) OutboundPolicy(context.Context) (Policy, error) {
	return Policy{}, errors.New("database unavailable")
}

func TestDialerConnectsOnlyToAllowedAddress(t *testing.T) {
	port := listen(t)
	r := &fakeResolver{answers: map[string][][]string{
		"mixed.test": {{"169.254.169.254", "10.9.9.9", "127.0.0.1"}},
	}}
	// 10.9.9.9 is denied, 169.254.169.254 always blocked: only 127.0.0.1 passes and is dialled.
	p := mustPolicy(t, ModeStandard, []string{"127.0.0.0/8", "169.254.0.0/16"}, []string{"10.9.9.9"})
	d := &dialer{policy: StaticPolicy(p), resolver: r, timeout: time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("mixed.test", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if got := conn.RemoteAddr().String(); got != "127.0.0.1:"+port {
		t.Fatalf("connected to %s", got)
	}
	_ = conn.Close()
}

func TestDialerChecksTheAnswerItDials(t *testing.T) {
	port := listen(t)
	// A rebinding resolver: the first answer passes, a later one would be link-local. The dialer resolves once and
	// dials the address it checked.
	r := &fakeResolver{answers: map[string][][]string{"rebind.test": {{"127.0.0.1"}, {"169.254.169.254"}}}}
	d := &dialer{policy: StaticPolicy(mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, nil)), resolver: r,
		timeout: time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("rebind.test", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	if r.calls != 1 {
		t.Fatalf("resolved %d times", r.calls)
	}
	_, err = d.DialContext(context.Background(), "tcp", net.JoinHostPort("rebind.test", port))
	var b *BlockedError
	if !errors.As(err, &b) || !strings.Contains(b.Rule, "169.254.169.254 is link-local") {
		t.Fatalf("second dial: %v", err)
	}
}

func TestDialerBlocks(t *testing.T) {
	r := &fakeResolver{answers: map[string][][]string{
		"meta.test": {{"169.254.169.254", "fd00:ec2::254"}},
		"bad.test":  {{"127.0.0.1"}},
	}}
	p := mustPolicy(t, ModeStandard, nil, []string{"bad.test"})
	tests := []struct {
		name, address, rule string
		proxy               bool
	}{
		{"all addresses blocked", "meta.test:80", "169.254.169.254 is link-local (always blocked)", false},
		{"address", "127.0.0.1:80", "127.0.0.1 is loopback", false},
		{"zoned address", "[::1%lo0]:80", "::1 is loopback", false},
		{"zoned proxy address", "[fe80::1%eth0]:1080", "proxy fe80::1 is link-local", true},
		{"denied name", "bad.test:80", "host bad.test matches the denied entry bad.test", false},
		{"proxy address", "169.254.169.254:1080", "proxy 169.254.169.254 is link-local (always blocked)", true},
		{"proxy name", "meta.test:1080", "proxy 169.254.169.254 is link-local", true},
		{"proxy denied name", "bad.test:1080", "proxy host bad.test matches the denied entry", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &dialer{policy: StaticPolicy(p), resolver: r, timeout: time.Second, proxy: tt.proxy}
			_, err := d.DialContext(context.Background(), "tcp", tt.address)
			var b *BlockedError
			if !errors.As(err, &b) || !strings.Contains(b.Rule, tt.rule) {
				t.Fatalf("err %v, want rule %q", err, tt.rule)
			}
		})
	}
}

func TestDialerErrors(t *testing.T) {
	allow := StaticPolicy(mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, nil))
	d := &dialer{policy: allow, resolver: &fakeResolver{}, timeout: time.Second}
	if _, err := d.DialContext(context.Background(), "tcp", "no-port"); err == nil {
		t.Fatal("dialled an address without a port")
	}
	var dnsErr *net.DNSError
	if _, err := d.DialContext(context.Background(), "tcp", "missing.test:80"); !errors.As(err, &dnsErr) {
		t.Fatalf("unresolved name: %v", err)
	}
	d.resolver = &fakeResolver{answers: map[string][][]string{"empty.test": {{}}}}
	if _, err := d.DialContext(context.Background(), "tcp", "empty.test:80"); !errors.As(err, &dnsErr) {
		t.Fatalf("empty answer: %v", err)
	}
	if _, err := d.DialContext(context.Background(), "tcp", "127.0.0.1:1"); err == nil ||
		!strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("refused connection: %v", err)
	}
	d.policy = failingPolicy{}
	if _, err := d.DialContext(context.Background(), "tcp", "127.0.0.1:1"); err == nil ||
		!strings.Contains(err.Error(), "read the outbound address policy: database unavailable") {
		t.Fatalf("policy error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.policy = allow
	d.resolver = &fakeResolver{answers: map[string][][]string{"two.test": {{"127.0.0.1", "127.0.0.2"}}}}
	if _, err := d.DialContext(ctx, "tcp", "two.test:1"); err == nil {
		t.Fatal("dialled with an ended context")
	}
}
