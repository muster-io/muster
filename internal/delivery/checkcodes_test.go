// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"testing"

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
