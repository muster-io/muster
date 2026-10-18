// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/muster-io/muster/internal/buildinfo"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
)

// agentServer records the User-Agent and the protocol of every request it answers.
type agentServer struct {
	*httptest.Server
	mu     sync.Mutex
	agents []string
	protos []string
}

func newAgentServer(t *testing.T, tlsServer bool) *agentServer {
	t.Helper()
	s := &agentServer{}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.agents = append(s.agents, r.Header.Get("User-Agent"))
		s.protos = append(s.protos, r.Proto)
		s.mu.Unlock()
		_, _ = io.WriteString(w, "hello")
	}))
	if tlsServer {
		s.EnableHTTP2 = true
		s.StartTLS()
	} else {
		s.Start()
	}
	t.Cleanup(s.Close)
	return s
}

func (s *agentServer) seen() (agents, protos []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.agents...), append([]string(nil), s.protos...)
}

func TestUserAgentOf(t *testing.T) {
	tests := map[string]string{
		"0.0.1-SNAPSHOT-1bf5d20": "Muster/0.0.1-SNAPSHOT-1bf5d20",
		"v1.2.3":                 "Muster/1.2.3",
		"0.0.0-dev":              "Muster/0.0.0-dev",
		"":                       "Muster/unknown",
	}
	for version, want := range tests {
		if got := userAgentOf(version); got != want {
			t.Errorf("userAgentOf(%q) = %q, want %q", version, got, want)
		}
	}
	if want := userAgentOf(buildinfo.Version); userAgent != want {
		t.Fatalf("userAgent %q, want %q from the build's version", userAgent, want)
	}
}

func TestUserAgentEveryClass(t *testing.T) {
	for _, class := range Classes {
		for _, tlsServer := range []bool{false, true} {
			srv := newAgentServer(t, tlsServer)
			c, _ := newTestClient(t, Config{Class: class})
			if tlsServer {
				trust(c, srv.Certificate())
			}
			// A header of the caller's own without a User-Agent still gets Muster's.
			req := Request{URL: srv.URL + "/x", Header: http.Header{"Accept": {"application/json"}}}
			if res, err := c.Do(context.Background(), req); err != nil || string(res.Body) != "hello" {
				t.Fatalf("%s, TLS %v: %+v %v", class, tlsServer, res, err)
			}
			wantProto := "HTTP/1.1"
			if tlsServer {
				wantProto = "HTTP/2.0"
			}
			agents, protos := srv.seen()
			if len(agents) != 1 || agents[0] != userAgent || protos[0] != wantProto {
				t.Fatalf("%s, TLS %v: agents %q over %q, want %q over %s", class, tlsServer, agents, protos,
					userAgent, wantProto)
			}
		}
	}
}

func TestUserAgentOfTheCallerKept(t *testing.T) {
	srv := newAgentServer(t, false)
	c, _ := newTestClient(t, Config{})
	req := Request{URL: srv.URL, Header: http.Header{"User-Agent": {"receiver-wants/1"}}}
	if _, err := c.Do(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if agents, _ := srv.seen(); len(agents) != 1 || agents[0] != "receiver-wants/1" {
		t.Fatalf("agents %q", agents)
	}
}

func TestUserAgentThroughProxies(t *testing.T) {
	for _, kind := range []fakeproxy.Kind{fakeproxy.HTTP, fakeproxy.SOCKS5} {
		p := startProxy(t, kind, fakeproxy.Options{Username: "u", Password: "p"})
		plain, secure := newAgentServer(t, false), newAgentServer(t, true)
		c, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxyType(kind), Address: p.Addr(), Username: "u",
			Password: "p"}})
		trust(c, secure.Certificate())
		// Through an HTTP proxy, a plain target gets the request in absolute form and a TLS target through a tunnel.
		for _, srv := range []*agentServer{plain, secure} {
			if res, err := c.Do(context.Background(), Request{URL: srv.URL}); err != nil || string(res.Body) != "hello" {
				t.Fatalf("%s proxy, %s: %+v %v", kind, srv.URL, res, err)
			}
			if agents, _ := srv.seen(); len(agents) != 1 || agents[0] != userAgent {
				t.Fatalf("%s proxy, %s: agents %q", kind, srv.URL, agents)
			}
		}
	}
}

func TestUserAgentOfConnect(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// The proxy reads the CONNECT and refuses it.
	got := make(chan *http.Request, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if req, err := http.ReadRequest(bufio.NewReader(conn)); err == nil {
			got <- req
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
	}()
	c, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxyHTTP, Address: ln.Addr().String(), Username: "u",
		Password: "p"}})
	if _, err := c.Do(context.Background(), Request{URL: "https://127.0.0.1:1/x"}); err == nil {
		t.Fatal("the refused CONNECT succeeded")
	}
	req := <-got
	if req.Method != http.MethodConnect || req.UserAgent() != userAgent ||
		!strings.HasPrefix(req.Header.Get("Proxy-Authorization"), "Basic ") {
		t.Fatalf("CONNECT %s with headers %v, want the User-Agent %q and the credentials", req.Method, req.Header,
			userAgent)
	}
}
