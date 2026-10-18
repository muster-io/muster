// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package requestlog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/requestlog/dbgen"
)

// captureRecorder keeps the records Inbound hands over. With started and release, it says that a write began and
// waits until it may end; done is closed once a record is kept.
type captureRecorder struct {
	started, release, done chan struct{}

	mu      sync.Mutex
	records []InboundRecord
}

func (c *captureRecorder) RecordInbound(_ context.Context, r InboundRecord) {
	if c.started != nil {
		close(c.started)
	}
	if c.release != nil {
		<-c.release
	}
	c.mu.Lock()
	c.records = append(c.records, r)
	c.mu.Unlock()
	if c.done != nil {
		close(c.done)
	}
}

func (c *captureRecorder) got() []InboundRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]InboundRecord(nil), c.records...)
}

const inboundConn = "CN0000000000A7"

// annotated is a handler that names the Connection 7 and answers 200 with body.
func annotated(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		Annotate(r.Context(), Links{Connection: 7, ConnectionID: inboundConn})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func serve(t *testing.T, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestInboundRecords covers C-23.FR-1 and FR-2 at the middleware: the record of a request whose handler named its
// Connection has the operation, the method, the path and query as received, the links of every Annotate merged, the
// headers with the values of the masked ones [redacted], the body as the handler read it, Muster's answer and the
// handler's time on the real clock; the handler reads the body and the caller gets the answer unchanged, with its
// length, flushed.
func TestInboundRecords(t *testing.T) {
	realClock := clock.NewManual(t0)
	rec := &captureRecorder{}
	var read string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		read = string(b)
		Annotate(r.Context(), Links{Connection: 7, ConnectionID: inboundConn})
		Annotate(r.Context(), Links{Destination: 3, AlertGroup: 9})
		Annotate(r.Context(), Links{})
		realClock.Advance(25 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=s3cret-e")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":`))
		_, _ = w.Write([]byte(`true}`))
		w.WriteHeader(http.StatusTeapot)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/callbacks/x/"+inboundConn+"?q=1",
		strings.NewReader(`{"a":1}`))
	for name, value := range map[string]string{"Authorization": "Bearer s3cret-a",
		"X-Telegram-Bot-Api-Secret-Token": "s3cret-b", "Webhook-Signature": "v1,s3cret-c", "Cookie": "s=s3cret-d",
		"X-Other": "kept"} {
		req.Header.Set(name, value)
	}
	w := serve(t, Inbound(rec, realClock, OperationPush, h), req)
	if read != `{"a":1}` || w.Code != http.StatusCreated || w.Body.String() != `{"ok":true}` ||
		w.Header().Get("Content-Length") != "11" || w.Header().Get("Content-Type") != "application/json" ||
		w.Header().Get("Set-Cookie") != "session=s3cret-e" || !w.Flushed {
		t.Fatalf("read %q; answered %d %v %q, flushed %v", read, w.Code, w.Header(), w.Body, w.Flushed)
	}
	got := rec.got()
	if len(got) != 1 {
		t.Fatalf("%d records", len(got))
	}
	r := got[0]
	if r.Operation != "push" || r.Method != http.MethodPost || r.URL != "/api/v1/callbacks/x/"+inboundConn+"?q=1" ||
		r.Links != (Links{Connection: 7, ConnectionID: inboundConn, Destination: 3, AlertGroup: 9}) ||
		string(r.Request.Data) != `{"a":1}` || r.Request.Size != 7 || !r.Request.Whole || r.Status != http.StatusCreated ||
		string(r.Response.Data) != `{"ok":true}` || r.Response.Size != 11 || !r.Response.Whole ||
		r.Latency != 25*time.Millisecond {
		t.Errorf("record %+v", r)
	}
	for _, name := range []string{"Authorization", "X-Telegram-Bot-Api-Secret-Token", "Webhook-Signature", "Cookie"} {
		if v := r.RequestHeader[name]; len(v) != 1 || v[0] != redacted {
			t.Errorf("%s: %v", name, v)
		}
	}
	if r.RequestHeader.Get("X-Other") != "kept" || r.ResponseHeader.Get("Set-Cookie") != redacted ||
		r.ResponseHeader.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v, answer %v", r.RequestHeader, r.ResponseHeader)
	}
}

// handlerFunc is a handler that can be compared.
type handlerFunc struct{ f http.HandlerFunc }

func (h *handlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.f(w, r) }

// TestInboundWithoutAnnotation covers the other half of C-23.FR-1: a request whose handler named no Connection — one
// for an unknown or deleted Connection — leaves no record and gets its answer unchanged; Annotate outside Inbound does
// nothing, and Inbound without a recorder is the handler itself.
func TestInboundWithoutAnnotation(t *testing.T) {
	rec := &captureRecorder{}
	h := &handlerFunc{f: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("no"))
	}}
	w := serve(t, Inbound(rec, clock.NewManual(t0), OperationUpdate, h),
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", strings.NewReader("{}")))
	if w.Code != http.StatusUnauthorized || w.Body.String() != "no" || len(rec.got()) != 0 {
		t.Errorf("answered %d %q, %d records", w.Code, w.Body, len(rec.got()))
	}
	Annotate(t.Context(), Links{Connection: 7})
	if got := Inbound(nil, clock.NewManual(t0), OperationUpdate, h); got != http.Handler(h) {
		t.Errorf("Inbound without a recorder is %T", got)
	}
}

// TestInboundBodies covers the bodies of an inbound record (C-23.FR-2): a request body is kept whole when its handler
// read it to the end or all of its declared length, and otherwise only its size — the declared one when the handler
// stopped early — as is one past captureMax; an answer past captureMax goes out as it is written and keeps only its
// size; an answer without a body has a length of 0, one whose status takes none has no length.
func TestInboundBodies(t *testing.T) {
	big := strings.Repeat("b", captureMax+1)
	for _, tt := range []struct {
		name    string
		body    string
		chunked bool
		handle  func(w http.ResponseWriter, r *http.Request)
		req     Body
		status  int
		answer  string
		length  string
		resp    Body
	}{
		{name: "unread", body: strings.Repeat("u", 50), handle: func(http.ResponseWriter, *http.Request) {},
			req: Body{Size: 50}, status: 200, length: "0", resp: Body{Whole: true}},
		{name: "read to the end", body: "whole", handle: func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
		}, req: Body{Data: []byte("whole"), Size: 5, Whole: true}, status: 200, length: "0", resp: Body{Whole: true}},
		{name: "all of the declared length", body: "exact", handle: func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadFull(r.Body, make([]byte, 5))
		}, req: Body{Data: []byte("exact"), Size: 5, Whole: true}, status: 200, length: "0", resp: Body{Whole: true}},
		{name: "read in part", body: strings.Repeat("p", 50), handle: func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(io.LimitReader(r.Body, 10))
		}, req: Body{Size: 50}, status: 200, length: "0", resp: Body{Whole: true}},
		{name: "past captureMax", body: big, handle: func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
		}, req: Body{Size: captureMax + 1}, status: 200, length: "0", resp: Body{Whole: true}},
		{name: "chunked and unread", body: "chunked", chunked: true, handle: func(http.ResponseWriter, *http.Request) {},
			status: 200, length: "0", resp: Body{Whole: true}},
		{name: "an answer past captureMax", handle: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("a", captureMax)))
			_, _ = w.Write([]byte(strings.Repeat("z", 10)))
			_, _ = w.Write([]byte("!"))
		}, req: Body{Whole: true}, status: 200, answer: strings.Repeat("a", captureMax) + strings.Repeat("z", 10) + "!",
			resp: Body{Size: captureMax + 11}},
		{name: "no content", handle: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}, req: Body{Whole: true}, status: 204, resp: Body{Whole: true}},
		{name: "a length of the handler", handle: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "2")
			_, _ = w.Write([]byte("ok"))
		}, req: Body{Whole: true}, status: 200, answer: "ok", length: "2",
			resp: Body{Data: []byte("ok"), Size: 2, Whole: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := &captureRecorder{}
			h := Inbound(rec, clock.NewManual(t0), OperationPress, http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					Annotate(r.Context(), Links{Connection: 7, ConnectionID: inboundConn})
					tt.handle(w, r)
				}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", strings.NewReader(tt.body))
			if tt.chunked {
				req.ContentLength = -1
			}
			w := serve(t, h, req)
			if w.Code != tt.status || w.Body.String() != tt.answer || w.Header().Get("Content-Length") != tt.length {
				t.Fatalf("answered %d, %d bytes, length %q", w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
			}
			got := rec.got()
			if len(got) != 1 {
				t.Fatalf("%d records", len(got))
			}
			r := got[0]
			if r.Request.Size != tt.req.Size || r.Request.Whole != tt.req.Whole ||
				!bytes.Equal(r.Request.Data, tt.req.Data) || r.Status != tt.status ||
				r.Response.Size != tt.resp.Size || r.Response.Whole != tt.resp.Whole ||
				!bytes.Equal(r.Response.Data, tt.resp.Data) {
				t.Errorf("request %d %v %d bytes, status %d, answer %d %v %d bytes", r.Request.Size, r.Request.Whole,
					len(r.Request.Data), r.Status, r.Response.Size, r.Response.Whole, len(r.Response.Data))
			}
		})
	}
}

// failingWriter is a ResponseWriter whose writes fail.
type failingWriter struct{ *httptest.ResponseRecorder }

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the caller went away") }

// TestInboundCallerGone: an answer past captureMax to a caller that went away fails the handler's write, and the
// request is still recorded.
func TestInboundCallerGone(t *testing.T) {
	rec := &captureRecorder{}
	var werr error
	h := Inbound(rec, clock.NewManual(t0), OperationPress, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			Annotate(r.Context(), Links{Connection: 7, ConnectionID: inboundConn})
			_, werr = w.Write(make([]byte, captureMax+1))
		}))
	h.ServeHTTP(failingWriter{httptest.NewRecorder()},
		httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", nil))
	if werr == nil || len(rec.got()) != 1 || rec.got()[0].Response.Size != captureMax+1 {
		t.Errorf("write %v, records %+v", werr, rec.got())
	}
}

// TestInboundAnswersBeforeRecord covers C-23.FR-1 and FR-4: the caller has Muster's whole answer, with its length,
// while its record is still being written; the request ends only when the write does.
func TestInboundAnswersBeforeRecord(t *testing.T) {
	rec := &captureRecorder{started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	srv := httptest.NewServer(Inbound(rec, clock.Real{}, OperationPush, annotated(`{"ok":true}`)))
	defer srv.Close()
	released := false
	defer func() {
		if !released {
			close(rec.release)
		}
	}()
	type answer struct {
		status int
		length int64
		body   string
		err    error
	}
	answers := make(chan answer, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/x", strings.NewReader("{}"))
		if err != nil {
			answers <- answer{err: err}
			return
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			answers <- answer{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		answers <- answer{status: resp.StatusCode, length: resp.ContentLength, body: string(b), err: err}
	}()
	select {
	case <-rec.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the record was never written")
	}
	select {
	case a := <-answers:
		if a.err != nil || a.status != http.StatusOK || a.length != 11 || a.body != `{"ok":true}` {
			t.Fatalf("answer %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer did not arrive while its record was being written")
	}
	if len(rec.got()) != 0 {
		t.Fatal("the record was written before its write was released")
	}
	close(rec.release)
	released = true
	select {
	case <-rec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the record was not kept")
	}
	if got := rec.got(); len(got) != 1 || got[0].Status != http.StatusOK || string(got[0].Request.Data) != "{}" {
		t.Errorf("records %+v", got)
	}
}

// TestInboundWriteFails covers C-23.FR-4 for inbound records: a record that the database refuses or whose write runs
// past its timeout leaves the answer as the handler wrote it, and is logged as request_log_write_failed without its
// content and counted.
func TestInboundWriteFails(t *testing.T) {
	failures := func() uint64 { return metrics.RequestLogWriteFailures.With("inbound").Get() }
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		Annotate(r.Context(), Links{Connection: 7, ConnectionID: inboundConn})
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":401,"detail":"answer-marker"}`))
	})
	request := func() *http.Request {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/callbacks/telegram/"+inboundConn,
			strings.NewReader("body-marker"))
		r.Header.Set("X-Marker", "hdr-marker")
		return r
	}
	plain := serve(t, h, request())
	for name, insert := range map[string]func(ctx context.Context, p dbgen.InsertRecordParams) error{
		"refused": func(context.Context, dbgen.InsertRecordParams) error {
			return &pgconn.PgError{Severity: "ERROR", Code: "23503", Message: "violates foreign key constraint"}
		},
		"past its timeout": func(ctx context.Context, _ dbgen.InsertRecordParams) error {
			<-ctx.Done()
			return ctx.Err()
		},
	} {
		t.Run(name, func(t *testing.T) {
			writer, out, _ := newTestWriter(&fakeQueries{insert: insert}, 10*time.Millisecond)
			before := failures()
			w := serve(t, Inbound(writer, clock.Real{}, OperationUpdate, h), request())
			header := w.Header().Clone()
			header.Del("Content-Length")
			if w.Code != plain.Code || w.Body.String() != plain.Body.String() ||
				!reflect.DeepEqual(header, plain.Header()) {
				t.Errorf("answered %d %v %q, unwrapped %d %v %q", w.Code, w.Header(), w.Body, plain.Code,
					plain.Header(), plain.Body)
			}
			line := out.String()
			if failures() != before+1 || strings.Count(line, "\n") != 1 || !strings.Contains(line,
				`"event":"request_log_write_failed","direction":"inbound","operation":"update","owner":"`+inboundConn+`"`) {
				t.Errorf("failures +%d, logged %s", failures()-before, line)
			}
			for _, marker := range []string{"hdr-marker", "body-marker", "answer-marker", "callbacks/telegram"} {
				if strings.Contains(line, marker) {
					t.Errorf("the log line holds %s: %s", marker, line)
				}
			}
		})
	}
}

// TestRecordInbound covers the row of an inbound record (C-23.FR-2): the direction inbound, the path direct, the
// attempt 1, no error, no check kind or run, the Connection and the links, Muster's answer as its status — 200 when the
// handler wrote none — the business time; a body Inbound did not keep whole is stored empty, truncated, with its size,
// a whole one is cut at request_log.body_max; a method longer than the column is cut; a nil Writer records nothing.
func TestRecordInbound(t *testing.T) {
	records := func() uint64 { return metrics.RequestLogRecords.With("inbound").Get() }
	q := &fakeQueries{}
	w, out, business := newTestWriter(q, 0)
	business.Advance(time.Minute)
	before := records()
	w.RecordInbound(t.Context(), InboundRecord{
		Links:     Links{Connection: 7, ConnectionID: inboundConn, Destination: 3, AlertGroup: 9},
		Operation: OperationPress, Method: http.MethodPost, URL: "/api/v1/callbacks/mattermost/" + inboundConn,
		RequestHeader:  http.Header{"Content-Type": {"application/json"}},
		Request:        Body{Data: []byte(`{"a":1}`), Size: 7, Whole: true},
		ResponseHeader: http.Header{"Content-Type": {"application/json"}},
		Response:       Body{Data: []byte("{}"), Size: 2, Whole: true}, Latency: 12 * time.Millisecond})
	if len(q.inserts) != 1 || records() != before+1 || out.Len() != 0 {
		t.Fatalf("%d inserts, records +%d, logged %s", len(q.inserts), records()-before, out)
	}
	p := q.inserts[0]
	if p.OrgID != 1 || !p.At.Equal(t0.Add(time.Minute)) || p.Direction != "inbound" || p.Operation != "press" ||
		p.Method != http.MethodPost || p.Url != "/api/v1/callbacks/mattermost/"+inboundConn || p.Via != "direct" ||
		p.Attempt != 1 || p.ErrorClass.Valid || p.ErrorCode.Valid || p.Error.Valid ||
		p.ConnectionID != (pgtype.Int8{Int64: 7, Valid: true}) || p.DestinationID != (pgtype.Int8{Int64: 3, Valid: true}) ||
		p.AlertGroupID != (pgtype.Int8{Int64: 9, Valid: true}) || p.Status != (pgtype.Int4{Int32: 200, Valid: true}) ||
		string(p.RequestHeaders) != `{"Content-Type":["application/json"]}` || p.RequestBody != `{"a":1}` ||
		p.RequestBodyBytes != 7 || p.RequestBodyTruncated || string(p.ResponseHeaders) != `{"Content-Type":["application/json"]}` ||
		p.ResponseBody != (pgtype.Text{String: "{}", Valid: true}) || p.ResponseBodyBytes != 2 ||
		p.ResponseBodyTruncated || p.LatencyMs != 12 || p.CheckKind.Valid || p.CheckRun.Valid {
		t.Errorf("a press: %+v", p)
	}

	long := strings.Repeat("x", 20480)
	w.RecordInbound(t.Context(), InboundRecord{Links: Links{Connection: 7, ConnectionID: inboundConn},
		Operation: OperationUpdate, Method: strings.Repeat("M", 40), URL: "/x", Request: Body{Size: 50},
		Status: http.StatusUnauthorized, Response: Body{Size: captureMax + 10}})
	w.RecordInbound(t.Context(), InboundRecord{Links: Links{Connection: 7, ConnectionID: inboundConn},
		Operation: OperationPush, Method: http.MethodPost, URL: "/x",
		Request: Body{Data: []byte(long), Size: int64(len(long)), Whole: true}, Status: http.StatusNoContent,
		Response: Body{Whole: true}})
	cut, whole := q.inserts[1], q.inserts[2]
	if cut.Method != strings.Repeat("M", maxMethod) || cut.RequestBody != "" || !cut.RequestBodyTruncated ||
		cut.RequestBodyBytes != 50 || cut.ResponseBody != (pgtype.Text{Valid: true}) || !cut.ResponseBodyTruncated ||
		cut.ResponseBodyBytes != captureMax+10 || cut.Status.Int32 != 401 || string(cut.RequestHeaders) != "{}" ||
		string(cut.ResponseHeaders) != "{}" || cut.DestinationID.Valid || cut.AlertGroupID.Valid {
		t.Errorf("bodies Inbound did not keep whole: %+v", cut)
	}
	if len(whole.RequestBody) != BodyMax || !whole.RequestBodyTruncated || whole.RequestBodyBytes != 20480 ||
		whole.ResponseBody != (pgtype.Text{Valid: true}) || whole.ResponseBodyTruncated || whole.ResponseBodyBytes != 0 {
		t.Errorf("a body past request_log.body_max: %d bytes of %d, cut %v; answer %+v", len(whole.RequestBody),
			whole.RequestBodyBytes, whole.RequestBodyTruncated, whole.ResponseBody)
	}

	var none *Writer
	none.RecordInbound(t.Context(), InboundRecord{Links: Links{Connection: 7}})
}

// texts are the string, []byte and pgtype.Text fields of the struct v.
func texts(v any) []string {
	var out []string
	rv := reflect.ValueOf(v)
	for i := range rv.NumField() {
		switch f := rv.Field(i).Interface().(type) {
		case string:
			out = append(out, f)
		case []byte:
			out = append(out, string(f))
		case pgtype.Text:
			out = append(out, f.String)
		}
	}
	return out
}

// TestInboundMasking covers C-23.FR-3 and C-23.AC-3 for the requests of the callbacks: the values of Authorization,
// X-Telegram-Bot-Api-Secret-Token, webhook-signature, Cookie and Set-Cookie, the signed action id in a press's context,
// in a Telegram button's data and in a query, a `/start <token>` in an update and the token of a pushed account_link
// event reach no column of the stored row; each is [redacted].
func TestInboundMasking(t *testing.T) {
	press := signed(t, buttons.SubjectRoot, buttons.CommandAcknowledge)
	data := signed(t, buttons.SubjectRoot, buttons.CommandResolve)
	query := signed(t, buttons.SubjectReply, buttons.CommandStillOnIt)
	start, link := linkToken(1), linkToken(2)
	q := &fakeQueries{}
	writer, out, _ := newTestWriter(q, 0)
	h := Inbound(writer, clock.NewManual(t0), OperationPress, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			Annotate(r.Context(), Links{Connection: 7, ConnectionID: inboundConn})
			w.Header().Set("Set-Cookie", "session=s3cret-e")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		}))
	bodies := []struct{ body, shows string }{
		{`{"user_id":"u-bob","channel_id":"c","post_id":"p","trigger_id":"tr","context":{"action":"` + press +
			`","key_id":"k-01"}}`, `"context":{"action":"[redacted]","key_id":"k-01"}`},
		{`{"update_id":1,"message":{"message_id":1,"from":{"id":42,"is_bot":false,"first_name":"Bob"},` +
			`"chat":{"id":42,"type":"private"},"date":1,"text":"/start ` + start + `"}}`, `"text":"/start [redacted]"`},
		{`{"update_id":2,"callback_query":{"id":"q1","from":{"id":42,"is_bot":false,"first_name":"Bob"},"data":"` +
			data + `"}}`, `"data":"[redacted]"`},
		{`{"id":"e1","type":"account_link","occurred_at":"2026-10-18T12:00:00Z","token":"` + link +
			`","user":{"id":"u-bob","name":"Bob"}}`, `"token":"[redacted]"`},
	}
	for i, b := range bodies {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/api/v1/callbacks/x/"+inboundConn+"?a="+query, strings.NewReader(b.body))
		req.Header.Set("Authorization", "Bearer s3cret-a")
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "s3cret-b")
		req.Header["webhook-signature"] = []string{"v1,s3cret-c"}
		req.Header.Set("Cookie", "session=s3cret-d")
		if w := serve(t, h, req); w.Code != http.StatusOK || w.Body.String() != "{}" {
			t.Fatalf("request %d answered %d %q", i, w.Code, w.Body)
		}
		if len(q.inserts) != i+1 {
			t.Fatalf("request %d: %d inserts, logged %s", i, len(q.inserts), out)
		}
		p := q.inserts[i]
		for _, text := range texts(p) {
			for _, secret := range []string{press, data, query, start, link, "s3cret-"} {
				if strings.Contains(text, secret) {
					t.Errorf("request %d stores %q in %q", i, secret, text)
				}
			}
		}
		if !strings.Contains(p.RequestBody, b.shows) || p.Url != "/api/v1/callbacks/x/"+inboundConn+"?a=[redacted]" {
			t.Errorf("request %d: body %s, URL %s", i, p.RequestBody, p.Url)
		}
		for _, want := range []string{`"Authorization":["[redacted]"]`,
			`"X-Telegram-Bot-Api-Secret-Token":["[redacted]"]`, `"webhook-signature":["[redacted]"]`,
			`"Cookie":["[redacted]"]`} {
			if !strings.Contains(string(p.RequestHeaders), want) {
				t.Errorf("request %d: headers %s, want %s", i, p.RequestHeaders, want)
			}
		}
		if !strings.Contains(string(p.ResponseHeaders), `"Set-Cookie":["[redacted]"]`) {
			t.Errorf("request %d: answer headers %s", i, p.ResponseHeaders)
		}
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("logged %s", out)
	}
}

func TestBodyAllowed(t *testing.T) {
	for status, want := range map[int]bool{100: false, 200: true, 201: true, 204: false, 304: false, 401: true,
		500: true} {
		if bodyAllowed(status) != want {
			t.Errorf("bodyAllowed(%d) = %v", status, !want)
		}
	}
}
