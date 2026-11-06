// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/routing"
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
		{"snooze ended", want{EventSnoozeEnded, KindStatus, Loud, []string{"snooze_ended"}},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				g := h.join(t, func(g *dbgen.LockGroupsRow) { h.snooze(g, t0.Add(time.Hour), false) })
				h.clock.Advance(time.Hour)
				if _, err := h.svc.EndSnooze(t.Context(), nil, g.ID); err != nil {
					t.Fatal(err)
				}
				return g
			}},
		{"unacknowledged, its owner released", want{EventUnacknowledged, KindStatus, Loud, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
				h.changes(t, ingest.ChangeFired, a)
				g := h.groupOf(t, a)
				h.acknowledge(g)
				h.clock.Advance(time.Minute)
				if _, err := h.svc.ReleaseOwner(t.Context(), nil, owner, ReasonOwnerDisabled); err != nil {
					t.Fatal(err)
				}
				return g
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
		{"team joined", want{EventTeamJoined, KindAlerts, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				h.withTeams()
				a := h.teamAlert("x", "", byLabel(payments))
				h.changes(t, ingest.ChangeFired, a)
				return h.groupOf(t, a)
			}},
		{"unknown team value", want{EventUnknownTeamValue, KindSystem, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				a := h.teamAlert("x", "Payments")
				h.changes(t, ingest.ChangeFired, a)
				return h.groupOf(t, a)
			}},
		{"destination joined by a firing alert group", want{EventDestinationJoined, KindSystem, Loud,
			[]string{"new_alert_group"}},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				h.withReach()
				a := h.teamAlert("x", "", byLabel(payments))
				h.changes(t, ingest.ChangeFired, a)
				g := h.groupOf(t, a)
				h.published(g, destNotice)
				h.clock.Advance(time.Minute)
				h.changes(t, ingest.ChangeFired, h.teamAlert("x", "", byLabel(search)))
				return g
			}},
		{"destination joined by an acknowledged alert group", want{EventDestinationJoined, KindSystem, Quiet, nil},
			func(t *testing.T, h *harness) *dbgen.LockGroupsRow {
				h.withReach()
				a := h.teamAlert("x", "", byLabel(payments))
				h.changes(t, ingest.ChangeFired, a)
				g := h.groupOf(t, a)
				h.published(g, destNotice)
				h.acknowledge(g)
				h.clock.Advance(time.Minute)
				h.changes(t, ingest.ChangeFired, h.teamAlert("x", "", byLabel(search)))
				return g
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
	// TestCommandEventTable and TestNoteAddedEvent cover the rows of the Commands (C-10.FR-15), TestTimerEventTable
	// those of the timers (C-17.FR-11).
	for _, e := range []Event{EventAcknowledged, EventTakeover, EventUnresolved, EventSnoozed, EventUnsnoozed,
		EventNoteAdded, EventAckTimeout, EventUnclaimed, EventReminder, EventReminderAnswered, EventAutoUnacknowledged,
		EventNoticesMissed} {
		produced[e] = true
	}
	for _, r := range Table {
		if !produced[r.Event] {
			t.Errorf("no case covers %s", r.Event)
		}
	}
}

// TestCommandEventTable is C-10.AC-16, C-10.FR-15 and C-09.FR-11: every row of the lifecycle event table of the
// Commands but note_added (TestNoteAddedEvent) records exactly one Timeline entry with the row's event, kind, loudness and Mentions
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

// TestNoteAddedEvent is C-10.AC-16, C-10.FR-15 and C-09.FR-11: Add Note records exactly one note_added — kind notes,
// Quiet, without Mentions — with the actor and Transport of the Command and the next number among the lifecycle
// events, which the Timeline shows; a Note alone records nothing else.
func TestNoteAddedEvent(t *testing.T) {
	h := newHarness(t)
	h.people()
	g := h.firing(t, "x")
	h.clock.Advance(time.Minute)
	entries, notes := len(h.entriesOf(g)), len(h.db.noteRows)
	n, err := h.svc.AddNote(t.Context(), bob, g.PublicID, "Looking.")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.entriesOf(g)) != entries || len(h.db.noteRows) != notes+1 || h.noteRow(t, n.PublicID).EventSeq != g.EventSeq {
		t.Errorf("%d entries, %d notes", len(h.entriesOf(g))-entries, len(h.db.noteRows)-notes)
	}
	row := rowOf(EventNoteAdded, VariantAny)
	if row.Kind != KindNotes || row.Loudness != Quiet || len(row.Mentions) != 0 {
		t.Errorf("row %+v", row)
	}
	tl, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 1})
	if err != nil || len(tl.Entries) != 1 {
		t.Fatalf("timeline = %v %+v", err, tl)
	}
	e := tl.Entries[0]
	if e.Kind != KindNotes || e.Event != string(EventNoteAdded) || e.Loudness != Quiet || len(e.Mentions) != 0 ||
		e.Actor.Kind != "user" || e.Actor.Ref == nil || e.Actor.Ref.Name != "Bob" || e.Actor.Transport != "ui" ||
		e.Note == nil || e.Note.Body != "Looking." {
		t.Errorf("entry %+v", e)
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
	rowOf("still_on_it", VariantAny)
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

// TestTimerEventTable is C-17.AC-9, C-17.FR-11 and C-09.FR-11: every row of the lifecycle event table of the timers
// records exactly one Timeline entry with the row's event, kind, loudness and Mentions — notices_missed with those of
// the notices it stands for — made by the system, reminder_answered by the Owner who answered. The expected values are
// the PRD table's, written out here.
func TestTimerEventTable(t *testing.T) {
	type want struct {
		event    Event
		kind     Kind
		loudness Loudness
		mentions []string
		// actor is the actor_kind of the entry: the system, or the Owner who answered.
		actor string
	}
	// ack fires the ack_timeout timer of an Alert Group published at t0 at the offset at; remind acknowledges it as
	// Bob at t0 and fires its reminder timer at at, Reminders reaching their Destinations.
	ack := func(at time.Duration) func(t *testing.T, h *harness, g *dbgen.LockGroupsRow) {
		return func(t *testing.T, h *harness, g *dbgen.LockGroupsRow) {
			h.clock.Set(t0.Add(at))
			h.fire(t, g, TimerAckTimeout)
		}
	}
	remind := func(until time.Duration, answer bool) func(t *testing.T, h *harness, g *dbgen.LockGroupsRow) {
		return func(t *testing.T, h *harness, g *dbgen.LockGroupsRow) {
			var asked []int64
			h.svc.SetReminderReached(reachedAll(&asked))
			if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
				t.Fatal(err)
			}
			h.runUntil(t, g, t0.Add(until))
			if answer {
				h.clock.Advance(time.Minute)
				if _, err := h.svc.AnswerReminder(t.Context(), bob, g.PublicID); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	collapse := func(t *testing.T, h *harness, g *dbgen.LockGroupsRow) {
		if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
			t.Fatal(err)
		}
		h.clock.Set(t0.Add(13 * time.Hour))
		h.fire(t, g, TimerReminder)
	}
	cases := []struct {
		name    string
		want    want
		produce func(t *testing.T, h *harness, g *dbgen.LockGroupsRow)
	}{
		{"ack timeout notice", want{EventAckTimeout, KindTimers, Loud, []string{"ack_timeout"}, "system"},
			ack(15 * time.Minute)},
		{"unclaimed", want{EventUnclaimed, KindTimers, Quiet, nil, "system"}, ack(2 * time.Hour)},
		{"overdue ack timeout notices", want{EventNoticesMissed, KindTimers, Loud, []string{"ack_timeout"}, "system"},
			ack(time.Hour)},
		{"reminder", want{EventReminder, KindTimers, Loud, []string{"owner"}, "system"}, remind(4*time.Hour, false)},
		{"still on it", want{EventReminderAnswered, KindTimers, Quiet, nil, "user"}, remind(4*time.Hour, true)},
		{"auto-unacknowledge", want{EventAutoUnacknowledged, KindStatus, Loud, []string{"owner"}, "system"},
			remind(28*time.Hour, false)},
		{"overdue reminders", want{EventNoticesMissed, KindTimers, Loud, []string{"owner"}, "system"}, collapse},
	}
	produced := map[Event]bool{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.people()
			h.paging()
			h.db.routes[2].timers.AutoUnacknowledge = true
			g := h.firing(t, "x")
			h.publish(t, g, t0)
			c.produce(t, h, g)
			var matching []*fakeEntry
			for _, e := range h.entriesOf(g) {
				if e.Event.String == string(c.want.event) {
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
				!slices.Equal(e.Mentions, mentions) || e.EventSeq.Int64 < 1 || e.EventSeq.Int64 > g.EventSeq ||
				e.ActorKind != c.want.actor {
				t.Errorf("entry %+v, want %+v", e.InsertTimelineEntryParams, c.want)
			}
			produced[c.want.event] = true
		})
	}
	for _, e := range []Event{EventAckTimeout, EventUnclaimed, EventReminder, EventReminderAnswered,
		EventAutoUnacknowledged, EventNoticesMissed} {
		if !produced[e] {
			t.Errorf("no case covers %s", e)
		}
	}
}

// fakeReach is what the reach of an Alert Group is read from (C-26.FR-2): the Destination entries of each Route and
// each Team, "Also to the Teams from the label" of each Route, the Destinations that have a Root message by id, and the
// Route membership locks taken, in order, and the Team membership locks, in order. The deliveries are
// fakeDB.delivered.
type fakeReach struct {
	routes    map[int64][]routing.Entry
	teams     map[int64][]routing.Entry
	toTeams   map[int64]bool
	dests     map[int64]JoinedDestination
	locks     []int64
	teamLocks []int64
}

func (f *fakeDB) ShareTeamMembership(_ context.Context, arg dbgen.ShareTeamMembershipParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if arg.LockClass != db.TeamMembershipLockClass {
		return errors.New("not the team membership lock")
	}
	f.reach.teamLocks = append(f.reach.teamLocks, arg.TeamID)
	return f.call("ShareTeamMembership")
}

func (f *fakeDB) ListGroupsTeamIDs(_ context.Context, arg dbgen.ListGroupsTeamIDsParams) (
	[]dbgen.ListGroupsTeamIDsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupsTeamIDs"); err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []dbgen.ListGroupsTeamIDsRow
	for _, id := range slices.Sorted(slices.Values(arg.AlertGroupIds)) {
		var teams []int64
		for _, gt := range f.teams.groups[id] {
			teams = append(teams, gt.team)
		}
		slices.Sort(teams)
		for _, team := range teams {
			out = append(out, dbgen.ListGroupsTeamIDsRow{AlertGroupID: id, TeamID: team})
		}
	}
	return out, nil
}

func (f *fakeDB) ShareRouteMembership(_ context.Context, arg dbgen.ShareRouteMembershipParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if arg.LockClass != db.RouteMembershipLockClass {
		return errors.New("not the membership lock")
	}
	f.reach.locks = append(f.reach.locks, arg.RouteID)
	return f.call("ShareRouteMembership")
}

func (f *fakeDB) GetRouteDeliverToTeams(_ context.Context, arg dbgen.GetRouteDeliverToTeamsParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetRouteDeliverToTeams"); err != nil || arg.OrgID != orgID {
		return false, err
	}
	return f.reach.toTeams[arg.ID], nil
}

func (f *fakeDB) ListGroupTeamIDs(_ context.Context, arg dbgen.ListGroupTeamIDsParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupTeamIDs"); err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []int64
	for _, gt := range f.teams.groups[arg.AlertGroupID] {
		out = append(out, gt.team)
	}
	slices.Sort(out)
	return out, nil
}

func (f *fakeDB) ListReachEntries(_ context.Context, arg dbgen.ListReachEntriesParams) ([]dbgen.ListReachEntriesRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListReachEntries"); err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []dbgen.ListReachEntriesRow
	row := func(kind string, team int64, e routing.Entry) {
		d, ok := f.reach.dests[e.DestinationID]
		if !ok {
			return
		}
		out = append(out, dbgen.ListReachEntriesRow{Kind: kind, TeamID: team, DestinationID: e.DestinationID,
			SeverityCondition: string(e.Condition), PublicID: d.PublicID, Name: d.Name, Type: d.Type,
			Delivered: slices.Contains(f.delivered[arg.AlertGroupID], e.DestinationID)})
	}
	for _, e := range f.reach.routes[arg.RouteID] {
		row(routing.EntryRoute, 0, e)
	}
	for _, gt := range f.teams.groups[arg.AlertGroupID] {
		for _, e := range f.reach.teams[gt.team] {
			row(routing.EntryTeam, gt.team, e)
		}
	}
	slices.SortFunc(out, func(a, b dbgen.ListReachEntriesRow) int {
		return cmp.Or(cmp.Compare(a.DestinationID, b.DestinationID), cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.TeamID, b.TeamID))
	})
	return out, nil
}

func (f *fakeDB) ListTimelineDestinations(_ context.Context, arg dbgen.ListTimelineDestinationsParams) (
	[]dbgen.ListTimelineDestinationsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListTimelineDestinations"); err != nil || arg.OrgID != orgID {
		return nil, err
	}
	var out []dbgen.ListTimelineDestinationsRow
	for _, id := range arg.Ids {
		if d, ok := f.reach.dests[id]; ok {
			out = append(out, dbgen.ListTimelineDestinationsRow{ID: id, PublicID: d.PublicID, Name: d.Name,
				Type: d.Type, Health: "healthy"})
		}
	}
	return out, nil
}

// The Destinations of the reach tests: notice of the Route db below critical, critical of the Route db for
// critical, and the search Team's notice below critical.
const (
	destNotice   = 61
	destCritical = 62
	destSearch   = 63
)

// withReach gives the Route db its Destinations notice (below critical) and critical (critical) with "Also to the
// Teams from the label" on, and the Team search the Destination search-notice below critical; publish marks the Alert
// Groups published once they exist.
func (h *harness) withReach() {
	h.withTeams()
	h.db.reach = fakeReach{
		routes: map[int64][]routing.Entry{2: {{DestinationID: destNotice, Condition: routing.ConditionBelowCritical},
			{DestinationID: destCritical, Condition: routing.ConditionCritical}}},
		teams:   map[int64][]routing.Entry{search: {{DestinationID: destSearch, Condition: routing.ConditionBelowCritical}}},
		toTeams: map[int64]bool{2: true},
		dests: map[int64]JoinedDestination{
			destNotice:   {ID: destNotice, PublicID: "DS00000000NOTC", Name: "infra-notice", Type: "mattermost"},
			destCritical: {ID: destCritical, PublicID: "DS00000000CRIT", Name: "infra-critical", Type: "mattermost"},
			destSearch:   {ID: destSearch, PublicID: "DS00000000SRCH", Name: "search-notice", Type: "mattermost"},
		},
	}
	if h.db.delivered == nil {
		h.db.delivered = map[int64][]int64{}
	}
}

// published marks the Alert Group published with a delivery in each Destination of dests, as delivery does.
func (h *harness) published(g *dbgen.LockGroupsRow, dests ...int64) {
	g.FirstPublishedAt = ts(t0)
	h.db.delivered[g.ID] = append(h.db.delivered[g.ID], dests...)
}

// joinedEntries are the destination_joined entries of an Alert Group with their Destinations.
func (h *harness) joinedEntries(g *dbgen.LockGroupsRow) []*fakeEntry {
	var out []*fakeEntry
	for _, e := range h.entriesOf(g) {
		if e.Event.String == string(EventDestinationJoined) {
			out = append(out, e)
		}
	}
	return out
}

// TestDestinationJoined is C-26.FR-4, FR-5, AC-4 and AC-5 in the dispatcher: an Alert of a Team joining a published,
// firing Alert Group records one Loud destination_joined with new_alert_group for the Team's Destination it reaches
// without a delivery, under the Route's membership lock and its Teams', and nothing for the Destinations it already
// has; a Route without the option takes no Team's lock; joining an
// acknowledged one records it Quiet; a rise to critical joins the critical Destination; an Alert Group not published
// yet, a resolved one or a change of configuration records none; the Rendering carries the Teams and the event's
// Destination.
func TestDestinationJoined(t *testing.T) {
	t.Run("a team joins a firing alert group", func(t *testing.T) {
		h := newHarness(t)
		h.withReach()
		var renderings []Rendering
		h.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
			renderings = append(renderings, r)
			return nil
		})
		a := h.teamAlert("x", "", byLabel(payments))
		h.changes(t, ingest.ChangeFired, a)
		g := h.groupOf(t, a)
		if len(h.joinedEntries(g)) != 0 || len(h.db.reach.locks) != 0 {
			t.Fatalf("the creation joined %v, locks %v", h.events(g), h.db.reach.locks)
		}
		h.published(g, destNotice)
		h.clock.Advance(time.Minute)
		b := h.teamAlert("x", "", byLabel(search))
		h.changes(t, ingest.ChangeFired, b)
		joined := h.joinedEntries(g)
		if len(joined) != 1 {
			t.Fatalf("joined %v", h.events(g))
		}
		e := joined[0]
		if e.Kind != string(KindSystem) || e.SystemEvent.String != string(SystemDestinationJoined) ||
			e.Loudness.String != string(Loud) || !slices.Equal(e.Mentions, []string{"new_alert_group"}) ||
			e.DestinationID.Int64 != destSearch || !e.EventSeq.Valid {
			t.Errorf("entry %+v", e.InsertTimelineEntryParams)
		}
		// After the Route's lock, those of its Teams by id, as the Route delivers to the Teams (C-26.FR-6).
		if !slices.Equal(h.db.reach.locks, []int64{2}) ||
			!slices.Equal(h.db.reach.teamLocks, []int64{payments, search}) {
			t.Errorf("membership locks %v, team locks %v", h.db.reach.locks, h.db.reach.teamLocks)
		}
		r := renderings[len(renderings)-1]
		if !slices.Equal(r.Teams, []int64{payments, search}) || len(r.Joined()) != 1 ||
			r.Joined()[destSearch].Destination.PublicID != "DS00000000SRCH" ||
			r.Joined()[destSearch].Loudness != Loud ||
			!slices.Equal(r.Joined()[destSearch].Destination.Entries, []routing.Reaching{{Kind: routing.EntryTeam,
				TeamID: search, Condition: routing.ConditionBelowCritical}}) {
			t.Errorf("rendering teams %v joined %+v", r.Teams, r.Joined())
		}
		page, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(page.Entries, func(e TimelineEntry) bool { return e.Event == string(EventDestinationJoined) })
		if i < 0 || page.Entries[i].Destination == nil || page.Entries[i].Destination.Name != "search-notice" ||
			page.Entries[i].Destination.Type != "mattermost" {
			t.Errorf("timeline %+v", page.Entries)
		}

		// The Destination has its delivery now: the next Alert joins nothing.
		h.published(g, destSearch)
		h.clock.Advance(time.Minute)
		c := h.teamAlert("x", "", byLabel(search))
		h.changes(t, ingest.ChangeFired, c)
		if len(h.joinedEntries(g)) != 1 {
			t.Errorf("joined again %v", h.events(g))
		}
	})
	t.Run("an acknowledged alert group joins quietly", func(t *testing.T) {
		h := newHarness(t)
		h.withReach()
		a := h.teamAlert("x", "", byLabel(payments))
		h.changes(t, ingest.ChangeFired, a)
		g := h.groupOf(t, a)
		h.published(g, destNotice)
		h.acknowledge(g)
		h.clock.Advance(time.Minute)
		h.changes(t, ingest.ChangeFired, h.teamAlert("x", "", byLabel(search)))
		joined := h.joinedEntries(g)
		if len(joined) != 1 || joined[0].Loudness.String != string(Quiet) || len(joined[0].Mentions) != 0 {
			t.Errorf("joined %v", h.events(g))
		}
	})
	t.Run("a rise to critical joins the critical destination", func(t *testing.T) {
		h := newHarness(t)
		h.withReach()
		g := h.rise(t, func(g *dbgen.LockGroupsRow) { h.published(g, destNotice) })
		joined := h.joinedEntries(g)
		if len(joined) != 1 || joined[0].DestinationID.Int64 != destCritical ||
			joined[0].Loudness.String != string(Loud) {
			t.Errorf("joined %v", h.events(g))
		}
	})
	t.Run("a joining team brings only its own destinations", func(t *testing.T) {
		// A warning Alert Group published in the critical Destination only, as one whose level fell from critical:
		// search joining it brings search-notice and never the Route's notice, which the fall did not bring.
		h := newHarness(t)
		h.withReach()
		a := h.teamAlert("x", "", byLabel(payments))
		h.changes(t, ingest.ChangeFired, a)
		g := h.groupOf(t, a)
		h.published(g, destCritical)
		h.changes(t, ingest.ChangeFired, h.teamAlert("x", "", byLabel(search)))
		joined := h.joinedEntries(g)
		if len(joined) != 1 || joined[0].DestinationID.Int64 != destSearch {
			t.Errorf("joined %v", h.events(g))
		}
	})
	t.Run("a route without the option takes no team's lock", func(t *testing.T) {
		h := newHarness(t)
		h.withReach()
		h.db.reach.toTeams[2] = false
		a := h.teamAlert("x", "", byLabel(payments))
		h.changes(t, ingest.ChangeFired, a)
		h.published(h.groupOf(t, a), destNotice)
		h.changes(t, ingest.ChangeFired, h.teamAlert("x", "", byLabel(search)))
		if !slices.Equal(h.db.reach.locks, []int64{2}) || len(h.db.reach.teamLocks) != 0 {
			t.Errorf("membership locks %v, team locks %v", h.db.reach.locks, h.db.reach.teamLocks)
		}
	})
	// A Reopen at a level above the one the Alert Group resolved at is a rise by Alerts: the Destinations of the
	// higher level join it, Loud while it fires; a Reopen at the same or a lower level joins nothing.
	reopenAt := func(t *testing.T, h *harness, from, to string, dests ...int64) *dbgen.LockGroupsRow {
		t.Helper()
		a := h.alert(2, from, map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
		h.changes(t, ingest.ChangeFired, a)
		g := h.groupOf(t, a)
		h.published(g, dests...)
		h.clock.Advance(time.Minute)
		h.resolve(t, a)
		h.clock.Advance(8 * time.Minute)
		h.changes(t, ingest.ChangeFired, h.alert(2, to, map[string]string{"alertname": "A", "cluster": "x", "n": "2"}))
		if g.Status != string(StatusFiring) || g.SeverityLevel != to {
			t.Fatalf("after the reopen %s %s: %v", g.Status, g.SeverityLevel, h.events(g))
		}
		return g
	}
	t.Run("a reopen at a higher level joins the critical destination", func(t *testing.T) {
		h := newHarness(t)
		h.withReach()
		g := reopenAt(t, h, "warning", "critical", destNotice)
		joined := h.joinedEntries(g)
		if len(joined) != 1 || joined[0].DestinationID.Int64 != destCritical ||
			joined[0].Loudness.String != string(Loud) || !g.Urgent {
			t.Errorf("joined %v", h.events(g))
		}
		var rise *fakeEntry
		for _, e := range h.entriesOf(g) {
			if e.Event.String == string(EventUrgencyRaised) {
				rise = e
			}
		}
		// The Quiet urgency_raised that keeps the status, by Alerts: the Reopen is what is announced.
		if rise == nil || rise.Loudness.String != string(Quiet) || rise.FromStatus != rise.ToStatus ||
			rise.Reason.String == ReasonMinimumSeverity {
			t.Errorf("the reopen recorded %v", h.events(g))
		}
	})
	t.Run("a reopen at a higher level that is not urgent records severity_raised", func(t *testing.T) {
		h := newHarness(t)
		h.withReach()
		g := reopenAt(t, h, "info", "warning", destNotice)
		if !slices.Contains(h.events(g), string(EventSeverityRaised)) || len(h.joinedEntries(g)) != 0 {
			t.Errorf("the reopen recorded %v", h.events(g))
		}
	})
	for _, c := range []struct{ name, from, to string }{{"the same", "warning", "warning"},
		{"a lower", "critical", "warning"}} {
		t.Run("a reopen at "+c.name+" level joins nothing", func(t *testing.T) {
			h := newHarness(t)
			h.withReach()
			g := reopenAt(t, h, c.from, c.to, destCritical)
			events := h.events(g)
			if len(h.joinedEntries(g)) != 0 || slices.Contains(events, string(EventSeverityRaised)) ||
				slices.Contains(events, string(EventUrgencyRaised)) {
				t.Errorf("the reopen recorded %v", events)
			}
		})
	}
	t.Run("a new alert after a fall joins nothing", func(t *testing.T) {
		h := newHarness(t)
		h.withReach()
		a := h.alert(2, "critical", map[string]string{"alertname": "A", "cluster": "x", "n": "1"})
		b := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x", "n": "2"})
		h.changes(t, ingest.ChangeFired, a, b)
		g := h.groupOf(t, a)
		h.published(g, destCritical)
		h.clock.Advance(time.Minute)
		h.resolve(t, a)
		if g.SeverityLevel != "warning" {
			t.Fatalf("level after the fall %s", g.SeverityLevel)
		}
		h.clock.Advance(time.Minute)
		h.changes(t, ingest.ChangeFired, h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x",
			"n": "3"}))
		if joined := h.joinedEntries(g); len(joined) != 0 {
			t.Errorf("a warning alert after the fall joined %v", h.events(g))
		}
	})
	t.Run("before the first publication nothing joins", func(t *testing.T) {
		h := newHarness(t)
		h.withReach()
		g := h.rise(t, nil)
		if len(h.joinedEntries(g)) != 0 || len(h.db.reach.locks) != 0 {
			t.Errorf("joined %v", h.events(g))
		}
	})
	t.Run("a change of configuration joins nothing", func(t *testing.T) {
		g := &Group{FirstPublishedAt: &t0, Status: StatusFiring}
		for name, c := range map[string]change{
			"teams evaluated": {configuration: true, entries: []entry{{Event: EventTeamJoined}}},
			"minimum severity": {entries: []entry{{Event: EventUrgencyRaised, Variant: VariantRemovesNone,
				Reason: ReasonMinimumSeverity}}},
			"a command": {entries: []entry{{Event: EventAcknowledged}}},
			// Alerts that bring neither a Team nor a rise join nothing (C-26.FR-5).
			"alerts added":   {entries: []entry{{Event: EventAlertsAdded}}},
			"alert replaced": {entries: []entry{{Event: EventAlertReplaced}}},
			// A Reopen joins only with the rise it records above the level it resolved at.
			"reopened":       {entries: []entry{{Event: EventReopened}}},
			"alert resolved": {entries: []entry{{Event: EventAlertResolved}}},
		} {
			if joinsByAlerts(&c, g) {
				t.Errorf("%s joins", name)
			}
		}
		resolved := &Group{FirstPublishedAt: &t0, Status: StatusResolved}
		if joinsByAlerts(&change{entries: []entry{{Event: EventTeamJoined}}}, resolved) {
			t.Error("a resolved alert group joins")
		}
		for _, e := range []entry{{Event: EventTeamJoined}, {Event: EventSeverityRaised},
			{Event: EventUrgencyRaised, Variant: VariantRemoves}} {
			if !joinsByAlerts(&change{entries: []entry{{Event: EventAlertsAdded}, e}}, g) ||
				!joinsByAlerts(&change{entries: []entry{{Event: EventReopened}, e}}, g) {
				t.Errorf("%s joins nothing", e.Event)
			}
		}
	})
	t.Run("failures", func(t *testing.T) {
		for _, q := range []string{"ShareRouteMembership", "GetRouteDeliverToTeams", "ListReachEntries",
			"ListGroupTeamIDs", "ShareTeamMembership"} {
			h := newHarness(t)
			h.withReach()
			h.svc.SetRerender(func(context.Context, DBTX, Rendering) error { return nil })
			a := h.teamAlert("x", "", byLabel(payments))
			h.changes(t, ingest.ChangeFired, a)
			h.published(h.groupOf(t, a), destNotice)
			h.db.fail[q] = errBoom
			if _, err := h.apply(ingest.ChangeFired, h.teamAlert("x", "", byLabel(search))); !errors.Is(err, errBoom) {
				t.Errorf("%s: %v", q, err)
			}
		}
		h := newHarness(t)
		h.withReach()
		a := h.teamAlert("x", "", byLabel(payments))
		h.changes(t, ingest.ChangeFired, a)
		g := h.groupOf(t, a)
		h.published(g, destNotice)
		h.changes(t, ingest.ChangeFired, h.teamAlert("x", "", byLabel(search)))
		h.db.fail["ListTimelineDestinations"] = errBoom
		if _, err := h.svc.Timeline(t.Context(), g.PublicID, TimelineFilter{Limit: 50}); !errors.Is(err, errBoom) {
			t.Errorf("timeline: %v", err)
		}
	})
}
