// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
)

// owner is the User who acknowledges and snoozes in the tests.
const owner = 9

// acknowledge sets an Alert Group acknowledged by the active User owner directly, as Acknowledge does from S-032.
func (h *harness) acknowledge(g *dbgen.LockGroupsRow) {
	g.Status, g.OwnerUserID, g.AcknowledgedAt = "acknowledged", i8(owner), ts(t0)
	h.db.users[owner] = dbgen.ListUserRefsRow{ID: owner, PublicID: "SRAAAAAAAAAAA9", Name: "Alice", Login: "alice",
		Status: "active"}
}

// snooze sets an Alert Group snoozed by the User owner until a time directly, as Snooze does from S-032.
func (h *harness) snooze(g *dbgen.LockGroupsRow, until time.Time, whileUrgent bool) {
	g.Status, g.SnoozeUntil, g.SnoozedByUserID, g.SnoozedWhileUrgent = "snoozed", ts(until), i8(owner), whileUrgent
}

// TestLifecycleEventTable is C-09.AC-21 and C-09.FR-22: every row of the lifecycle event table that this capability
// produces records exactly one Timeline entry with the row's event, kind, loudness and Mentions; the rows reached
// through Acknowledge and Snooze are set up directly. The expected values are the PRD table's, written out here, so
// that the closed list in events.go is checked against it.
func TestLifecycleEventTable(t *testing.T) {
	type want struct {
		event    Event
		kind     Kind
		loudness Loudness
		mentions []string
	}
	cases := []struct {
		name string
		want want
		// run makes the change and returns its Alert Group.
		run func(t *testing.T, h *harness) *dbgen.LockGroupsRow
	}{
		{"created", want{EventCreated, KindStatus, Loud, []string{"new_alert_group"}},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
				h.changes(t, ingest.ChangeFired, a)
				return h.groupOf(t, a)
			}},
		{"alerts added to a firing alert group", want{EventAlertsAdded, KindAlerts, Loud, []string{"new_alerts"}},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.join(t, nil)
			}},
		{"alerts added to an acknowledged alert group", want{EventAlertsAdded, KindAlerts, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.join(t, func(g *dbgen.LockGroupsRow) { h.acknowledge(g) })
			}},
		{"alerts added to a snoozed alert group", want{EventAlertsAdded, KindAlerts, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.join(t, func(g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), false) })
			}},
		{"alert replaced", want{EventAlertReplaced, KindAlerts, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "p1"})
				h.changes(t, ingest.ChangeFired, a)
				b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "pod": "p2"})
				h.changes(t, ingest.ChangeFired, b)
				return h.groupOf(t, a)
			}},
		{"alert resolved", want{EventAlertResolved, KindAlerts, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := h.join(t, nil)
				h.resolve(t, 1)
				return g
			}},
		{"alert continued", want{EventAlertContinued, KindAlerts, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := h.join(t, nil)
				h.db.alerts[1].StartsAt = t0.Add(time.Hour)
				h.changes(t, ingest.ChangeContinued, 1)
				return g
			}},
		{"annotations changed", want{EventAnnotationsChanged, KindAlerts, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := h.join(t, nil)
				h.db.alerts[1].Annotations = []byte(`{"summary":"new"}`)
				h.changes(t, ingest.ChangeAnnotations, 1)
				return g
			}},
		{"severity raised", want{EventSeverityRaised, KindAlerts, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				a := h.alert(2, "info", map[string]string{"alertname": "A", "cluster": "x"})
				h.changes(t, ingest.ChangeFired, a)
				b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
				h.changes(t, ingest.ChangeFired, b)
				return h.groupOf(t, a)
			}},
		{"urgency raised, removing the acknowledgement",
			want{EventUrgencyRaised, KindStatus, Loud, []string{"owner", "rise_to_urgent"}},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.rise(t, func(g *dbgen.LockGroupsRow) { h.acknowledge(g) })
			}},
		{"urgency raised, ending the snooze",
			want{EventUrgencyRaised, KindStatus, Loud, []string{"owner", "rise_to_urgent"}},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.rise(t, func(g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), false) })
			}},
		{"urgency raised, removing nothing", want{EventUrgencyRaised, KindStatus, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.rise(t, nil)
			}},
		{"reopened into firing", want{EventReopened, KindStatus, Loud, []string{"reopen"}},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.reopen(t, nil)
			}},
		{"reopened into acknowledged", want{EventReopened, KindStatus, Loud, []string{"owner"}},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.reopen(t, func(g *dbgen.LockGroupsRow) { h.acknowledge(g) })
			}},
		{"reopened into snoozed", want{EventReopened, KindStatus, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				return h.reopen(t, func(g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), false) })
			}},
		{"resolved by the system", want{EventResolved, KindStatus, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
				h.changes(t, ingest.ChangeFired, a)
				h.resolve(t, a)
				return h.groupOf(t, a)
			}},
		{"moved to the default route", want{EventMovedToDefaultRoute, KindSystem, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
				h.changes(t, ingest.ChangeFired, a)
				if _, err := h.svc.MoveOpenAlertGroups(t.Context(), Requester{Actor: audit.System,
					Transport: audit.TransportSystem}, "RTDBAAAAAAAAAA"); err != nil {
					t.Fatal(err)
				}
				return h.groupOf(t, a)
			}},
	}
	produced := map[Event]bool{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			g := c.run(t, h)
			var matching []*fakeEntry
			for _, e := range h.entriesOf(g) {
				if e.Event.String == string(c.want.event) && e.At.Equal(h.clock.Now()) {
					matching = append(matching, e)
				}
			}
			if len(matching) != 1 {
				t.Fatalf("%d %s entries in %v", len(matching), c.want.event, h.events(g))
			}
			e := matching[0]
			mentions := c.want.mentions
			if mentions == nil {
				mentions = []string{}
			}
			if e.Kind != string(c.want.kind) || e.Loudness.String != string(c.want.loudness) ||
				!slices.Equal(e.Mentions, mentions) || !e.EventSeq.Valid || e.ActorKind != "system" ||
				e.Transport != "system" {
				t.Errorf("entry %+v, want %+v", e.InsertTimelineEntryParams, c.want)
			}
			if e.EventSeq.Int64 < 1 || e.EventSeq.Int64 > g.EventSeq {
				t.Errorf("event_seq %d, alert group at %d", e.EventSeq.Int64, g.EventSeq)
			}
			produced[c.want.event] = true
		})
	}
	// The rows of other stories are in the closed list with the PRD's values: Snooze ends (S-063) and a released
	// Owner (S-063).
	for _, r := range []struct {
		row  Row
		want want
	}{
		{rowOf(EventSnoozeEnded, VariantAny), want{EventSnoozeEnded, KindStatus, Loud, []string{"snooze_ended"}}},
		{rowOf(EventUnacknowledged, VariantOwnerReleased), want{EventUnacknowledged, KindStatus, Loud, nil}},
	} {
		var mentions []string
		for _, m := range r.row.Mentions {
			mentions = append(mentions, string(m))
		}
		if r.row.Kind != r.want.kind || r.row.Loudness != r.want.loudness || !slices.Equal(mentions, r.want.mentions) {
			t.Errorf("row %+v, want %+v", r.row, r.want)
		}
		produced[r.row.Event] = true
	}
	// TestCommandEventTable covers the rows of the Commands (C-10.FR-15).
	for _, e := range []Event{EventAcknowledged, EventTakeover, EventUnresolved, EventSnoozed, EventUnsnoozed} {
		produced[e] = true
	}
	for _, r := range Table {
		if !produced[r.Event] {
			t.Errorf("no case covers %s", r.Event)
		}
	}
}

// TestCommandEventTable is C-10.AC-16, C-10.FR-15 and C-09.FR-11: every row of the lifecycle event table of the
// Commands but note_added (S-063) records exactly one Timeline entry with the row's event, kind, loudness and Mentions
// and the actor and Transport of the Command, and an Acknowledge by the current Owner records none. The expected
// values are the PRD table's, written out here.
func TestCommandEventTable(t *testing.T) {
	type want struct {
		event    Event
		loudness Loudness
		mentions []string
	}
	cases := []struct {
		name string
		want *want
		// set prepares the Alert Group, run is the Command.
		set func(h *harness, g *dbgen.LockGroupsRow)
		run func(h *harness, g *dbgen.LockGroupsRow) (Result, error)
	}{
		{"acknowledged", &want{EventAcknowledged, Quiet, nil}, nil,
			func(h *harness, g *dbgen.LockGroupsRow) (Result, error) { return h.do(bob, CommandAcknowledge, g) }},
		{"takeover", &want{EventTakeover, Loud, []string{"previous_owner"}},
			func(h *harness, g *dbgen.LockGroupsRow) { h.acknowledge(g) },
			func(h *harness, g *dbgen.LockGroupsRow) (Result, error) { return h.do(bob, CommandAcknowledge, g) }},
		{"acknowledge by the current owner", nil,
			func(h *harness, g *dbgen.LockGroupsRow) { h.acknowledge(g) },
			func(h *harness, g *dbgen.LockGroupsRow) (Result, error) { return h.do(alice, CommandAcknowledge, g) }},
		{"unacknowledged", &want{EventUnacknowledged, Quiet, nil},
			func(h *harness, g *dbgen.LockGroupsRow) { h.acknowledge(g) },
			func(h *harness, g *dbgen.LockGroupsRow) (Result, error) { return h.do(bob, CommandUnacknowledge, g) }},
		{"resolved by a person", &want{EventResolved, Quiet, nil}, nil,
			func(h *harness, g *dbgen.LockGroupsRow) (Result, error) { return h.do(bob, CommandResolve, g) }},
		{"unresolved", &want{EventUnresolved, Quiet, nil},
			func(h *harness, g *dbgen.LockGroupsRow) { h.personResolve(g, time.Minute) },
			func(h *harness, g *dbgen.LockGroupsRow) (Result, error) { return h.do(bob, CommandUnresolve, g) }},
		{"snoozed", &want{EventSnoozed, Quiet, nil}, nil,
			func(h *harness, g *dbgen.LockGroupsRow) (Result, error) { return h.do(bob, CommandSnooze, g) }},
		{"snoozed, only the end changes", &want{EventSnoozed, Quiet, nil},
			func(h *harness, g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), false) },
			func(h *harness, g *dbgen.LockGroupsRow) (Result, error) { return h.do(bob, CommandSnooze, g) }},
		{"unsnoozed", &want{EventUnsnoozed, Quiet, nil},
			func(h *harness, g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), false) },
			func(h *harness, g *dbgen.LockGroupsRow) (Result, error) { return h.do(bob, CommandUnsnooze, g) }},
	}
	produced := map[Event]bool{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.people()
			g := h.firing(t, "x")
			if c.set != nil {
				c.set(h, g)
			}
			h.clock.Advance(time.Minute)
			before := len(h.entriesOf(g))
			if _, err := c.run(h, g); err != nil {
				t.Fatal(err)
			}
			entries := h.entriesOf(g)[before:]
			if c.want == nil {
				if len(entries) != 0 {
					t.Errorf("recorded %v", h.events(g))
				}
				return
			}
			if len(entries) != 1 {
				t.Fatalf("%d entries: %v", len(entries), h.events(g))
			}
			e := entries[0]
			mentions := c.want.mentions
			if mentions == nil {
				mentions = []string{}
			}
			if e.Event.String != string(c.want.event) || e.Kind != string(KindStatus) ||
				e.Loudness.String != string(c.want.loudness) || !slices.Equal(e.Mentions, mentions) ||
				e.ActorKind != "user" || e.ActorUserID != i8(bobID) || e.Transport != "ui" ||
				e.EventSeq.Int64 != g.EventSeq {
				t.Errorf("entry %+v, want %+v", e.InsertTimelineEntryParams, *c.want)
			}
			produced[c.want.event] = true
		})
	}
	for _, r := range Table {
		if r.Kind == KindStatus && (r.Variant == VariantCommand || slices.Contains([]Event{EventAcknowledged,
			EventTakeover, EventUnresolved, EventSnoozed, EventUnsnoozed}, r.Event)) && !produced[r.Event] {
			t.Errorf("no case covers %s", r.Event)
		}
	}
}

// TestCommandPaths is C-10.AC-6, AC-7, AC-9, AC-12, AC-13 and AC-8 through the Commands: the acknowledged and
// snoozed paths of the lifecycle that Acknowledge, Snooze and Resolve now reach.
func TestCommandPaths(t *testing.T) {
	t.Run("reopen into acknowledged", func(t *testing.T) {
		h := newHarness(t)
		h.people()
		a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
		h.changes(t, ingest.ChangeFired, a)
		g := h.groupOf(t, a)
		if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
			t.Fatal(err)
		}
		b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
		h.changes(t, ingest.ChangeFired, b)
		if e := h.last(t, g); e.Event.String != "alerts_added" || e.Loudness.String != "quiet" || g.Status != "acknowledged" {
			t.Errorf("alerts added = %+v", e)
		}
		h.db.alerts[a].StartsAt = t0.Add(time.Hour)
		h.changes(t, ingest.ChangeContinued, a)
		if e := h.last(t, g); e.Event.String != "alert_continued" || e.Loudness.String != "quiet" ||
			g.Status != "acknowledged" {
			t.Errorf("continued = %+v", e)
		}
		h.resolve(t, a, b)
		h.clock.Advance(5 * time.Minute)
		c := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "3"})
		h.changes(t, ingest.ChangeFired, c)
		e := h.last(t, g)
		if g.Status != "acknowledged" || g.OwnerUserID != i8(bobID) || g.ReopenCount != 1 ||
			e.Event.String != "reopened" || e.Loudness.String != "loud" || !slices.Equal(e.Mentions, []string{"owner"}) {
			t.Errorf("reopen = %+v, %+v", g, e)
		}
	})
	t.Run("rise to urgent", func(t *testing.T) {
		for _, removes := range []bool{true, false} {
			h := newHarness(t)
			h.people()
			h.db.routes[2].UrgentRiseRemovesAck = removes
			g := h.rise(t, func(g *dbgen.LockGroupsRow) {
				if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
					t.Fatal(err)
				}
			})
			e := h.last(t, g)
			if removes && (g.Status != "firing" || g.OwnerUserID.Valid || e.Loudness.String != "loud" ||
				!slices.Equal(e.Mentions, []string{"owner", "rise_to_urgent"}) || e.PreviousOwnerUserID != i8(bobID)) {
				t.Errorf("removes: %+v %+v", g, e)
			}
			if !removes && (g.Status != "acknowledged" || e.Loudness.String != "quiet") {
				t.Errorf("keeps: %+v %+v", g, e)
			}
		}
	})
	t.Run("snoozed while not urgent and while urgent", func(t *testing.T) {
		h := newHarness(t)
		h.people()
		g := h.rise(t, func(g *dbgen.LockGroupsRow) {
			if _, err := h.svc.Snooze(t.Context(), bob, g.PublicID, SnoozeEnd{NoEnd: true}); err != nil {
				t.Fatal(err)
			}
		})
		if e := h.last(t, g); g.Status != "firing" || e.Event.String != "urgency_raised" || e.Loudness.String != "loud" {
			t.Errorf("the rise kept the snooze: %+v", e)
		}
		if _, err := h.svc.Snooze(t.Context(), bob, g.PublicID, SnoozeEnd{NoEnd: true}); err != nil ||
			!g.SnoozedWhileUrgent {
			t.Fatalf("snooze while urgent = %v", err)
		}
		critical := h.db.members[len(h.db.members)-1].alert
		h.resolve(t, critical)
		h.refire(t, critical)
		if g.Status != "snoozed" {
			t.Errorf("status %s", g.Status)
		}
	})
	t.Run("grace period after a person's resolve", func(t *testing.T) {
		h := newHarness(t)
		h.people()
		g := h.firing(t, "x")
		if _, err := h.svc.Resolve(t.Context(), bob, g.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(14 * time.Minute)
		if out, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID); err != nil || len(out.AlertGroups) != 0 {
			t.Fatalf("early %v %+v", err, out)
		}
		h.clock.Advance(2 * time.Minute)
		out, err := h.svc.EndGracePeriod(t.Context(), nil, g.ID)
		if err != nil || len(out.AlertGroups) != 1 {
			t.Fatalf("end %v %+v", err, out)
		}
		n := h.groupOf(t, h.db.members[0].alert)
		v, err := h.svc.Get(t.Context(), n.PublicID)
		if err != nil || n == g || !slices.ContainsFunc(v.Notices, func(x Notice) bool {
			return x.Kind == NoticeFiringAgainAfterManualResolve && *x.ResolvedNumber == g.Number
		}) {
			t.Errorf("new alert group %v %+v", err, v.Notices)
		}
	})
}

// join starts an Alert Group of two Alerts (1 and 2), sets it up with set, and has a third Alert join it.
func (h *harness) join(t *testing.T, set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow {
	t.Helper()
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
	b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, a, b)
	g := h.groupOf(t, a)
	if set != nil {
		set(g)
	}
	h.clock.Advance(time.Minute)
	c := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "3"})
	h.changes(t, ingest.ChangeFired, c)
	return g
}

// rise starts a warning Alert Group, sets it up with set, and has a critical Alert join it.
func (h *harness) rise(t *testing.T, set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow {
	t.Helper()
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	if set != nil {
		set(g)
	}
	h.clock.Advance(time.Minute)
	b := h.alert(2, "critical", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
	h.changes(t, ingest.ChangeFired, b)
	return g
}

// reopen starts an Alert Group, sets it up with set, resolves its Alert and fires it again within the window.
func (h *harness) reopen(t *testing.T, set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow {
	t.Helper()
	a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
	h.changes(t, ingest.ChangeFired, a)
	g := h.groupOf(t, a)
	if set != nil {
		set(g)
	}
	h.clock.Advance(time.Minute)
	h.resolve(t, a)
	h.clock.Advance(8 * time.Minute)
	h.refire(t, a)
	return g
}

// TestRowOf: an event outside the closed list is a programming error.
func TestRowOf(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "no lifecycle event") {
			t.Errorf("recovered %v", r)
		}
	}()
	rowOf("note_added", VariantAny)
}

// TestStatusVariant: alerts_added and reopened take the variant of the status.
func TestStatusVariant(t *testing.T) {
	for s, v := range map[Status]Variant{StatusFiring: VariantFiring, StatusAcknowledged: VariantAcknowledged,
		StatusSnoozed: VariantSnoozed, StatusResolved: VariantFiring} {
		if got := statusVariant(s); got != v {
			t.Errorf("statusVariant(%s) = %s", s, got)
		}
	}
}
