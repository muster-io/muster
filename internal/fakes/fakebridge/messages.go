// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakebridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// MessageGone is the title of the gone answer for a message that a person deleted.
const MessageGone = "The message no longer exists."

// Message is a message a bot posted, as GET /_fake/messages lists it: its id, the bot that posted it — 1 for Token, 2
// for SecondToken, never the token — its channel, the envelope as Muster sent it, each edit as the whole envelope
// Muster sent, its Thread replies, the link the fake answered, whether a person deleted it, and whether its body was
// longer than limits.max_body_length, which the fake answers as truncated.
type Message struct {
	ID        string               `json:"id"`
	Bot       int                  `json:"bot"`
	ChannelID string               `json:"channel_id"`
	Envelope  gen.MessageInput     `json:"envelope"`
	Edits     []gen.MessageContent `json:"edits"`
	Replies   []Reply              `json:"replies"`
	URL       string               `json:"url"`
	Deleted   bool                 `json:"deleted"`
	Truncated bool                 `json:"truncated"`
}

// Reply is a Thread reply under its message: its id, the thread_ref Muster sent (null for none), the reply as Muster
// sent it, the thread_ref the fake answered, t-<message id>, and whether its body was too long.
type Reply struct {
	ID            string               `json:"id"`
	SentThreadRef *string              `json:"sent_thread_ref"`
	Input         gen.ThreadReplyInput `json:"input"`
	ThreadRef     string               `json:"thread_ref"`
	Truncated     bool                 `json:"truncated"`
}

// stored is the first answer to an Idempotency-Key: its status and body.
type stored struct {
	status int
	body   []byte
}

// keyPattern is what api/bridge.yaml allows as an Idempotency-Key.
var keyPattern = regexp.MustCompile(`^[!-~]{1,255}$`)

// Messages returns the messages of both bots in the order they were posted, with their edits and replies.
func (f *Fake) Messages() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Message, 0, len(f.messages))
	for _, m := range f.messages {
		c := *m
		c.Edits, c.Replies = slices.Clone(m.Edits), slices.Clone(m.Replies)
		out = append(out, c)
	}
	return out
}

// botFrom is the bot that serve found for the request.
func botFrom(r *http.Request) *bot {
	b, _ := r.Context().Value(botKey{}).(*bot)
	return b
}

// postMessage answers postMessage: a repeated Idempotency-Key gets its first answer, replayed; otherwise the message
// is posted to a channel the bot can use, as m<N> with a link to it and no thread_ref, and a channel it cannot use is
// forbidden with MessageNotMember.
func (f *Fake) postMessage(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in gen.MessageInput
	err := fakeserver.DecodeJSON(w, r, &in)
	if err == nil {
		err = checkPost(in)
	}
	b := botFrom(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replay(w, b, key) {
		return
	}
	if err != nil {
		invalid(w, err)
		return
	}
	if f.config.Channels[in.ChannelId] == nil {
		writeProblemDetail(w, http.StatusForbidden, gen.ErrorClassForbidden, "Forbidden", MessageNotMember)
		return
	}
	f.posted++
	m := &Message{ID: "m" + strconv.Itoa(f.posted), Bot: b.n, ChannelID: in.ChannelId, Envelope: in,
		Edits: []gen.MessageContent{}, Replies: []Reply{}, Truncated: f.tooLong(in.Body)}
	m.URL = f.URL() + "/m/" + m.ID
	f.messages = append(f.messages, m)
	f.answer(w, b, key, http.StatusCreated, gen.MessagePosted{MessageId: m.ID,
		Url: nullable.NewNullableWithValue(m.URL), Truncated: m.Truncated})
}

// editMessage answers editMessage: the whole envelope is added to the message's edits.
func (f *Fake) editMessage(w http.ResponseWriter, r *http.Request) {
	var in gen.MessageContent
	if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
		invalid(w, err)
		return
	}
	if err := checkContent(in.Status, in.Buttons); err != nil {
		invalid(w, err)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.find(w, botFrom(r), r.PathValue("message_id"))
	if m == nil {
		return
	}
	m.Edits = append(m.Edits, in)
	fakeserver.WriteJSON(w, http.StatusOK, gen.MessageEdited{Truncated: f.tooLong(in.Body)})
}

// replyInThread answers replyInThread: a repeated Idempotency-Key gets its first answer, replayed; otherwise the reply
// r<N> is added under its message, whatever thread_ref was sent, and answers the thread_ref t-<message id>.
func (f *Fake) replyInThread(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in gen.ThreadReplyInput
	err := fakeserver.DecodeJSON(w, r, &in)
	if err == nil {
		err = checkReply(in)
	}
	b := botFrom(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replay(w, b, key) {
		return
	}
	if err != nil {
		invalid(w, err)
		return
	}
	m := f.find(w, b, r.PathValue("message_id"))
	if m == nil {
		return
	}
	f.replies++
	reply := Reply{ID: "r" + strconv.Itoa(f.replies), Input: in, ThreadRef: "t-" + m.ID, Truncated: f.tooLong(in.Body)}
	if ref, err := in.ThreadRef.Get(); err == nil {
		reply.SentThreadRef = &ref
	}
	m.Replies = append(m.Replies, reply)
	f.answer(w, b, key, http.StatusCreated, gen.ThreadReplyPosted{MessageId: reply.ID, ThreadRef: reply.ThreadRef,
		Truncated: reply.Truncated})
}

// deleteMessage deletes the message id as a person in the messenger would: its later edits and replies are gone, and
// the first deletion queues a message_deleted event when capabilities.deletion_events is on (events.go).
func (f *Fake) deleteMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.messages, func(m *Message) bool { return m.ID == id })
	if i < 0 {
		fakeserver.WriteError(w, http.StatusNotFound, "unknown message "+id)
		return
	}
	if m := f.messages[i]; !m.Deleted {
		m.Deleted = true
		f.deleted(r.Context(), m)
	}
	w.WriteHeader(http.StatusNoContent)
}

// find is the message id of the bot b, or nil after answering: invalid for an id the bot never posted, and gone for a
// message that a person deleted. f.mu is held.
func (f *Fake) find(w http.ResponseWriter, b *bot, id string) *Message {
	i := slices.IndexFunc(f.messages, func(m *Message) bool { return m.ID == id && m.Bot == b.n })
	switch {
	case i < 0:
		writeProblemDetail(w, http.StatusBadRequest, gen.ErrorClassInvalid, "Invalid", "No message "+id+".")
		return nil
	case f.messages[i].Deleted:
		writeProblem(w, http.StatusNotFound, gen.ErrorClassGone, MessageGone)
		return nil
	}
	return f.messages[i]
}

// tooLong reports whether body is longer than limits.max_body_length code points, which the fake answers as
// truncated. f.mu is held.
func (f *Fake) tooLong(body string) bool {
	limit := f.config.Limits.MaxBodyLength
	return limit > 0 && utf8.RuneCountInString(body) > limit
}

// replay answers a key that the bot b stored with its first answer and replayed: true, and reports whether it did;
// never with replays off. f.mu is held.
func (f *Fake) replay(w http.ResponseWriter, b *bot, key string) bool {
	s, ok := b.keys[key]
	if !ok || !f.config.Replays {
		return false
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(s.body, &members); err != nil {
		panic(fmt.Sprintf("fakebridge: a stored answer: %v", err))
	}
	members["replayed"] = json.RawMessage("true")
	body, err := json.Marshal(members)
	if err != nil {
		panic(fmt.Sprintf("fakebridge: a replayed answer: %v", err))
	}
	writeBody(w, s.status, body)
	return true
}

// answer answers a request that succeeded with v and, with replays on, stores it for its key. f.mu is held.
func (f *Fake) answer(w http.ResponseWriter, b *bot, key string, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("fakebridge: an answer: %v", err))
	}
	if f.config.Replays {
		b.keys[key] = stored{status: status, body: body}
	}
	writeBody(w, status, body)
}

func writeBody(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// idempotencyKey is the request's Idempotency-Key; without a valid one it answers invalid.
func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if !keyPattern.MatchString(key) {
		invalid(w, errors.New("the Idempotency-Key header is missing or is not 1 to 255 printable ASCII characters"))
		return "", false
	}
	return key, true
}

func invalid(w http.ResponseWriter, err error) {
	writeProblemDetail(w, http.StatusBadRequest, gen.ErrorClassInvalid, "Invalid", err.Error())
}

// checkPost checks what decoding a postMessage cannot: its channel, its kind and the members the protocol requires.
func checkPost(in gen.MessageInput) error {
	switch {
	case in.ChannelId == "":
		return errors.New("channel_id is required")
	case !in.Kind.Valid():
		return fmt.Errorf("kind %q is not a message kind", in.Kind)
	case in.Mentions == nil:
		return errors.New("mentions is required")
	}
	return checkContent(in.Status, in.Buttons)
}

// checkContent checks the status and the buttons of an envelope, which the protocol requires.
func checkContent(status gen.Status, buttons []gen.Button) error {
	switch {
	case !status.Valid():
		return fmt.Errorf("status %q is not a status", status)
	case buttons == nil:
		return errors.New("buttons is required")
	}
	return nil
}

// checkReply checks the members of a Thread reply that the protocol requires: thread_ref, null when there is none,
// buttons and mentions.
func checkReply(in gen.ThreadReplyInput) error {
	switch {
	case !in.ThreadRef.IsSpecified():
		return errors.New("thread_ref is required")
	case in.Buttons == nil:
		return errors.New("buttons is required")
	case in.Mentions == nil:
		return errors.New("mentions is required")
	}
	return nil
}
