// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
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
