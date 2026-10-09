// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakewebhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/fakes/fakewebhook"
)

// The example of the Standard Webhooks specification.
const (
	exampleSecret    = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	exampleID        = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	exampleTimestamp = "1614265330"
	exampleBody      = `{"test": 2432232314}`
	exampleSignature = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
)

var otherSecret = "whsec_" + base64.StdEncoding.EncodeToString([]byte("another key of thirty-two bytes!"))

func start(t *testing.T) string {
	t.Helper()
	f := fakewebhook.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(t.Context()) })
	return f.URL()
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func call(t *testing.T, method, url, body string, header ...string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Add(header[i], header[i+1])
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func received(t *testing.T, base, name string) []fakewebhook.Received {
	t.Helper()
	status, _, body := call(t, http.MethodGet, base+"/_fake/received/"+name, "")
	if status != http.StatusOK {
		t.Fatalf("received = %d %s", status, body)
	}
	if !strings.HasPrefix(body, "[") {
		t.Fatalf("received = %s, want a JSON array", body)
	}
	var out []fakewebhook.Received
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func setSecrets(t *testing.T, base, name string, secrets ...string) {
	t.Helper()
	b, _ := json.Marshal(append([]string{}, secrets...))
	if status, _, body := call(t, http.MethodPut, base+"/_fake/secrets/"+name, string(b)); status != http.StatusNoContent {
		t.Fatalf("secrets = %d %s", status, body)
	}
}

func sign(t *testing.T, secret, id, timestamp, body string) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "." + body))
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func post(t *testing.T, base, path, body, signature string) {
	t.Helper()
	status, header, answer := call(t, http.MethodPost, base+path, body, "Content-Type", "application/json",
		"webhook-id", exampleID, "webhook-timestamp", exampleTimestamp, "webhook-signature", signature)
	if status != http.StatusOK || answer != "{}" || header.Get("Content-Type") != "application/json" {
		t.Fatalf("POST %s = %d %q %q", path, status, header.Get("Content-Type"), answer)
	}
}

func TestSignatures(t *testing.T) {
	if got := sign(t, exampleSecret, exampleID, exampleTimestamp, exampleBody); got != exampleSignature {
		t.Fatalf("the test signs %s, want the published %s", got, exampleSignature)
	}
	both := exampleSignature + " " + sign(t, otherSecret, exampleID, exampleTimestamp, exampleBody)
	tests := []struct {
		name      string
		secrets   []string
		body      string
		signature string
		want      int
	}{
		{"the published example", []string{exampleSecret}, exampleBody, exampleSignature, 1},
		{"two signatures, both secrets", []string{exampleSecret, otherSecret}, exampleBody, both, 2},
		{"two signatures, one secret", []string{otherSecret}, exampleBody, both, 1},
		{"a tampered body", []string{exampleSecret}, `{"test": 2432232315}`, exampleSignature, 0},
		{"no secrets", nil, exampleBody, exampleSignature, 0},
		{"another version", []string{exampleSecret}, exampleBody, "v1a" + exampleSignature[2:], 0},
		{"bad base64", []string{exampleSecret}, exampleBody, "v1,!!!", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := start(t)
			if tt.secrets != nil {
				setSecrets(t, base, "auto", tt.secrets...)
			}
			post(t, base, "/hook/auto", tt.body, tt.signature)
			got := received(t, base, "auto")
			if len(got) != 1 || got[0].SignaturesValid != tt.want || got[0].WebhookID != exampleID {
				t.Fatalf("received = %+v, want %d valid signatures", got, tt.want)
			}
		})
	}
}

func TestSecretsAreReplacedAndCheckedWhenListed(t *testing.T) {
	base := start(t)
	setSecrets(t, base, "auto", otherSecret)
	post(t, base, "/hook/auto", exampleBody, exampleSignature)
	if got := received(t, base, "auto"); got[0].SignaturesValid != 0 {
		t.Fatalf("with another secret: %d valid", got[0].SignaturesValid)
	}
	setSecrets(t, base, "auto", exampleSecret)
	if got := received(t, base, "auto"); got[0].SignaturesValid != 1 {
		t.Fatalf("after replacing the secrets: %d valid", got[0].SignaturesValid)
	}
	setSecrets(t, base, "auto")
	if got := received(t, base, "auto"); got[0].SignaturesValid != 0 {
		t.Fatalf("after clearing the secrets: %d valid", got[0].SignaturesValid)
	}
}

func TestRecord(t *testing.T) {
	base := start(t)
	status, _, _ := call(t, http.MethodPost, base+"/hook/auto?x=1", exampleBody, "Authorization", "Bearer t0k",
		"X-Multi", "a", "X-Multi", "b")
	if status != http.StatusOK {
		t.Fatalf("POST = %d", status)
	}
	if status, _, _ := call(t, http.MethodPut, base+"/hook/auto/sub/path", "not json"); status != http.StatusOK {
		t.Fatalf("PUT below the hook = %d", status)
	}
	if status, _, _ := call(t, http.MethodPost, base+"/hook/automatic", "{}"); status != http.StatusOK {
		t.Fatalf("POST to another name = %d", status)
	}
	if status, _, _ := call(t, http.MethodPost, base+"/elsewhere", "{}"); status != http.StatusNotFound {
		t.Fatalf("POST outside /hook/ = %d", status)
	}
	got := received(t, base, "auto")
	if len(got) != 2 {
		t.Fatalf("received %d requests, want 2: %+v", len(got), got)
	}
	first := got[0]
	if first.Method != http.MethodPost || first.Path != "/hook/auto" || first.Query != "x=1" ||
		first.Status != http.StatusOK || first.AtMs == 0 || first.WebhookID != "" {
		t.Errorf("first = %+v", first)
	}
	if first.Headers["authorization"] != "Bearer t0k" || first.Headers["x-multi"] != "a, b" {
		t.Errorf("headers = %v, want lower-case keys", first.Headers)
	}
	for name := range first.Headers {
		if name != strings.ToLower(name) {
			t.Errorf("header %q is not lower-case", name)
		}
	}
	if body, ok := first.Body.(map[string]any); !ok || body["test"] != float64(2432232314) {
		t.Errorf("body = %#v, want the parsed JSON", first.Body)
	}
	if got[1].Method != http.MethodPut || got[1].Path != "/hook/auto/sub/path" || got[1].Body != "not json" {
		t.Errorf("second = %+v, want the text body", got[1])
	}
	if other := received(t, base, "automatic"); len(other) != 1 {
		t.Errorf("automatic received %d", len(other))
	}
	status, _, body := call(t, http.MethodGet, base+"/_fake/received/unknown", "")
	if status != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Errorf("unknown name = %d %s, want []", status, body)
	}
}

func TestBinaryBody(t *testing.T) {
	base := start(t)
	raw := "\xff\xfe"
	setSecrets(t, base, "bin", exampleSecret)
	post(t, base, "/hook/bin", raw, sign(t, exampleSecret, exampleID, exampleTimestamp, raw))
	if got := received(t, base, "bin"); len(got) != 1 || got[0].SignaturesValid != 1 {
		t.Fatalf("received = %+v, want the signature over the raw body", got)
	}
}

func TestFaults(t *testing.T) {
	base := start(t)
	fault := `{"path":"/hook/auto","status":302,"location":"http://elsewhere.example.org/x","times":1}`
	if status, _, body := call(t, http.MethodPost, base+"/_fake/faults", fault); status != http.StatusNoContent {
		t.Fatalf("fault = %d %s", status, body)
	}
	status, header, _ := call(t, http.MethodPost, base+"/hook/auto", "{}")
	if status != http.StatusFound || header.Get("Location") != "http://elsewhere.example.org/x" {
		t.Fatalf("faulted = %d %q", status, header.Get("Location"))
	}
	if status, _, _ := call(t, http.MethodPost, base+"/hook/auto", "{}"); status != http.StatusOK {
		t.Fatalf("after the fault = %d", status)
	}
	fault = `{"path":"/hook/auto","status":503,"retry_after_seconds":2}`
	if status, _, body := call(t, http.MethodPost, base+"/_fake/faults", fault); status != http.StatusNoContent {
		t.Fatalf("fault = %d %s", status, body)
	}
	if status, header, _ := call(t, http.MethodPost, base+"/hook/auto", "{}"); status != http.StatusServiceUnavailable ||
		header.Get("Retry-After") != "2" {
		t.Fatalf("faulted = %d %q", status, header.Get("Retry-After"))
	}
	got := received(t, base, "auto")
	if len(got) != 3 || got[0].Status != http.StatusFound || got[1].Status != http.StatusOK ||
		got[2].Status != http.StatusServiceUnavailable {
		t.Fatalf("received = %+v, want the statuses 302, 200, 503", got)
	}
}

func TestBadSecrets(t *testing.T) {
	base := start(t)
	for _, body := range []string{``, `null`, `{"secrets":[]}`, `[1]`, `["nope"]`, `["whsec_!!"]`, `[] []`} {
		status, _, answer := call(t, http.MethodPut, base+"/_fake/secrets/auto", body)
		if status != http.StatusBadRequest || !strings.Contains(answer, `"error"`) {
			t.Errorf("secrets %q = %d %s, want 400", body, status, answer)
		}
	}
}

// TestChat: the fake chat posts and edits messages, answers each with its id, takes replies in one step under a
// message and in two steps through a thread, refuses an unknown message or thread with 404, keeps chats apart by name,
// lists them and empties one.
func TestChat(t *testing.T) {
	base := start(t)
	ok := func(method, path, body, want string) {
		t.Helper()
		if status, _, got := call(t, method, base+path, body); status != http.StatusOK || strings.TrimSpace(got) != want {
			t.Errorf("%s %s = %d %s, want %s", method, path, status, got, want)
		}
	}
	ok(http.MethodPost, "/chat/ops/messages", `{"text":"#1 firing"}`, `{"data":{"id":"m1"}}`)
	ok(http.MethodPost, "/chat/ops/messages", `plain text`, `{"data":{"id":"m2"}}`)
	ok(http.MethodPut, "/chat/ops/messages/m1", `{"text":"#1 acknowledged"}`, `{"data":{"id":"m1"}}`)
	ok(http.MethodPost, "/chat/ops/messages/m1/replies", `{"text":"one step"}`, `{"data":{"id":"r1"}}`)
	ok(http.MethodPost, "/chat/ops/threads", `{"root":"m2"}`, `{"thread":{"id":"t1"}}`)
	ok(http.MethodPost, "/chat/ops/threads/t1/messages", `{"text":"two steps"}`, `{"data":{"id":"r2"}}`)
	ok(http.MethodPost, "/chat/other/messages", `{"text":"x"}`, `{"data":{"id":"m1"}}`)
	for _, path := range []string{"/chat/ops/messages/m9", "/chat/ops/messages/m9/replies",
		"/chat/ops/threads/t9/messages"} {
		method := http.MethodPost
		if !strings.HasSuffix(path, "s") {
			method = http.MethodPut
		}
		if status, _, _ := call(t, method, base+path, `{}`); status != http.StatusNotFound {
			t.Errorf("%s %s = %d", method, path, status)
		}
	}
	_, _, body := call(t, http.MethodGet, base+"/_fake/chat/ops", "")
	var c fakewebhook.Chat
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Messages) != 2 || c.Messages[0].Text != "#1 firing" || c.Messages[1].Text != "plain text" ||
		len(c.Messages[0].Replies) != 1 || c.Messages[0].Replies[0].Text != "one step" ||
		len(c.Edits) != 1 || c.Edits[0].ID != "m1" || c.Edits[0].Text != "#1 acknowledged" ||
		len(c.Threads) != 1 || string(c.Threads[0].Body) != `{"root":"m2"}` ||
		len(c.Threads[0].Messages) != 1 || c.Threads[0].Messages[0].Text != "two steps" {
		t.Errorf("chat %s", body)
	}
	if status, _, _ := call(t, http.MethodDelete, base+"/_fake/chat/ops", ""); status != http.StatusNoContent {
		t.Errorf("reset = %d", status)
	}
	if _, _, body := call(t, http.MethodGet, base+"/_fake/chat/ops", ""); strings.TrimSpace(body) !=
		`{"messages":[],"edits":[],"threads":[]}` {
		t.Errorf("after the reset %s", body)
	}
}
