// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package publicid

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

var allPrefixes = []Prefix{
	AuditLogEntry, AlertGroup, AccountLink, AccountLinkRequest, Connection, DeliveryEvent, Destination, LinkRule,
	Note, IntegrationToken, Integration, PersonalToken, Organization, Route, ServiceAccount, Session, User,
	StoredSnapshot, ServiceToken, LookupTable, TimelineEntry,
}

// The pattern of the PublicId schema and of the CHECK constraints, for one prefix.
func pattern(p Prefix) *regexp.Regexp {
	return regexp.MustCompile(`^` + string(p) + `[0-9A-HJKMNP-TV-Z]{12}$`)
}

func TestNewMatchesTheSchemaPattern(t *testing.T) {
	for _, p := range allPrefixes {
		id := New(p)
		if !pattern(p).MatchString(id) {
			t.Errorf("New(%s) = %q, not matching the schema pattern", p, id)
		}
		if got, err := Parse(p, id); err != nil || got != id {
			t.Errorf("Parse(%s, %q) = %q, %v; want the id back", p, id, got, err)
		}
	}
}

func TestPrefixesAreDistinctAndInTheAlphabet(t *testing.T) {
	seen := map[Prefix]bool{}
	for _, p := range allPrefixes {
		if seen[p] {
			t.Errorf("prefix %s is used twice", p)
		}
		seen[p] = true
		if len(p) != 2 || strings.Trim(string(p), Alphabet) != "" {
			t.Errorf("prefix %s is not two letters of the alphabet", p)
		}
	}
}

func TestNewUsesTheWholeAlphabetRandomly(t *testing.T) {
	used := map[rune]bool{}
	ids := map[string]bool{}
	for range 2000 {
		id := New(Route)
		if ids[id] {
			t.Fatalf("New returned %q twice", id)
		}
		ids[id] = true
		for _, c := range id[2:] {
			used[c] = true
		}
	}
	if len(used) != len(Alphabet) {
		t.Errorf("2000 ids used %d of the %d characters of the alphabet", len(used), len(Alphabet))
	}
}

func TestParseNormalizes(t *testing.T) {
	cases := map[string]string{
		"rgabcdefghjkmn": "RGABCDEFGHJKMN",
		"RGo0iIlL123456": "RG001111123456",
		"Rg7m3qx9p2rtaz": "RG7M3QX9P2RTAZ",
	}
	for in, want := range cases {
		got, err := Parse(Organization, in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	cases := map[string]string{
		"":                  "has 14 characters",
		"RG":                "has 14 characters",
		"RGABCDEFGHJKMNP":   "has 14 characters",
		"AGABCDEFGHJKMN":    "does not start with RG",
		"RGABCDEFGHJKMU":    "outside Crockford base32",
		"RGABCDEFGHJKM-":    "outside Crockford base32",
		"RGABCDEFGHJKMé":    "has 14 characters",
		"RGABCDEFGHJKM\x00": "outside Crockford base32",
	}
	for in, want := range cases {
		_, err := Parse(Organization, in)
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) error = %v; want %q", in, err, want)
			continue
		}
		if len(in) > 2 && strings.Contains(err.Error(), in) {
			t.Errorf("Parse(%q) repeats its input: %v", in, err)
		}
	}
}
