// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
)

// person is a User acting through the UI: the actor of a Command.
var person = groups.Actor{Kind: audit.ActorUser, Transport: audit.TransportUI}

// TestTableMatchesLifecycleEvents: the table of delivery has a row for every row of the lifecycle event tables of
// C-09.FR-22 and C-10.FR-15, with the same loudness and Mentions, resolved split by who resolved; the six rows of
// C-17.FR-11, the two of C-25 — team_joined, a Root message update, and unknown_team_value, nothing (C-11.FR-20
// revised) — and the three of destination_joined of C-26, a new Root message, complete the 37; a Loud event is always a
// new message and an edit is always Quiet (C-11.FR-7).
func TestTableMatchesLifecycleEvents(t *testing.T) {
	if len(delivery.Table) != 37 {
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
			if row.Event == groups.EventDestinationJoined {
				// destination_joined publishes in the Destination it names, after a Publication elsewhere.
				ev.Destination = &groups.JoinedDestination{ID: destMM, PublicID: "DSAAAAAAAAAA11", Name: "ops",
					Type: delivery.TypeMattermost}
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
					!strings.Contains(textOf(r), string(row.Event)) {
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

// TestTeamsEvaluatedIsQuiet is C-25.FR-13 and ADR-0018 in delivery: a rendering of the evaluation of the Teams on a
// save, marked as a change of configuration and carrying its Quiet team_joined, that changes the Desired state of a
// Not delivered Publication which was never published publishes it Quietly — the same rendering unmarked, as an Alert
// that joins brings it, keeps the Loud Publication — and releases an Urgent Alert Group that a Storm holds Quietly.
func TestTeamsEvaluatedIsQuiet(t *testing.T) {
	joined := groups.Recorded{Seq: 2, Event: groups.EventTeamJoined, Variant: groups.VariantAny,
		Loudness: groups.Quiet}
	for _, c := range []struct {
		name          string
		configuration bool
		want          groups.Loudness
	}{{"the teams evaluated", true, groups.Quiet}, {"a team an alert brings", false, groups.Loud}} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System, created())
			d := e.only(t)
			d.state, d.publishedAt = "not_delivered", nil
			if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring,
				"disk full on two hosts"), Actor: groups.System, Events: []groups.Recorded{joined},
				Configuration: c.configuration}); err != nil {
				t.Fatal(err)
			}
			if d := e.only(t); d.state != "pending" || d.loud.Bool != (c.want == groups.Loud) {
				t.Fatalf("the delivery is %s, loud %+v", d.state, d.loud)
			}
			e.round(t)
			if p := e.publications(); len(p) != 1 || p[0].Loudness != c.want {
				t.Errorf("published %+v", p)
			}
		})
	}

	e := stormEnv(t)
	r := e.db.routes[routeID]
	r.threshold = 2
	e.db.routes[routeID] = r
	id := int64(5000)
	for range 4 {
		id++
		e.addGroup(t, id, false)
	}
	e.onlyStorm(t)
	e.round(t)
	e.rec.Reset()
	g := e.view(id, groups.StatusFiring)
	g.Urgent = true
	e.db.groups[id].urgent = true
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
		Events: []groups.Recorded{joined}, Configuration: true}); err != nil {
		t.Fatal(err)
	}
	e.round(t)
	if p := e.publications(); len(p) != 1 || p[0].Loudness != groups.Quiet ||
		!strings.HasPrefix(textOf(p[0]), fmt.Sprintf("#%d ", id)) {
		t.Errorf("released by the teams evaluated %+v", p)
	}
}

// TestMessageSettingsRerenderIsQuiet is C-29.FR-1 to FR-3 and ADR-0018 in delivery: the re-render of the open Root
// messages after a change of the Organization's message settings, or of a Team's name, has no lifecycle event and is
// marked as a change of configuration; when it changes the Desired state of a Not delivered Publication that was never
// published, it publishes it Quietly, once. When the evaluation of the Teams that follows it in the same transaction
// brings a Team, whose Quiet team_joined renders the Alert Group again with another Desired state, the later state is
// stored over the earlier one before the worker runs, so that the Root message is still published once, Quietly, with
// the later state; and when that rendering finds the hash unchanged, it changes nothing.
func TestMessageSettingsRerenderIsQuiet(t *testing.T) {
	joined := groups.Recorded{Seq: 2, Event: groups.EventTeamJoined, Variant: groups.VariantAny,
		Loudness: groups.Quiet}
	type render struct {
		title  string
		events []groups.Recorded
	}
	for _, c := range []struct {
		name     string
		renders  []render
		versions int64
		want     string
	}{
		{"the re-render alone", []render{{"disk full in Europe/Berlin", nil}}, 1, "disk full in Europe/Berlin"},
		{"the re-render, then a team joined", []render{{"disk full in Europe/Berlin", nil},
			{"disk full in Europe/Berlin, team ops", []groups.Recorded{joined}}}, 2,
			"disk full in Europe/Berlin, team ops"},
		{"the re-render, then a rendering with the same state", []render{{"disk full in Europe/Berlin", nil},
			{"disk full in Europe/Berlin", []groups.Recorded{joined}}}, 1, "disk full in Europe/Berlin"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System, created())
			d := e.only(t)
			d.state, d.publishedAt = "not_delivered", nil
			version := d.version
			for _, r := range c.renders {
				if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.group(groups.StatusFiring,
					r.title), Actor: groups.System, Events: r.events, Configuration: true}); err != nil {
					t.Fatal(err)
				}
			}
			if d := e.only(t); d.state != "pending" || d.loud.Bool || d.version != version+c.versions {
				t.Fatalf("the delivery is %s, loud %+v, version %d after %d", d.state, d.loud, d.version, version)
			}
			e.round(t)
			if p := e.publications(); len(p) != 1 || p[0].Loudness != groups.Quiet ||
				!strings.Contains(textOf(p[0]), c.want) || strings.Contains(textOf(p[0]), c.want+",") {
				t.Errorf("published %+v", p)
			}
		})
	}
}

// appendEntries appends to the entries of a delivery those of add that it lacks, by kind and Team, as EnsureDelivery
// does.
func appendEntries(have, add []byte) ([]byte, error) {
	var cur, more []routing.Reaching
	if len(have) > 0 {
		if err := json.Unmarshal(have, &cur); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal(add, &more); err != nil {
		return nil, err
	}
	for _, e := range more {
		if !slices.ContainsFunc(cur, func(c routing.Reaching) bool { return c.Kind == e.Kind && c.TeamID == e.TeamID }) {
			cur = append(cur, e)
		}
	}
	if cur == nil {
		cur = []routing.Reaching{}
	}
	return json.Marshal(cur)
}

// teamReaches reports whether a Team of the Alert Group names the Destination on a Route with deliver_to_teams, as
// RecordDelivered reads it.
func (f *fakeDB) teamReaches(g *fakeGroup, dest int64) bool {
	if !f.routes[g.route].toTeams {
		return false
	}
	for _, team := range g.teams {
		if slices.ContainsFunc(f.teamDests[team], func(e routing.Entry) bool { return e.DestinationID == dest }) {
			return true
		}
	}
	return false
}

func (f *fakeDB) routeRow(d *fakeDest) dbgen.ListRouteDestinationsRow {
	return dbgen.ListRouteDestinationsRow{ID: d.id, PublicID: d.publicID, Name: d.name, Type: d.typ,
		ConnectionID: nullInt(d.connection), Health: d.health, WebhookMode: pgtype.Text{String: d.mode,
			Valid: d.mode != ""}, WebhookReplies: d.typ == delivery.TypeWebhook && !d.noReplies, Buttons: d.buttons,
		OrganizationButtons: f.orgButtons}
}

func (f *fakeDB) ListTeamDestinations(_ context.Context, arg dbgen.ListTeamDestinationsParams) (
	[]dbgen.ListTeamDestinationsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListTeamDestinations"); err != nil {
		return nil, err
	}
	var out []dbgen.ListTeamDestinationsRow
	for _, team := range slices.Sorted(slices.Values(arg.TeamIds)) {
		for _, e := range f.teamDests[team] {
			if d := f.dests[e.DestinationID]; d != nil && !d.deleted {
				r := f.routeRow(d)
				out = append(out, dbgen.ListTeamDestinationsRow{TeamID: team, ID: r.ID, PublicID: r.PublicID,
					Name: r.Name, Type: r.Type, ConnectionID: r.ConnectionID, Health: r.Health,
					WebhookMode: r.WebhookMode, WebhookReplies: r.WebhookReplies, Buttons: r.Buttons,
					OrganizationButtons: r.OrganizationButtons, SeverityCondition: string(e.Condition)})
			}
		}
	}
	return out, nil
}

func (f *fakeDB) ListGroupDestinations(_ context.Context, arg dbgen.ListGroupDestinationsParams) (
	[]dbgen.ListGroupDestinationsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupDestinations"); err != nil {
		return nil, err
	}
	var out []dbgen.ListGroupDestinationsRow
	for _, id := range slices.Sorted(maps.Keys(f.dests)) {
		d := f.dests[id]
		if d.deleted {
			continue
		}
		var had *fakeDelivery
		for _, x := range f.deliveries {
			if x.group == arg.AlertGroupID && x.dest == id {
				had = x
			}
		}
		events := slices.ContainsFunc(f.webhookEvents, func(e *fakeEvent) bool {
			return e.group == arg.AlertGroupID && e.dest == id
		})
		if had == nil && !events {
			continue
		}
		r := f.routeRow(d)
		row := dbgen.ListGroupDestinationsRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Type: r.Type,
			ConnectionID: r.ConnectionID, Health: r.Health, WebhookMode: r.WebhookMode,
			WebhookReplies: r.WebhookReplies, Buttons: r.Buttons, OrganizationButtons: r.OrganizationButtons}
		if had != nil {
			row.Delivered, row.State, row.HeldByStormID, row.Retiring = true, had.state, heldOf(had), had.desiredRetire
		}
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeDB) ListDestinationsByID(_ context.Context, arg dbgen.ListDestinationsByIDParams) (
	[]dbgen.ListDestinationsByIDRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListDestinationsByID"); err != nil {
		return nil, err
	}
	var out []dbgen.ListDestinationsByIDRow
	for _, id := range arg.Ids {
		if d := f.dests[id]; d != nil && !d.deleted {
			r := f.routeRow(d)
			out = append(out, dbgen.ListDestinationsByIDRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name,
				Type: r.Type, ConnectionID: r.ConnectionID, Health: r.Health, WebhookMode: r.WebhookMode,
				WebhookReplies: r.WebhookReplies, Buttons: r.Buttons, OrganizationButtons: r.OrganizationButtons})
		}
	}
	return out, nil
}

func (f *fakeDB) ListEntryTeams(_ context.Context, arg dbgen.ListEntryTeamsParams) ([]dbgen.ListEntryTeamsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListEntryTeams"); err != nil {
		return nil, err
	}
	var out []dbgen.ListEntryTeamsRow
	for _, id := range arg.Ids {
		if t, ok := f.teamRefs[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// The Destinations of the reach tests: the Route's infra-notice is destMM below critical and infra-critical
// critical; the Team payments has payments-notice below critical and payments-critical critical.
const (
	destCritical    = 13
	destTeamNotice  = 14
	destTeamCrit    = 15
	teamPayments    = 7
	stormOfTheRoute = 900
)

// reachEnv is newEnv with the Route's Destinations conditioned, "Also to the Teams from the label" on and the Team
// payments with its two Destinations; the Alert Group of the fixtures is warning.
func reachEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	conn := int64(connID)
	for id, name := range map[int64]string{destCritical: "infra-critical", destTeamNotice: "payments-notice",
		destTeamCrit: "payments-critical"} {
		e.db.dests[id] = &fakeDest{id: id, publicID: fmt.Sprintf("DSAAAAAAAAAA%02d", id), name: name,
			typ: delivery.TypeMattermost, connection: &conn, health: "healthy", limit: 600, per: 60}
	}
	e.db.dests[destMM].limit = 600
	e.db.routeDests[routeID] = []int64{destMM, destCritical}
	e.db.conditions = map[int64]map[int64]string{routeID: {destMM: "below_critical", destCritical: "critical"}}
	r := e.db.routes[routeID]
	r.toTeams = true
	e.db.routes[routeID] = r
	e.db.teamDests = map[int64][]routing.Entry{teamPayments: {
		{DestinationID: destTeamNotice, Condition: routing.ConditionBelowCritical},
		{DestinationID: destTeamCrit, Condition: routing.ConditionCritical}}}
	e.db.teamRefs = map[int64]dbgen.ListEntryTeamsRow{teamPayments: {ID: teamPayments, PublicID: "TMAAAAAAAAAAA7",
		Name: "Payments", Slug: "payments"}}
	return e
}

// render renders the Alert Group of the fixtures as the dispatcher does, with its Severity level, Teams and events.
func (e *env) render(t *testing.T, g *groups.Group, actor groups.Actor, configuration bool, teams []int64,
	events ...groups.Recorded) {
	t.Helper()
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: actor, Events: events,
		Teams: teams, Configuration: configuration}); err != nil {
		t.Fatal(err)
	}
}

// leveled is the Alert Group of the fixtures in a status at a Severity level, published once published is set.
func (e *env) leveled(status groups.Status, level string, published bool) *groups.Group {
	g := e.group(status, "disk full "+level)
	g.Severity = organization.SeverityLevel(level)
	g.Urgent = level == "critical"
	if published {
		g.FirstPublishedAt = at(business0)
	}
	return g
}

// joinedEvent is the destination_joined of the Destination dest, Loud while firing.
func joinedEvent(seq, dest int64, status groups.Status) groups.Recorded {
	r := groups.Recorded{Seq: seq, Event: groups.EventDestinationJoined, Variant: groups.VariantFiring,
		Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionNewAlertGroup},
		Destination: &groups.JoinedDestination{ID: dest, PublicID: fmt.Sprintf("DSAAAAAAAAAA%02d", dest)}}
	if status != groups.StatusFiring {
		r.Variant, r.Loudness, r.Mentions = groups.Variant(status), groups.Quiet, nil
	}
	return r
}

// published is the one Publication to a Destination.
func (e *env) published(t *testing.T, dest int64) deliverytest.Call {
	t.Helper()
	var out []deliverytest.Call
	for _, c := range e.callsTo(dest) {
		if c.Method == deliverytest.MethodPublish {
			out = append(out, c)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%d publications to %d: %+v", len(out), dest, e.callsTo(dest))
	}
	return out[0]
}

// repliesTo are the Thread replies queued in a Destination.
func (e *env) repliesTo(dest int64) []*fakeReply {
	var out []*fakeReply
	for _, r := range e.db.replies {
		if r.dest == dest {
			out = append(out, r)
		}
	}
	return out
}

// TestEnqueueReach is C-26.FR-2, FR-3, AC-1, AC-2 and AC-10 in Enqueue: a warning Alert Group of the Team payments on
// a Route with "Also to the Teams from the label" is published Loudly in the Route's and the Team's Destinations below
// critical and nowhere else, each delivery with the entry that reached it, which the delivery state names; with the
// option off it reaches only the Route's.
func TestEnqueueReach(t *testing.T) {
	e := reachEnv(t)
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
		created())
	e.round(t)
	for _, dest := range []int64{destMM, destTeamNotice} {
		if c := e.published(t, dest); c.Loudness != groups.Loud ||
			!slices.Equal(c.Mentions, []groups.Mention{groups.MentionNewAlertGroup}) {
			t.Errorf("publication to %d %+v", dest, c)
		}
	}
	if e.deliveryOf(groupID, destCritical) != nil || e.deliveryOf(groupID, destTeamCrit) != nil {
		t.Error("a critical Destination got a warning Alert Group")
	}
	states, err := e.svc.States(t.Context(), "AGAAAAAAAAAA21")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, st := range states {
		var parts []string
		for _, en := range st.Entries {
			p := en.Kind + "/" + en.Condition
			if en.Team != nil {
				p += "/" + en.Team.Slug
			}
			parts = append(parts, p)
		}
		got[st.Destination.Name] = strings.Join(parts, ",")
	}
	if want := map[string]string{"ops": "route/below_critical", "payments-notice": "team/below_critical/payments"}; !maps.Equal(got, want) {
		t.Errorf("entries %v", got)
	}

	// Without the option only the Route's Destinations are reached.
	e = reachEnv(t)
	r := e.db.routes[routeID]
	r.toTeams = false
	e.db.routes[routeID] = r
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
		created())
	if e.deliveryOf(groupID, destMM) == nil || e.deliveryOf(groupID, destTeamNotice) != nil ||
		e.db.calls["ListTeamDestinations"] != 0 {
		t.Errorf("deliveries %+v", e.db.deliveries)
	}
}

// TestEnqueueDestinationJoined is C-26.FR-4, FR-5, AC-4, AC-5 and C-11.FR-20 revised: destination_joined publishes the
// Alert Group in the Destination it names, Loud with new_alert_group while it fires and Quiet while it is acknowledged,
// without the Thread replies of the change, while the Destinations it had get the update and the replies; a rise to
// critical publishes in the critical Destinations and keeps the ones below critical current, with their entries.
func TestEnqueueDestinationJoined(t *testing.T) {
	for _, status := range []groups.Status{groups.StatusFiring, groups.StatusAcknowledged} {
		t.Run(string(status), func(t *testing.T) {
			e := reachEnv(t)
			e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, nil, created())
			e.round(t)
			e.rec.Reset()
			added := groups.Recorded{Seq: 2, Event: groups.EventAlertsAdded, Variant: groups.VariantFiring,
				Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionNewAlerts}}
			if status != groups.StatusFiring {
				added = groups.Recorded{Seq: 2, Event: groups.EventAlertsAdded, Variant: groups.VariantAcknowledged,
					Loudness: groups.Quiet}
			}
			e.render(t, e.leveled(status, "warning", true), groups.System, false, []int64{teamPayments}, added,
				joinedEvent(3, destTeamNotice, status))
			e.round(t)
			c := e.published(t, destTeamNotice)
			if status == groups.StatusFiring && (c.Loudness != groups.Loud ||
				!slices.Equal(c.Mentions, []groups.Mention{groups.MentionNewAlertGroup})) {
				t.Errorf("loud join %+v", c)
			}
			if status != groups.StatusFiring && (c.Loudness != groups.Quiet || len(c.Mentions) != 0) {
				t.Errorf("quiet join %+v", c)
			}
			if len(e.repliesTo(destTeamNotice)) != 0 || len(e.repliesTo(destMM)) != 1 {
				t.Errorf("replies %+v", e.db.replies)
			}
		})
	}

	t.Run("rise", func(t *testing.T) {
		e := reachEnv(t)
		e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
			created())
		e.round(t)
		e.rec.Reset()
		rise := groups.Recorded{Seq: 4, Event: groups.EventSeverityRaised, Loudness: groups.Quiet}
		e.render(t, e.leveled(groups.StatusFiring, "critical", true), groups.System, false, []int64{teamPayments},
			rise, joinedEvent(5, destCritical, groups.StatusFiring), joinedEvent(6, destTeamCrit, groups.StatusFiring))
		e.round(t)
		for _, dest := range []int64{destCritical, destTeamCrit} {
			if c := e.published(t, dest); c.Loudness != groups.Loud {
				t.Errorf("rise into %d %+v", dest, c)
			}
		}
		for _, dest := range []int64{destMM, destTeamNotice} {
			calls := e.callsTo(dest)
			if len(calls) != 1 || calls[0].Method != deliverytest.MethodUpdate ||
				!strings.Contains(textOf(calls[0]), "critical") {
				t.Errorf("the Root message below critical in %d %+v", dest, calls)
			}
		}
		if d := e.deliveryOf(groupID, destMM); !strings.Contains(string(d.entries), `"route"`) {
			t.Errorf("entries %s", d.entries)
		}

		// The critical Alert resolves: every Root message stays and is edited to the current state.
		e.rec.Reset()
		e.render(t, e.leveled(groups.StatusFiring, "warning", true), groups.System, false, []int64{teamPayments},
			groups.Recorded{Seq: 7, Event: groups.EventAlertResolved, Loudness: groups.Quiet})
		e.round(t)
		for _, dest := range []int64{destMM, destCritical, destTeamNotice, destTeamCrit} {
			if calls := e.callsTo(dest); len(calls) != 1 || calls[0].Method != deliverytest.MethodUpdate {
				t.Errorf("after the fall %d %+v", dest, calls)
			}
		}
	})
}

// TestEnqueueReopenRise is C-26.FR-4 and FR-5 for a Reopen: a Reopen above the level the Alert Group resolved at
// records its rise, Quiet, with destination_joined for the Destinations of the higher level, which get their first
// Publication Loud, while the Destinations it had get the update and the Reopen's Thread reply; a Reopen at the same or
// a lower level records no rise and publishes nowhere new.
func TestEnqueueReopenRise(t *testing.T) {
	reopened := groups.Recorded{Seq: 3, Event: groups.EventReopened, Variant: groups.VariantFiring,
		Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionReopen}}
	t.Run("higher", func(t *testing.T) {
		e := reachEnv(t)
		e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
			created())
		e.round(t)
		e.rec.Reset()
		raised := groups.Recorded{Seq: 4, Event: groups.EventUrgencyRaised, Variant: groups.VariantRemovesNone,
			Loudness: groups.Quiet}
		e.render(t, e.leveled(groups.StatusFiring, "critical", true), groups.System, false, []int64{teamPayments},
			reopened, raised, joinedEvent(5, destCritical, groups.StatusFiring),
			joinedEvent(6, destTeamCrit, groups.StatusFiring))
		e.round(t)
		for _, dest := range []int64{destCritical, destTeamCrit} {
			if c := e.published(t, dest); c.Loudness != groups.Loud ||
				!slices.Equal(c.Mentions, []groups.Mention{groups.MentionNewAlertGroup}) {
				t.Errorf("reopen into %d %+v", dest, c)
			}
			if len(e.repliesTo(dest)) != 0 {
				t.Errorf("replies in %d", dest)
			}
		}
		for _, dest := range []int64{destMM, destTeamNotice} {
			if len(e.repliesTo(dest)) != 1 {
				t.Errorf("the reopen's reply in %d %+v", dest, e.db.replies)
			}
		}
	})
	for _, c := range []struct{ name, from, to string }{{"same", "warning", "warning"},
		{"lower", "critical", "warning"}} {
		t.Run(c.name, func(t *testing.T) {
			e := reachEnv(t)
			e.render(t, e.leveled(groups.StatusFiring, c.from, false), groups.System, false, []int64{teamPayments},
				created())
			e.round(t)
			had := len(e.db.deliveries)
			e.render(t, e.leveled(groups.StatusFiring, c.to, true), groups.System, false, []int64{teamPayments},
				reopened)
			if n := len(e.db.deliveries); n != had {
				t.Errorf("the reopen made %d deliveries, had %d: %+v", n, had, e.db.deliveries)
			}
		})
	}
}

// TestEnqueueConfigurationQuiet is C-26.FR-9, AC-11, C-25.FR-13 and AC-13, ADR-0018: a Destination that a change of
// configuration brings into the reach — a raised Minimum severity, a Team evaluated on a save — gets a Quiet
// Publication without Thread replies; a delivery Not delivered and never published is not made Loud by it; one that a
// Team or a rise adds before the first Publication is part of it, Loud; and a Command that finds a reached
// Destination without a delivery publishes there Quietly.
func TestEnqueueConfigurationQuiet(t *testing.T) {
	t.Run("minimum severity", func(t *testing.T) {
		e := reachEnv(t)
		e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
			created())
		e.round(t)
		e.rec.Reset()
		raised := groups.Recorded{Seq: 2, Event: groups.EventUrgencyRaised, Variant: groups.VariantRemovesNone,
			Loudness: groups.Quiet, Reason: groups.ReasonMinimumSeverity}
		e.render(t, e.leveled(groups.StatusFiring, "critical", true), groups.System, false, []int64{teamPayments},
			raised)
		e.round(t)
		for _, dest := range []int64{destCritical, destTeamCrit} {
			if c := e.published(t, dest); c.Loudness != groups.Quiet || len(c.Mentions) != 0 {
				t.Errorf("publication to %d %+v", dest, c)
			}
			if len(e.repliesTo(dest)) != 0 {
				t.Errorf("replies in %d", dest)
			}
		}
	})
	t.Run("teams evaluated", func(t *testing.T) {
		e := reachEnv(t)
		e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, nil, created())
		e.round(t)
		e.rec.Reset()
		e.render(t, e.leveled(groups.StatusFiring, "warning", true), groups.System, true, []int64{teamPayments},
			groups.Recorded{Seq: 2, Event: groups.EventTeamJoined, Loudness: groups.Quiet, Team: teamPayments})
		e.round(t)
		if c := e.published(t, destTeamNotice); c.Loudness != groups.Quiet || len(c.Mentions) != 0 {
			t.Errorf("publication %+v", c)
		}
	})
	t.Run("not delivered is not revived loud", func(t *testing.T) {
		e := reachEnv(t)
		e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, nil, created())
		d := e.deliveryOf(groupID, destMM)
		d.state = "not_delivered"
		g := e.leveled(groups.StatusFiring, "warning", true)
		g.Title = "disk full with its team"
		e.render(t, g, groups.System, true, []int64{teamPayments},
			groups.Recorded{Seq: 2, Event: groups.EventTeamJoined, Loudness: groups.Quiet, Team: teamPayments})
		e.round(t)
		for _, dest := range []int64{destMM, destTeamNotice} {
			if c := e.published(t, dest); c.Loudness != groups.Quiet {
				t.Errorf("publication to %d %+v", dest, c)
			}
		}
	})
	t.Run("before the first publication", func(t *testing.T) {
		e := reachEnv(t)
		e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, nil, created())
		e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
			groups.Recorded{Seq: 2, Event: groups.EventAlertsAdded, Variant: groups.VariantFiring,
				Loudness: groups.Loud, Mentions: []groups.Mention{groups.MentionNewAlerts}},
			groups.Recorded{Seq: 3, Event: groups.EventTeamJoined, Loudness: groups.Quiet, Team: teamPayments})
		e.round(t)
		if c := e.published(t, destTeamNotice); c.Loudness != groups.Loud ||
			!slices.Equal(c.Mentions, []groups.Mention{groups.MentionNewAlertGroup}) {
			t.Errorf("publication %+v", c)
		}
	})
	t.Run("a command creates no delivery", func(t *testing.T) {
		e := reachEnv(t)
		e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, nil, created())
		e.round(t)
		e.rec.Reset()
		e.render(t, e.leveled(groups.StatusAcknowledged, "warning", true), person, false, []int64{teamPayments},
			groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
		e.round(t)
		if e.deliveryOf(groupID, destTeamNotice) != nil || len(e.callsTo(destMM)) != 1 {
			t.Errorf("deliveries %+v, calls %+v", e.db.deliveries, e.rec.Calls())
		}
	})
}

// TestEnqueueFallAddsNothing is C-26.FR-4 (decision M2 of the review): the Destinations grow only by Alerts, a Team, a
// rise or a change of configuration; a critical Alert Group whose critical Alert resolves, leaving it warning, updates
// its Root messages in the critical Destinations and creates no delivery in the Destinations below critical.
func TestEnqueueFallAddsNothing(t *testing.T) {
	e := reachEnv(t)
	e.render(t, e.leveled(groups.StatusFiring, "critical", false), groups.System, false, []int64{teamPayments},
		created())
	e.round(t)
	e.rec.Reset()
	e.render(t, e.leveled(groups.StatusFiring, "warning", true), groups.System, false, []int64{teamPayments},
		groups.Recorded{Seq: 2, Event: groups.EventAlertResolved, Loudness: groups.Quiet})
	e.round(t)
	if e.deliveryOf(groupID, destMM) != nil || e.deliveryOf(groupID, destTeamNotice) != nil {
		t.Errorf("a fall created deliveries below critical: %+v", e.db.deliveries)
	}
	for _, dest := range []int64{destCritical, destTeamCrit} {
		if calls := e.callsTo(dest); len(calls) != 1 || calls[0].Method != deliverytest.MethodUpdate {
			t.Errorf("the critical root message in %d %+v", dest, calls)
		}
	}

	// Neither Alerts that bring no Team nor rise the level (C-26.FR-5), nor the re-render of a change of configuration
	// that moves no reach — a renamed Team, "Labels in the message", a button set — add a Destination after the fall.
	g := e.leveled(groups.StatusFiring, "warning", true)
	g.Title = "after the fall"
	e.render(t, g, groups.System, false, []int64{teamPayments}, groups.Recorded{Seq: 3,
		Event: groups.EventAlertsAdded, Variant: groups.VariantFiring, Loudness: groups.Loud,
		Mentions: []groups.Mention{groups.MentionNewAlerts}})
	g.Title = "renamed team"
	e.render(t, g, groups.System, true, []int64{teamPayments})
	if e.deliveryOf(groupID, destMM) != nil || e.deliveryOf(groupID, destTeamNotice) != nil {
		t.Errorf("new alerts or a re-render created deliveries below critical: %+v", e.db.deliveries)
	}
}

// TestEnqueueKeepsJoined (finding L2 of the review): a Destination the dispatcher recorded destination_joined for gets
// its delivery, with the entries the dispatcher read, even when the reach Enqueue reads again no longer has it — a
// Team saved between the two reads; a Destination deleted meanwhile gets none.
func TestEnqueueKeepsJoined(t *testing.T) {
	e := reachEnv(t)
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, nil, created())
	e.db.teamDests = map[int64][]routing.Entry{}
	joined := joinedEvent(2, destTeamNotice, groups.StatusFiring)
	joined.Destination.Entries = []routing.Reaching{{Kind: routing.EntryTeam, TeamID: teamPayments,
		Condition: routing.ConditionBelowCritical}}
	e.render(t, e.leveled(groups.StatusFiring, "warning", true), groups.System, false, []int64{teamPayments},
		groups.Recorded{Seq: 1, Event: groups.EventTeamJoined, Loudness: groups.Quiet, Team: teamPayments}, joined)
	d := e.deliveryOf(groupID, destTeamNotice)
	if d == nil || !d.loud.Bool || !strings.Contains(string(d.entries), `"team_id":7`) {
		t.Fatalf("the joined destination %+v", d)
	}
	e = reachEnv(t)
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, nil, created())
	e.db.teamDests = map[int64][]routing.Entry{}
	e.db.dests[destTeamNotice].deleted = true
	e.render(t, e.leveled(groups.StatusFiring, "warning", true), groups.System, false, []int64{teamPayments},
		joinedEvent(2, destTeamNotice, groups.StatusFiring))
	if d := e.deliveryOf(groupID, destTeamNotice); d != nil {
		t.Errorf("a deleted destination got a delivery %+v", d)
	}
	e.db.fail["ListDestinationsByID"] = errBoom
	err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.leveled(groups.StatusFiring, "warning", true),
		Actor: groups.System, Events: []groups.Recorded{joinedEvent(2, destTeamCrit, groups.StatusFiring)}})
	if !errors.Is(err, errBoom) {
		t.Errorf("a failing read: %v", err)
	}
}

// TestEnqueueTeamsEvaluatedDuringStorm is C-26.FR-5 and C-11.FR-6 (finding H1 of the review): the evaluation of the
// Teams on a save, a change of configuration, that brings a Team Destination to an Alert Group a Storm of the Route
// holds creates the delivery held by the Storm, under the Storm locked FOR SHARE, with the Storm summary there, and
// publishes nothing of its own; an Urgent Alert Group is not held.
func TestEnqueueTeamsEvaluatedDuringStorm(t *testing.T) {
	e := reachEnv(t)
	e.db.storms[stormOfTheRoute] = &fakeStorm{id: stormOfTheRoute, route: routeID, started: business0,
		alertGroupCount: 25}
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, nil, created())
	if d := e.deliveryOf(groupID, destMM); d == nil || d.heldBy != stormOfTheRoute {
		t.Fatalf("not held in the route's destination: %+v", d)
	}
	locks := e.db.calls["LockActiveStorm"]
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, true, []int64{teamPayments},
		groups.Recorded{Seq: 2, Event: groups.EventTeamJoined, Loudness: groups.Quiet, Team: teamPayments})
	d := e.deliveryOf(groupID, destTeamNotice)
	if d == nil || d.heldBy != stormOfTheRoute || e.db.calls["LockActiveStorm"] != locks+1 {
		t.Fatalf("the team destination is not held: %+v", d)
	}
	if !slices.ContainsFunc(e.db.deliveries, func(x *fakeDelivery) bool {
		return x.storm == stormOfTheRoute && x.dest == destTeamNotice
	}) {
		t.Errorf("no storm summary in the team destination: %+v", e.db.deliveries)
	}
	e.round(t)
	for _, c := range e.callsTo(destTeamNotice) {
		if c.Method == deliverytest.MethodPublish && !strings.Contains(textOf(c), "Storm") {
			t.Errorf("the held alert group was published on its own: %+v", c)
		}
	}

	// An Urgent Alert Group is published there on its own.
	e = reachEnv(t)
	e.db.storms[stormOfTheRoute] = &fakeStorm{id: stormOfTheRoute, route: routeID, started: business0,
		alertGroupCount: 25}
	e.render(t, e.leveled(groups.StatusFiring, "critical", false), groups.System, false, nil, created())
	e.render(t, e.leveled(groups.StatusFiring, "critical", false), groups.System, true, []int64{teamPayments},
		groups.Recorded{Seq: 2, Event: groups.EventTeamJoined, Loudness: groups.Quiet, Team: teamPayments})
	if d := e.deliveryOf(groupID, destTeamCrit); d == nil || d.heldBy != 0 {
		t.Errorf("an urgent alert group is held: %+v", d)
	}
}

// TestEnqueueStormTeamDestination is C-26.FR-5 and C-11.FR-6 revised: during a Storm of the Route a new Alert Group
// is held in a Team Destination too, which gets the Storm summary; an Alert Group that joins a Team Destination during
// the Storm is held there with the summary unless it is Urgent, which is published there on its own.
func TestEnqueueStormTeamDestination(t *testing.T) {
	e := reachEnv(t)
	e.db.storms[stormOfTheRoute] = &fakeStorm{id: stormOfTheRoute, route: routeID, started: business0,
		alertGroupCount: 25}
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
		created())
	for _, dest := range []int64{destMM, destTeamNotice} {
		if d := e.deliveryOf(groupID, dest); d == nil || d.heldBy != stormOfTheRoute {
			t.Errorf("not held in %d: %+v", dest, d)
		}
	}
	summaryIn := func(dest int64) bool {
		return slices.ContainsFunc(e.db.deliveries, func(d *fakeDelivery) bool {
			return d.storm == stormOfTheRoute && d.dest == dest
		})
	}
	if !summaryIn(destMM) || !summaryIn(destTeamNotice) || summaryIn(destTeamCrit) {
		t.Errorf("summaries %+v", e.db.deliveries)
	}

	// Another Alert Group, published before the Storm, joins the Team's critical Destination by a rise: held there.
	e.db.groups[22] = &fakeGroup{id: 22, publicID: "AGAAAAAAAAAA22", number: 8, title: "g", status: "firing",
		route: routeID, created: business0, published: true}
	g := e.view(22, groups.StatusFiring)
	g.Severity, g.Urgent, g.FirstPublishedAt = "critical", true, at(business0)
	e.render(t, g, groups.System, false, []int64{teamPayments}, joinedEvent(3, destTeamCrit, groups.StatusFiring))
	if d := e.deliveryOf(22, destTeamCrit); d == nil || d.heldBy != 0 {
		t.Errorf("an Urgent join was held: %+v", d)
	}
	g.Urgent = false
	g.Severity = "warning"
	e.render(t, g, groups.System, false, []int64{teamPayments}, joinedEvent(4, destTeamNotice, groups.StatusFiring))
	if d := e.deliveryOf(22, destTeamNotice); d == nil || d.heldBy != stormOfTheRoute ||
		e.db.calls["LockActiveStorm"] == 0 {
		t.Errorf("a join during the storm is not held: %+v", d)
	}

	for _, q := range []string{"LockActiveStorm", "ListStormSummaries", "ListTeamDestinations",
		"ListGroupDestinations"} {
		e := reachEnv(t)
		e.db.storms[stormOfTheRoute] = &fakeStorm{id: stormOfTheRoute, route: routeID, started: business0,
			alertGroupCount: 25}
		e.db.groups[22] = &fakeGroup{id: 22, publicID: "AGAAAAAAAAAA22", number: 8, title: "g", status: "firing",
			route: routeID, created: business0}
		e.db.fail[q] = errBoom
		var err error
		if q == "ListStormSummaries" {
			err = e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.leveled(groups.StatusFiring, "warning",
				false), Actor: groups.System, Events: []groups.Recorded{created()}, Teams: []int64{teamPayments}})
		} else {
			g := e.view(22, groups.StatusFiring)
			g.Severity, g.FirstPublishedAt = "warning", at(business0)
			err = e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
				Events: []groups.Recorded{joinedEvent(4, destTeamNotice, groups.StatusFiring)},
				Teams:  []int64{teamPayments}})
		}
		if !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
	}
	e = reachEnv(t)
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
		created())
	e.db.fail["ListEntryTeams"] = errBoom
	if _, err := e.svc.States(t.Context(), "AGAAAAAAAAAA21"); !errors.Is(err, errBoom) {
		t.Errorf("states: %v", err)
	}
}

// TestRecordDeliveredKeepsTeamDestination is C-11.FR-14 with a Team Destination (finding M1 of the review): a
// Publication in flight whose delivery is withheld meanwhile keeps the message it created in a Destination that a Team
// of the Alert Group reaches on a Route with deliver_to_teams, instead of giving it the final edit as a Destination
// that left the Route; without the option it is a Destination the Route no longer reaches, and gets the final edit.
func TestRecordDeliveredKeepsTeamDestination(t *testing.T) {
	for _, toTeams := range []bool{true, false} {
		e := reachEnv(t)
		e.db.routeDests[routeID] = nil
		e.db.groups[groupID].teams = []int64{teamPayments}
		e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
			created())
		r := e.db.routes[routeID]
		r.toTeams = toTeams
		e.db.routes[routeID] = r
		e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
			Then: func() { e.deliveryOf(groupID, destTeamNotice).state = "withheld" }})
		e.round(t)
		d := e.deliveryOf(groupID, destTeamNotice)
		if toTeams && (d.state != "delivered" || d.desiredRetire || d.messageID == nil) {
			t.Errorf("with deliver_to_teams: %+v", d)
		}
		if !toTeams && (d.state != "pending" || !d.desiredRetire) {
			t.Errorf("without deliver_to_teams: %+v", d)
		}
	}
}

// TestEnqueueTeamLocks is C-26.FR-6 on Enqueue: on a Route that delivers to the Teams it takes the membership lock of
// each Team of the Alert Group shared, by Team id, after the Route's and before it reads the Teams' entries, so that a
// change of a Team's entries and the Enqueue serialize; without the option it takes none. A failing lock fails it.
func TestEnqueueTeamLocks(t *testing.T) {
	e := reachEnv(t)
	e.db.before["ShareTeamMembership"] = func() {
		if len(e.db.membershipLocks) != 1 {
			t.Errorf("a team's lock before the route's: %v", e.db.membershipLocks)
		}
	}
	e.db.before["ListTeamDestinations"] = func() {
		if !slices.Equal(e.db.teamLocks, []int64{3, teamPayments}) {
			t.Errorf("the teams' entries read under the locks %v", e.db.teamLocks)
		}
	}
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments, 3},
		created())
	if e.db.calls["ListTeamDestinations"] != 1 {
		t.Error("the teams' entries were not read")
	}

	e = reachEnv(t)
	r := e.db.routes[routeID]
	r.toTeams = false
	e.db.routes[routeID] = r
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
		created())
	if len(e.db.teamLocks) != 0 {
		t.Errorf("team locks without the option %v", e.db.teamLocks)
	}

	e = reachEnv(t)
	e.db.fail["ShareTeamMembership"] = errBoom
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.leveled(groups.StatusFiring, "warning", false),
		Actor: groups.System, Events: []groups.Recorded{created()}, Teams: []int64{teamPayments}}); !errors.Is(err,
		errBoom) {
		t.Errorf("a failing team lock: %v", err)
	}
}

// TestEnqueueGrowsQuietly is C-26.FR-6, C-25.FR-13 and ADR-0018 on the re-render of a change of a Team's Destination
// entries (Rendering.Grows): without a lifecycle event it publishes the Alert Group Quietly, without Mentions and
// Thread replies, in each Destination it now reaches without a delivery, and publishes again, Quietly, one whose
// delivery got its final edit; a delivery Not delivered before its first Publication is not revived Loud; a Destination
// whose condition its level does not meet gets nothing, so a level that fell gains no critical Destination; and a
// re-render of a change of configuration that is not marked so creates nothing.
func TestEnqueueGrowsQuietly(t *testing.T) {
	e := reachEnv(t)
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, nil, created())
	e.round(t)
	e.rec.Reset()
	// Not marked as growing, the re-render of a change of configuration creates no delivery.
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.leveled(groups.StatusFiring, "warning", true),
		Actor: groups.System, Teams: []int64{teamPayments}, Configuration: true}); err != nil {
		t.Fatal(err)
	}
	if e.deliveryOf(groupID, destTeamNotice) != nil {
		t.Fatal("a re-render that does not grow created a delivery")
	}
	grow := func() int {
		t.Helper()
		var changed int
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.leveled(groups.StatusFiring, "warning",
			true), Actor: groups.System, Teams: []int64{teamPayments}, Configuration: true, Grows: &groups.Growth{Team: teamPayments, Destination: destTeamNotice},
			Changed: func() { changed++ }}); err != nil {
			t.Fatal(err)
		}
		return changed
	}
	grow()
	e.round(t)
	if c := e.published(t, destTeamNotice); c.Loudness != groups.Quiet || len(c.Mentions) != 0 ||
		len(e.repliesTo(destTeamNotice)) != 0 {
		t.Errorf("the publication in the team's destination %+v", c)
	}
	if e.deliveryOf(groupID, destTeamCrit) != nil || e.deliveryOf(groupID, destCritical) != nil {
		t.Error("a warning alert group grew into a critical destination")
	}

	// Its final edit done, the Destination reached again publishes afresh, Quietly.
	d := e.deliveryOf(groupID, destTeamNotice)
	d.state, d.desiredRetire = "retired", false
	e.rec.Reset()
	if grow() == 0 {
		t.Error("publishing again changed nothing")
	}
	e.round(t)
	if c := e.published(t, destTeamNotice); c.Loudness != groups.Quiet || len(c.Mentions) != 0 {
		t.Errorf("published again %+v", c)
	}

	// Not delivered before its first Publication, created Loud: published Quietly by the change.
	e = reachEnv(t)
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
		created())
	nd := e.deliveryOf(groupID, destTeamNotice)
	nd.state = "not_delivered"
	g := e.leveled(groups.StatusFiring, "warning", true)
	g.Title = "disk full, renamed"
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
		Teams: []int64{teamPayments}, Configuration: true, Grows: &groups.Growth{Team: teamPayments, Destination: destTeamNotice}}); err != nil {
		t.Fatal(err)
	}
	if nd.loud.Bool || nd.state != "pending" {
		t.Errorf("not delivered revived %s, loud %v", nd.state, nd.loud)
	}
}

// fallenEnv is reachEnv whose Alert Group of the fixtures of the Team payments was created critical — published in
// infra-critical and payments-critical — and then fell to warning, which creates nothing below critical (C-26.FR-4).
func fallenEnv(t *testing.T) *env {
	t.Helper()
	e := reachEnv(t)
	e.w.PublicURL = publicURL
	e.db.groups[groupID].teams = []int64{teamPayments}
	e.db.groups[groupID].severity = "critical"
	e.render(t, e.leveled(groups.StatusFiring, "critical", false), groups.System, false, []int64{teamPayments},
		created())
	e.db.groups[groupID].severity = "warning"
	e.render(t, e.leveled(groups.StatusFiring, "warning", true), groups.System, false, []int64{teamPayments},
		groups.Recorded{Seq: 2, Event: groups.EventAlertResolved, Loudness: groups.Quiet})
	e.round(t)
	if e.deliveryOf(groupID, destCritical) == nil || e.deliveryOf(groupID, destTeamCrit) == nil ||
		e.deliveryOf(groupID, destMM) != nil || e.deliveryOf(groupID, destTeamNotice) != nil {
		t.Fatalf("deliveries after the fall %+v", e.db.deliveries)
	}
	e.rec.Reset()
	return e
}

// TestUnrelatedChangeGainsNothing is C-26.FR-4 and FR-6 (decision D3 revised): a change of configuration publishes
// only in the Destinations whose entries it added or changed, where the Alert Group meets them now. After a fall from
// critical to warning, a save of the Route that adds an unrelated Destination, and a change of the Team that adds one,
// publish the Alert Group in that Destination only — never in infra-notice or payments-notice, which the change did
// not touch and which the fall did not bring either.
func TestUnrelatedChangeGainsNothing(t *testing.T) {
	t.Run("route", func(t *testing.T) {
		e := fallenEnv(t)
		e.route(t, e.setCondition(criticalTwo(e), "any"))
		e.round(t)
		if e.deliveryOf(groupID, destMM) != nil || e.deliveryOf(groupID, destTeamNotice) != nil {
			t.Errorf("an unrelated save published below critical: %+v", e.rec.Calls())
		}
		if c := e.published(t, 16); c.Loudness != groups.Quiet {
			t.Errorf("the added destination %+v", c)
		}
		// Turning the option off and on again touches the Teams' entries: payments-notice, which the Alert Group
		// meets now, is published; infra-notice, the Route's untouched entry, still not.
		e.route(t, e.setToTeams(false))
		e.route(t, e.setToTeams(true))
		e.round(t)
		if e.deliveryOf(groupID, destTeamNotice) == nil || e.deliveryOf(groupID, destMM) != nil {
			t.Errorf("the option on again: notice %+v, infra-notice %+v", e.deliveryOf(groupID, destTeamNotice),
				e.deliveryOf(groupID, destMM))
		}
	})
	t.Run("team", func(t *testing.T) {
		e := fallenEnv(t)
		criticalTwo(e)
		e.db.teamDests[teamPayments] = append(e.db.teamDests[teamPayments],
			routing.Entry{DestinationID: 16, Condition: routing.ConditionAny})
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.leveled(groups.StatusFiring, "warning",
			true), Actor: groups.System, Teams: []int64{teamPayments}, Configuration: true,
			Grows: &groups.Growth{Team: teamPayments, Destination: 16}}); err != nil {
			t.Fatal(err)
		}
		e.round(t)
		if e.deliveryOf(groupID, destMM) != nil || e.deliveryOf(groupID, destTeamNotice) != nil {
			t.Errorf("an unrelated team save published below critical: %+v", e.rec.Calls())
		}
		if c := e.published(t, 16); c.Loudness != groups.Quiet {
			t.Errorf("the added team destination %+v", c)
		}
		// A growth whose entry the Alert Group does not meet adds nothing.
		if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: e.leveled(groups.StatusFiring, "warning",
			true), Actor: groups.System, Teams: []int64{teamPayments}, Configuration: true,
			Grows: &groups.Growth{Team: teamPayments, Destination: destTeamCrit}}); err != nil {
			t.Fatal(err)
		}
		if e.deliveryOf(groupID, destTeamNotice) != nil {
			t.Error("a growth of another destination published payments-notice")
		}
	})
}

// TestEnqueueGrowsDuringStorm is C-11.FR-6 with C-26.FR-6: during a Storm of the Route, a Team's Destination that the
// change publishes an Alert Group created since the Storm started in holds it there, with the Storm summary, even after
// a final edit cleared its hold — a Destination added anew and one added again alike.
func TestEnqueueGrowsDuringStorm(t *testing.T) {
	e := reachEnv(t)
	e.db.storms[stormOfTheRoute] = &fakeStorm{id: stormOfTheRoute, route: routeID, started: business0,
		alertGroupCount: 25}
	g := e.leveled(groups.StatusFiring, "warning", false)
	g.CreatedAt = business0
	e.db.groups[groupID].teams = []int64{teamPayments}
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
		Events: []groups.Recorded{created()}, Teams: []int64{teamPayments}}); err != nil {
		t.Fatal(err)
	}
	// Every hold of the Alert Group is gone: its deliveries got their final edits, as RetireDeliveries clears it.
	for _, d := range e.db.deliveries {
		if d.group == groupID {
			d.heldBy, d.state = 0, "withheld"
		}
	}
	g.FirstPublishedAt = at(business0)
	ensured := e.db.calls["EnsureStormSummary"]
	if err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
		Teams: []int64{teamPayments}, Configuration: true,
		Grows: &groups.Growth{Team: teamPayments, Destination: destTeamNotice}}); err != nil {
		t.Fatal(err)
	}
	if d := e.deliveryOf(groupID, destTeamNotice); d == nil || d.heldBy != stormOfTheRoute || d.state != "pending" {
		t.Errorf("the destination published again is not held: %+v", d)
	}
	if e.db.calls["EnsureStormSummary"] != ensured+1 {
		t.Errorf("the storm summary of the destination published again was rendered %d times",
			e.db.calls["EnsureStormSummary"]-ensured)
	}
	e.round(t)
	for _, c := range e.callsTo(destTeamNotice) {
		if c.Method == deliverytest.MethodPublish && !strings.Contains(textOf(c), "Storm") {
			t.Errorf("the held alert group was published on its own: %+v", c)
		}
	}
}

// TestJoinedAgainAfterFinalEdit is C-26.FR-4 and FR-5 after C-11.FR-14: a Destination whose delivery got its final edit
// and that the Alert Group reaches again by a rise or by a Team that joins it — the dispatcher records
// destination_joined, which counts only deliveries kept current — publishes it there afresh, Loud with new_alert_group
// while it fires and Quiet while it is acknowledged, held by an active Storm where one holds it, without the Thread
// replies of the change.
func TestJoinedAgainAfterFinalEdit(t *testing.T) {
	rise := func(t *testing.T, e *env, status groups.Status) {
		t.Helper()
		e.db.groups[groupID].severity = "critical"
		e.render(t, e.leveled(status, "critical", true), groups.System, false, []int64{teamPayments},
			groups.Recorded{Seq: 5, Event: groups.EventSeverityRaised, Loudness: groups.Quiet},
			joinedEvent(6, destCritical, status))
	}
	for _, status := range []groups.Status{groups.StatusFiring, groups.StatusAcknowledged} {
		t.Run("rise "+string(status), func(t *testing.T) {
			// The Route names infra-notice and infra-critical for any; the warning Alert Group is in both.
			e := entriesEnv(t)
			e.db.conditions[routeID][destCritical] = "any"
			e.route(t, e.changeOf(routeID, []int64{destCritical}, nil))
			e.round(t)
			// infra-critical taken off the Route: its final edit; added back for critical: nothing.
			e.route(t, e.setCondition(destCritical, ""))
			e.round(t)
			if d := e.deliveryOf(groupID, destCritical); d.state != "retired" {
				t.Fatalf("after the final edit: %s", d.state)
			}
			e.route(t, e.setCondition(destCritical, "critical"))
			if d := e.deliveryOf(groupID, destCritical); d.state != "retired" {
				t.Fatalf("a condition not met published: %s", d.state)
			}
			e.rec.Reset()
			rise(t, e, status)
			e.round(t)
			c := e.published(t, destCritical)
			if status == groups.StatusFiring && (c.Loudness != groups.Loud ||
				!slices.Equal(c.Mentions, []groups.Mention{groups.MentionNewAlertGroup})) {
				t.Errorf("the loud join again %+v", c)
			}
			if status != groups.StatusFiring && (c.Loudness != groups.Quiet || len(c.Mentions) != 0) {
				t.Errorf("the quiet join again %+v", c)
			}
			if len(e.repliesTo(destCritical)) != 0 {
				t.Errorf("thread replies in the destination joined again %+v", e.repliesTo(destCritical))
			}
		})
	}
	t.Run("team", func(t *testing.T) {
		// payments drops payments-notice: its final edit; then search, which names it, joins with its Alert.
		e := entriesEnv(t)
		e.db.teamDests[teamPayments] = e.db.teamDests[teamPayments][1:]
		if _, err := e.svc.FinalEditsFor(t.Context(), nil, e.leveled(groups.StatusFiring, "warning", true),
			teamPayments); err != nil {
			t.Fatal(err)
		}
		e.round(t)
		if d := e.deliveryOf(groupID, destTeamNotice); d.state != "retired" {
			t.Fatalf("after the final edit: %s", d.state)
		}
		e.rec.Reset()
		e.db.teamDests[3] = []routing.Entry{{DestinationID: destTeamNotice, Condition: routing.ConditionAny}}
		e.db.groups[groupID].teams = []int64{3, teamPayments}
		e.render(t, e.leveled(groups.StatusFiring, "warning", true), groups.System, false, []int64{3, teamPayments},
			groups.Recorded{Seq: 5, Event: groups.EventTeamJoined, Loudness: groups.Quiet, Team: 3},
			joinedEvent(6, destTeamNotice, groups.StatusFiring))
		e.round(t)
		if c := e.published(t, destTeamNotice); c.Loudness != groups.Loud {
			t.Errorf("the team's join again %+v", c)
		}
	})
	t.Run("storm", func(t *testing.T) {
		e := entriesEnv(t)
		e.db.conditions[routeID][destCritical] = "any"
		e.route(t, e.changeOf(routeID, []int64{destCritical}, nil))
		e.round(t)
		e.route(t, e.setCondition(destCritical, ""))
		e.round(t)
		e.route(t, e.setCondition(destCritical, "critical"))
		e.db.storms[stormOfTheRoute] = &fakeStorm{id: stormOfTheRoute, route: routeID, started: business0,
			alertGroupCount: 25}
		ensured := e.db.calls["EnsureStormSummary"]
		e.db.groups[groupID].severity = "critical"
		g := e.leveled(groups.StatusFiring, "critical", true)
		g.Urgent = false
		e.render(t, g, groups.System, false, []int64{teamPayments},
			groups.Recorded{Seq: 5, Event: groups.EventSeverityRaised, Loudness: groups.Quiet},
			joinedEvent(6, destCritical, groups.StatusFiring))
		if d := e.deliveryOf(groupID, destCritical); d.heldBy != stormOfTheRoute ||
			e.db.calls["EnsureStormSummary"] == ensured {
			t.Errorf("joined again during a storm: held by %d, %d summaries", d.heldBy,
				e.db.calls["EnsureStormSummary"]-ensured)
		}
	})
}

// TestTeamEvaluatedAfterFall is C-26.FR-4 with C-25.FR-13: a Team that the evaluation of the Teams on a save brings to
// an Alert Group whose level fell from critical publishes it Quietly in the Destinations of that Team's entries only —
// never in a Destination of the Route or of another Team below critical, which the fall did not bring.
func TestTeamEvaluatedAfterFall(t *testing.T) {
	e := fallenEnv(t)
	criticalTwo(e)
	e.db.teamDests[3] = []routing.Entry{{DestinationID: 16, Condition: routing.ConditionAny}}
	e.db.groups[groupID].teams = []int64{3, teamPayments}
	e.render(t, e.leveled(groups.StatusFiring, "warning", true), groups.System, true, []int64{3, teamPayments},
		groups.Recorded{Seq: 5, Event: groups.EventTeamJoined, Loudness: groups.Quiet, Team: 3})
	e.round(t)
	if e.deliveryOf(groupID, destMM) != nil || e.deliveryOf(groupID, destTeamNotice) != nil {
		t.Errorf("the evaluated team published below critical: %+v", e.rec.Calls())
	}
	if c := e.published(t, 16); c.Loudness != groups.Quiet {
		t.Errorf("the evaluated team's destination %+v", c)
	}
}
