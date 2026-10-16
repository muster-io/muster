// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"net/http"
	"time"

	"github.com/muster-io/muster/internal/bridge/gen"
)

// Class is the error class of a failed call (ADR-0017 Errors): what the bridge says in the problem's class, or what
// the protocol's mapping of the status gives.
type Class string

// The error classes.
const (
	ClassTransient   Class = "transient"
	ClassRateLimited Class = "rate_limited"
	ClassGone        Class = "gone"
	ClassForbidden   Class = "forbidden"
	ClassInvalid     Class = "invalid"
)

// Scope is what a rate_limited answer holds: one channel, or every channel of the bot.
type Scope string

// The scopes of a rate_limited answer.
const (
	ScopeChannel Scope = "channel"
	ScopeBot     Scope = "bot"
)

// ReasonEditWindowClosed is the reason of a gone answer to an edit refused only because of the message's age.
const ReasonEditWindowClosed = "edit_window_closed"

// Classification is how a failed call is classified: its class; for rate_limited the Retry-After of the answer, when
// it has one, and its scope; for gone the problem's reason. The zero value is a call that succeeded.
type Classification struct {
	Class         Class
	RetryAfter    time.Duration
	HasRetryAfter bool
	Scope         Scope
	Reason        string
}

// Classify classifies an answer (C-22.FR-8, ADR-0017 Errors): by the class of its problem p when it has a known one,
// otherwise by its status — 0, a call without an answer, transient; 401 and 403 forbidden; 408 transient; 429, and 503
// with Retry-After, rate_limited; other 5xx transient; a 2xx no failure; 3xx, 404, 410 and everything else invalid.
// No status is ever gone: only the bridge decides that a message is gone.
func Classify(status int, retryAfter time.Duration, hasRetryAfter bool, p *gen.Problem) Classification {
	c := Classification{Class: classOf(status, hasRetryAfter)}
	if p != nil && p.Class != nil && p.Class.Valid() {
		c.Class = Class(*p.Class)
	}
	if c.Class == ClassRateLimited {
		c.RetryAfter, c.HasRetryAfter, c.Scope = retryAfter, hasRetryAfter, ScopeChannel
		if p != nil && p.Scope != nil && *p.Scope == gen.Bot {
			c.Scope = ScopeBot
		}
	}
	if c.Class == ClassGone && p != nil && p.Reason != nil && *p.Reason == gen.EditWindowClosed {
		c.Reason = ReasonEditWindowClosed
	}
	return c
}

// classOf is the protocol's mapping of a status without a problem class.
func classOf(status int, hasRetryAfter bool) Class {
	switch {
	case status == 0, status == http.StatusRequestTimeout:
		return ClassTransient
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return ""
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ClassForbidden
	case status == http.StatusTooManyRequests, status == http.StatusServiceUnavailable && hasRetryAfter:
		return ClassRateLimited
	case status >= 500 && status < 600:
		return ClassTransient
	}
	return ClassInvalid
}
