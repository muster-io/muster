// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/schedules/dbgen"
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

// TestTeamOnCallAt is the On-call that a Mention entry mentions (C-27.FR-4, C-28.FR-3): the person on Shift in the
// Team's main Schedule at the moment, none in a gap of it, for a Team without a main Schedule, or when a read fails.
func TestTeamOnCallAt(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	if _, err := svc.Create(ctx, admin, gapInput("Main", "monday")); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := svc.TeamOnCallAt(ctx, nil, 1, t0); err != nil || !ok || id != olga.ID {
		t.Errorf("payments now = %d, %v, %v; want olga", id, ok, err)
	}
	if id, ok, err := svc.TeamOnCallAt(ctx, nil, 1, utc("2026-11-02T01:00:00Z")); err != nil || ok || id != 0 {
		t.Errorf("payments in a gap = %d, %v, %v", id, ok, err)
	}
	// The Monday gap of the Days layer, from 2026-11-02T00:00Z to 2026-11-03T00:00Z, at its boundaries.
	for at, want := range map[string]bool{"2026-11-01T23:59:00Z": true, "2026-11-01T23:59:59Z": true,
		"2026-11-02T00:00:00Z": false, "2026-11-02T00:01:00Z": false, "2026-11-02T23:59:00Z": false,
		"2026-11-03T00:00:00Z": true, "2026-11-03T00:01:00Z": true} {
		if id, ok, err := svc.TeamOnCallAt(ctx, nil, 1, utc(at)); err != nil || ok != want || (ok && id != olga.ID) {
			t.Errorf("payments at %s = %d, %v, %v; want on call %v", at, id, ok, err, want)
		}
	}
	// Through a caller's transaction, which the store's On runs the queries in.
	if id, ok, err := svc.TeamOnCallAt(ctx, struct{ dbgen.DBTX }{}, 1, t0); err != nil || !ok || id != olga.ID {
		t.Errorf("payments in a transaction = %d, %v, %v", id, ok, err)
	}
	if _, ok, err := svc.TeamOnCallAt(ctx, nil, 2, t0); err != nil || ok {
		t.Errorf("ops without a main schedule = %v, %v", ok, err)
	}
	for _, name := range []string{"GetTeamMainSchedule", "ListRotationLayers", "ListPeriodOverrides"} {
		store.fail[name] = errBoom
		if _, _, err := svc.TeamOnCallAt(ctx, nil, 1, t0); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
}

// TestOnCallNowUnreachable is C-27.FR-9 and C-27.AC-6 in "On call now": the On-call now of payments, petr without a
// Mattermost Account link, is marked unreachable in payments-notice with the messenger mattermost and the reason
// no_account_link — and in tg-payments, without a Telegram link — read once for every Team; olga, linked, has no
// mark the next day; ops, without a Schedule, and a Team in a gap have none.
func TestOnCallNowUnreachable(t *testing.T) {
	links := []dbgen.ListAccountLinkSpacesRow{{UserID: olga.ID, IdentitySpace: "mattermost:31"},
		{UserID: olga.ID, IdentitySpace: "telegram"}}
	svc, store, c := newReachService(t, reachDests(), links)
	ctx := t.Context()
	if _, err := svc.Create(ctx, admin, reachInput()); err != nil {
		t.Fatal(err)
	}
	marks := func(t *testing.T) map[string]string {
		t.Helper()
		list, err := svc.OnCallNow(ctx, 0)
		if err != nil || len(list) != 2 {
			t.Fatalf("on call now = %+v, %v", list, err)
		}
		out := map[string]string{}
		for _, team := range list {
			var got []string
			for _, u := range team.Unreachable {
				got = append(got, u.Destination.Name+"/"+u.Messenger+"/"+u.Reason)
			}
			if team.Unreachable == nil {
				t.Errorf("%s: unreachable is nil", team.Team.Slug)
			}
			out[team.Team.Slug] = strings.Join(got, " ")
		}
		return out
	}
	if got := marks(t); got["payments"] != "payments-notice/mattermost/no_account_link tg-payments/telegram/no_account_link" ||
		got["ops"] != "" {
		t.Errorf("petr now = %v", got)
	}
	if n := len(store.asked); n != 1 || !slices.Equal(store.asked[0].TeamIds, []int64{1}) {
		t.Errorf("the reach read %+v", store.asked)
	}
	c.Set(utc("2026-10-29T12:00:00Z"))
	if got := marks(t); got["payments"] != "" {
		t.Errorf("olga now = %v", got)
	}
	store.fail["ListTeamReach"] = errBoom
	if _, err := svc.OnCallNow(ctx, 0); !errors.Is(err, errBoom) {
		t.Errorf("ListTeamReach fails = %v", err)
	}
	delete(store.fail, "ListTeamReach")

	gap, _, _ := newReachService(t, reachDests(), nil)
	if _, err := gap.Create(ctx, admin, gapInput("Main", "wednesday")); err != nil {
		t.Fatal(err)
	}
	list, err := gap.OnCallNow(ctx, 0)
	if err != nil || len(list) != 2 || list[1].Current == nil || list[1].Current.User != nil ||
		len(list[1].Unreachable) != 0 {
		t.Errorf("a Team in a gap = %+v, %v", list, err)
	}
}

// TestOnCallNowGatewayReaches is C-27.FR-8 in "On call now": each mark of the On-call carries gateway_reaches, true
// while petr, who has a confirmed contact at a Notification gateway, is On-call, false without one.
func TestOnCallNowGatewayReaches(t *testing.T) {
	svc, store, _ := newReachService(t, reachDests(), nil)
	ctx := t.Context()
	if _, err := svc.Create(ctx, admin, reachInput()); err != nil {
		t.Fatal(err)
	}
	reaches := func() []bool {
		list, err := svc.OnCallNow(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []bool
		for _, team := range list {
			for _, u := range team.Unreachable {
				out = append(out, u.GatewayReaches)
			}
		}
		return out
	}
	if got := reaches(); len(got) != 2 || got[0] || got[1] {
		t.Errorf("petr without a contact = %v", got)
	}
	store.gateway = []int64{petr.ID}
	if got := reaches(); len(got) != 2 || !got[0] || !got[1] {
		t.Errorf("petr with a confirmed contact = %v", got)
	}
}
