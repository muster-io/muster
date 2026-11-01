// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package schedules_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/ingest"
	ingestdb "github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/schedules"
	"github.com/muster-io/muster/internal/teams"
)

// noSink takes the Alert changes of processing and routes nothing: the scan's test reads the Alerts themselves.
type noSink struct{}

func (noSink) AlertChanges(context.Context, ingestdb.DBTX, []ingest.AlertChange) (ingest.Routed, error) {
	return ingest.Routed{}, nil
}

// scanEnv is env with the gap scan, the built-in Integration and the processing of its synthetic Snapshots.
type scanEnv struct {
	*env
	scan    *schedules.GapScan
	process *ingest.Processor
	log     *bytes.Buffer
}

func setupScan(t *testing.T, s dbtest.Server) *scanEnv {
	t.Helper()
	e := setup(t, s)
	ctx := t.Context()
	if err := integrations.EnsureBuiltin(ctx, integrations.NewStore(e.d.Pool), e.orgID, t0); err != nil {
		t.Fatal(err)
	}
	for day := t0.Truncate(24*time.Hour).AddDate(0, 0, -1); day.Before(t0.AddDate(0, 0, 40)); day = day.AddDate(0, 0,
		1) {
		next, name := day.AddDate(0, 0, 1), day.Format("20060102")
		for _, stmt := range []string{
			`CREATE TABLE stored_snapshots_p` + name + ` PARTITION OF stored_snapshots FOR VALUES FROM ('` +
				day.Format(time.RFC3339) + `') TO ('` + next.Format(time.RFC3339) + `')`,
			`CREATE TABLE snapshot_bodies_p` + name + ` PARTITION OF snapshot_bodies FOR VALUES FROM ('` +
				day.Format(time.DateOnly) + `') TO ('` + next.Format(time.DateOnly) + `')`,
		} {
			if _, err := e.d.Pool.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	var log bytes.Buffer
	logger := logging.New(&log, logging.LevelInfo)
	return &scanEnv{env: e, log: &log,
		scan: &schedules.GapScan{Store: schedules.NewStore(e.d.Pool), Business: e.clock, Log: logger},
		process: ingest.NewProcessor(ingest.ProcessorConfig{OrgID: e.orgID, Store: ingest.NewProcessStore(e.d.Pool),
			Business: e.clock, Log: logger, Sink: noSink{}, Lease: db.Lease{Owner: "test", Duration: ingest.Lease,
				Clocks: clock.Clocks{Business: e.clock, Real: clock.Real{}}}})}
}

// run moves the business clock on by a minute, as the scans every schedule.gap_scan_interval do, runs the scan and
// processes what it wrote. A raise at the very moment of a resolve of the same Alert would be taken for that firing.
func (e *scanEnv) run(t *testing.T) {
	t.Helper()
	e.clock.Advance(time.Minute)
	if err := e.scan.Scan(t.Context(), e.orgID); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		n, err := e.process.Drain(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("processing does not settle")
}

// gapAlert is an Alert MusterScheduleGap of the built-in Integration.
type gapAlert struct {
	Status      string
	Labels      map[string]string
	Annotations map[string]string
	StartsAt    time.Time
}

// alerts are the MusterScheduleGap Alerts, firing or resolved, by Schedule: the newest of each.
func (e *scanEnv) alerts(t *testing.T) map[string]gapAlert {
	t.Helper()
	rows, err := e.d.Pool.Query(t.Context(), `SELECT a.status, a.labels, a.annotations, a.starts_at
		FROM alerts a JOIN integrations i ON i.id = a.integration_id AND i.builtin
		WHERE a.labels->>'alertname' = 'MusterScheduleGap' ORDER BY a.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]gapAlert{}
	for rows.Next() {
		var a gapAlert
		var labels, annotations []byte
		if err := rows.Scan(&a.Status, &labels, &annotations, &a.StartsAt); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(labels, &a.Labels); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(annotations, &a.Annotations); err != nil {
			t.Fatal(err)
		}
		out[a.Labels["schedule"]] = a
	}
	return out
}

// raises counts the synthetic Stored Snapshots of MusterScheduleGap.
func (e *scanEnv) raises(t *testing.T) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM stored_snapshots s
		JOIN snapshot_bodies b ON b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
		WHERE s.source = 'internal' AND convert_from(b.body, 'UTF8')::jsonb->'groupLabels'->>'alertname' = 'MusterScheduleGap'`)
}

func gauge(t *testing.T, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(func() bool { return true }).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, "/metrics", nil))
	for line := range strings.Lines(rec.Body.String()) {
		if strings.HasPrefix(line, `muster_schedule_gap{schedule="`+id+`"}`) {
			return strings.TrimSpace(strings.TrimPrefix(line, `muster_schedule_gap{schedule="`+id+`"}`))
		}
	}
	return ""
}

// gapInput is a Schedule of payments in UTC whose only Rotation layer has olga on every day but those left out, from
// 28 October 2026.
func (e *env) gapInput(name string, left ...string) schedules.Input {
	var days []string
	for _, d := range schedules.Weekdays {
		if !strings.Contains(strings.Join(left, ","), d) {
			days = append(days, d)
		}
	}
	return schedules.Input{TeamID: e.team.PublicID, Name: name, TimeZone: ptr("UTC"),
		RotationLayers: []schedules.RotationLayerInput{{Name: "Days", UserIDs: []string{e.people["olga"]},
			ShiftLength: schedules.ShiftDay, HandoverTime: "00:00",
			StartDate: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), Limits: &schedules.Limits{Days: days}}}}
}

// asOlga is olga making Overrides as a person of the Team; an Override needs a User who made it.
func (e *env) asOlga(t *testing.T) schedules.Requester {
	t.Helper()
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT id FROM users WHERE login = 'olga'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return schedules.Requester{Actor: audit.User(id, e.people["olga"]), Transport: audit.TransportAPI}
}

func utcAt(s string) time.Time {
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return at
}

// TestIntegrationGapScan is C-27.FR-7, C-27.AC-5 and C-25.FR-5 on PostgreSQL, with processing: a gap 5 days ahead in
// the Team's main Schedule raises one MusterScheduleGap, with the Schedule and its Team by id and name, the Team's slug
// under the Team label and the start of the gap, and muster_schedule_gap 1; a second run raises nothing; a second
// Schedule of the Team with a gap exports 0 and raises nothing until its gap alert is on; an Override that closes the
// gap resolves it; a gap 8 days ahead raises nothing, 6 days ahead it raises; once the gap has started it stays raised,
// and its end resolves it.
func TestIntegrationGapScan(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupScan(t, s)
		ctx := t.Context()
		// Monday 2 November is left out: 4 days 12 hours from Wednesday noon, the gap 5 days ahead of the story.
		main, err := e.svc.Create(ctx, admin, e.gapInput("Main", "monday"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := e.svc.Create(ctx, admin, e.gapInput("Second", "monday"))
		if err != nil {
			t.Fatal(err)
		}
		e.run(t)
		got := e.alerts(t)
		a := got[main.PublicID]
		want := map[string]string{"alertname": "MusterScheduleGap", "severity": "warning", "schedule": main.PublicID,
			"schedule_name": "Main", "team_id": e.team.PublicID, "team_name": "Payments", "team": "payments"}
		if len(got) != 1 || a.Status != "firing" || len(a.Labels) != len(want) || a.Annotations["gap_start"] !=
			"2026-11-02T00:00:00Z" || !strings.Contains(a.Annotations["description"], "has a gap from 2026-11-02T00:00:00Z") {
			t.Fatalf("raised %+v", got)
		}
		for k, v := range want {
			if a.Labels[k] != v {
				t.Errorf("label %s = %q, want %q", k, a.Labels[k], v)
			}
		}
		if gauge(t, main.PublicID) != "1" || gauge(t, second.PublicID) != "0" {
			t.Errorf("gauges %q %q", gauge(t, main.PublicID), gauge(t, second.PublicID))
		}
		e.run(t)
		if n := e.raises(t); n != 1 {
			t.Errorf("two runs raised %d times", n)
		}

		// The second Schedule raises once its gap alert is on.
		in := inputFor(second)
		in.GapAlert = ptr(schedules.GapAlertOn)
		if second, err = e.svc.Update(ctx, admin, second.PublicID, nil, in); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if got := e.alerts(t); got[second.PublicID].Status != "firing" || gauge(t, second.PublicID) != "1" {
			t.Fatalf("the second Schedule on: %+v", got)
		}
		in.GapAlert = ptr(schedules.GapAlertOff)
		if second, err = e.svc.Update(ctx, admin, second.PublicID, nil, in); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if got := e.alerts(t); got[second.PublicID].Status != "resolved" || gauge(t, second.PublicID) != "0" {
			t.Fatalf("the second Schedule off: %+v", got)
		}

		// An Override over Monday closes the gap: the next one, Monday 9 November, is 11 days 12 hours ahead.
		if _, err := e.svc.CreateOverride(ctx, e.asOlga(t), main.PublicID, schedules.OverrideInput{
			UserID: e.people["olga"], Start: utcAt("2026-11-02T00:00:00Z"),
			End: utcAt("2026-11-03T00:00:00Z")}); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if got := e.alerts(t); got[main.PublicID].Status != "resolved" || gauge(t, main.PublicID) != "0" {
			t.Fatalf("closed by an Override: %+v", got)
		}
		if !strings.Contains(e.log.String(), `"event":"schedule_gap_cleared","schedule":"`+main.PublicID+`"`) {
			t.Errorf("logged %s", e.log.String())
		}
		// Monday 16 November is closed too, so that the gap after the next one is beyond the horizon.
		e.clock.Set(utcAt("2026-11-01T00:00:00Z"))
		if _, err := e.svc.CreateOverride(ctx, e.asOlga(t), main.PublicID, schedules.OverrideInput{
			UserID: e.people["olga"], Start: utcAt("2026-11-16T00:00:00Z"),
			End: utcAt("2026-11-17T00:00:00Z")}); err != nil {
			t.Fatal(err)
		}
		e.clock.Set(utcAt("2026-11-01T01:00:00Z")) // 8 days less an hour before 9 November
		e.run(t)
		if got := e.alerts(t); got[main.PublicID].Status != "resolved" {
			t.Fatalf("8 days ahead: %+v", got)
		}
		e.clock.Set(utcAt("2026-11-03T00:00:00Z")) // 6 days before
		e.run(t)
		if a := e.alerts(t)[main.PublicID]; a.Status != "firing" || a.Annotations["gap_start"] != "2026-11-09T00:00:00Z" {
			t.Fatalf("6 days ahead: %+v", a)
		}
		raised := e.raises(t)
		e.clock.Set(utcAt("2026-11-09T02:00:00Z"))
		e.run(t)
		if a := e.alerts(t)[main.PublicID]; a.Status != "firing" || e.raises(t) != raised {
			t.Fatalf("inside the gap: %+v after %d raises", a, e.raises(t))
		}
		e.clock.Set(utcAt("2026-11-10T01:00:00Z")) // past it; the next gap is on 23 November
		e.run(t)
		if a := e.alerts(t)[main.PublicID]; a.Status != "resolved" || gauge(t, main.PublicID) != "0" {
			t.Fatalf("past the gap: %+v", a)
		}
	})
}

// TestIntegrationGapScanMain is C-27.AC-8 on PostgreSQL: with by_default on two Schedules of a Team, making the second
// one main moves the gap alert to it at the next run and resolves the first one's; with on on the first one it stays
// raised there.
func TestIntegrationGapScanMain(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupScan(t, s)
		ctx := t.Context()
		first, err := e.svc.Create(ctx, admin, e.gapInput("First", "monday"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := e.svc.Create(ctx, admin, e.gapInput("Second", "monday"))
		if err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if got := e.alerts(t); len(got) != 1 || got[first.PublicID].Status != "firing" {
			t.Fatalf("first main: %+v", got)
		}
		in := inputFor(second)
		in.Main = ptr(true)
		if _, err := e.svc.Update(ctx, admin, second.PublicID, nil, in); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if got := e.alerts(t); got[first.PublicID].Status != "resolved" || got[second.PublicID].Status != "firing" {
			t.Fatalf("second main: %+v", got)
		}
		if first, err = e.svc.Get(ctx, first.PublicID); err != nil {
			t.Fatal(err)
		}
		in = inputFor(first)
		in.GapAlert = ptr(schedules.GapAlertOn)
		if _, err := e.svc.Update(ctx, admin, first.PublicID, nil, in); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if got := e.alerts(t); got[first.PublicID].Status != "firing" || got[second.PublicID].Status != "firing" {
			t.Fatalf("first on: %+v", got)
		}
		// The first one, on, stays raised when the main mark moves back to it and away again.
		in = inputFor(first)
		in.Main = ptr(true)
		if _, err := e.svc.Update(ctx, admin, first.PublicID, nil, in); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if got := e.alerts(t); got[first.PublicID].Status != "firing" || got[second.PublicID].Status != "resolved" {
			t.Fatalf("first main again: %+v", got)
		}
	})
}

// TestIntegrationGapScanNames is C-27.FR-7 on PostgreSQL: renaming the Schedule or its Team updates the name labels of
// the open MusterScheduleGap, and a changed slug or Team label its Team label, without a second Alert and with the
// same start; deleting the Schedule resolves it and removes its series.
func TestIntegrationGapScanNames(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setupScan(t, s)
		ctx := t.Context()
		sc, err := e.svc.Create(ctx, admin, e.gapInput("Main", "monday"))
		if err != nil {
			t.Fatal(err)
		}
		e.run(t)
		started := e.alerts(t)[sc.PublicID].StartsAt
		in := inputFor(sc)
		in.Name = "Primary"
		if sc, err = e.svc.Update(ctx, admin, sc.PublicID, nil, in); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Hour)
		e.run(t)
		if a := e.alerts(t)[sc.PublicID]; a.Labels["schedule_name"] != "Primary" || !a.StartsAt.Equal(started) {
			t.Errorf("renamed Schedule: %+v", a)
		}
		if _, err := e.teams.Update(ctx, teamAdmin, e.team.PublicID, nil, teams.Input{Name: "Payments EU",
			Slug: "payments-eu"}); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Hour)
		e.run(t)
		if a := e.alerts(t)[sc.PublicID]; a.Labels["team_name"] != "Payments EU" || a.Labels["team"] != "payments-eu" ||
			!strings.Contains(a.Annotations["summary"], "of Payments EU") {
			t.Errorf("renamed Team: %+v", a)
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE organizations SET team_label = 'owner' WHERE id = $1`,
			e.orgID); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Hour)
		e.run(t)
		if a := e.alerts(t)[sc.PublicID]; a.Labels["owner"] != "payments-eu" || a.Labels["team"] != "" ||
			a.Status != "firing" || !a.StartsAt.Equal(started) {
			t.Errorf("another Team label: %+v", a)
		}
		raised := e.raises(t)
		e.run(t)
		if e.raises(t) != raised {
			t.Error("a run without a change raised again")
		}
		if n := e.count(t, `SELECT count(*) FROM alerts WHERE labels->>'schedule' = $1`, sc.PublicID); n != 1 {
			t.Errorf("%d Alerts of the Schedule, want 1", n)
		}
		if err := e.svc.Delete(ctx, admin, sc.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if a := e.alerts(t)[sc.PublicID]; a.Status != "resolved" || gauge(t, sc.PublicID) != "" {
			t.Errorf("deleted: %+v, gauge %q", a, gauge(t, sc.PublicID))
		}
		if !strings.Contains(e.log.String(), `"event":"schedule_gap_cleared","schedule":"`+sc.PublicID+`","team":"`+
			e.team.PublicID+`"`) {
			t.Errorf("logged %s", e.log.String())
		}
	})
}

// inputFor is the input that saves sc as it is.
func inputFor(sc schedules.Schedule) schedules.Input {
	in := schedules.Input{TeamID: sc.Team.PublicID, Name: sc.Name, TimeZone: ptr(sc.TimeZone)}
	for _, l := range sc.RotationLayers {
		li := schedules.RotationLayerInput{ID: l.PublicID, Name: l.Name, ShiftLength: l.ShiftLength,
			HandoverTime: l.HandoverTime, HandoverDay: l.HandoverDay, StartDate: l.StartDate, Limits: l.Limits}
		if l.ShiftLength == schedules.ShiftHours {
			li.ShiftSeconds = ptr(l.ShiftSeconds)
		}
		for _, u := range l.Users {
			li.UserIDs = append(li.UserIDs, u.PublicID)
		}
		in.RotationLayers = append(in.RotationLayers, li)
	}
	return in
}
