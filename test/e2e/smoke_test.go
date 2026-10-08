// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

type answer struct {
	status int
	header http.Header
	body   []byte
}

var client = &http.Client{Timeout: 10 * time.Second}

// call sends a request with the given header names and values.
func call(t *testing.T, method, url, body string, header ...string) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return answer{status: resp.StatusCode, header: resp.Header, body: b}
}

// botToken is any token the fake Mattermost takes as its bot's.
var botToken = []string{"Authorization", "Bearer dev-bot-token"}

func decode(t *testing.T, a answer, v any) {
	t.Helper()
	if err := json.Unmarshal(a.body, v); err != nil {
		t.Fatalf("decode %s: %v", a.body, err)
	}
}

func recordedPaths(t *testing.T, fakeURL string) []string {
	t.Helper()
	a := call(t, http.MethodGet, fakeURL+"/_fake/requests", "")
	var reqs []struct {
		Path string `json:"path"`
	}
	decode(t, a, &reqs)
	paths := make([]string, len(reqs))
	for i, r := range reqs {
		paths[i] = r.Path
	}
	return paths
}

func TestDevModeFakes(t *testing.T) {
	h := Start(t, DevProcess)

	t.Run("Telegram", func(t *testing.T) {
		var me struct {
			OK     bool `json:"ok"`
			Result struct {
				ID       int64  `json:"id"`
				IsBot    bool   `json:"is_bot"`
				Username string `json:"username"`
			} `json:"result"`
		}
		decode(t, call(t, http.MethodGet, h.Fakes.Telegram+"/bot123:abc/getMe", ""), &me)
		if !me.OK || !me.Result.IsBot || me.Result.Username != "muster_dev_bot" || me.Result.ID != 123456 {
			t.Errorf("getMe = %+v", me)
		}
		if paths := recordedPaths(t, h.Fakes.Telegram); len(paths) == 0 || paths[0] != "/bot123:abc/getMe" {
			t.Errorf("recorded paths %v, want /bot123:abc/getMe first", paths)
		}
		a := call(t, http.MethodPost, h.Fakes.Telegram+"/bot123:abc/sendMessage", `{"chat_id":1,"text":"hi"}`)
		var e struct {
			OK        bool `json:"ok"`
			ErrorCode int  `json:"error_code"`
		}
		decode(t, a, &e)
		if a.status != http.StatusNotImplemented || e.OK || e.ErrorCode != http.StatusNotImplemented {
			t.Errorf("sendMessage = %d %s, want 501 with ok false and error_code 501", a.status, a.body)
		}
	})

	t.Run("Mattermost", func(t *testing.T) {
		var me struct {
			IsBot bool `json:"is_bot"`
		}
		a := call(t, http.MethodGet, h.Fakes.Mattermost+"/api/v4/users/me", "", botToken...)
		decode(t, a, &me)
		if a.status != http.StatusOK || !me.IsBot {
			t.Errorf("users/me = %d %s", a.status, a.body)
		}
		if a := call(t, http.MethodGet, h.Fakes.Mattermost+"/api/v4/users/me", ""); a.status != http.StatusUnauthorized {
			t.Errorf("users/me without a token = %d, want 401", a.status)
		}
		if a := call(t, http.MethodPost, h.Fakes.Mattermost+"/hooks/smoke", `{"text":"x"}`); a.status !=
			http.StatusNotImplemented {
			t.Errorf("POST /hooks/smoke = %d, want 501", a.status)
		}
	})

	t.Run("faults", func(t *testing.T) {
		faults := h.Fakes.Mattermost + "/_fake/faults"
		fault := `{"path":"/api/v4/users/me","status":429,"retry_after_seconds":3}`
		if a := call(t, http.MethodPost, faults, fault); a.status != http.StatusNoContent {
			t.Fatalf("POST /_fake/faults = %d %s", a.status, a.body)
		}
		a := call(t, http.MethodGet, h.Fakes.Mattermost+"/api/v4/users/me", "")
		if a.status != http.StatusTooManyRequests || a.header.Get("Retry-After") != "3" {
			t.Errorf("users/me under the fault = %d, Retry-After %q; want 429 and 3", a.status, a.header.Get("Retry-After"))
		}

		delay := `{"path":"/api/v4/users/me","delay_ms":200}`
		if a := call(t, http.MethodPost, faults, delay); a.status != http.StatusNoContent {
			t.Fatalf("POST /_fake/faults %s = %d %s", delay, a.status, a.body)
		}
		begin := time.Now()
		a = call(t, http.MethodGet, h.Fakes.Mattermost+"/api/v4/users/me", "", botToken...)
		if elapsed := time.Since(begin); elapsed < 200*time.Millisecond || a.status != http.StatusOK {
			t.Errorf("users/me with a delay = %d after %v, want 200 after at least 200ms", a.status, elapsed)
		}

		if a := call(t, http.MethodDelete, faults, ""); a.status != http.StatusNoContent {
			t.Errorf("DELETE /_fake/faults = %d", a.status)
		}
		if a := call(t, http.MethodGet, h.Fakes.Mattermost+"/api/v4/users/me", "", botToken...); a.status !=
			http.StatusOK {
			t.Errorf("users/me after the reset = %d, want 200", a.status)
		}
	})

	t.Run("Alertmanager", func(t *testing.T) {
		type webhookRequest struct {
			method, path, authorization string
			body                        []byte
		}
		var (
			mu       sync.Mutex
			received []webhookRequest
		)
		ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			received = append(received, webhookRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), b})
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		}))
		defer ingest.Close()

		send := `{"url":"` + ingest.URL + `/api/v1/ingest","token":"t0ken"}`
		a := call(t, http.MethodPost, h.Fakes.Alertmanager+"/_fake/send", send)
		if a.status != http.StatusOK || strings.TrimSpace(string(a.body)) != `{"status":202}` {
			t.Fatalf("POST /_fake/send = %d %s, want 200 {\"status\":202}", a.status, a.body)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(received) != 1 {
			t.Fatalf("the receiver got %d webhooks, want 1", len(received))
		}
		r := received[0]
		if r.method != http.MethodPost || r.path != "/api/v1/ingest" || r.authorization != "Bearer t0ken" {
			t.Errorf("webhook request %s %s, Authorization %q", r.method, r.path, r.authorization)
		}
		var w struct {
			Version  string `json:"version"`
			GroupKey string `json:"groupKey"`
			Status   string `json:"status"`
			Alerts   []struct {
				Labels map[string]string `json:"labels"`
			} `json:"alerts"`
		}
		if err := json.Unmarshal(r.body, &w); err != nil {
			t.Fatal(err)
		}
		if w.Version != "4" || w.GroupKey == "" || w.Status != "firing" || len(w.Alerts) == 0 ||
			len(w.Alerts[0].Labels) == 0 {
			t.Errorf("webhook %s", r.body)
		}
	})

	t.Run("replicas", func(t *testing.T) {
		want := replicaCount(t)
		if len(h.Replicas) != want {
			t.Fatalf("%d replicas, want %d", len(h.Replicas), want)
		}
		for _, r := range h.Replicas {
			if !r.Running() {
				t.Errorf("muster %v is not running; output:\n%s", r.args, r.Output())
			}
		}
		if want == 2 {
			r := h.Replicas[1]
			if !strings.Contains(r.Output(), devmode.ReplicaLine) || r.App != "http://127.0.0.1:9080" {
				t.Errorf("second replica at %s printed %q", r.App, r.Output())
			}
		}
	})
}

func TestFakesSurviveReplicaRestart(t *testing.T) {
	h := Start(t, FakesInProcess)
	r := h.StartReplica(t, ReplicaOptions{Env: map[string]string{"MUSTER_SECRET_KEYS": devmode.DevelopmentKey}})
	if r.App != "http://127.0.0.1:9080" || r.Ingest != "http://127.0.0.1:9081" || r.Internal != "http://127.0.0.1:9082" {
		t.Errorf("replica addresses %s, %s, %s", r.App, r.Ingest, r.Internal)
	}

	call(t, http.MethodGet, h.Fakes.Telegram+"/bot123:abc/getMe", "")
	fault := `{"path":"/api/v4/users/me","status":503,"retry_after_seconds":2}`
	if a := call(t, http.MethodPost, h.Fakes.Mattermost+"/_fake/faults", fault); a.status != http.StatusNoContent {
		t.Fatalf("POST /_fake/faults = %d %s", a.status, a.body)
	}

	pid := r.PID()
	r.Restart(t)
	if r.PID() == pid || !r.Running() {
		t.Errorf("after Restart: pid %d (was %d), running %v", r.PID(), pid, r.Running())
	}
	if !strings.Contains(r.Output(), devmode.ReplicaLine) {
		t.Errorf("the restarted replica printed %q", r.Output())
	}

	if paths := recordedPaths(t, h.Fakes.Telegram); len(paths) != 1 || paths[0] != "/bot123:abc/getMe" {
		t.Errorf("recorded paths after the restart %v, want [/bot123:abc/getMe]", paths)
	}
	if reqs := h.InProcess.Telegram.Requests(); len(reqs) != 1 {
		t.Errorf("Requests() after the restart has %d entries, want 1", len(reqs))
	}
	a := call(t, http.MethodGet, h.Fakes.Mattermost+"/api/v4/users/me", "")
	if a.status != http.StatusServiceUnavailable || a.header.Get("Retry-After") != "2" {
		t.Errorf("users/me after the restart = %d, Retry-After %q; want the fault's 503 and 2", a.status,
			a.header.Get("Retry-After"))
	}

	r.Stop(t)
	if r.Running() {
		t.Error("the replica runs after Stop")
	}
	r.Start(t)
	if !r.Running() {
		t.Error("the replica does not run after Start")
	}
}
