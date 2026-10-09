// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"context"
	"net/http"

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

// Step is one step of a check: its name, whether it passed, and when it failed the message for the person and the
// outcome for delivery.
type Step struct {
	Name    string
	OK      bool
	Message string
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
// channel of another team and MessageArchived for an archived one, and any other failure with the masked text of the
// answer.
func CheckDestination(ctx context.Context, c *Client, run Runner, teamID, channelID string) (Check, error) {
	var out Check
	fail := func(name, message string, r Result) Check {
		if message == "" {
			message = string(r.Outcome.Error)
		}
		out.Steps = append(out.Steps, Step{Name: name, Message: message, Outcome: r.Outcome})
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
		message := ""
		if r.Status == http.StatusUnauthorized {
			message = MessageTokenInvalid
		}
		return fail(StepToken, message, r), nil
	}
	out.Steps = append(out.Steps, Step{Name: StepToken, OK: true})
	if r, err = call(ctx, run, func(ctx context.Context, class outbound.Class) Result {
		return c.Member(ctx, class, channelID)
	}); err != nil {
		return Check{}, err
	}
	if !r.OK() {
		message := ""
		if r.Status == http.StatusForbidden || r.Status == http.StatusNotFound {
			message = MessageNotMember
		}
		return fail(StepBotInChannel, message, r), nil
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
		return fail(StepBotInChannel, "", r), nil
	}
	switch {
	case ch.TeamID != teamID:
		return fail(StepBotInChannel, MessageOtherTeam, Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal,
			Error: MessageOtherTeam}}), nil
	case ch.DeleteAt != 0:
		// Mattermost refuses posts to an archived channel with 404, which is Fatal (C-13.FR-5).
		return fail(StepBotInChannel, MessageArchived, Result{Outcome: delivery.Outcome{Kind: delivery.OutcomeFatal,
			Error: MessageArchived}}), nil
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
		return fail(StepBotInChannel, "", r), nil
	}
	out.Steps = append(out.Steps, Step{Name: StepBotInChannel, OK: true})
	out.TeamName, out.ChannelName = team.Name, ch.Name
	out.Outcome = delivery.Outcome{Kind: delivery.OutcomeOK}
	return out, nil
}
