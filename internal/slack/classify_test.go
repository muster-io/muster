// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// TestClassify covers C-24.FR-8: a case per error of the list, 429 with and without Retry-After, the 5xx, 408, no
// answer, an `ok: false` answer with HTTP 200 and an error of no list, statuses without an error, and needed kept in
// the reason.
func TestClassify(t *testing.T) {
	type tc struct {
		a    Answer
		want Classification
	}
	cases := []tc{
		{Answer{}, Classification{Class: ClassTransient}},
		{Answer{Status: http.StatusTooManyRequests, RetryAfter: 30 * time.Second, HasRetryAfter: true,
			Error: "ratelimited"}, Classification{Class: ClassRetryAfter, RetryAfter: 30 * time.Second,
			Reason: "ratelimited"}},
		{Answer{Status: http.StatusTooManyRequests, Error: "ratelimited"}, Classification{Class: ClassTransient,
			Reason: "ratelimited"}},
		{Answer{Status: http.StatusTooManyRequests}, Classification{Class: ClassTransient}},
		{Answer{Status: http.StatusRequestTimeout}, Classification{Class: ClassTransient}},
		{Answer{Status: http.StatusOK}, Classification{}},
		{Answer{Status: http.StatusNoContent}, Classification{}},
		{Answer{Status: http.StatusOK, Error: "foo_bar"}, Classification{Class: ClassUnknown, Reason: "foo_bar"}},
		{Answer{Status: http.StatusNotFound}, Classification{Class: ClassUnknown}},
		{Answer{Status: http.StatusMovedPermanently}, Classification{Class: ClassUnknown}},
		{Answer{Status: 600}, Classification{Class: ClassUnknown}},
		{Answer{Status: http.StatusOK, Error: "missing_scope", Needed: "channels:history,groups:history"},
			Classification{Class: ClassFatal, Reason: "missing_scope (needed: channels:history,groups:history)"}},
		{Answer{Status: http.StatusBadRequest, Error: "token_revoked"}, Classification{Class: ClassFatal,
			Reason: "token_revoked"}},
		{Answer{Status: http.StatusInternalServerError, Error: "invalid_auth"}, Classification{Class: ClassTransient,
			Reason: "invalid_auth"}},
	}
	for _, s := range []int{500, 502, 503, 504, 599} {
		cases = append(cases, tc{Answer{Status: s}, Classification{Class: ClassTransient}})
	}
	for class, errs := range map[Class][]string{
		ClassTransient: {"internal_error", "fatal_error", "service_unavailable", "request_timeout"},
		ClassFatal: {"invalid_auth", "not_authed", "account_inactive", "token_revoked", "token_expired",
			"not_allowed_token_type", "missing_scope", "channel_not_found", "not_in_channel", "is_archived",
			"restricted_action"},
		ClassMarkupRejected: {"msg_too_long", "invalid_blocks", "invalid_attachments", "no_text", "block_mismatch"},
		ClassGoneCandidate:  {"message_not_found"},
		ClassUnknown:        {"edit_window_closed", "cant_update_message", "ratelimited", "unknown_method"},
	} {
		for _, e := range errs {
			cases = append(cases, tc{Answer{Status: http.StatusOK, Error: e},
				Classification{Class: class, Reason: outbound.Untrusted(e)}})
		}
	}
	for _, c := range cases {
		if got := Classify(c.a); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Classify(%+v) = %+v, want %+v", c.a, got, c.want)
		}
	}
	if len(fatalErrors) != len(tokenErrors)+6 || !reflect.DeepEqual(fatalErrors[:len(tokenErrors)], tokenErrors) {
		t.Fatalf("fatal errors %v", fatalErrors)
	}
}

// TestOutcome covers the outcomes of C-24.FR-8 for delivery: RetryAfter holds the whole Connection; a Gone-candidate is
// unknown until delivery reads the channel.
func TestOutcome(t *testing.T) {
	for _, tc := range []struct {
		c     Classification
		kind  delivery.OutcomeKind
		scope delivery.Scope
	}{
		{Classification{}, delivery.OutcomeOK, ""},
		{Classification{Class: ClassRetryAfter, RetryAfter: 30 * time.Second}, delivery.OutcomeRetryAfter,
			delivery.ScopeConnection},
		{Classification{Class: ClassTransient}, delivery.OutcomeTransient, ""},
		{Classification{Class: ClassFatal}, delivery.OutcomeFatal, ""},
		{Classification{Class: ClassMarkupRejected}, delivery.OutcomeMarkupRejected, ""},
		{Classification{Class: ClassGoneCandidate}, delivery.OutcomeUnknown, ""},
		{Classification{Class: ClassUnknown}, delivery.OutcomeUnknown, ""},
	} {
		o := Result{Classification: tc.c, Error: "why", Status: 200}.Outcome()
		if o.Kind != tc.kind || o.Scope != tc.scope || o.Error != "why" || o.Status != 200 ||
			o.RetryAfter != tc.c.RetryAfter {
			t.Errorf("%+v: %+v", tc.c, o)
		}
	}
	for c, want := range map[Class]outbound.Outcome{"": outbound.OutcomeOK, ClassRetryAfter: outbound.OutcomeRetryAfter,
		ClassTransient: outbound.OutcomeTransient, ClassFatal: outbound.OutcomeFatal,
		ClassMarkupRejected: outbound.OutcomeUnknown, ClassGoneCandidate: outbound.OutcomeUnknown,
		ClassUnknown: outbound.OutcomeUnknown} {
		if got := outcomeOf(c); got != want {
			t.Errorf("outcomeOf(%q) = %s", c, got)
		}
	}
}

// TestErrorIsMaskedUntrusted covers C-24.FR-8 and FR-1: Slack's error, and the scopes needed, are kept as untrusted
// text with the tokens and the Slack signing secret [redacted], in the Result, its reason and its outcome.
func TestErrorIsMaskedUntrusted(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":false,"error":"invalid_auth `+testBot+`","needed":"`+
		testApp+`,`+testSigning+`"}`))
	c := newClient(t, network(t, nil), settings(s.base()))
	_, r := c.AuthTest(t.Context(), outbound.ClassInteractive)
	want := outbound.Untrusted("invalid_auth [redacted] (needed: [redacted],[redacted])")
	if r.Error != want || r.Classification.Reason != want || r.Outcome().Error != want || r.ErrorCode != "invalid_auth [redacted]" ||
		r.Classification.Class != ClassUnknown {
		t.Fatalf("result %+v", r)
	}
	if strings.Contains(string(r.Error), "xoxb-") {
		t.Fatal("the token leaked")
	}
}

// TestOutcomeGoneCandidateIsUnknown covers C-24.FR-8: an edit answered message_not_found is a Gone-candidate, which a
// Result alone — every call but the adapter's edit — takes as unknown, with Slack's error; only the adapter, after
// reading the channel, makes it Gone or Fatal (adapter_test.go).
func TestOutcomeGoneCandidateIsUnknown(t *testing.T) {
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":false,"error":"message_not_found"}`))
	c := newClient(t, network(t, nil), settings(s.base()))
	r := c.updateMessage(t.Context(), outbound.ClassDelivery, outgoing{Channel: "C1", TS: "1.000001", Text: "x"})
	o := r.Outcome()
	if r.Classification.Class != ClassGoneCandidate || r.ErrorCode != "message_not_found" ||
		o.Kind != delivery.OutcomeUnknown || o.Error != "message_not_found" || o.Status != http.StatusOK ||
		!strings.Contains(string(r.Body), `"message_not_found"`) {
		t.Fatalf("result %+v, outcome %+v", r, o)
	}
	if c := Classify(Answer{Status: http.StatusOK, Error: "message_not_found"}); c.Class != ClassGoneCandidate ||
		c.Reason != "message_not_found" {
		t.Fatalf("classification %+v", c)
	}
}
