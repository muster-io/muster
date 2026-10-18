// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package outbound is the only code that makes outbound HTTP requests (ADR-0015, C-02.FR-20 to FR-22): clients are
// built per client class with a connect timeout and an overall timeout, never follow redirects, name Muster and its
// version in the User-Agent of every request and every CONNECT to a proxy, classify every answer into an outcome,
// retry by the policy of their class, redact registered secrets from everything they log and return, count their
// requests in the muster_client_* metrics, and connect only to addresses that pass the Organization's outbound address
// policy, directly or through their own proxy. No other package creates an http.Client or an http.Transport
// (architecture lint 4).
package outbound

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muster-io/muster/internal/buildinfo"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// DefaultMaxBodyBytes is how much of an answer a client reads unless its configuration says otherwise.
const DefaultMaxBodyBytes = 8 << 20

// maxResponseHeaderBytes bounds the header of every answer, instead of the 10 MB net/http allows: an answer with a
// larger header fails as a transport error like any other answer that cannot be read, and no record of the Request
// log holds more of an answer's header than this.
const maxResponseHeaderBytes = 64 << 10

// minSecretPrefix is the length in bytes from which the path prefix of a base URL, without its leading and trailing
// slashes (inner slashes count), is masked wherever a record holds it, in its headers and bodies as in its URL: a
// shorter prefix, such as /api/v4 or /v1, is a route rather than a secret, and replacing it everywhere would garble
// the bodies that name it.
const minSecretPrefix = 16

// userAgent is the User-Agent of every request that does not set its own, and of every CONNECT to a proxy: Muster and
// its version, nothing else.
var userAgent = userAgentOf(buildinfo.Version)

// userAgentOf is the User-Agent for version, without the v that a tag may carry.
func userAgentOf(version string) string {
	return "Muster/" + cmp.Or(strings.TrimPrefix(version, "v"), "unknown")
}

// Config is the configuration of one client.
type Config struct {
	Class Class
	// ConnectTimeout bounds name resolution, the connection and the TLS handshake, to the proxy when there is one.
	ConnectTimeout time.Duration
	// Timeout bounds each attempt, from the dial to the end of the answer's body.
	Timeout time.Duration
	// Proxy is the client's own proxy; nil connects directly.
	Proxy *Proxy
	// Secrets are replaced with [redacted] in every log line and every returned error; the proxy's password is added.
	Secrets []logging.Secret
	// BaseURL, when set, is the only address the client sends to: request URLs are paths below it, its own path
	// prefix included.
	BaseURL string
	// Policy gives the outbound address policy in force.
	Policy PolicySource
	Logger *logging.Logger
	// Clock is the real clock: Retry-After dates and the duration of requests.
	Clock clock.Clock
	// Resolver resolves host names; nil is the system resolver.
	Resolver Resolver
	// MaxBodyBytes is the largest answer read; 0 is DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// Recorder, with an Owner that names a Connection or an outgoing webhook Destination, gets the record of each
	// attempt (C-23.FR-1); without either the client records nothing.
	Recorder Recorder
	Owner    Owner
}

// policyGeneration moves each time the outbound address policy may have changed (CloseIdleConnections).
var policyGeneration atomic.Uint64

// CloseIdleConnections makes every client use a new transport from its next request on, so that the request dials
// again and its address is checked against the policy in force: the policy is checked when a connection is dialled,
// and a pooled connection opened under an older policy, idle or still serving another request, would otherwise carry
// a request past a stricter one. Each client compares the generation this moves with the one its transport was built
// for; when it moved, the client builds a fresh transport and closes the idle connections of the old one. Requests in
// flight finish on the old transport, which no new request reaches, and its remaining connections close when idle.
// Nothing keeps the clients, so a client its owner dropped is not kept alive.
func CloseIdleConnections() { policyGeneration.Add(1) }

// generationTransport is a transport and the policy generation it was built for.
type generationTransport struct {
	generation uint64
	tr         *http.Transport
}

// transports is the round tripper of a client: it sends each request through the transport of the current policy
// generation, building a new one when the generation moved.
type transports struct {
	mu      sync.Mutex
	current atomic.Pointer[generationTransport]
	build   func() *http.Transport
}

func newTransports(build func() *http.Transport) *transports {
	t := &transports{build: build}
	t.current.Store(&generationTransport{generation: policyGeneration.Load(), tr: build()})
	return t
}

// transport is the transport of the current policy generation.
func (t *transports) transport() *http.Transport {
	if cur := t.current.Load(); cur.generation == policyGeneration.Load() {
		return cur.tr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	g := policyGeneration.Load()
	cur := t.current.Load()
	if cur.generation == g {
		return cur.tr
	}
	next := &generationTransport{generation: g, tr: t.build()}
	t.current.Store(next)
	cur.tr.CloseIdleConnections()
	return next.tr
}

func (t *transports) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.transport().RoundTrip(req)
}

func (t *transports) CloseIdleConnections() { t.current.Load().tr.CloseIdleConnections() }

// Client makes the requests of one client configuration.
type Client struct {
	cfg        Config
	base       *url.URL
	http       *http.Client
	transports *transports
	resolver   Resolver
	// secrets are those of the configuration and the proxy's password, which redactor replaces.
	secrets  []logging.Secret
	redactor redactor
	// recordSecrets are secrets and the base URL's path prefix of at least minSecretPrefix bytes, which
	// recordRedactor replaces in a record; a record adds the request's own.
	recordSecrets  []logging.Secret
	recordRedactor redactor
	maxBody        int64
	// sleep waits between the attempts of the background class, and jitter spreads its backoff; tests replace both.
	sleep  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

// New builds a client from cfg.
func New(cfg Config) (*Client, error) {
	secrets := slices.Clone(cfg.Secrets)
	if cfg.Proxy != nil {
		secrets = append(secrets, cfg.Proxy.Password)
	}
	c := &Client{cfg: cfg, secrets: secrets, redactor: newRedactor(secrets), resolver: cfg.Resolver,
		maxBody: cfg.MaxBodyBytes, sleep: sleepContext, jitter: randomJitter}
	if err := c.build(); err != nil {
		return nil, errors.New(c.redact("outbound client: " + err.Error()))
	}
	c.recordSecrets = secrets
	if c.base != nil {
		if prefix := strings.Trim(c.base.Path, "/"); len(prefix) >= minSecretPrefix {
			c.recordSecrets = append(slices.Clone(secrets), logging.Secret(prefix))
		}
	}
	c.recordRedactor = newRedactor(c.recordSecrets)
	return c, nil
}

func (c *Client) build() error {
	cfg := c.cfg
	switch {
	case !slices.Contains(Classes, cfg.Class):
		return fmt.Errorf("class %q is not delivery, interactive, background or heartbeat", cfg.Class)
	case cfg.ConnectTimeout <= 0 || cfg.Timeout <= 0:
		return errors.New("the connect timeout and the overall timeout must be positive")
	case cfg.Policy == nil || cfg.Logger == nil || cfg.Clock == nil:
		return errors.New("the policy source, the logger and the clock are required")
	}
	if c.resolver == nil {
		c.resolver = &net.Resolver{}
	}
	if c.maxBody <= 0 {
		c.maxBody = DefaultMaxBodyBytes
	}
	if cfg.BaseURL != "" {
		u, err := parseTarget(cfg.BaseURL)
		if err != nil {
			return fmt.Errorf("base URL: %w", err)
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return errors.New("base URL: it has a query or a fragment")
		}
		c.base = u
	}
	d := &dialer{policy: cfg.Policy, resolver: c.resolver, timeout: cfg.ConnectTimeout, proxy: cfg.Proxy != nil}
	// Proxy stays nil without a proxy of the client's own, so the environment is never consulted.
	var proxy func(*http.Request) (*url.URL, error)
	if cfg.Proxy != nil {
		pu, err := cfg.Proxy.url()
		if err != nil {
			return err
		}
		proxy = c.proxyFunc(pu)
	}
	c.transports = newTransports(func() *http.Transport {
		return &http.Transport{
			DialContext:            d.DialContext,
			Proxy:                  proxy,
			ProxyConnectHeader:     http.Header{"User-Agent": {userAgent}},
			ForceAttemptHTTP2:      true,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    cfg.ConnectTimeout,
			IdleConnTimeout:        30 * time.Second,
			MaxIdleConnsPerHost:    4,
			MaxResponseHeaderBytes: maxResponseHeaderBytes,
		}
	})
	c.http = &http.Client{
		Transport:     c.transports,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return nil
}

func parseTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("it is not a URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("it is not an absolute http or https URL")
	}
	return u, nil
}

func (c *Client) redact(s string) string { return c.redactor.redact(s) }

// networkOf is what failed in a request that got no answer.
func networkOf(err error) string {
	var dns *net.DNSError
	var ne net.Error
	var cert *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	text := err.Error()
	switch {
	case errors.As(err, &dns):
		return NetworkDNS
	case errors.As(err, &cert), errors.As(err, &record), errors.As(err, &alert), errors.As(err, &unknown),
		errors.As(err, &host), errors.As(err, &invalid), strings.Contains(text, "tls: "):
		return NetworkTLS
	// Go reports a CONNECT that the proxy answered 407 with the status text alone, and the SOCKS5 dialer a refused
	// username and password with this text.
	case strings.Contains(text, http.StatusText(http.StatusProxyAuthRequired)),
		strings.Contains(text, "username/password authentication failed"):
		return NetworkProxyAuth
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return NetworkTimeout
	}
	return NetworkConnect
}

// Request is one request. URL is absolute, or a path below the base URL when the client has one.
type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
	// Mapping replaces DefaultMapping.
	Mapping Mapping
	// ClassifyBody finds errors the provider reports in the body; nil reads none.
	ClassifyBody BodyClassifier
	// Operation names the request in the Request log as its protocol names it, such as createPost or getUpdates.
	Operation string
	// SkipRecord, when set, says of an attempt that succeeded, by its status and body, that the Request log leaves it
	// out, as it does a long poll that ended without events; a failed attempt is always recorded.
	SkipRecord func(status int, body []byte) bool
}

// Result is what a request ended with. Body and Header are those of the last answer, if there was one.
type Result struct {
	Outcome Outcome
	Status  int
	Header  http.Header
	Body    []byte
	// RetryAfter is the exact delay asked for when Outcome is retry_after.
	RetryAfter time.Duration
	// ProviderError is the provider's error text, untrusted and redacted.
	ProviderError Untrusted
	Attempts      int
}

// Error is a request that did not end with ok. Its text is redacted; it wraps only the context's error, when the
// context ended.
type Error struct {
	Outcome    Outcome
	RetryAfter time.Duration
	// Rule names the rule of the outbound address policy that blocked the request.
	Rule string
	// Status is the status code of the answer, 0 without one.
	Status        int
	ProviderError Untrusted
	// Network is what failed when the request got no answer, one of the Network constants, empty otherwise; Detail is
	// the redacted text of that failure without the request, such as the TLS error.
	Network string
	Detail  string
	msg     string
	cause   error
}

// What failed when a request got no answer (Error.Network).
const (
	NetworkDNS     = "dns"
	NetworkTLS     = "tls"
	NetworkTimeout = "timeout"
	// NetworkProxyAuth is a proxy that refused the client's credentials: 407 to an HTTP CONNECT, or a SOCKS5 proxy
	// that refused the username and password.
	NetworkProxyAuth = "proxy_auth"
	// NetworkConnect is any other failure to connect or to read the answer, such as a refused connection.
	NetworkConnect = "connect"
)

func (e *Error) Error() string { return e.msg }

func (e *Error) Unwrap() error { return e.cause }

// Do sends req and makes further attempts as the client's class allows. The returned error is an *Error whenever the
// outcome is not ok. A client with a Recorder and an Owner records each attempt once it has its outcome, before the
// next one.
func (c *Client) Do(ctx context.Context, req Request) (Result, error) {
	u, err := c.target(req.URL)
	if err != nil {
		c.observe(OutcomeFatal, 0)
		return Result{Outcome: OutcomeFatal, Attempts: 1}, &Error{Outcome: OutcomeFatal, msg: c.redact(err.Error())}
	}
	for n := 1; ; n++ {
		res, ex, elapsed, err := c.attempt(ctx, req, u)
		res.Attempts = n
		c.record(ctx, req, u, n, res, ex, elapsed, err)
		if err == nil || !c.cfg.Class.retries(res.Outcome) {
			return res, err
		}
		// The delay asked for is kept exactly; without one, or with none left, the class backs off.
		wait := res.RetryAfter
		if wait <= 0 {
			wait = backoff(n, c.jitter)
		}
		if serr := c.sleep(ctx, wait); serr != nil {
			var e *Error
			if errors.As(err, &e) && e.cause == nil {
				e.cause = serr
			}
			return res, err
		}
	}
}

func (c *Client) target(raw string) (*url.URL, error) {
	if c.base == nil {
		return parseTarget(raw)
	}
	if u, err := url.Parse(raw); err != nil || u.IsAbs() || u.Host != "" {
		return nil, errors.New("the request URL must be a path below the client's base URL")
	}
	joined := strings.TrimSuffix(c.base.String(), "/") + "/" + strings.TrimPrefix(raw, "/")
	return parseTarget(joined)
}

// attempt sends req once: its result, what it exchanged and how long it took on the real clock.
func (c *Client) attempt(ctx context.Context, req Request, u *url.URL) (Result, exchange, time.Duration, error) {
	start := c.cfg.Clock.Now()
	res, ex, err := c.send(ctx, req, u)
	elapsed := c.cfg.Clock.Now().Sub(start)
	c.observe(res.Outcome, elapsed)
	return res, ex, elapsed, err
}

func (c *Client) observe(o Outcome, elapsed time.Duration) {
	metrics.ClientRequests.With(string(c.cfg.Class), string(o)).Inc()
	metrics.ClientRequestDuration.With(string(c.cfg.Class), string(o)).Update(max(elapsed.Seconds(), 0))
}

// display is u as an error shows it: redacted, and with the path of the base URL, which may be a secret prefix,
// replaced with [redacted].
func (c *Client) display(u *url.URL) string { return c.displayWith(c.redactor, u) }

// displayWith is display with the secrets of red.
func (c *Client) displayWith(red redactor, u *url.URL) string {
	if c.base != nil && sameOrigin(u, c.base) {
		if prefix := strings.TrimSuffix(c.base.Path, "/"); prefix != "" {
			if rest, ok := strings.CutPrefix(u.Path, prefix); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
				v := *u
				v.Path, v.RawPath = "/"+redacted+rest, ""
				u = &v
			}
		}
	}
	return red.redact(red.url(u))
}

// sameOrigin reports whether a and b have the same scheme and host, both compared without regard to case, with an
// explicit default port (:80 for http, :443 for https) equal to none.
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(originHost(a), originHost(b))
}

// originHost is the host and port of u, without the port when it is the default port of u's scheme.
func originHost(u *url.URL) string {
	port := u.Port()
	switch {
	case port == "",
		port == "80" && strings.EqualFold(u.Scheme, "http"),
		port == "443" && strings.EqualFold(u.Scheme, "https"):
		return u.Hostname()
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// send makes one attempt of req: its result, and what it sent and was answered for its record.
func (c *Client) send(ctx context.Context, req Request, u *url.URL) (Result, exchange, error) {
	actx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	hreq, err := http.NewRequestWithContext(actx, method, u.String(), body)
	if err != nil {
		return Result{Outcome: OutcomeFatal}, exchange{}, &Error{Outcome: OutcomeFatal, msg: c.redact(err.Error())}
	}
	hreq.Header = sentHeader(req.Header)
	ex := exchange{sent: true, header: hreq.Header}
	resp, err := c.http.Do(hreq)
	if err != nil {
		res, err := c.transportError(ctx, u, err)
		return res, ex, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil {
		res, err := c.transportError(ctx, u, &url.Error{Op: "read the answer of " + method, URL: u.String(), Err: err})
		return res, ex, err
	}
	ex.answer(resp, data)
	if int64(len(data)) > c.maxBody {
		return Result{Outcome: OutcomeUnknown, Status: resp.StatusCode, Header: resp.Header}, ex,
			&Error{Outcome: OutcomeUnknown, Status: resp.StatusCode,
				msg: fmt.Sprintf("%s %q: the answer is larger than %d bytes", method, c.display(u), c.maxBody)}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
		return Result{Outcome: OutcomeRedirect, Status: resp.StatusCode, Header: resp.Header, Body: data}, ex,
			&Error{Outcome: OutcomeRedirect, Status: resp.StatusCode, msg: c.redirectMessage(u, resp)}
	}
	res, err := c.classify(req, method, u, resp, data)
	return res, ex, err
}

// sentHeader is h as a request sends it: a copy, with Muster's User-Agent unless h names its own, as an outgoing
// webhook may in a header template.
func sentHeader(h http.Header) http.Header {
	out := h.Clone()
	if out == nil {
		out = http.Header{}
	}
	if out.Get("User-Agent") == "" {
		out.Set("User-Agent", userAgent)
	}
	return out
}

func (c *Client) redirectMessage(u *url.URL, resp *http.Response) string {
	loc := resp.Header.Get("Location")
	if loc == "" {
		return fmt.Sprintf("redirect refused: the answer %d has no Location", resp.StatusCode)
	}
	target := loc
	if l, err := u.Parse(loc); err == nil {
		target = c.display(l)
	}
	return c.redact("redirect to " + target + " refused")
}

func (c *Client) classify(req Request, method string, u *url.URL, resp *http.Response, data []byte) (Result, error) {
	delay, hasDelay := retryAfter(resp.Header, c.cfg.Clock.Now())
	status := resp.StatusCode
	var text string
	var reported bool
	if req.ClassifyBody != nil {
		if be, ok := req.ClassifyBody(resp.StatusCode, data); ok {
			reported = true
			status, text = be.Code, be.Text
			if be.RetryAfter > 0 {
				delay, hasDelay = be.RetryAfter, true
			}
		}
	}
	if !reported && (status < 200 || status >= 300) {
		text = string(data)
	}
	mapping := req.Mapping
	if mapping == nil {
		mapping = DefaultMapping
	}
	res := Result{Outcome: mapping(status, delay, hasDelay), Status: resp.StatusCode, Header: resp.Header,
		Body: data, ProviderError: providerText(c.redact(text))}
	if res.Outcome == OutcomeRetryAfter {
		res.RetryAfter = delay
	}
	if res.Outcome == OutcomeOK {
		return res, nil
	}
	msg := fmt.Sprintf("%s %q: answered %d", method, c.display(u), resp.StatusCode)
	if reported {
		msg += fmt.Sprintf(", the body reports error %d", status)
	}
	return res, &Error{Outcome: res.Outcome, RetryAfter: res.RetryAfter, Status: resp.StatusCode,
		ProviderError: res.ProviderError, msg: c.redact(msg + " (" + string(res.Outcome) + ")")}
}

// transportError classifies a request that got no answer: blocked by the policy, or transient for timeouts,
// connection errors and a context that ended.
func (c *Client) transportError(ctx context.Context, u *url.URL, err error) (Result, error) {
	var blocked *BlockedError
	if errors.As(err, &blocked) {
		rule := c.redact(blocked.Rule)
		c.cfg.Logger.Log(ctx, logging.OutboundBlocked, logging.F("client", string(c.cfg.Class)),
			logging.F("rule", rule), logging.F("scheme", u.Scheme), logging.F("host", c.redact(u.Hostname())))
		return Result{Outcome: OutcomeBlocked},
			&Error{Outcome: OutcomeBlocked, Rule: rule, msg: c.redact(blocked.Error())}
	}
	msg, detail := err.Error(), err
	if ue, ok := errors.AsType[*url.Error](err); ok {
		msg, detail = fmt.Sprintf("%s %q: %v", ue.Op, c.display(u), ue.Err), ue.Err
	}
	e := &Error{Outcome: OutcomeTransient, msg: c.redact(msg), Network: networkOf(err),
		Detail: c.redact(detail.Error())}
	switch {
	case ctx.Err() != nil:
		e.cause = ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		e.cause = context.DeadlineExceeded
	}
	return Result{Outcome: OutcomeTransient}, e
}
