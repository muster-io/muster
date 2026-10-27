// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// WorkingHours are days of the week, a start and an end on the 24-hour clock and an IANA time zone (C-25.FR-9): the
// Organization's (organization.working_hours) or a Team's own (team.working_hours). The end is later than the start
// on the same day, so Working hours never span midnight (P-63).
type WorkingHours struct {
	Days     []string `json:"days"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	TimeZone string   `json:"time_zone"`
}

// Weekdays are the days of the week as the API and the CHECKs name them, Monday first.
var Weekdays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

// timeOfDay is HH:MM on the 24-hour clock, the pattern of the API and of the CHECKs.
var timeOfDay = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// maxTimeZoneLength bounds the name of a time zone, as for the Organization.
const maxTimeZoneLength = 64

// Validate refuses Working hours that are not valid, at the pointer base of the request body: no day, a day that is
// not a weekday or is given twice, a start or an end that is not HH:MM, an end not later than the start (out_of_range
// at the end) and a time zone that is not an IANA time zone. It returns the hours with their days in the order of the
// week.
func (w WorkingHours) Validate(base string) (WorkingHours, error) {
	if len(w.Days) == 0 {
		return w, &FieldError{Pointer: base + "/days", Code: CodeRequired, Detail: "Working hours need at least one day."}
	}
	seen := map[string]bool{}
	for i, d := range w.Days {
		pointer := base + "/days/" + strconv.Itoa(i)
		switch {
		case !slices.Contains(Weekdays, d):
			return w, &FieldError{Pointer: pointer, Code: CodeInvalidFormat,
				Detail: "The day is not a day from monday to sunday."}
		case seen[d]:
			return w, &FieldError{Pointer: pointer, Code: CodeDuplicate, Detail: "The day appears twice."}
		}
		seen[d] = true
	}
	switch {
	case !timeOfDay.MatchString(w.Start):
		return w, &FieldError{Pointer: base + "/start", Code: CodeInvalidFormat,
			Detail: "The start is not a time HH:MM on the 24-hour clock, such as 09:00."}
	case !timeOfDay.MatchString(w.End):
		return w, &FieldError{Pointer: base + "/end", Code: CodeInvalidFormat,
			Detail: "The end is not a time HH:MM on the 24-hour clock, such as 18:00."}
	case w.End <= w.Start:
		return w, &FieldError{Pointer: base + "/end", Code: CodeOutOfRange,
			Detail: "Working hours end later than they start on the same day; they cannot span midnight."}
	case !ValidTimeZone(w.TimeZone):
		return w, &FieldError{Pointer: base + "/time_zone", Code: CodeInvalidFormat,
			Detail: "The time zone is not an IANA time zone such as Europe/Berlin."}
	}
	out := w
	out.Days = make([]string, 0, len(w.Days))
	for _, d := range Weekdays {
		if seen[d] {
			out.Days = append(out.Days, d)
		}
	}
	return out, nil
}

// ValidTimeZone reports whether name is an IANA time zone that time.LoadLocation knows, never Local, the zone of the
// server.
func ValidTimeZone(name string) bool {
	if name == "" || name == "Local" || len(name) > maxTimeZoneLength {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// Effective are the Working hours that apply to the Team t: its own when it has them, else the Organization's org.
func Effective(t Team, org WorkingHours) WorkingHours {
	if t.Own != nil {
		return *t.Own
	}
	return org
}

// Contains reports whether the instant at falls in the Working hours: on one of their days, at or after their start
// and before their end, in their time zone. Hours whose time zone does not load contain nothing.
func (w WorkingHours) Contains(at time.Time) bool {
	loc, err := time.LoadLocation(w.TimeZone)
	if err != nil {
		return false
	}
	local := at.In(loc)
	if !slices.Contains(w.Days, strings.ToLower(local.Weekday().String())) {
		return false
	}
	clock := local.Format("15:04")
	return clock >= w.Start && clock < w.End
}
