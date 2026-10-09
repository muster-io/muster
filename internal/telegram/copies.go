// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/muster-io/muster/internal/delivery"
)

// CopyLearner learns the automatic copy of a channel post, declared by its consumer; *delivery.Service implements it,
// since only delivery writes the copy buffer and the deliveries whose Threads the copy attaches.
type CopyLearner interface {
	LearnCopy(ctx context.Context, c delivery.Copy) error
}

// Copies is the handler of the messages of channels and groups (KindChatMessage) on the update router (C-14.FR-3): a
// group message with is_automatic_forward whose forward_origin is a channel is the automatic copy of the post
// forward_origin.message_id (F-005), and a group message that replies to such a copy is a person's comment, which
// teaches the same copy (F-007). The edit_date a copy carries from the start changes nothing, and neither does the
// edited_message Telegram sends when it mirrors an edit of the post onto the copy (F-005, F-006): a copy is never an
// edit. Every other message is ignored. It is one short transaction of delivery and touches no connections row, as the
// router asks of its handlers.
type Copies struct {
	Learner CopyLearner
}

// Handle learns the copy that u teaches, if any; a failure leaves the update to come again, but a copy that can never
// be learned is dropped.
func (h Copies) Handle(ctx context.Context, c Conn, u Update) error {
	cp, ok := copyOf(u)
	if !ok {
		return nil
	}
	cp.ConnectionID = c.ID
	switch err := h.Learner.LearnCopy(ctx, cp); {
	case errors.Is(err, delivery.ErrBadCopy):
		return nil // it would come again and again, holding back every later update of the Connection
	case err != nil:
		return fmt.Errorf("learn the copy of post %d: %w", cp.PostID, err)
	}
	return nil
}

// groupMessage is the part of a group message that teaches a copy.
type groupMessage struct {
	MessageID          int64          `json:"message_id"`
	Chat               Chat           `json:"chat"`
	IsAutomaticForward bool           `json:"is_automatic_forward"`
	ForwardOrigin      *forwardOrigin `json:"forward_origin"`
	ReplyToMessage     *groupMessage  `json:"reply_to_message"`
}

// forwardOrigin is the origin of a forwarded message; an automatic copy's is the channel and the post.
type forwardOrigin struct {
	Type      string `json:"type"`
	Chat      *Chat  `json:"chat"`
	MessageID int64  `json:"message_id"`
}

// copyOf is the copy a message update teaches: the automatic copy itself, or the copy a comment replies to. An
// edited_message, a channel post or any other message teaches none.
func copyOf(u Update) (delivery.Copy, bool) {
	if u.Message == nil {
		return delivery.Copy{}, false
	}
	var raw struct {
		Message *groupMessage `json:"message"`
	}
	if err := json.Unmarshal(u.Raw, &raw); err != nil || raw.Message == nil || raw.Message.Chat.ID == 0 ||
		(raw.Message.Chat.Type != "supergroup" && raw.Message.Chat.Type != "group") {
		return delivery.Copy{}, false
	}
	m := raw.Message
	if channel, post, ok := automaticCopy(m); ok {
		return delivery.Copy{ChannelChatID: channel, PostID: post, DiscussionChatID: m.Chat.ID, CopyID: m.MessageID,
			LearnedFrom: delivery.LearnedFromAutomaticForward}, true
	}
	if r := m.ReplyToMessage; r != nil && (r.Chat.ID == 0 || r.Chat.ID == m.Chat.ID) {
		if channel, post, ok := automaticCopy(r); ok {
			return delivery.Copy{ChannelChatID: channel, PostID: post, DiscussionChatID: m.Chat.ID, CopyID: r.MessageID,
				LearnedFrom: delivery.LearnedFromComment}, true
		}
	}
	return delivery.Copy{}, false
}

// automaticCopy is the channel and the post of an automatic copy, false for any other message.
func automaticCopy(m *groupMessage) (int64, int64, bool) {
	o := m.ForwardOrigin
	if !m.IsAutomaticForward || m.MessageID <= 0 || o == nil || o.Type != "channel" || o.Chat == nil || o.Chat.ID == 0 ||
		o.MessageID <= 0 {
		return 0, 0, false
	}
	return o.Chat.ID, o.MessageID, true
}
