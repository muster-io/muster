// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

// outcomes are the outcome, code and message of each item.
func outcomes(items []BulkItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = string(it.Outcome) + " " + it.Code + " " + it.Message
	}
	return out
}

// TestBulkResolve is C-10.AC-11: a bulk Resolve of three Alert Groups, one already resolved, resolves two, refuses
// one with already_resolved and writes two Audit log entries; an unknown id fails with not found, in request order.
func TestBulkResolve(t *testing.T) {
	h := newHarness(t)
	h.people()
	a, b, c := h.firing(t, "a"), h.firing(t, "b"), h.firing(t, "c")
	if _, err := h.svc.Resolve(t.Context(), alice, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	audits := len(h.db.audit)
	items, err := h.svc.Bulk(t.Context(), bobToken, BulkRequest{Command: CommandResolve,
		IDs: []string{a.PublicID, b.PublicID, c.PublicID, "AGZZZZZZZZZZZZ"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"done  ", "done  ", "refused already_resolved already resolved", "failed not_found not found"}
	if got := outcomes(items); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("outcomes %q", got)
	}
	if *items[0].Number != a.Number || *items[2].Number != c.Number || items[3].Number != nil ||
		items[3].PublicID != "AGZZZZZZZZZZZZ" {
		t.Errorf("items %+v", items)
	}
	if n := len(h.db.audit) - audits; n != 2 || h.db.audit[audits].Action != ActionResolved ||
		h.db.audit[audits].TokenName.String != "script" || h.db.audit[audits].Transport != "api" {
		t.Errorf("%d audit entries %+v", n, h.db.audit[audits:])
	}
	if a.Status != "resolved" || b.Status != "resolved" {
		t.Errorf("statuses %s %s", a.Status, b.Status)
	}
}

// TestBulkAcknowledge is C-10.AC-15 and C-10.AC-18: a bulk Acknowledge by Bob of three firing Alert Groups, one owned
// by Alice, acknowledges two and skips the third "skipped: owned by Alice" without a takeover; one Bob owns already
// is unchanged; a Service account is refused for every item with owner_must_be_user.
func TestBulkAcknowledge(t *testing.T) {
	h := newHarness(t)
	h.people()
	a, b, c, d := h.firing(t, "a"), h.firing(t, "b"), h.firing(t, "c"), h.firing(t, "d")
	if _, err := h.svc.Acknowledge(t.Context(), alice, c.PublicID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Acknowledge(t.Context(), bob, d.PublicID); err != nil {
		t.Fatal(err)
	}
	ids := []string{a.PublicID, b.PublicID, c.PublicID, d.PublicID}
	items, err := h.svc.Bulk(t.Context(), bob, BulkRequest{Command: CommandAcknowledge, IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	want := "done  |done  |skipped owned_by_other skipped: owned by Alice|unchanged  "
	if got := strings.Join(outcomes(items), "|"); got != want {
		t.Errorf("outcomes %q", got)
	}
	if c.OwnerUserID != i8(owner) || a.OwnerUserID != i8(bobID) {
		t.Errorf("owners %v %v", c.OwnerUserID, a.OwnerUserID)
	}
	for _, g := range []*dbgen.LockGroupsRow{a, b, c, d} {
		for _, e := range h.entriesOf(g) {
			if e.Event.String == "takeover" {
				t.Errorf("a takeover on #%d", g.Number)
			}
		}
	}
	items, err = h.svc.Bulk(t.Context(), robot, BulkRequest{Command: CommandAcknowledge, IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Outcome != BulkRefused || it.Code != CodeOwnerMustBeUser {
			t.Errorf("robot %+v", it)
		}
	}
	// The name of an Owner that cannot be read.
	h.db.fail["ListUserRefs"] = errBoom
	if _, err := h.svc.Bulk(t.Context(), bob, BulkRequest{Command: CommandAcknowledge, IDs: ids[2:3]}); !errors.Is(err,
		errBoom) {
		t.Errorf("owner name failing = %v", err)
	}
	delete(h.db.fail, "ListUserRefs")
	delete(h.db.users, owner)
	if items, err := h.svc.Bulk(t.Context(), bob, BulkRequest{Command: CommandAcknowledge, IDs: ids[2:3]}); err != nil ||
		items[0].Message != "skipped: owned by another user" {
		t.Errorf("an owner without a row = %v %+v", err, items)
	}
}

// TestBulkRequest is C-10.AC-17 and C-10.FR-14: the command and the number of ids are checked first, then the
// Permission of the command once for the whole request, then its arguments: a Snooze needs its end; Unsnooze of
// snoozed Alert Groups.
func TestBulkRequest(t *testing.T) {
	h := newHarness(t)
	h.people()
	a := h.firing(t, "a")
	ids := []string{a.PublicID}
	future := h.clock.Now().Add(time.Hour)
	for _, c := range []struct {
		r    BulkRequest
		want string
	}{
		{BulkRequest{Command: CommandUnresolve, IDs: ids}, "/command invalid_format"},
		{BulkRequest{Command: CommandAddNote, IDs: ids}, "/command invalid_format"},
		{BulkRequest{Command: "nonsense", IDs: ids}, "/command invalid_format"},
		{BulkRequest{Command: CommandResolve}, "/alert_group_ids out_of_range"},
		{BulkRequest{Command: CommandResolve, IDs: make([]string, BulkMax+1)}, "/alert_group_ids out_of_range"},
		{BulkRequest{Command: CommandSnooze, IDs: ids}, "/snooze required"},
		{BulkRequest{Command: CommandSnooze, IDs: ids, Snooze: &SnoozeEnd{}}, "/snooze one_of_required"},
		{BulkRequest{Command: CommandSnooze, IDs: ids, Snooze: &SnoozeEnd{Until: ptr(t0.Add(-time.Hour))}},
			"/snooze/until out_of_range"},
		{BulkRequest{Command: CommandResolve, IDs: ids, Note: ptr("")}, "/note required"},
	} {
		if _, err := h.svc.Bulk(t.Context(), alice, c.r); code(err) != c.want {
			t.Errorf("%+v = %v, want %s", c.r, err, c.want)
		}
	}
	locks := h.db.calls["LockGroups"]
	if _, err := h.svc.Bulk(t.Context(), carol, BulkRequest{Command: CommandSnooze, IDs: ids}); code(err) !=
		CodeForbidden {
		t.Errorf("a viewer's bulk snooze without its end = %v", err)
	}
	_, err := h.svc.Bulk(t.Context(), carol, BulkRequest{Command: CommandResolve, IDs: ids})
	if f, ok := errors.AsType[*ForbiddenError](err); !ok || f.Permission != PermissionResolve ||
		h.db.calls["LockGroups"] != locks {
		t.Errorf("a viewer's bulk resolve = %v", err)
	}
	if !strings.Contains(h.log.String(), `"event":"command_refused","command":"resolve","group":"","actor":"SRAAAAAAAAAAB1"`) {
		t.Errorf("log %s", h.log)
	}
	if items, err := h.svc.Bulk(t.Context(), alice, BulkRequest{Command: CommandUnsnooze, IDs: ids,
		Note: ptr("ignored")}); err != nil || items[0].Code != CodeNotSnoozed {
		t.Errorf("a note on another command = %v %+v", err, items)
	}
	items, err := h.svc.Bulk(t.Context(), alice, BulkRequest{Command: CommandSnooze, IDs: ids,
		Snooze: &SnoozeEnd{Until: &future}})
	if err != nil || items[0].Outcome != BulkDone || !a.SnoozeUntil.Time.Equal(future) {
		t.Fatalf("bulk snooze = %v %+v", err, items)
	}
	items, err = h.svc.Bulk(t.Context(), alice, BulkRequest{Command: CommandUnsnooze, IDs: ids})
	if err != nil || items[0].Outcome != BulkDone || a.Status != "firing" {
		t.Errorf("bulk unsnooze = %v %+v", err, items)
	}
	h.db.fail["LockGroups"] = errBoom
	if _, err := h.svc.Bulk(t.Context(), alice, BulkRequest{Command: CommandResolve, IDs: ids}); !errors.Is(err,
		errBoom) {
		t.Errorf("a failing item = %v", err)
	}
}

// TestBulkSnoozePreset is C-10.FR-18 and C-10.AC-22 for a bulk Snooze: the check of the Snooze at /snooze, and the end
// of a preset computed once before the first Alert Group, so that every Alert Group gets the same end even when the
// business clock passes the start of the working day in between; each has its own Audit log entry naming the preset.
func TestBulkSnoozePreset(t *testing.T) {
	h := newHarness(t)
	h.people()
	a, b := h.firing(t, "a"), h.firing(t, "b")
	ids := []string{a.PublicID, b.PublicID}
	future := h.clock.Now().Add(time.Hour)
	for _, c := range []struct {
		end  SnoozeEnd
		want string
	}{
		{SnoozeEnd{Preset: PresetTomorrow, Until: &future}, "/snooze one_of_required"},
		{SnoozeEnd{Until: &future, TimeZone: ptr("UTC")}, "/snooze/time_zone unsupported"},
		{SnoozeEnd{Preset: PresetWeekStart, TimeZone: ptr("Mars/Base")}, "/snooze/time_zone invalid_format"},
	} {
		if _, err := h.svc.Bulk(t.Context(), alice, BulkRequest{Command: CommandSnooze, IDs: ids,
			Snooze: &c.end}); code(err) != c.want {
			t.Errorf("%+v = %v, want %s", c.end, err, c.want)
		}
	}
	h.db.fail["GetListSettings"] = errBoom
	locks := h.db.calls["LockGroups"]
	if _, err := h.svc.Bulk(t.Context(), alice, BulkRequest{Command: CommandSnooze, IDs: ids,
		Snooze: &SnoozeEnd{Preset: PresetTomorrow}}); !errors.Is(err, errBoom) || h.db.calls["LockGroups"] != locks {
		t.Errorf("settings failing = %v", err)
	}
	delete(h.db.fail, "GetListSettings")
	// One second before 09:00 UTC; the clock passes 09:00 while the first Alert Group is locked.
	h.clock.Set(time.Date(2026, 10, 8, 8, 59, 59, 0, time.UTC))
	h.db.before["LockGroups"] = func() { h.clock.Advance(2 * time.Second) }
	end := &SnoozeEnd{Preset: PresetTomorrow, TimeZone: ptr("UTC")}
	audits, reads := len(h.db.audit), h.db.calls["GetListSettings"]
	items, err := h.svc.Bulk(t.Context(), bobToken, BulkRequest{Command: CommandSnooze, IDs: ids, Snooze: end})
	want := ts(time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC))
	if err != nil || strings.Join(outcomes(items), "|") != "done  |done  " || a.SnoozeUntil != want ||
		b.SnoozeUntil != want {
		t.Fatalf("bulk snooze = %v %+v, ends %v %v", err, items, a.SnoozeUntil, b.SnoozeUntil)
	}
	if _, pending := h.db.before["LockGroups"]; pending || !h.clock.Now().After(want.Time) {
		t.Error("the clock did not pass 09:00 during the bulk Snooze")
	}
	if n := h.db.calls["GetListSettings"] - reads; n != 1 || end.Until != nil {
		t.Errorf("%d reads of the settings, the request's end %v", n, end.Until)
	}
	if n := len(h.db.audit) - audits; n != 2 {
		t.Fatalf("%d audit entries", n)
	}
	for _, e := range h.db.audit[audits:] {
		if string(e.Details) != `{"no_end":false,"preset":"tomorrow","time_zone":"UTC","until":"2026-10-08T09:00:00Z"}` {
			t.Errorf("details %s", e.Details)
		}
	}
	// The week starts on the first day of the week of the Organization, in the caller's profile time zone.
	h.db.zones[bobID] = "Asia/Tokyo"
	if _, err := h.svc.Bulk(t.Context(), bobToken, BulkRequest{Command: CommandSnooze, IDs: ids,
		Snooze: &SnoozeEnd{Preset: PresetWeekStart}}); err != nil || a.SnoozeUntil != b.SnoozeUntil ||
		a.SnoozeUntil != ts(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("week_start = %v %v %v", err, a.SnoozeUntil, b.SnoozeUntil)
	}
}
