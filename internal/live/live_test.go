// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package live

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// sessions is the set of usable sessions the Hub checks against.
type sessions struct {
	mu   sync.Mutex
	live map[int64]bool
	err  error
}

func (s *sessions) check(_ context.Context, ids []int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var out []int64
	for _, id := range ids {
		if s.live[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func newHub(live map[int64]bool) (*Hub, *sessions) {
	s := &sessions{live: live}
	return NewHub(1, s.check), s
}

func subscribe(t *testing.T, h *Hub, s Subscriber) *Subscription {
	t.Helper()
	sub, err := h.Subscribe(s)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// received drains the hints waiting for sub.
func received(sub *Subscription) []Hint {
	var out []Hint
	for {
		select {
		case h := <-sub.hints:
			out = append(out, h)
		default:
			return out
		}
	}
}

func done(sub *Subscription) bool {
	select {
	case <-sub.done:
		return true
	default:
		return false
	}
}

// TestHubFanOut: a hint of the Organization reaches every stream, one of another Organization none; a restored
// LISTEN sends the hints of everything.
func TestHubFanOut(t *testing.T) {
	h, _ := newHub(nil)
	a, b := subscribe(t, h, Subscriber{SessionID: 1}), subscribe(t, h, Subscriber{SessionID: 2, Admin: true})
	h.Receive(db.Hint{OrgID: 1, Type: HintOrganization})
	h.Receive(db.Hint{OrgID: 2, Type: HintOrganization})
	h.Receive(db.Hint{OrgID: 1, Type: "destination", ID: "DSAAAAAAAAAAAA"})
	want := []Hint{{Type: HintOrganization}, {Type: "destination", ID: "DSAAAAAAAAAAAA"}}
	if got := received(a); !slices.Equal(got, want) {
		t.Errorf("a = %v", got)
	}
	if got := received(b); !slices.Equal(got, want) {
		t.Errorf("b = %v", got)
	}
	h.Listening(false)
	if got := received(a); len(got) != 0 {
		t.Errorf("the first LISTEN sent %v", got)
	}
	h.Listening(true)
	if got := received(a); !slices.Equal(got, []Hint{{Type: HintOrganization}, {Type: HintSystemNotices}}) {
		t.Errorf("a restored LISTEN sent %v", got)
	}
	h.Send(Hint{Type: HintSystemNotices}, func(s Subscriber) bool { return s.Admin })
	if len(received(a)) != 0 || len(received(b)) != 3 {
		t.Error("a hint for Admins reached another stream")
	}
}

// TestHubAlertGroupHints: the hints about Alert Groups (C-09.FR-25) reach only the streams whose identity reads Alert
// Groups, and a restored LISTEN sends them alert-groups.
func TestHubAlertGroupHints(t *testing.T) {
	h, _ := newHub(nil)
	reader, other := subscribe(t, h, Subscriber{SessionID: 1, AlertGroups: true}), subscribe(t, h,
		Subscriber{SessionID: 2})
	h.Receive(db.Hint{OrgID: 1, Type: HintAlertGroup, ID: "AGAAAAAAAAAAAA"})
	h.Receive(db.Hint{OrgID: 1, Type: HintAlertGroups})
	h.Receive(db.Hint{OrgID: 1, Type: HintOrganization})
	if got := received(reader); !slices.Equal(got, []Hint{{Type: HintAlertGroup, ID: "AGAAAAAAAAAAAA"},
		{Type: HintAlertGroups}, {Type: HintOrganization}}) {
		t.Errorf("reader = %v", got)
	}
	if got := received(other); !slices.Equal(got, []Hint{{Type: HintOrganization}}) {
		t.Errorf("a stream without alert-groups:read = %v", got)
	}
	h.Listening(true)
	if got := received(reader); !slices.Contains(got, Hint{Type: HintAlertGroups}) {
		t.Errorf("a restored LISTEN sent %v", got)
	}
	if got := received(other); slices.Contains(got, Hint{Type: HintAlertGroups}) {
		t.Errorf("a restored LISTEN sent alert-groups to %v", got)
	}
}

// TestHubLimits: a session gets at most MaxStreamsPerSession streams and the replica MaxStreams; a stream that falls
// behind is closed; an unsubscribed stream frees its place.
func TestHubLimits(t *testing.T) {
	h, _ := newHub(nil)
	var subs []*Subscription
	for range MaxStreamsPerSession {
		subs = append(subs, subscribe(t, h, Subscriber{SessionID: 1}))
	}
	if _, err := h.Subscribe(Subscriber{SessionID: 1}); !errors.Is(err, ErrTooManyStreams) {
		t.Errorf("one stream too many for the session: %v", err)
	}
	h.Unsubscribe(subs[0])
	h.Unsubscribe(subs[0])
	if !done(subs[0]) || h.Streams() != MaxStreamsPerSession-1 {
		t.Error("an unsubscribed stream is not done or still counted")
	}
	subscribe(t, h, Subscriber{SessionID: 1})
	for i := int64(2); h.Streams() < MaxStreams; i++ {
		subscribe(t, h, Subscriber{SessionID: i})
	}
	if _, err := h.Subscribe(Subscriber{SessionID: 99999}); !errors.Is(err, ErrTooManyStreams) {
		t.Errorf("one stream too many for the replica: %v", err)
	}
	slow := subs[1]
	for range bufferSize + 1 {
		h.Send(Hint{Type: HintOrganization}, func(s Subscriber) bool { return s.SessionID == 1 })
	}
	if !done(slow) || h.Streams() != MaxStreams-MaxStreamsPerSession {
		t.Errorf("the slow streams were not closed: %d streams", h.Streams())
	}
	h.Close()
	if h.Streams() != 0 {
		t.Error("Close left streams")
	}
	late, err := h.Subscribe(Subscriber{SessionID: 1})
	if err != nil || !done(late) {
		t.Errorf("a stream after Close = %v, done %v", err, late != nil && done(late))
	}
}

// TestCheckSessions is C-09.AC-24's server side: a stream whose session is no longer usable closes at the next
// check; when the check fails, every stream stays open.
func TestCheckSessions(t *testing.T) {
	h, s := newHub(map[int64]bool{1: true, 2: true})
	h.CheckSessions(t.Context()) // no streams: nothing to ask
	a, b := subscribe(t, h, Subscriber{SessionID: 1}), subscribe(t, h, Subscriber{SessionID: 2})
	s.err = errors.New("db down")
	h.CheckSessions(t.Context())
	if done(a) || done(b) {
		t.Fatal("a failed check closed a stream")
	}
	s.err = nil
	delete(s.live, 2)
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		h.Run(ctx, ticks)
		close(stopped)
	}()
	ticks <- t0
	ticks <- t0 // the second tick runs after the first check finished
	if done(a) || !done(b) {
		t.Errorf("after the check: a done %v, b done %v", done(a), done(b))
	}
	cancel()
	<-stopped
	if !done(a) || h.Streams() != 0 {
		t.Error("the end of Run did not close the Hub")
	}

	// A stream that opens while the check runs is not closed by it.
	h, s = newHub(map[int64]bool{1: true})
	subscribe(t, h, Subscriber{SessionID: 1})
	var late *Subscription
	h.live = func(ctx context.Context, ids []int64) ([]int64, error) {
		late = subscribe(t, h, Subscriber{SessionID: 3})
		return s.check(ctx, ids)
	}
	h.CheckSessions(t.Context())
	if late == nil || done(late) {
		t.Error("a stream opened during the check was closed")
	}
}

// syncWriter collects what a stream writes.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	err error
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
	}
}

// TestStream is C-09.FR-25: the stream starts with retry: 3000, sends a keepalive comment at each tick and every hint
// as an event named hint with an id line and the data {"type", "id"}, flushed one by one; it ends when its
// subscription closes or its request does.
func TestStream(t *testing.T) {
	h, _ := newHub(nil)
	keepalive := make(chan time.Time)
	var every time.Duration
	h.every = func(d time.Duration) (<-chan time.Time, func()) {
		every = d
		return keepalive, func() {}
	}
	sub := subscribe(t, h, Subscriber{SessionID: 1, AlertGroups: true})
	var w syncWriter
	flushes := 0
	var mu sync.Mutex
	errc := make(chan error, 1)
	go func() {
		errc <- h.Stream(t.Context(), &w, func() error {
			mu.Lock()
			flushes++
			mu.Unlock()
			return nil
		}, sub)
	}()
	waitFor(t, func() bool { return strings.HasPrefix(w.String(), "retry: 3000\n\n") })
	keepalive <- t0
	h.Send(Hint{Type: HintSystemNotices}, nil)
	h.Send(Hint{Type: "alert-group", ID: "AGK7M3QX9P2RTA"}, nil)
	want := "retry: 3000\n\n: keepalive\n\n" +
		"event: hint\nid: 1\ndata: {\"type\":\"system-notices\",\"id\":null}\n\n" +
		"event: hint\nid: 2\ndata: {\"type\":\"alert-group\",\"id\":\"AGK7M3QX9P2RTA\"}\n\n"
	waitFor(t, func() bool { return w.String() == want })
	h.Unsubscribe(sub)
	if err := <-errc; err != nil {
		t.Errorf("Stream = %v", err)
	}
	mu.Lock()
	if flushes != 4 || every != KeepaliveInterval {
		t.Errorf("%d flushes, keepalive every %v", flushes, every)
	}
	mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	sub = subscribe(t, h, Subscriber{SessionID: 1})
	go func() { errc <- h.Stream(ctx, &syncWriter{}, func() error { return nil }, sub) }()
	cancel()
	if err := <-errc; err != nil {
		t.Errorf("Stream after the request ended = %v", err)
	}
	broken := errors.New("client gone")
	for _, c := range []struct {
		w     *syncWriter
		flush error
		after func()
	}{
		{&syncWriter{err: broken}, nil, nil},
		{&syncWriter{}, broken, nil},
		{&syncWriter{}, nil, func() { keepalive <- t0 }},
		{&syncWriter{}, nil, func() { h.Send(Hint{Type: HintOrganization}, nil) }},
	} {
		sub := subscribe(t, h, Subscriber{SessionID: 2})
		calls := 0
		go func() {
			errc <- h.Stream(t.Context(), c.w, func() error {
				calls++
				if c.after != nil && calls > 1 {
					return broken
				}
				return c.flush
			}, sub)
		}()
		if c.after != nil {
			waitFor(t, func() bool { return c.w.String() != "" })
			c.after()
		}
		if err := <-errc; !errors.Is(err, broken) {
			t.Errorf("a failed write = %v", err)
		}
		h.Unsubscribe(sub)
	}
}

// noticeSource is the runtime state the notices are computed from, at a business time.
type noticeSource struct {
	mu    sync.Mutex
	state organization.RuntimeState
	err   error
}

func (s *noticeSource) read(_ context.Context, now time.Time) ([]organization.Notice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return organization.ActiveNotices(s.state, now, 2*time.Minute), nil
}

// TestNotices is C-03.FR-18 and C-02.FR-24 with a manual clock: the recovery notice is visible to everyone and the
// notice that no replica leads only to Admins; a check sends a system-notices hint when the set a stream may see
// changed — when the recovery window starts and when it ends on the business clock — and the Admins' notice reaches
// only the streams of Admins. A failed check is logged once.
func TestNotices(t *testing.T) {
	h, _ := newHub(nil)
	business := clock.NewManual(t0)
	src := &noticeSource{state: organization.RuntimeState{AliveAt: t0}}
	var log bytes.Buffer
	n := NewNotices(src.read, business, h, logging.New(&log, logging.LevelInfo))
	user, admin := subscribe(t, h, Subscriber{SessionID: 1}), subscribe(t, h, Subscriber{SessionID: 2, Admin: true})
	hints := func() (int, int) { return len(received(user)), len(received(admin)) }

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		n.Run(ctx, ticks)
		close(stopped)
	}()
	ticks <- t0 // the first check, in Run, only recorded the sets; this one finds no change
	ticks <- t0
	if u, a := hints(); u != 0 || a != 0 {
		t.Fatalf("hints without a change: %d, %d", u, a)
	}
	src.mu.Lock()
	src.state.RecoveryUntil = t0.Add(10 * time.Minute)
	src.state.DowntimeEnd = t0
	src.mu.Unlock()
	ticks <- t0
	ticks <- t0
	if u, a := hints(); u != 1 || a != 1 {
		t.Errorf("the recovery window started: %d, %d hints", u, a)
	}
	got, err := n.Visible(t.Context(), false)
	if err != nil || len(got) != 1 || got[0].Kind != organization.NoticeRecoveringAfterDowntime ||
		!got[0].Until.Equal(t0.Add(10*time.Minute)) {
		t.Errorf("Visible = %+v, %v", got, err)
	}
	business.Advance(10 * time.Minute)
	src.mu.Lock()
	src.state.AliveAt = business.Now()
	src.mu.Unlock()
	ticks <- t0
	ticks <- t0
	if u, a := hints(); u != 1 || a != 1 {
		t.Errorf("the recovery window ended: %d, %d hints", u, a)
	}
	business.Advance(3 * time.Minute) // the alive mark is older than leader.absence_notice
	ticks <- t0
	ticks <- t0
	if u, a := hints(); u != 0 || a != 1 {
		t.Errorf("no replica leads: %d, %d hints, want only the Admin's", u, a)
	}
	if got, _ := n.Visible(t.Context(), false); len(got) != 0 {
		t.Errorf("a user sees %+v", got)
	}
	if got, _ := n.Visible(t.Context(), true); len(got) != 1 || got[0].Kind != organization.NoticeNoReplicaLeading {
		t.Errorf("an Admin sees %+v", got)
	}
	src.mu.Lock()
	src.err = errors.New("db down")
	src.mu.Unlock()
	ticks <- t0
	ticks <- t0
	ticks <- t0
	if c := strings.Count(log.String(), `"event":"system_notices_check_failed"`); c != 1 {
		t.Errorf("system_notices_check_failed logged %d times, want once", c)
	}
	if _, err := n.Visible(t.Context(), true); err == nil {
		t.Error("Visible with the read failing: no error")
	}
	cancel()
	<-stopped
}
