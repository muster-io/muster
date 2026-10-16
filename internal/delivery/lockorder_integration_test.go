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
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
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

// TestReplayedPublicationAgainstDispatcherLockOrder (D307, C-22.FR-6): the worker retries a first Publication to an
// Idempotent adapter whose earlier attempt got no answer, and the bridge replays it — the outcome records the message,
// the thread_ref and the first attempt's start as published_at, and stays pending — while an acknowledgement holds the
// Alert Group and then waits for the delivery row in Enqueue; the first Publication is then marked through the
// dispatcher, and the edit that follows brings the Root message to the acknowledged state. Neither the outcome nor the
// edit locks the Alert Group before the delivery: nothing ends in a deadlock, no possible duplicate is recorded, and the
// Alert Group's first Publication is the first attempt's start.
func TestReplayedPublicationAgainstDispatcherLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		l.fresh(t, 1000, 1)
		l.fire(t, "lockreplay", "LockReplay/a")
		gid := l.group(t, "LockReplay")
		idem := idempotent(l.rec)
		l.a.Adapters[delivery.TypeMattermost] = idem
		l.a.Published = l.groups.MarkPublished
		started := l.business.Now().Add(-time.Minute).UTC()
		l.exec(t, `UPDATE deliveries SET publication_started_at = $1`, started)
		l.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
			MessageID: "m-replayed", ThreadRef: "t-1", Replayed: true}})
		l.againstDispatcher(t, gid)
		if l.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = $1 AND d.state = 'pending' AND d.actual_hash IS NULL AND d.message_id = 'm-replayed'
			AND d.thread_anchor_id = 't-1' AND d.published_at = $2 AND g.first_published_at = $2
			AND NOT d.possible_duplicate`, gid, started) != 1 {
			t.Errorf("the replayed publication is not recorded from its first attempt\n%s", l.log)
		}
		l.round(t, l.a)
		l.noDeadlock(t)
		calls := l.rec.Calls()
		if callMethods(calls) != "publish,update" || calls[1].MessageID != "m-replayed" ||
			calls[1].Message.Colour != "acknowledged" {
			t.Errorf("calls %s %+v", callMethods(calls), calls)
		}
		if keys := idem.Keys(); len(keys) != 2 || !strings.HasPrefix(keys[0], "pub-DSAAAAAAAAAAA1-") ||
			!strings.HasSuffix(keys[0], "-1") || keys[1] != "" {
			t.Errorf("keys %v", keys)
		}
		if l.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = $1 AND g.status = 'acknowledged' AND d.state = 'delivered' AND d.desired_version >= 2
			AND d.actual_version = d.desired_version AND d.actual_hash = d.desired_hash AND d.published_at = $2
			AND NOT d.possible_duplicate`, gid, started) != 1 ||
			l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'possible_duplicate'`) != 0 {
			t.Errorf("the edit did not bring the root message to the acknowledged state\n%s", l.log)
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

// gateRenderer holds the first render through it — of the Alert Group only when only is set — after reporting the
// backend of its transaction, until proceed is closed: the re-render after a rotation holding its Alert Group, the
// Route's membership lock and the deliveries.
type gateRenderer struct {
	delivery.Renderer
	once     sync.Once
	only     int64
	pid      int32
	entered  chan struct{}
	proceed  chan struct{}
	pidError error
}

func (g *gateRenderer) Roots(ctx context.Context, db messages.DBTX, v delivery.GroupView, markups []messages.Markup) (
	delivery.Roots, error) {
	if g.only != 0 && v.ID != g.only {
		return g.Renderer.Roots(ctx, db, v, markups)
	}
	g.once.Do(func() {
		g.pidError = db.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&g.pid)
		close(g.entered)
		<-g.proceed
	})
	return g.Renderer.Roots(ctx, db, v, markups)
}

// gateMentions holds the first resolution of Mentions, inside the worker's preparation of a Publication, which holds
// its delivery and updated it twice, until proceed is closed.
type gateMentions struct {
	delivery.Mentioner
	once    sync.Once
	pid     int32
	entered chan struct{}
	proceed chan struct{}
}

func (g *gateMentions) Resolve(ctx context.Context, db mentions.DBTX, org int64, r mentions.Request) (
	[]mentions.Target, error) {
	g.once.Do(func() {
		_ = db.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&g.pid)
		close(g.entered)
		<-g.proceed
	})
	return g.Mentioner.Resolve(ctx, db, org, r)
}

// waitFor waits until c is closed.
func waitFor(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never happened", what)
	}
}

// TestRerenderAgainstDispatcherLockOrder covers the lock order of the re-render after a rotation (D307, S-071): it
// locks the Alert Group first, SKIP LOCKED, then the Route's membership lock, then the deliveries. Against an
// acknowledgement of the same Alert Group that holds it, it skips the Alert Group without waiting, and the
// acknowledgement signs with the active key; holding the Alert Group itself, the acknowledgement waits for it; against
// the worker preparing a Publication or an edit of its delivery, which holds the delivery and through a second update
// takes FOR KEY SHARE on the Alert Group, it waits for the delivery. Nothing ends in a deadlock.
func TestRerenderAgainstDispatcherLockOrder(t *testing.T) {
	setup := func(t *testing.T, s dbtest.Server, publish bool) (*live, string, string) {
		t.Helper()
		l := setupLive(t, s)
		l.fresh(t, 1000, 1)
		l.fire(t, "lockrerender", "LockRerender/a")
		if publish {
			l.round(t, l.a)
		}
		return l, l.group(t, "LockRerender"), l.rotate(t)
	}
	keyOf := func(t *testing.T, l *live, gid string) string {
		t.Helper()
		var key string
		if err := l.d.Pool.QueryRow(t.Context(), `SELECT d.desired_button_key_id FROM deliveries d
			JOIN alert_groups g ON g.id = d.alert_group_id WHERE g.public_id = $1`, gid).Scan(&key); err != nil {
			t.Fatal(err)
		}
		return key
	}
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		t.Run("a command holds the alert group", func(t *testing.T) {
			l, gid, active := setup(t, s, true)
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
			var ackErr, rerenderErr error
			var n int
			wg.Go(func() { _, ackErr = l.groups.Acknowledge(ctx, l.alice, gid) })
			waitFor(t, locked, "the acknowledgement's Enqueue")
			done := make(chan struct{})
			wg.Go(func() {
				defer close(done)
				n, rerenderErr = l.svc.RerenderOpen(ctx, active)
			})
			blocked := l.blockedOrDone(t, done, held)
			close(proceed)
			wg.Wait()
			l.groups.SetRerender(l.svc.Enqueue)
			if blocked || n != 0 || ackErr != nil || rerenderErr != nil {
				t.Errorf("the re-render waited %v, re-rendered %d, %v; the acknowledgement: %v", blocked, n,
					rerenderErr, ackErr)
			}
			if key := keyOf(t, l, gid); key != active {
				t.Errorf("the acknowledgement signed with %s", key)
			}
			if n, err := l.svc.RerenderOpen(ctx, active); err != nil || n != 0 {
				t.Errorf("the next run re-rendered %d, %v", n, err)
			}
			l.noDeadlock(t)
		})
		t.Run("the re-render holds the alert group", func(t *testing.T) {
			l, gid, active := setup(t, s, true)
			ctx := t.Context()
			gate := &gateRenderer{Renderer: delivery.MessageRenderer{Renderer: l.renderer}, entered: make(chan struct{}),
				proceed: make(chan struct{})}
			svc := delivery.New(delivery.Config{OrgID: l.orgID, Store: delivery.NewStore(l.d.Pool, l.d.Pool),
				Business: l.business, Log: l.logger, Renderer: gate})
			var wg sync.WaitGroup
			var ackErr, rerenderErr, roundErr error
			var n int
			wg.Go(func() { n, rerenderErr = svc.RerenderOpen(ctx, active) })
			waitFor(t, gate.entered, "the re-render's render")
			acked, worked := make(chan struct{}), make(chan struct{})
			wg.Go(func() {
				defer close(acked)
				_, ackErr = l.groups.Acknowledge(ctx, l.alice, gid)
			})
			blocked := l.blockedOrDone(t, acked, gate.pid)
			wg.Go(func() {
				defer close(worked)
				_, roundErr = l.a.Round(ctx)
			})
			waitFor(t, worked, "the worker's round") // the claim skips the locked delivery
			close(gate.proceed)
			wg.Wait()
			if !blocked || gate.pidError != nil || n != 1 || ackErr != nil || rerenderErr != nil || roundErr != nil {
				t.Errorf("the acknowledgement waited %v; re-rendered %d, %v; acknowledgement %v; round %v", blocked, n,
					rerenderErr, ackErr, roundErr)
			}
			if key := keyOf(t, l, gid); key != active {
				t.Errorf("signed with %s", key)
			}
			l.noDeadlock(t)
		})
		t.Run("the worker prepares a publication", func(t *testing.T) {
			l, gid, active := setup(t, s, false)
			ctx := t.Context()
			gate := &gateMentions{Mentioner: l.mentions, entered: make(chan struct{}), proceed: make(chan struct{})}
			l.a.Mentions = gate
			var wg sync.WaitGroup
			var rerenderErr, roundErr error
			var n int
			wg.Go(func() { _, roundErr = l.a.Round(ctx) })
			waitFor(t, gate.entered, "the worker's preparation")
			done := make(chan struct{})
			wg.Go(func() {
				defer close(done)
				n, rerenderErr = l.svc.RerenderOpen(ctx, active)
			})
			blocked := l.blockedOrDone(t, done, gate.pid)
			close(gate.proceed)
			wg.Wait()
			if !blocked || n != 1 || rerenderErr != nil || roundErr != nil {
				t.Errorf("the re-render waited %v; re-rendered %d, %v; round %v", blocked, n, rerenderErr, roundErr)
			}
			l.round(t, l.a)
			if key := keyOf(t, l, gid); key != active || l.count(t, `SELECT count(*) FROM deliveries
				WHERE actual_button_key_id = $1 AND state = 'delivered'`, active) != 1 {
				t.Errorf("signed with %s\n%s", key, l.log)
			}
			l.noDeadlock(t)
		})
		t.Run("an edit is prepared", func(t *testing.T) {
			l, gid, active := setup(t, s, true)
			ctx := t.Context()
			tx, err := l.d.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			var held int32
			var id int64
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&held); err != nil {
				t.Fatal(err)
			}
			// As prepare does: the delivery locked, then updated twice, which locks its Alert Group FOR KEY SHARE.
			if err := tx.QueryRow(ctx, `SELECT d.id FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
				WHERE g.public_id = $1 FOR NO KEY UPDATE OF d`, gid).Scan(&id); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := tx.Exec(ctx, `UPDATE deliveries SET updated_at = updated_at WHERE id = $1`, id); err != nil {
					t.Fatal(err)
				}
			}
			var n int
			var rerenderErr error
			done := make(chan struct{})
			go func() {
				defer close(done)
				n, rerenderErr = l.svc.RerenderOpen(ctx, active)
			}()
			blocked := l.blockedOrDone(t, done, held)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			<-done
			if !blocked || n != 1 || rerenderErr != nil {
				t.Errorf("the re-render waited %v; re-rendered %d, %v", blocked, n, rerenderErr)
			}
			l.noDeadlock(t)
		})
	})
}

// TestRerenderAgainstStormCalmLockOrder: the re-render after a rotation and the calm check of a Storm that holds an
// Alert Group in two Destinations both lock that Alert Group's held deliveries by Destination — the calm check by
// Alert Group, then by Destination (ReleaseHeldOpen) — so whichever holds them first, the other waits and nothing ends
// in a deadlock: the Storm ends, nothing stays held, and the released deliveries carry the active key's buttons.
func TestRerenderAgainstStormCalmLockOrder(t *testing.T) {
	setup := func(t *testing.T, s dbtest.Server) (*live, int64, int64, string) {
		t.Helper()
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		routeID, _ := l.newRoute(t, "lockcalm", 2, "DSAAAAAAAAAAA1", "DSAAAAAAAAAAA2")
		for _, a := range []alert{{name: "LC0/a", team: "lockcalm"}, {name: "LC1/a", team: "lockcalm"},
			{name: "LC2/a", team: "lockcalm"}, {name: "LC3/a", team: "lockcalm"}} {
			l.fireAlerts(t, "lc-"+a.name, a)
			l.business.Advance(time.Second)
		}
		var stormID, group int64
		if err := l.d.Pool.QueryRow(ctx, `SELECT id FROM storms WHERE route_id = $1 AND ended_at IS NULL`,
			routeID).Scan(&stormID); err != nil {
			t.Fatalf("no storm: %v", err)
		}
		if err := l.d.Pool.QueryRow(ctx, `SELECT alert_group_id FROM deliveries WHERE held_by_storm_id = $1
			AND desired_button_key_id IS NOT NULL GROUP BY alert_group_id HAVING count(*) = 2
			ORDER BY alert_group_id LIMIT 1`, stormID).Scan(&group); err != nil {
			t.Fatalf("the storm holds no alert group in two destinations: %v", err)
		}
		active := l.rotate(t)
		l.business.Advance(delivery.StormWindow + delivery.StormCalmPeriod + time.Minute)
		return l, stormID, group, active
	}
	calm := func(ctx context.Context, l *live, stormID int64) error {
		tx, err := l.d.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		if _, err := l.svc.CheckStormCalm(ctx, tx, stormID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	check := func(t *testing.T, l *live, stormID, group int64, active string) {
		t.Helper()
		ended := l.count(t, `SELECT count(*) FROM storms WHERE id = $1 AND ended_at IS NOT NULL`, stormID)
		stuck := l.count(t, `SELECT count(*) FROM deliveries WHERE held_by_storm_id IS NOT NULL`)
		resigned := l.count(t, `SELECT count(*) FROM deliveries WHERE alert_group_id = $1
			AND desired_button_key_id = $2`, group, active)
		if ended != 1 || stuck != 0 || resigned != 2 {
			t.Errorf("storm ended %d, deliveries still held %d, re-signed deliveries of the alert group %d\n%s",
				ended, stuck, resigned, l.log)
		}
		if n, err := l.svc.RerenderOpen(t.Context(), active); err != nil || n != 0 {
			t.Errorf("the next run re-rendered %d, %v", n, err)
		}
		l.noDeadlock(t)
	}
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		t.Run("the re-render holds the deliveries", func(t *testing.T) {
			l, stormID, group, active := setup(t, s)
			ctx := t.Context()
			gate := &gateRenderer{Renderer: delivery.MessageRenderer{Renderer: l.renderer}, only: group,
				entered: make(chan struct{}), proceed: make(chan struct{})}
			svc := delivery.New(delivery.Config{OrgID: l.orgID, Store: delivery.NewStore(l.d.Pool, l.d.Pool),
				Business: l.business, Log: l.logger, Renderer: gate})
			var wg sync.WaitGroup
			var rerenderErr, calmErr error
			wg.Go(func() { _, rerenderErr = svc.RerenderOpen(ctx, active) })
			waitFor(t, gate.entered, "the re-render's render of the held alert group")
			calmed := make(chan struct{})
			wg.Go(func() {
				defer close(calmed)
				calmErr = calm(ctx, l, stormID)
			})
			blocked := l.blockedOrDone(t, calmed, gate.pid)
			close(gate.proceed)
			wg.Wait()
			if !blocked || gate.pidError != nil || rerenderErr != nil || calmErr != nil {
				t.Errorf("the calm check waited %v; the re-render: %v; the calm check: %v", blocked, rerenderErr,
					calmErr)
			}
			check(t, l, stormID, group, active)
		})
		t.Run("the calm check holds the deliveries", func(t *testing.T) {
			l, stormID, group, active := setup(t, s)
			ctx := t.Context()
			tx, err := l.d.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			var held int32
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&held); err != nil {
				t.Fatal(err)
			}
			if _, err := l.svc.CheckStormCalm(ctx, tx, stormID); err != nil {
				t.Fatal(err)
			}
			var rerenderErr error
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, rerenderErr = l.svc.RerenderOpen(ctx, active)
			}()
			blocked := l.blockedOrDone(t, done, held)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			<-done
			if !blocked || rerenderErr != nil {
				t.Errorf("the re-render waited %v; %v", blocked, rerenderErr)
			}
			check(t, l, stormID, group, active)
		})
	})
}
