// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.yaml.in/yaml/v3"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/heartbeat/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	idb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// fakeStore is the database of the package in memory: the Heartbeats of Integrations by id, the synthetic Stored
// Snapshots of the built-in Integration and the hints. It does not roll back; tests read what was written.
type fakeStore struct {
	mu        sync.Mutex
	rows      map[int64]*dbgen.LockHeartbeatRow
	clocks    map[int64]int64
	internal  []map[string]any
	hints     []db.Hint
	leader    *dbgen.GetLeaderStartRow
	fail      map[string]error
	noBuiltin bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[int64]*dbgen.LockHeartbeatRow{}, clocks: map[int64]int64{}, fail: map[string]error{}}
}

// add adds the Integration id with its Heartbeat in state, last signalled at last when it is set.
func (f *fakeStore) add(id int64, state string, last *time.Time) *dbgen.LockHeartbeatRow {
	r := &dbgen.LockHeartbeatRow{ID: id, PublicID: fmt.Sprintf("NT%012d", id), Name: "hb",
		StaticLabels: []byte(`{"env":"prod","severity":"low"}`), HeartbeatEnabled: state != StateNotConfigured,
		HeartbeatTimeoutSeconds: 300, HeartbeatState: state}
	if last != nil {
		r.HeartbeatLastSignalAt = pgtype.Timestamptz{Time: *last, Valid: true}
	}
	if state == StateLost {
		r.HeartbeatLostSince = r.HeartbeatLastSignalAt
	}
	f.rows[id] = r
	return r
}

func (f *fakeStore) InTx(_ context.Context, fn func(Queries) error) error { return fn(f) }

func (f *fakeStore) LockHeartbeat(_ context.Context, arg dbgen.LockHeartbeatParams) (dbgen.LockHeartbeatRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["LockHeartbeat"]; err != nil {
		return dbgen.LockHeartbeatRow{}, err
	}
	r, ok := f.rows[arg.ID]
	if !ok || arg.OrgID != 1 {
		return dbgen.LockHeartbeatRow{}, pgx.ErrNoRows
	}
	return *r, nil
}

func (f *fakeStore) RecordSignal(_ context.Context, arg dbgen.RecordSignalParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["RecordSignal"]; err != nil {
		return err
	}
	r := f.rows[arg.ID]
	r.HeartbeatState, r.HeartbeatLastSignalAt, r.HeartbeatLostSince = StateLive,
		pgtype.Timestamptz{Time: arg.SignalAt, Valid: true}, pgtype.Timestamptz{}
	f.clocks[arg.ID] += arg.AdvanceMs
	return nil
}

func (f *fakeStore) MarkLost(_ context.Context, arg dbgen.MarkLostParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["MarkLost"]; err != nil {
		return err
	}
	if r := f.rows[arg.ID]; r.HeartbeatState == StateLive {
		r.HeartbeatState, r.HeartbeatLostSince = StateLost, pgtype.Timestamptz{Time: arg.LostSince, Valid: true}
	}
	return nil
}

func (f *fakeStore) ListOverdue(_ context.Context, arg dbgen.ListOverdueParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["ListOverdue"]; err != nil {
		return nil, err
	}
	var out []int64
	for id := int64(1); id <= 9; id++ {
		r, ok := f.rows[id]
		if !ok || r.HeartbeatState != StateLive {
			continue
		}
		start := r.HeartbeatLastSignalAt.Time
		if arg.MeasuredFrom.Valid && arg.MeasuredFrom.Time.After(start) {
			start = arg.MeasuredFrom.Time
		}
		if arg.Now.Sub(start) > time.Duration(r.HeartbeatTimeoutSeconds)*time.Second {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeStore) ListHeartbeats(_ context.Context, orgID int64) ([]dbgen.ListHeartbeatsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["ListHeartbeats"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListHeartbeatsRow
	for id := int64(1); id <= 9; id++ {
		if r, ok := f.rows[id]; ok && r.HeartbeatEnabled && orgID == 1 {
			out = append(out, dbgen.ListHeartbeatsRow{PublicID: r.PublicID, HeartbeatState: r.HeartbeatState})
		}
	}
	return out, nil
}

func (f *fakeStore) GetLeaderStart(context.Context) (dbgen.GetLeaderStartRow, error) {
	if err := f.fail["GetLeaderStart"]; err != nil {
		return dbgen.GetLeaderStartRow{}, err
	}
	if f.leader == nil {
		return dbgen.GetLeaderStartRow{}, pgx.ErrNoRows
	}
	return *f.leader, nil
}

func (f *fakeStore) FindBuiltinIntegration(context.Context, int64) (int64, error) {
	if f.noBuiltin {
		return 0, pgx.ErrNoRows
	}
	return 100, f.fail["FindBuiltinIntegration"]
}

func (f *fakeStore) InsertInternalBody(_ context.Context, arg idb.InsertInternalBodyParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var w map[string]any
	if err := json.Unmarshal(arg.Body, &w); err != nil {
		return err
	}
	f.internal = append(f.internal, w)
	return nil
}

func (f *fakeStore) InsertInternalSnapshot(_ context.Context, arg idb.InsertInternalSnapshotParams) error {
	if arg.IntegrationID != 100 {
		return errors.New("not the built-in integration")
	}
	return nil
}

func (f *fakeStore) NotifyInternalSnapshot(context.Context, idb.NotifyInternalSnapshotParams) error {
	return nil
}

func (f *fakeStore) ListOpenInternalAlerts(context.Context, idb.ListOpenInternalAlertsParams) (
	[]idb.ListOpenInternalAlertsRow, error) {
	return nil, nil
}

func (f *fakeStore) ListPendingInternalRaises(context.Context, int64) ([][]byte, error) {
	return nil, nil
}

func (f *fakeStore) Notify(_ context.Context, h db.Hint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["Notify"]; err != nil {
		return err
	}
	f.hints = append(f.hints, h)
	return nil
}

// alert is the only Alert of the n-th synthetic Snapshot: its status and labels.
func (f *fakeStore) alert(t *testing.T, n int) (string, map[string]any) {
	t.Helper()
	if len(f.internal) <= n {
		t.Fatalf("%d internal snapshots, want more than %d", len(f.internal), n)
	}
	a := f.internal[n]["alerts"].([]any)[0].(map[string]any)
	return a["status"].(string), a["labels"].(map[string]any)
}

func at(t time.Time) *time.Time { return &t }

func lines(t *testing.T, log *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(log.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func newService(store *fakeStore, c clock.Clock, log *bytes.Buffer) *Service {
	return New(Config{OrgID: 1, Store: store, Business: c, Log: logging.New(log, logging.LevelInfo)})
}

// TestSignalStep covers each transition of a signal and the gap rule of the liveness clock (C-07.FR-3, schema.md
// 4.7).
func TestSignalStep(t *testing.T) {
	timeout := 5 * time.Minute
	for name, tt := range map[string]struct {
		state string
		last  *time.Time
		now   time.Time
		want  step
	}{
		"waiting: live, the clock stands": {state: StateWaiting, now: t0, want: step{at: t0, first: true}},
		"live: a gap within the timeout counts": {state: StateLive, last: at(t0), now: t0.Add(time.Minute),
			want: step{at: t0.Add(time.Minute), advanceMs: 60_000}},
		"live: a gap of the timeout counts": {state: StateLive, last: at(t0), now: t0.Add(timeout),
			want: step{at: t0.Add(timeout), advanceMs: timeout.Milliseconds()}},
		"live: a longer gap does not count": {state: StateLive, last: at(t0), now: t0.Add(timeout + time.Second),
			want: step{at: t0.Add(timeout + time.Second)}},
		"lost: live, the clock stands": {state: StateLost, last: at(t0), now: t0.Add(10 * time.Minute),
			want: step{at: t0.Add(10 * time.Minute), recovered: true}},
		"a late commit never moves the last signal back": {state: StateLive, last: at(t0.Add(time.Second)), now: t0,
			want: step{at: t0.Add(time.Second)}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := signalStep(tt.state, tt.last, timeout, tt.now); got != tt.want {
				t.Errorf("signalStep = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestSignal walks one Heartbeat through its states with a manual clock: an Integration whose Heartbeat is off
// records nothing; the first signal makes it live with a hint and a log line, without advancing the clock; a signal
// within the timeout advances the clock by the gap, quietly; a signal that ends a loss resolves MusterHeartbeatLost.
func TestSignal(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	c := clock.NewManual(t0)
	var log bytes.Buffer
	s := newService(store, c, &log)

	store.add(1, StateNotConfigured, nil)
	store.add(2, StateWaiting, nil).Builtin = true
	if err := s.Signal(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Signal(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Signal(ctx, 9); err != nil {
		t.Fatalf("a deleted integration: %v", err)
	}
	if store.rows[1].HeartbeatState != StateNotConfigured || store.rows[1].HeartbeatLastSignalAt.Valid ||
		store.rows[2].HeartbeatState != StateWaiting || len(store.hints) != 0 || log.Len() != 0 {
		t.Fatalf("an integration without a heartbeat recorded a signal: %+v %+v %v", store.rows[1], store.rows[2],
			store.hints)
	}

	store.add(3, StateWaiting, nil)
	if err := s.Signal(ctx, 3); err != nil {
		t.Fatal(err)
	}
	r := store.rows[3]
	if r.HeartbeatState != StateLive || !r.HeartbeatLastSignalAt.Time.Equal(t0) || store.clocks[3] != 0 ||
		len(store.hints) != 1 || store.hints[0] != (db.Hint{OrgID: 1, Type: integrations.Hint, ID: r.PublicID}) ||
		len(store.internal) != 0 {
		t.Fatalf("first signal: %+v clock %d hints %v internal %v", r, store.clocks[3], store.hints, store.internal)
	}
	if l := lines(t, &log); len(l) != 1 || l[0]["event"] != "heartbeat_live" || l[0]["first"] != true ||
		l[0]["integration"] != r.PublicID {
		t.Errorf("log %v", l)
	}

	c.Advance(time.Minute)
	if err := s.Signal(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if store.clocks[3] != 60_000 || len(store.hints) != 1 || len(lines(t, &log)) != 1 {
		t.Errorf("a live signal: clock %d, hints %v", store.clocks[3], store.hints)
	}

	store.rows[3].HeartbeatState, store.rows[3].HeartbeatLostSince = StateLost, store.rows[3].HeartbeatLastSignalAt
	c.Advance(10 * time.Minute)
	if err := s.Signal(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if r := store.rows[3]; r.HeartbeatState != StateLive || r.HeartbeatLostSince.Valid || store.clocks[3] != 60_000 ||
		len(store.hints) != 2 {
		t.Fatalf("a signal after a loss: %+v clock %d", r, store.clocks[3])
	}
	status, labels := store.alert(t, 0)
	if status != "resolved" || labels["alertname"] != "MusterHeartbeatLost" || labels["integration"] != r.PublicID {
		t.Errorf("resolve %s %v", status, labels)
	}
	if l := lines(t, &log); len(l) != 2 || l[1]["event"] != "heartbeat_live" || l[1]["first"] != false {
		t.Errorf("log %v", l)
	}
}

// TestSignalErrors: a failed lock, write, resolve or hint fails the signal, logged by nobody here.
func TestSignalErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, name := range []string{"LockHeartbeat", "RecordSignal", "FindBuiltinIntegration", "Notify"} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			store.add(1, StateLost, at(t0))
			store.fail[name] = boom
			var log bytes.Buffer
			if err := newService(store, clock.NewManual(t0.Add(time.Hour)), &log).Signal(t.Context(), 1); !errors.Is(err,
				boom) {
				t.Errorf("Signal = %v", err)
			}
			if log.Len() != 0 {
				t.Errorf("a failed signal was logged: %s", log.String())
			}
		})
	}
}

// fakeAuth accepts the token "mstr_int_good" of the Integration 7, knows the revoked "mstr_int_revoked" and fails
// on "mstr_int_down".
type fakeAuth struct{ seen []string }

func (a *fakeAuth) Authenticate(_ context.Context, value string) (integrations.Caller, error) {
	a.seen = append(a.seen, value)
	switch value {
	case "mstr_int_good":
		return integrations.Caller{TokenID: 1, IntegrationID: 7, Integration: "NTAAAAAAAAAAAA"}, nil
	case "mstr_int_revoked":
		return integrations.Caller{Integration: "NTAAAAAAAAAAAA"}, integrations.ErrInvalidToken
	case "mstr_int_down":
		return integrations.Caller{Integration: integrations.Unknown}, errors.New("the database is unavailable")
	}
	return integrations.Caller{Integration: integrations.Unknown}, integrations.ErrInvalidToken
}

type fakeSignals struct {
	ids  []int64
	fail error
}

func (s *fakeSignals) Signal(_ context.Context, id int64) error {
	if s.fail != nil {
		return s.fail
	}
	s.ids = append(s.ids, id)
	return nil
}

// TestHandler covers C-07.FR-1 and C-07.AC-3 on the handler: GET and POST with the token as Authorization: Bearer or
// in the path answer 204 whatever the body; a wrong, revoked or API token answers 401; a failure 500; any other method
// or path 404. No log line carries a token.
func TestHandler(t *testing.T) {
	auth, signals := &fakeAuth{}, &fakeSignals{}
	var log bytes.Buffer
	h := NewHandler(HandlerConfig{Auth: auth, Signals: signals, Log: logging.New(&log, logging.LevelInfo),
		BodyLimit: 8})
	do := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tt := range []struct {
		method, path, bearer, body string
		status                     int
	}{
		{http.MethodGet, "/api/v1/heartbeat", "mstr_int_good", "", http.StatusNoContent},
		{http.MethodPost, "/api/v1/heartbeat", "mstr_int_good", strings.Repeat("x", 100), http.StatusNoContent},
		{http.MethodGet, "/api/v1/heartbeat/mstr_int_good", "", "", http.StatusNoContent},
		{http.MethodPost, "/api/v1/heartbeat/mstr_int_good", "", "anything", http.StatusNoContent},
		{http.MethodPost, "/api/v1/heartbeat", "mstr_int_wrong", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/heartbeat/mstr_int_revoked", "", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/heartbeat", "mstr_pat_secretsecret", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/heartbeat", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/heartbeat/mstr_int_down", "", "", http.StatusInternalServerError},
		{http.MethodPut, "/api/v1/heartbeat", "mstr_int_good", "", http.StatusNotFound},
		{http.MethodGet, "/api/v1/heartbeat/a/b", "", "", http.StatusNotFound},
	} {
		w := do(tt.method, tt.path, tt.bearer, tt.body)
		if w.Code != tt.status {
			t.Errorf("%s %s = %d %s, want %d", tt.method, tt.path, w.Code, w.Body, tt.status)
		}
		if tt.status == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s %s: no WWW-Authenticate", tt.method, tt.path)
		}
	}
	if len(signals.ids) != 4 || signals.ids[0] != 7 {
		t.Errorf("signals %v", signals.ids)
	}
	signals.fail = errors.New("the write failed")
	if w := do(http.MethodPost, "/api/v1/heartbeat/mstr_int_good", "", ""); w.Code != http.StatusInternalServerError {
		t.Errorf("a failed write = %d", w.Code)
	}
	l := lines(t, &log)
	if len(l) != 6 {
		t.Fatalf("%d lines: %s", len(l), log.String())
	}
	for _, line := range l {
		if p := line["route_pattern"]; p != RouteHeader && p != RoutePath {
			t.Errorf("route_pattern %v", line)
		}
	}
	if strings.Contains(log.String(), "mstr_") {
		t.Errorf("a token reached the log: %s", log.String())
	}
}

// TestSnippet covers C-07.FR-6: the always-firing rule, the route with snippet.heartbeat_repeat_interval and the
// receiver pointing at the Heartbeat URL with the token, without resolves; it is valid YAML.
func TestSnippet(t *testing.T) {
	s := Snippet(`prod: "eu"`, "https://ingest.example.org/api/v1/heartbeat", "mstr_int_token")
	var doc struct {
		Groups []struct {
			Rules []struct {
				Alert string `yaml:"alert"`
				Expr  string `yaml:"expr"`
			} `yaml:"rules"`
		} `yaml:"groups"`
		Route struct {
			Routes []struct {
				Receiver       string   `yaml:"receiver"`
				Matchers       []string `yaml:"matchers"`
				Continue       bool     `yaml:"continue"`
				GroupWait      string   `yaml:"group_wait"`
				GroupInterval  string   `yaml:"group_interval"`
				RepeatInterval string   `yaml:"repeat_interval"`
			} `yaml:"routes"`
		} `yaml:"route"`
		Receivers []struct {
			Name           string `yaml:"name"`
			WebhookConfigs []struct {
				URL          string `yaml:"url"`
				SendResolved bool   `yaml:"send_resolved"`
				HTTPConfig   struct {
					Authorization struct {
						Type        string `yaml:"type"`
						Credentials string `yaml:"credentials"`
					} `yaml:"authorization"`
				} `yaml:"http_config"`
			} `yaml:"webhook_configs"`
		} `yaml:"receivers"`
	}
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatalf("%v\n%s", err, s)
	}
	rule, route, receiver := doc.Groups[0].Rules[0], doc.Route.Routes[0], doc.Receivers[0]
	hook := receiver.WebhookConfigs[0]
	if rule.Alert != "MusterHeartbeat" || rule.Expr != "vector(1)" || route.Receiver != `muster-heartbeat-prod: "eu"` ||
		route.Matchers[0] != `alertname="MusterHeartbeat"` || !route.Continue || route.GroupWait != "0s" ||
		route.GroupInterval != "1m" || route.RepeatInterval != "1m" || receiver.Name != route.Receiver ||
		hook.URL != "https://ingest.example.org/api/v1/heartbeat" || hook.SendResolved ||
		hook.HTTPConfig.Authorization.Type != "Bearer" || hook.HTTPConfig.Authorization.Credentials != "mstr_int_token" {
		t.Errorf("snippet %+v\n%s", doc, s)
	}
	if !strings.Contains(s, "Watchdog") {
		t.Errorf("no note on Watchdog:\n%s", s)
	}
}
