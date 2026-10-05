// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/logging"
)

// target is a server that records the remote address of every request.
type target struct {
	*httptest.Server
	mu    sync.Mutex
	from  []string
	paths []string
}

func newTarget(t *testing.T, tlsServer bool) *target {
	t.Helper()
	tg := &target{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tg.mu.Lock()
		tg.from = append(tg.from, r.RemoteAddr)
		tg.paths = append(tg.paths, r.URL.Path)
		tg.mu.Unlock()
		_, _ = w.Write([]byte("hello"))
	})
	if tlsServer {
		tg.Server = httptest.NewTLSServer(h)
	} else {
		tg.Server = httptest.NewServer(h)
	}
	t.Cleanup(tg.Close)
	return tg
}

func (tg *target) remotes() []string {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	return append([]string(nil), tg.from...)
}

func startProxy(t *testing.T, kind fakeproxy.Kind, opts fakeproxy.Options) *fakeproxy.Server {
	t.Helper()
	p, err := fakeproxy.Start(t.Context(), kind, "127.0.0.1:0", opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// trust makes c trust the certificates of servers.
func trust(c *Client, certs ...*x509.Certificate) {
	pool := x509.NewCertPool()
	for _, cert := range certs {
		pool.AddCert(cert)
	}
	c.http.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool
}

// assertThroughProxy checks that every connection the target saw came from the proxy.
func assertThroughProxy(t *testing.T, p *fakeproxy.Server, tg *target, wantTarget string) {
	t.Helper()
	conns := p.Connections()
	if len(conns) == 0 || conns[0].Target != wantTarget {
		t.Fatalf("proxy connections %+v, want target %s", conns, wantTarget)
	}
	from := tg.remotes()
	if len(from) == 0 {
		t.Fatal("the target saw no request")
	}
	for _, f := range from {
		found := false
		for _, c := range conns {
			found = found || c.LocalAddr == f
		}
		if !found {
			t.Fatalf("the target saw a connection from %s, not from the proxy %+v", f, conns)
		}
	}
}

func TestSOCKS5Proxy(t *testing.T) {
	password := logging.Secret("pr0xy-pa55+/=")
	p := startProxy(t, fakeproxy.SOCKS5, fakeproxy.Options{Username: "muster", Password: string(password)})
	tg := newTarget(t, false)
	c, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr(), Username: "muster",
		Password: password}})
	res, err := c.Do(context.Background(), Request{URL: tg.URL + "/through"})
	if err != nil || string(res.Body) != "hello" {
		t.Fatalf("result %+v, err %v", res, err)
	}
	assertThroughProxy(t, p, tg, strings.TrimPrefix(tg.URL, "http://"))

	wrong, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr(), Username: "muster",
		Password: "wrong-" + password}})
	res, err = wrong.Do(context.Background(), Request{URL: tg.URL})
	if res.Outcome != OutcomeTransient || err == nil || strings.Contains(err.Error(), "pa55") || p.Refused() != 1 {
		t.Fatalf("wrong password: %+v %v, refused %d", res, err, p.Refused())
	}
}

func TestHTTPProxy(t *testing.T) {
	p := startProxy(t, fakeproxy.HTTP, fakeproxy.Options{Username: "u", Password: "p"})
	plain := newTarget(t, false)
	secure := newTarget(t, true)
	c, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxyHTTP, Address: p.Addr(), Username: "u", Password: "p"}})
	trust(c, secure.Certificate())
	// A plain target is sent to the proxy in absolute form, a TLS target through a CONNECT tunnel.
	for _, tg := range []*target{plain, secure} {
		if res, err := c.Do(context.Background(), Request{URL: tg.URL + "/x"}); err != nil || string(res.Body) != "hello" {
			t.Fatalf("%s: %+v %v", tg.URL, res, err)
		}
	}
	targets := p.Targets()
	if len(targets) != 2 || targets[0] != strings.TrimPrefix(plain.URL, "http://") ||
		targets[1] != strings.TrimPrefix(secure.URL, "https://") {
		t.Fatalf("targets %v", targets)
	}
	noAuth, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxyHTTP, Address: p.Addr()}})
	if res, _ := noAuth.Do(context.Background(), Request{URL: plain.URL}); res.Status != http.StatusProxyAuthRequired {
		t.Fatalf("without credentials: %+v", res)
	}
}

func TestHTTPSProxy(t *testing.T) {
	cert := httptest.NewTLSServer(http.NotFoundHandler())
	cert.Close()
	p := startProxy(t, fakeproxy.HTTP, fakeproxy.Options{TLS: &tls.Config{Certificates: cert.TLS.Certificates,
		MinVersion: tls.VersionTLS12}})
	tg := newTarget(t, false)
	c, _ := newTestClient(t, Config{Proxy: &Proxy{Type: ProxyHTTPS, Address: p.Addr()}})
	trust(c, cert.Certificate())
	res, err := c.Do(context.Background(), Request{URL: tg.URL + "/x"})
	if err != nil || string(res.Body) != "hello" {
		t.Fatalf("result %+v, err %v", res, err)
	}
	assertThroughProxy(t, p, tg, strings.TrimPrefix(tg.URL, "http://"))
}

func TestProxyAddressChecked(t *testing.T) {
	tests := []struct {
		name, address, rule string
		policy              PolicySource
	}{
		{"metadata", "169.254.169.254:1080", "proxy 169.254.169.254 is link-local (always blocked)", loopbackAllowed()},
		{"loopback by default", "127.0.0.1:1080", "proxy 127.0.0.1 is loopback", StaticPolicy(Policy{Mode: ModeStandard})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := newTarget(t, false)
			c, log := newTestClient(t, Config{Policy: tt.policy,
				Proxy: &Proxy{Type: ProxySOCKS5, Address: tt.address}})
			// The target is a public address so that only the proxy's own address can be refused.
			c.resolver = &fakeResolver{answers: map[string][][]string{"public.test": {{"93.184.215.14"}}}}
			res, err := c.Do(context.Background(), Request{URL: "http://public.test/x"})
			var e *Error
			if res.Outcome != OutcomeBlocked || !errors.As(err, &e) ||
				!strings.HasPrefix(e.Rule, tt.rule) {
				t.Fatalf("result %+v, err %v", res, err)
			}
			if !strings.Contains(log.String(), `"event":"outbound_blocked"`) || len(tg.remotes()) != 0 {
				t.Fatalf("log %s", log)
			}
		})
	}
}

func TestProxyTargetChecks(t *testing.T) {
	names := []string{"only-proxy.test", "local.test", "mixed.test"}
	hosts := map[string]string{}
	for _, n := range names {
		hosts[n] = "127.0.0.1"
	}
	p := startProxy(t, fakeproxy.SOCKS5, fakeproxy.Options{Hosts: hosts})
	tg := newTarget(t, false)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(tg.URL, "http://"))
	tests := []struct {
		name, host string
		policy     Policy
		rule       string // empty when the request passes
		unverified bool
	}{
		{"address", "127.0.0.1", mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, nil), "", false},
		{"blocked address", "169.254.169.254", mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, nil),
			"169.254.169.254 is link-local", false},
		{"resolved name", "local.test", mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, nil), "", false},
		{"every address checked", "mixed.test", mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, nil),
			"169.254.169.254 is link-local", false},
		{"denied name", "local.test", mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, []string{"*.test"}),
			"host local.test matches the denied entry *.test", false},
		{"name only the proxy resolves", "only-proxy.test", mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, nil),
			"", true},
		{"strict refuses an unlisted name", "only-proxy.test", mustPolicy(t, ModeStrict, []string{"127.0.0.0/8"}, nil),
			"host only-proxy.test cannot be resolved and matches no allowed entry", false},
		{"strict passes an allowed name", "only-proxy.test",
			mustPolicy(t, ModeStrict, []string{"127.0.0.0/8", "only-proxy.test"}, nil), "", true},
		{"denied unresolved name", "only-proxy.test",
			mustPolicy(t, ModeStandard, []string{"127.0.0.0/8"}, []string{"only-proxy.test"}),
			"host only-proxy.test matches the denied entry", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, log := newTestClient(t, Config{Class: ClassInteractive, Policy: StaticPolicy(tt.policy),
				Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr()}})
			c.resolver = &fakeResolver{answers: map[string][][]string{
				"mixed.test": {{"127.0.0.1", "169.254.169.254"}},
				"local.test": {{"127.0.0.1"}},
			}}
			before := len(p.Targets())
			res, err := c.Do(context.Background(), Request{URL: "http://" + net.JoinHostPort(tt.host, port) + "/x"})
			if tt.rule != "" {
				var e *Error
				if res.Outcome != OutcomeBlocked || !errors.As(err, &e) || !strings.Contains(e.Rule, tt.rule) {
					t.Fatalf("result %+v, err %v", res, err)
				}
				if len(p.Targets()) != before {
					t.Fatal("a blocked request reached the proxy")
				}
				return
			}
			targets := p.Targets()
			if err != nil || string(res.Body) != "hello" || len(targets) != before+1 ||
				targets[before] != net.JoinHostPort(tt.host, port) {
				t.Fatalf("result %+v, err %v, targets %v", res, err, targets)
			}
			if got := strings.Contains(log.String(), `"event":"outbound_unverified_address"`); got != tt.unverified {
				t.Fatalf("unverified logged %v, log %s", got, log)
			}
		})
	}
}

func TestUnverifiedAddressLogged(t *testing.T) {
	tg := newTarget(t, false)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(tg.URL, "http://"))
	p := startProxy(t, fakeproxy.SOCKS5, fakeproxy.Options{Hosts: map[string]string{"only-proxy.test": "127.0.0.1"}})
	c, log := newTestClient(t, Config{Class: ClassInteractive,
		Policy: StaticPolicy(mustPolicy(t, ModeStrict, []string{"127.0.0.0/8", "*.proxy.test", "only-proxy.test"}, nil)),
		Proxy:  &Proxy{Type: ProxySOCKS5, Address: p.Addr()}})
	c.resolver = &fakeResolver{}
	res, err := c.Do(context.Background(), Request{URL: "http://only-proxy.test:" + port + "/x"})
	if err != nil || string(res.Body) != "hello" {
		t.Fatalf("result %+v, err %v", res, err)
	}
	assertThroughProxy(t, p, tg, "only-proxy.test:"+port)
	var line map[string]any
	if json.Unmarshal(log.Bytes(), &line) != nil || line["event"] != "outbound_unverified_address" ||
		line["level"] != "INFO" || line["client"] != "interactive" || line["scheme"] != "http" ||
		line["host"] != "only-proxy.test" {
		t.Fatalf("log %s", log)
	}
}

// endingResolver ends the request's context while it resolves, as a request cancelled during the lookup.
type endingResolver struct{ cancel context.CancelFunc }

func (r endingResolver) LookupNetIP(ctx context.Context, _, host string) ([]netip.Addr, error) {
	r.cancel()
	return nil, &net.DNSError{Err: ctx.Err().Error(), Name: host}
}

func TestProxyLookupWithEndedContext(t *testing.T) {
	p := startProxy(t, fakeproxy.SOCKS5, fakeproxy.Options{})
	c, log := newTestClient(t, Config{Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr()}})
	ctx, cancel := context.WithCancel(context.Background())
	c.resolver = endingResolver{cancel: cancel}
	res, err := c.Do(ctx, Request{URL: "http://name.test/"})
	if res.Outcome != OutcomeTransient || !errors.Is(err, context.Canceled) || log.Len() != 0 || len(p.Targets()) != 0 {
		t.Fatalf("result %+v, err %v, log %s", res, err, log)
	}
}

func TestProxyPolicyAndResolverErrors(t *testing.T) {
	p := startProxy(t, fakeproxy.SOCKS5, fakeproxy.Options{})
	c, _ := newTestClient(t, Config{Policy: failingPolicy{}, Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr()}})
	if res, err := c.Do(context.Background(), Request{URL: "http://127.0.0.1:1/"}); res.Outcome != OutcomeTransient ||
		err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("policy error: %+v %v", res, err)
	}
	c, _ = newTestClient(t, Config{Proxy: &Proxy{Type: ProxySOCKS5, Address: p.Addr()}})
	c.resolver = &fakeResolver{err: errors.New("resolver broken")}
	if res, err := c.Do(context.Background(), Request{URL: "http://name.test/"}); res.Outcome != OutcomeTransient ||
		err == nil || !strings.Contains(err.Error(), "resolver broken") {
		t.Fatalf("resolver error: %+v %v", res, err)
	}
}
