// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/routing"
)

// The queries of Destinations joining and leaving Routes over the fake database.

// pendingOf says whether a delivery of the Destination is pending.
func (f *fakeDB) pendingOf(dest int64) bool {
	return slices.ContainsFunc(f.deliveries, func(d *fakeDelivery) bool { return d.dest == dest && d.state == "pending" })
}

// terminal says whether a delivery ended for good: withheld, deleted in the messenger or retired.
func terminal(d *fakeDelivery) bool {
	return slices.Contains([]string{"withheld", "deleted_in_messenger", "retired"}, d.state)
}

// retire gives a delivery its final edit, or withholds it when nothing of it was ever published.
func retire(d *fakeDelivery, now time.Time) {
	if d.publishedAt == nil && d.started == nil {
		d.state, d.desiredRetire = "withheld", false
	} else {
		d.state, d.desiredRetire = "pending", true
	}
	d.heldBy, d.configOnly, d.next, d.updated = 0, false, now, now
}

func (f *fakeDB) open(group int64) bool {
	g := f.groups[group]
	return g != nil && g.status != "resolved"
}

func (f *fakeDB) ListOpenGroupsOfRoute(_ context.Context, arg dbgen.ListOpenGroupsOfRouteParams) (
	[]dbgen.ListOpenGroupsOfRouteRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListOpenGroupsOfRoute"); err != nil {
		return nil, err
	}
	var out []dbgen.ListOpenGroupsOfRouteRow
	for _, g := range f.groups {
		if g.route == arg.RouteID && g.status != "resolved" {
			held := !g.urgent && arg.StormID.Valid && (!g.created.Before(arg.StormStarted.Time) ||
				slices.ContainsFunc(f.deliveries, func(d *fakeDelivery) bool {
					return d.group == g.id && d.heldBy == arg.StormID.Int64
				}))
			out = append(out, dbgen.ListOpenGroupsOfRouteRow{ID: g.id, PublicID: g.publicID, Number: g.number,
				Title: g.title, Status: g.status, Urgent: g.urgent, SeverityLevel: cmp.Or(g.severity, "warning"),
				TeamIds: append([]int64{}, slices.Sorted(slices.Values(g.teams))...), Held: held})
		}
	}
	slices.SortFunc(out, func(a, b dbgen.ListOpenGroupsOfRouteRow) int { return int(a.ID - b.ID) })
	return out, nil
}

func (f *fakeDB) RejoinDelivery(_ context.Context, arg dbgen.RejoinDeliveryParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RejoinDelivery"); err != nil {
		return 0, err
	}
	for _, d := range f.deliveries {
		if d.group != arg.AlertGroupID || d.dest != arg.DestinationID ||
			(d.state != "retired" && d.state != "withheld" && !d.desiredRetire) {
			continue
		}
		d.heldBy = 0
		if d.state == "retired" || d.state == "withheld" {
			d.messageID, d.messageURL, d.actualVersion, d.actualHash, d.started = nil, nil, 0, nil, nil
			d.publishedAt, d.responseValues, d.threadOpened = nil, nil, false
			d.threadState, d.anchorID, d.chainLastID, d.republished = "none", nil, nil, false
			d.loud, d.heldBy = pgtype.Bool{Bool: arg.Loud, Valid: true}, arg.HeldByStormID.Int64
			d.actualStatus, d.pastEditWindow, d.restarted = nil, false, nil
		}
		d.lateNote, d.desiredRetire, d.state, d.next, d.updated = false, false, "pending", arg.Now, arg.Now
		d.configOnly = false
		return 1, nil
	}
	return 0, nil
}

func (f *fakeDB) RetireDeliveries(_ context.Context, arg dbgen.RetireDeliveriesParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RetireDeliveries"); err != nil {
		return nil, err
	}
	var out []int64
	for _, d := range f.deliveries {
		if slices.Contains(arg.Ids, d.id) && !d.desiredRetire && !terminal(d) {
			retire(d, arg.Now)
			out = append(out, d.id)
		}
	}
	return out, nil
}

func (f *fakeDB) ListGroupTeams(_ context.Context, arg dbgen.ListGroupTeamsParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListGroupTeams"); err != nil {
		return nil, err
	}
	if g := f.groups[arg.AlertGroupID]; g != nil {
		return slices.Sorted(slices.Values(g.teams)), nil
	}
	return nil, nil
}

func (f *fakeDB) ListEntryDeliveries(_ context.Context, arg dbgen.ListEntryDeliveriesParams) (
	[]dbgen.ListEntryDeliveriesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListEntryDeliveries"); err != nil {
		return nil, err
	}
	var out []dbgen.ListEntryDeliveriesRow
	for _, d := range f.deliveries {
		if d.storm == 0 && slices.Contains(arg.AlertGroupIds, d.group) {
			entries := d.entries
			if entries == nil {
				entries = []byte("[]")
			}
			out = append(out, dbgen.ListEntryDeliveriesRow{ID: d.id, AlertGroupID: d.group, DestinationID: d.dest,
				State: d.state, DesiredRetire: d.desiredRetire, Entries: entries})
		}
	}
	slices.SortFunc(out, func(a, b dbgen.ListEntryDeliveriesRow) int {
		return cmp.Or(cmp.Compare(a.AlertGroupID, b.AlertGroupID), cmp.Compare(a.DestinationID, b.DestinationID))
	})
	return out, nil
}

func (f *fakeDB) AppendDeliveryEntries(_ context.Context, arg dbgen.AppendDeliveryEntriesParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("AppendDeliveryEntries"); err != nil {
		return err
	}
	d := f.delivery(arg.ID)
	entries, err := appendEntries(d.entries, arg.Entries)
	if err != nil {
		return err
	}
	d.entries = entries
	return nil
}

func (f *fakeDB) RetireGroupDeliveries(_ context.Context, arg dbgen.RetireGroupDeliveriesParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RetireGroupDeliveries"); err != nil {
		return nil, err
	}
	if arg.Keep == nil {
		return nil, nil // destination_id <> ALL(NULL) is never true
	}
	var out []int64
	for _, d := range f.deliveries {
		if d.group == arg.AlertGroupID && d.storm == 0 && !slices.Contains(arg.Keep, d.dest) && !terminal(d) {
			retire(d, arg.Now)
			out = append(out, d.id)
		}
	}
	return out, nil
}

func (f *fakeDB) RetireDestinationDeliveries(_ context.Context, arg dbgen.RetireDestinationDeliveriesParams) (
	[]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RetireDestinationDeliveries"); err != nil {
		return nil, err
	}
	var out []int64
	for _, d := range f.deliveries {
		if d.dest == arg.DestinationID && d.storm == 0 && f.open(d.group) && !terminal(d) {
			retire(d, arg.Now)
			out = append(out, d.id)
		}
	}
	return out, nil
}

func (f *fakeDB) SettleDestinationLeftovers(_ context.Context, arg dbgen.SettleDestinationLeftoversParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("SettleDestinationLeftovers"); err != nil {
		return err
	}
	for _, d := range f.deliveries {
		leftover := (d.state == "pending" && !f.open(d.group)) || (d.storm != 0 && !terminal(d))
		if d.dest == arg.DestinationID && !d.desiredRetire && leftover {
			d.state = "retired"
			if d.publishedAt == nil {
				d.state = "withheld"
			}
			d.heldBy, d.updated = 0, arg.Now
		}
	}
	return nil
}

func (f *fakeDB) dropReplies(match func(r *fakeReply) bool) {
	for _, r := range f.replies {
		if match(r) && (r.state == "pending" || r.state == "collecting") {
			r.state, r.owner, r.until = "dropped", "", time.Time{}
		}
	}
}

func (f *fakeDB) DropDeliveryReplies(_ context.Context, arg dbgen.DropDeliveryRepliesParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DropDeliveryReplies"); err != nil {
		return err
	}
	f.dropReplies(func(r *fakeReply) bool { return slices.Contains(arg.DeliveryIds, r.delivery) })
	return nil
}

func (f *fakeDB) DropDestinationReplies(_ context.Context, arg dbgen.DropDestinationRepliesParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DropDestinationReplies"); err != nil {
		return err
	}
	f.dropReplies(func(r *fakeReply) bool { return r.dest == arg.DestinationID })
	return nil
}

func (f *fakeDB) RetireLeased(_ context.Context, arg dbgen.RetireLeasedParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RetireLeased"); err != nil {
		return err
	}
	if d := f.delivery(arg.ID); d.owner == arg.Owner {
		d.desiredRetire, d.heldBy, d.updated = true, 0, arg.Now
	}
	return nil
}

func (f *fakeDB) RecordRetired(_ context.Context, arg dbgen.RecordRetiredParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RecordRetired"); err != nil {
		return 0, err
	}
	d := f.delivery(arg.ID)
	if d.owner != arg.Owner {
		return 0, pgx.ErrNoRows
	}
	switch {
	case d.state == "withheld" || d.state == "deleted_in_messenger":
	case d.desiredRetire:
		d.state = "retired"
	default:
		// Joined again while the final edit was in flight: edited back to the current state by the next call.
		d.state, d.actualHash, d.actualStatus, d.next = "pending", nil, nil, arg.Now
	}
	d.desiredRetire, d.attempts, d.firstFailed, d.errorClass, d.lastError = false, 0, nil, nil, nil
	d.lastDelivered, d.owner, d.until, d.updated, d.actualKey = at(arg.Now), "", time.Time{}, arg.Now, nil
	d.restarted = nil
	return d.id, nil
}

func (f *fakeDB) WithholdLeased(_ context.Context, arg dbgen.WithholdLeasedParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("WithholdLeased"); err != nil {
		return err
	}
	if d := f.delivery(arg.ID); d.owner == arg.Owner {
		d.state, d.desiredRetire, d.owner, d.until, d.updated = "withheld", false, "", time.Time{}, arg.Now
	}
	return nil
}

func (f *fakeDB) GetDestinationState(_ context.Context, arg dbgen.GetDestinationStateParams) (
	dbgen.GetDestinationStateRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("GetDestinationState"); err != nil {
		return dbgen.GetDestinationStateRow{}, err
	}
	ds := f.dests[arg.ID]
	return dbgen.GetDestinationStateRow{PublicID: ds.publicID, Health: ds.health,
		TemplateErrorSet: ds.templateError != nil}, nil
}

func (f *fakeDB) WipeDestinationSecrets(_ context.Context, arg dbgen.WipeDestinationSecretsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("WipeDestinationSecrets"); err != nil {
		return err
	}
	for _, ds := range f.dests {
		if arg.ID.Valid && ds.id != arg.ID.Int64 {
			continue
		}
		if ds.deleted && !f.pendingOf(ds.id) && !f.leasedOf(ds.id, arg.Now) {
			ds.secrets, ds.named = 0, 0
		}
	}
	return nil
}

func (f *fakeDB) AbandonConnectionDeliveries(_ context.Context, arg dbgen.AbandonConnectionDeliveriesParams) (
	[]dbgen.AbandonConnectionDeliveriesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("AbandonConnectionDeliveries"); err != nil {
		return nil, err
	}
	var out []dbgen.AbandonConnectionDeliveriesRow
	for _, d := range f.deliveries {
		ds := f.dests[d.dest]
		if d.state != "pending" || !ds.deleted || ds.connection == nil || *ds.connection != arg.ConnectionID.Int64 {
			continue
		}
		row := dbgen.AbandonConnectionDeliveriesRow{ID: d.id, DestinationID: d.dest, DestinationPublicID: ds.publicID,
			FinalEdit: d.desiredRetire, Unpublished: d.publishedAt == nil}
		d.state, d.desiredRetire, d.errorClass, d.lastError = "not_delivered", false, at2("unknown"), at2(arg.Error)
		d.owner, d.until, d.updated = "", time.Time{}, arg.Now
		if d.storm != 0 {
			row.StormID = pgtype.Int8{Int64: d.storm, Valid: true}
		} else {
			row.AlertGroupID = pgtype.Int8{Int64: d.group, Valid: true}
			row.AlertGroupPublicID = f.groups[d.group].publicID
		}
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeDB) ListDeletedDestinationsOfConnection(_ context.Context,
	arg dbgen.ListDeletedDestinationsOfConnectionParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListDeletedDestinationsOfConnection"); err != nil {
		return nil, err
	}
	var out []int64
	for _, ds := range f.dests {
		if ds.deleted && ds.connection != nil && *ds.connection == arg.ConnectionID.Int64 {
			out = append(out, ds.id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// The public URL of the fixtures, the third Destination and the Default route.
const (
	publicURL = "https://muster.example.org/"
	destX     = 13
	defaultID = 2
)

// memberEnv is an env with a third Destination, an outgoing webhook X, the Default route, and limiters that never run
// dry.
func memberEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.w.PublicURL = publicURL
	e.db.dests[destMM].limit = 600
	e.db.dests[destWH].limit = 600
	e.db.dests[destX] = &fakeDest{id: destX, publicID: "DSAAAAAAAAAA13", name: "x", typ: delivery.TypeWebhook,
		health: "healthy", limit: 600, per: 60}
	e.db.routes[defaultID] = fakeRoute{language: "en", window: 60, publicID: "RTAAAAAAAAAAA2", name: "Default",
		threshold: 20}
	return e
}

// callsTo are the recorded calls to the Destination dest.
func (e *env) callsTo(dest int64) []deliverytest.Call {
	var out []deliverytest.Call
	for _, c := range e.rec.Calls() {
		if c.Destination.ID == dest {
			out = append(out, c)
		}
	}
	return out
}

// deliveryOf is the delivery of an Alert Group in a Destination.
func (e *env) deliveryOf(group, dest int64) *fakeDelivery {
	for _, d := range e.db.deliveries {
		if d.group == group && d.dest == dest && d.storm == 0 {
			return d
		}
	}
	return nil
}

// changeOf is the change of the Route's Destinations as routing gives it after it wrote route_destinations: the
// entries after are those of the fake, and before them those without added and with removed, any for one the fake
// has no condition of; the option stays as the fake has it.
func (e *env) changeOf(route int64, added, removed []int64) routing.MembershipChange {
	entryOf := func(id int64) routing.Entry {
		return routing.Entry{DestinationID: id, Condition: routing.SeverityCondition(cmp.Or(e.db.conditions[route][id],
			"any"))}
	}
	c := routing.MembershipChange{RouteID: route, ToTeamsBefore: e.db.routes[route].toTeams,
		ToTeamsAfter: e.db.routes[route].toTeams}
	for _, id := range slices.Sorted(slices.Values(e.db.routeDests[route])) {
		c.After = append(c.After, entryOf(id))
		if !slices.Contains(added, id) {
			c.Before = append(c.Before, entryOf(id))
		}
	}
	for _, id := range removed {
		c.Before = append(c.Before, entryOf(id))
	}
	slices.SortFunc(c.Before, func(a, b routing.Entry) int { return cmp.Compare(a.DestinationID, b.DestinationID) })
	return c
}

// changed runs the membership hook of the Route as routing does after it wrote route_destinations.
func (e *env) changed(t *testing.T, route int64, added, removed []int64) {
	t.Helper()
	if err := e.svc.RouteDestinationsChanged(t.Context(), nil, e.changeOf(route, added, removed)); err != nil {
		t.Fatal(err)
	}
}

// finalText is the note of the final edit of the Alert Group publicID.
func finalText(publicID string) string {
	return "No longer updated here; current state in Muster: https://muster.example.org/alert-groups/" + publicID
}

// TestRouteMembership is C-11.FR-14 through the recorder: a Destination added to a Route publishes the Route's open
// Alert Groups there Quietly, never a resolved one; a Destination removed from it gives each of their open Root
// messages one final Quiet edit with the link to Muster and no buttons, drops their pending Thread replies, and
// receives nothing after; one never published there is withheld; added again, it publishes them afresh.
func TestRouteMembership(t *testing.T) {
	e := memberEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.addGroup(t, 22, false)
	e.enqueue(t, e.view(22, groups.StatusAcknowledged), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.addGroup(t, 23, false)
	e.round(t)
	e.enqueue(t, e.view(23, groups.StatusResolved), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventResolved, Loudness: groups.Quiet})
	e.round(t)
	e.rec.Reset()

	e.db.routeDests[routeID] = []int64{destMM, destWH}
	e.changed(t, routeID, []int64{destWH}, nil)
	e.round(t)
	added := e.callsTo(destWH)
	if callMethods(added) != "publish,publish" || slices.ContainsFunc(added, func(c deliverytest.Call) bool {
		return c.Loudness != groups.Quiet || len(c.Mentions) != 0
	}) || !strings.HasPrefix(textOf(added[0]), "#7 a") || !strings.HasPrefix(textOf(added[1]), "#22 g") ||
		e.deliveryOf(23, destWH) != nil || len(e.callsTo(destMM)) != 0 {
		t.Fatalf("added %+v", added)
	}

	// A Thread reply queued in ops before it leaves the Route is dropped with the final edit.
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(3, groups.StatusFiring, "fp2"))
	e.addGroup(t, 24, false) // never published in ops: withheld
	e.rec.Reset()
	e.db.routeDests[routeID] = []int64{destWH}
	e.changed(t, routeID, nil, []int64{destMM})
	if d := e.deliveryOf(24, destMM); d.state != "withheld" {
		t.Errorf("never published %+v", d)
	}
	e.round(t)
	final := e.callsTo(destMM)
	if callMethods(final) != "update,update" {
		t.Fatalf("final edits %s", callMethods(final))
	}
	for i, publicID := range []string{"AGAAAAAAAAAA21", "AGAAAAAAAA0022"} {
		c := final[i]
		if c.Loudness != groups.Quiet || len(c.Message.Buttons) != 0 ||
			c.Message.Notices[len(c.Message.Notices)-1] != finalText(publicID) {
			t.Errorf("final edit %+v", c)
		}
	}
	for _, id := range []int64{groupID, 22} {
		if d := e.deliveryOf(id, destMM); d.state != "retired" || d.messageURL == nil || d.desiredRetire {
			t.Errorf("retired %+v", d)
		}
	}
	if d := e.deliveryOf(23, destMM); d.state != "delivered" {
		t.Errorf("resolved %+v", d)
	}
	if i := slices.IndexFunc(e.db.replies, func(r *fakeReply) bool { return r.dest == destMM && r.event == "alerts_added" }); i < 0 ||
		e.db.replies[i].state != "dropped" {
		t.Errorf("replies %+v", e.db.replies)
	}
	if n := strings.Count(strings.Join(e.eventKinds(), ","), "final_edit"); n != 2 {
		t.Errorf("events %v", e.eventKinds())
	}
	e.rec.Reset()
	e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
		groups.Recorded{Seq: 4, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
	e.round(t)
	if len(e.callsTo(destMM)) != 0 || len(e.callsTo(destWH)) != 1 {
		t.Errorf("after leaving %+v", e.rec.Calls())
	}

	// Added again: a fresh Quiet Publication of each open Alert Group.
	e.rec.Reset()
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	e.changed(t, routeID, []int64{destMM}, nil)
	e.round(t)
	if again := e.callsTo(destMM); callMethods(again) != "publish,publish,publish" ||
		slices.ContainsFunc(again, func(c deliverytest.Call) bool { return c.Loudness != groups.Quiet }) {
		t.Errorf("added again %+v", again)
	}
	if err := e.svc.RouteDestinationsChanged(t.Context(), nil, e.changeOf(routeID, nil, nil)); err != nil ||
		e.db.calls["ListOpenGroupsOfRoute"] != 3 {
		t.Errorf("no change = %v", err)
	}
	// A Route without open Alert Groups publishes nothing.
	e.changed(t, defaultID, []int64{destX}, nil)
	if e.db.calls["ListRouteDestinations"] == 0 || slices.ContainsFunc(e.db.deliveries, func(d *fakeDelivery) bool {
		return d.dest == destX
	}) {
		t.Error("published an empty route")
	}
}

// TestRemovedWhileInFlight: a Destination that leaves the Route while its Root message is being published gets the
// final edit once the Publication is recorded; one whose Publication started but never answered is withheld when the
// final edit comes due without a Root message; a final edit that finds the Root message gone retires the delivery.
func TestRemovedWhileInFlight(t *testing.T) {
	e := memberEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: deliverytest.OK().Outcome, Then: func() {
		e.db.routeDests[routeID] = nil
		e.changed(t, routeID, nil, []int64{destMM})
	}})
	e.round(t)
	d := e.only(t)
	if d.state != "pending" || !d.desiredRetire || d.messageID == nil {
		t.Fatalf("in flight %+v", d)
	}
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "deleted"))
	e.round(t)
	if d.state != "retired" || slices.Contains(e.eventKinds(), "final_edit") {
		t.Errorf("gone at the final edit %+v %v", d, e.eventKinds())
	}

	e = memberEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	d = e.only(t)
	d.started = at(business0)
	e.db.routeDests[routeID] = nil
	e.changed(t, routeID, nil, []int64{destMM})
	if d.state != "pending" || !d.desiredRetire {
		t.Fatalf("started %+v", d)
	}
	e.round(t)
	if d.state != "withheld" || len(e.rec.Calls()) != 0 {
		t.Errorf("withheld at the final edit %+v, calls %d", d, len(e.rec.Calls()))
	}
}

// TestDeleteDestinationDelivery is C-11.FR-14 for a deleted Destination: its open Root messages get the final edit,
// its other pending deliveries end without a call, its Thread replies are dropped and MusterDestinationBroken about
// it is resolved; its secrets are wiped once the last final edit is done, or at once when none waits; a deleted
// Broken Destination is probed only for its final edits.
func TestDeleteDestinationDelivery(t *testing.T) {
	e := memberEnv(t)
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.addGroup(t, 22, false)
	e.round(t)
	e.enqueue(t, e.view(22, groups.StatusResolved), groups.System,
		groups.Recorded{Seq: 2, Event: groups.EventResolved, Loudness: groups.Quiet})
	e.rec.Reset()
	wh := e.db.dests[destWH]
	wh.secrets, wh.named, wh.deleted = 3, 2, true
	e.db.routeDests[routeID] = []int64{destMM}
	if err := e.svc.RetireDestination(t.Context(), nil, destWH); err != nil {
		t.Fatal(err)
	}
	if wh.secrets != 3 || e.deliveryOf(22, destWH).state != "retired" || !e.deliveryOf(groupID, destWH).desiredRetire {
		t.Fatalf("before the final edit %+v", wh)
	}
	e.round(t)
	if c := e.callsTo(destWH); callMethods(c) != "update" || c[0].Message.Notices[len(c[0].Message.Notices)-1] !=
		finalText("AGAAAAAAAAAA21") {
		t.Errorf("final edit %+v", c)
	}
	if wh.secrets != 0 || wh.named != 0 || e.deliveryOf(groupID, destWH).state != "retired" {
		t.Errorf("after the final edit %+v", wh)
	}
	if err := e.svc.ExportQueue(t.Context()); err != nil || strings.Contains(scrape(t), `muster_delivery_queue{destination="DSAAAAAAAAAA12"}`) {
		t.Errorf("a deleted destination keeps its queue series: %v", err)
	}

	// A Storm summary published there is retired with it, and the end of its Storm leaves it alone.
	summary := &fakeDelivery{id: 904, dest: destWH, storm: 7, state: "delivered", messageID: at2("s"), publishedAt: at(business0),
		threadState: "none"}
	e.db.deliveries = append(e.db.deliveries, summary)
	if err := e.svc.RetireDestination(t.Context(), nil, destWH); err != nil || summary.state != "retired" {
		t.Errorf("summary %+v %v", summary, err)
	}

	// Nothing waits: wiped at once.
	x := e.db.dests[destX]
	x.secrets, x.deleted = 1, true
	if err := e.svc.RetireDestination(t.Context(), nil, destX); err != nil || x.secrets != 0 {
		t.Errorf("wiped at once %+v %v", x, err)
	}

	// A Broken Destination deleted: MusterDestinationBroken resolved; the probe attempts the final edit.
	e.rec.Reset()
	e.breakDest(destMM, "503")
	mm := e.db.dests[destMM]
	mm.deleted, mm.secrets = true, 1
	e.db.routeDests[routeID] = nil
	if err := e.svc.RetireDestination(t.Context(), nil, destMM); err != nil {
		t.Fatal(err)
	}
	if got := e.raised("resolved"); !slices.Equal(got, []string{"MusterDestinationBroken/DSAAAAAAAAAA11"}) {
		t.Errorf("resolved %v", got)
	}
	if next := e.round(t); next > delivery.BrokenProbeInterval || len(e.rec.Calls()) != 0 {
		t.Fatalf("before the probe: next %v, calls %d", next, len(e.rec.Calls()))
	}
	e.business.Set(*mm.nextProbe)
	e.round(t)
	if c := e.callsTo(destMM); callMethods(c) != "update" || mm.secrets != 0 || mm.health != "healthy" {
		t.Errorf("probe of a deleted destination %s %+v", callMethods(c), mm)
	}
	e.business.Advance(delivery.BrokenProbeInterval)
	e.breakDest(destMM, "503")
	e.rec.Reset()
	e.business.Set(*mm.nextProbe)
	e.round(t)
	if len(e.rec.Calls()) != 0 {
		t.Errorf("a deleted destination with nothing waiting was probed: %+v", e.rec.Calls())
	}
}

// TestMoveToDefaultRoute is C-09.FR-19, C-11.FR-14 and FR-20: an Alert Group moved to the Default route gets the final
// edit in the Destinations it leaves, a Quiet Publication in those of the Default route it joins — afresh where it
// left before — and an update in those of both.
func TestMoveToDefaultRoute(t *testing.T) {
	e := memberEnv(t)
	e.db.routeDests[routeID] = []int64{destMM, destX}
	e.db.routeDests[defaultID] = []int64{destMM, destWH}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 900, dest: destWH, group: groupID, state: "retired",
		messageID: at2("old"), publishedAt: at(business0), threadState: "none", next: business0})
	e.rec.Reset()
	e.db.groups[groupID].route = defaultID
	g := e.group(groups.StatusFiring, "b")
	g.RouteID = defaultID
	e.enqueue(t, g, groups.System, groups.Recorded{Seq: 2, Event: groups.EventMovedToDefaultRoute,
		Loudness: groups.Quiet})
	e.round(t)
	mm, x, wh := e.callsTo(destMM), e.callsTo(destX), e.callsTo(destWH)
	if callMethods(mm) != "update" || mm[0].Loudness != groups.Quiet || strings.Contains(textOf(mm[0]),
		"No longer updated") {
		t.Errorf("both %+v", mm)
	}
	if callMethods(x) != "update" || x[0].Message.Notices[len(x[0].Message.Notices)-1] != finalText("AGAAAAAAAAAA21") ||
		e.deliveryOf(groupID, destX).state != "retired" {
		t.Errorf("left %+v", x)
	}
	if callMethods(wh) != "publish" || wh[0].Loudness != groups.Quiet || len(wh[0].Mentions) != 0 {
		t.Errorf("joined %+v", wh)
	}

	// Moved to a Default route without Destinations: final edits only.
	e = memberEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	e.rec.Reset()
	g = e.group(groups.StatusFiring, "a")
	g.RouteID = defaultID
	notified := e.db.notified
	e.enqueue(t, g, groups.System, groups.Recorded{Seq: 2, Event: groups.EventMovedToDefaultRoute})
	e.round(t)
	if callMethods(e.rec.Calls()) != "update" || e.db.notified != notified+1 {
		t.Errorf("moved without destinations %+v", e.rec.Calls())
	}
}

// TestAbandonConnection is C-11.FR-14: deleting a Connection ends the final edits still pending in its deleted
// Destinations as Not delivered with "the Connection was deleted", with the delivery event, drops their Thread replies
// and wipes their secrets; once the caller's transaction committed, each ended delivery is logged as
// delivery_not_delivered, and nothing is logged before.
func TestAbandonConnection(t *testing.T) {
	e := memberEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "fp2"))
	mm := e.db.dests[destMM]
	mm.deleted, mm.secrets = true, 1
	e.breakDest(destMM, "403")
	if err := e.svc.RetireDestination(t.Context(), nil, destMM); err != nil {
		t.Fatal(err)
	}
	d := e.only(t)
	if d.state != "pending" || mm.secrets != 1 {
		t.Fatalf("waiting %+v", d)
	}
	e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 901, dest: destMM, storm: 5, state: "pending",
		messageID: at2("s"), publishedAt: at(business0), desiredRetire: true, threadState: "none"},
		&fakeDelivery{id: 902, dest: destMM, storm: 6, state: "pending", threadState: "none"},
		&fakeDelivery{id: 903, dest: destMM, group: groupID, state: "pending", threadState: "none"},
		&fakeDelivery{id: 904, dest: destMM, group: groupID, state: "pending", messageID: at2("u"), publishedAt: at(business0),
			threadState: "none"})
	e.log.Reset()
	committed, err := e.svc.AbandonConnection(t.Context(), nil, connID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.log.String(), "delivery_not_delivered") {
		t.Errorf("logged before the commit: %s", e.log)
	}
	committed(t.Context())
	lines := e.log.String()
	if strings.Count(lines, `"event":"delivery_not_delivered"`) != 5 ||
		!strings.Contains(lines, `"group":"AGAAAAAAAAAA21","kind":"final_edit"`) ||
		!strings.Contains(lines, `"group":"","kind":"final_edit"`) ||
		!strings.Contains(lines, `"group":"","kind":"storm_summary"`) ||
		!strings.Contains(lines, `"group":"AGAAAAAAAAAA21","kind":"publication"`) ||
		!strings.Contains(lines, `"group":"AGAAAAAAAAAA21","kind":"update"`) {
		t.Errorf("abandoned lines %s", lines)
	}
	ev := e.db.events[slices.IndexFunc(e.db.events, func(ev dbgen.InsertDeliveryEventParams) bool {
		return ev.Kind == "not_delivered" && ev.StormID.Int64 == 5
	})]
	if d.state != "not_delivered" || *d.lastError != "the Connection was deleted" || mm.secrets != 0 ||
		ev.Kind != "not_delivered" || ev.Error.String != "the Connection was deleted" || ev.StormID.Int64 != 5 {
		t.Errorf("abandoned %+v %+v", d, ev)
	}
	if r := e.db.replies[0]; r.state != "dropped" {
		t.Errorf("reply %+v", r)
	}
}

// TestMembershipFailures: a failed query fails the change that needed it.
func TestMembershipFailures(t *testing.T) {
	published := func(t *testing.T) *env {
		e := memberEnv(t)
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
		e.round(t)
		return e
	}
	for _, q := range []string{"RetireDeliveries", "DropDeliveryReplies", "ListOpenGroupsOfRoute",
		"ListRouteDestinations", "ListEntryDeliveries", "EnsureDelivery", "RejoinDelivery", "SetDesired",
		"NotifyDelivery"} {
		e := published(t)
		e.db.routeDests[routeID] = []int64{destWH}
		e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 902, dest: destWH, group: groupID,
			state: "retired", threadState: "none"})
		e.db.fail[q] = errBoom
		err := e.svc.RouteDestinationsChanged(t.Context(), nil, e.changeOf(routeID, []int64{destWH},
			[]int64{destMM}))
		if !errors.Is(err, errBoom) {
			t.Errorf("membership with %s failing = %v", q, err)
		}
	}
	for _, q := range []string{"GetDestinationState", "RetireDestinationDeliveries", "SettleDestinationLeftovers",
		"DropDestinationReplies", "InsertInternalBody", "WipeDestinationSecrets", "NotifyDelivery"} {
		e := published(t)
		e.breakDest(destMM, "x")
		e.db.fail[q] = errBoom
		if err := e.svc.RetireDestination(t.Context(), nil, destMM); !errors.Is(err, errBoom) {
			t.Errorf("deletion with %s failing = %v", q, err)
		}
	}
	for _, q := range []string{"AbandonConnectionDeliveries", "InsertDeliveryEvent",
		"ListDeletedDestinationsOfConnection", "DropDestinationReplies", "WipeDestinationSecrets"} {
		e := published(t)
		e.db.dests[destMM].deleted = true
		e.only(t).state = "pending"
		e.db.fail[q] = errBoom
		if _, err := e.svc.AbandonConnection(t.Context(), nil, connID); !errors.Is(err, errBoom) {
			t.Errorf("abandon with %s failing = %v", q, err)
		}
	}
	for _, q := range []string{"RetireGroupDeliveries", "DropDeliveryReplies", "RejoinDelivery"} {
		e := published(t)
		e.db.routeDests[defaultID] = []int64{destWH}
		e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 903, dest: destWH, group: groupID,
			state: "retired", threadState: "none"})
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "fp2"))
		e.db.fail[q] = errBoom
		g := e.group(groups.StatusFiring, "a")
		g.RouteID = defaultID
		err := e.svc.Enqueue(t.Context(), nil, groups.Rendering{Group: g, Actor: groups.System,
			Events: []groups.Recorded{{Seq: 3, Event: groups.EventMovedToDefaultRoute}}})
		if !errors.Is(err, errBoom) {
			t.Errorf("move with %s failing = %v", q, err)
		}
	}
	for _, q := range []string{"WithholdLeased", "RecordRetired"} {
		e := published(t)
		d := e.only(t)
		if q == "WithholdLeased" {
			d.messageID, d.publishedAt = nil, nil
		}
		d.state, d.desiredRetire = "pending", true
		e.db.fail[q] = errBoom
		e.round(t)
		if !strings.Contains(e.log.String(), "boom") || d.state != "pending" {
			t.Errorf("final edit with %s failing: %+v", q, d)
		}
	}
	e := published(t)
	d := e.only(t)
	d.state, d.desiredRetire = "pending", true
	e.db.fail["InsertDeliveryEvent"] = errBoom
	e.round(t)
	if !strings.Contains(e.log.String(), "boom") {
		t.Error("a failed final edit event was not logged")
	}
	e = published(t)
	d = e.only(t)
	d.state, d.desiredRetire = "pending", true
	e.db.before["RecordRetired"] = func() { d.owner = "r2" }
	e.round(t)
	if d.state != "pending" {
		t.Errorf("a lost lease retired the delivery %+v", d)
	}
}

// recoverOps makes the probe of the Broken Destination ops due and runs a round.
func (e *env) recoverOps(t *testing.T) {
	t.Helper()
	e.business.Set(*e.db.dests[destMM].nextProbe)
	e.round(t)
}

// without are the calls but those of the Destination check.
func without(calls []deliverytest.Call, method string) []deliverytest.Call {
	return slices.DeleteFunc(slices.Clone(calls), func(c deliverytest.Call) bool { return c.Method == method })
}

// stormStarted is an env whose Route, with route.storm_threshold 1, has a Storm whose summary is published.
func stormStarted(t *testing.T) *env {
	t.Helper()
	e := stormEnv(t)
	r := e.db.routes[routeID]
	r.threshold = 1
	e.db.routes[routeID] = r
	e.addGroup(t, 5001, false)
	e.round(t)
	e.rec.Reset()
	e.addGroup(t, 5002, false)
	e.round(t)
	return e
}

// TestDeliveryEventRows is C-11.FR-20 and AC-10, table-driven: every row of the Loud/Quiet table of delivery events
// produces through the recording adapter the new message, the edit or nothing it names, Loud or Quiet and with its
// Mentions; the Publication after a recovery is Loud only for an Alert Group firing at that moment.
func TestDeliveryEventRows(t *testing.T) {
	if len(delivery.DeliveryEvents) != 12 {
		t.Fatalf("%d rows", len(delivery.DeliveryEvents))
	}
	type scenario struct {
		firing bool
		run    func(t *testing.T) []deliverytest.Call
	}
	published := func(t *testing.T) *env {
		e := memberEnv(t)
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
		e.round(t)
		e.rec.Reset()
		return e
	}
	scenarios := map[string][]scenario{
		delivery.DeliveryAddedDestination: {{true, func(t *testing.T) []deliverytest.Call {
			e := published(t)
			e.db.routeDests[routeID] = []int64{destMM, destWH}
			e.changed(t, routeID, []int64{destWH}, nil)
			e.round(t)
			return e.rec.Calls()
		}}},
		delivery.DeliveryStormSummary: {{true, func(t *testing.T) []deliverytest.Call {
			return stormStarted(t).rec.Calls()
		}}},
		delivery.DeliveryStormUpdate: {{true, func(t *testing.T) []deliverytest.Call {
			e := stormStarted(t)
			e.rec.Reset()
			e.addGroup(t, 5003, false)
			e.round(t)
			return e.rec.Calls()
		}}},
		delivery.DeliveryAfterStorm: {{true, func(t *testing.T) []deliverytest.Call {
			e := stormStarted(t)
			e.rec.Reset()
			e.calmCheck(t, e.onlyStorm(t))
			e.calmCheck(t, e.onlyStorm(t))
			e.round(t)
			return slices.DeleteFunc(e.rec.Calls(), func(c deliverytest.Call) bool {
				return strings.HasPrefix(textOf(c), "Storm over")
			})
		}}},
		delivery.DeliveryLate: {{true, func(t *testing.T) []deliverytest.Call {
			e := memberEnv(t)
			e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
			e.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(30*time.Second,
				delivery.ScopeDestination))
			e.round(t)
			e.resolve(t)
			e.rec.Reset()
			e.business.Advance(31 * time.Second)
			e.round(t)
			// The resolution's own Thread reply follows the Root message, as its lifecycle row says.
			calls := without(e.rec.Calls(), deliverytest.MethodReply)
			if len(calls) != 1 || !strings.Contains(textOf(calls[0]), "Delivered late") {
				t.Errorf("late %+v", calls)
			}
			return calls
		}}},
		delivery.DeliveryAfterRecovery: {
			{true, func(t *testing.T) []deliverytest.Call {
				e := memberEnv(t)
				e.breakDest(destMM, "503")
				e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
				e.recoverOps(t)
				return e.rec.Calls()
			}},
			{false, func(t *testing.T) []deliverytest.Call {
				e := memberEnv(t)
				e.breakDest(destMM, "503")
				e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
				e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
					groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
				e.recoverOps(t)
				return e.rec.Calls()
			}},
		},
		delivery.DeliveryUpdateRecovery: {{true, func(t *testing.T) []deliverytest.Call {
			e := published(t)
			e.breakDest(destMM, "503")
			e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
				groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
			e.recoverOps(t)
			return e.rec.Calls()
		}}},
		delivery.DeliveryRepliesWhileBroken: {{true, func(t *testing.T) []deliverytest.Call {
			e := published(t)
			e.breakDest(destMM, "503")
			e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, alertsAdded(2, groups.StatusFiring, "fp2"))
			e.recoverOps(t)
			return without(e.rec.Calls(), deliverytest.MethodCheck)
		}}},
		delivery.DeliveryResolvedBroken: {{true, func(t *testing.T) []deliverytest.Call {
			e := memberEnv(t)
			e.breakDest(destMM, "503")
			e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
			e.resolve(t)
			e.recoverOps(t)
			if d := e.only(t); d.state != "withheld" || e.db.dests[destMM].health != "healthy" {
				t.Errorf("resolved while broken %+v", d)
			}
			return without(e.rec.Calls(), deliverytest.MethodCheck)
		}}},
		delivery.DeliveryRepublication: {{true, func(t *testing.T) []deliverytest.Call {
			e := published(t)
			e.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "deleted"))
			e.enqueue(t, e.group(groups.StatusAcknowledged, "a"), groups.System,
				groups.Recorded{Seq: 2, Event: groups.EventAcknowledged, Loudness: groups.Quiet})
			e.round(t)
			e.rec.Reset()
			e.round(t)
			return e.rec.Calls()
		}}},
		delivery.DeliveryFinalEdit: {{true, func(t *testing.T) []deliverytest.Call {
			e := published(t)
			e.db.routeDests[routeID] = nil
			e.changed(t, routeID, nil, []int64{destMM})
			e.round(t)
			return e.rec.Calls()
		}}},
		// A change of status past a bridge's edit window: the new Root message, apart from the pointer reply under
		// the old one.
		delivery.DeliveryPastEditWindow: {{true, func(t *testing.T) []deliverytest.Call {
			e, _, _ := publishedWindowed(t)
			e.business.Set(business0.Add(11 * time.Minute))
			e.rec.Script(deliverytest.MethodPublish, publishedAs("m2", "t-2"))
			e.acknowledged(t, 2)
			e.round(t)
			return without(e.rec.Calls(), deliverytest.MethodReply)
		}}},
	}
	for _, row := range delivery.DeliveryEvents {
		for _, sc := range scenarios[row.Name] {
			t.Run(row.Name, func(t *testing.T) {
				calls := sc.run(t)
				loudness, mentions := row.Loudness, row.Mentions
				if row.LoudWhenFiring && !sc.firing {
					loudness, mentions = groups.Quiet, nil
				}
				want := map[delivery.Form]string{delivery.FormPublication: deliverytest.MethodPublish,
					delivery.FormUpdate: deliverytest.MethodUpdate, delivery.FormNothing: ""}[row.Form]
				if want == "" {
					if len(calls) != 0 {
						t.Errorf("sent %+v", calls)
					}
					return
				}
				if len(calls) != 1 || calls[0].Method != want || calls[0].Loudness != loudness ||
					!slices.Equal(calls[0].Mentions, mentions) {
					t.Errorf("want one %s %s %v, got %+v", want, loudness, mentions, calls)
				}
			})
		}
		if scenarios[row.Name] == nil {
			t.Errorf("no scenario for %s", row.Name)
		}
	}
}

// entriesEnv is reachEnv with the public URL of the final edits and the warning Alert Group of the fixtures of the
// Team payments, published in infra-notice and payments-notice.
func entriesEnv(t *testing.T) *env {
	t.Helper()
	e := reachEnv(t)
	e.w.PublicURL = publicURL
	e.db.groups[groupID].teams = []int64{teamPayments}
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments},
		created())
	e.round(t)
	e.rec.Reset()
	return e
}

// setCondition sets the Severity condition of a Destination of the Route, adding it when missing, or removes it with
// an empty condition, and returns the change routing gives.
func (e *env) setCondition(dest int64, condition string) routing.MembershipChange {
	before := e.changeOf(routeID, nil, nil).After
	if condition == "" {
		e.db.routeDests[routeID] = slices.DeleteFunc(slices.Clone(e.db.routeDests[routeID]),
			func(d int64) bool { return d == dest })
		delete(e.db.conditions[routeID], dest)
	} else {
		if !slices.Contains(e.db.routeDests[routeID], dest) {
			e.db.routeDests[routeID] = append(e.db.routeDests[routeID], dest)
		}
		e.db.conditions[routeID][dest] = condition
	}
	c := e.changeOf(routeID, nil, nil)
	c.Before = before
	return c
}

// setToTeams sets "Also to the Teams from the label" of the Route and returns the change routing gives.
func (e *env) setToTeams(on bool) routing.MembershipChange {
	c := e.changeOf(routeID, nil, nil)
	r := e.db.routes[routeID]
	r.toTeams = on
	e.db.routes[routeID] = r
	c.ToTeamsAfter = on
	return c
}

func (e *env) route(t *testing.T, c routing.MembershipChange) {
	t.Helper()
	if err := e.svc.RouteDestinationsChanged(t.Context(), nil, c); err != nil {
		t.Fatal(err)
	}
}

// finalEdited reports whether the calls give the Alert Group of the fixtures its final edit in the Destination.
func (e *env) finalEdited(dest int64) bool {
	return slices.ContainsFunc(e.callsTo(dest), func(c deliverytest.Call) bool {
		return c.Method == deliverytest.MethodUpdate && len(c.Message.Notices) > 0 &&
			c.Message.Notices[len(c.Message.Notices)-1] == finalText("AGAAAAAAAAAA21")
	})
}

// entriesIn are the entries of the delivery of the Alert Group of the fixtures in the Destination, as stored.
func (e *env) entriesIn(t *testing.T, dest int64) string {
	t.Helper()
	d := e.deliveryOf(groupID, dest)
	if d == nil {
		return ""
	}
	return string(d.entries)
}

// TestRouteEntriesChanged is C-26.FR-6, AC-6 and C-11.FR-14 revised on a Route's save: a condition widened from
// critical to any publishes the warning Alert Group Quietly in that Destination, with its entry, without Mentions;
// narrowed back it removes nothing; a Destination added with critical receives only the critical Alert Group, Quietly;
// a level that fell gains no critical Destination at a save; a Destination taken off the Route gets the final edit for
// the Alert Group whose delivery the Route's entry reached, and one that a Team still names stays, with the Team's
// entry appended — met or not — so that the Team's removal later gives it the final edit; a delivery made before the
// entries were recorded counts as the Route's. A change that moves nothing calls nothing.
func TestRouteEntriesChanged(t *testing.T) {
	e := entriesEnv(t)
	e.route(t, e.setCondition(destCritical, "any"))
	e.round(t)
	if c := e.published(t, destCritical); c.Loudness != groups.Quiet || len(c.Mentions) != 0 {
		t.Errorf("the widened condition published %+v", c)
	}
	if got := e.entriesIn(t, destCritical); got != `[{"kind":"route","severity_condition":"any"}]` {
		t.Errorf("entries %s", got)
	}
	if d := e.deliveryOf(groupID, destCritical); d.loud.Bool {
		t.Errorf("a loud publication %+v", d.loud)
	}
	e.rec.Reset()
	e.route(t, e.setCondition(destCritical, "critical"))
	e.round(t)
	if d := e.deliveryOf(groupID, destCritical); len(e.rec.Calls()) != 0 || d.state != "delivered" || d.desiredRetire {
		t.Errorf("the narrowed condition removed: calls %+v, delivery %s", e.rec.Calls(), d.state)
	}

	// A Destination added with critical: the warning Alert Group stays out, a critical one arrives Quietly.
	e.db.groups[22] = &fakeGroup{id: 22, publicID: "AGAAAAAAAA0022", number: 22, title: "g", status: "firing",
		urgent: true, route: routeID, created: e.business.Now(), severity: "critical"}
	e.route(t, e.setCondition(criticalTwo(e), "critical"))
	e.round(t)
	if e.deliveryOf(groupID, 16) != nil || e.deliveryOf(22, 16) == nil {
		t.Errorf("deliveries in the critical destination: warning %+v, critical %+v", e.deliveryOf(groupID, 16),
			e.deliveryOf(22, 16))
	}
	if c := e.published(t, 16); c.Loudness != groups.Quiet || len(c.Mentions) != 0 {
		t.Errorf("the critical alert group arrived %+v", c)
	}
	delete(e.db.groups, 22)
	e.rec.Reset()

	// Off the Route: the final edit where only the Route named it.
	e.route(t, e.setCondition(destCritical, ""))
	e.round(t)
	if !e.finalEdited(destCritical) || e.deliveryOf(groupID, destCritical).state != "retired" {
		t.Errorf("no final edit in the removed destination: %+v", e.callsTo(destCritical))
	}
	if e.db.calls["ShareTeamMembership"] == 0 {
		t.Error("the change read the teams' entries without their locks")
	}

	// payments-notice: named by the Route for critical, which the warning Alert Group does not meet, and by the Team.
	e.rec.Reset()
	e.route(t, e.setCondition(destTeamNotice, "critical"))
	e.round(t)
	if len(e.callsTo(destTeamNotice)) != 0 {
		t.Errorf("an unmet condition published %+v", e.callsTo(destTeamNotice))
	}
	// The Team's entry is taken by turning the option off: the Route's still names it, so it stays, with that entry.
	e.route(t, e.setToTeams(false))
	e.round(t)
	if d := e.deliveryOf(groupID, destTeamNotice); e.finalEdited(destTeamNotice) || d.state != "delivered" ||
		e.entriesIn(t, destTeamNotice) != `[{"kind":"team","team_id":7,"severity_condition":"below_critical"},`+
			`{"kind":"route","severity_condition":"critical"}]` {
		t.Errorf("payments-notice: calls %+v, entries %s", e.callsTo(destTeamNotice), e.entriesIn(t, destTeamNotice))
	}
	// Its Route entry, never met, is taken away: nothing names it now, and it gets the final edit.
	e.route(t, e.setCondition(destTeamNotice, ""))
	e.round(t)
	if !e.finalEdited(destTeamNotice) {
		t.Errorf("no final edit after the last entry: %+v", e.callsTo(destTeamNotice))
	}

	// A delivery made before the entries were recorded holds the Route's entry.
	e.deliveryOf(groupID, destMM).entries = []byte("[]")
	e.rec.Reset()
	e.route(t, e.setCondition(destMM, ""))
	e.round(t)
	if !e.finalEdited(destMM) {
		t.Errorf("no final edit of a delivery without entries: %+v", e.callsTo(destMM))
	}
	calls := e.db.calls["ListOpenGroupsOfRoute"]
	e.route(t, e.changeOf(routeID, nil, nil))
	if e.db.calls["ListOpenGroupsOfRoute"] != calls {
		t.Error("a change that moved nothing read the alert groups")
	}
}

// criticalTwo adds the Mattermost Destination 16 "critical-two" to the fake and returns its id.
func criticalTwo(e *env) int64 {
	conn := int64(connID)
	e.db.dests[16] = &fakeDest{id: 16, publicID: "DSAAAAAAAAAA16", name: "critical-two", typ: delivery.TypeMattermost,
		connection: &conn, health: "healthy", limit: 600, per: 60}
	return 16
}

// TestRouteTeamsOption is C-26.FR-6 and AC-10 on the option: turning "Also to the Teams from the label" on publishes
// the open Alert Groups Quietly in the Destinations of their Teams whose condition they meet, under the Teams' locks
// taken by id before any delivery; turning it off gives the final edit in each Destination only a Team named, and keeps
// the one the Route names too, met or not, whatever the Severity level, so that a level that fell keeps its Root
// message in a critical Destination it reached.
func TestRouteTeamsOption(t *testing.T) {
	e := reachEnv(t)
	e.w.PublicURL = publicURL
	e.db.groups[groupID].teams = []int64{teamPayments, 3}
	r := e.db.routes[routeID]
	r.toTeams = false
	e.db.routes[routeID] = r
	e.render(t, e.leveled(groups.StatusFiring, "warning", false), groups.System, false, []int64{teamPayments, 3},
		created())
	e.round(t)
	e.rec.Reset()
	ensured := e.db.calls["EnsureDelivery"]
	e.db.before["LockActiveStorm"] = func() {
		if !slices.Equal(e.db.teamLocks, []int64{3, teamPayments}) || e.db.calls["EnsureDelivery"] != ensured {
			t.Errorf("before the storm: team locks %v, deliveries %d", e.db.teamLocks, e.db.calls["EnsureDelivery"])
		}
	}
	e.route(t, e.setToTeams(true))
	e.round(t)
	if c := e.published(t, destTeamNotice); c.Loudness != groups.Quiet || len(c.Mentions) != 0 ||
		e.deliveryOf(groupID, destTeamCrit) != nil {
		t.Errorf("the option on: %+v, critical %+v", c, e.deliveryOf(groupID, destTeamCrit))
	}

	// A rise to critical through Alerts brings payments-critical; the level falls again; the option off gives the
	// final edit in both Team Destinations, which the Route does not name, and none in the Route's.
	e.db.groups[groupID].severity = "critical"
	e.render(t, e.leveled(groups.StatusFiring, "critical", true), groups.System, false, []int64{teamPayments, 3},
		groups.Recorded{Seq: 2, Event: groups.EventSeverityRaised, Loudness: groups.Quiet},
		joinedEvent(3, destTeamCrit, groups.StatusFiring), joinedEvent(4, destCritical, groups.StatusFiring))
	e.db.groups[groupID].severity = "warning"
	e.round(t)
	e.rec.Reset()
	e.route(t, e.setToTeams(false))
	e.round(t)
	if !e.finalEdited(destTeamNotice) || !e.finalEdited(destTeamCrit) || e.finalEdited(destMM) ||
		e.finalEdited(destCritical) {
		t.Errorf("the option off: calls %+v", e.rec.Calls())
	}
	if d := e.deliveryOf(groupID, destCritical); d.state != "delivered" || d.desiredRetire {
		t.Errorf("the critical destination of the route after the fall %+v", d.state)
	}
	// A second save with the option off finds nothing more to do.
	e.rec.Reset()
	e.route(t, e.setToTeams(false))
	e.round(t)
	if len(e.rec.Calls()) != 0 {
		t.Errorf("a second save %+v", e.rec.Calls())
	}
}

// TestFinalEditsFor is C-26.FR-6, AC-10 and C-25.FR-10 on a Team's change (groups.FinalEdits): after the Team's entry
// of a Destination is removed, the delivery its entry reached gets the final edit, unless an entry of the Route names
// the Destination — a Destination named by the Route for critical and by the Team below critical keeps its Root
// message for the warning Alert Group, with the Route's entry appended — or another Team of the Alert Group does; a
// deleted Team's Destinations all get it; the deliveries the Team never reached are left alone; a second run changes
// nothing; a failing query fails it.
func TestFinalEditsFor(t *testing.T) {
	e := entriesEnv(t)
	e.db.conditions[routeID][destTeamNotice] = "critical"
	e.db.routeDests[routeID] = append(e.db.routeDests[routeID], destTeamNotice)
	e.db.teamDests[teamPayments] = nil
	g := e.leveled(groups.StatusFiring, "warning", true)
	n, err := e.svc.FinalEditsFor(t.Context(), nil, g, teamPayments)
	e.round(t)
	if err != nil || n != 0 || e.finalEdited(destTeamNotice) || e.entriesIn(t, destTeamNotice) !=
		`[{"kind":"team","team_id":7,"severity_condition":"below_critical"},`+
			`{"kind":"route","severity_condition":"critical"}]` {
		t.Errorf("named by the route: %d, %v, entries %s", n, err, e.entriesIn(t, destTeamNotice))
	}
	if n, err := e.svc.FinalEditsFor(t.Context(), nil, g, teamPayments); err != nil || n != 0 ||
		e.db.calls["AppendDeliveryEntries"] != 1 {
		t.Errorf("a second run: %d, %v, %d appends", n, err, e.db.calls["AppendDeliveryEntries"])
	}

	// The Team deleted: its entries are gone, and the Route no longer names payments-notice.
	e = entriesEnv(t)
	e.db.groups[groupID].teams = []int64{teamPayments, 3}
	e.db.teamDests[3] = []routing.Entry{{DestinationID: destCritical, Condition: routing.ConditionCritical}}
	e.deliveryOf(groupID, destMM).entries = []byte(`[{"kind":"route","severity_condition":"below_critical"},` +
		`{"kind":"team","team_id":7,"severity_condition":"any"}]`)
	delete(e.db.teamDests, teamPayments)
	e.db.hints = nil
	n, err = e.svc.FinalEditsFor(t.Context(), nil, g, teamPayments)
	if !slices.Equal(e.db.hints, []db.Hint{groupHint}) {
		t.Errorf("hints of the final edits %v", e.db.hints)
	}
	e.round(t)
	if err != nil || n != 1 || !e.finalEdited(destTeamNotice) || e.finalEdited(destMM) {
		t.Errorf("a deleted team: %d, %v, calls %+v", n, err, e.rec.Calls())
	}
	if n, err := e.svc.FinalEditsFor(t.Context(), nil, g, teamPayments); err != nil || n != 0 {
		t.Errorf("a second run: %d, %v", n, err)
	}
	if n, err := e.svc.FinalEditsFor(t.Context(), nil, g, 99); err != nil || n != 0 ||
		e.db.calls["ListGroupTeams"] != 2 {
		t.Errorf("a team that reached nothing: %d, %v", n, err)
	}

	for _, q := range []string{"ListEntryDeliveries", "GetRouteDelivery", "ListGroupTeams", "ListRouteDestinations",
		"ListTeamDestinations", "RetireDeliveries", "DropDeliveryReplies", "Notify", "NotifyDelivery",
		"AppendDeliveryEntries"} {
		e := entriesEnv(t)
		e.db.groups[groupID].teams = []int64{teamPayments, 3}
		e.db.teamDests[3] = []routing.Entry{{DestinationID: destMM}}
		delete(e.db.teamDests, teamPayments)
		if q == "AppendDeliveryEntries" {
			e.db.teamDests[3] = []routing.Entry{{DestinationID: destTeamNotice}}
		}
		e.db.fail[q] = errBoom
		if _, err := e.svc.FinalEditsFor(t.Context(), nil, g, teamPayments); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
	}
}

// TestNoSummaryWithoutHeldGroup is C-11.FR-6 with ADR-0018: during a Storm of warning Alert Groups, a Destination added
// to the Route for critical, which none of them reaches, gets no Storm summary — no Loud message with the Mentions of
// a new Alert Group — while one they reach gets it with the held Alert Groups.
func TestNoSummaryWithoutHeldGroup(t *testing.T) {
	e := stormStarted(t)
	st := e.onlyStorm(t)
	summaryIn := func(dest int64) bool {
		return slices.ContainsFunc(e.db.deliveries, func(d *fakeDelivery) bool { return d.storm == st.id && d.dest == dest })
	}
	e.db.conditions = map[int64]map[int64]string{routeID: {destWH: "critical"}}
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	e.changed(t, routeID, []int64{destWH}, nil)
	if summaryIn(destWH) || slices.ContainsFunc(e.db.deliveries, func(d *fakeDelivery) bool { return d.dest == destWH }) {
		t.Errorf("the destination no held alert group reaches got %+v", e.db.deliveries)
	}
	e.route(t, e.setCondition(destWH, "any"))
	if !summaryIn(destWH) {
		t.Error("the destination the held alert groups reach got no summary")
	}
}

// TestDeletedDestinationNeverPublished is C-11.FR-14 against a late delivery: one that an Enqueue created in a
// Destination while its deletion was committing — after the deletion ended its other deliveries — is claimed, never
// published there, and withheld without a call, with its Alert Group's hint; the secrets of the Destination are then
// wiped. A final edit there still goes out.
func TestDeletedDestinationNeverPublished(t *testing.T) {
	e := memberEnv(t)
	e.db.routeDests[routeID] = []int64{destMM, destWH}
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	e.rec.Reset()
	wh := e.db.dests[destWH]
	wh.secrets, wh.deleted = 3, true
	late := &fakeDelivery{id: 905, dest: destWH, group: 22, state: "pending", threadState: "none",
		next: e.business.Now(), entries: []byte(`[{"kind":"team","team_id":7,"severity_condition":"any"}]`)}
	e.db.groups[22] = &fakeGroup{id: 22, publicID: "AGAAAAAAAA0022", number: 22, title: "g", status: "firing",
		route: routeID, created: e.business.Now()}
	if err := e.svc.RetireDestination(t.Context(), nil, destWH); err != nil {
		t.Fatal(err)
	}
	// Created after the deletion ended the Destination's deliveries, before it committed.
	e.db.deliveries = append(e.db.deliveries, late)
	e.db.hints = nil
	e.round(t)
	if late.state != "withheld" || late.messageID != nil || late.publishedAt != nil {
		t.Errorf("the late delivery in the deleted destination: %+v", late)
	}
	if c := e.callsTo(destWH); callMethods(c) != "update" ||
		c[0].Message.Notices[len(c[0].Message.Notices)-1] != finalText("AGAAAAAAAAAA21") {
		t.Errorf("calls to the deleted destination %+v", c)
	}
	if wh.secrets != 0 || !slices.Contains(e.db.hints, db.Hint{OrgID: orgID, Type: "alert-group",
		ID: "AGAAAAAAAA0022"}) {
		t.Errorf("secrets %d, hints %+v", wh.secrets, e.db.hints)
	}
}

// TestJoinedAgainDuringFinalEdit is C-26.FR-4 with C-11.FR-14: a Destination added back to the Route while the final
// edit of a Root message there is in flight leaves the delivery pending, not retired, without an actual state, so the
// next call edits the Root message back to the current state; the final edit itself is recorded, as it was made.
func TestJoinedAgainDuringFinalEdit(t *testing.T) {
	e := memberEnv(t)
	e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
	e.round(t)
	d := e.only(t)
	e.db.routeDests[routeID] = nil
	e.changed(t, routeID, nil, []int64{destMM})
	if !d.desiredRetire {
		t.Fatalf("no final edit waits %+v", d)
	}
	e.rec.Reset()
	e.rec.Script(deliverytest.MethodUpdate, deliverytest.Answer{Outcome: deliverytest.OK().Outcome, Then: func() {
		e.db.routeDests[routeID] = []int64{destMM}
		e.changed(t, routeID, []int64{destMM}, nil)
	}})
	e.round(t)
	if d.state != "pending" || d.desiredRetire || d.actualHash != nil || d.messageID == nil {
		t.Fatalf("joined again during the final edit %+v", d)
	}
	if !slices.Contains(e.eventKinds(), "final_edit") {
		t.Errorf("the final edit made is not recorded: %v", e.eventKinds())
	}
	e.rec.Reset()
	e.round(t)
	c := e.callsTo(destMM)
	if callMethods(c) != "update" || slices.Contains(c[0].Message.Notices, finalText("AGAAAAAAAAAA21")) ||
		len(c[0].Message.Buttons) == 0 || d.state != "delivered" {
		t.Errorf("edited back %s %+v, state %s", callMethods(c), c, d.state)
	}
}

// TestDeletedDestinationFailedPublication is C-11.FR-14 against a Publication claimed before its Destination's
// deletion committed that then failed: the worker claims it again once the Destination is deleted and ends it —
// withheld without a call when the Publication cannot exist, the final edit when it may (an Idempotent adapter, whose
// Publication the bridge may have made: replayed with the final state) — so nothing of the Destination stays pending
// and its secrets are wiped.
func TestDeletedDestinationFailedPublication(t *testing.T) {
	t.Run("not idempotent", func(t *testing.T) {
		e := memberEnv(t)
		e.enqueue(t, e.group(groups.StatusFiring, "a"), groups.System, created())
		e.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
		e.round(t)
		d := e.only(t)
		if d.state != "pending" || d.publishedAt != nil || d.attempts == 0 {
			t.Fatalf("after the failed publication %+v", d)
		}
		// Its start recorded, as a call whose answer was lost leaves it.
		d.started = at(business0)
		mm := e.db.dests[destMM]
		mm.secrets, mm.deleted = 2, true
		e.rec.Reset()
		e.business.Set(d.next)
		e.round(t)
		if d.state != "withheld" || len(e.rec.Calls()) != 0 || mm.secrets != 0 {
			t.Errorf("the failed publication in the deleted destination: %+v, calls %d, secrets %d", d,
				len(e.rec.Calls()), mm.secrets)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		e, d, _ := pendingBridge(t)
		e.w.PublicURL = publicURL
		e.rec.Script(deliverytest.MethodPublish, timedOut())
		e.round(t)
		if d.state != "pending" || d.started == nil || d.publishedAt != nil {
			t.Fatalf("after the unanswered publication %+v", d)
		}
		e.db.dests[destMM].deleted = true
		e.rec.Reset()
		e.business.Set(d.next)
		e.round(t)
		c := e.rec.Calls()
		if len(c) != 1 || c[0].Method != deliverytest.MethodPublish ||
			c[0].Message.Notices[len(c[0].Message.Notices)-1] != finalText("AGAAAAAAAAAA21") || d.state != "retired" {
			t.Errorf("the publication that may exist: calls %+v, state %s", c, d.state)
		}
	})
}
