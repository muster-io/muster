// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/organization"
)

// SnoozePreset is a Snooze preset: a Snooze that ends at the next start of the working day, or at the start of the
// working day on the first day of the week (C-10.FR-17).
type SnoozePreset string

// The Snooze presets of C-10.FR-17.
const (
	PresetTomorrow  SnoozePreset = "tomorrow"
	PresetWeekStart SnoozePreset = "week_start"
)

// PresetOrder is the Snooze presets in the order the API, the UI and messengers offer them.
func PresetOrder() []SnoozePreset {
	return []SnoozePreset{PresetTomorrow, PresetWeekStart}
}

// WorkdayStart is the start of the working day, organization.workday_start, as a wall-clock time.
type WorkdayStart struct {
	Hour, Minute int
}

// ParseWorkdayStart reads organization.workday_start, HH:MM on the 24-hour clock.
func ParseWorkdayStart(s string) (WorkdayStart, error) {
	if len(s) == 5 && s[2] == ':' {
		if h, m := twoDigits(s[:2]), twoDigits(s[3:]); h >= 0 && h <= 23 && m >= 0 && m <= 59 {
			return WorkdayStart{Hour: h, Minute: m}, nil
		}
	}
	return WorkdayStart{}, fmt.Errorf("the start of the working day %q is not HH:MM on the 24-hour clock", s)
}

// twoDigits is the number of two decimal digits, or -1.
func twoDigits(s string) int {
	if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return -1
	}
	return int(s[0]-'0')*10 + int(s[1]-'0')
}

// ParseWeekStart reads organization.week_start, monday to sunday.
func ParseWeekStart(s string) (time.Weekday, error) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if s == strings.ToLower(d.String()) {
			return d, nil
		}
	}
	return 0, fmt.Errorf("the first day of the week %q is not a day from monday to sunday", s)
}

// SnoozePresetEnd is the end of the Snooze preset p at now in the time zone loc (C-10.FR-17): from the date of now in
// loc, the first start of the working day strictly after now — on any day for tomorrow, on a day that is weekStart for
// week_start. The end is in UTC.
func SnoozePresetEnd(p SnoozePreset, now time.Time, loc *time.Location, start WorkdayStart,
	weekStart time.Weekday) (time.Time, error) {
	if p != PresetTomorrow && p != PresetWeekStart {
		return time.Time{}, fmt.Errorf("unknown snooze preset %q", p)
	}
	if weekStart < time.Sunday || weekStart > time.Saturday {
		return time.Time{}, fmt.Errorf("unknown first day of the week %d", weekStart)
	}
	y, m, d := now.In(loc).Date()
	for day := d; ; day++ {
		wall := time.Date(y, m, day, start.Hour, start.Minute, 0, 0, time.UTC)
		if p == PresetWeekStart && wall.Weekday() != weekStart {
			continue
		}
		if end := instantOf(wall, loc); end.After(now) {
			return end.UTC(), nil
		}
	}
}

// instantOf is the instant at which the clocks of loc show wall, a wall-clock time written in UTC. A clock change
// decides it here, as time.Date does not promise either side: a wall time the clocks skip takes the offset in force
// before the change, which moves it forward by the change, and one they show twice is the earlier of the two
// instants. The offsets in force a day before and a day after are the candidates.
func instantOf(wall time.Time, loc *time.Location) time.Time {
	before, after := offsetAt(wall.Add(-24*time.Hour), loc), offsetAt(wall.Add(24*time.Hour), loc)
	for _, off := range []int{max(before, after), min(before, after)} {
		if t := wall.Add(-time.Duration(off) * time.Second); offsetAt(t, loc) == off {
			return t
		}
	}
	return wall.Add(-time.Duration(before) * time.Second)
}

func offsetAt(t time.Time, loc *time.Location) int {
	_, off := t.In(loc).Zone()
	return off
}

// PresetEnd is the end a Snooze with the Snooze preset would set now.
type PresetEnd struct {
	Preset SnoozePreset
	Until  time.Time
}

// PresetList is what the Snooze presets end at now (SnoozePresetList): the time zone used, organization.workday_start
// and organization.week_start, and one end per Snooze preset, in the order of SnoozePresets.
type PresetList struct {
	TimeZone     string
	WorkdayStart string
	WeekStart    string
	Ends         []PresetEnd
}

// SnoozePresets are the ends a Snooze with each Snooze preset would set now for the caller c (C-10.FR-18), in the time
// zone timeZone, or, when it is empty, the one C-10.FR-17 picks for the caller. It needs alert-groups:snooze, reads
// nothing else and changes nothing; a time zone Muster does not know is invalid_format at /query/time_zone.
func (s *Service) SnoozePresets(ctx context.Context, c Caller, timeZone string) (PresetList, error) {
	if !c.can(PermissionSnooze) {
		return PresetList{}, &ForbiddenError{Permission: PermissionSnooze}
	}
	var requested *string
	if timeZone != "" {
		if !organization.ValidTimeZone(timeZone) {
			return PresetList{}, errUnknownZone
		}
		requested = &timeZone
	}
	b, err := s.readPresetBasis(ctx, c, requested)
	if err != nil {
		return PresetList{}, err
	}
	out := PresetList{TimeZone: b.zone, WorkdayStart: b.settings.WorkdayStart, WeekStart: b.settings.WeekStart}
	for _, p := range PresetOrder() {
		until, err := b.end(p)
		if err != nil {
			return PresetList{}, err
		}
		out.Ends = append(out.Ends, PresetEnd{Preset: p, Until: until})
	}
	return out, nil
}

// resolveEnd computes the end of a Snooze preset for the caller c, before the dispatcher locks an Alert Group, and
// returns a copy of e with Until set and the time zone it was computed in; any other end, and none, stay as they are.
func (s *Service) resolveEnd(ctx context.Context, c Caller, e *SnoozeEnd) (*SnoozeEnd, error) {
	if e == nil || e.Preset == "" {
		return e, nil
	}
	b, err := s.readPresetBasis(ctx, c, e.TimeZone)
	if err != nil {
		return nil, err
	}
	until, err := b.end(e.Preset)
	if err != nil {
		return nil, err
	}
	out := *e
	out.Until, out.zone = &until, b.zone
	return &out, nil
}

// presetBasis is what the ends of the Snooze presets are computed from: now on the business clock, the time zone with
// its name, and the Organization's start of the working day and first day of the week.
type presetBasis struct {
	now       time.Time
	zone      string
	loc       *time.Location
	start     WorkdayStart
	weekStart time.Weekday
	settings  dbgen.GetListSettingsRow
}

// readPresetBasis reads what the Snooze presets of the caller c are computed from (C-10.FR-17): the time zone is
// timeZone, checked already, when the request names one, else the profile time zone of the User c acts as — a
// Personal access token acts as its owner — else the Organization's, which a Service account always takes.
func (s *Service) readPresetBasis(ctx context.Context, c Caller, timeZone *string) (presetBasis, error) {
	settings, err := s.store.GetListSettings(ctx, s.orgID)
	if err != nil {
		return presetBasis{}, fmt.Errorf("read the snooze preset settings: %w", err)
	}
	var zone string
	switch {
	case timeZone != nil:
		zone = *timeZone
	case c.user() != 0:
		profile, err := s.store.GetUserTimeZone(ctx, dbgen.GetUserTimeZoneParams{OrgID: s.orgID, ID: c.user()})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return presetBasis{}, fmt.Errorf("read the profile time zone: %w", err)
		}
		if organization.ValidTimeZone(profile) {
			zone = profile
		}
	}
	zone = cmp.Or(zone, settings.TimeZone)
	b := presetBasis{now: s.clock.Now(), zone: zone, settings: settings}
	if b.loc, err = time.LoadLocation(zone); err != nil {
		return presetBasis{}, fmt.Errorf("load the time zone of the snooze presets: %w", err)
	}
	if b.start, err = ParseWorkdayStart(settings.WorkdayStart); err != nil {
		return presetBasis{}, err
	}
	if b.weekStart, err = ParseWeekStart(settings.WeekStart); err != nil {
		return presetBasis{}, err
	}
	return b, nil
}

// end is the end of the Snooze preset p.
func (b presetBasis) end(p SnoozePreset) (time.Time, error) {
	return SnoozePresetEnd(p, b.now, b.loc, b.start, b.weekStart)
}
