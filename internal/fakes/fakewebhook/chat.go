// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakewebhook

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// ChatPrefix starts the paths of the fake chat behind the receiver (C-01.FR-13), a small REST API for the recipes of
// the template mode of outgoing webhooks: one chat per {name}, whose messages take replies in one step, or in two
// steps through a thread created first.
//
//	POST /chat/{name}/messages                    posts a message: {"data":{"id":"m<N>"}}
//	PUT  /chat/{name}/messages/{id}               edits it: {"data":{"id":"<id>"}}; 404 for an unknown message
//	POST /chat/{name}/messages/{id}/replies       replies to it (one-step threads): {"data":{"id":"r<N>"}}
//	POST /chat/{name}/threads                     creates a thread: {"thread":{"id":"t<N>"}}
//	POST /chat/{name}/threads/{id}/messages       posts into it (two-step threads): {"data":{"id":"r<N>"}}
//
// The text of each is the field text of its JSON body, the whole body as a string otherwise. GET /_fake/chat/{name}
// lists the messages with their replies, the edits and the threads with their messages; DELETE /_fake/chat/{name}
// empties the chat. The harness records the requests and applies its faults as for every path.
const ChatPrefix = "/chat/"

// ChatEntry is a message, an edit, a reply or a message of a thread: its id, its text and the body it was sent with.
type ChatEntry struct {
	ID   string          `json:"id"`
	Text string          `json:"text"`
	Body json.RawMessage `json:"body"`
}

// ChatMessage is a message of the chat as posted, with its replies; its edits are listed apart.
type ChatMessage struct {
	ChatEntry
	Replies []ChatEntry `json:"replies"`
}

// ChatThread is a thread created in two steps: the body it was created with and its messages.
type ChatThread struct {
	ID       string          `json:"id"`
	Body     json.RawMessage `json:"body"`
	Messages []ChatEntry     `json:"messages"`
}

// Chat is the state of one chat as GET /_fake/chat/{name} lists it.
type Chat struct {
	Messages []*ChatMessage `json:"messages"`
	Edits    []ChatEntry    `json:"edits"`
	Threads  []*ChatThread  `json:"threads"`
	next     map[string]int
}

// chats are the fake chats by name.
type chats struct {
	mu    sync.Mutex
	chats map[string]*Chat
}

// mount adds the chat API to mux and its control endpoints to s.
func (c *chats) mount(mux *http.ServeMux, s *fakeserver.Server) {
	c.chats = map[string]*Chat{}
	mux.HandleFunc("POST "+ChatPrefix+"{name}/messages", c.post)
	mux.HandleFunc("PUT "+ChatPrefix+"{name}/messages/{id}", c.edit)
	mux.HandleFunc("POST "+ChatPrefix+"{name}/messages/{id}/replies", c.reply)
	mux.HandleFunc("POST "+ChatPrefix+"{name}/threads", c.thread)
	mux.HandleFunc("POST "+ChatPrefix+"{name}/threads/{id}/messages", c.threadMessage)
	s.HandleControl("GET /_fake/chat/{name}", func(w http.ResponseWriter, r *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, c.Chat(r.PathValue("name")))
	})
	s.HandleControl("DELETE /_fake/chat/{name}", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		delete(c.chats, r.PathValue("name"))
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
}

// Chat is a copy of the chat name, empty when nothing was posted to it.
func (c *chats) Chat(name string) Chat {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := Chat{Messages: []*ChatMessage{}, Edits: []ChatEntry{}, Threads: []*ChatThread{}}
	ch := c.chats[name]
	if ch == nil {
		return out
	}
	for _, m := range ch.Messages {
		cp := *m
		cp.Replies = append([]ChatEntry{}, m.Replies...)
		out.Messages = append(out.Messages, &cp)
	}
	out.Edits = append(out.Edits, ch.Edits...)
	for _, t := range ch.Threads {
		cp := *t
		cp.Messages = append([]ChatEntry{}, t.Messages...)
		out.Threads = append(out.Threads, &cp)
	}
	return out
}

// entry reads the body of r as an entry with the next id of the chat for that prefix, counted per prefix; the chat is created on first
// use. It holds the lock until done is called.
func (c *chats) entry(r *http.Request, prefix string) (*Chat, ChatEntry, func()) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	c.mu.Lock()
	ch := c.chats[r.PathValue("name")]
	if ch == nil {
		ch = &Chat{Messages: []*ChatMessage{}, Edits: []ChatEntry{}, Threads: []*ChatThread{}, next: map[string]int{}}
		c.chats[r.PathValue("name")] = ch
	}
	ch.next[prefix]++
	raw := json.RawMessage(body)
	if !json.Valid(body) {
		raw, _ = json.Marshal(string(body))
	}
	text := string(body)
	var fields struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(body, &fields) == nil && fields.Text != nil {
		text = *fields.Text
	}
	return ch, ChatEntry{ID: prefix + strconv.Itoa(ch.next[prefix]), Text: text, Body: raw}, c.mu.Unlock
}

func answer(w http.ResponseWriter, key, id string) {
	fakeserver.WriteJSON(w, http.StatusOK, map[string]map[string]string{key: {"id": id}})
}

func (c *chats) post(w http.ResponseWriter, r *http.Request) {
	ch, e, done := c.entry(r, "m")
	defer done()
	ch.Messages = append(ch.Messages, &ChatMessage{ChatEntry: e, Replies: []ChatEntry{}})
	answer(w, "data", e.ID)
}

// message is the message id of ch, nil for an unknown one.
func (ch *Chat) message(id string) *ChatMessage {
	for _, m := range ch.Messages {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (c *chats) edit(w http.ResponseWriter, r *http.Request) {
	ch, e, done := c.entry(r, "e")
	defer done()
	m := ch.message(r.PathValue("id"))
	if m == nil {
		fakeserver.WriteError(w, http.StatusNotFound, "no such message")
		return
	}
	ch.Edits = append(ch.Edits, ChatEntry{ID: m.ID, Text: e.Text, Body: e.Body})
	answer(w, "data", m.ID)
}

func (c *chats) reply(w http.ResponseWriter, r *http.Request) {
	ch, e, done := c.entry(r, "r")
	defer done()
	m := ch.message(r.PathValue("id"))
	if m == nil {
		fakeserver.WriteError(w, http.StatusNotFound, "no such message")
		return
	}
	m.Replies = append(m.Replies, e)
	answer(w, "data", e.ID)
}

func (c *chats) thread(w http.ResponseWriter, r *http.Request) {
	ch, e, done := c.entry(r, "t")
	defer done()
	ch.Threads = append(ch.Threads, &ChatThread{ID: e.ID, Body: e.Body, Messages: []ChatEntry{}})
	answer(w, "thread", e.ID)
}

func (c *chats) threadMessage(w http.ResponseWriter, r *http.Request) {
	ch, e, done := c.entry(r, "r")
	defer done()
	for _, t := range ch.Threads {
		if t.ID == r.PathValue("id") {
			t.Messages = append(t.Messages, e)
			answer(w, "data", e.ID)
			return
		}
	}
	fakeserver.WriteError(w, http.StatusNotFound, "no such thread")
}
