// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

var direct = Direct(delivery.Call{Class: outbound.ClassInteractive})

// ticking is a real clock that moves a millisecond each time it is read, so that every request takes one.
type ticking struct {
	mu sync.Mutex
	t  time.Time
}

func (c *ticking) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond)
	return c.t
}

func tick() *ticking { return &ticking{t: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)} }

func startFake(t *testing.T) *fakeslack.Fake {
	t.Helper()
	f := fakeslack.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(t.Context()) })
	return f
}

// fakeClient is the client of the fake's app a, or of b when second, routed to the fake as `muster dev` routes it.
func fakeClient(t *testing.T, f *fakeslack.Fake, second bool) *Client {
	t.Helper()
	bot, app, signing := fakeslack.BotToken, fakeslack.AppToken, fakeslack.SigningSecret
	if second {
		bot, app, signing = fakeslack.SecondBotToken, fakeslack.SecondAppToken, fakeslack.SecondSigningSecret
	}
	return newClient(t, network(t, nil), Settings{BotToken: logging.Secret(bot), AppToken: logging.Secret(app),
		SigningSecret: logging.Secret(signing), Route: devRoute(f)})
}

// devRoute is the Route of `muster dev`: the fake for its published bot tokens, Slack for any other.
func devRoute(f *fakeslack.Fake) Route {
	return func(token logging.Secret) string {
		if fakeslack.PublishedBotToken(string(token)) {
			return f.URL() + "/api/"
		}
		return ""
	}
}

func names(c Check) string {
	var b strings.Builder
	for _, s := range c.Steps {
		state := "fail"
		switch {
		case s.Skipped:
			state = "skip"
		case s.OK:
			state = "ok"
		}
		b.WriteString(s.Name + ":" + state + " ")
	}
	return strings.TrimSpace(b.String())
}

// failed is the step that failed as "name code: message".
func failed(c Check) string {
	s, ok := c.Failed()
	if !ok {
		return ""
	}
	return s.Name + " " + s.Code + ": " + s.Message
}

// TestCheckSocket covers C-24.FR-2 and AC-1: in socket mode the steps token, scopes and app_token pass against the
// fake; token finds the bot and the workspace, scopes makes no request and has no latency, app_token calls
// apps.connections.open with the app-level token first, then finds the app, and opens no socket; each step names its
// path.
func TestCheckSocket(t *testing.T) {
	f := startFake(t)
	b := fakeslack.Apps()[1]
	res, err := RunCheck(t.Context(), fakeClient(t, f, true), direct, tick(), EventModeSocket)
	if err != nil || !res.OK() || names(res) != "token:ok scopes:ok app_token:ok" {
		t.Fatalf("check = %+v, %v", res, err)
	}
	want := Identity{TeamID: b.TeamID, TeamName: "Example workspace B", TeamURL: b.TeamURL, BotID: b.BotID,
		BotUserID: b.BotUserID, BotName: b.BotName, AppID: b.AppID}
	if res.Identity == nil || *res.Identity != want {
		t.Fatalf("identity %+v", res.Identity)
	}
	if lat := []time.Duration{res.Steps[0].Latency, res.Steps[1].Latency, res.Steps[2].Latency}; lat[0] !=
		time.Millisecond || lat[1] != 0 || lat[2] != 3*time.Millisecond {
		t.Fatalf("latencies %v", lat)
	}
	for _, s := range res.Steps {
		if s.Via != ViaDirect || s.Code != "" || s.Message != "" {
			t.Errorf("step %+v", s)
		}
	}
	var paths []string
	for _, r := range f.Requests() {
		paths = append(paths, r.Path+" "+r.Headers["Authorization"][0])
	}
	if fmt.Sprint(paths) != "[/api/auth.test Bearer "+fakeslack.SecondBotToken+" /api/apps.connections.open Bearer "+
		fakeslack.SecondAppToken+" /api/auth.test Bearer "+fakeslack.SecondAppToken+" /api/bots.info Bearer "+
		fakeslack.SecondBotToken+"]" {
		t.Fatalf("requests %v", paths)
	}
	if st := f.Sockets(); st.Apps[fakeslack.AppB].Opened != 0 {
		t.Fatalf("the check opened a socket: %+v", st)
	}
	if (Check{}).OK() {
		t.Fatal("a check without steps passed")
	}
}

// TestCheckHTTPHasNoAppToken covers C-24.FR-2 and AC-1: in http mode there is no step app_token, so no app is found.
func TestCheckHTTPHasNoAppToken(t *testing.T) {
	f := startFake(t)
	res, err := RunCheck(t.Context(), fakeClient(t, f, false), direct, tick(), EventModeHTTP)
	if err != nil || !res.OK() || names(res) != "token:ok scopes:ok" || res.Identity.AppID != "" ||
		res.Identity.TeamName != "Example workspace" || res.Identity.BotName != "muster-dev-bot" || len(f.Requests()) != 1 {
		t.Fatalf("check = %+v %+v, %v", res, res.Identity, err)
	}
}

// TestCheckRevokedToken covers C-24.AC-1: a revoked bot token fails token with token_invalid and "Slack refused the
// token.", and the steps after it are skipped.
func TestCheckRevokedToken(t *testing.T) {
	f := startFake(t)
	if err := f.SetConfig(fakeslack.Config{Revoked: []string{fakeslack.SecondBotToken}}); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, f, true)
	res, err := RunCheck(t.Context(), c, direct, tick(), EventModeSocket)
	if err != nil || res.OK() || names(res) != "token:fail scopes:skip app_token:skip" || res.Identity != nil ||
		failed(res) != "token token_invalid: Slack refused the token." {
		t.Fatalf("check = %+v, %v", res, err)
	}
	res, _ = RunCheck(t.Context(), c, direct, tick(), EventModeHTTP)
	if names(res) != "token:fail scopes:skip" {
		t.Fatalf("http mode = %s", names(res))
	}
	for _, e := range []string{"invalid_auth", "not_authed", "account_inactive", "token_expired"} {
		if err := f.SetFault(fakeserver.Fault{Path: "/api/auth.test", Status: http.StatusOK,
			Body: `{"ok":false,"error":"` + e + `"}`, Times: 1}); err != nil {
			t.Fatal(err)
		}
		if res, _ := RunCheck(t.Context(), c, direct, tick(), EventModeHTTP); res.Steps[0].Code != CodeTokenInvalid {
			t.Errorf("%s: %+v", e, res.Steps[0])
		}
	}
}

// TestCheckUserToken covers C-24.FR-2 and AC-1: a token whose auth.test names no bot_id, and one Slack answers
// not_allowed_token_type, fail token with wrong_token_type and "This is not a bot token.".
func TestCheckUserToken(t *testing.T) {
	f := startFake(t)
	if err := f.SetConfig(fakeslack.Config{UserTokens: []string{fakeslack.SecondBotToken}}); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, f, true)
	res, err := RunCheck(t.Context(), c, direct, tick(), EventModeSocket)
	if err != nil || res.OK() || names(res) != "token:fail scopes:skip app_token:skip" ||
		failed(res) != "token wrong_token_type: This is not a bot token." {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if err := f.SetConfig(fakeslack.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := f.SetFault(fakeserver.Fault{Path: "/api/auth.test", Status: http.StatusOK,
		Body: `{"ok":false,"error":"not_allowed_token_type"}`, Times: 1}); err != nil {
		t.Fatal(err)
	}
	res, _ = RunCheck(t.Context(), c, direct, tick(), EventModeSocket)
	if failed(res) != "token wrong_token_type: This is not a bot token." {
		t.Fatalf("not_allowed_token_type = %+v", res)
	}
}

// TestCheckMissingScope covers C-24.FR-2 and AC-1: scopes fails with missing_scope naming the scopes the bot token
// lacks in the order of C-24.FR-1, and app_token is skipped; D396: the step carries those scopes as MissingScopes, which
// its English message names, and a step that passed or failed otherwise carries none.
func TestCheckMissingScope(t *testing.T) {
	f := startFake(t)
	c := fakeClient(t, f, true)
	for _, tc := range []struct {
		scopes, want string
		missing      []string
	}{
		{"chat:write,channels:read,groups:read,im:write,users:read",
			"scopes missing_scope: The bot token lacks the scopes users:read.email.", []string{"users:read.email"}},
		{"users:read,im:write,groups:read,channels:read",
			"scopes missing_scope: The bot token lacks the scopes chat:write, users:read.email.",
			[]string{"chat:write", "users:read.email"}},
		{"", "scopes missing_scope: The bot token lacks the scopes chat:write, channels:read, groups:read, im:write, " +
			"users:read, users:read.email.", RequiredScopes},
	} {
		if err := f.SetConfig(fakeslack.Config{Scopes: map[string]string{fakeslack.AppB: tc.scopes}}); err != nil {
			t.Fatal(err)
		}
		res, err := RunCheck(t.Context(), c, direct, tick(), EventModeSocket)
		if err != nil || res.OK() || names(res) != "token:ok scopes:fail app_token:skip" || failed(res) != tc.want ||
			res.Identity == nil || res.Steps[1].Latency != 0 {
			t.Fatalf("%q: check = %+v, %v", tc.scopes, res, err)
		}
		st := res.Steps[1]
		if !slices.Equal(st.MissingScopes, tc.missing) || st.Message != MissingScopesMessage(st.MissingScopes) ||
			res.Steps[0].MissingScopes != nil || res.Steps[2].MissingScopes != nil {
			t.Fatalf("%q: missing scopes %q, steps %+v", tc.scopes, st.MissingScopes, res.Steps)
		}
	}
	if err := f.SetConfig(fakeslack.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	res, err := RunCheck(t.Context(), c, direct, tick(), EventModeSocket)
	if err != nil || !res.OK() || slices.ContainsFunc(res.Steps, func(s Step) bool { return s.MissingScopes != nil }) {
		t.Fatalf("a passing check carries missing scopes: %+v, %v", res, err)
	}
}

// TestCheckAppMismatch covers C-24.FR-2 and AC-1: an app-level token of another app than the bot's fails app_token
// with app_mismatch, and no app is found.
func TestCheckAppMismatch(t *testing.T) {
	f := startFake(t)
	if err := f.SetConfig(fakeslack.Config{AppOf: map[string]string{fakeslack.SecondAppToken: fakeslack.AppA}}); err != nil {
		t.Fatal(err)
	}
	res, err := RunCheck(t.Context(), fakeClient(t, f, true), direct, tick(), EventModeSocket)
	if err != nil || res.OK() || names(res) != "token:ok scopes:ok app_token:fail" || res.Identity.AppID != "" ||
		failed(res) != "app_token app_mismatch: The app-level token belongs to another Slack app than the bot token." {
		t.Fatalf("check = %+v, %v", res, err)
	}
}

// recording is a Runner that calls at once and keeps the outcome of each request it ran.
type recording struct {
	mu       sync.Mutex
	outcomes []delivery.Outcome
}

func (r *recording) run(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (
	delivery.Outcome, error) {
	o := f(ctx, delivery.Call{Class: outbound.ClassInteractive})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, o)
	return o, nil
}

// TestCheckFailures covers C-24.FR-2: a step without a code of its own has the code of delivery.FailureCode, or
// request_failed with Slack's masked error; a 429 reaches the runner as a RetryAfter for the Connection.
func TestCheckFailures(t *testing.T) {
	f := startFake(t)
	c := fakeClient(t, f, false)
	fault := func(path string, status, retryAfter int, body string) {
		t.Helper()
		if err := f.SetFault(fakeserver.Fault{Path: path, Status: status, RetryAfterSeconds: retryAfter, Body: body,
			Times: 1}); err != nil {
			t.Fatal(err)
		}
	}
	fault("/api/auth.test", http.StatusInternalServerError, 0, "<html>down</html>")
	res, _ := RunCheck(t.Context(), c, direct, tick(), EventModeSocket)
	if s := res.Steps[0]; s.Code != delivery.CodeRequestFailed || !strings.Contains(s.Message, "answered 500") ||
		names(res) != "token:fail scopes:skip app_token:skip" {
		t.Fatalf("500 = %+v", res)
	}
	fault("/api/auth.test", http.StatusTooManyRequests, 30, `{"ok":false,"error":"ratelimited"}`)
	rec := &recording{}
	res, _ = RunCheck(t.Context(), c, rec.run, tick(), EventModeSocket)
	if failed(res) != "token request_failed: ratelimited" || len(rec.outcomes) != 1 ||
		rec.outcomes[0].Kind != delivery.OutcomeRetryAfter || rec.outcomes[0].Scope != delivery.ScopeConnection ||
		rec.outcomes[0].RetryAfter != 30*time.Second {
		t.Fatalf("429 = %+v, outcomes %+v", res, rec.outcomes)
	}
	if err := f.SetConfig(fakeslack.Config{Revoked: []string{fakeslack.AppToken}}); err != nil {
		t.Fatal(err)
	}
	res, _ = RunCheck(t.Context(), c, direct, tick(), EventModeSocket)
	if failed(res) != "app_token token_invalid: Slack refused the token." || res.Identity.AppID != "" {
		t.Fatalf("revoked app-level token = %+v", res)
	}
	if err := f.SetConfig(fakeslack.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]string{
		`{"ok":false,"error":"bot_not_found"}`:    "app_token request_failed: bot_not_found",
		`{"ok":false,"error":"invalid_auth"}`:     "app_token token_invalid: Slack refused the token.",
		`{"ok":true,"bot":{"id":"B0MUSTERDEVA"}}`: "app_token request_failed: Slack's answer names no app.",
	} {
		fault("/api/bots.info", http.StatusOK, 0, body)
		if res, _ := RunCheck(t.Context(), c, direct, tick(), EventModeSocket); failed(res) != want ||
			res.Steps[2].Latency != 3*time.Millisecond {
			t.Errorf("bots.info %s = %q %+v", body, failed(res), res.Steps)
		}
	}
	if err := f.SetFault(fakeserver.Fault{Path: "/api/bots.info", Status: http.StatusBadGateway, Times: 1}); err != nil {
		t.Fatal(err)
	}
	if res, _ := RunCheck(t.Context(), c, direct, tick(), EventModeSocket); res.Steps[2].Code !=
		delivery.CodeRequestFailed || !strings.Contains(res.Steps[2].Message, "answered 502") {
		t.Fatalf("bots.info 502 = %+v", res.Steps)
	}
}

// TestCheckAppTokenAnswers: an app-level token whose auth.test fails, or names no app, fails app_token before bots.info;
// the address apps.connections.open answers is not read, whatever it is.
func TestCheckAppTokenAnswers(t *testing.T) {
	for _, tc := range []struct{ answer, want string }{
		{`{"ok":true}`, "app_token request_failed: Slack's answer names no app."},
		{`{"ok":false,"error":"internal_error"}`, "app_token request_failed: internal_error"},
		{`{"ok":false,"error":"token_expired"}`, "app_token token_invalid: Slack refused the token."},
	} {
		s := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/apps.connections.open" {
				answerJSON(http.StatusOK, `{"ok":true,"url":"wss://evil.example/link/?ticket=t"}`)(w, r)
				return
			}
			if r.Header.Get("Authorization") == "Bearer "+testApp {
				answerJSON(http.StatusOK, tc.answer)(w, r)
				return
			}
			answerJSON(http.StatusOK, `{"ok":true,"bot_id":"B1","team":"W","team_id":"T1"}`,
				"X-OAuth-Scopes", strings.Join(RequiredScopes, ","))(w, r)
		})
		res, err := RunCheck(t.Context(), newClient(t, network(t, nil), settings(s.base())), direct, tick(),
			EventModeSocket)
		if err != nil || failed(res) != tc.want || len(s.seen()) != 3 {
			t.Errorf("%s: %q, %d requests, %v", tc.answer, failed(res), len(s.seen()), err)
		}
	}
}

// TestCheckWithoutAnswer covers C-24.FR-2: a request without an answer has the code of what failed and the text of
// delivery.FailureMessage — the name of Slack's address, the time — or the error's own; a blocked address is blocked.
func TestCheckWithoutAnswer(t *testing.T) {
	n := network(t, nil)
	n.Resolver = &resolver{}
	res, err := RunCheck(t.Context(), newClient(t, n, Settings{BotToken: testBot, AppToken: testApp}), direct, tick(),
		EventModeSocket)
	if err != nil || failed(res) != "token dns_failed: DNS lookup failed: slack.com" ||
		names(res) != "token:fail scopes:skip app_token:skip" {
		t.Fatalf("dns = %+v, %v", res, err)
	}
	f := startFake(t)
	expired, cancel := context.WithTimeout(t.Context(), 0)
	defer cancel()
	if res, _ := RunCheck(expired, fakeClient(t, f, false), direct, tick(), EventModeHTTP); failed(res) !=
		"token timeout: No answer within 10 s" {
		t.Fatalf("timeout = %+v", res)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String() + "/api/"
	_ = ln.Close()
	res, _ = RunCheck(t.Context(), newClient(t, network(t, nil), settings(closed)), direct, tick(), EventModeHTTP)
	if s := res.Steps[0]; s.Code != delivery.CodeConnectFailed || s.Message == "" ||
		strings.Contains(s.Message, testBot) {
		t.Fatalf("connect = %+v", res)
	}
	strict, err := outbound.ParsePolicy("standard", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked := network(t, nil)
	blocked.Policy = outbound.StaticPolicy(strict)
	res, _ = RunCheck(t.Context(), newClient(t, blocked, settings(f.URL()+"/api/")), direct, tick(), EventModeHTTP)
	if s := res.Steps[0]; s.Code != delivery.CodeBlocked || !strings.Contains(s.Message, "blocked") {
		t.Fatalf("blocked = %+v", res)
	}
}

// TestCheckRunnerError: the runner's own error, such as no limiter token within the budget, ends the check, at any
// request of it.
func TestCheckRunnerError(t *testing.T) {
	f := startFake(t)
	c := fakeClient(t, f, false)
	limited := &delivery.LimitedError{RetryAfter: time.Second}
	for n := 1; n <= 3; n++ {
		calls := 0
		run := func(ctx context.Context, fn func(ctx context.Context, c delivery.Call) delivery.Outcome) (
			delivery.Outcome, error) {
			calls++
			if calls == n {
				return delivery.Outcome{}, limited
			}
			return direct(ctx, fn)
		}
		res, err := RunCheck(t.Context(), c, run, tick(), EventModeSocket)
		if !errors.Is(err, limited) || len(res.Steps) != 0 {
			t.Fatalf("request %d: %+v, %v", n, res, err)
		}
	}
}

// TestCheckThroughProxy covers C-24.AC-3: with an HTTP proxy every request of the check reaches the fake only through
// it and each step names the path; a proxy that refuses the credentials fails token with proxy_refused.
func TestCheckThroughProxy(t *testing.T) {
	f := startFake(t)
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0",
		fakeproxy.Options{Username: "u", Password: "proxy-pw-right"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	for _, tc := range []struct{ password, want string }{
		{"proxy-pw-right", ""},
		{"proxy-pw-wrong", "token proxy_refused: " + MessageProxyRefused},
	} {
		f.ResetRequests()
		s := Settings{BotToken: fakeslack.BotToken, AppToken: fakeslack.AppToken, Route: devRoute(f),
			Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP, Address: proxy.Addr(), Username: "u",
				Password: logging.Secret(tc.password)}}
		res, err := RunCheck(t.Context(), newClient(t, network(t, nil), s), direct, tick(), EventModeSocket)
		if err != nil || failed(res) != tc.want {
			t.Fatalf("%s: %+v, %v", tc.password, res, err)
		}
		for _, st := range res.Steps {
			if st.Via != ViaProxy {
				t.Errorf("step %+v", st)
			}
		}
		if tc.want == "" && (!res.OK() || len(f.Requests()) != 4 || len(proxy.Connections()) == 0) {
			t.Fatalf("through the proxy: %+v, %d requests, %+v", res, len(f.Requests()), proxy.Connections())
		}
		if tc.want != "" && len(f.Requests()) != 0 {
			t.Fatalf("a refused proxy passed %d requests", len(f.Requests()))
		}
	}
}

// TestCheckAppTokenOpen covers C-24.FR-2: apps.connections.open is the first call of app_token, so that a bot token
// typed as the app-level token fails it with wrong_token_type; a refused token is token_invalid and any other failure
// request_failed; no request ever goes to a socket address, and http mode calls no apps.connections.open.
func TestCheckAppTokenOpen(t *testing.T) {
	f := startFake(t)
	bot := Settings{BotToken: fakeslack.SecondBotToken, AppToken: fakeslack.SecondBotToken, Route: devRoute(f)}
	res, err := RunCheck(t.Context(), newClient(t, network(t, nil), bot), direct, tick(), EventModeSocket)
	if err != nil || failed(res) != "app_token wrong_token_type: This is not an app-level token." ||
		res.Identity.AppID != "" || res.Steps[2].Latency != time.Millisecond {
		t.Fatalf("a bot token as the app-level token = %+v, %v", res, err)
	}
	c := fakeClient(t, f, true)
	for body, want := range map[string]string{
		`{"ok":false,"error":"not_allowed_token_type"}`: "app_token wrong_token_type: " + MessageNotAppToken,
		`{"ok":false,"error":"invalid_auth"}`:           "app_token token_invalid: Slack refused the token.",
		`{"ok":false,"error":"internal_error"}`:         "app_token request_failed: internal_error",
	} {
		if err := f.SetFault(fakeserver.Fault{Path: "/api/apps.connections.open", Status: http.StatusOK, Body: body,
			Times: 1}); err != nil {
			t.Fatal(err)
		}
		if res, _ := RunCheck(t.Context(), c, direct, tick(), EventModeSocket); failed(res) != want ||
			names(res) != "token:ok scopes:ok app_token:fail" {
			t.Errorf("%s: %q", body, failed(res))
		}
	}
	for _, r := range f.Requests() {
		if r.Path == "/link/" {
			t.Errorf("the check requested %s", r.Path)
		}
	}
	f.ResetRequests()
	if res, _ := RunCheck(t.Context(), c, direct, tick(), EventModeHTTP); !res.OK() {
		t.Fatalf("http = %+v", res)
	}
	for _, r := range f.Requests() {
		if r.Path == "/api/apps.connections.open" {
			t.Errorf("http mode called %s", r.Path)
		}
	}
	if st := f.Sockets(); st.Apps[fakeslack.AppB].Opened != 0 || st.Apps[fakeslack.AppA].Opened != 0 {
		t.Fatalf("a check opened a socket: %+v", st)
	}
}

// probeFault scripts the next answer of the fake to auth.test.
func probeFault(t *testing.T, f *fakeslack.Fake, fault fakeserver.Fault) {
	t.Helper()
	fault.Path, fault.Times = "/api/auth.test", 1
	if err := f.SetFault(fault); err != nil {
		t.Fatal(err)
	}
}

// TestDryProbe covers C-24.FR-2 and C-13.FR-14: the dry probe is `POST <base>auth.test` without Authorization, also
// on a client that holds both tokens, recorded as auth.test, and passes on Slack's 200 answer ok:false with not_authed;
// any other answer is not_slack — not JSON, with its status and media type, or other JSON with 200 — and JSON with
// another status unexpected_status; a 429 reaches the runner as Transient, never holding the Connection's limiter.
func TestDryProbe(t *testing.T) {
	f := startFake(t)
	rec := &records{}
	n := network(t, nil)
	n.Recorder = rec
	for _, s := range []Settings{{Owner: outbound.Owner{Draft: true}}, {BotToken: fakeslack.BotToken,
		AppToken: fakeslack.AppToken, Route: devRoute(f), Owner: outbound.Owner{Draft: true}}} {
		if s.BotToken == "" {
			s.Route = to(f.URL() + "/api/")
		}
		f.ResetRequests()
		c := newClient(t, n, s)
		p, err := RunDryProbe(t.Context(), c, direct, tick())
		if err != nil || p != (Probe{OK: true, Latency: time.Millisecond}) {
			t.Fatalf("dry probe = %+v, %v", p, err)
		}
		reqs := f.Requests()
		if len(reqs) != 1 || reqs[0].Method != http.MethodPost || reqs[0].Path != "/api/auth.test" ||
			len(reqs[0].Headers["Authorization"]) != 0 || reqs[0].Body != "" {
			t.Fatalf("requests %+v", reqs)
		}
	}
	if got := rec.got(); len(got) != 2 || got[0].Operation != "auth.test" {
		t.Fatalf("records %+v", got)
	}
	c := newClient(t, network(t, nil), Settings{Route: to(f.URL() + "/api/")})
	for _, tc := range []struct {
		fault         fakeserver.Fault
		code, message string
	}{
		{fakeserver.Fault{Status: http.StatusOK, ContentType: "text/html; charset=utf-8", Body: "<html></html>"},
			CodeNotSlack, "The address answered 200 with text/html, not as Slack's Web API."},
		{fakeserver.Fault{Status: http.StatusOK, ContentType: "", Body: "plain"}, CodeNotSlack,
			"The address answered 200 with text/plain, not as Slack's Web API."},
		{fakeserver.Fault{Status: http.StatusOK, Body: `[{"ok":false,"error":"not_authed"}]`}, CodeNotSlack,
			"The address answered 200 with application/json, not as Slack's Web API."},
		{fakeserver.Fault{Status: http.StatusOK, Body: `{"ok":true}`}, CodeNotSlack, messageNotNotAuthed},
		{fakeserver.Fault{Status: http.StatusOK, Body: `{"status":"OK"}`}, CodeNotSlack, messageNotNotAuthed},
		{fakeserver.Fault{Status: http.StatusOK, Body: `{"ok":false,"error":"invalid_auth"}`}, CodeNotSlack,
			messageNotNotAuthed},
		{fakeserver.Fault{Status: http.StatusInternalServerError, ContentType: "text/html", Body: "<html>down</html>"},
			CodeNotSlack, "The address answered 500 with text/html, not as Slack's Web API."},
		{fakeserver.Fault{Status: http.StatusNotFound, ContentType: "x/y; bad=", Body: "nope"}, CodeNotSlack,
			"The address answered 404 with no content type, not as Slack's Web API."},
		{fakeserver.Fault{Status: http.StatusServiceUnavailable, Body: `{"ok":false,"error":"not_authed"}`},
			CodeUnexpectedStatus, "Slack answered 503 instead of 200 with not_authed."},
		{fakeserver.Fault{Status: http.StatusUnauthorized, Body: `{"ok":false,"error":"not_authed"}`},
			CodeUnexpectedStatus, "Slack answered 401 instead of 200 with not_authed."},
	} {
		probeFault(t, f, tc.fault)
		p, err := RunDryProbe(t.Context(), c, direct, tick())
		if err != nil || p.OK || p.Code != tc.code || p.Message != tc.message || p.Latency != time.Millisecond {
			t.Errorf("%d %s = %+v, %v", tc.fault.Status, tc.fault.Body, p, err)
		}
	}
	probeFault(t, f, fakeserver.Fault{Status: http.StatusTooManyRequests, RetryAfterSeconds: 30,
		Body: `{"ok":false,"error":"ratelimited"}`})
	r := &recording{}
	p, err := RunDryProbe(t.Context(), c, r.run, tick())
	if err != nil || p.Code != CodeUnexpectedStatus || len(r.outcomes) != 1 ||
		r.outcomes[0].Kind != delivery.OutcomeTransient || r.outcomes[0].RetryAfter != 0 || r.outcomes[0].Scope != "" {
		t.Fatalf("429 = %+v, %+v, %v", p, r.outcomes, err)
	}
	if p, err := RunDryProbe(t.Context(), c, Direct(delivery.Call{Class: "nope"}), tick()); err != nil ||
		p.Code != delivery.CodeRequestFailed || !strings.Contains(p.Message, "no nope class") {
		t.Fatalf("a class the client lacks = %+v, %v", p, err)
	}
	limited := &delivery.LimitedError{RetryAfter: time.Second}
	if _, err := RunDryProbe(t.Context(), c, func(context.Context, func(context.Context, delivery.Call) delivery.Outcome) (
		delivery.Outcome, error) {
		return delivery.Outcome{}, limited
	}, tick()); !errors.Is(err, limited) {
		t.Fatalf("runner error = %v", err)
	}
}

// TestDryProbeWithoutAnswer covers C-24.FR-2: a dry probe without an answer has the code of what failed and the text
// of delivery.FailureMessage — Slack's name directly, none through a proxy — or the error's own.
func TestDryProbeWithoutAnswer(t *testing.T) {
	n := network(t, nil)
	res := &resolver{}
	n.Resolver = res
	p, err := RunDryProbe(t.Context(), newClient(t, n, Settings{}), direct, tick())
	if err != nil || p.OK || p.Code != delivery.CodeDNSFailed || p.Message != "DNS lookup failed: slack.com" ||
		!slices.Equal(res.got(), []string{"slack.com"}) {
		t.Fatalf("dns = %+v, %v, %v", p, err, res.got())
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String() + "/api/"
	_ = ln.Close()
	if p, _ := RunDryProbe(t.Context(), newClient(t, network(t, nil), Settings{Route: to(closed)}), direct,
		tick()); p.Code != delivery.CodeConnectFailed || p.Message == "" {
		t.Fatalf("connect = %+v", p)
	}
	f := startFake(t)
	expired, cancel := context.WithTimeout(t.Context(), 0)
	defer cancel()
	if p, _ := RunDryProbe(expired, newClient(t, network(t, nil), Settings{Route: to(f.URL() + "/api/")}), direct,
		tick()); p.Code != delivery.CodeTimeout || p.Message != "No answer within 10 s" {
		t.Fatalf("timeout = %+v", p)
	}
	strict, err := outbound.ParsePolicy("standard", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked := network(t, nil)
	blocked.Policy = outbound.StaticPolicy(strict)
	if p, _ := RunDryProbe(t.Context(), newClient(t, blocked, Settings{Route: to(f.URL() + "/api/")}), direct,
		tick()); p.Code != delivery.CodeBlocked || !strings.Contains(p.Message, "blocked") {
		t.Fatalf("blocked = %+v", p)
	}
	viaProxy := network(t, nil)
	viaProxy.Resolver = &resolver{}
	if p, _ := RunDryProbe(t.Context(), newClient(t, viaProxy, Settings{Proxy: &outbound.Proxy{
		Type: outbound.ProxyHTTP, Address: "proxy.invalid:3128"}}), direct, tick()); p.Code != delivery.CodeDNSFailed ||
		!strings.Contains(p.Message, "lookup proxy.invalid") || strings.Contains(p.Message, "DNS lookup failed") {
		t.Fatalf("dns through a proxy = %+v", p)
	}
}

// TestDryProbeThroughProxy covers C-24.FR-2 and C-13.FR-14: the dry probe of a client with a proxy reaches the fake
// only through it, without Authorization, and passes; a proxy that refuses the credentials is proxy_refused and
// nothing reaches the fake.
func TestDryProbeThroughProxy(t *testing.T) {
	f := startFake(t)
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0",
		fakeproxy.Options{Username: "u", Password: "proxy-pw-right"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	for _, tc := range []struct{ password, code string }{{"proxy-pw-right", ""},
		{"proxy-pw-wrong", delivery.CodeProxyRefused}} {
		f.ResetRequests()
		c := newClient(t, network(t, nil), Settings{BotToken: fakeslack.BotToken, Route: devRoute(f),
			Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP, Address: proxy.Addr(), Username: "u",
				Password: logging.Secret(tc.password)}})
		p, err := RunDryProbe(t.Context(), c, direct, tick())
		if err != nil || p.OK != (tc.code == "") || p.Code != tc.code || c.Via() != ViaProxy {
			t.Fatalf("%s: %+v, %v", tc.password, p, err)
		}
		reqs := f.Requests()
		if tc.code == "" && (len(reqs) != 1 || len(reqs[0].Headers["Authorization"]) != 0 ||
			len(proxy.Connections()) == 0) {
			t.Fatalf("through the proxy: %+v, %+v", reqs, proxy.Connections())
		}
		if tc.code != "" && (len(reqs) != 0 || p.Message != MessageProxyRefused) {
			t.Fatalf("a refused proxy passed %d requests: %+v", len(reqs), p)
		}
	}
}
