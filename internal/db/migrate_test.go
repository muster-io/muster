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
	"github.com/jackc/pgx/v5/pgconn"

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
				AND column_name IN ('actual_status', 'past_edit_window', 'republication_started_at',
				'superseded_message_ids')`) != 0 {
				t.Errorf("after down: %d events of the new kind, or the new columns are left", n)
			}
		}
		if v, dirty, err := mm.Version(); err != nil || dirty || v != 10 {
			t.Errorf("version %d, dirty %v, %v", v, dirty, err)
		}
	})
}

// migrationEnv is a database migrated to a version, with its Organization, and the migrator that moves it.
type migrationEnv struct {
	d  *DB
	mm *migrate.Migrate
}

func newMigrationEnv(t *testing.T, s dbtest.Server, version uint) *migrationEnv {
	t.Helper()
	d := openFresh(t, s)
	c, err := d.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(t.Context(), c) })
	m, err := newMigrate(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	mm := m.(*migrate.Migrate)
	t.Cleanup(func() { _, _ = mm.Close() })
	if err := mm.Migrate(version); err != nil {
		t.Fatal(err)
	}
	e := &migrationEnv{d: d, mm: mm}
	e.exec(t, `INSERT INTO organizations (public_id, name, time_zone, severity_label, severity_mapping,
		severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
		retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
		oidc_token_grace_seconds, created_at, updated_at)
		VALUES ('RG0000000000AA', 'Muster', 'UTC', 'severity', '[]', '[]', true, '{}', 14, 90, 730, 365, 'nobody',
		604800, '2026-10-05', '2026-10-05')`)
	return e
}

func (e *migrationEnv) exec(t *testing.T, sql string) {
	t.Helper()
	if _, err := e.d.Pool.Exec(t.Context(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *migrationEnv) count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *migrationEnv) step(t *testing.T, n int) {
	t.Helper()
	if err := e.mm.Steps(n); err != nil {
		t.Fatalf("migrate %+d: %v", n, err)
	}
}

// constraints lists the constraints of the tables whose names start with prefix, as table.name:validated.
func (e *migrationEnv) constraints(t *testing.T, tables []string, prefix string) string {
	t.Helper()
	rows, err := e.d.Pool.Query(t.Context(), `SELECT c.relname, k.conname, k.convalidated
		FROM pg_constraint k JOIN pg_class c ON c.oid = k.conrelid
		WHERE c.relname = ANY($1) AND k.conname LIKE $2 || '%' ORDER BY 1, 2`, tables, prefix)
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

// TestIntegrationTransportCheck runs 0011 over a database whose partitioned timeline_entries and audit_log have a
// partition with a row: up adds the transport CHECK with bridge to the parents and the partitions and validates it on
// all of them, dropping the old one, so that bridge is accepted and an unknown Transport is not; down restores the old
// CHECK, validated while no row has the Transport bridge and NOT VALID once one has, which the down migration keeps
// (the Audit log is append-only), refusing new bridge rows either way; up again works from both states.
func TestIntegrationTransportCheck(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 10)
		for _, table := range []string{"timeline_entries", "audit_log"} {
			e.exec(t, fmt.Sprintf(`CREATE TABLE %s_p202610 PARTITION OF %s
				FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`, table, table))
		}
		n := 0
		insert := func(t *testing.T, transport string) error {
			t.Helper()
			n++
			if _, err := e.d.Pool.Exec(t.Context(), `INSERT INTO timeline_entries (org_id, public_id, alert_group_id,
				at, kind, system_event, actor_kind, actor_user_id, transport)
				SELECT id, $1, 1, '2026-10-05 12:00:00+00', 'system', 'muster_unavailable', 'user', 1, $2
				FROM organizations`, fmt.Sprintf("TE00000000%04d", n), transport); err != nil {
				return err
			}
			_, err := e.d.Pool.Exec(t.Context(), `INSERT INTO audit_log (org_id, public_id, at, actor_kind,
				actor_user_id, transport, action) SELECT id, $1, '2026-10-05 12:00:00+00', 'user', 1, $2,
				'alert_group.acknowledged' FROM organizations`, fmt.Sprintf("AE00000000%04d", n), transport)
			return err
		}
		checks := func(t *testing.T) string {
			t.Helper()
			return e.constraints(t, []string{"timeline_entries", "timeline_entries_p202610"},
				"timeline_entries_transport") + " " + e.constraints(t, []string{"audit_log", "audit_log_p202610"},
				"audit_log_transport")
		}
		old := func(valid bool) string {
			return fmt.Sprintf("timeline_entries.timeline_entries_transport_check:%[1]v "+
				"timeline_entries_p202610.timeline_entries_transport_check:%[1]v "+
				"audit_log.audit_log_transport_check:%[1]v audit_log_p202610.audit_log_transport_check:%[1]v", valid)
		}
		const v2 = "timeline_entries.timeline_entries_transport_v2_check:true " +
			"timeline_entries_p202610.timeline_entries_transport_v2_check:true " +
			"audit_log.audit_log_transport_v2_check:true audit_log_p202610.audit_log_transport_v2_check:true"
		if err := insert(t, "telegram"); err != nil {
			t.Fatal(err)
		}
		if got := checks(t); got != old(true) {
			t.Fatalf("before 0011: %s", got)
		}
		if err := insert(t, "bridge"); err == nil {
			t.Fatal("the Transport bridge was accepted before 0011")
		}

		e.step(t, 1)
		if got := checks(t); got != v2 {
			t.Errorf("after up: %s", got)
		}
		e.step(t, -1)
		if got := checks(t); got != old(true) {
			t.Errorf("after down without bridge rows: %s", got)
		}

		e.step(t, 1)
		if err := insert(t, "bridge"); err != nil {
			t.Fatalf("after up: %v", err)
		}
		if err := insert(t, "pigeon"); err == nil {
			t.Error("after up: an unknown Transport was accepted")
		}
		e.step(t, -1)
		if got := checks(t); got != old(false) {
			t.Errorf("after down with bridge rows: %s", got)
		}
		if got := e.count(t, `SELECT count(*) FROM timeline_entries WHERE transport = 'bridge'`) +
			e.count(t, `SELECT count(*) FROM audit_log WHERE transport = 'bridge'`); got != 2 {
			t.Errorf("after down: %d rows with the Transport bridge, want the 2 of history", got)
		}
		if err := insert(t, "bridge"); err == nil {
			t.Error("after down: the Transport bridge was accepted")
		}
		if err := insert(t, "cli"); err != nil {
			t.Errorf("after down: %v", err)
		}

		e.step(t, 1)
		if got := checks(t); got != v2 {
			t.Errorf("after up again: %s", got)
		}
		if err := insert(t, "bridge"); err != nil {
			t.Errorf("after up again: %v", err)
		}
		if v, dirty, err := e.mm.Version(); err != nil || dirty || v != 11 {
			t.Errorf("version %d, dirty %v, %v", v, dirty, err)
		}
	})
}

// TestIntegrationAccountLinksIdentitySpace runs 0011 over Mattermost and Telegram Account links: their identity
// spaces keep their values and the two unique constraints their names; a Bridge link's space is bridge:<connection
// id>, with its display name; the unique constraints and the type-checked foreign key hold. Down deletes the Bridge
// links and restores the old expression under the same names; up again works.
func TestIntegrationAccountLinksIdentitySpace(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 10)
		e.exec(t, `INSERT INTO users (org_id, public_id, login, name, role, source, status, created_at, updated_at)
			SELECT id, u.public_id, u.login, u.login, 'responder', 'local', 'active', '2026-10-05', '2026-10-05'
			FROM organizations, (VALUES ('SR0000000000A1', 'bob'), ('SR0000000000A2', 'vera')) AS u (public_id, login)`)
		e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			telegram_bot_api_base_url, telegram_update_mode, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			deleted_at, created_at, updated_at)
			SELECT id, c.public_id, c.type, c.name, c.server, c.bot_api, c.mode, '2026-10-05', 1, 1, '2026-10-05',
			'2026-10-05', '2026-10-05'
			FROM organizations, (VALUES ('CN0000000000M1', 'mattermost', 'mm', 'http://mm', NULL, NULL),
				('CN0000000000T1', 'telegram', 'tg', NULL, 'http://tg', 'long_polling'))
				AS c (public_id, type, name, server, bot_api, mode)`)
		link := func(t *testing.T, publicID, login, messenger, conn, external string) error {
			t.Helper()
			_, err := e.d.Pool.Exec(t.Context(), `INSERT INTO account_links (org_id, public_id, user_id, messenger,
				connection_id, external_id, created_at)
				SELECT u.org_id, $1, u.id, $3, c.id, $5, '2026-10-05' FROM users u, connections c
				WHERE u.login = $2 AND c.public_id = $4`, publicID, login, messenger, conn, external)
			return err
		}
		for _, l := range [][]string{{"AK0000000000M1", "bob", "mattermost", "CN0000000000M1", "u-bob"},
			{"AK0000000000T1", "bob", "telegram", "CN0000000000T1", "1001"}} {
			if err := link(t, l[0], l[1], l[2], l[3], l[4]); err != nil {
				t.Fatal(err)
			}
		}
		spaces := func(t *testing.T) string {
			t.Helper()
			rows, err := e.d.Pool.Query(t.Context(), `SELECT al.public_id, al.identity_space = CASE c.type
				WHEN 'telegram' THEN 'telegram' ELSE c.type || ':' || c.id::text END
				FROM account_links al JOIN connections c ON c.id = al.connection_id ORDER BY 1`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var id string
				var ok bool
				if err := rows.Scan(&id, &ok); err != nil {
					t.Fatal(err)
				}
				out = append(out, fmt.Sprintf("%s:%v", id, ok))
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return strings.Join(out, " ")
		}
		uniques := func(t *testing.T) int {
			t.Helper()
			return e.count(t, `SELECT count(*) FROM pg_constraint
				WHERE conrelid = 'account_links'::regclass AND contype = 'u'
				  AND conname IN ('account_links_account_key', 'account_links_user_space_key')
				  AND pg_get_constraintdef(oid) LIKE '%identity_space%'`)
		}
		refusedBy := func(t *testing.T, err error, constraint string) {
			t.Helper()
			var pe *pgconn.PgError
			if !errors.As(err, &pe) || pe.ConstraintName != constraint {
				t.Errorf("refused by %v, want %s", err, constraint)
			}
		}
		const kept = "AK0000000000M1:true AK0000000000T1:true"
		if got := spaces(t); got != kept || uniques(t) != 2 {
			t.Fatalf("before 0011: %s", got)
		}

		e.step(t, 1)
		if got := spaces(t); got != kept || uniques(t) != 2 {
			t.Errorf("after up: %s, %d unique constraints", got, uniques(t))
		}
		if e.count(t, `SELECT count(*) FROM account_links WHERE identity_space = 'mattermost:' || connection_id::text
			AND messenger = 'mattermost'`) != 1 {
			t.Error("after up: the Mattermost space changed")
		}
		e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url, bridge_event_delivery,
			bot_token_updated_at, limiter_limit, limiter_per_seconds, deleted_at, created_at, updated_at)
			SELECT id, 'CN0000000000B1', 'bridge', 'br', 'http://br', 'poll', '2026-10-05', 1, 1, '2026-10-05',
			'2026-10-05', '2026-10-05' FROM organizations`)
		if err := link(t, "AK0000000000B1", "bob", "bridge", "CN0000000000B1", "u-bob"); err != nil {
			t.Fatalf("a Bridge link: %v", err)
		}
		e.exec(t, `UPDATE account_links SET display_name = 'Bob Brown' WHERE public_id = 'AK0000000000B1'`)
		if e.count(t, `SELECT count(*) FROM account_links al JOIN connections c ON c.id = al.connection_id
			WHERE al.public_id = 'AK0000000000B1' AND al.identity_space = 'bridge:' || c.id::text
			  AND al.display_name = 'Bob Brown'`) != 1 {
			t.Error("the Bridge link's space is not bridge:<connection id>")
		}
		refusedBy(t, link(t, "AK0000000000B2", "vera", "bridge", "CN0000000000B1", "u-bob"), "account_links_account_key")
		refusedBy(t, link(t, "AK0000000000B3", "bob", "bridge", "CN0000000000B1", "u-bob2"),
			"account_links_user_space_key")
		if err := link(t, "AK0000000000B4", "vera", "bridge", "CN0000000000M1", "u-vera"); err == nil {
			t.Error("a Bridge link through a Mattermost Connection was accepted")
		}

		e.step(t, -1)
		if got := spaces(t); got != kept || uniques(t) != 2 {
			t.Errorf("after down: %s, %d unique constraints", got, uniques(t))
		}
		if e.count(t, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'account_links'
			AND column_name = 'display_name'`) != 0 || e.count(t, `SELECT count(*) FROM information_schema.tables
			WHERE table_name = 'bridge_events_seen'`) != 0 {
			t.Error("after down: display_name or bridge_events_seen is left")
		}
		if err := link(t, "AK0000000000B6", "vera", "bridge", "CN0000000000B1", "u-vera"); err == nil {
			t.Error("after down: a Bridge link was accepted")
		}
		refusedBy(t, link(t, "AK0000000000M2", "vera", "mattermost", "CN0000000000M1", "u-bob"),
			"account_links_account_key")

		e.step(t, 1)
		if got := spaces(t); got != kept || uniques(t) != 2 {
			t.Errorf("after up again: %s, %d unique constraints", got, uniques(t))
		}
		if err := link(t, "AK0000000000B7", "vera", "bridge", "CN0000000000B1", "u-vera"); err != nil {
			t.Errorf("after up again: %v", err)
		}
	})
}

func TestIntegrationMigrate(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := openFresh(t, s)
		var out bytes.Buffer
		log := logging.New(&out, logging.LevelInfo)
		if err := d.CheckSchema(t.Context(), log); err == nil ||
			!strings.Contains(err.Error(), "database schema version 0 is older than this binary needs (11)") {
			t.Errorf("CheckSchema before migrating: %v", err)
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		if !strings.Contains(out.String(), `"event":"migrations_applied","from":0,"to":11`) {
			t.Errorf("logged %s", out.String())
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil || !strings.Contains(out.String(), `"event":"migrations_current","version":11`) {
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
			if !errors.As(err, &se) || err.Error() != "database schema version 999 is newer than this binary knows (11)" {
				t.Errorf("%s on version 999: %v", name, err)
			}
			if !strings.Contains(out.String(), `"event":"schema_too_new","database_version":999,"known_version":11`) {
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
