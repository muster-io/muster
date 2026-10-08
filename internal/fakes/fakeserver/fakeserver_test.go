// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeserver_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// echo answers 200 with the body it received, so tests can see that recording leaves the body to the handler.
type echo struct{ calls atomic.Int32 }

func (e *echo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.calls.Add(1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write(body)
}

func start(t *testing.T) (*fakeserver.Server, *echo) {
	t.Helper()
	h := &echo{}
	s := fakeserver.New("Test", h)
	if err := s.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s, h
}

type response struct {
	status int
	header http.Header
	body   string
}

func do(t *testing.T, method, url, body string, header ...string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Add(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

func recorded(t *testing.T, s *fakeserver.Server) []fakeserver.Request {
	t.Helper()
	r := do(t, http.MethodGet, s.URL()+"/_fake/requests", "")
	if r.status != http.StatusOK {
		t.Fatalf("GET /_fake/requests: status %d, body %s", r.status, r.body)
	}
	var reqs []fakeserver.Request
	if err := json.Unmarshal([]byte(r.body), &reqs); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
	return reqs
}

func setFault(t *testing.T, s *fakeserver.Server, body string) {
	t.Helper()
	if r := do(t, http.MethodPost, s.URL()+"/_fake/faults", body); r.status != http.StatusNoContent {
		t.Fatalf("POST /_fake/faults %s: status %d, body %s", body, r.status, r.body)
	}
}

func TestRecording(t *testing.T) {
	s, _ := start(t)
	if r := do(t, http.MethodGet, s.URL()+"/_fake/requests", ""); r.body != "[]\n" {
		t.Errorf("empty record = %q, want []", r.body)
	}
	before := time.Now().UnixMilli()
	r := do(t, http.MethodPost, s.URL()+"/api/v4/posts?a=1&b=two", `{"message":"hi"}`, "X-Test", "one", "X-Test", "two")
	if r.status != http.StatusOK || r.body != `{"message":"hi"}` {
		t.Errorf("handler answered %d %q, want 200 with the request body", r.status, r.body)
	}
	binary := []byte{0xff, 0xfe, 0x00, 'a'}
	do(t, http.MethodPut, s.URL()+"/raw", string(binary))
	after := time.Now().UnixMilli()

	reqs := recorded(t, s)
	if len(reqs) != 2 {
		t.Fatalf("recorded %d requests, want 2: %+v", len(reqs), reqs)
	}
	got := reqs[0]
	if got.Method != http.MethodPost || got.Path != "/api/v4/posts" || got.Query != "a=1&b=two" {
		t.Errorf("first request = %s %s ? %s", got.Method, got.Path, got.Query)
	}
	if got.Body != `{"message":"hi"}` || got.BodyEncoding != fakeserver.EncodingUTF8 {
		t.Errorf("first body = %q (%s)", got.Body, got.BodyEncoding)
	}
	if h := got.Headers["X-Test"]; len(h) != 2 || h[0] != "one" || h[1] != "two" {
		t.Errorf("X-Test = %q, want [one two]", h)
	}
	if got.AtMs < before || got.AtMs > after {
		t.Errorf("at_ms = %d, want between %d and %d", got.AtMs, before, after)
	}
	if reqs[1].BodyEncoding != fakeserver.EncodingBase64 || reqs[1].Body != base64.StdEncoding.EncodeToString(binary) {
		t.Errorf("binary body = %q (%s), want base64", reqs[1].Body, reqs[1].BodyEncoding)
	}
	if len(s.Requests()) != 2 {
		t.Errorf("Requests() has %d entries, want 2", len(s.Requests()))
	}

	if r := do(t, http.MethodDelete, s.URL()+"/_fake/requests", ""); r.status != http.StatusNoContent {
		t.Errorf("DELETE /_fake/requests: status %d", r.status)
	}
	if reqs := recorded(t, s); len(reqs) != 0 {
		t.Errorf("after reset: %d requests, want none", len(reqs))
	}
}

func TestRecordingLimits(t *testing.T) {
	h := &echo{}
	s := fakeserver.New("Test", h)
	for i := range fakeserver.MaxRequests + 1 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/r/"+strconv.Itoa(i), nil)
		s.ServeHTTP(httptest.NewRecorder(), req)
	}
	reqs := s.Requests()
	if len(reqs) != fakeserver.MaxRequests {
		t.Fatalf("kept %d requests, want %d", len(reqs), fakeserver.MaxRequests)
	}
	if reqs[0].Path != "/r/1" || reqs[len(reqs)-1].Path != "/r/10000" {
		t.Errorf("kept %s to %s, want /r/1 to /r/10000", reqs[0].Path, reqs[len(reqs)-1].Path)
	}
	s.ResetRequests()
	if len(s.Requests()) != 0 {
		t.Errorf("ResetRequests kept %d requests", len(s.Requests()))
	}

	big := bytes.Repeat([]byte("x"), fakeserver.MaxBodyBytes+10)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/big", bytes.NewReader(big)))
	if rec.Body.Len() != len(big) {
		t.Errorf("handler received %d bytes, want %d", rec.Body.Len(), len(big))
	}
	if got := len(s.Requests()[0].Body); got != fakeserver.MaxBodyBytes {
		t.Errorf("recorded %d bytes, want %d", got, fakeserver.MaxBodyBytes)
	}
}

func TestControlRequestsAreNotRecorded(t *testing.T) {
	s, _ := start(t)
	do(t, http.MethodGet, s.URL()+"/_fake/requests", "")
	setFault(t, s, `{"path":"/x","status":500}`)
	do(t, http.MethodDelete, s.URL()+"/_fake/faults", "")
	do(t, http.MethodGet, s.URL()+"/_fake/unknown", "")
	if reqs := s.Requests(); len(reqs) != 0 {
		t.Errorf("recorded control requests: %+v", reqs)
	}
}

func TestFaultAnswers(t *testing.T) {
	tests := []struct {
		name            string
		fault           string
		wantStatus      int
		wantRetryAfter  string
		wantContentType string
		wantBody        string
	}{
		{
			name:            "Mattermost rate limit",
			fault:           `{"path":"/api/v4/users/me","status":429,"retry_after_seconds":1,"body":"limit exceeded"}`,
			wantStatus:      http.StatusTooManyRequests,
			wantRetryAfter:  "1",
			wantContentType: "text/plain; charset=utf-8",
			wantBody:        "limit exceeded",
		},
		{
			name:            "JSON body",
			fault:           `{"path":"/api/v4/users/me","status":400,"body":"{\"ok\":false,\"error_code\":400}"}`,
			wantStatus:      http.StatusBadRequest,
			wantContentType: "application/json",
			wantBody:        `{"ok":false,"error_code":400}`,
		},
		{
			name:            "empty body is the status text",
			fault:           `{"path":"/api/v4/users/me","status":503,"retry_after_seconds":3}`,
			wantStatus:      http.StatusServiceUnavailable,
			wantRetryAfter:  "3",
			wantContentType: "text/plain; charset=utf-8",
			wantBody:        "Service Unavailable",
		},
		{
			name:       "no content",
			fault:      `{"path":"/api/v4/users/me","status":204}`,
			wantStatus: http.StatusNoContent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, h := start(t)
			setFault(t, s, tt.fault)
			r := do(t, http.MethodGet, s.URL()+"/api/v4/users/me", "")
			if r.status != tt.wantStatus || r.body != tt.wantBody {
				t.Errorf("answer = %d %q, want %d %q", r.status, r.body, tt.wantStatus, tt.wantBody)
			}
			if got := r.header.Get("Retry-After"); got != tt.wantRetryAfter {
				t.Errorf("Retry-After = %q, want %q", got, tt.wantRetryAfter)
			}
			if got := r.header.Get("Content-Type"); got != tt.wantContentType {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContentType)
			}
			if h.calls.Load() != 0 {
				t.Errorf("the handler ran %d times under a status fault", h.calls.Load())
			}
			if reqs := s.Requests(); len(reqs) != 1 {
				t.Errorf("recorded %d requests, want the faulted one", len(reqs))
			}
			if r := do(t, http.MethodGet, s.URL()+"/other", ""); r.status != http.StatusOK {
				t.Errorf("another path answered %d, want 200", r.status)
			}
		})
	}
}

func TestFaultReplaceAndReset(t *testing.T) {
	s, _ := start(t)
	setFault(t, s, `{"path":"/p","status":500}`)
	setFault(t, s, `{"path":"/p","status":502}`)
	if r := do(t, http.MethodGet, s.URL()+"/p", ""); r.status != http.StatusBadGateway {
		t.Errorf("after replacement: %d, want 502", r.status)
	}
	if r := do(t, http.MethodGet, s.URL()+"/p", ""); r.status != http.StatusBadGateway {
		t.Errorf("a fault stays until reset: %d, want 502", r.status)
	}
	if r := do(t, http.MethodDelete, s.URL()+"/_fake/faults", ""); r.status != http.StatusNoContent {
		t.Errorf("DELETE /_fake/faults: %d, want 204", r.status)
	}
	if r := do(t, http.MethodGet, s.URL()+"/p", ""); r.status != http.StatusOK {
		t.Errorf("after reset: %d, want 200", r.status)
	}

	if err := s.SetFault(fakeserver.Fault{Path: "/p", Status: 418}); err != nil {
		t.Fatalf("SetFault: %v", err)
	}
	if r := do(t, http.MethodGet, s.URL()+"/p", ""); r.status != http.StatusTeapot {
		t.Errorf("after SetFault: %d, want 418", r.status)
	}
	s.ResetFaults()
	if r := do(t, http.MethodGet, s.URL()+"/p", ""); r.status != http.StatusOK {
		t.Errorf("after ResetFaults: %d, want 200", r.status)
	}
	if err := s.SetFault(fakeserver.Fault{Path: "/p"}); err == nil {
		t.Error("SetFault accepted a fault without status and delay")
	}
}

func TestFaultDelay(t *testing.T) {
	s, h := start(t)
	setFault(t, s, `{"path":"/slow","delay_ms":50}`)
	begin := time.Now()
	r := do(t, http.MethodPost, s.URL()+"/slow", "payload")
	if elapsed := time.Since(begin); elapsed < 50*time.Millisecond {
		t.Errorf("answered after %v, want at least 50ms", elapsed)
	}
	if r.status != http.StatusOK || r.body != "payload" || h.calls.Load() != 1 {
		t.Errorf("after the delay: %d %q, handler ran %d times; want the normal answer", r.status, r.body, h.calls.Load())
	}

	setFault(t, s, `{"path":"/slow","delay_ms":50,"status":500}`)
	begin = time.Now()
	r = do(t, http.MethodGet, s.URL()+"/slow", "")
	if elapsed := time.Since(begin); elapsed < 50*time.Millisecond || r.status != http.StatusInternalServerError {
		t.Errorf("delayed status: %d after %v, want 500 after at least 50ms", r.status, elapsed)
	}
}

func TestFaultDelayEndsWhenTheClientGivesUp(t *testing.T) {
	h := &echo{}
	s := fakeserver.New("Test", h)
	if err := s.SetFault(fakeserver.Fault{Path: "/held", DelayMs: 60_000}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	begin := time.Now()
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/held", nil))
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Errorf("the held request returned after %v, want soon after the client gave up", elapsed)
	}
	if h.calls.Load() != 1 {
		t.Errorf("the handler ran %d times, want once before the delay", h.calls.Load())
	}
	if reqs := s.Requests(); len(reqs) != 1 || reqs[0].Status != 0 {
		t.Errorf("recorded %+v, want the request with status 0: no answer was written", reqs)
	}
}

func TestCloseEndsHeldDelays(t *testing.T) {
	h := &echo{}
	s := fakeserver.New("Test", h)
	if err := s.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFault(fakeserver.Fault{Path: "/held", DelayMs: 60_000}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL()+"/held", nil)
		if err != nil {
			done <- err
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	for len(s.Requests()) == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	begin := time.Now()
	if err := s.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("Close took %v with a held request", elapsed)
	}
	if err := <-done; err == nil {
		t.Error("the held request got an answer, want the response aborted")
	}
	if err := s.Close(ctx); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := s.Start(t.Context(), "127.0.0.1:0"); err == nil {
		t.Error("Start after Close succeeded")
	}
}

func TestCloseStopsRunningHandlers(t *testing.T) {
	s := fakeserver.New("Busy", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.WriteHeader(http.StatusOK)
	}))
	if err := s.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL()+"/busy", nil)
		if err != nil {
			return
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	for len(s.Requests()) == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	begin := time.Now()
	if err := s.Close(ctx); err != nil {
		t.Errorf("Close with a running handler: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("Close took %v with a running handler", elapsed)
	}
	<-done
}

func TestControlRefusesBrowsers(t *testing.T) {
	s, _ := start(t)
	tests := []struct {
		name, host, origin string
		wantStatus         int
	}{
		{name: "curl to 127.0.0.1", host: "127.0.0.1:18065", wantStatus: http.StatusOK},
		{name: "localhost", host: "localhost:18065", wantStatus: http.StatusOK},
		{name: "localhost in capitals", host: "LOCALHOST", wantStatus: http.StatusOK},
		{name: "IPv6 loopback", host: "[::1]:18065", wantStatus: http.StatusOK},
		{name: "IPv6 loopback without a port", host: "[::1]", wantStatus: http.StatusOK},
		{name: "rebound name", host: "evil.example", wantStatus: http.StatusForbidden},
		{name: "rebound name with a port", host: "evil.example:18065", wantStatus: http.StatusForbidden},
		{name: "other loopback address", host: "127.0.0.2:18065", wantStatus: http.StatusForbidden},
		{name: "Origin", host: "127.0.0.1:18065", origin: "http://evil.example", wantStatus: http.StatusForbidden},
		{name: "empty Origin", host: "127.0.0.1:18065", origin: "-", wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_fake/requests", nil)
			req.Host = tt.host
			switch tt.origin {
			case "":
			case "-":
				req.Header["Origin"] = []string{""}
			default:
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantStatus == http.StatusForbidden {
				var e struct{ Error string }
				if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error == "" {
					t.Errorf("body %q, want {\"error\": ...}", rec.Body)
				}
			}
		})
	}

	r := do(t, http.MethodPost, s.URL()+"/_fake/faults", `{"path":"/p","status":500}`, "Origin", "http://evil.example")
	if r.status != http.StatusForbidden || !strings.Contains(r.body, "Origin") {
		t.Errorf("POST /_fake/faults with an Origin = %d %s, want 403 naming the Origin header", r.status, r.body)
	}
	if r := do(t, http.MethodGet, s.URL()+"/p", "", "Origin", "http://evil.example"); r.status != http.StatusOK {
		t.Errorf("a refused fault applied, or a fake API path refused an Origin: %d", r.status)
	}
	if reqs := s.Requests(); len(reqs) != 1 || reqs[0].Path != "/p" {
		t.Errorf("recorded %+v, want the fake API request only", reqs)
	}
}

func TestFaultValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"invalid JSON", `{"path":`, "invalid JSON body"},
		{"two values", `{"path":"/a","status":500}{}`, "more than one JSON value"},
		{"unknown field", `{"path":"/a","status":500,"code":1}`, `unknown field "code"`},
		{"no path", `{"status":500}`, "path is required"},
		{"relative path", `{"path":"a","status":500}`, "path must start with /"},
		{"control path", `{"path":"/_fake/requests","status":500}`, "must not be under /_fake/"},
		{"control root", `{"path":"/_fake","status":500}`, "must not be under /_fake/"},
		{"status too low", `{"path":"/a","status":99}`, "status must be"},
		{"informational status", `{"path":"/a","status":100}`, "status must be"},
		{"status too high", `{"path":"/a","status":600}`, "status must be"},
		{"negative retry", `{"path":"/a","status":429,"retry_after_seconds":-1}`, "retry_after_seconds must not be"},
		{"negative delay", `{"path":"/a","delay_ms":-5}`, "delay_ms must not be negative"},
		{"nothing to do", `{"path":"/a"}`, "needs a status or a delay_ms"},
		{"body without status", `{"path":"/a","delay_ms":5,"body":"x"}`, "need a status"},
	}
	s, _ := start(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := do(t, http.MethodPost, s.URL()+"/_fake/faults", tt.body)
			if r.status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", r.status)
			}
			var e struct{ Error string }
			if err := json.Unmarshal([]byte(r.body), &e); err != nil || !strings.Contains(e.Error, tt.wantErr) {
				t.Errorf("body %q, want an error containing %q", r.body, tt.wantErr)
			}
		})
	}
	if r := do(t, http.MethodGet, s.URL()+"/a", ""); r.status != http.StatusOK {
		t.Errorf("a rejected fault applied: %d", r.status)
	}
}

func TestControlMethods(t *testing.T) {
	s, _ := start(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/_fake/requests"},
		{http.MethodPost, "/_fake/requests"},
		{http.MethodGet, "/_fake/faults"},
		{http.MethodPatch, "/_fake/faults"},
	} {
		if r := do(t, c.method, s.URL()+c.path, ""); r.status != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d, want 405", c.method, c.path, r.status)
		}
	}
}

func TestHandleControl(t *testing.T) {
	s, _ := start(t)
	s.HandleControl("POST /_fake/hello", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name string }
		if err := fakeserver.DecodeJSON(w, r, &in); err != nil {
			fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		fakeserver.WriteJSON(w, http.StatusOK, map[string]string{"hello": in.Name})
	})
	if r := do(t, http.MethodPost, s.URL()+"/_fake/hello", `{"name":"fake"}`); r.status != http.StatusOK ||
		r.body != "{\"hello\":\"fake\"}\n" {
		t.Errorf("POST /_fake/hello = %d %q", r.status, r.body)
	}
	if r := do(t, http.MethodPost, s.URL()+"/_fake/hello", `{"nope":1}`); r.status != http.StatusBadRequest {
		t.Errorf("POST /_fake/hello with an unknown field = %d, want 400", r.status)
	}
	if len(s.Requests()) != 0 {
		t.Error("an added control endpoint was recorded")
	}

	defer func() {
		if recover() == nil {
			t.Error("HandleControl accepted a pattern outside /_fake/")
		}
	}()
	s.HandleControl("GET /hello", func(http.ResponseWriter, *http.Request) {})
}

func TestStartAndClose(t *testing.T) {
	var lc net.ListenConfig
	busy, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	s := fakeserver.New("Busy", &echo{})
	if s.Name() != "Busy" || s.URL() != "" {
		t.Errorf("before Start: name %q, URL %q", s.Name(), s.URL())
	}
	if err := s.Start(t.Context(), busy.Addr().String()); err == nil {
		t.Error("Start on a busy address succeeded")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Errorf("Close of a server that never started: %v", err)
	}

	s2, _ := start(t)
	if !strings.HasPrefix(s2.URL(), "http://127.0.0.1:") {
		t.Errorf("URL = %q", s2.URL())
	}
	if err := s2.Start(t.Context(), "127.0.0.1:0"); err == nil {
		t.Error("a second Start succeeded")
	}
}

func TestCloseTimesOut(t *testing.T) {
	block := make(chan struct{})
	s := fakeserver.New("Slow", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	if err := s.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer close(block)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL()+"/stuck", nil)
		if err != nil {
			return
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	for len(s.Requests()) == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close with a stuck handler = %v, want the deadline", err)
	}
}

func TestRecordedStatus(t *testing.T) {
	s := fakeserver.New("Status", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/created":
			w.WriteHeader(http.StatusCreated)
			w.WriteHeader(http.StatusOK)
		case "/body":
			_, _ = io.WriteString(w, "ok")
		case "/abort":
			panic(http.ErrAbortHandler)
		}
	}))
	serve := func(p string) {
		defer func() { _ = recover() }()
		s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, p, nil))
	}
	if err := s.SetFault(fakeserver.Fault{Path: "/faulted", Status: http.StatusServiceUnavailable}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/created", "/body", "/nothing", "/abort", "/faulted"} {
		serve(p)
	}
	want := map[string]int{"/created": 201, "/body": 200, "/nothing": 200, "/abort": 0, "/faulted": 503}
	reqs := s.Requests()
	if len(reqs) != len(want) {
		t.Fatalf("recorded %d requests, want %d", len(reqs), len(want))
	}
	for _, r := range reqs {
		if r.Status != want[r.Path] {
			t.Errorf("%s: status %d, want %d", r.Path, r.Status, want[r.Path])
		}
	}
}

func TestRecordedStatusOfADroppedRequest(t *testing.T) {
	var s *fakeserver.Server
	s = fakeserver.New("Reset", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.ResetRequests()
		w.WriteHeader(http.StatusAccepted)
	}))
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/a", nil))
	if reqs := s.Requests(); len(reqs) != 0 {
		t.Errorf("a request dropped while it was served came back: %+v", reqs)
	}
}

func TestRecordedStatusOverHTTP(t *testing.T) {
	s, _ := start(t)
	setFault(t, s, `{"path":"/f","status":429}`)
	do(t, http.MethodGet, s.URL()+"/f", "")
	do(t, http.MethodGet, s.URL()+"/ok", "")
	reqs := recorded(t, s)
	if len(reqs) != 2 || reqs[0].Status != http.StatusTooManyRequests || reqs[1].Status != http.StatusOK {
		t.Errorf("recorded %+v, want statuses 429 and 200", reqs)
	}
}

func TestFaultTimes(t *testing.T) {
	s, h := start(t)
	setFault(t, s, `{"path":"/p","status":500,"times":2}`)
	for i, want := range []int{500, 500, 200, 200} {
		if r := do(t, http.MethodGet, s.URL()+"/p", ""); r.status != want {
			t.Errorf("request %d: %d, want %d", i+1, r.status, want)
		}
	}
	if h.calls.Load() != 2 {
		t.Errorf("the handler ran %d times, want 2 after the fault was used up", h.calls.Load())
	}

	setFault(t, s, `{"path":"/p","status":500,"times":1}`)
	setFault(t, s, `{"path":"/p","status":502,"times":1}`)
	if r := do(t, http.MethodGet, s.URL()+"/p", ""); r.status != http.StatusBadGateway {
		t.Errorf("after replacement: %d, want 502", r.status)
	}
	if r := do(t, http.MethodGet, s.URL()+"/p", ""); r.status != http.StatusOK {
		t.Errorf("a replaced fault kept the hits of the old one: %d, want 200", r.status)
	}
}

func TestFaultPatterns(t *testing.T) {
	s, _ := start(t)
	setFault(t, s, `{"path":"/api/v4/posts/*/patch","status":500}`)
	setFault(t, s, `{"path":"/api/v4/posts/*","status":501}`)
	setFault(t, s, `{"path":"/api/v4/posts/x*","status":502}`)
	setFault(t, s, `{"path":"/api/v4/posts/exact","status":503}`)
	tests := []struct {
		path string
		want int
	}{
		{"/api/v4/posts/abc/patch", 500},
		{"/api/v4/posts/abc", 501},
		{"/api/v4/posts/xyz", 501},
		{"/api/v4/posts/exact", 503},
		{"/api/v4/posts/a/b/patch", 200},
		{"/api/v4/posts", 200},
	}
	for _, tt := range tests {
		if r := do(t, http.MethodGet, s.URL()+tt.path, ""); r.status != tt.want {
			t.Errorf("%s: %d, want %d", tt.path, r.status, tt.want)
		}
	}
	setFault(t, s, `{"path":"/api/v4/posts/*","status":504,"times":1}`)
	if r := do(t, http.MethodGet, s.URL()+"/api/v4/posts/abc", ""); r.status != http.StatusGatewayTimeout {
		t.Errorf("a replaced pattern lost its place: %d, want 504", r.status)
	}
	if r := do(t, http.MethodGet, s.URL()+"/api/v4/posts/xyz", ""); r.status != http.StatusBadGateway {
		t.Errorf("after the first pattern was used up: %d, want 502", r.status)
	}
}

func TestFaultContentType(t *testing.T) {
	s, _ := start(t)
	setFault(t, s, `{"path":"/p","status":429,"body":"{\"a\":1}","content_type":"text/plain; charset=utf-8"}`)
	r := do(t, http.MethodGet, s.URL()+"/p", "")
	if r.status != http.StatusTooManyRequests || r.body != `{"a":1}` ||
		r.header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("answer %d %q as %q, want 429 with the body as text", r.status, r.body, r.header.Get("Content-Type"))
	}
}

func TestFaultDelayHoldsTheAnswer(t *testing.T) {
	var made atomic.Int32
	s := fakeserver.New("Hold", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		made.Add(1)
		w.Header().Set("X-Made", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "created")
	}))
	if err := s.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if err := s.SetFault(fakeserver.Fault{Path: "/post", DelayMs: 50}); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	r := do(t, http.MethodPost, s.URL()+"/post", "")
	if elapsed := time.Since(begin); elapsed < 50*time.Millisecond {
		t.Errorf("answered after %v, want at least 50ms", elapsed)
	}
	if r.status != http.StatusCreated || r.body != "created" || r.header.Get("X-Made") != "yes" {
		t.Errorf("held answer = %d %q, X-Made %q", r.status, r.body, r.header.Get("X-Made"))
	}
	if reqs := s.Requests(); len(reqs) != 1 || reqs[0].Status != http.StatusCreated {
		t.Errorf("recorded %+v, want status 201", reqs)
	}

	if err := s.SetFault(fakeserver.Fault{Path: "/post", DelayMs: 60_000}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL()+"/post", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the client got an answer during the delay")
	}
	if made.Load() != 2 {
		t.Errorf("the handler ran %d times, want twice: the change is made before the delay", made.Load())
	}
}

func TestFaultValidationOfTheNewFields(t *testing.T) {
	for _, tt := range []struct {
		fault   fakeserver.Fault
		wantErr string
	}{
		{fakeserver.Fault{Path: "/a[", Status: 500}, "not a valid pattern"},
		{fakeserver.Fault{Path: "/a", Status: 500, Times: -1}, "times must not be negative"},
		{fakeserver.Fault{Path: "/a", DelayMs: 5, ContentType: "text/plain"}, "need a status"},
	} {
		if err := tt.fault.Validate(); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("Validate(%+v) = %v, want %q", tt.fault, err, tt.wantErr)
		}
	}
	if err := (fakeserver.Fault{Path: "/a/*/b", Status: 500, Times: 3, ContentType: "text/plain"}).Validate(); err != nil {
		t.Errorf("a valid fault: %v", err)
	}
}
