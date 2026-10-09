// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// ErrNoTarget is a Destination that has no Telegram Connection to send through: it, or its Connection, no longer
// exists, or it is not a Telegram Destination.
var ErrNoTarget = errors.New("the destination has no telegram connection")

// errNoGroup is a Thread reply of a Destination whose discussion group is not known.
const errNoGroup = "the discussion group of the destination is not known; run its Destination check"

// Target is where a Telegram Destination sends: the client of its Connection and the Connection's id; the channel as
// entered, and the numeric ids of the channel and of its discussion group that its Destination check learned, 0 when
// unknown.
type Target struct {
	Client       *Client
	ConnectionID int64
	Channel      string
	ChannelID    int64
	GroupID      int64
}

// channel is the chat that Root messages go to: the channel's numeric id once known, otherwise as entered.
func (t Target) channel() string {
	if t.ChannelID != 0 {
		return strconv.FormatInt(t.ChannelID, 10)
	}
	return t.Channel
}

// Targets find the Target of a Destination by its id, declared by their consumer; *connections.Service implements
// them. A Destination without one is ErrNoTarget. AwaitUpdates waits until no update of the Connection is being
// handled, under its update lock.
type Targets interface {
	TelegramTarget(ctx context.Context, destinationID int64) (Target, error)
	AwaitUpdates(ctx context.Context, connectionID int64) error
}

// Adapter is the delivery adapter of Telegram Destinations (C-11.FR-7, C-14): Publish and Update of Root messages as
// channel posts, Reply into the comment Thread in the discussion group, called by the delivery worker and the
// interactive path only, in the client class of their Call; and Check, the Destination check of C-14.FR-14 for the
// Broken probe. Clock is the real clock of the window in which two 429s of a Connection hold all of it; nil is the
// system's.
type Adapter struct {
	Targets Targets
	Clock   clock.Clock

	limits limits
}

var (
	_ delivery.Adapter = (*Adapter)(nil)
	_ delivery.Checker = (*Adapter)(nil)
)

// LengthLimit is the longest message, which a Root message is shortened to (C-14.FR-13).
func (a *Adapter) LengthLimit() int { return LengthLimit }

// Markup is the markup of Telegram messages.
func (a *Adapter) Markup() messages.Markup { return messages.MarkupHTML }

// Publish creates the Root message m as a channel post with its keyboard (C-14.FR-2, FR-16): the Mentions of a Loud
// call after its heading, and disable_notification on a Quiet one (C-14.FR-6, F-012). The channel carries only Root
// messages and Storm summaries; Muster never replies inside it (F-015).
func (a *Adapter) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	w := writer{plain: c.Plain}
	mentioned := ""
	if c.Loudness == groups.Loud {
		mentioned = mentionText(c.Targets, w)
	}
	out := a.message(c, w, m, mentioned)
	out.ChatID, out.DisableNotification = chatRef(t.channel()), c.Loudness != groups.Loud
	if len(out.ReplyMarkup.InlineKeyboard) == 0 {
		out.ReplyMarkup = nil
	}
	sent, r := t.Client.sendMessage(ctx, c.Class, out)
	return a.sent(t, c, r, sent)
}

// Update edits the Root message messageID to m, always with the whole keyboard of its new state (C-14.FR-15, F-011),
// which an empty one removes on purpose. An edit notifies nobody and mentions nobody (F-012). It first waits until no
// update of the Connection is being handled, so that a press is answered before the edit its Command causes
// (C-14.FR-5, AC-18): the press handler runs under the same update lock until it has answered.
func (a *Adapter) Update(ctx context.Context, c delivery.Call, messageID string, m delivery.Message) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	id, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil || id <= 0 {
		return delivery.Outcome{Kind: delivery.OutcomeUnknown,
			Error: outbound.Untrusted("the root message id " + strconv.Quote(messageID) + " is not a Telegram message id")}
	}
	if err := a.Targets.AwaitUpdates(ctx, t.ConnectionID); err != nil {
		return delivery.Outcome{Kind: delivery.OutcomeTransient,
			Error: "the presses of the connection could not be awaited"}
	}
	out := a.message(c, writer{plain: c.Plain}, m, "")
	out.ChatID, out.MessageID = chatRef(t.channel()), id
	_, r := t.Client.editMessageText(ctx, c.Class, out)
	o := a.outcome(t, c, r)
	if o.Kind == delivery.OutcomeOK {
		o.MessageID = messageID
	}
	return o
}

// Reply sends m into the Thread of root in the discussion group (C-14.FR-3): a reply to the post's automatic copy while
// the Thread is attached, which makes it a comment under the post (F-007); a reply to the last link of an unattached
// chain; or, as the first link of a chain, no reply at all. Muster never replies inside the channel (F-015). The
// Mentions of a Loud call come first, and only a Loud one carries them (C-12.FR-8); a Quiet one carries
// disable_notification (C-14.FR-6, F-012). A reply to a copy or link that is gone is refused, as a lost Thread (F-008).
func (a *Adapter) Reply(ctx context.Context, c delivery.Call, root delivery.Root, m delivery.Message) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	if t.GroupID == 0 {
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: errNoGroup}
	}
	to := root.ThreadAnchorID
	if to == "" {
		to = root.ChainLastID
	}
	var reply *replyParameters
	if to != "" {
		id, err := strconv.ParseInt(to, 10, 64)
		if err != nil || id <= 0 {
			return delivery.Outcome{Kind: delivery.OutcomeUnknown,
				Error: outbound.Untrusted("the thread message id " + strconv.Quote(to) + " is not a Telegram message id")}
		}
		reply = &replyParameters{MessageID: id}
	}
	w := writer{plain: c.Plain}
	mentioned := ""
	if c.Loudness == groups.Loud {
		mentioned = mentionText(c.Targets, w)
	}
	out := a.message(c, w, m, mentioned)
	out.ChatID, out.DisableNotification, out.ReplyParameters = t.GroupID, c.Loudness != groups.Loud, reply
	if len(out.ReplyMarkup.InlineKeyboard) == 0 {
		out.ReplyMarkup = nil
	}
	sent, r := t.Client.sendMessage(ctx, c.Class, out)
	return a.sent(t, c, r, sent)
}

// Check runs the Destination check of C-14.FR-14 at once, in the client class of c: the Broken probe calls it in the
// delivery class. A check that finds another channel or discussion group than the stored ones fails with
// MessageMoved, so that the probe does not end a Broken state that the next send would start again.
func (a *Adapter) Check(ctx context.Context, c delivery.Call) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	res, _ := CheckDestination(ctx, t.Client, Direct(c), t.Channel) // Direct never refuses a request
	if res.Moved(t.ChannelID, t.GroupID) {
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: MessageMoved}
	}
	return res.Outcome
}

// message is m laid out for the call: HTML, or plain text after the messenger rejected the markup, within
// LengthLimit, with its keyboard.
func (a *Adapter) message(c delivery.Call, w writer, m delivery.Message, mentioned string) outgoing {
	out := outgoing{Text: fit(m, w, mentioned, LengthLimit), ReplyMarkup: keyboard(m.Buttons),
		LinkPreview: linkPreview{IsDisabled: true}}
	if !c.Plain {
		out.ParseMode = "HTML"
	}
	return out
}

// sent is the outcome of a new message: an ok one names the message and its link; a success without a message id is
// unknown.
func (a *Adapter) sent(t Target, c delivery.Call, r Result, m sentMessage) delivery.Outcome {
	o := a.outcome(t, c, r)
	switch {
	case o.Kind != delivery.OutcomeOK:
		return o
	case m.MessageID == 0:
		return delivery.Outcome{Kind: delivery.OutcomeUnknown, Error: "the message was answered without its id"}
	}
	o.MessageID, o.MessageURL = strconv.FormatInt(m.MessageID, 10), permalink(m.Chat, m.MessageID)
	return o
}

// outcome classifies the answer of a send or an edit; a 429 holds the whole Connection when another of its
// Destinations got one within ConnectionWindow.
func (a *Adapter) outcome(t Target, c delivery.Call, r Result) delivery.Outcome {
	o := classify(r)
	if o.Kind == delivery.OutcomeRetryAfter {
		o.Scope = a.limits.scope(a.Clock, t.ConnectionID, c.Destination.ID)
	}
	return o
}

// target is the Target of the call's Destination, or the outcome of a call that cannot be made: Fatal without a
// Connection, Transient when it could not be read.
func (a *Adapter) target(ctx context.Context, c delivery.Call) (Target, *delivery.Outcome) {
	t, err := a.Targets.TelegramTarget(ctx, c.Destination.ID)
	switch {
	case errors.Is(err, ErrNoTarget):
		return Target{}, &delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(ErrNoTarget.Error())}
	case err != nil:
		return Target{}, &delivery.Outcome{Kind: delivery.OutcomeTransient,
			Error: "the connection of the destination could not be read"}
	}
	return t, nil
}

// permalink is the link to the message id in the chat: by the username of a public chat, otherwise by the chat's id
// without its -100 prefix, which opens for its members.
func permalink(chat Chat, id int64) string {
	if chat.Username != "" {
		return "https://t.me/" + chat.Username + "/" + strconv.FormatInt(id, 10)
	}
	if inner, ok := strings.CutPrefix(strconv.FormatInt(chat.ID, 10), "-100"); ok && inner != "" {
		return "https://t.me/c/" + inner + "/" + strconv.FormatInt(id, 10)
	}
	return ""
}
