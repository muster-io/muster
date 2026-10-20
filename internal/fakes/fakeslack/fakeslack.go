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
// Socket Mode address on the fake with a new single-use ticket for an app-level token (socket.go); conversations.info
// answers a channel of the token's app and conversations.list its channels page by page (F-098, channels.go);
// chat.postMessage, chat.update, chat.delete, chat.postEphemeral and chat.getPermalink keep each channel's messages
// and Threads by the rules of F-074 to F-092 that Muster relies on (messages.go); chat.postEphemeral shows a message
// only to one of the fake's people — UserAlice, UserBob, UserCarol, UserDora and the person UserID, members of every
// channel the bot is in — and answers user_not_in_channel for anyone else. Each app's workspace has a directory of
// people (people.go, F-095), which users.list answers over three pages with next_cursor, users.info by user id and
// users.lookupByEmail by an address in any letter case: Slackbot, two accounts whose display name is "Sam", a bot
// whose display name is "Bob", the app's bot user, a deleted account whose display name is "bob", and UserAlice,
// UserBob, UserCarol and UserDora; conversations.open answers the bot's direct chat with one of them, and
// chat.postMessage to a person's user id posts into that direct chat (F-096), cannot_dm_bot for a bot and
// user_not_found for a deleted account. Any other method is unknown_method. A POST
// to the response_url of a press, /actions/<team>/<n>/<hex>, takes an answer, five at most (presses.go); /link/ is the
// Socket Mode WebSocket of an address apps.connections.open answered (socket.go); any other path is 404.
//
// The control endpoints under /_fake/ come from the fake-server harness (requests; faults, such as an error answer, a
// 429 with Retry-After or a delay) and add GET and PUT /_fake/config: the scopes of each app's bot token, the revoked
// tokens, the tokens answered as a person's and the app of each token, over the published pairing; the channels of
// each app, by id, with their name, the bot's membership and whether they are archived or private; a page size that
// makes conversations.list answer smaller pages; the request_url of each app, the Interactivity Request URL its
// presses go to; the extra_connections of each app, the connections of other programs that the hello of its
// sockets counts; and messages_tab_off, the apps whose Messages tab is off, whose bot's direct messages to a person
// answer messages_tab_disabled. GET /_fake/messages lists the messages of a channel, with their Thread replies, and POST
// /_fake/delete deletes one as a person would (messages.go); GET /_fake/dms?user=U…[&app=a|b] lists the direct
// messages the bot sent to a person (people.go). POST /_fake/press presses a button of a message as one of
// the people and sends the app's request_url the payload signed with the app's Slack signing secret and with its
// verification token, answering Muster's status and body, or sends it as an interactive envelope through a socket of
// the app, answering whether Muster confirmed it; GET /_fake/ephemeral lists what chat.postEphemeral showed and GET
// /_fake/responses what the response_url of presses took (presses.go). GET /_fake/socket shows the sockets of each app
// and the envelopes sent, and POST /_fake/socket sends a socket disconnect or an envelope, ends a socket, or arms the
// next sockets to get no hello or too_many_websockets (socket.go).
package fakeslack

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
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
// its published tokens, Slack signing secret and verification token.
type App struct {
	Key               string
	AppID             string
	TeamID            string
	TeamName          string
	TeamURL           string
	BotID             string
	BotUserID         string
	BotName           string
	BotToken          string
	AppToken          string
	SigningSecret     string
	VerificationToken string
}

var apps = []App{
	{Key: AppA, AppID: "A0MUSTERDEVA", TeamID: "T0MUSTERDEVA", TeamName: "Fake workspace",
		TeamURL: "https://fake-workspace.slack.com/", BotID: "B0MUSTERDEVA", BotUserID: "U0MUSTERBOTA",
		BotName: "muster-dev-bot", BotToken: BotToken, AppToken: AppToken, SigningSecret: SigningSecret,
		VerificationToken: VerificationToken},
	{Key: AppB, AppID: "A0MUSTERDEVB", TeamID: "T0MUSTERDEVB", TeamName: "Fake workspace B",
		TeamURL: "https://fake-workspace-b.slack.com/", BotID: "B0MUSTERDEVB", BotUserID: "U0MUSTERBOTB",
		BotName: "muster-dev-bot-b", BotToken: SecondBotToken, AppToken: SecondAppToken,
		SigningSecret: SecondSigningSecret, VerificationToken: SecondVerificationToken},
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
// starts with xapp- and a bot token otherwise. Channels are the channels of each app's workspace, by app key and
// channel id. PageSize, when positive, is the most channels a page of conversations.list holds. RequestURL is the
// Interactivity Request URL of each app, by app key, where /_fake/press sends its presses. ExtraConnections are the
// connections of other programs to each app, by app key, which the hello of its sockets counts beside the fake's own
// (F-102). MessagesTabOff are the keys of the apps whose Messages tab is off: chat.postMessage to a person's user id
// answers messages_tab_disabled, as Slack's reference lists it for chat.postMessage (not seen on the stand).
type Config struct {
	Scopes           map[string]string             `json:"scopes"`
	Revoked          []string                      `json:"revoked"`
	UserTokens       []string                      `json:"user_tokens"`
	AppOf            map[string]string             `json:"app_of"`
	Channels         map[string]map[string]Channel `json:"channels"`
	PageSize         int                           `json:"page_size"`
	RequestURL       map[string]string             `json:"request_url"`
	ExtraConnections map[string]int                `json:"extra_connections"`
	MessagesTabOff   []string                      `json:"messages_tab_off"`
}

// configPatch is a PUT of /_fake/config: each field given replaces the whole value, the others are kept, except
// scopes, whose entries are set one by one: a text sets an app's scopes and null restores DefaultScopes; and channels,
// merged by id within each app: a channel replaces the one of its id, null deletes it, and an app given as null gets
// its default channels again; and request_url and extra_connections, set app by app: a value sets an app's, null
// removes it, as does an extra_connections of 0.
type configPatch struct {
	Scopes           map[string]*string             `json:"scopes"`
	Revoked          *[]string                      `json:"revoked"`
	UserTokens       *[]string                      `json:"user_tokens"`
	AppOf            *map[string]string             `json:"app_of"`
	Channels         map[string]map[string]*Channel `json:"channels"`
	PageSize         *int                           `json:"page_size"`
	RequestURL       map[string]*string             `json:"request_url"`
	ExtraConnections map[string]*int                `json:"extra_connections"`
	MessagesTabOff   *[]string                      `json:"messages_tab_off"`
}

// DefaultConfig is the configuration of a new fake: both apps with DefaultScopes and the default channels, nothing
// revoked, no person's token, the published pairing of tokens and apps, pages of the size conversations.list asks, no
// request_url and no other program's connections.
func DefaultConfig() Config {
	return Config{Scopes: map[string]string{AppA: DefaultScopes, AppB: DefaultScopes}, Revoked: []string{},
		UserTokens: []string{}, AppOf: map[string]string{},
		Channels:   map[string]map[string]Channel{AppA: defaultChannels(), AppB: defaultChannels()},
		RequestURL: map[string]string{}, ExtraConnections: map[string]int{}, MessagesTabOff: []string{}}
}

// Fake is the fake Slack Web API server.
type Fake struct {
	*fakeserver.Server

	mu     sync.Mutex
	config Config
	// chats are the messages of each channel of each app, the roots in the order they were posted, and seq the
	// counter every `ts` comes from.
	chats map[chatKey][]*stored
	seq   int64
	// ephemeral are what chat.postEphemeral showed, responses what the response_url of presses took, slots those
	// response_url by path, and pressSeq the counter of presses.
	ephemeral []Ephemeral
	responses []Response
	slots     map[string]*responseSlot
	pressSeq  int64
	// tickets are the tickets apps.connections.open issued that no socket used yet, with their app's key; sockets the
	// open sockets by id, socketSeq the counter of their ids and appSockets the Socket Mode state of each app by key;
	// envelopes the envelopes sent, in order, and envelopeIDs the same by envelope_id (socket.go).
	tickets     map[string]string
	sockets     map[int64]*fakeSocket
	socketSeq   int64
	appSockets  map[string]*appSocket
	envelopes   []*envelopeState
	envelopeIDs map[string]*envelopeState
}

// New returns the fake with DefaultConfig.
func New() *Fake {
	f := &Fake{config: DefaultConfig(), chats: map[chatKey][]*stored{}, ephemeral: []Ephemeral{},
		responses: []Response{}, slots: map[string]*responseSlot{}, tickets: map[string]string{},
		sockets: map[int64]*fakeSocket{}, appSockets: map[string]*appSocket{}, envelopeIDs: map[string]*envelopeState{}}
	f.Server = fakeserver.New("Slack", http.HandlerFunc(f.serve))
	f.HandleControl("GET /_fake/config", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Config())
	})
	f.HandleControl("PUT /_fake/config", f.putConfig)
	f.HandleControl("GET /_fake/messages", f.listMessages)
	f.HandleControl("GET /_fake/dms", f.listDMs)
	f.HandleControl("POST /_fake/delete", f.deleteByPerson)
	f.HandleControl("POST /_fake/press", f.press)
	f.HandleControl("GET /_fake/ephemeral", f.listEphemeral)
	f.HandleControl("GET /_fake/responses", f.listResponses)
	f.HandleControl("GET /_fake/socket", f.getSocket)
	f.HandleControl("POST /_fake/socket", f.postSocket)
	return f
}

// Config is the configuration in force.
func (f *Fake) Config() Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clone(f.config)
}

// SetConfig replaces the whole configuration; it fails on an app key other than a and b, an empty channel id, a
// negative page size or number of extra connections and a request_url that is not an absolute http or https URL, and
// an app without scopes gets DefaultScopes, one without channels the default channels.
func (f *Fake) SetConfig(c Config) error {
	c = clone(c)
	for _, a := range apps {
		if _, ok := c.Scopes[a.Key]; !ok {
			c.Scopes[a.Key] = DefaultScopes
		}
		if _, ok := c.Channels[a.Key]; !ok {
			c.Channels[a.Key] = defaultChannels()
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

// check refuses a configuration that names an app the fake does not have, a channel without an id, a negative page
// size or number of extra connections, or a request_url that is not an absolute http or https URL.
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
	for key, chans := range c.Channels {
		if _, ok := appByKey(key); !ok {
			return fmt.Errorf("channels: the app %q is not a or b", key)
		}
		if _, ok := chans[""]; ok {
			return fmt.Errorf("channels: a channel of the app %q has no id", key)
		}
	}
	if c.PageSize < 0 {
		return fmt.Errorf("page_size: %d is negative", c.PageSize)
	}
	for key, target := range c.RequestURL {
		if _, ok := appByKey(key); !ok {
			return fmt.Errorf("request_url: the app %q is not a or b", key)
		}
		if u, err := url.Parse(target); err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("request_url: %q of the app %q is not an absolute http or https URL", target, key)
		}
	}
	for key, n := range c.ExtraConnections {
		if _, ok := appByKey(key); !ok {
			return fmt.Errorf("extra_connections: the app %q is not a or b", key)
		}
		if n < 0 {
			return fmt.Errorf("extra_connections: %d of the app %q is negative", n, key)
		}
	}
	for _, key := range c.MessagesTabOff {
		if _, ok := appByKey(key); !ok {
			return fmt.Errorf("messages_tab_off: the app %q is not a or b", key)
		}
	}
	return nil
}

// clone copies c, with empty lists and maps rather than none.
func clone(c Config) Config {
	out := Config{Scopes: maps.Clone(c.Scopes), Revoked: slices.Clone(c.Revoked),
		UserTokens: slices.Clone(c.UserTokens), AppOf: maps.Clone(c.AppOf), Channels: cloneChannels(c.Channels),
		PageSize: c.PageSize, RequestURL: maps.Clone(c.RequestURL), ExtraConnections: maps.Clone(c.ExtraConnections),
		MessagesTabOff: slices.Clone(c.MessagesTabOff)}
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
	if out.RequestURL == nil {
		out.RequestURL = map[string]string{}
	}
	if out.ExtraConnections == nil {
		out.ExtraConnections = map[string]int{}
	}
	if out.MessagesTabOff == nil {
		out.MessagesTabOff = []string{}
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
	for key, chans := range p.Channels {
		if chans == nil {
			c.Channels[key] = defaultChannels()
			continue
		}
		if c.Channels[key] == nil {
			c.Channels[key] = map[string]Channel{}
		}
		for id, ch := range chans {
			if ch == nil {
				delete(c.Channels[key], id)
				continue
			}
			c.Channels[key][id] = *ch
		}
	}
	if p.PageSize != nil {
		c.PageSize = *p.PageSize
	}
	for key, target := range p.RequestURL {
		if target == nil {
			delete(c.RequestURL, key)
			continue
		}
		c.RequestURL[key] = *target
	}
	for key, n := range p.ExtraConnections {
		if n == nil || *n == 0 {
			delete(c.ExtraConnections, key)
			continue
		}
		c.ExtraConnections[key] = *n
	}
	if p.MessagesTabOff != nil {
		c.MessagesTabOff = *p.MessagesTabOff
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
	"conversations.info":    conversationScopes,
	"conversations.list":    conversationScopes,
	"chat.postMessage":      chatScope,
	"chat.update":           chatScope,
	"chat.delete":           chatScope,
	"chat.postEphemeral":    chatScope,
	"chat.getPermalink":     "",
	"users.list":            usersScope,
	"users.info":            usersScope,
	"users.lookupByEmail":   emailScope,
	"conversations.open":    openScopes,
}

// conversationScopes are the scopes the methods that read conversations accept (F-071).
const conversationScopes = "channels:read,groups:read,mpim:read,im:read,read"

// serve answers `/api/<method>` as Slack does, the response_url of presses and the Socket Mode WebSocket; any other
// path is 404.
func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, actionsPrefix) {
		f.respond(w, r)
		return
	}
	if r.URL.Path == linkPath {
		f.link(w, r)
		return
	}
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
	args, raw, code := argsOf(r)
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
	case "conversations.info":
		conversationsInfo(w, c, cfg, scopes, args["channel"])
	case "conversations.list":
		conversationsList(w, c, cfg, scopes, args)
	case "users.list":
		usersList(w, c, scopes, args)
	case "users.info":
		usersInfo(w, c, scopes, args["user"])
	case "users.lookupByEmail":
		usersLookupByEmail(w, c, scopes, args["email"])
	case "conversations.open":
		conversationsOpen(w, c, scopes, args["users"])
	case "chat.postMessage":
		if channel, _ := chatArgs(raw).text("channel"); directTarget(channel) {
			f.postDirect(w, c, scopes, raw)
			return
		}
		f.postMessage(w, c, cfg, scopes, raw)
	case "chat.update":
		f.updateMessage(w, c, cfg, scopes, raw)
	case "chat.delete":
		f.deleteMessage(w, c, cfg, scopes, raw)
	case "chat.postEphemeral":
		f.postEphemeral(w, c, cfg, scopes, raw)
	case "chat.getPermalink":
		f.getPermalink(w, c, cfg, raw)
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
// listen address with a new single-use ticket, kept for the token's app; any other token is the wrong kind.
func (f *Fake) connectionsOpen(w http.ResponseWriter, c caller) {
	if c.kind != kindApp {
		writeError(w, "not_allowed_token_type")
		return
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	ticket := hex.EncodeToString(b[:])
	host := strings.TrimPrefix(f.URL(), "http://")
	f.mu.Lock()
	f.tickets[ticket] = c.app.Key
	f.mu.Unlock()
	writeOK(w, map[string]any{"url": "ws://" + host + linkPath + "?ticket=" + ticket + "&app_id=" + c.app.AppID})
}

// maxBody is the largest JSON body the fake reads.
const maxBody = 1 << 20

// argsOf are the arguments of a request: its query and form, or its query and the members of its JSON body, read
// whole once, each as text where it is one, and raw, each as JSON — a form value as a JSON text, except blocks and
// attachments, which a form carries as JSON; a body that cannot be read is answered with the error code it returns,
// invalid_json or invalid_form_data.
func argsOf(r *http.Request) (map[string]string, map[string]json.RawMessage, string) {
	out, raw := map[string]string{}, map[string]json.RawMessage{}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "application/json" {
		data, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil || json.Unmarshal(data, &raw) != nil {
			return nil, nil, "invalid_json"
		}
		if raw == nil {
			raw = map[string]json.RawMessage{}
		}
		for k, v := range raw {
			var s string
			if json.Unmarshal(v, &s) == nil {
				out[k] = s
			}
		}
		for k, v := range r.URL.Query() {
			out[k] = v[0]
			if _, ok := raw[k]; !ok {
				raw[k] = formRaw(k, v[0])
			}
		}
		return out, raw, ""
	}
	if r.ParseForm() != nil {
		return nil, nil, "invalid_form_data"
	}
	for k, v := range r.Form {
		out[k] = v[0]
		raw[k] = formRaw(k, v[0])
	}
	return out, raw, ""
}

// formRaw is a form value as JSON: blocks and attachments as the JSON they carry, anything else as a JSON text.
func formRaw(k, v string) json.RawMessage {
	if (k == "blocks" || k == "attachments") && json.Valid([]byte(v)) {
		return json.RawMessage(v)
	}
	b, _ := json.Marshal(v) // a string always encodes
	return b
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
