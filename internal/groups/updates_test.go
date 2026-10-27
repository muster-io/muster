// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
)

// fakeUpdate is an open_group_updates row.
type fakeUpdate struct {
	id, routeID, after int64
	destinationID      int64
	kind               string
	requested          time.Time
	leasedBy           string
	leaseUntil         *time.Time
}

// destinationPublicID is the public_id of the Destination id in the fake.
func destinationPublicID(id int64) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("DS%012d", id)
}

func (f *fakeDB) ClaimOpenGroupUpdate(_ context.Context, arg dbgen.ClaimOpenGroupUpdateParams) (
	[]dbgen.ClaimOpenGroupUpdateRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ClaimOpenGroupUpdate"); err != nil {
		return nil, err
	}
	slices.SortFunc(f.updates, func(a, b *fakeUpdate) int {
		return cmp.Or(a.requested.Compare(b.requested), cmp.Compare(a.id, b.id))
	})
	for _, u := range f.updates {
		if arg.OrgID != orgID || !slices.Contains(arg.Kinds, u.kind) ||
			(u.leaseUntil != nil && u.leaseUntil.After(arg.Now)) {
			continue
		}
		until := arg.LeaseUntil
		u.leasedBy, u.leaseUntil = arg.Owner, &until
		route := ""
		if r := f.routes[u.routeID]; r != nil {
			route = r.PublicID
		}
		return []dbgen.ClaimOpenGroupUpdateRow{{ID: u.id, Kind: u.kind, RouteID: pgtype.Int8{Int64: u.routeID,
			Valid: u.routeID != 0}, RequestedAt: u.requested, AfterAlertGroupID: u.after, RoutePublicID: route,
			DestinationID:       pgtype.Int8{Int64: u.destinationID, Valid: u.destinationID != 0},
			DestinationPublicID: destinationPublicID(u.destinationID)}}, nil
	}
	return nil, nil
}

func (f *fakeDB) ListFiringLevels(_ context.Context, arg dbgen.ListFiringLevelsParams) ([]dbgen.ListFiringLevelsRow,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListFiringLevels"); err != nil {
		return nil, err
	}
	levels := map[int64]organization.SeverityLevel{}
	for _, m := range f.members {
		if m.state == "firing" && slices.Contains(arg.AlertGroupIds, m.group) && arg.OrgID == orgID {
			levels[m.group] = higher(cmp.Or(levels[m.group], organization.SeverityInfo),
				severityOf(f.alerts[m.alert].SeverityLevel))
		}
	}
	out := make([]dbgen.ListFiringLevelsRow, 0, len(levels))
	for _, id := range slices.Sorted(maps.Keys(levels)) {
		out = append(out, dbgen.ListFiringLevelsRow{AlertGroupID: id, SeverityLevel: string(levels[id])})
	}
	return out, nil
}

// TestUpdateNames: routing and organization, which insert the rows and wake the workers, and groups, which claims them,
// name the kinds and the channel alike; destinations checks its own names against groups (write_test.go).
func TestUpdateNames(t *testing.T) {
	if routing.OpenGroupUpdatesChannel != OpenGroupUpdatesChannel || routing.KindMinimumSeverity != KindRouteMinimumSeverity {
		t.Error("routing and groups name the open alert group updates differently")
	}
	if organization.OpenGroupUpdatesChannel != OpenGroupUpdatesChannel ||
		organization.KindOrganizationButtons != KindOrganizationButtons {
		t.Error("organization and groups name the open alert group updates differently")
	}
}

func (f *fakeDB) update(id int64) *fakeUpdate {
	for _, u := range f.updates {
		if u.id == id {
			return u
		}
	}
	return nil
}

func (f *fakeDB) LockOpenGroupUpdate(_ context.Context, arg dbgen.LockOpenGroupUpdateParams) (
	dbgen.LockOpenGroupUpdateRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockOpenGroupUpdate"); err != nil {
		return dbgen.LockOpenGroupUpdateRow{}, err
	}
	u := f.update(arg.ID)
	if u == nil || arg.OrgID != orgID || u.leasedBy != arg.Owner || u.leaseUntil == nil ||
		!u.leaseUntil.After(arg.Now) {
		return dbgen.LockOpenGroupUpdateRow{}, pgx.ErrNoRows
	}
	return dbgen.LockOpenGroupUpdateRow{ID: u.id, Kind: u.kind, RouteID: pgtype.Int8{Int64: u.routeID,
		Valid: u.routeID != 0}, DestinationID: pgtype.Int8{Int64: u.destinationID, Valid: u.destinationID != 0},
		AfterAlertGroupID: u.after}, nil
}

func (f *fakeDB) AdvanceOpenGroupUpdate(_ context.Context, arg dbgen.AdvanceOpenGroupUpdateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("AdvanceOpenGroupUpdate"); err != nil {
		return err
	}
	if u := f.update(arg.ID); u != nil && arg.OrgID == orgID {
		until := arg.LeaseUntil
		u.after, u.leaseUntil = arg.AfterAlertGroupID, &until
	}
	return nil
}

func (f *fakeDB) DeleteOpenGroupUpdate(_ context.Context, arg dbgen.DeleteOpenGroupUpdateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("DeleteOpenGroupUpdate"); err != nil {
		return err
	}
	f.updates = slices.DeleteFunc(f.updates, func(u *fakeUpdate) bool { return u.id == arg.ID && arg.OrgID == orgID })
	return nil
}

func (f *fakeDB) NextOpenGroupUpdate(_ context.Context, arg dbgen.NextOpenGroupUpdateParams) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("NextOpenGroupUpdate"); err != nil {
		return time.Time{}, err
	}
	var next time.Time
	for _, u := range f.updates {
		if !slices.Contains(arg.Kinds, u.kind) {
			continue
		}
		at := arg.Now
		if u.leaseUntil != nil && u.leaseUntil.After(arg.Now) {
			at = *u.leaseUntil
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	if next.IsZero() {
		return time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), nil
	}
	return next, nil
}

func (f *fakeDB) ListOpenGroupsOfRouteAfter(_ context.Context, arg dbgen.ListOpenGroupsOfRouteAfterParams) (
	[]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListOpenGroupsOfRouteAfter"); err != nil {
		return nil, err
	}
	var out []int64
	for _, g := range f.sortedGroups() {
		if g.RouteID == arg.RouteID && g.Status != "resolved" && g.ID > arg.AfterID && arg.OrgID == orgID {
			out = append(out, g.ID)
		}
	}
	return out[:min(len(out), int(arg.Lim))], nil
}

func (f *fakeDB) ListOpenGroupsOfDestinationAfter(_ context.Context,
	arg dbgen.ListOpenGroupsOfDestinationAfterParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListOpenGroupsOfDestinationAfter"); err != nil {
		return nil, err
	}
	return f.openDelivered(arg.OrgID, arg.AfterID, arg.Lim, func(dest int64) bool { return dest == arg.DestinationID }),
		nil
}

func (f *fakeDB) ListOpenGroupsFollowingOrganizationAfter(_ context.Context,
	arg dbgen.ListOpenGroupsFollowingOrganizationAfterParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListOpenGroupsFollowingOrganizationAfter"); err != nil {
		return nil, err
	}
	return f.openDelivered(arg.OrgID, arg.AfterID, arg.Lim, func(dest int64) bool {
		return slices.Contains(f.following, dest)
	}), nil
}

// openDelivered are the open Alert Groups after the id after with a delivery that is not being retired in a
// Destination that matches, by id, at most lim.
func (f *fakeDB) openDelivered(org, after int64, lim int32, match func(dest int64) bool) []int64 {
	var out []int64
	for _, g := range f.sortedGroups() {
		if g.Status != "resolved" && g.ID > after && org == orgID && slices.ContainsFunc(f.delivered[g.ID],
			func(dest int64) bool { return match(dest) && !f.retired[[2]int64{g.ID, dest}] }) {
			out = append(out, g.ID)
		}
	}
	return out[:min(len(out), int(lim))]
}

// updates is the harness of the open Alert Group updates: Alert Groups of the Route db (2) — acknowledged, snoozed
// and firing, all warning — one of the Route net (3), and a worker over them at a manual real clock.
type updates struct {
	*harness
	real              *clock.Manual
	acked, snoozed    *dbgen.LockGroupsRow
	firing, elsewhere *dbgen.LockGroupsRow
	worker            *UpdateWorker
}

func newUpdates(t *testing.T) *updates {
	t.Helper()
	h := newHarness(t)
	u := &updates{harness: h, real: clock.NewManual(t0.Add(-24 * time.Hour))}
	open := func(route int64, cluster string) *dbgen.LockGroupsRow {
		a := h.alert(route, "warning", map[string]string{"alertname": "A", "cluster": cluster})
		h.changes(t, ingest.ChangeFired, a)
		return h.groupOf(t, a)
	}
	u.acked, u.snoozed, u.firing, u.elsewhere = open(2, "a"), open(2, "b"), open(2, "c"), open(3, "d")
	h.acknowledge(u.acked)
	h.snooze(u.snoozed, t0.Add(time.Hour), false)
	u.worker = u.workerOf("replica-a")
	return u
}

func (u *updates) workerOf(owner string) *UpdateWorker {
	return &UpdateWorker{Service: u.svc, Log: u.svc.log, Lease: db.Lease{Owner: owner, Duration: UpdateLease,
		Clocks: clock.Clocks{Business: u.clock, Real: u.real}}}
}

// request sets the Minimum severity of the Route db and inserts its row, as routing does.
func (u *updates) request(minimum string) {
	u.db.routes[2].MinimumSeverity = minimum
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), routeID: 2, kind: KindRouteMinimumSeverity,
		requested: u.clock.Now()})
}

// round runs a round of the worker and fails on its error.
func (u *updates) round(t *testing.T, w *UpdateWorker) time.Duration {
	t.Helper()
	next, err := w.Round(t.Context())
	if err != nil {
		t.Fatalf("round: %v", err)
	}
	return next
}

// TestMinimumSeverityUpdate is C-26.FR-9, C-26.AC-11 and C-09.AC-8: raising the Minimum severity of a Route to
// critical records on each of its open warning Alert Groups the Quiet urgency_raised that removes nothing, with the
// reason of the Minimum severity, keeping the acknowledgement, the Snooze and the Owner, and leaves the Alert Groups of
// other Routes alone; the row is deleted and open_group_update_done logged; a second run changes nothing; raising it to
// warning records the Quiet severity_raised on info Alert Groups; lowering it records nothing and lowers the level.
func TestMinimumSeverityUpdate(t *testing.T) {
	u := newUpdates(t)
	u.clock.Advance(time.Minute)
	u.request("critical")
	before := len(u.entriesOf(u.elsewhere))
	if next := u.round(t, u.worker); next != UpdateMaxWait || len(u.db.updates) != 0 {
		t.Errorf("next %v, rows %d", next, len(u.db.updates))
	}
	for name, g := range map[string]*dbgen.LockGroupsRow{"acknowledged": u.acked, "snoozed": u.snoozed,
		"firing": u.firing} {
		e := u.last(t, g)
		if g.SeverityLevel != "critical" || !g.Urgent || e.Event.String != "urgency_raised" ||
			e.Loudness.String != "quiet" || len(e.Mentions) != 0 || e.Reason.String != ReasonMinimumSeverity ||
			e.ActorKind != "system" || e.Transport != "system" || e.FromStatus.String != g.Status ||
			e.ToStatus.String != g.Status || !e.At.Equal(u.clock.Now()) {
			t.Errorf("%s: %+v %+v", name, g, e.InsertTimelineEntryParams)
		}
	}
	if u.acked.Status != "acknowledged" || u.acked.OwnerUserID.Int64 != owner || u.snoozed.Status != "snoozed" ||
		!u.snoozed.SnoozeUntil.Time.Equal(t0.Add(time.Hour)) || u.firing.Status != "firing" {
		t.Errorf("statuses %s %s %s", u.acked.Status, u.snoozed.Status, u.firing.Status)
	}
	if u.elsewhere.SeverityLevel != "warning" || len(u.entriesOf(u.elsewhere)) != before {
		t.Errorf("another route's alert group changed: %+v", u.elsewhere)
	}
	if !strings.Contains(u.log.String(), `"event":"open_group_update_done","kind":"route_minimum_severity",`+
		`"route":"RTDBAAAAAAAAAA","alert_groups":3`) {
		t.Errorf("log %s", u.log)
	}
	// A second run of the same change records nothing.
	entries := len(u.db.entries)
	u.request("critical")
	u.round(t, u.worker)
	if len(u.db.entries) != entries || !strings.Contains(u.log.String(), `"alert_groups":0`) {
		t.Errorf("a second run recorded %d entries", len(u.db.entries)-entries)
	}
	// Lowered to warning: the level falls silently, as a falling Severity level does.
	u.request("warning")
	u.round(t, u.worker)
	if len(u.db.entries) != entries || u.acked.SeverityLevel != "warning" || u.acked.Urgent ||
		u.acked.Status != "acknowledged" || u.firing.SeverityLevel != "warning" {
		t.Errorf("lowered: %d entries, %+v", len(u.db.entries)-entries, u.acked)
	}
	// Raised to warning over an info Alert Group: the Quiet severity_raised with the reason.
	info := u.alert(2, "info", map[string]string{"alertname": "A", "cluster": "e"})
	u.db.routes[2].MinimumSeverity = "none"
	u.changes(t, ingest.ChangeFired, info)
	g := u.groupOf(t, info)
	u.request("warning")
	u.round(t, u.worker)
	if e := u.last(t, g); g.SeverityLevel != "warning" || g.Urgent || e.Event.String != "severity_raised" ||
		e.Loudness.String != "quiet" || e.Reason.String != ReasonMinimumSeverity {
		t.Errorf("raised to warning: %+v %+v", g, e.InsertTimelineEntryParams)
	}
	// An Alert Group whose urgency a 0.1 replica wrote apart from its level is corrected silently.
	u.firing.Urgent = true
	entries = len(u.db.entries)
	u.request("warning")
	u.round(t, u.worker)
	if u.firing.Urgent || len(u.db.entries) != entries {
		t.Errorf("urgency corrected: %+v, %d entries", u.firing, len(u.db.entries)-entries)
	}
}

// TestUpdateBatches: a row walks its Alert Groups in batches by id, each recording how far it got and renewing the
// lease, and the batch that comes up short deletes it; an Alert Group resolved or moved after the batch listed it is
// left alone.
func TestUpdateBatches(t *testing.T) {
	u := newUpdates(t)
	u.worker.Batch = 2
	u.request("critical")
	u.db.before["LockGroups"] = func() {
		u.db.before["LockGroups"] = func() { u.firing.RouteID = 3 }
		u.snoozed.Status, u.snoozed.SnoozeUntil, u.snoozed.SnoozedByUserID = "resolved", pgtype.Timestamptz{},
			pgtype.Int8{}
		u.snoozed.ResolvedAt = ts(t0)
	}
	u.round(t, u.worker)
	if u.db.calls["AdvanceOpenGroupUpdate"] != 1 || u.db.calls["DeleteOpenGroupUpdate"] != 1 ||
		u.db.calls["ListOpenGroupsOfRouteAfter"] != 2 || len(u.db.updates) != 0 {
		t.Errorf("calls %v", u.db.calls)
	}
	if u.acked.SeverityLevel != "critical" || u.snoozed.SeverityLevel != "warning" ||
		u.firing.SeverityLevel != "warning" || !strings.Contains(u.log.String(), `"alert_groups":1`) {
		t.Errorf("levels %s %s %s, log %s", u.acked.SeverityLevel, u.snoozed.SeverityLevel, u.firing.SeverityLevel,
			u.log)
	}
}

// TestUpdateClaims is ADR-0007 for the open Alert Group updates: two workers each claim a row once — the second finds
// it leased while the first holds it — and a worker whose lease ran out stops while any replica takes the row over
// and finishes it.
func TestUpdateClaims(t *testing.T) {
	u := newUpdates(t)
	b := u.workerOf("replica-b")
	u.request("critical")
	claimed, ok, err := u.svc.claimUpdate(t.Context(), u.worker.Lease)
	if err != nil || !ok || claimed.routePID != "RTDBAAAAAAAAAA" || claimed.kind != KindRouteMinimumSeverity {
		t.Fatalf("claim = %+v %v %v", claimed, ok, err)
	}
	if next := u.round(t, b); next != UpdateMaxWait || u.acked.SeverityLevel != "warning" ||
		u.db.calls["ClaimOpenGroupUpdate"] != 2 {
		t.Errorf("the second worker: next %v, level %s", next, u.acked.SeverityLevel)
	}
	u.worker.run(t.Context(), claimed)
	if len(u.db.updates) != 0 || u.acked.SeverityLevel != "critical" {
		t.Errorf("the first worker: rows %d, level %s", len(u.db.updates), u.acked.SeverityLevel)
	}
	if next := u.round(t, b); next != UpdateMaxWait {
		t.Errorf("nothing left: next %v", next)
	}
	// A lost lease: the first worker claimed the row, its lease ran out, the second took it over and finished it.
	u.request("warning")
	claimed, _, _ = u.svc.claimUpdate(t.Context(), u.worker.Lease)
	u.real.Advance(UpdateLease)
	u.round(t, b)
	entries := len(u.db.entries)
	u.request("critical")
	u.db.updates[0].leasedBy, u.db.updates[0].leaseUntil = "replica-b", new(u.real.Now().Add(time.Minute))
	claimed.id = u.db.updates[0].id
	u.worker.run(t.Context(), claimed)
	if u.acked.SeverityLevel != "warning" || len(u.db.entries) != entries || len(u.db.updates) != 1 {
		t.Errorf("a lost lease changed %+v", u.acked)
	}
}

// TestUpdateFailures: a failing claim or read fails the round; a failing batch logs open_group_update_failed with the
// row's scope and leaves the row leased for any replica once the lease runs out, as does a kind without a handler.
func TestUpdateFailures(t *testing.T) {
	for _, q := range []string{"ClaimOpenGroupUpdate", "NextOpenGroupUpdate"} {
		u := newUpdates(t)
		u.request("critical")
		u.db.fail[q] = errBoom
		if _, err := u.worker.Round(t.Context()); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s failing = %v", q, err)
		}
	}
	for _, q := range []string{"LockOpenGroupUpdate", "ListOpenGroupsOfRouteAfter", "GetRoutePolicy", "LockGroups",
		"ListFiringLevels", "SaveGroup", "AdvanceOpenGroupUpdate", "DeleteOpenGroupUpdate"} {
		u := newUpdates(t)
		u.worker.Batch = 3
		u.request("critical")
		u.db.fail[q] = errBoom
		if next := u.round(t, u.worker); next != UpdateMaxWait || len(u.db.updates) != 1 ||
			!strings.Contains(u.log.String(), `"event":"open_group_update_failed","kind":"route_minimum_severity",`+
				`"route":"RTDBAAAAAAAAAA","error":`) {
			t.Errorf("%s failing: next %v, log %s", q, next, u.log)
		}
	}
	// A row of a kind this version does not handle is neither claimed nor waited for; one leased all the same, as a
	// replica of a later version would, fails without changing anything.
	u := newUpdates(t)
	u.db.updates = append(u.db.updates, &fakeUpdate{id: 1, kind: "button_set", requested: t0})
	if next := u.round(t, u.worker); next != UpdateMaxWait || u.db.updates[0].leaseUntil != nil ||
		strings.Contains(u.log.String(), "open_group_update") {
		t.Errorf("an unknown kind: next %v, row %+v, log %s", next, u.db.updates[0], u.log)
	}
	until := u.real.Now().Add(time.Minute)
	u.db.updates[0].leasedBy, u.db.updates[0].leaseUntil = "replica-a", &until
	u.worker.run(t.Context(), update{id: 1, kind: "button_set"})
	if !strings.Contains(u.log.String(), `"event":"open_group_update_failed","kind":"button_set",`+
		`"error":"no handler for the kind \"button_set\""`) || len(u.db.updates) != 1 {
		t.Errorf("an unknown kind leased: %s", u.log)
	}
	// A canceled run logs nothing.
	u = newUpdates(t)
	u.request("critical")
	ctx, cancel := context.WithCancel(t.Context())
	u.db.fail["LockOpenGroupUpdate"] = errBoom
	cancel()
	if _, err := u.worker.Round(ctx); err != nil || strings.Contains(u.log.String(), "open_group_update_failed") {
		t.Errorf("canceled: %v, log %s", err, u.log)
	}
}

// TestButtonsUpdate is C-28.FR-9 on the worker: a row destination_buttons runs the dispatcher's re-render step,
// without a lifecycle event, on each open Alert Group with a delivery in its Destination that is not being retired —
// whatever its Route — and a row organization_buttons on each with such a delivery in a Destination that follows the
// Organization. Neither saves an Alert Group, records a Timeline entry or sends a live-update hint; a Route template
// that failed is recorded as the dispatcher records it. Both are deleted with their last batch and logged as
// open_group_update_done with their scope and the number of Alert Groups whose Desired state changed, so a second run,
// which changes no Desired state, counts none. An Alert Group resolved after the batch listed it is left alone, and the
// batches walk by id.
func TestButtonsUpdate(t *testing.T) {
	u := newUpdates(t)
	var rendered []int64
	changes := map[int64]bool{}
	u.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
		if len(r.Events) != 0 || r.Actor != System || r.Changed == nil || r.Fallback == nil || r.After == nil {
			t.Errorf("a re-render with events %+v as %+v", r.Events, r.Actor)
		}
		rendered = append(rendered, r.Group.ID)
		if changes[r.Group.ID] {
			r.Changed()
			r.Changed()
		}
		return nil
	})
	u.db.delivered = map[int64][]int64{u.acked.ID: {21}, u.firing.ID: {21, 22}, u.snoozed.ID: {22},
		u.elsewhere.ID: {21, 23}}
	u.db.retired = map[[2]int64]bool{{u.elsewhere.ID, 21}: true}
	u.db.following = []int64{22}
	changes[u.acked.ID], changes[u.firing.ID] = true, true
	before := []dbgen.LockGroupsRow{*u.acked, *u.snoozed, *u.firing, *u.elsewhere}
	entries, saves, hints := len(u.db.entries), u.db.calls["SaveGroup"], len(u.db.hints)
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), kind: KindDestinationButtons, destinationID: 21,
		requested: u.clock.Now()})
	u.round(t, u.worker)
	if want := []int64{u.acked.ID, u.firing.ID}; !slices.Equal(rendered, want) {
		t.Errorf("rendered %v, want %v", rendered, want)
	}
	if len(u.db.updates) != 0 || len(u.db.entries) != entries || u.db.calls["SaveGroup"] != saves ||
		len(u.db.hints) != hints || !strings.Contains(u.log.String(),
		`"event":"open_group_update_done","kind":"destination_buttons","destination":"DS000000000021",`+
			`"alert_groups":2`) {
		t.Errorf("rows %d, entries %d, saves %d, hints %d, log %s", len(u.db.updates), len(u.db.entries)-entries,
			u.db.calls["SaveGroup"]-saves, len(u.db.hints)-hints, u.log)
	}
	for i, g := range []*dbgen.LockGroupsRow{u.acked, u.snoozed, u.firing, u.elsewhere} {
		if g.Status != before[i].Status || g.SeverityLevel != before[i].SeverityLevel ||
			g.LastChangedAt != before[i].LastChangedAt || g.OwnerUserID != before[i].OwnerUserID {
			t.Errorf("alert group %d changed: %+v", g.ID, g)
		}
	}

	// A second run changes no Desired state and counts none.
	rendered, changes = nil, map[int64]bool{}
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), kind: KindDestinationButtons, destinationID: 21,
		requested: u.clock.Now()})
	u.round(t, u.worker)
	if !slices.Equal(rendered, []int64{u.acked.ID, u.firing.ID}) || !strings.Contains(u.log.String(),
		`"kind":"destination_buttons","destination":"DS000000000021","alert_groups":0`) {
		t.Errorf("a second run: rendered %v, log %s", rendered, u.log)
	}

	rendered = nil
	changes[u.snoozed.ID] = true
	u.worker.Batch = 1
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), kind: KindOrganizationButtons,
		requested: u.clock.Now()})
	u.db.before["LockGroups"] = func() {
		u.db.before["LockGroups"] = nil
		u.firing.Status, u.firing.ResolvedAt = "resolved", ts(t0)
	}
	u.round(t, u.worker)
	if want := []int64{u.snoozed.ID}; !slices.Equal(rendered, want) || len(u.db.updates) != 0 ||
		!strings.Contains(u.log.String(), `"event":"open_group_update_done","kind":"organization_buttons",`+
			`"alert_groups":1`) || u.db.calls["ListOpenGroupsFollowingOrganizationAfter"] != 2 {
		t.Errorf("the organization's set: rendered %v, log %s, calls %v", rendered, u.log, u.db.calls)
	}

	// A Route template that fails is recorded as the dispatcher records it.
	u.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
		r.Fallback(TemplateFailure{Template: "root_message", Detail: "root_message template failed: boom"})
		return nil
	})
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), kind: KindOrganizationButtons,
		requested: u.clock.Now()})
	entries = len(u.db.entries)
	u.round(t, u.worker)
	if e := u.last(t, u.snoozed); len(u.db.entries) != entries+1 || e.SystemEvent.String != "fallback_template_used" {
		t.Errorf("the failed template: %d entries, %+v", len(u.db.entries)-entries, e.InsertTimelineEntryParams)
	}

	for kind, q := range map[string]string{KindDestinationButtons: "ListOpenGroupsOfDestinationAfter",
		KindOrganizationButtons: "ListOpenGroupsFollowingOrganizationAfter"} {
		for _, failing := range []string{q, "LockGroups", "rerender", "InsertTimelineEntry"} {
			u := newUpdates(t)
			u.db.delivered, u.db.following = map[int64][]int64{u.acked.ID: {21}}, []int64{21}
			u.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
				if failing == "rerender" {
					return errBoom
				}
				r.Fallback(TemplateFailure{Template: "root_message", Detail: "failed"})
				return nil
			})
			u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), kind: kind, destinationID: 21,
				requested: u.clock.Now()})
			u.db.fail[failing] = errBoom
			if u.round(t, u.worker); len(u.db.updates) != 1 || !strings.Contains(u.log.String(),
				`"event":"open_group_update_failed","kind":"`+kind+`","destination":"DS000000000021","error":`) {
				t.Errorf("%s with %s failing: log %s", kind, failing, u.log)
			}
		}
	}
	// Without a re-render step, as in some tests, nothing is rendered and the row is done.
	u = newUpdates(t)
	u.db.delivered = map[int64][]int64{u.acked.ID: {21}}
	u.svc.d.rerender = nil
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), kind: KindDestinationButtons, destinationID: 21,
		requested: u.clock.Now()})
	if u.round(t, u.worker); len(u.db.updates) != 0 {
		t.Errorf("without a re-render step: %d rows", len(u.db.updates))
	}
}

// TestUpdateWorkerRun: Run runs a round at once, waits as long as the round says, between the minimum and MaxWait,
// backs off after a failed round, and stops with its context; Wake wakes it without blocking.
func TestUpdateWorkerRun(t *testing.T) {
	u := newUpdates(t)
	u.request("critical")
	ctx, cancel := context.WithCancel(t.Context())
	var waits []time.Duration
	u.worker.MaxWait = time.Hour
	u.worker.Wait = func(_ context.Context, d time.Duration, _ <-chan struct{}) {
		waits = append(waits, d)
		switch len(waits) {
		case 1:
			u.db.fail["ClaimOpenGroupUpdate"] = errBoom
		case 2:
			cancel()
		}
	}
	u.worker.Run(ctx)
	if len(waits) != 2 || waits[0] != time.Hour || waits[1] < updateFailureBackoff/2 ||
		waits[1] > updateFailureBackoff || u.acked.SeverityLevel != "critical" {
		t.Errorf("waits %v", waits)
	}
	u.worker.Wake()
	u.worker.Wake()
	if len(u.worker.wake) != 1 {
		t.Errorf("wakes queued %d", len(u.worker.wake))
	}
	if (&UpdateWorker{}).maxWait() != UpdateMaxWait || (&UpdateWorker{}).batch() != UpdateBatch {
		t.Error("defaults")
	}
	// The real wait ends with a wake, with the context, or after d.
	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	waitReal(t.Context(), time.Hour, wake)
	waitReal(ctx, time.Hour, nil)
	waitReal(t.Context(), time.Millisecond, nil)
}
