// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// keyRecorder is the recording adapter that also records the idempotency key of each call, in order.
type keyRecorder struct {
	*deliverytest.Recorder
	mu   sync.Mutex
	keys []string
}

func (r *keyRecorder) key(c delivery.Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, c.Key)
}

// Keys are the keys of the calls so far, in order.
func (r *keyRecorder) Keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.keys)
}

// forget forgets the keys recorded so far.
func (r *keyRecorder) forget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = nil
}

func (r *keyRecorder) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	r.key(c)
	return r.Recorder.Publish(ctx, c, m)
}

func (r *keyRecorder) Update(ctx context.Context, c delivery.Call, messageID string, m delivery.Message) delivery.Outcome {
	r.key(c)
	return r.Recorder.Update(ctx, c, messageID, m)
}

func (r *keyRecorder) Reply(ctx context.Context, c delivery.Call, root delivery.Root, m delivery.Message) delivery.Outcome {
	r.key(c)
	return r.Recorder.Reply(ctx, c, root, m)
}

// idempotentRecorder is the key recorder as an Idempotent adapter with the keys of the Bridge protocol, as a bridge's
// delivery is (C-22.FR-6).
type idempotentRecorder struct{ *keyRecorder }

var _ delivery.Idempotent = idempotentRecorder{}

func (idempotentRecorder) PublicationKey(d delivery.Destination, deliveryID, publication int64) string {
	return fmt.Sprintf("pub-%s-%d-%d", d.PublicID, deliveryID, publication)
}

func (idempotentRecorder) ReplyKey(d delivery.Destination, replyID int64) string {
	return fmt.Sprintf("reply-%s-%d", d.PublicID, replyID)
}

// idempotent is rec as an Idempotent adapter.
func idempotent(rec *deliverytest.Recorder) idempotentRecorder {
	return idempotentRecorder{&keyRecorder{Recorder: rec}}
}

// bridgePublicID is the public_id of Destination 11 as a Bridge, its own so that its metrics are its own.
const bridgePublicID = "DSAAAAAAAAAB11"

// bridged is newEnv with Destination 11 a Bridge whose adapter is Idempotent, with a limiter that never runs dry and the
// jitter of delivery.transient_backoff fixed at half the bound.
func bridged(t *testing.T) (*env, idempotentRecorder) {
	t.Helper()
	e := newEnv(t)
	ds := e.db.dests[destMM]
	ds.typ, ds.publicID, ds.limit = delivery.TypeBridge, bridgePublicID, 600
	e.w.Random = func() float64 { return 0.5 }
	idem := idempotent(e.rec)
	e.w.Adapters[delivery.TypeBridge] = idem
	return e, idem
}

// pendingBridge is a bridged env whose Alert Group waits for its first Publication.
func pendingBridge(t *testing.T) (*env, *fakeDelivery, idempotentRecorder) {
	t.Helper()
	e, idem := bridged(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	return e, e.only(t), idem
}

// publishedBridge is a bridged env whose Alert Group has its Root message, published with the answer given, and
// nothing recorded yet.
func publishedBridge(t *testing.T, answer deliverytest.Answer) (*env, *fakeDelivery, idempotentRecorder) {
	t.Helper()
	e, d, idem := pendingBridge(t)
	e.rec.Script(deliverytest.MethodPublish, answer)
	e.round(t)
	if d.state != "delivered" {
		t.Fatalf("not published %+v", d)
	}
	e.rec.Reset()
	idem.forget()
	return e, d, idem
}

// timedOut is a Transient failure whose request may have reached the messenger without its answer, as after a
// timeout or a connection lost after the request was sent.
func timedOut() deliverytest.Answer {
	a := deliverytest.Failure(delivery.OutcomeTransient, "timeout")
	a.Outcome.Unanswered = true
	return a
}

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
		pub.Message.Notices[len(pub.Message.Notices)-1] != "The previous message was deleted at 12:42." ||
		!strings.Contains(textOf(pub), "Acknowledged") {
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
				len(pub.Mentions) != 0 || pub.Message.Notices[len(pub.Message.Notices)-1] !=
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
	if pub := e.publications()[1]; strings.Contains(textOf(pub), "Delivered late") ||
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

// TestIdempotentPossibleDuplicate is C-22.FR-6 and C-11.FR-12 for a Bridge: a worker stopped after the bridge took a
// Publication and before its outcome was recorded leaves publication_started_at set; the retry carries the same key,
// and its possible duplicate is decided by the outcome, not before the call — none when the answer is replayed, one,
// with its delivery event before the publication event and a WARN line, when it is not. Mattermost keeps recording it
// before the retry (TestPossibleDuplicate).
func TestIdempotentPossibleDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replayed bool
		events   []string
		state    string
	}{
		{"replayed", true, []string{"publication"}, "pending"},
		{"not replayed", false, []string{"possible_duplicate", "publication"}, "delivered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, d, idem := pendingBridge(t)
			calls := e.recordPublished()
			first := e.business.Now()
			e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{
				Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "m-1"},
				Then:    func() { e.db.fail["RecordDelivered"] = errBoom }})
			e.round(t)
			if d.started == nil || !d.started.Equal(first) || d.messageID != nil || d.owner == "" {
				t.Fatalf("after the stop %+v", d)
			}
			delete(e.db.fail, "RecordDelivered")
			e.real.Advance(delivery.Lease)
			e.business.Advance(time.Minute)
			retried := e.business.Now()
			var before []string
			e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{
				Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "m-1", Replayed: tc.replayed},
				Then: func() {
					if d.possibleDuplicate {
						before = append(before, "possible_duplicate")
					}
					before = append(before, e.eventKinds()...)
				}})
			e.round(t)
			key := fmt.Sprintf("pub-%s-%d-1", bridgePublicID, d.id)
			if !slices.Equal(idem.Keys(), []string{key, key}) {
				t.Errorf("keys %v", idem.Keys())
			}
			if len(before) != 0 || e.db.calls["MarkPossibleDuplicate"] != 0 {
				t.Errorf("recorded before the call: %v, %d", before, e.db.calls["MarkPossibleDuplicate"])
			}
			if d.possibleDuplicate == tc.replayed || !slices.Equal(e.eventKinds(), tc.events) || d.state != tc.state {
				t.Errorf("delivery %+v, events %v", d, e.eventKinds())
			}
			logged := strings.Contains(e.log.String(), `"event":"delivery_possible_duplicate"`)
			if logged == tc.replayed {
				t.Errorf("possible duplicate logged %v: %s", logged, e.log)
			}
			states, _ := e.svc.States(t.Context(), "AGAAAAAAAAAA21")
			if states[0].PossibleDuplicate == tc.replayed {
				t.Errorf("states %+v", states)
			}
			published := retried
			if tc.replayed {
				published = first
			}
			if d.publishedAt == nil || !d.publishedAt.Equal(published) || len(*calls) != 1 ||
				!(*calls)[0].at.Equal(published) {
				t.Errorf("published at %v, marked %+v", d.publishedAt, *calls)
			}
			if !tc.replayed {
				return
			}
			e.round(t)
			if callMethods(e.rec.Calls()) != "publish,publish,update" || d.state != "delivered" || d.possibleDuplicate {
				t.Errorf("after the replay %s %+v", callMethods(e.rec.Calls()), d)
			}
		})
	}
	// A failed record of the possible duplicate is logged; the row waits for its lease.
	e, d, _ := pendingBridge(t)
	d.started = at(business0.Add(-time.Minute))
	e.db.before["RecordDelivered"] = func() { e.db.fail["InsertDeliveryEvent"] = errBoom }
	e.round(t)
	if !strings.Contains(e.log.String(), `"error":"record the possible_duplicate delivery event: boom"`) {
		t.Errorf("a failed record: log %s", e.log)
	}
}
