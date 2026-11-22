// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package groups_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/ingest"
	ingestdb "github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/teams"
)

var teamsBy = teams.Requester{Actor: audit.System, Transport: audit.TransportSystem}

// teamsOf is the Teams service of the env's Organization.
func (e *env) teamsOf() *teams.Service {
	return teams.New(teams.Config{OrgID: e.orgID, Store: teams.NewStore(e.d.Pool),
		Audit: audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), e.clock), Business: e.clock})
}

// alertTeams are the Teams routing recorded on the Alert with the label pod=value, as slug/rule, and the value of its
// Team label that named no Team, empty for none.
func (e *env) alertTeams(t *testing.T, pod string) ([]string, string) {
	t.Helper()
	rows, err := e.d.Pool.Query(t.Context(), `SELECT n.slug || '/' || t.rule FROM alert_teams t
		JOIN alerts a ON a.id = t.alert_id JOIN teams n ON n.id = t.team_id
		WHERE a.org_id = $1 AND a.labels->>'pod' = $2 ORDER BY n.slug`, e.orgID, pod)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var unknown string
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT coalesce(unknown_team_value, '') FROM alerts
		WHERE org_id = $1 AND labels->>'pod' = $2`, e.orgID, pod).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	return out, unknown
}

// groupTeams are the Teams of the Alert Group of the Alert with the label pod=value as slug/rule, and its recorded
// values of the Team label that named no Team.
func (e *env) groupTeams(t *testing.T, pod string) ([]string, []string) {
	t.Helper()
	v, err := e.groups.Get(t.Context(), e.groupOf(t, pod))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, gt := range v.Teams {
		out = append(out, gt.Team.Slug+"/"+gt.Rule)
	}
	var values []string
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT unknown_team_values FROM alert_groups
		WHERE org_id = $1 AND public_id = $2`, e.orgID, v.PublicID).Scan(&values); err != nil {
		t.Fatal(err)
	}
	return out, values
}

// TestIntegrationSnapshotTeams is C-25.FR-5, FR-6 and FR-7 through Snapshot processing on every supported PostgreSQL,
// with Teams by the label and by a Team matcher, a value of the Team label that names no Team and an Alert without
// any: the first firings record their Teams and values and bring them to their Alert Group, an Alert Group of Alerts
// without Teams has none, and the firings after a resolve replace what the previous firing recorded, a Team since
// deleted becoming a value that names no Team and a value since a Team becoming that Team.
func TestIntegrationSnapshotTeams(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		ts := e.teamsOf()
		payments, err := ts.Create(ctx, teamsBy, teams.Input{Name: "Payments", Slug: "payments"})
		if err != nil {
			t.Fatal(err)
		}
		ms := []teams.Matcher{{Label: "namespace", Op: "=~", Value: "checkout|billing"}}
		if _, err := ts.Create(ctx, teamsBy, teams.Input{Name: "Checkout", Slug: "checkout", Matchers: &ms}); err != nil {
			t.Fatal(err)
		}
		e.route(t, "db", "db", "alertname")
		const s1, s2 = "2026-10-07T11:00:00Z", "2026-10-07T11:30:00Z"
		e.process(t, gk, alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "p1", "muster_team",
			"payments"),
			alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "p2", "namespace", "billing"),
			alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "p3", "muster_team", "nosuch"),
			alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "p4"))
		e.process(t, `{}:{alertname=\"Quiet\"}`, alert("firing", s1, "alertname", "Quiet", "team", "db", "pod", "q1"))
		for pod, want := range map[string]struct {
			teams   []string
			unknown string
		}{"p1": {[]string{"payments/label"}, ""}, "p2": {[]string{"checkout/matcher"}, ""}, "p3": {nil, "nosuch"},
			"p4": {nil, ""}, "q1": {nil, ""}} {
			if got, unknown := e.alertTeams(t, pod); !slices.Equal(got, want.teams) || unknown != want.unknown {
				t.Errorf("%s: teams %v, value %q", pod, got, unknown)
			}
		}
		g := e.groupOf(t, "p1")
		if got, values := e.groupTeams(t, "p1"); !slices.Equal(got, []string{"payments/label", "checkout/matcher"}) ||
			!slices.Equal(values, []string{"nosuch"}) {
			t.Errorf("teams of the alert group %v, values %v", got, values)
		}
		if ev := e.events(t, g); !slices.Equal(ev, []string{"created", "team_joined", "team_joined",
			"unknown_team_value"}) {
			t.Errorf("events %v", ev)
		}
		if got, values := e.groupTeams(t, "q1"); got != nil || len(values) != 0 ||
			!slices.Equal(e.events(t, e.groupOf(t, "q1")), []string{"created"}) {
			t.Errorf("an alert group without teams: %v, values %v, events %v", got, values,
				e.events(t, e.groupOf(t, "q1")))
		}

		e.clock.Advance(time.Minute)
		e.process(t, gk, alert("resolved", s1, "alertname", "DiskFull", "team", "db", "pod", "p1", "muster_team",
			"payments"),
			alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "p2", "namespace", "billing"),
			alert("resolved", s1, "alertname", "DiskFull", "team", "db", "pod", "p3", "muster_team", "nosuch"),
			alert("resolved", s1, "alertname", "DiskFull", "team", "db", "pod", "p4"))
		if err := ts.Delete(ctx, teamsBy, payments.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := ts.Create(ctx, teamsBy, teams.Input{Name: "No such", Slug: "nosuch"}); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
		e.process(t, gk, alert("firing", s2, "alertname", "DiskFull", "team", "db", "pod", "p1", "muster_team",
			"payments"),
			alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "p2", "namespace", "billing"),
			alert("firing", s2, "alertname", "DiskFull", "team", "db", "pod", "p3", "muster_team", "nosuch"),
			alert("firing", s2, "alertname", "DiskFull", "team", "db", "pod", "p4"))
		for pod, want := range map[string]struct {
			teams   []string
			unknown string
		}{"p1": {nil, "payments"}, "p2": {[]string{"checkout/matcher"}, ""}, "p3": {[]string{"nosuch/label"}, ""},
			"p4": {nil, ""}} {
			if got, unknown := e.alertTeams(t, pod); !slices.Equal(got, want.teams) || unknown != want.unknown {
				t.Errorf("after the firings again, %s: teams %v, value %q", pod, got, unknown)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM alerts WHERE org_id = $1 AND episode = 2`, e.orgID); n != 3 {
			t.Errorf("%d alerts fired again", n)
		}
		if got, values := e.groupTeams(t, "p3"); e.groupOf(t, "p3") != g ||
			!slices.Equal(got, []string{"payments/label", "checkout/matcher", "nosuch/label"}) ||
			!slices.Equal(values, []string{"nosuch", "payments"}) {
			t.Errorf("teams of the alert group after the firings again %v, values %v", got, values)
		}
	})
}

// pausing is a Sink between routing and grouping that holds the Snapshot's transaction until released.
type pausing struct {
	reached, release chan struct{}
}

func (p pausing) AlertChanges(context.Context, ingestdb.DBTX, []ingest.AlertChange) (ingest.Routed, error) {
	close(p.reached)
	<-p.release
	return ingest.Routed{}, nil
}

// TestIntegrationSnapshotTeamCreatedInFlight is C-25.FR-13 against the race with a Snapshot on every supported
// PostgreSQL, through routing and grouping: an Alert fires in a Snapshot that routing classified with no Team and no
// value, so grouping reads no Teams for it; a Team whose matcher takes the Alert is created while the Snapshot is
// between routing and grouping, and its evaluation waits for the Snapshot, after which the Alert and its Alert Group
// have the Team.
func TestIntegrationSnapshotTeamCreatedInFlight(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.route(t, "db", "db", "alertname")
		body := `{"groupKey":"` + gk + `","status":"firing","alerts":[` + alert("firing", "2026-10-07T11:00:00Z",
			"alertname", "DiskFull", "team", "db", "pod", "p1", "namespace", "billing") + `]}`
		if _, err := e.snaps.Store(ctx, ingest.Received{IntegrationID: e.intID, Body: []byte(body)}); err != nil {
			t.Fatal(err)
		}
		p := pausing{reached: make(chan struct{}), release: make(chan struct{})}
		processor := ingest.NewProcessor(ingest.ProcessorConfig{OrgID: e.orgID, Store: ingest.NewProcessStore(e.d.Pool),
			Business: e.clock, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo),
			Sink: ingest.Chain(e.router, p, e.groups), Lease: db.Lease{Owner: "r1", Duration: ingest.Lease,
				Clocks: clock.Clocks{Business: e.clock, Real: clock.Real{}}}})
		processed := make(chan error, 1)
		go func() {
			_, err := processor.Drain(context.WithoutCancel(ctx))
			processed <- err
		}()
		select {
		case <-p.reached:
		case err := <-processed:
			t.Fatalf("the snapshot did not reach grouping: %v", err)
		}
		released := false
		defer func() {
			if !released {
				close(p.release)
			}
		}()
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ms := []teams.Matcher{{Label: "namespace", Op: "=", Value: "billing"}}
		billing, err := e.teamsOf().Create(cctx, teamsBy, teams.Input{Name: "Billing", Slug: "billing", Matchers: &ms})
		if err != nil {
			t.Fatal(err)
		}
		logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		worker := &groups.UpdateWorker{Service: e.groups, Log: logger, Lease: db.Lease{Owner: "r1",
			Duration: groups.UpdateLease, Clocks: clock.Clocks{Business: e.clock, Real: clock.Real{}}}}
		evaluated := make(chan error, 1)
		go func() {
			_, err := worker.Round(context.WithoutCancel(ctx))
			evaluated <- err
		}()
		deadline := time.Now().Add(10 * time.Second)
		for e.count(t, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock' AND wait_event = 'advisory'`) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the evaluation does not wait for the snapshot")
			}
			time.Sleep(10 * time.Millisecond)
		}
		released = true
		close(p.release)
		if err := <-processed; err != nil {
			t.Fatal(err)
		}
		if err := <-evaluated; err != nil {
			t.Fatal(err)
		}
		if got, unknown := e.alertTeams(t, "p1"); !slices.Equal(got, []string{"billing/matcher"}) || unknown != "" {
			t.Errorf("teams of the alert %v, value %q", got, unknown)
		}
		if got, _ := e.groupTeams(t, "p1"); !slices.Equal(got, []string{"billing/matcher"}) ||
			e.count(t, `SELECT count(*) FROM timeline_entries WHERE org_id = $1 AND event = 'team_joined'
				AND team_id = $2`, e.orgID, billing.ID) != 1 {
			t.Errorf("teams of the alert group %v", got)
		}
	})
}
