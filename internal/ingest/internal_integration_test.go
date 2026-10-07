// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// changeSink records every Alert change that processing hands over.
type changeSink struct {
	mu      sync.Mutex
	changes []ingest.AlertChange
}

func (s *changeSink) AlertChanges(_ context.Context, _ dbgen.DBTX, changes []ingest.AlertChange) (ingest.Routed,
	error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes = append(s.changes, changes...)
	return ingest.Routed{}, nil
}

func (s *changeSink) take() []ingest.AlertChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.changes
	s.changes = nil
	return out
}

// drain processes every pending Stored Snapshot, the synthetic ones that processing writes meanwhile included.
func drain(t *testing.T, p *ingest.Processor) {
	t.Helper()
	for range 10 {
		n, err := p.Drain(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("processing does not settle")
}

// webhook is a Snapshot of the groupKey key listing the Alerts of names, each firing since startsAt, with
// truncatedAlerts and a notification_reason.
func webhook(key, reason string, truncated int, startsAt time.Time, names ...string) []byte {
	alerts := make([]string, len(names))
	for i, n := range names {
		alerts[i] = fmt.Sprintf(`{"status":"firing","labels":{"alertname":"PodDown","pod":%q},"startsAt":%q,`+
			`"endsAt":"0001-01-01T00:00:00Z","fingerprint":"fp-%s"}`, n, startsAt.Format(time.RFC3339), n)
	}
	return fmt.Appendf(nil, `{"version":"4","groupKey":%q,"status":"firing","truncatedAlerts":%d,`+
		`"notification_reason":%q,"alerts":[%s]}`, key, truncated, reason, strings.Join(alerts, ","))
}

type internalAlert struct {
	Fingerprint string
	Status      string
	Labels      map[string]string
	Reason      *string
	ReasonText  *string
}

// builtinAlerts reads the Internal alerts: the Alerts of the built-in Integration.
func (e *env) builtinAlerts(t *testing.T) []internalAlert {
	t.Helper()
	rows, err := e.d.Pool.Query(t.Context(), `SELECT a.fingerprint, a.status, a.labels, a.resolve_reason,
		a.resolve_reason_text FROM alerts a JOIN integrations i ON i.id = a.integration_id AND i.builtin ORDER BY a.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []internalAlert
	for rows.Next() {
		var a internalAlert
		var labels []byte
		if err := rows.Scan(&a.Fingerprint, &a.Status, &labels, &a.Reason, &a.ReasonText); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(labels, &a.Labels); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// TestIntegrationBuiltinIntegration is C-06.FR-14: the built-in Integration exists after the ensure step, a second
// start creates nothing, it is listed marked builtin, and changing, deleting it or giving it a token is refused.
func TestIntegrationBuiltinIntegration(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		if err := integrations.EnsureBuiltin(ctx, integrations.NewStore(e.d.Pool), e.orgID, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM integrations WHERE builtin`); n != 1 {
			t.Fatalf("built-in integrations after a second start = %d", n)
		}
		page, err := e.ints.List(ctx, integrations.ListFilter{Limit: 10})
		if err != nil || len(page.Integrations) != 1 {
			t.Fatalf("list = %+v, %v", page, err)
		}
		b := page.Integrations[0]
		if !b.Builtin || b.Name != "Muster" || b.Heartbeat.State != integrations.HeartbeatNotConfigured ||
			len(b.StaticLabels) != 0 || b.ConnectionMode != integrations.ConnectionWebhookOnly ||
			!b.CreatedAt.Equal(t0) {
			t.Errorf("built-in = %+v", b)
		}
		if _, err := e.ints.Update(ctx, by, b.PublicID, nil, input("renamed")); !errors.Is(err, integrations.ErrBuiltinImmutable) {
			t.Errorf("update = %v", err)
		}
		if err := e.ints.Delete(ctx, by, b.PublicID, nil); !errors.Is(err, integrations.ErrBuiltinImmutable) {
			t.Errorf("delete = %v", err)
		}
		if _, err := e.ints.CreateToken(ctx, by, b.PublicID, "x"); !errors.Is(err, integrations.ErrBuiltinImmutable) {
			t.Errorf("token = %v", err)
		}
		if tokens, err := e.ints.ListTokens(ctx, b.PublicID); err != nil || len(tokens) != 0 {
			t.Errorf("tokens = %v, %v", tokens, err)
		}
		if _, err := e.ints.Create(ctx, by, input("Muster")); !errors.Is(err, integrations.ErrNameTaken) {
			t.Errorf("an integration named Muster = %v", err)
		}
	})
}

// TestIntegrationSnapshotTruncated is C-06.AC-5 and C-06.AC-11: a truncated Snapshot raises MusterSnapshotTruncated
// as a firing Alert of the built-in Integration, a second truncated Snapshot raises nothing more, renaming the
// Integration updates integration_name in place with the same fingerprint, a synthetic Snapshot never makes an
// Internal alert Gone, and the next untruncated Snapshot of the groupKey resolves it.
func TestIntegrationSnapshotTruncated(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		lab, err := e.ints.Create(ctx, by, input("lab"))
		if err != nil {
			t.Fatal(err)
		}
		other, err := e.ints.Create(ctx, by, input("other"))
		if err != nil {
			t.Fatal(err)
		}
		sink := &changeSink{}
		p := e.processor("replica-a", sink)
		key := `{}:{alertname="PodDown"}`
		e.storeAt(t, lab.ID, t0, webhook(key, "first notification", 0, t0, "p0", "p1", "p2"))
		e.storeAt(t, lab.ID, t0.Add(10*time.Second), webhook(key, "repeat interval elapsed", 1, t0, "p0", "p1"))
		drain(t, p)
		alerts := e.builtinAlerts(t)
		want := map[string]string{"alertname": "MusterSnapshotTruncated", "severity": "warning",
			"integration": lab.PublicID, "integration_name": "lab"}
		if len(alerts) != 1 || alerts[0].Status != "firing" || !mapsEqual(alerts[0].Labels, want) {
			t.Fatalf("internal alerts = %+v", alerts)
		}
		fingerprint := alerts[0].Fingerprint
		internal := `SELECT count(*) FROM stored_snapshots WHERE source = 'internal' AND group_key = $1`
		if n := e.count(t, internal, internalalerts.GroupKey("MusterSnapshotTruncated")); n != 1 {
			t.Errorf("internal snapshots after the raise = %d", n)
		}
		got, err := e.ints.Get(ctx, lab.PublicID)
		if err != nil || len(got.Warnings) != 2 || got.Warnings[0].Kind != integrations.WarningHeartbeatNotConfigured ||
			got.Warnings[1].Kind != integrations.WarningSnapshotTruncated || got.Warnings[1].TruncatedGroupCount != 1 {
			t.Errorf("warnings = %+v, %v", got.Warnings, err)
		}

		e.storeAt(t, lab.ID, t0.Add(20*time.Second), webhook(key, "repeat interval elapsed", 1, t0, "p0", "p1"))
		drain(t, p)
		if n := e.count(t, internal, internalalerts.GroupKey("MusterSnapshotTruncated")); n != 1 {
			t.Errorf("internal snapshots after a second truncated snapshot = %d", n)
		}

		sink.take()
		e.clock.Set(t0.Add(time.Minute))
		renamed := input("lab-eu")
		if _, err := e.ints.Update(ctx, by, lab.PublicID, nil, renamed); err != nil {
			t.Fatal(err)
		}
		drain(t, p)
		alerts = e.builtinAlerts(t)
		want["integration_name"] = "lab-eu"
		if len(alerts) != 1 || alerts[0].Fingerprint != fingerprint || alerts[0].Status != "firing" ||
			!mapsEqual(alerts[0].Labels, want) {
			t.Errorf("internal alerts after the rename = %+v", alerts)
		}
		if changes := sink.take(); len(changes) != 1 || changes[0].Kind != ingest.ChangeAnnotations {
			t.Errorf("changes of the rename = %+v", changes)
		}

		// Another Integration's raise and resolve are synthetic Snapshots of the same groupKey that do not list it,
		// far apart: the Internal alert of lab never goes Gone.
		otherKey := `{}:{alertname="Other"}`
		e.storeAt(t, other.ID, t0.Add(2*time.Minute), webhook(otherKey, "first notification", 1, t0, "o0"))
		drain(t, p)
		e.storeAt(t, other.ID, t0.Add(time.Hour), webhook(otherKey, "repeat interval elapsed", 0, t0, "o0"))
		drain(t, p)
		alerts = e.builtinAlerts(t)
		if len(alerts) != 2 || alerts[0].Status != "firing" || alerts[1].Status != "resolved" {
			t.Errorf("internal alerts after another integration's raise and resolve = %+v", alerts)
		}

		e.storeAt(t, lab.ID, t0.Add(2*time.Hour), webhook(key, "repeat interval elapsed", 0, t0, "p0", "p1", "p2"))
		drain(t, p)
		alerts = e.builtinAlerts(t)
		if alerts[0].Status != "resolved" || *alerts[0].Reason != ingest.ResolveResolved {
			t.Errorf("internal alert after an untruncated snapshot = %+v", alerts[0])
		}
		got, _ = e.ints.Get(ctx, lab.PublicID)
		if slices.ContainsFunc(got.Warnings, func(w integrations.Warning) bool {
			return w.Kind == integrations.WarningSnapshotTruncated
		}) {
			t.Errorf("warnings after the truncation ended = %+v", got.Warnings)
		}
		lines := strings.Count(e.processLog.String(), `"event":"internal_alert_raised"`)
		resolved := strings.Count(e.processLog.String(), `"event":"internal_alert_resolved"`)
		if lines != 2 || resolved != 2 || !strings.Contains(e.processLog.String(),
			`"event":"internal_alert_raised","alertname":"MusterSnapshotTruncated","fingerprint":"`+fingerprint+
				`","entity":"`+lab.PublicID+`"`) {
			t.Errorf("raised %d, resolved %d:\n%s", lines, resolved, e.processLog.String())
		}
	})
}

// TestIntegrationDeletion is C-06.FR-16, C-06.AC-7 and C-06.AC-10: the Snapshots an Integration accepted before its
// deletion are processed first; then its two open Alerts resolve with the reason integration_deleted and the text
// "Integration {name} deleted", muster_alerts_resolved_total{reason="integration_deleted"} grows by two, its
// MusterSnapshotTruncated resolves, its Alerts stay readable by its id, and a replayed marker changes nothing.
func TestIntegrationDeletion(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("gone-soon"))
		if err != nil {
			t.Fatal(err)
		}
		sink := &changeSink{}
		p := e.processor("replica-a", sink)
		deleted0 := metrics.AlertsResolved.With(in.PublicID, ingest.ResolveIntegrationDeleted).Get()
		key := `{}:{alertname="PodDown"}`
		e.storeAt(t, in.ID, t0, webhook(key, "first notification", 3, t0, "p0"))
		drain(t, p)
		if alerts := e.builtinAlerts(t); len(alerts) != 1 || alerts[0].Status != "firing" {
			t.Fatalf("internal alerts before the deletion = %+v", alerts)
		}
		sink.take()
		// Accepted before the deletion and still pending when it commits.
		e.storeAt(t, in.ID, t0.Add(10*time.Second), webhook(key, "new alerts added", 3, t0, "p0", "p1"))
		e.clock.Set(t0.Add(11 * time.Second))
		if err := e.ints.Delete(ctx, by, in.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		drain(t, p)
		var order []string
		for _, c := range sink.take() {
			order = append(order, string(c.Kind)+":"+c.Fingerprint)
		}
		// The pending Snapshot fired p1, the marker resolved both Alerts, and the built-in Integration's resolve
		// resolved the Internal alert.
		if len(order) != 4 || order[0] != "fired:fp-p1" || order[1] != "resolved:fp-p0" || order[2] != "resolved:fp-p1" ||
			!strings.HasPrefix(order[3], "resolved:") {
			t.Errorf("changes = %v", order)
		}
		if got := metrics.AlertsResolved.With(in.PublicID, ingest.ResolveIntegrationDeleted).Get() - deleted0; got != 2 {
			t.Errorf("muster_alerts_resolved_total{reason=integration_deleted} grew by %d", got)
		}
		view := ingest.NewAlertsView(e.orgID, ingest.NewProcessStore(e.d.Pool), e.clock)
		page, err := view.List(ctx, ingest.AlertFilter{Integration: in.PublicID, Sort: ingest.SortLastSeenDesc,
			Limit: 10})
		if err != nil || len(page.Alerts) != 2 {
			t.Fatalf("alerts of the deleted integration = %+v, %v", page, err)
		}
		for _, a := range page.Alerts {
			if a.State != "resolved" || *a.Reason != ingest.ResolveIntegrationDeleted ||
				*a.ReasonText != "Integration gone-soon deleted" {
				t.Errorf("alert = %+v", a)
			}
		}
		if alerts := e.builtinAlerts(t); len(alerts) != 1 || alerts[0].Status != "resolved" ||
			alerts[0].Labels["integration"] != in.PublicID {
			t.Errorf("internal alerts = %+v", alerts)
		}
		if _, err := view.Routes(ctx, in.PublicID); !errors.Is(err, integrations.ErrNotFound) {
			t.Errorf("routes of a deleted integration = %v", err)
		}
		// A request that authenticated before the deletion and was stored after it fires nothing.
		late := e.storeAt(t, in.ID, t0.Add(12*time.Second), webhook(`{}:{alertname="Late"}`, "first notification", 0,
			t0, "late"))
		drain(t, p)
		if n := e.count(t, `SELECT count(*) FROM stored_snapshots WHERE id = $1 AND state = 'failed'
			AND processing_error = 'the integration was deleted before the snapshot was received'`, late); n != 1 {
			t.Error("a snapshot received after the deletion was not refused")
		}
		if n := e.count(t, `SELECT count(*) FROM alerts WHERE integration_id = $1 AND status = 'firing'`, in.ID); n != 0 {
			t.Errorf("%d alerts fire after the deletion", n)
		}
		sink.take()
		// Replaying the hour, the marker included, changes no Alert and writes no synthetic Snapshot.
		internal := e.builtinAlerts(t)
		before := e.count(t, `SELECT count(*) FROM stored_snapshots`)
		r := ingest.Replayer{OrgID: e.orgID, Store: ingest.NewReplayStore(e.d.Pool),
			Audit: audit.NewWriter(logging.New(&e.processLog, logging.LevelInfo), e.clock),
			Log:   logging.New(&e.processLog, logging.LevelInfo), Business: e.clock}
		if _, err := r.Replay(ctx, ingest.Replay{Since: time.Hour, SinceText: "1h", Actor: "ops-alice"}); err != nil {
			t.Fatal(err)
		}
		drain(t, p)
		if changes := sink.take(); len(changes) != 0 {
			t.Errorf("changes of the replay = %+v", changes)
		}
		if after := e.builtinAlerts(t); !slices.EqualFunc(after, internal, func(a, b internalAlert) bool {
			return a.Fingerprint == b.Fingerprint && a.Status == b.Status && mapsEqual(a.Labels, b.Labels)
		}) {
			t.Errorf("internal alerts after the replay = %+v", after)
		}
		if after := e.count(t, `SELECT count(*) FROM stored_snapshots`); after != before {
			t.Errorf("stored snapshots %d → %d", before, after)
		}
	})
}

// alertState is what an Alert shows, apart from updated_at.
type alertState struct {
	Fingerprint, Status, Labels, Annotations, ResolvedAt, Reason string
	Episode                                                      int64
	StartsAt, LastSeenAt                                         time.Time
}

func (e *env) alertStates(t *testing.T, integrationID int64) []alertState {
	t.Helper()
	rows, err := e.d.Pool.Query(t.Context(), `SELECT fingerprint, status, labels::text, annotations::text, episode,
		starts_at, last_seen_at, coalesce(resolved_at::text, ''), coalesce(resolve_reason, '')
		FROM alerts WHERE integration_id = $1 ORDER BY id`,
		integrationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []alertState
	for rows.Next() {
		var a alertState
		if err := rows.Scan(&a.Fingerprint, &a.Status, &a.Labels, &a.Annotations, &a.Episode, &a.StartsAt,
			&a.LastSeenAt, &a.ResolvedAt, &a.Reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// TestIntegrationReplay is C-06.FR-17, C-06.AC-6 and C-06.FR-20: replaying the last hour sets its Stored Snapshots
// back to pending and records ingest.replayed; a Snapshot that failed is processed again with the current code, and
// replaying the same hour twice changes no Alert and records no Alert change the second time. A replayed Snapshot is
// not counted on its Integration again.
func TestIntegrationReplay(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("rp"))
		if err != nil {
			t.Fatal(err)
		}
		sink := &changeSink{}
		p := e.processor("replica-a", sink)
		key := `{}/{team="db"}:{alertname="PodDown"}`
		start := t0.Add(-3 * time.Hour)
		e.storeAt(t, in.ID, start, webhook(key, "first notification", 0, start, "old"))
		e.storeAt(t, in.ID, t0, webhook(key, "first notification", 0, t0, "p0", "p1"))
		e.storeAt(t, in.ID, t0.Add(5*time.Minute), webhook(key, "repeat interval elapsed", 0, t0, "p0", "p1"))
		e.storeAt(t, in.ID, t0.Add(10*time.Minute), []byte(`{"version":"4","groupKey":"`+
			strings.ReplaceAll(key, `"`, `\"`)+`","status":"firing","notification_reason":"some alerts resolved",`+
			`"alerts":[{"status":"resolved","labels":{"alertname":"PodDown","pod":"p1"},"startsAt":"`+
			t0.Format(time.RFC3339)+`","endsAt":"`+t0.Add(9*time.Minute).Format(time.RFC3339)+
			`","fingerprint":"fp-p1"},{"status":"firing","labels":{"alertname":"PodDown","pod":"p0"},"startsAt":"`+
			t0.Format(time.RFC3339)+`","fingerprint":"fp-p0"}]}`))
		drain(t, p)
		// A Snapshot that an earlier version could not process: it failed and was never applied.
		failed := e.storeAt(t, in.ID, t0.Add(15*time.Minute), webhook(`{}:{alertname="Late"}`, "first notification",
			0, t0, "x"))
		if _, err := e.d.Pool.Exec(ctx, `UPDATE stored_snapshots SET state = 'failed', processed_at = $2,
			processing_error = 'a bug of an earlier version' WHERE id = $1`, failed, t0.Add(15*time.Minute)); err != nil {
			t.Fatal(err)
		}
		sink.take()
		count0 := e.count(t, `SELECT snapshot_count FROM integrations WHERE id = $1`, in.ID)
		states0 := e.alertStates(t, in.ID)

		e.clock.Set(t0.Add(30 * time.Minute))
		log := logging.New(&e.processLog, logging.LevelInfo)
		r := ingest.Replayer{OrgID: e.orgID, Store: ingest.NewReplayStore(e.d.Pool), Audit: audit.NewWriter(log, e.clock),
			Log: log, Business: e.clock}
		replay := ingest.Replay{Since: time.Hour, SinceText: "1h", Integration: "rp", Actor: "ops-alice"}
		out, err := r.Replay(ctx, replay)
		if err != nil || out.Count != 4 || out.Integration == nil || out.Integration.PublicID != in.PublicID {
			t.Fatalf("replay = %+v, %v", out, err)
		}
		if n := e.count(t, `SELECT count(*) FROM stored_snapshots WHERE integration_id = $1 AND state = 'pending'
			AND processed_at IS NULL AND processing_error IS NULL AND replayed_at IS NOT NULL`, in.ID); n != 4 {
			t.Errorf("pending after the replay = %d", n)
		}
		drain(t, p)
		changes := sink.take()
		if len(changes) != 1 || changes[0].Kind != ingest.ChangeFired || changes[0].Fingerprint != "fp-x" {
			t.Errorf("changes of the first replay = %+v", changes)
		}
		if n := e.count(t, `SELECT count(*) FROM stored_snapshots WHERE id = $1 AND state = 'processed'`,
			failed); n != 1 {
			t.Error("the failed snapshot was not processed again")
		}
		states1 := e.alertStates(t, in.ID)
		if !slices.Equal(states1[:len(states0)], states0) || len(states1) != len(states0)+1 {
			t.Errorf("alerts after the first replay:\n%+v\nwant\n%+v", states1, states0)
		}

		out, err = r.Replay(ctx, replay)
		if err != nil || out.Count != 4 {
			t.Fatalf("second replay = %+v, %v", out, err)
		}
		drain(t, p)
		if changes := sink.take(); len(changes) != 0 {
			t.Errorf("changes of the second replay = %+v", changes)
		}
		if states2 := e.alertStates(t, in.ID); !slices.Equal(states2, states1) {
			t.Errorf("alerts after the second replay:\n%+v\nwant\n%+v", states2, states1)
		}
		if n := e.count(t, `SELECT snapshot_count FROM integrations WHERE id = $1`, in.ID); n != count0 {
			t.Errorf("snapshot_count %d after two replays, want %d", n, count0)
		}
		if n := e.count(t, `SELECT count(*) FROM audit_log WHERE action = 'ingest.replayed' AND actor_kind = 'cli'
			AND actor_name = 'ops-alice' AND transport = 'cli' AND resource_public_id = $1
			AND details = '{"count": 4, "integration": "`+in.PublicID+`", "since": "1h"}'::jsonb`,
			in.PublicID); n != 2 {
			t.Errorf("ingest.replayed entries = %d", n)
		}
		if !strings.Contains(e.processLog.String(), `"event":"ingest_replayed","actor":"ops-alice","since":"1h",`+
			`"integration":"`+in.PublicID+`","count":4`) {
			t.Errorf("log:\n%s", e.processLog.String())
		}
		if _, err := r.Replay(ctx, ingest.Replay{Since: time.Hour, SinceText: "1h", Integration: "nope",
			Actor: "ops-alice"}); err == nil || !strings.Contains(err.Error(), "nope") {
			t.Errorf("an unknown integration = %v", err)
		}
	})
}

// TestIntegrationRoutesAndRetention is C-06.FR-18, C-06.AC-3 and C-06.FR-19: a route whose Snapshots repeat every 5
// minutes learns 5 minutes, one that repeats every 2 hours warns with a recommended snippet, the Integration carries
// the warning, and the Leader's retention deletes the Alerts resolved longer than retention.alert_details ago with
// their presences.
func TestIntegrationRoutesAndRetention(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		in, err := e.ints.Create(ctx, by, input("rt"))
		if err != nil {
			t.Fatal(err)
		}
		p := e.processor("replica-a", nil)
		web, info := `{}/{team="web"}:{alertname="Slow"}`, `{}/{kind="info"}:{alertname="CertExpiry"}`
		start := t0.Add(-3 * time.Hour)
		e.storeAt(t, in.ID, start, webhook(web, "first notification", 0, start, "w"))
		e.storeAt(t, in.ID, start, webhook(info, "first notification", 0, start, "c"))
		for k := 1; k <= 3; k++ {
			e.storeAt(t, in.ID, start.Add(time.Duration(k)*5*time.Minute), webhook(web, "repeat interval elapsed", 0,
				start, "w"))
		}
		e.storeAt(t, in.ID, start.Add(2*time.Hour), webhook(info, "repeat interval elapsed", 0, start, "c"))
		drain(t, p)
		view := ingest.NewAlertsView(e.orgID, ingest.NewProcessStore(e.d.Pool), e.clock)
		routes, err := view.Routes(ctx, in.PublicID)
		if err != nil || len(routes) != 2 {
			t.Fatalf("routes = %+v, %v", routes, err)
		}
		w, c := routes[1], routes[0]
		if w.RoutePath != `{}/{team="web"}` || *w.LearnedRepeatInterval != 5*time.Minute ||
			w.ResolveByAbsenceAfter != 15*time.Minute || w.LongIntervalWarning || w.RecommendedSnippet != "" {
			t.Errorf("web route = %+v", w)
		}
		if c.RoutePath != `{}/{kind="info"}` || *c.LearnedRepeatInterval != 2*time.Hour || !c.LongIntervalWarning ||
			!strings.HasSuffix(c.RecommendedSnippet, "route:\n  routes:\n    - matchers:\n        - \"kind=\\\"info\\\"\"\n"+
				"      repeat_interval: 10m\n") {
			t.Errorf("info route = %+v\n%s", c, c.RecommendedSnippet)
		}
		got, err := e.ints.Get(ctx, in.PublicID)
		if err != nil || len(got.Warnings) != 2 || got.Warnings[0].Kind != integrations.WarningHeartbeatNotConfigured ||
			got.Warnings[1] != (integrations.Warning{Kind: integrations.WarningLongRepeatInterval,
				RoutePath: `{}/{kind="info"}`, RepeatIntervalSeconds: 7200}) {
			t.Errorf("warnings = %+v, %v", got.Warnings, err)
		}

		// Retention: one Alert resolved 91 days before now goes with its presence; a recent one and a firing one stay.
		if _, err := e.d.Pool.Exec(ctx, `UPDATE alerts SET status = 'resolved', resolved_at = $2,
			resolve_reason = 'resolved' WHERE integration_id = $1 AND fingerprint = 'fp-w'`, in.ID, start); err != nil {
			t.Fatal(err)
		}
		now := start.Add(91 * 24 * time.Hour)
		n, err := ingest.PruneAlerts(ctx, ingest.NewProcessStore(e.d.Pool), e.orgID, now)
		if err != nil || n != 1 {
			t.Fatalf("pruned %d, %v", n, err)
		}
		if n := e.count(t, `SELECT count(*) FROM alerts WHERE integration_id = $1`, in.ID); n != 1 {
			t.Errorf("alerts left = %d", n)
		}
		if n := e.count(t, `SELECT count(*) FROM alert_presences WHERE integration_id = $1`, in.ID); n != 1 {
			t.Errorf("presences left = %d", n)
		}
		if n, err := ingest.PruneAlerts(ctx, ingest.NewProcessStore(e.d.Pool), e.orgID, now); err != nil || n != 0 {
			t.Errorf("a second run pruned %d, %v", n, err)
		}
	})
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
