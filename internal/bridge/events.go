// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
)

// The types of the events of the Bridge protocol (C-22.FR-11).
const (
	TypePress          = "press"
	TypeThreadMessage  = "thread_message"
	TypeAccountLink    = "account_link"
	TypeMessageDeleted = "message_deleted"
)

// The event delivery modes of a Bridge Connection (connection.bridge.event_delivery).
const (
	EventDeliveryPoll = string(gen.Poll)
	EventDeliveryPush = string(gen.Push)
)

// EventDedupWindow is bridge.event_dedup_window: an event id of a Connection is handled once within it, on the
// business clock.
const EventDedupWindow = 24 * time.Hour

// ParallelEvents is bridge.parallel_events: the most Bridge events one process handles at a time, over all its Bridge
// Connections and both event delivery modes; the others wait for a slot. An event being handled holds a connection of
// the main pool for its claim while its Command takes one more, as telegram.ParallelUpdates bounds Telegram updates.
const ParallelEvents = 2

// EventMaxAge is bridge.event_max_age: a press or a Text command older than this after its occurred_at, by the real
// clock, is dropped, unanswered, and logged.
const EventMaxAge = time.Hour

// The bounds of the members of an event, from api/bridge.yaml.
const (
	maxDisplayName = 200
	maxHandle      = 320
	maxText        = 4000
)

var (
	eventIDRe = regexp.MustCompile(`^[!-~]{1,200}$`)
	tokenRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// ErrNotAnEvent is a body that is no event of the Bridge protocol: not a JSON object, of a type the protocol does not
// define, or without a member its type requires or with one out of its bounds. The wrapped text names what is wrong,
// never a value of the body.
var ErrNotAnEvent = errors.New("the body is not an event of the bridge protocol")

// ErrNoConnection is a Bridge Connection that is unknown, deleted or not a Bridge Connection.
var ErrNoConnection = errors.New("no such bridge connection")

// Event is one event of the Bridge protocol, checked against api/bridge.yaml, since the generated models check
// nothing. MessageID is a press's, a Thread message's and a message_deleted event's; User a press's, a Thread
// message's and an account_link event's; Data a press's; Text and ThreadRef a Thread message's; Token an account_link
// event's.
type Event struct {
	ID         string
	Type       string
	OccurredAt time.Time
	MessageID  string
	User       gen.Person
	Data       string
	Text       string
	ThreadRef  *string
	Token      string
}

func notAnEvent(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNotAnEvent, fmt.Sprintf(format, args...))
}

// required are the members each type of event must have, besides id, type and occurred_at.
var required = map[string][]string{
	TypePress:          {"message_id", "user", "data"},
	TypeThreadMessage:  {"message_id", "thread_ref", "user", "text"},
	TypeAccountLink:    {"token", "user"},
	TypeMessageDeleted: {"message_id"},
}

// DecodeEvent decodes one event of the Bridge protocol (C-22.FR-11) with the generated models and checks it against
// api/bridge.yaml: a JSON object of one of the four types, with its id, occurred_at and every member its type requires,
// within their bounds; members it does not know are ignored. Anything else is ErrNotAnEvent.
func DecodeEvent(raw []byte) (Event, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return Event{}, notAnEvent("not a JSON object")
	}
	var union gen.Event
	_ = union.UnmarshalJSON(raw) // raw is a JSON object, which the union always takes as it is
	var base gen.EventBase
	if err := json.Unmarshal(raw, &base); err != nil {
		return Event{}, notAnEvent("id, type or occurred_at is not of its type")
	}
	names, ok := required[base.Type]
	if !ok {
		return Event{}, notAnEvent("the type is not one of the protocol")
	}
	for _, name := range append([]string{"id", "occurred_at"}, names...) {
		v, ok := members[name]
		if !ok {
			return Event{}, notAnEvent("%s is missing", name)
		}
		if name != "thread_ref" && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return Event{}, notAnEvent("%s is null", name)
		}
	}
	if !eventIDRe.MatchString(base.Id) {
		return Event{}, notAnEvent("id is not 1 to 200 printable ASCII characters without spaces")
	}
	if base.OccurredAt.IsZero() {
		return Event{}, notAnEvent("occurred_at is not a date-time")
	}
	v, err := union.ValueByDiscriminator()
	if err != nil {
		return Event{}, notAnEvent("the members of the %s event are not of their types", base.Type)
	}
	ev := Event{ID: base.Id, Type: base.Type, OccurredAt: base.OccurredAt}
	switch e := v.(type) {
	case gen.PressEvent:
		ev.MessageID, ev.User, ev.Data = e.MessageId, e.User, e.Data
	case gen.ThreadMessageEvent:
		ev.MessageID, ev.User, ev.Text = e.MessageId, e.User, e.Text
		if e.ThreadRef.IsSpecified() && !e.ThreadRef.IsNull() {
			ref := e.ThreadRef.MustGet()
			ev.ThreadRef = &ref
		}
		if utf8.RuneCountInString(ev.Text) > maxText {
			return Event{}, notAnEvent("text is longer than %d characters", maxText)
		}
	case gen.AccountLinkEvent:
		ev.User, ev.Token = e.User, e.Token
		if !tokenRe.MatchString(ev.Token) {
			return Event{}, notAnEvent("token is not 43 characters of base64url")
		}
	case gen.MessageDeletedEvent:
		ev.MessageID = e.MessageId
	}
	if ev.Type != TypeMessageDeleted {
		if err := checkPerson(members["user"], ev.User); err != nil {
			return Event{}, err
		}
	}
	return ev, nil
}

// checkPerson checks the member user of an event: an object with external_id and display_name within their bounds,
// and a handle that is a string within its bound or null.
func checkPerson(raw json.RawMessage, p gen.Person) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return notAnEvent("user is not an object")
	}
	for _, name := range []string{"external_id", "display_name"} {
		if _, ok := members[name]; !ok {
			return notAnEvent("user.%s is missing", name)
		}
	}
	if n := utf8.RuneCountInString(p.ExternalId); n < 1 || n > maxID {
		return notAnEvent("user.external_id is not 1 to %d characters", maxID)
	}
	if n := utf8.RuneCountInString(p.DisplayName); n < 1 || n > maxDisplayName {
		return notAnEvent("user.display_name is not 1 to %d characters", maxDisplayName)
	}
	if p.Handle.IsSpecified() && !p.Handle.IsNull() && utf8.RuneCountInString(p.Handle.MustGet()) > maxHandle {
		return notAnEvent("user.handle is longer than %d characters", maxHandle)
	}
	return nil
}

// Polled is a Bridge Connection that receives its events by polling, with the stored cursor of its events, nil for
// none.
type Polled struct {
	Conn   Conn
	Cursor *string
}

// Connections are the Bridge Connections as the event paths need them, declared by their consumer;
// *connections.Service implements them and internal/runtime wires it, since internal/connections imports this package.
type Connections interface {
	// PushConnection is the Bridge Connection publicID with its client, whose settings hold the push secret, and its
	// event delivery mode; a Connection that is unknown, deleted or not a Bridge Connection is ErrNoConnection.
	PushConnection(ctx context.Context, publicID string) (Conn, string, error)
	// PollingConnections lists the Bridge Connections that are not deleted and receive their events by polling, each
	// with its client, kept until the Connection changes, and its stored cursor.
	PollingConnections(ctx context.Context) ([]Polled, error)
	// StoreEventsCursor stores the cursor of the events of c, or drops it when cursor is nil, unless the Connection
	// changed since c was read.
	StoreEventsCursor(ctx context.Context, c Conn, cursor *string) error
	// HandleEventOnce runs f for the event eventID of the Connection connectionID unless its id was recorded within
	// EventDedupWindow, under the transaction advisory lock of the event's claim, so that a second copy of the event
	// waits for the first and then finds the id recorded; once f succeeded it records the id and commits. It reports
	// whether f ran. An error of f records nothing, so that the event comes again.
	HandleEventOnce(ctx context.Context, connectionID int64, eventID string, f func(context.Context) error) (bool,
		error)
}

// handled is what the handling of an event leads to once its claim committed: the line to log, when there is one.
type handled struct {
	line   logging.Event
	fields []logging.Field
}

// Events is the one handler of Bridge events for both event delivery modes (C-22.FR-11, ADR-0017): the poller and the
// push callback hand it every event they decoded. It handles at most Parallel events at a time, claims each event id
// of a Connection through Store, dispatches the event inside the claim — a Command runs through the dispatcher in its
// own transaction — and logs what it did once the claim committed. Every event type of the protocol is handled,
// whatever the Connection's info declares.
type Events struct {
	Store Connections
	// Clocks: the real clock dates the age of a press against bridge.event_max_age.
	Clocks clock.Clocks
	// MaxAge is bridge.event_max_age; zero is EventMaxAge.
	MaxAge time.Duration
	// Parallel is bridge.parallel_events; zero is ParallelEvents.
	Parallel int
	Log      *logging.Logger

	slotsOnce sync.Once
	slots     chan struct{}
}

// slot waits until fewer than Parallel events are being handled, or ctx ends, and returns the release of the slot it
// took.
func (e *Events) slot(ctx context.Context) (func(), error) {
	e.slotsOnce.Do(func() {
		n := e.Parallel
		if n <= 0 {
			n = ParallelEvents
		}
		e.slots = make(chan struct{}, n)
	})
	select {
	case e.slots <- struct{}{}:
		return func() { <-e.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Handle handles the event ev of the Connection c once a slot is free (C-22.FR-11). An event whose id the Connection
// handled before is acknowledged without handling it. An error — no slot before ctx ended, or a claim or a handling
// that failed, logged as bridge_event_failed — records nothing: the poller keeps its cursor and the push callback
// answers 500, so that the event comes again.
func (e *Events) Handle(ctx context.Context, c Conn, ev Event) error {
	release, err := e.slot(ctx)
	if err != nil {
		return fmt.Errorf("handle the %s event %s of the connection %s: %w", ev.Type, ev.ID, c.PublicID, err)
	}
	var h handled
	ran, err := e.Store.HandleEventOnce(ctx, c.ID, ev.ID, func(ctx context.Context) error {
		var err error
		h, err = e.dispatch(ctx, c, ev)
		return err
	})
	release()
	if err != nil {
		e.Log.Log(ctx, logging.BridgeEventFailed, logging.F("connection", c.PublicID), logging.F("event_id", ev.ID),
			logging.F("type", ev.Type), logging.F("error", err.Error()))
		return fmt.Errorf("handle the %s event %s of the connection %s: %w", ev.Type, ev.ID, c.PublicID, err)
	}
	if ran && h.line.Name() != "" {
		e.Log.Log(ctx, h.line, h.fields...)
	}
	return nil
}

// dispatch handles ev of the Connection c inside its claim and returns what to log once the claim committed; it sends
// nothing. An event whose context ended fails, so that it is not recorded. A press older than MaxAge by the real clock
// is dropped, unanswered. An account_link event is acknowledged and changes nothing until Account links through a
// Bridge exist.
func (e *Events) dispatch(ctx context.Context, c Conn, ev Event) (handled, error) {
	if err := ctx.Err(); err != nil {
		return handled{}, err
	}
	if ev.Type == TypePress {
		if age, old := e.tooOld(ev); old {
			return handled{line: logging.BridgePressDropped, fields: []logging.Field{
				logging.F("connection", c.PublicID), logging.F("external_id", ev.User.ExternalId),
				logging.F("age_seconds", int64(age/time.Second))}}, nil
		}
	}
	return handled{}, nil
}

// tooOld reports whether ev happened more than MaxAge ago by the real clock, with its age.
func (e *Events) tooOld(ev Event) (time.Duration, bool) {
	maxAge := e.MaxAge
	if maxAge <= 0 {
		maxAge = EventMaxAge
	}
	age := e.Clocks.Real.Now().Sub(ev.OccurredAt)
	return age, age > maxAge
}
