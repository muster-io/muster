// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Outcome is the classified result of a request (C-02.FR-20), the closed set of the metric label outcome.
type Outcome string

const (
	OutcomeOK         Outcome = "ok"
	OutcomeRetryAfter Outcome = "retry_after"
	OutcomeTransient  Outcome = "transient"
	OutcomeFatal      Outcome = "fatal"
	// OutcomeUnknown is an answer the mapping does not know; it is never retried.
	OutcomeUnknown Outcome = "unknown"
	// OutcomeBlocked is a request the outbound address policy refused.
	OutcomeBlocked Outcome = "blocked"
	// OutcomeRedirect is a 3xx answer, never followed.
	OutcomeRedirect Outcome = "redirect"
)

// Outcomes is the closed set of outcomes.
var Outcomes = []Outcome{OutcomeOK, OutcomeRetryAfter, OutcomeTransient, OutcomeFatal, OutcomeUnknown,
	OutcomeBlocked, OutcomeRedirect}

// Mapping classifies a status code; retryAfter is the delay the answer asked for, valid when hasRetryAfter. Callers
// replace DefaultMapping with their own (C-13, C-14, C-15). A mapping that classifies 409 Conflict as transient makes
// the background class only back off.
type Mapping func(status int, retryAfter time.Duration, hasRetryAfter bool) Outcome

// DefaultMapping is the mapping of C-02.FR-20: 2xx is ok; 429 or 503 with Retry-After is retry_after; 408, 500, 502,
// 503 without Retry-After and 504 are transient; 400, 401, 403 and 404 are fatal; anything else is unknown.
func DefaultMapping(status int, _ time.Duration, hasRetryAfter bool) Outcome {
	switch {
	case status >= 200 && status < 300:
		return OutcomeOK
	case (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) && hasRetryAfter:
		return OutcomeRetryAfter
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return OutcomeTransient
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return OutcomeFatal
	}
	return OutcomeUnknown
}

// BodyError is an error the provider reports in the body of its answer, such as Telegram's ok: false with its
// error_code, description and parameters.retry_after.
type BodyError struct {
	// Code is the status code the body reports; the mapping classifies it like the status of the answer.
	Code int
	// RetryAfter is the delay the body asks for, used exactly when positive.
	RetryAfter time.Duration
	// Text is the provider's error text.
	Text string
}

// BodyClassifier finds an error in the body of an answer; ok is false when the body reports none. It runs inside
// the attempt on every answer, so an error inside a successful answer is classified like a status code.
type BodyClassifier func(status int, body []byte) (e BodyError, ok bool)

// Untrusted is text that comes from the provider: escape it wherever it is shown, and never act on it.
type Untrusted string

// maxProviderText is how much of the provider's error text is kept.
const maxProviderText = 1024

// providerText is the start of the provider's error text, cut on a rune boundary.
func providerText(s string) Untrusted {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "�")
	if len(s) <= maxProviderText {
		return Untrusted(s)
	}
	cut := maxProviderText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return Untrusted(s[:cut] + "…")
}

// retryAfter reads Retry-After as seconds or as an HTTP date, relative to now; a date in the past is no delay.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 || n > int64(365*24*time.Hour/time.Second) {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	return max(t.Sub(now), 0), true
}
