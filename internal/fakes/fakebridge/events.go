// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakebridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// The people of the fake messenger, by their external ids, as the events name them: alice ("Alice Smith"), bob ("Bob
// Brown") and vera ("Vera Viewer"), whose handles are their logins.
const (
	AliceID = "u-alice"
	BobID   = "u-bob"
	VeraID  = "u-vera"
)

// people are the accounts of the fake messenger.
var people = []gen.Person{
	{ExternalId: AliceID, DisplayName: "Alice Smith", Handle: nullable.NewNullableWithValue("alice")},
	{ExternalId: BobID, DisplayName: "Bob Brown", Handle: nullable.NewNullableWithValue("bob")},
	{ExternalId: VeraID, DisplayName: "Vera Viewer", Handle: nullable.NewNullableWithValue("vera")},
}

// People are the accounts of the fake messenger: alice, bob and vera.
func People() []gen.Person { return slices.Clone(people) }

func person(externalID string) (gen.Person, bool) {
	i := slices.IndexFunc(people, func(p gen.Person) bool { return p.ExternalId == externalID })
	if i < 0 {
		return gen.Person{}, false
	}
	return people[i], true
}

// The event types the fake sends, as api/bridge.yaml names them.
const (
	TypePress          = "press"
	TypeThreadMessage  = "thread_message"
	TypeMessageDeleted = "message_deleted"
)

const (
	// DefaultWait is how long pollEvents holds a request without events when it names no wait, in seconds.
	DefaultWait = 25
	// MaxWait is the longest wait pollEvents takes, in seconds.
	MaxWait = 60
	// MaxText is the longest text of a Thread message, in code points.
	MaxText = 4000
	// MaxAnswer is the longest answer, in code points.
	MaxAnswer = 500

	// EventGone is the title of the gone answer of answerEvent for an event the bot does not know or cannot answer.
	EventGone = "The event is gone."

	// pushTimeout bounds each push of an event; the back-off between its attempts doubles from pushRetryFirst up to
	// pushRetryLast.
	pushTimeout    = 10 * time.Second
	pushRetryFirst = 100 * time.Millisecond
	pushRetryLast  = 5 * time.Second
	// pushSecretPrefix starts a push secret in the Standard Webhooks form, followed by base64.
	pushSecretPrefix = "whsec_"
)

// Event is an event of a bot as GET /_fake/events lists it: the bot it belongs to, 1 for Token and 2 for SecondToken,
// and the event as the bot sends it, by polling and by push.
type Event struct {
	Bot   int             `json:"bot"`
	Event json.RawMessage `json:"event"`
}

// Answer is an answer Muster made with answerEvent, as GET /_fake/answers lists it: the bot, the event and its type,
// the message the event is about, the person behind it, the text, and where the person sees it — ephemeral or popup
// for a press when the messenger can answer privately, otherwise thread, in the Thread of the message.
type Answer struct {
	Bot       int    `json:"bot"`
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	MessageID string `json:"message_id"`
	UserID    string `json:"user_id"`
	Text      string `json:"text"`
	ShownAs   string `json:"shown_as"`
}

// PushTarget is where the fake pushes events, as /_fake/config sets it under push: the callback URL and the push secret
// in the Standard Webhooks form, whsec_ followed by base64.
type PushTarget struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

// check refuses a target the fake cannot push to: a URL that is not absolute http or https, or a secret that is not
// whsec_ followed by base64.
func (p PushTarget) check() error {
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("push.url is not an absolute http or https URL")
	}
	if _, err := pushKey(p.Secret); err != nil {
		return err
	}
	return nil
}

func pushKey(secret string) ([]byte, error) {
	rest, ok := strings.CutPrefix(secret, pushSecretPrefix)
	if !ok {
		return nil, errors.New("push.secret does not start with whsec_")
	}
	key, err := base64.StdEncoding.DecodeString(rest)
	if err != nil || len(key) == 0 {
		return nil, errors.New("push.secret is not whsec_ followed by base64")
	}
	return key, nil
}

// sent is an event as the fake keeps it to answer it: its bot, its type, its message and the person behind it.
type sent struct {
	bot       int
	typ       string
	messageID string
	userID    string
}

// pending is an event waiting to be pushed.
type pending struct {
	id  string
	raw json.RawMessage
}

// The events as the bot sends them. Each type has its own shape, so that thread_ref stays in a Thread message as null.
type pressEvent struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	OccurredAt time.Time  `json:"occurred_at"`
	MessageID  string     `json:"message_id"`
	User       gen.Person `json:"user"`
	Data       string     `json:"data"`
}

type threadMessageEvent struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	OccurredAt time.Time  `json:"occurred_at"`
	MessageID  string     `json:"message_id"`
	ThreadRef  *string    `json:"thread_ref"`
	User       gen.Person `json:"user"`
	Text       string     `json:"text"`
}

type messageDeletedEvent struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
	MessageID  string    `json:"message_id"`
}

// Events returns the events of both bots in the order they happened.
func (f *Fake) Events() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// Answers returns the answers Muster made, in the order it made them.
func (f *Fake) Answers() []Answer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.answers)
}

// queue adds the event v of type typ about the message messageID, behind the person userID, to the poll queue of the
// bot b and, while a push target is set, to the events to push, under the next id e<N>; it wakes the long polls of b
// and starts the pusher when none runs. ctx is the request that caused the event. f.mu is held.
func (f *Fake) queue(ctx context.Context, b *bot, typ, messageID, userID string, v func(id string) any) Event {
	f.eventIDs++
	id := "e" + strconv.Itoa(f.eventIDs)
	raw, err := json.Marshal(v(id))
	if err != nil {
		panic(fmt.Sprintf("fakebridge: an event: %v", err))
	}
	b.events = append(b.events, raw)
	close(b.changed)
	b.changed = make(chan struct{})
	ev := Event{Bot: b.n, Event: raw}
	f.events = append(f.events, ev)
	f.sent[id] = sent{bot: b.n, typ: typ, messageID: messageID, userID: userID}
	if f.config.Push != nil {
		f.toPush = append(f.toPush, pending{id: id, raw: raw})
		f.startPusher(ctx)
	}
	return ev
}

// botByNumber is the bot n, 1 or 2. f.mu is held.
func (f *Fake) botByNumber(n int) *bot {
	for _, b := range f.bots {
		if b.n == n {
			return b
		}
	}
	return nil
}

// batch is the answer of pollEvents; Events is never null.
type batch struct {
	Events     []json.RawMessage `json:"events"`
	NextCursor string            `json:"next_cursor"`
}

// cursorPattern is a cursor the fake gives: the decimal index of the next event.
var cursorPattern = regexp.MustCompile(`^[0-9]{1,9}$`)

// pollEvents answers pollEvents: the events of the bot after cursor — the index of the next event, from the first
// when there is none — at once when there are any; otherwise it waits for the bot's next event up to wait seconds
// (DefaultWait when there is none) and answers the empty list after it. next_cursor is the index after the last event
// of the bot. A cursor the fake never gave and a wait outside 0…MaxWait are invalid. A request that ends while it
// waits, because the client gave up or the fake closes, is aborted without an answer.
func (f *Fake) pollEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wait := DefaultWait
	if q.Has("wait") {
		n, err := strconv.Atoi(q.Get("wait"))
		if err != nil || n < 0 || n > MaxWait {
			invalid(w, fmt.Errorf("wait is a number of seconds from 0 to %d", MaxWait))
			return
		}
		wait = n
	}
	b := botFrom(r)
	f.mu.Lock()
	next := 0
	if q.Has("cursor") {
		c := q.Get("cursor")
		n, err := strconv.Atoi(c)
		if !cursorPattern.MatchString(c) || err != nil || n > len(b.events) {
			f.mu.Unlock()
			invalid(w, errors.New("the cursor is not known"))
			return
		}
		next = n
	}
	timer := time.NewTimer(time.Duration(wait) * time.Second)
	defer timer.Stop()
	for {
		if len(b.events) > next || wait == 0 {
			out := batch{Events: append([]json.RawMessage{}, b.events[next:]...),
				NextCursor: strconv.Itoa(len(b.events))}
			f.mu.Unlock()
			fakeserver.WriteJSON(w, http.StatusOK, out)
			return
		}
		changed := b.changed
		f.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			wait = 0
		case <-r.Context().Done():
			panic(http.ErrAbortHandler)
		}
		f.mu.Lock()
	}
}

// answerEvent answers answerEvent: a repeated Idempotency-Key gets its first answer, replayed; otherwise the answer is
// recorded for an event of the bot that a person caused, a press or a Thread message, and shown privately for a press
// when capabilities.private_answers allows it — ephemeral before popup — and in the Thread of its message otherwise.
// An event the bot does not know, and a message_deleted event, are gone.
func (f *Fake) answerEvent(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in gen.AnswerInput
	err := fakeserver.DecodeJSON(w, r, &in)
	if err == nil {
		err = checkAnswer(in)
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
	ev, ok := f.sent[in.EventId]
	if !ok || ev.bot != b.n || ev.typ == TypeMessageDeleted {
		writeProblem(w, http.StatusNotFound, gen.ErrorClassGone, EventGone)
		return
	}
	shown := gen.AnswerShownShownAsThread
	if ev.typ == TypePress && f.config.Capabilities.PrivateAnswers != nil {
		switch private := *f.config.Capabilities.PrivateAnswers; {
		case slices.Contains(private, gen.CapabilitiesPrivateAnswersEphemeral):
			shown = gen.AnswerShownShownAsEphemeral
		case slices.Contains(private, gen.CapabilitiesPrivateAnswersPopup):
			shown = gen.AnswerShownShownAsPopup
		}
	}
	f.answers = append(f.answers, Answer{Bot: b.n, EventID: in.EventId, EventType: ev.typ, MessageID: ev.messageID,
		UserID: ev.userID, Text: in.Text, ShownAs: string(shown)})
	f.answer(w, b, key, http.StatusOK, gen.AnswerShown{ShownAs: shown})
}

// eventIDPattern is an event id as api/bridge.yaml allows it.
var eventIDPattern = regexp.MustCompile(`^[!-~]{1,200}$`)

func checkAnswer(in gen.AnswerInput) error {
	switch n := utf8.RuneCountInString(in.Text); {
	case !eventIDPattern.MatchString(in.EventId):
		return errors.New("event_id is not 1 to 200 printable ASCII characters")
	case n == 0 || n > MaxAnswer:
		return fmt.Errorf("text is 1 to %d characters", MaxAnswer)
	}
	return nil
}

// Press is a person's press as POST /_fake/press takes it: the message — a Root message, a Thread reply, a Storm
// summary or a test message — and the person; the label of the button in the message's latest content, its last edit
// or what was posted, whose data the event carries unchanged; or the data itself, which needs no message the fake
// knows, with the bot whose queue gets the event, the message's bot by default, else 1. OccurredAt defaults to now.
type Press struct {
	MessageID  string     `json:"message_id"`
	UserID     string     `json:"user_id"`
	Button     string     `json:"button"`
	Data       *string    `json:"data"`
	OccurredAt *time.Time `json:"occurred_at"`
	Bot        int        `json:"bot"`
}

// ThreadMessage is a person's message in the Thread of a message as POST /_fake/thread-message takes it: the message
// the Thread belongs to, never a reply, the person and the text; OccurredAt defaults to now.
type ThreadMessage struct {
	MessageID  string     `json:"message_id"`
	UserID     string     `json:"user_id"`
	Text       string     `json:"text"`
	OccurredAt *time.Time `json:"occurred_at"`
}

func (f *Fake) postPress(w http.ResponseWriter, r *http.Request) {
	var p Press
	if err := fakeserver.DecodeJSON(w, r, &p); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	ev, err := f.Press(r.Context(), p)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, ev)
}

// Press queues the press p as a press event of its bot and returns it; ctx is what caused it, which a pusher started
// for it outlives. It fails for an unknown person, a message the fake does not know or that a person deleted, or a
// label the message has no button with, unless p gives the data.
func (f *Fake) Press(ctx context.Context, p Press) (Event, error) {
	user, ok := person(p.UserID)
	switch {
	case !ok:
		return Event{}, fmt.Errorf("unknown user %q: the fake knows %s, %s and %s", p.UserID, AliceID, BobID, VeraID)
	case p.MessageID == "":
		return Event{}, errors.New("message_id is required")
	case p.Bot < 0 || p.Bot > 2:
		return Event{}, errors.New("bot is 1 or 2")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n, buttons, deleted, found := f.pressable(p.MessageID)
	var data string
	switch {
	case p.Data != nil:
		data = *p.Data
	case !found:
		return Event{}, errors.New("unknown message " + p.MessageID)
	case deleted:
		return Event{}, errors.New("the message " + p.MessageID + " was deleted")
	default:
		i := slices.IndexFunc(buttons, func(b gen.Button) bool { return b.Label == p.Button })
		if i < 0 {
			labels := make([]string, 0, len(buttons))
			for _, b := range buttons {
				labels = append(labels, strconv.Quote(b.Label))
			}
			return Event{}, fmt.Errorf("the message %s has no button %q; its buttons are [%s]", p.MessageID, p.Button,
				strings.Join(labels, ", "))
		}
		data = buttons[i].Data
	}
	if p.Bot != 0 {
		n = p.Bot
	}
	at := occurred(p.OccurredAt)
	return f.queue(ctx, f.botByNumber(n), TypePress, p.MessageID, user.ExternalId, func(id string) any {
		return pressEvent{ID: id, Type: TypePress, OccurredAt: at, MessageID: p.MessageID, User: user, Data: data}
	}), nil
}

// pressable finds the message or the Thread reply id: its bot, 1 when there is none, the buttons of its latest
// content, and whether a person deleted it. f.mu is held.
func (f *Fake) pressable(id string) (n int, buttons []gen.Button, deleted, found bool) {
	for _, m := range f.messages {
		if m.ID == id {
			buttons = m.Envelope.Buttons
			if len(m.Edits) > 0 {
				buttons = m.Edits[len(m.Edits)-1].Buttons
			}
			return m.Bot, buttons, m.Deleted, true
		}
		for _, r := range m.Replies {
			if r.ID == id {
				return m.Bot, r.Input.Buttons, m.Deleted, true
			}
		}
	}
	return 1, nil, false, false
}

func (f *Fake) postThreadMessage(w http.ResponseWriter, r *http.Request) {
	var t ThreadMessage
	if err := fakeserver.DecodeJSON(w, r, &t); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	ev, err := f.SendThreadMessage(r.Context(), t)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, ev)
}

// SendThreadMessage queues t as a thread_message event of the bot of its message and returns it; its thread_ref is
// t-<message id> once the message has a Thread reply, null before. ctx is what caused it. It fails for an unknown
// person, a message the fake does not know, a Thread reply, a message that a person deleted, or a text longer than
// MaxText code points.
func (f *Fake) SendThreadMessage(ctx context.Context, t ThreadMessage) (Event, error) {
	user, ok := person(t.UserID)
	switch {
	case !ok:
		return Event{}, fmt.Errorf("unknown user %q: the fake knows %s, %s and %s", t.UserID, AliceID, BobID, VeraID)
	case utf8.RuneCountInString(t.Text) > MaxText:
		return Event{}, fmt.Errorf("text is longer than %d characters", MaxText)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.messages, func(m *Message) bool { return m.ID == t.MessageID })
	switch {
	case i < 0:
		return Event{}, errors.New("unknown message " + t.MessageID + "; a Thread message names the message of its Thread")
	case f.messages[i].Deleted:
		return Event{}, errors.New("the message " + t.MessageID + " was deleted")
	}
	m := f.messages[i]
	var ref *string
	if len(m.Replies) > 0 {
		s := "t-" + m.ID
		ref = &s
	}
	at := occurred(t.OccurredAt)
	return f.queue(ctx, f.botByNumber(m.Bot), TypeThreadMessage, m.ID, user.ExternalId, func(id string) any {
		return threadMessageEvent{ID: id, Type: TypeThreadMessage, OccurredAt: at, MessageID: m.ID, ThreadRef: ref,
			User: user, Text: t.Text}
	}), nil
}

// deleted queues a message_deleted event of m when capabilities.deletion_events is on. f.mu is held.
func (f *Fake) deleted(ctx context.Context, m *Message) {
	if on := f.config.Capabilities.DeletionEvents; on == nil || !*on {
		return
	}
	at := time.Now().UTC()
	f.queue(ctx, f.botByNumber(m.Bot), TypeMessageDeleted, m.ID, "", func(id string) any {
		return messageDeletedEvent{ID: id, Type: TypeMessageDeleted, OccurredAt: at, MessageID: m.ID}
	})
}

func occurred(at *time.Time) time.Time {
	if at != nil {
		return at.UTC()
	}
	return time.Now().UTC()
}

// startPusher starts the pusher when events wait to be pushed to a target and none runs, unless the fake closed. The
// pusher's context outlives ctx, the request that caused the event, and ends when the fake closes: Close, or the
// shutdown of its server, which the request names. f.mu is held.
func (f *Fake) startPusher(ctx context.Context) {
	if f.pushStopped || f.pushing || f.config.Push == nil || len(f.toPush) == 0 {
		return
	}
	if f.pushCtx == nil {
		f.pushCtx, f.pushStop = context.WithCancel(context.WithoutCancel(ctx))
		if srv, ok := ctx.Value(http.ServerContextKey).(*http.Server); ok {
			srv.RegisterOnShutdown(f.stopPushing)
		}
	}
	f.pushing = true
	f.pushers.Add(1)
	go f.push(f.pushCtx)
}

// stopPushing ends the pusher and starts no other.
func (f *Fake) stopPushing() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushStopped = true
	if f.pushStop != nil {
		f.pushStop()
	}
}

// retarget handles a change of the push target from old: none drops the events not pushed yet; another one wakes the
// pusher, which pushes them to the new target at once. f.mu is held.
func (f *Fake) retarget(old *PushTarget) {
	switch target := f.config.Push; {
	case target == nil:
		f.toPush = nil
	case old == nil || *old != *target:
		select {
		case f.pushWake <- struct{}{}:
		default:
		}
	}
}

// push posts the events waiting to be pushed one at a time, in order, until none waits, the target is removed or ctx
// ends: each until the target answers 204, or 400, which drops it, retrying every other answer and every failure after
// a back-off from pushRetryFirst, doubled up to pushRetryLast, or at once when the target changes.
func (f *Fake) push(ctx context.Context) {
	defer f.pushers.Done()
	delay := pushRetryFirst
	for {
		f.mu.Lock()
		if ctx.Err() != nil || f.config.Push == nil || len(f.toPush) == 0 {
			f.pushing = false
			f.mu.Unlock()
			return
		}
		target, next := *f.config.Push, f.toPush[0]
		f.mu.Unlock()
		status := f.pushOne(ctx, target, next)
		if status == http.StatusNoContent || status == http.StatusBadRequest {
			f.mu.Lock()
			if len(f.toPush) > 0 && f.toPush[0].id == next.id {
				f.toPush = f.toPush[1:]
			}
			f.mu.Unlock()
			delay = pushRetryFirst
			continue
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
		case <-f.pushWake:
		case <-t.C:
		}
		t.Stop()
		delay = min(delay*2, pushRetryLast)
	}
}

// pushOne posts the event p to target, signed as Standard Webhooks does — webhook-id the event's id, webhook-timestamp
// now, webhook-signature v1, and the base64 HMAC-SHA256 of id.timestamp.body with the decoded secret — and returns the
// status of the answer, 0 when there was none.
func (f *Fake) pushOne(ctx context.Context, target PushTarget, p pending) int {
	key, err := pushKey(target.Secret)
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(p.raw))
	if err != nil {
		return 0
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(p.id + "." + timestamp + "."))
	_, _ = mac.Write(p.raw)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("webhook-id", p.id)
	req.Header.Set("webhook-timestamp", timestamp)
	req.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	resp, err := f.client.Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	return resp.StatusCode
}
