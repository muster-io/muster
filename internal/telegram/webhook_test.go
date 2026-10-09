// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
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

// TestRouterCapsParallelUpdates covers telegram.parallel_updates (P-52): one process routes at most ParallelUpdates
// updates at a time, over all its Connections; the next waits for a slot and runs once one is released, and an update
// whose context ends while it waits runs nothing.
func TestRouterCapsParallelUpdates(t *testing.T) {
	r := &Router{Offsets: &memOffsets{}, Log: logging.New(&syncBuffer{}, logging.LevelInfo)}
	entered, release := make(chan int64, 4), make(chan struct{})
	var mu sync.Mutex
	running, most := 0, 0
	r.Handle(KindCallbackQuery, func(_ context.Context, c Conn, _ Update) error {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		entered <- c.ID
		<-release
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	})
	u, _ := ParseUpdate([]byte(`{"update_id":1,"callback_query":{"id":"q","from":{"id":1}}}`))
	done := make(chan error, ParallelUpdates+1)
	for id := range int64(ParallelUpdates + 1) {
		go func() {
			_, err := r.Route(t.Context(), Conn{ID: id + 1, PublicID: "CNAAAAAAAAAAT" + strconv.FormatInt(id+1, 10)}, u)
			done <- err
		}()
	}
	for range ParallelUpdates {
		<-entered
	}
	select {
	case id := <-entered:
		t.Fatalf("the update of connection %d ran past the cap", id)
	case <-time.After(100 * time.Millisecond):
	}
	waiting, cancel := context.WithCancel(t.Context())
	cancel()
	if ran, err := r.Route(waiting, Conn{ID: 9, PublicID: "CNAAAAAAAAAAT9"}, u); ran || !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait = %v %v", ran, err)
	}
	close(release)
	<-entered
	for range ParallelUpdates + 1 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if most != ParallelUpdates {
		t.Fatalf("at most %d updates at a time, want %d", most, ParallelUpdates)
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

// TestWebhookGapAfterAnOutage covers C-14.FR-4 in the webhook mode (journal D293): a quiet period is no gap; an update
// that reaches a replica before any other of its Connection, within telegram.press_max_age of the replica's start,
// gets as its gap the outage before that start, up to the replica's start: a short outage leaves later presses alone. A press dropped for it receives nothing, so that the
// next kept press gets the gap too, until another update arrives; a failed read of the outage is a 500.
func TestWebhookGapAfterAnOutage(t *testing.T) {
	business := clock.NewManual(press0)
	g := &gaps{}
	r := &Router{Offsets: &memOffsets{}, Log: logging.New(&syncBuffer{}, logging.LevelInfo)}
	r.Handle(KindCallbackQuery, g.handler)
	r.Handle(KindPrivateMessage, g.handler)
	conn := Conn{ID: 1, PublicID: "CNAAAAAAAAAAT1"}
	cfg := WebhookConfig{Connections: hooks{conn: conn, secret: "s3cr3t_hook"}, Router: r, Clock: business,
		Outages: fixedOutages{o: Outage{AliveAt: press0, DowntimeStart: press0.Add(-3 * time.Hour),
			DowntimeEnd: press0.Add(-time.Second)}}, Log: logging.New(&syncBuffer{}, logging.LevelInfo)}
	h := NewWebhook(cfg)
	pressAt := func(h http.Handler, id int64) time.Duration {
		t.Helper()
		body := fmt.Sprintf(`{"update_id":%d,"callback_query":{"id":"q%d","from":{"id":5001},"data":"x"}}`, id, id)
		if rec := post(t, h, WebhookPath+conn.PublicID, "s3cr3t_hook", body); rec.Code != http.StatusOK {
			t.Fatalf("press %d = %d %s", id, rec.Code, rec.Body)
		}
		d, _ := g.of(id)
		return d
	}
	business.Advance(time.Minute)
	if d := pressAt(h, 1); d != 3*time.Hour {
		t.Errorf("the first kept press has the gap %v", d)
	}
	if d := pressAt(h, 2); d != 3*time.Hour {
		t.Errorf("the second kept press has the gap %v", d)
	}
	if rec := post(t, h, WebhookPath+conn.PublicID, "s3cr3t_hook", strings.ReplaceAll(privateUpdate, "9001",
		"3")); rec.Code != http.StatusOK {
		t.Fatalf("message = %d", rec.Code)
	}
	business.Advance(30 * time.Minute)
	if d := pressAt(h, 4); d != 0 {
		t.Errorf("a press after another update has the gap %v", d)
	}
	// A replica that started long after the outage, or one without an outage, sees no gap.
	late := NewWebhook(cfg)
	business.Advance(PressMaxAge + time.Second)
	if d := pressAt(late, 5); d != 0 {
		t.Errorf("a replica running for over an hour sees the gap %v", d)
	}
	short := cfg
	short.Outages = fixedOutages{o: Outage{AliveAt: business.Now(), DowntimeStart: business.Now().Add(-20 * time.Minute),
		DowntimeEnd: business.Now().Add(-time.Second)}}
	recovered := NewWebhook(short)
	business.Advance(41 * time.Minute)
	if d := pressAt(recovered, 9); d != 20*time.Minute {
		t.Errorf("a press 41 min after a 20-min outage has the gap %v", d)
	}
	cfg.Outages = fixedOutages{}
	if d := pressAt(NewWebhook(cfg), 6); d != 0 {
		t.Errorf("without an outage the gap is %v", d)
	}
	cfg.Outages = fixedOutages{err: errors.New("down")}
	if rec := post(t, NewWebhook(cfg), WebhookPath+conn.PublicID, "s3cr3t_hook",
		`{"update_id":7,"callback_query":{"id":"q7","from":{"id":5001}}}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("a failed outage read = %d", rec.Code)
	}
	cfg.Outages, cfg.Clock = nil, nil
	if d := pressAt(NewWebhook(cfg), 8); d != 0 {
		t.Errorf("without Outages the gap is %v", d)
	}
}
