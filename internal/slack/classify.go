// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"net/http"
	"slices"
	"time"

	"github.com/muster-io/muster/internal/outbound"
)

// Class is the class of a failed call (C-24.FR-8), decided by Slack's `error` and, without one, by the status.
type Class string

// The classes of a failed call. A Gone-candidate is a `chat.update` that answered message_not_found: delivery reads
// the channel before it takes the Root message for deleted (S-099).
const (
	ClassRetryAfter     Class = "retry_after"
	ClassTransient      Class = "transient"
	ClassFatal          Class = "fatal"
	ClassMarkupRejected Class = "markup_rejected"
	ClassGoneCandidate  Class = "gone_candidate"
	ClassUnknown        Class = "unknown"
)

// Classification is how a call is classified: its class, the wait of a RetryAfter, and the reason, Slack's `error`
// with the scopes `needed` names, masked and untrusted. The zero value is a call that succeeded.
type Classification struct {
	Class      Class
	RetryAfter time.Duration
	Reason     outbound.Untrusted
}

// Answer is what Classify reads of an answer: its status, 0 without one; its Retry-After, valid when HasRetryAfter;
// and the `error` and `needed` of an `ok: false` answer.
type Answer struct {
	Status        int
	RetryAfter    time.Duration
	HasRetryAfter bool
	Error         string
	Needed        string
}

// The errors of C-24.FR-8 by their class; edit_window_closed, cant_update_message and any other error are unknown.
var (
	// tokenErrors are the errors of a token Slack refuses, which a check names token_invalid (C-24.FR-2).
	tokenErrors     = []string{"invalid_auth", "not_authed", "account_inactive", "token_revoked", "token_expired"}
	transientErrors = []string{"internal_error", "fatal_error", "service_unavailable", "request_timeout"}
	fatalErrors     = append(slices.Clone(tokenErrors), "not_allowed_token_type", "missing_scope",
		"channel_not_found", "not_in_channel", "is_archived", "restricted_action")
	markupErrors = []string{"msg_too_long", "invalid_blocks", "invalid_attachments", "no_text", "block_mismatch"}
)

// errorMessageNotFound is the error of an edit of a message that no longer exists (F-087).
const errorMessageNotFound = "message_not_found"

// Classify classifies an answer (C-24.FR-8): without an answer Transient; 429 RetryAfter for its Retry-After, Transient
// without one; 408 and 5xx Transient; otherwise by the `error` of an `ok: false` answer, whatever its status (F-070) —
// internal_error, fatal_error, service_unavailable and request_timeout Transient; a refused token, the wrong kind of
// token, a missing scope and a channel the bot cannot use Fatal; markup Slack refused MarkupRejected;
// message_not_found a Gone-candidate; any other error Unknown — and without an error a 2xx succeeded and any other
// status is Unknown. The reason is the error with the scopes `needed` names.
func Classify(a Answer) Classification {
	c := Classification{Class: classOf(a)}
	if c.Class == "" {
		return c
	}
	if a.Error != "" {
		c.Reason = outbound.Untrusted(errorText(a.Error, a.Needed))
	}
	if c.Class == ClassRetryAfter {
		c.RetryAfter = a.RetryAfter
	}
	return c
}

// classOf is the class of an answer as Classify says.
func classOf(a Answer) Class {
	switch {
	case a.Status == 0:
		return ClassTransient
	case a.Status == http.StatusTooManyRequests && a.HasRetryAfter:
		return ClassRetryAfter
	case !byError(a.Status):
		return ClassTransient
	case a.Error != "":
		return classOfError(a.Error)
	case a.Status >= http.StatusOK && a.Status < http.StatusMultipleChoices:
		return ""
	}
	return ClassUnknown
}

// byError reports whether an answer with status is classified by its error: any but 429, 408 and 5xx.
func byError(status int) bool {
	return status != http.StatusTooManyRequests && status != http.StatusRequestTimeout && (status < 500 || status > 599)
}

// classOfError is the class of Slack's error.
func classOfError(e string) Class {
	switch {
	case slices.Contains(transientErrors, e):
		return ClassTransient
	case slices.Contains(fatalErrors, e):
		return ClassFatal
	case slices.Contains(markupErrors, e):
		return ClassMarkupRejected
	case e == errorMessageNotFound:
		return ClassGoneCandidate
	}
	return ClassUnknown
}

// errorText is Slack's error with the scopes it needs, when it names them.
func errorText(e, needed string) string {
	if needed == "" {
		return e
	}
	return e + " (needed: " + needed + ")"
}
