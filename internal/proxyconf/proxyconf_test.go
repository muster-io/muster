// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package proxyconf

import (
	"errors"
	"testing"

	"github.com/muster-io/muster/internal/outbound"
)

func ptr(s string) *string { return &s }

func TestApplyKeepsReplacesAndClears(t *testing.T) {
	stored := Config{Enabled: true, Type: TypeHTTP, Address: "proxy.example:3128", Username: ptr("muster")}

	got, err := Input{Enabled: false}.Apply(stored)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Type != TypeHTTP || got.Address != "proxy.example:3128" || *got.Username != "muster" {
		t.Fatalf("an omitted type, address and username must be kept: %+v", got)
	}

	got, err = Input{Enabled: true, Type: ptr(TypeSOCKS5), Address: ptr(" 127.0.0.1:1080 "), UsernameSet: true}.Apply(stored)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeSOCKS5 || got.Address != "127.0.0.1:1080" || got.Username != nil {
		t.Fatalf("replace and clear = %+v", got)
	}
	got, err = Input{Enabled: true, UsernameSet: true, Username: ptr("")}.Apply(stored)
	if err != nil || got.Username != nil {
		t.Fatalf("an empty username clears it: %+v, %v", got, err)
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		c       Config
		pointer string
	}{
		{"disabled and empty", Config{}, ""},
		{"enabled without type", Config{Enabled: true, Address: "p:1"}, "/type"},
		{"unknown type", Config{Type: "ftp"}, "/type"},
		{"enabled without address", Config{Enabled: true, Type: TypeHTTP}, "/address"},
		{"no port", Config{Type: TypeHTTP, Address: "proxy.example"}, "/address"},
		{"port zero", Config{Type: TypeHTTP, Address: "proxy.example:0"}, "/address"},
		{"port too large", Config{Type: TypeHTTP, Address: "proxy.example:65536"}, "/address"},
		{"no host", Config{Type: TypeHTTP, Address: ":8080"}, "/address"},
		{"a URL", Config{Type: TypeHTTP, Address: "http://proxy:80"}, "/address"},
		{"username with a colon", Config{Type: TypeHTTP, Address: "p:1", Username: ptr("a:b")}, "/username"},
		{"valid IPv6", Config{Enabled: true, Type: TypeHTTPS, Address: "[::1]:443"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if tc.pointer == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			fe, ok := errors.AsType[*FieldError](err)
			if !ok || fe.Pointer != tc.pointer {
				t.Fatalf("err = %v, want a field error at %s", err, tc.pointer)
			}
		})
	}
}

func TestParseAndOutbound(t *testing.T) {
	c, err := Parse([]byte(`{"enabled": false}`))
	if err != nil || c.Enabled || c.Outbound("pw") != nil {
		t.Fatalf("disabled = %+v, %v", c, err)
	}
	if c, err := Parse(nil); err != nil || c.Enabled {
		t.Fatalf("empty = %+v, %v", c, err)
	}
	if _, err := Parse([]byte(`[`)); err == nil {
		t.Fatal("broken JSON parsed")
	}
	c = Config{Enabled: true, Type: TypeSOCKS5, Address: "127.0.0.1:18092", Username: ptr("u")}
	back, err := Parse(c.JSON())
	if err != nil || back.Address != c.Address || *back.Username != "u" {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
	p := c.Outbound("pw")
	if p == nil || p.Type != outbound.ProxySOCKS5 || p.Address != "127.0.0.1:18092" || p.Username != "u" ||
		p.Password != "pw" {
		t.Fatalf("outbound = %+v", p)
	}
	if string(Config{}.JSON()) != `{"enabled":false}` {
		t.Fatalf("the disabled object = %s", Config{}.JSON())
	}
}
