// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/logging"
)

// TestMaskHeaders: the values of every masked header are [redacted] whatever they hold and whatever the case of its
// name; every other name and value goes through the redactor; the input is not changed, and nil stays nil.
func TestMaskHeaders(t *testing.T) {
	if MaskHeaders(nil, strings.ToUpper) != nil {
		t.Error("MaskHeaders(nil) is not nil")
	}
	h := http.Header{"Content-Type": {"application/json"}, "X-Token": {"s3cret", "other"}}
	for i, name := range MaskedHeaders {
		h[name] = []string{"value", "second"}
		if i%2 == 0 {
			h[strings.ToLower(name)+"-x"] = []string{"kept"}
			delete(h, name)
			h[strings.ToLower(name)] = []string{"lower " + name}
		}
	}
	before := h.Clone()
	red := newRedactor([]logging.Secret{"s3cret"}).redact
	got := MaskHeaders(h, red)
	if !reflect.DeepEqual(h, before) {
		t.Errorf("the input changed: %v", h)
	}
	for name, values := range got {
		masked := false
		for _, m := range MaskedHeaders {
			masked = masked || strings.EqualFold(name, m)
		}
		for _, v := range values {
			if masked && v != "[redacted]" || strings.Contains(v, "s3cret") || strings.Contains(v, "lower") {
				t.Errorf("%s: %q", name, v)
			}
		}
	}
	if !reflect.DeepEqual(got["X-Token"], []string{"[redacted]", "other"}) ||
		!reflect.DeepEqual(got["Content-Type"], []string{"application/json"}) ||
		!reflect.DeepEqual(got["Set-Cookie"], []string{"[redacted]", "[redacted]"}) {
		t.Errorf("MaskHeaders = %v", got)
	}
	if got := MaskHeaders(http.Header{"X-s3cret": {"v"}}, red); got["X-[redacted]"] == nil {
		t.Errorf("a secret in a name: %v", got)
	}
}

// TestRecordContext: links and secrets travel in the context of a request; WithSecrets adds to the secrets already
// there without changing the parent's; an owner is set when it names a Connection or a Destination.
func TestRecordContext(t *testing.T) {
	ctx := t.Context()
	if LinksOf(ctx) != (Links{}) || secretsOf(ctx) != nil {
		t.Error("an empty context carries links or secrets")
	}
	l := Links{Destination: 7, AlertGroup: 9}
	if got := LinksOf(WithLinks(ctx, l)); got != l {
		t.Errorf("LinksOf = %+v", got)
	}
	one := WithSecrets(ctx, "a")
	two := WithSecrets(one, "b", "c")
	if got := secretsOf(two); !reflect.DeepEqual(got, []logging.Secret{"a", "b", "c"}) {
		t.Errorf("secretsOf = %v", got)
	}
	if got := secretsOf(one); !reflect.DeepEqual(got, []logging.Secret{"a"}) {
		t.Errorf("the parent's secrets = %v", got)
	}
	for _, tt := range []struct {
		o    Owner
		want bool
	}{{Owner{}, false}, {Owner{PublicID: "CN0000000000A1"}, false}, {Owner{Connection: 1}, true},
		{Owner{Destination: 2}, true}} {
		if tt.o.set() != tt.want {
			t.Errorf("%+v.set() = %v", tt.o, !tt.want)
		}
	}
}

// fakeRecorder keeps every record it gets, and the context it got it with; change, when set, changes the record it
// was handed, as no Recorder may affect the request.
type fakeRecorder struct {
	mu      sync.Mutex
	records []Record
	ctxs    []context.Context
	change  func(*Record)
}

func (f *fakeRecorder) Record(ctx context.Context, r Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := r
	kept.RequestHeader, kept.ResponseHeader = r.RequestHeader.Clone(), r.ResponseHeader.Clone()
	kept.RequestBody, kept.ResponseBody = bytes.Clone(r.RequestBody), bytes.Clone(r.ResponseBody)
	f.records = append(f.records, kept)
	f.ctxs = append(f.ctxs, ctx)
	if f.change != nil {
		f.change(&r)
	}
}

func (f *fakeRecorder) got() []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Record(nil), f.records...)
}

var testOwner = Owner{Connection: 7, PublicID: "CN0000000000A7"}

// recording is a test client with a fakeRecorder and testOwner.
func recording(t *testing.T, cfg Config) (*Client, *fakeRecorder) {
	t.Helper()
	rec := &fakeRecorder{}
	cfg.Recorder = rec
	if !cfg.Owner.set() {
		cfg.Owner = testOwner
	}
	c, _ := newTestClient(t, cfg)
	return c, rec
}

// TestRecordEachAttempt: the background class records each attempt, numbered, with its own outcome, once it has its
// outcome and before the wait for the next one (C-23.AC-2).
func TestRecordEachAttempt(t *testing.T) {
	srv, _ := statusServer(t, nil, 503, 503, 200)
	m := clock.NewManual(testNow)
	var events []string
	rec := &fakeRecorder{}
	rec.change = func(r *Record) { events = append(events, fmt.Sprintf("record %d %s", r.Attempt, r.Outcome)) }
	c, _ := newTestClient(t, Config{Class: ClassBackground, Clock: m, Recorder: rec, Owner: testOwner})
	c.sleep = func(_ context.Context, d time.Duration) error {
		events = append(events, "sleep")
		m.Advance(d)
		return nil
	}
	res, err := c.Do(t.Context(), Request{URL: srv.URL + "/poll", Operation: "getUpdates"})
	if err != nil || res.Attempts != 3 {
		t.Fatalf("result %+v, err %v", res, err)
	}
	want := "[record 1 transient sleep record 2 transient sleep record 3 ok]"
	if got := fmt.Sprint(events); got != want {
		t.Fatalf("events %s, want %s", got, want)
	}
	for i, r := range rec.got() {
		if r.Attempt != i+1 || r.Operation != "getUpdates" || r.Method != http.MethodGet || r.Owner != testOwner ||
			r.Status == 0 || string(r.ResponseBody) != "provider says no" || r.Via != ViaDirect {
			t.Errorf("record %d: %+v", i+1, r)
		}
	}
}

// TestRecordAnswers: an answer is recorded with its status, headers and body, the error class of a failure and its
// redacted text; Do's Result and error are the same with and without a Recorder, whatever the Recorder does with what
// it was handed.
func TestRecordAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Date"] = nil
		w.Header().Set("X-Answer", "a")
		switch r.URL.Path {
		case "/fail":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"broken"}`)
		case "/moved":
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusFound)
		case "/large":
			_, _ = io.WriteString(w, "0123456789")
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"p1"}`)
		}
	}))
	defer srv.Close()
	for _, tt := range []struct {
		path    string
		outcome Outcome
		status  int
		body    string
		err     string
	}{
		{"/ok", OutcomeOK, 201, `{"id":"p1"}`, ""},
		{"/fail", OutcomeTransient, 500, `{"error":"broken"}`, `POST "` + srv.URL + `/fail": answered 500 (transient)`},
		{"/moved", OutcomeRedirect, 302, "", "redirect to " + srv.URL + "/elsewhere refused"},
		{"/large", OutcomeUnknown, 200, "01234", `POST "` + srv.URL + `/large": the answer is larger than 4 bytes`},
	} {
		t.Run(tt.path, func(t *testing.T) {
			maxBody := int64(0)
			if tt.path == "/large" {
				maxBody = 4
			}
			plain, _ := newTestClient(t, Config{MaxBodyBytes: maxBody})
			c, rec := recording(t, Config{MaxBodyBytes: maxBody})
			rec.change = func(r *Record) {
				for _, b := range [][]byte{r.RequestBody, r.ResponseBody} {
					if len(b) > 0 {
						b[0] = 'X'
					}
				}
				for _, h := range []http.Header{r.RequestHeader, r.ResponseHeader} {
					for k := range h {
						h[k] = []string{"changed"}
					}
				}
			}
			req := Request{Method: http.MethodPost, URL: srv.URL + tt.path, Header: http.Header{"X-Req": {"1"}},
				Body: []byte(`{"m":1}`), Operation: "createPost"}
			wantRes, wantErr := plain.Do(t.Context(), req)
			res, err := c.Do(t.Context(), req)
			if !reflect.DeepEqual(res, wantRes) || !reflect.DeepEqual(err, wantErr) || string(req.Body) != `{"m":1}` ||
				req.Header.Get("X-Req") != "1" {
				t.Fatalf("with a Recorder %+v %#v, without %+v %#v", res, err, wantRes, wantErr)
			}
			got := rec.got()
			if len(got) != 1 {
				t.Fatalf("records %+v", got)
			}
			r := got[0]
			if r.Outcome != tt.outcome || r.Status != tt.status || string(r.ResponseBody) != tt.body ||
				r.ResponseHeader.Get("X-Answer") != "a" || r.Error != tt.err || r.Network != "" ||
				r.URL != srv.URL+tt.path || r.Method != http.MethodPost || string(r.RequestBody) != `{"m":1}` ||
				r.RequestHeader.Get("X-Req") != "1" || r.RequestHeader.Get("User-Agent") != userAgent {
				t.Fatalf("record %+v", r)
			}
		})
	}
}

// TestRecordWithoutAnswer: a request that got no answer is recorded without a status, headers or body, with what
// failed and the redacted error; one the policy blocked, as blocked; one whose caller ran out of time, too.
func TestRecordWithoutAnswer(t *testing.T) {
	secret := logging.Secret("n0-answer-s3cret")
	c, rec := recording(t, Config{Secrets: []logging.Secret{secret}})
	if _, err := c.Do(t.Context(), Request{URL: "http://127.0.0.1:1/x/" + string(secret)}); err == nil {
		t.Fatal("no error")
	}
	blocked, brec := recording(t, Config{Policy: StaticPolicy(Policy{Mode: ModeStandard})})
	if _, err := blocked.Do(t.Context(), Request{URL: "http://127.0.0.1:1/y"}); err == nil {
		t.Fatal("not blocked")
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	late, lrec := recording(t, Config{})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := late.Do(ctx, Request{URL: slow.URL}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late: %v", err)
	}
	for name, tt := range map[string]struct {
		got     []Record
		outcome Outcome
		network string
		text    string
	}{
		"refused": {rec.got(), OutcomeTransient, NetworkConnect, `Get "http://127.0.0.1:1/x/[redacted]": dial tcp`},
		"blocked": {brec.got(), OutcomeBlocked, "", "blocked by the outbound address policy"},
		"late":    {lrec.got(), OutcomeTransient, NetworkTimeout, `Get "` + slow.URL + `"`},
	} {
		if len(tt.got) != 1 {
			t.Fatalf("%s: records %+v", name, tt.got)
		}
		r := tt.got[0]
		if r.Status != 0 || r.ResponseHeader != nil || r.ResponseBody != nil || r.Outcome != tt.outcome ||
			r.Network != tt.network || !strings.HasPrefix(r.Error, tt.text) || strings.Contains(r.Error, string(secret)) ||
			r.RequestHeader.Get("User-Agent") == "" || r.Attempt != 1 {
			t.Errorf("%s: record %+v", name, r)
		}
	}
}

// TestRecordMasking: the Secrets of the client and of the request are [redacted] in the URL, the headers, both bodies
// and the error, raw and in every escaped form a URL or a JSON string gives them; the masked headers are [redacted]
// whatever they hold, the path prefix of the base URL is [redacted] and user information is left out (C-23.AC-3).
func TestRecordMasking(t *testing.T) {
	client := logging.Secret(`c&l<i"e/n+t`)
	request := logging.Secret(`r&e<q"u/e+st-c0de`)
	jsonOf := func(v any, html bool) string {
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(html)
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(b.String())
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "session=abc")
		w.Header().Set("X-Echo", string(client))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, jsonOf(map[string]string{"a": string(client), "b": string(request)}, false)+
			strings.ReplaceAll(jsonOf(string(request), true), "/", `\/`))
	}))
	defer srv.Close()
	c, rec := recording(t, Config{Secrets: []logging.Secret{client}, BaseURL: srv.URL + "/pr3fix"})
	header := http.Header{"X-Key": {"k " + string(client)}, "X-Code": {string(request)}}
	for _, name := range MaskedHeaders {
		header.Set(name, "anything at all")
	}
	q := url.Values{"c": {string(client)}, "r": {string(request)}}.Encode()
	body := jsonOf(map[string]string{"message": "your code " + string(request), "token": string(client)}, true)
	_, err := c.Do(WithSecrets(t.Context(), request), Request{Method: http.MethodPost,
		URL: "/bot" + url.PathEscape(string(client)) + "/send?" + q, Header: header, Body: []byte(body)})
	if err == nil {
		t.Fatal("no error")
	}
	got := rec.got()
	if len(got) != 1 {
		t.Fatalf("records %+v", got)
	}
	r := got[0]
	text := fmt.Sprintf("%s %v %s %v %s %s", r.URL, r.RequestHeader, r.RequestBody, r.ResponseHeader, r.ResponseBody,
		r.Error)
	for _, s := range []logging.Secret{client, request} {
		for _, form := range append([]string{string(s), url.PathEscape(string(s)), url.QueryEscape(string(s))},
			jsonForms(string(s))...) {
			if strings.Contains(text, form) {
				t.Errorf("%q in the record: %s", form, text)
			}
		}
		for _, part := range []string{"l<i", "e/n", "q\\\"u", "e<q", "\\u003c"} {
			if strings.Contains(text, part) {
				t.Errorf("%q in the record: %s", part, text)
			}
		}
	}
	if !strings.HasPrefix(r.URL, srv.URL+"/[redacted]/bot[redacted]/send?") || !strings.Contains(r.URL, "[redacted]") {
		t.Errorf("url %q", r.URL)
	}
	for _, name := range MaskedHeaders {
		if v := r.RequestHeader.Values(name); len(v) != 1 || v[0] != "[redacted]" {
			t.Errorf("%s: %q", name, v)
		}
	}
	if r.ResponseHeader.Get("Set-Cookie") != "[redacted]" || r.ResponseHeader.Get("X-Echo") != "[redacted]" ||
		r.RequestHeader.Get("X-Key") != "k [redacted]" || r.RequestHeader.Get("X-Code") != "[redacted]" ||
		r.RequestHeader.Get("User-Agent") != userAgent {
		t.Errorf("headers %v %v", r.RequestHeader, r.ResponseHeader)
	}
	var sent map[string]string
	if json.Unmarshal(r.RequestBody, &sent) != nil || sent["message"] != "your code [redacted]" ||
		sent["token"] != "[redacted]" {
		t.Errorf("request body %s", r.RequestBody)
	}
	// The request's Secrets mask only its own record.
	if _, err := c.Do(t.Context(), Request{URL: "/plain?r=" + url.QueryEscape(string(request))}); err == nil {
		t.Fatal("no error")
	}
	if last := rec.got()[1]; !strings.Contains(last.URL, string(request)) {
		t.Errorf("a request without the Secret: %q", last.URL)
	}
	user, urec := recording(t, Config{})
	if _, err := user.Do(t.Context(), Request{URL: strings.Replace(srv.URL, "http://", "http://who:pw@", 1) +
		"/u"}); err == nil {
		t.Fatal("no error")
	}
	if u := urec.got()[0].URL; u != srv.URL+"/u" {
		t.Errorf("user information: %q", u)
	}
}

// TestRedactorJSONForms: a secret is replaced in a JSON string however the encoder escaped it.
func TestRedactorJSONForms(t *testing.T) {
	secret := `a&b<c>d"e\f/g+h` + "\n"
	red := newRedactor([]logging.Secret{logging.Secret(secret)})
	plain, _ := json.Marshal(secret)
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(secret)
	for _, s := range []string{string(plain), b.String(), strings.ReplaceAll(string(plain), "/", `\/`)} {
		if got := red.redact(s); strings.TrimSpace(got) != `"[redacted]"` {
			t.Errorf("redact(%s) = %s", s, got)
		}
	}
	if forms := jsonForms("plain"); fmt.Sprint(forms) != "[plain plain plain plain]" {
		t.Errorf("jsonForms(plain) = %v", forms)
	}
}

// TestRecordSkipsAndLinks: an answer that SkipRecord leaves out is not recorded, while a failed attempt is recorded
// without asking it; a client without a Recorder or an Owner records nothing; an attempt whose caller went away is
// not recorded; one that sent nothing neither. The links of the context, the proxy path and the latency on the real
// clock reach the record.
func TestRecordSkipsAndLinks(t *testing.T) {
	m := clock.NewManual(testNow)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.Advance(1500 * time.Millisecond)
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusBadRequest)
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
	}))
	defer srv.Close()
	asked := 0
	skip := func(status int, body []byte) bool {
		asked++
		return status == http.StatusOK && string(body) == `{"ok":true,"result":[]}`
	}
	c, rec := recording(t, Config{Clock: m})
	if _, err := c.Do(t.Context(), Request{URL: srv.URL + "/poll", SkipRecord: skip}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(t.Context(), Request{URL: srv.URL + "/fail", SkipRecord: skip}); err == nil {
		t.Fatal("no error")
	}
	if got := rec.got(); asked != 1 || len(got) != 1 || got[0].Status != http.StatusBadRequest {
		t.Fatalf("asked %d, records %+v", asked, got)
	}
	ctx := WithLinks(t.Context(), Links{Destination: 3, AlertGroup: 4})
	if _, err := c.Do(ctx, Request{URL: srv.URL + "/linked"}); err != nil {
		t.Fatal(err)
	}
	if r := rec.got()[1]; r.Links != (Links{Destination: 3, AlertGroup: 4}) || r.Latency != 1500*time.Millisecond ||
		rec.ctxs[1] != ctx {
		t.Errorf("record %+v", r)
	}
	if _, err := c.Do(t.Context(), Request{Method: "BAD METHOD", URL: srv.URL}); err == nil {
		t.Fatal("a bad method was sent")
	}
	if _, err := c.Do(t.Context(), Request{URL: "%zz"}); err == nil {
		t.Fatal("a bad URL was sent")
	}
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Do(ended, Request{URL: srv.URL + "/gone"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ended: %v", err)
	}
	if got := rec.got(); len(got) != 2 {
		t.Fatalf("records %+v", got)
	}
	for _, cfg := range []Config{{Recorder: nil, Owner: testOwner}, {Recorder: &fakeRecorder{}}} {
		n, _ := newTestClient(t, cfg)
		if _, err := n.Do(t.Context(), Request{URL: srv.URL + "/x"}); err != nil {
			t.Fatal(err)
		}
		if f, ok := cfg.Recorder.(*fakeRecorder); ok && len(f.got()) != 0 {
			t.Fatalf("a client without an Owner recorded %+v", f.got())
		}
	}
	p := startProxy(t, fakeproxy.HTTP, fakeproxy.Options{})
	proxied, prec := recording(t, Config{Proxy: &Proxy{Type: ProxyHTTP, Address: p.Addr()}})
	if _, err := proxied.Do(t.Context(), Request{URL: srv.URL + "/via"}); err != nil {
		t.Fatal(err)
	}
	if got := prec.got(); len(got) != 1 || got[0].Via != ViaProxy {
		t.Fatalf("records %+v", got)
	}
}

// TestRecordBasePrefix: a base URL's path prefix of at least minSecretPrefix characters is [redacted] wherever a record
// holds it — in Location and Content-Location, shown resolved as the URL is, and in a body that echoes the path — while
// a shorter one, a route such as /v1, is left alone in bodies; Location shows any prefix as the URL does. Do's result
// and error are those of a client without a Recorder.
func TestRecordBasePrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Date"] = nil
		prefix, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		switch {
		case strings.HasSuffix(r.URL.Path, "/moved"):
			w.Header().Set("Location", "/"+prefix+"/elsewhere")
			w.Header().Add("Location", "sibling")
			w.Header().Set("Content-Location", "%zz /"+prefix+"/raw")
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "no route "+r.URL.Path) //nolint:gosec // G705: a test server that echoes the path on purpose
		}
	}))
	defer srv.Close()
	const long = "s3cret-pr3fix-0123456789"
	for _, tt := range []struct {
		prefix, body, raw string
	}{
		{long, "no route /[redacted]/missing", "%zz /[redacted]/raw"},
		{"v1", "no route /v1/missing", "%zz /v1/raw"},
	} {
		t.Run(tt.prefix, func(t *testing.T) {
			cfg := Config{BaseURL: srv.URL + "/" + tt.prefix + "/"}
			plain, _ := newTestClient(t, cfg)
			c, rec := recording(t, cfg)
			for _, path := range []string{"/moved", "/missing"} {
				wantRes, wantErr := plain.Do(t.Context(), Request{URL: path})
				res, err := c.Do(t.Context(), Request{URL: path})
				if err == nil || !reflect.DeepEqual(res, wantRes) || !reflect.DeepEqual(err, wantErr) {
					t.Fatalf("%s: with a Recorder %+v %v, without %+v %v", path, res, err, wantRes, wantErr)
				}
			}
			got := rec.got()
			if len(got) != 2 {
				t.Fatalf("records %+v", got)
			}
			moved, missing := got[0], got[1]
			if loc := moved.ResponseHeader.Values("Location"); !reflect.DeepEqual(loc,
				[]string{srv.URL + "/[redacted]/elsewhere", srv.URL + "/[redacted]/sibling"}) {
				t.Errorf("Location %q", loc)
			}
			if cl := moved.ResponseHeader.Get("Content-Location"); cl != tt.raw {
				t.Errorf("Content-Location %q", cl)
			}
			if moved.URL != srv.URL+"/[redacted]/moved" || missing.URL != srv.URL+"/[redacted]/missing" ||
				moved.Error != "redirect to "+srv.URL+"/[redacted]/elsewhere refused" {
				t.Errorf("urls %q %q, error %q", moved.URL, missing.URL, moved.Error)
			}
			if string(missing.ResponseBody) != tt.body {
				t.Errorf("body %q", missing.ResponseBody)
			}
			if tt.prefix == long {
				for _, r := range got {
					if text := fmt.Sprintf("%s %v %s %v %s %s", r.URL, r.RequestHeader, r.RequestBody,
						r.ResponseHeader, r.ResponseBody, r.Error); strings.Contains(text, long) {
						t.Errorf("the prefix in the record: %s", text)
					}
				}
			}
		})
	}
}

// TestAnswerHeaderBound: an answer whose header is larger than maxResponseHeaderBytes fails as a transport error and
// its record has no answer; one just below the bound is read.
func TestAnswerHeaderBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := maxResponseHeaderBytes - 4<<10
		if r.URL.Path == "/large" {
			size = maxResponseHeaderBytes + 1
		}
		w.Header().Set("X-Big", strings.Repeat("h", size))
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	c, rec := recording(t, Config{})
	if res, err := c.Do(t.Context(), Request{URL: srv.URL + "/small"}); err != nil || string(res.Body) != "ok" {
		t.Fatalf("below the bound: %+v %v", res, err)
	}
	res, err := c.Do(t.Context(), Request{URL: srv.URL + "/large"})
	e, ok := errors.AsType[*Error](err)
	if !ok || res.Outcome != OutcomeTransient || res.Status != 0 || e.Network != NetworkConnect ||
		!strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("above the bound: %+v %v", res, err)
	}
	got := rec.got()
	if len(got) != 2 || got[0].Status != http.StatusOK || len(got[0].ResponseHeader.Get("X-Big")) == 0 ||
		got[1].Status != 0 || got[1].ResponseHeader != nil || got[1].Outcome != OutcomeTransient {
		t.Fatalf("records %+v", got)
	}
}
