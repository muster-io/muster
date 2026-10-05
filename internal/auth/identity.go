// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package auth

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/muster-io/muster/internal/audit"
)

// Identity is who makes a request: for now a user signed in with a web session (S-016 adds tokens). It is derived
// once per request by the API middleware and travels in the request context.
type Identity struct {
	Session     Session
	Permissions []Permission
	// Transport is ui for the web session.
	Transport audit.Transport
}

// Actor is the identity as the Audit log records it.
func (id *Identity) Actor() audit.Actor {
	return audit.User(id.Session.User.ID, id.Session.User.PublicID)
}

// Can reports whether the identity holds p.
func (id *Identity) Can(p Permission) bool {
	return slices.Contains(id.Permissions, p)
}

type identityKey struct{}

type addressKey struct{}

// WithIdentity returns ctx carrying id.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity of the request, if it has one.
func IdentityFrom(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(*Identity)
	return id, ok && id != nil
}

// WithClientAddress returns ctx carrying the client address of the request.
func WithClientAddress(ctx context.Context, addr netip.Addr) context.Context {
	return context.WithValue(ctx, addressKey{}, addr)
}

// ClientAddressFrom returns the client address of the request; it is invalid when the middleware did not set one.
func ClientAddressFrom(ctx context.Context) netip.Addr {
	addr, _ := ctx.Value(addressKey{}).(netip.Addr)
	return addr
}

// ClientAddress is the client address of C-02.FR-1: the TCP peer of remoteAddr; when the peer is inside one of the
// trusted proxy networks, the first address of X-Forwarded-For read from the right that is outside them. A forged
// entry on the left is never reached while a trusted proxy appended the real one after it. When every entry is
// trusted, the left-most one is the client; an entry that does not parse ends the walk at the last good address.
func ClientAddress(remoteAddr string, forwardedFor []string, trusted []netip.Prefix) netip.Addr {
	peer := parseAddr(remoteAddr)
	if !peer.IsValid() || !inside(peer, trusted) {
		return peer
	}
	var hops []string
	for _, h := range forwardedFor {
		hops = append(hops, strings.Split(h, ",")...)
	}
	client := peer
	for _, hop := range slices.Backward(hops) {
		addr := parseAddr(strings.TrimSpace(hop))
		if !addr.IsValid() {
			return client
		}
		client = addr
		if !inside(addr, trusted) {
			return addr
		}
	}
	return client
}

// parseAddr reads an address with or without a port, unmapping IPv4 in IPv6.
func parseAddr(s string) netip.Addr {
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap().WithZone("")
}

func inside(addr netip.Addr, networks []netip.Prefix) bool {
	return slices.ContainsFunc(networks, func(p netip.Prefix) bool { return p.Contains(addr) })
}
