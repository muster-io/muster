// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"testing/iotest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

type stubRow struct {
	values []any
	err    error
}

func (r stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = r.values[i].(string)
		case *bool:
			*p = r.values[i].(bool)
		case *int64:
			*p = r.values[i].(int64)
		default:
			return errors.New("stubRow: unsupported destination")
		}
	}
	return nil
}

// stubConn answers by the start of the SQL; a notification is delivered once notified.
type stubConn struct {
	rows     map[string]stubRow
	execErrs map[string]error
	notify   chan *pgconn.Notification
	waitErr  error
	execs    []string
	closed   bool
}

func (c *stubConn) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.execs = append(c.execs, sql)
	for prefix, err := range c.execErrs {
		if strings.HasPrefix(sql, prefix) {
			return pgconn.CommandTag{}, err
		}
	}
	if strings.HasPrefix(sql, "SELECT pg_notify") && c.notify != nil {
		c.notify <- &pgconn.Notification{Channel: "other", Payload: "x"}
		c.notify <- &pgconn.Notification{Channel: args[0].(string), Payload: args[1].(string)}
	}
	return pgconn.CommandTag{}, nil
}

func (c *stubConn) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	for prefix, row := range c.rows {
		if strings.HasPrefix(sql, prefix) {
			return row
		}
	}
	return stubRow{err: errors.New("stubConn: unexpected query " + sql)}
}

func (c *stubConn) Close(context.Context) error {
	c.closed = true
	return nil
}

func (c *stubConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	if c.waitErr != nil {
		return nil, c.waitErr
	}
	select {
	case n := <-c.notify:
		return n, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func healthySession() *stubConn {
	return &stubConn{
		rows:   map[string]stubRow{"SELECT pg_advisory_unlock": {values: []any{true}}},
		notify: make(chan *pgconn.Notification, 2),
	}
}

func TestCheckVersion(t *testing.T) {
	tests := []struct {
		version string
		err     error
		want    string
	}{
		{version: "140011"},
		{version: "170006"},
		{version: "130020", want: "PostgreSQL 13.x is not supported: Muster needs PostgreSQL 14 or newer"},
		{version: "90624", want: "PostgreSQL 9.x is not supported: Muster needs PostgreSQL 14 or newer"},
		{version: "fourteen", want: `read the PostgreSQL version: server_version_num is "fourteen"`},
		{err: errors.New("boom"), want: "read the PostgreSQL version: boom"},
	}
	for _, tt := range tests {
		q := &stubConn{rows: map[string]stubRow{"SHOW server_version_num": {values: []any{tt.version}, err: tt.err}}}
		err := CheckVersion(t.Context(), q)
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || err.Error() != tt.want) {
			t.Errorf("version %q: got %v, want %q", tt.version, err, tt.want)
		}
	}
}

func TestCheckSession(t *testing.T) {
	session := healthySession()
	if err := CheckSession(t.Context(), session, session); err != nil {
		t.Fatalf("a healthy session: %v", err)
	}
	want := []string{"SELECT pg_advisory_lock($1)", "LISTEN muster_session_check", "SELECT pg_notify($1, $2)",
		"UNLISTEN muster_session_check"}
	if strings.Join(session.execs, "|") != strings.Join(want, "|") {
		t.Errorf("statements %q, want %q", session.execs, want)
	}
}

func TestCheckSessionPooler(t *testing.T) {
	sessionCheckTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sessionCheckTimeout = 5 * time.Second })
	tests := []struct {
		name   string
		mutate func(s, n *stubConn)
		want   string
	}{
		{
			name:   "advisory locks refused",
			mutate: func(s, _ *stubConn) { s.execErrs = map[string]error{"SELECT pg_advisory_lock": errors.New("refused")} },
			want:   "take an advisory lock: refused",
		},
		{
			name:   "unlock fails",
			mutate: func(s, _ *stubConn) { s.rows["SELECT pg_advisory_unlock"] = stubRow{err: errors.New("gone")} },
			want:   "release the advisory lock: gone",
		},
		{
			name:   "lock not held",
			mutate: func(s, _ *stubConn) { s.rows["SELECT pg_advisory_unlock"] = stubRow{values: []any{false}} },
			want:   "was not held by its session",
		},
		{
			name:   "LISTEN refused",
			mutate: func(s, _ *stubConn) { s.execErrs = map[string]error{"LISTEN": errors.New("not supported")} },
			want:   "LISTEN: not supported",
		},
		{
			name:   "NOTIFY fails",
			mutate: func(_, n *stubConn) { n.execErrs = map[string]error{"SELECT pg_notify": errors.New("closed")} },
			want:   "NOTIFY: closed",
		},
		{
			name:   "no notification",
			mutate: func(_, n *stubConn) { n.notify = nil },
			want:   "no notification arrived within 50ms of NOTIFY",
		},
		{
			name:   "wait fails",
			mutate: func(s, _ *stubConn) { s.waitErr = errors.New("conn closed") },
			want:   "wait for the notification: conn closed",
		},
		{
			name:   "UNLISTEN fails",
			mutate: func(s, _ *stubConn) { s.execErrs = map[string]error{"UNLISTEN": errors.New("busy")} },
			want:   "UNLISTEN: busy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := healthySession()
			notifier := &stubConn{notify: session.notify}
			tt.mutate(session, notifier)
			err := CheckSession(t.Context(), session, notifier)
			var pe *PoolerError
			if !errors.As(err, &pe) || !strings.Contains(pe.Err.Error(), tt.want) {
				t.Fatalf("got %v, want a PoolerError with %q", err, tt.want)
			}
			for _, name := range []string{"MUSTER_DATABASE_SESSION_URL", "MUSTER_DATABASE_SESSION_HOST",
				"MUSTER_DATABASE_SESSION_PORT", "transaction pooler", "does not keep session state"} {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("the error %q does not name %s", err, name)
				}
			}
			if !errors.Is(errors.Unwrap(err), pe.Err) {
				t.Error("Unwrap does not return the cause")
			}
		})
	}
}

func TestEncryptedAndReport(t *testing.T) {
	tests := []struct {
		row       stubRow
		want      bool
		wantLevel string
		wantErr   bool
	}{
		{row: stubRow{values: []any{true}}, want: true, wantLevel: "INFO"},
		{row: stubRow{values: []any{false}}, want: false, wantLevel: "WARN"},
		{row: stubRow{err: pgx.ErrNoRows}, want: false, wantLevel: "WARN"},
		{row: stubRow{err: errors.New("denied")}, wantErr: true},
	}
	for _, tt := range tests {
		q := &stubConn{rows: map[string]stubRow{"SELECT ssl FROM pg_stat_ssl": tt.row}}
		got, err := Encrypted(t.Context(), q)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Encrypted = %v, %v; want %v (error %v)", got, err, tt.want, tt.wantErr)
		}
		if tt.wantErr {
			continue
		}
		var out bytes.Buffer
		ReportSecurity(t.Context(), logging.New(&out, logging.LevelInfo), ConnectionSession, "prefer", got)
		var line map[string]any
		if err := json.Unmarshal(out.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if line["event"] != "database_connection_security" || line["level"] != tt.wantLevel ||
			line["connection"] != "session" || line["sslmode"] != "prefer" || line["encrypted"] != got {
			t.Errorf("line %v", line)
		}
	}
}

func TestCheckSchema(t *testing.T) {
	tests := []struct {
		name      string
		version   uint
		dirty     bool
		migrating bool
		want      string
		wantEvent string
	}{
		{name: "current", version: 1},
		{name: "older while migrating", version: 0, migrating: true},
		{
			name: "newer", version: 999, migrating: true, wantEvent: "schema_too_new",
			want: "database schema version 999 is newer than this binary knows (1)",
		},
		{
			name: "older without migrating", version: 0, wantEvent: "schema_too_old",
			want: "database schema version 0 is older than this binary needs (1): run muster migrate or set MUSTER_MIGRATE_ON_START=true",
		},
		{
			name: "dirty", version: 1, dirty: true, migrating: true, wantEvent: "schema_dirty",
			want: "the database schema is dirty at version 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := checkSchema(t.Context(), logging.New(&out, logging.LevelInfo), tt.version, tt.dirty, 1, tt.migrating)
			if tt.want == "" {
				if err != nil || out.Len() > 0 {
					t.Errorf("got %v, logged %q", err, out.String())
				}
				return
			}
			var se *SchemaError
			if !errors.As(err, &se) || !strings.HasPrefix(err.Error(), tt.want) || se.Database != tt.version || se.Known != 1 {
				t.Fatalf("got %v, want a SchemaError %q", err, tt.want)
			}
			var line map[string]any
			if err := json.Unmarshal(out.Bytes(), &line); err != nil {
				t.Fatal(err)
			}
			if line["event"] != tt.wantEvent || line["level"] != "ERROR" || line["database_version"] != float64(tt.version) {
				t.Errorf("logged %v", line)
			}
		})
	}
}

func TestKnownVersion(t *testing.T) {
	v, err := KnownVersion()
	if err != nil || v != 6 {
		t.Errorf("KnownVersion = %d, %v; want 6", v, err)
	}
	fsys := fstest.MapFS{
		"m/0001_init.up.sql":   {Data: []byte("SELECT 1;")},
		"m/0001_init.down.sql": {Data: []byte("SELECT 1;")},
		"m/0002_more.up.sql":   {Data: []byte("SELECT 1;")},
		"m/0010_last.up.sql":   {Data: []byte("SELECT 1;")},
	}
	if v, err := newestVersion(fsys, "m"); err != nil || v != 10 {
		t.Errorf("newestVersion = %d, %v; want 10", v, err)
	}
	if _, err := newestVersion(fstest.MapFS{"m/README": {}}, "m"); err == nil {
		t.Error("a directory without migrations has a version")
	}
	if _, err := newestVersion(fsys, "missing"); err == nil {
		t.Error("a missing directory has a version")
	}
}

// unreachable is a database address where nothing listens.
var unreachable = config.Database{URL: "postgres://muster:pw@127.0.0.1:1/muster?sslmode=disable&connect_timeout=1",
	SSLMode: "disable"}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(t.Context(), config.Database{URL: "postgres://h:notaport/db"}, unreachable); err == nil ||
		!strings.Contains(err.Error(), "the main database connection") {
		t.Errorf("a bad main URL: %v", err)
	}
	if _, err := Open(t.Context(), unreachable, config.Database{URL: "postgres://h:notaport/db"}); err == nil ||
		!strings.Contains(err.Error(), "the session database connection") {
		t.Errorf("a bad session URL: %v", err)
	}
}

// TestPoolSize: the main pool opens max(MinPoolConns, CPUs) connections unless its URL sets pool_max_conns.
func TestPoolSize(t *testing.T) {
	if defaultMaxConns(4) != 10 || defaultMaxConns(16) != 16 || defaultMaxConns(0) != 10 {
		t.Errorf("defaults %d %d %d", defaultMaxConns(4), defaultMaxConns(16), defaultMaxConns(0))
	}
	for url, want := range map[string]int32{
		"postgres://muster@127.0.0.1:5432/muster?sslmode=disable":                  defaultMaxConns(runtime.NumCPU()),
		"postgres://muster@127.0.0.1:5432/muster?sslmode=disable&pool_max_conns=3": 3,
	} {
		d, err := Open(t.Context(), config.Database{URL: logging.Secret(url)}, unreachable)
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Pool.Config().MaxConns; got != want {
			t.Errorf("%s: %d connections, want %d", url, got, want)
		}
		d.Close()
	}
}

// TestOnlyFromURL sets the PG* variables pgx reads: none of them may change the connections.
func TestOnlyFromURL(t *testing.T) {
	t.Setenv("PGPASSWORD", "from-the-environment")
	t.Setenv("PGCONNECT_TIMEOUT", "99")
	t.Setenv("PGAPPNAME", "other")
	t.Setenv("PGPASSFILE", "/nonexistent")
	noPassword := config.Database{URL: "postgres://muster@127.0.0.1:5432/muster?sslmode=disable"}
	d, err := Open(t.Context(), noPassword, config.Database{
		URL: "postgres://muster:pw@127.0.0.1:5432/muster?sslmode=disable&connect_timeout=3&application_name=muster",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	main := d.Pool.Config().ConnConfig
	if main.Password != "" || main.ConnectTimeout != connectTimeout || main.RuntimeParams["application_name"] != "" {
		t.Errorf("main connection: password %q, connect timeout %v, application name %q", main.Password,
			main.ConnectTimeout, main.RuntimeParams["application_name"])
	}
	if s := d.sessionConfig; s.Password != "pw" || s.ConnectTimeout != 3*time.Second ||
		s.RuntimeParams["application_name"] != "muster" {
		t.Errorf("session connection: password %q, connect timeout %v, application name %q", s.Password,
			s.ConnectTimeout, s.RuntimeParams["application_name"])
	}
	c := d.sessionConfig.Copy()
	onlyFromURL(c, "postgres://%zz")
	if c.Password != "pw" {
		t.Error("an unparsable URL changed the settings")
	}
}

func TestUnreachable(t *testing.T) {
	d, err := Open(t.Context(), unreachable, unreachable)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	log := logging.New(io.Discard, logging.LevelInfo)
	checks := []struct {
		name string
		err  error
		want string
	}{
		{"Ping", d.Ping(t.Context()), "the database does not answer"},
		{"Check", d.Check(t.Context(), log), "connect to the database (main connection)"},
		{"Migrate", d.Migrate(t.Context(), log), "connect to the database (session connection)"},
		{"CheckSchema", d.CheckSchema(t.Context(), log), "read the schema version"},
	}
	for _, c := range checks {
		if c.err == nil || !strings.HasPrefix(c.err.Error(), c.want) {
			t.Errorf("%s = %v, want %q", c.name, c.err, c.want)
		} else if strings.Contains(c.err.Error(), ":pw@") {
			t.Errorf("%s returned the password: %v", c.name, c.err)
		}
	}
}

func TestPoolMetrics(t *testing.T) {
	d, err := Open(t.Context(), unreachable, unreachable)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.RegisterMetrics()
	d.RegisterMetrics()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`muster_db_pool_connections{state="acquired"} 0`,
		`muster_db_pool_connections{state="idle"} 0`,
		`muster_db_pool_connections{state="constructing"} 0`,
		`muster_db_pool_acquires_total 0`,
		`muster_db_pool_acquire_wait_seconds_total 0`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("the exposition lacks %s", want)
		}
	}
	if !strings.Contains(body, "muster_db_pool_max_connections ") {
		t.Error("the exposition lacks muster_db_pool_max_connections")
	}
	metricsPool.Store(nil)
	rec = httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "muster_db_pool_max_connections 0\n") {
		t.Error("without a pool the metrics are not 0")
	}
}

// fakeDB is a DB over stubs: a healthy PostgreSQL 17 without TLS, with the schema at version 1.
func fakeDB() (*DB, *stubConn, *stubConn) {
	pool := healthySession()
	pool.rows["SHOW server_version_num"] = stubRow{values: []any{"170006"}}
	pool.rows["SELECT ssl FROM pg_stat_ssl"] = stubRow{values: []any{true}}
	pool.rows["SELECT to_regclass"] = stubRow{values: []any{true}}
	pool.rows["SELECT version, dirty"] = stubRow{values: []any{int64(1), false}}
	session := healthySession()
	session.notify = pool.notify
	session.rows["SHOW server_version_num"] = stubRow{values: []any{"170006"}}
	session.rows["SELECT ssl FROM pg_stat_ssl"] = stubRow{values: []any{false}}
	session.rows["SELECT pg_advisory_unlock"] = stubRow{values: []any{true}}
	d := &DB{
		main:    config.Database{SSLMode: "require"},
		session: config.Database{SSLMode: "prefer"},
		pool:    pool,
		connect: func(context.Context) (conn, error) { return session, nil },
	}
	return d, pool, session
}

func decode(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.Lines(out.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestCheck(t *testing.T) {
	d, _, session := fakeDB()
	var out bytes.Buffer
	if err := d.Check(t.Context(), logging.New(&out, logging.LevelInfo)); err != nil {
		t.Fatal(err)
	}
	if !session.closed {
		t.Error("the session connection was not closed")
	}
	lines := decode(t, &out)
	if len(lines) != 2 {
		t.Fatalf("logged %s", out.String())
	}
	if lines[0]["connection"] != "main" || lines[0]["level"] != "INFO" || lines[0]["sslmode"] != "require" ||
		lines[1]["connection"] != "session" || lines[1]["level"] != "WARN" || lines[1]["sslmode"] != "prefer" {
		t.Errorf("logged %v", lines)
	}
}

func TestCheckFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(d *DB, pool, session *stubConn)
		want   string
	}{
		{
			name: "old main",
			mutate: func(_ *DB, pool, _ *stubConn) {
				pool.rows["SHOW server_version_num"] = stubRow{values: []any{"130004"}}
			},
			want: "PostgreSQL 13.x is not supported",
		},
		{
			name: "no session",
			mutate: func(d *DB, _, _ *stubConn) {
				d.connect = func(context.Context) (conn, error) {
					return nil, errors.New("connect to the database (session connection): refused")
				}
			},
			want: "connect to the database (session connection): refused",
		},
		{
			name: "old session",
			mutate: func(_ *DB, _, session *stubConn) {
				session.rows["SHOW server_version_num"] = stubRow{values: []any{"120004"}}
			},
			want: "PostgreSQL 12.x is not supported",
		},
		{
			name: "pooler",
			mutate: func(_ *DB, _, session *stubConn) {
				session.execErrs = map[string]error{"LISTEN": errors.New("unsupported")}
			},
			want: "the session connection does not keep session state",
		},
		{
			name: "TLS state",
			mutate: func(_ *DB, pool, _ *stubConn) {
				pool.rows["SELECT ssl FROM pg_stat_ssl"] = stubRow{err: errors.New("denied")}
			},
			want: "read the TLS state of the main database connection: denied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, pool, session := fakeDB()
			tt.mutate(d, pool, session)
			err := d.Check(t.Context(), logging.New(io.Discard, logging.LevelInfo))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSchemaVersion(t *testing.T) {
	tests := []struct {
		name      string
		rows      map[string]stubRow
		want      uint
		wantDirty bool
		wantErr   string
	}{
		{name: "current", rows: map[string]stubRow{}, want: 1},
		{name: "no table", rows: map[string]stubRow{"SELECT to_regclass": {values: []any{false}}}},
		{name: "empty table", rows: map[string]stubRow{"SELECT version, dirty": {err: pgx.ErrNoRows}}},
		{
			name: "dirty", rows: map[string]stubRow{"SELECT version, dirty": {values: []any{int64(2), true}}},
			want: 2, wantDirty: true,
		},
		{name: "nil version", rows: map[string]stubRow{"SELECT version, dirty": {values: []any{int64(-1), false}}}},
		{
			name: "table error", rows: map[string]stubRow{"SELECT to_regclass": {err: errors.New("x")}},
			wantErr: "read the schema version: x",
		},
		{
			name: "row error", rows: map[string]stubRow{"SELECT version, dirty": {err: errors.New("y")}},
			wantErr: "read the schema version: y",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, pool, _ := fakeDB()
			for k, v := range tt.rows {
				pool.rows[k] = v
			}
			v, dirty, err := schemaVersion(t.Context(), d.pool)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("got %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || v != tt.want || dirty != tt.wantDirty {
				t.Errorf("got %d, %v, %v; want %d, %v", v, dirty, err, tt.want, tt.wantDirty)
			}
		})
	}
	d, pool, _ := fakeDB()
	pool.rows["SELECT version, dirty"] = stubRow{values: []any{int64(7), false}}
	var out bytes.Buffer
	err := d.CheckSchema(t.Context(), logging.New(&out, logging.LevelInfo))
	if err == nil || !strings.Contains(err.Error(), "database schema version 7 is newer than this binary knows (6)") ||
		!strings.Contains(out.String(), `"event":"schema_too_new"`) {
		t.Errorf("CheckSchema on version 7: %v, logged %s", err, out.String())
	}
}

type fakeMigrator struct {
	versions []uint // the version before and after Up; 0 is no version
	dirty    bool
	upErr    error
	verErr   error
	calls    int
	ups      int
	closed   bool
}

func (m *fakeMigrator) Version() (uint, bool, error) {
	defer func() { m.calls++ }()
	if m.verErr != nil {
		return 0, false, m.verErr
	}
	v := m.versions[min(m.calls, len(m.versions)-1)]
	if v == 0 {
		return 0, false, migrate.ErrNilVersion
	}
	return v, m.dirty, nil
}

func (m *fakeMigrator) Up() error {
	m.ups++
	return m.upErr
}

func (m *fakeMigrator) Close() (error, error) {
	m.closed = true
	return nil, nil
}

func TestMigrate(t *testing.T) {
	tests := []struct {
		name     string
		m        *fakeMigrator
		wantErr  string
		wantLine string
		wantUps  int
	}{
		{name: "new database", m: &fakeMigrator{versions: []uint{0, 2}}, wantUps: 1,
			wantLine: `"event":"migrations_applied","from":0,"to":2`},
		{name: "current", m: &fakeMigrator{versions: []uint{2, 2}, upErr: migrate.ErrNoChange}, wantUps: 1,
			wantLine: `"event":"migrations_current","version":2`},
		{name: "newer", m: &fakeMigrator{versions: []uint{999}},
			wantErr: "database schema version 999 is newer than this binary knows (6)", wantLine: `"event":"schema_too_new"`},
		{name: "dirty", m: &fakeMigrator{versions: []uint{1}, dirty: true},
			wantErr: "the database schema is dirty at version 1", wantLine: `"event":"schema_dirty"`},
		{name: "up fails", m: &fakeMigrator{versions: []uint{0}, upErr: errors.New("syntax error")}, wantUps: 1,
			wantErr: "apply the migrations from version 0: syntax error"},
		{name: "version fails", m: &fakeMigrator{verErr: errors.New("gone")}, wantErr: "read the schema version: gone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _, session := fakeDB()
			d.newMigrator = func(context.Context, conn) (migrator, error) { return tt.m, nil }
			var out bytes.Buffer
			err := d.Migrate(t.Context(), logging.New(&out, logging.LevelInfo))
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.wantErr)) {
				t.Errorf("got %v, want %q", err, tt.wantErr)
			}
			if !strings.Contains(out.String(), tt.wantLine) {
				t.Errorf("logged %s, want %s", out.String(), tt.wantLine)
			}
			if tt.m.ups != tt.wantUps || !tt.m.closed || !session.closed {
				t.Errorf("ups %d, migrator closed %v, session closed %v", tt.m.ups, tt.m.closed, session.closed)
			}
			want := []string{"SELECT pg_advisory_lock($1)", "SELECT pg_advisory_unlock($1)"}
			if strings.Join(session.execs, "|") != strings.Join(want, "|") {
				t.Errorf("statements %q, want the lock and its release", session.execs)
			}
		})
	}
}

func TestMigrationLock(t *testing.T) {
	d, _, session := fakeDB()
	session.execErrs = map[string]error{"SELECT pg_advisory_lock": errors.New("timeout")}
	ran := false
	err := d.WithMigrationLock(t.Context(), func(context.Context) error { ran = true; return nil })
	if err == nil || err.Error() != "take the migration lock: timeout" || ran {
		t.Errorf("a lock that fails: %v, ran %v", err, ran)
	}

	d, _, session = fakeDB()
	session.execErrs = map[string]error{"SELECT pg_advisory_unlock": errors.New("closed")}
	if err := d.WithMigrationLock(t.Context(), func(context.Context) error { return nil }); err == nil ||
		err.Error() != "release the migration lock: closed" {
		t.Errorf("an unlock that fails: %v", err)
	}
	failed := errors.New("failed inside")
	if err := d.WithMigrationLock(t.Context(), func(context.Context) error { return failed }); !errors.Is(err, failed) {
		t.Errorf("the error of f is lost: %v", err)
	}

	d.newMigrator = func(context.Context, conn) (migrator, error) { return nil, errors.New("prepare the migrations: no") }
	if err := d.Migrate(t.Context(), logging.New(io.Discard, logging.LevelInfo)); err == nil ||
		err.Error() != "prepare the migrations: no" {
		t.Errorf("a migrator that fails: %v", err)
	}
}

func TestDriver(t *testing.T) {
	c := &stubConn{rows: map[string]stubRow{"SELECT version, dirty": {err: pgx.ErrNoRows}}}
	m, err := newMigrate(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if v, dirty, err := version(m); v != 0 || dirty || err != nil {
		t.Errorf("version of an empty database: %d, %v, %v", v, dirty, err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	_, _ = m.Close()
	if len(c.execs) != 19 || !strings.HasPrefix(c.execs[0], "CREATE TABLE IF NOT EXISTS schema_migrations") ||
		c.execs[1] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (1, true)" ||
		!strings.Contains(c.execs[2], "CREATE EXTENSION IF NOT EXISTS pg_trgm") ||
		c.execs[3] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (1, false)" ||
		c.execs[4] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (2, true)" ||
		!strings.Contains(c.execs[5], "ADD COLUMN replayed_at") ||
		c.execs[6] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (2, false)" ||
		c.execs[7] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (3, true)" ||
		!strings.Contains(c.execs[8], "ON DELETE SET NULL") ||
		c.execs[9] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (3, false)" ||
		c.execs[10] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (4, true)" ||
		!strings.Contains(c.execs[11], "CREATE INDEX thread_replies_pending_idx") ||
		c.execs[12] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (4, false)" ||
		c.execs[13] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (5, true)" ||
		!strings.Contains(c.execs[14], "ALTER TABLE webhook_events ADD COLUMN received_at") ||
		c.execs[15] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (5, false)" ||
		c.execs[16] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (6, true)" ||
		!strings.Contains(c.execs[17], "UPDATE deliveries SET published_at = NULL") ||
		c.execs[18] != "TRUNCATE schema_migrations; INSERT INTO schema_migrations (version, dirty) VALUES (6, false)" {
		t.Errorf("statements %q", c.execs)
	}

	d := &driver{ctx: t.Context(), conn: c}
	c.execs = nil
	if err := d.SetVersion(-1, false); err != nil || c.execs[0] != "TRUNCATE schema_migrations" {
		t.Errorf("SetVersion(nil) = %v, %q", err, c.execs)
	}
	c.rows["SELECT version, dirty"] = stubRow{values: []any{int64(3), true}}
	if v, dirty, err := d.Version(); v != 3 || !dirty || err != nil {
		t.Errorf("Version = %d, %v, %v", v, dirty, err)
	}
	c.rows["SELECT version, dirty"] = stubRow{err: errors.New("gone")}
	if _, _, err := d.Version(); err == nil || err.Error() != "read the schema version: gone" {
		t.Errorf("Version = %v", err)
	}
	if _, err := d.Open("postgres://x"); err == nil {
		t.Error("Open succeeded")
	}
	if d.Drop() == nil || d.Lock() != nil || d.Unlock() != nil || d.Close() != nil {
		t.Error("Drop, Lock, Unlock or Close")
	}
	c.execErrs = map[string]error{"TRUNCATE": errors.New("denied"), "SELECT 1": errors.New("syntax")}
	if err := d.SetVersion(2, false); err == nil || err.Error() != "record the schema version 2: denied" {
		t.Errorf("SetVersion = %v", err)
	}
	if err := d.Run(strings.NewReader("SELECT 1")); err == nil || err.Error() != "run the migration: syntax" {
		t.Errorf("Run = %v", err)
	}
	if err := d.Run(iotest.ErrReader(errors.New("read"))); err == nil {
		t.Error("Run of an unreadable migration succeeded")
	}
	c.execErrs = map[string]error{"CREATE TABLE": errors.New("permission denied")}
	if _, err := newMigrate(t.Context(), c); err == nil ||
		err.Error() != "create the schema_migrations table: permission denied" {
		t.Errorf("newMigrate = %v", err)
	}
}
