// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/muster-io/muster/internal/logging"
)

// minQueryValue is the length in bytes from which a value in the query of a WebSocket address is replaced on its own
// wherever an error shows it; the whole query is always replaced. A shorter value is no credential, and replacing it
// everywhere would garble the error's text.
const minQueryValue = 8

// errNotWebSocketURL never shows the address, whose query may hold a credential.
var errNotWebSocketURL = errors.New("websocket: the address is not an absolute ws or wss URL")

// DialWebSocket opens a WebSocket connection to rawURL, ws or wss, for the client c (ADR-0015, C-02.FR-20): through
// c's dialer, which checks the outbound address policy after name resolution, and c's proxy — an HTTP or HTTPS proxy
// by CONNECT whatever the target's scheme, so that no request in absolute form carries the upgrade, a SOCKS5 proxy as
// for every request — with c's connect timeout for the connection, a TLS handshake and the CONNECT, c's overall
// timeout for the handshake, Muster's User-Agent unless header names one, and no redirect. The handshake is one
// request in muster_client_requests_total and muster_client_request_duration_seconds under c's class; it is not
// recorded in the Request log. Every failure is an *Error whose text shows the address with its query as [redacted],
// with c's secrets and the query's values replaced. ctx bounds only the handshake: reading, writing, pinging and
// closing the connection, and its read limit, are the caller's.
func DialWebSocket(ctx context.Context, c *Client, rawURL string, header http.Header) (*WebSocket, error) {
	u, err := webSocketTarget(rawURL)
	if err != nil {
		c.observe(OutcomeFatal, 0)
		return nil, &Error{Outcome: OutcomeFatal, msg: err.Error()}
	}
	red := newRedactor(append(slices.Clone(c.secrets), webSocketSecrets(u)...))
	ws := &WebSocket{}
	hc, err := c.webSocketClient(u, ws)
	if err != nil {
		c.observe(OutcomeFatal, 0)
		return nil, &Error{Outcome: OutcomeFatal, msg: red.redact("websocket: " + err.Error())}
	}
	hctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	start := c.cfg.Clock.Now()
	conn, resp, err := websocket.Dial(hctx, u.String(), &websocket.DialOptions{HTTPClient: hc,
		HTTPHeader: sentHeader(header)})
	elapsed := c.cfg.Clock.Now().Sub(start)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		c.observe(OutcomeOK, elapsed)
		ws.Conn = conn
		return ws, nil
	}
	e := c.webSocketError(ctx, red, u, resp, err)
	c.observe(e.Outcome, elapsed)
	return nil, e
}

// WebSocket is an open WebSocket connection with its network connection, which Cut closes at once.
type WebSocket struct {
	*websocket.Conn

	mu  sync.Mutex
	net net.Conn
}

// dialled keeps the network connection the handshake was made on: with keep-alives disabled and no redirect, there is
// one.
func (w *WebSocket) dialled(dial func(ctx context.Context, network, address string) (net.Conn, error)) func(
	ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		nc, err := dial(ctx, network, address)
		if err == nil {
			w.mu.Lock()
			w.net = nc
			w.mu.Unlock()
		}
		return nc, err
	}
}

// Cut closes the network connection at once. Unlike Conn.CloseNow, which waits for a Close already under way, it also
// ends a Close whose peer does not answer, which otherwise waits for the answer up to the library's own five seconds.
func (w *WebSocket) Cut() {
	w.mu.Lock()
	nc := w.net
	w.mu.Unlock()
	if nc != nil {
		_ = nc.Close() // the connection's own close closes it again
	}
}

// webSocketTarget parses an absolute ws or wss URL with a host and without user information or a fragment.
func webSocketTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Opaque != "" || u.Hostname() == "" ||
		u.User != nil || strings.Contains(raw, "#") {
		return nil, errNotWebSocketURL
	}
	return u, nil
}

// webSocketSecrets are the query of u, as sent and unescaped, and each of its values of at least minQueryValue bytes.
func webSocketSecrets(u *url.URL) []logging.Secret {
	var out []logging.Secret
	add := func(s string) {
		if s != "" && !slices.Contains(out, logging.Secret(s)) {
			out = append(out, logging.Secret(s))
		}
	}
	add(u.RawQuery)
	if q, err := url.QueryUnescape(u.RawQuery); err == nil {
		add(q)
	}
	for _, values := range u.Query() {
		for _, v := range values {
			if len(v) >= minQueryValue {
				add(v)
			}
		}
	}
	return out
}

// webSocketDisplay is u as an error shows it: the query as [redacted], the host and the path redacted.
func webSocketDisplay(red redactor, u *url.URL) string {
	s := u.Scheme + "://" + red.redact(u.Host) + red.redact(u.Path)
	if u.RawQuery != "" {
		s += "?" + redacted
	}
	return s
}

// webSocketClient is the HTTP client of one handshake to u: a transport of its own that keeps no connection, with c's
// dialer, TLS configuration and timeouts. An HTTP or HTTPS proxy is reached through connectDial instead of the
// transport's Proxy, which would send a ws target in absolute form; a SOCKS5 proxy goes through the transport, as for
// every request. ws keeps the network connection it dials.
func (c *Client) webSocketClient(u *url.URL, ws *WebSocket) (*http.Client, error) {
	d := &dialer{policy: c.cfg.Policy, resolver: c.resolver, timeout: c.cfg.ConnectTimeout, proxy: c.cfg.Proxy != nil}
	tlsConfig := c.transports.transport().TLSClientConfig.Clone()
	// The upgrade needs HTTP/1.1.
	tlsConfig.NextProtos = nil
	tr := &http.Transport{
		DialContext:            d.DialContext,
		TLSClientConfig:        tlsConfig,
		TLSHandshakeTimeout:    c.cfg.ConnectTimeout,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
	}
	if c.cfg.Proxy != nil {
		pu, err := c.cfg.Proxy.url()
		if err != nil {
			return nil, err
		}
		if c.cfg.Proxy.Type == ProxySOCKS5 {
			tr.Proxy = func(req *http.Request) (*url.URL, error) {
				if err := c.checkProxiedTarget(req.Context(), u.Scheme, req.URL.Hostname()); err != nil {
					return nil, err
				}
				return pu, nil
			}
		} else {
			tr.DialContext = c.connectDial(pu, d, tlsConfig, u.Scheme)
		}
	}
	tr.DialContext = ws.dialled(tr.DialContext)
	return &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// webSocketError classifies a handshake that did not open the connection: an answer other than the switch of
// protocols by its status, a redirect never followed; no answer as blocked by the policy, or transient.
func (c *Client) webSocketError(ctx context.Context, red redactor, u *url.URL, resp *http.Response,
	err error) *Error {
	where := webSocketDisplay(red, u)
	if resp != nil {
		code := resp.StatusCode
		if code >= 300 && code < 400 && code != http.StatusNotModified {
			return &Error{Outcome: OutcomeRedirect, Status: code,
				msg: fmt.Sprintf("websocket %q: answered %d, redirect refused", where, code)}
		}
		delay, hasDelay := retryAfter(resp.Header, c.cfg.Clock.Now())
		o := DefaultMapping(code, delay, hasDelay)
		msg := fmt.Sprintf("websocket %q: answered %d", where, code)
		if code < 300 {
			// A switch of protocols that broke the protocol, or an answer that switched nothing.
			o = OutcomeUnknown
			msg += ", " + red.redact(strings.TrimPrefix(err.Error(), "failed to WebSocket dial: "))
		}
		e := &Error{Outcome: o, Status: code, msg: msg + " (" + string(o) + ")"}
		if o == OutcomeRetryAfter {
			e.RetryAfter = delay
		}
		return e
	}
	var blocked *BlockedError
	if errors.As(err, &blocked) {
		rule := red.redact(blocked.Rule)
		c.cfg.Logger.Log(ctx, logging.OutboundBlocked, logging.F("client", string(c.cfg.Class)),
			logging.F("rule", rule), logging.F("scheme", u.Scheme), logging.F("host", red.redact(u.Hostname())))
		return &Error{Outcome: OutcomeBlocked, Rule: rule, msg: red.redact(blocked.Error())}
	}
	// The library's own text repeats the address with its query, the url.Error's cause does not.
	detail := err
	if ue, ok := errors.AsType[*url.Error](err); ok {
		detail = ue.Err
	}
	text := red.redact(detail.Error())
	e := &Error{Outcome: OutcomeTransient, Network: networkOf(err), Detail: text,
		msg: fmt.Sprintf("websocket %q: %s", where, text)}
	switch {
	case ctx.Err() != nil:
		e.cause = ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		e.cause = context.DeadlineExceeded
	}
	return e
}
