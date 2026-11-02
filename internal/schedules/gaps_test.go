// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"errors"
	"testing"
	"time"
)

// gapsText writes gaps as "start–end" lines in UTC.
func gapsText(list []Gap) string {
	out := ""
	for _, g := range list {
		out += g.Start.UTC().Format("01-02 15:04") + "–" + g.End.UTC().Format("01-02 15:04") + "\n"
	}
	return out
}

// TestGaps: the gaps of a Result are its segments without a person, cut to the period; a fully covered period has
// none, and an empty Schedule is one gap.
func TestGaps(t *testing.T) {
	from, to := utc("2026-11-02T00:00:00Z"), utc("2026-11-09T00:00:00Z")
	res := compute(time.UTC, scenario(), nil, nil, from, to)
	if got := gapsText(res.Gaps()); got != "11-02 00:00–11-02 10:00\n" {
		t.Errorf("scenario 1 from midnight: %q", got)
	}
	if got := res.Gaps(); len(got) != len(gapWarnings(res)) || gapWarnings(res)[0].Kind != WarningGap ||
		!gapWarnings(res)[0].Start.Equal(from) || gapWarnings(res)[0].User.ID != 0 {
		t.Errorf("warnings %+v", gapWarnings(res))
	}
	if got := compute(time.UTC, scenario(), nil, nil, utc("2026-11-03T00:00:00Z"), to).Gaps(); len(got) != 0 {
		t.Errorf("a covered week: %v", got)
	}
	if got := gapsText(compute(time.UTC, nil, nil, nil, from, to).Gaps()); got != "11-02 00:00–11-09 00:00\n" {
		t.Errorf("no Rotation layer: %q", got)
	}
}

// TestFridayNightGaps is C-27.FR-7 with C-27.FR-2: with a Rotation layer limited to Friday 19:00–09:00 as the only one,
// the gaps run from Saturday 09:00 to Friday 19:00, the night from Friday to Saturday is no gap, and the warnings gap
// and next_gap say so.
func TestFridayNightGaps(t *testing.T) {
	svc, _, c := newService(t)
	ctx := t.Context()
	in := gapInput("Nights")
	in.RotationLayers[0].Limits = &Limits{Days: []string{"friday"}, Start: "19:00", End: "09:00"}
	sc, err := svc.Create(ctx, admin, in)
	if err != nil {
		t.Fatal(err)
	}
	from, to := utc("2026-10-30T00:00:00Z"), utc("2026-11-01T00:00:00Z") // Friday to Sunday
	res, err := svc.OnCall(ctx, sc.PublicID, &from, &to)
	if err != nil {
		t.Fatal(err)
	}
	var warned []Gap
	for _, w := range res.Warnings {
		if w.Kind == WarningGap {
			warned = append(warned, Gap{Start: w.Start, End: w.End})
		}
	}
	if got := gapsText(warned); got != "10-30 00:00–10-30 19:00\n10-31 09:00–11-01 00:00\n" {
		t.Errorf("the gap warnings of Friday and Saturday: %q", got)
	}
	week, end := utc("2026-10-31T09:00:00Z"), utc("2026-11-14T00:00:00Z")
	res, err = svc.OnCall(ctx, sc.PublicID, &week, &end)
	if err != nil {
		t.Fatal(err)
	}
	if got := gapsText(res.Gaps()); got != "10-31 09:00–11-06 19:00\n11-07 09:00–11-13 19:00\n" {
		t.Errorf("two weeks from Saturday 09:00: %q", got)
	}
	c.Set(utc("2026-10-30T20:00:00Z")) // inside the Friday night
	got, err := svc.Get(ctx, sc.PublicID)
	if err != nil || got.NextGap == nil || !got.NextGap.Start.Equal(utc("2026-10-31T09:00:00Z")) ||
		!got.NextGap.End.Equal(utc("2026-11-06T19:00:00Z")) {
		t.Errorf("next_gap in the night = %+v, %v", got.NextGap, err)
	}
	c.Set(utc("2026-11-02T12:00:00Z")) // inside the gap: it starts now
	if got, err = svc.Get(ctx, sc.PublicID); err != nil || got.NextGap == nil || !got.NextGap.Start.Equal(c.Now()) {
		t.Errorf("next_gap inside the gap = %+v, %v", got.NextGap, err)
	}
}

// TestNextGap is C-27.FR-7 on the reads: getSchedule, listSchedules, createSchedule and updateSchedule return the first
// gap within schedule.gap_view, or none; the Results of a page are computed together, each with whether its people
// may be Paged in its own Team; a failed read fails the call.
func TestNextGap(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	covered, err := svc.Create(ctx, admin, gapInput("Covered"))
	if err != nil {
		t.Fatal(err)
	}
	if covered.NextGap != nil {
		t.Errorf("created with next_gap %+v", covered.NextGap)
	}
	in := gapInput("Ops")
	in.TeamID, in.RotationLayers[0].UserIDs = teamOps, []string{bob.PublicID}
	ops, err := svc.Create(ctx, admin, in)
	if err != nil || ops.NextGap != nil {
		t.Fatalf("created %+v, %v", ops, err)
	}
	store.inTeam[2] = nil // bob leaves ops: his Shifts are skipped, a gap from now
	page, err := svc.List(ctx, ListFilter{Limit: 10})
	if err != nil || len(page.Schedules) != 2 {
		t.Fatalf("list = %+v, %v", page, err)
	}
	for _, sc := range page.Schedules {
		switch sc.PublicID {
		case covered.PublicID:
			if sc.NextGap != nil {
				t.Errorf("covered next_gap %+v", sc.NextGap)
			}
		case ops.PublicID:
			if sc.NextGap == nil || !sc.NextGap.Start.Equal(t0) || !sc.NextGap.End.Equal(t0.Add(GapView)) {
				t.Errorf("ops next_gap %+v", sc.NextGap)
			}
		}
	}
	upd := inputOf(covered)
	upd.RotationLayers[0].Limits.Days = []string{"monday"}
	got, err := svc.Update(ctx, admin, covered.PublicID, nil, upd)
	if err != nil || got.NextGap == nil || !got.NextGap.Start.Equal(t0) ||
		!got.NextGap.End.Equal(utc("2026-11-02T00:00:00Z")) {
		t.Errorf("updated next_gap %+v, %v", got.NextGap, err)
	}
	if same, err := svc.Update(ctx, admin, covered.PublicID, nil, upd); err != nil || same.NextGap == nil {
		t.Errorf("an update that changes nothing = %+v, %v", same.NextGap, err)
	}
	for _, name := range []string{"ListPeriodOverrides", "ListPageableInTeams"} {
		store.fail[name] = errBoom
		if _, err := svc.Get(ctx, covered.PublicID); !errors.Is(err, errBoom) {
			t.Errorf("get when %s fails = %v", name, err)
		}
		if _, err := svc.List(ctx, ListFilter{Limit: 10}); !errors.Is(err, errBoom) {
			t.Errorf("list when %s fails = %v", name, err)
		}
		if _, err := svc.Create(ctx, admin, gapInput("Fails "+name)); !errors.Is(err, errBoom) {
			t.Errorf("create when %s fails = %v", name, err)
		}
		if _, err := svc.Update(ctx, admin, covered.PublicID, nil, upd); !errors.Is(err, errBoom) {
			t.Errorf("update when %s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	if _, err := svc.Get(ctx, "SC00000000NONE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
}
