// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
)

// eventually waits until cond holds, polling every few milliseconds, and fails the test after five seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen in time", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// eventsBridge is a bridge that answers pollEvents from a queue of events, as the protocol says: the cursor is the
// index of the next event, a cursor it does not know is invalid, and a call that finds no event waits for one until
// its wait ends or its request is cancelled. With hold, every call waits until its request is cancelled; with fail, every
// call is answered fail's status with a problem of class failClass. It records the query of each call and how many
// requests were cancelled while it held them, and answers answerEvent, counting the answers.
type eventsBridge struct {
	srv  *httptest.Server
	done chan struct{}

	answered int

	mu        sync.Mutex
	events    []json.RawMessage
	added     chan struct{}
	queries   []url.Values
	cancelled int
	hold      bool
	fail      int
	failClass string
	raw       string
}

func startEventsBridge(t *testing.T) *eventsBridge {
	t.Helper()
	b := &eventsBridge{done: make(chan struct{}), added: make(chan struct{})}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	t.Cleanup(func() { close(b.done) })
	return b
}

func (b *eventsBridge) add(raw ...[]byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range raw {
		b.events = append(b.events, r)
	}
	close(b.added)
	b.added = make(chan struct{})
}

func (b *eventsBridge) set(f func(b *eventsBridge)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f(b)
}

func (b *eventsBridge) calls() []url.Values {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.queries)
}

func (b *eventsBridge) cancels() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cancelled
}

func (b *eventsBridge) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == "/v1/answers" {
		b.mu.Lock()
		b.answered++
		b.mu.Unlock()
		answerJSON(http.StatusOK, "application/json", `{"shown_as":"thread","replayed":false}`)(w, r)
		return
	}
	if r.Method != http.MethodGet || r.URL.Path != "/v1/events" ||
		r.Header.Get("Authorization") != "Bearer "+testToken {
		answerJSON(http.StatusNotFound, problemJSON, `{"class":"invalid","title":"not found"}`)(w, r)
		return
	}
	q := r.URL.Query()
	b.mu.Lock()
	b.queries = append(b.queries, q)
	hold, fail, failClass, raw := b.hold, b.fail, b.failClass, b.raw
	b.mu.Unlock()
	switch {
	case hold:
		b.waitCancelled(r)
		return
	case fail != 0:
		answerJSON(fail, problemJSON, fmt.Sprintf(`{"class":%q,"detail":"refused %s"}`, failClass, testToken))(w, r)
		return
	case raw != "":
		answerJSON(http.StatusOK, "application/json", raw)(w, r)
		return
	}
	from := 0
	if c, ok := q["cursor"]; ok {
		n, err := strconv.Atoi(c[0])
		b.mu.Lock()
		known := err == nil && n >= 0 && n <= len(b.events)
		b.mu.Unlock()
		if !known {
			answerJSON(http.StatusBadRequest, problemJSON, `{"class":"invalid","detail":"unknown cursor"}`)(w, r)
			return
		}
		from = n
	}
	wait, _ := strconv.Atoi(q.Get("wait"))
	timer := time.NewTimer(time.Duration(wait) * time.Second)
	defer timer.Stop()
	for {
		b.mu.Lock()
		events, added := b.events[min(from, len(b.events)):], b.added
		next := len(b.events)
		b.mu.Unlock()
		if len(events) > 0 {
			body, _ := json.Marshal(map[string]any{"events": events, "next_cursor": strconv.Itoa(next)})
			answerJSON(http.StatusOK, "application/json", string(body))(w, r)
			return
		}
		select {
		case <-added:
		case <-timer.C:
			answerJSON(http.StatusOK, "application/json", fmt.Sprintf(`{"events":[],"next_cursor":"%d"}`, next))(w, r)
			return
		case <-r.Context().Done():
			b.mu.Lock()
			b.cancelled++
			b.mu.Unlock()
			return
		case <-b.done:
			return
		}
	}
}

// waitCancelled holds the request r until it is cancelled, and counts it.
func (b *eventsBridge) waitCancelled(r *http.Request) {
	select {
	case <-r.Context().Done():
		b.mu.Lock()
		b.cancelled++
		b.mu.Unlock()
	case <-b.done:
	}
}

// pollSource lists the Connections to poll with their stored cursors and stores the cursors, as internal/connections
// does: a cursor of a Connection whose version changed is not stored. It records every store.
type pollSource struct {
	mu        sync.Mutex
	conns     []Polled
	cursors   map[int64]*string
	stores    []*string
	reads     int
	failList  error
	failStore int
}

func (s *pollSource) PushConnection(context.Context, string) (Conn, string, error) {
	return Conn{}, "", ErrNoConnection
}

func (s *pollSource) HandleEventOnce(context.Context, int64, string, func(context.Context) error) (bool, error) {
	return false, errors.New("the poller claims nothing itself")
}

func (s *pollSource) PollingConnections(context.Context) ([]Polled, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.failList != nil {
		return nil, s.failList
	}
	out := make([]Polled, 0, len(s.conns))
	for _, c := range s.conns {
		c.Cursor = s.cursors[c.Conn.ID]
		out = append(out, c)
	}
	return out, nil
}

func (s *pollSource) StoreEventsCursor(_ context.Context, c Conn, cursor *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failStore > 0 {
		s.failStore--
		return errors.New("the database went away")
	}
	s.stores = append(s.stores, cursor)
	for _, l := range s.conns {
		if l.Conn.ID == c.ID && l.Conn.Version == c.Version {
			s.cursors[c.ID] = cursor
		}
	}
	return nil
}

func (s *pollSource) set(conns ...Polled) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns = conns
}

func (s *pollSource) stored() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.stores))
	for _, c := range s.stores {
		if c == nil {
			out = append(out, "<nil>")
			continue
		}
		out = append(out, *c)
	}
	return out
}

func (s *pollSource) cursor(id int64) *string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[id]
}

// pollEnv is a Poller over an eventsBridge, a pollSource and Events over a claimStore, whose real clock is two hours
// past the events, so that each press it handles logs bridge_press_dropped once, which shows how often it was handled.
// The back-offs are recorded instead of slept, and the jitter is the most it can be, so that a back-off is its bound.
type pollEnv struct {
	*eventsEnv
	bridge *eventsBridge
	source *pollSource
	polled Polled

	mu     sync.Mutex
	pauses []time.Duration
}

func newPollEnv(t *testing.T) *pollEnv {
	t.Helper()
	e := &pollEnv{eventsEnv: newEventsEnv(eventTime.Add(2 * EventMaxAge)), bridge: startEventsBridge(t)}
	e.conn.Client = newClient(t, network(t, nil), Settings{BaseURL: e.bridge.srv.URL, Token: testToken})
	e.polled = Polled{Conn: e.conn}
	e.source = &pollSource{cursors: map[int64]*string{}}
	e.source.set(e.polled)
	return e
}

func (e *pollEnv) poller() *Poller {
	return &Poller{Source: e.source, Events: e.events, Log: e.events.Log, Every: time.Hour,
		jitter: func(d time.Duration) time.Duration { return d },
		sleep: func(ctx context.Context, d time.Duration) {
			e.mu.Lock()
			e.pauses = append(e.pauses, d)
			e.mu.Unlock()
			t := time.NewTimer(time.Millisecond)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}}
}

func (e *pollEnv) waits() []time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.pauses)
}

// press is a press of the account externalID with the id id, two hours old.
func (e *pollEnv) press(t *testing.T, id, externalID string) []byte {
	t.Helper()
	return eventJSON(t, eventFixture(TypePress, member("id", id), userMember("external_id", externalID)))
}

// dropped counts the bridge_press_dropped lines of the account externalID.
func (e *pollEnv) dropped(externalID string) int {
	return strings.Count(e.log.String(), `"event":"bridge_press_dropped","connection":"CN0000000000B3",`+
		`"external_id":"`+externalID+`"`)
}

// runPoller runs p as a Leader would until stop is called, which waits for Run and returns its error.
func runPoller(t *testing.T, p *Poller) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	return func() error {
		cancel()
		return <-done
	}
}

// TestPollerHandlesAPressWithinOnePoll covers C-22.FR-11 and AC-13: in poll mode the Leader calls pollEvents with the
// wait bridge.poll_wait and, with nothing stored, without a cursor; a press the bridge queued is handled within that
// one poll, and next_cursor is stored after the batch and sent with the next call.
func TestPollerHandlesAPressWithinOnePoll(t *testing.T) {
	e := newPollEnv(t)
	e.bridge.add(e.press(t, "ev-1", "u-bob"))
	stop := runPoller(t, e.poller())
	eventually(t, "the second poll", func() bool { return len(e.bridge.calls()) == 2 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	calls := e.bridge.calls()
	if calls[0].Get("wait") != "25" || calls[0].Has("cursor") || len(calls[0]) != 1 {
		t.Errorf("the first poll %v", calls[0])
	}
	if calls[1].Get("wait") != "25" || calls[1].Get("cursor") != "1" {
		t.Errorf("the second poll %v", calls[1])
	}
	if !e.store.recorded(e.conn.ID, "ev-1") || e.dropped("u-bob") != 1 {
		t.Errorf("the press was not handled once: %s", e.log.String())
	}
	if got := e.source.stored(); !slices.Equal(got, []string{"1"}) {
		t.Errorf("stored cursors %v", got)
	}
	eventually(t, "the bridge seeing the held poll cancelled", func() bool { return e.bridge.cancels() == 1 })
	if len(e.waits()) != 0 {
		t.Errorf("paused %v", e.waits())
	}
}

// answeredSource is a pollSource that records, at each store of a cursor, how many answers the bridge had received.
type answeredSource struct {
	*pollSource
	bridge *eventsBridge

	mu      sync.Mutex
	answers []int
}

func (s *answeredSource) StoreEventsCursor(ctx context.Context, c Conn, cursor *string) error {
	s.bridge.mu.Lock()
	n := s.bridge.answered
	s.bridge.mu.Unlock()
	s.mu.Lock()
	s.answers = append(s.answers, n)
	s.mu.Unlock()
	return s.pollSource.StoreEventsCursor(ctx, c, cursor)
}

func (s *answeredSource) seen() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.answers)
}

// TestAnswerBeforeTheCursor covers C-22.FR-11 and FR-13 in poll mode: an event is answered once its claim committed and
// before the cursor after it is stored, which acknowledges it.
func TestAnswerBeforeTheCursor(t *testing.T) {
	e := newPollEnv(t)
	now := eventTime.Add(2 * EventMaxAge)
	e.bridge.add(eventJSON(t, eventFixture(TypePress, member("occurred_at", now.Format(time.RFC3339)),
		member("data", e.sign(t, buttons.SubjectTest, pressDest.PublicID, buttons.CommandAcknowledge, 0)))))
	src := &answeredSource{pollSource: e.source, bridge: e.bridge}
	p := e.poller()
	p.Source = src
	stop := runPoller(t, p)
	eventually(t, "the cursor", func() bool { return len(src.seen()) == 1 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if got := src.seen(); !slices.Equal(got, []int{1}) || !strings.Contains(e.log.String(), `"outcome":"test"`) {
		t.Errorf("answers at each store %v, logged %s", got, e.log.String())
	}
}

// TestPollerSendsTheStoredCursor covers C-22.FR-11: a new Leader polls with the cursor stored on the Connection and
// handles only the events after it; a batch that brings nothing new stores nothing.
func TestPollerSendsTheStoredCursor(t *testing.T) {
	e := newPollEnv(t)
	e.bridge.add(e.press(t, "ev-1", "u-alice"), e.press(t, "ev-2", "u-bob"))
	one := "1"
	e.source.cursors[e.conn.ID] = &one
	stop := runPoller(t, e.poller())
	eventually(t, "the second poll", func() bool { return len(e.bridge.calls()) == 2 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if got := e.bridge.calls()[0].Get("cursor"); got != "1" {
		t.Errorf("the first poll sent the cursor %q", got)
	}
	if e.dropped("u-alice") != 0 || e.dropped("u-bob") != 1 {
		t.Errorf("handled %s", e.log.String())
	}
	if got := e.source.stored(); !slices.Equal(got, []string{"2"}) {
		t.Errorf("stored cursors %v", got)
	}

	// A bridge that answers at once without events gets the same cursor back and nothing is stored; the poller pauses
	// a second before it polls again.
	e = newPollEnv(t)
	e.bridge.set(func(b *eventsBridge) { b.raw = `{"events":[],"next_cursor":"9"}` })
	nine := "9"
	e.source.cursors[e.conn.ID] = &nine
	stop = runPoller(t, e.poller())
	eventually(t, "three polls", func() bool { return len(e.bridge.calls()) >= 3 })
	_ = stop()
	if got := e.source.stored(); len(got) != 0 {
		t.Errorf("stored cursors %v", got)
	}
	if w := e.waits(); len(w) < 2 || w[0] != emptyPause || w[1] != emptyPause {
		t.Errorf("pauses %v", w)
	}
}

// TestPollerCallDeadline covers C-22.FR-11 and bridge.poll_wait: each call gets bridge.poll_wait plus 15 s, a shorter
// Wait shortens both, and a longer one is bridge.poll_wait; pollEvents goes through the background class's long-poll
// client, whose timeout is no shorter than that deadline.
func TestPollerCallDeadline(t *testing.T) {
	for _, tc := range []struct {
		wait, want time.Duration
	}{{0, PollWait}, {3 * time.Second, 3 * time.Second}, {90 * time.Second, PollWait}} {
		e := newPollEnv(t)
		p := e.poller()
		p.Wait = tc.wait
		type call struct {
			left time.Duration
			wait time.Duration
			ok   bool
		}
		calls := make(chan call, 1)
		p.poll = func(ctx context.Context, _ Conn, _ *string, wait time.Duration) (Batch, Result) {
			deadline, ok := ctx.Deadline()
			select {
			case calls <- call{left: time.Until(deadline), wait: wait, ok: ok}:
			default:
			}
			<-ctx.Done()
			return Batch{}, Result{Classification: Classification{Class: ClassTransient}}
		}
		stop := runPoller(t, p)
		got := <-calls
		_ = stop()
		limit := tc.want + PollMargin
		if !got.ok || got.wait != tc.want || got.left > limit || got.left < limit-time.Second {
			t.Errorf("Wait %v: the call waited %v with %v left of its deadline (%v)", tc.wait, got.wait, got.left,
				got.ok)
		}
	}
	long := clientKey{class: outbound.ClassBackground, long: true}
	if long.timeout() != PollWait+PollMargin || (clientKey{class: outbound.ClassBackground}).timeout() != callTimeout {
		t.Errorf("timeouts %v and %v", long.timeout(), clientKey{}.timeout())
	}
	e := newPollEnv(t)
	e.bridge.set(func(b *eventsBridge) { b.raw = `{"events":[],"next_cursor":"0"}` })
	if _, r := e.conn.Client.PollEvents(t.Context(), nil, PollWait); !r.OK() {
		t.Fatalf("poll = %+v", r)
	}
	e.conn.Client.mu.Lock()
	_, made := e.conn.Client.clients[long]
	e.conn.Client.mu.Unlock()
	if !made {
		t.Error("pollEvents did not go through the long-poll client of the background class")
	}
}

// TestPollEventsAnswers: pollEvents decodes a batch as api/bridge.yaml's EventBatch has it, each event as sent, and an
// answer without an array of events or without a next_cursor of at most 1,024 characters is invalid, like an answer
// that is not JSON; a refusal keeps the bridge's class, masked of the token, and its status.
func TestPollEventsAnswers(t *testing.T) {
	e := newPollEnv(t)
	cursor := "a b&c"
	e.bridge.set(func(b *eventsBridge) { b.raw = `{"events":[{"id":"x"},7],"next_cursor":"n1","more":true}` })
	batch, r := e.conn.Client.PollEvents(t.Context(), &cursor, 7*time.Second)
	if !r.OK() || len(batch.Events) != 2 || string(batch.Events[0]) != `{"id":"x"}` || string(batch.Events[1]) != "7" ||
		batch.NextCursor != "n1" {
		t.Fatalf("batch %+v, %+v", batch, r)
	}
	if q := e.bridge.calls()[0]; q.Get("cursor") != cursor || q.Get("wait") != "7" {
		t.Errorf("query %v", q)
	}
	for _, raw := range []string{`{"events":null,"next_cursor":"1"}`, `{"next_cursor":"1"}`, `{"events":[]}`,
		`{"events":[],"next_cursor":null}`, `{"events":{},"next_cursor":"1"}`, `[]`, `not json`,
		`{"events":[],"next_cursor":"` + strings.Repeat("c", maxCursor+1) + `"}`} {
		e.bridge.set(func(b *eventsBridge) { b.raw = raw })
		if _, r := e.conn.Client.PollEvents(t.Context(), nil, PollWait); r.OK() ||
			r.Classification.Class != ClassInvalid || refusedCursor(r) ||
			!strings.HasPrefix(string(r.Error), "GET events: ") {
			t.Errorf("%s = %+v", raw, r)
		}
	}
	e.bridge.set(func(b *eventsBridge) { b.raw = `{"events":[],"next_cursor":"` + strings.Repeat("с", maxCursor) + `"}` })
	if _, r := e.conn.Client.PollEvents(t.Context(), nil, PollWait); !r.OK() {
		t.Errorf("a cursor of 1,024 characters = %+v", r)
	}
	e.bridge.set(func(b *eventsBridge) { b.raw, b.fail, b.failClass = "", http.StatusBadRequest, "invalid" })
	if _, r := e.conn.Client.PollEvents(t.Context(), nil, PollWait); r.Status != http.StatusBadRequest ||
		!refusedCursor(r) || strings.Contains(string(r.Error), testToken) {
		t.Errorf("a refusal = %+v", r)
	}
}

// TestPollerOverlappingPollHandledOnce covers C-22.FR-11 and ADR-0007: a frozen old Leader and its successor polling at
// once both receive the same events, and each event is handled once, by its id.
func TestPollerOverlappingPollHandledOnce(t *testing.T) {
	e := newPollEnv(t)
	e.store.hold = make(chan struct{})
	e.bridge.add(e.press(t, "ev-1", "u-1"), e.press(t, "ev-2", "u-2"), e.press(t, "ev-3", "u-3"))
	successor := &Events{Store: e.store, Clocks: e.events.Clocks, Log: e.events.Log}
	old, next := e.poller(), e.poller()
	next.Events = successor
	stopOld, stopNext := runPoller(t, old), runPoller(t, next)
	// Both Leaders hold the first event of the same batch at the claim.
	eventually(t, "both Leaders at the claim", func() bool {
		e.store.mu.Lock()
		defer e.store.mu.Unlock()
		return e.store.running == 2
	})
	close(e.store.hold)
	eventually(t, "the three events", func() bool {
		return e.store.recorded(e.conn.ID, "ev-1") && e.store.recorded(e.conn.ID, "ev-2") &&
			e.store.recorded(e.conn.ID, "ev-3")
	})
	eventually(t, "both Leaders polling again", func() bool { return len(e.bridge.calls()) >= 4 })
	if err := errors.Join(stopOld(), stopNext()); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"u-1", "u-2", "u-3"} {
		if n := e.dropped(u); n != 1 {
			t.Errorf("the press of %s was handled %d times", u, n)
		}
	}
	for _, q := range e.bridge.calls()[:2] {
		if q.Has("cursor") {
			t.Errorf("a first poll %v", q)
		}
	}
}

// TestPollerDropsAnInvalidCursor covers C-22.FR-11: a cursor the bridge refuses as invalid is dropped from the
// Connection at once, without a back-off or a failure, and the next poll starts without one.
func TestPollerDropsAnInvalidCursor(t *testing.T) {
	e := newPollEnv(t)
	e.bridge.add(e.press(t, "ev-1", "u-bob"))
	seven := "7"
	e.source.cursors[e.conn.ID] = &seven
	stop := runPoller(t, e.poller())
	eventually(t, "the third poll", func() bool { return len(e.bridge.calls()) == 3 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	calls := e.bridge.calls()
	if calls[0].Get("cursor") != "7" || calls[1].Has("cursor") || calls[2].Get("cursor") != "1" {
		t.Errorf("polls %v", calls)
	}
	if got := e.source.stored(); !slices.Equal(got, []string{"<nil>", "1"}) {
		t.Errorf("stored cursors %v", got)
	}
	if e.dropped("u-bob") != 1 || strings.Contains(e.log.String(), "bridge_poll_failed") || len(e.waits()) != 0 {
		t.Errorf("logged %s, paused %v", e.log.String(), e.waits())
	}

	// Refused without a cursor, the call is a failure.
	e = newPollEnv(t)
	e.bridge.set(func(b *eventsBridge) { b.fail, b.failClass = http.StatusBadRequest, "invalid" })
	stop = runPoller(t, e.poller())
	eventually(t, "a failure", func() bool { return strings.Contains(e.log.String(), "bridge_poll_failed") })
	_ = stop()
	if got := e.source.stored(); len(got) != 0 {
		t.Errorf("stored cursors %v", got)
	}
}

// TestPollerBacksOffPerCall covers C-22.FR-11, AC-13 and bridge.poll_wait: each failed call of the background client
// class backs off, from 1 s doubling up to 1 min, counted per call, and logs bridge_poll_failed — masked of the token —
// at the first failure in a row and every tenth only; it neither stores nor drops the cursor.
func TestPollerBacksOffPerCall(t *testing.T) {
	e := newPollEnv(t)
	e.bridge.set(func(b *eventsBridge) { b.fail, b.failClass = http.StatusUnauthorized, "forbidden" })
	five := "5"
	e.source.cursors[e.conn.ID] = &five
	counter := metrics.ClientRequests.With(string(outbound.ClassBackground), string(outbound.OutcomeFatal))
	before := counter.Get()
	stop := runPoller(t, e.poller())
	eventually(t, "21 failures", func() bool { return len(e.waits()) >= 21 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	s := time.Second
	want := []time.Duration{s, 2 * s, 4 * s, 8 * s, 16 * s, 32 * s, time.Minute, time.Minute}
	if w := e.waits(); !slices.Equal(w[:len(want)], want) || w[20] != time.Minute {
		t.Errorf("back-offs %v", w)
	}
	calls := e.bridge.calls()
	// The last call may end with the stop instead.
	if got := counter.Get() - before; got < 21 || got > uint64(len(calls)) || uint64(len(calls)) > got+1 {
		t.Errorf("%d background requests for %d calls", got, len(calls))
	}
	for _, q := range calls {
		if q.Get("cursor") != "5" {
			t.Fatalf("a poll %v", q)
		}
	}
	var failures []float64
	for line := range strings.Lines(e.log.String()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil || m["event"] != "bridge_poll_failed" {
			continue
		}
		if m["level"] != "WARN" || m["connection"] != "CN0000000000B3" || m["error"] != "refused [redacted]" {
			t.Errorf("line %v", m)
		}
		failures = append(failures, m["failures"].(float64))
	}
	if len(failures) < 3 || !slices.Equal(failures[:3], []float64{1, 10, 20}) {
		t.Errorf("logged the failures %v", failures)
	}
	if strings.Contains(e.log.String(), testToken) || len(e.source.stored()) != 0 {
		t.Errorf("stored %v, logged %s", e.source.stored(), e.log.String())
	}
}

// TestPollerStopsAtOnce covers C-22.FR-11 and ADR-0007 (M12): every call runs under the task's context, so losing
// leadership or stopping the replica cancels a long poll in flight at once — the bridge sees its request cancelled —
// and Run returns well within a second.
func TestPollerStopsAtOnce(t *testing.T) {
	e := newPollEnv(t)
	e.bridge.set(func(b *eventsBridge) { b.hold = true })
	stop := runPoller(t, e.poller())
	eventually(t, "a long poll in flight", func() bool { return len(e.bridge.calls()) == 1 })
	start := time.Now()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("Run took %v to return", took)
	}
	eventually(t, "the bridge seeing the poll cancelled", func() bool { return e.bridge.cancels() == 1 })
	if strings.Contains(e.log.String(), "bridge_poll_failed") || strings.Contains(e.log.String(), "bridge_event_failed") {
		t.Errorf("a stop logged %s", e.log.String())
	}

	// A loop waiting for a slot of Events stops as soon, and so does one in a back-off.
	e = newPollEnv(t)
	e.events.Parallel = 1
	e.store.hold = make(chan struct{})
	busy := make(chan error, 1)
	other := e.event(t, TypeMessageDeleted, member("id", "ev-busy"))
	go func() { busy <- e.events.Handle(t.Context(), e.conn, other) }()
	eventually(t, "the slot taken", func() bool {
		e.store.mu.Lock()
		defer e.store.mu.Unlock()
		return e.store.running == 1
	})
	e.bridge.add(e.press(t, "ev-1", "u-1"))
	stop = runPoller(t, e.poller())
	eventually(t, "the poll", func() bool { return len(e.bridge.calls()) == 1 })
	start = time.Now()
	_ = stop()
	if took := time.Since(start); took > 500*time.Millisecond || e.store.recorded(e.conn.ID, "ev-1") {
		t.Errorf("Run took %v to return", took)
	}
	close(e.store.hold)
	if err := <-busy; err != nil {
		t.Fatal(err)
	}

	e = newPollEnv(t)
	e.bridge.set(func(b *eventsBridge) { b.fail, b.failClass = http.StatusUnauthorized, "forbidden" })
	p := e.poller()
	p.sleep = nil
	stop = runPoller(t, p)
	eventually(t, "a back-off", func() bool { return strings.Contains(e.log.String(), "bridge_poll_failed") })
	start = time.Now()
	_ = stop()
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("Run took %v to return from a back-off of a second", took)
	}
}

// TestPollerFollowsTheConnections covers C-22.FR-11: a loop starts for each Connection in poll mode, starts again when
// the Connection's version changes, stops when it leaves poll mode or is deleted — its poll in flight cancelled — and a
// wake reads the Connections at once; a failed read stops every loop and returns its error.
func TestPollerFollowsTheConnections(t *testing.T) {
	e := newPollEnv(t)
	e.bridge.set(func(b *eventsBridge) { b.hold = true })
	p := e.poller()
	stop := runPoller(t, p)
	eventually(t, "the first poll", func() bool { return len(e.bridge.calls()) == 1 })
	moved := e.polled
	moved.Conn.Version = 2
	e.source.set(moved)
	p.Wake()
	p.Wake()
	eventually(t, "the poll of the new version", func() bool {
		return len(e.bridge.calls()) == 2 && e.bridge.cancels() == 1
	})
	e.source.set()
	p.Wake()
	eventually(t, "the loop to stop", func() bool { return e.bridge.cancels() == 2 })
	failed := errors.New("list failed")
	e.source.mu.Lock()
	reads := e.source.reads
	e.source.failList = failed
	e.source.mu.Unlock()
	p.Wake()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- stop() }()
	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, failed) {
			t.Fatalf("stop = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the poller did not stop")
	}
	if reads < 3 || len(e.bridge.calls()) != 2 {
		t.Errorf("%d reads, %d polls", reads, len(e.bridge.calls()))
	}
	if err := e.poller().Run(t.Context()); !errors.Is(err, failed) {
		t.Fatalf("run with a failed read = %v", err)
	}
	ended, end := context.WithCancel(t.Context())
	end()
	if err := e.poller().Run(ended); err != nil {
		t.Fatalf("run after the end = %v", err)
	}
}

// TestPollerUnbuiltClient covers C-22.FR-11 and FR-24: a Connection whose client cannot be built is not polled, and
// bridge_poll_failed is logged at the first read of the Connections that finds it so and every tenth, counted for each
// version, not at every read; a new version counts from one again, a Connection that left the list is forgotten, and
// one whose client is built again is polled.
func TestPollerUnbuiltClient(t *testing.T) {
	e := newPollEnv(t)
	e.bridge.set(func(b *eventsBridge) { b.hold = true })
	unbuilt := func(version int64) Polled {
		return Polled{Conn: Conn{ID: e.conn.ID, PublicID: e.conn.PublicID, Version: version},
			Err: errors.New("open the token: the key left the keyring")}
	}
	e.source.set(unbuilt(1))
	p := e.poller()
	stop := runPoller(t, p)
	reads := func() int {
		e.source.mu.Lock()
		defer e.source.mu.Unlock()
		return e.source.reads
	}
	read := func() {
		t.Helper()
		n := reads()
		p.Wake()
		eventually(t, "a read of the Connections", func() bool { return reads() > n })
	}
	eventually(t, "the first read", func() bool { return reads() == 1 })
	for range 10 {
		read()
	}
	e.source.set(unbuilt(2))
	read()
	e.source.set()
	read()
	e.source.set(unbuilt(2))
	read()
	if n := len(e.bridge.calls()); n != 0 {
		t.Fatalf("%d polls without a client", n)
	}
	e.source.set(e.polled)
	p.Wake()
	eventually(t, "the poll once the client is built", func() bool { return len(e.bridge.calls()) == 1 })
	// The poller logs the failures of a list before it reads the next one, so the poll proves them all written.
	var failures []float64
	for line := range strings.Lines(e.log.String()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil || m["event"] != "bridge_poll_failed" {
			continue
		}
		if m["level"] != "WARN" || m["connection"] != "CN0000000000B3" ||
			m["error"] != "open the token: the key left the keyring" {
			t.Errorf("line %v", m)
		}
		failures = append(failures, m["failures"].(float64))
	}
	if !slices.Equal(failures, []float64{1, 10, 1, 1}) {
		t.Errorf("logged the failures %v", failures)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

// TestPollerBatchFailureKeepsTheCursor covers C-22.FR-11: an event whose handling fails is not recorded, the cursor
// stays, and after a back-off the batch is polled again from it and handled.
func TestPollerBatchFailureKeepsTheCursor(t *testing.T) {
	e := newPollEnv(t)
	e.store.failRecord = 1
	e.bridge.add(e.press(t, "ev-1", "u-bob"))
	stop := runPoller(t, e.poller())
	eventually(t, "the third poll", func() bool { return len(e.bridge.calls()) == 3 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	calls := e.bridge.calls()
	if calls[0].Has("cursor") || calls[1].Has("cursor") || calls[2].Get("cursor") != "1" {
		t.Errorf("polls %v", calls)
	}
	if got := e.source.stored(); !slices.Equal(got, []string{"1"}) {
		t.Errorf("stored cursors %v", got)
	}
	if w := e.waits(); !slices.Equal(w, []time.Duration{time.Second}) {
		t.Errorf("pauses %v", w)
	}
	log := e.log.String()
	if strings.Count(log, `"event":"bridge_event_failed"`) != 1 || strings.Contains(log, "bridge_poll_failed") ||
		!e.store.recorded(e.conn.ID, "ev-1") {
		t.Errorf("logged %s", log)
	}
}

// TestPollerSkipsANonEvent covers C-22.FR-11 and FR-24: an entry of a batch that is no event of the protocol is skipped
// with bridge_event_rejected, and the batch goes on.
func TestPollerSkipsANonEvent(t *testing.T) {
	e := newPollEnv(t)
	e.bridge.add([]byte(`{"id":"ev-0","type":"reaction","occurred_at":"2026-10-14T12:00:00Z"}`),
		e.press(t, "ev-1", "u-bob"))
	stop := runPoller(t, e.poller())
	eventually(t, "the second poll", func() bool { return len(e.bridge.calls()) == 2 })
	_ = stop()
	want := `"level":"INFO","event":"bridge_event_rejected","connection":"CN0000000000B3","reason":"not_an_event"`
	if log := e.log.String(); strings.Count(log, want) != 1 || e.dropped("u-bob") != 1 {
		t.Errorf("logged %s", log)
	}
	if got := e.source.stored(); !slices.Equal(got, []string{"2"}) {
		t.Errorf("stored cursors %v", got)
	}
}

// TestPollerCursorStoreFails covers C-22.FR-11: a cursor that cannot be stored after a handled batch is a failure,
// logged as bridge_poll_failed, and the loop goes on from the cursor it holds.
func TestPollerCursorStoreFails(t *testing.T) {
	e := newPollEnv(t)
	e.source.failStore = 1
	e.bridge.add(e.press(t, "ev-1", "u-bob"))
	stop := runPoller(t, e.poller())
	eventually(t, "the second poll", func() bool { return len(e.bridge.calls()) == 2 })
	_ = stop()
	if got := e.bridge.calls()[1].Get("cursor"); got != "1" {
		t.Errorf("the second poll sent %q", got)
	}
	want := `"event":"bridge_poll_failed","connection":"CN0000000000B3","failures":1,"error":"the database went away"`
	if log := e.log.String(); !strings.Contains(log, want) || e.source.cursor(e.conn.ID) != nil {
		t.Errorf("logged %s", log)
	}
	if w := e.waits(); !slices.Equal(w, []time.Duration{time.Second}) {
		t.Errorf("pauses %v", w)
	}

	// A failed store of a dropped cursor is a failure too, and the loop goes on without a cursor.
	e = newPollEnv(t)
	e.source.failStore = 1
	seven := "7"
	e.source.cursors[e.conn.ID] = &seven
	e.bridge.add(e.press(t, "ev-1", "u-bob"))
	stop = runPoller(t, e.poller())
	eventually(t, "the third poll", func() bool { return len(e.bridge.calls()) == 3 })
	_ = stop()
	if calls := e.bridge.calls(); calls[1].Has("cursor") || calls[2].Get("cursor") != "1" {
		t.Errorf("polls %v", calls)
	}
	if !strings.Contains(e.log.String(), `"failures":1,"error":"the database went away"`) {
		t.Errorf("logged %s", e.log.String())
	}
}

// TestPollerBackoffAndPause: the back-off with its random jitter is between half its bound and the bound, and a pause
// ends with its context.
func TestPollerBackoffAndPause(t *testing.T) {
	p := &Poller{}
	for range 100 {
		if d := p.backoff(10); d < 30*time.Second || d > time.Minute {
			t.Fatalf("backoff %v", d)
		}
		if d := p.backoff(1); d < 500*time.Millisecond || d > time.Second {
			t.Fatalf("backoff %v", d)
		}
	}
	if randomJitter(0) != 0 {
		t.Fatal("jitter of 0")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	p.pause(ctx, time.Hour)
	p.pause(t.Context(), time.Millisecond)
	if time.Since(start) > time.Second {
		t.Fatal("a pause ignored its context")
	}
}

// TestPollerThroughTheClient: without a test hook the poller calls pollEvents through the Connection's client, with
// the bearer token, and a Run whose Connections are listed again on Every finds a new one.
func TestPollerThroughTheClient(t *testing.T) {
	e := newPollEnv(t)
	e.source.set()
	p := e.poller()
	p.Every = 5 * time.Millisecond
	stop := runPoller(t, p)
	e.bridge.add(e.press(t, "ev-1", "u-bob"))
	e.source.set(e.polled)
	eventually(t, "the press", func() bool { return e.store.recorded(e.conn.ID, "ev-1") })
	_ = stop()
	if e.dropped("u-bob") != 1 {
		t.Errorf("logged %s", e.log.String())
	}
}
