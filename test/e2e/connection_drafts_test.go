// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// draftTGToken is the token of the Telegram Connection that TestConnectionDrafts creates in the webhook mode, so that
// no poller sends it: every request that carries it comes from a check.
const draftTGToken = "777041:draft-token"

// draftEnv checks drafts of Connections through the API with an Admin's Personal access token.
type draftEnv struct {
	h     *Harness
	base  string
	token string
}

type draftResult struct {
	checkResult
	Info *struct {
		Messenger struct {
			Name string `json:"name"`
		} `json:"messenger"`
	} `json:"info"`
	Slack *struct {
		TeamName  string `json:"team_name"`
		BotUserID string `json:"bot_user_id"`
	} `json:"slack"`
	Run string `json:"request_log_run"`
}

// String is r as JSON for a failure message, without any token.
func (r draftResult) String() string {
	type plain draftResult
	b, _ := json.Marshal(plain(r))
	return redacted(string(b))
}

func (e *draftEnv) api(t *testing.T) patClient {
	return patClient{t: t, base: e.base, token: e.token}
}

// draft checks the draft body and returns its result, empty unless the answer is 200, and the answer.
func (e *draftEnv) draft(t *testing.T, body string) (draftResult, answer) {
	t.Helper()
	a := e.api(t).do(http.MethodPost, "/api/v1/connections/checks", body)
	var r draftResult
	if a.status == http.StatusOK {
		decode(t, a, &r)
	}
	return r, a
}

// wantSteps checks the draft body and fails t unless it answers 200 with the steps want, as steps writes them.
func (e *draftEnv) wantSteps(t *testing.T, body, want string) draftResult {
	t.Helper()
	r, a := e.draft(t, body)
	if a.status != http.StatusOK || steps(r.checkResult) != want {
		t.Fatalf("draft %s = %d %s, want the steps %s", redacted(body), a.status, redacted(string(a.body)), want)
	}
	return r
}

// id is the public_id of the Connection of typ named name.
func (e *draftEnv) id(t *testing.T, typ, name string) string {
	t.Helper()
	for _, item := range e.api(t).json(http.MethodGet, "/api/v1/connections?type="+typ+"&limit=500", "",
		http.StatusOK)["items"].([]any) {
		if c := item.(map[string]any); c["name"] == name {
			return c["id"].(string)
		}
	}
	t.Fatalf("no %s Connection %q", typ, name)
	return ""
}

// stored is what a check may store on the Connection id: its bot, its info and when it was read, and its etag.
func (e *draftEnv) stored(t *testing.T, id string) string {
	t.Helper()
	c := e.api(t).json(http.MethodGet, "/api/v1/connections/"+id, "", http.StatusOK)
	return fmt.Sprintf("bot_username=%v info=%v info_read_at=%v etag=%v", c["bot_username"], c["info"],
		c["info_read_at"], c["etag"])
}

// counts are the numbers of Connections and of Audit log entries.
func (e *draftEnv) counts(t *testing.T) (int, int) {
	t.Helper()
	api := e.api(t)
	return len(api.json(http.MethodGet, "/api/v1/connections?limit=500", "", http.StatusOK)["items"].([]any)),
		len(api.json(http.MethodGet, "/api/v1/audit-log?limit=500", "", http.StatusOK)["items"].([]any))
}

// fakeRequests are the requests the fake server at fakeURL recorded; resetFake forgets them.
func fakeRequests(t *testing.T, fakeURL string) []fakeserver.Request {
	t.Helper()
	var reqs []fakeserver.Request
	decode(t, call(t, http.MethodGet, fakeURL+"/_fake/requests", ""), &reqs)
	return reqs
}

func resetFake(t *testing.T, fakeURL string) {
	t.Helper()
	if a := call(t, http.MethodDelete, fakeURL+"/_fake/requests", ""); a.status != http.StatusNoContent {
		t.Fatalf("DELETE %s/_fake/requests = %d %s", fakeURL, a.status, a.body)
	}
}

// proxyTargets are the targets the development HTTP proxy connected to since resetProxy.
func proxyTargets(t *testing.T) []string {
	t.Helper()
	var conns []struct {
		Target string `json:"target"`
	}
	decode(t, call(t, http.MethodGet, "http://"+devmode.HTTPProxyAddr+"/_fake/requests", ""), &conns)
	targets := make([]string, 0, len(conns))
	for _, c := range conns {
		targets = append(targets, c.Target)
	}
	return targets
}

func resetProxy(t *testing.T) {
	t.Helper()
	if a := call(t, http.MethodDelete, "http://"+devmode.HTTPProxyAddr+"/_fake/requests", ""); a.status !=
		http.StatusNoContent {
		t.Fatalf("reset the HTTP proxy = %d %s", a.status, a.body)
	}
}

// carries reports whether r carries token as a whole token — in its path, query, a header or its body — and not as the
// prefix of a longer one, such as the second bot's token of the same fake.
func carries(r fakeserver.Request, token string) bool {
	re := regexp.MustCompile(regexp.QuoteMeta(token) + `([^-\w]|$)`)
	texts := []string{r.Path, r.Query, r.Body}
	for _, values := range r.Headers {
		texts = append(texts, values...)
	}
	return slices.ContainsFunc(texts, re.MatchString)
}

// knownTokens are the bot tokens of the test, the longer before the tokens they start with.
var knownTokens = []string{devmode.MattermostSecondBotToken, devmode.ConnectionBotToken,
	devmode.TelegramSecondBotToken, devmode.TelegramConnectionBotToken, devmode.BridgeSecondBotToken,
	devmode.BridgeConnectionBotToken, draftTGToken, devmode.SlackSecondBotToken, devmode.SlackSecondAppToken,
	fakeslack.SecondSigningSecret, devmode.SlackConnectionBotToken, devmode.SlackConnectionAppToken}

// redacted is s with every known token replaced, for a failure message.
func redacted(s string) string {
	for _, token := range knownTokens {
		s = strings.ReplaceAll(s, token, "[token]")
	}
	return s
}

// described is reqs as method, path and whether each has an Authorization header, without any token.
func described(reqs []fakeserver.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, fmt.Sprintf("%s %s auth=%t", r.Method, redacted(r.Path), len(r.Headers["Authorization"]) > 0))
	}
	return out
}

// TestConnectionDrafts is S-087's end-to-end check of checkConnectionDraft against `muster dev` and its fake servers,
// with further fake Telegram and bridge servers in the test process as other addresses: drafts of new Connections of
// each type store nothing (C-13.FR-14); a draft of a saved Connection with a changed address or proxy and no token makes
// only the dry probe of its type, without a token (C-13.FR-2, C-13.AC-17, C-14.AC-6, C-14.AC-20, C-22.FR-2,
// C-22.AC-26, C-24.FR-2, C-24.AC-2); a typed token goes to the draft's address; the stored token goes only to the saved
// address through the saved proxy (C-22.FR-1); a draft leaves the saved Connection as it was; its requests are
// recorded as draft_check; and the refusals of a draft.
func TestConnectionDrafts(t *testing.T) {
	h := Start(t, DevProcess)
	admin := newTG(t, h)
	e := &draftEnv{h: h, base: admin.api.base, token: admin.api.token}

	t.Run("new connections store nothing", func(t *testing.T) {
		conns, entries := e.counts(t)
		r := e.wantSteps(t, `{"type":"mattermost","server_url":"`+h.Fakes.MattermostSecond+`","bot_token":"`+
			devmode.MattermostSecondBotToken+`"}`, "token:ok")
		if !r.OK || r.BotName == nil || *r.BotName != fakemattermost.SecondBotUsername {
			t.Fatalf("Mattermost draft = %+v", r)
		}
		r = e.wantSteps(t, `{"type":"telegram","bot_api_base_url":"`+h.Fakes.Telegram+`","update_mode":"long_polling",
			"bot_token":"`+devmode.TelegramSecondBotToken+`"}`, "dry_probe:ok get_me:ok get_webhook_info:ok")
		if !r.OK || r.BotName == nil || *r.BotName != faketelegram.SecondBotUsername {
			t.Fatalf("Telegram draft = %+v", r)
		}
		r = e.wantSteps(t, `{"type":"bridge","base_url":"`+h.Fakes.Bridge+`","event_delivery":"poll","bot_token":"`+
			devmode.BridgeSecondBotToken+`"}`, "info:ok event_delivery:ok")
		if !r.OK || r.Info == nil || r.Info.Messenger.Name != fakebridge.MessengerName {
			t.Fatalf("Bridge draft = %+v", r)
		}
		e.wantSteps(t, `{"type":"mattermost","server_url":"`+h.Fakes.MattermostSecond+`"}`, "dry_probe:ok token:skip")
		e.wantSteps(t, `{"type":"bridge","base_url":"`+h.Fakes.Bridge+`","event_delivery":"poll"}`,
			"dry_probe:ok info:skip event_delivery:skip")
		r = e.wantSteps(t, `{"type":"slack","event_mode":"socket","bot_token":"`+devmode.SlackSecondBotToken+
			`","app_token":"`+devmode.SlackSecondAppToken+`","proxy":{"enabled":false}}`,
			"token:ok scopes:ok app_token:ok")
		b := fakeslack.Apps()[1]
		if !r.OK || r.BotName == nil || *r.BotName != b.BotName || r.Slack == nil || r.Slack.TeamName != b.TeamName ||
			r.Slack.BotUserID != b.BotUserID {
			t.Fatalf("Slack draft = %+v", r)
		}
		e.wantSteps(t, `{"type":"slack","event_mode":"http","bot_token":"`+devmode.SlackSecondBotToken+`"}`,
			"token:ok scopes:ok")
		if c, a := e.counts(t); c != conns || a != entries {
			t.Fatalf("after the drafts %d Connections and %d Audit log entries, before %d and %d", c, a, conns, entries)
		}
	})

	t.Run("mattermost", func(t *testing.T) {
		id := e.id(t, "mattermost", devmode.ConnectionName)
		before := e.stored(t, id)
		second := h.Fakes.MattermostSecond

		resetFake(t, second)
		r := e.wantSteps(t, `{"type":"mattermost","connection_id":"`+id+`","server_url":"`+second+`"}`,
			"dry_probe:ok token:skip")
		if !r.OK || r.Steps[0].Via != "direct" {
			t.Fatalf("changed server URL = %+v", r)
		}
		if got := described(fakeRequests(t, second)); !slices.Equal(got,
			[]string{"GET /api/v4/system/ping auth=false"}) {
			t.Fatalf("requests to the changed server URL %v", got)
		}

		resetFake(t, second)
		r = e.wantSteps(t, `{"type":"mattermost","connection_id":"`+id+`","server_url":"`+second+`","bot_token":"`+
			devmode.MattermostSecondBotToken+`"}`, "token:ok")
		if r.BotName == nil || *r.BotName != fakemattermost.SecondBotUsername {
			t.Fatalf("typed token = %+v", r)
		}
		reqs := fakeRequests(t, second)
		for _, q := range reqs {
			if !slices.Equal(q.Headers["Authorization"], []string{"Bearer " + devmode.MattermostSecondBotToken}) {
				t.Fatalf("a request of the typed token without it: %v", described([]fakeserver.Request{q}))
			}
		}
		if len(reqs) == 0 {
			t.Fatal("the typed token reached no request")
		}

		// The saved server URL with the typed token of the second bot, then with the stored token.
		r = e.wantSteps(t, `{"type":"mattermost","connection_id":"`+id+`","server_url":"`+h.Fakes.Mattermost+
			`","bot_token":"`+devmode.MattermostSecondBotToken+`"}`, "token:ok")
		if r.BotName == nil || *r.BotName != fakemattermost.SecondBotUsername {
			t.Fatalf("typed token at the saved server URL = %+v", r)
		}
		r = e.wantSteps(t, `{"type":"mattermost","connection_id":"`+id+`","server_url":"`+h.Fakes.Mattermost+`"}`,
			"token:ok")
		if r.BotName == nil || *r.BotName != fakemattermost.BotUsername {
			t.Fatalf("stored token at the saved server URL = %+v", r)
		}

		// Only the proxy changed: the dry probe through the proxy, and no token.
		resetProxy(t)
		r = e.wantSteps(t, `{"type":"mattermost","connection_id":"`+id+`","server_url":"`+h.Fakes.Mattermost+
			`","proxy":{"enabled":true,"type":"http","address":"`+devmode.HTTPProxyAddr+`"}}`, "dry_probe:ok token:skip")
		if r.Steps[0].Via != "proxy" {
			t.Fatalf("changed proxy = %+v", r)
		}
		if got := proxyTargets(t); len(got) == 0 || slices.ContainsFunc(got, func(s string) bool {
			return s != devmode.MattermostAddr
		}) {
			t.Fatalf("targets of the proxy %v", got)
		}

		for _, q := range fakeRequests(t, second) {
			if carries(q, devmode.ConnectionBotToken) {
				t.Fatalf("the stored token reached the second server: %v", described([]fakeserver.Request{q}))
			}
		}
		if after := e.stored(t, id); after != before {
			t.Fatalf("the drafts changed the saved Connection: %s, before %s", after, before)
		}
	})

	t.Run("telegram", func(t *testing.T) {
		created := e.api(t).json(http.MethodPost, "/api/v1/connections", `{"type":"telegram","name":"draft-tg",
			"bot_token":"`+draftTGToken+`","bot_api_base_url":"`+h.Fakes.Telegram+`","update_mode":"webhook",
			"proxy":{"enabled":false},"limiter":{"limit":15,"per_seconds":1}}`, http.StatusCreated)
		id := created["id"].(string)
		before := e.stored(t, id)
		other := faketelegram.New()
		if err := other.Start(t.Context(), "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = other.Close(context.WithoutCancel(t.Context())) })
		body := func(base, extra string) string {
			return `{"type":"telegram","connection_id":"` + id + `","bot_api_base_url":"` + base +
				`","update_mode":"webhook"` + extra + `}`
		}
		// the requests of the dev fake that a check made, without those of the poller of "Example Telegram"
		checked := func() []fakeserver.Request {
			return slices.DeleteFunc(fakeRequests(t, h.Fakes.Telegram), func(q fakeserver.Request) bool {
				return carries(q, devmode.TelegramConnectionBotToken)
			})
		}

		// Another address, no token: only the dry probe there.
		r := e.wantSteps(t, body(other.URL(), ""), "dry_probe:ok get_me:skip get_webhook_info:skip")
		if got := described(other.Requests()); !slices.Equal(got, []string{"GET /bot0:x/getMe auth=false"}) {
			t.Fatalf("requests to another address %v", got)
		}
		if !r.OK {
			t.Fatalf("another address = %+v", r)
		}

		// Another path prefix of the saved fake, no token: the dry probe fails there and nothing else is sent.
		resetFake(t, h.Fakes.Telegram)
		e.wantSteps(t, body(h.Fakes.Telegram+"/other", ""), "dry_probe:fail get_me:skip get_webhook_info:skip")
		if got := described(checked()); !slices.Equal(got, []string{"GET /other/bot0:x/getMe auth=false"}) {
			t.Fatalf("requests under another prefix %v", got)
		}

		// Only the proxy changed, no token: the dry probe through the proxy, and no token.
		resetFake(t, h.Fakes.Telegram)
		resetProxy(t)
		r = e.wantSteps(t, body(h.Fakes.Telegram, `,"proxy":{"enabled":true,"type":"http","address":"`+
			devmode.HTTPProxyAddr+`"}`), "dry_probe:ok get_me:skip get_webhook_info:skip")
		if r.Steps[0].Via != "proxy" {
			t.Fatalf("changed proxy = %+v", r)
		}
		if got := proxyTargets(t); len(got) == 0 || slices.ContainsFunc(got, func(s string) bool {
			return s != devmode.TelegramAddr
		}) {
			t.Fatalf("targets of the proxy %v", got)
		}
		if got := described(checked()); !slices.Equal(got, []string{"GET /bot0:x/getMe auth=false"}) {
			t.Fatalf("requests through the changed proxy %v", got)
		}

		// Another address with a typed token: the three steps there with that token.
		r = e.wantSteps(t, body(other.URL(), `,"bot_token":"`+devmode.TelegramSecondBotToken+`"`),
			"dry_probe:ok get_me:ok get_webhook_info:ok")
		if r.BotName == nil || *r.BotName != faketelegram.SecondBotUsername {
			t.Fatalf("typed token = %+v", r)
		}
		var typed []string
		for _, q := range other.Requests() {
			if carries(q, devmode.TelegramSecondBotToken) {
				typed = append(typed, redacted(strings.TrimPrefix(q.Path, "/bot"+devmode.TelegramSecondBotToken)))
			}
		}
		if !slices.Equal(typed, []string{"/getMe", "/getWebhookInfo"}) {
			t.Fatalf("requests of the typed token %v", typed)
		}

		// The saved address and proxy: the stored token.
		resetFake(t, h.Fakes.Telegram)
		r = e.wantSteps(t, body(h.Fakes.Telegram, ""), "dry_probe:ok get_me:ok get_webhook_info:ok")
		if r.BotName == nil || *r.BotName != faketelegram.BotUsername {
			t.Fatalf("stored token = %+v", r)
		}
		var stored int
		for _, q := range checked() {
			if carries(q, draftTGToken) {
				stored++
			}
		}
		if stored != 2 {
			t.Fatalf("%d requests of the stored token at the saved address, want getMe and getWebhookInfo", stored)
		}

		for _, q := range other.Requests() {
			if carries(q, draftTGToken) {
				t.Fatalf("the stored token reached another address: %v", described([]fakeserver.Request{q}))
			}
		}
		if after := e.stored(t, id); after != before {
			t.Fatalf("the drafts changed the saved Connection: %s, before %s", after, before)
		}
	})

	t.Run("bridge", func(t *testing.T) {
		id := e.id(t, "bridge", devmode.BridgeConnectionName)
		if a := e.api(t).do(http.MethodPost, "/api/v1/connections/"+id+"/checks", "{}"); a.status != http.StatusOK ||
			!strings.Contains(string(a.body), `"ok":true`) {
			t.Fatalf("check of the saved Bridge = %d %s", a.status, redacted(string(a.body)))
		}
		// A draft that stored info would store it at a later time.
		advance(t, h.Replicas[0], 60)
		before := e.stored(t, id)
		other := fakebridge.New()
		if err := other.Start(t.Context(), "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = other.Close(context.WithoutCancel(t.Context())) })
		body := func(base, extra string) string {
			return `{"type":"bridge","connection_id":"` + id + `","base_url":"` + base + `","event_delivery":"poll"` +
				extra + `}`
		}

		// The address of the fake Mattermost server, which is not a bridge.
		resetFake(t, h.Fakes.Mattermost)
		r := e.wantSteps(t, body(h.Fakes.Mattermost, ""), "dry_probe:fail info:skip event_delivery:skip")
		if r.Steps[0].Code != "not_bridge" {
			t.Fatalf("not a bridge = %+v", r)
		}
		if got := described(fakeRequests(t, h.Fakes.Mattermost)); !slices.Equal(got,
			[]string{"GET /v1/info auth=false"}) {
			t.Fatalf("requests to the address that is not a bridge %v", got)
		}

		// An address that answers 200 with an HTML page, as a web server does.
		page := h.Fakes.MattermostSecond
		resetFake(t, page)
		if a := call(t, http.MethodPost, page+"/_fake/faults", `{"path":"/v1/info","status":200,`+
			`"content_type":"text/html","body":"<html></html>","times":1}`); a.status != http.StatusNoContent {
			t.Fatalf("POST /_fake/faults = %d %s", a.status, a.body)
		}
		r = e.wantSteps(t, body(page, ""), "dry_probe:fail info:skip event_delivery:skip")
		if r.Steps[0].Code != "not_bridge" || !strings.Contains(r.Steps[0].Message, "text/html") {
			t.Fatalf("an HTML page = %+v", r)
		}
		if got := described(fakeRequests(t, page)); !slices.Equal(got, []string{"GET /v1/info auth=false"}) {
			t.Fatalf("requests to the HTML page %v", got)
		}

		// Another bridge, no token: only the dry probe there.
		r = e.wantSteps(t, body(other.URL(), ""), "dry_probe:ok info:skip event_delivery:skip")
		if r.Info != nil || !r.OK {
			t.Fatalf("another bridge = %+v", r)
		}
		if got := described(other.Requests()); !slices.Equal(got, []string{"GET /v1/info auth=false"}) {
			t.Fatalf("requests to another bridge %v", got)
		}

		// Another bridge with a typed token, then the saved address with the stored token: info returned.
		r = e.wantSteps(t, body(other.URL(), `,"bot_token":"`+devmode.BridgeSecondBotToken+`"`),
			"info:ok event_delivery:ok")
		if r.Info == nil || r.Info.Messenger.Name != fakebridge.MessengerName {
			t.Fatalf("typed token = %+v", r)
		}
		r = e.wantSteps(t, body(h.Fakes.Bridge, ""), "info:ok event_delivery:ok")
		if r.Info == nil || r.Info.Messenger.Name != fakebridge.MessengerName {
			t.Fatalf("stored token = %+v", r)
		}

		for _, reqs := range [][]fakeserver.Request{other.Requests(), fakeRequests(t, h.Fakes.Mattermost),
			fakeRequests(t, page)} {
			for _, q := range reqs {
				if carries(q, devmode.BridgeConnectionBotToken) {
					t.Fatalf("the stored token reached another address: %v", described([]fakeserver.Request{q}))
				}
			}
		}
		if after := e.stored(t, id); after != before {
			t.Fatalf("the drafts changed the saved Connection: %s, before %s", after, before)
		}
	})

	t.Run("slack", func(t *testing.T) {
		fs := h.Fakes.Slack
		created := e.api(t).json(http.MethodPost, "/api/v1/connections", `{"type":"slack","name":"draft-sl",
			"event_mode":"http","bot_token":"`+devmode.SlackSecondBotToken+`","app_token":"`+
			devmode.SlackSecondAppToken+`","slack_signing_secret":"`+fakeslack.SecondSigningSecret+`",
			"proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":60}}`, http.StatusCreated)
		id := created["id"].(string)
		before := e.stored(t, id)
		body := func(mode, extra string) string {
			return `{"type":"slack","connection_id":"` + id + `","event_mode":"` + mode + `"` + extra + `}`
		}
		proxy := `,"proxy":{"enabled":true,"type":"http","address":"` + devmode.HTTPProxyAddr + `"}`
		bot := `,"bot_token":"` + devmode.SlackSecondBotToken + `"`
		app := `,"app_token":"` + devmode.SlackSecondAppToken + `"`
		// authTests are the requests to auth.test since the fake was reset; "Example Slack" may reconnect its socket
		// meanwhile, with apps.connections.open alone.
		authTests := func() []fakeserver.Request {
			return slices.DeleteFunc(fakeRequests(t, fs), func(q fakeserver.Request) bool {
				return q.Path != "/api/auth.test"
			})
		}
		// carrying are the requests since the fake was reset that carry token.
		carrying := func(token string) []fakeserver.Request {
			return slices.DeleteFunc(fakeRequests(t, fs), func(q fakeserver.Request) bool { return !carries(q, token) })
		}
		viaAll := func(r draftResult, via string) bool {
			return !slices.ContainsFunc(r.Steps, func(s checkStep) bool { return s.Via != via })
		}

		// Only the proxy changed, no token: the dry probe through the proxy, without Authorization.
		resetFake(t, fs)
		resetProxy(t)
		r := e.wantSteps(t, body("socket", proxy), "dry_probe:ok token:skip scopes:skip app_token:skip")
		if !r.OK || !viaAll(r, "proxy") || r.Slack != nil {
			t.Fatalf("changed proxy = %+v", r)
		}
		if got := described(authTests()); !slices.Equal(got, []string{"POST /api/auth.test auth=false"}) {
			t.Fatalf("requests of the dry probe %v", got)
		}
		if got := proxyTargets(t); len(got) == 0 || slices.ContainsFunc(got, func(s string) bool {
			return s != devmode.SlackAddr
		}) {
			t.Fatalf("targets of the proxy %v", got)
		}
		// The proxy connected only to the fake, so every request through it is among the fake's: none, on any path,
		// carries a stored token.
		for _, token := range []string{devmode.SlackSecondBotToken, devmode.SlackSecondAppToken} {
			if got := carrying(token); len(got) != 0 {
				t.Fatalf("a stored token reached the fake through the dry probe: %v", described(got))
			}
		}

		// The tokens typed again: the steps through the new proxy with them.
		resetProxy(t)
		r = e.wantSteps(t, body("socket", proxy+bot+app), "token:ok scopes:ok app_token:ok")
		if !viaAll(r, "proxy") || r.BotName == nil || *r.BotName != fakeslack.Apps()[1].BotName {
			t.Fatalf("typed tokens = %+v", r)
		}
		if got := proxyTargets(t); len(got) == 0 {
			t.Fatal("the typed tokens did not go through the proxy")
		}

		// The bot token typed alone through the new proxy: the stored app-level token stays.
		resetFake(t, fs)
		e.wantSteps(t, body("socket", proxy+bot), "token:ok scopes:ok app_token:skip")
		if got := carrying(devmode.SlackSecondAppToken); len(got) != 0 {
			t.Fatalf("the stored app-level token went through a new proxy: %v", described(got))
		}
		if got := carrying(devmode.SlackSecondBotToken); len(got) != 1 {
			t.Fatalf("requests of the typed bot token %v", described(got))
		}

		// The saved route: the stored tokens, in either mode.
		e.wantSteps(t, body("socket", ""), "token:ok scopes:ok app_token:ok")
		e.wantSteps(t, body("http", ""), "token:ok scopes:ok")

		// Answers to the dry probe that are not Slack's not_authed, and a token Slack refuses.
		for _, c := range []struct{ fault, steps, code string }{
			{`{"path":"/api/auth.test","status":200,"content_type":"text/html","body":"<html></html>","times":1}`,
				"dry_probe:fail token:skip scopes:skip", "not_slack"},
			{`{"path":"/api/auth.test","status":200,"body":"{\"ok\":true}","times":1}`,
				"dry_probe:fail token:skip scopes:skip", "not_slack"},
			{`{"path":"/api/auth.test","status":503,"body":"{\"ok\":false,\"error\":\"not_authed\"}","times":1}`,
				"dry_probe:fail token:skip scopes:skip", "unexpected_status"},
		} {
			if a := call(t, http.MethodPost, fs+"/_fake/faults", c.fault); a.status != http.StatusNoContent {
				t.Fatalf("POST /_fake/faults = %d %s", a.status, a.body)
			}
			if r := e.wantSteps(t, body("http", proxy), c.steps); r.Steps[0].Code != c.code {
				t.Fatalf("fault %s = %+v", c.fault, r)
			}
		}
		if a := call(t, http.MethodPost, fs+"/_fake/faults", `{"path":"/api/auth.test","status":200,`+
			`"body":"{\"ok\":false,\"error\":\"invalid_auth\"}","times":1}`); a.status != http.StatusNoContent {
			t.Fatalf("POST /_fake/faults = %d %s", a.status, a.body)
		}
		if r := e.wantSteps(t, body("socket", bot+app), "token:fail scopes:skip app_token:skip"); r.Steps[0].Code !=
			"token_invalid" {
			t.Fatalf("a refused token = %+v", r)
		}

		// The records of a draft: draft_check in its run, under the Connection it names, and without an owner.
		for _, c := range []struct{ body, owner string }{
			{body("socket", bot+app), id},
			{`{"type":"slack","event_mode":"socket"` + bot + app + `}`, "<nil>"},
		} {
			r, a := e.draft(t, c.body)
			if a.status != http.StatusOK || !strings.HasPrefix(r.Run, "CR") {
				t.Fatalf("draft = %d %s", a.status, redacted(string(a.body)))
			}
			var items []any
			eventually(t, "the records of a Slack draft", func() bool {
				items = e.api(t).json(http.MethodGet, "/api/v1/request-log?run="+r.Run, "",
					http.StatusOK)["items"].([]any)
				return len(items) >= 4
			})
			for _, it := range items {
				rec := it.(map[string]any)
				if rec["check"] != "draft_check" || rec["check_run"] != r.Run || fmt.Sprint(rec["connection_id"]) !=
					c.owner {
					t.Errorf("a record of the draft %v", redacted(fmt.Sprint(rec)))
				}
			}
		}
		if after := e.stored(t, id); after != before {
			t.Fatalf("the drafts changed the saved Connection: %s, before %s", after, before)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		mm := e.id(t, "mattermost", devmode.ConnectionName)
		for _, c := range []struct{ body, pointer, code string }{
			{`{"type":"bridge","connection_id":"CN0000000000AA","base_url":"` + h.Fakes.Bridge +
				`","event_delivery":"poll"}`, "/connection_id", "unknown_id"},
			{`{"type":"telegram","connection_id":"` + mm + `","bot_api_base_url":"` + h.Fakes.Telegram +
				`","update_mode":"long_polling"}`, "/connection_id", "unknown_id"},
			{`{"type":"mattermost","server_url":"ftp://x"}`, "/server_url", "invalid_format"},
			{`{"type":"slack","event_mode":"socket","bot_token":"` + devmode.SlackSecondAppToken + `"}`, "/bot_token",
				"invalid_format"},
			{`{"type":"slack","event_mode":"socket","connection_id":"` + mm + `"}`, "/connection_id", "unknown_id"},
		} {
			_, a := e.draft(t, c.body)
			if pointer, code := firstError(t, a); a.status != http.StatusUnprocessableEntity || pointer != c.pointer ||
				code != c.code {
				t.Errorf("draft %s = %d %s, want 422 %s at %s", redacted(c.body), a.status, redacted(string(a.body)),
					c.code, c.pointer)
			}
		}
		session := newAgent(t, e.base)
		if a := session.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
			t.Fatalf("sign in = %d %s", a.status, a.body)
		}
		reader := session.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
			`{"name":"reader","permissions":["connections:read"]}`, http.StatusCreated)["value"].(string)
		a := patClient{t: t, base: e.base, token: reader}.do(http.MethodPost, "/api/v1/connections/checks",
			`{"type":"mattermost","server_url":"`+h.Fakes.MattermostSecond+`"}`)
		if a.status != http.StatusForbidden {
			t.Fatalf("draft with connections:read = %d %s", a.status, redacted(string(a.body)))
		}
	})

	for _, r := range h.Replicas {
		for _, token := range knownTokens {
			if strings.Contains(r.Output(), token) {
				t.Fatalf("a bot token reached the output of %s", r.name())
			}
		}
	}
}
