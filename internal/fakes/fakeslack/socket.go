// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeslack

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// Socket Mode of the fake (F-093, F-100 to F-102). apps.connections.open issues a single-use ticket and answers the
// address ws://<fake>/link/?ticket=…&app_id=…; /link/ accepts a WebSocket for it once and sends hello, whose
// num_connections counts the app's open sockets that said hello, this one included, and the app's extra_connections of
// /_fake/config: other programs connected to the same app. A ticket used before, or one the fake never issued, gets a
// socket that never says hello. An app that would count more than ten connections gets the disconnect
// too_many_websockets instead of hello, and the socket is closed. The fake answers pings, and an envelope is confirmed
// by {"envelope_id": …} on the socket it was sent on.
//
// GET /_fake/socket shows SocketState; POST /_fake/socket takes SocketScript and answers the state. POST /_fake/press
// sends its payload as an interactive envelope through the newest socket of the app that said hello when the app has
// no request_url, or when its transport is socket, and waits up to three seconds for the confirmation (presses.go).

const (
	// linkPath is the path of every Socket Mode address of the fake.
	linkPath = "/link/"
	// maxConnections is how many connections an app may have: one more gets too_many_websockets (F-102).
	maxConnections = 10
	// frameTimeout bounds the write of one frame.
	frameTimeout = 5 * time.Second
	// approximateConnectionTime is what hello tells of the time until a planned refresh, in seconds (F-100).
	approximateConnectionTime = 18060
)

// The reasons of disconnect the fake gives itself or names as a planned refresh (F-101, F-102).
const (
	reasonWarning          = "warning"
	reasonRefreshRequested = "refresh_requested"
	reasonTooMany          = "too_many_websockets"
)

// SocketState is GET /_fake/socket: the Socket Mode state of both apps, by key, and every envelope sent, in order.
type SocketState struct {
	Apps      map[string]AppSockets `json:"apps"`
	Envelopes []Envelope            `json:"envelopes"`
}

// AppSockets is the Socket Mode state of an app: its sockets open now, those opened and the hellos sent since the
// fake started, and whether another socket said hello while the socket of the last scripted refresh — a disconnect
// warning or refresh_requested — was still open; false until the first one.
type AppSockets struct {
	Open       int  `json:"open"`
	Opened     int  `json:"opened"`
	Hellos     int  `json:"hellos"`
	Overlapped bool `json:"overlapped"`
}

// Envelope is an envelope the fake sent: its app, envelope_id and type, and whether it was confirmed, with the time
// from sending to the confirmation in milliseconds, null until then.
type Envelope struct {
	App        string `json:"app"`
	EnvelopeID string `json:"envelope_id"`
	Type       string `json:"type"`
	Confirmed  bool   `json:"confirmed"`
	ConfirmMS  *int64 `json:"confirm_ms"`
}

// SocketScript is a POST of /_fake/socket for the app, b by default, carried out in this order: the next SkipHello
// sockets of the app get no hello and the next TooManyWebsockets get too_many_websockets, added to those already armed;
// Send sends an envelope through the newest socket of the app that said hello; Disconnect sends it disconnect with the
// reason and keeps it open, warning and refresh_requested starting a scripted refresh; Close ends the newest open
// socket of the app at once, without disconnect.
type SocketScript struct {
	App               string      `json:"app"`
	Disconnect        string      `json:"disconnect"`
	SkipHello         int         `json:"skip_hello"`
	TooManyWebsockets int         `json:"too_many_websockets"`
	Close             bool        `json:"close"`
	Send              *SocketSend `json:"send"`
}

// SocketSend is an envelope a script sends: its type, such as events_api, and its payload, {} when it has none.
type SocketSend struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// fakeSocket is an open WebSocket of /link/: its id, its app's key — empty when its address named no app of the fake
// — and whether it said hello; wmu orders the frames written to it, hello first.
type fakeSocket struct {
	id      int64
	app     string
	conn    *websocket.Conn
	helloed bool
	wmu     sync.Mutex
}

// write writes v to the socket as a JSON text frame, after any frame being written.
func (s *fakeSocket) write(ctx context.Context, v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return writeFrame(ctx, s.conn, v)
}

// appSocket is the Socket Mode state of an app: its sockets opened and hellos sent, the next sockets armed to get no
// hello or too_many_websockets, and the last scripted refresh.
type appSocket struct {
	opened, hellos     int
	skipHello, tooMany int
	refresh            *refreshWatch
}

// refreshWatch is a scripted refresh: the socket told to go, whether it has closed, and whether another socket of the
// app said hello while it was open.
type refreshWatch struct {
	socket     int64
	ended      bool
	overlapped bool
}

// envelopeState is an envelope sent: the socket it went through, when, and a channel closed by its confirmation.
type envelopeState struct {
	Envelope
	socket    int64
	sentAt    time.Time
	confirmed chan struct{}
}

// envelopeFrame is an envelope as Slack sends it over a socket (F-093).
type envelopeFrame struct {
	EnvelopeID             string          `json:"envelope_id"`
	Type                   string          `json:"type"`
	Payload                json.RawMessage `json:"payload"`
	AcceptsResponsePayload bool            `json:"accepts_response_payload"`
}

// appState is the Socket Mode state of the app key, under f.mu.
func (f *Fake) appState(key string) *appSocket {
	st, ok := f.appSockets[key]
	if !ok {
		st = &appSocket{}
		f.appSockets[key] = st
	}
	return st
}

// appKeyOf is the key of the app whose id is id, empty for another id.
func appKeyOf(id string) string {
	i := slices.IndexFunc(apps, func(a App) bool { return a.AppID == id })
	if i < 0 {
		return ""
	}
	return apps[i].Key
}

// link accepts a Socket Mode connection (F-100 to F-102) and reads it until it ends. The handler returns only then:
// net/http ends the request's context, which bounds the socket, when it does.
func (f *Fake) link(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	app, issued := f.tickets[q.Get("ticket")]
	delete(f.tickets, q.Get("ticket"))
	f.mu.Unlock()
	if !issued {
		app = appKeyOf(q.Get("app_id"))
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept answered the request
	}
	conn.SetReadLimit(maxBody)
	f.mu.Lock()
	f.socketSeq++
	s := &fakeSocket{id: f.socketSeq, app: app, conn: conn}
	f.sockets[s.id] = s
	if app != "" {
		f.appState(app).opened++
	}
	f.mu.Unlock()
	if issued {
		f.greet(r.Context(), s)
	}
	f.read(r.Context(), s)
}

// greet sends the new socket s hello; or too_many_websockets, and closes it, when the app is armed so or already
// counts ten connections; or nothing when it is armed so. The hello is counted, with the overlap of a scripted refresh,
// before it is written, so that what the other side does after it finds the state of the fake ready, and nothing else
// is written to the socket before it.
func (f *Fake) greet(ctx context.Context, s *fakeSocket) {
	a, _ := appByKey(s.app)
	f.mu.Lock()
	st := f.appState(s.app)
	n := f.config.ExtraConnections[s.app]
	for _, o := range f.sockets {
		if o.app == s.app && o.helloed {
			n++
		}
	}
	switch {
	case st.tooMany > 0 || n >= maxConnections:
		st.tooMany = max(st.tooMany-1, 0)
		f.mu.Unlock()
		_ = s.write(ctx, disconnectFrame(reasonTooMany)) // a socket that ended is closed below anyway
		f.closeSocket(s)
		_ = s.conn.Close(websocket.StatusNormalClosure, "")
		return
	case st.skipHello > 0:
		st.skipHello--
		f.mu.Unlock()
		return
	}
	s.helloed = true
	st.hellos++
	if w := st.refresh; w != nil && !w.ended && w.socket != s.id {
		w.overlapped = true
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	f.mu.Unlock()
	hello := map[string]any{"type": "hello", "num_connections": n + 1,
		"debug_info": map[string]any{"host": "fake-applink", "build_number": 1,
			"approximate_connection_time": approximateConnectionTime},
		"connection_info": map[string]any{"app_id": a.AppID}}
	_ = writeFrame(ctx, s.conn, hello) // a socket that ended is forgotten by the read loop
}

// read reads the socket s until it ends, confirming the envelopes that come back, and then forgets it.
func (f *Fake) read(ctx context.Context, s *fakeSocket) {
	defer func() {
		f.closeSocket(s)
		_ = s.conn.CloseNow() // already closed when the read failed for that reason
	}()
	for {
		typ, data, err := s.conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			f.confirm(s, data)
		}
	}
}

// confirm takes {"envelope_id": …} on the socket s as the confirmation of the envelope sent through it (F-093);
// anything else is ignored.
func (f *Fake) confirm(s *fakeSocket, data []byte) {
	var ack struct {
		EnvelopeID string `json:"envelope_id"`
	}
	if json.Unmarshal(data, &ack) != nil || ack.EnvelopeID == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.envelopeIDs[ack.EnvelopeID]
	if !ok || e.socket != s.id || e.Confirmed {
		return
	}
	ms := time.Since(e.sentAt).Milliseconds()
	e.Confirmed, e.ConfirmMS = true, &ms
	close(e.confirmed)
}

// closeSocket forgets the socket s, ending the scripted refresh that watches it; the caller closes its connection.
func (f *Fake) closeSocket(s *fakeSocket) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sockets[s.id]; !ok {
		return
	}
	delete(f.sockets, s.id)
	if st, ok := f.appSockets[s.app]; ok && st.refresh != nil && st.refresh.socket == s.id {
		st.refresh.ended = true
	}
}

// newest is the newest open socket of the app key, only among those that said hello when helloed; nil without one;
// under f.mu.
func (f *Fake) newest(key string, helloed bool) *fakeSocket {
	var out *fakeSocket
	for _, s := range f.sockets {
		if s.app == key && (s.helloed || !helloed) && (out == nil || s.id > out.id) {
			out = s
		}
	}
	return out
}

// Sockets is the Socket Mode state of the fake.
func (f *Fake) Sockets() SocketState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := SocketState{Apps: map[string]AppSockets{}, Envelopes: make([]Envelope, 0, len(f.envelopes))}
	for _, a := range apps {
		var as AppSockets
		if st, ok := f.appSockets[a.Key]; ok {
			as.Opened, as.Hellos = st.opened, st.hellos
			as.Overlapped = st.refresh != nil && st.refresh.overlapped
		}
		for _, s := range f.sockets {
			if s.app == a.Key {
				as.Open++
			}
		}
		out.Apps[a.Key] = as
	}
	for _, e := range f.envelopes {
		out.Envelopes = append(out.Envelopes, e.Envelope)
	}
	return out
}

// Script carries out the socket script s; it fails with the status 400 for an app other than a and b, a negative
// count, an envelope without a type or with a payload that is not JSON, and with 409 when the app has no socket the
// script needs or the socket ended meanwhile.
func (f *Fake) Script(ctx context.Context, s SocketScript) error {
	app, ok := appByKey(cmp.Or(s.App, AppB))
	switch {
	case !ok:
		return &pressError{http.StatusBadRequest, "app: " + s.App + " is not a or b"}
	case s.SkipHello < 0 || s.TooManyWebsockets < 0:
		return &pressError{http.StatusBadRequest, "skip_hello and too_many_websockets must not be negative"}
	case s.Send != nil && s.Send.Type == "":
		return &pressError{http.StatusBadRequest, "send: type is required"}
	case s.Send != nil && len(s.Send.Payload) > 0 && !json.Valid(s.Send.Payload):
		return &pressError{http.StatusBadRequest, "send: payload is not JSON"}
	}
	f.mu.Lock()
	var talk, last *fakeSocket
	if s.Send != nil || s.Disconnect != "" {
		if talk = f.newest(app.Key, true); talk == nil {
			f.mu.Unlock()
			return &pressError{http.StatusConflict, "the app has no socket that said hello"}
		}
	}
	if s.Close {
		if last = f.newest(app.Key, false); last == nil {
			f.mu.Unlock()
			return &pressError{http.StatusConflict, "the app has no open socket"}
		}
	}
	st := f.appState(app.Key)
	st.skipHello += s.SkipHello
	st.tooMany += s.TooManyWebsockets
	if s.Disconnect == reasonWarning || s.Disconnect == reasonRefreshRequested {
		st.refresh = &refreshWatch{socket: talk.id}
	}
	f.mu.Unlock()
	if s.Send != nil {
		if _, err := f.sendOn(ctx, talk, s.Send.Type, s.Send.Payload); err != nil {
			return err
		}
	}
	if s.Disconnect != "" {
		if err := talk.write(ctx, disconnectFrame(s.Disconnect)); err != nil {
			return &pressError{http.StatusConflict, "disconnect: the socket ended: " + err.Error()}
		}
	}
	if s.Close {
		f.closeSocket(last)
		_ = last.conn.CloseNow() // the read loop ends with it
	}
	return nil
}

// SendEnvelope sends an envelope of the type with the payload, {} when it is empty, through the newest socket of the
// app key that said hello; 409 when it has none or the socket ended meanwhile.
func (f *Fake) SendEnvelope(ctx context.Context, key, typ string, payload json.RawMessage) (Envelope, error) {
	f.mu.Lock()
	s := f.newest(key, true)
	f.mu.Unlock()
	if s == nil {
		return Envelope{}, &pressError{http.StatusConflict, "the app has no socket that said hello"}
	}
	return f.sendOn(ctx, s, typ, payload)
}

// sendOn sends an envelope of the type with the payload through the socket s.
func (f *Fake) sendOn(ctx context.Context, s *fakeSocket, typ string, payload json.RawMessage) (Envelope, error) {
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	e := &envelopeState{Envelope: Envelope{App: s.app, EnvelopeID: envelopeID(), Type: typ}, socket: s.id,
		confirmed: make(chan struct{})}
	f.mu.Lock()
	e.sentAt = time.Now()
	f.envelopes = append(f.envelopes, e)
	f.envelopeIDs[e.EnvelopeID] = e
	sent := e.Envelope
	f.mu.Unlock()
	err := s.write(ctx, envelopeFrame{EnvelopeID: e.EnvelopeID, Type: typ, Payload: payload})
	if err != nil {
		f.mu.Lock()
		f.envelopes = slices.DeleteFunc(f.envelopes, func(o *envelopeState) bool { return o == e })
		delete(f.envelopeIDs, e.EnvelopeID)
		f.mu.Unlock()
		return Envelope{}, &pressError{http.StatusConflict, "the socket ended: " + err.Error()}
	}
	return sent, nil
}

// WaitConfirmed waits until the envelope envelopeID is confirmed or ctx ends, and returns it with whether it was; an
// envelope the fake did not send is not.
func (f *Fake) WaitConfirmed(ctx context.Context, envelopeID string) (Envelope, bool) {
	f.mu.Lock()
	e, ok := f.envelopeIDs[envelopeID]
	f.mu.Unlock()
	if !ok {
		return Envelope{}, false
	}
	select {
	case <-e.confirmed:
	case <-ctx.Done():
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return e.Envelope, e.Confirmed
}

func (f *Fake) getSocket(w http.ResponseWriter, _ *http.Request) {
	fakeserver.WriteJSON(w, http.StatusOK, f.Sockets())
}

func (f *Fake) postSocket(w http.ResponseWriter, r *http.Request) {
	var s SocketScript
	if err := fakeserver.DecodeJSON(w, r, &s); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := f.Script(r.Context(), s); err != nil {
		status := http.StatusBadRequest
		if perr, ok := errors.AsType[*pressError](err); ok {
			status = perr.status
		}
		fakeserver.WriteError(w, status, err.Error())
		return
	}
	fakeserver.WriteJSON(w, http.StatusOK, f.Sockets())
}

// disconnectFrame is the frame disconnect with the reason (F-101).
func disconnectFrame(reason string) map[string]any {
	return map[string]any{"type": "disconnect", "reason": reason, "debug_info": map[string]any{"host": "fake-applink"}}
}

// writeFrame writes v as a JSON text frame to conn within frameTimeout.
func writeFrame(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, frameTimeout)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// envelopeID is a new envelope_id, a random UUID as Slack's look.
func envelopeID() string {
	h := randomHex(16)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
