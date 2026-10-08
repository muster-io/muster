// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package fakeserver is the HTTP harness the fake servers are built on: it records every request with the status it
// was answered with, answers scripted faults and serves the control endpoints under /_fake/. The same servers run in Go
// tests and in `muster dev`.
package fakeserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// ControlPrefix starts the paths of the control endpoints; requests below it are neither recorded nor faulted.
	ControlPrefix = "/_fake/"
	// MaxRequests is how many requests a server keeps; the oldest are dropped first.
	MaxRequests = 10_000
	// MaxBodyBytes is how much of a request body is recorded; the handler still receives the whole body.
	MaxBodyBytes = 1 << 20

	maxControlBody = 1 << 20
)

const (
	EncodingUTF8   = "utf8"
	EncodingBase64 = "base64"
)

// Request is a recorded request as GET /_fake/requests lists it.
type Request struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query"`
	Headers map[string][]string `json:"headers"`
	// Body is the body as text, or in base64 when BodyEncoding says so because it is not valid UTF-8.
	Body         string `json:"body"`
	BodyEncoding string `json:"body_encoding"`
	AtMs         int64  `json:"at_ms"`
	// Status is the status the request was answered with, filled in once the answer is written; it is 0 while the
	// request is served and when no answer was written: the client gave up during a fault's delay, or the handler
	// aborted.
	Status int `json:"status"`
}

// Fault is a scripted answer for the requests to a path. With a Status, a fault waits DelayMs and answers that status
// with Body, as ContentType when it is set and otherwise as JSON when Body is valid JSON and as text when it is not.
// Without a Status, the fake answers as usual, but the answer is held for DelayMs after the fake made it: what the
// request changed is done even when the client gives up waiting.
//
// Path is an exact path or a path.Match pattern, in which `*` matches any run of characters within one segment (`?`
// and `[...]` work as there too). An
// exact path wins over patterns, and among patterns the one added first wins. Times is how many requests the fault
// hits before it is removed; 0 is every request until the faults are reset.
type Fault struct {
	Path              string `json:"path"`
	Status            int    `json:"status"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	DelayMs           int    `json:"delay_ms"`
	Body              string `json:"body"`
	ContentType       string `json:"content_type"`
	Times             int    `json:"times"`
}

func (f Fault) Validate() error {
	switch {
	case f.Path == "":
		return errors.New("path is required")
	case !strings.HasPrefix(f.Path, "/"):
		return errors.New("path must start with /")
	case !validPattern(f.Path):
		return errors.New("path is not a valid pattern")
	case f.Path+"/" == ControlPrefix || strings.HasPrefix(f.Path, ControlPrefix):
		return fmt.Errorf("path must not be under %s", ControlPrefix)
	// net/http treats 1xx statuses as informational and answers 200 after them, so they cannot be scripted.
	case f.Status != 0 && (f.Status < 200 || f.Status > 599):
		return errors.New("status must be 0 or between 200 and 599")
	case f.RetryAfterSeconds < 0:
		return errors.New("retry_after_seconds must not be negative")
	case f.DelayMs < 0:
		return errors.New("delay_ms must not be negative")
	case f.Times < 0:
		return errors.New("times must not be negative")
	case f.Status == 0 && f.DelayMs == 0:
		return errors.New("a fault needs a status or a delay_ms")
	case f.Status == 0 && (f.RetryAfterSeconds != 0 || f.Body != "" || f.ContentType != ""):
		return errors.New("retry_after_seconds, body and content_type need a status")
	}
	return nil
}

func validPattern(p string) bool {
	_, err := path.Match(p, "")
	return err == nil
}

// matches reports whether the fault applies to the request path p.
func (f Fault) matches(p string) bool {
	ok, _ := path.Match(f.Path, p)
	return ok
}

// fault is a scripted fault with the number of hits it has left; left is 0 for a fault without Times.
type fault struct {
	Fault
	left int
}

// Server wraps a fake's own handler with recording, faults and the control endpoints.
type Server struct {
	name    string
	handler http.Handler
	control *http.ServeMux

	mu sync.Mutex
	// requests are pointers, so that the status of an answer can be filled in after the request was recorded; a request
	// dropped meanwhile is simply not listed.
	requests []*Request
	// first is the index of the oldest request once the record is full and works as a ring.
	first int
	// faults are in the order they were added, which decides between patterns.
	faults []*fault

	srv    *http.Server
	ln     net.Listener
	served chan error
	// stop cancels the base context of every request with errClosed.
	stop   context.CancelCauseFunc
	closed bool
}

var errClosed = errors.New("fake server closed")

// New returns a server named after the system it fakes, such as "Mattermost", that answers with h.
func New(name string, h http.Handler) *Server {
	s := &Server{
		name:    name,
		handler: h,
		control: http.NewServeMux(),
	}
	s.control.HandleFunc("GET /_fake/requests", s.listRequests)
	s.control.HandleFunc("DELETE /_fake/requests", func(w http.ResponseWriter, _ *http.Request) {
		s.ResetRequests()
		w.WriteHeader(http.StatusNoContent)
	})
	s.control.HandleFunc("POST /_fake/faults", s.addFault)
	s.control.HandleFunc("DELETE /_fake/faults", func(w http.ResponseWriter, _ *http.Request) {
		s.ResetFaults()
		w.WriteHeader(http.StatusNoContent)
	})
	return s
}

func (s *Server) Name() string { return s.name }

// HandleControl adds a control endpoint for a ServeMux pattern whose path is under /_fake/, such as
// "POST /_fake/send". A pattern outside it could never be reached, so it panics like ServeMux does on a bad pattern.
func (s *Server) HandleControl(pattern string, h http.HandlerFunc) {
	path := pattern
	if _, p, ok := strings.Cut(pattern, " "); ok {
		path = strings.TrimLeft(p, " ")
	}
	if !strings.HasPrefix(path, ControlPrefix) {
		panic(fmt.Sprintf("fakeserver: control pattern %q is not under %s", pattern, ControlPrefix))
	}
	s.control.HandleFunc(pattern, h)
}

// Start listens on addr, such as 127.0.0.1:0 in tests, and serves in the background until Close. The context bounds
// the start; requests get a context that Close cancels.
func (s *Server) Start(ctx context.Context, addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil || s.closed {
		return fmt.Errorf("fake %s: already started", s.name)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	base, stop := context.WithCancelCause(context.WithoutCancel(ctx))
	s.ln, s.stop = ln, stop
	s.srv = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	s.served = make(chan error, 1)
	go func() { s.served <- s.srv.Serve(ln) }()
	return nil
}

// URL is the base URL, such as http://127.0.0.1:18065, or "" before Start.
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return "http://" + s.ln.Addr().String()
}

// Close cancels the context of every request, so running handlers stop and held delays abort their responses, and
// shuts the server down gracefully; when ctx ends first, it closes every connection.
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	srv, served, stop := s.srv, s.served, s.stop
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	stop(errClosed)
	err := srv.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, srv.Close())
	}
	if serveErr := <-served; !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	if err != nil {
		return fmt.Errorf("fake %s: close: %w", s.name, err)
	}
	return nil
}

// Requests returns the recorded requests in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, 0, len(s.requests))
	for _, r := range s.requests[s.first:] {
		out = append(out, *r)
	}
	for _, r := range s.requests[:s.first] {
		out = append(out, *r)
	}
	return out
}

func (s *Server) ResetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests, s.first = nil, 0
}

// SetFault adds a fault, replacing an earlier one for the same path in its place.
func (s *Server) SetFault(f Fault) error {
	if err := f.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nf := &fault{Fault: f, left: f.Times}
	for i, old := range s.faults {
		if old.Path == f.Path {
			s.faults[i] = nf
			return nil
		}
	}
	s.faults = append(s.faults, nf)
	return nil
}

func (s *Server) ResetFaults() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = nil
}

// takeFault returns the fault for the request path p and counts the hit, removing the fault after its last one.
func (s *Server) takeFault(p string) (Fault, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.faults, func(f *fault) bool { return f.Path == p })
	if i < 0 {
		i = slices.IndexFunc(s.faults, func(f *fault) bool { return f.matches(p) })
	}
	if i < 0 {
		return Fault{}, false
	}
	f := s.faults[i]
	if f.Times > 0 {
		f.left--
		if f.left == 0 {
			s.faults = slices.Delete(s.faults, i, i+1)
		}
	}
	return f.Fault, true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path+"/" == ControlPrefix || strings.HasPrefix(r.URL.Path, ControlPrefix) {
		if msg := refuseControl(r); msg != "" {
			WriteError(w, http.StatusForbidden, msg)
			return
		}
		s.control.ServeHTTP(w, r)
		return
	}
	req := s.record(r)
	sw := &statusWriter{ResponseWriter: w}
	// A handler that aborts with a panic skips this, and its request keeps the status 0.
	if s.serve(sw, r) {
		s.mu.Lock()
		req.Status = sw.answered()
		s.mu.Unlock()
	}
}

// serve answers r and reports whether an answer was written.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) bool {
	f, faulted := s.takeFault(r.URL.Path)
	if !faulted {
		s.handler.ServeHTTP(w, r)
		return true
	}
	delay := time.Duration(f.DelayMs) * time.Millisecond
	if f.Status != 0 {
		if !wait(r.Context(), delay) {
			return false
		}
		writeFault(w, f)
		return true
	}
	held := &heldAnswer{header: http.Header{}}
	s.handler.ServeHTTP(held, r)
	if !wait(r.Context(), delay) {
		return false
	}
	held.writeTo(w)
	return true
}

// statusWriter notes the status a handler answers with.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// answered is the status of the answer; net/http answers 200 for a handler that wrote nothing.
func (w *statusWriter) answered() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// heldAnswer keeps a handler's answer until a fault's delay has passed.
type heldAnswer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (h *heldAnswer) Header() http.Header { return h.header }

func (h *heldAnswer) WriteHeader(status int) {
	if h.status == 0 && status >= 200 {
		h.status = status
	}
}

func (h *heldAnswer) Write(b []byte) (int, error) {
	if h.status == 0 {
		h.status = http.StatusOK
	}
	return h.body.Write(b)
}

func (h *heldAnswer) writeTo(w http.ResponseWriter) {
	maps.Copy(w.Header(), h.header)
	if h.status == 0 {
		h.status = http.StatusOK
	}
	w.WriteHeader(h.status)
	_, _ = w.Write(h.body.Bytes())
}

// refuseControl keeps browsers away from the control endpoints: a web page can reach loopback, but its requests carry
// an Origin header or, after DNS rebinding, a foreign Host.
func refuseControl(r *http.Request) string {
	if _, ok := r.Header["Origin"]; ok {
		return "the control endpoints refuse requests with an Origin header"
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost") {
		return ""
	}
	return "the control endpoints answer only requests for the host 127.0.0.1, localhost or ::1"
}

// wait reports whether the delay passed; a client that gives up ends it early, and Close aborts the response.
func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), errClosed) {
			panic(http.ErrAbortHandler)
		}
		return false
	}
}

func (s *Server) record(r *http.Request) *Request {
	// A read error shows again to the handler when it reads past the recorded part.
	body, _ := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes))
	r.Body = readCloser{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
	req := &Request{
		Method:       r.Method,
		Path:         r.URL.Path,
		Query:        r.URL.RawQuery,
		Headers:      map[string][]string(r.Header.Clone()),
		Body:         string(body),
		BodyEncoding: EncodingUTF8,
		AtMs:         time.Now().UnixMilli(),
	}
	if req.Headers == nil {
		req.Headers = map[string][]string{}
	}
	if !utf8.Valid(body) {
		req.Body, req.BodyEncoding = base64.StdEncoding.EncodeToString(body), EncodingBase64
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) < MaxRequests {
		s.requests = append(s.requests, req)
		return req
	}
	s.requests[s.first] = req
	s.first = (s.first + 1) % MaxRequests
	return req
}

type readCloser struct {
	io.Reader
	io.Closer
}

func writeFault(w http.ResponseWriter, f Fault) {
	if f.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(f.RetryAfterSeconds))
	}
	if f.Status == http.StatusNoContent || f.Status == http.StatusNotModified {
		w.WriteHeader(f.Status)
		return
	}
	body, contentType := f.Body, "text/plain; charset=utf-8"
	switch {
	case body == "":
		body = http.StatusText(f.Status)
	case json.Valid([]byte(body)):
		contentType = "application/json"
	}
	if f.ContentType != "" {
		contentType = f.ContentType
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(f.Status)
	_, _ = io.WriteString(w, body)
}

func (s *Server) listRequests(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, s.Requests())
}

func (s *Server) addFault(w http.ResponseWriter, r *http.Request) {
	var f Fault
	if err := DecodeJSON(w, r, &f); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.SetFault(f); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DecodeJSON reads a control request's body as exactly one JSON value into v, rejecting unknown fields.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body: more than one JSON value")
	}
	return nil
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError answers a control request with {"error": msg}.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}
