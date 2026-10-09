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

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/delivery"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/destinations"
	destinationsdb "github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
	routingdb "github.com/muster-io/muster/internal/routing/dbgen"
	"github.com/muster-io/muster/internal/timers"
	timersdb "github.com/muster-io/muster/internal/timers/dbgen"
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
	// dsvc deletes Destinations through delivery's Retire hook; timers fires the calm checks of Storms; defaultID is
	// the Default route.
	dsvc      *destinations.Service
	timers    *timers.Worker
	defaultID int64
	// renderer renders messages as the runtime does (C-12); mentions resolves the Mentions of Loud calls.
	renderer *messages.Renderer
	mentions *mentions.Service
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
	// Messages are rendered as the runtime renders them: the sandbox, the buttons signed by an opened Keyring, and the
	// templates of Routes dry-run on save.
	keys, err := keyring.New([][]byte{bytes.Repeat([]byte{'k'}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := keys.Establish(ctx, keyring.NewStore(d.Pool), t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Open(ctx, logger, state); err != nil {
		t.Fatal(err)
	}
	l.mentions = mentions.New(org.ID, d.Pool)
	l.renderer = messages.New(messages.Config{OrgID: org.ID, DB: d.Pool, PublicURL: "http://localhost:8080",
		Business: l.business, Real: l.real, Keys: keys, Log: logger, Names: l.mentions})
	l.routes.SetTemplates(routing.TemplateHooks{Check: l.renderer.CheckTemplate,
		Saved: func(ctx context.Context, tx routingdb.DBTX, routeID int64, publicID string, kinds []string) error {
			return l.renderer.TemplateSaved(ctx, tx, routeID, publicID, kinds)
		}})
	store := delivery.NewStore(d.Pool, d.Pool)
	l.svc = delivery.New(delivery.Config{OrgID: org.ID, Store: store, Business: l.business, Log: logger,
		Renderer: delivery.MessageRenderer{Renderer: l.renderer}})
	l.groups.SetRerender(l.svc.Enqueue)
	// As the runtime wires them: Route membership, the deletion of Destinations and the calm checks of Storms.
	l.routes.SetMembership(func(ctx context.Context, tx routingdb.DBTX, routeID int64, added, removed []int64) error {
		return l.svc.RouteDestinationsChanged(ctx, tx, routeID, added, removed)
	})
	l.dsvc = destinations.New(org.ID, destinations.NewStore(d.Pool))
	l.dsvc.SetWriter(destinations.WriterConfig{Writer: destinations.NewWriter(d.Pool), Audit: w, Business: l.business,
		Routes: func(ctx context.Context, tx destinationsdb.DBTX, id int64) error {
			return l.routes.DestinationDeleted(ctx, tx, id)
		},
		Retire: func(ctx context.Context, tx destinationsdb.DBTX, id int64) error {
			return l.svc.RetireDestination(ctx, tx, id)
		}})
	l.timers = &timers.Worker{Store: timers.NewStore(d.Pool), Lease: db.Lease{Owner: "t1", Duration: timers.Lease,
		Clocks: clock.Clocks{Business: l.business, Real: l.real}},
		Organizations: func(context.Context) ([]int64, error) { return []int64{org.ID}, nil }, Log: logger,
		Handlers: map[string]timers.Handler{delivery.TimerStormCalmCheck: func(ctx context.Context, tx timersdb.DBTX,
			_ int64, tm timers.Timer) (func(context.Context), error) {
			if tm.StormID == nil {
				return nil, nil
			}
			return l.svc.CheckStormCalm(ctx, tx, *tm.StormID)
		}}}
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
	// The subtests create many Alert Groups a minute on this Route; a Storm would hold them (C-11.FR-6).
	policy := routing.Profiles()[0].Policy
	policy.StormThreshold = 1_000_000
	rt, err := l.routes.Create(ctx, routing.Requester{Actor: audit.System, Transport: audit.TransportSystem},
		routing.Input{Name: "db", GroupKey: []string{"alertname"}, Policy: policy,
			Matchers: []routing.Matcher{{Label: "team", Op: "=", Value: "db"}}})
	if err != nil {
		t.Fatal(err)
	}
	l.routeID = rt.ID
	if err := d.Pool.QueryRow(ctx, `SELECT id FROM routes WHERE org_id = $1 AND is_default`, org.ID).Scan(
		&l.defaultID); err != nil {
		t.Fatal(err)
	}
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
		Adapters:      delivery.Adapters{delivery.TypeMattermost: l.rec}, Log: l.logger,
		PublicURL: "http://localhost:8080", Renderer: delivery.MessageRenderer{Renderer: l.renderer},
		Mentions: l.mentions}
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

// fresh starts a subtest from no delivery, Thread reply, bucket, Route membership but the first Destination's on the
// Route db, Storm or Broken Destination, at the given limiter of the Destinations.
func (l *live) fresh(t *testing.T, limit, per int) {
	t.Helper()
	for _, stmt := range []string{`DELETE FROM thread_replies`, `DELETE FROM deliveries`,
		`DELETE FROM rate_limit_buckets`, `DELETE FROM delivery_events`, `DELETE FROM route_destinations`,
		`DELETE FROM timers WHERE kind = 'storm_calm_check'`,
		`UPDATE storms SET ended_at = started_at WHERE ended_at IS NULL`,
		`UPDATE destinations SET health = 'healthy', broken_since = NULL, broken_cause = NULL, broken_reason = NULL,
			next_probe_at = NULL`} {
		l.exec(t, stmt)
	}
	l.exec(t, `UPDATE destinations SET limiter_limit = $1, limiter_per_seconds = $2`, limit, per)
	l.attach(t, l.dests[0])
	l.rec.Reset()
	l.business.Set(l.business.Now().Add(time.Hour).Truncate(time.Hour))
}

// fire stores a Snapshot of firing Alerts of the Route db — one per alertname given, each its own Alert Group — and
// processes it.
func (l *live) fire(t *testing.T, groupKey string, names ...string) {
	t.Helper()
	alerts := make([]alert, len(names))
	for i, n := range names {
		alerts[i] = alert{name: n, team: "db"}
	}
	l.fireAlerts(t, groupKey, alerts...)
}

// alert is a firing Alert: its alertname before the slash, its disk label the whole name, its team the Route it goes
// to, and its severity when set.
type alert struct {
	name, team, severity string
}

// fireAlerts stores a Snapshot of firing Alerts and processes it, with the synthetic Snapshots of Internal alerts
// waiting before it.
func (l *live) fireAlerts(t *testing.T, groupKey string, alerts ...alert) {
	t.Helper()
	l.storeAlerts(t, groupKey, alerts...)
	l.process(t)
}

// storeAlerts stores a Snapshot of firing Alerts without processing it.
func (l *live) storeAlerts(t *testing.T, groupKey string, alerts ...alert) {
	t.Helper()
	var out []string
	for _, a := range alerts {
		labels := fmt.Sprintf(`"alertname":%q,"team":%q,"disk":%q`, strings.Split(a.name, "/")[0], a.team, a.name)
		if a.severity != "" {
			labels += fmt.Sprintf(`,"severity":%q`, a.severity)
		}
		out = append(out, `{"status":"firing","labels":{`+labels+`},"annotations":{},"startsAt":"2026-10-07T11:00:00Z"}`)
	}
	body := `{"groupKey":"` + groupKey + `","status":"firing","alerts":[` + strings.Join(out, ",") + `]}`
	if _, err := l.snaps.Store(t.Context(), ingest.Received{IntegrationID: l.intID, Body: []byte(body)}); err != nil {
		t.Fatal(err)
	}
}

// process drains the pending Stored Snapshots, the synthetic ones of the Internal alerts included.
func (l *live) process(t *testing.T) {
	t.Helper()
	if err := l.drain(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// drain drains the pending Stored Snapshots and returns how that failed.
func (l *live) drain(ctx context.Context) error {
	p := ingest.NewProcessor(ingest.ProcessorConfig{OrgID: l.orgID, Store: ingest.NewProcessStore(l.d.Pool),
		Business: l.business, Log: l.logger, Sink: l.sink, Lease: db.Lease{Owner: "ingest", Duration: ingest.Lease,
			Clocks: clock.Clocks{Business: l.business, Real: clock.Real{}}}})
	_, err := p.Drain(ctx)
	return err
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

// head is "#N title" of a Root message, or the first line of another message.
func head(m delivery.Message) string {
	if m.Heading != nil {
		return fmt.Sprintf("#%d %s", m.Heading.Number, m.Heading.Title)
	}
	if len(m.Lines) > 0 {
		return m.Lines[0]
	}
	return ""
}

func methods(calls []deliverytest.Call) string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Method)
	}
	return strings.Join(out, ",")
}

// TestLive is the live check of S-034, S-035 and S-036 (C-11, C-12, Verification).
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
				if seen[head(c.Message)] {
					t.Errorf("published twice: %s", head(c.Message))
				}
				seen[head(c.Message)] = true
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
			if len(calls) != 1 || !strings.Contains(head(calls[0].Message), "Urg3") {
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
			out, err := in.Do(t.Context(), delivery.Subject{Destination: &dest}, delivery.CheckOp(l.rec))
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
			ok := delivery.CheckOp(l.rec)
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
			if r := replies(); len(r) != 1 || len(r[0].Message.Lines) != 2 {
				t.Fatalf("before the window closes %+v", r)
			}
			l.business.Set(start.Add(60 * time.Second))
			l.round(t, l.a)
			r := replies()
			if len(r) != 2 || len(r[1].Message.Lines) != 12 ||
				r[1].Message.Lines[11] != "…and 2 more — open in Muster" {
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
				// The renderer reads the Alert Group through the transaction: a change of what it shows is a
				// change of its row.
				l.exec(t, `UPDATE alert_groups SET title = 'Rows' WHERE id = $1`, id)
				if row.Form != delivery.FormPublication {
					render(g, created())
					l.round(t, l.a)
					l.rec.Reset()
					if row.Form == delivery.FormUpdate || row.Form == delivery.FormReplyAndUpdate {
						l.exec(t, `UPDATE alert_groups SET title = 'Rows changed' WHERE id = $1`, id)
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

		// S-035.
		for _, sub := range []struct {
			name string
			run  func(t *testing.T)
		}{
			{"storm", l.storm},
			{"deleted_root", l.deletedRoot},
			{"duplicate_after_crash", l.duplicateAfterCrash},
			{"transient_budget", l.transientBudget},
			{"fatal_is_broken", l.fatalIsBroken},
			{"rename_keeps_broken_alert", l.renameKeepsBrokenAlert},
			{"recovery_current_state", l.recoveryCurrentState},
			{"unknown_not_delivered", l.unknownNotDelivered},
			{"late_publication_retry_after", l.lateRetryAfter},
			{"late_publication_transient", l.lateTransient},
			{"probe_check", l.probeCheck},
			{"markup_rejected", l.markupRejected},
			{"destination_added_and_removed", l.destinationAddedAndRemoved},
			{"moved_to_default_route", l.movedToDefaultRoute},
			{"delivery_event_rows", l.deliveryEventRows},
			// The review of S-035.
			{"resolve_before_first_publication", l.resolveBeforeFirstPublication},
			{"in_flight_races", l.inFlightRaces},
			{"late_note_reopened", l.lateNoteReopened},
			{"storm_membership", l.stormMembership},
			{"storm_summary_after_end", l.stormSummaryAfterEnd},
			{"deleted_broken_final_edit", l.deletedBrokenFinalEdit},
			{"destination_deleted_bumps_routes", l.destinationDeletedBumpsRoutes},
			{"membership_lock", l.membershipLock},
			{"budget_reset_on_recovery", l.budgetResetOnRecovery},
			{"abandon_connection", l.abandonConnection},
			// S-036.
			{"fallback_template", l.fallbackTemplate},
			{"russian_replies", l.russianReplies},
			{"storm_texts", l.stormTexts},
			// S-037.
			{"mentions", l.mentionTargets},
			// S-061.
			{"press_binding", l.pressBinding},
			// S-065.
			{"claims_choose_once", l.claimsChooseOnce},
			// S-044.
			{"webhook_events", l.webhookEvents},
			// S-066.
			{"telegram_threads", l.telegramThreads},
			// The follow-ups of S-067.
			{"telegram_press_lock", l.telegramPressLock},
		} {
			t.Run(sub.name, sub.run)
		}
	})
}

// claimsChooseOnce: each claim of the delivery worker and the timer worker runs its LIMIT … FOR UPDATE SKIP LOCKED
// choice once per call, also planned as a nested loop over the table it updates; as a subquery in FROM it ran once
// per row of that table, which took seconds once Thread replies piled up (S-065).
func (l *live) claimsChooseOnce(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.fire(t, "claims", "ClaimA/1", "ClaimB/1")
	l.round(t, l.a)
	l.fire(t, "claims", "ClaimA/1", "ClaimB/1", "ClaimA/2", "ClaimB/2")
	l.exec(t, `INSERT INTO timers (org_id, alert_group_id, kind, deadline, created_at, updated_at)
		SELECT org_id, id, 'reopen_window_end', $1, $1, $1 FROM alert_groups WHERE title IN ('ClaimA', 'ClaimB')
		ON CONFLICT DO NOTHING`, l.business.Now().Add(48*time.Hour))
	for _, table := range []string{"deliveries", "thread_replies", "destinations", "timers"} {
		if n := l.count(t, `SELECT count(*) FROM `+table); n < 2 {
			t.Fatalf("%d rows in %s: a choice run once per row would not show", n, table)
		}
	}
	now := l.business.Now()
	loops := dbtest.LockRowsLoops(t, l.d.Pool.Config().ConnString(), func(ctx context.Context, conn *pgx.Conn) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q, tq := deliverydb.New(tx), timersdb.New(tx)
		if _, err := q.ClaimDueDeliveries(ctx, deliverydb.ClaimDueDeliveriesParams{Owner: "probe",
			LeaseUntil: now.Add(time.Minute), OrgID: l.orgID, Due: now.Add(time.Hour), Now: now, Lim: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.ClaimDueReplies(ctx, deliverydb.ClaimDueRepliesParams{Owner: "probe",
			LeaseUntil: now.Add(time.Minute), OrgID: l.orgID, Due: now.Add(time.Hour), Now: now, Lim: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.ClaimBrokenProbes(ctx, deliverydb.ClaimBrokenProbesParams{NextProbe: now.Add(time.Minute),
			OrgID: l.orgID, Due: now.Add(time.Hour), Now: now, Lim: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := tq.ClaimDueTimers(ctx, timersdb.ClaimDueTimersParams{Owner: "probe",
			LeaseUntil: now.Add(time.Minute), OrgID: l.orgID, Due: now.Add(24 * time.Hour), Now: now, Lim: 1,
			Kinds: []string{"ack_timeout", "reminder", "snooze_end", "reopen_window_end", "grace_period_end",
				delivery.TimerStormCalmCheck}}); err != nil {
			t.Fatal(err)
		}
	})
	if loops != 1 {
		t.Errorf("a claim ran its choice %d times in one call", loops)
	}
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
		Business: replicas[0].business, Log: l.logger, Renderer: delivery.MessageRenderer{Renderer: l.renderer}})
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

// The live checks of S-035 (C-11.AC-3 to AC-13, C-09.FR-19, C-08.FR-1).

// system is the requester of the changes the tests make through the API's services.
var system = routing.Requester{Actor: audit.System, Transport: audit.TransportSystem}

// newRoute creates the Route name for the Alerts whose team is name, with its Storm threshold and Destinations, and
// returns its id and public_id.
func (l *live) newRoute(t *testing.T, name string, threshold int64, dests ...string) (int64, string) {
	t.Helper()
	policy := routing.Profiles()[0].Policy
	policy.StormThreshold = threshold
	rt, err := l.routes.Create(t.Context(), system, routing.Input{Name: name, GroupKey: []string{"alertname"},
		Policy: policy, Matchers: []routing.Matcher{{Label: "team", Op: "=", Value: name}}, DestinationIDs: dests})
	if err != nil {
		t.Fatal(err)
	}
	return rt.ID, rt.PublicID
}

// setDestinations is an updateRoute that changes only the Route's destination_ids.
func (l *live) setDestinations(t *testing.T, publicID string, ids ...string) (routing.Route, error) {
	t.Helper()
	rt, err := l.routes.Get(t.Context(), publicID)
	if err != nil {
		t.Fatal(err)
	}
	return l.routes.Update(t.Context(), system, publicID, &rt.Version, routing.Input{Name: rt.Name,
		Description: &rt.Description, Matchers: rt.Matchers, Urgent: rt.Urgent, GroupKey: rt.GroupKey,
		DestinationIDs: append([]string{}, ids...), Policy: rt.Policy})
}

// newDest adds the Mattermost Destination DSAAAAAAAAAAA<n> of the Connection and returns its id.
func (l *live) newDest(t *testing.T, n int) int64 {
	t.Helper()
	return l.newDestNamed(t, fmt.Sprintf("DSAAAAAAAAAAA%d", n))
}

// newDestNamed adds the Mattermost Destination publicID of the Connection and returns its id.
func (l *live) newDestNamed(t *testing.T, publicID string) int64 {
	t.Helper()
	var id int64
	if err := l.d.Pool.QueryRow(t.Context(), `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
		mattermost_team_id, mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at,
		updated_at) VALUES ($1, $2, 'mattermost', $3, $4, 'team', $5, '{}', 1000, 1, 'healthy', $6, $6) RETURNING id`,
		l.orgID, publicID, "ops-"+publicID, l.conn, "chan-"+publicID, l.business.Now()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// brk makes the Destination Broken directly, as after a 503, the start of a scenario about what follows.
func (l *live) brk(t *testing.T, dest int64) {
	t.Helper()
	now := l.business.Now()
	l.exec(t, `UPDATE destinations SET health = 'broken', broken_since = $2, broken_cause = 'fatal',
		broken_reason = '503', next_probe_at = $3 WHERE id = $1`, dest, now, now.Add(delivery.BrokenProbeInterval))
}

// probe advances the business clock by delivery.broken_probe_interval and runs a round, which probes.
func (l *live) probe(t *testing.T) {
	t.Helper()
	l.business.Advance(delivery.BrokenProbeInterval)
	l.round(t, l.a)
}

// health is the health of the Destination and its reason.
func (l *live) health(t *testing.T, dest int64) (string, string) {
	t.Helper()
	var health, reason string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT health, coalesce(broken_reason, '') FROM destinations
		WHERE id = $1`, dest).Scan(&health, &reason); err != nil {
		t.Fatal(err)
	}
	return health, reason
}

// state is the state of the delivery of the Alert Group publicID in the Destination.
func (l *live) state(t *testing.T, publicID string, dest int64) string {
	t.Helper()
	var state string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT d.state FROM deliveries d JOIN alert_groups g ON g.id =
		d.alert_group_id WHERE g.public_id = $1 AND d.destination_id = $2`, publicID, dest).Scan(&state); err != nil {
		t.Fatalf("the delivery of %s: %v", publicID, err)
	}
	return state
}

// toNextAttempt sets the business clock to the next attempt of the only delivery.
func (l *live) toNextAttempt(t *testing.T) {
	t.Helper()
	var at time.Time
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT next_attempt_at FROM deliveries`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at.After(l.business.Now()) {
		l.business.Set(at)
	}
}

// internalAlert is the status of MusterDestinationBroken about the Destination once processing reached its synthetic
// Snapshots, empty when it never fired.
func (l *live) internalAlert(t *testing.T, publicID string) string {
	t.Helper()
	l.process(t)
	var status string
	err := l.d.Pool.QueryRow(t.Context(), `SELECT status FROM alerts WHERE labels->>'alertname' =
		'MusterDestinationBroken' AND labels->>'destination' = $1 ORDER BY id DESC LIMIT 1`, publicID).Scan(&status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	return status
}

// groupsUnchanged is a fingerprint of every Alert Group table: the events of each Alert Group, its Timeline and
// notes.
func (l *live) groupsUnchanged(t *testing.T) string {
	t.Helper()
	var out string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM alert_groups)::text || '/' ||
		(SELECT coalesce(sum(event_seq), 0) FROM alert_groups)::text || '/' ||
		(SELECT coalesce(max(last_changed_at), 'epoch') FROM alert_groups)::text || '/' ||
		(SELECT count(*) FROM timeline_entries)::text || '/' || (SELECT count(*) FROM notes)::text`).Scan(
		&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// calm fires the calm checks of the Storm of the Route, a minute of business time at a time, until it ends, and
// returns how many checks moved its deadline first.
func (l *live) calm(t *testing.T, routeID int64) int {
	t.Helper()
	moved := 0
	for range 30 {
		l.business.Advance(time.Minute)
		before := l.count(t, `SELECT count(*) FROM timers WHERE kind = 'storm_calm_check' AND deadline <= $1`,
			l.business.Now())
		if _, err := l.timers.Round(t.Context()); err != nil {
			t.Fatal(err)
		}
		if l.count(t, `SELECT count(*) FROM storms WHERE route_id = $1 AND ended_at IS NULL`, routeID) == 0 {
			return moved
		}
		if before > 0 {
			moved++
		}
	}
	t.Fatal("the storm never calmed")
	return 0
}

// only is the calls of methods.
func only(calls []deliverytest.Call, ms ...string) []deliverytest.Call {
	return slices.DeleteFunc(slices.Clone(calls), func(c deliverytest.Call) bool { return !slices.Contains(ms, c.Method) })
}

// to is the calls to the Destination publicID.
func to(calls []deliverytest.Call, publicID string) []deliverytest.Call {
	return slices.DeleteFunc(slices.Clone(calls), func(c deliverytest.Call) bool {
		return c.Destination.PublicID != publicID
	})
}

func (l *live) ack(t *testing.T, publicID string) {
	t.Helper()
	if _, err := l.groups.Acknowledge(t.Context(), l.alice, publicID); err != nil {
		t.Fatal(err)
	}
}

func (l *live) resolve(t *testing.T, publicID string) {
	t.Helper()
	if _, err := l.groups.Resolve(t.Context(), l.alice, publicID, nil); err != nil {
		t.Fatal(err)
	}
}

// storm is C-11.AC-3: threshold 20, 30 new Alert Groups in 50 s with 3 Urgent among the last 10.
func (l *live) storm(t *testing.T) {
	l.fresh(t, 1000, 1)
	routeID, routePublic := l.newRoute(t, "storm", 20, "DSAAAAAAAAAAA1", "DSAAAAAAAAAAA2")
	start := l.business.Now()
	urgent := map[int]bool{22: true, 25: true, 28: true}
	for i := range 30 {
		l.business.Set(start.Add(time.Duration(i) * 50 * time.Second / 29))
		a := alert{name: fmt.Sprintf("Storm%02d/a", i), team: "storm"}
		if urgent[i] {
			a.severity = "critical"
		}
		l.fireAlerts(t, fmt.Sprintf("storm-%d", i), a)
	}
	took := l.business.Now().Sub(start)
	var stormID int64
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT id FROM storms WHERE route_id = $1 AND ended_at IS NULL`,
		routeID).Scan(&stormID); err != nil {
		t.Fatalf("no storm: %v", err)
	}
	// Route.storm_active and muster_storm_active while it lasts.
	rt, err := l.routes.Get(t.Context(), routePublic)
	if err != nil || rt.Storm == nil || rt.Storm.AlertGroupCount != 10 {
		t.Fatalf("route storm %+v %v", rt.Storm, err)
	}
	if err := l.svc.ExportStorms(t.Context()); err != nil ||
		!strings.Contains(scrape(t), `muster_storm_active{route="`+routePublic+`"} 1`) {
		t.Fatalf("muster_storm_active %v", err)
	}
	l.round(t, l.a)
	calls := l.rec.Calls()
	var groupsPublished, summaries, urgentFirst int
	for i, c := range calls {
		text := c.Message.Text()
		switch {
		case c.Method != deliverytest.MethodPublish:
			t.Errorf("call %s", c.Method)
		case strings.HasPrefix(text, "⛈ Storm on storm: 10 Alert Groups, "):
			summaries++
			if c.Loudness != groups.Loud || !slices.Equal(c.Mentions, []groups.Mention{groups.MentionNewAlertGroup}) {
				t.Errorf("summary %s %v", c.Loudness, c.Mentions)
			}
		default:
			groupsPublished++
			n := 0
			if _, err := fmt.Sscanf(strings.SplitN(head(c.Message), "Storm", 2)[1], "%02d", &n); err != nil {
				t.Fatalf("%q: %v", text, err)
			}
			if n >= 20 && !urgent[n] {
				t.Errorf("held alert group %d published", n)
			}
			if urgent[n] && i < 6 {
				urgentFirst++
			}
		}
	}
	// 20 before the Storm and 3 Urgent, in each of the two Destinations; one summary each.
	if groupsPublished != 46 || summaries != 2 || urgentFirst != 6 {
		t.Fatalf("published %d, summaries %d, urgent first %d: %s", groupsPublished, summaries, urgentFirst,
			methods(calls))
	}
	held := l.count(t, `SELECT count(*) FROM deliveries WHERE held_by_storm_id = $1`, stormID)
	t.Logf("route threshold 20, 30 new Alert Groups in %.0f s (3 Urgent among the last 10): publish=20 before the "+
		"Storm; storm summary: publish=1 loud [new_alert_group]; urgent publish=3 first (per Destination, 2 "+
		"Destinations); held %d; storm_active true", took.Seconds(), held)
	// Two held Alert Groups resolve during the Storm; the other five stay open.
	for _, n := range []int{20, 21} {
		l.resolve(t, l.group(t, fmt.Sprintf("Storm%02d", n)))
	}
	l.round(t, l.a)
	if n := len(only(l.rec.Calls(), deliverytest.MethodPublish)); n != len(calls) {
		t.Fatalf("published during the storm: %d", n)
	}
	l.rec.Reset()
	moved := l.calm(t, routeID)
	l.round(t, l.a)
	calls = l.rec.Calls()
	var over, quiet int
	for _, c := range calls {
		switch {
		case c.Method == deliverytest.MethodUpdate && c.Message.Lines[0] == "Storm over: 5 Alert Groups still open":
			over++
		case c.Method == deliverytest.MethodPublish && c.Loudness == groups.Quiet && len(c.Mentions) == 0:
			quiet++
			if strings.Contains(head(c.Message), "Storm20") || strings.Contains(head(c.Message), "Storm21") {
				t.Errorf("resolved held alert group published %q", head(c.Message))
			}
		default:
			t.Errorf("after the storm: %s %s %q", c.Method, c.Loudness, c.Message.Text())
		}
	}
	withheld := l.count(t, `SELECT count(*) FROM deliveries WHERE held_by_storm_id IS NULL AND state = 'withheld'
		AND alert_group_id IN (SELECT id FROM alert_groups WHERE route_id = $1)`, routeID)
	if over != 2 || quiet != 10 || withheld != 4 {
		t.Fatalf("over %d, quiet %d, withheld %d: %s", over, quiet, withheld, methods(calls))
	}
	rt, err = l.routes.Get(t.Context(), routePublic)
	if err != nil || rt.Storm != nil {
		t.Errorf("route storm after the end %+v %v", rt.Storm, err)
	}
	if err := l.svc.ExportStorms(t.Context()); err != nil ||
		!strings.Contains(scrape(t), `muster_storm_active{route="`+routePublic+`"} 0`) {
		t.Errorf("muster_storm_active after the end %v", err)
	}
	if !strings.Contains(scrape(t), `muster_delivery_attempts_total{destination="DSAAAAAAAAAAA1",kind="storm_summary",`+
		`outcome="delivered"}`) {
		t.Error("no attempts of kind storm_summary")
	}
	if !strings.Contains(l.log.String(), `"event":"storm_started"`) || !strings.Contains(l.log.String(),
		`"event":"storm_ended"`) {
		t.Error("no storm_started or storm_ended line")
	}
	t.Logf("after the calm period (%d calm checks moved first): summary update=1 quiet (\"Storm over: 5 Alert "+
		"Groups still open\"); quiet publish=5; withheld=2 (per Destination); storm_active false", moved)
}

// deletedRoot is C-11.AC-4.
func (l *live) deletedRoot(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.fire(t, "dr", "DelRoot/a")
	gid := l.group(t, "DelRoot")
	l.round(t, l.a)
	l.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "message not found"))
	l.business.Advance(42 * time.Minute)
	deleted := l.business.Now()
	l.ack(t, gid)
	l.round(t, l.a)
	l.round(t, l.a)
	calls := l.rec.Calls()
	note := "The previous message was deleted at " + deleted.UTC().Format("15:04") + "."
	if methods(calls) != "publish,update,publish" || calls[2].Loudness != groups.Quiet || len(calls[2].Mentions) != 0 ||
		calls[2].Message.Notices[len(calls[2].Message.Notices)-1] != note {
		t.Fatalf("calls %s %+v", methods(calls), calls)
	}
	if n := l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'republished'`); n != 1 {
		t.Errorf("republished events %d", n)
	}
	// Gone again: the pair is deleted in the messenger, and nothing more is sent, Thread replies included.
	l.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "message not found"))
	if _, err := l.groups.Unacknowledge(t.Context(), l.alice, gid); err != nil {
		t.Fatal(err)
	}
	l.round(t, l.a)
	l.round(t, l.a)
	l.ack(t, gid)
	l.fire(t, "dr", "DelRoot/a", "DelRoot/b")
	l.round(t, l.a)
	if got := methods(l.rec.Calls()); got != "publish,update,publish,update" ||
		l.state(t, gid, l.dests[0]) != "deleted_in_messenger" ||
		l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'deleted_in_messenger'`) != 1 {
		t.Fatalf("after the second deletion: %s, %s", got, l.state(t, gid, l.dests[0]))
	}
	// A resolved Alert Group: only the mark.
	l.fire(t, "dr2", "DelRootR/a")
	rid := l.group(t, "DelRootR")
	l.round(t, l.a)
	l.rec.Reset()
	l.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "message not found"))
	l.resolve(t, rid)
	l.round(t, l.a)
	l.round(t, l.a)
	if methods(l.rec.Calls()) != "update" || l.state(t, rid, l.dests[0]) != "deleted_in_messenger" {
		t.Fatalf("resolved: %s %s", methods(l.rec.Calls()), l.state(t, rid, l.dests[0]))
	}
	t.Logf("gone → quiet republish=1 with %q; gone again → deleted_in_messenger; no further call; resolved: "+
		"gone → deleted_in_messenger only", note)
}

// duplicateAfterCrash is C-11.AC-5.
func (l *live) duplicateAfterCrash(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.fire(t, "dup", "Dup/a")
	gid := l.group(t, "Dup")
	crashed, crash := context.WithCancel(t.Context())
	defer crash()
	// The adapter accepts the Publication; the replica stops before it records the outcome.
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Then: crash})
	_, _ = l.worker("crashed").Round(crashed)
	var started bool
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT publication_started_at IS NOT NULL AND message_id IS NULL
		FROM deliveries`).Scan(&started); err != nil || !started {
		t.Fatalf("publication_started_at not left set: %v", err)
	}
	l.real.Advance(delivery.Lease + time.Second)
	l.round(t, l.b)
	var dup bool
	var state string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT possible_duplicate, state FROM deliveries`).Scan(&dup,
		&state); err != nil || !dup || state != "delivered" || l.rec.Count(deliverytest.MethodPublish) != 2 {
		t.Fatalf("possible_duplicate %v %s %v, publish %d", dup, state, err, l.rec.Count(deliverytest.MethodPublish))
	}
	page, err := l.groups.Timeline(t.Context(), gid, groups.TimelineFilter{Kinds: []groups.Kind{groups.KindDelivery},
		Limit: 50, Ascending: true})
	var events []string
	for _, e := range page.Entries {
		events = append(events, e.Delivery.Event)
	}
	if err != nil || !slices.Contains(events, "possible_duplicate") {
		t.Fatalf("timeline %v %v", events, err)
	}
	states, err := l.svc.States(t.Context(), gid)
	if err != nil || len(states) != 1 || !states[0].PossibleDuplicate {
		t.Errorf("states %+v %v", states, err)
	}
	if !strings.Contains(l.log.String(), `"event":"delivery_possible_duplicate"`) {
		t.Error("no delivery_possible_duplicate line")
	}
	t.Logf("worker stopped after the adapter accepted; second worker: publish=%d, possible_duplicate=%v, "+
		"timeline delivery events %v", l.rec.Count(deliverytest.MethodPublish), dup, events)
}

// transientBudget is C-11.AC-6.
func (l *live) transientBudget(t *testing.T) {
	l.fresh(t, 1000, 1)
	for range 10 {
		l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "503"))
	}
	l.fire(t, "tb2", "Transient/a")
	gid := l.group(t, "Transient")
	start := l.business.Now()
	for i := range 10 {
		if health, _ := l.health(t, l.dests[0]); health != "healthy" {
			t.Fatalf("broken after %d attempts", i)
		}
		l.toNextAttempt(t)
		l.round(t, l.a)
	}
	health, reason := l.health(t, l.dests[0])
	state := l.state(t, gid, l.dests[0])
	if l.rec.Count(deliverytest.MethodPublish) != 10 || health != "broken" ||
		reason != "unavailable after repeated failures: 503" || state != "pending" {
		t.Fatalf("after 10 transient attempts: %d calls, %s %q, %s", len(l.rec.Calls()), health, reason, state)
	}
	if n := l.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND broken_cause = 'unavailable'`,
		l.dests[0]); n != 1 {
		t.Error("broken_cause is not unavailable")
	}
	states, err := l.svc.States(t.Context(), gid)
	if err != nil || states[0].State != delivery.StateWaitingForBroken {
		t.Errorf("states %+v %v", states, err)
	}
	if err := l.svc.ExportBroken(t.Context()); err != nil ||
		!strings.Contains(scrape(t), `muster_destination_broken{destination="DSAAAAAAAAAAA1"} 1`) {
		t.Errorf("muster_destination_broken %v", err)
	}
	alert := l.internalAlert(t, "DSAAAAAAAAAAA1")
	if alert != "firing" || l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'destination_broken'`) != 1 ||
		!strings.Contains(l.log.String(), `"event":"destination_broken"`) || !strings.Contains(scrape(t),
		`muster_delivery_attempts_total{destination="DSAAAAAAAAAAA1",kind="publication",outcome="transient"}`) {
		t.Fatalf("MusterDestinationBroken %q", alert)
	}
	t.Logf("10 transient attempts in %v of business time → broken (%s); MusterDestinationBroken %s; "+
		"muster_destination_broken 1; state %s (API %s)", l.business.Now().Sub(start).Round(time.Second), reason,
		alert, state, states[0].State)
}

// fatalIsBroken is C-11.FR-8, FR-9: a Fatal outcome makes the Destination Broken at once.
func (l *live) fatalIsBroken(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "channel not found"))
	l.fire(t, "fa", "Fatal/a")
	gid := l.group(t, "Fatal")
	l.round(t, l.a)
	l.round(t, l.a)
	health, reason := l.health(t, l.dests[0])
	if len(l.rec.Calls()) != 1 || health != "broken" || reason != "channel not found" ||
		l.state(t, gid, l.dests[0]) != "pending" || l.count(t, `SELECT count(*) FROM delivery_events
		WHERE kind = 'destination_broken' AND error_class = 'fatal'`) != 1 || !strings.Contains(scrape(t),
		`muster_delivery_attempts_total{destination="DSAAAAAAAAAAA1",kind="publication",outcome="fatal"}`) {
		t.Fatalf("after a fatal outcome: %d calls, %s %q", len(l.rec.Calls()), health, reason)
	}
	t.Logf("one fatal outcome → broken at once (%s), the delivery waits (pending); no further call", reason)
}

// renameKeepsBrokenAlert: the rename hook of the Destinations gives the firing MusterDestinationBroken the new
// destination_name as the same Alert, with the same fingerprint and no second Alert, and the alert still fires.
func (l *live) renameKeepsBrokenAlert(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "channel not found"))
	l.fire(t, "rn", "Rename/a")
	l.round(t, l.a)
	read := func() (n int64, fingerprint, name, status string) {
		t.Helper()
		l.process(t)
		n = l.count(t, `SELECT count(*) FROM alerts WHERE labels->>'alertname' = 'MusterDestinationBroken' AND
			labels->>'destination' = 'DSAAAAAAAAAAA1'`)
		err := l.d.Pool.QueryRow(t.Context(), `SELECT fingerprint, labels->>'destination_name', status FROM alerts
			WHERE labels->>'alertname' = 'MusterDestinationBroken' AND labels->>'destination' = 'DSAAAAAAAAAAA1'
			ORDER BY id DESC LIMIT 1`).Scan(&fingerprint, &name, &status)
		if err != nil {
			t.Fatal(err)
		}
		return n, fingerprint, name, status
	}
	var destName string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT name FROM destinations WHERE id = $1`, l.dests[0]).Scan(
		&destName); err != nil {
		t.Fatal(err)
	}
	_, fingerprint, before, firing := read()
	if firing != "firing" {
		t.Fatalf("MusterDestinationBroken %q before the rename", firing)
	}
	err := pgx.BeginFunc(t.Context(), l.d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE destinations SET name = 'renamed' WHERE id = $1`,
			l.dests[0]); err != nil {
			return err
		}
		return l.svc.DestinationRenamed(t.Context(), tx, "DSAAAAAAAAAAA1", "renamed")
	})
	if err != nil {
		t.Fatal(err)
	}
	n, after, name, status := read()
	l.exec(t, `UPDATE destinations SET name = $2 WHERE id = $1`, l.dests[0], destName)
	if n != 1 || after != fingerprint || name != "renamed" || status != "firing" || before == "renamed" {
		t.Fatalf("after the rename: %d alerts, fingerprint %s (was %s), name %q (was %q), %s", n, after, fingerprint,
			name, before, status)
	}
	t.Logf("rename: destination_name %q → %q, one Alert, fingerprint %s kept, still firing", before, name, after)
}

// recoveryCurrentState is C-11.AC-7.
func (l *live) recoveryCurrentState(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.fire(t, "recb", "RecB/a")
	b := l.group(t, "RecB")
	l.round(t, l.a)
	// A starts while its Publication breaks the Destination.
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "channel archived"))
	l.fire(t, "reca", "RecA/a")
	a := l.group(t, "RecA")
	l.round(t, l.a)
	if health, _ := l.health(t, l.dests[0]); health != "broken" {
		t.Fatal("not broken")
	}
	// B is acknowledged and gets two new Alerts; C starts and resolves.
	l.ack(t, b)
	l.fire(t, "recb", "RecB/a", "RecB/b")
	l.business.Advance(2 * time.Minute)
	l.fire(t, "recb", "RecB/a", "RecB/b", "RecB/c")
	l.fire(t, "recc", "RecC/a")
	c := l.group(t, "RecC")
	l.resolve(t, c)
	l.round(t, l.a)
	before := len(l.rec.Calls())
	l.rec.Reset()
	l.probe(t)
	calls := l.rec.Calls()
	pubs, upds := only(calls, deliverytest.MethodPublish), only(calls, deliverytest.MethodUpdate)
	var bid int64
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT id FROM alert_groups WHERE public_id = $1`, b).Scan(
		&bid); err != nil {
		t.Fatal(err)
	}
	dropped := l.count(t, `SELECT count(*) FROM thread_replies WHERE alert_group_id = $1 AND state = 'dropped'`, bid)
	var root string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT message_id FROM deliveries WHERE alert_group_id = $1`, bid).Scan(
		&root); err != nil {
		t.Fatal(err)
	}
	if before != 2 || len(pubs) != 1 || !strings.Contains(head(pubs[0].Message), "RecA") ||
		pubs[0].Loudness != groups.Loud || !slices.Equal(pubs[0].Mentions,
		[]groups.Mention{groups.MentionNewAlertGroup}) || len(upds) != 1 || upds[0].MessageID != root ||
		upds[0].Loudness != groups.Quiet || len(only(calls, deliverytest.MethodReply)) != 0 || dropped != 2 ||
		l.state(t, c, l.dests[0]) != "withheld" || l.state(t, a, l.dests[0]) != "delivered" {
		t.Fatalf("after the probe: %s %+v, dropped %d", methods(calls), calls, dropped)
	}
	for _, call := range calls {
		if call.Class != "delivery" {
			t.Errorf("%s in class %s", call.Method, call.Class)
		}
	}
	if health, _ := l.health(t, l.dests[0]); health != "healthy" || l.count(t,
		`SELECT count(*) FROM delivery_events WHERE kind = 'destination_recovered'`) != 1 {
		t.Errorf("not recovered")
	}
	t.Logf("A: publish loud %v; B: update=1, replies=0 (%d dropped); C: %s", pubs[0].Mentions, dropped,
		l.state(t, c, l.dests[0]))
}

// unknownNotDelivered is C-11.AC-8 and AC-13.
func (l *live) unknownNotDelivered(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.fire(t, "un", "Unknown/a")
	gid := l.group(t, "Unknown")
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeUnknown, "HTTP 418: teapot"))
	l.business.Advance(time.Second)
	before, events := l.groupsUnchanged(t), l.count(t, `SELECT count(*) FROM delivery_events`)
	l.round(t, l.a)
	var state, errText string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT state, coalesce(last_error, '') FROM deliveries`).Scan(&state,
		&errText); err != nil || state != "not_delivered" || errText != "HTTP 418: teapot" {
		t.Fatalf("delivery %s %q %v", state, errText, err)
	}
	after := l.groupsUnchanged(t)
	added := l.count(t, `SELECT count(*) FROM delivery_events`) - events
	health, _ := l.health(t, l.dests[0])
	if after != before || added != 1 || l.count(t, `SELECT count(*) FROM delivery_events
		WHERE kind = 'not_delivered'`) != 1 || health != "healthy" || !strings.Contains(l.log.String(),
		`"event":"delivery_not_delivered"`) || !strings.Contains(scrape(t),
		`muster_delivery_attempts_total{destination="DSAAAAAAAAAAA1",kind="publication",outcome="unknown"}`) {
		t.Fatalf("alert group tables %s → %s; delivery_events +%d; %s", before, after, added, health)
	}
	page, err := l.groups.Timeline(t.Context(), gid, groups.TimelineFilter{Limit: 50, Ascending: true})
	if err != nil || len(page.Entries) != 2 || page.Entries[0].Event != "created" ||
		page.Entries[1].Kind != groups.KindDelivery || page.Entries[1].Delivery.Event != "not_delivered" ||
		page.Entries[1].At.Before(page.Entries[0].At) {
		t.Fatalf("timeline %+v %v", page.Entries, err)
	}
	states, err := l.svc.States(t.Context(), gid)
	if err != nil || states[0].State != "not_delivered" || states[0].Error == nil {
		t.Errorf("states %+v %v", states, err)
	}
	// A later change of the Desired state starts a new delivery.
	l.ack(t, gid)
	l.round(t, l.a)
	if methods(l.rec.Calls()) != "publish,publish" || l.state(t, gid, l.dests[0]) != "delivered" {
		t.Fatalf("after a change: %s %s", methods(l.rec.Calls()), l.state(t, gid, l.dests[0]))
	}
	t.Logf("state not_delivered (%q); delivery_events +%d; Alert Group tables unchanged (%s); destination %s; "+
		"timeline [%s %s, %s %s] in order; acknowledged → a new delivery published", errText, added, after, health,
		page.Entries[0].Kind, page.Entries[0].Event, page.Entries[1].Kind, page.Entries[1].Delivery.Event)
}

// lateRetryAfter is C-11.AC-9.
func (l *live) lateRetryAfter(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(30*time.Second, delivery.ScopeDestination))
	l.fire(t, "lra", "LateRA/a")
	gid := l.group(t, "LateRA")
	started := l.business.Now()
	l.round(t, l.a)
	l.business.Advance(10 * time.Second)
	resolved := l.business.Now()
	l.resolve(t, gid)
	l.business.Advance(20*time.Second + delivery.TokenMargin)
	l.round(t, l.a)
	pubs := only(l.rec.Calls(), deliverytest.MethodPublish)
	note := fmt.Sprintf("Delivered late: started %s, resolved %s while this Destination was unavailable.",
		started.UTC().Format("15:04"), resolved.UTC().Format("15:04"))
	if len(pubs) != 2 || pubs[1].Loudness != groups.Quiet || len(pubs[1].Mentions) != 0 ||
		pubs[1].Message.Notices[len(pubs[1].Message.Notices)-1] != note || l.count(t,
		`SELECT count(*) FROM delivery_events WHERE kind = 'delivered_late' AND loudness = 'quiet'`) != 1 {
		t.Fatalf("calls %q / %q", pubs[len(pubs)-1].Message.Notices, note)
	}
	t.Logf("resolved while waiting 30 s: quiet publish with %q; delivered_late event", note)
}

// lateTransient is C-11.AC-12, and its exception: a Destination that became Broken first withholds it.
func (l *live) lateTransient(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeTransient, "502"),
		deliverytest.Failure(delivery.OutcomeTransient, "502"))
	l.fire(t, "ltr", "LateTr/a")
	gid := l.group(t, "LateTr")
	started := l.business.Now()
	l.round(t, l.a)
	l.toNextAttempt(t)
	l.round(t, l.a)
	resolved := l.business.Now()
	l.resolve(t, gid)
	l.toNextAttempt(t)
	l.round(t, l.a)
	pubs := only(l.rec.Calls(), deliverytest.MethodPublish)
	note := fmt.Sprintf("Delivered late: started %s, resolved %s while this Destination was unavailable.",
		started.UTC().Format("15:04"), resolved.UTC().Format("15:04"))
	if len(pubs) != 3 || pubs[2].Loudness != groups.Quiet ||
		pubs[2].Message.Notices[len(pubs[2].Message.Notices)-1] != note || l.count(t,
		`SELECT count(*) FROM delivery_events WHERE kind = 'delivered_late'`) != 1 {
		t.Fatalf("calls %s, last %q, want %q", methods(l.rec.Calls()), pubs[len(pubs)-1].Message.Notices, note)
	}
	t.Logf("2 transient attempts, resolved within the budget: quiet publish with %q; delivered_late event", note)
	// Broken first: the resolved Alert Group is withheld and never published, even after the recovery.
	l.fresh(t, 1000, 1)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "bot removed"))
	l.fire(t, "lbr", "BrokenLate/a")
	bid := l.group(t, "BrokenLate")
	l.round(t, l.a)
	l.resolve(t, bid)
	l.probe(t)
	if health, _ := l.health(t, l.dests[0]); health != "healthy" || l.rec.Count(deliverytest.MethodPublish) != 1 ||
		l.state(t, bid, l.dests[0]) != "withheld" || l.count(t, `SELECT count(*) FROM delivery_events
		WHERE kind = 'delivered_late'`) != 0 {
		t.Fatalf("broken first: %s, %s", methods(l.rec.Calls()), l.state(t, bid, l.dests[0]))
	}
	t.Log("broken before the late publication: withheld, never published; no delivered_late event")
}

// probeCheck is C-11.AC-11.
func (l *live) probeCheck(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "channel not found"))
	l.fire(t, "pc", "Probe/a")
	gid := l.group(t, "Probe")
	l.round(t, l.a)
	l.resolve(t, gid) // never published, Broken: withheld, so nothing waits
	if l.state(t, gid, l.dests[0]) != "withheld" {
		t.Fatal("not withheld")
	}
	l.rec.Reset()
	l.rec.Script(deliverytest.MethodCheck, deliverytest.Failure(delivery.OutcomeFatal, "not a member"))
	l.probe(t)
	health, reason := l.health(t, l.dests[0])
	if methods(l.rec.Calls()) != "check" || l.rec.Calls()[0].Class != "delivery" || health != "broken" ||
		reason != "not a member" {
		t.Fatalf("failing check: %s %s %q", methods(l.rec.Calls()), health, reason)
	}
	if alert := l.internalAlert(t, "DSAAAAAAAAAAA1"); alert != "firing" {
		t.Fatalf("MusterDestinationBroken %q", alert)
	}
	before := l.groupsUnchanged(t)
	l.probe(t)
	health, _ = l.health(t, l.dests[0])
	after := l.groupsUnchanged(t)
	if methods(l.rec.Calls()) != "check,check" || health != "healthy" || after != before || l.count(t,
		`SELECT count(*) FROM delivery_events WHERE kind = 'destination_recovered'`) != 1 {
		t.Fatalf("passing check: %s %s, alert groups %s → %s", methods(l.rec.Calls()), health, before, after)
	}
	alert := l.internalAlert(t, "DSAAAAAAAAAAA1")
	if alert != "resolved" || !strings.Contains(l.log.String(), `"event":"destination_recovered"`) {
		t.Fatalf("MusterDestinationBroken %q", alert)
	}
	t.Logf("check fails → broken, reason %q; check passes → healthy, MusterDestinationBroken %s, "+
		"destination_recovered, no Alert Group touched (%s)", reason, alert, after)
	// With a delivery waiting, the probe attempts the oldest one instead of the check.
	l.rec.Reset()
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "channel not found"))
	l.fire(t, "pc2", "ProbeWait/a")
	l.round(t, l.a)
	l.fire(t, "pc3", "ProbeLater/a")
	l.probe(t)
	if methods(l.rec.Calls()) != "publish,publish,publish" || l.rec.Count(deliverytest.MethodCheck) != 0 ||
		!strings.Contains(head(l.rec.Calls()[1].Message), "ProbeWait") {
		t.Fatalf("with a delivery waiting: %s", methods(l.rec.Calls()))
	}
	t.Log("with a delivery waiting: the probe published the oldest one (no check), then the rest followed")
}

// markupRejected is C-11.FR-8: the same text without markup in the same attempt.
func (l *live) markupRejected(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeMarkupRejected, "bad markdown"))
	l.fire(t, "mk", "Markup/a")
	gid := l.group(t, "Markup")
	l.round(t, l.a)
	calls := l.rec.Calls()
	if methods(calls) != "publish,publish" || calls[0].Plain || !calls[1].Plain ||
		calls[0].Message.Text() != calls[1].Message.Text() || l.state(t, gid, l.dests[0]) != "delivered" ||
		l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'markup_rejected'`) != 1 ||
		!strings.Contains(scrape(t), `muster_delivery_attempts_total{destination="DSAAAAAAAAAAA1",kind="publication",`+
			`outcome="markup_rejected"}`) {
		t.Fatalf("calls %+v", calls)
	}
	t.Log("markup_rejected → the same text resent plain in the same attempt; delivered; counted; " +
		"markup_rejected event")
}

// finalLink is the final note of the Alert Group publicID.
func finalLink(publicID string) string {
	return "No longer updated here; current state in Muster: http://localhost:8080/alert-groups/" + publicID
}

// isFinalEdit says whether a call is the Quiet final edit of the Alert Group publicID.
func isFinalEdit(c deliverytest.Call, publicID string) bool {
	s := c.Message.Notices
	return c.Method == deliverytest.MethodUpdate && c.Loudness == groups.Quiet && len(c.Mentions) == 0 &&
		len(s) > 0 && s[len(s)-1] == finalLink(publicID) && len(c.Message.Buttons) == 0
}

// finalEdits says whether calls are exactly one final edit of each Alert Group.
func finalEdits(calls []deliverytest.Call, ids ...string) bool {
	if len(calls) != len(ids) {
		return false
	}
	for _, id := range ids {
		if !slices.ContainsFunc(calls, func(c deliverytest.Call) bool { return isFinalEdit(c, id) }) {
			return false
		}
	}
	return true
}

// routeEdit is C-08.FR-1 and C-11.FR-14 for updateRoute: an edit of only destination_ids is saved with a new version,
// recorded as route.updated with /destination_ids in its diff, and an unknown or deleted id is unknown_id at its
// pointer. It returns the Route's public_id.
func (l *live) routeEdit(t *testing.T, publicID string, ids ...string) routing.Route {
	t.Helper()
	before, err := l.routes.Get(t.Context(), publicID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := l.setDestinations(t, publicID, ids...)
	if err != nil {
		t.Fatal(err)
	}
	var diff string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT diff::text FROM audit_log WHERE action = 'route.updated'
		AND resource_public_id = $1 ORDER BY id DESC LIMIT 1`, publicID).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	if after.Version <= before.Version || !slices.Equal(after.DestinationIDs, ids) ||
		!strings.Contains(diff, `"pointer": "/destination_ids"`) || strings.Contains(diff, `"/policy`) {
		t.Fatalf("route edit: version %d → %d, %v, diff %s", before.Version, after.Version, after.DestinationIDs, diff)
	}
	return after
}

// destinationAddedAndRemoved is C-11.FR-14 and C-08.FR-1.
func (l *live) destinationAddedAndRemoved(t *testing.T) {
	l.fresh(t, 1000, 1)
	_, route := l.newRoute(t, "members", 1_000_000)
	l.fireAlerts(t, "mem", alert{name: "MemA/a", team: "members"}, alert{name: "MemB/a", team: "members"})
	ga, gb := l.group(t, "MemA"), l.group(t, "MemB")
	l.round(t, l.a)
	if len(l.rec.Calls()) != 0 {
		t.Fatal("published without a destination")
	}
	// Added: a Quiet Publication of each open Alert Group of the Route.
	rt := l.routeEdit(t, route, "DSAAAAAAAAAAA1")
	l.round(t, l.a)
	calls := l.rec.Calls()
	for _, c := range calls {
		if c.Method != deliverytest.MethodPublish || c.Loudness != groups.Quiet || len(c.Mentions) != 0 {
			t.Errorf("added: %s %s %v", c.Method, c.Loudness, c.Mentions)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("added: %s", methods(calls))
	}
	t.Logf("updateRoute of destination_ids only: version %d, route.updated with /destination_ids; added: quiet "+
		"publish=%d", rt.Version, len(calls))
	// Removed: one final Quiet edit each, and nothing after.
	l.rec.Reset()
	l.routeEdit(t, route)
	l.round(t, l.a)
	calls = l.rec.Calls()
	if !finalEdits(calls, ga, gb) || l.state(t, ga, l.dests[0]) != "retired" ||
		l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'final_edit'`) != 2 || !strings.Contains(scrape(t),
		`muster_delivery_attempts_total{destination="DSAAAAAAAAAAA1",kind="final_edit",outcome="delivered"}`) {
		t.Fatalf("removed: %+v", calls)
	}
	l.ack(t, ga)
	l.fire(t, "mem2", "MemNone/a")
	l.round(t, l.a)
	if len(only(l.rec.Calls(), deliverytest.MethodUpdate, deliverytest.MethodReply)) != 2 {
		t.Fatalf("a call after the final edit: %s", methods(l.rec.Calls()))
	}
	t.Logf("removed: final edit=%d %q; then nothing", len(calls),
		calls[0].Message.Notices[len(calls[0].Message.Notices)-1])
	// Deleted: removed from every Route, final edits, then its secrets wiped; the row stays, unlisted.
	ds3 := l.newDest(t, 3)
	l.exec(t, `UPDATE destinations SET proxy_password_ciphertext = '\x01', proxy_password_key_id = 'k1',
		proxy_password_updated_at = $2 WHERE id = $1`, ds3, l.business.Now())
	l.exec(t, `INSERT INTO destination_secrets (destination_id, org_id, name, value_ciphertext, value_key_id,
		value_updated_at) VALUES ($1, $2, 'token', '\x00', 'k1', $3)`, ds3, l.orgID, l.business.Now())
	l.routeEdit(t, route, "DSAAAAAAAAAAA3")
	l.round(t, l.a)
	l.rec.Reset()
	if err := l.dsvc.Delete(t.Context(), destinations.Requester{Actor: audit.System,
		Transport: audit.TransportSystem}, "DSAAAAAAAAAAA3", nil); err != nil {
		t.Fatal(err)
	}
	secrets := func() int64 {
		return l.count(t, `SELECT count(*) FROM destination_secrets WHERE destination_id = $1`, ds3) +
			l.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND proxy_password_ciphertext IS NOT NULL`, ds3)
	}
	rt, err := l.routes.Get(t.Context(), route)
	if err != nil || len(rt.DestinationIDs) != 0 || secrets() != 2 || l.count(t,
		`SELECT count(*) FROM route_destinations WHERE destination_id = $1`, ds3) != 0 {
		t.Fatalf("after the delete: %v %v, secrets %d", rt.DestinationIDs, err, secrets())
	}
	l.round(t, l.a)
	calls = l.rec.Calls()
	if !finalEdits(calls, ga, gb) || secrets() != 0 ||
		l.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND deleted_at IS NOT NULL`, ds3) != 1 {
		t.Fatalf("deleted: %s, secrets %d", methods(calls), secrets())
	}
	page, err := l.dsvc.List(t.Context(), destinations.ListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range page.Destinations {
		if d.PublicID == "DSAAAAAAAAAAA3" {
			t.Error("a deleted destination is listed")
		}
	}
	if _, err := l.dsvc.Get(t.Context(), "DSAAAAAAAAAAA3"); !errors.Is(err, destinations.ErrNotFound) {
		t.Errorf("get a deleted destination: %v", err)
	}
	if l.count(t, `SELECT count(*) FROM audit_log WHERE action = 'destination.deleted'
		AND resource_public_id = 'DSAAAAAAAAAAA3'`) != 1 {
		t.Error("no destination.deleted entry")
	}
	// A deleted or unknown Destination is unknown_id at its pointer.
	for _, ids := range [][]string{{"DSAAAAAAAAAAA1", "DSAAAAAAAAAAA3"}, {"DSZZZZZZZZZZZZ"}} {
		_, err := l.setDestinations(t, route, ids...)
		var fe *routing.FieldError
		want := fmt.Sprintf("/destination_ids/%d", len(ids)-1)
		if !errors.As(err, &fe) || fe.Code != routing.CodeUnknownID || fe.Pointer != want {
			t.Errorf("%v: %v", ids, err)
		}
	}
	t.Logf("deleteDestination: removed from every Route; final edit=%d; secrets wiped after them; row kept, "+
		"not listed, read as not found; destination.deleted; a deleted or unknown id: 422 unknown_id at its pointer",
		len(calls))
}

// movedToDefaultRoute is C-09.FR-19 and C-11.FR-14: the final edit in the Destinations an Alert Group leaves, a Quiet
// Publication in those of the Default route it joins, and nothing extra in a Destination of both.
func (l *live) movedToDefaultRoute(t *testing.T) {
	l.fresh(t, 1000, 1)
	ds4 := l.newDest(t, 4)
	_, route := l.newRoute(t, "mover", 1_000_000, "DSAAAAAAAAAAA1", "DSAAAAAAAAAAA2")
	for _, d := range []int64{l.dests[1], ds4} {
		l.exec(t, `INSERT INTO route_destinations (route_id, destination_id, org_id, added_at) VALUES ($1, $2, $3, $4)`,
			l.defaultID, d, l.orgID, l.business.Now())
	}
	l.fireAlerts(t, "mv", alert{name: "MoveA/a", team: "mover"}, alert{name: "MoveB/a", team: "mover"})
	ga, gb := l.group(t, "MoveA"), l.group(t, "MoveB")
	l.round(t, l.a)
	if l.rec.Count(deliverytest.MethodPublish) != 4 {
		t.Fatalf("before the move: %s", methods(l.rec.Calls()))
	}
	l.rec.Reset()
	moved, err := l.groups.MoveOpenAlertGroups(t.Context(), groups.Requester{Actor: audit.System,
		Transport: audit.TransportSystem}, route)
	if err != nil || moved != 2 {
		t.Fatalf("moved %d %v", moved, err)
	}
	l.round(t, l.a)
	calls := l.rec.Calls()
	left, joined, both := to(calls, "DSAAAAAAAAAAA1"), to(calls, "DSAAAAAAAAAAA4"), to(calls, "DSAAAAAAAAAAA2")
	for _, c := range left {
		if !isFinalEdit(c, ga) && !isFinalEdit(c, gb) {
			t.Errorf("left: %s %q", c.Method, c.Message.Text())
		}
	}
	for _, c := range joined {
		if c.Method != deliverytest.MethodPublish || c.Loudness != groups.Quiet || len(c.Mentions) != 0 {
			t.Errorf("joined: %s %s %v", c.Method, c.Loudness, c.Mentions)
		}
	}
	for _, c := range both {
		if c.Method != deliverytest.MethodUpdate || strings.Contains(c.Message.Text(), "No longer updated here") {
			t.Errorf("in both: %s %q", c.Method, c.Message.Text())
		}
	}
	if len(left) != 2 || len(joined) != 2 || len(both) > 2 || l.state(t, ga, l.dests[0]) != "retired" ||
		l.state(t, ga, l.dests[1]) != "delivered" {
		t.Fatalf("after the move: %s", methods(calls))
	}
	t.Logf("moved to the Default route: final edit=%d in the Destination it left; quiet publish=%d in the Default "+
		"route's own; %d edit(s), no final edit, in the Destination of both", len(left), len(joined), len(both))
}

// deliveryEventRows is C-11.AC-10 for the Loud/Quiet table of delivery events: every row, on real Alert Groups,
// produces the new message, the edit or nothing it names, with its loudness and Mentions; the Publication after a
// recovery is Loud only for an Alert Group firing at that moment.
func (l *live) deliveryEventRows(t *testing.T) {
	if len(delivery.DeliveryEvents) != 11 {
		t.Fatalf("%d rows", len(delivery.DeliveryEvents))
	}
	n := 0
	// start is a Route of its own with the first Destination and one published Alert Group on it.
	start := func(t *testing.T, threshold int64, published bool) (string, string, int64) {
		t.Helper()
		l.fresh(t, 1000, 1)
		n++
		name := fmt.Sprintf("row%02d", n)
		id, route := l.newRoute(t, name, threshold, "DSAAAAAAAAAAA1")
		l.fireAlerts(t, name, alert{name: "Row" + name + "/a", team: name})
		if published {
			l.round(t, l.a)
			l.rec.Reset()
		}
		return route, l.group(t, "Row"+name), id
	}
	// stormStarted starts a Storm on a Route of threshold 1 whose first summary is published: one Alert Group
	// published, one held.
	stormStarted := func(t *testing.T) (int64, string) {
		t.Helper()
		l.fresh(t, 1000, 1)
		n++
		name := fmt.Sprintf("row%02d", n)
		id, _ := l.newRoute(t, name, 1, "DSAAAAAAAAAAA1")
		l.fireAlerts(t, name, alert{name: "Row" + name + "x/a", team: name}, alert{name: "Row" + name + "y/a",
			team: name})
		l.round(t, l.a)
		return id, name
	}
	summary := func(c deliverytest.Call) bool { return c.Message.Kind == messages.KindStorm }
	scenarios := map[string][]struct {
		firing bool
		run    func(t *testing.T) []deliverytest.Call
	}{
		delivery.DeliveryAddedDestination: {{true, func(t *testing.T) []deliverytest.Call {
			route, _, _ := start(t, 1_000_000, true)
			l.routeEdit(t, route, "DSAAAAAAAAAAA1", "DSAAAAAAAAAAA2")
			l.round(t, l.a)
			return l.rec.Calls()
		}}},
		delivery.DeliveryStormSummary: {{true, func(t *testing.T) []deliverytest.Call {
			stormStarted(t)
			return slices.DeleteFunc(l.rec.Calls(), func(c deliverytest.Call) bool { return !summary(c) })
		}}},
		delivery.DeliveryStormUpdate: {{true, func(t *testing.T) []deliverytest.Call {
			_, name := stormStarted(t)
			l.rec.Reset()
			l.fireAlerts(t, name+"z", alert{name: "Row" + name + "z/a", team: name})
			l.round(t, l.a)
			return l.rec.Calls()
		}}},
		delivery.DeliveryAfterStorm: {{true, func(t *testing.T) []deliverytest.Call {
			id, _ := stormStarted(t)
			l.rec.Reset()
			l.calm(t, id)
			l.round(t, l.a)
			return slices.DeleteFunc(l.rec.Calls(), summary)
		}}},
		delivery.DeliveryLate: {{true, func(t *testing.T) []deliverytest.Call {
			_, gid, _ := start(t, 1_000_000, false)
			l.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(30*time.Second, delivery.ScopeDestination))
			l.round(t, l.a)
			l.resolve(t, gid)
			l.rec.Reset()
			l.business.Advance(31 * time.Second)
			l.round(t, l.a)
			return only(l.rec.Calls(), deliverytest.MethodPublish, deliverytest.MethodUpdate)
		}}},
		delivery.DeliveryAfterRecovery: {
			{true, func(t *testing.T) []deliverytest.Call {
				start(t, 1_000_000, false)
				l.brk(t, l.dests[0])
				l.probe(t)
				return l.rec.Calls()
			}},
			{false, func(t *testing.T) []deliverytest.Call {
				_, gid, _ := start(t, 1_000_000, false)
				l.brk(t, l.dests[0])
				l.ack(t, gid)
				l.probe(t)
				return l.rec.Calls()
			}},
		},
		delivery.DeliveryUpdateRecovery: {{true, func(t *testing.T) []deliverytest.Call {
			_, gid, _ := start(t, 1_000_000, true)
			l.brk(t, l.dests[0])
			l.ack(t, gid)
			l.probe(t)
			return l.rec.Calls()
		}}},
		delivery.DeliveryRepliesWhileBroken: {{true, func(t *testing.T) []deliverytest.Call {
			_, gid, _ := start(t, 1_000_000, true)
			name := fmt.Sprintf("row%02d", n)
			l.brk(t, l.dests[0])
			l.fireAlerts(t, name, alert{name: "Row" + name + "/a", team: name}, alert{name: "Row" + name + "/b",
				team: name})
			l.probe(t)
			if l.count(t, `SELECT count(*) FROM thread_replies WHERE state = 'dropped' AND alert_group_id =
				(SELECT id FROM alert_groups WHERE public_id = $1)`, gid) != 1 {
				t.Error("the reply was not dropped")
			}
			return only(l.rec.Calls(), deliverytest.MethodReply)
		}}},
		delivery.DeliveryResolvedBroken: {{true, func(t *testing.T) []deliverytest.Call {
			_, gid, _ := start(t, 1_000_000, false)
			l.brk(t, l.dests[0])
			l.resolve(t, gid)
			l.probe(t)
			if l.state(t, gid, l.dests[0]) != "withheld" {
				t.Errorf("resolved while broken: %s", l.state(t, gid, l.dests[0]))
			}
			return without(l.rec.Calls(), deliverytest.MethodCheck)
		}}},
		delivery.DeliveryRepublication: {{true, func(t *testing.T) []deliverytest.Call {
			_, gid, _ := start(t, 1_000_000, true)
			l.rec.Script(deliverytest.MethodUpdate, deliverytest.Failure(delivery.OutcomeGone, "deleted"))
			l.ack(t, gid)
			l.round(t, l.a)
			l.rec.Reset()
			l.round(t, l.a)
			return l.rec.Calls()
		}}},
		delivery.DeliveryFinalEdit: {{true, func(t *testing.T) []deliverytest.Call {
			route, _, _ := start(t, 1_000_000, true)
			l.routeEdit(t, route)
			l.round(t, l.a)
			return l.rec.Calls()
		}}},
	}
	var lines []string
	for _, row := range delivery.DeliveryEvents {
		if scenarios[row.Name] == nil {
			t.Errorf("no scenario for %s", row.Name)
		}
		for _, sc := range scenarios[row.Name] {
			calls := sc.run(t)
			loudness, mentions := row.Loudness, row.Mentions
			if row.LoudWhenFiring && !sc.firing {
				loudness, mentions = groups.Quiet, nil
			}
			want := map[delivery.Form]string{delivery.FormPublication: deliverytest.MethodPublish,
				delivery.FormUpdate: deliverytest.MethodUpdate, delivery.FormNothing: ""}[row.Form]
			got := "nothing"
			switch {
			case want == "" && len(calls) != 0:
				t.Errorf("%s: sent %s", row.Name, methods(calls))
			case want == "":
			case len(calls) != 1 || calls[0].Method != want || calls[0].Loudness != loudness ||
				len(calls[0].Mentions) != len(mentions) || !slices.Equal(calls[0].Mentions, mentions) &&
				len(mentions) > 0:
				t.Errorf("%s (firing %v): want one %s %s %v, got %+v", row.Name, sc.firing, want, loudness, mentions,
					calls)
			default:
				got = fmt.Sprintf("%s %s %v", calls[0].Method, calls[0].Loudness, calls[0].Mentions)
			}
			lines = append(lines, fmt.Sprintf("%s: %s", row.Name, got))
		}
	}
	t.Logf("%d rows of the Loud/Quiet table of delivery events, each as its row says: %s",
		len(delivery.DeliveryEvents), strings.Join(lines, "; "))
}

// The live checks of the review of S-035.

// deleteDest deletes the Destination publicID through deleteDestination's service, reporting a failure to t.
func (l *live) deleteDest(t *testing.T, publicID string) {
	t.Helper()
	if err := l.dsvc.Delete(t.Context(), destinations.Requester{Actor: audit.System,
		Transport: audit.TransportSystem}, publicID, nil); err != nil {
		t.Error(err)
	}
}

// row reads the delivery of the Alert Group publicID in the Destination: its state, whether it has a message,
// whether its final edit waits, its late note, its loudness and its Transient attempts.
type deliveryRow struct {
	state                         string
	message, retire, late, loud   bool
	attempts                      int64
	firstFailed                   bool
	lastErrorClass, lastErrorText string
}

func (l *live) row(t *testing.T, publicID string, dest int64) deliveryRow {
	t.Helper()
	var r deliveryRow
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT d.state, d.message_id IS NOT NULL, d.desired_retire,
		d.late_note, coalesce(d.publication_loud, false), d.attempts, d.first_failed_at IS NOT NULL,
		coalesce(d.last_error_class, ''), coalesce(d.last_error, '')
		FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
		WHERE g.public_id = $1 AND d.destination_id = $2`, publicID, dest).Scan(&r.state, &r.message, &r.retire,
		&r.late, &r.loud, &r.attempts, &r.firstFailed, &r.lastErrorClass, &r.lastErrorText); err != nil {
		t.Fatalf("the delivery of %s: %v", publicID, err)
	}
	return r
}

// resolveBeforeFirstPublication is C-11.FR-11 when no error was ever recorded (last_error_class NULL): an Alert Group
// resolved while its first Publication waited only for limiter tokens, or before it was ever attempted, resolves —
// the dispatcher's transaction commits — and its Publication goes without the late note.
func (l *live) resolveBeforeFirstPublication(t *testing.T) {
	l.fresh(t, 1, 3600)
	l.fire(t, "rb", "EarlyTok/a", "EarlyWait/a")
	l.round(t, l.a)
	if l.rec.Count(deliverytest.MethodPublish) != 1 {
		t.Fatalf("one token: %s", methods(l.rec.Calls()))
	}
	var waiting string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT g.public_id FROM deliveries d JOIN alert_groups g ON
		g.id = d.alert_group_id WHERE d.message_id IS NULL AND d.state = 'pending' AND d.last_error_class IS NULL`).Scan(
		&waiting); err != nil {
		t.Fatalf("the delivery waiting for its token: %v", err)
	}
	l.resolve(t, waiting)
	l.fire(t, "rb2", "EarlyNone/a")
	never := l.group(t, "EarlyNone")
	l.resolve(t, never)
	for _, gid := range []string{waiting, never} {
		r := l.row(t, gid, l.dests[0])
		if r.state != "pending" || r.late || r.lastErrorClass != "" ||
			l.count(t, `SELECT count(*) FROM alert_groups WHERE public_id = $1 AND status = 'resolved'`, gid) != 1 {
			t.Fatalf("%s after the resolve: %+v", gid, r)
		}
	}
	t.Log("resolved while waiting for a token, and before any attempt: both resolves committed; pending, no late note")
}

// inFlightRaces is C-11.FR-14 and FR-16 with a call in flight: a row that ended meanwhile is never revived, a probe's
// success is never lost, a message created on an ended row is kept consistent, and the secrets of a Destination
// deleted meanwhile are wiped.
func (l *live) inFlightRaces(t *testing.T) {
	// The probe's Publication is in flight when its Alert Group resolves: withheld with its lease kept, so the success
	// is recorded and the Destination recovers; the message is then edited to the resolved state.
	l.fresh(t, 1000, 1)
	l.fire(t, "ifr1", "RaceProbe/a")
	gid := l.group(t, "RaceProbe")
	l.brk(t, l.dests[0])
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Then: func() { l.resolve(t, gid) }})
	l.probe(t)
	health, _ := l.health(t, l.dests[0])
	if r := l.row(t, gid, l.dests[0]); health != "healthy" || !r.message || r.state != "delivered" ||
		methods(l.rec.Calls()) != "publish,update" {
		t.Fatalf("probe publication resolved meanwhile: %s %+v %s", health, r, methods(l.rec.Calls()))
	}
	t.Log("probe publish in flight while resolved: success recorded, destination healthy, then one edit")

	// A late Publication is in flight when its Destination is deleted: the row was withheld, but the message now
	// exists, so it gets its final edit.
	l.fresh(t, 1000, 1)
	ds5 := l.newDest(t, 5)
	l.attach(t, ds5)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(time.Second, delivery.ScopeDestination))
	l.fire(t, "ifr2", "RaceLate/a")
	late := l.group(t, "RaceLate")
	l.round(t, l.a)
	l.resolve(t, late)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Then: func() { l.deleteDest(t, "DSAAAAAAAAAAA5") }})
	l.business.Advance(2 * time.Second)
	l.round(t, l.a)
	if r := l.row(t, late, ds5); r.state != "pending" || !r.message || !r.retire {
		t.Fatalf("raced publication: %+v", r)
	}
	l.round(t, l.a)
	calls := l.rec.Calls()
	if r := l.row(t, late, ds5); r.state != "retired" || !isFinalEdit(calls[len(calls)-1], late) {
		t.Fatalf("final edit of the raced publication: %+v %s", r, methods(calls))
	}
	t.Log("late publish in flight while its destination was deleted: message kept, final edit, retired")

	// A Publication in flight when its Destination is deleted, which makes it its final edit, ends unknown: Not
	// delivered, no final edit waits, and the secrets are wiped.
	l.fresh(t, 1000, 1)
	ds6 := l.newDest(t, 6)
	l.exec(t, `UPDATE destinations SET proxy_password_ciphertext = '\x01', proxy_password_key_id = 'k1',
		proxy_password_updated_at = $2 WHERE id = $1`, ds6, l.business.Now())
	l.exec(t, `INSERT INTO destination_secrets (destination_id, org_id, name, value_ciphertext, value_key_id,
		value_updated_at) VALUES ($1, $2, 'token', '\x00', 'k1', $3)`, ds6, l.orgID, l.business.Now())
	l.attach(t, ds6)
	l.fire(t, "ifr3", "RaceUnknown/a")
	unknown := l.group(t, "RaceUnknown")
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{
		Outcome: delivery.Outcome{Kind: delivery.OutcomeUnknown, Error: "odd"}, Then: func() {
			l.deleteDest(t, "DSAAAAAAAAAAA6")
			if r := l.row(t, unknown, ds6); !r.retire {
				t.Errorf("the deletion did not make it the final edit: %+v", r)
			}
		}})
	l.round(t, l.a)
	secrets := l.count(t, `SELECT count(*) FROM destination_secrets WHERE destination_id = $1`, ds6) +
		l.count(t, `SELECT count(*) FROM destinations WHERE id = $1 AND proxy_password_ciphertext IS NOT NULL`, ds6)
	if r := l.row(t, unknown, ds6); r.state != "not_delivered" || r.retire || secrets != 0 {
		t.Fatalf("unknown after the deletion: %+v, secrets %d", r, secrets)
	}
	t.Log("publish in flight while its destination was deleted, answered unknown: not_delivered, no final edit, " +
		"secrets wiped")

	// An edit of a resolved Alert Group is in flight when its Destination is deleted: retired without a call, it stays
	// retired and receives nothing more.
	l.fresh(t, 1000, 1)
	ds7 := l.newDest(t, 7)
	l.attach(t, ds7)
	l.fire(t, "ifr4", "RaceEdit/a")
	edit := l.group(t, "RaceEdit")
	l.round(t, l.a)
	l.resolve(t, edit)
	l.rec.Script(deliverytest.MethodUpdate, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK},
		Then: func() { l.deleteDest(t, "DSAAAAAAAAAAA7") }})
	l.round(t, l.a)
	l.round(t, l.a)
	if r := l.row(t, edit, ds7); r.state != "retired" || methods(l.rec.Calls()) != "publish,update" {
		t.Fatalf("edit in flight while retired: %+v %s", r, methods(l.rec.Calls()))
	}
	t.Log("edit in flight while its destination was deleted: stays retired, nothing more")
}

// lateNoteReopened is C-11.FR-11 when the Alert Group opens again before its late Publication: the note is gone and
// the Publication follows the recovery rule, Loud with new_alert_group while it fires.
func (l *live) lateNoteReopened(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.rec.Script(deliverytest.MethodPublish, deliverytest.RetryAfter(30*time.Second, delivery.ScopeDestination))
	l.fire(t, "lnr", "LateReopen/a")
	gid := l.group(t, "LateReopen")
	l.round(t, l.a)
	l.resolve(t, gid)
	if r := l.row(t, gid, l.dests[0]); !r.late || r.loud {
		t.Fatalf("resolved while waiting: %+v", r)
	}
	if _, err := l.groups.Unresolve(t.Context(), l.alice, gid); err != nil {
		t.Fatal(err)
	}
	if r := l.row(t, gid, l.dests[0]); r.late || !r.loud {
		t.Fatalf("opened again: %+v", r)
	}
	l.business.Advance(31 * time.Second)
	l.round(t, l.a)
	pubs := only(l.rec.Calls(), deliverytest.MethodPublish)
	pub := pubs[len(pubs)-1]
	if len(pubs) != 2 || pub.Loudness != groups.Loud ||
		!slices.Equal(pub.Mentions, []groups.Mention{groups.MentionNewAlertGroup}) ||
		strings.Contains(pub.Message.Text(), "Delivered late") ||
		l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'delivered_late'`) != 0 {
		t.Fatalf("publication after the reopen: %+v", pubs)
	}
	t.Log("resolved while waiting (late note), opened again: published loud [new_alert_group] without the note")
}

// stormMembership is C-11.FR-6 with C-11.FR-14: a Destination added to a Route during its Storm gets the Storm summary,
// Loud, holds the Alert Groups the Storm holds and publishes the others Quietly; removed during the Storm, its summary
// is retired and the Storm's later changes and its end no longer edit it.
func (l *live) stormMembership(t *testing.T) {
	l.fresh(t, 1000, 1)
	routeID, route := l.newRoute(t, "stormjoin", 2, "DSAAAAAAAAAAA1")
	for _, a := range []alert{{name: "SJ0/a", team: "stormjoin"}, {name: "SJ1/a", team: "stormjoin"},
		{name: "SJ2/a", team: "stormjoin"}, {name: "SJ3/a", team: "stormjoin", severity: "critical"},
		{name: "SJ4/a", team: "stormjoin"}} {
		l.fireAlerts(t, "sj-"+a.name, a)
		l.business.Advance(time.Second)
	}
	l.round(t, l.a)
	if l.count(t, `SELECT count(*) FROM storms WHERE route_id = $1 AND ended_at IS NULL`, routeID) != 1 {
		t.Fatal("no storm")
	}
	l.rec.Reset()
	l.routeEdit(t, route, "DSAAAAAAAAAAA1", "DSAAAAAAAAAAA2")
	l.round(t, l.a)
	var quiet []string
	summaries := 0
	for _, c := range to(l.rec.Calls(), "DSAAAAAAAAAAA2") {
		switch {
		case c.Method != deliverytest.MethodPublish:
			t.Errorf("joined: %s", c.Method)
		case strings.HasPrefix(c.Message.Text(), "⛈ Storm on"):
			summaries++
			if c.Loudness != groups.Loud {
				t.Errorf("summary %+v", c)
			}
		case c.Loudness == groups.Quiet:
			quiet = append(quiet, strings.Fields(head(c.Message))[1])
		default:
			t.Errorf("loud publication %+v", c)
		}
	}
	slices.Sort(quiet)
	held := l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND held_by_storm_id IS NOT NULL`,
		l.dests[1])
	if summaries != 1 || !slices.Equal(quiet, []string{"SJ0", "SJ1", "SJ3"}) || held != 2 ||
		len(to(l.rec.Calls(), "DSAAAAAAAAAAA1")) != 0 {
		t.Fatalf("joined during the storm: summary %d, quiet %v, held %d, %s", summaries, quiet, held,
			methods(l.rec.Calls()))
	}
	t.Logf("joined during the storm: summary publish loud; quiet publish=%d; held=%d", len(quiet), held)
	l.rec.Reset()
	l.routeEdit(t, route, "DSAAAAAAAAAAA1")
	if l.count(t, `SELECT count(*) FROM deliveries WHERE destination_id = $1 AND storm_id IS NOT NULL
		AND state = 'retired'`, l.dests[1]) != 1 {
		t.Fatal("the summary of the destination that left is not retired")
	}
	l.fireAlerts(t, "sj-5", alert{name: "SJ5/a", team: "stormjoin"})
	l.calm(t, routeID)
	l.round(t, l.a)
	left := to(l.rec.Calls(), "DSAAAAAAAAAAA2")
	for _, c := range left {
		if !strings.Contains(c.Message.Text(), "No longer updated here") {
			t.Errorf("a call to the destination that left: %s %q", c.Method, c.Message.Text())
		}
	}
	if len(left) != 3 || !slices.ContainsFunc(to(l.rec.Calls(), "DSAAAAAAAAAAA1"), func(c deliverytest.Call) bool {
		return strings.HasPrefix(c.Message.Text(), "Storm over")
	}) {
		t.Fatalf("after leaving: %s", methods(l.rec.Calls()))
	}
	t.Logf("left during the storm: final edit=%d, summary retired, no summary edit there at the end", len(left))
}

// stormSummaryAfterEnd is C-11.FR-6: a Storm summary first published after its Storm ended is Quiet.
func (l *live) stormSummaryAfterEnd(t *testing.T) {
	l.fresh(t, 1000, 1)
	routeID, _ := l.newRoute(t, "stormquiet", 1, "DSAAAAAAAAAAA1")
	l.fireAlerts(t, "sq", alert{name: "SQ0/a", team: "stormquiet"}, alert{name: "SQ1/a", team: "stormquiet"})
	l.calm(t, routeID)
	l.round(t, l.a)
	var summary []deliverytest.Call
	for _, c := range only(l.rec.Calls(), deliverytest.MethodPublish) {
		if c.Message.Kind == messages.KindStorm {
			summary = append(summary, c)
		}
	}
	if len(summary) != 1 || !strings.HasPrefix(summary[0].Message.Text(), "Storm over") ||
		summary[0].Loudness != groups.Quiet || len(summary[0].Mentions) != 0 || l.count(t,
		`SELECT count(*) FROM delivery_events WHERE kind = 'storm_summary' AND loudness = 'quiet'`) != 1 {
		t.Fatalf("summary first published after the end: %+v", summary)
	}
	t.Log("summary first published after its storm ended: quiet, no mentions")
}

// deletedBrokenFinalEdit: a probe whose final edit reaches a deleted Broken Destination ends its Broken state without
// a destination_recovered event or line.
func (l *live) deletedBrokenFinalEdit(t *testing.T) {
	l.fresh(t, 1000, 1)
	ds8 := l.newDest(t, 8)
	l.attach(t, ds8)
	l.fire(t, "dbf", "DeletedBroken/a")
	gid := l.group(t, "DeletedBroken")
	l.round(t, l.a)
	l.brk(t, ds8)
	l.deleteDest(t, "DSAAAAAAAAAAA8")
	l.rec.Reset()
	l.probe(t)
	health, _ := l.health(t, ds8)
	calls := l.rec.Calls()
	if len(calls) != 1 || !isFinalEdit(calls[0], gid) || l.row(t, gid, ds8).state != "retired" || health != "healthy" {
		t.Fatalf("probe of the deleted destination: %s %s", methods(calls), health)
	}
	if l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'destination_recovered'
		AND destination_id = $1`, ds8) != 0 ||
		strings.Contains(l.log.String(), `"event":"destination_recovered","destination":"DSAAAAAAAAAAA8"`) {
		t.Fatal("a deleted destination recovered")
	}
	t.Log("final edit reached a deleted broken destination: retired, healthy, no destination_recovered")
}

// destinationDeletedBumpsRoutes is C-11.FR-14 on the Routes: deleting a Destination gives each Route it belonged to a
// new version without it; another Route keeps its version.
func (l *live) destinationDeletedBumpsRoutes(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.newDest(t, 9)
	_, r1 := l.newRoute(t, "bump1", 1_000_000, "DSAAAAAAAAAAA9")
	_, r2 := l.newRoute(t, "bump2", 1_000_000, "DSAAAAAAAAAAA9", "DSAAAAAAAAAAA1")
	_, r3 := l.newRoute(t, "bump3", 1_000_000, "DSAAAAAAAAAAA1")
	before := map[string]int64{}
	for _, id := range []string{r1, r2, r3} {
		rt, err := l.routes.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		before[id] = rt.Version
	}
	l.deleteDest(t, "DSAAAAAAAAAAA9")
	for id, want := range map[string][]string{r1: {}, r2: {"DSAAAAAAAAAAA1"}, r3: {"DSAAAAAAAAAAA1"}} {
		rt, err := l.routes.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		bumped := rt.Version == before[id]+1
		if !slices.Equal(rt.DestinationIDs, want) || bumped != (id != r3) || (id == r3 && rt.Version != before[id]) {
			t.Errorf("%s: version %d → %d, destinations %v", rt.Name, before[id], rt.Version, rt.DestinationIDs)
		}
	}
	t.Log("deleteDestination: its two Routes got a new version without it; the third kept its version")
}

// membershipLock is C-11.FR-14 between an Enqueue and a change of the Route's Destinations: while a change holds the
// Route's membership lock, the re-render of a new Alert Group of the Route waits, and goes on once it commits.
func (l *live) membershipLock(t *testing.T) {
	l.fresh(t, 1000, 1)
	tx, err := l.d.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1, hashint8($2::bigint))`,
		db.RouteMembershipLockClass, l.routeID); err != nil {
		t.Fatal(err)
	}
	l.storeAlerts(t, "ml", alert{name: "Membership/a", team: "db"})
	done := make(chan error, 1)
	go func() { done <- l.drain(t.Context()) }()
	waiting := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && !waiting; {
		waiting = l.count(t, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
			AND classid = $1::bigint::oid`, int64(uint32(db.RouteMembershipLockClass))) == 1
		select {
		case err := <-done:
			t.Fatalf("the re-render did not wait: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !waiting || l.count(t, `SELECT count(*) FROM deliveries`) != 0 {
		t.Fatal("the re-render does not wait for the membership lock")
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if l.count(t, `SELECT count(*) FROM deliveries`) != 1 {
		t.Fatal("no delivery after the change committed")
	}
	t.Log("an Enqueue on the Route waited for the membership lock of a change of its Destinations, then delivered")
}

// budgetResetOnRecovery is C-11.FR-8 and FR-19: breaking a Destination leaves the Transient budget of its other
// waiting deliveries alone — another replica may hold them — and the recovery gives them a fresh one.
func (l *live) budgetResetOnRecovery(t *testing.T) {
	l.fresh(t, 2, 3600)
	l.fire(t, "brr", "BudgetA/a", "BudgetB/a")
	a, b := l.group(t, "BudgetA"), l.group(t, "BudgetB")
	l.exec(t, `UPDATE deliveries d SET attempts = 3, first_failed_at = $2, next_attempt_at = $3 FROM alert_groups g
		WHERE g.id = d.alert_group_id AND g.public_id = $1`, b, l.business.Now(), l.business.Now().Add(time.Hour))
	l.rec.Script(deliverytest.MethodPublish, deliverytest.Failure(delivery.OutcomeFatal, "HTTP 403"))
	l.round(t, l.a)
	health, _ := l.health(t, l.dests[0])
	if r := l.row(t, b, l.dests[0]); health != "broken" || r.attempts != 3 || !r.firstFailed {
		t.Fatalf("breaking touched another row: %s %+v", health, r)
	}
	l.probe(t)
	health, _ = l.health(t, l.dests[0])
	if r := l.row(t, b, l.dests[0]); health != "healthy" || r.attempts != 0 || r.firstFailed ||
		l.row(t, a, l.dests[0]).state != "delivered" {
		t.Fatalf("no fresh budget after the recovery: %s %+v", health, r)
	}
	t.Log("broken by A: B kept attempts=3; recovered: B attempts=0")
}

// abandonConnection is C-11.FR-14 for deleteConnection (S-039): the final edits still pending in its deleted
// Destinations end Not delivered, each logged as delivery_not_delivered once the transaction committed.
func (l *live) abandonConnection(t *testing.T) {
	l.fresh(t, 1000, 1)
	ds := l.newDestNamed(t, "DSAAAAAAAAAAB1")
	l.attach(t, ds)
	l.fire(t, "ab", "Abandon/a")
	gid := l.group(t, "Abandon")
	l.round(t, l.a)
	l.deleteDest(t, "DSAAAAAAAAAAB1")
	l.log.Reset()
	var committed func(context.Context)
	if err := pgxTx(t, l, func(tx groups.DBTX) error {
		var err error
		committed, err = l.svc.AbandonConnection(t.Context(), tx, l.conn)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(l.log.String(), "delivery_not_delivered") {
		t.Fatal("logged before the commit")
	}
	committed(t.Context())
	r := l.row(t, gid, ds)
	if r.state != "not_delivered" || r.retire || r.lastErrorText != "the Connection was deleted" ||
		!strings.Contains(l.log.String(), `"event":"delivery_not_delivered","destination":"DSAAAAAAAAAAB1","group":"`+
			gid+`","kind":"final_edit","error_class":"unknown"`) {
		t.Fatalf("abandoned %+v, log %s", r, l.log)
	}
	t.Log("deleted connection: the pending final edit is not_delivered, logged after the commit")
}

// fallbackTemplate is C-12.AC-3: a Route template that passed its dry run against the Route's Stored Snapshot of two
// Alerts fails on an Alert Group of one Alert; that message is the Fallback template, muster_template_errors_total
// grows, the Route shows its template error, the Timeline records fallback_template_used and MusterTemplateError
// fires; the next good render clears the error and resolves the Internal alert.
func (l *live) fallbackTemplate(t *testing.T) {
	l.fresh(t, 1000, 1)
	id, publicID := l.newRoute(t, "fb", 1_000_000, "DSAAAAAAAAAAA1")
	l.fireAlerts(t, "fb1", alert{name: "Fb/a", team: "fb"}, alert{name: "Fb/b", team: "fb"})
	rt, err := l.routes.Get(t.Context(), publicID)
	if err != nil {
		t.Fatal(err)
	}
	in := routing.Input{Name: rt.Name, Matchers: rt.Matchers, GroupKey: rt.GroupKey, DestinationIDs: rt.DestinationIDs,
		Policy: rt.Policy}
	in.Policy.Templates.RootMessage = ptrTo(`second alert {{ (index .Alerts 1).Labels.disk }}`)
	if _, err := l.routes.Update(t.Context(), system, publicID, nil, in); err != nil {
		t.Fatalf("the template failed its dry run: %v", err)
	}
	bad := in
	bad.Policy.Templates.RootMessage = ptrTo(`{{ env "HOME" }}`)
	if _, err := l.routes.Update(t.Context(), system, publicID, nil, bad); err == nil {
		t.Fatal("a template with env was saved")
	}
	counter := func() uint64 { return metrics.TemplateErrors.With(publicID, "", "root_message").Get() }
	before := counter()
	l.round(t, l.a)
	l.rec.Reset()
	l.fireAlerts(t, "fb2", alert{name: "FbOne/a", team: "fb"})
	l.round(t, l.a)
	gid := l.group(t, "FbOne")
	pubs := only(l.rec.Calls(), deliverytest.MethodPublish)
	if len(pubs) != 1 || head(pubs[0].Message) == "" || len(pubs[0].Message.Buttons) != 5 ||
		!strings.Contains(pubs[0].Message.Text(), "The message template of this Route failed") ||
		!strings.Contains(pubs[0].Message.Text(), "disk=FbOne/a") || pubs[0].Message.Body != nil {
		t.Fatalf("fallback publication %+v", pubs)
	}
	var errTemplate, detail string
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT r.template_error_template, e.detail FROM routes r,
		timeline_entries e JOIN alert_groups g ON g.id = e.alert_group_id WHERE r.id = $1 AND g.public_id = $2
		AND e.system_event = 'fallback_template_used'`, id, gid).Scan(&errTemplate, &detail); err != nil {
		t.Fatal(err)
	}
	route, err := l.routes.Get(t.Context(), publicID)
	if err != nil || errTemplate != "root_message" || route.TemplateError == nil ||
		!strings.HasPrefix(detail, "root_message template failed: line 1, column ") || counter() != before+1 ||
		l.templateAlert(t, publicID) != "firing" || !strings.Contains(l.log.String(), `"event":"fallback_template_used"`) {
		t.Fatalf("error state %q, route %+v, detail %q, counter +%d, internal alert %q", errTemplate,
			route.TemplateError, detail, counter()-before, l.templateAlert(t, publicID))
	}
	t.Logf("template passed its dry run (2 Alerts); Alert Group with 1 Alert: fallback used; "+
		"muster_template_errors_total{template=\"root_message\"} +%d; route template_error set (%s); timeline "+
		"system_event=fallback_template_used (%q); MusterTemplateError %s", counter()-before, errTemplate, detail,
		l.templateAlert(t, publicID))
	// The next Alert Group renders again: the error clears and the Internal alert resolves.
	l.rec.Reset()
	l.fireAlerts(t, "fb3", alert{name: "FbTwo/a", team: "fb"}, alert{name: "FbTwo/b", team: "fb"})
	l.round(t, l.a)
	pubs = only(l.rec.Calls(), deliverytest.MethodPublish)
	if len(pubs) != 1 || pubs[0].Message.Body == nil || pubs[0].Message.Body.Text != `second alert FbTwo/a` ||
		l.count(t, `SELECT count(*) FROM routes WHERE id = $1 AND template_error IS NULL`, id) != 1 ||
		l.templateAlert(t, publicID) != "resolved" || !strings.Contains(l.log.String(), `"event":"template_recovered"`) {
		t.Fatalf("recovery %+v, internal alert %q", pubs, l.templateAlert(t, publicID))
	}
	t.Log("next good render: cleared and resolved")
}

// templateAlert is the status of MusterTemplateError about the Route publicID, after processing the synthetic
// Snapshots, empty when it never fired.
func (l *live) templateAlert(t *testing.T, publicID string) string {
	t.Helper()
	l.process(t)
	var status string
	err := l.d.Pool.QueryRow(t.Context(), `SELECT status FROM alerts WHERE labels->>'alertname' =
		'MusterTemplateError' AND labels->>'route' = $1 ORDER BY id DESC LIMIT 1`, publicID).Scan(&status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	return status
}

func ptrTo(s string) *string { return &s }

// russianReplies is C-12.AC-5: with route.language ru, the Root message and the Thread replies are in Russian.
func (l *live) russianReplies(t *testing.T) {
	l.fresh(t, 1000, 1)
	id, _ := l.newRoute(t, "ru", 1_000_000, "DSAAAAAAAAAAA1")
	l.exec(t, `UPDATE routes SET language = 'ru' WHERE id = $1`, id)
	l.fireAlerts(t, "ru1", alert{name: "Ru/a", team: "ru"})
	l.round(t, l.a)
	l.business.Advance(2 * time.Minute)
	l.fireAlerts(t, "ru1", alert{name: "Ru/a", team: "ru"}, alert{name: "Ru/b", team: "ru"},
		alert{name: "Ru/c", team: "ru"})
	l.round(t, l.a)
	pubs, replies := only(l.rec.Calls(), deliverytest.MethodPublish), only(l.rec.Calls(), deliverytest.MethodReply)
	if len(pubs) != 1 || len(replies) != 1 {
		t.Fatalf("calls %s", methods(l.rec.Calls()))
	}
	root, reply := pubs[0].Message, replies[0].Message
	if !strings.Contains(root.Environment, "Начало: ") || root.Buttons[0].Label != "Подтвердить" ||
		root.Links[0].Text != "Открыть в Muster" || reply.Lines[0] != "Новые алерты (2):" ||
		!strings.HasPrefix(reply.Lines[1], "• disk: Ru/") {
		t.Fatalf("root %q, reply %q", root.Text(), reply.Lines)
	}
	l.resolve(t, l.group(t, "Ru"))
	l.round(t, l.a)
	updates := only(l.rec.Calls(), deliverytest.MethodUpdate)
	if len(updates) == 0 || updates[len(updates)-1].Message.Footer != "Закрыл(а): alice" {
		t.Fatalf("updates %+v", updates)
	}
	t.Logf("thread reply: %q; root message: %q; footer %q", strings.Join(reply.Lines, " "), root.Environment,
		updates[len(updates)-1].Message.Footer)
}

// stormTexts is C-12.FR-10: the Storm summary and its final state read as the PRD says.
func (l *live) stormTexts(t *testing.T) {
	l.fresh(t, 1000, 1)
	id, _ := l.newRoute(t, "st", 1, "DSAAAAAAAAAAA1")
	l.fireAlerts(t, "st1", alert{name: "StA/a", team: "st"}, alert{name: "StB/a", team: "st", severity: "critical"},
		alert{name: "StC/a", team: "st"})
	l.round(t, l.a)
	var summary string
	for _, c := range l.rec.Calls() {
		if c.Message.Kind == messages.KindStorm {
			summary = c.Message.Lines[0]
		}
	}
	if !strings.HasPrefix(summary, "⛈ Storm on st: ") || !strings.HasSuffix(summary, " — open in Muster") {
		t.Fatalf("summary %q in %s", summary, methods(l.rec.Calls()))
	}
	l.rec.Reset()
	l.calm(t, id)
	l.round(t, l.a)
	var over string
	for _, c := range l.rec.Calls() {
		if c.Message.Kind == messages.KindStorm {
			over = c.Message.Lines[0]
		}
	}
	// The final state counts the Alert Groups the Storm held that are still open: the second one; the first was
	// published before the Storm and the Urgent third was released at once.
	if over != "Storm over: 1 Alert Group still open" {
		t.Fatalf("final state %q", over)
	}
	t.Logf("%q … %q", summary, over)
}

// mentionTargets is C-12.FR-8 and C-12.FR-12 through the recording adapter: a Destination whose new_alert_group
// mentions the channel and whose new_alerts mention Alice, who has an Account link in its identity space. A firing
// `created` carries everyone:channel; a Loud `alerts_added` carries Alice by her username; once Alice acknowledged,
// the footer names her by her username in that identity space and by her display name elsewhere; an `alerts_added` on
// the acknowledged Alert Group carries none; a Reopen into acknowledged carries only the Owner, although the reopen
// setting mentions the channel.
func (l *live) mentionTargets(t *testing.T) {
	l.fresh(t, 1000, 1)
	nobody := `{"everyone":"none","user_ids":[],"groups":[]}`
	l.exec(t, `UPDATE destinations SET mentions = $1 WHERE id = $2`, `{"new_alert_group":{"everyone":"channel",
		"user_ids":[],"groups":[]},"new_alerts":{"everyone":"none","user_ids":["sraaaaaaaaaaa1"],"groups":[]},
		"reopen":{"everyone":"channel","user_ids":[],"groups":[]},"ack_timeout":`+nobody+`,"snooze_ended":`+nobody+
		`,"rise_to_urgent":`+nobody+`}`, l.dests[0])
	l.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		created_at) SELECT $1, 'AKAAAAAAAAAAA1', id, 'mattermost', $2, 'mm-alice', 'alice.mm', $3 FROM users
		WHERE public_id = 'SRAAAAAAAAAAA1'`, l.orgID, l.conn, t0)
	t.Cleanup(func() {
		for _, stmt := range []string{`UPDATE destinations SET mentions = '{}'`, `DELETE FROM account_links`} {
			if _, err := l.d.Pool.Exec(context.Background(), stmt); err != nil {
				t.Error(err)
			}
		}
	})
	targets := func(c deliverytest.Call) string {
		var out []string
		for _, tg := range c.Targets {
			switch tg.Kind {
			case mentions.TargetUser:
				out = append(out, "user:"+tg.User.Display())
			default:
				out = append(out, tg.Kind+":"+tg.Everyone+tg.Group)
			}
		}
		return strings.Join(out, ",")
	}
	last := func(method string) deliverytest.Call {
		calls := only(l.rec.Calls(), method)
		if len(calls) == 0 {
			t.Fatalf("no %s: %s", method, methods(l.rec.Calls()))
		}
		return calls[len(calls)-1]
	}
	fire := func(names ...string) {
		alerts := make([]alert, len(names))
		for i, n := range names {
			alerts[i] = alert{name: "Mention/" + n, team: "db"}
		}
		l.fireAlerts(t, "men", alerts...)
		l.round(t, l.a)
	}
	fire("a")
	gid := l.group(t, "Mention")
	if pub := last(deliverytest.MethodPublish); pub.Loudness != groups.Loud || targets(pub) != "everyone:channel" {
		t.Fatalf("created: %s %q", pub.Loudness, targets(pub))
	}
	l.business.Advance(2 * time.Minute)
	fire("a", "b")
	if r := last(deliverytest.MethodReply); r.Loudness != groups.Loud || targets(r) != "user:alice.mm" ||
		r.Targets[0].User.PublicID != "SRAAAAAAAAAAA1" || r.Targets[0].User.Name != "alice" {
		t.Fatalf("firing alerts_added: %s %q", r.Loudness, targets(r))
	}
	if _, err := l.groups.Acknowledge(t.Context(), l.alice, gid); err != nil {
		t.Fatal(err)
	}
	l.round(t, l.a)
	space := fmt.Sprintf("mattermost:%d", l.conn)
	if u := last(deliverytest.MethodUpdate); u.Loudness != groups.Quiet || u.Targets != nil ||
		u.Message.FooterIn(space) != "Acknowledged by alice.mm" || u.Message.Footer != "Acknowledged by alice" ||
		u.Message.FooterIn("telegram") != "Acknowledged by alice" {
		t.Fatalf("acknowledged: %q %q %v", u.Message.Footer, u.Message.FooterIn(space), u.Targets)
	}
	l.business.Advance(2 * time.Minute)
	fire("a", "b", "c")
	if r := last(deliverytest.MethodReply); r.Loudness != groups.Quiet || r.Targets != nil {
		t.Fatalf("alerts_added on acknowledged: %s %q", r.Loudness, targets(r))
	}
	l.business.Advance(2 * time.Minute)
	var resolved []string
	for _, n := range []string{"a", "b", "c"} {
		resolved = append(resolved, `{"status":"resolved","labels":{"alertname":"Mention","team":"db","disk":"Mention/`+
			n+`"},"annotations":{},"startsAt":"2026-10-07T11:00:00Z","endsAt":"2026-10-07T11:30:00Z"}`)
	}
	if _, err := l.snaps.Store(t.Context(), ingest.Received{IntegrationID: l.intID,
		Body: []byte(`{"groupKey":"men","status":"resolved","alerts":[` + strings.Join(resolved, ",") + `]}`)}); err != nil {
		t.Fatal(err)
	}
	l.process(t)
	l.round(t, l.a)
	if st := l.count(t, `SELECT count(*) FROM alert_groups WHERE public_id = $1 AND status = 'resolved'`, gid); st != 1 {
		t.Fatalf("not resolved: %s", methods(l.rec.Calls()))
	}
	l.business.Advance(time.Minute)
	l.rec.Reset()
	if _, err := l.snaps.Store(t.Context(), ingest.Received{IntegrationID: l.intID,
		Body: []byte(`{"groupKey":"men","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"Mention",` +
			`"team":"db","disk":"Mention/a"},"annotations":{},"startsAt":"2026-10-07T11:45:00Z"}]}`)}); err != nil {
		t.Fatal(err)
	}
	l.process(t)
	l.round(t, l.a)
	r := last(deliverytest.MethodReply)
	if r.Loudness != groups.Loud || !slices.Equal(r.Mentions, []groups.Mention{groups.MentionOwner}) ||
		targets(r) != "user:alice.mm" {
		t.Fatalf("reopened into acknowledged: %s %v %q", r.Loudness, r.Mentions, targets(r))
	}
	t.Logf("created → %q; firing alerts_added → %q; footer %q in %s; alerts_added on acknowledged → none; "+
		"reopen into acknowledged → [owner] → %q", "everyone:channel", "user:alice.mm", "Acknowledged by alice.mm",
		space, targets(r))
}

// pressBinding is the binding of a Mattermost button press (C-13.FR-4, AC-11): the post of the Alert Group's delivery
// to a Destination of the Connection binds the press, with the Destination's channel and the Route's language and
// Snooze durations; another post, another Connection or a deleted Destination does not; the Destination of a post of
// the Connection is found by the post and its channel.
func (l *live) pressBinding(t *testing.T) {
	l.fresh(t, 1000, 1)
	l.fire(t, "press", "Press/a")
	gid := l.group(t, "Press")
	l.round(t, l.a)
	l.exec(t, `UPDATE deliveries SET message_id = 'post-press' WHERE org_id = $1 AND destination_id = $2`, l.orgID,
		l.dests[0])
	var snooze []int64
	if err := l.d.Pool.QueryRow(t.Context(), `SELECT snooze_durations_seconds FROM routes WHERE id = $1`,
		l.routeID).Scan(&snooze); err != nil {
		t.Fatal(err)
	}
	b, err := l.svc.PressBinding(t.Context(), l.conn, gid, "post-press")
	if err != nil || b.Destination.ID != l.dests[0] || b.Destination.PublicID != "DSAAAAAAAAAAA1" ||
		b.Destination.Name != "ops-1" || *b.Destination.Connection != l.conn || b.ChannelID != "chan1" ||
		b.Language != "en" || !slices.Equal(b.SnoozeSeconds, snooze) || len(snooze) == 0 {
		t.Fatalf("PressBinding = %+v, %v; snooze %v", b, err, snooze)
	}
	for _, c := range []struct {
		conn        int64
		group, post string
	}{{l.conn, gid, "post-other"}, {l.conn + 1000, gid, "post-press"}, {l.conn, "AG0000000000ZZ", "post-press"}} {
		if _, err := l.svc.PressBinding(t.Context(), c.conn, c.group, c.post); !errors.Is(err, delivery.ErrNotBound) {
			t.Errorf("PressBinding(%d, %s, %s) = %v", c.conn, c.group, c.post, err)
		}
	}
	d, ok, err := l.svc.PostDestination(t.Context(), l.conn, "post-press", "chan1")
	if err != nil || !ok || d.ID != l.dests[0] || d.PublicID != "DSAAAAAAAAAAA1" {
		t.Errorf("PostDestination(post-press, chan1) = %+v, %v, %v", d, ok, err)
	}
	for _, c := range []struct{ post, channel string }{{"post-press", "chan2"}, {"post-garbage", "chan1"},
		{"post-press", "chan-foreign"}} {
		if _, ok, err := l.svc.PostDestination(t.Context(), l.conn, c.post, c.channel); ok || err != nil {
			t.Errorf("PostDestination(%s, %s) = %v, %v", c.post, c.channel, ok, err)
		}
	}
	l.exec(t, `UPDATE destinations SET deleted_at = $2 WHERE org_id = $1 AND id = $3`, l.orgID, l.business.Now(),
		l.dests[0])
	defer l.exec(t, `UPDATE destinations SET deleted_at = NULL WHERE org_id = $1 AND id = $2`, l.orgID, l.dests[0])
	if _, err := l.svc.PressBinding(t.Context(), l.conn, gid, "post-press"); !errors.Is(err, delivery.ErrNotBound) {
		t.Errorf("a deleted Destination = %v", err)
	}
	if _, ok, err := l.svc.PostDestination(t.Context(), l.conn, "post-press", "chan1"); ok || err != nil {
		t.Errorf("the post of a deleted Destination = %v, %v", ok, err)
	}
	t.Log("press binding: post, connection, channel and route values read from PostgreSQL")
}

// webhookEvents checks the events mode of outgoing webhooks on PostgreSQL (C-15.FR-2, FR-12, C-11.FR-9): only the head
// event of an Alert Group and Destination is claimed, so an event answered with Retry-After holds back the next one of
// its Alert Group but not those of another, and its retry keeps its webhook-id; the probe of a Broken Destination
// leases its oldest waiting event, and waits while another replica holds it; deleting the Destination ends its waiting
// events and keeps its secrets while a call holds a lease, and the retention wipes them once the lease ran out.
func (l *live) webhookEvents(t *testing.T) {
	l.fresh(t, 1000, 1)
	ctx := t.Context()
	var dest int64
	if err := l.d.Pool.QueryRow(ctx, `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode,
		webhook_events_config, signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at, mentions,
		limiter_limit, limiter_per_seconds, health, created_at, updated_at)
		VALUES ($1, 'DSAAAAAAAAAAWH', 'webhook', 'hook', 'events', '{"url":"http://127.0.0.1/","headers":[]}', '\x01',
		'k1', $2, '{}', 1000, 1, 'healthy', $2, $2) RETURNING id`, l.orgID, l.business.Now()).Scan(&dest); err != nil {
		t.Fatal(err)
	}
	l.exec(t, `INSERT INTO destination_secrets (destination_id, org_id, name, value_ciphertext, value_key_id,
		value_updated_at) VALUES ($1, $2, 'token', '\x01', 'k1', $3)`, dest, l.orgID, l.business.Now())
	l.attach(t, dest)
	svc := delivery.New(delivery.Config{OrgID: l.orgID, Store: delivery.NewStore(l.d.Pool, l.d.Pool),
		Business: l.business, Real: l.real, Log: l.logger, Renderer: delivery.MessageRenderer{Renderer: l.renderer},
		Bodies: &stubBodies{}})
	l.groups.SetRerender(svc.Enqueue)
	defer l.groups.SetRerender(l.svc.Enqueue)
	sender := &fakeSender{answers: []delivery.Outcome{{Kind: delivery.OutcomeRetryAfter, RetryAfter: 10 * time.Second,
		Status: 503}}}
	w := l.worker("r1")
	w.Events = sender
	l.fire(t, "wh-a", "WhAlpha")
	alpha := l.group(t, "WhAlpha")
	l.ack(t, alpha)
	l.fire(t, "wh-b", "WhBeta")
	if n := l.count(t, `SELECT count(*) FROM webhook_events WHERE destination_id = $1 AND state = 'pending'`,
		dest); n != 3 {
		t.Fatalf("queued %d events", n)
	}
	l.round(t, w)
	got := sender.sent(t)
	if len(got) != 2 || !strings.HasPrefix(got[0], "created@") || !strings.HasPrefix(got[1], "created@") ||
		got[0] == got[1] {
		t.Fatalf("sent %v", got)
	}
	if n := l.count(t, `SELECT count(*) FROM webhook_events WHERE destination_id = $1 AND event = 'acknowledged'
		AND state = 'pending' AND attempts = 0 AND lease_owner IS NULL`, dest); n != 1 {
		t.Fatal("the acknowledged event did not wait for the created event of its Alert Group")
	}
	l.business.Advance(10 * time.Second)
	for range 3 {
		l.round(t, w)
	}
	got = sender.sent(t)
	if len(got) != 4 || got[2] != got[0] || !strings.HasPrefix(got[3], "acknowledged@") ||
		sender.calls[2].WebhookID != sender.calls[0].WebhookID {
		t.Fatalf("after the Retry-After: %v", got)
	}

	// The probe leases the oldest waiting event, and waits while another replica holds it.
	l.brk(t, dest)
	l.fire(t, "wh-c", "WhGamma")
	l.exec(t, `UPDATE webhook_events SET lease_owner = 'other', lease_until = $2 WHERE destination_id = $1
		AND state = 'pending'`, dest, l.real.Now().Add(time.Minute))
	l.business.Advance(delivery.BrokenProbeInterval)
	l.round(t, w)
	if len(sender.calls) != 4 {
		t.Fatal("the probe sent an event another replica holds")
	}
	l.exec(t, `UPDATE webhook_events SET lease_owner = NULL, lease_until = NULL WHERE destination_id = $1`, dest)
	l.business.Advance(delivery.BrokenProbeInterval)
	l.round(t, w)
	if health, _ := l.health(t, dest); len(sender.calls) != 5 || health != "healthy" {
		t.Fatalf("probe: %d calls, %s", len(sender.calls), health)
	}

	// Deleted while a call holds a lease: the events end, the secrets wait for the lease.
	l.brk(t, dest)
	l.fire(t, "wh-d", "WhDelta")
	l.exec(t, `UPDATE webhook_events SET lease_owner = 'r9', lease_until = $2 WHERE destination_id = $1
		AND state = 'pending'`, dest, l.real.Now().Add(time.Minute))
	l.exec(t, `UPDATE destinations SET deleted_at = $2 WHERE id = $1`, dest, l.business.Now())
	if err := pgxTx(t, l, func(tx groups.DBTX) error { return svc.RetireDestination(ctx, tx, dest) }); err != nil {
		t.Fatal(err)
	}
	secrets := func() int64 {
		return l.count(t, `SELECT (signing_secret_ciphertext IS NOT NULL)::int + (SELECT count(*) FROM
			destination_secrets s WHERE s.destination_id = d.id) FROM destinations d WHERE d.id = $1`, dest)
	}
	if n := l.count(t, `SELECT count(*) FROM webhook_events WHERE destination_id = $1 AND state = 'not_delivered'
		AND last_error = 'the Destination was deleted'`, dest); n != 1 || secrets() != 2 {
		t.Fatalf("abandoned %d, secrets %d", n, secrets())
	}
	l.real.Advance(2 * time.Minute)
	if _, err := svc.PruneWebhookEvents(ctx, l.business.Now()); err != nil {
		t.Fatal(err)
	}
	if secrets() != 0 {
		t.Fatal("the secrets outlived the lease")
	}
}

// telegramThreads checks Telegram comment Threads on PostgreSQL (C-14.FR-3, AC-2, AC-16): the copy and the
// Publication meet in telegram_post_copies in every order — the copy first, the copy after, both waiting on the lock
// of the post at once, and the copy found only by the due Thread reply; a reply waits telegram.copy_wait for the copy,
// then starts an unattached chain with the thread_not_attached delivery event, and a comment's copy attaches the
// replies after it; a lost Thread resends without its link and stays healthy; and short-lived pruning deletes the
// copies after a day.
func (l *live) telegramThreads(t *testing.T) {
	l.fresh(t, 1000, 1)
	ctx := t.Context()
	const channel, group = int64(-1001000000001), int64(-1001000000002)
	var conn, dest int64
	if err := l.d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name,
		telegram_bot_api_base_url, telegram_update_mode, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at,
		limiter_limit, limiter_per_seconds, created_at, updated_at) VALUES ($1, 'CNAAAAAAAAAAT1', 'telegram', 'tg',
		'https://api.telegram.org', 'long_polling', '\x00', 'k1', $2, 1000, 1, $2, $2) RETURNING id`, l.orgID,
		t0).Scan(&conn); err != nil {
		t.Fatal(err)
	}
	if err := l.d.Pool.QueryRow(ctx, `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
		telegram_channel_id, telegram_channel_chat_id, telegram_discussion_chat_id, mentions, limiter_limit,
		limiter_per_seconds, health, created_at, updated_at) VALUES ($1, 'DSAAAAAAAAAAT1', 'telegram', 'alerts', $2,
		'@muster_alerts', $3, $4, '{}', 1000, 1, 'healthy', $5, $5) RETURNING id`, l.orgID, conn, channel, group,
		t0).Scan(&dest); err != nil {
		t.Fatal(err)
	}
	l.attach(t, dest)
	defer l.exec(t, `DELETE FROM telegram_post_copies`)
	w := l.worker("tg")
	w.Adapters[delivery.TypeTelegram] = l.rec
	learn := func(post, cp int64, from string) error {
		return l.svc.LearnCopy(ctx, delivery.Copy{ConnectionID: conn, ChannelChatID: channel, PostID: post,
			DiscussionChatID: group, CopyID: cp, LearnedFrom: from})
	}
	posted := func(post string) {
		l.rec.Script(deliverytest.MethodPublish, deliverytest.Answer{Outcome: delivery.Outcome{
			Kind: delivery.OutcomeOK, MessageID: post}})
	}
	type thread struct{ state, anchor, chain string }
	threadOf := func(gid string) thread {
		t.Helper()
		var th thread
		if err := l.d.Pool.QueryRow(ctx, `SELECT d.thread_state, coalesce(d.thread_anchor_id, ''),
			coalesce(d.thread_chain_last_id, '') FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
			WHERE g.public_id = $1 AND d.destination_id = $2`, gid, dest).Scan(&th.state, &th.anchor,
			&th.chain); err != nil {
			t.Fatal(err)
		}
		return th
	}
	lastReply := func() deliverytest.Call {
		t.Helper()
		var last deliverytest.Call
		for _, c := range l.rec.Calls() {
			if c.Method == deliverytest.MethodReply {
				last = c
			}
		}
		return last
	}

	// The copy before the Publication completes.
	if err := learn(1001, 9001, delivery.LearnedFromAutomaticForward); err != nil {
		t.Fatal(err)
	}
	posted("1001")
	l.fire(t, "tg1", "TgFirst/a")
	g1 := l.group(t, "TgFirst")
	l.round(t, w)
	if th := threadOf(g1); th != (thread{"attached", "9001", ""}) {
		t.Fatalf("copy first: %+v", th)
	}
	l.fire(t, "tg1", "TgFirst/a", "TgFirst/b")
	l.round(t, w)
	if r := lastReply(); r.Root != (delivery.Root{MessageID: "1001", ThreadAnchorID: "9001"}) {
		t.Fatalf("reply %+v", r.Root)
	}

	// The copy after the Publication.
	posted("1002")
	l.fire(t, "tg2", "TgSecond/a")
	g2 := l.group(t, "TgSecond")
	l.round(t, w)
	if th := threadOf(g2); th.state != "waiting_for_copy" {
		t.Fatalf("copy after: %+v", th)
	}
	if err := learn(1002, 9002, delivery.LearnedFromAutomaticForward); err != nil {
		t.Fatal(err)
	}
	if th := threadOf(g2); th != (thread{"attached", "9002", ""}) {
		t.Fatalf("copy after: %+v", th)
	}

	// Both at once: the lock of the post is held while the worker records the Publication and the update path learns
	// the copy; whichever goes second attaches the Thread.
	posted("1003")
	l.fire(t, "tg3", "TgThird/a")
	g3 := l.group(t, "TgThird")
	holder, err := l.d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext(format('%s/%s/%s', $2::bigint,
		$3::bigint, $4::bigint)))`, int32(0x6d75_0003), conn, channel, int64(1003)); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { _, err := w.Round(ctx); errs <- err }()
	go func() { errs <- learn(1003, 9003, delivery.LearnedFromAutomaticForward) }()
	for start := time.Now(); ; {
		if l.count(t, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`) == 2 {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("the worker and the copy did not both wait for the lock of the post")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if th := threadOf(g3); th != (thread{"attached", "9003", ""}) {
		t.Fatalf("at once: %+v", th)
	}

	// Copy wait and the unattached chain (C-14.AC-2): the copy is withheld; the reply waits telegram.copy_wait from
	// the Publication, then starts a chain, which the next reply follows.
	posted("1004")
	l.fire(t, "tg4", "TgFourth/a")
	g4 := l.group(t, "TgFourth")
	l.round(t, w)
	published := l.business.Now()
	l.fire(t, "tg4", "TgFourth/a", "TgFourth/b")
	l.round(t, w)
	if r := lastReply(); r.Root.MessageID == "1004" {
		t.Fatalf("the reply did not wait for the copy: %+v", r.Root)
	}
	var due time.Time
	if err := l.d.Pool.QueryRow(ctx, `SELECT r.next_attempt_at FROM thread_replies r JOIN deliveries d ON
		d.id = r.delivery_id WHERE d.message_id = '1004' AND r.state = 'pending'`).Scan(&due); err != nil ||
		!due.Equal(published.Add(delivery.CopyWait)) {
		t.Fatalf("due %v, published %v: %v", due, published, err)
	}
	l.rec.Script(deliverytest.MethodReply, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "2001"}}, deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK,
		MessageID: "2002"}})
	l.business.Set(due)
	l.round(t, w)
	if r := lastReply(); r.Root != (delivery.Root{MessageID: "1004"}) || threadOf(g4) !=
		(thread{"unattached", "", "2001"}) {
		t.Fatalf("first link %+v, thread %+v", r.Root, threadOf(g4))
	}
	states, err := l.svc.States(ctx, g4)
	if err != nil || len(states) != 1 || !states[0].ThreadNotAttached {
		t.Errorf("states %+v %v", states, err)
	}
	l.business.Advance(2 * time.Minute)
	l.fire(t, "tg4", "TgFourth/a", "TgFourth/b", "TgFourth/c")
	l.round(t, w)
	if r := lastReply(); r.Root != (delivery.Root{MessageID: "1004", ChainLastID: "2001"}) ||
		threadOf(g4).chain != "2002" {
		t.Fatalf("second link %+v", r.Root)
	}
	if err := learn(1004, 9004, delivery.LearnedFromComment); err != nil {
		t.Fatal(err)
	}
	l.business.Advance(2 * time.Minute)
	l.fire(t, "tg4", "TgFourth/a", "TgFourth/b", "TgFourth/c", "TgFourth/d")
	l.round(t, w)
	if r := lastReply(); r.Root != (delivery.Root{MessageID: "1004", ThreadAnchorID: "9004"}) {
		t.Fatalf("after the comment %+v", r.Root)
	}
	if n := l.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'thread_not_attached'
		AND destination_id = $1 AND loudness = 'quiet'`, dest); n != 1 {
		t.Errorf("thread_not_attached events %d", n)
	}

	// The copy only found by the due reply.
	posted("1005")
	l.fire(t, "tg5", "TgFifth/a")
	g5 := l.group(t, "TgFifth")
	l.round(t, w)
	l.exec(t, `INSERT INTO telegram_post_copies (connection_id, channel_chat_id, channel_message_id, org_id,
		discussion_chat_id, copy_message_id, learned_from, received_at) VALUES ($1, $2, 1005, $3, $4, 9005,
		'automatic_forward', $5)`, conn, channel, l.orgID, group, l.business.Now())
	l.fire(t, "tg5", "TgFifth/a", "TgFifth/b")
	l.round(t, w)
	if r := lastReply(); r.Root != (delivery.Root{MessageID: "1005", ThreadAnchorID: "9005"}) ||
		threadOf(g5).state != "attached" {
		t.Fatalf("looked up by the reply %+v", r.Root)
	}

	// The lost Thread (C-14.AC-16): the copy of the first post is deleted.
	l.rec.Script(deliverytest.MethodReply,
		deliverytest.Failure(delivery.OutcomeThreadLost, "Bad Request: message to be replied not found"),
		deliverytest.Answer{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "3001"}})
	l.business.Advance(2 * time.Minute)
	l.fire(t, "tg1", "TgFirst/a", "TgFirst/b", "TgFirst/c")
	l.round(t, w)
	if r := lastReply(); r.Root != (delivery.Root{MessageID: "1001"}) ||
		threadOf(g1) != (thread{"unattached", "9001", "3001"}) {
		t.Fatalf("lost %+v, thread %+v", r.Root, threadOf(g1))
	}
	if health, _ := l.health(t, dest); health != "healthy" || l.count(t, `SELECT count(*) FROM delivery_events
		WHERE kind = 'thread_not_attached' AND error LIKE '%message to be replied not found%'`) != 1 {
		t.Errorf("health %s", health)
	}
	if err := learn(1001, 9001, delivery.LearnedFromComment); err != nil || threadOf(g1).state != "unattached" {
		t.Errorf("the lost copy attached again: %v %+v", err, threadOf(g1))
	}

	// Pruning: a day later the copies are gone; again, nothing more.
	if n, err := l.svc.PrunePostCopies(ctx, l.business.Now().Add(delivery.PostCopyRetention+time.Hour), 2); err != nil ||
		n != 2 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if n, err := l.svc.PrunePostCopies(ctx, l.business.Now().Add(delivery.PostCopyRetention+time.Hour),
		1000); err != nil || n != 3 || l.count(t, `SELECT count(*) FROM telegram_post_copies`) != 0 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if n, err := l.svc.PrunePostCopies(ctx, l.business.Now().Add(48*time.Hour), 1000); err != nil || n != 0 {
		t.Fatalf("pruned again %d %v", n, err)
	}
	t.Log("copy before, after and at once with the Publication, and found by the reply; the chain after " +
		"telegram.copy_wait; the comment's copy; the lost Thread; the copies pruned after a day")
}

// telegramPressLock is C-14.FR-5 and AC-18 on PostgreSQL with D295: while a press holds the update lock of a Telegram
// Connection, the worker does not wait for it. An edit of a Root message of the Connection is rescheduled
// telegram.press_edit_delay later, with no call, no attempt, no error and no limiter token, as long as the press runs,
// and the round goes on: a Publication to the same Destination goes out meanwhile. Once the press released the lock,
// that is once it has been answered, the edit goes out.
func (l *live) telegramPressLock(t *testing.T) {
	l.fresh(t, 1000, 1)
	ctx := t.Context()
	var conn, dest int64
	if err := l.d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name,
		telegram_bot_api_base_url, telegram_update_mode, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at,
		limiter_limit, limiter_per_seconds, created_at, updated_at) VALUES ($1, 'CNAAAAAAAAAAT2', 'telegram', 'tg-press',
		'https://api.telegram.org', 'long_polling', '\x00', 'k1', $2, 1000, 1, $2, $2) RETURNING id`, l.orgID,
		t0).Scan(&conn); err != nil {
		t.Fatal(err)
	}
	if err := l.d.Pool.QueryRow(ctx, `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
		telegram_channel_id, telegram_channel_chat_id, telegram_discussion_chat_id, mentions, limiter_limit,
		limiter_per_seconds, health, created_at, updated_at) VALUES ($1, 'DSAAAAAAAAAAT2', 'telegram', 'press', $2,
		'@muster_press', -1001000000011, -1001000000012, '{}', 1000, 1, 'healthy', $3, $3) RETURNING id`, l.orgID,
		conn, t0).Scan(&dest); err != nil {
		t.Fatal(err)
	}
	l.attach(t, dest)
	w := l.worker("tgp")
	w.Adapters[delivery.TypeTelegram] = l.rec
	l.fire(t, "tp1", "TgPress/a")
	g1 := l.group(t, "TgPress")
	l.round(t, w)
	if l.rec.Count(deliverytest.MethodPublish) != 1 {
		t.Fatalf("calls %+v", l.rec.Calls())
	}
	holder, err := l.d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashint8($2::bigint))`,
		db.TelegramUpdateLockClass, conn); err != nil {
		t.Fatal(err)
	}
	l.ack(t, g1)
	l.fire(t, "tp2", "TgPressSecond/a")
	tokens := func() string {
		t.Helper()
		var s string
		if err := l.d.Pool.QueryRow(ctx, `SELECT string_agg(subject_kind || '=' || tokens::text, ',' ORDER BY
			subject_kind) FROM rate_limit_buckets WHERE (subject_kind = 'destination' AND subject_id = $1) OR
			(subject_kind = 'connection' AND subject_id = $2)`, dest, conn).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	now := l.business.Now()
	done := make(chan error, 1)
	go func() { _, err := w.Round(ctx); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the round waited for the press")
	}
	var due time.Time
	if err := l.d.Pool.QueryRow(ctx, `SELECT d.next_attempt_at FROM deliveries d JOIN alert_groups g ON
		g.id = d.alert_group_id WHERE g.public_id = $1 AND d.destination_id = $2`, g1, dest).Scan(&due); err != nil {
		t.Fatal(err)
	}
	r := l.row(t, g1, dest)
	if l.rec.Count(deliverytest.MethodUpdate) != 0 || l.rec.Count(deliverytest.MethodPublish) != 2 ||
		r.state != "pending" || r.attempts != 0 || r.firstFailed || r.lastErrorClass != "" ||
		!due.Equal(now.Add(delivery.TelegramPressEditDelay)) {
		t.Fatalf("during the press: %+v, due %v (now %v), calls %+v", r, due, now, l.rec.Calls())
	}
	spent := tokens()
	l.business.Advance(delivery.TelegramPressEditDelay)
	l.round(t, w)
	if l.rec.Count(deliverytest.MethodUpdate) != 0 || tokens() != spent || l.row(t, g1, dest).attempts != 0 {
		t.Fatalf("the edit was made, or spent tokens, while the press held the lock: %s → %s", spent, tokens())
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	l.business.Advance(delivery.TelegramPressEditDelay)
	l.round(t, w)
	if r := l.row(t, g1, dest); l.rec.Count(deliverytest.MethodUpdate) != 1 || r.state != "delivered" ||
		r.attempts != 0 {
		t.Fatalf("after the press: %+v", r)
	}
	t.Log("an edit during a press was rescheduled telegram.press_edit_delay later without a token or an attempt, " +
		"a Publication went out meanwhile, and the edit followed the press")
}
