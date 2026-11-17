// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/logging"
)

const twoTransactions = "-- header\nBEGIN;\nCREATE TABLE a (id int);\nCOMMIT;\n\nbegin;\nALTER TABLE b ADD c int;\ncommit;\n"

type execResult struct {
	commits int
	err     error
}

// TestDriverRetries runs a migration whose attempts end as results, with a session that records the ROLLBACKs, and
// checks the SQL of every attempt, the waits between them and the retries logged.
func TestDriverRetries(t *testing.T) {
	deadlock := &pgconn.PgError{Severity: "ERROR", Code: "40P01", Message: "deadlock detected"}
	lockTimeout := &pgconn.PgError{Severity: "ERROR", Code: "55P03", Message: "canceling statement due to lock timeout"}
	rest := "\nbegin;\nALTER TABLE b ADD c int;\ncommit;\n"
	tests := []struct {
		name        string
		results     []execResult
		rollbackErr error
		waitFails   bool
		wantErr     string
		wantSQL     []string
		wantWaits   []time.Duration
		wantRetries []string
	}{
		{name: "first attempt", results: []execResult{{commits: 2}}, wantSQL: []string{twoTransactions}},
		{
			name: "deadlock in the first transaction", results: []execResult{{0, deadlock}, {2, nil}},
			wantSQL: []string{twoTransactions, twoTransactions}, wantWaits: []time.Duration{time.Second},
			wantRetries: []string{`"version":7,"attempt":1,"sqlstate":"40P01"`},
		},
		{
			name: "lock timeout in the second transaction", results: []execResult{{1, lockTimeout}, {1, nil}},
			wantSQL: []string{twoTransactions, rest}, wantWaits: []time.Duration{time.Second},
			wantRetries: []string{`"version":7,"attempt":1,"sqlstate":"55P03"`},
		},
		{
			name:    "three attempts",
			results: []execResult{{1, lockTimeout}, {0, deadlock}, {0, lockTimeout}},
			wantErr: "run the migration: ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)",
			wantSQL: []string{twoTransactions, rest, rest}, wantWaits: []time.Duration{time.Second, 2 * time.Second},
			wantRetries: []string{`"attempt":1,"sqlstate":"55P03"`, `"attempt":2,"sqlstate":"40P01"`},
		},
		{
			name:    "another error",
			results: []execResult{{0, &pgconn.PgError{Severity: "ERROR", Code: "42P07", Message: "relation \"a\" already exists"}}},
			wantErr: "run the migration: ERROR: relation \"a\" already exists (SQLSTATE 42P07)",
			wantSQL: []string{twoTransactions},
		},
		{
			name: "an error that is not PostgreSQL's", results: []execResult{{0, errors.New("conn closed")}},
			wantErr: "run the migration: conn closed", wantSQL: []string{twoTransactions},
		},
		{
			name: "commits unknown", results: []execResult{{-1, deadlock}},
			wantErr: "run the migration: ERROR: deadlock detected (SQLSTATE 40P01)", wantSQL: []string{twoTransactions},
		},
		{
			name: "more commits than the file shows", results: []execResult{{3, deadlock}},
			wantErr: "run the migration: ERROR: deadlock detected (SQLSTATE 40P01)", wantSQL: []string{twoTransactions},
		},
		{
			name: "rollback fails", results: []execResult{{0, deadlock}}, rollbackErr: errors.New("conn closed"),
			wantErr: "run the migration: ERROR: deadlock detected (SQLSTATE 40P01)\nconn closed",
			wantSQL: []string{twoTransactions},
		},
		{
			name: "cancelled while waiting", results: []execResult{{0, deadlock}}, waitFails: true,
			wantErr: "run the migration: ERROR: deadlock detected (SQLSTATE 40P01)\ncontext canceled",
			wantSQL: []string{twoTransactions}, wantWaits: []time.Duration{time.Second},
			wantRetries: []string{`"attempt":1,"sqlstate":"40P01"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := &stubConn{execErrs: map[string]error{}}
			if tt.rollbackErr != nil {
				c.execErrs["ROLLBACK"] = tt.rollbackErr
			}
			var (
				out   bytes.Buffer
				sqls  []string
				waits []time.Duration
			)
			d := &driver{ctx: ctx, conn: c, log: logging.New(&out, logging.LevelInfo), version: 7}
			d.exec = func(_ context.Context, sql string) (int, error) {
				r := tt.results[len(sqls)]
				sqls = append(sqls, sql)
				return r.commits, r.err
			}
			d.wait = func(_ context.Context, w time.Duration) bool {
				waits = append(waits, w)
				if tt.waitFails {
					cancel()
				}
				return !tt.waitFails
			}
			err := d.Run(strings.NewReader(twoTransactions))
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Errorf("Run = %v, want %q", err, tt.wantErr)
			}
			if fmt.Sprint(sqls) != fmt.Sprint(tt.wantSQL) {
				t.Errorf("ran %q, want %q", sqls, tt.wantSQL)
			}
			if fmt.Sprint(waits) != fmt.Sprint(tt.wantWaits) {
				t.Errorf("waited %v, want %v", waits, tt.wantWaits)
			}
			if n := strings.Count(out.String(), `"level":"WARN","event":"migration_retried"`); n != len(tt.wantRetries) {
				t.Errorf("logged %d retries, want %d:\n%s", n, len(tt.wantRetries), out.String())
			}
			for _, want := range tt.wantRetries {
				if !strings.Contains(out.String(), want) {
					t.Errorf("logged %s, want %s", out.String(), want)
				}
			}
			rollbacks := strings.Count(strings.Join(c.execs, "|"), "ROLLBACK")
			if tt.rollbackErr == nil && rollbacks != len(tt.wantRetries) {
				t.Errorf("%d ROLLBACKs, want one per retry", rollbacks)
			}
		})
	}
}

func TestAfterCommits(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		n    int
		want string
		ok   bool
	}{
		{name: "unknown", sql: twoTransactions, n: -1},
		{name: "nothing committed", sql: twoTransactions, n: 0, want: twoTransactions, ok: true},
		{name: "first committed", sql: twoTransactions, n: 1, want: "\nbegin;\nALTER TABLE b ADD c int;\ncommit;\n", ok: true},
		{name: "both committed", sql: twoTransactions, n: 2, want: "", ok: true},
		{name: "more than the file has", sql: twoTransactions, n: 3},
		{name: "without transactions", sql: "CREATE TABLE a (id int);\n", n: 1},
		{name: "COMMIT without BEGIN", sql: "CREATE TABLE a (id int);\nCOMMIT;\n", n: 1},
		{name: "BEGIN twice", sql: "BEGIN;\nBEGIN;\nCOMMIT;\n", n: 1},
		{name: "indented, no final newline", sql: "  BEGIN;\n  SELECT 1;\n  COMMIT;  \nSELECT 2;", n: 1, want: "SELECT 2;", ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := afterCommits(tt.sql, tt.n)
			if got != tt.want || ok != tt.ok {
				t.Errorf("afterCommits(%d) = %q, %v; want %q, %v", tt.n, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestMigrationTransactions: in every embedded migration, each transaction control statement is a line BEGIN; or
// COMMIT; of its own and those lines alternate, one transaction each, so that a retry after a deadlock or a lock
// timeout finds where every committed transaction ends.
func TestMigrationTransactions(t *testing.T) {
	entries, err := fs.ReadDir(migrationFiles, migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(migrationFiles, migrationsDir+"/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		sql := string(b)
		if err := checkTransactionLines(sql); err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		begins, commits := 0, 0
		for line := range strings.Lines(sql) {
			switch strings.ToUpper(strings.TrimSpace(line)) {
			case "BEGIN;":
				begins++
			case "COMMIT;":
				commits++
			}
		}
		if begins != commits {
			t.Errorf("%s has %d lines BEGIN; and %d lines COMMIT;", e.Name(), begins, commits)
			continue
		}
		for n := range commits + 1 {
			if _, ok := afterCommits(sql, n); !ok {
				t.Errorf("%s: the end of transaction %d is not found", e.Name(), n)
			}
		}
	}
}

func TestCheckTransactionLines(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		ok   bool
	}{
		{name: "two transactions", sql: twoTransactions, ok: true},
		{name: "a function body", ok: true, sql: "BEGIN;\nCREATE FUNCTION f() RETURNS trigger\n  LANGUAGE plpgsql AS $$\n" +
			"BEGIN\n  RETURN NEW;\nEND;\n$$;\nCOMMIT;\n"},
		{name: "a tagged function body", ok: true, sql: "DO $body$ BEGIN PERFORM 1; END; $body$;\n"},
		{name: "CASE … END", ok: true, sql: "UPDATE t SET a = CASE WHEN b THEN 1 ELSE 2 END;\n"},
		{name: "a quoted semicolon", ok: true, sql: "BEGIN;\nSELECT 'x; COMMIT;', \"y;\";\nCOMMIT;\n"},
		{name: "two on one line", sql: "BEGIN;\nA;\nCOMMIT; BEGIN;\nB;\nCOMMIT;\n"},
		{name: "in a line comment, which afterCommits skips too", sql: "BEGIN;\n-- COMMIT;\nCOMMIT;\n", ok: true},
		{name: "in a block comment", sql: "/*\nBEGIN;\n*/\n"},
		{name: "in a function body", sql: "DO $$\nCOMMIT;\n$$;\n"},
		{name: "in a string", sql: "SELECT '\nCOMMIT;\n';\n"},
		{name: "END", sql: "BEGIN;\nEND;\n"},
		{name: "ROLLBACK", sql: "BEGIN;\nROLLBACK;\n"},
		{name: "START TRANSACTION", sql: "START TRANSACTION;\nCOMMIT;\n"},
		{name: "BEGIN with more words", sql: "BEGIN ISOLATION LEVEL SERIALIZABLE;\nCOMMIT;\n"},
		{name: "after a statement", sql: "SELECT 1; COMMIT;\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkTransactionLines(tt.sql); (err == nil) != tt.ok {
				t.Errorf("checkTransactionLines = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

// transactionControl are the first words of the statements that begin or end a transaction or a part of one.
var transactionControl = []string{"BEGIN", "COMMIT", "END", "ROLLBACK", "ABORT", "START", "SAVEPOINT", "RELEASE"}

// checkTransactionLines fails when a transaction control statement of sql, outside comments and quotes, is anything
// but a line BEGIN; or COMMIT; of its own, or when such a line is inside a comment or a quote: afterCommits reads those
// lines alone.
func checkTransactionLines(sql string) error {
	code := codeOnly(sql)
	raw, clean := strings.Split(sql, "\n"), strings.Split(code, "\n")
	isTx := func(line string) bool {
		l := strings.ToUpper(strings.TrimSpace(line))
		return l == "BEGIN;" || l == "COMMIT;"
	}
	for i := range raw {
		if isTx(raw[i]) != isTx(clean[i]) {
			return fmt.Errorf("line %d, %q, is inside a comment or a quote", i+1, raw[i])
		}
	}
	start := 0
	for i := 0; i <= len(code); i++ {
		if i < len(code) && code[i] != ';' {
			continue
		}
		stmt := code[start:i]
		words := strings.Fields(strings.ToUpper(stmt))
		if len(words) > 0 && slices.Contains(transactionControl, words[0]) {
			first := start + len(stmt) - len(strings.TrimLeftFunc(stmt, unicode.IsSpace))
			line := strings.Count(code[:first], "\n")
			if len(words) != 1 || !isTx(clean[line]) {
				return fmt.Errorf("line %d, %q, is not a line BEGIN; or COMMIT; of its own", line+1, raw[line])
			}
		}
		start = i + 1
	}
	return nil
}

var dollarTag = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z_0-9]*)?\$`)

// codeOnly is sql with its comments, quoted strings and identifiers and dollar-quoted bodies blanked, keeping every
// newline, so that its lines and offsets are those of sql.
func codeOnly(sql string) string {
	b := []byte(sql)
	blank := func(from, to int) int {
		to = min(to, len(b))
		for i := from; i < to; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
		return to
	}
	// until is the offset just past the first close at or after from, or the end of sql.
	until := func(from int, closing string) int {
		if end := strings.Index(sql[from:], closing); end >= 0 {
			return from + end + len(closing)
		}
		return len(sql)
	}
	for i := 0; i < len(b); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				end = len(sql) - i
			}
			i = blank(i, i+end)
		case strings.HasPrefix(sql[i:], "/*"):
			i = blank(i, until(i+2, "*/"))
		case sql[i] == '\'' || sql[i] == '"':
			i = blank(i, until(i+1, sql[i:i+1]))
		case dollarTag.MatchString(sql[i:]):
			tag := dollarTag.FindString(sql[i:])
			i = blank(i, until(i+len(tag), tag))
		default:
			i++
		}
	}
	return string(b)
}
