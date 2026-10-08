// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package ingest_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

// laneOrderSink records, per fingerprint prefix, the Stored Snapshots whose changes reach it in order, how many run
// at the same time overall and within one prefix, and blocks the Snapshot block until its context ends.
type laneOrderSink struct {
	mu       sync.Mutex
	order    map[string][]int64
	running  map[string]int
	total    int
	maxTotal int
	overlap  int
	hold     time.Duration
	block    int64
	blocked  chan struct{}
}

func newLaneOrderSink(hold time.Duration) *laneOrderSink {
	return &laneOrderSink{order: map[string][]int64{}, running: map[string]int{}, hold: hold,
		blocked: make(chan struct{}, 1)}
}

func (s *laneOrderSink) AlertChanges(ctx context.Context, _ dbgen.DBTX, changes []ingest.AlertChange) (ingest.Routed,
	error) {
	keys := map[string]bool{}
	for _, c := range changes {
		key, _, _ := strings.Cut(c.Fingerprint, "-")
		keys[key] = true
	}
	id := changes[0].StoredSnapshotID
	s.mu.Lock()
	for key := range keys {
		s.order[key] = append(s.order[key], id)
		s.running[key]++
		if s.running[key] > 1 {
			s.overlap++
		}
	}
	s.total++
	s.maxTotal = max(s.maxTotal, s.total)
	block := s.block == id
	s.mu.Unlock()
	if block {
		s.blocked <- struct{}{}
		<-ctx.Done()
	} else {
		time.Sleep(s.hold)
	}
	s.mu.Lock()
	for key := range keys {
		s.running[key]--
	}
	s.total--
	s.mu.Unlock()
	return ingest.Routed{}, ctx.Err()
}

// sharedOf is a Snapshot of the groupKey of name whose one Alert, s-x, starts at minute n: two such groupKeys list
// the same fingerprint, and every Snapshot is a Continuation of it.
func sharedOf(name string, n int) []byte {
	return fmt.Appendf(nil, `{"groupKey":"{}:{alertname=\"%s\"}","status":"firing","alerts":[{"status":"firing",`+
		`"labels":{"alertname":"Shared"},"startsAt":"2026-10-06T%02d:%02d:00Z","fingerprint":"s-x"}]}`, name,
		n/60, n%60)
}

// TestIntegrationLanes is C-06.FR-1 with two replicas: the Alertmanager groups of one Integration are processed at
// the same time, each in arrival order and one Snapshot at a time; two groupKeys that list one fingerprint keep their
// arrival order; every Snapshot is processed and counted once, and the leases are released.
func TestIntegrationLanes(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("lanes"))
		if err != nil {
			t.Fatal(err)
		}
		keys := []string{"g0", "g1", "g2", "g3", "g4", "g5"}
		want := map[string][]int64{}
		at := t0
		for n := range 4 {
			for _, key := range keys {
				at = at.Add(time.Second)
				want[key] = append(want[key], e.storeAt(t, in.ID, at, webhookOf(key, n)))
			}
			for _, group := range []string{"s0", "s1"} {
				at = at.Add(time.Second)
				want["s"] = append(want["s"], e.storeAt(t, in.ID, at, sharedOf(group, len(want["s"]))))
			}
		}
		sink := newLaneOrderSink(15 * time.Millisecond)
		var workers []*ingest.Worker
		for _, owner := range []string{"replica-a", "replica-b"} {
			p := e.processor(owner, sink)
			workers = append(workers, &ingest.Worker{
				Organizations: func(context.Context) ([]int64, error) { return []int64{e.orgID}, nil },
				Processor:     func(int64) (*ingest.Processor, bool) { return p, true },
				Log:           logging.New(&e.processLog, logging.LevelInfo),
				Poll:          50 * time.Millisecond,
			})
		}
		runCtx, stop := context.WithCancel(ctx)
		var wg sync.WaitGroup
		for _, w := range workers {
			wg.Go(func() { w.Run(runCtx) })
		}
		deadline := time.Now().Add(30 * time.Second)
		for e.count(t, `SELECT count(*) FROM stored_snapshots WHERE state = 'pending'`) > 0 {
			if time.Now().After(deadline) {
				t.Fatal("the workers did not finish")
			}
			time.Sleep(20 * time.Millisecond)
		}
		stop()
		wg.Wait()
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for key, ids := range want {
			if !slices.Equal(sink.order[key], ids) {
				t.Errorf("%s in order %v, want %v", key, sink.order[key], ids)
			}
		}
		if sink.overlap != 0 || sink.maxTotal < 2 {
			t.Errorf("overlap within a groupKey %d, at most %d at a time", sink.overlap, sink.maxTotal)
		}
		if n := e.count(t, `SELECT snapshot_count FROM integrations WHERE id = $1`, in.ID); n != 32 {
			t.Errorf("snapshot_count %d", n)
		}
		if n := e.count(t, `SELECT count(*) FROM stored_snapshots WHERE state = 'processed'`); n != 32 {
			t.Errorf("%d processed", n)
		}
		if n := e.count(t, `SELECT count(*) FROM ingest_claims WHERE lease_owner IS NOT NULL`); n != 0 {
			t.Errorf("%d leases were not released", n)
		}
		if n := e.count(t, `SELECT max(episode) FROM alerts`); n != 1 {
			t.Errorf("episode %d: a continuation was taken for a new firing", n)
		}
	})
}

// TestIntegrationKilledReplica is C-06.FR-1 and C-06.FR-20 when a replica dies mid-processing: the Snapshot whose
// transaction it held stays pending, the later Snapshots of its Alertmanager group wait for it while another
// Alertmanager group goes on; another replica takes the Integration only once the lease ran out, and processes the
// rest in order, each Snapshot counted once.
func TestIntegrationKilledReplica(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("killed"))
		if err != nil {
			t.Fatal(err)
		}
		// The Alertmanager route of every groupKey here exists before: a transaction that creates it holds up the
		// others of the route until it commits.
		e.storeAt(t, in.ID, t0.Add(-time.Minute), webhookOf("k0", 0))
		if n, err := e.processor("replica-a", nil).Drain(ctx); n != 1 || err != nil {
			t.Fatalf("the first snapshot: %d, %v", n, err)
		}
		var k1, k2 []int64
		for n := range 3 {
			k1 = append(k1, e.storeAt(t, in.ID, t0.Add(time.Duration(2*n)*time.Second), webhookOf("k1", n)))
			k2 = append(k2, e.storeAt(t, in.ID, t0.Add(time.Duration(2*n+1)*time.Second), webhookOf("k2", n)))
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE ingest_claims SET lease_owner = 'replica-a',
			lease_until = now() + interval '1 hour' WHERE integration_id = $1`, in.ID); err != nil {
			t.Fatal(err)
		}
		sink := newLaneOrderSink(0)
		sink.block = k1[0]
		a := e.processor("replica-a", sink)
		killCtx, kill := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			_, err := a.ProcessPending(killCtx, in.ID)
			done <- err
		}()
		<-sink.blocked
		deadline := time.Now().Add(10 * time.Second)
		for e.count(t, `SELECT count(*) FROM stored_snapshots WHERE id = ANY($1) AND state = 'processed'`, k2) < 3 {
			if time.Now().After(deadline) {
				t.Fatal("the other alertmanager group waited")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if n := e.count(t, `SELECT count(*) FROM stored_snapshots WHERE id = ANY($1) AND state = 'pending'`,
			k1); n != 3 {
			t.Errorf("%d of the blocked alertmanager group pending", n)
		}
		kill()
		if err := <-done; err == nil {
			t.Error("the killed replica reported no error")
		}
		b := e.processor("replica-b", sink)
		if n, err := b.Drain(ctx); n != 0 || err != nil {
			t.Errorf("b took a leased integration: %d, %v", n, err)
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE ingest_claims SET lease_until = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		sink.mu.Lock()
		sink.block = 0
		sink.mu.Unlock()
		if n, err := b.Drain(ctx); n != 3 || err != nil {
			t.Errorf("after the lease ran out: %d, %v", n, err)
		}
		sink.mu.Lock()
		defer sink.mu.Unlock()
		// k1's first Snapshot reached the Sink twice: once in the transaction the kill rolled back.
		if got := sink.order["k1"]; !slices.Equal(got, append([]int64{k1[0]}, k1...)) ||
			!slices.Equal(sink.order["k2"], k2) {
			t.Errorf("k1 %v, k2 %v", got, sink.order["k2"])
		}
		if n := e.count(t, `SELECT snapshot_count FROM integrations WHERE id = $1`, in.ID); n != 7 {
			t.Errorf("snapshot_count %d", n)
		}
		if n := e.count(t, `SELECT count(*) FROM stored_snapshots WHERE state = 'processed'`); n != 7 {
			t.Errorf("%d processed", n)
		}
	})
}

// TestIntegrationClaimChoosesOnce: the claim of Integrations runs its LIMIT … FOR UPDATE SKIP LOCKED choice once per
// call, also planned as a nested loop over the claim rows (S-065).
func TestIntegrationClaimChoosesOnce(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		var ids []int64
		for _, name := range []string{"c1", "c2", "c3"} {
			in, err := e.ints.Create(ctx, by, input(name))
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, in.ID)
		}
		if err := dbgen.New(e.d.Pool).EnsureIngestClaims(ctx, dbgen.EnsureIngestClaimsParams{OrgID: e.orgID,
			IntegrationIds: ids}); err != nil {
			t.Fatal(err)
		}
		var claimed int
		loops := dbtest.LockRowsLoops(t, e.d.Pool.Config().ConnString(), func(ctx context.Context, conn *pgx.Conn) {
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			rows, err := dbgen.New(tx).ClaimIntegrations(ctx, dbgen.ClaimIntegrationsParams{OrgID: e.orgID,
				Owner: pgtype.Text{String: "probe", Valid: true}, LeaseUntil: pgtype.Timestamptz{Time: t0.Add(time.Hour),
					Valid: true}, IntegrationIds: ids, Now: t0, BatchSize: 2})
			if err != nil {
				t.Fatal(err)
			}
			claimed = len(rows)
		})
		if loops != 1 || claimed != 2 {
			t.Errorf("the choice ran %d times, claimed %d", loops, claimed)
		}
	})
}
