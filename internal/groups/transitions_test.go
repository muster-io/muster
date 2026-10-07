// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/metrics"
)

// TestResolution is C-09.FR-3, C-09.FR-10 and C-06.FR-4: an Alert resolving while others fire is an alert_resolved;
// when the last one resolves the system resolves the Alert Group with that Alert's reason, keeps the status to return
// to and sets the Reopen window with its timer.
func TestResolution(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, a, b)
	g := h.groupOf(t, a)
	h.acknowledge(g)
	h.clock.Advance(10 * time.Minute)
	h.resolve(t, a)
	if g.Status != "acknowledged" || g.FiringAlertCount != 1 || g.ResolvedAlertCount != 1 ||
		h.last(t, g).Event.String != "alert_resolved" {
		t.Fatalf("one of two resolved: %+v", g)
	}
	h.db.alerts[b].Status = "resolved"
	out, err := h.svc.AlertChanges(t.Context(), nil, []ingest.AlertChange{{Kind: ingest.ChangeResolved, AlertID: b,
		Reason: "integration_deleted", ReasonText: "Integration lab deleted"}})
	if err != nil {
		t.Fatal(err)
	}
	out.Committed(t.Context())
	e := h.last(t, g)
	if g.Status != "resolved" || g.ResolvedByKind.String != "system" || g.ResolveReason.String != "integration_deleted" ||
		g.ResolveReasonText.String != "Integration lab deleted" || !g.ResolvedAt.Time.Equal(h.clock.Now()) ||
		g.OwnerUserID.Valid || g.AcknowledgedAt.Valid || g.PriorStatus.String != "acknowledged" ||
		g.PriorOwnerUserID.Int64 != 9 || !g.ReopenDeadline.Time.Equal(h.clock.Now().Add(15*time.Minute)) {
		t.Errorf("resolved %+v", g)
	}
	if e.Event.String != "resolved" || e.Reason.String != "integration_deleted" || e.FromStatus.String != "acknowledged" ||
		e.ToStatus.String != "resolved" || !slices.Equal(e.Fingerprints, []string{fingerprintOf(b)}) {
		t.Errorf("entry %+v", e.InsertTimelineEntryParams)
	}
	if d, ok := h.db.timers[timerKey(g.ID, TimerReopenWindowEnd)]; !ok || !d.Equal(*timeOf(g.ReopenDeadline)) ||
		h.db.notified != 1 {
		t.Errorf("timers %v, notified %d", h.db.timers, h.db.notified)
	}
	scrape := metricsText(t)
	if !strings.Contains(scrape, `muster_alert_groups_resolved_total{route="RTDBAAAAAAAAAA",by="system"}`) ||
		!strings.Contains(scrape, `muster_alert_group_time_to_resolve_seconds_bucket{route="RTDBAAAAAAAAAA",le="600"} `) {
		t.Errorf("metrics: %s", scrape)
	}
}

// TestNoReopenWindow: a Route without a Reopen window, and an Alert Group moved to the Default route, resolve without
// one; a resolved Alert of a person-resolved Alert Group ends its membership only.
func TestNoReopenWindow(t *testing.T) {
	h := newHarness(t)
	h.db.routes[3].ReopenWindowSeconds = 0
	a := h.alert(3, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	h.resolve(t, a)
	if g := h.groupOf(t, a); g.Status != "resolved" || g.ReopenDeadline.Valid || g.PriorStatus.Valid {
		t.Errorf("no window: %+v", g)
	}
	b := h.alert(2, "warning", map[string]string{"alertname": "B", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, b)
	gb := h.groupOf(t, b)
	gb.RouteID, gb.MovedFromRouteID = 1, i8(2)
	h.resolve(t, b)
	if gb.Status != "resolved" || gb.ReopenDeadline.Valid || len(h.db.timers) != 0 {
		t.Errorf("moved: %+v", gb)
	}
	c := h.alert(2, "warning", map[string]string{"alertname": "C", "cluster": "x", "n": "1"})
	d := h.alert(2, "warning", map[string]string{"alertname": "C", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, c, d)
	gc := h.groupOf(t, c)
	gc.Status, gc.ResolvedAt, gc.ResolvedByKind, gc.ResolvedByUserID = "resolved", ts(t0), txt("user"), i8(9)
	h.resolve(t, c, d)
	if gc.ResolvedByKind.String != "user" || gc.FiringAlertCount != 0 || h.last(t, gc).Event.String != "alert_resolved" {
		t.Errorf("person-resolved: %+v %v", gc, h.events(gc))
	}
}

// TestReopen is C-09.FR-4, C-09.AC-1, AC-22 and AC-2: within the Reopen window an Alert with the same Route and
// Group key values — one of its own or another fingerprint — reopens the same #N into its prior status with its
// Reopen count increased; after the window a new #N starts.
func TestReopen(t *testing.T) {
	h := newHarness(t)
	j1 := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "b", "pod": "j1"})
	h.changes(t, ingest.ChangeFired, j1)
	g := h.groupOf(t, j1)
	h.resolve(t, j1)
	h.clock.Advance(8 * time.Minute)
	out := h.refire(t, j1)
	e := h.last(t, g)
	if g.Status != "firing" || g.ReopenCount != 1 || g.Number != 1 || len(h.db.groups) != 1 || g.ResolvedAt.Valid ||
		g.ReopenDeadline.Valid || g.PriorStatus.Valid || e.Event.String != "reopened" || e.Loudness.String != "loud" ||
		!slices.Equal(e.Mentions, []string{"reopen"}) || e.FromStatus.String != "resolved" ||
		!slices.Equal(out.AlertGroups, []int64{1}) || len(h.db.timers) != 0 {
		t.Fatalf("reopened %+v, entry %+v", g, e.InsertTimelineEntryParams)
	}
	if !strings.Contains(metricsText(t), `muster_alert_groups_reopened_total{route="RTDBAAAAAAAAAA"}`) {
		t.Error("reopen not counted")
	}
	// Another fingerprint with the same key reopens it too.
	h.resolve(t, j1)
	h.clock.Advance(time.Minute)
	j2 := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "b", "pod": "j2"})
	h.changes(t, ingest.ChangeFired, j2)
	if h.groupOf(t, j2) != g || g.ReopenCount != 2 || len(h.db.groups) != 1 {
		t.Fatalf("another fingerprint: %+v", g)
	}
	// After the window a new #N starts.
	h.resolve(t, j1, j2)
	h.clock.Advance(16 * time.Minute)
	j3 := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "b", "pod": "j3"})
	h.changes(t, ingest.ChangeFired, j3)
	if n := h.groupOf(t, j3); n == g || n.Number != 2 || n.Status != "firing" {
		t.Errorf("after the window: #%d", n.Number)
	}
}

// TestReopenPriorStatus is C-09.FR-4: a Reopen returns to acknowledged with the same Owner while that Owner is active,
// to snoozed while the Snooze end is ahead or without an end, and to firing otherwise.
func TestReopenPriorStatus(t *testing.T) {
	cases := []struct {
		name  string
		set   func(h *harness, g *dbgen.LockGroupsRow)
		after time.Duration
		want  string
	}{
		{"acknowledged", func(h *harness, g *dbgen.LockGroupsRow) { h.acknowledge(g) }, time.Minute, "acknowledged"},
		{"owner disabled meanwhile", func(h *harness, g *dbgen.LockGroupsRow) {
			h.acknowledge(g)
			r := h.db.users[9]
			r.Status = "disabled"
			h.db.users[9] = r
		}, time.Minute, "firing"},
		{"snoozed ahead", func(h *harness, g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), true) },
			time.Minute, "snoozed"},
		{"snooze ended meanwhile", func(h *harness, g *dbgen.LockGroupsRow) {
			h.snooze(g, t0.Add(5*time.Minute), false)
		}, 10 * time.Minute, "firing"},
		{"snoozed without an end", func(_ *harness, g *dbgen.LockGroupsRow) {
			g.Status, g.SnoozeNoEnd, g.SnoozedByServiceAccountID = "snoozed", true, i8(4)
		}, 10 * time.Minute, "snoozed"},
		{"snoozed by nobody", func(_ *harness, g *dbgen.LockGroupsRow) {
			g.Status, g.SnoozeUntil = "snoozed", ts(t0.Add(time.Hour))
		}, time.Minute, "firing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
			h.changes(t, ingest.ChangeFired, a)
			g := h.groupOf(t, a)
			c.set(h, g)
			h.resolve(t, a)
			if g.SnoozeUntil.Valid || g.SnoozeNoEnd || g.OwnerUserID.Valid || g.SnoozedByUserID.Valid {
				t.Fatalf("the resolve kept a snooze or an owner: %+v", g)
			}
			h.clock.Advance(c.after)
			h.refire(t, a)
			if g.Status != c.want || h.last(t, g).ToStatus.String != c.want {
				t.Errorf("reopened into %s, want %s", g.Status, c.want)
			}
			switch c.want {
			case "acknowledged":
				if g.OwnerUserID.Int64 != 9 || !g.AcknowledgedAt.Time.Equal(h.clock.Now()) ||
					h.last(t, g).OwnerUserID.Int64 != 9 {
					t.Errorf("owner %+v", g)
				}
			case "snoozed":
				if !g.SnoozedByUserID.Valid && !g.SnoozedByServiceAccountID.Valid {
					t.Errorf("snoozer %+v", g)
				}
				if g.SnoozeNoEnd && g.SnoozeUntil.Valid {
					t.Errorf("a snooze without an end has an end: %+v", g)
				}
			}
		})
	}
}

// TestRise is C-09.FR-9, C-08.FR-6 and C-09.AC-12: a rise to critical in a Route whose Group key lacks severity makes
// a firing Alert Group Urgent with a Quiet urgency_raised; a snoozed one stays snoozed when the Snooze was set while
// Urgent; an acknowledged one keeps its Owner when the Route says so; a rise that leaves urgency unchanged is a Quiet
// severity_raised.
func TestRise(t *testing.T) {
	h := newHarness(t)
	g := h.rise(t, nil)
	if g.SeverityLevel != "critical" || !g.Urgent || g.Status != "firing" || h.last(t, g).Event.String != "urgency_raised" ||
		h.last(t, g).Loudness.String != "quiet" {
		t.Errorf("firing: %+v %v", g, h.events(g))
	}
	h = newHarness(t)
	g = h.rise(t, func(g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), true) })
	if g.Status != "snoozed" || h.last(t, g).Loudness.String != "quiet" {
		t.Errorf("snoozed while urgent: %+v", g)
	}
	h = newHarness(t)
	h.db.routes[2].UrgentRiseRemovesAck = false
	g = h.rise(t, func(g *dbgen.LockGroupsRow) { h.acknowledge(g) })
	if g.Status != "acknowledged" || g.OwnerUserID.Int64 != 9 || h.last(t, g).Loudness.String != "quiet" {
		t.Errorf("kept the acknowledgement: %+v", g)
	}
	h = newHarness(t)
	g = h.rise(t, func(g *dbgen.LockGroupsRow) { h.acknowledge(g) })
	if e := h.last(t, g); g.Status != "firing" || g.OwnerUserID.Valid || e.PreviousOwnerUserID.Int64 != 9 ||
		e.FromStatus.String != "acknowledged" || e.ToStatus.String != "firing" {
		t.Errorf("removed the acknowledgement: %+v", e.InsertTimelineEntryParams)
	}
	// Without critical is Urgent a rise to critical leaves urgency unchanged; so does a rise of an Urgent one.
	h = newHarness(t)
	h.db.settings.CriticalIsUrgent = false
	g = h.rise(t, nil)
	if g.Urgent || g.SeverityLevel != "critical" || h.last(t, g).Event.String != "severity_raised" {
		t.Errorf("not urgent: %+v", g)
	}
	h = newHarness(t)
	h.db.routes[2].Urgent = true
	a := h.alert(2, "info", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g = h.groupOf(t, a)
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, b)
	if !g.Urgent || h.last(t, g).Event.String != "severity_raised" {
		t.Errorf("urgent route: %v", h.events(g))
	}
	// A lower or equal level changes nothing about the level.
	c := h.alert(2, "info", map[string]string{"alertname": "A", "cluster": "x", "n": "3"})
	h.changes(t, ingest.ChangeFired, c)
	if g.SeverityLevel != "warning" || h.last(t, g).Event.String != "alerts_added" {
		t.Errorf("a lower level: %s %v", g.SeverityLevel, h.events(g))
	}
}

// TestConfigurationEdits is C-08.FR-8 and C-09.AC-8: marking a Route urgent changes the status of none of its open
// Alert Groups, also when a new Alert joins at the same level.
func TestConfigurationEdits(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	h.acknowledge(g)
	h.db.routes[2].Urgent = true
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, b)
	if g.Status != "acknowledged" || g.OwnerUserID.Int64 != 9 {
		t.Errorf("an edit changed the status: %+v", g)
	}
}

// TestEndReopenWindow is the timer reopen_window_end (C-09.FR-12): once due it forgets the prior status, without a
// Timeline entry; before its deadline, or for an Alert Group without a window, it changes nothing.
func TestEndReopenWindow(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	h.resolve(t, a)
	entries := len(h.entriesOf(g))
	if err := h.svc.EndReopenWindow(t.Context(), nil, g.ID); err != nil || !g.ReopenDeadline.Valid {
		t.Fatalf("early: %v, %+v", err, g)
	}
	h.clock.Advance(15 * time.Minute)
	if err := h.svc.EndReopenWindow(t.Context(), nil, g.ID); err != nil || g.ReopenDeadline.Valid ||
		g.PriorStatus.Valid || len(h.entriesOf(g)) != entries {
		t.Errorf("due: %v, %+v", err, g)
	}
	if err := h.svc.EndReopenWindow(t.Context(), nil, 999); err != nil {
		t.Errorf("unknown: %v", err)
	}
	h.refire(t, a)
	if g.Status != "resolved" || len(h.db.groups) != 2 {
		t.Error("reopened after the window ended")
	}
	for _, q := range []string{"LockGroups", "SaveGroup"} {
		h := newHarness(t)
		a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
		h.changes(t, ingest.ChangeFired, a)
		h.resolve(t, a)
		h.clock.Advance(time.Hour)
		h.db.fail[q] = errBoom
		if err := h.svc.EndReopenWindow(t.Context(), nil, h.groupOf(t, a).ID); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", q, err)
		}
	}
}

// personResolve resolves an Alert Group as a person does from S-032, its Alerts still firing, with a Grace period.
func (h *harness) personResolve(g *dbgen.LockGroupsRow, grace time.Duration) {
	g.Status, g.ResolvedAt, g.ResolvedByKind, g.ResolvedByUserID = "resolved", ts(h.clock.Now()), txt("user"), i8(9)
	g.GraceDeadline = ts(h.clock.Now().Add(grace))
}

// TestGracePeriod is C-09.FR-5 and C-09.FR-12: within the Grace period a new Alert with the same key starts a new
// Alert Group at once; when it ends, the Alerts still firing join that open Alert Group and leave the resolved one.
func TestGracePeriod(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, a, b)
	g := h.groupOf(t, a)
	h.personResolve(g, 15*time.Minute)
	c := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "3"})
	h.changes(t, ingest.ChangeFired, c)
	n := h.groupOf(t, c)
	if n == g || n.Status != "firing" || g.FiringAlertCount != 2 {
		t.Fatalf("a new alert within the grace period joined #%d", n.Number)
	}
	if out, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID); err != nil || out.AlertGroups != nil ||
		!g.GraceDeadline.Valid {
		t.Fatalf("early: %v %+v", err, out)
	}
	h.clock.Advance(15 * time.Minute)
	out, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h.groupOf(t, a) != n || h.groupOf(t, b) != n || n.FiringAlertCount != 3 || g.FiringAlertCount != 0 ||
		g.GraceDeadline.Valid || !slices.Equal(out.AlertGroups, []int64{n.Number}) ||
		h.last(t, n).Event.String != "alerts_added" {
		t.Errorf("tails: %+v %v", n, h.events(n))
	}
	moved := 0
	for _, m := range h.db.members {
		if m.group == g.ID && m.state == "moved" && m.movedTo == n.ID {
			moved++
		}
	}
	if moved != 2 {
		t.Errorf("%d memberships moved", moved)
	}
	// Running it again changes nothing.
	if out, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID); err != nil || out.AlertGroups != nil {
		t.Errorf("twice: %v %+v", err, out)
	}
}

// TestGracePeriodStartsAgain: tails with no open Alert Group of their key start a new one marked as firing again
// after the manual resolve, or reopen a system-resolved one inside its window; a deleted Route sends them to the
// Default route; an Alert Group without tails only ends its Grace period.
func TestGracePeriodStartsAgain(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	h.personResolve(g, 0)
	out, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := h.groupOf(t, a)
	if out.Committed != nil {
		out.Committed(t.Context())
	}
	if n == g || n.FiringAgainAfterID.Int64 != g.ID || n.Number != 2 || h.events(n)[0] != "created" {
		t.Errorf("firing again: %+v", n)
	}
	v, err := h.svc.Get(t.Context(), n.PublicID)
	if err != nil || len(v.Notices) != 1 || v.Notices[0].Kind != NoticeFiringAgainAfterManualResolve ||
		*v.Notices[0].ResolvedNumber != 1 {
		t.Errorf("notice %+v, %v", v.Notices, err)
	}
	// A system-resolved Alert Group of the key inside its window reopens.
	b := h.alert(2, "warning", map[string]string{"alertname": "B", "cluster": "x", "n": "1"})
	c := h.alert(2, "warning", map[string]string{"alertname": "B", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, b)
	gb := h.groupOf(t, b)
	h.changes(t, ingest.ChangeFired, c)
	h.resolve(t, c)
	if gb.Status != "firing" {
		t.Fatal("setup")
	}
	h.resolve(t, b)
	h.db.alerts[b].Status = "firing"
	for _, m := range h.db.members {
		if m.alert == b {
			m.state, m.ended, m.group = "firing", nil, gb.ID
		}
	}
	// b fires on as the tail of another, person-resolved Alert Group.
	other := h.alert(2, "warning", map[string]string{"alertname": "B", "cluster": "other"})
	h.changes(t, ingest.ChangeFired, other)
	go1 := h.groupOf(t, other)
	for _, m := range h.db.members {
		if m.alert == b && m.state == "firing" {
			m.group = go1.ID
		}
	}
	h.personResolve(go1, 0)
	if _, err := h.svc.EndGracePeriod(t.Context(), nil, go1.ID); err != nil {
		t.Fatal(err)
	}
	if h.groupOf(t, b) != gb || gb.Status != "firing" || gb.ReopenCount != 1 {
		t.Errorf("reopen: %+v %v", gb, h.events(gb))
	}
	// A deleted Route sends the tails to the Default route.
	d := h.alert(3, "warning", map[string]string{"alertname": "D", "cluster": "z"})
	h.changes(t, ingest.ChangeFired, d)
	gd := h.groupOf(t, d)
	h.personResolve(gd, 0)
	h.db.routes[3].deleted = true
	if _, err := h.svc.EndGracePeriod(t.Context(), nil, gd.ID); err != nil {
		t.Fatal(err)
	}
	if nd := h.groupOf(t, d); nd.RouteID != 1 || len(h.restamps) != 1 {
		t.Errorf("deleted route: %+v %v", nd, h.restamps)
	}
	// No tails: the Grace period ends.
	e := h.alert(2, "warning", map[string]string{"alertname": "E", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, e)
	ge := h.groupOf(t, e)
	h.resolve(t, e)
	h.personResolve(ge, 0)
	if out, err := h.svc.EndGracePeriod(t.Context(), nil, ge.ID); err != nil || ge.GraceDeadline.Valid ||
		out.AlertGroups != nil {
		t.Errorf("no tails: %v %+v", err, ge)
	}
	if _, err := h.svc.EndGracePeriod(t.Context(), nil, 999); err != nil {
		t.Errorf("unknown: %v", err)
	}
}

// TestGracePeriodFailures: every query error of the Grace period end reaches the timer.
func TestGracePeriodFailures(t *testing.T) {
	for _, q := range []string{"PeekGroup", "EnsureCounter", "LockCounter", "LockGroups", "ListGroupFiringAlerts", "LockRoutes",
		"FindOpenGroup", "MoveMemberships", "InsertMemberships", "SaveGroup", "NextNumber"} {
		t.Run(q, func(t *testing.T) {
			h := newHarness(t)
			a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
			h.changes(t, ingest.ChangeFired, a)
			g := h.groupOf(t, a)
			h.personResolve(g, 0)
			h.db.fail[q] = errBoom
			if _, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID); !errors.Is(err, errBoom) {
				t.Errorf("%s failing = %v", q, err)
			}
		})
	}
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	h.personResolve(g, 0)
	h.db.alerts[a].Labels = []byte("{")
	if _, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID); err == nil {
		t.Error("bad labels")
	}
	// A target that keeps changing under the lock gives up.
	h = newHarness(t)
	a = h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g = h.groupOf(t, a)
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, b)
	h.personResolve(g, 0)
	c := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "3"})
	h.changes(t, ingest.ChangeFired, c)
	open := h.groupOf(t, c)
	// The open target is moved whenever it is locked and back whenever it is looked up.
	var move, back func()
	move = func() {
		open.MovedFromRouteID = i8(9)
		h.db.before["LockGroups"] = move
	}
	back = func() {
		open.MovedFromRouteID = pgtype.Int8{}
		h.db.before["FindOpenGroup"] = back
	}
	h.db.before["LockGroups"], h.db.before["FindOpenGroup"] = move, back
	if _, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID); err == nil {
		t.Error("unstable targets")
	}
}

// metricsText is the scrape of the Leader's series and every replica's.
func metricsText(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(func() bool { return true }).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// TestRiseAfterAnEdit is C-09.FR-9 and C-08.FR-8: once the Route is marked urgent, a rise that leaves urgency
// unchanged under the current settings is a Quiet severity_raised that keeps the acknowledgement and the Snooze.
func TestRiseAfterAnEdit(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "info", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	h.acknowledge(g)
	h.db.routes[2].Urgent = true
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, b)
	if g.Status != "acknowledged" || !g.Urgent || g.SeverityLevel != "warning" ||
		h.last(t, g).Event.String != "severity_raised" {
		t.Errorf("after the edit: %+v %v", g, h.events(g))
	}
}

// TestLevelFollowsFiringAlerts is C-08.FR-6: the Severity level is the highest of the Alerts still firing, lowered with
// the alert_resolved that changes it, and a Reopen takes the level of its new Alerts; a later rise to critical makes
// the Alert Group Urgent again.
func TestLevelFollowsFiringAlerts(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
	b := h.alert(2, "critical", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, a, b)
	g := h.groupOf(t, a)
	if g.SeverityLevel != "critical" || !g.Urgent {
		t.Fatalf("created %+v", g)
	}
	h.resolve(t, b)
	if g.SeverityLevel != "warning" || g.Urgent || h.last(t, g).Event.String != "alert_resolved" {
		t.Errorf("after the critical alert resolved: %+v", g)
	}
	h.refire(t, b)
	if g.SeverityLevel != "critical" || !g.Urgent || h.last(t, g).Event.String != "urgency_raised" {
		t.Errorf("critical again: %+v %v", g, h.events(g))
	}
	h.resolve(t, a, b)
	h.clock.Advance(time.Minute)
	c := h.alert(2, "info", map[string]string{"alertname": "A", "cluster": "x", "n": "3"})
	h.changes(t, ingest.ChangeFired, c)
	if g.Status != "firing" || g.SeverityLevel != "info" || g.Urgent || h.last(t, g).Event.String != "reopened" {
		t.Errorf("reopened %+v %v", g, h.events(g))
	}
	d := h.alert(2, "info", map[string]string{"alertname": "A", "cluster": "x", "n": "4"})
	h.changes(t, ingest.ChangeFired, d)
	h.db.fail["ListGroupFiringAlerts"] = errBoom
	h.db.alerts[c].Status = "resolved"
	if _, err := h.apply(ingest.ChangeResolved, c); !errors.Is(err, errBoom) {
		t.Errorf("the level without the firing alerts = %v", err)
	}
}

// TestGracePeriodRace: a tail that resolved after the Grace period end looked but before it locked stays in the
// resolved Alert Group.
func TestGracePeriodRace(t *testing.T) {
	h := newHarness(t)
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, a, b)
	g := h.groupOf(t, a)
	h.personResolve(g, 0)
	h.db.before["LockGroups"] = func() {
		for _, m := range h.db.members {
			if m.alert == b {
				m.state, m.reason, m.text = "resolved", "resolved", "resolved"
			}
		}
	}
	if _, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID); err != nil {
		t.Fatal(err)
	}
	if n := h.groupOf(t, a); n == g || h.groupOf(t, b) != g || n.FiringAlertCount != 1 || g.GraceDeadline.Valid {
		t.Errorf("moved %+v, source %+v", n, g)
	}
	// Ended meanwhile by another replica: nothing changes.
	c := h.alert(2, "warning", map[string]string{"alertname": "C", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, c)
	gc := h.groupOf(t, c)
	h.personResolve(gc, 0)
	h.db.before["LockGroups"] = func() { gc.GraceDeadline = pgtype.Timestamptz{} }
	if _, err := h.svc.EndGracePeriod(t.Context(), nil, gc.ID); err != nil || h.groupOf(t, c) != gc {
		t.Errorf("ended twice: %v", err)
	}
}
