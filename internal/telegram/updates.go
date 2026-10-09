// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/logging"
)

// The kinds of updates the router hands on (C-14.FR-1, FR-8).
const (
	// KindCallbackQuery is a button press.
	KindCallbackQuery = "callback_query"
	// KindChatMessage is a message, an edit or a post in a channel or a group, such as the automatic copy of a channel
	// post in its discussion group, which Copies processes.
	KindChatMessage = "chat_message"
	// KindPrivateMessage is a message to the bot in a private chat, such as `/start <token>`, which the Account link
	// handler of S-051 processes.
	KindPrivateMessage = "private_message"
	// KindMyChatMember is a change of the bot's own membership in a chat.
	KindMyChatMember = "my_chat_member"
	// KindOther is any other update.
	KindOther = "other"
)

// Chat is the chat of a message.
type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

// Message is a message or a channel post, with the fields the router reads; handlers read the rest from Raw.
type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	From      *User  `json:"from,omitempty"`
	Text      string `json:"text,omitempty"`
}

// CallbackQuery is a button press.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// Update is a Bot API Update: its id, the parts the router reads, and the whole update as it came, for handlers.
type Update struct {
	UpdateID      int64           `json:"update_id"`
	Message       *Message        `json:"message,omitempty"`
	EditedMessage *Message        `json:"edited_message,omitempty"`
	ChannelPost   *Message        `json:"channel_post,omitempty"`
	CallbackQuery *CallbackQuery  `json:"callback_query,omitempty"`
	MyChatMember  json.RawMessage `json:"my_chat_member,omitempty"`
	Raw           json.RawMessage `json:"-"`
	// Gap is how long the Connection had received no updates before the poll or the webhook request that brought this
	// one, as far as this replica knows (C-14.FR-4): a press after a gap longer than telegram.press_max_age is dropped.
	Gap time.Duration `json:"-"`
}

// errNoUpdateID is an update without update_id.
var errNoUpdateID = errors.New("the update has no update_id")

// ParseUpdate reads an update; it must carry an integer update_id. Unknown fields are kept in Raw only.
func ParseUpdate(b []byte) (Update, error) {
	var head struct {
		UpdateID *int64 `json:"update_id"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return Update{}, fmt.Errorf("read the update: %w", err)
	}
	if head.UpdateID == nil {
		return Update{}, errNoUpdateID
	}
	var u Update
	if err := json.Unmarshal(b, &u); err != nil {
		return Update{}, fmt.Errorf("read the update: %w", err)
	}
	u.Raw = append(json.RawMessage(nil), b...)
	return u, nil
}

// Kind is what the update is, for the router: a press, a message of a channel or group, a private message, a change
// of the bot's membership, or other.
func (u Update) Kind() string {
	switch {
	case u.CallbackQuery != nil:
		return KindCallbackQuery
	case u.ChannelPost != nil:
		return KindChatMessage
	case len(u.MyChatMember) > 0:
		return KindMyChatMember
	}
	m := u.Message
	if m == nil {
		m = u.EditedMessage
	}
	switch {
	case m == nil:
		return KindOther
	case m.Chat.Type == "private":
		return KindPrivateMessage
	case m.Chat.Type == "channel" || m.Chat.Type == "group" || m.Chat.Type == "supergroup":
		return KindChatMessage
	}
	return KindOther
}

// Conn is a Telegram Connection as the update path knows it: its ids and its client.
type Conn struct {
	ID       int64
	PublicID string
	Client   *Client
}

// Handler processes the updates of one kind. An error leaves the update unconfirmed, so that it comes again: with long
// polling at the next poll, with a webhook when Telegram retries it. A handler runs under the Connection's update lock
// — a transaction advisory lock keyed by the Connection, not its connections row — so it may run while a save of the
// Connection holds that row; it must be short, since the next update of the Connection and every edit of its Root
// messages wait for it.
type Handler func(ctx context.Context, c Conn, u Update) error

// Offsets keep the offset of each Connection's updates, connections.telegram_update_offset: the id after the last
// update handed to the router. Both update modes share it, so that an update is handled once.
type Offsets interface {
	// HandleOnce runs f for the update updateID of the Connection id unless the stored offset is past it, holding the
	// Connection's update lock meanwhile, so that a second poller or a webhook request with the same update waits and
	// then skips it; once f succeeded it raises the offset to updateID+1. It reports whether f ran. An error of f
	// stores nothing.
	HandleOnce(ctx context.Context, id, updateID int64, f func(ctx context.Context) error) (bool, error)
}

// Router is the one entry of updates for both modes (C-14.FR-1): it ignores an update the Connection has already
// handled, hands each kind to the handler registered for it — the messages of channels and groups to Copies, the
// presses and private messages to theirs — and drops a kind without a handler with telegram_update_dropped; then it
// stores the offset after the update.
type Router struct {
	Offsets Offsets
	Log     *logging.Logger

	mu       sync.RWMutex
	handlers map[string]Handler
}

// Handle registers h for the updates of kind, replacing an earlier handler.
func (r *Router) Handle(kind string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handlers == nil {
		r.handlers = map[string]Handler{}
	}
	r.handlers[kind] = h
}

func (r *Router) handler(kind string) Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handlers[kind]
}

// Route hands u of the Connection c on and reports whether it was new. An update before the stored offset was handled
// already and changes nothing. A handler's error stores no offset, so that the update comes again.
func (r *Router) Route(ctx context.Context, c Conn, u Update) (bool, error) {
	kind := u.Kind()
	ran, err := r.Offsets.HandleOnce(ctx, c.ID, u.UpdateID, func(ctx context.Context) error {
		if h := r.handler(kind); h != nil {
			return h(ctx, c, u)
		}
		r.Log.Log(ctx, logging.TelegramUpdateDropped, logging.F("connection", c.PublicID), logging.F("kind", kind))
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("route the %s update %d of the connection %s: %w", kind, u.UpdateID, c.PublicID, err)
	}
	return ran, nil
}
