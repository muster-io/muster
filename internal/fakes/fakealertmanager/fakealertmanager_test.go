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

// receivedRequest is a request a pathSink received.
type receivedRequest struct {
	path, authorization, contentType string
	size                             int
	body                             string
}

// pathSink stands for Muster's ingestion endpoint, keeping the path of each request: 202 with a token, 401 without.
func pathSink(t *testing.T) (*httptest.Server, func() []receivedRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []receivedRequest
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, receivedRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"), size: len(body), body: string(body)})
		mu.Unlock()
		if r.Header.Get("Authorization") == "" && !strings.HasPrefix(r.URL.Path, "/api/v1/ingest/") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(s.Close)
	return s, func() []receivedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]receivedRequest(nil), got...)
	}
}

// TestReceivers is C-01.FR-13: receivers registered with the token in the header or in the path; a send to a
// receiver with a JSON payload, a raw body with its content type, a filler of a given size, another token or none.
func TestReceivers(t *testing.T) {
	f := startFake(t)
	s, got := pathSink(t)
	for _, body := range []string{
		`{"name":"prod-eu","url":"` + s.URL + `/api/v1/ingest","token":"tok"}`,
		`{"name":"in-path","url":"` + s.URL + `/api/v1/ingest/","token":"t/k","token_in":"path"}`,
	} {
		if status, answer := post(t, f.URL()+"/_fake/receivers", body); status != http.StatusNoContent {
			t.Fatalf("POST /_fake/receivers %s = %d %s", body, status, answer)
		}
	}
	sends := []struct{ body, want string }{
		{`{"receiver":"prod-eu","payload":{"version":"4","alerts":[]}}`, `{"status":202}`},
		{`{"receiver":"prod-eu","raw":"not json","content_type":"text/plain"}`, `{"status":202}`},
		{`{"receiver":"prod-eu","raw":"{}"}`, `{"status":202}`},
		{`{"receiver":"prod-eu","size_bytes":1000}`, `{"status":202}`},
		{`{"receiver":"prod-eu","raw":"{}","token":"other"}`, `{"status":202}`},
		{`{"receiver":"prod-eu","raw":"{}","no_token":true}`, `{"status":401}`},
		{`{"receiver":"in-path"}`, `{"status":202}`},
		{`{"receiver":"in-path","raw":"{}","no_token":true}`, `{"status":202}`},
	}
	for _, tt := range sends {
		if status, answer := post(t, f.URL()+"/_fake/send", tt.body); status != http.StatusOK ||
			strings.TrimSpace(answer) != tt.want {
			t.Errorf("send %s = %d %s, want %s", tt.body, status, answer, tt.want)
		}
	}
	r := got()
	if len(r) != len(sends) {
		t.Fatalf("received %d, want %d", len(r), len(sends))
	}
	checks := []struct {
		ok   bool
		what string
	}{
		{r[0].path == "/api/v1/ingest" && r[0].authorization == "Bearer tok" &&
			r[0].body == `{"version":"4","alerts":[]}` && r[0].contentType == "application/json", "payload"},
		{r[1].body == "not json" && r[1].contentType == "text/plain", "raw with a content type"},
		{r[2].body == "{}" && r[2].contentType == "", "raw without a content type"},
		{r[3].size == 1000, "filler"},
		{r[4].authorization == "Bearer other", "another token"},
		{r[5].authorization == "", "no token"},
		{r[6].path == "/api/v1/ingest/t/k" && r[6].authorization == "", "token in the path"},
		{r[7].path == "/api/v1/ingest/", "no token in the path"},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: %+v", c.what, r)
		}
	}
	// A webhook generated for a path receiver carries no Authorization header.
	checkWebhook(t, []byte(r[6].body), f.URL())
}

func TestReceiverErrors(t *testing.T) {
	f := startFake(t)
	s, _ := pathSink(t)
	for _, bad := range []string{
		`{"name":"","url":"` + s.URL + `"}`,
		`{"name":"x","url":"http://example.org"}`,
		`{"name":"x","url":"` + s.URL + `","token_in":"query"}`,
		`{"name":"x","url":"` + s.URL + `","other":1}`,
	} {
		if status, body := post(t, f.URL()+"/_fake/receivers", bad); status != http.StatusBadRequest {
			t.Errorf("POST /_fake/receivers %s = %d %s, want 400", bad, status, body)
		}
	}
	if err := f.Register(fakealertmanager.Receiver{URL: s.URL}); err == nil {
		t.Error("a receiver without a name was registered")
	}
	if status, _ := post(t, f.URL()+"/_fake/receivers", `{"name":"ok","url":"`+s.URL+`"}`); status != http.StatusNoContent {
		t.Fatal("no receiver")
	}
	for _, bad := range []string{
		`{"receiver":"missing"}`,
		`{"receiver":"ok","url":"` + s.URL + `"}`,
		`{"receiver":"ok","raw":"x","size_bytes":1}`,
		`{"receiver":"ok","payload":{},"raw":"x"}`,
		`{"receiver":"ok","size_bytes":-1}`,
		`{"receiver":"ok","size_bytes":100000000}`,
	} {
		if status, body := post(t, f.URL()+"/_fake/send", bad); status != http.StatusBadRequest {
			t.Errorf("POST /_fake/send %s = %d %s, want 400", bad, status, body)
		}
	}
}

// TestLoadToAReceiver: the load mode sends to a registered receiver.
func TestLoadToAReceiver(t *testing.T) {
	f := startFake(t)
	s, got := pathSink(t)
	if err := f.Register(fakealertmanager.Receiver{Name: "muster", URL: s.URL + "/api/v1/ingest", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	status, body := post(t, f.URL()+"/_fake/load", `{"receiver":"muster","rate_per_second":100,"duration_seconds":0.03}`)
	if status != http.StatusOK || !strings.Contains(body, `"accepted":3`) {
		t.Errorf("load = %d %s", status, body)
	}
	for _, r := range got() {
		if r.authorization != "Bearer tok" {
			t.Errorf("load request %+v", r)
		}
	}
	if status, _ := post(t, f.URL()+"/_fake/load", `{"receiver":"none","rate_per_second":1,"duration_seconds":1}`); status !=
		http.StatusBadRequest {
		t.Errorf("load to an unknown receiver = %d", status)
	}
}

func send(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type groupWebhook struct {
	webhook
	RouteLabels map[string]string `json:"routeLabels"`
	Reason      *string           `json:"notification_reason"`
}

// TestGroups is the group model of C-01.FR-13: a group and its groupKey rendered as Alertmanager does, Alerts with
// the group labels merged in, Snapshots in Alertmanager's order, cut to max_alerts, with the group's status, the
// notification_reason and identical copies.
func TestGroups(t *testing.T) {
	f := startFake(t)
	s := newSink(t, always(http.StatusAccepted))
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	f.SetClock(func() time.Time { return now })
	fam := f.URL() + "/_fake"
	if code, body := post(t, fam+"/receivers", `{"name":"lab","url":"`+s.URL+`"}`); code != http.StatusNoContent {
		t.Fatalf("receiver = %d %s", code, body)
	}
	code, body := send(t, http.MethodPut, fam+"/groups/g1",
		`{"receiver":"lab","route":"{}/{team=\"db\"}","labels":{"alertname":"DiskFull","env":"a\"b"}}`)
	if code != http.StatusOK || body != `{"group_key":"{}/{team=\"db\"}:{alertname=\"DiskFull\", env=\"a\\\"b\"}"}`+"\n" {
		t.Fatalf("group = %d %s", code, body)
	}
	for _, a := range []struct{ name, body string }{
		{"c", `{"labels":{"instance":"db-c"},"annotations":{"summary":"c"}}`},
		{"a", `{"labels":{"instance":"db-a","job":"node"},"annotations":{"summary":"a"}}`},
		{"b", `{"labels":{"instance":"db-b"},"annotations":{"summary":"b"},"starts_at":"2026-10-06T11:00:00Z"}`},
		{"d", `{"labels":{"zone":"z"},"fingerprint":"00000000000000dd"}`},
	} {
		if code, body := send(t, http.MethodPut, fam+"/groups/g1/alerts/"+a.name, a.body); code != http.StatusNoContent {
			t.Fatalf("alert %s = %d %s", a.name, code, body)
		}
	}
	code, body = post(t, fam+"/groups/g1/notify", `{"reason":"first notification","max_alerts":3,"copies":2}`)
	var res fakealertmanager.NotifyResult
	if err := json.Unmarshal([]byte(body), &res); err != nil || code != http.StatusOK || len(res.Sent) != 2 ||
		res.Sent[0] != (fakealertmanager.Sent{Status: 202, Listed: 3, Truncated: 1}) {
		t.Fatalf("notify = %d %s", code, body)
	}
	got := s.received()
	if len(got) != 2 || string(got[0].body) != string(got[1].body) {
		t.Fatalf("copies differ: %d", len(got))
	}
	var w groupWebhook
	if err := json.Unmarshal(got[0].body, &w); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, a := range w.Alerts {
		order = append(order, a.Labels["instance"])
		if a.Labels["alertname"] != "DiskFull" || a.Status != "firing" || a.EndsAt != "0001-01-01T00:00:00Z" {
			t.Errorf("alert %+v", a)
		}
	}
	if strings.Join(order, ",") != "db-a,db-b,db-c" || *w.TruncatedAlerts != 1 || w.Status != "firing" ||
		*w.Reason != "first notification" || w.RouteLabels == nil || w.CommonLabels["alertname"] != "DiskFull" ||
		w.CommonLabels["instance"] != "" || w.GroupLabels["env"] != `a"b` || w.Receiver != "lab" {
		t.Errorf("webhook %s", got[0].body)
	}
	if w.Alerts[0].StartsAt != "2026-10-06T12:00:00Z" || w.Alerts[1].StartsAt != "2026-10-06T11:00:00Z" ||
		!hex16.MatchString(w.Alerts[0].Fingerprint) || f.Fingerprint("g1", "d") != "00000000000000dd" {
		t.Errorf("times or fingerprints: %+v", w.Alerts)
	}

	// A resolve keeps startsAt and ends now; a removed Alert is no longer listed; a list picks Alerts by name.
	now = now.Add(time.Minute)
	send(t, http.MethodPut, fam+"/groups/g1/alerts/a", `{"labels":{"instance":"db-a","job":"node"},"status":"resolved"}`)
	if code, _ := send(t, http.MethodDelete, fam+"/groups/g1/alerts/c", ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	post(t, fam+"/groups/g1/notify", `{"reason":"some alerts resolved","omit_reason":true}`)
	w = groupWebhook{}
	if err := json.Unmarshal(s.received()[2].body, &w); err != nil {
		t.Fatal(err)
	}
	if len(w.Alerts) != 3 || w.Reason != nil || w.Alerts[0].Status != "resolved" ||
		w.Alerts[0].StartsAt != "2026-10-06T12:00:00Z" || w.Alerts[0].EndsAt != "2026-10-06T12:01:00Z" || w.Status != "firing" {
		t.Errorf("after the resolve: %s", s.received()[2].body)
	}
	for _, name := range []string{"b", "d"} {
		send(t, http.MethodPut, fam+"/groups/g1/alerts/"+name, `{"labels":{},"status":"resolved"}`)
	}
	post(t, fam+"/groups/g1/notify", `{"reason":"all alerts resolved","list":["c","a"]}`)
	w = groupWebhook{}
	if err := json.Unmarshal(s.received()[3].body, &w); err != nil {
		t.Fatal(err)
	}
	if len(w.Alerts) != 2 || w.Status != "resolved" || w.Alerts[0].Labels["instance"] != "db-a" {
		t.Errorf("listed: %s", s.received()[3].body)
	}
	// A new firing after a resolve starts now; "now" is accepted for both times.
	now = now.Add(time.Minute)
	send(t, http.MethodPut, fam+"/groups/g1/alerts/a", `{"labels":{"instance":"db-a"},"ends_at":"now"}`)
	key, _, snaps, err := f.Snapshots("g1", fakealertmanager.NotifyOptions{List: []string{"a"}, Status: "firing"})
	if err != nil || !strings.HasPrefix(key, "{}/") || !strings.Contains(string(snaps[0].Body), `"startsAt":"2026-10-06T12:02:00Z"`) ||
		!strings.Contains(string(snaps[0].Body), `"endsAt":"2026-10-06T12:02:00Z"`) {
		t.Errorf("new firing %s, %v", snaps[0].Body, err)
	}
}

func TestGroupErrors(t *testing.T) {
	f := startFake(t)
	fam := f.URL() + "/_fake"
	for _, tt := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPut, "/groups/g", `{"route":"{}"}`, http.StatusBadRequest},
		{http.MethodPut, "/groups/g", `{"receiver":`, http.StatusBadRequest},
		{http.MethodPut, "/groups/nope/alerts/a", `{"labels":{}}`, http.StatusBadRequest},
		{http.MethodDelete, "/groups/nope/alerts/a", ``, http.StatusNotFound},
		{http.MethodPost, "/groups/nope/notify", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/groups/nope/notify", `{`, http.StatusBadRequest},
		{http.MethodPut, "/groups/nope/alerts/a", `{`, http.StatusBadRequest},
	} {
		if code, body := send(t, tt.method, fam+tt.path, tt.body); code != tt.want {
			t.Errorf("%s %s = %d %s", tt.method, tt.path, code, body)
		}
	}
	if _, err := f.PutGroup("g", fakealertmanager.GroupSpec{Receiver: "missing"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []fakealertmanager.AlertSpec{{Status: "maybe"}, {StartsAt: "yesterday"}, {EndsAt: "x"}} {
		if err := f.PutAlert("g", "a", s); err == nil {
			t.Errorf("PutAlert(%+v) = nil", s)
		}
	}
	if err := f.RemoveAlert("g", "nope"); err == nil {
		t.Error("RemoveAlert of an unknown alert")
	}
	if _, _, _, err := f.Snapshots("g", fakealertmanager.NotifyOptions{}); err == nil {
		t.Error("Snapshots without the receiver")
	}
	if err := f.Register(fakealertmanager.Receiver{Name: "missing", URL: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.Snapshots("g", fakealertmanager.NotifyOptions{List: []string{"x"}}); err == nil {
		t.Error("Snapshots of an unknown alert")
	}
	if _, err := f.Notify(t.Context(), "g", fakealertmanager.NotifyOptions{}); err == nil {
		t.Error("Notify to a closed port")
	}
	if code, _ := post(t, fam+"/groups/g/notify", `{}`); code != http.StatusBadGateway {
		t.Errorf("notify to a closed port = %d", code)
	}
	if f.Fingerprint("nope", "a") != "" {
		t.Error("Fingerprint of an unknown group")
	}
}

// TestScenarios checks the scenario library: one scenario per fact that processing relies on, steps in time order,
// groups defined before use and every expectation naming an Alert that a step set; every notification builds; only a
// scenario with a Heartbeat signals.
func TestScenarios(t *testing.T) {
	want := []string{"F-033", "F-034", "F-035", "F-036", "F-037", "F-038", "F-039", "F-040", "F-041", "F-042", "F-043",
		"F-044", "F-045", "F-046", "F-047", "F-048", "F-049", "F-050", "F-051", "F-052", "F-053"}
	var facts []string
	for _, sc := range fakealertmanager.Scenarios() {
		facts = append(facts, sc.Fact)
		if !strings.HasPrefix(sc.Requirement, "C-06.FR-") || sc.Title == "" {
			t.Errorf("%s: requirement %q, title %q", sc.Fact, sc.Requirement, sc.Title)
		}
		f := fakealertmanager.New()
		start := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
		var now time.Time
		f.SetClock(func() time.Time { return now })
		if err := f.Register(fakealertmanager.Receiver{Name: fakealertmanager.ScenarioReceiver, URL: "http://x"}); err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		var last time.Duration
		for i, st := range sc.Steps {
			if st.At < last {
				t.Errorf("%s step %d goes back in time", sc.Fact, i)
			}
			last, now = st.At, start.Add(st.At)
			switch {
			case st.PutGroup != nil:
				if _, err := f.PutGroup(st.Group, *st.PutGroup); err != nil {
					t.Errorf("%s step %d: %v", sc.Fact, i, err)
				}
			case st.PutAlert != nil:
				if err := f.PutAlert(st.Group, st.Alert, *st.PutAlert); err != nil {
					t.Errorf("%s step %d: %v", sc.Fact, i, err)
				}
				set[st.Group+"/"+st.Alert] = true
			case st.Remove:
				if err := f.RemoveAlert(st.Group, st.Alert); err != nil {
					t.Errorf("%s step %d: %v", sc.Fact, i, err)
				}
			case st.Notify != nil:
				if _, _, _, err := f.Snapshots(st.Group, *st.Notify); err != nil {
					t.Errorf("%s step %d: %v", sc.Fact, i, err)
				}
			case st.Signal:
				if !sc.Heartbeat {
					t.Errorf("%s step %d signals without a Heartbeat", sc.Fact, i)
				}
			case st.Expect != nil:
				e := st.Expect
				var named []string
				named = append(named, e.Firing...)
				for _, m := range []map[string]string{e.Resolved} {
					for k := range m {
						named = append(named, k)
					}
				}
				for k := range e.Episodes {
					named = append(named, k)
				}
				for k := range e.StartsAt {
					named = append(named, k)
				}
				for _, n := range named {
					if !set[n] {
						t.Errorf("%s step %d names %s, which no step set", sc.Fact, i, n)
					}
				}
			default:
				t.Errorf("%s step %d does nothing", sc.Fact, i)
			}
		}
	}
	if strings.Join(facts, " ") != strings.Join(want, " ") {
		t.Errorf("facts %v, want %v", facts, want)
	}
}

// heartbeatSink stands for Muster's Heartbeat endpoint: it keeps the method, path and Authorization header of each
// signal and answers 204.
type heartbeatSink struct {
	*httptest.Server
	got chan string
}

func newHeartbeatSink(t *testing.T) *heartbeatSink {
	t.Helper()
	s := &heartbeatSink{got: make(chan string, 16)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		s.got <- r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *heartbeatSink) next(t *testing.T) string {
	t.Helper()
	select {
	case got := <-s.got:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("no signal arrived")
		return ""
	}
}

// TestHeartbeatSenders covers C-01.FR-13: a Heartbeat sender signals on request, by POST or GET, with the token as a
// bearer token or in the path; with an interval it signals at every tick; stopped, it signals no more, as a cut
// network would.
func TestHeartbeatSenders(t *testing.T) {
	f := startFake(t)
	hb := newHeartbeatSink(t)
	ticks := make(chan time.Time)
	f.SetHeartbeatTicker(func(d time.Duration) (<-chan time.Time, func()) {
		if d != 60*time.Second {
			t.Errorf("interval %v", d)
		}
		return ticks, func() {}
	})
	control := f.URL() + "/_fake/heartbeats"
	if status, body := post(t, control, `{"name":"a","url":"`+hb.URL+`/api/v1/heartbeat","token":"mstr_int_x",
		"interval_seconds":60}`); status != http.StatusNoContent {
		t.Fatalf("start = %d %s", status, body)
	}
	if status, body := post(t, control+"/a/send", ""); status != http.StatusOK || body != `{"status":204}`+"\n" {
		t.Errorf("send = %d %q", status, body)
	}
	if got := hb.next(t); got != "POST /api/v1/heartbeat Bearer mstr_int_x" {
		t.Errorf("signal %q", got)
	}
	ticks <- time.Time{}
	if got := hb.next(t); got != "POST /api/v1/heartbeat Bearer mstr_int_x" {
		t.Errorf("tick %q", got)
	}
	if status, body := post(t, control, `{"name":"b","url":"`+hb.URL+`/api/v1/heartbeat","token":"mstr_int_y",
		"token_in":"path","method":"get"}`); status != http.StatusNoContent {
		t.Fatalf("start b = %d %s", status, body)
	}
	if status, err := f.SendHeartbeat(t.Context(), "b"); err != nil || status != http.StatusNoContent {
		t.Errorf("send b = %d %v", status, err)
	}
	if got := hb.next(t); got != "GET /api/v1/heartbeat/mstr_int_y " {
		t.Errorf("signal b %q", got)
	}
	del := func(name string) int {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodDelete, control+"/"+name, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if status := del("a"); status != http.StatusNoContent {
		t.Errorf("stop = %d", status)
	}
	if status := del("a"); status != http.StatusNotFound {
		t.Errorf("stop again = %d", status)
	}
	if status, _ := post(t, control+"/a/send", ""); status != http.StatusNotFound {
		t.Errorf("send after the stop = %d", status)
	}
	if _, err := f.SendHeartbeat(t.Context(), "a"); err == nil {
		t.Error("a stopped sender signalled")
	}
	select {
	case ticks <- time.Time{}:
		t.Error("a stopped sender still ticks")
	case <-time.After(100 * time.Millisecond):
	}
	for name, body := range map[string]string{
		"remote url":    `{"name":"c","url":"http://example.org/api/v1/heartbeat"}`,
		"no name":       `{"url":"` + hb.URL + `"}`,
		"token place":   `{"name":"c","url":"` + hb.URL + `","token_in":"query"}`,
		"method":        `{"name":"c","url":"` + hb.URL + `","method":"PUT"}`,
		"interval":      `{"name":"c","url":"` + hb.URL + `","interval_seconds":-1}`,
		"unknown field": `{"name":"c","url":"` + hb.URL + `","every":1}`,
	} {
		if status, _ := post(t, control, body); status != http.StatusBadRequest {
			t.Errorf("%s = %d", name, status)
		}
	}
	// A replaced sender stops ticking, and StopHeartbeats stops every sender.
	if err := f.StartHeartbeat(t.Context(), fakealertmanager.HeartbeatSender{Name: "b", URL: hb.URL,
		IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	f.StopHeartbeats()
	if _, err := f.SendHeartbeat(t.Context(), "b"); err == nil {
		t.Error("StopHeartbeats left a sender")
	}
	hb.Close()
	if err := f.StartHeartbeat(t.Context(), fakealertmanager.HeartbeatSender{Name: "d", URL: hb.URL}); err != nil {
		t.Fatal(err)
	}
	if status, _ := post(t, control+"/d/send", ""); status != http.StatusBadGateway {
		t.Errorf("send to a closed endpoint = %d", status)
	}
}

// TestStoppedSenderTakesNoTick: a sender stopped while it is still signalling takes no tick once StopHeartbeat has
// returned. Its loop may only come back to the ticks after the stop; without waiting for it, it would then choose at
// random between the end of its context and a tick waiting to be sent.
func TestStoppedSenderTakesNoTick(t *testing.T) {
	for i := range 20 {
		f := startFake(t)
		release := make(chan struct{})
		arrived := make(chan struct{}, 1)
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		ticks := make(chan time.Time)
		f.SetHeartbeatTicker(func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} })
		if err := f.StartHeartbeat(t.Context(), fakealertmanager.HeartbeatSender{Name: "a", URL: slow.URL,
			IntervalSeconds: 60}); err != nil {
			t.Fatal(err)
		}
		ticks <- time.Time{}
		<-arrived
		stopped := make(chan bool)
		go func() { stopped <- f.StopHeartbeat("a") }()
		if !<-stopped {
			t.Fatal("the sender was not started")
		}
		close(release)
		select {
		case ticks <- time.Time{}:
			t.Fatalf("round %d: a stopped sender took a tick", i)
		case <-time.After(50 * time.Millisecond):
		}
		slow.Close()
	}
}
