// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package accountlinks_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// TestLiveLookup reads Account links from PostgreSQL: a Mattermost account in the identity space of its Connection,
// not in another one; a disabled User with its status; a deleted User and an unknown account are not linked.
func TestLiveLookup(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
		d, err := db.Open(ctx, conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		if err := d.Migrate(ctx, logger); err != nil {
			t.Fatal(err)
		}
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
			t.Fatal(err)
		}
		org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var connID int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at) VALUES ($1, 'CNAAAAAAAAAAA1', 'mattermost', 'mm', 'https://mm.example.org', '\x00',
			'k1', $2, 10, 1, $2, $2) RETURNING id`, org.ID, t0).Scan(&connID); err != nil {
			t.Fatal(err)
		}
		users := map[string]int64{}
		for i, u := range []struct{ login, status string }{{"bob", "active"}, {"carol", "disabled"},
			{"dave", "deleted"}} {
			var deleted *time.Time
			if u.status == "deleted" {
				deleted = &t0
			}
			var id int64
			if err := d.Pool.QueryRow(ctx, `INSERT INTO users (org_id, public_id, login, name, role, source, status,
				deleted_at, created_at, updated_at) VALUES ($1, $2, $3, $3, 'responder', 'local', $4, $5, $6, $6)
				RETURNING id`, org.ID, "SRAAAAAAAAAAB"+string(rune('1'+i)), u.login, u.status, deleted,
				t0).Scan(&id); err != nil {
				t.Fatal(err)
			}
			users[u.login] = id
			if _, err := d.Pool.Exec(ctx, `INSERT INTO account_links (org_id, public_id, user_id, messenger,
				connection_id, external_id, username, created_at) VALUES ($1, $2, $3, 'mattermost', $4, $5, $6, $7)`,
				org.ID, "AKAAAAAAAAAAB"+string(rune('1'+i)), id, connID, "u-"+u.login, u.login, t0); err != nil {
				t.Fatal(err)
			}
		}
		links := accountlinks.New(org.ID, d.Pool)
		space := accountlinks.SpaceMattermost(connID)
		u, err := links.Lookup(ctx, space, "u-bob")
		if err != nil || u.ID != users["bob"] || u.PublicID != "SRAAAAAAAAAAB1" || u.Login != "bob" ||
			u.Role != "responder" || u.Status != accountlinks.StatusActive {
			t.Fatalf("Lookup(bob) = %+v, %v", u, err)
		}
		if u, err := links.Lookup(ctx, space, "u-carol"); err != nil || u.Status != accountlinks.StatusDisabled {
			t.Errorf("Lookup(carol) = %+v, %v", u, err)
		}
		for _, c := range [][2]string{{space, "u-dave"}, {space, "u-erin"}, {accountlinks.SpaceMattermost(connID + 1),
			"u-bob"}, {accountlinks.SpaceTelegram, "u-bob"}} {
			if _, err := links.Lookup(ctx, c[0], c[1]); !errors.Is(err, accountlinks.ErrNotLinked) {
				t.Errorf("Lookup(%s, %s) = %v, want ErrNotLinked", c[0], c[1], err)
			}
		}
		if _, err := accountlinks.New(org.ID+1, d.Pool).Lookup(ctx, space, "u-bob"); !errors.Is(err,
			accountlinks.ErrNotLinked) {
			t.Errorf("another Organization = %v", err)
		}
	})
}
