// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"bytes"
	"strings"
	"time"
	"unicode/utf8"
)

// The iCal document of a feed (RFC 5545), written by hand: a VCALENDAR with a VTIMEZONE of UTC, so that it has a
// component even without events, and one VEVENT per event.
const (
	icalProdID   = "-//Muster//Schedules//EN"
	icalName     = "Muster On-call"
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

// Calendar writes the events of the User user (a public_id) as an iCal document whose DTSTAMP is stamp: the times in
// UTC, each event named "{Team} · {Schedule}", transparent — being On-call is not being busy — with the UID
// "{schedule public_id}-{user public_id}-{start}@{host}", which stays the same while the event starts at the same
// time and differs between the feeds of two Users; host is the host of MUSTER_PUBLIC_URL. The VTIMEZONE of UTC,
// which no time refers to, keeps a calendar without events valid: RFC 5545 asks for one component at least. Its
// lines end in CRLF and are folded at 75 octets.
func Calendar(events []Event, user, host string, stamp time.Time) []byte {
	var b bytes.Buffer
	line := func(name, value string) { writeLine(&b, name+":"+value) }
	line("BEGIN", "VCALENDAR")
	line("VERSION", "2.0")
	line("PRODID", icalProdID)
	line("X-WR-CALNAME", escapeText(icalName))
	line("BEGIN", "VTIMEZONE")
	line("TZID", "UTC")
	line("BEGIN", "STANDARD")
	line("DTSTART", "19700101T000000")
	line("TZOFFSETFROM", "+0000")
	line("TZOFFSETTO", "+0000")
	line("END", "STANDARD")
	line("END", "VTIMEZONE")
	dtstamp := stamp.UTC().Format(icalTime)
	for _, e := range events {
		start := e.Start.UTC().Format(icalTime)
		line("BEGIN", "VEVENT")
		line("UID", escapeText(e.Schedule+"-"+user+"-"+start+"@"+host))
		line("DTSTAMP", dtstamp)
		line("DTSTART", start)
		line("DTEND", e.End.UTC().Format(icalTime))
		line("SUMMARY", escapeText(e.Team+icalSummary+e.Name))
		line("TRANSP", "TRANSPARENT")
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
