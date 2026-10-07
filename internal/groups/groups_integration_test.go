// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package groups_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
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
	"github.com/muster-io/muster/internal/users"
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

// TestIntegrationListedAlertsWithoutAlertGroup: every firing, routed Alert belongs to an Alert Group. Alerts that
// fired before grouping took them — routed but in no Alert Group, or not even routed — are grouped by the next
// Snapshot that lists them without a change, as one `created` entry; that Snapshot again changes nothing.
func TestIntegrationListedAlertsWithoutAlertGroup(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		r := e.route(t, "db", "db", "alertname")
		const s1 = "2026-10-07T11:00:00Z"
		a := alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "a")
		b := alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "b")
		c := alert("firing", s1, "alertname", "Other", "team", "db", "pod", "c")
		const gkc = `{}:{alertname=\"Other\"}`
		// Before routing and grouping existed: a and b are routed only, c is neither.
		if err := e.snapshot(t, ingest.Chain(e.router), gk, a, b); err != nil {
			t.Fatal(err)
		}
		if err := e.snapshot(t, nil, gkc, c); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM alert_groups`); n != 0 {
			t.Fatalf("%d alert groups before grouping", n)
		}
		if n := e.count(t, `SELECT count(*) FROM alerts WHERE route_id IS NULL`); n != 1 {
			t.Fatalf("%d alerts without a route", n)
		}
		e.clock.Advance(5 * time.Minute)
		e.process(t, gk, a, b)
		e.process(t, gkc, c)
		ga, gc := e.groupOf(t, "a"), e.groupOf(t, "c")
		if e.groupOf(t, "b") != ga || ga == gc {
			t.Fatalf("groups %s %s %s", ga, e.groupOf(t, "b"), gc)
		}
		va, err := e.groups.Get(t.Context(), ga)
		if err != nil {
			t.Fatal(err)
		}
		vc, err := e.groups.Get(t.Context(), gc)
		if err != nil {
			t.Fatal(err)
		}
		if va.Route.PublicID != r.PublicID || va.FiringCount != 2 || va.Status != "firing" ||
			vc.Route.PublicID != r.PublicID || vc.FiringCount != 1 {
			t.Errorf("alert groups %+v %+v", va, vc)
		}
		if !slices.Equal(e.events(t, ga), []string{"created"}) || !slices.Equal(e.events(t, gc), []string{"created"}) {
			t.Errorf("events %v %v", e.events(t, ga), e.events(t, gc))
		}
		state := func() [3]int64 {
			return [3]int64{e.count(t, `SELECT count(*) FROM alert_groups`),
				e.count(t, `SELECT count(*) FROM alert_group_alerts`), e.count(t, `SELECT count(*) FROM timeline_entries`)}
		}
		before := state()
		e.clock.Advance(5 * time.Minute)
		e.process(t, gk, a, b)
		e.process(t, gkc, c)
		if after := state(); after != before || before != [3]int64{2, 3, 2} {
			t.Errorf("a repeat changed %v to %v", before, after)
		}
	})
}

// TestIntegrationListedAlertRace: two workers that find the same firing Alert, or two Alerts of one key, without an
// Alert Group group them once — one Alert Group, one membership each, one `created` — whichever commits first.
func TestIntegrationListedAlertRace(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		r := e.route(t, "db", "db", "alertname")
		var x int64
		if err := e.d.Pool.QueryRow(ctx, `INSERT INTO alerts (org_id, integration_id, fingerprint, labels, annotations,
			status, starts_at, fired_at, first_seen_at, last_seen_at, updated_at, route_id, severity_level)
			VALUES ($1, $2, 'x', '{"alertname":"X","team":"db","pod":"x"}', '{}', 'firing', $3, $3, $3, $3, $3, $4,
			'warning') RETURNING id`, e.orgID, e.intID, t0, r.ID).Scan(&x); err != nil {
			t.Fatal(err)
		}
		listed := []ingest.AlertChange{{Kind: ingest.ChangeListed, AlertID: x}}
		first, err := e.d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.sink.AlertChanges(ctx, first, listed); err != nil {
			t.Fatal(err)
		}
		second := make(chan error, 1)
		go func() {
			second <- pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
				_, err := e.sink.AlertChanges(ctx, tx, listed)
				return err
			})
		}()
		select {
		case err := <-second:
			t.Fatalf("the second worker did not wait for the counter: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := first.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-second; err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM alert_group_alerts WHERE alert_id = $1`, x); n != 1 {
			t.Errorf("%d memberships", n)
		}
		if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE title = 'X'`); n != 1 {
			t.Errorf("%d alert groups", n)
		}
		if ev := e.events(t, e.groupOf(t, "x")); !slices.Equal(ev, []string{"created"}) {
			t.Errorf("events %v", ev)
		}
		// Two Integrations list an Alert of one key each, routed and in no Alert Group, on two workers at once.
		other, err := e.ints.Create(ctx, integrations.Requester{Actor: audit.System, Transport: audit.TransportSystem},
			integrations.Input{Name: "lab2", ConnectionMode: "webhook_only", DuplicateWindowSeconds: 45})
		if err != nil {
			t.Fatal(err)
		}
		const s1 = "2026-10-07T11:00:00Z"
		body := `{"groupKey":"{}:{alertname=\"Race\"}","status":"firing","alerts":[` + alert("firing", s1,
			"alertname", "Race", "team", "db", "pod", "r") + `]}`
		drain := func(sink ingest.Sink) {
			var wg sync.WaitGroup
			for _, owner := range []string{"r1", "r2"} {
				p := ingest.NewProcessor(ingest.ProcessorConfig{OrgID: e.orgID, Store: ingest.NewProcessStore(e.d.Pool),
					Business: e.clock, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo), Sink: sink,
					Lease: db.Lease{Owner: owner, Duration: ingest.Lease, Clocks: clock.Clocks{Business: e.clock,
						Real: clock.Real{}}}})
				wg.Go(func() { _, _ = p.Drain(ctx) })
			}
			wg.Wait()
		}
		store := func() {
			for _, id := range []int64{e.intID, other.ID} {
				if _, err := e.snaps.Store(ctx, ingest.Received{IntegrationID: id, Body: []byte(body)}); err != nil {
					t.Fatal(err)
				}
			}
		}
		store()
		drain(ingest.Chain(e.router))
		e.clock.Advance(5 * time.Minute)
		store()
		drain(e.sink)
		if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE title = 'Race'`); n != 1 {
			t.Errorf("%d alert groups for one key", n)
		}
		if n := e.count(t, `SELECT count(*) FROM alert_group_alerts m JOIN alert_groups g ON g.id = m.alert_group_id
			WHERE g.title = 'Race' AND m.state = 'firing'`); n != 2 {
			t.Errorf("%d memberships", n)
		}
		if e.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'processed'`) != 0 {
			t.Error("a racing snapshot did not process")
		}
	})
}

// user inserts an active User and returns its id.
func (e *env) user(t *testing.T, publicID, login string) int64 {
	t.Helper()
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO users (org_id, public_id, login, name, role, source, status,
		created_at, updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', 'active', $4, $4) RETURNING id`, e.orgID,
		publicID, login, t0).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestIntegrationCommands is S-032 on PostgreSQL: Acknowledge records the first acknowledgement once and the reads
// carry the Owner (C-09.FR-1); two users acknowledging at once leave one Owner and a single Takeover under the row lock
// (C-10.AC-3); a person's Resolve starts the Grace period; a newer open Alert Group of the key shows as the notice in
// the reads and refuses Unresolve (C-10.FR-7); once the Route is deleted Unresolve is refused with route_deleted.
func TestIntegrationCommands(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		rt := e.route(t, "db", "db", "alertname")
		perms := []auth.Permission{groups.PermissionAcknowledge, groups.PermissionResolve, groups.PermissionSnooze}
		alice := groups.Caller{Actor: audit.User(e.user(t, "SRAAAAAAAAAAA1", "alice"), "SRAAAAAAAAAAA1"),
			Transport: audit.TransportUI, Permissions: perms}
		bob := groups.Caller{Actor: audit.User(e.user(t, "SRAAAAAAAAAAA2", "bob"), "SRAAAAAAAAAAA2"),
			Transport: audit.TransportAPI, Permissions: perms}
		const s1 = "2026-10-07T11:00:00Z"
		e.process(t, gk, alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "a"))
		ga := e.groupOf(t, "a")
		e.clock.Advance(5 * time.Minute)

		// Two users at once: one acknowledgement and one Takeover, whatever the order.
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, c := range []groups.Caller{alice, bob} {
			wg.Go(func() { _, errs[i] = e.groups.Acknowledge(ctx, c, ga) })
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("acknowledges = %v", errs)
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries WHERE event = 'takeover'`); n != 1 {
			t.Errorf("%d takeovers", n)
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries WHERE event = 'acknowledged'`); n != 1 {
			t.Errorf("%d acknowledgements", n)
		}
		if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE public_id = $1
			AND first_acknowledged_at = created_at + interval '5 minutes'
			AND owner_user_id = (SELECT actor_user_id FROM timeline_entries WHERE event = 'takeover')`, ga); n != 1 {
			t.Error("the first acknowledgement or the later Owner is wrong")
		}
		v, err := e.groups.Get(ctx, ga)
		if err != nil || v.Owner == nil || len(v.Allowed(alice)) == 0 {
			t.Fatalf("get = %v %+v", err, v)
		}
		page, err := e.groups.List(ctx, groups.ListRequest{Limit: 10})
		if err != nil || len(page.Groups) != 1 || page.Groups[0].Owner == nil ||
			page.Groups[0].Owner.Name != v.Owner.Name {
			t.Errorf("list = %v %+v", err, page.Groups)
		}

		// A person's Resolve with the Alert still firing, then a new Alert of the same key within the Grace period.
		if _, err := e.groups.Resolve(ctx, bob, ga, nil); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'grace_period_end'`); n != 1 {
			t.Errorf("%d grace period timers", n)
		}
		e.process(t, gk, alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "a"),
			alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "b"))
		gb := e.groupOf(t, "b")
		v, err = e.groups.Get(ctx, ga)
		if err != nil || gb == ga || v.Newer == nil || v.Newer.PublicID != gb || slices.Contains(v.Allowed(alice),
			groups.CommandUnresolve) || !slices.ContainsFunc(v.Notices, func(n groups.Notice) bool {
			return n.Kind == groups.NoticeNewerAlertGroupExists && n.Related != nil && n.Related.Number == 2
		}) {
			t.Fatalf("get after the new alert = %v %+v", err, v)
		}
		page, err = e.groups.List(ctx, groups.ListRequest{Filter: groups.Filter{Statuses: []groups.Status{
			groups.StatusResolved}}, Limit: 10})
		if err != nil || len(page.Groups) != 1 || page.Groups[0].Newer == nil || page.Groups[0].Newer.PublicID != gb {
			t.Errorf("list of resolved = %v %+v", err, page.Groups)
		}
		_, err = e.groups.Unresolve(ctx, alice, ga)
		if r, ok := errors.AsType[*groups.RefusedError](err); !ok || r.Code != groups.CodeNewerGroupExists ||
			r.Related == nil || r.Related.PublicID != gb {
			t.Errorf("unresolve = %v", err)
		}

		// Without the newer one Unresolve works; once the Route is deleted it is refused.
		if _, err := e.groups.Resolve(ctx, alice, gb, nil); err != nil {
			t.Fatal(err)
		}
		if res, err := e.groups.Unresolve(ctx, alice, ga); err != nil || res.Group.Status != groups.StatusFiring {
			t.Fatalf("unresolve = %v %+v", err, res.Group)
		}
		if _, err := e.groups.Resolve(ctx, alice, ga, nil); err != nil {
			t.Fatal(err)
		}
		if err := e.routes.Delete(ctx, by, rt.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.groups.Unresolve(ctx, alice, ga); err == nil || !strings.Contains(err.Error(), "route_deleted") {
			t.Errorf("unresolve on a deleted route = %v", err)
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action LIKE 'alert_group.%'`); n != 6 {
			t.Errorf("%d audit entries", n)
		}
	})
}

// TestIntegrationNotesSnoozeEndsAndRelease is S-063 on PostgreSQL: a Note and Resolve with a Note numbered among the
// lifecycle events (C-10.FR-15); a Snooze until T whose timer two replicas fire exactly once at T, listing the Alert
// that joined meanwhile (C-10.AC-10, C-09.FR-12); the Owner filters of the list and the counts (C-10.AC-19); and the
// release of a disabled and a deleted Owner inside the transaction of the user administration, with no deadlock
// against Snapshot processing that changes the same Alert Group meanwhile (C-03.FR-13, D267).
func TestIntegrationNotesSnoozeEndsAndRelease(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.route(t, "db", "db", "pod")
		perms := []auth.Permission{groups.PermissionAcknowledge, groups.PermissionResolve, groups.PermissionSnooze,
			groups.PermissionNote}
		caller := func(publicID, login string) groups.Caller {
			return groups.Caller{Actor: audit.User(e.user(t, publicID, login), publicID), Transport: audit.TransportUI,
				Permissions: perms}
		}
		alice, bob, carol := caller("SRAAAAAAAAAAA1", "alice"), caller("SRAAAAAAAAAAA2", "bob"),
			caller("SRAAAAAAAAAAA3", "carol")
		const s1 = "2026-10-07T11:00:00Z"
		firing := func(pods ...string) []string {
			var out []string
			for _, p := range pods {
				out = append(out, alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", p))
			}
			return out
		}
		e.process(t, gk, firing("a", "b", "c")...)
		ga, gb, gc := e.groupOf(t, "a"), e.groupOf(t, "b"), e.groupOf(t, "c")

		// Notes.
		if _, err := e.groups.AddNote(ctx, bob, ga, "Looking."); err != nil {
			t.Fatal(err)
		}
		if _, err := e.groups.Resolve(ctx, alice, gc, ptr("Rolled back.")); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM notes n JOIN alert_groups g ON g.id = n.alert_group_id
			WHERE (g.public_id = $1 AND n.event_seq = g.event_seq AND n.body = 'Looking.')
			   OR (g.public_id = $2 AND n.event_seq = g.event_seq AND n.event_seq = (SELECT t.event_seq + 1
			       FROM timeline_entries t WHERE t.alert_group_id = g.id AND t.event = 'resolved'))`, ga, gc); n != 2 {
			t.Errorf("%d notes in place", n)
		}
		if got := e.events(t, gc); !slices.Equal(got[len(got)-2:], []string{"resolved", "note_added"}) {
			t.Errorf("timeline of a resolve with a note = %v", got)
		}

		// A Snooze until T, an Alert joining meanwhile, and two replicas at T.
		until := e.clock.Now().Add(10 * time.Minute)
		if _, err := e.groups.Snooze(ctx, alice, gb, groups.SnoozeEnd{Until: &until}); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM timers WHERE kind = 'snooze_end' AND deadline = $1`, until); n != 1 {
			t.Fatalf("%d snooze_end timers", n)
		}
		e.clock.Advance(time.Minute)
		e.process(t, gk, append(firing("a", "b", "c"), alert("firing", s1, "alertname", "DiskFull", "team", "db",
			"pod", "b", "n", "2"))...)
		e.clock.Advance(10 * time.Minute)
		var mu sync.Mutex
		fired := map[int64]int{}
		worker := func(owner string) *timers.Worker {
			return &timers.Worker{Store: timers.NewStore(e.d.Pool),
				Lease: db.Lease{Owner: owner, Duration: timers.Lease, Clocks: clock.Clocks{Business: e.clock,
					Real: clock.Real{}}},
				Organizations: func(context.Context) ([]int64, error) { return []int64{e.orgID}, nil },
				Log:           logging.New(&bytes.Buffer{}, logging.LevelInfo),
				Handlers: map[string]timers.Handler{groups.TimerSnoozeEnd: func(ctx context.Context,
					tx timersdb.DBTX, _ int64, tm timers.Timer) (func(context.Context), error) {
					mu.Lock()
					fired[tm.ID]++
					mu.Unlock()
					return e.groups.EndSnooze(ctx, tx, *tm.AlertGroupID)
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
		if len(fired) != 1 || e.count(t, `SELECT count(*) FROM timers WHERE kind = 'snooze_end'`) != 0 {
			t.Errorf("fired %v", fired)
		}
		for id, n := range fired {
			if n != 1 {
				t.Errorf("timer %d fired %d times", id, n)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries t JOIN alert_groups g ON g.id = t.alert_group_id
			WHERE g.public_id = $1 AND g.status = 'firing' AND g.owner_user_id IS NULL AND t.event = 'snooze_ended'
			  AND t.loudness = 'loud' AND t.mentions = '{snooze_ended}' AND cardinality(t.fingerprints) = 1`, gb); n != 1 {
			var dump string
			_ = e.d.Pool.QueryRow(ctx, `SELECT string_agg(concat_ws(' ', t.event, t.loudness, t.mentions::text,
				t.fingerprints::text, g.status, g.owner_user_id), '; ') FROM timeline_entries t JOIN alert_groups g
				ON g.id = t.alert_group_id WHERE g.public_id = $1`, gb).Scan(&dump)
			t.Errorf("%d snooze_ended entries; timeline %s", n, dump)
		}

		// The Owner filters.
		if _, err := e.groups.Acknowledge(ctx, bob, ga); err != nil {
			t.Fatal(err)
		}
		if _, err := e.groups.Snooze(ctx, alice, gb, groups.SnoozeEnd{NoEnd: true}); err != nil {
			t.Fatal(err)
		}
		yes, no := true, false
		for _, c := range []struct {
			f    groups.Filter
			want []string
		}{
			{groups.Filter{Owner: groups.OwnerMe, Me: bob.Actor}, []string{ga}},
			{groups.Filter{Owner: strings.ToLower(bob.Actor.PublicID)}, []string{ga}},
			{groups.Filter{SnoozedNoEnd: &no}, []string{ga}},
			{groups.Filter{Owner: alice.Actor.PublicID}, nil},
			{groups.Filter{Owner: groups.OwnerNone}, []string{gb}},
			{groups.Filter{SnoozedNoEnd: &yes}, []string{gb}},
		} {
			page, err := e.groups.List(ctx, groups.ListRequest{Filter: c.f, Limit: 10})
			var got []string
			for _, v := range page.Groups {
				got = append(got, v.PublicID)
			}
			counts, cerr := e.groups.Counts(ctx, c.f)
			if err != nil || cerr != nil || !slices.Equal(got, c.want) || counts.All-counts.Resolved != int64(len(c.want)) {
				t.Errorf("%+v = %v %v %v, counts %+v", c.f, err, cerr, got, counts)
			}
		}

		// Disabling Bob releases ga in the transaction of the disable, with user.disabled the only Audit log entry.
		w := audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), e.clock)
		admin := users.NewAdmin(e.orgID, users.NewAdminStore(e.d.Pool), w, e.clock, &url.URL{Scheme: "http",
			Host: "localhost"})
		admin.SetOwnerReleaser(e.groups)
		r := users.Requester{Actor: audit.System, Transport: audit.TransportSystem}
		audits := e.count(t, `SELECT count(*) FROM audit_log`)
		if _, err := admin.Disable(ctx, r, bob.Actor.PublicID); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries t JOIN alert_groups g ON g.id = t.alert_group_id
			WHERE g.public_id = $1 AND g.status = 'firing' AND t.event = 'unacknowledged' AND t.reason = 'owner_disabled'
			  AND t.actor_kind = 'system' AND t.loudness = 'loud' AND t.previous_owner_user_id = $2`, ga,
			bob.Actor.ID); n != 1 {
			t.Errorf("%d releases; timeline %v", n, e.events(t, ga))
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log`); n != audits+1 {
			t.Errorf("%d audit entries", n-audits)
		}
		if _, err := admin.Enable(ctx, r, bob.Actor.PublicID); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE public_id = $1 AND status = 'firing'`, ga); n != 1 {
			t.Error("enabling gave the acknowledgement back")
		}

		// The release and Snapshot processing on the same Alert Group at once, while the delete also takes the
		// counter row: neither waits for the other forever.
		for i := range 5 {
			if _, err := admin.Enable(ctx, r, carol.Actor.PublicID); err != nil {
				t.Fatal(err)
			}
			if _, err := e.groups.Acknowledge(ctx, carol, ga); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			var errs [2]error
			wg.Go(func() { _, errs[0] = admin.Disable(ctx, r, carol.Actor.PublicID) })
			wg.Go(func() {
				errs[1] = e.snapshot(t, e.sink, gk, append(firing("a", "b", "c"), alert("firing", s1, "alertname",
					"DiskFull", "team", "db", "pod", "a", "n", fmt.Sprint(i)), alert("firing", s1, "alertname",
					"DiskFull", "team", "db", "pod", fmt.Sprintf("new%d", i)))...)
			})
			wg.Wait()
			if errs[0] != nil || errs[1] != nil {
				t.Fatalf("round %d: %v", i, errs)
			}
		}
		// An Acknowledge racing the disable of its User never leaves that User as the Owner.
		for i := range 5 {
			if _, err := admin.Enable(ctx, r, bob.Actor.PublicID); err != nil {
				t.Fatal(err)
			}
			if _, err := e.groups.Unacknowledge(ctx, alice, ga); err != nil && !strings.Contains(err.Error(),
				"not_acknowledged") {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			var errs [2]error
			wg.Go(func() { _, errs[0] = admin.Disable(ctx, r, bob.Actor.PublicID) })
			wg.Go(func() { _, errs[1] = e.groups.Acknowledge(ctx, bob, ga) })
			wg.Wait()
			if _, forbidden := errors.AsType[*groups.ForbiddenError](errs[1]); errs[0] != nil ||
				(errs[1] != nil && !forbidden) {
				t.Fatalf("round %d: %v", i, errs)
			}
			if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE owner_user_id = $1`, bob.Actor.ID); n != 0 {
				t.Fatalf("round %d: the disabled user owns %d alert groups", i, n)
			}
		}

		// Disabled and enabled again before the Reopen: a system-resolved Alert Group the user owned reopens into
		// firing.
		if _, err := admin.Enable(ctx, r, bob.Actor.PublicID); err != nil {
			t.Fatal(err)
		}
		e.process(t, gk, firing("a", "b", "c", "d")...)
		gd := e.groupOf(t, "d")
		if _, err := e.groups.Acknowledge(ctx, bob, gd); err != nil {
			t.Fatal(err)
		}
		e.process(t, gk, append(firing("a", "b", "c"), alert("resolved", s1, "alertname", "DiskFull", "team", "db",
			"pod", "d"))...)
		for _, f := range []func(context.Context, users.Requester, string) (users.User, error){admin.Disable,
			admin.Enable} {
			if _, err := f(ctx, r, bob.Actor.PublicID); err != nil {
				t.Fatal(err)
			}
		}
		e.clock.Advance(time.Minute)
		e.process(t, gk, append(firing("a", "b", "c"), alert("firing", s1, "alertname", "DiskFull", "team", "db",
			"pod", "d", "n", "2"))...)
		if v, err := e.groups.Get(ctx, gd); err != nil || v.Status != groups.StatusFiring || v.Owner != nil ||
			v.ReopenCount != 1 {
			t.Errorf("reopened after the release = %v %+v", err, v)
		}

		// A Reopen into snoozed: the end of the Snooze lists the Alert that reopened it.
		dn := alert("firing", s1, "alertname", "DiskFull", "team", "db", "pod", "d", "n", "2")
		e.process(t, gk, append(firing("a", "b", "c", "e"), dn)...)
		ge := e.groupOf(t, "e")
		end := e.clock.Now().Add(time.Hour)
		if _, err := e.groups.Snooze(ctx, alice, ge, groups.SnoozeEnd{Until: &end}); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
		e.process(t, gk, append(firing("a", "b", "c"), dn, alert("resolved", s1, "alertname", "DiskFull", "team",
			"db", "pod", "e"))...)
		e.clock.Advance(time.Minute)
		e.process(t, gk, append(firing("a", "b", "c"), dn, alert("firing", s1, "alertname", "DiskFull", "team", "db",
			"pod", "e", "n", "2"))...)
		if n := e.count(t, `SELECT count(*) FROM alert_groups WHERE public_id = $1 AND status = 'snoozed'`, ge); n != 1 {
			t.Fatalf("not reopened into snoozed: %v", e.events(t, ge))
		}
		e.clock.Advance(time.Hour)
		if _, err := worker("c").Round(ctx); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM timeline_entries t JOIN alert_groups g ON g.id = t.alert_group_id
			WHERE g.public_id = $1 AND t.event = 'snooze_ended' AND cardinality(t.fingerprints) = 1`, ge); n != 1 {
			t.Errorf("snooze_ended after a reopen: %v", e.events(t, ge))
		}

		if _, err := e.groups.Acknowledge(ctx, alice, ga); err != nil {
			t.Fatal(err)
		}
		if err := admin.Delete(ctx, r, alice.Actor.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		v, err := e.groups.Get(ctx, ga)
		if err != nil || v.Status != groups.StatusFiring || v.Owner != nil {
			t.Fatalf("after the delete = %v %+v", err, v)
		}
		tl, err := e.groups.Timeline(ctx, ga, groups.TimelineFilter{Limit: 1})
		if err != nil || len(tl.Entries) != 1 || *tl.Entries[0].Reason != groups.ReasonOwnerDeleted ||
			tl.Entries[0].PreviousOwner == nil || !tl.Entries[0].PreviousOwner.Deactivated {
			t.Errorf("released by the delete = %v %+v", err, tl.Entries)
		}
	})
}
