// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"regexp"
	"strings"
)

// The value of a JSON member as it stands in a text: plain, `"name":"…"`, and JSON-escaped, `\"name\":\"…\"`, as a
// JSON document inside a JSON string carries it — the problem of a bridge that quotes the request it got. In the
// escaped form, a backslash of the inner document is two, so an inner escape is `\\` and the escaped character, and the
// value ends at the first `\"` that is not one.
const (
	plainValue   = `(?:[^"\\]|\\.)*`
	escapedValue = `(?:[^\\]|\\\\(?:\\.|[^\\])|\\[^"\\])*`
)

// MemberMasker replaces the values of named JSON members with [redacted] by their place, whatever their length, so
// that a value too short to mask as a substring, such as a contact "1234", is masked where a JSON document carries
// it. A value in free text, outside a JSON member, is not found.
type MemberMasker struct {
	names []string
	res   []*regexp.Regexp
}

// NewMemberMasker masks the members names, each matched literally.
func NewMemberMasker(names ...string) MemberMasker {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	alt := strings.Join(quoted, "|")
	return MemberMasker{names: names, res: []*regexp.Regexp{
		regexp.MustCompile(`("(?:` + alt + `)"\s*:\s*")` + plainValue + `(")`),
		regexp.MustCompile(`(\\"(?:` + alt + `)\\"\s*:\s*\\")` + escapedValue + `(\\")`),
	}}
}

// Mask is s with the value of every named member replaced, in both forms.
func (m MemberMasker) Mask(s string) string {
	found := false
	for _, n := range m.names {
		if strings.Contains(s, n) {
			found = true
			break
		}
	}
	if !found {
		return s
	}
	for _, re := range m.res {
		s = re.ReplaceAllString(s, "${1}"+redacted+"${2}")
	}
	return s
}
