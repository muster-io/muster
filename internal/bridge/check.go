// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"cmp"
	"context"
	"fmt"
	"mime"
	"net/http"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// ItemChannel is the one item of the Destination check of a Bridge Destination (DestinationCheckItem.name): whether
// the bot can post, edit and reply in the channel.
const ItemChannel = "channel"

// CodeChannelForbidden is the check code of a channel the bridge answered forbidden for: it does not exist, or the bot
// cannot use it (DestinationCheckItem.code).
const CodeChannelForbidden = "channel_forbidden"

// MessageChannelForbidden is the message of a forbidden answer that carries no text of the bridge's.
const MessageChannelForbidden = "The bridge refused the channel."

// Runner makes the request of a check with the Call it is made in: on the interactive path when a person waits for it,
// at once for muster doctor. Its error is the runner's own — no limiter token within the budget — and ends the check.
type Runner func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
	error)

// Direct is the Runner that calls at once with c: muster doctor, which waits for no token.
func Direct(c delivery.Call) Runner {
	return func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		return f(ctx, c), nil
	}
}

// Path is the interactive path, declared by its consumer; *delivery.Interactive implements it.
type Path interface {
	Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error)
}

// Interactive is the Runner of a check a person waits for: its request through the interactive path, limited by s.
func Interactive(in Path, s delivery.Subject) Runner {
	return func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		return in.Do(ctx, s, delivery.ReadOp(f))
	}
}

// ChannelCheck is the result of the Destination check of a channel: the channel's name when it passed; otherwise the
// message, in English, with its code; and the outcome for delivery — ok, or the failure as Result.Outcome classifies
// it, with the message as its error — which the Broken probe keeps.
type ChannelCheck struct {
	Name    string
	Message string
	Code    string
	Outcome delivery.Outcome
}

// OK reports whether the check passed.
func (c ChannelCheck) OK() bool { return c.Outcome.Kind == delivery.OutcomeOK }

// CheckDestination runs the Destination check of the channel channelID through run (C-22.FR-3): one checkChannel,
// which posts nothing. A token the bridge refuses with 401 is token_invalid; any other forbidden answer is
// channel_forbidden, with the bridge's detail as the message; a request without an answer, or one the outbound
// address policy refuses, has the code of delivery.FailureCode; any other error class is request_failed, with the
// class named in the message. Its error is the runner's own.
func CheckDestination(ctx context.Context, c *Client, run Runner, channelID string) (ChannelCheck, error) {
	var name string
	var r Result
	if _, err := run(ctx, func(ctx context.Context, call delivery.Call) delivery.Outcome {
		name, r = c.CheckChannel(ctx, call.Class, channelID)
		return r.Outcome()
	}); err != nil {
		return ChannelCheck{}, err
	}
	return checkOf(name, r), nil
}

// checkOf is the check of a checkChannel that ended with r; a blocked address is classified forbidden too, so it is
// told apart before a forbidden answer.
func checkOf(name string, r Result) ChannelCheck {
	out := ChannelCheck{Outcome: r.Outcome()}
	switch {
	case r.OK():
		out.Name = name
		return out
	case r.Status == http.StatusUnauthorized:
		out.Code, out.Message = CodeTokenInvalid, MessageTokenInvalid
	case r.Blocked || r.Status == 0:
		out.Code, out.Message = delivery.FailureCode(r.Blocked, r.Network), cmp.Or(string(r.Error), checkFailed)
	case r.Classification.Class == ClassForbidden:
		out.Code, out.Message = CodeChannelForbidden, cmp.Or(string(r.Error), MessageChannelForbidden)
	default:
		out.Code = delivery.CodeRequestFailed
		out.Message = "The bridge answered " + string(r.Classification.Class) + ": " + cmp.Or(string(r.Error),
			"no details")
	}
	out.Outcome.Error = outbound.Untrusted(out.Message)
	return out
}

// checkFailed is the message of a failed request that said nothing.
const checkFailed = "The check failed."

// The step of the dry probe of a Bridge Connection (C-22.FR-2) and the messages of what answered it.
const (
	StepDryProbe = "dry_probe"

	MessageWrongPrefix = "Wrong path prefix: the server answered 404."
	messageNotBridge   = "The address answered %d with %s, not with a problem of the Bridge protocol."
	messageOtherStatus = "The bridge answered %d instead of 401 with a problem."
	noContentType      = "no content type"
)

// The codes of the dry probe that reached no bridge (ConnectionCheckStep.code); a probe without an answer has the code
// of delivery.FailureCode.
const (
	CodeNotBridge        = "not_bridge"
	CodeWrongPrefix      = "wrong_path_prefix"
	CodeUnexpectedStatus = "unexpected_status"
)

// Probe is the result of a dry probe: whether it passed, its latency on the real clock, and when it failed what
// answered, in English, with its code, which clients translate.
type Probe struct {
	OK      bool
	Latency time.Duration
	Message string
	Code    string
}

// RunDryProbe makes the dry probe of c through run (C-22.FR-2): `GET <base>/v1/info` without the token, timed on the
// real clock, on a client that may have none. It passes on a 401 answer with a problem and otherwise names what
// answered. A rate_limited answer is handed to run as Transient: a request without a token says nothing of the bot's
// budget, so it never holds the Connection's limiter, least of all when an unsaved base URL answered it. Its error is
// the runner's own.
func RunDryProbe(ctx context.Context, c *Client, run Runner, realClock clock.Clock) (Probe, error) {
	var res outbound.Result
	var r Result
	var latency time.Duration
	if _, err := run(ctx, func(ctx context.Context, call delivery.Call) delivery.Outcome {
		start := realClock.Now()
		res, r = c.dryProbe(ctx, call.Class)
		latency = realClock.Now().Sub(start)
		o := r.Outcome()
		if o.Kind == delivery.OutcomeRetryAfter {
			o.Kind, o.Scope, o.RetryAfter = delivery.OutcomeTransient, "", 0
		}
		return o
	}); err != nil {
		return Probe{}, err
	}
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil {
		mediaType = ""
	}
	p := probeVerdict(res, r, c.settings.mask(mediaType))
	p.Latency = latency
	return p, nil
}

// probeVerdict says whether the answer to the dry probe is a bridge's, in the order of C-22.FR-2: no answer has the
// code of delivery.FailureCode, a proxy that refused the credentials — which resultOf turns into a call without an
// answer when the Connection has a proxy — proxy_refused, 404 wrong_path_prefix, an answer that is not a problem
// not_bridge with its status and media type, 401 with a problem passes, and another status with a problem
// unexpected_status.
func probeVerdict(res outbound.Result, r Result, mediaType string) Probe {
	switch {
	case r.Status == 0 && r.Network == outbound.NetworkProxyAuth, r.Status == http.StatusProxyAuthRequired:
		return Probe{Message: MessageProxyRefused, Code: delivery.CodeProxyRefused}
	case r.Status == 0:
		return Probe{Message: cmp.Or(string(r.Error), checkFailed), Code: delivery.FailureCode(r.Blocked, r.Network)}
	case r.Status == http.StatusNotFound:
		return Probe{Message: MessageWrongPrefix, Code: CodeWrongPrefix}
	case problemOf(res) == nil:
		return Probe{Message: fmt.Sprintf(messageNotBridge, r.Status, cmp.Or(mediaType, noContentType)),
			Code: CodeNotBridge}
	case r.Status == http.StatusUnauthorized:
		return Probe{OK: true}
	}
	return Probe{Message: fmt.Sprintf(messageOtherStatus, r.Status), Code: CodeUnexpectedStatus}
}
