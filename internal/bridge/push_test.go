// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/webhooks"
)

// The public_ids of the Connections of the push tests.
const (
	pushID    = "CN0000000000B3"
	pollingID = "CN0000000000P1"
	brokenID  = "CN0000000000E1"
)

// pushSource finds the Connections of a push as *connections.Service does: pushID in push mode, with its client;
// pollingID in poll mode, without one, since the secrets of a Connection in poll mode are never opened for a push;
// brokenID whose read fails; any other is unknown, deleted or not a Bridge Connection. It counts the reads.
type pushSource struct {
	conn Conn

	mu    sync.Mutex
	reads int
}

func (s *pushSource) PushConnection(_ context.Context, publicID string) (Conn, string, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	switch publicID {
	case pushID:
		return s.conn, EventDeliveryPush, nil
	case pollingID:
		return Conn{ID: 4, PublicID: pollingID, Version: s.conn.Version}, EventDeliveryPoll, nil
	case brokenID:
		return Conn{}, "", errors.New("the database went away")
	}
	return Conn{}, "", ErrNoConnection
}

func (s *pushSource) PollingConnections(context.Context) ([]Polled, error) { return nil, nil }

func (s *pushSource) StoreEventsCursor(context.Context, Conn, *string) error { return nil }

func (s *pushSource) HandleEventOnce(context.Context, int64, string, func(context.Context) error) (bool, error) {
	return false, errors.New("the push callback claims nothing itself")
}

// pushEnv is the push callback on a mux as the ingest listener mounts it, over Events whose real clock is two hours
// past the events, so that each press it handles logs bridge_press_dropped once; the callback's real clock is now.
type pushEnv struct {
	*eventsEnv
	handler http.Handler
	source  *pushSource
	now     time.Time
	cfg     PushConfig
}

var pushNow = time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)

func newPushEnv(t *testing.T) *pushEnv {
	t.Helper()
	e := &pushEnv{eventsEnv: newEventsEnv(eventTime.Add(2 * EventMaxAge)), now: pushNow}
	e.conn.Client = newClient(t, network(t, nil), Settings{BaseURL: "http://127.0.0.1:9", Token: testToken,
		PushSecret: testPushSecret})
	e.source = &pushSource{conn: e.conn}
	e.cfg = PushConfig{Connections: e.source, Events: e.events, Real: clock.NewManual(e.now), Log: e.events.Log}
	e.mount()
	return e
}

func (e *pushEnv) mount() {
	mux := http.NewServeMux()
	mux.Handle(PushPattern, NewPush(e.cfg))
	e.handler = mux
}

// pushed is a push to the Connection publicID with body and the three headers; an empty header is left out.
type pushed struct {
	publicID, id, timestamp, signature string
	body                               []byte
}

// signed is a push of body to pushID with webhook-id id, signed with the push secret at at.
func signed(t *testing.T, id string, at time.Time, body []byte) pushed {
	t.Helper()
	sig, err := webhooks.Sign([]logging.Secret{testPushSecret}, id, at, body)
	if err != nil {
		t.Fatal(err)
	}
	return pushed{publicID: pushID, id: id, timestamp: strconv.FormatInt(at.Unix(), 10), signature: sig, body: body}
}

func (e *pushEnv) send(p pushed) *httptest.ResponseRecorder {
	return e.sendContext(context.Background(), p)
}

func (e *pushEnv) sendContext(ctx context.Context, p pushed) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, e.request(ctx, p))
	return w
}

// request is the request of the push p.
func (e *pushEnv) request(ctx context.Context, p pushed) *http.Request {
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, CallbackPath+p.publicID, bytes.NewReader(p.body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range map[string]string{webhooks.HeaderID: p.id, webhooks.HeaderTimestamp: p.timestamp,
		webhooks.HeaderSignature: p.signature} {
		if v != "" {
			r.Header.Set(k, v)
		}
	}
	return r
}

// watchedWriter is a ResponseRecorder that notes when the answer starts to be written.
type watchedWriter struct {
	*httptest.ResponseRecorder
	written *atomic.Bool
}

func (w watchedWriter) WriteHeader(code int) {
	w.written.Store(true)
	w.ResponseRecorder.WriteHeader(code)
}

func (w watchedWriter) Write(b []byte) (int, error) {
	w.written.Store(true)
	return w.ResponseRecorder.Write(b)
}

// TestAnswerBefore204 covers C-22.FR-11 and FR-13 in push mode: a pushed event is answered once its claim committed
// and before the 204 that acknowledges it is written; a copy of it is acknowledged without a second answer.
func TestAnswerBefore204(t *testing.T) {
	e := newPushEnv(t)
	var written atomic.Bool
	var mu sync.Mutex
	var during []bool
	b := startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		during = append(during, written.Load())
		mu.Unlock()
		answerJSON(http.StatusOK, "application/json", `{"shown_as":"ephemeral","replayed":false}`)(w, r)
	})
	e.conn.Client = newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken,
		PushSecret: testPushSecret})
	e.source.conn = e.conn
	body := eventJSON(t, eventFixture(TypePress, member("occurred_at", eventTime.Add(2*EventMaxAge).Format(time.RFC3339)),
		member("data", e.sign(t, buttons.SubjectTest, pressDest.PublicID, buttons.CommandAcknowledge, 0))))
	for range 2 {
		w := watchedWriter{ResponseRecorder: httptest.NewRecorder(), written: &written}
		e.handler.ServeHTTP(w, e.request(t.Context(), signed(t, "ev-1", e.now, body)))
		if w.Code != http.StatusNoContent {
			t.Fatalf("answered %d %s", w.Code, w.Body.String())
		}
		written.Store(false)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(during, []bool{false}) {
		t.Errorf("the answers came with the 204 written: %v", during)
	}
}

// answerProblem decodes the problem of an answer of the push callback.
func answerProblem(t *testing.T, w *httptest.ResponseRecorder) problem {
	t.Helper()
	var p problem
	if w.Header().Get("Content-Type") != problemType || w.Header().Get("Cache-Control") != "no-store" ||
		json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Status != w.Code {
		t.Fatalf("not a problem: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	return p
}

// TestPushHandlesASignedEvent covers C-22.FR-11 and AC-14: a pushed event with a valid Standard Webhooks signature is
// handled and answered 204, and the same webhook-id again — a retry, signed anew — is answered 204 without handling it
// twice.
func TestPushHandlesASignedEvent(t *testing.T) {
	e := newPushEnv(t)
	body := eventJSON(t, eventFixture(TypePress))
	w := e.send(signed(t, "ev-1", e.now, body))
	if w.Code != http.StatusNoContent || w.Header().Get("Cache-Control") != "no-store" || w.Body.Len() != 0 {
		t.Fatalf("answered %d %s", w.Code, w.Body.String())
	}
	if !e.store.recorded(e.conn.ID, "ev-1") {
		t.Fatal("the event was not recorded")
	}
	retry := resign(t, signed(t, "ev-1", e.now, body), strconv.FormatInt(e.now.Add(time.Minute).Unix(), 10))
	if w := e.send(retry); w.Code != http.StatusNoContent {
		t.Fatalf("the retry answered %d", w.Code)
	}
	if n := strings.Count(e.log.String(), `"event":"bridge_press_dropped"`); n != 1 ||
		strings.Contains(e.log.String(), "bridge_event_rejected") {
		t.Errorf("handled %d times: %s", n, e.log.String())
	}
}

// TestPushRefusals covers C-22.FR-11, AC-3 and AC-14: a wrong signature, a missing header, a webhook-timestamp 6
// minutes from the real clock either way or not in seconds, an unknown, deleted or other Connection, a malformed id
// and a Connection in poll mode are each answered 401 with the same body, change nothing and log bridge_event_rejected
// with the reason, never the signature or the secret; an id that is not a public_id is logged as empty.
func TestPushRefusals(t *testing.T) {
	body := eventJSON(t, eventFixture(TypeMessageDeleted))
	now := pushNow
	other, err := webhooks.Sign([]logging.Secret{"whsec_b3RoZXItc2VjcmV0LW9mLWFub3RoZXItYnJpZGdl"}, "ev-1", now, body)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		change     func(p *pushed)
		connection string
		reason     string
	}{
		{"a wrong signature", func(p *pushed) { p.signature = "v1,AAAA" }, pushID, "signature"},
		{"another secret's signature", func(p *pushed) { p.signature = other }, pushID, "signature"},
		{"a signature of another body", func(p *pushed) { p.body = append(bytes.Clone(p.body), ' ') }, pushID,
			"signature"},
		{"a signature of another id", func(p *pushed) { p.id = "ev-2" }, pushID, "signature"},
		{"another version", func(p *pushed) { p.signature = "v2," + strings.TrimPrefix(p.signature, "v1,") }, pushID,
			"signature"},
		{"no webhook-id", func(p *pushed) { p.id = "" }, pushID, "signature"},
		{"no webhook-timestamp", func(p *pushed) { p.timestamp = "" }, pushID, "signature"},
		{"no webhook-signature", func(p *pushed) { p.signature = "" }, pushID, "signature"},
		{"a timestamp 6 minutes behind", func(p *pushed) { *p = signed(t, "ev-1", now.Add(-6*time.Minute), p.body) },
			pushID, "timestamp"},
		{"a timestamp 6 minutes ahead", func(p *pushed) { *p = signed(t, "ev-1", now.Add(6*time.Minute), p.body) },
			pushID, "timestamp"},
		{"a timestamp that is not seconds", func(p *pushed) { *p = resign(t, *p, "16e8") }, pushID, "timestamp"},
		{"a timestamp of 13 digits", func(p *pushed) { *p = resign(t, *p, "1000000000000") }, pushID, "timestamp"},
		{"an unknown Connection", func(p *pushed) { p.publicID = "CN0000000000Z9" }, "CN0000000000Z9",
			"unknown_connection"},
		{"an id in lower case", func(p *pushed) { p.publicID = "cn0000000000z9" }, "CN0000000000Z9",
			"unknown_connection"},
		{"an id that is no public_id", func(p *pushed) { p.publicID = "whsec_x" }, "", "unknown_connection"},
		{"a Connection in poll mode", func(p *pushed) { p.publicID = pollingID }, pollingID, "poll_mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPushEnv(t)
			p := signed(t, "ev-1", now, body)
			tc.change(&p)
			w := e.send(p)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("answered %d %s", w.Code, w.Body.String())
			}
			pr := answerProblem(t, w)
			if pr.Type != problemBase+"unauthenticated" ||
				pr.Detail != "The signature, the Connection or its event delivery mode is wrong." {
				t.Errorf("problem %+v", pr)
			}
			if e.store.recorded(e.conn.ID, "ev-1") || e.store.recorded(4, "ev-1") {
				t.Error("a refused event was recorded")
			}
			want := `"level":"INFO","event":"bridge_event_rejected","connection":"` + tc.connection +
				`","reason":"` + tc.reason + `"}`
			log := e.log.String()
			if !strings.Contains(log, want) || strings.Count(log, "\n") != 1 {
				t.Errorf("logged %s", log)
			}
			for _, secret := range []string{testPushSecret, strings.TrimPrefix(testPushSecret, "whsec_"), p.signature,
				string(body), testToken} {
				if secret != "" && strings.Contains(log+w.Body.String(), secret) {
					t.Errorf("the answer or the log carries %q", secret)
				}
			}
		})
	}
}

// resign is p with the timestamp ts as sent, signed over it.
func resign(t *testing.T, p pushed, ts string) pushed {
	t.Helper()
	p.timestamp, p.signature = ts, signWith(t, p.id, ts, p.body)
	return p
}

// signWith is the v1 signature of id, the timestamp ts and body under the push secret, computed apart from
// internal/webhooks, as a bridge does.
func signWith(t *testing.T, id, ts string, body []byte) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(testPushSecret, "whsec_"))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// TestPushSignatures covers C-22.FR-11: any of several space-separated signatures may match, as while a bridge rotates
// its secret; and a webhook-timestamp just within bridge.push_tolerance, either way, is fresh.
func TestPushSignatures(t *testing.T) {
	e := newPushEnv(t)
	body := eventJSON(t, eventFixture(TypeMessageDeleted))
	p := signed(t, "ev-1", e.now, body)
	p.signature = "v1,AAAA " + p.signature + " v2,BBBB"
	if w := e.send(p); w.Code != http.StatusNoContent {
		t.Fatalf("several signatures answered %d", w.Code)
	}
	for i, at := range []time.Time{e.now.Add(-PushTolerance), e.now.Add(PushTolerance)} {
		id := "ev-edge-" + strconv.Itoa(i)
		if w := e.send(signed(t, id, at, eventJSON(t, eventFixture(TypeMessageDeleted, member("id", id))))); w.Code !=
			http.StatusNoContent {
			t.Errorf("a timestamp %v from the clock answered %d", at.Sub(e.now), w.Code)
		}
	}
	e.cfg.Tolerance = time.Minute
	e.mount()
	if w := e.send(signed(t, "ev-9", e.now.Add(-2*time.Minute),
		eventJSON(t, eventFixture(TypeMessageDeleted, member("id", "ev-9"))))); w.Code != http.StatusUnauthorized {
		t.Errorf("a timestamp past Tolerance answered %d", w.Code)
	}
}

// TestPushNotAnEvent covers C-22.FR-11 and AC-14: a signed body that is no event of the protocol is answered 400 and
// logged as not_an_event; a body larger than the limit is answered 400 as too_large before anything is read, whatever
// the Connection.
func TestPushNotAnEvent(t *testing.T) {
	e := newPushEnv(t)
	w := e.send(signed(t, "ev-2", e.now, []byte("not json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("answered %d", w.Code)
	}
	if p := answerProblem(t, w); p.Type != problemBase+"validation-failed" ||
		p.Detail != "The body is not an event of the Bridge protocol." {
		t.Errorf("problem %+v", p)
	}
	if !strings.Contains(e.log.String(), `"connection":"CN0000000000B3","reason":"not_an_event"`) {
		t.Errorf("logged %s", e.log.String())
	}

	e = newPushEnv(t)
	big := eventJSON(t, eventFixture(TypeMessageDeleted, member("pad", strings.Repeat("x", MaxEventBytes))))
	p := signed(t, "ev-1", e.now, big)
	p.publicID = "CN0000000000Z9"
	if w := e.send(p); w.Code != http.StatusBadRequest {
		t.Fatalf("a large body answered %d", w.Code)
	}
	e.source.mu.Lock()
	reads := e.source.reads
	e.source.mu.Unlock()
	if !strings.Contains(e.log.String(), `"connection":"CN0000000000Z9","reason":"too_large"`) || reads != 0 {
		t.Errorf("%d reads, logged %s", reads, e.log.String())
	}
	e.cfg.BodyLimit = 64
	e.mount()
	small := eventJSON(t, eventFixture(TypeMessageDeleted))
	if w := e.send(signed(t, "ev-1", e.now, small)); w.Code != http.StatusBadRequest ||
		e.store.recorded(e.conn.ID, "ev-1") {
		t.Errorf("a body past BodyLimit answered %d", w.Code)
	}
}

// TestPushIDMismatch covers C-22.FR-11: a signed event whose id is not webhook-id is answered 400 and is not handled.
func TestPushIDMismatch(t *testing.T) {
	e := newPushEnv(t)
	w := e.send(signed(t, "ev-2", e.now, eventJSON(t, eventFixture(TypeMessageDeleted))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("answered %d", w.Code)
	}
	if p := answerProblem(t, w); p.Detail != "The id of the event is not webhook-id." {
		t.Errorf("problem %+v", p)
	}
	if e.store.recorded(e.conn.ID, "ev-1") || e.store.recorded(e.conn.ID, "ev-2") ||
		!strings.Contains(e.log.String(), `"reason":"id_mismatch"`) {
		t.Errorf("logged %s", e.log.String())
	}
}

// TestPushHandlingFails covers C-22.FR-11 and AC-14: an event whose handling fails is answered 500 and is not recorded
// as seen, and the bridge's retry handles it; a Connection that cannot be read is answered 500 too.
func TestPushHandlingFails(t *testing.T) {
	e := newPushEnv(t)
	e.store.failRecord = 1
	body := eventJSON(t, eventFixture(TypePress))
	w := e.send(signed(t, "ev-1", e.now, body))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("answered %d", w.Code)
	}
	if p := answerProblem(t, w); p.Type != problemBase+"internal" {
		t.Errorf("problem %+v", p)
	}
	if e.store.recorded(e.conn.ID, "ev-1") ||
		!strings.Contains(e.log.String(), `"event":"bridge_event_failed","connection":"CN0000000000B3"`) {
		t.Fatalf("logged %s", e.log.String())
	}
	if w := e.send(signed(t, "ev-1", e.now.Add(30*time.Second), body)); w.Code != http.StatusNoContent ||
		!e.store.recorded(e.conn.ID, "ev-1") {
		t.Fatalf("the retry answered %d", w.Code)
	}

	e = newPushEnv(t)
	p := signed(t, "ev-1", e.now, body)
	p.publicID = brokenID
	if w := e.send(p); w.Code != http.StatusInternalServerError {
		t.Fatalf("a failed read answered %d", w.Code)
	}
	want := `"level":"WARN","event":"bridge_event_failed","connection":"CN0000000000E1","error":"the database went away"`
	if !strings.Contains(e.log.String(), want) {
		t.Errorf("logged %s", e.log.String())
	}
}

// TestPushWaitsForASlot covers bridge.parallel_events: a pushed event that finds no free slot waits for one and is
// then handled, never refused.
func TestPushWaitsForASlot(t *testing.T) {
	e := newPushEnv(t)
	e.events.Parallel = 1
	e.store.hold = make(chan struct{})
	parked := make(chan struct{}, 1)
	e.events.parked = func() { parked <- struct{}{} }
	codes := make(chan int, 2)
	for _, id := range []string{"ev-1", "ev-2"} {
		p := signed(t, id, e.now, eventJSON(t, eventFixture(TypeMessageDeleted, member("id", id))))
		go func() { codes <- e.send(p).Code }()
	}
	eventually(t, "one event at the claim", func() bool {
		e.store.mu.Lock()
		defer e.store.mu.Unlock()
		return e.store.running == 1
	})
	// The other push waits for the slot.
	<-parked
	select {
	case code := <-codes:
		t.Fatalf("a push was answered %d while the slot was taken", code)
	default:
	}
	close(e.store.hold)
	for range 2 {
		if code := <-codes; code != http.StatusNoContent {
			t.Errorf("answered %d", code)
		}
	}
	if e.store.most != 1 || !e.store.recorded(e.conn.ID, "ev-1") || !e.store.recorded(e.conn.ID, "ev-2") {
		t.Errorf("%d at a time", e.store.most)
	}

	// A push whose request ends while it waits is answered 500 and records nothing; no failure is logged for it.
	e = newPushEnv(t)
	e.events.Parallel = 1
	e.store.hold = make(chan struct{})
	// The late push's request ends once it waits for the slot.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.events.parked = cancel
	busy := make(chan int, 1)
	first := signed(t, "ev-1", e.now, eventJSON(t, eventFixture(TypeMessageDeleted)))
	go func() { busy <- e.send(first).Code }()
	eventually(t, "the slot taken", func() bool {
		e.store.mu.Lock()
		defer e.store.mu.Unlock()
		return e.store.running == 1
	})
	late := signed(t, "ev-2", e.now, eventJSON(t, eventFixture(TypeMessageDeleted, member("id", "ev-2"))))
	if w := e.sendContext(ctx, late); w.Code != http.StatusInternalServerError || e.store.recorded(e.conn.ID, "ev-2") {
		t.Errorf("a push that ended waiting answered %d", w.Code)
	}
	close(e.store.hold)
	if code := <-busy; code != http.StatusNoContent || strings.Contains(e.log.String(), "bridge_event_failed") {
		t.Errorf("answered %d, logged %s", code, e.log.String())
	}
}
