// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"fmt"
	"net/url"
	"time"

	"github.com/muster-io/muster/internal/outbound"
)

// The codes of a request of a Connection or Destination check that failed for a reason its step does not name
// (ConnectionCheckStep.code, DestinationCheckItem.code): what failed when the request got no answer, an address the
// outbound address policy refused, and any other failure, such as an error answer, whose text says what answered.
// Clients translate the code and show the step's English text as its detail where it says more than the code's text.
const (
	CodeDNSFailed     = "dns_failed"
	CodeConnectFailed = "connect_failed"
	CodeTLSFailed     = "tls_failed"
	CodeTimeout       = "timeout"
	CodeProxyRefused  = "proxy_refused"
	CodeBlocked       = "blocked"
	CodeRequestFailed = "request_failed"
)

// FailureCode is the check code of a failed request: CodeBlocked when the outbound address policy refused its address,
// the code of what failed (outbound.Network*) when it got no answer, and CodeRequestFailed otherwise.
func FailureCode(blocked bool, network string) string {
	if blocked {
		return CodeBlocked
	}
	switch network {
	case outbound.NetworkDNS:
		return CodeDNSFailed
	case outbound.NetworkConnect:
		return CodeConnectFailed
	case outbound.NetworkTLS:
		return CodeTLSFailed
	case outbound.NetworkTimeout:
		return CodeTimeout
	case outbound.NetworkProxyAuth:
		return CodeProxyRefused
	}
	return CodeRequestFailed
}

// MessageProxyRefused is the text of a request that the proxy refused: 407 to an HTTP CONNECT, or a SOCKS5 proxy that
// refused the credentials.
const MessageProxyRefused = "The proxy refused the connection (407)"

// The texts of a request without an answer that FailureMessage names.
const (
	messageDNS     = "DNS lookup failed: %s"
	messageTLS     = "TLS handshake failed: %s"
	messageTimeout = "No answer within %d s"
)

// FailureMessage is the English text of a request of a check that got no answer, by what failed (outbound.Network*):
// the host of address, the URL it was sent to, that did not resolve; the TLS failure detail; the timeout of the
// request; or MessageProxyRefused. ok is false for any other failure, for a TLS failure without its detail, and for a
// DNS failure without an address, whose own error texts say more: a request through a proxy passes no address, since
// Muster leaves the name of its URL to the proxy, so the name that did not resolve is the proxy's.
func FailureMessage(network, address, detail string, timeout time.Duration) (message string, ok bool) {
	switch network {
	case outbound.NetworkDNS:
		if address == "" {
			return "", false
		}
		host := address
		if u, err := url.Parse(address); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		return fmt.Sprintf(messageDNS, host), true
	case outbound.NetworkTLS:
		if detail == "" {
			return "", false
		}
		return fmt.Sprintf(messageTLS, detail), true
	case outbound.NetworkTimeout:
		return fmt.Sprintf(messageTimeout, int(timeout/time.Second)), true
	case outbound.NetworkProxyAuth:
		return MessageProxyRefused, true
	}
	return "", false
}
