// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
)

// person is a User acting through the UI: the actor of a Command.
var person = groups.Actor{Kind: audit.ActorUser, Transport: audit.TransportUI}

// TestTableMatchesLifecycleEvents: the table of delivery has a row for every row of the lifecycle event tables of
// C-09.FR-22 and C-10.FR-15, with the same loudness and Mentions, resolved split by who resolved; the six rows of
// C-17.FR-11 complete the 32; a Loud event is always a new message and an edit is always Quiet (C-11.FR-7).
func TestTableMatchesLifecycleEvents(t *testing.T) {
	if len(delivery.Table) != 32 {
		t.Errorf("%d rows", len(delivery.Table))
	}
	seen := map[string]bool{}
	for _, r := range delivery.Table {
		key := string(r.Event) + "/" + string(r.Variant)
		if seen[key] {
			t.Errorf("%s twice", key)
		}
		seen[key] = true
		if r.Loudness == groups.Loud && r.Form != delivery.FormPublication && r.Form != delivery.FormReply &&
			r.Form != delivery.FormReplyAndUpdate {
			t.Errorf("%s is Loud without a new message", key)
		}
		if r.Form == delivery.FormUpdate && r.Loudness != groups.Quiet {
			t.Errorf("%s is a Loud edit", key)
		}
	}
	for _, g := range groups.Table {
		variants := []groups.Variant{g.Variant}
		if g.Event == groups.EventResolved {
			variants = []groups.Variant{delivery.VariantSystem, groups.VariantCommand}
		}
		for _, v := range variants {
			i := slices.IndexFunc(delivery.Table, func(r delivery.Row) bool { return r.Event == g.Event && r.Variant == v })
			if i < 0 {
				t.Errorf("no row for %s/%s", g.Event, v)
				continue
			}
			if r := delivery.Table[i]; r.Loudness != g.Loudness || !slices.Equal(r.Mentions, g.Mentions) {
				t.Errorf("%s/%s: %s %v, the lifecycle table says %s %v", g.Event, v, r.Loudness, r.Mentions,
					g.Loudness, g.Mentions)
			}
		}
	}
}

// TestLifecycleRows is C-11.FR-7, FR-20 and AC-10, table-driven: every row of the lifecycle event tables, keyed by
// event and variant, produces through the recording adapter a new Root message, an edit, a Thread reply, both, or
// nothing, Loud or Quiet and with the row's symbolic Mentions. The rows of C-17 are set up directly.
func TestLifecycleRows(t *testing.T) {
	for _, row := range delivery.Table {
		t.Run(string(row.Event)+"/"+string(row.Variant), func(t *testing.T) {
			e := newEnv(t)
			e.db.dests[destMM].limit = 600
			actor := groups.System
			if row.Variant == groups.VariantCommand {
				actor = person
			}
			ev := groups.Recorded{Seq: 2, Event: row.Event, Variant: row.Variant, Loudness: row.Loudness,
				Mentions: row.Mentions, Fingerprints: []string{"fp9"}}
			if row.Event == groups.EventResolved {
				ev.Variant = groups.VariantAny
			}
			if row.EventMentions {
				ev.Mentions = []groups.Mention{groups.MentionAckTimeout}
			}
			want := row.Mentions
			if row.EventMentions {
				want = ev.Mentions
			}
			if row.Form == delivery.FormPublication {
				e.enqueue(t, e.group(groups.StatusFiring, "a"), actor, ev)
				e.round(t)
				calls := e.rec.Calls()
				if len(calls) != 1 || calls[0].Method != deliverytest.MethodPublish || calls[0].Loudness != row.Loudness ||
					!slices.Equal(calls[0].Mentions, want) {
					t.Errorf("calls %+v", calls)
				}
				return
			}
			e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
			e.round(t)
			e.rec.Reset()
			// An event whose form updates the Root message changes what it shows; the others leave it as it is.
			title := "a"
			if row.Form == delivery.FormUpdate || row.Form == delivery.FormReplyAndUpdate {
				title = "b"
			}
			e.enqueue(t, e.group(groups.StatusFiring, title), actor, ev)
			e.round(t)
			var updates, replies []deliverytest.Call
			for _, c := range e.rec.Calls() {
				switch c.Method {
				case deliverytest.MethodUpdate:
					updates = append(updates, c)
				case deliverytest.MethodReply:
					replies = append(replies, c)
				default:
					t.Errorf("unexpected %+v", c)
				}
			}
			wantUpdates := map[delivery.Form]int{delivery.FormUpdate: 1, delivery.FormReplyAndUpdate: 1}[row.Form]
			wantReplies := map[delivery.Form]int{delivery.FormReply: 1, delivery.FormReplyAndUpdate: 1}[row.Form]
			if len(updates) != wantUpdates || len(replies) != wantReplies {
				t.Fatalf("form %s: %d updates, %d replies", row.Form, len(updates), len(replies))
			}
			for _, u := range updates {
				if u.Loudness != groups.Quiet || len(u.Mentions) != 0 || u.MessageID != "m1" {
					t.Errorf("update %+v", u)
				}
			}
			for _, r := range replies {
				if r.Loudness != row.Loudness || !slices.Equal(r.Mentions, want) || r.MessageID != "m1" ||
					!strings.Contains(r.Message.Text(), string(row.Event)) {
					t.Errorf("reply %+v", r)
				}
			}
		})
	}
}

// TestEnqueue covers the edges of the re-render step: an Alert Group whose Route has no Destination that is not
// deleted delivers nothing; an event outside the table is an error; the receipt time
// of the oldest undelivered Snapshot is kept; a delivery whose message did not change follows the urgency of its Alert
// Group without a new version; the Route's membership lock is taken shared before its Destinations are read.
func TestEnqueue(t *testing.T) {
	e := newEnv(t)
	e.db.routeDests[routeID] = nil
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	if len(e.db.deliveries) != 0 || e.db.notified != 0 {
		t.Errorf("no destination: %d deliveries", len(e.db.deliveries))
	}
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	e.db.dests[destWH].deleted = true
	err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring, "a"),
		Actor: groups.System, Events: []groups.Recorded{{Event: "exploded"}}})
	if err == nil || !strings.Contains(err.Error(), "no row in the delivery table") {
		t.Errorf("unknown event = %v", err)
	}
	t1, t2 := business0.Add(-5*time.Second), business0.Add(-time.Second)
	for i, rcv := range []*time.Time{&t1, &t2, nil} {
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring,
			fmt.Sprint(i)), Actor: groups.System, ReceivedAt: rcv}); err != nil {
			t.Fatal(err)
		}
	}
	d := e.only(t)
	if d.version != 3 || !d.receivedAt.Equal(t1) {
		t.Errorf("received %v at version %d", d.receivedAt, d.version)
	}
	d.urgent = true
	e.enqueue(t, e.group(groups.StatusFiring, "2"), groups.System)
	if d.version != 3 || d.urgent {
		t.Errorf("urgency %+v", d)
	}
	// The membership lock of the Route is taken shared before its Destinations are read.
	e.db.membershipLocks = nil
	e.db.before["ListRouteDestinations"] = func() {
		if !slices.Equal(e.db.membershipLocks, []int64{routeID}) {
			t.Errorf("the destinations were read under the locks %v", e.db.membershipLocks)
		}
	}
	e.enqueue(t, e.group(groups.StatusFiring, "2"), groups.System)
	for _, q := range []string{"ShareRouteMembership", "ListRouteDestinations", "GetRouteDelivery", "EnsureDelivery",
		"SetDesired", "SetDeliveryUrgent", "NotifyDelivery", "InsertThreadReply"} {
		e.db.fail[q] = errBoom
		title := "2"
		if q == "SetDesired" {
			title = "changed"
		}
		err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring, title),
			Actor: groups.System, Events: []groups.Recorded{{Seq: 9, Event: groups.EventTakeover}}})
		if !errors.Is(err, errBoom) {
			t.Errorf("%s = %v", q, err)
		}
		delete(e.db.fail, q)
	}
}
