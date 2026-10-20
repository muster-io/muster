// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
)

// The steps of the connection check of a Slack Connection (C-24.FR-2).
const (
	StepToken    = "token"
	StepScopes   = "scopes"
	StepAppToken = "app_token"
)

// The codes of the failures the steps name (ConnectionCheckStep.code), which clients translate: a token Slack refused,
// a token that is not a bot's, missing scopes and two tokens of different apps. Any other failure has the code of
// delivery.FailureCode.
const (
	CodeTokenInvalid   = "token_invalid"
	CodeWrongTokenType = "wrong_token_type"
	CodeMissingScope   = "missing_scope"
	CodeAppMismatch    = "app_mismatch"
)

// The messages of the steps that failed, in English (reference.md).
const (
	MessageTokenInvalid   = "Slack refused the token." //nolint:gosec // G101: a message, not a credential
	MessageWrongTokenType = "This is not a bot token." //nolint:gosec // G101: a message, not a credential
	messageMissingScope   = "The bot token lacks the scopes %s."
	MessageAppMismatch    = "The app-level token belongs to another Slack app than the bot token."
	messageNoApp          = "Slack's answer names no app."
	checkFailed           = "The check failed."
)

// RequiredScopes are the scopes the bot token needs, and no other (C-24.FR-1).
var RequiredScopes = []string{"chat:write", "channels:read", "groups:read", "im:write", "users:read",
	"users:read.email"}

// MissingScopesMessage is the message of the step scopes for the scopes the bot token lacks.
func MissingScopesMessage(missing []string) string {
	return fmt.Sprintf(messageMissingScope, strings.Join(missing, ", "))
}

// Runner makes one request of a check with the Call it is made in: on the interactive path when a person waits for it,
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

// Interactive is the Runner of a check a person waits for: each request through the interactive path, limited by s.
func Interactive(in Path, s delivery.Subject) Runner {
	return func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
		error) {
		return in.Do(ctx, s, delivery.ReadOp(f))
	}
}

// Step is one step of a check: whether it passed or was skipped, its latency on the real clock and path, and when it
// failed what answered, in English, with its code, which clients translate, and with CodeMissingScope the scopes the
// bot token lacks, which clients name in their own words (D396). The step scopes makes no request and has no latency.
type Step struct {
	Name          string
	OK            bool
	Skipped       bool
	Latency       time.Duration
	Via           string
	Message       string
	Code          string
	MissingScopes []string
}

// Identity is what a check found: the workspace's id, name and address, the bot's id, user id and name from the step
// token, and the app's id from the step app_token, empty without it.
type Identity struct {
	TeamID    string
	TeamName  string
	TeamURL   string
	BotID     string
	BotUserID string
	BotName   string
	AppID     string
}

// Check is the result of a connection check: its steps, and what it found once the step token passed.
type Check struct {
	Steps    []Step
	Identity *Identity
}

// OK reports whether no step failed; skipped steps do not fail a check.
func (c Check) OK() bool {
	_, failed := c.Failed()
	return !failed && len(c.Steps) > 0
}

// Failed is the first step that failed, if any.
func (c Check) Failed() (Step, bool) {
	for _, s := range c.Steps {
		if !s.OK && !s.Skipped {
			return s, true
		}
	}
	return Step{}, false
}

// RunCheck runs the connection check of c in the event mode mode (C-24.FR-2) through run, timing each request on the
// real clock:
//
//  1. token — auth.test with the bot token: the bot and its workspace; a token Slack refuses is token_invalid, and one
//     that is not a bot's — not_allowed_token_type, or an answer without bot_id — wrong_token_type;
//  2. scopes — no request: the x-oauth-scopes of that answer must name every scope of RequiredScopes, else
//     missing_scope names the missing ones in their order, in the message and in MissingScopes;
//  3. app_token, in socket mode only — auth.test with the app-level token answers the app's id, which must equal the
//     bot's app_id that bots.info answers, else app_mismatch.
//
// A step that fails skips the steps after it. Any other failure has the code of delivery.FailureCode: without an answer
// with the text of delivery.FailureMessage — the name of Slack's address, which is left out through a proxy whose own
// name is the one that did not resolve, the TLS failure, the time, the proxy — or the error's own; with an answer
// request_failed with Slack's masked error. A RetryAfter goes back to run as it is, so that the interactive path holds
// the Connection's limiter. Its error is the runner's own.
func RunCheck(ctx context.Context, c *Client, run Runner, realClock clock.Clock, mode string) (Check, error) {
	address := c.Base()
	if c.Via() == ViaProxy {
		address = ""
	}
	var out Check
	skipRest := func(names ...string) {
		for _, n := range names {
			if n == StepAppToken && mode != EventModeSocket {
				continue
			}
			out.Steps = append(out.Steps, Step{Name: n, Skipped: true, Via: c.Via()})
		}
	}
	var at AuthTest
	var r Result
	latency, err := timed(ctx, run, realClock, func(ctx context.Context, call delivery.Call) Result {
		at, r = c.AuthTest(ctx, call.Class)
		return r
	})
	if err != nil {
		return Check{}, err
	}
	token := Step{Name: StepToken, OK: r.OK(), Latency: latency, Via: c.Via()}
	switch {
	case !r.OK() && slices.Contains(tokenErrors, r.ErrorCode):
		token.Code, token.Message = CodeTokenInvalid, MessageTokenInvalid
	case !r.OK() && r.ErrorCode == "not_allowed_token_type", r.OK() && at.BotID == "":
		token.OK, token.Code, token.Message = false, CodeWrongTokenType, MessageWrongTokenType
	case !r.OK():
		token.Code, token.Message = failure(r, address)
	}
	out.Steps = append(out.Steps, token)
	if !token.OK {
		skipRest(StepScopes, StepAppToken)
		return out, nil
	}
	id := &Identity{TeamID: at.TeamID, TeamName: at.Team, TeamURL: at.URL, BotID: at.BotID, BotUserID: at.UserID,
		BotName: at.User}
	out.Identity = id
	scopes := Step{Name: StepScopes, OK: true, Via: c.Via()}
	var missing []string
	for _, s := range RequiredScopes {
		if !slices.Contains(r.Scopes, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		scopes.OK, scopes.Code, scopes.Message = false, CodeMissingScope, MissingScopesMessage(missing)
		scopes.MissingScopes = missing
	}
	out.Steps = append(out.Steps, scopes)
	if !scopes.OK {
		skipRest(StepAppToken)
		return out, nil
	}
	if mode != EventModeSocket {
		return out, nil
	}
	app, err := appStep(ctx, c, run, realClock, id, address)
	if err != nil {
		return Check{}, err
	}
	out.Steps = append(out.Steps, app)
	return out, nil
}

// appStep is the step app_token: auth.test with the app-level token, then bots.info for the bot of the step token,
// whose app ids must be equal; when they are, the app's id joins id.
func appStep(ctx context.Context, c *Client, run Runner, realClock clock.Clock, id *Identity, address string) (Step,
	error) {
	s := Step{Name: StepAppToken, Via: c.Via()}
	var app AuthTest
	var r Result
	latency, err := timed(ctx, run, realClock, func(ctx context.Context, call delivery.Call) Result {
		app, r = c.AppAuthTest(ctx, call.Class)
		return r
	})
	if err != nil {
		return Step{}, err
	}
	s.Latency = latency
	switch {
	case !r.OK() && slices.Contains(tokenErrors, r.ErrorCode):
		s.Code, s.Message = CodeTokenInvalid, MessageTokenInvalid
		return s, nil
	case !r.OK():
		s.Code, s.Message = failure(r, address)
		return s, nil
	case app.AppID == "":
		s.Code, s.Message = delivery.CodeRequestFailed, messageNoApp
		return s, nil
	}
	var bot Bot
	latency, err = timed(ctx, run, realClock, func(ctx context.Context, call delivery.Call) Result {
		bot, r = c.BotsInfo(ctx, call.Class, id.BotID)
		return r
	})
	if err != nil {
		return Step{}, err
	}
	s.Latency += latency
	switch {
	case !r.OK() && slices.Contains(tokenErrors, r.ErrorCode):
		s.Code, s.Message = CodeTokenInvalid, MessageTokenInvalid
	case !r.OK():
		s.Code, s.Message = failure(r, address)
	case bot.AppID == "":
		s.Code, s.Message = delivery.CodeRequestFailed, messageNoApp
	case bot.AppID != app.AppID:
		s.Code, s.Message = CodeAppMismatch, MessageAppMismatch
	default:
		s.OK, id.AppID = true, app.AppID
	}
	return s, nil
}

// failure is the code and message of a failed request that its step does not name: without an answer, or refused by
// the outbound address policy, the code of delivery.FailureCode with the text of delivery.FailureMessage for the
// address, empty through a proxy, or the error's own; with an answer request_failed with Slack's masked error.
func failure(r Result, address string) (string, string) {
	if r.Status == 0 {
		if message, ok := delivery.FailureMessage(r.Network, address, r.Detail, CallTimeout); ok {
			return r.CheckCode(), message
		}
		return r.CheckCode(), cmp.Or(string(r.Error), checkFailed)
	}
	return delivery.CodeRequestFailed, cmp.Or(string(r.Error), checkFailed)
}

// timed makes one request of a check through run and measures it on the real clock; the request's outcome goes back to
// run as Result.Outcome classifies it.
func timed(ctx context.Context, run Runner, realClock clock.Clock, f func(ctx context.Context, c delivery.Call) Result) (
	time.Duration, error) {
	var latency time.Duration
	_, err := run(ctx, func(ctx context.Context, c delivery.Call) delivery.Outcome {
		start := realClock.Now()
		r := f(ctx, c)
		latency = realClock.Now().Sub(start)
		return r.Outcome()
	})
	return latency, err
}
