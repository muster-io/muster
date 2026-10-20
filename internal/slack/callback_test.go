// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/requestlog"
)

// F-104: Slack's example of a signed request, a slash command, which Muster's verification reproduces.
const (
	vectorSecret    = "8f742231b10e8888abcd99yyyzzz85a5"
	vectorTimestamp = "1531420618"
	vectorBody      = "token=xyzz0WbapA4vBCDEFasx0q6G&team_id=T1DC2JH3J&team_domain=testteamnow&channel_id=G8PSS9T3V&" +
		"channel_name=foobar&user_id=U2CERLKJA&user_name=roadrunner&command=%2Fwebhook-collect&text=&response_url=" +
		"https%3A%2F%2Fhooks.slack.com%2Fcommands%2FT1DC2JH3J%2F397700885554%2F96rGlfmibIGlgcZRskXaIFfN&trigger_id=" +
		"398738663015.47445629121.803a0bc887a14d10d2c447fce8b6703c"
	vectorSignature = "v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503"
)

// The Connections of the callback tests: cbHTTP in http mode with its signing secret, cbVector in http mode with the
// secret of F-104, cbSocket in socket mode, cbNoSecret in http mode without a stored secret, cbBroken whose read fails;
// any other is unknown, deleted or not a Slack Connection.
const (
	cbHTTP     = "CN0000000000S1"
	cbVector   = "CN0000000000S2"
	cbSocket   = "CN0000000000S3"
	cbNoSecret = "CN0000000000S4"
	cbBroken   = "CN0000000000S5"
	cbSecret   = logging.Secret("callback-signing-s3cr3t-1")
)

var cbNow = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)

type cbConnections struct{}

func (cbConnections) SlackPressConnection(_ context.Context, publicID string) (Conn, logging.Secret, error) {
	switch publicID {
	case cbHTTP:
		return Conn{ID: 3, PublicID: cbHTTP, Mode: EventModeHTTP}, cbSecret, nil
	case cbVector:
		return Conn{ID: 4, PublicID: cbVector, Mode: EventModeHTTP}, vectorSecret, nil
	case cbSocket:
		return Conn{ID: 5, PublicID: cbSocket, Mode: EventModeSocket}, "", nil
	case cbNoSecret:
		return Conn{ID: 6, PublicID: cbNoSecret, Mode: EventModeHTTP}, "", nil
	case cbBroken:
		return Conn{}, "", errors.New("the database went away")
	}
	return Conn{}, "", ErrNoConnection
}

// handedPress is a press the callback handed over.
type handedPress struct {
	ctx     context.Context //nolint:containedctx // the context the press is handled in, which the test inspects
	conn    Conn
	payload []byte
}

// cbHandler records the presses handed to it; each blocks until release is closed, when set.
type cbHandler struct {
	mu      sync.Mutex
	got     []handedPress
	release chan struct{}
	entered chan struct{}
}

func (h *cbHandler) Handle(ctx context.Context, c Conn, payload []byte) {
	h.mu.Lock()
	h.got = append(h.got, handedPress{ctx: ctx, conn: c, payload: payload})
	release, entered := h.release, h.entered
	h.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}
}

func (h *cbHandler) handed() []handedPress {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]handedPress(nil), h.got...)
}

// lockedBuffer is a log that tests read while presses still write to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lines are the log lines of event.
func (b *lockedBuffer) lines(t *testing.T, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if m["event"] == event {
			out = append(out, m)
		}
	}
	return out
}

type baseKey struct{}

// cbEnv is the callback on a mux as the ingest listener mounts it, under the Request log's inbound recorder, with the
// real clock at cbNow and a process context that carries baseKey.
type cbEnv struct {
	handler http.Handler
	presses *cbHandler
	log     *lockedBuffer
	rec     *inboundRecords
	real    *clock.Manual
	cfg     CallbackConfig
}

// inboundRecords keep the inbound records of the Request log.
type inboundRecords struct {
	mu   sync.Mutex
	list []requestlog.InboundRecord
}

func (r *inboundRecords) RecordInbound(_ context.Context, rec requestlog.InboundRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, rec)
}

func (r *inboundRecords) got() []requestlog.InboundRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]requestlog.InboundRecord(nil), r.list...)
}

func newCBEnv(t *testing.T) *cbEnv {
	t.Helper()
	e := &cbEnv{presses: &cbHandler{}, log: &lockedBuffer{}, rec: &inboundRecords{}, real: clock.NewManual(cbNow)}
	e.cfg = CallbackConfig{Connections: cbConnections{}, Presses: e.presses, Real: e.real,
		Base: context.WithValue(t.Context(), baseKey{}, "process"), Log: logging.New(e.log, logging.LevelInfo)}
	e.mount()
	return e
}

func (e *cbEnv) mount() {
	mux := http.NewServeMux()
	mux.Handle(CallbackPattern, requestlog.Inbound(e.rec, e.real, requestlog.OperationInteractive, NewCallback(e.cfg)))
	e.handler = mux
}

// signedBody is the signature of body sent at ts under secret.
func signedBody(secret logging.Secret, ts string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("v0:" + ts + ":"))
	m.Write(body)
	return "v0=" + hex.EncodeToString(m.Sum(nil))
}

// pressForm is the form-encoded body of a press with payload.
func pressForm(payload string) []byte {
	return []byte(url.Values{"payload": {payload}}.Encode())
}

const cbPayload = `{"type":"block_actions","user":{"id":"U0BOB"},"container":{"type":"message_attachment",` +
	`"message_ts":"1.000001","channel_id":"C0ALERTS"},"actions":[{"action_id":"ack","value":"x"}]}`

// post sends body to the Connection publicID with the timestamp ts and the signature sig, each left out when empty.
func (e *cbEnv) post(t *testing.T, publicID, ts, sig string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, CallbackPath+publicID, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if ts != "" {
		r.Header.Set(HeaderTimestamp, ts)
	}
	if sig != "" {
		r.Header.Set(HeaderSignature, sig)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w
}

// signedPost sends body to cbHTTP signed at the real clock's now moved by offset.
func (e *cbEnv) signedPost(t *testing.T, offset time.Duration, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	ts := strconv.FormatInt(e.real.Now().Add(offset).Unix(), 10)
	return e.post(t, cbHTTP, ts, signedBody(cbSecret, ts, body), body)
}

// TestSignatureVector is F-104: Slack's example of a signed request verifies — in either case of hex — and a changed
// body, timestamp or secret does not. Through the callback, with the real clock at its timestamp, the example passes
// the signature and is answered 400 only because a slash command carries no payload.
func TestSignatureVector(t *testing.T) {
	body := []byte(vectorBody)
	if !verifySignature(vectorSecret, vectorTimestamp, body, vectorSignature) ||
		!verifySignature(vectorSecret, vectorTimestamp, body, "v0="+strings.ToUpper(vectorSignature[3:])) {
		t.Fatal("the example of F-104 does not verify")
	}
	for name, ok := range map[string]bool{
		"changed body":      verifySignature(vectorSecret, vectorTimestamp, append(body, '&'), vectorSignature),
		"changed timestamp": verifySignature(vectorSecret, "1531420619", body, vectorSignature),
		"other secret":      verifySignature("8f742231b10e8888abcd99yyyzzz85a6", vectorTimestamp, body, vectorSignature),
		"no version":        verifySignature(vectorSecret, vectorTimestamp, body, vectorSignature[3:]),
		"v1":                verifySignature(vectorSecret, vectorTimestamp, body, "v1="+vectorSignature[3:]),
		"short":             verifySignature(vectorSecret, vectorTimestamp, body, vectorSignature[:60]),
		"not hex":           verifySignature(vectorSecret, vectorTimestamp, body, vectorSignature[:66]+"zz"),
		"no secret":         verifySignature("", vectorTimestamp, body, vectorSignature),
	} {
		if ok {
			t.Errorf("%s verifies", name)
		}
	}
	e := newCBEnv(t)
	sec, _ := strconv.ParseInt(vectorTimestamp, 10, 64)
	e.real.Set(time.Unix(sec, 0).Add(30 * time.Second))
	w := e.post(t, cbVector, vectorTimestamp, vectorSignature, body)
	lines := e.log.lines(t, "slack_press_rejected")
	if w.Code != http.StatusBadRequest || len(lines) != 1 || lines[0]["reason"] != reasonNoPayload ||
		lines[0]["connection"] != cbVector {
		t.Fatalf("the example through the callback = %d %s, %v", w.Code, w.Body, lines)
	}
}

// TestCallbackAnswersAtOnce is C-24.FR-9 and AC-11: a signed press is answered 200 with an empty body at once, in well
// under 100 ms, while its handling takes longer; the press goes to Presses in the process context — not the
// request's, which has ended — with its Connection and its payload; the request is in the Request log of the
// Connection as interactive.
func TestCallbackAnswersAtOnce(t *testing.T) {
	e := newCBEnv(t)
	e.presses.release, e.presses.entered = make(chan struct{}), make(chan struct{}, 1)
	defer close(e.presses.release)
	start := time.Now()
	w := e.signedPost(t, 0, pressForm(cbPayload))
	elapsed := time.Since(start)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("answer %d %q %v", w.Code, w.Body, w.Header())
	}
	if elapsed >= 100*time.Millisecond {
		t.Errorf("the answer took %v", elapsed)
	}
	select {
	case <-e.presses.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the press was not handed over")
	}
	got := e.presses.handed()
	if len(got) != 1 || got[0].conn.PublicID != cbHTTP || got[0].conn.Mode != EventModeHTTP ||
		string(got[0].payload) != cbPayload || got[0].ctx.Value(baseKey{}) != "process" ||
		got[0].ctx.Err() != nil {
		t.Fatalf("handed %+v", got)
	}
	recs := e.rec.got()
	if len(recs) != 1 || recs[0].Operation != requestlog.OperationInteractive || recs[0].Status != http.StatusOK ||
		recs[0].Links.Connection != 3 || recs[0].Links.ConnectionID != cbHTTP || recs[0].Links.Destination != 0 ||
		recs[0].RequestHeader.Get(HeaderSignature) == "" {
		t.Errorf("records %+v", recs)
	}
	if lines := e.log.lines(t, "slack_press_rejected"); len(lines) != 0 {
		t.Errorf("rejected %v", lines)
	}
}

// TestCallbackWithoutBase: without a process context, a press is handled in the request's context without its end.
func TestCallbackWithoutBase(t *testing.T) {
	e := newCBEnv(t)
	e.cfg.Base = nil
	e.presses.entered = make(chan struct{}, 1)
	e.mount()
	ctx, cancel := context.WithCancel(t.Context())
	body := pressForm(cbPayload)
	ts := strconv.FormatInt(cbNow.Unix(), 10)
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, CallbackPath+cbHTTP, bytes.NewReader(body))
	r.Header.Set(HeaderTimestamp, ts)
	r.Header.Set(HeaderSignature, signedBody(cbSecret, ts, body))
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	cancel()
	<-e.presses.entered
	if got := e.presses.handed(); w.Code != http.StatusOK || len(got) != 1 || got[0].ctx.Err() != nil {
		t.Fatalf("answer %d, handed %+v", w.Code, got)
	}
}

// TestCallbackRefusals is C-24.FR-9 and AC-11: a missing or wrong signature, a changed body, a missing or malformed
// timestamp, one 6 minutes old or 6 minutes ahead, an unknown or deleted Connection, one in socket mode and one in
// http mode without a stored secret are each answered the same 401, hand nothing to Presses and log
// slack_press_rejected with the reason and the Connection when it is known; every request for a known Connection is
// in its Request log, an unknown one's is not. At the edge of the tolerance a press is still taken.
func TestCallbackRefusals(t *testing.T) {
	e := newCBEnv(t)
	body := pressForm(cbPayload)
	now := strconv.FormatInt(cbNow.Unix(), 10)
	sig := signedBody(cbSecret, now, body)
	at := func(d time.Duration) string { return strconv.FormatInt(cbNow.Add(d).Unix(), 10) }
	cases := []struct {
		name, publicID, ts, sig string
		body                    []byte
		reason, connection      string
	}{
		{"wrong signature", cbHTTP, now, "v0=" + strings.Repeat("ab", 32), body, reasonBadSignature, cbHTTP},
		{"not a signature", cbHTTP, now, "wrong", body, reasonBadSignature, cbHTTP},
		{"no signature", cbHTTP, now, "", body, reasonBadSignature, cbHTTP},
		{"changed body", cbHTTP, now, sig, append(bytes.Clone(body), '&'), reasonBadSignature, cbHTTP},
		{"other secret", cbHTTP, now, signedBody("other", now, body), body, reasonBadSignature, cbHTTP},
		{"no timestamp", cbHTTP, "", sig, body, reasonStale, cbHTTP},
		{"malformed timestamp", cbHTTP, "-" + now, sig, body, reasonStale, cbHTTP},
		{"6 minutes old", cbHTTP, at(-6 * time.Minute), signedBody(cbSecret, at(-6*time.Minute), body), body,
			reasonStale, cbHTTP},
		{"6 minutes ahead", cbHTTP, at(6 * time.Minute), signedBody(cbSecret, at(6*time.Minute), body), body,
			reasonStale, cbHTTP},
		{"unknown", "CN0000000000ZZ", now, sig, body, reasonUnknownConnection, ""},
		{"not a public_id", "nope", now, sig, body, reasonUnknownConnection, ""},
		{"socket mode", cbSocket, now, sig, body, reasonWrongMode, cbSocket},
		{"http without a secret", cbNoSecret, now, sig, body, reasonWrongMode, cbNoSecret},
	}
	var first []byte
	for i, c := range cases {
		w := e.post(t, c.publicID, c.ts, c.sig, c.body)
		if w.Code != http.StatusUnauthorized || w.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: %d %v", c.name, w.Code, w.Header())
		}
		if first == nil {
			first = w.Body.Bytes()
		} else if !bytes.Equal(w.Body.Bytes(), first) {
			t.Errorf("%s: body %s, want %s", c.name, w.Body, first)
		}
		lines := e.log.lines(t, "slack_press_rejected")
		if len(lines) != i+1 || lines[i]["reason"] != c.reason || lines[i]["connection"] != c.connection ||
			lines[i]["level"] != "WARN" {
			t.Errorf("%s: lines %v", c.name, lines)
		}
	}
	if !strings.Contains(string(first), `"status":401`) || !strings.Contains(string(first), "unauthenticated") {
		t.Errorf("the 401 %s", first)
	}
	if got := e.presses.handed(); len(got) != 0 {
		t.Fatalf("handed %+v", got)
	}
	if n := len(e.rec.got()); n != len(cases)-2 {
		t.Errorf("%d records, want %d", n, len(cases)-2)
	}
	if strings.Contains(e.log.String(), string(cbSecret)) || strings.Contains(e.log.String(), sig[3:]) {
		t.Errorf("the log shows the secret or a signature: %s", e.log)
	}
	for _, d := range []time.Duration{-RequestTolerance, RequestTolerance} {
		if w := e.signedPost(t, d, body); w.Code != http.StatusOK {
			t.Errorf("at %v: %d", d, w.Code)
		}
	}
}

// TestCallbackBadBodies is C-24.FR-9: a signed body without a payload that is a JSON object is answered 400 and logs
// no_payload; a body over 1 MiB is answered 400 and logs too_large, before anything else, whatever the Connection;
// both 400s are alike and nothing is handed over.
func TestCallbackBadBodies(t *testing.T) {
	e := newCBEnv(t)
	var bodies [][]byte
	for _, body := range [][]byte{[]byte("foo=bar"), pressForm(""), pressForm("[1]"), pressForm("not json"),
		pressForm(`"text"`), []byte("payload=%zz")} {
		w := e.signedPost(t, 0, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: %d", body, w.Code)
		}
		bodies = append(bodies, w.Body.Bytes())
	}
	large := pressForm(cbPayload + strings.Repeat(" ", MaxRequestBytes))
	for _, publicID := range []string{cbHTTP, "CN0000000000ZZ"} {
		w := e.post(t, publicID, "", "", large)
		if w.Code != http.StatusBadRequest {
			t.Errorf("too large for %s: %d", publicID, w.Code)
		}
		bodies = append(bodies, w.Body.Bytes())
	}
	for _, b := range bodies {
		if !bytes.Equal(b, bodies[0]) || !strings.Contains(string(b), "validation-failed") {
			t.Errorf("400 %s, want %s", b, bodies[0])
		}
	}
	lines := e.log.lines(t, "slack_press_rejected")
	var reasons []string
	for _, l := range lines {
		reasons = append(reasons, l["reason"].(string)+"/"+l["connection"].(string))
	}
	want := "no_payload/" + cbHTTP
	if got := strings.Join(reasons, ","); got != strings.Repeat(want+",", 6)+"too_large/"+cbHTTP+",too_large/" {
		t.Errorf("reasons %s", got)
	}
	if got := e.presses.handed(); len(got) != 0 {
		t.Fatalf("handed %+v", got)
	}
}

// failingBody is a body whose read fails.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("the connection broke") }

// TestCallbackFailures: a body that cannot be read is answered 400; a Connection that cannot be read is answered 500 and
// logs slack_press as failed with the error.
func TestCallbackFailures(t *testing.T) {
	e := newCBEnv(t)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, CallbackPath+cbHTTP, failingBody{})
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unreadable body: %d", w.Code)
	}
	w = e.post(t, cbBroken, "", "", pressForm(cbPayload))
	lines := e.log.lines(t, "slack_press")
	if w.Code != http.StatusInternalServerError || len(lines) != 1 || lines[0]["outcome"] != outcomeFailed ||
		lines[0]["error"] != "the database went away" {
		t.Fatalf("a failed read = %d %s, %v", w.Code, w.Body, lines)
	}
	if got := e.presses.handed(); len(got) != 0 {
		t.Fatalf("handed %+v", got)
	}
}

// TestCallbackOnlyPost: the callback takes POST only.
func TestCallbackOnlyPost(t *testing.T) {
	e := newCBEnv(t)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, CallbackPath+cbHTTP, nil)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", w.Code)
	}
}
