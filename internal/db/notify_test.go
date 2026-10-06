// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package db

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/logging"
)

// execer records the statements it runs.
type execer struct {
	sql  []string
	args [][]any
	err  error
}

func (e *execer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	e.sql, e.args = append(e.sql, sql), append(e.args, args)
	return pgconn.CommandTag{}, e.err
}

func TestNotifyHint(t *testing.T) {
	e := &execer{}
	if err := NotifyHint(t.Context(), e, Hint{OrgID: 3, Type: "organization"}); err != nil {
		t.Fatal(err)
	}
	if e.sql[0] != "SELECT pg_notify($1, $2)" || e.args[0][0] != HintChannel ||
		e.args[0][1] != `{"org_id":3,"type":"organization"}` {
		t.Errorf("sent %q %v", e.sql, e.args)
	}
	e.err = errors.New("tx aborted")
	if err := NotifyHint(t.Context(), e, Hint{OrgID: 3, Type: "route", ID: "RTAAAAAAAAAAAA"}); !errors.Is(err, e.err) ||
		e.args[1][1] != `{"org_id":3,"type":"route","id":"RTAAAAAAAAAAAA"}` {
		t.Errorf("= %v, sent %v", err, e.args[1])
	}
}

// step is what one WaitForNotification of a listenConn returns: a payload, or an error, or a timeout when both are
// empty.
type step struct {
	payload string
	err     error
	// channel is the channel of the notification, HintChannel when empty.
	channel string
}

// listenConn plays its steps, then blocks until the context ends.
type listenConn struct {
	mu      sync.Mutex
	steps   []step
	execErr map[string]error
	execs   []string
	closed  bool
}

func (c *listenConn) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execs = append(c.execs, sql)
	return pgconn.CommandTag{}, c.execErr[sql]
}

func (c *listenConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	c.mu.Lock()
	if len(c.steps) == 0 {
		c.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s := c.steps[0]
	c.steps = c.steps[1:]
	c.mu.Unlock()
	switch {
	case s.err != nil:
		return nil, s.err
	case s.payload == "":
		return nil, context.DeadlineExceeded
	}
	channel := s.channel
	if channel == "" {
		channel = HintChannel
	}
	return &pgconn.Notification{Channel: channel, Payload: s.payload}, nil
}

func (c *listenConn) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// TestListener: the Listener listens on the hint channel and passes on the hints; a wait without a notification
// pings the connection; a loss — of the connection, a ping, a LISTEN or a dial — is logged once until the LISTEN is
// back and retried with a delay that grows until then; the LISTEN that comes back is logged and reported as restored,
// so that clients read everything again.
func TestListener(t *testing.T) {
	broken := errors.New("connection reset")
	conns := []*listenConn{
		{steps: []step{{payload: `{"org_id":1,"type":"organization"}`}, {payload: "not json"}, {payload: `{"org_id":1}`},
			{}, {err: broken}}},
		{execErr: map[string]error{`LISTEN "muster_hints"`: broken}},
		{steps: []step{{}}, execErr: map[string]error{"SELECT 1": broken}},
		{steps: []step{{payload: `{"org_id":1,"type":"route","id":"RTAAAAAAAAAAAA"}`}}},
	}
	var mu sync.Mutex
	var hints []Hint
	var listening []bool
	got := make(chan struct{}, 10)
	var log bytes.Buffer
	dials := 0
	l := NewListener(func(context.Context) (ListenConn, error) {
		mu.Lock()
		defer mu.Unlock()
		dials++
		if dials == 2 {
			return nil, broken
		}
		c := conns[0]
		conns = conns[1:]
		return c, nil
	}, logging.New(&syncLog{buf: &log}, logging.LevelInfo))
	var waits []time.Duration
	l.wait = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return true
	}
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	all := slicesOf(conns)
	go func() {
		l.Run(ctx, func(h Hint) {
			mu.Lock()
			hints = append(hints, h)
			mu.Unlock()
			got <- struct{}{}
		}, func(restored bool) {
			mu.Lock()
			listening = append(listening, restored)
			mu.Unlock()
		})
		close(stopped)
	}()
	<-got
	<-got
	cancel()
	<-stopped
	mu.Lock()
	defer mu.Unlock()
	if len(hints) != 2 || hints[0] != (Hint{OrgID: 1, Type: "organization"}) ||
		hints[1] != (Hint{OrgID: 1, Type: "route", ID: "RTAAAAAAAAAAAA"}) {
		t.Errorf("hints = %+v", hints)
	}
	// The third connection listens again, then fails its ping; the fourth listens for good.
	if len(listening) != 3 || listening[0] || !listening[1] || !listening[2] {
		t.Errorf("listening = %v, want [false true true]", listening)
	}
	if len(waits) != 4 || waits[0] != time.Second || waits[1] != 2*time.Second || waits[2] != 4*time.Second ||
		waits[3] != time.Second {
		t.Errorf("waits = %v, want [1s 2s 4s 1s]: the delay grows until a LISTEN works", waits)
	}
	out := log.String()
	if c := strings.Count(out, `"event":"live_updates_listen_failed"`); c != 2 {
		t.Errorf("live_updates_listen_failed logged %d times, want once per loss:\n%s", c, out)
	}
	if c := strings.Count(out, `"event":"live_updates_listen_restored"`); c != 2 {
		t.Errorf("live_updates_listen_restored logged %d times, want twice:\n%s", c, out)
	}
	for i, c := range all {
		if !c.closed {
			t.Errorf("connection %d was not closed", i)
		}
	}
	if all[0].execs[0] != `LISTEN "muster_hints"` || all[0].execs[1] != "SELECT 1" {
		t.Errorf("the first connection ran %v", all[0].execs)
	}
}

func slicesOf(conns []*listenConn) []*listenConn {
	return append([]*listenConn(nil), conns...)
}

// TestListenerStopsWhileWaiting: a Listener that waits to reconnect stops when its context ends.
func TestListenerStopsWhileWaiting(t *testing.T) {
	l := NewListener(func(context.Context) (ListenConn, error) { return nil, errors.New("down") },
		logging.New(&bytes.Buffer{}, logging.LevelInfo))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	l.Run(ctx, func(Hint) {}, func(bool) {})
	if sleep(ctx, time.Hour) {
		t.Error("sleep outlived its context")
	}
	if !sleep(t.Context(), time.Millisecond) {
		t.Error("a short sleep did not pass")
	}
	l = NewListener(func(context.Context) (ListenConn, error) { return nil, errors.New("down") },
		logging.New(&bytes.Buffer{}, logging.LevelInfo))
	ctx, cancel = context.WithCancel(t.Context())
	l.wait = func(context.Context, time.Duration) bool {
		cancel()
		return false
	}
	l.Run(ctx, func(Hint) {}, func(bool) {})
}

// syncLog lets the test read the log after the Listener's goroutine wrote it.
type syncLog struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

func (w *syncLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// TestListenerChannels: the Listener also listens on the channels given to Listen and passes each payload to their
// function; a notification on any other channel is skipped.
func TestListenerChannels(t *testing.T) {
	c := &listenConn{steps: []step{{channel: "muster_snapshots", payload: `{"org_id":1}`},
		{channel: "dev_clock", payload: "600"}, {channel: "other", payload: "x"},
		{payload: `{"org_id":1,"type":"organization"}`}}}
	l := NewListener(func(context.Context) (ListenConn, error) { return c, nil },
		logging.New(&bytes.Buffer{}, logging.LevelInfo))
	var mu sync.Mutex
	var got []string
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, s)
	}
	l.Listen("muster_snapshots", func(p string) { record("snapshots " + p) })
	l.Listen("dev_clock", func(p string) { record("clock " + p) })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		l.Run(ctx, func(h Hint) {
			record("hint " + h.Type)
			cancel()
		}, func(bool) {})
		close(done)
	}()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, "|") != `snapshots {"org_id":1}|clock 600|hint organization` {
		t.Errorf("received %q", got)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Join(c.execs[:3], "|") != `LISTEN "muster_hints"|LISTEN "dev_clock"|LISTEN "muster_snapshots"` {
		t.Errorf("execs %q", c.execs)
	}
}

// TestListenerChannelFails: a LISTEN on an added channel that fails is a loss like the hint channel's.
func TestListenerChannelFails(t *testing.T) {
	broken := errors.New("permission denied")
	c := &listenConn{execErr: map[string]error{`LISTEN "dev_clock"`: broken}}
	l := NewListener(func(context.Context) (ListenConn, error) { return c, nil },
		logging.New(&bytes.Buffer{}, logging.LevelInfo))
	l.Listen("dev_clock", func(string) {})
	ctx, cancel := context.WithCancel(t.Context())
	l.wait = func(context.Context, time.Duration) bool {
		cancel()
		return false
	}
	l.Run(ctx, func(Hint) {}, func(bool) { t.Error("listening without the channel") })
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		t.Error("the connection stayed open")
	}
}
