// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/logging"
)

const (
	// wsTicket and wsAppID stand for the query of a Socket Mode address: the single-use ticket and the fake's app.
	wsTicket = "3f0c9a7e5b1d4c2f8a6e0b9d7c5a3e1f"
	wsAppID  = "A0MUSTERDEVB"
	wsQuery  = "?ticket=" + wsTicket + "&app_id=" + wsAppID
)

// wsServer is a WebSocket server that echoes every message and records the User-Agent and the remote address of each
// handshake.
type wsServer struct {
	*httptest.Server
	mu     sync.Mutex
	agents []string
	from   []string
}

func newWSServer(t *testing.T, tlsServer bool) *wsServer {
	t.Helper()
	ws := &wsServer{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws.mu.Lock()
		ws.agents = append(ws.agents, r.UserAgent())
		ws.from = append(ws.from, r.RemoteAddr)
		ws.mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), typ, data); err != nil {
				return
			}
		}
	})
	if tlsServer {
		ws.Server = httptest.NewTLSServer(h)
	} else {
		ws.Server = httptest.NewServer(h)
	}
	t.Cleanup(ws.Close)
	return ws
}

// url is the server's address with scheme ws or wss and the query of a Socket Mode address.
func (ws *wsServer) url() string {
	u := strings.Replace(ws.URL, "http", "ws", 1)
	return u + "/link/" + wsQuery
}

func (ws *wsServer) seen() (agents, from []string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return append([]string(nil), ws.agents...), append([]string(nil), ws.from...)
}

// echo checks that conn carries a message there and back, then closes it.
func echo(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	defer func() { _ = conn.CloseNow() }()
	if err := conn.Write(t.Context(), websocket.MessageText, []byte(`{"envelope_id":"e1"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, data, err := conn.Read(t.Context())
	if err != nil || string(data) != `{"envelope_id":"e1"}` {
		t.Fatalf("read %q, %v", data, err)
	}
}

func dialError(t *testing.T, err error) *Error {
	t.Helper()
	e, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("error %v is not an *Error", err)
	}
	return e
}

// proxyRequest is what a recording proxy was asked.
type proxyRequest struct {
	method, host, agent string
	// local is the address of the proxy's connection to the target, which the target sees.
	local string
}

// recordingProxy is an HTTP proxy that records each request and tunnels only CONNECT, answering 405 to anything
// else; answer, when set, is its answer to CONNECT instead of the tunnel, and silent makes it answer nothing.
type recordingProxy struct {
	ln     net.Listener
	answer string
	silent bool
	mu     sync.Mutex
	reqs   []proxyRequest
	open   map[net.Conn]struct{}
	wg     sync.WaitGroup
}

func startRecordingProxy(t *testing.T, tlsConfig *tls.Config, answer string, silent bool) *recordingProxy {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tlsConfig != nil {
		ln = tls.NewListener(ln, tlsConfig)
	}
	p := &recordingProxy{ln: ln, answer: answer, silent: silent, open: map[net.Conn]struct{}{}}
	p.wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.track(c)
			p.wg.Go(func() { p.serve(c) })
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		p.mu.Lock()
		for c := range p.open {
			_ = c.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p
}

func (p *recordingProxy) Addr() string { return p.ln.Addr().String() }

func (p *recordingProxy) track(c net.Conn) {
	p.mu.Lock()
	p.open[c] = struct{}{}
	p.mu.Unlock()
}

func (p *recordingProxy) requests() []proxyRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]proxyRequest(nil), p.reqs...)
}

func (p *recordingProxy) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	pr := proxyRequest{method: req.Method, host: req.Host, agent: req.UserAgent()}
	switch {
	case p.silent:
		p.record(pr)
		_, _ = io.Copy(io.Discard, br)
		return
	case req.Method != http.MethodConnect:
		p.record(pr)
		_, _ = io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
		return
	case p.answer != "":
		p.record(pr)
		_, _ = io.WriteString(c, p.answer)
		return
	}
	var d net.Dialer
	target, err := d.DialContext(context.Background(), "tcp", req.Host)
	if err != nil {
		p.record(pr)
		return
	}
	p.track(target)
	defer func() { _ = target.Close() }()
	pr.local = target.LocalAddr().String()
	p.record(pr)
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(target, br)
		_ = target.Close()
		close(done)
	}()
	_, _ = io.Copy(c, target)
	_ = c.Close()
	<-done
}

func (p *recordingProxy) record(pr proxyRequest) {
	p.mu.Lock()
	p.reqs = append(p.reqs, pr)
	p.mu.Unlock()
}

// testCert is the certificate of httptest's TLS servers, which the HTTPS proxies here serve too.
func testCert(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	srv.Close()
	return srv
}

func TestDialWebSocketDirect(t *testing.T) {
	ws := newWSServer(t, false)
	c, log := newTestClient(t, Config{Class: ClassBackground})
	ok := `muster_client_requests_total{client="background",outcome="ok"}`
	before := scrape(t, ok)
	conn, err := DialWebSocket(t.Context(), c, ws.url(), nil)
	if err != nil {
		t.Fatal(err)
	}
	echo(t, conn)
	if after := scrape(t, ok); after != before+1 {
		t.Fatalf("ok handshakes moved from %v to %v", before, after)
	}
	if agents, _ := ws.seen(); len(agents) != 1 || agents[0] != userAgent {
		t.Fatalf("user agents %q", agents)
	}
	if log.Len() != 0 {
		t.Fatalf("log %s", log)
	}

	conn, err = DialWebSocket(t.Context(), c, ws.url(), http.Header{"User-Agent": {"custom/1"}})
	if err != nil {
		t.Fatal(err)
	}
	echo(t, conn)
	if agents, _ := ws.seen(); agents[1] != "custom/1" {
		t.Fatalf("user agents %q", agents)
	}
}

func TestDialWebSocketTLS(t *testing.T) {
	ws := newWSServer(t, true)
	c, _ := newTestClient(t, Config{})
	if _, err := DialWebSocket(t.Context(), c, ws.url(), nil); err == nil || dialError(t, err).Network != NetworkTLS {
		t.Fatalf("an untrusted certificate: %v", err)
	}
	trust(c, ws.Certificate())
	conn, err := DialWebSocket(t.Context(), c, ws.url(), nil)
	if err != nil {
		t.Fatal(err)
	}
	echo(t, conn)
}

func TestDialWebSocketConnectForEveryScheme(t *testing.T) {
	cert := testCert(t)
	for _, secure := range []bool{false, true} {
		for _, proxyType := range []ProxyType{ProxyHTTP, ProxyHTTPS} {
			name := "ws"
			if secure {
				name = "wss"
			}
			t.Run(name+" through "+string(proxyType), func(t *testing.T) {
				var tlsConfig *tls.Config
				if proxyType == ProxyHTTPS {
					tlsConfig = &tls.Config{Certificates: cert.TLS.Certificates, MinVersion: tls.VersionTLS12}
				}
				p := startRecordingProxy(t, tlsConfig, "", false)
				ws := newWSServer(t, secure)
				c, _ := newTestClient(t, Config{Proxy: &Proxy{Type: proxyType, Address: p.Addr()}})
				// httptest's TLS servers share one certificate.
				trust(c, cert.Certificate())
				conn, err := DialWebSocket(t.Context(), c, ws.url(), nil)
				if err != nil {
					t.Fatal(err)
				}
				echo(t, conn)
				target := ws.Listener.Addr().String()
				reqs := p.requests()
				if len(reqs) != 1 || reqs[0].method != http.MethodConnect || reqs[0].host != target ||
					reqs[0].agent != userAgent {
					t.Fatalf("proxy requests %+v, want one CONNECT to %s", reqs, target)
				}
				if _, from := ws.seen(); len(from) != 1 || from[0] != reqs[0].local {
					t.Fatalf("the target saw %v, the proxy connected from %s", from, reqs[0].local)
				}
			})
		}
	}
}

func TestDialWebSocketSOCKS5(t *testing.T) {
	password := logging.Secret("s0cks-pa55+/=")
	p := startProxy(t, fakeproxy.SOCKS5, fakeproxy.Options{Username: "muster", Password: string(password)})
	ws := newWSServer(t, false)
	c, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr(), Username: "muster",
		Password: password}})
	conn, err := DialWebSocket(t.Context(), c, ws.url(), nil)
	if err != nil {
		t.Fatal(err)
	}
	echo(t, conn)
	conns := p.Connections()
	if len(conns) != 1 || conns[0].Target != ws.Listener.Addr().String() {
		t.Fatalf("proxy connections %+v", conns)
	}
	if _, from := ws.seen(); len(from) != 1 || from[0] != conns[0].LocalAddr {
		t.Fatalf("the target saw %v, the proxy connected from %s", from, conns[0].LocalAddr)
	}

	// A name only the proxy resolves is checked against the lists and logged with the WebSocket scheme.
	named := startProxy(t, fakeproxy.SOCKS5, fakeproxy.Options{Hosts: map[string]string{"only-proxy.test": "127.0.0.1"}})
	_, port, _ := net.SplitHostPort(ws.Listener.Addr().String())
	viaName, log := newTestClient(t, Config{Class: ClassInteractive,
		Proxy: &Proxy{Type: ProxySOCKS5, Address: named.Addr()}})
	viaName.resolver = &fakeResolver{}
	conn, err = DialWebSocket(t.Context(), viaName, "ws://only-proxy.test:"+port+"/link/"+wsQuery, nil)
	if err != nil {
		t.Fatal(err)
	}
	echo(t, conn)
	if !strings.Contains(log.String(), `"event":"outbound_unverified_address"`) ||
		!strings.Contains(log.String(), `"scheme":"ws"`) || strings.Contains(log.String(), wsTicket) {
		t.Fatalf("log %s", log)
	}

	wrong, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr(), Username: "muster",
		Password: "wrong-" + password}})
	_, err = DialWebSocket(t.Context(), wrong, ws.url(), nil)
	if e := dialError(t, err); e.Outcome != OutcomeTransient || e.Network != NetworkProxyAuth ||
		strings.Contains(e.Error(), "pa55") {
		t.Fatalf("wrong password: %+v", e)
	}
}

func TestDialWebSocketPolicy(t *testing.T) {
	ws := newWSServer(t, false)
	blocked := `muster_client_requests_total{client="interactive",outcome="blocked"}`
	before := scrape(t, blocked)
	c, log := newTestClient(t, Config{Class: ClassInteractive, Policy: StaticPolicy(Policy{Mode: ModeStandard})})
	_, err := DialWebSocket(t.Context(), c, ws.url(), nil)
	e := dialError(t, err)
	if e.Outcome != OutcomeBlocked || !strings.HasPrefix(e.Rule, "127.0.0.1 is loopback") {
		t.Fatalf("loopback: %+v", e)
	}
	if after := scrape(t, blocked); after != before+1 {
		t.Fatalf("blocked handshakes moved from %v to %v", before, after)
	}
	line := log.String()
	if !strings.Contains(line, `"event":"outbound_blocked"`) || !strings.Contains(line, `"host":"127.0.0.1"`) ||
		!strings.Contains(line, `"scheme":"ws"`) || strings.Contains(line, wsTicket) {
		t.Fatalf("log %s", line)
	}
	if _, from := ws.seen(); len(from) != 0 {
		t.Fatalf("the target saw %v", from)
	}

	// The proxy's own address is checked: the target is public, so that only the proxy can be refused.
	p := startRecordingProxy(t, nil, "", false)
	c, _ = newTestClient(t, Config{Policy: StaticPolicy(Policy{Mode: ModeStandard}),
		Proxy: &Proxy{Type: ProxyHTTP, Address: p.Addr()}})
	c.resolver = &fakeResolver{answers: map[string][][]string{"public.test": {{"93.184.215.14"}}}}
	_, err = DialWebSocket(t.Context(), c, "wss://public.test/link/"+wsQuery, nil)
	if e := dialError(t, err); e.Outcome != OutcomeBlocked || !strings.HasPrefix(e.Rule, "proxy 127.0.0.1 is loopback") {
		t.Fatalf("proxy address: %+v", e)
	}

	// A denied target name is refused before the proxy is reached.
	_, port, _ := net.SplitHostPort(ws.Listener.Addr().String())
	c, _ = newTestClient(t, Config{Policy: StaticPolicy(mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"},
		[]string{"*.test"})), Proxy: &Proxy{Type: ProxyHTTP, Address: p.Addr()}})
	_, err = DialWebSocket(t.Context(), c, "ws://denied.test:"+port+"/link/"+wsQuery, nil)
	if e := dialError(t, err); e.Outcome != OutcomeBlocked ||
		!strings.Contains(e.Rule, "host denied.test matches the denied entry *.test") {
		t.Fatalf("denied name: %+v", e)
	}
	if reqs := p.requests(); len(reqs) != 0 {
		t.Fatalf("a refused handshake reached the proxy: %+v", reqs)
	}
}

func TestDialWebSocketNoRedirect(t *testing.T) {
	other := newWSServer(t, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", other.url())
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, Config{})
	_, err := DialWebSocket(t.Context(), c, "ws"+strings.TrimPrefix(srv.URL, "http")+"/link/"+wsQuery, nil)
	e := dialError(t, err)
	if e.Outcome != OutcomeRedirect || e.Status != http.StatusFound || !strings.Contains(e.Error(), "redirect refused") ||
		strings.Contains(e.Error(), wsTicket) {
		t.Fatalf("redirect: %+v %q", e, e.Error())
	}
	if _, from := other.seen(); len(from) != 0 {
		t.Fatalf("the redirect was followed: %v", from)
	}
}

func TestDialWebSocketAnswers(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		header     http.Header
		want       Outcome
		retryAfter time.Duration
	}{
		{"refused", http.StatusForbidden, nil, OutcomeFatal, 0},
		{"unavailable", http.StatusServiceUnavailable, nil, OutcomeTransient, 0},
		{"rate limited", http.StatusTooManyRequests, http.Header{"Retry-After": {"7"}}, OutcomeRetryAfter, 7 * time.Second},
		{"not a websocket", http.StatusOK, nil, OutcomeUnknown, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.header {
					w.Header()[k] = v
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, r.URL.String()) //nolint:gosec // G705: echoes the ticket on purpose
			}))
			defer srv.Close()
			c, log := newTestClient(t, Config{})
			_, err := DialWebSocket(t.Context(), c, "ws"+strings.TrimPrefix(srv.URL, "http")+"/link/"+wsQuery, nil)
			e := dialError(t, err)
			if e.Outcome != tt.want || e.Status != tt.status || e.RetryAfter != tt.retryAfter {
				t.Fatalf("got %+v", e)
			}
			text := e.Error() + e.Detail + log.String()
			if !strings.Contains(e.Error(), "/link/?[redacted]") || strings.Contains(text, wsTicket) ||
				strings.Contains(text, wsAppID) {
				t.Fatalf("error %q, log %s", e.Error(), log)
			}
		})
	}
}

func TestDialWebSocketTimeouts(t *testing.T) {
	p := startRecordingProxy(t, nil, "", true)
	ws := newWSServer(t, false)
	c, _ := newTestClient(t, Config{ConnectTimeout: 100 * time.Millisecond, Timeout: 5 * time.Second,
		Proxy: &Proxy{Type: ProxyHTTP, Address: p.Addr()}})
	start := time.Now()
	_, err := DialWebSocket(t.Context(), c, ws.url(), nil)
	e := dialError(t, err)
	if e.Outcome != OutcomeTransient || e.Network != NetworkTimeout || time.Since(start) > 3*time.Second {
		t.Fatalf("a proxy that never answers CONNECT: %+v after %v", e, time.Since(start))
	}
	if reqs := p.requests(); len(reqs) != 1 || reqs[0].method != http.MethodConnect {
		t.Fatalf("proxy requests %+v", reqs)
	}

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	c, _ = newTestClient(t, Config{Timeout: 150 * time.Millisecond})
	_, err = DialWebSocket(t.Context(), c, "ws"+strings.TrimPrefix(slow.URL, "http")+"/link/"+wsQuery, nil)
	e = dialError(t, err)
	if e.Outcome != OutcomeTransient || e.Network != NetworkTimeout || !errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(e.Error(), wsTicket) {
		t.Fatalf("a target that never answers the handshake: %+v", e)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = DialWebSocket(ctx, c, ws.url(), nil)
	if e := dialError(t, err); e.Outcome != OutcomeTransient || !errors.Is(err, context.Canceled) {
		t.Fatalf("an ended context: %+v", e)
	}
}

func TestDialWebSocketProxyAuth(t *testing.T) {
	ws := newWSServer(t, false)
	password := logging.Secret("pr0xy-pa55+/=")
	p := startProxy(t, fakeproxy.HTTP, fakeproxy.Options{Username: "muster", Password: string(password)})
	c, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxyHTTP, Address: p.Addr(), Username: "muster",
		Password: password}})
	conn, err := DialWebSocket(t.Context(), c, ws.url(), nil)
	if err != nil {
		t.Fatal(err)
	}
	echo(t, conn)
	if targets := p.Targets(); len(targets) != 1 || targets[0] != ws.Listener.Addr().String() {
		t.Fatalf("targets %v", targets)
	}

	wrong, log := newTestClient(t, Config{Proxy: &Proxy{Type: ProxyHTTP, Address: p.Addr(), Username: "muster",
		Password: "wrong-" + password}})
	_, err = DialWebSocket(t.Context(), wrong, ws.url(), nil)
	e := dialError(t, err)
	if e.Outcome != OutcomeTransient || e.Network != NetworkProxyAuth || p.Refused() != 1 {
		t.Fatalf("wrong password: %+v, refused %d", e, p.Refused())
	}
	if text := e.Error() + e.Detail + log.String(); strings.Contains(text, "pa55") || strings.Contains(text, wsTicket) {
		t.Fatalf("error %q, log %s", e.Error(), log)
	}

	// An answer without a status text.
	odd := startRecordingProxy(t, nil, "HTTP/1.1 418\r\nContent-Length: 0\r\n\r\n", false)
	c, _ = newTestClient(t, Config{Proxy: &Proxy{Type: ProxyHTTP, Address: odd.Addr()}})
	_, err = DialWebSocket(t.Context(), c, ws.url(), nil)
	if e := dialError(t, err); e.Network != NetworkConnect || !strings.Contains(e.Detail, "unknown status code") {
		t.Fatalf("status without text: %+v", e)
	}
}

func TestDialWebSocketBadURL(t *testing.T) {
	ws := newWSServer(t, false)
	host := ws.Listener.Addr().String()
	fatal := `muster_client_requests_total{client="heartbeat",outcome="fatal"}`
	c, log := newTestClient(t, Config{Class: ClassHeartbeat})
	inputs := []string{
		"http://" + host + "/link/" + wsQuery,
		"ftp://" + host + "/link/" + wsQuery,
		"ws://user:" + wsTicket + "@" + host + "/link/",
		"ws://" + host + "/link/" + wsQuery + "#f",
		"ws://" + host + "/link/" + wsQuery + "#",
		"ws:" + wsTicket,
		"ws:///link/" + wsQuery,
		"ws://%zz/" + wsQuery,
	}
	before := scrape(t, fatal)
	for _, raw := range inputs {
		_, err := DialWebSocket(t.Context(), c, raw, nil)
		e := dialError(t, err)
		if e.Outcome != OutcomeFatal || e.Error() != errNotWebSocketURL.Error() {
			t.Fatalf("%s: %+v", raw, e)
		}
	}
	if after := scrape(t, fatal); after != before+float64(len(inputs)) {
		t.Fatalf("fatal handshakes moved from %v to %v", before, after)
	}
	if _, from := ws.seen(); len(from) != 0 || log.Len() != 0 {
		t.Fatalf("the target saw %v, log %s", from, log)
	}
}

func TestDialWebSocketQueryRedacted(t *testing.T) {
	c, _ := newTestClient(t, Config{Secrets: []logging.Secret{"client-secret-value"}})
	_, err := DialWebSocket(t.Context(), c, "ws://127.0.0.1:1/client-secret-value/"+wsQuery, nil)
	e := dialError(t, err)
	if e.Outcome != OutcomeTransient || e.Network != NetworkConnect || e.Detail == "" {
		t.Fatalf("closed port: %+v", e)
	}
	text := e.Error() + e.Detail
	if strings.Contains(text, wsTicket) || strings.Contains(text, wsAppID) || strings.Contains(text, "client-secret") ||
		!strings.Contains(e.Error(), `"ws://127.0.0.1:1/[redacted]/?[redacted]"`) {
		t.Fatalf("error %q, detail %q", e.Error(), e.Detail)
	}
}

func TestWebSocketSecrets(t *testing.T) {
	u, err := webSocketTarget("wss://h.example/link/?ticket=a%2Fb%2B" + wsTicket + "&app_id=" + wsAppID + "&n=1")
	if err != nil {
		t.Fatal(err)
	}
	red := newRedactor(webSocketSecrets(u))
	got := red.redact("ticket a/b+" + wsTicket + ", app " + wsAppID + ", n=1, query " + u.RawQuery)
	if got != "ticket [redacted], app [redacted], n=1, query [redacted]" {
		t.Fatalf("redacted %q", got)
	}
}

// TestConnectEarlyBytes covers a proxy that sends the target's first bytes together with its answer to CONNECT.
func TestConnectEarlyBytes(t *testing.T) {
	client, proxy := net.Pipe()
	defer func() { _ = client.Close() }()
	go func() {
		defer func() { _ = proxy.Close() }()
		if _, err := http.ReadRequest(bufio.NewReader(proxy)); err != nil {
			return
		}
		_, _ = io.WriteString(proxy, "HTTP/1.1 200 Connection Established\r\n\r\nearly")
	}()
	conn, err := connect(t.Context(), client, "target.test:443", &url.URL{Scheme: "http", Host: "proxy.test:3128"},
		time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conn.(*bufferedConn); !ok {
		t.Fatalf("connection %T", conn)
	}
	got, err := io.ReadAll(conn)
	if err != nil && !errors.Is(err, io.ErrClosedPipe) || string(got) != "early" {
		t.Fatalf("read %q, %v", got, err)
	}
}
