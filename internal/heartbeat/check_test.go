// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package heartbeat

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/heartbeat/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

func newChecker(store *fakeStore, c clock.Clock, log *bytes.Buffer) *Checker {
	return &Checker{Store: store, Business: c, Log: logging.New(log, logging.LevelInfo)}
}

// TestCheck covers C-07.AC-1 and C-07.AC-2 on the check: a live Heartbeat whose last signal is older than its timeout
// becomes lost since that signal and raises MusterHeartbeatLost with severity critical, its id and name and its
// Static labels, its own labels winning over a Static label of the same name; a waiting one is never lost; a second
// run changes nothing; muster_heartbeat_lost is 1 for the lost Integration and 0 for the others that are on.
func TestCheck(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	store.add(1, StateLive, at(t0))
	store.add(2, StateWaiting, nil)
	store.add(3, StateLive, at(t0.Add(time.Minute)))
	store.add(4, StateNotConfigured, nil)
	c := clock.NewManual(t0.Add(5 * time.Minute))
	var log bytes.Buffer
	ch := newChecker(store, c, &log)

	if err := ch.Check(ctx, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if store.rows[1].HeartbeatState != StateLive || len(store.internal) != 0 {
		t.Fatalf("lost at exactly the timeout: %+v", store.rows[1])
	}
	c.Advance(time.Second)
	for range 2 {
		if err := ch.Check(ctx, []int64{1}); err != nil {
			t.Fatal(err)
		}
	}
	r := store.rows[1]
	if r.HeartbeatState != StateLost || !r.HeartbeatLostSince.Time.Equal(t0) || store.rows[2].HeartbeatState !=
		StateWaiting || store.rows[3].HeartbeatState != StateLive || len(store.internal) != 1 || len(store.hints) != 1 {
		t.Fatalf("after the timeout: %+v, %d raises, hints %v", r, len(store.internal), store.hints)
	}
	status, labels := store.alert(t, 0)
	want := map[string]any{"alertname": "MusterHeartbeatLost", "severity": "critical", "integration": r.PublicID,
		"integration_name": "hb", "env": "prod"}
	if status != "firing" || len(labels) != len(want) {
		t.Fatalf("raise %s %v", status, labels)
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %v, want %v", k, labels[k], v)
		}
	}
	l := lines(t, &log)
	if len(l) != 1 || l[0]["event"] != "heartbeat_lost" || l[0]["integration"] != r.PublicID ||
		l[0]["last_signal_at"] != t0.Format(time.RFC3339) {
		t.Errorf("log %v", l)
	}
	if v := metrics.HeartbeatLost.With(r.PublicID).Get(); v != 1 {
		t.Errorf("muster_heartbeat_lost of the lost one = %v", v)
	}
	if v := metrics.HeartbeatLost.With(store.rows[2].PublicID).Get(); v != 0 {
		t.Errorf("muster_heartbeat_lost of the waiting one = %v", v)
	}

	// C-07.AC-2: before the first signal nothing is raised, however long it takes.
	c.Advance(30 * 24 * time.Hour)
	store.rows[1].HeartbeatState = StateWaiting
	store.rows[3].HeartbeatState = StateWaiting
	if err := ch.Check(ctx, []int64{1}); err != nil || len(store.internal) != 1 {
		t.Errorf("waiting heartbeats: %v, %d raises", err, len(store.internal))
	}

	// An Integration whose Heartbeat was turned off leaves the metric.
	store.rows[2].HeartbeatEnabled = false
	if err := ch.Check(ctx, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if ch.exported[store.rows[2].PublicID] {
		t.Error("the series of an integration without a heartbeat stays")
	}
}

// TestCheckAfterDowntime covers C-07.FR-4: after a downtime the Leader recorded on taking over, the timeout counts
// from the moment it started leading, not from the last signal before the outage.
func TestCheckAfterDowntime(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore()
	store.add(1, StateLive, at(t0))
	start := t0.Add(time.Hour)
	store.leader = &dbgen.GetLeaderStartRow{LeaderSince: pgtype.Timestamptz{Time: start, Valid: true},
		AfterDowntime: true}
	c := clock.NewManual(start.Add(5 * time.Minute))
	var log bytes.Buffer
	ch := newChecker(store, c, &log)
	if err := ch.Check(ctx, []int64{1}); err != nil || store.rows[1].HeartbeatState != StateLive {
		t.Fatalf("lost within the timeout after the downtime: %v %+v", err, store.rows[1])
	}
	c.Advance(time.Second)
	if err := ch.Check(ctx, []int64{1}); err != nil || store.rows[1].HeartbeatState != StateLost ||
		!store.rows[1].HeartbeatLostSince.Time.Equal(t0) {
		t.Fatalf("after the timeout from the takeover: %v %+v", err, store.rows[1])
	}

	// A Leader that took over without a downtime measures from the last signal.
	store.add(2, StateLive, at(t0))
	store.leader.AfterDowntime = false
	c.Set(t0.Add(5*time.Minute + time.Second))
	if err := ch.Check(ctx, []int64{1}); err != nil || store.rows[2].HeartbeatState != StateLost {
		t.Errorf("without a downtime: %v %+v", err, store.rows[2])
	}
}

func TestOverdue(t *testing.T) {
	timeout := 5 * time.Minute
	for name, tt := range map[string]struct {
		last, from *time.Time
		now        time.Time
		want       bool
	}{
		"within":            {last: at(t0), now: t0.Add(timeout)},
		"past":              {last: at(t0), now: t0.Add(timeout + 1), want: true},
		"from a later time": {last: at(t0), from: at(t0.Add(time.Hour)), now: t0.Add(time.Hour + timeout)},
		"from an earlier":   {last: at(t0), from: at(t0.Add(-time.Hour)), now: t0.Add(timeout + 1), want: true},
		"never signalled":   {now: t0},
	} {
		if got := overdue(tt.last, tt.from, timeout, tt.now); got != tt.want {
			t.Errorf("%s: overdue = %v", name, got)
		}
	}
}

// TestCheckErrors: a failed read stops the check, a failed change of one Integration is returned, and a failed listing
// keeps the series it cannot see.
func TestCheckErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, name := range []string{"GetLeaderStart", "ListOverdue", "LockHeartbeat", "MarkLost", "FindBuiltinIntegration",
		"Notify", "ListHeartbeats"} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			store.add(1, StateLive, at(t0))
			store.fail[name] = boom
			var log bytes.Buffer
			ch := newChecker(store, clock.NewManual(t0.Add(time.Hour)), &log)
			ch.exported = map[string]bool{"NTZZZZZZZZZZZZ": true}
			if err := ch.Check(t.Context(), []int64{1}); !errors.Is(err, boom) {
				t.Errorf("Check = %v", err)
			}
			if name == "ListHeartbeats" && !ch.exported["NTZZZZZZZZZZZZ"] {
				t.Error("a failed listing removed a series")
			}
		})
	}
	store := newFakeStore()
	store.add(1, StateLive, at(t0)).StaticLabels = []byte("not json")
	var log bytes.Buffer
	if err := newChecker(store, clock.NewManual(t0.Add(time.Hour)), &log).Check(t.Context(), []int64{1}); err == nil {
		t.Error("static labels that do not decode raised")
	}
}
