// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"errors"
	"testing"
)

// TestOnCallNow is C-27.FR-9: every Team by name, each with the On-call of its main Schedule now — the person, what
// decides it and until when — and the segment that follows, a gap included, or none for a Team without a main
// Schedule; with a User, only their Teams; an Override counts; a segment that lasts beyond schedule.gap_view has no
// end and nothing after it.
func TestOnCallNow(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	store.teams = append(store.teams, teamRec{3, "TM00000000BILL", "billing", "billing"})
	sc, err := svc.Create(ctx, admin, gapInput("Main", "monday"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, admin, gapInput("Second")); err != nil {
		t.Fatal(err)
	}
	list, err := svc.OnCallNow(ctx, 0)
	if err != nil || len(list) != 3 {
		t.Fatalf("on call now = %+v, %v", list, err)
	}
	for i, slug := range []string{"billing", "ops", "payments"} {
		if list[i].Team.Slug != slug {
			t.Errorf("team %d = %s, want %s", i, list[i].Team.Slug, slug)
		}
	}
	if b := list[0]; b.Schedule != nil || b.Current != nil || b.Next != nil || b.Until != nil {
		t.Errorf("a Team without a Schedule %+v", b)
	}
	p := list[2]
	if p.Schedule == nil || p.Schedule.PublicID != sc.PublicID || p.Current == nil || p.Current.User == nil ||
		p.Current.User.Login != "olga" || p.Current.Source.Kind != SourceRotationLayer ||
		p.Current.Source.RotationLayer.Name != "Days" || p.Until == nil ||
		!p.Until.Equal(utc("2026-11-02T00:00:00Z")) || p.Next == nil || p.Next.User != nil ||
		!p.Next.Start.Equal(utc("2026-11-02T00:00:00Z")) || !p.Next.End.Equal(utc("2026-11-03T00:00:00Z")) {
		t.Errorf("payments %+v, current %+v, next %+v", p, p.Current, p.Next)
	}
	mine, err := svc.OnCallNow(ctx, bob.ID)
	if err != nil || len(mine) != 1 || mine[0].Team.Slug != "ops" || mine[0].Schedule != nil {
		t.Errorf("bob's Teams = %+v, %v", mine, err)
	}
	if _, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: petr.PublicID, Start: t0,
		End: t0.Add(GapView + 24*60*60*1e9)}); err != nil {
		t.Fatal(err)
	}
	list, err = svc.OnCallNow(ctx, olga.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("olga's Teams = %+v, %v", list, err)
	}
	if p := list[0]; p.Current.User.Login != "petr" || p.Current.Source.Kind != SourceOverride || p.Until != nil ||
		p.Next != nil {
		t.Errorf("an Override over the whole view %+v, current %+v", p, p.Current)
	}
	for _, name := range []string{"ListOnCallTeams", "ListRotationLayers", "ListPeriodOverrides"} {
		store.fail[name] = errBoom
		if _, err := svc.OnCallNow(ctx, 0); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
}

// TestOnCallNowJoinsOnePerson: "On call now" joins the adjacent segments of one person from different sources, so that
// until is the end of the person's span and next names someone else, or a gap, joined the same way.
func TestOnCallNowJoinsOnePerson(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	store.teams = store.teams[:1]
	sc, err := svc.Create(ctx, admin, gapInput("Main", "monday", "tuesday"))
	if err != nil {
		t.Fatal(err)
	}
	// olga by the Rotation layer until Monday, then by an Override until Monday noon; then a gap until Wednesday.
	if _, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: utc("2026-11-02T00:00:00Z"), End: utc("2026-11-02T12:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.OnCallNow(ctx, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("on call now = %+v, %v", list, err)
	}
	p := list[0]
	if p.Current.User.Login != "olga" || p.Current.Source.Kind != SourceRotationLayer || p.Until == nil ||
		!p.Until.Equal(utc("2026-11-02T12:00:00Z")) || p.Next == nil || p.Next.User != nil ||
		!p.Next.Start.Equal(utc("2026-11-02T12:00:00Z")) || !p.Next.End.Equal(utc("2026-11-04T00:00:00Z")) {
		t.Errorf("current %+v until %v next %+v", p.Current, p.Until, p.Next)
	}
}
