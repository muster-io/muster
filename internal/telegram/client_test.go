// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

const (
	testToken  = "777001:AAE-s3cr3t-token"
	otherToken = "777002:AAE-other-token"
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

func startFake(t *testing.T) *faketelegram.Fake {
	t.Helper()
	f := faketelegram.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(t.Context()) })
	return f
}

func newClient(t *testing.T, s Settings) *Client {
	t.Helper()
	c, err := NewClient(network(t, nil), s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// paths are the paths the fake recorded.
func paths(f *faketelegram.Fake) []string {
	var out []string
	for _, r := range f.Requests() {
		out = append(out, r.Path)
	}
	return out
}

func TestGetMeAndWebhookInfoAgainstTheFake(t *testing.T) {
	f := startFake(t)
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	u, r := c.GetMe(t.Context(), outbound.ClassInteractive)
	if !r.OK() || u.Username != faketelegram.BotUsername || u.ID != faketelegram.BotID || c.Via() != ViaDirect {
		t.Fatalf("getMe = %+v %+v", u, r)
	}
	info, r := c.GetWebhookInfo(t.Context(), outbound.ClassInteractive)
	if !r.OK() || info.URL != "" || info.PendingUpdateCount != 0 {
		t.Fatalf("getWebhookInfo = %+v %+v", info, r)
	}
	if got := paths(f); !slices.Equal(got, []string{"/bot" + testToken + "/getMe", "/bot" + testToken + "/getWebhookInfo"}) {
		t.Fatalf("paths %v", got)
	}
	if c.Origin() != f.URL() {
		t.Fatalf("origin %q", c.Origin())
	}
}

// TestPathPrefixAndProxy covers C-14.AC-10: with a path prefix every request goes to <base>/bot<token>/<method>; with a
// SOCKS5 proxy only through the proxy.
func TestPathPrefixAndProxy(t *testing.T) {
	f := startFake(t)
	prefix := "/k3x9"
	if err := f.SetConfig(&prefix, nil, nil); err != nil {
		t.Fatal(err)
	}
	socks, err := fakeproxy.Start(t.Context(), fakeproxy.SOCKS5, "127.0.0.1:0", fakeproxy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socks.Close() })
	base, err := ParseBaseURL(f.URL() + "/k3x9/")
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t, Settings{BaseURL: base, Token: testToken,
		Proxy: &outbound.Proxy{Type: outbound.ProxySOCKS5, Address: socks.Addr()}})
	if c.Via() != ViaProxy {
		t.Fatalf("via %q", c.Via())
	}
	if _, r := c.GetMe(t.Context(), outbound.ClassInteractive); !r.OK() {
		t.Fatalf("getMe = %+v", r)
	}
	if r := c.DryProbe(t.Context(), outbound.ClassInteractive); r.Status != http.StatusUnauthorized {
		t.Fatalf("dry probe = %+v", r)
	}
	if _, r := c.GetUpdates(t.Context(), nil, 0); !r.OK() {
		t.Fatalf("getUpdates = %+v", r)
	}
	want := []string{"/k3x9/bot" + testToken + "/getMe", "/k3x9/bot0:x/getMe", "/k3x9/bot" + testToken + "/getUpdates"}
	if got := paths(f); !slices.Equal(got, want) {
		t.Fatalf("paths %v, want %v", got, want)
	}
	for _, conn := range socks.Connections() {
		if conn.Target != strings.TrimPrefix(f.URL(), "http://") {
			t.Fatalf("proxy target %q", conn.Target)
		}
	}
	if len(socks.Connections()) == 0 {
		t.Fatal("nothing went through the proxy")
	}
}

// TestRefusedConnectionIsRedacted covers C-14.FR-12 and AC-7: a network error carries bot[redacted], never the token,
// in the result and in the log.
func TestRefusedConnectionIsRedacted(t *testing.T) {
	var log bytes.Buffer
	c, err := NewClient(network(t, &log), Settings{BaseURL: "http://127.0.0.1:1/secret-prefix", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	_, r := c.GetMe(t.Context(), outbound.ClassInteractive)
	if r.Outcome.Kind != delivery.OutcomeTransient || r.Network != outbound.NetworkConnect {
		t.Fatalf("result %+v", r)
	}
	text := string(r.Outcome.Error) + r.Detail + log.String()
	if strings.Contains(text, "s3cr3t") || strings.Contains(text, "secret-prefix") ||
		!strings.Contains(string(r.Outcome.Error), "bot[redacted]/getMe") {
		t.Fatalf("not redacted: %q", text)
	}
}

// scripted serves one answer per request in order and records the requests.
type scripted struct {
	mu       sync.Mutex
	answers  []answer
	requests []*http.Request
	bodies   []string
}

type answer struct {
	status      int
	contentType string
	body        string
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r)
	s.bodies = append(s.bodies, string(b))
	a := answer{status: http.StatusOK, contentType: "application/json", body: `{"ok":true,"result":true}`}
	if len(s.answers) > 0 {
		a, s.answers = s.answers[0], s.answers[1:]
	}
	w.Header().Set("Content-Type", a.contentType)
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, a.body)
}

func serve(t *testing.T, answers ...answer) (*scripted, string) {
	t.Helper()
	s := &scripted{answers: answers}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func jsonAnswer(status int, body string) answer {
	return answer{status: status, contentType: "application/json", body: body}
}

// TestClassification covers C-14.FR-7 and the client contract: ok: false is classified by error_code like a status,
// retry_after is the exact delay, an answer that is not JSON is Transient, 409 is unknown and a conflict.
func TestClassification(t *testing.T) {
	cases := []struct {
		name     string
		answer   answer
		kind     delivery.OutcomeKind
		check    func(Result) bool
		describe string
	}{
		{"429 in a 200 body", jsonAnswer(200,
			`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`),
			delivery.OutcomeRetryAfter, func(r Result) bool {
				return r.Outcome.RetryAfter == 7*time.Second && r.Outcome.Scope == delivery.ScopeConnection && r.Code == 429
			}, "Telegram answered 429: Too Many Requests: retry after 7"},
		{"429 without a delay", jsonAnswer(429, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`),
			delivery.OutcomeRetryAfter, func(r Result) bool { return r.Outcome.RetryAfter == defaultRetryAfter }, ""},
		{"401", jsonAnswer(401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`), delivery.OutcomeFatal,
			func(r Result) bool { return r.Status == 401 && !r.Conflict() }, "Telegram answered 401: Unauthorized"},
		{"409", jsonAnswer(409, `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request"}`),
			delivery.OutcomeUnknown, func(r Result) bool { return r.Conflict() }, ""},
		{"400", jsonAnswer(400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`),
			delivery.OutcomeUnknown, nil, ""},
		{"502 HTML", answer{502, "text/html", "<html>bad gateway</html>"}, delivery.OutcomeTransient,
			func(r Result) bool { return r.NotJSON }, ""},
		{"200 HTML", answer{200, "text/html", "<html>it works</html>"}, delivery.OutcomeTransient,
			func(r Result) bool { return r.NotJSON }, ""},
		{"ok false without a code", jsonAnswer(200, `{"ok":false,"description":"odd"}`), delivery.OutcomeTransient, nil, ""},
		{"500 without a code", jsonAnswer(500, `{"ok":false,"description":"Internal"}`), delivery.OutcomeTransient, nil, ""},
		{"result not a user", jsonAnswer(200, `{"ok":true,"result":[1,2]}`), delivery.OutcomeUnknown, nil, ""},
		{"no result", jsonAnswer(200, `{"ok":true}`), delivery.OutcomeUnknown, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, base := serve(t, tc.answer)
			c := newClient(t, Settings{BaseURL: base, Token: testToken})
			_, r := c.GetMe(t.Context(), outbound.ClassInteractive)
			if r.Outcome.Kind != tc.kind || (tc.check != nil && !tc.check(r)) {
				t.Fatalf("result %+v", r)
			}
			if tc.describe != "" && string(r.Outcome.Error) != tc.describe {
				t.Fatalf("error %q", r.Outcome.Error)
			}
		})
	}
}

func TestBlockedAndRedirectAreFatal(t *testing.T) {
	_, base := serve(t, answer{http.StatusFound, "text/plain", ""})
	c := newClient(t, Settings{BaseURL: base, Token: testToken})
	if _, r := c.GetMe(t.Context(), outbound.ClassInteractive); r.Outcome.Kind != delivery.OutcomeFatal {
		t.Fatalf("redirect = %+v", r)
	}
	p, _ := outbound.ParsePolicy("standard", nil, nil)
	n := network(t, nil)
	n.Policy = outbound.StaticPolicy(p)
	c, err := NewClient(n, Settings{BaseURL: base, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	if _, r := c.GetMe(t.Context(), outbound.ClassInteractive); r.Outcome.Kind != delivery.OutcomeFatal ||
		strings.Contains(string(r.Outcome.Error), "s3cr3t") {
		t.Fatalf("blocked = %+v", r)
	}
}

func TestGetUpdatesSetAndDeleteWebhook(t *testing.T) {
	s, base := serve(t,
		jsonAnswer(200, `{"ok":true,"result":[{"update_id":5,"message":{"message_id":1,"chat":{"id":1,"type":"private"},"text":"/start x"}},{"update_id":6,"callback_query":{"id":"q","from":{"id":2}}}]}`),
		jsonAnswer(200, `{"ok":true,"result":[{"no_id":1},{"update_id":7,"message":"odd"},{"update_id":8,"poll":{}}]}`),
		jsonAnswer(200, `{"ok":true,"result":{"not":"a list"}}`),
		jsonAnswer(200, `{"ok":true,"result":true}`),
		jsonAnswer(400, `{"ok":false,"error_code":400,"description":"Bad Request: bad webhook: secret token s3cr3t-hook is bad"}`),
		jsonAnswer(200, `{"ok":true,"result":true}`))
	c := newClient(t, Settings{BaseURL: base, Token: testToken})
	offset := int64(5)
	ups, r := c.GetUpdates(t.Context(), &offset, 25*time.Second)
	if !r.OK() || len(ups) != 2 || ups[0].Kind() != KindPrivateMessage || ups[1].Kind() != KindCallbackQuery ||
		!strings.Contains(string(ups[0].Raw), "/start x") {
		t.Fatalf("getUpdates = %+v %+v", ups, r)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(s.bodies[0]), &body)
	if body["offset"] != float64(5) || body["timeout"] != float64(25) ||
		fmt.Sprint(body["allowed_updates"]) != "[message edited_message channel_post callback_query my_chat_member]" {
		t.Fatalf("getUpdates body %v", body)
	}
	if ups, r := c.GetUpdates(t.Context(), nil, 0); !r.OK() || len(ups) != 2 || ups[0].UpdateID != 7 ||
		ups[0].Kind() != KindOther || string(ups[0].Raw) != `{"update_id":7,"message":"odd"}` || ups[1].UpdateID != 8 {
		t.Fatalf("odd updates = %+v %+v", ups, r)
	}
	if _, r := c.GetUpdates(t.Context(), nil, 0); r.Outcome.Kind != delivery.OutcomeUnknown {
		t.Fatalf("a result that is not a list = %+v", r)
	}
	if r := c.SetWebhook(t.Context(), outbound.ClassInteractive, "https://in.example.org/hook", "s3cr3t-hook"); !r.OK() {
		t.Fatalf("setWebhook = %+v", r)
	}
	_ = json.Unmarshal([]byte(s.bodies[3]), &body)
	if body["url"] != "https://in.example.org/hook" || body["secret_token"] != "s3cr3t-hook" ||
		body["max_connections"] != float64(1) {
		t.Fatalf("setWebhook body %v", body)
	}
	r = c.SetWebhook(t.Context(), outbound.ClassInteractive, "https://in.example.org/hook", "s3cr3t-hook")
	if r.OK() || strings.Contains(string(r.Outcome.Error)+r.Description, "s3cr3t-hook") {
		t.Fatalf("refused setWebhook = %+v", r)
	}
	if r := c.DeleteWebhook(t.Context(), outbound.ClassInteractive); !r.OK() {
		t.Fatalf("deleteWebhook = %+v", r)
	}
	if !strings.HasSuffix(s.requests[5].URL.Path, "/deleteWebhook") || s.requests[5].Method != http.MethodPost {
		t.Fatalf("deleteWebhook request %s %s", s.requests[5].Method, s.requests[5].URL.Path)
	}
}

func TestClientWithoutTokenOrClass(t *testing.T) {
	s, base := serve(t, jsonAnswer(401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	probe := newClient(t, Settings{BaseURL: base})
	if _, r := probe.GetMe(t.Context(), outbound.ClassInteractive); r.Outcome.Kind != delivery.OutcomeFatal ||
		len(s.requests) != 0 {
		t.Fatalf("a call with the token on the probe client = %+v, %d requests", r, len(s.requests))
	}
	if r := probe.DryProbe(t.Context(), outbound.ClassInteractive); r.Status != 401 || r.Code != 401 || r.NotJSON {
		t.Fatalf("dry probe = %+v", r)
	}
	if s.requests[0].URL.Path != "/bot0:x/getMe" || s.requests[0].Header.Get("Authorization") != "" {
		t.Fatalf("dry probe request %s", s.requests[0].URL.Path)
	}
	c := newClient(t, Settings{BaseURL: base, Token: testToken})
	if _, r := c.GetMe(t.Context(), outbound.ClassHeartbeat); r.Outcome.Kind != delivery.OutcomeFatal {
		t.Fatalf("heartbeat class = %+v", r)
	}
	if r := c.DryProbe(t.Context(), outbound.ClassHeartbeat); r.Outcome.Kind != delivery.OutcomeFatal {
		t.Fatalf("dry probe in the heartbeat class = %+v", r)
	}
	if _, err := NewClient(network(t, nil), Settings{BaseURL: base, Token: testToken,
		Proxy: &outbound.Proxy{Type: "ftp", Address: "proxy:1", Password: otherToken}}); err == nil ||
		strings.Contains(err.Error(), otherToken) {
		t.Fatalf("bad proxy = %v", err)
	}
}

func TestParseUpdateAndKinds(t *testing.T) {
	for raw, want := range map[string]string{
		`{"update_id":1,"callback_query":{"id":"q","from":{"id":1}}}`:                         KindCallbackQuery,
		`{"update_id":1,"channel_post":{"message_id":1,"chat":{"id":-1,"type":"channel"}}}`:   KindChatMessage,
		`{"update_id":1,"message":{"message_id":1,"chat":{"id":-2,"type":"supergroup"}}}`:     KindChatMessage,
		`{"update_id":1,"edited_message":{"message_id":1,"chat":{"id":-3,"type":"group"}}}`:   KindChatMessage,
		`{"update_id":1,"edited_message":{"message_id":1,"chat":{"id":3,"type":"private"}}}`:  KindPrivateMessage,
		`{"update_id":1,"message":{"message_id":1,"chat":{"id":3,"type":"something_new"}}}`:   KindOther,
		`{"update_id":1,"my_chat_member":{"chat":{"id":1},"new_chat_member":{"status":"x"}}}`: KindMyChatMember,
		`{"update_id":1,"poll":{"id":"p"}}`:                                                   KindOther,
	} {
		u, err := ParseUpdate([]byte(raw))
		if err != nil || u.Kind() != want || string(u.Raw) != raw {
			t.Errorf("%s: kind %q, %v", raw, u.Kind(), err)
		}
	}
	for _, raw := range []string{`{}`, `[`, `{"update_id":"x"}`, `{"update_id":1,"message":"x"}`} {
		if _, err := ParseUpdate([]byte(raw)); err == nil {
			t.Errorf("%s parsed", raw)
		}
	}
}

// TestDryProbeRetryAfterIsTransient: a 429 to the dry probe's token says nothing of the bot's budget and never holds a
// limiter.
func TestDryProbeRetryAfterIsTransient(t *testing.T) {
	_, base := serve(t, jsonAnswer(429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":30}}`))
	r := newClient(t, Settings{BaseURL: base}).DryProbe(t.Context(), outbound.ClassInteractive)
	if r.Outcome.Kind != delivery.OutcomeTransient || r.Outcome.Scope != "" || r.Outcome.RetryAfter != 0 || r.Status != 429 {
		t.Fatalf("dry probe 429 = %+v", r)
	}
}
