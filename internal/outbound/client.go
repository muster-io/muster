// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package outbound is the only code that makes outbound HTTP requests (ADR-0015, C-02.FR-20 to FR-22): clients are
// built per client class with a connect timeout and an overall timeout, never follow redirects, classify every answer
// into an outcome, retry by the policy of their class, redact registered secrets from everything they log and return,
// count their requests in the muster_client_* metrics, and connect only to addresses that pass the Organization's
// outbound address policy, directly or through their own proxy. No other package creates an http.Client or an
// http.Transport (architecture lint 4).
package outbound

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// DefaultMaxBodyBytes is how much of an answer a client reads unless its configuration says otherwise.
const DefaultMaxBodyBytes = 8 << 20

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
}

// Client makes the requests of one client configuration.
type Client struct {
	cfg      Config
	base     *url.URL
	http     *http.Client
	resolver Resolver
	redactor redactor
	maxBody  int64
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
	c := &Client{cfg: cfg, redactor: newRedactor(secrets), resolver: cfg.Resolver, maxBody: cfg.MaxBodyBytes,
		sleep: sleepContext, jitter: randomJitter}
	if err := c.build(); err != nil {
		return nil, errors.New(c.redact("outbound client: " + err.Error()))
	}
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
	tr := &http.Transport{
		DialContext:         d.DialContext,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: cfg.ConnectTimeout,
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConnsPerHost: 4,
	}
	// Proxy stays nil without a proxy of the client's own, so the environment is never consulted.
	if cfg.Proxy != nil {
		pu, err := cfg.Proxy.url()
		if err != nil {
			return err
		}
		tr.Proxy = c.proxyFunc(pu)
	}
	c.http = &http.Client{
		Transport:     tr,
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
	msg           string
	cause         error
}

func (e *Error) Error() string { return e.msg }

func (e *Error) Unwrap() error { return e.cause }

// Do sends req and makes further attempts as the client's class allows. The returned error is an *Error whenever the
// outcome is not ok.
func (c *Client) Do(ctx context.Context, req Request) (Result, error) {
	u, err := c.target(req.URL)
	if err != nil {
		c.observe(OutcomeFatal, 0)
		return Result{Outcome: OutcomeFatal, Attempts: 1}, &Error{Outcome: OutcomeFatal, msg: c.redact(err.Error())}
	}
	for n := 1; ; n++ {
		res, err := c.attempt(ctx, req, u)
		res.Attempts = n
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

func (c *Client) attempt(ctx context.Context, req Request, u *url.URL) (Result, error) {
	start := c.cfg.Clock.Now()
	res, err := c.send(ctx, req, u)
	c.observe(res.Outcome, c.cfg.Clock.Now().Sub(start))
	return res, err
}

func (c *Client) observe(o Outcome, elapsed time.Duration) {
	metrics.ClientRequests.With(string(c.cfg.Class), string(o)).Inc()
	metrics.ClientRequestDuration.With(string(c.cfg.Class), string(o)).Update(max(elapsed.Seconds(), 0))
}

// display is u as an error shows it: redacted, and with the path of the base URL, which may be a secret prefix,
// replaced with [redacted].
func (c *Client) display(u *url.URL) string {
	if c.base != nil && u.Host == c.base.Host && u.Scheme == c.base.Scheme {
		if prefix := strings.TrimSuffix(c.base.Path, "/"); prefix != "" {
			if rest, ok := strings.CutPrefix(u.Path, prefix); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
				v := *u
				v.Path, v.RawPath = "/"+redacted+rest, ""
				u = &v
			}
		}
	}
	return c.redact(c.redactor.url(u))
}

func (c *Client) send(ctx context.Context, req Request, u *url.URL) (Result, error) {
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
		return Result{Outcome: OutcomeFatal}, &Error{Outcome: OutcomeFatal, msg: c.redact(err.Error())}
	}
	if req.Header != nil {
		hreq.Header = req.Header.Clone()
	}
	resp, err := c.http.Do(hreq)
	if err != nil {
		return c.transportError(ctx, u, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil {
		return c.transportError(ctx, u, &url.Error{Op: "read the answer of " + method, URL: u.String(), Err: err})
	}
	if int64(len(data)) > c.maxBody {
		return Result{Outcome: OutcomeUnknown, Status: resp.StatusCode, Header: resp.Header},
			&Error{Outcome: OutcomeUnknown, Status: resp.StatusCode,
				msg: fmt.Sprintf("%s %q: the answer is larger than %d bytes", method, c.display(u), c.maxBody)}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
		return Result{Outcome: OutcomeRedirect, Status: resp.StatusCode, Header: resp.Header, Body: data},
			&Error{Outcome: OutcomeRedirect, Status: resp.StatusCode, msg: c.redirectMessage(u, resp)}
	}
	return c.classify(req, method, u, resp, data)
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
	msg := err.Error()
	if ue, ok := errors.AsType[*url.Error](err); ok {
		msg = fmt.Sprintf("%s %q: %v", ue.Op, c.display(u), ue.Err)
	}
	e := &Error{Outcome: OutcomeTransient, msg: c.redact(msg)}
	switch {
	case ctx.Err() != nil:
		e.cause = ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		e.cause = context.DeadlineExceeded
	}
	return Result{Outcome: OutcomeTransient}, e
}
