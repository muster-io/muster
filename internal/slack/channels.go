// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/outbound"
)

// ItemChannel is the one item of the Destination check of a Slack Destination (C-24.FR-3): conversations.info.
const ItemChannel = "channel"

// The codes of the item channel besides those of the Connection check (DestinationCheckItem.code), which clients
// translate.
const (
	CodeChannelNotFound = "channel_not_found"
	CodeChannelArchived = "channel_archived"
	CodeBotNotInChannel = "bot_not_in_channel"
)

// The messages of the item channel that failed, in English (reference.md).
const (
	MessageChannelNotFound  = "The channel was not found, or the bot cannot see it."
	MessageChannelArchived  = "The channel is archived."
	messageInviteBot        = "Invite the bot to the channel: /invite @%s"
	MessageInviteBotUnnamed = "Invite the bot to the channel."
	messageScopesUnnamed    = "The bot token lacks scopes that Muster needs."
)

// The channel list reads conversations.list in pages of channelPageLimit, at most maxChannelPages of them: the method
// has the low rate limit of tier 2 (F-099).
const (
	channelPageLimit = 200
	maxChannelPages  = 5
)

// channelID is the id of a channel a Slack Destination posts to: C… for a public or private channel, G… for a private
// channel of an older workspace, in upper-case letters and digits (C-24.FR-3). A direct chat's D… and a user's U… are
// not channels: Slack would deliver a message to them as a direct message (F-096).
var channelID = regexp.MustCompile(`^[CG][A-Z0-9]{2,63}$`)

// ValidChannelID reports whether id names a channel a Slack Destination can post to, as entered: no lower case, no #.
func ValidChannelID(id string) bool { return channelID.MatchString(id) }

// Channel is a channel as conversations.info and conversations.list answer it (F-098): its id and name, masked, whether
// the bot is a member, whether it is archived and whether it is private.
type Channel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsMember   bool   `json:"is_member"`
	IsArchived bool   `json:"is_archived"`
	IsPrivate  bool   `json:"is_private"`
	IsChannel  bool   `json:"is_channel"`
}

// masked is ch with its id and name masked.
func (c *Client) masked(ch Channel) Channel {
	ch.ID, ch.Name = c.mask(ch.ID), c.mask(ch.Name)
	return ch
}

// ConversationsInfo calls conversations.info with the bot token for the channel channelID (scopes channels:read and
// groups:read): the channel, masked, which answers even when the bot is not a member of it.
func (c *Client) ConversationsInfo(ctx context.Context, class outbound.Class, channelID string) (Channel, Result) {
	var out struct {
		Channel *Channel `json:"channel"`
	}
	r := c.Call(ctx, class, "conversations.info", Args{"channel": channelID})
	if !r.OK() {
		return Channel{}, r
	}
	if r.Decode(&out) != nil || out.Channel == nil {
		return Channel{}, notExpected(r, "conversations.info")
	}
	return c.masked(*out.Channel), r
}

// ConversationsList calls conversations.list with the bot token for one page of the public and private channels the
// bot sees that are not archived, from cursor, empty for the first page: the channels, masked; the cursor of the next
// page is the Result's NextCursor, empty after the last one.
func (c *Client) ConversationsList(ctx context.Context, class outbound.Class, cursor string) ([]Channel, Result) {
	var out struct {
		Channels *[]Channel `json:"channels"`
	}
	args := Args{"types": "public_channel,private_channel", "exclude_archived": true, "limit": channelPageLimit}
	if cursor != "" {
		args["cursor"] = cursor
	}
	r := c.Call(ctx, class, "conversations.list", args)
	if !r.OK() {
		return nil, r
	}
	if r.Decode(&out) != nil || out.Channels == nil {
		return nil, notExpected(r, "conversations.list")
	}
	chans := make([]Channel, 0, len(*out.Channels))
	for _, ch := range *out.Channels {
		chans = append(chans, c.masked(ch))
	}
	return chans, r
}

// ChannelCheck is the result of the Destination check of a channel: the channel's name when it passed; otherwise the
// message, in English, with its code and, for missing_scope, the scopes Slack named as needed; and the outcome for
// delivery — ok, or the failure as Result.Outcome classifies it, with the message as its error — which the Broken probe
// keeps.
type ChannelCheck struct {
	Name          string
	Message       string
	Code          string
	MissingScopes []string
	Outcome       delivery.Outcome
}

// OK reports whether the check passed.
func (c ChannelCheck) OK() bool { return c.Outcome.Kind == delivery.OutcomeOK }

// CheckChannel runs the Destination check of the channel channelID through run (C-24.FR-3): one conversations.info,
// which posts nothing, in the class of the Call run makes it in — the interactive path for a save and for "Check", the
// background class for muster doctor, the delivery class for the Broken probe. An archived channel is
// channel_archived; one the bot is not a member of is bot_not_in_channel with the text that names botName, the bot's
// handle, or without a name when it is empty; a token Slack refuses is token_invalid, a missing scope missing_scope
// with the scopes of `needed`, an unknown channel, a deleted one or a private one without the bot channel_not_found;
// a request without an answer, or one the outbound address policy refuses, has the code of delivery.FailureCode with
// the text of delivery.FailureMessage; any other answer is request_failed with Slack's masked error. Its error is the
// runner's own.
func CheckChannel(ctx context.Context, c *Client, run Runner, channelID, botName string) (ChannelCheck, error) {
	var ch Channel
	var r Result
	if _, err := run(ctx, func(ctx context.Context, call delivery.Call) delivery.Outcome {
		ch, r = c.ConversationsInfo(ctx, call.Class, channelID)
		return r.Outcome()
	}); err != nil {
		return ChannelCheck{}, err
	}
	out := ChannelCheck{Outcome: r.Outcome()}
	switch {
	case r.OK() && ch.IsArchived:
		out.Code, out.Message = CodeChannelArchived, MessageChannelArchived
		out.Outcome = delivery.Outcome{Kind: delivery.OutcomeFatal, Status: r.Status}
	case r.OK() && !ch.IsMember:
		out.Code, out.Message = CodeBotNotInChannel, MessageInviteBotUnnamed
		if botName != "" {
			out.Message = c.mask(fmt.Sprintf(messageInviteBot, botName))
		}
		out.Outcome = delivery.Outcome{Kind: delivery.OutcomeFatal, Status: r.Status}
	case r.OK():
		out.Name = ch.Name
		return out, nil
	case slices.Contains(tokenErrors, r.ErrorCode):
		out.Code, out.Message = CodeTokenInvalid, MessageTokenInvalid
	case r.ErrorCode == "missing_scope":
		out.Code, out.Message, out.MissingScopes = CodeMissingScope, messageScopesUnnamed, r.Needed
		if len(r.Needed) > 0 {
			out.Message = MissingScopesMessage(r.Needed)
		}
	case r.ErrorCode == "channel_not_found":
		out.Code, out.Message = CodeChannelNotFound, MessageChannelNotFound
	default:
		out.Code, out.Message = failure(r, addressOf(c))
	}
	out.Outcome.Error = outbound.Untrusted(out.Message)
	return out, nil
}

// addressOf is the address a check names when Slack could not be reached: Slack's, or the Route's, and none through a
// proxy, whose own name is the one that did not resolve.
func addressOf(c *Client) string {
	if c.Via() == ViaProxy {
		return ""
	}
	return c.Base()
}

// ListedChannel is a channel of the channel list: its id, its name and whether it is private.
type ListedChannel struct {
	ID      string
	Name    string
	Private bool
}

// ChannelList is the channel list of a Slack Connection: its channels by name, and whether channels were left after
// the last page it read.
type ChannelList struct {
	Channels  []ListedChannel
	Truncated bool
}

// RateLimitedError is a page of the channel list that Slack answered 429: the list stops, and the caller answers that
// it can be asked again after RetryAfter, zero when Slack named no wait.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("slack rate-limited the channel list for %s", e.RetryAfter)
}

// ListFailedError is a page of the channel list that failed otherwise: Text is what answered, masked and untrusted.
type ListFailedError struct {
	Text outbound.Untrusted
}

func (e *ListFailedError) Error() string { return "the channel list failed: " + string(e.Text) }

// Channels lists the channels of C-24.FR-3 through run: conversations.list of the public and private channels that are
// not archived, page by page, at most maxChannelPages pages, keeping those the bot is a member of whose name contains
// q, regardless of case, by name; Truncated when a page was left. A page answered 429 stops the list at once with a
// *RateLimitedError and waits for nothing: the interactive path holds the Connection's limiter for its Retry-After. A
// page that failed otherwise is a *ListFailedError. Its other errors are the runner's own.
func Channels(ctx context.Context, c *Client, run Runner, q string) (ChannelList, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	out := ChannelList{Channels: []ListedChannel{}}
	cursor := ""
	for page := 0; ; page++ {
		if page == maxChannelPages {
			out.Truncated = true
			break
		}
		var chans []Channel
		var r Result
		if _, err := run(ctx, func(ctx context.Context, call delivery.Call) delivery.Outcome {
			chans, r = c.ConversationsList(ctx, call.Class, cursor)
			return r.Outcome()
		}); err != nil {
			return ChannelList{}, err
		}
		switch {
		case r.Status == http.StatusTooManyRequests:
			return ChannelList{}, &RateLimitedError{RetryAfter: r.Classification.RetryAfter}
		case !r.OK():
			return ChannelList{}, &ListFailedError{Text: outbound.Untrusted(cmp.Or(string(r.Error), checkFailed))}
		}
		for _, ch := range chans {
			if ch.IsMember && !ch.IsArchived && strings.Contains(strings.ToLower(ch.Name), q) {
				out.Channels = append(out.Channels, ListedChannel{ID: ch.ID, Name: ch.Name, Private: ch.IsPrivate})
			}
		}
		if r.NextCursor == "" {
			break
		}
		cursor = r.NextCursor
	}
	slices.SortStableFunc(out.Channels, func(a, b ListedChannel) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return out, nil
}
