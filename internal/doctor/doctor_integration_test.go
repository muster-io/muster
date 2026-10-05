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
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
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
