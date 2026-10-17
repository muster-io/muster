// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"encoding/json"
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

// windowedRecorder is the idempotent recorder as an EditWindowed adapter with the keys of the Bridge protocol and an
// edit window a test sets, as a bridge's delivery is (C-22.FR-9). It counts the reads of the window.
type windowedRecorder struct {
	idempotentRecorder
	mu    sync.Mutex
	max   time.Duration
	ok    bool
	reads int
}

var _ delivery.EditWindowed = (*windowedRecorder)(nil)

func (r *windowedRecorder) EditWindow(context.Context, delivery.Call) (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	return r.max, r.ok
}

func (*windowedRecorder) PointerKey(d delivery.Destination, deliveryID, publication int64) string {
	return fmt.Sprintf("pointer-%s-%d-%d", d.PublicID, deliveryID, publication)
}

func (*windowedRecorder) FinalKey(d delivery.Destination, deliveryID int64) string {
	return fmt.Sprintf("final-%s-%d", d.PublicID, deliveryID)
}

// set declares the window: length, or none when ok is false (edits never expire, or info cannot be read).
func (r *windowedRecorder) set(length time.Duration, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.max, r.ok = length, ok
}

// Reads is how many times the window was read.
func (r *windowedRecorder) Reads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

// windowed is rec as a bridge's adapter whose info declares edit_max_age_seconds 600.
func windowed(rec *deliverytest.Recorder) *windowedRecorder {
	return &windowedRecorder{idempotentRecorder: idempotent(rec), max: 600 * time.Second, ok: true}
}

// windowedBridge is bridged with a windowed adapter and the base of links to Muster.
func windowedBridge(t *testing.T) (*env, *windowedRecorder) {
	t.Helper()
	e, _ := bridged(t)
	w := windowed(e.rec)
	e.w.Adapters[delivery.TypeBridge] = w
	e.w.PublicURL = "https://muster.example.org"
	return e, w
}

// publishedAs answers a Publication with the message id, its url and its thread_ref.
func publishedAs(id, ref string) deliverytest.Answer {
	return deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: id,
		MessageURL: "https://chat.example.org/m/" + id, ThreadRef: ref}}
}

// publishedWindowed is windowedBridge whose firing Alert Group has its Root message m1, Thread t-1, published at
// business0, with nothing recorded since.
func publishedWindowed(t *testing.T) (*env, *fakeDelivery, *windowedRecorder) {
	t.Helper()
	e, w := windowedBridge(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.rec.Script(deliverytest.MethodPublish, publishedAs("m1", "t-1"))
	e.round(t)
	d := e.only(t)
	if d.state != "delivered" || d.actualStatus == nil || *d.actualStatus != "firing" || w.Reads() != 0 {
		t.Fatalf("not published %+v, %d reads", d, w.Reads())
	}
	e.rec.Reset()
	w.forget()
	w.mu.Lock()
	w.reads = 0
	w.mu.Unlock()
	e.db.events = nil
	return e, d, w
}

// retitled enqueues a change of the Alert Group to status and title, as annotations_changed brings.
func (e *env) retitled(t *testing.T, status groups.Status, title string, seq int64) {
	t.Helper()
	e.enqueue(t, e.group(status, title), groups.System, groups.Recorded{Seq: seq,
		Event: groups.EventAnnotationsChanged, Loudness: groups.Quiet})
}

// acknowledged enqueues the acknowledgement of the Alert Group.
func (e *env) acknowledged(t *testing.T, seq int64) {
	t.Helper()
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System, groups.Recorded{Seq: seq,
		Event: groups.EventAcknowledged, Loudness: groups.Quiet})
}

// TestEditWindowBoundary is C-22.FR-9 with a manual clock: with edit_max_age_seconds 600 and bridge.edit_window_margin
// 60 s, a Root message 539 s old is edited, and one 540 s old is past its window: a Desired state with the status it
// shows already makes no call and takes no token, and the delivery stays delivered, flagged; once flagged, the window
// is not read again.
func TestEditWindowBoundary(t *testing.T) {
	if delivery.EditWindowMargin != time.Minute {
		t.Fatalf("bridge.edit_window_margin %v", delivery.EditWindowMargin)
	}
	e, d, w := publishedWindowed(t)
	e.business.Set(business0.Add(539 * time.Second))
	e.retitled(t, groups.StatusFiring, "b", 2)
	e.round(t)
	if calls := e.rec.Calls(); callMethods(calls) != "update" || calls[0].MessageID != "m1" || d.pastEditWindow ||
		d.state != "delivered" || w.Reads() != 1 {
		t.Fatalf("at 539 s: %s, %+v, %d reads", callMethods(calls), d, w.Reads())
	}
	e.rec.Reset()
	tokens := e.db.calls["TakeTokens"]
	e.business.Set(business0.Add(540 * time.Second))
	e.retitled(t, groups.StatusFiring, "c", 3)
	e.round(t)
	if len(e.rec.Calls()) != 0 || d.state != "delivered" || !d.pastEditWindow || d.receivedAt != nil ||
		e.db.calls["TakeTokens"] != tokens || w.Reads() != 2 || len(e.db.events) != 0 {
		t.Fatalf("at 540 s: %s, %+v, %d reads", callMethods(e.rec.Calls()), d, w.Reads())
	}
	e.retitled(t, groups.StatusFiring, "d", 4)
	e.round(t)
	if len(e.rec.Calls()) != 0 || d.state != "delivered" || w.Reads() != 2 {
		t.Errorf("flagged: %s, %+v, %d reads", callMethods(e.rec.Calls()), d, w.Reads())
	}
	states, err := e.svc.States(t.Context(), "AGAAAAAAAAAA21")
	if err != nil || len(states) != 1 || !states[0].PastEditWindow || states[0].State != "delivered" {
		t.Errorf("states %+v, %v", states, err)
	}
	// A Destination type without edit windows never shows the flag; nor does a delivery that is neither pending nor
	// delivered.
	d.state = "retired"
	if states, _ := e.svc.States(t.Context(), "AGAAAAAAAAAA21"); states[0].PastEditWindow {
		t.Errorf("retired %+v", states)
	}
}

// TestEditWindowNever is C-22.FR-9: without edit_max_age_seconds in info (null: edits never expire), or with an info
// that cannot be read, an hour-old Root message is edited as usual.
func TestEditWindowNever(t *testing.T) {
	e, d, w := publishedWindowed(t)
	w.set(0, false)
	e.business.Set(business0.Add(time.Hour))
	e.acknowledged(t, 2)
	e.round(t)
	if calls := e.rec.Calls(); callMethods(calls) != "update" || d.pastEditWindow || d.state != "delivered" ||
		*d.actualStatus != "acknowledged" || w.Reads() != 1 {
		t.Errorf("%s, %+v", callMethods(calls), d)
	}
}

// TestRepublishPastWindow is C-22.FR-9, FR-24 and AC-8 through the recorder: past the window, Acknowledge publishes a
// new Root message Quietly with the key of the next Publication, then replies under the old one, in its Thread, with
// "This message is no longer updated. The current one: {url}" and the pointer key; the new message replaces the old
// one, whose id is kept, and its window starts now; the republication is the republished_past_edit_window delivery
// event and the bridge_republished_past_edit_window line. Later Thread replies go under the new Root message, and an
// edit within its window edits it.
func TestRepublishPastWindow(t *testing.T) {
	e, d, w := publishedWindowed(t)
	now := business0.Add(11 * time.Minute)
	e.business.Set(now)
	e.rec.Script(deliverytest.MethodPublish, publishedAs("m2", "t-2"))
	e.acknowledged(t, 2)
	e.round(t)
	calls := e.rec.Calls()
	if callMethods(calls) != "publish,reply" {
		t.Fatalf("calls %s", callMethods(calls))
	}
	pub, ptr := calls[0], calls[1]
	if pub.Loudness != groups.Quiet || len(pub.Mentions) != 0 || pub.Message.Colour != "acknowledged" ||
		!strings.Contains(textOf(pub), "Acknowledged") || len(pub.Message.Buttons) != 3 {
		t.Errorf("publication %+v", pub)
	}
	if ptr.Root.MessageID != "m1" || ptr.Root.ThreadAnchorID != "t-1" || ptr.Loudness != groups.Quiet ||
		textOf(ptr) != "This message is no longer updated. The current one: https://chat.example.org/m/m2" ||
		len(ptr.Message.Buttons) != 0 {
		t.Errorf("pointer %+v", ptr)
	}
	pubKey := fmt.Sprintf("pub-%s-%d-2", bridgePublicID, d.id)
	if keys := w.Keys(); !slices.Equal(keys, []string{pubKey, fmt.Sprintf("pointer-%s-%d-1", bridgePublicID, d.id)}) {
		t.Errorf("keys %v", keys)
	}
	if d.state != "delivered" || *d.messageID != "m2" || *d.messageURL != "https://chat.example.org/m/m2" ||
		*d.anchorID != "t-2" || !slices.Equal(d.superseded, []string{"m1"}) || d.publications != 2 ||
		!d.publishedAt.Equal(now) || *d.actualStatus != "acknowledged" || d.pastEditWindow {
		t.Errorf("delivery %+v", d)
	}
	if len(e.db.events) != 1 {
		t.Fatalf("events %v", e.eventKinds())
	}
	ev := e.db.events[0]
	var detail map[string]string
	if err := json.Unmarshal(ev.Detail, &detail); err != nil || ev.Kind != "republished_past_edit_window" ||
		ev.Loudness != "quiet" || len(ev.Mentions) != 0 || ev.ErrorClass.Valid || ev.Error.Valid ||
		detail["old_message_id"] != "m1" || detail["new_message_id"] != "m2" || detail["pointer"] != "sent" ||
		ev.AlertGroupID.Int64 != groupID {
		t.Errorf("event %+v, detail %s", ev, ev.Detail)
	}
	log := e.log.String()
	for _, want := range []string{`"event":"bridge_republished_past_edit_window"`, `"destination":"DSAAAAAAAAAB11"`,
		`"group":"AGAAAAAAAAAA21"`, `"old_message_id":"m1"`, `"new_message_id":"m2"`} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %s:\n%s", want, log)
		}
	}
	out := scrape(t)
	for _, want := range []string{
		`muster_delivery_attempts_total{destination="DSAAAAAAAAAB11",kind="publication",outcome="delivered"}`,
		`muster_delivery_attempts_total{destination="DSAAAAAAAAAB11",kind="thread_reply",outcome="delivered"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	// The next Thread reply goes under the new Root message.
	e.rec.Reset()
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	e.round(t)
	if r := e.replies(); len(r) != 1 || r[0].Root.MessageID != "m2" || r[0].Root.ThreadAnchorID != "t-2" {
		t.Errorf("replies %+v", r)
	}
	// Within the new message's window, a change is an edit of it.
	e.rec.Reset()
	e.business.Set(now.Add(539 * time.Second))
	e.retitled(t, groups.StatusAcknowledged, "b", 4)
	e.round(t)
	if calls := e.rec.Calls(); callMethods(calls) != "update" || calls[0].MessageID != "m2" {
		t.Errorf("calls %s %+v", callMethods(calls), calls)
	}
}

// TestPointerFailsNotRetried is C-22.FR-9: a pointer reply that fails is recorded with the delivery event — its error
// class and its error — and never retried, and the republication stands; a url the bridge answered that is not a safe
// link points to the Alert Group page instead.
func TestPointerFailsNotRetried(t *testing.T) {
	e, d, _ := publishedWindowed(t)
	e.business.Set(business0.Add(11 * time.Minute))
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "m2", MessageURL: "javascript:alert(1)"}})
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeTransient, "HTTP 503"))
	e.acknowledged(t, 2)
	e.round(t)
	calls := e.rec.Calls()
	if callMethods(calls) != "publish,reply" ||
		textOf(calls[1]) != "This message is no longer updated. The current one: "+
			"https://muster.example.org/alert-groups/AGAAAAAAAAAA21" {
		t.Fatalf("calls %s %+v", callMethods(calls), calls)
	}
	if d.state != "delivered" || *d.messageID != "m2" || !slices.Equal(d.superseded, []string{"m1"}) ||
		d.anchorID != nil || d.errorClass != nil {
		t.Errorf("delivery %+v", d)
	}
	ev := e.db.events[0]
	if len(e.db.events) != 1 || ev.ErrorClass.String != "transient" || ev.Error.String != "HTTP 503" ||
		!strings.Contains(string(ev.Detail), `"pointer":"failed"`) {
		t.Errorf("event %+v %s", ev, ev.Detail)
	}
	e.business.Advance(10 * time.Minute)
	e.round(t)
	if len(e.rec.Calls()) != 2 || len(e.db.replies) != 0 {
		t.Errorf("retried: %s, replies %d", callMethods(e.rec.Calls()), len(e.db.replies))
	}
	// A Thread reply under the old Root message that is still in flight is no Thread reply of the new one.
	if !strings.Contains(scrape(t), `kind="thread_reply",outcome="transient"`) {
		t.Error("the failed pointer reply was not counted")
	}
}

// TestPointerLinkWithoutURL is C-22.FR-9: a new Root message without a url points to the Alert Group page; a
// pointer reply that fails without an error of its own is recorded as unknown.
func TestPointerLinkWithoutURL(t *testing.T) {
	e, _, _ := publishedWindowed(t)
	e.business.Set(business0.Add(11 * time.Minute))
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "m2"}})
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, ""))
	e.acknowledged(t, 2)
	e.round(t)
	if r := e.replies(); len(r) != 1 || !strings.HasSuffix(textOf(r[0]), "/alert-groups/AGAAAAAAAAAA21") {
		t.Errorf("replies %+v", r)
	}
	if ev := e.db.events[0]; ev.ErrorClass.String != "unknown" || ev.Error.String != "unknown response" {
		t.Errorf("event %+v", ev)
	}
}

// TestRepublicationRetry is C-22.FR-6 and FR-9: a republication that fails keeps the old Root message and is retried
// with the same key; a replayed one stays pending with the status it shows unknown, so the next call edits it.
func TestRepublicationRetry(t *testing.T) {
	e, d, w := publishedWindowed(t)
	e.business.Set(business0.Add(11 * time.Minute))
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "HTTP 502"),
		deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "m2", Replayed: true}})
	e.acknowledged(t, 2)
	e.round(t)
	if d.state != "pending" || *d.messageID != "m1" || *d.errorClass != "transient" || len(d.superseded) != 0 ||
		len(e.db.events) != 0 {
		t.Fatalf("after a failure %+v", d)
	}
	e.business.Set(d.next)
	e.round(t)
	key := fmt.Sprintf("pub-%s-%d-2", bridgePublicID, d.id)
	if keys := w.Keys(); len(keys) != 3 || keys[0] != key || keys[1] != key ||
		keys[2] != fmt.Sprintf("pointer-%s-%d-1", bridgePublicID, d.id) {
		t.Errorf("keys %v", keys)
	}
	if d.state != "pending" || *d.messageID != "m2" || d.actualStatus != nil || d.actualHash != nil ||
		d.publications != 2 {
		t.Fatalf("replayed %+v", d)
	}
	e.rec.Reset()
	e.round(t)
	if calls := e.rec.Calls(); callMethods(calls) != "update" || calls[0].MessageID != "m2" || d.state != "delivered" ||
		*d.actualStatus != "acknowledged" {
		t.Errorf("after the replay %s, %+v", callMethods(calls), d)
	}
}

// TestNullActualStatusRepublishes is C-22.FR-9: a Root message whose status is unknown — delivered before the edit
// windows, or replayed — counts as changed past its window: it is published again once, then its status is known.
func TestNullActualStatusRepublishes(t *testing.T) {
	e, d, _ := publishedWindowed(t)
	d.actualStatus = nil
	e.business.Set(business0.Add(11 * time.Minute))
	e.retitled(t, groups.StatusFiring, "b", 2)
	e.round(t)
	if calls := e.rec.Calls(); callMethods(calls) != "publish,reply" || *d.actualStatus != "firing" {
		t.Fatalf("calls %s, %+v", callMethods(calls), d)
	}
	e.rec.Reset()
	e.business.Set(business0.Add(22 * time.Minute))
	e.retitled(t, groups.StatusFiring, "c", 3)
	e.round(t)
	if len(e.rec.Calls()) != 0 || !d.pastEditWindow || d.state != "delivered" {
		t.Errorf("again %s, %+v", callMethods(e.rec.Calls()), d)
	}
}

// TestEditWindowClosedOutcome is C-22.FR-8, FR-9 and AC-9 through the recorder: an edit refused as edit_window_closed
// inside the declared window is not the deleted Root message flow: the Root message is marked past its window and the
// delivery is due at once, with no error and no attempt; the edit's Desired state, with the status the message shows,
// then makes no call, and the next change of status republishes.
func TestEditWindowClosedOutcome(t *testing.T) {
	e, d, _ := publishedWindowed(t)
	e.business.Set(business0.Add(time.Minute))
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeEditWindowClosed,
		"The edit window of this message has closed."))
	e.retitled(t, groups.StatusFiring, "b", 2)
	e.round(t)
	if d.republished || d.messageID == nil || *d.messageID != "m1" || !d.pastEditWindow || d.state != "pending" ||
		!d.next.Equal(e.business.Now()) || d.errorClass != nil || d.lastError != nil || d.attempts != 0 ||
		len(e.db.events) != 0 || d.owner != "" {
		t.Fatalf("after edit_window_closed %+v, events %v", d, e.eventKinds())
	}
	e.round(t)
	if callMethods(e.rec.Calls()) != "update" || d.state != "delivered" {
		t.Fatalf("the same status %s, %+v", callMethods(e.rec.Calls()), d)
	}
	e.rec.Script(deliverytest.MethodPublish, publishedAs("m2", "t-2"))
	e.acknowledged(t, 3)
	e.round(t)
	if callMethods(e.rec.Calls()) != "update,publish,reply" || *d.messageID != "m2" || d.pastEditWindow {
		t.Errorf("the change of status %s, %+v", callMethods(e.rec.Calls()), d)
	}
	if strings.Contains(scrape(t), `outcome="edit_window_closed"`) {
		t.Error("edit_window_closed counted as a delivery outcome")
	}
}

// TestFinalEditPastWindowIsReply is C-22.FR-9 and C-11.FR-14: the final edit of a Root message past its window — its
// Destination left the Route — is a Quiet Thread reply with the same text, the key final-<Destination
// public_id>-<delivery id>, and no edit; the delivery is retired with the final_edit delivery event.
func TestFinalEditPastWindowIsReply(t *testing.T) {
	e, d, w := publishedWindowed(t)
	e.business.Set(business0.Add(11 * time.Minute))
	e.db.routeDests[routeID] = nil
	e.changed(t, routeID, nil, []int64{destMM})
	e.round(t)
	calls := e.rec.Calls()
	if callMethods(calls) != "reply" || calls[0].Root.MessageID != "m1" || calls[0].Root.ThreadAnchorID != "t-1" ||
		calls[0].Loudness != groups.Quiet || !strings.Contains(textOf(calls[0]), finalText("AGAAAAAAAAAA21")) ||
		!strings.Contains(textOf(calls[0]), "#7 a") || len(calls[0].Message.Buttons) != 0 {
		t.Fatalf("calls %s %+v", callMethods(calls), calls)
	}
	if keys := w.Keys(); !slices.Equal(keys, []string{fmt.Sprintf("final-%s-%d", bridgePublicID, d.id)}) {
		t.Errorf("keys %v", keys)
	}
	if d.state != "retired" || !slices.Equal(e.eventKinds(), []string{"final_edit"}) {
		t.Errorf("delivery %+v, events %v", d, e.eventKinds())
	}
	if !strings.Contains(scrape(t), `muster_delivery_attempts_total{destination="DSAAAAAAAAAB11",kind="final_edit",`+
		`outcome="delivered"}`) {
		t.Error("the final reply was not counted as the final edit")
	}
	// A final reply whose Root message is gone retires the delivery all the same.
	e, d, _ = publishedWindowed(t)
	e.business.Set(business0.Add(11 * time.Minute))
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "no such message"))
	e.db.routeDests[routeID] = nil
	e.changed(t, routeID, nil, []int64{destMM})
	e.round(t)
	if d.state != "retired" || len(e.db.events) != 0 || d.republished {
		t.Errorf("gone %+v, events %v", d, e.eventKinds())
	}
}

// TestStormSummaryNeverRepublished is C-22.FR-9: a Storm summary past its window takes no edit and is never published
// again, whatever the status its message shows.
func TestStormSummaryNeverRepublished(t *testing.T) {
	e := stormEnv(t)
	ds := e.db.dests[destMM]
	ds.typ, ds.publicID = delivery.TypeBridge, bridgePublicID
	w := windowed(e.rec)
	e.w.Adapters[delivery.TypeBridge] = w
	r := e.db.routes[routeID]
	r.threshold = 1
	e.db.routes[routeID] = r
	e.addGroup(t, 5001, false)
	e.round(t)
	e.addGroup(t, 5002, false)
	e.round(t)
	var summary *fakeDelivery
	for _, d := range e.db.deliveries {
		if d.storm != 0 {
			summary = d
		}
	}
	if summary == nil || summary.state != "delivered" || *summary.actualStatus != "storm" {
		t.Fatalf("no storm summary published %+v", summary)
	}
	summary.actualStatus = nil
	e.rec.Reset()
	e.business.Advance(11 * time.Minute)
	e.addGroup(t, 5003, false)
	e.round(t)
	if len(e.rec.Calls()) != 0 || summary.state != "delivered" || !summary.pastEditWindow {
		t.Errorf("calls %s, summary %+v", callMethods(e.rec.Calls()), summary)
	}
}

// TestMattermostUnaffected is C-22.FR-9: a messenger without edit windows edits an hour-old Root message, and only
// records the status it shows.
func TestMattermostUnaffected(t *testing.T) {
	e := published(t)
	d := e.only(t)
	e.business.Set(business0.Add(time.Hour))
	e.acknowledged(t, 2)
	e.round(t)
	if calls := e.rec.Calls(); callMethods(calls) != "update" || *d.actualStatus != "acknowledged" || d.pastEditWindow {
		t.Errorf("%s, %+v", callMethods(calls), d)
	}
}

// TestDeletedRootResetsWindow is C-22.FR-9 and C-11.FR-13: a Root message past its window that is found deleted is
// published again with a window of its own, which a change of status within it edits.
func TestDeletedRootResetsWindow(t *testing.T) {
	e, d, _ := publishedWindowed(t)
	e.business.Set(business0.Add(11 * time.Minute))
	e.retitled(t, groups.StatusFiring, "b", 2)
	e.round(t)
	if !d.pastEditWindow {
		t.Fatalf("not past its window %+v", d)
	}
	e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "no such message"))
	e.enqueue(t, e.group(groups.StatusFiring, "b"), groups.System, groups.Recorded{Seq: 3,
		Event: groups.EventTakeover, Loudness: groups.Loud})
	e.round(t)
	if !d.republished || d.pastEditWindow || d.actualStatus != nil || d.messageID != nil {
		t.Fatalf("after gone %+v", d)
	}
	e.round(t)
	if d.messageID == nil || d.state != "delivered" || !d.publishedAt.Equal(e.business.Now()) {
		t.Fatalf("not published again %+v", d)
	}
	e.rec.Reset()
	e.business.Advance(time.Minute)
	e.acknowledged(t, 4)
	e.round(t)
	if calls := e.rec.Calls(); callMethods(calls) != "update" || d.pastEditWindow {
		t.Errorf("within the new window %s, %+v", callMethods(calls), d)
	}
}

// TestEditWindowRecordFailures: a failed record of a republication or of a closed window is logged, and the row is
// attempted again once its lease runs out; a record after the lease went to another replica changes nothing.
func TestEditWindowRecordFailures(t *testing.T) {
	e, d, _ := publishedWindowed(t)
	e.business.Set(business0.Add(11 * time.Minute))
	e.db.fail["RecordRepublished"] = errBoom
	e.acknowledged(t, 2)
	e.round(t)
	if !strings.Contains(e.log.String(), `"error":"record the republication past the edit window: boom"`) ||
		*d.messageID != "m1" {
		t.Errorf("log %s, %+v", e.log, d)
	}
	delete(e.db.fail, "RecordRepublished")
	e.db.before["RecordRepublished"] = func() { d.owner = "r2" }
	e.real.Advance(2 * delivery.Lease)
	e.round(t)
	if *d.messageID != "m1" || len(e.db.events) != 0 {
		t.Errorf("recorded without the lease %+v", d)
	}

	e, d, _ = publishedWindowed(t)
	e.business.Set(business0.Add(time.Minute))
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeEditWindowClosed, ""),
		deliverytest.Failure(delivery.OutcomeEditWindowClosed, ""))
	e.db.fail["RecordEditWindowClosed"] = errBoom
	e.retitled(t, groups.StatusFiring, "b", 2)
	e.round(t)
	if !strings.Contains(e.log.String(), `"error":"record the closed edit window: boom"`) || d.pastEditWindow {
		t.Errorf("log %s, %+v", e.log, d)
	}
	delete(e.db.fail, "RecordEditWindowClosed")
	e.db.before["RecordEditWindowClosed"] = func() { d.owner = "r2" }
	e.real.Advance(2 * delivery.Lease)
	e.round(t)
	if d.pastEditWindow {
		t.Errorf("recorded without the lease %+v", d)
	}
	// A failed mark of a Root message past its window is logged too.
	e, _, _ = publishedWindowed(t)
	e.business.Set(business0.Add(11 * time.Minute))
	e.db.fail["MarkDelivered"] = errBoom
	e.retitled(t, groups.StatusFiring, "b", 2)
	e.round(t)
	if !strings.Contains(e.log.String(), `"error":"mark the delivery delivered past its edit window: boom"`) {
		t.Errorf("log %s", e.log)
	}
}

// TestRepublicationAsProbe is C-11.FR-9 with C-22.FR-9: the republication past the edit window of the oldest waiting
// delivery is the probe of a Broken Bridge Destination, which it ends; an Alert Group whose first Publication is still
// unmarked is marked once the republication committed.
func TestRepublicationAsProbe(t *testing.T) {
	e, _, w := publishedWindowed(t)
	marked := 0
	e.w.Published = func(context.Context, int64, time.Time) error { marked++; return nil }
	e.business.Set(business0.Add(11 * time.Minute))
	e.breakDest(destMM, "503")
	e.rec.Script(deliverytest.MethodPublish, publishedAs("m2", "t-2"))
	e.acknowledged(t, 2)
	e.round(t)
	if len(e.rec.Calls()) != 0 {
		t.Fatalf("called while broken: %s", callMethods(e.rec.Calls()))
	}
	e.business.Set(*e.db.dests[destMM].nextProbe)
	e.round(t)
	if callMethods(e.rec.Calls()) != "publish,reply" || e.db.dests[destMM].health != "healthy" || marked != 1 ||
		w.Reads() != 1 {
		t.Errorf("probe %s, health %s, marked %d", callMethods(e.rec.Calls()), e.db.dests[destMM].health, marked)
	}
	if kinds := e.eventKinds(); !slices.Equal(kinds, []string{"republished_past_edit_window", "destination_recovered"}) {
		t.Errorf("events %v", kinds)
	}
}
