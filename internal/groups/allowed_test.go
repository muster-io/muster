// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
)

// TestAllowed is C-10.FR-16, C-10.AC-18 and C-10.FR-7: allowed_commands by status for a Responder, the Owner, a
// Viewer and a Service account, and Unresolve on a person-resolved Alert Group only while every precondition holds.
func TestAllowed(t *testing.T) {
	type cmds = []Command
	viewer := carol
	cases := []struct {
		name  string
		setup func(h *harness, g *dbgen.LockGroupsRow)
		c     Caller
		want  cmds
	}{
		{"firing, responder", nil, alice, cmds{CommandAcknowledge, CommandResolve, CommandSnooze}},
		{"firing, service account", nil, robot, cmds{CommandResolve, CommandSnooze}},
		{"firing, viewer", nil, viewer, cmds{}},
		{"acknowledged, owner", func(h *harness, g *dbgen.LockGroupsRow) { h.acknowledge(g) }, alice,
			cmds{CommandUnacknowledge, CommandResolve, CommandSnooze}},
		{"acknowledged, another user", func(h *harness, g *dbgen.LockGroupsRow) { h.acknowledge(g) }, bob,
			cmds{CommandAcknowledge, CommandUnacknowledge, CommandResolve, CommandSnooze}},
		{"acknowledged, service account", func(h *harness, g *dbgen.LockGroupsRow) { h.acknowledge(g) }, robot,
			cmds{CommandUnacknowledge, CommandResolve, CommandSnooze}},
		{"snoozed, responder", func(h *harness, g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), false) },
			bob, cmds{CommandAcknowledge, CommandResolve, CommandSnooze, CommandUnsnooze}},
		{"resolved by the system", func(_ *harness, g *dbgen.LockGroupsRow) {
			g.Status, g.ResolvedAt, g.ResolvedByKind = "resolved", ts(t0), txt(ResolvedBySystem)
			g.ResolveReason = txt("resolved")
		}, alice, cmds{}},
		{"resolved by a person, alerts firing", func(h *harness, g *dbgen.LockGroupsRow) {
			h.personResolve(g, time.Minute)
		}, alice, cmds{CommandUnresolve}},
		{"resolved by a person, viewer", func(h *harness, g *dbgen.LockGroupsRow) {
			h.personResolve(g, time.Minute)
		}, viewer, cmds{}},
		{"resolved by a person, no alert firing", func(h *harness, g *dbgen.LockGroupsRow) {
			h.personResolve(g, time.Minute)
			h.resolve(t, h.db.members[0].alert)
		}, alice, cmds{}},
		{"resolved by a person, a newer open one", func(h *harness, g *dbgen.LockGroupsRow) {
			h.personResolve(g, time.Minute)
			b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
			h.changes(t, ingest.ChangeFired, b)
		}, alice, cmds{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.people()
			g := h.firing(t, "x")
			if c.setup != nil {
				c.setup(h, g)
			}
			v, err := h.svc.Get(t.Context(), g.PublicID)
			if err != nil {
				t.Fatal(err)
			}
			if got := v.Allowed(c.c); !slices.Equal(got, c.want) {
				t.Errorf("get: allowed %v, want %v", got, c.want)
			}
			page := h.list(t, ListRequest{Filter: Filter{Statuses: []Status{StatusFiring, StatusAcknowledged,
				StatusSnoozed, StatusResolved}}})
			i := slices.IndexFunc(page.Groups, func(x View) bool { return x.PublicID == g.PublicID })
			if i < 0 {
				t.Fatalf("not listed: %v", numbers(page))
			}
			if got := page.Groups[i].Allowed(c.c); !slices.Equal(got, c.want) {
				t.Errorf("list: allowed %v, want %v", got, c.want)
			}
		})
	}
}

// TestOwnerAndSnoozeInReads is C-09.FR-1: getAlertGroup and the items of listAlertGroups carry the Owner, the Snooze
// end and who set the Snooze as the Commands set them.
func TestOwnerAndSnoozeInReads(t *testing.T) {
	h := newHarness(t)
	h.people()
	a, b := h.firing(t, "a"), h.firing(t, "b")
	if _, err := h.svc.Acknowledge(t.Context(), bob, a.PublicID); err != nil {
		t.Fatal(err)
	}
	until := h.clock.Now().Add(time.Hour)
	if _, err := h.svc.Snooze(t.Context(), robot, b.PublicID, SnoozeEnd{Until: &until}); err != nil {
		t.Fatal(err)
	}
	page := h.list(t, ListRequest{})
	for _, v := range page.Groups {
		switch v.PublicID {
		case a.PublicID:
			if v.Owner == nil || v.Owner.Name != "Bob" || v.Owner.Login != "bob" || v.SnoozedBy != nil {
				t.Errorf("a = %+v", v)
			}
		case b.PublicID:
			if v.Owner != nil || v.SnoozeUntil == nil || !v.SnoozeUntil.Equal(until) || v.SnoozedBy == nil ||
				v.SnoozedBy.Kind != "service_account" || v.SnoozedBy.Name != "robot" {
				t.Errorf("b = %+v", v)
			}
		}
	}
	v, err := h.svc.Get(t.Context(), b.PublicID)
	if err != nil || v.SnoozedBy == nil || v.SnoozedBy.Name != "robot" || v.SnoozeUntil == nil {
		t.Errorf("get b = %v %+v", err, v)
	}
}
