// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
)

var eventTime = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// eventFixture is an event of type typ with every member it requires, changed by mutate.
func eventFixture(typ string, mutate ...func(map[string]any)) map[string]any {
	user := map[string]any{"external_id": "u-bob", "display_name": "Bob Brown", "handle": "bob"}
	m := map[string]any{"id": "ev-1", "type": typ, "occurred_at": eventTime.Format(time.RFC3339)}
	switch typ {
	case TypePress:
		m["message_id"], m["user"], m["data"] = "m1", user, "a1.k1.SIGNATURE"
	case TypeThreadMessage:
		m["message_id"], m["user"], m["text"], m["thread_ref"] = "m1", user, "ack", nil
	case TypeAccountLink:
		m["user"], m["token"] = user, strings.Repeat("A", 43)
	case TypeMessageDeleted:
		m["message_id"] = "m1"
	}
	for _, f := range mutate {
		f(m)
	}
	return m
}

func eventJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func member(key string, v any) func(map[string]any) {
	return func(m map[string]any) { m[key] = v }
}

func without(key string) func(map[string]any) {
	return func(m map[string]any) { delete(m, key) }
}

func userMember(key string, v any) func(map[string]any) {
	return func(m map[string]any) { m["user"].(map[string]any)[key] = v }
}

func withoutUser(key string) func(map[string]any) {
	return func(m map[string]any) { delete(m["user"].(map[string]any), key) }
}

// TestDecodeEvent covers C-22.FR-11: each of the four event types decodes with its members, unknown members ignored,
// and passes the schema Event of api/bridge.yaml.
func TestDecodeEvent(t *testing.T) {
	schema := bridgeSchema(t, "Event")
	ref := "t-7"
	for _, tc := range []struct {
		name string
		m    map[string]any
		want Event
	}{
		{"press", eventFixture(TypePress, member("future", map[string]any{"kept": false})),
			Event{ID: "ev-1", Type: TypePress, OccurredAt: eventTime, MessageID: "m1", Data: "a1.k1.SIGNATURE"}},
		{"press with empty data", eventFixture(TypePress, member("data", "")),
			Event{ID: "ev-1", Type: TypePress, OccurredAt: eventTime, MessageID: "m1"}},
		{"thread message", eventFixture(TypeThreadMessage, member("text", strings.Repeat("ё", maxText))),
			Event{ID: "ev-1", Type: TypeThreadMessage, OccurredAt: eventTime, MessageID: "m1",
				Text: strings.Repeat("ё", maxText)}},
		{"thread message with a thread_ref", eventFixture(TypeThreadMessage, member("thread_ref", ref)),
			Event{ID: "ev-1", Type: TypeThreadMessage, OccurredAt: eventTime, MessageID: "m1", Text: "ack",
				ThreadRef: &ref}},
		{"account link", eventFixture(TypeAccountLink, withoutUser("handle")),
			Event{ID: "ev-1", Type: TypeAccountLink, OccurredAt: eventTime, Token: strings.Repeat("A", 43)}},
		{"message deleted", eventFixture(TypeMessageDeleted, member("id", strings.Repeat("~", 200))),
			Event{ID: strings.Repeat("~", 200), Type: TypeMessageDeleted, OccurredAt: eventTime, MessageID: "m1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := eventJSON(t, tc.m)
			if err := visit(t, schema, raw); err != nil {
				t.Fatalf("the fixture does not match api/bridge.yaml: %v", err)
			}
			got, err := DecodeEvent(raw)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want.Type != TypeMessageDeleted && (got.User.ExternalId != "u-bob" ||
				got.User.DisplayName != "Bob Brown") {
				t.Errorf("user %+v", got.User)
			}
			got.User = tc.want.User
			if (got.ThreadRef == nil) != (tc.want.ThreadRef == nil) ||
				got.ThreadRef != nil && *got.ThreadRef != *tc.want.ThreadRef {
				t.Errorf("thread_ref %v", got.ThreadRef)
			}
			got.ThreadRef = tc.want.ThreadRef
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("decoded %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestDecodeEventRefuses covers C-22.FR-11: a body that is no event of the Bridge protocol is ErrNotAnEvent, whose
// text never carries a value of the body.
func TestDecodeEventRefuses(t *testing.T) {
	const secret = "whsec_c2VjcmV0"
	for _, tc := range []struct {
		name string
		raw  string
		m    map[string]any
	}{
		{name: "not JSON", raw: "not json " + secret},
		{name: "an array", raw: `["` + secret + `"]`},
		{name: "null", raw: "null"},
		{name: "a string", raw: `"` + secret + `"`},
		{name: "an unknown type", m: eventFixture(TypePress, member("type", "reaction"))},
		{name: "no type", m: eventFixture(TypePress, without("type"))},
		{name: "a type that is no string", m: eventFixture(TypePress, member("type", 1))},
		{name: "no id", m: eventFixture(TypePress, without("id"))},
		{name: "an id with a space", m: eventFixture(TypePress, member("id", "ev 1"))},
		{name: "an empty id", m: eventFixture(TypePress, member("id", ""))},
		{name: "a long id", m: eventFixture(TypePress, member("id", strings.Repeat("e", 201)))},
		{name: "an id that is no string", m: eventFixture(TypePress, member("id", 7))},
		{name: "a null id", m: eventFixture(TypePress, member("id", nil))},
		{name: "no occurred_at", m: eventFixture(TypePress, without("occurred_at"))},
		{name: "a null occurred_at", m: eventFixture(TypePress, member("occurred_at", nil))},
		{name: "an occurred_at that is no date-time", m: eventFixture(TypePress, member("occurred_at", secret))},
		{name: "a press without message_id", m: eventFixture(TypePress, without("message_id"))},
		{name: "a press with a null message_id", m: eventFixture(TypePress, member("message_id", nil))},
		{name: "a press without data", m: eventFixture(TypePress, without("data"))},
		{name: "a press with null data", m: eventFixture(TypePress, member("data", nil))},
		{name: "a press with data that is no string", m: eventFixture(TypePress, member("data", 5))},
		{name: "a press without user", m: eventFixture(TypePress, without("user"))},
		{name: "a press with a null user", m: eventFixture(TypePress, member("user", nil))},
		{name: "a user that is no object", m: eventFixture(TypePress, member("user", secret))},
		{name: "a user without external_id", m: eventFixture(TypePress, withoutUser("external_id"))},
		{name: "a user without display_name", m: eventFixture(TypePress, withoutUser("display_name"))},
		{name: "an empty external_id", m: eventFixture(TypePress, userMember("external_id", ""))},
		{name: "a long external_id", m: eventFixture(TypePress, userMember("external_id", strings.Repeat("u", 513)))},
		{name: "an empty display_name", m: eventFixture(TypePress, userMember("display_name", ""))},
		{name: "a long display_name",
			m: eventFixture(TypePress, userMember("display_name", strings.Repeat("я", 201)))},
		{name: "a long handle", m: eventFixture(TypePress, userMember("handle", strings.Repeat("h", 321)))},
		{name: "a handle that is no string", m: eventFixture(TypePress, userMember("handle", 1))},
		{name: "a thread message without thread_ref", m: eventFixture(TypeThreadMessage, without("thread_ref"))},
		{name: "a thread message with a thread_ref that is no string",
			m: eventFixture(TypeThreadMessage, member("thread_ref", 3))},
		{name: "a thread message without text", m: eventFixture(TypeThreadMessage, without("text"))},
		{name: "a long text", m: eventFixture(TypeThreadMessage, member("text", strings.Repeat("ё", maxText+1)))},
		{name: "an account link without token", m: eventFixture(TypeAccountLink, without("token"))},
		{name: "a short token", m: eventFixture(TypeAccountLink, member("token", strings.Repeat("A", 42)))},
		{name: "a token outside base64url", m: eventFixture(TypeAccountLink, member("token", strings.Repeat("+", 43)))},
		{name: "an account link without user", m: eventFixture(TypeAccountLink, without("user"))},
		{name: "a deletion without message_id", m: eventFixture(TypeMessageDeleted, without("message_id"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.raw)
			if tc.m != nil {
				raw = eventJSON(t, tc.m)
			}
			_, err := DecodeEvent(raw)
			if !errors.Is(err, ErrNotAnEvent) {
				t.Fatalf("decoded with %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error carries a value of the body: %v", err)
			}
		})
	}
}

// claimStore is the claim of event ids in memory, as internal/connections keeps it: a lock per Connection and event
// id, held while f runs, and the recorded ids. failRecord makes the record of an id fail that many times, as a crash
// between the Command and the record does; hold, when set, blocks every f until it is closed.
type claimStore struct {
	mu         sync.Mutex
	locks      map[string]*sync.Mutex
	seen       map[string]bool
	failRecord int
	running    int
	most       int
	hold       chan struct{}
}

func newClaimStore() *claimStore {
	return &claimStore{locks: map[string]*sync.Mutex{}, seen: map[string]bool{}}
}

func (s *claimStore) PushConnection(context.Context, string) (Conn, string, error) {
	return Conn{}, "", ErrNoConnection
}

func (s *claimStore) PollingConnections(context.Context) ([]Polled, error) { return nil, nil }

func (s *claimStore) StoreEventsCursor(context.Context, Conn, *string) error { return nil }

func (s *claimStore) key(connectionID int64, eventID string) string {
	return fmt.Sprintf("%d/%s", connectionID, eventID)
}

func (s *claimStore) HandleEventOnce(ctx context.Context, connectionID int64, eventID string,
	f func(context.Context) error) (bool, error) {
	k := s.key(connectionID, eventID)
	s.mu.Lock()
	l, ok := s.locks[k]
	if !ok {
		l = &sync.Mutex{}
		s.locks[k] = l
	}
	s.running++
	s.most = max(s.most, s.running)
	hold := s.hold
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running--
		s.mu.Unlock()
	}()
	if hold != nil {
		<-hold
	}
	l.Lock()
	defer l.Unlock()
	s.mu.Lock()
	seen := s.seen[k]
	s.mu.Unlock()
	if seen {
		return false, nil
	}
	if err := f(ctx); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRecord > 0 {
		s.failRecord--
		return false, errors.New("the database went away")
	}
	s.seen[k] = true
	return true, nil
}

func (s *claimStore) recorded(connectionID int64, eventID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[s.key(connectionID, eventID)]
}

// eventsEnv is an Events over a claimStore, with the real and the business clock at now and the log in a buffer; its
// presses are verified with keys and bound by bindings, its accounts linked by links, its Commands run by commands and
// its answers made on path, which takes every token at once, through the Connection's client, nil until a test sets
// one.
type eventsEnv struct {
	events   *Events
	store    *claimStore
	real     *clock.Manual
	log      *syncBuffer
	conn     Conn
	keys     *pressKeys
	bindings *pressBindings
	links    *pressLinks
	commands *pressCommands
	path     *answerPath
}

// syncBuffer is a log buffer that several goroutines write.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newEventsEnv(now time.Time) *eventsEnv {
	e := &eventsEnv{store: newClaimStore(), real: clock.NewManual(now), log: &syncBuffer{},
		conn: Conn{ID: pressConnID, PublicID: "CN0000000000B3", Version: 1}, keys: newPressKeys(),
		bindings: newPressBindings(), links: &pressLinks{users: pressUsers}, commands: &pressCommands{},
		path: newTestPath(now)}
	e.events = &Events{Store: e.store, Bindings: e.bindings, Links: e.links, Commands: e.commands, Roles: pressRoles,
		Keys: e.keys, Path: e.path, PublicURL: "http://localhost:8080/",
		Clocks: clock.Clocks{Business: clock.NewManual(now), Real: e.real}, Log: logging.New(e.log, logging.LevelInfo)}
	return e
}

func (e *eventsEnv) event(t *testing.T, typ string, mutate ...func(map[string]any)) Event {
	t.Helper()
	ev, err := DecodeEvent(eventJSON(t, eventFixture(typ, mutate...)))
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// TestEventsHandleEveryType covers C-22.FR-11: an event of each type of the protocol is handled and its id recorded,
// whatever the Connection's info declares, which Events never reads; an account_link event is acknowledged and changes
// nothing; an event handled before is acknowledged again without handling. The press and the Text command, on a
// message the Connection did not post, are the lines logged, and the message_deleted event reaches RootDeleted once.
func TestEventsHandleEveryType(t *testing.T) {
	e := newEventsEnv(eventTime)
	for i, typ := range []string{TypePress, TypeThreadMessage, TypeAccountLink, TypeMessageDeleted} {
		ev := e.event(t, typ, member("id", "ev-"+typ), member("message_id", "m9"))
		if err := e.events.Handle(t.Context(), e.conn, ev); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if !e.store.recorded(e.conn.ID, ev.ID) {
			t.Errorf("%d %s was not recorded", i, typ)
		}
		if err := e.events.Handle(t.Context(), e.conn, ev); err != nil {
			t.Errorf("%s again: %v", typ, err)
		}
	}
	if got := e.log.String(); strings.Count(got, "\n") != 2 ||
		!strings.Contains(got, pressLine("", "", "unknown_message", "u-bob")+"}") ||
		!strings.Contains(got, textLine("", "acknowledge", "unknown_message", "u-bob")+"}") {
		t.Errorf("logged %s", got)
	}
	if d := e.bindings.deletions(); !slices.Equal(d, []string{"3/m9"}) {
		t.Errorf("RootDeleted for %v", d)
	}
}

// TestEventsDropAnOldPress covers C-22.FR-12: a press older than bridge.event_max_age by the real clock is dropped,
// recorded and logged as bridge_press_dropped once its claim committed, once however often it comes; a press at the
// limit is not, and MaxAge replaces the default.
func TestEventsDropAnOldPress(t *testing.T) {
	e := newEventsEnv(eventTime.Add(EventMaxAge + 90*time.Second))
	ev := e.event(t, TypePress)
	for range 2 {
		if err := e.events.Handle(t.Context(), e.conn, ev); err != nil {
			t.Fatal(err)
		}
	}
	want := `"event":"bridge_press_dropped","connection":"CN0000000000B3","external_id":"u-bob","age_seconds":3690`
	if got := e.log.String(); strings.Count(got, want) != 1 || strings.Count(got, "\n") != 1 {
		t.Errorf("logged %s", got)
	}
	if !e.store.recorded(e.conn.ID, ev.ID) {
		t.Error("a dropped press was not recorded")
	}
	e = newEventsEnv(eventTime.Add(EventMaxAge))
	if err := e.events.Handle(t.Context(), e.conn, e.event(t, TypePress, member("message_id", "m9"))); err != nil ||
		strings.Contains(e.log.String(), "bridge_press_dropped") ||
		!strings.Contains(e.log.String(), `"outcome":"unknown_message"`) {
		t.Errorf("a press at the limit = %v, logged %s", err, e.log.String())
	}
	e = newEventsEnv(eventTime.Add(time.Minute + time.Second))
	e.events.MaxAge = time.Minute
	if err := e.events.Handle(t.Context(), e.conn, e.event(t, TypePress)); err != nil ||
		!strings.Contains(e.log.String(), `"age_seconds":61`) {
		t.Errorf("a press past MaxAge = %v, logged %s", err, e.log.String())
	}
	e = newEventsEnv(eventTime.Add(2 * EventMaxAge))
	if err := e.events.Handle(t.Context(), e.conn, e.event(t, TypeThreadMessage)); err != nil ||
		strings.Contains(e.log.String(), "bridge_press_dropped") {
		t.Errorf("an old Thread message = %v, logged %s", err, e.log.String())
	}
}

// TestTwoCopiesRunOneAfterTheOther covers C-22.FR-11: two copies of one event handled at the same time — a push
// retried while the first runs — wait on the claim, so that only the first is handled.
func TestTwoCopiesRunOneAfterTheOther(t *testing.T) {
	e := newEventsEnv(eventTime.Add(2 * EventMaxAge))
	e.store.hold = make(chan struct{})
	ev := e.event(t, TypePress)
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- e.events.Handle(t.Context(), e.conn, ev) }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.store.mu.Lock()
		running := e.store.running
		e.store.mu.Unlock()
		if running == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the two copies did not reach the claim together")
		}
		time.Sleep(time.Millisecond)
	}
	close(e.store.hold)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(e.log.String(), `"event":"bridge_press_dropped"`); n != 1 {
		t.Errorf("handled %d times:\n%s", n, e.log.String())
	}
}

// TestCrashBeforeTheRecordRepeatsTheEvent covers C-22.FR-11: when the record of the id fails after the event was
// dispatched, as a crash between the Command and the record does, the event fails with bridge_event_failed, is not
// recorded, and its redelivery dispatches it again; its line is logged only once its claim committed.
func TestCrashBeforeTheRecordRepeatsTheEvent(t *testing.T) {
	e := newEventsEnv(eventTime.Add(2 * EventMaxAge))
	e.store.failRecord = 1
	ev := e.event(t, TypePress)
	err := e.events.Handle(t.Context(), e.conn, ev)
	if err == nil || !strings.Contains(err.Error(), "handle the press event ev-1 of the connection CN0000000000B3") {
		t.Fatalf("the first delivery = %v", err)
	}
	want := `"level":"WARN","event":"bridge_event_failed","connection":"CN0000000000B3","event_id":"ev-1",` +
		`"type":"press","error":"the database went away"`
	if got := e.log.String(); !strings.Contains(got, want) || strings.Contains(got, "bridge_press_dropped") ||
		e.store.recorded(e.conn.ID, ev.ID) {
		t.Fatalf("after the first delivery, logged %s", got)
	}
	if err := e.events.Handle(t.Context(), e.conn, ev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.log.String(), "bridge_press_dropped") || !e.store.recorded(e.conn.ID, ev.ID) {
		t.Errorf("the redelivery was not handled: %s", e.log.String())
	}
}

// TestEventsEndedContextFails: an event whose context ended before it was dispatched fails and is not recorded.
func TestEventsEndedContextFails(t *testing.T) {
	e := newEventsEnv(eventTime)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ev := e.event(t, TypeMessageDeleted)
	if err := e.events.Handle(ctx, e.conn, ev); !errors.Is(err, context.Canceled) ||
		e.store.recorded(e.conn.ID, ev.ID) {
		t.Errorf("handled with %v", err)
	}
}

// TestParallelEventsBound covers bridge.parallel_events: at most Parallel events are handled at a time, ParallelEvents
// when it is zero, and an event that finds no free slot waits for one, or fails when its context ends first.
func TestParallelEventsBound(t *testing.T) {
	for _, tc := range []struct {
		parallel, want int
	}{{0, ParallelEvents}, {1, 1}, {3, 3}} {
		e := newEventsEnv(eventTime)
		e.events.Parallel = tc.parallel
		e.store.hold = make(chan struct{})
		parked := make(chan struct{}, tc.want+1)
		e.events.parked = func() { parked <- struct{}{} }
		errs := make(chan error, tc.want+1)
		for i := range tc.want + 1 {
			ev := e.event(t, TypeMessageDeleted, member("id", "ev-"+string(rune('a'+i))))
			go func() { errs <- e.events.Handle(t.Context(), e.conn, ev) }()
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			e.store.mu.Lock()
			running := e.store.running
			e.store.mu.Unlock()
			if running == tc.want {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("parallel %d: %d events reached the claim", tc.parallel, running)
			}
			time.Sleep(time.Millisecond)
		}
		// The event beyond the bound waits for a slot, and so has not reached the claim.
		<-parked
		e.store.mu.Lock()
		running := e.store.running
		e.store.mu.Unlock()
		if running != tc.want {
			t.Fatalf("parallel %d: %d events at the claim while one waits for a slot", tc.parallel, running)
		}
		close(e.store.hold)
		for range tc.want + 1 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if e.store.most != tc.want {
			t.Errorf("parallel %d: %d events at a time, want %d", tc.parallel, e.store.most, tc.want)
		}
	}

	e := newEventsEnv(eventTime)
	e.events.Parallel = 1
	e.store.hold = make(chan struct{})
	// The waiting event's context ends once it waits for a slot.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.events.parked = cancel
	done := make(chan error, 1)
	go func() { done <- e.events.Handle(t.Context(), e.conn, e.event(t, TypeMessageDeleted)) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.store.mu.Lock()
		running := e.store.running
		e.store.mu.Unlock()
		if running == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first event did not reach the claim")
		}
		time.Sleep(time.Millisecond)
	}
	waiting := e.event(t, TypeMessageDeleted, member("id", "ev-waiting"))
	if err := e.events.Handle(ctx, e.conn, waiting); !errors.Is(err, context.Canceled) ||
		e.store.recorded(e.conn.ID, waiting.ID) {
		t.Errorf("an event without a slot = %v", err)
	}
	close(e.store.hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestMessageDeletedCallsRootDeleted covers C-22.FR-10 and AC-17 on the event side: a message_deleted event hands the
// Connection and the message to delivery's deleted Root message flow at once, whatever the message and however old the
// event, and is recorded, answered by nothing and logged by nothing; delivery decides that only the current Root
// message of an open delivery changes anything. A flow that fails fails the event, which is not recorded and runs the
// flow again on redelivery.
func TestMessageDeletedCallsRootDeleted(t *testing.T) {
	e := newEventsEnv(eventTime.Add(2 * EventMaxAge))
	for i, m := range []string{"m1", "m0", "r1"} {
		e.handle(t, e.event(t, TypeMessageDeleted, member("id", fmt.Sprintf("ev-%d", i)), member("message_id", m)))
		if !e.store.recorded(e.conn.ID, fmt.Sprintf("ev-%d", i)) {
			t.Errorf("the deletion of %s was not recorded", m)
		}
	}
	if d := e.bindings.deletions(); !slices.Equal(d, []string{"3/m1", "3/m0", "3/r1"}) {
		t.Errorf("RootDeleted for %v", d)
	}
	if e.log.String() != "" || e.path.calls() != 0 || len(e.commands.ran()) != 0 {
		t.Errorf("logged %s, %d answers, commands %+v", e.log.String(), e.path.calls(), e.commands.ran())
	}
	e.bindings.mu.Lock()
	e.bindings.deleteErr = errors.New("the database is down")
	e.bindings.mu.Unlock()
	ev := e.event(t, TypeMessageDeleted, member("id", "ev-x"))
	if err := e.events.Handle(t.Context(), e.conn, ev); err == nil || e.store.recorded(e.conn.ID, "ev-x") ||
		!strings.Contains(e.log.String(), `"event":"bridge_event_failed","connection":"CN0000000000B3",`+
			`"event_id":"ev-x","type":"message_deleted","error":"`) {
		t.Errorf("a failed flow = %v, logged %s", err, e.log.String())
	}
	e.bindings.mu.Lock()
	e.bindings.deleteErr = nil
	e.bindings.mu.Unlock()
	e.handle(t, ev)
	if d := e.bindings.deletions(); len(d) != 5 || d[4] != "3/m1" || !e.store.recorded(e.conn.ID, "ev-x") {
		t.Errorf("redelivered: RootDeleted for %v", d)
	}
}
