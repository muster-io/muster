// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// The fixtures of the round tests: the Mattermost Destination 13 of Connection 6, alone on Route 2, and the outgoing
// webhook 12 in the events mode, alone on Route 3.
const (
	destOther  = 13
	connOther  = 6
	routeOther = 2
	routeHook  = 3
)

// annotatedEvent, the second event of an Alert Group, changes its Root message and makes no Thread reply.
func annotatedEvent() groups.Recorded {
	return groups.Recorded{Seq: 2, Event: groups.EventAnnotationsChanged, Loudness: groups.Quiet}
}

// groupOn is the Alert Group i of Route route, not Urgent.
func groupOn(e *env, route int64, i int, title string) *groups.Group {
	gid := groupID + 9000 + int64(i)
	if e.db.groups[gid] == nil {
		e.db.groups[gid] = &fakeGroup{id: gid, publicID: fmt.Sprintf("AG%012d", 9000+i), number: 9000 + int64(i),
			status: "firing", route: route, created: e.business.Now()}
	}
	e.db.groups[gid].title = title
	return &groups.Group{ID: gid, PublicID: e.db.groups[gid].publicID, Number: 9000 + int64(i), RouteID: route,
		Title: title, Status: groups.StatusFiring}
}

// roundEnv is the events env with Route 1 on the Mattermost Destination 11 alone, the Destination 13 alone on Route 2
// and the outgoing webhook 12 in the events mode alone on Route 3, no Storm on any of them and every limiter wide.
func roundEnv(t *testing.T, batch int32) *eventsEnv {
	t.Helper()
	e := newEventsEnv(t, nil)
	e.w.Batch = batch
	conn := int64(connOther)
	e.db.dests[destOther] = &fakeDest{id: destOther, publicID: "DSAAAAAAAAAA13", name: "other",
		typ: delivery.TypeMattermost, connection: &conn, health: "healthy", limit: 600, per: 60}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 100_000, 1
	e.db.dests[destWH].limit, e.db.dests[destWH].per = 100_000, 1
	r := e.db.routes[routeID]
	r.threshold = 1_000_000
	e.db.routes[routeID] = r
	r.publicID = "RTAAAAAAAAAAA2"
	e.db.routes[routeOther] = r
	r.publicID = "RTAAAAAAAAAAA3"
	e.db.routes[routeHook] = r
	e.db.routeDests[routeID] = []int64{destMM}
	e.db.routeDests[routeOther] = []int64{destOther}
	e.db.routeDests[routeHook] = []int64{destWH}
	return e
}

// backlog is a roundEnv with n published Alert Groups on Route 1 whose Root messages in Destination 11 each wait for
// an edit that Alerts made, not a configuration edit, and Destination 11 at 5 calls a second, its bucket full; the
// Alert Groups "other" on Route 2 and "hook" on Route 3 are published and sent too.
func backlog(t *testing.T, n int, batch int32) (*eventsEnv, *groups.Group, *groups.Group) {
	t.Helper()
	e := roundEnv(t, batch)
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
	}
	other, hook := groupOn(e.env, routeOther, 1, "other"), groupOn(e.env, routeHook, 2, "hook")
	e.enqueue(t, other, groups.System, created())
	e.enqueue(t, hook, groups.System, created())
	for range 3 {
		e.round(t)
	}
	if p, s := e.rec.Count(deliverytest.MethodPublish), len(e.sender.calls); p != n+1 || s != 1 {
		t.Fatalf("%d publications and %d events, want %d and 1", p, s, n+1)
	}
	e.rec.Reset()
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "annotated"), groups.System, annotatedEvent())
	}
	for i, d := range e.db.deliveries[:n] {
		if d.state != "pending" || d.configOnly || d.urgent {
			t.Fatalf("delivery %d: %s, configuration only %v, urgent %v", i, d.state, d.configOnly, d.urgent)
		}
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 5, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	return e, other, hook
}

// takesBefore runs one round with the business clock moving 2 ms a token, as the fake environment of #540 measured,
// and returns how many tokens were taken before the token of the call that sent finds made; -1 when it was not made in
// the round. After the first most takes the clock stops, so that a round that would hold the call much longer ends.
func takesBefore(t *testing.T, e *eventsEnv, sent func() bool, most int) (before, takes int) {
	t.Helper()
	seen := -1
	var tick func()
	tick = func() {
		if seen < 0 && sent() {
			seen = takes
		}
		takes++
		if takes <= most {
			e.business.Advance(2 * time.Millisecond)
		}
		e.db.before["TakeTokens"] = tick
	}
	e.db.before["TakeTokens"] = tick
	e.round(t)
	delete(e.db.before, "TakeTokens")
	if seen < 0 && sent() {
		seen = takes
	}
	if seen < 0 {
		return -1, takes
	}
	return seen - 1, takes
}

// TestReplyWithinOneBatchOfABacklog is C-11.FR-3 and FR-4 for #540: while 1000 edits of Root messages that Alerts
// made wait for the limiter of one Destination, a Thread reply due on another Destination is sent after the edits
// that got a token and at most one claimed batch of those refused: once the limiter refused one, the round skips the
// rest of that Destination's rows, untouched, for the next round. With a batch of one, the round alone decides the
// order.
func TestReplyWithinOneBatchOfABacklog(t *testing.T) {
	for _, batch := range []int32{0, 1} {
		t.Run(fmt.Sprintf("batch %d", batch), func(t *testing.T) {
			const n = 1000
			e, other, _ := backlog(t, n, batch)
			e.enqueue(t, other, groups.System, alertsAdded(2, groups.StatusFiring, "fp-other"))
			if r := e.repliesTo(destOther); len(r) != 1 || r[0].state != "pending" ||
				r[0].next.After(e.business.Now()) {
				t.Fatalf("the reply in Destination 13: %+v", r)
			}
			before, takes := takesBefore(t, e, func() bool { return len(e.replies()) > 0 }, 5000)
			b := int(e.w.Batch)
			if b == 0 {
				b = int(delivery.Batch)
			}
			// The edits that got a token go first, in order; of those refused, at most one claimed batch.
			calls := e.rec.Calls()
			sent := slices.IndexFunc(calls, func(c deliverytest.Call) bool {
				return c.Method == deliverytest.MethodReply
			})
			if before < 0 || before-sent > b {
				t.Fatalf("the Thread reply went out after %d takes of the edits, %d of them refused "+
					"(%d takes in the round), want at most %d refused", before, before-sent, takes, b)
			}
			// The 5 tokens of the bucket, at most one batch refused, and the reply.
			if takes > 5+b+1 {
				t.Errorf("%d takes in the round for %d edits and a reply", takes, n)
			}
			skipped := 0
			for _, d := range e.db.deliveries[:n] {
				if d.state == "pending" && d.owner == "" && !d.next.After(e.business.Now()) {
					skipped++
				}
			}
			if skipped < n-5-b {
				t.Errorf("%d edits skipped untouched, want at least %d", skipped, n-5-b)
			}
		})
	}
}

// TestEventWithinOneBatchOfABacklog is C-11.FR-3 and C-15.FR-2 for #540: while 1000 edits of Root messages that
// Alerts made wait for the limiter of one Destination, an event due for an outgoing webhook is sent after the edits
// that got a token and at most one claimed batch of those refused.
func TestEventWithinOneBatchOfABacklog(t *testing.T) {
	for _, batch := range []int32{0, 1} {
		t.Run(fmt.Sprintf("batch %d", batch), func(t *testing.T) {
			const n = 1000
			e, _, hook := backlog(t, n, batch)
			e.enqueue(t, hook, webUser, event(2, groups.EventAcknowledged, groups.VariantAny))
			before, takes := takesBefore(t, e, func() bool { return len(e.sender.calls) > 1 }, 5000)
			b := int(e.w.Batch)
			if b == 0 {
				b = int(delivery.Batch)
			}
			// The edits that got a token go first, in order (rank 3 before rank 5); of those refused, at most one
			// claimed batch.
			sent := e.rec.Count(deliverytest.MethodUpdate)
			if before < 0 || before-sent > b {
				t.Fatalf("the webhook event went out after %d takes of the edits, %d of them refused "+
					"(%d takes in the round), want at most %d refused", before, before-sent, takes, b)
			}
		})
	}
}

// TestLaterRanksOncePerRound is C-11.FR-3 for #540 with every limiter holding back, the business clock moving 50 ms a
// token: a row denied a token is due again within the round, but a round attempts each edit that Alerts made, each
// Thread reply and each webhook event due at its start at most once — and, once a Destination's limiter refused one,
// none of the rest of its rows — and ends; the next rounds send them all as the tokens come.
func TestLaterRanksOncePerRound(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	const n = 50
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
	}
	e.drain(t)
	if p, s := e.rec.Count(deliverytest.MethodPublish), len(e.sender.calls); p != n || s != n {
		t.Fatalf("%d publications and %d events, want %d and %d", p, s, n, n)
	}
	e.rec.Reset()
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "annotated"), groups.System, annotatedEvent())
		e.enqueue(t, newGroup(e.env, i, "annotated"), groups.System,
			alertsAdded(3, groups.StatusFiring, fmt.Sprintf("fp%d", i)))
	}
	pending := func() (int, int, int) {
		var d, r, ev int
		for _, x := range e.db.deliveries {
			if x.state == "pending" {
				d++
			}
		}
		for _, x := range e.db.replies {
			if x.state == "pending" || x.state == "collecting" {
				r++
			}
		}
		for _, x := range e.db.webhookEvents {
			if x.state == "pending" {
				ev++
			}
		}
		return d, r, ev
	}
	if d, r, ev := pending(); d != n || r != n || ev != 2*n {
		t.Fatalf("%d edits, %d replies and %d events pending, want %d, %d and %d", d, r, ev, n, n, 2*n)
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 1
	e.db.dests[destWH].limit, e.db.dests[destWH].per = 1, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	leased := func() (int, int, int) {
		return e.db.calls["GetLeasedDelivery"], e.db.calls["GetLeasedReply"], e.db.calls["GetLeasedWebhookEvent"]
	}
	d0, r0, ev0 := leased()
	takes := 0
	var tick func()
	tick = func() {
		takes++
		e.business.Advance(50 * time.Millisecond)
		e.db.before["TakeTokens"] = tick
	}
	e.db.before["TakeTokens"] = tick
	e.round(t)
	delete(e.db.before, "TakeTokens")
	d1, r1, ev1 := leased()
	if d1-d0 > n || r1-r0 > n || ev1-ev0 > 2*n || takes > 4*n {
		t.Fatalf("%d attempts of %d edits, %d of %d replies, %d of %d events, %d takes in one round", d1-d0, n, r1-r0, n,
			ev1-ev0, 2*n, takes)
	}
	for i := 0; ; i++ {
		if d, r, ev := pending(); d+r+ev == 0 {
			break
		}
		if i == 8*n {
			d, r, ev := pending()
			t.Fatalf("%d edits, %d replies and %d events left after %d rounds", d, r, ev, i)
		}
		e.business.Advance(time.Second + delivery.TokenMargin)
		e.round(t)
	}
	if u, r := e.rec.Count(deliverytest.MethodUpdate), len(e.replies()); u != n || r != n || len(e.sender.calls) != 3*n {
		t.Errorf("%d edits, %d replies, %d events; want %d, %d, %d", u, r, len(e.sender.calls), n, n, 3*n)
	}
}

// TestConfigurationEditsLastBeyondOneBatch is C-11.FR-3 beyond one batch: ranks 3, 4 and 5 go in order, each until a
// claim comes back short, and the configuration edits that are not Urgent start only after them, so every edit that
// Alerts made goes before every Thread reply and every reply before every configuration edit, though there are more of
// each than one batch.
func TestConfigurationEditsLastBeyondOneBatch(t *testing.T) {
	e := roundEnv(t, 0)
	const n = 25
	for i := range 2 * n {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
	}
	e.round(t)
	if p := e.rec.Count(deliverytest.MethodPublish); p != 2*n {
		t.Fatalf("%d of %d published", p, 2*n)
	}
	e.rec.Reset()
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "annotated"), groups.System, annotatedEvent())
		e.enqueue(t, newGroup(e.env, i, "annotated"), groups.System,
			alertsAdded(3, groups.StatusFiring, fmt.Sprintf("fp%d", i)))
	}
	for i := n; i < 2*n; i++ {
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: newGroup(e.env, i, "renamed"),
			Actor: groups.System, Configuration: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.round(t)
	calls := e.rec.Calls()
	firstConfiguration := slices.IndexFunc(calls, func(c deliverytest.Call) bool {
		return c.Method == deliverytest.MethodUpdate && strings.Contains(textOf(c), "renamed")
	})
	var last, lastEdit int
	firstReply := slices.IndexFunc(calls, func(c deliverytest.Call) bool {
		return c.Method == deliverytest.MethodReply
	})
	for i, c := range calls {
		if c.Method == deliverytest.MethodReply || strings.Contains(textOf(c), "annotated") {
			last = i
		}
		if c.Method == deliverytest.MethodUpdate && strings.Contains(textOf(c), "annotated") {
			lastEdit = i
		}
	}
	if firstReply < 0 || lastEdit > firstReply {
		t.Errorf("the last edit that Alerts made at %d, the first Thread reply at %d", lastEdit, firstReply)
	}
	if len(calls) != 3*n || firstConfiguration < 0 || last > firstConfiguration {
		t.Fatalf("%d calls, the first configuration edit at %d, the last edit of Alerts or reply at %d", len(calls),
			firstConfiguration, last)
	}
}

// TestUrgentPublicationDuringEvents is C-11.FR-3 and C-15.FR-2: an Urgent Publication that comes due while 200 events
// of an outgoing webhook are sent goes out within one batch of them.
func TestUrgentPublicationDuringEvents(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.routeDests[routeID] = []int64{destWH}
	e.db.routeDests[routeOther] = []int64{destMM}
	const n = 200
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
	}
	takes, injected, published := 0, -1, -1
	var tick func()
	tick = func() {
		takes++
		e.business.Advance(10 * time.Millisecond)
		if published < 0 && e.rec.Count(deliverytest.MethodPublish) > 0 {
			published = len(e.sender.calls)
		}
		if takes == 25 {
			injected = len(e.sender.calls)
			g := groupOn(e.env, routeOther, 1, "urgent")
			g.Urgent, e.db.groups[g.ID].urgent = true, true
			e.enqueue(t, g, groups.System, created())
		}
		e.db.before["TakeTokens"] = tick
	}
	e.db.before["TakeTokens"] = tick
	e.round(t)
	delete(e.db.before, "TakeTokens")
	if published < 0 && e.rec.Count(deliverytest.MethodPublish) > 0 {
		published = len(e.sender.calls)
	}
	if injected < 0 || published < 0 {
		t.Fatalf("the Urgent Publication did not go out in the round (injected after %d of %d events)", injected,
			len(e.sender.calls))
	}
	if published-injected > int(delivery.Batch) {
		t.Fatalf("the Urgent Publication went out after %d events", published-injected)
	}
	if s := len(e.sender.calls); s != n {
		t.Errorf("%d events in the round, want %d", s, n)
	}
}

// TestUrgentAfterAShortBatch is C-11.FR-3: the round goes back to the Urgent ranks after every batch of ranks 3 to 6
// that claimed a row, a short one included. An Urgent Publication that comes due during a batch of one edit that
// Alerts made goes out before the full batch of Thread replies that follows it.
func TestUrgentAfterAShortBatch(t *testing.T) {
	e := roundEnv(t, 0)
	const n = 15
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
	}
	e.round(t)
	if p := e.rec.Count(deliverytest.MethodPublish); p != n {
		t.Fatalf("%d of %d published", p, n)
	}
	e.rec.Reset()
	e.enqueue(t, newGroup(e.env, 0, "annotated"), groups.System, annotatedEvent())
	for i := 1; i < n; i++ {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System,
			alertsAdded(2, groups.StatusFiring, fmt.Sprintf("fp%d", i)))
	}
	e.db.before["TakeTokens"] = func() {
		g := newGroupOf(e.env, n, "urgent", true)
		e.enqueue(t, g, groups.System, created())
	}
	e.round(t)
	calls := e.rec.Calls()
	urgent := slices.IndexFunc(calls, func(c deliverytest.Call) bool {
		return c.Method == deliverytest.MethodPublish && strings.Contains(textOf(c), "urgent")
	})
	reply := slices.IndexFunc(calls, func(c deliverytest.Call) bool { return c.Method == deliverytest.MethodReply })
	var order []string
	for _, c := range calls[:min(len(calls), 4)] {
		order = append(order, c.Method+" "+strings.Fields(textOf(c))[0])
	}
	if len(calls) == 0 || !strings.Contains(textOf(calls[0]), "annotated") || urgent != 1 || reply != 2 {
		t.Fatalf("the Urgent Publication at %d and the first reply at %d; first calls %q", urgent, reply, order)
	}
}
