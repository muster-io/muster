// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package slack is Muster's client of Slack's Web API (C-24): calls of `https://slack.com/api/<method>` with a
// Connection's bot token or app-level token as a bearer token, through internal/outbound with the Connection's proxy in
// the caller's client class (ADR-0015), JSON for the write methods and form-encoded for the others, with both tokens and
// the Slack signing secret registered for redaction; the classification of every answer by Slack's `error`
// (classify.go); the Connection check (check.go); the Destination check of a channel and the channel list
// (channels.go); delivery (adapter.go), whose messages are laid out in blocks (layout.go) and sent with the
// unexported chat calls of this file; and the presses of people (presses.go) — confirmed by the signed callback of
// http mode (callback.go) or by the Leader's Socket Mode connections, which confirm each envelope at once and hand its
// press over (socket.go) — and their private answers (answers.go), with chat.postEphemeral or the press's
// response_url; and Account links (links.go): the lookup of a person by email or name and the bot's direct messages
// with a code.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// BaseURL is the only address of Slack's Web API (C-24.FR-1): a Connection has no address of its own.
const BaseURL = "https://slack.com/api/"

// CallbackPath is the path below MUSTER_INGEST_URL where Slack posts the button presses of a Connection in http mode,
// followed by its public_id (C-24.FR-9).
const CallbackPath = "/api/v1/callbacks/slack/"

// The event modes of a Connection (connection.slack.event_mode): Socket Mode with the app-level token, or presses
// posted to the callback address and signed with the Slack signing secret.
const (
	EventModeSocket = "socket"
	EventModeHTTP   = "http"
)

// The paths of a Connection's requests (ConnectionPath).
const (
	ViaDirect = "direct"
	ViaProxy  = "proxy"
)

// The timeouts of a call and the largest answer read.
const (
	connectTimeout = 5 * time.Second
	// CallTimeout bounds every call; a check names it when nothing answered within it.
	CallTimeout    = 10 * time.Second
	maxAnswerBytes = 4 << 20
)

// The headers of every answer that name the token's scopes and the scopes the method accepts (F-071).
const (
	headerScopes         = "X-OAuth-Scopes"
	headerAcceptedScopes = "X-Accepted-OAuth-Scopes"
)

// MessageProxyRefused is the message of a request that the Connection's proxy refused with 407, as every check says it.
const MessageProxyRefused = delivery.MessageProxyRefused

// maxText is how much of a text Slack sends is kept, as outbound keeps a provider's error text.
const maxText = 1024

// Network is what a Client needs besides its Connection: the outbound address policy, the logger, the real clock of
// the outbound metrics and of Retry-After dates, a resolver (nil: the system resolver) and the Request log, nil for
// none (C-23.FR-1). Its fields are those of mattermost.Network, so that one converts into the other.
type Network struct {
	Policy   outbound.PolicySource
	Log      *logging.Logger
	Real     clock.Clock
	Resolver outbound.Resolver
	Recorder outbound.Recorder
}

// Route names the base URL a Connection's requests go to instead of BaseURL, by its bot token: under `muster dev` the
// fake Slack server for its published tokens; an empty answer is Slack.
type Route func(botToken logging.Secret) string

// Settings are a Connection as its client needs it: the bot token, the app-level token, empty without one, and the
// Slack signing secret, which is never sent and is registered for redaction only; the proxy, nil for none; the
// Connection whose Request log keeps its requests, none for a client that records nothing; and the Route of `muster
// dev`, nil outside it.
type Settings struct {
	BotToken      logging.Secret
	AppToken      logging.Secret
	SigningSecret logging.Secret
	Proxy         *outbound.Proxy
	Owner         outbound.Owner
	Route         Route
}

// Client calls Slack's Web API for one Connection, in the interactive, delivery and background client classes; the
// outbound client of a class is built on its first call.
type Client struct {
	network  Network
	settings Settings
	base     string
	// origin is the scheme and host of base, the outbound client's base URL, and path its path, which every method
	// follows: Slack's address is no secret, so the Request log shows it whole.
	origin string
	path   string
	via    string

	mu      sync.Mutex
	clients map[outbound.Class]*outbound.Client
	// hooks is the outbound client of the answers to response_url, in the interactive class, without a base URL:
	// built on its first answer.
	hooks *outbound.Client
	// socket is the outbound client of the Socket Mode handshakes, in the background class, without a base URL: built
	// on its first connection.
	socket *outbound.Client
}

// errBaseURL is a Route answer that is not an absolute http or https URL without user information, query or fragment.
var errBaseURL = errors.New("the Slack base URL is not an absolute http or https URL without user information, query " +
	"or fragment")

// NewClient builds the client of the Connection s, checking its settings with the interactive class. Its requests go to
// BaseURL, or to what s.Route answers for the bot token when it answers. Both tokens, the Slack signing secret and the
// proxy password are registered for redaction.
func NewClient(n Network, s Settings) (*Client, error) {
	c := &Client{network: n, settings: s, base: BaseURL, via: ViaDirect, clients: map[outbound.Class]*outbound.Client{}}
	if s.Route != nil {
		if b := s.Route(s.BotToken); b != "" {
			c.base = b
		}
	}
	u, err := url.Parse(c.base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errBaseURL
	}
	c.origin, c.path = u.Scheme+"://"+u.Host, strings.TrimSuffix(u.EscapedPath(), "/")+"/"
	if s.Proxy != nil {
		c.via = ViaProxy
	}
	if _, err := c.client(outbound.ClassInteractive); err != nil {
		return nil, err
	}
	return c, nil
}

// Via is how the requests of the client travel: ViaDirect or ViaProxy.
func (c *Client) Via() string { return c.via }

// Base is the base URL the client's requests go to: BaseURL, or the Route's answer.
func (c *Client) Base() string { return c.base }

// secrets are the values the client masks: both tokens, the Slack signing secret and the proxy password.
func (s Settings) secrets() []logging.Secret {
	out := []logging.Secret{s.BotToken, s.AppToken, s.SigningSecret}
	if s.Proxy != nil {
		out = append(out, s.Proxy.Password)
	}
	return out
}

// mask replaces the secrets in a text of Slack's that outbound did not mask, the longer first, and keeps the start of
// a long text.
func (c *Client) mask(text string) string {
	return cut(c.redact(text))
}

// redact replaces the secrets in a text of Slack's that outbound did not mask, the longer first, and keeps all of it.
func (c *Client) redact(text string) string {
	return string(c.maskBody([]byte(text)))
}

// maskBody is a copy of an answer of Slack's with the secrets replaced, the longer first, nil for nil.
func (c *Client) maskBody(body []byte) []byte {
	if body == nil {
		return nil
	}
	body = slices.Clone(body)
	secrets := c.settings.secrets()
	slices.SortFunc(secrets, func(a, b logging.Secret) int { return len(b) - len(a) })
	for _, v := range secrets {
		if v != "" {
			body = bytes.ReplaceAll(body, []byte(v), []byte("[redacted]"))
		}
	}
	return body
}

// cut is the start of s, at most maxText bytes on a rune boundary, valid UTF-8.
func cut(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxText {
		return s
	}
	n := maxText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func (c *Client) client(class outbound.Class) (*outbound.Client, error) {
	if class != outbound.ClassInteractive && class != outbound.ClassDelivery && class != outbound.ClassBackground {
		return nil, fmt.Errorf("the Slack client has no %s class", class)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if oc, ok := c.clients[class]; ok {
		return oc, nil
	}
	s, n := c.settings, c.network
	oc, err := outbound.New(outbound.Config{Class: class, ConnectTimeout: connectTimeout, Timeout: CallTimeout,
		Proxy: s.Proxy, Secrets: []logging.Secret{s.BotToken, s.AppToken, s.SigningSecret}, BaseURL: c.origin,
		Policy: n.Policy, Logger: n.Log, Clock: n.Real, Resolver: n.Resolver, MaxBodyBytes: maxAnswerBytes,
		Recorder: n.Recorder, Owner: s.Owner})
	if err != nil {
		return nil, err
	}
	c.clients[class] = oc
	return oc, nil
}

// Args are the arguments of a method: sent as a JSON object to the write methods, and form-encoded to the others,
// where each is text, a bool, an int or int64, or a list of texts joined with commas.
type Args map[string]any

// Result is how a call ended; a zero Classification is success. Error is the masked, untrusted text of a failure:
// Slack's `error`, with `needed` when it names one, or what outbound reports; ErrorCode is Slack's `error` alone,
// masked, empty without an `ok: false` answer, and Needed the scopes its `needed` names, masked. Warning is the
// `warning` of the answer and NextCursor its `response_metadata.next_cursor`; Scopes are the token's scopes that the
// answer's x-oauth-scopes names, kept on a failure too, and AcceptedScopes those of x-accepted-oauth-scopes. Body is
// the answer of a call that succeeded, or of an `ok: false` answer, which a test step shows, with the secrets replaced.
// Network is what failed when the call got no answer (outbound.Network*), Detail its masked text, and Blocked whether
// the outbound address policy refused the address.
type Result struct {
	Status         int
	Classification Classification
	Error          outbound.Untrusted
	ErrorCode      string
	Needed         []string
	Warning        outbound.Untrusted
	NextCursor     string
	Scopes         []string
	AcceptedScopes []string
	Body           []byte
	Network        string
	Detail         string
	Blocked        bool
}

// OK reports whether the call succeeded.
func (r Result) OK() bool { return r.Classification.Class == "" }

// Decode reads the answer of a call that succeeded into out.
func (r Result) Decode(out any) error { return json.Unmarshal(r.Body, out) }

// Outcome is the Result for delivery (C-24.FR-8): RetryAfter holds the whole Connection, since Slack limits each method
// per app and workspace (F-099); Transient, Fatal and MarkupRejected are themselves; a Gone-candidate is unknown until
// the adapter reads the channel, and Unknown is unknown.
func (r Result) Outcome() delivery.Outcome {
	o := delivery.Outcome{Error: r.Error, Status: r.Status}
	switch r.Classification.Class {
	case "":
		o.Kind = delivery.OutcomeOK
	case ClassRetryAfter:
		o.Kind, o.RetryAfter, o.Scope = delivery.OutcomeRetryAfter, r.Classification.RetryAfter,
			delivery.ScopeConnection
	case ClassTransient:
		o.Kind = delivery.OutcomeTransient
	case ClassFatal:
		o.Kind = delivery.OutcomeFatal
	case ClassMarkupRejected:
		o.Kind = delivery.OutcomeMarkupRejected
	default:
		o.Kind = delivery.OutcomeUnknown
	}
	return o
}

// CheckCode is the check code of a failed call that its step does not name: that of delivery.FailureCode, which is
// request_failed for any call that got an answer; empty for a call that succeeded.
func (r Result) CheckCode() string {
	if r.OK() {
		return ""
	}
	return delivery.FailureCode(r.Blocked, r.Network)
}

// AuthTest is the answer of auth.test (F-072): with a bot token the workspace's address and name, the bot's name and
// user id, the workspace's id and the bot's id; with an app-level token the app's id alone.
type AuthTest struct {
	URL    string `json:"url"`
	Team   string `json:"team"`
	User   string `json:"user"`
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
	AppID  string `json:"app_id"`
}

// Bot is a bot as bots.info answers it: its id, name, app and user.
type Bot struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	AppID   string `json:"app_id"`
	UserID  string `json:"user_id"`
	Deleted bool   `json:"deleted"`
}

// AuthTest calls auth.test with the bot token: the bot and its workspace, masked.
func (c *Client) AuthTest(ctx context.Context, class outbound.Class) (AuthTest, Result) {
	return c.authTest(ctx, class, c.settings.BotToken, "bot token")
}

// AppAuthTest calls auth.test with the app-level token: the app's id, masked.
func (c *Client) AppAuthTest(ctx context.Context, class outbound.Class) (AuthTest, Result) {
	return c.authTest(ctx, class, c.settings.AppToken, "app-level token")
}

func (c *Client) authTest(ctx context.Context, class outbound.Class, token logging.Secret, which string) (AuthTest,
	Result) {
	var out AuthTest
	r := c.call(ctx, class, token, which, "auth.test", nil)
	if !r.OK() {
		return AuthTest{}, r
	}
	if r.Decode(&out) != nil {
		return AuthTest{}, notExpected(r, "auth.test")
	}
	m := c.mask
	return AuthTest{URL: m(out.URL), Team: m(out.Team), User: m(out.User), TeamID: m(out.TeamID),
		UserID: m(out.UserID), BotID: m(out.BotID), AppID: m(out.AppID)}, r
}

// BotsInfo calls bots.info with the bot token for the bot botID (scope users:read): its app, masked.
func (c *Client) BotsInfo(ctx context.Context, class outbound.Class, botID string) (Bot, Result) {
	var out struct {
		Bot *Bot `json:"bot"`
	}
	r := c.Call(ctx, class, "bots.info", Args{"bot": botID})
	if !r.OK() {
		return Bot{}, r
	}
	if r.Decode(&out) != nil || out.Bot == nil {
		return Bot{}, notExpected(r, "bots.info")
	}
	b := *out.Bot
	return Bot{ID: c.mask(b.ID), Name: c.mask(b.Name), AppID: c.mask(b.AppID), UserID: c.mask(b.UserID),
		Deleted: b.Deleted}, r
}

// Call calls method in class with the bot token.
func (c *Client) Call(ctx context.Context, class outbound.Class, method string, args Args) Result {
	return c.call(ctx, class, c.settings.BotToken, "bot token", method, args)
}

// CallApp calls method in class with the app-level token.
func (c *Client) CallApp(ctx context.Context, class outbound.Class, method string, args Args) Result {
	return c.call(ctx, class, c.settings.AppToken, "app-level token", method, args)
}

// notExpected is the Result of a call that succeeded but whose answer is not what method answers.
func notExpected(r Result, method string) Result {
	return Result{Status: r.Status, Classification: Classification{Class: ClassUnknown},
		Error: outbound.Untrusted(method + ": the answer is not the expected JSON"), Scopes: r.Scopes,
		AcceptedScopes: r.AcceptedScopes}
}

// jsonMethod reports whether method takes a JSON body: the write methods.
func jsonMethod(method string) bool {
	return strings.HasPrefix(method, "chat.") || method == "conversations.open"
}

// call makes one call of method with token, which the client names which: a POST to `<base><method>` with the token
// as a bearer token, args as JSON for the write methods and form-encoded for the others, named by the method in the
// Request log. A client without the token makes no call, nor does one whose args cannot be encoded.
func (c *Client) call(ctx context.Context, class outbound.Class, token logging.Secret, which, method string,
	args Args) Result {
	if token == "" {
		return unknown("this client has no " + which)
	}
	oc, err := c.client(class)
	if err != nil {
		return unknown(err.Error())
	}
	h := http.Header{"Authorization": {"Bearer " + string(token)}, "Accept": {"application/json"}}
	var body []byte
	if jsonMethod(method) {
		if args == nil {
			args = Args{}
		}
		if body, err = json.Marshal(args); err != nil {
			return unknown(method + ": the arguments are not valid JSON")
		}
		h.Set("Content-Type", "application/json; charset=utf-8")
	} else {
		form, err := formOf(args)
		if err != nil {
			return unknown(method + ": " + err.Error())
		}
		if len(form) > 0 {
			body = []byte(form.Encode())
		}
		h.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	a := &answerClass{}
	res, err := oc.Do(ctx, outbound.Request{Method: http.MethodPost, URL: c.path + method, Header: h, Body: body,
		Mapping: a.mapping, ClassifyBody: a.body, Operation: method})
	return c.resultOf(method, res, err)
}

// unknown is the Result of a call that was not made.
func unknown(text string) Result {
	return Result{Classification: Classification{Class: ClassUnknown}, Error: outbound.Untrusted(text)}
}

// formOf is args form-encoded.
func formOf(args Args) (url.Values, error) {
	form := url.Values{}
	for k, v := range args {
		switch v := v.(type) {
		case string:
			form.Set(k, v)
		case bool:
			form.Set(k, strconv.FormatBool(v))
		case int:
			form.Set(k, strconv.Itoa(v))
		case int64:
			form.Set(k, strconv.FormatInt(v, 10))
		case []string:
			form.Set(k, strings.Join(v, ","))
		default:
			return nil, fmt.Errorf("the argument %s cannot be form-encoded", k)
		}
	}
	return form, nil
}

// posted is what chat.postMessage and chat.update answer of the message: its `ts`, and its `thread_ts` when it went
// into a Thread (F-089); a reply to a deleted root without replies is answered without one (F-091).
type posted struct {
	TS      string `json:"ts"`
	Message struct {
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts"`
	} `json:"message"`
}

// postMessage calls chat.postMessage with the bot token in class for the message o: a Root message, or a Thread reply
// when o names its `thread_ts`. Its answer is decoded from the masked body of a call that succeeded.
func (c *Client) postMessage(ctx context.Context, class outbound.Class, o outgoing) (posted, Result) {
	var p posted
	r := c.Call(ctx, class, "chat.postMessage", o.args())
	if !r.OK() {
		return posted{}, r
	}
	if r.Decode(&p) != nil {
		return posted{}, notExpected(r, "chat.postMessage")
	}
	return p, r
}

// updateMessage calls chat.update with the bot token in class: the message o.TS becomes o (F-084, F-085).
func (c *Client) updateMessage(ctx context.Context, class outbound.Class, o outgoing) Result {
	return c.Call(ctx, class, "chat.update", o.args())
}

// deleteMessage calls chat.delete with the bot token in class for the message ts of channel (F-087).
func (c *Client) deleteMessage(ctx context.Context, class outbound.Class, channel, ts string) Result {
	return c.Call(ctx, class, "chat.delete", Args{"channel": channel, "ts": ts})
}

// answer is the envelope of every answer of the Web API: ok, and for an error its `error`, with `needed` and
// `provided` for missing_scope (F-070); a `warning`, and the cursor of the next page.
type answer struct {
	OK               *bool  `json:"ok"`
	Error            string `json:"error"`
	Needed           string `json:"needed"`
	Provided         string `json:"provided"`
	Warning          string `json:"warning"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

// answerOf decodes the envelope of body; false for a body that is not Slack's JSON.
func answerOf(body []byte) (answer, bool) {
	var a answer
	if json.Unmarshal(body, &a) != nil || a.OK == nil {
		return answer{}, false
	}
	return a, true
}

// answerClass classifies the answers of one call for outbound (C-24.FR-8): by Slack's `error`, never by the status
// alone (F-070), so that the outcome outbound counts and records, and retries in the background class, is that of the
// error. body runs first on each answer and notes the class its error decides; mapping then answers it.
type answerClass struct {
	class Class
}

// body reports the error of an `ok: false` answer, with `needed`, so that outbound masks it into ProviderError, and
// notes the class of an error that decides the class: one with a status that is not 408, 429 or 5xx. A successful
// status whose answer is not Slack's JSON is unknown.
func (a *answerClass) body(status int, data []byte) (outbound.BodyError, bool) {
	a.class = ""
	env, ok := answerOf(data)
	switch {
	case !ok:
		if status >= http.StatusOK && status < http.StatusMultipleChoices {
			a.class = ClassUnknown
		}
		return outbound.BodyError{}, false
	case *env.OK:
		return outbound.BodyError{}, false
	}
	if byError(status) {
		a.class = Classify(Answer{Status: status, Error: env.Error, Needed: env.Needed}).Class
		if env.Error == "" {
			a.class = ClassUnknown
		}
	}
	return outbound.BodyError{Code: status, Text: errorText(env.Error, env.Needed)}, true
}

// mapping is the outcome of an answer for outbound: that of the class body noted, otherwise by the status — a 2xx is
// ok, 429 waits for its Retry-After and is transient without one, 408 and 5xx are transient, anything else unknown.
func (a *answerClass) mapping(status int, _ time.Duration, hasRetryAfter bool) outbound.Outcome {
	if a.class != "" {
		return outcomeOf(a.class)
	}
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return outbound.OutcomeOK
	case status == http.StatusTooManyRequests && hasRetryAfter:
		return outbound.OutcomeRetryAfter
	case status == http.StatusTooManyRequests, status == http.StatusRequestTimeout, status >= 500 && status < 600:
		return outbound.OutcomeTransient
	}
	return outbound.OutcomeUnknown
}

// outcomeOf is the outbound outcome of a class.
func outcomeOf(c Class) outbound.Outcome {
	switch c {
	case "":
		return outbound.OutcomeOK
	case ClassRetryAfter:
		return outbound.OutcomeRetryAfter
	case ClassTransient:
		return outbound.OutcomeTransient
	case ClassFatal:
		return outbound.OutcomeFatal
	default:
		return outbound.OutcomeUnknown
	}
}

// resultOf is the Result of the outbound call of method. Without an answer it is transient — a request that could not
// be built is unknown and a blocked address fatal; a 407 through the Connection's proxy is the proxy's refusal, a call
// without an answer, as the HTTPS CONNECT that a proxy refuses is; a redirect, never followed, is unknown; an `ok:
// false` answer is classified by its error, masked, with `needed`; a successful status whose answer is not Slack's JSON
// is unknown; any other error answer is classified by its status.
func (c *Client) resultOf(method string, res outbound.Result, err error) Result {
	r := Result{Status: res.Status, Scopes: c.scopes(res.Header.Get(headerScopes)),
		AcceptedScopes: c.scopes(res.Header.Get(headerAcceptedScopes))}
	env, decoded := answer{}, false
	if res.Status != 0 {
		env, decoded = answerOf(res.Body)
	}
	if decoded {
		r.Warning = outbound.Untrusted(c.mask(env.Warning))
		r.NextCursor = c.mask(env.ResponseMetadata.NextCursor)
	}
	if err == nil {
		r.Body = c.maskBody(res.Body)
		return r
	}
	r.Error = outbound.Untrusted(err.Error())
	switch {
	case res.Status == 0:
		if oe, ok := errors.AsType[*outbound.Error](err); ok {
			r.Network, r.Detail = oe.Network, oe.Detail
		}
		r.Blocked = res.Outcome == outbound.OutcomeBlocked
		switch {
		case r.Blocked:
			r.Classification = Classification{Class: ClassFatal}
		case res.Outcome == outbound.OutcomeFatal:
			r.Classification = Classification{Class: ClassUnknown}
		default:
			r.Classification = Classify(Answer{})
		}
	case res.Status == http.StatusProxyAuthRequired && c.settings.Proxy != nil:
		r.Status, r.Network, r.Error = 0, outbound.NetworkProxyAuth, MessageProxyRefused
		r.Classification = Classify(Answer{})
	case res.Outcome == outbound.OutcomeRedirect:
		r.Classification = Classification{Class: ClassUnknown}
	case decoded && !*env.OK:
		r.Body = c.maskBody(res.Body)
		code := c.redact(env.Error)
		r.ErrorCode, r.Needed = cut(code), c.scopes(env.Needed)
		r.Classification = Classify(Answer{Status: res.Status, RetryAfter: res.RetryAfter,
			HasRetryAfter: res.Outcome == outbound.OutcomeRetryAfter, Error: code, Needed: c.redact(env.Needed)})
		r.Classification.Reason = outbound.Untrusted(cut(string(r.Classification.Reason)))
		switch {
		case r.Classification.Class == "":
			r.Classification = Classification{Class: ClassUnknown}
			r.Error = outbound.Untrusted(method + ": Slack answered ok: false without an error")
		case r.Classification.Reason != "":
			r.Error = r.Classification.Reason
		}
	case res.Status >= http.StatusOK && res.Status < http.StatusMultipleChoices:
		r.Classification = Classification{Class: ClassUnknown}
		if res.Body != nil {
			r.Error = outbound.Untrusted(method + ": the answer is not Slack's JSON")
		}
	default:
		r.Classification = Classify(Answer{Status: res.Status, RetryAfter: res.RetryAfter,
			HasRetryAfter: res.Outcome == outbound.OutcomeRetryAfter})
	}
	return r
}

// scopes are the scopes a header names, comma-separated, masked.
func (c *Client) scopes(header string) []string {
	var out []string
	for s := range strings.SplitSeq(header, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, c.mask(s))
		}
	}
	return out
}

// postEphemeral calls chat.postEphemeral with the bot token in class (F-092): text, shown only to user in channel, in
// the Thread of threadTS when it is set.
func (c *Client) postEphemeral(ctx context.Context, class outbound.Class, channel, user, threadTS, text string) Result {
	args := Args{"channel": channel, "user": user, "text": text}
	if threadTS != "" {
		args["thread_ts"] = threadTS
	}
	return c.Call(ctx, class, "chat.postEphemeral", args)
}

// HooksHost is the only host a press's response_url is called on outside `muster dev` (C-24.FR-11).
const HooksHost = "hooks.slack.com"

// responseAnswer is the body of an answer to a press's response_url (F-093): ephemeral, and never replacing the
// pressed message, which only the delivery worker edits (C-11.FR-2); replace_original is always sent.
type responseAnswer struct {
	ResponseType    string `json:"response_type"`
	ReplaceOriginal bool   `json:"replace_original"`
	Text            string `json:"text"`
}

// responseError is an error code an answer of response_url may carry, which a Result names; anything else in its body
// is not shown.
var responseError = regexp.MustCompile(`^[a-z_]{1,64}$`)

// hooksPrefix starts every address a response_url is answered at outside `muster dev`: HooksHost, followed by the
// checked path of the response_url.
const hooksPrefix = "https://" + HooksHost + "/"

// responsePath is the path of a press's response_url without its leading slash (F-093): actions/<team>/<n>/<token>,
// with a Slack team id, T or E and capitals or digits, as the Request log masks it; nothing else, no percent-encoding.
var responsePath = regexp.MustCompile(`^actions/[TE][A-Z0-9]+/[0-9]+/[A-Za-z0-9]+$`)

// responseTarget is the address the client answers responseURL at when it may call it (C-24.FR-11), and its checked
// path: responseURL is an https address on HooksHost, without a port, user information, query or fragment, whose path
// is responsePath, answered at hooksPrefix and that path; or, for a client that `muster dev` routes to the fake Slack
// server, an address on that server with such a path, answered at the server's configured origin and that path. The
// address is built anew, never taken from responseURL. The host is compared in ASCII only: one with any other
// character, such as the Kelvin sign that Unicode folds to a `k`, is refused.
func (c *Client) responseTarget(responseURL string) (target, path string, ok bool) {
	u, err := url.Parse(responseURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		!ascii(u.Host) {
		return "", "", false
	}
	path = strings.TrimPrefix(u.EscapedPath(), "/")
	if !responsePath.MatchString(path) {
		return "", "", false
	}
	host := strings.ToLower(u.Host)
	if u.Scheme == "https" && host == HooksHost {
		return hooksPrefix + path, path, true
	}
	if c.base != BaseURL && u.Scheme+"://"+host == strings.ToLower(c.origin) {
		return c.origin + "/" + path, path, true
	}
	return "", "", false
}

// ascii reports whether s has only ASCII characters.
func ascii(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// hooksClient is the outbound client of the answers to response_url: the interactive class through the Connection's
// proxy, with the client's secrets, recorded in the Connection's Request log, without a base URL.
func (c *Client) hooksClient() (*outbound.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hooks != nil {
		return c.hooks, nil
	}
	s, n := c.settings, c.network
	oc, err := outbound.New(outbound.Config{Class: outbound.ClassInteractive, ConnectTimeout: connectTimeout,
		Timeout: CallTimeout, Proxy: s.Proxy, Secrets: []logging.Secret{s.BotToken, s.AppToken, s.SigningSecret},
		Policy: n.Policy, Logger: n.Log, Clock: n.Real, Resolver: n.Resolver, MaxBodyBytes: maxAnswerBytes,
		Recorder: n.Recorder, Owner: s.Owner})
	if err != nil {
		return nil, err
	}
	c.hooks = oc
	return oc, nil
}

// respond posts text to a press's responseURL in class as an ephemeral answer that replaces nothing (C-24.FR-11),
// named response_url in the Request log. A responseURL the client may not call is not called. The response_url lets
// anyone who holds it post to the channel, so it is a secret of the request, masked in its record, and no Result
// carries it: its error is built from the status, Slack's error code or what failed, never from outbound's text, which
// shows the address.
func (c *Client) respond(ctx context.Context, class outbound.Class, responseURL, text string) Result {
	if class != outbound.ClassInteractive {
		return unknown("a response_url is answered only in the interactive class")
	}
	target, path, ok := c.responseTarget(responseURL)
	if !ok {
		return unknown("the response_url is not Slack's")
	}
	oc, err := c.hooksClient()
	if err != nil {
		return unknown("response_url: the client could not be built")
	}
	body, _ := json.Marshal(responseAnswer{ResponseType: "ephemeral", ReplaceOriginal: false, Text: text}) // strings
	ctx = outbound.WithSecrets(ctx, logging.Secret(responseURL), logging.Secret(target), logging.Secret("/"+path))
	code := ""
	refused := func(_ int, data []byte) (outbound.BodyError, bool) {
		var env struct {
			OK    *bool  `json:"ok"`
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &env) == nil && env.OK != nil && !*env.OK {
			code = env.Error
		}
		return outbound.BodyError{}, false
	}
	mapping := func(status int, d time.Duration, hasRetryAfter bool) outbound.Outcome {
		if code != "" && status >= http.StatusOK && status < http.StatusMultipleChoices {
			return outbound.OutcomeUnknown
		}
		return (&answerClass{}).mapping(status, d, hasRetryAfter)
	}
	res, err := oc.Do(ctx, outbound.Request{Method: http.MethodPost, URL: target,
		Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}, Body: body, Mapping: mapping,
		ClassifyBody: refused, Operation: "response_url"})
	return responded(res, err, code)
}

// responded is the Result of an answer to a response_url, whose answer reported the error code: its text names the
// status and the code, or what failed when nothing answered.
func responded(res outbound.Result, err error, code string) Result {
	r := Result{Status: res.Status}
	if err == nil {
		return r
	}
	switch res.Outcome {
	case outbound.OutcomeRetryAfter:
		r.Classification = Classification{Class: ClassRetryAfter, RetryAfter: res.RetryAfter}
	case outbound.OutcomeTransient:
		r.Classification = Classification{Class: ClassTransient}
	case outbound.OutcomeBlocked:
		r.Classification, r.Blocked = Classification{Class: ClassFatal}, true
	default:
		r.Classification = Classification{Class: ClassUnknown}
	}
	text := "response_url answered " + strconv.Itoa(res.Status)
	if responseError.MatchString(code) {
		text += ": " + code
		r.ErrorCode = code
	}
	if res.Status == 0 {
		text = "response_url: no answer"
		if oe, ok := errors.AsType[*outbound.Error](err); ok && oe.Network != "" {
			r.Network = oe.Network
			text = "response_url: " + oe.Network
		}
		if r.Blocked {
			text = "response_url: blocked by the outbound address policy"
		}
	}
	r.Error = outbound.Untrusted(text)
	return r
}

// SocketDomain is the domain whose hosts a Socket Mode address may be on outside `muster dev` (C-24.FR-9, ADR-0015).
const SocketDomain = "slack.com"

// The errors of a Socket Mode address the client may not connect to and of a client that could not be built; neither
// shows the address, whose query holds the ticket.
var (
	errSocketAddress = errors.New("the Socket Mode address is not Slack's")
	errSocketClient  = errors.New("the Socket Mode client could not be built")
)

// OpenSocket calls apps.connections.open with the app-level token in the background class (F-100) and returns the
// address of a new Socket Mode connection, with its single-use ticket, as a Secret: only an address that socketTarget
// accepts, built anew. Its record in the Request log has the ticket masked by its shape, and the answer's body is
// dropped from the Result, so that the ticket travels no further. An answer without an address is unknown, and so is
// one whose address the client may not connect to, without showing it.
func (c *Client) OpenSocket(ctx context.Context) (logging.Secret, Result) {
	r := c.CallApp(ctx, outbound.ClassBackground, "apps.connections.open", nil)
	if !r.OK() {
		return "", r
	}
	var out struct {
		URL string `json:"url"`
	}
	if r.Decode(&out) != nil || out.URL == "" {
		return "", notExpected(r, "apps.connections.open")
	}
	r.Body = nil
	target, ok := c.socketTarget(out.URL)
	if !ok {
		return "", Result{Status: r.Status, Classification: Classification{Class: ClassUnknown},
			Error: outbound.Untrusted("apps.connections.open: " + errSocketAddress.Error())}
	}
	return logging.Secret(target), r
}

// DialSocket opens the Socket Mode connection at address through outbound.DialWebSocket in the background class, with
// the Connection's proxy, and lets it read messages of up to MaxRequestBytes. An address socketTarget does not accept
// is not dialled. Its error never shows the address's query, nor the client's secrets.
func (c *Client) DialSocket(ctx context.Context, address logging.Secret) (*websocket.Conn, error) {
	target, ok := c.socketTarget(string(address))
	if !ok {
		return nil, errSocketAddress
	}
	oc, err := c.socketClient()
	if err != nil {
		return nil, errSocketClient
	}
	conn, err := outbound.DialWebSocket(ctx, oc, target, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(MaxRequestBytes)
	return conn, nil
}

// socketTarget is the address a Socket Mode connection may go to (C-24.FR-9), built anew, and whether it may: raw is a
// wss address on SocketDomain or a host below it, with the port 443 or none, without user information or a fragment;
// or, for a client that `muster dev` routes to the fake Slack server, a ws address for an http server — wss for https
// — on the server's host and port. The host is compared in ASCII only, as responseTarget does. Its path and query are
// kept as raw gave them.
func (c *Client) socketTarget(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" || strings.Contains(raw, "#") ||
		u.Host == "" || !ascii(u.Host) {
		return "", false
	}
	host, rest := strings.ToLower(u.Host), u.EscapedPath()
	if u.RawQuery != "" {
		rest += "?" + u.RawQuery
	}
	if u.Scheme == "wss" && slackSocketHost(host) {
		return "wss://" + host + rest, true
	}
	if c.base == BaseURL {
		return "", false
	}
	scheme, origin, _ := strings.Cut(c.origin, "://")
	socketScheme := map[string]string{"http": "ws", "https": "wss"}[strings.ToLower(scheme)]
	if socketScheme == "" || u.Scheme != socketScheme || host != strings.ToLower(origin) {
		return "", false
	}
	return socketScheme + "://" + origin + rest, true
}

// slackSocketHost reports whether host, lower-cased, is SocketDomain or a name below it, with the port 443 or none:
// every label of the name is letters, digits and hyphens.
func slackSocketHost(host string) bool {
	name := host
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		if host[i+1:] != "443" {
			return false
		}
		name = host[:i]
	}
	if name != SocketDomain && !strings.HasSuffix(name, "."+SocketDomain) {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" {
			return false
		}
		for i := range len(label) {
			if b := label[i]; (b < 'a' || b > 'z') && (b < '0' || b > '9') && b != '-' {
				return false
			}
		}
	}
	return true
}

// socketClient is the outbound client of the Socket Mode handshakes: the background class through the Connection's
// proxy, with the client's secrets and connect timeout, CallTimeout for a handshake, no base URL, and no Request log,
// since a handshake carries the ticket and nothing of the protocol.
func (c *Client) socketClient() (*outbound.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.socket != nil {
		return c.socket, nil
	}
	s, n := c.settings, c.network
	oc, err := outbound.New(outbound.Config{Class: outbound.ClassBackground, ConnectTimeout: connectTimeout,
		Timeout: CallTimeout, Proxy: s.Proxy, Secrets: []logging.Secret{s.BotToken, s.AppToken, s.SigningSecret},
		Policy: n.Policy, Logger: n.Log, Clock: n.Real, Resolver: n.Resolver, MaxBodyBytes: maxAnswerBytes})
	if err != nil {
		return nil, err
	}
	c.socket = oc
	return oc, nil
}
