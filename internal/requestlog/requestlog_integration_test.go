// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package requestlog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/requestlog/dbgen"
	"github.com/muster-io/muster/internal/routing"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

// liveEnv is a migrated database with two Organizations: the first has a Mattermost Connection, a messenger
// Destination on it, an outgoing webhook Destination and an Alert Group; the second a Connection of its own.
type liveEnv struct {
	d                                   *db.DB
	org, org2                           int64
	conn, conn2, messenger, webhook     int64
	group                               string
	business                            *clock.Manual
	log                                 *bytes.Buffer
	w, w2                               *Writer
	r                                   *Reader
	connPub, conn2Pub, messPub, hookPub string
}

func newLiveEnv(t *testing.T, s dbtest.Server) *liveEnv {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	log := &bytes.Buffer{}
	logger := logging.New(log, logging.LevelInfo)
	if err := d.Migrate(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
		t.Fatal(err)
	}
	e := &liveEnv{d: d, business: clock.NewManual(t0), log: log, group: "AGAAAAAAAAAAR1", connPub: "CN0000000000M1",
		conn2Pub: "CN0000000000M2", messPub: "DS0000000000M1", hookPub: "DS0000000000W1"}
	e.scan(t, &e.org, `SELECT id FROM organizations`)
	e.exec(t, `INSERT INTO organizations (public_id, name, time_zone, severity_label, severity_mapping,
		severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
		retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
		oidc_token_grace_seconds, created_at, updated_at)
		VALUES ('RG0000000000BB', 'Other', 'UTC', 'severity', '[]', '[]', true, '{}', 14, 90, 730, 365, 'nobody',
		604800, '2026-10-05', '2026-10-05')`)
	e.scan(t, &e.org2, `SELECT id FROM organizations WHERE public_id = 'RG0000000000BB'`)
	addConn := func(org int64, pub string) int64 {
		var id int64
		e.scan(t, &id, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at,
			updated_at)
			VALUES ($1, $2, 'mattermost', $2, 'http://mm', '\x00', 'k1', '2026-10-05', 1, 1, '2026-10-05', '2026-10-05')
			RETURNING id`, org, pub)
		return id
	}
	e.conn, e.conn2 = addConn(e.org, e.connPub), addConn(e.org2, e.conn2Pub)
	e.scan(t, &e.messenger, `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
		mattermost_team_id, mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at,
		updated_at)
		VALUES ($1, $2, 'mattermost', 'alerts', $3, 't1', 'ch1', '{}', 1, 1, 'healthy', '2026-10-05', '2026-10-05')
		RETURNING id`, e.org, e.messPub, e.conn)
	e.scan(t, &e.webhook, `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode,
		webhook_events_config, signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at, mentions,
		limiter_limit, limiter_per_seconds, health, broken_since, broken_cause, broken_reason, next_probe_at, created_at,
		updated_at)
		VALUES ($1, $2, 'webhook', 'hook', 'events', '{}', '\x00', 'k1', '2026-10-05', '{}', 1, 1, 'broken',
		'2026-10-05', 'unavailable', 'unavailable after repeated failures', '2026-10-05', '2026-10-05', '2026-10-05')
		RETURNING id`, e.org, e.hookPub)
	if err := routing.EnsureDefault(ctx, routing.NewStore(d.Pool), e.org, t0); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO alert_groups (org_id, public_id, number, route_id, group_key_labels, group_key_values,
		group_key_sha256, title, common_labels, common_annotations, integration_ids, status, severity_level, urgent,
		created_at, last_changed_at)
		SELECT r.org_id, 'AGAAAAAAAAAAR1', 7, r.id, '{alertname}', '{}', sha256('rl'::bytea), 'Latency', '{}', '{}',
			'{}', 'firing', 'critical', false, '2026-10-05', '2026-10-05'
		FROM routes r WHERE r.is_default`)
	e.w = NewWriter(Config{OrgID: e.org, DB: d.Pool, Business: e.business, Log: logger})
	e.w2 = NewWriter(Config{OrgID: e.org2, DB: d.Pool, Business: e.business, Log: logger})
	e.r = NewReader(e.org, d.Pool, e.business)
	return e
}

func (e *liveEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.d.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *liveEnv) scan(t *testing.T, dst any, sql string, args ...any) {
	t.Helper()
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(dst); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// oldest are the public_ids of the records where says, oldest first.
func (e *liveEnv) oldest(t *testing.T, where string) []string {
	t.Helper()
	rows, err := e.d.Pool.Query(t.Context(), `SELECT public_id FROM request_log WHERE `+where+` ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func (e *liveEnv) count(t *testing.T, where string, args ...any) int {
	t.Helper()
	var n int
	e.scan(t, &n, `SELECT count(*) FROM request_log WHERE `+where, args...)
	return n
}

// record is the i-th outbound record of a test: a Publication answered 201, except every 50th, answered 503, and the
// one after it, which got no answer; every tenth from the fifth is a patchPost.
func record(owner outbound.Owner, links outbound.Links, i int) outbound.Record {
	r := outbound.Record{Owner: owner, Links: links, Operation: "createPost", Method: http.MethodPost,
		URL: "http://mm/api/v4/posts", RequestHeader: http.Header{"User-Agent": {"Muster/1.0.0"}},
		RequestBody: fmt.Appendf(nil, `{"n":%d}`, i), Status: 201, ResponseHeader: http.Header{},
		ResponseBody: []byte(`{"id":"p"}`), Latency: 12 * time.Millisecond, Via: outbound.ViaDirect, Attempt: 1,
		Outcome: outbound.OutcomeOK}
	switch i % 50 {
	case 0:
		r.Status, r.Outcome, r.Error = 503, outbound.OutcomeTransient, "503 Service Unavailable (transient)"
	case 1:
		r.Status, r.ResponseHeader, r.ResponseBody = 0, nil, nil
		r.Outcome, r.Network, r.Error = outbound.OutcomeTransient, outbound.NetworkConnect, "connection refused"
	}
	if i%10 == 5 {
		r.Operation = "patchPost"
	}
	return r
}

// write writes n records through w, the business clock moving by step after each.
func (e *liveEnv) write(t *testing.T, w *Writer, owner outbound.Owner, links func(i int) outbound.Links, n int,
	step time.Duration) {
	t.Helper()
	for i := range n {
		var l outbound.Links
		if links != nil {
			l = links(i)
		}
		w.Record(t.Context(), record(owner, l, i))
		e.business.Advance(step)
	}
	if strings.Contains(e.log.String(), "request_log_write_failed") {
		t.Fatalf("a write failed: %s", e.log)
	}
}

// all lists every page of f, size records at a time, and returns the records in order.
func (e *liveEnv) all(t *testing.T, f Filter, size int) []Summary {
	t.Helper()
	f.Limit = size
	var out []Summary
	for {
		page, err := e.r.List(t.Context(), f)
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		out = append(out, page.Records...)
		if page.Next == nil {
			return out
		}
		f.After = page.Next
	}
}

func newestFirst(t *testing.T, name string, got []Summary) {
	t.Helper()
	seen := map[string]bool{}
	for i, s := range got {
		if seen[s.PublicID] {
			t.Errorf("%s: %s listed twice", name, s.PublicID)
		}
		seen[s.PublicID] = true
		if i > 0 && (s.At.After(got[i-1].At) || s.At.Equal(got[i-1].At) && s.ID > got[i-1].ID) {
			t.Errorf("%s: record %d is newer than the one before it", name, i)
		}
	}
}

// TestIntegrationCountBound covers C-23.AC-4 and C-23.FR-4, FR-5 for the count bound: with 510 records each, a
// Connection, an outgoing webhook Destination and the records without an owner list and keep the newest 500 —
// paged by cursor, ties of time included; a messenger Destination's view lies within its Connection's bound; the
// filters work on the bounded set; the pruning deletes the 30 past the bound, deletes nothing more when run again, and
// two runs at once delete each record once; another Organization's records are neither listed nor pruned.
func TestIntegrationCountBound(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := newLiveEnv(t, s)
		connOwner := outbound.Owner{Connection: e.conn, PublicID: e.connPub}
		hookOwner := outbound.Owner{Destination: e.webhook, PublicID: e.hookPub}
		// The 10 oldest and the 3 newest records of the Connection concern the messenger Destination, the 3 newest
		// also the Alert Group.
		var group int64
		e.scan(t, &group, `SELECT id FROM alert_groups WHERE public_id = $1`, e.group)
		e.write(t, e.w, connOwner, func(i int) outbound.Links {
			switch {
			case i < 10:
				return outbound.Links{Destination: e.messenger}
			case i >= 507:
				return outbound.Links{Destination: e.messenger, AlertGroup: group}
			}
			return outbound.Links{}
		}, 510, time.Second)
		// The webhook's records come two at a time.
		for i := range 510 {
			e.w.Record(ctx, record(hookOwner, outbound.Links{Destination: e.webhook}, i))
			if i%2 == 1 {
				e.business.Advance(time.Second)
			}
		}
		e.write(t, e.w, outbound.Owner{}, nil, 510, time.Millisecond)
		e.write(t, e.w2, outbound.Owner{Connection: e.conn2, PublicID: e.conn2Pub}, nil, 510, time.Second)

		conn := e.all(t, Filter{Connection: e.connPub}, 200)
		newestFirst(t, "the Connection", conn)
		if len(conn) != MaxRecords || conn[0].Operation != "createPost" || conn[499].Status == nil {
			t.Fatalf("the Connection lists %d records", len(conn))
		}
		oldest := e.oldest(t, fmt.Sprintf("connection_id = %d", e.conn))
		first, past := oldest[10], oldest[9]
		if conn[499].PublicID != first {
			t.Errorf("the oldest listed is %s, want the 11th oldest %s", conn[499].PublicID, first)
		}
		if _, err := e.r.Get(ctx, first); err != nil {
			t.Errorf("Get the oldest within the bound: %v", err)
		}
		if _, err := e.r.Get(ctx, past); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get the newest past the bound: %v", err)
		}
		hook := e.all(t, Filter{Destination: e.hookPub}, 7)
		newestFirst(t, "the outgoing webhook", hook)
		if len(hook) != MaxRecords || hook[0].Destination == nil || hook[0].Destination.Health != "broken" ||
			hook[0].Destination.BrokenSince == nil || *hook[0].Destination.BrokenReason == "" {
			t.Errorf("the outgoing webhook lists %d records: %+v", len(hook), hook[0].Destination)
		}
		hookPast := e.oldest(t, fmt.Sprintf("destination_id = %d", e.webhook))[9]
		unowned := e.oldest(t, fmt.Sprintf("org_id = %d AND connection_id IS NULL AND destination_id IS NULL", e.org))
		nonePast, noneIn := unowned[9], unowned[10]
		if rec, err := e.r.Get(ctx, hookPast); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get an outgoing webhook's record past the bound: %+v, %v", rec, err)
		}
		if rec, err := e.r.Get(ctx, nonePast); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get a record of no owner past the bound: %+v, %v", rec, err)
		}
		if rec, err := e.r.Get(ctx, noneIn); err != nil || rec.Owner != OwnerNone {
			t.Errorf("Get a record of no owner within the bound: %+v, %v", rec, err)
		}

		mess := e.all(t, Filter{Destination: e.messPub}, 50)
		if len(mess) != 3 || mess[0].AlertGroup == nil || mess[0].AlertGroup.Number != 7 ||
			mess[0].Destination.PublicID != e.messPub || *mess[0].Connection != e.connPub {
			t.Errorf("the messenger Destination lists %d records: %+v", len(mess), mess)
		}
		for _, tt := range []struct {
			f    Filter
			want int
		}{
			{Filter{Connection: e.connPub, AlertGroup: e.group}, 3},
			{Filter{Destination: e.messPub, AlertGroup: strings.ToLower(e.group)}, 3},
			{Filter{Connection: e.connPub, AlertGroup: "AGAAAAAAAAAAR2"}, 0},
			{Filter{Connection: e.connPub, Direction: DirectionOutbound}, 500},
			{Filter{Connection: e.connPub, Direction: DirectionInbound}, 0},
			{Filter{Connection: e.connPub, StatusClass: Status2xx}, 480},
			{Filter{Connection: e.connPub, StatusClass: Status3xx}, 0},
			{Filter{Connection: e.connPub, StatusClass: Status5xx}, 10},
			{Filter{Connection: e.connPub, StatusClass: StatusNoAnswer}, 10},
			{Filter{Connection: e.connPub, Operation: "patchPost"}, 50},
			{Filter{Destination: e.hookPub, Operation: "patchPost", StatusClass: Status2xx}, 50},
		} {
			if got := e.all(t, tt.f, 100); len(got) != tt.want {
				t.Errorf("%+v: %d records, want %d", tt.f, len(got), tt.want)
			}
		}
		none := e.all(t, Filter{Connection: e.connPub, StatusClass: StatusNoAnswer}, 100)
		rec, err := e.r.Get(ctx, none[0].PublicID)
		if err != nil || rec.Status != nil || rec.ResponseHeaders != nil || rec.ResponseBody != nil ||
			*rec.ErrorCode != "connect_failed" || *rec.ErrorClass != "transient" || *rec.Error != "connection refused" ||
			rec.Owner != OwnerConnection {
			t.Errorf("a record without an answer: %+v, %v", rec, err)
		}
		for _, f := range []Filter{{Connection: e.conn2Pub}, {Connection: "CN0000000000ZZ"},
			{Destination: "DS0000000000ZZ"}} {
			if _, err := e.r.List(ctx, f); !errors.Is(err, ErrNotFound) {
				t.Errorf("%+v from the first Organization: %v", f, err)
			}
		}
		var other string
		e.scan(t, &other, `SELECT public_id FROM request_log WHERE org_id = $1 ORDER BY at DESC LIMIT 1`, e.org2)
		if _, err := e.r.Get(ctx, other); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get a record of another Organization: %v", err)
		}

		before := metrics.RequestLogPruned.With().Get()
		newest := oldest[10:]
		// Two runs at once delete each record past the bounds once between them.
		var wg sync.WaitGroup
		results := make([]int64, 2)
		errs := make([]error, 2)
		for i := range 2 {
			wg.Go(func() { results[i], errs[i] = Prune(ctx, e.d.Pool, e.org, e.business.Now()) })
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil || results[0]+results[1] != 30 {
			t.Errorf("two runs at once: %v, %v", results, err)
		}
		if n, err := Prune(ctx, e.d.Pool, e.org, e.business.Now()); err != nil || n != 0 {
			t.Errorf("a third run: %d, %v", n, err)
		}
		if metrics.RequestLogPruned.With().Get() != before {
			t.Error("Prune counts muster_request_log_pruned_total, which is the Leader task's")
		}
		for name, where := range map[string]string{
			"the Connection":       fmt.Sprintf("connection_id = %d", e.conn),
			"the outgoing webhook": fmt.Sprintf("destination_id = %d AND connection_id IS NULL", e.webhook),
			"the records of nobody": fmt.Sprintf("org_id = %d AND connection_id IS NULL AND destination_id IS NULL",
				e.org),
			"the other Organization": fmt.Sprintf("org_id = %d", e.org2),
		} {
			want := MaxRecords
			if name == "the other Organization" {
				want = 510
			}
			if got := e.count(t, where); got != want {
				t.Errorf("%s keeps %d records, want %d", name, got, want)
			}
		}
		for _, pub := range newest {
			if e.count(t, "public_id = $1", pub) != 1 {
				t.Errorf("the pruning deleted %s, one of the newest 500", pub)
			}
		}
		if got := e.all(t, Filter{Connection: e.connPub}, 500); len(got) != MaxRecords {
			t.Errorf("after the pruning the Connection lists %d", len(got))
		}
		if n, err := Prune(ctx, e.d.Pool, e.org2, e.business.Now()); err != nil || n != 10 {
			t.Errorf("the other Organization's run: %d, %v", n, err)
		}
	})
}

// TestIntegrationAgeBound covers the age bound of C-23.AC-4: once the business clock moves past
// request_log.max_age a record is neither listed nor read, and the pruning deletes it, in statements of at most the
// batch, each choosing its batch once; a record exactly request_log.max_age old stays.
func TestIntegrationAgeBound(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := newLiveEnv(t, s)
		connOwner := outbound.Owner{Connection: e.conn, PublicID: e.connPub}
		e.write(t, e.w, connOwner, nil, 6, 0)
		e.write(t, e.w, outbound.Owner{Destination: e.webhook, PublicID: e.hookPub}, nil, 4, time.Hour)
		var old string
		e.scan(t, &old, `SELECT public_id FROM request_log WHERE connection_id = $1 LIMIT 1`, e.conn)
		// A record a day later is exactly request_log.max_age old when the clock has moved 8 days.
		e.business.Set(t0.Add(24 * time.Hour))
		e.write(t, e.w, connOwner, nil, 1, 0)
		e.business.Set(t0.Add(8 * 24 * time.Hour))
		if got := e.all(t, Filter{Connection: e.connPub}, 50); len(got) != 1 {
			t.Errorf("the Connection lists %d records after 8 days, want the one a day later", len(got))
		}
		if got := e.all(t, Filter{Destination: e.hookPub}, 50); len(got) != 0 {
			t.Errorf("the outgoing webhook lists %d records after 8 days", len(got))
		}
		if _, err := e.r.Get(ctx, old); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get a record past the age bound: %v", err)
		}

		// Statements of a batch of 3, each choosing its batch once.
		var n int64
		var err error
		loops := dbtest.LockRowsLoops(t, e.d.Pool.Config().ConnString(), func(ctx context.Context, c *pgx.Conn) {
			n, err = dbgen.New(c).PruneOlder(ctx, dbgen.PruneOlderParams{OrgID: e.org,
				Before: e.business.Now().Add(-MaxAge), BatchSize: 3})
		})
		if err != nil || n != 3 || loops != 1 {
			t.Errorf("one statement of a batch of 3: %d, %v, chosen %d times", n, err, loops)
		}
		if n, err := prune(ctx, dbgen.New(e.d.Pool), e.org, e.business.Now(), 3); err != nil || n != 7 {
			t.Errorf("prune in batches of 3: %d, %v", n, err)
		}
		if got := e.count(t, "org_id = $1", e.org); got != 1 {
			t.Errorf("%d records left, want the one at the edge of the age bound", got)
		}
		e.business.Advance(time.Microsecond)
		if got := e.all(t, Filter{Connection: e.connPub}, 50); len(got) != 0 {
			t.Errorf("past the edge the Connection lists %d", len(got))
		}
	})
}

// TestIntegrationExcessBatches: a statement of the count bound deletes at most its batch, choosing it once, and only
// records older than the newest request_log.max_records.
func TestIntegrationExcessBatches(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newLiveEnv(t, s)
		e.write(t, e.w, outbound.Owner{Connection: e.conn, PublicID: e.connPub}, nil, MaxRecords+5, time.Second)
		var n int64
		var err error
		loops := dbtest.LockRowsLoops(t, e.d.Pool.Config().ConnString(), func(ctx context.Context, c *pgx.Conn) {
			n, err = dbgen.New(c).PruneConnectionExcess(ctx, dbgen.PruneConnectionExcessParams{OrgID: e.org,
				ConnectionID: e.conn, MaxRecords: MaxRecords, BatchSize: 3})
		})
		if err != nil || n != 3 || loops != 1 {
			t.Errorf("one statement of a batch of 3: %d, %v, chosen %d times", n, err, loops)
		}
		if n, err := prune(t.Context(), dbgen.New(e.d.Pool), e.org, e.business.Now(), 3); err != nil || n != 2 {
			t.Errorf("the rest: %d, %v", n, err)
		}
		if got := e.count(t, "connection_id = $1", e.conn); got != MaxRecords {
			t.Errorf("%d records left", got)
		}
	})
}

// TestIntegrationBodies covers the stored text of C-23.FR-2 and C-23.AC-4 on the database: a 40 KB body is kept as its
// first 16 KB, cut at a character boundary, marked cut with its full size; bytes that are not UTF-8 and NUL, in
// bodies and headers, are stored replaced; a record the database refuses is logged and counted, and nothing else
// happens.
func TestIntegrationBodies(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := newLiveEnv(t, s)
		owner := outbound.Owner{Connection: e.conn, PublicID: e.connPub}
		r := record(owner, outbound.Links{}, 2)
		r.RequestBody = []byte(strings.Repeat("é", 20480))
		r.ResponseBody = []byte("bad \xff\xfe bytes and a \x00")
		r.RequestHeader = http.Header{"User-Agent": {"Muster/1.0.0"}, "X-Odd": {"a\x00\xffb"}}
		e.w.Record(ctx, r)
		page, err := e.r.List(ctx, Filter{Connection: e.connPub, Limit: 1})
		if err != nil || len(page.Records) != 1 || !page.Records[0].RequestBodyTruncated {
			t.Fatalf("list: %+v, %v (log %s)", page, err, e.log)
		}
		rec, err := e.r.Get(ctx, page.Records[0].PublicID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rec.RequestBody) > BodyMax || len(rec.RequestBody) < BodyMax-1 || !utf8.ValidString(rec.RequestBody) ||
			rec.RequestBodyBytes != 40960 || !rec.RequestBodyTruncated || rec.ResponseBodyTruncated ||
			*rec.ResponseBody != "bad � bytes and a �" ||
			!slices.Equal(rec.RequestHeaders["X-Odd"], []string{"a��b"}) ||
			rec.RequestHeaders["User-Agent"][0] != "Muster/1.0.0" || rec.URL != "http://mm/api/v4/posts" {
			t.Errorf("stored: %d bytes of %d, cut %v, answer %q, headers %v", len(rec.RequestBody),
				rec.RequestBodyBytes, rec.RequestBodyTruncated, *rec.ResponseBody, rec.RequestHeaders)
		}

		failures := metrics.RequestLogWriteFailures.With("outbound").Get()
		bad := record(owner, outbound.Links{}, 2)
		bad.Operation = strings.Repeat("o", 65)
		e.w.Record(ctx, bad)
		if metrics.RequestLogWriteFailures.With("outbound").Get() != failures+1 ||
			!strings.Contains(e.log.String(), `"event":"request_log_write_failed","direction":"outbound","operation":"`+
				bad.Operation+`","owner":"CN0000000000M1","error":"ERROR: new row for relation \"request_log\" `+
				`violates check constraint`) {
			t.Errorf("a refused record: logged %s", e.log)
		}
		if got := e.count(t, "org_id = $1", e.org); got != 1 {
			t.Errorf("%d records, want the one written", got)
		}
	})
}

// TestIntegrationRunList covers the read of a check run alone (C-23.FR-4, FR-5) on the database: it lists the run's
// records of a Connection and of nobody within their owner's newest request_log.max_records, newest first and paged,
// not those of an outgoing webhook, which its Destination lists, nor those of a deleted Connection; beside an owner the
// run narrows its list; past request_log.max_age a run lists nothing. The kinds of checks and tests and the inbound
// records are stored as the table's checks allow.
func TestIntegrationRunList(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := newLiveEnv(t, s)
		const r1, r2, r3, r4, r5, r6, r7 = "CR0000000000R1", "CR0000000000R2", "CR0000000000R3", "CR0000000000R4",
			"CR0000000000R5", "CR0000000000R6", "CR0000000000R7"
		var deleted int64
		e.scan(t, &deleted, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at,
			updated_at, deleted_at)
			VALUES ($1, 'CN0000000000M3', 'mattermost', 'gone', 'http://mm', '\x00', 'k1', '2026-10-05', 1, 1,
			'2026-10-05', '2026-10-05', '2026-10-06')
			RETURNING id`, e.org)
		run := func(oldest, newest, n int, older, newer string, kind string) func(i int) outbound.Links {
			return func(i int) outbound.Links {
				switch {
				case i < oldest:
					return outbound.Links{Check: kind, Run: older}
				case i >= n-newest:
					return outbound.Links{Check: kind, Run: newer}
				}
				return outbound.Links{}
			}
		}
		e.write(t, e.w, outbound.Owner{Connection: e.conn, PublicID: e.connPub},
			run(20, 3, 510, r1, r2, outbound.CheckConnection), 510, time.Second)
		e.write(t, e.w, outbound.Owner{Draft: true}, run(10, 2, 505, r3, r4, outbound.CheckDraft), 505, time.Second)
		e.write(t, e.w, outbound.Owner{Destination: e.webhook, PublicID: e.hookPub}, func(int) outbound.Links {
			return outbound.Links{Destination: e.webhook, Check: outbound.CheckTest, Run: r5}
		}, 3, time.Second)
		e.write(t, e.w, outbound.Owner{Connection: deleted, PublicID: "CN0000000000M3"}, func(int) outbound.Links {
			return outbound.Links{Check: outbound.CheckConnection, Run: r6}
		}, 2, time.Second)
		e.write(t, e.w2, outbound.Owner{Connection: e.conn2, PublicID: e.conn2Pub}, func(int) outbound.Links {
			return outbound.Links{Check: outbound.CheckConnection, Run: r2}
		}, 2, time.Second)

		for _, tt := range []struct {
			f    Filter
			want int
		}{
			{Filter{Run: r1}, 10},
			{Filter{Run: r2}, 3},
			{Filter{Run: r3}, 5},
			{Filter{Run: r4}, 2},
			{Filter{Run: r5}, 0},
			{Filter{Destination: e.hookPub, Run: r5}, 3},
			{Filter{Run: r6}, 0},
			{Filter{Connection: e.connPub, Run: r2}, 3},
			{Filter{Connection: e.connPub, Run: r3}, 0},
			{Filter{Run: r1, Operation: "patchPost"}, 1},
		} {
			got := e.all(t, tt.f, 3)
			newestFirst(t, fmt.Sprintf("%+v", tt.f), got)
			if len(got) != tt.want {
				t.Errorf("%+v: %d records, want %d", tt.f, len(got), tt.want)
			}
			for _, s := range got {
				if s.CheckRun == nil || *s.CheckRun != tt.f.Run || s.CheckKind == nil {
					t.Errorf("%+v: a record of run %v, kind %v", tt.f, s.CheckRun, s.CheckKind)
				}
			}
		}
		within := e.oldest(t, fmt.Sprintf("connection_id = %d", e.conn))[10:20]
		got := e.all(t, Filter{Run: r1}, 4)
		for i, s := range got {
			if s.PublicID != within[len(within)-1-i] || *s.CheckKind != "connection_check" || *s.Connection != e.connPub {
				t.Errorf("record %d of run R1: %+v, want %s", i, s, within[len(within)-1-i])
			}
		}
		if draft := e.all(t, Filter{Run: r4}, 10); len(draft) == 2 &&
			(draft[0].Connection != nil || draft[0].Destination != nil || *draft[0].CheckKind != "draft_check") {
			t.Errorf("a check before saving: %+v", draft[0])
		}

		var group int64
		e.scan(t, &group, `SELECT id FROM alert_groups WHERE public_id = $1`, e.group)
		for _, kind := range []string{outbound.CheckConnection, outbound.CheckDraft, outbound.CheckDestination,
			outbound.CheckTest, outbound.CheckProbe} {
			e.write(t, e.w, outbound.Owner{Draft: true}, func(int) outbound.Links {
				return outbound.Links{Check: kind, Run: r7}
			}, 1, time.Second)
		}
		e.write(t, e.w, outbound.Owner{Destination: e.webhook, PublicID: e.hookPub}, func(int) outbound.Links {
			return outbound.Links{Destination: e.webhook, AlertGroup: group, Check: outbound.CheckProbe, Run: r7}
		}, 1, time.Second)
		if got := e.all(t, Filter{Run: r7}, 10); len(got) != 5 {
			t.Errorf("the five kinds: %d records", len(got))
		}
		probe := e.all(t, Filter{Destination: e.hookPub, Run: r7}, 10)
		if len(probe) != 1 || *probe[0].CheckKind != "probe" || probe[0].AlertGroup == nil ||
			probe[0].AlertGroup.Number != 7 {
			t.Errorf("a probe's delivery attempt: %+v", probe)
		}
		e.w.RecordInbound(ctx, InboundRecord{Links: Links{Connection: e.conn, ConnectionID: e.connPub},
			Operation: OperationPush, Method: http.MethodPost, URL: "/api/v1/callbacks/bridge/" + e.connPub,
			Request: Body{Size: 10}, Status: http.StatusUnauthorized, Response: Body{Size: captureMax + 1}})
		in := e.all(t, Filter{Connection: e.connPub, Direction: DirectionInbound}, 10)
		if len(in) != 1 {
			t.Fatalf("inbound records: %+v (log %s)", in, e.log)
		}
		rec, err := e.r.Get(ctx, in[0].PublicID)
		if err != nil || rec.Operation != "push" || *rec.Status != 401 || rec.Via != "direct" || rec.Attempt != 1 ||
			rec.RequestBody != "" || !rec.RequestBodyTruncated || rec.RequestBodyBytes != 10 ||
			*rec.ResponseBody != "" || !rec.ResponseBodyTruncated || rec.ResponseBodyBytes != captureMax+1 ||
			rec.CheckKind != nil || rec.ErrorClass != nil || len(rec.ResponseHeaders) != 0 {
			t.Errorf("an inbound record whose bodies were not kept: %+v, %v", rec, err)
		}

		e.business.Set(t0.Add(MaxAge + 2*time.Hour))
		for _, f := range []Filter{{Run: r2}, {Run: r4}, {Connection: e.connPub, Run: r2}} {
			if got := e.all(t, f, 10); len(got) != 0 {
				t.Errorf("%+v past request_log.max_age: %d records", f, len(got))
			}
		}
	})
}
