// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math/rand/v2"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/requestlog"
)

// The waits of Socket Mode (C-24.FR-9, F-100), all on the real clock.
const (
	// HelloTimeout is slack.hello_timeout: how long a new connection may take to say hello before it is closed.
	HelloTimeout = 10 * time.Second
	// PingInterval is how often an open connection is pinged, and PingTimeout how long its pong may take.
	PingInterval = 30 * time.Second
	PingTimeout  = 10 * time.Second
	// SocketBackoffBase and SocketBackoffCap are slack.socket_backoff: the wait after a failed connection doubles
	// from the base up to the cap, with equal jitter, and a hello resets it.
	SocketBackoffBase = time.Second
	SocketBackoffCap  = 5 * time.Minute
	// SocketReadInterval is how often the Connections are read again without a wake.
	SocketReadInterval = time.Minute
	// confirmTimeout bounds the write of the confirmation of an envelope.
	confirmTimeout = 10 * time.Second
	// openTimeout bounds apps.connections.open, whose background class retries what is transient.
	openTimeout = 2 * CallTimeout
)

// The reasons of slack_socket_failed.
const (
	reasonDial         = "dial"
	reasonHelloTimeout = "hello_timeout"
	reasonLinkDisabled = "link_disabled"
	reasonTooMany      = "too_many_websockets"
	reasonClosed       = "closed"
)

// The frame types and the reasons of disconnect that Muster reads (F-093, F-100 to F-102).
const (
	frameHello              = "hello"
	frameDisconnect         = "disconnect"
	typeInteractive         = "interactive"
	disconnectWarning       = "warning"
	disconnectRefresh       = "refresh_requested"
	disconnectLinkDisabled  = "link_disabled"
	disconnectTooMany       = "too_many_websockets"
	maxDisconnectReasonText = 64
)

// The errors of a Connection without a client, of a confirmation that could not be written and of a ping without a
// pong.
var (
	errNoClient      = errors.New("the Connection has no client")
	errNotConfirmed  = errors.New("the confirmation of an envelope could not be written")
	errPingNoPong    = errors.New("no pong within " + PingTimeout.String())
	errClosedNoCause = errors.New("the connection ended")
)

// SocketConn is a Slack Connection in socket mode as its Socket Mode connection needs it: the Connection with its
// client, its version, and Err when its client could not be built.
type SocketConn struct {
	Conn
	Version int64
	Err     error
}

// SocketSource lists the Slack Connections in socket mode that are not deleted and records what a hello of one counted:
// the connections of its Slack app beyond those the Leader holds, for the version its socket read. *connections.Service
// implements it.
type SocketSource interface {
	SocketConnections(ctx context.Context) ([]SocketConn, error)
	RecordSocketHello(ctx context.Context, c SocketConn, otherConnections int) error
}

// Sockets holds the Socket Mode connections of an Organization's Slack Connections, the Leader task slack_socket_mode
// (C-24.FR-9, ADR-0007). Run holds one connection for each Connection in socket mode, and starts again for each new
// version of it: before each connection apps.connections.open answers its address, which is dialled through the
// Connection's proxy; the connection must say hello within HelloTimeout, is pinged every PingInterval and closed when a
// pong takes longer than PingTimeout. Every envelope with an envelope_id is confirmed with {"envelope_id": …} on its
// socket at once, before anything else is done with it; an interactive one is then handed to Presses in Base, as the
// callback of http mode hands over its presses, and any other type is ignored; each is recorded in the Request log.
// A disconnect warning or refresh_requested opens the next connection, with its hello, before the old one is closed;
// link_disabled, too_many_websockets, a failed dial, a missing hello and a connection that ended back off by
// slack.socket_backoff, reset by a hello, and log slack_socket_failed; nothing counts them and nothing becomes Broken.
// Each hello logs slack_socket_connected and stores, through Source, the connections of the app it counts beyond
// those held, logging slack_socket_shared when there are any.
//
// Running it on two replicas at once is safe: each press reaches one of their connections.
type Sockets struct {
	// Source lists the Connections and records their hellos.
	Source SocketSource
	// Presses handle the presses of the interactive envelopes; *Presses implements it.
	Presses Handler
	// Recorder keeps the records of the envelopes; nil records nothing.
	Recorder requestlog.EnvelopeRecorder
	// Base is the context of the process the presses are handled in, as the callback's: the start of a shutdown does
	// not end it, the end of its grace period does, so that a press Slack considers delivered is still handled after
	// the Leader stops; nil takes Run's context without its end.
	Base context.Context //nolint:containedctx // the process context the presses outlive the Leader in
	// Real is the real clock that times the confirmations.
	Real clock.Clock
	Log  *logging.Logger
	// HelloTimeout, BackoffBase, BackoffCap and Every replace HelloTimeout, SocketBackoffBase, SocketBackoffCap and
	// SocketReadInterval when they are set.
	HelloTimeout, BackoffBase, BackoffCap, Every time.Duration

	// after arms a timer on the real clock and returns its channel and its stop; jitter spreads a back-off. Tests
	// replace them.
	after  func(d time.Duration) (<-chan time.Time, func())
	jitter func(d time.Duration) time.Duration

	wakeOnce sync.Once
	wake     chan struct{}
}

func (s *Sockets) wakes() chan struct{} {
	s.wakeOnce.Do(func() { s.wake = make(chan struct{}, 1) })
	return s.wake
}

// Wake reads the Connections again at once: a Connection changed, maybe on another replica.
func (s *Sockets) Wake() {
	select {
	case s.wakes() <- struct{}{}:
	default:
	}
}

// Run holds the connections until ctx ends, which is when the Leader loses the lock or stops, and closes every one of
// them at once before it returns, after their readers, pingers and records have ended: a holder starts for each
// Connection in socket mode, starts again when the Connection's version changes and stops when it leaves socket mode,
// is deleted, or its client cannot be built — logged once for each version. The Connections are read again on Wake
// and every Every. A failed read of the Connections stops every holder and returns its error, so that the Leader task
// runs again.
func (s *Sockets) Run(ctx context.Context) error {
	base := s.Base
	if base == nil {
		base = context.WithoutCancel(ctx)
	}
	every := s.Every
	if every <= 0 {
		every = SocketReadInterval
	}
	running := map[int64]*holder{}
	broken := map[int64]int64{}
	stop := func(ids ...int64) {
		for _, id := range ids {
			running[id].cancel()
		}
		for _, id := range ids {
			<-running[id].done
			delete(running, id)
		}
	}
	defer func() { stop(slices.Collect(maps.Keys(running))...) }()
	for {
		list, err := s.Source.SocketConnections(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		want := make(map[int64]SocketConn, len(list))
		listed := make(map[int64]bool, len(list))
		for _, c := range list {
			listed[c.ID] = true
			if c.Err == nil && c.Client == nil {
				c.Err = errNoClient
			}
			if c.Err == nil {
				want[c.ID] = c
				delete(broken, c.ID)
				continue
			}
			if v, ok := broken[c.ID]; !ok || v != c.Version {
				broken[c.ID] = c.Version
				s.failed(ctx, c, reasonDial, every, c.Err)
			}
		}
		maps.DeleteFunc(broken, func(id, _ int64) bool { return !listed[id] })
		var gone []int64
		for id, h := range running {
			if c, ok := want[id]; !ok || c.Version != h.c.Version {
				gone = append(gone, id)
			}
		}
		stop(gone...)
		for id, c := range want {
			if _, ok := running[id]; ok {
				continue
			}
			hctx, cancel := context.WithCancel(ctx)
			h := &holder{s: s, c: c, base: base, cancel: cancel, done: make(chan struct{}),
				open: map[*socket]struct{}{}}
			running[id] = h
			go func() {
				defer close(h.done)
				h.run(hctx)
			}()
		}
		if !s.waitRead(ctx, every) {
			return nil
		}
	}
}

// waitRead waits for a wake or for every, and reports false when ctx ended first.
func (s *Sockets) waitRead(ctx context.Context, every time.Duration) bool {
	t, stop := s.timer(every)
	defer stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wakes():
	case <-t:
	}
	return true
}

// holder holds the Socket Mode connection of one version of a Connection: the one in use, the next one while a
// planned refresh opens it, and the old ones while they close. held counts the sockets whose reader runs; bg the
// closes of old sockets and the writes of records, which the holder waits for before it ends.
type holder struct {
	s      *Sockets
	c      SocketConn
	base   context.Context //nolint:containedctx // the process context the presses are handled in
	cancel context.CancelFunc
	done   chan struct{}

	held atomic.Int32
	mu   sync.Mutex
	open map[*socket]struct{}
	bg   sync.WaitGroup
}

// socket is one Socket Mode connection: its WebSocket, the address of its records, its context, which its reader and
// pinger end with, and what its reader tells the holder — that its first hello came and was counted, and once the
// first disconnect reason, or "" when it ended.
type socket struct {
	conn   *websocket.Conn
	url    string
	ctx    context.Context //nolint:containedctx // the lifetime of the socket, which its reader and pinger share
	cancel context.CancelFunc
	hello  chan struct{}
	stop   chan string
	ended  chan struct{}
	pinged chan struct{}

	mu     sync.Mutex
	endErr error
}

// signal tells the holder reason, unless it was told something it has not read yet.
func (sc *socket) signal(reason string) {
	select {
	case sc.stop <- reason:
	default:
	}
}

// end notes why the socket ended, the first cause only, and tells the holder.
func (sc *socket) end(err error) {
	sc.mu.Lock()
	if sc.endErr == nil {
		sc.endErr = err
	}
	sc.mu.Unlock()
	sc.signal("")
}

func (sc *socket) cause() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.endErr == nil {
		return errClosedNoCause
	}
	return sc.endErr
}

// run holds the connection until ctx ends, and then closes every socket at once and waits for their goroutines and
// records.
func (h *holder) run(ctx context.Context) {
	defer h.bg.Wait()
	defer h.closeAll()
	var cur *socket
	refreshing := false
	failures := 0
	for ctx.Err() == nil {
		if cur == nil || refreshing {
			next, reason, err := h.connect(ctx)
			if ctx.Err() != nil {
				return
			}
			if next == nil {
				failures++
				wait := h.s.backoff(failures)
				h.s.failed(ctx, h.c, reason, wait, err)
				if !h.s.pause(ctx, wait) {
					return
				}
				continue
			}
			failures = 0
			if cur != nil {
				h.retire(cur)
			}
			cur, refreshing = next, false
			continue
		}
		select {
		case <-ctx.Done():
			return
		case reason := <-cur.stop:
			if ctx.Err() != nil {
				return
			}
			if reason == disconnectWarning || reason == disconnectRefresh {
				refreshing = true
				continue
			}
			h.closeNow(cur)
			why, err := failureOf(reason, cur)
			cur = nil
			failures++
			wait := h.s.backoff(failures)
			h.s.failed(ctx, h.c, why, wait, err)
			if !h.s.pause(ctx, wait) {
				return
			}
		}
	}
}

// connect opens a new connection: apps.connections.open, the dial, and its hello within the hello timeout. It returns
// the socket, or why it failed — the reason of slack_socket_failed and what failed, masked.
func (h *holder) connect(ctx context.Context) (*socket, string, error) {
	octx, cancel := context.WithTimeout(ctx, openTimeout)
	address, r := h.c.Client.OpenSocket(octx)
	cancel()
	if !r.OK() {
		text := string(r.Error)
		if !strings.Contains(text, "apps.connections.open") {
			text = "apps.connections.open: " + text
		}
		return nil, reasonDial, errors.New(text)
	}
	conn, err := h.c.Client.DialSocket(ctx, address)
	if err != nil {
		return nil, reasonDial, err
	}
	sctx, scancel := context.WithCancel(ctx)
	sc := &socket{conn: conn, url: recordURL(string(address)), ctx: sctx, cancel: scancel, hello: make(chan struct{}, 1),
		stop: make(chan string, 1), ended: make(chan struct{}), pinged: make(chan struct{})}
	h.mu.Lock()
	h.open[sc] = struct{}{}
	h.mu.Unlock()
	h.held.Add(1)
	go h.read(sc)
	go h.ping(sc)
	timeout := h.s.HelloTimeout
	if timeout <= 0 {
		timeout = HelloTimeout
	}
	timer, stop := h.s.timer(timeout)
	defer stop()
	select {
	case <-sc.hello:
		return sc, "", nil
	case reason := <-sc.stop:
		h.closeNow(sc)
		why, err := failureOf(reason, sc)
		return nil, why, err
	case <-timer:
		h.closeNow(sc)
		return nil, reasonHelloTimeout, nil
	case <-ctx.Done():
		h.closeNow(sc)
		return nil, "", ctx.Err()
	}
}

// failureOf is the reason of slack_socket_failed and what failed for a socket that told the holder reason: a disconnect
// reason, or "" when it ended.
func failureOf(reason string, sc *socket) (string, error) {
	switch reason {
	case disconnectLinkDisabled:
		return reasonLinkDisabled, nil
	case disconnectTooMany:
		return reasonTooMany, nil
	case "":
		return reasonClosed, sc.cause()
	}
	return reasonClosed, errors.New("disconnect " + disconnectReason(reason))
}

// disconnectReason is a reason of disconnect Muster does not know, as a log line shows it: printable ASCII, at most
// maxDisconnectReasonText bytes.
func disconnectReason(reason string) string {
	b := []byte(reason)
	if len(b) > maxDisconnectReasonText {
		b = b[:maxDisconnectReasonText]
	}
	for i, c := range b {
		if c <= ' ' || c > '~' {
			b[i] = '?'
		}
	}
	return string(b)
}

// recordURL is the address of a socket as its records show it: the scheme, the host and the path, without the query.
func recordURL(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// closeNow closes the socket sc at once and waits for its reader and pinger.
func (h *holder) closeNow(sc *socket) {
	sc.cancel()
	_ = sc.conn.CloseNow() // closed already when its reader or pinger closed it
	<-sc.ended
	<-sc.pinged
	h.forget(sc)
}

// retire closes the old socket sc of a planned refresh with a normal closure, in the background: it keeps reading and
// confirming until then.
func (h *holder) retire(sc *socket) {
	h.bg.Go(func() {
		_ = sc.conn.Close(websocket.StatusNormalClosure, "") // a socket that ended meanwhile is closed already
		h.closeNow(sc)
	})
}

// closeAll closes every socket of the holder at once and waits for their readers and pingers.
func (h *holder) closeAll() {
	h.mu.Lock()
	all := slices.Collect(maps.Keys(h.open))
	h.mu.Unlock()
	for _, sc := range all {
		sc.cancel()
		_ = sc.conn.CloseNow() // closed already when its reader or pinger closed it
	}
	for _, sc := range all {
		<-sc.ended
		<-sc.pinged
		h.forget(sc)
	}
}

func (h *holder) forget(sc *socket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.open, sc)
}

// frame is the part of a frame of Socket Mode that Muster reads (F-093, F-100, F-101).
type frame struct {
	Type           string          `json:"type"`
	EnvelopeID     string          `json:"envelope_id"`
	Reason         string          `json:"reason"`
	NumConnections int             `json:"num_connections"`
	Payload        json.RawMessage `json:"payload"`
}

// read reads the frames of sc until it ends: each envelope with an envelope_id is confirmed and handled; each hello is
// counted, in order, and the first then tells the holder that waits for it; a disconnect tells the holder its reason.
// Binary frames and frames that are not JSON are ignored. A confirmation that cannot be written ends the socket.
func (h *holder) read(sc *socket) {
	defer func() {
		h.held.Add(-1)
		close(sc.ended)
	}()
	first := true
	for {
		typ, data, err := sc.conn.Read(sc.ctx)
		if err != nil {
			sc.end(err)
			return
		}
		var f frame
		if typ != websocket.MessageText || json.Unmarshal(data, &f) != nil {
			continue
		}
		switch {
		case f.EnvelopeID != "":
			if !h.envelope(sc, data, f) {
				sc.end(errNotConfirmed)
				_ = sc.conn.CloseNow() // the holder closes the socket again
				return
			}
		case f.Type == frameHello:
			h.helloed(sc.ctx, f.NumConnections)
			if first {
				first = false
				sc.hello <- struct{}{}
			}
		case f.Type == frameDisconnect:
			sc.signal(f.Reason)
		}
	}
}

// envelope confirms the envelope f, received as data on sc, at once, then hands the payload of an interactive one that
// is a JSON object to Presses in the process's context, and records it; it reports false when the confirmation could
// not be written, and then does nothing else.
func (h *holder) envelope(sc *socket, data []byte, f frame) bool {
	start := h.s.Real.Now()
	ack, _ := json.Marshal(struct { // a string
		EnvelopeID string `json:"envelope_id"`
	}{f.EnvelopeID})
	wctx, cancel := context.WithTimeout(sc.ctx, confirmTimeout)
	err := sc.conn.Write(wctx, websocket.MessageText, ack)
	cancel()
	if err != nil {
		return false
	}
	latency := h.s.Real.Now().Sub(start)
	if f.Type == typeInteractive && jsonObject(f.Payload) {
		h.s.Presses.Go(h.base, h.c.Conn, slices.Clone(f.Payload))
	}
	if h.s.Recorder != nil {
		rec := requestlog.EnvelopeRecord{Links: requestlog.Links{Connection: h.c.ID, ConnectionID: h.c.PublicID},
			Operation: f.Type, URL: sc.url, Envelope: data, Confirmation: ack, Via: h.c.Client.Via(),
			Latency: latency}
		ctx := context.WithoutCancel(sc.ctx)
		h.bg.Go(func() { h.s.Recorder.RecordEnvelope(ctx, rec) })
	}
	return true
}

// jsonObject reports whether p is a JSON object.
func jsonObject(p []byte) bool {
	return json.Valid(p) && bytes.HasPrefix(bytes.TrimLeft(p, " \t\r\n"), []byte("{"))
}

// ping pings sc every PingInterval until it ends; a ping without a pong within PingTimeout ends it.
func (h *holder) ping(sc *socket) {
	defer close(sc.pinged)
	for {
		tick, stop := h.s.timer(PingInterval)
		select {
		case <-sc.ctx.Done():
			stop()
			return
		case <-tick:
		}
		if err := h.pingOnce(sc); err != nil {
			if sc.ctx.Err() == nil {
				sc.end(err)
				_ = sc.conn.CloseNow() // the holder closes the socket again
			}
			return
		}
	}
}

// pingOnce pings sc and waits for the pong for at most PingTimeout. A ping past its deadline ends the socket as one
// without a pong before its context is cancelled, since a cancelled write closes the connection at once.
func (h *holder) pingOnce(sc *socket) error {
	pctx, cancel := context.WithCancel(sc.ctx)
	defer cancel()
	deadline, stop := h.s.timer(PingTimeout)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- sc.conn.Ping(pctx) }()
	select {
	case err := <-done:
		return err
	case <-deadline:
		sc.end(errPingNoPong)
		cancel()
		<-done
		return errPingNoPong
	}
}

// helloed records what a hello of the holder's connection counted: num_connections n, of which the holder holds
// those whose reader runs — two while a planned refresh overlaps.
func (h *holder) helloed(ctx context.Context, n int) {
	held := int(h.held.Load())
	other := max(n-held, 0)
	err := h.s.Source.RecordSocketHello(ctx, h.c, other)
	fields := []logging.Field{logging.F("connection", h.c.PublicID), logging.F("num_connections", n),
		logging.F("held", held)}
	if err != nil {
		fields = append(fields, logging.F("error", h.c.Client.mask(err.Error())))
	}
	h.s.Log.Log(ctx, logging.SlackSocketConnected, fields...)
	if other > 0 {
		h.s.Log.Log(ctx, logging.SlackSocketShared, logging.F("connection", h.c.PublicID),
			logging.F("other_connections", other))
	}
}

// failed logs slack_socket_failed for the Connection c: the reason, the wait before the next connection and what
// failed, masked of the client's secrets; none for a reason that says it all.
func (s *Sockets) failed(ctx context.Context, c SocketConn, reason string, wait time.Duration, err error) {
	fields := []logging.Field{logging.F("connection", c.PublicID), logging.F("reason", reason),
		logging.F("backoff_seconds", wait.Seconds())}
	if err != nil {
		text := cut(err.Error())
		if c.Client != nil {
			text = c.Client.mask(err.Error())
		}
		fields = append(fields, logging.F("error", text))
	}
	s.Log.Log(ctx, logging.SlackSocketFailed, fields...)
}

// backoff is the wait after n failures in a row: from BackoffBase doubling up to BackoffCap, with equal jitter.
func (s *Sockets) backoff(n int) time.Duration {
	d, ceiling := s.BackoffBase, s.BackoffCap
	if d <= 0 {
		d = SocketBackoffBase
	}
	if ceiling <= 0 {
		ceiling = SocketBackoffCap
	}
	for i := 1; i < n && d < ceiling; i++ {
		d *= 2
	}
	d = min(d, ceiling)
	jitter := s.jitter
	if jitter == nil {
		jitter = socketJitter
	}
	return d/2 + jitter(d/2)
}

// pause waits for d unless ctx ends first, and reports whether it waited.
func (s *Sockets) pause(ctx context.Context, d time.Duration) bool {
	t, stop := s.timer(d)
	defer stop()
	select {
	case <-ctx.Done():
		return false
	case <-t:
		return true
	}
}

// timer arms a timer for d on the real clock.
func (s *Sockets) timer(d time.Duration) (<-chan time.Time, func()) {
	if s.after != nil {
		return s.after(d)
	}
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// socketJitter returns a random duration in [0, d].
func socketJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return rand.N(d + 1) //nolint:gosec // G404: jitter spreads reconnections; it needs no cryptographic randomness
}
