// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package publicid generates and parses the opaque public_id of every resource (ADR-0008): a type prefix and 12
// random characters of Crockford base32 (60 bits) from crypto/rand, written in upper case. Input is read in any case,
// with O as 0 and I and L as 1, and checked against the prefix the caller expects.
package publicid

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

// Prefix is the type prefix of a public_id, as in the table of api/README.md.
type Prefix string

const (
	AuditLogEntry      Prefix = "AE"
	AlertGroup         Prefix = "AG"
	AccountLink        Prefix = "AK"
	AccountLinkRequest Prefix = "AR"
	Connection         Prefix = "CN"
	DeliveryEvent      Prefix = "DE"
	Destination        Prefix = "DS"
	LinkRule           Prefix = "KR"
	Note               Prefix = "NE"
	IntegrationToken   Prefix = "NK"
	Integration        Prefix = "NT"
	PersonalToken      Prefix = "PT"
	Organization       Prefix = "RG"
	Route              Prefix = "RT"
	ServiceAccount     Prefix = "SA"
	Session            Prefix = "SN"
	User               Prefix = "SR"
	StoredSnapshot     Prefix = "SS"
	ServiceToken       Prefix = "ST"
	LookupTable        Prefix = "TB"
	TimelineEntry      Prefix = "TE"
)

// Alphabet is Crockford base32: the digits and the upper-case letters without I, L, O and U.
const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// RandomLength is the number of random characters after the prefix.
const RandomLength = 12

// ErrInvalid is wrapped by every error of Parse.
var ErrInvalid = errors.New("invalid public id")

// New returns a new public_id with prefix p.
func New(p Prefix) string {
	var b [RandomLength]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
	out := make([]byte, 0, len(p)+RandomLength)
	out = append(out, p...)
	for _, c := range b {
		out = append(out, Alphabet[c&31])
	}
	return string(out)
}

// Parse normalizes s — upper case, O read as 0, I and L as 1 — and returns it when it is a public_id with prefix
// want; the error says why not without repeating s.
func Parse(want Prefix, s string) (string, error) {
	norm := normalize(s)
	if len(norm) != len(want)+RandomLength {
		return "", fmt.Errorf("%w: a %s id has %d characters", ErrInvalid, want, len(want)+RandomLength)
	}
	if !strings.HasPrefix(norm, string(want)) {
		return "", fmt.Errorf("%w: the id does not start with %s", ErrInvalid, want)
	}
	for _, c := range norm[len(want):] {
		if !strings.ContainsRune(Alphabet, c) {
			return "", fmt.Errorf("%w: the id has a character outside Crockford base32", ErrInvalid)
		}
	}
	return norm, nil
}

func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		switch r = toUpper(r); r {
		case 'O':
			return '0'
		case 'I', 'L':
			return '1'
		}
		return r
	}, s)
}

// toUpper maps only ASCII letters, so that no other character becomes one of the alphabet.
func toUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 'a' + 'A'
	}
	return r
}
