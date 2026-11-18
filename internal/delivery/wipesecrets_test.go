// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/delivery/dbgen"
)

// execRecorder is a dbgen.DBTX that records the statements it executes.
type execRecorder struct {
	dbgen.DBTX
	sql []string
}

func (r *execRecorder) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.sql = append(r.sql, sql)
	return pgconn.CommandTag{}, nil
}

// TestWipeDestinationSecretsLocksInIDOrder: the wipe of the secrets of deleted Destinations, for every one of the
// Organization as the retention of outgoing webhook events runs it, locks the Destinations it changes in id order in
// the CTE that picks them, before it changes any, and takes no other lock after it (#616).
func TestWipeDestinationSecretsLocksInIDOrder(t *testing.T) {
	rec := &execRecorder{}
	if err := dbgen.New(rec).WipeDestinationSecrets(t.Context(),
		dbgen.WipeDestinationSecretsParams{OrgID: 1}); err != nil || len(rec.sql) != 1 {
		t.Fatalf("%v, %q", err, rec.sql)
	}
	sql := rec.sql[0]
	gone, rest, ok := strings.Cut(strings.Join(strings.Fields(sql), " "), " ), secrets AS (")
	if !ok || !strings.HasPrefix(gone, "-- name: WipeDestinationSecrets :exec WITH gone AS (") ||
		!strings.HasSuffix(gone, " ORDER BY x.id FOR NO KEY UPDATE") {
		t.Errorf("the destinations are not locked in id order where they are picked:\n%s", sql)
	}
	if strings.Contains(rest, "FOR ") {
		t.Errorf("another lock after the pick:\n%s", sql)
	}
}
