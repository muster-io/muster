// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
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
	if d.messageID == nil && d.started == nil {
		d.state, d.desiredRetire = "withheld", false
	} else {
		d.state, d.desiredRetire = "pending", true
	}
	d.heldBy, d.next, d.updated = 0, now, now
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
				Title: g.title, Status: g.status, Urgent: g.urgent, Held: held})
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
			d.threadState, d.anchorID, d.chainLastID, d.republished = "none", nil, nil, false
			d.loud, d.heldBy = pgtype.Bool{Bool: arg.Loud, Valid: true}, arg.HeldByStormID.Int64
		}
		d.lateNote, d.desiredRetire, d.state, d.next, d.updated = false, false, "pending", arg.Now, arg.Now
		return 1, nil
	}
	return 0, nil
}

func (f *fakeDB) RetireRouteDeliveries(_ context.Context, arg dbgen.RetireRouteDeliveriesParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("RetireRouteDeliveries"); err != nil {
		return nil, err
	}
	var out []int64
	for _, d := range f.deliveries {
		if d.dest == arg.DestinationID && d.storm == 0 && f.open(d.group) && f.groups[d.group].route == arg.RouteID &&
			!terminal(d) {
			retire(d, arg.Now)
			out = append(out, d.id)
		}
	}
	return out, nil
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
			if d.messageID == nil {
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
	if d.state != "withheld" && d.state != "deleted_in_messenger" {
		d.state = "retired"
	}
	d.desiredRetire, d.attempts, d.firstFailed, d.errorClass, d.lastError = false, 0, nil, nil, nil
	d.lastDelivered, d.owner, d.until, d.updated = at(arg.Now), "", time.Time{}, arg.Now
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
	return dbgen.GetDestinationStateRow{PublicID: ds.publicID, Health: ds.health}, nil
}

func (f *fakeDB) WipeDestinationSecrets(_ context.Context, arg dbgen.WipeDestinationSecretsParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("WipeDestinationSecrets"); err != nil {
		return err
	}
	if ds := f.dests[arg.ID]; ds.deleted && !f.pendingOf(ds.id) {
		ds.secrets, ds.named = 0, 0
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
			FinalEdit: d.desiredRetire, Unpublished: d.messageID == nil}
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

// changed runs the membership hook of the Route as routing does after it wrote route_destinations.
func (e *env) changed(t *testing.T, route int64, added, removed []int64) {
	t.Helper()
	if err := e.svc.RouteDestinationsChanged(t.Context(), nil, route, added, removed); err != nil {
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
	}) || !strings.HasPrefix(added[0].Message.Text(), "#7 a") || !strings.HasPrefix(added[1].Message.Text(), "#22 g") ||
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
			c.Message.Sections[len(c.Message.Sections)-1] != finalText(publicID) {
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
	if err := e.svc.RouteDestinationsChanged(t.Context(), nil, routeID, nil, nil); err != nil || e.db.calls["ListOpenGroupsOfRoute"] != 2 {
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
	if c := e.callsTo(destWH); callMethods(c) != "update" || c[0].Message.Sections[len(c[0].Message.Sections)-1] !=
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
	summary := &fakeDelivery{id: 904, dest: destWH, storm: 7, state: "delivered", messageID: at2("s"),
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
		messageID: at2("old"), threadState: "none", next: business0})
	e.rec.Reset()
	e.db.groups[groupID].route = defaultID
	g := e.group(groups.StatusFiring, "b")
	g.RouteID = defaultID
	e.enqueue(t, g, groups.System, groups.Recorded{Seq: 2, Event: groups.EventMovedToDefaultRoute,
		Loudness: groups.Quiet})
	e.round(t)
	mm, x, wh := e.callsTo(destMM), e.callsTo(destX), e.callsTo(destWH)
	if callMethods(mm) != "update" || mm[0].Loudness != groups.Quiet || strings.Contains(mm[0].Message.Text(),
		"No longer updated") {
		t.Errorf("both %+v", mm)
	}
	if callMethods(x) != "update" || x[0].Message.Sections[len(x[0].Message.Sections)-1] != finalText("AGAAAAAAAAAA21") ||
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
		messageID: at2("s"), desiredRetire: true, threadState: "none"},
		&fakeDelivery{id: 902, dest: destMM, storm: 6, state: "pending", threadState: "none"},
		&fakeDelivery{id: 903, dest: destMM, group: groupID, state: "pending", threadState: "none"},
		&fakeDelivery{id: 904, dest: destMM, group: groupID, state: "pending", messageID: at2("u"),
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
	for _, q := range []string{"RetireRouteDeliveries", "DropDeliveryReplies", "ListOpenGroupsOfRoute",
		"ListRouteDestinations", "GetRouteDelivery", "EnsureDelivery", "RejoinDelivery", "SetDesired",
		"NotifyDelivery"} {
		e := published(t)
		e.db.routeDests[routeID] = []int64{destMM, destWH}
		e.db.deliveries = append(e.db.deliveries, &fakeDelivery{id: 902, dest: destWH, group: groupID,
			state: "retired", threadState: "none"})
		e.db.fail[q] = errBoom
		err := e.svc.RouteDestinationsChanged(t.Context(), nil, routeID, []int64{destWH}, []int64{destMM})
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
			d.messageID = nil
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
	if len(delivery.DeliveryEvents) != 11 {
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
				return strings.HasPrefix(c.Message.Text(), "Storm over")
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
			if len(calls) != 1 || !strings.Contains(calls[0].Message.Text(), "Delivered late") {
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
