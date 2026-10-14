// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outgoingheartbeat

import (
	"errors"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/outbound"
)

// TestFailureOf: the class and the status of every failure that send stores are read back from its text, the outcome
// of an answer that the API does not name and any other text are unknown.
func TestFailureOf(t *testing.T) {
	tests := []struct {
		err  error
		want Failure
	}{
		{&outbound.Error{Outcome: outbound.OutcomeTransient, Status: 503}, Failure{Class: "transient", Status: 503}},
		{&outbound.Error{Outcome: outbound.OutcomeRetryAfter, Status: 429}, Failure{Class: "retry_after", Status: 429}},
		{&outbound.Error{Outcome: outbound.OutcomeFatal, Status: 404}, Failure{Class: "fatal", Status: 404}},
		{&outbound.Error{Outcome: outbound.OutcomeRedirect, Status: 302}, Failure{Class: "redirect", Status: 302}},
		{&outbound.Error{Outcome: outbound.OutcomeUnknown, Status: 200}, Failure{Class: "unknown", Status: 200}},
		{&outbound.Error{Outcome: outbound.OutcomeOK, Status: 204}, Failure{Class: "unknown", Status: 204}},
		{&outbound.Error{Outcome: outbound.OutcomeBlocked, Rule: "the always-blocked range 169.254.0.0/16"},
			Failure{Class: "blocked"}},
		{&outbound.Error{Outcome: outbound.OutcomeTransient, Network: outbound.NetworkConnect,
			Detail: "dial tcp 127.0.0.1:1: connect: connection refused"}, Failure{Class: "connect"}},
		{&outbound.Error{Outcome: outbound.OutcomeTransient, Network: outbound.NetworkDNS, Detail: "no such host"},
			Failure{Class: "dns"}},
		{&outbound.Error{Outcome: outbound.OutcomeTransient, Network: outbound.NetworkTLS,
			Detail: strings.Repeat("é", 2*maxError)}, Failure{Class: "tls"}},
		{&outbound.Error{Outcome: outbound.OutcomeTransient, Network: outbound.NetworkTimeout, Detail: "deadline"},
			Failure{Class: "timeout"}},
		{&outbound.Error{Outcome: outbound.OutcomeTransient, Network: outbound.NetworkProxyAuth, Detail: "407"},
			Failure{Class: "proxy_auth"}},
		{&outbound.Error{Outcome: outbound.OutcomeFatal}, Failure{Class: "fatal"}},
		{&outbound.Error{Outcome: outbound.OutcomeBlocked}, Failure{Class: "blocked"}},
		{errors.New("boom"), Failure{Class: "unknown"}},
	}
	for _, tt := range tests {
		text := masked(tt.err)
		if got := FailureOf(text); got != tt.want {
			t.Errorf("FailureOf(%q) = %+v, want %+v", text, got, tt.want)
		}
	}
	for text, want := range map[string]Failure{
		failedURL:                         {Class: ClassInternal},
		failedPassword:                    {Class: ClassInternal},
		failedProxy:                       {Class: ClassInternal},
		failedClient + "bad proxy URL":    {Class: ClassInternal},
		"answered 5034 (transient)":       {Class: "unknown"},
		"something the sender never said": {Class: "unknown"},
		"":                                {Class: "unknown"},
	} {
		if got := FailureOf(text); got != want {
			t.Errorf("FailureOf(%q) = %+v, want %+v", text, got, want)
		}
	}
}
