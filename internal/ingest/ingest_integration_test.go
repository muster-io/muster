// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package ingest_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// env is a migrated database with the Organization, the partitions of the Audit log for October 2026 and of the
// Stored Snapshots from 2026-09-20 to 2026-10-07, and the services on one manual business clock.
type env struct {
	d         *db.DB
	clock     *clock.Manual
	orgID     int64
	ints      *integrations.Service
	snapshots *ingest.Service
	handler   http.Handler
	log       *bytes.Buffer
}

var by = integrations.Requester{Actor: audit.System, Transport: audit.TransportSystem}

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
	stmts := []string{`CREATE TABLE audit_log_p202610 PARTITION OF audit_log
		FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`}
	for day := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC); !day.After(time.Date(2026, 10, 7, 0, 0, 0, 0,
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
	c := clock.NewManual(t0)
	u, _ := url.Parse("http://localhost:8081")
	e := &env{d: d, clock: c, orgID: org.ID, log: &log,
		ints: integrations.New(integrations.Config{OrgID: org.ID, Store: integrations.NewStore(d.Pool),
			Audit: audit.NewWriter(logger, c), Business: c, IngestURL: u}),
		snapshots: ingest.New(org.ID, ingest.NewStore(d.Pool), c)}
	e.handler = ingest.NewHandler(ingest.HandlerConfig{Auth: e.ints, Snapshots: e.snapshots, Log: logger,
		Real: clock.Real{}})
	return e
}

func (e *env) post(t *testing.T, path, token, body string) int {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w.Code
}

func (e *env) count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func input(name string) integrations.Input {
	return integrations.Input{Name: name, ConnectionMode: integrations.ConnectionWebhookOnly,
		StaticLabels: map[string]string{"cluster": name}, DuplicateWindowSeconds: 45}
}

// TestIntegrationIntegrations is S-018 steps 1 and 2 against PostgreSQL: the partial unique index of names, the
// If-Match version, the soft deletion that frees the name, the tokens stored as hashes, and the demo Integration.
func TestIntegrationIntegrations(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("prod-eu"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.ints.Create(ctx, by, input("prod-eu")); !errors.Is(err, integrations.ErrNameTaken) {
			t.Errorf("same name = %v", err)
		}
		other, _ := e.ints.Create(ctx, by, input("other"))
		if _, err := e.ints.Update(ctx, by, other.PublicID, nil, input("prod-eu")); !errors.Is(err,
			integrations.ErrNameTaken) {
			t.Errorf("rename onto a taken name = %v", err)
		}
		changed := input("prod-eu")
		changed.Description, changed.DuplicateWindowSeconds = new("described"), 60
		updated, err := e.ints.Update(ctx, by, in.PublicID, new(int64(1)), changed)
		if err != nil || updated.Version != 2 || updated.Description != "described" || updated.DuplicateWindowSeconds != 60 {
			t.Errorf("update = %+v, %v", updated, err)
		}
		if _, err := e.ints.Update(ctx, by, in.PublicID, new(int64(1)), changed); !errors.Is(err,
			integrations.ErrVersionMismatch) {
			t.Errorf("stale update = %v", err)
		}
		tok, err := e.ints.CreateToken(ctx, by, in.PublicID, "rotation-1")
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(tok.Value))
		if n := e.count(t, `SELECT count(*) FROM integration_tokens WHERE token_hash = $1 AND name = 'rotation-1'
			AND octet_length(token_hash) = 32`, sum[:]); n != 1 {
			t.Errorf("tokens stored by hash: %d", n)
		}
		if caller, err := e.ints.Authenticate(ctx, tok.Value); err != nil || caller.Integration != in.PublicID {
			t.Errorf("authenticate = %+v, %v", caller, err)
		}
		if err := e.ints.Delete(ctx, by, in.PublicID, new(int64(2))); err != nil {
			t.Fatal(err)
		}
		if caller, err := e.ints.Authenticate(ctx, tok.Value); !errors.Is(err, integrations.ErrInvalidToken) ||
			caller.Integration != integrations.Unknown {
			t.Errorf("token of a deleted integration = %+v, %v", caller, err)
		}
		page, err := e.ints.List(ctx, integrations.ListFilter{Limit: 10})
		if err != nil || len(page.Integrations) != 1 || page.Integrations[0].PublicID != other.PublicID {
			t.Errorf("list = %+v, %v", page, err)
		}
		if _, err := e.ints.Create(ctx, by, input("prod-eu")); err != nil {
			t.Errorf("the name of a deleted integration: %v", err)
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action LIKE 'integration%' AND org_id = $1`,
			e.orgID); n != 6 {
			t.Errorf("audit entries = %d, want 6", n)
		}

		demo := integrations.Demo{Name: "dev-alertmanager", StaticLabels: map[string]string{"cluster": "dev"},
			Token: "mstr_int_" + strings.Repeat("dev", 17) + "d", TokenName: "dev"}
		for range 2 {
			if err := e.ints.EnsureDemo(ctx, demo); err != nil {
				t.Fatal(err)
			}
		}
		caller, err := e.ints.Authenticate(ctx, demo.Token)
		if err != nil {
			t.Fatalf("demo token: %v", err)
		}
		// Deleted, the demo comes back at the next start with its token.
		if err := e.ints.Delete(ctx, by, caller.Integration, nil); err != nil {
			t.Fatal(err)
		}
		if err := e.ints.EnsureDemo(ctx, demo); err != nil {
			t.Fatal(err)
		}
		again, err := e.ints.Authenticate(ctx, demo.Token)
		if err != nil || again.Integration == caller.Integration {
			t.Errorf("demo after deletion = %+v, %v", again, err)
		}
		if n := e.count(t, `SELECT count(*) FROM integration_tokens WHERE name = 'dev'`); n != 1 {
			t.Errorf("demo tokens = %d", n)
		}
	})
}

// TestIntegrationIngestion is S-018 steps 3 and 4 against PostgreSQL (C-05.AC-1, AC-3, AC-8): accepted webhooks are
// pending Stored Snapshots in their day's partition, identical bodies are stored once a day, the list pages newest
// first with its filters and hides what is past retention, and the read returns the bytes as received.
func TestIntegrationIngestion(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, _ := e.ints.Create(ctx, by, input("prod-eu"))
		tok, _ := e.ints.CreateToken(ctx, by, in.PublicID, "")
		for i, body := range []string{"{}", "{}", "not json"} {
			e.clock.Set(t0.Add(time.Duration(i) * time.Second))
			path := "/api/v1/ingest"
			token := tok.Value
			if i == 1 {
				path, token = path+"/"+tok.Value, ""
			}
			if code := e.post(t, path, token, body); code != http.StatusAccepted {
				t.Fatalf("webhook %d = %d", i, code)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM snapshot_bodies_p20261006`); n != 2 {
			t.Errorf("bodies %d, want 2: identical bodies are stored once a day", n)
		}
		if n := e.count(t, `SELECT count(*) FROM stored_snapshots_p20261006 WHERE state = 'pending'
			AND source = 'webhook'`); n != 3 {
			t.Errorf("pending snapshots %d, want 3", n)
		}
		// An old Snapshot past retention.stored_snapshots, 14 days.
		e.clock.Set(t0.Add(-15 * 24 * time.Hour))
		old, err := e.snapshots.Store(ctx, ingest.Received{IntegrationID: in.ID, Body: []byte("old")})
		if err != nil {
			t.Fatal(err)
		}
		e.clock.Set(t0.Add(time.Minute))
		got, err := e.ints.Get(ctx, in.PublicID)
		if err != nil || got.LastSnapshotAt == nil || !got.LastSnapshotAt.Equal(t0.Add(2*time.Second)) ||
			got.SnapshotCount != 0 {
			t.Errorf("integration %+v, %v", got, err)
		}
		page, err := e.snapshots.List(ctx, ingest.ListFilter{Integration: in.PublicID, Limit: 2})
		if err != nil || len(page.Snapshots) != 2 || page.Next == nil ||
			!page.Snapshots[0].ReceivedAt.Equal(t0.Add(2*time.Second)) || page.Snapshots[0].SizeBytes != 8 {
			t.Fatalf("page 1 = %+v, %v", page, err)
		}
		page, err = e.snapshots.List(ctx, ingest.ListFilter{Integration: in.PublicID, After: page.Next, Limit: 2})
		if err != nil || len(page.Snapshots) != 1 || page.Next != nil || !page.Snapshots[0].ReceivedAt.Equal(t0) {
			t.Errorf("page 2 = %+v, %v", page, err)
		}
		from, to := t0.Add(time.Second), t0.Add(2*time.Second)
		page, _ = e.snapshots.List(ctx, ingest.ListFilter{Integration: in.PublicID, From: &from, To: &to, Limit: 10})
		if len(page.Snapshots) != 1 || !page.Snapshots[0].ReceivedAt.Equal(from) {
			t.Errorf("time range = %+v", page)
		}
		for states, want := range map[string]int{"pending": 3, "failed": 0} {
			page, _ = e.snapshots.List(ctx, ingest.ListFilter{Integration: in.PublicID, States: []string{states},
				Limit: 10})
			if len(page.Snapshots) != want {
				t.Errorf("state %s: %d, want %d", states, len(page.Snapshots), want)
			}
		}
		if _, err := e.snapshots.Get(ctx, old.PublicID); !errors.Is(err, ingest.ErrNotFound) {
			t.Errorf("past retention = %v", err)
		}
		first := page.Snapshots
		page, _ = e.snapshots.List(ctx, ingest.ListFilter{Integration: in.PublicID, Limit: 1})
		sn, err := e.snapshots.Get(ctx, page.Snapshots[0].PublicID)
		if err != nil || string(sn.Body) != "not json" || sn.ContentType != nil || sn.State != "pending" ||
			sn.Integration.Name != "prod-eu" {
			t.Errorf("snapshot = %+v, %v (%d)", sn, err, len(first))
		}
		// After deletion the Integration's Stored Snapshots are still listed by its id, and its token gets 401.
		if err := e.ints.Delete(ctx, by, in.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if code := e.post(t, "/api/v1/ingest", tok.Value, "{}"); code != http.StatusUnauthorized {
			t.Errorf("token of a deleted integration = %d", code)
		}
		page, _ = e.snapshots.List(ctx, ingest.ListFilter{Integration: in.PublicID, Limit: 10})
		if len(page.Snapshots) != 3 {
			t.Errorf("snapshots after deletion = %d, want 3", len(page.Snapshots))
		}
		if strings.Contains(e.log.String(), tok.Value) {
			t.Error("the token reached the log")
		}
	})
}

// TestIntegrationNotify: storing a Stored Snapshot notifies the processing workers at the commit.
func TestIntegrationNotify(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		conn, err := e.d.ConnectSession(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(context.WithoutCancel(ctx))
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{ingest.SnapshotChannel}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		in, _ := e.ints.Create(ctx, by, input("prod-eu"))
		if _, err := e.snapshots.Store(ctx, ingest.Received{IntegrationID: in.ID, Body: []byte("{}")}); err != nil {
			t.Fatal(err)
		}
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		n, err := conn.WaitForNotification(wctx)
		if err != nil || n.Channel != ingest.SnapshotChannel || !strings.Contains(n.Payload, `"integration_id":`) {
			t.Errorf("notification %+v, %v", n, err)
		}
	})
}
