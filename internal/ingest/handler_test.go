// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/server"
)

const (
	orgID = 7
	// The tokens of fakeAuth: valid of NTVALID000000A, revoked of NTREVOKED00000.
	validToken   = "mstr_int_valid"
	revokedToken = "mstr_int_revoked"
	brokenToken  = "mstr_int_broken"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fakeAuth stands for internal/integrations: one valid token, one revoked, one whose lookup fails.
type fakeAuth struct{}

func (fakeAuth) Authenticate(_ context.Context, value string) (integrations.Caller, error) {
	switch value {
	case validToken:
		return integrations.Caller{TokenID: 3, IntegrationID: 5, Integration: "NTVALID000000A"}, nil
	case revokedToken:
		return integrations.Caller{TokenID: 4, IntegrationID: 5, Integration: "NTREVOKED00000"},
			integrations.ErrInvalidToken
	case brokenToken:
		return integrations.Caller{Integration: integrations.Unknown}, errors.New("the database is unavailable")
	}
	return integrations.Caller{Integration: integrations.Unknown}, integrations.ErrInvalidToken
}

// fakeStorer records what the handler stores.
type fakeStorer struct {
	received []Received
	err      error
}

func (f *fakeStorer) Store(_ context.Context, r Received) (Stored, error) {
	if f.err != nil {
		return Stored{}, f.err
	}
	f.received = append(f.received, r)
	return Stored{PublicID: "SS00000000000" + strconv.Itoa(len(f.received))}, nil
}

type ingestTest struct {
	h     http.Handler
	store *fakeStorer
	log   *bytes.Buffer
}

func newIngest(limit int64) *ingestTest {
	var log bytes.Buffer
	store := &fakeStorer{}
	h := NewHandler(HandlerConfig{Auth: fakeAuth{}, Snapshots: store, Log: logging.New(&log, logging.LevelInfo),
		Real: clock.Real{}, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, BodyLimit: limit})
	return &ingestTest{h: h, store: store, log: &log}
}

type answer struct {
	status int
	header http.Header
	body   string
}

func (x *ingestTest) post(t *testing.T, path string, body io.Reader, headers ...string) answer {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, body)
	r.RemoteAddr = "10.1.2.3:5000"
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	x.h.ServeHTTP(w, r)
	return answer{status: w.Code, header: w.Header(), body: w.Body.String()}
}

// counter is the value of a series of /metrics, 0 when it is not there.
func counter(t *testing.T, series string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	for line := range strings.Lines(rec.Body.String()) {
		if name, value, ok := strings.Cut(strings.TrimSpace(line), " "); ok && name == series {
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	return 0
}

func requests(integration, outcome string) string {
	return `muster_ingest_requests_total{integration="` + integration + `",outcome="` + outcome + `"}`
}

// events are the log lines written, decoded.
func (x *ingestTest) events(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(x.log.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// TestIngestBothTokenForms is C-05.FR-3 and C-05.AC-1: a webhook with a valid token as Authorization: Bearer or in the
// path gets 202 with an empty body once stored, exactly as received with its Content-Type; it is counted as accepted
// and logged with its route pattern.
func TestIngestBothTokenForms(t *testing.T) {
	x := newIngest(0)
	before := counter(t, requests("NTVALID000000A", OutcomeAccepted))
	webhook := `{"version":"4","groupKey":"{}:{alertname=\"T\"}","status":"firing","alerts":[]}`
	a := x.post(t, "/api/v1/ingest", strings.NewReader(webhook), "Authorization", "Bearer "+validToken,
		"Content-Type", "application/json")
	if a.status != http.StatusAccepted || a.body != "" {
		t.Errorf("header token = %d %q", a.status, a.body)
	}
	a = x.post(t, "/api/v1/ingest/"+validToken, strings.NewReader("not json"), "Content-Type", "text/plain")
	if a.status != http.StatusAccepted {
		t.Errorf("path token = %d %q", a.status, a.body)
	}
	a = x.post(t, "/api/v1/ingest", bytes.NewReader([]byte{0xff, 0xfe}), "Authorization", "bearer  "+validToken)
	if a.status != http.StatusAccepted {
		t.Errorf("lowercase scheme = %d %q", a.status, a.body)
	}
	if len(x.store.received) != 3 || string(x.store.received[0].Body) != webhook ||
		x.store.received[0].ContentType != "application/json" || x.store.received[0].IntegrationID != 5 ||
		string(x.store.received[1].Body) != "not json" || x.store.received[1].ContentType != "text/plain" ||
		!bytes.Equal(x.store.received[2].Body, []byte{0xff, 0xfe}) || x.store.received[2].ContentType != "" {
		t.Errorf("stored %+v", x.store.received)
	}
	if got := counter(t, requests("NTVALID000000A", OutcomeAccepted)) - before; got != 3 {
		t.Errorf("accepted counted %v times, want 3", got)
	}
	ev := x.events(t)
	if len(ev) != 3 || ev[0]["event"] != "snapshot_accepted" || ev[0]["route_pattern"] != RouteHeader ||
		ev[1]["route_pattern"] != RoutePath || ev[0]["integration"] != "NTVALID000000A" ||
		ev[0]["stored_snapshot"] != "SS000000000001" || ev[0]["size_bytes"] != float64(len(webhook)) {
		t.Errorf("events %v", ev)
	}
	if strings.Contains(x.log.String(), validToken) {
		t.Error("the token reached the log")
	}
}

// TestIngestUnauthorized is C-05.FR-4, C-05.AC-1 and C-05.AC-7: a missing, unknown, revoked or other token gets 401,
// counted as unauthorized with the token's Integration when it is known and unknown otherwise; nothing is stored.
func TestIngestUnauthorized(t *testing.T) {
	x := newIngest(0)
	unknown := counter(t, requests("unknown", OutcomeUnauthorized))
	revoked := counter(t, requests("NTREVOKED00000", OutcomeUnauthorized))
	for name, req := range map[string][]string{
		"no token":        {"/api/v1/ingest"},
		"basic":           {"/api/v1/ingest", "Authorization", "Basic " + validToken},
		"unknown header":  {"/api/v1/ingest", "Authorization", "Bearer mstr_int_nope"},
		"unknown path":    {"/api/v1/ingest/mstr_int_nope"},
		"api token":       {"/api/v1/ingest", "Authorization", "Bearer mstr_pat_abc"},
		"revoked header":  {"/api/v1/ingest", "Authorization", "Bearer " + revokedToken},
		"revoked in path": {"/api/v1/ingest/" + revokedToken},
	} {
		a := x.post(t, req[0], strings.NewReader("{}"), req[1:]...)
		var p problem
		_ = json.Unmarshal([]byte(a.body), &p)
		if a.status != http.StatusUnauthorized || p.Status != 401 || p.Type != problemBase+"unauthenticated" ||
			a.header.Get("Content-Type") != "application/problem+json" || a.header.Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s = %d %s", name, a.status, a.body)
		}
		if strings.Contains(a.body, "mstr_") {
			t.Errorf("%s: the answer repeats the token: %s", name, a.body)
		}
	}
	if len(x.store.received) != 0 {
		t.Errorf("stored %d", len(x.store.received))
	}
	if got := counter(t, requests("unknown", OutcomeUnauthorized)) - unknown; got != 5 {
		t.Errorf("unknown unauthorized counted %v, want 5", got)
	}
	if got := counter(t, requests("NTREVOKED00000", OutcomeUnauthorized)) - revoked; got != 2 {
		t.Errorf("revoked unauthorized counted %v, want 2", got)
	}
	ev := x.events(t)
	last := ev[len(ev)-1]
	if last["event"] != "ingest_rejected" || last["outcome"] != "unauthorized" || last["client_address"] != "10.1.2.3" {
		t.Errorf("event %v", last)
	}
	if strings.Contains(x.log.String(), revokedToken) || strings.Contains(x.log.String(), "mstr_int_nope") {
		t.Error("a token from a path reached the log")
	}
	for _, e := range ev {
		if e["route_pattern"] != RouteHeader && e["route_pattern"] != RoutePath {
			t.Errorf("event without the route pattern: %v", e)
		}
	}
}

// readCounter counts the reads of a body.
type readCounter struct {
	r     io.Reader
	reads int
}

func (c *readCounter) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

// TestIngestTooLarge is C-05.FR-4 and C-05.AC-2: the token comes first, then the size; a Content-Length above the
// limit is refused before the body is read, and a body read past the limit is refused too, both 413 and counted as
// too_large; an oversized request without a valid token gets 401.
func TestIngestTooLarge(t *testing.T) {
	x := newIngest(16)
	before := counter(t, requests("NTVALID000000A", OutcomeTooLarge))
	body := &readCounter{r: strings.NewReader(strings.Repeat("x", 17))}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/ingest", body)
	r.Header.Set("Authorization", "Bearer "+validToken)
	r.ContentLength = 17
	w := httptest.NewRecorder()
	x.h.ServeHTTP(w, r)
	var p problem
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != http.StatusRequestEntityTooLarge || p.Type != problemBase+"payload-too-large" || body.reads != 0 {
		t.Errorf("declared size = %d %s after %d reads", w.Code, w.Body, body.reads)
	}
	// Without a Content-Length the body is read up to the limit.
	r = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/ingest/"+validToken,
		io.MultiReader(strings.NewReader(strings.Repeat("y", 17))))
	r.ContentLength = -1
	w = httptest.NewRecorder()
	x.h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("read size = %d %s", w.Code, w.Body)
	}
	if a := x.post(t, "/api/v1/ingest", strings.NewReader(strings.Repeat("z", 16)), "Authorization",
		"Bearer "+validToken); a.status != http.StatusAccepted {
		t.Errorf("a body of exactly the limit = %d", a.status)
	}
	if a := x.post(t, "/api/v1/ingest", strings.NewReader(strings.Repeat("x", 17))); a.status != http.StatusUnauthorized {
		t.Errorf("oversized without a token = %d, want 401", a.status)
	}
	if got := counter(t, requests("NTVALID000000A", OutcomeTooLarge)) - before; got != 2 {
		t.Errorf("too_large counted %v, want 2", got)
	}
	if len(x.store.received) != 1 {
		t.Errorf("stored %d, want 1", len(x.store.received))
	}
}

// failingBody fails the read of a body, as a sender that went away does.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// TestIngestFailures: a failed token lookup or write answers 500, which Alertmanager retries, and is logged; a body
// that never arrives stores nothing.
func TestIngestFailures(t *testing.T) {
	x := newIngest(0)
	if a := x.post(t, "/api/v1/ingest", strings.NewReader("{}"), "Authorization", "Bearer "+brokenToken); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failed lookup = %d", a.status)
	}
	x.store.err = errors.New("the write failed")
	a := x.post(t, "/api/v1/ingest/"+validToken, strings.NewReader("{}"))
	if a.status != http.StatusInternalServerError || !strings.Contains(a.body, problemBase+"internal") {
		t.Errorf("failed write = %d %s", a.status, a.body)
	}
	ev := x.events(t)
	if len(ev) != 2 || ev[0]["event"] != "ingest_failed" || ev[0]["integration"] != "unknown" ||
		ev[1]["integration"] != "NTVALID000000A" || ev[1]["route_pattern"] != RoutePath || ev[1]["level"] != "ERROR" {
		t.Errorf("events %v", ev)
	}
	if strings.Contains(x.log.String(), validToken) {
		t.Error("the token reached the log")
	}
	x.store.err = nil
	// A body that does not arrive whole drops the connection without an answer, never a 2xx.
	func() {
		defer func() {
			if p := recover(); p != http.ErrAbortHandler { //nolint:errorlint // the sentinel is compared as net/http does
				t.Errorf("a body that never arrived: recovered %v, want http.ErrAbortHandler", p)
			}
		}()
		_ = x.post(t, "/api/v1/ingest", failingBody{}, "Authorization", "Bearer "+validToken)
	}()
	if len(x.store.received) != 0 {
		t.Error("a body that never arrived was stored")
	}
	// A Content-Type that the database could not store is left out.
	_ = x.post(t, "/api/v1/ingest", strings.NewReader("{}"), "Authorization", "Bearer "+validToken, "Content-Type",
		"text/\xff")
	_ = x.post(t, "/api/v1/ingest", strings.NewReader("{}"), "Authorization", "Bearer "+validToken, "Content-Type",
		strings.Repeat("a", 256))
	if len(x.store.received) != 2 || x.store.received[0].ContentType != "" || x.store.received[1].ContentType != "" {
		t.Errorf("stored content types %+v", x.store.received)
	}
}

// TestIngestRoutes: the ingest listener serves only the ingestion routes, with POST; anything else is 404.
func TestIngestRoutes(t *testing.T) {
	x := newIngest(0)
	for _, tt := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/ingest", http.StatusNotFound},
		{http.MethodPost, "/api/v1/heartbeat", http.StatusNotFound},
		{http.MethodPost, "/api/v1/ingest/a/b", http.StatusNotFound},
		{http.MethodGet, "/", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		x.h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil))
		if w.Code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, w.Code, tt.want)
		}
	}
}

// TestSharedPort is C-02.AC-8: with one port for app and ingest, ingestion reaches this handler and every other path
// the app.
func TestSharedPort(t *testing.T) {
	x := newIngest(0)
	app := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	merged := server.Merge(app, x.h)
	for _, tt := range []struct {
		method, path, token string
		want                int
	}{
		{http.MethodPost, "/api/v1/ingest", validToken, http.StatusAccepted},
		{http.MethodPost, "/api/v1/ingest/" + validToken, "", http.StatusAccepted},
		{http.MethodGet, "/api/v1/integrations", "", http.StatusTeapot},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, strings.NewReader("{}"))
		if tt.token != "" {
			r.Header.Set("Authorization", "Bearer "+tt.token)
		}
		w := httptest.NewRecorder()
		merged.ServeHTTP(w, r)
		if w.Code != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, w.Code, tt.want)
		}
	}
}

// fakeStore is the database of the Service in memory.
type fakeStore struct {
	bodies    map[string][]byte
	snapshots []dbgen.InsertStoredSnapshotParams
	notified  []dbgen.NotifySnapshotParams
	listed    []dbgen.ListStoredSnapshotsParams
	got       []dbgen.GetStoredSnapshotParams
	rows      []dbgen.ListStoredSnapshotsRow
	snapshot  *dbgen.GetStoredSnapshotRow
	fail      map[string]error
}

func newStore() *fakeStore {
	return &fakeStore{bodies: map[string][]byte{}, fail: map[string]error{}}
}

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error { return f(s) }

func (s *fakeStore) InsertSnapshotBody(_ context.Context, arg dbgen.InsertSnapshotBodyParams) error {
	if err := s.fail["InsertSnapshotBody"]; err != nil {
		return err
	}
	key := string(arg.BodySha256) + arg.BodyDay.Time.Format(time.DateOnly)
	if _, ok := s.bodies[key]; !ok {
		s.bodies[key] = arg.Body
	}
	return nil
}

func (s *fakeStore) InsertStoredSnapshot(_ context.Context, arg dbgen.InsertStoredSnapshotParams) error {
	if err := s.fail["InsertStoredSnapshot"]; err != nil {
		return err
	}
	s.snapshots = append(s.snapshots, arg)
	return nil
}

func (s *fakeStore) NotifySnapshot(_ context.Context, arg dbgen.NotifySnapshotParams) error {
	if err := s.fail["NotifySnapshot"]; err != nil {
		return err
	}
	s.notified = append(s.notified, arg)
	return nil
}

func (s *fakeStore) GetRetention(context.Context, int64) (int64, error) {
	return 14, s.fail["GetRetention"]
}

func (s *fakeStore) FindSnapshotIntegration(_ context.Context, arg dbgen.FindSnapshotIntegrationParams) (
	dbgen.FindSnapshotIntegrationRow, error) {
	if err := s.fail["FindSnapshotIntegration"]; err != nil {
		return dbgen.FindSnapshotIntegrationRow{}, err
	}
	if arg.PublicID != "NTAAAAAAAAAAAA" || arg.OrgID != orgID {
		return dbgen.FindSnapshotIntegrationRow{}, pgx.ErrNoRows
	}
	return dbgen.FindSnapshotIntegrationRow{ID: 5, PublicID: arg.PublicID, Name: "prod-eu"}, nil
}

func (s *fakeStore) ListStoredSnapshots(_ context.Context, arg dbgen.ListStoredSnapshotsParams) (
	[]dbgen.ListStoredSnapshotsRow, error) {
	s.listed = append(s.listed, arg)
	if err := s.fail["ListStoredSnapshots"]; err != nil {
		return nil, err
	}
	return s.rows[:min(len(s.rows), int(arg.PageSize))], nil
}

func (s *fakeStore) GetStoredSnapshot(_ context.Context, arg dbgen.GetStoredSnapshotParams) (
	dbgen.GetStoredSnapshotRow, error) {
	s.got = append(s.got, arg)
	if err := s.fail["GetStoredSnapshot"]; err != nil {
		return dbgen.GetStoredSnapshotRow{}, err
	}
	if s.snapshot == nil || arg.PublicID != s.snapshot.PublicID {
		return dbgen.GetStoredSnapshotRow{}, pgx.ErrNoRows
	}
	return *s.snapshot, nil
}

// TestStore is the insert-only transaction of C-05.FR-3: the body once per day by its SHA-256, the pending Stored
// Snapshot received now on the business clock, and the notification for processing.
func TestStore(t *testing.T) {
	store := newStore()
	c := clock.NewManual(time.Date(2026, 10, 6, 23, 59, 59, 999_999_900, time.UTC))
	svc := New(orgID, store, c)
	body := []byte(`{"version":"4"}`)
	first, err := svc.Store(t.Context(), Received{IntegrationID: 5, Body: body, ContentType: "application/json"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Store(t.Context(), Received{IntegrationID: 5, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	s := store.snapshots[0]
	if !strings.HasPrefix(first.PublicID, "SS") || first.PublicID == second.PublicID || len(store.bodies) != 1 ||
		len(store.snapshots) != 2 || s.PublicID != first.PublicID || s.IntegrationID != 5 || s.OrgID != orgID ||
		!bytes.Equal(s.BodySha256, sum[:]) || s.SizeBytes != int64(len(body)) ||
		s.ContentType != (pgtype.Text{String: "application/json", Valid: true}) ||
		store.snapshots[1].ContentType.Valid {
		t.Errorf("stored %+v", store.snapshots)
	}
	// The receipt time is the business clock to the microsecond, and its UTC day is the body's day.
	if !s.ReceivedAt.Equal(time.Date(2026, 10, 6, 23, 59, 59, 999_999_000, time.UTC)) ||
		s.BodyDay.Time != time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC) {
		t.Errorf("received %v on %v", s.ReceivedAt, s.BodyDay.Time)
	}
	var n Notification
	if len(store.notified) != 2 || store.notified[0].Channel != SnapshotChannel ||
		json.Unmarshal([]byte(store.notified[0].Payload), &n) != nil || n != (Notification{OrgID: orgID, IntegrationID: 5}) {
		t.Errorf("notified %+v", store.notified)
	}
	for _, step := range []string{"InsertSnapshotBody", "InsertStoredSnapshot", "NotifySnapshot"} {
		store.fail[step] = errors.New("down")
		if _, err := svc.Store(t.Context(), Received{IntegrationID: 5, Body: body}); err == nil {
			t.Errorf("%s failing: no error", step)
		}
		delete(store.fail, step)
	}
}

func snapshotRow(id int64, at time.Time) dbgen.ListStoredSnapshotsRow {
	return dbgen.ListStoredSnapshotsRow{ID: id, PublicID: "SS00000000000" + strconv.FormatInt(id, 10), ReceivedAt: at,
		SizeBytes: 2, State: StatePending}
}

// TestList is C-05.FR-7 and FR-10: the list of an Integration, a deleted one included, filtered by time and state,
// newest first with a cursor, hides what is older than retention.stored_snapshots.
func TestList(t *testing.T) {
	store := newStore()
	c := clock.NewManual(t0)
	svc := New(orgID, store, c)
	store.rows = []dbgen.ListStoredSnapshotsRow{snapshotRow(3, t0), snapshotRow(2, t0), snapshotRow(1, t0.Add(-time.Hour))}
	from, to := t0.Add(-2*time.Hour), t0.Add(time.Hour)
	page, err := svc.List(t.Context(), ListFilter{Integration: "ntaaaaaaaaaaaa", From: &from, To: &to,
		States: []string{StatePending}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	p := store.listed[0]
	if len(page.Snapshots) != 2 || page.Next == nil || *page.Next != (Position{ReceivedAt: t0, ID: 2}) ||
		page.Snapshots[0].Integration != (Ref{PublicID: "NTAAAAAAAAAAAA", Name: "prod-eu"}) ||
		p.IntegrationID != 5 || !p.NotBefore.Equal(t0.Add(-14*24*time.Hour)) || !p.From.Time.Equal(from) ||
		!p.To.Time.Equal(to) || len(p.States) != 1 || p.PageSize != 3 || p.AfterAt.Valid {
		t.Errorf("page %+v with %+v", page, p)
	}
	page, err = svc.List(t.Context(), ListFilter{Integration: "NTAAAAAAAAAAAA", After: page.Next, Limit: 5})
	p = store.listed[1]
	if err != nil || page.Next != nil || !p.AfterAt.Time.Equal(t0) || p.AfterID.Int64 != 2 || p.States == nil ||
		p.From.Valid || p.To.Valid {
		t.Errorf("page 2 %+v, %v with %+v", page, err, p)
	}
	for _, id := range []string{"NT0000000000ZZ", "bad"} {
		if page, err := svc.List(t.Context(), ListFilter{Integration: id}); err != nil || len(page.Snapshots) != 0 {
			t.Errorf("%s = %+v, %v", id, page, err)
		}
	}
	for _, step := range []string{"FindSnapshotIntegration", "GetRetention", "ListStoredSnapshots"} {
		store.fail[step] = errors.New("down")
		if _, err := svc.List(t.Context(), ListFilter{Integration: "NTAAAAAAAAAAAA"}); err == nil {
			t.Errorf("%s failing: no error", step)
		}
		delete(store.fail, step)
	}
}

// TestGet: the Stored Snapshot with its body, content type, state and error; one past retention or unknown is
// ErrNotFound.
func TestGet(t *testing.T) {
	store := newStore()
	svc := New(orgID, store, clock.NewManual(t0))
	store.snapshot = &dbgen.GetStoredSnapshotRow{ID: 1, PublicID: "SSAAAAAAAAAAAA", ReceivedAt: t0, SizeBytes: 8,
		ContentType: pgtype.Text{String: "text/plain", Valid: true}, State: StateFailed,
		ProcessedAt:     pgtype.Timestamptz{Time: t0, Valid: true},
		ProcessingError: pgtype.Text{String: "not JSON", Valid: true}, GroupKey: pgtype.Text{},
		AlertCount: pgtype.Int8{Int64: 2, Valid: true}, Body: []byte("not json"),
		IntegrationPublicID: "NTAAAAAAAAAAAA", IntegrationName: "prod-eu"}
	sn, err := svc.Get(t.Context(), "ssaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if string(sn.Body) != "not json" || *sn.ContentType != "text/plain" || sn.State != StateFailed ||
		*sn.ProcessingError != "not JSON" || sn.GroupKey != nil || *sn.AlertCount != 2 || sn.TruncatedAlerts != nil ||
		sn.ProcessedAt == nil || sn.Integration.Name != "prod-eu" ||
		!store.got[0].NotBefore.Equal(t0.Add(-14*24*time.Hour)) ||
		store.got[0].NotBeforeDay.Time != time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC) {
		t.Errorf("snapshot %+v", sn)
	}
	for _, id := range []string{"SS0000000000ZZ", "bad"} {
		if _, err := svc.Get(t.Context(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s = %v", id, err)
		}
	}
	for _, step := range []string{"GetRetention", "GetStoredSnapshot"} {
		store.fail[step] = errors.New("down")
		if _, err := svc.Get(t.Context(), "SSAAAAAAAAAAAA"); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%s failing = %v", step, err)
		}
		delete(store.fail, step)
	}
}
