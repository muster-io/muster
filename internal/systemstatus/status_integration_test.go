// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package systemstatus_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/systemstatus"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// fixtures fill every section of the System status but the truncated Snapshots, which the end-to-end test covers, in
// the Organization $1 at $2: a Leader with a fresh alive mark in a recovery window after a downtime; a Broken
// Mattermost Destination with a pending Storm summary and a template error; a Heartbeat lost Integration; a Route with
// a template error and an active Storm; a deleted Route whose Storm has not ended; and a failed outgoing heartbeat.
// Each also has a deleted or healthy twin that the System status leaves out.
var fixtures = []string{
	`INSERT INTO runtime_state (leader_replica_id, leader_since, alive_at, recovery_until, updated_at)
	 SELECT 'replica-a', $2::timestamptz - interval '1 hour', $2::timestamptz - interval '30 seconds',
	        $2::timestamptz + interval '1 hour', $2
	 FROM organizations
	 WHERE id = $1`,
	`INSERT INTO downtime_periods (started_at, ended_at, recorded_at)
	 SELECT v.started_at, v.ended_at, v.ended_at
	 FROM organizations,
	      (VALUES ($2::timestamptz - interval '3 hours', $2::timestamptz - interval '2 hours'),
	              ($2::timestamptz - interval '2 hours', $2::timestamptz - interval '1 hour')) AS v (started_at, ended_at)
	 WHERE id = $1`,
	`INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url, bot_token_ciphertext,
	                          bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at,
	                          updated_at)
	 VALUES ($1, 'CN000000000001', 'mattermost', 'mm', 'http://mm.example.org', '\x00', 'k1', $2, 1, 1, $2, $2)`,
	`INSERT INTO destinations (org_id, public_id, type, name, connection_id, mattermost_team_id, mattermost_channel_id,
	                           mentions, limiter_limit, limiter_per_seconds, health, broken_since, broken_cause,
	                           broken_reason, next_probe_at, template_error_since, template_error, deleted_at,
	                           created_at, updated_at)
	 SELECT $1, v.public_id, 'mattermost', v.name, c.id, 'team', 'channel', '{}', 1, 1, v.health, v.since, v.cause,
	        v.reason, v.since, v.since, v.error, v.deleted_at, $2, $2
	 FROM connections c,
	      (VALUES ('DS000000000001', 'alerts', 'broken', $2::timestamptz - interval '5 minutes', 'fatal',
	               'answered 403', 'webhook template failed', NULL::timestamptz),
	              ('DS000000000002', 'gone', 'broken', $2::timestamptz - interval '5 minutes', 'fatal',
	               'answered 403', 'webhook template failed', $2::timestamptz),
	              ('DS000000000003', 'fine', 'healthy', NULL, NULL, NULL, NULL, NULL))
	        AS v (public_id, name, health, since, cause, reason, error, deleted_at)
	 WHERE c.org_id = $1`,
	`INSERT INTO integrations (org_id, public_id, name, connection_mode, static_labels, duplicate_window_seconds,
	                           heartbeat_enabled, heartbeat_timeout_seconds, heartbeat_state, heartbeat_lost_since,
	                           deleted_at, created_at, updated_at)
	 VALUES ($1, 'NT000000000001', 'hb', 'webhook_only', '{}', 45, true, 300, 'lost',
	         $2::timestamptz - interval '4 minutes', NULL, $2, $2),
	        ($1, 'NT000000000002', 'hb-gone', 'webhook_only', '{}', 45, true, 300, 'lost',
	         $2::timestamptz - interval '4 minutes', $2, $2, $2),
	        ($1, 'NT000000000003', 'hb-live', 'webhook_only', '{}', 45, true, 300, 'live', NULL, NULL, $2, $2)`,
	`INSERT INTO routes (org_id, public_id, name, position, urgent, group_key, reopen_window_seconds,
	                     grace_period_seconds, urgent_rise_removes_ack, snooze_durations_seconds,
	                     thread_batching_window_seconds, storm_threshold, language, ack_timeout_enabled,
	                     ack_timeout_first_interval_seconds, reminders_enabled, reminders_first_interval_seconds,
	                     reminders_cap_seconds, auto_unacknowledge, template_error_since, template_error,
	                     template_error_template, deleted_at, created_at, updated_at)
	 VALUES ($1, 'RT000000000001', 'storm', 1, false, '{}', 0, 0, false, '{}', 0, 2, 'en', false, 60, false, 60, 60,
	         false, $2::timestamptz - interval '3 minutes', 'index out of range', 'line', NULL, $2, $2),
	        ($1, 'RT000000000002', 'gone', 2, false, '{}', 0, 0, false, '{}', 0, 2, 'en', false, 60, false, 60, 60,
	         false, $2::timestamptz - interval '3 minutes', 'index out of range', 'line', $2, $2, $2)`,
	`INSERT INTO storms (org_id, route_id, started_at, alert_group_count)
	 SELECT $1, r.id, $2::timestamptz - interval '2 minutes', 7
	 FROM routes r
	 WHERE r.org_id = $1`,
	`INSERT INTO deliveries (org_id, destination_id, storm_id, state, next_attempt_at, created_at, updated_at)
	 SELECT $1, d.id, s.id, 'pending', $2, $2, $2
	 FROM destinations d, storms s
	 WHERE d.org_id = $1 AND s.org_id = $1`,
	`UPDATE organizations
	 SET outgoing_heartbeat_url_ciphertext = '\x00', outgoing_heartbeat_url_key_id = 'k1',
	     outgoing_heartbeat_url_updated_at = $2, outgoing_heartbeat_last_attempt_at = $2,
	     outgoing_heartbeat_last_ok = false, outgoing_heartbeat_last_error = 'answered 503 (transient)'
	 WHERE id = $1`,
}

// TestSystemStatusQueries covers C-19.FR-10 on every supported PostgreSQL: each section of the System status reads
// its rows, leaves out the deleted and the healthy ones, and a Storm of a deleted Route is not active.
func TestSystemStatusQueries(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
		d, err := db.Open(ctx, conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		if err := d.Migrate(ctx, log); err != nil {
			t.Fatal(err)
		}
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), log, t0); err != nil {
			t.Fatal(err)
		}
		org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		svc := systemstatus.New(systemstatus.Config{OrgID: org.ID, Queries: systemstatus.NewQueries(d.Pool),
			Business: clock.NewManual(t0), LeaderAbsence: 2 * time.Minute, Version: "1.2.3", Commit: "abc",
			Replicas: func(context.Context) ([]keyring.Replica, error) {
				return []keyring.Replica{{ID: "replica-a", KeyIDs: []string{"k1"}, RefreshedAt: t0},
					{ID: "replica-b", RefreshedAt: t0}}, nil
			}})

		// An empty installation: no Leader, no recovery, nothing to look at.
		empty, err := svc.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if empty.Replicas[0].Leader || empty.Recovery.Active || empty.Recovery.PeriodFrom != nil ||
			len(empty.DeliveryQueues)+len(empty.BrokenDestinations)+len(empty.IntegrationsAttention)+
				len(empty.TemplateErrors)+len(empty.ActiveStorms) != 0 || empty.OutgoingHeartbeat.Configured {
			t.Errorf("empty = %+v", empty)
		}

		for _, f := range fixtures {
			if _, err := d.Pool.Exec(ctx, f, org.ID, t0); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
		}
		st, err := svc.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Replicas[0].Leader || st.Replicas[1].Leader || len(st.Replicas[1].KeyIDs) != 0 {
			t.Errorf("replicas %+v", st.Replicas)
		}
		if r := st.Recovery; !r.Active || !r.Until.Equal(t0.Add(time.Hour)) ||
			!r.PeriodFrom.Equal(t0.Add(-2*time.Hour)) || !r.PeriodTo.Equal(t0.Add(-time.Hour)) {
			t.Errorf("recovery %+v", r)
		}
		// Two Storms, one summary delivery each, in the Destinations that are not deleted.
		if q := st.DeliveryQueues; len(q) != 2 || q[0].Destination.Name != "alerts" || q[0].Queued != 2 ||
			q[1].Destination.Name != "fine" || q[1].Queued != 2 {
			t.Errorf("delivery queues %+v", q)
		}
		if b := st.BrokenDestinations; len(b) != 1 || b[0].Destination != (systemstatus.Entity{ID: "DS000000000001",
			Name: "alerts"}) || b[0].Reason != "answered 403" || !b[0].Since.Equal(t0.Add(-5*time.Minute)) {
			t.Errorf("broken destinations %+v", b)
		}
		if a := st.IntegrationsAttention; len(a) != 1 || a[0].Integration.Name != "hb" ||
			a[0].Issue != systemstatus.IssueHeartbeatLost || !a[0].Since.Equal(t0.Add(-4*time.Minute)) {
			t.Errorf("integrations attention %+v", a)
		}
		if e := st.TemplateErrors; len(e) != 2 || e[0].SubjectKind != systemstatus.SubjectDestination ||
			e[0].Template != "webhook_request" || e[0].Error != "webhook template failed" ||
			e[1].SubjectKind != systemstatus.SubjectRoute || e[1].Subject.Name != "storm" || e[1].Template != "line" ||
			e[1].Error != "index out of range" {
			t.Errorf("template errors %+v", e)
		}
		if s := st.ActiveStorms; len(s) != 1 || s[0].Route.Name != "storm" || s[0].AlertGroupCount != 7 ||
			!s[0].Since.Equal(t0.Add(-2*time.Minute)) {
			t.Errorf("active storms %+v", s)
		}
		if h := st.OutgoingHeartbeat; !h.Configured || !h.LastAttemptAt.Equal(t0) || *h.LastOK ||
			*h.LastError != "answered 503 (transient)" {
			t.Errorf("outgoing heartbeat %+v", h)
		}

		// Past leader.absence_notice without a new alive mark the Leader is no longer flagged.
		late := systemstatus.New(systemstatus.Config{OrgID: org.ID, Queries: systemstatus.NewQueries(d.Pool),
			Business: clock.NewManual(t0.Add(2 * time.Minute)), LeaderAbsence: 2 * time.Minute,
			Replicas: func(context.Context) ([]keyring.Replica, error) {
				return []keyring.Replica{{ID: "replica-a"}}, nil
			}})
		if st, err := late.Get(ctx); err != nil || st.Replicas[0].Leader {
			t.Errorf("a stale Leader = %+v, %v", st.Replicas, err)
		}
	})
}
