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
	"maps"
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

// TestIntegrationBridgeLinkRequests runs 0012 over Telegram and Mattermost link requests: before it a Bridge request is
// refused; after up the earlier requests and new ones without a method are accepted, a Bridge request needs a method,
// a Bridge code its target, a Bridge request a Bridge Connection, and a Bridge token's hash is unique among Bridge
// tokens while a code's is not; the CHECKs are validated. Down deletes the Bridge requests and restores the old CHECK,
// validated; up again works.
func TestIntegrationBridgeLinkRequests(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 11)
		e.exec(t, `INSERT INTO users (org_id, public_id, login, name, role, source, status, created_at, updated_at)
			SELECT id, 'SR0000000000A1', 'bob', 'bob', 'responder', 'local', 'active', '2026-10-05', '2026-10-05'
			FROM organizations`)
		e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			telegram_bot_api_base_url, telegram_update_mode, bridge_base_url, bridge_event_delivery,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at,
			updated_at)
			SELECT id, c.public_id, c.type, c.name, c.server, c.bot_api, c.mode, c.base, c.delivery, '\x00', 'k1',
			'2026-10-05', 1, 1, '2026-10-05', '2026-10-05'
			FROM organizations, (VALUES ('CN0000000000M1', 'mattermost', 'mm', 'http://mm', NULL, NULL, NULL, NULL),
				('CN0000000000T1', 'telegram', 'tg', NULL, 'http://tg', 'long_polling', NULL, NULL),
				('CN0000000000B1', 'bridge', 'br', NULL, NULL, NULL, 'http://br', 'poll'))
				AS c (public_id, type, name, server, bot_api, mode, base, delivery)`)
		n := 0
		request := func(t *testing.T, messenger, conn, method, target string, hash byte) error {
			t.Helper()
			n++
			cols, vals := "", ""
			if method != "" {
				cols, vals = ", method", ", '"+method+"'"
			}
			var tgt any
			if target != "" {
				tgt = target
			}
			_, err := e.d.Pool.Exec(t.Context(), `INSERT INTO account_link_requests (org_id, public_id, user_id,
				messenger, connection_id, secret_hash, target_external_id, created_at, expires_at`+cols+`)
				SELECT u.org_id, $1, u.id, $2, c.id, $3, $4, '2026-10-05', '2026-10-05 00:10'`+vals+`
				FROM users u, connections c WHERE u.login = 'bob' AND c.public_id = $5`,
				fmt.Sprintf("AR%012d", n), messenger, bytes.Repeat([]byte{hash}, 32), tgt, conn)
			return err
		}
		checks := func(t *testing.T) string {
			t.Helper()
			rows, err := e.d.Pool.Query(t.Context(), `SELECT conname, convalidated FROM pg_constraint
				WHERE conrelid = 'account_link_requests'::regclass AND contype = 'c'
				  AND conname NOT IN ('account_link_requests_public_id_check', 'account_link_requests_secret_hash_check',
				                      'account_link_requests_attempts_check')
				ORDER BY 1`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var name string
				var valid bool
				if err := rows.Scan(&name, &valid); err != nil {
					t.Fatal(err)
				}
				out = append(out, fmt.Sprintf("%s:%v", strings.TrimPrefix(name, "account_link_requests_"), valid))
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return strings.Join(out, " ")
		}
		indexes := func(t *testing.T) int {
			t.Helper()
			return e.count(t, `SELECT count(*) FROM pg_indexes WHERE tablename = 'account_link_requests'
				AND indexname IN ('account_link_requests_bridge_token_key', 'account_link_requests_bridge_target_idx')`)
		}
		for _, r := range []struct {
			messenger, conn, target string
			hash                    byte
		}{{"telegram", "CN0000000000T1", "", 1}, {"mattermost", "CN0000000000M1", "u-bob", 2}} {
			if err := request(t, r.messenger, r.conn, "", r.target, r.hash); err != nil {
				t.Fatal(err)
			}
		}
		const old = "messenger_check:true target_check:true"
		if got := checks(t); got != old || indexes(t) != 0 {
			t.Fatalf("before 0012: %s", got)
		}
		if err := request(t, "bridge", "CN0000000000B1", "", "", 3); err == nil {
			t.Error("before 0012: a Bridge request was accepted")
		}

		e.step(t, 1)
		const v2 = "bridge_method_check:true bridge_target_check:true messenger_check:true method_check:true " +
			"target_check:true"
		if got := checks(t); got != v2 || indexes(t) != 2 {
			t.Errorf("after up: %s, %d indexes", got, indexes(t))
		}
		for _, tt := range []struct {
			name                         string
			messenger, conn, method, tgt string
			hash                         byte
			ok                           bool
		}{
			{"a Telegram request without a method", "telegram", "CN0000000000T1", "", "", 4, true},
			{"a Mattermost request without a method", "mattermost", "CN0000000000M1", "", "u-vera", 5, true},
			{"a Bridge token", "bridge", "CN0000000000B1", "token", "", 6, true},
			{"a Bridge code", "bridge", "CN0000000000B1", "code", "u-bob", 7, true},
			{"a second Bridge code with the same hash", "bridge", "CN0000000000B1", "code", "u-bob", 7, true},
			{"a Bridge token whose hash a Telegram token has", "bridge", "CN0000000000B1", "token", "", 1, true},
			{"a Bridge request without a method", "bridge", "CN0000000000B1", "", "", 8, false},
			{"a Bridge code without a target", "bridge", "CN0000000000B1", "code", "", 9, false},
			{"an unknown method", "bridge", "CN0000000000B1", "deep_link", "", 10, false},
			{"a Bridge request through a Mattermost Connection", "bridge", "CN0000000000M1", "token", "", 11, false},
			{"a second Bridge token with the same hash", "bridge", "CN0000000000B1", "token", "", 6, false},
		} {
			if err := request(t, tt.messenger, tt.conn, tt.method, tt.tgt, tt.hash); (err == nil) != tt.ok {
				t.Errorf("after up, %s: %v, want accepted %v", tt.name, err, tt.ok)
			}
		}
		e.exec(t, `UPDATE account_link_requests SET target_display_name = 'Bob Brown' WHERE method = 'code'`)

		e.step(t, -1)
		if got := checks(t); got != old || indexes(t) != 0 {
			t.Errorf("after down: %s, %d indexes", got, indexes(t))
		}
		if got := e.count(t, `SELECT count(*) FROM account_link_requests`); got != 4 {
			t.Errorf("after down: %d requests, want the 4 of Telegram and Mattermost", got)
		}
		if e.count(t, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'account_link_requests'
			AND column_name IN ('method', 'target_display_name')`) != 0 {
			t.Error("after down: method or target_display_name is left")
		}
		if err := request(t, "bridge", "CN0000000000B1", "", "", 12); err == nil {
			t.Error("after down: a Bridge request was accepted")
		}

		e.step(t, 1)
		if got := checks(t); got != v2 || indexes(t) != 2 {
			t.Errorf("after up again: %s, %d indexes", got, indexes(t))
		}
		if err := request(t, "bridge", "CN0000000000B1", "token", "", 13); err != nil {
			t.Errorf("after up again: %v", err)
		}
		if v, dirty, err := e.mm.Version(); err != nil || dirty || v != 12 {
			t.Errorf("version %d, dirty %v, %v", v, dirty, err)
		}
	})
}

// TestIntegrationRequestLog runs 0013: before it there is no request_log; after up the records of a Connection, of an
// outgoing webhook Destination, of a Connection naming a messenger Destination and an Alert Group, and of no owner
// without an answer are accepted, every CHECK refuses what it bounds, a public_id is unique in its Organization, an
// owner must exist and the hard deletion of a Connection takes its records along; the four indexes exist. Down drops
// the table; up again works.
func TestIntegrationRequestLog(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 12)
		e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url, bot_token_ciphertext,
			bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at)
			SELECT id, c.public_id, 'mattermost', c.public_id, 'http://mm', '\x00', 'k1', '2026-10-05', 1, 1,
				'2026-10-05', '2026-10-05'
			FROM organizations, (VALUES ('CN0000000000M1'), ('CN0000000000M2')) AS c (public_id)`)
		e.exec(t, `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode, webhook_events_config,
			signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at, mentions, limiter_limit,
			limiter_per_seconds, health, created_at, updated_at)
			SELECT id, 'DS0000000000W1', 'webhook', 'hook', 'events', '{}', '\x00', 'k1', '2026-10-05', '{}', 1, 1,
				'healthy', '2026-10-05', '2026-10-05'
			FROM organizations`)
		e.exec(t, `INSERT INTO destinations (org_id, public_id, type, name, connection_id, mattermost_team_id,
			mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at)
			SELECT c.org_id, 'DS0000000000M1', 'mattermost', 'alerts', c.id, 't1', 'ch1', '{}', 1, 1, 'healthy',
				'2026-10-05', '2026-10-05'
			FROM connections c WHERE c.public_id = 'CN0000000000M1'`)
		if got := e.count(t, `SELECT count(*) FROM pg_class WHERE relname = 'request_log'`); got != 0 {
			t.Fatal("before 0013: request_log exists")
		}

		e.step(t, 1)
		n := 0
		record := func(t *testing.T, set map[string]string) error {
			t.Helper()
			n++
			cols := map[string]string{
				"public_id": fmt.Sprintf("'RQ%012d'", n), "at": "'2026-10-05 12:00:00+00'", "direction": "'outbound'",
				"operation": "'createPost'", "method": "'POST'", "url": "'http://mm/api/v4/posts'",
				"request_headers": `'{"User-Agent": ["Muster/1.0.0"]}'`, "request_body": "'{}'",
				"request_body_bytes": "2", "request_body_truncated": "false", "status": "201",
				"response_headers": "'{}'", "response_body": "'{}'", "response_body_bytes": "2",
				"response_body_truncated": "false", "latency_ms": "12", "via": "'direct'", "attempt": "1",
				"connection_id": "(SELECT id FROM connections WHERE public_id = 'CN0000000000M1')",
			}
			maps.Copy(cols, set)
			names, values := make([]string, 0, len(cols)), make([]string, 0, len(cols))
			for k, v := range cols {
				names, values = append(names, k), append(values, v)
			}
			_, err := e.d.Pool.Exec(t.Context(), `INSERT INTO request_log (org_id, `+strings.Join(names, ", ")+`)
				SELECT id, `+strings.Join(values, ", ")+` FROM organizations`)
			return err
		}
		code := func(err error) string {
			var pe *pgconn.PgError
			switch {
			case err == nil:
				return ""
			case errors.As(err, &pe):
				return pe.Code
			}
			return err.Error()
		}
		const (
			webhook   = "(SELECT id FROM destinations WHERE public_id = 'DS0000000000W1')"
			messenger = "(SELECT id FROM destinations WHERE public_id = 'DS0000000000M1')"
		)
		noAnswer := map[string]string{"status": "NULL", "response_headers": "NULL", "response_body": "NULL",
			"response_body_bytes": "0"}
		with := func(base map[string]string, kv ...string) map[string]string {
			out := maps.Clone(base)
			if out == nil {
				out = map[string]string{}
			}
			for i := 0; i+1 < len(kv); i += 2 {
				out[kv[i]] = kv[i+1]
			}
			return out
		}
		for _, tt := range []struct {
			name string
			set  map[string]string
			want string
		}{
			{"a Connection's record", nil, ""},
			{"an outgoing webhook's record", with(nil, "connection_id", "NULL", "destination_id", webhook,
				"status", "500", "error_class", "'transient'", "error", "'500 Internal Server Error (transient)'"), ""},
			{"a record naming a messenger Destination and an Alert Group", with(nil, "destination_id", messenger,
				"alert_group_id", "42"), ""},
			{"a record of no owner without an answer", with(noAnswer, "connection_id", "NULL",
				"error_class", "'transient'", "error_code", "'connect_failed'", "error", "'connection refused'",
				"check_kind", "'draft_check'", "check_run", "'CR0000000000A1'"), ""},
			{"an inbound record", with(nil, "direction", "'inbound'", "operation", "'press'"), ""},
			{"a wrong public_id prefix", with(nil, "public_id", "'XX0000000000A1'"), "23514"},
			{"an empty operation", with(nil, "operation", "''"), "23514"},
			{"an operation of 65 characters", with(nil, "operation", "'"+strings.Repeat("o", 65)+"'"), "23514"},
			{"an unknown direction", with(nil, "direction", "'sideways'"), "23514"},
			{"a status without answer headers", with(nil, "response_headers", "NULL"), "23514"},
			{"a status without an answer body", with(nil, "response_body", "NULL"), "23514"},
			{"answer headers without a status", with(noAnswer, "response_headers", "'{}'"), "23514"},
			{"a status below 100", with(nil, "status", "99"), "23514"},
			{"an error code with a status", with(nil, "error_code", "'timeout'"), "23514"},
			{"an unknown error code", with(noAnswer, "error_code", "'eof'"), "23514"},
			{"an unknown error class", with(nil, "error_class", "'ok'"), "23514"},
			{"an unknown check kind", with(nil, "check_kind", "'other'"), "23514"},
			{"a check run that is no CR id", with(nil, "check_run", "'RQ0000000000A1'"), "23514"},
			{"an unknown path", with(nil, "via", "'tunnel'"), "23514"},
			{"attempt 0", with(nil, "attempt", "0"), "23514"},
			{"a negative latency", with(nil, "latency_ms", "-1"), "23514"},
			{"headers that are no object", with(nil, "request_headers", "'[]'"), "23514"},
			{"an empty method", with(nil, "method", "''"), "23514"},
			{"a public_id used twice", with(nil, "public_id", "'RQ000000000001'"), "23505"},
			{"an unknown Connection", with(nil, "connection_id", "-1"), "23503"},
			{"an unknown Destination", with(nil, "destination_id", "-1"), "23503"},
		} {
			if got := code(record(t, tt.set)); got != tt.want {
				t.Errorf("%s: %s, want %q", tt.name, got, tt.want)
			}
		}
		for range 3 {
			if err := record(t, map[string]string{
				"connection_id": "(SELECT id FROM connections WHERE public_id = 'CN0000000000M2')"}); err != nil {
				t.Fatal(err)
			}
		}
		e.exec(t, `DELETE FROM connections WHERE public_id = 'CN0000000000M2'`)
		if got := e.count(t, `SELECT count(*) FROM request_log`); got != 5 {
			t.Errorf("after the deletion of a Connection: %d records, want the 5 of the others", got)
		}
		indexes := func(t *testing.T) int {
			t.Helper()
			return e.count(t, `SELECT count(*) FROM pg_indexes WHERE tablename = 'request_log'
				AND indexname IN ('request_log_connection_idx', 'request_log_destination_idx',
					'request_log_unowned_idx', 'request_log_at_idx')`)
		}
		if got := indexes(t); got != 4 {
			t.Errorf("after up: %d of the 4 indexes", got)
		}

		e.step(t, -1)
		if got := e.count(t, `SELECT count(*) FROM pg_class WHERE relname = 'request_log'`); got != 0 {
			t.Error("after down: request_log is left")
		}

		e.step(t, 1)
		if got := indexes(t); got != 4 {
			t.Errorf("after up again: %d of the 4 indexes", got)
		}
		if err := record(t, nil); err != nil {
			t.Errorf("after up again: %v", err)
		}
		if v, dirty, err := e.mm.Version(); err != nil || dirty || v != 13 {
			t.Errorf("version %d, dirty %v, %v", v, dirty, err)
		}
	})
}

// TestIntegrationAlertsOrgLastSeenIndex runs 0014 over an Organization with 90 days of Alerts: up adds
// alerts_org_last_seen_idx, through which the values seen of the severity label range over the last 14 days of the
// Organization's Alerts; down drops it; up again adds it.
func TestIntegrationAlertsOrgLastSeenIndex(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 13)
		e.exec(t, `INSERT INTO integrations (org_id, public_id, name, connection_mode, static_labels,
			duplicate_window_seconds, heartbeat_enabled, heartbeat_timeout_seconds, heartbeat_state, created_at,
			updated_at)
			SELECT id, i.public_id, i.public_id, 'webhook_only', '{}', 45, false, 300, 'not_configured',
				'2026-10-05', '2026-10-05'
			FROM organizations, (VALUES ('NT0000000000A1'), ('NT0000000000A2')) AS i (public_id)`)
		e.exec(t, `INSERT INTO alerts (org_id, integration_id, fingerprint, labels, annotations, status, starts_at,
			fired_at, first_seen_at, last_seen_at, updated_at)
			SELECT i.org_id, i.id, i.public_id || '-' || n, jsonb_build_object('severity', 'P' || n % 5), '{}',
				'firing', at, at, at, at, at
			FROM integrations i, generate_series(1, 3000) AS n,
				LATERAL (SELECT timestamptz '2026-10-05' - n * interval '43 minutes' AS at) seen`)
		e.exec(t, `ANALYZE alerts`)
		index := func(t *testing.T) int {
			t.Helper()
			return e.count(t, `SELECT count(*) FROM pg_indexes WHERE tablename = 'alerts'
				AND indexname = 'alerts_org_last_seen_idx' AND indexdef LIKE '%(org_id, last_seen_at)'`)
		}
		plan := func(t *testing.T) string {
			t.Helper()
			tx, err := e.d.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
			if _, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan = off`); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(t.Context(), `EXPLAIN SELECT (labels ->> 'severity')::text AS value,
				count(*)::bigint AS alerts
				FROM alerts
				WHERE org_id = (SELECT id FROM organizations) AND last_seen_at >= timestamptz '2026-09-21'
					AND labels ->> 'severity' <> ''
				GROUP BY 1 ORDER BY alerts DESC, value LIMIT 500`)
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			for rows.Next() {
				var l string
				if err := rows.Scan(&l); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, l)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return strings.Join(lines, "\n")
		}
		if got := index(t); got != 0 {
			t.Fatal("before 0014: alerts_org_last_seen_idx exists")
		}
		if p := plan(t); strings.Contains(p, "alerts_org_last_seen_idx") {
			t.Fatalf("before 0014: %s", p)
		}

		e.step(t, 1)
		if got := index(t); got != 1 {
			t.Errorf("after up: %d indexes", got)
		}
		if p := plan(t); !strings.Contains(p, "alerts_org_last_seen_idx") {
			t.Errorf("after up the values seen do not range over alerts_org_last_seen_idx:\n%s", p)
		}

		e.step(t, -1)
		if got := index(t); got != 0 {
			t.Error("after down: alerts_org_last_seen_idx is left")
		}

		e.step(t, 1)
		if v, dirty, err := e.mm.Version(); err != nil || dirty || v != 14 || index(t) != 1 {
			t.Errorf("up again: version %d, dirty %v, %v, %d indexes", v, dirty, err, index(t))
		}
	})
}

// TestIntegrationSnoozePresets runs 0015 over an existing Organization: up gives it workday_start 09:00 and week_start
// monday under validated CHECKs, which refuse 24:00, 9:00 and weekend and accept 08:30 with sunday; down drops both
// columns; up again gives the defaults back.
func TestIntegrationSnoozePresets(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 14)
		columns := func(t *testing.T) int {
			t.Helper()
			return e.count(t, `SELECT count(*) FROM information_schema.columns
				WHERE table_name = 'organizations' AND column_name IN ('workday_start', 'week_start')`)
		}
		settings := func(t *testing.T) string {
			t.Helper()
			var start, week string
			if err := e.d.Pool.QueryRow(t.Context(), `SELECT workday_start, week_start FROM organizations`).Scan(&start,
				&week); err != nil {
				t.Fatal(err)
			}
			return start + "|" + week
		}
		if got := columns(t); got != 0 {
			t.Fatal("before 0015: the columns exist")
		}

		e.step(t, 1)
		if got := settings(t); got != "09:00|monday" {
			t.Errorf("after up: %s", got)
		}
		if got := e.constraints(t, []string{"organizations"}, "organizations_w"); got !=
			"organizations.organizations_week_start_check:true organizations.organizations_workday_start_check:true" {
			t.Errorf("after up: constraints %s", got)
		}
		for _, set := range []string{"workday_start = '24:00'", "workday_start = '9:00'", "workday_start = '09:60'",
			"week_start = 'weekend'", "week_start = 'Monday'"} {
			_, err := e.d.Pool.Exec(t.Context(), `UPDATE organizations SET `+set)
			if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.Code != "23514" {
				t.Errorf("%s: %v", set, err)
			}
		}
		e.exec(t, `UPDATE organizations SET workday_start = '08:30', week_start = 'sunday'`)
		if got := settings(t); got != "08:30|sunday" {
			t.Errorf("an accepted change: %s", got)
		}

		e.step(t, -1)
		if got := columns(t); got != 0 {
			t.Error("after down: the columns are left")
		}

		e.step(t, 1)
		if v, dirty, err := e.mm.Version(); err != nil || dirty || v != 15 || settings(t) != "09:00|monday" {
			t.Errorf("up again: version %d, dirty %v, %v, %s", v, dirty, err, settings(t))
		}
	})
}

// TestIntegrationSlackConnections runs 0016 over a database with a Mattermost and a Bridge Connection: up refuses
// nothing they hold and validates the replaced and the new CHECKs, so that a Slack Connection is accepted in socket
// mode with its app-level token and in http mode with its Slack signing secret, deleted without either, and refused
// without the secret of its mode, with an unknown event mode, with another type's address, or with a ciphertext
// without its key id, as another type with an event mode is; encrypted_values lists both new secrets; down deletes the
// Slack Connections and drops their columns, refusing the type again; up again works.
func TestIntegrationSlackConnections(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 15)
		e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url, bridge_base_url,
			bridge_event_delivery, bridge_push_secret_ciphertext, bridge_push_secret_key_id, bot_token_ciphertext,
			bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at)
			SELECT id, c.public_id, c.type, c.name, c.server, c.base, c.delivery, c.push, c.push_key, '\x01', 'k1',
			'2026-10-05', 1, 1, '2026-10-05', '2026-10-05'
			FROM organizations, (VALUES ('CN0000000000M1', 'mattermost', 'mm', 'http://mm', NULL, NULL, NULL, NULL),
				('CN0000000000B1', 'bridge', 'br', NULL, 'http://br', 'push', '\x02'::bytea, 'k1'))
				AS c (public_id, type, name, server, base, delivery, push, push_key)`)
		insert := func(t *testing.T, id, columns, values string) error {
			t.Helper()
			_, err := e.d.Pool.Exec(t.Context(), `INSERT INTO connections (org_id, public_id, type, name,
				bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
				created_at, updated_at`+columns+`)
				SELECT id, '`+id+`', 'slack', '`+id+`', '\x01', 'k1', '2026-10-05', 50, 60, '2026-10-05',
				'2026-10-05'`+values+` FROM organizations`)
			return err
		}
		refused := func(t *testing.T, name string, err error, constraint string) {
			t.Helper()
			if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.Code != "23514" || pe.ConstraintName != constraint {
				t.Errorf("%s: %v", name, err)
			}
		}
		refused(t, "before 0016", insert(t, "CN0000000000S0", ``, ``), "connections_type_check")

		e.step(t, 1)
		if got := e.constraints(t, []string{"connections"}, "connections_"); !strings.Contains(got,
			"connections.connections_slack_app_token_check:true") ||
			!strings.Contains(got, "connections.connections_slack_signing_secret_check:true") ||
			!strings.Contains(got, "connections.connections_type_check:true") ||
			!strings.Contains(got, "connections.connections_type_fields_check:true") {
			t.Errorf("after up: constraints %s", got)
		}
		for _, ok := range []struct {
			name             string
			id, cols, values string
		}{
			{"socket", "CN0000000000S1", `, slack_event_mode, slack_app_token_ciphertext, slack_app_token_key_id,
				slack_signing_secret_ciphertext, slack_signing_secret_key_id`, `, 'socket', '\x03', 'k1', '\x04', 'k1'`},
			{"http", "CN0000000000S2", `, slack_event_mode, slack_signing_secret_ciphertext,
				slack_signing_secret_key_id`, `, 'http', '\x04', 'k1'`},
			{"deleted", "CN0000000000S3", `, slack_event_mode, deleted_at`, `, 'socket', '2026-10-06'`},
		} {
			if err := insert(t, ok.id, ok.cols, ok.values); err != nil {
				t.Errorf("%s: %v", ok.name, err)
			}
		}
		for _, bad := range []struct {
			name, cols, values, constraint string
		}{
			{"socket without app-level token", `, slack_event_mode, slack_signing_secret_ciphertext,
				slack_signing_secret_key_id`, `, 'socket', '\x04', 'k1'`, "connections_type_fields_check"},
			{"http without Slack signing secret", `, slack_event_mode, slack_app_token_ciphertext,
				slack_app_token_key_id`, `, 'http', '\x03', 'k1'`, "connections_type_fields_check"},
			{"no event mode", ``, ``, "connections_type_fields_check"},
			{"unknown event mode", `, slack_event_mode, slack_app_token_ciphertext, slack_app_token_key_id`,
				`, 'poll', '\x03', 'k1'`, "connections_type_fields_check"},
			{"a server URL", `, slack_event_mode, slack_app_token_ciphertext, slack_app_token_key_id,
				mattermost_server_url`, `, 'socket', '\x03', 'k1', 'http://mm'`, "connections_type_fields_check"},
			{"a bridge address", `, slack_event_mode, slack_app_token_ciphertext, slack_app_token_key_id,
				bridge_base_url`, `, 'socket', '\x03', 'k1', 'http://br'`, "connections_type_fields_check"},
			{"an app-level token without its key", `, slack_event_mode, slack_app_token_ciphertext`,
				`, 'socket', '\x03'`, "connections_slack_app_token_check"},
			{"a Slack signing secret without its key", `, slack_event_mode, slack_app_token_ciphertext,
				slack_app_token_key_id, slack_signing_secret_ciphertext`, `, 'socket', '\x03', 'k1', '\x04'`,
				"connections_slack_signing_secret_check"},
		} {
			refused(t, bad.name, insert(t, "CN0000000000S9", bad.cols, bad.values), bad.constraint)
		}
		_, err := e.d.Pool.Exec(t.Context(), `UPDATE connections SET slack_event_mode = 'socket'
			WHERE public_id = 'CN0000000000M1'`)
		refused(t, "a Mattermost Connection with an event mode", err, "connections_type_fields_check")
		if n := e.count(t, `SELECT count(*) FROM encrypted_values WHERE row_key = (SELECT id::text FROM connections
			WHERE public_id = 'CN0000000000S1') AND field IN ('connections.slack_app_token',
			'connections.slack_signing_secret')`); n != 2 {
			t.Errorf("encrypted_values lists %d Slack secrets", n)
		}

		e.step(t, -1)
		if n := e.count(t, `SELECT count(*) FROM connections WHERE type = 'slack'`); n != 0 {
			t.Errorf("after down: %d Slack Connections", n)
		}
		if n := e.count(t, `SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'connections' AND column_name LIKE 'slack%'`); n != 0 {
			t.Errorf("after down: %d Slack columns", n)
		}
		if n := e.count(t, `SELECT count(*) FROM connections`); n != 2 {
			t.Errorf("after down: %d Connections", n)
		}
		if got := e.constraints(t, []string{"connections"}, "connections_type"); got !=
			"connections.connections_type_check:true connections.connections_type_fields_check:true" {
			t.Errorf("after down: constraints %s", got)
		}
		refused(t, "after down", insert(t, "CN0000000000S4", ``, ``), "connections_type_check")

		e.step(t, 1)
		if v, dirty, err := e.mm.Version(); err != nil || dirty || v != 16 {
			t.Errorf("up again: version %d, dirty %v, %v", v, dirty, err)
		}
	})
}

// TestIntegrationSlackDestinations runs 0017 over a database with Mattermost, Bridge and Slack Connections and a
// Mattermost and a Bridge Destination: up refuses nothing they hold and validates both CHECKs; a Slack Destination
// needs a Slack Connection and a channel id and takes none of the other types' columns, and the other types take no
// Slack column; down deletes the Slack Destinations with their places on Routes, their delivery events and their
// limiter buckets, drops the columns and refuses the type again; up again works.
func TestIntegrationSlackDestinations(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 16)
		e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url, bridge_base_url,
			bridge_event_delivery, slack_event_mode, slack_app_token_ciphertext, slack_app_token_key_id,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at)
			SELECT id, c.public_id, c.type, c.name, c.server, c.base, c.delivery, c.mode, c.app, c.app_key, '\x01',
			'k1', '2026-10-05', 1, 1, '2026-10-05', '2026-10-05'
			FROM organizations, (VALUES
				('CN0000000000M1', 'mattermost', 'mm', 'http://mm', NULL, NULL, NULL, NULL, NULL),
				('CN0000000000B1', 'bridge', 'br', NULL, 'http://br', 'poll', NULL, NULL, NULL),
				('CN0000000000S1', 'slack', 'sl', NULL, NULL, NULL, 'socket', '\x03'::bytea, 'k1'))
				AS c (public_id, type, name, server, base, delivery, mode, app, app_key)`)
		e.exec(t, `INSERT INTO destinations (org_id, public_id, type, name, connection_id, mattermost_team_id,
			mattermost_channel_id, bridge_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at,
			updated_at)
			SELECT c.org_id, d.public_id, c.type, d.name, c.id, d.team, d.channel, d.bridge, '{}', 1, 1, 'healthy',
				'2026-10-05', '2026-10-05'
			FROM connections c JOIN (VALUES ('DS0000000000M1', 'mm', 'CN0000000000M1', 't1', 'ch1', NULL),
				('DS0000000000B1', 'br', 'CN0000000000B1', NULL, NULL, 'ch-br')) AS d (public_id, name, conn, team,
				channel, bridge) ON d.conn = c.public_id`)
		insert := func(t *testing.T, id, conn, typ, columns, values string) error {
			t.Helper()
			_, err := e.d.Pool.Exec(t.Context(), `INSERT INTO destinations (org_id, public_id, type, name,
				connection_id, mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at`+columns+`)
				SELECT c.org_id, '`+id+`', '`+typ+`', '`+id+`', c.id, '{}', 30, 60, 'healthy', '2026-10-05',
				'2026-10-05'`+values+` FROM connections c WHERE c.public_id = '`+conn+`'`)
			return err
		}
		refused := func(t *testing.T, name string, err error, code, constraint string) {
			t.Helper()
			if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.Code != code || pe.ConstraintName != constraint {
				t.Errorf("%s: %v", name, err)
			}
		}
		refused(t, "before 0017", insert(t, "DS0000000000S0", "CN0000000000S1", "slack", ``, ``), "23514",
			"destinations_type_check")

		e.step(t, 1)
		if got := e.constraints(t, []string{"destinations"}, "destinations_type"); got !=
			"destinations.destinations_type_check:true destinations.destinations_type_fields_check:true" {
			t.Errorf("after up: constraints %s", got)
		}
		for _, ok := range []struct {
			name, id, cols, values string
		}{
			{"a channel and its name", "DS0000000000S1", `, slack_channel_id, slack_channel_name`,
				`, 'C0FAKEALERTS', 'alerts'`},
			{"a channel without a name", "DS0000000000S2", `, slack_channel_id`, `, 'G0FAKEOPS'`},
			{"deleted", "DS0000000000S3", `, slack_channel_id, slack_channel_name, deleted_at`,
				`, 'C0FAKEOLD', 'old', '2026-10-06'`},
		} {
			if err := insert(t, ok.id, "CN0000000000S1", "slack", ok.cols, ok.values); err != nil {
				t.Errorf("%s: %v", ok.name, err)
			}
		}
		for _, bad := range []struct {
			name, conn, typ, cols, values, code, constraint string
		}{
			{"no channel id", "CN0000000000S1", "slack", ``, ``, "23514", "destinations_type_fields_check"},
			{"a Mattermost team", "CN0000000000S1", "slack", `, slack_channel_id, mattermost_team_id`,
				`, 'C0FAKEALERTS', 't1'`, "23514", "destinations_type_fields_check"},
			{"a Telegram channel", "CN0000000000S1", "slack", `, slack_channel_id, telegram_channel_id`,
				`, 'C0FAKEALERTS', '@alerts'`, "23514", "destinations_type_fields_check"},
			{"a Bridge channel", "CN0000000000S1", "slack", `, slack_channel_id, bridge_channel_id`,
				`, 'C0FAKEALERTS', 'ch-br'`, "23514", "destinations_type_fields_check"},
			{"a webhook mode", "CN0000000000S1", "slack", `, slack_channel_id, webhook_mode`,
				`, 'C0FAKEALERTS', 'events'`, "23514", "destinations_type_fields_check"},
			{"through a Mattermost Connection", "CN0000000000M1", "slack", `, slack_channel_id`, `, 'C0FAKEALERTS'`,
				"23503", "destinations_connection_id_type_fkey"},
			{"a Bridge Destination with a Slack channel", "CN0000000000B1", "bridge",
				`, bridge_channel_id, slack_channel_id`, `, 'ch-br', 'C0FAKEALERTS'`, "23514",
				"destinations_type_fields_check"},
			{"a Mattermost Destination with a Slack name", "CN0000000000M1", "mattermost",
				`, mattermost_team_id, mattermost_channel_id, slack_channel_name`, `, 't1', 'ch2', 'alerts'`, "23514",
				"destinations_type_fields_check"},
		} {
			refused(t, bad.name, insert(t, "DS0000000000S9", bad.conn, bad.typ, bad.cols, bad.values), bad.code,
				bad.constraint)
		}
		e.exec(t, `INSERT INTO routes (org_id, public_id, name, position, urgent, group_key, reopen_window_seconds,
			grace_period_seconds, urgent_rise_removes_ack, snooze_durations_seconds, thread_batching_window_seconds,
			storm_threshold, language, ack_timeout_enabled, ack_timeout_first_interval_seconds, reminders_enabled,
			reminders_first_interval_seconds, reminders_cap_seconds, auto_unacknowledge, created_at, updated_at)
			SELECT id, 'RT0000000000R1', 'r1', 1, false, '{}', 0, 0, false, '{}', 0, 10, 'en', false, 60, false, 60,
				60, false, '2026-10-05', '2026-10-05'
			FROM organizations`)
		e.exec(t, `INSERT INTO route_destinations (route_id, destination_id, org_id, added_at)
			SELECT r.id, d.id, d.org_id, '2026-10-05' FROM routes r, destinations d
			WHERE d.public_id IN ('DS0000000000S1', 'DS0000000000M1')`)
		e.exec(t, `CREATE TABLE delivery_events_p202610 PARTITION OF delivery_events
			FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`)
		e.exec(t, `INSERT INTO delivery_events (org_id, public_id, occurred_at, destination_id, kind, loudness)
			SELECT d.org_id, 'DE' || substr(d.public_id, 3), '2026-10-05 12:00:00+00', d.id, 'final_edit', 'quiet'
			FROM destinations d WHERE d.public_id IN ('DS0000000000S1', 'DS0000000000M1')`)
		e.exec(t, `INSERT INTO rate_limit_buckets (subject_kind, subject_id, org_id, tokens, refilled_at)
			SELECT 'destination', d.id, d.org_id, 1, '2026-10-05' FROM destinations d
			WHERE d.public_id IN ('DS0000000000S1', 'DS0000000000M1')`)

		e.step(t, -1)
		for what, sql := range map[string]string{
			"Slack Destinations": `SELECT count(*) FROM destinations WHERE public_id LIKE 'DS0000000000S%'`,
			"Slack columns": `SELECT count(*) FROM information_schema.columns
				WHERE table_name = 'destinations' AND column_name LIKE 'slack%'`,
		} {
			if n := e.count(t, sql); n != 0 {
				t.Errorf("after down: %d %s", n, what)
			}
		}
		for what, sql := range map[string]string{
			"Destinations":    `SELECT count(*) FROM destinations`,
			"Route places":    `SELECT count(*) FROM route_destinations`,
			"delivery events": `SELECT count(*) FROM delivery_events`,
			"limiter buckets": `SELECT count(*) FROM rate_limit_buckets`,
		} {
			want := 1
			if what == "Destinations" {
				want = 2
			}
			if n := e.count(t, sql); n != want {
				t.Errorf("after down: %d %s, want %d", n, what, want)
			}
		}
		if got := e.constraints(t, []string{"destinations"}, "destinations_type"); got !=
			"destinations.destinations_type_check:true destinations.destinations_type_fields_check:true" {
			t.Errorf("after down: constraints %s", got)
		}
		refused(t, "after down", insert(t, "DS0000000000S4", "CN0000000000S1", "slack", ``, ``), "23514",
			"destinations_type_check")

		e.step(t, 1)
		if v, dirty, err := e.mm.Version(); err != nil || dirty || v != 17 {
			t.Errorf("up again: version %d, dirty %v, %v", v, dirty, err)
		}
	})
}

// TestIntegrationSlackPressesAndLinks runs 0018 over a database whose partitioned timeline_entries and audit_log have
// a partition, with a Mattermost and a Slack Connection, a Mattermost link and a Mattermost request: before it the
// Transport slack and the messenger slack are refused; up adds the transport CHECK with slack to the parents and the
// partitions and validates it on all of them, dropping the old one, and replaces the messenger CHECKs under their
// names, validated, so that slack is accepted and an unknown Transport is not, a Slack link's identity space is
// slack:<connection id>, a Slack link needs a Slack Connection, a Slack request its target account, and the index of
// the per-target limit exists. Down deletes the Slack links and requests and restores the old CHECKs, the transport
// ones validated while no row has the Transport slack and NOT VALID once one has, which it keeps, refusing new Slack
// rows either way; up again works from both states.
func TestIntegrationSlackPressesAndLinks(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 17)
		for _, table := range []string{"timeline_entries", "audit_log"} {
			e.exec(t, fmt.Sprintf(`CREATE TABLE %s_p202610 PARTITION OF %s
				FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`, table, table))
		}
		e.exec(t, `INSERT INTO users (org_id, public_id, login, name, role, source, status, created_at, updated_at)
			SELECT id, u.public_id, u.login, u.login, 'responder', 'local', 'active', '2026-10-05', '2026-10-05'
			FROM organizations, (VALUES ('SR0000000000A1', 'bob'), ('SR0000000000A2', 'vera')) AS u (public_id, login)`)
		e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url, slack_event_mode,
			slack_signing_secret_ciphertext, slack_signing_secret_key_id, bot_token_ciphertext, bot_token_key_id,
			bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at)
			SELECT id, c.public_id, c.type, c.name, c.server, c.mode, c.secret, c.secret_key, '\x01', 'k1',
			'2026-10-05', 1, 1, '2026-10-05', '2026-10-05'
			FROM organizations, (VALUES ('CN0000000000M1', 'mattermost', 'mm', 'http://mm', NULL, NULL, NULL),
				('CN0000000000S1', 'slack', 'sl', NULL, 'http', '\x04'::bytea, 'k1'))
				AS c (public_id, type, name, server, mode, secret, secret_key)`)
		n := 0
		transport := func(t *testing.T, transport string) error {
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
		link := func(t *testing.T, publicID, login, messenger, conn, external string) error {
			t.Helper()
			_, err := e.d.Pool.Exec(t.Context(), `INSERT INTO account_links (org_id, public_id, user_id, messenger,
				connection_id, external_id, created_at)
				SELECT u.org_id, $1, u.id, $3, c.id, $5, '2026-10-05' FROM users u, connections c
				WHERE u.login = $2 AND c.public_id = $4`, publicID, login, messenger, conn, external)
			return err
		}
		request := func(t *testing.T, messenger, conn, target string, hash byte) error {
			t.Helper()
			n++
			var tgt any
			if target != "" {
				tgt = target
			}
			_, err := e.d.Pool.Exec(t.Context(), `INSERT INTO account_link_requests (org_id, public_id, user_id,
				messenger, connection_id, secret_hash, target_external_id, created_at, expires_at)
				SELECT u.org_id, $1, u.id, $2, c.id, $3, $4, '2026-10-05', '2026-10-05 00:10'
				FROM users u, connections c WHERE u.login = 'bob' AND c.public_id = $5`,
				fmt.Sprintf("AR%012d", n), messenger, bytes.Repeat([]byte{hash}, 32), tgt, conn)
			return err
		}
		refusedBy := func(t *testing.T, name string, err error, constraint string) {
			t.Helper()
			if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.ConstraintName != constraint {
				t.Errorf("%s: refused by %v, want %s", name, err, constraint)
			}
		}
		transports := func(t *testing.T) string {
			t.Helper()
			return e.constraints(t, []string{"timeline_entries", "timeline_entries_p202610"},
				"timeline_entries_transport") + " " + e.constraints(t, []string{"audit_log", "audit_log_p202610"},
				"audit_log_transport")
		}
		transportChecks := func(version string, valid bool) string {
			return fmt.Sprintf("timeline_entries.timeline_entries_transport_%[1]s_check:%[2]v "+
				"timeline_entries_p202610.timeline_entries_transport_%[1]s_check:%[2]v "+
				"audit_log.audit_log_transport_%[1]s_check:%[2]v "+
				"audit_log_p202610.audit_log_transport_%[1]s_check:%[2]v", version, valid)
		}
		messengers := func(t *testing.T) string {
			t.Helper()
			return strings.TrimSpace(e.constraints(t, []string{"account_links"}, "account_links_messenger") + " " +
				e.constraints(t, []string{"account_link_requests"}, "account_link_requests_messenger") + " " +
				e.constraints(t, []string{"account_link_requests"}, "account_link_requests_slack"))
		}
		const oldMessengers = "account_links.account_links_messenger_check:true " +
			"account_link_requests.account_link_requests_messenger_check:true"
		const newMessengers = oldMessengers + " account_link_requests.account_link_requests_slack_target_check:true"
		index := func(t *testing.T) int {
			t.Helper()
			return e.count(t, `SELECT count(*) FROM pg_indexes WHERE tablename = 'account_link_requests'
				AND indexname = 'account_link_requests_slack_target_idx'`)
		}
		slackRows := func(t *testing.T) int {
			t.Helper()
			return e.count(t, `SELECT count(*) FROM account_links WHERE messenger = 'slack'`) +
				e.count(t, `SELECT count(*) FROM account_link_requests WHERE messenger = 'slack'`)
		}

		if err := transport(t, "bridge"); err != nil {
			t.Fatal(err)
		}
		if err := link(t, "AK0000000000M1", "bob", "mattermost", "CN0000000000M1", "u-bob"); err != nil {
			t.Fatal(err)
		}
		if err := request(t, "mattermost", "CN0000000000M1", "u-vera", 1); err != nil {
			t.Fatal(err)
		}
		if got := transports(t); got != transportChecks("v2", true) {
			t.Fatalf("before 0018: %s", got)
		}
		if got := messengers(t); got != oldMessengers || index(t) != 0 {
			t.Fatalf("before 0018: %s, %d indexes", got, index(t))
		}
		if err := transport(t, "slack"); err == nil {
			t.Error("before 0018: the Transport slack was accepted")
		}
		refusedBy(t, "before 0018, a Slack link", link(t, "AK0000000000S0", "bob", "slack", "CN0000000000S1",
			"U0FAKEBOB"), "account_links_messenger_check")
		refusedBy(t, "before 0018, a Slack request", request(t, "slack", "CN0000000000S1", "U0FAKEBOB", 2),
			"account_link_requests_messenger_check")

		e.step(t, 1)
		if got := transports(t); got != transportChecks("v3", true) {
			t.Errorf("after up: %s", got)
		}
		if got := messengers(t); got != newMessengers || index(t) != 1 {
			t.Errorf("after up: %s, %d indexes", got, index(t))
		}
		if err := link(t, "AK0000000000S1", "bob", "slack", "CN0000000000S1", "U0FAKEBOB"); err != nil {
			t.Fatalf("after up, a Slack link: %v", err)
		}
		if e.count(t, `SELECT count(*) FROM account_links al JOIN connections c ON c.id = al.connection_id
			WHERE al.public_id = 'AK0000000000S1' AND al.identity_space = 'slack:' || c.id::text`) != 1 {
			t.Error("after up: the Slack link's space is not slack:<connection id>")
		}
		refusedBy(t, "after up, a second link of the account", link(t, "AK0000000000S2", "vera", "slack",
			"CN0000000000S1", "U0FAKEBOB"), "account_links_account_key")
		if err := link(t, "AK0000000000S3", "vera", "slack", "CN0000000000M1", "U0FAKEVERA"); err == nil {
			t.Error("after up: a Slack link through a Mattermost Connection was accepted")
		}
		if err := request(t, "slack", "CN0000000000S1", "U0FAKEBOB", 3); err != nil {
			t.Errorf("after up, a Slack request: %v", err)
		}
		refusedBy(t, "after up, a Slack request without a target", request(t, "slack", "CN0000000000S1", "", 4),
			"account_link_requests_slack_target_check")
		if err := request(t, "slack", "CN0000000000M1", "U0FAKEBOB", 5); err == nil {
			t.Error("after up: a Slack request through a Mattermost Connection was accepted")
		}
		if err := request(t, "mattermost", "CN0000000000M1", "u-bob", 6); err != nil {
			t.Errorf("after up, a Mattermost request: %v", err)
		}
		if err := transport(t, "pigeon"); err == nil {
			t.Error("after up: an unknown Transport was accepted")
		}

		e.step(t, -1)
		if got := transports(t); got != transportChecks("v2", true) {
			t.Errorf("after down without slack rows: %s", got)
		}
		if got := messengers(t); got != oldMessengers || index(t) != 0 {
			t.Errorf("after down: %s, %d indexes", got, index(t))
		}
		if got := slackRows(t); got != 0 {
			t.Errorf("after down: %d Slack links and requests", got)
		}
		if got := e.count(t, `SELECT count(*) FROM account_links`) +
			e.count(t, `SELECT count(*) FROM account_link_requests`); got != 3 {
			t.Errorf("after down: %d links and requests, want the 3 of Mattermost", got)
		}
		refusedBy(t, "after down, a Slack link", link(t, "AK0000000000S4", "vera", "slack", "CN0000000000S1",
			"U0FAKEVERA"), "account_links_messenger_check")

		e.step(t, 1)
		if err := transport(t, "slack"); err != nil {
			t.Fatalf("after up: %v", err)
		}
		if err := link(t, "AK0000000000S5", "vera", "slack", "CN0000000000S1", "U0FAKEVERA"); err != nil {
			t.Fatalf("after up: %v", err)
		}
		e.step(t, -1)
		if got := transports(t); got != transportChecks("v2", false) {
			t.Errorf("after down with slack rows: %s", got)
		}
		if got := e.count(t, `SELECT count(*) FROM timeline_entries WHERE transport = 'slack'`) +
			e.count(t, `SELECT count(*) FROM audit_log WHERE transport = 'slack'`); got != 2 {
			t.Errorf("after down: %d rows with the Transport slack, want the 2 of history", got)
		}
		if got := slackRows(t); got != 0 {
			t.Errorf("after down: %d Slack links and requests", got)
		}
		if err := transport(t, "slack"); err == nil {
			t.Error("after down: the Transport slack was accepted")
		}
		if err := transport(t, "bridge"); err != nil {
			t.Errorf("after down: %v", err)
		}

		e.step(t, 1)
		if got := transports(t); got != transportChecks("v3", true) {
			t.Errorf("after up again: %s", got)
		}
		if got := messengers(t); got != newMessengers || index(t) != 1 {
			t.Errorf("after up again: %s, %d indexes", got, index(t))
		}
		if err := transport(t, "slack"); err != nil {
			t.Errorf("after up again: %v", err)
		}
		if v, dirty, err := e.mm.Version(); err != nil || dirty || v != 18 {
			t.Errorf("version %d, dirty %v, %v", v, dirty, err)
		}
	})
}

// TestIntegrationRequestLogSocketEnvelopes runs 0019 over a Request log with a Slack Connection's records: before it an
// inbound record whose answer has a body but no status — the confirmation of a Socket Mode envelope — is refused by
// request_log_answer_check; up replaces the CHECK under its name, validated, so that such an inbound record is
// accepted while an outbound one is still refused, and an answer with a status still needs its headers and body, and
// headers still need a status. Down deletes the envelope records, keeping the others, and restores the old CHECK,
// validated, which refuses them again; up again accepts them.
func TestIntegrationRequestLogSocketEnvelopes(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newMigrationEnv(t, s, 18)
		e.exec(t, `INSERT INTO connections (org_id, public_id, type, name, slack_event_mode,
			slack_app_token_ciphertext, slack_app_token_key_id, slack_app_token_updated_at, bot_token_ciphertext,
			bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at)
			SELECT id, 'CN0000000000S1', 'slack', 'sl', 'socket', '\x01', 'k1', '2026-10-05', '\x02', 'k1',
				'2026-10-05', 1, 1, '2026-10-05', '2026-10-05'
			FROM organizations`)
		n := 0
		record := func(t *testing.T, set map[string]string) error {
			t.Helper()
			n++
			cols := map[string]string{
				"public_id": fmt.Sprintf("'RQ%012d'", n), "at": "'2026-10-05 12:00:00+00'", "direction": "'inbound'",
				"operation": "'interactive'", "method": "'WEBSOCKET'", "url": "'wss://wss-primary.slack.com/link/'",
				"request_headers": "'{}'", "request_body": `'{"envelope_id":"e1","type":"interactive"}'`,
				"request_body_bytes": "42", "request_body_truncated": "false", "status": "NULL",
				"response_headers": "NULL", "response_body": `'{"envelope_id":"e1"}'`, "response_body_bytes": "20",
				"response_body_truncated": "false", "latency_ms": "1", "via": "'direct'", "attempt": "1",
				"connection_id": "(SELECT id FROM connections WHERE public_id = 'CN0000000000S1')",
			}
			maps.Copy(cols, set)
			names, values := make([]string, 0, len(cols)), make([]string, 0, len(cols))
			for k, v := range cols {
				names, values = append(names, k), append(values, v)
			}
			_, err := e.d.Pool.Exec(t.Context(), `INSERT INTO request_log (org_id, `+strings.Join(names, ", ")+`)
				SELECT id, `+strings.Join(values, ", ")+` FROM organizations`)
			return err
		}
		refused := func(t *testing.T, name string, err error) {
			t.Helper()
			if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.ConstraintName != "request_log_answer_check" {
				t.Errorf("%s: refused by %v, want request_log_answer_check", name, err)
			}
		}
		answered := map[string]string{"direction": "'outbound'", "operation": "'apps.connections.open'",
			"method": "'POST'", "url": "'https://slack.com/api/apps.connections.open'", "status": "200",
			"response_headers": "'{}'", "response_body": `'{"ok":true}'`}
		noAnswer := map[string]string{"direction": "'outbound'", "operation": "'apps.connections.open'",
			"method": "'POST'", "response_body": "NULL", "response_body_bytes": "0", "error_class": "'transient'",
			"error_code": "'connect_failed'", "error": "'connection refused'"}
		with := func(base map[string]string, kv ...string) map[string]string {
			out := maps.Clone(base)
			for i := 0; i+1 < len(kv); i += 2 {
				out[kv[i]] = kv[i+1]
			}
			return out
		}
		check := func(t *testing.T) string {
			t.Helper()
			return e.constraints(t, []string{"request_log"}, "request_log_answer")
		}
		const valid = "request_log.request_log_answer_check:true"
		envelopes := func(t *testing.T) int {
			t.Helper()
			return e.count(t, `SELECT count(*) FROM request_log WHERE status IS NULL AND response_body IS NOT NULL`)
		}
		for _, set := range []map[string]string{answered, noAnswer, with(answered, "direction", "'inbound'")} {
			if err := record(t, set); err != nil {
				t.Fatalf("before 0019: %v", err)
			}
		}
		if got := check(t); got != valid {
			t.Fatalf("before 0019: %s", got)
		}
		refused(t, "before 0019, an envelope", record(t, nil))

		e.step(t, 1)
		if got := check(t); got != valid {
			t.Errorf("after up: %s", got)
		}
		if err := record(t, nil); err != nil {
			t.Errorf("after up, an envelope: %v", err)
		}
		if err := record(t, map[string]string{"operation": "'events_api'", "via": "'proxy'"}); err != nil {
			t.Errorf("after up, an envelope through a proxy: %v", err)
		}
		if err := record(t, with(answered, "direction", "'inbound'")); err != nil {
			t.Errorf("after up, an answered inbound record: %v", err)
		}
		if err := record(t, noAnswer); err != nil {
			t.Errorf("after up, an outbound record without an answer: %v", err)
		}
		for _, tt := range []struct {
			name string
			set  map[string]string
		}{
			{"an outbound answer body without a status", with(noAnswer, "response_body", `'{"ok":true}'`,
				"error_code", "NULL")},
			{"a status without an answer body", with(answered, "response_body", "NULL")},
			{"a status without answer headers", with(answered, "response_headers", "NULL")},
			{"answer headers without a status", map[string]string{"response_headers": "'{}'"}},
			{"inbound answer headers without a status", map[string]string{"response_body": "NULL",
				"response_headers": "'{}'"}},
		} {
			refused(t, "after up, "+tt.name, record(t, tt.set))
		}
		if got := envelopes(t); got != 2 {
			t.Fatalf("after up: %d envelope records, want 2", got)
		}

		e.step(t, -1)
		if got := check(t); got != valid {
			t.Errorf("after down: %s", got)
		}
		if got := envelopes(t); got != 0 {
			t.Errorf("after down: %d envelope records", got)
		}
		if got := e.count(t, `SELECT count(*) FROM request_log`); got != 5 {
			t.Errorf("after down: %d records, want the 5 with a status or without an answer", got)
		}
		refused(t, "after down, an envelope", record(t, nil))

		e.step(t, 1)
		if got := check(t); got != valid {
			t.Errorf("after up again: %s", got)
		}
		if err := record(t, nil); err != nil {
			t.Errorf("after up again, an envelope: %v", err)
		}
		if v, dirty, err := e.mm.Version(); err != nil || dirty || v != 19 {
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
			!strings.Contains(err.Error(), "database schema version 0 is older than this binary needs (19)") {
			t.Errorf("CheckSchema before migrating: %v", err)
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		if !strings.Contains(out.String(), `"event":"migrations_applied","from":0,"to":19`) {
			t.Errorf("logged %s", out.String())
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil || !strings.Contains(out.String(), `"event":"migrations_current","version":19`) {
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
			if !errors.As(err, &se) || err.Error() != "database schema version 999 is newer than this binary knows (19)" {
				t.Errorf("%s on version 999: %v", name, err)
			}
			if !strings.Contains(out.String(), `"event":"schema_too_new","database_version":999,"known_version":19`) {
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
