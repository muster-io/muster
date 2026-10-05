// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"

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

// proxyFunc is the transport's Proxy for a client with a proxy: before each request it checks the target, then
// sends the request to the proxy, which resolves the name itself. Muster resolves the name too and every address must
// pass; when only the proxy can resolve it, the name is checked against the lists instead and the request is logged
// as sent without an address check. The connection to the proxy goes through the dialer, which checks its address.
func (c *Client) proxyFunc(proxyURL *url.URL) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		ctx := req.Context()
		p, err := c.cfg.Policy.OutboundPolicy(ctx)
		if err != nil {
			return nil, fmt.Errorf("read the outbound address policy: %w", err)
		}
		host := req.URL.Hostname()
		if a, err := netip.ParseAddr(host); err == nil {
			if b := p.checkAddr(a, ""); b != nil {
				return nil, b
			}
			return proxyURL, nil
		}
		if b := p.checkDeniedName(host, "host "); b != nil {
			return nil, b
		}
		lctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
		addrs, err := c.resolver.LookupNetIP(lctx, "ip", host)
		cancel()
		var dnsErr *net.DNSError
		if err != nil && (!errors.As(err, &dnsErr) || ctx.Err() != nil) {
			return nil, err
		}
		if err != nil || len(addrs) == 0 {
			if b := p.checkUnresolvedName(host); b != nil {
				return nil, b
			}
			c.cfg.Logger.Log(ctx, logging.OutboundUnverifiedAddress,
				logging.F("client", string(c.cfg.Class)), logging.F("scheme", req.URL.Scheme),
				logging.F("host", c.redact(host)))
			return proxyURL, nil
		}
		for _, a := range addrs {
			if b := p.checkAddr(a, ""); b != nil {
				return nil, b
			}
		}
		return proxyURL, nil
	}
}
