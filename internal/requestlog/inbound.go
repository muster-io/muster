// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package requestlog

import (
	"cmp"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/outbound"
)

// The operations of inbound records (C-23.FR-2): a Mattermost button press, a Telegram update in the webhook mode, a
// pushed Bridge event, and a Slack interaction — a press in http mode, and the envelope of the type interactive in
// Socket Mode, whose record takes the operation of its type as every envelope's does (C-24.FR-14).
const (
	OperationPress       = "press"
	OperationUpdate      = "update"
	OperationPush        = "push"
	OperationInteractive = "interactive"
)

// MethodWebSocket is the method of the record of an envelope received over a WebSocket, which has no HTTP method.
const MethodWebSocket = "WEBSOCKET"

// operationEnvelope is the operation of the record of an envelope without a type.
const operationEnvelope = "envelope"

// maxOperation is the longest operation a record stores.
const maxOperation = 64

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

// EnvelopeRecord is an envelope that reached a Connection over a WebSocket and was confirmed on it — an envelope of
// Slack's Socket Mode (C-24.FR-14, C-23.FR-1): its Connection, its type as the operation, the socket's address without
// its query, the envelope as received, Muster's confirmation, the path of the socket — outbound.ViaDirect or
// outbound.ViaProxy — and the time from its receipt to the confirmation on the real clock.
type EnvelopeRecord struct {
	Links        Links
	Operation    string
	URL          string
	Envelope     []byte
	Confirmation []byte
	Via          string
	Latency      time.Duration
}

// EnvelopeRecorder keeps the records of envelopes; *Writer implements it.
type EnvelopeRecorder interface {
	RecordEnvelope(ctx context.Context, r EnvelopeRecord)
}

var _ EnvelopeRecorder = (*Writer)(nil)

// RecordEnvelope writes the record of an envelope (C-23.FR-1, FR-2, C-24.FR-14) as RecordInbound writes the record of
// a callback's request: the direction inbound, the attempt 1, no error; the method WEBSOCKET, no request headers, the
// envelope as the request body and the confirmation as the answer's body without a status or answer headers,
// shape-masked and cut as every record. Its operation is the envelope's type, sanitized and cut to what a record
// stores, envelope when it has none. Like Record it fails nothing; a nil Writer records nothing.
func (w *Writer) RecordEnvelope(ctx context.Context, r EnvelopeRecord) {
	if w == nil {
		return
	}
	op := cmp.Or(cutAt(sanitize(r.Operation), maxOperation), operationEnvelope)
	p := w.paramsOf(DirectionInbound, outbound.Record{
		Owner:     outbound.Owner{Connection: r.Links.Connection, PublicID: r.Links.ConnectionID},
		Links:     outbound.Links{Destination: r.Links.Destination, AlertGroup: r.Links.AlertGroup},
		Operation: op, Method: MethodWebSocket, URL: r.URL, RequestBody: r.Envelope, Latency: r.Latency,
		Via: cmp.Or(r.Via, outbound.ViaDirect), Attempt: 1})
	body, size, cut := textOf(r.Confirmation)
	p.ResponseBody = pgtype.Text{String: body, Valid: true}
	p.ResponseBodyBytes, p.ResponseBodyTruncated = size, cut
	w.insert(ctx, DirectionInbound, op, r.Links.ConnectionID, p)
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
			ResponseHeader: outbound.MaskHeaders(out.sent, unchanged), Response: out.kept(), Latency: latency})
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
// as it is written (passing) and keeps only its size. As with net/http, the first final status counts, and the headers
// are those set before it: sent is header as it was then.
type answerWriter struct {
	w       http.ResponseWriter
	header  http.Header
	sent    http.Header
	status  int
	body    []byte
	size    int64
	passing bool
}

func (a *answerWriter) Header() http.Header { return a.header }

// WriteHeader takes the first final status and the headers set so far; an informational one (1xx) is not sent, since
// the answer is held back.
func (a *answerWriter) WriteHeader(code int) {
	if a.status != 0 || code >= 100 && code < 200 {
		return
	}
	a.status, a.sent = code, a.header.Clone()
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
	maps.Copy(h, a.sent)
	if length && bodyAllowed(a.status) && h.Get("Content-Length") == "" {
		h.Set("Content-Length", strconv.Itoa(len(a.body)))
	}
	a.w.WriteHeader(a.status)
}

// finish sends the answer the handler wrote, with its length, and flushes it: the caller has all of it before anything
// else happens.
func (a *answerWriter) finish() {
	a.WriteHeader(http.StatusOK)
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
