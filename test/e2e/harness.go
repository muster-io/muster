// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

// Package e2e is the end-to-end suite: it runs the muster binary in development mode and drives it over HTTP. make e2e
// builds the binary and runs the suite with MUSTER_E2E_BINARY pointing to it; E2E_REPLICAS=2 runs two replicas.
package e2e

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

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
	first := h.newReplica([]string{"dev"}, nil, ":8080", ":8081", ":8082", fakesReadyLine)
	first.Start(h.t)
	// Muster itself joins this wait, through /health/ready, once the server command exists.
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
		second := h.newReplica([]string{"dev", "--replica"}, nil, devmode.ReplicaListenApp, devmode.ReplicaListenIngest,
			devmode.ReplicaListenInternal, devmode.ReplicaLine)
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

// StartReplica starts a `muster dev --replica` child process in the mode FakesInProcess and waits until it is up; the
// process lives until the test that started the harness ends. A fresh database per replica joins it once Muster runs
// on one.
func (h *Harness) StartReplica(t testing.TB, opts ReplicaOptions) *Replica {
	t.Helper()
	if h.InProcess == nil {
		t.Fatal("StartReplica needs the mode FakesInProcess; in DevProcess, E2E_REPLICAS sets the replicas")
	}
	app := cmp.Or(opts.ListenApp, devmode.ReplicaListenApp)
	ingest := cmp.Or(opts.ListenIngest, devmode.ReplicaListenIngest)
	internal := cmp.Or(opts.ListenInternal, devmode.ReplicaListenInternal)
	env := make([]string, 0, len(opts.Env)+3)
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
