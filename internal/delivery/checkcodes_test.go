// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

func TestFailureCode(t *testing.T) {
	for _, tc := range []struct {
		blocked bool
		network string
		want    string
	}{
		{true, "", delivery.CodeBlocked},
		{true, outbound.NetworkDNS, delivery.CodeBlocked},
		{false, outbound.NetworkDNS, delivery.CodeDNSFailed},
		{false, outbound.NetworkConnect, delivery.CodeConnectFailed},
		{false, outbound.NetworkTLS, delivery.CodeTLSFailed},
		{false, outbound.NetworkTimeout, delivery.CodeTimeout},
		{false, outbound.NetworkProxyAuth, delivery.CodeProxyRefused},
		{false, "", delivery.CodeRequestFailed},
		{false, "other", delivery.CodeRequestFailed},
	} {
		if got := delivery.FailureCode(tc.blocked, tc.network); got != tc.want {
			t.Errorf("FailureCode(%v, %q) = %q, want %q", tc.blocked, tc.network, got, tc.want)
		}
	}
}

func TestFailureMessage(t *testing.T) {
	for _, tc := range []struct {
		network, address, detail string
		want                     string
		ok                       bool
	}{
		{outbound.NetworkDNS, "https://bot-api.invalid:8443/x", "", "DNS lookup failed: bot-api.invalid", true},
		{outbound.NetworkDNS, "bridge.invalid", "", "DNS lookup failed: bridge.invalid", true},
		{outbound.NetworkDNS, "", "", "", false},
		{outbound.NetworkTLS, "https://h", "tls: failed to verify certificate", "TLS handshake failed: tls: failed " +
			"to verify certificate", true},
		{outbound.NetworkTLS, "https://h", "", "", false},
		{outbound.NetworkTimeout, "https://h", "", "No answer within 10 s", true},
		{outbound.NetworkProxyAuth, "https://h", "", delivery.MessageProxyRefused, true},
		{outbound.NetworkConnect, "https://h", "connection refused", "", false},
		{"", "https://h", "", "", false},
	} {
		got, ok := delivery.FailureMessage(tc.network, tc.address, tc.detail, 10*time.Second)
		if got != tc.want || ok != tc.ok {
			t.Errorf("FailureMessage(%q, %q, %q) = %q, %v, want %q, %v", tc.network, tc.address, tc.detail, got, ok,
				tc.want, tc.ok)
		}
	}
}
