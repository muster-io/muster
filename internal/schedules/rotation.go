// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"slices"
	"time"
)

// The Shift lengths of a Rotation layer.
const (
	ShiftDay   = "day"
	ShiftWeek  = "week"
	ShiftHours = "hours"
)

// Weekdays are the days of the week as the API and the CHECKs name them, Monday first.
var Weekdays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

// weekdayOf is the time.Weekday of a day named as in Weekdays.
var weekdayOf = map[string]time.Weekday{"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday}

// Limits cut the Shifts of a Rotation layer to some days of the week and, when Start and End are set, a time of day,
// HH:MM. An End earlier than Start crosses midnight: each window runs from Start on a day it names until End on the
// next day, so it belongs to the day it starts on.
type Limits struct {
	Days  []string `json:"days"`
	Start string   `json:"start,omitempty"`
	End   string   `json:"end,omitempty"`
}

// Person is a User as a Schedule names them, with what decides whether they may be Paged.
type Person struct {
	ID       int64
	PublicID string
	Name     string
	Login    string
	Role     string
	Status   string
}

// RotationLayer is a Rotation layer: its people take Shifts in turn from StartDate at HandoverTime, the first person
// first. Number is its position, 1 for the lowest.
type RotationLayer struct {
	ID           int64
	PublicID     string
	Number       int
	Name         string
	Users        []Person
	ShiftLength  string
	ShiftSeconds int
	HandoverTime string
	HandoverDay  string
	// StartDate is a calendar date, as midnight UTC.
	StartDate time.Time
	Limits    *Limits
}

// Shift is the time one person of a Rotation layer is on Shift, cut to the limits of the layer.
type Shift struct {
	Start, End time.Time
	User       Person
}

// clock is a time of day HH:MM as hours and minutes; a malformed one is midnight, which validation never lets through.
func clockOf(hhmm string) (int, int) {
	if len(hhmm) != len("15:04") {
		return 0, 0
	}
	return int(hhmm[0]-'0')*10 + int(hhmm[1]-'0'), int(hhmm[3]-'0')*10 + int(hhmm[4]-'0')
}

// The lengths of the Shifts in seconds of wall-clock time.
const (
	daySeconds  = 24 * 60 * 60
	weekSeconds = 7 * daySeconds
)

// at is the instant of the wall-clock time hh:mm on the day y-m-d (d may overflow the month) in loc, by its rules on
// that day, the same in every zone, where time.Date may choose either side: a wall-clock time that the clocks skip,
// when they move forward, is the instant they move, the first instant whose wall-clock time is not earlier; a
// wall-clock time that happens twice, when they move back, is its first occurrence (RFC 5545).
func at(loc *time.Location, y int, m time.Month, d, hh, mm int) time.Time {
	t := time.Date(y, m, d, hh, mm, 0, 0, loc)
	want := time.Date(y, m, d, hh, mm, 0, 0, time.UTC)
	got := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	switch {
	case got.Before(want):
		// time.Date went back to the zone before the change: the change is where that zone ends.
		if _, change := t.ZoneBounds(); !change.IsZero() {
			return change
		}
		return t
	case got.After(want):
		// time.Date went on into the zone after the change: the change is where that zone starts.
		if change, _ := t.ZoneBounds(); !change.IsZero() {
			return change
		}
		return t
	}
	// The same wall-clock time in the zone before the one of t, when the clocks moved back into it less than that
	// zone's lead ago, is the first occurrence.
	start, _ := t.ZoneBounds()
	if start.IsZero() {
		return t
	}
	_, now := t.Zone()
	_, before := start.Add(-time.Second).Zone()
	lead := time.Duration(before-now) * time.Second
	if lead > 0 && t.Sub(start) < lead {
		return t.Add(-lead)
	}
	return t
}

// wallSeconds is the wall-clock time of t in loc as seconds since the Unix epoch read as UTC, so that two of them differ
// by the wall-clock time between them, whatever the changes of the clocks.
func wallSeconds(loc *time.Location, t time.Time) int64 {
	w := t.In(loc)
	return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), 0, 0, time.UTC).Unix()
}

// length is the length of a Shift of the layer in seconds of wall-clock time; the first weekly Shift may be shorter.
func (l RotationLayer) length() int64 {
	switch l.ShiftLength {
	case ShiftWeek:
		return weekSeconds
	case ShiftHours:
		return int64(l.ShiftSeconds)
	default:
		return daySeconds
	}
}

// handover is the instant of the k-th handover of the layer in loc: the start of its k-th Shift, k from 0. Weekly
// Shifts are handed over on HandoverDay: the first one runs from the start until the first HandoverDay after
// StartDate. Shifts in hours last ShiftSeconds of wall-clock time. The arithmetic is in calendar days and whole seconds.
func (l RotationLayer) handover(loc *time.Location, k int) time.Time {
	y, m, d := l.StartDate.Date()
	hh, mm := clockOf(l.HandoverTime)
	switch l.ShiftLength {
	case ShiftWeek:
		if k == 0 {
			return at(loc, y, m, d, hh, mm)
		}
		first := (int(weekdayOf[l.HandoverDay]) - int(l.StartDate.Weekday()) + 7) % 7
		if first == 0 {
			first = 7
		}
		return at(loc, y, m, d+first+7*(k-1), hh, mm)
	case ShiftHours:
		base := time.Date(y, m, d, hh, mm, 0, 0, time.UTC).Unix()
		wall := time.Unix(base+int64(k)*int64(l.ShiftSeconds), 0).UTC()
		return at(loc, wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute())
	default:
		return at(loc, y, m, d+k, hh, mm)
	}
}

// firstShift is the index of a Shift that starts no later than t, close before it: two before the one whose wall-clock
// start the elapsed wall-clock time points at, which a change of the clocks moves by an hour at most.
func (l RotationLayer) firstShift(loc *time.Location, t time.Time) int {
	y, m, d := l.StartDate.Date()
	hh, mm := clockOf(l.HandoverTime)
	elapsed := wallSeconds(loc, t) - time.Date(y, m, d, hh, mm, 0, 0, time.UTC).Unix()
	if elapsed <= 0 {
		return 0
	}
	return int(max(elapsed/l.length()-2, 0))
}

// Shifts are the Shifts of the layer that overlap the period from from to to in the time zone loc, cut to its limits
// and to the period, in order. A layer without people has none. Every Shift lasts at least an hour, so the loop takes
// at most one step per hour of the period, and a few more.
func (l RotationLayer) Shifts(loc *time.Location, from, to time.Time) []Shift {
	if len(l.Users) == 0 || !to.After(from) || (l.ShiftLength == ShiftHours && l.ShiftSeconds < 3600) {
		return nil
	}
	var out []Shift
	first := l.firstShift(loc, from)
	steps := int((to.Unix()-from.Unix())/3600) + 8
	for k := first; k < first+steps; k++ {
		start, end := l.handover(loc, k), l.handover(loc, k+1)
		if !start.Before(to) {
			break
		}
		if !end.After(from) || !end.After(start) {
			continue
		}
		user := l.Users[k%len(l.Users)]
		for _, p := range l.cut(loc, start, end) {
			p.Start, p.End = later(p.Start, from), earlier(p.End, to)
			if p.End.After(p.Start) {
				out = append(out, Shift{Start: p.Start, End: p.End, User: user})
			}
		}
	}
	return out
}

// span is a span of time.
type span struct{ Start, End time.Time }

// cut is the span from start to end cut to the limits of the layer: the parts within its windows, joined where they
// touch. It looks at the windows of each day from the day before start, since a window that crosses midnight starts
// then, to the day of end.
func (l RotationLayer) cut(loc *time.Location, start, end time.Time) []span {
	if l.Limits == nil {
		return []span{{start, end}}
	}
	var out []span
	first := start.In(loc)
	y, m, d := first.Date()
	days := int((end.Unix()-start.Unix())/daySeconds) + 2
	for day := d - 1; day <= d+days; day++ {
		date := time.Date(y, m, day, 12, 0, 0, 0, loc)
		ws := l.window(loc, date)
		if !ws.Start.Before(end) {
			break
		}
		if !slices.Contains(l.Limits.Days, Weekdays[(int(date.Weekday())+6)%7]) {
			continue
		}
		s, e := later(ws.Start, start), earlier(ws.End, end)
		if !e.After(s) {
			continue
		}
		if n := len(out); n > 0 && out[n-1].End.Equal(s) {
			out[n-1].End = e
			continue
		}
		out = append(out, span{s, e})
	}
	return out
}

// window is the window of the limits that starts on the day of date: the whole day without a time of day, else from
// Start until End that day, or the next day when End is not later than Start.
func (l RotationLayer) window(loc *time.Location, date time.Time) span {
	y, m, d := date.Date()
	if l.Limits.Start == "" {
		return span{at(loc, y, m, d, 0, 0), at(loc, y, m, d+1, 0, 0)}
	}
	sh, sm := clockOf(l.Limits.Start)
	eh, em := clockOf(l.Limits.End)
	endDay := d
	if l.Limits.End <= l.Limits.Start {
		endDay++
	}
	return span{at(loc, y, m, d, sh, sm), at(loc, y, m, endDay, eh, em)}
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
