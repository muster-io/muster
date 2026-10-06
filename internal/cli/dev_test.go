// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buildinfo"
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

var anyPort = devmode.Addresses{Alertmanager: "127.0.0.1:0", Mattermost: "127.0.0.1:0", Telegram: "127.0.0.1:0",
	OIDC: "127.0.0.1:0", HTTPProxy: "127.0.0.1:0", SOCKSProxy: "127.0.0.1:0"}

// started runs `muster dev args` in the background with env and the fakes at addrs; stop interrupts it like a signal
// and returns its exit code.
func started(t *testing.T, env devmode.Env, addrs devmode.Addresses, args ...string) (stdout, stderr *syncBuffer,
	stop func() int,
) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	d := devMode{
		env: env, fakes: addrs, notify: func() (context.Context, context.CancelFunc) { return ctx, cancel },
		server: func(ctx context.Context, _ io.Writer) error {
			<-ctx.Done()
			return nil
		},
	}
	stdout, stderr = &syncBuffer{}, &syncBuffer{}
	code := make(chan int, 1)
	go func() { code <- d.run(args, stdout, stderr) }()
	stop = func() int {
		cancel()
		select {
		case c := <-code:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("muster dev did not return after the interrupt")
			return -1
		}
	}
	return stdout, stderr, stop
}

func waitLines(t *testing.T, out *syncBuffer, lines int) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for strings.Count(out.String(), "\n") < lines {
		if time.Now().After(deadline) {
			t.Fatalf("output after 10s: %q", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	return out.String()
}

func TestDevFakeServers(t *testing.T) {
	env := mapEnv{"MUSTER_SECRET_KEYS": "a2V5"}
	stdout, stderr, stop := started(t, env, anyPort)
	got := waitLines(t, stdout, 8)
	re := regexp.MustCompile(`^muster dev: development defaults for MUSTER_PUBLIC_URL, MUSTER_DATABASE_URL, ` +
		`MUSTER_BOOTSTRAP_ADMIN_EMAIL, MUSTER_BOOTSTRAP_ADMIN_PASSWORD\n` +
		`muster dev: from the environment: MUSTER_SECRET_KEYS\n` +
		`muster dev: fake Alertmanager http://127\.0\.0\.1:\d+\n` +
		`muster dev: fake Mattermost http://127\.0\.0\.1:\d+\n` +
		`muster dev: fake OIDC http://127\.0\.0\.1:\d+\n` +
		`muster dev: fake HTTP proxy 127\.0\.0\.1:\d+\n` +
		`muster dev: fake SOCKS5 proxy 127\.0\.0\.1:\d+\n` +
		`muster dev: fake Telegram (http://127\.0\.0\.1:\d+)\n$`)
	m := re.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("stdout %q", got)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, m[1]+"/bot1:x/getMe", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("getMe = %d", resp.StatusCode)
	}
	if code := stop(); code != exitOK {
		t.Errorf("exit code %d after the interrupt, stderr %q", code, stderr.String())
	}
	if env["MUSTER_SECRET_KEYS"] != "a2V5" || env["MUSTER_DATABASE_URL"] != devmode.DatabaseURL {
		t.Errorf("environment after muster dev: %v", env)
	}
	if strings.Contains(stdout.String(), "a2V5") || strings.Contains(stdout.String(), devmode.AdminPassword) {
		t.Error("muster dev printed a value")
	}
}

func TestDevStopsDuringALoadRun(t *testing.T) {
	var received atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer sink.Close()
	stdout, stderr, stop := started(t, mapEnv{}, anyPort)
	m := regexp.MustCompile(`muster dev: fake Alertmanager (http://\S+)\n`).FindStringSubmatch(waitLines(t, stdout, 4))
	if m == nil {
		t.Fatalf("stdout %q", stdout.String())
	}
	loaded := make(chan error, 1)
	go func() {
		body := `{"url":"` + sink.URL + `","rate_per_second":20,"duration_seconds":600}`
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, m[1]+"/_fake/load", strings.NewReader(body))
		if err == nil {
			var resp *http.Response
			if resp, err = http.DefaultClient.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
		loaded <- err
	}()
	for received.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	begin := time.Now()
	if code := stop(); code != exitOK {
		t.Errorf("exit code %d after the interrupt during a load run, stderr %q", code, stderr.String())
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("muster dev took %v to stop during a load run", elapsed)
	}
	<-loaded
}

func TestDevReplica(t *testing.T) {
	env := mapEnv{"MUSTER_LISTEN_APP": ":9180"}
	stdout, stderr, stop := started(t, env, anyPort, "--replica")
	got := waitLines(t, stdout, 3)
	want := "muster dev: development defaults for MUSTER_PUBLIC_URL, MUSTER_DATABASE_URL, " +
		"MUSTER_SECRET_KEYS (the published development key), MUSTER_BOOTSTRAP_ADMIN_EMAIL, " +
		"MUSTER_BOOTSTRAP_ADMIN_PASSWORD, MUSTER_LISTEN_INGEST, MUSTER_LISTEN_INTERNAL\n" +
		"muster dev: from the environment: MUSTER_LISTEN_APP\n" +
		"muster dev: additional replica, no fake servers\n"
	if got != want {
		t.Errorf("stdout\n%q\nwant\n%q", got, want)
	}
	if code := stop(); code != exitOK || stderr.String() != "" {
		t.Errorf("exit code %d, stderr %q", code, stderr.String())
	}
	if env["MUSTER_LISTEN_APP"] != ":9180" || env["MUSTER_LISTEN_INGEST"] != ":9081" ||
		env["MUSTER_LISTEN_INTERNAL"] != ":9082" {
		t.Errorf("listen addresses %v", env)
	}
}

func TestDevBusyPort(t *testing.T) {
	var lc net.ListenConfig
	busy, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	addrs := anyPort
	addrs.Mattermost = busy.Addr().String()
	stdout, stderr, stop := started(t, mapEnv{}, addrs)
	if code := stop(); code != exitFailure {
		t.Errorf("exit code %d, want 1", code)
	}
	want := "muster dev: start the fake Mattermost on " + busy.Addr().String() + ": "
	if !strings.HasPrefix(stderr.String(), want) {
		t.Errorf("stderr %q, want it to start with %q", stderr.String(), want)
	}
	if strings.Contains(stdout.String(), "fake") {
		t.Errorf("stdout %q names fake servers that did not start", stdout.String())
	}
}

func TestDevEnvironmentError(t *testing.T) {
	for _, args := range [][]string{nil, {"--replica"}, {"version"}} {
		var stdout, stderr bytes.Buffer
		d := devMode{env: failingEnv{mapEnv{}}, fakes: anyPort}
		if code := d.run(args, &stdout, &stderr); code != exitFailure ||
			!strings.HasPrefix(stderr.String(), "muster dev: set MUSTER_PUBLIC_URL: ") {
			t.Errorf("muster dev %q = %d, stderr %q", args, code, stderr.String())
		}
	}
}

func TestDevUsage(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "short help", args: []string{"dev", "-h"}, wantStdout: devUsage},
		{name: "long help", args: []string{"dev", "--help"}, wantStdout: devUsage},
		{
			name:       "dev in dev",
			args:       []string{"dev", "dev"},
			wantCode:   exitUsage,
			wantStderr: "muster dev: dev cannot run dev\n\n" + devUsage,
		},
		{
			name:       "replica with arguments",
			args:       []string{"dev", "--replica", "version"},
			wantCode:   exitUsage,
			wantStderr: "muster dev: --replica takes no arguments\n\n" + devUsage,
		},
		{
			name:       "unknown flag",
			args:       []string{"dev", "--foo"},
			wantCode:   exitUsage,
			wantStderr: "muster dev: unknown flag \"--foo\"\n\n" + devUsage,
		},
		{
			name:       "unknown command",
			args:       []string{"dev", "frobnicate"},
			wantCode:   exitUsage,
			wantStderr: "muster: unknown command \"frobnicate\"\n\n" + usage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearDevEnv(t)
			var stdout, stderr bytes.Buffer
			if code := Run(tt.args, &stdout, &stderr); code != tt.wantCode {
				t.Errorf("exit code %d, want %d", code, tt.wantCode)
			}
			if stdout.String() != tt.wantStdout || stderr.String() != tt.wantStderr {
				t.Errorf("stdout %q, stderr %q\nwant stdout %q, stderr %q", stdout.String(), stderr.String(),
					tt.wantStdout, tt.wantStderr)
			}
		})
	}
}

// devVariables are the variables muster dev reads or sets; clearDevEnv unsets them for one test and restores them
// afterwards.
var devVariables = []string{
	"MUSTER_PUBLIC_URL", "MUSTER_DATABASE_URL", "MUSTER_DATABASE_HOST", "MUSTER_DATABASE_PORT", "MUSTER_DATABASE_NAME",
	"MUSTER_DATABASE_USER", "MUSTER_DATABASE_PASSWORD", "MUSTER_DATABASE_PASSWORD_FILE", "MUSTER_DATABASE_SSLMODE",
	"MUSTER_SECRET_KEYS", "MUSTER_SECRET_KEYS_FILE", "MUSTER_BOOTSTRAP_ADMIN_EMAIL", "MUSTER_BOOTSTRAP_ADMIN_PASSWORD",
	"MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE", "MUSTER_LISTEN_APP", "MUSTER_LISTEN_INGEST", "MUSTER_LISTEN_INTERNAL",
}

func clearDevEnv(t *testing.T) {
	t.Helper()
	for _, name := range devVariables {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDevSubcommand(t *testing.T) {
	origVersion, origCommit := buildinfo.Version, buildinfo.Commit
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = origVersion, origCommit })
	buildinfo.Version, buildinfo.Commit = "1.2.3", "0123456789ab"

	tests := []struct {
		name      string
		env       map[string]string
		wantEnv   map[string]string
		wantUnset []string
	}{
		{
			name: "development defaults",
			wantEnv: map[string]string{
				"MUSTER_PUBLIC_URL":               devmode.PublicURL,
				"MUSTER_DATABASE_URL":             devmode.DatabaseURL,
				"MUSTER_SECRET_KEYS":              devmode.DevelopmentKey,
				"MUSTER_BOOTSTRAP_ADMIN_EMAIL":    devmode.AdminEmail,
				"MUSTER_BOOTSTRAP_ADMIN_PASSWORD": devmode.AdminPassword,
			},
			wantUnset: []string{"MUSTER_LISTEN_APP", "MUSTER_LISTEN_INGEST", "MUSTER_LISTEN_INTERNAL"},
		},
		{
			name:      "a set key is the whole Keyring",
			env:       map[string]string{"MUSTER_SECRET_KEYS": "a2V5LW9uZQ==,a2V5LXR3bw=="},
			wantEnv:   map[string]string{"MUSTER_SECRET_KEYS": "a2V5LW9uZQ==,a2V5LXR3bw=="},
			wantUnset: nil,
		},
		{
			name:      "database fields replace the URL",
			env:       map[string]string{"MUSTER_DATABASE_HOST": "db.test", "MUSTER_DATABASE_PASSWORD_FILE": "/run/pw"},
			wantEnv:   map[string]string{"MUSTER_DATABASE_HOST": "db.test", "MUSTER_SECRET_KEYS": devmode.DevelopmentKey},
			wantUnset: []string{"MUSTER_DATABASE_URL"},
		},
		{
			name: "key and password files",
			env: map[string]string{
				"MUSTER_SECRET_KEYS_FILE":              "/run/keys",
				"MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE": "/run/pw",
			},
			wantEnv:   map[string]string{"MUSTER_BOOTSTRAP_ADMIN_EMAIL": devmode.AdminEmail},
			wantUnset: []string{"MUSTER_SECRET_KEYS", "MUSTER_BOOTSTRAP_ADMIN_PASSWORD"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearDevEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			var stdout, stderr bytes.Buffer
			if code := Run([]string{"dev", "version"}, &stdout, &stderr); code != exitOK {
				t.Fatalf("exit code %d, stderr %q", code, stderr.String())
			}
			if stdout.String() != "muster 1.2.3 (commit 0123456789ab)\n" || stderr.String() != "" {
				t.Errorf("stdout %q, stderr %q, want only the version line", stdout.String(), stderr.String())
			}
			for name, want := range tt.wantEnv {
				if got, ok := os.LookupEnv(name); !ok || got != want {
					t.Errorf("%s = %q (set %v), want %q", name, got, ok, want)
				}
			}
			for _, name := range tt.wantUnset {
				if got, ok := os.LookupEnv(name); ok {
					t.Errorf("%s = %q, want it unset", name, got)
				}
			}
		})
	}

	t.Run("the subcommand keeps its refusals", func(t *testing.T) {
		clearDevEnv(t)
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"dev", "version", "extra"}, &stdout, &stderr); code != exitUsage ||
			stderr.String() != "muster: version takes no arguments\n\n"+usage || stdout.Len() != 0 {
			t.Errorf("exit code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
		}
	})
}

func TestPrintApplied(t *testing.T) {
	var out bytes.Buffer
	printApplied(&out, devmode.Applied{FromEnv: []string{"MUSTER_PUBLIC_URL"}})
	if out.String() != "muster dev: from the environment: MUSTER_PUBLIC_URL\n" {
		t.Errorf("only from the environment: %q", out.String())
	}
	out.Reset()
	printApplied(&out, devmode.Applied{})
	if out.Len() != 0 {
		t.Errorf("nothing applied: %q", out.String())
	}
}
