// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package groups_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/ingest"
	ingestdb "github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/leader"
	leaderdb "github.com/muster-io/muster/internal/leader/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/timers"
	timersdb "github.com/muster-io/muster/internal/timers/dbgen"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

type env struct {
	d      *db.DB
	clock  *clock.Manual
	orgID  int64
	log    *bytes.Buffer
	routes *routing.Service
	router *routing.Router
	groups *groups.Service
	sink   ingest.Sink
	snaps  *ingest.Service
	ints   *integrations.Service
	intID  int64
}

var by = routing.Requester{Actor: audit.System, Transport: audit.TransportSystem}

func setup(t *testing.T, s dbtest.Server) *env {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	var log bytes.Buffer
	logger := logging.New(&log, logging.LevelInfo)
	if err := d.Migrate(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE audit_log_p202610 PARTITION OF audit_log
			FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`,
		`CREATE TABLE timeline_entries_p202610 PARTITION OF timeline_entries
			FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`,
		`CREATE TABLE delivery_events_p202610 PARTITION OF delivery_events
			FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`,
	}
	for day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); day.Before(time.Date(2026, 10, 12, 0, 0, 0, 0,
		time.UTC)); day = day.AddDate(0, 0, 1) {
		next, name := day.AddDate(0, 0, 1), day.Format("20060102")
		stmts = append(stmts,
			`CREATE TABLE stored_snapshots_p`+name+` PARTITION OF stored_snapshots FOR VALUES FROM ('`+
				day.Format(time.RFC3339)+`') TO ('`+next.Format(time.RFC3339)+`')`,
			`CREATE TABLE snapshot_bodies_p`+name+` PARTITION OF snapshot_bodies FOR VALUES FROM ('`+
				day.Format(time.DateOnly)+`') TO ('`+next.Format(time.DateOnly)+`')`)
	}
	for _, stmt := range stmts {
		if _, err := d.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := integrations.EnsureBuiltin(ctx, integrations.NewStore(d.Pool), org.ID, t0); err != nil {
		t.Fatal(err)
	}
	store := routing.NewStore(d.Pool)
	if err := routing.EnsureDefault(ctx, store, org.ID, t0); err != nil {
		t.Fatal(err)
	}
	c := clock.NewManual(t0)
	w := audit.NewWriter(logger, c)
	e := &env{d: d, clock: c, orgID: org.ID, log: &log, router: routing.NewRouter(org.ID)}
	e.routes = routing.New(routing.Config{OrgID: org.ID, Store: store, Audit: w, Business: c, Real: clock.Real{},
		Router: e.router, Log: logger})
	e.groups = groups.New(groups.Config{OrgID: org.ID, Store: groups.NewStore(d.Pool), Audit: w, Business: c,
		Log: logger, Restamp: func(ctx context.Context, tx groups.DBTX, ids []int64, routeID int64) error {
			return routing.RestampAlerts(ctx, tx, org.ID, ids, routeID)
		}})
	e.sink = ingest.Chain(e.router, e.groups)
	e.snaps = ingest.New(org.ID, ingest.NewStore(d.Pool), c)
	e.ints = integrations.New(integrations.Config{OrgID: org.ID, Store: integrations.NewStore(d.Pool), Audit: w,
		Business: c})
	in, err := e.ints.Create(ctx, integrations.Requester{Actor: audit.System, Transport: audit.TransportSystem},
		integrations.Input{Name: "lab", ConnectionMode: "webhook_only", DuplicateWindowSeconds: 45})
	if err != nil {
		t.Fatal(err)
	}
	e.intID = in.ID
	return e
}

// route creates a Route for team=value with the Group key and the On-call policy, and returns its public_id.
func (e *env) route(t *testing.T, name, team string, key ...string) routing.Route {
	t.Helper()
	p := routing.Profiles()[0].Policy
	r, err := e.routes.Create(t.Context(), by, routing.Input{Name: name, GroupKey: key, Policy: p,
		Matchers: []routing.Matcher{{Label: "team", Op: "=", Value: team}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// alert is a webhook Alert with the labels, firing or resolved, starting at startsAt.
func alert(status, startsAt string, labels ...string) string {
	var pairs []string
	for i := 0; i+1 < len(labels); i += 2 {
		pairs = append(pairs, fmt.Sprintf("%q:%q", labels[i], labels[i+1]))
	}
	return `{"status":"` + status + `","labels":{` + strings.Join(pairs, ",") + `},"annotations":{},"startsAt":"` +
		startsAt + `"}`
}

// snapshot stores a Snapshot of the groupKey listing the Alerts and processes it through routing and grouping.
func (e *env) snapshot(t *testing.T, sink ingest.Sink, groupKey string, alerts ...string) error {
	t.Helper()
	body := `{"groupKey":"` + groupKey + `","status":"firing","alerts":[` + strings.Join(alerts, ",") + `]}`
	if _, err := e.snaps.Store(t.Context(), ingest.Received{IntegrationID: e.intID, Body: []byte(body)}); err != nil {
		t.Fatal(err)
	}
	p := ingest.NewProcessor(ingest.ProcessorConfig{OrgID: e.orgID, Store: ingest.NewProcessStore(e.d.Pool),
		Business: e.clock, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo), Sink: sink,
		Lease: db.Lease{Owner: "r1", Duration: ingest.Lease, Clocks: clock.Clocks{Business: e.clock,
			Real: clock.Real{}}}})
	_, err := p.Drain(t.Context())
	return err
}

func (e *env) process(t *testing.T, groupKey string, alerts ...string) {
	t.Helper()
	if err := e.snapshot(t, e.sink, groupKey, alerts...); err != nil {
		t.Fatal(err)
	}
}

// groupOf is the public_id of the Alert Group of the Alert with the label pod=value, as the Alerts view names it.
func (e *env) groupOf(t *testing.T, pod string) string {
	t.Helper()
	var id string
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT g.public_id FROM alert_group_alerts m
		JOIN alerts a ON a.id = m.alert_id JOIN alert_groups g ON g.id = m.alert_group_id
		WHERE a.labels->>'pod' = $1 ORDER BY m.id DESC LIMIT 1`, pod).Scan(&id); err != nil {
		t.Fatalf("the alert group of %s: %v", pod, err)
	}
	return id
}

func (e *env) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// events are the lifecycle or system events of an Alert Group, oldest first.
func (e *env) events(t *testing.T, id string) []string {
	t.Helper()
	page, err := e.groups.Timeline(t.Context(), id, groups.TimelineFilter{Ascending: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range page.Entries {
		if x.Event != "" {
			out = append(out, x.Event)
		} else {
			out = append(out, x.System)
		}
	}
	return out
}

const gk = `{}:{alertname=\"DiskFull\"}`

// TestIntegrationLifecycle is the lifecycle on PostgreSQL through Snapshot processing: grouping by Route and Group
// key (C-09.AC-6), a Replacement (C-09.AC-4), a Continuation (C-09.AC-10), a rise to Urgent (C-09.AC-12), a
// resolution by the system and a Reopen by another fingerprint (C-09.AC-1, AC-22), a new #N after the window
// (C-09.AC-2), the title switch (C-09.AC-20) and the reads.
func TestIntegrationLifecycle(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.route(t, "db", "db", "alertname", "cluster")
		e.route(t, "net", "net", "cluster")
		const s1, s2 = "2026-10-07T11:00:00Z", "2026-10-07T11:30:00Z"
		i1 := alert("firing", s1, "alertname", "DiskFull", "team", "db", "cluster", "a", "pod", "i1", "severity",
			"warning")
		i2 := alert("firing", s1, "alertname", "DiskFull", "team", "db", "cluster", "a", "pod", "i2", "severity",
			"warning")
		j1 := alert("firing", s1, "alertname", "DiskFull", "team", "db", "cluster", "b", "pod", "j1", "severity",
			"warning")
		e.process(t, gk, i1, i2, j1)
		g1, g2 := e.groupOf(t, "i1"), e.groupOf(t, "j1")
		if e.groupOf(t, "i2") != g1 || g1 == g2 {
			t.Fatalf("groups %s %s %s", g1, e.groupOf(t, "i2"), g2)
		}
		v, err := e.groups.Get(ctx, g1)
		if err != nil || v.Number != 1 || v.Title != "DiskFull" || v.Status != groups.StatusFiring ||
			v.Route.Name != "db" || v.FiringCount != 2 || len(v.Integrations) != 1 || v.Integrations[0].Name != "lab" ||
			v.GroupLabels["cluster"] != "a" {
			t.Fatalf("g1 = %+v, %v", v, err)
		}
		// A Replacement while i2 is still firing (missed, not Gone yet), then a Continuation and a rise.
		e.clock.Advance(time.Minute)
		i2b := alert("firing", s1, "alertname", "DiskFull", "team", "db", "cluster", "a", "pod", "i2b", "severity",
			"warning")
		e.process(t, gk, i1, i2b, j1)
		e.clock.Advance(time.Minute)
		i1c := alert("firing", s2, "alertname", "DiskFull", "team", "db", "cluster", "a", "pod", "i1", "severity",
			"warning")
		e.process(t, gk, i1c, i2b, j1)
		e.clock.Advance(time.Minute)
		i3 := alert("firing", s2, "alertname", "DiskFull", "team", "db", "cluster", "a", "pod", "i3", "severity",
			"critical")
		e.process(t, gk, i1c, i2b, j1, i3)
		if got := e.events(t, g1); !slices.Equal(got, []string{"created", "alert_replaced", "alert_continued",
			"alerts_added", "urgency_raised"}) {
			t.Errorf("g1 events %v", got)
		}
		if v, _ := e.groups.Get(ctx, g1); v.Severity != "critical" || !v.Urgent || v.Status != groups.StatusFiring ||
			len(v.Notices) != 1 || *v.Notices[0].Label != "pod" {
			t.Errorf("g1 after the rise %+v", v)
		}
		// Resolve j1, then fire it again within the window, then another fingerprint.
		e.clock.Advance(time.Minute)
		e.process(t, gk, i1c, i2b, i3, alert("resolved", s1, "alertname", "DiskFull", "team", "db", "cluster", "b",
			"pod", "j1", "severity", "warning"))
		if v, _ := e.groups.Get(ctx, g2); v.Status != groups.StatusResolved || v.Resolution == nil ||
			*v.Resolution.ReasonCode != "resolved" {
			t.Fatalf("g2 = %+v", v)
		}
		e.clock.Advance(8 * time.Minute)
		e.process(t, gk, i1c, i2b, i3, alert("firing", s2, "alertname", "DiskFull", "team", "db", "cluster", "b",
			"pod", "j1", "severity", "warning"))
		if v, _ := e.groups.Get(ctx, g2); v.Status != groups.StatusFiring || v.ReopenCount != 1 || v.Number != 2 {
			t.Errorf("reopened g2 = %+v", v)
		}
		e.process(t, gk, i1c, i2b, i3, alert("resolved", s2, "alertname", "DiskFull", "team", "db", "cluster", "b",
			"pod", "j1", "severity", "warning"))
		e.clock.Advance(time.Minute)
		e.process(t, gk, i1c, i2b, i3, alert("firing", s2, "alertname", "DiskFull", "team", "db", "cluster", "b",
			"pod", "j2", "severity", "warning"))
		if e.groupOf(t, "j2") != g2 {
			t.Error("another fingerprint did not reopen g2")
		}
		e.process(t, gk, i1c, i2b, i3, alert("resolved", s2, "alertname", "DiskFull", "team", "db", "cluster", "b",
			"pod", "j2", "severity", "warning"))
		e.clock.Advance(16 * time.Minute)
		e.process(t, gk, i1c, i2b, i3, alert("firing", s2, "alertname", "DiskFull", "team", "db", "cluster", "b",
			"pod", "j3", "severity", "warning"))
		if g3 := e.groupOf(t, "j3"); g3 == g2 {
			t.Error("reopened after the window")
		}
		// The title switches once in a Route whose key lacks alertname.
		e.process(t, `{}:{alertname=\"LinkDown\"}`, alert("firing", s1, "alertname", "LinkDown", "team", "net",
			"cluster", "x", "pod", "l1"))
		e.process(t, `{}:{alertname=\"HighLatency\"}`, alert("firing", s1, "alertname", "HighLatency", "team", "net",
			"cluster", "x", "pod", "h1"))
		if v, _ := e.groups.Get(ctx, e.groupOf(t, "l1")); v.Title != "cluster=x" {
			t.Errorf("title %q", v.Title)
		}
		// The reads: the Alerts of g1, firing first, with their groupKeys, and the Alerts view.
		page, err := e.groups.Alerts(ctx, g1, groups.AlertFilter{Limit: 10})
		if err != nil || len(page.Alerts) != 4 || !page.Alerts[0].Firing || len(page.Alerts[0].GroupKeys) != 1 {
			t.Errorf("alerts %+v, %v", page, err)
		}
		view := ingest.NewAlertsView(e.orgID, ingest.NewProcessStore(e.d.Pool), e.clock)
		alerts, err := view.List(ctx, ingest.AlertFilter{Integration: e.integrationPublicID(t), Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range alerts.Alerts {
			if a.AlertGroup == nil || a.AlertGroup.PublicID == "" {
				t.Errorf("alert %s has no alert group", a.Fingerprint)
			}
		}
		if e.count(t, `SELECT max(number) - count(*) FROM alert_groups`) != 0 {
			t.Error("gaps in #N")
		}
	})
}

func (e *env) integrationPublicID(t *testing.T) string {
	t.Helper()
	var id string
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT public_id FROM integrations WHERE id = $1`, e.intID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// failingSink fails every Snapshot after grouping, so that its transaction rolls back.
type failingSink struct{}

func (failingSink) AlertChanges(context.Context, ingestdb.DBTX, []ingest.AlertChange) (ingest.Routed, error) {
	return ingest.Routed{}, errors.New("after grouping")
}

// TestIntegrationNumbers is C-09.FR-2: #N comes from the counter in the creating transaction, without gaps when that
// transaction rolls back; two Snapshots of different Integrations racing for one key start one Alert Group.
func TestIntegrationNumbers(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		e.route(t, "db", "db", "alertname")
		const s1 = "2026-10-07T11:00:00Z"
		err := e.snapshot(t, ingest.Chain(e.router, e.groups, failingSink{}), gk,
			alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "a"))
		if err == nil && e.count(t, `SELECT count(*) FROM stored_snapshots WHERE state = 'failed'`) != 1 {
			t.Fatalf("the snapshot did not fail: %v", err)
		}
		if n := e.count(t, `SELECT coalesce(max(last_number), 0) FROM alert_group_counters`); n != 0 {
			t.Errorf("a rolled-back creation took #%d", n)
		}
		e.process(t, gk, alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "b"))
		if v, _ := e.groups.Get(t.Context(), e.groupOf(t, "b")); v.Number != 1 {
			t.Errorf("first number %d", v.Number)
		}
		// Two Integrations fire the same key at once on two connections: one Alert Group with both Integrations.
		other, err := e.ints.Create(t.Context(), integrations.Requester{Actor: audit.System,
			Transport: audit.TransportSystem}, integrations.Input{Name: "lab2", ConnectionMode: "webhook_only",
			DuplicateWindowSeconds: 45})
		if err != nil {
			t.Fatal(err)
		}
		body := `{"groupKey":"{}:{alertname=\"Race\"}","status":"firing","alerts":[` + alert("firing", s1,
			"alertname", "Race", "team", "db", "pod", "r") + `]}`
		for _, id := range []int64{e.intID, other.ID} {
			if _, err := e.snaps.Store(t.Context(), ingest.Received{IntegrationID: id, Body: []byte(body)}); err != nil {
				t.Fatal(err)
			}
		}
		var wg sync.WaitGroup
		for _, owner := range []string{"r1", "r2"} {
			p := ingest.NewProcessor(ingest.ProcessorConfig{OrgID: e.orgID, Store: ingest.NewProcessStore(e.d.Pool),
				Business: e.clock, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo), Sink: e.sink,
				Lease: db.Lease{Owner: owner, Duration: ingest.Lease, Clocks: clock.Clocks{Business: e.clock,
					Real: clock.Real{}}}})
			wg.Go(func() { _, _ = p.Drain(t.Context()) })
		}
		wg.Wait()
		if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE title = 'Race'`); n != 1 {
			t.Errorf("%d alert groups for one key", n)
		}
		if n := e.count(t, `SELECT cardinality(integration_ids) FROM alert_groups WHERE title = 'Race'`); n != 2 {
			t.Errorf("%d integrations", n)
		}
		if e.count(t, `SELECT count(*) FROM stored_snapshots WHERE state = 'pending'`) != 0 {
			t.Error("a racing snapshot is pending")
		}
	})
}

// TestIntegrationRouteDeletionRace is C-09.FR-19 and C-08.FR-9 with two connections: a Snapshot that groups on a
// Route holds it FOR SHARE, so a deletion waits and then answers that the Route has an open Alert Group; a deletion
// that commits first sends the Snapshot's Alert to the Default route. No open Alert Group is left on a deleted Route.
func TestIntegrationRouteDeletionRace(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		r := e.route(t, "db", "db", "alertname")
		insert := func(pod string) int64 {
			var id int64
			if err := e.d.Pool.QueryRow(ctx, `INSERT INTO alerts (org_id, integration_id, fingerprint, labels,
				annotations, status, starts_at, fired_at, first_seen_at, last_seen_at, updated_at)
				VALUES ($1, $2, $3, $4, '{}', 'firing', $5, $5, $5, $5, $5) RETURNING id`, e.orgID, e.intID, pod,
				`{"alertname":"A","team":"db","pod":"`+pod+`"}`, t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		// The Snapshot locks the Route first: the deletion waits, then refuses.
		tx, err := e.d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.sink.AlertChanges(ctx, tx, []ingest.AlertChange{{Kind: ingest.ChangeFired,
			AlertID: insert("a")}}); err != nil {
			t.Fatal(err)
		}
		deleted := make(chan error, 1)
		go func() { deleted <- e.routes.Delete(ctx, by, r.PublicID, nil) }()
		select {
		case err := <-deleted:
			t.Fatalf("the deletion did not wait: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var open *routing.OpenAlertGroupsError
		if err := <-deleted; !errors.As(err, &open) || open.Count != 1 {
			t.Fatalf("deletion = %v", err)
		}
		if _, err := e.groups.MoveOpenAlertGroups(ctx, groups.Requester{Actor: audit.System,
			Transport: audit.TransportSystem}, r.PublicID); err != nil {
			t.Fatal(err)
		}
		// The deletion locks the Route first: the Snapshot waits, then groups on the Default route.
		del, err := e.d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := del.Exec(ctx, `SELECT id FROM routes WHERE id = $1 FOR NO KEY UPDATE`, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := del.Exec(ctx, `UPDATE routes SET deleted_at = $2 WHERE id = $1`, r.ID, t0); err != nil {
			t.Fatal(err)
		}
		b := insert("b")
		grouped := make(chan error, 1)
		go func() {
			grouped <- pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
				_, err := e.sink.AlertChanges(ctx, tx, []ingest.AlertChange{{Kind: ingest.ChangeFired, AlertID: b}})
				return err
			})
		}()
		select {
		case err := <-grouped:
			t.Fatalf("the snapshot did not wait: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := del.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-grouped; err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM alert_groups g JOIN routes r ON r.id = g.route_id
			WHERE r.deleted_at IS NOT NULL AND g.status <> 'resolved'`); n != 0 {
			t.Errorf("%d open alert groups on a deleted route", n)
		}
		if n := e.count(t, `SELECT count(*) FROM alerts a JOIN routes r ON r.id = a.route_id
			WHERE a.id = $1 AND r.is_default`, b); n != 1 {
			t.Error("the alert is not recorded on the default route")
		}
		if v, _ := e.groups.Get(ctx, e.groupOf(t, "b")); v.Route.Name != routing.DefaultName {
			t.Errorf("grouped on %s", v.Route.Name)
		}
	})
}

// TestIntegrationMove is C-09.AC-9: a Route with open Alert Groups refuses its deletion with their count; after the
// move each has moved_to_default_route, the deletion succeeds, a moved Alert Group takes no new Alert and stays open
// beside one of the Default route with the same values, and resolves without a Reopen window.
func TestIntegrationMove(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		r := e.route(t, "db", "db", "alertname")
		const s1 = "2026-10-07T11:00:00Z"
		e.process(t, gk, alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "a"),
			alert("firing", s1, "alertname", "Other", "team", "db", "pod", "b"))
		var open *routing.OpenAlertGroupsError
		if err := e.routes.Delete(ctx, by, r.PublicID, nil); !errors.As(err, &open) || open.Count != 2 {
			t.Fatalf("deletion = %v", err)
		}
		if got, _ := e.routes.Get(ctx, r.PublicID); got.OpenAlertGroupCount != 2 {
			t.Errorf("open count %d", got.OpenAlertGroupCount)
		}
		n, err := e.groups.MoveOpenAlertGroups(ctx, groups.Requester{Actor: audit.System,
			Transport: audit.TransportSystem}, r.PublicID)
		if err != nil || n != 2 {
			t.Fatalf("moved %d, %v", n, err)
		}
		ga := e.groupOf(t, "a")
		if got := e.events(t, ga); !slices.Equal(got, []string{"created", "moved_to_default_route"}) {
			t.Errorf("events %v", got)
		}
		if e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'route.open_alert_groups_moved'`) != 1 {
			t.Error("no audit entry")
		}
		if err := e.routes.Delete(ctx, by, r.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		e.process(t, gk, alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "a"),
			alert("firing", s1, "alertname", "Other", "team", "db", "pod", "b"),
			alert("firing", s1, "alertname", "DiskFull", "team", "x", "pod", "c"))
		if gc := e.groupOf(t, "c"); gc == ga {
			t.Error("a moved alert group took a new alert")
		}
		if v, _ := e.groups.Get(ctx, ga); v.Status != groups.StatusFiring || v.Route.Name != routing.DefaultName {
			t.Errorf("moved %+v", v)
		}
		e.process(t, gk, alert("resolved", s1, "alertname", "DiskFull", "team", "db", "pod", "a"))
		if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE public_id = $1 AND status = 'resolved'
			AND reopen_deadline IS NULL`, ga); n != 1 {
			t.Error("the moved alert group resolved with a reopen window")
		}
	})
}

// TestIntegrationTimers is C-09.FR-12 on PostgreSQL: Reopen window ends are timer rows that two replicas claim once
// each, and overdue rows fire once.
func TestIntegrationTimers(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.route(t, "db", "db", "pod")
		const s1 = "2026-10-07T11:00:00Z"
		var firing, resolved []string
		for i := range 20 {
			pod := fmt.Sprintf("p%02d", i)
			firing = append(firing, alert("firing", s1, "alertname", "A", "team", "db", "pod", pod))
			resolved = append(resolved, alert("resolved", s1, "alertname", "A", "team", "db", "pod", pod))
		}
		e.process(t, `{}:{alertname=\"A\"}`, firing...)
		e.process(t, `{}:{alertname=\"A\"}`, resolved...)
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'reopen_window_end'`); n != 20 {
			t.Fatalf("%d timers", n)
		}
		e.clock.Advance(time.Hour) // overdue, as after downtime
		var mu sync.Mutex
		fired := map[int64]int{}
		worker := func(owner string) *timers.Worker {
			return &timers.Worker{Store: timers.NewStore(e.d.Pool),
				Lease:         db.Lease{Owner: owner, Duration: timers.Lease, Clocks: clock.Clocks{Business: e.clock, Real: clock.Real{}}},
				Organizations: func(context.Context) ([]int64, error) { return []int64{e.orgID}, nil },
				Log:           logging.New(&bytes.Buffer{}, logging.LevelInfo), Batch: 3,
				Handlers: map[string]timers.Handler{groups.TimerReopenWindowEnd: func(ctx context.Context,
					tx timersdb.DBTX, _ int64, tm timers.Timer) (func(context.Context), error) {
					mu.Lock()
					fired[tm.ID]++
					mu.Unlock()
					return nil, e.groups.EndReopenWindow(ctx, tx, *tm.AlertGroupID)
				}}}
		}
		var wg sync.WaitGroup
		for _, owner := range []string{"a", "b"} {
			w := worker(owner)
			wg.Go(func() {
				if _, err := w.Round(ctx); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if len(fired) != 20 {
			t.Errorf("%d timers fired", len(fired))
		}
		for id, n := range fired {
			if n != 1 {
				t.Errorf("timer %d fired %d times", id, n)
			}
		}
		if e.count(t, `SELECT count(*) FROM timers`)+e.count(t, `SELECT count(*) FROM alert_groups
			WHERE reopen_deadline IS NOT NULL OR prior_status IS NOT NULL`) != 0 {
			t.Error("timers or reopen windows left")
		}
	})
}

// TestIntegrationGracePeriod is C-09.FR-5: when the Grace period of a person-resolved Alert Group ends, its Alerts
// still firing join the open Alert Group of their key, or start one marked as firing again after the manual resolve.
func TestIntegrationGracePeriod(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.route(t, "db", "db", "alertname")
		const s1 = "2026-10-07T11:00:00Z"
		e.process(t, gk, alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "a"))
		ga := e.groupOf(t, "a")
		var user int64
		if err := e.d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
			created_at, updated_at) VALUES ($1, 'SRAAAAAAAAAAAA', 'alice', 'Alice', 'responder', 'local', 'active', $2, $2)
			RETURNING id`, e.orgID, t0).Scan(&user); err != nil {
			t.Fatal(err)
		}
		// A person resolves it, as Resolve does from S-032: the Alert stays as a tail within the Grace period.
		if _, err := e.d.Pool.Exec(ctx, `UPDATE alert_groups SET status = 'resolved', resolved_at = $2,
			resolved_by_kind = 'user', resolved_by_user_id = $3, grace_deadline = $4 WHERE public_id = $1`, ga,
			e.clock.Now(), user, e.clock.Now().Add(15*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if v, _ := e.groups.Get(ctx, ga); len(v.Notices) != 1 || v.Notices[0].Kind != groups.NoticeAlertsStillFiring ||
			v.Resolution.Actor == nil || v.Resolution.Actor.Name != "Alice" {
			t.Errorf("person-resolved %+v", v)
		}
		e.clock.Advance(15 * time.Minute)
		if err := pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
			_, err := e.groups.EndGracePeriod(ctx, tx, e.groupID(t, ga))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		gn := e.groupOf(t, "a")
		v, err := e.groups.Get(ctx, gn)
		if err != nil || gn == ga || v.Status != groups.StatusFiring || len(v.Notices) != 1 ||
			v.Notices[0].Kind != groups.NoticeFiringAgainAfterManualResolve || *v.Notices[0].ResolvedNumber != 1 {
			t.Errorf("firing again %+v, %v", v, err)
		}
		if e.count(t, `SELECT count(*) FROM alert_group_alerts WHERE state = 'moved'`) != 1 {
			t.Error("the tail did not move")
		}
		// A Note and the Timeline merge, oldest first.
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO notes (org_id, public_id, alert_group_id, event_seq, body,
			actor_kind, actor_user_id, transport, created_at) VALUES ($1, 'NEAAAAAAAAAAAA', $2, 9, 'looking', 'user',
			$3, 'ui', $4)`, e.orgID, e.groupID(t, gn), user, e.clock.Now()); err != nil {
			t.Fatal(err)
		}
		page, err := e.groups.Timeline(ctx, gn, groups.TimelineFilter{Limit: 1})
		if err != nil || len(page.Entries) != 1 || page.Entries[0].Note == nil || page.Next == nil ||
			page.Entries[0].Note.Author.Name != "Alice" {
			t.Fatalf("timeline %+v, %v", page, err)
		}
		next, err := e.groups.Timeline(ctx, gn, groups.TimelineFilter{Limit: 5, After: page.Next})
		if err != nil || len(next.Entries) != 1 || next.Entries[0].Event != "created" {
			t.Errorf("next %+v, %v", next, err)
		}
		if _, err := e.groups.Timeline(ctx, gn, groups.TimelineFilter{Limit: 5, Ascending: true,
			Kinds: []groups.Kind{groups.KindStatus, groups.KindDelivery}}); err != nil {
			t.Error(err)
		}
	})
}

func (e *env) groupID(t *testing.T, publicID string) int64 {
	t.Helper()
	return e.count(t, `SELECT id FROM alert_groups WHERE public_id = $1`, publicID)
}

// TestIntegrationDowntime is C-09.FR-18 and C-09.AC-11: when the Leader records a downtime, every open Alert Group
// gets muster_unavailable with the period, in the takeover's transaction.
func TestIntegrationDowntime(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.route(t, "db", "db", "alertname")
		e.process(t, gk, alert("firing", "2026-10-07T11:00:00Z", "alertname", "DiskFull", "team", "db", "pod", "a"))
		alive := func() *leader.Alive {
			return leader.NewAlive(leader.NewStore(e.d.Pool), clock.Clocks{Business: e.clock, Real: clock.Real{}},
				logging.New(&bytes.Buffer{}, logging.LevelInfo), "r1").OnDowntime(func(ctx context.Context,
				tx leaderdb.DBTX, _ int64, d leader.Downtime) error {
				return e.groups.RecordDowntime(ctx, tx, d.Start, d.End)
			})
		}
		if err := alive().TakeOver(ctx); err != nil {
			t.Fatal(err)
		}
		start := e.clock.Now()
		e.clock.Advance(10 * time.Minute)
		if err := alive().TakeOver(ctx); err != nil {
			t.Fatal(err)
		}
		page, err := e.groups.Timeline(ctx, e.groupOf(t, "a"), groups.TimelineFilter{Limit: 1,
			Kinds: []groups.Kind{groups.KindSystem}})
		if err != nil || len(page.Entries) != 1 || page.Entries[0].System != "muster_unavailable" ||
			!page.Entries[0].PeriodFrom.Equal(start) || !page.Entries[0].PeriodTo.Equal(e.clock.Now()) {
			t.Errorf("timeline %+v, %v", page, err)
		}
	})
}

// TestIntegrationGauges: muster_alert_groups counts the open Alert Groups per Route and status on PostgreSQL.
func TestIntegrationGauges(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		e.route(t, "db", "db", "alertname")
		e.process(t, gk, alert("firing", "2026-10-07T11:00:00Z", "alertname", "DiskFull", "team", "db", "pod", "a"))
		if err := e.groups.ExportGauges(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}
