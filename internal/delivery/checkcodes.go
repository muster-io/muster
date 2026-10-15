// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import "github.com/muster-io/muster/internal/outbound"

// The codes of a request of a Connection or Destination check that failed for a reason its step does not name
// (ConnectionCheckStep.code, DestinationCheckItem.code): what failed when the request got no answer, an address the
// outbound address policy refused, and any other failure, such as an error answer, whose text says what answered.
// Clients translate the code and show the step's English text as its detail.
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
