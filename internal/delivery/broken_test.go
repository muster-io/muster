// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/outbound"
)

// callMethods are the methods of the calls, comma-separated.
func callMethods(calls []deliverytest.Call) string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Method)
	}
	return strings.Join(out, ",")
}

// raised are the names of the Internal alerts raised (firing) and resolved, in order.
func (e *env) raised(status string) []string {
	var out []string
	for _, in := range e.db.internal {
		if in.status == status {
			out = append(out, in.name+"/"+in.labels["destination"])
		}
	}
	return out
}

// TestTransientBudgetBreaks is C-11.FR-8, FR-9, FR-17, FR-18 and AC-6 through the recorder: Transient answers are
// retried with delivery.transient_backoff until the attempt budget of delivery.transient_budget runs out; then the
// Destination is Broken as unavailable with the reason "unavailable after repeated failures: {last error}",
// MusterDestinationBroken fires, muster_destination_broken is 1 and the delivery stays pending — waiting, not Not
// delivered.
func TestTransientBudgetBreaks(t *testing.T) {
	e, d := pending(t)
	for i := range delivery.TransientBudgetAttempts {
		e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "HTTP 503"))
		e.round(t)
		if i < delivery.TransientBudgetAttempts-1 && e.db.dests[destMM].health != "healthy" {
			t.Fatalf("broken after %d attempts", i+1)
		}
		e.business.Set(d.next)
	}
	ds := e.db.dests[destMM]
	if e.rec.Count(deliverytest.MethodPublish) != 10 || ds.health != "broken" || *ds.brokenCause != "unavailable" ||
		*ds.brokenReason != "unavailable after repeated failures: HTTP 503" {
		t.Fatalf("destination %+v after %d calls", ds, len(e.rec.Calls()))
	}
	if d.state != "pending" || d.attempts != delivery.TransientBudgetAttempts || d.firstFailed == nil {
		t.Errorf("delivery %+v", d)
	}
	if got := e.raised("firing"); !slices.Equal(got, []string{"MusterDestinationBroken/DSAAAAAAAAAA11"}) ||
		e.db.internal[0].labels["destination_name"] != "ops" || e.db.internal[0].labels["severity"] != "critical" {
		t.Errorf("internal alerts %+v", e.db.internal)
	}
	if len(e.db.hints) != 1 || e.db.hints[0] != (db.Hint{OrgID: orgID, Type: "destination", ID: "DSAAAAAAAAAA11"}) {
		t.Errorf("hints %+v", e.db.hints)
	}
	if ev := e.db.events[len(e.db.events)-1]; ev.Kind != "destination_broken" || ev.ErrorClass.String != "transient" ||
		ev.Error.String != "unavailable after repeated failures: HTTP 503" {
		t.Errorf("event %+v", ev)
	}
	if !strings.Contains(e.log.String(), `"event":"destination_broken"`) ||
		!strings.Contains(e.log.String(), `"cause":"unavailable"`) {
		t.Errorf("log %s", e.log)
	}
	states, err := e.svc.States(t.Context(), "AGAAAAAAAAAA21")
	if err != nil || states[0].State != delivery.StateWaitingForBroken || states[0].Destination.Health != "broken" {
		t.Errorf("states %+v %v", states, err)
	}
	if err := e.svc.ExportBroken(t.Context()); err != nil {
		t.Fatal(err)
	}
	out := scrape(t)
	if !strings.Contains(out, `muster_destination_broken{destination="DSAAAAAAAAAA11"} 1`) ||
		!strings.Contains(out, `muster_destination_broken{destination="DSAAAAAAAAAA12"} 0`) {
		t.Errorf("gauge %s", out)
	}
	e.db.dests[destWH].deleted = true
	if err := e.svc.ExportBroken(t.Context()); err != nil || strings.Contains(scrape(t),
		`muster_destination_broken{destination="DSAAAAAAAAAA12"}`) {
		t.Errorf("a deleted destination keeps its series: %v", err)
	}
	e.db.fail["ListDestinationHealth"] = errBoom
	if err := e.svc.ExportBroken(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("a failed read = %v", err)
	}
}

// TestTimeBudgetBreaks: the time budget of delivery.transient_budget runs out before the attempt budget.
func TestTimeBudgetBreaks(t *testing.T) {
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "timeout"))
	e.round(t)
	e.business.Advance(delivery.TransientBudgetTime + time.Second)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "timeout"))
	e.round(t)
	if ds := e.db.dests[destMM]; ds.health != "broken" || d.state != "pending" ||
		*ds.brokenReason != "unavailable after repeated failures: timeout" {
		t.Errorf("destination %+v", ds)
	}
}

// TestRecoveryCurrentState is C-11.FR-16, FR-19 and AC-7 through the recorder: while the Destination is Broken, A
// starts and keeps firing, B — published before — is acknowledged and gets a new Alert, and C starts and resolves;
// after a successful probe A is published as a Loud new message, B's Root message is edited once to its current state
// with no Thread reply, and C is never published, its delivery withheld.
func TestRecoveryCurrentState(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit = 600
	const groupA, groupB, groupC = 31, 32, 33
	for i, id := range []int64{groupA, groupB, groupC} {
		e.db.groups[id] = &fakeGroup{id: id, publicID: "AGAAAAAAAAAA3" + string(rune('A'+i)), number: id,
			title: "g", status: "firing", route: routeID, created: business0}
	}
	view := func(id int64, status groups.Status) *groups.Group {
		g := e.db.groups[id]
		g.status = string(status)
		if status == groups.StatusResolved {
			g.resolvedAt = at(e.business.Now())
		}
		return &groups.Group{ID: id, PublicID: g.publicID, Number: id, RouteID: routeID, Title: "g", Status: status}
	}
	// B is published before the Destination breaks.
	e.enqueue(t, view(groupB, groups.StatusFiring), groups.System, created())
	e.round(t)
	e.rec.Reset()
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"))
	e.business.Advance(time.Second)
	e.enqueue(t, view(groupA, groups.StatusFiring), groups.System, created())
	e.round(t)
	if e.db.dests[destMM].health != "broken" {
		t.Fatal("not broken")
	}
	e.business.Advance(time.Second)
	e.enqueue(t, view(groupB, groups.StatusAcknowledged), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.enqueue(t, view(groupB, groups.StatusAcknowledged), groups.System,
		alertsAdded(3, groups.StatusAcknowledged, "fpB"))
	e.enqueue(t, view(groupC, groups.StatusFiring), groups.System, created())
	e.business.Advance(time.Second)
	e.enqueue(t, view(groupC, groups.StatusResolved), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventResolved, Loudness: groups.Quiet})
	byGroup := func(id int64) *fakeDelivery {
		for _, d := range e.db.deliveries {
			if d.group == id {
				return d
			}
		}
		return nil
	}
	if c := byGroup(groupC); c.state != "withheld" {
		t.Errorf("C resolved while broken = %s", c.state)
	}
	// B's new Alert and C's resolution by the system queue no Thread reply.
	if r := e.db.replies; len(r) != 2 || r[0].state != "dropped" || r[1].state != "dropped" {
		t.Errorf("replies while broken %+v %+v", r[0], r[1])
	}
	// Nothing is attempted before the probe is due.
	e.round(t)
	if len(e.rec.Calls()) != 1 {
		t.Fatalf("calls while broken %d", len(e.rec.Calls()))
	}
	e.business.Set(*e.db.dests[destMM].nextProbe)
	e.round(t)
	calls := e.rec.Calls()[1:]
	if callMethods(calls) != "publish,update" {
		t.Fatalf("after the probe %s", callMethods(calls))
	}
	pub, upd := calls[0], calls[1]
	if pub.Loudness != groups.Loud || !slices.Equal(pub.Mentions, []groups.Mention{groups.MentionNewAlertGroup}) ||
		!strings.HasPrefix(pub.Message.Text(), "#31 ") {
		t.Errorf("A %+v", pub)
	}
	if !strings.HasPrefix(upd.Message.Text(), "#32 ") || !strings.Contains(upd.Message.Text(), "Acknowledged") {
		t.Errorf("B %+v", upd)
	}
	if e.db.dests[destMM].health != "healthy" || byGroup(groupC).state != "withheld" ||
		byGroup(groupA).state != "delivered" || byGroup(groupB).state != "delivered" {
		t.Errorf("after recovery %+v", e.db.dests[destMM])
	}
	if got := e.raised("resolved"); !slices.Equal(got, []string{"MusterDestinationBroken/DSAAAAAAAAAA11"}) {
		t.Errorf("resolved %v", got)
	}
	if !slices.Contains(e.eventKinds(), "destination_recovered") || !strings.Contains(e.log.String(),
		`"event":"destination_recovered"`) {
		t.Errorf("events %v, log %s", e.eventKinds(), e.log)
	}
	// Nothing more: no Thread reply for B, nothing for C.
	e.business.Advance(time.Hour)
	e.round(t)
	if len(e.rec.Calls()) != 3 {
		t.Errorf("more calls %s", callMethods(e.rec.Calls()))
	}
}

// TestProbeCheck is C-11.FR-9 and AC-11 through the recorder: with the Destination Broken and nothing waiting, the
// probe after delivery.broken_probe_interval runs the adapter's Destination check in the delivery client class; a
// failing check keeps it Broken with that error as the reason; a passing one makes it healthy, resolves
// MusterDestinationBroken and records destination_recovered, with no Alert Group activity.
func TestProbeCheck(t *testing.T) {
	e := newEnv(t)
	e.breakDest(destMM, "HTTP 403")
	if n := e.round(t); n != delivery.BrokenProbeInterval && n != delivery.MaxWait {
		t.Errorf("wait %v", n)
	}
	e.w.MaxWait = time.Hour
	if n := e.round(t); n != delivery.BrokenProbeInterval {
		t.Errorf("the worker wakes for the probe after %v", n)
	}
	if len(e.rec.Calls()) != 0 {
		t.Fatalf("probed early %+v", e.rec.Calls())
	}
	e.business.Advance(delivery.BrokenProbeInterval)
	e.rec.Script(deliverytest.MethodCheck, deliverytest.Failure(delivery.OutcomeFatal, "not a member"))
	e.round(t)
	ds := e.db.dests[destMM]
	if c := e.rec.Calls(); len(c) != 1 || c[0].Method != deliverytest.MethodCheck || c[0].Class != outbound.ClassDelivery ||
		c[0].Destination.PublicID != "DSAAAAAAAAAA11" {
		t.Fatalf("check %+v", c)
	}
	if ds.health != "broken" || *ds.brokenReason != "not a member" || *ds.brokenCause != "unavailable" ||
		!ds.nextProbe.Equal(e.business.Now().Add(delivery.BrokenProbeInterval)) {
		t.Errorf("after the failed check %+v", ds)
	}
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	if ds.health != "healthy" || ds.brokenReason != nil || ds.nextProbe != nil || len(e.rec.Calls()) != 2 {
		t.Errorf("after the passing check %+v", ds)
	}
	if got := e.raised("resolved"); !slices.Equal(got, []string{"MusterDestinationBroken/DSAAAAAAAAAA11"}) ||
		!slices.Equal(e.eventKinds(), []string{"destination_recovered"}) || e.db.events[0].AlertGroupID.Valid ||
		len(e.db.deliveries) != 0 {
		t.Errorf("internal %v, events %v", got, e.eventKinds())
	}
	if !strings.Contains(e.log.String(), `"broken_for_s":600`) {
		t.Errorf("log %s", e.log)
	}
	// A failing check without text keeps the outcome as the reason.
	e.breakDest(destMM, "x")
	e.business.Advance(delivery.BrokenProbeInterval)
	e.rec.Script(deliverytest.MethodCheck, deliverytest.Failure(delivery.OutcomeTransient, ""))
	e.round(t)
	if *ds.brokenReason != "transient" {
		t.Errorf("reason %q", *ds.brokenReason)
	}
}

// adapterOnly is an adapter that offers no Destination check, as an outgoing webhook.
type adapterOnly struct{ rec *deliverytest.Recorder }

func (a adapterOnly) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	return a.rec.Publish(ctx, c, m)
}

func (a adapterOnly) Update(ctx context.Context, c delivery.Call, id string, m delivery.Message) delivery.Outcome {
	return a.rec.Update(ctx, c, id, m)
}

func (a adapterOnly) Reply(ctx context.Context, c delivery.Call, r delivery.Root, m delivery.Message) delivery.Outcome {
	return a.rec.Reply(ctx, c, r, m)
}

func (a adapterOnly) LengthLimit() int { return a.rec.LengthLimit() }

// TestProbeWithoutCheck is C-11.FR-9 for a type without a Destination check: the probe marks the Destination, and its
// next delivery that comes due is attempted at once as the probe.
func TestProbeWithoutCheck(t *testing.T) {
	e := newEnv(t)
	e.w.Adapters[delivery.TypeMattermost] = adapterOnly{rec: e.rec}
	e.w.MaxWait = time.Hour
	e.breakDest(destMM, "HTTP 500")
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	ds := e.db.dests[destMM]
	if !ds.probeOnNext || ds.health != "broken" || len(e.rec.Calls()) != 0 {
		t.Fatalf("marked %+v", ds)
	}
	if n := e.round(t); n != time.Hour {
		t.Errorf("a marked destination with nothing due wakes the worker after %v", n)
	}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	if c := e.rec.Calls(); len(c) != 1 || c[0].Method != deliverytest.MethodPublish || c[0].Class != outbound.ClassDelivery ||
		ds.health != "healthy" || e.only(t).state != "delivered" {
		t.Errorf("the next delivery as the probe %+v %+v", c, ds)
	}
}

// TestProbeAttemptsOldest is C-11.FR-9: with deliveries waiting, the probe attempts the oldest one through the worker's
// path; a failure keeps the Destination Broken with the new reason, and no new event or Internal alert; one leased by
// another replica waits for the next probe.
func TestProbeAttemptsOldest(t *testing.T) {
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"))
	e.round(t)
	events, raised := len(e.db.events), len(e.db.internal)
	e.business.Advance(delivery.BrokenProbeInterval)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "HTTP 502"))
	e.round(t)
	ds := e.db.dests[destMM]
	if len(e.rec.Calls()) != 2 || ds.health != "broken" || *ds.brokenReason != "HTTP 502" ||
		*ds.brokenCause != "unavailable" || len(e.db.events) != events || len(e.db.internal) != raised {
		t.Errorf("after the failed probe %+v, %v", ds, e.eventKinds())
	}
	// Leased elsewhere: nothing this time.
	d.owner, d.until = "r2", e.real.Now().Add(time.Hour)
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	if len(e.rec.Calls()) != 2 {
		t.Errorf("probed a leased delivery")
	}
	d.owner, d.until = "", time.Time{}
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	if ds.health != "healthy" || d.state != "delivered" || len(e.rec.Calls()) != 3 {
		t.Errorf("after the passing probe %+v %+v", ds, d)
	}
	if !slices.Contains(e.eventKinds(), "publication") || !slices.Contains(e.eventKinds(), "destination_recovered") {
		t.Errorf("events %v", e.eventKinds())
	}
}

// TestMarkHealthy: a successful Destination check started by a person ends the Broken state as a probe does, in the
// caller's transaction or in its own; a healthy Destination changes nothing.
func TestMarkHealthy(t *testing.T) {
	e := newEnv(t)
	e.breakDest(destMM, "x")
	after, err := e.svc.MarkHealthy(t.Context(), nil, destMM)
	if err != nil || after == nil || e.db.dests[destMM].health != "healthy" ||
		!strings.Contains(e.log.String(), `"event":"destination_recovered"`) || e.db.notified != 1 {
		t.Fatalf("own transaction %v, %+v", err, e.db.dests[destMM])
	}
	e.log.Reset()
	e.breakDest(destMM, "x")
	after, err = e.svc.MarkHealthy(t.Context(), fakeTx{}, destMM)
	if err != nil || e.db.dests[destMM].health != "healthy" || strings.Contains(e.log.String(), "destination_recovered") {
		t.Fatalf("caller's transaction %v", err)
	}
	after(t.Context())
	if !strings.Contains(e.log.String(), `"event":"destination_recovered"`) {
		t.Errorf("not logged after the commit: %s", e.log)
	}
	if _, err := e.svc.MarkHealthy(t.Context(), nil, destMM); err != nil || len(e.raised("resolved")) != 2 {
		t.Errorf("a healthy destination %v %v", err, e.raised("resolved"))
	}
	for _, q := range []string{"MarkDestinationHealthy", "InsertDeliveryEvent", "FindBuiltinIntegration", "Notify",
		"DropDueReplies", "ResetDestinationBudgets", "WithholdResolvedUnpublished", "RecoverUnpublished", "RecoverPublished", "NotifyDelivery"} {
		e.db.fail[q] = errBoom
		for _, tx := range []dbgen.DBTX{fakeTx{}, nil} {
			e.breakDest(destMM, "x")
			if _, err := e.svc.MarkHealthy(t.Context(), tx, destMM); !errors.Is(err, errBoom) {
				t.Errorf("%s (%T) = %v", q, tx, err)
			}
		}
		delete(e.db.fail, q)
	}
}

// TestProbeFailures: a probe that fails is logged as delivery_work_failed with the work probe, and a failed claim fails
// the round.
func TestProbeFailures(t *testing.T) {
	for _, q := range []string{"LeaseOldestWaiting", "MarkProbeOnNextDelivery", "UpdateBrokenReason",
		"MarkDestinationHealthy"} {
		t.Run(q, func(t *testing.T) {
			e := newEnv(t)
			if q == "MarkProbeOnNextDelivery" {
				e.w.Adapters[delivery.TypeMattermost] = adapterOnly{rec: e.rec}
			}
			if q == "UpdateBrokenReason" {
				e.rec.Script(deliverytest.MethodCheck, deliverytest.Failure(delivery.OutcomeFatal, "no"))
			}
			e.breakDest(destMM, "x")
			e.business.Advance(delivery.BrokenProbeInterval)
			e.db.fail[q] = errBoom
			e.round(t)
			if !strings.Contains(e.log.String(), `"work":"probe"`) {
				t.Errorf("log %s", e.log)
			}
		})
	}
	e := newEnv(t)
	e.db.fail["ClaimBrokenProbes"] = errBoom
	if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("a failed claim = %v", err)
	}
	// More Broken Destinations than one claim takes: the probe claims again.
	e = newEnv(t)
	e.w.Batch = 1
	e.breakDest(destMM, "x")
	e.breakDest(destWH, "x")
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	if e.rec.Count(deliverytest.MethodCheck) != 2 || e.db.calls["ClaimBrokenProbes"] != 3 {
		t.Errorf("%d checks in %d claims", e.rec.Count(deliverytest.MethodCheck), e.db.calls["ClaimBrokenProbes"])
	}
}

// TestBreakingTouchesNoOtherRow: breaking a Destination changes only the Destination and the row whose outcome broke
// it, so that replicas recording outcomes of the same Destination at once never wait for each other's rows; the
// recovery gives every waiting delivery and Thread reply a fresh Transient budget.
func TestBreakingTouchesNoOtherRow(t *testing.T) {
	e, d := pending(t)
	e.db.groups[22] = &fakeGroup{id: 22, publicID: "AGAAAAAAAAAA22", number: 8, title: "b", status: "firing",
		route: routeID, created: business0}
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.view(22, groups.StatusFiring),
		Actor: groups.System, Events: []groups.Recorded{created()}}); err != nil {
		t.Fatal(err)
	}
	other := e.db.deliveries[1]
	other.attempts, other.firstFailed, other.next = 3, at(business0), business0.Add(time.Hour)
	reply := &fakeReply{id: 999, delivery: other.id, group: 22, dest: destMM, event: "takeover", state: "pending",
		next: business0.Add(time.Hour), attempts: 2, firstFailed: at(business0)}
	e.db.replies = append(e.db.replies, reply)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"))
	e.round(t)
	if e.db.dests[destMM].health != "broken" || e.db.calls["ResetDestinationBudgets"] != 0 ||
		other.attempts != 3 || other.firstFailed == nil || reply.attempts != 2 {
		t.Fatalf("breaking changed other rows: %+v %+v", other, reply)
	}
	if _, err := e.svc.MarkHealthy(t.Context(), nil, destMM); err != nil {
		t.Fatal(err)
	}
	if other.attempts != 0 || other.firstFailed != nil || reply.attempts != 0 || reply.firstFailed != nil ||
		d.attempts != 0 {
		t.Errorf("no fresh budget after the recovery: %+v %+v", other, reply)
	}
}

// TestDeletedBrokenFinalEdit: a probe whose final edit reaches a deleted Broken Destination ends its Broken state
// without the signs of a recovery — no destination_recovered event, hint or line — but resolves
// MusterDestinationBroken.
func TestDeletedBrokenFinalEdit(t *testing.T) {
	e := memberEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	d := e.only(t)
	e.breakDest(destMM, "503")
	e.db.dests[destMM].deleted = true
	if err := e.svc.RetireDestination(t.Context(), nil, destMM); err != nil {
		t.Fatal(err)
	}
	if !d.desiredRetire {
		t.Fatalf("no final edit %+v", d)
	}
	e.db.hints, e.db.internal = nil, nil
	e.log.Reset()
	e.business.Advance(delivery.BrokenProbeInterval)
	e.round(t)
	if d.state != "retired" || e.db.dests[destMM].health != "healthy" {
		t.Fatalf("after the probe %+v, %s", d, e.db.dests[destMM].health)
	}
	if slices.Contains(e.eventKinds(), "destination_recovered") || len(e.db.hints) != 0 ||
		strings.Contains(e.log.String(), "destination_recovered") {
		t.Errorf("recovered a deleted destination: %v, hints %+v, log %s", e.eventKinds(), e.db.hints, e.log)
	}
	if got := e.raised("resolved"); !slices.Equal(got, []string{"MusterDestinationBroken/DSAAAAAAAAAA11"}) {
		t.Errorf("resolved %v", got)
	}
}
