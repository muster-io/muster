// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// Resolver resolves host names; *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// dialer connects only to addresses that pass the outbound address policy. It resolves the name itself, checks
// every address it gets and dials the addresses that passed, so a DNS answer that changes between the check and the
// connection cannot bypass the policy.
type dialer struct {
	policy   PolicySource
	resolver Resolver
	timeout  time.Duration
	// proxy is set when every connection this dialer makes goes to the client's proxy; the rule then names it.
	proxy bool
}

func (d *dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	p, err := d.policy.OutboundPolicy(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the outbound address policy: %w", err)
	}
	what := ""
	if d.proxy {
		what = "proxy "
	}
	addrs, err := d.resolve(ctx, p, host, what)
	if err != nil {
		return nil, err
	}
	var allowed []netip.Addr
	var blocked *BlockedError
	for _, a := range addrs {
		if b := p.checkAddr(a, d.label(what, a)); b != nil {
			if blocked == nil {
				blocked = b
			}
			continue
		}
		allowed = append(allowed, a)
	}
	if len(allowed) == 0 {
		return nil, blocked
	}
	var nd net.Dialer
	var errs []error
	for _, a := range allowed {
		conn, err := nd.DialContext(ctx, network, net.JoinHostPort(plainAddr(a).String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

func (d *dialer) label(what string, a netip.Addr) string {
	if what == "" {
		return ""
	}
	return what + plainAddr(a).String()
}

// resolve returns the addresses of host after the denied names are checked; an address is its own answer.
func (d *dialer) resolve(ctx context.Context, p Policy, host, what string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	if b := p.checkDeniedName(host, what+"host "); b != nil {
		return nil, b
	}
	addrs, err := d.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	return addrs, nil
}
