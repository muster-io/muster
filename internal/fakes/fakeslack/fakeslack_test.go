// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeslack_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
)

func start(t *testing.T) *fakeslack.Fake {
	t.Helper()
	f := fakeslack.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(t.Context()) })
	return f
}

// answer is an answer of the fake: its status, header and JSON body.
type answer struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

// call posts method with the bearer token, none when it is empty, and the form args; JSON when asJSON.
func call(t *testing.T, f *fakeslack.Fake, method, token string, args map[string]string, asJSON bool) answer {
	t.Helper()
	var body io.Reader
	contentType := "application/x-www-form-urlencoded"
	if asJSON {
		b, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		body, contentType = strings.NewReader(string(b)), "application/json; charset=utf-8"
	} else {
		v := url.Values{}
		for k, a := range args {
			v.Set(k, a)
		}
		body = strings.NewReader(v.Encode())
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.URL()+"/api/"+method, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) answer {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	a := answer{status: resp.StatusCode, header: resp.Header, raw: string(b)}
	_ = json.Unmarshal(b, &a.body)
	return a
}

// slackError checks that a is Slack's error answer with code: 200, JSON, ok false.
func slackError(t *testing.T, a answer, code string) {
	t.Helper()
	if a.status != http.StatusOK || !strings.HasPrefix(a.header.Get("Content-Type"), "application/json") ||
		a.body["ok"] != false || a.body["error"] != code {
		t.Fatalf("want error %s, got %d %s %s", code, a.status, a.header.Get("Content-Type"), a.raw)
	}
}

// keys are the members of a JSON object, sorted.
func keys(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }

// TestAuthTest covers F-072: auth.test with a bot token answers the bot and its workspace, with a person's token the
// person without bot_id, and with an app-level token only the app's id; F-071: x-oauth-scopes names the token's scopes
// and x-accepted-oauth-scopes those of the method.
func TestAuthTest(t *testing.T) {
	f := start(t)
	a, b := fakeslack.Apps()[0], fakeslack.Apps()[1]
	for _, tc := range []struct {
		name, token, scopes string
		want                map[string]any
	}{
		{"bot a", fakeslack.BotToken, fakeslack.DefaultScopes, map[string]any{"ok": true,
			"url": "https://fake-workspace.slack.com/", "team": "Fake workspace", "user": "muster-dev-bot",
			"team_id": "T0MUSTERDEVA", "user_id": "U0MUSTERBOTA", "bot_id": "B0MUSTERDEVA",
			"is_enterprise_install": false}},
		{"bot b", fakeslack.SecondBotToken, fakeslack.DefaultScopes, map[string]any{"ok": true, "url": b.TeamURL,
			"team": "Fake workspace B", "user": b.BotName, "team_id": b.TeamID, "user_id": b.BotUserID,
			"bot_id": b.BotID, "is_enterprise_install": false}},
		{"app-level a", fakeslack.AppToken, fakeslack.AppTokenScopes, map[string]any{"ok": true, "app_id": a.AppID}},
		{"app-level b", fakeslack.SecondAppToken, fakeslack.AppTokenScopes, map[string]any{"ok": true,
			"app_id": "A0MUSTERDEVB"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := call(t, f, "auth.test", tc.token, nil, false)
			if got.status != http.StatusOK || !reflect.DeepEqual(got.body, tc.want) {
				t.Fatalf("auth.test = %d %s", got.status, got.raw)
			}
			if got.header.Get("Content-Type") != "application/json; charset=utf-8" ||
				got.header.Get("X-OAuth-Scopes") != tc.scopes || got.header.Values("X-Accepted-OAuth-Scopes") == nil ||
				got.header.Get("X-Accepted-OAuth-Scopes") != "" {
				t.Fatalf("header %v", got.header)
			}
		})
	}
	if err := f.SetConfig(fakeslack.Config{UserTokens: []string{fakeslack.SecondBotToken}}); err != nil {
		t.Fatal(err)
	}
	got := call(t, f, "auth.test", fakeslack.SecondBotToken, nil, false)
	if _, hasBot := got.body["bot_id"]; hasBot || got.body["ok"] != true || got.body["user"] != fakeslack.UserName ||
		got.body["user_id"] != fakeslack.UserID || got.body["team_id"] != b.TeamID ||
		got.header.Get("X-OAuth-Scopes") != fakeslack.UserScopes {
		t.Fatalf("a person's token = %s %v", got.raw, got.header)
	}
	if k := keys(got.body); !slices.Equal(k, []string{"is_enterprise_install", "ok", "team", "team_id", "url",
		"user", "user_id"}) {
		t.Fatalf("person members %v", k)
	}
}

// TestErrors covers F-070: every error is 200 with ok false — no token, an unknown token, a revoked one and an unknown
// method — with x-accepted-oauth-scopes and without x-oauth-scopes for a token the fake refuses; a path outside /api/
// is 404.
func TestErrors(t *testing.T) {
	f := start(t)
	if err := f.SetConfig(fakeslack.Config{Revoked: []string{fakeslack.BotToken}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, token, code string }{
		{"auth.test", "", "not_authed"},
		{"auth.test", "xoxb-unknown", "invalid_auth"},
		{"auth.test", fakeslack.BotToken, "token_revoked"},
		{"bots.info", fakeslack.BotToken, "token_revoked"},
		{"apps.connections.open", "xapp-unknown", "invalid_auth"},
		{"chat.scheduleMessage", fakeslack.SecondBotToken, "unknown_method"},
	} {
		got := call(t, f, tc.method, tc.token, nil, false)
		slackError(t, got, tc.code)
		if _, ok := got.header["X-Accepted-Oauth-Scopes"]; !ok || got.header.Get("X-OAuth-Scopes") != "" {
			t.Errorf("%s %s: header %v", tc.method, tc.code, got.header)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.URL()+"/api/auth.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Basic "+fakeslack.SecondBotToken)
	slackError(t, do(t, req), "not_authed")
	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, f.URL()+"/api/auth.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+fakeslack.SecondBotToken)
	if got := do(t, req); got.body["ok"] != true || got.body["bot_id"] != "B0MUSTERDEVB" {
		t.Fatalf("GET auth.test = %s", got.raw)
	}
	bad, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.URL()+"/api/auth.test",
		strings.NewReader("{"))
	if err != nil {
		t.Fatal(err)
	}
	bad.Header.Set("Content-Type", "application/json")
	bad.Header.Set("Authorization", "Bearer "+fakeslack.SecondBotToken)
	slackError(t, do(t, bad), "invalid_json")
	for _, p := range []string{"/", "/api/", "/api/a/b", "/v1/auth.test"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.URL()+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := do(t, req); got.status != http.StatusNotFound {
			t.Errorf("%s = %d", p, got.status)
		}
	}
}

// TestBotsInfo covers F-072: bots.info answers the bot of the token's app with its app_id, from a form or a JSON body;
// an app-level token is the wrong kind, a token without users:read lacks the scope with needed and provided (F-070),
// and another bot is not found.
func TestBotsInfo(t *testing.T) {
	f := start(t)
	for _, asJSON := range []bool{false, true} {
		got := call(t, f, "bots.info", fakeslack.BotToken, map[string]string{"bot": "B0MUSTERDEVA"}, asJSON)
		bot, _ := got.body["bot"].(map[string]any)
		if got.body["ok"] != true || bot["id"] != "B0MUSTERDEVA" || bot["app_id"] != "A0MUSTERDEVA" ||
			bot["user_id"] != "U0MUSTERBOTA" || bot["name"] != "muster-dev-bot" || bot["deleted"] != false ||
			got.header.Get("X-Accepted-OAuth-Scopes") != "users:read" ||
			got.header.Get("X-OAuth-Scopes") != fakeslack.DefaultScopes {
			t.Fatalf("bots.info (json %v) = %s %v", asJSON, got.raw, got.header)
		}
		if k := keys(bot); !slices.Equal(k, []string{"app_id", "deleted", "icons", "id", "name", "updated",
			"user_id"}) {
			t.Fatalf("bot members %v", k)
		}
	}
	slackError(t, call(t, f, "bots.info", fakeslack.BotToken, map[string]string{"bot": "B0MUSTERDEVB"}, false),
		"bot_not_found")
	slackError(t, call(t, f, "bots.info", fakeslack.BotToken, nil, false), "bot_not_found")
	slackError(t, call(t, f, "bots.info", fakeslack.AppToken, map[string]string{"bot": "B0MUSTERDEVA"}, false),
		"not_allowed_token_type")
	scopes := "chat:write,channels:read"
	if err := f.SetConfig(fakeslack.Config{Scopes: map[string]string{fakeslack.AppB: scopes}}); err != nil {
		t.Fatal(err)
	}
	got := call(t, f, "bots.info", fakeslack.SecondBotToken, map[string]string{"bot": "B0MUSTERDEVB"}, false)
	slackError(t, got, "missing_scope")
	if got.body["needed"] != "users:read" || got.body["provided"] != scopes || got.header.Get("X-OAuth-Scopes") != scopes {
		t.Fatalf("missing_scope = %s %v", got.raw, got.header)
	}
	if got := call(t, f, "auth.test", fakeslack.BotToken, nil, false); got.header.Get("X-OAuth-Scopes") !=
		fakeslack.DefaultScopes {
		t.Fatalf("app a scopes %v", got.header)
	}
}

// TestConnectionsOpen covers F-100: apps.connections.open answers an app-level token a Socket Mode address on the fake
// with a new ticket each time and the app's id; a bot token is the wrong kind (F-070).
func TestConnectionsOpen(t *testing.T) {
	f := start(t)
	host := strings.TrimPrefix(f.URL(), "http://")
	re := regexp.MustCompile(`^ws://` + regexp.QuoteMeta(host) + `/link/\?ticket=([0-9a-f]{32})&app_id=A0MUSTERDEVB$`)
	var tickets []string
	for range 2 {
		got := call(t, f, "apps.connections.open", fakeslack.SecondAppToken, nil, false)
		u, _ := got.body["url"].(string)
		m := re.FindStringSubmatch(u)
		if got.body["ok"] != true || m == nil || got.header.Get("X-Accepted-OAuth-Scopes") != "connections:write" ||
			got.header.Get("X-OAuth-Scopes") != fakeslack.AppTokenScopes {
			t.Fatalf("apps.connections.open = %s %v", got.raw, got.header)
		}
		tickets = append(tickets, m[1])
	}
	if tickets[0] == tickets[1] {
		t.Fatalf("the ticket was used twice: %v", tickets)
	}
	slackError(t, call(t, f, "apps.connections.open", fakeslack.BotToken, nil, false), "not_allowed_token_type")
}

// putConfig sends a PUT of /_fake/config as curl -d does, without a JSON content type.
func putConfig(t *testing.T, f *fakeslack.Fake, body string) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, f.URL()+"/_fake/config",
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(t, req)
}

// TestConfig covers /_fake/config: the default, the patches of the story's Verification — scopes set and reset with
// null, app_of set and cleared, revoked tokens and person's tokens set and cleared — and the refusals.
func TestConfig(t *testing.T) {
	f := start(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.URL()+"/_fake/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	channels := `{"C0FAKEALERTS":{"name":"alerts","member":true,"archived":false,"private":false},` +
		`"C0FAKEOTHER":{"name":"other","member":false,"archived":false,"private":false}}`
	if got := do(t, req); got.raw != `{"scopes":{"a":"`+fakeslack.DefaultScopes+`","b":"`+fakeslack.DefaultScopes+
		`"},"revoked":[],"user_tokens":[],"app_of":{},"channels":{"a":`+channels+`,"b":`+channels+
		`},"page_size":0,"request_url":{},"extra_connections":{}}`+"\n" {
		t.Fatalf("default config %s", got.raw)
	}
	five := "chat:write,channels:read,groups:read,im:write,users:read"
	if got := putConfig(t, f, `{"scopes":{"b":"`+five+`"}}`); got.status != http.StatusOK ||
		f.Config().Scopes[fakeslack.AppB] != five || f.Config().Scopes[fakeslack.AppA] != fakeslack.DefaultScopes {
		t.Fatalf("scopes = %d %s", got.status, got.raw)
	}
	if got := call(t, f, "auth.test", fakeslack.SecondBotToken, nil, false); got.header.Get("X-OAuth-Scopes") != five {
		t.Fatalf("scopes of b %v", got.header)
	}
	putConfig(t, f, `{"scopes":{"b":null},"app_of":{"xapp-muster-dev-slack-token-b":"a"}}`)
	c := f.Config()
	if c.Scopes[fakeslack.AppB] != fakeslack.DefaultScopes || c.AppOf[fakeslack.SecondAppToken] != fakeslack.AppA {
		t.Fatalf("config %+v", c)
	}
	if got := call(t, f, "auth.test", fakeslack.SecondAppToken, nil, false); got.body["app_id"] != "A0MUSTERDEVA" {
		t.Fatalf("app_of: auth.test = %s", got.raw)
	}
	if got := call(t, f, "bots.info", fakeslack.SecondBotToken, map[string]string{"bot": "B0MUSTERDEVB"},
		false); got.body["bot"].(map[string]any)["app_id"] != "A0MUSTERDEVB" {
		t.Fatalf("the bot token keeps its app: %s", got.raw)
	}
	putConfig(t, f, `{"app_of":{},"revoked":["xoxb-muster-dev-slack-token-b"]}`)
	if c := f.Config(); len(c.AppOf) != 0 || !slices.Equal(c.Revoked, []string{fakeslack.SecondBotToken}) {
		t.Fatalf("config %+v", c)
	}
	slackError(t, call(t, f, "auth.test", fakeslack.SecondBotToken, nil, false), "token_revoked")
	if got := call(t, f, "auth.test", fakeslack.SecondAppToken, nil, false); got.body["app_id"] != "A0MUSTERDEVB" {
		t.Fatalf("app_of cleared: %s", got.raw)
	}
	putConfig(t, f, `{"revoked":[],"user_tokens":["xoxb-muster-dev-slack-token-b"]}`)
	if got := call(t, f, "auth.test", fakeslack.SecondBotToken, nil, false); got.body["bot_id"] != nil ||
		got.body["user"] != fakeslack.UserName {
		t.Fatalf("user token: %s", got.raw)
	}
	putConfig(t, f, `{"user_tokens":[]}`)
	if !reflect.DeepEqual(f.Config(), fakeslack.DefaultConfig()) {
		t.Fatalf("config %+v", f.Config())
	}
	for _, body := range []string{`{"scopes":{"c":"chat:write"}}`, `{"scopes":{"c":null}}`,
		`{"app_of":{"xoxb-x":"c"}}`, `{"other":1}`, `{`} {
		if got := putConfig(t, f, body); got.status != http.StatusBadRequest {
			t.Errorf("%s = %d %s", body, got.status, got.raw)
		}
	}
	if !reflect.DeepEqual(f.Config(), fakeslack.DefaultConfig()) {
		t.Fatalf("a refused patch changed the config: %+v", f.Config())
	}
	if err := f.SetConfig(fakeslack.Config{AppOf: map[string]string{"xoxb-x": "z"}}); err == nil {
		t.Fatal("SetConfig took an unknown app")
	}
}

// TestAppOfOtherTokens: a token the fake did not publish belongs to the app app_of names, as an app-level token when it
// starts with xapp- and a bot token otherwise.
func TestAppOfOtherTokens(t *testing.T) {
	f := start(t)
	if err := f.SetConfig(fakeslack.Config{AppOf: map[string]string{"xoxb-other": fakeslack.AppB,
		"xapp-other": fakeslack.AppA}}); err != nil {
		t.Fatal(err)
	}
	if got := call(t, f, "auth.test", "xoxb-other", nil, false); got.body["bot_id"] != "B0MUSTERDEVB" {
		t.Fatalf("bot token %s", got.raw)
	}
	if got := call(t, f, "auth.test", "xapp-other", nil, false); got.body["app_id"] != "A0MUSTERDEVA" ||
		got.header.Get("X-OAuth-Scopes") != fakeslack.AppTokenScopes {
		t.Fatalf("app-level token %s", got.raw)
	}
}

// TestFaultsAndRequests covers the harness's faults as the story scripts them — an error answer, a 429 with Retry-After
// and a delay — and the requests recorded with the status they were answered with.
func TestFaultsAndRequests(t *testing.T) {
	f := start(t)
	post := func(body string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.URL()+"/_fake/faults",
			strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := do(t, req); got.status != http.StatusNoContent {
			t.Fatalf("fault %s = %d %s", body, got.status, got.raw)
		}
	}
	post(`{"path":"/api/auth.test","status":200,"body":"{\"ok\":false,\"error\":\"invalid_auth\"}","times":1}`)
	slackError(t, call(t, f, "auth.test", fakeslack.BotToken, nil, false), "invalid_auth")
	post(`{"path":"/api/auth.test","status":429,"retry_after_seconds":30,` +
		`"body":"{\"ok\":false,\"error\":\"ratelimited\"}","times":1}`)
	got := call(t, f, "auth.test", fakeslack.BotToken, nil, false)
	if got.status != http.StatusTooManyRequests || got.header.Get("Retry-After") != "30" ||
		got.body["error"] != "ratelimited" || !strings.HasPrefix(got.header.Get("Content-Type"), "application/json") {
		t.Fatalf("429 = %d %v %s", got.status, got.header, got.raw)
	}
	post(`{"path":"/api/auth.test","delay_ms":1,"times":1}`)
	if got := call(t, f, "auth.test", fakeslack.BotToken, nil, false); got.body["ok"] != true {
		t.Fatalf("delayed = %s", got.raw)
	}
	reqs := f.Requests()
	var statuses []int
	for _, r := range reqs {
		statuses = append(statuses, r.Status)
	}
	if len(reqs) != 3 || !slices.Equal(statuses, []int{200, 429, 200}) || reqs[0].Path != "/api/auth.test" ||
		reqs[0].Headers["Authorization"][0] != "Bearer "+fakeslack.BotToken {
		t.Fatalf("requests %+v", reqs)
	}
	if err := f.SetFault(fakeserver.Fault{Path: "/api/*", Status: 500}); err != nil {
		t.Fatal(err)
	}
	if got := call(t, f, "bots.info", fakeslack.BotToken, nil, false); got.status != http.StatusInternalServerError {
		t.Fatalf("500 fault = %d", got.status)
	}
}

func TestPublishedTokens(t *testing.T) {
	for token, want := range map[string]bool{fakeslack.BotToken: true, fakeslack.SecondBotToken: true,
		fakeslack.AppToken: false, fakeslack.SecondAppToken: false, "xoxb-other": false, "": false} {
		if fakeslack.PublishedBotToken(token) != want {
			t.Errorf("PublishedBotToken(%q) != %v", token, want)
		}
	}
	apps := fakeslack.Apps()
	if len(apps) != 2 || apps[0].Key != fakeslack.AppA || apps[1].Key != fakeslack.AppB ||
		apps[0].SigningSecret != "muster-dev-slack-signing-secret" ||
		apps[1].SigningSecret != "muster-dev-slack-signing-secret-b" || apps[1].TeamName != "Fake workspace B" {
		t.Fatalf("apps %+v", apps)
	}
	for _, a := range apps {
		if !strings.HasPrefix(a.BotToken, "xoxb-") || !strings.HasPrefix(a.AppToken, "xapp-") {
			t.Errorf("app %s does not follow Slack's prefixes", a.Key)
		}
	}
	apps[0].AppID = "changed"
	if fakeslack.Apps()[0].AppID != "A0MUSTERDEVA" {
		t.Fatal("Apps returned the fake's own list")
	}
}

// TestConversationsInfo covers F-098: conversations.info answers a channel of the token's app with its id, name and the
// bot's membership, ok also for a channel without the bot and for an archived one; an unknown id, a direct chat's and a
// private channel the bot is not in are channel_not_found; a token without the scope of the channel's type lacks it,
// with needed and provided (F-070); an app-level token is the wrong kind; F-071: x-accepted-oauth-scopes names the
// scopes that read conversations.
func TestConversationsInfo(t *testing.T) {
	f := start(t)
	cfg := fakeslack.DefaultConfig()
	cfg.Channels[fakeslack.AppB]["C0FAKEARCH"] = fakeslack.Channel{Name: "old", Member: true, Archived: true}
	cfg.Channels[fakeslack.AppB]["C0FAKEPRIV"] = fakeslack.Channel{Name: "private-ops", Member: true, Private: true}
	cfg.Channels[fakeslack.AppB]["G0FAKESECRET"] = fakeslack.Channel{Name: "secret", Private: true}
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	info := func(token, id string) answer {
		t.Helper()
		return call(t, f, "conversations.info", token, map[string]string{"channel": id}, false)
	}
	for _, tc := range []struct {
		id, name                    string
		member, archived, isPrivate bool
	}{
		{fakeslack.ChannelAlerts, "alerts", true, false, false},
		{fakeslack.ChannelOther, "other", false, false, false},
		{"C0FAKEARCH", "old", true, true, false},
		{"C0FAKEPRIV", "private-ops", true, false, true},
	} {
		got := info(fakeslack.SecondBotToken, tc.id)
		ch, _ := got.body["channel"].(map[string]any)
		if got.body["ok"] != true || ch["id"] != tc.id || ch["name"] != tc.name || ch["is_member"] != tc.member ||
			ch["is_archived"] != tc.archived || ch["is_private"] != tc.isPrivate || ch["is_channel"] != true ||
			got.header.Get("X-Accepted-OAuth-Scopes") != "channels:read,groups:read,mpim:read,im:read,read" {
			t.Errorf("%s = %s %v", tc.id, got.raw, got.header)
		}
	}
	for _, id := range []string{"C0FAKEGONE", "D0FAKEDM", "G0FAKESECRET", ""} {
		slackError(t, info(fakeslack.SecondBotToken, id), "channel_not_found")
	}
	slackError(t, info(fakeslack.BotToken, "C0FAKEARCH"), "channel_not_found")
	slackError(t, info(fakeslack.SecondAppToken, fakeslack.ChannelAlerts), "not_allowed_token_type")
	for _, tc := range []struct{ scopes, id, needed string }{
		{"chat:write,groups:read", fakeslack.ChannelAlerts, "channels:read"},
		{"chat:write,channels:read", "C0FAKEPRIV", "groups:read"},
	} {
		if err := f.SetConfig(fakeslack.Config{Scopes: map[string]string{fakeslack.AppB: tc.scopes},
			Channels: cfg.Channels}); err != nil {
			t.Fatal(err)
		}
		got := info(fakeslack.SecondBotToken, tc.id)
		slackError(t, got, "missing_scope")
		if got.body["needed"] != tc.needed || got.body["provided"] != tc.scopes {
			t.Errorf("missing %s = %s", tc.needed, got.raw)
		}
	}
	if err := f.SetConfig(fakeslack.Config{UserTokens: []string{fakeslack.BotToken}}); err != nil {
		t.Fatal(err)
	}
	slackError(t, info(fakeslack.BotToken, fakeslack.ChannelAlerts), "missing_scope")
}

// list calls conversations.list with token and args, and returns the answer with the ids of its channels.
func list(t *testing.T, f *fakeslack.Fake, token string, args map[string]string) (answer, []string) {
	t.Helper()
	got := call(t, f, "conversations.list", token, args, false)
	var ids []string
	chans, _ := got.body["channels"].([]any)
	for _, c := range chans {
		ids = append(ids, c.(map[string]any)["id"].(string))
	}
	return got, ids
}

// next is the next_cursor of an answer of conversations.list.
func next(a answer) string {
	meta, _ := a.body["response_metadata"].(map[string]any)
	cursor, _ := meta["next_cursor"].(string)
	return cursor
}

// TestConversationsList covers F-098 and F-099: conversations.list answers the public channels of the token's app,
// members or not, by default; private ones only where the bot is a member and when asked; without the archived ones
// when exclude_archived is true; in pages of limit, and of page_size when the configuration sets it, with next_cursor
// naming the rest; a cursor it did not give is invalid_cursor; im, mpim and private_channel need their scopes
// (missing_scope, F-070), an unknown type is invalid_types and an app-level token the wrong kind.
func TestConversationsList(t *testing.T) {
	f := start(t)
	cfg := fakeslack.DefaultConfig()
	cfg.Channels[fakeslack.AppB]["C0FAKEARCH"] = fakeslack.Channel{Name: "old", Member: true, Archived: true}
	cfg.Channels[fakeslack.AppB]["C0FAKEPRIV"] = fakeslack.Channel{Name: "private-ops", Member: true, Private: true}
	cfg.Channels[fakeslack.AppB]["G0FAKESECRET"] = fakeslack.Channel{Name: "secret", Private: true}
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	all := map[string]string{"types": "public_channel,private_channel", "exclude_archived": "true", "limit": "200"}
	got, ids := list(t, f, fakeslack.SecondBotToken, all)
	if got.body["ok"] != true || !slices.Equal(ids, []string{fakeslack.ChannelAlerts, fakeslack.ChannelOther,
		"C0FAKEPRIV"}) || next(got) != "" ||
		got.header.Get("X-Accepted-OAuth-Scopes") != "channels:read,groups:read,mpim:read,im:read,read" {
		t.Fatalf("all = %s %v", got.raw, ids)
	}
	first := got.body["channels"].([]any)[0].(map[string]any)
	if first["name"] != "alerts" || first["is_member"] != true || first["is_private"] != false ||
		first["is_archived"] != false {
		t.Errorf("a channel = %v", first)
	}
	if _, ids := list(t, f, fakeslack.SecondBotToken, nil); !slices.Equal(ids, []string{fakeslack.ChannelAlerts,
		"C0FAKEARCH", fakeslack.ChannelOther}) {
		t.Errorf("the default types = %v", ids)
	}
	if _, ids := list(t, f, fakeslack.SecondBotToken, map[string]string{"exclude_archived": "1"}); !slices.Equal(ids,
		[]string{fakeslack.ChannelAlerts, fakeslack.ChannelOther}) {
		t.Errorf("exclude_archived=1 = %v", ids)
	}
	if _, ids := list(t, f, fakeslack.BotToken, map[string]string{"types": "private_channel"}); len(ids) != 0 {
		t.Errorf("app a's private channels = %v", ids)
	}

	var pages [][]string
	args := map[string]string{"types": "public_channel,private_channel", "limit": "2"}
	for {
		got, ids := list(t, f, fakeslack.SecondBotToken, args)
		if got.body["ok"] != true {
			t.Fatalf("page = %s", got.raw)
		}
		pages = append(pages, ids)
		if args["cursor"] = next(got); args["cursor"] == "" {
			break
		}
	}
	if len(pages) != 2 || !slices.Equal(pages[0], []string{fakeslack.ChannelAlerts, "C0FAKEARCH"}) ||
		!slices.Equal(pages[1], []string{fakeslack.ChannelOther, "C0FAKEPRIV"}) {
		t.Errorf("limit 2 = %v", pages)
	}
	cfg.PageSize = 1
	if err := f.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, ids = list(t, f, fakeslack.SecondBotToken, all)
	if !slices.Equal(ids, []string{fakeslack.ChannelAlerts}) || next(got) == "" {
		t.Errorf("page_size 1 = %s", got.raw)
	}
	all["cursor"] = next(got)
	if _, ids := list(t, f, fakeslack.SecondBotToken, all); !slices.Equal(ids, []string{fakeslack.ChannelOther}) {
		t.Errorf("the second page = %v", ids)
	}
	for _, cursor := range []string{"bm9wZQ==", "%%%", "bmV4dDo="} {
		all["cursor"] = cursor
		slackError(t, call(t, f, "conversations.list", fakeslack.SecondBotToken, all, false), "invalid_cursor")
	}
	for _, tc := range []struct{ types, code string }{
		{"im", "missing_scope"}, {"mpim", "missing_scope"}, {"public_channel,dm", "invalid_types"},
	} {
		slackError(t, call(t, f, "conversations.list", fakeslack.SecondBotToken,
			map[string]string{"types": tc.types}, false), tc.code)
	}
	if err := f.SetConfig(fakeslack.Config{Scopes: map[string]string{fakeslack.AppB: "chat:write,channels:read"}}); err != nil {
		t.Fatal(err)
	}
	got = call(t, f, "conversations.list", fakeslack.SecondBotToken, map[string]string{"types": "private_channel"}, false)
	slackError(t, got, "missing_scope")
	if got.body["needed"] != "groups:read" {
		t.Errorf("needed = %s", got.raw)
	}
	slackError(t, call(t, f, "conversations.list", fakeslack.AppToken, nil, false), "not_allowed_token_type")
}

// TestChannelsConfig covers the channels of /_fake/config: both apps start with alerts, with the bot, and other,
// without it; a PUT merges by id within one app — a channel replaces the one of its id, null deletes it, an app given as
// null gets its default channels again — and sets and resets page_size; a refused patch changes nothing; SetConfig
// gives an app without channels the default ones.
func TestChannelsConfig(t *testing.T) {
	f := start(t)
	c := f.Config()
	for _, key := range []string{fakeslack.AppA, fakeslack.AppB} {
		if !reflect.DeepEqual(c.Channels[key], map[string]fakeslack.Channel{
			fakeslack.ChannelAlerts: {Name: "alerts", Member: true}, fakeslack.ChannelOther: {Name: "other"}}) {
			t.Fatalf("channels of %s = %+v", key, c.Channels[key])
		}
	}
	if got := putConfig(t, f, `{"page_size":1,"channels":{"b":{"C0FAKEPRIV":{"name":"private-ops","member":true,`+
		`"private":true},"C0FAKEALERTS":{"name":"alerts","member":false}}}}`); got.status != http.StatusOK {
		t.Fatalf("put = %d %s", got.status, got.raw)
	}
	c = f.Config()
	if c.PageSize != 1 || len(c.Channels[fakeslack.AppB]) != 3 || c.Channels[fakeslack.AppB][fakeslack.ChannelAlerts].Member ||
		!c.Channels[fakeslack.AppB]["C0FAKEPRIV"].Private || !c.Channels[fakeslack.AppA][fakeslack.ChannelAlerts].Member {
		t.Fatalf("merged %+v", c)
	}
	putConfig(t, f, `{"channels":{"b":{"C0FAKEOTHER":null}}}`)
	if _, ok := f.Config().Channels[fakeslack.AppB][fakeslack.ChannelOther]; ok {
		t.Error("null kept the channel")
	}
	putConfig(t, f, `{"page_size":0,"channels":{"b":null}}`)
	if !reflect.DeepEqual(f.Config(), fakeslack.DefaultConfig()) {
		t.Fatalf("reset %+v", f.Config())
	}
	for _, body := range []string{`{"channels":{"c":{"C1":{"name":"x"}}}}`, `{"channels":{"a":{"":{"name":"x"}}}}`,
		`{"page_size":-1}`, `{"channels":{"a":{"C1":{"name":1}}}}`} {
		if got := putConfig(t, f, body); got.status != http.StatusBadRequest {
			t.Errorf("%s = %d %s", body, got.status, got.raw)
		}
	}
	if !reflect.DeepEqual(f.Config(), fakeslack.DefaultConfig()) {
		t.Fatalf("a refused patch changed the config: %+v", f.Config())
	}
	if err := f.SetConfig(fakeslack.Config{Channels: map[string]map[string]fakeslack.Channel{
		fakeslack.AppA: {"C1": {Name: "one"}}}}); err != nil {
		t.Fatal(err)
	}
	if c := f.Config(); len(c.Channels[fakeslack.AppA]) != 1 || len(c.Channels[fakeslack.AppB]) != 2 {
		t.Errorf("SetConfig channels %+v", c.Channels)
	}
	if err := f.SetConfig(fakeslack.Config{PageSize: -2}); err == nil {
		t.Error("SetConfig took a negative page size")
	}
}

// botB is the bot token of app b, whose channel alerts the chat tests post to, and botUserB its bot's user.
const (
	botB     = fakeslack.SecondBotToken
	botUserB = "U0MUSTERBOTB"
	alerts   = fakeslack.ChannelAlerts
)

// chat calls the chat method with the bearer token and body as JSON, as Muster sends the write methods.
func chat(t *testing.T, f *fakeslack.Fake, method, token string, body map[string]any) answer {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.URL()+"/api/"+method, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	return do(t, req)
}

// post posts to the channel alerts of app b with the arguments args, the channel added.
func post(t *testing.T, f *fakeslack.Fake, args map[string]any) answer {
	t.Helper()
	if _, set := args["channel"]; !set {
		args["channel"] = alerts
	}
	return chat(t, f, "chat.postMessage", botB, args)
}

// ok checks that a is a successful answer and returns its ts.
func ok(t *testing.T, a answer) string {
	t.Helper()
	if a.status != http.StatusOK || a.body["ok"] != true {
		t.Fatalf("want ok, got %d %s", a.status, a.raw)
	}
	ts, _ := a.body["ts"].(string)
	return ts
}

func textBlock(id, text string) map[string]any {
	return map[string]any{"type": "section", "block_id": id, "text": map[string]any{"type": "mrkdwn", "text": text}}
}

func buttonOf(id, text, value string) map[string]any {
	return map[string]any{"type": "button", "action_id": id, "text": map[string]any{"type": "plain_text", "text": text},
		"value": value}
}

func actionsOf(id string, elements ...any) map[string]any {
	return map[string]any{"type": "actions", "block_id": id, "elements": elements}
}

// menu is a static_select with options n, each of text and value.
func menu(n int, text, value string) map[string]any {
	options := make([]any, n)
	for i := range options {
		options[i] = map[string]any{"text": map[string]any{"type": "plain_text", "text": text}, "value": value}
	}
	return map[string]any{"type": "static_select", "action_id": "snooze",
		"placeholder": map[string]any{"type": "plain_text", "text": "Snooze…"}, "options": options}
}

// sections are n sections with distinct block ids.
func sections(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = textBlock(fmt.Sprintf("b%d", i), "x")
	}
	return out
}

// invalid checks that a is invalid_blocks naming pointer.
func invalid(t *testing.T, a answer, pointer string) {
	t.Helper()
	slackError(t, a, "invalid_blocks")
	errs, _ := a.body["errors"].([]any)
	for _, e := range errs {
		if s, _ := e.(string); strings.HasSuffix(s, "[json-pointer:"+pointer+"]") {
			return
		}
	}
	t.Fatalf("invalid_blocks without %s: %s", pointer, a.raw)
}

// messagesOf are the messages of the channel alerts of app b, as GET /_fake/messages lists them.
func messagesOf(t *testing.T, f *fakeslack.Fake, query string) []fakeslack.Message {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.URL()+"/_fake/messages?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := do(t, req)
	var out []fakeslack.Message
	if a.status != http.StatusOK || json.Unmarshal([]byte(a.raw), &out) != nil {
		t.Fatalf("messages = %d %s", a.status, a.raw)
	}
	return out
}

// TestPostMessage covers F-074, F-089 and F-098 as Muster posts: a message with blocks, an attachment and unfurling
// off is answered with its channel, its ts and the message by the bot, kept as sent and listed by GET /_fake/messages
// without replies or edit; a request without text, blocks or attachments is no_text; blocks or attachments that are not
// a list are refused; a form-encoded post carries its blocks as JSON.
func TestPostMessage(t *testing.T) {
	f := start(t)
	blocks := []any{textBlock("muster_summary", "🔴 #1 DiskFull <!here>")}
	attachments := []any{map[string]any{"color": "#d32f2f", "fallback": "🔴 #1 DiskFull",
		"blocks": []any{textBlock("muster_heading", "*<http://x/a|#1 DiskFull>*")}}}
	a := post(t, f, map[string]any{"text": "🔴 #1 DiskFull", "blocks": blocks, "attachments": attachments,
		"unfurl_links": false, "unfurl_media": false})
	ts := ok(t, a)
	msg, _ := a.body["message"].(map[string]any)
	if ts != "1.000001" || a.body["channel"] != alerts || msg["user"] != botUserB || msg["bot_id"] != "B0MUSTERDEVB" ||
		msg["ts"] != ts || msg["text"] != "🔴 #1 DiskFull" || msg["thread_ts"] != nil {
		t.Fatalf("answer %s", a.raw)
	}
	got := messagesOf(t, f, "channel="+alerts+"&app=b")
	if len(got) != 1 {
		t.Fatalf("messages %+v", got)
	}
	m := got[0]
	sent, _ := json.Marshal(blocks)
	if m.App != "b" || m.Channel != alerts || m.TS != ts || m.User != botUserB || string(m.Blocks) != string(sent) ||
		!strings.Contains(string(m.Attachments), `"color":"#d32f2f"`) || m.UnfurlLinks == nil || *m.UnfurlLinks ||
		m.UnfurlMedia == nil || *m.UnfurlMedia || m.Subtype != "" || m.Edited != nil || len(m.Replies) != 0 ||
		!strings.Contains(m.Text, "DiskFull") {
		t.Errorf("message %+v", m)
	}
	slackError(t, post(t, f, map[string]any{}), "no_text")
	slackError(t, post(t, f, map[string]any{"text": "", "blocks": []any{}, "attachments": []any{}}), "no_text")
	invalid(t, post(t, f, map[string]any{"text": "x", "blocks": "not a list"}), "/blocks")
	slackError(t, post(t, f, map[string]any{"text": "x", "attachments": map[string]any{}}), "invalid_attachments")
	slackError(t, post(t, f, map[string]any{"text": "x", "attachments": []any{"x"}}), "invalid_attachments")
	form := call(t, f, "chat.postMessage", botB, map[string]string{"channel": alerts, "text": "plain",
		"blocks": `[{"type":"section","block_id":"s","text":{"type":"mrkdwn","text":"form"}}]`}, false)
	if ok(t, form) != "1.000002" {
		t.Fatalf("form post %s", form.raw)
	}
	if got := f.Messages(fakeslack.AppB, alerts); len(got) != 2 || !strings.Contains(string(got[1].Blocks), "form") ||
		got[1].UnfurlLinks != nil {
		t.Errorf("form message %+v", got)
	}
}

// TestTSSequence: every ts comes from one counter — posts, replies and edits alike, across channels and apps — never
// from a clock.
func TestTSSequence(t *testing.T) {
	f := start(t)
	first := ok(t, post(t, f, map[string]any{"text": "one"}))
	ok(t, chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": first, "text": "one, edited"}))
	reply := ok(t, post(t, f, map[string]any{"text": "two", "thread_ts": first}))
	other := ok(t, chat(t, f, "chat.postMessage", fakeslack.BotToken, map[string]any{"channel": alerts, "text": "a"}))
	if first != "1.000001" || reply != "1.000003" || other != "1.000004" {
		t.Fatalf("ts %s %s %s", first, reply, other)
	}
	if m := f.Messages(fakeslack.AppB, alerts)[0]; m.Edited == nil || m.Edited.TS != "1.000002" ||
		m.Edited.User != botUserB {
		t.Errorf("edited %+v", m.Edited)
	}
	if all := f.Messages("", ""); len(all) != 2 || all[0].App != "b" || all[1].App != "a" {
		t.Errorf("both apps %+v", all)
	}
}

// TestBlockLimits covers F-074 and F-076: each limit of Block Kit holds at its bound and is invalid_blocks, with its
// json-pointer, one past it — in the message's blocks and in an attachment's.
func TestBlockLimits(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) }
	for _, tc := range []struct {
		name    string
		at, ok  map[string]any
		pointer string
	}{
		{"section text", map[string]any{"blocks": []any{textBlock("s", long(3001))}},
			map[string]any{"blocks": []any{textBlock("s", long(3000))}}, "/blocks/0/text/text"},
		{"empty section", map[string]any{"blocks": []any{textBlock("s", "")}}, nil, "/blocks/0/text/text"},
		{"section without text", map[string]any{"blocks": []any{map[string]any{"type": "section"}}}, nil, "/blocks/0"},
		{"blocks", map[string]any{"blocks": sections(51)}, map[string]any{"blocks": sections(50)}, "/blocks"},
		{"blocks with attachments", map[string]any{"blocks": sections(26), "attachments": []any{map[string]any{
			"blocks": []any{textBlock("a0", "x"), textBlock("a1", "x"), textBlock("a2", "x"), textBlock("a3", "x"),
				textBlock("a4", "x"), textBlock("a5", "x"), textBlock("a6", "x"), textBlock("a7", "x"),
				textBlock("a8", "x"), textBlock("a9", "x"), textBlock("a10", "x"), textBlock("a11", "x"),
				textBlock("a12", "x"), textBlock("a13", "x"), textBlock("a14", "x"), textBlock("a15", "x"),
				textBlock("a16", "x"), textBlock("a17", "x"), textBlock("a18", "x"), textBlock("a19", "x"),
				textBlock("a20", "x"), textBlock("a21", "x"), textBlock("a22", "x"), textBlock("a23", "x"),
				textBlock("a24", "x")}}}}, nil, "/blocks"},
		{"type", map[string]any{"blocks": []any{map[string]any{"type": "table"}}},
			map[string]any{"blocks": []any{map[string]any{"type": "divider"}}}, "/blocks/0/type"},
		{"block_id", map[string]any{"blocks": []any{textBlock("s", "x"), textBlock("s", "y")}},
			map[string]any{"blocks": []any{textBlock("s", "x"), textBlock("t", "y")}}, "/blocks/1/block_id"},
		{"block_id across the attachment", map[string]any{"blocks": []any{textBlock("s", "x")},
			"attachments": []any{map[string]any{"blocks": []any{textBlock("s", "y")}}}}, nil,
			"/attachments/0/blocks/0/block_id"},
		{"header", map[string]any{"blocks": []any{map[string]any{"type": "header",
			"text": map[string]any{"type": "plain_text", "text": long(151)}}}},
			map[string]any{"blocks": []any{map[string]any{"type": "header",
				"text": map[string]any{"type": "plain_text", "text": long(150)}}}}, "/blocks/0/text/text"},
		{"context", map[string]any{"blocks": []any{map[string]any{"type": "context", "elements": make([]any, 11)}}},
			map[string]any{"blocks": []any{map[string]any{"type": "context", "elements": make([]any, 10)}}},
			"/blocks/0/elements"},
		{"actions", map[string]any{"blocks": []any{actionsOf("a", buttons(26)...)}},
			map[string]any{"blocks": []any{actionsOf("a", buttons(25)...)}}, "/blocks/0/elements"},
		{"button text", map[string]any{"blocks": []any{actionsOf("a", buttonOf("ack", long(76), "v"))}},
			map[string]any{"blocks": []any{actionsOf("a", buttonOf("ack", long(75), "v"))}},
			"/blocks/0/elements/0/text/text"},
		{"button value", map[string]any{"blocks": []any{actionsOf("a", buttonOf("ack", "Ack", long(2001)))}},
			map[string]any{"blocks": []any{actionsOf("a", buttonOf("ack", "Ack", long(2000)))}},
			"/blocks/0/elements/0/value"},
		{"action_id", map[string]any{"blocks": []any{actionsOf("a", buttonOf(long(256), "Ack", "v"))}},
			map[string]any{"blocks": []any{actionsOf("a", buttonOf(long(255), "Ack", "v"))}},
			"/blocks/0/elements/0/action_id"},
		{"action_id twice", map[string]any{"blocks": []any{actionsOf("a", buttonOf("ack", "Ack", "v"),
			buttonOf("ack", "Ack", "w"))}}, map[string]any{"blocks": []any{actionsOf("a", buttonOf("ack", "Ack", "v")),
			actionsOf("b", buttonOf("ack", "Ack", "w"))}}, "/blocks/0/elements/1/action_id"},
		{"element type", map[string]any{"blocks": []any{actionsOf("a", map[string]any{"type": "overflow"})}}, nil,
			"/blocks/0/elements/0/type"},
		{"options", map[string]any{"blocks": []any{actionsOf("a", menu(101, "1h", "v"))}},
			map[string]any{"blocks": []any{actionsOf("a", menu(100, "1h", "v"))}}, "/blocks/0/elements/0/options"},
		{"option text", map[string]any{"blocks": []any{actionsOf("a", menu(1, long(76), "v"))}},
			map[string]any{"blocks": []any{actionsOf("a", menu(1, long(75), "v"))}},
			"/blocks/0/elements/0/options/0/text/text"},
		{"option value", map[string]any{"blocks": []any{actionsOf("a", menu(1, "1h", long(151)))}},
			map[string]any{"blocks": []any{actionsOf("a", menu(1, "1h", long(150)))}},
			"/blocks/0/elements/0/options/0/value"},
		{"attachment section", map[string]any{"attachments": []any{map[string]any{
			"blocks": []any{textBlock("s", long(3001))}}}}, map[string]any{"attachments": []any{map[string]any{
			"blocks": []any{textBlock("s", long(3000))}}}}, "/attachments/0/blocks/0/text/text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := start(t)
			tc.at["text"] = "x"
			invalid(t, post(t, f, tc.at), tc.pointer)
			if tc.ok != nil {
				tc.ok["text"] = "x"
				ok(t, post(t, f, tc.ok))
			}
		})
	}
}

// buttons are n buttons with distinct action ids.
func buttons(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = buttonOf(fmt.Sprintf("b%d", i), "B", "v")
	}
	return out
}

// TestButtonStyles covers F-074: a button's style is primary or danger, and any other — Mattermost's success, an empty
// one — is invalid_blocks.
func TestButtonStyles(t *testing.T) {
	f := start(t)
	styled := func(style string) map[string]any {
		b := buttonOf("ack", "Ack", "v")
		b["style"] = style
		return map[string]any{"text": "x", "blocks": []any{actionsOf("a", b)}}
	}
	ok(t, post(t, f, styled("primary")))
	ok(t, post(t, f, styled("danger")))
	invalid(t, post(t, f, styled("success")), "/blocks/0/elements/0/style")
	invalid(t, post(t, f, styled("")), "/blocks/0/elements/0/style")
}

// TestUpdateKeepsAndReplaces covers F-084 and F-085: chat.update replaces the blocks it names and the attachments it
// names, keeps attachments it does not name, removes them with [], turns a text sent without blocks into one rich_text
// block, records who edited it, and takes the same edit twice; its answer names the message.
func TestUpdateKeepsAndReplaces(t *testing.T) {
	f := start(t)
	att := []any{map[string]any{"color": "#d32f2f", "fallback": "f", "blocks": []any{textBlock("h", "heading")}}}
	ts := ok(t, post(t, f, map[string]any{"text": "🔴 #1", "blocks": []any{textBlock("s", "top")}, "attachments": att}))
	update := func(args map[string]any) answer {
		args["channel"], args["ts"] = alerts, ts
		return chat(t, f, "chat.update", botB, args)
	}
	a := update(map[string]any{"text": "🟠 #1", "blocks": []any{textBlock("s", "acked")}})
	if ok(t, a) != ts || a.body["channel"] != alerts || a.body["text"] != "🟠 #1" {
		t.Fatalf("update %s", a.raw)
	}
	m := f.Messages(fakeslack.AppB, alerts)[0]
	if !strings.Contains(string(m.Blocks), "acked") || !strings.Contains(string(m.Attachments), "heading") ||
		m.Edited == nil || m.Edited.User != botUserB {
		t.Fatalf("kept attachments %+v", m)
	}
	ok(t, update(map[string]any{"text": "🟠 #1", "blocks": []any{textBlock("s", "acked")}}))
	ok(t, update(map[string]any{"text": "🟢 #1", "attachments": []any{}}))
	m = f.Messages(fakeslack.AppB, alerts)[0]
	if string(m.Attachments) != "[]" || !strings.Contains(string(m.Blocks), `"rich_text"`) || m.Text != "🟢 #1" {
		t.Errorf("text without blocks %+v", m)
	}
	ok(t, update(map[string]any{"blocks": []any{textBlock("s", "sections again")}}))
	if m = f.Messages(fakeslack.AppB, alerts)[0]; !strings.Contains(string(m.Blocks), "sections again") ||
		m.Text != "🟢 #1" {
		t.Errorf("a text-only message becomes sections %+v", m)
	}
	invalid(t, update(map[string]any{"blocks": []any{textBlock("s", "")}}), "/blocks/0/text/text")
	slackError(t, update(map[string]any{"text": "", "blocks": []any{}}), "no_text")
}

// TestUpdateTooLong covers F-078 and F-079: chat.update takes a text of 4,000 bytes in Slack's stored form and refuses
// one past it with msg_too_long, counting a bare & as &amp; and a symbol as a shortcode; a post is not checked.
func TestUpdateTooLong(t *testing.T) {
	f := start(t)
	ts := ok(t, post(t, f, map[string]any{"text": strings.Repeat("x", 5000)}))
	update := func(text string) answer {
		return chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": ts, "text": text})
	}
	for _, text := range []string{strings.Repeat("x", 4000), strings.Repeat("&", 800), strings.Repeat("&amp;", 800),
		strings.Repeat("🔴", 333) + "xxxx", strings.Repeat("<", 1000)} {
		ok(t, update(text))
	}
	for _, text := range []string{strings.Repeat("x", 4001), strings.Repeat("&", 801), strings.Repeat("🔴", 334),
		strings.Repeat(">", 1001)} {
		slackError(t, update(text), "msg_too_long")
	}
}

// TestBlockMismatch covers F-086: a message posted only of rich_text blocks cannot be edited into other blocks, but
// into rich_text again; Muster sends sections only, so it never meets block_mismatch.
func TestBlockMismatch(t *testing.T) {
	f := start(t)
	rich := []any{map[string]any{"type": "rich_text", "elements": []any{}}}
	ts := ok(t, post(t, f, map[string]any{"text": "x", "blocks": rich}))
	slackError(t, chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": ts,
		"blocks": []any{textBlock("s", "section")}}), "block_mismatch")
	ok(t, chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": ts, "blocks": rich}))
	plain := ok(t, post(t, f, map[string]any{"text": "plain"}))
	ok(t, chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": plain,
		"blocks": []any{textBlock("s", "section")}}))
}

// TestDeleteAndTombstone covers F-087 and F-090: a root without replies goes, and an edit or a second delete of it is
// message_not_found; a root with replies stays as a tombstone — "This message was deleted." by USLACKBOT, without
// blocks or attachments — that keeps its replies, takes new ones under it and cannot be edited or deleted; a deleted
// reply leaves its Thread.
func TestDeleteAndTombstone(t *testing.T) {
	f := start(t)
	del := func(ts string) answer {
		return chat(t, f, "chat.delete", botB, map[string]any{"channel": alerts, "ts": ts})
	}
	lone := ok(t, post(t, f, map[string]any{"text": "lone"}))
	if a := del(lone); ok(t, a) != lone || a.body["channel"] != alerts {
		t.Fatalf("delete %s", a.raw)
	}
	slackError(t, del(lone), "message_not_found")
	slackError(t, chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": lone, "text": "x"}),
		"message_not_found")
	if got := f.Messages(fakeslack.AppB, alerts); len(got) != 0 {
		t.Fatalf("after the delete %+v", got)
	}
	root := ok(t, post(t, f, map[string]any{"text": "🔴 #2 DiskFull", "blocks": []any{textBlock("s", "top")},
		"attachments": []any{map[string]any{"blocks": []any{textBlock("h", "heading")}}}}))
	first := ok(t, post(t, f, map[string]any{"text": "reply one", "thread_ts": root}))
	second := ok(t, post(t, f, map[string]any{"text": "reply two", "thread_ts": root}))
	ok(t, del(second))
	ok(t, del(root))
	got := f.Messages(fakeslack.AppB, alerts)
	if len(got) != 1 || got[0].Subtype != "tombstone" || got[0].Text != "This message was deleted." ||
		got[0].User != "USLACKBOT" || string(got[0].Blocks) != "[]" || string(got[0].Attachments) != "[]" ||
		len(got[0].Replies) != 1 || got[0].Replies[0].TS != first {
		t.Fatalf("tombstone %+v", got)
	}
	a := post(t, f, map[string]any{"text": "reply three", "thread_ts": root})
	msg, _ := a.body["message"].(map[string]any)
	if ok(t, a) == "" || msg["thread_ts"] != root || msg["parent_user_id"] != "USLACKBOT" {
		t.Errorf("a reply under the tombstone %s", a.raw)
	}
	slackError(t, del(root), "message_not_found")
	slackError(t, chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": root, "text": "x"}),
		"message_not_found")
	if !f.Delete(fakeslack.AppB, alerts, first) || f.Delete(fakeslack.AppB, alerts, first) ||
		f.Delete(fakeslack.AppB, alerts, root) {
		t.Error("Delete")
	}
}

// TestReplyToDeletedRoot covers F-091: a reply whose thread_ts names a root deleted before any reply is posted into the
// channel and answered without thread_ts; Muster deletes it.
func TestReplyToDeletedRoot(t *testing.T) {
	f := start(t)
	root := ok(t, post(t, f, map[string]any{"text": "root"}))
	f.Delete(fakeslack.AppB, alerts, root)
	a := post(t, f, map[string]any{"text": "reply", "thread_ts": root})
	msg, _ := a.body["message"].(map[string]any)
	stray := ok(t, a)
	if _, has := msg["thread_ts"]; has || stray == "" {
		t.Fatalf("answer %s", a.raw)
	}
	got := f.Messages(fakeslack.AppB, alerts)
	if len(got) != 1 || got[0].TS != stray || got[0].Text != "reply" {
		t.Fatalf("channel %+v", got)
	}
	ok(t, chat(t, f, "chat.delete", botB, map[string]any{"channel": alerts, "ts": stray}))
	if got := f.Messages(fakeslack.AppB, alerts); len(got) != 0 {
		t.Errorf("after the delete %+v", got)
	}
}

// TestThreadNormalized covers F-089: a thread_ts naming a reply goes under its root, answered with the root's ts and
// user; reply_broadcast is kept and answered as thread_broadcast; an unknown thread_ts posts into the channel.
func TestThreadNormalized(t *testing.T) {
	f := start(t)
	root := ok(t, post(t, f, map[string]any{"text": "root"}))
	reply := ok(t, post(t, f, map[string]any{"text": "one", "thread_ts": root}))
	a := post(t, f, map[string]any{"text": "two", "thread_ts": reply, "reply_broadcast": true})
	msg, _ := a.body["message"].(map[string]any)
	if ok(t, a) == "" || msg["thread_ts"] != root || msg["parent_user_id"] != botUserB ||
		msg["subtype"] != "thread_broadcast" {
		t.Fatalf("answer %s", a.raw)
	}
	got := f.Messages(fakeslack.AppB, alerts)
	if len(got) != 1 || len(got[0].Replies) != 2 || got[0].Replies[0].ReplyBroadcast ||
		!got[0].Replies[1].ReplyBroadcast || got[0].Replies[1].ThreadTS != root {
		t.Fatalf("thread %+v", got)
	}
	ok(t, post(t, f, map[string]any{"text": "lost", "thread_ts": "9.999999"}))
	if got := f.Messages(fakeslack.AppB, alerts); len(got) != 2 {
		t.Errorf("an unknown thread_ts %+v", got)
	}
}

// TestNotInChannel covers F-098 and C-24.FR-8: a channel the bot left is not_in_channel for a post, an edit and a
// delete of a message that exists, but message_not_found for one deleted; an archived channel is is_archived; an
// unknown channel, a #name, a direct chat and a private channel without the bot are channel_not_found; someone else's
// message cannot be edited or deleted; an app-level token is the wrong kind and a token without chat:write lacks it.
func TestNotInChannel(t *testing.T) {
	f := start(t)
	ts := ok(t, post(t, f, map[string]any{"text": "kept"}))
	gone := ok(t, post(t, f, map[string]any{"text": "gone"}))
	f.Delete(fakeslack.AppB, alerts, gone)
	putConfig(t, f, `{"channels":{"b":{"C0FAKEALERTS":{"name":"alerts","member":false},"C0FAKEARCH":{"name":"old",`+
		`"member":true,"archived":true},"C0FAKEPRIV":{"name":"ops","private":true}}}}`)
	slackError(t, post(t, f, map[string]any{"text": "x"}), "not_in_channel")
	slackError(t, chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": ts, "text": "x"}),
		"not_in_channel")
	slackError(t, chat(t, f, "chat.delete", botB, map[string]any{"channel": alerts, "ts": ts}), "not_in_channel")
	slackError(t, chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": gone, "text": "x"}),
		"message_not_found")
	slackError(t, post(t, f, map[string]any{"channel": "C0FAKEARCH", "text": "x"}), "is_archived")
	for _, ch := range []string{"C0FAKEGONE", "#alerts", "D0FAKEDM", "U0MUSTERUSR1", "C0FAKEPRIV"} {
		slackError(t, post(t, f, map[string]any{"channel": ch, "text": "x"}), "channel_not_found")
	}
	slackError(t, chat(t, f, "chat.update", botB, map[string]any{"channel": "C0FAKEGONE", "ts": ts, "text": "x"}),
		"channel_not_found")
	putConfig(t, f, `{"channels":{"b":null},"app_of":{"xoxp-alice":"b"},"user_tokens":["xoxp-alice"]}`)
	theirs := ok(t, chat(t, f, "chat.postMessage", "xoxp-alice", map[string]any{"channel": alerts, "text": "mine"}))
	if m := f.Messages(fakeslack.AppB, alerts); m[len(m)-1].User != fakeslack.UserID {
		t.Errorf("a person's message %+v", m[len(m)-1])
	}
	slackError(t, chat(t, f, "chat.update", botB, map[string]any{"channel": alerts, "ts": theirs, "text": "x"}),
		"cant_update_message")
	slackError(t, chat(t, f, "chat.delete", botB, map[string]any{"channel": alerts, "ts": theirs}),
		"cant_delete_message")
	slackError(t, chat(t, f, "chat.postMessage", fakeslack.SecondAppToken, map[string]any{"channel": alerts,
		"text": "x"}), "not_allowed_token_type")
	putConfig(t, f, `{"scopes":{"b":"channels:read"}}`)
	a := post(t, f, map[string]any{"text": "x"})
	slackError(t, a, "missing_scope")
	if a.body["needed"] != "chat:write" || a.header.Get("X-Accepted-OAuth-Scopes") != "chat:write" {
		t.Errorf("missing scope %s %v", a.raw, a.header)
	}
}

// TestGetPermalink covers F-088: the link to a message, to a reply with its Thread, and to a tombstone; a message that
// is deleted or unknown, or in a channel without the bot, is message_not_found; an unknown channel channel_not_found.
func TestGetPermalink(t *testing.T) {
	f := start(t)
	link := func(channel, ts string) answer {
		return chat(t, f, "chat.getPermalink", botB, map[string]any{"channel": channel, "message_ts": ts})
	}
	root := ok(t, post(t, f, map[string]any{"text": "root"}))
	reply := ok(t, post(t, f, map[string]any{"text": "reply", "thread_ts": root}))
	if a := link(alerts, root); a.body["ok"] != true ||
		a.body["permalink"] != "https://fake-workspace-b.slack.com/archives/C0FAKEALERTS/p1000001" ||
		a.header.Get("X-Accepted-OAuth-Scopes") != "" {
		t.Errorf("root %s", a.raw)
	}
	if a := link(alerts, reply); a.body["permalink"] !=
		"https://fake-workspace-b.slack.com/archives/C0FAKEALERTS/p1000002?thread_ts=1.000001&cid=C0FAKEALERTS" {
		t.Errorf("reply %s", a.raw)
	}
	f.Delete(fakeslack.AppB, alerts, root)
	if a := link(alerts, root); a.body["ok"] != true {
		t.Errorf("tombstone %s", a.raw)
	}
	slackError(t, link(alerts, "9.999999"), "message_not_found")
	slackError(t, link("C0FAKEGONE", root), "channel_not_found")
	slackError(t, chat(t, f, "chat.getPermalink", fakeslack.SecondAppToken, map[string]any{"channel": alerts,
		"message_ts": root}), "not_allowed_token_type")
	putConfig(t, f, `{"channels":{"b":{"C0FAKEALERTS":{"name":"alerts","member":false}}}}`)
	slackError(t, link(alerts, root), "message_not_found")
}

// TestPostEphemeral covers F-092: an ephemeral message to one of the fake's people in a channel with the bot is
// answered with a new message_ts, kept in /_fake/ephemeral — with its Thread, null without one — and never among the
// channel's messages; without a user, or to anyone else, it is user_not_in_channel, without the bot not_in_channel;
// its content is checked as a post's.
func TestPostEphemeral(t *testing.T) {
	f := start(t)
	eph := func(args map[string]any) answer {
		args["channel"] = alerts
		return chat(t, f, "chat.postEphemeral", botB, args)
	}
	a := eph(map[string]any{"user": fakeslack.UserID, "text": "This is a test message; nothing was changed"})
	if a.body["ok"] != true || a.body["message_ts"] != "1.000001" {
		t.Fatalf("ephemeral %s", a.raw)
	}
	if got := f.Messages("", ""); len(got) != 0 {
		t.Errorf("kept %+v", got)
	}
	for _, user := range []string{fakeslack.UserAlice, fakeslack.UserBob, fakeslack.UserCarol, fakeslack.UserDora} {
		ok(t, chat(t, f, "chat.postEphemeral", botB, map[string]any{"channel": alerts, "user": user, "text": "hi",
			"thread_ts": "1.000001"}))
	}
	slackError(t, eph(map[string]any{"user": "U0STRANGER", "text": "x"}), "user_not_in_channel")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.URL()+"/_fake/ephemeral", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := do(t, req)
	if !strings.HasPrefix(got.raw, `[{"app":"b","channel":"C0FAKEALERTS","user":"U0MUSTERUSR1","thread_ts":null,`+
		`"text":"This is a test message; nothing was changed","ts":"1.000001"},{"app":"b","channel":"C0FAKEALERTS",`+
		`"user":"U0FAKEALICE","thread_ts":"1.000001","text":"hi","ts":"1.000002"}`) || len(f.Ephemerals()) != 5 {
		t.Errorf("ephemeral %s", got.raw)
	}
	slackError(t, eph(map[string]any{"text": "x"}), "user_not_in_channel")
	slackError(t, eph(map[string]any{"user": fakeslack.UserID}), "no_text")
	invalid(t, eph(map[string]any{"user": fakeslack.UserID, "text": "x", "blocks": []any{textBlock("s", "")}}),
		"/blocks/0/text/text")
	putConfig(t, f, `{"channels":{"b":{"C0FAKEALERTS":{"name":"alerts","member":false}}}}`)
	slackError(t, eph(map[string]any{"user": fakeslack.UserID, "text": "x"}), "not_in_channel")
}

// TestMessagesAndDeleteEndpoints covers the control endpoints: GET /_fake/messages lists a channel's messages of one app
// or of both, in posting order, with the text as sent; POST /_fake/delete deletes one as a person would, answering 404
// for none and 400 for a body that is not the request.
func TestMessagesAndDeleteEndpoints(t *testing.T) {
	f := start(t)
	root := ok(t, post(t, f, map[string]any{"text": "🔴 #1 &lt;b&gt; <!here>"}))
	ok(t, post(t, f, map[string]any{"text": "reply", "thread_ts": root}))
	ok(t, chat(t, f, "chat.postMessage", fakeslack.BotToken, map[string]any{"channel": alerts, "text": "app a"}))
	if got := messagesOf(t, f, "channel="+alerts+"&app=b"); len(got) != 1 || got[0].Text != "🔴 #1 &lt;b&gt; <!here>" ||
		len(got[0].Replies) != 1 || got[0].Replies[0].ThreadTS != root || got[0].Replies[0].User != botUserB {
		t.Fatalf("app b %+v", got)
	}
	if got := messagesOf(t, f, "channel="+alerts); len(got) != 2 || got[1].App != "a" {
		t.Fatalf("both apps %+v", got)
	}
	if got := messagesOf(t, f, "channel=C0FAKEOTHER"); len(got) != 0 {
		t.Errorf("other %+v", got)
	}
	del := func(body string) answer {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.URL()+"/_fake/delete",
			strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return do(t, req)
	}
	if a := del(`{"channel":"C0FAKEALERTS","ts":"` + root + `","app":"b"}`); a.status != http.StatusOK ||
		a.body["ok"] != true {
		t.Fatalf("delete = %d %s", a.status, a.raw)
	}
	if got := messagesOf(t, f, "channel="+alerts+"&app=b"); len(got) != 1 || got[0].Subtype != "tombstone" {
		t.Errorf("tombstone %+v", got)
	}
	if a := del(`{"channel":"C0FAKEALERTS","ts":"` + root + `"}`); a.status != http.StatusNotFound {
		t.Errorf("again = %d %s", a.status, a.raw)
	}
	if a := del(`{"channel":"C0FAKEALERTS","ts":"1.000003"}`); a.status != http.StatusOK {
		t.Errorf("either app = %d %s", a.status, a.raw)
	}
	if a := del(`{"chan":1}`); a.status != http.StatusBadRequest {
		t.Errorf("bad body = %d %s", a.status, a.raw)
	}
}

// received is a request the stand-in app got from /_fake/press.
type received struct {
	header http.Header
	path   string
	body   string
}

// receiver is a stand-in for Muster's callback: it keeps each request and answers status with body.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []received
	status int
	answer string
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	rc := &receiver{status: http.StatusOK}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.got = append(rc.got, received{header: r.Header.Clone(), path: r.URL.Path, body: string(b)})
		status, answer := rc.status, rc.answer
		rc.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

// last is the last request the stand-in got, and its payload.
func (rc *receiver) last(t *testing.T) (received, map[string]any) {
	t.Helper()
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.got) == 0 {
		t.Fatal("the app got no press")
	}
	r := rc.got[len(rc.got)-1]
	form, err := url.ParseQuery(r.body)
	var p map[string]any
	if err != nil || json.Unmarshal([]byte(form.Get("payload")), &p) != nil {
		t.Fatalf("not a form with a JSON payload: %s", r.body)
	}
	return r, p
}

// control sends a request to a control endpoint of the fake.
func control(t *testing.T, f *fakeslack.Fake, method, path, body string) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.URL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return do(t, req)
}

// option is an option of the Snooze menu.
func option(text, value string) map[string]any {
	return map[string]any{"text": map[string]any{"type": "plain_text", "text": text}, "value": value}
}

// pressable posts, to the channel alerts of app b, a Root message whose attachment carries Ack, Resolve and the
// Snooze menu with value prefix, and a Reminder in its Thread with "Still on it" and Unack; it returns their ts.
func pressable(t *testing.T, f *fakeslack.Fake, prefix string) (root, reminder string) {
	t.Helper()
	snooze := map[string]any{"type": "static_select", "action_id": "snooze",
		"placeholder": map[string]any{"type": "plain_text", "text": "Snooze…"},
		"options":     []any{option("1h", prefix+"1H"), option("4h", prefix+"4H")}}
	root = ok(t, post(t, f, map[string]any{"text": "🔴 #1 DiskFull", "attachments": []any{map[string]any{
		"color": "#d32f2f", "blocks": []any{textBlock("muster_heading", "#1 DiskFull"),
			actionsOf("muster_actions", buttonOf("ack", "Ack", prefix+"ACK"), buttonOf("resolve", "Resolve",
				prefix+"RES"), snooze)}}}}))
	reminder = ok(t, post(t, f, map[string]any{"text": "Reminder", "thread_ts": root, "blocks": []any{
		textBlock("muster_text", "Reminder"), actionsOf("muster_actions", buttonOf("still_on_it", "Still on it",
			prefix+"STILL"), buttonOf("unack", "Unack", prefix+"UNACK"))}}))
	return root, reminder
}

// TestPress covers /_fake/press (F-093, F-104): the press of a button of a Root message, an option of its Snooze menu
// and a button of a Reminder in its Thread, as one of the people, goes to the app's request_url as Slack sends it — a
// form whose payload is the block_actions payload built from the stored message with the app's verification token and
// a response_url on the fake, signed with the app's Slack signing secret at the current time — and Muster's status and
// body come back; a wrong signature, a timestamp offset, another message's value and a value sent as it is go as they
// are; an unknown message is 404, an app without a request_url or a socket 409, an action the message lacks 422, a
// request it cannot read or an unknown transport 400 and an app that does not answer 502.
func TestPress(t *testing.T) {
	f := start(t)
	rc := newReceiver(t)
	root, reminder := pressable(t, f, "V")
	second, _ := pressable(t, f, "W")
	putConfig(t, f, `{"request_url":{"b":"`+rc.srv.URL+`/api/v1/callbacks/slack/CN1"}}`)
	press := func(body string) answer { return control(t, f, http.MethodPost, "/_fake/press", body) }
	b := fakeslack.Apps()[1]

	before := time.Now().Unix()
	if a := press(`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack","user":"U0FAKEALICE"}`); a.status !=
		http.StatusOK || a.raw != `{"status":200,"body":"","transport":"http"}`+"\n" {
		t.Fatalf("press = %d %s", a.status, a.raw)
	}
	r, p := rc.last(t)
	ts, _ := strconv.ParseInt(r.header.Get("X-Slack-Request-Timestamp"), 10, 64)
	mac := hmac.New(sha256.New, []byte(fakeslack.SecondSigningSecret))
	mac.Write([]byte("v0:" + r.header.Get("X-Slack-Request-Timestamp") + ":" + r.body))
	if r.path != "/api/v1/callbacks/slack/CN1" || r.header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
		r.header.Get("X-Slack-Signature") != "v0="+hex.EncodeToString(mac.Sum(nil)) || ts < before ||
		ts > time.Now().Unix() || !strings.HasPrefix(r.body, "payload=") {
		t.Errorf("request %s %v %s", r.path, r.header, r.body)
	}
	container, _ := p["container"].(map[string]any)
	actions, _ := p["actions"].([]any)
	action, _ := actions[0].(map[string]any)
	user, _ := p["user"].(map[string]any)
	message, _ := p["message"].(map[string]any)
	responseURL, _ := p["response_url"].(string)
	if p["type"] != "block_actions" || p["token"] != fakeslack.SecondVerificationToken || p["api_app_id"] != b.AppID ||
		p["team"].(map[string]any)["id"] != b.TeamID || user["id"] != fakeslack.UserAlice || user["name"] != "alice" ||
		container["type"] != "message_attachment" || container["message_ts"] != root ||
		container["channel_id"] != fakeslack.ChannelAlerts || container["thread_ts"] != nil ||
		p["channel"].(map[string]any)["name"] != "alerts" || message["ts"] != root || message["attachments"] == nil ||
		!regexp.MustCompile(`^`+regexp.QuoteMeta(f.URL())+`/actions/T0MUSTERDEVB/[0-9]+/[0-9a-f]{24}$`).
			MatchString(responseURL) || len(actions) != 1 || action["action_id"] != "ack" ||
		action["block_id"] != "muster_actions" || action["type"] != "button" || action["value"] != "VACK" {
		t.Errorf("payload %v", p)
	}

	press(`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"snooze","option":1,"user":"U0FAKEBOB"}`)
	_, p = rc.last(t)
	action = p["actions"].([]any)[0].(map[string]any)
	if action["type"] != "static_select" || action["selected_option"].(map[string]any)["value"] != "V4H" ||
		action["value"] != nil || p["user"].(map[string]any)["name"] != "bob" {
		t.Errorf("snooze payload %v", p)
	}
	press(`{"app":"b","channel":"C0FAKEALERTS","ts":"` + reminder + `","action":"still_on_it","user":"U0FAKEDORA",` +
		`"transport":"http"}`)
	_, p = rc.last(t)
	container = p["container"].(map[string]any)
	if container["type"] != "message" || container["message_ts"] != reminder || container["thread_ts"] != root ||
		p["actions"].([]any)[0].(map[string]any)["value"] != "VSTILL" {
		t.Errorf("reminder payload %v", p)
	}
	press(`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"resolve","user":"U0FAKECAROL","signature":"wrong",` +
		`"timestamp_offset_seconds":-360,"value_of_ts":"` + second + `"}`)
	r, p = rc.last(t)
	ts, _ = strconv.ParseInt(r.header.Get("X-Slack-Request-Timestamp"), 10, 64)
	if r.header.Get("X-Slack-Signature") != "wrong" || ts > time.Now().Unix()-359 || ts < before-361 ||
		p["actions"].([]any)[0].(map[string]any)["value"] != "WRES" {
		t.Errorf("negative press %v %v", r.header, p)
	}
	rc.mu.Lock()
	rc.status, rc.answer = http.StatusUnauthorized, `{"status":401}`
	rc.mu.Unlock()
	if a := press(`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack","user":"U0FAKEALICE","value":"AAAA"}`); a.raw !=
		`{"status":401,"body":"{\"status\":401}","transport":"http"}`+"\n" {
		t.Errorf("refused press %s", a.raw)
	}
	if _, p = rc.last(t); p["actions"].([]any)[0].(map[string]any)["value"] != "AAAA" {
		t.Errorf("value %v", p)
	}

	ok(t, chat(t, f, "chat.postMessage", fakeslack.BotToken, map[string]any{"channel": alerts, "text": "app a"}))
	appA := f.Messages("a", alerts)[0].TS
	for _, c := range []struct {
		body   string
		status int
	}{
		{`{"channel":"C0FAKEALERTS","ts":"9.999999","action":"ack","user":"U0FAKEALICE"}`, http.StatusNotFound},
		{`{"channel":"C0FAKEOTHER","ts":"` + root + `","action":"ack","user":"U0FAKEALICE"}`, http.StatusNotFound},
		{`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack","user":"U0FAKEALICE","value_of_ts":"9.1"}`,
			http.StatusNotFound},
		{`{"app":"a","channel":"C0FAKEALERTS","ts":"` + appA + `","action":"ack","user":"U0FAKEALICE"}`,
			http.StatusConflict},
		{`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"unsnooze","user":"U0FAKEALICE"}`,
			http.StatusUnprocessableEntity},
		{`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"snooze","option":2,"user":"U0FAKEALICE"}`,
			http.StatusUnprocessableEntity},
		{`{"channel":"C0FAKEALERTS","ts":"` + reminder + `","action":"ack","user":"U0FAKEALICE"}`,
			http.StatusUnprocessableEntity},
		{`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack","user":"U0FAKEALICE","transport":"socket"}`,
			http.StatusConflict},
		{`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack","user":"U0FAKEALICE","transport":"pigeon"}`,
			http.StatusBadRequest},
		{`{"app":"c","channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack","user":"U0FAKEALICE"}`,
			http.StatusBadRequest},
		{`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack"}`, http.StatusBadRequest},
		{`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack","user":"U0FAKEALICE","other":1}`,
			http.StatusBadRequest},
	} {
		if a := press(c.body); a.status != c.status {
			t.Errorf("%s = %d %s, want %d", c.body, a.status, a.raw, c.status)
		}
	}
	putConfig(t, f, `{"request_url":{"b":"http://127.0.0.1:1/cb"}}`)
	if a := press(`{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack","user":"U0FAKEALICE"}`); a.status !=
		http.StatusBadGateway {
		t.Errorf("an app that does not answer = %d %s", a.status, a.raw)
	}
	if got := fakeslack.Sign("8f742231b10e8888abcd99yyyzzz85a5", "1531420618", []byte(
		"token=xyzz0WbapA4vBCDEFasx0q6G&team_id=T1DC2JH3J&team_domain=testteamnow&channel_id=G8PSS9T3V&"+
			"channel_name=foobar&user_id=U2CERLKJA&user_name=roadrunner&command=%2Fwebhook-collect&text=&"+
			"response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2FT1DC2JH3J%2F397700885554%2F96rGlfmibIGlgcZRskXaIFfN&"+
			"trigger_id=398738663015.47445629121.803a0bc887a14d10d2c447fce8b6703c")); got !=
		"v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503" {
		t.Errorf("Slack's example vector signs as %s", got)
	}
}

// TestResponseURL covers the response_url of a press (F-093): an answer is taken, recorded in /_fake/requests, listed
// in /_fake/responses with null for what it did not send, and faultable; it takes five answers, then expired_url; an
// unknown one is expired_url, a body that is not a JSON object invalid_payload, and another method is refused.
func TestResponseURL(t *testing.T) {
	f := start(t)
	rc := newReceiver(t)
	root, _ := pressable(t, f, "V")
	putConfig(t, f, `{"request_url":{"b":"`+rc.srv.URL+`/cb"}}`)
	control(t, f, http.MethodPost, "/_fake/press", `{"channel":"C0FAKEALERTS","ts":"`+root+`","action":"resolve",`+
		`"user":"U0FAKECAROL"}`)
	_, p := rc.last(t)
	target := p["response_url"].(string)
	path := strings.TrimPrefix(target, f.URL())
	answerTo := func(u, body string) answer {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, u, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		return do(t, req)
	}
	muster := `{"response_type":"ephemeral","replace_original":false,"text":"Your Slack account is not linked"}`
	if a := answerTo(target, muster); a.status != http.StatusOK || a.body["ok"] != true {
		t.Fatalf("answer = %d %s", a.status, a.raw)
	}
	if a := control(t, f, http.MethodGet, "/_fake/responses", ""); a.raw != `[{"app":"b","channel":"C0FAKEALERTS",`+
		`"user":"U0FAKECAROL","response_type":"ephemeral","replace_original":false,"delete_original":null,`+
		`"text":"Your Slack account is not linked","thread_ts":null}]`+"\n" {
		t.Errorf("responses %s", a.raw)
	}
	var recorded []fakeserver.Request
	for _, r := range f.Requests() {
		if r.Path == path {
			recorded = append(recorded, r)
		}
	}
	if len(recorded) != 1 || recorded[0].Body != muster || recorded[0].Status != http.StatusOK {
		t.Errorf("recorded %+v", recorded)
	}
	if err := f.SetFault(fakeserver.Fault{Path: "/actions/*/*/*", Status: http.StatusInternalServerError,
		Times: 1}); err != nil {
		t.Fatal(err)
	}
	if a := answerTo(target, muster); a.status != http.StatusInternalServerError {
		t.Errorf("faulted answer = %d %s", a.status, a.raw)
	}
	if a := answerTo(target, `not json`); a.status != http.StatusBadRequest || a.body["error"] != "invalid_payload" {
		t.Errorf("not JSON = %d %s", a.status, a.raw)
	}
	if a := answerTo(target, `["x"]`); a.status != http.StatusBadRequest || a.body["error"] != "invalid_payload" {
		t.Errorf("not an object = %d %s", a.status, a.raw)
	}
	for i := 2; i <= 5; i++ {
		if a := answerTo(target, `{"text":"again","replace_original":true}`); a.status != http.StatusOK {
			t.Fatalf("answer %d = %d %s", i, a.status, a.raw)
		}
	}
	if a := answerTo(target, muster); a.status != http.StatusNotFound || a.body["error"] != "expired_url" {
		t.Errorf("a sixth answer = %d %s", a.status, a.raw)
	}
	if got := f.Responses(); len(got) != 5 || got[4].ResponseType != nil || got[4].ReplaceOriginal == nil ||
		!*got[4].ReplaceOriginal {
		t.Errorf("responses %+v", got)
	}
	if a := answerTo(f.URL()+"/actions/T0MUSTERDEVB/99/00", muster); a.status != http.StatusNotFound ||
		a.body["error"] != "expired_url" {
		t.Errorf("unknown = %d %s", a.status, a.raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := do(t, req); a.status != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d %s", a.status, a.raw)
	}
}

// TestRequestURLConfig covers request_url in /_fake/config: set app by app, kept beside the other app's, removed by
// null; an unknown app or an address that is not an absolute http or https URL is refused, changing nothing.
func TestRequestURLConfig(t *testing.T) {
	f := start(t)
	putConfig(t, f, `{"request_url":{"b":"http://localhost:8081/api/v1/callbacks/slack/CN1"}}`)
	putConfig(t, f, `{"request_url":{"a":"https://muster.example.org/api/v1/callbacks/slack/CN2"}}`)
	if got := f.Config().RequestURL; !reflect.DeepEqual(got, map[string]string{
		"a": "https://muster.example.org/api/v1/callbacks/slack/CN2",
		"b": "http://localhost:8081/api/v1/callbacks/slack/CN1"}) {
		t.Fatalf("request_url %v", got)
	}
	if a := putConfig(t, f, `{"request_url":{"b":null}}`); a.status != http.StatusOK ||
		!strings.Contains(a.raw, `"request_url":{"a":"https://muster.example.org/api/v1/callbacks/slack/CN2"}`) {
		t.Fatalf("removed = %d %s", a.status, a.raw)
	}
	for _, body := range []string{`{"request_url":{"c":"http://x/cb"}}`, `{"request_url":{"a":"/cb"}}`,
		`{"request_url":{"a":"ftp://x/cb"}}`, `{"request_url":{"a":"http://"}}`, `{"request_url":{"a":1}}`} {
		if a := putConfig(t, f, body); a.status != http.StatusBadRequest {
			t.Errorf("%s = %d %s", body, a.status, a.raw)
		}
	}
	if got := f.Config().RequestURL; len(got) != 1 {
		t.Errorf("a refused patch changed request_url: %v", got)
	}
	if err := f.SetConfig(fakeslack.Config{RequestURL: map[string]string{"b": "::"}}); err == nil {
		t.Error("SetConfig took a request_url that is no URL")
	}
	if err := f.SetConfig(fakeslack.Config{}); err != nil || len(f.Config().RequestURL) != 0 {
		t.Errorf("SetConfig without request_url: %v %v", err, f.Config().RequestURL)
	}
}

// sock is a socket of a test client of Socket Mode: the frames it received, read in the background as the pongs of
// its pings need; with confirm it confirms every envelope at once (F-093).
type sock struct {
	c      *websocket.Conn
	frames chan map[string]any
	done   chan struct{}
}

// dialSocket dials the Socket Mode address u; the socket is closed at the end of the test.
func dialSocket(t *testing.T, u string, confirm bool) *sock {
	t.Helper()
	c, resp, err := websocket.Dial(t.Context(), u, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial %s: %v", u, err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	s := &sock{c: c, frames: make(chan map[string]any, 100), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			_, data, err := c.Read(t.Context())
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			if id, ok := m["envelope_id"].(string); ok && confirm {
				_ = c.Write(t.Context(), websocket.MessageText, []byte(`{"envelope_id":"`+id+`"}`))
			}
			s.frames <- m
		}
	}()
	return s
}

// openSocket asks apps.connections.open with the app-level token for an address and dials it.
func openSocket(t *testing.T, f *fakeslack.Fake, token string, confirm bool) (*sock, string) {
	t.Helper()
	a := call(t, f, "apps.connections.open", token, nil, false)
	u, _ := a.body["url"].(string)
	if u == "" {
		t.Fatalf("apps.connections.open = %s", a.raw)
	}
	return dialSocket(t, u, confirm), u
}

// next is the next frame the socket received.
func (s *sock) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case m := <-s.frames:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no frame within 5 s")
	}
	return nil
}

// hello is the hello the socket received first.
func (s *sock) hello(t *testing.T) map[string]any {
	t.Helper()
	m := s.next(t)
	if m["type"] != "hello" {
		t.Fatalf("the first frame is %v, want hello", m)
	}
	return m
}

// sync pings through the socket: the fake answers the ping after what it wrote before and after the frames it read
// before, so that afterwards the frames sent before are in s.frames and those written are taken.
func (s *sock) sync(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// quiet checks that the socket is open and received nothing more.
func (s *sock) quiet(t *testing.T) {
	t.Helper()
	s.sync(t)
	select {
	case m := <-s.frames:
		t.Fatalf("an unexpected frame %v", m)
	default:
	}
}

// ended waits until the fake closed the socket.
func (s *sock) ended(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the socket is still open after 5 s")
	}
}

// eventually polls cond every 10 ms for up to 5 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: not within 5 s", what)
}

// script posts a socket script to the fake.
func script(t *testing.T, f *fakeslack.Fake, body string) answer {
	t.Helper()
	return control(t, f, http.MethodPost, "/_fake/socket", body)
}

// TestSocketHello covers F-100 and F-102: a socket opened at the address of apps.connections.open gets hello, whose
// num_connections counts the app's open sockets that said hello, this one included, and its extra_connections, each
// app on its own; GET /_fake/socket shows each app's open and opened sockets and hellos; the handshake is recorded and
// takes faults; closing the fake ends its sockets.
func TestSocketHello(t *testing.T) {
	f := start(t)
	b1, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	m := b1.hello(t)
	info, _ := m["connection_info"].(map[string]any)
	debug, _ := m["debug_info"].(map[string]any)
	if m["num_connections"] != 1.0 || info["app_id"] != "A0MUSTERDEVB" ||
		debug["approximate_connection_time"] != 18060.0 || debug["host"] != "fake-applink" {
		t.Errorf("hello %v", m)
	}
	b2, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	if m := b2.hello(t); m["num_connections"] != 2.0 {
		t.Errorf("the second hello %v", m)
	}
	_ = b1.c.CloseNow()
	eventually(t, "the closed socket is forgotten", func() bool { return f.Sockets().Apps["b"].Open == 1 })
	putConfig(t, f, `{"extra_connections":{"b":1}}`)
	b3, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	if m := b3.hello(t); m["num_connections"] != 3.0 {
		t.Errorf("hello with another program %v", m)
	}
	a1, _ := openSocket(t, f, fakeslack.AppToken, false)
	if m := a1.hello(t); m["num_connections"] != 1.0 ||
		m["connection_info"].(map[string]any)["app_id"] != "A0MUSTERDEVA" {
		t.Errorf("app a counts its own %v", m)
	}
	want := map[string]fakeslack.AppSockets{"a": {Open: 1, Opened: 1, Hellos: 1},
		"b": {Open: 2, Opened: 3, Hellos: 3}}
	if got := f.Sockets().Apps; !reflect.DeepEqual(got, want) {
		t.Errorf("sockets %+v", got)
	}
	if a := control(t, f, http.MethodGet, "/_fake/socket", ""); a.raw != `{"apps":{"a":{"open":1,"opened":1,`+
		`"hellos":1,"overlapped":false},"b":{"open":2,"opened":3,"hellos":3,"overlapped":false}},"envelopes":[]}`+"\n" {
		t.Errorf("GET /_fake/socket = %d %s", a.status, a.raw)
	}
	links := 0
	for _, r := range f.Requests() {
		if r.Path == "/link/" && strings.HasPrefix(r.Query, "ticket=") && r.Method == http.MethodGet {
			links++
		}
	}
	if links != 4 {
		t.Errorf("%d handshakes recorded, want 4", links)
	}

	if err := f.SetFault(fakeserver.Fault{Path: "/link/", Status: http.StatusForbidden, Times: 1}); err != nil {
		t.Fatal(err)
	}
	a := call(t, f, "apps.connections.open", fakeslack.SecondAppToken, nil, false)
	c, resp, err := websocket.Dial(t.Context(), a.body["url"].(string), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		if c != nil {
			_ = c.CloseNow()
		}
		t.Errorf("a faulted handshake: %v %v", resp, err)
	}

	if err := f.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*sock{b2, b3, a1} {
		s.ended(t)
	}
}

// TestSocketTicketSingleUse covers F-100: a ticket opens one socket; the same address again, or a ticket the fake did
// not issue, gets a socket that never says hello, counted under the app its address names.
func TestSocketTicketSingleUse(t *testing.T) {
	f := start(t)
	first, u := openSocket(t, f, fakeslack.SecondAppToken, false)
	first.hello(t)
	again := dialSocket(t, u, false)
	again.quiet(t)
	host := strings.TrimPrefix(f.URL(), "http://")
	unknown := dialSocket(t, "ws://"+host+"/link/?ticket="+strings.Repeat("0", 32)+"&app_id=A0MUSTERDEVB", false)
	unknown.quiet(t)
	other := dialSocket(t, "ws://"+host+"/link/?ticket=x&app_id=A0OTHER", false)
	other.quiet(t)
	if got := f.Sockets().Apps; got["b"] != (fakeslack.AppSockets{Open: 3, Opened: 3, Hellos: 1}) ||
		got["a"] != (fakeslack.AppSockets{}) {
		t.Errorf("sockets %+v", got)
	}
	if a := script(t, f, `{"send":{"type":"events_api"}}`); a.status != http.StatusOK {
		t.Fatalf("send = %d %s", a.status, a.raw)
	}
	if m := first.next(t); m["type"] != "events_api" {
		t.Errorf("the envelope went to %v", m)
	}
	again.quiet(t)
	unknown.quiet(t)
}

// TestSocketTooMany covers F-102: an app that would count more than ten connections, its other programs' included,
// gets disconnect too_many_websockets without hello, and the socket is closed.
func TestSocketTooMany(t *testing.T) {
	f := start(t)
	putConfig(t, f, `{"extra_connections":{"b":9}}`)
	tenth, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	if m := tenth.hello(t); m["num_connections"] != 10.0 {
		t.Errorf("the tenth hello %v", m)
	}
	eleventh, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	if m := eleventh.next(t); m["type"] != "disconnect" || m["reason"] != "too_many_websockets" {
		t.Errorf("the eleventh got %v", m)
	}
	eleventh.ended(t)
	tenth.quiet(t)
	if got := f.Sockets().Apps["b"]; got != (fakeslack.AppSockets{Open: 1, Opened: 2, Hellos: 1}) {
		t.Errorf("sockets %+v", got)
	}
}

// TestSocketScripts covers POST /_fake/socket: disconnect with a reason, keeping the socket open; too_many_websockets
// and a missing hello armed for the next sockets; close; the refusals; and overlapped, true when a socket said hello
// while the socket of the last scripted refresh was open, false otherwise.
func TestSocketScripts(t *testing.T) {
	f := start(t)
	for _, c := range []struct {
		body   string
		status int
	}{
		{`{"disconnect":"warning"}`, http.StatusConflict},
		{`{"send":{"type":"events_api"}}`, http.StatusConflict},
		{`{"close":true}`, http.StatusConflict},
		{`{"app":"c","skip_hello":1}`, http.StatusBadRequest},
		{`{"skip_hello":-1}`, http.StatusBadRequest},
		{`{"too_many_websockets":-1}`, http.StatusBadRequest},
		{`{"send":{"payload":{}}}`, http.StatusBadRequest},
		{`{"other":1}`, http.StatusBadRequest},
		{`{`, http.StatusBadRequest},
	} {
		if a := script(t, f, c.body); a.status != c.status {
			t.Errorf("%s = %d %s, want %d", c.body, a.status, a.raw, c.status)
		}
	}
	if err := f.Script(t.Context(), fakeslack.SocketScript{Send: &fakeslack.SocketSend{Type: "x",
		Payload: json.RawMessage(`{`)}}); err == nil {
		t.Error("Script sent a payload that is no JSON")
	}

	s1, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	s1.hello(t)
	if a := script(t, f, `{"app":"b","disconnect":"link_disabled"}`); a.status != http.StatusOK ||
		!strings.Contains(a.raw, `"b":{"open":1,"opened":1,"hellos":1,"overlapped":false}`) {
		t.Fatalf("disconnect = %d %s", a.status, a.raw)
	}
	if m := s1.next(t); m["type"] != "disconnect" || m["reason"] != "link_disabled" ||
		m["debug_info"].(map[string]any)["host"] != "fake-applink" {
		t.Errorf("disconnect frame %v", m)
	}
	s1.quiet(t)

	script(t, f, `{"too_many_websockets":1}`)
	s2, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	if m := s2.next(t); m["type"] != "disconnect" || m["reason"] != "too_many_websockets" {
		t.Errorf("an armed too_many_websockets: %v", m)
	}
	s2.ended(t)
	script(t, f, `{"skip_hello":1}`)
	s3, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	s3.quiet(t)
	s4, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	if m := s4.hello(t); m["num_connections"] != 2.0 {
		t.Errorf("a socket without hello is not counted: %v", m)
	}
	if a := script(t, f, `{"close":true}`); a.status != http.StatusOK {
		t.Fatalf("close = %d %s", a.status, a.raw)
	}
	s4.ended(t)
	s3.quiet(t)
	if got := f.Sockets().Apps["b"]; got != (fakeslack.AppSockets{Open: 2, Opened: 4, Hellos: 2}) {
		t.Errorf("sockets %+v", got)
	}
	_ = s3.c.CloseNow()
	eventually(t, "s3 closed", func() bool { return f.Sockets().Apps["b"].Open == 1 })

	script(t, f, `{"disconnect":"refresh_requested"}`)
	if m := s1.next(t); m["reason"] != "refresh_requested" {
		t.Errorf("refresh frame %v", m)
	}
	if f.Sockets().Apps["b"].Overlapped {
		t.Error("overlapped before the next socket")
	}
	s5, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	s5.hello(t)
	_ = s1.c.CloseNow()
	eventually(t, "s1 closed", func() bool { return f.Sockets().Apps["b"].Open == 1 })
	if !f.Sockets().Apps["b"].Overlapped {
		t.Error("the next hello came before the old socket closed, but overlapped is false")
	}

	if err := f.Script(t.Context(), fakeslack.SocketScript{Disconnect: "warning"}); err != nil {
		t.Fatal(err)
	}
	if m := s5.next(t); m["reason"] != "warning" {
		t.Errorf("warning frame %v", m)
	}
	_ = s5.c.CloseNow()
	eventually(t, "s5 closed", func() bool { return f.Sockets().Apps["b"].Open == 0 })
	s6, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	s6.hello(t)
	if got := f.Sockets().Apps["b"]; got.Overlapped || got.Open != 1 || got.Hellos != 4 {
		t.Errorf("a reconnect after the old socket closed is no overlap: %+v", got)
	}
	if f.Sockets().Apps["a"] != (fakeslack.AppSockets{}) {
		t.Errorf("app a %+v", f.Sockets().Apps["a"])
	}
}

// TestSocketEnvelopes covers F-093: an envelope goes through the newest socket of the app that said hello as
// {"envelope_id", "type", "payload", "accepts_response_payload"}; {"envelope_id": …} on that socket confirms it, with the
// time from sending, and on another socket does not; WaitConfirmed waits for the confirmation.
func TestSocketEnvelopes(t *testing.T) {
	f := start(t)
	s1, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	s1.hello(t)
	s2, _ := openSocket(t, f, fakeslack.SecondAppToken, false)
	s2.hello(t)
	a := script(t, f, `{"send":{"type":"events_api","payload":{"event":{"type":"app_mention"}}}}`)
	if a.status != http.StatusOK {
		t.Fatalf("send = %d %s", a.status, a.raw)
	}
	m := s2.next(t)
	id, _ := m["envelope_id"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(id) ||
		m["type"] != "events_api" || m["accepts_response_payload"] != false ||
		m["payload"].(map[string]any)["event"].(map[string]any)["type"] != "app_mention" {
		t.Errorf("envelope %v", m)
	}
	s1.quiet(t)
	if !strings.Contains(a.raw, `"envelopes":[{"app":"b","envelope_id":"`+id+
		`","type":"events_api","confirmed":false,"confirm_ms":null}]`) {
		t.Errorf("state %s", a.raw)
	}
	confirm := func(s *sock, id string) {
		t.Helper()
		if err := s.c.Write(t.Context(), websocket.MessageText, []byte(`{"envelope_id":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
		s.sync(t)
	}
	confirm(s1, id)
	_ = s1.c.Write(t.Context(), websocket.MessageText, []byte(`not json`))
	s1.sync(t)
	if got := f.Sockets().Envelopes[0]; got.Confirmed {
		t.Errorf("a confirmation on another socket counted: %+v", got)
	}
	confirm(s2, id)
	if got := f.Sockets().Envelopes[0]; !got.Confirmed || got.ConfirmMS == nil || *got.ConfirmMS < 0 ||
		*got.ConfirmMS > 5000 {
		t.Errorf("confirmed %+v", got)
	}

	sent, err := f.SendEnvelope(t.Context(), fakeslack.AppB, "slash_commands", nil)
	if err != nil || sent.Type != "slash_commands" || sent.App != "b" || sent.Confirmed {
		t.Fatalf("SendEnvelope = %+v, %v", sent, err)
	}
	if m := s2.next(t); m["envelope_id"] != sent.EnvelopeID || !reflect.DeepEqual(m["payload"], map[string]any{}) {
		t.Errorf("envelope %v", m)
	}
	done, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok := f.WaitConfirmed(done, sent.EnvelopeID); ok {
		t.Error("WaitConfirmed before the confirmation")
	}
	confirm(s2, sent.EnvelopeID)
	if got, ok := f.WaitConfirmed(t.Context(), sent.EnvelopeID); !ok || got.ConfirmMS == nil {
		t.Errorf("WaitConfirmed = %+v, %v", got, ok)
	}
	if _, ok := f.WaitConfirmed(t.Context(), "nope"); ok {
		t.Error("WaitConfirmed of an envelope never sent")
	}
	if _, err := f.SendEnvelope(t.Context(), fakeslack.AppA, "events_api", nil); err == nil {
		t.Error("SendEnvelope to an app without a socket")
	}
	if got := len(f.Sockets().Envelopes); got != 2 {
		t.Errorf("%d envelopes, want 2", got)
	}
}

// TestPressOverSocket covers /_fake/press in Socket Mode (F-093): without a request_url the press goes as an
// interactive envelope, whose payload is the block_actions payload, through a socket of the app, and the answer tells
// that Muster confirmed it and how fast; with a request_url it goes there unless transport names socket; an app with
// neither, or without the way asked, is 409.
func TestPressOverSocket(t *testing.T) {
	f := start(t)
	root, _ := pressable(t, f, "V")
	press := func(body string) answer {
		t.Helper()
		return control(t, f, http.MethodPost, "/_fake/press", body)
	}
	ack := `{"channel":"C0FAKEALERTS","ts":"` + root + `","action":"ack","user":"U0FAKEALICE"`
	if a := press(ack + `}`); a.status != http.StatusConflict || a.body["error"] != "the app has no request_url and no socket" {
		t.Errorf("neither = %d %s", a.status, a.raw)
	}
	s, _ := openSocket(t, f, fakeslack.SecondAppToken, true)
	s.hello(t)
	a := press(ack + `}`)
	var res fakeslack.PressResult
	if err := json.Unmarshal([]byte(a.raw), &res); err != nil || a.status != http.StatusOK ||
		res.Transport != fakeslack.TransportSocket || res.Confirmed == nil || !*res.Confirmed || res.ConfirmMS == nil ||
		res.EnvelopeID == "" || res.Status != 0 {
		t.Fatalf("press over the socket = %d %s", a.status, a.raw)
	}
	m := s.next(t)
	p, _ := m["payload"].(map[string]any)
	action := p["actions"].([]any)[0].(map[string]any)
	if m["type"] != "interactive" || m["envelope_id"] != res.EnvelopeID || p["type"] != "block_actions" ||
		p["token"] != fakeslack.SecondVerificationToken || action["value"] != "VACK" ||
		!strings.HasPrefix(p["response_url"].(string), f.URL()+"/actions/T0MUSTERDEVB/") {
		t.Errorf("envelope %v", m)
	}
	if got := f.Sockets().Envelopes; len(got) != 1 || got[0].Type != "interactive" || !got[0].Confirmed {
		t.Errorf("envelopes %+v", got)
	}

	rc := newReceiver(t)
	putConfig(t, f, `{"request_url":{"b":"`+rc.srv.URL+`/cb"}}`)
	if a := press(ack + `}`); a.status != http.StatusOK || !strings.Contains(a.raw, `"transport":"http"`) {
		t.Errorf("with a request_url = %d %s", a.status, a.raw)
	}
	rc.last(t)
	if a := press(ack + `,"transport":"socket"}`); a.status != http.StatusOK ||
		!strings.Contains(a.raw, `"transport":"socket"`) || !strings.Contains(a.raw, `"confirmed":true`) {
		t.Errorf("transport socket = %d %s", a.status, a.raw)
	}
	if m := s.next(t); m["type"] != "interactive" {
		t.Errorf("envelope %v", m)
	}
	ok(t, chat(t, f, "chat.postMessage", fakeslack.BotToken, map[string]any{"channel": alerts, "text": "app a"}))
	appA := f.Messages("a", alerts)[0].TS
	if a := press(`{"app":"a","channel":"C0FAKEALERTS","ts":"` + appA + `","action":"ack","user":"U0FAKEALICE",` +
		`"transport":"socket"}`); a.status != http.StatusConflict {
		t.Errorf("app a without a socket = %d %s", a.status, a.raw)
	}
	putConfig(t, f, `{"request_url":{"b":null}}`)
	if a := press(ack + `,"transport":"http"}`); a.status != http.StatusConflict {
		t.Errorf("transport http without a request_url = %d %s", a.status, a.raw)
	}
}

// TestExtraConnectionsConfig covers extra_connections in /_fake/config: set app by app, removed by null or 0; an
// unknown app or a negative number is refused, changing nothing.
func TestExtraConnectionsConfig(t *testing.T) {
	f := start(t)
	putConfig(t, f, `{"extra_connections":{"b":2}}`)
	putConfig(t, f, `{"extra_connections":{"a":1}}`)
	if got := f.Config().ExtraConnections; !reflect.DeepEqual(got, map[string]int{"a": 1, "b": 2}) {
		t.Fatalf("extra_connections %v", got)
	}
	if a := putConfig(t, f, `{"extra_connections":{"b":null,"a":0}}`); a.status != http.StatusOK ||
		!strings.Contains(a.raw, `"extra_connections":{}`) {
		t.Fatalf("removed = %d %s", a.status, a.raw)
	}
	putConfig(t, f, `{"extra_connections":{"b":3}}`)
	for _, body := range []string{`{"extra_connections":{"c":1}}`, `{"extra_connections":{"b":-1}}`,
		`{"extra_connections":{"b":"x"}}`} {
		if a := putConfig(t, f, body); a.status != http.StatusBadRequest {
			t.Errorf("%s = %d %s", body, a.status, a.raw)
		}
	}
	if got := f.Config().ExtraConnections; !reflect.DeepEqual(got, map[string]int{"b": 3}) {
		t.Errorf("a refused patch changed extra_connections: %v", got)
	}
	if err := f.SetConfig(fakeslack.Config{ExtraConnections: map[string]int{"b": -1}}); err == nil {
		t.Error("SetConfig took a negative number")
	}
	if err := f.SetConfig(fakeslack.Config{}); err != nil || len(f.Config().ExtraConnections) != 0 {
		t.Errorf("SetConfig without extra_connections: %v %v", err, f.Config().ExtraConnections)
	}
}
