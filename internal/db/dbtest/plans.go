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
	most := 0
	for _, p := range loggedPlans(t, connString, []string{"SET auto_explain.log_analyze = on", "SET enable_hashjoin = off",
		"SET enable_mergejoin = off"}, f) {
		most = max(most, lockRowsLoops(t, p))
	}
	return most
}

// UpdateScans runs f on a connection to the database at connString that plans every prepared statement as mode, a
// plan_cache_mode, says and sends the plan of every statement it executes back as a notice (auto_explain); for every
// UPDATE among them it returns how its plan reads the table it updates, each scan of that table under the alias of the
// UPDATE as EXPLAIN names it: "Seq Scan on deliveries d" or "Index Scan using deliveries_pkey on deliveries d".
func UpdateScans(t testing.TB, connString, mode string, f func(ctx context.Context, conn *pgx.Conn)) []string {
	t.Helper()
	var scans []string
	for _, p := range loggedPlans(t, connString, []string{"SET plan_cache_mode = " + mode}, f) {
		var top planNode
		if err := json.Unmarshal(p, &top); err != nil {
			t.Fatalf("plan %s: %v", p, err)
		}
		if top.Type == "ModifyTable" && top.Operation == "Update" {
			scans = append(scans, top.scansOf(top.Relation, top.Alias)...)
		}
	}
	return scans
}

// RowsRead runs f on a connection to the database at connString that plans every prepared statement as mode, a
// plan_cache_mode, says and sends the executed plan of every statement back as a notice (auto_explain with
// log_analyze); it returns how many rows of relation the scans under alias read in all those plans together, every
// loop counted: those they returned and those their filters removed. A claim whose plan cannot see its limit and
// misjudges its filters reads every due row to choose a batch.
func RowsRead(t testing.TB, connString, mode, relation, alias string,
	f func(ctx context.Context, conn *pgx.Conn)) float64 {
	t.Helper()
	read := 0.0
	for _, p := range loggedPlans(t, connString, []string{"SET plan_cache_mode = " + mode,
		"SET auto_explain.log_analyze = on", "SET auto_explain.log_timing = off"}, f) {
		read += rowsRead(t, p, relation, alias)
	}
	return read
}

// rowsRead is the rows the scans of relation under alias at the plan node raw and below it read, every loop counted.
func rowsRead(t testing.TB, raw json.RawMessage, relation, alias string) float64 {
	var node struct {
		Relation string            `json:"Relation Name"`
		Alias    string            `json:"Alias"`
		Rows     float64           `json:"Actual Rows"`
		Removed  float64           `json:"Rows Removed by Filter"`
		Loops    float64           `json:"Actual Loops"`
		Plans    []json.RawMessage `json:"Plans"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		t.Fatalf("plan node %s: %v", raw, err)
	}
	read := 0.0
	if node.Relation == relation && node.Alias == alias {
		read = (node.Rows + node.Removed) * node.Loops
	}
	for _, child := range node.Plans {
		read += rowsRead(t, child, relation, alias)
	}
	return read
}

// planNode is a node of an EXPLAIN plan in JSON.
type planNode struct {
	Type      string     `json:"Node Type"`
	Operation string     `json:"Operation"`
	Relation  string     `json:"Relation Name"`
	Alias     string     `json:"Alias"`
	Index     string     `json:"Index Name"`
	Plans     []planNode `json:"Plans"`
}

// scansOf names every scan of relation under alias at n and below it.
func (n planNode) scansOf(relation, alias string) []string {
	var out []string
	if n.Type != "ModifyTable" && n.Relation == relation && n.Alias == alias {
		s := n.Type
		if n.Index != "" {
			s += " using " + n.Index
		}
		out = append(out, s+" on "+relation+" "+alias)
	}
	for _, c := range n.Plans {
		out = append(out, c.scansOf(relation, alias)...)
	}
	return out
}

// loggedPlans runs f on a connection to the database at connString, after settings, that sends the plan of every
// statement it executes back as a notice (auto_explain, in JSON); it returns the top node of each plan.
func loggedPlans(t testing.TB, connString string, settings []string,
	f func(ctx context.Context, conn *pgx.Conn)) []json.RawMessage {
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
	for _, stmt := range append([]string{"LOAD 'auto_explain'", "SET auto_explain.log_min_duration = 0",
		"SET auto_explain.log_format = 'json'", "SET auto_explain.log_level = 'notice'",
		"SET client_min_messages = 'notice'"}, settings...) {
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
	out := make([]json.RawMessage, 0, len(plans))
	for _, p := range plans {
		var doc struct {
			Plan json.RawMessage `json:"Plan"`
		}
		if err := json.Unmarshal([]byte(p), &doc); err != nil {
			t.Fatalf("plan %s: %v", p, err)
		}
		out = append(out, doc.Plan)
	}
	return out
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
