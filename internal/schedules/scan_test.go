// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/internalalerts"
	alertsdb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// alertStore is the Internal alerts of the built-in Integration in memory: the firing ones that processing wrote and
// the bodies of the synthetic Snapshots that wait for it.
type alertStore struct {
	builtin   bool
	bodies    map[[32]byte][]byte
	pending   [][]byte
	snapshots int
	open      []alertsdb.ListOpenInternalAlertsRow
}

func newAlertStore() *alertStore {
	return &alertStore{builtin: true, bodies: map[[32]byte][]byte{}}
}

func (a *alertStore) FindBuiltinIntegration(_ context.Context, org int64) (int64, error) {
	if !a.builtin || org != orgID {
		return 0, pgx.ErrNoRows
	}
	return 3, nil
}

func (a *alertStore) InsertInternalBody(_ context.Context, arg alertsdb.InsertInternalBodyParams) error {
	a.bodies[[32]byte(arg.BodySha256)] = arg.Body
	return nil
}

func (a *alertStore) InsertInternalSnapshot(_ context.Context, arg alertsdb.InsertInternalSnapshotParams) error {
	a.snapshots++
	a.pending = append(a.pending, a.bodies[[32]byte(arg.BodySha256)])
	return nil
}

func (a *alertStore) NotifyInternalSnapshot(context.Context, alertsdb.NotifyInternalSnapshotParams) error {
	return nil
}

func (a *alertStore) ListOpenInternalAlerts(_ context.Context, arg alertsdb.ListOpenInternalAlertsParams) (
	[]alertsdb.ListOpenInternalAlertsRow, error) {
	want := map[string]string{}
	if err := json.Unmarshal(arg.Contains, &want); err != nil {
		return nil, err
	}
	var out []alertsdb.ListOpenInternalAlertsRow
	for _, r := range a.open {
		labels := map[string]string{}
		if err := json.Unmarshal(r.Labels, &labels); err != nil || contains(labels, want) {
			out = append(out, r)
		}
	}
	return out, nil
}

func contains(labels, want map[string]string) bool {
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (a *alertStore) ListPendingInternalRaises(context.Context, int64) ([][]byte, error) {
	return a.pending, nil
}

// syntheticAlert is the one Alert of a synthetic Snapshot.
type syntheticAlert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	Fingerprint string            `json:"fingerprint"`
}

func alertOf(t *testing.T, body []byte) syntheticAlert {
	t.Helper()
	var w struct {
		Alerts []syntheticAlert `json:"alerts"`
	}
	if err := json.Unmarshal(body, &w); err != nil || len(w.Alerts) != 1 {
		t.Fatalf("synthetic snapshot %s: %v", body, err)
	}
	return w.Alerts[0]
}

// process applies the pending synthetic Snapshots in order, as processing does: a firing one fires its Alert, by
// fingerprint, with its labels and annotations, and a resolved one ends it.
func (a *alertStore) process(t *testing.T) {
	t.Helper()
	for _, body := range a.pending {
		al := alertOf(t, body)
		a.open = slices.DeleteFunc(a.open, func(r alertsdb.ListOpenInternalAlertsRow) bool {
			return r.Fingerprint == al.Fingerprint
		})
		if al.Status == internalalerts.StatusFiring {
			labels, _ := json.Marshal(al.Labels)
			annotations, _ := json.Marshal(al.Annotations)
			a.open = append(a.open, alertsdb.ListOpenInternalAlertsRow{Fingerprint: al.Fingerprint, Labels: labels,
				StartsAt: al.StartsAt, Annotations: annotations})
		}
	}
	a.pending = nil
}

// last is the Alert of the newest synthetic Snapshot.
func (a *alertStore) last(t *testing.T) syntheticAlert {
	t.Helper()
	if len(a.pending) == 0 {
		t.Fatal("no synthetic snapshot waits")
	}
	return alertOf(t, a.pending[len(a.pending)-1])
}

// firing are the open MusterScheduleGap alerts by Schedule.
func (a *alertStore) firing(t *testing.T) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for _, r := range a.open {
		labels := map[string]string{}
		if err := json.Unmarshal(r.Labels, &labels); err != nil {
			t.Fatal(err)
		}
		out[labels["schedule"]] = labels
	}
	return out
}

func gapSeries(t *testing.T, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(func() bool { return true }).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	for line := range strings.Lines(rec.Body.String()) {
		if strings.HasPrefix(line, `muster_schedule_gap{schedule="`+id+`"}`) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// gapInput is a Schedule of payments in UTC whose only Rotation layer has olga on every day but the ones left out,
// from the start of 28 October 2026.
func gapInput(name string, left ...string) Input {
	days := slices.DeleteFunc(slices.Clone(Weekdays), func(d string) bool { return slices.Contains(left, d) })
	return Input{TeamID: teamPay, Name: name, TimeZone: ptr("UTC"), RotationLayers: []RotationLayerInput{{
		Name: "Days", UserIDs: []string{olga.PublicID}, ShiftLength: ShiftDay, HandoverTime: "00:00",
		StartDate: date("2026-10-28"), Limits: &Limits{Days: days}}}}
}

func newScan(store *fakeStore, c clock.Clock) (*GapScan, *bytes.Buffer) {
	var log bytes.Buffer
	return &GapScan{Store: store, Business: c, Log: logging.New(&log, logging.LevelInfo)}, &log
}

// TestGapScan is C-27.FR-7, C-27.AC-5 and C-25.FR-5 on the scan: a gap 5 days ahead in the main Schedule raises
// MusterScheduleGap once, with the Schedule and its Team by id and name, the Team's slug under the Team label and the
// start of the gap, and exports 1; a second run, before and after processing, raises nothing more; a second Schedule
// with by_default exports 0 and raises nothing until its gap alert is on; a rename of the Schedule or its Team and a
// change of the Team label raise it again as the same firing; a started gap stays raised without a new raise; once
// the gap has passed the next one within the horizon becomes the nearest; an Override that closes every gap of the
// horizon resolves it, and deleting a Schedule resolves its alert and removes its series.
func TestGapScan(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	scan, log := newScan(store, c)
	main, err := svc.Create(ctx, admin, gapInput("Main", "monday"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(ctx, admin, gapInput("Second", "monday"))
	if err != nil {
		t.Fatal(err)
	}
	if !main.GapAlertActive() || main.GapAlert != GapAlertByDefault || second.GapAlertActive() {
		t.Fatalf("gap alerts %s %v, %s %v", main.GapAlert, main.GapAlertActive(), second.GapAlert,
			second.GapAlertActive())
	}
	monday := utc("2026-11-02T00:00:00Z") // 4 days 12 hours ahead
	if err := scan.Scan(ctx, orgID); err != nil {
		t.Fatal(err)
	}
	a := store.alerts.last(t)
	want := map[string]string{"alertname": "MusterScheduleGap", "severity": "warning", "schedule": main.PublicID,
		"schedule_name": "Main", "team_id": teamPay, "team_name": "Payments", "team": "payments"}
	if store.alerts.snapshots != 1 || a.Status != internalalerts.StatusFiring || !maps.Equal(a.Labels, want) ||
		a.Annotations["gap_start"] != "2026-11-02T00:00:00Z" ||
		a.Annotations["summary"] != "No one is On-call in Schedule Main of Payments" ||
		!strings.Contains(a.Annotations["description"], "Schedule Main of Team Payments has a gap from "+
			"2026-11-02T00:00:00Z") || a.Fingerprint != internalalerts.Fingerprint(internalalerts.ScheduleGap,
		map[string]string{"schedule": main.PublicID}) || !a.StartsAt.Equal(t0) {
		t.Fatalf("raised %+v after %d snapshots", a, store.alerts.snapshots)
	}
	if gapSeries(t, main.PublicID) != `muster_schedule_gap{schedule="`+main.PublicID+`"} 1` ||
		gapSeries(t, second.PublicID) != `muster_schedule_gap{schedule="`+second.PublicID+`"} 0` {
		t.Errorf("series %q %q", gapSeries(t, main.PublicID), gapSeries(t, second.PublicID))
	}
	if !strings.Contains(log.String(), `"event":"schedule_gap_detected","schedule":"`+main.PublicID+`","team":"`+
		teamPay+`","gap_start":"2026-11-02T00:00:00Z"`) {
		t.Errorf("logged %s", log.String())
	}
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 1 {
		t.Fatalf("a second run with the raise pending: %d snapshots, %v", store.alerts.snapshots, err)
	}
	store.alerts.process(t)
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 1 {
		t.Fatalf("a run after processing: %d snapshots, %v", store.alerts.snapshots, err)
	}

	// The second Schedule raises once an Admin sets its gap alert on.
	in := inputOf(second)
	in.GapAlert = ptr(GapAlertOn)
	if second, err = svc.Update(ctx, admin, second.PublicID, nil, in); err != nil || !second.GapAlertActive() {
		t.Fatalf("gap alert on = %+v, %v", second, err)
	}
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 2 ||
		store.alerts.last(t).Labels["schedule"] != second.PublicID ||
		gapSeries(t, second.PublicID) != `muster_schedule_gap{schedule="`+second.PublicID+`"} 1` {
		t.Fatalf("the second Schedule on: %d snapshots, %v", store.alerts.snapshots, err)
	}
	store.alerts.process(t)

	// Renames and the Team label change the labels of the same firing.
	in = inputOf(main)
	in.Name = "Primary"
	if main, err = svc.Update(ctx, admin, main.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	store.teams[0].name, store.teams[0].slug, store.teamLabel = "Payments EU", "payments-eu", "owner"
	c.Advance(time.Hour)
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 4 {
		t.Fatalf("renamed: %d snapshots, %v", store.alerts.snapshots, err)
	}
	store.alerts.process(t)
	got := store.alerts.firing(t)
	if l := got[main.PublicID]; len(got) != 2 || l["schedule_name"] != "Primary" || l["team_name"] != "Payments EU" ||
		l["owner"] != "payments-eu" || l["team"] != "" {
		t.Errorf("after the renames %v", got)
	}
	for _, r := range store.alerts.open {
		if !r.StartsAt.Equal(t0) {
			t.Errorf("a raise again started a new firing at %v", r.StartsAt)
		}
	}
	if strings.Count(log.String(), "schedule_gap_detected") != 2 {
		t.Errorf("a raise again logged a detection: %s", log.String())
	}

	// A started gap stays raised, with the start it was raised with.
	c.Set(monday.Add(2 * time.Hour))
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 4 || len(store.alerts.firing(t)) != 2 {
		t.Fatalf("inside the gap: %d snapshots, %v", store.alerts.snapshots, err)
	}
	// Past it, the next Monday is the nearest gap, within the horizon.
	c.Set(monday.Add(25 * time.Hour))
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 6 ||
		store.alerts.last(t).Annotations["gap_start"] != "2026-11-09T00:00:00Z" {
		t.Fatalf("past the gap: %d snapshots, %v", store.alerts.snapshots, err)
	}
	store.alerts.process(t)

	// An Override over the next Monday closes every gap of the horizon of the main Schedule.
	if _, err := svc.CreateOverride(ctx, admin, main.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: utc("2026-11-09T00:00:00Z"), End: utc("2026-11-10T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 7 ||
		store.alerts.last(t).Status != internalalerts.StatusResolved ||
		gapSeries(t, main.PublicID) != `muster_schedule_gap{schedule="`+main.PublicID+`"} 0` {
		t.Fatalf("closed by an Override: %d snapshots, %v", store.alerts.snapshots, err)
	}
	if !strings.Contains(log.String(), `"event":"schedule_gap_cleared","schedule":"`+main.PublicID+`","team":"`+
		teamPay+`"`) {
		t.Errorf("logged %s", log.String())
	}
	store.alerts.process(t)

	// Deleting a Schedule resolves its alert and removes its series.
	if err := svc.Delete(ctx, admin, second.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 8 ||
		store.alerts.last(t).Labels["schedule"] != second.PublicID || gapSeries(t, second.PublicID) != "" {
		t.Fatalf("deleted: %d snapshots, series %q, %v", store.alerts.snapshots, gapSeries(t, second.PublicID), err)
	}
	store.alerts.process(t)
	if len(store.alerts.firing(t)) != 0 {
		t.Errorf("still firing %v", store.alerts.firing(t))
	}
}

// TestGapScanMain is C-27.AC-8 on the scan: with by_default on two Schedules of a Team, making the second one main
// moves the gap alert to it at the next run and resolves the first one's; with on on the first one it is raised there
// again.
func TestGapScanMain(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	scan, _ := newScan(store, c)
	first, err := svc.Create(ctx, admin, gapInput("First", "monday"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(ctx, admin, gapInput("Second", "monday"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(what string) map[string]map[string]string {
		t.Helper()
		if err := scan.Scan(ctx, orgID); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		store.alerts.process(t)
		return store.alerts.firing(t)
	}
	if got := run("first main"); len(got) != 1 || got[first.PublicID] == nil {
		t.Fatalf("first main: %v", got)
	}
	in := inputOf(second)
	in.Main = ptr(true)
	if _, err := svc.Update(ctx, admin, second.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	if got := run("second main"); len(got) != 1 || got[second.PublicID] == nil {
		t.Fatalf("second main: %v", got)
	}
	if first, err = svc.Get(ctx, first.PublicID); err != nil {
		t.Fatal(err)
	}
	in = inputOf(first)
	in.GapAlert = ptr(GapAlertOn)
	if _, err := svc.Update(ctx, admin, first.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	if got := run("first on"); len(got) != 2 {
		t.Fatalf("first on: %v", got)
	}
	in.GapAlert = ptr(GapAlertOff)
	if _, err := svc.Update(ctx, admin, first.PublicID, nil, in); err != nil {
		t.Fatal(err)
	}
	if got := run("first off"); len(got) != 1 || got[second.PublicID] == nil {
		t.Fatalf("first off: %v", got)
	}
}

// TestGapScanHorizon: with the first Thursday closed by an Override, the next gap is Thursday 5 November; 8 days ahead
// it raises nothing yet and exports 0, 6 days ahead it raises with its start.
func TestGapScanHorizon(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	scan, _ := newScan(store, c)
	sc, err := svc.Create(ctx, admin, gapInput("Main", "thursday"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateOverride(ctx, admin, sc.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: utc("2026-10-29T00:00:00Z"), End: utc("2026-10-30T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	c.Set(utc("2026-10-28T00:00:00Z"))
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 0 ||
		gapSeries(t, sc.PublicID) != `muster_schedule_gap{schedule="`+sc.PublicID+`"} 0` {
		t.Fatalf("8 days ahead: %d snapshots, %v", store.alerts.snapshots, err)
	}
	c.Set(utc("2026-10-30T00:00:00Z"))
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 1 ||
		store.alerts.last(t).Annotations["gap_start"] != "2026-11-05T00:00:00Z" ||
		gapSeries(t, sc.PublicID) != `muster_schedule_gap{schedule="`+sc.PublicID+`"} 1` {
		t.Fatalf("6 days ahead: %d snapshots, %v", store.alerts.snapshots, err)
	}
}

// TestGapScanErrors: a failed read stops the run with its error, and a failed write leaves the gauge set.
func TestGapScanErrors(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	scan, _ := newScan(store, c)
	sc, err := svc.Create(ctx, admin, gapInput("Main", "monday"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ListGapScanSchedules", "ListRotationLayers", "ListPeriodOverrides",
		"ListPageableInTeams", "GetOrganizationTeamLabel", "InAlertsTx"} {
		store.fail[name] = errBoom
		if err := scan.Scan(ctx, orgID); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	if gapSeries(t, sc.PublicID) != `muster_schedule_gap{schedule="`+sc.PublicID+`"} 1` {
		t.Errorf("the gauge after a failed write = %q", gapSeries(t, sc.PublicID))
	}
	store.alerts.builtin = false
	if err := scan.Scan(ctx, orgID); !errors.Is(err, internalalerts.ErrNoBuiltin) || store.alerts.snapshots != 0 {
		t.Errorf("without the built-in Integration = %v", err)
	}
	store.alerts.builtin = true
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 1 {
		t.Fatal(err)
	}
	store.alerts.process(t)
	store.alerts.open[0].Labels = []byte("{")
	if err := scan.Scan(ctx, orgID); err == nil {
		t.Error("unreadable open alerts were taken")
	}
}

// TestSameGap: an open alert was raised for the nearest gap when it has the same start, or, for a gap that began
// before the lookback, any start at or before the lookback's; another start or no start is another gap.
func TestSameGap(t *testing.T) {
	open := func(start string) internalalerts.Open {
		return internalalerts.Open{Annotations: map[string]string{"gap_start": start}}
	}
	for _, c := range []struct {
		open   internalalerts.Open
		start  time.Time
		before bool
		want   bool
	}{
		{open("2026-11-09T00:00:00Z"), utc("2026-11-09T00:00:00Z"), false, true},
		{open("2026-11-02T00:00:00Z"), utc("2026-11-04T00:00:00Z"), false, false},
		{open("2026-11-09T00:00:00Z"), utc("2026-11-02T00:00:00Z"), false, false},
		{open("2026-10-20T00:00:00Z"), utc("2026-10-21T00:00:00Z"), true, true},
		{open("2026-10-22T00:00:00Z"), utc("2026-10-21T00:00:00Z"), true, false},
		{internalalerts.Open{}, utc("2026-11-02T00:00:00Z"), false, false},
	} {
		if got := sameGap(c.open, gapOf{start: c.start, before: c.before}); got != c.want {
			t.Errorf("sameGap(%v, %v, %v) = %v", c.open, c.start, c.before, got)
		}
	}
}

// TestGapScanBegun: a gap found once it has begun is raised with when it began, in UTC also for a Schedule in another
// time zone, and stays as it is at the next scans; when that gap ends and another begins between two scans, the alert
// is raised again, as the same firing, with the start of the new one; a Schedule without Rotation layers is a gap from
// its creation, and one created before the lookback a gap from the lookback's start, which keeps the start it was
// raised with while the lookback moves.
func TestGapScanBegun(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	scan, log := newScan(store, c)
	in := gapInput("Berlin", "monday", "wednesday")
	in.TimeZone = ptr("Europe/Berlin")
	sc, err := svc.Create(ctx, admin, in)
	if err != nil {
		t.Fatal(err)
	}
	// Monday 2 November 00:00 in Berlin is Sunday 23:00 UTC; the scan first runs at 03:00 UTC, inside the gap.
	c.Set(utc("2026-11-02T03:00:00Z"))
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 1 {
		t.Fatalf("inside the gap: %d snapshots, %v", store.alerts.snapshots, err)
	}
	a := store.alerts.last(t)
	if a.Annotations["gap_start"] != "2026-11-01T23:00:00Z" ||
		!strings.Contains(a.Annotations["description"], "has a gap from 2026-11-01T23:00:00Z:") ||
		!strings.Contains(log.String(), `"gap_start":"2026-11-01T23:00:00Z"`) {
		t.Fatalf("raised %+v, logged %s", a, log.String())
	}
	store.alerts.process(t)
	c.Set(utc("2026-11-02T20:00:00Z"))
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 1 {
		t.Fatalf("later in the gap: %d snapshots, %v", store.alerts.snapshots, err)
	}
	// The scans of Tuesday are missed: on Wednesday the gap of Wednesday has begun, not the one of Monday.
	c.Set(utc("2026-11-04T05:00:00Z"))
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 2 {
		t.Fatalf("another gap: %d snapshots, %v", store.alerts.snapshots, err)
	}
	if b := store.alerts.last(t); b.Annotations["gap_start"] != "2026-11-03T23:00:00Z" || !b.StartsAt.Equal(a.StartsAt) {
		t.Errorf("raised again %+v", b)
	}
	store.alerts.process(t)
	if err := svc.Delete(ctx, admin, sc.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if err := scan.Scan(ctx, orgID); err != nil {
		t.Fatal(err)
	}
	store.alerts.process(t)

	empty, err := svc.Create(ctx, admin, Input{TeamID: teamOps, Name: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	if err := scan.Scan(ctx, orgID); err != nil {
		t.Fatal(err)
	}
	first := store.alerts.last(t)
	if first.Labels["schedule"] != empty.PublicID || first.Annotations["gap_start"] != empty.CreatedAt.Format(time.RFC3339) {
		t.Fatalf("an empty Schedule %+v, created %v", first, empty.CreatedAt)
	}
	store.alerts.process(t)
	old, err := svc.Create(ctx, admin, Input{TeamID: teamOps, Name: "Old", GapAlert: ptr(GapAlertOn)})
	if err != nil {
		t.Fatal(err)
	}
	store.schedule(old.PublicID).created = c.Now().AddDate(0, -1, 0)
	if err := scan.Scan(ctx, orgID); err != nil {
		t.Fatal(err)
	}
	if a := store.alerts.last(t); a.Labels["schedule"] != old.PublicID ||
		a.Annotations["gap_start"] != c.Now().Add(-GapView).UTC().Format(time.RFC3339) {
		t.Fatalf("a Schedule created before the lookback %+v", a)
	}
	raisedWith := store.alerts.last(t).Annotations["gap_start"]
	store.alerts.process(t)
	raised := store.alerts.snapshots
	c.Advance(time.Hour)
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != raised {
		t.Errorf("an hour later: %d snapshots, %v", store.alerts.snapshots, err)
	}
	// A rename raises it again with the start it was raised with, not the lookback's, which has moved on.
	rename := inputOf(old)
	rename.Name = "Older"
	if _, err := svc.Update(ctx, admin, old.PublicID, nil, rename); err != nil {
		t.Fatal(err)
	}
	c.Advance(time.Hour)
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != raised+1 {
		t.Fatalf("renamed: %d snapshots, %v", store.alerts.snapshots, err)
	}
	if a := store.alerts.last(t); a.Labels["schedule_name"] != "Older" || a.Annotations["gap_start"] != raisedWith {
		t.Errorf("renamed %+v, raised with %s", a, raisedWith)
	}
}

// TestGapScanForget: once the Leader stops, the scan removes every muster_schedule_gap series it exported, and the
// next scan exports them again.
func TestGapScanForget(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	scan, _ := newScan(store, c)
	sc, err := svc.Create(ctx, admin, gapInput("Main", "monday"))
	if err != nil {
		t.Fatal(err)
	}
	if err := scan.Scan(ctx, orgID); err != nil || gapSeries(t, sc.PublicID) == "" {
		t.Fatalf("scan: %v, series %q", err, gapSeries(t, sc.PublicID))
	}
	scan.Forget()
	if got := gapSeries(t, sc.PublicID); got != "" {
		t.Errorf("after Forget: %q", got)
	}
	scan.Forget()
	if err := scan.Scan(ctx, orgID); err != nil || gapSeries(t, sc.PublicID) == "" {
		t.Errorf("scan again: %v, series %q", err, gapSeries(t, sc.PublicID))
	}
	scan.Forget()
}

// TestGapScanFractions: a Schedule created at a fraction of a second, whose gap began then, and a gap that starts at the
// end of an Override with a fraction of a second are raised once, with their starts to the second, and rescans every
// schedule.gap_scan_interval write nothing more (ADR-0007).
func TestGapScanFractions(t *testing.T) {
	svc, store, c := newService(t)
	ctx := t.Context()
	scan, _ := newScan(store, c)
	c.Set(t0.Add(1500 * time.Millisecond))
	empty, err := svc.Create(ctx, admin, Input{TeamID: teamOps, Name: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	main, err := svc.Create(ctx, admin, gapInput("Main", "monday"))
	if err != nil {
		t.Fatal(err)
	}
	// The Override covers Monday until 06:00 and a quarter of a second: the gap starts then.
	if _, err := svc.CreateOverride(ctx, admin, main.PublicID, OverrideInput{UserID: olga.PublicID,
		Start: utc("2026-11-01T20:00:00Z"), End: utc("2026-11-02T06:00:00Z").Add(250 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 2 {
		t.Fatalf("first scan: %d snapshots, %v", store.alerts.snapshots, err)
	}
	starts := map[string]string{}
	for _, body := range store.alerts.pending {
		a := alertOf(t, body)
		starts[a.Labels["schedule"]] = a.Annotations["gap_start"]
	}
	if starts[empty.PublicID] != "2026-10-28T12:00:01Z" || starts[main.PublicID] != "2026-11-02T06:00:00Z" {
		t.Fatalf("starts %v", starts)
	}
	store.alerts.process(t)
	for range 3 {
		c.Advance(5 * time.Minute)
		if err := scan.Scan(ctx, orgID); err != nil || store.alerts.snapshots != 2 {
			t.Fatalf("a rescan at %v: %d snapshots, %v", c.Now(), store.alerts.snapshots, err)
		}
		store.alerts.process(t)
	}
}
