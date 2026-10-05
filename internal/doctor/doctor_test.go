// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package doctor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

// rows are query results; Scan assigns each value to its destination as it is.
type rows struct {
	values [][]any
	i      int
	err    error
}

func (r *rows) Close()                                       {}
func (r *rows) Err() error                                   { return nil }
func (r *rows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *rows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *rows) Values() ([]any, error)                       { return nil, nil }
func (r *rows) RawValues() [][]byte                          { return nil }
func (r *rows) Conn() *pgx.Conn                              { return nil }
func (r *rows) TypeMap() *pgtype.Map                         { return nil }

func (r *rows) Next() bool {
	r.i++
	return r.i <= len(r.values)
}

func (r *rows) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.i == 0 { // QueryRow
		if len(r.values) == 0 {
			return pgx.ErrNoRows
		}
		r.i = 1
	}
	for i, d := range dest {
		reflect.ValueOf(d).Elem().Set(reflect.ValueOf(r.values[r.i-1][i]))
	}
	return nil
}

// fakeDB answers the doctor's statements; each answer can be replaced or failed by the SQL it matches.
type fakeDB struct {
	answers map[string]*rows
	execs   []string
	listen  bool
}

// conn is one connection of the database; notify is set on the session connection.
type conn struct {
	db      *fakeDB
	name    string
	payload *string
}

func newFakeDB(ssl bool) *fakeDB {
	return &fakeDB{answers: map[string]*rows{
		"SHOW server_version_num": {values: [][]any{{"170011"}}},
		"SHOW server_version":     {values: [][]any{{"17.11 (Debian 17.11-1)"}}},
		"pg_stat_ssl":             {values: [][]any{{ssl}}},
		"pg_advisory_unlock":      {values: [][]any{{true}}},
		"clock_timestamp":         {values: [][]any{{time.Now()}}},
		"FROM organizations":      {values: [][]any{{int64(1)}}},
		"FROM encrypted_values":   {values: [][]any{}},
	}}
}

// answer is the answer whose key is the longest part of sql.
func (d *fakeDB) answer(sql string) *rows {
	var best string
	for match := range d.answers {
		if strings.Contains(sql, match) && len(match) > len(best) {
			best = match
		}
	}
	if r, ok := d.answers[best]; ok {
		return &rows{values: r.values, err: r.err}
	}
	return &rows{err: fmt.Errorf("unexpected statement %q", sql)}
}

func (c *conn) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.db.execs = append(c.db.execs, c.name+": "+sql)
	if r, ok := c.db.answers["exec:"+sql]; ok && r.err != nil {
		return pgconn.CommandTag{}, r.err
	}
	if strings.Contains(sql, "pg_notify") && c.db.listen {
		*c.payload = args[1].(string)
	}
	if strings.HasPrefix(sql, "LISTEN") {
		c.db.listen = true
	}
	return pgconn.CommandTag{}, nil
}

func (c *conn) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	r := c.db.answer(sql)
	if r.err != nil {
		return nil, r.err
	}
	return r, nil
}

func (c *conn) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row { return c.db.answer(sql) }

func (c *conn) WaitForNotification(context.Context) (*pgconn.Notification, error) {
	return &pgconn.Notification{Channel: "muster_session_check", Payload: *c.payload}, nil
}

func (d *fakeDB) connections() Connections {
	payload := new(string)
	return Connections{Main: &conn{db: d, name: "main", payload: payload},
		Session: &conn{db: d, name: "session", payload: payload}, Close: func() {}}
}

// keys is a master key of 32 bytes of c, in base64, and its id.
func keys(c byte) (string, string) {
	m := bytes.Repeat([]byte{c}, keyring.KeySize)
	return base64.StdEncoding.EncodeToString(m), keyring.KeyID(m)
}

// stateStore keeps the keyring state that Establish writes, as the row GetKeyringState scans.
type stateStore struct {
	row []any
}

func (s *stateStore) GetKeyringState(context.Context) (kdb.GetKeyringStateRow, error) {
	if s.row == nil {
		return kdb.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return kdb.GetKeyringStateRow{ActiveKeyID: s.row[0].(string), ActivatedAt: s.row[1].(time.Time),
		CanaryCiphertext: s.row[2].([]byte), CanaryKeyID: s.row[3].(string)}, nil
}

func (s *stateStore) CreateKeyringState(_ context.Context, arg kdb.CreateKeyringStateParams) (int64, error) {
	s.row = []any{arg.ActiveKeyID, arg.ActivatedAt, arg.CanaryCiphertext, arg.ActiveKeyID}
	return 1, nil
}

func (s *stateStore) GetActiveKeyID(context.Context) (string, error)               { return "", nil }
func (s *stateStore) RecordReplica(context.Context, kdb.RecordReplicaParams) error { return nil }
func (s *stateStore) DeleteReplica(context.Context, string) error                  { return nil }
func (s *stateStore) ListLiveReplicas(context.Context, time.Time) ([]kdb.Replica, error) {
	return nil, nil
}

// canary is the row of keyring_state that the Keyring of key writes on a new database.
func canary(t *testing.T, key string) []any {
	t.Helper()
	k, err := keyring.Load(t.Context(), keyring.Env{Keys: logging.Secret(key), Source: keyring.SecretKeysVar}, true)
	if err != nil {
		t.Fatal(err)
	}
	s := &stateStore{}
	if _, err := k.Establish(t.Context(), s, time.Now()); err != nil {
		t.Fatalf("establish: %v", err)
	}
	return s.row
}

func environ(key string, extra ...string) []string {
	return append([]string{
		"MUSTER_DATABASE_URL=postgres://muster:pw@127.0.0.1:1/muster?connect_timeout=1",
		"MUSTER_PUBLIC_URL=http://localhost:8080",
		"MUSTER_SECRET_KEYS=" + key,
	}, extra...)
}

func run(t *testing.T, d *fakeDB, env []string) (string, bool) {
	t.Helper()
	var out bytes.Buffer
	ok, err := Run(t.Context(), Options{Environ: env, Out: &out,
		open: func(context.Context, config.Config) (Connections, error) { return d.connections(), nil },
		real: clock.Real{}})
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), ok
}

func TestEveryCheckPasses(t *testing.T) {
	key, id := keys('a')
	d := newFakeDB(true)
	d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, key)}}
	out, ok := run(t, d, environ(key,
		"MUSTER_DATABASE_URL=postgres://muster:pw@127.0.0.1:1/muster?connect_timeout=1&sslmode=require"))
	want := "OK   database: main and session connections reachable\n" +
		"OK   postgresql_version: 17.11\n" +
		"OK   session_connection: advisory locks and LISTEN work\n" +
		"OK   tls_main: sslmode=require, the connection is encrypted\n" +
		"OK   tls_session: sslmode=require, the connection is encrypted\n" +
		"OK   key_canary: decrypts with key " + id + "\n" +
		"OK   keyring: active key " + id + ", no older key in use\n"
	if !ok || !strings.HasPrefix(out, want) || !strings.Contains(out, "OK   clock_skew: 0.0") {
		t.Errorf("ok %v, output:\n%s\nwant:\n%s", ok, out, want)
	}
	if strings.Count(out, "\n") != 8 {
		t.Errorf("%d lines, want one per check", strings.Count(out, "\n"))
	}
}

// TestUnencryptedConnectionsOnlyWarn: C-02.AC-13, sslmode=prefer against a server without TLS.
func TestUnencryptedConnectionsOnlyWarn(t *testing.T) {
	key, _ := keys('a')
	d := newFakeDB(false)
	d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, key)}}
	out, ok := run(t, d, environ(key))
	for _, want := range []string{
		"WARN tls_main: sslmode=prefer, the connection is not encrypted\n",
		"WARN tls_session: sslmode=prefer, the connection is not encrypted\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !ok || strings.Contains(out, "FAIL") {
		t.Errorf("ok %v, output:\n%s", ok, out)
	}
}

// TestKeyCanaryFails: C-02.AC-13, a Keyring that cannot decrypt the canary.
func TestKeyCanaryFails(t *testing.T) {
	key, id := keys('a')
	other, _ := keys('b')
	d := newFakeDB(false)
	d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, key)}}
	out, ok := run(t, d, environ(other))
	for _, want := range []string{
		"FAIL key_canary: master key does not match the database\n",
		"FAIL keyring: the active key " + id + " is not in MUSTER_SECRET_KEYS\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if ok {
		t.Error("the doctor passed")
	}
}

func TestOlderKeysInUse(t *testing.T) {
	key, id := keys('a')
	old, oldID := keys('b')
	_, goneID := keys('c')
	d := newFakeDB(true)
	d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, key)}}
	d.answers["FROM encrypted_values"] = &rows{values: [][]any{
		{pgtype.Text{String: id, Valid: true}, int64(4)},
		{pgtype.Text{String: oldID, Valid: true}, int64(2)},
		{pgtype.Text{String: goneID, Valid: true}, int64(1)},
		{pgtype.Text{}, int64(9)},
	}}
	out, ok := run(t, d, environ(key+","+old))
	ids := []string{oldID, goneID}
	if oldID > goneID {
		ids = []string{goneID, oldID}
	}
	parts := map[string]string{oldID: oldID + " (2 encrypted values)",
		goneID: goneID + " (1 encrypted values, not in MUSTER_SECRET_KEYS)"}
	want := "WARN keyring: active key " + id + "; older keys still in use: " + parts[ids[0]] + ", " + parts[ids[1]] + "\n"
	if !ok || !strings.Contains(out, want) {
		t.Errorf("ok %v, output:\n%s\nwant %s", ok, out, want)
	}
}

func TestFailingChecks(t *testing.T) {
	key, _ := keys('a')
	for _, tt := range []struct {
		name  string
		setup func(*fakeDB)
		env   []string
		want  string
	}{
		{name: "PostgreSQL 13", setup: func(d *fakeDB) {
			d.answers["SHOW server_version_num"] = &rows{values: [][]any{{"130012"}}}
		}, want: "FAIL postgresql_version: PostgreSQL 13.x is not supported: Muster needs PostgreSQL 14 or newer\n"},
		{name: "version unreadable", setup: func(d *fakeDB) {
			d.answers["SHOW server_version"] = &rows{err: errors.New("boom")}
			d.answers["SHOW server_version_num"] = &rows{values: [][]any{{"170011"}}}
		}, want: "FAIL postgresql_version: read the PostgreSQL version: boom\n"},
		{name: "a transaction pooler", setup: func(d *fakeDB) {
			d.answers["exec:SELECT pg_advisory_lock($1)"] = &rows{err: errors.New("pooler")}
		}, want: "FAIL session_connection: the session connection does not keep session state"},
		{name: "TLS state unreadable", setup: func(d *fakeDB) {
			d.answers["pg_stat_ssl"] = &rows{err: errors.New("boom")}
		}, want: "FAIL tls_main: read the TLS state: boom\n"},
		{name: "no canary yet", setup: func(d *fakeDB) {
			d.answers["FROM keyring_state"] = &rows{values: [][]any{}}
		}, want: "FAIL key_canary: read the key canary: the database has no key canary yet"},
		{name: "keyring unreadable", setup: func(d *fakeDB) {
			d.answers["FROM keyring_state"] = &rows{err: errors.New(`relation "keyring_state" does not exist`)}
		}, want: "FAIL keyring: the active key is unknown: the key canary could not be read\n"},
		{name: "no master key", env: environ(""), setup: func(d *fakeDB) {
			d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, key)}}
		}, want: "FAIL keyring: the master keys could not be loaded: MUSTER_SECRET_KEYS is empty"},
		{name: "organizations unreadable", setup: func(d *fakeDB) {
			d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, key)}}
			d.answers["FROM organizations"] = &rows{err: errors.New("boom")}
		}, want: "FAIL keyring: list the organizations: boom\n"},
		{name: "encrypted values unreadable", setup: func(d *fakeDB) {
			d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, key)}}
			d.answers["FROM encrypted_values"] = &rows{err: errors.New("boom")}
		}, want: "FAIL keyring: count the encrypted values: boom\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := newFakeDB(true)
			d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, key)}}
			tt.setup(d)
			env := tt.env
			if env == nil {
				env = environ(key)
			}
			out, ok := run(t, d, env)
			if ok || !strings.Contains(out, tt.want) {
				t.Errorf("ok %v, output:\n%s\nwant %q", ok, out, tt.want)
			}
			if strings.Count(out, "\n") != 8 {
				t.Errorf("%d lines, want one per check", strings.Count(out, "\n"))
			}
		})
	}
}

// TestDevelopmentKey: only muster dev doctor accepts the published development key, as only the server in development
// mode does.
func TestDevelopmentKey(t *testing.T) {
	d := newFakeDB(true)
	d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, keyring.DevelopmentKey)}}
	out, ok := run(t, d, environ(keyring.DevelopmentKey))
	if ok || !strings.Contains(out, "FAIL key_canary: MUSTER_SECRET_KEYS holds the published development key") {
		t.Errorf("ok %v, output:\n%s", ok, out)
	}
	var buf bytes.Buffer
	ok, err := Run(t.Context(), Options{Environ: environ(keyring.DevelopmentKey), Out: &buf, Development: true,
		open: func(context.Context, config.Config) (Connections, error) { return d.connections(), nil }})
	if err != nil || !ok || !strings.Contains(buf.String(), "OK   key_canary: decrypts with key") {
		t.Errorf("ok %v, err %v, output:\n%s", ok, err, buf.String())
	}
}

func TestClockSkewOnlyWarns(t *testing.T) {
	key, _ := keys('a')
	d := newFakeDB(true)
	d.answers["FROM keyring_state"] = &rows{values: [][]any{canary(t, key)}}
	d.answers["clock_timestamp"] = &rows{values: [][]any{{time.Now().Add(-3 * time.Second)}}}
	out, ok := run(t, d, environ(key))
	if !ok || !strings.Contains(out, "WARN clock_skew: 3.0") || !strings.Contains(out, "more than 2s") {
		t.Errorf("ok %v, output:\n%s", ok, out)
	}
	d.answers["clock_timestamp"] = &rows{err: errors.New("boom")}
	out, ok = run(t, d, environ(key))
	if !ok || !strings.Contains(out, "WARN clock_skew: not measured: read the database clock: boom\n") {
		t.Errorf("ok %v, output:\n%s", ok, out)
	}
}

func TestUnreachableDatabase(t *testing.T) {
	key, _ := keys('a')
	var out bytes.Buffer
	// Nothing listens on port 1: the main connection fails, and nothing else is checked.
	ok, err := Run(t.Context(), Options{Environ: environ(key), Out: &out})
	if err != nil || ok {
		t.Fatalf("Run = %v, %v", ok, err)
	}
	if got := out.String(); !strings.HasPrefix(got, "FAIL database: the main connection is unreachable: ") ||
		strings.Count(got, "\n") != 1 {
		t.Errorf("output:\n%s", got)
	}
	out.Reset()
	if _, err := Run(t.Context(), Options{Environ: environ(key, "MUSTER_DATABASE_PORT=abc",
		"MUSTER_DATABASE_URL="), Out: &out}); err == nil {
		t.Error("Run with invalid settings succeeded")
	}
}

func TestReadOnly(t *testing.T) {
	d := newFakeDB(true)
	conns := d.connections()
	if err := readOnly(t.Context(), conns); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main: SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY",
		"session: SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY"} {
		if !strings.Contains(strings.Join(d.execs, "\n"), want) {
			t.Errorf("statements %v lack %s", d.execs, want)
		}
	}
	d.answers["exec:SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY"] = &rows{err: errors.New("boom")}
	if err := readOnly(t.Context(), conns); err == nil {
		t.Error("readOnly succeeded")
	}
}
