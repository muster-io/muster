// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package db

import (
	"bytes"
	"context"
	"errors"
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

func TestIntegrationMigrate(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := openFresh(t, s)
		var out bytes.Buffer
		log := logging.New(&out, logging.LevelInfo)
		if err := d.CheckSchema(t.Context(), log); err == nil ||
			!strings.Contains(err.Error(), "database schema version 0 is older than this binary needs (6)") {
			t.Errorf("CheckSchema before migrating: %v", err)
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		if !strings.Contains(out.String(), `"event":"migrations_applied","from":0,"to":6`) {
			t.Errorf("logged %s", out.String())
		}
		out.Reset()
		if err := d.Migrate(t.Context(), log); err != nil || !strings.Contains(out.String(), `"event":"migrations_current","version":6`) {
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
			if !errors.As(err, &se) || err.Error() != "database schema version 999 is newer than this binary knows (6)" {
				t.Errorf("%s on version 999: %v", name, err)
			}
			if !strings.Contains(out.String(), `"event":"schema_too_new","database_version":999,"known_version":6`) {
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
