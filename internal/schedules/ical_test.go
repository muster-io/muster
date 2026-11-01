// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// unfold reads an iCal document back into its content lines: every line must end in CRLF, and a line that starts
// with a space continues the one before (RFC 5545 §3.1).
func unfold(t *testing.T, doc []byte) []string {
	t.Helper()
	s := string(doc)
	if !strings.HasSuffix(s, "\r\n") {
		t.Fatalf("the document does not end in CRLF: %q", s)
	}
	var lines []string
	for raw := range strings.SplitSeq(strings.TrimSuffix(s, "\r\n"), "\r\n") {
		if strings.ContainsAny(raw, "\r\n") {
			t.Fatalf("a bare CR or LF in %q", raw)
		}
		if len(raw) > icalLineMax {
			t.Errorf("a line of %d octets: %q", len(raw), raw)
		}
		if !utf8.ValidString(raw) {
			t.Errorf("a fold split a UTF-8 sequence: %q", raw)
		}
		if cont, ok := strings.CutPrefix(raw, " "); ok && len(lines) > 0 {
			lines[len(lines)-1] += cont
			continue
		}
		lines = append(lines, raw)
	}
	return lines
}

// event is a VEVENT of a feed by its properties.
type event map[string]string

// parseEvents are the VEVENTs of an iCal document, and its VCALENDAR properties.
func parseEvents(t *testing.T, doc []byte) ([]event, map[string]string) {
	t.Helper()
	lines := unfold(t, doc)
	if len(lines) < 2 || lines[0] != "BEGIN:VCALENDAR" || lines[len(lines)-1] != "END:VCALENDAR" {
		t.Fatalf("not a VCALENDAR: %q", lines)
	}
	cal := map[string]string{}
	var events []event
	var cur event
	for _, l := range lines[1 : len(lines)-1] {
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			t.Fatalf("a line without a value: %q", l)
		}
		switch {
		case l == "BEGIN:VEVENT":
			cur = event{}
		case l == "END:VEVENT":
			events = append(events, cur)
			cur = nil
		case cur != nil:
			cur[name] = value
		default:
			cal[name] = value
		}
	}
	return events, cal
}

// spans are the DTSTART/DTEND pairs of events, as "start/end".
func spans(events []event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e["DTSTART"] + "/" + e["DTEND"]
	}
	return out
}

// TestCalendarDocument is C-27.FR-10: the VCALENDAR has PRODID, VERSION and its name, and each VEVENT its UID,
// DTSTAMP, DTSTART and DTEND in UTC and SUMMARY "{Team} · {Schedule}"; every line ends in CRLF, the summary is
// escaped as TEXT and long lines are folded at 75 octets without splitting the two octets of "·" or any other UTF-8
// sequence, and read back unfolded they are the lines written.
func TestCalendarDocument(t *testing.T) {
	stamp := time.Date(2026, 10, 28, 12, 0, 0, 0, time.FixedZone("CET", 3600))
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 6, 19, 0, 0, 0, berlin)
	long := strings.Repeat("Ä", 40) + "; a, b\\c\nd\r\ne\x01"
	doc := Calendar([]Event{
		{Schedule: "SC0000000000AA", Team: "payments", Name: "Main", Start: start, End: start.Add(14 * time.Hour)},
		{Schedule: "SC0000000000BB", Team: "Payments, EU", Name: long, Start: start, End: start.Add(time.Hour)},
	}, stamp)
	if strings.Contains(string(doc), "VTIMEZONE") || strings.Contains(string(doc), "TZID") {
		t.Errorf("the document names a time zone:\n%s", doc)
	}
	events, cal := parseEvents(t, doc)
	if cal["VERSION"] != "2.0" || cal["PRODID"] != "-//Muster//Schedules//EN" || cal["X-WR-CALNAME"] != "Muster On-call" {
		t.Errorf("calendar = %v", cal)
	}
	if len(events) != 2 {
		t.Fatalf("events = %v", events)
	}
	want := event{"UID": "SC0000000000AA-20261106T180000Z@muster", "DTSTAMP": "20261028T110000Z",
		"DTSTART": "20261106T180000Z", "DTEND": "20261107T080000Z", "SUMMARY": "payments · Main"}
	if len(events[0]) != len(want) {
		t.Errorf("event = %v, want %v", events[0], want)
	}
	for k, v := range want {
		if events[0][k] != v {
			t.Errorf("%s = %q, want %q", k, events[0][k], v)
		}
	}
	summary := `Payments\, EU · ` + strings.Repeat("Ä", 40) + `\; a\, b\\c\nd\ne`
	if got := events[1]["SUMMARY"]; got != summary {
		t.Errorf("SUMMARY = %q, want %q", got, summary)
	}
	if strings.Count(string(doc), "\r\n ") == 0 {
		t.Errorf("the long summary was not folded:\n%s", doc)
	}
}

// TestWriteLine is the folding of RFC 5545 §3.1: a line of 75 octets stays whole, one of 76 is folded into 75 and a
// space with the rest, continuation lines carry at most 74 octets after their space, and a fold that would fall inside
// a UTF-8 sequence moves before it.
func TestWriteLine(t *testing.T) {
	fold := func(s string) []string {
		var b bytes.Buffer
		writeLine(&b, s)
		if !strings.HasSuffix(b.String(), "\r\n") {
			t.Fatalf("%q does not end in CRLF", b.String())
		}
		return strings.Split(strings.TrimSuffix(b.String(), "\r\n"), "\r\n")
	}
	if got := fold(strings.Repeat("a", 75)); len(got) != 1 {
		t.Errorf("75 octets = %q", got)
	}
	if got := fold(strings.Repeat("a", 76)); len(got) != 2 || len(got[0]) != 75 || got[1] != " a" {
		t.Errorf("76 octets = %q", got)
	}
	got := fold(strings.Repeat("a", 75+74+3))
	if len(got) != 3 || len(got[1]) != 75 || got[2] != " aaa" {
		t.Errorf("152 octets = %q", got)
	}
	// "·" is two octets: at 74 'a's it would take octets 75 and 76, so the first line ends before it.
	got = fold(strings.Repeat("a", 74) + "·b")
	if len(got) != 2 || got[0] != strings.Repeat("a", 74) || got[1] != " ·b" {
		t.Errorf("a fold inside · = %q", got)
	}
	for _, l := range fold(strings.Repeat("€", 60)) {
		if !utf8.ValidString(l) || len(l) > icalLineMax {
			t.Errorf("a line %q", l)
		}
	}
}

// TestEscapeText is the TEXT escaping of RFC 5545 §3.3.11, which drops the control characters TEXT does not allow.
func TestEscapeText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":             "plain",
		`a\b`:               `a\\b`,
		"a;b,c":             `a\;b\,c`,
		"a\nb\r\nc\rd":      `a\nb\nc\nd`,
		"tab\there\x00\x7f": "tab\there",
		"payments · Main":   "payments · Main",
	} {
		if got := escapeText(in); got != want {
			t.Errorf("escapeText(%q) = %q, want %q", in, got, want)
		}
	}
}
