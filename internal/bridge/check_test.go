// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/outbound"
)

var direct = Direct(delivery.Call{Class: outbound.ClassInteractive})

const problemJSON = "application/problem+json"

// TestCheckChannel covers C-22.FR-3: the Destination check is one checkChannel, and each answer has its code — the
// channel's name on 200, token_invalid on 401, channel_forbidden with the bridge's detail on any other forbidden, the
// code of delivery.FailureCode without an answer, and request_failed naming the class otherwise.
func TestCheckChannel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  http.HandlerFunc
		ok      bool
		want    string // the name when ok, else the message
		code    string
		kind    delivery.OutcomeKind
		retry   time.Duration
		scope   delivery.Scope
		partial bool // want is a part of the message
	}{
		{name: "200", answer: answerJSON(http.StatusOK, "application/json", `{"channel_id":"c","name":"alerts"}`),
			ok: true, want: "alerts", kind: delivery.OutcomeOK},
		{name: "200 with a name that echoes the token", answer: answerJSON(http.StatusOK, "application/json",
			`{"channel_id":"c","name":"alerts `+testToken+`"}`), ok: true, want: "alerts [redacted]",
			kind: delivery.OutcomeOK},
		{name: "200 with an empty name", answer: answerJSON(http.StatusOK, "application/json",
			`{"channel_id":"c","name":""}`), want: "The bridge answered invalid: POST channels/{channel_id}/check: " +
			"the channel's name is not 1 to 200 characters without NUL", code: delivery.CodeRequestFailed,
			kind: delivery.OutcomeUnknown},
		{name: "200 with a name of 201 characters", answer: answerJSON(http.StatusOK, "application/json",
			`{"channel_id":"c","name":"`+strings.Repeat("é", 201)+`"}`), want: "the channel's name is not 1 to 200",
			code: delivery.CodeRequestFailed, kind: delivery.OutcomeUnknown, partial: true},
		{name: "200 with NUL in the name", answer: answerJSON(http.StatusOK, "application/json",
			`{"channel_id":"c","name":"a\u0000b"}`), want: "The bridge answered invalid", code: delivery.CodeRequestFailed,
			kind: delivery.OutcomeUnknown, partial: true},
		{name: "200 that is not the expected JSON", answer: answerJSON(http.StatusOK, "text/html", "<p>hi</p>"),
			want: "The bridge answered invalid: POST channels/", code: delivery.CodeRequestFailed,
			kind: delivery.OutcomeUnknown, partial: true},
		{name: "401", answer: answerJSON(http.StatusUnauthorized, problemJSON,
			`{"title":"Unauthorized","class":"forbidden"}`), want: MessageTokenInvalid, code: CodeTokenInvalid,
			kind: delivery.OutcomeFatal},
		{name: "403 with a detail", answer: answerJSON(http.StatusForbidden, problemJSON,
			`{"title":"Forbidden","class":"forbidden","detail":"The bot is not a member of this channel."}`),
			want: "The bot is not a member of this channel.", code: CodeChannelForbidden, kind: delivery.OutcomeFatal},
		{name: "403 with a detail that echoes the token", answer: answerJSON(http.StatusForbidden, problemJSON,
			`{"class":"forbidden","detail":"no `+testToken+`"}`), want: "no [redacted]", code: CodeChannelForbidden,
			kind: delivery.OutcomeFatal},
		{name: "403 without a problem", answer: answerJSON(http.StatusForbidden, "text/plain", "no"),
			want: "403", code: CodeChannelForbidden, kind: delivery.OutcomeFatal, partial: true},
		{name: "forbidden on 400", answer: answerJSON(http.StatusBadRequest, problemJSON,
			`{"class":"forbidden","detail":"Unknown channel."}`), want: "Unknown channel.", code: CodeChannelForbidden,
			kind: delivery.OutcomeFatal},
		{name: "500", answer: answerJSON(http.StatusInternalServerError, problemJSON,
			`{"class":"transient","detail":"The messenger is down."}`),
			want: "The bridge answered transient: The messenger is down.", code: delivery.CodeRequestFailed,
			kind: delivery.OutcomeTransient},
		{name: "429", answer: answerJSON(http.StatusTooManyRequests, problemJSON,
			`{"class":"rate_limited","scope":"bot","detail":"Slow down."}`, "Retry-After", "7"),
			want: "The bridge answered rate_limited: Slow down.", code: delivery.CodeRequestFailed,
			kind: delivery.OutcomeRetryAfter, retry: 7 * time.Second, scope: delivery.ScopeConnection},
		{name: "gone", answer: answerJSON(http.StatusNotFound, problemJSON, `{"class":"gone","detail":"Gone."}`),
			want: "The bridge answered gone: Gone.", code: delivery.CodeRequestFailed, kind: delivery.OutcomeGone},
		{name: "404", answer: answerJSON(http.StatusNotFound, "text/plain", "no"),
			want: "The bridge answered invalid: ", code: delivery.CodeRequestFailed, kind: delivery.OutcomeUnknown,
			partial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			b := startStandIn(t, tc.answer)
			c := newClient(t, network(t, &buf), Settings{BaseURL: b.srv.URL, Token: testToken})
			res, err := CheckDestination(t.Context(), c, direct, "ch-1")
			if err != nil {
				t.Fatal(err)
			}
			got := res.Message
			if tc.ok {
				got = res.Name
			}
			if res.OK() != tc.ok || res.Code != tc.code || res.Outcome.Kind != tc.kind ||
				(!tc.partial && got != tc.want) || (tc.partial && !strings.Contains(got, tc.want)) {
				t.Fatalf("check = %+v", res)
			}
			if !tc.ok && string(res.Outcome.Error) != res.Message {
				t.Fatalf("the outcome's error %q is not the message %q", res.Outcome.Error, res.Message)
			}
			if tc.ok && (res.Message != "" || res.Code != "") {
				t.Fatalf("a passing check has a message: %+v", res)
			}
			if res.Outcome.RetryAfter != tc.retry || res.Outcome.Scope != tc.scope {
				t.Fatalf("outcome %+v", res.Outcome)
			}
			if strings.Contains(res.Message+res.Name, testToken) || strings.Contains(buf.String(), testToken) {
				t.Fatalf("the token leaked: %+v %s", res, buf.String())
			}
		})
	}
}

// TestCheckChannelRequest: checkChannel is a POST without a body to /v1/channels/<channel id>/check, the id one path
// segment, with the token as a bearer token.
func TestCheckChannelRequest(t *testing.T) {
	b := startStandIn(t, answerJSON(http.StatusOK, "application/json", `{"channel_id":"x","name":"alerts"}`))
	base, err := ParseBaseURL(b.srv.URL + "/pfx")
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t, network(t, nil), Settings{BaseURL: base, Token: testToken})
	for _, id := range []string{"C0/../x y", ".."} {
		if res, err := CheckDestination(t.Context(), c, direct, id); err != nil || !res.OK() {
			t.Fatalf("check %q = %+v, %v", id, res, err)
		}
	}
	got := b.seen()
	if len(got) != 2 {
		t.Fatalf("requests %+v", got)
	}
	for i, raw := range []string{"/pfx/v1/channels/C0%2F..%2Fx%20y/check", "/pfx/v1/channels/%2E%2E/check"} {
		r := got[i]
		if r.Method != http.MethodPost || r.RawPath != raw || r.Body != "" || r.Header.Get("Content-Type") != "" ||
			r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("request %d %+v", i, r)
		}
	}
}

// failingResolver answers every lookup with a DNS error.
type failingResolver struct{}

func (failingResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// TestCheckChannelWithoutAnswer: a checkChannel without an answer has the code of what failed, and an address the
// outbound address policy refuses is blocked, not channel_forbidden.
func TestCheckChannelWithoutAnswer(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + ln.Addr().String()
	_ = ln.Close()
	slow := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		answerJSON(http.StatusOK, "application/json", `{"channel_id":"c","name":"late"}`)(w, r)
	})
	ok := startStandIn(t, answerJSON(http.StatusOK, "application/json", `{"channel_id":"c","name":"alerts"}`))
	strict, err := outbound.ParsePolicy("standard", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked := network(t, nil)
	blocked.Policy = outbound.StaticPolicy(strict)
	dns := network(t, nil)
	dns.Resolver = failingResolver{}
	for _, tc := range []struct {
		name string
		n    Network
		base string
		ctx  time.Duration
		code string
		kind delivery.OutcomeKind
	}{
		{"timeout", network(t, nil), slow.srv.URL, 100 * time.Millisecond, delivery.CodeTimeout,
			delivery.OutcomeTransient},
		{"refused", network(t, nil), refused, 0, delivery.CodeConnectFailed, delivery.OutcomeTransient},
		{"dns", dns, "http://bridge.invalid", 0, delivery.CodeDNSFailed, delivery.OutcomeTransient},
		{"blocked", blocked, ok.srv.URL, 0, delivery.CodeBlocked, delivery.OutcomeFatal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.ctx > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.ctx)
				defer cancel()
			}
			c := newClient(t, tc.n, Settings{BaseURL: tc.base, Token: testToken})
			res, err := CheckDestination(ctx, c, direct, "ch-1")
			if err != nil || res.OK() || res.Code != tc.code || res.Outcome.Kind != tc.kind || res.Message == "" ||
				strings.Contains(res.Message, testToken) {
				t.Fatalf("check = %+v, %v", res, err)
			}
		})
	}
	if len(ok.seen()) != 0 {
		t.Fatal("a blocked address was called")
	}
	if got := checkOf("", Result{Classification: Classification{Class: ClassTransient}}); got.Message != checkFailed ||
		got.Code != delivery.CodeRequestFailed {
		t.Fatalf("a failure without text = %+v", got)
	}
	if got := checkOf("", Result{Status: http.StatusForbidden, Classification: Classification{
		Class: ClassForbidden}}); got.Message != MessageChannelForbidden || got.Code != CodeChannelForbidden {
		t.Fatalf("a forbidden answer without text = %+v", got)
	}
	if got := checkOf("", Result{Status: http.StatusBadRequest, Classification: Classification{
		Class: ClassInvalid}}); got.Message != "The bridge answered invalid: no details" {
		t.Fatalf("an invalid answer without text = %+v", got)
	}
}

// TestCheckChannelThroughTheProxy covers C-22.AC-3: with a proxy on the Connection the check's request reaches the
// bridge only through it.
func TestCheckChannelThroughTheProxy(t *testing.T) {
	b := startStandIn(t, answerJSON(http.StatusOK, "application/json", `{"channel_id":"c","name":"alerts"}`))
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken,
		Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP, Address: proxy.Addr()}})
	res, err := CheckDestination(t.Context(), c, direct, "ch-1")
	if err != nil || !res.OK() || res.Name != "alerts" || c.Via() != ViaProxy {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if len(proxy.Connections()) != 1 || len(b.seen()) != 1 {
		t.Fatalf("proxy %+v, bridge %d", proxy.Connections(), len(b.seen()))
	}
}

// path is an interactive path that records its subject and answers with err without calling.
type path struct {
	subjects []delivery.Subject
	err      error
}

func (p *path) Do(_ context.Context, s delivery.Subject, _ delivery.Op) (delivery.Outcome, error) {
	p.subjects = append(p.subjects, s)
	return delivery.Outcome{}, p.err
}

// TestCheckChannelRunners: the Interactive runner goes through the interactive path limited by its subject, and its
// error — no limiter token — ends the check without a request; Direct calls at once.
func TestCheckChannelRunners(t *testing.T) {
	b := startStandIn(t, answerJSON(http.StatusOK, "application/json", `{"channel_id":"c","name":"alerts"}`))
	c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken})
	conn := int64(3)
	subject := delivery.Subject{Destination: &delivery.Destination{ID: 9, Type: "bridge", Connection: &conn}}
	limited := &delivery.LimitedError{RetryAfter: time.Second}
	p := &path{err: limited}
	if _, err := CheckDestination(t.Context(), c, Interactive(p, subject), "ch-1"); !errors.Is(err, limited) {
		t.Fatalf("limited = %v", err)
	}
	if len(p.subjects) != 1 || p.subjects[0].Destination.ID != 9 || len(b.seen()) != 0 {
		t.Fatalf("subjects %+v, requests %d", p.subjects, len(b.seen()))
	}
	clocks := clock.Clocks{Business: clock.Real{}, Real: clock.Real{}}
	res, err := CheckDestination(t.Context(), c, Interactive(deliverytest.Unlimited(1, clocks), subject), "ch-1")
	if err != nil || !res.OK() || res.Name != "alerts" || len(b.seen()) != 1 {
		t.Fatalf("check = %+v, %v", res, err)
	}
	res, err = CheckDestination(t.Context(), c, Direct(delivery.Call{Class: outbound.ClassBackground}), "ch-1")
	if err != nil || !res.OK() || len(b.seen()) != 2 {
		t.Fatalf("direct check = %+v, %v", res, err)
	}
	res, err = CheckDestination(t.Context(), c, Direct(delivery.Call{Class: outbound.ClassHeartbeat}), "ch-1")
	if err != nil || res.OK() || res.Code != delivery.CodeRequestFailed || len(b.seen()) != 2 {
		t.Fatalf("a class the client has not = %+v, %v", res, err)
	}
}

// wantProbeOnly fails the test unless the stand-in got only the dry probe, without an Authorization header.
func wantProbeOnly(t *testing.T, b *standIn) {
	t.Helper()
	got := b.seen()
	if len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != "/v1/info" ||
		got[0].Header.Get("Authorization") != "" {
		t.Errorf("requests %+v", got)
	}
}

// TestDryProbe covers C-22.FR-2 and AC-26: the dry probe is GET <base>/v1/info without an Authorization header, on a
// client without a token and on one with a token alike; it passes on 401 with a problem and names every other answer
// in the order of the PRD, with its latency on the real clock it is given.
func TestDryProbe(t *testing.T) {
	f := startFake(t)
	for i, c := range []*Client{newClient(t, network(t, nil), Settings{BaseURL: f.URL()}),
		newClient(t, network(t, nil), Settings{BaseURL: f.URL(), Token: testToken})} {
		f.ResetRequests()
		p, err := RunDryProbe(t.Context(), c, direct, clock.NewManual(time.Time{}))
		if err != nil || !p.OK || p.Message != "" || p.Code != "" {
			t.Errorf("client %d: probe of the fake bridge = %+v, %v", i, p, err)
		}
		if reqs := f.Requests(); len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Path != "/v1/info" ||
			len(reqs[0].Headers["Authorization"]) != 0 || reqs[0].Status != http.StatusUnauthorized {
			t.Errorf("client %d: requests %+v", i, reqs)
		}
	}

	noContentType := func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Content-Type"] = nil
		_, _ = w.Write([]byte("OK"))
	}
	for _, tc := range []struct {
		name    string
		answer  http.HandlerFunc
		message string
		code    string
	}{
		{name: "401 with a problem", answer: answerJSON(http.StatusUnauthorized, problemJSON+"; charset=utf-8",
			`{"title":"Unauthorized","class":"forbidden"}`)},
		{name: "200 HTML", answer: answerJSON(http.StatusOK, "text/html; charset=utf-8", "<p>hi</p>"),
			message: "The address answered 200 with text/html, not with a problem of the Bridge protocol.",
			code:    CodeNotBridge},
		{name: "200 JSON", answer: answerJSON(http.StatusOK, "application/json", `{"protocol_version":"1"}`),
			message: "The address answered 200 with application/json, not with a problem of the Bridge protocol.",
			code:    CodeNotBridge},
		{name: "401 JSON", answer: answerJSON(http.StatusUnauthorized, "application/json", `{"title":"Unauthorized"}`),
			message: "The address answered 401 with application/json, not with a problem of the Bridge protocol.",
			code:    CodeNotBridge},
		{name: "401 problem that is not JSON", answer: answerJSON(http.StatusUnauthorized, problemJSON, "no"),
			message: "The address answered 401 with application/problem+json, not with a problem of the Bridge " +
				"protocol.", code: CodeNotBridge},
		{name: "500 JSON", answer: answerJSON(http.StatusInternalServerError, "application/json",
			`{"class":"transient"}`),
			message: "The address answered 500 with application/json, not with a problem of the Bridge protocol.",
			code:    CodeNotBridge},
		{name: "no content type", answer: noContentType,
			message: "The address answered 200 with no content type, not with a problem of the Bridge protocol.",
			code:    CodeNotBridge},
		{name: "404 problem", answer: answerJSON(http.StatusNotFound, problemJSON, `{"title":"Not Found"}`),
			message: MessageWrongPrefix, code: CodeWrongPrefix},
		{name: "404 HTML", answer: answerJSON(http.StatusNotFound, "text/html", "<p>no</p>"),
			message: MessageWrongPrefix, code: CodeWrongPrefix},
		{name: "500 problem", answer: answerJSON(http.StatusInternalServerError, problemJSON,
			`{"class":"transient","detail":"down"}`), message: "The bridge answered 500 instead of 401 with a problem.",
			code: CodeUnexpectedStatus},
		{name: "200 problem", answer: answerJSON(http.StatusOK, problemJSON, `{"title":"OK"}`),
			message: "The bridge answered 200 instead of 401 with a problem.", code: CodeUnexpectedStatus},
		{name: "407 of the server", answer: answerJSON(http.StatusProxyAuthRequired, "text/plain", "auth"),
			message: MessageProxyRefused, code: delivery.CodeProxyRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := startStandIn(t, tc.answer)
			p, err := RunDryProbe(t.Context(), newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL}), direct,
				clock.NewManual(time.Time{}))
			if err != nil || p.OK != (tc.code == "") || p.Message != tc.message || p.Code != tc.code {
				t.Errorf("probe = %+v, %v", p, err)
			}
			wantProbeOnly(t, b)
		})
	}

	realClock := clock.NewManual(time.Time{})
	b := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		realClock.Advance(42 * time.Millisecond)
		answerJSON(http.StatusOK, "text/x-"+testToken, "x")(w, r)
	})
	c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL + "/pfx", Token: testToken})
	p, err := RunDryProbe(t.Context(), c, direct, realClock)
	if err != nil || p.Latency != 42*time.Millisecond || p.Code != CodeNotBridge ||
		p.Message != "The address answered 200 with text/x-[redacted], not with a problem of the Bridge protocol." {
		t.Errorf("probe = %+v, %v", p, err)
	}
	if got := b.seen(); len(got) != 1 || got[0].Path != "/pfx/v1/info" || got[0].Header.Get("Authorization") != "" {
		t.Errorf("requests %+v", got)
	}
	boom := errors.New("limiter down")
	if _, err := RunDryProbe(t.Context(), c, func(context.Context,
		func(context.Context, delivery.Call) delivery.Outcome) (delivery.Outcome, error) {
		return delivery.Outcome{}, boom
	}, realClock); !errors.Is(err, boom) {
		t.Errorf("the runner's error = %v", err)
	}
}

// TestDryProbeWithoutAnswer covers C-22.FR-2: a dry probe without an answer has the code of delivery.FailureCode — a
// refused connection, an unknown name, an address the outbound address policy refuses — and a proxy that refuses the
// credentials, SOCKS5 or HTTP, is proxy_refused; nothing reaches the bridge.
func TestDryProbeWithoutAnswer(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + ln.Addr().String()
	_ = ln.Close()
	b := startStandIn(t, answerJSON(http.StatusUnauthorized, problemJSON, `{"title":"Unauthorized"}`))
	strict, err := outbound.ParsePolicy("standard", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked := network(t, nil)
	blocked.Policy = outbound.StaticPolicy(strict)
	dns := network(t, nil)
	dns.Resolver = failingResolver{}
	proxies := map[fakeproxy.Kind]string{}
	for _, kind := range []fakeproxy.Kind{fakeproxy.SOCKS5, fakeproxy.HTTP} {
		p, err := fakeproxy.Start(t.Context(), kind, "127.0.0.1:0", fakeproxy.Options{Username: "u", Password: "right"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Close() })
		proxies[kind] = p.Addr()
	}
	for _, tc := range []struct {
		name    string
		n       Network
		base    string
		proxy   *outbound.Proxy
		code    string
		message string
	}{
		{name: "refused", n: network(t, nil), base: refused, code: delivery.CodeConnectFailed},
		{name: "dns", n: dns, base: "http://bridge.invalid", code: delivery.CodeDNSFailed},
		{name: "blocked", n: blocked, base: b.srv.URL, code: delivery.CodeBlocked},
		{name: "socks5 refused the credentials", n: network(t, nil), base: b.srv.URL, proxy: &outbound.Proxy{
			Type: outbound.ProxySOCKS5, Address: proxies[fakeproxy.SOCKS5], Username: "u", Password: "wrong"},
			code: delivery.CodeProxyRefused, message: MessageProxyRefused},
		{name: "http proxy 407", n: network(t, nil), base: b.srv.URL, proxy: &outbound.Proxy{
			Type: outbound.ProxyHTTP, Address: proxies[fakeproxy.HTTP], Username: "u", Password: "wrong"},
			code: delivery.CodeProxyRefused, message: MessageProxyRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, tc.n, Settings{BaseURL: tc.base, Proxy: tc.proxy})
			p, err := RunDryProbe(t.Context(), c, direct, clock.NewManual(time.Time{}))
			if err != nil || p.OK || p.Code != tc.code || p.Message == "" ||
				(tc.message != "" && p.Message != tc.message) || strings.Contains(p.Message, "wrong") {
				t.Errorf("probe = %+v, %v", p, err)
			}
		})
	}
	p, err := RunDryProbe(t.Context(), newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL}),
		Direct(delivery.Call{Class: outbound.ClassHeartbeat}), clock.NewManual(time.Time{}))
	if err != nil || p.OK || p.Code != delivery.CodeRequestFailed || p.Message != "the Bridge client has no heartbeat "+
		"class" {
		t.Errorf("a class the client has not = %+v, %v", p, err)
	}
	if got := b.seen(); len(got) != 0 {
		t.Errorf("requests %+v", got)
	}
	if p := probeVerdict(outbound.Result{}, Result{Classification: Classification{Class: ClassTransient}}, ""); p.OK ||
		p.Message != checkFailed || p.Code != delivery.CodeRequestFailed {
		t.Errorf("a failure without text = %+v", p)
	}
}

// TestDryProbeNeverWaits: a rate_limited answer to the dry probe reaches the runner as Transient, without a delay or a
// scope, so that a request without a token never holds the limiter of the Connection whose limiter token the check
// took.
func TestDryProbeNeverWaits(t *testing.T) {
	for _, scope := range []string{"channel", "bot"} {
		b := startStandIn(t, answerJSON(http.StatusTooManyRequests, problemJSON,
			`{"title":"Too Many Requests","class":"rate_limited","scope":"`+scope+`"}`, "Retry-After", "30"))
		c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL})
		var got delivery.Outcome
		run := func(ctx context.Context, fn func(context.Context, delivery.Call) delivery.Outcome) (delivery.Outcome,
			error) {
			got = fn(ctx, delivery.Call{Class: outbound.ClassInteractive})
			return got, nil
		}
		p, err := RunDryProbe(t.Context(), c, run, clock.NewManual(time.Time{}))
		if err != nil || p.OK || p.Code != CodeUnexpectedStatus || got.Kind != delivery.OutcomeTransient ||
			got.RetryAfter != 0 || got.Scope != "" {
			t.Errorf("scope %s: probe = %+v, %v, outcome %+v", scope, p, err, got)
		}
		wantProbeOnly(t, b)
	}
}

// TestClientWithoutToken: a client built without a token refuses every call that would carry one before any request,
// and makes only the dry probe.
func TestClientWithoutToken(t *testing.T) {
	b := startStandIn(t, answerJSON(http.StatusUnauthorized, problemJSON, `{"title":"Unauthorized"}`))
	c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL})
	ctx, class := t.Context(), outbound.ClassInteractive
	refused := func(what string, r Result) {
		t.Helper()
		if r.OK() || r.Status != 0 || r.Classification.Class != ClassInvalid ||
			string(r.Error) != "this client has no bot token: only the dry probe runs without one" {
			t.Errorf("%s = %+v", what, r)
		}
	}
	_, r := c.GetInfo(ctx, class)
	refused("GetInfo", r)
	_, r = c.CheckChannel(ctx, class, "ch-1")
	refused("CheckChannel", r)
	_, r = c.PollEvents(ctx, nil, time.Second)
	refused("PollEvents", r)
	refused("DeleteMessage", c.DeleteMessage(ctx, class, "m-1"))
	if got := b.seen(); len(got) != 0 {
		t.Fatalf("a client without a token made requests: %+v", got)
	}
	if p, err := RunDryProbe(ctx, c, direct, clock.NewManual(time.Time{})); err != nil || !p.OK {
		t.Errorf("probe = %+v, %v", p, err)
	}
	wantProbeOnly(t, b)
}
