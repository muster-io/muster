// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/outbound"
)

var direct = Direct(delivery.Call{Class: outbound.ClassInteractive})

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

// TestCheckPasses covers C-14.FR-11 and AC-5: the dry probe passes on the 401 JSON answer to /bot0:x/getMe, getMe
// names the bot, getWebhookInfo reports no webhook and the pending updates; the dry probe carries no token.
func TestCheckPasses(t *testing.T) {
	f := startFake(t)
	if _, err := f.Enqueue(t.Context(), testToken, []byte(`{"message":{}}`)); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	res, err := RunCheck(t.Context(), c, nil, direct, clock.Real{})
	if err != nil || !res.OK() || names(res) != "dry_probe:ok get_me:ok get_webhook_info:ok" {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if res.Bot.Username != faketelegram.BotUsername || res.Webhook.URL != "" || res.Webhook.PendingUpdateCount != 1 ||
		res.Steps[2].Message != "" {
		t.Fatalf("bot %+v webhook %+v", res.Bot, res.Webhook)
	}
	if _, failed := res.Failed(); failed {
		t.Fatal("a passed check failed")
	}
	reqs := f.Requests()
	if reqs[0].Path != "/bot0:x/getMe" || strings.Contains(reqs[0].Path+reqs[0].Query, testToken) {
		t.Fatalf("dry probe %s", reqs[0].Path)
	}
}

// TestCheckWebhookSet: getWebhookInfo names the host of a webhook that is set, which makes long polling fail with 409.
func TestCheckWebhookSet(t *testing.T) {
	_, base := serve(t, jsonAnswer(401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`),
		jsonAnswer(200, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"b"}}`),
		jsonAnswer(200, `{"ok":true,"result":{"url":"https://hooks.example.org/x/y","pending_update_count":3}}`))
	res, err := RunCheck(t.Context(), newClient(t, Settings{BaseURL: base, Token: testToken}), nil, direct, clock.Real{})
	if err != nil || !res.OK() || res.Steps[2].Message != "A webhook is set at hooks.example.org." ||
		res.Webhook.PendingUpdateCount != 3 {
		t.Fatalf("check = %+v, %v", res, err)
	}
	_, base = serve(t, jsonAnswer(401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`),
		jsonAnswer(200, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"b"}}`),
		jsonAnswer(200, `{"ok":true,"result":{"url":"::bad","pending_update_count":0}}`))
	res, _ = RunCheck(t.Context(), newClient(t, Settings{BaseURL: base, Token: testToken}), nil, direct, clock.Real{})
	if res.Steps[2].Message != "A webhook is set." {
		t.Fatalf("webhook without a host = %+v", res.Steps[2])
	}
}

// TestCheckFailures covers C-14.FR-11 and AC-5: the message of each answer the dry probe gets, and the steps after it.
func TestCheckFailures(t *testing.T) {
	f := startFake(t)
	html := faketelegram.ModeHTML
	tlsServer := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(tlsServer.Close)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	httpProxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0",
		fakeproxy.Options{Username: "u", Password: "right"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = httpProxy.Close() })
	socks, err := fakeproxy.Start(t.Context(), fakeproxy.SOCKS5, "127.0.0.1:0",
		fakeproxy.Options{Username: "u", Password: "right"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socks.Close() })
	_, ok200 := serve(t, jsonAnswer(200, `{"ok":true,"result":{"id":1}}`))
	cases := []struct {
		name    string
		setup   func()
		client  func() *Client
		ctx     func() (context.Context, context.CancelFunc)
		message string
	}{
		{name: "html", setup: func() { _ = f.SetConfig(nil, &html, nil) },
			client:  func() *Client { return newClient(t, Settings{BaseURL: f.URL(), Token: testToken}) },
			message: MessageNotBotAPI},
		{name: "wrong prefix", client: func() *Client {
			return newClient(t, Settings{BaseURL: f.URL() + "/other", Token: testToken})
		}, message: MessageWrongPrefix},
		{name: "dns", client: func() *Client {
			n := network(t, nil)
			n.Resolver = failingResolver{}
			c, _ := NewClient(n, Settings{BaseURL: "https://bot-api.invalid/x", Token: testToken})
			return c
		}, message: "DNS lookup failed: bot-api.invalid"},
		{name: "tls", client: func() *Client { return newClient(t, Settings{BaseURL: tlsServer.URL, Token: testToken}) },
			message: "TLS handshake failed: tls: failed to verify certificate"},
		{name: "timeout", client: func() *Client { return newClient(t, Settings{BaseURL: slow.URL, Token: testToken}) },
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(t.Context(), 100*time.Millisecond)
			}, message: "No answer within 10 s"},
		{name: "http proxy 407", client: func() *Client {
			return newClient(t, Settings{BaseURL: f.URL(), Token: testToken, Proxy: &outbound.Proxy{
				Type: outbound.ProxyHTTP, Address: httpProxy.Addr(), Username: "u", Password: "wrong"}})
		}, message: MessageProxyRefused},
		{name: "socks5 refused credentials", client: func() *Client {
			return newClient(t, Settings{BaseURL: f.URL(), Token: testToken, Proxy: &outbound.Proxy{
				Type: outbound.ProxySOCKS5, Address: socks.Addr(), Username: "u", Password: "wrong"}})
		}, message: MessageProxyRefused},
		{name: "refused", client: func() *Client {
			return newClient(t, Settings{BaseURL: "http://127.0.0.1:1", Token: testToken})
		}, message: "connection refused"},
		{name: "another status", client: func() *Client { return newClient(t, Settings{BaseURL: ok200, Token: testToken}) },
			message: "The server answered 200 instead of 401 with JSON."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bot := faketelegram.ModeBotAPI
			_ = f.SetConfig(nil, &bot, nil)
			if tc.setup != nil {
				tc.setup()
			}
			ctx, cancel := context.WithCancel(t.Context())
			if tc.ctx != nil {
				ctx, cancel = tc.ctx()
			}
			defer cancel()
			f.ResetRequests()
			res, err := RunCheck(ctx, tc.client(), nil, direct, clock.Real{})
			if err != nil || res.OK() || names(res) != "dry_probe:fail get_me:skip get_webhook_info:skip" ||
				!strings.Contains(res.Steps[0].Message, tc.message) {
				t.Fatalf("check = %+v, %v", res, err)
			}
			for _, r := range f.Requests() {
				if strings.Contains(r.Path, testToken) {
					t.Fatalf("the token went to a server that failed the dry probe: %s", r.Path)
				}
			}
		})
	}
}

// failingResolver answers every lookup with a DNS error.
type failingResolver struct{}

func (failingResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// TestCheckUnsavedBaseURL covers C-14.FR-11 and AC-6: with an unsaved base URL only the dry probe runs, against it,
// and no request with the token reaches it.
func TestCheckUnsavedBaseURL(t *testing.T) {
	saved := startFake(t)
	unsaved := startFake(t)
	c := newClient(t, Settings{BaseURL: saved.URL(), Token: testToken})
	probe := newClient(t, Settings{BaseURL: unsaved.URL()})
	res, err := RunCheck(t.Context(), c, probe, direct, clock.Real{})
	if err != nil || !res.OK() || names(res) != "dry_probe:ok get_me:skip get_webhook_info:skip" || res.Bot != nil {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if len(saved.Requests()) != 0 || len(unsaved.Requests()) != 1 || unsaved.Requests()[0].Path != "/bot0:x/getMe" {
		t.Fatalf("saved %v unsaved %v", paths(saved), paths(unsaved))
	}
}

// TestCheckTokenRefused: getMe that answers 401 fails with "The bot token is not valid." and skips getWebhookInfo; any
// other failure shows its masked text.
func TestCheckTokenRefused(t *testing.T) {
	f := startFake(t)
	revoked := []string{testToken}
	_ = f.SetConfig(nil, nil, &revoked)
	res, err := RunCheck(t.Context(), newClient(t, Settings{BaseURL: f.URL(), Token: testToken}), nil, direct,
		clock.Real{})
	if err != nil || res.OK() || names(res) != "dry_probe:ok get_me:fail get_webhook_info:skip" ||
		res.Steps[1].Message != MessageTokenInvalid {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if st, failed := res.Failed(); !failed || st.Name != StepGetMe {
		t.Fatalf("failed step %+v", st)
	}
	_, base := serve(t, jsonAnswer(401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`),
		jsonAnswer(500, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`))
	res, _ = RunCheck(t.Context(), newClient(t, Settings{BaseURL: base, Token: testToken}), nil, direct, clock.Real{})
	if res.Steps[1].Message != "Telegram answered 500: Internal Server Error" {
		t.Fatalf("getMe 500 = %+v", res.Steps[1])
	}
	_, base = serve(t, jsonAnswer(401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`),
		jsonAnswer(200, `{"ok":true,"result":{"id":1,"username":"b"}}`),
		jsonAnswer(403, `{"ok":false,"error_code":403,"description":"Forbidden"}`))
	res, _ = RunCheck(t.Context(), newClient(t, Settings{BaseURL: base, Token: testToken}), nil, direct, clock.Real{})
	if res.OK() || res.Steps[2].Message != "Telegram answered 403: Forbidden" || res.Webhook != nil {
		t.Fatalf("getWebhookInfo 403 = %+v", res)
	}
}

var errLimited = errors.New("no token")

// limited is a Runner that calls at once and fails after limit calls, as an interactive path without a token would.
func limited(limit int) Runner {
	calls := 0
	return func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		calls++
		if calls > limit {
			return delivery.Outcome{}, errLimited
		}
		return f(ctx, delivery.Call{Class: outbound.ClassInteractive}), nil
	}
}

// TestCheckRunnerErrors: an error of the runner — no limiter token — ends the check at any step.
func TestCheckRunnerErrors(t *testing.T) {
	f := startFake(t)
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	for limit := range 3 {
		if _, err := RunCheck(t.Context(), c, nil, limited(limit), clock.Real{}); !errors.Is(err, errLimited) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	if (Check{}).OK() {
		t.Fatal("a check without steps passed")
	}
}

// TestCheckOnTheInteractivePath: the Interactive runner takes the tokens of its subject and calls in the interactive
// class.
func TestCheckOnTheInteractivePath(t *testing.T) {
	f := startFake(t)
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	id := int64(7)
	clocks := clock.Clocks{Business: clock.Real{}, Real: clock.Real{}}
	res, err := RunCheck(t.Context(), c, nil, Interactive(deliverytest.Unlimited(1, clocks),
		delivery.Subject{Connection: &id}), clock.Real{})
	if err != nil || !res.OK() {
		t.Fatalf("check = %+v, %v", res, err)
	}
}
