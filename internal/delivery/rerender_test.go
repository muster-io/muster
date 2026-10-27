// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	livehub "github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/messages"
)

// rerenderable is the selection of the re-render after a rotation: a delivery of an open Alert Group signed by another
// key than active, not a final edit, pending or delivered, or Not delivered with a Root message.
func (f *fakeDB) rerenderable(d *fakeDelivery, active string) bool {
	g := f.groups[d.group]
	return g != nil && g.status != "resolved" && d.buttonKeyID != "" && d.buttonKeyID != active && !d.desiredRetire &&
		(d.state == "pending" || d.state == "delivered" || (d.state == "not_delivered" && d.publishedAt != nil))
}

func (f *fakeDB) ListRerenderGroups(_ context.Context, arg dbgen.ListRerenderGroupsParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListRerenderGroups"); err != nil {
		return nil, err
	}
	var ids []int64
	for _, d := range f.deliveries {
		if d.group > arg.After && f.rerenderable(d, arg.ActiveKeyID) {
			ids = append(ids, d.group)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return ids[:min(len(ids), int(arg.Lim))], nil
}

func (f *fakeDB) LockRerenderGroup(_ context.Context, arg dbgen.LockRerenderGroupParams) (dbgen.LockRerenderGroupRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockRerenderGroup"); err != nil {
		return dbgen.LockRerenderGroupRow{}, err
	}
	g := f.groups[arg.ID]
	if g == nil || g.status == "resolved" {
		return dbgen.LockRerenderGroupRow{}, pgx.ErrNoRows
	}
	return dbgen.LockRerenderGroupRow{ID: g.id, PublicID: g.publicID, Number: g.number, Title: g.title,
		Status: g.status, Urgent: g.urgent, RouteID: g.route}, nil
}

func (f *fakeDB) LockRerenderDeliveries(_ context.Context, arg dbgen.LockRerenderDeliveriesParams) (
	[]dbgen.LockRerenderDeliveriesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockRerenderDeliveries"); err != nil {
		return nil, err
	}
	var out []dbgen.LockRerenderDeliveriesRow
	for _, d := range f.deliveries {
		if d.group != arg.AlertGroupID || !f.rerenderable(d, arg.ActiveKeyID) {
			continue
		}
		ds := f.dests[d.dest]
		out = append(out, dbgen.LockRerenderDeliveriesRow{ID: d.id, DesiredHash: d.hash,
			DesiredButtonKeyID: txt(&d.buttonKeyID), DestinationID: ds.id, DestinationPublicID: ds.publicID,
			DestinationName: ds.name, DestinationType: ds.typ, ConnectionID: nullInt(ds.connection),
			Buttons: ds.buttons, OrganizationButtons: f.orgButtons})
	}
	slices.SortFunc(out, func(a, b dbgen.LockRerenderDeliveriesRow) int {
		return int(a.DestinationID - b.DestinationID)
	})
	return out, nil
}

func (f *fakeDB) LockButtonKey(_ context.Context, arg dbgen.LockButtonKeyParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if arg.LockClass == 0 || arg.KeyID == "" {
		return errors.New("not a key lock")
	}
	return f.call("LockButtonKey")
}

func (f *fakeDB) ButtonKeyInUse(_ context.Context, arg dbgen.ButtonKeyInUseParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ButtonKeyInUse"); err != nil {
		return false, err
	}
	for _, d := range f.deliveries {
		g := f.groups[d.group]
		signed := (d.actualKey != nil && *d.actualKey == arg.KeyID) || slices.Contains(d.supersededKeys, arg.KeyID)
		if signed && g != nil && g.status != "resolved" &&
			!slices.Contains([]string{"deleted_in_messenger", "retired", "withheld"}, d.state) {
			return true, nil
		}
	}
	return false, nil
}

// keyRenderer is the stub renderer whose buttons the key *key signs: their action ids name it, so a new key changes
// what a Root message shows.
type keyRenderer struct {
	stubRenderer
	key *string
}

func (r keyRenderer) Roots(ctx context.Context, db messages.DBTX, g delivery.GroupView, markups []messages.Markup) (
	delivery.Roots, error) {
	out, err := r.stubRenderer.Roots(ctx, db, g, markups)
	if err != nil {
		return out, err
	}
	for mk, rd := range out.Messages {
		buttons := slices.Clone(rd.Message.Buttons)
		for i := range buttons {
			buttons[i].ActionID, buttons[i].KeyID = buttons[i].Command+"@"+*r.key, *r.key
		}
		rd.Message.Buttons, rd.KeyID = buttons, *r.key
		out.Messages[mk] = rd
	}
	return out, nil
}

// destMM2 is a second Mattermost Destination of Route 1.
const destMM2 = 13

// rotationEnv is the environment with buttons signed by *key, which a test changes to rotate: the Alert Group in both
// Mattermost Destinations and the outgoing webhook, published and delivered with key k-old.
func rotationEnv(t *testing.T) (*env, *string) {
	t.Helper()
	e := newEnv(t)
	key := "k-old"
	conn := int64(connID)
	e.db.dests[destMM2] = &fakeDest{id: destMM2, publicID: "DSAAAAAAAAAA13", name: "ops-2",
		typ: delivery.TypeMattermost, connection: &conn, health: "healthy", limit: 600, per: 60}
	e.db.dests[destMM].limit = 600
	e.db.routeDests[routeID] = []int64{destMM, destWH, destMM2}
	e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business,
		Log: e.w.Log, Renderer: keyRenderer{key: &key}})
	e.enqueue(t, e.group(groups.StatusFiring, "disk full"), groups.System, created())
	e.round(t)
	for _, d := range e.db.deliveries {
		if d.state != "delivered" {
			t.Fatalf("delivery to %d is %s", d.dest, d.state)
		}
	}
	e.rec.Reset()
	e.db.hints = nil
	return e, &key
}

// organizationHints counts the organization hints sent.
func (e *env) organizationHints() int {
	n := 0
	for _, h := range e.db.hints {
		if h.Type == livehub.HintOrganization {
			n++
		}
	}
	return n
}

// deliveryTo is the delivery of the Alert Group to the Destination dest.
func (e *env) deliveryTo(t *testing.T, dest int64) *fakeDelivery {
	t.Helper()
	for _, d := range e.db.deliveries {
		if d.dest == dest {
			return d
		}
	}
	t.Fatalf("no delivery to %d", dest)
	return nil
}

// TestRerenderAfterRotation covers C-20.FR-7 and AC-5 with the delivery worker: the re-render selects only the
// deliveries signed by another key, recomputes their Desired state with buttons the active key signs in the
// dispatcher's lock order, wakes the worker and sends the organization hint; the worker edits the Root messages and
// records the new key, and the outcome that replaces the last carrier of the old key sends the organization hint. An
// outgoing webhook carries no buttons and is never selected; a second run changes nothing.
func TestRerenderAfterRotation(t *testing.T) {
	e, key := rotationEnv(t)
	if k := e.deliveryTo(t, destMM).actualKey; k == nil || *k != "k-old" {
		t.Fatalf("actual key %v", k)
	}
	if e.deliveryTo(t, destWH).buttonKeyID != "" || e.deliveryTo(t, destWH).actualKey != nil {
		t.Error("the outgoing webhook carries a key")
	}
	*key = "k-new"
	var order []string
	for _, q := range []string{"LockRerenderGroup", "ShareRouteMembership", "LockRerenderDeliveries", "SetDesired"} {
		e.db.before[q] = func() { order = append(order, q) }
	}
	notified := e.db.notified
	n, err := e.svc.RerenderOpen(t.Context(), "k-new")
	if err != nil || n != 2 {
		t.Fatalf("re-rendered %d, %v", n, err)
	}
	if !slices.Equal(order, []string{"LockRerenderGroup", "ShareRouteMembership", "LockRerenderDeliveries",
		"SetDesired"}) {
		t.Errorf("lock order %v", order)
	}
	for _, dest := range []int64{destMM, destMM2} {
		if d := e.deliveryTo(t, dest); d.buttonKeyID != "k-new" || d.state != "pending" {
			t.Errorf("delivery to %d: key %s, %s", dest, d.buttonKeyID, d.state)
		}
	}
	if e.db.notified != notified+1 || e.organizationHints() != 1 {
		t.Errorf("wakes %d, organization hints %d", e.db.notified-notified, e.organizationHints())
	}

	e.round(t)
	calls := e.rec.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls %+v", calls)
	}
	for _, c := range calls {
		if c.Method != deliverytest.MethodUpdate || c.Message.Buttons[0].KeyID != "k-new" {
			t.Errorf("call %s with buttons %+v", c.Method, c.Message.Buttons)
		}
	}
	for _, dest := range []int64{destMM, destMM2} {
		if k := e.deliveryTo(t, dest).actualKey; k == nil || *k != "k-new" {
			t.Errorf("actual key of %d: %v", dest, k)
		}
	}
	// One hint of the batch, one of the outcome that replaced the last carrier of k-old; each outcome took the key's
	// lock before it looked.
	if e.organizationHints() != 2 || e.db.calls["LockButtonKey"] != 2 || e.db.calls["ButtonKeyInUse"] != 2 {
		t.Errorf("organization hints %d, key locks %d", e.organizationHints(), e.db.calls["LockButtonKey"])
	}

	again, err := e.svc.RerenderOpen(t.Context(), "k-new")
	if err != nil || again != 0 || e.organizationHints() != 2 {
		t.Errorf("second run %d, %v, hints %d", again, err, e.organizationHints())
	}
}

// TestRerenderLeavesEndedDeliveries: deleted, withheld, retired deliveries, final edits and Not delivered ones without
// a Root message keep their Desired state; one held by a Storm is re-rendered and stays held, so that the Storm's
// release publishes the active key's buttons; a Not delivered one with a Root message is edited again.
func TestRerenderLeavesEndedDeliveries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		set     func(d *fakeDelivery)
		changed bool
	}{
		{"deleted_in_messenger", func(d *fakeDelivery) { d.state = "deleted_in_messenger" }, false},
		{"withheld", func(d *fakeDelivery) { d.state = "withheld" }, false},
		{"retired", func(d *fakeDelivery) { d.state = "retired" }, false},
		{"final edit", func(d *fakeDelivery) { d.desiredRetire = true }, false},
		{"not delivered unpublished", func(d *fakeDelivery) { d.state, d.publishedAt = "not_delivered", nil }, false},
		{"not delivered published", func(d *fakeDelivery) { d.state = "not_delivered" }, true},
		{"held by a storm", func(d *fakeDelivery) { d.state, d.heldBy, d.publishedAt = "pending", 77, nil }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, key := rotationEnv(t)
			e.db.routeDests[routeID] = []int64{destMM}
			e.db.deliveries = slices.DeleteFunc(e.db.deliveries, func(d *fakeDelivery) bool { return d.dest != destMM })
			d := e.only(t)
			tc.set(d)
			state, held := d.state, d.heldBy
			*key = "k-new"
			n, err := e.svc.RerenderOpen(t.Context(), "k-new")
			if err != nil || (n == 1) != tc.changed {
				t.Fatalf("re-rendered %d, %v", n, err)
			}
			switch {
			case !tc.changed && (d.buttonKeyID != "k-old" || d.state != state):
				t.Errorf("key %s, state %s", d.buttonKeyID, d.state)
			case tc.changed && (d.buttonKeyID != "k-new" || d.state != "pending" || d.heldBy != held):
				t.Errorf("key %s, state %s, held by %d", d.buttonKeyID, d.state, d.heldBy)
			}
		})
	}
}

// TestRerenderSkipsLockedAndResolvedGroups: an Alert Group a Command holds is skipped and picked up by the next run;
// one that resolved since it was listed is left alone.
func TestRerenderSkipsLockedAndResolvedGroups(t *testing.T) {
	e, key := rotationEnv(t)
	*key = "k-new"
	e.db.fail["LockRerenderGroup"] = pgx.ErrNoRows
	if n, err := e.svc.RerenderOpen(t.Context(), "k-new"); err != nil || n != 0 || e.organizationHints() != 0 {
		t.Fatalf("re-rendered %d, %v while the alert group was held", n, err)
	}
	delete(e.db.fail, "LockRerenderGroup")
	e.db.before["LockRerenderGroup"] = func() { e.db.groups[groupID].status = "resolved" }
	if n, err := e.svc.RerenderOpen(t.Context(), "k-new"); err != nil || n != 0 {
		t.Fatalf("re-rendered %d, %v a resolved alert group", n, err)
	}
	e.db.groups[groupID].status = "firing"
	if n, err := e.svc.RerenderOpen(t.Context(), "k-new"); err != nil || n != 2 {
		t.Errorf("the next run re-rendered %d, %v", n, err)
	}
}

// TestRerenderBatches: Alert Groups are re-rendered in batches of delivery.RerenderBatch, in id order, with one
// organization hint per batch that changed a delivery.
func TestRerenderBatches(t *testing.T) {
	e, key := rotationEnv(t)
	manyGroups(t, e, delivery.RerenderBatch+1)
	*key = "k-new"
	n, err := e.svc.RerenderOpen(t.Context(), "k-new")
	if err != nil || n != delivery.RerenderBatch+1 || e.db.calls["ListRerenderGroups"] != 2 ||
		e.organizationHints() != 2 {
		t.Errorf("re-rendered %d, %v, %d lists, %d hints", n, err, e.db.calls["ListRerenderGroups"],
			e.organizationHints())
	}
}

// manyGroups replaces the Alert Groups of e with n open ones, each delivered to destMM with buttons the old key signed.
func manyGroups(t *testing.T, e *env, n int) []*fakeDelivery {
	t.Helper()
	tmpl := e.deliveryTo(t, destMM)
	e.db.deliveries = []*fakeDelivery{}
	for i := range int64(n) {
		id := 1000 + i
		e.db.groups[id] = &fakeGroup{id: id, publicID: "AGAAAAAAAAA" + string(rune('A'+i%26)) + "1", number: id,
			title: "disk", status: "firing", route: routeID, created: business0}
		d := *tmpl
		d.id, d.group = 5000+i, id
		e.db.deliveries = append(e.db.deliveries, &d)
	}
	return e.db.deliveries
}

// TestRerenderGoesOnPastAFailedGroup: an Alert Group that fails is left for the next run, the others are re-rendered
// and hinted, and its error is returned at the end; the next run re-renders it.
func TestRerenderGoesOnPastAFailedGroup(t *testing.T) {
	e, key := rotationEnv(t)
	ds := manyGroups(t, e, 3)
	*key = "k-new"
	boom := errors.New("boom")
	e.db.fail["SetDesired"] = boom
	e.db.before["LockRerenderGroup"] = func() {
		e.db.before["LockRerenderGroup"] = func() { delete(e.db.fail, "SetDesired") }
	}
	n, err := e.svc.RerenderOpen(t.Context(), "k-new")
	if !errors.Is(err, boom) || n != 2 || e.db.calls["LockRerenderGroup"] != 3 || e.organizationHints() != 1 {
		t.Fatalf("re-rendered %d, %v, %d groups locked, %d hints", n, err, e.db.calls["LockRerenderGroup"],
			e.organizationHints())
	}
	if ds[0].buttonKeyID != "k-old" || ds[1].buttonKeyID != "k-new" || ds[2].buttonKeyID != "k-new" {
		t.Errorf("keys %s, %s, %s", ds[0].buttonKeyID, ds[1].buttonKeyID, ds[2].buttonKeyID)
	}
	if n, err := e.svc.RerenderOpen(t.Context(), "k-new"); err != nil || n != 1 || ds[0].buttonKeyID != "k-new" {
		t.Errorf("the next run re-rendered %d, %v", n, err)
	}
}

// TestRerenderStops: a renderer that signs with another key than the active one stops the run at the first Alert
// Group with ErrButtonKeyNotActive, and so does an ended context; neither sends a hint.
func TestRerenderStops(t *testing.T) {
	t.Run("another key", func(t *testing.T) {
		e, key := rotationEnv(t)
		manyGroups(t, e, 3)
		*key = "k-stale"
		n, err := e.svc.RerenderOpen(t.Context(), "k-new")
		if !errors.Is(err, delivery.ErrButtonKeyNotActive) || n != 0 || e.db.calls["LockRerenderGroup"] != 1 ||
			e.organizationHints() != 0 {
			t.Errorf("re-rendered %d, %v, %d groups locked", n, err, e.db.calls["LockRerenderGroup"])
		}
	})
	t.Run("ended context", func(t *testing.T) {
		e, key := rotationEnv(t)
		manyGroups(t, e, 3)
		*key = "k-new"
		ctx, cancel := context.WithCancel(t.Context())
		e.db.fail["LockRerenderGroup"] = context.Canceled
		e.db.before["LockRerenderGroup"] = cancel
		n, err := e.svc.RerenderOpen(ctx, "k-new")
		if !errors.Is(err, context.Canceled) || n != 0 || e.db.calls["LockRerenderGroup"] != 1 {
			t.Errorf("re-rendered %d, %v, %d groups locked", n, err, e.db.calls["LockRerenderGroup"])
		}
	})
	t.Run("a failed hint keeps the errors", func(t *testing.T) {
		e, key := rotationEnv(t)
		manyGroups(t, e, 2)
		*key = "k-new"
		boom, hint := errors.New("boom"), errors.New("hint")
		e.db.fail["SetDesired"] = boom
		e.db.before["LockRerenderGroup"] = func() {
			e.db.before["LockRerenderGroup"] = func() { delete(e.db.fail, "SetDesired") }
		}
		e.db.fail["Notify"] = hint
		if n, err := e.svc.RerenderOpen(t.Context(), "k-new"); !errors.Is(err, boom) || !errors.Is(err, hint) || n != 1 {
			t.Errorf("re-rendered %d, %v", n, err)
		}
	})
}

// TestRerenderErrors: every failed step is an error, the renderer signing with another key than the active one
// among them, and nothing is re-rendered without an active key.
func TestRerenderErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := newEnv(t).svc.RerenderOpen(t.Context(), ""); err == nil {
		t.Error("no error without an active key")
	}
	for _, q := range []string{"ListRerenderGroups", "LockRerenderGroup", "ShareRouteMembership",
		"LockRerenderDeliveries", "SetDesired", "NotifyDelivery", "Notify"} {
		t.Run(q, func(t *testing.T) {
			e, key := rotationEnv(t)
			*key = "k-new"
			e.db.fail[q] = boom
			if _, err := e.svc.RerenderOpen(t.Context(), "k-new"); !errors.Is(err, boom) {
				t.Errorf("err %v", err)
			}
		})
	}
	t.Run("render", func(t *testing.T) {
		e, key := rotationEnv(t)
		*key = "k-new"
		e.svc = delivery.New(delivery.Config{OrgID: orgID, Store: e.store, Business: e.business, Log: e.w.Log,
			Renderer: keyRenderer{stubRenderer: stubRenderer{err: boom}, key: key}})
		if _, err := e.svc.RerenderOpen(t.Context(), "k-new"); !errors.Is(err, boom) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("another key", func(t *testing.T) {
		e, key := rotationEnv(t)
		*key = "k-stale"
		if _, err := e.svc.RerenderOpen(t.Context(), "k-new"); !errors.Is(err, delivery.ErrButtonKeyNotActive) ||
			e.deliveryTo(t, destMM).buttonKeyID != "k-old" {
			t.Errorf("err %v", err)
		}
	})
}

// TestLastResignedMessage: the outcome that replaces a key's last carrier among the open Root messages sends the
// organization hint, also when the final edit retires it; a key still carried elsewhere sends none, unless only by a
// withheld or retired delivery, which the re-render never re-signs; a failed lock or look is the outcome's error.
func TestLastResignedMessage(t *testing.T) {
	t.Run("final edit", func(t *testing.T) {
		e, _ := rotationEnv(t)
		e.db.routeDests[routeID] = []int64{destWH, destMM2}
		e.deliveryTo(t, destMM).desiredRetire, e.deliveryTo(t, destMM).state = true, "pending"
		e.round(t)
		if d := e.deliveryTo(t, destMM); d.state != "retired" || d.actualKey != nil || e.organizationHints() != 0 {
			t.Errorf("state %s, key %v, hints %d while ops-2 still carries k-old", d.state, d.actualKey,
				e.organizationHints())
		}
		e.deliveryTo(t, destMM2).desiredRetire, e.deliveryTo(t, destMM2).state = true, "pending"
		e.round(t)
		if e.organizationHints() != 1 {
			t.Errorf("hints %d after the last final edit", e.organizationHints())
		}
	})
	for _, state := range []string{"withheld", "retired"} {
		t.Run("the other carrier "+state, func(t *testing.T) {
			e, _ := rotationEnv(t)
			e.db.routeDests[routeID] = []int64{destWH, destMM2}
			e.deliveryTo(t, destMM2).state = state
			e.deliveryTo(t, destMM).desiredRetire, e.deliveryTo(t, destMM).state = true, "pending"
			e.round(t)
			if d := e.deliveryTo(t, destMM2); d.actualKey == nil || *d.actualKey != "k-old" ||
				e.organizationHints() != 1 {
				t.Errorf("key %v, hints %d while only a %s delivery carries k-old", d.actualKey,
					e.organizationHints(), state)
			}
		})
	}
	for _, q := range []string{"LockButtonKey", "ButtonKeyInUse"} {
		t.Run(q, func(t *testing.T) {
			e, key := rotationEnv(t)
			*key = "k-new"
			if _, err := e.svc.RerenderOpen(t.Context(), "k-new"); err != nil {
				t.Fatal(err)
			}
			e.db.fail[q] = errors.New("boom")
			e.round(t)
			if !strings.Contains(e.log.String(), `"event":"delivery_work_failed"`) || e.organizationHints() != 1 {
				t.Errorf("hints %d, log %s", e.organizationHints(), e.log.String())
			}
		})
	}
}
