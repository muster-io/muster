// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeslack

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The messages of the fake (F-074 to F-092). Each channel of each app keeps its messages, a Thread's replies under its
// root, every `ts` taken from one counter — 1.000001, 1.000002, … — never from a clock, an edit's included. The rules
// Muster relies on are kept: the block limits of F-074 and F-076 with invalid_blocks and a json-pointer, a `text` over
// 4,000 bytes in Slack's stored form on an edit with msg_too_long (F-078), what an edit keeps (F-085),
// block_mismatch (F-086), message_not_found for a deleted, unknown or tombstoned `ts` (F-087), the tombstone of a
// deleted root with replies, under which replies still go (F-090), a reply to a deleted root without replies posted
// into the channel and answered without `thread_ts` (F-091), and not_in_channel for a channel the bot is not a member
// of (F-098). Not kept, since Muster relies on none of them: splitting a long `text` into several messages (F-077),
// emoji answered as shortcodes (F-079), an attachment's text cut at 8,000 characters (F-080) and a `markdown` block
// turned into `rich_text` (F-081). The stored form of F-078 is an approximation: an entity as it is, a bare `&`, `<` or
// `>` as its entity, and a code point from U+2190 on — arrows, symbols, emoji — as a 12-byte shortcode.

// chatScope is the scope the chat methods need (F-070).
const chatScope = "chat:write"

const (
	// tombstoneText is the text of a deleted root that has replies (F-090).
	tombstoneText = "This message was deleted."
	// slackbot is the user of a tombstone and the parent_user_id of a reply under it (F-090).
	slackbot = "USLACKBOT"
	// storedTextLimit is the most bytes of `text` chat.update takes, in Slack's stored form (F-078).
	storedTextLimit = 4000
	// shortcodeBytes stands for a symbol or an emoji in the stored form (F-079).
	shortcodeBytes = 12
)

// The limits of Block Kit the fake checks (F-074, F-076), in code points.
const (
	maxBlocks         = 50
	maxSectionText    = 3000
	maxHeaderText     = 150
	maxContextItems   = 10
	maxActionItems    = 25
	maxButtonText     = 75
	maxButtonValue    = 2000
	maxActionID       = 255
	maxBlockID        = 255
	maxOptions        = 100
	maxOptionText     = 75
	maxOptionValue    = 150
	richTextBlockType = "rich_text"
)

// blockTypes are the types of blocks the fake accepts.
var blockTypes = []string{"section", "context", "actions", "header", "divider", "image", richTextBlockType, "markdown"}

// Message is a message of a channel as GET /_fake/messages lists it: the app and channel it is in, its `ts`, its user,
// its text, its blocks and attachments as sent — an empty list when none were — whether it asked for unfurling, its
// subtype (tombstone for a deleted root with replies), its last edit and its Thread replies, in order.
type Message struct {
	App         string          `json:"app"`
	Channel     string          `json:"channel"`
	TS          string          `json:"ts"`
	User        string          `json:"user"`
	Text        string          `json:"text"`
	Blocks      json.RawMessage `json:"blocks"`
	Attachments json.RawMessage `json:"attachments"`
	UnfurlLinks *bool           `json:"unfurl_links"`
	UnfurlMedia *bool           `json:"unfurl_media"`
	Subtype     string          `json:"subtype"`
	Edited      *Edited         `json:"edited"`
	Replies     []Reply         `json:"replies"`
}

// Reply is a Thread reply of a Message: its `ts`, the `ts` of its root, its user, text, blocks and attachments as sent,
// whether it was broadcast to the channel and its last edit.
type Reply struct {
	TS             string          `json:"ts"`
	ThreadTS       string          `json:"thread_ts"`
	User           string          `json:"user"`
	Text           string          `json:"text"`
	Blocks         json.RawMessage `json:"blocks"`
	Attachments    json.RawMessage `json:"attachments"`
	ReplyBroadcast bool            `json:"reply_broadcast"`
	Edited         *Edited         `json:"edited"`
}

// Edited is who edited a message last, and the `ts` of the edit (F-084).
type Edited struct {
	User string `json:"user"`
	TS   string `json:"ts"`
}

// chatKey is a channel of an app.
type chatKey struct {
	app     string
	channel string
}

// stored is a message as the fake keeps it; a root holds its replies, and a reply the `ts` of its root.
type stored struct {
	seq         int64
	ts          string
	user        string
	text        string
	blocks      json.RawMessage
	attachments json.RawMessage
	unfurlLinks *bool
	unfurlMedia *bool
	// richOnly is a message whose blocks, as sent, are all rich_text: it cannot be edited into other blocks (F-086).
	richOnly  bool
	tombstone bool
	broadcast bool
	edited    *Edited
	threadTS  string
	replies   []*stored
}

// emptyList is the blocks or attachments of a message sent without them.
var emptyList = json.RawMessage("[]")

// nextTS is the next `ts` of the counter, under f.mu: 1.000001, 1.000002, …
func (f *Fake) nextTS() (int64, string) {
	f.seq++
	return f.seq, fmt.Sprintf("%d.%06d", 1+f.seq/1_000_000, f.seq%1_000_000)
}

// find is the message ts of the channel, under f.mu, with its root when it is a reply; nil for none.
func (f *Fake) find(k chatKey, ts string) (msg, root *stored) {
	for _, m := range f.chats[k] {
		if m.ts == ts {
			return m, nil
		}
		for _, r := range m.replies {
			if r.ts == ts {
				return r, m
			}
		}
	}
	return nil, nil
}

// remove deletes the message ts of the channel as Slack does, under f.mu (F-087, F-090): a reply leaves its Thread, a
// root with replies becomes a tombstone that keeps them, and a root without replies goes; false for a `ts` that is
// unknown, deleted or a tombstone.
func (f *Fake) remove(k chatKey, ts string) bool {
	m, root := f.find(k, ts)
	switch {
	case m == nil || m.tombstone:
		return false
	case root != nil:
		root.replies = slices.DeleteFunc(root.replies, func(r *stored) bool { return r == m })
	case len(m.replies) > 0:
		m.tombstone, m.user, m.text = true, slackbot, tombstoneText
		m.blocks, m.attachments, m.edited, m.richOnly = emptyList, emptyList, nil, false
	default:
		f.chats[k] = slices.DeleteFunc(f.chats[k], func(r *stored) bool { return r == m })
	}
	return true
}

// Messages are the messages of the channel of the app — of both apps when app is empty, of every channel when channel
// is — in the order they were posted, each with its Thread replies.
func (f *Fake) Messages(app, channel string) []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	type keyed struct {
		k chatKey
		m *stored
	}
	var all []keyed
	for k, ms := range f.chats {
		if (app == "" || k.app == app) && (channel == "" || k.channel == channel) {
			for _, m := range ms {
				all = append(all, keyed{k, m})
			}
		}
	}
	slices.SortFunc(all, func(a, b keyed) int { return cmp.Compare(a.m.seq, b.m.seq) })
	out := make([]Message, 0, len(all))
	for _, e := range all {
		m := e.m
		msg := Message{App: e.k.app, Channel: e.k.channel, TS: m.ts, User: m.user, Text: m.text, Blocks: m.blocks,
			Attachments: m.attachments, UnfurlLinks: m.unfurlLinks, UnfurlMedia: m.unfurlMedia, Edited: m.edited,
			Replies: []Reply{}}
		if m.tombstone {
			msg.Subtype = "tombstone"
		}
		for _, r := range m.replies {
			msg.Replies = append(msg.Replies, Reply{TS: r.ts, ThreadTS: m.ts, User: r.user, Text: r.text,
				Blocks: r.blocks, Attachments: r.attachments, ReplyBroadcast: r.broadcast, Edited: r.edited})
		}
		out = append(out, msg)
	}
	return out
}

// Delete deletes the message ts of the channel of the app — of either app when app is empty — as a person would
// (F-087, F-090); false when there is no such message.
func (f *Fake) Delete(app, channel, ts string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range apps {
		if (app == "" || a.Key == app) && f.remove(chatKey{a.Key, channel}, ts) {
			return true
		}
	}
	return false
}

// listMessages answers GET /_fake/messages?channel=…[&app=a|b] with Messages.
func (f *Fake) listMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fakeserver.WriteJSON(w, http.StatusOK, f.Messages(q.Get("app"), q.Get("channel")))
}

// deleteByPerson answers POST /_fake/delete {"channel", "ts"[, "app"]} with Delete: {"ok": true}, or 404 when there is
// no such message.
func (f *Fake) deleteByPerson(w http.ResponseWriter, r *http.Request) {
	var in struct {
		App     string `json:"app"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !f.Delete(in.App, in.Channel, in.TS) {
		fakeserver.WriteError(w, http.StatusNotFound, "no message "+in.TS+" in "+in.Channel)
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// chatArgs are the arguments of a chat method as sent: raw JSON by name.
type chatArgs map[string]json.RawMessage

// text is the argument k as text, false when it is missing or not a text.
func (a chatArgs) text(k string) (string, bool) {
	var s string
	if v, ok := a[k]; !ok || json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

// flag is the argument k as a bool, given as one or as the text true or 1; nil when it is missing.
func (a chatArgs) flag(k string) *bool {
	v, ok := a[k]
	if !ok {
		return nil
	}
	var b bool
	if json.Unmarshal(v, &b) != nil {
		s, _ := a.text(k)
		b = s == "true" || s == "1"
	}
	return &b
}

// list is the list argument k — blocks or attachments — as JSON, given as a list or as a text that holds one; nil
// when it is missing, and false when it is not a list.
func (a chatArgs) list(k string) (json.RawMessage, bool) {
	v, ok := a[k]
	if !ok {
		return nil, true
	}
	if s, isText := a.text(k); isText {
		v = json.RawMessage(s)
	}
	var items []json.RawMessage
	if json.Unmarshal(v, &items) != nil || items == nil {
		return nil, false
	}
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return nil, false
	}
	return b.Bytes(), true
}

// content is what a message shows: its text, blocks and attachments as sent, nil for those not sent.
type content struct {
	text        *string
	blocks      json.RawMessage
	attachments json.RawMessage
}

// contentOf reads and checks the content of a chat method: invalid_blocks with the errors of F-076, or
// invalid_attachments, as the error code and its errors.
func contentOf(a chatArgs) (content, string, []string) {
	var c content
	if s, ok := a.text("text"); ok {
		c.text = &s
	}
	blocks, ok := a.list("blocks")
	if !ok {
		return c, "invalid_blocks", []string{"must be an array [json-pointer:/blocks]"}
	}
	attachments, ok := a.list("attachments")
	if !ok {
		return c, "invalid_attachments", nil
	}
	c.blocks, c.attachments = blocks, attachments
	v := &validator{ids: map[string]bool{}}
	v.blocks(blocks, "/blocks")
	var atts []any
	_ = json.Unmarshal(orEmpty(attachments), &atts)
	for i, at := range atts {
		obj, ok := at.(map[string]any)
		if !ok {
			return c, "invalid_attachments", nil
		}
		if bs, ok := obj["blocks"]; ok {
			raw, _ := json.Marshal(bs) // decoded JSON always encodes
			v.blocks(raw, fmt.Sprintf("/attachments/%d/blocks", i))
		}
	}
	if v.count > maxBlocks {
		v.fail("/blocks", "no more than 50 items allowed")
	}
	if len(v.errors) > 0 {
		return c, "invalid_blocks", v.errors
	}
	return c, "", nil
}

// orEmpty is a list as sent, an empty one when it was not sent.
func orEmpty(list json.RawMessage) json.RawMessage {
	if list == nil {
		return emptyList
	}
	return list
}

// empty reports whether a list as sent has no items.
func empty(list json.RawMessage) bool {
	return len(list) == 0 || string(list) == "[]"
}

// richOnly reports whether every block of a list as sent is rich_text, and it has some.
func richOnly(list json.RawMessage) bool {
	types := blockTypesOf(list)
	return len(types) > 0 && !slices.ContainsFunc(types, func(t string) bool { return t != richTextBlockType })
}

// hasRich reports whether a list of blocks as sent has a rich_text block.
func hasRich(list json.RawMessage) bool {
	return slices.Contains(blockTypesOf(list), richTextBlockType)
}

// blockTypesOf are the types of the blocks of a list as sent.
func blockTypesOf(list json.RawMessage) []string {
	var blocks []struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(list, &blocks)
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.Type)
	}
	return out
}

// validator collects the errors of blocks as Slack names them, each with its json-pointer (F-074, F-076).
type validator struct {
	ids    map[string]bool
	count  int
	errors []string
}

func (v *validator) fail(pointer, text string) {
	v.errors = append(v.errors, text+" [json-pointer:"+pointer+"]")
}

// within checks that the text at pointer has at most limit code points.
func (v *validator) within(s, pointer string, limit int) {
	if utf8.RuneCountInString(s) > limit {
		v.fail(pointer, fmt.Sprintf("must be less than %d characters", limit+1))
	}
}

// textOf is the `text` of a text object, false when there is none.
func textOf(o any) (string, bool) {
	m, ok := o.(map[string]any)
	if !ok {
		return "", false
	}
	s, ok := m["text"].(string)
	return s, ok
}

func (v *validator) blocks(raw json.RawMessage, pointer string) {
	var blocks []any
	if len(raw) == 0 || json.Unmarshal(raw, &blocks) != nil {
		if len(raw) > 0 {
			v.fail(pointer, "must be an array")
		}
		return
	}
	v.count += len(blocks)
	for i, b := range blocks {
		p := fmt.Sprintf("%s/%d", pointer, i)
		block, ok := b.(map[string]any)
		if !ok {
			v.fail(p, "must be an object")
			continue
		}
		v.block(block, p)
	}
}

func (v *validator) block(b map[string]any, p string) {
	if id, ok := b["block_id"]; ok {
		s, _ := id.(string)
		switch {
		case s == "" || utf8.RuneCountInString(s) > maxBlockID:
			v.fail(p+"/block_id", "must be a non-empty string of at most 255 characters")
		case v.ids[s]:
			v.fail(p+"/block_id", "duplicate block_id \""+s+"\"")
		}
		v.ids[s] = true
	}
	typ, _ := b["type"].(string)
	if !slices.Contains(blockTypes, typ) {
		v.fail(p+"/type", "unsupported type")
		return
	}
	switch typ {
	case "section":
		text, ok := textOf(b["text"])
		_, fields := b["fields"]
		switch {
		case ok && text == "":
			v.fail(p+"/text/text", "must be more than 0 characters")
		case ok:
			v.within(text, p+"/text/text", maxSectionText)
		case !fields:
			v.fail(p, "must provide either text or fields")
		}
	case "header":
		text, _ := textOf(b["text"])
		if text == "" {
			v.fail(p+"/text/text", "must be more than 0 characters")
		}
		v.within(text, p+"/text/text", maxHeaderText)
	case "context":
		elements, _ := b["elements"].([]any)
		if len(elements) == 0 || len(elements) > maxContextItems {
			v.fail(p+"/elements", "must have between 1 and 10 items")
		}
	case "actions":
		v.actions(b, p)
	}
}

func (v *validator) actions(b map[string]any, p string) {
	elements, _ := b["elements"].([]any)
	if len(elements) == 0 || len(elements) > maxActionItems {
		v.fail(p+"/elements", "must have between 1 and 25 items")
	}
	actionIDs := map[string]bool{}
	for j, el := range elements {
		ep := fmt.Sprintf("%s/elements/%d", p, j)
		e, _ := el.(map[string]any)
		if id, ok := e["action_id"].(string); ok {
			v.within(id, ep+"/action_id", maxActionID)
			if actionIDs[id] {
				v.fail(ep+"/action_id", "`action_id` \""+id+"\" already exists")
			}
			actionIDs[id] = true
		}
		switch e["type"] {
		case "button":
			text, _ := textOf(e["text"])
			v.within(text, ep+"/text/text", maxButtonText)
			if value, ok := e["value"].(string); ok {
				v.within(value, ep+"/value", maxButtonValue)
			}
			if style, ok := e["style"]; ok && style != "primary" && style != "danger" {
				v.fail(ep+"/style", "must be a valid enum value")
			}
		case "static_select":
			options, _ := e["options"].([]any)
			if len(options) == 0 || len(options) > maxOptions {
				v.fail(ep+"/options", "must have between 1 and 100 items")
			}
			for k, o := range options {
				op := fmt.Sprintf("%s/options/%d", ep, k)
				om, _ := o.(map[string]any)
				text, _ := textOf(om["text"])
				v.within(text, op+"/text/text", maxOptionText)
				value, _ := om["value"].(string)
				v.within(value, op+"/value", maxOptionValue)
			}
		default:
			v.fail(ep+"/type", "unsupported type")
		}
	}
}

// storedSize is the size of a text in Slack's stored form, approximated (F-078, F-079): an entity as it is, a bare
// `&`, `<` or `>` as its entity, a code point from U+2190 on as a shortcode, and any other in UTF-8.
func storedSize(s string) int {
	n := 0
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '&' && entityAt(s[i:]) > 0:
			w = entityAt(s[i:])
			n += w
		case r == '&':
			n += len("&amp;")
		case r == '<' || r == '>':
			n += len("&lt;")
		case r >= '←':
			n += shortcodeBytes
		default:
			n += w
		}
		i += w
	}
	return n
}

// entityAt is the length of the entity s starts with — &amp;, &lt; or &gt; — 0 for none.
func entityAt(s string) int {
	for _, e := range []string{"&amp;", "&lt;", "&gt;"} {
		if strings.HasPrefix(s, e) {
			return len(e)
		}
	}
	return 0
}

// writeInvalid answers an error code with the errors of the blocks.
func writeInvalid(w http.ResponseWriter, code string, errs []string) {
	body := map[string]any{"ok": false, "error": code}
	if len(errs) > 0 {
		body["errors"] = errs
	}
	writeJSON(w, body)
}

// chatCaller answers the token of a chat method that cannot use it: an app-level token is the wrong kind, and a token
// without chat:write lacks the scope; false when it answered.
func chatCaller(w http.ResponseWriter, c caller, scopes string) bool {
	switch {
	case c.kind == kindApp:
		writeError(w, "not_allowed_token_type")
		return false
	case !slices.Contains(splitScopes(scopes), chatScope):
		missingScope(w, chatScope, scopes)
		return false
	}
	return true
}

// userOf is who the caller's messages are by: the bot of its app, or the person.
func userOf(c caller) string {
	if c.kind == kindPerson {
		return UserID
	}
	return c.app.BotUserID
}

// channelOf is the channel id of the caller's app, answered channel_not_found when it is unknown — a #name, a direct
// chat and a user included — or private without the bot (F-098); false when it answered.
func channelOf(w http.ResponseWriter, c caller, cfg Config, id string) (Channel, bool) {
	ch, ok := cfg.Channels[c.app.Key][id]
	if !ok || ch.Private && !ch.Member {
		writeError(w, "channel_not_found")
		return Channel{}, false
	}
	return ch, true
}

// postable answers a channel the caller cannot write to: archived, or without the bot; false when it answered.
func postable(w http.ResponseWriter, ch Channel) bool {
	switch {
	case ch.Archived:
		writeError(w, "is_archived")
		return false
	case !ch.Member:
		writeError(w, "not_in_channel")
		return false
	}
	return true
}

// answerOf is a message as the chat methods answer it.
func answerOf(c caller, m, root *stored) map[string]any {
	out := map[string]any{"type": "message", "user": m.user, "text": m.text, "ts": m.ts,
		"blocks": m.blocks, "attachments": m.attachments}
	if m.user == c.app.BotUserID {
		out["bot_id"] = c.app.BotID
	}
	if root != nil {
		out["thread_ts"], out["parent_user_id"] = root.ts, root.user
	}
	if m.broadcast {
		out["subtype"] = "thread_broadcast"
	}
	if m.edited != nil {
		out["edited"] = m.edited
	}
	return out
}

// postMessage answers chat.postMessage (F-074 to F-076, F-089 to F-091, F-098): to a channel of the caller's app that
// the bot is a member of, with a text, blocks or attachments that pass the checks of Block Kit; with a `thread_ts`
// that names a root, a tombstone or a reply, under its root, and posted into the channel without `thread_ts` in the
// answer when it names nothing.
func (f *Fake) postMessage(w http.ResponseWriter, c caller, cfg Config, scopes string, a chatArgs) {
	if !chatCaller(w, c, scopes) {
		return
	}
	id, _ := a.text("channel")
	ch, ok := channelOf(w, c, cfg, id)
	if !ok || !postable(w, ch) {
		return
	}
	con, code, errs := contentOf(a)
	switch {
	case code != "":
		writeInvalid(w, code, errs)
		return
	case (con.text == nil || *con.text == "") && empty(con.blocks) && empty(con.attachments):
		writeError(w, "no_text")
		return
	}
	k := chatKey{c.app.Key, id}
	m := &stored{user: userOf(c), text: deref(con.text), blocks: orEmpty(con.blocks),
		attachments: orEmpty(con.attachments), unfurlLinks: a.flag("unfurl_links"),
		unfurlMedia: a.flag("unfurl_media"), richOnly: richOnly(con.blocks)}
	f.mu.Lock()
	defer f.mu.Unlock()
	m.seq, m.ts = f.nextTS()
	var root *stored
	if threadTS, _ := a.text("thread_ts"); threadTS != "" {
		if r, parent := f.find(k, threadTS); r != nil {
			root = cmp.Or(parent, r)
		}
	}
	if root == nil {
		f.chats[k] = append(f.chats[k], m)
		writeOK(w, map[string]any{"channel": id, "ts": m.ts, "message": answerOf(c, m, nil)})
		return
	}
	m.threadTS = root.ts
	if b := a.flag("reply_broadcast"); b != nil {
		m.broadcast = *b
	}
	root.replies = append(root.replies, m)
	writeOK(w, map[string]any{"channel": id, "ts": m.ts, "message": answerOf(c, m, root)})
}

// deref is the text p points to, empty for none.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// editable finds the message ts of the channel id for chat.update and chat.delete (F-087, F-098): channel_not_found
// for an unknown channel; message_not_found for a `ts` that is unknown, deleted or a tombstone; then is_archived or
// not_in_channel when the caller can no longer write to the channel; and cant_update_message or cant_delete_message,
// as refused, for someone else's message. It runs under f.mu, and answers when it returns nil.
func (f *Fake) editable(w http.ResponseWriter, c caller, cfg Config, id, ts, refused string) (*stored, *stored) {
	ch, ok := channelOf(w, c, cfg, id)
	if !ok {
		return nil, nil
	}
	m, root := f.find(chatKey{c.app.Key, id}, ts)
	switch {
	case m == nil || m.tombstone:
		writeError(w, "message_not_found")
		return nil, nil
	case !postable(w, ch):
		return nil, nil
	case m.user != userOf(c):
		writeError(w, refused)
		return nil, nil
	}
	return m, root
}

// updateMessage answers chat.update (F-084 to F-087): the caller's own message, its `text` within 4,000 bytes in the
// stored form, its blocks checked; without `blocks` a `text` replaces them with rich_text and none keeps them, without
// `attachments` they are kept and `[]` removes them; a message sent only of rich_text blocks cannot take other blocks.
// The same edit twice is ok, and an edit records who made it.
func (f *Fake) updateMessage(w http.ResponseWriter, c caller, cfg Config, scopes string, a chatArgs) {
	if !chatCaller(w, c, scopes) {
		return
	}
	id, _ := a.text("channel")
	ts, _ := a.text("ts")
	f.mu.Lock()
	defer f.mu.Unlock()
	m, root := f.editable(w, c, cfg, id, ts, "cant_update_message")
	if m == nil {
		return
	}
	con, code, errs := contentOf(a)
	switch {
	case code != "":
		writeInvalid(w, code, errs)
		return
	case con.text != nil && storedSize(*con.text) > storedTextLimit:
		writeError(w, "msg_too_long")
		return
	case con.blocks != nil && m.richOnly && !empty(con.blocks) && !hasRich(con.blocks):
		writeError(w, "block_mismatch")
		return
	}
	text, blocks, attachments, rich := m.text, m.blocks, m.attachments, m.richOnly
	if con.text != nil {
		text = *con.text
	}
	switch {
	case con.blocks != nil:
		blocks, rich = con.blocks, richOnly(con.blocks)
	case con.text != nil:
		blocks, rich = textBlocks(text), false
	}
	if con.attachments != nil {
		attachments = con.attachments
	}
	if text == "" && empty(blocks) && empty(attachments) {
		writeError(w, "no_text")
		return
	}
	m.text, m.blocks, m.attachments, m.richOnly = text, blocks, attachments, rich
	_, editTS := f.nextTS()
	m.edited = &Edited{User: userOf(c), TS: editTS}
	writeOK(w, map[string]any{"channel": id, "ts": m.ts, "text": m.text, "message": answerOf(c, m, root)})
}

// textBlocks are the blocks Slack makes of a `text` an edit sends without blocks: one rich_text block (F-085).
func textBlocks(text string) json.RawMessage {
	b, _ := json.Marshal([]any{map[string]any{"type": richTextBlockType, "block_id": "fake",
		"elements": []any{map[string]any{"type": "rich_text_section",
			"elements": []any{map[string]any{"type": "text", "text": text}}}}}}) // plain values always encode
	return b
}

// deleteMessage answers chat.delete (F-087, F-090): the caller's own message; a root with replies stays as a
// tombstone, and a second delete is message_not_found.
func (f *Fake) deleteMessage(w http.ResponseWriter, c caller, cfg Config, scopes string, a chatArgs) {
	if !chatCaller(w, c, scopes) {
		return
	}
	id, _ := a.text("channel")
	ts, _ := a.text("ts")
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, _ := f.editable(w, c, cfg, id, ts, "cant_delete_message"); m == nil {
		return
	}
	f.remove(chatKey{c.app.Key, id}, ts)
	writeOK(w, map[string]any{"channel": id, "ts": ts})
}

// postEphemeral answers chat.postEphemeral (F-092) with a new `message_ts` and keeps what it showed for
// GET /_fake/ephemeral, never among the channel's messages: a channel the bot is a member of, a user who is one of the
// fake's people — user_not_in_channel for anyone else — in the Thread of `thread_ts` when it is sent, and a text, blocks
// or attachments that pass the checks.
func (f *Fake) postEphemeral(w http.ResponseWriter, c caller, cfg Config, scopes string, a chatArgs) {
	if !chatCaller(w, c, scopes) {
		return
	}
	id, _ := a.text("channel")
	ch, ok := channelOf(w, c, cfg, id)
	if !ok || !postable(w, ch) {
		return
	}
	user, _ := a.text("user")
	if !member(user) {
		writeError(w, "user_not_in_channel")
		return
	}
	con, code, errs := contentOf(a)
	switch {
	case code != "":
		writeInvalid(w, code, errs)
		return
	case (con.text == nil || *con.text == "") && empty(con.blocks) && empty(con.attachments):
		writeError(w, "no_text")
		return
	}
	var thread *string
	if ts, ok := a.text("thread_ts"); ok && ts != "" {
		thread = &ts
	}
	f.mu.Lock()
	_, ts := f.nextTS()
	f.ephemeral = append(f.ephemeral, Ephemeral{App: c.app.Key, Channel: id, User: user, ThreadTS: thread,
		Text: deref(con.text), TS: ts})
	f.mu.Unlock()
	writeOK(w, map[string]any{"message_ts": ts})
}

// getPermalink answers chat.getPermalink (F-088), which needs no scope: the link to a message or a tombstone of a
// channel the bot is a member of, with the Thread for a reply; message_not_found for a `ts` that is unknown or deleted
// and in a channel without the bot.
func (f *Fake) getPermalink(w http.ResponseWriter, c caller, cfg Config, a chatArgs) {
	if c.kind == kindApp {
		writeError(w, "not_allowed_token_type")
		return
	}
	id, _ := a.text("channel")
	ts, _ := a.text("message_ts")
	ch, ok := channelOf(w, c, cfg, id)
	if !ok {
		return
	}
	f.mu.Lock()
	m, root := f.find(chatKey{c.app.Key, id}, ts)
	f.mu.Unlock()
	if m == nil || !ch.Member {
		writeError(w, "message_not_found")
		return
	}
	link := c.app.TeamURL + "archives/" + id + "/p" + strings.ReplaceAll(ts, ".", "")
	if root != nil {
		link += "?thread_ts=" + root.ts + "&cid=" + id
	}
	writeOK(w, map[string]any{"channel": id, "permalink": link})
}
