// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func stub(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, name+" "+r.URL.Path) //nolint:gosec // G705: a plain-text test stub echoing its path
	})
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

func start(t *testing.T, addrs Addresses, h Handlers) *Server {
	t.Helper()
	s, err := Start(t.Context(), addrs, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.WithoutCancel(t.Context())) })
	return s
}

func stubs() Handlers {
	return Handlers{App: stub("app"), Ingest: stub("ingest"), Internal: stub("internal")}
}

func TestIsIngestPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/api/v1/ingest":                       true,
		"/api/v1/ingest/tok3n":                 true,
		"/api/v1/heartbeat":                    true,
		"/api/v1/heartbeat/tok3n":              true,
		"/api/v1/callbacks/mattermost/CN1":     true,
		"/api/v1/callbacks/telegram/CN1":       true,
		"/api/v1/callbacks":                    false,
		"/api/v1/ingestion":                    false,
		"/api/v1/sessions/oidc/callback":       false,
		"/api/v1/alert-groups":                 false,
		"/":                                    false,
		"/integrations/ingest":                 false,
		"/api/v1/me/oidc-identity/callback":    false,
		"/api/v1/heartbeats":                   false,
		"/api/v1/callbacks/mattermost/CN1/x/y": true,
	} {
		if got := IsIngestPath(p); got != want {
			t.Errorf("IsIngestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestMergedListeners is C-02.AC-8 with stub handlers: with ingest and app on the same address, one port serves the
// ingest paths from the ingest handler and every other path from the app handler.
func TestMergedListeners(t *testing.T) {
	s := start(t, Addresses{App: "127.0.0.1:0", Ingest: "127.0.0.1:0", Internal: "127.0.0.1:0"}, stubs())
	a := s.Addrs()
	if a.App != a.Ingest || a.App == a.Internal || len(s.listeners) != 2 {
		t.Fatalf("addresses %+v with %d listeners, want app and ingest on one port", a, len(s.listeners))
	}
	for p, want := range map[string]string{
		"/api/v1/ingest":                   "ingest /api/v1/ingest",
		"/api/v1/heartbeat/tok3n":          "ingest /api/v1/heartbeat/tok3n",
		"/api/v1/callbacks/telegram/CN1":   "ingest /api/v1/callbacks/telegram/CN1",
		"/api/v1/alert-groups":             "app /api/v1/alert-groups",
		"/":                                "app /",
		"/api/v1/sessions/oidc/callback":   "app /api/v1/sessions/oidc/callback",
		"/alert-groups/AG0000000000AA/foo": "app /alert-groups/AG0000000000AA/foo",
	} {
		if code, body := get(t, "http://"+a.App+p); code != http.StatusOK || body != want {
			t.Errorf("GET %s = %d %q, want %q", p, code, body, want)
		}
	}
	if _, body := get(t, "http://"+a.Internal+"/api/v1/ingest"); body != "internal /api/v1/ingest" {
		t.Errorf("the internal listener answered %q", body)
	}
}

func TestSeparateListeners(t *testing.T) {
	s := start(t, Addresses{App: "127.0.0.1:0", Ingest: "localhost:0", Internal: "127.0.0.1:0"}, stubs())
	a := s.Addrs()
	if a.App == a.Ingest || len(s.listeners) != 3 {
		t.Fatalf("addresses %+v", a)
	}
	if _, body := get(t, "http://"+a.Ingest+"/api/v1/alert-groups"); body != "ingest /api/v1/alert-groups" {
		t.Errorf("the ingest listener answered %q", body)
	}
	if _, body := get(t, "http://"+a.App+"/api/v1/ingest"); body != "app /api/v1/ingest" {
		t.Errorf("the app listener answered %q", body)
	}
}

func TestListenFailure(t *testing.T) {
	var lc net.ListenConfig
	busy, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	_, err = Start(t.Context(), Addresses{App: "127.0.0.1:0", Ingest: "127.0.0.1:0", Internal: busy.Addr().String()}, stubs())
	if err == nil || !strings.HasPrefix(err.Error(), "listen on "+busy.Addr().String()+" (MUSTER_LISTEN_INTERNAL): ") {
		t.Errorf("got %v", err)
	}
}

func TestListenerError(t *testing.T) {
	s := start(t, Addresses{App: "127.0.0.1:0", Ingest: "127.0.0.1:0", Internal: "127.0.0.1:0"}, stubs())
	_ = s.listeners[1].ln.Close()
	select {
	case err := <-s.Errors():
		var le *ListenerError
		if !errors.As(err, &le) || le.Listener != ListenerInternal || errors.Unwrap(err) == nil ||
			!strings.HasPrefix(err.Error(), "the internal listener stopped: ") {
			t.Errorf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no listener error")
	}
}

func TestShutdownDrains(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	s, err := Start(t.Context(), Addresses{App: "127.0.0.1:0", Ingest: "127.0.0.1:0", Internal: "127.0.0.1:0"},
		Handlers{App: slow, Ingest: stub("ingest"), Internal: stub("internal")})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		_, body := get(t, "http://"+s.Addrs().App+"/")
		got <- body
	}()
	<-started
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(t.Context()) }()
	time.Sleep(50 * time.Millisecond)
	if code, body := get(t, "http://"+s.Addrs().Internal+"/health/ready"); code != http.StatusOK ||
		body != "internal /health/ready" {
		t.Errorf("the internal listener during the drain: %d %q", code, body)
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("Shutdown = %v", err)
	}
	if body := <-got; body != "done" {
		t.Errorf("the request in progress got %q", body)
	}
	var d net.Dialer
	if conn, err := d.DialContext(t.Context(), "tcp", s.Addrs().App); err == nil {
		_ = conn.Close()
		t.Error("the listener accepts connections after Shutdown")
	}
}

func TestShutdownGraceExceeded(t *testing.T) {
	started := make(chan struct{})
	stuck := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	s, err := Start(t.Context(), Addresses{App: "127.0.0.1:0", Ingest: "127.0.0.1:0", Internal: "127.0.0.1:0"},
		Handlers{App: stuck, Ingest: stub("ingest"), Internal: stub("internal")})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+s.Addrs().App+"/", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if err := s.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want the deadline", err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("Shutdown took %v", elapsed)
	}
}

func TestHealth(t *testing.T) {
	var dbErr error
	h := NewHealth(func(context.Context) error { return dbErr })
	s := start(t, Addresses{App: "127.0.0.1:0", Ingest: "127.0.0.1:0", Internal: "127.0.0.1:0"},
		Handlers{App: stub("app"), Ingest: stub("ingest"), Internal: Internal(h, stub("metrics"))})
	internal := "http://" + s.Addrs().Internal
	check := func(path string, wantCode int, wantBody string) {
		t.Helper()
		if code, body := get(t, internal+path); code != wantCode || body != wantBody {
			t.Errorf("GET %s = %d %q, want %d %q", path, code, body, wantCode, wantBody)
		}
	}
	check("/health/live", http.StatusOK, "ok\n")
	check("/health/ready", http.StatusOK, "ok\n")
	check("/metrics", http.StatusOK, "metrics /metrics")
	check("/health/other", http.StatusNotFound, "404 page not found\n")

	dbErr = errors.New("connection refused")
	check("/health/ready", http.StatusServiceUnavailable, "database unavailable\n")
	check("/health/live", http.StatusOK, "ok\n")

	dbErr = nil
	h.ShuttingDown()
	check("/health/ready", http.StatusServiceUnavailable, "shutting down\n")
	check("/health/live", http.StatusOK, "ok\n")

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, internal+"/health/live", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /health/live = %d, want 405", resp.StatusCode)
	}
}

func TestSPA(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":       {Data: []byte("<html>muster</html>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/dir/x.txt": {Data: []byte("x")},
	}
	s := start(t, Addresses{App: "127.0.0.1:0", Ingest: "127.0.0.1:0", Internal: "127.0.0.1:0"},
		Handlers{App: SPA(dist), Ingest: stub("ingest"), Internal: stub("internal")})
	app := "http://" + s.Addrs().App
	for p, want := range map[string]string{
		"/":                       "<html>muster</html>",
		"/index.html":             "<html>muster</html>",
		"/alert-groups/AG1":       "<html>muster</html>",
		"/assets":                 "<html>muster</html>",
		"/assets/app-1.js":        "console.log(1)",
		"/favicon.svg":            "<svg/>",
		"/../../etc/passwd":       "<html>muster</html>",
		"/assets/dir/x.txt":       "x",
		"/assets/missing-file.js": "<html>muster</html>",
	} {
		if code, body := get(t, app+p); code != http.StatusOK || body != want {
			t.Errorf("GET %s = %d %q, want %q", p, code, body, want)
		}
	}

	empty := start(t, Addresses{App: "127.0.0.1:0", Ingest: "127.0.0.1:0", Internal: "127.0.0.1:0"},
		Handlers{App: SPA(fstest.MapFS{".gitkeep": {}}), Ingest: stub("ingest"), Internal: stub("internal")})
	if code, body := get(t, "http://"+empty.Addrs().App+"/"); code != http.StatusServiceUnavailable ||
		body != "the web interface is not built\n" {
		t.Errorf("an unbuilt SPA: %d %q", code, body)
	}
}

// TestApp is C-03.FR-16: the app listener sends /api/ paths to the API and every other path to the SPA, and every
// answer carries the Content Security Policy and the security headers; Strict-Transport-Security only for https.
func TestApp(t *testing.T) {
	dist := fstest.MapFS{"index.html": {Data: []byte("<html>index</html>")}}
	for _, hsts := range []bool{false, true} {
		h := App(stub("api"), dist, hsts)
		for path, want := range map[string]string{
			"/":                   "<html>index</html>",
			"/alert-groups/AG1":   "<html>index</html>",
			"/api/v1/nothing":     "api /api/v1/nothing",
			"/api":                "api /api",
			"/apiary":             "<html>index</html>",
			"/api/v1/sign-in-opt": "api /api/v1/sign-in-opt",
		} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
			if w.Body.String() != want {
				t.Errorf("%s = %q, want %q", path, w.Body.String(), want)
			}
			for name, value := range map[string]string{
				"Content-Security-Policy": ContentSecurityPolicy, "X-Content-Type-Options": "nosniff",
				"Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY",
				"Cross-Origin-Opener-Policy": "same-origin",
			} {
				if got := w.Header().Get(name); got != value {
					t.Errorf("%s: %s = %q", path, name, got)
				}
			}
			if got := w.Header().Get("Strict-Transport-Security"); (got != "") != hsts {
				t.Errorf("hsts %v: Strict-Transport-Security = %q", hsts, got)
			}
		}
	}
	if !strings.Contains(ContentSecurityPolicy, "frame-ancestors 'none'") ||
		!strings.HasPrefix(ContentSecurityPolicy, "default-src 'self'; script-src 'self';") {
		t.Errorf("CSP = %s", ContentSecurityPolicy)
	}
}
