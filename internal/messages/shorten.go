// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package messages

import (
	"strings"
	"unicode/utf8"
)

// Shortening (C-12.FR-11): a message laid out longer than the length limit an adapter declares loses, in turn, its
// Alert lines (down to the distinct values of the differing labels), its common annotations, common labels and group
// labels, and then its distinct values, environment, summary, text lines, the lines of a template's body and the
// notices, until it fits. The heading, status, footer, buttons and the link to Muster are never removed; only a title
// or a footer that cannot fit even alone is cut.

// Length is the length of m laid out in a markup, in characters.
func Length(m Message, markup Markup) int {
	return utf8.RuneCountInString(Layout(m, markup))
}

// Shorten shortens m to the limit in a markup and says whether it changed anything; a limit of 0 or less is none.
func Shorten(m Message, limit int, markup Markup) (Message, bool) {
	if limit <= 0 || Length(m, markup) <= limit {
		return m, false
	}
	m = cloneMessage(m)
	fits := func() bool { return Length(m, markup) <= limit }
	steps := []func() bool{
		func() bool { // the Alert lines, down to the distinct values
			if m.Alerts == nil || len(m.Alerts.Lines) == 0 {
				return false
			}
			m.Alerts.Lines = nil
			if m.Alerts.FullList == nil && m.Heading != nil {
				m.Alerts.FullList = &Link{Text: T(m.Language, "alerts.fullList", nil), URL: m.Heading.URL}
			}
			return true
		},
		func() bool { return drop(&m.CommonAnnotations) },
		func() bool { return drop(&m.CommonLabels) },
		func() bool { return drop(&m.GroupLabels) },
		func() bool { // the distinct values, keeping the link to the full list
			if m.Alerts == nil || len(m.Alerts.Distinct) == 0 {
				return false
			}
			m.Alerts.Distinct = nil
			return true
		},
		func() bool { return clearText(&m.Environment) },
		func() bool { return clearText(&m.Summary) },
	}
	for _, step := range steps {
		if step() && fits() {
			return m, true
		}
	}
	// The text lines, the body and the notices go from their end; then the title and the footer are cut.
	for len(m.Lines) > 0 && !fits() {
		m.Lines = m.Lines[:len(m.Lines)-1]
	}
	// A body is template output in its markup: it loses whole lines, so that no tag or entity is cut, and goes
	// entirely when one line is still too long.
	for m.Body != nil && !fits() {
		lines := strings.Split(strings.TrimRight(m.Body.Text, "\n"), "\n")
		if len(lines) <= 1 {
			m.Body = nil
			break
		}
		m.Body = &Body{Text: strings.Join(lines[:len(lines)-1], "\n"), Markup: m.Body.Markup}
	}
	for len(m.Notices) > 0 && !fits() {
		m.Notices = m.Notices[:len(m.Notices)-1]
	}
	if m.Heading != nil && m.Heading.Title != "" && !fits() {
		m.Heading.Title = cutEnd(m.Heading.Title, Length(m, markup)-limit)
	}
	if m.Footer != "" && !fits() {
		m.Footer = cutEnd(m.Footer, Length(m, markup)-limit)
	}
	return m, true
}

// drop empties a label section and says whether it had anything.
func drop(s *[]Label) bool {
	had := len(*s) > 0
	*s = nil
	return had
}

func clearText(s *string) bool {
	had := *s != ""
	*s = ""
	return had
}

// cutEnd removes at least over characters from the end of s, at a character boundary, ending it with "…".
func cutEnd(s string, over int) string {
	n := utf8.RuneCountInString(s) - over - utf8.RuneCountInString(ellipsis)
	if n <= 0 {
		return ellipsis
	}
	return string([]rune(s)[:n]) + ellipsis
}

// cloneMessage copies the parts of m that shortening changes.
func cloneMessage(m Message) Message {
	if m.Alerts != nil {
		a := *m.Alerts
		m.Alerts = &a
	}
	if m.Heading != nil {
		h := *m.Heading
		m.Heading = &h
	}
	m.Lines = append([]string(nil), m.Lines...)
	m.Notices = append([]string(nil), m.Notices...)
	return m
}
