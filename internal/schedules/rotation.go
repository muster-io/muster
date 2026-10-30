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

// Layer is a Rotation layer: its people take Shifts in turn from StartDate at HandoverTime, the first person first.
// Number is its position, 1 for the lowest.
type Layer struct {
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

// at is the instant of the wall-clock time hh:mm on the day y-m-d (d may overflow the month) in loc, by its rules on
// that day.
func at(loc *time.Location, y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, loc)
}

// handover is the instant of the k-th handover of the layer in loc: the start of its k-th Shift, k from 0. Weekly
// Shifts are handed over on HandoverDay: the first one runs from the start until the first HandoverDay after
// StartDate. Shifts in hours last ShiftSeconds of wall-clock time.
func (l Layer) handover(loc *time.Location, k int) time.Time {
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
		wall := time.Date(y, m, d, hh, mm, 0, 0, time.UTC).Add(time.Duration(k) * time.Duration(l.ShiftSeconds) *
			time.Second)
		return at(loc, wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute())
	default:
		return at(loc, y, m, d+k, hh, mm)
	}
}

// firstShift is the index of a Shift that starts no later than t, close before it.
func (l Layer) firstShift(loc *time.Location, t time.Time) int {
	w := t.In(loc)
	wall := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), 0, 0, time.UTC)
	y, m, d := l.StartDate.Date()
	hh, mm := clockOf(l.HandoverTime)
	elapsed := wall.Sub(time.Date(y, m, d, hh, mm, 0, 0, time.UTC))
	var k int
	switch l.ShiftLength {
	case ShiftWeek:
		k = int(elapsed / (7 * 24 * time.Hour))
	case ShiftHours:
		k = int(elapsed / (time.Duration(l.ShiftSeconds) * time.Second))
	default:
		k = int(elapsed / (24 * time.Hour))
	}
	return max(k-2, 0)
}

// Shifts are the Shifts of the layer that overlap the period from from to to in the time zone loc, cut to its limits
// and to the period, in order. A layer without people has none.
func (l Layer) Shifts(loc *time.Location, from, to time.Time) []Shift {
	if len(l.Users) == 0 || !to.After(from) || (l.ShiftLength == ShiftHours && l.ShiftSeconds <= 0) {
		return nil
	}
	var out []Shift
	for k := l.firstShift(loc, from); ; k++ {
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
// touch.
func (l Layer) cut(loc *time.Location, start, end time.Time) []span {
	if l.Limits == nil {
		return []span{{start, end}}
	}
	var out []span
	first := start.In(loc)
	y, m, d := first.Date()
	// A window that crosses midnight starts on the day before.
	for day := d - 1; ; day++ {
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
func (l Layer) window(loc *time.Location, date time.Time) span {
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
