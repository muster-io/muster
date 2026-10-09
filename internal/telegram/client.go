// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package telegram is the Bot API client of Telegram Connections (C-14.FR-1, FR-10 to FR-12): calls to
// `<base>/bot<token>/<method>` through internal/outbound with the Connection's proxy, in the client class of the caller
// (ADR-0015), with the bot token registered for redaction so that every error and log line carries `bot[redacted]`;
// the step-by-step connection check with its dry probe; and the receiving of updates — long polling on the Leader or
// the webhook endpoint with its secret token — and the update router both modes share; the Destination check of a
// Telegram Destination and the delivery adapter of its Root messages.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// The timeouts of a call and the largest answer read.
const (
	connectTimeout = 5 * time.Second
	// CallTimeout bounds every call but a long poll; the dry probe names it when nothing answered within it.
	CallTimeout    = 10 * time.Second
	maxAnswerBytes = 4 << 20
)

// PollTimeout is the long-poll timeout of getUpdates: how long the Bot API holds a getUpdates open while no update
// comes. The request itself may take pollMargin longer.
const (
	PollTimeout = 25 * time.Second
	pollMargin  = 10 * time.Second
)

// defaultRetryAfter is the wait of a 429 whose answer names no delay.
const defaultRetryAfter = 5 * time.Second

// codeNotJSON is the status the body classifier reports for an answer that is not JSON, which no Bot API sends.
const codeNotJSON = -1

// The paths of a Connection's requests (ConnectionPath).
const (
	ViaDirect = "direct"
	ViaProxy  = "proxy"
)

// AllowedUpdates are the update types Muster asks for, explicitly, in getUpdates and setWebhook (C-14.FR-1).
var AllowedUpdates = []string{"message", "edited_message", "channel_post", "callback_query", "my_chat_member"}

// dryProbeToken is the token of the dry probe, which no bot has: a Bot API answers it 401 with a JSON body.
const dryProbeToken = "0:x"

// errNoToken refuses a call with the token on a client that has none: the probe client of an unsaved base URL.
var errNoToken = errors.New("this client has no bot token: only the dry probe runs against an unsaved base URL")

// Network is what a Client needs besides its Connection: the outbound address policy, the logger, the real clock of
// the outbound metrics and a resolver (nil: the system resolver).
type Network struct {
	Policy   outbound.PolicySource
	Log      *logging.Logger
	Real     clock.Clock
	Resolver outbound.Resolver
}

// Settings are a Connection as its client needs it: the normalized Bot API base URL, the bot token — empty for the
// client of the dry probe of an unsaved base URL — and the proxy, nil for none.
type Settings struct {
	BaseURL string
	Token   logging.Secret
	Proxy   *outbound.Proxy
}

// Client calls the Bot API of one Connection, in the interactive and the delivery client classes, and in the
// background class for long polling and muster doctor; the outbound client of a class is built on its first call.
type Client struct {
	network  Network
	settings Settings
	via      string

	mu      sync.Mutex
	clients map[clientKey]*outbound.Client
	botID   int64
}

// clientKey names an outbound client: its class, and whether it makes long polls, which take longer.
type clientKey struct {
	class outbound.Class
	long  bool
}

// NewClient builds the client of the Connection s, checking its settings with the interactive class. The bot token
// and the proxy password are registered for redaction.
func NewClient(n Network, s Settings) (*Client, error) {
	c := &Client{network: n, settings: s, via: ViaDirect, clients: map[clientKey]*outbound.Client{}}
	if s.Proxy != nil {
		c.via = ViaProxy
	}
	if _, err := c.client(clientKey{class: outbound.ClassInteractive}); err != nil {
		return nil, err
	}
	return c, nil
}

// Via is how the requests of the client travel: ViaDirect or ViaProxy.
func (c *Client) Via() string { return c.via }

// Origin is the scheme and host of the client's base URL, all that log lines show of it.
func (c *Client) Origin() string { return Origin(c.settings.BaseURL) }

func (c *Client) client(k clientKey) (*outbound.Client, error) {
	if k.class != outbound.ClassInteractive && k.class != outbound.ClassDelivery && k.class != outbound.ClassBackground {
		return nil, fmt.Errorf("the Telegram client has no %s class", k.class)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if oc, ok := c.clients[k]; ok {
		return oc, nil
	}
	timeout := CallTimeout
	if k.long {
		timeout = PollTimeout + pollMargin
	}
	s, n := c.settings, c.network
	var secrets []logging.Secret
	if s.Token != "" {
		secrets = append(secrets, s.Token)
	}
	oc, err := outbound.New(outbound.Config{Class: k.class, ConnectTimeout: connectTimeout, Timeout: timeout,
		Proxy: s.Proxy, Secrets: secrets, BaseURL: s.BaseURL, Policy: n.Policy, Logger: n.Log, Clock: n.Real,
		Resolver: n.Resolver, MaxBodyBytes: maxAnswerBytes})
	if err != nil {
		return nil, err
	}
	c.clients[k] = oc
	return oc, nil
}

// Result is how a call ended: its outcome for delivery, classified as C-14.FR-7 says; the status of the answer, 0
// without one; the error_code and description of an `ok: false` answer, the description untrusted and masked; whether
// the answer was not JSON; and, when the call got no answer, what failed (outbound.Network*) and its masked detail.
type Result struct {
	Outcome     delivery.Outcome
	Status      int
	Code        int
	Description string
	NotJSON     bool
	Network     string
	Detail      string
}

// OK reports whether the call succeeded.
func (r Result) OK() bool { return r.Outcome.Kind == delivery.OutcomeOK }

// User is the bot as getMe answers it.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// WebhookInfo is the answer of getWebhookInfo: the webhook's URL, empty when none is set, and the updates waiting.
type WebhookInfo struct {
	URL                string `json:"url"`
	PendingUpdateCount int64  `json:"pending_update_count"`
}

// GetMe reads the bot: the check of its token.
func (c *Client) GetMe(ctx context.Context, class outbound.Class) (User, Result) {
	var u User
	return u, c.call(ctx, clientKey{class: class}, http.MethodGet, "getMe", nil, &u)
}

// GetWebhookInfo reads whether a webhook is set, which makes long polling fail with 409, and how many updates wait.
func (c *Client) GetWebhookInfo(ctx context.Context, class outbound.Class) (WebhookInfo, Result) {
	var w WebhookInfo
	return w, c.call(ctx, clientKey{class: class}, http.MethodGet, "getWebhookInfo", nil, &w)
}

// GetUpdates makes one long poll in the background class: the updates from offset, when it is set, of the types of
// AllowedUpdates, waiting up to timeout for one. The background class retries transient failures and 429 until ctx
// ends; a 409 from a second poller or a set webhook ends the call at once.
func (c *Client) GetUpdates(ctx context.Context, offset *int64, timeout time.Duration) ([]Update, Result) {
	body := map[string]any{"timeout": int64(timeout / time.Second), "allowed_updates": AllowedUpdates}
	if offset != nil {
		body["offset"] = *offset
	}
	var raw []json.RawMessage
	r := c.call(ctx, clientKey{class: outbound.ClassBackground, long: true}, http.MethodPost, "getUpdates", body, &raw)
	if !r.OK() {
		return nil, r
	}
	out := make([]Update, 0, len(raw))
	for _, b := range raw {
		u, err := ParseUpdate(b)
		if err != nil {
			// An update whose known parts are not what Muster expects still has its id: the router drops it as other,
			// so that one odd update never stalls the Connection. One without an id cannot be confirmed and is skipped.
			var head struct {
				UpdateID *int64 `json:"update_id"`
			}
			if json.Unmarshal(b, &head) != nil || head.UpdateID == nil {
				continue
			}
			u = Update{UpdateID: *head.UpdateID, Raw: append(json.RawMessage(nil), b...)}
		}
		out = append(out, u)
	}
	return out, r
}

// SetWebhook sets the webhook at hookURL with the secret token header secret and AllowedUpdates, one connection at a
// time, so that updates arrive in order.
func (c *Client) SetWebhook(ctx context.Context, class outbound.Class, hookURL string, secret logging.Secret) Result {
	r := c.call(ctx, clientKey{class: class}, http.MethodPost, "setWebhook", map[string]any{"url": hookURL,
		"secret_token": string(secret), "allowed_updates": AllowedUpdates, "max_connections": 1}, nil)
	if secret != "" {
		r.Description = strings.ReplaceAll(r.Description, string(secret), "[redacted]")
		r.Outcome.Error = outbound.Untrusted(strings.ReplaceAll(string(r.Outcome.Error), string(secret), "[redacted]"))
	}
	return r
}

// DeleteWebhook removes the webhook, so that long polling works again; pending updates are kept.
func (c *Client) DeleteWebhook(ctx context.Context, class outbound.Class) Result {
	return c.call(ctx, clientKey{class: class}, http.MethodPost, "deleteWebhook", map[string]any{}, nil)
}

// ChatInfo is a chat as getChat answers it: a channel names its discussion group in linked_chat_id only while comments
// are enabled (F-002).
type ChatInfo struct {
	Chat
	LinkedChatID int64 `json:"linked_chat_id,omitempty"`
}

// The statuses of a chat member that may write to the chat as an admin.
const (
	StatusCreator       = "creator"
	StatusAdministrator = "administrator"
)

// ChatMember is a membership as getChatMember answers it; the rights to post and edit are named for channels only.
type ChatMember struct {
	Status          string `json:"status"`
	CanPostMessages bool   `json:"can_post_messages"`
	CanEditMessages bool   `json:"can_edit_messages"`
}

// GetChat reads the chat named by an id or an @username.
func (c *Client) GetChat(ctx context.Context, class outbound.Class, chat string) (ChatInfo, Result) {
	var ch ChatInfo
	return ch, c.call(ctx, clientKey{class: class}, http.MethodPost, "getChat", map[string]any{"chat_id": chatRef(chat)},
		&ch)
}

// GetChatMember reads the membership of the user in the chat; outside a supergroup the bot gets 403 (F-003).
func (c *Client) GetChatMember(ctx context.Context, class outbound.Class, chat string, user int64) (ChatMember,
	Result) {
	var m ChatMember
	return m, c.call(ctx, clientKey{class: class}, http.MethodPost, "getChatMember",
		map[string]any{"chat_id": chatRef(chat), "user_id": user}, &m)
}

// BotID is the user id of the bot, read with getMe on first use and kept with the client.
func (c *Client) BotID(ctx context.Context, class outbound.Class) (int64, Result) {
	c.mu.Lock()
	id := c.botID
	c.mu.Unlock()
	if id != 0 {
		return id, Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeOK}}
	}
	u, r := c.GetMe(ctx, class)
	if !r.OK() {
		return 0, r
	}
	c.mu.Lock()
	c.botID = u.ID
	c.mu.Unlock()
	return u.ID, r
}

// knownBotID is the bot's user id once BotID read it, 0 before.
func (c *Client) knownBotID() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.botID
}

// chatRef is a chat id as the Bot API takes it: an integer, or an @username as text.
func chatRef(chat string) any {
	if id, err := strconv.ParseInt(strings.TrimSpace(chat), 10, 64); err == nil {
		return id
	}
	return strings.TrimSpace(chat)
}

// outgoing is a sendMessage or an editMessageText: an edit names its message and has no disable_notification. Link
// previews are off, so that the link to Muster does not take the room of the message.
type outgoing struct {
	ChatID              any              `json:"chat_id"`
	MessageID           int64            `json:"message_id,omitempty"`
	Text                string           `json:"text"`
	ParseMode           string           `json:"parse_mode,omitempty"`
	ReplyMarkup         *inlineKeyboard  `json:"reply_markup,omitempty"`
	ReplyParameters     *replyParameters `json:"reply_parameters,omitempty"`
	DisableNotification bool             `json:"disable_notification,omitempty"`
	LinkPreview         linkPreview      `json:"link_preview_options"`
}

// replyParameters make a message a reply to message_id in the same chat; without allow_sending_without_reply, a
// reply to a message that is gone fails (F-008).
type replyParameters struct {
	MessageID int64 `json:"message_id"`
}

type linkPreview struct {
	IsDisabled bool `json:"is_disabled"`
}

// inlineKeyboard is the keyboard of a message: rows of buttons, none to remove it from an edited message (F-011).
type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

// inlineButton is a button whose press sends its callback_data, at most 64 bytes.
type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// sentMessage is the message a send or an edit answers with.
type sentMessage struct {
	MessageID int64 `json:"message_id"`
	Chat      Chat  `json:"chat"`
}

// sendMessage sends m. Only the adapter calls it, from the delivery worker and the interactive path (lint 3).
func (c *Client) sendMessage(ctx context.Context, class outbound.Class, m outgoing) (sentMessage, Result) {
	var out sentMessage
	return out, c.call(ctx, clientKey{class: class}, http.MethodPost, "sendMessage", m, &out)
}

// editMessageText edits the message m names to m, which notifies nobody (F-012) and keeps only the keyboard it
// carries (F-011).
func (c *Client) editMessageText(ctx context.Context, class outbound.Class, m outgoing) (sentMessage, Result) {
	var out sentMessage
	return out, c.call(ctx, clientKey{class: class}, http.MethodPost, "editMessageText", m, &out)
}

// answerCallbackQuery answers the press id with text, which the person who pressed sees on the button (C-14.FR-5);
// Telegram refuses it about 15 seconds after the press (F-010). Only the press handler calls it, inside the answer of
// the interactive path.
func (c *Client) answerCallbackQuery(ctx context.Context, class outbound.Class, id, text string) Result {
	return c.call(ctx, clientKey{class: class}, http.MethodPost, "answerCallbackQuery",
		map[string]any{"callback_query_id": id, "text": text}, nil)
}

// DryProbe is `GET <base>/bot0:x/getMe` without the token (C-14.FR-11): a Bot API answers it 401 with a JSON body.
// The Result is the raw answer, which the check names; a 429 is Transient, never a RetryAfter.
func (c *Client) DryProbe(ctx context.Context, class outbound.Class) Result {
	oc, err := c.client(clientKey{class: class})
	if err != nil {
		return Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(err.Error())}}
	}
	res, err := oc.Do(ctx, outbound.Request{Method: http.MethodGet, URL: "/bot" + dryProbeToken + "/getMe",
		Header: http.Header{"Accept": {"application/json"}}, Mapping: mapping, ClassifyBody: classifyBody})
	r := resultOf(res, err)
	// A 429 to a token no bot has says nothing of the bot's budget: it must not hold the Connection's limiter, least
	// of all when an unsaved base URL answered it.
	if r.Outcome.Kind == delivery.OutcomeRetryAfter {
		r.Outcome.Kind, r.Outcome.Scope, r.Outcome.RetryAfter = delivery.OutcomeTransient, "", 0
	}
	return r
}

// call makes one call of method in the client k, with body as JSON unless it is nil, and reads the result of a
// successful answer into out unless it is nil.
func (c *Client) call(ctx context.Context, k clientKey, httpMethod, method string, body, out any) Result {
	if c.settings.Token == "" {
		return Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(errNoToken.Error())}}
	}
	oc, err := c.client(k)
	if err != nil {
		return Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(err.Error())}}
	}
	header := http.Header{"Accept": {"application/json"}}
	var data []byte
	if body != nil {
		if data, err = json.Marshal(body); err != nil {
			return Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeUnknown,
				Error: outbound.Untrusted(method + ": the request is not valid JSON")}}
		}
		header.Set("Content-Type", "application/json")
	}
	res, err := oc.Do(ctx, outbound.Request{Method: httpMethod, URL: "/bot" + string(c.settings.Token) + "/" + method,
		Header: header, Body: data, Mapping: mapping, ClassifyBody: classifyBody})
	r := resultOf(res, err)
	if !r.OK() || out == nil {
		return r
	}
	var env envelope
	if err := json.Unmarshal(res.Body, &env); err != nil || len(env.Result) == 0 || json.Unmarshal(env.Result, out) != nil {
		return Result{Status: res.Status, Outcome: delivery.Outcome{Kind: delivery.OutcomeUnknown,
			Error: outbound.Untrusted(method + ": the answer is not the expected JSON")}}
	}
	return r
}

// envelope is every Bot API answer: ok with a result, or an error_code, a description and parameters.
type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int64 `json:"retry_after"`
	} `json:"parameters"`
}

// classifyBody reads the envelope of an answer (C-14.FR-7): `ok: false` is classified by its error_code like a status,
// with parameters.retry_after as the exact delay; an answer that is not JSON is codeNotJSON, Transient.
func classifyBody(status int, body []byte) (outbound.BodyError, bool) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return outbound.BodyError{Code: codeNotJSON, Text: "the answer is not JSON"}, true
	}
	if env.OK {
		return outbound.BodyError{}, false
	}
	code := env.ErrorCode
	if code == 0 {
		code = status
		if code >= 200 && code < 300 {
			code = http.StatusInternalServerError
		}
	}
	be := outbound.BodyError{Code: code, Text: env.Description}
	if env.Parameters.RetryAfter > 0 {
		be.RetryAfter = time.Duration(env.Parameters.RetryAfter) * time.Second
	}
	return be, true
}

// mapping classifies the status of an answer, or the error_code its body reports (C-14.FR-7): 429 is RetryAfter;
// 5xx, 408 and an answer that is not JSON are Transient; 401, 403 and 404 Fatal; anything else, 409 among them, is
// unknown and never retried. classify.go refines the answers of sends and edits by their description.
func mapping(status int, _ time.Duration, _ bool) outbound.Outcome {
	switch {
	case status >= 200 && status < 300:
		return outbound.OutcomeOK
	case status == codeNotJSON, status >= 500, status == http.StatusRequestTimeout:
		return outbound.OutcomeTransient
	case status == http.StatusTooManyRequests:
		return outbound.OutcomeRetryAfter
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return outbound.OutcomeFatal
	}
	return outbound.OutcomeUnknown
}

// resultOf is the Result of an outbound call: a blocked address and a refused redirect are configuration errors and
// Fatal; a timeout or a failed connection is Transient; a RetryAfter holds the whole Connection here, since these calls
// name no chat; the adapter holds a Destination for its own calls (classify.go).
func resultOf(res outbound.Result, err error) Result {
	r := Result{Status: res.Status}
	var env envelope
	if res.Status != 0 {
		if json.Unmarshal(res.Body, &env) != nil {
			r.NotJSON = true
		} else if !env.OK {
			r.Code, r.Description = env.ErrorCode, string(res.ProviderError)
		}
	}
	if err == nil {
		r.Outcome = delivery.Outcome{Kind: delivery.OutcomeOK}
		return r
	}
	r.Outcome.Error = outbound.Untrusted(err.Error())
	if oe, ok := errors.AsType[*outbound.Error](err); ok {
		r.Network, r.Detail = oe.Network, oe.Detail
	}
	if r.Description != "" {
		code := r.Code
		if code == 0 {
			code = res.Status
		}
		r.Outcome.Error = outbound.Untrusted("Telegram answered " + strconv.Itoa(code) + ": " + r.Description)
	}
	switch res.Outcome {
	case outbound.OutcomeRetryAfter:
		r.Outcome.Kind, r.Outcome.Scope = delivery.OutcomeRetryAfter, delivery.ScopeConnection
		r.Outcome.RetryAfter = res.RetryAfter
		if r.Outcome.RetryAfter <= 0 {
			r.Outcome.RetryAfter = defaultRetryAfter
		}
	case outbound.OutcomeTransient:
		r.Outcome.Kind = delivery.OutcomeTransient
	case outbound.OutcomeFatal, outbound.OutcomeBlocked, outbound.OutcomeRedirect:
		r.Outcome.Kind = delivery.OutcomeFatal
	default:
		r.Outcome.Kind = delivery.OutcomeUnknown
	}
	return r
}

// Conflict reports whether the call was refused with 409: a second poller on the token (F-018) or a set webhook.
func (r Result) Conflict() bool {
	return r.Status == http.StatusConflict || r.Code == http.StatusConflict
}
