// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
)

// pending is an env whose Alert Group waits for its first Publication, with a limiter that never runs dry and the
// jitter of delivery.transient_backoff fixed at half the bound.
func pending(t *testing.T) (*env, *fakeDelivery) {
	t.Helper()
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	e.w.Random = func() float64 { return 0.5 }
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	return e, e.only(t)
}

// eventKinds are the kinds of the recorded delivery events, in order.
func (e *env) eventKinds() []string {
	var out []string
	for _, ev := range e.db.events {
		out = append(out, ev.Kind)
	}
	return out
}

// TestOutcomeRules is C-11.FR-8 and FR-10, one row of the outcome table each, on a first Publication with a manual
// clock: what the row becomes, when it is due, its error and the health of the Destination.
func TestOutcomeRules(t *testing.T) {
	type want struct {
		state, class, err, health string
		next                      time.Duration
		attempts                  int64
		events                    []string
	}
	for _, tc := range []struct {
		name   string
		answer deliverytest.Answer
		want   want
	}{
		{"ok", deliverytest.OK(), want{state: "delivered", health: "healthy", events: []string{"publication"}}},
		{"retry_after", deliverytest.RetryAfter(7*time.Second, delivery.ScopeDestination),
			want{state: "pending", class: "retry_after", err: "Too Many Requests", health: "healthy",
				next: 7*time.Second + delivery.TokenMargin}},
		{"transient", deliverytest.Failure(delivery.OutcomeTransient, "HTTP 503"),
			want{state: "pending", class: "transient", err: "HTTP 503", health: "healthy",
				next: delivery.TransientFirstStep, attempts: 1}},
		{"fatal", deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"),
			want{state: "pending", class: "fatal", err: "HTTP 403", health: "broken",
				events: []string{"destination_broken"}}},
		{"unknown", deliverytest.Failure(delivery.OutcomeUnknown, "HTTP 418"),
			want{state: "not_delivered", class: "unknown", err: "HTTP 418", health: "healthy",
				events: []string{"not_delivered"}}},
		{"unknown without text", deliverytest.Failure(delivery.OutcomeUnknown, ""),
			want{state: "not_delivered", class: "unknown", err: "unknown response", health: "healthy",
				events: []string{"not_delivered"}}},
		{"markup_rejected", deliverytest.Failure(delivery.OutcomeMarkupRejected, "bad entity"),
			want{state: "delivered", health: "healthy", events: []string{"publication", "markup_rejected"}}},
		{"gone on a publication", deliverytest.Failure(delivery.OutcomeGone, "no such channel"),
			want{state: "not_delivered", class: "unknown", err: "no such channel", health: "healthy",
				events: []string{"not_delivered"}}},
		{"thread_lost", deliverytest.Failure(delivery.OutcomeThreadLost, "thread closed"),
			want{state: "pending", err: "thread closed", health: "healthy", next: delivery.TransientFirstStep}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, d := pending(t)
			e.rec.Script(deliverytest.MethodPublish, tc.answer)
			e.round(t)
			w := tc.want
			if d.state != w.state || (w.class == "") != (d.errorClass == nil) ||
				(d.errorClass != nil && *d.errorClass != w.class) || (w.err == "") != (d.lastError == nil) ||
				(d.lastError != nil && *d.lastError != w.err) || e.db.dests[destMM].health != w.health ||
				d.attempts != 0 && d.attempts != w.attempts {
				t.Errorf("delivery %+v, health %s", d, e.db.dests[destMM].health)
			}
			if d.state == "pending" && !d.next.Equal(business0.Add(w.next)) {
				t.Errorf("due at %v, want %v", d.next, business0.Add(w.next))
			}
			if !slices.Equal(e.eventKinds(), w.events) {
				t.Errorf("events %v, want %v", e.eventKinds(), w.events)
			}
			if d.state != "delivered" && d.started != nil {
				t.Error("the start of a publication the messenger did not take is kept")
			}
		})
	}
}

// TestTransientBackoffAndReset: Transient attempts wait delivery.transient_backoff, exponential with jitter up to its
// longest wait, and a successful call resets the budget.
func TestTransientBackoffAndReset(t *testing.T) {
	e, d := pending(t)
	// Half the step plus half of the other half, at least the first step: 2 s, 3 s, 6 s and 12 s.
	waits := []time.Duration{2 * time.Second, 3 * time.Second, 6 * time.Second, 12 * time.Second}
	for i, want := range waits {
		e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "HTTP 502"))
		e.round(t)
		if d.attempts != int64(i+1) || !d.next.Equal(e.business.Now().Add(want)) || !d.firstFailed.Equal(business0) {
			t.Fatalf("attempt %d: %+v, want a wait of %v", i+1, d, want)
		}
		e.business.Set(d.next)
	}
	e.round(t)
	if d.state != "delivered" || d.attempts != 0 || d.firstFailed != nil || d.errorClass != nil {
		t.Errorf("after the success %+v", d)
	}
	if !strings.Contains(scrape(t), `kind="publication",outcome="transient"`) {
		t.Error("transient not counted")
	}
}

// TestTransientBackoffEqualJitter is delivery.transient_backoff: the step doubles from its first step, 2 s, up to its
// longest wait, 5 min; each wait lies between half the step and the step (equal jitter) and is never shorter than the
// first step, whatever the jitter.
func TestTransientBackoffEqualJitter(t *testing.T) {
	for _, r := range []float64{0, 0.25, 0.5, 0.999999} {
		w := &delivery.Worker{Random: func() float64 { return r }}
		for n := int64(1); n <= 12; n++ {
			step := min(delivery.TransientFirstStep<<(n-1), delivery.TransientBackoffMax)
			got := w.Backoff(n)
			if got < delivery.TransientFirstStep || got < step/2 || got > step || got > delivery.TransientBackoffMax {
				t.Errorf("random %v, attempt %d: wait %v outside [max(%v, %v), %v]", r, n, got,
					delivery.TransientFirstStep, step/2, step)
			}
		}
	}
	w := &delivery.Worker{Random: func() float64 { return 0 }}
	if got := w.Backoff(1); got != delivery.TransientFirstStep {
		t.Errorf("first wait without jitter %v, want %v", got, delivery.TransientFirstStep)
	}
	if got := w.Backoff(20); got != delivery.TransientBackoffMax/2 {
		t.Errorf("longest step without jitter %v", got)
	}
	if got := (&delivery.Worker{}).Backoff(3); got < 4*time.Second || got > 8*time.Second {
		t.Errorf("the default jitter %v", got)
	}
}

// TestUnknownIsNotDelivered is C-11.FR-10, AC-8 and AC-13 through the recorder: an unknown response ends the delivery
// as Not delivered with the error, adds exactly one not_delivered delivery event of the Alert Group, leaves the
// Destination healthy and is logged; a later change of the Desired state starts a new delivery. A type that no
// adapter serves is Not delivered too (D277).
func TestUnknownIsNotDelivered(t *testing.T) {
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeUnknown, "HTTP 418: teapot"))
	e.round(t)
	if d.state != "not_delivered" || *d.lastError != "HTTP 418: teapot" || len(e.db.events) != 1 {
		t.Fatalf("delivery %+v, events %v", d, e.eventKinds())
	}
	ev := e.db.events[0]
	if ev.Kind != "not_delivered" || ev.AlertGroupID.Int64 != groupID || ev.ErrorClass.String != "unknown" ||
		ev.Error.String != "HTTP 418: teapot" || ev.Loudness != "quiet" || e.db.dests[destMM].health != "healthy" {
		t.Errorf("event %+v", ev)
	}
	if !strings.Contains(e.log.String(), `"event":"delivery_not_delivered"`) ||
		!strings.Contains(e.log.String(), `"group":"AGAAAAAAAAAA21"`) ||
		!strings.Contains(e.log.String(), `"kind":"publication"`) {
		t.Errorf("log %s", e.log)
	}
	states, err := e.svc.States(t.Context(), "AGAAAAAAAAAA21")
	if err != nil || states[0].State != "not_delivered" || *states[0].Error != "HTTP 418: teapot" {
		t.Errorf("states %+v %v", states, err)
	}
	// Nothing more until the Desired state changes; then a new delivery.
	e.round(t)
	if len(e.rec.Calls()) != 1 {
		t.Errorf("a not delivered row was called again")
	}
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.round(t)
	if d.state != "delivered" || e.rec.Count(deliverytest.MethodPublish) != 2 || d.lastError != nil {
		t.Errorf("after the change %+v", d)
	}
	// D277: no adapter for the type.
	e2, d2 := pending(t)
	delete(e2.w.Adapters, delivery.TypeMattermost)
	e2.round(t)
	if d2.state != "not_delivered" || *d2.lastError != "no adapter for the destination type mattermost" ||
		e2.db.dests[destMM].health != "healthy" {
		t.Errorf("no adapter %+v", d2)
	}
}

// TestFatalIsBroken is C-11.FR-8 and FR-9: a Fatal error makes the Destination Broken at once with the masked error
// as its reason; the delivery waits and is not claimed.
func TestFatalIsBroken(t *testing.T) {
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403: not a member"))
	e.round(t)
	ds := e.db.dests[destMM]
	if ds.health != "broken" || *ds.brokenCause != "fatal" || *ds.brokenReason != "HTTP 403: not a member" ||
		!ds.brokenSince.Equal(business0) || !ds.nextProbe.Equal(business0.Add(delivery.BrokenProbeInterval)) {
		t.Fatalf("destination %+v", ds)
	}
	if d.state != "pending" || d.attempts != 0 {
		t.Errorf("delivery %+v", d)
	}
	if ev := e.db.events[0]; ev.Kind != "destination_broken" || ev.AlertGroupID.Valid || ev.ErrorClass.String !=
		"fatal" || string(ev.Detail) != `{"cause":"fatal"}` {
		t.Errorf("event %+v", ev)
	}
	e.business.Advance(time.Minute)
	e.round(t)
	if len(e.rec.Calls()) != 1 {
		t.Errorf("a delivery of a broken destination was claimed")
	}
	// No text: a fixed reason.
	e2, _ := pending(t)
	e2.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, ""))
	e2.round(t)
	if *e2.db.dests[destMM].brokenReason != "fatal error" {
		t.Errorf("reason %q", *e2.db.dests[destMM].brokenReason)
	}
}

// TestMarkupRejected is C-11.FR-8: a rejected markup is sent again without markup in the same attempt, counted as
// markup_rejected and recorded as a delivery event; a second rejection is an unknown response.
func TestMarkupRejected(t *testing.T) {
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeMarkupRejected, "can't parse"))
	e.round(t)
	calls := e.rec.Calls()
	if len(calls) != 2 || calls[0].Plain || !calls[1].Plain || textOf(calls[1]) != textOf(calls[0]) ||
		calls[1].Loudness != groups.Loud || d.state != "delivered" {
		t.Fatalf("calls %+v, delivery %+v", calls, d)
	}
	if !slices.Equal(e.eventKinds(), []string{"publication", "markup_rejected"}) ||
		e.db.events[1].Error.String != "can't parse" {
		t.Errorf("events %+v", e.db.events)
	}
	if !strings.Contains(scrape(t), `kind="publication",outcome="markup_rejected"`) {
		t.Error("markup_rejected not counted")
	}
	// An edit, rejected twice.
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeMarkupRejected, "bad"),
		deliverytest.Failure(delivery.OutcomeMarkupRejected, ""))
	e.round(t)
	if d.state != "not_delivered" || *d.lastError != "the messenger rejected the text without markup" ||
		!slices.Equal(e.eventKinds(), []string{"publication", "markup_rejected", "not_delivered", "markup_rejected"}) {
		t.Errorf("rejected twice %+v, %v", d, e.eventKinds())
	}
	if !strings.Contains(e.log.String(), `"kind":"update"`) {
		t.Errorf("log %s", e.log)
	}
}

// TestReplyOutcomeRules is the outcome table for Thread replies: the same rules, the same budgets.
func TestReplyOutcomeRules(t *testing.T) {
	reply := func(t *testing.T) (*env, *fakeReply) {
		t.Helper()
		e := published(t)
		e.w.Random = func() float64 { return 0.5 }
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "fp1"))
		return e, e.db.replies[0]
	}
	t.Run("transient", func(t *testing.T) {
		e, r := reply(t)
		for i := range delivery.TransientBudgetAttempts {
			e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeTransient, "HTTP 503"))
			e.round(t)
			if i < delivery.TransientBudgetAttempts-1 && (r.attempts != int64(i+1) || r.state != "pending") {
				t.Fatalf("attempt %d %+v", i+1, r)
			}
			e.business.Set(r.next)
		}
		ds := e.db.dests[destMM]
		if ds.health != "broken" || *ds.brokenReason != "unavailable after repeated failures: HTTP 503" ||
			r.state != "pending" || r.attempts != delivery.TransientBudgetAttempts {
			t.Errorf("after the budget %+v %+v", ds, r)
		}
	})
	t.Run("fatal", func(t *testing.T) {
		e, r := reply(t)
		e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeFatal, "kicked"))
		e.round(t)
		if e.db.dests[destMM].health != "broken" || *e.db.dests[destMM].brokenCause != "fatal" || r.state != "pending" {
			t.Errorf("fatal %+v", r)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		e, r := reply(t)
		e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeUnknown, "odd"))
		e.round(t)
		if r.state != "not_delivered" || *r.lastError != "odd" || e.db.events[len(e.db.events)-1].Kind !=
			"not_delivered" || string(e.db.events[len(e.db.events)-1].Detail) !=
			`{"event":"alerts_added","kind":"thread_reply"}` || !strings.Contains(e.log.String(),
			`"kind":"thread_reply"`) {
			t.Errorf("unknown %+v, %v", r, e.eventKinds())
		}
	})
	t.Run("no adapter", func(t *testing.T) {
		e, r := reply(t)
		delete(e.w.Adapters, delivery.TypeMattermost)
		e.round(t)
		if r.state != "not_delivered" || !strings.Contains(*r.lastError, "no adapter") {
			t.Errorf("no adapter %+v", r)
		}
	})
	t.Run("markup_rejected", func(t *testing.T) {
		e, r := reply(t)
		e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeMarkupRejected, "bad"))
		e.round(t)
		if rs := e.replies(); len(rs) != 2 || !rs[1].Plain || r.state != "sent" ||
			e.db.events[len(e.db.events)-1].Kind != "markup_rejected" {
			t.Errorf("markup %+v %+v", rs, r)
		}
	})
	t.Run("retry_after", func(t *testing.T) {
		e, r := reply(t)
		e.rec.Script(deliverytest.MethodReply, deliverytest.RetryAfter(3*time.Second, delivery.ScopeConnection))
		e.round(t)
		if r.state != "pending" || !r.next.Equal(business0.Add(3*time.Second+delivery.TokenMargin)) || r.attempts != 0 {
			t.Errorf("retry after %+v", r)
		}
	})
	t.Run("thread_lost", func(t *testing.T) {
		e, r := reply(t)
		e.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeThreadLost, "lost"))
		e.round(t)
		if r.state != "pending" || !r.next.Equal(business0.Add(delivery.TransientFirstStep)) || r.errorClass != nil {
			t.Errorf("thread lost %+v", r)
		}
	})
}

// TestOutcomesLeaseLost: an outcome recorded after the lease went to another replica changes nothing.
func TestOutcomesLeaseLost(t *testing.T) {
	for _, kind := range []delivery.OutcomeKind{delivery.OutcomeRetryAfter, delivery.OutcomeTransient,
		delivery.OutcomeFatal, delivery.OutcomeUnknown} {
		t.Run(string(kind), func(t *testing.T) {
			e, d := pending(t)
			a := deliverytest.Failure(kind, "x")
			a.Then = func() { d.owner = "r2" }
			e.rec.Script(deliverytest.MethodPublish, a)
			e.round(t)
			if d.state != "pending" || d.errorClass != nil || len(e.db.events) != 0 ||
				e.db.dests[destMM].health != "healthy" {
				t.Errorf("recorded without the lease %+v", d)
			}
		})
		t.Run("reply "+string(kind), func(t *testing.T) {
			e := published(t)
			e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "fp"))
			r := e.db.replies[0]
			a := deliverytest.Failure(kind, "x")
			a.Then = func() { r.owner = "r2" }
			e.rec.Script(deliverytest.MethodReply, a)
			e.round(t)
			if r.state != "pending" || r.errorClass != nil || len(e.db.events) != 1 {
				t.Errorf("recorded without the lease %+v", r)
			}
		})
	}
}

// TestOutcomeQueryFailures: a failed query of an outcome rule is logged as delivery_work_failed and the row waits for
// its lease.
func TestOutcomeQueryFailures(t *testing.T) {
	for _, tc := range []struct {
		query  string
		method string
		answer deliverytest.Answer
		reply  bool
	}{
		{"RecordNotDelivered", deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeUnknown, "x"), false},
		{"InsertDeliveryEvent", deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeUnknown, "x"), false},
		{"BreakDestination", deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "x"), false},
		{"RecordDeliveryRetry", deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "x"), false},
		{"FindBuiltinIntegration", deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "x"), false},
		{"Notify", deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "x"), false},
		{"InsertDeliveryEvent", deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "x"), false},
		{"InsertDeliveryEvent", deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeMarkupRejected, "x"),
			false},
		{"RecordReplyNotDelivered", deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeUnknown, "x"),
			true},
		{"InsertDeliveryEvent", deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeUnknown, "x"), true},
		{"RecordReplyRetry", deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeFatal, "x"), true},
		{"RecordReplyRetry", deliverytest.MethodReply, deliverytest.RetryAfter(time.Second,
			delivery.ScopeDestination), true},
		{"RecordReplyRetry", deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeTransient, "x"), true},
		{"HoldBucket", deliverytest.MethodReply, deliverytest.RetryAfter(time.Second, delivery.ScopeDestination),
			true},
		{"RecordReplySent", deliverytest.MethodReply, deliverytest.OK(), true},
		{"RescheduleReply", deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "x"), true},
		{"InsertDeliveryEvent", deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeMarkupRejected, "x"),
			true},
	} {
		t.Run(tc.query+"/"+string(tc.answer.Outcome.Kind), func(t *testing.T) {
			e, _ := pending(t)
			if tc.reply {
				e = published(t)
				e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring,
					"fp"))
			}
			if tc.answer.Outcome.Kind == delivery.OutcomeMarkupRejected {
				// The event of the rejection fails after the plain call was recorded.
				e.rec.Script(tc.method, tc.answer)
				e.db.before["RecordDelivered"] = func() { e.db.fail[tc.query] = errBoom }
				e.db.before["RecordReplySent"] = func() { e.db.fail[tc.query] = errBoom }
			} else {
				e.rec.Script(tc.method, tc.answer)
				e.db.fail[tc.query] = errBoom
			}
			e.round(t)
			if !strings.Contains(e.log.String(), `"event":"delivery_work_failed"`) {
				t.Errorf("log %s", e.log)
			}
		})
	}
}

// TestRecordAfterTheRowEnded: an outcome recorded after its row ended while the call was in flight never revives it,
// and the secrets of a Destination deleted meanwhile are wiped once nothing of it is pending (C-11.FR-14, FR-16).
func TestRecordAfterTheRowEnded(t *testing.T) {
	t.Run("probe publication resolved meanwhile", func(t *testing.T) {
		// The probe's Publication is in flight when its Alert Group resolves: the row is withheld but keeps its lease,
		// so the success is recorded — the message exists and is kept current — and the Destination recovers.
		e, d := pending(t)
		e.breakDest(destMM, "503")
		e.business.Advance(delivery.BrokenProbeInterval)
		e.db.before["RecordDelivered"] = func() { e.resolve(t) }
		e.round(t)
		// The same round edits it to the resolved state once the Destination is healthy.
		if d.messageID == nil || d.publications != 1 || e.db.dests[destMM].health != "healthy" ||
			callMethods(e.rec.Calls()) != "publish,update" || d.state != "delivered" {
			t.Errorf("after the probe %+v, %s, calls %s", d, e.db.dests[destMM].health, callMethods(e.rec.Calls()))
		}
	})
	t.Run("edit of a row retired meanwhile", func(t *testing.T) {
		// The edit of a resolved Alert Group is in flight when its Destination is deleted: the row is retired without a
		// call and stays retired.
		e := memberEnv(t)
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
		e.round(t)
		e.resolve(t)
		d := e.only(t)
		mm := e.db.dests[destMM]
		e.db.before["RecordDelivered"] = func() {
			mm.deleted = true
			if err := e.svc.RetireDestination(t.Context(), nil, destMM); err != nil {
				t.Fatal(err)
			}
		}
		e.round(t)
		e.round(t)
		if d.state != "retired" || callMethods(e.rec.Calls()) != "publish,update" {
			t.Errorf("revived %+v, calls %s", d, callMethods(e.rec.Calls()))
		}
	})
	t.Run("publication raced the deletion", func(t *testing.T) {
		// A late Publication is in flight when its Destination is deleted: the row was withheld, but the message now
		// exists, so it gets its final edit. Nothing of the Destination was pending at its deletion, so its secrets were
		// wiped at once.
		e := memberEnv(t)
		e.w.Random = func() float64 { return 0.5 }
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
		d := e.only(t)
		e.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(time.Second, delivery.ScopeDestination))
		e.round(t)
		e.resolve(t)
		mm := e.db.dests[destMM]
		mm.secrets = 2
		e.db.before["RecordDelivered"] = func() {
			mm.deleted = true
			if err := e.svc.RetireDestination(t.Context(), nil, destMM); err != nil {
				t.Fatal(err)
			}
		}
		e.waitPublished(t, d)
		if d.state != "pending" || !d.desiredRetire || d.messageID == nil || mm.secrets != 0 {
			t.Fatalf("after the raced publication %+v, secrets %d", d, mm.secrets)
		}
		e.round(t)
		last := e.rec.Calls()[len(e.rec.Calls())-1]
		if d.state != "retired" || last.Method != deliverytest.MethodUpdate ||
			!strings.Contains(textOf(last), "No longer updated here") || mm.secrets != 0 {
			t.Errorf("final edit %+v, secrets %d, last %+v", d, mm.secrets, last)
		}
	})
	t.Run("unknown after the deletion", func(t *testing.T) {
		// A Publication is in flight when its Destination is deleted, which makes it the final edit; the call ends
		// unknown: the row is Not delivered, no final edit waits, and the secrets are wiped.
		e := memberEnv(t)
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
		d := e.only(t)
		mm := e.db.dests[destMM]
		mm.secrets, mm.named = 1, 1
		e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{
			Outcome: delivery.Outcome{Kind: delivery.OutcomeUnknown, Error: "odd"}, Then: func() {
				mm.deleted = true
				if err := e.svc.RetireDestination(t.Context(), nil, destMM); err != nil {
					t.Error(err)
				}
				if !d.desiredRetire {
					t.Error("the deletion did not make it the final edit")
				}
			}})
		e.round(t)
		if d.state != "not_delivered" || d.desiredRetire || mm.secrets != 0 || mm.named != 0 {
			t.Errorf("after the unknown answer %+v, secrets %d/%d", d, mm.secrets, mm.named)
		}
	})
	for _, tc := range []struct {
		state  string
		answer delivery.OutcomeKind
	}{
		{"withheld", delivery.OutcomeUnknown},
		{"deleted_in_messenger", delivery.OutcomeUnknown},
		{"withheld", delivery.OutcomeOK},
		{"deleted_in_messenger", delivery.OutcomeOK},
	} {
		t.Run("final edit of a row "+tc.state+" meanwhile, "+string(tc.answer), func(t *testing.T) {
			e := memberEnv(t)
			e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
			e.round(t)
			d := e.only(t)
			e.db.routeDests[routeID] = nil
			if err := e.svc.RouteDestinationsChanged(t.Context(), nil, routeID, nil, []int64{destMM}); err != nil {
				t.Fatal(err)
			}
			e.rec.Script(deliverytest.MethodUpdate, deliverytest.Answer{
				Outcome: delivery.Outcome{Kind: tc.answer, Error: "odd"}, Then: func() { d.state = tc.state }})
			e.round(t)
			if d.state != tc.state || d.desiredRetire {
				t.Errorf("revived %+v", d)
			}
		})
	}
}

// TestThreadLost is C-14.AC-16, C-11.FR-21: a reply to a deleted copy, refused with "message to be replied not
// found", is sent again at once without its reply link; the Thread is unattached with a thread_not_attached delivery
// event carrying the refusal, the Destination stays healthy, the lost copy is never attached again and later replies
// follow the chain.
func TestThreadLost(t *testing.T) {
	e := telegramEnv(t)
	e.learn(t, tgCopy, delivery.LearnedFromAutomaticForward)
	d := e.publish(t)
	e.rec.Script(deliverytest.MethodReply,
		deliverytest.Failure(delivery.OutcomeThreadLost, "Bad Request: message to be replied not found"),
		deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "701"}})
	e.newAlerts(t, 2, "fp2")
	e.round(t)
	r := e.replies()
	if len(r) != 2 || r[0].Root.ThreadAnchorID != "9001" || r[1].Root != (delivery.Root{MessageID: tgPost}) {
		t.Fatalf("replies %+v", r)
	}
	if d.threadState != "unattached" || str(d.anchorID) != "9001" || str(d.chainLastID) != "701" ||
		e.threadEvents() != 1 || e.db.dests[destTG].health != "healthy" || e.db.replies[0].state != "sent" {
		t.Fatalf("thread %s %s %s events %d health %s", d.threadState, str(d.anchorID), str(d.chainLastID),
			e.threadEvents(), e.db.dests[destTG].health)
	}
	if ev := e.db.events[len(e.db.events)-1]; !strings.Contains(ev.Error.String, "message to be replied not found") {
		t.Errorf("event %+v", ev)
	}
	if !strings.Contains(e.log.String(), `"outcome":"thread_lost"`) {
		t.Errorf("no delivery_attempt of the lost call:\n%s", e.log)
	}
	e.learn(t, tgCopy, delivery.LearnedFromComment)
	if d.threadState != "unattached" {
		t.Fatalf("the lost copy was attached again")
	}
	e.business.Set(business0.Add(2 * time.Minute))
	e.newAlerts(t, 3, "fp3")
	e.round(t)
	if r = e.replies(); len(r) != 3 || r[2].Root != (delivery.Root{MessageID: tgPost, ChainLastID: "701"}) ||
		e.threadEvents() != 1 {
		t.Fatalf("later reply %+v", r)
	}
}

// TestThreadLostResendFails: when the resend without the link fails as well, the Thread is unattached with its chain
// starting over and the reply is retried as the first link; a reply refused as lost without any link is retried after
// a short wait.
func TestThreadLostResendFails(t *testing.T) {
	e := telegramEnv(t)
	e.learn(t, tgCopy, delivery.LearnedFromAutomaticForward)
	d := e.publish(t)
	e.rec.Script(deliverytest.MethodReply,
		deliverytest.Failure(delivery.OutcomeThreadLost, "message to be replied not found"),
		deliverytest.Failure(delivery.OutcomeTransient, "timeout"),
		deliverytest.Failure(delivery.OutcomeThreadLost, "message to be replied not found"))
	e.newAlerts(t, 2, "fp2")
	e.round(t)
	if d.threadState != "unattached" || d.chainLastID != nil || e.threadEvents() != 1 ||
		e.db.replies[0].state != "pending" {
		t.Fatalf("thread %s %s events %d reply %+v", d.threadState, str(d.chainLastID), e.threadEvents(),
			e.db.replies[0])
	}
	e.business.Set(e.db.replies[0].next)
	e.round(t)
	r := e.replies()
	if len(r) != 3 || r[2].Root != (delivery.Root{MessageID: tgPost}) ||
		!e.db.replies[0].next.Equal(e.business.Now().Add(delivery.TransientFirstStep)) || e.threadEvents() != 1 {
		t.Fatalf("replies %+v next %v", r, e.db.replies[0].next)
	}
}

// TestThreadLostMarkupRejected: a lost Thread found after the markup was rejected is resent plain, without the link.
func TestThreadLostMarkupRejected(t *testing.T) {
	e := telegramEnv(t)
	e.learn(t, tgCopy, delivery.LearnedFromAutomaticForward)
	d := e.publish(t)
	e.rec.Script(deliverytest.MethodReply,
		deliverytest.Failure(delivery.OutcomeMarkupRejected, "can't parse entities"),
		deliverytest.Failure(delivery.OutcomeThreadLost, "message to be replied not found"),
		deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "801"}})
	e.newAlerts(t, 2, "fp2")
	e.round(t)
	r := e.replies()
	if len(r) != 3 || !r[2].Plain || r[2].Root != (delivery.Root{MessageID: tgPost}) || str(d.chainLastID) != "801" {
		t.Fatalf("replies %+v", r)
	}
}

// fakeRequests renders the "update" request of a template-mode outgoing webhook as the status of its Alert Group, so
// that a change of another field makes no call, and fails when told to.
type fakeRequests struct{ err error }

func (r fakeRequests) Desired(_ context.Context, _ dbgen.DBTX, _ int64, st delivery.RequestState) ([]byte,
	error) {
	if r.err != nil {
		return nil, r.err
	}
	key := "storm"
	if st.Group != nil {
		key = st.Group.AlertGroup.Status
	}
	sum := sha256.Sum256([]byte(key))
	return sum[:], nil
}

// webhookEnv is an env whose Route delivers only to the outgoing webhook 12 in the template mode, with fakeRequests.
func webhookEnv(t *testing.T) *env {
	t.Helper()
	e := memberEnv(t)
	e.db.dests[destWH].mode = "template"
	e.db.routeDests[routeID] = []int64{destWH}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Renderer: stubRenderer{},
		Requests: fakeRequests{}, Log: logging.New(e.log, logging.LevelInfo)})
	return e
}

// TestTemplateMode is C-15.FR-3, FR-4 and FR-7 through the recorder: "create" carries the Desired state and stores the
// values it extracted, a missing one recorded in the Timeline through groups and logged; an update carries the values,
// and one whose template fails ends Not delivered as template_error with the Destination's template error,
// MusterTemplateError about the Destination and the webhook_template_failed line, until the next request that renders
// clears them; a reply carries its event, records "open thread" whatever came after it, and one that fails is
// template_error too; a Destination without "reply in thread" queues no reply; the final edit carries .Final.
func TestTemplateMode(t *testing.T) {
	e := webhookEnv(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "m1", Values: map[string]string{"id": "m1"}, Missing: []string{"url"}, Rendered: true,
		Request: "create"}})
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	d := e.only(t)
	c := e.rec.Calls()[0]
	var st delivery.RequestState
	if c.Method != deliverytest.MethodPublish || c.Webhook == nil || json.Unmarshal(c.Webhook.State, &st) != nil ||
		st.Group == nil || st.Group.AlertGroup.Number != 7 || len(c.Webhook.Response) != 0 ||
		c.Loudness != groups.Loud || d.publishedAt == nil || d.responseValues["id"] != "m1" || *d.messageID != "m1" {
		t.Fatalf("create %+v, delivery %+v", c, d)
	}
	if len(e.db.system) != 1 || e.db.system[0].System != groups.SystemTemplateValueMissing ||
		e.db.system[0].Detail != "url" || e.db.system[0].AlertGroupID != groupID ||
		!strings.Contains(e.log.String(), `"event":"webhook_value_missing"`) {
		t.Errorf("missing value %+v %s", e.db.system, e.log.String())
	}

	// The same status renders the same update: no call.
	e.enqueue(t, e.group(groups.StatusFiring, "b"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAnnotationsChanged, Loudness: groups.Quiet})
	e.round(t)
	if len(e.rec.Calls()) != 1 {
		t.Fatalf("an unchanged update was sent: %+v", e.rec.Calls())
	}

	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Answer{Outcome: delivery.Outcome{
		Kind: delivery.OutcomeTemplateError, Error: "the request template failed: update/url: the value x is missing",
		Request: "update"}})
	e.enqueue(t, e.group(groups.StatusAcknowledged, "b"), groups.System,
		groups.Recorded{Seq: 3, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.round(t)
	ds := e.db.dests[destWH]
	if c := e.rec.Calls()[1]; c.Method != deliverytest.MethodUpdate || c.Webhook.Response["id"] != "m1" {
		t.Errorf("update %+v", c)
	}
	if d.state != "not_delivered" || *d.errorClass != "template_error" || ds.templateError == nil ||
		!strings.Contains(e.log.String(), `"event":"webhook_template_failed"`) ||
		!strings.Contains(e.log.String(), `"request":"update"`) {
		t.Fatalf("template error %+v %+v %s", d, ds, e.log.String())
	}
	raised := e.db.internal[len(e.db.internal)-1]
	if raised.name != "MusterTemplateError" || raised.status != "firing" || raised.labels["destination"] !=
		"DSAAAAAAAAAA12" || raised.labels["destination_name"] != "hook" || raised.labels["template"] !=
		"webhook_request" || raised.labels["route"] != "" {
		t.Errorf("raised %+v", raised)
	}
	if kinds := e.eventKinds(); kinds[len(kinds)-1] != "not_delivered" ||
		e.db.events[len(e.db.events)-1].ErrorClass.String != "template_error" {
		t.Errorf("events %v", kinds)
	}
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		Rendered: true, Request: "update"}})
	e.enqueue(t, e.group(groups.StatusSnoozed, "b"), groups.System,
		groups.Recorded{Seq: 4, Event: groups.EventSnoozed, Loudness: groups.Quiet})
	e.round(t)
	if resolved := e.db.internal[len(e.db.internal)-1]; ds.templateError != nil || d.state != "delivered" ||
		resolved.status != "resolved" || resolved.labels["destination"] != "DSAAAAAAAAAA12" {
		t.Fatalf("recovered %+v %+v", ds, resolved)
	}

	e.rec.Script(deliverytest.MethodReply, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		ThreadOpened: true, Values: map[string]string{"thread": "t1"}, Missing: []string{"x"}, Rendered: true}},
		deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeTemplateError, Error: "boom",
			ThreadOpened: true, Values: map[string]string{"other": "o"}}})
	e.enqueue(t, e.group(groups.StatusFiring, "b"), groups.System, alertsAdded(5, groups.StatusFiring, "fp2"))
	e.round(t)
	rc := e.replies()
	if len(rc) != 1 || rc[0].Webhook == nil || rc[0].Webhook.Event != "alerts_added" ||
		rc[0].Webhook.Response["id"] != "m1" || rc[0].Webhook.ThreadOpened || !d.threadOpened ||
		d.responseValues["thread"] != "t1" || d.responseValues["id"] != "m1" || len(e.db.system) != 2 {
		t.Fatalf("reply %+v, delivery %+v", rc, d)
	}
	e.business.Advance(2 * time.Minute)
	e.enqueue(t, e.group(groups.StatusFiring, "b"), groups.System,
		groups.Recorded{Seq: 6, Event: groups.EventTakeover, Loudness: groups.Loud})
	e.round(t)
	last := e.db.replies[len(e.db.replies)-1]
	if rc := e.replies(); len(rc) != 2 || !rc[1].Webhook.ThreadOpened || last.state != "not_delivered" ||
		*last.errorClass != "template_error" || d.responseValues["other"] != "o" {
		t.Errorf("failed reply %+v %+v", rc, last)
	}

	ds.noReplies = true
	e.enqueue(t, e.group(groups.StatusFiring, "b"), groups.System,
		groups.Recorded{Seq: 7, Event: groups.EventTakeover, Loudness: groups.Loud})
	if len(e.db.replies) != 2 {
		t.Errorf("a reply was queued without reply in thread: %d", len(e.db.replies))
	}

	e.db.routeDests[routeID] = nil
	ds.deleted = true
	failing := "the request template failed"
	ds.templateError = &failing
	if err := e.svc.RetireDestination(t.Context(), nil, destWH); err != nil {
		t.Fatal(err)
	}
	if last := e.db.internal[len(e.db.internal)-1]; last.status != "resolved" || last.name != "MusterTemplateError" ||
		last.labels["destination"] != "DSAAAAAAAAAA12" {
		t.Errorf("the template error of the deleted destination %+v", last)
	}
	e.round(t)
	calls := e.callsTo(destWH)
	if f := calls[len(calls)-1]; f.Method != deliverytest.MethodUpdate || f.Webhook.Final != finalText("AGAAAAAAAAAA21") {
		t.Errorf("final edit %+v", f)
	}
}

// TestTemplateModeStorm is C-15.FR-3 and C-11.FR-6: the Storm summary of a template-mode Destination carries .Storm
// with the Route, the counts and the link, and its final state Final; a Desired state that cannot be rendered fails the
// change.
func TestTemplateModeStorm(t *testing.T) {
	e := webhookEnv(t)
	e.db.routes[routeID] = fakeRoute{language: "en", window: 60, publicID: "RTAAAAAAAAAAA1", name: "payments",
		threshold: 0}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	var summary *fakeDelivery
	for _, d := range e.db.deliveries {
		if d.storm != 0 {
			summary = d
		}
	}
	var st delivery.RequestState
	if summary == nil || json.Unmarshal(summary.payload, &st) != nil || st.Storm == nil || st.Group != nil ||
		st.Storm.Route != "payments" || st.Storm.AlertGroupCount != 1 || st.Storm.Final {
		t.Fatalf("summary %+v", summary)
	}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Renderer: stubRenderer{},
		Requests: fakeRequests{err: errBoom}, Log: logging.New(e.log, logging.LevelInfo)})
	err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusAcknowledged, "a"),
		Actor: groups.System, Events: []groups.Recorded{{Seq: 2, Event: groups.EventAcknowledged,
			Loudness: groups.Quiet}}})
	if !errors.Is(err, errBoom) {
		t.Errorf("a failing render = %v", err)
	}
}

// TestPublishedDuringUpgrade: a messenger's Root message that a replica of an earlier version forgot for a
// republication — no message id, published_at still set — is published again, never edited without an id; an "open
// thread" recorded for a Root message published again meanwhile is left alone.
func TestPublishedDuringUpgrade(t *testing.T) {
	e, d := pending(t)
	e.round(t)
	d.messageID, d.state, d.next = nil, "pending", e.business.Now()
	d.version++
	d.hash = []byte("changed")
	e.round(t)
	if calls := e.rec.Calls(); len(calls) != 2 || calls[1].Method != deliverytest.MethodPublish {
		t.Fatalf("calls %+v", calls)
	}
	stale := e.business.Now().Add(-time.Hour)
	if err := e.db.RecordThreadOpened(t.Context(), dbgen.RecordThreadOpenedParams{ID: d.id,
		ResponseValues: []byte(`{"thread":"old"}`), PublishedAt: stale}); err != nil || d.threadOpened {
		t.Errorf("a stale open thread was recorded: %v %+v", err, d)
	}
}
