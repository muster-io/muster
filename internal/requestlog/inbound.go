// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package requestlog

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/outbound"
)

// The operations of inbound records (C-23.FR-2): a Mattermost button press, a Telegram update in the webhook mode, a
// pushed Bridge event.
const (
	OperationPress  = "press"
	OperationUpdate = "update"
	OperationPush   = "push"
)

// captureMax is the most of a request or an answer body Inbound keeps. The shape masking needs a JSON body whole, so a
// larger body, or one its handler did not read to the end, is recorded with its size and without its text.
const captureMax = 16 * BodyMax

// Links are what an inbound request concerns, as its callback tells Annotate: its Connection by internal id and
// public_id, and the Destination and the Alert Group it was matched to, 0 for none.
type Links struct {
	Connection   int64
	ConnectionID string
	Destination  int64
	AlertGroup   int64
}

type annotationKey struct{}

type annotation struct {
	mu    sync.Mutex
	links Links
}

// Annotate tells the Request log what the request of a callback wrapped by Inbound concerns: its Connection, without
// which the request is not recorded, and, once matched, its Destination and Alert Group. A zero field keeps what an
// earlier call set; outside Inbound it does nothing.
func Annotate(ctx context.Context, l Links) {
	a, ok := ctx.Value(annotationKey{}).(*annotation)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if l.Connection != 0 {
		a.links.Connection, a.links.ConnectionID = l.Connection, l.ConnectionID
	}
	if l.Destination != 0 {
		a.links.Destination = l.Destination
	}
	if l.AlertGroup != 0 {
		a.links.AlertGroup = l.AlertGroup
	}
}

func (a *annotation) get() Links {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.links
}

// Body is a body as Inbound kept it: Data is all of it when Whole, nothing otherwise; Size is its full size, the
// declared length when its handler stopped before the end.
type Body struct {
	Data  []byte
	Size  int64
	Whole bool
}

// InboundRecord is a request that reached a callback, as Inbound hands it over: what it concerns, its method, path and
// query as received, its headers and Muster's answer with the values of outbound.MaskedHeaders [redacted], both
// bodies, and the handler's time on the real clock.
type InboundRecord struct {
	Links          Links
	Operation      string
	Method         string
	URL            string
	RequestHeader  http.Header
	Request        Body
	Status         int
	ResponseHeader http.Header
	Response       Body
	Latency        time.Duration
}

// InboundRecorder keeps the records of the requests that reach the callbacks; *Writer implements it.
type InboundRecorder interface {
	RecordInbound(ctx context.Context, r InboundRecord)
}

// Inbound wraps the callback next of operation (C-23.FR-1, FR-3): it keeps the request's headers and the body as the
// handler reads it, holds Muster's answer back while the handler runs and times it on the real clock. Once the handler
// returned it sends the answer with its length and flushes it, so that the caller has all of it, and then, when the
// handler named the request's Connection with Annotate, hands the record — the values of outbound.MaskedHeaders
// [redacted] — to rec, which masks it further and writes it. A request without a Connection, for an unknown or
// deleted one, leaves no trace; a record that cannot be written leaves the answer as it was. The answer is not
// flushed, hijacked or unwrapped while the handler runs: a callback that needs to would need answerWriter to allow it.
func Inbound(rec InboundRecorder, realClock clock.Clock, operation string, next http.Handler) http.Handler {
	if rec == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := &annotation{}
		ctx := context.WithValue(r.Context(), annotationKey{}, a)
		body := &bodyTee{rc: r.Body}
		if body.rc == nil {
			body.rc = http.NoBody
		}
		r = r.WithContext(ctx)
		r.Body = body
		out := &answerWriter{w: w, header: http.Header{}}
		start := realClock.Now()
		next.ServeHTTP(out, r)
		latency := realClock.Now().Sub(start)
		out.finish()
		l := a.get()
		if l.Connection == 0 {
			return
		}
		rec.RecordInbound(ctx, InboundRecord{Links: l, Operation: operation, Method: r.Method,
			URL: r.URL.RequestURI(), RequestHeader: outbound.MaskHeaders(r.Header, unchanged),
			Request: body.kept(r.ContentLength), Status: out.status,
			ResponseHeader: outbound.MaskHeaders(out.header, unchanged), Response: out.kept(), Latency: latency})
	})
}

func unchanged(s string) string { return s }

// bodyTee is a request body that keeps what its handler reads, up to captureMax bytes, and counts all of it.
type bodyTee struct {
	rc   io.ReadCloser
	data []byte
	size int64
	over bool
	eof  bool
}

func (t *bodyTee) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	switch {
	case t.over:
	case t.size+int64(n) <= captureMax:
		t.data = append(t.data, p[:n]...)
	default:
		t.over, t.data = true, nil
	}
	t.size += int64(n)
	if errors.Is(err, io.EOF) {
		t.eof = true
	}
	return n, err
}

func (t *bodyTee) Close() error { return t.rc.Close() }

// kept is the body as its handler read it: whole when the handler read to the end, or all of the declared length, and
// it is at most captureMax bytes; otherwise without its data, sized by the larger of what was read and declared.
func (t *bodyTee) kept(declared int64) Body {
	if !t.over && (t.eof || t.size == declared) {
		return Body{Data: t.data, Size: t.size, Whole: true}
	}
	return Body{Size: max(t.size, declared)}
}

// answerWriter holds a callback's answer back until its handler returns; an answer that outgrows captureMax goes out
// as it is written (passing) and keeps only its size.
type answerWriter struct {
	w       http.ResponseWriter
	header  http.Header
	status  int
	body    []byte
	size    int64
	passing bool
}

func (a *answerWriter) Header() http.Header { return a.header }

func (a *answerWriter) WriteHeader(code int) {
	if a.status == 0 {
		a.status = code
	}
}

func (a *answerWriter) Write(p []byte) (int, error) {
	a.WriteHeader(http.StatusOK)
	a.size += int64(len(p))
	if !a.passing && int64(len(a.body)+len(p)) <= captureMax {
		a.body = append(a.body, p...)
		return len(p), nil
	}
	if !a.passing {
		a.passing = true
		a.commit(false)
		held := a.body
		a.body = nil
		if _, err := a.w.Write(held); err != nil {
			return 0, err
		}
	}
	return a.w.Write(p)
}

// commit sends the answer's status and headers; with length, its Content-Length when the status takes a body and the
// handler set none.
func (a *answerWriter) commit(length bool) {
	h := a.w.Header()
	maps.Copy(h, a.header)
	if length && bodyAllowed(a.status) && h.Get("Content-Length") == "" {
		h.Set("Content-Length", strconv.Itoa(len(a.body)))
	}
	a.w.WriteHeader(a.status)
}

// finish sends the answer the handler wrote, with its length, and flushes it: the caller has all of it before anything
// else happens.
func (a *answerWriter) finish() {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	if !a.passing {
		a.commit(true)
		if len(a.body) > 0 {
			_, _ = a.w.Write(a.body) // a caller that went away has nothing left to receive
		}
	}
	_ = http.NewResponseController(a.w).Flush() // a writer that cannot flush has written it all already
}

func (a *answerWriter) kept() Body {
	if a.passing {
		return Body{Size: a.size}
	}
	return Body{Data: a.body, Size: a.size, Whole: true}
}

// bodyAllowed reports whether an answer of status carries a body (RFC 9110): not 1xx, 204 or 304.
func bodyAllowed(status int) bool {
	return status >= http.StatusOK && status != http.StatusNoContent && status != http.StatusNotModified
}
