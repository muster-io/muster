// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package devmode_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

type mapEnv map[string]string

func (e mapEnv) LookupEnv(name string) (string, bool) {
	v, ok := e[name]
	return v, ok
}

func (e mapEnv) Setenv(name, value string) error {
	e[name] = value
	return nil
}

type failingEnv struct{ mapEnv }

func (failingEnv) Setenv(string, string) error { return errors.New("read-only") }

func TestDevelopmentKey(t *testing.T) {
	key, err := base64.StdEncoding.DecodeString(devmode.DevelopmentKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != "muster-dev-only-key-not-a-secret" || len(key) != 32 {
		t.Errorf("the development key decodes to %q (%d bytes)", key, len(key))
	}
}

var defaults = map[string]string{
	"MUSTER_PUBLIC_URL":               "http://localhost:8080",
	"MUSTER_DATABASE_URL":             "postgres://muster:muster@127.0.0.1:55432/muster?sslmode=disable",
	"MUSTER_SECRET_KEYS":              devmode.DevelopmentKey,
	"MUSTER_BOOTSTRAP_ADMIN_EMAIL":    "admin@example.org",
	"MUSTER_BOOTSTRAP_ADMIN_PASSWORD": "muster-dev-password",
}

var allNames = []string{
	"MUSTER_PUBLIC_URL", "MUSTER_DATABASE_URL", "MUSTER_SECRET_KEYS", "MUSTER_BOOTSTRAP_ADMIN_EMAIL",
	"MUSTER_BOOTSTRAP_ADMIN_PASSWORD",
}

func without(names []string, drop ...string) []string {
	return slices.DeleteFunc(slices.Clone(names), func(n string) bool { return slices.Contains(drop, n) })
}

func TestApply(t *testing.T) {
	replicaNames := append(slices.Clone(allNames), "MUSTER_LISTEN_APP", "MUSTER_LISTEN_INGEST", "MUSTER_LISTEN_INTERNAL")
	tests := []struct {
		name         string
		env          mapEnv
		replica      bool
		want         map[string]string
		wantDefaults []string
		wantFromEnv  []string
	}{
		{
			name:         "empty environment",
			env:          mapEnv{},
			want:         defaults,
			wantDefaults: allNames,
		},
		{
			name: "set variables replace their defaults",
			env: mapEnv{
				"MUSTER_PUBLIC_URL":  "http://muster.test:8080",
				"MUSTER_SECRET_KEYS": "a2V5LW9uZQ==",
			},
			want: map[string]string{
				"MUSTER_PUBLIC_URL":   "http://muster.test:8080",
				"MUSTER_SECRET_KEYS":  "a2V5LW9uZQ==",
				"MUSTER_DATABASE_URL": defaults["MUSTER_DATABASE_URL"],
			},
			wantDefaults: without(allNames, "MUSTER_PUBLIC_URL", "MUSTER_SECRET_KEYS"),
			wantFromEnv:  []string{"MUSTER_PUBLIC_URL", "MUSTER_SECRET_KEYS"},
		},
		{
			name:         "an empty variable counts as set",
			env:          mapEnv{"MUSTER_BOOTSTRAP_ADMIN_PASSWORD": ""},
			want:         map[string]string{"MUSTER_BOOTSTRAP_ADMIN_PASSWORD": ""},
			wantDefaults: without(allNames, "MUSTER_BOOTSTRAP_ADMIN_PASSWORD"),
			wantFromEnv:  []string{"MUSTER_BOOTSTRAP_ADMIN_PASSWORD"},
		},
		{
			name:         "the database URL",
			env:          mapEnv{"MUSTER_DATABASE_URL": "postgres://elsewhere/muster"},
			want:         map[string]string{"MUSTER_DATABASE_URL": "postgres://elsewhere/muster"},
			wantDefaults: without(allNames, "MUSTER_DATABASE_URL"),
			wantFromEnv:  []string{"MUSTER_DATABASE_URL"},
		},
		{
			name:         "a database field keeps the URL default out",
			env:          mapEnv{"MUSTER_DATABASE_HOST": "db.test", "MUSTER_DATABASE_PASSWORD_FILE": "/run/pw"},
			want:         map[string]string{"MUSTER_DATABASE_HOST": "db.test"},
			wantDefaults: without(allNames, "MUSTER_DATABASE_URL"),
			wantFromEnv:  []string{"MUSTER_DATABASE_HOST", "MUSTER_DATABASE_PASSWORD_FILE"},
		},
		{
			name:         "an empty database field counts as set",
			env:          mapEnv{"MUSTER_DATABASE_SSLMODE": ""},
			wantDefaults: without(allNames, "MUSTER_DATABASE_URL"),
			wantFromEnv:  []string{"MUSTER_DATABASE_SSLMODE"},
		},
		{
			name:         "the key file",
			env:          mapEnv{"MUSTER_SECRET_KEYS_FILE": "/run/keys"},
			wantDefaults: without(allNames, "MUSTER_SECRET_KEYS"),
			wantFromEnv:  []string{"MUSTER_SECRET_KEYS_FILE"},
		},
		{
			name:         "the Admin password file",
			env:          mapEnv{"MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE": "/run/admin"},
			wantDefaults: without(allNames, "MUSTER_BOOTSTRAP_ADMIN_PASSWORD"),
			wantFromEnv:  []string{"MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE"},
		},
		{
			name:    "replica listen defaults",
			env:     mapEnv{},
			replica: true,
			want: map[string]string{
				"MUSTER_LISTEN_APP":      ":9080",
				"MUSTER_LISTEN_INGEST":   ":9081",
				"MUSTER_LISTEN_INTERNAL": ":9082",
				"MUSTER_SECRET_KEYS":     devmode.DevelopmentKey,
			},
			wantDefaults: replicaNames,
		},
		{
			name:    "replica listen overrides",
			env:     mapEnv{"MUSTER_LISTEN_INGEST": "127.0.0.1:9181"},
			replica: true,
			want: map[string]string{
				"MUSTER_LISTEN_APP":    ":9080",
				"MUSTER_LISTEN_INGEST": "127.0.0.1:9181",
			},
			wantDefaults: without(replicaNames, "MUSTER_LISTEN_INGEST"),
			wantFromEnv:  []string{"MUSTER_LISTEN_INGEST"},
		},
		{
			name:         "no listen defaults without replica",
			env:          mapEnv{},
			wantDefaults: allNames,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := devmode.Apply(tt.env, tt.replica)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(a.Defaults, tt.wantDefaults) {
				t.Errorf("Defaults = %v, want %v", a.Defaults, tt.wantDefaults)
			}
			if !slices.Equal(a.FromEnv, tt.wantFromEnv) {
				t.Errorf("FromEnv = %v, want %v", a.FromEnv, tt.wantFromEnv)
			}
			for name, want := range tt.want {
				if got, ok := tt.env[name]; !ok || got != want {
					t.Errorf("%s = %q (set %v), want %q", name, got, ok, want)
				}
			}
			if _, set := tt.env["MUSTER_LISTEN_APP"]; set != tt.replica {
				t.Errorf("MUSTER_LISTEN_APP set = %v with replica %v", set, tt.replica)
			}
			if _, set := tt.env["MUSTER_DATABASE_HOST"]; set && tt.env["MUSTER_DATABASE_URL"] != "" {
				t.Error("the URL default was added next to a database field")
			}
		})
	}
}

func TestApplyError(t *testing.T) {
	_, err := devmode.Apply(failingEnv{mapEnv{}}, false)
	if err == nil || !strings.Contains(err.Error(), "MUSTER_PUBLIC_URL") {
		t.Errorf("Apply = %v, want an error naming the variable", err)
	}
}

func TestOSEnv(t *testing.T) {
	t.Setenv("MUSTER_DEVMODE_TEST", "")
	var env devmode.OSEnv
	if err := env.Setenv("MUSTER_DEVMODE_TEST", "value"); err != nil {
		t.Fatal(err)
	}
	if v, ok := env.LookupEnv("MUSTER_DEVMODE_TEST"); !ok || v != "value" {
		t.Errorf("LookupEnv = %q, %v", v, ok)
	}
}

// syncBuffer lets a test read what Run writes from another goroutine.
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

func waitFor(t *testing.T, out *syncBuffer, lines int, done <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for strings.Count(out.String(), "\n") < lines {
		select {
		case err := <-done:
			t.Fatalf("returned early: %v; output %q", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("output after 10s: %q", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	return out.String()
}

func get(t *testing.T, url string) int {
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
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

var anyPort = devmode.Addresses{Alertmanager: "127.0.0.1:0", Mattermost: "127.0.0.1:0", Telegram: "127.0.0.1:0"}

func TestRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- devmode.Run(ctx, out, anyPort) }()

	got := waitFor(t, out, 3, done)
	re := regexp.MustCompile(`^muster dev: fake Alertmanager (http://127\.0\.0\.1:\d+)\n` +
		`muster dev: fake Mattermost (http://127\.0\.0\.1:\d+)\n` +
		`muster dev: fake Telegram (http://127\.0\.0\.1:\d+)\n$`)
	m := re.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("output %q, want the three fake servers", got)
	}
	for _, base := range m[1:] {
		if status := get(t, base+"/_fake/requests"); status != http.StatusOK {
			t.Errorf("GET %s/_fake/requests = %d", base, status)
		}
	}
	if status := get(t, m[3]+"/bot1:x/getMe"); status != http.StatusOK {
		t.Errorf("Telegram getMe = %d", status)
	}
	if status := get(t, m[2]+"/api/v4/users/me"); status != http.StatusOK {
		t.Errorf("Mattermost users/me = %d", status)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil after the cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}
	for _, base := range m[1:] {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/_fake/requests", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			t.Errorf("%s still answers after Run returned", base)
		}
	}
}

func TestRunBusyPort(t *testing.T) {
	var lc net.ListenConfig
	busy, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	addrs := anyPort
	addrs.Telegram = busy.Addr().String()
	var out bytes.Buffer
	err = devmode.Run(t.Context(), &out, addrs)
	if err == nil || !strings.Contains(err.Error(), "start the fake Telegram on "+busy.Addr().String()) {
		t.Errorf("Run = %v, want an error naming the fake Telegram and its address", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q before failing", out.String())
	}
}

func TestStartFakes(t *testing.T) {
	f, err := devmode.StartFakes(t.Context(), anyPort)
	if err != nil {
		t.Fatal(err)
	}
	if get(t, f.Telegram.URL()+"/bot1:x/getMe") != http.StatusOK || len(f.Telegram.Requests()) != 1 {
		t.Error("the in-process Telegram fake did not record getMe")
	}
	if err := f.Close(t.Context()); err != nil {
		t.Errorf("Close: %v", err)
	}
	want := devmode.Addresses{Alertmanager: "127.0.0.1:19093", Mattermost: "127.0.0.1:18065", Telegram: "127.0.0.1:18081"}
	if devmode.FakeAddresses() != want {
		t.Errorf("FakeAddresses() = %+v, want %+v", devmode.FakeAddresses(), want)
	}
}

func TestRunReplica(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- devmode.RunReplica(ctx, out) }()
	if got := waitFor(t, out, 1, done); got != "muster dev: additional replica, no fake servers\n" {
		t.Errorf("output %q", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunReplica = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunReplica did not return after the cancel")
	}
}
