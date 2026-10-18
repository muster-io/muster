// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package requestlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/requestlog/dbgen"
)

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

// fakeQueries answers the queries the tests set and fails every other one.
type fakeQueries struct {
	Queries
	mu      sync.Mutex
	insert  func(ctx context.Context, p dbgen.InsertRecordParams) error
	inserts []dbgen.InsertRecordParams

	conn     map[string]int64
	dest     map[string]dbgen.FindDestinationRow
	findErr  error
	connList []dbgen.ListConnectionRecordsRow
	destList []dbgen.ListDestinationRecordsRow
	listErr  error
	connArgs []dbgen.ListConnectionRecordsParams
	destArgs []dbgen.ListDestinationRecordsParams
	record   *dbgen.GetRecordRow
	getErr   error
	newer    int64
	newerErr error
	counted  []string
}

func (f *fakeQueries) InsertRecord(ctx context.Context, p dbgen.InsertRecordParams) error {
	f.mu.Lock()
	f.inserts = append(f.inserts, p)
	f.mu.Unlock()
	if f.insert != nil {
		return f.insert(ctx, p)
	}
	return nil
}

func (f *fakeQueries) FindConnection(_ context.Context, p dbgen.FindConnectionParams) (int64, error) {
	if f.findErr != nil {
		return 0, f.findErr
	}
	id, ok := f.conn[p.PublicID]
	if !ok || p.OrgID != 1 {
		return 0, pgx.ErrNoRows
	}
	return id, nil
}

func (f *fakeQueries) FindDestination(_ context.Context, p dbgen.FindDestinationParams) (
	dbgen.FindDestinationRow, error) {
	if f.findErr != nil {
		return dbgen.FindDestinationRow{}, f.findErr
	}
	d, ok := f.dest[p.PublicID]
	if !ok || p.OrgID != 1 {
		return dbgen.FindDestinationRow{}, pgx.ErrNoRows
	}
	return d, nil
}

func (f *fakeQueries) ListConnectionRecords(_ context.Context, p dbgen.ListConnectionRecordsParams) (
	[]dbgen.ListConnectionRecordsRow, error) {
	f.connArgs = append(f.connArgs, p)
	return f.connList, f.listErr
}

func (f *fakeQueries) ListDestinationRecords(_ context.Context, p dbgen.ListDestinationRecordsParams) (
	[]dbgen.ListDestinationRecordsRow, error) {
	f.destArgs = append(f.destArgs, p)
	return f.destList, f.listErr
}

func (f *fakeQueries) GetRecord(_ context.Context, p dbgen.GetRecordParams) (dbgen.GetRecordRow, error) {
	if f.getErr != nil {
		return dbgen.GetRecordRow{}, f.getErr
	}
	if f.record == nil || f.record.PublicID != p.PublicID || p.OrgID != 1 || !p.NotBefore.Equal(t0.Add(-MaxAge)) {
		return dbgen.GetRecordRow{}, pgx.ErrNoRows
	}
	return *f.record, nil
}

func (f *fakeQueries) count(kind string, maxRecords int32) (int64, error) {
	f.counted = append(f.counted, kind)
	if maxRecords != MaxRecords {
		return 0, errors.New("the bound is not request_log.max_records")
	}
	return f.newer, f.newerErr
}

func (f *fakeQueries) CountNewerOfConnection(_ context.Context, p dbgen.CountNewerOfConnectionParams) (int64,
	error) {
	return f.count("connection", p.MaxRecords)
}

func (f *fakeQueries) CountNewerOfDestination(_ context.Context, p dbgen.CountNewerOfDestinationParams) (int64,
	error) {
	return f.count("destination", p.MaxRecords)
}

func (f *fakeQueries) CountNewerUnowned(_ context.Context, p dbgen.CountNewerUnownedParams) (int64, error) {
	return f.count("none", p.MaxRecords)
}

// fakeDB is a dbgen.DBTX that keeps the arguments of every Exec and fails every read.
type fakeDB struct {
	execs [][]any
	err   error
}

func (d *fakeDB) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	d.execs = append(d.execs, args)
	return pgconn.NewCommandTag("DELETE 0"), d.err
}

func (d *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("no reads")
}

func (d *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return failedRow{} }

type failedRow struct{}

func (failedRow) Scan(...any) error { return errors.New("no reads") }

func newTestWriter(q Queries, timeout time.Duration) (*Writer, *bytes.Buffer, *clock.Manual) {
	var out bytes.Buffer
	business := clock.NewManual(t0)
	return newWriter(Config{OrgID: 1, Business: business, Log: logging.New(&out, logging.LevelInfo),
		Timeout: timeout}, q), &out, business
}

// TestParamsOf maps the outbound record of each kind of attempt to its row (C-23.FR-2): the business time, an RQ id,
// the owner and links, the answer or none, the error class and, for a request that got no answer, the code of
// delivery.FailureCode; headers, URL, bodies and error shape-masked and sanitized; defaults for what is missing.
func TestParamsOf(t *testing.T) {
	w, _, business := newTestWriter(&fakeQueries{}, 0)
	business.Advance(time.Hour)
	ack := signed(t, buttons.SubjectRoot, buttons.CommandAcknowledge)
	ok := outbound.Record{
		Owner: outbound.Owner{Connection: 3, PublicID: "CN0000000000M1"}, Links: outbound.Links{Destination: 4,
			AlertGroup: 5},
		Operation: "createPost", Method: http.MethodPost, URL: "http://mm/[redacted]/api/v4/posts?a=" + ack,
		RequestHeader: http.Header{"User-Agent": {"Muster/1.0.0"}, "Authorization": {"[redacted]"},
			"X-Nul": {"a\x00b"}, "X-Empty": nil},
		RequestBody: []byte(`{"props":{"context":{"action":"` + ack + `"}}}`), Status: 201,
		ResponseHeader: http.Header{"Content-Type": {"application/json"}}, ResponseBody: []byte(`{"id":"p1"}`),
		Latency: 1500 * time.Microsecond, Via: outbound.ViaProxy, Attempt: 2, Outcome: outbound.OutcomeOK,
	}
	p := w.paramsOf(DirectionOutbound, ok)
	var headers map[string][]string
	if err := json.Unmarshal(p.RequestHeaders, &headers); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^RQ[0-9A-HJKMNP-TV-Z]{12}$`).MatchString(p.PublicID) || !p.At.Equal(t0.Add(time.Hour)) ||
		p.OrgID != 1 || p.Direction != "outbound" || p.Operation != "createPost" || p.Method != http.MethodPost ||
		p.Url != "http://mm/[redacted]/api/v4/posts?a=[redacted]" ||
		p.RequestBody != `{"props":{"context":{"action":"[redacted]"}}}` || p.RequestBodyBytes != int64(len(ok.RequestBody)) ||
		p.RequestBodyTruncated || p.Status != (pgtype.Int4{Int32: 201, Valid: true}) ||
		string(p.ResponseHeaders) != `{"Content-Type":["application/json"]}` ||
		p.ResponseBody != (pgtype.Text{String: `{"id":"p1"}`, Valid: true}) || p.ResponseBodyBytes != 11 ||
		p.LatencyMs != 1 || p.Via != "proxy" || p.Attempt != 2 || p.ErrorClass.Valid || p.ErrorCode.Valid ||
		p.Error.Valid || p.ConnectionID != (pgtype.Int8{Int64: 3, Valid: true}) ||
		p.DestinationID != (pgtype.Int8{Int64: 4, Valid: true}) || p.AlertGroupID != (pgtype.Int8{Int64: 5, Valid: true}) {
		t.Errorf("a Publication: %+v", p)
	}
	if headers["X-Nul"][0] != "a\uFFFDb" || headers["Authorization"][0] != "[redacted]" ||
		headers["User-Agent"][0] != "Muster/1.0.0" || headers["X-Empty"] == nil {
		t.Errorf("headers %v", headers)
	}
	if w.paramsOf(DirectionOutbound, ok).PublicID == p.PublicID {
		t.Error("two records with one public_id")
	}

	for _, tt := range []struct {
		name                  string
		r                     outbound.Record
		class, code, errText  string
		status                int32
		conn, dest, group     int64
		respHeaders, respBody bool
	}{
		{name: "an outgoing webhook answered 500", r: outbound.Record{Owner: outbound.Owner{Destination: 8},
			Links: outbound.Links{Destination: 8, AlertGroup: 9}, Operation: "event", Status: 500,
			ResponseBody: []byte("boom"), Outcome: outbound.OutcomeTransient, Error: "500 Internal Server Error"},
			class: "transient", errText: "500 Internal Server Error", status: 500, dest: 8, group: 9,
			respHeaders: true, respBody: true},
		{name: "a redirect", r: outbound.Record{Owner: outbound.Owner{Connection: 3}, Status: 302,
			Outcome: outbound.OutcomeRedirect, Error: "redirected to " + ack}, class: "redirect",
			errText: "redirected to [redacted]", status: 302, conn: 3, respHeaders: true, respBody: true},
		{name: "no answer: DNS", r: outbound.Record{Owner: outbound.Owner{Connection: 3},
			Outcome: outbound.OutcomeTransient, Network: outbound.NetworkDNS, Error: "lookup mm: no such host"},
			class: "transient", code: "dns_failed", errText: "lookup mm: no such host", conn: 3},
		{name: "no answer: refused", r: outbound.Record{Owner: outbound.Owner{Connection: 3},
			Outcome: outbound.OutcomeTransient, Network: outbound.NetworkConnect, Error: "connection refused"},
			class: "transient", code: "connect_failed", errText: "connection refused", conn: 3},
		{name: "no answer: blocked", r: outbound.Record{Owner: outbound.Owner{Destination: 8},
			Outcome: outbound.OutcomeBlocked, Error: "blocked by the outbound address policy"}, class: "blocked",
			code: "blocked", errText: "blocked by the outbound address policy", dest: 8},
		{name: "no answer: another failure", r: outbound.Record{Owner: outbound.Owner{Connection: 3},
			Outcome: outbound.OutcomeFatal}, class: "fatal", code: "request_failed", conn: 3},
		{name: "a record of no owner", r: outbound.Record{Status: 200, Outcome: outbound.OutcomeOK},
			status: 200, respHeaders: true, respBody: true},
	} {
		p := w.paramsOf(DirectionOutbound, tt.r)
		if p.ErrorClass.String != tt.class || p.ErrorClass.Valid != (tt.class != "") ||
			p.ErrorCode.String != tt.code || p.ErrorCode.Valid != (tt.code != "") ||
			p.Error.String != tt.errText || p.Error.Valid != (tt.errText != "") || p.Status.Int32 != tt.status ||
			p.Status.Valid != (tt.status != 0) || p.ConnectionID.Int64 != tt.conn ||
			p.ConnectionID.Valid != (tt.conn != 0) || p.DestinationID.Int64 != tt.dest ||
			p.DestinationID.Valid != (tt.dest != 0) || p.AlertGroupID.Int64 != tt.group ||
			(p.ResponseHeaders != nil) != tt.respHeaders || p.ResponseBody.Valid != tt.respBody {
			t.Errorf("%s: %+v", tt.name, p)
		}
		if tt.respHeaders && string(p.ResponseHeaders) != "{}" || p.Method != http.MethodGet || p.Via != "direct" ||
			p.Attempt != 1 || p.RequestBody != "" || string(p.RequestHeaders) != "{}" {
			t.Errorf("%s: the defaults %+v", tt.name, p)
		}
	}

	long := w.paramsOf(DirectionOutbound, outbound.Record{Latency: 365 * 24 * time.Hour,
		RequestBody: bytes.Repeat([]byte("b"), BodyMax+1), Status: 200, ResponseBody: []byte("\xff"),
		Error: strings.Repeat("e", BodyMax+5)})
	if long.LatencyMs != math.MaxInt32 || !long.RequestBodyTruncated || len(long.RequestBody) != BodyMax ||
		long.RequestBodyBytes != BodyMax+1 || long.ResponseBody.String != "\uFFFD" || long.ResponseBodyBytes != 1 ||
		len(long.Error.String) != BodyMax {
		t.Errorf("bounds: latency %d, request %d of %d, cut %v, answer %q, error %d", long.LatencyMs,
			len(long.RequestBody), long.RequestBodyBytes, long.RequestBodyTruncated, long.ResponseBody.String,
			len(long.Error.String))
	}
	if neg := w.paramsOf(DirectionOutbound, outbound.Record{Latency: -time.Second}); neg.LatencyMs != 0 {
		t.Errorf("a negative latency: %d", neg.LatencyMs)
	}
}

// TestRecord covers the write statements of C-23.FR-4: a written record is counted in
// muster_request_log_records_total; one the database refuses, or whose write runs past its timeout, is logged as
// request_log_write_failed without its content and counted in muster_request_log_write_failures_total, and nothing
// reaches the caller; the write runs under a context of its own, alive when the caller's has ended, that ends after
// the timeout.
func TestRecord(t *testing.T) {
	records := func() uint64 { return metrics.RequestLogRecords.With("outbound").Get() }
	failures := func() uint64 { return metrics.RequestLogWriteFailures.With("outbound").Get() }
	r := outbound.Record{Owner: outbound.Owner{Connection: 3, PublicID: "CN0000000000M1"}, Operation: "createPost",
		Method: http.MethodPost, URL: "http://mm/api/v4/posts", RequestHeader: http.Header{"X-Marker": {"hdr-marker"}},
		RequestBody: []byte("body-marker"), Status: 201, ResponseBody: []byte("answer-marker"),
		Outcome: outbound.OutcomeOK}

	q := &fakeQueries{}
	w, out, _ := newTestWriter(q, 0)
	before, beforeFailures := records(), failures()
	w.Record(t.Context(), r)
	if len(q.inserts) != 1 || records() != before+1 || failures() != beforeFailures || out.Len() != 0 {
		t.Errorf("a written record: %d inserts, records +%d, failures +%d, logged %s", len(q.inserts),
			records()-before, failures()-beforeFailures, out)
	}

	refused := &fakeQueries{insert: func(context.Context, dbgen.InsertRecordParams) error {
		return &pgconn.PgError{Severity: "ERROR", Code: "23514", Message: `new row for relation "request_log" violates check constraint`}
	}}
	w, out, _ = newTestWriter(refused, 0)
	before, beforeFailures = records(), failures()
	w.Record(t.Context(), r)
	line := out.String()
	if records() != before || failures() != beforeFailures+1 ||
		!strings.Contains(line, `"level":"WARN","event":"request_log_write_failed","direction":"outbound",`+
			`"operation":"createPost","owner":"CN0000000000M1","error":"ERROR: new row for relation`) {
		t.Errorf("a refused record: records +%d, failures +%d, logged %s", records()-before,
			failures()-beforeFailures, line)
	}
	for _, marker := range []string{"hdr-marker", "body-marker", "answer-marker", "api/v4/posts"} {
		if strings.Contains(line, marker) {
			t.Errorf("the log line holds %s: %s", marker, line)
		}
	}

	var deadline time.Time
	var alive bool
	detached := &fakeQueries{insert: func(ctx context.Context, _ dbgen.InsertRecordParams) error {
		deadline, _ = ctx.Deadline()
		alive = ctx.Err() == nil
		return nil
	}}
	w, _, _ = newTestWriter(detached, 0)
	caller, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	w.Record(caller, r)
	if !alive || deadline.Before(start.Add(WriteTimeout-time.Second)) || deadline.After(time.Now().Add(WriteTimeout)) {
		t.Errorf("the write of a caller whose context ended: alive %v, deadline in %v", alive, deadline.Sub(start))
	}

	slow := &fakeQueries{insert: func(ctx context.Context, _ dbgen.InsertRecordParams) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	w, out, _ = newTestWriter(slow, 10*time.Millisecond)
	beforeFailures = failures()
	w.Record(t.Context(), r)
	if failures() != beforeFailures+1 || !strings.Contains(out.String(), `"error":"context deadline exceeded"`) {
		t.Errorf("a write past its timeout: failures +%d, logged %s", failures()-beforeFailures, out)
	}

	db := &fakeDB{}
	nw := NewWriter(Config{OrgID: 1, DB: db, Business: clock.NewManual(t0), Log: logging.New(out, logging.LevelInfo)})
	nw.Record(t.Context(), r)
	if nw.timeout != WriteTimeout || len(db.execs) != 1 || db.execs[0][0] != int64(1) {
		t.Errorf("NewWriter: timeout %v, execs %v", nw.timeout, db.execs)
	}
}

func listRow(id int64, at time.Time) dbgen.ListConnectionRecordsRow {
	return dbgen.ListConnectionRecordsRow{ID: id, PublicID: "RQ00000000000" + string(rune('0'+id%10)), At: at,
		Direction: "outbound", Operation: "createPost", Method: "POST", Url: "http://mm/api/v4/posts",
		LatencyMs: 12, Via: "direct", Attempt: 1}
}

func newTestReader(q Queries) *Reader {
	return &Reader{orgID: 1, q: q, business: clock.NewManual(t0)}
}

// TestList covers the reads of C-23.FR-5 over the queries: exactly one owner, unknown or deleted owners not found, the
// bounds and every filter handed to the query of the owner — a Connection's, a messenger Destination's within its
// Connection's, an outgoing webhook's — and pages that end where the next one starts.
func TestList(t *testing.T) {
	q := &fakeQueries{conn: map[string]int64{"CN0000000000M1": 3}, dest: map[string]dbgen.FindDestinationRow{
		"DS0000000000M1": {ID: 4, Type: "mattermost", ConnectionID: pgtype.Int8{Int64: 3, Valid: true}},
		"DS0000000000W1": {ID: 8, Type: "webhook"}}}
	r := newTestReader(q)
	ctx := t.Context()
	for _, f := range []Filter{{}, {Connection: "CN0000000000M1", Destination: "DS0000000000M1"}} {
		if _, err := r.List(ctx, f); !errors.Is(err, ErrOneOfRequired) {
			t.Errorf("%+v: %v", f, err)
		}
	}
	for _, f := range []Filter{{Connection: "CN0000000000ZZ"}, {Connection: "DS0000000000M1"},
		{Connection: "nonsense"}, {Destination: "DS0000000000ZZ"}, {Destination: "CN0000000000M1"}} {
		if _, err := r.List(ctx, f); !errors.Is(err, ErrNotFound) {
			t.Errorf("%+v: %v", f, err)
		}
	}

	q.connList = []dbgen.ListConnectionRecordsRow{listRow(9, t0), listRow(8, t0), listRow(7, t0.Add(-time.Minute))}
	page, err := r.List(ctx, Filter{Connection: "cn0000000000m1", Direction: "outbound", StatusClass: "4xx",
		Operation: "createPost", AlertGroup: "agk7m3qx9p2rta", After: &Position{At: t0, ID: 10}, Limit: 2})
	want := dbgen.ListConnectionRecordsParams{OrgID: 1, ConnectionID: 3, NotBefore: t0.Add(-MaxAge),
		MaxRecords: MaxRecords, PageSize: 3, Direction: text("outbound"),
		StatusFrom: pgtype.Int4{Int32: 400, Valid: true}, StatusTo: pgtype.Int4{Int32: 500, Valid: true},
		Operation: text("createPost"), AlertGroup: text("AGK7M3QX9P2RTA"),
		AfterAt: pgtype.Timestamptz{Time: t0, Valid: true}, AfterID: pgtype.Int8{Int64: 10, Valid: true}}
	if err != nil || len(q.connArgs) != 1 || q.connArgs[0] != want {
		t.Fatalf("a Connection: %v, %+v", err, q.connArgs)
	}
	if len(page.Records) != 2 || page.Records[0].ID != 9 || page.Next == nil || *page.Next != (Position{At: t0, ID: 8}) {
		t.Errorf("a page of 2 of 3: %+v, next %+v", page.Records, page.Next)
	}
	q.connList = q.connList[:2]
	if page, err := r.List(ctx, Filter{Connection: "CN0000000000M1", Limit: 2}); err != nil || page.Next != nil ||
		len(page.Records) != 2 {
		t.Errorf("the last page: %+v, %v", page, err)
	}

	for class, want := range map[string][3]int32{"2xx": {200, 300}, "3xx": {300, 400}, "5xx": {500, 600},
		"none": {0, 0, 1}, "": {}} {
		q.connArgs = nil
		if _, err := r.List(ctx, Filter{Connection: "CN0000000000M1", StatusClass: class}); err != nil {
			t.Fatal(err)
		}
		a := q.connArgs[0]
		if a.StatusFrom.Int32 != want[0] || a.StatusTo.Int32 != want[1] || a.NoAnswer != (want[2] == 1) ||
			a.StatusFrom.Valid != (want[0] != 0) {
			t.Errorf("status class %q: %+v", class, a)
		}
	}
	if _, err := r.List(ctx, Filter{Connection: "CN0000000000M1", StatusClass: "6xx"}); err == nil ||
		errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown status class: %v", err)
	}
	q.connArgs = nil
	for limit, want := range map[int]int32{0: 2, -5: 2, 50: 51, 501: MaxRecords + 1} {
		q.connArgs = nil
		if _, err := r.List(ctx, Filter{Connection: "CN0000000000M1", Limit: limit}); err != nil ||
			q.connArgs[0].PageSize != want {
			t.Errorf("limit %d: page size %d, %v", limit, q.connArgs[0].PageSize, err)
		}
	}
	q.connArgs = nil
	if page, err := r.List(ctx, Filter{Connection: "CN0000000000M1", AlertGroup: "not-an-id"}); err != nil ||
		len(page.Records) != 0 || page.Records == nil || len(q.connArgs) != 0 {
		t.Errorf("an Alert Group that is no public_id: %+v, %v, %d queries", page, err, len(q.connArgs))
	}

	q.connArgs = nil
	if _, err := r.List(ctx, Filter{Destination: "DS0000000000M1"}); err != nil || len(q.connArgs) != 1 ||
		q.connArgs[0].ConnectionID != 3 || q.connArgs[0].DestinationID != (pgtype.Int8{Int64: 4, Valid: true}) {
		t.Errorf("a messenger Destination: %v, %+v", err, q.connArgs)
	}
	q.destList = []dbgen.ListDestinationRecordsRow{dbgen.ListDestinationRecordsRow(listRow(5, t0))}
	page, err = r.List(ctx, Filter{Destination: "DS0000000000W1", StatusClass: "none", Limit: 10})
	if err != nil || len(q.destArgs) != 1 || q.destArgs[0].DestinationID != 8 || !q.destArgs[0].NoAnswer ||
		q.destArgs[0].MaxRecords != MaxRecords || !q.destArgs[0].NotBefore.Equal(t0.Add(-MaxAge)) ||
		q.destArgs[0].PageSize != 11 || len(page.Records) != 1 || page.Records[0].ID != 5 {
		t.Errorf("an outgoing webhook: %v, %+v, %+v", err, q.destArgs, page)
	}

	q.listErr = errors.New("gone")
	for _, f := range []Filter{{Connection: "CN0000000000M1"}, {Destination: "DS0000000000W1"}} {
		if _, err := r.List(ctx, f); err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "gone") {
			t.Errorf("%+v, a failed list: %v", f, err)
		}
	}
	q.findErr = errors.New("down")
	for _, f := range []Filter{{Connection: "CN0000000000M1"}, {Destination: "DS0000000000W1"}} {
		if _, err := r.List(ctx, f); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%+v, a failed lookup: %v", f, err)
		}
	}
}

// TestSummaryOf maps a row to a Summary: nullable columns are nil, a Destination comes with its health, an Alert Group
// with its number.
func TestSummaryOf(t *testing.T) {
	bare := summaryOf(listRow(1, t0))
	if bare.Status != nil || bare.ErrorClass != nil || bare.ErrorCode != nil || bare.CheckKind != nil ||
		bare.CheckRun != nil || bare.Connection != nil || bare.Destination != nil || bare.AlertGroup != nil {
		t.Errorf("a bare record: %+v", bare)
	}
	row := listRow(2, t0)
	row.Status = pgtype.Int4{Int32: 503, Valid: true}
	row.ErrorClass, row.CheckKind = text("transient"), text("test")
	row.ConnectionPublicID, row.DestinationPublicID = text("CN0000000000M1"), text("DS0000000000M1")
	row.DestinationName, row.DestinationType, row.DestinationHealth = text("alerts"), text("mattermost"), text("broken")
	row.DestinationBrokenSince = pgtype.Timestamptz{Time: t0, Valid: true}
	row.DestinationBrokenReason = text("unavailable")
	row.AlertGroupPublicID, row.AlertGroupNumber = text("AGK7M3QX9P2RTA"), pgtype.Int8{Int64: 42, Valid: true}
	row.RequestBodyTruncated = true
	s := summaryOf(row)
	if *s.Status != 503 || *s.ErrorClass != "transient" || *s.CheckKind != "test" || *s.Connection != "CN0000000000M1" ||
		*s.Destination.BrokenReason != "unavailable" || !s.Destination.BrokenSince.Equal(t0) ||
		s.Destination.Name != "alerts" || s.Destination.Health != "broken" || s.Destination.Type != "mattermost" ||
		*s.AlertGroup != (AlertGroupRef{PublicID: "AGK7M3QX9P2RTA", Number: 42}) || !s.RequestBodyTruncated ||
		s.ResponseBodyTruncated || s.LatencyMS != 12 || s.Attempt != 1 {
		t.Errorf("a full record: %+v", s)
	}
	row.DestinationBrokenSince = pgtype.Timestamptz{}
	if s := summaryOf(row); s.Destination.BrokenSince != nil {
		t.Errorf("a healthy Destination: %+v", s.Destination)
	}
}

func getRow() *dbgen.GetRecordRow {
	return &dbgen.GetRecordRow{ID: 7, PublicID: "RQ0000000000A1", At: t0.Add(-time.Hour), Direction: "outbound",
		Operation: "createPost", Method: "POST", Url: "http://mm/api/v4/posts",
		RequestHeaders: []byte(`{"User-Agent":["Muster/1.0.0"]}`), RequestBody: "{}", RequestBodyBytes: 2,
		Status: pgtype.Int4{Int32: 201, Valid: true}, ResponseHeaders: []byte(`{}`),
		ResponseBody: text(`{"id":"p1"}`), ResponseBodyBytes: 11, LatencyMs: 12, Via: "direct", Attempt: 1,
		ConnectionID: pgtype.Int8{Int64: 3, Valid: true}, ConnectionPublicID: text("CN0000000000M1")}
}

// TestGet covers the read of one record (C-23.FR-5): its headers and bodies and the kind of its owner, which decides
// the Permission; not found when it does not exist, its owner is deleted, or it has request_log.max_records newer
// records of its owner.
func TestGet(t *testing.T) {
	q := &fakeQueries{record: getRow()}
	r := newTestReader(q)
	ctx := t.Context()
	rec, err := r.Get(ctx, "rq0000000000a1")
	if err != nil || rec.Owner != OwnerConnection || rec.RequestHeaders["User-Agent"][0] != "Muster/1.0.0" ||
		rec.ResponseHeaders == nil || *rec.ResponseBody != `{"id":"p1"}` || *rec.Status != 201 || rec.Error != nil ||
		*rec.Connection != "CN0000000000M1" || q.counted[0] != "connection" {
		t.Fatalf("a Connection's record: %+v, %v", rec, err)
	}
	for _, id := range []string{"RQ0000000000A2", "CN0000000000A1", ""} {
		if _, err := r.Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: %v", id, err)
		}
	}
	q.newer = MaxRecords - 1
	if _, err := r.Get(ctx, "RQ0000000000A1"); err != nil {
		t.Errorf("with %d newer records: %v", q.newer, err)
	}
	q.newer = MaxRecords
	if _, err := r.Get(ctx, "RQ0000000000A1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("past the count bound: %v", err)
	}
	q.newer = 0

	hook := getRow()
	hook.ConnectionID, hook.ConnectionPublicID = pgtype.Int8{}, pgtype.Text{}
	hook.DestinationID, hook.DestinationPublicID = pgtype.Int8{Int64: 8, Valid: true}, text("DS0000000000W1")
	hook.Status, hook.ResponseHeaders, hook.ResponseBody = pgtype.Int4{}, nil, pgtype.Text{}
	hook.ErrorClass, hook.ErrorCode, hook.Error = text("transient"), text("timeout"), text("timed out")
	q.record = hook
	rec, err = r.Get(ctx, "RQ0000000000A1")
	if err != nil || rec.Owner != OwnerDestination || rec.ResponseHeaders != nil || rec.ResponseBody != nil ||
		rec.Status != nil || *rec.ErrorCode != "timeout" || *rec.Error != "timed out" ||
		q.counted[len(q.counted)-1] != "destination" {
		t.Errorf("an outgoing webhook's record: %+v, %v", rec, err)
	}
	hook.DestinationDeletedAt = pgtype.Timestamptz{Time: t0, Valid: true}
	if _, err := r.Get(ctx, "RQ0000000000A1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted outgoing webhook: %v", err)
	}

	none := getRow()
	none.ConnectionID, none.ConnectionPublicID = pgtype.Int8{}, pgtype.Text{}
	q.record = none
	if rec, err := r.Get(ctx, "RQ0000000000A1"); err != nil || rec.Owner != OwnerNone ||
		q.counted[len(q.counted)-1] != "none" {
		t.Errorf("a record of no owner: %+v, %v", rec, err)
	}

	gone := getRow()
	gone.ConnectionDeletedAt = pgtype.Timestamptz{Time: t0, Valid: true}
	q.record = gone
	if _, err := r.Get(ctx, "RQ0000000000A1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted Connection: %v", err)
	}

	for name, broken := range map[string]func(*dbgen.GetRecordRow){
		"request headers": func(r *dbgen.GetRecordRow) { r.RequestHeaders = []byte("[") },
		"answer headers":  func(r *dbgen.GetRecordRow) { r.ResponseHeaders = []byte("[") },
	} {
		row := getRow()
		broken(row)
		q.record = row
		if _, err := r.Get(ctx, "RQ0000000000A1"); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("unreadable %s: %v", name, err)
		}
	}
	q.record = getRow()
	q.newerErr = errors.New("count failed")
	if _, err := r.Get(ctx, "RQ0000000000A1"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("a failed count: %v", err)
	}
	q.getErr = errors.New("down")
	if _, err := r.Get(ctx, "RQ0000000000A1"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("a failed read: %v", err)
	}
	if _, err := NewReader(1, &fakeDB{}, clock.NewManual(t0)).Get(ctx, "RQ0000000000A1"); err == nil ||
		!strings.Contains(err.Error(), "no reads") {
		t.Errorf("NewReader: %v", err)
	}
}

// pruneQueries deletes from per-owner backlogs, batch by batch.
type pruneQueries struct {
	Queries
	older, unowned int64
	conns, hooks   map[int64]int64
	fail           map[string]error
	calls          []string
	batches        []int32
	before         time.Time
	maxRecords     []int32
	cancel         func()
}

func (p *pruneQueries) take(left *int64, batch int32, call string) (int64, error) {
	p.calls = append(p.calls, call)
	p.batches = append(p.batches, batch)
	if err := p.fail[call]; err != nil {
		return 0, err
	}
	n := min(*left, int64(batch))
	*left -= n
	return n, nil
}

func (p *pruneQueries) PruneOlder(_ context.Context, a dbgen.PruneOlderParams) (int64, error) {
	p.before = a.Before
	return p.take(&p.older, a.BatchSize, "older")
}

func (p *pruneQueries) ListConnectionIDs(_ context.Context, orgID int64) ([]int64, error) {
	if err := p.fail["connections"]; err != nil || orgID != 1 {
		return nil, err
	}
	return []int64{3, 4}, nil
}

func (p *pruneQueries) ListWebhookDestinationIDs(_ context.Context, orgID int64) ([]int64, error) {
	if err := p.fail["hooks"]; err != nil || orgID != 1 {
		return nil, err
	}
	return []int64{8}, nil
}

func (p *pruneQueries) PruneConnectionExcess(_ context.Context, a dbgen.PruneConnectionExcessParams) (int64, error) {
	p.maxRecords = append(p.maxRecords, a.MaxRecords)
	left := p.conns[a.ConnectionID]
	defer func() { p.conns[a.ConnectionID] = left }()
	return p.take(&left, a.BatchSize, "connection")
}

func (p *pruneQueries) PruneDestinationExcess(_ context.Context, a dbgen.PruneDestinationExcessParams) (int64,
	error) {
	p.maxRecords = append(p.maxRecords, a.MaxRecords)
	left := p.hooks[a.DestinationID]
	defer func() { p.hooks[a.DestinationID] = left }()
	return p.take(&left, a.BatchSize, "destination")
}

func (p *pruneQueries) PruneUnownedExcess(_ context.Context, a dbgen.PruneUnownedExcessParams) (int64, error) {
	p.maxRecords = append(p.maxRecords, a.MaxRecords)
	if p.cancel != nil {
		p.cancel()
	}
	return p.take(&p.unowned, a.BatchSize, "unowned")
}

// TestPrune covers the pruning of C-23.FR-4 over the queries: the age bound at now minus request_log.max_age, then the
// count bound of every Connection, outgoing webhook Destination and of the records without an owner, each in batches
// until one comes back short, the deletions summed; an owner that fails does not stop the others, and every failure
// is returned; a context that ends stops the run.
func TestPrune(t *testing.T) {
	q := &pruneQueries{older: 7, unowned: 3, conns: map[int64]int64{3: 10, 4: 0}, hooks: map[int64]int64{8: 4}}
	n, err := prune(t.Context(), q, 1, t0, 3)
	if err != nil || n != 24 || !q.before.Equal(t0.Add(-MaxAge)) || strings.Join(q.calls, ",") !=
		"older,older,older,connection,connection,connection,connection,connection,destination,destination,"+
			"unowned,unowned" {
		t.Errorf("prune = %d, %v; calls %v", n, err, q.calls)
	}
	for i, b := range q.batches {
		if b != 3 {
			t.Errorf("batch %d of %d rows", i, b)
		}
	}
	for _, m := range q.maxRecords {
		if m != MaxRecords {
			t.Errorf("a count bound of %d", m)
		}
	}
	if q.older != 0 || q.unowned != 0 || q.conns[3] != 0 || q.hooks[8] != 0 {
		t.Errorf("left %+v", q)
	}

	q = &pruneQueries{older: 2, unowned: 1, conns: map[int64]int64{3: 5, 4: 2}, hooks: map[int64]int64{8: 1},
		fail: map[string]error{"connection": errors.New("locked"), "hooks": errors.New("hooks down")}}
	n, err = prune(t.Context(), q, 1, t0, PruneBatch)
	if n != 3 || err == nil || !strings.Contains(err.Error(), "prune the records of connection 3: locked") ||
		!strings.Contains(err.Error(), "prune the records of connection 4: locked") ||
		!strings.Contains(err.Error(), "list the outgoing webhooks: hooks down") {
		t.Errorf("failing owners: %d, %v", n, err)
	}
	q = &pruneQueries{fail: map[string]error{"connections": errors.New("down")}, hooks: map[int64]int64{}}
	if _, err := prune(t.Context(), q, 1, t0, PruneBatch); err == nil ||
		!strings.Contains(err.Error(), "list the connections: down") {
		t.Errorf("no Connections: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	q = &pruneQueries{unowned: 10, conns: map[int64]int64{}, hooks: map[int64]int64{}, cancel: cancel}
	if n, err := prune(ctx, q, 1, t0, 3); n != 3 || !errors.Is(err, context.Canceled) {
		t.Errorf("a context that ends: %d, %v", n, err)
	}

	db := &fakeDB{}
	if _, err := Prune(t.Context(), db, 1, t0); err == nil || len(db.execs) != 2 || db.execs[0][2] != int32(PruneBatch) {
		t.Errorf("Prune: %v, execs %v", err, db.execs)
	}
}
