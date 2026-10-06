// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package ingest_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/fakes/fakealertmanager"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// processor is the Processor of a replica named owner over the database of e, on its manual business clock.
func (e *env) processor(owner string, sink ingest.Sink) *ingest.Processor {
	return ingest.NewProcessor(ingest.ProcessorConfig{OrgID: e.orgID, Store: ingest.NewProcessStore(e.d.Pool),
		Business: e.clock, Log: logging.New(&e.processLog, logging.LevelInfo), Sink: sink,
		Lease: db.Lease{Owner: owner, Duration: ingest.Lease, Clocks: clock.Clocks{Business: e.clock,
			Real: clock.Real{}}}})
}

// TestIntegrationFacts runs every scenario of the fake Alertmanager — one per verified Alertmanager fact that
// processing relies on, F-033 to F-044, F-046 and F-048 to F-053 — through ingestion and processing on PostgreSQL
// with a manual clock, and checks the requirement that cites the fact.
func TestIntegrationFacts(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		for _, sc := range fakealertmanager.Scenarios() {
			t.Run(sc.Fact, func(t *testing.T) {
				t.Logf("%s (%s): %s", sc.Fact, sc.Requirement, sc.Title)
				runScenario(t, e, sc)
			})
		}
	})
}

func runScenario(t *testing.T, e *env, sc fakealertmanager.Scenario) {
	ctx := t.Context()
	start := t0
	e.clock.Set(start)
	in, err := e.ints.Create(ctx, by, integrations.Input{Name: strings.ToLower(sc.Fact),
		ConnectionMode: integrations.ConnectionWebhookOnly, DuplicateWindowSeconds: 45})
	if err != nil {
		t.Fatal(err)
	}
	fake := fakealertmanager.New()
	fake.SetClock(e.clock.Now)
	if err := fake.Register(fakealertmanager.Receiver{Name: fakealertmanager.ScenarioReceiver,
		URL: "http://localhost:8081/api/v1/ingest"}); err != nil {
		t.Fatal(err)
	}
	p := e.processor("replica-a", nil)
	keys := map[string]string{}
	for i, st := range sc.Steps {
		e.clock.Set(start.Add(st.At))
		switch {
		case st.PutGroup != nil:
			if keys[st.Group], err = fake.PutGroup(st.Group, *st.PutGroup); err != nil {
				t.Fatal(err)
			}
		case st.PutAlert != nil:
			spec := *st.PutAlert
			if st.StartsAt != nil {
				spec.StartsAt = start.Add(*st.StartsAt).Format(time.RFC3339Nano)
			}
			if st.EndsAt != nil {
				spec.EndsAt = start.Add(*st.EndsAt).Format(time.RFC3339Nano)
			}
			if err := fake.PutAlert(st.Group, st.Alert, spec); err != nil {
				t.Fatal(err)
			}
		case st.Remove:
			if err := fake.RemoveAlert(st.Group, st.Alert); err != nil {
				t.Fatal(err)
			}
		case st.Notify != nil:
			_, _, snaps, err := fake.Snapshots(st.Group, *st.Notify)
			if err != nil {
				t.Fatal(err)
			}
			for j, sn := range snaps {
				if j > 0 {
					e.clock.Advance(time.Duration(st.Notify.CopyDelayMs) * time.Millisecond)
				}
				if _, err := e.snapshots.Store(ctx, ingest.Received{IntegrationID: in.ID, Body: sn.Body,
					ContentType: "application/json"}); err != nil {
					t.Fatal(err)
				}
				if n, err := p.Drain(ctx); err != nil || n != 1 {
					t.Fatalf("step %d: processed %d, %v", i, n, err)
				}
			}
		case st.Expect != nil:
			check(t, e, i, in, fake, keys, start, *st.Expect)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM stored_snapshots WHERE integration_id = $1 AND state <> 'processed'`,
		in.ID); n != 0 {
		t.Errorf("%d stored snapshots were not processed", n)
	}
}

func check(t *testing.T, e *env, step int, in integrations.Integration, fake *fakealertmanager.Fake,
	keys map[string]string, start time.Time, x fakealertmanager.Expect) {
	t.Helper()
	ctx := t.Context()
	row := func(name string) (status, reason string, episode int64, startsAt time.Time) {
		t.Helper()
		group, alert, _ := strings.Cut(name, "/")
		err := e.d.Pool.QueryRow(ctx, `SELECT status, coalesce(resolve_reason, ''), episode, starts_at FROM alerts
			WHERE integration_id = $1 AND fingerprint = $2`, in.ID, fake.Fingerprint(group, alert)).Scan(&status,
			&reason, &episode, &startsAt)
		if err != nil {
			t.Fatalf("step %d: alert %s: %v", step, name, err)
		}
		return status, reason, episode, startsAt
	}
	for _, name := range x.Firing {
		if status, _, _, _ := row(name); status != "firing" {
			t.Errorf("step %d: %s is %s, want firing", step, name, status)
		}
	}
	for name, want := range x.Resolved {
		if status, reason, _, _ := row(name); status != "resolved" || reason != want {
			t.Errorf("step %d: %s is %s/%s, want resolved/%s", step, name, status, reason, want)
		}
	}
	for name, want := range x.Episodes {
		if _, _, episode, _ := row(name); episode != want {
			t.Errorf("step %d: %s has episode %d, want %d", step, name, episode, want)
		}
	}
	for name, want := range x.StartsAt {
		if _, _, _, startsAt := row(name); !startsAt.Equal(start.Add(want)) {
			t.Errorf("step %d: %s starts at %v, want %v", step, name, startsAt, start.Add(want))
		}
	}
	for group, want := range x.Truncated {
		var truncated bool
		if err := e.d.Pool.QueryRow(ctx, `SELECT truncated FROM alertmanager_groups WHERE integration_id = $1
			AND group_key = $2`, in.ID, keys[group]).Scan(&truncated); err != nil || truncated != want {
			t.Errorf("step %d: group %s truncated %v, %v; want %v", step, group, truncated, err, want)
		}
	}
	for path, bounds := range x.Learned {
		var ms int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT learned_repeat_interval_ms FROM alertmanager_routes
			WHERE integration_id = $1 AND route_path = $2`, in.ID, path).Scan(&ms); err != nil {
			t.Errorf("step %d: route %s: %v", step, path, err)
			continue
		}
		if learned := time.Duration(ms) * time.Millisecond; learned < bounds[0] || learned > bounds[1] {
			t.Errorf("step %d: route %s learned %v, want %v to %v", step, path, learned, bounds[0], bounds[1])
		}
	}
	if x.Dropped != nil {
		if got := metrics.IngestResolvedDropped.With(in.PublicID).Get(); fmt.Sprint(got) != fmt.Sprint(*x.Dropped) {
			t.Errorf("step %d: dropped %d, want %d", step, got, *x.Dropped)
		}
	}
	if x.Alerts != nil {
		if n := e.count(t, `SELECT count(*) FROM alerts WHERE integration_id = $1`, in.ID); n != int64(*x.Alerts) {
			t.Errorf("step %d: %d alerts, want %d", step, n, *x.Alerts)
		}
	}
}
