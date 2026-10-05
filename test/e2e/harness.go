// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

// Package e2e is the end-to-end suite: it runs the muster binary in development mode and drives it over HTTP. make e2e
// starts the development PostgreSQL, builds the binary and runs the suite with MUSTER_E2E_BINARY pointing to it;
// E2E_REPLICAS=2 runs two replicas. Each harness gets a fresh database on the development PostgreSQL, or on the server
// of MUSTER_E2E_DATABASE_URL, and drops it at the end.
package e2e

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/devmode"
)

type Mode int

const (
	// DevProcess runs `muster dev` as a child process with its fake servers and, with E2E_REPLICAS=2,
	// `muster dev --replica` beside it. The fake servers live in the first process.
	DevProcess Mode = iota + 1
	// FakesInProcess runs the fake servers in the test process and each replica as a `muster dev --replica` child
	// process that a test can stop and start again while the fakes keep their recorded requests and faults.
	FakesInProcess
)

const (
	readyTimeout = 30 * time.Second
	pollInterval = 100 * time.Millisecond
	stopTimeout  = 10 * time.Second

	// fakesReadyLine is the last address line of `muster dev`, printed once all three fake servers listen. A stale
	// process on the fixed ports answers the HTTP checks, but the new one exits instead of printing it.
	fakesReadyLine = "muster dev: fake Telegram "
)

// FakeURLs are the base URLs of the fake servers.
type FakeURLs struct {
	Alertmanager, Mattermost, Telegram string
}

type Harness struct {
	t      *testing.T
	binary string
	// DatabaseURL is the fresh database that every replica of the harness uses.
	DatabaseURL string

	Fakes FakeURLs
	// InProcess holds the fake servers in the mode FakesInProcess, for their Go accessors.
	InProcess *devmode.Fakes
	Replicas  []*Replica
}

// Start runs Muster in development mode for the test; everything it starts is stopped when the test ends. The fixed
// ports are shared, so tests that use the harness must not run in parallel. The methods that act on a replica take
// the testing.TB to fail, so subtests can call them.
func Start(t *testing.T, mode Mode) *Harness {
	t.Helper()
	h := &Harness{t: t, binary: binary(t)}
	h.DatabaseURL = freshDatabase(t)
	switch mode {
	case DevProcess:
		h.startDevProcess(replicaCount(t))
	case FakesInProcess:
		h.startFakesInProcess()
	default:
		t.Fatalf("unknown harness mode %d", mode)
	}
	return h
}

func (h *Harness) startDevProcess(replicas int) {
	h.t.Helper()
	a := devmode.FakeAddresses()
	h.Fakes = FakeURLs{
		Alertmanager: "http://" + a.Alertmanager,
		Mattermost:   "http://" + a.Mattermost,
		Telegram:     "http://" + a.Telegram,
	}
	first := h.newReplica([]string{"dev"}, h.databaseEnv(), ":8080", ":8081", ":8082", fakesReadyLine)
	first.Start(h.t)
	first.waitFor(h.t, "the fake servers to answer", func() bool {
		for _, u := range []string{h.Fakes.Alertmanager, h.Fakes.Mattermost, h.Fakes.Telegram} {
			if !answers(h.t.Context(), u+"/_fake/requests") {
				return false
			}
		}
		return true
	})
	h.Replicas = append(h.Replicas, first)
	if replicas == 2 {
		second := h.newReplica([]string{"dev", "--replica"}, h.databaseEnv(), devmode.ReplicaListenApp,
			devmode.ReplicaListenIngest, devmode.ReplicaListenInternal, devmode.ReplicaLine)
		second.Start(h.t)
		h.Replicas = append(h.Replicas, second)
	}
}

func (h *Harness) startFakesInProcess() {
	h.t.Helper()
	fakes, err := devmode.StartFakes(h.t.Context(), devmode.FakeAddresses())
	if err != nil {
		h.t.Fatalf("start the fake servers in the test process: %v", err)
	}
	h.t.Cleanup(func() {
		if err := fakes.Close(context.WithoutCancel(h.t.Context())); err != nil {
			h.t.Errorf("stop the fake servers: %v", err)
		}
	})
	h.InProcess = fakes
	h.Fakes = FakeURLs{
		Alertmanager: fakes.Alertmanager.URL(),
		Mattermost:   fakes.Mattermost.URL(),
		Telegram:     fakes.Telegram.URL(),
	}
}

// ReplicaOptions configure a replica in the mode FakesInProcess. The listen addresses default to those of
// `muster dev --replica`; Env is added to the test process's environment.
type ReplicaOptions struct {
	Env                                     map[string]string
	ListenApp, ListenIngest, ListenInternal string
}

// StartReplica starts a `muster dev --replica` child process in the mode FakesInProcess on the harness's database and
// waits until it is ready; the process lives until the test that started the harness ends. Env may replace the
// database.
func (h *Harness) StartReplica(t testing.TB, opts ReplicaOptions) *Replica {
	t.Helper()
	if h.InProcess == nil {
		t.Fatal("StartReplica needs the mode FakesInProcess; in DevProcess, E2E_REPLICAS sets the replicas")
	}
	app := cmp.Or(opts.ListenApp, devmode.ReplicaListenApp)
	ingest := cmp.Or(opts.ListenIngest, devmode.ReplicaListenIngest)
	internal := cmp.Or(opts.ListenInternal, devmode.ReplicaListenInternal)
	env := h.databaseEnv()
	for name, value := range opts.Env {
		env = append(env, name+"="+value)
	}
	env = append(env, "MUSTER_LISTEN_APP="+app, "MUSTER_LISTEN_INGEST="+ingest, "MUSTER_LISTEN_INTERNAL="+internal)
	r := h.newReplica([]string{"dev", "--replica"}, env, app, ingest, internal, devmode.ReplicaLine)
	r.Start(t)
	h.Replicas = append(h.Replicas, r)
	return r
}

// Replica is one muster child process. App, Ingest and Internal are the base URLs of its listeners.
type Replica struct {
	App, Ingest, Internal string

	// owner is the test that started the harness: its context bounds the process and its cleanup stops it. The
	// methods report to the testing.TB they are given.
	owner     *testing.T
	binary    string
	args      []string
	env       []string
	readyLine string

	cmd     *exec.Cmd
	out     *syncBuffer
	exited  chan struct{}
	waitErr error
	// earlier holds the output of the runs before the current one.
	earlier strings.Builder
}

func (h *Harness) newReplica(args, env []string, app, ingest, internal, readyLine string) *Replica {
	r := &Replica{
		App:       baseURL(app),
		Ingest:    baseURL(ingest),
		Internal:  baseURL(internal),
		owner:     h.t,
		binary:    h.binary,
		args:      args,
		env:       env,
		readyLine: readyLine,
	}
	h.t.Cleanup(func() {
		r.Stop(h.t)
		h.t.Logf("output of %s:\n%s", r.name(), r.allOutput())
	})
	return r
}

func (r *Replica) name() string { return "muster " + strings.Join(r.args, " ") }

// Start starts the process and waits for its ready line; it fails t at once when the process exits first.
func (r *Replica) Start(t testing.TB) {
	t.Helper()
	if r.Running() {
		t.Fatalf("%s is already running", r.name())
	}
	if r.out != nil {
		r.earlier.WriteString(r.out.String())
	}
	//nolint:gosec // G204: the binary under test, from MUSTER_E2E_BINARY
	cmd := exec.CommandContext(r.owner.Context(), r.binary, r.args...)
	cmd.Env = append(os.Environ(), r.env...)
	r.out = &syncBuffer{}
	cmd.Stdout, cmd.Stderr = r.out, r.out
	// The end of the test interrupts the process like Stop does.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = stopTimeout
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", r.name(), err)
	}
	r.cmd, r.exited = cmd, make(chan struct{})
	go func(exited chan struct{}) {
		r.waitErr = cmd.Wait()
		close(exited)
	}(r.exited)
	r.waitFor(t, "the line "+r.readyLine, func() bool { return strings.Contains(r.out.String(), r.readyLine) })
	r.waitFor(t, "Muster to be ready at "+r.Internal+"/health/ready", func() bool {
		return answers(r.owner.Context(), r.Internal+"/health/ready")
	})
}

// Stop interrupts the process and waits for it to exit; after stopTimeout it kills it.
func (r *Replica) Stop(t testing.TB) {
	t.Helper()
	if !r.Running() {
		return
	}
	_ = r.cmd.Process.Signal(os.Interrupt)
	select {
	case <-r.exited:
	case <-time.After(stopTimeout):
		t.Errorf("%s did not stop within %v after an interrupt; killing it", r.name(), stopTimeout)
		_ = r.cmd.Process.Kill()
		<-r.exited
	}
}

func (r *Replica) Restart(t testing.TB) {
	t.Helper()
	r.Stop(t)
	r.Start(t)
}

func (r *Replica) Running() bool {
	if r.exited == nil {
		return false
	}
	select {
	case <-r.exited:
		return false
	default:
		return true
	}
}

// PID is the process id of the current run.
func (r *Replica) PID() int {
	if r.cmd == nil || r.cmd.Process == nil {
		return 0
	}
	return r.cmd.Process.Pid
}

// Output is what the current run printed.
func (r *Replica) Output() string {
	if r.out == nil {
		return ""
	}
	return r.out.String()
}

func (r *Replica) allOutput() string {
	return r.earlier.String() + r.Output()
}

// waitFor polls cond until it holds and fails t when readyTimeout passes first or the process has exited, even with
// cond holding: a stale process on the same ports must not stand in for it.
func (r *Replica) waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(readyTimeout)
	for {
		held := cond()
		select {
		case <-r.exited:
			t.Fatalf("%s exited (%v) while waiting for %s; output:\n%s", r.name(), r.waitErr, what, r.Output())
		default:
		}
		if held {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited %v for %s; output of %s:\n%s", readyTimeout, what, r.name(), r.Output())
		}
		select {
		case <-r.exited:
		case <-time.After(pollInterval):
		}
	}
}

func (h *Harness) databaseEnv() []string {
	return []string{"MUSTER_DATABASE_URL=" + h.DatabaseURL}
}

// freshDatabase creates an empty database on the development PostgreSQL, or on the server of MUSTER_E2E_DATABASE_URL,
// and drops it when the test ends; it returns the database's URL.
func freshDatabase(t *testing.T) string {
	t.Helper()
	admin := cmp.Or(os.Getenv("MUSTER_E2E_DATABASE_URL"), devmode.DatabaseURL)
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("MUSTER_E2E_DATABASE_URL: %v", err)
	}
	name := "muster_e2e_" + strings.ToLower(rand.Text()[:12])
	exec := func(ctx context.Context, sql string) error {
		conn, err := pgx.Connect(ctx, admin)
		if err != nil {
			return fmt.Errorf("connect to the PostgreSQL of the end-to-end suite (start it with make dev-db): %w", err)
		}
		defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
		_, err = conn.Exec(ctx, sql)
		return err
	}
	if err := exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create the database %s: %v", name, err)
	}
	// Registered first, so it runs after the cleanups that stop the replicas.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		if err := exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop the database %s: %v", name, err)
		}
	})
	u.Path = "/" + name
	return u.String()
}

func binary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("MUSTER_E2E_BINARY")
	if path == "" {
		t.Fatal("MUSTER_E2E_BINARY is not set: run the end-to-end suite with make e2e, which builds the binary and sets it")
	}
	fi, err := os.Stat(path) //nolint:gosec // G703: the binary under test, named by make e2e
	if err != nil || fi.IsDir() {
		t.Fatalf("MUSTER_E2E_BINARY=%s is not a binary (%v): run the end-to-end suite with make e2e", path, err)
	}
	return path
}

func replicaCount(t *testing.T) int {
	t.Helper()
	switch v := os.Getenv("E2E_REPLICAS"); v {
	case "", "1":
		return 1
	case "2":
		return 2
	default:
		t.Fatalf("E2E_REPLICAS=%q: want 1 or 2", v)
		return 0
	}
}

// baseURL turns a listen address such as :9080 into http://127.0.0.1:9080.
func baseURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

var pollClient = &http.Client{Timeout: 2 * time.Second}

func answers(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := pollClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Metric is the value of the series name, such as muster_leader, on the replica's /metrics; empty when it is absent.
func (r *Replica) Metric(t testing.TB, name string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, r.Internal+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := pollClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), name+" "); ok {
			return value
		}
	}
	return ""
}

// LogLine is the first log line of the event in the current run's output, and the time it carries.
func (r *Replica) LogLine(event string) (map[string]any, time.Time, bool) {
	for line := range strings.Lines(r.Output()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil || m["event"] != event {
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(m["time"]))
		return m, at, true
	}
	return nil, time.Time{}, false
}

// Blackhole is a TCP proxy to the harness's database that a test can cut: after Cut it forwards nothing more in
// either direction and connects no new connection, while every connection stays open, like a network that drops
// every packet.
type Blackhole struct {
	ln     net.Listener
	target string
	cut    atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
}

// Blackhole starts a proxy to the database of the harness; Close, or the end of the test, stops it.
func (h *Harness) Blackhole(t testing.TB) *Blackhole {
	t.Helper()
	u, err := url.Parse(h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &Blackhole{ln: ln, target: u.Host}
	t.Cleanup(b.Close)
	go b.accept(t.Context())
	return b
}

// DatabaseURL is the harness's database reached through the proxy.
func (b *Blackhole) DatabaseURL(h *Harness) string {
	u, _ := url.Parse(h.DatabaseURL) // parsed by Blackhole
	u.Host = b.ln.Addr().String()
	return u.String()
}

// Cut stops forwarding.
func (b *Blackhole) Cut() { b.cut.Store(true) }

// Close stops the proxy and closes every connection, so that what used it fails at once.
func (b *Blackhole) Close() {
	_ = b.ln.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.conns {
		_ = c.Close()
	}
	b.conns = nil
}

func (b *Blackhole) accept(ctx context.Context) {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.track(c)
		if b.cut.Load() {
			continue
		}
		var d net.Dialer
		up, err := d.DialContext(ctx, "tcp", b.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		b.track(up)
		go b.pipe(up, c)
		go b.pipe(c, up)
	}
}

func (b *Blackhole) track(c net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.conns = append(b.conns, c)
}

// pipe copies src to dst until src fails; after the cut it drops what it reads and closes nothing.
func (b *Blackhole) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if err != nil {
			if !b.cut.Load() {
				_ = dst.Close()
			}
			return
		}
		if !b.cut.Load() {
			if _, err := dst.Write(buf[:n]); err != nil {
				return
			}
		}
	}
}
