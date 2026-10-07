// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package routing_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

var by = routing.Requester{Actor: audit.System, Transport: audit.TransportSystem}

type env struct {
	d      *db.DB
	orgID  int64
	svc    *routing.Service
	intID  int64
	nextFP int
}

func setup(t *testing.T, s dbtest.Server) *env {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	if err := d.Migrate(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
		FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := integrations.EnsureBuiltin(ctx, integrations.NewStore(d.Pool), org.ID, t0); err != nil {
		t.Fatal(err)
	}
	store := routing.NewStore(d.Pool)
	for range 2 {
		if err := routing.EnsureDefault(ctx, store, org.ID, t0); err != nil {
			t.Fatal(err)
		}
	}
	c := clock.NewManual(t0)
	e := &env{d: d, orgID: org.ID,
		svc: routing.New(routing.Config{OrgID: org.ID, Store: store, Audit: audit.NewWriter(logger, c), Business: c})}
	if err := d.Pool.QueryRow(ctx, `SELECT id FROM integrations WHERE builtin`).Scan(&e.intID); err != nil {
		t.Fatal(err)
	}
	return e
}

// alert inserts a firing Alert with the labels and returns its id.
func (e *env) alert(t *testing.T, labels string) int64 {
	t.Helper()
	e.nextFP++
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `INSERT INTO alerts (org_id, integration_id, fingerprint, labels,
		annotations, status, starts_at, fired_at, first_seen_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, $4, '{}', 'firing', $5, $5, $5, $5, $5) RETURNING id`, e.orgID, e.intID,
		fmt.Sprintf("%016x", e.nextFP), labels, t0).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// route routes the Alerts in one transaction, as processing hands them over.
func (e *env) route(t *testing.T, r *routing.Router, ids ...int64) ingest.Routed {
	t.Helper()
	changes := make([]ingest.AlertChange, len(ids))
	for i, id := range ids {
		changes[i] = ingest.AlertChange{Kind: ingest.ChangeFired, AlertID: id}
	}
	var out ingest.Routed
	if err := pgx.BeginFunc(t.Context(), e.d.Pool, func(tx pgx.Tx) error {
		var err error
		out, err = r.AlertChanges(t.Context(), tx, changes)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// routeOf is the Route name, Severity level and raw value recorded on the Alert.
func (e *env) routeOf(t *testing.T, id int64) string {
	t.Helper()
	var name, level, raw *string
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT r.name, a.severity_level, a.severity_raw FROM alerts a
		LEFT JOIN routes r ON r.id = a.route_id WHERE a.id = $1`, id).Scan(&name, &level, &raw); err != nil {
		t.Fatal(err)
	}
	s := func(p *string) string {
		if p == nil {
			return "-"
		}
		return *p
	}
	return s(name) + "/" + s(level) + "/" + s(raw)
}

func names(list routing.List) string {
	var out []string
	for _, r := range list.Routes {
		out = append(out, r.Name)
	}
	return strings.Join(out, ",")
}

func input(name string, ms ...routing.Matcher) routing.Input {
	p := routing.Profiles()[0]
	return routing.Input{Name: name, Matchers: ms, GroupKey: p.GroupKey, Policy: p.Policy}
}

// TestIntegrationRoutes is C-08.FR-1, FR-3, FR-9 and FR-10 on PostgreSQL: the Default route exists once and is last,
// creation goes before it, the list ETag guards a reorder, a reorder that names the Default route or another set
// changes nothing, the names are unique among Routes that are not deleted, and a deleted Route leaves the list.
func TestIntegrationRoutes(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		a, err := e.svc.Create(ctx, by, input("A", routing.Matcher{Label: "severity", Op: "=", Value: "critical"}))
		if err != nil {
			t.Fatal(err)
		}
		b, err := e.svc.Create(ctx, by, input("B", routing.Matcher{Label: "team", Op: "=", Value: "x"},
			routing.Matcher{Label: "pod", Op: "=~", Value: "api-.*"}))
		if err != nil {
			t.Fatal(err)
		}
		list, err := e.svc.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if names(list) != "A,B,Default" || list.Version != 3 || len(list.Routes[1].Matchers) != 2 ||
			list.Routes[1].Matchers[1].Value != "api-.*" || list.Routes[2].Position != 2 || len(list.Routes[2].Matchers) != 0 {
			t.Fatalf("list %s %d %+v", names(list), list.Version, list.Routes)
		}
		if _, err := e.svc.Create(ctx, by, input("A")); !errors.Is(err, routing.ErrNameTaken) {
			t.Errorf("taken = %v", err)
		}
		def := list.Routes[2]
		v := list.Version
		if _, err := e.svc.Reorder(ctx, by, &v, []string{b.PublicID, a.PublicID, def.PublicID}); !errors.Is(err,
			routing.ErrDefaultImmutable) {
			t.Errorf("default = %v", err)
		}
		if _, err := e.svc.Reorder(ctx, by, &v, []string{b.PublicID}); err == nil {
			t.Error("a reorder of another set")
		}
		stale := v - 1
		if _, err := e.svc.Reorder(ctx, by, &stale, []string{b.PublicID, a.PublicID}); !errors.Is(err,
			routing.ErrVersionMismatch) {
			t.Errorf("stale = %v", err)
		}
		got, err := e.svc.Reorder(ctx, by, &v, []string{b.PublicID, a.PublicID})
		if err != nil || names(got) != "B,A,Default" || got.Version != v+1 {
			t.Fatalf("reorder %s %d %v", names(got), got.Version, err)
		}
		if r, err := e.svc.Get(ctx, a.PublicID); err != nil || r.Position != 1 {
			t.Errorf("get A %+v %v", r, err)
		}
		if err := e.svc.Delete(ctx, by, def.PublicID, nil); !errors.Is(err, routing.ErrDefaultImmutable) {
			t.Errorf("delete default = %v", err)
		}
		if err := e.svc.Delete(ctx, by, b.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if after, _ := e.svc.List(ctx); names(after) != "A,Default" || after.Version != v+2 {
			t.Errorf("after the deletion %s %d", names(after), after.Version)
		}
		if _, err := e.svc.Create(ctx, by, input("B")); err != nil {
			t.Errorf("the name of a deleted route = %v", err)
		}
		in := input("A2", routing.Matcher{Label: "team", Op: "!~", Value: "db|web"})
		in.Urgent = true
		u, err := e.svc.Update(ctx, by, a.PublicID, nil, in)
		if err != nil || u.Name != "A2" || !u.Urgent || u.Version != 2 || len(u.Matchers) != 1 || u.Matchers[0].Op != "!~" {
			t.Errorf("update %+v %v", u, err)
		}
		var entries int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action LIKE 'route.%'`).Scan(
			&entries); err != nil || entries != 6 {
			t.Errorf("%d audit entries, %v", entries, err)
		}
	})
}

// TestIntegrationRouter is C-08.FR-3, FR-6, FR-8 and FR-13 with C-08.AC-1 and AC-9 on PostgreSQL: the first Route in
// evaluation order takes a newly firing Alert, a reorder made through another replica's service applies to the next
// one at once, ties go to the older Route, and the Severity level follows the default mapping.
func TestIntegrationRouter(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		router := routing.NewRouter(e.orgID)
		a, _ := e.svc.Create(ctx, by, input("A", routing.Matcher{Label: "severity", Op: "=", Value: "critical"}))
		b, _ := e.svc.Create(ctx, by, input("B", routing.Matcher{Label: "team", Op: "=", Value: "x"}))
		one := e.alert(t, `{"severity":"critical","team":"x"}`)
		p5 := e.alert(t, `{"severity":"P5"}`)
		none := e.alert(t, `{"severity":"none"}`)
		missing := e.alert(t, `{"k":"v"}`)
		got := e.route(t, router, one, p5, none, missing)
		if len(got.IDs) != 2 || got.PublicIDs[0] != a.PublicID {
			t.Errorf("routed %+v", got)
		}
		for id, want := range map[int64]string{one: "A/critical/-", p5: "Default/warning/P5", none: "Default/info/-",
			missing: "Default/info/-"} {
			if g := e.routeOf(t, id); g != want {
				t.Errorf("alert %d = %s, want %s", id, g, want)
			}
		}
		list, _ := e.svc.List(ctx)
		if _, err := e.svc.Reorder(ctx, by, &list.Version, []string{b.PublicID, a.PublicID}); err != nil {
			t.Fatal(err)
		}
		two := e.alert(t, `{"severity":"critical","team":"x"}`)
		e.route(t, router, two)
		if e.routeOf(t, two) != "B/critical/-" || e.routeOf(t, one) != "A/critical/-" {
			t.Errorf("after the reorder %s %s", e.routeOf(t, two), e.routeOf(t, one))
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE routes SET position = 0 WHERE NOT is_default`); err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE organizations SET route_order_version = route_order_version + 1`); err != nil {
			t.Fatal(err)
		}
		three := e.alert(t, `{"severity":"critical","team":"x"}`)
		e.route(t, router, three)
		if e.routeOf(t, three) != "A/critical/-" {
			t.Errorf("a tie went to %s", e.routeOf(t, three))
		}
	})
}
