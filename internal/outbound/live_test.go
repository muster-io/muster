// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package outbound

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/logging"
)

// TestLive runs the package against real listeners, the system resolver and the fake proxies on loopback, with the
// real clock: the live check of the outbound package until its first consumer has an API.
func TestLive(t *testing.T) {
	var redirects, hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			redirects.Add(1)
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		hits.Add(1)
		w.Header().Set("X-Remote-Addr", r.RemoteAddr)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	standard := StaticPolicy(Policy{Mode: ModeStandard})
	loopback := StaticPolicy(mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, nil))

	client := func(t *testing.T, cfg Config) (*Client, *bytes.Buffer) {
		t.Helper()
		var log bytes.Buffer
		cfg.Logger = logging.New(&log, logging.LevelInfo)
		cfg.Clock = clock.Real{}
		cfg.ConnectTimeout, cfg.Timeout = 2*time.Second, 5*time.Second
		if cfg.Class == "" {
			cfg.Class = ClassDelivery
		}
		c, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.http.CloseIdleConnections)
		return c, &log
	}
	blocked := func(t *testing.T, res Result, err error, want string) {
		t.Helper()
		if res.Outcome != OutcomeBlocked || res.Attempts != 1 || err == nil || err.Error() != want {
			t.Fatalf("result %+v, err %v, want %q", res, err, want)
		}
		t.Log(err)
	}

	t.Run("metadata_blocked", func(t *testing.T) {
		c, log := client(t, Config{Class: ClassBackground, Policy: standard})
		res, err := c.Do(t.Context(), Request{URL: "http://169.254.169.254/latest/meta-data/"})
		blocked(t, res, err, "blocked by the outbound address policy: 169.254.169.254 is link-local (always blocked)")
		if !strings.Contains(log.String(), `"event":"outbound_blocked"`) ||
			!strings.Contains(log.String(), `"host":"169.254.169.254"`) {
			t.Fatalf("log %s", log)
		}
	})

	t.Run("redirect_refused", func(t *testing.T) {
		c, _ := client(t, Config{Class: ClassBackground, Policy: loopback})
		res, err := c.Do(t.Context(), Request{URL: srv.URL + "/redirect"})
		if res.Outcome != OutcomeRedirect || res.Attempts != 1 || redirects.Load() != 1 || err == nil ||
			err.Error() != "redirect to "+srv.URL+"/elsewhere refused" {
			t.Fatalf("result %+v, err %v", res, err)
		}
		t.Log(err)
	})

	t.Run("loopback_blocked_by_default", func(t *testing.T) {
		c, _ := client(t, Config{Policy: standard})
		res, err := c.Do(t.Context(), Request{URL: srv.URL + "/x"})
		blocked(t, res, err, "blocked by the outbound address policy: 127.0.0.1 is loopback (allow it with an allowed network)")
	})

	t.Run("loopback_allowed_by_network", func(t *testing.T) {
		c, _ := client(t, Config{Policy: loopback})
		res, err := c.Do(t.Context(), Request{URL: srv.URL + "/x"})
		if err != nil || res.Outcome != OutcomeOK || !strings.HasPrefix(res.Header.Get("X-Remote-Addr"), "127.0.0.1:") {
			t.Fatalf("result %+v, err %v", res, err)
		}
	})

	t.Run("socks5_proxy_used", func(t *testing.T) {
		p, err := fakeproxy.Start(t.Context(), fakeproxy.SOCKS5, "127.0.0.1:0", fakeproxy.Options{Username: "muster", Password: "pw"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = p.Close() }()
		c, _ := client(t, Config{Policy: loopback, Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr(),
			Username: "muster", Password: "pw"}})
		res, err := c.Do(t.Context(), Request{URL: srv.URL + "/x"})
		conns := p.Connections()
		if err != nil || len(conns) != 1 || conns[0].Target != strings.TrimPrefix(srv.URL, "http://") ||
			res.Header.Get("X-Remote-Addr") != conns[0].LocalAddr {
			t.Fatalf("result %+v, err %v, proxy connections %+v", res, err, conns)
		}
		t.Logf("proxy recorded target %s; target saw the connection from the proxy", conns[0].Target)
	})

	t.Run("proxy_on_metadata_address_refused", func(t *testing.T) {
		c, _ := client(t, Config{Policy: loopback, Proxy: &Proxy{Type: ProxySOCKS5, Address: "169.254.169.254:1080"}})
		res, err := c.Do(t.Context(), Request{URL: srv.URL + "/x"})
		blocked(t, res, err, "blocked by the outbound address policy: proxy 169.254.169.254 is link-local (always blocked)")
	})

	t.Run("name_only_the_proxy_resolves", func(t *testing.T) {
		// .invalid never resolves (RFC 6761); only the fake proxy knows the name.
		p, err := fakeproxy.Start(t.Context(), fakeproxy.SOCKS5, "127.0.0.1:0",
			fakeproxy.Options{Hosts: map[string]string{"chat.muster.invalid": "127.0.0.1"}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = p.Close() }()
		port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
		strict := StaticPolicy(mustPolicy(t, ModeStrict, []string{"127.0.0.0/8", "*.muster.invalid"}, nil))
		c, log := client(t, Config{Policy: strict, Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr()}})
		res, err := c.Do(t.Context(), Request{URL: "http://chat.muster.invalid:" + port + "/x"})
		if err != nil || res.Outcome != OutcomeOK || !strings.Contains(log.String(), `"event":"outbound_unverified_address"`) {
			t.Fatalf("result %+v, err %v, log %s", res, err, log)
		}
		t.Log(strings.TrimSpace(log.String()))
		denied := StaticPolicy(mustPolicy(t, ModeStrict, []string{"127.0.0.0/8"}, nil))
		c, _ = client(t, Config{Policy: denied, Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr()}})
		res, err = c.Do(t.Context(), Request{URL: "http://chat.muster.invalid:" + port + "/x"})
		blocked(t, res, err, "blocked by the outbound address policy: host chat.muster.invalid cannot be resolved and "+
			"matches no allowed entry (the strict policy needs one)")
	})

	t.Run("secret_in_url_redacted", func(t *testing.T) {
		secret := logging.Secret("123456:AAE" + strings.Repeat("x", 30) + "+/=")
		c, log := client(t, Config{Policy: loopback, Secrets: []logging.Secret{secret}})
		res, err := c.Do(t.Context(), Request{URL: "http://127.0.0.1:1/bot" + string(secret) + "/getMe"})
		if res.Outcome != OutcomeTransient || err == nil {
			t.Fatalf("result %+v, err %v", res, err)
		}
		var e *Error
		if !errors.As(err, &e) || errors.Unwrap(err) != nil {
			t.Fatalf("err %#v", err)
		}
		for _, text := range []string{err.Error(), log.String()} {
			if strings.Contains(text, "AAE") {
				t.Fatalf("the secret leaked: %s", text)
			}
		}
		if !strings.HasPrefix(err.Error(), `Get "http://127.0.0.1:1/bot[redacted]/getMe": dial tcp 127.0.0.1:1: connect: `) {
			t.Fatalf("err %v", err)
		}
		t.Log(err)
	})

	t.Run("env_proxy_ignored", func(t *testing.T) {
		envProxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = envProxy.Close() }()
		for _, v := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			if os.Getenv(v) == "" {
				t.Setenv(v, "http://"+envProxy.Addr())
			}
			t.Logf("%s=%s", v, os.Getenv(v))
		}
		t.Setenv("NO_PROXY", "")
		before := hits.Load()
		c, _ := client(t, Config{Policy: loopback})
		res, err := c.Do(t.Context(), Request{URL: srv.URL + "/x"})
		if err != nil || res.Outcome != OutcomeOK || hits.Load() != before+1 || len(envProxy.Targets()) != 0 {
			t.Fatalf("result %+v, err %v, env proxy targets %v", res, err, envProxy.Targets())
		}
	})
}
