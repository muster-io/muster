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
	"github.com/muster-io/muster/internal/live"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/teams"
	teamsdb "github.com/muster-io/muster/internal/teams/dbgen"
)

// fakeUpdate is an open_group_updates row.
type fakeUpdate struct {
	id, routeID, after int64
	destinationID      int64
	teamID             int64
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
			DestinationPublicID: destinationPublicID(u.destinationID),
			TeamID:              pgtype.Int8{Int64: u.teamID, Valid: u.teamID != 0},
			TeamPublicID:        f.teamsOf().refs[u.teamID].PublicID}}, nil
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

// TestUpdateNames: routing, organization and teams, which insert the rows and wake the workers, and groups, which
// claims them, name the kinds and the channel alike; destinations checks its own names against groups
// (write_test.go).
func TestUpdateNames(t *testing.T) {
	if routing.OpenGroupUpdatesChannel != OpenGroupUpdatesChannel || routing.KindMinimumSeverity != KindRouteMinimumSeverity ||
		routing.KindMessageLabels != KindRouteMessageLabels {
		t.Error("routing and groups name the open alert group updates differently")
	}
	if organization.OpenGroupUpdatesChannel != OpenGroupUpdatesChannel ||
		organization.KindOrganizationButtons != KindOrganizationButtons ||
		organization.KindTeamEvaluation != KindTeamEvaluation {
		t.Error("organization and groups name the open alert group updates differently")
	}
	if organization.KindMessageSettings != KindOrganizationMessageSettings {
		t.Error("organization and groups name the open alert group updates of the message settings differently")
	}
	if teams.OpenGroupUpdatesChannel != OpenGroupUpdatesChannel || teams.KindTeamEvaluation != KindTeamEvaluation ||
		teams.KindTeamName != KindTeamName {
		t.Error("teams and groups name the open alert group updates differently")
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
		TeamID: pgtype.Int8{Int64: u.teamID, Valid: u.teamID != 0}, AfterAlertGroupID: u.after}, nil
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

func (f *fakeDB) ListEvaluatedGroupsAfter(_ context.Context, arg dbgen.ListEvaluatedGroupsAfterParams) ([]int64,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListEvaluatedGroupsAfter"); err != nil {
		return nil, err
	}
	var out []int64
	for _, g := range f.sortedGroups() {
		if (g.Status != "resolved" || g.GraceDeadline.Valid) && g.ID > arg.AfterID && arg.OrgID == orgID {
			out = append(out, g.ID)
		}
	}
	return out[:min(len(out), int(arg.Lim))], nil
}

func (f *fakeDB) ListOpenGroupsOfTeamAfter(_ context.Context, arg dbgen.ListOpenGroupsOfTeamAfterParams) ([]int64,
	error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("ListOpenGroupsOfTeamAfter"); err != nil {
		return nil, err
	}
	ts := f.teamsOf()
	var out []int64
	for _, g := range f.sortedGroups() {
		if g.Status != "resolved" && g.ID > arg.AfterID && arg.OrgID == orgID &&
			slices.ContainsFunc(ts.groups[g.ID], func(t fakeGroupTeam) bool { return t.team == arg.TeamID }) {
			out = append(out, g.ID)
		}
	}
	return out[:min(len(out), int(arg.Lim))], nil
}

func (f *fakeDB) LockTeamEvaluation(_ context.Context, arg dbgen.LockTeamEvaluationParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call("LockTeamEvaluation"); err != nil {
		return err
	}
	if arg.LockClass != db.TeamEvaluationLockClass || arg.OrgID != orgID {
		return fmt.Errorf("the lock of the evaluation of the teams %+v", arg)
	}
	return nil
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
	u.acked.RouteID = 3
	before := []dbgen.LockGroupsRow{*u.acked, *u.snoozed, *u.firing, *u.elsewhere}
	entries, saves, hints := len(u.db.entries), u.db.calls["SaveGroup"], len(u.db.hints)
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), kind: KindDestinationButtons, destinationID: 21,
		requested: u.clock.Now()})
	u.round(t, u.worker)
	// The acknowledged Alert Group, the first by id, is of the later Route 3: the Alert Groups go by Route, then id.
	if want := []int64{u.firing.ID, u.acked.ID}; !slices.Equal(rendered, want) {
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
	if !slices.Equal(rendered, []int64{u.firing.ID, u.acked.ID}) || !strings.Contains(u.log.String(),
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

	// A Route template that fails is recorded as the dispatcher records it, with the Alert Group's live-update hint,
	// when the re-render changed a Desired state; a second run, which changes none, records nothing and sends no hint.
	changes = map[int64]bool{u.snoozed.ID: true}
	u.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
		r.Fallback(TemplateFailure{Template: "root_message", Detail: "root_message template failed: boom"})
		if changes[r.Group.ID] {
			r.Changed()
		}
		return nil
	})
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), kind: KindOrganizationButtons,
		requested: u.clock.Now()})
	entries, hints = len(u.db.entries), len(u.db.hints)
	u.round(t, u.worker)
	if e := u.last(t, u.snoozed); len(u.db.entries) != entries+1 || e.SystemEvent.String != "fallback_template_used" {
		t.Errorf("the failed template: %d entries, %+v", len(u.db.entries)-entries, e.InsertTimelineEntryParams)
	}
	if got := u.db.hints[hints:]; len(got) != 1 || got[0] != (db.Hint{OrgID: orgID, Type: live.HintAlertGroup,
		ID: u.snoozed.PublicID}) {
		t.Errorf("the hints of the failed template %+v", got)
	}
	changes = map[int64]bool{}
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), kind: KindOrganizationButtons,
		requested: u.clock.Now()})
	entries, hints = len(u.db.entries), len(u.db.hints)
	u.round(t, u.worker)
	if len(u.db.entries) != entries || len(u.db.hints) != hints || len(u.db.updates) != 0 {
		t.Errorf("a second run of the failed template: %d entries, %d hints", len(u.db.entries)-entries,
			len(u.db.hints)-hints)
	}

	for kind, q := range map[string]string{KindDestinationButtons: "ListOpenGroupsOfDestinationAfter",
		KindOrganizationButtons: "ListOpenGroupsFollowingOrganizationAfter"} {
		for _, failing := range []string{q, "LockGroups", "rerender", "InsertTimelineEntry", "Notify"} {
			u := newUpdates(t)
			u.db.delivered, u.db.following = map[int64][]int64{u.acked.ID: {21}}, []int64{21}
			u.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
				if failing == "rerender" {
					return errBoom
				}
				r.Fallback(TemplateFailure{Template: "root_message", Detail: "failed"})
				r.Changed()
				return nil
			})
			// A row of the Organization's set names no Destination.
			row, scope := &fakeUpdate{id: u.db.id(), kind: kind, requested: u.clock.Now()}, ""
			if kind == KindDestinationButtons {
				row.destinationID, scope = 21, `"destination":"DS000000000021",`
			}
			u.db.updates = append(u.db.updates, row)
			u.db.fail[failing] = errBoom
			if u.round(t, u.worker); len(u.db.updates) != 1 || !strings.Contains(u.log.String(),
				`"event":"open_group_update_failed","kind":"`+kind+`",`+scope+`"error":`) {
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

// checkoutTeam is the Team "checkout" that a test creates for the value checkout of the Team label.
const checkoutTeam int64 = 13

// evaluation is the harness of the evaluation on save (C-25.FR-13): the Teams payments, search and checkout-team,
// classified from the fake database, a worker at a manual real clock, and the re-renders the dispatcher hands over.
type evaluation struct {
	*harness
	real     *clock.Manual
	worker   *UpdateWorker
	rendered []Rendering
}

func newEvaluation(t *testing.T) *evaluation {
	t.Helper()
	h := newHarness(t)
	h.withTeams()
	h.svc.classify = func(Queries) teams.ClassifyQueries { return h.db }
	e := &evaluation{harness: h, real: clock.NewManual(t0.Add(-24 * time.Hour))}
	h.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
		e.rendered = append(e.rendered, r)
		return nil
	})
	e.worker = e.workerOf("replica-a")
	return e
}

func (e *evaluation) workerOf(owner string) *UpdateWorker {
	return &UpdateWorker{Service: e.svc, Log: e.svc.log, Lease: db.Lease{Owner: owner, Duration: UpdateLease,
		Clocks: clock.Clocks{Business: e.clock, Real: e.real}}}
}

// fire fires an Alert of the Route with labels, to which routing gave no Teams, and returns it with its Alert Group.
func (e *evaluation) fire(t *testing.T, route int64, labels map[string]string) (int64, *dbgen.LockGroupsRow) {
	t.Helper()
	a := e.alert(route, "warning", labels)
	e.changes(t, ingest.ChangeFired, a)
	return a, e.groupOf(t, a)
}

// save inserts a row team_evaluation of the Team, none for 0, as teams and organization do with a save, after a
// change of the Teams that raises teams_version.
func (e *evaluation) save(team int64) {
	e.db.teamsOf().version++
	e.db.updates = append(e.db.updates, &fakeUpdate{id: e.db.id(), kind: KindTeamEvaluation, teamID: team,
		requested: e.clock.Now()})
}

func (e *evaluation) round(t *testing.T, w *UpdateWorker) {
	t.Helper()
	if _, err := w.Round(t.Context()); err != nil {
		t.Fatalf("round: %v", err)
	}
}

// TestTeamEvaluation is C-25.FR-13 and C-25.AC-13 on the worker: with an open, firing Alert Group of
// team="checkout", whose Alert routing gave no Teams — no alert_teams row at all, as for an Alert that routing took
// up late or that a 0.1 replica first saw fire — creating the Team "checkout" adds it to the Alert's Teams and to the
// Alert Group with one Quiet team_joined of kind alerts by the label, as the system, and re-renders it with that event
// alone: no unknown_team_value, no other event and no timer. Another Alert Group is not touched. The row is deleted
// and open_group_update_done names the kind, the Team and one Alert Group; a second run adds nothing, saves nothing,
// renders nothing and counts none.
func TestTeamEvaluation(t *testing.T) {
	e := newEvaluation(t)
	if classifyQueries(e.db) == nil {
		t.Fatal("no queries of the classification")
	}
	a, g := e.fire(t, 2, map[string]string{"alertname": "Latency", "cluster": "a", "team": "checkout"})
	b, other := e.fire(t, 3, map[string]string{"alertname": "Latency", "cluster": "b", "team": "Payments"})
	ts := e.db.teamsOf()
	if len(ts.alerts[a]) != 0 || len(ts.groups[g.ID]) != 0 || len(e.teamEntries(g)) != 0 {
		t.Fatalf("before the save: alert teams %v, group teams %v", ts.alerts[a], ts.groups[g.ID])
	}
	ts.refs[checkoutTeam] = dbgen.ListTeamRefsRow{ID: checkoutTeam, PublicID: "TMCHECKOUTBBBB", Name: "checkout",
		Slug: "checkout"}
	e.clock.Advance(time.Minute)
	entries, saves, timers, otherEntries := len(e.db.entries), e.db.calls["SaveGroup"], e.db.calls["UpsertTimer"],
		len(e.entriesOf(other))
	e.rendered = nil
	e.save(checkoutTeam)
	e.round(t, e.worker)

	if got := ts.alerts[a]; !slices.Equal(got, []fakeAlertTeam{byLabel(checkoutTeam)}) {
		t.Errorf("teams of the alert %v", got)
	}
	if got := e.teamEntries(g); !slices.Equal(got, []string{"team_joined/alerts/quiet//checkout/label//"}) {
		t.Errorf("timeline %v", got)
	}
	if got := e.groupTeamSlugs(t, g); !slices.Equal(got, []string{"checkout:label"}) {
		t.Errorf("teams of the alert group %v", got)
	}
	last := e.last(t, g)
	if len(e.db.entries) != entries+1 || last.ActorKind != "system" || last.Transport != "system" ||
		!last.At.Equal(e.clock.Now()) || e.db.calls["SaveGroup"] != saves+1 || e.db.calls["UpsertTimer"] != timers {
		t.Errorf("%d entries, %d saves, %d timers, last %+v", len(e.db.entries)-entries,
			e.db.calls["SaveGroup"]-saves, e.db.calls["UpsertTimer"]-timers, last.InsertTimelineEntryParams)
	}
	if len(e.rendered) != 1 || e.rendered[0].Group.ID != g.ID || e.rendered[0].Actor != System ||
		!e.rendered[0].Configuration ||
		len(e.rendered[0].Events) != 1 || e.rendered[0].Events[0].Event != EventTeamJoined ||
		e.rendered[0].Events[0].Loudness != Quiet || len(e.rendered[0].Events[0].Mentions) != 0 {
		t.Errorf("rendered %+v", e.rendered)
	}
	if len(ts.alerts[b]) != 0 || len(e.entriesOf(other)) != otherEntries || len(ts.groups[other.ID]) != 0 {
		t.Errorf("another alert group changed: %v %v", ts.alerts[b], ts.groups[other.ID])
	}
	if len(e.db.updates) != 0 || !strings.Contains(e.log.String(), `"event":"open_group_update_done",`+
		`"kind":"team_evaluation","team":"TMCHECKOUTBBBB","alert_groups":1`) {
		t.Errorf("rows %d, log %s", len(e.db.updates), e.log)
	}

	// A second run adds nothing.
	entries, saves = len(e.db.entries), e.db.calls["SaveGroup"]
	e.rendered = nil
	e.save(checkoutTeam)
	e.round(t, e.worker)
	if len(e.db.entries) != entries || e.db.calls["SaveGroup"] != saves || len(e.rendered) != 0 ||
		!slices.Equal(ts.alerts[a], []fakeAlertTeam{byLabel(checkoutTeam)}) || !strings.Contains(e.log.String(),
		`"kind":"team_evaluation","team":"TMCHECKOUTBBBB","alert_groups":0`) {
		t.Errorf("a second run: %d entries, %d saves, rendered %d, log %s", len(e.db.entries)-entries,
			e.db.calls["SaveGroup"]-saves, len(e.rendered), e.log)
	}
}

// TestTeamEvaluationMatchersAndLabel is C-25.FR-13 and C-25.FR-5 for the other saves: a new Team matcher brings its
// Team by the rule matcher with its text; removing it again takes nothing away — the Team stays on the Alert and in
// the Alert Group, which records nothing; a new Team label brings the Teams its values name, in a run without a Team,
// by the label even when another Alert of the Alert Group, earlier by id, brings the same Team by a matcher. Only
// what the classification adds is written: a Team an Alert already has keeps its rule.
func TestTeamEvaluationMatchersAndLabel(t *testing.T) {
	e := newEvaluation(t)
	ts := e.db.teamsOf()
	a, g := e.fire(t, 2, map[string]string{"alertname": "Latency", "cluster": "x", "team": "Payments"})
	ts.matchers = map[int64][]teamsdb.ListClassifiedMatchersRow{search: {{TeamID: search, Label: "cluster", Op: "=",
		Value: "x"}}}
	e.save(search)
	e.round(t, e.worker)
	matcher := `cluster="x"`
	if got := ts.alerts[a]; !slices.Equal(got, []fakeAlertTeam{byMatcher(search, matcher)}) {
		t.Errorf("teams of the alert %v", got)
	}
	if got := e.teamEntries(g); !slices.Equal(got, []string{"team_joined/alerts/quiet//search/matcher/" + matcher +
		"/"}) {
		t.Errorf("timeline after the matcher %v", got)
	}

	delete(ts.matchers, search)
	entries := len(e.db.entries)
	e.save(search)
	e.round(t, e.worker)
	if len(e.db.entries) != entries || !slices.Equal(ts.alerts[a], []fakeAlertTeam{byMatcher(search, matcher)}) ||
		!slices.Equal(e.groupTeamSlugs(t, g), []string{"search:matcher"}) {
		t.Errorf("the matcher removed: %d entries, alert %v, group %v", len(e.db.entries)-entries, ts.alerts[a],
			e.groupTeamSlugs(t, g))
	}

	// A new Team label: c, earlier by id, is of payments by a matcher, b by the label; payments now also picks a by
	// its matcher, while a already has payments by the label.
	ts.matchers[payments] = []teamsdb.ListClassifiedMatchersRow{{TeamID: payments, Label: "cluster", Op: "=~",
		Value: "x|y"}}
	c, gb := e.fire(t, 3, map[string]string{"alertname": "Latency", "cluster": "y", "service_team": "nobody"})
	b, _ := e.fire(t, 3, map[string]string{"alertname": "Disk", "cluster": "y", "service_team": "payments"})
	if e.groupOf(t, b) != gb {
		t.Fatal("the alerts of cluster y are in two alert groups")
	}
	ts.alerts[a] = append(ts.alerts[a], byLabel(payments))
	ts.label = "service_team"
	entries = len(e.db.entries)
	e.save(0)
	e.round(t, e.worker)
	if got := e.teamEntries(gb); !slices.Equal(got, []string{"team_joined/alerts/quiet//payments/label//"}) {
		t.Errorf("timeline after the team label %v", got)
	}
	if !slices.Equal(ts.alerts[b], []fakeAlertTeam{byLabel(payments)}) ||
		!slices.Equal(ts.alerts[c], []fakeAlertTeam{byMatcher(payments, `cluster=~"x|y"`)}) {
		t.Errorf("teams of the alerts %v %v", ts.alerts[b], ts.alerts[c])
	}
	// The Alert Group of a joins payments by the matcher that picks a now; a keeps the rule it had.
	if got := e.teamEntries(g); !slices.Equal(got, []string{"team_joined/alerts/quiet//search/matcher/" + matcher +
		"/", "team_joined/alerts/quiet//payments/matcher/" + `cluster=~"x|y"` + "/"}) {
		t.Errorf("timeline of the first alert group %v", got)
	}
	if !slices.Equal(ts.alerts[a], []fakeAlertTeam{byMatcher(search, matcher), byLabel(payments)}) ||
		len(e.db.entries) != entries+2 || !strings.Contains(e.log.String(),
		`"event":"open_group_update_done","kind":"team_evaluation","alert_groups":2`) {
		t.Errorf("alert %v, %d entries, log %s", ts.alerts[a], len(e.db.entries)-entries, e.log)
	}
}

// TestTeamEvaluationLeavesAlone is C-25.FR-13 on what the evaluation does not reach: a resolved Alert of an open Alert
// Group, a resolved Alert Group, one resolved after the batch listed it, and an Alert whose own status is resolved
// while it still fires in its Alert Group, which a Snapshot that fires it again routes; and it is C-25.FR-13 in
// batches: the Alert Groups are walked by id, a batch at a time, and changed in the order (Route, id).
func TestTeamEvaluationLeavesAlone(t *testing.T) {
	e := newEvaluation(t)
	ts := e.db.teamsOf()
	gone, gg := e.fire(t, 2, map[string]string{"alertname": "Gone", "cluster": "a", "team": "search"})
	e.resolve(t, gone)
	gg.Status, gg.ResolvedAt = "resolved", ts0()
	kept, gk := e.fire(t, 3, map[string]string{"alertname": "Kept", "cluster": "k", "team": "payments"})
	left, _ := e.fire(t, 3, map[string]string{"alertname": "Left", "cluster": "k", "team": "search"})
	e.resolve(t, left)
	refired, gr := e.fire(t, 2, map[string]string{"alertname": "Refired", "cluster": "r", "team": "search"})
	e.db.alerts[refired].Status = "resolved"
	late, gl := e.fire(t, 2, map[string]string{"alertname": "Late", "cluster": "l", "team": "payments"})
	first, gf := e.fire(t, 2, map[string]string{"alertname": "First", "cluster": "f", "team": "search"})
	e.db.before["LockGroups"] = func() {
		e.db.before["LockGroups"] = nil
		gl.Status, gl.ResolvedAt = "resolved", ts0()
	}
	e.rendered = nil
	e.save(0)
	e.round(t, e.worker)
	if !slices.Equal(ts.alerts[kept], []fakeAlertTeam{byLabel(payments)}) || len(ts.alerts[left]) != 0 ||
		len(ts.alerts[gone]) != 0 || len(ts.alerts[refired]) != 0 || len(ts.alerts[late]) != 0 ||
		!slices.Equal(ts.alerts[first], []fakeAlertTeam{byLabel(search)}) {
		t.Errorf("teams of the alerts: kept %v, left %v, gone %v, refired %v, late %v, first %v", ts.alerts[kept],
			ts.alerts[left], ts.alerts[gone], ts.alerts[refired], ts.alerts[late], ts.alerts[first])
	}
	for name, g := range map[string]*dbgen.LockGroupsRow{"resolved": gg, "refired": gr, "resolved later": gl} {
		if len(ts.groups[g.ID]) != 0 {
			t.Errorf("the %s alert group got teams %v", name, ts.groups[g.ID])
		}
	}
	if got := e.groupTeamSlugs(t, gk); !slices.Equal(got, []string{"payments:label"}) {
		t.Errorf("teams of the alert group of kept %v", got)
	}
	// Route 2 comes before Route 3, whatever the ids.
	var order []int64
	for _, r := range e.rendered {
		order = append(order, r.Group.ID)
	}
	if want := []int64{gf.ID, gk.ID}; !slices.Equal(order, want) || gk.ID > gf.ID {
		t.Errorf("rendered %v, want %v", order, want)
	}
	if len(e.db.updates) != 0 {
		t.Errorf("rows %d", len(e.db.updates))
	}

	// One Alert Group a batch: the three open ones, then an empty batch that deletes the row.
	e.worker.Batch = 1
	lists, advances := e.db.calls["ListEvaluatedGroupsAfter"], e.db.calls["AdvanceOpenGroupUpdate"]
	e.save(0)
	e.round(t, e.worker)
	if e.db.calls["ListEvaluatedGroupsAfter"]-lists != 4 || e.db.calls["AdvanceOpenGroupUpdate"]-advances != 3 ||
		len(e.db.updates) != 0 {
		t.Errorf("batches: %d lists, %d advances", e.db.calls["ListEvaluatedGroupsAfter"]-lists,
			e.db.calls["AdvanceOpenGroupUpdate"]-advances)
	}
}

func ts0() pgtype.Timestamptz { return ts(t0) }

// TestTeamEvaluationClaims is ADR-0007 for the evaluation: a row one worker holds is not run by another; when the
// first worker's lease runs out before it ran, the other takes the row over and runs it, and the first, whose lease is
// lost, changes nothing, so the Team joins once.
func TestTeamEvaluationClaims(t *testing.T) {
	e := newEvaluation(t)
	_, g := e.fire(t, 2, map[string]string{"alertname": "Latency", "cluster": "a", "team": "search"})
	b := e.workerOf("replica-b")
	e.save(search)
	claimed, ok, err := e.svc.claimUpdate(t.Context(), e.worker.Lease)
	if err != nil || !ok || claimed.kind != KindTeamEvaluation || claimed.teamPID != "TMSEARCHAAAAAA" {
		t.Fatalf("claim = %+v %v %v", claimed, ok, err)
	}
	e.round(t, b)
	if len(e.teamEntries(g)) != 0 || len(e.db.updates) != 1 {
		t.Fatalf("the other worker ran a held row: %v", e.teamEntries(g))
	}
	e.real.Advance(UpdateLease)
	e.round(t, b)
	e.worker.run(t.Context(), claimed)
	if got := e.teamEntries(g); !slices.Equal(got, []string{"team_joined/alerts/quiet//search/label//"}) ||
		len(e.db.updates) != 0 {
		t.Errorf("timeline %v, rows %d", got, len(e.db.updates))
	}
}

// TestTeamEvaluationFailures: a failing read or write of a batch logs open_group_update_failed with the kind and the
// Team, and leaves the row for any replica once its lease runs out, its Alert Groups unchanged.
func TestTeamEvaluationFailures(t *testing.T) {
	for _, q := range []string{"ListEvaluatedGroupsAfter", "LockTeamEvaluation", "LockGroups", "GetTeamsStamp",
		"ListClassifiedTeams",
		"ListClassifiedMatchers", "ListGroupFiringAlerts", "AddAlertTeams", "AddGroupTeams", "InsertTimelineEntry",
		"SaveGroup", "labels"} {
		e := newEvaluation(t)
		a, _ := e.fire(t, 2, map[string]string{"alertname": "Latency", "cluster": "a", "team": "search"})
		e.db.teamsOf().matchers = map[int64][]teamsdb.ListClassifiedMatchersRow{payments: {{TeamID: payments,
			Label: "cluster", Op: "=", Value: "a"}}}
		if q == "labels" {
			e.db.alerts[a].Labels = []byte(`[`)
		}
		e.save(search)
		e.db.fail[q] = errBoom
		e.round(t, e.worker)
		if len(e.db.updates) != 1 || !strings.Contains(e.log.String(),
			`"event":"open_group_update_failed","kind":"team_evaluation","team":"TMSEARCHAAAAAA","error":`) ||
			strings.Contains(e.log.String(), "open_group_update_done") {
			t.Errorf("%s failing: rows %d, log %s", q, len(e.db.updates), e.log)
		}
	}
}

// TestTeamEvaluationGracePeriod is C-25.FR-13 for an Alert Group a person resolved that is still in its Grace
// period: the evaluation records the Team on its firing tail, which the end of the Grace period groups again with it,
// and leaves the Alert Group itself as it is — no Team, no Timeline entry, no re-render; one resolved without a Grace
// period is not read at all.
func TestTeamEvaluationGracePeriod(t *testing.T) {
	e := newEvaluation(t)
	tail, g := e.fire(t, 2, map[string]string{"alertname": "Latency", "cluster": "a", "team": "search"})
	gone, gr := e.fire(t, 2, map[string]string{"alertname": "Gone", "cluster": "b", "team": "search"})
	g.Status, g.ResolvedAt, g.GraceDeadline = "resolved", ts0(), ts(t0.Add(time.Hour))
	gr.Status, gr.ResolvedAt = "resolved", ts0()
	ts := e.db.teamsOf()
	entries, saves := len(e.db.entries), e.db.calls["SaveGroup"]
	e.rendered = nil
	e.save(search)
	e.round(t, e.worker)
	if !slices.Equal(ts.alerts[tail], []fakeAlertTeam{byLabel(search)}) || len(ts.alerts[gone]) != 0 {
		t.Errorf("teams of the tail %v, of the alert resolved without a grace period %v", ts.alerts[tail],
			ts.alerts[gone])
	}
	if len(ts.groups[g.ID]) != 0 || len(ts.groups[gr.ID]) != 0 || len(e.db.entries) != entries ||
		e.db.calls["SaveGroup"] != saves || len(e.rendered) != 0 || !strings.Contains(e.log.String(),
		`"kind":"team_evaluation","team":"TMSEARCHAAAAAA","alert_groups":0`) {
		t.Errorf("the alert groups changed: teams %v %v, %d entries, log %s", ts.groups[g.ID], ts.groups[gr.ID],
			len(e.db.entries)-entries, e.log)
	}
	e.db.fail["AddAlertTeams"] = errBoom
	e.save(search)
	e.round(t, e.worker)
	if len(e.db.updates) != 1 || !strings.Contains(e.log.String(), `"event":"open_group_update_failed"`) {
		t.Errorf("a failing write of the tails: rows %d, log %s", len(e.db.updates), e.log)
	}
}

// TestMessageLabelsUpdate is C-29.FR-2 and C-29.AC-4 on the worker: a row route_message_labels runs the dispatcher's
// re-render step, without a lifecycle event, on each open Alert Group of its Route, by id in batches, and on none of
// another Route, so that delivery edits their Root messages Quietly to show the new labels. It saves no Alert Group,
// records no Timeline entry and sends no live-update hint; it is deleted with its last batch and logged as
// open_group_update_done with its Route and the number of Alert Groups whose Desired state changed, none on a second
// run.
func TestMessageLabelsUpdate(t *testing.T) {
	u := newUpdates(t)
	var rendered []int64
	changed := true
	u.svc.SetRerender(func(_ context.Context, _ DBTX, r Rendering) error {
		if len(r.Events) != 0 || r.Actor != System || !r.Configuration {
			t.Errorf("a re-render with events %+v as %+v, configuration %v", r.Events, r.Actor, r.Configuration)
		}
		rendered = append(rendered, r.Group.ID)
		if changed {
			r.Changed()
		}
		return nil
	})
	entries, saves, hints := len(u.db.entries), u.db.calls["SaveGroup"], len(u.db.hints)
	u.worker.Batch = 2
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), routeID: 2, kind: KindRouteMessageLabels,
		requested: u.clock.Now()})
	u.round(t, u.worker)
	if want := []int64{u.acked.ID, u.snoozed.ID, u.firing.ID}; !slices.Equal(rendered, want) {
		t.Errorf("rendered %v, want %v", rendered, want)
	}
	if len(u.db.updates) != 0 || len(u.db.entries) != entries || u.db.calls["SaveGroup"] != saves ||
		len(u.db.hints) != hints || u.db.calls["ListOpenGroupsOfRouteAfter"] != 2 || !strings.Contains(u.log.String(),
		`"event":"open_group_update_done","kind":"route_message_labels","route":"RTDBAAAAAAAAAA","alert_groups":3`) {
		t.Errorf("rows %d, entries %d, saves %d, hints %d, calls %v, log %s", len(u.db.updates),
			len(u.db.entries)-entries, u.db.calls["SaveGroup"]-saves, len(u.db.hints)-hints, u.db.calls, u.log)
	}
	rendered, changed = nil, false
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), routeID: 2, kind: KindRouteMessageLabels,
		requested: u.clock.Now()})
	u.round(t, u.worker)
	if len(rendered) != 3 || !strings.Contains(u.log.String(),
		`"kind":"route_message_labels","route":"RTDBAAAAAAAAAA","alert_groups":0`) {
		t.Errorf("a second run: rendered %v, log %s", rendered, u.log)
	}
	u.db.fail["ListOpenGroupsOfRouteAfter"] = errBoom
	u.db.updates = append(u.db.updates, &fakeUpdate{id: u.db.id(), routeID: 2, kind: KindRouteMessageLabels,
		requested: u.clock.Now()})
	u.round(t, u.worker)
	if !strings.Contains(u.log.String(), `"event":"open_group_update_failed","kind":"route_message_labels"`) {
		t.Errorf("a failing list: %s", u.log)
	}
}

// renders records the renderings the dispatcher hands over as group:events, and reports a changed Desired state only
// on the first rendering of each Alert Group, as delivery does with a Root message whose hash is then unchanged.
type renders struct {
	got  []string
	seen map[int64]bool
	fail error
}

func (r *renders) install(t *testing.T, svc *Service) {
	t.Helper()
	r.seen = map[int64]bool{}
	svc.SetRerender(func(_ context.Context, _ DBTX, rd Rendering) error {
		if r.fail != nil {
			return r.fail
		}
		if rd.Actor != System || !rd.Configuration {
			t.Errorf("a rendering as %+v, configuration %v", rd.Actor, rd.Configuration)
		}
		events := make([]string, len(rd.Events))
		for i, e := range rd.Events {
			events[i] = string(e.Event) + "/" + string(e.Loudness)
		}
		r.got = append(r.got, fmt.Sprintf("%d:%s", rd.Group.ID, strings.Join(events, ",")))
		if !r.seen[rd.Group.ID] {
			r.seen[rd.Group.ID] = true
			rd.Changed()
		}
		return nil
	})
}

// TestMessageSettingsUpdate is C-29.FR-1 to FR-3 on the worker: a row organization_message_settings, which a changed
// Severity label, Team label, Instance labels, time zone, start of the working day or first day of the week writes,
// takes the lock of the evaluation before it lists its batch, runs the re-render step without a lifecycle event on
// each open Alert Group, by id in batches and in the order (Route, id), marked as a change of configuration, so that
// delivery edits each Root message Quietly, then evaluates their Teams, as team_evaluation does: a Team the evaluation
// brings joins with the Quiet team_joined, which renders its Alert Group again in the same transaction. A resolved
// Alert Group is not rendered. Nothing is saved for the re-render, no Timeline entry is recorded but the team_joined,
// and the row is deleted with its last batch and logged with the number of Alert Groups a Team joined or whose Desired
// state changed, each counted once, none on a second run.
func TestMessageSettingsUpdate(t *testing.T) {
	e := newEvaluation(t)
	var r renders
	_, g := e.fire(t, 2, map[string]string{"alertname": "Latency", "cluster": "a", "team": "search"})
	_, other := e.fire(t, 3, map[string]string{"alertname": "Disk", "cluster": "b", "team": "nobody"})
	gone, resolved := e.fire(t, 2, map[string]string{"alertname": "Gone", "cluster": "c"})
	e.resolve(t, gone)
	if resolved.Status != string(StatusResolved) {
		t.Fatalf("the alert group is %s", resolved.Status)
	}
	r.install(t, e.svc)
	entries, saves := len(e.db.entries), e.db.calls["SaveGroup"]
	e.worker.Batch = 1
	locks, lists := 0, 0
	e.db.before["ListEvaluatedGroupsAfter"] = func() { locks, lists = e.db.calls["LockTeamEvaluation"], 1 }
	e.db.updates = append(e.db.updates, &fakeUpdate{id: e.db.id(), kind: KindOrganizationMessageSettings,
		requested: e.clock.Now()})
	e.round(t, e.worker)
	if lists != 1 || locks == 0 {
		t.Errorf("the batch listed after %d locks of the evaluation", locks)
	}
	want := []string{fmt.Sprintf("%d:", g.ID), fmt.Sprintf("%d:team_joined/quiet", g.ID), fmt.Sprintf("%d:", other.ID)}
	if !slices.Equal(r.got, want) {
		t.Errorf("rendered %v, want %v", r.got, want)
	}
	if got := e.teamEntries(g); !slices.Equal(got, []string{"team_joined/alerts/quiet//search/label//"}) {
		t.Errorf("timeline %v", got)
	}
	if len(e.db.entries) != entries+1 || e.db.calls["SaveGroup"] != saves+1 || len(e.db.updates) != 0 ||
		!strings.Contains(e.log.String(), `"event":"open_group_update_done","kind":"organization_message_settings",`+
			`"alert_groups":2`) {
		t.Errorf("%d entries, %d saves, rows %d, log %s", len(e.db.entries)-entries, e.db.calls["SaveGroup"]-saves,
			len(e.db.updates), e.log)
	}

	// A second run renders each open Alert Group again, changes no Desired state and records nothing.
	r.got = nil
	entries, saves = len(e.db.entries), e.db.calls["SaveGroup"]
	e.db.updates = append(e.db.updates, &fakeUpdate{id: e.db.id(), kind: KindOrganizationMessageSettings,
		requested: e.clock.Now()})
	e.round(t, e.worker)
	if want := []string{fmt.Sprintf("%d:", g.ID), fmt.Sprintf("%d:", other.ID)}; !slices.Equal(r.got, want) ||
		len(e.db.entries) != entries || e.db.calls["SaveGroup"] != saves || !strings.Contains(e.log.String(),
		`"kind":"organization_message_settings","alert_groups":0`) {
		t.Errorf("a second run: rendered %v, %d entries, %d saves, log %s", r.got, len(e.db.entries)-entries,
			e.db.calls["SaveGroup"]-saves, e.log)
	}

	// A failing evaluation, list or re-render fails the batch and leaves the row for any replica.
	for _, fail := range []string{"LockTeamEvaluation", "ListEvaluatedGroupsAfter", "render"} {
		e.log.Reset()
		if fail == "render" {
			r.fail = errBoom
		} else {
			e.db.fail[fail] = errBoom
		}
		e.db.updates = []*fakeUpdate{{id: e.db.id(), kind: KindOrganizationMessageSettings, requested: e.clock.Now()}}
		e.round(t, e.worker)
		if len(e.db.updates) != 1 || !strings.Contains(e.log.String(),
			`"event":"open_group_update_failed","kind":"organization_message_settings"`) {
			t.Errorf("%s fails: rows %d, log %s", fail, len(e.db.updates), e.log)
		}
		delete(e.db.fail, fail)
		r.fail = nil
		e.real.Advance(UpdateLease)
	}
}

// TestTeamNameUpdate is C-29.FR-1 on the worker: a row team_name, which a rename of a Team writes, re-renders, Quietly
// and without a lifecycle event, the open Alert Groups that have the Team, whose field Teams shows its name, and no
// other: not one of another Team, not one without a Team, not a resolved one. It evaluates nothing (C-25.FR-13), but
// waits for the Snapshots in flight under the lock of the evaluation before it lists. It is
// deleted with its last batch and logged with its Team; a second run changes no Desired state; a row of a Team no open
// Alert Group has renders nothing, and one whose Team is gone is done.
func TestTeamNameUpdate(t *testing.T) {
	e := newEvaluation(t)
	var r renders
	_, g := e.fire(t, 2, map[string]string{"alertname": "Latency", "cluster": "a", "team": "search"})
	_, paid := e.fire(t, 2, map[string]string{"alertname": "Latency", "cluster": "b", "team": "payments"})
	_, none := e.fire(t, 3, map[string]string{"alertname": "Disk", "cluster": "c"})
	gone, resolved := e.fire(t, 3, map[string]string{"alertname": "Gone", "cluster": "d", "team": "search"})
	e.save(0)
	e.round(t, e.worker)
	e.resolve(t, gone)
	if !slices.Equal(e.groupTeamSlugs(t, g), []string{"search:label"}) ||
		!slices.Equal(e.groupTeamSlugs(t, paid), []string{"payments:label"}) ||
		!slices.Equal(e.groupTeamSlugs(t, resolved), []string{"search:label"}) || len(e.groupTeamSlugs(t, none)) != 0 {
		t.Fatalf("teams %v %v %v", e.groupTeamSlugs(t, g), e.groupTeamSlugs(t, paid), e.groupTeamSlugs(t, resolved))
	}
	ts := e.db.teamsOf()
	ref := ts.refs[search]
	ref.Name = "Search and discovery"
	ts.refs[search] = ref
	r.install(t, e.svc)
	entries, saves := len(e.db.entries), e.db.calls["SaveGroup"]
	locks, evaluated := e.db.calls["LockTeamEvaluation"], e.db.calls["ListGroupFiringAlerts"]
	listedAfter := 0
	e.db.before["ListOpenGroupsOfTeamAfter"] = func() { listedAfter = e.db.calls["LockTeamEvaluation"] }
	e.db.updates = append(e.db.updates, &fakeUpdate{id: e.db.id(), kind: KindTeamName, teamID: search,
		requested: e.clock.Now()})
	e.round(t, e.worker)
	if want := []string{fmt.Sprintf("%d:", g.ID)}; !slices.Equal(r.got, want) {
		t.Errorf("rendered %v, want %v", r.got, want)
	}
	// A rename evaluates nothing (C-25.FR-13): no classification. Its one batch takes the lock of the evaluation, which
	// waits for the Snapshots in flight, before it lists.
	if e.db.calls["LockTeamEvaluation"] != locks+1 || e.db.calls["ListGroupFiringAlerts"] != evaluated ||
		listedAfter != locks+1 {
		t.Errorf("the rename: %d locks, %d reads of the firing alerts, listed after %d locks",
			e.db.calls["LockTeamEvaluation"]-locks, e.db.calls["ListGroupFiringAlerts"]-evaluated, listedAfter-locks)
	}
	if len(e.db.entries) != entries || e.db.calls["SaveGroup"] != saves || len(e.db.updates) != 0 ||
		!strings.Contains(e.log.String(), `"event":"open_group_update_done","kind":"team_name",`+
			`"team":"TMSEARCHAAAAAA","alert_groups":1`) {
		t.Errorf("%d entries, %d saves, rows %d, log %s", len(e.db.entries)-entries, e.db.calls["SaveGroup"]-saves,
			len(e.db.updates), e.log)
	}

	r.got = nil
	e.db.updates = append(e.db.updates, &fakeUpdate{id: e.db.id(), kind: KindTeamName, teamID: search,
		requested: e.clock.Now()})
	e.db.updates = append(e.db.updates, &fakeUpdate{id: e.db.id(), kind: KindTeamName, teamID: checkout,
		requested: e.clock.Now()})
	e.round(t, e.worker)
	if want := []string{fmt.Sprintf("%d:", g.ID)}; !slices.Equal(r.got, want) || len(e.db.updates) != 0 ||
		!strings.Contains(e.log.String(), `"kind":"team_name","team":"TMSEARCHAAAAAA","alert_groups":0`) {
		t.Errorf("a second run: rendered %v, rows %d, log %s", r.got, len(e.db.updates), e.log)
	}

	// A row whose Team is gone, team_id set to null by its deletion, is done without a list.
	r.got = nil
	e.db.fail["ListOpenGroupsOfTeamAfter"] = errBoom
	e.db.updates = append(e.db.updates, &fakeUpdate{id: e.db.id(), kind: KindTeamName, requested: e.clock.Now()})
	e.round(t, e.worker)
	if len(r.got) != 0 || len(e.db.updates) != 0 || !strings.Contains(e.log.String(),
		`"event":"open_group_update_done","kind":"team_name","alert_groups":0`) {
		t.Errorf("a row without its team: rendered %v, rows %d, log %s", r.got, len(e.db.updates), e.log)
	}

	e.db.updates = append(e.db.updates, &fakeUpdate{id: e.db.id(), kind: KindTeamName, teamID: search,
		requested: e.clock.Now()})
	e.round(t, e.worker)
	if len(e.db.updates) != 1 || !strings.Contains(e.log.String(),
		`"event":"open_group_update_failed","kind":"team_name","team":"TMSEARCHAAAAAA"`) {
		t.Errorf("a failing list: rows %d, log %s", len(e.db.updates), e.log)
	}
}

// TestUpdateKindsOfOlderVersions is the rolling update for the kinds of 0031: a replica of a version that does not
// know organization_message_settings and team_name — whose kinds end with team_evaluation — never claims a row of
// them nor waits for one, and a replica of this version claims both.
func TestUpdateKindsOfOlderVersions(t *testing.T) {
	e := newEvaluation(t)
	known := updateKinds
	t.Cleanup(func() { updateKinds = known })
	updateKinds = []string{KindRouteMinimumSeverity, KindRouteMessageLabels, KindDestinationButtons,
		KindOrganizationButtons, KindTeamEvaluation}
	e.db.updates = append(e.db.updates,
		&fakeUpdate{id: e.db.id(), kind: KindOrganizationMessageSettings, requested: e.clock.Now()},
		&fakeUpdate{id: e.db.id(), kind: KindTeamName, teamID: search, requested: e.clock.Now()})
	if _, ok, err := e.svc.claimUpdate(t.Context(), e.worker.Lease); ok || err != nil {
		t.Errorf("an older replica claimed a row of a newer kind: %v, %v", ok, err)
	}
	if next, err := e.svc.nextUpdate(t.Context(), e.worker.Lease, time.Hour); next != time.Hour || err != nil {
		t.Errorf("an older replica waits %v for a row of a newer kind, %v", next, err)
	}
	updateKinds = known
	for _, kind := range []string{KindOrganizationMessageSettings, KindTeamName} {
		u, ok, err := e.svc.claimUpdate(t.Context(), e.worker.Lease)
		if !ok || err != nil || u.kind != kind {
			t.Errorf("claimed %+v, %v, %v; want %s", u, ok, err, kind)
		}
	}
}
