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

func TestIntegrationMigrate(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := openFresh(t, s)
		var out bytes.Buffer
		log := logging.New(&out, logging.LevelInfo)
		if err := d.CheckSchema(t.Context(), log); err == nil ||
			!strings.Contains(err.Error(), "database schema version 0 is older than this binary needs (13)") {
			t.Errorf("CheckSchema before migrating: %v", err)
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		if !strings.Contains(out.String(), `"event":"migrations_applied","from":0,"to":13`) {
			t.Errorf("logged %s", out.String())
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil || !strings.Contains(out.String(), `"event":"migrations_current","version":13`) {
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
			if !errors.As(err, &se) || err.Error() != "database schema version 999 is newer than this binary knows (13)" {
				t.Errorf("%s on version 999: %v", name, err)
			}
			if !strings.Contains(out.String(), `"event":"schema_too_new","database_version":999,"known_version":13`) {
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
