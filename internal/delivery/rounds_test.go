// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
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

// TestUrgentOnARefusedDestination is C-11.FR-3: an Urgent Publication that comes due during a long pass of
// configuration edits of another Destination, in a Destination whose limiter refused an edit earlier in the round,
// goes out within one claimed batch once that limiter has a token again: the round leaves the Destination out of the
// claims of the work that is not Urgent until the next round, and out of the Urgent claims only until its token is
// due.
func TestUrgentOnARefusedDestination(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 100_000, 1
	const n, m = 30, 200
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
	}
	for i := range m {
		e.enqueue(t, groupOn(e.env, routeOther, i, "g"), groups.System, created())
	}
	e.round(t)
	if p := e.rec.Count(deliverytest.MethodPublish); p != n+m {
		t.Fatalf("%d of %d published", p, n+m)
	}
	e.rec.Reset()
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "annotated"), groups.System, annotatedEvent())
	}
	for i := range m {
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: groupOn(e.env, routeOther, i, "renamed"),
			Actor: groups.System, Configuration: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 5, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	takes, injected := 0, -1
	var tick func()
	tick = func() {
		takes++
		e.business.Advance(10 * time.Millisecond)
		if takes == 60 {
			injected = len(e.rec.Calls())
			e.enqueue(t, newGroupOf(e.env, n, "urgent", true), groups.System, created())
		}
		e.db.before["TakeTokens"] = tick
	}
	e.db.before["TakeTokens"] = tick
	e.round(t)
	delete(e.db.before, "TakeTokens")
	calls := e.rec.Calls()
	at := slices.IndexFunc(calls, func(c deliverytest.Call) bool {
		return c.Method == deliverytest.MethodPublish && strings.Contains(textOf(c), "urgent")
	})
	if injected < 0 || at < 0 || at-injected > int(delivery.Batch) {
		t.Fatalf("takes %d, injected at call %d, the Urgent Publication at call %d of %d", takes, injected, at,
			len(calls))
	}
	// The edits that Alerts made in Destination 11 still wait: none of them took a token after the refusal.
	if u := slices.IndexFunc(calls[injected:], func(c deliverytest.Call) bool {
		return c.Destination.ID == destMM && strings.Contains(textOf(c), "annotated")
	}); u >= 0 {
		t.Errorf("an edit that Alerts made in Destination 11 went at call %d after the refusal", injected+u)
	}
}

// urgentBacklog is a roundEnv with n published Urgent Alert Groups on Route 1 whose Root messages in Destination 11
// each wait for an edit — one that Alerts made, or one that configuration alone made — and Destination 11 at limit
// calls a second, its bucket full; on Route 2, the Root messages of the Urgent Alert Group "bu" and of "bn", which is
// not Urgent, in Destination 13 each wait for an edit that Alerts made, due after those of Destination 11 — "bn" only
// with later, so that without it no work comes after the Urgent ranks. The worker works a second Organization after
// the first.
func urgentBacklog(t *testing.T, n int, limit int64, configuration, later bool) *eventsEnv {
	t.Helper()
	e := roundEnv(t, 0)
	e.w.Organizations = func(context.Context) ([]int64, error) { return []int64{orgID, orgID + 1}, nil }
	for i := range n {
		e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
	}
	bu, bn := groupOn(e.env, routeOther, 1, "bu"), groupOn(e.env, routeOther, 2, "bn")
	bu.Urgent, e.db.groups[bu.ID].urgent = true, true
	e.enqueue(t, bu, groups.System, created())
	b := 1
	if later {
		e.enqueue(t, bn, groups.System, created())
		b++
	}
	for range 3 {
		e.round(t)
	}
	if p := e.rec.Count(deliverytest.MethodPublish); p != n+b {
		t.Fatalf("%d of %d published", p, n+b)
	}
	e.rec.Reset()
	for i := range n {
		g := newGroupOf(e.env, i, "edit", true)
		if !configuration {
			e.enqueue(t, g, groups.System, annotatedEvent())
			continue
		}
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
			Configuration: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.business.Advance(time.Millisecond)
	bu, bn = groupOn(e.env, routeOther, 1, "buedit"), groupOn(e.env, routeOther, 2, "bnedit")
	bu.Urgent = true
	e.enqueue(t, bu, groups.System, annotatedEvent())
	if later {
		e.enqueue(t, bn, groups.System, annotatedEvent())
	}
	for _, d := range e.db.deliveries {
		if d.state != "pending" || d.dest == destMM && (!d.urgent || d.configOnly != configuration) {
			t.Fatalf("delivery %d of Destination %d: %s, urgent %v, configuration only %v", d.id, d.dest, d.state,
				d.urgent, d.configOnly)
		}
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = limit, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	return e
}

// TestUrgentBacklogOnOneDestination is C-11.FR-3 for #546, with the business clock moving 2 ms a token: while 300
// Urgent edits — that Alerts made, or that configuration alone made, after a key rotation for one — wait for the
// limiter of one Mattermost Destination, each edit due in another Destination goes after the calls before it and, in
// this scenario, where no other Urgent work comes after the backlog, at most two claimed batches of refused takes: one
// of the visit's bounded claims and one of its batch of rank 1 due now (each further batch due now that finds the token
// due would add one); and the round of the first Organization ends having attempted each of its rows at most once, so
// that the next Organization's turn comes. At 100 calls a second the limiter has a token again after almost every
// batch: a round that claims the Urgent ranks due now attempts the backlog again and again.
func TestUrgentBacklogOnOneDestination(t *testing.T) {
	for _, c := range []struct {
		limit         int64
		configuration bool
	}{{5, false}, {5, true}, {100, false}, {100, true}} {
		t.Run(fmt.Sprintf("%d a second, configuration %v", c.limit, c.configuration), func(t *testing.T) {
			const n = 300
			e := urgentBacklog(t, n, c.limit, c.configuration, true)
			titles := []string{"buedit", "bnedit"}
			takes, turn, leased := 0, -1, -1
			seen := map[string]int{}
			sent := func(title string) int {
				return slices.IndexFunc(e.rec.Calls(), func(c deliverytest.Call) bool {
					return c.Destination.ID == destOther && strings.Contains(textOf(c), title)
				})
			}
			var tick, next func()
			tick = func() {
				for _, title := range titles {
					if _, ok := seen[title]; !ok && sent(title) >= 0 {
						seen[title] = takes - 1
					}
				}
				takes++
				e.business.Advance(2 * time.Millisecond)
				e.db.before["TakeTokens"] = tick
			}
			// The first read of the next due time ends the reconcile of the first Organization.
			leased0 := e.db.calls["GetLeasedDelivery"]
			next = func() {
				if turn < 0 {
					turn, leased = takes, e.db.calls["GetLeasedDelivery"]-leased0
				}
				e.db.before["NextDeliveryWork"] = next
			}
			e.db.before["TakeTokens"], e.db.before["NextDeliveryWork"] = tick, next
			e.round(t)
			delete(e.db.before, "TakeTokens")
			delete(e.db.before, "NextDeliveryWork")
			if turn < 0 {
				t.Fatal("the reconcile of the first Organization did not end")
			}
			if leased > n+len(titles) {
				t.Errorf("the next Organization's turn after %d takes and %d attempts of %d edits, want at most %d "+
					"attempts", turn, leased, n+len(titles), n+len(titles))
			}
			for _, title := range titles {
				before, ok := seen[title]
				at := sent(title)
				if !ok || before > turn {
					t.Fatalf("the edit %s did not go out before the next Organization's turn (sent after %d takes, "+
						"the turn after %d)", title, before, turn)
				}
				if before-at > 2*int(delivery.Batch) {
					t.Errorf("the edit %s went out after %d calls and %d refused takes, want at most %d refused",
						title, at, before-at, 2*delivery.Batch)
				}
			}
		})
	}
}

// TestUrgentPublicationDuringUrgentWork is C-11.FR-3 for #546, with the business clock moving 2 ms a token: an Urgent
// Publication that comes due during the Urgent ranks of a round goes out in the round within one claimed batch, whether
// or not work that is not Urgent follows them: during the refused backlog of 300 Urgent edits of one Destination;
// during 300 Urgent edits that Alerts made that have their tokens, right after them, as each rank goes oldest first;
// during 300 Urgent configuration edits that have their tokens, within one batch of them. So do 15 Urgent Publications
// that come due during a drain, more than one batch, with nothing after them, the last within one batch of the first
// ten, and 25 that come due during a batch of the configuration edits, all before the next batch of them: the drain
// after it claims what is due when it starts.
func TestUrgentPublicationDuringUrgentWork(t *testing.T) {
	for _, c := range []struct {
		name                 string
		limit                int64
		configuration, later bool
		at, pubs             int
	}{{"refused edits", 5, false, true, 3, 1}, {"edits", 100_000, false, true, 25, 1},
		{"configuration edits", 100_000, true, true, 25, 1}, {"refused edits, nothing after", 5, false, false, 3, 1},
		{"edits, nothing after", 100_000, false, false, 25, 1},
		{"edits, nothing after, 15 Publications", 100_000, false, false, 25, 15},
		{"configuration edits, 25 Publications", 100_000, true, true, 25, 25}} {
		t.Run(c.name, func(t *testing.T) {
			const n = 300
			e := urgentBacklog(t, n, c.limit, c.configuration, c.later)
			// One Organization: the fake database does not keep them apart.
			e.w.Organizations = func(context.Context) ([]int64, error) { return []int64{orgID}, nil }
			takes, injected := 0, -1
			var tick func()
			tick = func() {
				takes++
				e.business.Advance(2 * time.Millisecond)
				if takes == c.at {
					injected = len(e.rec.Calls())
					for k := range c.pubs {
						g := groupOn(e.env, routeOther, 3+k, "urgent")
						g.Urgent, e.db.groups[g.ID].urgent = true, true
						e.enqueue(t, g, groups.System, created())
					}
				}
				e.db.before["TakeTokens"] = tick
			}
			e.db.before["TakeTokens"] = tick
			e.round(t)
			delete(e.db.before, "TakeTokens")
			calls := e.rec.Calls()
			at, sent := -1, 0
			for i, c := range calls {
				if c.Method == deliverytest.MethodPublish && strings.Contains(textOf(c), "urgent") {
					at, sent = i, sent+1
				}
			}
			if injected < 0 || sent < c.pubs {
				t.Fatalf("%d of %d Urgent Publications went out in the round (injected after %d of %d calls)", sent,
					c.pubs, injected, len(calls))
			}
			// The Urgent edits that Alerts made in Destination 11, due before it, go first.
			after := injected
			if c.limit > 5 && !c.configuration {
				for i, c := range calls {
					if c.Destination.ID == destMM {
						after = i
					}
				}
				if after > at {
					t.Fatalf("the Urgent Publication at call %d, before an edit due before it at %d", at, after)
				}
			}
			if at-after > int(delivery.Batch)+c.pubs-1 {
				t.Fatalf("the Urgent Publication at call %d, injected after %d calls, the last call before it it "+
					"waits for at %d", at, injected, after)
			}
		})
	}
}

// TestUrgentOnASpentDestinationDuringAVisit is C-11.FR-3 for #546, with the business clock moving 10 ms a token: during
// one long visit of the Urgent ranks — 30 Urgent configuration edits in Destination 11 at 5 calls a second, then 200 in
// Destination 13, whose limiter has room — Destination 11's limiter refuses an edit and the visit's bounded claims skip
// it; an Urgent Publication that then comes due in Destination 11 goes out within one claimed batch once its limiter
// has a token again, and the remaining configuration edits of Destination 11 take no token in the visit.
func TestUrgentOnASpentDestinationDuringAVisit(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 100_000, 1
	const n, m = 30, 200
	for i := range n {
		e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
	}
	others := make([]*groups.Group, m)
	for i := range m {
		others[i] = groupOn(e.env, routeOther, i, "g")
		others[i].Urgent, e.db.groups[others[i].ID].urgent = true, true
		e.enqueue(t, others[i], groups.System, created())
	}
	e.round(t)
	if p := e.rec.Count(deliverytest.MethodPublish); p != n+m {
		t.Fatalf("%d of %d published", p, n+m)
	}
	e.rec.Reset()
	rerender := func(g *groups.Group) {
		g.Title = "renamed"
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
			Configuration: true}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range n {
		rerender(newGroupOf(e.env, i, "renamed", true))
	}
	e.business.Advance(time.Millisecond)
	for _, g := range others {
		rerender(g)
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 5, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	takes := 0
	var injected time.Time
	var tick func()
	tick = func() {
		takes++
		e.business.Advance(10 * time.Millisecond)
		if takes == 30 {
			injected = e.business.Now()
			e.enqueue(t, newGroupOf(e.env, n, "urgent", true), groups.System, created())
		}
		e.db.before["TakeTokens"] = tick
	}
	e.db.before["TakeTokens"] = tick
	e.round(t)
	delete(e.db.before, "TakeTokens")
	calls := e.rec.Calls()
	at := slices.IndexFunc(calls, func(c deliverytest.Call) bool {
		return c.Method == deliverytest.MethodPublish && strings.Contains(textOf(c), "urgent")
	})
	edits := 0
	for _, c := range calls {
		if c.Destination.ID == destMM && c.Method == deliverytest.MethodUpdate {
			edits++
		}
	}
	if injected.IsZero() || at < 0 {
		t.Fatalf("the Urgent Publication did not go out in the round (%d calls, %d edits in Destination 11)",
			len(calls), edits)
	}
	// Destination 11 has a token again 200 ms after its bucket ran dry, before the injection: the Publication waits
	// for at most the rest of the claimed batch in flight.
	waited := slices.IndexFunc(calls, func(c deliverytest.Call) bool { return !c.At.Before(injected) })
	t.Logf("the Urgent Publication at call %d of %d, %d calls after it came due; %d edits in Destination 11", at,
		len(calls), at-waited, edits)
	if at-waited > int(delivery.Batch) {
		t.Errorf("the Urgent Publication at call %d, %d calls after it came due", at, at-waited)
	}
	if edits > 5 {
		t.Errorf("%d configuration edits in Destination 11 in the round, want the 5 of its bucket", edits)
	}
}

// TestUrgentConfigurationEditsInLaterVisits is C-11.FR-3 for #546, with the business clock moving 2 ms a token: 300
// Urgent configuration edits wait in Destination 11 at 100 calls a second, and 50 Publications that are not Urgent in
// Destination 13, whose limiter has room. Destination 11's limiter refuses an edit in the first visit of the Urgent
// ranks, which skips it for the rest of the visit; each later visit, after a batch of the Publications, claims its
// edits again once its token is due, so some of its edits go after the first Publication, and a refusal in a later
// visit skips it again for the rest of that visit, so each visit makes at most one claimed batch of refused takes
// there.
func TestUrgentConfigurationEditsInLaterVisits(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 100_000, 1
	const n, m = 300, 50
	for i := range n {
		e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
	}
	e.round(t)
	e.rec.Reset()
	for i := range n {
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: newGroupOf(e.env, i, "renamed", true),
			Actor: groups.System, Configuration: true}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range m {
		e.enqueue(t, groupOn(e.env, routeOther, i, "p"), groups.System, created())
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 100, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	takes := 0
	var tick func()
	tick = func() {
		takes++
		e.business.Advance(2 * time.Millisecond)
		e.db.before["TakeTokens"] = tick
	}
	e.db.before["TakeTokens"] = tick
	e.round(t)
	delete(e.db.before, "TakeTokens")
	edits, published, last := 0, 0, -1
	for i, c := range e.rec.Calls() {
		switch {
		case c.Destination.ID == destMM && c.Method == deliverytest.MethodUpdate:
			edits++
			if published > 0 {
				last = i
			}
		case c.Method == deliverytest.MethodPublish:
			published++
		}
	}
	refused := takes - len(e.rec.Calls())
	t.Logf("%d configuration edits in Destination 11 and %d Publications in the round, the last edit at call %d; %d "+
		"refused takes", edits, published, last, refused)
	if published != m || last < 0 {
		t.Errorf("%d configuration edits in Destination 11, none after the first Publication, and %d of %d "+
			"Publications in the round; want some edits in the later visits", edits, published, m)
	}
	// A visit at the start and one after each of the 5 full batches of Publications: the bounded claims of each make
	// at most one claimed batch of refused takes in Destination 11, which has no work for the batches due now.
	if visits := 1 + m/int(delivery.Batch); refused > visits*int(delivery.Batch) {
		t.Errorf("%d refused takes in the round, want at most %d, one claimed batch in each of the %d visits", refused,
			visits*int(delivery.Batch), visits)
	}
}

// TestUrgentPublicationDuringAShortConfigurationBatch is C-11.FR-3 for #546: an Urgent Publication that comes due
// during a short batch of 5 Urgent configuration edits, with nothing after it, goes out in the round: the visit claims
// one more batch of rank 1 due now after a short batch of rank 2 that claimed a row.
func TestUrgentPublicationDuringAShortConfigurationBatch(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 100_000, 1
	const n = 5
	others := make([]*groups.Group, n)
	for i := range n {
		others[i] = groupOn(e.env, routeOther, i, "g")
		others[i].Urgent, e.db.groups[others[i].ID].urgent = true, true
		e.enqueue(t, others[i], groups.System, created())
	}
	e.round(t)
	e.rec.Reset()
	for _, g := range others {
		g.Title = "renamed"
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
			Configuration: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.db.before["TakeTokens"] = func() {
		e.enqueue(t, newGroupOf(e.env, 0, "urgent", true), groups.System, created())
	}
	e.round(t)
	calls := e.rec.Calls()
	at := slices.IndexFunc(calls, func(c deliverytest.Call) bool {
		return c.Method == deliverytest.MethodPublish && strings.Contains(textOf(c), "urgent")
	})
	if at != n {
		t.Fatalf("the Urgent Publication at call %d of %d, want %d, right after the configuration edits", at,
			len(calls), n)
	}
}
