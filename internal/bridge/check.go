// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"cmp"
	"context"
	"net/http"

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
