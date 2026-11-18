// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// TestIntegrationNewDesiredKeepsTransientBackoff covers C-11.FR-8 on PostgreSQL (issue #630): a pending delivery that
// waits out the backoff of a Transient error stays due at that time after a new Alert and after a re-render, and its
// next attempt sends the newest Desired state; a delivery without a failed attempt, and one that is not pending, is
// due at once after a new Desired state.
func TestIntegrationNewDesiredKeepsTransientBackoff(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		l.fresh(t, 1000, 1)
		l.fire(t, "backoff", "Backoff/a")
		gid := l.group(t, "Backoff")
		l.round(t, l.a)
		type row struct {
			state                 string
			attempts, version     int64
			actualVersion         int64
			next                  time.Time
			desiredHash, actualOf []byte
		}
		read := func() row {
			t.Helper()
			var r row
			if err := l.d.Pool.QueryRow(t.Context(), `SELECT d.state, d.attempts, d.desired_version,
				coalesce(d.actual_version, 0), d.next_attempt_at, d.desired_hash, d.actual_hash
				FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
				WHERE g.public_id = $1 AND d.destination_id = $2`, gid, l.dests[0]).Scan(&r.state, &r.attempts,
				&r.version, &r.actualVersion, &r.next, &r.desiredHash, &r.actualOf); err != nil {
				t.Fatal(err)
			}
			return r
		}
		if r := read(); r.state != "delivered" {
			t.Fatalf("the first publication: %+v, %s", r, methods(l.rec.Calls()))
		}

		l.business.Advance(time.Minute)
		l.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeTransient, "503"))
		l.fire(t, "backoff", "Backoff/a", "Backoff/b")
		l.round(t, l.a)
		failed := read()
		backoffAt := l.business.Now().Add(delivery.TransientFirstStep)
		if failed.state != "pending" || failed.attempts != 1 || !failed.next.Equal(backoffAt) {
			t.Fatalf("after the transient error: %+v, want due at %v", failed, backoffAt)
		}
		updates := l.rec.Count(deliverytest.MethodUpdate)

		l.fire(t, "backoff", "Backoff/a", "Backoff/b", "Backoff/c")
		if r := read(); r.state != "pending" || r.attempts != 1 || r.version <= failed.version ||
			!r.next.Equal(backoffAt) {
			t.Errorf("a new alert during the backoff: %+v, want due at %v", r, backoffAt)
		}
		active := l.rotate(t)
		if n, err := l.svc.RerenderOpen(t.Context(), active); err != nil || n != 1 {
			t.Fatalf("re-rendered %d, %v", n, err)
		}
		rerendered := read()
		if rerendered.state != "pending" || rerendered.attempts != 1 || !rerendered.next.Equal(backoffAt) {
			t.Errorf("a re-render during the backoff: %+v, want due at %v", rerendered, backoffAt)
		}
		l.round(t, l.a)
		if n := l.rec.Count(deliverytest.MethodUpdate); n != updates {
			t.Errorf("%d edits before the backoff ran out: %s", n-updates, methods(l.rec.Calls()))
		}

		l.business.Set(backoffAt)
		l.round(t, l.a)
		sent := read()
		calls := l.rec.Calls()
		last := calls[len(calls)-1]
		if l.rec.Count(deliverytest.MethodUpdate) != updates+1 || last.Method != deliverytest.MethodUpdate ||
			len(last.Message.Buttons) == 0 || last.Message.Buttons[0].KeyID != active {
			t.Errorf("at the backoff: %s, the last call %+v", methods(calls), last)
		}
		if sent.state != "delivered" || sent.attempts != 0 || sent.version != rerendered.version ||
			sent.actualVersion != rerendered.version || !bytes.Equal(sent.actualOf, rerendered.desiredHash) {
			t.Errorf("the attempt at the backoff did not send the newest desired state: %+v, want version %d",
				sent, rerendered.version)
		}

		// Without a failed attempt, a new Desired state is due now, even when the pending delivery waited for a
		// later time, such as a limiter token.
		l.business.Advance(time.Minute)
		l.fire(t, "backoff", "Backoff/a", "Backoff/b", "Backoff/c", "Backoff/d")
		if r := read(); r.state != "pending" || !r.next.Equal(l.business.Now()) {
			t.Errorf("a delivered row without a failed attempt: %+v, want due at %v", r, l.business.Now())
		}
		l.exec(t, `UPDATE deliveries SET next_attempt_at = $1 WHERE destination_id = $2`,
			l.business.Now().Add(time.Hour), l.dests[0])
		l.fire(t, "backoff", "Backoff/a", "Backoff/b", "Backoff/c", "Backoff/d", "Backoff/e")
		if r := read(); r.state != "pending" || !r.next.Equal(l.business.Now()) {
			t.Errorf("a pending row without a failed attempt: %+v, want due at %v", r, l.business.Now())
		}
		l.round(t, l.a)

		// A delivery that is not pending is due now, whatever attempts it kept.
		names := []string{"Backoff/a", "Backoff/b", "Backoff/c", "Backoff/d", "Backoff/e"}
		for i, state := range []string{"delivered", "not_delivered"} {
			l.business.Advance(time.Minute)
			l.exec(t, `UPDATE deliveries SET state = $1, attempts = 3, next_attempt_at = $2,
				last_error_class = CASE WHEN $1 = 'not_delivered' THEN 'unknown' END,
				last_error = CASE WHEN $1 = 'not_delivered' THEN 'unknown response' END
				WHERE destination_id = $3`, state, l.business.Now().Add(time.Hour), l.dests[0])
			names = append(names, "Backoff/n"+string(rune('a'+i)))
			l.fire(t, "backoff", names...)
			if r := read(); r.state != "pending" || !r.next.Equal(l.business.Now()) {
				t.Errorf("a %s row with attempts: %+v, want due at %v", state, r, l.business.Now())
			}
		}
	})
}

// TestIntegrationMembershipChangesKeepTransientBackoff covers C-11.FR-8 with C-11.FR-14, C-26.FR-6 and C-09.FR-19 on
// PostgreSQL (issue #641): a pending delivery that waits out the backoff of a Transient error stays due at that time
// when its Destination leaves the Route, when its Alert Group moves to the Default route, when its Destination is
// deleted and when its Destination joins the Route again before the final edit went out; its next attempt sends the
// final edit, or after the rejoin the Desired state. So does a Publication whose answer was lost, which may exist. A
// delivery without a failed attempt is due at once, even when it waited for a later time, and so is one that leaves
// withheld.
func TestIntegrationMembershipChangesKeepTransientBackoff(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		for i, c := range []struct {
			team   string
			change func(t *testing.T, m *membershipBackoff)
		}{
			{"removed", func(t *testing.T, m *membershipBackoff) {
				if _, err := l.setDestinations(t, m.route); err != nil {
					t.Fatal(err)
				}
			}},
			{"moved", func(t *testing.T, m *membershipBackoff) {
				if n, err := l.groups.MoveOpenAlertGroups(t.Context(), groups.Requester{Actor: audit.System,
					Transport: audit.TransportSystem}, m.route); err != nil || n != 3 {
					t.Fatalf("moved %d, %v", n, err)
				}
			}},
			{"deleted", func(t *testing.T, m *membershipBackoff) { l.deleteDest(t, m.dest) }},
		} {
			m := newMembershipBackoff(t, l, i+3, c.team)
			c.change(t, m)
			now := l.business.Now()
			if r := m.read(t, m.x); r.state != "pending" || !r.retire || r.attempts != 1 || !r.next.Equal(m.at) {
				t.Errorf("%s: the delivery waiting out the backoff %+v, want due at %v", c.team, r, m.at)
			}
			if r := m.read(t, m.y); r.state != "pending" || !r.retire || r.attempts != 0 || !r.next.Equal(now) {
				t.Errorf("%s: the delivery without a failed attempt %+v, want due at %v", c.team, r, now)
			}
			if r := m.read(t, m.z); r.state != "withheld" || !r.next.Equal(now) {
				t.Errorf("%s: the delivery never published %+v, want withheld at %v", c.team, r, now)
			}
			l.round(t, l.a)
			if calls := l.rec.Calls(); !finalEdits(calls, m.y) {
				t.Errorf("%s: before the backoff ran out: %s", c.team, methods(calls))
			}
			l.rec.Reset()
			l.business.Set(m.at)
			l.round(t, l.a)
			if calls := l.rec.Calls(); !finalEdits(calls, m.x) || l.state(t, m.x, m.destID) != "retired" {
				t.Errorf("%s: at the backoff: %s, %s", c.team, methods(calls), l.state(t, m.x, m.destID))
			}
		}

		// A Publication whose answer was lost may exist (C-22.FR-6): its Destination leaving the Route keeps the
		// delivery pending for the final edit, due at its backoff, which then publishes the final message under the
		// same key.
		l.fresh(t, 1000, 1)
		st := &membershipBackoff{l: l, team: "started", dest: "DSAAAAAAAAAAA7", destID: l.newDest(t, 7)}
		_, st.route = l.newRoute(t, st.team, 1_000_000, st.dest)
		bridge := l.worker("r3")
		bridge.Adapters = delivery.Adapters{delivery.TypeMattermost: idempotent(l.rec)}
		l.rec.Script(deliverytest.MethodPublish, timedOut())
		st.fire(t, "X", "a")
		l.round(t, bridge)
		st.x, st.at = l.group(t, "startedX"), l.business.Now().Add(delivery.TransientFirstStep)
		if r := st.read(t, st.x); r.state != "pending" || r.attempts != 1 || !r.next.Equal(st.at) ||
			l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND published_at IS NULL
				AND publication_started_at IS NOT NULL`, st.destID) != 1 {
			t.Fatalf("after the lost answer: %+v, want due at %v with its start kept", r, st.at)
		}
		l.rec.Reset()
		if _, err := l.setDestinations(t, st.route); err != nil {
			t.Fatal(err)
		}
		if r := st.read(t, st.x); r.state != "pending" || !r.retire || r.attempts != 1 || !r.next.Equal(st.at) {
			t.Errorf("started: the delivery waiting out the backoff %+v, want due at %v", r, st.at)
		}
		l.round(t, bridge)
		if calls := l.rec.Calls(); len(calls) != 0 {
			t.Errorf("started: before the backoff ran out: %s", methods(calls))
		}
		l.business.Set(st.at)
		l.round(t, bridge)
		final := l.rec.Calls()
		if len(final) != 1 || final[0].Method != deliverytest.MethodPublish || final[0].Loudness != groups.Quiet ||
			len(final[0].Message.Buttons) != 0 || !slices.Contains(final[0].Message.Notices, finalLink(st.x)) ||
			l.state(t, st.x, st.destID) != "retired" {
			t.Errorf("started, at the backoff: %s %+v, %s", methods(final), final, l.state(t, st.x, st.destID))
		}

		// The Destination joins the Route again while the final edits wait: the delivery waiting out the backoff is
		// edited to the current state at the backoff, the others at once, and the one never published is published
		// afresh at once, whatever its due time.
		m := newMembershipBackoff(t, l, 6, "rejoined")
		if _, err := l.setDestinations(t, m.route); err != nil {
			t.Fatal(err)
		}
		if r := m.read(t, m.x); r.state != "pending" || !r.retire || !r.next.Equal(m.at) {
			t.Errorf("the final edit waiting out the backoff: %+v, want due at %v", r, m.at)
		}
		later := l.business.Now().Add(time.Hour)
		for _, id := range []string{m.y, m.z} {
			l.exec(t, `UPDATE deliveries d SET next_attempt_at = $1 FROM alert_groups g
				WHERE g.id = d.alert_group_id AND g.public_id = $2`, later, id)
		}
		if _, err := l.setDestinations(t, m.route, m.dest); err != nil {
			t.Fatal(err)
		}
		now := l.business.Now()
		if r := m.read(t, m.x); r.state != "pending" || r.retire || r.attempts != 1 || !r.next.Equal(m.at) {
			t.Errorf("rejoined: the delivery waiting out the backoff %+v, want due at %v", r, m.at)
		}
		for _, id := range []string{m.y, m.z} {
			if r := m.read(t, id); r.state != "pending" || r.retire || !r.next.Equal(now) {
				t.Errorf("rejoined: %s %+v, want due at %v", id, r, now)
			}
		}
		l.round(t, l.a)
		calls := l.rec.Calls()
		if len(calls) != 2 || l.rec.Count(deliverytest.MethodPublish) != 1 ||
			slices.ContainsFunc(calls, func(c deliverytest.Call) bool { return isFinalEdit(c, m.y) }) {
			t.Errorf("rejoined, before the backoff ran out: %s", methods(calls))
		}
		if r := m.read(t, m.x); r.state != "pending" || r.attempts != 1 {
			t.Errorf("rejoined: attempted before the backoff ran out: %+v", r)
		}
		l.rec.Reset()
		l.business.Set(m.at)
		l.round(t, l.a)
		calls = l.rec.Calls()
		sent := m.read(t, m.x)
		if len(calls) != 1 || calls[0].Method != deliverytest.MethodUpdate || isFinalEdit(calls[0], m.x) ||
			len(calls[0].Message.Buttons) == 0 || sent.state != "delivered" || sent.actual != sent.version {
			t.Errorf("rejoined, at the backoff: %s, %+v", methods(calls), sent)
		}
	})
}

// membershipBackoff is a Route with a Destination of its own and three Alert Groups there while the messenger fails,
// before a change of the Destination's membership (issue #641): the edit of x failed Transiently and waits out the
// first backoff step, until at; y is published and waits, without a failed attempt, for a later time, as for a limiter
// token; the first Publication of z failed Transiently, so it was never published.
type membershipBackoff struct {
	l                 *live
	team, route, dest string
	destID            int64
	x, y, z           string
	at                time.Time
}

// backoffRow is the delivery of an Alert Group in the Destination of a membershipBackoff.
type backoffRow struct {
	state           string
	retire          bool
	attempts        int64
	version, actual int64
	next            time.Time
}

// newMembershipBackoff starts from no delivery with the Route team, for the Alerts whose team is team, and the
// Destination DSAAAAAAAAAAA<n> on it alone.
func newMembershipBackoff(t *testing.T, l *live, n int, team string) *membershipBackoff {
	t.Helper()
	l.fresh(t, 1000, 1)
	m := &membershipBackoff{l: l, team: team, dest: fmt.Sprintf("DSAAAAAAAAAAA%d", n), destID: l.newDest(t, n)}
	_, m.route = l.newRoute(t, team, 1_000_000, m.dest)
	m.fire(t, "X", "a")
	m.fire(t, "Y", "a")
	l.round(t, l.a)
	m.x, m.y = l.group(t, team+"X"), l.group(t, team+"Y")
	if n := l.rec.Count(deliverytest.MethodPublish); n != 2 {
		t.Fatalf("%s: the first publications: %s", team, methods(l.rec.Calls()))
	}
	l.business.Advance(time.Minute)
	l.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeTransient, "503"))
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
	m.fire(t, "X", "a", "b")
	m.fire(t, "Z", "a")
	l.round(t, l.a)
	m.z = l.group(t, team+"Z")
	m.at = l.business.Now().Add(delivery.TransientFirstStep)
	for _, id := range []string{m.x, m.z} {
		if r := m.read(t, id); r.state != "pending" || r.attempts != 1 || !r.next.Equal(m.at) {
			t.Fatalf("%s: after the transient error: %+v, want due at %v", team, r, m.at)
		}
	}
	m.fire(t, "Y", "a", "b")
	l.exec(t, `UPDATE deliveries d SET next_attempt_at = $1 FROM alert_groups g
		WHERE g.id = d.alert_group_id AND g.public_id = $2`, l.business.Now().Add(time.Hour), m.y)
	l.rec.Reset()
	return m
}

// fire fires the Alerts of the Alert Group ag, one per disk.
func (m *membershipBackoff) fire(t *testing.T, ag string, disks ...string) {
	t.Helper()
	alerts := make([]alert, len(disks))
	for i, d := range disks {
		alerts[i] = alert{name: m.team + ag + "/" + d, team: m.team}
	}
	m.l.fireAlerts(t, m.team+ag, alerts...)
}

func (m *membershipBackoff) read(t *testing.T, publicID string) backoffRow {
	t.Helper()
	var r backoffRow
	if err := m.l.d.Pool.QueryRow(t.Context(), `SELECT d.state, d.desired_retire, d.attempts, d.desired_version,
		coalesce(d.actual_version, 0), d.next_attempt_at
		FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
		WHERE g.public_id = $1 AND d.destination_id = $2`, publicID, m.destID).Scan(&r.state, &r.retire, &r.attempts,
		&r.version, &r.actual, &r.next); err != nil {
		t.Fatalf("the delivery of %s: %v", publicID, err)
	}
	return r
}
