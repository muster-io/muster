// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"fmt"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the time zones of the tests, whatever the machine has
)

var (
	petr   = Person{ID: 1, PublicID: "SR00000000PETR", Name: "Petr", Login: "petr", Role: "responder", Status: "active"}
	sergey = Person{ID: 2, PublicID: "SR0000000SERGE", Name: "Sergey", Login: "sergey", Role: "responder",
		Status: "active"}
	olga = Person{ID: 3, PublicID: "SR000000000KGA", Name: "Olga", Login: "olga", Role: "responder", Status: "active"}
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func date(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// wall is the instant of a wall-clock time "2006-01-02 15:04" in loc.
func wall(loc *time.Location, s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

// shiftsText writes Shifts as "start–end login" lines in UTC.
func shiftsText(list []Shift) string {
	var b strings.Builder
	for _, s := range list {
		fmt.Fprintf(&b, "%s–%s %s\n", s.Start.UTC().Format("01-02T15:04"), s.End.UTC().Format("01-02T15:04"),
			s.User.Login)
	}
	return b.String()
}

// TestShifts is C-27.FR-2: the Shifts of a Rotation layer — a day, a week handed over on its day and 12 hours of
// wall-clock time — with limits of days alone and of a time of day, windows across midnight every day and on a Friday
// only, a start date in the future, the changes of the clocks in Europe/Berlin in March and October, at which the
// handover stays at 10:00 in Berlin, and a zone without them.
func TestShifts(t *testing.T) {
	berlin, tokyo, newYork := zone(t, "Europe/Berlin"), zone(t, "Asia/Tokyo"), zone(t, "America/New_York")
	three := []Person{petr, sergey, olga}
	for _, c := range []struct {
		name     string
		loc      *time.Location
		layer    Layer
		from, to time.Time
		want     string
	}{
		{name: "a day in UTC", loc: time.UTC,
			layer: Layer{Users: three, ShiftLength: ShiftDay, HandoverTime: "09:00", StartDate: date("2026-11-02")},
			from:  utc("2026-11-02T00:00:00Z"), to: utc("2026-11-05T00:00:00Z"),
			want: "11-02T09:00–11-03T09:00 petr\n11-03T09:00–11-04T09:00 sergey\n" +
				"11-04T09:00–11-05T00:00 olga\n"},
		{name: "a day later in the rotation", loc: time.UTC,
			layer: Layer{Users: three, ShiftLength: ShiftDay, HandoverTime: "09:00", StartDate: date("2026-11-02")},
			from:  utc("2026-11-12T12:00:00Z"), to: utc("2026-11-13T12:00:00Z"),
			want: "11-12T12:00–11-13T09:00 sergey\n11-13T09:00–11-13T12:00 olga\n"},
		{name: "a week handed over on Monday at 10:00 in Berlin", loc: berlin,
			layer: Layer{Users: three, ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
				StartDate: date("2026-11-02")},
			from: utc("2026-11-01T00:00:00Z"), to: utc("2026-11-20T00:00:00Z"),
			want: "11-02T09:00–11-09T09:00 petr\n11-09T09:00–11-16T09:00 sergey\n" +
				"11-16T09:00–11-20T00:00 olga\n"},
		{name: "a week that starts on a Thursday runs until the first Monday", loc: time.UTC,
			layer: Layer{Users: three, ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
				StartDate: date("2026-11-05")},
			from: utc("2026-11-01T00:00:00Z"), to: utc("2026-11-17T00:00:00Z"),
			want: "11-05T10:00–11-09T10:00 petr\n11-09T10:00–11-16T10:00 sergey\n" +
				"11-16T10:00–11-17T00:00 olga\n"},
		{name: "12 hours of wall-clock time", loc: berlin,
			layer: Layer{Users: []Person{petr, olga}, ShiftLength: ShiftHours, ShiftSeconds: 43200,
				HandoverTime: "09:00", StartDate: date("2026-11-02")},
			from: utc("2026-11-02T00:00:00Z"), to: utc("2026-11-04T00:00:00Z"),
			want: "11-02T08:00–11-02T20:00 petr\n11-02T20:00–11-03T08:00 olga\n" +
				"11-03T08:00–11-03T20:00 petr\n11-03T20:00–11-04T00:00 olga\n"},
		{name: "12 hours across the change of the clocks in October", loc: berlin,
			layer: Layer{Users: []Person{petr, olga}, ShiftLength: ShiftHours, ShiftSeconds: 43200,
				HandoverTime: "09:00", StartDate: date("2026-10-01")},
			from: utc("2026-10-24T12:00:00Z"), to: utc("2026-10-25T12:00:00Z"),
			want: "10-24T12:00–10-24T19:00 petr\n10-24T19:00–10-25T08:00 olga\n" +
				"10-25T08:00–10-25T12:00 petr\n"},
		{name: "the handover stays at 10:00 in Berlin across the change of the clocks in March", loc: berlin,
			layer: Layer{Users: three, ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
				StartDate: date("2027-03-01")},
			from: utc("2027-03-21T00:00:00Z"), to: utc("2027-03-31T00:00:00Z"),
			want: "03-21T00:00–03-22T09:00 olga\n03-22T09:00–03-29T08:00 petr\n" +
				"03-29T08:00–03-31T00:00 sergey\n"},
		{name: "the handover stays at 10:00 in Berlin across the change of the clocks in October", loc: berlin,
			layer: Layer{Users: three, ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
				StartDate: date("2026-10-19")},
			from: utc("2026-10-19T00:00:00Z"), to: utc("2026-10-28T00:00:00Z"),
			want: "10-19T08:00–10-26T09:00 petr\n10-26T09:00–10-28T00:00 sergey\n"},
		{name: "a zone without changes of the clocks", loc: tokyo,
			layer: Layer{Users: three, ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
				StartDate: date("2027-03-01")},
			from: utc("2027-03-21T00:00:00Z"), to: utc("2027-03-31T00:00:00Z"),
			want: "03-21T00:00–03-22T01:00 olga\n03-22T01:00–03-29T01:00 petr\n" +
				"03-29T01:00–03-31T00:00 sergey\n"},
		{name: "hours across the change of the clocks in March in Berlin: 02:00 is 03:00", loc: berlin,
			layer: Layer{Users: three, ShiftLength: ShiftHours, ShiftSeconds: 3600, HandoverTime: "00:00",
				StartDate: date("2027-03-01")},
			from: utc("2027-03-28T00:00:00Z"), to: utc("2027-03-28T03:00:00Z"),
			want: "03-28T00:00–03-28T01:00 sergey\n03-28T01:00–03-28T02:00 petr\n03-28T02:00–03-28T03:00 sergey\n"},
		{name: "hours across the change of the clocks in March in New York: 02:00 is 03:00", loc: newYork,
			layer: Layer{Users: three, ShiftLength: ShiftHours, ShiftSeconds: 3600, HandoverTime: "00:00",
				StartDate: date("2027-03-01")},
			from: utc("2027-03-14T05:00:00Z"), to: utc("2027-03-14T09:00:00Z"),
			want: "03-14T05:00–03-14T06:00 petr\n03-14T06:00–03-14T07:00 sergey\n03-14T07:00–03-14T08:00 petr\n" +
				"03-14T08:00–03-14T09:00 sergey\n"},
		{name: "a daily handover at 02:30 in New York moves to 03:00 on the day of the change", loc: newYork,
			layer: Layer{Users: three, ShiftLength: ShiftDay, HandoverTime: "02:30", StartDate: date("2027-03-01")},
			from:  utc("2027-03-13T00:00:00Z"), to: utc("2027-03-16T00:00:00Z"),
			want: "03-13T00:00–03-13T07:30 olga\n03-13T07:30–03-14T07:00 petr\n03-14T07:00–03-15T06:30 sergey\n" +
				"03-15T06:30–03-16T00:00 olga\n"},
		{name: "a window that starts in the skipped hour starts when the clocks move", loc: newYork,
			layer: Layer{Users: []Person{olga}, ShiftLength: ShiftWeek, HandoverTime: "00:00", HandoverDay: "monday",
				StartDate: date("2027-03-01"), Limits: &Limits{Days: []string{"sunday"}, Start: "02:30", End: "04:00"}},
			from: utc("2027-03-14T00:00:00Z"), to: utc("2027-03-15T00:00:00Z"),
			want: "03-14T07:00–03-14T08:00 olga\n"},
		{name: "a far period: hours in the year 2400", loc: berlin,
			layer: Layer{Users: three, ShiftLength: ShiftHours, ShiftSeconds: 3600, HandoverTime: "00:00",
				StartDate: date("2027-03-01")},
			from: utc("2400-01-01T00:00:00Z"), to: utc("2400-01-01T03:00:00Z"),
			want: "01-01T00:00–01-01T01:00 sergey\n01-01T01:00–01-01T02:00 olga\n01-01T02:00–01-01T03:00 petr\n"},
		{name: "an old start date: weeks from the year 1", loc: time.UTC,
			layer: Layer{Users: three, ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
				StartDate: date("0001-01-01")},
			from: utc("2026-11-02T00:00:00Z"), to: utc("2026-11-10T00:00:00Z"),
			want: "11-02T00:00–11-02T10:00 petr\n11-02T10:00–11-09T10:00 sergey\n11-09T10:00–11-10T00:00 olga\n"},
		{name: "a start date in the future", loc: time.UTC,
			layer: Layer{Users: three, ShiftLength: ShiftDay, HandoverTime: "09:00", StartDate: date("2026-12-01")},
			from:  utc("2026-11-29T00:00:00Z"), to: utc("2026-12-02T12:00:00Z"),
			want: "12-01T09:00–12-02T09:00 petr\n12-02T09:00–12-02T12:00 sergey\n"},
		{name: "before the start date", loc: time.UTC,
			layer: Layer{Users: three, ShiftLength: ShiftDay, HandoverTime: "09:00", StartDate: date("2026-12-01")},
			from:  utc("2026-11-01T00:00:00Z"), to: utc("2026-11-30T00:00:00Z"), want: ""},
		{name: "limits of days alone are whole days, joined where they touch", loc: time.UTC,
			layer: Layer{Users: three, ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
				StartDate: date("2026-11-02"), Limits: &Limits{Days: []string{"saturday", "sunday"}}},
			from: utc("2026-11-02T00:00:00Z"), to: utc("2026-11-16T00:00:00Z"),
			want: "11-07T00:00–11-09T00:00 petr\n11-14T00:00–11-16T00:00 sergey\n"},
		{name: "limits of weekdays from 11:00 to 19:00", loc: berlin,
			layer: Layer{Users: []Person{olga, petr}, ShiftLength: ShiftWeek, HandoverTime: "11:00",
				HandoverDay: "monday", StartDate: date("2026-11-02"), Limits: &Limits{Days: []string{"monday",
					"tuesday", "wednesday", "thursday", "friday"}, Start: "11:00", End: "19:00"}},
			from: utc("2026-11-02T00:00:00Z"), to: utc("2026-11-10T00:00:00Z"),
			want: "11-02T10:00–11-02T18:00 olga\n11-03T10:00–11-03T18:00 olga\n11-04T10:00–11-04T18:00 olga\n" +
				"11-05T10:00–11-05T18:00 olga\n11-06T10:00–11-06T18:00 olga\n11-09T10:00–11-09T18:00 petr\n"},
		{name: "a window across midnight every day", loc: time.UTC,
			layer: Layer{Users: []Person{petr, olga}, ShiftLength: ShiftDay, HandoverTime: "12:00",
				StartDate: date("2026-11-02"), Limits: &Limits{Days: Weekdays, Start: "19:00", End: "09:00"}},
			from: utc("2026-11-02T00:00:00Z"), to: utc("2026-11-04T12:00:00Z"),
			want: "11-02T19:00–11-03T09:00 petr\n11-03T19:00–11-04T09:00 olga\n"},
		{name: "a window across midnight on Friday belongs to Friday", loc: berlin,
			layer: Layer{Users: []Person{olga}, ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
				StartDate: date("2026-11-02"), Limits: &Limits{Days: []string{"friday"}, Start: "19:00", End: "09:00"}},
			from: utc("2026-11-01T00:00:00Z"), to: utc("2026-11-16T00:00:00Z"),
			want: "11-06T18:00–11-07T08:00 olga\n11-13T18:00–11-14T08:00 olga\n"},
		{name: "a window across a handover is cut at it", loc: time.UTC,
			layer: Layer{Users: []Person{petr, olga}, ShiftLength: ShiftDay, HandoverTime: "06:00",
				StartDate: date("2026-11-02"), Limits: &Limits{Days: []string{"monday"}, Start: "22:00", End: "08:00"}},
			from: utc("2026-11-02T00:00:00Z"), to: utc("2026-11-04T00:00:00Z"),
			want: "11-02T22:00–11-03T06:00 petr\n11-03T06:00–11-03T08:00 olga\n"},
		{name: "no people", loc: time.UTC,
			layer: Layer{ShiftLength: ShiftDay, HandoverTime: "09:00", StartDate: date("2026-11-02")},
			from:  utc("2026-11-02T00:00:00Z"), to: utc("2026-11-05T00:00:00Z"), want: ""},
		{name: "an empty period", loc: time.UTC,
			layer: Layer{Users: three, ShiftLength: ShiftDay, HandoverTime: "09:00", StartDate: date("2026-11-02")},
			from:  utc("2026-11-05T00:00:00Z"), to: utc("2026-11-05T00:00:00Z"), want: ""},
		{name: "hours without a length", loc: time.UTC,
			layer: Layer{Users: three, ShiftLength: ShiftHours, HandoverTime: "09:00", StartDate: date("2026-11-02")},
			from:  utc("2026-11-02T00:00:00Z"), to: utc("2026-11-05T00:00:00Z"), want: ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := shiftsText(c.layer.Shifts(c.loc, c.from, c.to)); got != c.want {
				t.Errorf("Shifts =\n%swant\n%s", got, c.want)
			}
		})
	}
}

// TestFridayNight is C-27.FR-2 at moments: with Friday 19:00–09:00 in Berlin, the person is on Shift on Friday at 23:00
// and on Saturday at 08:00, and not on Saturday at 10:00 or on Thursday at 23:00.
func TestFridayNight(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	l := Layer{Users: []Person{olga}, ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday",
		StartDate: date("2026-11-02"), Limits: &Limits{Days: []string{"friday"}, Start: "19:00", End: "09:00"}}
	for at, want := range map[string]bool{"2026-11-06 23:00": true, "2026-11-07 08:00": true,
		"2026-11-07 10:00": false, "2026-11-05 23:00": false} {
		moment := wall(berlin, at)
		if got := len(l.Shifts(berlin, moment, moment.Add(time.Minute))) == 1; got != want {
			t.Errorf("on Shift at %s = %v, want %v", at, got, want)
		}
	}
}

func TestClockOf(t *testing.T) {
	if h, m := clockOf("23:59"); h != 23 || m != 59 {
		t.Errorf("23:59 = %d:%d", h, m)
	}
	if h, m := clockOf("9:00"); h != 0 || m != 0 {
		t.Errorf("a malformed time = %d:%d", h, m)
	}
}
