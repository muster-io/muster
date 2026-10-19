// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"slices"
	"testing"
	"time"
	_ "time/tzdata" // the time zones of the tests, whatever the host ships
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func instant(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// TestSnoozePresetEnd is C-10.AC-21: every row of its table (Europe/Berlin, 09:00, Monday) and its two
// America/New_York cases, both clock changes of 2026 in Europe/Berlin — a start the clocks skip moves forward by the
// change, one they show twice is the first — a zone with a 45-minute offset, zones far from UTC, whose date differs
// from UTC's, and UTC itself, on fixed instants.
func TestSnoozePresetEnd(t *testing.T) {
	berlin, newYork, utc := zone(t, "Europe/Berlin"), zone(t, "America/New_York"), time.UTC
	nine := WorkdayStart{Hour: 9}
	for _, c := range []struct {
		name               string
		loc                *time.Location
		start              WorkdayStart
		now                string
		tomorrow, weekFrom string
	}{
		{"Berlin Tue 02:00: today, 07:00Z", berlin, nine, "2026-10-20T02:00:00+02:00",
			"2026-10-20T07:00:00Z", "2026-10-26T08:00:00Z"},
		{"Berlin Tue 08:59: today", berlin, nine, "2026-10-20T08:59:00+02:00",
			"2026-10-20T09:00:00+02:00", "2026-10-26T09:00:00+01:00"},
		{"Berlin Tue 09:00 sharp: the next day", berlin, nine, "2026-10-20T09:00:00+02:00",
			"2026-10-21T09:00:00+02:00", "2026-10-26T09:00:00+01:00"},
		{"Berlin Tue 23:30", berlin, nine, "2026-10-20T23:30:00+02:00",
			"2026-10-21T09:00:00+02:00", "2026-10-26T09:00:00+01:00"},
		{"Berlin Fri evening: Saturday is an ordinary day", berlin, nine, "2026-10-23T18:00:00+02:00",
			"2026-10-24T09:00:00+02:00", "2026-10-26T09:00:00+01:00"},
		{"Berlin Sat 10:00 across the clocks going back", berlin, nine, "2026-10-24T08:00:00Z",
			"2026-10-25T08:00:00Z", "2026-10-26T08:00:00Z"},
		{"Berlin Sun 12:00", berlin, nine, "2026-10-25T12:00:00+01:00",
			"2026-10-26T09:00:00+01:00", "2026-10-26T09:00:00+01:00"},
		{"Berlin Mon 08:00: the first day of the week, before the start", berlin, nine, "2026-10-26T08:00:00+01:00",
			"2026-10-26T09:00:00+01:00", "2026-10-26T09:00:00+01:00"},
		{"Berlin Mon 09:30: the next week", berlin, nine, "2026-10-26T09:30:00+01:00",
			"2026-10-27T09:00:00+01:00", "2026-11-02T09:00:00+01:00"},
		{"New York 8 Mar, 02:30 skipped: 03:30 EDT", newYork, WorkdayStart{Hour: 2, Minute: 30},
			"2026-03-08T00:00:00-05:00", "2026-03-08T07:30:00Z", "2026-03-09T02:30:00-04:00"},
		{"New York 1 Nov, 01:30 twice: the first", newYork, WorkdayStart{Hour: 1, Minute: 30},
			"2026-11-01T00:00:00-04:00", "2026-11-01T05:30:00Z", "2026-11-02T01:30:00-05:00"},
		{"Berlin 29 Mar, 02:30 skipped: 03:30 CEST", berlin, WorkdayStart{Hour: 2, Minute: 30},
			"2026-03-29T00:00:00+01:00", "2026-03-29T01:30:00Z", "2026-03-30T02:30:00+02:00"},
		{"Berlin 25 Oct, 02:30 twice: the first, CEST", berlin, WorkdayStart{Hour: 2, Minute: 30},
			"2026-10-25T00:00:00+02:00", "2026-10-25T00:30:00Z", "2026-10-26T02:30:00+01:00"},
		{"Berlin 25 Oct 02:45 CEST, after the first 02:30: the next day", berlin, WorkdayStart{Hour: 2, Minute: 30},
			"2026-10-25T02:45:00+02:00", "2026-10-26T02:30:00+01:00", "2026-10-26T02:30:00+01:00"},
		{"Kathmandu 08:45", zone(t, "Asia/Kathmandu"), nine, "2026-10-20T08:45:00+05:45",
			"2026-10-20T03:15:00Z", "2026-10-26T09:00:00+05:45"},
		{"Kathmandu 09:00 sharp", zone(t, "Asia/Kathmandu"), nine, "2026-10-20T09:00:00+05:45",
			"2026-10-21T09:00:00+05:45", "2026-10-26T09:00:00+05:45"},
		{"Kiritimati, a day ahead of UTC", zone(t, "Pacific/Kiritimati"), nine, "2026-10-20T18:00:00Z",
			"2026-10-20T19:00:00Z", "2026-10-25T19:00:00Z"},
		{"Pago Pago, a day behind UTC, Monday evening", zone(t, "Pacific/Pago_Pago"), nine, "2026-10-20T05:00:00Z",
			"2026-10-20T20:00:00Z", "2026-10-26T20:00:00Z"},
		{"UTC 02:00: the same day", utc, nine, "2026-10-20T02:00:00Z",
			"2026-10-20T09:00:00Z", "2026-10-26T09:00:00Z"},
		{"UTC 08:59", utc, nine, "2026-10-20T08:59:00Z", "2026-10-20T09:00:00Z", "2026-10-26T09:00:00Z"},
		{"UTC 09:00 sharp", utc, nine, "2026-10-20T09:00:00Z", "2026-10-21T09:00:00Z", "2026-10-26T09:00:00Z"},
		{"UTC 09:00 and a nanosecond", utc, nine, "2026-10-20T09:00:00.000000001Z",
			"2026-10-21T09:00:00Z", "2026-10-26T09:00:00Z"},
		{"UTC Friday evening", utc, nine, "2026-10-23T21:00:00Z", "2026-10-24T09:00:00Z", "2026-10-26T09:00:00Z"},
		{"UTC Sunday", utc, nine, "2026-10-25T10:00:00Z", "2026-10-26T09:00:00Z", "2026-10-26T09:00:00Z"},
		{"UTC at the end of a month", utc, WorkdayStart{Hour: 23, Minute: 59}, "2026-12-31T23:59:00Z",
			"2027-01-01T23:59:00Z", "2027-01-04T23:59:00Z"},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := instant(t, c.now)
			for _, w := range []struct {
				preset SnoozePreset
				want   string
			}{{PresetTomorrow, c.tomorrow}, {PresetWeekStart, c.weekFrom}} {
				got, err := SnoozePresetEnd(w.preset, now, c.loc, c.start, time.Monday)
				if err != nil || !got.Equal(instant(t, w.want)) || got.Location() != time.UTC {
					t.Errorf("%s from %s = %s, %v; want %s", w.preset, c.now, got, err, w.want)
				}
			}
		})
	}
	// The clocks go back on 25 October: from Saturday 10:00 to Monday 09:00 is 48 hours, not 47.
	now := instant(t, "2026-10-24T10:00:00+02:00")
	if end, _ := SnoozePresetEnd(PresetWeekStart, now, berlin, nine, time.Monday); end.Sub(now) != 48*time.Hour {
		t.Errorf("Saturday 10:00 to Monday 09:00 = %s", end.Sub(now))
	}
}

// TestSnoozePresetEndWeekStart: week_start ends on each of the seven first days of the week, on the day itself when its
// start is still ahead, and a week later once it has passed; 08:30 with sunday is C-20.AC-7.
func TestSnoozePresetEndWeekStart(t *testing.T) {
	now := instant(t, "2026-10-21T12:00:00Z") // a Wednesday
	nine := WorkdayStart{Hour: 9}
	for _, c := range []struct {
		weekStart time.Weekday
		start     WorkdayStart
		now, want string
	}{
		{time.Monday, nine, "", "2026-10-26T09:00:00Z"},
		{time.Tuesday, nine, "", "2026-10-27T09:00:00Z"},
		{time.Wednesday, nine, "", "2026-10-28T09:00:00Z"},
		{time.Thursday, nine, "", "2026-10-22T09:00:00Z"},
		{time.Friday, nine, "", "2026-10-23T09:00:00Z"},
		{time.Saturday, nine, "", "2026-10-24T09:00:00Z"},
		{time.Sunday, nine, "", "2026-10-25T09:00:00Z"},
		{time.Sunday, WorkdayStart{Hour: 8, Minute: 30}, "", "2026-10-25T08:30:00Z"},
		{time.Wednesday, nine, "2026-10-21T08:00:00Z", "2026-10-21T09:00:00Z"},
		{time.Wednesday, nine, "2026-10-21T09:00:00Z", "2026-10-28T09:00:00Z"},
	} {
		at := now
		if c.now != "" {
			at = instant(t, c.now)
		}
		got, err := SnoozePresetEnd(PresetWeekStart, at, time.UTC, c.start, c.weekStart)
		if err != nil || !got.Equal(instant(t, c.want)) {
			t.Errorf("%s %02d:%02d from %s = %s, %v; want %s", c.weekStart, c.start.Hour, c.start.Minute, at, got,
				err, c.want)
		}
	}
	if _, err := SnoozePresetEnd("someday", now, time.UTC, nine, time.Monday); err == nil {
		t.Error("an unknown preset has an end")
	}
	if _, err := SnoozePresetEnd(PresetWeekStart, now, time.UTC, nine, time.Weekday(7)); err == nil {
		t.Error("an unknown first day of the week has an end")
	}
	if got := PresetOrder(); !slices.Equal(got, []SnoozePreset{PresetTomorrow, PresetWeekStart}) {
		t.Errorf("PresetOrder = %v", got)
	}
}

// TestParseSnoozePresetSettings: the stored organization.workday_start and organization.week_start.
func TestParseSnoozePresetSettings(t *testing.T) {
	for s, want := range map[string]WorkdayStart{
		"00:00": {}, "09:00": {Hour: 9}, "08:30": {Hour: 8, Minute: 30}, "23:59": {Hour: 23, Minute: 59},
	} {
		if got, err := ParseWorkdayStart(s); err != nil || got != want {
			t.Errorf("ParseWorkdayStart(%q) = %+v, %v", s, got, err)
		}
	}
	for _, s := range []string{"24:00", "9:00", "09:60", "0900", "09-00", "+9:00", "09:0a", "", "09:00 "} {
		if got, err := ParseWorkdayStart(s); err == nil {
			t.Errorf("ParseWorkdayStart(%q) = %+v", s, got)
		}
	}
	for s, want := range map[string]time.Weekday{
		"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday, "thursday": time.Thursday,
		"friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday,
	} {
		if got, err := ParseWeekStart(s); err != nil || got != want {
			t.Errorf("ParseWeekStart(%q) = %s, %v", s, got, err)
		}
	}
	for _, s := range []string{"weekend", "Monday", "mon", ""} {
		if got, err := ParseWeekStart(s); err == nil {
			t.Errorf("ParseWeekStart(%q) = %s", s, got)
		}
	}
}
