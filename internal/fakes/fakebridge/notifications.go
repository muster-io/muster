// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakebridge

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// TypeNotificationAcknowledged is the event a gateway sends when a person acknowledges a notification.
const TypeNotificationAcknowledged = "notification_acknowledged"

// The bounds of a notification, from api/bridge.yaml.
const (
	MaxContact           = 200
	MaxNotificationText  = 1000
	maxNotificationLabel = 200
)

// actionDataPattern is the data of a notification's action as api/bridge.yaml allows it.
var actionDataPattern = regexp.MustCompile(`^[!-~]{1,64}$`)

// Notification is a notification a bot sent, as GET /_fake/notifications lists it: its id n<N>, the bot that sent it —
// 3 for GatewayToken — the Idempotency-Key it was sent with, and the request as Muster sent it: the contact, the text,
// the language and the action, null for a confirmation code. A repeated key sends nothing and adds none.
type Notification struct {
	ID       string                  `json:"notification_id"`
	Bot      int                     `json:"bot"`
	Key      string                  `json:"key"`
	Contact  string                  `json:"contact"`
	Text     string                  `json:"text"`
	Language string                  `json:"language"`
	Action   *gen.NotificationAction `json:"action"`
}

// Acknowledge is a person's acknowledgement of a notification as POST /_fake/acknowledge takes it: the notification,
// and when it happened, now by default. Data, when given, is sent in place of the action's data, as a tampering bridge
// would; Bot, when not zero, queues the event for that bot instead of the one that sent the notification, as the data
// of one Connection reported by another; and EventID, when given, queues that earlier event again unchanged, under
// its id, as a bridge that delivers an event twice, and ignores the other members.
type Acknowledge struct {
	NotificationID string     `json:"notification_id"`
	OccurredAt     *time.Time `json:"occurred_at"`
	Data           *string    `json:"data"`
	Bot            int        `json:"bot"`
	EventID        string     `json:"event_id"`
}

type notificationAcknowledgedEvent struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	OccurredAt     time.Time `json:"occurred_at"`
	NotificationID string    `json:"notification_id"`
	Data           string    `json:"data"`
}

// Notifications returns the notifications the bots sent, in the order they sent them.
func (f *Fake) Notifications() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.notifications)
}

// sendNotification answers sendNotification of a bot whose info declares the role gateway, and 501 for any other: a
// repeated Idempotency-Key gets its first answer, replayed; otherwise the notification is recorded as n<N> and answered
// 201 with its id. A request outside the bounds of api/bridge.yaml is invalid.
func (f *Fake) sendNotification(w http.ResponseWriter, r *http.Request) {
	b := botFrom(r)
	f.mu.Lock()
	gateway := f.hasRole(b, gen.Gateway)
	f.mu.Unlock()
	if !gateway {
		writeProblem(w, http.StatusNotImplemented, gen.ErrorClassInvalid,
			"POST /v1/notifications is served by a bot in the role gateway only")
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in gen.NotificationInput
	err := fakeserver.DecodeJSON(w, r, &in)
	if err == nil {
		err = checkNotification(in)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replay(w, b, key) {
		return
	}
	if err != nil {
		invalid(w, err)
		return
	}
	n := Notification{ID: "n" + strconv.Itoa(len(f.notifications)+1), Bot: b.n, Key: key, Contact: in.Contact,
		Text: in.Text, Language: string(in.Language), Action: in.Action}
	f.notifications = append(f.notifications, n)
	f.answer(w, b, key, http.StatusCreated, gen.NotificationSent{NotificationId: n.ID})
}

// checkNotification checks what decoding a sendNotification cannot: the bounds of its members.
func checkNotification(in gen.NotificationInput) error {
	switch {
	case in.Contact == "" || utf8.RuneCountInString(in.Contact) > MaxContact:
		return fmt.Errorf("contact is 1 to %d characters", MaxContact)
	case in.Text == "" || utf8.RuneCountInString(in.Text) > MaxNotificationText:
		return fmt.Errorf("text is 1 to %d characters", MaxNotificationText)
	case !in.Language.Valid():
		return fmt.Errorf("language %q is not en or ru", in.Language)
	case in.Action == nil:
		return nil
	case !actionDataPattern.MatchString(in.Action.Data):
		return errors.New("action.data is not 1 to 64 printable ASCII characters")
	case in.Action.Label == "" || utf8.RuneCountInString(in.Action.Label) > maxNotificationLabel:
		return fmt.Errorf("action.label is 1 to %d characters", maxNotificationLabel)
	}
	return nil
}

func (f *Fake) postAcknowledge(w http.ResponseWriter, r *http.Request) {
	var a Acknowledge
	if err := fakeserver.DecodeJSON(w, r, &a); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	ev, err := f.Acknowledge(r.Context(), a)
	if err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, ev)
}

// Acknowledge queues a notification_acknowledged event of the notification a names, for the bot that sent it or the
// bot a names, with the data of its action unchanged or the data a gives, and returns it; ctx is what caused it. It
// fails for a notification the fake did not send, for one without an action, which there is nothing to acknowledge
// in, and for an unknown bot. With EventID it queues that earlier event again, under its id, for its bot, and fails
// for an event the fake did not queue.
func (f *Fake) Acknowledge(ctx context.Context, a Acknowledge) (Event, error) {
	at := occurred(a.OccurredAt)
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.EventID != "" {
		return f.redeliver(ctx, a.EventID)
	}
	i := slices.IndexFunc(f.notifications, func(n Notification) bool { return n.ID == a.NotificationID })
	switch {
	case i < 0:
		return Event{}, fmt.Errorf("unknown notification %q", a.NotificationID)
	case f.notifications[i].Action == nil:
		return Event{}, fmt.Errorf("the notification %s has no action to acknowledge", a.NotificationID)
	}
	n := f.notifications[i]
	b := f.botByNumber(cmp.Or(a.Bot, n.Bot))
	if b == nil {
		return Event{}, fmt.Errorf("unknown bot %d", a.Bot)
	}
	data := n.Action.Data
	if a.Data != nil {
		data = *a.Data
	}
	return f.queue(ctx, b, TypeNotificationAcknowledged, n.ID, "", func(id string) any {
		return notificationAcknowledgedEvent{ID: id, Type: TypeNotificationAcknowledged, OccurredAt: at,
			NotificationID: n.ID, Data: data}
	}), nil
}
