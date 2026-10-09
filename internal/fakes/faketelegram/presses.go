// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package faketelegram

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// Button presses (C-01.FR-13): POST /_fake/press presses a button of a message the bot sent — a channel post, or a
// reply in a comment Thread of the discussion group, whose message carries message_thread_id (F-009) — and queues the
// callback_query for the bot that sent it. answerCallbackQuery answers a press once, with a text of at most 200
// characters, and only within answer_deadline_ms of the press: later it fails as on the Bot API (F-010). GET
// /_fake/answers lists every answer with its time and whether it was accepted.

// DefaultAnswerDeadline is how long after a press the Bot API accepts its answer (F-010).
const DefaultAnswerDeadline = 15 * time.Second

// MaxAnswerLength is the longest text of an answer to a press, in characters.
const MaxAnswerLength = 200

// The descriptions of the failures of answerCallbackQuery, as the Bot API words them.
const (
	DescriptionQueryTooOld   = "Bad Request: query is too old and response timeout expired or query ID is invalid"
	DescriptionAnswerTooLong = "Bad Request: MESSAGE_TOO_LONG"
)

// Press is a press as POST /_fake/press takes it: the chat (an id or an @username) and the message pressed, the person
// who pressed and the label of the button. DataFrom takes the button's data from another message of the chat, to press
// a valid button on the wrong message; Data, when set, is the data itself, for a forged button. PressedMsAgo makes the
// press that long ago, as an update that arrives late. Token receives the update instead of the bot that sent the
// message.
type Press struct {
	Chat         json.RawMessage `json:"chat"`
	MessageID    int64           `json:"message_id"`
	From         Sender          `json:"from"`
	Button       string          `json:"button"`
	DataFrom     int64           `json:"data_from"`
	Data         *string         `json:"data"`
	PressedMsAgo int64           `json:"pressed_ms_ago"`
	Token        string          `json:"token"`
}

// Pressed is the answer of POST /_fake/press: the update queued and its callback_query id.
type Pressed struct {
	UpdateID        int64  `json:"update_id"`
	CallbackQueryID string `json:"callback_query_id"`
}

// Answer is one answerCallbackQuery, as GET /_fake/answers lists it: the press it answered, its text and alert flag,
// when it came, how long after the press, and whether the fake accepted it, with the failure when it did not.
type Answer struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
	AtMs            int64  `json:"at_ms"`
	AfterMs         int64  `json:"after_ms"`
	OK              bool   `json:"ok"`
	Description     string `json:"description,omitempty"`
}

// press is a press waiting for its answer.
type press struct {
	at       time.Time
	answered bool
}

// handlePresses adds the control endpoints of presses.
func (f *Fake) handlePresses() {
	f.HandleControl("POST /_fake/press", f.postPress)
	f.HandleControl("GET /_fake/answers", func(w http.ResponseWriter, _ *http.Request) {
		fakeserver.WriteJSON(w, http.StatusOK, f.Answers())
	})
}

func (f *Fake) postPress(w http.ResponseWriter, r *http.Request) {
	var p Press
	if err := decodeControl(w, r, &p); err != nil {
		return
	}
	out, status, failure := f.Press(r.Context(), p)
	if failure != "" {
		fakeserver.WriteError(w, status, failure)
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, out)
}

// Press presses a button and queues its callback_query; it returns the status and text of a failure when the chat,
// the message or the button does not exist.
func (f *Fake) Press(ctx context.Context, p Press) (Pressed, int, string) {
	if p.From.ID == 0 {
		return Pressed{}, http.StatusBadRequest, "from.id is required"
	}
	if p.PressedMsAgo < 0 {
		return Pressed{}, http.StatusBadRequest, "pressed_ms_ago is 0 or more"
	}
	c := f.chats
	c.mu.Lock()
	ch, ok := c.resolve(strings.Trim(string(p.Chat), `"`))
	if !ok {
		c.mu.Unlock()
		return Pressed{}, http.StatusNotFound, "no such chat"
	}
	m := c.find(ch.ID, p.MessageID)
	source := m
	if p.DataFrom != 0 {
		source = c.find(ch.ID, p.DataFrom)
	}
	if source == nil {
		c.mu.Unlock()
		return Pressed{}, http.StatusNotFound, "no such message"
	}
	data, found := buttonData(source.ReplyMarkup, p.Button)
	if p.Data != nil {
		data, found = *p.Data, true
	}
	if !found {
		c.mu.Unlock()
		return Pressed{}, http.StatusNotFound, "the message has no button " + strconv.Quote(p.Button)
	}
	message := map[string]any{"message_id": p.MessageID, "date": c.now().Unix(), "chat": refOf(ch)}
	if m != nil {
		message = c.apiMessage(m, false)
	}
	token := p.Token
	if token == "" {
		token = source.token
	}
	at := c.now().Add(-time.Duration(p.PressedMsAgo) * time.Millisecond)
	c.mu.Unlock()
	f.mu.Lock()
	f.pressed++
	id := "cq-" + strconv.FormatInt(f.pressed, 10)
	f.presses[id] = &press{at: at}
	f.mu.Unlock()
	from := p.From
	if from.FirstName == "" {
		from.FirstName = "Presser " + strconv.FormatInt(from.ID, 10)
	}
	query := map[string]any{"id": id, "from": from, "message": message,
		"chat_instance": "ci" + strconv.FormatInt(-ch.ID, 10), "data": data}
	raw, _ := json.Marshal(map[string]any{"callback_query": query})
	updateID, err := f.Enqueue(ctx, token, raw)
	if err != nil {
		return Pressed{}, http.StatusBadRequest, err.Error()
	}
	return Pressed{UpdateID: updateID, CallbackQueryID: id}, 0, ""
}

// buttonData is the callback_data of the button labelled label in a keyboard.
func buttonData(markup json.RawMessage, label string) (string, bool) {
	var kb struct {
		InlineKeyboard [][]struct {
			Text         string `json:"text"`
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if len(markup) == 0 || json.Unmarshal(markup, &kb) != nil {
		return "", false
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.Text == label {
				return b.CallbackData, true
			}
		}
	}
	return "", false
}

// Answers are the answers to presses, in order.
func (f *Fake) Answers() []Answer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.answers)
}

// SetAnswerDeadline sets how long after a press its answer is accepted; 0 or less is DefaultAnswerDeadline.
func (f *Fake) SetAnswerDeadline(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d <= 0 {
		d = DefaultAnswerDeadline
	}
	f.config.AnswerDeadlineMS = d.Milliseconds()
}

// answerCallbackQuery answers a press: once, within the answer deadline, with at most MaxAnswerLength characters.
// Every answer is recorded, those the fake refuses too.
func (f *Fake) answerCallbackQuery(w http.ResponseWriter, p params) {
	f.chats.mu.Lock()
	now := f.chats.now()
	f.chats.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	deadline := time.Duration(f.config.AnswerDeadlineMS) * time.Millisecond
	id := p.string("callback_query_id")
	a := Answer{CallbackQueryID: id, Text: p.string("text"), ShowAlert: p.bool("show_alert"), AtMs: now.UnixMilli()}
	q := f.presses[id]
	if q != nil {
		a.AfterMs = now.Sub(q.at).Milliseconds()
	}
	status := http.StatusBadRequest
	switch {
	case id == "":
		a.Description = "Bad Request: parameter \"callback_query_id\" is required"
	case q == nil || q.answered || now.Sub(q.at) > deadline:
		a.Description = DescriptionQueryTooOld
	case utf8.RuneCountInString(a.Text) > MaxAnswerLength:
		a.Description = DescriptionAnswerTooLong
	default:
		q.answered, a.OK, status = true, true, http.StatusOK
	}
	f.answers = append(f.answers, a)
	if !a.OK {
		writeFailure(w, status, a.Description)
		return
	}
	writeOK(w, true)
}
