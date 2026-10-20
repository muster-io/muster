// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeslack_test

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

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
		{"chat.postMessage", fakeslack.SecondBotToken, "unknown_method"},
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
		`},"page_size":0}`+"\n" {
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
