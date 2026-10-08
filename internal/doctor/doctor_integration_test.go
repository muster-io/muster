// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package doctor

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/organization"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

// fingerprint is the content of every table and the list of every relation of the database.
func fingerprint(t *testing.T, d *db.DB) string {
	t.Helper()
	var out string
	err := d.Pool.QueryRow(t.Context(), `SELECT string_agg(c.relkind::text || ':' || c.relname, ',' ORDER BY c.relname)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public'`).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.Pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	for _, table := range tables {
		var content string
		if err := d.Pool.QueryRow(t.Context(), "SELECT coalesce(md5(string_agg(t::text, '|' ORDER BY t::text)), '') FROM "+
			table+" t").Scan(&content); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		out += "\n" + table + ":" + content
	}
	return out
}

// TestIntegrationDoctor: C-02.AC-13 against PostgreSQL without TLS with sslmode=prefer, the canary failure with
// another Keyring, and a run that changes nothing in the database.
func TestIntegrationDoctor(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		u := s.NewDatabase(t)
		conn := config.Database{URL: logging.Secret(u), SSLMode: "disable"}
		d, err := db.Open(t.Context(), conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := d.Migrate(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo)); err != nil {
			t.Fatal(err)
		}
		key, id := keys('a')
		k, err := keyring.Load(t.Context(), keyring.Env{Keys: logging.Secret(key), Source: keyring.SecretKeysVar}, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.Establish(t.Context(), keyring.NewStore(d.Pool), time.Now()); err != nil {
			t.Fatal(err)
		}
		before := fingerprint(t, d)

		prefer, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		q := prefer.Query()
		q.Set("sslmode", "prefer")
		prefer.RawQuery = q.Encode()
		env := []string{"MUSTER_DATABASE_URL=" + prefer.String(), "MUSTER_PUBLIC_URL=http://localhost:8080"}

		var out bytes.Buffer
		ok, err := Run(t.Context(), Options{Environ: append(env, "MUSTER_SECRET_KEYS="+key), Out: &out})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("muster doctor:\n%s", out.String())
		for _, want := range []string{
			"OK   database: main and session connections reachable\n",
			"OK   postgresql_version: " + s.Version + ".",
			"OK   session_connection: advisory locks and LISTEN work\n",
			"WARN tls_main: sslmode=prefer, the connection is not encrypted\n",
			"WARN tls_session: sslmode=prefer, the connection is not encrypted\n",
			"OK   key_canary: decrypts with key " + id + "\n",
			"OK   keyring: active key " + id + ", no older key in use\n",
			"OK   clock_skew: ",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q", want)
			}
		}
		if !ok || strings.Count(out.String(), "\n") != 8 {
			t.Errorf("ok %v with %d lines", ok, strings.Count(out.String(), "\n"))
		}

		other, _ := keys('b')
		out.Reset()
		ok, err = Run(t.Context(), Options{Environ: append(env, "MUSTER_SECRET_KEYS="+other), Out: &out})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("muster doctor with another key:\n%s", out.String())
		if ok || !strings.Contains(out.String(), "FAIL key_canary: master key does not match the database\n") {
			t.Errorf("ok %v, output:\n%s", ok, out.String())
		}

		if after := fingerprint(t, d); after != before {
			t.Errorf("the doctor changed the database:\nbefore %s\nafter  %s", before, after)
		}

		// The Mattermost Connections and Destinations of the Organization, one line each, still without a write.
		st, err := keyring.NewStore(d.Pool).GetKeyringState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := k.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelError), keyring.State{
			ActiveKeyID: st.ActiveKeyID, CanaryKeyID: st.CanaryKeyID, Canary: st.CanaryCiphertext}); err != nil {
			t.Fatal(err)
		}
		fake := fakemattermost.New()
		if err := fake.Start(t.Context(), "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fake.Close(context.WithoutCancel(t.Context())) }()
		now := time.Now()
		if err := organization.Ensure(t.Context(), organization.NewStore(d.Pool),
			logging.New(&bytes.Buffer{}, logging.LevelInfo), now); err != nil {
			t.Fatal(err)
		}
		ciphertext, keyID, err := k.Encrypt("connections.bot_token", []byte("doctor-integration-token"))
		if err != nil {
			t.Fatal(err)
		}
		exec := func(stmt string, args ...any) {
			t.Helper()
			if _, err := d.Pool.Exec(t.Context(), stmt, args...); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		exec(`UPDATE outbound_policies SET allowed = '{127.0.0.0/8}'`)
		exec(`INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url, bot_token_ciphertext,
			bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at)
			SELECT id, 'CNAAAAAAAAAAA1', 'mattermost', 'Dev Mattermost', $1, $2, $3, $4, 5, 1, $4, $4
			FROM organizations`, fake.URL(), ciphertext, keyID, now)
		for _, ds := range [][3]string{{"DSAAAAAAAAAAA1", "alerts", fakemattermost.ChannelAlerts},
			{"DSAAAAAAAAAAA2", "no-bot", fakemattermost.ChannelNoBot}} {
			exec(`INSERT INTO destinations (org_id, public_id, type, name, connection_id, mattermost_team_id,
				mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at)
				SELECT org_id, $1, 'mattermost', $2, id, $3, $4, '{}', 5, 1, 'healthy', $5, $5 FROM connections`,
				ds[0], ds[1], fakemattermost.TeamID, ds[2], now)
		}
		before = fingerprint(t, d)
		out.Reset()
		ok, err = Run(t.Context(), Options{Environ: append(env, "MUSTER_SECRET_KEYS="+key), Out: &out})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("muster doctor with a Mattermost Connection:\n%s", out.String())
		want := "WARN connection Dev Mattermost: " + mattermost.HintPressAnswersInThread + "\n" +
			"OK   destination alerts: ok\n" +
			"FAIL destination no-bot: The bot is not a member of this channel.\n"
		if ok || !strings.HasSuffix(out.String(), want) || strings.Count(out.String(), "\n") != 11 {
			t.Errorf("ok %v, output:\n%s", ok, out.String())
		}
		if after := fingerprint(t, d); after != before {
			t.Errorf("the doctor changed the database:\nbefore %s\nafter  %s", before, after)
		}

		// Both connections of the doctor refuse writes.
		cfg, err := config.Load(append(env, "MUSTER_SECRET_KEYS="+key))
		if err != nil {
			t.Fatal(err)
		}
		conns, err := Open(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer conns.Close()
		for _, c := range []Conn{conns.Main, conns.Session} {
			_, err := c.Exec(t.Context(), "DELETE FROM replicas")
			if err == nil || !strings.Contains(err.Error(), "read-only transaction") {
				t.Errorf("a write on a doctor connection: %v", err)
			}
		}
	})
}
