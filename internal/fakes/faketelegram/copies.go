// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package faketelegram

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// Comment Threads (C-01.FR-13): every channel post with a discussion group gets its automatic copy there, copy_delay_ms
// after the post, from Telegram (777000) on behalf of the channel, with is_automatic_forward, forward_origin naming the
// post, edit_date equal to its date and no keyboard (F-005). Only a bot that is an admin of the group receives it as a
// message update (F-003): the bot that posted, and every other bot that is an admin there and receives updates (it
// called getUpdates or set a webhook), since the copy comes from Telegram, not from a bot; withhold_copies keeps it
// back from every bot and withhold_copies_from from the bots it names. An edit of the post edits the copy and sends an
// edited_message for it at once to the same bots (F-006), and a person's comment reaches them too; a bot's own messages
// reach no other bot, as bots do not see each other's messages. A reply to the copy is a comment in its Thread (F-007),
// and a reply to a deleted copy fails (F-008). The copy notifies member with sound, even for a Quiet post (F-013); a
// reply in a Thread notifies member only (F-014); a reply inside the channel is a new post with a copy of its own
// (F-015).

// TelegramUserID is the sender of automatic copies: Telegram itself.
const TelegramUserID int64 = 777000

// The descriptions of the failures of the control endpoints of comment Threads.
const (
	descriptionNoGroup = "the channel has no discussion group"
	descriptionNoCopy  = "the post has no automatic copy yet"
)

// Sender is the sender of a message.
type Sender struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// ChatRef is a chat as a message names it.
type ChatRef struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username,omitempty"`
}

// ForwardOrigin is the origin of a forwarded message: for an automatic copy, the channel and the post.
type ForwardOrigin struct {
	Type      string  `json:"type"`
	Chat      ChatRef `json:"chat"`
	MessageID int64   `json:"message_id"`
	Date      int64   `json:"date"`
}

// telegramSender is the sender of automatic copies.
var telegramSender = Sender{ID: TelegramUserID, FirstName: "Telegram"}

func refOf(ch *Chat) ChatRef {
	return ChatRef{ID: ch.ID, Type: ch.Type, Title: ch.Title, Username: ch.Username}
}

// copyLater makes the automatic copy of a channel post after the copy delay; with none, at once.
func (f *Fake) copyLater(ctx context.Context, post *Message) {
	cfg := f.Config()
	ctx = context.WithoutCancel(ctx)
	if cfg.CopyDelayMS == 0 {
		f.makeCopy(ctx, post)
		return
	}
	time.AfterFunc(time.Duration(cfg.CopyDelayMS)*time.Millisecond, func() { f.makeCopy(ctx, post) })
}

// makeCopy adds the automatic copy of a channel post to its discussion group and delivers it to the bots that receive
// it. A post deleted meanwhile, or of a channel without comments, gets none.
func (f *Fake) makeCopy(ctx context.Context, post *Message) {
	cfg := f.Config()
	others := f.receivers()
	c := f.chats
	c.mu.Lock()
	update := c.addCopy(post, cfg.WithholdCopies, cfg.WithholdCopiesFrom, others)
	c.mu.Unlock()
	if update != nil {
		for _, t := range update.tokens {
			f.send(ctx, t, "message", update.update)
		}
	}
}

// receivers are the tokens that receive updates — those that called getUpdates or set a webhook — and are not
// revoked, in order; f.mu is taken.
func (f *Fake) receivers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for token, b := range f.bots {
		if b.receiving && !slices.Contains(f.config.RevokedTokens, token) {
			out = append(out, token)
		}
	}
	slices.Sort(out)
	return out
}

// copyRecipients are the tokens that receive an update about the copy cp of a post sent by poster: poster, and every
// token of others whose bot is not poster's; each only when its bot is an admin of the group and the copy is not
// withheld from it. A comment reaches poster in any case and is never withheld. c.mu is held.
func (c *chats) copyRecipients(cp *Message, poster string, others []string, comment bool) []string {
	gets := func(token string) bool {
		bot := botOf(token).ID
		if !comment && (cp.withheld || slices.Contains(cp.withheldFrom, bot)) {
			return false
		}
		return c.receivesCopies(cp.Chat, bot)
	}
	out := []string{}
	if poster != "" && (comment || gets(poster)) {
		out = append(out, poster)
	}
	for _, t := range others {
		if t != poster && botOf(t).ID != botOf(poster).ID && gets(t) {
			out = append(out, t)
		}
	}
	return out
}

// addCopy adds the copy of post and returns its update and the tokens that receive it, nil when there is no copy;
// c.mu is held.
func (c *chats) addCopy(post *Message, withhold bool, withheldFrom []int64, others []string) *editedCopy {
	channel := c.byID[post.Chat]
	if channel == nil || c.find(channel.ID, post.ID) == nil || post.copyID != 0 {
		return nil
	}
	group := c.byID[channel.LinkedChatID]
	if group == nil {
		return nil
	}
	now := c.now().Unix()
	m := &Message{Chat: group.ID, Date: now, EditDate: now, From: &telegramSender, SenderChat: new(refOf(channel)),
		IsAutomaticForward: true, ForwardOrigin: &ForwardOrigin{Type: "channel", Chat: refOf(channel),
			MessageID: post.ID, Date: post.Date},
		Text: post.Text, ParseMode: post.ParseMode, Edits: []Edit{}, token: post.token, withheld: withhold,
		withheldFrom: slices.Clone(withheldFrom)}
	c.next[group.ID]++
	m.ID = c.next[group.ID]
	c.messages[group.ID] = append(c.messages[group.ID], m)
	post.copyID = m.ID
	c.notifications = append(c.notifications, Notification{Account: AccountMember, Chat: group.ID, MessageID: m.ID,
		Sound: true})
	return &editedCopy{tokens: c.copyRecipients(m, post.token, others, false), update: c.apiMessage(m, true)}
}

// receivesCopies reports whether the bot receives the automatic copies of the group: only as its admin (F-003); c.mu
// is held.
func (c *chats) receivesCopies(group, bot int64) bool {
	s := c.member(group, bot).Status
	return s == StatusAdministrator || s == StatusCreator
}

// editCopy mirrors the edit of a channel post onto its automatic copy and returns the edited_message for the bots that
// receive the copy, nil when there is no copy; c.mu is held.
func (c *chats) editCopy(ch *Chat, post *Message, now time.Time, others []string) *editedCopy {
	if ch.Type != TypeChannel || post.copyID == 0 {
		return nil
	}
	m := c.find(ch.LinkedChatID, post.copyID)
	if m == nil {
		return nil
	}
	m.Text, m.ParseMode, m.EditDate = post.Text, post.ParseMode, now.Unix()
	m.Edits = append(m.Edits, Edit{Date: now.Unix(), Text: post.Text, ParseMode: post.ParseMode})
	return &editedCopy{tokens: c.copyRecipients(m, m.token, others, false), update: c.apiMessage(m, true)}
}

// apiMessage is m as an update carries it, with the message it replies to when nested; c.mu is held.
func (c *chats) apiMessage(m *Message, nested bool) map[string]any {
	out := map[string]any{"message_id": m.ID, "date": m.Date, "chat": refOf(c.byID[m.Chat]), "text": m.Text}
	if m.From != nil {
		out["from"] = *m.From
	}
	if m.SenderChat != nil {
		out["sender_chat"] = *m.SenderChat
	}
	if m.IsAutomaticForward {
		out["is_automatic_forward"] = true
	}
	if m.ForwardOrigin != nil {
		out["forward_origin"] = *m.ForwardOrigin
	}
	if m.EditDate != 0 {
		out["edit_date"] = m.EditDate
	}
	if m.MessageThreadID != 0 {
		out["message_thread_id"] = m.MessageThreadID
	}
	if target := c.find(m.Chat, m.replyTo); nested && m.replyTo != 0 && target != nil {
		out["reply_to_message"] = c.apiMessage(target, false)
	}
	return out
}

// send queues the update {kind: message} for token; without a token nobody receives it.
func (f *Fake) send(ctx context.Context, token, kind string, message map[string]any) {
	if token == "" {
		return
	}
	raw, _ := json.Marshal(map[string]any{kind: message})
	_, _ = f.Enqueue(ctx, token, raw)
}

// Comment is a person's comment under a channel post, as POST /_fake/comment takes it: the post, its channel (the
// channel @muster_alerts when empty), the person and the text; Token alone receives the update instead of the bots that
// receive the copy's updates.
type Comment struct {
	PostID int64  `json:"post_id"`
	Chat   string `json:"chat"`
	From   Sender `json:"from"`
	Text   string `json:"text"`
	Token  string `json:"token"`
}

// handleCopies adds the control endpoints of comment Threads: comments and deleted messages.
func (f *Fake) handleCopies() {
	f.HandleControl("POST /_fake/comment", f.postComment)
	f.HandleControl("DELETE /_fake/chats/{chat}/messages/{id}", f.deleteMessage)
}

// postComment posts a person's comment under a channel post: a reply to its automatic copy in the discussion group,
// in the copy's Thread (F-007), delivered to the bot that posted even when the copy was withheld, and to every other
// admin bot of the group that receives updates. A deleted copy cannot be replied to (F-008).
func (f *Fake) postComment(w http.ResponseWriter, r *http.Request) {
	var cm Comment
	if err := decodeControl(w, r, &cm); err != nil {
		return
	}
	m, update, status, failure := f.chats.comment(cm, f.receivers())
	if failure != "" {
		fakeserver.WriteError(w, status, failure)
		return
	}
	for _, t := range update.tokens {
		f.send(r.Context(), t, "message", update.update)
	}
	fakeserver.WriteJSON(w, http.StatusOK, m)
}

// comment adds a comment and returns it, its update and the tokens that receive it, or the status and text of a
// failure; others are the tokens that receive updates; c.mu is taken.
func (c *chats) comment(cm Comment, others []string) (Message, editedCopy, int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	name := cm.Chat
	if name == "" {
		name = strconv.FormatInt(ChannelID, 10)
	}
	channel, ok := c.resolve(name)
	if !ok || channel.Type != TypeChannel {
		return Message{}, editedCopy{}, http.StatusNotFound, "no such channel"
	}
	post := c.find(channel.ID, cm.PostID)
	switch {
	case post == nil:
		return Message{}, editedCopy{}, http.StatusNotFound, "no such post"
	case c.byID[channel.LinkedChatID] == nil:
		return Message{}, editedCopy{}, http.StatusBadRequest, descriptionNoGroup
	case post.copyID == 0:
		return Message{}, editedCopy{}, http.StatusConflict, descriptionNoCopy
	}
	cp := c.find(channel.LinkedChatID, post.copyID)
	if cp == nil {
		return Message{}, editedCopy{}, http.StatusBadRequest, DescriptionReplyNotFound
	}
	from := cm.From
	if from.FirstName == "" {
		from.FirstName = "Commenter " + strconv.FormatInt(from.ID, 10)
	}
	now := c.now().Unix()
	m := &Message{Chat: cp.Chat, Date: now, From: &from, Text: cm.Text, Edits: []Edit{},
		ReplyParameters: json.RawMessage(`{"message_id":` + strconv.FormatInt(cp.ID, 10) + `}`),
		MessageThreadID: cp.ID, replyTo: cp.ID}
	c.next[cp.Chat]++
	m.ID = c.next[cp.Chat]
	c.messages[cp.Chat] = append(c.messages[cp.Chat], m)
	c.notifications = append(c.notifications, Notification{Account: AccountMember, Chat: cp.Chat, MessageID: m.ID,
		Sound: true})
	tokens := []string{cm.Token}
	if cm.Token == "" {
		tokens = c.copyRecipients(cp, post.token, others, true)
	}
	out := *m
	out.Edits = slices.Clone(m.Edits)
	return out, editedCopy{tokens: tokens, update: c.apiMessage(m, true)}, 0, ""
}

// deleteMessage deletes a message of a chat, as a person deleting the automatic copy of a post does: replies to it
// fail from then on (F-008).
func (f *Fake) deleteMessage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, "the message id is not an integer")
		return
	}
	c := f.chats
	c.mu.Lock()
	ch, ok := c.resolve(r.PathValue("chat"))
	c.mu.Unlock()
	if !ok || !f.DeleteMessage(ch.ID, id) {
		fakeserver.WriteError(w, http.StatusNotFound, "no such message")
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// DeleteMessage deletes the message id of the chat and reports whether it was there.
func (f *Fake) DeleteMessage(chat, id int64) bool {
	c := f.chats
	c.mu.Lock()
	defer c.mu.Unlock()
	before := len(c.messages[chat])
	c.messages[chat] = slices.DeleteFunc(c.messages[chat], func(m *Message) bool { return m.ID == id })
	return len(c.messages[chat]) < before
}

// SetCopyDelay sets how long after a channel post its automatic copy arrives; 0 makes it before the answer.
func (f *Fake) SetCopyDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.config.CopyDelayMS = max(d.Milliseconds(), 0)
}
