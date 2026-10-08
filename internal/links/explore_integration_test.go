// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package links

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

// TestIntegrationEnsureExplore: on PostgreSQL the ensure step creates the built-in rule, gives one that still has the
// previous built-in template the current one as a new version, once, keeps an edited template, and stops on a rule
// that is not the built-in one and has its name.
func TestIntegrationEnsureExplore(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
		d, err := db.Open(ctx, conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		if err := d.Migrate(ctx, log); err != nil {
			t.Fatal(err)
		}
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), log, t0); err != nil {
			t.Fatal(err)
		}
		org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		store := NewStore(d.Pool)
		read := func() (publicID, tmpl string, version int64) {
			t.Helper()
			if err := d.Pool.QueryRow(ctx, `SELECT public_id, url_template, version FROM link_rules
				WHERE org_id = $1 AND builtin`, org.ID).Scan(&publicID, &tmpl, &version); err != nil {
				t.Fatal(err)
			}
			return publicID, tmpl, version
		}
		set := func(tmpl string) {
			t.Helper()
			if _, err := d.Pool.Exec(ctx, `UPDATE link_rules SET url_template = $2 WHERE org_id = $1 AND builtin`,
				org.ID, tmpl); err != nil {
				t.Fatal(err)
			}
		}

		if _, err := d.Pool.Exec(ctx, `INSERT INTO link_rules (org_id, public_id, name, scope_type, url_template,
			created_at, updated_at) VALUES ($1, 'KRAAAAAAAAAAAA', $2, 'alert_group', 'https://x', $3, $3)`,
			org.ID, ExploreName, t0); err != nil {
			t.Fatal(err)
		}
		if err := EnsureExplore(ctx, store, org.ID, t0); !errors.Is(err, ErrRuleNameTaken) {
			t.Fatalf("a rule of its name: %v", err)
		}
		if _, err := d.Pool.Exec(ctx, `DELETE FROM link_rules WHERE org_id = $1`, org.ID); err != nil {
			t.Fatal(err)
		}

		if err := EnsureExplore(ctx, store, org.ID, t0); err != nil {
			t.Fatal(err)
		}
		id, tmpl, version := read()
		if tmpl != ExploreTemplate || version != 1 {
			t.Fatalf("created %s version %d", tmpl, version)
		}
		set(previousExploreTemplates[0])
		for range 2 {
			if err := EnsureExplore(ctx, store, org.ID, t0); err != nil {
				t.Fatal(err)
			}
		}
		if gotID, tmpl, version := read(); gotID != id || tmpl != ExploreTemplate || version != 2 {
			t.Fatalf("upgraded %s %s version %d", gotID, tmpl, version)
		}
		set(previousExploreTemplates[0] + " ")
		if err := EnsureExplore(ctx, store, org.ID, t0); err != nil {
			t.Fatal(err)
		}
		if _, tmpl, version := read(); tmpl != previousExploreTemplates[0]+" " || version != 2 {
			t.Errorf("an edited template changed: version %d", version)
		}
	})
}
