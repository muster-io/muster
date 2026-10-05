// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package partitions

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

func migrated(t *testing.T, s dbtest.Server) *db.DB {
	t.Helper()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(t.Context(), conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo)); err != nil {
		t.Fatal(err)
	}
	return d
}

func partitionsOf(t *testing.T, d *db.DB, table string) []string {
	t.Helper()
	rows, err := d.Pool.Query(t.Context(), `SELECT c.relname::text || ' ' || pg_get_expr(c.relpartbound, c.oid)
		FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid WHERE i.inhparent = $1::regclass ORDER BY 1`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func exec(t *testing.T, d *db.DB, sql string) {
	t.Helper()
	if _, err := d.Pool.Exec(t.Context(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// TestIntegrationPartitions creates the partitions, runs twice without errors, drops only expired partitions and
// leaves no duplicates when two runs overlap.
func TestIntegrationPartitions(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := migrated(t, s)
		exec(t, d, `INSERT INTO organizations (public_id, name, time_zone, severity_label, severity_mapping,
			severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
			oidc_token_grace_seconds, created_at, updated_at)
			VALUES ('RG0000000000AA', 'Muster', 'UTC', 'severity', '[]', '[]', true, '{}', 14, 90, 730, 365, 'nobody',
			604800, '2026-10-05', '2026-10-05')`)
		now := time.Date(2026, 10, 5, 23, 59, 0, 0, time.UTC)
		var log bytes.Buffer
		m := New(func(ctx context.Context) (Session, error) { return d.ConnectSession(ctx) }, clock.NewManual(now),
			logging.New(&log, logging.LevelInfo))
		if err := m.Create(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := partitionsOf(t, d, "stored_snapshots")
		if len(got) != 8 || got[0] != "stored_snapshots_p20261005 FOR VALUES FROM ('2026-10-05 00:00:00+00') TO "+
			"('2026-10-06 00:00:00+00')" || !strings.HasPrefix(got[7], "stored_snapshots_p20261012 ") {
			t.Errorf("stored_snapshots partitions %v", got)
		}
		if got := partitionsOf(t, d, "snapshot_bodies"); len(got) != 8 ||
			got[0] != "snapshot_bodies_p20261005 FOR VALUES FROM ('2026-10-05') TO ('2026-10-06')" {
			t.Errorf("snapshot_bodies partitions %v", got)
		}
		for _, table := range []string{"timeline_entries", "delivery_events", "audit_log"} {
			got := partitionsOf(t, d, table)
			want := []string{
				table + "_p202610 FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')",
				table + "_p202611 FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00')",
				table + "_p202612 FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00')",
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s partitions %v, want %v", table, got, want)
			}
		}
		// Rows land in the partitions; the Audit log's row triggers reach them.
		exec(t, d, `INSERT INTO audit_log (org_id, public_id, at, actor_kind, transport, action)
			SELECT id, 'AE0000000000AA', '2026-10-05 12:00+00', 'system', 'system', 'test.created' FROM organizations`)

		// Old partitions, as an earlier month of running would have left them.
		for _, sql := range []string{
			`CREATE TABLE stored_snapshots_p20260920 PARTITION OF stored_snapshots
				FOR VALUES FROM ('2026-09-20 00:00:00+00') TO ('2026-09-21 00:00:00+00')`,
			`CREATE TABLE stored_snapshots_p20260921 PARTITION OF stored_snapshots
				FOR VALUES FROM ('2026-09-21 00:00:00+00') TO ('2026-09-22 00:00:00+00')`,
			`CREATE TABLE snapshot_bodies_p20260920 PARTITION OF snapshot_bodies FOR VALUES FROM ('2026-09-20') TO ('2026-09-21')`,
			`CREATE TABLE timeline_entries_p202606 PARTITION OF timeline_entries
				FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00')`,
			`CREATE TABLE timeline_entries_p202607 PARTITION OF timeline_entries
				FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00')`,
			`CREATE TABLE audit_log_p202509 PARTITION OF audit_log
				FOR VALUES FROM ('2025-09-01 00:00:00+00') TO ('2025-10-01 00:00:00+00')`,
			`CREATE TABLE audit_log_p202510 PARTITION OF audit_log
				FOR VALUES FROM ('2025-10-01 00:00:00+00') TO ('2025-11-01 00:00:00+00')`,
		} {
			exec(t, d, sql)
		}
		log.Reset()
		if err := m.Maintain(t.Context()); err != nil {
			t.Fatal(err)
		}
		for table, want := range map[string][2]int{
			"stored_snapshots": {9, 0}, "snapshot_bodies": {8, 0}, "timeline_entries": {4, 0}, "audit_log": {4, 0},
		} {
			if got := partitionsOf(t, d, table); len(got) != want[0] {
				t.Errorf("%s has %d partitions after retention, want %d: %v", table, len(got), want[0], got)
			}
		}
		if got := strings.Join(partitionsOf(t, d, "stored_snapshots"), "\n"); strings.Contains(got, "p20260920") ||
			!strings.Contains(got, "p20260921") {
			t.Errorf("stored_snapshots partitions after retention:\n%s", got)
		}
		for _, want := range []string{`"stored_snapshots_p20260920"`, `"snapshot_bodies_p20260920"`,
			`"timeline_entries_p202606"`, `"audit_log_p202509"`, `"created":[]`} {
			if !strings.Contains(log.String(), want) {
				t.Errorf("log lacks %s: %s", want, log.String())
			}
		}
		var tables int
		if err := d.Pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_class WHERE relname IN
			('stored_snapshots_p20260920', 'snapshot_bodies_p20260920', 'timeline_entries_p202606', 'audit_log_p202509')`,
		).Scan(&tables); err != nil || tables != 0 {
			t.Errorf("%d dropped partitions still exist as tables (%v)", tables, err)
		}

		// Two overlapping Leaders a day later: both succeed, and every partition exists once.
		m2 := New(func(ctx context.Context) (Session, error) { return d.ConnectSession(ctx) },
			clock.NewManual(now.Add(24*time.Hour)), logging.New(&bytes.Buffer{}, logging.LevelInfo))
		m.business = clock.NewManual(now.Add(24 * time.Hour))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, mm := range []*Maintainer{m, m2} {
			wg.Go(func() { errs[i] = mm.Maintain(t.Context()) })
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("overlapping runs: %v", errs)
		}
		got = partitionsOf(t, d, "stored_snapshots")
		if len(got) != 9 || !strings.HasPrefix(got[len(got)-1], "stored_snapshots_p20261013 ") {
			t.Errorf("stored_snapshots partitions after overlapping runs: %v", got)
		}
	})
}
