// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// The steps of the Destination check of a Mattermost Destination and their messages (C-13.FR-10).
const (
	StepToken        = "token"
	StepBotInChannel = "bot_in_channel"

	MessageTokenInvalid = "The bot token is not valid." //nolint:gosec // G101: a message, not a credential
	MessageNotMember    = "The bot is not a member of this channel."
	MessageOtherTeam    = "The channel is not in this team."
	MessageArchived     = "The channel is archived."
)

// The codes of the steps that failed (ConnectionCheckStep.code, DestinationCheckItem.code), one per message above; any
// other failure has the code of delivery.FailureCode.
const (
	CodeTokenInvalid = "token_invalid"
	CodeNotMember    = "bot_not_in_channel"
	CodeOtherTeam    = "channel_in_other_team"
	CodeArchived     = "channel_archived"
)

// Step is one step of a check: its name, whether it passed, and when it failed the message for the person, in English,
// its code, which clients translate, and the outcome for delivery.
type Step struct {
	Name    string
	OK      bool
	Message string
	Code    string
	Outcome delivery.Outcome
}

// Check is the result of a Destination check: the steps that ran, the names of the team and the channel when every
// step passed, and the outcome of the whole — ok, or the outcome of the step that failed — for the Broken probe.
type Check struct {
	Steps       []Step
	TeamName    string
	ChannelName string
	// Bot is the bot the token belongs to, once the step token passed: the callback of button presses tells the bot's
	// own press of a test message by its user id (C-16.FR-3).
	Bot     User
	Outcome delivery.Outcome
}

// OK reports whether every step passed.
func (c Check) OK() bool { return c.Outcome.Kind == delivery.OutcomeOK }

// Runner makes one request of a check with the Call it is made in: on the interactive path when a person waits for it,
// directly in the delivery client class for the Broken probe. Its error is the runner's own — no limiter token within
// the budget, a failed limiter — and ends the check unfinished.
type Runner func(ctx context.Context, f func(ctx context.Context, c delivery.Call) delivery.Outcome) (delivery.Outcome,
	error)

// Direct is the Runner that calls at once with c: the Broken probe and muster doctor, which wait for no token.
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

// call makes one read of a check through run and keeps its Result.
func call(ctx context.Context, run Runner, read func(ctx context.Context, class outbound.Class) Result) (Result,
	error) {
	var r Result
	_, err := run(ctx, func(ctx context.Context, c delivery.Call) delivery.Outcome {
		r = read(ctx, c.Class)
		return r.Outcome
	})
	return r, err
}

// CheckDestination runs the Destination check of the channel channelID in the team teamID without sending a message:
// GET /api/v4/users/me for the token, then GET /api/v4/channels/{channel_id}/members/me for the bot's membership,
// then, once both passed, the channel and its team for their names. A step that fails ends the check; the token fails
// with MessageTokenInvalid on 401, the membership with MessageNotMember on 403 or 404, with MessageOtherTeam for a
// channel of another team and MessageArchived for an archived one, each with its code, and any other failure with the
// masked text of the answer and the code of Result.FailureCode.
func CheckDestination(ctx context.Context, c *Client, run Runner, teamID, channelID string) (Check, error) {
	var out Check
	fail := func(name, code, message string, r Result) Check {
		if message == "" {
			message, code = string(r.Outcome.Error), r.FailureCode()
		}
		out.Steps = append(out.Steps, Step{Name: name, Message: message, Code: code, Outcome: r.Outcome})
		out.Outcome = r.Outcome
		return out
	}
	r, err := call(ctx, run, func(ctx context.Context, class outbound.Class) Result {
		var r Result
		out.Bot, r = c.Me(ctx, class)
		return r
	})
	if err != nil {
		return Check{}, err
	}
	if !r.OK() {
		if r.Status == http.StatusUnauthorized {
			return fail(StepToken, CodeTokenInvalid, MessageTokenInvalid, r), nil
		}
		return fail(StepToken, "", "", r), nil
	}
	out.Steps = append(out.Steps, Step{Name: StepToken, OK: true})
	if r, err = call(ctx, run, func(ctx context.Context, class outbound.Class) Result {
		return c.Member(ctx, class, channelID)
	}); err != nil {
		return Check{}, err
	}
	if !r.OK() {
		if r.Status == http.StatusForbidden || r.Status == http.StatusNotFound {
			return fail(StepBotInChannel, CodeNotMember, MessageNotMember, r), nil
		}
		return fail(StepBotInChannel, "", "", r), nil
	}
	var ch Channel
	if r, err = call(ctx, run, func(ctx context.Context, class outbound.Class) Result {
		var r Result
		ch, r = c.Channel(ctx, class, channelID)
		return r
	}); err != nil {
		return Check{}, err
	}
	if !r.OK() {
		return fail(StepBotInChannel, "", "", r), nil
	}
	switch {
	case ch.TeamID != teamID:
		return fail(StepBotInChannel, CodeOtherTeam, MessageOtherTeam, Result{Outcome: delivery.Outcome{
			Kind: delivery.OutcomeFatal, Error: MessageOtherTeam}}), nil
	case ch.DeleteAt != 0:
		// Mattermost refuses posts to an archived channel with 404, which is Fatal (C-13.FR-5).
		return fail(StepBotInChannel, CodeArchived, MessageArchived, Result{Outcome: delivery.Outcome{
			Kind: delivery.OutcomeFatal, Error: MessageArchived}}), nil
	}
	var team Team
	if r, err = call(ctx, run, func(ctx context.Context, class outbound.Class) Result {
		var r Result
		team, r = c.Team(ctx, class, teamID)
		return r
	}); err != nil {
		return Check{}, err
	}
	if !r.OK() {
		return fail(StepBotInChannel, "", "", r), nil
	}
	out.Steps = append(out.Steps, Step{Name: StepBotInChannel, OK: true})
	out.TeamName, out.ChannelName = team.Name, ch.Name
	out.Outcome = delivery.Outcome{Kind: delivery.OutcomeOK}
	return out, nil
}

// The step of the dry probe of a Mattermost Connection (C-13.FR-2) and the messages of what answered it.
const (
	StepDryProbe = "dry_probe"

	MessageWrongPrefix   = "Wrong path prefix: the server answered 404."
	MessageProxyRefused  = delivery.MessageProxyRefused
	messageNotMattermost = "The address answered %d with %s, not as a Mattermost server."
	messageStatusNotOK   = "The address answered 200 with JSON whose status is not OK."
	messageOtherStatus   = "The server answered %d instead of 200 with JSON whose status is OK."
	noContentType        = "no content type"
)

// The codes of the dry probe that reached no Mattermost server (ConnectionCheckStep.code); a probe without an answer
// has the code of delivery.FailureCode.
const (
	CodeNotMattermost    = "not_mattermost"
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

// RunDryProbe makes the dry probe of c through run (C-13.FR-2): `GET <server>/api/v4/system/ping` without the token,
// timed on the real clock, on a client that may have none. It passes on a 200 JSON answer whose status is OK and
// otherwise names what answered, a server it could not reach as delivery.FailureMessage does, without the server's
// name through a proxy, whose own name is the one that did not resolve. Its error is the runner's own.
func RunDryProbe(ctx context.Context, c *Client, run Runner, realClock clock.Clock) (Probe, error) {
	var r Result
	var mediaType string
	var latency time.Duration
	if _, err := run(ctx, func(ctx context.Context, call delivery.Call) delivery.Outcome {
		start := realClock.Now()
		r, mediaType = c.DryProbe(ctx, call.Class)
		latency = realClock.Now().Sub(start)
		return r.Outcome
	}); err != nil {
		return Probe{}, err
	}
	address := c.settings.ServerURL
	if c.Via() == ViaProxy {
		address = ""
	}
	p := probeVerdict(r, mediaType, address)
	p.Latency = latency
	return p, nil
}

// probeVerdict says whether the answer to the dry probe of the server serverURL, empty through a proxy, is a Mattermost
// server's, in the order of C-13.FR-2: no answer has the code of Result.FailureCode, with the text of
// delivery.FailureMessage — the name, the time, the proxy that refused the credentials — or the error's own; then 407
// proxy_refused, 404 wrong_path_prefix, an answer that is not a JSON object not_mattermost with its status and media
// type, a 200 whose status is OK passes and any other 200 is not_mattermost, and another status with JSON
// unexpected_status.
func probeVerdict(r Result, mediaType, serverURL string) Probe {
	if r.Status == 0 {
		if message, ok := delivery.FailureMessage(r.Network, serverURL, "", callTimeout); ok {
			return Probe{Message: message, Code: r.FailureCode()}
		}
		return Probe{Message: string(r.Outcome.Error), Code: r.FailureCode()}
	}
	var answer map[string]json.RawMessage
	isObject := json.Unmarshal(r.Body, &answer) == nil && answer != nil
	var status string
	_ = json.Unmarshal(answer["status"], &status) // a status that is not a string is not OK
	switch {
	case r.Status == http.StatusProxyAuthRequired:
		return Probe{Message: MessageProxyRefused, Code: delivery.CodeProxyRefused}
	case r.Status == http.StatusNotFound:
		return Probe{Message: MessageWrongPrefix, Code: CodeWrongPrefix}
	case !isObject:
		return Probe{Message: fmt.Sprintf(messageNotMattermost, r.Status, cmp.Or(mediaType, noContentType)),
			Code: CodeNotMattermost}
	case r.Status == http.StatusOK && status == "OK":
		return Probe{OK: true}
	case r.Status == http.StatusOK:
		return Probe{Message: messageStatusNotOK, Code: CodeNotMattermost}
	}
	return Probe{Message: fmt.Sprintf(messageOtherStatus, r.Status), Code: CodeUnexpectedStatus}
}
