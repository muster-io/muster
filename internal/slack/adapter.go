// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"context"
	"errors"
	"strings"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// ErrNoTarget is a Destination that has no Slack Connection to post through: it, or its Connection, no longer exists,
// or it is not a Slack Destination.
var ErrNoTarget = errors.New("the destination has no slack connection")

// The errors of the outcomes the adapter decides itself (C-24.FR-8).
const (
	errorNoTS            = "chat.postMessage answered no ts"
	errorRootDeleted     = "message_not_found: the root message was deleted in Slack"
	errorBotLeft         = "not_in_channel: the bot is no longer a member of the channel"
	errorChannelArchived = "is_archived: the channel is archived"
	errorMisplaced       = "the thread's root message is gone; the reply that landed in the channel was deleted"
	errorMisplacedKept   = "the thread's root message is gone; the reply that landed in the channel could not be " +
		"deleted: "
	errorNoConnection = "the connection of the destination could not be read"
)

// Target is where a Slack Destination posts: the client of its Connection, the Connection's id and public_id, the
// channel id of the Destination, the workspace's address that the Connection's last passing check stored — empty
// before one — which the link to a message starts with, and the bot's name that check stored, which the text of
// bot_not_in_channel names.
type Target struct {
	Client             *Client
	ConnectionID       int64
	ConnectionPublicID string
	ChannelID          string
	TeamURL            string
	BotName            string
}

// Targets find the Target of a Destination by its id, declared by their consumer; *connections.Service implements
// them. A Destination without one is ErrNoTarget.
type Targets interface {
	SlackTarget(ctx context.Context, destinationID int64) (Target, error)
}

// Adapter is the delivery adapter of Slack Destinations (C-24.FR-4 to FR-8): Publish, Update and Reply, called by the
// delivery worker and the interactive path only, in the client class of their Call, and Check, the Destination check
// of C-24.FR-3, which the Broken probe runs in the delivery class (C-24.FR-15). Version is the version the context of
// a Root message names. The message id is the Root message's `ts`.
type Adapter struct {
	Targets Targets
	Version string
}

var (
	_ delivery.Adapter = (*Adapter)(nil)
	_ delivery.Checker = (*Adapter)(nil)
)

// LengthLimit is the longest text of a section, which the body of a Root message is shortened to (F-076).
func (a *Adapter) LengthLimit() int { return LengthLimit }

// Markup is the markup of Slack messages.
func (a *Adapter) Markup() messages.Markup { return messages.MarkupMrkdwn }

// Publish posts the Root message m with chat.postMessage, with the Mentions of a Loud call in its `text` and top
// section; its link is that of the workspace the Connection's check stored.
func (a *Adapter) Publish(ctx context.Context, c delivery.Call, m delivery.Message) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	p, r := t.Client.postMessage(ctx, c.Class, layout(m, a.look(c, t), callMentions(c, m)))
	return published(t, r, p)
}

// Update edits the Root message messageID to m with chat.update, Quiet and without Mentions (C-24.FR-6). An edit
// answered message_not_found reads the channel in the same client class: the Root message was deleted when the bot is
// still a member of a channel that is not archived, and the answer is Fatal otherwise (C-24.FR-8).
func (a *Adapter) Update(ctx context.Context, c delivery.Call, messageID string, m delivery.Message) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	o := layout(m, a.look(c, t), nil)
	o.TS = messageID
	r := t.Client.updateMessage(ctx, c.Class, o)
	switch {
	case r.OK():
		return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: messageID,
			MessageURL: messageURL(t.TeamURL, t.ChannelID, messageID), Status: r.Status}
	case r.Classification.Class == ClassGoneCandidate:
		return rootDeleted(ctx, c, t, r)
	}
	return r.Outcome()
}

// Reply posts m into the Thread of root with chat.postMessage and its `thread_ts`, never broadcast (C-24.FR-6), with
// the Mentions of a Loud call. A reply that Slack posted into the channel instead — its root was deleted before any
// reply (F-091) — is deleted at once with chat.delete, and the Root message is gone (C-24.FR-8).
func (a *Adapter) Reply(ctx context.Context, c delivery.Call, root delivery.Root, m delivery.Message) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	o := layout(m, a.look(c, t), callMentions(c, m))
	o.ThreadTS = root.MessageID
	p, r := t.Client.postMessage(ctx, c.Class, o)
	switch {
	case !r.OK():
		return r.Outcome()
	case p.TS == "":
		return delivery.Outcome{Kind: delivery.OutcomeUnknown, Error: errorNoTS, Status: r.Status}
	case p.Message.ThreadTS == "":
		return misplaced(ctx, c, t, p.TS, r)
	}
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: p.TS, Status: r.Status}
}

// Check runs the Destination check of C-24.FR-3 at once, in the client class of c: one conversations.info, which
// posts nothing. The Broken probe calls it in the delivery class.
func (a *Adapter) Check(ctx context.Context, c delivery.Call) delivery.Outcome {
	t, fail := a.target(ctx, c)
	if fail != nil {
		return *fail
	}
	// The direct runner has no error of its own.
	res, _ := CheckChannel(ctx, t.Client, Direct(c), t.ChannelID, t.BotName)
	return res.Outcome
}

// target is the Target of the call's Destination, or the outcome of a call that cannot be made: Fatal without a
// Connection, Transient when it could not be read.
func (a *Adapter) target(ctx context.Context, c delivery.Call) (Target, *delivery.Outcome) {
	t, err := a.Targets.SlackTarget(ctx, c.Destination.ID)
	switch {
	case errors.Is(err, ErrNoTarget):
		return Target{}, &delivery.Outcome{Kind: delivery.OutcomeFatal, Error: outbound.Untrusted(ErrNoTarget.Error())}
	case err != nil:
		return Target{}, &delivery.Outcome{Kind: delivery.OutcomeTransient, Error: errorNoConnection}
	}
	return t, nil
}

// look is how the messages of the call are laid out in the Target's channel: the footer names users in the identity
// space of the Target's Connection.
func (a *Adapter) look(c delivery.Call, t Target) look {
	c.Destination.Connection = &t.ConnectionID
	return lookOf(c, t.ChannelID, a.Version)
}

// published is the outcome of a Publication: the Root message's `ts` and its link.
func published(t Target, r Result, p posted) delivery.Outcome {
	switch {
	case !r.OK():
		return r.Outcome()
	case p.TS == "":
		return delivery.Outcome{Kind: delivery.OutcomeUnknown, Error: errorNoTS, Status: r.Status}
	}
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: p.TS,
		MessageURL: messageURL(t.TeamURL, t.ChannelID, p.TS), Status: r.Status}
}

// rootDeleted is the outcome of an edit answered message_not_found (C-24.FR-8, F-087): conversations.info of the
// channel in the call's client class tells a deleted Root message — the bot a member of a channel that is not
// archived: Gone — from a channel the bot can no longer write to: Fatal. A read that fails has its own outcome.
func rootDeleted(ctx context.Context, c delivery.Call, t Target, r Result) delivery.Outcome {
	ch, read := t.Client.ConversationsInfo(ctx, c.Class, t.ChannelID)
	switch {
	case !read.OK():
		return read.Outcome()
	case ch.IsArchived:
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: errorChannelArchived, Status: read.Status}
	case !ch.IsMember:
		return delivery.Outcome{Kind: delivery.OutcomeFatal, Error: errorBotLeft, Status: read.Status}
	}
	return delivery.Outcome{Kind: delivery.OutcomeGone, Error: errorRootDeleted, Status: r.Status}
}

// misplaced deletes, in the call's client class, the Thread reply ts that Slack posted into the channel because its
// root was deleted before any reply (F-091), and is the Root message gone, with the masked error of a delete that
// failed.
func misplaced(ctx context.Context, c delivery.Call, t Target, ts string, r Result) delivery.Outcome {
	out := delivery.Outcome{Kind: delivery.OutcomeGone, Error: errorMisplaced, Status: r.Status}
	if d := t.Client.deleteMessage(ctx, c.Class, t.ChannelID, ts); !d.OK() {
		out.Error = errorMisplacedKept + d.Error
	}
	return out
}

// messageURL is the link to the message ts of channel in the workspace teamURL: `<workspace url>archives/<channel
// id>/p<ts without its dot>`, made without a request; none without the workspace's address — before the
// Connection's first passing check — or for an address that is not a safe link.
func messageURL(teamURL, channel, ts string) string {
	if teamURL == "" || ts == "" || channel == "" {
		return ""
	}
	return messages.SafeURL(strings.TrimSuffix(teamURL, "/") + "/archives/" + channel + "/p" +
		strings.ReplaceAll(ts, ".", ""))
}
