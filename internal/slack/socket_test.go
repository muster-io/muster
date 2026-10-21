// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/requestlog"
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

// fakeTimer is a timer the sockets armed: its duration, its channel, and whether it was stopped or fired.
type fakeTimer struct {
	d       time.Duration
	ch      chan time.Time
	stopped bool
	fired   bool
}

// fakeTimers are the real-clock timers of the sockets, which only the test fires.
type fakeTimers struct {
	mu   sync.Mutex
	list []*fakeTimer
}

func (ft *fakeTimers) after(d time.Duration) (<-chan time.Time, func()) {
	t := &fakeTimer{d: d, ch: make(chan time.Time, 1)}
	ft.mu.Lock()
	ft.list = append(ft.list, t)
	ft.mu.Unlock()
	return t.ch, func() {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		t.stopped = true
	}
}

// fire fires the oldest armed timer of d, waiting for one to be armed.
func (ft *fakeTimers) fire(t *testing.T, d time.Duration) {
	t.Helper()
	eventually(t, "a timer of "+d.String(), func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		for _, x := range ft.list {
			if x.d == d && !x.stopped && !x.fired {
				x.fired = true
				x.ch <- time.Time{}
				return true
			}
		}
		return false
	})
}

// fireArmed fires every timer of d neither stopped nor fired, without waiting for one.
func (ft *fakeTimers) fireArmed(d time.Duration) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	for _, x := range ft.list {
		if x.d == d && !x.stopped && !x.fired {
			x.fired = true
			x.ch <- time.Time{}
		}
	}
}

// armed counts the timers of d neither stopped nor fired.
func (ft *fakeTimers) armed(d time.Duration) int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	n := 0
	for _, x := range ft.list {
		if x.d == d && !x.stopped && !x.fired {
			n++
		}
	}
	return n
}

// made counts the timers of d armed so far, and fired those of them that the test fired.
func (ft *fakeTimers) made(d time.Duration) (made, fired int) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	for _, x := range ft.list {
		if x.d == d {
			made++
			if x.fired {
				fired++
			}
		}
	}
	return made, fired
}

// sourceHello is a hello a socketSource recorded.
type sourceHello struct {
	id, version int64
	other       int
}

// socketSource lists the Connections set and records the hellos; err fails the list, helloErr the records. While block
// is set, a record sends the deadline of its context to entered and waits for block or for its context.
type socketSource struct {
	mu       sync.Mutex
	conns    []SocketConn
	err      error
	helloErr error
	hellos   []sourceHello
	reads    int
	block    chan struct{}
	entered  chan time.Time
}

func (s *socketSource) set(conns ...SocketConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns = conns
}

func (s *socketSource) fail(err, helloErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err, s.helloErr = err, helloErr
}

func (s *socketSource) SocketConnections(context.Context) ([]SocketConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return slices.Clone(s.conns), s.err
}

func (s *socketSource) RecordSocketHello(ctx context.Context, c SocketConn, other int) error {
	s.mu.Lock()
	block, entered := s.block, s.entered
	s.mu.Unlock()
	if block != nil {
		deadline, _ := ctx.Deadline()
		entered <- deadline
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hellos = append(s.hellos, sourceHello{id: c.ID, version: c.Version, other: other})
	return s.helloErr
}

func (s *socketSource) recorded() []sourceHello {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.hellos)
}

func (s *socketSource) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// socketPresses records the presses handed to it, calling onGo, when set, before Go returns.
type socketPresses struct {
	mu   sync.Mutex
	got  []handedPress
	onGo func(handedPress)
}

func (p *socketPresses) Go(ctx context.Context, c Conn, payload []byte) {
	h := handedPress{ctx: ctx, conn: c, payload: payload}
	p.mu.Lock()
	p.got = append(p.got, h)
	on := p.onGo
	p.mu.Unlock()
	if on != nil {
		on(h)
	}
}

func (p *socketPresses) handed() []handedPress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.got)
}

// envelopeRecords keep the records of envelopes; while gate is set, each write waits for it to close.
type envelopeRecords struct {
	mu       sync.Mutex
	list     []requestlog.EnvelopeRecord
	gate     chan struct{}
	entered  chan struct{}
	finished int
}

func (r *envelopeRecords) RecordEnvelope(_ context.Context, rec requestlog.EnvelopeRecord) {
	r.mu.Lock()
	gate, entered := r.gate, r.entered
	r.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, rec)
	r.finished++
}

func (r *envelopeRecords) written() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}

func (r *envelopeRecords) got() []requestlog.EnvelopeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.list)
}

// socketEnv runs Sockets against the fake Slack server with fake timers, an identity jitter — a back-off after n
// failures is exactly 1 s·2^(n-1) — a recording source, presses and Request log, and the log in a buffer.
type socketEnv struct {
	fake    *fakeslack.Fake
	source  *socketSource
	presses *socketPresses
	records *envelopeRecords
	timers  *fakeTimers
	log     *lockedBuffer
	base    context.Context //nolint:containedctx // the process context the presses must get
	sockets *Sockets

	cancel context.CancelFunc
	done   chan error
}

func newSocketEnv(t *testing.T) *socketEnv {
	t.Helper()
	e := &socketEnv{fake: startFake(t), source: &socketSource{}, presses: &socketPresses{},
		records: &envelopeRecords{}, timers: &fakeTimers{}, log: &lockedBuffer{}}
	e.base = context.WithValue(t.Context(), baseKey{}, "process")
	e.sockets = &Sockets{Source: e.source, Presses: e.presses, Recorder: e.records, Base: e.base, Real: tick(),
		Log: logging.New(e.log, logging.LevelInfo), after: e.timers.after,
		jitter: func(d time.Duration) time.Duration { return d }}
	return e
}

// network is the Network of the env's clients: loopback allowed, the log in the env's buffer.
func (e *socketEnv) network(t *testing.T) Network {
	t.Helper()
	n := network(t, nil)
	n.Log = logging.New(e.log, logging.LevelInfo)
	return n
}

// conn is the Connection id in socket mode, version 1, of the fake's app b, or of a when first.
func (e *socketEnv) conn(t *testing.T, id int64, first bool) SocketConn {
	t.Helper()
	bot, app := fakeslack.SecondBotToken, fakeslack.SecondAppToken
	if first {
		bot, app = fakeslack.BotToken, fakeslack.AppToken
	}
	c := newClient(t, e.network(t), Settings{BotToken: logging.Secret(bot), AppToken: logging.Secret(app),
		Route: devRoute(e.fake)})
	return socketConn(id, c)
}

func socketConn(id int64, c *Client) SocketConn {
	return SocketConn{Conn: Conn{ID: id, PublicID: fmt.Sprintf("CN0000000000K%d", id), Mode: EventModeSocket,
		Client: c}, Version: 1}
}

// start runs the sockets until the test ends or stop.
func (e *socketEnv) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	e.cancel, e.done = cancel, make(chan error, 1)
	go func() { e.done <- e.sockets.Run(ctx) }()
	t.Cleanup(func() { _ = e.stop(t) })
}

// stop ends the run and returns what Run returned; a socket whose peer does not answer its closure is closed at once
// after a few milliseconds.
func (e *socketEnv) stop(t *testing.T) error {
	t.Helper()
	if e.cancel == nil {
		return nil
	}
	e.cancel()
	e.cancel = nil
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-e.done:
			return err
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not return")
			return nil
		}
		e.timers.fireArmed(closeGrace)
	}
}

// ended waits for the run, stopped by the test, to return within d, and returns what it returned.
func (e *socketEnv) ended(t *testing.T, d time.Duration) error {
	t.Helper()
	select {
	case err := <-e.done:
		e.cancel = nil
		return err
	case <-time.After(d):
		t.Fatalf("Run did not return within %s", d)
		return nil
	}
}

// lines waits until the log has n lines of event, and returns them.
func (e *socketEnv) lines(t *testing.T, event string, n int) []map[string]any {
	t.Helper()
	var got []map[string]any
	eventually(t, fmt.Sprintf("%d lines %s", n, event), func() bool {
		got = e.log.lines(t, event)
		return len(got) >= n
	})
	if len(got) != n {
		t.Fatalf("%s: %d lines, want %d: %v", event, len(got), n, got)
	}
	return got
}

// app is the socket state of the fake's app b.
func (e *socketEnv) app() fakeslack.AppSockets { return e.fake.Sockets().Apps[fakeslack.AppB] }

// script carries out a socket script for app b.
func (e *socketEnv) script(t *testing.T, s fakeslack.SocketScript) {
	t.Helper()
	if s.App == "" {
		s.App = fakeslack.AppB
	}
	if err := e.fake.Script(t.Context(), s); err != nil {
		t.Fatal(err)
	}
}

// send sends an envelope through the newest socket of app b and waits for its confirmation.
func (e *socketEnv) send(t *testing.T, typ, payload string) fakeslack.Envelope {
	t.Helper()
	env, err := e.fake.SendEnvelope(t.Context(), fakeslack.AppB, typ, json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got, ok := e.fake.WaitConfirmed(ctx, env.EnvelopeID)
	if !ok || got.ConfirmMS == nil {
		t.Fatalf("envelope %s not confirmed: %+v", typ, got)
	}
	return got
}

// opens are the bearer tokens of the fake's apps.connections.open requests.
func (e *socketEnv) opens() []string {
	var out []string
	for _, r := range e.fake.Requests() {
		if r.Path == "/api/apps.connections.open" {
			out = append(out, strings.TrimPrefix(r.Headers["Authorization"][0], "Bearer "))
		}
	}
	return out
}

func num(line map[string]any, name string) float64 {
	v, _ := line[name].(float64)
	return v
}

// failedLine checks a slack_socket_failed line: its Connection, reason and back-off — none when backoff is negative —
// and an error only when one is wanted.
func failedLine(t *testing.T, line map[string]any, conn, reason string, backoff float64, withError bool) {
	t.Helper()
	wait, hasWait := line["backoff_seconds"].(float64)
	if field(line, "connection") != conn || field(line, "reason") != reason || hasWait != (backoff >= 0) ||
		(hasWait && wait != backoff) || line["level"] != "WARN" || (line["error"] != nil) != withError {
		t.Fatalf("slack_socket_failed %v, want %s %s %v error %v", line, conn, reason, backoff, withError)
	}
}

// TestSocketOnePerConnection covers C-24.FR-9, AC-10: one socket for each Connection in socket mode, each opened after
// its own apps.connections.open with its app-level token; each hello logs slack_socket_connected with num_connections
// and held, and is recorded; a Connection whose client cannot be built is not held and logged once for each version,
// without a back-off.
func TestSocketOnePerConnection(t *testing.T) {
	e := newSocketEnv(t)
	broken := SocketConn{Conn: Conn{ID: 3, PublicID: "CN0000000000K3", Mode: EventModeSocket}, Version: 4,
		Err: errors.New("the app-level token cannot be decrypted")}
	e.source.set(e.conn(t, 1, true), e.conn(t, 2, false), broken)
	e.start(t)
	lines := e.lines(t, "slack_socket_connected", 2)
	conns := map[string]bool{}
	for _, l := range lines {
		conns[field(l, "connection")] = true
		if num(l, "num_connections") != 1 || num(l, "held") != 1 || l["error"] != nil || l["level"] != "INFO" {
			t.Errorf("connected %v", l)
		}
	}
	if !conns["CN0000000000K1"] || !conns["CN0000000000K2"] {
		t.Fatalf("connected %v", lines)
	}
	opens := e.opens()
	slices.Sort(opens)
	if fmt.Sprint(opens) != fmt.Sprint([]string{fakeslack.AppToken, fakeslack.SecondAppToken}) {
		t.Fatalf("apps.connections.open with %v", opens)
	}
	for key, st := range e.fake.Sockets().Apps {
		if st.Open != 1 || st.Opened != 1 || st.Hellos != 1 {
			t.Errorf("app %s: %+v", key, st)
		}
	}
	hellos := e.source.recorded()
	if len(hellos) != 2 || hellos[0].other != 0 || hellos[1].other != 0 {
		t.Fatalf("hellos %+v", hellos)
	}
	failed := e.lines(t, "slack_socket_failed", 1)
	failedLine(t, failed[0], "CN0000000000K3", reasonDial, -1, true)
	if field(failed[0], "error") != "the app-level token cannot be decrypted" {
		t.Fatalf("failed %v", failed[0])
	}
	reads := e.source.readCount()
	e.sockets.Wake()
	eventually(t, "a read", func() bool { return e.source.readCount() > reads })
	broken.Version = 5
	e.source.set(e.conn(t, 1, true), e.conn(t, 2, false), broken)
	e.sockets.Wake()
	e.lines(t, "slack_socket_failed", 2)
	if n := len(e.log.lines(t, "slack_socket_connected")); n != 2 || len(e.opens()) != 2 {
		t.Fatalf("a read without a change reconnected: %d hellos, %d opens", n, len(e.opens()))
	}
}

// TestSocketHelloTimeout covers C-24.FR-9, AC-10: a connection without hello within slack.hello_timeout is closed and
// logged as hello_timeout without an error; after the back-off the next connection opens with a new
// apps.connections.open.
func TestSocketHelloTimeout(t *testing.T) {
	e := newSocketEnv(t)
	e.script(t, fakeslack.SocketScript{SkipHello: 1})
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	eventually(t, "the socket", func() bool { return e.app().Open == 1 })
	e.timers.fire(t, HelloTimeout)
	failedLine(t, e.lines(t, "slack_socket_failed", 1)[0], "CN0000000000K2", reasonHelloTimeout, 1, false)
	eventually(t, "the socket closed", func() bool { return e.app().Open == 0 })
	e.timers.fire(t, SocketBackoffBase)
	e.lines(t, "slack_socket_connected", 1)
	if st := e.app(); st.Open != 1 || st.Opened != 2 || st.Hellos != 1 || len(e.opens()) != 2 {
		t.Fatalf("after the back-off: %+v, %d opens", st, len(e.opens()))
	}
}

// TestSocketEnvelopeConfirmedFirst covers C-24.FR-9, AC-10: an interactive envelope is confirmed on its socket before
// its press is handed over — the fake has the confirmation while the handler still holds the press — and is handed
// over whole, with the Connection and its client, in the process's context; envelopes of other types, and an
// interactive one whose payload is no JSON object, are confirmed and not handed over.
func TestSocketEnvelopeConfirmedFirst(t *testing.T) {
	e := newSocketEnv(t)
	c := e.conn(t, 2, false)
	e.source.set(c)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	e.presses.onGo = func(handedPress) {
		entered <- struct{}{}
		<-release
	}
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	payload := `{"type":"block_actions","user":{"id":"U0FAKEALICE"},"actions":[{"value":"v"}]}`
	env, err := e.fake.SendEnvelope(t.Context(), fakeslack.AppB, typeInteractive, json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the press was not handed over")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	got, ok := e.fake.WaitConfirmed(ctx, env.EnvelopeID)
	cancel()
	close(release)
	if !ok {
		t.Fatalf("the envelope was not confirmed while its press was held: %+v", got)
	}
	handed := e.presses.handed()
	if len(handed) != 1 || string(handed[0].payload) != payload || handed[0].ctx.Value(baseKey{}) != "process" ||
		handed[0].conn != c.Conn {
		t.Fatalf("handed %+v", handed)
	}
	e.presses.onGo = nil
	e.send(t, "events_api", `{"event":{"type":"app_mention"}}`)
	e.send(t, "slash_commands", `{"command":"/muster"}`)
	e.send(t, typeInteractive, `"block_actions"`)
	e.send(t, typeInteractive, `[{"type":"block_actions"}]`)
	if n := len(e.presses.handed()); n != 1 {
		t.Fatalf("%d presses handed over", n)
	}
	if err := e.stop(t); err != nil {
		t.Fatal(err)
	}
	if handed[0].ctx.Err() != nil {
		t.Fatal("the press's context ended with the Leader's run")
	}
}

// TestSocketWithoutBase: without Base a press gets Run's context without its end.
func TestSocketWithoutBase(t *testing.T) {
	e := newSocketEnv(t)
	e.sockets.Base = nil
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	e.send(t, typeInteractive, `{"type":"block_actions"}`)
	if err := e.stop(t); err != nil {
		t.Fatal(err)
	}
	if h := e.presses.handed(); len(h) != 1 || h[0].ctx.Err() != nil {
		t.Fatalf("handed %+v", h)
	}
}

// TestSocketPressRunsTheCommand covers C-24.FR-9, FR-10, AC-10: a press confirmed over the socket takes the path of the
// callback's presses: an Ack from an account linked to a Responder acknowledges as that User with the Transport slack.
func TestSocketPressRunsTheCommand(t *testing.T) {
	pe := newPressEnv(t)
	e := newSocketEnv(t)
	e.sockets.Presses = pe.presses
	c := e.conn(t, pressConnID, false)
	c.PublicID = "CN0000000000S1"
	e.source.set(c)
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	payload := pe.payload(t, pressIn{user: "U0BOB", channel: pressChannel, ts: pressRootTS,
		value: pe.act(t, buttons.CommandAcknowledge, 0)})
	e.send(t, typeInteractive, string(payload))
	eventually(t, "the Command", func() bool { return len(pe.commands.ran()) == 1 })
	if err := pe.presses.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := pe.commands.ran()[0]
	if r.group != pressGroup || r.caller.Transport != audit.TransportSlack ||
		r.caller.Actor != audit.User(7, "SR0000000000B1") {
		t.Fatalf("ran %+v", r)
	}
	for _, s := range pe.links.spaces {
		if s != accountlinks.SpaceSlack(pressConnID) {
			t.Errorf("looked up in %s", s)
		}
	}
	line := pe.log.lines(t, "slack_press")
	if len(line) != 1 || field(line[0], "connection") != "CN0000000000S1" || field(line[0], "outcome") != "done" {
		t.Fatalf("slack_press %v", line)
	}
}

// TestSocketStopsAtOnce covers C-24.FR-9, AC-10 and ADR-0007: when the Leader's run ends every socket closes at once,
// and Run returns only after the records of the envelopes are written; nothing reaches the presses after it.
func TestSocketStopsAtOnce(t *testing.T) {
	e := newSocketEnv(t)
	e.source.set(e.conn(t, 1, true), e.conn(t, 2, false))
	e.start(t)
	e.lines(t, "slack_socket_connected", 2)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	e.records.mu.Lock()
	e.records.gate, e.records.entered = gate, entered
	e.records.mu.Unlock()
	e.send(t, "events_api", `{}`)
	<-entered
	e.cancel()
	eventually(t, "every socket closed", func() bool {
		st := e.fake.Sockets()
		return st.Apps[fakeslack.AppA].Open == 0 && st.Apps[fakeslack.AppB].Open == 0
	})
	select {
	case <-e.done:
		t.Fatal("Run returned before the record of its envelope was written")
	default:
	}
	close(gate)
	select {
	case err := <-e.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	e.cancel = nil
	if n := e.records.written(); n != 1 {
		t.Fatalf("%d records written", n)
	}
	if _, cut := e.timers.made(closeGrace); cut != 0 {
		t.Fatalf("%d closures the fake answers were cut", cut)
	}
	if _, err := e.fake.SendEnvelope(t.Context(), fakeslack.AppB, "events_api", nil); err == nil {
		t.Fatal("a socket is still open")
	}
	if n := len(e.log.lines(t, "slack_socket_failed")); n != 0 {
		t.Fatalf("%d failures logged at the end of the run", n)
	}
}

// TestSocketModeSwitchAndDelete covers C-24.FR-9, AC-10: a Connection that leaves the list — switched to http or
// deleted — loses its socket as soon as a wake reads the list, without waiting for the minute; a new version of it
// closes the old socket and opens a new one.
func TestSocketModeSwitchAndDelete(t *testing.T) {
	e := newSocketEnv(t)
	c := e.conn(t, 2, false)
	e.source.set(c)
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	e.source.set()
	start := time.Now()
	e.sockets.Wake()
	eventually(t, "the socket closed", func() bool { return e.app().Open == 0 })
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the socket closed after %s", d)
	}
	e.source.set(c)
	e.sockets.Wake()
	e.lines(t, "slack_socket_connected", 2)
	c.Version = 2
	e.source.set(c)
	e.sockets.Wake()
	e.lines(t, "slack_socket_connected", 3)
	eventually(t, "one socket", func() bool { st := e.app(); return st.Open == 1 && st.Opened == 3 })
	if _, fired := e.timers.made(SocketReadInterval); fired != 0 {
		t.Fatal("the minute was waited for")
	}
	if h := e.source.recorded(); h[len(h)-1].version != 2 {
		t.Fatalf("hellos %+v", h)
	}
	if n := len(e.log.lines(t, "slack_socket_failed")); n != 0 {
		t.Fatalf("%d failures logged", n)
	}
}

// TestSocketEveryMinute covers C-24.FR-9: without a wake the Connections are read again every minute.
func TestSocketEveryMinute(t *testing.T) {
	e := newSocketEnv(t)
	e.start(t)
	eventually(t, "a read", func() bool { return e.source.readCount() == 1 })
	e.source.set(e.conn(t, 2, false))
	e.timers.fire(t, SocketReadInterval)
	e.lines(t, "slack_socket_connected", 1)
}

// TestSocketReadFails: when the first read of the Connections fails, Run returns its error, so that the Leader task
// runs again; a later failed read keeps the sockets, which still confirm, is logged as leader_task_failed, and is tried
// again at the next wake.
func TestSocketReadFails(t *testing.T) {
	down := errors.New("the database went away")
	first := newSocketEnv(t)
	first.source.fail(down, nil)
	first.start(t)
	if err := first.ended(t, 5*time.Second); !errors.Is(err, down) {
		t.Fatalf("Run = %v", err)
	}

	e := newSocketEnv(t)
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	e.source.fail(down, nil)
	e.sockets.Wake()
	if l := e.lines(t, "leader_task_failed", 1)[0]; field(l, "task") != "slack_socket_mode" ||
		field(l, "error") != down.Error() || l["level"] != "WARN" {
		t.Fatalf("leader_task_failed %v", l)
	}
	e.send(t, "events_api", `{}`)
	reads := e.source.readCount()
	e.source.fail(nil, nil)
	e.sockets.Wake()
	eventually(t, "a read", func() bool { return e.source.readCount() > reads })
	select {
	case err := <-e.done:
		t.Fatalf("Run returned %v", err)
	default:
	}
	if st := e.app(); st.Open != 1 || st.Opened != 1 || len(e.log.lines(t, "slack_socket_failed")) != 0 {
		t.Fatalf("the socket did not stay: %+v", st)
	}
}

// TestSocketRefreshOverlap covers C-24.FR-9, AC-10: after disconnect warning or refresh_requested the next connection
// says hello while the old one is still open, which closes only then; its hello counts the two the Leader holds, and
// nothing is logged as failed. Muster counts a socket as held until its own reader ends, which may be after the fake
// has forgotten it, so each refresh waits for the old reader too.
func TestSocketRefreshOverlap(t *testing.T) {
	e := newSocketEnv(t)
	var readersEnded atomic.Int64
	e.sockets.readerEnded = func() { readersEnded.Add(1) }
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	for i, reason := range []string{disconnectWarning, disconnectRefresh} {
		e.script(t, fakeslack.SocketScript{Disconnect: reason})
		lines := e.lines(t, "slack_socket_connected", i+2)
		if l := lines[i+1]; num(l, "held") != 2 || num(l, "num_connections") != 2 {
			t.Fatalf("%s: %v", reason, l)
		}
		eventually(t, reason+": the old socket closed", func() bool {
			return e.app().Open == 1 && readersEnded.Load() == int64(i+1)
		})
		if st := e.app(); !st.Overlapped || st.Opened != i+2 {
			t.Fatalf("%s: %+v", reason, st)
		}
	}
	if h := e.source.recorded(); len(h) != 3 || h[1].other != 0 || h[2].other != 0 {
		t.Fatalf("hellos %+v", h)
	}
	if n := len(e.log.lines(t, "slack_socket_failed")); n != 0 || len(e.opens()) != 3 {
		t.Fatalf("%d failures, %d opens", n, len(e.opens()))
	}
	e.send(t, "events_api", `{}`)
}

// TestSocketHelloThenDisconnect covers C-24.FR-9: a refresh_requested that the reader takes right after the hello,
// before the holder has looked for the hello, refreshes the connection and fails nothing. The holder then finds both at
// once, which a select would take in either order, so the case runs several times.
func TestSocketHelloThenDisconnect(t *testing.T) {
	for range 8 {
		e := newSocketEnv(t)
		after := e.sockets.after
		var once sync.Once
		e.sockets.after = func(d time.Duration) (<-chan time.Time, func()) {
			if d == HelloTimeout {
				once.Do(func() { e.afterHello(t, disconnectRefresh) })
			}
			return after(d)
		}
		e.source.set(e.conn(t, 2, false))
		e.start(t)
		e.lines(t, "slack_socket_connected", 2)
		if n := len(e.log.lines(t, "slack_socket_failed")); n != 0 || len(e.opens()) != 2 {
			t.Fatalf("%d failures, %d opens: %s", n, len(e.opens()), e.log.String())
		}
		if err := e.stop(t); err != nil {
			t.Fatal(err)
		}
	}
}

// afterHello waits, on the holder's goroutine before it looks for the hello, until the first socket's hello is
// logged, sends the disconnect with reason unless it is empty and then an envelope through it, and waits for the
// envelope's confirmation: the reader takes the frames in order, so it has told the holder the hello, and the
// disconnect, by then. It marks the test failed when any of it fails.
func (e *socketEnv) afterHello(t *testing.T, reason string) {
	if !e.waitLogged(t, "slack_socket_connected") {
		return
	}
	if reason != "" {
		if err := e.fake.Script(t.Context(), fakeslack.SocketScript{App: fakeslack.AppB,
			Disconnect: reason}); err != nil {
			t.Error(err)
			return
		}
	}
	env, err := e.fake.SendEnvelope(t.Context(), fakeslack.AppB, "events_api", json.RawMessage(`{}`))
	if err != nil {
		t.Error(err)
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, ok := e.fake.WaitConfirmed(ctx, env.EnvelopeID); !ok {
		t.Error("the envelope after the hello was not confirmed")
	}
}

// TestSocketHelloAtTimeout covers C-24.FR-9: a hello that came by the time the hello timeout fires, before the holder
// looked for it, holds the connection; nothing fails. The holder then finds both at once, which a select would take
// in either order, so the case runs several times.
func TestSocketHelloAtTimeout(t *testing.T) {
	for range 8 {
		e := newSocketEnv(t)
		after := e.sockets.after
		var once sync.Once
		e.sockets.after = func(d time.Duration) (<-chan time.Time, func()) {
			ch, stop := after(d)
			if d != HelloTimeout {
				return ch, stop
			}
			fired := make(chan time.Time, 1)
			once.Do(func() {
				e.afterHello(t, "")
				fired <- time.Time{}
			})
			return fired, stop
		}
		e.source.set(e.conn(t, 2, false))
		e.start(t)
		e.lines(t, "slack_socket_connected", 1)
		e.send(t, "events_api", `{}`)
		if n := len(e.log.lines(t, "slack_socket_failed")); n != 0 || len(e.opens()) != 1 {
			t.Fatalf("%d failures, %d opens: %s", n, len(e.opens()), e.log.String())
		}
		if err := e.stop(t); err != nil {
			t.Fatal(err)
		}
	}
}

// waitLogged waits, off the test's goroutine, until event is logged; it marks the test failed when it is not.
func (e *socketEnv) waitLogged(t *testing.T, event string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(e.log.String(), `"event":"`+event+`"`) {
		if time.Now().After(deadline) {
			t.Errorf("%s was not logged", event)
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return true
}

// TestSocketRefreshOpenFails covers C-24.FR-9: a planned refresh whose next connection cannot be opened keeps the old
// one, which still confirms envelopes, and tries again after the back-off.
func TestSocketRefreshOpenFails(t *testing.T) {
	e := newSocketEnv(t)
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	revoked := fakeslack.DefaultConfig()
	revoked.Revoked = []string{fakeslack.SecondAppToken}
	if err := e.fake.SetConfig(revoked); err != nil {
		t.Fatal(err)
	}
	e.script(t, fakeslack.SocketScript{Disconnect: disconnectRefresh})
	l := e.lines(t, "slack_socket_failed", 1)[0]
	failedLine(t, l, "CN0000000000K2", reasonDial, 1, true)
	if field(l, "error") != "apps.connections.open: token_revoked" {
		t.Fatalf("error %q", field(l, "error"))
	}
	e.send(t, "events_api", `{}`)
	if err := e.fake.SetConfig(fakeslack.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	e.timers.fire(t, SocketBackoffBase)
	e.lines(t, "slack_socket_connected", 2)
	eventually(t, "the old socket closed", func() bool { st := e.app(); return st.Open == 1 && st.Overlapped })
}

// TestSocketBackoff covers C-24.FR-9, AC-10: link_disabled, too_many_websockets, a refused handshake, a connection
// that ended and an unknown disconnect each close the socket, log slack_socket_failed with their reason and back off
// from 1 s doubling; a connection that lasted 30 s after its hello resets the back-off.
func TestSocketBackoff(t *testing.T) {
	e := newSocketEnv(t)
	rc := clock.NewManual(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	e.sockets.Real = rc
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	const conn = "CN0000000000K2"

	e.script(t, fakeslack.SocketScript{Disconnect: disconnectLinkDisabled})
	failedLine(t, e.lines(t, "slack_socket_failed", 1)[0], conn, reasonLinkDisabled, 1, false)
	eventually(t, "the socket closed", func() bool { return e.app().Open == 0 })

	e.script(t, fakeslack.SocketScript{TooManyWebsockets: 1})
	e.timers.fire(t, time.Second)
	failedLine(t, e.lines(t, "slack_socket_failed", 2)[1], conn, reasonTooMany, 2, false)

	if err := e.fake.SetFault(fakeserver.Fault{Path: "/link/", Status: http.StatusForbidden, Times: 1}); err != nil {
		t.Fatal(err)
	}
	e.timers.fire(t, 2*time.Second)
	l := e.lines(t, "slack_socket_failed", 3)[2]
	failedLine(t, l, conn, reasonDial, 4, true)
	if text := field(l, "error"); !strings.Contains(text, "answered 403") || strings.Contains(text, "ticket=") ||
		!strings.Contains(text, "[redacted]") {
		t.Fatalf("error %q", text)
	}

	e.timers.fire(t, 4*time.Second)
	e.lines(t, "slack_socket_connected", 2)
	rc.Advance(socketSettled)
	e.script(t, fakeslack.SocketScript{Close: true})
	l = e.lines(t, "slack_socket_failed", 4)[3]
	failedLine(t, l, conn, reasonClosed, 1, true)

	e.timers.fire(t, time.Second)
	e.lines(t, "slack_socket_connected", 3)
	rc.Advance(socketSettled - time.Millisecond)
	e.script(t, fakeslack.SocketScript{Disconnect: "socket_mode_disabled"})
	l = e.lines(t, "slack_socket_failed", 5)[4]
	failedLine(t, l, conn, reasonClosed, 2, true)
	if field(l, "error") != "disconnect socket_mode_disabled" {
		t.Fatalf("error %q", field(l, "error"))
	}
	e.timers.fire(t, 2*time.Second)
	e.lines(t, "slack_socket_connected", 4)
	eventually(t, "one socket", func() bool { return e.app().Open == 1 })
}

// TestSocketBackoffAfterHello covers slack.socket_backoff: a connection that ends right after its hello does not reset
// the back-off, which keeps doubling — 1 s, 2 s, 4 s — so that a socket Slack ends at once is not opened again every
// second; a planned refresh starts it from 1 s again.
func TestSocketBackoffAfterHello(t *testing.T) {
	e := newSocketEnv(t)
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	const conn = "CN0000000000K2"
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		e.lines(t, "slack_socket_connected", i+1)
		e.script(t, fakeslack.SocketScript{Close: true})
		failedLine(t, e.lines(t, "slack_socket_failed", i+1)[i], conn, reasonClosed, want.Seconds(), true)
		e.timers.fire(t, want)
	}
	e.lines(t, "slack_socket_connected", 4)
	e.script(t, fakeslack.SocketScript{Disconnect: disconnectRefresh})
	e.lines(t, "slack_socket_connected", 5)
	eventually(t, "the old socket closed", func() bool { return e.app().Open == 1 })
	e.script(t, fakeslack.SocketScript{Close: true})
	failedLine(t, e.lines(t, "slack_socket_failed", 4)[3], conn, reasonClosed, 1, true)
}

// TestSocketRetryAfter: when apps.connections.open is rate-limited with a Retry-After longer than the back-off, the
// next connection waits for the Retry-After, up to the back-off's cap of 5 min, so that a Retry-After of a day does not
// leave the Connection without a socket for a day.
func TestSocketRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after int
		want  time.Duration
	}{
		{"waited for", 30, 30 * time.Second},
		{"capped", 86400, SocketBackoffCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSocketEnv(t)
			e.sockets.openTimeout = 100 * time.Millisecond
			if err := e.fake.SetFault(fakeserver.Fault{Path: "/api/apps.connections.open",
				Status: http.StatusTooManyRequests, RetryAfterSeconds: tc.after, Times: 1}); err != nil {
				t.Fatal(err)
			}
			e.source.set(e.conn(t, 2, false))
			e.start(t)
			failedLine(t, e.lines(t, "slack_socket_failed", 1)[0], "CN0000000000K2", reasonDial, tc.want.Seconds(),
				true)
			e.timers.fire(t, tc.want)
			e.lines(t, "slack_socket_connected", 1)
			if made, _ := e.timers.made(SocketBackoffBase); made != 0 {
				t.Fatal("the back-off did not wait for the Retry-After")
			}
			if whole := time.Duration(tc.after) * time.Second; whole != tc.want {
				if made, _ := e.timers.made(whole); made != 0 {
					t.Fatal("the back-off waited for the whole Retry-After")
				}
			}
		})
	}
}

// TestSocketBackoffBounds covers slack.socket_backoff: from 1 s doubling up to 5 min, with equal jitter in [d/2, d];
// BackoffBase and BackoffCap replace the bounds.
func TestSocketBackoffBounds(t *testing.T) {
	s := &Sockets{}
	for n := 1; n <= 40; n++ {
		d := SocketBackoffCap
		if n <= 9 {
			d = SocketBackoffBase << (n - 1)
		}
		if w := s.backoff(n); w < d/2 || w > d {
			t.Errorf("backoff(%d) = %s, want within [%s, %s]", n, w, d/2, d)
		}
	}
	s.jitter = func(d time.Duration) time.Duration { return d }
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second,
		9: 256 * time.Second, 10: 5 * time.Minute, 1000: 5 * time.Minute} {
		if got := s.backoff(n); got != want {
			t.Errorf("backoff(%d) = %s, want %s", n, got, want)
		}
	}
	s.BackoffBase, s.BackoffCap = time.Millisecond, 3*time.Millisecond
	if got := []time.Duration{s.backoff(1), s.backoff(2), s.backoff(3)}; fmt.Sprint(got) != "[1ms 2ms 3ms]" {
		t.Errorf("custom backoff %v", got)
	}
	if socketJitter(0) != 0 || socketJitter(-time.Second) != 0 {
		t.Error("jitter of nothing")
	}
	if ch, stop := (&Sockets{}).timer(time.Hour); ch == nil {
		t.Error("no real timer")
	} else {
		stop()
	}
}

// startSocketStandIn is a Slack whose apps.connections.open answers a Socket Mode address on itself with the ticket,
// and whose /link/ is link, which may block on quit until the test ends; anything else is 404.
func startSocketStandIn(t *testing.T, ticket string, link func(w http.ResponseWriter, r *http.Request,
	quit <-chan struct{})) *httptest.Server {
	t.Helper()
	quit := make(chan struct{})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps.connections.open":
			answerJSON(http.StatusOK, `{"ok":true,"url":"ws://`+strings.TrimPrefix(srv.URL, "http://")+
				`/link/?ticket=`+ticket+`&app_id=A0FAKESTAND"}`)(w, r)
		case "/link/":
			link(w, r, quit)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(quit) })
	return srv
}

// sending is a /link/ that accepts the socket, sends frames, and then holds it without reading — so without answering
// pings — until the test ends.
func sending(frames ...[]byte) func(w http.ResponseWriter, r *http.Request, quit <-chan struct{}) {
	return func(w http.ResponseWriter, r *http.Request, quit <-chan struct{}) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, f := range frames {
			typ := websocket.MessageText
			if !json.Valid(f) {
				typ = websocket.MessageBinary
			}
			if conn.Write(ctx, typ, f) != nil {
				return
			}
		}
		<-quit
	}
}

// TestSocketPing covers C-24.FR-9: an open connection is pinged every 30 s; a pong arms the next ping, and a ping
// without a pong within 10 s closes the connection as closed.
func TestSocketPing(t *testing.T) {
	e := newSocketEnv(t)
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	e.timers.fire(t, PingInterval)
	eventually(t, "the next ping", func() bool { made, _ := e.timers.made(PingInterval); return made == 2 })
	if n := len(e.log.lines(t, "slack_socket_failed")); n != 0 {
		t.Fatalf("%d failures", n)
	}

	q := newSocketEnv(t)
	srv := startSocketStandIn(t, "0123456789abcdef0123456789abcdef",
		sending([]byte(`{"type":"hello","num_connections":1}`)))
	q.source.set(socketConn(4, newClient(t, q.network(t), settings(srv.URL+"/api/"))))
	q.start(t)
	q.lines(t, "slack_socket_connected", 1)
	q.timers.fire(t, PingInterval)
	eventually(t, "the deadline of the ping", func() bool {
		made, _ := q.timers.made(PingTimeout)
		return made == 2 && q.timers.armed(PingTimeout) == 1
	})
	q.timers.fire(t, PingTimeout)
	l := q.lines(t, "slack_socket_failed", 1)[0]
	failedLine(t, l, "CN0000000000K4", reasonClosed, 1, true)
	if field(l, "error") != errPingNoPong.Error() {
		t.Fatalf("error %q", field(l, "error"))
	}
}

// TestSocketFrames covers C-24.FR-2, FR-9: a later hello on the same connection is counted and stored, replacing the
// count of an earlier one not stored yet; binary frames, frames that are not JSON and frames of other types are
// ignored.
func TestSocketFrames(t *testing.T) {
	e := newSocketEnv(t)
	srv := startSocketStandIn(t, "0123456789abcdef0123456789abcdef", sending(
		[]byte(`{"type":"hello","num_connections":1}`), []byte{0xff, 0x00}, []byte(`{"type":"unknown"}`),
		[]byte(`{"type":"hello","num_connections":3}`)))
	e.source.set(socketConn(4, newClient(t, e.network(t), settings(srv.URL+"/api/"))))
	e.start(t)
	shared := e.lines(t, "slack_socket_shared", 1)
	if field(shared[0], "connection") != "CN0000000000K4" || num(shared[0], "other_connections") != 2 {
		t.Fatalf("shared %v", shared)
	}
	lines := e.log.lines(t, "slack_socket_connected")
	if last := lines[len(lines)-1]; len(lines) > 2 || num(last, "num_connections") != 3 || num(last, "held") != 1 {
		t.Fatalf("connected %v", lines)
	}
	if h := e.source.recorded(); h[len(h)-1].other != 2 {
		t.Fatalf("hellos %+v", h)
	}
	if n := len(e.log.lines(t, "slack_socket_failed")); n != 0 {
		t.Fatalf("%d failures", n)
	}
}

// TestSocketHelloStoreBlocked: a store of what a hello counted that does not return holds up nothing — the hello
// reaches the holder, which stops its hello timeout, and an envelope is confirmed meanwhile — is bounded by 5 s, and
// ends with the run, which returns at once and logs no slack_socket_connected for the store it cut short.
func TestSocketHelloStoreBlocked(t *testing.T) {
	e := newSocketEnv(t)
	entered := make(chan time.Time, 4)
	e.source.mu.Lock()
	e.source.block, e.source.entered = make(chan struct{}), entered
	e.source.mu.Unlock()
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	select {
	case deadline := <-entered:
		if d := time.Until(deadline); d <= 0 || d > helloWriteTimeout {
			t.Fatalf("the store has %s left", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the count was not stored")
	}
	eventually(t, "the hello", func() bool {
		made, _ := e.timers.made(HelloTimeout)
		return made == 1 && e.timers.armed(HelloTimeout) == 0
	})
	e.send(t, "events_api", `{}`)
	if n := len(e.log.lines(t, "slack_socket_connected")) + len(e.log.lines(t, "slack_socket_failed")); n != 0 {
		t.Fatalf("%d lines while the store is blocked", n)
	}
	e.cancel()
	if err := e.ended(t, time.Second); err != nil {
		t.Fatal(err)
	}
	if n := len(e.log.lines(t, "slack_socket_connected")); n != 0 {
		t.Fatalf("%d slack_socket_connected lines for a store the stop cut short", n)
	}
}

// TestSocketClosesWithClosure covers C-24.FR-9 and ADR-0007: a socket Muster lets go — its Connection left socket
// mode, or the run ended — is closed with a normal closure, which the peer answers at once; a socket whose peer does
// not answer it is closed at once after a second, and the run ends then.
func TestSocketClosesWithClosure(t *testing.T) {
	statuses := make(chan websocket.StatusCode, 4)
	srv := startSocketStandIn(t, "0123456789abcdef0123456789abcdef",
		func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			if conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"hello","num_connections":1}`)) != nil {
				return
			}
			for {
				if _, _, err := conn.Read(r.Context()); err != nil {
					statuses <- websocket.CloseStatus(err)
					return
				}
			}
		})
	closure := func(what string) {
		t.Helper()
		select {
		case got := <-statuses:
			if got != websocket.StatusNormalClosure {
				t.Fatalf("%s: the peer saw %v", what, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the socket was not closed", what)
		}
	}
	e := newSocketEnv(t)
	c := socketConn(4, newClient(t, e.network(t), settings(srv.URL+"/api/")))
	e.source.set(c)
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	e.source.set()
	e.sockets.Wake()
	closure("a Connection that left socket mode")
	e.source.set(c)
	e.sockets.Wake()
	e.lines(t, "slack_socket_connected", 2)
	e.cancel()
	closure("the end of the run")
	if err := e.ended(t, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if made, fired := e.timers.made(closeGrace); made != 2 || fired != 0 {
		t.Fatalf("%d waits for the closure, %d cut", made, fired)
	}

	q := newSocketEnv(t)
	silent := startSocketStandIn(t, "0123456789abcdef0123456789abcdef",
		sending([]byte(`{"type":"hello","num_connections":1}`)))
	q.source.set(socketConn(5, newClient(t, q.network(t), settings(silent.URL+"/api/"))))
	q.start(t)
	q.lines(t, "slack_socket_connected", 1)
	q.cancel()
	eventually(t, "the wait for the closure", func() bool { return q.timers.armed(closeGrace) == 1 })
	select {
	case <-q.done:
		t.Fatal("Run returned before the closure was answered or cut")
	default:
	}
	q.timers.fire(t, closeGrace)
	if err := q.ended(t, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := len(e.log.lines(t, "slack_socket_failed")) + len(q.log.lines(t, "slack_socket_failed")); n != 0 {
		t.Fatalf("%d failures", n)
	}
}

// TestSocketReasonsMasked covers C-24.FR-14, C-23.FR-3: a close frame and a disconnect whose reasons repeat the
// address's query are logged with it masked.
func TestSocketReasonsMasked(t *testing.T) {
	const ticket = "4c2b9e7a1d3f5a6b8c0d2e4f6a8b0c1d"
	var links atomic.Int32
	e := newSocketEnv(t)
	srv := startSocketStandIn(t, ticket, func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := r.Context()
		if conn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","num_connections":1}`)) != nil {
			return
		}
		if links.Add(1) == 1 {
			_ = conn.Close(websocket.StatusPolicyViolation, "bad "+r.URL.RawQuery)
			return
		}
		if conn.Write(ctx, websocket.MessageText, []byte(`{"type":"disconnect","reason":"gone `+
			r.URL.Query().Get("ticket")+`"}`)) != nil {
			return
		}
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	})
	e.source.set(socketConn(4, newClient(t, e.network(t), settings(srv.URL+"/api/"))))
	e.start(t)
	l := e.lines(t, "slack_socket_failed", 1)[0]
	failedLine(t, l, "CN0000000000K4", reasonClosed, 1, true)
	if text := field(l, "error"); !strings.Contains(text, "StatusPolicyViolation") ||
		!strings.Contains(text, `reason = "bad [redacted]"`) {
		t.Fatalf("error %q", text)
	}
	e.timers.fire(t, time.Second)
	l = e.lines(t, "slack_socket_failed", 2)[1]
	failedLine(t, l, "CN0000000000K4", reasonClosed, 2, true)
	if text := field(l, "error"); text != "disconnect gone?[redacted]" {
		t.Fatalf("error %q", text)
	}
	if strings.Contains(e.log.String(), ticket) {
		t.Fatalf("the ticket reached the log: %s", e.log.String())
	}
}

// TestSocketHelloCounts covers C-24.FR-2, FR-9, AC-10: a hello that counts more connections of the app than the Leader
// holds records the others and logs slack_socket_shared; the next hello without others records none and logs nothing
// more; a count that cannot be stored is logged with slack_socket_connected's error.
func TestSocketHelloCounts(t *testing.T) {
	e := newSocketEnv(t)
	cfg := fakeslack.DefaultConfig()
	cfg.ExtraConnections = map[string]int{fakeslack.AppB: 1}
	if err := e.fake.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	if l := e.lines(t, "slack_socket_connected", 1)[0]; num(l, "num_connections") != 2 || num(l, "held") != 1 {
		t.Fatalf("connected %v", l)
	}
	shared := e.lines(t, "slack_socket_shared", 1)[0]
	if field(shared, "connection") != "CN0000000000K2" || num(shared, "other_connections") != 1 ||
		shared["level"] != "WARN" {
		t.Fatalf("shared %v", shared)
	}
	if h := e.source.recorded(); len(h) != 1 || h[0] != (sourceHello{id: 2, version: 1, other: 1}) {
		t.Fatalf("hellos %+v", h)
	}
	if err := e.fake.SetConfig(fakeslack.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	e.script(t, fakeslack.SocketScript{Disconnect: disconnectRefresh})
	e.lines(t, "slack_socket_connected", 2)
	if h := e.source.recorded(); len(h) != 2 || h[1].other != 0 || len(e.log.lines(t, "slack_socket_shared")) != 1 {
		t.Fatalf("hellos %+v", h)
	}
	eventually(t, "the old socket closed", func() bool { return e.app().Open == 1 })
	e.source.fail(nil, errors.New("the database went away"))
	e.script(t, fakeslack.SocketScript{Disconnect: disconnectRefresh})
	if l := e.lines(t, "slack_socket_connected", 3)[2]; field(l, "error") != "the database went away" {
		t.Fatalf("connected %v", l)
	}
}

// TestSocketEnvelopeRecords covers C-24.FR-14, AC-17 and C-23.FR-1: each envelope with an envelope_id is recorded once
// confirmed — its type as the operation, the socket's address without its query, the envelope as received and the
// confirmation — and hello and disconnect are not. Each record is written in the background, so the records are
// matched to the envelopes by envelope_id, not by their order.
func TestSocketEnvelopeRecords(t *testing.T) {
	e := newSocketEnv(t)
	e.source.set(e.conn(t, 2, false))
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	sent := map[string]fakeslack.Envelope{}
	for _, env := range []fakeslack.Envelope{e.send(t, typeInteractive, `{"type":"block_actions"}`),
		e.send(t, "events_api", `{}`)} {
		sent[env.EnvelopeID] = env
	}
	eventually(t, "two records", func() bool { return len(e.records.got()) == 2 })
	e.script(t, fakeslack.SocketScript{Disconnect: disconnectRefresh})
	e.lines(t, "slack_socket_connected", 2)
	got := e.records.got()
	if len(got) != 2 {
		t.Fatalf("records %+v", got)
	}
	wantURL := "ws://" + strings.TrimPrefix(e.fake.URL(), "http://") + "/link/"
	for i, r := range got {
		var frame struct {
			EnvelopeID string          `json:"envelope_id"`
			Type       string          `json:"type"`
			Payload    json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(r.Envelope, &frame); err != nil {
			t.Fatal(err)
		}
		want, ok := sent[frame.EnvelopeID]
		if !ok {
			t.Errorf("record %d of no envelope sent, or of one recorded twice: %+v", i, r)
			continue
		}
		delete(sent, frame.EnvelopeID)
		if r.Operation != want.Type || r.URL != wantURL ||
			string(r.Confirmation) != `{"envelope_id":"`+want.EnvelopeID+`"}` || r.Via != ViaDirect ||
			r.Links != (requestlog.Links{Connection: 2, ConnectionID: "CN0000000000K2"}) || r.Latency <= 0 {
			t.Errorf("record %d: %+v", i, r)
		}
	}
	if len(sent) != 0 {
		t.Errorf("envelopes not recorded: %+v", sent)
	}
}

// TestSocketTargets covers C-24.FR-9, AC-3 and ADR-0015: outside muster dev only a wss address on slack.com or a host
// below it, with the port 443 or none, is connected to; a client routed to the fake connects to the fake's own
// address only; an answer of apps.connections.open with another address is refused without showing it.
func TestSocketTargets(t *testing.T) {
	prod := newClient(t, network(t, nil), Settings{BotToken: testBot, AppToken: testApp})
	for _, raw := range []string{
		"ws://wss-primary.slack.com/link/?ticket=t",
		"wss://evil.example/link/?ticket=t",
		"wss://slack.com.evil.example/link/?ticket=t",
		"wss://evilslack.com/link/?ticket=t",
		"wss://user@wss-primary.slack.com/link/?ticket=t",
		"wss://wss-primary.slack.com:8443/link/?ticket=t",
		"wss://wss-primary.slack.com:/link/",
		"wss://wss-primary.slacK.com/link/",
		"wss://.slack.com/link/",
		"wss://a..slack.com/link/",
		"wss://wss_primary.slack.com/link/",
		"wss://wss-primary.slack.com/link/#frag",
		"wss:///link/",
		"https://wss-primary.slack.com/link/",
		"wss://wss-primary.slack.com\x7f/",
	} {
		if target, ok := prod.socketTarget(raw); ok {
			t.Errorf("%q accepted as %q", raw, target)
		}
	}
	for raw, want := range map[string]string{
		"wss://wss-primary.slack.com/link/?ticket=t&app_id=A1":    "wss://wss-primary.slack.com/link/?ticket=t&app_id=A1",
		"WSS://WSS-Primary.Slack.COM:443/link/?ticket=t":          "wss://wss-primary.slack.com:443/link/?ticket=t",
		"wss://slack.com/link":                                    "wss://slack.com/link",
		"wss://wss-backup.slack.com/link/?ticket=a%2Fb&app_id=A1": "wss://wss-backup.slack.com/link/?ticket=a%2Fb&app_id=A1",
	} {
		if got, ok := prod.socketTarget(raw); !ok || got != want {
			t.Errorf("%q = %q %v, want %q", raw, got, ok, want)
		}
	}
	f := startFake(t)
	routed := fakeClient(t, f, true)
	host := strings.TrimPrefix(f.URL(), "http://")
	if got, ok := routed.socketTarget("ws://" + host + "/link/?ticket=t"); !ok || got != "ws://"+host+"/link/?ticket=t" {
		t.Errorf("the fake's address = %q %v", got, ok)
	}
	for _, raw := range []string{"wss://" + host + "/link/", "ws://127.0.0.2:1/link/", "http://" + host + "/link/"} {
		if _, ok := routed.socketTarget(raw); ok {
			t.Errorf("%q accepted for the routed client", raw)
		}
	}
	if _, ok := routed.socketTarget("wss://wss-primary.slack.com/link/"); !ok {
		t.Error("Slack's own address refused for the routed client")
	}
	secure := newClient(t, network(t, nil), settings("https://127.0.0.1:9/api/"))
	if got, ok := secure.socketTarget("wss://127.0.0.1:9/link/"); !ok || got != "wss://127.0.0.1:9/link/" {
		t.Errorf("https origin = %q %v", got, ok)
	}

	const ticket = "5e3c7a90b1d24f68a0c1e2d3f4a5b6c7"
	s := startStandIn(t, answerJSON(http.StatusOK, `{"ok":true,"url":"wss://evil.example/link/?ticket=`+ticket+`"}`))
	c := newClient(t, network(t, nil), settings(s.base()))
	address, r := c.OpenSocket(t.Context())
	if address != "" || r.OK() || r.Classification.Class != ClassUnknown || r.Body != nil ||
		string(r.Error) != "apps.connections.open: the Socket Mode address is not Slack's" {
		t.Fatalf("OpenSocket = %v %+v", address, r)
	}
	if _, err := c.DialSocket(t.Context(), "wss://evil.example/link/?ticket="+ticket); !errors.Is(err,
		errSocketAddress) || strings.Contains(err.Error(), ticket) {
		t.Fatalf("DialSocket = %v", err)
	}
	for _, seen := range s.seen() {
		if seen.Path != "/api/apps.connections.open" {
			t.Errorf("requested %s", seen.Path)
		}
	}
}

// TestOpenSocketAnswers: an answer without an address is not what apps.connections.open answers; a failed call keeps
// its Result; a good answer gives the address as a Secret and drops the body.
func TestOpenSocketAnswers(t *testing.T) {
	for body, want := range map[string]string{
		`{"ok":true}`:                         "apps.connections.open: the answer is not the expected JSON",
		`{"ok":true,"url":7}`:                 "apps.connections.open: the answer is not the expected JSON",
		`{"ok":false,"error":"invalid_auth"}`: "invalid_auth",
	} {
		s := startStandIn(t, answerJSON(http.StatusOK, body))
		address, r := newClient(t, network(t, nil), settings(s.base())).OpenSocket(t.Context())
		if address != "" || r.OK() || string(r.Error) != want {
			t.Errorf("%s: %v %+v", body, address, r)
		}
	}
	f := startFake(t)
	address, r := fakeClient(t, f, true).OpenSocket(t.Context())
	if !r.OK() || r.Body != nil || !strings.HasPrefix(string(address), "ws://"+strings.TrimPrefix(f.URL(), "http://")+
		"/link/?ticket=") || fmt.Sprint(address) != "[redacted]" {
		t.Fatalf("OpenSocket = %+v", r)
	}
	if rec := f.Requests(); len(rec) != 1 || rec[0].Headers["Authorization"][0] != "Bearer "+fakeslack.SecondAppToken {
		t.Fatalf("requests %+v", rec)
	}
}

// TestSocketThroughProxy covers C-24.AC-3 and C-02.FR-20: with an HTTP proxy on the Connection the socket reaches the
// fake only through the proxy, by CONNECT, so that the confirmation of an envelope travels back; records say proxy.
func TestSocketThroughProxy(t *testing.T) {
	e := newSocketEnv(t)
	proxy, err := fakeproxy.Start(t.Context(), fakeproxy.HTTP, "127.0.0.1:0", fakeproxy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	c := newClient(t, e.network(t), Settings{BotToken: fakeslack.SecondBotToken, AppToken: fakeslack.SecondAppToken,
		Route: devRoute(e.fake), Proxy: &outbound.Proxy{Type: outbound.ProxyHTTP, Address: proxy.Addr()}})
	e.source.set(socketConn(2, c))
	e.start(t)
	e.lines(t, "slack_socket_connected", 1)
	e.send(t, "events_api", `{}`)
	host := strings.TrimPrefix(e.fake.URL(), "http://")
	targets := proxy.Targets()
	if len(targets) < 2 {
		t.Fatalf("targets %v", targets)
	}
	for _, target := range targets {
		if target != host {
			t.Errorf("the proxy connected to %s", target)
		}
	}
	eventually(t, "the record", func() bool { return len(e.records.got()) == 1 })
	if r := e.records.got()[0]; r.Via != ViaProxy {
		t.Fatalf("record %+v", r)
	}
}

// TestSocketSecretsNotLogged covers C-24.FR-14, C-23.FR-3: a handshake refused with a body that echoes the address and
// the headers leaves neither the ticket nor a token in any log line or error.
func TestSocketSecretsNotLogged(t *testing.T) {
	const ticket = "9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	e := newSocketEnv(t)
	srv := startSocketStandIn(t, ticket, func(w http.ResponseWriter, r *http.Request, _ <-chan struct{}) {
		w.WriteHeader(http.StatusForbidden)
		//nolint:gosec // G705: the stand-in echoes the request on purpose, to show that no error repeats it
		_, _ = fmt.Fprintf(w, "refused %s %s %s", r.URL.String(), r.Header.Get("Authorization"), r.URL.RawQuery)
	})
	c := newClient(t, e.network(t), settings(srv.URL+"/api/"))
	e.source.set(socketConn(4, c))
	e.start(t)
	l := e.lines(t, "slack_socket_failed", 1)[0]
	failedLine(t, l, "CN0000000000K4", reasonDial, 1, true)
	if !strings.Contains(field(l, "error"), "answered 403") {
		t.Fatalf("error %q", field(l, "error"))
	}
	address, r := c.OpenSocket(t.Context())
	if !r.OK() || !strings.Contains(string(address), ticket) {
		t.Fatalf("OpenSocket = %+v", r)
	}
	_, err := c.DialSocket(t.Context(), address)
	if err == nil {
		t.Fatal("the refused handshake opened a socket")
	}
	if err := e.stop(t); err != nil {
		t.Fatal(err)
	}
	logged := e.log.String()
	for _, secret := range []string{ticket, testApp, testBot, testSigning} {
		if strings.Contains(logged, secret) || strings.Contains(err.Error(), secret) {
			t.Errorf("%s leaked: %s\n%v", secret, logged, err)
		}
	}
}

// TestDisconnectReason: an unknown reason of disconnect is logged as printable ASCII of at most 64 bytes.
func TestDisconnectReason(t *testing.T) {
	if got := disconnectReason("bad reason\né" + strings.Repeat("x", 100)); len(got) != maxDisconnectReasonText ||
		!strings.HasPrefix(got, "bad?reason???xx") {
		t.Fatalf("reason %q", got)
	}
	if recordURL("%zz") != "" {
		t.Fatal("an address that is no URL has a record URL")
	}
	if recordURL("wss://wss-primary.slack.com/link/?ticket=t") != "wss://wss-primary.slack.com/link/" {
		t.Fatal("the record URL keeps the query")
	}
}

// TestAddressSecrets: what a log may show of a socket's address is its query, as sent and unescaped, its ticket
// whatever its length, and its other values of 8 bytes or more, the longer first.
func TestAddressSecrets(t *testing.T) {
	for address, want := range map[string][]string{
		"wss://h/link/?ticket=a%2Fb&app_id=A1&x=0123456789": {"ticket=a%2Fb&app_id=A1&x=0123456789",
			"ticket=a/b&app_id=A1&x=0123456789", "0123456789", "a%2Fb", "a/b"},
		"wss://h/link/?ticket=%zz": {"ticket=%zz", "%zz"},
		"wss://h/link/":            nil,
		"%zz":                      nil,
	} {
		if got := addressSecrets(address); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", address, got, want)
		}
	}
	if got := maskWith(addressSecrets("wss://h/?ticket=t1&app_id=A1"), `reason = "bad ticket=t1&app_id=A1 t1"`); got !=
		`reason = "bad [redacted] [redacted]"` {
		t.Errorf("masked %q", got)
	}
}
