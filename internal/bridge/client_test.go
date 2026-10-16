// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

const testToken = "brg-s3cr3t-token-77"

// testPushSecret is longer than the part of an unknown protocol_version that a message shows.
const testPushSecret = "whsec_cHVzaC1zZWNyZXQtb2YtdGhlLWJyaWRnZS03Nw=="

// network is the Network of the tests: loopback allowed, the log in buf.
func network(t *testing.T, buf *bytes.Buffer) Network {
	t.Helper()
	p, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if buf == nil {
		buf = &bytes.Buffer{}
	}
	return Network{Policy: outbound.StaticPolicy(p), Log: logging.New(buf, logging.LevelInfo), Real: clock.Real{}}
}

func newClient(t *testing.T, n Network, s Settings) *Client {
	t.Helper()
	c, err := NewClient(n, s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// seen is a request as the stand-in bridge received it.
type seen struct {
	Method, Path, RawPath string
	Header                http.Header
	Body                  string
}

// standIn is a bridge that records its requests and answers with answer.
type standIn struct {
	mu       sync.Mutex
	requests []seen
	srv      *httptest.Server
}

func startStandIn(t *testing.T, answer http.HandlerFunc) *standIn {
	t.Helper()
	s := &standIn{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, seen{Method: r.Method, Path: r.URL.Path, RawPath: r.URL.RawPath,
			Header: r.Header.Clone(), Body: string(b)})
		s.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *standIn) seen() []seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seen(nil), s.requests...)
}

func answerJSON(status int, contentType, body string, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// TestRequestPlumbing covers the requests of every call: below the base URL's path prefix, the token as a bearer
// token, JSON in and out, extra headers, and an id percent-encoded as one path segment.
func TestRequestPlumbing(t *testing.T) {
	b := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pfx/v1/info" {
			answerJSON(http.StatusOK, "application/json", string(validInfo(t, nil)))(w, r)
			return
		}
		answerJSON(http.StatusOK, "application/json", `{"message_id":"m1","replayed":false}`)(w, r)
	})
	base, err := ParseBaseURL(b.srv.URL + "/pfx/")
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t, network(t, nil), Settings{BaseURL: base, Token: testToken})
	if c.Via() != ViaDirect {
		t.Fatalf("via %q", c.Via())
	}
	if _, r := c.GetInfo(t.Context(), outbound.ClassInteractive); !r.OK() || r.Status != http.StatusOK {
		t.Fatalf("getInfo = %+v", r)
	}
	var out struct {
		MessageID string `json:"message_id"`
	}
	_, r := c.do(t.Context(), outbound.ClassDelivery, http.MethodPost, "messages/"+Segment("a/b c?#%"),
		http.Header{"Idempotency-Key": {"k-1"}}, map[string]string{"body": "x"}, &out)
	if !r.OK() || out.MessageID != "m1" {
		t.Fatalf("post = %+v %+v", r, out)
	}
	got := b.seen()
	if len(got) != 2 {
		t.Fatalf("requests %+v", got)
	}
	info, post := got[0], got[1]
	if info.Method != http.MethodGet || info.Path != "/pfx/v1/info" ||
		info.Header.Get("Authorization") != "Bearer "+testToken ||
		!strings.Contains(info.Header.Get("Accept"), "application/json") || info.Body != "" {
		t.Errorf("info request %+v", info)
	}
	if post.Method != http.MethodPost || post.RawPath != "/pfx/v1/messages/a%2Fb%20c%3F%23%25" ||
		post.Path != "/pfx/v1/messages/a/b c?#%" || post.Header.Get("Idempotency-Key") != "k-1" ||
		post.Header.Get("Content-Type") != "application/json" || post.Body != `{"body":"x"}` ||
		post.Header.Get("Authorization") != "Bearer "+testToken {
		t.Errorf("post request %+v", post)
	}
}

// TestRedirectIsNotFollowed covers C-22.FR-8: a redirect is invalid and its Location is never fetched, so the token
// never reaches another address.
func TestRedirectIsNotFollowed(t *testing.T) {
	other := startStandIn(t, answerJSON(http.StatusOK, "application/json", `{}`))
	b := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL+"/v1/info", http.StatusFound)
	})
	c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken})
	_, r := c.GetInfo(t.Context(), outbound.ClassInteractive)
	if r.OK() || r.Status != http.StatusFound || r.Classification.Class != ClassInvalid ||
		r.CheckCode() != delivery.CodeRequestFailed || !strings.Contains(string(r.Error), "redirect") {
		t.Fatalf("redirect = %+v", r)
	}
	if len(b.seen()) != 1 || len(other.seen()) != 0 {
		t.Fatalf("requests: bridge %d, redirect target %d", len(b.seen()), len(other.seen()))
	}
}

// TestProblemAnswers covers C-22.FR-8: a problem is classified by its class, whatever its status, and its detail is
// kept as masked, untrusted text; the same body as application/json is classified by the status.
func TestProblemAnswers(t *testing.T) {
	body := `{"title":"Bad request","status":400,"class":"transient","detail":"the token ` + testToken + ` is odd"}`
	for _, tc := range []struct {
		name, contentType string
		class             Class
		masked            bool
	}{
		{"problem", "application/problem+json; charset=utf-8", ClassTransient, true},
		{"not a problem", "application/json", ClassInvalid, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			b := startStandIn(t, answerJSON(http.StatusBadRequest, tc.contentType, body))
			c := newClient(t, network(t, &buf), Settings{BaseURL: b.srv.URL, Token: testToken})
			_, r := c.GetInfo(t.Context(), outbound.ClassInteractive)
			if r.Classification.Class != tc.class || r.Status != http.StatusBadRequest ||
				r.CheckCode() != delivery.CodeRequestFailed {
				t.Fatalf("result %+v", r)
			}
			if tc.masked && string(r.Error) != "the token [redacted] is odd" {
				t.Fatalf("error %q", r.Error)
			}
			if strings.Contains(string(r.Error), testToken) || strings.Contains(buf.String(), testToken) {
				t.Fatalf("the token leaked: %q %s", r.Error, buf.String())
			}
		})
	}
	t.Run("title only", func(t *testing.T) {
		b := startStandIn(t, answerJSON(http.StatusForbidden, "application/problem+json",
			`{"title":"The bot was removed","class":"forbidden"}`))
		c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken})
		_, r := c.GetInfo(t.Context(), outbound.ClassInteractive)
		if r.Classification.Class != ClassForbidden || r.Error != "The bot was removed" ||
			r.Outcome().Kind != delivery.OutcomeFatal {
			t.Fatalf("result %+v", r)
		}
	})
	t.Run("no text", func(t *testing.T) {
		b := startStandIn(t, answerJSON(http.StatusNotFound, "application/problem+json", `{"class":"gone"}`))
		c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken})
		_, r := c.GetInfo(t.Context(), outbound.ClassInteractive)
		if r.Classification.Class != ClassGone || !strings.Contains(string(r.Error), "answered 404") {
			t.Fatalf("result %+v", r)
		}
	})
}

// TestPushSecretIsMasked covers C-22.FR-1 and AC-3: the bridge knows the push secret too, so a problem's detail or
// title and a protocol_version that echo it show [redacted], never the secret or a part of it, in the Result and the
// log.
func TestPushSecretIsMasked(t *testing.T) {
	echo := "push " + testPushSecret + " token " + testToken
	problem := func(field string) string {
		b, _ := json.Marshal(map[string]any{"status": 500, field: echo, "type": "urn:" + testPushSecret})
		return string(b)
	}
	for _, tc := range []struct {
		name   string
		answer http.HandlerFunc
		want   string
	}{
		{"detail", answerJSON(http.StatusInternalServerError, "application/problem+json", problem("detail")),
			"push [redacted] token [redacted]"},
		{"title", answerJSON(http.StatusInternalServerError, "application/problem+json", problem("title")),
			"push [redacted] token [redacted]"},
		{"protocol_version", answerJSON(http.StatusOK, "application/json",
			`{"protocol_version":"`+testPushSecret+`"}`),
			"The bridge speaks protocol version [redacted]; this Muster speaks 1."},
		{"protocol_version cut inside the secret", answerJSON(http.StatusOK, "application/json",
			`{"protocol_version":"v`+strings.Repeat("-", 20)+testPushSecret+`"}`),
			"The bridge speaks protocol version v" + strings.Repeat("-", 20) + "[redacted]; this Muster speaks 1."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			b := startStandIn(t, tc.answer)
			c := newClient(t, network(t, &buf), Settings{BaseURL: b.srv.URL, Token: testToken,
				PushSecret: testPushSecret})
			_, r := c.GetInfo(t.Context(), outbound.ClassInteractive)
			if r.OK() || string(r.Error) != tc.want {
				t.Fatalf("result %+v", r)
			}
			part := testPushSecret[len("whsec_") : len("whsec_")+12]
			if strings.Contains(string(r.Error), part) || strings.Contains(buf.String(), part) {
				t.Fatalf("the push secret leaked: %q %s", r.Error, buf.String())
			}
		})
	}
}

// TestRetryAfterAnswers: Retry-After is read in seconds and as an HTTP date on the real clock, with the problem's
// scope.
func TestRetryAfterAnswers(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	n := network(t, nil)
	n.Real = clock.NewManual(t0)
	for _, tc := range []struct {
		name, retryAfter, body string
		status                 int
		want                   time.Duration
		has                    bool
		scope                  delivery.Scope
		kind                   delivery.OutcomeKind
	}{
		{"seconds", "7", `{"class":"rate_limited","scope":"bot"}`, http.StatusTooManyRequests, 7 * time.Second,
			true, delivery.ScopeConnection, delivery.OutcomeRetryAfter},
		{"date", t0.Add(30 * time.Second).Format(http.TimeFormat), `{}`, http.StatusServiceUnavailable,
			30 * time.Second, true, delivery.ScopeDestination, delivery.OutcomeRetryAfter},
		{"none", "", `{"class":"rate_limited"}`, http.StatusBadRequest, 0, false, "", delivery.OutcomeTransient},
		{"garbage", "soon", `{}`, http.StatusTooManyRequests, 0, false, "", delivery.OutcomeTransient},
		{"negative", "-3", `{}`, http.StatusTooManyRequests, 0, false, "", delivery.OutcomeTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var h []string
			if tc.retryAfter != "" {
				h = []string{"Retry-After", tc.retryAfter}
			}
			b := startStandIn(t, answerJSON(tc.status, "application/problem+json", tc.body, h...))
			c := newClient(t, n, Settings{BaseURL: b.srv.URL, Token: testToken})
			_, r := c.GetInfo(t.Context(), outbound.ClassInteractive)
			cl := r.Classification
			if cl.Class != ClassRateLimited || cl.RetryAfter != tc.want || cl.HasRetryAfter != tc.has {
				t.Fatalf("classification %+v", cl)
			}
			if o := r.Outcome(); o.Kind != tc.kind || o.Scope != tc.scope || o.RetryAfter != tc.want {
				t.Fatalf("outcome %+v", o)
			}
		})
	}
}

// TestCallsWithoutAnswer: a call without an answer is transient with the code of what failed; an address the policy
// refuses is blocked.
func TestCallsWithoutAnswer(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + ln.Addr().String()
	_ = ln.Close()
	b := startStandIn(t, answerJSON(http.StatusOK, "application/json", `{}`))
	strict, err := outbound.ParsePolicy("standard", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked := network(t, nil)
	blocked.Policy = outbound.StaticPolicy(strict)
	expired, cancel := context.WithTimeout(t.Context(), 0)
	defer cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		n     Network
		base  string
		code  string
		class Class
		kind  delivery.OutcomeKind
	}{
		{"timeout", expired, network(t, nil), b.srv.URL, delivery.CodeTimeout, ClassTransient,
			delivery.OutcomeTransient},
		{"refused", t.Context(), network(t, nil), refused, delivery.CodeConnectFailed, ClassTransient,
			delivery.OutcomeTransient},
		{"blocked", t.Context(), blocked, b.srv.URL, delivery.CodeBlocked, ClassForbidden, delivery.OutcomeFatal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, tc.n, Settings{BaseURL: tc.base, Token: testToken})
			_, r := c.GetInfo(tc.ctx, outbound.ClassInteractive)
			if r.Status != 0 || r.CheckCode() != tc.code || r.Classification.Class != tc.class ||
				r.Outcome().Kind != tc.kind || strings.Contains(string(r.Error), testToken) {
				t.Fatalf("result %+v", r)
			}
		})
	}
}

// TestAnswersThatAreNotTheExpectedJSON: a successful answer that does not decode is invalid.
func TestAnswersThatAreNotTheExpectedJSON(t *testing.T) {
	b := startStandIn(t, answerJSON(http.StatusOK, "text/html", "<html>hello</html>"))
	c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken})
	var out struct{}
	_, r := c.do(t.Context(), outbound.ClassBackground, http.MethodGet, "events", nil, nil, &out)
	if r.Classification.Class != ClassInvalid || r.Status != http.StatusOK ||
		r.Error != "GET events: the answer is not the expected JSON" || r.Outcome().Kind != delivery.OutcomeUnknown {
		t.Fatalf("result %+v", r)
	}
	_, r = c.do(t.Context(), outbound.ClassBackground, http.MethodPost, "events", nil, func() {}, nil)
	if r.Classification.Class != ClassInvalid || !strings.Contains(string(r.Error), "not valid JSON") {
		t.Fatalf("unencodable body = %+v", r)
	}
	_, r = c.do(t.Context(), outbound.ClassHeartbeat, http.MethodGet, "info", nil, nil, nil)
	if r.Classification.Class != ClassInvalid || !strings.Contains(string(r.Error), "no heartbeat class") {
		t.Fatalf("heartbeat class = %+v", r)
	}
}

// TestThroughTheProxy: with a proxy every request goes through it.
func TestThroughTheProxy(t *testing.T) {
	b := startStandIn(t, answerJSON(http.StatusOK, "application/json", string(validInfo(t, nil))))
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken,
		Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP, Address: proxy.Addr()}})
	if c.Via() != ViaProxy {
		t.Fatalf("via %q", c.Via())
	}
	if _, r := c.GetInfo(t.Context(), outbound.ClassInteractive); !r.OK() {
		t.Fatalf("getInfo = %+v", r)
	}
	if len(proxy.Connections()) == 0 || len(b.seen()) != 1 {
		t.Fatalf("proxy %+v, bridge %d", proxy.Connections(), len(b.seen()))
	}
}

func TestNewClientRefusesABadProxyWithoutItsPassword(t *testing.T) {
	_, err := NewClient(network(t, nil), Settings{BaseURL: "http://127.0.0.1:1", Token: testToken,
		Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP, Address: "proxy-pw-77", Password: "proxy-pw-77"}})
	if err == nil || strings.Contains(err.Error(), "proxy-pw-77") {
		t.Fatalf("err %v", err)
	}
}

// TestSegment: an id is one path segment, and a segment of dots only is no step in the path, on the wire too.
func TestSegment(t *testing.T) {
	for in, want := range map[string]string{".": "%2E", "..": "%2E%2E", "...": "%2E%2E%2E", "a.b": "a.b", ".a": ".a",
		"a/..": "a%2F..", "": ""} {
		if got := Segment(in); got != want {
			t.Errorf("Segment(%q) = %q, want %q", in, got, want)
		}
	}
	b := startStandIn(t, answerJSON(http.StatusOK, "application/json", `{}`))
	c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL + "/pfx", Token: testToken})
	for _, id := range []string{".", ".."} {
		if _, r := c.do(t.Context(), outbound.ClassDelivery, http.MethodDelete, "messages/"+Segment(id), nil, nil,
			nil); !r.OK() {
			t.Fatalf("delete %q = %+v", id, r)
		}
	}
	got := b.seen()
	if len(got) != 2 || got[0].RawPath != "/pfx/v1/messages/%2E" || got[1].RawPath != "/pfx/v1/messages/%2E%2E" ||
		got[1].Path != "/pfx/v1/messages/.." {
		t.Fatalf("requests %+v", got)
	}
}

func TestParseBaseURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://bridge:8080/", "http://bridge:8080"},
		{" https://bridge.example.org/prefix/// ", "https://bridge.example.org/prefix"},
		{"https://bridge.example.org/a%2Fb/", "https://bridge.example.org/a%2Fb"},
		{"http://bridge/?q=1", ""},
		{"http://bridge/#f", ""},
		{"http://user:pw@bridge/", ""},
		{"ftp://bridge/", ""},
		{"http:///path", ""},
		{"bridge:8080", ""},
		{"mailto:a@b", ""},
	} {
		got, err := ParseBaseURL(tc.in)
		if got != tc.want || (tc.want == "") != (err != nil) {
			t.Errorf("ParseBaseURL(%q) = %q, %v", tc.in, got, err)
		}
	}
}

// TestOutcome covers the outcomes of C-22.FR-8 for delivery.
func TestOutcome(t *testing.T) {
	for _, tc := range []struct {
		c     Classification
		kind  delivery.OutcomeKind
		scope delivery.Scope
	}{
		{Classification{}, delivery.OutcomeOK, ""},
		{Classification{Class: ClassRateLimited, RetryAfter: DefaultRetryAfterMax, HasRetryAfter: true,
			Scope: ScopeBot}, delivery.OutcomeRetryAfter, delivery.ScopeConnection},
		{Classification{Class: ClassRateLimited, RetryAfter: DefaultRetryAfterMax + time.Second, HasRetryAfter: true,
			Scope: ScopeBot}, delivery.OutcomeTransient, ""},
		{Classification{Class: ClassTransient}, delivery.OutcomeTransient, ""},
		{Classification{Class: ClassRateLimited, RetryAfter: time.Second, HasRetryAfter: true, Scope: ScopeChannel},
			delivery.OutcomeRetryAfter, delivery.ScopeDestination},
		{Classification{Class: ClassRateLimited, RetryAfter: time.Second, HasRetryAfter: true, Scope: ScopeBot},
			delivery.OutcomeRetryAfter, delivery.ScopeConnection},
		{Classification{Class: ClassRateLimited, Scope: ScopeBot}, delivery.OutcomeTransient, ""},
		{Classification{Class: ClassGone, Reason: ReasonEditWindowClosed}, delivery.OutcomeGone, ""},
		{Classification{Class: ClassForbidden}, delivery.OutcomeFatal, ""},
		{Classification{Class: ClassInvalid}, delivery.OutcomeUnknown, ""},
		{Classification{Class: "other"}, delivery.OutcomeTransient, ""},
	} {
		o := Result{Classification: tc.c, Error: "why", Status: 500}.Outcome()
		if o.Kind != tc.kind || o.Scope != tc.scope || (o.Kind != delivery.OutcomeOK && o.Error != "why") ||
			o.Status != 500 || (o.Kind == delivery.OutcomeRetryAfter) != (o.RetryAfter > 0) ||
			(o.Kind == delivery.OutcomeRetryAfter && o.RetryAfter != tc.c.RetryAfter) {
			t.Errorf("%+v: %+v", tc.c, o)
		}
	}
	if (Result{}).CheckCode() != "" {
		t.Error("a call that succeeded has a check code")
	}
}
