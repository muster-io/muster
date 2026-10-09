// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package faketelegram_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

const token = "777001:test-token"

func start(t *testing.T) (*faketelegram.Fake, string) {
	t.Helper()
	f := faketelegram.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(t.Context()) })
	return f, f.URL()
}

type answer struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func call(t *testing.T, method, url, body string) (int, string, answer) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var a answer
	_ = json.Unmarshal(b, &a)
	return resp.StatusCode, string(b), a
}

func TestDryProbeIsAlways401(t *testing.T) {
	_, base := start(t)
	status, body, a := call(t, http.MethodGet, base+"/bot0:x/getMe", "")
	if status != http.StatusUnauthorized || a.OK || a.ErrorCode != 401 || a.Description != "Unauthorized" {
		t.Fatalf("dry probe = %d %s", status, body)
	}
	status, _, a = call(t, http.MethodGet, base+"/bot"+token+"/getMe", "")
	var me faketelegram.User
	_ = json.Unmarshal(a.Result, &me)
	if status != http.StatusOK || !a.OK || me.Username != faketelegram.BotUsername || me.ID != faketelegram.BotID {
		t.Fatalf("getMe = %d %+v", status, a)
	}
}

func TestRevokedTokens(t *testing.T) {
	_, base := start(t)
	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"revoked_tokens":["`+token+`"]}`); s != http.StatusOK {
		t.Fatalf("config = %d %s", s, b)
	}
	if s, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getMe", ""); s != http.StatusUnauthorized || a.OK {
		t.Fatalf("revoked getMe = %d", s)
	}
	if s, _, _ := call(t, http.MethodGet, base+"/botother:token/getMe", ""); s != http.StatusOK {
		t.Fatalf("other token = %d", s)
	}
}

func TestPathPrefixAndHTMLMode(t *testing.T) {
	f, base := start(t)
	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"path_prefix":"/k3x9/"}`); s != http.StatusOK ||
		f.Config().PathPrefix != "/k3x9" {
		t.Fatalf("config = %d %s", s, b)
	}
	if s, _, _ := call(t, http.MethodGet, base+"/k3x9/bot"+token+"/getMe", ""); s != http.StatusOK {
		t.Fatalf("under the prefix = %d", s)
	}
	for _, p := range []string{"/bot" + token + "/getMe", "/other/bot0:x/getMe", "/k3x9/getMe"} {
		if s, _, a := call(t, http.MethodGet, base+p, ""); s != http.StatusNotFound || a.ErrorCode != 404 {
			t.Fatalf("%s = %d", p, s)
		}
	}
	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"path_prefix":"","mode":"html"}`); s != http.StatusOK {
		t.Fatalf("config = %d %s", s, b)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/bot0:x/getMe", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
		string(b) != faketelegram.HTMLPage {
		t.Fatalf("html mode = %d %q", resp.StatusCode, b)
	}
	for _, bad := range []string{`{"mode":"xml"}`, `{"path_prefix":"k3x9"}`, `{"unknown":1}`} {
		if s, _, _ := call(t, http.MethodPut, base+"/_fake/config", bad); s != http.StatusBadRequest {
			t.Fatalf("config %s = %d", bad, s)
		}
	}
}

func TestGetUpdatesWithOffsetAndAllowedUpdates(t *testing.T) {
	f, base := start(t)
	for _, u := range []string{`{"message":{"message_id":1,"chat":{"id":1,"type":"private"}}}`,
		`{"chat_member":{"date":1}}`, `{"update_id":40,"callback_query":{"id":"q"}}`} {
		if s, b, _ := call(t, http.MethodPost, base+"/_fake/updates", `{"token":"`+token+`","update":`+u+`}`); s !=
			http.StatusOK {
			t.Fatalf("enqueue = %d %s", s, b)
		}
	}
	_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates",
		`{"timeout":0,"allowed_updates":["message","callback_query"]}`)
	var got []map[string]any
	_ = json.Unmarshal(a.Result, &got)
	if !a.OK || len(got) != 2 || got[0]["update_id"] != float64(1) || got[1]["update_id"] != float64(40) {
		t.Fatalf("getUpdates = %+v", got)
	}
	if f.Pending(token) != 2 {
		t.Fatalf("pending = %d, want 2 unconfirmed", f.Pending(token))
	}
	_, _, a = call(t, http.MethodGet, base+"/bot"+token+"/getUpdates?offset=41&timeout=0", "")
	if string(a.Result) != "[]" || f.Pending(token) != 0 {
		t.Fatalf("after the offset = %s, pending %d", a.Result, f.Pending(token))
	}
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"offset":"x"}`); s != 400 {
		t.Fatalf("bad offset = %d", s)
	}
}

func TestLongPollWaitsForAnUpdate(t *testing.T) {
	_, base := start(t)
	done := make(chan answer, 1)
	go func() {
		_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":5}`)
		done <- a
	}()
	waitPolling(t, base)
	call(t, http.MethodPost, base+"/_fake/updates", `{"token":"`+token+`","update":{"message":{"text":"hi"}}}`)
	select {
	case a := <-done:
		if !a.OK || !strings.Contains(string(a.Result), `"hi"`) {
			t.Fatalf("long poll = %+v", a)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the long poll did not end with the update")
	}
}

// waitPolling waits until a getUpdates is recorded and has not been answered yet.
func waitPolling(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, body, _ := call(t, http.MethodGet, base+"/_fake/requests", "")
		var reqs []struct {
			Path   string `json:"path"`
			Status int    `json:"status"`
		}
		_ = json.Unmarshal([]byte(body), &reqs)
		for _, r := range reqs {
			if strings.HasSuffix(r.Path, "/getUpdates") && r.Status == 0 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no long poll is running")
}

func TestSecondPollerEndsTheFirstWith409(t *testing.T) {
	_, base := start(t)
	first := make(chan answer, 1)
	go func() {
		_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":10}`)
		first <- a
	}()
	waitPolling(t, base)
	second := make(chan answer, 1)
	go func() {
		_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":1}`)
		second <- a
	}()
	a := <-first
	if a.OK || a.ErrorCode != http.StatusConflict ||
		!strings.HasPrefix(a.Description, "Conflict: terminated by other getUpdates request") {
		t.Fatalf("first poller = %+v", a)
	}
	if a := <-second; !a.OK {
		t.Fatalf("second poller = %+v", a)
	}
}

func TestConflictEndpoint(t *testing.T) {
	_, base := start(t)
	// Without a running poll, the next getUpdates gets the 409.
	_, body, _ := call(t, http.MethodPost, base+"/_fake/conflict", `{"token":"`+token+`"}`)
	if !strings.Contains(body, `"ended_running_poll":false`) {
		t.Fatalf("conflict = %s", body)
	}
	if s, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":0}`); s != 409 ||
		a.ErrorCode != 409 {
		t.Fatalf("owed conflict = %d", s)
	}
	running := make(chan int, 1)
	go func() {
		s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":10}`)
		running <- s
	}()
	waitPolling(t, base)
	_, body, _ = call(t, http.MethodPost, base+"/_fake/conflict", `{"token":"`+token+`"}`)
	if s := <-running; s != http.StatusConflict || !strings.Contains(body, `"ended_running_poll":true`) {
		t.Fatalf("running poll = %d, %s", s, body)
	}
	if s, _, _ := call(t, http.MethodPost, base+"/_fake/conflict", `{}`); s != http.StatusBadRequest {
		t.Fatalf("conflict without token = %d", s)
	}
}

func TestWebhookDelivery(t *testing.T) {
	f, base := start(t)
	var mu sync.Mutex
	var got []string
	var headers []string
	accept := true
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		headers = append(headers, r.Header.Get(faketelegram.SecretTokenHeader))
		if !accept {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got = append(got, string(b))
	}))
	defer hook.Close()
	running := make(chan answer, 1)
	go func() {
		_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":10}`)
		running <- a
	}()
	waitPolling(t, base)
	if s, b, _ := call(t, http.MethodPost, base+"/bot"+token+"/setWebhook", `{"url":"`+hook.URL+
		`/hook","secret_token":"s3cr3t_-x","allowed_updates":["message"],"max_connections":1}`); s != 200 {
		t.Fatalf("setWebhook = %d %s", s, b)
	}
	if a := <-running; a.ErrorCode != 409 || a.Description != "Conflict: terminated by setWebhook request" {
		t.Fatalf("running poll after setWebhook = %+v", a)
	}
	if s, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":0}`); s != 409 ||
		!strings.Contains(a.Description, "webhook is active") {
		t.Fatalf("getUpdates with a webhook = %d %+v", s, a)
	}
	_, _, a := call(t, http.MethodGet, base+"/bot"+token+"/getWebhookInfo", "")
	var info faketelegram.WebhookInfo
	_ = json.Unmarshal(a.Result, &info)
	if info.URL != hook.URL+"/hook" || info.PendingUpdateCount != 0 || info.MaxConnections != 1 {
		t.Fatalf("getWebhookInfo = %+v", info)
	}
	call(t, http.MethodPost, base+"/_fake/updates", `{"token":"`+token+`","update":{"update_id":9001,"message":{}}}`)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	mu.Lock()
	if headers[0] != "s3cr3t_-x" || !strings.Contains(got[0], `"update_id":9001`) {
		t.Fatalf("posted %v with %v", got, headers)
	}
	accept = false
	mu.Unlock()
	call(t, http.MethodPost, base+"/_fake/updates", `{"token":"`+token+`","update":{"message":{}}}`)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(headers) == 2 })
	waitFor(t, func() bool { return f.Pending(token) == 1 })
	if wh := f.Webhooks()[token]; wh.SecretToken != "s3cr3t_-x" {
		t.Fatalf("webhooks = %+v", f.Webhooks())
	}
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/setWebhook", `{"url":"x","secret_token":"a b"}`); s !=
		400 {
		t.Fatalf("bad secret = %d", s)
	}
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/deleteWebhook", ""); s != 200 ||
		len(f.Webhooks()) != 0 {
		t.Fatalf("deleteWebhook = %d", s)
	}
	if s, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":0}`); s != 200 ||
		!strings.Contains(string(a.Result), "update_id") {
		t.Fatalf("getUpdates after deleteWebhook = %d %s", s, a.Result)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("the condition did not hold in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnknownMethodsAndPaths(t *testing.T) {
	_, base := start(t)
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/sendMessage", `{}`); s != http.StatusNotImplemented {
		t.Fatalf("sendMessage = %d", s)
	}
	if s, _, _ := call(t, http.MethodDelete, base+"/bot"+token+"/getMe", ""); s != http.StatusNotFound {
		t.Fatalf("DELETE = %d", s)
	}
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/getMe", `[`); s != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d", s)
	}
	if s, _, _ := call(t, http.MethodPost, base+"/_fake/updates", `{"token":"","update":{}}`); s !=
		http.StatusBadRequest {
		t.Fatalf("update without token = %d", s)
	}
}
