// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// Mode is the outbound address policy (organization.outbound_policy).
type Mode string

const (
	// ModeStandard allows private addresses and blocks loopback unless an allowed network contains it.
	ModeStandard Mode = "standard"
	// ModeStrict allows only public addresses and allowed networks.
	ModeStrict Mode = "strict"
)

// Entry is one entry of the allowed or denied list: a network in CIDR form (a bare address is a network of one), a
// host name such as chat.example.org, matched exactly, or a domain written *.example.org, which matches every name
// below example.org but not example.org itself.
type Entry struct {
	text    string
	network netip.Prefix
	name    string // a host name, or the suffix .example.org of a domain
	domain  bool
}

// String is the entry as written.
func (e Entry) String() string { return e.text }

// ParseEntry parses an entry of the allowed or denied list.
func ParseEntry(s string) (Entry, error) {
	text := strings.TrimSpace(s)
	if text == "" {
		return Entry{}, errors.New("an entry is empty")
	}
	if strings.Contains(text, "/") {
		p, err := netip.ParsePrefix(text)
		if err != nil {
			return Entry{}, fmt.Errorf("%q is not a network in CIDR form", text)
		}
		return Entry{text: text, network: p.Masked()}, nil
	}
	if a, err := netip.ParseAddr(text); err == nil {
		return Entry{text: text, network: netip.PrefixFrom(a, a.BitLen())}, nil
	}
	name := normalizeName(text)
	domain := false
	if rest, ok := strings.CutPrefix(name, "*."); ok {
		name, domain = "."+rest, true
	}
	if !validName(strings.TrimPrefix(name, ".")) {
		return Entry{}, fmt.Errorf("%q is not a network, a host name or a domain such as *.example.org", text)
	}
	return Entry{text: text, name: name, domain: domain}, nil
}

func validName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
				return false
			}
		}
	}
	return true
}

func normalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(name), ".")
}

func (e Entry) containsAddr(a netip.Addr) bool {
	return e.network.IsValid() && e.network.Contains(a)
}

func (e Entry) matchesName(name string) bool {
	switch {
	case e.network.IsValid():
		return false
	case e.domain:
		return strings.HasSuffix(name, e.name)
	default:
		return name == e.name
	}
}

// Policy is the outbound address policy with its parsed lists.
type Policy struct {
	Mode    Mode
	Allowed []Entry
	Denied  []Entry
}

// ParsePolicy parses the policy as the Organization stores it; the error names every entry that does not parse.
func ParsePolicy(mode string, allowed, denied []string) (Policy, error) {
	p := Policy{Mode: Mode(mode)}
	var errs []error
	if p.Mode != ModeStandard && p.Mode != ModeStrict {
		errs = append(errs, fmt.Errorf("policy %q is neither standard nor strict", mode))
	}
	parse := func(list string, entries []string) []Entry {
		out := make([]Entry, 0, len(entries))
		for i, s := range entries {
			e, err := ParseEntry(s)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s entry %d: %w", list, i, err))
				continue
			}
			out = append(out, e)
		}
		return out
	}
	p.Allowed = parse("allowed", allowed)
	p.Denied = parse("denied", denied)
	return p, errors.Join(errs...)
}

// PolicySource gives the policy in force; the Organization's cached policy implements it.
type PolicySource interface {
	OutboundPolicy(ctx context.Context) (Policy, error)
}

type staticPolicy Policy

func (p staticPolicy) OutboundPolicy(context.Context) (Policy, error) { return Policy(p), nil }

// StaticPolicy is a source that always gives p.
func StaticPolicy(p Policy) PolicySource { return staticPolicy(p) }

// alwaysBlocked are the rules no allowed entry opens. Cloud metadata addresses outside link-local are a reviewed list
// that grows like the registries.
var alwaysBlocked = []struct {
	rule     string
	networks []netip.Prefix
}{
	{"unspecified", []netip.Prefix{netip.MustParsePrefix("0.0.0.0/32"), netip.MustParsePrefix("::/128")}},
	{"link-local", []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("fe80::/10")}},
	{"a cloud metadata address", []netip.Prefix{
		netip.MustParsePrefix("fd00:ec2::254/128"),
		netip.MustParsePrefix("100.100.100.200/32"),
	}},
	{"multicast", []netip.Prefix{netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("ff00::/8")}},
}

// AlwaysBlocked lists the rules that always apply, each with its networks, for the policy as the API shows it.
func AlwaysBlocked() []string {
	out := make([]string, 0, len(alwaysBlocked))
	for _, r := range alwaysBlocked {
		nets := make([]string, 0, len(r.networks))
		for _, n := range r.networks {
			nets = append(nets, n.String())
		}
		out = append(out, strings.TrimPrefix(r.rule, "a ")+": "+strings.Join(nets, ", "))
	}
	return out
}

var (
	loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	// nonPublic are addresses that are neither private in the sense of netip nor public: shared address space,
	// IETF protocol assignments, documentation, benchmarking and reserved ranges.
	nonPublic = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
	}
	// nat64 embeds an IPv4 address in its last 32 bits; the embedded address is the one checked.
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
)

// plainAddr is a without an IPv6 zone and with an IPv4-mapped address unmapped: netip prefixes never contain a zoned
// address, so a zone left on would pass every rule.
func plainAddr(a netip.Addr) netip.Addr {
	return a.WithZone("").Unmap()
}

func inAny(a netip.Addr, nets []netip.Prefix) bool {
	return slices.ContainsFunc(nets, func(p netip.Prefix) bool { return p.Contains(a) })
}

// BlockedError is a request refused by the outbound address policy; Rule names the rule that refused it.
type BlockedError struct {
	Rule string
}

func (e *BlockedError) Error() string { return "blocked by the outbound address policy: " + e.Rule }

// checkAddr applies p to one address; what names the address in the rule, such as "proxy 10.0.0.1".
func (p Policy) checkAddr(a netip.Addr, what string) *BlockedError {
	a = plainAddr(a)
	if nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	if what == "" {
		what = a.String()
	}
	for _, r := range alwaysBlocked {
		if inAny(a, r.networks) {
			return &BlockedError{Rule: what + " is " + r.rule + " (always blocked)"}
		}
	}
	if i := slices.IndexFunc(p.Denied, func(e Entry) bool { return e.containsAddr(a) }); i >= 0 {
		return &BlockedError{Rule: what + " is in the denied network " + p.Denied[i].text}
	}
	if slices.ContainsFunc(p.Allowed, func(e Entry) bool { return e.containsAddr(a) }) {
		return nil
	}
	if inAny(a, loopback) {
		return &BlockedError{Rule: what + " is loopback (allow it with an allowed network)"}
	}
	if p.Mode == ModeStrict && (!a.IsGlobalUnicast() || a.IsPrivate() || inAny(a, nonPublic)) {
		return &BlockedError{Rule: what + " is not a public address (the strict policy allows it only with an allowed network)"}
	}
	return nil
}

// checkDeniedName blocks a host name that matches a denied host name or domain; a denied entry always wins.
func (p Policy) checkDeniedName(name, what string) *BlockedError {
	name = normalizeName(name)
	if i := slices.IndexFunc(p.Denied, func(e Entry) bool { return e.matchesName(name) }); i >= 0 {
		return &BlockedError{Rule: what + name + " matches the denied entry " + p.Denied[i].text}
	}
	return nil
}

// checkUnresolvedName is the check of a target name that only the proxy can resolve: a denied name is blocked, and
// in strict mode only a name that matches an allowed entry passes.
func (p Policy) checkUnresolvedName(name string) *BlockedError {
	if err := p.checkDeniedName(name, "host "); err != nil {
		return err
	}
	name = normalizeName(name)
	if p.Mode == ModeStrict && !slices.ContainsFunc(p.Allowed, func(e Entry) bool { return e.matchesName(name) }) {
		return &BlockedError{Rule: "host " + name + " cannot be resolved and matches no allowed entry (the strict " +
			"policy needs one)"}
	}
	return nil
}
