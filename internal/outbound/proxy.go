// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/logging"
)

// ProxyType is the kind of a client's proxy.
type ProxyType string

const (
	ProxyHTTP   ProxyType = "http"
	ProxyHTTPS  ProxyType = "https"
	ProxySOCKS5 ProxyType = "socks5"
)

// Proxy is a client's own proxy (C-02.FR-22). The password is a Secret decrypted with the Keyring only to build the
// client. Proxies from the environment (HTTP_PROXY, HTTPS_PROXY, NO_PROXY) are never consulted.
type Proxy struct {
	Type ProxyType
	// Address is host:port.
	Address  string
	Username string
	Password logging.Secret
}

func (p *Proxy) url() (*url.URL, error) {
	switch p.Type {
	case ProxyHTTP, ProxyHTTPS, ProxySOCKS5:
	default:
		return nil, fmt.Errorf("proxy type %q is not http, https or socks5", p.Type)
	}
	host, port, err := net.SplitHostPort(p.Address)
	if err != nil || host == "" {
		return nil, fmt.Errorf("proxy address %q is not host:port", p.Address)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("proxy address %q has no valid port", p.Address)
	}
	u := &url.URL{Scheme: string(p.Type), Host: p.Address}
	if p.Username != "" || p.Password != "" {
		u.User = url.UserPassword(p.Username, string(p.Password))
	}
	return u, nil
}

// proxyFunc is the transport's Proxy for a client with a proxy: before each request it checks the target
// (checkProxiedTarget), then sends the request to the proxy. The connection to the proxy goes through the dialer,
// which checks its address.
func (c *Client) proxyFunc(proxyURL *url.URL) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if err := c.checkProxiedTarget(req.Context(), req.URL.Scheme, req.URL.Hostname()); err != nil {
			return nil, err
		}
		return proxyURL, nil
	}
}

// checkProxiedTarget checks the target host of a request that goes through the client's proxy, which resolves the
// name itself: an address must pass; a name is checked against the denied names, resolved by Muster too, and every
// address it resolves to must pass; when only the proxy can resolve it, the name is checked against the lists instead
// and the request is logged as sent without an address check. scheme only names the request in that log line.
func (c *Client) checkProxiedTarget(ctx context.Context, scheme, host string) error {
	p, err := c.cfg.Policy.OutboundPolicy(ctx)
	if err != nil {
		return fmt.Errorf("read the outbound address policy: %w", err)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if b := p.checkAddr(a, ""); b != nil {
			return b
		}
		return nil
	}
	if b := p.checkDeniedName(host, "host "); b != nil {
		return b
	}
	lctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	addrs, err := c.resolver.LookupNetIP(lctx, "ip", host)
	cancel()
	var dnsErr *net.DNSError
	if err != nil && (!errors.As(err, &dnsErr) || ctx.Err() != nil) {
		return err
	}
	if err != nil || len(addrs) == 0 {
		if b := p.checkUnresolvedName(host); b != nil {
			return b
		}
		c.cfg.Logger.Log(ctx, logging.OutboundUnverifiedAddress,
			logging.F("client", string(c.cfg.Class)), logging.F("scheme", scheme),
			logging.F("host", c.redact(host)))
		return nil
	}
	for _, a := range addrs {
		if b := p.checkAddr(a, ""); b != nil {
			return b
		}
	}
	return nil
}

// connectDial is the DialContext of a WebSocket transport through the client's HTTP or HTTPS proxy: it checks the
// target (checkProxiedTarget, with scheme), dials the proxy through dial, the dialer's, which checks the proxy's
// address, speaks TLS to an HTTPS proxy, and opens a CONNECT tunnel to address whatever the target's scheme, so that
// no request in absolute form carries the upgrade (ADR-0015). The TLS handshake with the proxy and the CONNECT are
// each bounded by the connect timeout.
func (c *Client) connectDial(proxyURL *url.URL, dial dialFunc, tlsConfig *tls.Config, scheme string) dialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if err := c.checkProxiedTarget(ctx, scheme, host); err != nil {
			return nil, err
		}
		conn, err := dial(ctx, network, proxyURL.Host)
		if err != nil {
			return nil, err
		}
		if proxyURL.Scheme == string(ProxyHTTPS) {
			cfg := tlsConfig.Clone()
			cfg.ServerName = proxyURL.Hostname()
			cfg.NextProtos = nil
			tconn := tls.Client(conn, cfg)
			hctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
			err := tconn.HandshakeContext(hctx)
			cancel()
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			conn = tconn
		}
		tunnel, err := connect(ctx, conn, address, proxyURL, c.cfg.ConnectTimeout)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tunnel, nil
	}
}

// connect asks the proxy on conn for a tunnel to address within timeout: CONNECT with Host, Muster's User-Agent and,
// with the proxy's username or password, Proxy-Authorization Basic. An answer other than 200 fails with its status
// text, as net/http's CONNECT does, so that networkOf names a 407 proxy_auth.
func connect(ctx context.Context, conn net.Conn, address string, proxyURL *url.URL,
	timeout time.Duration) (net.Conn, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Ending the context interrupts the exchange: a deadline in the past fails every read and write at once.
	stop := context.AfterFunc(cctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	h := http.Header{"User-Agent": {userAgent}}
	if u := proxyURL.User; u != nil {
		password, _ := u.Password()
		h.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+password)))
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: h}
	br := bufio.NewReader(&io.LimitedReader{R: conn, N: maxResponseHeaderBytes})
	err := req.Write(conn)
	var resp *http.Response
	if err == nil {
		resp, err = http.ReadResponse(br, req)
	}
	if !stop() {
		return nil, fmt.Errorf("CONNECT to the proxy: %w", cctx.Err())
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// The caller closes the connection; the answer's body is not read.
		if _, text, ok := strings.Cut(resp.Status, " "); ok && text != "" {
			return nil, errors.New(text)
		}
		return nil, errors.New("unknown status code")
	}
	// The body of a tunnel's answer is the tunnel itself: closing it reads nothing.
	_ = resp.Body.Close()
	if n := br.Buffered(); n > 0 {
		early, _ := br.Peek(n)
		return &bufferedConn{Conn: conn, r: io.MultiReader(bytes.NewReader(bytes.Clone(early)), conn)}, nil
	}
	return conn, nil
}

// bufferedConn is a connection whose first bytes came with the proxy's answer.
type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }
