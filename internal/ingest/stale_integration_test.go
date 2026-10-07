// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package ingest_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/heartbeat"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// staleWorld drives Integrations through Snapshots, Heartbeat signals, the Leader's Heartbeat check and the Stale
// scan on PostgreSQL, on the manual business clock of env, with times as offsets from t0.
type staleWorld struct {
	e       *env
	signals *heartbeat.Service
	checker *heartbeat.Checker
	p       *ingest.Processor
	scanner *ingest.Processor
	log     *bytes.Buffer
}

func newStaleWorld(t *testing.T, e *env) *staleWorld {
	t.Helper()
	var log bytes.Buffer
	logger := logging.New(&log, logging.LevelInfo)
	return &staleWorld{e: e, log: &log,
		signals: heartbeat.New(heartbeat.Config{OrgID: e.orgID, Store: heartbeat.NewStore(e.d.Pool), Business: e.clock,
			Log: logger}),
		checker: &heartbeat.Checker{Store: heartbeat.NewStore(e.d.Pool), Business: e.clock, Log: logger},
		p:       e.processor("replica-a", nil), scanner: e.processor("replica-a/stale-scan", nil)}
}

func (w *staleWorld) at(d time.Duration) { w.e.clock.Set(t0.Add(d)) }

// create creates an Integration with the Static label env=prod and its Heartbeat on or off.
func (w *staleWorld) create(t *testing.T, name string, heartbeatOn bool) integrations.Integration {
	t.Helper()
	in, err := w.e.ints.Create(t.Context(), by, integrations.Input{Name: name,
		ConnectionMode: integrations.ConnectionWebhookOnly, StaticLabels: map[string]string{"env": "prod"},
		DuplicateWindowSeconds: 45, Heartbeat: integrations.HeartbeatInput{Enabled: heartbeatOn}})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// send stores a Snapshot of the Integration at the current time and processes it.
func (w *staleWorld) send(t *testing.T, in integrations.Integration, body []byte) {
	t.Helper()
	if _, err := w.e.snapshots.Store(t.Context(), ingest.Received{IntegrationID: in.ID, Body: body}); err != nil {
		t.Fatal(err)
	}
	w.drain(t)
}

func (w *staleWorld) drain(t *testing.T) {
	t.Helper()
	if _, err := w.p.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (w *staleWorld) signal(t *testing.T, in integrations.Integration) {
	t.Helper()
	if err := w.signals.Signal(t.Context(), in.ID); err != nil {
		t.Fatal(err)
	}
}

// leader runs the Heartbeat check and the Stale scan, then processes what they raised or resolved.
func (w *staleWorld) leader(t *testing.T) {
	t.Helper()
	if err := w.checker.Check(t.Context(), []int64{w.e.orgID}); err != nil {
		t.Fatal(err)
	}
	if err := w.scanner.StaleScan(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.drain(t)
}

// state is the status and resolve reason of the Alert with the fingerprint of the Integration.
func (w *staleWorld) state(t *testing.T, in integrations.Integration, fingerprint string) string {
	t.Helper()
	var status, reason string
	if err := w.e.d.Pool.QueryRow(t.Context(), `SELECT status, coalesce(resolve_reason, '') FROM alerts
		WHERE integration_id = $1 AND fingerprint = $2`, in.ID, fingerprint).Scan(&status, &reason); err != nil {
		t.Fatalf("alert %s: %v", fingerprint, err)
	}
	if reason != "" {
		return status + "/" + reason
	}
	return status
}

// internal is the status of the Internal alert named alertname about the Integration, or "" when it never fired.
func (w *staleWorld) internal(t *testing.T, alertname string, in integrations.Integration) internalAlert {
	t.Helper()
	for _, a := range w.e.builtinAlerts(t) {
		if a.Labels["alertname"] == alertname && a.Labels["integration"] == in.PublicID {
			return a
		}
	}
	return internalAlert{}
}

const quietKey = `{}/{team="x"}:{alertname="Quiet"}`

// learn sends a first notification of key from each Integration at from and three repeats 5 minutes apart, with a
// Heartbeat signal of beat every minute; it returns at the last repeat.
func (w *staleWorld) learn(t *testing.T, ins []integrations.Integration, key string, from time.Duration,
	beat integrations.Integration, names ...string) time.Duration {
	t.Helper()
	for m := range 16 {
		w.at(from + time.Duration(m)*time.Minute)
		w.signal(t, beat)
		for _, in := range ins {
			switch {
			case m == 0:
				w.send(t, in, webhook(key, "first notification", 0, t0, names...))
			case m%5 == 0:
				w.send(t, in, webhook(key, "repeat interval elapsed", 0, t0, names...))
			}
		}
	}
	return from + 15*time.Minute
}

// TestIntegrationStaleScan covers C-07.AC-4, C-07.AC-2 and C-06.AC-3 on PostgreSQL: with a live Heartbeat and a
// learned 5-minute interval, an Alert whose Alertmanager group last arrived at T resolves as Stale just after T+15
// min, with the reason text of C-06.FR-10, a log line and the metric; with no signal from T to T+10 min, Heartbeat lost
// in between, it resolves just after T+25 min; an Integration without a Heartbeat, and one still waiting for its first
// signal, never resolve it as Stale, however long their groups stay silent.
func TestIntegrationStaleScan(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		w := newStaleWorld(t, e)
		live, off, waiting := w.create(t, "live", true), w.create(t, "off", false), w.create(t, "waiting", true)
		all := []integrations.Integration{live, off, waiting}
		w.at(0)
		w.signal(t, live)
		T := w.learn(t, all, quietKey, 0, live, "q")
		for _, in := range all {
			if got := w.state(t, in, "fp-q"); got != "firing" {
				t.Fatalf("%s: %s", in.Name, got)
			}
		}
		before := metrics.AlertsResolved.With(live.PublicID, ingest.ResolveStale).Get()
		for m := 1; m <= 15; m++ {
			w.at(T + time.Duration(m)*time.Minute)
			w.signal(t, live)
			w.leader(t)
		}
		if got := w.state(t, live, "fp-q"); got != "firing" {
			t.Fatalf("at T+15 min: %s", got)
		}
		w.at(T + 16*time.Minute)
		w.signal(t, live)
		w.leader(t)
		if got := w.state(t, live, "fp-q"); got != "resolved/stale" {
			t.Fatalf("at T+16 min: %s", got)
		}
		var text string
		if err := e.d.Pool.QueryRow(t.Context(), `SELECT resolve_reason_text FROM alerts WHERE integration_id = $1`,
			live.ID).Scan(&text); err != nil || text != ingest.GoneReasonText {
			t.Errorf("reason text %q, %v", text, err)
		}
		if got := metrics.AlertsResolved.With(live.PublicID, ingest.ResolveStale).Get() - before; got != 1 {
			t.Errorf("muster_alerts_resolved_total{reason=stale} grew by %d", got)
		}
		if n := strings.Count(e.processLog.String(), `"event":"alerts_stale","integration":"`+live.PublicID+
			`","count":1`); n != 1 {
			t.Errorf("%d alerts_stale lines: %s", n, e.processLog.String())
		}

		// Again with a loss: the Alert fires anew at T2, and no signal comes from T2 to T2+10 min.
		T2 := T + 17*time.Minute
		w.at(T2)
		w.signal(t, live)
		w.send(t, live, webhook(quietKey, "first notification", 0, t0.Add(T2), "q"))
		w.at(T2 + 5*time.Minute + time.Second)
		w.leader(t)
		in, err := e.ints.Get(t.Context(), live.PublicID)
		if err != nil || in.Heartbeat.State != integrations.HeartbeatLost || !in.Heartbeat.LostSince.Equal(t0.Add(T2)) {
			t.Fatalf("lost: %+v, %v", in.Heartbeat, err)
		}
		w.at(T2 + 10*time.Minute)
		w.signal(t, live)
		for m := 11; m <= 25; m++ {
			w.at(T2 + time.Duration(m)*time.Minute)
			w.signal(t, live)
			w.leader(t)
		}
		if got := w.state(t, live, "fp-q"); got != "firing" {
			t.Fatalf("at T2+25 min: %s", got)
		}
		w.at(T2 + 26*time.Minute)
		w.signal(t, live)
		w.leader(t)
		if got := w.state(t, live, "fp-q"); got != "resolved/stale" {
			t.Fatalf("at T2+26 min: %s", got)
		}

		// Without a Heartbeat, or before its first signal, nothing goes Stale; nothing is raised for the waiting one.
		w.at(T2 + 30*time.Hour)
		w.leader(t)
		for _, in := range []integrations.Integration{off, waiting} {
			if got := w.state(t, in, "fp-q"); got != "firing" {
				t.Errorf("%s after 30 h: %s", in.Name, got)
			}
			if a := w.internal(t, "MusterHeartbeatLost", in); a.Status != "" {
				t.Errorf("%s raised %+v", in.Name, a)
			}
		}
	})
}

// TestIntegrationStaleTruncation covers C-06.FR-6 and C-06.FR-7 with the Stale scan on PostgreSQL: the unlisted
// Alerts of a truncated groupKey stay alive while its Snapshots arrive; once none has arrived for stale_after counted
// like staleness, its truncation ends, MusterSnapshotTruncated resolves and its Alerts go Stale; an Alert Stale in one
// groupKey while another still lists it keeps firing; a second scan changes nothing.
func TestIntegrationStaleTruncation(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		w := newStaleWorld(t, e)
		in := w.create(t, "trunc", true)
		w.at(0)
		w.signal(t, in)
		cut, kept, gone := `{}/{team="cut"}:{alertname="Cut"}`, `{}/{team="kept"}:{alertname="Both"}`,
			`{}/{team="old"}:{alertname="Both"}`
		for m := range 16 {
			w.at(time.Duration(m) * time.Minute)
			w.signal(t, in)
			reason := "repeat interval elapsed"
			if m == 0 {
				reason = "first notification"
			}
			if m%5 == 0 {
				w.send(t, in, webhook(cut, reason, 0, t0, "a", "b", "c"))
				w.send(t, in, webhook(kept, reason, 0, t0, "x"))
				w.send(t, in, webhook(gone, reason, 0, t0, "x"))
			}
		}
		// From 20 min on, cut is truncated to a and old (a reload replaced its matchers) sends nothing more.
		for m := 16; m <= 40; m++ {
			w.at(time.Duration(m) * time.Minute)
			w.signal(t, in)
			if m%5 == 0 {
				w.send(t, in, webhook(cut, "repeat interval elapsed", 2, t0, "a"))
				w.send(t, in, webhook(kept, "repeat interval elapsed", 0, t0, "x"))
			}
			w.leader(t)
		}
		for _, fp := range []string{"fp-a", "fp-b", "fp-c", "fp-x"} {
			if got := w.state(t, in, fp); got != "firing" {
				t.Errorf("%s at 40 min: %s", fp, got)
			}
		}
		var oldState string
		if err := e.d.Pool.QueryRow(t.Context(), `SELECT p.state FROM alert_presences p
			JOIN alertmanager_groups g ON g.id = p.alertmanager_group_id WHERE g.group_key = $1`, gone).Scan(
			&oldState); err != nil || oldState != "stale" {
			t.Errorf("x in the old groupKey: %q, %v", oldState, err)
		}
		if a := w.internal(t, "MusterSnapshotTruncated", in); a.Status != "firing" {
			t.Fatalf("MusterSnapshotTruncated %+v", a)
		}

		// cut sends nothing more: after stale_after its truncation ends and its Alerts go Stale.
		for m := 41; m <= 56; m++ {
			w.at(time.Duration(m) * time.Minute)
			w.signal(t, in)
			if m%5 == 0 {
				w.send(t, in, webhook(kept, "repeat interval elapsed", 0, t0, "x"))
			}
			w.leader(t)
		}
		for fp, want := range map[string]string{"fp-a": "resolved/stale", "fp-b": "resolved/stale",
			"fp-c": "resolved/stale", "fp-x": "firing"} {
			if got := w.state(t, in, fp); got != want {
				t.Errorf("%s at 56 min: %s, want %s", fp, got, want)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM alertmanager_groups WHERE integration_id = $1 AND truncated`,
			in.ID); n != 0 {
			t.Errorf("%d groupKeys still truncated", n)
		}
		if a := w.internal(t, "MusterSnapshotTruncated", in); a.Status != "resolved" {
			t.Errorf("MusterSnapshotTruncated %+v", a)
		}

		// A second scan at the same time changes nothing.
		count := func() int64 {
			return e.count(t, `SELECT count(*) FROM alerts WHERE integration_id = $1 AND status = 'resolved'`, in.ID)
		}
		resolved, snapshots := count(), e.count(t, `SELECT count(*) FROM stored_snapshots`)
		lines := strings.Count(e.processLog.String(), `"event":"alerts_stale"`)
		w.leader(t)
		if count() != resolved || e.count(t, `SELECT count(*) FROM stored_snapshots`) != snapshots ||
			strings.Count(e.processLog.String(), `"event":"alerts_stale"`) != lines {
			t.Error("a second scan changed something")
		}
	})
}

// TestIntegrationHeartbeat covers C-07.FR-1 to FR-4, C-07.AC-1, C-07.AC-5 and C-06.FR-16 on PostgreSQL: the
// signals move the state and the liveness clock without touching version; the check makes a silent Integration lost
// and raises MusterHeartbeatLost with its Static labels, once however often it runs; the next signal resolves it;
// turning the Heartbeat off, renaming and deleting the Integration resolve or relabel it; after a downtime the Leader
// recorded, the timeout counts from its takeover.
func TestIntegrationHeartbeat(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		w := newStaleWorld(t, e)
		ctx := t.Context()
		in := w.create(t, "hb", true)
		clockOf := func() (ms int64, version int64) {
			if err := e.d.Pool.QueryRow(ctx, `SELECT liveness_clock_ms, version FROM integrations WHERE id = $1`,
				in.ID).Scan(&ms, &version); err != nil {
				t.Fatal(err)
			}
			return ms, version
		}
		w.at(0)
		w.signal(t, in)
		w.at(time.Minute)
		w.signal(t, in)
		w.at(8 * time.Minute)
		w.signal(t, in)
		if ms, version := clockOf(); ms != 60_000 || version != 1 {
			t.Errorf("clock %d, version %d", ms, version)
		}
		w.at(13 * time.Minute)
		w.leader(t)
		if got, _ := e.ints.Get(ctx, in.PublicID); got.Heartbeat.State != integrations.HeartbeatLive {
			t.Fatalf("at the timeout: %+v", got.Heartbeat)
		}
		w.at(13*time.Minute + time.Second)
		w.leader(t)
		w.leader(t)
		got, err := e.ints.Get(ctx, in.PublicID)
		if err != nil || got.Heartbeat.State != integrations.HeartbeatLost || got.Version != 1 ||
			len(got.Warnings) != 1 || got.Warnings[0].Kind != integrations.WarningHeartbeatLost ||
			!got.Warnings[0].Since.Equal(t0.Add(8*time.Minute)) {
			t.Fatalf("lost %+v, %v", got, err)
		}
		a := w.internal(t, "MusterHeartbeatLost", in)
		want := map[string]string{"alertname": "MusterHeartbeatLost", "severity": "critical", "integration": in.PublicID,
			"integration_name": "hb", "env": "prod"}
		if a.Status != "firing" || len(a.Labels) != len(want) {
			t.Fatalf("MusterHeartbeatLost %+v", a)
		}
		for k, v := range want {
			if a.Labels[k] != v {
				t.Errorf("label %s = %q", k, a.Labels[k])
			}
		}
		if n := e.count(t, `SELECT count(*) FROM stored_snapshots s JOIN integrations i ON i.id = s.integration_id
			AND i.builtin`); n != 1 {
			t.Errorf("%d raises", n)
		}
		w.at(30 * time.Minute)
		w.signal(t, in)
		w.drain(t)
		if a := w.internal(t, "MusterHeartbeatLost", in); a.Status != "resolved" {
			t.Errorf("after the signal %+v", a)
		}
		if ms, _ := clockOf(); ms != 60_000 {
			t.Errorf("the loss advanced the clock to %d", ms)
		}

		// Turned off while lost: not configured, and the Internal alert resolves.
		w.at(36 * time.Minute)
		w.leader(t)
		on := integrations.Input{Name: "hb", ConnectionMode: integrations.ConnectionWebhookOnly,
			StaticLabels: map[string]string{"env": "prod"}, DuplicateWindowSeconds: 45,
			Heartbeat: integrations.HeartbeatInput{Enabled: true}}
		off := on
		off.Heartbeat.Enabled = false
		if got, err := e.ints.Update(ctx, by, in.PublicID, nil, off); err != nil ||
			got.Heartbeat.State != integrations.HeartbeatNotConfigured || got.Heartbeat.LostSince != nil {
			t.Fatalf("off %+v, %v", got, err)
		}
		w.drain(t)
		if a := w.internal(t, "MusterHeartbeatLost", in); a.Status != "resolved" {
			t.Errorf("after turning it off %+v", a)
		}
		w.signal(t, in)
		if got, _ := e.ints.Get(ctx, in.PublicID); got.Heartbeat.State != integrations.HeartbeatNotConfigured {
			t.Errorf("a signal while off: %+v", got.Heartbeat)
		}

		// On again it waits; lost again, a rename relabels it and the deletion resolves it.
		if got, err := e.ints.Update(ctx, by, in.PublicID, nil, on); err != nil ||
			got.Heartbeat.State != integrations.HeartbeatWaiting {
			t.Fatalf("on %+v, %v", got, err)
		}
		w.signal(t, in)
		w.at(42 * time.Minute)
		w.leader(t)
		on.Name = "hb-renamed"
		if _, err := e.ints.Update(ctx, by, in.PublicID, nil, on); err != nil {
			t.Fatal(err)
		}
		w.drain(t)
		if a := w.internal(t, "MusterHeartbeatLost", in); a.Status != "firing" ||
			a.Labels["integration_name"] != "hb-renamed" || a.Labels["env"] != "prod" {
			t.Errorf("after the rename %+v", a)
		}
		if err := e.ints.Delete(ctx, by, in.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		w.drain(t) // the marker of the deletion, which writes the resolve
		w.drain(t) // the resolve
		if a := w.internal(t, "MusterHeartbeatLost", in); a.Status != "resolved" {
			t.Errorf("after the deletion %+v", a)
		}

		// After a downtime the Leader recorded, the timeout counts from its takeover.
		other := w.create(t, "after-downtime", true)
		w.at(50 * time.Minute)
		w.signal(t, other)
		takeover := t0.Add(2 * time.Hour)
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO runtime_state (leader_replica_id, leader_since, alive_at,
			updated_at) VALUES ('b', $1, $1, $1)`, takeover); err != nil {
			t.Fatal(err)
		}
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO downtime_periods (started_at, ended_at, recorded_at)
			VALUES ($1, $2, $2)`, t0.Add(51*time.Minute), takeover); err != nil {
			t.Fatal(err)
		}
		w.at(2*time.Hour + 5*time.Minute)
		w.leader(t)
		if got, _ := e.ints.Get(ctx, other.PublicID); got.Heartbeat.State != integrations.HeartbeatLive {
			t.Errorf("lost within the timeout after the takeover: %+v", got.Heartbeat)
		}
		w.at(2*time.Hour + 5*time.Minute + time.Second)
		w.leader(t)
		if got, _ := e.ints.Get(ctx, other.PublicID); got.Heartbeat.State != integrations.HeartbeatLost ||
			!got.Heartbeat.LostSince.Equal(t0.Add(50*time.Minute)) {
			t.Errorf("after the timeout from the takeover: %+v", got.Heartbeat)
		}
		if !strings.Contains(w.log.String(), `"event":"heartbeat_lost","integration":"`+other.PublicID+`"`) ||
			!strings.Contains(w.log.String(), `"event":"heartbeat_live","integration":"`+in.PublicID+`","first":true`) {
			t.Errorf("log %s", w.log.String())
		}
	})
}

// TestIntegrationStalePending: the Stale scan leaves an Integration whose Stored Snapshots still wait for processing
// to the next run, so that it never resolves an Alert that a waiting Snapshot lists.
func TestIntegrationStalePending(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		w := newStaleWorld(t, e)
		in := w.create(t, "pending", true)
		w.at(0)
		T := w.learn(t, []integrations.Integration{in}, quietKey, 0, in, "q")
		for m := 1; m <= 15; m++ {
			w.at(T + time.Duration(m)*time.Minute)
			w.signal(t, in)
		}
		// Received at T+15 min, listing q, and not processed yet when the scan runs at T+16 min.
		w.at(T + 15*time.Minute)
		if _, err := e.snapshots.Store(t.Context(), ingest.Received{IntegrationID: in.ID,
			Body: webhook(quietKey, "repeat interval elapsed", 0, t0, "q")}); err != nil {
			t.Fatal(err)
		}
		w.at(T + 16*time.Minute)
		w.signal(t, in)
		if err := w.scanner.StaleScan(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := w.state(t, in, "fp-q"); got != "firing" {
			t.Fatalf("with a pending snapshot: %s", got)
		}
		w.drain(t)
		w.leader(t)
		if got := w.state(t, in, "fp-q"); got != "firing" {
			t.Fatalf("after its processing: %s", got)
		}
		// Processed after the signal at T+16 min, the Snapshot records the clock of then, slightly ahead of its receipt.
		for m := 17; m <= 32; m++ {
			w.at(T + time.Duration(m)*time.Minute)
			w.signal(t, in)
		}
		w.leader(t)
		if got := w.state(t, in, "fp-q"); got != "resolved/stale" {
			t.Errorf("16 min after the last listing: %s", got)
		}
	})
}
