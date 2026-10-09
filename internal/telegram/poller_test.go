// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/logging"
)

// syncBuffer is a log sink that pollers write to concurrently.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lines are the log lines of event.
func (s *syncBuffer) lines(event string) []map[string]any {
	var out []map[string]any
	for line := range strings.Lines(s.String()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["event"] == event {
			out = append(out, m)
		}
	}
	return out
}

// memOffsets are the stored offsets of the Connections, shared by every poller and router of a test; HandleOnce holds
// the offset of its Connection locked, as the row lock does.
type memOffsets struct {
	mu      sync.Mutex
	locks   map[int64]*sync.Mutex
	offsets map[int64]int64
	fail    error
}

func (m *memOffsets) lock(id int64) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = map[int64]*sync.Mutex{}
	}
	if m.locks[id] == nil {
		m.locks[id] = &sync.Mutex{}
	}
	return m.locks[id]
}

func (m *memOffsets) HandleOnce(ctx context.Context, id, updateID int64, f func(context.Context) error) (bool,
	error) {
	l := m.lock(id)
	l.Lock()
	defer l.Unlock()
	o, err := m.Offset(ctx, id)
	if err != nil {
		return false, err
	}
	if o != nil && updateID < *o {
		return false, nil
	}
	if err := f(ctx); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.offsets == nil {
		m.offsets = map[int64]int64{}
	}
	m.offsets[id] = max(m.offsets[id], updateID+1)
	return true, nil
}

func (m *memOffsets) Offset(_ context.Context, id int64) (*int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	o, ok := m.offsets[id]
	if !ok {
		return nil, nil
	}
	return &o, nil
}

// memSource lists its Connections with the stored offsets, as the connections table would.
type memSource struct {
	mu      sync.Mutex
	conns   []Polled
	offsets *memOffsets
	fail    error
	reads   int
}

func (s *memSource) Polling(ctx context.Context) ([]Polled, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.fail != nil {
		return nil, s.fail
	}
	out := make([]Polled, 0, len(s.conns))
	for _, c := range s.conns {
		c.Offset, _ = s.offsets.Offset(ctx, c.ID)
		out = append(out, c)
	}
	return out, nil
}

func (s *memSource) set(conns ...Polled) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns = conns
}

// handled records the update ids a handler saw.
type handled struct {
	mu  sync.Mutex
	ids []int64
}

func (h *handled) handler(_ context.Context, _ Conn, u Update) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ids = append(h.ids, u.UpdateID)
	return nil
}

func (h *handled) get() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64(nil), h.ids...)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen in time", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type pollerEnv struct {
	fake    *faketelegram.Fake
	offsets *memOffsets
	source  *memSource
	log     *syncBuffer
	seen    *handled
	conn    Polled
}

func newPollerEnv(t *testing.T) *pollerEnv {
	t.Helper()
	e := &pollerEnv{fake: startFake(t), offsets: &memOffsets{}, log: &syncBuffer{}, seen: &handled{}}
	e.source = &memSource{offsets: e.offsets}
	e.conn = Polled{Conn: Conn{ID: 1, PublicID: "CNAAAAAAAAAAT1",
		Client: newClient(t, Settings{BaseURL: e.fake.URL(), Token: testToken})}, Version: 1}
	e.source.set(e.conn)
	return e
}

// poller is a new Leader's poller over the env, with waits recorded instead of slept.
func (e *pollerEnv) poller(waits *[]time.Duration, mu *sync.Mutex) *Poller {
	r := &Router{Offsets: e.offsets, Log: logging.New(e.log, logging.LevelInfo)}
	r.Handle(KindPrivateMessage, e.seen.handler)
	return &Poller{Source: e.source, Router: r, Log: logging.New(e.log, logging.LevelInfo), Every: time.Hour,
		jitter: func(time.Duration) time.Duration { return 0 },
		sleep: func(ctx context.Context, d time.Duration) bool {
			if mu != nil {
				mu.Lock()
				*waits = append(*waits, d)
				mu.Unlock()
			}
			t := time.NewTimer(5 * time.Millisecond)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-t.C:
				return true
			}
		}}
}

func (e *pollerEnv) enqueue(t *testing.T, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		raw, _ := json.Marshal(map[string]any{"update_id": id, "message": map[string]any{"message_id": id,
			"chat": map[string]any{"id": 42, "type": "private"}, "text": "/start"}})
		if _, err := e.fake.Enqueue(t.Context(), testToken, raw); err != nil {
			t.Fatal(err)
		}
	}
}

// run runs p as a Leader would until stop is called, which waits for it.
func run(t *testing.T, p *Poller) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	return func() error {
		cancel()
		return <-done
	}
}

// TestPollerResumesAtTheStoredOffsetAfterALeaderChange covers C-14.FR-1 and C-02.FR-10: the Leader polls getUpdates
// with AllowedUpdates, hands each update to the router, which stores the offset after it; a new Leader resumes there.
func TestPollerResumesAtTheStoredOffsetAfterALeaderChange(t *testing.T) {
	e := newPollerEnv(t)
	e.enqueue(t, 10, 11)
	stop := run(t, e.poller(nil, nil))
	eventually(t, "the first Leader handling 10 and 11", func() bool { return len(e.seen.get()) == 2 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if o, _ := e.offsets.Offset(t.Context(), 1); o == nil || *o != 12 {
		t.Fatalf("stored offset %v", o)
	}
	e.enqueue(t, 12)
	e.fake.ResetRequests()
	stop = run(t, e.poller(nil, nil))
	eventually(t, "the new Leader handling 12", func() bool { return len(e.seen.get()) == 3 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if got := e.seen.get(); got[0] != 10 || got[1] != 11 || got[2] != 12 {
		t.Fatalf("handled %v", got)
	}
	first := e.fake.Requests()[0]
	var body map[string]any
	_ = json.Unmarshal([]byte(first.Body), &body)
	if body["offset"] != float64(12) || !strings.Contains(first.Body, `"allowed_updates":["message","edited_message",`+
		`"channel_post","callback_query","my_chat_member"]`) {
		t.Fatalf("the new Leader's first poll %s", first.Body)
	}
	if n := len(e.log.lines("telegram_update_dropped")); n != 0 {
		t.Fatalf("%d updates dropped", n)
	}
}

// TestPollerBacksOffOnConflict covers C-14.FR-1 and AC-3: a 409 from a second poller backs off with jitter and logs
// telegram_poll_conflict; it is not a failure.
func TestPollerBacksOffOnConflict(t *testing.T) {
	e := newPollerEnv(t)
	var mu sync.Mutex
	var waits []time.Duration
	stop := run(t, e.poller(&waits, &mu))
	eventually(t, "a running poll", func() bool {
		for _, r := range e.fake.Requests() {
			if strings.HasSuffix(r.Path, "/getUpdates") && r.Status == 0 {
				return true
			}
		}
		return false
	})
	e.fake.Conflict(testToken)
	eventually(t, "telegram_poll_conflict", func() bool { return len(e.log.lines("telegram_poll_conflict")) == 1 })
	_ = stop()
	line := e.log.lines("telegram_poll_conflict")[0]
	if line["connection"] != "CNAAAAAAAAAAT1" || line["backoff_ms"] != float64(2500) || line["level"] != "INFO" {
		t.Fatalf("conflict line %v", line)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) == 0 || waits[0] != 2500*time.Millisecond {
		t.Fatalf("waits %v", waits)
	}
	if n := len(e.log.lines("telegram_poll_failed")); n != 0 {
		t.Fatalf("a conflict logged %d failures", n)
	}
}

// TestTwoPollersHandleAnUpdateOnce: a frozen old Leader and its successor polling the same bot take turns with 409
// (F-018) and the router hands each update on once.
func TestTwoPollersHandleAnUpdateOnce(t *testing.T) {
	e := newPollerEnv(t)
	stopA := run(t, e.poller(nil, nil))
	stopB := run(t, e.poller(nil, nil))
	e.enqueue(t, 1, 2, 3)
	eventually(t, "three updates", func() bool { return len(e.seen.get()) >= 3 })
	_ = stopA()
	_ = stopB()
	seen := map[int64]int{}
	for _, id := range e.seen.get() {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("update %d handled %d times", id, n)
		}
	}
}

// TestPollerFailures: a failed poll and a router that fails log telegram_poll_failed and back off exponentially; the
// offset stays at the last update handled.
func TestPollerFailures(t *testing.T) {
	e := newPollerEnv(t)
	prefix := "/k3x9"
	_ = e.fake.SetConfig(&prefix, nil, nil)
	var mu sync.Mutex
	var waits []time.Duration
	stop := run(t, e.poller(&waits, &mu))
	eventually(t, "three failures", func() bool { return len(e.log.lines("telegram_poll_failed")) >= 3 })
	_ = stop()
	mu.Lock()
	if waits[0] != 500*time.Millisecond || waits[1] != time.Second || waits[2] != 2*time.Second {
		t.Fatalf("waits %v", waits)
	}
	mu.Unlock()
	line := e.log.lines("telegram_poll_failed")[0]
	if line["error"] != "Telegram answered 404: Not Found" || line["level"] != "WARN" {
		t.Fatalf("failure line %v", line)
	}
	empty := ""
	_ = e.fake.SetConfig(&empty, nil, nil)
	e.offsets.fail = errors.New("database down")
	e.enqueue(t, 5)
	before := len(e.log.lines("telegram_poll_failed"))
	stop = run(t, e.poller(nil, nil))
	eventually(t, "a router failure", func() bool { return len(e.log.lines("telegram_poll_failed")) > before })
	_ = stop()
	if len(e.seen.get()) != 0 || !strings.Contains(e.log.String(), "database down") {
		t.Fatalf("handled %v", e.seen.get())
	}
}

// TestPollerFollowsTheConnections: a new version restarts a poller, a Connection that leaves the list stops, a wake
// reads the list at once, and a failed read stops every poller and returns.
func TestPollerFollowsTheConnections(t *testing.T) {
	e := newPollerEnv(t)
	p := e.poller(nil, nil)
	stop := run(t, p)
	eventually(t, "the first poll", func() bool { return len(e.fake.Requests()) > 0 })
	moved := e.conn
	moved.Version = 2
	e.source.set(moved)
	p.Wake()
	p.Wake()
	eventually(t, "a read after the wake", func() bool {
		e.source.mu.Lock()
		defer e.source.mu.Unlock()
		return e.source.reads >= 2
	})
	eventually(t, "the poll of the new version", func() bool {
		n := 0
		for _, r := range e.fake.Requests() {
			if strings.HasSuffix(r.Path, "/getUpdates") {
				n++
			}
		}
		return n >= 2
	})
	e.source.set()
	p.Wake()
	eventually(t, "the poller to stop", func() bool {
		for _, r := range e.fake.Requests() {
			if r.Status == 0 {
				return false
			}
		}
		return true
	})
	failed := errors.New("list failed")
	e.source.mu.Lock()
	e.source.fail = failed
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
	again := e.poller(nil, nil)
	if err := again.Run(t.Context()); !errors.Is(err, failed) {
		t.Fatalf("run with a failed read = %v", err)
	}
}

func TestBackoffAndJitter(t *testing.T) {
	p := &Poller{}
	for range 100 {
		if d := p.backoff(10, time.Second, time.Minute); d < 30*time.Second || d > time.Minute {
			t.Fatalf("backoff %v", d)
		}
	}
	if randomJitter(0) != 0 {
		t.Fatal("jitter of 0")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	p.wait(ctx, time.Hour)
	p.wait(t.Context(), time.Millisecond)
	if time.Since(start) > time.Second {
		t.Fatal("wait ignored its context")
	}
}
