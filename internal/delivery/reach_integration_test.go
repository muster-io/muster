// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"testing"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/routing"
)

// rejoinSetup publishes an Alert Group name of the Route db in its two Destinations, both for any, then takes the second
// off the Route and, when made, lets the worker make its final edit, then names it again for critical, which the
// Alert Group, below critical, does not meet. It returns the Alert Group.
func rejoinSetup(t *testing.T, l *live, name string, made bool) string {
	t.Helper()
	l.fresh(t, 1000, 1)
	l.attach(t, l.dests[0], l.dests[1])
	l.a.Published = l.groups.MarkPublished
	l.fire(t, name, name+"/a")
	gid := l.group(t, name)
	l.round(t, l.a)
	if r := l.row(t, gid, l.dests[1]); !r.message {
		t.Fatalf("not published in the second destination: %+v", r)
	}
	route := routePublicIDOf(t, l)
	if _, err := l.setDestinations(t, route, "DSAAAAAAAAAAA1"); err != nil {
		t.Fatal(err)
	}
	if made {
		l.round(t, l.a)
		if r := l.row(t, gid, l.dests[1]); r.state != "retired" {
			t.Fatalf("after the final edit: %+v", r)
		}
	}
	rt, err := l.routes.Get(t.Context(), route)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.routes.Update(t.Context(), system, route, nil, routing.Input{Name: rt.Name,
		Description: &rt.Description, Matchers: rt.Matchers, GroupKey: rt.GroupKey, Policy: rt.Policy,
		DestinationEntries: &[]routing.DestinationEntry{{DestinationID: "DSAAAAAAAAAAA1",
			Condition: routing.ConditionAny}, {DestinationID: "DSAAAAAAAAAAA2", Condition: routing.ConditionCritical}}}); err != nil {
		t.Fatal(err)
	}
	l.rec.Reset()
	return gid
}

// dump is the delivery of the Alert Group in the Destination as the log of a failure shows it.
func (l *live) dump(t *testing.T, gid string, dest int64) string {
	t.Helper()
	var out string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT format('state %s retire %s message %s published %s started %s
		desired %s actual %s', d.state, d.desired_retire, d.message_id, d.published_at, d.publication_started_at,
		d.desired_version, d.actual_version)
		FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
		WHERE g.public_id = $1 AND d.destination_id = $2`, gid, dest).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// joinedIn counts the destination_joined entries of the Alert Group for the Destination.
func (l *live) joinedIn(t *testing.T, gid string, dest int64) int64 {
	t.Helper()
	return l.count(t, `SELECT count(*) FROM timeline_entries x JOIN alert_groups g ON g.id = x.alert_group_id
		WHERE g.public_id = $1 AND x.event = 'destination_joined' AND x.destination_id = $2`, gid, dest)
}

// TestRejoinAfterFinalEdit is C-26.FR-4 and FR-5 after C-11.FR-14, through the dispatcher and the reach as PostgreSQL
// reads it (ListReachEntries): a Destination whose delivery got its final edit, and that the Alert Group reaches again
// when a critical Alert raises it, gets destination_joined and a new Loud Publication with new_alert_group; one whose
// final edit still waits keeps its Root message, gets no destination_joined, and is edited to the current state,
// Quietly, without the final edit.
func TestRejoinAfterFinalEdit(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		gid := rejoinSetup(t, l, "Rejoin", true)
		l.fireAlerts(t, "Rejoin", alert{name: "Rejoin/a", team: "db"},
			alert{name: "Rejoin/b", team: "db", severity: "critical"})
		if n := l.joinedIn(t, gid, l.dests[1]); n != 1 {
			t.Errorf("%d destination_joined for the destination reached again\n%s", n, l.log)
		}
		l.round(t, l.a)
		var published []deliverytest.Call
		for _, c := range l.rec.Calls() {
			if c.Destination.ID == l.dests[1] && c.Method == deliverytest.MethodPublish {
				published = append(published, c)
			}
		}
		if len(published) != 1 || published[0].Loudness != groups.Loud || len(published[0].Mentions) == 0 {
			t.Errorf("the publication in the destination reached again: %+v", published)
		}

		gid = rejoinSetup(t, l, "Waiting", false)
		l.fireAlerts(t, "Waiting", alert{name: "Waiting/a", team: "db"},
			alert{name: "Waiting/b", team: "db", severity: "critical"})
		if n := l.joinedIn(t, gid, l.dests[1]); n != 0 {
			t.Errorf("%d destination_joined for a root message still there", n)
		}
		before := l.dump(t, gid, l.dests[1])
		if r := l.row(t, gid, l.dests[1]); r.retire || !r.message {
			t.Errorf("the waiting final edit was not cancelled: %s", before)
		}
		l.round(t, l.a)
		for _, c := range l.rec.Calls() {
			if c.Destination.ID == l.dests[1] && (c.Method != deliverytest.MethodUpdate || c.Loudness != groups.Quiet) {
				t.Errorf("a call to the destination kept: %s %s; before the round: %s", c.Method, c.Loudness, before)
			}
		}
	})
}

// TestDeletedDestinationWithheld is C-11.FR-14 against a delivery created in a Destination while its deletion
// committed: the worker claims it, never publishes it, and withholds it without a call.
func TestDeletedDestinationWithheld(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		l.fresh(t, 1000, 1)
		l.fire(t, "late", "Late/a")
		gid := l.group(t, "Late")
		if r := l.row(t, gid, l.dests[0]); r.state != "pending" || r.message {
			t.Fatalf("not a pending first publication: %+v", r)
		}
		// As the deletion leaves it, its other deliveries ended before this one was created.
		l.exec(t, `UPDATE destinations SET deleted_at = $2 WHERE id = $1`, l.dests[0], l.business.Now())
		l.round(t, l.a)
		if r := l.row(t, gid, l.dests[0]); r.state != "withheld" || r.message {
			t.Errorf("the delivery in the deleted destination: %+v", r)
		}
		if calls := l.rec.Calls(); len(calls) != 0 {
			t.Errorf("calls to the deleted destination: %+v", calls)
		}
	})
}
