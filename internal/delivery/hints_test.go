// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

var (
	groupHint = db.Hint{OrgID: orgID, Type: "alert-group", ID: "AGAAAAAAAAAA21"}
	destHint  = db.Hint{OrgID: orgID, Type: "destination", ID: "DSAAAAAAAAAA11"}
)

// TestDeliveryOutcomeHints is C-09.FR-25 for delivery: an outcome that changes what the Alert Group page and the
// Delivery problem mark show sends the alert-group hint of the Alert Group, so that the page and the list read it
// again without polling; a retry that leaves the delivery pending sends none, and a Destination that becomes Broken
// sends its own hint and the hint of each Alert Group now waiting for it.
func TestDeliveryOutcomeHints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer deliverytest.Answer
		hints  []db.Hint
	}{
		{"delivered", deliverytest.OK(), []db.Hint{groupHint}},
		{"not delivered", deliverytest.Failure(delivery.OutcomeUnknown, "HTTP 418"), []db.Hint{groupHint}},
		{"gone on a publication", deliverytest.Failure(delivery.OutcomeGone, "no such channel"), []db.Hint{groupHint}},
		{"waiting for a broken destination", deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"),
			[]db.Hint{destHint, groupHint}},
		{"retry_after", deliverytest.RetryAfter(7*time.Second, delivery.ScopeDestination), nil},
		{"transient", deliverytest.Failure(delivery.OutcomeTransient, "HTTP 503"), nil},
		{"thread_lost", deliverytest.Failure(delivery.OutcomeThreadLost, "thread closed"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := pending(t)
			e.rec.Script(deliverytest.MethodPublish, tc.answer)
			e.round(t)
			if !slices.Equal(e.db.hints, tc.hints) {
				t.Errorf("hints %+v, want %+v", e.db.hints, tc.hints)
			}
		})
	}
}

// TestRecoveryHints: a Broken Destination that becomes healthy sends its hint and the hint of each Alert Group that
// waited for it; the delivery that follows sends the hint of its end.
func TestRecoveryHints(t *testing.T) {
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"))
	e.round(t)
	e.db.hints = nil
	if err := e.svc.EndBroken(t.Context(), destMM); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.db.hints, []db.Hint{destHint, groupHint}) {
		t.Fatalf("recovery hints %+v", e.db.hints)
	}
	e.db.hints = nil
	e.round(t)
	if d.state != "delivered" || !slices.Equal(e.db.hints, []db.Hint{groupHint}) {
		t.Errorf("delivery %s, hints %+v", d.state, e.db.hints)
	}
}

// TestPossibleDuplicateHint: the mark "Possible duplicate" and the delivery that follows send the hint.
func TestPossibleDuplicateHint(t *testing.T) {
	e, d := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Then: func() { e.db.fail["RecordDelivered"] = errBoom }})
	e.round(t)
	delete(e.db.fail, "RecordDelivered")
	e.db.hints = nil
	e.real.Advance(delivery.Lease)
	e.round(t)
	if !d.possibleDuplicate || !slices.Equal(e.db.hints, []db.Hint{groupHint, groupHint}) {
		t.Errorf("possible duplicate %v, hints %+v", d.possibleDuplicate, e.db.hints)
	}
}

// TestDeletedRootHints: a Root message deleted in the messenger sends the hint when it is published again and when
// it is marked deleted in the messenger.
func TestDeletedRootHints(t *testing.T) {
	e := published(t)
	d := e.only(t)
	e.db.hints = nil
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "post not found"))
	e.round(t)
	if !d.republished || !slices.Equal(e.db.hints, []db.Hint{groupHint}) {
		t.Fatalf("republish %+v, hints %+v", d, e.db.hints)
	}
	e.round(t)
	e.db.hints = nil
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System,
		groups.Recorded{Seq: 3, Event: groups.EventUnacknowledged, Variant: groups.VariantCommand,
			Loudness: groups.Quiet})
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, ""))
	e.round(t)
	if d.state != "deleted_in_messenger" || !slices.Equal(e.db.hints, []db.Hint{groupHint}) {
		t.Errorf("deleted %s, hints %+v", d.state, e.db.hints)
	}
}

// TestStormSummaryHints: a Storm summary has no Alert Group, so its deliveries send no alert-group hint; every
// alert-group hint names an Alert Group.
func TestStormSummaryHints(t *testing.T) {
	e := stormEnv(t)
	for i := int64(1); i <= 25; i++ {
		e.business.Set(business0.Add(time.Duration(i) * time.Second))
		e.addGroup(t, 1000+i, false)
		e.round(t)
	}
	e.onlyStorm(t)
	known := map[string]bool{}
	for _, g := range e.db.groups {
		known[g.publicID] = true
	}
	n := 0
	for _, h := range e.db.hints {
		if h.Type == "alert-group" {
			n++
			if !known[h.ID] {
				t.Errorf("hint %+v names no Alert Group", h)
			}
		}
	}
	if n != 20 {
		t.Errorf("%d alert-group hints for 20 published Alert Groups: %+v", n, e.db.hints)
	}
}

// TestHintFailures: a hint that cannot be sent fails the transaction of the change it announces, which the worker
// reports as failed work.
func TestHintFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer deliverytest.Answer
		fail   func(e *env)
	}{
		{"delivered", deliverytest.OK(), func(e *env) { e.db.fail["Notify"] = errBoom }},
		{"not delivered", deliverytest.Failure(delivery.OutcomeUnknown, "HTTP 418"),
			func(e *env) { e.db.fail["Notify"] = errBoom }},
		{"list the waiting alert groups", deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"),
			func(e *env) { e.db.fail["ListPendingGroups"] = errBoom }},
		{"hint a waiting alert group", deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"),
			func(e *env) {
				// The destination hint goes out; the hint of the Alert Group after it fails.
				e.db.before["Notify"] = func() { e.db.before["Notify"] = func() { e.db.fail["Notify"] = errBoom } }
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := pending(t)
			e.rec.Script(deliverytest.MethodPublish, tc.answer)
			tc.fail(e)
			e.round(t)
			if !strings.Contains(e.log.String(), `"event":"delivery_work_failed"`) {
				t.Errorf("log %s", e.log)
			}
		})
	}
	e, _ := pending(t)
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"))
	e.round(t)
	e.db.fail["ListPendingGroups"] = errBoom
	if err := e.svc.EndBroken(t.Context(), destMM); !errors.Is(err, errBoom) {
		t.Errorf("recovery = %v", err)
	}
}

// alertGroupHints are the ids of the alert-group hints sent, sorted.
func (e *env) alertGroupHints() []string {
	var out []string
	for _, h := range e.db.hints {
		if h.Type == "alert-group" {
			out = append(out, h.ID)
		}
	}
	slices.Sort(out)
	return out
}

// TestStormEndHints: when a Storm ends, the held Alert Groups that resolved meanwhile are withheld and each gets its
// hint; those still open are released to be published and get theirs when that delivery ends.
func TestStormEndHints(t *testing.T) {
	e := stormEnv(t)
	for i := int64(1); i <= 25; i++ {
		e.business.Set(business0.Add(time.Duration(i) * time.Second))
		e.addGroup(t, 1000+i, false)
		e.round(t)
	}
	st := e.onlyStorm(t)
	for _, id := range []int64{1021, 1022} {
		e.enqueue(t, e.view(id, groups.StatusResolved), groups.System,
			groups.Recorded{Seq: 2, Event: groups.EventResolved, Loudness: groups.Quiet})
	}
	e.db.hints = nil
	for range 5 {
		if st.ended != nil {
			break
		}
		e.calmCheck(t, st)
	}
	if st.ended == nil {
		t.Fatal("the storm did not end")
	}
	if got := e.alertGroupHints(); !slices.Equal(got, []string{"AGAAAAAAAA1021", "AGAAAAAAAA1022"}) {
		t.Errorf("hints at the end of the storm %v", got)
	}
	e.db.hints = nil
	e.round(t)
	if got := e.alertGroupHints(); !slices.Equal(got, []string{"AGAAAAAAAA1023", "AGAAAAAAAA1024",
		"AGAAAAAAAA1025"}) {
		t.Errorf("hints of the released publications %v", got)
	}

	f := stormEnv(t)
	for i := int64(1); i <= 21; i++ {
		f.business.Set(business0.Add(time.Duration(i) * time.Second))
		f.addGroup(t, 1000+i, false)
		f.round(t)
	}
	fst := f.onlyStorm(t)
	f.enqueue(t, f.view(1021, groups.StatusResolved), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventResolved, Loudness: groups.Quiet})
	f.business.Set(f.db.timers[fst.id].Add(time.Hour))
	f.db.before["WithholdHeldResolved"] = func() { f.db.fail["Notify"] = errBoom }
	if _, err := f.svc.CheckStormCalm(t.Context(), nil, fst.id); !errors.Is(err, errBoom) {
		t.Errorf("a failed hint at the end of the storm = %v", err)
	}
}

// TestRouteDestinationRemovedHints: a Destination that leaves a Route gives the open Alert Groups of the Route their
// final edit there, or withholds those never published there; each of them gets its hint in the transaction of the
// Route's change, and the final edit sends another when it is made.
func TestRouteDestinationRemovedHints(t *testing.T) {
	e := published(t)
	d := e.only(t)
	e.db.hints = nil
	e.changed(t, routeID, nil, []int64{destMM})
	if !d.desiredRetire || !slices.Equal(e.db.hints, []db.Hint{groupHint}) {
		t.Fatalf("final edit %+v, hints %+v", d, e.db.hints)
	}
	e.db.hints = nil
	e.round(t)
	if d.state != "retired" || !slices.Equal(e.db.hints, []db.Hint{groupHint}) {
		t.Errorf("after the final edit %s, hints %+v", d.state, e.db.hints)
	}

	u, ud := pending(t)
	u.db.hints = nil
	u.changed(t, routeID, nil, []int64{destMM})
	if ud.state != "withheld" || !slices.Equal(u.db.hints, []db.Hint{groupHint}) {
		t.Errorf("never published %s, hints %+v", ud.state, u.db.hints)
	}

	f, _ := pending(t)
	f.db.fail["Notify"] = errBoom
	if err := f.svc.RouteDestinationsChanged(t.Context(), nil, routeID, nil, []int64{destMM}); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed hint of the route's change = %v", err)
	}
}
