// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

var testNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// loopbackAllowed is the standard policy with loopback opened, for test servers on 127.0.0.1.
func loopbackAllowed() PolicySource {
	return StaticPolicy(Policy{Mode: ModeStandard, Allowed: []Entry{{text: "127.0.0.0/8",
		network: mustPrefix("127.0.0.0/8")}}})
}

func newTestClient(t *testing.T, cfg Config) (*Client, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	if cfg.Class == "" {
		cfg.Class = ClassDelivery
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 2 * time.Second
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Policy == nil {
		cfg.Policy = loopbackAllowed()
	}
	if cfg.Logger == nil {
		cfg.Logger = logging.New(&log, logging.LevelInfo)
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.NewManual(testNow)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(c.http.CloseIdleConnections)
	return c, &log
}

// recordSleeps replaces the waits of c with a manual clock that records each wait and moves forward by it.
func recordSleeps(c *Client, m *clock.Manual) *[]time.Duration {
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		waits = append(waits, d)
		m.Advance(d)
		return nil
	}
	return &waits
}

// statusServer answers each request with the next of statuses, repeating the last.
func statusServer(t *testing.T, header http.Header, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(n.Add(1)) - 1
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(statuses[min(i, len(statuses)-1)])
		_, _ = io.WriteString(w, "provider says no")
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// scrape reads one series of the metric registry, 0 when it is absent.
func scrape(t *testing.T, series string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), series+" "); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				t.Fatal(err)
			}
			return f
		}
	}
	return 0
}

func TestDefaultMapping(t *testing.T) {
	tests := []struct {
		status   int
		hasRetry bool
		want     Outcome
	}{
		{200, false, OutcomeOK}, {204, true, OutcomeOK},
		{429, true, OutcomeRetryAfter}, {503, true, OutcomeRetryAfter},
		{408, false, OutcomeTransient}, {500, false, OutcomeTransient}, {502, false, OutcomeTransient},
		{503, false, OutcomeTransient}, {504, false, OutcomeTransient}, {500, true, OutcomeTransient},
		{400, false, OutcomeFatal}, {401, false, OutcomeFatal}, {403, false, OutcomeFatal}, {404, false, OutcomeFatal},
		{409, false, OutcomeUnknown}, {418, false, OutcomeUnknown}, {429, false, OutcomeUnknown},
		{501, false, OutcomeUnknown}, {100, false, OutcomeUnknown},
	}
	for _, tt := range tests {
		if got := DefaultMapping(tt.status, time.Second, tt.hasRetry); got != tt.want {
			t.Errorf("DefaultMapping(%d, %v) = %s, want %s", tt.status, tt.hasRetry, got, tt.want)
		}
	}
}

func TestOutcomesOfAnswers(t *testing.T) {
	tests := []struct {
		status int
		want   Outcome
	}{
		{200, OutcomeOK}, {503, OutcomeTransient}, {404, OutcomeFatal}, {418, OutcomeUnknown},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.status), func(t *testing.T) {
			srv, _ := statusServer(t, nil, tt.status)
			c, _ := newTestClient(t, Config{Class: ClassInteractive})
			res, err := c.Do(context.Background(), Request{URL: srv.URL + "/x"})
			if res.Outcome != tt.want || res.Status != tt.status || res.Attempts != 1 {
				t.Fatalf("result %+v", res)
			}
			if tt.want == OutcomeOK {
				if err != nil || string(res.Body) != "provider says no" || res.ProviderError != "" {
					t.Fatalf("ok: %v %+v", err, res)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) || e.Outcome != tt.want || e.Status != tt.status {
				t.Fatalf("error %#v", err)
			}
			want := fmt.Sprintf("GET %q: answered %d (%s)", srv.URL+"/x", tt.status, tt.want)
			if e.Error() != want || e.ProviderError != "provider says no" || res.ProviderError != e.ProviderError {
				t.Fatalf("error %q (%q), want %q", e.Error(), e.ProviderError, want)
			}
		})
	}
}

func TestRetryAfterExact(t *testing.T) {
	m := clock.NewManual(testNow)
	tests := []struct {
		name, value string
		status      int
		want        time.Duration
	}{
		{"seconds", "7", 429, 7 * time.Second},
		{"date", testNow.Add(42 * time.Second).Format(http.TimeFormat), 503, 42 * time.Second},
		{"date in the past", testNow.Add(-time.Minute).Format(http.TimeFormat), 429, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := statusServer(t, http.Header{"Retry-After": {tt.value}}, tt.status)
			c, _ := newTestClient(t, Config{Clock: m})
			res, err := c.Do(context.Background(), Request{Method: http.MethodPost, URL: srv.URL, Body: []byte("{}")})
			var e *Error
			if res.Outcome != OutcomeRetryAfter || res.RetryAfter != tt.want || !errors.As(err, &e) ||
				e.RetryAfter != tt.want {
				t.Fatalf("result %+v, err %v", res, err)
			}
		})
	}
	for _, v := range []string{"soon", "-5", "99999999999"} {
		if d, ok := retryAfter(http.Header{"Retry-After": {v}}, testNow); ok {
			t.Errorf("retryAfter(%q) = %v", v, d)
		}
	}
}

// telegramBody reads a Telegram-like body: ok false with error_code, description and parameters.retry_after.
func telegramBody(_ int, body []byte) (BodyError, bool) {
	var b struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(body, &b) != nil || b.OK {
		return BodyError{}, false
	}
	return BodyError{Code: b.ErrorCode, Text: b.Description,
		RetryAfter: time.Duration(b.Parameters.RetryAfter) * time.Second}, true
}

func TestBodyClassifier(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		want       Outcome
		retry      time.Duration
		text       Untrusted
	}{
		{"error inside a successful answer", `{"ok":false,"error_code":400,"description":"chat not found"}`, 200,
			OutcomeFatal, 0, "chat not found"},
		{"retry delay in the body", `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 5",` +
			`"parameters":{"retry_after":5}}`, 429, OutcomeRetryAfter, 5 * time.Second, "Too Many Requests: retry after 5"},
		{"retry delay inside a successful answer", `{"ok":false,"error_code":429,"parameters":{"retry_after":3}}`, 200,
			OutcomeRetryAfter, 3 * time.Second, ""},
		{"no error", `{"ok":true,"result":{}}`, 200, OutcomeOK, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			c, _ := newTestClient(t, Config{})
			res, err := c.Do(context.Background(), Request{URL: srv.URL, ClassifyBody: telegramBody})
			if res.Outcome != tt.want || res.RetryAfter != tt.retry || res.ProviderError != tt.text {
				t.Fatalf("result %+v", res)
			}
			if tt.want == OutcomeOK {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !strings.Contains(err.Error(), "the body reports error") || strings.Contains(err.Error(), string(tt.text)) && tt.text != "" {
				t.Fatalf("error %q", err)
			}
		})
	}
}

func TestCallerMapping(t *testing.T) {
	srv, _ := statusServer(t, nil, 409)
	c, _ := newTestClient(t, Config{})
	conflict := func(status int, d time.Duration, has bool) Outcome {
		if status == http.StatusConflict {
			return OutcomeTransient
		}
		return DefaultMapping(status, d, has)
	}
	res, _ := c.Do(context.Background(), Request{URL: srv.URL, Mapping: conflict})
	if res.Outcome != OutcomeTransient {
		t.Fatalf("outcome %s", res.Outcome)
	}
}

func TestBackgroundBacksOff(t *testing.T) {
	m := clock.NewManual(testNow)
	srv, n := statusServer(t, nil, 503, 502, 500, 200)
	c, _ := newTestClient(t, Config{Class: ClassBackground, Clock: m})
	waits := recordSleeps(c, m)
	c.jitter = func(d time.Duration) time.Duration { return d } // the longest wait of each step
	res, err := c.Do(context.Background(), Request{URL: srv.URL})
	if err != nil || res.Outcome != OutcomeOK || res.Attempts != 4 || n.Load() != 4 {
		t.Fatalf("result %+v, err %v", res, err)
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}; fmt.Sprint(*waits) != fmt.Sprint(want) {
		t.Fatalf("waits %v, want %v", *waits, want)
	}
	if !m.Now().Equal(testNow.Add(7 * time.Second)) {
		t.Fatalf("clock at %v", m.Now())
	}
}

func TestBackoffJitterAndCap(t *testing.T) {
	none := func(time.Duration) time.Duration { return 0 }
	full := func(d time.Duration) time.Duration { return d }
	for n, want := range map[int][2]time.Duration{
		1: {500 * time.Millisecond, time.Second}, 2: {time.Second, 2 * time.Second},
		7: {30 * time.Second, time.Minute}, 50: {30 * time.Second, time.Minute},
	} {
		if lo, hi := backoff(n, none), backoff(n, full); lo != want[0] || hi != want[1] {
			t.Errorf("backoff(%d) between %v and %v, want %v", n, lo, hi, want)
		}
	}
	for range 100 {
		if d := randomJitter(time.Second); d < 0 || d > time.Second {
			t.Fatalf("jitter %v", d)
		}
	}
	if randomJitter(0) != 0 {
		t.Fatal("jitter of nothing")
	}
}

func TestBackgroundHonoursRetryAfterAndContext(t *testing.T) {
	m := clock.NewManual(testNow)
	srv, n := statusServer(t, http.Header{"Retry-After": {"30"}}, 429)
	c, _ := newTestClient(t, Config{Class: ClassBackground, Clock: m})
	ctx, cancel := context.WithCancel(context.Background())
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) == 3 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	res, err := c.Do(ctx, Request{URL: srv.URL})
	if res.Outcome != OutcomeRetryAfter || res.Attempts != 3 || n.Load() != 3 || !errors.Is(err, context.Canceled) {
		t.Fatalf("result %+v, err %v", res, err)
	}
	if fmt.Sprint(waits) != fmt.Sprint([]time.Duration{30 * time.Second, 30 * time.Second, 30 * time.Second}) {
		t.Fatalf("waits %v", waits)
	}
}

func TestBackgroundBacksOffWithoutDelayLeft(t *testing.T) {
	m := clock.NewManual(testNow)
	srv, n := statusServer(t, http.Header{"Retry-After": {"0"}}, 503, 503, 200)
	c, _ := newTestClient(t, Config{Class: ClassBackground, Clock: m})
	waits := recordSleeps(c, m)
	c.jitter = func(time.Duration) time.Duration { return 0 }
	res, err := c.Do(context.Background(), Request{URL: srv.URL})
	if err != nil || res.Attempts != 3 || n.Load() != 3 {
		t.Fatalf("result %+v, err %v", res, err)
	}
	if fmt.Sprint(*waits) != fmt.Sprint([]time.Duration{500 * time.Millisecond, time.Second}) {
		t.Fatalf("waits %v", *waits)
	}
}

func TestSleepContext(t *testing.T) {
	if err := sleepContext(context.Background(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep %v", err)
	}
	if err := sleepContext(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep %v", err)
	}
}

func TestOneAttemptClasses(t *testing.T) {
	for _, class := range []Class{ClassDelivery, ClassInteractive, ClassHeartbeat} {
		t.Run(string(class), func(t *testing.T) {
			srv, n := statusServer(t, nil, 503)
			c, _ := newTestClient(t, Config{Class: class})
			c.sleep = func(context.Context, time.Duration) error { t.Fatal("waited for a retry"); return nil }
			res, _ := c.Do(context.Background(), Request{URL: srv.URL})
			if res.Outcome != OutcomeTransient || res.Attempts != 1 || n.Load() != 1 {
				t.Fatalf("result %+v after %d requests", res, n.Load())
			}
		})
	}
}

func TestBackgroundDoesNotRetryFinalOutcomes(t *testing.T) {
	for _, status := range []int{404, 418, 302} {
		srv, n := statusServer(t, http.Header{"Location": {"/elsewhere"}}, status)
		c, _ := newTestClient(t, Config{Class: ClassBackground})
		c.sleep = func(context.Context, time.Duration) error { t.Fatal("waited for a retry"); return nil }
		if res, err := c.Do(context.Background(), Request{URL: srv.URL}); err == nil || n.Load() != 1 {
			t.Fatalf("status %d: %+v after %d requests", status, res, n.Load())
		}
	}
}

func TestRedirectRefused(t *testing.T) {
	secret := logging.Secret("s3cr3t+/=token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/secret":
			w.Header().Set("Location", "http://user:pw@127.0.0.1:1/bot"+string(secret)+"/x")
		case "/nowhere":
		default:
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, Config{Class: ClassBackground, Secrets: []logging.Secret{secret}})
	res, err := c.Do(context.Background(), Request{URL: srv.URL + "/start"})
	var e *Error
	if res.Outcome != OutcomeRedirect || res.Attempts != 1 || !errors.As(err, &e) || e.Outcome != OutcomeRedirect ||
		err.Error() != "redirect to "+srv.URL+"/elsewhere refused" {
		t.Fatalf("result %+v, err %v", res, err)
	}
	_, err = c.Do(context.Background(), Request{URL: srv.URL + "/secret"})
	if err == nil || err.Error() != "redirect to http://127.0.0.1:1/bot[redacted]/x refused" {
		t.Fatalf("secret redirect: %v", err)
	}
	_, err = c.Do(context.Background(), Request{URL: srv.URL + "/nowhere"})
	if err == nil || !strings.Contains(err.Error(), "redirect refused: the answer 302 has no Location") {
		t.Fatalf("no location: %v", err)
	}
}

func TestBlockedByDefaultPolicy(t *testing.T) {
	srv, n := statusServer(t, nil, 200)
	c, log := newTestClient(t, Config{Class: ClassBackground,
		Policy: StaticPolicy(Policy{Mode: ModeStandard})})
	res, err := c.Do(context.Background(), Request{URL: srv.URL + "/x"})
	var e *Error
	if res.Outcome != OutcomeBlocked || res.Attempts != 1 || !errors.As(err, &e) || n.Load() != 0 {
		t.Fatalf("result %+v, err %v", res, err)
	}
	if err.Error() != "blocked by the outbound address policy: 127.0.0.1 is loopback (allow it with an allowed network)" ||
		e.Rule != "127.0.0.1 is loopback (allow it with an allowed network)" {
		t.Fatalf("error %q", err)
	}
	var line map[string]any
	if json.Unmarshal(log.Bytes(), &line) != nil || line["event"] != "outbound_blocked" || line["level"] != "WARN" ||
		line["client"] != "background" || line["rule"] != e.Rule || line["scheme"] != "http" || line["host"] != "127.0.0.1" {
		t.Fatalf("log %s", log)
	}
}

func TestNetworkErrorsRedacted(t *testing.T) {
	secret := logging.Secret("123456:AAE-s3cr3t+/=")
	c, log := newTestClient(t, Config{Secrets: []logging.Secret{secret, ""}})
	res, err := c.Do(context.Background(), Request{URL: "http://127.0.0.1:1/bot" + string(secret) + "/getMe?t=" +
		strings.ReplaceAll(string(secret), "+", "%2B")})
	if res.Outcome != OutcomeTransient || err == nil {
		t.Fatalf("result %+v, err %v", res, err)
	}
	for _, text := range []string{err.Error(), fmt.Sprintf("%+v", err), log.String()} {
		if strings.Contains(text, "s3cr3t") {
			t.Fatalf("secret in %q", text)
		}
	}
	if !strings.HasPrefix(err.Error(), `Get "http://127.0.0.1:1/bot[redacted]/getMe?t=[redacted]": dial tcp 127.0.0.1:1`) {
		t.Fatalf("error %q", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("the error wraps %v", errors.Unwrap(err))
	}
}

func TestTimeoutIsTransient(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c, _ := newTestClient(t, Config{Timeout: 50 * time.Millisecond})
	res, err := c.Do(context.Background(), Request{URL: srv.URL})
	if res.Outcome != OutcomeTransient || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("result %+v, err %v", res, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(ctx, Request{URL: srv.URL}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ended context: %v", err)
	}
}

func TestAnswerTooLarge(t *testing.T) {
	srv, _ := statusServer(t, nil, 200)
	c, _ := newTestClient(t, Config{MaxBodyBytes: 4})
	res, err := c.Do(context.Background(), Request{URL: srv.URL})
	if res.Outcome != OutcomeUnknown || err == nil || !strings.Contains(err.Error(), "larger than 4 bytes") {
		t.Fatalf("result %+v, err %v", res, err)
	}
}

func TestBaseURL(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"|"+r.Header.Get("X-Test"))
	}))
	defer srv.Close()
	c, _ := newTestClient(t, Config{BaseURL: srv.URL + "/prefix/"})
	for _, p := range []string{"bot1/getMe", "/bot1/getMe"} {
		if _, err := c.Do(context.Background(), Request{URL: p, Header: http.Header{"X-Test": {"h"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(paths) != "[/prefix/bot1/getMe|h /prefix/bot1/getMe|h]" {
		t.Fatalf("paths %v", paths)
	}
	fatal := `muster_client_requests_total{client="delivery",outcome="fatal"}`
	before := scrape(t, fatal)
	for _, u := range []string{"http://elsewhere.test/x", "//elsewhere.test/x", "%zz"} {
		res, err := c.Do(context.Background(), Request{URL: u})
		if res.Outcome != OutcomeFatal || res.Attempts != 1 || err == nil ||
			!strings.Contains(err.Error(), "below the client's base URL") {
			t.Fatalf("%s: %+v %v", u, res, err)
		}
	}
	if after := scrape(t, fatal); after != before+3 {
		t.Fatalf("fatal requests moved from %v to %v", before, after)
	}
	plain, _ := newTestClient(t, Config{})
	for _, u := range []string{"/relative", "ftp://x.test/", "%zz"} {
		if res, err := plain.Do(context.Background(), Request{URL: u}); res.Outcome != OutcomeFatal || err == nil {
			t.Fatalf("%s: %+v %v", u, res, err)
		}
	}
	if res, err := plain.Do(context.Background(), Request{Method: "BAD METHOD", URL: srv.URL}); res.Outcome != OutcomeFatal ||
		err == nil {
		t.Fatalf("bad method: %+v %v", res, err)
	}
}

// TestBaseURLPathHiddenInErrors: the path of the base URL may be a secret prefix, so errors show it as [redacted].
func TestBaseURLPathHiddenInErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/s3cret-prefix/moved" {
			w.Header().Set("Location", "/s3cret-prefix/elsewhere")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, Config{BaseURL: srv.URL + "/s3cret-prefix"})
	for path, want := range map[string]string{
		"/getMe": `GET "` + srv.URL + `/[redacted]/getMe": answered 404 (fatal)`,
		"/moved": "redirect to " + srv.URL + "/[redacted]/elsewhere refused",
	} {
		if _, err := c.Do(context.Background(), Request{URL: path}); err == nil || err.Error() != want {
			t.Fatalf("%s: %v, want %q", path, err, want)
		}
	}
	other, _ := newTestClient(t, Config{BaseURL: srv.URL + "/s3cret-prefix"})
	other.base.Path = "/"
	if got := other.display(other.base.JoinPath("x")); got != srv.URL+"/x" {
		t.Fatalf("display %q", got)
	}
}

func TestNewRefuses(t *testing.T) {
	logger := logging.New(io.Discard, logging.LevelInfo)
	ok := Config{Class: ClassDelivery, ConnectTimeout: time.Second, Timeout: time.Second, Policy: loopbackAllowed(),
		Logger: logger, Clock: clock.Real{}}
	secret := logging.Secret("t0ps3cr3t")
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"class", func(c *Config) { c.Class = "batch" }, `class "batch"`},
		{"timeout", func(c *Config) { c.Timeout = 0 }, "must be positive"},
		{"policy", func(c *Config) { c.Policy = nil }, "are required"},
		{"base URL", func(c *Config) { c.BaseURL = "mailto:x" }, "base URL: it is not an absolute"},
		{"base URL parse", func(c *Config) { c.BaseURL = "http://x.test/%zz" + string(secret) }, "base URL: it is not a URL"},
		{"base URL query", func(c *Config) { c.BaseURL = "http://x.test/?a=1" }, "query or a fragment"},
		{"proxy type", func(c *Config) { c.Proxy = &Proxy{Type: "ftp", Address: "p.test:1"} }, `proxy type "ftp"`},
		{"proxy address", func(c *Config) { c.Proxy = &Proxy{Type: ProxySOCKS5, Address: "p.test"} }, "not host:port"},
		{"proxy port", func(c *Config) { c.Proxy = &Proxy{Type: ProxyHTTP, Address: "p.test:0"} }, "no valid port"},
		{"proxy with secret", func(c *Config) {
			c.Proxy = &Proxy{Type: ProxyHTTP, Address: string(secret), Password: secret}
		}, "proxy address \"[redacted]\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ok
			tt.edit(&cfg)
			_, err := New(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), string(secret)) {
				t.Fatalf("New: %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
}

func TestProviderText(t *testing.T) {
	long := strings.Repeat("é", maxProviderText)
	got := string(providerText(long))
	if !strings.HasSuffix(got, "…") || len(got) > maxProviderText+len("…") || !strings.HasPrefix(got, "é") {
		t.Fatalf("cut %d bytes", len(got))
	}
	if providerText(" \xff bad ") != "� bad" {
		t.Fatalf("invalid UTF-8: %q", providerText(" \xff bad "))
	}
}

func TestMetricsMove(t *testing.T) {
	srv, _ := statusServer(t, nil, 200, 404)
	c, _ := newTestClient(t, Config{Class: ClassHeartbeat})
	okCount := `muster_client_requests_total{client="heartbeat",outcome="ok"}`
	fatalCount := `muster_client_requests_total{client="heartbeat",outcome="fatal"}`
	okSeconds := `muster_client_request_duration_seconds_count{client="heartbeat",outcome="ok"}`
	blocked := `muster_client_requests_total{client="heartbeat",outcome="blocked"}`
	before := []float64{scrape(t, okCount), scrape(t, fatalCount), scrape(t, okSeconds), scrape(t, blocked)}
	_, _ = c.Do(context.Background(), Request{URL: srv.URL})
	_, _ = c.Do(context.Background(), Request{URL: srv.URL})
	strict, _ := newTestClient(t, Config{Class: ClassHeartbeat, Policy: StaticPolicy(Policy{Mode: ModeStrict})})
	_, _ = strict.Do(context.Background(), Request{URL: srv.URL})
	after := []float64{scrape(t, okCount), scrape(t, fatalCount), scrape(t, okSeconds), scrape(t, blocked)}
	for i := range before {
		if after[i] != before[i]+1 {
			t.Fatalf("series %d moved from %v to %v", i, before[i], after[i])
		}
	}
}

func TestEnvironmentProxyIgnored(t *testing.T) {
	proxied := 0
	envProxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied++ }))
	defer envProxy.Close()
	t.Setenv("HTTP_PROXY", envProxy.URL)
	t.Setenv("HTTPS_PROXY", envProxy.URL)
	t.Setenv("NO_PROXY", "")
	srv, n := statusServer(t, nil, 200)
	c, _ := newTestClient(t, Config{})
	if _, err := c.Do(context.Background(), Request{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if proxied != 0 || n.Load() != 1 {
		t.Fatalf("proxied %d, direct %d", proxied, n.Load())
	}
}
