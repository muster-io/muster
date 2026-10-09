// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package faketelegram is the fake Telegram Bot API server. It answers `<prefix>/bot<token>/<method>`, GET or POST,
// like the Bot API: getMe answers its bot for any token but the dry probe's `0:x` and the revoked ones, which get 401;
// getUpdates long-polls a queue of updates per bot, and a second poller on the same token ends the first with 409
// (F-018); setWebhook, deleteWebhook and getWebhookInfo keep a webhook per bot, to which the queued updates are posted
// with the secret token header. getChat, getChatMember, sendMessage and editMessageText work on the chats of chats.go:
// two channels and a discussion group, the bot's rights in them, the messages the bot sent with their keyboards and
// edits, the budget of sends and edits per chat and the notifications of two accounts. Every other method is recorded
// and answered 501. The control endpoints under /_fake/ change the configuration (a path prefix, an HTML mode that
// stands for a web server that is not a Bot API, revoked tokens), queue updates, end a running long poll with 409, and
// change and list the chats, the bot's rights, the messages and the notifications.
package faketelegram

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The bot of every token the fake accepts.
const (
	BotID       = 123456
	BotUsername = "muster_dev_bot"
)

// DryProbeToken is the token of the dry probe: getMe with it is always 401, as on the Bot API.
const DryProbeToken = "0:x"

// The modes of the fake: the Bot API, or an HTML page answered 200 to every request.
const (
	ModeBotAPI = "bot_api"
	ModeHTML   = "html"
)

// HTMLPage is what the fake answers in ModeHTML, as a web server that is not a Bot API would.
const HTMLPage = "<!doctype html><html><head><title>Welcome</title></head><body><h1>It works!</h1></body></html>\n"

const (
	maxBody = 4 << 20
	// webhookTimeout bounds each post of an update to a webhook.
	webhookTimeout = 5 * time.Second
)

// Config is the configuration of the fake, as GET and PUT /_fake/config show it.
type Config struct {
	// PathPrefix, such as /k3x9, is the only path the fake answers the Bot API under; every other path gets 404.
	PathPrefix string `json:"path_prefix"`
	// Mode is ModeBotAPI or ModeHTML.
	Mode string `json:"mode"`
	// RevokedTokens are refused with 401 on every method.
	RevokedTokens []string `json:"revoked_tokens"`
}

type configPatch struct {
	PathPrefix    *string   `json:"path_prefix"`
	Mode          *string   `json:"mode"`
	RevokedTokens *[]string `json:"revoked_tokens"`
}

// Fake is the fake Telegram Bot API server.
type Fake struct {
	*fakeserver.Server

	client *http.Client

	mu     sync.Mutex
	config Config
	bots   map[string]*bot

	chats *chats
}

// New returns the fake with no prefix, in the Bot API mode.
func New() *Fake {
	f := &Fake{config: Config{Mode: ModeBotAPI, RevokedTokens: []string{}}, bots: map[string]*bot{}, chats: newChats(),
		client: &http.Client{Timeout: webhookTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	f.Server = fakeserver.New("Telegram", http.HandlerFunc(f.serve))
	f.HandleControl("GET /_fake/config", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Config())
	})
	f.HandleControl("PUT /_fake/config", f.putConfig)
	f.HandleControl("POST /_fake/updates", f.postUpdate)
	f.HandleControl("POST /_fake/conflict", f.postConflict)
	f.HandleControl("GET /_fake/webhooks", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Webhooks())
	})
	f.handleChats()
	return f
}

// Config is the configuration in force.
func (f *Fake) Config() Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.config
	c.RevokedTokens = slices.Clone(c.RevokedTokens)
	return c
}

// SetConfig replaces the fields of the configuration that p sets; it fails on an unknown mode or a prefix that is not a
// path.
func (f *Fake) SetConfig(prefix, mode *string, revoked *[]string) error {
	if mode != nil && *mode != ModeBotAPI && *mode != ModeHTML {
		return errors.New("mode is bot_api or html")
	}
	var p string
	if prefix != nil {
		p = strings.TrimSuffix(strings.TrimSpace(*prefix), "/")
		if p != "" && (!strings.HasPrefix(p, "/") || strings.Contains(p, "?") || strings.Contains(p, "#")) {
			return errors.New("path_prefix is empty or a path that starts with /")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if prefix != nil {
		f.config.PathPrefix = p
	}
	if mode != nil {
		f.config.Mode = *mode
	}
	if revoked != nil {
		f.config.RevokedTokens = slices.Clone(*revoked)
	}
	return nil
}

func (f *Fake) putConfig(w http.ResponseWriter, r *http.Request) {
	var p configPatch
	if err := fakeserver.DecodeJSON(w, r, &p); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := f.SetConfig(p.PathPrefix, p.Mode, p.RevokedTokens); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, f.Config())
}

// User is the bot as getMe answers it.
type User struct {
	ID                      int64  `json:"id"`
	IsBot                   bool   `json:"is_bot"`
	FirstName               string `json:"first_name"`
	Username                string `json:"username"`
	CanJoinGroups           bool   `json:"can_join_groups"`
	CanReadAllGroupMessages bool   `json:"can_read_all_group_messages"`
	SupportsInlineQueries   bool   `json:"supports_inline_queries"`
}

// answer is the envelope of every Bot API answer.
type answer struct {
	OK          bool        `json:"ok"`
	Result      any         `json:"result,omitempty"`
	ErrorCode   int         `json:"error_code,omitempty"`
	Description string      `json:"description,omitempty"`
	Parameters  *parameters `json:"parameters,omitempty"`
}

type parameters struct {
	RetryAfter int `json:"retry_after,omitempty"`
}

func writeOK(w http.ResponseWriter, result any) {
	fakeserver.WriteJSON(w, http.StatusOK, answer{OK: true, Result: result})
}

func writeFailure(w http.ResponseWriter, code int, description string) {
	fakeserver.WriteJSON(w, code, answer{ErrorCode: code, Description: description})
}

// The descriptions of the Bot API errors the fake answers.
const (
	descriptionNotFound      = "Not Found"
	descriptionUnauthorized  = "Unauthorized"
	descriptionTerminated    = "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"
	descriptionWebhookActive = "Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first"
	descriptionSetWebhook    = "Conflict: terminated by setWebhook request"
)

// serve answers <prefix>/bot<token>/<method>: method names are compared case-insensitively.
func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	cfg := f.Config()
	if cfg.Mode == ModeHTML {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, HTMLPage)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, cfg.PathPrefix+"/bot")
	token, method, found := strings.Cut(rest, "/")
	if !ok || !found || token == "" || method == "" || strings.Contains(method, "/") ||
		(r.Method != http.MethodGet && r.Method != http.MethodPost) {
		writeFailure(w, http.StatusNotFound, descriptionNotFound)
		return
	}
	if token == DryProbeToken || slices.Contains(cfg.RevokedTokens, token) {
		writeFailure(w, http.StatusUnauthorized, descriptionUnauthorized)
		return
	}
	p, err := readParams(r)
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	switch strings.ToLower(method) {
	case "getme":
		writeOK(w, User{ID: BotID, IsBot: true, FirstName: "Muster Dev", Username: BotUsername, CanJoinGroups: true})
	case "getupdates":
		f.getUpdates(w, r, token, p)
	case "setwebhook":
		f.setWebhook(r.Context(), w, token, p)
	case "deletewebhook":
		f.deleteWebhook(w, token, p)
	case "getwebhookinfo":
		writeOK(w, f.webhookInfo(token))
	case "getchat":
		f.getChat(w, p)
	case "getchatmember":
		f.getChatMember(w, p)
	case "sendmessage":
		f.sendMessage(w, p)
	case "editmessagetext":
		f.editMessageText(w, p)
	default:
		writeFailure(w, http.StatusNotImplemented,
			"Not Implemented: method "+method+" is not implemented by the fake Telegram server")
	}
}

// params are the parameters of a call, from the query string and from a JSON or form body; a JSON body wins.
type params map[string]any

func readParams(r *http.Request) (params, error) {
	p := params{}
	for k, v := range r.URL.Query() {
		p[k] = v[len(v)-1]
	}
	if r.Method != http.MethodPost {
		return p, nil
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, errors.New("the body cannot be read")
	}
	switch {
	case len(body) == 0:
	case ct == "application/json":
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return nil, errors.New("the body is not a JSON object")
		}
		for k, v := range m {
			p[k] = v
		}
	case ct == "application/x-www-form-urlencoded":
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, errors.New("the body is not a form")
		}
		for k, v := range form {
			p[k] = v[len(v)-1]
		}
	}
	return p, nil
}

// int reads an integer parameter; ok is false when it is missing, and err set when it is not an integer.
func (p params) int(name string) (n int64, ok bool, err error) {
	v, present := p[name]
	if !present {
		return 0, false, nil
	}
	var s string
	switch x := v.(type) {
	case json.Number:
		s = x.String()
	case string:
		s = x
	default:
		return 0, true, errors.New("field \"" + name + "\" must be an integer")
	}
	n, err = strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, true, errors.New("field \"" + name + "\" must be an integer")
	}
	return n, true, nil
}

func (p params) string(name string) string {
	if s, ok := p[name].(string); ok {
		return s
	}
	return ""
}

// list reads a list of strings, given as a JSON array or as the JSON text of one; ok is false when it is missing.
func (p params) list(name string) (l []string, ok bool, err error) {
	v, present := p[name]
	if !present {
		return nil, false, nil
	}
	if s, isString := v.(string); isString {
		if err := json.Unmarshal([]byte(s), &l); err != nil {
			return nil, true, errors.New("field \"" + name + "\" must be a JSON-serialized array of strings")
		}
		return l, true, nil
	}
	items, isList := v.([]any)
	if !isList {
		return nil, true, errors.New("field \"" + name + "\" must be an array of strings")
	}
	for _, it := range items {
		s, isString := it.(string)
		if !isString {
			return nil, true, errors.New("field \"" + name + "\" must be an array of strings")
		}
		l = append(l, s)
	}
	return l, true, nil
}

// bool reads a boolean parameter, false when it is missing.
func (p params) bool(name string) bool {
	switch v := p[name].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	}
	return false
}

type updateRequest struct {
	Token  string          `json:"token"`
	Update json.RawMessage `json:"update"`
}

// postUpdate queues a raw update for a bot: {token, update}. An update without update_id gets the next one.
func (f *Fake) postUpdate(w http.ResponseWriter, r *http.Request) {
	var req updateRequest
	if err := fakeserver.DecodeJSON(w, r, &req); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := f.Enqueue(r.Context(), req.Token, req.Update)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, map[string]int64{"update_id": id})
}

type conflictRequest struct {
	Token string `json:"token"`
}

// postConflict ends the running long poll of a bot with 409, as a second poller would (F-018); without one, the next
// getUpdates of the bot is answered so.
func (f *Fake) postConflict(w http.ResponseWriter, r *http.Request) {
	var req conflictRequest
	if err := fakeserver.DecodeJSON(w, r, &req); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Token == "" {
		fakeserver.WriteError(w, http.StatusBadRequest, "token is required")
		return
	}
	ended := f.Conflict(req.Token)
	fakeserver.WriteJSON(w, http.StatusOK, map[string]bool{"ended_running_poll": ended})
}
