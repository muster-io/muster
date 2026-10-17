// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/golang-migrate/migrate/v4"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

func openFresh(t *testing.T, s dbtest.Server) *DB {
	t.Helper()
	u := s.NewDatabase(t)
	d, err := Open(t.Context(), config.Database{URL: logging.Secret(u), SSLMode: "disable"},
		config.Database{URL: logging.Secret(u), SSLMode: "disable"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

func TestIntegrationCheck(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := openFresh(t, s)
		var out bytes.Buffer
		if err := d.Check(t.Context(), logging.New(&out, logging.LevelInfo)); err != nil {
			t.Fatalf("Check: %v", err)
		}
		for _, want := range []string{
			`"level":"WARN","event":"database_connection_security","connection":"main","sslmode":"disable","encrypted":false`,
			`"level":"WARN","event":"database_connection_security","connection":"session","sslmode":"disable","encrypted":false`,
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("the log lacks %s:\n%s", want, out.String())
			}
		}
		if err := d.Ping(t.Context()); err != nil {
			t.Errorf("Ping: %v", err)
		}
	})
}

// TestIntegrationUpDownUp runs up, down and up again over every migration, one step at a time.
func TestIntegrationUpDownUp(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := openFresh(t, s)
		known, err := KnownVersion()
		if err != nil {
			t.Fatal(err)
		}
		c, err := d.ConnectSession(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer closeQuietly(t.Context(), c)
		m, err := newMigrate(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		mm := m.(*migrate.Migrate)
		defer mm.Close()
		for step := uint(1); step <= known; step++ {
			for _, n := range []int{1, -1, 1} {
				if err := mm.Steps(n); err != nil {
					t.Fatalf("step %d, migrate %+d: %v", step, n, err)
				}
			}
			if v, dirty, err := mm.Version(); err != nil || dirty || v != step {
				t.Fatalf("after step %d: version %d, dirty %v, %v", step, v, dirty, err)
			}
		}
		if err := mm.Down(); err != nil {
			t.Fatalf("down to nothing: %v", err)
		}
		if err := mm.Up(); err != nil {
			t.Fatalf("up again: %v", err)
		}
		if err := d.CheckSchema(t.Context(), logging.New(io.Discard, logging.LevelInfo)); err != nil {
			t.Errorf("CheckSchema after up → down → up: %v", err)
		}
	})
}

// TestIntegrationDeliveryEventsKindCheck runs 0010 over a database whose partitioned delivery_events has a partition
// with a row: up adds the new kind CHECK to the parent and the partition and validates it on both, dropping the old
// one; down restores the old CHECK, validated, and deletes the events of the new kind; up again works.
func TestIntegrationDeliveryEventsKindCheck(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := openFresh(t, s)
		c, err := d.ConnectSession(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer closeQuietly(t.Context(), c)
		m, err := newMigrate(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		mm := m.(*migrate.Migrate)
		defer mm.Close()
		if err := mm.Migrate(9); err != nil {
			t.Fatal(err)
		}
		exec := func(t *testing.T, sql string) {
			t.Helper()
			if _, err := d.Pool.Exec(t.Context(), sql); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		exec(t, `INSERT INTO organizations (public_id, name, time_zone, severity_label, severity_mapping,
			severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
			oidc_token_grace_seconds, created_at, updated_at)
			VALUES ('RG0000000000AA', 'Muster', 'UTC', 'severity', '[]', '[]', true, '{}', 14, 90, 730, 365, 'nobody',
			604800, '2026-10-05', '2026-10-05')`)
		exec(t, `CREATE TABLE delivery_events_p202610 PARTITION OF delivery_events
			FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`)
		insert := func(t *testing.T, publicID, kind string) error {
			t.Helper()
			_, err := d.Pool.Exec(t.Context(), `INSERT INTO delivery_events (org_id, public_id, occurred_at,
				destination_id, kind, loudness) SELECT id, $1, '2026-10-05 12:00:00+00', 1, $2, 'quiet'
				FROM organizations`, publicID, kind)
			return err
		}
		if err := insert(t, "DE0000000000A1", "final_edit"); err != nil {
			t.Fatal(err)
		}
		checks := func(t *testing.T) string {
			t.Helper()
			rows, err := d.Pool.Query(t.Context(), `SELECT c.relname, k.conname, k.convalidated
				FROM pg_constraint k JOIN pg_class c ON c.oid = k.conrelid
				WHERE c.relname IN ('delivery_events', 'delivery_events_p202610')
				  AND k.conname LIKE 'delivery_events_kind%' ORDER BY 1, 2`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var rel, name string
				var valid bool
				if err := rows.Scan(&rel, &name, &valid); err != nil {
					t.Fatal(err)
				}
				out = append(out, fmt.Sprintf("%s.%s:%v", rel, name, valid))
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return strings.Join(out, " ")
		}
		count := func(t *testing.T, sql string) int {
			t.Helper()
			var n int
			if err := d.Pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
			return n
		}
		const (
			v1 = "delivery_events.delivery_events_kind_check:true " +
				"delivery_events_p202610.delivery_events_kind_check:true"
			v2 = "delivery_events.delivery_events_kind_v2_check:true " +
				"delivery_events_p202610.delivery_events_kind_v2_check:true"
		)
		if got := checks(t); got != v1 {
			t.Fatalf("before 0010: %s", got)
		}
		if err := insert(t, "DE0000000000A2", "republished_past_edit_window"); err == nil {
			t.Error("the kind republished_past_edit_window was accepted before 0010")
		}
		for round := range 2 {
			if err := mm.Steps(1); err != nil {
				t.Fatalf("up %d: %v", round, err)
			}
			if got := checks(t); got != v2 {
				t.Errorf("after up %d: %s", round, got)
			}
			if count(t, `SELECT count(*) FROM pg_constraint WHERE conname = 'deliveries_actual_status_check'
				AND convalidated`) != 1 || count(t, `SELECT count(*) FROM pg_indexes
				WHERE indexname = 'deliveries_superseded_idx'`) != 1 {
				t.Errorf("after up %d: the actual_status check or the superseded index is missing", round)
			}
			if err := insert(t, fmt.Sprintf("DE0000000000B%d", round), "republished_past_edit_window"); err != nil {
				t.Errorf("after up %d: %v", round, err)
			}
			if err := insert(t, fmt.Sprintf("DE0000000000C%d", round), "no_such_kind"); err == nil {
				t.Errorf("after up %d: an unknown kind was accepted", round)
			}
			if round == 1 {
				break
			}
			if err := mm.Steps(-1); err != nil {
				t.Fatalf("down: %v", err)
			}
			if got := checks(t); got != v1 {
				t.Errorf("after down: %s", got)
			}
			if n := count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'republished_past_edit_window'`); n != 0 ||
				count(t, `SELECT count(*) FROM delivery_events`) != 1 || count(t, `SELECT count(*)
				FROM information_schema.columns WHERE table_name = 'deliveries'
				AND column_name IN ('actual_status', 'past_edit_window', 'superseded_message_ids')`) != 0 {
				t.Errorf("after down: %d events of the new kind, or the new columns are left", n)
			}
		}
		if v, dirty, err := mm.Version(); err != nil || dirty || v != 10 {
			t.Errorf("version %d, dirty %v, %v", v, dirty, err)
		}
	})
}

func TestIntegrationMigrate(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := openFresh(t, s)
		var out bytes.Buffer
		log := logging.New(&out, logging.LevelInfo)
		if err := d.CheckSchema(t.Context(), log); err == nil ||
			!strings.Contains(err.Error(), "database schema version 0 is older than this binary needs (10)") {
			t.Errorf("CheckSchema before migrating: %v", err)
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		if !strings.Contains(out.String(), `"event":"migrations_applied","from":0,"to":10`) {
			t.Errorf("logged %s", out.String())
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil || !strings.Contains(out.String(), `"event":"migrations_current","version":10`) {
			t.Errorf("Migrate again: %v, logged %s", err, out.String())
		}
		if err := d.CheckSchema(t.Context(), log); err != nil {
			t.Errorf("CheckSchema: %v", err)
		}

		if _, err := d.Pool.Exec(t.Context(), "UPDATE schema_migrations SET version = 999"); err != nil {
			t.Fatal(err)
		}
		for name, f := range map[string]func(context.Context, *logging.Logger) error{
			"Migrate": d.Migrate, "CheckSchema": d.CheckSchema,
		} {
			out.Reset()
			err := f(t.Context(), log)
			var se *SchemaError
			if !errors.As(err, &se) || err.Error() != "database schema version 999 is newer than this binary knows (10)" {
				t.Errorf("%s on version 999: %v", name, err)
			}
			if !strings.Contains(out.String(), `"event":"schema_too_new","database_version":999,"known_version":10`) {
				t.Errorf("%s logged %s", name, out.String())
			}
		}

		if _, err := d.Pool.Exec(t.Context(), "UPDATE schema_migrations SET version = 1, dirty = true"); err != nil {
			t.Fatal(err)
		}
		if err := d.Migrate(t.Context(), log); err == nil || !strings.Contains(err.Error(), "dirty at version 1") {
			t.Errorf("Migrate on a dirty schema: %v", err)
		}
	})
}

// TestIntegrationConcurrentMigrate starts two migrations at once: the lock lets one apply and the other find the
// schema current.
func TestIntegrationConcurrentMigrate(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := openFresh(t, s)
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			outs []string
			errs []error
		)
		for range 2 {
			wg.Go(func() {
				var out bytes.Buffer
				err := d.Migrate(t.Context(), logging.New(&out, logging.LevelInfo))
				mu.Lock()
				defer mu.Unlock()
				outs = append(outs, out.String())
				errs = append(errs, err)
			})
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
		all := strings.Join(outs, "")
		if strings.Count(all, `"event":"migrations_applied"`) != 1 || strings.Count(all, `"event":"migrations_current"`) != 1 {
			t.Errorf("two concurrent migrations logged:\n%s", all)
		}
	})
}

func TestIntegrationVersions(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := openFresh(t, s)
		if err := CheckVersion(t.Context(), d.Pool); err != nil {
			t.Errorf("PostgreSQL %s: %v", s.Version, err)
		}
	})
}
