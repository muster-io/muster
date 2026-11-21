// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db/dbtest"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	gatewaysdb "github.com/muster-io/muster/internal/gateways/dbgen"
	groupsdb "github.com/muster-io/muster/internal/groups/dbgen"
	ingestdb "github.com/muster-io/muster/internal/ingest/dbgen"
	timersdb "github.com/muster-io/muster/internal/timers/dbgen"
)

// TestIntegrationClaimsUpdateByKey is #535 on PostgreSQL: every claim whose LIMIT … FOR UPDATE SKIP LOCKED choice is a
// materialized CTE updates the rows it chose through the primary key of its table, in a custom plan and in a generic
// one alike. Joined to the CTE, a plan that expects many chosen rows, as a generic plan does, which cannot know the
// limit and expects a tenth of the due rows, hashed the choice against a sequential scan of the whole table, and a
// semi-join of ClaimIntegrations did so in a custom plan too. The tables hold a backlog: thousands of rows, two in five
// of them due.
func TestIntegrationClaimsUpdateByKey(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		now := t0
		for _, stmt := range []string{
			`INSERT INTO users (org_id, public_id, login, name, role, source, status, created_at, updated_at)
			VALUES ($1, 'SRAAAAAAAAAAB1', 'bob', 'bob', 'responder', 'local', 'active', $2, $2)`,
			`INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url, bot_token_ciphertext,
				bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at,
				gateway_broken_since, gateway_broken_reason, gateway_next_probe_at)
			SELECT $1, 'CN' || lpad(upper(to_hex(n)), 12, '0'), 'mattermost', 'claims' || n, 'https://mm.example.org',
			       '\x00', 'k1', $2, 1000, 1, $2, $2, CASE WHEN n % 3 = 0 THEN $2::timestamptz END,
			       CASE WHEN n % 3 = 0 THEN 'down' END, CASE WHEN n % 3 = 0 THEN $2::timestamptz END
			FROM generate_series(1, 3000) n`,
			`INSERT INTO destinations (org_id, public_id, type, name, connection_id, mattermost_team_id,
				mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, broken_since, broken_cause,
				broken_reason, next_probe_at, created_at, updated_at)
			SELECT $1, 'DS' || lpad(upper(to_hex(n)), 12, '0'), 'mattermost', 'claims' || n,
			       (SELECT id FROM connections WHERE public_id = 'CNAAAAAAAAAAA1'), 'team', 'claims' || n, '{}', 1000, 1,
			       CASE WHEN n > 10 AND n % 3 = 0 THEN 'broken' ELSE 'healthy' END,
			       CASE WHEN n > 10 AND n % 3 = 0 THEN $2::timestamptz END,
			       CASE WHEN n > 10 AND n % 3 = 0 THEN 'fatal' END, CASE WHEN n > 10 AND n % 3 = 0 THEN 'down' END,
			       CASE WHEN n > 10 AND n % 3 = 0 THEN $2::timestamptz END, $2, $2
			FROM generate_series(1, 3000) n`,
			`INSERT INTO alert_groups (org_id, public_id, number, route_id, group_key_labels, group_key_values,
				group_key_sha256, title, common_labels, common_annotations, integration_ids, status, severity_level,
				urgent, resolved_at, resolved_by_kind, resolve_reason, firing_alert_count, resolved_alert_count,
				created_at, last_changed_at)
			SELECT $1, 'AG' || lpad(upper(to_hex(n)), 12, '0'), 100000 + n,
			       (SELECT id FROM routes WHERE org_id = $1 AND is_default), '{alertname}', '{}',
			       sha256(convert_to('claims' || n, 'UTF8')), 'claims', '{}', '{}', '{}', 'resolved', 'warning', false,
			       $2, 'system', 'gone', 0, 1, $2, $2
			FROM generate_series(1, 5000) n`,
			// Ten deliveries of each Alert Group, two in five pending and due.
			`INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, next_attempt_at, published_at,
				created_at, updated_at)
			SELECT g.org_id, ds.id, g.id, CASE WHEN (g.id + ds.id) % 5 < 2 THEN 'pending' ELSE 'delivered' END, $2, $2,
			       $2, $2
			FROM alert_groups g
			CROSS JOIN (SELECT id FROM destinations
			            WHERE org_id = $1 AND public_id LIKE 'DS%' AND public_id <= 'DS00000000000A') ds
			WHERE g.org_id = $1 AND g.title = 'claims'`,
			`INSERT INTO thread_replies (org_id, delivery_id, alert_group_id, destination_id, event, event_seqs, loudness,
				state, next_attempt_at, created_at)
			SELECT org_id, id, alert_group_id, destination_id, 'alerts_added', '{1}', 'quiet',
			       CASE WHEN id % 5 < 2 THEN 'pending' ELSE 'sent' END, $2, $2
			FROM deliveries WHERE org_id = $1`,
			`INSERT INTO webhook_events (org_id, destination_id, alert_group_id, sequence, webhook_id, event, notify,
				occurred_at, body, state, next_attempt_at, created_at)
			SELECT g.org_id, (SELECT id FROM destinations WHERE public_id = 'DS000000000001'), g.id, s,
			       'claims-' || g.id || '-' || s, 'created', false, $2, '{}',
			       CASE WHEN s = 3 AND g.id % 5 < 2 THEN 'pending' ELSE 'delivered' END, $2, $2
			FROM alert_groups g CROSS JOIN generate_series(1, 3) s
			WHERE g.org_id = $1 AND g.title = 'claims'`,
			`INSERT INTO timers (org_id, alert_group_id, kind, deadline, created_at, updated_at)
			SELECT g.org_id, g.id, k, $2::timestamptz + CASE WHEN g.id % 5 < 2 THEN interval '0' ELSE interval '1 day' END,
			       $2, $2
			FROM alert_groups g CROSS JOIN unnest('{ack_timeout,reminder,snooze_end}'::text[]) k
			WHERE g.org_id = $1 AND g.title = 'claims'`,
			`INSERT INTO gateway_calls (org_id, public_id, connection_id, user_id, state, language, text,
				next_attempt_at, attempts, first_attempt_at, ended_at, created_at)
			SELECT $1, 'CA' || lpad(upper(to_hex(n)), 12, '0'),
			       (SELECT id FROM connections WHERE public_id = 'CN000000000001'),
			       (SELECT id FROM users WHERE public_id = 'SRAAAAAAAAAAB1'),
			       CASE WHEN n % 5 < 2 THEN 'sending' ELSE 'accepted' END, 'en', CASE WHEN n % 5 < 2 THEN 'call' END,
			       CASE WHEN n % 5 < 2 THEN $2::timestamptz END, 1, $2,
			       CASE WHEN n % 5 >= 2 THEN $2::timestamptz END, $2
			FROM generate_series(1, 30000) n`,
			`INSERT INTO open_group_updates (org_id, kind, requested_at)
			SELECT $1, 'organization_buttons', $2 FROM generate_series(1, 3000)`,
			`INSERT INTO integrations (org_id, public_id, name, connection_mode, static_labels, duplicate_window_seconds,
				heartbeat_enabled, heartbeat_timeout_seconds, heartbeat_state, created_at, updated_at)
			SELECT $1, 'NT' || lpad(upper(to_hex(n)), 12, '0'), 'claims' || n, 'webhook_only', '{}', 300, false, 300,
			       'not_configured', $2, $2
			FROM generate_series(1, 3000) n`,
		} {
			l.exec(t, stmt, l.orgID, now)
		}
		l.exec(t, `INSERT INTO ingest_claims (integration_id, org_id)
			SELECT id, org_id FROM integrations WHERE org_id = $1
			ON CONFLICT (integration_id) DO NOTHING`, l.orgID)
		l.exec(t, `ANALYZE`)
		var integrationIDs []int64
		if err := l.d.Pool.QueryRow(t.Context(), `SELECT array_agg(integration_id) FROM ingest_claims
			WHERE org_id = $1`, l.orgID).Scan(&integrationIDs); err != nil {
			t.Fatal(err)
		}

		org, lease, probe := l.orgID, now.Add(time.Minute), now.Add(time.Hour)
		for _, c := range []struct {
			name, table string
			claim       func(ctx context.Context, tx pgx.Tx) error
		}{
			{"ClaimDueDeliveries", "deliveries", func(ctx context.Context, tx pgx.Tx) error {
				_, err := deliverydb.New(tx).ClaimDueDeliveries(ctx, deliverydb.ClaimDueDeliveriesParams{Owner: "r1",
					LeaseUntil: lease, OrgID: org, Due: now, Now: now, Lim: 10})
				return err
			}},
			{"ClaimDueReplies", "thread_replies", func(ctx context.Context, tx pgx.Tx) error {
				_, err := deliverydb.New(tx).ClaimDueReplies(ctx, deliverydb.ClaimDueRepliesParams{Owner: "r1",
					LeaseUntil: lease, OrgID: org, Due: now, Now: now, Lim: 10})
				return err
			}},
			{"ClaimDueWebhookEvents", "webhook_events", func(ctx context.Context, tx pgx.Tx) error {
				_, err := deliverydb.New(tx).ClaimDueWebhookEvents(ctx, deliverydb.ClaimDueWebhookEventsParams{
					Owner: "r1", LeaseUntil: lease, OrgID: org, Due: now, Now: now, Lim: 10})
				return err
			}},
			{"ClaimBrokenProbes", "destinations", func(ctx context.Context, tx pgx.Tx) error {
				_, err := deliverydb.New(tx).ClaimBrokenProbes(ctx, deliverydb.ClaimBrokenProbesParams{
					NextProbe: probe, OrgID: org, Due: now, Now: now, Lim: 10})
				return err
			}},
			{"ClaimDueTimers", "timers", func(ctx context.Context, tx pgx.Tx) error {
				_, err := timersdb.New(tx).ClaimDueTimers(ctx, timersdb.ClaimDueTimersParams{Owner: "r1",
					LeaseUntil: lease, OrgID: org, Due: now, Now: now, Lim: 100,
					Kinds: []string{"ack_timeout", "reminder", "snooze_end"}})
				return err
			}},
			{"ClaimDueCalls", "gateway_calls", func(ctx context.Context, tx pgx.Tx) error {
				_, err := gatewaysdb.New(tx).ClaimDueCalls(ctx, gatewaysdb.ClaimDueCallsParams{Owner: "r1",
					LeaseUntil: lease, OrgID: org, Due: now, Now: now, Lim: 10})
				return err
			}},
			{"ClaimGatewayProbes", "connections", func(ctx context.Context, tx pgx.Tx) error {
				_, err := gatewaysdb.New(tx).ClaimGatewayProbes(ctx, gatewaysdb.ClaimGatewayProbesParams{Owner: "r1",
					LeaseUntil: lease, NextProbe: probe, OrgID: org, Due: now, Now: now, Lim: 10})
				return err
			}},
			{"ClaimOpenGroupUpdate", "open_group_updates", func(ctx context.Context, tx pgx.Tx) error {
				_, err := groupsdb.New(tx).ClaimOpenGroupUpdate(ctx, groupsdb.ClaimOpenGroupUpdateParams{Owner: "r1",
					LeaseUntil: lease, OrgID: org, Kinds: []string{"organization_buttons"}, Now: now})
				return err
			}},
			{"ClaimIntegrations", "ingest_claims", func(ctx context.Context, tx pgx.Tx) error {
				_, err := ingestdb.New(tx).ClaimIntegrations(ctx, ingestdb.ClaimIntegrationsParams{
					Owner:      pgtype.Text{String: "r1", Valid: true},
					LeaseUntil: pgtype.Timestamptz{Time: lease, Valid: true}, OrgID: org,
					IntegrationIds: integrationIDs, Now: now, BatchSize: 10})
				return err
			}},
		} {
			for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
				scans := dbtest.UpdateScans(t, l.d.Pool.Config().ConnString(), mode,
					func(ctx context.Context, conn *pgx.Conn) {
						tx, err := conn.Begin(ctx)
						if err != nil {
							t.Fatal(err)
						}
						defer func() { _ = tx.Rollback(ctx) }()
						if err := c.claim(ctx, tx); err != nil {
							t.Fatalf("%s: %v", c.name, err)
						}
					})
				if len(scans) != 1 || !strings.HasPrefix(scans[0], "Index Scan using "+c.table+"_pkey on ") {
					t.Errorf("%s, %s: the update reads %s by %q, not by its primary key", c.name, mode, c.table, scans)
				}
			}
		}
	})
}
