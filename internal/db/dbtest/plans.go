// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package dbtest

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// LockRowsLoops runs f on a connection to the database at connString that sends the executed plan of every statement
// back as a notice (auto_explain), with hash and merge joins off, so that a row choice joined to its own table is
// planned as a nested loop; it returns the most times any LockRows node ran in one statement. A claim whose
// LIMIT … FOR UPDATE SKIP LOCKED choice runs more than once per call rescans and relocks it for every row of the
// table it updates.
func LockRowsLoops(t testing.TB, connString string, f func(ctx context.Context, conn *pgx.Conn)) int {
	t.Helper()
	cfg, err := pgx.ParseConfig(connString)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var plans []string
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		if i := strings.IndexByte(n.Message, '{'); i >= 0 && strings.Contains(n.Message, "plan:") {
			mu.Lock()
			plans = append(plans, n.Message[i:])
			mu.Unlock()
		}
	}
	ctx := t.Context()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	for _, stmt := range []string{"LOAD 'auto_explain'", "SET auto_explain.log_min_duration = 0",
		"SET auto_explain.log_analyze = on", "SET auto_explain.log_format = 'json'",
		"SET auto_explain.log_level = 'notice'", "SET client_min_messages = 'notice'", "SET enable_hashjoin = off",
		"SET enable_mergejoin = off"} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	f(ctx, conn)
	mu.Lock()
	defer mu.Unlock()
	if len(plans) == 0 {
		t.Fatal("no plan was logged")
	}
	most := 0
	for _, p := range plans {
		var doc struct {
			Plan json.RawMessage `json:"Plan"`
		}
		if err := json.Unmarshal([]byte(p), &doc); err != nil {
			t.Fatalf("plan %s: %v", p, err)
		}
		most = max(most, lockRowsLoops(t, doc.Plan))
	}
	return most
}

// lockRowsLoops is the most loops of a LockRows node in the plan node raw and below it.
func lockRowsLoops(t testing.TB, raw json.RawMessage) int {
	var node struct {
		Type  string            `json:"Node Type"`
		Loops float64           `json:"Actual Loops"`
		Plans []json.RawMessage `json:"Plans"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatalf("plan node %s: %v", raw, err)
	}
	most := 0
	if node.Type == "LockRows" {
		most = int(node.Loops)
	}
	for _, child := range node.Plans {
		most = max(most, lockRowsLoops(t, child))
	}
	return most
}
