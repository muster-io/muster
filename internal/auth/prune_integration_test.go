// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/auth/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/leader"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// TestIntegrationShortLivedPruning runs the short_lived_pruning Leader task over seeded sessions and sign-in throttles:
// it deletes, in batches, the sessions that ended or expired more than auth.session_prune_after ago and the throttles
// without a failure for auth.signin_throttle_prune_after, keeps every other row and every row of an Organization it is
// not given, and a second run deletes nothing more.
func TestIntegrationShortLivedPruning(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		d, orgID := setup(t, s)
		now := t0.Add(30 * 24 * time.Hour)
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		var userID int64
		if err := d.Pool.QueryRow(ctx, `SELECT id FROM users WHERE org_id = $1`, orgID).Scan(&userID); err != nil {
			t.Fatal(err)
		}

		// Sessions by public_id: created, idle expiry, absolute expiry, end.
		day := 24 * time.Hour
		type session struct {
			id              string
			idle, abs       time.Duration
			ended           time.Duration
			endReason, kept string
		}
		sessions := []session{
			{id: "SN0000000000A1", idle: time.Hour, abs: 6 * day, kept: "live"},
			{id: "SN0000000000A2", idle: -day, abs: 6 * day, ended: -8 * day, endReason: "sign_out"},
			{id: "SN0000000000A3", idle: time.Hour, abs: 6 * day, ended: -6 * day, endReason: "sign_out",
				kept: "ended within the grace"},
			{id: "SN0000000000A4", idle: -8 * day, abs: 6 * day},
			{id: "SN0000000000A5", idle: -day, abs: 6 * day, kept: "idle within the grace"},
			{id: "SN0000000000A6", idle: time.Hour, abs: -8 * day},
			{id: "SN0000000000A7", idle: -10 * day, abs: -9 * day, ended: -10 * day, endReason: "expired"},
		}
		for _, sn := range sessions {
			var ended, reason any
			if sn.endReason != "" {
				ended, reason = now.Add(sn.ended), sn.endReason
			}
			exec(`INSERT INTO sessions (org_id, public_id, user_id, token_hash, state, method, created_at, last_used_at,
				idle_expires_at, expires_at, ended_at, end_reason)
				VALUES ($1, $2, $3, sha256($4::bytea), 'active', 'local', $5, $5, $6, $7, $8, $9)`,
				orgID, sn.id, userID, []byte(sn.id), now.Add(-20*day), now.Add(sn.idle), now.Add(sn.abs), ended,
				reason)
		}
		// A backlog larger than two batches of sessions that ended long ago.
		backlog := 2*leader.PruneBatch + 7
		exec(`INSERT INTO sessions (org_id, public_id, user_id, token_hash, state, method, created_at, last_used_at,
			idle_expires_at, expires_at, ended_at, end_reason)
			SELECT $1, 'SN' || lpad(i::text, 12, '0'), $2, sha256(('bulk' || i)::bytea), 'active', 'local', $3, $3,
				$4, $4, $4, 'user_disabled'
			FROM generate_series(1, $5::int) AS i`,
			orgID, userID, now.Add(-20*day), now.Add(-10*day), backlog)

		// A second Organization that the task is not given.
		var otherOrg int64
		if err := d.Pool.QueryRow(ctx, `INSERT INTO organizations (public_id, name, time_zone, severity_label,
			severity_mapping, severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days,
			totp_required, oidc_token_grace_seconds, created_at, updated_at)
			SELECT 'RG0000000000ZZ', 'Other', time_zone, severity_label, severity_mapping, severity_styles,
				critical_is_urgent, instance_labels, retention_stored_snapshots_days, retention_alert_details_days,
				retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
				oidc_token_grace_seconds, created_at, updated_at
			FROM organizations WHERE id = $1
			RETURNING id`, orgID).Scan(&otherOrg); err != nil {
			t.Fatal(err)
		}
		throttle := func(org int64, subject string, lastFailure time.Duration, blocked any) {
			exec(`INSERT INTO sign_in_throttles (org_id, subject_kind, subject, consecutive_failures, last_failure_at,
				blocked_until) VALUES ($1, 'address', $2, 4, $3, $4)`, org, subject, now.Add(lastFailure), blocked)
		}
		throttle(orgID, "192.0.2.1", -25*time.Hour, now.Add(-25*time.Hour+2*time.Second)) // stale and unblocked
		throttle(orgID, "192.0.2.2", -25*time.Hour, nil)                                  // stale, never blocked
		throttle(orgID, "192.0.2.3", -23*time.Hour, now.Add(-23*time.Hour+time.Second))   // failed within 24 h
		throttle(orgID, "192.0.2.4", -25*time.Hour, now.Add(time.Minute))                 // still blocked
		throttle(otherOrg, "192.0.2.5", -48*time.Hour, nil)                               // another Organization

		var log bytes.Buffer
		task := leader.Tasks(leader.Work{
			Organizations: func(context.Context) ([]int64, error) { return []int64{orgID}, nil },
			Business:      clock.NewManual(now),
			Log:           logging.New(&log, logging.LevelInfo),
			PruneAuth: []leader.PruneTable{
				{Name: "sessions", Delete: auth.NewPruner(dbgen.New(d.Pool)).Sessions},
				{Name: "sign_in_throttles", Delete: auth.NewPruner(dbgen.New(d.Pool)).SignInThrottles},
			},
		})()[3]
		if task.Name != "short_lived_pruning" {
			t.Fatalf("task %s", task.Name)
		}
		counted := func() (uint64, uint64) {
			return metrics.ShortLivedRowsPruned.With("sessions").Get(),
				metrics.ShortLivedRowsPruned.With("sign_in_throttles").Get()
		}
		sessionsBefore, throttlesBefore := counted()
		if err := task.Run(ctx); err != nil {
			t.Fatal(err)
		}

		list := func(sql string, args ...any) []string {
			t.Helper()
			rows, err := d.Pool.Query(ctx, sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var out []string
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err != nil {
					t.Fatal(err)
				}
				out = append(out, v)
			}
			return out
		}
		check := func(run string) {
			t.Helper()
			var keptSessions []string
			for _, sn := range sessions {
				if sn.kept != "" {
					keptSessions = append(keptSessions, sn.id)
				}
			}
			if got := list(`SELECT public_id FROM sessions ORDER BY public_id`); !slices.Equal(got, keptSessions) {
				t.Errorf("%s: sessions left %v, want %v", run, got, keptSessions)
			}
			want := []string{"192.0.2.3", "192.0.2.4", "192.0.2.5"}
			if got := list(`SELECT subject FROM sign_in_throttles ORDER BY subject`); !slices.Equal(got, want) {
				t.Errorf("%s: throttles left %v, want %v", run, got, want)
			}
		}
		check("first run")
		sessionsAfter, throttlesAfter := counted()
		if sessionsAfter-sessionsBefore != uint64(backlog+4) || throttlesAfter-throttlesBefore != 2 {
			t.Errorf("counted %d sessions and %d throttles", sessionsAfter-sessionsBefore,
				throttlesAfter-throttlesBefore)
		}
		for _, want := range []string{`"event":"short_lived_pruned","table":"sessions","rows":2011`,
			`"event":"short_lived_pruned","table":"sign_in_throttles","rows":2`} {
			if !strings.Contains(log.String(), want) {
				t.Errorf("log misses %s: %s", want, log.String())
			}
		}

		// A second run, as an overlapping Leader would make, deletes, logs and counts nothing more.
		log.Reset()
		if err := task.Run(ctx); err != nil {
			t.Fatal(err)
		}
		check("second run")
		if s, th := counted(); s != sessionsAfter || th != throttlesAfter || log.Len() != 0 {
			t.Errorf("second run counted %d and %d more, logged %s", s-sessionsAfter, th-throttlesAfter, log.String())
		}
	})
}
