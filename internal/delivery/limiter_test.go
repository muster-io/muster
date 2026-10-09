// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// queue makes n Alert Groups, each with its own delivery to the mattermost Destination.
func (e *env) queue(t *testing.T, n int) []*groups.Group {
	t.Helper()
	var out []*groups.Group
	for i := range int64(n) {
		gid := 100 + i
		e.db.groups[gid] = &fakeGroup{id: gid, publicID: fmt.Sprintf("AGAAAAAAAAA%03d", i), number: 100 + i,
			title: "g", status: "firing", route: routeID}
		g := &groups.Group{ID: gid, Number: 100 + i, RouteID: routeID, Title: "g", Status: groups.StatusFiring}
		e.enqueue(t, g, groups.System, created())
		out = append(out, g)
	}
	return out
}

// TestLimiterMixedCalls is C-11.FR-3 and AC-14: with a Destination limiter of 6 calls per minute, 6 calls in a mix of
// sends and edits pass and the next one — an edit — waits for its token, rescheduled to the time it is due plus the
// margin, never failed; at that time it passes.
func TestLimiterMixedCalls(t *testing.T) {
	e := newEnv(t)
	gs := e.queue(t, 3)
	e.round(t)
	for i, g := range gs {
		g.Title = "changed"
		e.enqueue(t, g, groups.System)
		if i == 2 {
			break
		}
	}
	// 3 publications, then 3 edits: the bucket of 6 is empty; a 4th edit must wait.
	gs[0].Title = "changed again"
	e.round(t)
	if p, u := e.rec.Count(deliverytest.MethodPublish), e.rec.Count(deliverytest.MethodUpdate); p != 3 || u != 3 {
		t.Fatalf("passed %d publications, %d updates", p, u)
	}
	e.enqueue(t, gs[0], groups.System)
	e.round(t)
	if e.rec.Count(deliverytest.MethodUpdate) != 3 {
		t.Fatal("the 7th call passed")
	}
	var waiting *fakeDelivery
	for _, d := range e.db.deliveries {
		if d.state == "pending" {
			waiting = d
		}
	}
	// One token every 10 s: due 10 s after the bucket emptied, plus the margin.
	if want := business0.Add(10*time.Second + delivery.TokenMargin); waiting == nil || !waiting.next.Equal(want) ||
		waiting.errorClass != nil || waiting.attempts != 0 {
		t.Fatalf("waiting %+v, want %v", waiting, want)
	}
	e.business.Set(waiting.next)
	e.round(t)
	if e.rec.Count(deliverytest.MethodUpdate) != 4 || waiting.state != "delivered" {
		t.Errorf("after the token: %d updates, %s", e.rec.Count(deliverytest.MethodUpdate), waiting.state)
	}
}

// TestConnectionLimiter is C-11.FR-3 and FR-8: a delivery also waits for its Connection's limiter, which an outgoing
// webhook does not have; a RetryAfter scoped to the Connection holds every Destination of the Connection.
func TestConnectionLimiter(t *testing.T) {
	e := newEnv(t)
	conn := int64(connID)
	e.db.dests[13] = &fakeDest{id: 13, publicID: "DSAAAAAAAAAA13", name: "ops-2", typ: delivery.TypeMattermost,
		connection: &conn, health: "healthy", limit: 600, per: 60}
	e.db.dests[destMM].limit = 600
	e.db.connLimits[connID] = [2]int64{2, 60}
	e.db.routeDests[routeID] = []int64{destMM, 13, destWH}
	e.queue(t, 2)
	e.round(t)
	perDest := map[string]int{}
	for _, c := range e.rec.Calls() {
		perDest[c.Destination.PublicID]++
	}
	// 4 deliveries through the Connection share its 2 tokens; the webhook's 2 have no Connection.
	if perDest["DSAAAAAAAAAA11"]+perDest["DSAAAAAAAAAA13"] != 2 || perDest["DSAAAAAAAAAA12"] != 2 {
		t.Fatalf("calls %v", perDest)
	}
	e.db.connLimits[connID] = [2]int64{100, 60}
	e.db.buckets = map[bucketKey]*fakeBucket{}
	for _, d := range e.db.deliveries {
		d.next = business0
	}
	e.rec.Reset()
	e.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(3*time.Second, delivery.ScopeConnection))
	e.round(t)
	held := e.db.buckets[bucketKey{"connection", connID}]
	if held == nil || !held.refilled.Equal(business0.Add(3*time.Second)) {
		t.Fatalf("connection bucket %+v", held)
	}
	for _, d := range e.db.deliveries {
		if d.dest != destWH && d.state == "pending" && d.next.Before(business0.Add(3*time.Second)) {
			t.Errorf("a delivery of the connection is due before the hold ends: %+v", d)
		}
	}
	e.business.Set(business0.Add(3 * time.Second))
	e.round(t)
	e.business.Advance(time.Second)
	e.round(t)
	for _, d := range e.db.deliveries {
		if d.state != "delivered" {
			t.Errorf("after the hold %+v", d)
		}
	}
	for _, c := range e.rec.Calls()[1:] {
		if c.Destination.Type == delivery.TypeMattermost && c.At.Before(business0.Add(3*time.Second)) {
			t.Errorf("called during the hold %+v", c)
		}
	}
}

// interactive is the interactive path of the env, whose sleeps advance both clocks.
func (e *env) interactive() *delivery.Interactive {
	return &delivery.Interactive{OrgID: orgID, Store: e.store, Clocks: clock.Clocks{Business: e.business, Real: e.real},
		Sleep: func(_ context.Context, d time.Duration) bool {
			e.business.Advance(d)
			e.real.Advance(d)
			return true
		}}
}

// TestInteractiveAheadOfQueue is C-11.FR-2: with the bucket of 6 per 30 s empty and deliveries waiting, a call on the interactive path
// takes the next token as soon as it is due, ahead of the deliveries, which wait the margin past it; it calls in the
// interactive client class and writes no delivery.
func TestInteractiveAheadOfQueue(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].per = 30
	e.queue(t, 11)
	e.round(t)
	waiting := 0
	for _, d := range e.db.deliveries {
		if d.state == "pending" {
			waiting++
		}
	}
	if e.rec.Count(deliverytest.MethodPublish) != 6 || waiting != 5 {
		t.Fatalf("%d publications, %d waiting", e.rec.Count(deliverytest.MethodPublish), waiting)
	}
	before := e.db.calls["RecordDelivered"]
	dest := delivery.Destination{ID: destMM, PublicID: "DSAAAAAAAAAA11", Type: delivery.TypeMattermost}
	conn := int64(connID)
	dest.Connection = &conn
	out, err := e.interactive().Do(t.Context(), delivery.Subject{Destination: &dest},
		delivery.ReadOp(func(ctx context.Context, c delivery.Call) delivery.Outcome {
			if c.Class != "interactive" || c.Destination.PublicID != dest.PublicID {
				t.Errorf("call %+v", c)
			}
			return e.rec.Check(ctx, c)
		}))
	if err != nil || out.Kind != delivery.OutcomeOK || !e.business.Now().Equal(business0.Add(5*time.Second)) {
		t.Fatalf("interactive = %+v, %v at %v", out, err, e.business.Now())
	}
	// The deliveries come at the margin and find the token taken.
	e.business.Advance(delivery.TokenMargin)
	e.round(t)
	if e.rec.Count(deliverytest.MethodPublish) != 6 || e.db.calls["RecordDelivered"] != before {
		t.Errorf("a delivery took the interactive token")
	}
}

// TestInteractiveLimited is C-11.FR-2: when no token frees within delivery.interactive_budget the call is not made and
// the caller learns how long until a token is due.
func TestInteractiveLimited(t *testing.T) {
	e := newEnv(t)
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 12
	dest := delivery.Destination{ID: destMM, PublicID: "DSAAAAAAAAAA11", Type: delivery.TypeMattermost}
	in := e.interactive()
	if _, err := in.Do(t.Context(), delivery.Subject{Destination: &dest}, delivery.ReadOp(func(context.Context,
		delivery.Call) delivery.Outcome {
		return delivery.Outcome{Kind: delivery.OutcomeOK}
	})); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := in.Do(t.Context(), delivery.Subject{Destination: &dest}, delivery.ReadOp(func(context.Context,
		delivery.Call) delivery.Outcome {
		called = true
		return delivery.Outcome{}
	}))
	var limited *delivery.LimitedError
	if !errors.As(err, &limited) || called || limited.RetryAfter != 7*time.Second || limited.Seconds() != 7 ||
		!strings.Contains(limited.Error(), "retry after 7s") {
		t.Errorf("limited = %v, called %v", err, called)
	}
	if (&delivery.LimitedError{RetryAfter: 100 * time.Millisecond}).Seconds() != 1 {
		t.Error("a wait under a second is not 1")
	}
}

// TestInteractiveRetryAfter: a RetryAfter answered on the interactive path holds the bucket of its scope; a
// Connection alone is limited by its own bucket only.
func TestInteractiveRetryAfter(t *testing.T) {
	e := newEnv(t)
	conn := int64(connID)
	dest := delivery.Destination{ID: destMM, PublicID: "DSAAAAAAAAAA11", Type: delivery.TypeMattermost,
		Connection: &conn}
	in := e.interactive()
	retry := func(scope delivery.Scope) delivery.Op {
		return delivery.ReadOp(func(context.Context, delivery.Call) delivery.Outcome {
			return delivery.Outcome{Kind: delivery.OutcomeRetryAfter, RetryAfter: 5 * time.Second, Scope: scope}
		})
	}
	if _, err := in.Do(t.Context(), delivery.Subject{Destination: &dest}, retry(delivery.ScopeDestination)); err != nil {
		t.Fatal(err)
	}
	if b := e.db.buckets[bucketKey{"destination", destMM}]; !b.refilled.Equal(business0.Add(5 * time.Second)) {
		t.Errorf("destination bucket %+v", b)
	}
	if _, err := in.Do(t.Context(), delivery.Subject{Connection: &conn}, retry(delivery.ScopeConnection)); err != nil {
		t.Fatal(err)
	}
	if b := e.db.buckets[bucketKey{"connection", connID}]; !b.refilled.After(business0) {
		t.Errorf("connection bucket %+v", b)
	}
	holds := e.db.calls["HoldBucket"]
	if _, err := in.Do(t.Context(), delivery.Subject{Connection: &conn}, retry(delivery.ScopeDestination)); err != nil ||
		e.db.calls["HoldBucket"] != holds {
		t.Errorf("a connection call held a destination: %v", err)
	}
	e.db.fail["HoldBucket"] = errBoom
	if _, err := in.Do(t.Context(), delivery.Subject{Connection: &conn}, retry(delivery.ScopeConnection)); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed hold = %v", err)
	}
	e.db.fail["TakeTokens"] = errBoom
	if _, err := in.Do(t.Context(), delivery.Subject{Connection: &conn}, retry(delivery.ScopeConnection)); !errors.Is(err,
		errBoom) {
		t.Errorf("a failed take = %v", err)
	}
	delete(e.db.fail, "TakeTokens")
	if _, err := in.Do(t.Context(), delivery.Subject{}, retry(delivery.ScopeConnection)); err == nil {
		t.Error("an interactive call without a subject")
	}
	// A sleep cut short by the context ends the call.
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 60
	e.db.buckets = map[bucketKey]*fakeBucket{}
	in.Sleep = func(context.Context, time.Duration) bool { return false }
	ok := delivery.ReadOp(func(context.Context, delivery.Call) delivery.Outcome {
		return delivery.Outcome{Kind: delivery.OutcomeOK}
	})
	_, _ = in.Do(t.Context(), delivery.Subject{Destination: &dest}, ok)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := in.Do(ctx, delivery.Subject{Destination: &dest}, ok); err == nil {
		t.Error("a cancelled wait called")
	}
	// The defaults: the budget of delivery.interactive_budget and a real sleep.
	in.Sleep, in.Budget = nil, 0
	in.Clocks.Real = clock.NewManual(real0)
	done := make(chan error, 1)
	go func() {
		_, err := in.Do(ctx, delivery.Subject{Destination: &dest}, ok)
		done <- err
	}()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("a real sleep after the context ended = %v", err)
	}
	if !delivery.SleepReal(t.Context(), time.Millisecond) || delivery.SleepReal(ctx, time.Hour) {
		t.Error("real sleep")
	}
}

// TestInteractiveAdapterOps: a send, an edit, a Thread reply and a Destination check on the interactive path each take
// a token and call the adapter in the interactive client class.
func TestInteractiveAdapterOps(t *testing.T) {
	e := newEnv(t)
	conn := int64(connID)
	dest := delivery.Destination{ID: destMM, PublicID: "DSAAAAAAAAAA11", Type: delivery.TypeMattermost,
		Connection: &conn}
	in := e.interactive()
	m := delivery.Message{Language: "en"}
	for _, op := range []delivery.Op{delivery.PublishOp(e.rec, m), delivery.UpdateOp(e.rec, "m1", m),
		delivery.ReplyOp(e.rec, delivery.Root{MessageID: "m1"}, m), delivery.CheckOp(e.rec)} {
		if out, err := in.Do(t.Context(), delivery.Subject{Destination: &dest}, op); err != nil ||
			out.Kind != delivery.OutcomeOK {
			t.Fatalf("op = %+v, %v", out, err)
		}
	}
	calls := e.rec.Calls()
	want := []string{deliverytest.MethodPublish, deliverytest.MethodUpdate, deliverytest.MethodReply,
		deliverytest.MethodCheck}
	if len(calls) != len(want) {
		t.Fatalf("calls %+v", calls)
	}
	for i, c := range calls {
		if c.Method != want[i] || c.Class != "interactive" || c.Destination.PublicID != dest.PublicID {
			t.Errorf("call %d = %+v", i, c)
		}
	}
	if calls[1].MessageID != "m1" || calls[2].MessageID != "m1" {
		t.Errorf("the edit and the reply name m1: %+v %+v", calls[1], calls[2])
	}
	w := &delivery.WebhookCall{Event: "alerts_added"}
	if _, err := in.Do(t.Context(), delivery.Subject{Destination: &dest}, delivery.WebhookOp(delivery.PublishOp(e.rec,
		m), w)); err != nil || e.rec.Calls()[4].Webhook != w {
		t.Errorf("a webhook op %v %+v", err, e.rec.Calls())
	}
}
