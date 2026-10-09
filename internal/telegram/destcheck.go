// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// The steps of the Destination check of a Telegram Destination (C-14.FR-14).
const (
	StepChannelExists    = "channel_exists"
	StepDiscussionGroup  = "discussion_group"
	StepBotRightsChannel = "bot_rights_channel"
	StepBotRightsGroup   = "bot_rights_group"
)

// The messages of the steps that failed; the two of the discussion group are those of reference.md.
const (
	MessageChannelNotFound = "The channel was not found, or the bot cannot see it."
	MessageNotChannel      = "This chat is not a channel."
	MessageNoComments      = "Comments are not enabled for this channel. Enable comments in the channel settings in " +
		"Telegram; this creates its discussion group."
	MessageChannelNotAdmin = "The bot is not an admin of the channel."
	MessageMayNotPost      = "The bot may not post messages in the channel."
	MessageMayNotEdit      = "The bot may not edit messages in the channel."
	MessageMoved           = "The channel or its discussion group changed since the Destination was saved. Save the " +
		"Destination again."
	messageGroupNotAdmin = "The bot is not an admin of the discussion group %s. Make the bot an admin there, " +
		"allowed to post messages."
)

// MessageGroupNotAdmin is the message of a discussion group whose admins do not include the bot.
func MessageGroupNotAdmin(group string) string { return fmt.Sprintf(messageGroupNotAdmin, group) }

// DestinationStep is one step of a Destination check: whether it passed, and when it failed the message for the person
// and the outcome for delivery.
type DestinationStep struct {
	Name    string
	OK      bool
	Message string
	Outcome delivery.Outcome
}

// DestinationCheck is the result of a Destination check: the steps that ran; once the channel was read its numeric id
// and title, and once its discussion group was found the group's id and title; and the outcome of the whole — ok, or
// the outcome of the step that failed — for the Broken probe.
type DestinationCheck struct {
	Steps        []DestinationStep
	ChannelID    int64
	ChannelTitle string
	GroupID      int64
	GroupTitle   string
	Outcome      delivery.Outcome
}

// OK reports whether every step passed.
func (c DestinationCheck) OK() bool { return c.Outcome.Kind == delivery.OutcomeOK }

// Moved reports whether a check that passed found another channel or discussion group than the ids stored with the
// Destination, 0 for one not known: comments were linked to another group, or the @username moved to another channel.
// Muster sends to the stored ids, which a save of the Destination updates.
func (c DestinationCheck) Moved(channelID, groupID int64) bool {
	return c.OK() && ((channelID != 0 && c.ChannelID != channelID) || (groupID != 0 && c.GroupID != groupID))
}

// read makes one request of a check through run and keeps its Result.
func read(ctx context.Context, run Runner, f func(ctx context.Context, class outbound.Class) Result) (Result, error) {
	var r Result
	_, err := run(ctx, func(ctx context.Context, c delivery.Call) delivery.Outcome {
		r = f(ctx, c.Class)
		return r.Outcome
	})
	return r, err
}

// CheckDestination runs the Destination check of the channel, an id or an @username, without sending a message
// (C-14.FR-14): getChat on the channel — it exists and is a channel — whose linked_chat_id is its discussion group
// (F-002); getChatMember for the bot in the channel — an admin allowed to post and edit messages (F-001); then getChat
// on the group for its title and getChatMember for the bot there — an admin, who may always post in a group; a bot
// outside the group gets 403 (F-003). The bot's id comes from getMe once per client, which later checks skip. A step
// that fails ends the check: a refusal that a person must fix is Fatal with the step's message, which the Broken probe
// keeps as the reason; an answer that says nothing of the chat, such as a timeout, keeps its own outcome.
func CheckDestination(ctx context.Context, c *Client, run Runner, channel string) (DestinationCheck, error) {
	var out DestinationCheck
	fail := func(name, message string, r Result) DestinationCheck {
		o := r.Outcome
		switch {
		case message != "":
			o = delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(message)}
		case o.Kind == delivery.OutcomeOK:
			o = delivery.Outcome{Kind: delivery.OutcomeUnknown, Error: "the check failed"}
		}
		if message == "" {
			message = string(o.Error)
		}
		out.Steps = append(out.Steps, DestinationStep{Name: name, Message: message, Outcome: o})
		out.Outcome = o
		return out
	}
	pass := func(name string) { out.Steps = append(out.Steps, DestinationStep{Name: name, OK: true}) }
	bot := c.knownBotID()
	if bot == 0 {
		r, err := read(ctx, run, func(ctx context.Context, class outbound.Class) Result {
			var r Result
			bot, r = c.BotID(ctx, class)
			return r
		})
		if err != nil {
			return DestinationCheck{}, err
		}
		if !r.OK() {
			return fail(StepChannelExists, refusedToken(r), r), nil
		}
	}
	var ch ChatInfo
	r, err := read(ctx, run, func(ctx context.Context, class outbound.Class) Result {
		var r Result
		ch, r = c.GetChat(ctx, class, channel)
		return r
	})
	if err != nil {
		return DestinationCheck{}, err
	}
	switch {
	case !r.OK():
		message := refusedToken(r)
		if message == "" && isAnswered(r) {
			message = MessageChannelNotFound
		}
		return fail(StepChannelExists, message, r), nil
	case ch.Type != "channel":
		return fail(StepChannelExists, MessageNotChannel, r), nil
	}
	out.ChannelID, out.ChannelTitle = ch.ID, ch.Title
	pass(StepChannelExists)
	if ch.LinkedChatID == 0 {
		return fail(StepDiscussionGroup, MessageNoComments, r), nil
	}
	out.GroupID = ch.LinkedChatID
	pass(StepDiscussionGroup)
	chatID, groupID := strconv.FormatInt(ch.ID, 10), strconv.FormatInt(ch.LinkedChatID, 10)
	var m ChatMember
	if r, err = read(ctx, run, func(ctx context.Context, class outbound.Class) Result {
		var r Result
		m, r = c.GetChatMember(ctx, class, chatID, bot)
		return r
	}); err != nil {
		return DestinationCheck{}, err
	}
	switch {
	case !r.OK() && isAnswered(r):
		return fail(StepBotRightsChannel, MessageChannelNotAdmin, r), nil
	case !r.OK():
		return fail(StepBotRightsChannel, "", r), nil
	case m.Status == StatusCreator:
	case m.Status != StatusAdministrator:
		return fail(StepBotRightsChannel, MessageChannelNotAdmin, r), nil
	case !m.CanPostMessages:
		return fail(StepBotRightsChannel, MessageMayNotPost, r), nil
	case !m.CanEditMessages:
		return fail(StepBotRightsChannel, MessageMayNotEdit, r), nil
	}
	pass(StepBotRightsChannel)
	var group ChatInfo
	if r, err = read(ctx, run, func(ctx context.Context, class outbound.Class) Result {
		var r Result
		group, r = c.GetChat(ctx, class, groupID)
		return r
	}); err != nil {
		return DestinationCheck{}, err
	}
	out.GroupTitle = group.Title
	name := group.Title
	if !r.OK() || name == "" {
		// A group the bot cannot read is named by its id; whether the bot is in it is the next request's to say.
		name = groupID
	}
	if r, err = read(ctx, run, func(ctx context.Context, class outbound.Class) Result {
		var r Result
		m, r = c.GetChatMember(ctx, class, groupID, bot)
		return r
	}); err != nil {
		return DestinationCheck{}, err
	}
	switch {
	case !r.OK() && isAnswered(r):
		return fail(StepBotRightsGroup, MessageGroupNotAdmin(name), r), nil
	case !r.OK():
		return fail(StepBotRightsGroup, "", r), nil
	case m.Status != StatusCreator && m.Status != StatusAdministrator:
		return fail(StepBotRightsGroup, MessageGroupNotAdmin(name), r), nil
	}
	pass(StepBotRightsGroup)
	out.Outcome = delivery.Outcome{Kind: delivery.OutcomeOK}
	return out, nil
}

// refusedToken is MessageTokenInvalid for an answer 401, empty otherwise.
func refusedToken(r Result) string {
	if r.Status == http.StatusUnauthorized || r.Code == http.StatusUnauthorized {
		return MessageTokenInvalid
	}
	return ""
}

// isAnswered reports whether the Bot API refused the request itself, with 400 or 403 — not a transport failure, a
// timeout, a 429 or a 5xx, which say nothing of the chat.
func isAnswered(r Result) bool {
	code := r.Code
	if code == 0 {
		code = r.Status
	}
	return code == http.StatusBadRequest || code == http.StatusForbidden || code == http.StatusNotFound
}
