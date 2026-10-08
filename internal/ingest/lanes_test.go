// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/internalalerts"
)

// bodyOf is a Snapshot of the groupKey listing the fingerprints, firing.
func bodyOf(groupKey string, fingerprints ...string) string {
	alerts := make([]string, len(fingerprints))
	for i, fp := range fingerprints {
		alerts[i] = wireAlertOf(fp, "firing")
	}
	return `{"version":"4","groupKey":` + fmt.Sprintf("%q", groupKey) + `,"status":"firing","alerts":[` +
		strings.Join(alerts, ",") + `]}`
}

// markerBody is the marker of the deletion of an Integration, as the Raiser writes it.
func markerBody() string {
	return `{"version":"4","groupKey":` + fmt.Sprintf("%q", internalalerts.DeletionGroupKey) +
		`,"status":"resolved","receiver":"muster","commonLabels":{"integration":"NTAAAAAAAAAAAA",` +
		`"integration_name":"lab"},"alerts":[]}`
}

func itemOf(t *testing.T, id int64, source, body string) *item {
	t.Helper()
	return newItem(dbgen.ListPendingSnapshotsRow{ID: id, Source: source, Body: []byte(body)})
}

// TestWaitsFor is the ordering rule of C-06.FR-1: a later Snapshot waits for an earlier one of the same groupKey, for
// one that lists a fingerprint it lists, and for a deletion marker, which also waits for every earlier one; a body
// that does not parse waits only for a barrier.
func TestWaitsFor(t *testing.T) {
	marker := markerBody()
	a := itemOf(t, 1, SourceWebhook, bodyOf("g1", "x", "y"))
	b := itemOf(t, 2, SourceWebhook, bodyOf("g1", "z"))
	c := itemOf(t, 3, SourceWebhook, bodyOf("g2", "y"))
	d := itemOf(t, 4, SourceWebhook, bodyOf("g3", "w"))
	broken := itemOf(t, 5, SourceWebhook, "not json")
	del := itemOf(t, 6, SourceInternal, marker)
	fake := itemOf(t, 7, SourceWebhook, marker)
	if !del.barrier || fake.barrier || broken.parseErr == nil {
		t.Fatalf("barrier %v, a webhook marker %v, parse error %v", del.barrier, fake.barrier, broken.parseErr)
	}
	for _, tc := range []struct {
		name        string
		later, earl *item
		want        bool
	}{
		{"same groupKey", b, a, true},
		{"shared fingerprint", c, a, true},
		{"disjoint", d, a, false},
		{"disjoint other way", c, b, false},
		{"broken after", broken, a, false},
		{"after broken", a, broken, false},
		{"marker after", del, d, true},
		{"after marker", d, del, true},
		{"broken after marker", broken, del, true},
	} {
		if got := tc.later.waitsFor(tc.earl); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// TestBlocked: in a window of held Snapshots, each starts once nothing earlier that it waits for is unfinished.
func TestBlocked(t *testing.T) {
	marker := markerBody()
	r := &run{window: []*item{
		itemOf(t, 1, SourceWebhook, bodyOf("g1", "a")),
		itemOf(t, 2, SourceWebhook, bodyOf("g2", "b")),
		itemOf(t, 3, SourceWebhook, bodyOf("g1", "c")),
		itemOf(t, 4, SourceInternal, marker),
		itemOf(t, 5, SourceWebhook, bodyOf("g3", "d")),
	}}
	var got []bool
	for i := range r.window {
		got = append(got, r.blocked(i))
	}
	if !slices.Equal(got, []bool{false, false, true, true, true}) {
		t.Errorf("blocked %v", got)
	}
}

// laneSink records, per groupKey, the Stored Snapshots in the order their changes arrive, how many run at the same
// time overall and within one groupKey, and holds each a little so that lanes overlap.
type laneSink struct {
	mu        sync.Mutex
	order     map[string][]int64
	running   map[string]int
	total     int
	maxTotal  int
	overlap   int
	hold      time.Duration
	groupKeys map[int64]string
}

func newLaneSink(hold time.Duration) *laneSink {
	return &laneSink{order: map[string][]int64{}, running: map[string]int{}, hold: hold,
		groupKeys: map[int64]string{}}
}

func (s *laneSink) AlertChanges(_ context.Context, _ dbgen.DBTX, changes []AlertChange) (Routed, error) {
	key, _, _ := strings.Cut(changes[0].Fingerprint, "-")
	s.mu.Lock()
	s.order[key] = append(s.order[key], changes[0].StoredSnapshotID)
	s.running[key]++
	s.total++
	s.maxTotal = max(s.maxTotal, s.total)
	if s.running[key] > 1 {
		s.overlap++
	}
	s.mu.Unlock()
	time.Sleep(s.hold)
	s.mu.Lock()
	s.running[key]--
	s.total--
	s.mu.Unlock()
	return Routed{}, nil
}

// TestLanes is C-06.FR-1 in one replica: the Snapshots of different Alertmanager groups run at the same time, each
// Alertmanager group in arrival order and one at a time, and every Snapshot is processed and counted once.
func TestLanes(t *testing.T) {
	store := newFakeProcess()
	var log bytes.Buffer
	sink := newLaneSink(5 * time.Millisecond)
	p := newTestProcessor(store, clock.NewManual(t0.Add(time.Hour)), &log, sink)
	p.lanes = 4
	want := map[string][]int64{}
	id := int64(0)
	for round := range 5 {
		for _, key := range []string{"g1", "g2", "g3", "g4", "g5", "g6"} {
			id++
			store.add(id, t0.Add(time.Duration(id)*time.Second), bodyOf(key, fmt.Sprintf("%s-%d", key, round)))
			want[key] = append(want[key], id)
		}
	}
	n, err := p.ProcessPending(t.Context(), 5)
	if err != nil || n != 30 || len(store.pending) != 0 || store.counted != 30 {
		t.Fatalf("processed %d, %v, pending %d, counted %d", n, err, len(store.pending), store.counted)
	}
	for key, ids := range want {
		if !slices.Equal(sink.order[key], ids) {
			t.Errorf("%s in order %v, want %v", key, sink.order[key], ids)
		}
	}
	if sink.overlap != 0 || sink.maxTotal < 2 || sink.maxTotal > 4 {
		t.Errorf("overlap within a groupKey %d, at most %d at a time", sink.overlap, sink.maxTotal)
	}
	lanes := map[float64]bool{}
	for _, e := range events(t, &log) {
		if e["event"] == "snapshot_processed" {
			lanes[e["lane"].(float64)] = true
		}
	}
	if len(lanes) < 2 || len(lanes) > 4 {
		t.Errorf("lanes %v", lanes)
	}
}

// TestLanesSharedFingerprint: Snapshots of two Alertmanager groups that list one fingerprint keep their arrival order
// and never run at the same time.
func TestLanesSharedFingerprint(t *testing.T) {
	store := newFakeProcess()
	sink := newLaneSink(5 * time.Millisecond)
	p := newTestProcessor(store, clock.NewManual(t0.Add(time.Hour)), &bytes.Buffer{}, sink)
	p.lanes = 4
	var want []int64
	for n := range 6 {
		id := int64(n + 1)
		store.add(id, t0.Add(time.Duration(n)*time.Second), bodyOf(fmt.Sprintf("g%d", n%2), "s-x"))
		want = append(want, id)
	}
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 6 {
		t.Fatalf("processed %d, %v", n, err)
	}
	if !slices.Equal(sink.order["s"], want) || sink.overlap != 0 {
		t.Errorf("order %v, want %v, overlap %d", sink.order["s"], want, sink.overlap)
	}
}

// TestLaneRetry: a deadlock or a fingerprint another lane inserted retries the Snapshot in its lane; one that keeps
// failing so stops the Integration's processing and leaves it pending; another unique violation fails it.
func TestLaneRetry(t *testing.T) {
	store := newFakeProcess()
	p := newTestProcessor(store, clock.NewManual(t0.Add(time.Hour)), &bytes.Buffer{}, nil)
	p.pause = func(int) time.Duration { return 0 }
	deadlock := &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
	inserted := &pgconn.PgError{Code: "23505", ConstraintName: alertsFingerprintKey}
	store.failOnce["InsertAlerts"] = []error{deadlock, inserted}
	store.add(1, t0, bodyOf("g1", "a"))
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 1 || store.finished[0].State != StateProcessed {
		t.Fatalf("processed %d, %v, %+v", n, err, store.finished)
	}
	store.fail["InsertAlerts"] = deadlock
	store.add(2, t0.Add(time.Second), bodyOf("g1", "b"))
	if n, err := p.ProcessPending(t.Context(), 5); !errors.Is(err, deadlock) || n != 0 || len(store.pending) != 1 {
		t.Errorf("a lasting deadlock: %d, %v, pending %d", n, err, len(store.pending))
	}
	store.fail["InsertAlerts"] = &pgconn.PgError{Code: "23505", ConstraintName: "other_key"}
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 1 || store.finished[1].State != StateFailed {
		t.Errorf("another unique violation: %d, %v, %+v", n, err, store.finished)
	}
	delete(store.fail, "InsertAlerts")
	ctx, cancel := context.WithCancel(t.Context())
	p.pause = func(int) time.Duration { cancel(); return time.Hour }
	store.failOnce["InsertAlerts"] = []error{deadlock}
	store.add(3, t0.Add(2*time.Second), bodyOf("g1", "c"))
	if _, err := p.ProcessPending(ctx, 5); !errors.Is(err, context.Canceled) || len(store.pending) != 1 {
		t.Errorf("a retry cut short: %v, pending %d", err, len(store.pending))
	}
	if !retryable(fmt.Errorf("wrapped: %w", deadlock)) || retryable(errors.New("deadlock")) {
		t.Error("retryable")
	}
	if d := lanePause(2); d < 30*time.Millisecond || d >= 60*time.Millisecond {
		t.Errorf("pause %v", d)
	}
}

// TestLanesLeaseAndRenewal: the run renews the lease while it works, stops when a Snapshot's check or a renewal finds
// it lost, and stops on a failed renewal or a failed read of the pending Snapshots; the route found missing once, as
// when another lane creates it, is looked up again.
func TestLanesLeaseAndRenewal(t *testing.T) {
	store := newFakeProcess()
	sink := newLaneSink(20 * time.Millisecond)
	p := newTestProcessor(store, clock.NewManual(t0.Add(time.Hour)), &bytes.Buffer{}, sink)
	p.renewEvery = time.Millisecond
	id := int64(0)
	// fill gives each case four fresh pending Snapshots of one groupKey, 20 ms each in the Sink.
	fill := func() {
		store.mu.Lock()
		store.pending = nil
		store.mu.Unlock()
		for range 4 {
			id++
			store.add(id, t0.Add(time.Duration(id)*time.Second), bodyOf("g1", fmt.Sprintf("a%d", id)))
		}
	}
	fill()
	store.routeRace = true
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 4 || store.renewals < 3 {
		t.Fatalf("processed %d, %v, renewals %d", n, err, store.renewals)
	}
	fill()
	store.renewals, store.loseRenewal = 0, 2
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n == 4 || len(store.pending) == 0 {
		t.Errorf("a lost renewal: %d, %v, pending %d", n, err, len(store.pending))
	}
	fill()
	store.loseRenewal, store.renewals = 0, 0
	store.checks, store.loseAfter = 0, 1
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 1 {
		t.Errorf("a lost check: %d, %v", n, err)
	}
	fill()
	store.loseAfter = 0
	store.failOnce["RenewIngestLease"] = []error{nil, errors.New("conn closed")}
	if _, err := p.ProcessPending(t.Context(), 5); err == nil {
		t.Error("a failed renewal was ignored")
	}
	fill()
	store.fail["ListPendingSnapshots"] = errors.New("conn closed")
	if _, err := p.ProcessPending(t.Context(), 5); err == nil {
		t.Error("a failed read was ignored")
	}
	delete(store.fail, "ListPendingSnapshots")
	store.lost = true
	if n, err := p.ProcessPending(t.Context(), 5); err != nil || n != 0 {
		t.Errorf("a lost lease at the start: %d, %v", n, err)
	}
}

// TestAdd: a Snapshot read late with an earlier receipt goes before the held ones that have not started, never before
// one that started.
func TestAdd(t *testing.T) {
	row := func(id int64, at time.Duration) dbgen.ListPendingSnapshotsRow {
		return dbgen.ListPendingSnapshotsRow{ID: id, ReceivedAt: t0.Add(at), Body: []byte(bodyOf("g1", "a"))}
	}
	r := &run{}
	for _, x := range []dbgen.ListPendingSnapshotsRow{row(1, 1), row(3, 3), row(4, 4)} {
		r.add(newItem(x))
	}
	r.window[0].started = true
	r.add(newItem(row(2, 3)))
	r.add(newItem(row(0, 0)))
	r.add(newItem(row(5, 5)))
	var ids []int64
	for _, it := range r.window {
		ids = append(ids, it.row.ID)
	}
	if !slices.Equal(ids, []int64{1, 0, 2, 3, 4, 5}) {
		t.Errorf("window %v", ids)
	}
}

// TestGate: a Processor over a pool of 4 connections runs 2 lanes per Integration and 2 in all; a lane waiting for the
// gate gives up when its context ends.
func TestGate(t *testing.T) {
	p := NewProcessor(ProcessorConfig{OrgID: 1, Store: sizedStore{newFakeProcess(), 4}, Lanes: 8})
	if p.lanes != 2 || cap(p.gate) != 2 || lanesFor(8, 1) != 1 || lanesFor(3, 0) != 3 {
		t.Fatalf("lanes %d, gate %d", p.lanes, cap(p.gate))
	}
	p.gate <- struct{}{}
	p.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.gated(ctx, func() error { return errors.New("ran") }); !errors.Is(err, context.Canceled) {
		t.Errorf("gated = %v", err)
	}
	<-p.gate
	if err := p.gated(t.Context(), func() error { return nil }); err != nil || len(p.gate) != 1 {
		t.Errorf("gated = %v, %d held", err, len(p.gate))
	}
}

// sizedStore is a store over a pool of a known size.
type sizedStore struct {
	*fakeProcess
	size int
}

func (s sizedStore) PoolSize() int { return s.size }
