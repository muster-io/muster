// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"errors"
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

// runRounds runs rounds as Run does, the business clock moving 2 ms a token and, after each round, by the wait it
// returns, at least MinWait, until done says so or after most rounds; it returns the rounds it ran. With woken, each
// round comes MinWait after the last, as when the NOTIFY of other work wakes the worker.
func runRounds(t *testing.T, e *eventsEnv, woken bool, most int, done func() bool) int {
	t.Helper()
	var tick func()
	tick = func() {
		e.business.Advance(2 * time.Millisecond)
		e.db.before["TakeTokens"] = tick
	}
	defer delete(e.db.before, "TakeTokens")
	for r := range most {
		if done() {
			return r
		}
		e.db.before["TakeTokens"] = tick
		next := e.round(t)
		if woken {
			next = 0
		}
		e.business.Advance(max(next, delivery.MinWait))
	}
	return most
}

// callsOn is the sequence of the calls to Destination dest whose text has one of the marks, each written as its
// letter.
func callsOn(e *eventsEnv, dest int64, marks map[string]string) string {
	var b strings.Builder
	for _, c := range e.rec.Calls() {
		if c.Destination.ID != dest {
			continue
		}
		for mark, letter := range marks {
			if strings.Contains(textOf(c), mark) {
				b.WriteString(letter)
			}
		}
	}
	return b.String()
}

// TestUrgentEditsBeforeConfigurationEditsAcrossRounds is C-11.FR-3 for #554: 8 Urgent edits that Alerts made and,
// due after them, 500 configuration edits that are not Urgent wait in Destination 11 at 1 call a second, and the
// worker waits between rounds as Run does. Every Urgent edit goes out before any configuration edit: a refused
// Destination is left out of every claim until its token's time plus TokenMargin, when the refused Urgent edits are
// due again, also in the rounds that follow the refusal, whether the worker sleeps until then or the NOTIFY of other
// work wakes it every MinWait.
func TestUrgentEditsBeforeConfigurationEditsAcrossRounds(t *testing.T) {
	for _, woken := range []bool{false, true} {
		t.Run(fmt.Sprintf("woken %v", woken), func(t *testing.T) {
			e := roundEnv(t, 0)
			const u, m = 8, 500
			for i := range u {
				e.enqueue(t, newGroupOf(e.env, i, "u", true), groups.System, created())
			}
			for i := range m {
				e.enqueue(t, newGroupOf(e.env, 100+i, "n", false), groups.System, created())
			}
			for range 3 {
				e.round(t)
			}
			e.rec.Reset()
			for i := range u {
				e.enqueue(t, newGroupOf(e.env, i, "uedit", true), groups.System, annotatedEvent())
			}
			e.business.Advance(time.Millisecond)
			for i := range m {
				if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: newGroupOf(e.env, 100+i, "cfg",
					false), Actor: groups.System, Configuration: true}); err != nil {
					t.Fatal(err)
				}
			}
			e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 1
			e.db.buckets = map[bucketKey]*fakeBucket{}
			marks := map[string]string{"uedit": "U", "cfg": "c"}
			rounds := runRounds(t, e, woken, 6000, func() bool {
				return strings.Count(callsOn(e, destMM, marks), "U") == u
			})
			if s := callsOn(e, destMM, marks); !strings.HasPrefix(s, strings.Repeat("U", u)) {
				t.Fatalf("after %d rounds, the first configuration edit at call %d and the last Urgent edit at call "+
					"%d of %d in Destination 11, want the %d Urgent edits first", rounds, strings.Index(s, "c"),
					strings.LastIndex(s, "U"), len(s), u)
			}
		})
	}
}

// TestUrgentEditsBeforeUrgentConfigurationEditsInARound is C-11.FR-3 for #554 within one round: 8 Urgent edits that
// Alerts made and 50 Urgent configuration edits wait in Destination 11 at 5 calls a second, and 300 edits that are not
// Urgent in Destination 13, whose limiter has room, make the round visit the Urgent ranks many times. A configuration
// edit never takes a token that Destination 11 gets back ahead of the Urgent edits refused earlier in the round, and
// across the rounds every Urgent edit goes first.
func TestUrgentEditsBeforeUrgentConfigurationEditsInARound(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 100_000, 1
	const u, c, m = 8, 50, 300
	for i := range u + c {
		e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
	}
	for i := range m {
		e.enqueue(t, groupOn(e.env, routeOther, i, "g"), groups.System, created())
	}
	for range 3 {
		e.round(t)
	}
	e.rec.Reset()
	for i := range c {
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: newGroupOf(e.env, u+i, "cfg", true),
			Actor: groups.System, Configuration: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.business.Advance(time.Millisecond)
	for i := range u {
		e.enqueue(t, newGroupOf(e.env, i, "uedit", true), groups.System, annotatedEvent())
	}
	for i := range m {
		e.enqueue(t, groupOn(e.env, routeOther, i, "nedit"), groups.System, annotatedEvent())
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 5, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	marks := map[string]string{"uedit": "U", "cfg": "c"}
	runRounds(t, e, false, 1, func() bool { return false })
	first := callsOn(e, destMM, marks)
	if n := e.rec.Count(deliverytest.MethodUpdate) - len(first); n != m {
		t.Fatalf("%d edits in Destination 13 in the round, want %d", n, m)
	}
	if i := strings.Index(first, "c"); i >= 0 && strings.Count(first, "U") < u {
		t.Fatalf("calls in Destination 11 in the round %q: a configuration edit at call %d, ahead of a refused "+
			"Urgent edit", first, i)
	}
	runRounds(t, e, true, 200, func() bool { return strings.Count(callsOn(e, destMM, marks), "U") == u })
	if s := callsOn(e, destMM, marks); !strings.HasPrefix(s, strings.Repeat("U", u)) {
		t.Fatalf("calls in Destination 11 %q, want the %d Urgent edits first", s, u)
	}
}

// TestConnectionRefusalSkipsItsDestinations is C-11.FR-3 for #547: Destinations 11 and 14 share Connection 5, whose
// limiter allows 5 calls a second while theirs have room; 8 Urgent edits wait in Destination 11, 50 edits that are
// not Urgent and 25 Thread replies in Destination 14, due after 100 edits in Destination 13, which keep the round going
// until the Connection has a token again. Once the Connection refused an Urgent edit, the round leaves every
// Destination of the Connection out of the claims of the work that is not Urgent, so Destination 14 takes no token in
// the round, and across the rounds the Urgent edits go first.
func TestConnectionRefusalSkipsItsDestinations(t *testing.T) {
	e := roundEnv(t, 0)
	const dest14, route14 = 14, 4
	conn := int64(connID)
	e.db.dests[dest14] = &fakeDest{id: dest14, publicID: "DSAAAAAAAAAA14", name: "ops-2", typ: delivery.TypeMattermost,
		connection: &conn, health: "healthy", limit: 100_000, per: 1}
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 100_000, 1
	r := e.db.routes[routeID]
	r.publicID = "RTAAAAAAAAAAA4"
	e.db.routes[route14] = r
	e.db.routeDests[route14] = []int64{dest14}
	const u, m, n = 8, 50, 100
	for i := range u {
		e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
	}
	for i := range n {
		e.enqueue(t, groupOn(e.env, routeOther, i, "g"), groups.System, created())
	}
	for i := range m {
		e.enqueue(t, groupOn(e.env, route14, 500+i, "g"), groups.System, created())
	}
	for range 3 {
		e.round(t)
	}
	if p := e.rec.Count(deliverytest.MethodPublish); p != u+m+n {
		t.Fatalf("%d of %d published", p, u+m+n)
	}
	e.rec.Reset()
	for i := range u {
		e.enqueue(t, newGroupOf(e.env, i, "uedit", true), groups.System, annotatedEvent())
	}
	for i := range n {
		e.enqueue(t, groupOn(e.env, routeOther, i, "nedit"), groups.System, annotatedEvent())
	}
	e.business.Advance(time.Millisecond)
	for i := range m {
		ev := annotatedEvent()
		if i%2 == 1 {
			ev = alertsAdded(2, groups.StatusFiring, fmt.Sprintf("fp-%d", i))
		}
		e.enqueue(t, groupOn(e.env, route14, 500+i, "cedit"), groups.System, ev)
	}
	if r := e.repliesTo(dest14); len(r) != m/2 {
		t.Fatalf("%d Thread replies in Destination 14, want %d", len(r), m/2)
	}
	e.db.connLimits[connID] = [2]int64{5, 1}
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
	on11, on14 := callsOn(e, destMM, map[string]string{"uedit": "U"}), 0
	for _, c := range e.rec.Calls() {
		if c.Destination.ID == dest14 {
			on14++
		}
	}
	if sent := len(e.rec.Calls()); on14 != 0 || takes-sent > 2*int(delivery.Batch) {
		t.Fatalf("%d edits in Destination 11 and %d calls in Destination 14 in the round, %d refused takes; want none "+
			"in Destination 14 and at most two claimed batches refused", len(on11), on14, takes-sent)
	}
	runRounds(t, e, true, 200, func() bool {
		return strings.Count(callsOn(e, destMM, map[string]string{"uedit": "U"}), "U") == u
	})
	var order strings.Builder
	for _, c := range e.rec.Calls() {
		switch {
		case c.Destination.ID == destMM && strings.Contains(textOf(c), "uedit"):
			order.WriteString("U")
		case c.Destination.ID == dest14:
			order.WriteString("c")
		}
	}
	if s := order.String(); !strings.HasPrefix(s, strings.Repeat("U", u)) {
		t.Fatalf("the calls through Connection 5 %q, want the %d Urgent edits first", s, u)
	}
}

// TestMemoKeepsTheLatestDueTime is C-11.FR-3 for #554: the memo keeps the latest due time of the tokens a limiter
// refused, not the first. Destination 11 allows 1 call a second; of 4 Urgent edits, the first takes the token, the
// second is refused, the clock then moves 1.5 s, the third takes the token that came back and the fourth is refused
// again, its token due 1 s later. The batch of rank 1 due now leaves Destination 11 alone until then, though the second
// edit is due again already, and the worker waits for that token, though 3 edits that are not Urgent wait there
// untouched.
func TestMemoKeepsTheLatestDueTime(t *testing.T) {
	e := roundEnv(t, 0)
	const u, m = 4, 3
	for i := range u {
		e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
	}
	for i := range m {
		e.enqueue(t, newGroupOf(e.env, 100+i, "g", false), groups.System, created())
	}
	e.round(t)
	e.rec.Reset()
	for i := range u {
		e.enqueue(t, newGroupOf(e.env, i, "uedit", true), groups.System, annotatedEvent())
	}
	for i := range m {
		e.enqueue(t, newGroupOf(e.env, 100+i, "nedit", false), groups.System, annotatedEvent())
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	takes := 0
	var tick func()
	tick = func() {
		takes++
		if takes == 2 {
			e.business.Advance(1500 * time.Millisecond)
		}
		e.db.before["TakeTokens"] = tick
	}
	e.db.before["TakeTokens"] = tick
	next := e.round(t)
	delete(e.db.before, "TakeTokens")
	if sent := len(e.rec.Calls()); takes != u || sent != 2 {
		t.Errorf("%d takes and %d calls in the round, want %d and 2: a refused edit was claimed again before the "+
			"latest refused token was due", takes, sent, u)
	}
	if want := time.Second; next != want {
		t.Errorf("the worker waits %v, want %v, until the latest due time of the refused tokens", next, want)
	}
}

// TestWaitForARefusedToken is C-11.FR-3 for #547: when every due row waits for a limiter that refused a take, the
// worker sleeps until that token's time instead of repeating rounds every MinWait, and a wake — the NOTIFY of new work —
// still runs a round at once, which delivers the new work on another Destination and leaves the refused one alone. The
// round at the token, where the batches of rank 1 due now could take it, leaves the edits that are not Urgent alone
// until the token's time plus TokenMargin, when they are due again; one more round then sends one. Destination 11
// allows 10 calls a minute, as a Telegram chat does, and 30 edits wait there.
func TestWaitForARefusedToken(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 100_000, 1
	const n = 30
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
	}
	e.round(t)
	e.rec.Reset()
	for i := range n {
		e.enqueue(t, newGroup(e.env, i, "edit"), groups.System, annotatedEvent())
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 10, 60
	e.db.buckets = map[bucketKey]*fakeBucket{}
	interval := 6 * time.Second
	var waits []time.Duration
	var takes []int
	ctx, cancel := context.WithCancel(t.Context())
	e.w.Wait = func(_ context.Context, d time.Duration, wake <-chan struct{}) {
		waits = append(waits, d)
		takes = append(takes, e.db.calls["TakeTokens"])
		switch len(waits) {
		case 1:
			e.enqueue(t, groupOn(e.env, routeOther, 1, "woken"), groups.System, created())
			e.w.Wake()
			<-wake
		case 2, 3:
			e.business.Advance(d)
		case 4:
			cancel()
		}
	}
	e.w.Run(ctx)
	if len(waits) != 4 {
		t.Fatalf("waits %v", waits)
	}
	if waits[0] != interval || waits[1] != interval || waits[2] != delivery.TokenMargin {
		t.Errorf("waits %v, want %v twice, until the refused token's time, also after a wake, then %v", waits,
			interval, delivery.TokenMargin)
	}
	if woken := callsOn(e, destOther, map[string]string{"woken": "w"}); woken != "w" {
		t.Errorf("the Publication in Destination 13 that woke the worker: %q", woken)
	}
	// The wake and the round at the token took no token in Destination 11; the round after the margin makes one call
	// there and one claimed batch of refused takes, and the worker sleeps until the next token.
	if takes[1]-takes[0] != 1 || takes[2] != takes[1] || takes[3]-takes[2] != int(delivery.Batch) {
		t.Errorf("takes %v: want 1 in the woken round, none at the token and %d after the margin", takes,
			delivery.Batch)
	}
	if edits := e.rec.Count(deliverytest.MethodUpdate); edits != 11 {
		t.Errorf("%d edits in Destination 11, want the 10 of its bucket and 1 after the token", edits)
	}
	if waits[3] <= interval-2*delivery.TokenMargin || waits[3] > interval {
		t.Errorf("the wait after the token %v, want about %v", waits[3], interval)
	}
}

// TestWaitForTheEarliestRefusedToken is C-11.FR-3 for #547: with the limiters of Destinations 11 and 13 refusing, at
// 1 call a second and 1 call in 3 seconds, the worker sleeps until the earlier token's time.
func TestWaitForTheEarliestRefusedToken(t *testing.T) {
	e := roundEnv(t, 0)
	for i := range 2 {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
		e.enqueue(t, groupOn(e.env, routeOther, i, "g"), groups.System, created())
	}
	e.round(t)
	for i := range 2 {
		e.enqueue(t, newGroup(e.env, i, "edit"), groups.System, annotatedEvent())
		e.enqueue(t, groupOn(e.env, routeOther, i, "edit"), groups.System, annotatedEvent())
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 1
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 1, 3
	e.db.buckets = map[bucketKey]*fakeBucket{}
	if next, want := e.round(t), time.Second; next != want {
		t.Errorf("the worker waits %v, want %v", next, want)
	}
}

// TestUrgentPublicationOnARefusedDestinationAtItsToken is C-11.FR-3 for #554: Destination 11 refused an Urgent edit
// that Alerts made, which is due again at the token's time plus TokenMargin; an Urgent Publication there that comes
// due before the token goes out once the token is due, ahead of the refused edit, which is due later, while 200 Urgent
// configuration edits in Destination 13 keep the round going.
func TestUrgentPublicationOnARefusedDestinationAtItsToken(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 100_000, 1
	const n, m = 6, 200
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
	e.rec.Reset()
	for i := range n {
		e.enqueue(t, newGroupOf(e.env, i, "annotated", true), groups.System, annotatedEvent())
	}
	e.business.Advance(time.Millisecond)
	for _, g := range others {
		g.Title = "renamed"
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
			Configuration: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 5, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	takes := 0
	var injected time.Time
	var tick func()
	tick = func() {
		takes++
		e.business.Advance(10 * time.Millisecond)
		if takes == 15 {
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
	last := slices.IndexFunc(calls, func(c deliverytest.Call) bool {
		return c.Destination.ID == destMM && c.Method == deliverytest.MethodUpdate && !c.At.Before(injected)
	})
	if injected.IsZero() || at < 0 {
		t.Fatalf("the Urgent Publication did not go out in the round (%d calls)", len(calls))
	}
	// The bucket runs dry at the 5th take and refuses the 6th; its token is due after the injection, and the refused
	// edit TokenMargin later.
	t.Logf("the Urgent Publication at call %d of %d, %v after it came due; the refused edit at call %d", at, len(calls),
		calls[at].At.Sub(injected), last)
	if !calls[at].At.Before(injected.Add(delivery.TokenMargin)) {
		t.Errorf("the Urgent Publication went %v after it came due, want it at the token, before %v",
			calls[at].At.Sub(injected), delivery.TokenMargin)
	}
	if last >= 0 && last < at {
		t.Errorf("the refused edit at call %d went before the Urgent Publication at call %d", last, at)
	}
}

// TestRefusalOfBothLimiters is C-11.FR-3 for #547: when both Destination 11's limiter and its Connection's refuse a
// take, the round leaves Destination 11, with an edit that is not Urgent, and every other Destination of the
// Connection alone; the worker wakes for the Connection's token, which Destination 14 takes TokenMargin later, while
// Destination 11 waits for its own.
func TestRefusalOfBothLimiters(t *testing.T) {
	e := roundEnv(t, 0)
	const dest14, route14 = 14, 4
	conn := int64(connID)
	e.db.dests[dest14] = &fakeDest{id: dest14, publicID: "DSAAAAAAAAAA14", name: "ops-2", typ: delivery.TypeMattermost,
		connection: &conn, health: "healthy", limit: 100_000, per: 1}
	r := e.db.routes[routeID]
	r.publicID = "RTAAAAAAAAAAA4"
	e.db.routes[route14] = r
	e.db.routeDests[route14] = []int64{dest14}
	for i := range 2 {
		e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
	}
	e.enqueue(t, newGroupOf(e.env, 2, "g", false), groups.System, created())
	e.enqueue(t, groupOn(e.env, route14, 500, "g"), groups.System, created())
	e.round(t)
	e.rec.Reset()
	for i := range 2 {
		e.enqueue(t, newGroupOf(e.env, i, "uedit", true), groups.System, annotatedEvent())
	}
	e.enqueue(t, newGroupOf(e.env, 2, "nedit", false), groups.System, annotatedEvent())
	e.enqueue(t, groupOn(e.env, route14, 500, "cedit"), groups.System, annotatedEvent())
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 2
	e.db.connLimits[connID] = [2]int64{1, 1}
	e.db.buckets = map[bucketKey]*fakeBucket{}
	takes := e.db.calls["TakeTokens"]
	next := e.round(t)
	if takes, sent := e.db.calls["TakeTokens"]-takes, len(e.rec.Calls()); takes != 2 || sent != 1 {
		t.Errorf("%d takes and %d calls in the round, want 2 and 1: Destination 14 left alone", takes, sent)
	}
	// The Connection's token comes first: at its time the work that is not Urgent still leaves the Connection alone,
	// TokenMargin later Destination 14 takes it, and Destination 11 waits for its own token as well.
	steps := []struct {
		wait  time.Duration
		takes int
		calls string
	}{{time.Second, 0, ""}, {delivery.TokenMargin, 1, "c"}, {800 * time.Millisecond, 0, "c"}}
	for i, step := range steps {
		if next != step.wait {
			t.Fatalf("step %d: the worker waits %v, want %v", i, next, step.wait)
		}
		e.business.Advance(next)
		takes = e.db.calls["TakeTokens"]
		next = e.round(t)
		if s := callsOn(e, dest14, map[string]string{"cedit": "c"}); s != step.calls ||
			e.db.calls["TakeTokens"]-takes != step.takes {
			t.Errorf("step %d: calls in Destination 14 %q after %d takes, want %q after %d", i, s,
				e.db.calls["TakeTokens"]-takes, step.calls, step.takes)
		}
	}
}

// TestLimitRaisedAfterARefusal is C-11.FR-3 for #554: a limit raised after a refusal ends the wait of the Urgent
// claims. Destination 11 at 1 call a minute refuses the second of three edits that are not Urgent; its limit is then
// raised to 100 a second, which keeps the state of its bucket, and 1 s later a Publication comes due there. The round
// reads the bucket again by the current limit, and an Urgent Publication goes out in it. One that is not Urgent waits
// for the refused edits, which are due again at the refused token's time plus TokenMargin, and goes then, ahead of
// them, as it is due earlier.
func TestLimitRaisedAfterARefusal(t *testing.T) {
	for _, urgent := range []bool{true, false} {
		t.Run(fmt.Sprintf("urgent %v", urgent), func(t *testing.T) {
			e := roundEnv(t, 0)
			for i := range 3 {
				e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
			}
			e.round(t)
			e.rec.Reset()
			for i := range 3 {
				e.enqueue(t, newGroup(e.env, i, "edit"), groups.System, annotatedEvent())
			}
			e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 60
			e.db.buckets = map[bucketKey]*fakeBucket{}
			refused := e.business.Now()
			if next := e.round(t); len(e.rec.Calls()) != 1 || next != delivery.MaxWait {
				t.Fatalf("%d calls in the first round and a wait of %v, want 1 and %v", len(e.rec.Calls()), next,
					delivery.MaxWait)
			}
			e.db.dests[destMM].limit, e.db.dests[destMM].per = 100, 1
			e.business.Advance(time.Second)
			e.enqueue(t, newGroupOf(e.env, 50, "fresh", urgent), groups.System, created())
			next := e.round(t)
			marks := map[string]string{"fresh": "F", "edit": "e"}
			if s, want := callsOn(e, destMM, marks), map[bool]string{true: "eF", false: "e"}[urgent]; s != want {
				t.Fatalf("calls in Destination 11 1 s after the limit was raised %q, want %q; the worker waits %v", s,
					want, next)
			}
			if urgent {
				return
			}
			runRounds(t, e, false, 20, func() bool { return strings.Contains(callsOn(e, destMM, marks), "F") })
			calls := e.rec.Calls()
			at := slices.IndexFunc(calls, func(c deliverytest.Call) bool { return strings.Contains(textOf(c), "fresh") })
			if s := callsOn(e, destMM, marks); s != "eFee" || at < 0 ||
				calls[at].At.Before(refused.Add(time.Minute+delivery.TokenMargin)) {
				t.Errorf("calls in Destination 11 %q, the Publication at %v after the refusal, want %q at %v", s,
					calls[max(at, 0)].At.Sub(refused), "eFee", time.Minute+delivery.TokenMargin)
			}
		})
	}
}

// TestUrgentDueInsideTheMargin is C-11.FR-3 for #554: the worker wakes at a refused token's time, when the batches of
// rank 1 due now may take it, not only TokenMargin later. Destination 11 at 1 call a second refuses an edit, and an
// Urgent Publication there comes due 1.05 s later without a NOTIFY, as after a Transient backoff: the round at the
// token finds it not due yet, and the worker wakes again at the end of the margin, when it goes.
func TestUrgentDueInsideTheMargin(t *testing.T) {
	e := roundEnv(t, 0)
	for i := range 3 {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
	}
	e.round(t)
	e.rec.Reset()
	for i := range 3 {
		e.enqueue(t, newGroup(e.env, i, "edit"), groups.System, annotatedEvent())
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	e.enqueue(t, newGroupOf(e.env, 50, "late", true), groups.System, created())
	late := e.db.deliveries[len(e.db.deliveries)-1]
	if !late.urgent || late.state != "pending" {
		t.Fatalf("the last delivery %+v, want the pending Urgent Publication", late)
	}
	late.next = e.business.Now().Add(time.Second + 50*time.Millisecond)
	if next := e.round(t); next != time.Second {
		t.Fatalf("the worker waits %v, want %v, until the refused token's time", next, time.Second)
	}
	e.business.Advance(time.Second)
	if next := e.round(t); next != delivery.TokenMargin {
		t.Fatalf("at the token the worker waits %v, want %v, until the end of the margin", next, delivery.TokenMargin)
	}
	e.business.Advance(delivery.TokenMargin)
	e.round(t)
	if s := callsOn(e, destMM, map[string]string{"late": "L", "edit": "e"}); s != "eL" {
		t.Errorf("calls in Destination 11 %q, want the edit of the bucket, then the Urgent Publication", s)
	}
}

// TestDestinationRefusalLeavesItsConnection is C-11.FR-3 for #547: a refusal of Destination 11's own limiter leaves
// the other Destinations of its Connection alone: Destination 14 sends its edits in the round.
func TestDestinationRefusalLeavesItsConnection(t *testing.T) {
	e := roundEnv(t, 0)
	const dest14, route14 = 14, 4
	conn := int64(connID)
	e.db.dests[dest14] = &fakeDest{id: dest14, publicID: "DSAAAAAAAAAA14", name: "ops-2", typ: delivery.TypeMattermost,
		connection: &conn, health: "healthy", limit: 100_000, per: 1}
	r := e.db.routes[routeID]
	r.publicID = "RTAAAAAAAAAAA4"
	e.db.routes[route14] = r
	e.db.routeDests[route14] = []int64{dest14}
	for i := range 2 {
		e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
	}
	for i := range 3 {
		e.enqueue(t, groupOn(e.env, route14, 500+i, "g"), groups.System, created())
	}
	e.round(t)
	e.rec.Reset()
	for i := range 2 {
		e.enqueue(t, newGroupOf(e.env, i, "uedit", true), groups.System, annotatedEvent())
	}
	for i := range 3 {
		e.enqueue(t, groupOn(e.env, route14, 500+i, "cedit"), groups.System, annotatedEvent())
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 60
	e.db.buckets = map[bucketKey]*fakeBucket{}
	e.round(t)
	u, c := callsOn(e, destMM, map[string]string{"uedit": "U"}), callsOn(e, dest14, map[string]string{"cedit": "c"})
	if u != "U" || c != "ccc" {
		t.Errorf("calls in Destination 11 %q and in Destination 14 %q, want %q and %q", u, c, "U", "ccc")
	}
}

// urgentAndConfiguration is a roundEnv with 8 Urgent edits that Alerts made and, due after them, 50 configuration
// edits, Urgent or not, waiting in Destination 11 at limit calls per per seconds, its bucket full.
func urgentAndConfiguration(t *testing.T, limit, per int64, urgentConfiguration bool) *eventsEnv {
	t.Helper()
	e := roundEnv(t, 0)
	const u, m = 8, 50
	for i := range u {
		e.enqueue(t, newGroupOf(e.env, i, "u", true), groups.System, created())
	}
	for i := range m {
		e.enqueue(t, newGroupOf(e.env, 100+i, "n", urgentConfiguration), groups.System, created())
	}
	for range 3 {
		e.round(t)
	}
	e.rec.Reset()
	for i := range u {
		e.enqueue(t, newGroupOf(e.env, i, "uedit", true), groups.System, annotatedEvent())
	}
	e.business.Advance(time.Millisecond)
	for i := range m {
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: newGroupOf(e.env, 100+i, "cfg",
			urgentConfiguration), Actor: groups.System, Configuration: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = limit, per
	e.db.buckets = map[bucketKey]*fakeBucket{}
	return e
}

// TestUrgentEditsFirstAfterARaise is C-11.FR-3 for #554 with a limit raised while the bucket is still empty: 8 Urgent
// edits and 50 configuration edits, Urgent or not, wait in Destination 11 at 1 call in 10 s, which then goes to 1 call
// in 2 s. The later ranks still wait until the refused edits are due again, so every Urgent edit goes first, whether
// the worker sleeps or is woken every MinWait.
func TestUrgentEditsFirstAfterARaise(t *testing.T) {
	for _, c := range []struct{ woken, urgent bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		woken := c.woken
		t.Run(fmt.Sprintf("woken %v, Urgent configuration edits %v", c.woken, c.urgent), func(t *testing.T) {
			e := urgentAndConfiguration(t, 1, 10, c.urgent)
			e.round(t)
			e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 2
			marks := map[string]string{"uedit": "U", "cfg": "c"}
			runRounds(t, e, woken, 6000, func() bool { return strings.Count(callsOn(e, destMM, marks), "U") == 8 })
			if s := callsOn(e, destMM, marks); !strings.HasPrefix(s, strings.Repeat("U", 8)) {
				t.Errorf("calls in Destination 11 %q, want the 8 Urgent edits first", s)
			}
		})
	}
}

// TestMemoCarriedWhenItsReadFails is C-11.FR-3 for #554: when the round cannot read the limiters of the memo again, it
// carries them as they were and says so. Destination 11 at 1 call a second refuses an Urgent edit; a round woken before
// the token fails to read them and still waits for that token, another one woken after the token but before its time
// plus TokenMargin fails too, and every Urgent edit still goes first.
func TestMemoCarriedWhenItsReadFails(t *testing.T) {
	e := urgentAndConfiguration(t, 1, 1, false)
	e.round(t)
	// Before the token, the Urgent claims still wait for it.
	e.business.Advance(500 * time.Millisecond)
	e.db.fail["LimiterDues"] = errBoom
	if next, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) || next != 500*time.Millisecond {
		t.Errorf("the round before the token after a failed read = %v, %v; want %v, %v", next, err,
			500*time.Millisecond, errBoom)
	}
	e.business.Advance(550 * time.Millisecond)
	if _, err := e.w.Round(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("the round after a failed read of the memo = %v, want %v", err, errBoom)
	}
	delete(e.db.fail, "LimiterDues")
	marks := map[string]string{"uedit": "U", "cfg": "c"}
	runRounds(t, e, true, 3000, func() bool { return strings.Count(callsOn(e, destMM, marks), "U") == 8 })
	if s := callsOn(e, destMM, marks); !strings.HasPrefix(s, strings.Repeat("U", 8)) {
		t.Errorf("calls in Destination 11 %q, want the 8 Urgent edits first", s)
	}
}

// TestUrgentAtTheTokenAfterARaise is C-11.FR-3 for #554: after a raise that leaves the bucket empty, the Urgent claims
// wait for the new token's time itself. Destination 11 at 1 call in 10 s refuses an edit and goes to 1 call in 2 s; an
// Urgent Publication that comes due 0.5 s later goes out at the new token, 2 s after the refusal, with no refused take.
func TestUrgentAtTheTokenAfterARaise(t *testing.T) {
	e := roundEnv(t, 0)
	for i := range 3 {
		e.enqueue(t, newGroup(e.env, i, "g"), groups.System, created())
	}
	e.round(t)
	e.rec.Reset()
	for i := range 3 {
		e.enqueue(t, newGroup(e.env, i, "edit"), groups.System, annotatedEvent())
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 10
	e.db.buckets = map[bucketKey]*fakeBucket{}
	t0 := e.business.Now()
	e.round(t)
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 2
	e.business.Advance(500 * time.Millisecond)
	e.enqueue(t, newGroupOf(e.env, 50, "fresh", true), groups.System, created())
	takes := e.db.calls["TakeTokens"]
	runRounds(t, e, false, 50, func() bool {
		return strings.Contains(callsOn(e, destMM, map[string]string{"fresh": "F"}), "F")
	})
	calls := e.rec.Calls()
	at := slices.IndexFunc(calls, func(c deliverytest.Call) bool { return strings.Contains(textOf(c), "fresh") })
	// The round's clock moves 2 ms a take.
	if at < 0 || calls[at].At.Sub(t0) != 2*time.Second+2*time.Millisecond || e.db.calls["TakeTokens"]-takes != 1 {
		t.Errorf("the Urgent Publication at %v after the refusal with %d takes, want at 2.002s with 1",
			calls[max(at, 0)].At.Sub(t0), e.db.calls["TakeTokens"]-takes)
	}
}

// TestUrgentConfigurationEditJudgedAtTheStartOfTheStep is C-11.FR-3 for #554 within one step of a visit: Destination
// 11 allows 1 call a second and holds 2 Urgent edits that Alerts made and an Urgent configuration edit; the second edit
// is refused in a round. The next round starts after that token is due but before its time plus TokenMargin: its drain
// sends an Urgent edit in Destination 13, during which an Urgent Publication there comes due, and the clock passes that
// time while the batch of rank 1 due now sends the Publication. The claim of rank 2 that follows still leaves
// Destination 11 alone, as the drain before it did, so the configuration edit never takes the token ahead of the
// refused edit, which goes first.
func TestUrgentConfigurationEditJudgedAtTheStartOfTheStep(t *testing.T) {
	e := roundEnv(t, 0)
	e.db.dests[destOther].limit, e.db.dests[destOther].per = 100_000, 1
	const u = 2
	for i := range u + 1 {
		e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
	}
	q := groupOn(e.env, routeOther, 1, "g")
	q.Urgent, e.db.groups[q.ID].urgent = true, true
	e.enqueue(t, q, groups.System, created())
	e.round(t)
	e.rec.Reset()
	for i := range u {
		e.enqueue(t, newGroupOf(e.env, i, "uedit", true), groups.System, annotatedEvent())
	}
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: newGroupOf(e.env, u, "cfg", true),
		Actor: groups.System, Configuration: true}); err != nil {
		t.Fatal(err)
	}
	e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 1
	e.db.buckets = map[bucketKey]*fakeBucket{}
	e.round(t)
	marks := map[string]string{"uedit": "U", "cfg": "c"}
	if s := callsOn(e, destMM, marks); s != "U" {
		t.Fatalf("calls in Destination 11 in the first round %q, want %q", s, "U")
	}
	e.business.Advance(time.Second + delivery.TokenMargin/2)
	q.Title = "qedit"
	e.enqueue(t, q, groups.System, annotatedEvent())
	takes := 0
	var tick func()
	tick = func() {
		takes++
		switch takes {
		case 1:
			p := groupOn(e.env, routeOther, 2, "urgent")
			p.Urgent, e.db.groups[p.ID].urgent = true, true
			e.enqueue(t, p, groups.System, created())
		case 2:
			e.business.Advance(time.Second)
		}
		e.db.before["TakeTokens"] = tick
	}
	e.db.before["TakeTokens"] = tick
	e.round(t)
	delete(e.db.before, "TakeTokens")
	p := callsOn(e, destOther, map[string]string{"qedit": "q", "urgent": "p"})
	if s := callsOn(e, destMM, marks); s != "U" || p != "qp" {
		t.Fatalf("calls in Destination 11 after the second round %q and in Destination 13 %q, want %q and %q", s, p,
			"U", "qp")
	}
	runRounds(t, e, true, 50, func() bool { return strings.Contains(callsOn(e, destMM, marks), "c") })
	if s := callsOn(e, destMM, marks); s != "UUc" {
		t.Fatalf("calls in Destination 11 %q, want %q", s, "UUc")
	}
}

// TestConnectionRefusalThenMove is C-11.FR-3: Connection 5 refuses an Urgent edit of Destination 11 — alone, or with
// Destination 11's own limiter, whose token comes sooner — and Destination 11 then moves to Connection 7, with room,
// with or without a re-render of its Urgent Root messages. The edits that are not Urgent of Destination 11 still wait
// for the refused Urgent edits, which go first.
func TestConnectionRefusalThenMove(t *testing.T) {
	for _, c := range []struct{ rerender, both bool }{{false, false}, {true, false}, {false, true}} {
		t.Run(fmt.Sprintf("re-render %v, both empty %v", c.rerender, c.both), func(t *testing.T) {
			e := roundEnv(t, 0)
			const u, m = 4, 4
			for i := range u {
				e.enqueue(t, newGroupOf(e.env, i, "g", true), groups.System, created())
			}
			for i := range m {
				e.enqueue(t, newGroupOf(e.env, 100+i, "g", false), groups.System, created())
			}
			e.round(t)
			e.rec.Reset()
			for i := range u {
				e.enqueue(t, newGroupOf(e.env, i, "uedit", true), groups.System, annotatedEvent())
			}
			e.business.Advance(time.Millisecond)
			for i := range m {
				e.enqueue(t, newGroupOf(e.env, 100+i, "nedit", false), groups.System, annotatedEvent())
			}
			if c.both {
				e.db.dests[destMM].limit, e.db.dests[destMM].per = 1, 1
			}
			e.db.connLimits[connID] = [2]int64{1, 10}
			e.db.buckets = map[bucketKey]*fakeBucket{}
			e.round(t)
			c7 := int64(7)
			e.db.dests[destMM].connection = &c7
			if c.rerender {
				for i := range u {
					if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: newGroupOf(e.env, i, "uedit",
						true), Actor: groups.System, Configuration: true}); err != nil {
						t.Fatal(err)
					}
				}
			}
			marks := map[string]string{"uedit": "U", "nedit": "n"}
			runRounds(t, e, true, 300, func() bool { return strings.Count(callsOn(e, destMM, marks), "U") == u })
			if s := callsOn(e, destMM, marks); !strings.HasPrefix(s, strings.Repeat("U", u)) {
				t.Errorf("calls in Destination 11 %q, want the %d Urgent edits first", s, u)
			}
		})
	}
}
