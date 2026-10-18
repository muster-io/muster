// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/logging"
)

// TestMaskHeaders: the values of every masked header are [redacted] whatever they hold and whatever the case of its
// name; every other name and value goes through the redactor; the input is not changed, and nil stays nil.
func TestMaskHeaders(t *testing.T) {
	if MaskHeaders(nil, strings.ToUpper) != nil {
		t.Error("MaskHeaders(nil) is not nil")
	}
	h := http.Header{"Content-Type": {"application/json"}, "X-Token": {"s3cret", "other"}}
	for i, name := range MaskedHeaders {
		h[name] = []string{"value", "second"}
		if i%2 == 0 {
			h[strings.ToLower(name)+"-x"] = []string{"kept"}
			delete(h, name)
			h[strings.ToLower(name)] = []string{"lower " + name}
		}
	}
	before := h.Clone()
	red := newRedactor([]logging.Secret{"s3cret"}).redact
	got := MaskHeaders(h, red)
	if !reflect.DeepEqual(h, before) {
		t.Errorf("the input changed: %v", h)
	}
	for name, values := range got {
		masked := false
		for _, m := range MaskedHeaders {
			masked = masked || strings.EqualFold(name, m)
		}
		for _, v := range values {
			if masked && v != "[redacted]" || strings.Contains(v, "s3cret") || strings.Contains(v, "lower") {
				t.Errorf("%s: %q", name, v)
			}
		}
	}
	if !reflect.DeepEqual(got["X-Token"], []string{"[redacted]", "other"}) ||
		!reflect.DeepEqual(got["Content-Type"], []string{"application/json"}) ||
		!reflect.DeepEqual(got["Set-Cookie"], []string{"[redacted]", "[redacted]"}) {
		t.Errorf("MaskHeaders = %v", got)
	}
	if got := MaskHeaders(http.Header{"X-s3cret": {"v"}}, red); got["X-[redacted]"] == nil {
		t.Errorf("a secret in a name: %v", got)
	}
}

// TestRecordContext: links and secrets travel in the context of a request; WithSecrets adds to the secrets already
// there without changing the parent's; an owner is set when it names a Connection or a Destination.
func TestRecordContext(t *testing.T) {
	ctx := t.Context()
	if LinksOf(ctx) != (Links{}) || secretsOf(ctx) != nil {
		t.Error("an empty context carries links or secrets")
	}
	l := Links{Destination: 7, AlertGroup: 9}
	if got := LinksOf(WithLinks(ctx, l)); got != l {
		t.Errorf("LinksOf = %+v", got)
	}
	one := WithSecrets(ctx, "a")
	two := WithSecrets(one, "b", "c")
	if got := secretsOf(two); !reflect.DeepEqual(got, []logging.Secret{"a", "b", "c"}) {
		t.Errorf("secretsOf = %v", got)
	}
	if got := secretsOf(one); !reflect.DeepEqual(got, []logging.Secret{"a"}) {
		t.Errorf("the parent's secrets = %v", got)
	}
	for _, tt := range []struct {
		o    Owner
		want bool
	}{{Owner{}, false}, {Owner{PublicID: "CN0000000000A1"}, false}, {Owner{Connection: 1}, true},
		{Owner{Destination: 2}, true}} {
		if tt.o.set() != tt.want {
			t.Errorf("%+v.set() = %v", tt.o, !tt.want)
		}
	}
}
