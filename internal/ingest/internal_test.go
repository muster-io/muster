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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/internalalerts"
	idb "github.com/muster-io/muster/internal/internalalerts/dbgen"
	"github.com/muster-io/muster/internal/metrics"
)

// internalSim applies synthetic Snapshots of one groupKey through the engine and keeps the Alerts they made.
type internalSim struct {
	group  *group
	route  *route
	alerts map[string]*alert
	next   int64
}

func newInternalSim() *internalSim {
	return &internalSim{group: &group{ID: 1}, route: &route{ID: 2}, alerts: map[string]*alert{}}
}

func (s *internalSim) send(at time.Time, alerts ...PayloadAlert) *engine {
	var rows []*alert
	for _, a := range s.alerts {
		rows = append(rows, a)
	}
	status := StatusFiring
	if len(alerts) == 1 {
		status = alerts[0].Status
	}
	e := newEngine(snapshotIn{ReceivedAt: at, Internal: true, DuplicateWindow: 45 * time.Second,
		Payload: Payload{GroupKey: internalalerts.GroupKey("MusterSnapshotTruncated"), Status: status,
			Alerts: alerts}}, s.group, s.route, rows, nil)
	e.run()
	for _, a := range e.alerts {
		if a.ID == 0 {
			s.next++
			a.ID = s.next
		}
		s.alerts[a.Fingerprint] = a
	}
	return e
}

func kindsOf(e *engine) []ChangeKind {
	var out []ChangeKind
	for _, c := range e.changes {
		out = append(out, c.kind)
	}
	return out
}

func internalAlert(status, name string, startsAt time.Time) PayloadAlert {
	labels := map[string]string{"alertname": "MusterSnapshotTruncated", "severity": "warning",
		"integration": "NTAAAAAAAAAAAA", "integration_name": name}
	if status == StatusResolved {
		delete(labels, "integration_name")
	}
	return PayloadAlert{Status: status, Labels: labels, Annotations: map[string]string{"summary": name},
		StartsAt: startsAt, Fingerprint: internalalerts.Fingerprint(internalalerts.SnapshotTruncated, labels)}
}

// TestInternalAlertRules is the processing of synthetic Snapshots (C-06.FR-14): a raise fires, a repeated raise
// changes nothing, a raise with a new name label updates it in place as an annotation change, an older raise changes
// nothing, a resolve resolves, a replayed raise never reopens, a newer raise fires again; nothing is learned, no window
// is opened, no absence is decided and no resolve is counted as dropped.
func TestInternalAlertRules(t *testing.T) {
	s := newInternalSim()
	e := s.send(t0, internalAlert(StatusFiring, "lab", t0))
	if !slices.Equal(kindsOf(e), []ChangeKind{ChangeFired}) || len(e.listedAlerts) != 1 {
		t.Fatalf("raise: %v", kindsOf(e))
	}
	if s.group.WindowSeq != 0 || s.group.WindowStartedAt != nil || s.route.changed || e.late || e.early {
		t.Errorf("a synthetic snapshot opened a window or learned: %+v %+v", s.group, s.route)
	}
	fp := internalAlert(StatusFiring, "lab", t0).Fingerprint
	if e := s.send(t0.Add(time.Minute), internalAlert(StatusFiring, "lab", t0.Add(time.Minute))); len(e.changes) != 0 {
		t.Errorf("repeated raise: %v", kindsOf(e))
	}
	e = s.send(t0.Add(2*time.Minute), internalAlert(StatusFiring, "lab-eu", t0))
	a := s.alerts[fp]
	if !slices.Equal(kindsOf(e), []ChangeKind{ChangeAnnotations}) || a.Labels["integration_name"] != "lab-eu" ||
		!a.StartsAt.Equal(t0) || a.Episode != 1 || a.Status != StatusFiring {
		t.Errorf("rename: %v, %+v", kindsOf(e), a)
	}
	// A raise received before the latest one applied, as in a replay, changes nothing.
	if e := s.send(t0.Add(time.Minute), internalAlert(StatusFiring, "lab", t0)); len(e.changes) != 0 ||
		s.alerts[fp].Labels["integration_name"] != "lab-eu" {
		t.Errorf("an older raise: %v", kindsOf(e))
	}
	e = s.send(t0.Add(3*time.Minute), internalAlert(StatusResolved, "", t0.Add(3*time.Minute)))
	if !slices.Equal(kindsOf(e), []ChangeKind{ChangeResolved}) || s.alerts[fp].Reason != ResolveResolved ||
		s.alerts[fp].Labels["integration_name"] != "lab-eu" {
		t.Errorf("resolve: %v, %+v", kindsOf(e), s.alerts[fp])
	}
	for _, again := range []PayloadAlert{internalAlert(StatusFiring, "lab", t0),
		internalAlert(StatusResolved, "", t0.Add(3*time.Minute))} {
		if e := s.send(t0.Add(time.Hour), again); len(e.changes) != 0 || e.stats.Dropped != 0 {
			t.Errorf("replayed %s: %v", again.Status, kindsOf(e))
		}
	}
	e = s.send(t0.Add(2*time.Hour), internalAlert(StatusFiring, "lab-eu", t0.Add(2*time.Hour)))
	if !slices.Equal(kindsOf(e), []ChangeKind{ChangeFired}) || s.alerts[fp].Episode != 2 {
		t.Errorf("a new raise: %v, %+v", kindsOf(e), s.alerts[fp])
	}
	// A resolve of an Internal alert that fires nowhere changes nothing and is not dropped.
	other := internalAlert(StatusResolved, "", t0)
	other.Fingerprint = "0000000000000000"
	if e := s.send(t0.Add(3*time.Hour), other); len(e.changes) != 0 || e.stats.Dropped != 0 {
		t.Errorf("an unknown resolve: %v, %+v", kindsOf(e), e.stats)
	}
}

// TestInternalSnapshotNeverGone: a synthetic Snapshot that does not list an Internal alert of its groupKey, however
// late, never makes it Gone.
func TestInternalSnapshotNeverGone(t *testing.T) {
	s := newInternalSim()
	s.send(t0, internalAlert(StatusFiring, "lab", t0))
	fp := internalAlert(StatusFiring, "lab", t0).Fingerprint
	b := internalAlert(StatusFiring, "other", t0)
	b.Labels["integration"] = "NTBBBBBBBBBBBB"
	b.Fingerprint = internalalerts.Fingerprint(internalalerts.SnapshotTruncated, b.Labels)
	presences := []*presence{{AlertID: s.alerts[fp].ID, State: PresenceMissed, LastListedWindow: 0,
		MissedSince: new(t0)}}
	for _, at := range []time.Time{t0.Add(time.Hour), t0.Add(48 * time.Hour)} {
		var rows []*alert
		for _, a := range s.alerts {
			rows = append(rows, a)
		}
		e := newEngine(snapshotIn{ReceivedAt: at, Internal: true, Payload: Payload{GroupKey: "k", Status: StatusFiring,
			Alerts: []PayloadAlert{b}}}, s.group, s.route, rows, presences)
		e.run()
		if s.alerts[fp].Status != StatusFiring || len(e.gone) != 0 || len(e.missed) != 0 || e.stats.Gone != 0 {
			t.Errorf("at %v: %+v", at, s.alerts[fp])
		}
		for _, a := range e.alerts {
			if a.ID == 0 {
				s.next++
				a.ID = s.next
			}
			s.alerts[a.Fingerprint] = a
		}
	}
}

// TestFingerprintAndChannel: the fingerprint of an Internal alert is Alertmanager's of alertname and its id label,
// and synthetic Stored Snapshots wake the same workers.
func TestFingerprintAndChannel(t *testing.T) {
	labels := map[string]string{"alertname": "MusterSnapshotTruncated", "integration": "NTAAAAAAAAAAAA",
		"integration_name": "lab", "severity": "warning"}
	if got, want := internalalerts.Fingerprint(internalalerts.SnapshotTruncated, labels),
		Fingerprint(map[string]string{"alertname": "MusterSnapshotTruncated", "integration": "NTAAAAAAAAAAAA"}); got != want {
		t.Errorf("fingerprint %s, want %s", got, want)
	}
	a, _ := json.Marshal(Notification{OrgID: 1, IntegrationID: 2})
	b, _ := json.Marshal(internalalerts.Notification{OrgID: 1, IntegrationID: 2})
	if SnapshotChannel != internalalerts.SnapshotChannel || !bytes.Equal(a, b) {
		t.Errorf("channel %s and %s, payloads %s and %s", SnapshotChannel, internalalerts.SnapshotChannel, a, b)
	}
}

// fakeInternal is the processing database with the queries of Internal alerts, truncation and deletion.
type fakeInternal struct {
	*fakeProcess
	// truncatedCount is what CountTruncatedGroups answers; cleared what ClearTruncation does.
	truncatedCount int64
	cleared        int64
	deleted        []dbgen.ResolveIntegrationAlertsRow
	resolveArgs    []dbgen.ResolveIntegrationAlertsParams
	builtin        int64
	deletedAt      pgtype.Timestamptz
	open           []idb.ListOpenInternalAlertsRow
	bodies         [][]byte
	inserted       []idb.InsertInternalSnapshotParams
}

func newFakeInternal() *fakeInternal {
	return &fakeInternal{fakeProcess: newFakeProcess(), builtin: 9}
}

func (f *fakeInternal) InTx(_ context.Context, fn func(ProcessQueries, dbgen.DBTX) error) error {
	return fn(f, nil)
}

func (f *fakeInternal) RenewIngestLease(ctx context.Context, arg dbgen.RenewIngestLeaseParams) (
	dbgen.RenewIngestLeaseRow, error) {
	row, err := f.fakeProcess.RenewIngestLease(ctx, arg)
	row.Name, row.DeletedAt = "lab", f.deletedAt
	return row, err
}

func (f *fakeInternal) CheckIngestLease(ctx context.Context, arg dbgen.CheckIngestLeaseParams) (
	dbgen.CheckIngestLeaseRow, error) {
	row, err := f.fakeProcess.CheckIngestLease(ctx, arg)
	row.Name, row.DeletedAt = "lab", f.deletedAt
	return row, err
}

// UpsertAlertmanagerGroup keeps the truncation the previous Snapshot wrote.
func (f *fakeInternal) UpsertAlertmanagerGroup(ctx context.Context, arg dbgen.UpsertAlertmanagerGroupParams) (
	dbgen.UpsertAlertmanagerGroupRow, error) {
	row, err := f.fakeProcess.UpsertAlertmanagerGroup(ctx, arg)
	if n := len(f.groups); n > 0 {
		row.Truncated, row.TruncatedSince = f.groups[n-1].Truncated, f.groups[n-1].TruncatedSince
	}
	return row, err
}

func (f *fakeInternal) LockIntegrationName(context.Context, dbgen.LockIntegrationNameParams) (string, error) {
	return "lab-now", f.err("LockIntegrationName")
}

func (f *fakeInternal) GetAlertRetention(context.Context, int64) (int64, error) {
	return 90, f.err("GetAlertRetention")
}

func (f *fakeInternal) CountTruncatedGroups(context.Context, dbgen.CountTruncatedGroupsParams) (int64, error) {
	return f.truncatedCount, f.err("CountTruncatedGroups")
}

func (f *fakeInternal) ResolveIntegrationAlerts(_ context.Context, arg dbgen.ResolveIntegrationAlertsParams) (
	[]dbgen.ResolveIntegrationAlertsRow, error) {
	f.resolveArgs = append(f.resolveArgs, arg)
	return f.deleted, f.err("ResolveIntegrationAlerts")
}

func (f *fakeInternal) ClearTruncation(context.Context, dbgen.ClearTruncationParams) (int64, error) {
	return f.cleared, f.err("ClearTruncation")
}

func (f *fakeInternal) FindBuiltinIntegration(context.Context, int64) (int64, error) {
	if f.builtin == 0 {
		return 0, pgx.ErrNoRows
	}
	return f.builtin, nil
}

func (f *fakeInternal) InsertInternalBody(_ context.Context, arg idb.InsertInternalBodyParams) error {
	f.bodies = append(f.bodies, arg.Body)
	return nil
}

func (f *fakeInternal) InsertInternalSnapshot(_ context.Context, arg idb.InsertInternalSnapshotParams) error {
	f.inserted = append(f.inserted, arg)
	return nil
}

func (f *fakeInternal) NotifyInternalSnapshot(context.Context, idb.NotifyInternalSnapshotParams) error {
	return nil
}

func (f *fakeInternal) ListOpenInternalAlerts(context.Context, idb.ListOpenInternalAlertsParams) (
	[]idb.ListOpenInternalAlertsRow, error) {
	return f.open, f.err("ListOpenInternalAlerts")
}

func (f *fakeInternal) ListPendingInternalRaises(context.Context, int64) ([][]byte, error) {
	return nil, nil
}

// addSource adds a pending Stored Snapshot of the source.
func (f *fakeInternal) addSource(id int64, at time.Time, source string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, dbgen.ListPendingSnapshotsRow{ID: id, PublicID: fmt.Sprintf("SS%012d", id),
		ReceivedAt: at, Source: source, Body: body})
}

func truncatedBody(truncated int) string {
	return strings.Replace(body(wireAlertOf("db-a", "firing")), `"status":"firing",`,
		`"status":"firing","truncatedAlerts":`+strconv.Itoa(truncated)+`,`, 1)
}

// TestTruncationStamps: a raise is dated with the business time of the decision, under the Integration's row, so that
// lanes that commit out of receipt order reach the built-in Integration in the order they decided; a replayed Snapshot
// keeps its receipt, so that a replay changes nothing it did not before.
func TestTruncationStamps(t *testing.T) {
	for _, replayed := range []bool{false, true} {
		store := newFakeInternal()
		p := newTestProcessor(store, clock.NewManual(t0.Add(time.Hour)), &bytes.Buffer{}, nil)
		store.truncatedCount = 1
		store.add(1, t0, truncatedBody(2))
		store.pending[0].Replayed = replayed
		if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 1 || len(store.inserted) != 1 {
			t.Fatalf("replayed %v: %d, %v, %d", replayed, n, err, len(store.inserted))
		}
		want := t0.Add(time.Hour)
		if replayed {
			want = t0
		}
		if at := store.inserted[0].ReceivedAt; !at.Equal(want) {
			t.Errorf("replayed %v: raised at %v, want %v", replayed, at, want)
		}
	}
}

// TestTruncationRaisesAndResolves is C-06.FR-6 in processing: the first truncated groupKey of an Integration raises
// MusterSnapshotTruncated, another one raises nothing more, and the end of the last one resolves it.
func TestTruncationRaisesAndResolves(t *testing.T) {
	store := newFakeInternal()
	var log bytes.Buffer
	p := newTestProcessor(store, clock.NewManual(t0), &log, nil)
	store.truncatedCount = 1
	store.add(1, t0, truncatedBody(2))
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 1 || len(store.inserted) != 1 {
		t.Fatalf("truncated: %d, %v, %d", n, err, len(store.inserted))
	}
	var w struct {
		Alerts []PayloadAlert `json:"alerts"`
	}
	raise, err := ParsePayload(store.bodies[0])
	if err != nil || json.Unmarshal(store.bodies[0], &w) != nil || raise.Alerts[0].Labels["integration_name"] != "lab-now" ||
		raise.Alerts[0].Labels["integration"] != "NTAAAAAAAAAAAA" || raise.Status != StatusFiring ||
		store.inserted[0].IntegrationID != 9 || !store.inserted[0].ReceivedAt.Equal(t0) {
		t.Errorf("raise %+v, %v", raise, err)
	}
	// A second truncated groupKey: the count is 2, nothing is raised.
	store.groups = nil
	store.truncatedCount = 2
	store.add(2, t0.Add(time.Second), truncatedBody(1))
	if _, err := p.ProcessPending(t.Context(), 5); err != nil || len(store.inserted) != 1 {
		t.Errorf("second truncated groupKey: %v, %d", err, len(store.inserted))
	}
	// The groupKey is untruncated again and none is left: the resolve.
	store.truncatedCount = 0
	store.add(3, t0.Add(2*time.Second), body(wireAlertOf("db-a", "firing")))
	if _, err := p.ProcessPending(t.Context(), 5); err != nil || len(store.inserted) != 2 {
		t.Fatalf("untruncated: %v, %d", err, len(store.inserted))
	}
	if resolve, err := ParsePayload(store.bodies[1]); err != nil || resolve.Alerts[0].Status != StatusResolved {
		t.Errorf("resolve %+v, %v", resolve, err)
	}
	// Without the built-in Integration the Snapshot fails.
	store.builtin = 0
	store.groups = nil
	store.truncatedCount = 1
	store.add(4, t0.Add(3*time.Second), truncatedBody(1))
	if _, err := p.ProcessPending(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	if last := store.finished[len(store.finished)-1]; last.State != StateFailed ||
		!strings.Contains(last.ProcessingError.String, "MusterSnapshotTruncated: the built-in Muster integration is missing") {
		t.Errorf("without the built-in integration: %+v", last)
	}
	// A lost connection while counting leaves the Snapshot pending.
	store.builtin = 9
	store.fail["CountTruncatedGroups"] = errors.New("conn closed")
	store.groups = nil
	store.add(5, t0.Add(4*time.Second), truncatedBody(1))
	if _, err := p.ProcessPending(t.Context(), 5); err == nil || len(store.pending) != 1 {
		t.Errorf("a failed count: %v, %d pending", err, len(store.pending))
	}
}

// TestInternalSnapshotLogs: a synthetic Snapshot that fires or resolves an Internal alert logs it with its alertname,
// fingerprint and entity.
func TestInternalSnapshotLogs(t *testing.T) {
	store := newFakeInternal()
	var log bytes.Buffer
	p := newTestProcessor(store, clock.NewManual(t0), &log, nil)
	r := internalalerts.NewRaiser(1, "")
	if err := r.Raise(t.Context(), store, t0, internalalerts.SnapshotTruncated,
		internalalerts.Entity{ID: "NTBBBBBBBBBBBB", Name: "lab"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Resolve(t.Context(), store, t0.Add(time.Second), internalalerts.SnapshotTruncated,
		"NTBBBBBBBBBBBB"); err != nil {
		t.Fatal(err)
	}
	store.addSource(1, t0, SourceInternal, store.bodies[0])
	store.addSource(2, t0.Add(time.Second), SourceInternal, store.bodies[1])
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 2 {
		t.Fatalf("processed %d, %v", n, err)
	}
	var got []string
	for _, e := range events(t, &log) {
		if e["alertname"] != nil {
			got = append(got, e["event"].(string)+" "+e["alertname"].(string)+" "+e["entity"].(string))
			if e["fingerprint"] == "" {
				t.Errorf("line %v", e)
			}
		}
	}
	if !slices.Equal(got, []string{"internal_alert_raised MusterSnapshotTruncated NTBBBBBBBBBBBB",
		"internal_alert_resolved MusterSnapshotTruncated NTBBBBBBBBBBBB"}) {
		t.Errorf("lines %v\n%s", got, log.String())
	}
	if c := internalChangeOf(false, "f", map[string]string{"alertname": "MusterUnknown"}); c.Entity != "" {
		t.Errorf("unknown alertname %+v", c)
	}
}

// TestDeletionMarker is C-06.FR-16 in processing: the marker resolves the firing Alerts of the Integration with the
// reason integration_deleted, ends their presences, hands the changes over, counts them by reason, ends the
// truncation and resolves MusterSnapshotTruncated; a failure marks it failed.
func TestDeletionMarker(t *testing.T) {
	store := newFakeInternal()
	var log bytes.Buffer
	sink := &recordingSink{}
	c := clock.NewManual(t0.Add(time.Minute))
	p := newTestProcessor(store, c, &log, sink)
	integration := "NTAAAAAAAAAAAA"
	deleted0 := metrics.AlertsResolved.With(integration, ResolveIntegrationDeleted).Get()
	r := internalalerts.NewRaiser(1, "")
	if err := r.MarkDeleted(t.Context(), store, t0, 5, integration, "lab-eu"); err != nil {
		t.Fatal(err)
	}
	marker := store.bodies[0]
	store.bodies, store.inserted = nil, nil
	store.deleted = []dbgen.ResolveIntegrationAlertsRow{{ID: 1, Fingerprint: "a", Episode: 1},
		{ID: 2, Fingerprint: "b", Episode: 3}}
	store.cleared = 1
	store.open = []idb.ListOpenInternalAlertsRow{{Fingerprint: "f", StartsAt: t0,
		Labels: []byte(`{"alertname":"MusterSnapshotTruncated","integration":"NTAAAAAAAAAAAA"}`)}}
	store.addSource(1, t0, SourceInternal, marker)
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 1 {
		t.Fatalf("processed %d, %v", n, err)
	}
	arg := store.resolveArgs[0]
	if arg.ReasonText != "Integration lab-eu deleted" || !arg.ResolvedAt.Equal(t0) || !arg.UpdatedAt.Equal(c.Now()) ||
		arg.IntegrationID != 5 {
		t.Errorf("resolve %+v", arg)
	}
	if got := metrics.AlertsResolved.With(integration, ResolveIntegrationDeleted).Get() - deleted0; got != 2 {
		t.Errorf("integration_deleted +%d", got)
	}
	if len(sink.changes) != 2 || sink.changes[1] != (AlertChange{Kind: ChangeResolved, AlertID: 2, Fingerprint: "b",
		Episode: 3, StoredSnapshotID: 1, Reason: ResolveIntegrationDeleted, ReasonText: "Integration lab-eu deleted"}) {
		t.Errorf("changes %+v", sink.changes)
	}
	if len(store.ended) != 1 || !slices.Equal(store.ended[0].AlertIds, []int64{1, 2}) {
		t.Errorf("ended %+v", store.ended)
	}
	if len(store.inserted) != 1 || store.inserted[0].IntegrationID != 9 {
		t.Fatalf("resolve of MusterSnapshotTruncated %+v", store.inserted)
	}
	if resolve, err := ParsePayload(store.bodies[0]); err != nil || resolve.Alerts[0].Status != StatusResolved ||
		resolve.Alerts[0].Labels["integration"] != integration {
		t.Errorf("resolve %+v, %v", resolve, err)
	}
	if fin := store.finished[0]; fin.State != StateProcessed || fin.GroupKey.String != internalalerts.DeletionGroupKey ||
		fin.AlertCount.Int64 != 0 {
		t.Errorf("finished %+v", fin)
	}
	for _, e := range events(t, &log) {
		if e["event"] == "snapshot_processed" && (e["resolved"] != 2.0 || e["fired"] != 0.0) {
			t.Errorf("line %v", e)
		}
	}
	// Nothing fires and no Internal alert is open: nothing is resolved.
	open := store.open
	store.deleted, store.cleared, store.inserted, store.open = nil, 0, nil, nil
	store.addSource(2, t0.Add(time.Second), SourceInternal, marker)
	if _, err := p.ProcessPending(t.Context(), 5); err != nil || len(store.inserted) != 0 || len(sink.changes) != 2 {
		t.Errorf("an empty deletion: %v, %+v", err, store.inserted)
	}
	// A webhook whose body looks like a marker is processed as a webhook.
	store.addSource(3, t0.Add(2*time.Second), SourceWebhook, marker)
	if _, err := p.ProcessPending(t.Context(), 5); err != nil || len(store.resolveArgs) != 2 {
		t.Errorf("a webhook marker: %v, %d", err, len(store.resolveArgs))
	}
	store.open = open
	for _, name := range []string{"ResolveIntegrationAlerts", "EndPresences", "ClearTruncation",
		"ListOpenInternalAlerts"} {
		store.fail = map[string]error{name: &pgconn.PgError{Code: "22001"}}
		store.deleted, store.cleared = []dbgen.ResolveIntegrationAlertsRow{{ID: 1, Fingerprint: "a", Episode: 1}}, 1
		store.addSource(4, t0.Add(3*time.Second), SourceInternal, marker)
		if _, err := p.ProcessPending(t.Context(), 5); err != nil {
			t.Fatal(err)
		}
		if last := store.finished[len(store.finished)-1]; last.State != StateFailed {
			t.Errorf("%s failing: %+v", name, last)
		}
	}
	store.fail = map[string]error{}
	store.builtin = 0
	store.addSource(5, t0.Add(4*time.Second), SourceInternal, marker)
	if _, err := p.ProcessPending(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	if last := store.finished[len(store.finished)-1]; last.State != StateFailed {
		t.Errorf("without the built-in integration: %+v", last)
	}
	store.builtin = 9
	sink.err = &pgconn.PgError{Code: "22001"}
	store.addSource(6, t0.Add(5*time.Second), SourceInternal, marker)
	if _, err := p.ProcessPending(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	if last := store.finished[len(store.finished)-1]; last.State != StateFailed {
		t.Errorf("a failing sink: %+v", last)
	}
}

// fakeRoutes answers the queries of the learned routes.
type fakeRoutes struct {
	ProcessQueries
	builtin bool
	id      int64
	rows    []dbgen.ListAlertmanagerRoutesRow
	err     error
}

func (f *fakeRoutes) FindRoutesIntegration(_ context.Context, arg dbgen.FindRoutesIntegrationParams) (
	dbgen.FindRoutesIntegrationRow, error) {
	if f.id == 0 || arg.OrgID != 1 {
		return dbgen.FindRoutesIntegrationRow{}, pgx.ErrNoRows
	}
	return dbgen.FindRoutesIntegrationRow{ID: f.id, Builtin: f.builtin}, f.err
}

func (f *fakeRoutes) ListAlertmanagerRoutes(context.Context, dbgen.ListAlertmanagerRoutesParams) (
	[]dbgen.ListAlertmanagerRoutesRow, error) {
	return f.rows, f.err
}

// TestRoutes is C-06.FR-18 and C-06.AC-3: each route with its learned interval and time to resolve by absence, the
// unlearned stale_after, and the warning with a snippet above processing.long_repeat_warning.
func TestRoutes(t *testing.T) {
	q := &fakeRoutes{id: 3, rows: []dbgen.ListAlertmanagerRoutesRow{
		{RoutePath: "{}", TruncatedGroupCount: 1},
		{RoutePath: `{}/{team="web"}`, LearnedRepeatIntervalMs: pgtype.Int8{Int64: 300_000, Valid: true}},
		{RoutePath: `{}/{kind="info"}`, LearnedRepeatIntervalMs: pgtype.Int8{Int64: 7_200_000, Valid: true}},
		{RoutePath: `{}/{a="b"}`, LearnedRepeatIntervalMs: pgtype.Int8{Int64: 3_600_000, Valid: true}},
		{RoutePath: `{}/{b="c"}`, LearnedRepeatIntervalMs: pgtype.Int8{Int64: 300_400, Valid: true}},
		{RoutePath: `{}/{c="d"}`, LearnedRepeatIntervalMs: pgtype.Int8{Int64: 3_600_400, Valid: true}},
	}}
	v := NewAlertsView(1, q, clock.NewManual(t0))
	routes, err := v.Routes(t.Context(), "NTAAAAAAAAAAAA")
	if err != nil || len(routes) != 6 {
		t.Fatalf("routes %+v, %v", routes, err)
	}
	if r := routes[0]; r.LearnedRepeatInterval != nil || r.ResolveByAbsenceAfter != 25*time.Hour ||
		r.TruncatedGroupCount != 1 || r.LongIntervalWarning {
		t.Errorf("unlearned %+v", r)
	}
	if r := routes[1]; *r.LearnedRepeatInterval != 5*time.Minute || r.ResolveByAbsenceAfter != 15*time.Minute ||
		r.LongIntervalWarning || r.RecommendedSnippet != "" {
		t.Errorf("5 minutes %+v", r)
	}
	if r := routes[2]; !r.LongIntervalWarning || !strings.Contains(r.RecommendedSnippet, "repeat_interval: 10m") ||
		!strings.Contains(r.RecommendedSnippet, "a repeat interval of 2h for") {
		t.Errorf("2 hours %+v", r)
	}
	if r := routes[3]; r.LongIntervalWarning {
		t.Errorf("exactly the threshold %+v", r)
	}
	// A measured gap is not whole seconds: the interval is rounded first and the time to resolve by absence is three
	// times the rounded interval, never 901 s from rounding 3 × 300.4 s.
	if r := routes[4]; *r.LearnedRepeatInterval != 300*time.Second || r.ResolveByAbsenceAfter != 900*time.Second {
		t.Errorf("300.4 seconds %+v", r)
	}
	if r := routes[5]; *r.LearnedRepeatInterval != time.Hour || r.ResolveByAbsenceAfter != 3*time.Hour ||
		r.LongIntervalWarning {
		t.Errorf("an hour and 400 ms %+v", r)
	}
	q.builtin = true
	if routes, err := v.Routes(t.Context(), "NTAAAAAAAAAAAA"); err != nil || len(routes) != 0 || routes == nil {
		t.Errorf("routes of the built-in integration %+v, %v", routes, err)
	}
	q.builtin = false
	for _, id := range []string{"bad", "NTZZZZZZZZZZZZ"} {
		q.id = 0
		if _, err := v.Routes(t.Context(), id); !errors.Is(err, integrations.ErrNotFound) {
			t.Errorf("%s = %v", id, err)
		}
	}
	q.id, q.err = 3, errors.New("down")
	if _, err := v.Routes(t.Context(), "NTAAAAAAAAAAAA"); err == nil {
		t.Error("an error was ignored")
	}
	q.err = nil
	q.rows = nil
	q.id = 3
	v.routes = &fakeListFails{fakeRoutes: q}
	if _, err := v.Routes(t.Context(), "NTAAAAAAAAAAAA"); err == nil {
		t.Error("a failed list was ignored")
	}
}

type fakeListFails struct{ *fakeRoutes }

func (f *fakeListFails) ListAlertmanagerRoutes(context.Context, dbgen.ListAlertmanagerRoutesParams) (
	[]dbgen.ListAlertmanagerRoutesRow, error) {
	return nil, errors.New("list down")
}

func TestRouteSnippet(t *testing.T) {
	if got := RouteSnippet("{}", 2*time.Hour); !strings.HasSuffix(got, "\nroute:\n  repeat_interval: 10m\n") ||
		!strings.Contains(got, "# \"{}\", so it takes long") {
		t.Errorf("top-level route:\n%s", got)
	}
	got := RouteSnippet(`{}/{a="b/c",d=~"x,y"}/{}/{e!="it's"}`, 90*time.Minute+20*time.Second)
	want := "route:\n  routes:\n" +
		"    - matchers:\n        - \"a=\\\"b/c\\\"\"\n        - \"d=~\\\"x,y\\\"\"\n      routes:\n" +
		"        - matchers: []\n          routes:\n" +
		"            - matchers:\n                - \"e!=\\\"it's\\\"\"\n              repeat_interval: 10m\n"
	if !strings.HasSuffix(got, want) || !strings.Contains(got, "interval of 90m for") {
		t.Errorf("nested route:\n%s\nwant suffix\n%s", got, want)
	}
	if got := routeLevels(`{}/{a="\"}/{"}`); len(got) != 1 || got[0][0] != `a="\"}/{"` {
		t.Errorf("an escaped quote: %q", got)
	}
	// A groupKey is received text: line breaks and other control characters never leave a comment or a scalar.
	got = RouteSnippet("{}/{a=\"x\ny: 1\"}\nroutes: []\u2028\x85'", time.Hour)
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, " ") && line != "route:" {
			t.Errorf("an injected line %q in\n%s", line, got)
		}
	}
	for _, r := range got {
		if r > 0x7e || (r < 0x20 && r != '\n') {
			t.Errorf("a character %U in\n%s", r, got)
		}
	}
	for d, want := range map[time.Duration]string{2 * time.Hour: "2h", 10 * time.Minute: "10m",
		90 * time.Second: "90s", 45 * time.Second: "45s"} {
		if got := amDuration(d); got != want {
			t.Errorf("amDuration(%v) = %s", d, got)
		}
	}
}

// fakeRetention deletes from a backlog of expired Alerts, at most a batch at a time.
type fakeRetention struct {
	ProcessQueries
	backlog int64
	calls   []dbgen.DeleteExpiredAlertsParams
	err     map[string]error
}

func (f *fakeRetention) GetAlertRetention(context.Context, int64) (int64, error) {
	return 90, f.err["GetAlertRetention"]
}

func (f *fakeRetention) DeleteExpiredAlerts(_ context.Context, arg dbgen.DeleteExpiredAlertsParams) (int64, error) {
	f.calls = append(f.calls, arg)
	if err := f.err["DeleteExpiredAlerts"]; err != nil {
		return 0, err
	}
	n := min(f.backlog, int64(arg.BatchSize))
	f.backlog -= n
	return n, nil
}

// TestPruneAlerts is C-06.FR-19: the Alerts resolved before retention.alert_details are deleted in batches of 5,000
// until a batch comes back short.
func TestPruneAlerts(t *testing.T) {
	q := &fakeRetention{backlog: 10_003, err: map[string]error{}}
	n, err := PruneAlerts(t.Context(), q, 1, t0)
	if err != nil || n != 10_003 || len(q.calls) != 3 || q.calls[0].BatchSize != 5000 ||
		!q.calls[0].Cutoff.Equal(t0.Add(-90*24*time.Hour)) || q.calls[2].OrgID != 1 {
		t.Errorf("pruned %d, %v, %+v", n, err, q.calls)
	}
	q.err["DeleteExpiredAlerts"] = errors.New("down")
	if _, err := PruneAlerts(t.Context(), q, 1, t0); err == nil {
		t.Error("a failed delete was ignored")
	}
	q.err["GetAlertRetention"] = errors.New("down")
	if _, err := PruneAlerts(t.Context(), q, 1, t0); err == nil {
		t.Error("a failed read was ignored")
	}
}

// TestDeletedIntegrationSnapshots: a webhook received once its Integration was deleted fails without firing anything,
// one received before is processed but raises no Internal alert about the deleted Integration, and a replayed raise
// of an Internal alert that retention removed fires nothing.
func TestDeletedIntegrationSnapshots(t *testing.T) {
	store := newFakeInternal()
	var log bytes.Buffer
	p := newTestProcessor(store, clock.NewManual(t0.Add(time.Hour)), &log, nil)
	store.deletedAt = pgtype.Timestamptz{Time: t0.Add(time.Minute), Valid: true}
	store.truncatedCount = 1
	store.add(1, t0, truncatedBody(1))
	store.add(2, t0.Add(time.Minute), body(wireAlertOf("db-b", "firing")))
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 2 {
		t.Fatalf("processed %d, %v", n, err)
	}
	if first := store.finished[0]; first.State != StateProcessed || len(store.inserted) != 0 {
		t.Errorf("before the deletion: %+v, internal snapshots %d", first, len(store.inserted))
	}
	if late := store.finished[1]; late.State != StateFailed || late.ProcessingError.String !=
		"the integration was deleted before the snapshot was received" || store.alerts["db-b"] != nil {
		t.Errorf("after the deletion: %+v", late)
	}

	store.deletedAt = pgtype.Timestamptz{}
	r := internalalerts.NewRaiser(1, "")
	old := t0.Add(-100 * 24 * time.Hour)
	if err := r.Raise(t.Context(), store, old, internalalerts.SnapshotTruncated,
		internalalerts.Entity{ID: "NTCCCCCCCCCCCC", Name: "old"}, nil); err != nil {
		t.Fatal(err)
	}
	store.addSource(3, old, SourceInternal, store.bodies[len(store.bodies)-1])
	if _, err := p.ProcessPending(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	if last := store.finished[len(store.finished)-1]; last.State != StateProcessed || len(store.inserted) != 1 ||
		strings.Contains(log.String(), "internal_alert_raised") {
		t.Errorf("an old raise: %+v\n%s", last, log.String())
	}
	store.fail["GetAlertRetention"] = &pgconn.PgError{Code: "22001"}
	store.addSource(4, old, SourceInternal, store.bodies[len(store.bodies)-1])
	if _, err := p.ProcessPending(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	if last := store.finished[len(store.finished)-1]; last.State != StateFailed {
		t.Errorf("a failed retention read: %+v", last)
	}
	store.fail = map[string]error{"LockIntegrationName": &pgconn.PgError{Code: "22001"}}
	store.groups = nil
	store.add(5, t0.Add(2*time.Hour), truncatedBody(1))
	if _, err := p.ProcessPending(t.Context(), 5); err != nil {
		t.Fatal(err)
	}
	if last := store.finished[len(store.finished)-1]; last.State != StateFailed {
		t.Errorf("a failed name read: %+v", last)
	}
}
