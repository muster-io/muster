// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package keyring

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/keyring/dbgen"
)

// sqlRecorder is a dbgen.DBTX that records the statements it runs; a query fails after it is recorded.
type sqlRecorder struct {
	dbgen.DBTX
	sql []string
}

func (r *sqlRecorder) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.sql = append(r.sql, sql)
	return pgconn.CommandTag{}, nil
}

func (r *sqlRecorder) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	r.sql = append(r.sql, sql)
	return nil, errors.New("recorded")
}

// TestLockRows: every Connection and every outgoing webhook, and no messenger Destination, is locked in id order by
// its query; another table is refused. The users of the offline tokens are locked in id order too.
func TestLockRows(t *testing.T) {
	rec := &sqlRecorder{}
	q := pgRotationQueries{Queries: dbgen.New(rec)}
	for table, where := range map[string]string{
		"connections":  "-- name: LockRotateConnections :exec\nSELECT id\nFROM connections\nWHERE org_id = $1\n",
		"destinations": "-- name: LockRotateDestinations :exec\nSELECT id\nFROM destinations\nWHERE org_id = $1 AND type = 'webhook'\n",
	} {
		rec.sql = nil
		if err := q.LockRows(t.Context(), table, 1); err != nil || len(rec.sql) != 1 ||
			rec.sql[0] != where+"ORDER BY id\nFOR NO KEY UPDATE\n" {
			t.Errorf("%s: %v, %q", table, err, rec.sql)
		}
		if !lockedInOrder[table] {
			t.Errorf("%s is not in lockedInOrder", table)
		}
	}
	rec.sql = nil
	if err := q.LockRows(t.Context(), "users", 1); err == nil || len(rec.sql) != 0 {
		t.Errorf("users: %v, %q", err, rec.sql)
	}
	if len(lockedInOrder) != 2 {
		t.Errorf("lockedInOrder %v", lockedInOrder)
	}

	rec.sql = nil
	if _, err := q.EncryptedValues(t.Context(), "users.oidc_offline_token", 1, "k"); err == nil || len(rec.sql) != 1 ||
		!strings.HasSuffix(rec.sql[0], "\nORDER BY id\nFOR NO KEY UPDATE\n") {
		t.Errorf("offline tokens: %v, %q", err, rec.sql)
	}
}
