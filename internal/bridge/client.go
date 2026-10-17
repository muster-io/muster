// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package bridge is Muster's client of the Bridge protocol (ADR-0017, C-22): requests to `<base>/v1/…` with the
// Connection's token as a bearer token, through internal/outbound with the Connection's proxy, in the client class of
// the caller (ADR-0015), with the token registered for redaction; the classification of every answer by the problem's
// class or the protocol's status mapping (classify.go); `info` with Muster's copy of it (info.go); the Destination
// check (check.go); the Bridge delivery, which posts, edits and replies to messages as envelopes (delivery.go,
// envelope.go); the events of the Connections, polled by the Leader or pushed to the ingest listener, handled once per
// id and answered through answerEvent (events.go, poller.go, push.go, presses.go, commands.go); the Account links by a
// token sent to the bot or a code sent by direct message (links.go); and the conformance check `muster bridge check`,
// which runs the protocol against a bridge outside any installation and also deletes its message (conformance.go). The
// models are generated from api/bridge.yaml into internal/bridge/gen.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// The timeouts of a call and the largest answer read; info has its own, smaller cap (InfoMaxBytes).
const (
	connectTimeout = 5 * time.Second
	callTimeout    = 10 * time.Second
	maxAnswerBytes = 4 << 20
)

// ProtocolVersion is the version of the Bridge protocol this Muster speaks.
const ProtocolVersion = "1"

// CallbackPath is the path below MUSTER_INGEST_URL where a bridge in push mode posts the events of a Connection,
// followed by its public_id (C-22.FR-11).
const CallbackPath = "/api/v1/callbacks/bridge/"

// The paths of a Connection's requests (ConnectionPath).
const (
	ViaDirect = "direct"
	ViaProxy  = "proxy"
)

// problemType is the media type of an error answer that the protocol describes.
const problemType = "application/problem+json"

// maxRetryAfter bounds a Retry-After in seconds that is read at all; bridge.retry_after_max applies in Outcome.
const maxRetryAfter = 365 * 24 * time.Hour

// DefaultRetryAfterMax is bridge.retry_after_max: a rate_limited answer that asks for a longer wait counts as
// Transient.
const DefaultRetryAfterMax = 15 * time.Minute

// MessageProxyRefused is the message of a request that the Connection's proxy refused with 407 (D338), as Telegram's
// check says it.
const MessageProxyRefused = "The proxy refused the connection (407)"

// maxID is the longest message_id and thread_ref of the protocol, in characters.
const maxID = 512

// ErrBaseURL is a base URL that breaks the rules of C-22.FR-1.
var ErrBaseURL = errors.New("the base URL is not an absolute http or https URL without user information, query or " +
	"fragment")

// ParseBaseURL checks the base URL of a bridge (C-22.FR-1) and returns it normalized: an absolute http or https URL
// with a host and without user information, query or fragment, whose path prefix is kept without its trailing
// slashes, so that requests go to `<base>/v1/…`.
func ParseBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "?#") {
		return "", ErrBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" ||
		u.User != nil || u.Opaque != "" {
		return "", ErrBaseURL
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
}

// Segment is id percent-encoded as one path segment, as the protocol sends a message_id or a channel_id; a segment
// of dots only is encoded too, so that no client or server takes `.` or `..` for a step in the path.
func Segment(id string) string {
	if id != "" && strings.Trim(id, ".") == "" {
		return strings.Repeat("%2E", len(id))
	}
	return url.PathEscape(id)
}

// Network is what a Client needs besides its Connection: the outbound address policy, the logger, the real clock of
// the outbound metrics and of Retry-After dates, and a resolver (nil: the system resolver). Its fields are those of
// mattermost.Network, so that one converts into the other.
type Network struct {
	Policy   outbound.PolicySource
	Log      *logging.Logger
	Real     clock.Clock
	Resolver outbound.Resolver
}

// Settings are a Connection as its client needs it: the normalized base URL, the token, the push secret, which the
// bridge knows too and is masked like the token, and the proxy, nil for none.
type Settings struct {
	BaseURL    string
	Token      logging.Secret
	PushSecret logging.Secret
	Proxy      *outbound.Proxy
}

// Client calls the bridge of one Connection, in the interactive, delivery and background client classes; the
// outbound client of a class is built on its first call.
type Client struct {
	network  Network
	settings Settings
	via      string

	mu      sync.Mutex
	clients map[clientKey]*outbound.Client
}

// clientKey names an outbound client: its class, and whether it makes long polls, which take longer.
type clientKey struct {
	class outbound.Class
	long  bool
}

// timeout bounds each attempt of the client k: callTimeout, or for long polls PollWait plus PollMargin, so that the
// client's timeout is never shorter than the deadline the poller gives a call.
func (k clientKey) timeout() time.Duration {
	if k.long {
		return PollWait + PollMargin
	}
	return callTimeout
}

// NewClient builds the client of the Connection s, checking its settings with the interactive class. The token, the
// push secret and the proxy password are registered for redaction.
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

func (s Settings) secrets() []logging.Secret { return []logging.Secret{s.Token, s.PushSecret} }

// mask replaces the token and the push secret in a text of the bridge that outbound did not mask, the longer first.
func (s Settings) mask(text string) string {
	secrets := s.secrets()
	slices.SortFunc(secrets, func(a, b logging.Secret) int { return len(b) - len(a) })
	for _, v := range secrets {
		if v != "" {
			text = strings.ReplaceAll(text, string(v), "[redacted]")
		}
	}
	return text
}

func (c *Client) client(k clientKey) (*outbound.Client, error) {
	if k.class != outbound.ClassInteractive && k.class != outbound.ClassDelivery && k.class != outbound.ClassBackground {
		return nil, fmt.Errorf("the Bridge client has no %s class", k.class)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if oc, ok := c.clients[k]; ok {
		return oc, nil
	}
	s, n := c.settings, c.network
	oc, err := outbound.New(outbound.Config{Class: k.class, ConnectTimeout: connectTimeout, Timeout: k.timeout(),
		Proxy: s.Proxy, Secrets: s.secrets(), BaseURL: s.BaseURL, Policy: n.Policy, Logger: n.Log,
		Clock: n.Real, Resolver: n.Resolver, MaxBodyBytes: maxAnswerBytes})
	if err != nil {
		return nil, err
	}
	c.clients[k] = oc
	return oc, nil
}

// Result is how a call ended; a zero Classification is success. Error is the masked text of a failure: the problem's
// detail, or its title, when the bridge sent one, otherwise what outbound reports. Code is the check code the protocol
// names for the failure (token_invalid, protocol_version_unknown, protocol_mismatch), empty otherwise; Network is what
// failed when the call got no answer (outbound.Network*), and Blocked whether the outbound address policy refused it.
// Unanswered is a call without an answer whose request may have reached the bridge: one written in full before the
// answer was lost to a timeout or a lost connection — not one that failed while connecting, which sent nothing.
type Result struct {
	Status         int
	Classification Classification
	Error          outbound.Untrusted
	Code           string
	Network        string
	Blocked        bool
	Unanswered     bool
}

// OK reports whether the call succeeded.
func (r Result) OK() bool { return r.Classification.Class == "" }

// CheckCode is the check code of a failed call: the one the protocol names, otherwise that of delivery.FailureCode;
// empty for a call that succeeded.
func (r Result) CheckCode() string {
	switch {
	case r.OK():
		return ""
	case r.Code != "":
		return r.Code
	}
	return delivery.FailureCode(r.Blocked, r.Network)
}

// Outcome is the Result for delivery (C-22.FR-8): rate_limited waits for the channel's Destination or, with the scope
// bot, for every Destination of the Connection, and is Transient without a Retry-After or with one longer than
// DefaultRetryAfterMax, so that a long pause ends in Broken; forbidden is Fatal, gone is Gone and invalid is unknown,
// which ends the delivery as Not delivered. Unanswered is kept, so that delivery knows the request may have been
// posted.
func (r Result) Outcome() delivery.Outcome {
	o := delivery.Outcome{Error: r.Error, Status: r.Status, Unanswered: r.Unanswered}
	switch r.Classification.Class {
	case "":
		o.Kind = delivery.OutcomeOK
	case ClassRateLimited:
		if !r.Classification.HasRetryAfter || r.Classification.RetryAfter > DefaultRetryAfterMax {
			o.Kind = delivery.OutcomeTransient
			break
		}
		o.Kind, o.RetryAfter, o.Scope = delivery.OutcomeRetryAfter, r.Classification.RetryAfter, delivery.ScopeDestination
		if r.Classification.Scope == ScopeBot {
			o.Scope = delivery.ScopeConnection
		}
	case ClassForbidden:
		o.Kind = delivery.OutcomeFatal
	case ClassGone:
		o.Kind = delivery.OutcomeGone
	case ClassInvalid:
		o.Kind = delivery.OutcomeUnknown
	default:
		o.Kind = delivery.OutcomeTransient
	}
	return o
}

// maxChannelName is the longest channel name checkChannel answers with (api/bridge.yaml ChannelCheck).
const maxChannelName = 200

// CheckChannel calls checkChannel for channelID in class (C-22.FR-3), which posts nothing: the channel's name, masked
// of the token and the push secret, when the bridge answers 200 with a name of 1 to 200 characters without NUL. Any
// other answer is a failed Result; a 200 without such a name is invalid.
func (c *Client) CheckChannel(ctx context.Context, class outbound.Class, channelID string) (string, Result) {
	var out gen.ChannelCheck
	_, r := c.do(ctx, class, http.MethodPost, "channels/"+Segment(channelID)+"/check", nil, nil, &out)
	if !r.OK() {
		return "", r
	}
	if n := utf8.RuneCountInString(out.Name); n < 1 || n > maxChannelName || strings.ContainsRune(out.Name, 0) {
		return "", Result{Status: r.Status, Classification: Classification{Class: ClassInvalid},
			Error: "POST channels/{channel_id}/check: the channel's name is not 1 to 200 characters without NUL"}
	}
	return c.settings.mask(out.Name), r
}

// PostMessage calls postMessage (C-22.FR-4, FR-6): it posts the envelope in to its channel with the idempotency key,
// sent as Idempotency-Key unless it is empty. A successful answer without a message_id of 1 to 512 characters, or with
// a longer thread_ref, is invalid.
func (c *Client) PostMessage(ctx context.Context, class outbound.Class, key string, in gen.MessageInput) (
	gen.MessagePosted, Result) {
	out, _, r := c.postMessage(ctx, class, key, in)
	return out, r
}

// postMessage is PostMessage that also returns the outbound result, whose answer a test message shows.
func (c *Client) postMessage(ctx context.Context, class outbound.Class, key string, in gen.MessageInput) (
	gen.MessagePosted, outbound.Result, Result) {
	var out gen.MessagePosted
	res, r := c.do(ctx, class, http.MethodPost, "messages", idempotency(key), in, &out)
	if r.OK() && (!validID(out.MessageId) || utf8.RuneCountInString(out.ThreadRef.GetOrEmpty()) > maxID) {
		return gen.MessagePosted{}, res, notExpected(r.Status, "POST messages")
	}
	return out, res, r
}

// EditMessage calls editMessage (C-22.FR-6): it replaces the message messageID with the envelope in, which has no
// Mentions and is always Quiet.
func (c *Client) EditMessage(ctx context.Context, class outbound.Class, messageID string, in gen.MessageContent) (
	gen.MessageEdited, Result) {
	var out gen.MessageEdited
	_, r := c.do(ctx, class, http.MethodPatch, "messages/"+Segment(messageID), nil, in, &out)
	return out, r
}

// ReplyInThread calls replyInThread (C-22.FR-6): it posts the reply in into the Thread of the message messageID with
// the idempotency key, sent as Idempotency-Key unless it is empty. A successful answer without a message_id and a
// thread_ref of 1 to 512 characters each is invalid.
func (c *Client) ReplyInThread(ctx context.Context, class outbound.Class, messageID, key string,
	in gen.ThreadReplyInput) (gen.ThreadReplyPosted, Result) {
	var out gen.ThreadReplyPosted
	_, r := c.do(ctx, class, http.MethodPost, "messages/"+Segment(messageID)+"/thread-replies", idempotency(key), in,
		&out)
	if r.OK() && (!validID(out.MessageId) || !validID(out.ThreadRef)) {
		return gen.ThreadReplyPosted{}, notExpected(r.Status, "POST messages/{message_id}/thread-replies")
	}
	return out, r
}

// DeleteMessage calls deleteMessage in class: it deletes the message messageID of the bot, and a message that no
// longer exists is a success too. Only the conformance check calls it (C-22.FR-21); Muster L1 never deletes a message.
func (c *Client) DeleteMessage(ctx context.Context, class outbound.Class, messageID string) Result {
	_, r := c.do(ctx, class, http.MethodDelete, "messages/"+Segment(messageID), nil, nil, nil)
	return r
}

// maxCursor is the longest next_cursor of the protocol, in characters.
const maxCursor = 1024

// Batch is the answer of pollEvents: the events after the cursor sent, oldest first, each as the bridge sent it, and
// the cursor of the next call.
type Batch struct {
	Events     []json.RawMessage
	NextCursor string
}

// PollEvents calls pollEvents (C-22.FR-11): the events after cursor, all the bridge keeps when cursor is nil, which the
// bridge holds open for wait while there are none. It runs in the background client class through the client that
// makes long polls, whose timeout is PollWait plus PollMargin, and the caller's context bounds the call with the
// retries of the class. A successful answer without an array of events and a next_cursor of at most 1024 characters
// is invalid.
func (c *Client) PollEvents(ctx context.Context, cursor *string, wait time.Duration) (Batch, Result) {
	q := url.Values{"wait": {strconv.FormatInt(int64(wait/time.Second), 10)}}
	if cursor != nil {
		q.Set("cursor", *cursor)
	}
	var out struct {
		Events     *[]json.RawMessage `json:"events"`
		NextCursor *string            `json:"next_cursor"`
	}
	_, r := c.call(ctx, clientKey{class: outbound.ClassBackground, long: true}, http.MethodGet,
		"events?"+q.Encode(), nil, nil, &out)
	if !r.OK() {
		return Batch{}, r
	}
	if out.Events == nil || out.NextCursor == nil || utf8.RuneCountInString(*out.NextCursor) > maxCursor {
		return Batch{}, notExpected(r.Status, "GET events")
	}
	return Batch{Events: *out.Events, NextCursor: *out.NextCursor}, r
}

// MaxAnswerLength is the longest text of an answer to an event, in characters (C-22.FR-13).
const MaxAnswerLength = 500

// AnswerEvent calls answerEvent (C-22.FR-13): it shows text, a plain text cut to MaxAnswerLength characters, to the
// person behind the event eventID, with the Idempotency-Key answer-<event id>, and returns where the bridge showed it.
// A successful answer without a shown_as of the protocol is invalid.
func (c *Client) AnswerEvent(ctx context.Context, class outbound.Class, eventID, text string) (gen.AnswerShown,
	Result) {
	var out gen.AnswerShown
	_, r := c.do(ctx, class, http.MethodPost, "answers", idempotency("answer-"+eventID),
		gen.AnswerInput{EventId: eventID, Text: cutText(text, MaxAnswerLength)}, &out)
	if r.OK() && !out.ShownAs.Valid() {
		return gen.AnswerShown{}, notExpected(r.Status, "POST answers")
	}
	return out, r
}

// MaxDirectMessageLength is the longest body of a direct message, in characters (api/bridge.yaml).
const MaxDirectMessageLength = 2000

// FindUsers calls findUsers (C-22.FR-15) in class for the account with handle: the one person the bridge found, and
// false for none. A successful answer without a list of at most one person within the protocol's bounds is invalid.
// The handle travels in the query, which the texts of a failure leave out.
func (c *Client) FindUsers(ctx context.Context, class outbound.Class, handle string) (gen.Person, bool, Result) {
	var out struct {
		Items *[]json.RawMessage `json:"items"`
	}
	_, r := c.do(ctx, class, http.MethodGet, "users?"+url.Values{"handle": {handle}}.Encode(), nil, nil, &out)
	if !r.OK() {
		return gen.Person{}, false, r
	}
	if out.Items == nil || len(*out.Items) > 1 {
		return gen.Person{}, false, notExpected(r.Status, "GET users")
	}
	if len(*out.Items) == 0 {
		return gen.Person{}, false, r
	}
	raw := (*out.Items)[0]
	var p gen.Person
	if json.Unmarshal(raw, &p) != nil || checkPerson(raw, p) != nil {
		return gen.Person{}, false, notExpected(r.Status, "GET users")
	}
	return p, true, r
}

// SendDirectMessage calls sendDirectMessage (C-22.FR-15) in class with the Idempotency-Key key: body, CommonMark cut
// to MaxDirectMessageLength characters, to the account externalID, in the direct chat the bridge opens if needed. A
// successful answer without a message_id is invalid.
func (c *Client) SendDirectMessage(ctx context.Context, class outbound.Class, key, externalID, body string) (
	gen.DirectMessageSent, Result) {
	var out gen.DirectMessageSent
	_, r := c.do(ctx, class, http.MethodPost, "direct-messages", idempotency(key),
		gen.DirectMessageInput{UserExternalId: externalID, Body: cutText(body, MaxDirectMessageLength)}, &out)
	if r.OK() && out.MessageId == "" {
		return gen.DirectMessageSent{}, notExpected(r.Status, "POST direct-messages")
	}
	return out, r
}

// cutText is s cut to at most n characters.
func cutText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// idempotency is the header that carries key, none for an empty key.
func idempotency(key string) http.Header {
	if key == "" {
		return nil
	}
	return http.Header{"Idempotency-Key": {key}}
}

// validID reports whether id is a message_id or thread_ref of the protocol: 1 to 512 characters.
func validID(id string) bool {
	n := utf8.RuneCountInString(id)
	return n >= 1 && n <= maxID
}

// notExpected is the Result of a successful status whose answer does not match the protocol.
func notExpected(status int, call string) Result {
	return Result{Status: status, Classification: Classification{Class: ClassInvalid},
		Error: outbound.Untrusted(call + ": the answer is not the expected JSON")}
}

// do makes one call of method on path below `<base>/v1/` in the client class, with body as JSON unless it is nil and
// the extra header, such as Idempotency-Key, and reads a successful answer into out unless it is nil. It returns the
// outbound result, whose body the caller may read, and how the call ended. Whether the request was written is traced
// on the request's context: a call without an answer whose request was written may have reached the bridge.
func (c *Client) do(ctx context.Context, class outbound.Class, method, path string, header http.Header, body,
	out any) (outbound.Result, Result) {
	return c.call(ctx, clientKey{class: class}, method, path, header, body, out)
}

// call is do through the outbound client k; a query of path is left out of the texts of its failures.
func (c *Client) call(ctx context.Context, k clientKey, method, path string, header http.Header, body,
	out any) (outbound.Result, Result) {
	name, _, _ := strings.Cut(path, "?")
	oc, err := c.client(k)
	if err != nil {
		return outbound.Result{}, Result{Classification: Classification{Class: ClassInvalid},
			Error: outbound.Untrusted(err.Error())}
	}
	h := header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Authorization", "Bearer "+string(c.settings.Token))
	h.Set("Accept", "application/json, "+problemType)
	var data []byte
	if body != nil {
		if data, err = json.Marshal(body); err != nil {
			return outbound.Result{}, Result{Classification: Classification{Class: ClassInvalid},
				Error: outbound.Untrusted(method + " " + name + ": the request is not valid JSON")}
		}
		h.Set("Content-Type", "application/json")
	}
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(w httptrace.WroteRequestInfo) {
		if w.Err == nil {
			wrote.Store(true)
		}
	}})
	res, err := oc.Do(ctx, outbound.Request{Method: method, URL: "v1/" + path, Header: h, Body: data,
		Mapping: mapping, ClassifyBody: problemText})
	r := c.resultOf(res, err, wrote.Load())
	if !r.OK() || out == nil {
		return res, r
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return res, Result{Status: res.Status, Classification: Classification{Class: ClassInvalid},
			Error: outbound.Untrusted(method + " " + name + ": the answer is not the expected JSON")}
	}
	return res, r
}

// resultOf is the Result of an outbound call: without an answer it is transient — a request that could not be built
// is invalid and a blocked address forbidden — and Unanswered when its request was written (wrote); an error answer
// is classified by Classify, with the masked text of its problem. A 407 through the Connection's proxy is the proxy's
// refusal, not an answer of the bridge (D338): a call without an answer, as the HTTPS CONNECT that a proxy refuses is.
func (c *Client) resultOf(res outbound.Result, err error, wrote bool) Result {
	r := Result{Status: res.Status}
	if err == nil {
		return r
	}
	r.Error = outbound.Untrusted(err.Error())
	switch {
	case res.Status == 0:
		oe, _ := errors.AsType[*outbound.Error](err)
		if oe != nil {
			r.Network = oe.Network
		}
		r.Blocked = res.Outcome == outbound.OutcomeBlocked
		switch {
		case r.Blocked:
			r.Classification = Classification{Class: ClassForbidden}
		case res.Outcome == outbound.OutcomeFatal:
			r.Classification = Classification{Class: ClassInvalid}
		default:
			r.Classification = Classify(0, 0, false, nil)
			r.Unanswered = wrote
		}
	case res.Status == http.StatusProxyAuthRequired && c.settings.Proxy != nil:
		r.Status, r.Network, r.Error = 0, outbound.NetworkProxyAuth, MessageProxyRefused
		r.Classification = Classify(0, 0, false, nil)
	case res.Status < http.StatusMultipleChoices, res.Outcome == outbound.OutcomeRedirect:
		// A successful status whose answer was not read (larger than maxAnswerBytes), and a redirect, never followed.
		r.Classification = Classification{Class: ClassInvalid}
	default:
		p := problemOf(res)
		delay, has := retryAfter(res.Header, c.network.Real.Now())
		r.Classification = Classify(res.Status, delay, has, p)
		if p != nil && textOf(*p) != "" {
			r.Error = res.ProviderError
		}
	}
	return r
}

// problemOf is the problem of an error answer, when it is one: application/problem+json that decodes.
func problemOf(res outbound.Result) *gen.Problem {
	mt, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || mt != problemType {
		return nil
	}
	var p gen.Problem
	if json.Unmarshal(res.Body, &p) != nil {
		return nil
	}
	return &p
}

// problemText hands outbound the text of an error answer that looks like a problem — its detail, or its title — so
// that outbound masks it into ProviderError; a successful answer reports no error.
func problemText(status int, body []byte) (outbound.BodyError, bool) {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return outbound.BodyError{}, false
	}
	var p gen.Problem
	if json.Unmarshal(body, &p) != nil || textOf(p) == "" {
		return outbound.BodyError{}, false
	}
	return outbound.BodyError{Code: status, Text: textOf(p)}, true
}

// textOf is the text of a problem that a person reads: its detail, or its title.
func textOf(p gen.Problem) string {
	if p.Detail != nil && *p.Detail != "" {
		return *p.Detail
	}
	if p.Title != nil {
		return *p.Title
	}
	return ""
}

// mapping classifies a status for outbound — its metrics and the retries of the background class — as the protocol
// does without a problem (ADR-0017): 429, and 503 with Retry-After, wait; 408 and other 5xx are transient; 401 and
// 403 fatal; anything else unknown.
func mapping(status int, _ time.Duration, hasRetryAfter bool) outbound.Outcome {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return outbound.OutcomeOK
	case status == http.StatusTooManyRequests, status == http.StatusServiceUnavailable && hasRetryAfter:
		return outbound.OutcomeRetryAfter
	case status == http.StatusRequestTimeout, status >= 500 && status < 600:
		return outbound.OutcomeTransient
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return outbound.OutcomeFatal
	}
	return outbound.OutcomeUnknown
}

// retryAfter reads Retry-After as seconds or as an HTTP date, relative to now; a date in the past is no delay.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 || n > int64(maxRetryAfter/time.Second) {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	return max(t.Sub(now), 0), true
}
