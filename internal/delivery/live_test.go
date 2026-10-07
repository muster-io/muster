// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

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

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/groups"
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

// live is the real delivery runtime against PostgreSQL: Snapshots through the ingest processing into routing and the
// dispatcher, whose re-render step is delivery's Enqueue; two delivery workers as two replicas, sharing the token
// buckets; the recording adapter in place of a messenger.
type live struct {
	d        *db.DB
	business *clock.Manual
	real     *clock.Manual
	orgID    int64
	log      *bytes.Buffer
	logger   *logging.Logger
	routes   *routing.Service
	groups   *groups.Service
	svc      *delivery.Service
	sink     ingest.Sink
	snaps    *ingest.Service
	intID    int64
	routeID  int64
	conn     int64
	dests    []int64
	rec      *deliverytest.Recorder
	a, b     *delivery.Worker
	alice    groups.Caller
}

func setupLive(t *testing.T, s dbtest.Server) *live {
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
	var stmts []string
	for _, m := range []string{"2026-10", "2026-11"} {
		from, _ := time.Parse("2006-01", m)
		to := from.AddDate(0, 1, 0)
		for _, table := range []string{"audit_log", "timeline_entries", "delivery_events"} {
			stmts = append(stmts, fmt.Sprintf(`CREATE TABLE %s_p%s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
				table, from.Format("200601"), table, from.Format(time.RFC3339), to.Format(time.RFC3339)))
		}
	}
	for day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); day.Before(time.Date(2026, 11, 30, 0, 0, 0, 0,
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
	rstore := routing.NewStore(d.Pool)
	if err := routing.EnsureDefault(ctx, rstore, org.ID, t0); err != nil {
		t.Fatal(err)
	}
	l := &live{d: d, business: clock.NewManual(t0), real: clock.NewManual(t0.Add(-3 * time.Hour)), orgID: org.ID,
		log: &log, logger: logger, rec: &deliverytest.Recorder{}}
	l.rec.Clock = l.business
	w := audit.NewWriter(logger, l.business)
	router := routing.NewRouter(org.ID)
	l.routes = routing.New(routing.Config{OrgID: org.ID, Store: rstore, Audit: w, Business: l.business,
		Real: clock.Real{}, Router: router, Log: logger})
	l.groups = groups.New(groups.Config{OrgID: org.ID, Store: groups.NewStore(d.Pool), Audit: w, Business: l.business,
		Log: logger, Restamp: func(ctx context.Context, tx groups.DBTX, ids []int64, routeID int64) error {
			return routing.RestampAlerts(ctx, tx, org.ID, ids, routeID)
		}})
	store := delivery.NewStore(d.Pool, d.Pool)
	l.svc = delivery.New(delivery.Config{OrgID: org.ID, Store: store, Business: l.business, Log: logger})
	l.groups.SetRerender(l.svc.Enqueue)
	l.sink = ingest.Chain(router, l.groups)
	l.snaps = ingest.New(org.ID, ingest.NewStore(d.Pool), l.business)
	ints := integrations.New(integrations.Config{OrgID: org.ID, Store: integrations.NewStore(d.Pool), Audit: w,
		Business: l.business})
	in, err := ints.Create(ctx, integrations.Requester{Actor: audit.System, Transport: audit.TransportSystem},
		integrations.Input{Name: "lab", ConnectionMode: "webhook_only", DuplicateWindowSeconds: 45})
	if err != nil {
		t.Fatal(err)
	}
	l.intID = in.ID
	rt, err := l.routes.Create(ctx, routing.Requester{Actor: audit.System, Transport: audit.TransportSystem},
		routing.Input{Name: "db", GroupKey: []string{"alertname"}, Policy: routing.Profiles()[0].Policy,
			Matchers: []routing.Matcher{{Label: "team", Op: "=", Value: "db"}}})
	if err != nil {
		t.Fatal(err)
	}
	l.routeID = rt.ID
	if err := d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
		bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at,
		updated_at) VALUES ($1, 'CNAAAAAAAAAAA1', 'mattermost', 'bot', 'https://mm.example.org', '\x00', 'k1', $2,
		1000, 1, $2, $2) RETURNING id`, org.ID, t0).Scan(&l.conn); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		var id int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
			mattermost_team_id, mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at,
			updated_at) VALUES ($1, $2, 'mattermost', $3, $4, 'team', $5, '{}', 1000, 1, 'healthy', $6, $6)
			RETURNING id`, org.ID, fmt.Sprintf("DSAAAAAAAAAAA%d", i+1), fmt.Sprintf("ops-%d", i+1), l.conn,
			fmt.Sprintf("chan%d", i+1), t0).Scan(&id); err != nil {
			t.Fatal(err)
		}
		l.dests = append(l.dests, id)
	}
	l.attach(t, l.dests[0])
	var user int64
	if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
		created_at, updated_at) VALUES ($1, 'SRAAAAAAAAAAA1', 'alice', 'alice', 'responder', 'local', 'active', $2, $2)
		RETURNING id`, org.ID, t0).Scan(&user); err != nil {
		t.Fatal(err)
	}
	l.alice = groups.Caller{Actor: audit.User(user, "SRAAAAAAAAAAA1"), Transport: audit.TransportUI,
		Permissions: []auth.Permission{groups.PermissionAcknowledge, groups.PermissionResolve}}
	l.a, l.b = l.worker("r1"), l.worker("r2")
	return l
}

func (l *live) worker(owner string) *delivery.Worker {
	return &delivery.Worker{Store: delivery.NewStore(l.d.Pool, l.d.Pool), Lease: db.Lease{Owner: owner,
		Duration: delivery.Lease, Clocks: clock.Clocks{Business: l.business, Real: l.real}},
		Organizations: func(context.Context) ([]int64, error) { return []int64{l.orgID}, nil },
		Adapters:      delivery.Adapters{delivery.TypeMattermost: l.rec}, Log: l.logger}
}

// attach puts Destinations on the Route, and only them.
func (l *live) attach(t *testing.T, ids ...int64) {
	t.Helper()
	l.exec(t, `DELETE FROM route_destinations WHERE route_id = $1`, l.routeID)
	for _, id := range ids {
		l.exec(t, `INSERT INTO route_destinations (route_id, destination_id, org_id, added_at) VALUES ($1, $2, $3, $4)`,
			l.routeID, id, l.orgID, t0)
	}
}

func (l *live) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := l.d.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (l *live) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := l.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// fresh starts a subtest from no delivery, Thread reply or bucket, at the given limiter of the Destinations.
func (l *live) fresh(t *testing.T, limit, per int) {
	t.Helper()
	for _, stmt := range []string{`DELETE FROM thread_replies`, `DELETE FROM deliveries`,
		`DELETE FROM rate_limit_buckets`, `DELETE FROM delivery_events`} {
		l.exec(t, stmt)
	}
	l.exec(t, `UPDATE destinations SET limiter_limit = $1, limiter_per_seconds = $2`, limit, per)
	l.attach(t, l.dests[0])
	l.rec.Reset()
	l.business.Set(l.business.Now().Add(time.Hour).Truncate(time.Hour))
}

// fire stores a Snapshot of firing Alerts — one per alertname given, each its own Alert Group — and processes it.
func (l *live) fire(t *testing.T, groupKey string, names ...string) {
	t.Helper()
	var alerts []string
	for _, n := range names {
		alerts = append(alerts, fmt.Sprintf(`{"status":"firing","labels":{"alertname":%q,"team":"db","disk":%q},`+
			`"annotations":{},"startsAt":"2026-10-07T11:00:00Z"}`, strings.Split(n, "/")[0], n))
	}
	body := `{"groupKey":"` + groupKey + `","status":"firing","alerts":[` + strings.Join(alerts, ",") + `]}`
	if _, err := l.snaps.Store(t.Context(), ingest.Received{IntegrationID: l.intID, Body: []byte(body)}); err != nil {
		t.Fatal(err)
	}
	p := ingest.NewProcessor(ingest.ProcessorConfig{OrgID: l.orgID, Store: ingest.NewProcessStore(l.d.Pool),
		Business: l.business, Log: l.logger, Sink: l.sink, Lease: db.Lease{Owner: "ingest", Duration: ingest.Lease,
			Clocks: clock.Clocks{Business: l.business, Real: clock.Real{}}}})
	if _, err := p.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// group is the public_id of the newest Alert Group of an alertname.
func (l *live) group(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT public_id FROM alert_groups WHERE title LIKE $1 || '%'
		ORDER BY id DESC LIMIT 1`, name).Scan(&id); err != nil {
		t.Fatalf("the alert group of %s: %v", name, err)
	}
	return id
}

func (l *live) round(t *testing.T, w *delivery.Worker) {
	t.Helper()
	if _, err := w.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// both runs one round of each worker at the same time, as two replicas do.
func (l *live) both(t *testing.T) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, w := range []*delivery.Worker{l.a, l.b} {
		wg.Go(func() { _, errs[i] = w.Round(t.Context()) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

func methods(calls []deliverytest.Call) string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Method)
	}
	return strings.Join(out, ",")
}

// TestLive is the live check of S-034 (C-11, Verification).
func TestLive(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)

		t.Run("burst_collapses", func(t *testing.T) {
			// C-11.AC-1: ten Alerts in one Snapshot and an Acknowledge within 2 seconds: one Publication and at most
			// one edit; the Acknowledge, made while the Publication is in flight, collapses into the next call.
			l.fresh(t, 1000, 1)
			names := make([]string, 10)
			for i := range names {
				names[i] = fmt.Sprintf("Burst/pod-%d", i)
			}
			l.fire(t, "burst", names...)
			gid := l.group(t, "Burst")
			var acked bool
			l.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
				Then: func() {
					if _, err := l.groups.Acknowledge(t.Context(), l.alice, gid); err != nil {
						t.Error(err)
					}
					acked = true
					l.business.Advance(time.Second)
				}})
			l.business.Advance(500 * time.Millisecond)
			l.round(t, l.a)
			l.round(t, l.a)
			l.round(t, l.a)
			calls := l.rec.Calls()
			if !acked || methods(calls) != "publish,update" || !strings.Contains(calls[1].Message.Text(), "Acknowledged") ||
				calls[0].Loudness != groups.Loud {
				t.Fatalf("calls %s %+v", methods(calls), calls)
			}
			var version, state string
			if err := l.d.Pool.QueryRow(t.Context(), `SELECT desired_version::text || '/' || actual_version::text, state
				FROM deliveries`).Scan(&version, &state); err != nil || version != "2/2" || state != "delivered" {
				t.Fatalf("delivery %s %s %v", version, state, err)
			}
			t.Logf("recorder: publish=%d update=%d; the update carries desired version 2 (acknowledged)",
				l.rec.Count(deliverytest.MethodPublish), l.rec.Count(deliverytest.MethodUpdate))
			// C-11.FR-21, C-09.FR-11: one publication row in delivery_events, merged into the Timeline as delivery.
			if n := l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'publication' AND loudness = 'loud'
				AND mentions = '{new_alert_group}'`); n != 1 {
				t.Errorf("publication events %d", n)
			}
			page, err := l.groups.Timeline(t.Context(), gid, groups.TimelineFilter{Kinds: []groups.Kind{groups.KindDelivery},
				Limit: 50})
			if err != nil || len(page.Entries) != 1 || page.Entries[0].Delivery.Event != "publication" ||
				page.Entries[0].Delivery.Destination.PublicID != "DSAAAAAAAAAAA1" {
				t.Errorf("timeline %+v, %v", page.Entries, err)
			}
			all, err := l.groups.Timeline(t.Context(), gid, groups.TimelineFilter{Limit: 50, Ascending: true})
			if err != nil || len(all.Entries) != 3 || all.Entries[0].Event != "created" ||
				all.Entries[1].Event != "acknowledged" || all.Entries[2].Kind != groups.KindDelivery {
				t.Errorf("merged timeline %+v, %v", all.Entries, err)
			}
			// C-11.FR-16: the delivery state.
			states, err := l.svc.States(t.Context(), gid)
			if err != nil || len(states) != 1 || states[0].State != "delivered" || states[0].MessageURL == nil {
				t.Errorf("states %+v, %v", states, err)
			}
			// C-11.FR-17: latency from the receipt of the Snapshot, attempts and the log line.
			out := scrape(t)
			for _, want := range []string{`muster_delivery_latency_seconds_count{destination="DSAAAAAAAAAAA1"}`,
				`muster_delivery_attempts_total{destination="DSAAAAAAAAAAA1",kind="publication",outcome="delivered"}`} {
				if !strings.Contains(out, want) {
					t.Errorf("metrics lack %s", want)
				}
			}
			if strings.Count(l.log.String(), `"event":"delivery_attempt"`) < 2 {
				t.Errorf("log %s", l.log)
			}
		})

		t.Run("not_modified_is_delivered", func(t *testing.T) {
			l.fresh(t, 1000, 1)
			l.fire(t, "nm", "NotModified/a")
			gid := l.group(t, "NotModified")
			// C-11.FR-21: the Publication records one publication row and changes no Alert Group table.
			snapshot := func() string {
				var out string
				if err := l.d.Pool.QueryRow(t.Context(), `SELECT g.event_seq::text || '/' || g.last_changed_at::text ||
					'/' || (SELECT count(*) FROM timeline_entries e WHERE e.alert_group_id = g.id)::text || '/' ||
					(SELECT count(*) FROM notes n WHERE n.alert_group_id = g.id)::text
					FROM alert_groups g WHERE g.public_id = $1`, gid).Scan(&out); err != nil {
					t.Fatal(err)
				}
				return out
			}
			before := snapshot()
			l.round(t, l.a)
			if after := snapshot(); after != before || l.count(t, `SELECT count(*) FROM delivery_events
				WHERE kind = 'publication'`) != 1 {
				t.Errorf("the publication changed the alert group: %s → %s", before, after)
			}
			l.rec.Script(deliverytest.MethodUpdate, deliverytest.NotModified())
			if _, err := l.groups.Acknowledge(t.Context(), l.alice, gid); err != nil {
				t.Fatal(err)
			}
			l.round(t, l.a)
			var state string
			var attempts int64
			if err := l.d.Pool.QueryRow(t.Context(), `SELECT state, attempts FROM deliveries`).Scan(&state,
				&attempts); err != nil || state != "delivered" || attempts != 0 || methods(l.rec.Calls()) !=
				"publish,update" {
				t.Fatalf("%s %d %v %s", state, attempts, err, methods(l.rec.Calls()))
			}
			t.Logf("recorder: update answered \"not modified\"; delivery state %s, attempts %d", state, attempts)
		})

		t.Run("retry_after_destination", func(t *testing.T) {
			// C-11.AC-2: a RetryAfter of 7 s delays the next call to the Destination by 7 to 8 s; attempts unchanged.
			l.fresh(t, 1000, 1)
			l.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(7*time.Second, delivery.ScopeDestination))
			l.fire(t, "ra", "RetryAfter/a")
			start := l.business.Now()
			for range 40 {
				l.round(t, l.a)
				if len(l.rec.Calls()) == 2 {
					break
				}
				l.business.Advance(250 * time.Millisecond)
			}
			calls := l.rec.Calls()
			if len(calls) != 2 {
				t.Fatalf("calls %+v", calls)
			}
			gap := calls[1].At.Sub(calls[0].At)
			attempts := l.count(t, `SELECT attempts FROM deliveries`)
			if gap < 7*time.Second || gap > 8*time.Second || attempts != 0 || !calls[0].At.Equal(start) {
				t.Errorf("gap %v attempts %d", gap, attempts)
			}
			t.Logf("retry_after 7s: next call to the Destination after %.1f s; attempts %d", gap.Seconds(), attempts)
		})

		t.Run("retry_after_connection", func(t *testing.T) {
			// C-11.FR-8: a RetryAfter scoped to the Connection delays every Destination of the Connection.
			l.fresh(t, 1000, 1)
			l.attach(t, l.dests...)
			l.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(3*time.Second, delivery.ScopeConnection))
			l.fire(t, "rc", "RetryConn/a")
			start := l.business.Now()
			for range 30 {
				l.round(t, l.a)
				if l.rec.Count(deliverytest.MethodPublish) == 3 {
					break
				}
				l.business.Advance(250 * time.Millisecond)
			}
			calls := l.rec.Calls()
			if len(calls) != 3 {
				t.Fatalf("calls %+v", calls)
			}
			for _, c := range calls[1:] {
				if gap := c.At.Sub(start); gap < 3*time.Second || gap > 4*time.Second {
					t.Errorf("%s waited %v", c.Destination.PublicID, gap)
				}
			}
			t.Logf("retry_after 3s scoped to the Connection: both of its Destinations waited %.2f–%.2f s",
				calls[1].At.Sub(start).Seconds(), calls[2].At.Sub(start).Seconds())
		})

		t.Run("limiter_mixed_calls", func(t *testing.T) {
			// C-11.AC-14: 6 per 60 s; 3 publications and 3 edits pass, the 7th (an edit) waits for a token.
			l.fresh(t, 6, 60)
			l.fire(t, "mix", "MixA/a", "MixB/a", "MixC/a")
			l.round(t, l.a)
			for _, n := range []string{"MixA", "MixB", "MixC"} {
				if _, err := l.groups.Acknowledge(t.Context(), l.alice, l.group(t, n)); err != nil {
					t.Fatal(err)
				}
				l.round(t, l.a)
			}
			if _, err := l.groups.Resolve(t.Context(), l.alice, l.group(t, "MixA"), nil); err != nil {
				t.Fatal(err)
			}
			l.round(t, l.a)
			if methods(l.rec.Calls()) != "publish,publish,publish,update,update,update" {
				t.Fatalf("calls %s", methods(l.rec.Calls()))
			}
			waiting := l.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending' AND next_attempt_at > $1`,
				l.business.Now())
			l.business.Advance(10*time.Second + delivery.TokenMargin)
			l.round(t, l.a)
			if waiting != 1 || methods(l.rec.Calls()) != "publish,publish,publish,update,update,update,update" {
				t.Errorf("waiting %d, calls %s", waiting, methods(l.rec.Calls()))
			}
			t.Log("limiter 6 per 60 s: calls 1–6 passed (3 publish, 3 update); call 7 (update) waited for a token")
		})

		t.Run("limiter_two_replicas", func(t *testing.T) {
			// C-11.AC-14: two workers on two replicas share the buckets: the adapter sees the rate of one limiter.
			l.fresh(t, 6, 60)
			names := make([]string, 25)
			for i := range names {
				names[i] = fmt.Sprintf("Two%02d/a", i)
			}
			l.fire(t, "two", names...)
			start := l.business.Now()
			counts := map[int]int{}
			for step := range 122 {
				l.both(t)
				counts[step] = len(l.rec.Calls())
				// A token every 10 s, taken once the waiting deliveries come back after the margin: no burst
				// beyond the bucket and no second replica's share.
				if want := 6 + max(step-1, 0)/10; counts[step] != want {
					t.Errorf("by %d s: %d calls, want %d", step, counts[step], want)
				}
				l.business.Advance(time.Second)
			}
			// No call twice: every Alert Group has at most one Publication.
			seen := map[string]bool{}
			for _, c := range l.rec.Calls() {
				if seen[c.Message.Sections[0]] {
					t.Errorf("published twice: %s", c.Message.Sections[0])
				}
				seen[c.Message.Sections[0]] = true
			}
			t.Logf("two workers, limiter 6 per 60 s from %s: 6 calls at once, %d in the first minute, %d after two "+
				"minutes — one token every 10 s, the rate of one replica", start.Format(time.TimeOnly), counts[60],
				counts[121])
		})

		t.Run("urgent_first", func(t *testing.T) {
			l.fresh(t, 1, 60)
			l.fire(t, "urg", "Urg0/a", "Urg1/a", "Urg2/a", "Urg3/a", "Urg4/a")
			l.exec(t, `UPDATE deliveries SET urgent = true WHERE alert_group_id = (SELECT id FROM alert_groups
				WHERE title LIKE 'Urg3%')`)
			l.round(t, l.a)
			calls := l.rec.Calls()
			if len(calls) != 1 || !strings.Contains(calls[0].Message.Sections[0], "Urg3") {
				t.Fatalf("calls %+v", calls)
			}
			if n := l.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending' AND next_attempt_at > $1`,
				l.business.Now()); n != 4 {
				t.Errorf("held by the limiter %d", n)
			}
			t.Log("queue of 5 with one Urgent: the Urgent delivery was called first; the limiter held all of them")
		})

		t.Run("interactive_ahead_of_queue", func(t *testing.T) {
			// C-11.FR-2: the bucket is empty and deliveries wait; the interactive call takes the next token.
			l.fresh(t, 1, 5)
			l.fire(t, "ia", "Ia0/a", "Ia1/a", "Ia2/a", "Ia3/a", "Ia4/a", "Ia5/a")
			l.round(t, l.a)
			in := l.interactive()
			conn := l.conn
			dest := delivery.Destination{ID: l.dests[0], PublicID: "DSAAAAAAAAAAA1", Type: delivery.TypeMattermost,
				Connection: &conn}
			at := l.business.Now()
			out, err := in.Do(t.Context(), delivery.Subject{Destination: &dest},
				func(ctx context.Context, c delivery.Call) delivery.Outcome { return l.rec.Check(ctx, c) })
			if err != nil || out.Kind != delivery.OutcomeOK || l.business.Now().Sub(at) != 5*time.Second {
				t.Fatalf("interactive %+v %v after %v", out, err, l.business.Now().Sub(at))
			}
			l.business.Advance(delivery.TokenMargin)
			l.round(t, l.a)
			if l.rec.Count(deliverytest.MethodPublish) != 1 || l.rec.Count(deliverytest.MethodCheck) != 1 {
				t.Errorf("calls %s", methods(l.rec.Calls()))
			}
			if n := l.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`); n != 5 {
				t.Errorf("waiting %d", n)
			}
			t.Log("bucket empty, 5 deliveries waiting: the interactive call took the next token")
		})

		t.Run("interactive_limited", func(t *testing.T) {
			l.fresh(t, 1, 12)
			in := l.interactive()
			dest := delivery.Destination{ID: l.dests[0], PublicID: "DSAAAAAAAAAAA1", Type: delivery.TypeMattermost}
			ok := func(ctx context.Context, c delivery.Call) delivery.Outcome { return l.rec.Check(ctx, c) }
			if _, err := in.Do(t.Context(), delivery.Subject{Destination: &dest}, ok); err != nil {
				t.Fatal(err)
			}
			l.rec.Reset()
			_, err := in.Do(t.Context(), delivery.Subject{Destination: &dest}, ok)
			var limited *delivery.LimitedError
			if !errors.As(err, &limited) || limited.RetryAfter != 7*time.Second || len(l.rec.Calls()) != 0 {
				t.Fatalf("limited = %v, calls %d", err, len(l.rec.Calls()))
			}
			t.Logf("no token within 5 s: limited, retry after %v; the recorder saw no call", limited.RetryAfter)
		})

		t.Run("thread_batching", func(t *testing.T) {
			// C-11.FR-4, FR-5: one reply at once, then one per window listing 10 and the rest; at once after a
			// quiet period longer than the window.
			l.fresh(t, 1000, 1)
			l.fire(t, "tb", "Thread/p00")
			l.round(t, l.a)
			start := l.business.Now()
			pods := []string{"Thread/p00"}
			fireAt := func(sec int, n int) {
				l.business.Set(start.Add(time.Duration(sec) * time.Second))
				for range n {
					pods = append(pods, fmt.Sprintf("Thread/p%02d", len(pods)))
				}
				l.fire(t, "tb", pods...)
				l.round(t, l.a)
			}
			fireAt(0, 1)
			for _, sec := range []int{10, 20, 30, 40, 50} {
				fireAt(sec, 2)
			}
			fireAt(55, 2)
			replies := func() []deliverytest.Call {
				var out []deliverytest.Call
				for _, c := range l.rec.Calls() {
					if c.Method == deliverytest.MethodReply {
						out = append(out, c)
					}
				}
				return out
			}
			if r := replies(); len(r) != 1 || len(r[0].Message.Sections) != 2 {
				t.Fatalf("before the window closes %+v", r)
			}
			l.business.Set(start.Add(60 * time.Second))
			l.round(t, l.a)
			r := replies()
			if len(r) != 2 || len(r[1].Message.Sections) != 12 ||
				r[1].Message.Sections[11] != "…and 2 more — open in Muster" {
				t.Fatalf("at 60 s %+v", r)
			}
			l.business.Set(start.Add(200 * time.Second))
			l.fire(t, "tb", append(pods, "Thread/p99")...)
			l.round(t, l.a)
			if r := replies(); len(r) != 3 || !r[2].At.Equal(start.Add(200*time.Second)) {
				t.Fatalf("at 200 s %+v", r)
			}
			t.Log("t=0 s reply (1 Alert); t=10–55 s collected; t=60 s reply listing 10 of 12 Alerts and " +
				"\"…and 2 more — open in Muster\"; t=200 s reply at once")
		})

		t.Run("clock_wake", func(t *testing.T) {
			l.clockWake(t)
		})

		t.Run("lifecycle_rows", func(t *testing.T) {
			// C-11.AC-10: every row, keyed by event and variant, set up directly on a real Alert Group.
			l.fresh(t, 1000, 1)
			l.fire(t, "rows", "Rows/a")
			gid := l.group(t, "Rows")
			var id, number int64
			if err := l.d.Pool.QueryRow(t.Context(), `SELECT id, number FROM alert_groups WHERE public_id = $1`,
				gid).Scan(&id, &number); err != nil {
				t.Fatal(err)
			}
			for _, row := range delivery.Table {
				l.exec(t, `DELETE FROM thread_replies`)
				l.exec(t, `DELETE FROM deliveries`)
				l.rec.Reset()
				g := &groups.Group{ID: id, PublicID: gid, Number: number, RouteID: l.routeID, Title: "Rows",
					Status: groups.StatusFiring}
				actor := groups.System
				if row.Variant == groups.VariantCommand {
					actor = person
				}
				ev := groups.Recorded{Seq: 2, Event: row.Event, Variant: row.Variant, Loudness: row.Loudness,
					Mentions: row.Mentions}
				if row.Event == groups.EventResolved {
					ev.Variant = groups.VariantAny
				}
				if row.EventMentions {
					ev.Mentions = []groups.Mention{groups.MentionOwner}
				}
				render := func(g *groups.Group, evs ...groups.Recorded) {
					err := pgxTx(t, l, func(tx groups.DBTX) error {
						return l.svc.Enqueue(t.Context(), tx, groups.Rendering{Group: g, Actor: actor, Events: evs})
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				if row.Form != delivery.FormPublication {
					render(g, created())
					l.round(t, l.a)
					l.rec.Reset()
					if row.Form == delivery.FormUpdate || row.Form == delivery.FormReplyAndUpdate {
						g.Title = "Rows changed"
					}
				}
				render(g, ev)
				l.round(t, l.a)
				want := map[delivery.Form]string{delivery.FormPublication: "publish", delivery.FormUpdate: "update",
					delivery.FormReply: "reply", delivery.FormReplyAndUpdate: "update,reply", delivery.FormNothing: ""}
				calls := l.rec.Calls()
				if methods(calls) != want[row.Form] {
					t.Errorf("%s/%s: calls %s, want %s", row.Event, row.Variant, methods(calls), want[row.Form])
					continue
				}
				for _, c := range calls {
					wantLoud, wantMentions := groups.Quiet, []groups.Mention(nil)
					if c.Method != deliverytest.MethodUpdate {
						wantLoud, wantMentions = row.Loudness, row.Mentions
						if row.EventMentions {
							wantMentions = ev.Mentions
						}
					}
					if c.Loudness != wantLoud || !slices.Equal(c.Mentions, wantMentions) {
						t.Errorf("%s/%s: %s %s %v", row.Event, row.Variant, c.Method, c.Loudness, c.Mentions)
					}
				}
			}
			t.Logf("%d rows of the lifecycle event tables, keyed by event and variant: each produced its row's "+
				"form, loudness and Mentions", len(delivery.Table))
		})

		t.Run("reads_and_retention", func(t *testing.T) {
			// C-11.FR-18, C-08.FR-1: Destinations with health and Routes; muster_destination_info; the queue gauge;
			// the retention of sent Thread replies.
			dsvc := destinations.New(l.orgID, destinations.NewStore(l.d.Pool))
			page, err := dsvc.List(t.Context(), destinations.ListFilter{Limit: 10})
			if err != nil || len(page.Destinations) != 2 || len(page.Destinations[0].Routes) != 1 {
				t.Fatalf("list %+v, %v", page, err)
			}
			rt := "RTXXXXXXXXXXXX"
			if p, err := dsvc.List(t.Context(), destinations.ListFilter{Route: &rt, Limit: 10}); err != nil ||
				len(p.Destinations) != 0 {
				t.Errorf("an unknown route %+v %v", p, err)
			}
			refs, err := dsvc.RouteRefs(t.Context(), []int64{l.routeID})
			if err != nil || len(refs[l.routeID]) != 1 || refs[l.routeID][0].Health.State != "healthy" {
				t.Errorf("route refs %+v %v", refs, err)
			}
			if err := dsvc.RefreshInfo(t.Context()); err != nil || !strings.Contains(scrape(t),
				`muster_destination_info{destination="DSAAAAAAAAAAA2",name="ops-2"} 1`) {
				t.Errorf("info %v", err)
			}
			if err := l.svc.ExportQueue(t.Context()); err != nil || !strings.Contains(scrape(t),
				`muster_delivery_queue{destination="DSAAAAAAAAAAA1"}`) {
				t.Errorf("queue %v", err)
			}
			l.exec(t, `UPDATE thread_replies SET state = 'sent', created_at = $1`, t0.AddDate(0, -6, 0))
			before := l.count(t, `SELECT count(*) FROM thread_replies`)
			n, err := l.svc.PruneReplies(t.Context(), l.business.Now())
			if err != nil || before == 0 || n != before || l.count(t, `SELECT count(*) FROM thread_replies`) != 0 {
				t.Errorf("pruned %d of %d, %v", n, before, err)
			}
		})
		t.Run("lease_expiry", func(t *testing.T) {
			// C-11.FR-15: worker A stops while holding a lease; B claims the row once the lease ran out and delivers
			// it once; A's late outcome changes nothing.
			l.fresh(t, 1000, 1)
			stuck := &blocking{release: make(chan struct{}), entered: make(chan struct{})}
			a := l.worker("r1")
			a.Adapters = delivery.Adapters{delivery.TypeMattermost: stuck}
			l.fire(t, "le", "Lease/a")
			done := make(chan error, 1)
			go func() {
				_, err := a.Round(t.Context())
				done <- err
			}()
			<-stuck.entered
			l.round(t, l.b)
			if len(l.rec.Calls()) != 0 {
				t.Fatal("B claimed a held row")
			}
			l.real.Advance(delivery.Lease)
			l.round(t, l.b)
			close(stuck.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var publications int64
			var state, owner string
			if err := l.d.Pool.QueryRow(t.Context(), `SELECT publications, state, coalesce(message_id, '')
				FROM deliveries`).Scan(&publications, &state, &owner); err != nil || publications != 1 ||
				state != "delivered" || owner == "" || len(l.rec.Calls()) != 1 {
				t.Errorf("after the lease ran out: %d %s %q %v", publications, state, owner, err)
			}
			t.Log("worker A stopped holding a lease; worker B claimed the row after the lease ran out and " +
				"delivered it once")
		})

	})
}

// interactive is the interactive path whose sleeps advance both clocks.
func (l *live) interactive() *delivery.Interactive {
	return &delivery.Interactive{OrgID: l.orgID, Store: delivery.NewStore(l.d.Pool, l.d.Pool),
		Clocks: clock.Clocks{Business: l.business, Real: l.real},
		Sleep: func(_ context.Context, d time.Duration) bool {
			l.business.Advance(d)
			l.real.Advance(d)
			return true
		}}
}

// pgxTx runs f in a transaction of the pool, as the dispatcher's transaction.
func pgxTx(t *testing.T, l *live, f func(tx groups.DBTX) error) error {
	t.Helper()
	tx, err := l.d.Pool.Begin(t.Context())
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		_ = tx.Rollback(t.Context())
		return err
	}
	return tx.Commit(t.Context())
}

// blocking is an adapter whose calls wait until released, standing for a replica that stopped mid-call.
type blocking struct {
	deliverytest.Recorder
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (b *blocking) Publish(context.Context, delivery.Call, delivery.Message) delivery.Outcome {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return delivery.Outcome{Kind: delivery.OutcomeTransient, Error: "too late"}
}

// clockWake is C-11.FR-15 in development mode: two replicas, each with its development clock and its delivery
// worker running on real waits, listen on the database; a Thread batch due in 60 s goes at once when one replica
// advances the development clock by 60 s, because the move wakes the workers of both; NOTIFY delivery wakes them too.
func (l *live) clockWake(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l.fresh(t, 1000, 1)
	type replica struct {
		business *clock.Business
		dev      *devmode.Clock
		w        *delivery.Worker
		woken    chan struct{}
	}
	var replicas []*replica
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	listening := make(chan bool, 2)
	for i, owner := range []string{"r1", "r2"} {
		_, business := clock.System()
		r := &replica{business: business, woken: make(chan struct{}, 16)}
		r.w = l.worker(owner)
		r.w.Lease.Clocks = clock.Clocks{Business: business, Real: clock.Real{}}
		r.w.Wait = func(ctx context.Context, d time.Duration, wake <-chan struct{}) {
			select {
			case <-ctx.Done():
			case <-wake:
				r.woken <- struct{}{}
			case <-time.After(d):
			}
		}
		r.dev = devmode.NewClock(devmode.NewClockStore(l.d.Pool), business,
			func(context.Context, time.Time) error { return nil }, r.w.Wake)
		if err := r.dev.Load(ctx); err != nil {
			t.Fatal(err)
		}
		listener := db.NewListener(l.d.SessionListenConn, l.logger)
		listener.Listen(devmode.ClockChannel, func(string) { _ = r.dev.Load(ctx) })
		listener.Listen(delivery.Channel, func(string) { r.w.Wake() })
		wg.Go(func() {
			listener.Run(ctx, func(db.Hint) {}, func(bool) {
				select {
				case listening <- true:
				default:
				}
			})
		})
		wg.Go(func() { r.w.Run(ctx) })
		replicas = append(replicas, r)
		_ = i
	}
	<-listening
	<-listening
	drain := func() {
		for _, r := range replicas {
			for len(r.woken) > 0 {
				<-r.woken
			}
		}
	}
	waitFor := func(what string, cond func() bool) {
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; calls %s", what, methods(l.rec.Calls()))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// A new Alert Group on the replicas' time: NOTIFY delivery wakes a worker, which publishes it.
	svc := delivery.New(delivery.Config{OrgID: l.orgID, Store: delivery.NewStore(l.d.Pool, l.d.Pool),
		Business: replicas[0].business, Log: l.logger})
	l.fire(t, "cw", "Wake/a")
	gid := l.group(t, "Wake")
	var id, number int64
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT id, number FROM alert_groups WHERE public_id = $1`, gid).Scan(
		&id, &number); err != nil {
		t.Fatal(err)
	}
	l.exec(t, `DELETE FROM deliveries`)
	l.rec.Reset()
	g := &groups.Group{ID: id, PublicID: gid, Number: number, RouteID: l.routeID, Title: "Wake",
		Status: groups.StatusFiring}
	enqueue := func(evs ...groups.Recorded) {
		if err := pgxTx(t, l, func(tx groups.DBTX) error {
			return svc.Enqueue(ctx, tx, groups.Rendering{Group: g, Actor: groups.System, Events: evs})
		}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(created())
	waitFor("the publication", func() bool { return l.rec.Count(deliverytest.MethodPublish) == 1 })
	// The leading reply opens the window; the next Alerts wait for its end, 60 s away.
	enqueue(groups.Recorded{Seq: 2, Event: groups.EventAlertsAdded, Variant: groups.VariantFiring,
		Loudness: groups.Loud, Fingerprints: []string{"fp1"}})
	waitFor("the leading reply", func() bool { return l.rec.Count(deliverytest.MethodReply) == 1 })
	enqueue(groups.Recorded{Seq: 3, Event: groups.EventAlertsAdded, Variant: groups.VariantFiring,
		Loudness: groups.Loud, Fingerprints: []string{"fp2"}})
	time.Sleep(300 * time.Millisecond)
	if l.rec.Count(deliverytest.MethodReply) != 1 {
		t.Fatal("the batch went before its window ended")
	}
	drain()
	start := time.Now()
	if _, err := replicas[0].dev.Advance(ctx, 60); err != nil {
		t.Fatal(err)
	}
	waitFor("the batch", func() bool { return l.rec.Count(deliverytest.MethodReply) == 2 })
	waitFor("both workers woken", func() bool { return len(replicas[0].woken) > 0 && len(replicas[1].woken) > 0 })
	took := time.Since(start)
	if took > 5*time.Second {
		t.Errorf("the batch went %v after the advance", took)
	}
	t.Logf("a Thread batch due in 60 s: an advance of the development clock by 60 s woke both workers and the "+
		"reply went after %v", took.Round(time.Millisecond))
	// The development clock goes back for the other subtests of this database.
	l.exec(t, `UPDATE runtime_state SET dev_clock_offset_seconds = 0`)
}
