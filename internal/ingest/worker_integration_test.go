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
	"sync/atomic"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/matchers"
)

// orderSink records the Stored Snapshots in the order their changes reach it, per Integration (the fingerprint's
// prefix), and how many transactions of one Integration ran at the same time.
type orderSink struct {
	mu      sync.Mutex
	order   map[string][]int64
	running map[string]int
	overlap atomic.Int32
	slow    map[string]time.Duration
	at      map[string]time.Time
}

func newOrderSink() *orderSink {
	return &orderSink{order: map[string][]int64{}, running: map[string]int{}, slow: map[string]time.Duration{},
		at: map[string]time.Time{}}
}

func (s *orderSink) AlertChanges(_ context.Context, _ dbgen.DBTX, changes []ingest.AlertChange) (ingest.Routed,
	error) {
	key, _, _ := strings.Cut(changes[0].Fingerprint, "-")
	s.mu.Lock()
	s.running[key]++
	if s.running[key] > 1 {
		s.overlap.Add(1)
	}
	s.order[key] = append(s.order[key], changes[0].StoredSnapshotID)
	s.at[key] = time.Now()
	wait := s.slow[key]
	s.mu.Unlock()
	time.Sleep(wait)
	s.mu.Lock()
	s.running[key]--
	s.mu.Unlock()
	return ingest.Routed{}, nil
}

// webhookOf is a Snapshot of one Alert of the Integration key whose startsAt is minute n, so that every Snapshot
// changes the Alert: a new firing, then Continuations.
func webhookOf(key string, n int) []byte {
	return fmt.Appendf(nil, `{"groupKey":"{}:{alertname=\"%s\"}","status":"firing","alerts":[{"status":"firing",`+
		`"labels":{"alertname":%q},"startsAt":"2026-10-06T%02d:%02d:00Z","fingerprint":"%s-a"}]}`, key, key,
		n/60, n%60, key)
}

func (e *env) storeAt(t *testing.T, integration int64, at time.Time, body []byte) int64 {
	t.Helper()
	e.clock.Set(at)
	if _, err := e.snapshots.Store(t.Context(), ingest.Received{IntegrationID: integration, Body: body}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), `SELECT max(id) FROM stored_snapshots WHERE integration_id = $1`,
		integration).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestIntegrationWorkers is C-06.FR-1 with two replicas: the Stored Snapshots of one Integration are processed in
// arrival order and never two at a time, while a Snapshot of another Integration is not held up by them; a body that
// is not JSON fails alone (C-06.FR-20); the Integration's count grows with each Snapshot.
func TestIntegrationWorkers(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		a, err := e.ints.Create(ctx, by, input("busy"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := e.ints.Create(ctx, by, input("quiet"))
		if err != nil {
			t.Fatal(err)
		}
		var want []int64
		for n := range 40 {
			body := webhookOf("busy", n)
			if n == 17 {
				body = []byte("not json")
			}
			id := e.storeAt(t, a.ID, t0.Add(time.Duration(n)*time.Second), body)
			if n != 17 {
				want = append(want, id)
			}
		}
		sink := newOrderSink()
		sink.slow["busy"] = 20 * time.Millisecond
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
		started := time.Now()
		time.Sleep(100 * time.Millisecond)
		quiet := e.storeAt(t, b.ID, t0.Add(time.Minute), webhookOf("quiet", 0))
		for _, w := range workers {
			w.Wake()
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
		if t.Failed() || len(sink.order["busy"]) == 0 {
			t.Log(e.processLog.String())
		}
		if !slices.Equal(sink.order["busy"], want) {
			t.Errorf("order %v, want %v", sink.order["busy"], want)
		}
		if n := sink.overlap.Load(); n != 0 {
			t.Errorf("%d snapshots of one integration overlapped", n)
		}
		if !slices.Equal(sink.order["quiet"], []int64{quiet}) || !sink.at["quiet"].Before(sink.at["busy"]) {
			t.Errorf("the quiet integration waited: %v after %v, busy done %v after", sink.at["quiet"].Sub(started),
				started, sink.at["busy"].Sub(started))
		}
		var state, reason string
		if err := e.d.Pool.QueryRow(ctx, `SELECT state, processing_error FROM stored_snapshots WHERE
			processing_error IS NOT NULL`).Scan(&state, &reason); err != nil || state != "failed" ||
			!strings.HasPrefix(reason, "the body is not valid JSON") {
			t.Errorf("failed snapshot %s %q, %v", state, reason, err)
		}
		if n := e.count(t, `SELECT snapshot_count FROM integrations WHERE id = $1`, a.ID); n != 40 {
			t.Errorf("snapshot_count %d", n)
		}
		if n := e.count(t, `SELECT episode FROM alerts WHERE fingerprint = 'busy-a'`); n != 1 {
			t.Errorf("episode %d: the continuations were taken for new firings", n)
		}
		if n := e.count(t, `SELECT count(*) FROM ingest_claims WHERE lease_owner IS NOT NULL`); n != 0 {
			t.Errorf("%d leases were not released", n)
		}
		if lines := strings.Count(e.processLog.String(), `"event":"snapshot_processed"`); lines != 40 {
			t.Errorf("%d snapshot_processed lines", lines)
		}
	})
}

// TestIntegrationLease: a replica whose lease ran out and went to another replica stops processing the Integration
// at its next Snapshot; the claim skips an Integration another replica leases.
func TestIntegrationLease(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("leased"))
		if err != nil {
			t.Fatal(err)
		}
		for n := range 3 {
			e.storeAt(t, in.ID, t0.Add(time.Duration(n)*time.Second), webhookOf("leased", n))
		}
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO ingest_claims (integration_id, org_id, lease_owner, lease_until)
			VALUES ($1, $2, 'replica-b', now() + interval '1 hour')`, in.ID, e.orgID); err != nil {
			t.Fatal(err)
		}
		a := e.processor("replica-a", nil)
		if n, err := a.Drain(ctx); n != 0 || err != nil {
			t.Errorf("a claimed a leased integration: %d, %v", n, err)
		}
		if n, err := a.ProcessPending(ctx, in.ID); n != 0 || err != nil {
			t.Errorf("a processed without the lease: %d, %v", n, err)
		}
		if _, err := e.d.Pool.Exec(ctx, `UPDATE ingest_claims SET lease_until = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if n, err := a.Drain(ctx); n != 3 || err != nil {
			t.Errorf("after the lease ran out: %d, %v", n, err)
		}
		if n := e.count(t, `SELECT count(*) FROM ingest_claims WHERE lease_owner IS NULL`); n != 1 {
			t.Errorf("the lease was not released")
		}
	})
}

// TestIntegrationAlertsView is C-06.FR-19 against PostgreSQL: the Alerts of an Integration with their groupKeys,
// filtered by state, Matchers and text and sorted with a cursor, and a resolved one only within
// retention.alert_details.
func TestIntegrationAlertsView(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("view"))
		if err != nil {
			t.Fatal(err)
		}
		alert := func(name, status string) string {
			starts := "2026-10-06T11:00:00Z"
			if strings.HasPrefix(name, "web") {
				starts = "2026-10-06T11:30:00Z"
			}
			return `{"status":"` + status + `","labels":{"alertname":"Disk","instance":"` + name +
				`"},"startsAt":"` + starts + `"}`
		}
		group := func(key string, alerts ...string) []byte {
			return []byte(`{"groupKey":"` + key + `","status":"firing","alerts":[` + strings.Join(alerts, ",") + `]}`)
		}
		e.storeAt(t, in.ID, t0, group(`{}:{alertname=\"Disk\"}`, alert("db-1", "firing"), alert("db-2", "firing"),
			alert("web-1", "firing")))
		e.storeAt(t, in.ID, t0.Add(time.Minute), group(`{}/{team=\"db\"}:{alertname=\"Disk\"}`,
			alert("db-1", "firing")))
		e.storeAt(t, in.ID, t0.Add(2*time.Minute), group(`{}:{alertname=\"Disk\"}`, alert("db-1", "firing"),
			alert("db-2", "resolved"), alert("web-1", "firing")))
		if n, err := e.processor("replica-a", nil).Drain(ctx); n != 3 || err != nil {
			t.Fatalf("Drain = %d, %v", n, err)
		}
		v := ingest.NewAlertsView(e.orgID, ingest.NewProcessStore(e.d.Pool), e.clock)
		m, _ := matchers.Parse(`instance=~"db-.*"`)
		page, err := v.List(ctx, ingest.AlertFilter{Integration: in.PublicID, Matchers: []matchers.Matcher{m},
			Sort: ingest.SortLastSeenDesc, Limit: 1})
		if err != nil || len(page.Alerts) != 1 || page.Next == nil {
			t.Fatalf("first page %+v, %v", page, err)
		}
		first := page.Alerts[0]
		if first.Labels["instance"] != "db-1" || first.Labels["cluster"] != "view" ||
			!slices.Equal(first.GroupKeys, []string{`{}/{team="db"}:{alertname="Disk"}`, `{}:{alertname="Disk"}`}) {
			t.Errorf("first %+v", first)
		}
		page, err = v.List(ctx, ingest.AlertFilter{Integration: in.PublicID, Matchers: []matchers.Matcher{m},
			Sort: ingest.SortLastSeenDesc, Limit: 1, After: page.Next})
		if err != nil || len(page.Alerts) != 1 || page.Next != nil || page.Alerts[0].State != "resolved" ||
			*page.Alerts[0].Reason != "resolved" {
			t.Fatalf("second page %+v, %v", page, err)
		}
		for _, tt := range []struct {
			f    ingest.AlertFilter
			want []string
		}{
			{ingest.AlertFilter{State: "firing", Sort: ingest.SortStarts}, []string{"db-1", "web-1"}},
			{ingest.AlertFilter{Query: "WEB", Sort: ingest.SortStartsDesc}, []string{"web-1"}},
			{ingest.AlertFilter{Matchers: []matchers.Matcher{eq("instance", "db-2")}, Sort: ingest.SortLastSeen},
				[]string{"db-2"}},
		} {
			tt.f.Integration, tt.f.Limit = in.PublicID, 10
			page, err := v.List(ctx, tt.f)
			var got []string
			for _, a := range page.Alerts {
				got = append(got, a.Labels["instance"])
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("%+v: %v, %v", tt.f, got, err)
			}
		}
		e.clock.Set(t0.Add(91 * 24 * time.Hour))
		page, err = v.List(ctx, ingest.AlertFilter{Integration: in.PublicID, Limit: 10})
		if err != nil || len(page.Alerts) != 2 {
			t.Errorf("after retention.alert_details: %d alerts, %v", len(page.Alerts), err)
		}
	})
}

func eq(name, value string) matchers.Matcher {
	m, _ := matchers.New(name, matchers.Equal, value)
	return m
}

// TestIntegrationNanosecondStartsAt: a startsAt to the nanosecond, as Alertmanager stamps an Alert posted without one,
// received again is the same firing, not a Continuation, although PostgreSQL keeps microseconds.
func TestIntegrationNanosecondStartsAt(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("nanos"))
		if err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"groupKey":"{}:{}","status":"firing","alerts":[{"status":"firing","labels":{"a":"b"},` +
			`"startsAt":"2026-10-06T11:00:00.123456789Z"}]}`)
		e.storeAt(t, in.ID, t0, body)
		e.storeAt(t, in.ID, t0.Add(time.Minute), body)
		sink := newOrderSink()
		if n, err := e.processor("replica-a", sink).Drain(ctx); n != 2 || err != nil {
			t.Fatalf("Drain = %d, %v", n, err)
		}
		var continued int
		for _, ids := range sink.order {
			continued += len(ids)
		}
		if continued != 1 {
			t.Errorf("%d snapshots made changes, want only the first", continued)
		}
	})
}
