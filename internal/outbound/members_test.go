// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import "testing"

// TestMemberMasker masks the named members by their place — plain, and JSON-escaped inside a JSON string, an escaped
// quote or backslash in the value included — and leaves other members, free text and the same digits elsewhere.
func TestMemberMasker(t *testing.T) {
	m := NewMemberMasker("contact", "text")
	for in, want := range map[string]string{
		`{"contact":"1234","id":"n1234"}`:                                `{"contact":"[redacted]","id":"n1234"}`,
		`{"contact" : "12\"34", "text":"a\\b"}`:                          `{"contact" : "[redacted]", "text":"[redacted]"}`,
		`{"detail":"got {\"contact\":\"1234\",\"text\":\"x\"} at 1234"}`: `{"detail":"got {\"contact\":\"[redacted]\",\"text\":\"[redacted]\"} at 1234"}`,
		`{"detail":"{\"contact\":\"12\\\"34\\\\\",\"x\":\"5\"}"}`:        `{"detail":"{\"contact\":\"[redacted]\",\"x\":\"5\"}"}`,
		`{"detail":"{\"text\":\"line\nnext\"}"}`:                         `{"detail":"{\"text\":\"[redacted]\"}"}`,
		`cannot call 1234: contact unreachable`:                          `cannot call 1234: contact unreachable`,
		`{"contacts":"1234","context":"1234"}`:                           `{"contacts":"1234","context":"1234"}`,
		`no members here`:                                                `no members here`,
	} {
		if got := m.Mask(in); got != want {
			t.Errorf("Mask(%s)\n = %s\nwant %s", in, got, want)
		}
	}
}
