// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package fakeslack is the fake Slack Web API server for tests and `muster dev` (C-01.FR-13). It serves two Slack
// apps, each installed in a workspace of its own with a bot: app a with the bot token BotToken, the app-level token
// AppToken and the Slack signing secret SigningSecret, which the demo Connection of `muster dev` uses, and app b with
// SecondBotToken, SecondAppToken and SecondSigningSecret. It answers `/api/<method>`, GET or POST, as Slack does
// (F-070 to F-072): every answer is 200 with JSON, an error is `{"ok":false,"error":…}`, a request without a bearer
// token is not_authed, an unknown token invalid_auth and a revoked one token_revoked; every answer names the scopes the
// method accepts in x-accepted-oauth-scopes and, for a valid token, the token's scopes in x-oauth-scopes. auth.test
// answers the bot and its workspace for a bot token, the person alice for a token configured as a person's, and the
// app's id alone for an app-level token; bots.info answers the bot of the token's app; apps.connections.open answers a
// Socket Mode address with a new ticket for an app-level token. Any other method is unknown_method; any other path
// 404.
//
// The control endpoints under /_fake/ come from the fake-server harness (requests; faults, such as an error answer, a
// 429 with Retry-After or a delay) and add GET and PUT /_fake/config: the scopes of each app's bot token, the revoked
// tokens, the tokens answered as a person's and the app of each token, over the published pairing.
package fakeslack

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The published tokens and Slack signing secrets of the two apps.
const (
	BotToken            = "xoxb-muster-dev-slack-token"       //nolint:gosec // G101: the published development token of the fake
	AppToken            = "xapp-muster-dev-slack-token"       //nolint:gosec // G101: the published development token of the fake
	SigningSecret       = "muster-dev-slack-signing-secret"   //nolint:gosec // G101: the published development secret of the fake
	SecondBotToken      = "xoxb-muster-dev-slack-token-b"     //nolint:gosec // G101: the published development token of the fake
	SecondAppToken      = "xapp-muster-dev-slack-token-b"     //nolint:gosec // G101: the published development token of the fake
	SecondSigningSecret = "muster-dev-slack-signing-secret-b" //nolint:gosec // G101: the published development secret of the fake
)

// The keys of the two apps in /_fake/config.
const (
	AppA = "a"
	AppB = "b"
)

// The scopes the fake names: those of a bot token by default, in the order of F-071; those of every app-level token;
// and those of a token answered as a person's.
const (
	DefaultScopes  = "chat:write,im:write,channels:read,groups:read,users:read,users:read.email"
	AppTokenScopes = "connections:write"
	UserScopes     = "chat:write,users:read"
)

// The person whose token a token in user_tokens stands for.
const (
	UserID   = "U0MUSTERUSR1"
	UserName = "alice"
)

// App is one Slack app of the fake: its key, its id, the workspace it is installed in with its address, its bot and
// its published tokens and Slack signing secret.
type App struct {
	Key           string
	AppID         string
	TeamID        string
	TeamName      string
	TeamURL       string
	BotID         string
	BotUserID     string
	BotName       string
	BotToken      string
	AppToken      string
	SigningSecret string
}

var apps = []App{
	{Key: AppA, AppID: "A0MUSTERDEVA", TeamID: "T0MUSTERDEVA", TeamName: "Fake workspace",
		TeamURL: "https://fake-workspace.slack.com/", BotID: "B0MUSTERDEVA", BotUserID: "U0MUSTERBOTA",
		BotName: "muster-dev-bot", BotToken: BotToken, AppToken: AppToken, SigningSecret: SigningSecret},
	{Key: AppB, AppID: "A0MUSTERDEVB", TeamID: "T0MUSTERDEVB", TeamName: "Fake workspace B",
		TeamURL: "https://fake-workspace-b.slack.com/", BotID: "B0MUSTERDEVB", BotUserID: "U0MUSTERBOTB",
		BotName: "muster-dev-bot-b", BotToken: SecondBotToken, AppToken: SecondAppToken,
		SigningSecret: SecondSigningSecret},
}

// Apps are the two apps, a first.
func Apps() []App { return slices.Clone(apps) }

// appByKey is the app of the key, false for another key.
func appByKey(key string) (App, bool) {
	i := slices.IndexFunc(apps, func(a App) bool { return a.Key == key })
	if i < 0 {
		return App{}, false
	}
	return apps[i], true
}

// PublishedBotToken reports whether token is the published bot token of one of the apps.
func PublishedBotToken(token string) bool {
	return slices.ContainsFunc(apps, func(a App) bool { return a.BotToken == token })
}

// Config is the configuration of the fake, as GET and PUT /_fake/config show it. Scopes are the scopes of each app's
// bot token, by app key, comma-separated as x-oauth-scopes names them. Revoked tokens are answered token_revoked.
// UserTokens are answered as tokens of the person alice: auth.test names no bot_id. AppOf names the app of a token by
// its key, over the published pairing; a token it names that the fake did not publish is an app-level token when it
// starts with xapp- and a bot token otherwise.
type Config struct {
	Scopes     map[string]string `json:"scopes"`
	Revoked    []string          `json:"revoked"`
	UserTokens []string          `json:"user_tokens"`
	AppOf      map[string]string `json:"app_of"`
}

// configPatch is a PUT of /_fake/config: each field given replaces the whole value, the others are kept, except
// scopes, whose entries are set one by one: a text sets an app's scopes and null restores DefaultScopes.
type configPatch struct {
	Scopes     map[string]*string `json:"scopes"`
	Revoked    *[]string          `json:"revoked"`
	UserTokens *[]string          `json:"user_tokens"`
	AppOf      *map[string]string `json:"app_of"`
}

// DefaultConfig is the configuration of a new fake: both apps with DefaultScopes, nothing revoked, no person's token
// and the published pairing of tokens and apps.
func DefaultConfig() Config {
	return Config{Scopes: map[string]string{AppA: DefaultScopes, AppB: DefaultScopes}, Revoked: []string{},
		UserTokens: []string{}, AppOf: map[string]string{}}
}

// Fake is the fake Slack Web API server.
type Fake struct {
	*fakeserver.Server

	mu     sync.Mutex
	config Config
}

// New returns the fake with DefaultConfig.
func New() *Fake {
	f := &Fake{config: DefaultConfig()}
	f.Server = fakeserver.New("Slack", http.HandlerFunc(f.serve))
	f.HandleControl("GET /_fake/config", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Config())
	})
	f.HandleControl("PUT /_fake/config", f.putConfig)
	return f
}

// Config is the configuration in force.
func (f *Fake) Config() Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clone(f.config)
}

// SetConfig replaces the whole configuration; it fails on an app key other than a and b, and an app without scopes
// gets DefaultScopes.
func (f *Fake) SetConfig(c Config) error {
	c = clone(c)
	for _, a := range apps {
		if _, ok := c.Scopes[a.Key]; !ok {
			c.Scopes[a.Key] = DefaultScopes
		}
	}
	if err := check(c); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.config = c
	return nil
}

// check refuses a configuration that names an app the fake does not have.
func check(c Config) error {
	for key := range c.Scopes {
		if _, ok := appByKey(key); !ok {
			return fmt.Errorf("scopes: the app %q is not a or b", key)
		}
	}
	for token, key := range c.AppOf {
		if _, ok := appByKey(key); !ok {
			return fmt.Errorf("app_of: the app %q of the token %q is not a or b", key, token)
		}
	}
	return nil
}

// clone copies c, with empty lists and maps rather than none.
func clone(c Config) Config {
	out := Config{Scopes: maps.Clone(c.Scopes), Revoked: slices.Clone(c.Revoked),
		UserTokens: slices.Clone(c.UserTokens), AppOf: maps.Clone(c.AppOf)}
	if out.Scopes == nil {
		out.Scopes = map[string]string{}
	}
	if out.Revoked == nil {
		out.Revoked = []string{}
	}
	if out.UserTokens == nil {
		out.UserTokens = []string{}
	}
	if out.AppOf == nil {
		out.AppOf = map[string]string{}
	}
	return out
}

func (f *Fake) putConfig(w http.ResponseWriter, r *http.Request) {
	var p configPatch
	if err := fakeserver.DecodeJSON(w, r, &p); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.mu.Lock()
	c := clone(f.config)
	for key, scopes := range p.Scopes {
		if scopes == nil {
			c.Scopes[key] = DefaultScopes
			continue
		}
		c.Scopes[key] = *scopes
	}
	if p.Revoked != nil {
		c.Revoked = *p.Revoked
	}
	if p.UserTokens != nil {
		c.UserTokens = *p.UserTokens
	}
	if p.AppOf != nil {
		c.AppOf = *p.AppOf
	}
	c = clone(c)
	err := check(c)
	if err == nil {
		f.config = c
	}
	f.mu.Unlock()
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, f.Config())
}

// The kinds of tokens the fake tells apart.
const (
	kindBot    = "bot"
	kindApp    = "app"
	kindPerson = "person"
)

// caller is the token of a request as the fake knows it: its app and its kind.
type caller struct {
	app  App
	kind string
}

// scopes are the scopes of the caller's token, as x-oauth-scopes names them.
func (c caller) scopes(cfg Config) string {
	switch c.kind {
	case kindApp:
		return AppTokenScopes
	case kindPerson:
		return UserScopes
	}
	return cfg.Scopes[c.app.Key]
}

// callerOf is the caller of token under cfg; false for a token the fake does not know.
func callerOf(cfg Config, token string) (caller, bool) {
	var app App
	kind := kindBot
	published := slices.IndexFunc(apps, func(a App) bool { return a.BotToken == token || a.AppToken == token })
	if published >= 0 {
		app = apps[published]
		if app.AppToken == token {
			kind = kindApp
		}
	}
	if key, ok := cfg.AppOf[token]; ok {
		app, _ = appByKey(key)
		if published < 0 && strings.HasPrefix(token, "xapp-") {
			kind = kindApp
		}
	} else if published < 0 {
		return caller{}, false
	}
	if slices.Contains(cfg.UserTokens, token) {
		kind = kindPerson
	}
	return caller{app: app, kind: kind}, true
}

// acceptedScopes are the scopes each method of the fake accepts, as x-accepted-oauth-scopes names them.
var acceptedScopes = map[string]string{
	"auth.test":             "",
	"bots.info":             "users:read",
	"apps.connections.open": "connections:write",
}

// serve answers `/api/<method>` as Slack does; any other path is 404.
func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	method, ok := strings.CutPrefix(r.URL.Path, "/api/")
	if !ok || method == "" || strings.Contains(method, "/") {
		http.NotFound(w, r)
		return
	}
	accepted, known := acceptedScopes[method]
	w.Header().Set("X-Accepted-OAuth-Scopes", accepted)
	if !known {
		writeError(w, "unknown_method")
		return
	}
	args, code := argsOf(r)
	if code != "" {
		writeError(w, code)
		return
	}
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		writeError(w, "not_authed")
		return
	}
	token = strings.TrimSpace(token)
	cfg := f.Config()
	if slices.Contains(cfg.Revoked, token) {
		writeError(w, "token_revoked")
		return
	}
	c, ok := callerOf(cfg, token)
	if !ok {
		writeError(w, "invalid_auth")
		return
	}
	scopes := c.scopes(cfg)
	w.Header().Set("X-OAuth-Scopes", scopes)
	switch method {
	case "auth.test":
		authTest(w, c)
	case "bots.info":
		botsInfo(w, c, scopes, args["bot"])
	case "apps.connections.open":
		f.connectionsOpen(w, c)
	}
}

// authTest answers auth.test (F-072): the bot and its workspace for a bot token, the person for a person's token, and
// the app's id alone for an app-level token.
func authTest(w http.ResponseWriter, c caller) {
	switch c.kind {
	case kindApp:
		writeOK(w, map[string]any{"app_id": c.app.AppID})
	case kindPerson:
		writeOK(w, map[string]any{"url": c.app.TeamURL, "team": c.app.TeamName, "user": UserName,
			"team_id": c.app.TeamID, "user_id": UserID, "is_enterprise_install": false})
	default:
		writeOK(w, map[string]any{"url": c.app.TeamURL, "team": c.app.TeamName, "user": c.app.BotName,
			"team_id": c.app.TeamID, "user_id": c.app.BotUserID, "bot_id": c.app.BotID, "is_enterprise_install": false})
	}
}

// botsInfo answers bots.info for the bot id: the bot of the caller's app; an app-level token is the wrong kind, a
// token without users:read lacks the scope, and any other id is bot_not_found.
func botsInfo(w http.ResponseWriter, c caller, scopes, botID string) {
	switch {
	case c.kind == kindApp:
		writeError(w, "not_allowed_token_type")
	case !slices.Contains(splitScopes(scopes), "users:read"):
		writeJSON(w, map[string]any{"ok": false, "error": "missing_scope", "needed": "users:read",
			"provided": scopes})
	case botID != c.app.BotID:
		writeError(w, "bot_not_found")
	default:
		writeOK(w, map[string]any{"bot": map[string]any{"id": c.app.BotID, "deleted": false,
			"name": c.app.BotName, "updated": 1760000000, "app_id": c.app.AppID, "user_id": c.app.BotUserID,
			"icons": map[string]any{}}})
	}
}

// connectionsOpen answers apps.connections.open (F-100) for an app-level token: a Socket Mode address on the fake's own
// listen address with a new single-use ticket; any other token is the wrong kind.
func (f *Fake) connectionsOpen(w http.ResponseWriter, c caller) {
	if c.kind != kindApp {
		writeError(w, "not_allowed_token_type")
		return
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	host := strings.TrimPrefix(f.URL(), "http://")
	writeOK(w, map[string]any{"url": "ws://" + host + "/link/?ticket=" + hex.EncodeToString(b[:]) + "&app_id=" +
		c.app.AppID})
}

// argsOf are the arguments of a request: its query and form, or its query and the members of its JSON body that are
// text; a body that cannot be read is answered with the error code it returns, invalid_json or invalid_form_data.
func argsOf(r *http.Request) (map[string]string, string) {
	out := map[string]string{}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "application/json" {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, "invalid_json"
		}
		for k, v := range body {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		for k, v := range r.URL.Query() {
			out[k] = v[0]
		}
		return out, ""
	}
	if r.ParseForm() != nil {
		return nil, "invalid_form_data"
	}
	for k, v := range r.Form {
		out[k] = v[0]
	}
	return out, ""
}

// splitScopes is a list of scopes as x-oauth-scopes names them.
func splitScopes(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func writeOK(w http.ResponseWriter, fields map[string]any) {
	fields["ok"] = true
	writeJSON(w, fields)
}

func writeError(w http.ResponseWriter, code string) {
	writeJSON(w, map[string]any{"ok": false, "error": code})
}

// writeJSON answers v as Slack answers every call, success or error: 200 with JSON.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}
