// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/metrics"
)

// onCall gives the Route db the timer policy of the On-call profile (defaults.md): an ack timeout of 15 minutes,
// and Reminders from 4 hours doubling up to 24 hours.
func (h *harness) onCall() {
	h.db.routes[2].timers = dbgen.GetTimerPolicyRow{AckTimeoutEnabled: true, AckTimeoutFirstIntervalSeconds: 900,
		RemindersEnabled: true, RemindersFirstIntervalSeconds: 4 * 3600, RemindersCapSeconds: 24 * 3600}
}

// publish marks the first Publication of g at at, as delivery does once its outcome committed.
func (h *harness) publish(t *testing.T, g *dbgen.LockGroupsRow, at time.Time) {
	t.Helper()
	if err := h.svc.MarkPublished(t.Context(), g.ID, at); err != nil {
		t.Fatal(err)
	}
}

// ackTimer is the deadline and notice number of the ack_timeout timer of g, nil when there is none.
func (h *harness) ackTimer(g *dbgen.LockGroupsRow) (*time.Time, int64) {
	d, ok := h.db.timers[timerKey(g.ID, TimerAckTimeout)]
	if !ok {
		return nil, 0
	}
	return &d, h.db.notices[timerKey(g.ID, TimerAckTimeout)]
}

func (f *fakeDB) GetGroupTimer(_ context.Context, arg dbgen.GetGroupTimerParams) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetGroupTimer"); err != nil {
		return time.Time{}, err
	}
	d, ok := f.timers[timerKey(arg.AlertGroupID, arg.Kind)]
	if !ok {
		return time.Time{}, pgx.ErrNoRows
	}
	return d, nil
}

func (f *fakeDB) GetLastReminderSeq(_ context.Context, arg dbgen.GetLastReminderSeqParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetLastReminderSeq"); err != nil {
		return 0, err
	}
	var seq int64
	for _, e := range f.entries {
		if e.AlertGroupID == arg.AlertGroupID && (e.Event.String == "reminder" ||
			(e.Event.String == "notices_missed" && slices.Contains(e.Mentions, "owner"))) {
			seq = max(seq, e.EventSeq.Int64)
		}
	}
	if seq == 0 {
		return 0, pgx.ErrNoRows
	}
	return seq, nil
}

// fire fires a timer of g as the timer worker does: the handler of its kind in its transaction, which then deletes
// the timer unless the handler set it again, and what the handler returned once that committed.
func (h *harness) fire(t *testing.T, g *dbgen.LockGroupsRow, kind string) {
	t.Helper()
	key := timerKey(g.ID, kind)
	claimed, upserts := h.db.timers[key], h.db.upserts[key]
	var after func(context.Context)
	var err error
	switch kind {
	case TimerAckTimeout:
		after, err = h.svc.FireAckTimeout(t.Context(), nil, g.ID)
	case TimerReminder:
		after, err = h.svc.FireReminder(t.Context(), nil, g.ID, claimed)
	case TimerSnoozeEnd:
		after, err = h.svc.EndSnooze(t.Context(), nil, g.ID)
	default:
		t.Fatalf("no handler for %s", kind)
	}
	if err != nil {
		t.Fatalf("fire %s: %v", kind, err)
	}
	if d, ok := h.db.timers[key]; ok && d.Equal(claimed) && h.db.upserts[key] == upserts {
		delete(h.db.timers, key)
		delete(h.db.notices, key)
	}
	if after != nil {
		after(t.Context())
	}
}

// runUntil moves the clock to until as the timer worker sees it: each timer of g that comes due on the way fires at
// its deadline, in deadline order.
func (h *harness) runUntil(t *testing.T, g *dbgen.LockGroupsRow, until time.Time) {
	t.Helper()
	for range 100 {
		var kind string
		var next time.Time
		for _, k := range []string{TimerAckTimeout, TimerSnoozeEnd, TimerReminder} {
			if d, ok := h.db.timers[timerKey(g.ID, k)]; ok && !d.After(until) && (kind == "" || d.Before(next)) {
				kind, next = k, d
			}
		}
		if kind == "" {
			h.clock.Set(until)
			return
		}
		if next.After(h.clock.Now()) {
			h.clock.Set(next)
		}
		h.fire(t, g, kind)
	}
	t.Fatal("the timers never settle")
}

// timerEntry is a Timeline entry of the kind timers, as the tests compare it.
type timerEntry struct {
	event    string
	at       time.Duration
	notice   int64
	missed   int64
	loudness string
	mentions string
}

// timerEntries are the entries of the kind timers of g, at their offset from t0.
func (h *harness) timerEntries(g *dbgen.LockGroupsRow) []timerEntry {
	var out []timerEntry
	for _, e := range h.entriesOf(g) {
		if e.Kind == string(KindTimers) {
			out = append(out, timerEntry{e.Event.String, e.At.Sub(t0), e.NoticeNumber.Int64, e.MissedCount.Int64,
				e.Loudness.String, strings.Join(e.Mentions, ",")})
		}
	}
	return out
}

// hintsOf counts the live-update hints of a type sent so far, of g for alert-group.
func (h *harness) hintsOf(typ string, g *dbgen.LockGroupsRow) int {
	n := 0
	for _, x := range h.db.hints {
		if x.Type == typ && (typ != live.HintAlertGroup || x.ID == g.PublicID) {
			n++
		}
	}
	return n
}

// TestAckTimeoutNotices is C-17.AC-1, C-17.FR-1 to FR-3 and C-17.AC-6 with a manual clock: an Alert Group without a
// Publication has no ack timeout; from its first Publication the notices come at exactly 15, 45 and 105 minutes, Loud
// with the Mention ack_timeout and their numbers, then unclaimed, Quiet, makes it Unclaimed — re-rendered and hinted
// to the list — and no fourth notice follows. A second MarkPublished, as after a republication, changes nothing.
func TestAckTimeoutNotices(t *testing.T) {
	h := newHarness(t)
	h.onCall()
	var unclaimedRenders []bool
	h.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
		for _, e := range r.Events {
			if e.Event == EventUnclaimed {
				unclaimedRenders = append(unclaimedRenders, r.Group.Unclaimed)
			}
		}
		return nil
	})
	g := h.firing(t, "x")
	h.runUntil(t, g, t0.Add(3*time.Hour))
	if d, _ := h.ackTimer(g); d != nil || len(h.timerEntries(g)) != 0 || g.AckTimeoutStartedAt.Valid {
		t.Fatalf("an unpublished alert group has an ack timeout: %v %v", d, h.timerEntries(g))
	}
	published := t0.Add(3 * time.Hour)
	hints := h.hintsOf(live.HintAlertGroup, g)
	h.publish(t, g, published)
	if g.FirstPublishedAt != ts(published) || g.AckTimeoutStartedAt != ts(published) ||
		h.hintsOf(live.HintAlertGroup, g) != hints+1 {
		t.Fatalf("published = %+v", g)
	}
	if d, n := h.ackTimer(g); d == nil || !d.Equal(published.Add(15*time.Minute)) || n != 1 {
		t.Fatalf("ack_timeout timer %v notice %d", d, n)
	}
	// A republication after the Root message was deleted marks nothing and does not start the ack timeout over.
	h.clock.Set(published.Add(10 * time.Minute))
	h.publish(t, g, h.clock.Now())
	if g.FirstPublishedAt != ts(published) || g.AckTimeoutStartedAt != ts(published) {
		t.Fatalf("published again = %+v", g)
	}
	groupsHints := h.hintsOf(live.HintAlertGroups, g)
	h.runUntil(t, g, published.Add(10*time.Hour))
	at := published.Sub(t0)
	want := []timerEntry{
		{"ack_timeout", at + 15*time.Minute, 1, 0, "loud", "ack_timeout"},
		{"ack_timeout", at + 45*time.Minute, 2, 0, "loud", "ack_timeout"},
		{"ack_timeout", at + 105*time.Minute, 3, 0, "loud", "ack_timeout"},
		{"unclaimed", at + 105*time.Minute, 0, 0, "quiet", ""},
	}
	if got := h.timerEntries(g); !slices.Equal(got, want) {
		t.Errorf("entries %v, want %v", got, want)
	}
	if d, _ := h.ackTimer(g); d != nil || !g.Unclaimed || g.AckTimeoutNoticesSent != 3 ||
		g.Status != "firing" {
		t.Errorf("after the third notice: timer %v, %+v", d, g)
	}
	if h.hintsOf(live.HintAlertGroups, g) != groupsHints+1 || !slices.Equal(unclaimedRenders, []bool{true}) {
		t.Errorf("alert-groups hints %d, renders %v", h.hintsOf(live.HintAlertGroups, g)-groupsHints,
			unclaimedRenders)
	}
	for _, line := range []string{`"kind":"ack_timeout","group":"` + g.PublicID + `","notice":1,"missed":0`,
		`"notice":3,"missed":0`} {
		if !strings.Contains(h.log.String(), `"event":"timer_fired"`) || !strings.Contains(h.log.String(), line) {
			t.Errorf("no %s in %s", line, h.log)
		}
	}
	// The timer fired again changes nothing.
	entries := len(h.entriesOf(g))
	if after, err := h.svc.FireAckTimeout(t.Context(), nil, g.ID); err != nil || after != nil ||
		len(h.entriesOf(g)) != entries {
		t.Errorf("a fourth firing = %v", err)
	}
}

// TestAckTimeoutStartsOver is C-17.AC-2, C-17.FR-1 and C-09.FR-8: acknowledging at minute 20 removes the pending
// notice and Unacknowledge at minute 30 starts the ack timeout over, so the next notice comes at minute 45; Reopen
// into firing, Unsnooze, Unresolve, a Snooze ending, a rise to Urgent that removes the acknowledgement or ends the
// Snooze, and the release of a disabled Owner start it over the same way, at the time of the transition.
func TestAckTimeoutStartsOver(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.onCall()
	g := h.firing(t, "x")
	h.publish(t, g, t0)
	h.runUntil(t, g, t0.Add(20*time.Minute))
	if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
		t.Fatal(err)
	}
	if d, _ := h.ackTimer(g); d != nil || g.AckTimeoutStartedAt.Valid {
		t.Fatalf("acknowledged: timer %v", d)
	}
	h.runUntil(t, g, t0.Add(30*time.Minute))
	if _, err := h.svc.Unacknowledge(t.Context(), bob, g.PublicID); err != nil {
		t.Fatal(err)
	}
	if d, n := h.ackTimer(g); d == nil || !d.Equal(t0.Add(45*time.Minute)) || n != 1 ||
		g.AckTimeoutStartedAt != ts(t0.Add(30*time.Minute)) || g.AckTimeoutNoticesSent != 0 {
		t.Fatalf("unacknowledged at 30: timer %v notice %d", d, n)
	}
	h.runUntil(t, g, t0.Add(46*time.Minute))
	if got := h.timerEntries(g); len(got) != 2 || got[1].at != 45*time.Minute || got[1].notice != 1 {
		t.Errorf("entries %v", got)
	}

	snooze := func(h *harness, g *dbgen.LockGroupsRow) {
		until := h.clock.Now().Add(time.Hour)
		if _, err := h.svc.Snooze(t.Context(), bob, g.PublicID, SnoozeEnd{Until: &until}); err != nil {
			t.Fatal(err)
		}
	}
	ack := func(h *harness, g *dbgen.LockGroupsRow) {
		if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		// run makes the Alert Group firing again and returns it; set publishes it and changes it before.
		run func(t *testing.T, h *harness, set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow
		set func(h *harness, g *dbgen.LockGroupsRow)
	}{
		{"reopen into firing", func(t *testing.T, h *harness, set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow {
			return h.reopen(t, set)
		}, nil},
		{"unsnooze", firingAgain(func(h *harness, g *dbgen.LockGroupsRow) (Result, error) {
			return h.svc.Unsnooze(t.Context(), bob, g.PublicID)
		}), snooze},
		{"unacknowledge", firingAgain(func(h *harness, g *dbgen.LockGroupsRow) (Result, error) {
			return h.svc.Unacknowledge(t.Context(), bob, g.PublicID)
		}), ack},
		{"unresolve", firingAgain(func(h *harness, g *dbgen.LockGroupsRow) (Result, error) {
			return h.svc.Unresolve(t.Context(), bob, g.PublicID)
		}), func(h *harness, g *dbgen.LockGroupsRow) {
			if _, err := h.svc.Resolve(t.Context(), bob, g.PublicID, nil); err != nil {
				t.Fatal(err)
			}
		}},
		{"a snooze ending", func(t *testing.T, h *harness, set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow {
			g := h.firing(t, "x")
			set(g)
			h.runUntil(t, g, h.clock.Now().Add(53*time.Minute))
			return g
		}, snooze},
		{"a rise to urgent that removes the acknowledgement",
			func(t *testing.T, h *harness, set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow {
				return h.rise(t, set)
			},
			ack},
		{"a rise to urgent that ends the snooze",
			func(t *testing.T, h *harness, set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow {
				return h.rise(t, set)
			},
			snooze},
		{"the release of a disabled owner", firingAgain(func(h *harness, _ *dbgen.LockGroupsRow) (Result, error) {
			after, err := h.svc.ReleaseOwner(t.Context(), nil, bobID, ReasonOwnerDisabled)
			if after != nil {
				after(t.Context())
			}
			return Result{}, err
		}), ack},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.people()
			h.onCall()
			var firedAt time.Time
			g := c.run(t, h, func(g *dbgen.LockGroupsRow) {
				h.publish(t, g, h.clock.Now())
				h.clock.Advance(5 * time.Minute)
				if c.set != nil {
					c.set(h, g)
				}
				h.clock.Advance(7 * time.Minute)
			})
			firedAt = h.clock.Now()
			if g.Status != "firing" {
				t.Fatalf("status %s", g.Status)
			}
			if d, n := h.ackTimer(g); d == nil || !d.Equal(firedAt.Add(15*time.Minute)) || n != 1 ||
				g.AckTimeoutStartedAt != ts(firedAt) {
				t.Errorf("started over at %s: timer %v notice %d, started %v", firedAt.Sub(t0), d, n,
					g.AckTimeoutStartedAt)
			}
		})
	}
}

// firingAgain is a case of TestAckTimeoutStartsOver that starts a firing Alert Group, sets it up and makes it firing
// again with do.
func firingAgain(do func(h *harness, g *dbgen.LockGroupsRow) (Result, error)) func(t *testing.T, h *harness,
	set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow {
	return func(t *testing.T, h *harness, set func(*dbgen.LockGroupsRow)) *dbgen.LockGroupsRow {
		t.Helper()
		g := h.firing(t, "x")
		set(g)
		if _, err := do(h, g); err != nil {
			t.Fatal(err)
		}
		return g
	}
}

// TestSnoozeEndStartsAckTimeout is C-09.FR-8: when a Snooze ends while Alerts still fire, the Alert Group is firing
// without an Owner, snooze_ended is Loud and the next ack timeout notice is due one first interval later.
func TestSnoozeEndStartsAckTimeout(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.onCall()
	g := h.firing(t, "x")
	h.publish(t, g, t0)
	if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
		t.Fatal(err)
	}
	until := t0.Add(time.Hour)
	if _, err := h.svc.Snooze(t.Context(), bob, g.PublicID, SnoozeEnd{Until: &until}); err != nil {
		t.Fatal(err)
	}
	if d, _ := h.ackTimer(g); d != nil {
		t.Fatalf("snoozed with an ack timeout at %v", d)
	}
	h.runUntil(t, g, until)
	e := h.last(t, g)
	if g.Status != "firing" || g.OwnerUserID.Valid || e.Event.String != "snooze_ended" || e.Loudness.String != "loud" {
		t.Fatalf("after the snooze: %s, %+v", g.Status, e.InsertTimelineEntryParams)
	}
	if d, n := h.ackTimer(g); d == nil || !d.Equal(until.Add(15*time.Minute)) || n != 1 {
		t.Errorf("ack_timeout timer %v notice %d", d, n)
	}
}

// TestUnclaimedEnds is C-17.AC-8 and C-17.FR-3: an Unclaimed Alert Group stops being Unclaimed when it is
// acknowledged, snoozed or resolved by a person or the system, and the list is hinted; after Unacknowledge it is not
// Unclaimed again until the third notice of the new ack timeout.
func TestUnclaimedEnds(t *testing.T) {
	cases := []struct {
		name string
		end  func(t *testing.T, h *harness, g *dbgen.LockGroupsRow)
	}{
		{"acknowledged", func(t *testing.T, h *harness, g *dbgen.LockGroupsRow) {
			if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
				t.Fatal(err)
			}
		}},
		{"snoozed", func(t *testing.T, h *harness, g *dbgen.LockGroupsRow) {
			until := h.clock.Now().Add(time.Hour)
			if _, err := h.svc.Snooze(t.Context(), bob, g.PublicID, SnoozeEnd{Until: &until}); err != nil {
				t.Fatal(err)
			}
		}},
		{"resolved by a person", func(t *testing.T, h *harness, g *dbgen.LockGroupsRow) {
			if _, err := h.svc.Resolve(t.Context(), bob, g.PublicID, nil); err != nil {
				t.Fatal(err)
			}
		}},
		{"resolved by the system", func(t *testing.T, h *harness, _ *dbgen.LockGroupsRow) {
			h.resolve(t, h.db.members[len(h.db.members)-1].alert)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.people()
			h.onCall()
			g := h.firing(t, "x")
			h.publish(t, g, t0)
			h.runUntil(t, g, t0.Add(2*time.Hour))
			if !g.Unclaimed {
				t.Fatal("not unclaimed")
			}
			hints := h.hintsOf(live.HintAlertGroups, g)
			c.end(t, h, g)
			if d, _ := h.ackTimer(g); g.Unclaimed || d != nil || g.AckTimeoutStartedAt.Valid ||
				h.hintsOf(live.HintAlertGroups, g) <= hints {
				t.Errorf("after the end: unclaimed %v, timer %v", g.Unclaimed, d)
			}
		})
	}
	h := newHarness(t)
	h.people()
	h.onCall()
	g := h.firing(t, "x")
	h.publish(t, g, t0)
	h.runUntil(t, g, t0.Add(2*time.Hour))
	for _, cmd := range []Command{CommandAcknowledge, CommandUnacknowledge} {
		if _, err := h.do(bob, cmd, g); err != nil {
			t.Fatal(err)
		}
	}
	h.runUntil(t, g, t0.Add(2*time.Hour+104*time.Minute))
	if g.Unclaimed || g.AckTimeoutNoticesSent != 2 {
		t.Fatalf("before the third new notice: unclaimed %v, %d sent", g.Unclaimed, g.AckTimeoutNoticesSent)
	}
	h.runUntil(t, g, t0.Add(2*time.Hour+105*time.Minute))
	if !g.Unclaimed || g.AckTimeoutNoticesSent != 3 {
		t.Errorf("after the third new notice: unclaimed %v", g.Unclaimed)
	}
}

// TestMarkPublished is C-17.FR-1: the first Publication of an Alert Group that is not firing marks it published and
// starts nothing, so that the next transition into firing starts the ack timeout; a Route without an ack timeout
// starts none; an Alert Group that is gone is nothing to mark; failures are returned and change nothing.
func TestMarkPublished(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.onCall()
	g := h.firing(t, "x")
	if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
		t.Fatal(err)
	}
	h.publish(t, g, t0.Add(time.Minute))
	if d, _ := h.ackTimer(g); d != nil || g.FirstPublishedAt != ts(t0.Add(time.Minute)) {
		t.Fatalf("acknowledged when published: timer %v, %+v", d, g)
	}
	h.clock.Set(t0.Add(time.Hour))
	if _, err := h.svc.Unacknowledge(t.Context(), bob, g.PublicID); err != nil {
		t.Fatal(err)
	}
	if d, _ := h.ackTimer(g); d == nil || !d.Equal(t0.Add(75*time.Minute)) {
		t.Errorf("unacknowledged after the publication: timer %v", d)
	}
	if err := h.svc.MarkPublished(t.Context(), 9999, t0); err != nil {
		t.Errorf("an alert group that is gone = %v", err)
	}

	off := h.firing(t, "y")
	h.db.routes[2].timers.AckTimeoutEnabled = false
	h.publish(t, off, t0)
	if d, _ := h.ackTimer(off); d != nil || off.AckTimeoutStartedAt.Valid || !off.FirstPublishedAt.Valid {
		t.Errorf("a route without an ack timeout: timer %v, %+v", d, off)
	}

	for _, q := range []string{"LockGroups", "GetTimerPolicy", "UpsertTimer", "SaveGroup"} {
		h.db.routes[2].timers.AckTimeoutEnabled = true
		g := h.firing(t, "z"+q)
		h.db.fail[q] = errBoom
		if err := h.svc.MarkPublished(t.Context(), g.ID, t0); !errors.Is(err, errBoom) {
			t.Errorf("%s failing = %v", q, err)
		}
		delete(h.db.fail, q)
	}
}

// TestFireAckTimeout is C-17.FR-7 and the edges of the ack_timeout timer: overdue notices collapse into one Loud
// notices_missed with the Mention ack_timeout, the number of the last and how many it stands for, then unclaimed when
// the third is among them; fewer than all left schedule the next one from now; a first interval that grew since the
// timer was set only moves it; an Alert Group that is no longer firing, whose Route turned the ack timeout off or that
// is gone fires nothing, and failures are returned.
func TestFireAckTimeout(t *testing.T) {
	setup := func(t *testing.T) (*harness, *dbgen.LockGroupsRow) {
		h := newHarness(t)
		h.people()
		h.onCall()
		g := h.firing(t, "x")
		h.publish(t, g, t0)
		return h, g
	}
	t.Run("all three overdue", func(t *testing.T) {
		h, g := setup(t)
		h.clock.Set(t0.Add(2 * time.Hour))
		h.fire(t, g, TimerAckTimeout)
		want := []timerEntry{{"notices_missed", 2 * time.Hour, 3, 3, "loud", "ack_timeout"},
			{"unclaimed", 2 * time.Hour, 0, 0, "quiet", ""}}
		if got := h.timerEntries(g); !slices.Equal(got, want) || !g.Unclaimed {
			t.Errorf("entries %v, want %v", got, want)
		}
		if !strings.Contains(h.log.String(), `"event":"timer_fired"`) ||
			!strings.Contains(h.log.String(), `"notice":3,"missed":3`) {
			t.Errorf("log %s", h.log)
		}
	})
	t.Run("the last two overdue", func(t *testing.T) {
		h, g := setup(t)
		h.runUntil(t, g, t0.Add(20*time.Minute))
		h.clock.Set(t0.Add(3 * time.Hour))
		h.fire(t, g, TimerAckTimeout)
		got := h.timerEntries(g)
		if len(got) != 3 || got[1] != (timerEntry{"notices_missed", 3 * time.Hour, 3, 2, "loud", "ack_timeout"}) ||
			got[2].event != "unclaimed" {
			t.Errorf("entries %v", got)
		}
	})
	t.Run("two of three overdue", func(t *testing.T) {
		h, g := setup(t)
		h.clock.Set(t0.Add(50 * time.Minute))
		h.fire(t, g, TimerAckTimeout)
		got := h.timerEntries(g)
		if len(got) != 1 || got[0] != (timerEntry{"notices_missed", 50 * time.Minute, 2, 2, "loud", "ack_timeout"}) ||
			g.Unclaimed {
			t.Errorf("entries %v", got)
		}
		if d, n := h.ackTimer(g); d == nil || !d.Equal(t0.Add(110*time.Minute)) || n != 3 {
			t.Errorf("next from now: %v notice %d", d, n)
		}
	})
	t.Run("a first interval that grew", func(t *testing.T) {
		h, g := setup(t)
		h.db.routes[2].timers.AckTimeoutFirstIntervalSeconds = 1800
		h.clock.Set(t0.Add(15 * time.Minute))
		h.fire(t, g, TimerAckTimeout)
		if d, n := h.ackTimer(g); len(h.timerEntries(g)) != 0 || d == nil ||
			!d.Equal(t0.Add(30*time.Minute)) || n != 1 {
			t.Errorf("entries %v, timer %v notice %d", h.timerEntries(g), d, n)
		}
	})
	t.Run("not due for the alert group", func(t *testing.T) {
		h, g := setup(t)
		h.clock.Set(t0.Add(15 * time.Minute))
		h.db.routes[2].timers.AckTimeoutEnabled = false
		h.fire(t, g, TimerAckTimeout)
		if d, _ := h.ackTimer(g); len(h.timerEntries(g)) != 0 || d != nil {
			t.Errorf("a route that turned it off: entries %v, timer %v", h.timerEntries(g), d)
		}
		g.Status = "acknowledged"
		if after, err := h.svc.FireAckTimeout(t.Context(), nil, g.ID); err != nil || after != nil {
			t.Errorf("acknowledged = %v", err)
		}
		if after, err := h.svc.FireAckTimeout(t.Context(), nil, 9999); err != nil || after != nil {
			t.Errorf("an alert group that is gone = %v", err)
		}
	})
	t.Run("failures", func(t *testing.T) {
		for _, c := range []struct {
			query string
			at    time.Duration
		}{{"LockGroups", 15}, {"GetTimerPolicy", 15}, {"UpsertTimer", 15}, {"UpsertTimer", 16}, {"DeleteTimer", 120}} {
			h, g := setup(t)
			if c.at == 16 {
				h.db.routes[2].timers.AckTimeoutFirstIntervalSeconds = 1800
			}
			h.clock.Set(t0.Add(c.at * time.Minute))
			h.db.fail[c.query] = errBoom
			if _, err := h.svc.FireAckTimeout(t.Context(), nil, g.ID); !errors.Is(err, errBoom) {
				t.Errorf("%s failing at %d = %v", c.query, c.at, err)
			}
		}
	})
}

// reminderTimer is the deadline and notice number of the reminder timer of g, nil when there is none.
func (h *harness) reminderTimer(g *dbgen.LockGroupsRow) (*time.Time, int64) {
	d, ok := h.db.timers[timerKey(g.ID, TimerReminder)]
	if !ok {
		return nil, 0
	}
	return &d, h.db.notices[timerKey(g.ID, TimerReminder)]
}

// reminders are the numbers of the reminder entries of g and their offsets from t0.
func (h *harness) reminders(g *dbgen.LockGroupsRow) ([]int64, []time.Duration) {
	var ns []int64
	var at []time.Duration
	for _, e := range h.timerEntries(g) {
		if e.event == string(EventReminder) {
			ns, at = append(ns, e.notice), append(at, e.at)
		}
	}
	return ns, at
}

// reachedAll is the answer rule's check of a Reminder that reached a Destination, recording the events it was asked
// about; reachedNone that of Reminders that reached nobody, as when every Destination is Broken.
func reachedAll(asked *[]int64) ReminderCheck {
	return func(_ context.Context, _ DBTX, _, seq int64) (bool, error) {
		*asked = append(*asked, seq)
		return true, nil
	}
}

func reachedNone(context.Context, DBTX, int64, int64) (bool, error) { return false, nil }

// acknowledgedBy starts a firing Alert Group published at t0 and acknowledged by c at t0.
func (h *harness) acknowledgedBy(t *testing.T, c Caller, cluster string) *dbgen.LockGroupsRow {
	t.Helper()
	g := h.firing(t, cluster)
	h.publish(t, g, t0)
	if _, err := h.svc.Acknowledge(t.Context(), c, g.PublicID); err != nil {
		t.Fatal(err)
	}
	return g
}

// TestReminders is C-17.AC-3, C-17.FR-4 and C-10.FR-4: Reminders — Loud, mentioning the Owner, with their numbers —
// come at 4, 12, 28 and 52 hours after the acknowledgement, whether or not "Still on it" was answered at hour 5; a
// Takeover at hour 13 starts them over for the new Owner, the next at hour 17; a snoozed Alert Group gets none; a
// Reopen into acknowledged continues the steps; a Route without Reminders writes no timer.
func TestReminders(t *testing.T) {
	t.Run("the schedule and an answer", func(t *testing.T) {
		h := newHarness(t)
		h.people()
		h.onCall()
		g := h.acknowledgedBy(t, bob, "x")
		if d, n := h.reminderTimer(g); d == nil || !d.Equal(t0.Add(4*time.Hour)) || n != 1 {
			t.Fatalf("reminder timer %v notice %d", d, n)
		}
		if _, err := h.svc.AnswerReminder(t.Context(), bob, g.PublicID); code(err) != CodeNoReminderPending {
			t.Errorf("before the first reminder = %v", err)
		}
		h.runUntil(t, g, t0.Add(5*time.Hour))
		if _, err := h.svc.AnswerReminder(t.Context(), bob, g.PublicID); err != nil {
			t.Fatal(err)
		}
		if d, n := h.reminderTimer(g); d == nil || !d.Equal(t0.Add(12*time.Hour)) || n != 2 {
			t.Errorf("after the answer: reminder timer %v notice %d", d, n)
		}
		h.runUntil(t, g, t0.Add(53*time.Hour))
		ns, at := h.reminders(g)
		if !slices.Equal(ns, []int64{1, 2, 3, 4}) ||
			!slices.Equal(at, []time.Duration{4 * time.Hour, 12 * time.Hour, 28 * time.Hour, 52 * time.Hour}) {
			t.Errorf("reminders %v at %v", ns, at)
		}
		for _, e := range h.entriesOf(g) {
			if e.Event.String == string(EventReminder) && (e.OwnerUserID != i8(bobID) || e.Loudness.String != "loud" ||
				!slices.Equal(e.Mentions, []string{"owner"}) || e.Kind != "timers") {
				t.Errorf("reminder entry %+v", e.InsertTimelineEntryParams)
			}
		}
		if d, n := h.reminderTimer(g); d == nil || !d.Equal(t0.Add(76*time.Hour)) || n != 5 ||
			g.RemindersSent != 4 || !g.LastReminderAt.Valid || !g.LastReminderAt.Time.Equal(t0.Add(52*time.Hour)) {
			t.Errorf("after hour 52: timer %v notice %d, %+v", d, n, g)
		}
		if !strings.Contains(h.log.String(), `"kind":"reminder","group":"`+g.PublicID+`","notice":4,"missed":0`) {
			t.Errorf("no timer_fired in %s", h.log)
		}
	})
	t.Run("a takeover at hour 13", func(t *testing.T) {
		h := newHarness(t)
		h.people()
		h.onCall()
		g := h.acknowledgedBy(t, alice, "x")
		h.runUntil(t, g, t0.Add(13*time.Hour))
		if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
			t.Fatal(err)
		}
		if d, n := h.reminderTimer(g); d == nil || !d.Equal(t0.Add(17*time.Hour)) || n != 1 || g.RemindersSent != 0 ||
			g.LastReminderAt.Valid {
			t.Fatalf("after the takeover: timer %v notice %d, %+v", d, n, g)
		}
		h.runUntil(t, g, t0.Add(17*time.Hour))
		if e := h.last(t, g); e.Event.String != string(EventReminder) || e.OwnerUserID != i8(bobID) ||
			e.NoticeNumber.Int64 != 1 {
			t.Errorf("reminder after the takeover %+v", e.InsertTimelineEntryParams)
		}
	})
	t.Run("snoozed", func(t *testing.T) {
		h := newHarness(t)
		h.people()
		h.onCall()
		g := h.acknowledgedBy(t, bob, "x")
		if _, err := h.svc.Snooze(t.Context(), bob, g.PublicID, SnoozeEnd{NoEnd: true}); err != nil {
			t.Fatal(err)
		}
		h.runUntil(t, g, t0.Add(100*time.Hour))
		if d, _ := h.reminderTimer(g); d != nil || len(h.timerEntries(g)) != 0 {
			t.Errorf("snoozed: timer %v, entries %v", d, h.timerEntries(g))
		}
	})
	t.Run("a reopen into acknowledged", func(t *testing.T) {
		h := newHarness(t)
		h.people()
		h.onCall()
		a := h.alert(2, "warning", map[string]string{"alertname": "A", "cluster": "x"})
		h.changes(t, ingest.ChangeFired, a)
		g := h.groupOf(t, a)
		h.publish(t, g, t0)
		if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
			t.Fatal(err)
		}
		h.runUntil(t, g, t0.Add(13*time.Hour))
		h.resolve(t, a)
		if d, _ := h.reminderTimer(g); d != nil {
			t.Fatalf("resolved with a reminder timer at %v", d)
		}
		h.clock.Advance(5 * time.Minute)
		reopened := h.clock.Now()
		h.refire(t, a)
		if d, n := h.reminderTimer(g); g.Status != "acknowledged" || d == nil ||
			!d.Equal(reopened.Add(16*time.Hour)) || n != 3 || g.RemindersSent != 2 {
			t.Errorf("reopened: %s, timer %v notice %d", g.Status, d, n)
		}
	})
	t.Run("a route without reminders", func(t *testing.T) {
		h := newHarness(t)
		h.people()
		h.onCall()
		h.db.routes[2].timers.RemindersEnabled = false
		g := h.acknowledgedBy(t, bob, "x")
		h.runUntil(t, g, t0.Add(100*time.Hour))
		if d, _ := h.reminderTimer(g); d != nil || len(h.timerEntries(g)) != 0 {
			t.Errorf("timer %v, entries %v", d, h.timerEntries(g))
		}
	})
}

// TestAutoUnacknowledge is C-17.AC-5 and C-17.FR-5: with auto-unacknowledge on and no answer to the Reminders at hours
// 4 and 12, the one due at hour 28 is not sent: the Alert Group is firing without an Owner, auto_unacknowledged is a
// Loud status entry mentioning the Owner who lost it, with the reason, and the ack timeout starts over, its notice 15
// minutes later; a Note by the Owner at hour 5 answers, so the Reminder at hour 28 is sent. Reminders that reached
// nobody — only Broken Destinations — never count, and one delivered only as an events-mode reminder event does.
func TestAutoUnacknowledge(t *testing.T) {
	setup := func(t *testing.T, check ReminderCheck) *harness {
		h := newHarness(t)
		h.people()
		h.onCall()
		h.db.routes[2].timers.AutoUnacknowledge = true
		h.svc.SetReminderReached(check)
		return h
	}
	t.Run("two unanswered reminders", func(t *testing.T) {
		var asked []int64
		h := setup(t, reachedAll(&asked))
		g7, g8 := h.acknowledgedBy(t, bob, "g7"), h.acknowledgedBy(t, bob, "g8")
		for _, until := range []time.Duration{4 * time.Hour, 5 * time.Hour} {
			h.runUntil(t, g7, t0.Add(until))
			h.runUntil(t, g8, t0.Add(until))
		}
		if _, err := h.svc.AddNote(t.Context(), bob, g8.PublicID, "Still checking the disk."); err != nil {
			t.Fatal(err)
		}
		for _, until := range []time.Duration{12 * time.Hour, 28 * time.Hour} {
			h.runUntil(t, g7, t0.Add(until))
			h.runUntil(t, g8, t0.Add(until))
		}
		if g7.Status != "firing" || g7.OwnerUserID.Valid {
			t.Fatalf("g7 = %s owner %v", g7.Status, g7.OwnerUserID)
		}
		var e *fakeEntry
		for _, x := range h.entriesOf(g7) {
			if x.Event.String == string(EventAutoUnacknowledged) {
				e = x
			}
		}
		if e == nil || e.Kind != "status" || e.Reason.String != "No answer to the last two Reminders" ||
			e.Loudness.String != "loud" || !slices.Equal(e.Mentions, []string{"owner"}) ||
			e.PreviousOwnerUserID != i8(bobID) || e.FromStatus.String != "acknowledged" ||
			e.ToStatus.String != "firing" || e.At != t0.Add(28*time.Hour) {
			t.Fatalf("auto_unacknowledged %+v", e)
		}
		if ns, _ := h.reminders(g7); !slices.Equal(ns, []int64{1, 2}) {
			t.Errorf("g7 reminders %v", ns)
		}
		if d, n := h.ackTimer(g7); d == nil || !d.Equal(t0.Add(28*time.Hour+15*time.Minute)) || n != 1 {
			t.Errorf("g7 ack timeout %v notice %d", d, n)
		}
		if d, _ := h.reminderTimer(g7); d != nil {
			t.Errorf("g7 reminder timer %v", d)
		}
		if ns, _ := h.reminders(g8); !slices.Equal(ns, []int64{1, 2, 3}) || g8.Status != "acknowledged" {
			t.Errorf("g8 reminders %v, %s", ns, g8.Status)
		}
		if len(asked) == 0 || !strings.Contains(h.log.String(), `"kind":"reminder","group":"`+g7.PublicID+
			`","notice":3,"missed":0`) {
			t.Errorf("asked %v, log %s", asked, h.log)
		}
	})
	t.Run("reminders that reached nobody", func(t *testing.T) {
		h := setup(t, reachedNone)
		g := h.acknowledgedBy(t, bob, "x")
		h.runUntil(t, g, t0.Add(80*time.Hour))
		if ns, _ := h.reminders(g); g.Status != "acknowledged" || !slices.Equal(ns, []int64{1, 2, 3, 4, 5}) ||
			g.RemindersUnanswered != 0 {
			t.Errorf("%s, reminders %v, unanswered %d", g.Status, ns, g.RemindersUnanswered)
		}
	})
	t.Run("without the check", func(t *testing.T) {
		h := setup(t, nil)
		g := h.acknowledgedBy(t, bob, "x")
		h.runUntil(t, g, t0.Add(30*time.Hour))
		if g.Status != "acknowledged" || g.RemindersUnanswered != 0 {
			t.Errorf("%s, unanswered %d", g.Status, g.RemindersUnanswered)
		}
	})
	t.Run("a reminder delivered only as an events-mode event", func(t *testing.T) {
		// The events-mode Destination delivered the reminder events, sequences the check answers for; the Reminder
		// whose event is still pending there did not reach it.
		var delivered []int64
		h := setup(t, func(_ context.Context, _ DBTX, _, seq int64) (bool, error) {
			return slices.Contains(delivered, seq), nil
		})
		g := h.acknowledgedBy(t, bob, "x")
		h.runUntil(t, g, t0.Add(4*time.Hour))
		h.runUntil(t, g, t0.Add(12*time.Hour))
		if g.RemindersUnanswered != 0 || g.Status != "acknowledged" {
			t.Fatalf("a pending event counted: unanswered %d", g.RemindersUnanswered)
		}
		delivered = append(delivered, h.last(t, g).EventSeq.Int64)
		h.runUntil(t, g, t0.Add(28*time.Hour))
		if g.RemindersUnanswered != 1 || g.Status != "acknowledged" {
			t.Fatalf("after one delivered: unanswered %d, %s", g.RemindersUnanswered, g.Status)
		}
		delivered = append(delivered, h.last(t, g).EventSeq.Int64)
		h.runUntil(t, g, t0.Add(52*time.Hour))
		if g.Status != "firing" || h.last(t, g).Event.String != string(EventAutoUnacknowledged) {
			t.Errorf("after two delivered: %s, %v", g.Status, h.events(g))
		}
	})
	t.Run("an Alert Group never published", func(t *testing.T) {
		var asked []int64
		h := setup(t, reachedAll(&asked))
		g := h.firing(t, "x")
		if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
			t.Fatal(err)
		}
		h.runUntil(t, g, t0.Add(29*time.Hour))
		if d, _ := h.ackTimer(g); g.Status != "firing" || d != nil {
			t.Errorf("auto-unacknowledged without a publication: %s, ack timer %v", g.Status, d)
		}
	})
}

// TestReminderCollapse is C-17.FR-7 and C-02.FR-12 for Reminders: overdue Reminders collapse into one Loud
// notices_missed mentioning the Owner, with how many it stands for and the number of the last; the next is scheduled
// from now, and the collapsed reply counts as one Reminder for auto-unacknowledge.
func TestReminderCollapse(t *testing.T) {
	var asked []int64
	h := newHarness(t)
	h.people()
	h.onCall()
	h.db.routes[2].timers.AutoUnacknowledge = true
	h.svc.SetReminderReached(reachedAll(&asked))
	g := h.acknowledgedBy(t, bob, "x")
	h.clock.Set(t0.Add(13 * time.Hour))
	h.fire(t, g, TimerReminder)
	want := []timerEntry{{"notices_missed", 13 * time.Hour, 2, 2, "loud", "owner"}}
	if got := h.timerEntries(g); !slices.Equal(got, want) || h.last(t, g).OwnerUserID != i8(bobID) {
		t.Fatalf("entries %v, want %v", got, want)
	}
	if d, n := h.reminderTimer(g); d == nil || !d.Equal(t0.Add(29*time.Hour)) || n != 3 || g.RemindersSent != 2 {
		t.Errorf("next from now: %v notice %d", d, n)
	}
	if !strings.Contains(h.log.String(), `"notice":2,"missed":2`) {
		t.Errorf("log %s", h.log)
	}
	h.runUntil(t, g, t0.Add(29*time.Hour))
	if g.Status != "acknowledged" || g.RemindersUnanswered != 1 || len(asked) != 1 {
		t.Fatalf("the collapsed reply counted as %d", g.RemindersUnanswered)
	}
	h.runUntil(t, g, t0.Add(53*time.Hour))
	if g.Status != "firing" {
		t.Errorf("after the next unanswered one: %s", g.Status)
	}
}

// TestFireReminder covers the edges of the reminder timer: one that comes due on an Alert Group no longer acknowledged
// or whose Route turned the Reminders off is dropped with reminder_skipped; one claimed before another change moved it
// changes nothing; one claimed early is set again; an Alert Group that is gone fires nothing; failures are returned.
func TestFireReminder(t *testing.T) {
	setup := func(t *testing.T) (*harness, *dbgen.LockGroupsRow) {
		h := newHarness(t)
		h.people()
		h.onCall()
		h.db.routes[2].timers.AutoUnacknowledge = true
		var asked []int64
		h.svc.SetReminderReached(reachedAll(&asked))
		return h, h.acknowledgedBy(t, bob, "x")
	}
	t.Run("skipped", func(t *testing.T) {
		h, g := setup(t)
		h.clock.Set(t0.Add(4 * time.Hour))
		h.db.routes[2].timers.RemindersEnabled = false
		h.fire(t, g, TimerReminder)
		if d, _ := h.reminderTimer(g); d != nil || len(h.timerEntries(g)) != 0 ||
			!strings.Contains(h.log.String(), `"event":"reminder_skipped","group":"`+g.PublicID+`","reason":"policy_off"`) {
			t.Errorf("policy off: timer %v, log %s", d, h.log)
		}
		g.Status = "firing"
		after, err := h.svc.FireReminder(t.Context(), nil, g.ID, t0.Add(4*time.Hour))
		if err != nil || after == nil {
			t.Fatalf("not acknowledged = %v", err)
		}
		after(t.Context())
		if !strings.Contains(h.log.String(), `"reason":"not_acknowledged"`) {
			t.Errorf("log %s", h.log)
		}
		if after, err := h.svc.FireReminder(t.Context(), nil, 9999, t0); err != nil || after != nil {
			t.Errorf("an alert group that is gone = %v", err)
		}
	})
	t.Run("moved or early", func(t *testing.T) {
		h, g := setup(t)
		h.clock.Set(t0.Add(5 * time.Hour))
		if _, err := h.svc.FireReminder(t.Context(), nil, g.ID, t0.Add(3*time.Hour)); err != nil ||
			len(h.timerEntries(g)) != 0 {
			t.Errorf("a moved timer = %v, entries %v", err, h.timerEntries(g))
		}
		delete(h.db.timers, timerKey(g.ID, TimerReminder))
		if _, err := h.svc.FireReminder(t.Context(), nil, g.ID, t0.Add(4*time.Hour)); err != nil ||
			len(h.timerEntries(g)) != 0 {
			t.Errorf("a deleted timer = %v", err)
		}
		h.db.timers[timerKey(g.ID, TimerReminder)] = t0.Add(6 * time.Hour)
		upserts := h.db.upserts[timerKey(g.ID, TimerReminder)]
		if _, err := h.svc.FireReminder(t.Context(), nil, g.ID, t0.Add(6*time.Hour)); err != nil ||
			len(h.timerEntries(g)) != 0 || h.db.upserts[timerKey(g.ID, TimerReminder)] != upserts+1 {
			t.Errorf("an early timer = %v", err)
		}
	})
	t.Run("failures", func(t *testing.T) {
		for _, c := range []struct {
			query string
			at    time.Duration
		}{{"LockGroups", 4}, {"GetTimerPolicy", 4}, {"GetGroupTimer", 4}, {"UpsertTimer", 4},
			{"GetLastReminderSeq", 12}, {"reached", 12}} {
			h, g := setup(t)
			h.runUntil(t, g, t0.Add(4*time.Hour))
			if c.at == 4 {
				h.db.timers[timerKey(g.ID, TimerReminder)] = t0.Add(4 * time.Hour)
			}
			h.clock.Set(t0.Add(c.at * time.Hour))
			if c.query == "reached" {
				h.svc.SetReminderReached(func(context.Context, DBTX, int64, int64) (bool, error) {
					return false, errBoom
				})
			} else {
				h.db.fail[c.query] = errBoom
			}
			if _, err := h.svc.FireReminder(t.Context(), nil, g.ID, t0.Add(c.at*time.Hour)); !errors.Is(err, errBoom) {
				t.Errorf("%s failing = %v", c.query, err)
			}
		}
		// The last Reminder's entry may be gone: it reached nobody.
		h, g := setup(t)
		h.runUntil(t, g, t0.Add(4*time.Hour))
		h.db.entries = slices.DeleteFunc(h.db.entries, func(e *fakeEntry) bool { return e.Event.String == "reminder" })
		h.runUntil(t, g, t0.Add(12*time.Hour))
		if g.RemindersUnanswered != 0 {
			t.Errorf("unanswered %d", g.RemindersUnanswered)
		}
	})
}

// TestReleaseEndsReminders is C-17.FR-8, C-03.FR-13 and C-03.AC-27: disabling the Owner of an acknowledged Alert Group
// whose Reminders run releases it: its reminder timer is deleted, so no Reminder and no auto-unacknowledge follow; the
// ack timeout starts over at the release and its third notice makes it Unclaimed again.
func TestReleaseEndsReminders(t *testing.T) {
	for _, reason := range []string{ReasonOwnerDisabled, ReasonOwnerDeleted} {
		t.Run(reason, func(t *testing.T) {
			var asked []int64
			h := newHarness(t)
			h.people()
			h.onCall()
			h.db.routes[2].timers.AutoUnacknowledge = true
			h.svc.SetReminderReached(reachedAll(&asked))
			g := h.acknowledgedBy(t, bob, "x")
			h.runUntil(t, g, t0.Add(5*time.Hour))
			after, err := h.svc.ReleaseOwner(t.Context(), nil, bobID, reason)
			if err != nil {
				t.Fatal(err)
			}
			if after != nil {
				after(t.Context())
			}
			if d, _ := h.reminderTimer(g); d != nil || g.Status != "firing" {
				t.Fatalf("released: %s, reminder timer %v", g.Status, d)
			}
			if d, n := h.ackTimer(g); d == nil || !d.Equal(t0.Add(5*time.Hour+15*time.Minute)) || n != 1 {
				t.Errorf("ack timeout %v notice %d", d, n)
			}
			h.runUntil(t, g, t0.Add(100*time.Hour))
			if ns, _ := h.reminders(g); !slices.Equal(ns, []int64{1}) || !g.Unclaimed ||
				slices.Contains(h.events(g), string(EventAutoUnacknowledged)) {
				t.Errorf("after the release: reminders %v, unclaimed %v, %v", ns, g.Unclaimed, h.events(g))
			}
		})
	}
}

// TestAnswerReminder is C-17.FR-10, C-10.FR-16 and C-04.FR-2: "Still on it" is refused, in this order, with
// owner_must_be_user for a Service account, not_owner for anyone but the Owner and no_reminder_pending without a
// Reminder waiting; by the Owner — a Personal access token acting as its User — it records reminder_answered, Quiet,
// the Audit log entry and the answer, counts muster_commands_total{command="still_on_it"} and leaves the schedule as it
// is; allowed_commands offers still_on_it only to the Owner while a Reminder waits.
func TestAnswerReminder(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.onCall()
	g := h.acknowledgedBy(t, bob, "x")
	allowed := func(c Caller) bool {
		t.Helper()
		v, err := h.svc.Get(t.Context(), g.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(v.Allowed(c), CommandStillOnIt)
	}
	if allowed(bob) {
		t.Error("offered before a reminder")
	}
	h.runUntil(t, g, t0.Add(4*time.Hour))
	if !allowed(bob) || !allowed(bobToken) || allowed(alice) || allowed(robot) {
		t.Errorf("allowed: bob %v, token %v, alice %v, robot %v", allowed(bob), allowed(bobToken), allowed(alice),
			allowed(robot))
	}
	for _, c := range []struct {
		caller Caller
		code   string
	}{{robot, CodeOwnerMustBeUser}, {alice, CodeNotOwner}, {carol, CodeForbidden}} {
		if _, err := h.svc.AnswerReminder(t.Context(), c.caller, g.PublicID); code(err) != c.code {
			t.Errorf("%s = %v, want %s", c.caller.Actor.PublicID, err, c.code)
		}
	}
	done := metrics.Commands.With(string(CommandStillOnIt), "api", "done")
	before, audits := done.Get(), len(h.db.audit)
	h.clock.Advance(time.Hour)
	r, err := h.svc.AnswerReminder(t.Context(), bobToken, g.PublicID)
	if err != nil || r.Outcome != OutcomeDone {
		t.Fatalf("answer = %v %+v", err, r)
	}
	e := h.last(t, g)
	if e.Event.String != string(EventReminderAnswered) || e.Kind != "timers" || e.Loudness.String != "quiet" ||
		len(e.Mentions) != 0 || e.ActorUserID != i8(bobID) || e.Transport != "api" ||
		!g.OwnerAnsweredAt.Valid || !g.OwnerAnsweredAt.Time.Equal(t0.Add(5*time.Hour)) {
		t.Errorf("entry %+v, answered %v", e.InsertTimelineEntryParams, g.OwnerAnsweredAt)
	}
	if len(h.db.audit) != audits+1 || h.db.audit[audits].Action != ActionReminderAnswered || done.Get() != before+1 {
		t.Errorf("audit %+v, counted %d", h.db.audit[audits:], done.Get()-before)
	}
	if d, n := h.reminderTimer(g); d == nil || !d.Equal(t0.Add(12*time.Hour)) || n != 2 || allowed(bob) {
		t.Errorf("after the answer: timer %v notice %d, still offered %v", d, n, allowed(bob))
	}
	if _, err := h.svc.AnswerReminder(t.Context(), bob, g.PublicID); code(err) != CodeNoReminderPending {
		t.Errorf("answered twice = %v", err)
	}
	if _, err := h.svc.Unacknowledge(t.Context(), alice, g.PublicID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.AnswerReminder(t.Context(), bob, g.PublicID); code(err) != CodeNotOwner {
		t.Errorf("not acknowledged = %v", err)
	}
	if CommandStillOnIt.Permission() != PermissionAcknowledge {
		t.Error("the permission of still_on_it")
	}
}

// TestOwnerAnswers is the answer of C-17.FR-5: a Command by the Owner that changes the Alert Group and a Note by the
// Owner answer a Reminder; another user's Note and an Acknowledge by the Owner that changes nothing do not.
func TestOwnerAnswers(t *testing.T) {
	h := newHarness(t)
	h.people()
	h.onCall()
	g := h.acknowledgedBy(t, bob, "x")
	h.runUntil(t, g, t0.Add(4*time.Hour))
	h.clock.Advance(time.Minute)
	if _, err := h.svc.AddNote(t.Context(), alice, g.PublicID, "looking too"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Acknowledge(t.Context(), bob, g.PublicID); err != nil {
		t.Fatal(err)
	}
	if g.OwnerAnsweredAt.Valid {
		t.Fatalf("answered by %v", g.OwnerAnsweredAt)
	}
	if _, err := h.svc.AddNote(t.Context(), bob, g.PublicID, "on it"); err != nil {
		t.Fatal(err)
	}
	if !g.OwnerAnsweredAt.Valid || !g.OwnerAnsweredAt.Time.Equal(t0.Add(4*time.Hour+time.Minute)) {
		t.Errorf("a note by the owner: answered %v", g.OwnerAnsweredAt)
	}
}
