// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package slack is Muster's client of Slack's Web API (C-24): calls of `https://slack.com/api/<method>` with a
// Connection's bot token or app-level token as a bearer token, through internal/outbound with the Connection's proxy in
// the caller's client class (ADR-0015), JSON for the write methods and form-encoded for the others, with both tokens and
// the Slack signing secret registered for redaction; the classification of every answer by Slack's `error`
// (classify.go); the Connection check (check.go); the Destination check of a channel and the channel list
// (channels.go); and delivery (adapter.go), whose messages are laid out in blocks (layout.go) and sent with the
// unexported chat calls of this file. Later stories add presses, Socket Mode and Account links.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
