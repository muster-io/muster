// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestLeaderFailover is C-02.AC-3: replica A reaches the database through a proxy and leads; replica B reaches it
// directly. Cutting A's network fences A within leader.fencing_timeout, and B exports muster_leader 1 within 60 s.
func TestLeaderFailover(t *testing.T) {
	const (
		fencingTimeout = 15 * time.Second
		handoverBound  = 60 * time.Second
	)
	h := Start(t, FakesInProcess)
	hole := h.Blackhole(t)
	// Closed before the replicas stop, so that A's shutdown does not wait for the cut network.
	defer hole.Close()

	a := h.StartReplica(t, ReplicaOptions{Env: map[string]string{"MUSTER_DATABASE_URL": hole.DatabaseURL(h)}})
	a.waitFor(t, "replica A to lead", func() bool { return a.Metric(t, "muster_leader") == "1" })
	b := h.StartReplica(t, ReplicaOptions{ListenApp: ":8080", ListenIngest: ":8081", ListenInternal: ":8082"})
	if got := b.Metric(t, "muster_leader"); got != "0" {
		t.Fatalf("replica B exports muster_leader %q while A leads, want 0", got)
	}
	// The first skew check runs at start beside the listeners, so readiness can answer before it has set the gauge;
	// it is bounded by its timeout, well within the wait.
	b.waitFor(t, "replica B to export muster_clock_skew_seconds", func() bool {
		return b.Metric(t, "muster_clock_skew_seconds") != ""
	})

	cut := time.Now()
	hole.Cut()
	deadline := cut.Add(handoverBound)
	for b.Metric(t, "muster_leader") != "1" {
		if time.Now().After(deadline) {
			t.Fatalf("replica B did not lead within %v of the cut; output of A:\n%s\noutput of B:\n%s", handoverBound,
				a.Output(), b.Output())
		}
		time.Sleep(pollInterval)
	}
	took := time.Since(cut)

	line, lost, ok := a.LogLine("leadership_lost")
	if !ok {
		t.Fatalf("replica A did not log leadership_lost; output:\n%s", a.Output())
	}
	// The deadline counts from the last successful ping, which came before the cut; stopping the tasks and writing
	// the line take a moment more.
	const slack = 500 * time.Millisecond
	if line["level"] != "WARN" || lost.Sub(cut) > fencingTimeout+slack {
		t.Errorf("replica A logged %v %v after the cut, want WARN within %v", line, lost.Sub(cut), fencingTimeout)
	}
	if got := a.Metric(t, "muster_leader"); got != "0" {
		t.Errorf("replica A exports muster_leader %q after it was fenced, want 0", got)
	}
	if _, _, ok := b.LogLine("leadership_acquired"); !ok {
		t.Errorf("replica B did not log leadership_acquired; output:\n%s", b.Output())
	}
	t.Logf("cut at %s: A logged leadership_lost after %v, B exported muster_leader 1 after %v",
		cut.UTC().Format(time.RFC3339Nano), lost.Sub(cut).Round(time.Millisecond), took.Round(time.Millisecond))
}

// TestDowntimeAfterAFullStop is C-02.AC-4 on two replicas: both stop, the alive mark is moved 10 minutes back as if
// Muster had been stopped that long, and both start again. The new Leader records one downtime of about 600 s
// although a replica that had just started refreshed its record before the takeover, and the recovery window is at
// most 15 min.
func TestDowntimeAfterAFullStop(t *testing.T) {
	h := Start(t, FakesInProcess)
	a := h.StartReplica(t, ReplicaOptions{})
	b := h.StartReplica(t, ReplicaOptions{ListenApp: ":8080", ListenIngest: ":8081", ListenInternal: ":8082"})
	conn, err := pgx.Connect(t.Context(), h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	query := func(sql string) string {
		var out string
		if err := conn.QueryRow(t.Context(), sql).Scan(&out); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}
	a.waitFor(t, "the alive mark", func() bool {
		return query("SELECT count(*)::text FROM runtime_state WHERE alive_at IS NOT NULL") == "1"
	})
	a.Stop(t)
	b.Stop(t)
	if _, err := conn.Exec(t.Context(), "UPDATE runtime_state SET alive_at = alive_at - interval '10 minutes'"); err != nil {
		t.Fatal(err)
	}
	// A third replica started just now and refreshed its record before the new Leader takes over: it did not run
	// across the gap, so it must not hide the downtime.
	if _, err := conn.Exec(t.Context(), `INSERT INTO replicas (replica_id, version, key_ids, started_at, refreshed_at)
		VALUES ('just-started', 'test', '{}', clock_timestamp(), clock_timestamp())`); err != nil {
		t.Fatal(err)
	}

	for _, r := range []*Replica{a, b} {
		r.Start(t)
	}
	var recorded map[string]any
	a.waitFor(t, "downtime_recorded", func() bool {
		for _, r := range []*Replica{a, b} {
			if line, _, ok := r.LogLine("downtime_recorded"); ok {
				recorded = line
				return true
			}
		}
		return false
	})
	if d, _ := recorded["duration_seconds"].(float64); recorded["level"] != "WARN" || d < 600 || d > 660 {
		t.Errorf("downtime_recorded %v, want WARN with a duration of about 600 s", recorded)
	}
	if got := query("SELECT count(*)::text FROM downtime_periods"); got != "1" {
		t.Errorf("%s downtime periods, want 1", got)
	}
	if got := query("SELECT (recovery_until - updated_at <= interval '15 minutes')::text FROM runtime_state"); got !=
		"true" {
		t.Errorf("the recovery window is longer than 15 minutes")
	}
	if n := strings.Count(a.Output()+b.Output(), `"event":"downtime_recorded"`); n != 1 {
		t.Errorf("downtime_recorded logged %d times", n)
	}
	t.Logf("%v", recorded)
}
