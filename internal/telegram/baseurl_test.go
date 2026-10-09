// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"errors"
	"slices"
	"testing"
)

// TestParseBaseURL covers C-14.FR-10: an absolute http or https URL without query, fragment or user information; a path
// prefix is kept and its trailing slashes are dropped.
func TestParseBaseURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://api.telegram.org":             "https://api.telegram.org",
		"https://api.telegram.org/":            "https://api.telegram.org",
		"https://tg.example.org/k3x9/":         "https://tg.example.org/k3x9",
		"http://127.0.0.1:18081//":             "http://127.0.0.1:18081",
		"https://tg.example.org/a/b%2Fc/":      "https://tg.example.org/a/b%2Fc",
		"HTTPS://Tg.Example.org:8443/prefix//": "https://Tg.Example.org:8443/prefix",
	} {
		got, err := ParseBaseURL(raw)
		if err != nil || got != want {
			t.Errorf("ParseBaseURL(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "api.telegram.org", "ftp://api.telegram.org", "https://user:pw@api.example.org/",
		"https://api.example.org/?x=1", "https://api.example.org/?", "https://api.example.org/#frag",
		"https://:443/", "/relative/path", "https://api.example.org/%zz", "mailto:bot@example.org"} {
		if got, err := ParseBaseURL(raw); !errors.Is(err, ErrBaseURL) {
			t.Errorf("ParseBaseURL(%q) = %q, %v; want ErrBaseURL", raw, got, err)
		}
	}
}

func TestBaseURLWarningsAndOrigin(t *testing.T) {
	if w := BaseURLWarnings("http://127.0.0.1:18081"); !slices.Equal(w, []string{WarningBaseURLUsesHTTP}) {
		t.Errorf("http warnings = %v", w)
	}
	if w := BaseURLWarnings("https://api.telegram.org"); w == nil || len(w) != 0 {
		t.Errorf("https warnings = %#v", w)
	}
	for base, want := range map[string]string{
		"https://tg.example.org/k3x9": "https://tg.example.org",
		"http://127.0.0.1:18081":      "http://127.0.0.1:18081",
		"::not a url":                 "",
	} {
		if got := Origin(base); got != want {
			t.Errorf("Origin(%q) = %q, want %q", base, got, want)
		}
	}
}
