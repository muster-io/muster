// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package bridge is Muster's client of the Bridge protocol (ADR-0017, C-22): requests to `<base>/v1/…` with the
// Connection's token as a bearer token, through internal/outbound with the Connection's proxy, in the client class of
// the caller (ADR-0015), with the token registered for redaction; the classification of every answer by the problem's
// class or the protocol's status mapping (classify.go); and `info` with Muster's copy of it (info.go). The models are
// generated from api/bridge.yaml into internal/bridge/gen.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

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
	clients map[outbound.Class]*outbound.Client
}

// NewClient builds the client of the Connection s, checking its settings with the interactive class. The token, the
// push secret and the proxy password are registered for redaction.
func NewClient(n Network, s Settings) (*Client, error) {
	c := &Client{network: n, settings: s, via: ViaDirect, clients: map[outbound.Class]*outbound.Client{}}
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

func (c *Client) client(class outbound.Class) (*outbound.Client, error) {
	if class != outbound.ClassInteractive && class != outbound.ClassDelivery && class != outbound.ClassBackground {
		return nil, fmt.Errorf("the Bridge client has no %s class", class)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if oc, ok := c.clients[class]; ok {
		return oc, nil
	}
	s, n := c.settings, c.network
	oc, err := outbound.New(outbound.Config{Class: class, ConnectTimeout: connectTimeout, Timeout: callTimeout,
		Proxy: s.Proxy, Secrets: s.secrets(), BaseURL: s.BaseURL, Policy: n.Policy, Logger: n.Log,
		Clock: n.Real, Resolver: n.Resolver, MaxBodyBytes: maxAnswerBytes})
	if err != nil {
		return nil, err
	}
	c.clients[class] = oc
	return oc, nil
}

// Result is how a call ended; a zero Classification is success. Error is the masked text of a failure: the problem's
// detail, or its title, when the bridge sent one, otherwise what outbound reports. Code is the check code the protocol
// names for the failure (token_invalid, protocol_version_unknown, protocol_mismatch), empty otherwise; Network is what
// failed when the call got no answer (outbound.Network*), and Blocked whether the outbound address policy refused it.
type Result struct {
	Status         int
	Classification Classification
	Error          outbound.Untrusted
	Code           string
	Network        string
	Blocked        bool
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
// which ends the delivery as Not delivered.
func (r Result) Outcome() delivery.Outcome {
	o := delivery.Outcome{Error: r.Error, Status: r.Status}
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

// do makes one call of method on path below `<base>/v1/` in the client class, with body as JSON unless it is nil and
// the extra header, such as Idempotency-Key, and reads a successful answer into out unless it is nil. It returns the
// outbound result, whose body the caller may read, and how the call ended.
func (c *Client) do(ctx context.Context, class outbound.Class, method, path string, header http.Header, body,
	out any) (outbound.Result, Result) {
	oc, err := c.client(class)
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
				Error: outbound.Untrusted(method + " " + path + ": the request is not valid JSON")}
		}
		h.Set("Content-Type", "application/json")
	}
	res, err := oc.Do(ctx, outbound.Request{Method: method, URL: "v1/" + path, Header: h, Body: data,
		Mapping: mapping, ClassifyBody: problemText})
	r := c.resultOf(res, err)
	if !r.OK() || out == nil {
		return res, r
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return res, Result{Status: res.Status, Classification: Classification{Class: ClassInvalid},
			Error: outbound.Untrusted(method + " " + path + ": the answer is not the expected JSON")}
	}
	return res, r
}

// resultOf is the Result of an outbound call: without an answer it is transient — a request that could not be built
// is invalid and a blocked address forbidden — and an error answer is classified by Classify, with the masked text of
// its problem.
func (c *Client) resultOf(res outbound.Result, err error) Result {
	r := Result{Status: res.Status}
	if err == nil {
		return r
	}
	r.Error = outbound.Untrusted(err.Error())
	switch {
	case res.Status == 0:
		if oe, ok := errors.AsType[*outbound.Error](err); ok {
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
		}
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
