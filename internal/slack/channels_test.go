// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/outbound"
)

// TestValidChannelID covers C-24.FR-3: a channel id is C… or G… in upper-case letters and digits, as entered; a direct
// chat's D…, a user's U…, a #name, lower case and white space are not.
func TestValidChannelID(t *testing.T) {
	for id, want := range map[string]bool{
		"C0123456789": true, "C0FAKEALERTS": true, "G0FAKESECRET": true, "CAB": true,
		"C" + strings.Repeat("A", 63): true, "C" + strings.Repeat("A", 64): false,
		"D0FAKEDM": false, "U0MUSTERUSR1": false, "#alerts": false, "c0123456789": false, "C0123abc": false,
		" C0123456789": false, "C0123456789 ": false, "C1": false, "C": false, "": false, "W0123456789": false,
	} {
		if ValidChannelID(id) != want {
			t.Errorf("ValidChannelID(%q) != %v", id, want)
		}
	}
}

// TestCheckChannel covers C-24.FR-3 and AC-4 against the fake Slack server, a case per answer of conversations.info: a
// channel with the bot passes with its name; one without the bot is bot_not_in_channel with the bot's handle, or
// without a name when there is none; an archived one channel_archived; an unknown id, a direct chat's and a private
// channel without the bot channel_not_found; a revoked token token_invalid; a token without channels:read
// missing_scope, which names Slack's needed in its message and in MissingScopes (D396). Every failure is Fatal for the
// Broken probe, with the message as its error, and the request names the channel, form-encoded, in the caller's class.
func TestCheckChannel(t *testing.T) {
	f := startFake(t)
	cfg := fakeslack.DefaultConfig()
	cfg.Channels[fakeslack.AppB]["C0FAKEARCH"] = fakeslack.Channel{Name: "old", Member: true, Archived: true}
	cfg.Channels[fakeslack.AppB]["C0FAKEPRIV"] = fakeslack.Channel{Name: "private-ops", Member: true, Private: true}
	cfg.Channels[fakeslack.AppB]["G0FAKESECRET"] = fakeslack.Channel{Name: "secret", Private: true}
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, f, true)
	rec := &recording{}
	for _, tc := range []struct {
		name, channel, bot string
		want               ChannelCheck
	}{
		{"a member", fakeslack.ChannelAlerts, "muster-dev-bot-b", ChannelCheck{Name: "alerts"}},
		{"a private channel", "C0FAKEPRIV", "muster-dev-bot-b", ChannelCheck{Name: "private-ops"}},
		{"not a member", fakeslack.ChannelOther, "muster-dev-bot-b", ChannelCheck{Code: CodeBotNotInChannel,
			Message: "Invite the bot to the channel: /invite @muster-dev-bot-b"}},
		{"no bot's name", fakeslack.ChannelOther, "", ChannelCheck{Code: CodeBotNotInChannel,
			Message: "Invite the bot to the channel."}},
		{"archived", "C0FAKEARCH", "muster-dev-bot-b", ChannelCheck{Code: CodeChannelArchived,
			Message: "The channel is archived."}},
		{"unknown", "C0FAKEGONE", "b", ChannelCheck{Code: CodeChannelNotFound,
			Message: "The channel was not found, or the bot cannot see it."}},
		{"a direct chat", "D0FAKEDM", "b", ChannelCheck{Code: CodeChannelNotFound, Message: MessageChannelNotFound}},
		{"private without the bot", "G0FAKESECRET", "b", ChannelCheck{Code: CodeChannelNotFound,
			Message: MessageChannelNotFound}},
	} {
		f.ResetRequests()
		got, err := CheckChannel(t.Context(), c, rec.run, tc.channel, tc.bot)
		if err != nil || got.Name != tc.want.Name || got.Code != tc.want.Code || got.Message != tc.want.Message ||
			got.MissingScopes != nil || got.OK() != (tc.want.Code == "") {
			t.Errorf("%s = %+v, %v", tc.name, got, err)
		}
		if !got.OK() && (got.Outcome.Kind != delivery.OutcomeFatal || string(got.Outcome.Error) != tc.want.Message) {
			t.Errorf("%s: outcome %+v", tc.name, got.Outcome)
		}
		reqs := f.Requests()
		if len(reqs) != 1 || reqs[0].Path != "/api/conversations.info" ||
			reqs[0].Headers["Authorization"][0] != "Bearer "+fakeslack.SecondBotToken ||
			reqs[0].Body != (url.Values{"channel": {tc.channel}}).Encode() {
			t.Errorf("%s: requests %+v", tc.name, reqs)
		}
	}
	if len(rec.outcomes) != 8 || rec.outcomes[0].Kind != delivery.OutcomeOK {
		t.Errorf("outcomes %+v", rec.outcomes)
	}

	if err := f.SetConfig(fakeslack.Config{Scopes: map[string]string{fakeslack.AppB: "chat:write,groups:read"}}); err != nil {
		t.Fatal(err)
	}
	got, err := CheckChannel(t.Context(), c, direct, fakeslack.ChannelAlerts, "b")
	if err != nil || got.Code != CodeMissingScope || !slices.Equal(got.MissingScopes, []string{"channels:read"}) ||
		got.Message != "The bot token lacks the scopes channels:read." || got.Message != MissingScopesMessage(got.MissingScopes) ||
		got.Outcome.Kind != delivery.OutcomeFatal {
		t.Errorf("missing scope = %+v, %v", got, err)
	}
	if err := f.SetConfig(fakeslack.Config{Revoked: []string{fakeslack.SecondBotToken}}); err != nil {
		t.Fatal(err)
	}
	if got, err := CheckChannel(t.Context(), c, direct, fakeslack.ChannelAlerts, "b"); err != nil ||
		got.Code != CodeTokenInvalid || got.Message != MessageTokenInvalid || got.Outcome.Kind != delivery.OutcomeFatal {
		t.Errorf("revoked = %+v, %v", got, err)
	}
	if err := f.SetConfig(fakeslack.Config{UserTokens: []string{fakeslack.SecondBotToken}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := CheckChannel(t.Context(), c, direct, fakeslack.ChannelAlerts, "b"); got.Code != CodeMissingScope {
		t.Errorf("a person's token = %+v", got)
	}
}

// TestCheckChannelAnswers covers C-24.FR-3 and FR-8: any other answer is request_failed with Slack's masked error — a
// 429, which goes back to the runner as a RetryAfter for the Connection, fatal_error, not_allowed_token_type, an HTML
// 500 and a 200 that is not the expected JSON — and a missing_scope without needed names no scope.
func TestCheckChannelAnswers(t *testing.T) {
	f := startFake(t)
	c := fakeClient(t, f, false)
	for _, tc := range []struct {
		status, retryAfter int
		body, code, text   string
		kind               delivery.OutcomeKind
	}{
		{http.StatusTooManyRequests, 30, `{"ok":false,"error":"ratelimited"}`, delivery.CodeRequestFailed, "ratelimited",
			delivery.OutcomeRetryAfter},
		{http.StatusOK, 0, `{"ok":false,"error":"fatal_error"}`, delivery.CodeRequestFailed, "fatal_error",
			delivery.OutcomeTransient},
		{http.StatusOK, 0, `{"ok":false,"error":"not_allowed_token_type"}`, delivery.CodeRequestFailed,
			"not_allowed_token_type", delivery.OutcomeFatal},
		{http.StatusInternalServerError, 0, `<html>down</html>`, delivery.CodeRequestFailed, "answered 500",
			delivery.OutcomeTransient},
		{http.StatusOK, 0, `{"ok":true}`, delivery.CodeRequestFailed,
			"conversations.info: the answer is not the expected JSON", delivery.OutcomeUnknown},
		{http.StatusOK, 0, `{"ok":false,"error":"missing_scope"}`, CodeMissingScope,
			"The bot token lacks scopes that Muster needs.", delivery.OutcomeFatal},
		{http.StatusOK, 0, `{"ok":false,"error":"channel_not_found","warning":"` + fakeslack.BotToken + `"}`,
			CodeChannelNotFound, MessageChannelNotFound, delivery.OutcomeFatal},
	} {
		if err := f.SetFault(fakeserver.Fault{Path: "/api/conversations.info", Status: tc.status,
			RetryAfterSeconds: tc.retryAfter, Body: tc.body, Times: 1}); err != nil {
			t.Fatal(err)
		}
		rec := &recording{}
		got, err := CheckChannel(t.Context(), c, rec.run, fakeslack.ChannelAlerts, "muster-dev-bot")
		if err != nil || got.Code != tc.code || !strings.Contains(got.Message, tc.text) || got.Outcome.Kind != tc.kind ||
			string(got.Outcome.Error) != got.Message || len(rec.outcomes) != 1 || rec.outcomes[0].Kind != tc.kind ||
			got.MissingScopes != nil || strings.Contains(got.Message, fakeslack.BotToken) {
			t.Errorf("%s = %+v, %v, outcomes %+v", tc.body, got, err, rec.outcomes)
		}
		if tc.kind == delivery.OutcomeRetryAfter && (rec.outcomes[0].Scope != delivery.ScopeConnection ||
			rec.outcomes[0].RetryAfter != 30*time.Second) {
			t.Errorf("429: outcome %+v", rec.outcomes[0])
		}
	}
}

// TestCheckChannelWithoutAnswer covers C-24.FR-3: a request without an answer has the code of what failed with the text
// of delivery.FailureMessage — the name of Slack's address, which a proxy leaves out, the time — and a blocked address
// is blocked; none of them shows the bot token.
func TestCheckChannelWithoutAnswer(t *testing.T) {
	n := network(t, nil)
	n.Resolver = &resolver{}
	got, err := CheckChannel(t.Context(), newClient(t, n, Settings{BotToken: testBot}), direct, "C0123", "bot")
	if err != nil || got.Code != delivery.CodeDNSFailed || got.Message != "DNS lookup failed: slack.com" ||
		got.Outcome.Kind != delivery.OutcomeTransient {
		t.Fatalf("dns = %+v, %v", got, err)
	}
	f := startFake(t)
	expired, cancel := context.WithTimeout(t.Context(), 0)
	defer cancel()
	if got, _ := CheckChannel(expired, fakeClient(t, f, false), direct, "C0123", "bot"); got.Code !=
		delivery.CodeTimeout || got.Message != "No answer within 10 s" {
		t.Fatalf("timeout = %+v", got)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String() + "/api/"
	_ = ln.Close()
	got, _ = CheckChannel(t.Context(), newClient(t, network(t, nil), settings(closed)), direct, "C0123", "bot")
	if got.Code != delivery.CodeConnectFailed || got.Message == "" || strings.Contains(got.Message, testBot) {
		t.Fatalf("connect = %+v", got)
	}
	strict, err := outbound.ParsePolicy("standard", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked := network(t, nil)
	blocked.Policy = outbound.StaticPolicy(strict)
	got, _ = CheckChannel(t.Context(), newClient(t, blocked, settings(f.URL()+"/api/")), direct, "C0123", "bot")
	if got.Code != delivery.CodeBlocked || !strings.Contains(got.Message, "blocked") ||
		got.Outcome.Kind != delivery.OutcomeFatal {
		t.Fatalf("blocked = %+v", got)
	}
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0",
		fakeproxy.Options{Username: "u", Password: "proxy-pw-right"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	st := Settings{BotToken: fakeslack.BotToken, Route: devRoute(f), Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP,
		Address: proxy.Addr(), Username: "u", Password: "proxy-pw-wrong"}}
	got, _ = CheckChannel(t.Context(), newClient(t, network(t, nil), st), direct, "C0123", "bot")
	if got.Code != delivery.CodeProxyRefused || got.Message != MessageProxyRefused {
		t.Fatalf("proxy refused = %+v", got)
	}
}

// TestCheckChannelRunnerError: the runner's own error, such as no limiter token within the budget, ends the check.
func TestCheckChannelRunnerError(t *testing.T) {
	f := startFake(t)
	limited := &delivery.LimitedError{RetryAfter: time.Second}
	run := func(context.Context, func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		return delivery.Outcome{}, limited
	}
	if got, err := CheckChannel(t.Context(), fakeClient(t, f, false), run, fakeslack.ChannelAlerts, "b"); !errors.Is(err,
		limited) || got.Code != "" || len(f.Requests()) != 0 {
		t.Fatalf("limited = %+v, %v", got, err)
	}
}

// pageRunner runs each page of the channel list at once in the interactive class and calls before, when it is set,
// with the number of the page, from 1, before it is read.
type pageRunner struct {
	mu       sync.Mutex
	pages    int
	before   func(page int)
	outcomes []delivery.Outcome
}

func (p *pageRunner) run(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (
	delivery.Outcome, error) {
	p.mu.Lock()
	p.pages++
	page := p.pages
	p.mu.Unlock()
	if p.before != nil {
		p.before(page)
	}
	o := f(ctx, delivery.Call{Class: outbound.ClassInteractive})
	p.mu.Lock()
	p.outcomes = append(p.outcomes, o)
	p.mu.Unlock()
	return o, nil
}

// TestChannels covers C-24.FR-3 and AC-4: the list reads conversations.list with types=public_channel,private_channel,
// exclude_archived=true and limit=200, form-encoded, page by page with the cursor of the previous page; it keeps the
// channels the bot is a member of, by name, filtered by q regardless of case, and is not truncated when the pages
// ended.
func TestChannels(t *testing.T) {
	f := startFake(t)
	cfg := fakeslack.DefaultConfig()
	b := cfg.Channels[fakeslack.AppB]
	b["C0FAKEPRIV"] = fakeslack.Channel{Name: "private-ops", Member: true, Private: true}
	b["C0FAKEARCH"] = fakeslack.Channel{Name: "old", Member: true, Archived: true}
	b["C0FAKEBETA"] = fakeslack.Channel{Name: "Beta-Alerts", Member: true}
	b["G0FAKESECRET"] = fakeslack.Channel{Name: "secret", Private: true}
	cfg.PageSize = 2
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, f, true)
	run := &pageRunner{}
	got, err := Channels(t.Context(), c, run.run, "")
	want := []ListedChannel{{ID: "C0FAKEBETA", Name: "Beta-Alerts"}, {ID: fakeslack.ChannelAlerts, Name: "alerts"},
		{ID: "C0FAKEPRIV", Name: "private-ops", Private: true}}
	if err != nil || !slices.Equal(got.Channels, want) || got.Truncated {
		t.Fatalf("channels = %+v, %v", got, err)
	}
	reqs := f.Requests()
	if len(reqs) != 2 || run.pages != 2 {
		t.Fatalf("%d requests, %d pages", len(reqs), run.pages)
	}
	for i, r := range reqs {
		form, err := url.ParseQuery(r.Body)
		if err != nil || r.Path != "/api/conversations.list" || form.Get("types") != "public_channel,private_channel" ||
			form.Get("exclude_archived") != "true" || form.Get("limit") != "200" || (i == 0) != (form.Get("cursor") == "") ||
			r.Headers["Content-Type"][0] != "application/x-www-form-urlencoded" {
			t.Errorf("request %d = %+v", i, r)
		}
	}
	for _, tc := range []struct {
		q     string
		names []string
	}{{"ALERTS", []string{"Beta-Alerts", "alerts"}}, {" ops ", []string{"private-ops"}}, {"none", nil}} {
		got, err := Channels(t.Context(), c, direct, tc.q)
		var names []string
		for _, ch := range got.Channels {
			names = append(names, ch.Name)
		}
		if err != nil || !slices.Equal(names, tc.names) || got.Channels == nil {
			t.Errorf("q %q = %+v, %v", tc.q, got, err)
		}
	}
}

// TestChannelsTruncated covers C-24.FR-3: the list reads at most five pages and is truncated when Slack named a cursor
// after the fifth.
func TestChannelsTruncated(t *testing.T) {
	f := startFake(t)
	cfg := fakeslack.DefaultConfig()
	for _, id := range []string{"C01", "C02", "C03", "C04", "C05", "C06"} {
		cfg.Channels[fakeslack.AppA][id] = fakeslack.Channel{Name: "ch-" + id, Member: true}
	}
	cfg.PageSize = 1
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Channels(t.Context(), fakeClient(t, f, false), direct, "")
	if err != nil || !got.Truncated || len(got.Channels) != 5 || len(f.Requests()) != 5 {
		t.Fatalf("truncated = %+v, %v, %d requests", got, err, len(f.Requests()))
	}
	cfg.PageSize = 2
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	f.ResetRequests()
	if got, err := Channels(t.Context(), fakeClient(t, f, false), direct, ""); err != nil || got.Truncated ||
		len(got.Channels) != 7 || len(f.Requests()) != 4 {
		t.Fatalf("not truncated = %+v, %v, %d requests", got, err, len(f.Requests()))
	}
}

// TestChannelsRateLimited covers C-24.FR-3 and FR-8: a 429 on a page stops the list at once, without a later page, as a
// *RateLimitedError with Slack's Retry-After, whose outcome reaches the runner as a RetryAfter for the Connection; a 429
// without Retry-After names no wait.
func TestChannelsRateLimited(t *testing.T) {
	f := startFake(t)
	cfg := fakeslack.DefaultConfig()
	cfg.PageSize = 1
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, f, false)
	for _, tc := range []struct {
		retryAfter int
		want       time.Duration
		kind       delivery.OutcomeKind
	}{{30, 30 * time.Second, delivery.OutcomeRetryAfter}, {0, 0, delivery.OutcomeTransient}} {
		f.ResetRequests()
		run := &pageRunner{before: func(page int) {
			if page == 2 {
				_ = f.SetFault(fakeserver.Fault{Path: "/api/conversations.list", Status: http.StatusTooManyRequests,
					RetryAfterSeconds: tc.retryAfter, Body: `{"ok":false,"error":"ratelimited"}`, Times: 1})
			}
		}}
		got, err := Channels(t.Context(), c, run.run, "")
		rl, ok := errors.AsType[*RateLimitedError](err)
		if !ok || rl.RetryAfter != tc.want || rl.Error() == "" || got.Channels != nil || run.pages != 2 ||
			len(f.Requests()) != 2 || run.outcomes[1].Kind != tc.kind {
			t.Fatalf("429 (%d) = %+v, %v, %d pages, outcomes %+v", tc.retryAfter, got, err, run.pages, run.outcomes)
		}
		if tc.kind == delivery.OutcomeRetryAfter && run.outcomes[1].Scope != delivery.ScopeConnection {
			t.Errorf("scope %+v", run.outcomes[1])
		}
	}
}

// TestChannelsRefused covers C-24.FR-3: a page that fails otherwise is a *ListFailedError with Slack's masked error, an
// answer without channels names the method, a request without an answer names what failed, and the runner's own error
// ends the list.
func TestChannelsRefused(t *testing.T) {
	f := startFake(t)
	c := fakeClient(t, f, false)
	for body, want := range map[string]string{
		`{"ok":false,"error":"invalid_cursor","warning":"` + fakeslack.BotToken + `"}`: "invalid_cursor",
		`{"ok":true}`: "conversations.list: the answer is not the expected JSON",
	} {
		if err := f.SetFault(fakeserver.Fault{Path: "/api/conversations.list", Status: http.StatusOK, Body: body,
			Times: 1}); err != nil {
			t.Fatal(err)
		}
		_, err := Channels(t.Context(), c, direct, "")
		lf, ok := errors.AsType[*ListFailedError](err)
		if !ok || string(lf.Text) != want || !strings.Contains(lf.Error(), want) ||
			strings.Contains(err.Error(), fakeslack.BotToken) {
			t.Errorf("%s = %v", body, err)
		}
	}
	n := network(t, nil)
	n.Resolver = &resolver{}
	_, err := Channels(t.Context(), newClient(t, n, Settings{BotToken: testBot}), direct, "")
	if lf, ok := errors.AsType[*ListFailedError](err); !ok || !strings.Contains(string(lf.Text), "slack.com") {
		t.Errorf("dns = %v", err)
	}
	limited := &delivery.LimitedError{RetryAfter: time.Second}
	run := func(context.Context, func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		return delivery.Outcome{}, limited
	}
	if _, err := Channels(t.Context(), c, run, ""); !errors.Is(err, limited) {
		t.Errorf("limited = %v", err)
	}
}

// TestChannelsMasked: a channel's id and name, and the cursor, are masked of the Connection's secrets, and so is the
// bot's name in the text of bot_not_in_channel.
func TestChannelsMasked(t *testing.T) {
	var lists atomic.Int32
	s := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "conversations.info") {
			answerJSON(http.StatusOK, `{"ok":true,"channel":{"id":"C1","name":"x-`+testBot+`","is_member":false}}`)(w, r)
			return
		}
		cursor := ""
		if lists.Add(1) == 1 {
			cursor = testApp
		}
		answerJSON(http.StatusOK, `{"ok":true,"channels":[{"id":"C2","name":"`+testBot+`","is_member":true}],`+
			`"response_metadata":{"next_cursor":"`+cursor+`"}}`)(w, r)
	})
	c := newClient(t, network(t, nil), settings(s.base()))
	got, err := Channels(t.Context(), c, direct, "")
	if err != nil || len(got.Channels) != 2 || got.Channels[0].Name != "[redacted]" {
		t.Fatalf("channels = %+v, %v", got, err)
	}
	seen := s.seen()
	if len(seen) != 2 || !strings.Contains(seen[1].Body, "cursor=%5Bredacted%5D") {
		t.Errorf("requests %+v", seen)
	}
	check, err := CheckChannel(t.Context(), c, direct, "C1", "bot-"+testBot)
	if err != nil || strings.Contains(check.Message, testBot) || check.Code != CodeBotNotInChannel ||
		check.Message != "Invite the bot to the channel: /invite @bot-[redacted]" {
		t.Errorf("check = %+v, %v", check, err)
	}
}
