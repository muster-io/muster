// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// fakeProcess is the database of processing in memory: pending Stored Snapshots, the Alerts and the calls that
// write. It does not roll back; tests read the calls.
type fakeProcess struct {
	ProcessQueries
	mu        sync.Mutex
	pending   []dbgen.NextPendingSnapshotRow
	finished  []dbgen.FinishSnapshotParams
	counted   int
	alerts    map[string]*dbgen.ListSnapshotAlertsRow
	nextID    int64
	inserted  [][]insertRow
	updated   [][]updateRow
	listed    []dbgen.UpsertListedPresencesParams
	missed    []dbgen.MarkPresencesMissedParams
	ended     []dbgen.EndPresencesParams
	groups    []dbgen.UpdateAlertmanagerGroupParams
	learned   []dbgen.UpdateRepeatIntervalParams
	released  []int64
	claimable []int64
	lost      bool
	fail      map[string]error
	static    string
	// refusePayload refuses to mark a Snapshot with what processing read of its payload, like a value PostgreSQL
	// cannot store.
	refusePayload bool
}

func newFakeProcess() *fakeProcess {
	return &fakeProcess{alerts: map[string]*dbgen.ListSnapshotAlertsRow{}, fail: map[string]error{},
		static: `{"cluster":"b"}`, claimable: []int64{5}}
}

func (f *fakeProcess) err(name string) error { return f.fail[name] }

func (f *fakeProcess) InTx(_ context.Context, fn func(ProcessQueries, dbgen.DBTX) error) error {
	return fn(f, nil)
}

func (f *fakeProcess) ClaimIntegrations(_ context.Context, l db.Lease, orgID int64, ids []int64,
	limit int32) ([]Claimed, error) {
	if err := f.err("ClaimIntegrations"); err != nil {
		return nil, err
	}
	var out []Claimed
	for _, id := range ids {
		if slices.Contains(f.claimable, id) && len(out) < int(limit) && orgID == 1 && l.Owner != "" {
			out = append(out, Claimed{ID: id, PublicID: fmt.Sprintf("NT%012d", id)})
		}
	}
	return out, nil
}

func (f *fakeProcess) GetRetention(context.Context, int64) (int64, error) {
	return 14, f.err("GetRetention")
}

func (f *fakeProcess) ListPendingIntegrations(context.Context, dbgen.ListPendingIntegrationsParams) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) == 0 {
		return nil, f.err("ListPendingIntegrations")
	}
	return []int64{5, 6}, f.err("ListPendingIntegrations")
}

func (f *fakeProcess) EnsureIngestClaims(context.Context, dbgen.EnsureIngestClaimsParams) error {
	return f.err("EnsureIngestClaims")
}

func (f *fakeProcess) ReleaseIngestClaim(_ context.Context, arg dbgen.ReleaseIngestClaimParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, arg.IntegrationID)
	return f.err("ReleaseIngestClaim")
}

func (f *fakeProcess) RenewIngestLease(_ context.Context, arg dbgen.RenewIngestLeaseParams) (
	dbgen.RenewIngestLeaseRow, error) {
	if f.lost {
		return dbgen.RenewIngestLeaseRow{}, pgx.ErrNoRows
	}
	if !arg.Owner.Valid || !arg.LeaseUntil.Valid {
		return dbgen.RenewIngestLeaseRow{}, errors.New("no owner")
	}
	return dbgen.RenewIngestLeaseRow{PublicID: "NTAAAAAAAAAAAA", StaticLabels: []byte(f.static),
		DuplicateWindowSeconds: 45, LivenessClockMs: 9}, f.err("RenewIngestLease")
}

func (f *fakeProcess) NextPendingSnapshot(context.Context, dbgen.NextPendingSnapshotParams) (
	dbgen.NextPendingSnapshotRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("NextPendingSnapshot"); err != nil {
		return dbgen.NextPendingSnapshotRow{}, err
	}
	if len(f.pending) == 0 {
		return dbgen.NextPendingSnapshotRow{}, pgx.ErrNoRows
	}
	return f.pending[0], nil
}

func (f *fakeProcess) FinishSnapshot(_ context.Context, arg dbgen.FinishSnapshotParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err("FinishSnapshot"); err != nil {
		return 0, err
	}
	if f.refusePayload && arg.GroupKey.Valid {
		return 0, &pgconn.PgError{Code: "22021", Message: "invalid byte sequence"}
	}
	if len(f.pending) == 0 || f.pending[0].ID != arg.ID {
		return 0, nil
	}
	f.pending = f.pending[1:]
	f.finished = append(f.finished, arg)
	return 1, nil
}

func (f *fakeProcess) CountSnapshot(context.Context, dbgen.CountSnapshotParams) error {
	f.counted++
	return f.err("CountSnapshot")
}

func (f *fakeProcess) UpsertAlertmanagerRoute(context.Context, dbgen.UpsertAlertmanagerRouteParams) (
	dbgen.UpsertAlertmanagerRouteRow, error) {
	return dbgen.UpsertAlertmanagerRouteRow{ID: 3, LearnedRepeatIntervalMs: pgtype.Int8{Int64: 300000, Valid: true},
		RecentRepeatGapsMs: []int64{300000}, RepeatObservations: 1}, f.err("UpsertAlertmanagerRoute")
}

func (f *fakeProcess) UpsertAlertmanagerGroup(context.Context, dbgen.UpsertAlertmanagerGroupParams) (
	dbgen.UpsertAlertmanagerGroupRow, error) {
	row := dbgen.UpsertAlertmanagerGroupRow{ID: 4}
	if n := len(f.groups); n > 0 {
		last := f.groups[n-1]
		row.WindowSeq, row.WindowStartedAt, row.LastContentSha256 = last.WindowSeq, last.WindowStartedAt,
			last.LastContentSha256
	}
	return row, f.err("UpsertAlertmanagerGroup")
}

func (f *fakeProcess) UpdateAlertmanagerGroup(_ context.Context, arg dbgen.UpdateAlertmanagerGroupParams) error {
	f.groups = append(f.groups, arg)
	return f.err("UpdateAlertmanagerGroup")
}

func (f *fakeProcess) UpdateRepeatInterval(_ context.Context, arg dbgen.UpdateRepeatIntervalParams) error {
	f.learned = append(f.learned, arg)
	return f.err("UpdateRepeatInterval")
}

func (f *fakeProcess) ListSnapshotAlerts(_ context.Context, arg dbgen.ListSnapshotAlertsParams) (
	[]dbgen.ListSnapshotAlertsRow, error) {
	var out []dbgen.ListSnapshotAlertsRow
	for _, fp := range arg.Fingerprints {
		if a := f.alerts[fp]; a != nil {
			out = append(out, *a)
		}
	}
	return out, f.err("ListSnapshotAlerts")
}

func (f *fakeProcess) ListActivePresences(context.Context, dbgen.ListActivePresencesParams) (
	[]dbgen.ListActivePresencesRow, error) {
	return nil, f.err("ListActivePresences")
}

func (f *fakeProcess) InsertAlerts(_ context.Context, arg dbgen.InsertAlertsParams) ([]dbgen.InsertAlertsRow, error) {
	if err := f.err("InsertAlerts"); err != nil {
		return nil, err
	}
	var rows []insertRow
	if err := json.Unmarshal(arg.Rows, &rows); err != nil {
		return nil, err
	}
	f.inserted = append(f.inserted, rows)
	var out []dbgen.InsertAlertsRow
	for _, r := range rows {
		f.nextID++
		labels, _ := json.Marshal(r.Labels)
		annotations, _ := json.Marshal(r.Annotations)
		f.alerts[r.Fingerprint] = &dbgen.ListSnapshotAlertsRow{ID: f.nextID, Fingerprint: r.Fingerprint,
			Labels: labels, Annotations: annotations, StaticLabelConflicts: r.Conflicts, Status: StatusFiring,
			StartsAt: r.StartsAt, Episode: 1, FiredAt: arg.SeenAt, FirstSeenAt: arg.SeenAt, LastSeenAt: arg.SeenAt}
		out = append(out, dbgen.InsertAlertsRow{ID: f.nextID, Fingerprint: r.Fingerprint})
	}
	return out, nil
}

func (f *fakeProcess) UpdateAlerts(_ context.Context, arg dbgen.UpdateAlertsParams) error {
	if err := f.err("UpdateAlerts"); err != nil {
		return err
	}
	var rows []updateRow
	if err := json.Unmarshal(arg.Rows, &rows); err != nil {
		return err
	}
	f.updated = append(f.updated, rows)
	for _, r := range rows {
		for _, a := range f.alerts {
			if a.ID == r.ID {
				a.Status, a.StartsAt, a.Episode, a.LastSeenAt = r.Status, r.StartsAt, r.Episode, r.LastSeenAt
				a.ResolvedAt = timestamptz(r.ResolvedAt)
				a.ResolveReason = pgtype.Text{}
				if r.Reason != nil {
					a.ResolveReason = pgtype.Text{String: *r.Reason, Valid: true}
				}
			}
		}
	}
	return nil
}

func (f *fakeProcess) UpsertListedPresences(_ context.Context, arg dbgen.UpsertListedPresencesParams) error {
	f.listed = append(f.listed, arg)
	return f.err("UpsertListedPresences")
}

func (f *fakeProcess) MarkPresencesMissed(_ context.Context, arg dbgen.MarkPresencesMissedParams) error {
	f.missed = append(f.missed, arg)
	return f.err("MarkPresencesMissed")
}

func (f *fakeProcess) EndPresences(_ context.Context, arg dbgen.EndPresencesParams) error {
	f.ended = append(f.ended, arg)
	return f.err("EndPresences")
}

func (f *fakeProcess) CountPendingSnapshots(_ context.Context, orgID int64) (int64, error) {
	return orgID * 10, f.err("CountPendingSnapshots")
}

func (f *fakeProcess) add(id int64, at time.Time, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, dbgen.NextPendingSnapshotRow{ID: id, PublicID: fmt.Sprintf("SS%012d", id),
		ReceivedAt: at, Body: []byte(body)})
}

type recordingSink struct {
	changes []AlertChange
	err     error
}

func (s *recordingSink) AlertChanges(_ context.Context, _ dbgen.DBTX, changes []AlertChange) error {
	s.changes = append(s.changes, changes...)
	return s.err
}

func newTestProcessor(store ProcessStore, business clock.Clock, log *bytes.Buffer, sink Sink) *Processor {
	clocks := clock.Clocks{Business: business, Real: clock.Real{}}
	return NewProcessor(ProcessorConfig{OrgID: 1, Store: store, Business: business, Log: logging.New(log, logging.LevelInfo),
		Lease: db.Lease{Owner: "replica-a", Duration: Lease, Clocks: clocks}, Sink: sink})
}

func body(alerts ...string) string {
	return `{"version":"4","groupKey":"{}/{team=\"db\"}:{alertname=\"DiskFull\"}","status":"firing",` +
		`"notification_reason":"first notification","alerts":[` + strings.Join(alerts, ",") + `]}`
}

func wireAlertOf(name, status string) string {
	return `{"status":"` + status + `","labels":{"alertname":"DiskFull","instance":"` + name +
		`","cluster":"a"},"annotations":{"summary":"` + name + `"},"startsAt":"2026-10-06T11:00:00Z",` +
		`"endsAt":"0001-01-01T00:00:00Z","fingerprint":"` + name + `"}`
}

func events(t *testing.T, log *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(log.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %s: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// TestProcessPending covers the transaction of a Snapshot through the store: a processed Snapshot with new Alerts,
// one that resolves, a body that is not JSON, which fails and does not block the next, the counters, the log lines and
// the Sink.
func TestProcessPending(t *testing.T) {
	store := newFakeProcess()
	c := clock.NewManual(t0.Add(2 * time.Second))
	var log bytes.Buffer
	sink := &recordingSink{}
	p := newTestProcessor(store, c, &log, sink)
	integration := "NTAAAAAAAAAAAA"
	failed0 := metrics.IngestFailedSnapshots.With(integration).Get()
	resolved0 := metrics.AlertsResolved.With(integration, ResolveResolved).Get()
	dropped0 := metrics.IngestResolvedDropped.With(integration).Get()

	store.add(10, t0, body(wireAlertOf("db-a", "firing"), wireAlertOf("db-b", "firing")))
	store.add(11, t0.Add(time.Second), "not json")
	store.add(12, t0.Add(2*time.Second), body(wireAlertOf("db-a", "resolved"), wireAlertOf("db-b", "firing"),
		wireAlertOf("ghost", "resolved")))
	n, err := p.ProcessPending(t.Context(), 5)
	if err != nil || n != 3 {
		t.Fatalf("ProcessPending = %d, %v", n, err)
	}
	if len(store.finished) != 3 || store.counted != 3 {
		t.Fatalf("finished %+v, counted %d", store.finished, store.counted)
	}
	first, bad, third := store.finished[0], store.finished[1], store.finished[2]
	if first.State != StateProcessed || first.GroupKey.String != `{}/{team="db"}:{alertname="DiskFull"}` ||
		first.AlertCount.Int64 != 2 || !first.TruncatedAlerts.Valid || first.ProcessingError.Valid ||
		!first.ProcessedAt.Time.Equal(t0.Add(2*time.Second)) {
		t.Errorf("first = %+v", first)
	}
	if bad.State != StateFailed || bad.GroupKey.Valid || bad.ProcessingError.String !=
		"the body is not valid JSON: invalid character 'o' in literal null (expecting 'u')" {
		t.Errorf("bad = %+v", bad)
	}
	if third.State != StateProcessed || third.AlertCount.Int64 != 3 {
		t.Errorf("third = %+v", third)
	}
	if len(store.inserted) != 1 || len(store.inserted[0]) != 2 || store.inserted[0][0].Labels["cluster"] != "a" ||
		!slices.Equal(store.inserted[0][0].Conflicts, []string{"cluster"}) {
		t.Errorf("inserted %+v", store.inserted)
	}
	if len(store.listed) != 2 || len(store.listed[0].AlertIds) != 2 || store.listed[0].ClockMs != 9 ||
		len(store.listed[1].AlertIds) != 1 {
		t.Errorf("listed %+v", store.listed)
	}
	if len(store.ended) != 1 || !slices.Equal(store.ended[0].AlertIds, []int64{1}) || store.ended[0].AlertmanagerGroupID.Valid {
		t.Errorf("ended %+v", store.ended)
	}
	if a := store.alerts["db-a"]; a.Status != StatusResolved || a.ResolveReason.String != ResolveResolved {
		t.Errorf("db-a = %+v", a)
	}
	if got := metrics.IngestFailedSnapshots.With(integration).Get() - failed0; got != 1 {
		t.Errorf("failed snapshots +%d", got)
	}
	if got := metrics.AlertsResolved.With(integration, ResolveResolved).Get() - resolved0; got != 1 {
		t.Errorf("resolved +%d", got)
	}
	if got := metrics.IngestResolvedDropped.With(integration).Get() - dropped0; got != 1 {
		t.Errorf("dropped +%d", got)
	}
	var kinds []string
	for _, c := range sink.changes {
		kinds = append(kinds, string(c.Kind)+":"+c.Fingerprint)
		if c.AlertID == 0 || c.StoredSnapshotID == 0 {
			t.Errorf("change %+v", c)
		}
	}
	if !slices.Equal(kinds, []string{"fired:db-a", "fired:db-b", "resolved:db-a"}) {
		t.Errorf("sink %v", kinds)
	}
	var names []string
	for _, e := range events(t, &log) {
		names = append(names, e["event"].(string))
		if e["event"] == "snapshot_processed" && e["stored_snapshot"] == "SS000000000012" &&
			(e["alerts"] != 3.0 || e["resolved"] != 1.0 || e["dropped"] != 1.0 || e["integration"] != integration) {
			t.Errorf("line %v", e)
		}
	}
	if !slices.Equal(names, []string{"snapshot_processed", "snapshot_failed", "snapshot_processed"}) {
		t.Errorf("events %v", names)
	}
}

// TestProcessErrors: a lost connection or a transient server error leaves the Snapshot pending; another server
// error, an error of processing or of the Sink marks it failed; a lost lease stops without an error.
func TestProcessErrors(t *testing.T) {
	ok := body(wireAlertOf("db-a", "firing"))
	for _, tt := range []struct {
		name    string
		setup   func(*fakeProcess, *recordingSink)
		pending bool
		state   string
		wantErr bool
	}{
		{"lost connection", func(f *fakeProcess, _ *recordingSink) {
			f.fail["ListSnapshotAlerts"] = errors.New("conn closed")
		}, true, "", true},
		{"serialization failure", func(f *fakeProcess, _ *recordingSink) {
			f.fail["UpdateAlertmanagerGroup"] = &pgconn.PgError{Code: "40001"}
		}, true, "", true},
		{"constraint violation", func(f *fakeProcess, _ *recordingSink) {
			f.fail["InsertAlerts"] = &pgconn.PgError{Code: "23505", Message: "duplicate key"}
		}, false, StateFailed, false},
		{"bad static labels", func(f *fakeProcess, _ *recordingSink) { f.static = `[]` }, false, StateFailed, false},
		{"lost lease", func(f *fakeProcess, _ *recordingSink) { f.lost = true }, true, "", false},
		{"pending read fails", func(f *fakeProcess, _ *recordingSink) {
			f.fail["NextPendingSnapshot"] = errors.New("conn closed")
		}, true, "", true},
		{"retention read fails", func(f *fakeProcess, _ *recordingSink) {
			f.fail["GetRetention"] = errors.New("conn closed")
		}, true, "", true},
		{"the payload cannot be stored", func(f *fakeProcess, _ *recordingSink) {
			f.refusePayload = true
		}, false, StateFailed, false},
		{"marking failed fails", func(f *fakeProcess, _ *recordingSink) {
			f.fail["UpsertListedPresences"] = &pgconn.PgError{Code: "22001"}
			f.fail["CountSnapshot"] = errors.New("conn closed")
		}, false, StateFailed, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeProcess()
			sink := &recordingSink{}
			tt.setup(store, sink)
			var log bytes.Buffer
			p := newTestProcessor(store, clock.NewManual(t0), &log, sink)
			store.add(10, t0, ok)
			n, err := p.ProcessPending(t.Context(), 5)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v", err)
			}
			if tt.pending && (len(store.pending) != 1 || n != 0) {
				t.Errorf("not pending: %d processed, %+v", n, store.finished)
			}
			if tt.state != "" && (len(store.finished) != 1 || store.finished[0].State != tt.state) {
				t.Errorf("finished %+v", store.finished)
			}
		})
	}
}

func TestTransient(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tt := range []struct {
		ctx  context.Context
		err  error
		want bool
	}{
		{t.Context(), errors.New("conn closed"), true},
		{canceled, &PayloadError{msg: "x"}, true},
		{t.Context(), &PayloadError{msg: "x"}, false},
		{t.Context(), processingError(errors.New("x")), false},
		{t.Context(), fmt.Errorf("wrap: %w", &pgconn.PgError{Code: "08006"}), true},
		{t.Context(), &pgconn.PgError{Code: "57P01"}, true},
		{t.Context(), &pgconn.PgError{Code: "53300"}, true},
		{t.Context(), &pgconn.PgError{Code: "23505"}, false},
		{t.Context(), &pgconn.PgError{Code: "22P02"}, false},
	} {
		if got := transient(tt.ctx, tt.err); got != tt.want {
			t.Errorf("transient(%v) = %v", tt.err, got)
		}
	}
	e := processingError(errors.New("inner"))
	if e.Error() != "inner" || errors.Unwrap(e).Error() != "inner" {
		t.Error("processingErr")
	}
}

func TestErrorText(t *testing.T) {
	if errorText(errors.New("short")) != "short" {
		t.Error("short")
	}
	if got := errorText(errors.New("a\x00b\xffc")); got != `a\x00b`+"\uFFFD"+"c" {
		t.Errorf("NUL and invalid UTF-8: %q", got)
	}
	long := strings.Repeat("é", maxErrorLength)
	got := errorText(errors.New(long))
	if len(got) > maxErrorLength+len("…") || !strings.HasSuffix(got, "…") || !strings.HasPrefix(got, "é") {
		t.Errorf("long error cut to %d bytes", len(got))
	}
}

func TestBacklog(t *testing.T) {
	store := newFakeProcess()
	if err := Backlog(t.Context(), store, []int64{1, 2}); err != nil || metrics.IngestBacklog.With().Get() != 30 {
		t.Errorf("backlog %v, %v", metrics.IngestBacklog.With().Get(), err)
	}
	store.fail["CountPendingSnapshots"] = errors.New("down")
	if err := Backlog(t.Context(), store, []int64{1}); err == nil {
		t.Error("no error")
	}
}

// TestDrain claims the Integrations with pending Snapshots, processes them and releases them.
func TestDrain(t *testing.T) {
	store := newFakeProcess()
	var log bytes.Buffer
	p := newTestProcessor(store, clock.NewManual(t0), &log, nil)
	if n, err := p.Drain(t.Context()); n != 0 || err != nil {
		t.Errorf("nothing pending: %d, %v", n, err)
	}
	store.add(10, t0, body(wireAlertOf("db-a", "firing")))
	store.add(11, t0.Add(time.Minute), body(wireAlertOf("db-a", "firing")))
	if n, err := p.Drain(t.Context()); n != 2 || err != nil || !slices.Equal(store.released, []int64{5}) {
		t.Errorf("Drain = %d, %v, released %v", n, err, store.released)
	}
	for _, name := range []string{"GetRetention", "ListPendingIntegrations", "EnsureIngestClaims", "ClaimIntegrations"} {
		store := newFakeProcess()
		store.add(10, t0, body())
		store.fail[name] = errors.New("down")
		if _, err := newTestProcessor(store, clock.NewManual(t0), &log, nil).Drain(t.Context()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	store = newFakeProcess()
	store.add(10, t0, body())
	store.fail["ReleaseIngestClaim"] = errors.New("down")
	if _, err := newTestProcessor(store, clock.NewManual(t0), &log, nil).Drain(t.Context()); err == nil {
		t.Error("a failed release: no error")
	}
}

// TestWorker runs the worker's rounds: a wake processes the pending Snapshots at once, a failed round is logged and
// retried, and the end of ctx stops it after the Integrations in processing released their leases.
func TestWorker(t *testing.T) {
	store := newFakeProcess()
	var log syncBuffer
	p := newTestProcessor(store, clock.NewManual(t0), &bytes.Buffer{}, nil)
	orgsErr := errors.New("organizations down")
	var calls int
	var mu sync.Mutex
	w := &Worker{
		Organizations: func(context.Context) ([]int64, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return nil, orgsErr
			}
			return []int64{1, 2}, nil
		},
		Processor: func(orgID int64) (*Processor, bool) { return p, orgID == 1 },
		Log:       logging.New(&log, logging.LevelInfo),
		Poll:      10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	store.add(10, t0, body(wireAlertOf("db-a", "firing")))
	w.Wake()
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		n := len(store.finished)
		store.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the worker did not process the snapshot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	store.mu.Lock()
	defer store.mu.Unlock()
	if !slices.Contains(store.released, 5) || w.holds(1, 5) {
		t.Errorf("released %v", store.released)
	}
	if !strings.Contains(log.String(), `"event":"snapshot_processing_interrupted"`) ||
		!strings.Contains(log.String(), "organizations down") {
		t.Errorf("log %s", log.String())
	}
}

// TestWorkerLogsFailedIntegration: an Integration whose processing fails is logged with its public_id and released.
func TestWorkerLogsFailedIntegration(t *testing.T) {
	store := newFakeProcess()
	store.add(10, t0, body(wireAlertOf("db-a", "firing")))
	store.fail["UpsertAlertmanagerRoute"] = errors.New("conn closed")
	store.fail["ReleaseIngestClaim"] = errors.New("conn closed")
	var log syncBuffer
	w := &Worker{Log: logging.New(&log, logging.LevelInfo)}
	w.init()
	w.hold(1, 5, true)
	w.process(t.Context(), newTestProcessor(store, clock.NewManual(t0), &bytes.Buffer{}, nil), 1,
		Claimed{ID: 5, PublicID: "NTBBBBBBBBBBBB"})
	if w.holds(1, 5) || strings.Count(log.String(), `"integration":"NTBBBBBBBBBBBB"`) != 2 {
		t.Errorf("log %s", log.String())
	}
	// The failed Integration waits a growing pause before it is claimed again; a success forgets the failures.
	if !w.skips(1, 5) || w.skips(1, 6) {
		t.Error("the failed integration is not paused")
	}
	w.settle(1, 5, true)
	if r := w.retry[[2]int64{1, 5}]; r.failures != 2 || time.Until(r.notBefore) < Poll/4 {
		t.Errorf("retry %+v", r)
	}
	w.settle(1, 5, false)
	if w.skips(1, 5) {
		t.Error("a success kept the pause")
	}
	select {
	case <-w.wake:
		t.Error("a failed integration woke the worker")
	default:
	}
}

// syncBuffer is a bytes.Buffer safe for the worker's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
