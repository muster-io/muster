// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package matchers parses Matchers in the Alertmanager syntax and matches label sets with them: name, operator
// (=, !=, =~, !~) and value, the value of =~ and !~ being an RE2 expression anchored at both ends as in Alertmanager,
// and a label the set lacks counting as the empty value. The Alerts view filters by Matchers, and routing reuses them.
package matchers

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Op is the operator of a Matcher.
type Op string

// The operators of the Alertmanager syntax.
const (
	Equal     Op = "="
	NotEqual  Op = "!="
	Regexp    Op = "=~"
	NotRegexp Op = "!~"
)

// ErrSyntax is a Matcher that is not in the Alertmanager syntax.
var ErrSyntax = errors.New("not a matcher")

// RegexpError is a Matcher whose regular expression does not compile.
type RegexpError struct {
	Err error
}

func (e *RegexpError) Error() string { return e.Err.Error() }

func (e *RegexpError) Unwrap() error { return e.Err }

// Matcher is one label condition.
type Matcher struct {
	Name  string
	Op    Op
	Value string
	re    *regexp.Regexp
}

// New returns the Matcher name op value; a regular expression that does not compile is a *RegexpError.
func New(name string, op Op, value string) (Matcher, error) {
	m := Matcher{Name: name, Op: op, Value: value}
	switch op {
	case Equal, NotEqual:
	case Regexp, NotRegexp:
		re, err := regexp.Compile("^(?:" + value + ")$")
		if err != nil {
			// The error is reported against the value as typed, not the anchored expression built from it; the
			// anchored error remains for a value that compiles alone.
			if _, alone := regexp.Compile(value); alone != nil {
				err = alone
			}
			return Matcher{}, &RegexpError{Err: err}
		}
		m.re = re
	default:
		return Matcher{}, fmt.Errorf("%w: the operator must be one of =, !=, =~, !~", ErrSyntax)
	}
	if name == "" {
		return Matcher{}, fmt.Errorf("%w: the label name is empty", ErrSyntax)
	}
	return m, nil
}

// Parse reads one Matcher such as namespace="payments" or pod=~"api-.*". The name may be quoted; the value may be
// unquoted, running to the end of the input.
func Parse(s string) (Matcher, error) {
	s = strings.TrimSpace(s)
	name, rest, err := parseName(s)
	if err != nil {
		return Matcher{}, err
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	var op Op
	for _, o := range []Op{Regexp, NotRegexp, NotEqual, Equal} {
		if strings.HasPrefix(rest, string(o)) {
			op = o
			break
		}
	}
	if op == "" {
		return Matcher{}, fmt.Errorf("%w: an operator =, !=, =~ or !~ must follow the label name", ErrSyntax)
	}
	value, err := parseValue(strings.TrimSpace(rest[len(op):]))
	if err != nil {
		return Matcher{}, err
	}
	return New(name, op, value)
}

// parseName reads a label name, plain or quoted, and returns the rest of s.
func parseName(s string) (string, string, error) {
	if strings.HasPrefix(s, `"`) {
		end := closingQuote(s)
		if end < 0 {
			return "", "", fmt.Errorf("%w: the quoted label name is not closed", ErrSyntax)
		}
		name, err := strconv.Unquote(s[:end+1])
		if err != nil || name == "" {
			return "", "", fmt.Errorf("%w: the quoted label name is not valid", ErrSyntax)
		}
		return name, s[end+1:], nil
	}
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != '_' && r != ':' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			break
		}
		i += size
	}
	if i == 0 {
		return "", "", fmt.Errorf("%w: a matcher starts with a label name", ErrSyntax)
	}
	return s[:i], s[i:], nil
}

// parseValue reads a value: a quoted string, which must end the input, or the unquoted rest.
func parseValue(s string) (string, error) {
	if !strings.HasPrefix(s, `"`) {
		if strings.ContainsAny(s, `"`) {
			return "", fmt.Errorf("%w: an unquoted value cannot contain a quote", ErrSyntax)
		}
		return s, nil
	}
	end := closingQuote(s)
	if end < 0 {
		return "", fmt.Errorf("%w: the quoted value is not closed", ErrSyntax)
	}
	if strings.TrimSpace(s[end+1:]) != "" {
		return "", fmt.Errorf("%w: nothing may follow the quoted value", ErrSyntax)
	}
	v, err := strconv.Unquote(s[:end+1])
	if err != nil {
		return "", fmt.Errorf("%w: the quoted value is not valid", ErrSyntax)
	}
	return v, nil
}

// closingQuote is the index of the quote that closes the one at the start of s, or -1.
func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// Matches reports whether the labels satisfy m; a missing label is the empty value.
func (m Matcher) Matches(labels map[string]string) bool {
	v := labels[m.Name]
	switch m.Op {
	case Equal:
		return v == m.Value
	case NotEqual:
		return v != m.Value
	case Regexp:
		return m.re.MatchString(v)
	case NotRegexp:
		return !m.re.MatchString(v)
	}
	return false
}

// String is m in the Alertmanager syntax, with a quoted value.
func (m Matcher) String() string {
	return m.Name + string(m.Op) + strconv.Quote(m.Value)
}

// All reports whether the labels satisfy every Matcher of ms.
func All(ms []Matcher, labels map[string]string) bool {
	for _, m := range ms {
		if !m.Matches(labels) {
			return false
		}
	}
	return true
}
