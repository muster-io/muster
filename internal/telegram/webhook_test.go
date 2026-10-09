// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/logging"
)

// hooks serve one Connection in the webhook mode with its secret token, or fail.
type hooks struct {
	conn   Conn
	secret logging.Secret
	err    error
}

func (h hooks) Hooked(_ context.Context, publicID string) (Conn, logging.Secret, error) {
	if h.err != nil {
		return Conn{}, "", h.err
	}
	if publicID != h.conn.PublicID {
		return Conn{}, "", ErrNoWebhook
	}
	return h.conn, h.secret, nil
}

func post(t *testing.T, h http.Handler, path, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	if secret != "" {
		req.Header.Set(SecretTokenHeader, secret)
	}
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.Handle(WebhookPattern, h)
	mux.ServeHTTP(rec, req)
	return rec
}

const privateUpdate = `{"update_id":9001,"message":{"message_id":1,"chat":{"id":42,"type":"private"},"text":"hello"}}`

// TestWebhookSecretToken covers C-14.FR-1 and AC-11: without the secret token header, with a wrong one, or for an
// unknown Connection the endpoint answers 401 and changes nothing; with the right header the update reaches the router.
func TestWebhookSecretToken(t *testing.T) {
	log := &syncBuffer{}
	offsets := &memOffsets{}
	seen := &handled{}
	r := &Router{Offsets: offsets, Log: logging.New(log, logging.LevelInfo)}
	r.Handle(KindChatMessage, seen.handler)
	conn := Conn{ID: 1, PublicID: "CNAAAAAAAAAAT1"}
	h := NewWebhook(WebhookConfig{Connections: hooks{conn: conn, secret: "s3cr3t_hook"}, Router: r,
		Log: logging.New(log, logging.LevelInfo)})
	for _, c := range []struct{ path, secret string }{
		{WebhookPath + conn.PublicID, ""},
		{WebhookPath + conn.PublicID, "wrong"},
		{WebhookPath + conn.PublicID, "s3cr3t_hook "},
		{WebhookPath + "CN000000000000", "s3cr3t_hook"},
	} {
		rec := post(t, h, c.path, c.secret, privateUpdate)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "unauthenticated") {
			t.Fatalf("%s with %q = %d %s", c.path, c.secret, rec.Code, rec.Body)
		}
	}
	if o, _ := offsets.Offset(t.Context(), 1); o != nil || log.String() != "" {
		t.Fatalf("a refused request changed the offset %v or logged %s", o, log)
	}
	rec := post(t, h, WebhookPath+conn.PublicID, "s3cr3t_hook", privateUpdate)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("right header = %d %s", rec.Code, rec.Body)
	}
	if lines := log.lines("telegram_update_dropped"); len(lines) != 1 || lines[0]["kind"] != KindPrivateMessage ||
		lines[0]["connection"] != conn.PublicID {
		t.Fatalf("dropped lines %v", lines)
	}
	if rec := post(t, h, WebhookPath+conn.PublicID, "s3cr3t_hook", privateUpdate); rec.Code != http.StatusOK ||
		len(log.lines("telegram_update_dropped")) != 1 {
		t.Fatalf("the same update again = %d, dropped %d times", rec.Code, len(log.lines("telegram_update_dropped")))
	}
	channel := `{"update_id":9002,"channel_post":{"message_id":2,"chat":{"id":-100,"type":"channel"}}}`
	if rec := post(t, h, WebhookPath+conn.PublicID, "s3cr3t_hook", channel); rec.Code != http.StatusOK ||
		len(seen.get()) != 1 || seen.get()[0] != 9002 {
		t.Fatalf("channel post = %d, handled %v", rec.Code, seen.get())
	}
	noSecret := NewWebhook(WebhookConfig{Connections: hooks{conn: conn}, Router: r, Log: logging.New(log,
		logging.LevelInfo)})
	if rec := post(t, noSecret, WebhookPath+conn.PublicID, "", privateUpdate); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a Connection without a secret token = %d", rec.Code)
	}
}

func TestWebhookBadBodiesAndFailures(t *testing.T) {
	log := &syncBuffer{}
	offsets := &memOffsets{}
	r := &Router{Offsets: offsets, Log: logging.New(log, logging.LevelInfo)}
	conn := Conn{ID: 1, PublicID: "CNAAAAAAAAAAT1"}
	h := NewWebhook(WebhookConfig{Connections: hooks{conn: conn, secret: "s"}, Router: r, BodyLimit: 64,
		Log: logging.New(log, logging.LevelInfo)})
	for _, body := range []string{`[`, `{"message":{}}`, `{"update_id":1,"pad":"` + strings.Repeat("x", 80) + `"}`} {
		if rec := post(t, h, WebhookPath+conn.PublicID, "s", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%.20s = %d", body, rec.Code)
		}
	}
	offsets.fail = errors.New("database down")
	if rec := post(t, h, WebhookPath+conn.PublicID, "s", `{"update_id":1}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("router failure = %d", rec.Code)
	}
	broken := NewWebhook(WebhookConfig{Connections: hooks{err: errors.New("lookup failed")}, Router: r,
		Log: logging.New(log, logging.LevelInfo)})
	if rec := post(t, broken, WebhookPath+conn.PublicID, "s", `{"update_id":1}`); rec.Code !=
		http.StatusInternalServerError {
		t.Fatalf("lookup failure = %d", rec.Code)
	}
	lines := log.lines("telegram_update_failed")
	if len(lines) != 2 || !strings.Contains(lines[0]["error"].(string), "database down") ||
		lines[1]["error"] != "lookup failed" || lines[1]["level"] != "WARN" {
		t.Fatalf("failure lines %v", lines)
	}
	// Without a path value, as when the handler is mounted alone, the public_id is the rest of the path.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, WebhookPath+"CN000000000000",
		strings.NewReader(`{"update_id":1}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unmounted = %d", rec.Code)
	}
}

func TestNewSecret(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if len(a) != 43 || a == b || strings.Trim(string(a), "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
		t.Fatalf("secrets %q %q", a, b)
	}
}

// TestRouterHandlers: a registered handler takes its kind, its error stores no offset, and an update before the offset
// is skipped.
func TestRouterHandlers(t *testing.T) {
	log := &syncBuffer{}
	offsets := &memOffsets{}
	r := &Router{Offsets: offsets, Log: logging.New(log, logging.LevelInfo)}
	failing := errors.New("handler failed")
	calls := 0
	r.Handle(KindCallbackQuery, func(context.Context, Conn, Update) error {
		calls++
		if calls == 1 {
			return failing
		}
		return nil
	})
	u, _ := ParseUpdate([]byte(`{"update_id":5,"callback_query":{"id":"q","from":{"id":1}}}`))
	c := Conn{ID: 3, PublicID: "CNAAAAAAAAAAT3"}
	if ran, err := r.Route(t.Context(), c, u); ran || !errors.Is(err, failing) {
		t.Fatalf("failing handler = %v %v", ran, err)
	}
	if o, _ := offsets.Offset(t.Context(), 3); o != nil {
		t.Fatalf("offset after a failure %v", *o)
	}
	if ran, err := r.Route(t.Context(), c, u); !ran || err != nil {
		t.Fatalf("second try = %v %v", ran, err)
	}
	if ran, err := r.Route(t.Context(), c, u); ran || err != nil || calls != 2 {
		t.Fatalf("a handled update = %v %v, %d calls", ran, err, calls)
	}
	if log.String() != "" {
		t.Fatalf("logged %s", log)
	}
}
