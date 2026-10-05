// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/auth"
	adb "github.com/muster-io/muster/internal/auth/dbgen"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/leader"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	odb "github.com/muster-io/muster/internal/organization/dbgen"
	"github.com/muster-io/muster/internal/partitions"
	"github.com/muster-io/muster/internal/server"
	"github.com/muster-io/muster/internal/users"
)

type fakeDB struct {
	mu                                 sync.Mutex
	checkErr, migrateErr, schemaErr    error
	pingErr, lockErr                   error
	checked, migrated, schema, metrics bool
	closed, locked                     int
	keys                               fakeKeyringStore
	org                                fakeOrgStore
	// partitionErr fails the partition maintenance session; ddl counts its statements.
	partitionErr error
	ddl          int
}

func (f *fakeDB) LeaderSession(context.Context) (leader.Session, error) {
	return nil, errors.New("the fake database has no Leader lock")
}

func (f *fakeDB) PartitionSession(context.Context) (partitions.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.partitionErr != nil {
		return nil, f.partitionErr
	}
	return fakeSession{f}, nil
}

func (f *fakeDB) LeaderStore() leader.Store { return nil }

func (f *fakeDB) ReplicaPruner() keyring.Pruner { return nil }

func (f *fakeDB) Clock() rowQuerier { return fakeClockRow{} }

// fakeSession is a partition maintenance session on a database without partitions: every query returns no rows.
type fakeSession struct{ db *fakeDB }

func (s fakeSession) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	s.db.ddl++
	return pgconn.CommandTag{}, nil
}

func (fakeSession) Query(context.Context, string, ...any) (pgx.Rows, error) { return noRows{}, nil }

func (fakeSession) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (fakeSession) Close(context.Context) error { return nil }

type noRows struct{}

func (noRows) Close()                                       {}
func (noRows) Err() error                                   { return nil }
func (noRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (noRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (noRows) Next() bool                                   { return false }
func (noRows) Scan(...any) error                            { return errors.New("no rows") }
func (noRows) Values() ([]any, error)                       { return nil, nil }
func (noRows) RawValues() [][]byte                          { return nil }
func (noRows) Conn() *pgx.Conn                              { return nil }
func (noRows) TypeMap() *pgtype.Map                         { return nil }

// fakeClockRow is a database clock that agrees with the system clock.
type fakeClockRow struct{}

func (fakeClockRow) QueryRow(context.Context, string, ...any) pgx.Row { return clockRow{} }

type clockRow struct{}

func (clockRow) Scan(dest ...any) error {
	*dest[0].(*time.Time) = time.Now()
	return nil
}

func (f *fakeDB) WithMigrationLock(ctx context.Context, fn func(context.Context) error) error {
	f.mu.Lock()
	f.locked++
	err := f.lockErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return fn(ctx)
}

func (f *fakeDB) KeyringStore() keyring.Store { return &f.keys }

func (f *fakeDB) OrganizationStore() organization.Store { return &f.org }

func (f *fakeDB) UsersStore() users.Store { return fakeUsersStore{} }

func (f *fakeDB) AuthStore() auth.Store { return fakeAuthStore{} }

// fakeUsersStore has an Admin, so the bootstrap step creates nothing.
type fakeUsersStore struct{ users.Store }

func (fakeUsersStore) CountAdmins(context.Context, int64) (int64, error) { return 1, nil }

// fakeAuthStore holds the allocation of Permissions to Roles.
type fakeAuthStore struct{ auth.Store }

func (fakeAuthStore) ListRolePermissions(context.Context) ([]adb.RolePermission, error) {
	return []adb.RolePermission{{Role: "admin", Permission: "users:read"}, {Role: "responder", Permission: "alerts:read"},
		{Role: "viewer", Permission: "alerts:read"}}, nil
}

// fakeKeyringStore keeps keyring_state and replicas in memory; err fails every call, recordErr the replica records.
type fakeKeyringStore struct {
	mu        sync.Mutex
	state     *kdb.GetKeyringStateRow
	replicas  map[string]kdb.RecordReplicaParams
	err       error
	recordErr error
}

func (s *fakeKeyringStore) GetKeyringState(context.Context) (kdb.GetKeyringStateRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return kdb.GetKeyringStateRow{}, s.err
	}
	if s.state == nil {
		return kdb.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.state, nil
}

func (s *fakeKeyringStore) CreateKeyringState(_ context.Context, arg kdb.CreateKeyringStateParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != nil {
		return 0, nil
	}
	s.state = &kdb.GetKeyringStateRow{ActiveKeyID: arg.ActiveKeyID, ActivatedAt: arg.ActivatedAt,
		CanaryCiphertext: arg.CanaryCiphertext, CanaryKeyID: arg.ActiveKeyID}
	return 1, nil
}

func (s *fakeKeyringStore) GetActiveKeyID(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.ActiveKeyID, nil
}

func (s *fakeKeyringStore) setActive(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.ActiveKeyID, s.state.CanaryKeyID = id, id
}

func (s *fakeKeyringStore) RecordReplica(_ context.Context, arg kdb.RecordReplicaParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordErr != nil {
		return s.recordErr
	}
	if s.replicas == nil {
		s.replicas = map[string]kdb.RecordReplicaParams{}
	}
	s.replicas[arg.ReplicaID] = arg
	return nil
}

func (s *fakeKeyringStore) DeleteReplica(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.replicas, id)
	return nil
}

func (s *fakeKeyringStore) ListLiveReplicas(context.Context, time.Time) ([]kdb.Replica, error) {
	return nil, errors.New("not used by the runtime")
}

func (s *fakeKeyringStore) replicaCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.replicas)
}

// fakeOrgStore keeps the Organization and its outbound address policy in memory; err fails every call.
type fakeOrgStore struct {
	mu     sync.Mutex
	org    *odb.CreateOrganizationParams
	policy *odb.CreateOutboundPolicyParams
	err    error
}

func (s *fakeOrgStore) GetOrganization(context.Context) (odb.GetOrganizationRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return odb.GetOrganizationRow{}, s.err
	}
	if s.org == nil {
		return odb.GetOrganizationRow{}, pgx.ErrNoRows
	}
	return odb.GetOrganizationRow{ID: 1, PublicID: s.org.PublicID, Name: s.org.Name}, nil
}

func (s *fakeOrgStore) CreateOrganization(_ context.Context, arg odb.CreateOrganizationParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.org = &arg
	return 1, nil
}

func (s *fakeOrgStore) CreateOutboundPolicy(_ context.Context, arg odb.CreateOutboundPolicyParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policy != nil {
		return 0, nil
	}
	s.policy = &arg
	return 1, nil
}

func (s *fakeOrgStore) GetOutboundPolicy(context.Context, int64) (odb.GetOutboundPolicyRow, error) {
	return odb.GetOutboundPolicyRow{}, errors.New("not used by the runtime")
}

func (f *fakeDB) Check(context.Context, *logging.Logger) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = true
	return f.checkErr
}

func (f *fakeDB) Migrate(context.Context, *logging.Logger) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.migrated = true
	return f.migrateErr
}

func (f *fakeDB) CheckSchema(context.Context, *logging.Logger) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schema = true
	return f.schemaErr
}

func (f *fakeDB) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

func (f *fakeDB) RegisterMetrics() { f.metrics = true }

func (f *fakeDB) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
}

// syncBuffer collects log lines written while the test reads them.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) events(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var lines []map[string]any
	for line := range strings.Lines(b.buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		lines = append(lines, m)
	}
	return lines
}

func names(lines []map[string]any) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l["event"].(string))
	}
	return out
}

// testKey is a master key for the tests: 32 bytes of "k" in base64. otherKey is another.
const (
	testKey  = "a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s="
	otherKey = "b29vb29vb29vb29vb29vb29vb29vb29vb29vb29vb28="
)

func env(extra ...string) []string {
	return append([]string{
		"MUSTER_DATABASE_URL=postgres://muster:pw@127.0.0.1:1/muster?sslmode=disable&connect_timeout=1",
		"MUSTER_SECRET_KEYS=" + testKey,
		"MUSTER_PUBLIC_URL=http://localhost:8080",
		"MUSTER_LISTEN_APP=127.0.0.1:0",
		"MUSTER_LISTEN_INGEST=127.0.0.1:0",
		"MUSTER_LISTEN_INTERNAL=127.0.0.1:0",
	}, extra...)
}

func options(fake *fakeDB, out io.Writer, environ []string) Options {
	return Options{
		Environ: environ,
		Stdout:  out,
		open:    func(context.Context, config.Config) (database, error) { return fake, nil },
		grace:   2 * time.Second,
	}
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// running starts Run in the background and returns the addresses once it serves.
func running(t *testing.T, opts Options) (server.Addresses, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	addrs := make(chan server.Addresses, 1)
	opts.serving = func(a server.Addresses) { addrs <- a }
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()
	select {
	case a := <-addrs:
		return a, cancel, done
	case err := <-done:
		cancel()
		t.Fatalf("Run returned before serving: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Run did not serve within 10s")
	}
	return server.Addresses{}, cancel, done
}

func TestRunAndShutdown(t *testing.T) {
	fake := &fakeDB{}
	var out syncBuffer
	addrs, cancel, done := running(t, options(fake, &out, env("MUSTER_DATABASE_HOST=ignored")))
	if addrs.App != addrs.Ingest {
		t.Errorf("app %s and ingest %s are on the same address and must share one port", addrs.App, addrs.Ingest)
	}
	internal := "http://" + addrs.Internal
	if code, body := get(t, internal+"/health/ready"); code != http.StatusOK || body != "ok\n" {
		t.Errorf("ready = %d %q", code, body)
	}
	if code, _ := get(t, internal+"/health/live"); code != http.StatusOK {
		t.Errorf("live = %d", code)
	}
	// The clock skew check runs at once in the background.
	_, metrics := get(t, internal+"/metrics")
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(metrics, "\nmuster_clock_skew_seconds ") &&
		time.Now().Before(deadline); _, metrics = get(t, internal+"/metrics") {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(metrics, "muster_build_info{") || !strings.Contains(metrics, "\nmuster_leader 0\n") ||
		!strings.Contains(metrics, "\nmuster_clock_skew_seconds ") {
		t.Errorf("/metrics lacks muster_build_info, muster_leader or muster_clock_skew_seconds:\n%s", metrics)
	}
	if code, _ := get(t, "http://"+addrs.App+"/api/v1/ingest"); code != http.StatusNotFound {
		t.Errorf("the ingest handler answered %d, want 404 until the ingestion route exists", code)
	}
	if code, _ := get(t, "http://"+addrs.App+"/"); code != http.StatusOK && code != http.StatusServiceUnavailable {
		t.Errorf("the SPA answered %d", code)
	}

	fake.mu.Lock()
	fake.pingErr = errors.New("down")
	fake.mu.Unlock()
	if code, body := get(t, internal+"/health/ready"); code != http.StatusServiceUnavailable || body != "database unavailable\n" {
		t.Errorf("ready without the database = %d %q", code, body)
	}

	begin := time.Now()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v after the shutdown", err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("the shutdown took %v", elapsed)
	}
	if !fake.checked || fake.migrated || !fake.schema || !fake.metrics || fake.closed != 1 {
		t.Errorf("database %+v: want checked, not migrated, schema checked, metrics, closed once", fake)
	}
	lines := out.events(t)
	want := []string{"process_started", "database_settings_conflict", "keyring_loaded", "organization_created",
		"partitions_maintained", "listeners_started", "shutdown_requested", "process_stopped"}
	if strings.Join(names(lines), " ") != strings.Join(want, " ") {
		t.Fatalf("events %v, want %v", names(lines), want)
	}
	if lines[1]["used"] != "MUSTER_DATABASE_URL" || lines[1]["level"] != "WARN" ||
		lines[6]["level"] != "WARN" || lines[6]["grace_seconds"] != float64(2) {
		t.Errorf("lines %v", lines)
	}
	d := net.Dialer{Timeout: time.Second}
	if conn, err := d.DialContext(t.Context(), "tcp", addrs.Internal); err == nil {
		_ = conn.Close()
		t.Error("the internal listener still accepts connections")
	}
}

func TestMigrateOnStart(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts func(Options) Options
		want bool
	}{
		{"off", func(o Options) Options { return o }, false},
		{"MUSTER_MIGRATE_ON_START", func(o Options) Options {
			o.Environ = append(o.Environ, "MUSTER_MIGRATE_ON_START=true")
			return o
		}, true},
		{"development", func(o Options) Options {
			o.Development = true
			return o
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDB{}
			_, cancel, done := running(t, tt.opts(options(fake, io.Discard, env())))
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if fake.migrated != tt.want {
				t.Errorf("migrated %v, want %v", fake.migrated, tt.want)
			}
		})
	}
}

func TestStartupFailures(t *testing.T) {
	var lc net.ListenConfig
	busy, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	tests := []struct {
		name       string
		fake       *fakeDB
		environ    []string
		openErr    error
		want       string
		wantClosed int
	}{
		{name: "open", fake: &fakeDB{}, openErr: errors.New("bad URL"), want: "bad URL"},
		{name: "check", fake: &fakeDB{checkErr: errors.New("PostgreSQL 13.x is not supported")},
			want: "PostgreSQL 13.x is not supported", wantClosed: 1},
		{name: "migrate", fake: &fakeDB{migrateErr: errors.New("syntax error")}, environ: []string{"MUSTER_MIGRATE_ON_START=true"},
			want: "syntax error", wantClosed: 1},
		{name: "schema", fake: &fakeDB{schemaErr: errors.New("database schema version 999 is newer")},
			want: "database schema version 999 is newer", wantClosed: 1},
		{name: "listen", fake: &fakeDB{}, environ: []string{"MUSTER_LISTEN_INTERNAL=" + busy.Addr().String()},
			want: "(MUSTER_LISTEN_INTERNAL)", wantClosed: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out syncBuffer
			opts := options(tt.fake, &out, env(tt.environ...))
			if tt.openErr != nil {
				opts.open = func(context.Context, config.Config) (database, error) { return nil, tt.openErr }
			}
			err := Run(t.Context(), opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run = %v, want %q", err, tt.want)
			}
			lines := out.events(t)
			last := lines[len(lines)-1]
			if last["event"] != "startup_failed" || last["level"] != "ERROR" || !strings.Contains(last["error"].(string), tt.want) {
				t.Errorf("last line %v", last)
			}
			if tt.fake.closed != tt.wantClosed {
				t.Errorf("closed %d times, want %d", tt.fake.closed, tt.wantClosed)
			}
		})
	}
}

func TestConfigError(t *testing.T) {
	var out syncBuffer
	err := Run(t.Context(), options(&fakeDB{}, &out, env("MUSTER_DATABASE_PORT=abc")))
	if err == nil || err.Error() != `invalid MUSTER_DATABASE_PORT: "abc" is not a port number` {
		t.Errorf("Run = %v", err)
	}
	if err := Migrate(t.Context(), options(&fakeDB{}, &out, nil)); err == nil ||
		!strings.Contains(err.Error(), "MUSTER_PUBLIC_URL is required") {
		t.Errorf("Migrate = %v", err)
	}
	if lines := out.events(t); len(lines) != 0 {
		t.Errorf("logged before the logger exists: %v", lines)
	}
}

func TestMigrate(t *testing.T) {
	fake := &fakeDB{}
	var out syncBuffer
	if err := Migrate(t.Context(), options(fake, &out, env())); err != nil {
		t.Fatal(err)
	}
	if !fake.checked || !fake.migrated || fake.schema || fake.closed != 1 {
		t.Errorf("database %+v", fake)
	}
	fake = &fakeDB{migrateErr: errors.New("database schema version 999 is newer than this binary knows (1)")}
	if err := Migrate(t.Context(), options(fake, &out, env())); err == nil || fake.closed != 1 {
		t.Errorf("Migrate = %v, closed %d", err, fake.closed)
	}
	fake = &fakeDB{}
	if err := Migrate(t.Context(), options(fake, &out, env("MUSTER_SECRET_KEYS="))); err == nil ||
		!strings.Contains(err.Error(), "MUSTER_SECRET_KEYS is empty") || fake.checked {
		t.Errorf("Migrate without a key = %v, database checked %v", err, fake.checked)
	}
	fake = &fakeDB{}
	if err := Migrate(t.Context(), options(fake, &out, env("MUSTER_SECRET_KEYS="+keyring.DevelopmentKey))); err != nil {
		t.Errorf("Migrate with the development key = %v", err)
	}
	fake = &fakeDB{checkErr: errors.New("pooler")}
	if err := Migrate(t.Context(), options(fake, &out, env())); err == nil || fake.migrated {
		t.Errorf("Migrate after a failed check = %v, migrated %v", err, fake.migrated)
	}
}

// TestRealDatabaseUnreachable runs the real internal/db against an address where nothing listens.
func TestRealDatabaseUnreachable(t *testing.T) {
	var out syncBuffer
	err := Run(t.Context(), Options{Environ: env(), Stdout: &out})
	if err == nil || !strings.HasPrefix(err.Error(), "connect to the database (main connection)") {
		t.Fatalf("Run = %v", err)
	}
	if strings.Contains(err.Error(), ":pw@") {
		t.Errorf("the error carries the password: %v", err)
	}
}

func TestListenerOf(t *testing.T) {
	if got := listenerOf(&server.ListenerError{Listener: "app", Err: io.EOF}); got != "app" {
		t.Errorf("listenerOf = %q", got)
	}
	if got := listenerOf(io.EOF); got != "unknown" {
		t.Errorf("listenerOf = %q", got)
	}
}

// TestReadinessDuringShutdown holds a request in progress on the app listener through the shutdown: meanwhile
// readiness answers 503, and the process ends once the request goes away.
func TestReadinessDuringShutdown(t *testing.T) {
	var out syncBuffer
	addrs, cancel, done := running(t, options(&fakeDB{}, &out, env("MUSTER_LISTEN_INGEST=127.0.0.1:0")))
	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "tcp", addrs.App)
	if err != nil {
		t.Fatal(err)
	}
	// A request whose headers have not ended keeps its connection active.
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: muster\r\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := get(t, "http://"+addrs.Internal+"/health/ready")
		if code == http.StatusServiceUnavailable && body == "shutting down\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("readiness during the shutdown: %d %q", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, _ := get(t, "http://"+addrs.Internal+"/health/live"); code != http.StatusOK {
		t.Errorf("liveness during the shutdown: %d", code)
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned while a request was in progress: %v", err)
	default:
	}
	_ = conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// blockingDB waits in Check until its context ends, like a migration lock held by another replica.
type blockingDB struct{ fakeDB }

func (b *blockingDB) Check(ctx context.Context, _ *logging.Logger) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestSignalDuringStartup ends the context while startup waits: the server stops cleanly and exits 0, muster migrate
// reports that it was stopped.
func TestSignalDuringStartup(t *testing.T) {
	for _, run := range []struct {
		name string
		f    func(context.Context, Options) error
		want error
	}{{"server", Run, nil}, {"migrate", Migrate, errStopped}} {
		t.Run(run.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			var out syncBuffer
			opts := options(nil, &out, env())
			opts.open = func(context.Context, config.Config) (database, error) { return &blockingDB{}, nil }
			done := make(chan error, 1)
			go func() { done <- run.f(ctx, opts) }()
			time.Sleep(20 * time.Millisecond)
			cancel()
			if err := <-done; !errors.Is(err, run.want) {
				t.Fatalf("got %v, want %v", err, run.want)
			}
			got := names(out.events(t))
			want := []string{"process_started", "shutdown_requested", "process_stopped"}
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("events %v, want %v", got, want)
			}
		})
	}
}

// TestStartupCreatesRuntimeRows: a first start writes the active key with its canary, the Organization with its
// outbound address policy and the replica record under the migration lock; the record goes at the shutdown, and a
// second start changes nothing.
func TestStartupCreatesRuntimeRows(t *testing.T) {
	fake := &fakeDB{}
	_, cancel, done := running(t, options(fake, io.Discard, env()))
	if fake.keys.replicaCount() != 1 {
		t.Errorf("%d replica records while serving, want 1", fake.keys.replicaCount())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if fake.locked != 1 || fake.keys.state == nil || fake.org.org == nil || fake.org.policy == nil {
		t.Fatalf("after the first start: locked %d, state %v, organization %v, policy %v", fake.locked,
			fake.keys.state, fake.org.org, fake.org.policy)
	}
	if fake.keys.replicaCount() != 0 {
		t.Errorf("%d replica records after the shutdown, want 0", fake.keys.replicaCount())
	}
	if want := keyring.KeyID([]byte(strings.Repeat("k", 32))); fake.keys.state.ActiveKeyID != want {
		t.Errorf("active key %s, want the first key %s", fake.keys.state.ActiveKeyID, want)
	}
	if fake.org.org.Name != "Muster" || fake.org.policy.Policy != "standard" {
		t.Errorf("organization %+v, policy %+v", fake.org.org, fake.org.policy)
	}
	state, org := *fake.keys.state, *fake.org.org

	var out syncBuffer
	_, cancel, done = running(t, options(fake, &out, env("MUSTER_SECRET_KEYS="+otherKey+","+testKey)))
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !sameState(*fake.keys.state, state) || fake.org.org.PublicID != org.PublicID {
		t.Errorf("a second start changed the rows")
	}
	if got := names(out.events(t)); slices.Contains(got, "organization_created") {
		t.Errorf("a second start logged organization_created: %v", got)
	}
}

func sameState(a, b kdb.GetKeyringStateRow) bool {
	return a.ActiveKeyID == b.ActiveKeyID && a.CanaryKeyID == b.CanaryKeyID &&
		bytes.Equal(a.CanaryCiphertext, b.CanaryCiphertext)
}

func TestKeyringRefusals(t *testing.T) {
	tests := []struct {
		name        string
		environ     []string
		development bool
		want        string
	}{
		{"empty", []string{"MUSTER_SECRET_KEYS="}, false,
			"MUSTER_SECRET_KEYS is empty: generate a key with `openssl rand -base64 32`"},
		{"placeholder", []string{"MUSTER_SECRET_KEYS=" + keyring.Placeholder}, false,
			"MUSTER_SECRET_KEYS still holds the placeholder of the compose example: generate a key with " +
				"`openssl rand -base64 32`"},
		{"development key", []string{"MUSTER_SECRET_KEYS=" + keyring.DevelopmentKey}, false,
			"MUSTER_SECRET_KEYS holds the published development key"},
		{"not a key", []string{"MUSTER_SECRET_KEYS=" + testKey + ",a2V5"}, true,
			"MUSTER_SECRET_KEYS: key 2 is not a base64-encoded key of 32 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDB{}
			var out syncBuffer
			opts := options(fake, &out, env(tt.environ...))
			opts.Development = tt.development
			err := Run(t.Context(), opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run = %v, want %q", err, tt.want)
			}
			if fake.checked {
				t.Error("the database was opened before the Keyring was checked")
			}
			if last := out.events(t)[len(out.events(t))-1]; last["event"] != "startup_failed" {
				t.Errorf("last line %v", last)
			}
		})
	}
}

// TestDevelopmentKeyInDevelopmentMode: muster dev, also with --replica, runs with the development key.
func TestDevelopmentKeyInDevelopmentMode(t *testing.T) {
	opts := options(&fakeDB{}, io.Discard, env("MUSTER_SECRET_KEYS="+keyring.DevelopmentKey))
	opts.Development = true
	_, cancel, done := running(t, opts)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestKeyCanaryFailed(t *testing.T) {
	fake := &fakeDB{}
	_, cancel, done := running(t, options(fake, io.Discard, env()))
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	fake.closed = 0
	var out syncBuffer
	err := Run(t.Context(), options(fake, &out, env("MUSTER_SECRET_KEYS="+otherKey)))
	if !errors.Is(err, keyring.ErrKeyMismatch) || err.Error() != "master key does not match the database" {
		t.Fatalf("Run = %v", err)
	}
	lines := out.events(t)
	got := names(lines)
	if want := []string{"process_started", "key_canary_failed", "startup_failed"}; strings.Join(got, " ") !=
		strings.Join(want, " ") {
		t.Fatalf("events %v, want %v", got, want)
	}
	if lines[1]["level"] != "ERROR" || lines[1]["error"] != "master key does not match the database" {
		t.Errorf("line %v", lines[1])
	}
	if fake.closed != 1 || fake.keys.replicaCount() != 0 {
		t.Errorf("closed %d, replica records %d", fake.closed, fake.keys.replicaCount())
	}
}

// TestActiveKeyNotHeld: a running replica that sees an active key it does not hold stops with the canary error and
// removes its record.
func TestActiveKeyNotHeld(t *testing.T) {
	fake := &fakeDB{}
	var out syncBuffer
	opts := options(fake, &out, env())
	opts.keyRecordRefresh = 10 * time.Millisecond
	_, cancel, done := running(t, opts)
	defer cancel()
	fake.keys.setActive("k-unknown")
	select {
	case err := <-done:
		if !errors.Is(err, keyring.ErrKeyMismatch) {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the replica did not stop")
	}
	got := names(out.events(t))
	if !slices.Contains(got, "active_key_not_held") || slices.Contains(got, "process_stopped") {
		t.Errorf("events %v", got)
	}
	if fake.keys.replicaCount() != 0 {
		t.Errorf("%d replica records after the stop", fake.keys.replicaCount())
	}
}

func TestBootstrapFailures(t *testing.T) {
	tests := []struct {
		name string
		fake *fakeDB
		want string
	}{
		{"lock", &fakeDB{lockErr: errors.New("take the migration lock: boom")}, "take the migration lock: boom"},
		{"keyring state", &fakeDB{keys: fakeKeyringStore{err: errors.New("boom")}}, "read the key canary: boom"},
		{"organization", &fakeDB{org: fakeOrgStore{err: errors.New("boom")}},
			"start-up step organization: read the organization: boom"},
		{"replica record", &fakeDB{keys: fakeKeyringStore{recordErr: errors.New("boom")}}, "record the keys of replica"},
		{"partitions", &fakeDB{partitionErr: errors.New("boom")}, "start-up step partitions: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Run(t.Context(), options(tt.fake, io.Discard, env()))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run = %v, want %q", err, tt.want)
			}
			if tt.fake.closed != 1 {
				t.Errorf("closed %d times", tt.fake.closed)
			}
		})
	}
}
