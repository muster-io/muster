// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakealertmanager_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakealertmanager"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// webhook holds the fields Muster reads from an Alertmanager webhook.
type webhook struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   *int              `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []struct {
		Status       string            `json:"status"`
		Labels       map[string]string `json:"labels"`
		Annotations  map[string]string `json:"annotations"`
		StartsAt     string            `json:"startsAt"`
		EndsAt       string            `json:"endsAt"`
		GeneratorURL string            `json:"generatorURL"`
		Fingerprint  string            `json:"fingerprint"`
	} `json:"alerts"`
}

type delivery struct {
	header http.Header
	body   []byte
}

// sink stands for Muster's ingestion endpoint: it keeps what it receives and answers with status(n), n counting
// from 1.
type sink struct {
	*httptest.Server
	mu         sync.Mutex
	deliveries []delivery
	n          atomic.Int32
}

func newSink(t *testing.T, status func(n int32) int) *sink {
	t.Helper()
	s := &sink{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.deliveries = append(s.deliveries, delivery{header: r.Header.Clone(), body: body})
		s.mu.Unlock()
		w.WriteHeader(status(s.n.Add(1)))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *sink) received() []delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]delivery(nil), s.deliveries...)
}

func always(status int) func(int32) int { return func(int32) int { return status } }

func startFake(t *testing.T) *fakealertmanager.Fake {
	t.Helper()
	f := fakealertmanager.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return f
}

func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

var hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

func checkWebhook(t *testing.T, body []byte, externalURL string) webhook {
	t.Helper()
	var w webhook
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if w.Version != "4" || w.GroupKey == "" || w.Status != "firing" || w.Receiver == "" || len(w.Alerts) == 0 {
		t.Errorf("webhook = %+v, want version 4, a groupKey, status firing, a receiver and alerts", w)
	}
	if w.TruncatedAlerts == nil || *w.TruncatedAlerts != 0 || w.ExternalURL != externalURL {
		t.Errorf("truncatedAlerts %v, externalURL %q, want 0 and %q", w.TruncatedAlerts, w.ExternalURL, externalURL)
	}
	if len(w.GroupLabels) == 0 || len(w.CommonLabels) == 0 || len(w.CommonAnnotations) == 0 {
		t.Errorf("group labels %v, common labels %v, common annotations %v", w.GroupLabels, w.CommonLabels,
			w.CommonAnnotations)
	}
	for _, a := range w.Alerts {
		if a.Status != "firing" || len(a.Labels) == 0 || len(a.Annotations) == 0 || a.GeneratorURL == "" {
			t.Errorf("alert = %+v", a)
		}
		if _, err := time.Parse(time.RFC3339, a.StartsAt); err != nil {
			t.Errorf("startsAt %q: %v", a.StartsAt, err)
		}
		if a.EndsAt != "0001-01-01T00:00:00Z" || !hex16.MatchString(a.Fingerprint) {
			t.Errorf("endsAt %q, fingerprint %q", a.EndsAt, a.Fingerprint)
		}
	}
	return w
}

func TestSend(t *testing.T) {
	s := newSink(t, always(http.StatusAccepted))
	f := fakealertmanager.New()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	status, err := f.Send(t.Context(), fakealertmanager.Endpoint{URL: s.URL, Token: "mstr_int_test"}, f.Webhook(7, now))
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("Send = %d, %v; want 202", status, err)
	}
	status, err = f.Send(t.Context(), fakealertmanager.Endpoint{URL: s.URL}, f.Webhook(8, now))
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("Send without a token = %d, %v; want 202", status, err)
	}
	got := s.received()
	if len(got) != 2 {
		t.Fatalf("received %d webhooks, want 2", len(got))
	}
	h := got[0].header
	if h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "Alertmanager/0.34.1" ||
		h.Get("Authorization") != "Bearer mstr_int_test" {
		t.Errorf("headers = %v", h)
	}
	if a := got[1].header.Get("Authorization"); a != "" {
		t.Errorf("Authorization without a token = %q", a)
	}
	w := checkWebhook(t, got[0].body, "http://localhost:9093")
	if w.Alerts[0].StartsAt != "2026-10-05T12:00:00Z" {
		t.Errorf("startsAt = %q, want the given time", w.Alerts[0].StartsAt)
	}
	other := checkWebhook(t, got[1].body, "http://localhost:9093")
	if w.Alerts[0].Fingerprint == other.Alerts[0].Fingerprint || w.GroupKey == other.GroupKey {
		t.Error("webhooks with different sequence numbers have the same fingerprint or groupKey")
	}
}

func TestSendDoesNotFollowRedirects(t *testing.T) {
	s := newSink(t, always(http.StatusTemporaryRedirect))
	status, err := fakealertmanager.New().Send(t.Context(), fakealertmanager.Endpoint{URL: s.URL}, []byte("{}"))
	if err != nil || status != http.StatusTemporaryRedirect {
		t.Errorf("Send = %d, %v; want 307", status, err)
	}
	if _, err := fakealertmanager.New().Send(t.Context(), fakealertmanager.Endpoint{URL: "::"}, nil); err == nil {
		t.Error("Send to an invalid URL succeeded")
	}
}

func TestControlSend(t *testing.T) {
	f := startFake(t)
	s := newSink(t, always(http.StatusAccepted))

	status, body := post(t, f.URL()+"/_fake/send", `{"url":"`+s.URL+`","token":"tok"}`)
	if status != http.StatusOK || body != "{\"status\":202}\n" {
		t.Errorf("default payload: %d %q, want 200 {\"status\":202}", status, body)
	}
	status, body = post(t, f.URL()+"/_fake/send", `{"url":"`+s.URL+`","payload":{"custom":true}}`)
	if status != http.StatusOK || body != "{\"status\":202}\n" {
		t.Errorf("custom payload: %d %q", status, body)
	}
	got := s.received()
	if len(got) != 2 {
		t.Fatalf("received %d webhooks, want 2", len(got))
	}
	checkWebhook(t, got[0].body, f.URL())
	if got[0].header.Get("Authorization") != "Bearer tok" {
		t.Errorf("Authorization = %q", got[0].header.Get("Authorization"))
	}
	if string(got[1].body) != `{"custom":true}` {
		t.Errorf("custom payload arrived as %s", got[1].body)
	}
	if reqs := f.Requests(); len(reqs) != 0 {
		t.Errorf("control requests were recorded: %+v", reqs)
	}
}

func TestControlSendErrors(t *testing.T) {
	f := startFake(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{"transport error", `{"url":"` + closed.URL + `"}`, http.StatusBadGateway, "connect"},
		{"no url", `{}`, http.StatusBadRequest, "url must be an absolute http or https URL"},
		{"relative url", `{"url":"/api/v1/ingest"}`, http.StatusBadRequest, "url must be"},
		{"other scheme", `{"url":"ftp://example.org"}`, http.StatusBadRequest, "url must be"},
		{"public name", `{"url":"http://example.org/api/v1/ingest"}`, http.StatusBadRequest, loopbackRule},
		{"private address", `{"url":"http://10.0.0.1:8081"}`, http.StatusBadRequest, loopbackRule},
		{"unspecified address", `{"url":"http://0.0.0.0:8081"}`, http.StatusBadRequest, loopbackRule},
		{"localhost suffix", `{"url":"http://localhost.example.org"}`, http.StatusBadRequest, loopbackRule},
		{"unknown field", `{"url":"http://127.0.0.1:1","to":"x"}`, http.StatusBadRequest, "unknown field"},
		{"invalid payload", `{"url":"http://127.0.0.1:1","payload":{`, http.StatusBadRequest, "invalid JSON body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := post(t, f.URL()+"/_fake/send", tt.body)
			if status != tt.wantStatus || !strings.Contains(body, tt.wantError) {
				t.Errorf("answer %d %s, want %d with %q", status, body, tt.wantStatus, tt.wantError)
			}
		})
	}
}

const loopbackRule = "development mode reaches nothing but loopback"

func TestControlSendReachesLoopback(t *testing.T) {
	f := startFake(t)
	s := newSink(t, always(http.StatusAccepted))
	port := s.URL[strings.LastIndex(s.URL, ":")+1:]
	for _, u := range []string{s.URL, "http://localhost:" + port, "http://LOCALHOST:" + port} {
		if status, body := post(t, f.URL()+"/_fake/send", `{"url":"`+u+`"}`); status != http.StatusOK {
			t.Errorf("send to %s = %d %s, want 200", u, status, body)
		}
	}
	// Nothing listens there; the rule lets it through to the transport error.
	if status, body := post(t, f.URL()+"/_fake/send", `{"url":"http://[::1]:1"}`); status != http.StatusBadGateway {
		t.Errorf("send to [::1]:1 = %d %s, want 502", status, body)
	}
	if n := len(s.received()); n != 3 {
		t.Errorf("the sink received %d webhooks, want 3", n)
	}
}

func TestGoAPIIsNotLimitedToLoopback(t *testing.T) {
	// A cancelled context stops the request before it dials, so only the missing rule shows.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := fakealertmanager.New().Send(ctx, fakealertmanager.Endpoint{URL: "http://192.0.2.1:8081"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Send to a public address = %v, want the request attempted and cancelled", err)
	}
}

func TestOtherPathsAreRecorded(t *testing.T) {
	f := startFake(t)
	status, _ := post(t, f.URL()+"/api/v2/alerts", `[]`)
	if status != http.StatusNotImplemented {
		t.Errorf("POST /api/v2/alerts = %d, want 501", status)
	}
	if reqs := f.Requests(); len(reqs) != 1 || reqs[0].Path != "/api/v2/alerts" {
		t.Errorf("recorded %+v", reqs)
	}
}

func TestLoad(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	tests := []struct {
		name                                   string
		status                                 func(int32) int
		url                                    string
		wantAccepted, wantRejected, wantFailed int
	}{
		{"accepted", always(http.StatusAccepted), "", 10, 0, 0},
		{"half rejected", func(n int32) int {
			if n%2 == 0 {
				return http.StatusInternalServerError
			}
			return http.StatusAccepted
		}, "", 5, 5, 0},
		{"failed", nil, closed.URL, 0, 0, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := tt.url
			var s *sink
			if tt.status != nil {
				s = newSink(t, tt.status)
				url = s.URL
			}
			f := fakealertmanager.New()
			rep := f.Load(t.Context(), fakealertmanager.LoadOptions{
				Endpoint: fakealertmanager.Endpoint{URL: url, Token: "tok"},
				Rate:     100,
				Duration: 100 * time.Millisecond,
			})
			if rep.Sent != 10 || rep.Accepted != tt.wantAccepted || rep.Rejected != tt.wantRejected ||
				rep.Failed != tt.wantFailed {
				t.Errorf("report = %+v, want 10 sent: %d accepted, %d rejected, %d failed", rep, tt.wantAccepted,
					tt.wantRejected, tt.wantFailed)
			}
			answered := tt.wantAccepted + tt.wantRejected
			if answered > 0 && (rep.P50 <= 0 || rep.P50 > rep.P95 || rep.P95 > rep.P99 || rep.P99 > rep.Max) {
				t.Errorf("latencies p50 %v, p95 %v, p99 %v, max %v", rep.P50, rep.P95, rep.P99, rep.Max)
			}
			if answered == 0 && rep.Max != 0 {
				t.Errorf("max latency %v without answers", rep.Max)
			}
			if s != nil {
				seen := map[string]bool{}
				for _, d := range s.received() {
					seen[string(d.body)] = true
				}
				if len(seen) != 10 {
					t.Errorf("%d distinct bodies among 10 sends", len(seen))
				}
			}
		})
	}
	zero := fakealertmanager.LoadOptions{Rate: 0, Duration: time.Second}
	if rep := fakealertmanager.New().Load(t.Context(), zero); rep.Sent != 0 {
		t.Errorf("a zero rate sent %d webhooks", rep.Sent)
	}
}

func TestLoadStopsOnCancel(t *testing.T) {
	s := newSink(t, always(http.StatusAccepted))
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
	defer cancel()
	begin := time.Now()
	rep := fakealertmanager.New().Load(ctx, fakealertmanager.LoadOptions{
		Endpoint: fakealertmanager.Endpoint{URL: s.URL},
		Rate:     20,
		Duration: 10 * time.Second,
	})
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("Load returned after %v, want soon after the cancel", elapsed)
	}
	if rep.Sent == 0 || rep.Sent > 5 {
		t.Errorf("sent %d webhooks before the cancel, want 1 to 5", rep.Sent)
	}
}

func TestReportString(t *testing.T) {
	rep := fakealertmanager.LoadReport{
		Sent: 4, Accepted: 2, Rejected: 1, Failed: 1,
		P50: 1234 * time.Microsecond, P95: 2 * time.Millisecond, P99: 3 * time.Millisecond, Max: 4 * time.Millisecond,
	}
	want := "accepted 2, rejected 1, failed 1 of 4 sent\nlatency p50 1.2ms, p95 2ms, p99 3ms, max 4ms"
	if got := rep.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestControlLoad(t *testing.T) {
	f := startFake(t)
	s := newSink(t, always(http.StatusAccepted))
	status, body := post(t, f.URL()+"/_fake/load", `{"url":"`+s.URL+`","rate_per_second":100,"duration_seconds":0.05}`)
	if status != http.StatusOK {
		t.Fatalf("POST /_fake/load = %d %s", status, body)
	}
	var rep struct {
		Sent     int      `json:"sent"`
		Accepted int      `json:"accepted"`
		Rejected int      `json:"rejected"`
		Failed   int      `json:"failed"`
		P50      *float64 `json:"p50_ms"`
		P95      *float64 `json:"p95_ms"`
		P99      *float64 `json:"p99_ms"`
		Max      *float64 `json:"max_ms"`
	}
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if rep.Sent != 5 || rep.Accepted != 5 || rep.P50 == nil || rep.P95 == nil || rep.P99 == nil || rep.Max == nil {
		t.Errorf("report = %s, want 5 sent and accepted with latencies", body)
	}
	for _, d := range s.received() {
		checkWebhook(t, d.body, f.URL())
	}

	for _, bad := range []string{
		`{"url":"` + s.URL + `","rate_per_second":0,"duration_seconds":1}`,
		`{"url":"` + s.URL + `","rate_per_second":10001,"duration_seconds":1}`,
		`{"url":"` + s.URL + `","rate_per_second":10,"duration_seconds":0}`,
		`{"url":"` + s.URL + `","rate_per_second":10,"duration_seconds":3601}`,
		`{"rate_per_second":10,"duration_seconds":1}`,
		`{"url":"` + s.URL + `","rate":10}`,
		`{"url":"http://example.org","rate_per_second":10,"duration_seconds":1}`,
		`{"url":"http://192.0.2.1:8081","rate_per_second":10,"duration_seconds":1}`,
	} {
		if status, body := post(t, f.URL()+"/_fake/load", bad); status != http.StatusBadRequest {
			t.Errorf("POST /_fake/load %s = %d %s, want 400", bad, status, body)
		}
	}
}

func TestCloseStopsALoadRun(t *testing.T) {
	f := fakealertmanager.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	s := newSink(t, always(http.StatusAccepted))
	type answer struct {
		status int
		body   string
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		body := `{"url":"` + s.URL + `","rate_per_second":20,"duration_seconds":600}`
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.URL()+"/_fake/load", strings.NewReader(body))
		if err != nil {
			done <- answer{err: err}
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- answer{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		done <- answer{resp.StatusCode, string(b), err}
	}()
	for len(s.received()) == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	begin := time.Now()
	if err := f.Close(ctx); err != nil {
		t.Errorf("Close during a load run: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Errorf("Close took %v during a load run", elapsed)
	}
	a := <-done
	var rep struct{ Sent int }
	if a.err != nil || a.status != http.StatusOK || json.Unmarshal([]byte(a.body), &rep) != nil || rep.Sent >= 12_000 {
		t.Errorf("the load run answered %d %s (%v), want 200 with a partial report", a.status, a.body, a.err)
	}
}

func TestFakeServerHarness(t *testing.T) {
	f := startFake(t)
	if err := f.SetFault(fakeserver.Fault{Path: "/api/v2/status", Status: http.StatusServiceUnavailable}); err != nil {
		t.Fatal(err)
	}
	if status, _ := post(t, f.URL()+"/api/v2/status", ""); status != http.StatusServiceUnavailable {
		t.Errorf("faulted path = %d, want 503", status)
	}
	if f.Name() != "Alertmanager" {
		t.Errorf("Name() = %q", f.Name())
	}
}
