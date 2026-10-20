// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

const (
	testBot     = "xoxb-test-bot-s3cr3t-77"
	testApp     = "xapp-test-app-s3cr3t-77"
	testSigning = "slack-signing-s3cr3t-77"
)

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

// to is the Route that sends every Connection to base.
func to(base string) Route { return func(logging.Secret) string { return base } }

// settings are the Settings of a Connection with both tokens and the Slack signing secret, routed to base.
func settings(base string) Settings {
	return Settings{BotToken: testBot, AppToken: testApp, SigningSecret: testSigning, Route: to(base)}
}

// seen is a request as the stand-in received it.
type seen struct {
	Method, Path string
	Header       http.Header
	Body         string
}

// standIn is a Slack that records its requests and answers with answer.
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
		s.requests = append(s.requests, seen{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(),
			Body: string(b)})
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

// base is the base URL of the stand-in, as a Route answers it.
func (s *standIn) base() string { return s.srv.URL + "/api/" }

// answerJSON answers status with body as JSON and the header pairs.
func answerJSON(status int, body string, header ...string) http.HandlerFunc {
	return answerAs(status, "application/json; charset=utf-8", body, header...)
}

func answerAs(status int, contentType, body string, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// TestRequestPlumbing covers C-24.FR-1: every call is a POST of `<base><method>` with its token as a bearer token and
// Muster's User-Agent; the read methods are form-encoded, the write methods (`chat.*`, `conversations.open`) JSON, and
// CallApp sends the app-level token.
func TestRequestPlumbing(t *testing.T) {
	s := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth.test":
			answerJSON(http.StatusOK, `{"ok":true,"url":"https://w.slack.com/","team":"W","user":"bot",`+
				`"team_id":"T1","user_id":"U1","bot_id":"B1"}`, "X-OAuth-Scopes", "chat:write")(w, r)
		case "/api/bots.info":
			answerJSON(http.StatusOK, `{"ok":true,"bot":{"id":"B1","name":"bot","app_id":"A1","user_id":"U1"}}`)(w, r)
		default:
			answerJSON(http.StatusOK, `{"ok":true}`)(w, r)
		}
	})
	c := newClient(t, network(t, nil), settings(s.base()))
	if c.Via() != ViaDirect || c.Base() != s.base() {
		t.Fatalf("via %q base %q", c.Via(), c.Base())
	}
	ctx := t.Context()
	at, r := c.AuthTest(ctx, outbound.ClassInteractive)
	if !r.OK() || at != (AuthTest{URL: "https://w.slack.com/", Team: "W", User: "bot", TeamID: "T1", UserID: "U1",
		BotID: "B1"}) || fmt.Sprint(r.Scopes) != "[chat:write]" {
		t.Fatalf("auth.test = %+v %+v", at, r)
	}
	if bot, r := c.BotsInfo(ctx, outbound.ClassInteractive, "B1"); !r.OK() || bot.AppID != "A1" || bot.ID != "B1" {
		t.Fatalf("bots.info = %+v %+v", bot, r)
	}
	for _, class := range []outbound.Class{outbound.ClassDelivery, outbound.ClassBackground} {
		if r := c.Call(ctx, class, "chat.postMessage", Args{"channel": "C1", "text": "hi"}); !r.OK() {
			t.Fatalf("chat.postMessage = %+v", r)
		}
	}
	if r := c.Call(ctx, outbound.ClassDelivery, "conversations.open", Args{"users": "U1"}); !r.OK() {
		t.Fatalf("conversations.open = %+v", r)
	}
	if r := c.Call(ctx, outbound.ClassDelivery, "chat.update", nil); !r.OK() {
		t.Fatalf("chat.update = %+v", r)
	}
	if _, r := c.AppAuthTest(ctx, outbound.ClassInteractive); !r.OK() {
		t.Fatalf("app auth.test = %+v", r)
	}
	if r := c.CallApp(ctx, outbound.ClassInteractive, "apps.connections.open", nil); !r.OK() {
		t.Fatalf("apps.connections.open = %+v", r)
	}
	form, js := "application/x-www-form-urlencoded", "application/json; charset=utf-8"
	want := []struct{ path, token, contentType, body string }{
		{"/api/auth.test", testBot, form, ""},
		{"/api/bots.info", testBot, form, "bot=B1"},
		{"/api/chat.postMessage", testBot, js, `{"channel":"C1","text":"hi"}`},
		{"/api/chat.postMessage", testBot, js, `{"channel":"C1","text":"hi"}`},
		{"/api/conversations.open", testBot, js, `{"users":"U1"}`},
		{"/api/chat.update", testBot, js, `{}`},
		{"/api/auth.test", testApp, form, ""},
		{"/api/apps.connections.open", testApp, form, ""},
	}
	got := s.seen()
	if len(got) != len(want) {
		t.Fatalf("requests %+v", got)
	}
	for i, w := range want {
		g := got[i]
		if g.Method != http.MethodPost || g.Path != w.path || g.Header.Get("Authorization") != "Bearer "+w.token ||
			g.Header.Get("Content-Type") != w.contentType || g.Body != w.body ||
			g.Header.Get("Accept") != "application/json" || !strings.HasPrefix(g.Header.Get("User-Agent"), "Muster/") {
			t.Errorf("request %d = %+v, want %+v", i, g, w)
		}
	}
}

// resolver records the names it is asked for and resolves none, so that nothing reaches slack.com.
type resolver struct {
	mu    sync.Mutex
	hosts []string
}

func (r *resolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (r *resolver) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.hosts...)
}

// records is an outbound.Recorder that keeps what it gets.
type records struct {
	mu   sync.Mutex
	list []outbound.Record
}

func (r *records) Record(_ context.Context, rec outbound.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, rec)
}

func (r *records) got() []outbound.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]outbound.Record(nil), r.list...)
}

// TestFixedAddressAndRoute covers C-24.FR-1: a token goes only to https://slack.com/api/<method>; a Route sends the
// Connections of the tokens it knows elsewhere, as `muster dev` sends those of the fake's published tokens to the fake,
// and any other to Slack; a Route answer that is not a base URL is refused.
func TestFixedAddressAndRoute(t *testing.T) {
	res, rec := &resolver{}, &records{}
	n := network(t, nil)
	n.Resolver, n.Recorder = res, rec
	owner := outbound.Owner{Connection: 7, PublicID: "CN0000000000SL"}
	c := newClient(t, n, Settings{BotToken: testBot, Owner: owner})
	if c.Base() != BaseURL {
		t.Fatalf("base %q", c.Base())
	}
	_, r := c.AuthTest(t.Context(), outbound.ClassInteractive)
	if r.OK() || r.Network != outbound.NetworkDNS || r.CheckCode() != delivery.CodeDNSFailed ||
		fmt.Sprint(res.got()) != "[slack.com]" {
		t.Fatalf("auth.test = %+v, resolved %v", r, res.got())
	}
	if got := rec.got(); len(got) != 1 || got[0].URL != "https://slack.com/api/auth.test" ||
		got[0].Operation != "auth.test" || got[0].Owner != owner {
		t.Fatalf("records %+v", got)
	}
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true,"bot_id":"B1"}`))
	route := func(token logging.Secret) string {
		if token == testBot {
			return s.base()
		}
		return ""
	}
	routed := newClient(t, n, Settings{BotToken: testBot, Route: route})
	if _, r := routed.AuthTest(t.Context(), outbound.ClassInteractive); !r.OK() || len(s.seen()) != 1 {
		t.Fatalf("routed = %+v, %d requests", r, len(s.seen()))
	}
	other := newClient(t, n, Settings{BotToken: "xoxb-other", Route: route})
	if _, r := other.AuthTest(t.Context(), outbound.ClassInteractive); r.Network != outbound.NetworkDNS ||
		len(s.seen()) != 1 || other.Base() != BaseURL || fmt.Sprint(res.got()) != "[slack.com slack.com]" {
		t.Fatalf("other token = %+v, %d requests, resolved %v", r, len(s.seen()), res.got())
	}
	for _, bad := range []string{"ftp://h/api/", "http:///api/", "http://h/api/?q=1", "http://h/api/#f",
		"http://u:p@h/api/", "::"} {
		if _, err := NewClient(n, Settings{BotToken: testBot, Route: to(bad)}); err == nil {
			t.Errorf("route %q was taken", bad)
		}
	}
}

// TestRedirectIsNotFollowed: a redirect is unknown and its Location is never fetched, so a token never reaches another
// address.
func TestRedirectIsNotFollowed(t *testing.T) {
	other := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true}`))
	s := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.base()+"auth.test", http.StatusFound)
	})
	c := newClient(t, network(t, nil), settings(s.base()))
	_, r := c.AuthTest(t.Context(), outbound.ClassInteractive)
	if r.OK() || r.Status != http.StatusFound || r.Classification.Class != ClassUnknown ||
		r.CheckCode() != delivery.CodeRequestFailed || !strings.Contains(string(r.Error), "redirect") ||
		r.Outcome().Kind != delivery.OutcomeUnknown {
		t.Fatalf("redirect = %+v", r)
	}
	if len(s.seen()) != 1 || len(other.seen()) != 0 {
		t.Fatalf("requests: Slack %d, redirect target %d", len(s.seen()), len(other.seen()))
	}
}

// TestRetryAfter covers C-24.FR-8 and F-099: a 429 waits for its Retry-After, in seconds or as a date on the real
// clock, holding the whole Connection; without one it is Transient.
func TestRetryAfter(t *testing.T) {
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	body := `{"ok":false,"error":"ratelimited"}`
	for _, tc := range []struct {
		name, retryAfter string
		class            Class
		want             time.Duration
		kind             delivery.OutcomeKind
	}{
		{"seconds", "30", ClassRetryAfter, 30 * time.Second, delivery.OutcomeRetryAfter},
		{"date", t0.Add(20 * time.Second).Format(http.TimeFormat), ClassRetryAfter, 20 * time.Second,
			delivery.OutcomeRetryAfter},
		{"none", "", ClassTransient, 0, delivery.OutcomeTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var h []string
			if tc.retryAfter != "" {
				h = []string{"Retry-After", tc.retryAfter}
			}
			s := startStandIn(t, answerJSON(http.StatusTooManyRequests, body, h...))
			n := network(t, nil)
			n.Real = clock.NewManual(t0)
			c := newClient(t, n, settings(s.base()))
			_, r := c.AuthTest(t.Context(), outbound.ClassInteractive)
			cl := r.Classification
			if cl.Class != tc.class || cl.RetryAfter != tc.want || r.ErrorCode != "ratelimited" ||
				r.Error != "ratelimited" || r.Status != http.StatusTooManyRequests {
				t.Fatalf("result %+v", r)
			}
			o := r.Outcome()
			if o.Kind != tc.kind || o.RetryAfter != tc.want || o.Error != "ratelimited" ||
				(tc.kind == delivery.OutcomeRetryAfter) != (o.Scope == delivery.ScopeConnection) {
				t.Fatalf("outcome %+v", o)
			}
		})
	}
}

// TestOKFalse covers C-24.FR-8 and F-070: an `ok: false` answer is classified by its error, whatever its status, with
// needed kept in the reason; its scopes header is kept; a successful answer keeps its warning, its cursor and its
// scopes; and the outcome outbound counts and records is that of the error.
func TestOKFalse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		class      Class
		err        string
		outcome    outbound.Outcome
	}{
		{"invalid_auth", `{"ok":false,"error":"invalid_auth"}`, http.StatusOK, ClassFatal, "invalid_auth",
			outbound.OutcomeFatal},
		{"missing_scope", `{"ok":false,"error":"missing_scope","needed":"channels:history,groups:history",` +
			`"provided":"chat:write"}`, http.StatusOK, ClassFatal,
			"missing_scope (needed: channels:history,groups:history)", outbound.OutcomeFatal},
		{"internal_error", `{"ok":false,"error":"internal_error"}`, http.StatusOK, ClassTransient, "internal_error",
			outbound.OutcomeTransient},
		{"invalid_blocks", `{"ok":false,"error":"invalid_blocks"}`, http.StatusOK, ClassMarkupRejected,
			"invalid_blocks", outbound.OutcomeUnknown},
		{"message_not_found", `{"ok":false,"error":"message_not_found"}`, http.StatusOK, ClassGoneCandidate,
			"message_not_found", outbound.OutcomeUnknown},
		{"unknown error", `{"ok":false,"error":"foo_bar"}`, http.StatusOK, ClassUnknown, "foo_bar",
			outbound.OutcomeUnknown},
		{"error with 400", `{"ok":false,"error":"invalid_auth"}`, http.StatusBadRequest, ClassFatal, "invalid_auth",
			outbound.OutcomeFatal},
		{"error with 503", `{"ok":false,"error":"service_unavailable"}`, http.StatusServiceUnavailable,
			ClassTransient, "service_unavailable", outbound.OutcomeTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startStandIn(t, answerJSON(tc.status, tc.body, "X-OAuth-Scopes", "chat:write, users:read"))
			rec := &records{}
			n := network(t, nil)
			n.Recorder = rec
			st := settings(s.base())
			st.Owner = outbound.Owner{Connection: 1}
			c := newClient(t, n, st)
			_, r := c.AuthTest(t.Context(), outbound.ClassInteractive)
			code, _, _ := strings.Cut(tc.err, " ")
			if r.OK() || r.Classification.Class != tc.class || string(r.Error) != tc.err || r.ErrorCode != code ||
				string(r.Classification.Reason) != tc.err || r.Status != tc.status ||
				fmt.Sprint(r.Scopes) != "[chat:write users:read]" || r.CheckCode() != delivery.CodeRequestFailed {
				t.Fatalf("result %+v", r)
			}
			if got := rec.got(); len(got) != 1 || got[0].Outcome != tc.outcome {
				t.Fatalf("records %+v", got)
			}
		})
	}
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true,"warning":"superfluous_charset",`+
		`"response_metadata":{"next_cursor":"dXNlcjpVMEc5V0ZYTlo="}}`, "X-OAuth-Scopes", "chat:write,,users:read",
		"X-Accepted-OAuth-Scopes", "users:read"))
	c := newClient(t, network(t, nil), settings(s.base()))
	r := c.Call(t.Context(), outbound.ClassInteractive, "users.list", nil)
	if !r.OK() || r.Warning != "superfluous_charset" || r.NextCursor != "dXNlcjpVMEc5V0ZYTlo=" ||
		fmt.Sprint(r.Scopes) != "[chat:write users:read]" || fmt.Sprint(r.AcceptedScopes) != "[users:read]" ||
		r.Error != "" || r.ErrorCode != "" || r.CheckCode() != "" || len(r.Body) == 0 {
		t.Fatalf("result %+v", r)
	}
	var out struct {
		Warning string `json:"warning"`
	}
	if err := r.Decode(&out); err != nil || out.Warning != "superfluous_charset" {
		t.Fatalf("decode %+v %v", out, err)
	}
}

// TestLongReasonIsCutOnce: the reason of an `ok: false` answer with a long error and a long needed is cut once to
// maxText, after the secrets in both are masked.
func TestLongReasonIsCutOnce(t *testing.T) {
	needed := testBot + "," + strings.Repeat("x", 2*maxText)
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":false,"error":"missing_scope`+strings.Repeat("e", maxText)+
		`","needed":"`+needed+`"}`))
	c := newClient(t, network(t, nil), settings(s.base()))
	_, r := c.AuthTest(t.Context(), outbound.ClassInteractive)
	reason := string(r.Classification.Reason)
	if len(reason) != maxText+len("…") || !strings.HasPrefix(reason, "missing_scope") || strings.Contains(reason,
		"(needed") || r.Error != r.Classification.Reason || len(r.ErrorCode) != maxText+len("…") {
		t.Fatalf("reason %d %q, error code %d", len(reason), reason[:32], len(r.ErrorCode))
	}
	s = startStandIn(t, answerJSON(http.StatusOK, `{"ok":false,"error":"missing_scope","needed":"`+needed+`"}`))
	c = newClient(t, network(t, nil), settings(s.base()))
	_, r = c.AuthTest(t.Context(), outbound.ClassInteractive)
	reason = string(r.Classification.Reason)
	if len(reason) != maxText+len("…") || r.Classification.Class != ClassFatal ||
		!strings.HasPrefix(reason, "missing_scope (needed: [redacted],xxx") || strings.Contains(reason, testBot) {
		t.Fatalf("reason %d %q", len(reason), reason[:48])
	}
}

// TestNotSlackJSON: an answer that is not Slack's JSON is unknown, a 5xx transient, whatever its body says; a
// successful answer whose members are not what the method answers is unknown; an answer larger than the limit is not
// read.
func TestNotSlackJSON(t *testing.T) {
	for _, tc := range []struct {
		name        string
		answer      http.HandlerFunc
		class       Class
		err, status string
	}{
		{"html", answerAs(http.StatusOK, "text/html", "<html>hi</html>"), ClassUnknown,
			"auth.test: the answer is not Slack's JSON", "200"},
		{"other json", answerJSON(http.StatusOK, `{"foo":1}`), ClassUnknown,
			"auth.test: the answer is not Slack's JSON", "200"},
		{"ok false without error", answerJSON(http.StatusOK, `{"ok":false}`), ClassUnknown,
			"auth.test: Slack answered ok: false without an error", "200"},
		{"html 500", answerAs(http.StatusInternalServerError, "text/html", "<html>down</html>"), ClassTransient,
			"answered 500", "500"},
		{"html 404", answerAs(http.StatusNotFound, "text/html", "<html>no</html>"), ClassUnknown, "answered 404",
			"404"},
		{"429 without error", answerJSON(http.StatusTooManyRequests, `{"ok":false}`), ClassTransient, "answered 429",
			"429"},
		{"members", answerJSON(http.StatusOK, `{"ok":true,"team":5}`), ClassUnknown,
			"auth.test: the answer is not the expected JSON", "200"},
		{"too large", answerJSON(http.StatusOK, `{"ok":true,"team":"`+strings.Repeat("x", maxAnswerBytes)+`"}`),
			ClassUnknown, "larger than", "200"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startStandIn(t, tc.answer)
			c := newClient(t, network(t, nil), settings(s.base()))
			_, r := c.AuthTest(t.Context(), outbound.ClassInteractive)
			if r.Classification.Class != tc.class || !strings.Contains(string(r.Error), tc.err) ||
				fmt.Sprint(r.Status) != tc.status {
				t.Fatalf("result %+v", r)
			}
		})
	}
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true}`))
	c := newClient(t, network(t, nil), settings(s.base()))
	if _, r := c.BotsInfo(t.Context(), outbound.ClassInteractive, "B1"); r.Classification.Class != ClassUnknown ||
		r.Error != "bots.info: the answer is not the expected JSON" {
		t.Fatalf("bots.info without a bot = %+v", r)
	}
}

// TestTokensAreMasked covers C-24.FR-1 and AC-3: both tokens and the Slack signing secret that Slack's answers echo —
// in error, needed and warning, an HTML 500, the members of auth.test and bots.info and a header — are [redacted]
// in every text a call returns and in the log.
func TestTokensAreMasked(t *testing.T) {
	echo := "auth Bearer " + testBot + " app " + testApp + " signing " + testSigning
	for _, tc := range []struct {
		name   string
		answer http.HandlerFunc
	}{
		{"error", answerJSON(http.StatusOK, `{"ok":false,"error":"invalid_auth `+echo+`","needed":"`+echo+
			`","warning":"`+echo+`"}`, "X-OAuth-Scopes", echo)},
		{"html 500", answerAs(http.StatusInternalServerError, "text/html", "<html>"+echo+"</html>")},
		{"members", answerJSON(http.StatusOK, `{"ok":true,"team":"`+echo+`","user":"`+echo+`","bot_id":"`+echo+
			`","app_id":"`+echo+`","bot":{"id":"`+echo+`","app_id":"`+echo+`","name":"`+echo+`"},"warning":"`+echo+
			`","response_metadata":{"next_cursor":"`+echo+`"}}`, "X-OAuth-Scopes", echo)},
		{"429", answerJSON(http.StatusTooManyRequests, `{"ok":false,"error":"`+echo+`"}`, "Retry-After", "1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := startStandIn(t, tc.answer)
			c := newClient(t, network(t, &buf), settings(s.base()))
			at, r1 := c.AuthTest(t.Context(), outbound.ClassInteractive)
			app, r2 := c.AppAuthTest(t.Context(), outbound.ClassInteractive)
			bot, r3 := c.BotsInfo(t.Context(), outbound.ClassInteractive, "B1")
			r4 := c.Call(t.Context(), outbound.ClassDelivery, "chat.postMessage", Args{"text": testSigning})
			text := fmt.Sprintf("%+v %+v %+v %+v %+v %+v %+v %s", at, app, bot,
				[]Result{r1, r2, r3, r4}, r1.Outcome(), r2.Classification, r4.Warning, buf.String())
			for _, r := range []Result{r1, r2, r3, r4} {
				text += " " + string(r.Body)
			}
			for _, secret := range []string{testBot, testApp, testSigning} {
				if strings.Contains(text, secret) {
					t.Fatalf("%s leaked: %s", secret, text)
				}
			}
			if tc.name != "html 500" && !strings.Contains(text, "[redacted]") {
				t.Fatalf("nothing was masked: %s", text)
			}
		})
	}
}

// TestCallsWithoutAnswer: a call without an answer is transient with the code of what failed; an address the policy
// refuses is blocked and fatal; a request that cannot be built is unknown.
func TestCallsWithoutAnswer(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + ln.Addr().String() + "/api/"
	_ = ln.Close()
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true}`))
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
		{"timeout", expired, network(t, nil), s.base(), delivery.CodeTimeout, ClassTransient,
			delivery.OutcomeTransient},
		{"refused", t.Context(), network(t, nil), refused, delivery.CodeConnectFailed, ClassTransient,
			delivery.OutcomeTransient},
		{"blocked", t.Context(), blocked, s.base(), delivery.CodeBlocked, ClassFatal, delivery.OutcomeFatal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, tc.n, settings(tc.base))
			_, r := c.AuthTest(tc.ctx, outbound.ClassInteractive)
			if r.Status != 0 || r.CheckCode() != tc.code || r.Classification.Class != tc.class ||
				r.Outcome().Kind != tc.kind || strings.Contains(string(r.Error), testBot) || r.Error == "" {
				t.Fatalf("result %+v", r)
			}
		})
	}
	c := newClient(t, network(t, nil), settings(s.base()))
	if r := c.Call(t.Context(), outbound.ClassInteractive, "bad\x7fmethod", nil); r.Classification.Class != ClassUnknown ||
		r.Status != 0 || r.CheckCode() != delivery.CodeRequestFailed {
		t.Fatalf("a request that cannot be built = %+v", r)
	}
}

// TestThroughTheProxy covers C-24.AC-3: with a proxy every request goes through it; a proxy that refuses the
// credentials is a call without an answer with its own message.
func TestThroughTheProxy(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true,"bot_id":"B1"}`))
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0",
		fakeproxy.Options{Username: "u", Password: "proxy-pw-right"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	st := settings(s.base())
	st.Proxy = &outbound.Proxy{Type: outbound.ProxyHTTP, Address: proxy.Addr(), Username: "u",
		Password: "proxy-pw-right"}
	c := newClient(t, network(t, nil), st)
	if c.Via() != ViaProxy {
		t.Fatalf("via %q", c.Via())
	}
	if _, r := c.AuthTest(t.Context(), outbound.ClassInteractive); !r.OK() {
		t.Fatalf("auth.test = %+v", r)
	}
	if len(proxy.Connections()) == 0 || len(s.seen()) != 1 {
		t.Fatalf("proxy %+v, Slack %d", proxy.Connections(), len(s.seen()))
	}
	st.Proxy = &outbound.Proxy{Type: outbound.ProxyHTTP, Address: proxy.Addr(), Username: "u",
		Password: "proxy-pw-wrong"}
	c = newClient(t, network(t, nil), st)
	_, r := c.AuthTest(t.Context(), outbound.ClassInteractive)
	if r.Status != 0 || r.Network != outbound.NetworkProxyAuth || r.Error != MessageProxyRefused ||
		r.CheckCode() != delivery.CodeProxyRefused || r.Classification.Class != ClassTransient || len(s.seen()) != 1 {
		t.Fatalf("refused = %+v", r)
	}
	if _, err := NewClient(network(t, nil), Settings{BotToken: testBot, Proxy: &outbound.Proxy{
		Type: "ftp", Address: "proxy-pw-77", Password: "proxy-pw-77"}}); err == nil ||
		strings.Contains(err.Error(), "proxy-pw-77") {
		t.Fatalf("a bad proxy: %v", err)
	}
}

// TestRequestLog covers C-24.FR-14 and C-23.FR-2: every request is recorded under the Connection with the method as
// its operation, Slack's address whole, the tokens [redacted] in its headers and bodies; a client without an Owner
// records nothing.
func TestRequestLog(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true,"bot_id":"B1","echo":"`+testApp+`"}`))
	rec := &records{}
	n := network(t, nil)
	n.Recorder = rec
	owner := outbound.Owner{Connection: 11, PublicID: "CN0000000000AB"}
	st := settings(s.base())
	st.Owner = owner
	c := newClient(t, n, st)
	ctx, class := t.Context(), outbound.ClassInteractive
	_, _ = c.AuthTest(ctx, class)
	_, _ = c.AppAuthTest(ctx, class)
	_, _ = c.BotsInfo(ctx, class, "B1")
	_ = c.Call(ctx, class, "chat.postMessage", Args{"text": "signed " + testSigning})
	var ops []string
	for _, r := range rec.got() {
		ops = append(ops, r.Operation)
		text := fmt.Sprintf("%s %v %s %s %s", r.URL, r.RequestHeader, r.RequestBody, r.ResponseBody, r.Error)
		if r.Owner != owner || r.Via != ViaDirect || r.Method != http.MethodPost || r.Status != http.StatusOK ||
			r.RequestHeader.Get("Authorization") != "[redacted]" || r.URL != s.base()+r.Operation ||
			strings.Contains(text, testBot) || strings.Contains(text, testApp) ||
			strings.Contains(text, testSigning) {
			t.Errorf("record %+v", r)
		}
	}
	if fmt.Sprint(ops) != "[auth.test auth.test bots.info chat.postMessage]" {
		t.Fatalf("operations %v", ops)
	}
	quiet := newClient(t, n, settings(s.base()))
	if _, r := quiet.AuthTest(ctx, class); !r.OK() || len(rec.got()) != 4 {
		t.Fatalf("a client without an Owner recorded: %+v %d", r, len(rec.got()))
	}
}

// TestFormArgs: the arguments of a read method are form-encoded — text, bool, int, int64 and a list of texts joined
// with commas — and one of another type makes no request; JSON arguments that cannot be encoded make none either.
func TestFormArgs(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true}`))
	c := newClient(t, network(t, nil), settings(s.base()))
	ctx := t.Context()
	if r := c.Call(ctx, outbound.ClassInteractive, "conversations.list", Args{"cursor": "c 1", "exclude_archived": true,
		"limit": 200, "count": int64(5), "types": []string{"public_channel", "private_channel"}}); !r.OK() {
		t.Fatalf("result %+v", r)
	}
	if got := s.seen(); len(got) != 1 || got[0].Body !=
		"count=5&cursor=c+1&exclude_archived=true&limit=200&types=public_channel%2Cprivate_channel" {
		t.Fatalf("requests %+v", got)
	}
	for _, tc := range []struct {
		method string
		args   Args
		want   string
	}{
		{"users.info", Args{"user": struct{}{}}, "users.info: the argument user cannot be form-encoded"},
		{"chat.postMessage", Args{"text": func() {}}, "chat.postMessage: the arguments are not valid JSON"},
	} {
		if r := c.Call(ctx, outbound.ClassInteractive, tc.method, tc.args); r.Classification.Class != ClassUnknown ||
			string(r.Error) != tc.want {
			t.Errorf("%s = %+v", tc.method, r)
		}
	}
	if len(s.seen()) != 1 {
		t.Fatalf("a call that could not be encoded was made: %+v", s.seen())
	}
}

// TestNoToken: a client without a token makes no call with it, and one in a class it has not makes none at all.
func TestNoToken(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true}`))
	c := newClient(t, network(t, nil), Settings{BotToken: testBot, Route: to(s.base())})
	if _, r := c.AppAuthTest(t.Context(), outbound.ClassInteractive); r.Classification.Class != ClassUnknown ||
		r.Error != "this client has no app-level token" {
		t.Fatalf("app-level token = %+v", r)
	}
	noBot := newClient(t, network(t, nil), Settings{AppToken: testApp, Route: to(s.base())})
	if r := noBot.Call(t.Context(), outbound.ClassInteractive, "auth.test", nil); r.Error !=
		"this client has no bot token" {
		t.Fatalf("bot token = %+v", r)
	}
	if r := c.Call(t.Context(), outbound.ClassHeartbeat, "auth.test", nil); r.Classification.Class != ClassUnknown ||
		!strings.Contains(string(r.Error), "no heartbeat class") {
		t.Fatalf("heartbeat class = %+v", r)
	}
	if len(s.seen()) != 0 {
		t.Fatalf("requests %+v", s.seen())
	}
}

// TestCut: a long text of Slack's is cut on a rune boundary and made valid UTF-8.
func TestCut(t *testing.T) {
	long := strings.Repeat("a", maxText-1) + "é" + "tail"
	if got := cut(long); got != strings.Repeat("a", maxText-1)+"…" {
		t.Fatalf("cut = %q", got[len(got)-8:])
	}
	if got := cut("a\xffb"); got != "a�b" {
		t.Fatalf("cut = %q", got)
	}
}
