// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/messages"
)

// Destination tests (C-16): a person sends one test message, or the requests of an outgoing webhook, through the
// interactive path, with the tokens of the Destination's limiters, and reads each step's request and response with
// every secret masked. A test never reads or writes deliveries, Thread replies or webhook events and records nothing
// in a Timeline; reconciliation never sees what it sent.

// The error classes of a step of a Destination test (DeliveryErrorClass, C-16.FR-2) besides the kinds of outcomes
// that failed: none for a step that succeeded, blocked for a request the outbound address policy refused, and limited
// for a step that found no limiter token within delivery.interactive_budget and sent nothing.
const (
	ClassNone    = "none"
	ClassBlocked = "blocked"
	ClassLimited = "limited"
)

// MaxResponseBody is how much of a response a test result shows: its first 4 KB.
const MaxResponseBody = 4 << 10

// TestInput is one Destination test or preview: the Destination, the Alert Group it renders — a recent one of the
// Destination's Routes or the built-in example — and who started it.
type TestInput struct {
	Destination Destination
	Source      *messages.Source
	Actor       groups.Actor
}

// TestRequest is a request of a test or a preview as it was or would be sent (RenderedRequest), with every Secret, the
// Signing secret and the tokens masked as [redacted]; Body is nil without one.
type TestRequest struct {
	Method  string
	URL     string
	Headers [][2]string
	Body    *string
}

// TestStep is one step of a test (TestStep): its name, the request, the status and the start of the body of the
// response (0 and nil without one), the values the extraction rules found, how long it took, its error class and,
// when it failed, what failed. Every text is masked.
type TestStep struct {
	Name           string
	Request        *TestRequest
	ResponseStatus int
	ResponseBody   *string
	Extracted      map[string]string
	Duration       time.Duration
	ErrorClass     string
	Error          string
}

// OK reports whether the step succeeded.
func (s TestStep) OK() bool { return s.ErrorClass == ClassNone }

// PreviewItem is one rendered output of a preview (DestinationPreviewItem): a message with its format and text, or a
// request.
type PreviewItem struct {
	Name    string
	Format  string
	Text    *string
	Request *TestRequest
}

// testOp is a request of a test other than its test message.
type testOp func(ctx context.Context, c Call) Outcome

func (f testOp) call(ctx context.Context, c Call) Outcome { return f(ctx, c) }

// TestOp is a request of a Destination test other than the test message, which is a PublishOp (C-16.FR-1, FR-3): the
// test event or the "create" request of an outgoing webhook, or the bot's own press of a Mattermost test message. It
// changes no Root message and no Thread reply.
func TestOp(send func(ctx context.Context, c Call) Outcome) Op { return testOp(send) }

// ErrorClass is the error class of a step that ended with an outcome of kind k: none when it succeeded; retry_after,
// transient, fatal, unknown and template_error as they are; anything else, which a test does not expect, unknown.
func ErrorClass(k OutcomeKind) string {
	switch k {
	case OutcomeOK:
		return ClassNone
	case OutcomeRetryAfter, OutcomeTransient, OutcomeFatal, OutcomeTemplateError:
		return string(k)
	case OutcomeUnknown, OutcomeMarkupRejected, OutcomeGone, OutcomeThreadLost:
	}
	return string(OutcomeUnknown)
}

// Step is the step name after its call through the interactive path took d: limited, with nothing sent, when no token
// was free in time — a *LimitedError, or the end of ctx while waiting for one, before any call — otherwise the class
// and the error of out. Any other error of the path is returned.
func Step(name string, out Outcome, err error, d time.Duration) (TestStep, error) {
	st := TestStep{Name: name, Duration: d}
	var limited *LimitedError
	switch {
	case errors.As(err, &limited):
		st.ErrorClass, st.Error = ClassLimited, limited.Error()
		return st, nil
	case errors.Is(err, context.DeadlineExceeded) && out.Kind == "":
		st.ErrorClass, st.Error = ClassLimited, "no limiter token was free within the interactive budget"
		return st, nil
	case err != nil:
		return TestStep{}, err
	}
	st.ErrorClass = ErrorClass(out.Kind)
	if st.ErrorClass != ClassNone {
		st.Error = string(out.Error)
		if st.Error == "" {
			st.Error = "the request failed: " + string(out.Kind)
		}
	}
	return st, nil
}

// ResponseText is the start of a response body as a test shows it: masked by mask first, so that a secret cut at the
// end cannot show in part, then at most MaxResponseBody bytes, cut on a rune boundary, with invalid UTF-8 replaced;
// nil for an empty body.
func ResponseText(body []byte, mask func(string) string) *string {
	if len(body) == 0 {
		return nil
	}
	s := strings.ToValidUTF8(string(body), "\uFFFD")
	if mask != nil {
		s = mask(s)
	}
	if len(s) > MaxResponseBody {
		cut := MaxResponseBody
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return &s
}
