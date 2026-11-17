// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package mentions_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/mentions/dbgen"
)

// TestIntegrationNativeMentions is C-22.FR-16 and C-28.FR-3 for what the resolution reads of a bridge on PostgreSQL:
// GetMentionConnection says native_mentions only when Muster's copy of the bridge's info declares it true — not
// while no info was read, nor when the info leaves it out or declares it false — so that a linked person on such a
// bridge is recorded as named by the plain name.
func TestIntegrationNativeMentions(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		pool, err := pgxpool.New(ctx, s.NewDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		migrateTo(t, pool, 1, 1000)
		var org int64
		if err := pool.QueryRow(ctx, `INSERT INTO organizations (public_id, name, time_zone, severity_label,
			severity_mapping, severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
			oidc_token_grace_seconds, created_at, updated_at)
			VALUES ('RG0000000000AA', 'Muster', 'UTC', 'severity', '[]', '[]', true, '{}', 14, 90, 730, 365, 'nobody',
			604800, '2026-11-17', '2026-11-17') RETURNING id`).Scan(&org); err != nil {
			t.Fatal(err)
		}
		q := dbgen.New(pool)
		for i, c := range []struct {
			name string
			info *string
			want bool
		}{
			{"no info read", nil, false},
			{"info without native_mentions", ptr(`{"capabilities":{"group_mentions":true}}`), false},
			{"native_mentions false", ptr(`{"capabilities":{"native_mentions":false}}`), false},
			{"native_mentions true", ptr(`{"capabilities":{"native_mentions":true}}`), true},
		} {
			var id int64
			if err := pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url,
				bridge_event_delivery, bridge_info, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at,
				limiter_limit, limiter_per_seconds, created_at, updated_at)
				VALUES ($1, $2, 'bridge', $3, 'http://br', 'poll', $4::jsonb, '\x01', 'k1', '2026-11-17', 1, 1,
				'2026-11-17', '2026-11-17') RETURNING id`, org, fmt.Sprintf("CN00000000000%d", i), c.name,
				c.info).Scan(&id); err != nil {
				t.Fatal(err)
			}
			got, err := q.GetMentionConnection(ctx, dbgen.GetMentionConnectionParams{OrgID: org,
				ID: pgtype.Int8{Int64: id, Valid: true}})
			if err != nil || got.NativeMentions != c.want {
				t.Errorf("%s: native_mentions %v, %v, want %v", c.name, got.NativeMentions, err, c.want)
			}
		}
	})
}

func ptr(s string) *string { return &s }
