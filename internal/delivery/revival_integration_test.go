// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/ingest"
)

// TestIntegrationRevivedDeliveryHasFreshTransientBudget covers C-11.FR-8 on PostgreSQL (issue #648): a delivery
// withheld or retired after a counted Transient error and brought back more than delivery.transient_budget later — its
// Destination joining the Route again (RejoinDelivery), its Alert Group opening again (SetDesired) or its Storm summary
// coming back to a Destination that joins the Route again (EnsureStormSummary) — starts with a fresh budget, so one
// more Transient error does not make its Destination Broken. A pending delivery that waits out the backoff of a
// Transient error keeps its counts through each of them.
func TestIntegrationRevivedDeliveryHasFreshTransientBudget(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		later := delivery.TransientBudgetTime + time.Minute

		// The Destination leaves the Route while the first Publication of z waits out the backoff of a Transient
		// error, and the edit of x too; it joins again later than the time budget.
		m := newMembershipBackoff(t, l, 3, "revived")
		if _, err := l.setDestinations(t, m.route); err != nil {
			t.Fatal(err)
		}
		z := l.budget(t, `g.public_id = $1 AND d.destination_id = $2`, m.z, m.destID)
		x := l.budget(t, `g.public_id = $1 AND d.destination_id = $2`, m.x, m.destID)
		if z.state != "withheld" || z.attempts != 1 || z.firstFailed == nil || x.state != "pending" ||
			x.attempts != 1 || x.firstFailed == nil {
			t.Fatalf("rejoined: after leaving the route: z %+v, x %+v", z, x)
		}
		l.business.Advance(later)
		if _, err := l.setDestinations(t, m.route, m.dest); err != nil {
			t.Fatal(err)
		}
		if r := l.budget(t, `g.public_id = $1 AND d.destination_id = $2`, m.z, m.destID); r.state != "pending" ||
			!r.fresh() {
			t.Errorf("rejoined: the withheld delivery %+v, want a fresh budget", r)
		}
		if r := l.budget(t, `g.public_id = $1 AND d.destination_id = $2`, m.x, m.destID); r.state != "pending" ||
			!r.keeps(x) {
			t.Errorf("rejoined: the delivery waiting out the backoff %+v, want the counts of %+v", r, x)
		}
		l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
		l.round(t, l.a)
		l.retriedOnce(t, "rejoined", m.destID, `g.public_id = $1 AND d.destination_id = $2`, m.z, m.destID)

		// The first Publication fails Transiently, the Destination is Broken and the Alert Group resolves, so its
		// delivery is withheld; the Destination recovers, and the Alert Group opens again later than the time budget.
		l.fresh(t, 1000, 1)
		l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
		l.fire(t, "reopened", "Reopened/a", "Reopened/b")
		gid := l.group(t, "Reopened")
		l.round(t, l.a)
		l.brk(t, l.dests[0])
		l.resolve(t, gid)
		l.probe(t)
		if health, _ := l.health(t, l.dests[0]); health != "healthy" {
			t.Fatalf("opened again: the destination did not recover: %s", methods(l.rec.Calls()))
		}
		withheld := l.budget(t, `g.public_id = $1 AND d.destination_id = $2`, gid, l.dests[0])
		if withheld.state != "withheld" || withheld.attempts != 1 || withheld.firstFailed == nil {
			t.Fatalf("opened again: after resolving while broken: %+v", withheld)
		}
		l.business.Advance(later)
		// A new Desired state while the Alert Group stays resolved — one of its Alerts resolves in Alertmanager —
		// leaves the delivery withheld with its budget.
		const version = `SELECT d.desired_version FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = $1 AND d.destination_id = $2`
		before := l.count(t, version, gid, l.dests[0])
		if _, err := l.snaps.Store(t.Context(), ingest.Received{IntegrationID: l.intID, Body: []byte(`{"groupKey":
			"reopened","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"Reopened","team":"db",
			"disk":"Reopened/a"},"annotations":{},"startsAt":"2026-10-07T11:00:00Z"},{"status":"resolved","labels":
			{"alertname":"Reopened","team":"db","disk":"Reopened/b"},"annotations":{},
			"startsAt":"2026-10-07T11:00:00Z","endsAt":"` + l.business.Now().UTC().Format(time.RFC3339) +
			`"}]}`)}); err != nil {
			t.Fatal(err)
		}
		l.process(t)
		if l.count(t, version, gid, l.dests[0]) <= before {
			t.Fatal("opened again: the resolved alert made no new desired state")
		}
		if r := l.budget(t, `g.public_id = $1 AND d.destination_id = $2`, gid, l.dests[0]); r.state != "withheld" ||
			!r.keeps(withheld) {
			t.Errorf("opened again: a new desired state while resolved %+v, want the counts of %+v", r, withheld)
		}
		if _, err := l.groups.Unresolve(t.Context(), l.alice, gid); err != nil {
			t.Fatal(err)
		}
		if r := l.budget(t, `g.public_id = $1 AND d.destination_id = $2`, gid, l.dests[0]); r.state != "pending" ||
			!r.fresh() {
			t.Errorf("opened again: the withheld delivery %+v, want a fresh budget", r)
		}
		l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
		l.round(t, l.a)
		failed := l.retriedOnce(t, "opened again", l.dests[0], `g.public_id = $1 AND d.destination_id = $2`, gid,
			l.dests[0])
		l.fire(t, "reopened", "Reopened/a", "Reopened/c")
		if r := l.budget(t, `g.public_id = $1 AND d.destination_id = $2`, gid, l.dests[0]); r.state != "pending" ||
			!r.keeps(failed) {
			t.Errorf("opened again: a new alert during the backoff %+v, want the counts of %+v", r, failed)
		}

		// The edit of a Storm summary fails Transiently, its Destination leaves the Route, so the summary is retired,
		// and joins it again later than the time budget, while the Storm goes on.
		l.fresh(t, 1000, 1)
		ds := l.newDest(t, 4)
		routeID, route := l.newRoute(t, "stormback", 2, "DSAAAAAAAAAAA4")
		storm := func(n int) {
			t.Helper()
			l.fireAlerts(t, fmt.Sprintf("sb-%d", n), alert{name: fmt.Sprintf("SB%d/a", n), team: "stormback"})
			l.business.Advance(time.Second)
		}
		for n := range 5 {
			storm(n)
		}
		l.round(t, l.a)
		const summary = `d.storm_id IS NOT NULL AND d.destination_id = $1`
		if l.count(t, `SELECT count(*) FROM storms WHERE route_id = $1 AND ended_at IS NULL`, routeID) != 1 ||
			l.budget(t, summary, ds).state != "delivered" {
			t.Fatalf("storm: no storm summary: %s", methods(l.rec.Calls()))
		}
		l.rec.Reset()
		l.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeTransient, "503"))
		storm(5)
		l.round(t, l.a)
		if r := l.budget(t, summary, ds); r.state != "pending" || r.attempts != 1 || r.firstFailed == nil {
			t.Fatalf("storm: after the transient error: %+v, %s", r, methods(l.rec.Calls()))
		}
		if _, err := l.setDestinations(t, route); err != nil {
			t.Fatal(err)
		}
		if r := l.budget(t, summary, ds); r.state != "retired" || r.attempts != 1 {
			t.Fatalf("storm: after leaving the route: %+v", r)
		}
		l.business.Advance(later)
		if _, err := l.setDestinations(t, route, "DSAAAAAAAAAAA4"); err != nil {
			t.Fatal(err)
		}
		if r := l.budget(t, summary, ds); r.state != "pending" || !r.fresh() {
			t.Errorf("storm: the retired summary %+v, want a fresh budget", r)
		}
		l.rec.Reset()
		l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
		l.round(t, l.a)
		failed = l.retriedOnce(t, "storm", ds, summary, ds)
		storm(6)
		if r := l.budget(t, summary, ds); r.state != "pending" || !r.keeps(failed) {
			t.Errorf("storm: a new alert group during the backoff %+v, want the counts of %+v", r, failed)
		}
	})
}

// budgetRow is the Transient budget of a delivery: its attempts, the time of its first counted failure and its last
// error.
type budgetRow struct {
	state                 string
	attempts              int64
	firstFailed           *time.Time
	errorClass, lastError *string
}

func (r budgetRow) String() string {
	first, class, text := "-", "-", "-"
	if r.firstFailed != nil {
		first = r.firstFailed.UTC().Format(time.TimeOnly)
	}
	if r.errorClass != nil {
		class = *r.errorClass
	}
	if r.lastError != nil {
		text = *r.lastError
	}
	return fmt.Sprintf("{%s attempts=%d first_failed_at=%s last_error_class=%s last_error=%q}", r.state, r.attempts,
		first, class, text)
}

// fresh says whether the budget is unused.
func (r budgetRow) fresh() bool {
	return r.attempts == 0 && r.firstFailed == nil && r.errorClass == nil && r.lastError == nil
}

// keeps says whether the budget is the one of before.
func (r budgetRow) keeps(before budgetRow) bool {
	return r.attempts == before.attempts && r.firstFailed != nil && before.firstFailed != nil &&
		r.firstFailed.Equal(*before.firstFailed) && r.errorClass != nil && before.errorClass != nil &&
		*r.errorClass == *before.errorClass
}

// budget reads the budget of the one delivery that where, over deliveries d left-joined to alert_groups g, selects.
func (l *live) budget(t *testing.T, where string, args ...any) budgetRow {
	t.Helper()
	var r budgetRow
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT d.state, d.attempts, d.first_failed_at, d.last_error_class,
		d.last_error FROM deliveries d LEFT JOIN alert_groups g ON g.id = d.alert_group_id WHERE `+where,
		args...).Scan(&r.state, &r.attempts, &r.firstFailed, &r.errorClass, &r.lastError); err != nil {
		t.Fatalf("the delivery where %s %v: %v", where, args, err)
	}
	return r
}

// retriedOnce checks that the one Transient error a revived delivery met is its first counted one: the Destination
// was never Broken, not even until a later success of the same round, and the delivery waits out the first backoff
// step. It returns the budget.
func (l *live) retriedOnce(t *testing.T, name string, dest int64, where string, args ...any) budgetRow {
	t.Helper()
	r := l.budget(t, where, args...)
	health, reason := l.health(t, dest)
	broken := l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'destination_broken'
		AND destination_id = $1`, dest)
	if health != "healthy" || broken != 0 || r.state != "pending" || r.attempts != 1 || r.firstFailed == nil ||
		!r.firstFailed.Equal(l.business.Now()) {
		t.Errorf("%s: after one transient error: %s %q, broken %d times, %+v, want 1 attempt failed at %v: %s", name,
			health, reason, broken, r, l.business.Now(), methods(l.rec.Calls()))
	}
	return r
}
