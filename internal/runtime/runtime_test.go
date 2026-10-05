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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/server"
)

type fakeDB struct {
	mu                                 sync.Mutex
	checkErr, migrateErr, schemaErr    error
	pingErr                            error
	checked, migrated, schema, metrics bool
	closed                             int
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

func env(extra ...string) []string {
	return append([]string{
		"MUSTER_DATABASE_URL=postgres://muster:pw@127.0.0.1:1/muster?sslmode=disable&connect_timeout=1",
		"MUSTER_SECRET_KEYS=a2V5",
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
	_, metrics := get(t, internal+"/metrics")
	if !strings.Contains(metrics, "muster_build_info{") {
		t.Errorf("/metrics lacks muster_build_info:\n%s", metrics)
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
	want := []string{"process_started", "database_settings_conflict", "listeners_started", "shutdown_requested",
		"process_stopped"}
	if strings.Join(names(lines), " ") != strings.Join(want, " ") {
		t.Fatalf("events %v, want %v", names(lines), want)
	}
	if lines[1]["used"] != "MUSTER_DATABASE_URL" || lines[1]["level"] != "WARN" ||
		lines[3]["level"] != "WARN" || lines[3]["grace_seconds"] != float64(2) {
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
