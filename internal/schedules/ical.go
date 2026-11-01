// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"bytes"
	"strings"
	"time"
	"unicode/utf8"
)

// The iCal document of a feed (RFC 5545), written by hand: a VCALENDAR with one VEVENT per event.
const (
	icalProdID   = "-//Muster//Schedules//EN"
	icalName     = "Muster On-call"
	icalUIDHost  = "@muster"
	icalTime     = "20060102T150405Z"
	icalLineMax  = 75 // octets of a content line before it is folded, without its CRLF
	icalSummary  = " · "
	icalCRLF     = "\r\n"
	icalFoldLead = " "
)

// Event is an event of an iCal feed: a span in which the User is On-call in the Schedule, of the Team Team, named
// Name.
type Event struct {
	Schedule   string
	Team, Name string
	Start, End time.Time
}

// Calendar writes the events as an iCal document whose DTSTAMP is stamp: the times in UTC, with no VTIMEZONE, each
// event named "{Team} · {Schedule}" with the UID "{schedule public_id}-{start}@muster", which stays the same while the
// event starts at the same time; its lines end in CRLF and are folded at 75 octets.
func Calendar(events []Event, stamp time.Time) []byte {
	var b bytes.Buffer
	line := func(name, value string) { writeLine(&b, name+":"+value) }
	line("BEGIN", "VCALENDAR")
	line("VERSION", "2.0")
	line("PRODID", icalProdID)
	line("X-WR-CALNAME", escapeText(icalName))
	dtstamp := stamp.UTC().Format(icalTime)
	for _, e := range events {
		start := e.Start.UTC().Format(icalTime)
		line("BEGIN", "VEVENT")
		line("UID", escapeText(e.Schedule+"-"+start+icalUIDHost))
		line("DTSTAMP", dtstamp)
		line("DTSTART", start)
		line("DTEND", e.End.UTC().Format(icalTime))
		line("SUMMARY", escapeText(e.Team+icalSummary+e.Name))
		line("END", "VEVENT")
	}
	line("END", "VCALENDAR")
	return b.Bytes()
}

// writeLine writes a content line folded at icalLineMax octets (RFC 5545 §3.1): each continuation line starts with one
// space and the fold never splits a UTF-8 sequence, so a line may stay a few octets short of the limit.
func writeLine(b *bytes.Buffer, s string) {
	limit := icalLineMax
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString(icalCRLF)
		b.WriteString(icalFoldLead)
		s = s[cut:]
		limit = icalLineMax - len(icalFoldLead)
	}
	b.WriteString(s)
	b.WriteString(icalCRLF)
}

// textEscaper escapes a TEXT value (RFC 5545 §3.3.11): a backslash, a semicolon, a comma and a line break.
var textEscaper = strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`)

// escapeText escapes s as a TEXT value, dropping the control characters that TEXT does not allow besides the line
// breaks it escapes.
func escapeText(s string) string {
	s = textEscaper.Replace(s)
	return strings.Map(func(r rune) rune {
		if r == '\t' || (r >= 0x20 && r != 0x7f) {
			return r
		}
		return -1
	}, s)
}
