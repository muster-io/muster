// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package faketelegram

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The chats of the fake (C-01.FR-13): the channel @muster_alerts with comments enabled and its discussion group, and the
// channel @no_comments without (F-002). The bot is an admin allowed to post and edit in all three.
const (
	ChannelID       int64 = -1001000000001
	ChannelUsername       = "muster_alerts"
	ChannelTitle          = "Muster alerts"
	GroupID         int64 = -1001000000002
	GroupTitle            = "Muster alerts Chat"
	NoCommentsID    int64 = -1001000000003
	NoCommentsName        = "no_comments"
	NoCommentsTitle       = "No comments"
)

// The types of chats.
const (
	TypeChannel    = "channel"
	TypeSupergroup = "supergroup"
)

// The statuses of a chat member.
const (
	StatusCreator       = "creator"
	StatusAdministrator = "administrator"
	StatusMember        = "member"
	StatusLeft          = "left"
	StatusKicked        = "kicked"
)

// The accounts whose notifications the fake records (F-012): member joined the discussion group and subscribed to the
// channel; subscriber only subscribed to the channel.
const (
	AccountMember     = "member"
	AccountSubscriber = "subscriber"
)

// ChatBudget is how many sends and edits a chat takes per ChatWindow before the next one answers 429 (F-016).
const (
	ChatBudget = 20
	ChatWindow = time.Minute
)

// MaxTextLength is the longest text of a message, in UTF-16 code units after entities are parsed.
const MaxTextLength = 4096

// The descriptions of the errors of chats and messages, as the Bot API words them.
const (
	DescriptionChatNotFound    = "Bad Request: chat not found"
	DescriptionNotModified     = "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message"
	DescriptionEditNotFound    = "Bad Request: message to edit not found"
	DescriptionReplyNotFound   = "Bad Request: message to be replied not found"
	DescriptionTextEmpty       = "Bad Request: message text is empty"
	DescriptionTooLong         = "Bad Request: message is too long"
	DescriptionNeedAdmin       = "Bad Request: need administrator rights in the channel chat"
	DescriptionButtonData      = "Bad Request: BUTTON_DATA_INVALID"
	descriptionNotMember       = "Forbidden: bot is not a member of the %s chat"
	descriptionKicked          = "Forbidden: bot was kicked from the %s chat"
	descriptionTooManyRequests = "Too Many Requests: retry after "
	descriptionParse           = "Bad Request: can't parse entities: "
)

// Chat is a chat as getChat answers it; LinkedChatID is set only for a channel with comments and for its discussion
// group (F-002).
type Chat struct {
	ID           int64  `json:"id"`
	Type         string `json:"type"`
	Title        string `json:"title"`
	Username     string `json:"username,omitempty"`
	LinkedChatID int64  `json:"linked_chat_id,omitempty"`
}

// Member is a member of a chat as getChatMember answers it, without its user.
type Member struct {
	Status          string `json:"status"`
	CanPostMessages bool   `json:"can_post_messages,omitempty"`
	CanEditMessages bool   `json:"can_edit_messages,omitempty"`
}

// Message is a message the bot sent, as GET /_fake/messages lists it: the text, parse mode and keyboard it has now,
// what it was sent with, and its edits.
type Message struct {
	ID                  int64           `json:"message_id"`
	Chat                int64           `json:"chat"`
	Date                int64           `json:"date"`
	Text                string          `json:"text"`
	ParseMode           string          `json:"parse_mode,omitempty"`
	ReplyMarkup         json.RawMessage `json:"reply_markup,omitempty"`
	ReplyParameters     json.RawMessage `json:"reply_parameters,omitempty"`
	MessageThreadID     int64           `json:"message_thread_id,omitempty"`
	DisableNotification bool            `json:"disable_notification,omitempty"`
	Edits               []Edit          `json:"edits"`
}

// Edit is one editMessageText of a message: the text, parse mode and keyboard it set — none removes the keyboard
// (F-011).
type Edit struct {
	Date        int64           `json:"date"`
	Text        string          `json:"text"`
	ParseMode   string          `json:"parse_mode,omitempty"`
	ReplyMarkup json.RawMessage `json:"reply_markup,omitempty"`
}

// Notification is what an account was notified of: a message in a chat, with or without sound (F-012).
type Notification struct {
	Account   string `json:"account"`
	Chat      int64  `json:"chat"`
	MessageID int64  `json:"message_id"`
	Sound     bool   `json:"sound"`
}

// chats is the state of the chats: shared by every token, as the chats of one Telegram are.
type chats struct {
	mu            sync.Mutex
	byID          map[int64]*Chat
	members       map[int64]map[int64]Member
	messages      map[int64][]*Message
	calls         map[int64][]time.Time
	notifications []Notification
	next          map[int64]int64
	now           func() time.Time
}

func newChats() *chats {
	c := &chats{byID: map[int64]*Chat{}, members: map[int64]map[int64]Member{}, messages: map[int64][]*Message{},
		calls: map[int64][]time.Time{}, next: map[int64]int64{}, now: time.Now}
	admin := Member{Status: StatusAdministrator, CanPostMessages: true, CanEditMessages: true}
	for _, ch := range []Chat{
		{ID: ChannelID, Type: TypeChannel, Title: ChannelTitle, Username: ChannelUsername, LinkedChatID: GroupID},
		{ID: GroupID, Type: TypeSupergroup, Title: GroupTitle, LinkedChatID: ChannelID},
		{ID: NoCommentsID, Type: TypeChannel, Title: NoCommentsTitle, Username: NoCommentsName},
	} {
		c.byID[ch.ID] = &ch
		c.members[ch.ID] = map[int64]Member{BotID: admin}
	}
	return c
}

// SetClock replaces the clock of the per-chat budget and of the message dates; nil is the system's.
func (f *Fake) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	f.chats.mu.Lock()
	defer f.chats.mu.Unlock()
	f.chats.now = now
}

// resolve is the chat named by an id or an @username; c.mu is held.
func (c *chats) resolve(name string) (*Chat, bool) {
	name = strings.TrimSpace(name)
	if u, ok := strings.CutPrefix(name, "@"); ok {
		for _, ch := range c.byID {
			if ch.Username != "" && strings.EqualFold(ch.Username, u) {
				return ch, true
			}
		}
		return nil, false
	}
	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil {
		return nil, false
	}
	ch, ok := c.byID[id]
	return ch, ok
}

// chatParam is the chat_id parameter as text: an integer or an @username.
func chatParam(p params) string {
	switch v := p["chat_id"].(type) {
	case json.Number:
		return v.String()
	case string:
		return v
	}
	return ""
}

// member is the bot's membership of the chat; c.mu is held.
func (c *chats) member(chat, user int64) Member {
	if m, ok := c.members[chat][user]; ok {
		return m
	}
	return Member{Status: StatusLeft}
}

// Chat is the chat id, false when the fake has none.
func (f *Fake) Chat(id int64) (Chat, bool) {
	f.chats.mu.Lock()
	defer f.chats.mu.Unlock()
	ch, ok := f.chats.byID[id]
	if !ok {
		return Chat{}, false
	}
	return *ch, true
}

// SetLinkedChat links the channel to the discussion group, or unlinks it with 0, as enabling or disabling comments
// does (F-002).
func (f *Fake) SetLinkedChat(channel, group int64) error {
	c := f.chats
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.byID[channel]
	if !ok {
		return errors.New("no such chat")
	}
	if old, ok := c.byID[ch.LinkedChatID]; ok && old.LinkedChatID == channel {
		old.LinkedChatID = 0
	}
	ch.LinkedChatID = group
	if g, ok := c.byID[group]; ok {
		if other, ok := c.byID[g.LinkedChatID]; ok && other.ID != channel {
			other.LinkedChatID = 0
		}
		g.LinkedChatID = channel
	}
	return nil
}

// SetMember sets the membership of the user in the chat.
func (f *Fake) SetMember(chat, user int64, m Member) error {
	c := f.chats
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byID[chat]; !ok {
		return errors.New("no such chat")
	}
	c.members[chat][user] = m
	return nil
}

// Messages are the messages the bot sent to the chat, 0 for every chat, in the order they were sent.
func (f *Fake) Messages(chat int64) []Message {
	c := f.chats
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []int64
	if chat != 0 {
		ids = []int64{chat}
	} else {
		for id := range c.messages {
			ids = append(ids, id)
		}
		slices.Sort(ids)
	}
	out := []Message{}
	for _, id := range ids {
		for _, m := range c.messages[id] {
			cp := *m
			cp.Edits = slices.Clone(m.Edits)
			out = append(out, cp)
		}
	}
	return out
}

// Notifications are the notifications of the accounts, in order.
func (f *Fake) Notifications() []Notification {
	f.chats.mu.Lock()
	defer f.chats.mu.Unlock()
	return slices.Clone(f.chats.notifications)
}

// handleChats adds the control endpoints of the chats.
func (f *Fake) handleChats() {
	f.HandleControl("GET /_fake/chats", func(w http.ResponseWriter, _ *http.Request) {
		c := f.chats
		c.mu.Lock()
		out := make([]Chat, 0, len(c.byID))
		for _, ch := range c.byID {
			out = append(out, *ch)
		}
		c.mu.Unlock()
		slices.SortFunc(out, func(a, b Chat) int { return int(b.ID - a.ID) })
		fakeserver.WriteJSON(w, http.StatusOK, out)
	})
	f.HandleControl("PUT /_fake/chats/{chat}", f.putChat)
	f.HandleControl("PUT /_fake/chats/{chat}/members/{user}", f.putMember)
	f.HandleControl("GET /_fake/messages", func(w http.ResponseWriter, r *http.Request) {
		var chat int64
		if q := r.URL.Query().Get("chat"); q != "" {
			f.chats.mu.Lock()
			ch, ok := f.chats.resolve(q)
			f.chats.mu.Unlock()
			if !ok {
				fakeserver.WriteError(w, http.StatusNotFound, "no such chat")
				return
			}
			chat = ch.ID
		}
		fakeserver.WriteJSON(w, http.StatusOK, f.Messages(chat))
	})
	f.HandleControl("GET /_fake/notifications", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Notifications())
	})
}

type chatPatch struct {
	LinkedChatID *int64 `json:"linked_chat_id"`
}

// putChat enables comments on a channel with {"linked_chat_id": <group>} and disables them with 0.
func (f *Fake) putChat(w http.ResponseWriter, r *http.Request) {
	var p chatPatch
	if err := decodeControl(w, r, &p); err != nil {
		return
	}
	f.chats.mu.Lock()
	ch, ok := f.chats.resolve(r.PathValue("chat"))
	f.chats.mu.Unlock()
	if !ok {
		fakeserver.WriteError(w, http.StatusNotFound, "no such chat")
		return
	}
	if p.LinkedChatID != nil {
		if err := f.SetLinkedChat(ch.ID, *p.LinkedChatID); err != nil {
			fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	got, _ := f.Chat(ch.ID)
	fakeserver.WriteJSON(w, http.StatusOK, got)
}

// putMember sets a membership: {"status", "can_post_messages", "can_edit_messages"}.
func (f *Fake) putMember(w http.ResponseWriter, r *http.Request) {
	var m Member
	if err := decodeControl(w, r, &m); err != nil {
		return
	}
	switch m.Status {
	case StatusCreator, StatusAdministrator, StatusMember, StatusLeft, StatusKicked:
	default:
		fakeserver.WriteError(w, http.StatusBadRequest, "status is creator, administrator, member, left or kicked")
		return
	}
	user, err := strconv.ParseInt(r.PathValue("user"), 10, 64)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, "the user is not an integer id")
		return
	}
	f.chats.mu.Lock()
	ch, ok := f.chats.resolve(r.PathValue("chat"))
	f.chats.mu.Unlock()
	if !ok {
		fakeserver.WriteError(w, http.StatusNotFound, "no such chat")
		return
	}
	_ = f.SetMember(ch.ID, user, m)
	fakeserver.WriteJSON(w, http.StatusOK, m)
}

// getChat answers the chat, with linked_chat_id only when comments are enabled (F-002).
func (f *Fake) getChat(w http.ResponseWriter, p params) {
	c := f.chats
	c.mu.Lock()
	ch, ok := c.resolve(chatParam(p))
	var out Chat
	if ok {
		out = *ch
	}
	c.mu.Unlock()
	if !ok {
		writeFailure(w, http.StatusBadRequest, DescriptionChatNotFound)
		return
	}
	writeOK(w, out)
}

// chatMember is the answer of getChatMember.
type chatMember struct {
	Member
	User User `json:"user"`
}

// getChatMember answers a membership. The bot outside a supergroup gets 403 (F-003); outside a channel it is left
// (F-001), which is also how a bot added to a channel without admin rights stays.
func (f *Fake) getChatMember(w http.ResponseWriter, p params) {
	user, ok, err := p.int("user_id")
	if err != nil || !ok {
		writeFailure(w, http.StatusBadRequest, "Bad Request: invalid user_id specified")
		return
	}
	c := f.chats
	c.mu.Lock()
	ch, found := c.resolve(chatParam(p))
	var m, bot Member
	var kind string
	if found {
		m, bot, kind = c.member(ch.ID, user), c.member(ch.ID, BotID), ch.Type
	}
	c.mu.Unlock()
	switch {
	case !found:
		writeFailure(w, http.StatusBadRequest, DescriptionChatNotFound)
		return
	case kind == TypeSupergroup && (bot.Status == StatusLeft || bot.Status == StatusKicked):
		writeFailure(w, http.StatusForbidden, strings.Replace(descriptionNotMember, "%s", kind, 1))
		return
	}
	u := User{ID: user}
	if user == BotID {
		u = User{ID: BotID, IsBot: true, FirstName: "Muster Dev", Username: BotUsername}
	}
	if m.Status != StatusAdministrator {
		m.CanPostMessages, m.CanEditMessages = false, false
	}
	writeOK(w, chatMember{Member: m, User: u})
}

// take counts one send or edit of the chat at now against its budget (F-016): the delay until a call fits again when
// the budget is spent, and 0 when the call is taken; c.mu is held.
func (c *chats) take(chat int64, now time.Time) time.Duration {
	calls := slices.DeleteFunc(c.calls[chat], func(t time.Time) bool { return now.Sub(t) >= ChatWindow })
	c.calls[chat] = calls
	if len(calls) >= ChatBudget {
		return calls[0].Add(ChatWindow).Sub(now)
	}
	c.calls[chat] = append(calls, now)
	return 0
}

// refusal is why the bot may not write to the chat, empty when it may; edit asks for the right to edit; c.mu is held.
func (c *chats) refusal(ch *Chat, edit bool) (int, string) {
	m := c.member(ch.ID, BotID)
	switch m.Status {
	case StatusLeft:
		return http.StatusForbidden, strings.Replace(descriptionNotMember, "%s", ch.Type, 1)
	case StatusKicked:
		return http.StatusForbidden, strings.Replace(descriptionKicked, "%s", ch.Type, 1)
	}
	if ch.Type != TypeChannel || m.Status == StatusCreator {
		return 0, ""
	}
	if m.Status != StatusAdministrator || (!edit && !m.CanPostMessages) || (edit && !m.CanEditMessages) {
		return http.StatusBadRequest, DescriptionNeedAdmin
	}
	return 0, ""
}

// tooMany answers 429 with the exact delay, rounded up to a second.
func tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int((wait + time.Second - 1) / time.Second)
	fakeserver.WriteJSON(w, http.StatusTooManyRequests, answer{ErrorCode: http.StatusTooManyRequests,
		Description: descriptionTooManyRequests + strconv.Itoa(secs), Parameters: &parameters{RetryAfter: secs}})
}

// content is the text, parse mode and keyboard of a send or an edit, checked as the Bot API checks them.
type content struct {
	text      string
	parseMode string
	markup    json.RawMessage
}

// contentOf reads and checks the content of a call: a text that is not empty, HTML that parses, at most MaxTextLength
// after entities, and buttons whose callback_data has at most 64 bytes. The failure is the description to answer.
func contentOf(p params) (content, string) {
	c := content{text: p.string("text"), parseMode: p.string("parse_mode")}
	if raw, ok := p["reply_markup"]; ok && raw != nil {
		b, err := markupJSON(raw)
		if err != nil {
			return content{}, "Bad Request: can't parse reply keyboard markup JSON object"
		}
		c.markup = b
	}
	visible := c.text
	if strings.EqualFold(c.parseMode, "HTML") {
		v, err := parseHTML(c.text)
		if err != nil {
			return content{}, descriptionParse + err.Error()
		}
		visible = v
	}
	switch {
	case strings.TrimSpace(visible) == "":
		return content{}, DescriptionTextEmpty
	case len(utf16.Encode([]rune(visible))) > MaxTextLength:
		return content{}, DescriptionTooLong
	}
	if c.markup != nil {
		var kb struct {
			InlineKeyboard [][]struct {
				Text         string `json:"text"`
				CallbackData string `json:"callback_data"`
			} `json:"inline_keyboard"`
		}
		if err := json.Unmarshal(c.markup, &kb); err != nil {
			return content{}, "Bad Request: can't parse reply keyboard markup JSON object"
		}
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if len(b.CallbackData) == 0 || len(b.CallbackData) > 64 {
					return content{}, DescriptionButtonData
				}
			}
		}
	}
	return c, ""
}

// markupJSON is reply_markup as compact JSON, given as an object or as its JSON text.
func markupJSON(v any) (json.RawMessage, error) {
	if s, ok := v.(string); ok {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, err
		}
		v = m
	}
	return json.Marshal(v)
}

// sentMessage is the answer of sendMessage and editMessageText.
type sentMessage struct {
	MessageID   int64           `json:"message_id"`
	Date        int64           `json:"date"`
	EditDate    int64           `json:"edit_date,omitempty"`
	Chat        Chat            `json:"chat"`
	Text        string          `json:"text"`
	ReplyMarkup json.RawMessage `json:"reply_markup,omitempty"`
}

// sendMessage sends a message to a chat within its budget, with notifications for the accounts in it: a channel post
// notifies member and subscriber, a group message member, without sound with disable_notification (F-012).
func (f *Fake) sendMessage(w http.ResponseWriter, p params) {
	c := f.chats
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.resolve(chatParam(p))
	if !ok {
		writeFailure(w, http.StatusBadRequest, DescriptionChatNotFound)
		return
	}
	now := c.now()
	if wait := c.take(ch.ID, now); wait > 0 {
		tooMany(w, wait)
		return
	}
	if status, d := c.refusal(ch, false); status != 0 {
		writeFailure(w, status, d)
		return
	}
	body, failure := contentOf(p)
	if failure != "" {
		writeFailure(w, http.StatusBadRequest, failure)
		return
	}
	m := &Message{Chat: ch.ID, Date: now.Unix(), Text: body.text, ParseMode: body.parseMode, ReplyMarkup: body.markup,
		DisableNotification: p.bool("disable_notification"), Edits: []Edit{}}
	if raw, ok := p["reply_parameters"]; ok && raw != nil {
		b, err := markupJSON(raw)
		if err != nil {
			writeFailure(w, http.StatusBadRequest, "Bad Request: can't parse reply parameters JSON object")
			return
		}
		var rp struct {
			MessageID int64 `json:"message_id"`
		}
		_ = json.Unmarshal(b, &rp)
		if c.find(ch.ID, rp.MessageID) == nil {
			writeFailure(w, http.StatusBadRequest, DescriptionReplyNotFound)
			return
		}
		m.ReplyParameters = b
	}
	if thread, ok, _ := p.int("message_thread_id"); ok {
		m.MessageThreadID = thread
	}
	c.next[ch.ID]++
	m.ID = c.next[ch.ID]
	c.messages[ch.ID] = append(c.messages[ch.ID], m)
	accounts := []string{AccountMember}
	if ch.Type == TypeChannel {
		accounts = append(accounts, AccountSubscriber)
	}
	for _, a := range accounts {
		c.notifications = append(c.notifications, Notification{Account: a, Chat: ch.ID, MessageID: m.ID,
			Sound: !m.DisableNotification})
	}
	writeOK(w, sentMessage{MessageID: m.ID, Date: m.Date, Chat: *ch, Text: body.text, ReplyMarkup: m.ReplyMarkup})
}

// find is the message id of the chat; c.mu is held.
func (c *chats) find(chat, id int64) *Message {
	for _, m := range c.messages[chat] {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// editMessageText edits a message within the chat's budget: the edit sets its keyboard, and one without reply_markup
// removes it (F-011); an edit that changes nothing fails with "message is not modified" (F-017). An edit notifies
// nobody (F-012).
func (f *Fake) editMessageText(w http.ResponseWriter, p params) {
	c := f.chats
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.resolve(chatParam(p))
	if !ok {
		writeFailure(w, http.StatusBadRequest, DescriptionChatNotFound)
		return
	}
	now := c.now()
	if wait := c.take(ch.ID, now); wait > 0 {
		tooMany(w, wait)
		return
	}
	if status, d := c.refusal(ch, true); status != 0 {
		writeFailure(w, status, d)
		return
	}
	id, _, err := p.int("message_id")
	m := c.find(ch.ID, id)
	if err != nil || m == nil {
		writeFailure(w, http.StatusBadRequest, DescriptionEditNotFound)
		return
	}
	body, failure := contentOf(p)
	if failure != "" {
		writeFailure(w, http.StatusBadRequest, failure)
		return
	}
	if body.text == m.Text && body.parseMode == m.ParseMode && jsonEqual(body.markup, m.ReplyMarkup) {
		writeFailure(w, http.StatusBadRequest, DescriptionNotModified)
		return
	}
	m.Text, m.ParseMode, m.ReplyMarkup = body.text, body.parseMode, body.markup
	m.Edits = append(m.Edits, Edit{Date: now.Unix(), Text: body.text, ParseMode: body.parseMode,
		ReplyMarkup: body.markup})
	writeOK(w, sentMessage{MessageID: m.ID, Date: m.Date, EditDate: now.Unix(), Chat: *ch, Text: body.text,
		ReplyMarkup: m.ReplyMarkup})
}

// jsonEqual reports whether two keyboards are the same, an empty one being the same as none.
func jsonEqual(a, b json.RawMessage) bool {
	norm := func(r json.RawMessage) string {
		var v struct {
			InlineKeyboard [][]map[string]any `json:"inline_keyboard"`
		}
		if len(r) == 0 || json.Unmarshal(r, &v) != nil || len(v.InlineKeyboard) == 0 {
			return ""
		}
		out, _ := json.Marshal(v)
		return string(out)
	}
	return norm(a) == norm(b)
}

// The tags of Telegram HTML.
var htmlTags = []string{"b", "strong", "i", "em", "u", "ins", "s", "strike", "del", "span", "tg-spoiler", "a",
	"tg-emoji", "code", "pre", "blockquote"}

// parseHTML checks text as Telegram HTML — known tags, closed in order, and the entities &lt; &gt; &amp; &quot; or
// numeric ones — and returns its visible text.
func parseHTML(text string) (string, error) {
	var out strings.Builder
	var open []string
	for i := 0; i < len(text); {
		switch text[i] {
		case '<':
			end := strings.IndexByte(text[i:], '>')
			if end < 0 {
				return "", errors.New("unclosed start tag at byte offset " + strconv.Itoa(i))
			}
			tag := text[i+1 : i+end]
			closing := strings.HasPrefix(tag, "/")
			name, _, _ := strings.Cut(strings.TrimPrefix(tag, "/"), " ")
			name = strings.ToLower(name)
			if !slices.Contains(htmlTags, name) {
				return "", errors.New("unsupported start tag \"" + name + "\" at byte offset " + strconv.Itoa(i))
			}
			if closing {
				if len(open) == 0 || open[len(open)-1] != name {
					return "", errors.New("unmatched end tag at byte offset " + strconv.Itoa(i) +
						", expected \"</" + last(open) + ">\", found \"</" + name + ">\"")
				}
				open = open[:len(open)-1]
			} else {
				open = append(open, name)
			}
			i += end + 1
		case '>':
			return "", errors.New("character '>' is reserved at byte offset " + strconv.Itoa(i))
		case '&':
			end := strings.IndexByte(text[i:], ';')
			if end < 0 {
				return "", errors.New("unsupported entity at byte offset " + strconv.Itoa(i))
			}
			r, ok := entity(text[i+1 : i+end])
			if !ok {
				return "", errors.New("unsupported entity at byte offset " + strconv.Itoa(i))
			}
			out.WriteRune(r)
			i += end + 1
		default:
			out.WriteByte(text[i])
			i++
		}
	}
	if len(open) > 0 {
		return "", errors.New("can't find end tag corresponding to start tag \"" + last(open) + "\"")
	}
	return out.String(), nil
}

func last(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

// entity is the character of a named or numeric HTML entity Telegram supports.
func entity(name string) (rune, bool) {
	switch name {
	case "lt":
		return '<', true
	case "gt":
		return '>', true
	case "amp":
		return '&', true
	case "quot":
		return '"', true
	}
	digits, ok := strings.CutPrefix(name, "#")
	if !ok {
		return 0, false
	}
	base := 10
	if hex, isHex := strings.CutPrefix(strings.ToLower(digits), "x"); isHex {
		digits, base = hex, 16
	}
	n, err := strconv.ParseInt(digits, base, 32)
	if err != nil || n <= 0 {
		return 0, false
	}
	return rune(n), true
}

// decodeControl reads the JSON body of a control request, answering 400 when it is not valid.
func decodeControl(w http.ResponseWriter, r *http.Request, v any) error {
	if err := fakeserver.DecodeJSON(w, r, v); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return err
	}
	return nil
}
