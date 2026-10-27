// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package timers

import (
	"math"
	"testing"
	"time"
)

// TestAckNoticeSchedule is C-17.FR-2 with the Paging profile (route.ack_timeout, 15 minutes): the notices come 15,
// 45 and 105 minutes after the start, at intervals of 15, 30 and 60 minutes, and there is no fourth.
func TestAckNoticeSchedule(t *testing.T) {
	start, first := business0, 15*time.Minute
	for n, want := range map[int64]time.Duration{1: 15 * time.Minute, 2: 45 * time.Minute, 3: 105 * time.Minute} {
		if got := AckNoticeDue(start, first, n).Sub(start); got != want {
			t.Errorf("notice %d at %s, want %s", n, got, want)
		}
	}
	for n, want := range map[int64]time.Duration{0: 15 * time.Minute, 1: 15 * time.Minute, 2: 30 * time.Minute,
		3: time.Hour} {
		if got := AckNoticeGap(first, n); got != want {
			t.Errorf("gap before notice %d %s, want %s", n, got, want)
		}
	}
	if got := DueAckNotices(start, first, 0, start.Add(10*time.Hour)); got != AckTimeoutNotices {
		t.Errorf("%d notices due long after the start, want %d and no fourth", got, AckTimeoutNotices)
	}
}

// TestDueAckNotices is C-17.FR-7: a firing that comes late counts every notice whose time came, and only those left.
func TestDueAckNotices(t *testing.T) {
	start, first := business0, 15*time.Minute
	cases := []struct {
		name string
		sent int64
		at   time.Duration
		want int64
	}{
		{"before the first", 0, 14 * time.Minute, 0},
		{"exactly at the first", 0, 15 * time.Minute, 1},
		{"between the first and the second", 0, 44 * time.Minute, 1},
		{"two hours after the start", 0, 2 * time.Hour, 3},
		{"the second, late, after the first was sent", 1, 50 * time.Minute, 1},
		{"the second and third after downtime", 1, 3 * time.Hour, 2},
		{"all sent", 3, 10 * time.Hour, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DueAckNotices(start, first, c.sent, start.Add(c.at)); got != c.want {
				t.Errorf("%d due, want %d", got, c.want)
			}
		})
	}
}

// TestReminderSchedule is C-17.FR-4 with the Paging profile (route.reminders, 4 hours doubling up to 24 hours): the
// intervals are 4, 8, 16, 24 and 24 hours, so the Reminders come 4, 12, 28, 52 and 76 hours after the
// acknowledgement.
func TestReminderSchedule(t *testing.T) {
	first, capped := 4*time.Hour, 24*time.Hour
	var at time.Duration
	for k, want := range []time.Duration{4, 12, 28, 52, 76} {
		at += ReminderInterval(int64(k+1), first, capped)
		if at != want*time.Hour {
			t.Errorf("reminder %d at %s, want %dh", k+1, at, want)
		}
	}
	for _, c := range []struct {
		name        string
		k           int64
		first, capd time.Duration
		want        time.Duration
	}{
		{"below one is the first", 0, first, capped, first},
		{"a huge step stays at the cap", math.MaxInt64, first, capped, capped},
		{"the cap equal to the first", 5, first, first, first},
		{"a cap that no doubling reaches before the overflow", 80, time.Hour, math.MaxInt64, math.MaxInt64},
	} {
		if got := ReminderInterval(c.k, c.first, c.capd); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// TestDueReminders is C-17.FR-7 for Reminders: the Reminders due at a late firing are the claimed one and each one
// interval of its following step later, counted at once after the cap.
func TestDueReminders(t *testing.T) {
	first, capped := 4*time.Hour, 24*time.Hour
	d := business0
	cases := []struct {
		name     string
		next     int64
		at       time.Duration
		want     int64
		wantLast time.Duration
	}{
		{"not yet due", 1, -time.Minute, 0, 0},
		{"exactly due", 1, 0, 1, 0},
		{"the first and the second (8 h later)", 1, 9 * time.Hour, 2, 8 * time.Hour},
		{"the first three, the fourth not yet", 1, 40 * time.Hour, 3, 24 * time.Hour},
		{"thirty days at the cap", 4, 30 * 24 * time.Hour, 31, 30 * 24 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, last := DueReminders(d, c.next, first, capped, d.Add(c.at))
			if n != c.want || (n > 0 && !last.Equal(d.Add(c.wantLast))) {
				t.Errorf("%d due, last at %s; want %d at %s", n, last.Sub(d), c.want, c.wantLast)
			}
		})
	}
	if n, last := DueReminders(d, 1, 0, 0, d.Add(time.Hour)); n != 1 || !last.Equal(d) {
		t.Errorf("without intervals: %d at %s", n, last)
	}
}
