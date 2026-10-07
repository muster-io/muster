// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// resolve resolves the Alert Group of the fixtures by the system at now, as the dispatcher does.
func (e *env) resolve(t *testing.T) {
	t.Helper()
	g := e.group(groups.StatusResolved, "a")
	e.db.groups[groupID].resolvedAt = at(e.business.Now())
	e.enqueue(t, g, groups.System, groups.Recorded{Seq: 2, Event: groups.EventResolved, Loudness: groups.Quiet})
}

// waitPublished runs rounds at the due time of d until its Root message is published.
func (e *env) waitPublished(t *testing.T, d *fakeDelivery) {
	t.Helper()
	for range 5 {
		if d.messageID != nil {
			return
		}
		e.business.Set(d.next)
		e.round(t)
	}
	t.Fatalf("never published %+v", d)
}

// publications are the recorded Publications.
func (e *env) publications() []deliverytest.Call {
	var out []deliverytest.Call
	for _, c := range e.rec.Calls() {
		if c.Method == deliverytest.MethodPublish {
			out = append(out, c)
		}
	}
	return out
}

// TestDeletedRootMessage is C-11.FR-13 and AC-4 through the recorder: a gone answer to an edit of an open Alert
// Group's Root message publishes it again once, Quietly, with "The previous message was deleted at HH:MM" and a new
// Thread; a gone answer for that one marks the pair deleted in the messenger and nothing more is sent.
func TestDeletedRootMessage(t *testing.T) {
	e := published(t)
	d := e.only(t)
	d.threadState, d.anchorID = "attached", at2("anchor")
	e.business.Set(time.Date(2026, 10, 7, 12, 42, 0, 0, time.UTC))
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "post not found"))
	e.round(t)
	if d.state != "pending" || !d.republished || d.messageID != nil || e.db.notified != 3 {
		t.Fatalf("reset %+v, %d wakes", d, e.db.notified)
	}
	e.round(t)
	calls := e.rec.Calls()
	if callMethods(calls) != "update,publish" {
		t.Fatalf("calls %s", callMethods(calls))
	}
	pub := calls[1]
	if pub.Loudness != groups.Quiet || len(pub.Mentions) != 0 ||
		pub.Message.Sections[len(pub.Message.Sections)-1] != "The previous message was deleted at 12:42" ||
		!strings.Contains(pub.Message.Text(), "Acknowledged") {
		t.Errorf("republication %+v", pub)
	}
	if d.state != "delivered" || !d.republished || *d.messageID != "m2" || d.threadState != "none" ||
		d.anchorID != nil || d.publications != 2 {
		t.Errorf("delivery %+v", d)
	}
	if !slices.Equal(e.eventKinds(), []string{"publication", "republished", "publication"}) ||
		e.db.events[2].Loudness != "quiet" {
		t.Errorf("events %v", e.eventKinds())
	}
	// The republished message is deleted too: marked, and left alone.
	e.rec.Reset()
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System,
		groups.Recorded{Seq: 3, Event: groups.EventUnacknowledged, Variant: groups.VariantCommand,
			Loudness: groups.Quiet})
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, ""))
	e.round(t)
	if d.state != "deleted_in_messenger" || len(e.rec.Calls()) != 1 ||
		e.db.events[len(e.db.events)-1].Kind != "deleted_in_messenger" {
		t.Fatalf("deleted twice %+v, %v", d, e.eventKinds())
	}
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 4, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System, groups.Recorded{Seq: 5,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	e.business.Advance(time.Hour)
	e.round(t)
	if d.state != "deleted_in_messenger" || len(e.rec.Calls()) != 1 {
		t.Errorf("sent after the mark: %s", callMethods(e.rec.Calls()))
	}
}

// TestDeletedRootResolved is C-11.FR-13: for a resolved Alert Group only the mark is recorded, and the pending
// Thread replies are dropped.
func TestDeletedRootResolved(t *testing.T) {
	e := published(t)
	e.resolve(t)
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "gone"))
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "gone"))
	e.round(t)
	d := e.only(t)
	if d.state != "deleted_in_messenger" || e.rec.Count(deliverytest.MethodPublish) != 0 ||
		e.db.events[len(e.db.events)-1].Kind != "deleted_in_messenger" {
		t.Errorf("resolved %+v %s", d, callMethods(e.rec.Calls()))
	}
	for _, r := range e.db.replies {
		if r.state != "dropped" {
			t.Errorf("reply %+v", r)
		}
	}
}

// TestReplyGone is C-11.FR-13 from a Thread reply: the deleted Root message is published again and the reply waits
// for it, then goes into the new Thread.
func TestReplyGone(t *testing.T) {
	e := published(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 2,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "root deleted"))
	e.round(t)
	r := e.db.replies[0]
	if r.state != "pending" || r.owner != "" || !e.only(t).republished {
		t.Fatalf("after gone %+v", r)
	}
	e.round(t)
	calls := e.rec.Calls()
	if callMethods(calls) != "reply,publish,reply" || calls[2].MessageID != "m2" || r.state != "sent" {
		t.Errorf("calls %s %+v", callMethods(calls), calls)
	}
	// A reply gone again: the pair is deleted in the messenger and its replies dropped.
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "root deleted"))
	e.round(t)
	if e.only(t).state != "deleted_in_messenger" || e.db.replies[1].state != "dropped" {
		t.Errorf("gone again %+v", e.db.replies[1])
	}
}

// TestPossibleDuplicate is C-11.FR-12 and AC-5 through the recorder: a worker stopped after the adapter accepted a
// Publication and before the record leaves publication_started_at set; the next attempt publishes again, sets
// possible_duplicate and records a possible_duplicate delivery event and a WARN line.
func TestPossibleDuplicate(t *testing.T) {
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Then: func() { e.db.fail["RecordDelivered"] = errBoom }})
	e.round(t)
	if d.started == nil || d.messageID != nil || d.owner == "" {
		t.Fatalf("after the stop %+v", d)
	}
	delete(e.db.fail, "RecordDelivered")
	e.real.Advance(delivery.Lease)
	e.round(t)
	if e.rec.Count(deliverytest.MethodPublish) != 2 || !d.possibleDuplicate || d.state != "delivered" {
		t.Fatalf("second worker %+v", d)
	}
	if !slices.Equal(e.eventKinds(), []string{"possible_duplicate", "publication"}) ||
		e.db.events[0].AlertGroupID.Int64 != groupID || e.db.events[0].Loudness != "quiet" {
		t.Errorf("events %v", e.eventKinds())
	}
	if !strings.Contains(e.log.String(), `"event":"delivery_possible_duplicate"`) ||
		!strings.Contains(e.log.String(), `"level":"WARN"`) {
		t.Errorf("log %s", e.log)
	}
	states, _ := e.svc.States(t.Context(), "AGAAAAAAAAAA21")
	if !states[0].PossibleDuplicate {
		t.Errorf("states %+v", states)
	}
	// A definite refusal clears the start: no duplicate next time.
	e2, d2 := pending(t)
	e2.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
	e2.round(t)
	e2.business.Set(d2.next)
	e2.round(t)
	if d2.possibleDuplicate || d2.state != "delivered" {
		t.Errorf("a refused publication counted as a duplicate %+v", d2)
	}
	for _, q := range []string{"MarkPossibleDuplicate", "InsertDeliveryEvent"} {
		e3, d3 := pending(t)
		d3.started = at(business0)
		e3.db.fail[q] = errBoom
		e3.round(t)
		if !strings.Contains(e3.log.String(), `"event":"delivery_work_failed"`) {
			t.Errorf("%s: log %s", q, e3.log)
		}
	}
}

// TestLatePublication is C-11.FR-11, AC-9 and AC-12 through the recorder: an Alert Group resolved while its first
// Publication waited for a RetryAfter, or for Transient retries within the budget, is published Quietly with the
// late note and a delivered_late delivery event.
func TestLatePublication(t *testing.T) {
	for _, answer := range []deliverytest.Answer{
		deliverytest.RetryAfter(30*time.Second, delivery.ScopeDestination),
		deliverytest.Failure(delivery.OutcomeTransient, "HTTP 503"),
	} {
		t.Run(string(answer.Outcome.Kind), func(t *testing.T) {
			e, d := pending(t)
			e.rec.Script(deliverytest.MethodPublish, answer)
			e.round(t)
			e.business.Advance(time.Second)
			e.resolve(t)
			if !d.lateNote || d.loud.Bool || d.state != "pending" {
				t.Fatalf("after the resolve %+v", d)
			}
			e.waitPublished(t, d)
			calls := e.publications()
			pub := calls[len(calls)-1]
			if len(calls) != 2 || pub.Loudness != groups.Quiet ||
				len(pub.Mentions) != 0 || pub.Message.Sections[len(pub.Message.Sections)-1] !=
				"Delivered late: started 12:00, resolved 12:00 while this Destination was unavailable." {
				t.Fatalf("late publication %+v", calls)
			}
			if !slices.Equal(e.eventKinds(), []string{"publication", "delivered_late"}) ||
				e.db.events[0].Loudness != "quiet" || e.db.events[1].Loudness != "quiet" {
				t.Errorf("events %+v", e.db.events)
			}
		})
	}
	// Reopened before the late Publication: the note is gone and the loudness follows the recovery rule, Loud with
	// new_alert_group while it fires.
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(time.Second, delivery.ScopeDestination))
	e.round(t)
	e.resolve(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventReopened, Variant: groups.VariantFiring, Loudness: groups.Loud})
	if d.lateNote || !d.loud.Valid || !d.loud.Bool {
		t.Errorf("reopened with %+v", d)
	}
	e.waitPublished(t, d)
	if pub := e.publications()[1]; strings.Contains(pub.Message.Text(), "Delivered late") ||
		slices.Contains(e.eventKinds(), "delivered_late") || pub.Loudness != groups.Loud ||
		!slices.Equal(pub.Mentions, []groups.Mention{groups.MentionNewAlertGroup}) {
		t.Errorf("a late note on an open alert group %+v", pub)
	}
	// Reopened acknowledged: Quiet, without the note.
	e, d = pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
	e.round(t)
	e.resolve(t)
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventReopened, Variant: groups.VariantAcknowledged, Loudness: groups.Loud})
	if d.lateNote || !d.loud.Valid || d.loud.Bool {
		t.Errorf("reopened acknowledged with %+v", d)
	}
	// Never attempted, or waiting for limiter tokens only: no error was recorded (last_error_class is NULL), the
	// resolve succeeds and the Publication goes without the note.
	e2, d2 := pending(t)
	e2.resolve(t)
	if d2.lateNote || d2.errorClass != nil || d2.state != "pending" {
		t.Errorf("a late note without a wait %+v", d2)
	}
}

// TestFakeMeetsSQLNull: the fake evaluates the booleans of SettleResolvedPublication as SQL does, so that a NULL from
// last_error_class written to late_note fails as in PostgreSQL; coalesce turns it into false.
func TestFakeMeetsSQLNull(t *testing.T) {
	waited := sqlIs(true).and(sqlIn(nil, "retry_after", "transient"))
	if !waited.null {
		t.Fatal("healthy AND NULL IN (...) is not NULL")
	}
	if _, err := notNull("late_note", sqlIs(false).or(waited)); err == nil ||
		!strings.Contains(err.Error(), "violates not-null constraint") {
		t.Errorf("false OR NULL into a NOT NULL column = %v", err)
	}
	if v, err := notNull("late_note", sqlIs(false).or(waited.coalesce(false))); err != nil || v {
		t.Errorf("with coalesce = %v %v", v, err)
	}
	if !sqlIs(true).or(waited).isTrue() || sqlIs(false).and(waited).null || waited.isTrue() {
		t.Error("three-valued logic")
	}
	if v, err := notNull("late_note", sqlIn(at2("x"), "x")); err != nil || !v {
		t.Errorf("a value IN its list = %v %v", v, err)
	}
	if sqlIs(true).and(sqlIs(true)).null || !sqlIs(true).and(sqlIs(true)).isTrue() || sqlIs(false).or(sqlIs(false)).v {
		t.Error("two-valued logic")
	}
}

// TestResolvedWhileBroken is C-11.FR-11 and FR-19: a first Publication whose Destination became Broken before the
// late Publication succeeded is withheld when its Alert Group resolves, never late.
func TestResolvedWhileBroken(t *testing.T) {
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
	e.round(t)
	e.breakDest(destMM, "503")
	e.resolve(t)
	if d.state != "withheld" || d.lateNote {
		t.Errorf("resolved while broken %+v", d)
	}
	states, _ := e.svc.States(t.Context(), "AGAAAAAAAAAA21")
	if states[0].State != "withheld" || states[0].MessageURL != nil {
		t.Errorf("states %+v", states)
	}
	// Withheld stays withheld while its Alert Group stays resolved; once it opens again it is published after all,
	// Loud while it fires, waiting for the Destination meanwhile, and the reply of the Reopen is dropped while Broken.
	e.enqueue(t, e.group(groups.StatusResolved, "z"), groups.System)
	if d.state != "withheld" {
		t.Errorf("revived while resolved %+v", d)
	}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventReopened, Variant: groups.VariantFiring, Loudness: groups.Loud})
	if last := e.db.replies[len(e.db.replies)-1]; d.state != "pending" || !d.loud.Bool || last.event != "reopened" ||
		last.state != "dropped" {
		t.Errorf("not revived %+v %+v", d, last)
	}
	e.db.fail["SettleResolvedPublication"] = errBoom
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusResolved, "b"),
		Actor: groups.System, Events: []groups.Recorded{{Seq: 4, Event: groups.EventResolved}}}); err == nil {
		t.Error("a failed settle")
	}
	delete(e.db.fail, "SettleResolvedPublication")
	e.db.fail["InsertThreadReply"] = errBoom
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring, "c"),
		Actor: groups.System, Events: []groups.Recorded{{Seq: 5, Event: groups.EventTakeover}}}); err == nil {
		t.Error("a failed dropped reply")
	}
}

// TestDeletedRootFailures: a failed query of the deleted Root message flow is logged and the row waits for its lease.
func TestDeletedRootFailures(t *testing.T) {
	for _, tc := range []struct {
		query    string
		resolved bool
	}{
		{"ResetForRepublish", false}, {"InsertDeliveryEvent", false}, {"NotifyDelivery", false},
		{"MarkDeletedInMessenger", true}, {"DropPendingReplies", true},
		{"GetLatestRepublishedAt", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			e := published(t)
			if tc.resolved {
				e.resolve(t)
			} else {
				e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
					groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
			}
			e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "gone"))
			if tc.query == "GetLatestRepublishedAt" {
				e.db.before["ResetForRepublish"] = func() { e.db.fail[tc.query] = errBoom }
			} else {
				e.db.fail[tc.query] = errBoom
			}
			e.round(t)
			e.round(t)
			if !strings.Contains(e.log.String(), `"event":"delivery_work_failed"`) {
				t.Errorf("log %s", e.log)
			}
		})
	}
	// Ended already, or republished concurrently: nothing more.
	e := published(t)
	d := e.only(t)
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Answer{
		Outcome: delivery.Outcome{Kind: delivery.OutcomeGone}, Then: func() { d.state = "retired" }})
	e.round(t)
	if d.state != "retired" || len(e.db.events) != 1 {
		t.Errorf("an ended row %+v %v", d, e.eventKinds())
	}
	e = published(t)
	e.resolve(t)
	d = e.only(t)
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Answer{
		Outcome: delivery.Outcome{Kind: delivery.OutcomeGone}, Then: func() { d.state = "withheld" }})
	e.round(t)
	if d.state != "withheld" || len(e.db.events) != 1 {
		t.Errorf("an ended row %+v %v", d, e.eventKinds())
	}
}
