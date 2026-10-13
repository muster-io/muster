// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/routing"
	routingdb "github.com/muster-io/muster/internal/routing/dbgen"
)

// The lock order of the delivery worker (worker.go) against the dispatcher: a Command — a timer's alike — locks its
// Alert Group, then its deliveries in Enqueue, while the worker holds a delivery or a Thread reply and, through a
// foreign-key check of a row it updates a second time, takes FOR KEY SHARE on the same Alert Group. Each test runs
// the real paths of both on one Alert Group, the dispatcher held between its lock of the Alert Group and Enqueue while
// the worker runs; neither may end in a deadlock.

// TestPrepareAgainstDispatcherLockOrder: the worker prepares a first Publication — GetLeasedDelivery,
// RenewDeliveryLease, StartPublication — while an acknowledgement holds the Alert Group and then waits for the
// delivery row in Enqueue.
func TestPrepareAgainstDispatcherLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		l.fresh(t, 1000, 1)
		l.fire(t, "lockorder", "LockOrder/a")
		gid := l.group(t, "LockOrder")
		l.againstDispatcher(t, gid)
		if l.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = $1 AND d.published_at IS NOT NULL AND d.desired_version = 2`, gid) != 1 {
			t.Errorf("the publication or the acknowledgement's desired state is missing\n%s", l.log)
		}
	})
}

// TestReplyOutcomeAgainstDispatcherLockOrder: the worker records a Thread reply whose Root message was deleted a
// second time — the reply released, the delivery deleted in the messenger, the reply dropped with the rest of the
// Thread, an update of the reply's row after its delivery's — while an acknowledgement holds the Alert Group and then
// waits for the delivery row in Enqueue.
func TestReplyOutcomeAgainstDispatcherLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		l.fresh(t, 1000, 1)
		l.fire(t, "lockreply", "LockReply/a")
		gid := l.group(t, "LockReply")
		l.round(t, l.a)
		l.exec(t, `UPDATE deliveries SET republished_after_delete = true`)
		l.fire(t, "lockreply", "LockReply/a", "LockReply/b")
		l.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "message not found"))
		l.againstDispatcher(t, gid)
		if l.state(t, gid, l.dests[0]) != "deleted_in_messenger" ||
			l.count(t, `SELECT count(*) FROM thread_replies WHERE state = 'dropped'`) != 1 {
			t.Errorf("delivery %s, replies dropped %d\n%s", l.state(t, gid, l.dests[0]),
				l.count(t, `SELECT count(*) FROM thread_replies WHERE state = 'dropped'`), l.log)
		}
	})
}

// againstDispatcher acknowledges the Alert Group publicID through the dispatcher and holds that transaction after it
// locked the Alert Group, before Enqueue; meanwhile a round of the worker runs until it ends or waits for a lock; then
// the acknowledgement goes on. Both must succeed, and nothing may fail with a deadlock.
func (l *live) againstDispatcher(t *testing.T, publicID string) {
	t.Helper()
	ctx := t.Context()
	locked, proceed := make(chan struct{}), make(chan struct{})
	var held int32
	l.groups.SetRerender(func(ctx context.Context, tx groups.DBTX, r groups.Rendering) error {
		err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&held)
		close(locked)
		<-proceed
		if err != nil {
			return err
		}
		return l.svc.Enqueue(ctx, tx, r)
	})
	var wg sync.WaitGroup
	var ackErr, roundErr error
	wg.Go(func() { _, ackErr = l.groups.Acknowledge(ctx, l.alice, publicID) })
	select {
	case <-locked:
	case <-time.After(10 * time.Second):
		close(proceed)
		t.Fatal("the acknowledgement never reached Enqueue")
	}
	worked := make(chan struct{})
	wg.Go(func() {
		defer close(worked)
		_, roundErr = l.a.Round(ctx)
	})
	blocked := l.blockedOrDone(t, worked, held)
	close(proceed)
	wg.Wait()
	l.groups.SetRerender(l.svc.Enqueue)
	t.Logf("the worker waited for a lock while the dispatcher held the alert group: %v", blocked)
	if ackErr != nil {
		t.Errorf("the acknowledgement: %v", ackErr)
	}
	if roundErr != nil {
		t.Errorf("the worker's round: %v", roundErr)
	}
	l.noDeadlock(t)
}

// noDeadlock fails the test when the log has a deadlock or a failed piece of delivery work.
func (l *live) noDeadlock(t *testing.T) {
	t.Helper()
	for _, bad := range []string{"deadlock", `"event":"delivery_work_failed"`} {
		if strings.Contains(l.log.String(), bad) {
			t.Errorf("the log has %s:\n%s", bad, l.log)
		}
	}
}

// blockedOrDone waits until done is closed or a backend of the database waits for a lock that the backend held holds,
// and reports the latter.
func (l *live) blockedOrDone(t *testing.T, done <-chan struct{}, held int32) bool {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return false
		default:
		}
		var n int
		if err := l.d.Pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND $1::int4 = ANY(pg_blocking_pids(pid))`, held).Scan(&n); err != nil {
			t.Error(err)
			return true
		}
		if n > 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the work neither ended nor waited for the held transaction")
	return true
}

// TestReplyRecordAgainstDeliveryLockOrder: the worker records a Thread reply whose Root message was deleted — the reply
// released, then the delivery published again — while another transaction holds the delivery, as the dispatcher's
// EnsureDelivery does, and then drops the delivery's pending replies, as the retirement of a delivery does. The record
// locks the delivery before the reply (LockReplyDelivery), so it waits for that transaction instead of holding the
// reply it wants.
func TestReplyRecordAgainstDeliveryLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		l.fire(t, "lockrecord", "LockRecord/a")
		gid := l.group(t, "LockRecord")
		l.round(t, l.a)
		l.fire(t, "lockrecord", "LockRecord/a", "LockRecord/b")
		l.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeTransient, "busy"))
		l.round(t, l.a)
		var group, dlv int64
		if err := l.d.Pool.QueryRow(ctx, `SELECT d.alert_group_id, d.id FROM deliveries d
			JOIN alert_groups g ON g.id = d.alert_group_id WHERE g.public_id = $1`, gid).Scan(&group, &dlv); err != nil {
			t.Fatal(err)
		}
		if l.count(t, `SELECT count(*) FROM deliveries WHERE id = $1 AND state = 'delivered'`, dlv) != 1 ||
			l.count(t, `SELECT count(*) FROM thread_replies WHERE state = 'pending'`) != 1 {
			t.Fatalf("setup: the edit is not delivered or the reply not pending\n%s", l.log)
		}
		l.business.Advance(time.Hour)
		l.rec.Script(deliverytest.MethodReply, deliverytest.Failure(delivery.OutcomeGone, "message not found"))

		tx, err := l.d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		var held int32
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&held); err != nil {
			t.Fatal(err)
		}
		q := deliverydb.New(tx)
		if _, err := q.EnsureDelivery(ctx, deliverydb.EnsureDeliveryParams{OrgID: l.orgID, DestinationID: l.dests[0],
			AlertGroupID: pgtype.Int8{Int64: group, Valid: true}, Now: l.business.Now()}); err != nil {
			t.Fatal(err)
		}
		var roundErr error
		worked := make(chan struct{})
		go func() {
			defer close(worked)
			_, roundErr = l.a.Round(ctx)
		}()
		blocked := l.blockedOrDone(t, worked, held)
		dropErr := q.DropDeliveryReplies(ctx, deliverydb.DropDeliveryRepliesParams{OrgID: l.orgID,
			DeliveryIds: []int64{dlv}})
		if dropErr == nil {
			dropErr = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		<-worked
		t.Logf("the record waited for the transaction that holds the delivery: %v", blocked)
		if dropErr != nil || roundErr != nil {
			t.Errorf("the other transaction: %v; the worker's round: %v", dropErr, roundErr)
		}
		l.noDeadlock(t)
		if l.count(t, `SELECT count(*) FROM thread_replies WHERE state = 'dropped'`) != 1 ||
			l.count(t, `SELECT count(*) FROM deliveries WHERE id = $1 AND republished_after_delete`, dlv) != 1 {
			t.Errorf("the reply is not dropped or the root message not published again\n%s", l.log)
		}
	})
}

// TestMembershipAgainstStormCalmLockOrder: a Destination joins a Route during its Storm, which holds Alert Groups
// there too, while the calm check of that Storm ends it. The change of the Route locks the active Storm FOR SHARE
// before it writes the held deliveries (LockActiveStorm), so the calm check waits for it and publishes them; no
// delivery stays held by an ended Storm.
func TestMembershipAgainstStormCalmLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		routeID, route := l.newRoute(t, "lockstorm", 2, "DSAAAAAAAAAAA1")
		for _, a := range []alert{{name: "LS0/a", team: "lockstorm"}, {name: "LS1/a", team: "lockstorm"},
			{name: "LS2/a", team: "lockstorm"}, {name: "LS3/a", team: "lockstorm"}} {
			l.fireAlerts(t, "ls-"+a.name, a)
			l.business.Advance(time.Second)
		}
		var stormID int64
		if err := l.d.Pool.QueryRow(ctx, `SELECT id FROM storms WHERE route_id = $1 AND ended_at IS NULL`,
			routeID).Scan(&stormID); err != nil {
			t.Fatalf("no storm: %v", err)
		}
		if l.count(t, `SELECT count(*) FROM deliveries WHERE held_by_storm_id = $1`, stormID) == 0 {
			t.Fatal("the storm holds nothing")
		}
		l.business.Advance(delivery.StormWindow + delivery.StormCalmPeriod + time.Minute)

		changed, proceed := make(chan struct{}), make(chan struct{})
		var held int32
		l.routes.SetMembership(func(ctx context.Context, tx routingdb.DBTX, id int64, added, removed []int64) error {
			err := l.svc.RouteDestinationsChanged(ctx, tx, id, added, removed)
			if err == nil {
				err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&held)
			}
			close(changed)
			<-proceed
			return err
		})
		rt, err := l.routes.Get(ctx, route)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var editErr, calmErr error
		wg.Go(func() {
			_, editErr = l.routes.Update(ctx, system, route, &rt.Version, routing.Input{Name: rt.Name,
				Description: &rt.Description, Matchers: rt.Matchers, Urgent: rt.Urgent, GroupKey: rt.GroupKey,
				DestinationIDs: []string{"DSAAAAAAAAAAA1", "DSAAAAAAAAAAA2"}, Policy: rt.Policy})
		})
		select {
		case <-changed:
		case <-time.After(10 * time.Second):
			close(proceed)
			t.Fatal("the route change never reached the membership hook")
		}
		calmed := make(chan struct{})
		wg.Go(func() {
			defer close(calmed)
			tx, err := l.d.Pool.Begin(ctx)
			if err != nil {
				calmErr = err
				return
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			if _, calmErr = l.svc.CheckStormCalm(ctx, tx, stormID); calmErr == nil {
				calmErr = tx.Commit(ctx)
			}
		})
		blocked := l.blockedOrDone(t, calmed, held)
		close(proceed)
		wg.Wait()
		l.routes.SetMembership(func(ctx context.Context, tx routingdb.DBTX, id int64, added, removed []int64) error {
			return l.svc.RouteDestinationsChanged(ctx, tx, id, added, removed)
		})
		t.Logf("the calm check waited for the route change: %v", blocked)
		if editErr != nil || calmErr != nil {
			t.Errorf("the route change: %v; the calm check: %v", editErr, calmErr)
		}
		ended := l.count(t, `SELECT count(*) FROM storms WHERE id = $1 AND ended_at IS NOT NULL`, stormID)
		stuck := l.count(t, `SELECT count(*) FROM deliveries d JOIN storms s ON s.id = d.held_by_storm_id
			WHERE s.ended_at IS NOT NULL`)
		joined := l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND alert_group_id IS NOT NULL`,
			l.dests[1])
		if ended != 1 || stuck != 0 || joined == 0 {
			t.Errorf("storm ended %d, deliveries held by an ended storm %d, joined deliveries %d", ended, stuck, joined)
		}
	})
}
