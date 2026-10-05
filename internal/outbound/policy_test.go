// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"context"
	"net/netip"
	"strings"
	"testing"
)

func mustPolicy(t *testing.T, mode Mode, allowed, denied []string) Policy {
	t.Helper()
	p, err := ParsePolicy(string(mode), allowed, denied)
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	return p
}

func TestCheckAddr(t *testing.T) {
	everything := []string{"0.0.0.0/0", "::/0"}
	tests := []struct {
		name    string
		mode    Mode
		allowed []string
		denied  []string
		addr    string
		rule    string // empty when the address passes
	}{
		{"link-local v4", ModeStandard, nil, nil, "169.254.169.254", "169.254.169.254 is link-local (always blocked)"},
		{"link-local v4 allowed", ModeStandard, []string{"169.254.0.0/16"}, nil, "169.254.1.1", "is link-local (always blocked)"},
		{"link-local v6", ModeStandard, everything, nil, "fe80::1", "fe80::1 is link-local (always blocked)"},
		{"metadata v6", ModeStandard, everything, nil, "fd00:ec2::254", "is a cloud metadata address (always blocked)"},
		{"metadata alibaba", ModeStandard, []string{"100.100.100.200"}, nil, "100.100.100.200", "cloud metadata address"},
		{"unspecified v4", ModeStandard, everything, nil, "0.0.0.0", "0.0.0.0 is unspecified (always blocked)"},
		{"unspecified v6", ModeStrict, everything, nil, "::", ":: is unspecified (always blocked)"},
		{"multicast v4", ModeStandard, everything, nil, "239.1.2.3", "is multicast (always blocked)"},
		{"multicast v6", ModeStandard, everything, nil, "ff02::1", "is multicast (always blocked)"},
		{"mapped link-local", ModeStandard, everything, nil, "::ffff:169.254.169.254", "169.254.169.254 is link-local"},
		{"nat64 link-local", ModeStandard, everything, nil, "64:ff9b::a9fe:a9fe", "169.254.169.254 is link-local"},
		{"zoned loopback", ModeStandard, nil, nil, "::1%lo0", "::1 is loopback"},
		{"zoned link-local", ModeStandard, everything, nil, "fe80::1%eth0", "fe80::1 is link-local (always blocked)"},
		{"zoned metadata", ModeStandard, everything, nil, "fd00:ec2::254%eth0", "fd00:ec2::254 is a cloud metadata address"},
		{"zoned private strict", ModeStrict, nil, nil, "fd12::1%eth0", "fd12::1 is not a public address"},
		{"this network strict", ModeStrict, nil, nil, "0.1.2.3", "0.1.2.3 is not a public address"},
		{"local-use nat64 strict", ModeStrict, nil, nil, "64:ff9b:1::1", "is not a public address"},
		{"loopback standard", ModeStandard, nil, nil, "127.0.0.1", "127.0.0.1 is loopback (allow it with an allowed network)"},
		{"loopback v6", ModeStandard, nil, nil, "::1", "::1 is loopback"},
		{"loopback allowed", ModeStandard, []string{"127.0.0.0/8"}, nil, "127.0.0.1", ""},
		{"loopback strict allowed", ModeStrict, []string{"127.0.0.1"}, nil, "127.0.0.1", ""},
		{"private standard", ModeStandard, nil, nil, "10.1.2.3", ""},
		{"private v6 standard", ModeStandard, nil, nil, "fd12::1", ""},
		{"cgnat standard", ModeStandard, nil, nil, "100.64.0.1", ""},
		{"public standard", ModeStandard, nil, nil, "93.184.215.14", ""},
		{"private strict", ModeStrict, nil, nil, "10.1.2.3", "10.1.2.3 is not a public address (the strict policy"},
		{"cgnat strict", ModeStrict, nil, nil, "100.64.0.1", "is not a public address"},
		{"reserved strict", ModeStrict, nil, nil, "240.0.0.1", "is not a public address"},
		{"broadcast strict", ModeStrict, nil, nil, "255.255.255.255", "is not a public address"},
		{"private strict allowed", ModeStrict, []string{"10.0.0.0/8"}, nil, "10.1.2.3", ""},
		{"public strict", ModeStrict, nil, nil, "93.184.215.14", ""},
		{"public v6 strict", ModeStrict, nil, nil, "2606:4700::1", ""},
		{"denied", ModeStandard, nil, []string{"10.1.0.0/16"}, "10.1.2.3", "10.1.2.3 is in the denied network 10.1.0.0/16"},
		{"denied wins", ModeStrict, []string{"10.0.0.0/8"}, []string{"10.1.2.3"}, "10.1.2.3", "denied network 10.1.2.3"},
		{"denied public", ModeStandard, nil, []string{"93.184.0.0/16"}, "93.184.215.14", "denied network"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mustPolicy(t, tt.mode, tt.allowed, tt.denied)
			b := p.checkAddr(netip.MustParseAddr(tt.addr), "")
			switch {
			case tt.rule == "" && b != nil:
				t.Fatalf("blocked: %v", b)
			case tt.rule != "" && b == nil:
				t.Fatalf("passed, want a rule with %q", tt.rule)
			case tt.rule != "" && !strings.Contains(b.Error(), tt.rule):
				t.Fatalf("rule %q, want it to contain %q", b.Error(), tt.rule)
			}
			if b != nil && !strings.HasPrefix(b.Error(), "blocked by the outbound address policy: ") {
				t.Fatalf("error %q", b.Error())
			}
		})
	}
}

func TestCheckAddrNamesProxy(t *testing.T) {
	b := Policy{Mode: ModeStandard}.checkAddr(netip.MustParseAddr("169.254.169.254"), "proxy 169.254.169.254")
	if b == nil || b.Rule != "proxy 169.254.169.254 is link-local (always blocked)" {
		t.Fatalf("rule %v", b)
	}
}

func TestParseEntry(t *testing.T) {
	valid := [][2]string{
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"\t10.1.2.3/8 ", "10.1.2.3/8"},
		{"10.1.2.3", "10.1.2.3"},
		{"fd00::/8", "fd00::/8"},
		{"::1", "::1"},
		{"Chat.Example.ORG.", "Chat.Example.ORG."},
		{"*.example.org", "*.example.org"},
		{"under_score.local", "under_score.local"},
	}
	for _, v := range valid {
		in, text := v[0], v[1]
		e, err := ParseEntry(in)
		if err != nil {
			t.Errorf("ParseEntry(%q): %v", in, err)
			continue
		}
		if e.String() != text {
			t.Errorf("ParseEntry(%q) = %q", in, e.String())
		}
	}
	for _, in := range []string{"", "  ", "10.0.0.0/33", "a..b", "-a.org", "a-.org", "*.", "x y", "*", "ä.org",
		strings.Repeat("a", 64) + ".org", strings.Repeat("a.", 127) + "ab"} {
		if _, err := ParseEntry(in); err == nil {
			t.Errorf("ParseEntry(%q) parsed", in)
		}
	}
}

func TestParsePolicy(t *testing.T) {
	if _, err := ParsePolicy("lenient", nil, nil); err == nil || !strings.Contains(err.Error(), `"lenient"`) {
		t.Fatalf("mode error %v", err)
	}
	_, err := ParsePolicy("strict", []string{"ok.org", "bad..org"}, []string{"10.0.0.0/99"})
	if err == nil || !strings.Contains(err.Error(), "allowed entry 1") || !strings.Contains(err.Error(), "denied entry 0") {
		t.Fatalf("entry errors %v", err)
	}
	p, err := StaticPolicy(mustPolicy(t, ModeStrict, []string{"a.org"}, nil)).OutboundPolicy(context.Background())
	if err != nil || p.Mode != ModeStrict || len(p.Allowed) != 1 {
		t.Fatalf("static policy %+v %v", p, err)
	}
}

func TestNames(t *testing.T) {
	p := mustPolicy(t, ModeStrict, []string{"chat.example.org", "*.corp.example", "10.0.0.0/8"},
		[]string{"*.bad.example", "evil.org"})
	tests := []struct {
		name       string
		denied     string // rule of checkDeniedName, empty when it passes
		unresolved string // rule of checkUnresolvedName, empty when it passes
	}{
		{"chat.example.org", "", ""},
		{"CHAT.example.org.", "", ""},
		{"a.corp.example", "", ""},
		{"corp.example", "", "cannot be resolved and matches no allowed entry"},
		{"other.example.org", "", "host other.example.org cannot be resolved"},
		{"x.bad.example", "host x.bad.example matches the denied entry *.bad.example", "matches the denied entry"},
		{"evil.org", "matches the denied entry evil.org", "matches the denied entry evil.org"},
		{"sub.evil.org", "", "cannot be resolved"},
	}
	for _, tt := range tests {
		check := func(kind string, b *BlockedError, want string) {
			t.Helper()
			switch {
			case want == "" && b != nil:
				t.Errorf("%s(%q) blocked: %v", kind, tt.name, b)
			case want != "" && (b == nil || !strings.Contains(b.Rule, want)):
				t.Errorf("%s(%q) = %v, want %q", kind, tt.name, b, want)
			}
		}
		check("checkDeniedName", p.checkDeniedName(tt.name, "host "), tt.denied)
		check("checkUnresolvedName", p.checkUnresolvedName(tt.name), tt.unresolved)
	}
	standard := mustPolicy(t, ModeStandard, nil, []string{"evil.org"})
	if b := standard.checkUnresolvedName("anything.internal"); b != nil {
		t.Fatalf("standard blocked an unresolved name: %v", b)
	}
	if b := standard.checkUnresolvedName("evil.org"); b == nil {
		t.Fatal("standard passed a denied name")
	}
}

func TestAlwaysBlocked(t *testing.T) {
	got := strings.Join(AlwaysBlocked(), "\n")
	for _, want := range []string{"link-local: 169.254.0.0/16, fe80::/10", "cloud metadata address: fd00:ec2::254/128",
		"100.100.100.200/32", "unspecified: 0.0.0.0/32, ::/128", "multicast: 224.0.0.0/4, ff00::/8"} {
		if !strings.Contains(got, want) {
			t.Errorf("AlwaysBlocked() = %q, missing %q", got, want)
		}
	}
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }
