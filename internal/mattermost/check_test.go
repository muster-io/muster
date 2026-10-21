// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

const token = "mm-test-token-0123456789"

var (
	interactive = mattermost.Direct(delivery.Call{Class: outbound.ClassInteractive})
	t0          = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
)

// startFake starts the fake Mattermost server on a free port of loopback.
func startFake(t *testing.T) *fakemattermost.Fake {
	t.Helper()
	f := fakemattermost.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	return f
}

// network is what a client needs, with an outbound address policy that allows loopback.
func network(t *testing.T, log *bytes.Buffer, allowed ...string) mattermost.Network {
	t.Helper()
	p, err := outbound.ParsePolicy("standard", allowed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mattermost.Network{Policy: outbound.StaticPolicy(p), Log: logging.New(log, logging.LevelInfo),
		Real: clock.Real{}}
}

func newClient(t *testing.T, serverURL string, proxy *outbound.Proxy) *mattermost.Client {
	t.Helper()
	var log bytes.Buffer
	c, err := mattermost.NewClient(network(t, &log, "127.0.0.0/8"), mattermost.Settings{ServerURL: serverURL,
		Token: token, Proxy: proxy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if strings.Contains(log.String(), token) {
			t.Errorf("the bot token reached the log: %s", log.String())
		}
	})
	return c
}

// control calls a control endpoint of the fake.
func control(t *testing.T, method, url, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s = %d", method, url, resp.StatusCode)
	}
}

func fault(t *testing.T, f *fakemattermost.Fake, ft fakeserver.Fault) {
	t.Helper()
	if err := f.SetFault(ft); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.ResetFaults)
}

// TestClientReads is the REST client of C-13.FR-1: the bot, its teams and channels, a team, a channel and the bot's
// membership, each with the bot token as a bearer token.
func TestClientReads(t *testing.T) {
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	ctx, class := t.Context(), outbound.ClassInteractive
	if u, r := c.Me(ctx, class); !r.OK() || r.Status != http.StatusOK || u.ID != fakemattermost.BotUserID ||
		u.Username != fakemattermost.BotUsername || !u.IsBot {
		t.Errorf("Me = %+v, %+v", u, r)
	}
	if ts, r := c.Teams(ctx, class); !r.OK() || len(ts) != 1 || ts[0].ID != fakemattermost.TeamID ||
		ts[0].Name != fakemattermost.TeamName || ts[0].DisplayName != "Dev" {
		t.Errorf("Teams = %+v, %+v", ts, r)
	}
	if cs, r := c.Channels(ctx, class, fakemattermost.TeamID); !r.OK() || len(cs) != 2 ||
		cs[0].ID != fakemattermost.ChannelAlerts || cs[1].ID != fakemattermost.ChannelAlertsProd ||
		cs[0].Type != mattermost.ChannelOpen || cs[0].TeamID != fakemattermost.TeamID {
		t.Errorf("Channels = %+v, %+v", cs, r)
	}
	if cs, r := c.Channels(ctx, class, "team-other"); r.OK() || r.Status != http.StatusNotFound ||
		r.Outcome.Kind != delivery.OutcomeFatal || r.ErrorID != "app.team.get.find.app_error" || cs != nil ||
		!strings.Contains(string(r.Outcome.Error), "Mattermost answered 404") {
		t.Errorf("Channels of another team = %+v, %+v", cs, r)
	}
	if tm, r := c.Team(ctx, class, fakemattermost.TeamID); !r.OK() || tm.Name != "dev" {
		t.Errorf("Team = %+v, %+v", tm, r)
	}
	if ch, r := c.Channel(ctx, class, fakemattermost.ChannelAlerts); !r.OK() || ch.Name != "alerts" ||
		ch.DisplayName != "Alerts" || ch.DeleteAt != 0 {
		t.Errorf("Channel = %+v, %+v", ch, r)
	}
	if r := c.Member(ctx, class, fakemattermost.ChannelAlerts); !r.OK() {
		t.Errorf("Member = %+v", r)
	}
	if r := c.Member(ctx, class, fakemattermost.ChannelNoBot); r.OK() || r.Status != http.StatusNotFound ||
		r.ErrorID != "app.channel.get_member.missing.app_error" {
		t.Errorf("Member of no-bot = %+v", r)
	}
	if u, r := c.Me(ctx, outbound.ClassBackground); !r.OK() || u.ID != fakemattermost.BotUserID {
		t.Errorf("Me in the background class = %+v, %+v", u, r)
	}
	if _, r := c.Me(ctx, outbound.ClassHeartbeat); r.Outcome.Kind != delivery.OutcomeFatal ||
		!strings.Contains(string(r.Outcome.Error), "no heartbeat class") {
		t.Errorf("Me in the heartbeat class = %+v", r)
	}
	for _, req := range f.Requests() {
		if got := req.Headers["Authorization"]; len(got) != 1 || got[0] != "Bearer "+token {
			t.Errorf("%s %s: Authorization %v", req.Method, req.Path, got)
		}
	}
}

// TestCheckDestination is the Destination check of C-13.FR-10 against the fake server: a channel with the bot passes
// with the names of its team and channel; a revoked token fails the token step with "The bot token is not valid.";
// the bot removed from the channel, an unknown channel, a channel of another team and an archived one fail
// bot_in_channel.
func TestCheckDestination(t *testing.T) {
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	ctx := t.Context()
	res, err := mattermost.CheckDestination(ctx, c, interactive, fakemattermost.TeamID, fakemattermost.ChannelAlerts)
	if err != nil || !res.OK() || res.TeamName != "dev" || res.ChannelName != "alerts" || len(res.Steps) != 2 ||
		res.Steps[0].Name != mattermost.StepToken || !res.Steps[0].OK || res.Steps[1].Name != mattermost.StepBotInChannel ||
		!res.Steps[1].OK {
		t.Fatalf("check = %+v, %v", res, err)
	}
	failing := func(t *testing.T, channel, step, message string, kind delivery.OutcomeKind) {
		t.Helper()
		res, err := mattermost.CheckDestination(ctx, c, interactive, fakemattermost.TeamID, channel)
		last := res.Steps[len(res.Steps)-1]
		codes := map[string]string{mattermost.MessageNotMember: mattermost.CodeNotMember,
			mattermost.MessageOtherTeam: mattermost.CodeOtherTeam, mattermost.MessageArchived: mattermost.CodeArchived,
			mattermost.MessageTokenInvalid: mattermost.CodeTokenInvalid}
		if err != nil || res.OK() || last.Name != step || last.OK || last.Message != message ||
			last.Code != codes[message] ||
			res.Outcome.Kind != kind || last.Outcome.Kind != kind || res.TeamName != "" {
			t.Errorf("check of %s = %+v, %v", channel, res, err)
		}
	}
	control(t, http.MethodDelete, f.URL()+"/_fake/channels/ch-alerts/members/"+fakemattermost.BotUserID, "")
	failing(t, fakemattermost.ChannelAlerts, mattermost.StepBotInChannel, mattermost.MessageNotMember,
		delivery.OutcomeFatal)
	control(t, http.MethodPut, f.URL()+"/_fake/channels/ch-alerts/members/"+fakemattermost.BotUserID, "")
	failing(t, "ch-unknown", mattermost.StepBotInChannel, mattermost.MessageNotMember, delivery.OutcomeFatal)
	control(t, http.MethodPost, f.URL()+"/api/v4/channels/direct",
		`["`+fakemattermost.BotUserID+`","`+fakemattermost.AliceUserID+`"]`)
	failing(t, fakemattermost.BotUserID+"__"+fakemattermost.AliceUserID, mattermost.StepBotInChannel,
		mattermost.MessageOtherTeam, delivery.OutcomeFatal)
	control(t, http.MethodPost, f.URL()+"/_fake/channels/"+fakemattermost.ChannelAlertsProd+"/archive", "")
	failing(t, fakemattermost.ChannelAlertsProd, mattermost.StepBotInChannel, mattermost.MessageArchived,
		delivery.OutcomeFatal)
	control(t, http.MethodPut, f.URL()+"/_fake/config", `{"revoked_tokens":["`+token+`"]}`)
	failing(t, fakemattermost.ChannelAlerts, mattermost.StepToken, mattermost.MessageTokenInvalid,
		delivery.OutcomeFatal)
	if res, _ := mattermost.CheckDestination(ctx, c, interactive, fakemattermost.TeamID,
		fakemattermost.ChannelAlerts); len(res.Steps) != 1 {
		t.Errorf("a failed token step went on: %+v", res)
	}
}

// TestCheckDestinationFailures: a step that fails for another reason than its own message carries the masked text of
// the answer, and the reads of the channel and its team fail bot_in_channel.
func TestCheckDestinationFailures(t *testing.T) {
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	cases := []struct {
		path string
		step string
		kind delivery.OutcomeKind
	}{
		{"/api/v4/users/me", mattermost.StepToken, delivery.OutcomeTransient},
		{"/api/v4/channels/ch-alerts/members/me", mattermost.StepBotInChannel, delivery.OutcomeTransient},
		{"/api/v4/channels/ch-alerts", mattermost.StepBotInChannel, delivery.OutcomeTransient},
		{"/api/v4/teams/team-dev", mattermost.StepBotInChannel, delivery.OutcomeTransient},
	}
	for _, tc := range cases {
		fault(t, f, fakeserver.Fault{Path: tc.path, Status: http.StatusServiceUnavailable, Times: 1,
			Body: `{"id":"app.down","message":"down for ` + token + `"}`})
		res, err := mattermost.CheckDestination(t.Context(), c, interactive, fakemattermost.TeamID,
			fakemattermost.ChannelAlerts)
		last := res.Steps[len(res.Steps)-1]
		if err != nil || res.OK() || last.Name != tc.step || res.Outcome.Kind != tc.kind ||
			!strings.Contains(last.Message, "Mattermost answered 503: down for") ||
			strings.Contains(last.Message, token) || last.Code != delivery.CodeRequestFailed {
			t.Errorf("%s: %+v, %v", tc.path, res, err)
		}
	}
}

// TestCheckDestinationRunnerError: an error of the runner — no limiter token, a failed limiter — ends the check with
// that error at whichever request it happens.
func TestCheckDestinationRunnerError(t *testing.T) {
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	boom := errors.New("limiter down")
	for n := range 4 {
		calls := 0
		run := func(ctx context.Context, fn func(ctx context.Context, c delivery.Call) delivery.Outcome) (
			delivery.Outcome, error) {
			if calls == n {
				return delivery.Outcome{}, boom
			}
			calls++
			return fn(ctx, delivery.Call{Class: outbound.ClassInteractive}), nil
		}
		res, err := mattermost.CheckDestination(t.Context(), c, run, fakemattermost.TeamID,
			fakemattermost.ChannelAlerts)
		if !errors.Is(err, boom) || len(res.Steps) != 0 || calls != n {
			t.Errorf("failure at request %d: %+v, %v", n+1, res, err)
		}
	}
}

// TestResponseMapping is the response mapping of C-13.FR-5: 429 holds the whole Connection for its Retry-After, 1 s
// without one (F-031); 5xx, 408, timeouts and refused connections are Transient; 401, 403, 404, a blocked address
// and a redirect are Fatal; 400 and an answer that is not the expected JSON are unknown.
func TestResponseMapping(t *testing.T) {
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	cases := []struct {
		name  string
		fault fakeserver.Fault
		kind  delivery.OutcomeKind
		retry time.Duration
		text  string
	}{
		{"429 with Retry-After", fakeserver.Fault{Status: 429, RetryAfterSeconds: 3, Body: "limit exceeded"},
			delivery.OutcomeRetryAfter, 3 * time.Second, "answered 429"},
		{"429 without Retry-After", fakeserver.Fault{Status: 429, Body: "limit exceeded"}, delivery.OutcomeRetryAfter,
			time.Second, "answered 429"},
		{"503", fakeserver.Fault{Status: 503}, delivery.OutcomeTransient, 0, "answered 503"},
		{"500", fakeserver.Fault{Status: 500}, delivery.OutcomeTransient, 0, "answered 500"},
		{"408", fakeserver.Fault{Status: 408}, delivery.OutcomeTransient, 0, "answered 408"},
		{"401", fakeserver.Fault{Status: 401}, delivery.OutcomeFatal, 0, "answered 401"},
		{"403", fakeserver.Fault{Status: 403, Body: `{"id":"api.context.permissions.app_error","message":"no"}`},
			delivery.OutcomeFatal, 0, "Mattermost answered 403: no (api.context.permissions.app_error)"},
		{"404", fakeserver.Fault{Status: 404}, delivery.OutcomeFatal, 0, "answered 404"},
		{"400", fakeserver.Fault{Status: 400}, delivery.OutcomeUnknown, 0, "answered 400"},
		{"302", fakeserver.Fault{Status: 302}, delivery.OutcomeFatal, 0, "redirect refused"},
		{"200 not JSON", fakeserver.Fault{Status: 200, Body: "<html>", ContentType: "text/html"},
			delivery.OutcomeUnknown, 0, "the answer is not the expected JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.fault.Path, tc.fault.Times = "/api/v4/users/me", 1
			fault(t, f, tc.fault)
			_, r := c.Me(t.Context(), outbound.ClassInteractive)
			if r.Outcome.Kind != tc.kind || r.Outcome.RetryAfter != tc.retry ||
				!strings.Contains(string(r.Outcome.Error), tc.text) || r.FailureCode() != delivery.CodeRequestFailed {
				t.Errorf("%s: %+v", tc.name, r)
			}
			if tc.kind == delivery.OutcomeRetryAfter && r.Outcome.Scope != delivery.ScopeConnection {
				t.Errorf("scope %q", r.Outcome.Scope)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		fault(t, f, fakeserver.Fault{Path: "/api/v4/users/me", Status: 200, DelayMs: 5000, Times: 1})
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		if _, r := c.Me(ctx, outbound.ClassInteractive); r.Outcome.Kind != delivery.OutcomeTransient || r.Status != 0 ||
			r.FailureCode() != delivery.CodeTimeout {
			t.Errorf("timeout: %+v", r)
		}
	})
	t.Run("refused connection", func(t *testing.T) {
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		if _, r := newClient(t, "http://"+addr, nil).Me(t.Context(), outbound.ClassInteractive); r.Outcome.Kind !=
			delivery.OutcomeTransient || r.FailureCode() != delivery.CodeConnectFailed {
			t.Errorf("refused: %+v", r)
		}
	})
	t.Run("blocked address", func(t *testing.T) {
		var log bytes.Buffer
		blocked, err := mattermost.NewClient(network(t, &log), mattermost.Settings{ServerURL: f.URL(), Token: token})
		if err != nil {
			t.Fatal(err)
		}
		before := len(f.Requests())
		if _, r := blocked.Me(t.Context(), outbound.ClassInteractive); r.Outcome.Kind != delivery.OutcomeFatal ||
			len(f.Requests()) != before || r.FailureCode() != delivery.CodeBlocked {
			t.Errorf("blocked: %+v", r)
		}
		if !strings.Contains(log.String(), `"event":"outbound_blocked"`) || strings.Contains(log.String(), token) {
			t.Errorf("log %s", log.String())
		}
	})
}

// TestVia is the path of a Connection's requests, direct or through its proxy; a proxy that does not parse fails the
// client.
func TestVia(t *testing.T) {
	var log bytes.Buffer
	n := network(t, &log, "127.0.0.0/8")
	direct, err := mattermost.NewClient(n, mattermost.Settings{ServerURL: "http://127.0.0.1:1", Token: token})
	if err != nil || direct.Via() != mattermost.ViaDirect {
		t.Errorf("direct = %v, %v", direct, err)
	}
	proxied, err := mattermost.NewClient(n, mattermost.Settings{ServerURL: "http://127.0.0.1:1", Token: token,
		Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP, Address: "127.0.0.1:2"}})
	if err != nil || proxied.Via() != mattermost.ViaProxy {
		t.Errorf("proxy = %v, %v", proxied, err)
	}
	if _, err := mattermost.NewClient(n, mattermost.Settings{ServerURL: "http://127.0.0.1:1", Token: token,
		Proxy: &outbound.Proxy{Type: "ftp", Address: "127.0.0.1:2"}}); err == nil {
		t.Error("a client with an ftp proxy")
	}
}

// TestProxy is C-13.AC-7: with a proxy on the Connection, every request of the Destination check and of the reads
// reaches the fake server only through the fake proxy, HTTP and SOCKS5.
func TestProxy(t *testing.T) {
	f := startFake(t)
	host := strings.TrimPrefix(f.URL(), "http://")
	for _, kind := range []fakeproxy.Kind{fakeproxy.HTTP, fakeproxy.SOCKS5} {
		t.Run(string(kind), func(t *testing.T) {
			p, err := fakeproxy.Start(t.Context(), kind, "127.0.0.1:0", fakeproxy.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			c := newClient(t, f.URL(), &outbound.Proxy{Type: outbound.ProxyType(kind), Address: p.Addr()})
			f.ResetRequests()
			res, err := mattermost.CheckDestination(t.Context(), c, interactive, fakemattermost.TeamID,
				fakemattermost.ChannelAlerts)
			if err != nil || !res.OK() {
				t.Fatalf("check through the proxy = %+v, %v", res, err)
			}
			if _, r := c.Channels(t.Context(), outbound.ClassInteractive, fakemattermost.TeamID); !r.OK() {
				t.Fatalf("channels through the proxy = %+v", r)
			}
			reqs, targets := f.Requests(), p.Targets()
			if len(reqs) != 5 || len(targets) == 0 {
				t.Fatalf("%d requests, targets %v", len(reqs), targets)
			}
			for _, tg := range targets {
				if tg != host {
					t.Errorf("the proxy connected to %s", tg)
				}
			}
			if kind == fakeproxy.HTTP && len(targets) != len(reqs) {
				t.Errorf("%d requests reached the fake, %d through the proxy", len(reqs), len(targets))
			}
		})
	}
}

// TestDirect is the Runner of the Broken probe: it calls at once with the Call it was given, in its client class.
func TestDirect(t *testing.T) {
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	call := delivery.Call{Class: outbound.ClassDelivery, Destination: delivery.Destination{ID: 7}}
	var got delivery.Call
	out, err := mattermost.Direct(call)(t.Context(), func(_ context.Context, c delivery.Call) delivery.Outcome {
		got = c
		return delivery.Outcome{Kind: delivery.OutcomeTransient}
	})
	if err != nil || out.Kind != delivery.OutcomeTransient || got.Class != outbound.ClassDelivery ||
		got.Destination.ID != 7 {
		t.Errorf("direct = %+v, %v, %+v", out, err, got)
	}
	counter := metrics.ClientRequests.With(string(outbound.ClassDelivery), string(outbound.OutcomeOK))
	before := counter.Get()
	res, err := mattermost.CheckDestination(t.Context(), c, mattermost.Direct(call), fakemattermost.TeamID,
		fakemattermost.ChannelAlerts)
	if err != nil || !res.OK() || counter.Get()-before != 4 {
		t.Errorf("check in the delivery class = %+v, %v, %d requests", res, err, counter.Get()-before)
	}
}

// TestInteractive is C-11.FR-2: a check a person waits for takes each request through the interactive path, limited
// by its subject, in the interactive client class.
func TestInteractive(t *testing.T) {
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	conn := int64(3)
	in := deliverytest.Unlimited(1, clock.Clocks{Business: clock.NewManual(t0), Real: clock.NewManual(t0)})
	counter := metrics.ClientRequests.With(string(outbound.ClassInteractive), string(outbound.OutcomeOK))
	before := counter.Get()
	res, err := mattermost.CheckDestination(t.Context(), c, mattermost.Interactive(in, delivery.Subject{
		Connection: &conn}), fakemattermost.TeamID, fakemattermost.ChannelAlerts)
	if err != nil || !res.OK() || counter.Get()-before != 4 {
		t.Errorf("interactive check = %+v, %v, %d requests", res, err, counter.Get()-before)
	}
}

// limitedPath is an interactive path whose limiter has no token within the budget.
type limitedPath struct {
	calls int
}

func (p *limitedPath) Do(context.Context, delivery.Subject, delivery.Op) (delivery.Outcome, error) {
	p.calls++
	return delivery.Outcome{}, &delivery.LimitedError{RetryAfter: 3 * time.Second}
}

// TestCheckLimited is C-11.FR-2: with an exhausted limiter the Destination check ends with *delivery.LimitedError,
// which the API answers with 503 and Retry-After, and no request reaches the server.
func TestCheckLimited(t *testing.T) {
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	conn := int64(3)
	p := &limitedPath{}
	res, err := mattermost.CheckDestination(t.Context(), c, mattermost.Interactive(p, delivery.Subject{Connection: &conn}),
		fakemattermost.TeamID, fakemattermost.ChannelAlerts)
	limited, ok := errors.AsType[*delivery.LimitedError](err)
	if !ok || limited.RetryAfter != 3*time.Second || limited.Seconds() != 3 || len(res.Steps) != 0 || p.calls != 1 {
		t.Errorf("limited check = %+v, %v", res, err)
	}
	if n := len(f.Requests()); n != 0 {
		t.Errorf("%d requests reached the server", n)
	}
}

// TestAppErrorText: the error id and message of a Mattermost error answer are read, masked of the bot token.
func TestAppErrorText(t *testing.T) {
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	body, _ := json.Marshal(map[string]string{"id": "api.context.session_expired.app_error",
		"message": "token " + token + " expired"})
	fault(t, f, fakeserver.Fault{Path: "/api/v4/users/me", Status: 401, Body: string(body), Times: 1})
	_, r := c.Me(t.Context(), outbound.ClassInteractive)
	if r.ErrorID != "api.context.session_expired.app_error" || strings.Contains(string(r.Outcome.Error), token) ||
		!strings.Contains(string(r.Outcome.Error), "[redacted]") {
		t.Errorf("app error = %+v", r)
	}
}

// newTokenless is a client without a token, as the check of a draft builds it for the dry probe.
func newTokenless(t *testing.T, n mattermost.Network, serverURL string, proxy *outbound.Proxy) *mattermost.Client {
	t.Helper()
	c, err := mattermost.NewClient(n, mattermost.Settings{ServerURL: serverURL, Proxy: proxy})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// wantProbeRequest fails the test unless the fake recorded only the dry probe, without an Authorization header.
func wantProbeRequest(t *testing.T, f *fakemattermost.Fake) {
	t.Helper()
	reqs := f.Requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Path != mattermost.DryProbePath ||
		len(reqs[0].Headers["Authorization"]) != 0 {
		t.Errorf("requests %+v", reqs)
	}
}

// TestDryProbe covers C-13.FR-2 and AC-17: the dry probe is GET /api/v4/system/ping without an Authorization header,
// on a client without a token and on one with a token alike; it passes on the 200 JSON answer whose status is OK and
// names every other answer in the order of the PRD, with its latency on the real clock it is given.
func TestDryProbe(t *testing.T) {
	f := startFake(t)
	clients := []*mattermost.Client{newTokenless(t, network(t, &bytes.Buffer{}, "127.0.0.0/8"), f.URL(), nil),
		newClient(t, f.URL(), nil)}
	for _, tc := range []struct {
		name    string
		fault   fakeserver.Fault
		message string
		code    string
	}{
		{name: "the answer of a Mattermost server"},
		{name: "200 with another status", fault: fakeserver.Fault{Status: 200, Body: `{"status":"DOWN"}`},
			message: "The address answered 200 with JSON whose status is not OK.", code: mattermost.CodeNotMattermost},
		{name: "200 with a status that is not text", fault: fakeserver.Fault{Status: 200, Body: `{"status":true}`},
			message: "The address answered 200 with JSON whose status is not OK.", code: mattermost.CodeNotMattermost},
		{name: "200 HTML", fault: fakeserver.Fault{Status: 200, ContentType: "text/html; charset=utf-8",
			Body: "<html>OK</html>"}, message: "The address answered 200 with text/html, not as a Mattermost server.",
			code: mattermost.CodeNotMattermost},
		{name: "200 JSON that is not an object", fault: fakeserver.Fault{Status: 200, Body: `["OK"]`},
			message: "The address answered 200 with application/json, not as a Mattermost server.",
			code:    mattermost.CodeNotMattermost},
		{name: "500 HTML", fault: fakeserver.Fault{Status: 500, ContentType: "text/html", Body: "<p>down</p>"},
			message: "The address answered 500 with text/html, not as a Mattermost server.",
			code:    mattermost.CodeNotMattermost},
		{name: "a redirect", fault: fakeserver.Fault{Status: 302, Location: "https://example.org/"},
			message: "The address answered 302 with text/plain, not as a Mattermost server.",
			code:    mattermost.CodeNotMattermost},
		{name: "404", fault: fakeserver.Fault{Status: 404, Body: "404 page not found"},
			message: mattermost.MessageWrongPrefix, code: mattermost.CodeWrongPrefix},
		{name: "404 JSON", fault: fakeserver.Fault{Status: 404, Body: `{"id":"api.context.404.app_error"}`},
			message: mattermost.MessageWrongPrefix, code: mattermost.CodeWrongPrefix},
		{name: "503 JSON", fault: fakeserver.Fault{Status: 503, Body: `{"status":"DOWN"}`},
			message: "The server answered 503 instead of 200 with JSON whose status is OK.",
			code:    mattermost.CodeUnexpectedStatus},
		{name: "407", fault: fakeserver.Fault{Status: 407, Body: "proxy"},
			message: mattermost.MessageProxyRefused, code: delivery.CodeProxyRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.fault.Status != 0 {
				tc.fault.Path = mattermost.DryProbePath
				fault(t, f, tc.fault)
			}
			for i, c := range clients {
				f.ResetRequests()
				p, err := mattermost.RunDryProbe(t.Context(), c, interactive, clock.NewManual(t0))
				if err != nil || p.OK != (tc.code == "") || p.Message != tc.message || p.Code != tc.code {
					t.Errorf("client %d: probe = %+v, %v", i, p, err)
				}
				wantProbeRequest(t, f)
			}
		})
	}

	realClock := clock.NewManual(t0)
	seen := make(chan http.Header, 1)
	standIn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		realClock.Advance(42 * time.Millisecond)
		w.Header()["Content-Type"] = nil
		_, _ = w.Write([]byte("OK"))
	}))
	t.Cleanup(standIn.Close)
	c := newTokenless(t, network(t, &bytes.Buffer{}, "127.0.0.0/8"), standIn.URL+"/mm/", nil)
	p, err := mattermost.RunDryProbe(t.Context(), c, interactive, realClock)
	if err != nil || p.OK || p.Latency != 42*time.Millisecond || p.Code != mattermost.CodeNotMattermost ||
		p.Message != "The address answered 200 with no content type, not as a Mattermost server." {
		t.Errorf("probe without a content type = %+v, %v", p, err)
	}
	if h := <-seen; h.Get("Authorization") != "" {
		t.Errorf("the probe carried Authorization: %v", h)
	}

	boom := errors.New("limiter down")
	if _, err := mattermost.RunDryProbe(t.Context(), c, func(context.Context,
		func(context.Context, delivery.Call) delivery.Outcome) (delivery.Outcome, error) {
		return delivery.Outcome{}, boom
	}, realClock); !errors.Is(err, boom) {
		t.Errorf("the runner's error = %v", err)
	}
}

// failingResolver answers every lookup with a DNS error.
type failingResolver struct{}

func (failingResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// TestDryProbeWithoutAnswer covers C-13.FR-2: a dry probe without an answer has the code of delivery.FailureCode — an
// unknown name, a refused connection, a failed TLS handshake, no answer in time, an address the outbound address
// policy refuses — and a proxy that refuses the credentials, SOCKS5 or HTTP, is proxy_refused; an unknown name, the
// time and the proxy have the text of delivery.FailureMessage. Nothing reaches the server.
func TestDryProbeWithoutAnswer(t *testing.T) {
	f := startFake(t)
	proxies := map[fakeproxy.Kind]string{}
	for _, kind := range []fakeproxy.Kind{fakeproxy.SOCKS5, fakeproxy.HTTP} {
		p, err := fakeproxy.Start(t.Context(), kind, "127.0.0.1:0", fakeproxy.Options{Username: "u", Password: "right"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Close() })
		proxies[kind] = p.Addr()
	}
	loopback := network(t, &bytes.Buffer{}, "127.0.0.0/8")
	dns := network(t, &bytes.Buffer{}, "127.0.0.0/8")
	dns.Resolver = failingResolver{}
	tlsServer := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(tlsServer.Close)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	for _, tc := range []struct {
		name      string
		n         mattermost.Network
		serverURL string
		proxy     *outbound.Proxy
		within    time.Duration
		code      string
		message   string
	}{
		{name: "dns", n: dns, serverURL: "http://mattermost.invalid", code: delivery.CodeDNSFailed,
			message: "DNS lookup failed: mattermost.invalid"},
		{name: "refused", n: loopback, serverURL: "http://127.0.0.1:1", code: delivery.CodeConnectFailed},
		{name: "tls", n: loopback, serverURL: tlsServer.URL, code: delivery.CodeTLSFailed},
		{name: "timeout", n: loopback, serverURL: slow.URL, within: 100 * time.Millisecond, code: delivery.CodeTimeout,
			message: "No answer within 10 s"},
		{name: "blocked", n: network(t, &bytes.Buffer{}), serverURL: f.URL(), code: delivery.CodeBlocked},
		{name: "socks5 refused the credentials", n: loopback, serverURL: f.URL(), proxy: &outbound.Proxy{
			Type: outbound.ProxySOCKS5, Address: proxies[fakeproxy.SOCKS5], Username: "u", Password: "wrong"},
			code: delivery.CodeProxyRefused, message: mattermost.MessageProxyRefused},
		{name: "http proxy 407", n: loopback, serverURL: f.URL(), proxy: &outbound.Proxy{
			Type: outbound.ProxyHTTP, Address: proxies[fakeproxy.HTTP], Username: "u", Password: "wrong"},
			code: delivery.CodeProxyRefused, message: mattermost.MessageProxyRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.within > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.within)
				defer cancel()
			}
			c := newTokenless(t, tc.n, tc.serverURL, tc.proxy)
			p, err := mattermost.RunDryProbe(ctx, c, interactive, clock.NewManual(t0))
			if err != nil || p.OK || p.Code != tc.code || p.Message == "" ||
				(tc.message != "" && p.Message != tc.message) || strings.Contains(p.Message, "wrong") {
				t.Errorf("probe = %+v, %v", p, err)
			}
		})
	}
	c := newTokenless(t, loopback, f.URL(), nil)
	p, err := mattermost.RunDryProbe(t.Context(), c, mattermost.Direct(delivery.Call{Class: outbound.ClassHeartbeat}),
		clock.NewManual(t0))
	if err != nil || p.OK || p.Code != delivery.CodeRequestFailed || p.Message != "the Mattermost client has no "+
		"heartbeat class" {
		t.Errorf("a class the client has not = %+v, %v", p, err)
	}
	if reqs := f.Requests(); len(reqs) != 0 {
		t.Errorf("requests %+v", reqs)
	}
}

// TestDryProbeProxyDNS covers C-13.FR-2: through a proxy whose name does not resolve, HTTP or SOCKS5, the dry probe is
// dns_failed with the error's own text, which names the proxy as the name that did not resolve and never the server,
// whose name Muster leaves to the proxy. Nothing reaches the server.
func TestDryProbeProxyDNS(t *testing.T) {
	f := startFake(t)
	n := network(t, &bytes.Buffer{}, "127.0.0.0/8")
	n.Resolver = failingResolver{}
	server, err := url.Parse(f.URL())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []outbound.ProxyType{outbound.ProxyHTTP, outbound.ProxySOCKS5} {
		t.Run(string(kind), func(t *testing.T) {
			c := newTokenless(t, n, f.URL(), &outbound.Proxy{Type: kind, Address: "proxy.invalid:3128"})
			p, err := mattermost.RunDryProbe(t.Context(), c, interactive, clock.NewManual(t0))
			if err != nil || p.OK || p.Code != delivery.CodeDNSFailed ||
				!strings.Contains(p.Message, "lookup proxy.invalid") ||
				strings.Contains(p.Message, "DNS lookup failed: "+server.Hostname()) {
				t.Errorf("probe = %+v, %v", p, err)
			}
		})
	}
	if reqs := f.Requests(); len(reqs) != 0 {
		t.Errorf("requests %+v", reqs)
	}
}

// TestDryProbeNeverWaits: a 429 to the dry probe reaches the runner as Transient, without a delay or a scope, so that a
// request without a token never holds the limiter of the Connection whose limiter token the check took; Mattermost's
// 429 is plain text (F-031), which is not a Mattermost server's answer to the probe.
func TestDryProbeNeverWaits(t *testing.T) {
	f := startFake(t)
	c := newTokenless(t, network(t, &bytes.Buffer{}, "127.0.0.0/8"), f.URL(), nil)
	fault(t, f, fakeserver.Fault{Path: mattermost.DryProbePath, Status: http.StatusTooManyRequests,
		RetryAfterSeconds: 30, ContentType: "text/plain", Body: "limit exceeded"})
	var got delivery.Outcome
	run := func(ctx context.Context, fn func(context.Context, delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		got = fn(ctx, delivery.Call{Class: outbound.ClassInteractive})
		return got, nil
	}
	p, err := mattermost.RunDryProbe(t.Context(), c, run, clock.NewManual(t0))
	if err != nil || p.OK || p.Code != mattermost.CodeNotMattermost || got.Kind != delivery.OutcomeTransient ||
		got.RetryAfter != 0 || got.Scope != "" {
		t.Errorf("probe = %+v, %v, outcome %+v", p, err, got)
	}
	wantProbeRequest(t, f)
}

// TestClientWithoutToken: a client built without a token refuses every call that would carry one before any request,
// and makes only the dry probe.
func TestClientWithoutToken(t *testing.T) {
	f := startFake(t)
	c := newTokenless(t, network(t, &bytes.Buffer{}, "127.0.0.0/8"), f.URL(), nil)
	ctx, class := t.Context(), outbound.ClassInteractive
	refused := func(what string, r mattermost.Result) {
		t.Helper()
		if r.OK() || r.Status != 0 || r.Outcome.Kind != delivery.OutcomeFatal ||
			string(r.Outcome.Error) != "this client has no bot token: only the dry probe runs without one" {
			t.Errorf("%s = %+v", what, r)
		}
	}
	_, r := c.Me(ctx, class)
	refused("Me", r)
	_, r = c.Teams(ctx, class)
	refused("Teams", r)
	_, r = c.Grants(ctx, class, mattermost.User{Roles: "system_user"}, mattermost.PermissionEphemeralPosts)
	refused("Grants", r)
	res, err := mattermost.CheckDestination(ctx, c, interactive, fakemattermost.TeamID, fakemattermost.ChannelAlerts)
	if err != nil || res.OK() || len(res.Steps) != 1 || res.Steps[0].Name != mattermost.StepToken {
		t.Errorf("check = %+v, %v", res, err)
	}
	if reqs := f.Requests(); len(reqs) != 0 {
		t.Fatalf("a client without a token made requests: %+v", reqs)
	}
	if p, err := mattermost.RunDryProbe(ctx, c, interactive, clock.NewManual(t0)); err != nil || !p.OK {
		t.Errorf("probe = %+v, %v", p, err)
	}
	wantProbeRequest(t, f)
}

// TestRoute is the route of `muster dev` (C-21.FR-10): a client whose Route answers for its server URL and token sends
// every request there; an empty answer sends to the server URL, which the dry probe's message then names. A routed
// request still passes the outbound address policy: without the loopback network it is blocked.
func TestRoute(t *testing.T) {
	f := startFake(t)
	const server = "https://chat.example.org"
	route := func(serverURL string, tok logging.Secret) string {
		if serverURL == server && tok == token {
			return f.URL()
		}
		return ""
	}
	settings := mattermost.Settings{ServerURL: server, Token: token, Route: route}
	c, err := mattermost.NewClient(network(t, &bytes.Buffer{}, "127.0.0.0/8"), settings)
	if err != nil {
		t.Fatal(err)
	}
	if u, r := c.Me(t.Context(), outbound.ClassInteractive); !r.OK() || u.ID != fakemattermost.BotUserID {
		t.Fatalf("routed Me = %+v, %+v", u, r)
	}
	if reqs := f.Requests(); len(reqs) != 1 || reqs[0].Path != "/api/v4/users/me" {
		t.Fatalf("requests %+v", reqs)
	}

	// Not routed: another token goes to the server URL, here an address that refuses connections, and the dry probe
	// names it.
	other, err := mattermost.NewClient(network(t, &bytes.Buffer{}, "127.0.0.0/8"),
		mattermost.Settings{ServerURL: "http://127.0.0.1:1", Token: "another-token", Route: route})
	if err != nil {
		t.Fatal(err)
	}
	p, err := mattermost.RunDryProbe(t.Context(), other, interactive, clock.Real{})
	if err != nil || p.OK || !strings.Contains(p.Message, "127.0.0.1:1") {
		t.Fatalf("unrouted dry probe = %+v, %v", p, err)
	}
	if n := len(f.Requests()); n != 1 {
		t.Fatalf("the fake got %d requests, want 1", n)
	}
	blocked, err := mattermost.NewClient(network(t, &bytes.Buffer{}), settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, r := blocked.Me(t.Context(), outbound.ClassInteractive); !r.Blocked ||
		r.Outcome.Kind != delivery.OutcomeFatal {
		t.Fatalf("routed Me without loopback = %+v", r)
	}
	if n := len(f.Requests()); n != 1 {
		t.Fatalf("a blocked request reached the fake: %d requests", n)
	}
}
