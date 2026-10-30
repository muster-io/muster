// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// segmentsText writes the segments of a Result as "start–end who source" lines in the wall-clock time of loc.
func segmentsText(loc *time.Location, list []Segment) string {
	var b strings.Builder
	for _, s := range list {
		who, src := "gap", "-"
		if s.User != nil {
			who = s.User.Login
		}
		if s.Source != nil {
			src = s.Source.Override
			if s.Source.Kind == SourceRotationLayer {
				src = s.Source.Layer.Name
			}
		}
		fmt.Fprintf(&b, "%s–%s %s %s\n", s.Start.In(loc).Format("01-02 15:04"), s.End.In(loc).Format("01-02 15:04"),
			who, src)
	}
	return b.String()
}

// warningsText writes warnings as "kind login where" lines.
func warningsText(list []Warning) string {
	var b strings.Builder
	for _, w := range list {
		where := w.Override
		if w.Layer != nil {
			where = w.Layer.Name
		}
		fmt.Fprintf(&b, "%s %s %s\n", w.Kind, w.User.Login, where)
	}
	return b.String()
}

// scenario are the Rotation layers of C-27 scenario 1 from Monday 2 November 2026: below, weekly Shifts handed over on
// Monday at 10:00 among petr, sergey and olga; above, weekly Shifts on weekdays from 11:00 to 19:00 among olga and petr.
func scenario() []Layer {
	return []Layer{
		{PublicID: "RY00000000LOW1", Number: 1, Name: "Around the clock", Users: []Person{petr, sergey, olga},
			ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday", StartDate: date("2026-11-02")},
		{PublicID: "RY00000000UP02", Number: 2, Name: "Working hours", Users: []Person{olga, petr},
			ShiftLength: ShiftWeek, HandoverTime: "11:00", HandoverDay: "monday", StartDate: date("2026-11-02"),
			Limits: &Limits{Days: []string{"monday", "tuesday", "wednesday", "thursday", "friday"}, Start: "11:00",
				End: "19:00"}},
	}
}

// TestResult is C-27.FR-3, FR-4 and FR-5 with C-27.AC-1, AC-2, AC-4 and AC-10: the higher Rotation layer wins where it
// has a Shift; an Override wins over every layer and closes a gap; of overlapping Overrides the one created last wins
// (P-65); the person an Override replaces changes nothing; a person who may not be Paged — disabled, a Viewer, outside
// the Team — is skipped in favour of the layer below, else a gap, with one warning per person and layer, or per
// Override; adjacent segments of the same person and source are joined.
func TestResult(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	at := func(s string) time.Time { return wall(berlin, s) }
	over := func(id string, p Person, from, to string) Override {
		return Override{PublicID: id, User: p, Start: at(from), End: at(to)}
	}
	wed := func(week int) (time.Time, time.Time) {
		day := at("2026-11-04 00:00").AddDate(0, 0, 7*week)
		return day, day.AddDate(0, 0, 1)
	}
	for _, c := range []struct {
		name      string
		layers    []Layer
		overrides []Override
		skipped   map[int64]string
		week      int
		want      string
		warnings  string
	}{
		{name: "the upper layer within its limits, the lower one outside", layers: scenario(),
			want: "11-04 00:00–11-04 11:00 petr Around the clock\n11-04 11:00–11-04 19:00 olga Working hours\n" +
				"11-04 19:00–11-05 00:00 petr Around the clock\n"},
		{name: "an Override over both layers, joined across them", layers: scenario(), week: 2,
			overrides: []Override{over("VR000000000001", petr, "2026-11-18 11:00", "2026-11-18 21:00")},
			want: "11-18 00:00–11-18 11:00 olga Around the clock\n11-18 11:00–11-18 21:00 petr VR000000000001\n" +
				"11-18 21:00–11-19 00:00 olga Around the clock\n"},
		{name: "the Override created last wins where they overlap", layers: scenario(), week: 2,
			overrides: []Override{over("VR000000000001", petr, "2026-11-18 11:00", "2026-11-18 15:00"),
				over("VR000000000002", sergey, "2026-11-18 13:00", "2026-11-18 17:00")},
			want: "11-18 00:00–11-18 11:00 olga Around the clock\n11-18 11:00–11-18 13:00 petr VR000000000001\n" +
				"11-18 13:00–11-18 17:00 sergey VR000000000002\n11-18 17:00–11-18 19:00 olga Working hours\n" +
				"11-18 19:00–11-19 00:00 olga Around the clock\n"},
		{name: "the person an Override replaces changes nothing", layers: scenario(), week: 2,
			overrides: []Override{{PublicID: "VR000000000001", User: petr, Replaces: &olga,
				Start: at("2026-11-18 11:00"), End: at("2026-11-18 12:00")}},
			want: "11-18 00:00–11-18 11:00 olga Around the clock\n11-18 11:00–11-18 12:00 petr VR000000000001\n" +
				"11-18 12:00–11-18 19:00 olga Working hours\n11-18 19:00–11-19 00:00 olga Around the clock\n"},
		{name: "an Override closes a gap", layers: scenario()[1:],
			overrides: []Override{over("VR000000000001", sergey, "2026-11-04 20:00", "2026-11-04 22:00")},
			want: "11-04 00:00–11-04 11:00 gap -\n11-04 11:00–11-04 19:00 olga Working hours\n" +
				"11-04 19:00–11-04 20:00 gap -\n11-04 20:00–11-04 22:00 sergey VR000000000001\n" +
				"11-04 22:00–11-05 00:00 gap -\n"},
		{name: "a disabled person of the upper layer falls through to the lower one", layers: scenario(), week: 1,
			skipped:  map[int64]string{petr.ID: ReasonDisabled},
			want:     "11-11 00:00–11-12 00:00 sergey Around the clock\n",
			warnings: "disabled petr Working hours\n"},
		{name: "a Viewer of the upper layer falls through", layers: scenario(),
			skipped:  map[int64]string{olga.ID: ReasonViewer},
			want:     "11-04 00:00–11-05 00:00 petr Around the clock\n",
			warnings: "viewer olga Working hours\n"},
		{name: "people outside the Team in both layers leave a gap", layers: scenario(), week: 1,
			skipped:  map[int64]string{petr.ID: ReasonNotInTeam, sergey.ID: ReasonNotInTeam},
			want:     "11-11 00:00–11-12 00:00 gap -\n",
			warnings: "not_in_team sergey Around the clock\nnot_in_team petr Working hours\n"},
		{name: "an Override whose person may not be Paged is skipped", layers: scenario(),
			skipped:   map[int64]string{sergey.ID: ReasonDisabled},
			overrides: []Override{over("VR000000000001", sergey, "2026-11-04 01:00", "2026-11-04 02:00")},
			want: "11-04 00:00–11-04 11:00 petr Around the clock\n11-04 11:00–11-04 19:00 olga Working hours\n" +
				"11-04 19:00–11-05 00:00 petr Around the clock\n",
			warnings: "disabled sergey VR000000000001\n"},
		{name: "an earlier Override decides where the later one is skipped", layers: scenario(),
			skipped: map[int64]string{sergey.ID: ReasonViewer},
			overrides: []Override{over("VR000000000001", olga, "2026-11-04 01:00", "2026-11-04 03:00"),
				over("VR000000000002", sergey, "2026-11-04 02:00", "2026-11-04 04:00")},
			want: "11-04 00:00–11-04 01:00 petr Around the clock\n11-04 01:00–11-04 03:00 olga VR000000000001\n" +
				"11-04 03:00–11-04 11:00 petr Around the clock\n11-04 11:00–11-04 19:00 olga Working hours\n" +
				"11-04 19:00–11-05 00:00 petr Around the clock\n",
			warnings: "viewer sergey VR000000000002\n"},
		{name: "Overrides outside the period are left out", layers: scenario(),
			overrides: []Override{over("VR000000000001", sergey, "2026-11-01 01:00", "2026-11-01 02:00")},
			want: "11-04 00:00–11-04 11:00 petr Around the clock\n11-04 11:00–11-04 19:00 olga Working hours\n" +
				"11-04 19:00–11-05 00:00 petr Around the clock\n"},
		{name: "the daily Shifts of one person are joined", week: 1,
			layers: []Layer{{PublicID: "RY000000000001", Name: "alone", Users: []Person{olga}, ShiftLength: ShiftDay,
				HandoverTime: "09:00", StartDate: date("2026-11-02")}},
			want: "11-11 00:00–11-12 00:00 olga alone\n"},
		{name: "no layers", want: "11-04 00:00–11-05 00:00 gap -\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			from, to := wed(c.week)
			res := compute(berlin, c.layers, c.overrides, c.skipped, from, to)
			if got := segmentsText(berlin, res.Segments); got != c.want {
				t.Errorf("segments =\n%swant\n%s", got, c.want)
			}
			if got := warningsText(res.Warnings); got != c.warnings {
				t.Errorf("warnings =\n%swant\n%s", got, c.warnings)
			}
			if len(res.Layers) != len(c.layers) {
				t.Errorf("%d layers of shifts, want %d", len(res.Layers), len(c.layers))
			}
		})
	}
}

// TestResultWeek is the Result of a week with a weekly Rotation layer around the clock below and one from 19:00 to
// 09:00 every day above: the nights are the upper layer's, the days the lower one's, and the skipped Shifts are marked.
func TestResultWeek(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	layers := []Layer{
		{PublicID: "RY00000000LOW1", Number: 1, Name: "day", Users: []Person{petr}, ShiftLength: ShiftWeek,
			HandoverTime: "10:00", HandoverDay: "monday", StartDate: date("2026-11-02")},
		{PublicID: "RY0000000NIGHT", Number: 2, Name: "night", Users: []Person{olga, sergey}, ShiftLength: ShiftWeek,
			HandoverTime: "10:00", HandoverDay: "monday", StartDate: date("2026-11-02"),
			Limits: &Limits{Days: Weekdays, Start: "19:00", End: "09:00"}},
	}
	from, to := wall(berlin, "2026-11-02 10:00"), wall(berlin, "2026-11-09 10:00")
	res := compute(berlin, layers, nil, nil, from, to)
	var want strings.Builder
	day := func(d int) string { return from.AddDate(0, 0, d).Format("01-02") }
	fmt.Fprintf(&want, "%s 10:00–%s 19:00 petr day\n", day(0), day(0))
	for d := range 7 {
		fmt.Fprintf(&want, "%s 19:00–%s 09:00 olga night\n", day(d), day(d+1))
		if d < 6 {
			fmt.Fprintf(&want, "%s 09:00–%s 19:00 petr day\n", day(d+1), day(d+1))
		}
	}
	fmt.Fprintf(&want, "%s 09:00–%s 10:00 petr day\n", day(7), day(7))
	if got := segmentsText(berlin, res.Segments); got != want.String() {
		t.Errorf("segments =\n%swant\n%s", got, want.String())
	}
	if len(res.Layers) != 2 || len(res.Layers[0].Shifts) != 1 || len(res.Layers[1].Shifts) != 7 {
		t.Errorf("shifts %+v", res.Layers)
	}
	skipped := compute(berlin, layers, nil, map[int64]string{olga.ID: ReasonDisabled}, from, to)
	if len(skipped.Segments) != 1 || skipped.Segments[0].User.Login != "petr" ||
		skipped.Layers[1].Shifts[0].Skipped != ReasonDisabled || len(skipped.Warnings) != 1 {
		t.Errorf("olga disabled: %s %+v", segmentsText(berlin, skipped.Segments), skipped.Warnings)
	}
}

func TestSkipReason(t *testing.T) {
	for _, c := range []struct {
		p      Person
		inTeam bool
		want   string
	}{
		{Person{Role: "responder", Status: "active"}, true, ""},
		{Person{Role: "admin", Status: "active"}, true, ""},
		{Person{Role: "viewer", Status: "disabled"}, true, ReasonDisabled},
		{Person{Role: "viewer", Status: "active"}, true, ReasonViewer},
		{Person{Role: "responder", Status: "active"}, false, ReasonNotInTeam},
		{Person{Role: "responder", Status: "deleted"}, false, ReasonNotInTeam},
	} {
		if got := skipReason(c.p, c.inTeam); got != c.want {
			t.Errorf("skipReason(%+v, %v) = %q, want %q", c.p, c.inTeam, got, c.want)
		}
	}
}
