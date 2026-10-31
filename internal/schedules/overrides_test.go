// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
)

// asCI is a Service account that is not an Admin, and asRobot one that is.
var (
	asCI    = Requester{Actor: audit.ServiceAccount(9, "SA0000000000CI"), Transport: audit.TransportAPI}
	asRobot = Requester{Actor: audit.ServiceAccount(9, "SA0000000000CI"), Transport: audit.TransportAPI, Admin: true}
)

// berlinAt is the instant of a wall-clock time in Berlin.
func berlinAt(t *testing.T, s string) time.Time {
	t.Helper()
	return wall(zone(t, "Europe/Berlin"), s)
}

// TestOverridePrecondition is C-27.FR-6 and AC-3: an Override by someone in the Team or an Admin, not by a Responder
// or a Service account outside it (not_in_team); the person who takes the Shift may be Paged, else not_pageable at
// /user_id with the reason, while the person replaced may be anyone; the end is after the start and not in the past
// (P-91), else out_of_range at /end.
func TestOverridePrecondition(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	span := func(who string) OverrideInput {
		return OverrideInput{UserID: who, Start: berlinAt(t, "2026-11-04 11:00"), End: berlinAt(t, "2026-11-04 21:00")}
	}
	for _, r := range []Requester{asBob, asCI} {
		if _, err := svc.CreateOverride(ctx, r, sc.PublicID, span(olga.PublicID)); !errors.Is(err, ErrNotInTeam) {
			t.Errorf("by %s = %v", r.Actor.PublicID, err)
		}
	}
	for _, c := range []struct {
		in            OverrideInput
		pointer, code string
		reason        string
	}{
		{span(vic.PublicID), "/user_id", CodeNotPageable, ReasonViewer},
		{span(dan.PublicID), "/user_id", CodeNotPageable, ReasonDisabled},
		{span(bob.PublicID), "/user_id", CodeNotPageable, ReasonNotInTeam},
		{span("SR00000000GENE"), "/user_id", CodeUnknownID, ""},
		{span("x"), "/user_id", CodeUnknownID, ""},
		{OverrideInput{UserID: olga.PublicID, ReplacesUserID: "SR00000000GENE", Start: t0, End: t0.Add(time.Hour)},
			"/replaces_user_id", CodeUnknownID, ""},
		{OverrideInput{UserID: olga.PublicID, Start: t0.Add(time.Hour), End: t0.Add(time.Hour)}, "/end",
			CodeOutOfRange, ""},
		{OverrideInput{UserID: olga.PublicID, Start: t0.Add(-2 * time.Hour), End: t0.Add(-time.Hour)}, "/start",
			CodeOutOfRange, ""},
		{OverrideInput{UserID: olga.PublicID, Start: t0.Add(-6 * time.Minute), End: t0.Add(time.Hour)}, "/start",
			CodeOutOfRange, ""},
		{OverrideInput{UserID: olga.PublicID, Start: t0, End: t0.Add(MaxOverrideLength + time.Second)}, "/end",
			CodeOutOfRange, ""},
	} {
		f := fieldError(t, func() error { _, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, c.in); return err }())
		if f.Pointer != c.pointer || f.Code != c.code || f.Reason != c.reason {
			t.Errorf("%+v = %+v", c.in, f)
		}
	}
	for _, c := range []struct {
		r        Requester
		replaces Person
		kind     string
	}{{asPetr, dan, "user"}, {admin, bob, "user"}, {asRobot, petr, "service_account"}} {
		in := span(olga.PublicID)
		in.ReplacesUserID = c.replaces.PublicID
		o, err := svc.CreateOverride(ctx, c.r, sc.PublicID, in)
		if err != nil || o.User.PublicID != olga.PublicID || o.Replaces == nil ||
			o.Replaces.PublicID != c.replaces.PublicID || o.CreatedBy.Kind != c.kind ||
			o.CreatedBy.PublicID != c.r.Actor.PublicID || !strings.HasPrefix(o.PublicID, "VR") || o.Version != 1 {
			t.Errorf("by %s replacing %s = %+v, %v", c.r.Actor.PublicID, c.replaces.Login, o, err)
		}
	}
	if store.overrides[2].bySA != 9 || store.overrides[0].byUser != petr.ID {
		t.Errorf("makers %+v %+v", *store.overrides[0], *store.overrides[2])
	}
	if _, err := svc.CreateOverride(ctx, asPetr, "SC00000000NNNE", span(olga.PublicID)); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("an unknown schedule = %v", err)
	}
	if _, err := svc.CreateOverride(ctx, asPetr, "x", span(olga.PublicID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("a malformed schedule = %v", err)
	}
	for _, name := range []string{"LockSchedule", "IsTeamUser", "LookupUsers", "ListPageable",
		"InsertScheduleOverride", "LockScheduleOverride", "GetScheduleOverride", "InsertAuditEntry", "Notify"} {
		store.fail[name] = errBoom
		in := span(olga.PublicID)
		in.ReplacesUserID = dan.PublicID
		if _, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, in); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
}

// TestOverrideLifecycle is C-27.FR-3 and FR-6: an Override is recorded as schedule_override.created with its people and
// span and announces the Schedule; a change with a stale If-Match gets 412; changing it records the diff; once it has
// ended it can be neither changed nor deleted (override_ended, settled 3); a deletion before is recorded.
func TestOverrideLifecycle(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	audits, hints := len(store.audit), len(store.hints)
	in := OverrideInput{UserID: petr.PublicID, ReplacesUserID: olga.PublicID, Start: berlinAt(t, "2026-11-18 11:00"),
		End: berlinAt(t, "2026-11-18 21:00")}
	o, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, in)
	if err != nil {
		t.Fatal(err)
	}
	e := store.audit[audits]
	if e.Action != ActionOverrideCreated || e.ResourceType.String != ResourceOverride ||
		e.ResourcePublicID.String != o.PublicID || e.ActorUserID.Int64 != petr.ID ||
		!strings.Contains(string(e.Diff), `"pointer":"/user_id","after":"`+petr.PublicID+`"`) ||
		!strings.Contains(string(e.Diff), `"pointer":"/replaces_user_id","after":"`+olga.PublicID+`"`) ||
		!strings.Contains(string(e.Details), `"schedule":"`+sc.PublicID+`"`) ||
		store.hints[hints] != (db.Hint{OrgID: orgID, Type: Hint, ID: sc.PublicID}) {
		t.Errorf("audit %+v %s %s, hints %+v", e, e.Diff, e.Details, store.hints[hints:])
	}
	if _, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, o.PublicID, ptr(int64(4)), in); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	if _, err := svc.UpdateOverride(ctx, asBob, sc.PublicID, o.PublicID, nil, in); !errors.Is(err, ErrNotInTeam) {
		t.Errorf("by bob = %v", err)
	}
	for _, id := range []string{"VR00000000NNNE", "x"} {
		if _, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, id, nil, in); !errors.Is(err, ErrOverrideNotFound) {
			t.Errorf("override %s = %v", id, err)
		}
	}
	in.End, in.ReplacesUserID = berlinAt(t, "2026-11-18 20:00"), ""
	changed, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, o.PublicID, ptr(int64(1)), in)
	if err != nil || changed.Version != 2 || changed.Replaces != nil || !changed.End.Equal(in.End) {
		t.Fatalf("changed = %+v, %v", changed, err)
	}
	if e := store.audit[len(store.audit)-1]; e.Action != ActionOverrideUpdated ||
		!strings.Contains(string(e.Diff), `"pointer":"/replaces_user_id","before":"`+olga.PublicID+`"`) {
		t.Errorf("audit of the change %s", e.Diff)
	}
	in.End = in.Start
	if f := fieldError(t, func() error {
		_, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, o.PublicID, nil, in)
		return err
	}()); f.Pointer != "/end" {
		t.Errorf("an end before the start = %+v", f)
	}
	for _, name := range []string{"UpdateScheduleOverride", "GetScheduleOverride", "InsertAuditEntry"} {
		store.fail[name] = errBoom
		in.End = berlinAt(t, "2026-11-18 19:00")
		if _, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, o.PublicID, nil, in); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	other, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: berlinAt(t, "2026-11-19 11:00"), End: berlinAt(t, "2026-11-19 12:00")})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteOverride(ctx, asPetr, sc.PublicID, other.PublicID, ptr(int64(9))); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("a stale deletion = %v", err)
	}
	store.fail["DeleteScheduleOverride"] = errBoom
	if err := svc.DeleteOverride(ctx, asPetr, sc.PublicID, other.PublicID, nil); !errors.Is(err, errBoom) {
		t.Errorf("the deletion fails = %v", err)
	}
	delete(store.fail, "DeleteScheduleOverride")
	if err := svc.DeleteOverride(ctx, asPetr, sc.PublicID, other.PublicID, ptr(int64(1))); err != nil {
		t.Fatal(err)
	}
	if e := store.audit[len(store.audit)-1]; e.Action != ActionOverrideDeleted || len(store.overrides) != 1 ||
		!strings.Contains(string(e.Diff), `"pointer":"/user_id","before":"`+olga.PublicID+`"`) {
		t.Errorf("audit of the deletion %+v %s", e, e.Diff)
	}
	c.Set(berlinAt(t, "2026-11-18 20:00"))
	in.End = berlinAt(t, "2026-11-18 23:00")
	if _, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, o.PublicID, nil, in); !errors.Is(err,
		ErrOverrideEnded) {
		t.Errorf("a change after the end = %v", err)
	}
	if err := svc.DeleteOverride(ctx, admin, sc.PublicID, o.PublicID, nil); !errors.Is(err, ErrOverrideEnded) {
		t.Errorf("a deletion after the end = %v", err)
	}
	if err := svc.DeleteOverride(ctx, asPetr, "SC00000000NNNE", o.PublicID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deletion in an unknown schedule = %v", err)
	}
}

// TestListOverrides: the Overrides that end after now by default, or after from and before to, by start, page by
// page.
func TestListOverrides(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{"2026-11-20", "2026-11-18", "2026-11-19"} {
		if _, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: olga.PublicID,
			Start: berlinAt(t, day+" 11:00"), End: berlinAt(t, day+" 12:00")}); err != nil {
			t.Fatal(err)
		}
	}
	store.overrides = append(store.overrides, &overrideRec{id: 999, schedule: store.schedules[0].id, user: dan.ID,
		bySA: 9, version: 1, publicID: "VR00000000PAST", start: t0.Add(-48 * time.Hour), end: t0.Add(-24 * time.Hour),
		created: t0.Add(-72 * time.Hour)})
	starts := func(p OverridePage) string {
		var out []string
		for _, o := range p.Overrides {
			out = append(out, o.Start.In(zone(t, "Europe/Berlin")).Format("01-02"))
		}
		return strings.Join(out, ",")
	}
	page, err := svc.Overrides(ctx, sc.PublicID, OverrideFilter{Limit: 2})
	if err != nil || starts(page) != "11-18,11-19" || page.Next == nil {
		t.Fatalf("first page = %s %+v, %v", starts(page), page.Next, err)
	}
	if page, err = svc.Overrides(ctx, sc.PublicID, OverrideFilter{Limit: 2, After: page.Next}); err != nil ||
		starts(page) != "11-20" || page.Next != nil {
		t.Errorf("second page = %s, %v", starts(page), err)
	}
	from, to := t0.Add(-100*time.Hour), berlinAt(t, "2026-11-19 00:00")
	page, err = svc.Overrides(ctx, sc.PublicID, OverrideFilter{From: &from, To: &to, Limit: 50})
	if err != nil || starts(page) != "10-26,11-18" || page.Overrides[0].CreatedBy.Kind != "service_account" ||
		!page.Overrides[0].CreatedBy.Deactivated {
		t.Errorf("from and to = %s %+v, %v", starts(page), page.Overrides, err)
	}
	if _, err := svc.Overrides(ctx, "SC00000000NNNE", OverrideFilter{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown schedule = %v", err)
	}
	store.fail["ListScheduleOverrides"] = errBoom
	if _, err := svc.Overrides(ctx, sc.PublicID, OverrideFilter{}); !errors.Is(err, errBoom) {
		t.Errorf("the list fails = %v", err)
	}
}

// TestOnCall is C-27.FR-4, FR-5 and C-27.AC-1, AC-2, AC-4 on the business clock: the On-call of scenario 1 on a
// Wednesday at 15:00 and 21:00 and on the Saturday at 15:00; the Override of petr from 11:00 to 21:00 over both
// layers; the default period from now to schedule.gap_view after it; the period refused when it ends before it
// starts or lasts longer than ical.window; a person disabled since falls through to the layer below with a warning.
func TestOnCall(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	who := func(at string) string {
		t.Helper()
		seg, err := svc.OnCallAt(ctx, sc.PublicID, berlinAt(t, at))
		if err != nil {
			t.Fatal(err)
		}
		if seg.User == nil {
			return "gap"
		}
		return seg.User.Login
	}
	for at, want := range map[string]string{"2026-11-04 15:00": "olga", "2026-11-04 21:00": "petr",
		"2026-11-07 15:00": "petr", "2026-11-01 15:00": "gap"} {
		if got := who(at); got != want {
			t.Errorf("On-call at %s = %s, want %s", at, got, want)
		}
	}
	if _, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: petr.PublicID,
		Start: berlinAt(t, "2026-11-18 11:00"), End: berlinAt(t, "2026-11-18 21:00")}); err != nil {
		t.Fatal(err)
	}
	for at, want := range map[string]string{"2026-11-18 10:00": "olga", "2026-11-18 15:00": "petr",
		"2026-11-18 20:00": "petr", "2026-11-18 22:00": "olga"} {
		if got := who(at); got != want {
			t.Errorf("with the Override, On-call at %s = %s, want %s", at, got, want)
		}
	}
	c.Set(berlinAt(t, "2026-11-02 00:00"))
	res, err := svc.OnCall(ctx, sc.PublicID, nil, nil)
	if err != nil || !res.From.Equal(c.Now()) || !res.To.Equal(c.Now().Add(GapView)) || len(res.RotationLayers) != 2 ||
		len(res.Overrides) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("the default period = %+v, %v", res, err)
	}
	store.users[0].Status = "disabled" // petr
	from := berlinAt(t, "2026-11-09 00:00")
	to := from.Add(7 * 24 * time.Hour)
	res, err = svc.OnCall(ctx, sc.PublicID, &from, &to)
	if err != nil {
		t.Fatal(err)
	}
	if got := warningsText(res.Warnings); got != "disabled petr Around the clock\ndisabled petr Working hours 11-19\n" {
		t.Errorf("warnings %q", got)
	}
	if got := who("2026-11-11 15:00"); got != "sergey" {
		t.Errorf("petr disabled, On-call = %s", got)
	}
	for _, c := range []struct{ from, to time.Time }{{to, from}, {from, from}, {from, from.Add(MaxPeriod + 1)}} {
		f := fieldError(t, func() error { _, err := svc.OnCall(ctx, sc.PublicID, &c.from, &c.to); return err }())
		if f.Pointer != "/query/to" || f.Code != CodeOutOfRange {
			t.Errorf("%v to %v = %+v", c.from, c.to, f)
		}
	}
	far, farther := c.Now().AddDate(374, 0, 0), c.Now().AddDate(374, 0, 1)
	if f := fieldError(t, func() error { _, err := svc.OnCall(ctx, sc.PublicID, &far, &farther); return err }()); f.Pointer != "/query/from" || f.Code != CodeOutOfRange {
		t.Errorf("a period in the year 2400 = %+v", f)
	}
	if f := fieldError(t, func() error { _, err := svc.OnCall(ctx, sc.PublicID, nil, &farther); return err }()); f.Pointer != "/query/to" || f.Code != CodeOutOfRange {
		t.Errorf("an end in the year 2400 = %+v", f)
	}
	past := c.Now().AddDate(-11, 0, 0)
	if f := fieldError(t, func() error { _, err := svc.OnCall(ctx, sc.PublicID, &past, nil); return err }()); f.Pointer != "/query/from" {
		t.Errorf("a start 11 years ago = %+v", f)
	}
	if _, err := svc.OnCall(ctx, sc.PublicID, &from, ptr(from.Add(MaxPeriod))); err != nil {
		t.Errorf("the longest period = %v", err)
	}
	if _, err := svc.OnCall(ctx, "SC00000000NNNE", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown schedule = %v", err)
	}
	if _, err := svc.OnCallAt(ctx, "SC00000000NNNE", from); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown schedule at a moment = %v", err)
	}
	for _, name := range []string{"ListPeriodOverrides", "ListPageable"} {
		store.fail[name] = errBoom
		if _, err := svc.OnCall(ctx, sc.PublicID, nil, nil); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		if _, err := svc.OnCallAt(ctx, sc.PublicID, from); !errors.Is(err, errBoom) {
			t.Errorf("at a moment when %s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	store.schedules[0].tz = "Nowhere/Else"
	if res, err := svc.OnCall(ctx, sc.PublicID, &from, &to); err != nil || len(res.Segments) == 0 {
		t.Errorf("an unknown stored time zone reads as UTC = %v", err)
	}
}

// TestOverridePast is the maintainer's rule that an Override may not rewrite the past: it starts no earlier than
// OverrideGrace before now and lasts at most 31 days, however far ahead; once it has begun only its end changes, and
// deleting it ends it now, keeping its past part in the Result and recording schedule_override.deleted with the end;
// a change that changes nothing writes nothing.
func TestOverridePast(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: t0.Add(-4 * time.Minute), End: t0.Add(time.Hour)}); err != nil {
		t.Errorf("a start within the grace = %v", err)
	}
	if _, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: t0.AddDate(3, 0, 0), End: t0.AddDate(3, 0, 0).Add(MaxOverrideLength)}); err != nil {
		t.Errorf("31 days three years ahead = %v", err)
	}
	running, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: petr.PublicID,
		ReplacesUserID: olga.PublicID, Start: t0.Add(time.Hour), End: t0.Add(5 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	same := OverrideInput{UserID: petr.PublicID, ReplacesUserID: olga.PublicID, Start: running.Start, End: running.End}
	audits := len(store.audit)
	if got, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, running.PublicID, ptr(int64(1)), same); err != nil ||
		got.Version != 1 || len(store.audit) != audits {
		t.Errorf("a change that changes nothing = %+v, %v, %d entries", got, err, len(store.audit)-audits)
	}
	c.Set(t0.Add(2 * time.Hour))
	for _, ch := range []struct {
		in      func(OverrideInput) OverrideInput
		pointer string
	}{
		{func(in OverrideInput) OverrideInput { in.Start = in.Start.Add(30 * time.Minute); return in }, "/start"},
		{func(in OverrideInput) OverrideInput { in.UserID = sergey.PublicID; return in }, "/user_id"},
		{func(in OverrideInput) OverrideInput { in.ReplacesUserID = ""; return in }, "/replaces_user_id"},
	} {
		f := fieldError(t, func() error {
			_, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, running.PublicID, nil, ch.in(same))
			return err
		}())
		if f.Pointer != ch.pointer || f.Code != CodeUnsupported {
			t.Errorf("a change of a begun Override = %+v, want %s unsupported", f, ch.pointer)
		}
	}
	longer := same
	longer.End = running.End.Add(time.Hour)
	if got, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, running.PublicID, nil, longer); err != nil ||
		!got.End.Equal(longer.End) || got.Version != 2 {
		t.Errorf("a new end of a begun Override = %+v, %v", got, err)
	}
	store.users[0].Status = "disabled" // petr, the person of the begun Override
	shorter := same
	if _, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, running.PublicID, nil, shorter); err != nil {
		t.Errorf("the end of a begun Override whose person was disabled since = %v", err)
	}
	store.users[0].Status = "active"
	c.Set(t0.Add(3 * time.Hour))
	audits = len(store.audit)
	if err := svc.DeleteOverride(ctx, asPetr, sc.PublicID, running.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	var kept *overrideRec
	for _, o := range store.overrides {
		if o.publicID == running.PublicID {
			kept = o
		}
	}
	e := store.audit[audits]
	if kept == nil || !kept.end.Equal(t0.Add(3*time.Hour)) || !kept.start.Equal(running.Start) ||
		e.Action != ActionOverrideDeleted || string(e.Diff) != `[{"pointer":"/end","before":"`+
		running.End.Format(time.RFC3339)+`","after":"`+t0.Add(3*time.Hour).Format(time.RFC3339)+`"}]` {
		t.Fatalf("a begun Override deleted: kept %+v, audit %s %s", kept, e.Action, e.Diff)
	}
	from, to := t0, t0.Add(4*time.Hour)
	res, err := svc.OnCall(ctx, sc.PublicID, &from, &to)
	if err != nil {
		t.Fatal(err)
	}
	if got := segmentsText(time.UTC, res.Segments); !strings.Contains(got, "10-28 13:00–10-28 15:00 petr "+
		running.PublicID) {
		t.Errorf("the past part of the deleted Override is not in the Result:\n%s", got)
	}
	if err := svc.DeleteOverride(ctx, asPetr, sc.PublicID, running.PublicID, nil); !errors.Is(err, ErrOverrideEnded) {
		t.Errorf("deleting it again = %v", err)
	}
	store.fail["UpdateScheduleOverride"] = errBoom
	soon, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: c.Now().Add(-time.Minute), End: c.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteOverride(ctx, asPetr, sc.PublicID, soon.PublicID, nil); !errors.Is(err, errBoom) {
		t.Errorf("ending a begun Override fails = %v", err)
	}
}

// TestOverlappingOverridesCap: a span that would overlap MaxOverlappingOverrides other Overrides is refused at /end;
// the Override itself does not count on a change.
func TestOverlappingOverridesCap(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	start := t0.Add(time.Hour)
	for i := range MaxOverlappingOverrides - 1 {
		store.overrides = append(store.overrides, &overrideRec{id: int64(10000 + i), schedule: store.schedules[0].id,
			user: olga.ID, byUser: olga.ID, version: 1, publicID: fmt.Sprintf("VR%012d", i), start: start,
			end: start.Add(time.Hour), created: t0})
	}
	last, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: olga.PublicID, Start: start,
		End: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("the 1000th Override = %v", err)
	}
	if _, err := svc.UpdateOverride(ctx, admin, sc.PublicID, last.PublicID, nil, OverrideInput{UserID: olga.PublicID,
		Start: start, End: start.Add(2 * time.Hour)}); err != nil {
		t.Errorf("a change of the 1000th = %v", err)
	}
	f := fieldError(t, func() error {
		_, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: olga.PublicID,
			Start: start.Add(30 * time.Minute), End: start.Add(40 * time.Minute)})
		return err
	}())
	if f.Pointer != "/end" || f.Code != CodeOutOfRange {
		t.Errorf("the 1001st = %+v", f)
	}
	if _, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: start.Add(3 * time.Hour), End: start.Add(4 * time.Hour)}); err != nil {
		t.Errorf("an Override that overlaps none = %v", err)
	}
	store.fail["CountOverlappingOverrides"] = errBoom
	if _, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: start.Add(5 * time.Hour), End: start.Add(6 * time.Hour)}); !errors.Is(err, errBoom) {
		t.Errorf("the count fails = %v", err)
	}
}

// TestOverrideDeletedPeople: the person whose Shift an Override takes may be anyone, a deleted User included; a begun
// Override whose people were deleted since still gets a new end, since they stay as stored; and an Override deleted
// exactly at its start, which has not begun, is removed.
func TestOverrideDeletedPeople(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	store.users = append(store.users, Person{ID: 9, PublicID: "SR0000000000GN", Name: "Gone", Login: "gone",
		Role: "responder", Status: "deleted"})
	o, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: sergey.PublicID,
		ReplacesUserID: "SR0000000000GN", Start: t0.Add(time.Hour), End: t0.Add(3 * time.Hour)})
	if err != nil || o.Replaces == nil || o.Replaces.PublicID != "SR0000000000GN" {
		t.Fatalf("replacing a deleted user = %+v, %v", o, err)
	}
	if f := fieldError(t, func() error {
		_, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: sergey.PublicID,
			ReplacesUserID: "SR00000000NNNE", Start: t0.Add(time.Hour), End: t0.Add(2 * time.Hour)})
		return err
	}()); f.Pointer != "/replaces_user_id" || f.Code != CodeUnknownID {
		t.Errorf("replacing nobody = %+v", f)
	}
	if f := fieldError(t, func() error {
		_, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: "SR0000000000GN",
			Start: t0.Add(time.Hour), End: t0.Add(2 * time.Hour)})
		return err
	}()); f.Pointer != "/user_id" || f.Code != CodeUnknownID {
		t.Errorf("a deleted user taking the Shift = %+v", f)
	}
	c.Set(t0.Add(2 * time.Hour))
	store.users[1].Status = "deleted" // sergey, who takes the Shift
	got, err := svc.UpdateOverride(ctx, asPetr, sc.PublicID, o.PublicID, nil, OverrideInput{UserID: sergey.PublicID,
		ReplacesUserID: "SR0000000000GN", Start: o.Start, End: t0.Add(4 * time.Hour)})
	if err != nil || !got.End.Equal(t0.Add(4*time.Hour)) {
		t.Errorf("a new end of a begun Override whose people were deleted = %+v, %v", got, err)
	}
	store.users[1].Status = "active"
	store.fail["FindUser"] = errBoom
	if _, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: sergey.PublicID,
		ReplacesUserID: olga.PublicID, Start: t0.Add(3 * time.Hour), End: t0.Add(5 * time.Hour)}); !errors.Is(err,
		errBoom) {
		t.Errorf("the lookup fails = %v", err)
	}
	delete(store.fail, "FindUser")

	later, err := svc.CreateOverride(ctx, asPetr, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: t0.Add(5 * time.Hour), End: t0.Add(6 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	c.Set(later.Start)
	n, audits := len(store.overrides), len(store.audit)
	if err := svc.DeleteOverride(ctx, asPetr, sc.PublicID, later.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if e := store.audit[audits]; len(store.overrides) != n-1 || e.Action != ActionOverrideDeleted ||
		!strings.Contains(string(e.Diff), `"pointer":"/user_id","before":"`+olga.PublicID+`"`) {
		t.Errorf("deleted at its start: %d overrides, audit %s %s", len(store.overrides), e.Action, e.Diff)
	}
}
