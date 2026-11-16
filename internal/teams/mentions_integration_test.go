// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package teams_test

import (
	"slices"
	"testing"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/teams"
)

// TestIntegrationTeamMentions is C-25.FR-8, FR-10 and FR-11 against PostgreSQL: what a Connection can mention is read
// from the Connection — a Slack one's slack_usergroups_read, a Bridge's stored info with group_mentions and its roles —
// a Team mention is saved and read with its Connection, a Notification gateway only and a deleted Connection are not
// offered, a lost ability keeps the Team mention unavailable, and deleting the Team deletes its Team mentions.
func TestIntegrationTeamMentions(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO connections (org_id, public_id, type, name,
			mattermost_server_url, telegram_bot_api_base_url, telegram_update_mode, bridge_base_url,
			bridge_event_delivery, bridge_info, slack_event_mode, slack_signing_secret_ciphertext,
			slack_signing_secret_key_id, slack_signing_secret_updated_at, slack_usergroups_read, bot_token_ciphertext,
			bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, deleted_at, created_at,
			updated_at)
			SELECT $1, c.public_id, c.type, c.name, c.server, c.bot_api, c.mode, c.base, c.delivery, c.info::jsonb,
				c.slack_mode, c.signing, c.signing_key, c.signing_at::timestamptz, c.usergroups, '\x01', 'k1', $2, 1, 1,
				c.deleted::timestamptz, $2, $2
			FROM (VALUES
				('CN0000000000MM', 'mattermost', 'Mattermost', 'http://mm', NULL, NULL, NULL, NULL, NULL, NULL, NULL,
					NULL, NULL, NULL::boolean, NULL),
				('CN0000000000TG', 'telegram', 'Telegram', NULL, 'http://tg', 'long_polling', NULL, NULL, NULL, NULL,
					NULL, NULL, NULL, NULL, NULL),
				('CN0000000000SK', 'slack', 'Slack', NULL, NULL, NULL, NULL, NULL, NULL, 'http', '\x02'::bytea, 'k1',
					'2026-10-27', true, NULL),
				('CN0000000000BR', 'bridge', 'Bridge', NULL, NULL, NULL, 'http://br', 'poll',
					'{"capabilities":{"group_mentions":true}}', NULL, NULL, NULL, NULL, NULL, NULL),
				('CN0000000000GW', 'bridge', 'Gateway', NULL, NULL, NULL, 'http://gw', 'poll',
					'{"roles":["gateway"],"capabilities":{"group_mentions":true}}', NULL, NULL, NULL, NULL, NULL, NULL),
				('CN0000000000DX', 'mattermost', 'Old', 'http://old', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL,
					NULL, NULL, '2026-10-27'))
				AS c (public_id, type, name, server, bot_api, mode, base, delivery, info, slack_mode, signing,
					signing_key, signing_at, usergroups, deleted)`, e.orgID, t0); err != nil {
			t.Fatal(err)
		}
		pay, err := e.svc.Create(ctx, by, teams.Input{Name: "Payments", Slug: "payments",
			Mentions: &[]teams.MentionInput{{ConnectionID: "CN0000000000MM", Group: "payments_team"},
				{ConnectionID: "CN0000000000SK", Group: "payments-team", GroupID: "S0FAKEPAYTEAM"},
				{ConnectionID: "CN0000000000BR", Group: "payments"}}})
		if err != nil {
			t.Fatal(err)
		}
		names := func(team teams.Team) ([]string, []string) {
			var ms, cs []string
			for _, m := range team.Mentions {
				ms = append(ms, m.Connection.Name+":"+m.Group+":"+m.GroupID+":"+m.UnavailableReason)
			}
			for _, c := range team.MentionConnections {
				cs = append(cs, c.Connection.Name+":"+c.UnavailableReason)
			}
			return ms, cs
		}
		ms, cs := names(pay)
		if !slices.Equal(ms, []string{"Bridge:payments::", "Mattermost:payments_team::",
			"Slack:payments-team:S0FAKEPAYTEAM:"}) ||
			!slices.Equal(cs, []string{"Bridge:", "Mattermost:", "Slack:", "Telegram:telegram_no_groups"}) {
			t.Fatalf("created %v, %v", ms, cs)
		}
		for _, refused := range []string{"CN0000000000TG", "CN0000000000GW"} {
			if _, err := e.svc.Update(ctx, by, pay.PublicID, nil, teams.Input{Name: "Payments", Slug: "payments",
				Mentions: &[]teams.MentionInput{{ConnectionID: refused, Group: "x"}}}); err == nil {
				t.Errorf("%s: a Team mention was saved", refused)
			}
		}
		if _, err := e.svc.Update(ctx, by, pay.PublicID, nil, teams.Input{Name: "Payments", Slug: "payments",
			Mentions: &[]teams.MentionInput{{ConnectionID: "CN0000000000DX", Group: "x"}}}); err == nil {
			t.Error("a Team mention for a deleted Connection was saved")
		}

		if _, err := e.d.Pool.Exec(ctx, `UPDATE connections SET slack_usergroups_read = false,
			bridge_info = '{"capabilities":{}}' WHERE org_id = $1 AND public_id IN ('CN0000000000SK', 'CN0000000000BR')`,
			e.orgID); err != nil {
			t.Fatal(err)
		}
		got, err := e.svc.Get(ctx, pay.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		ms, cs = names(got)
		if !slices.Equal(ms, []string{"Bridge:payments::bridge_no_group_mentions", "Mattermost:payments_team::",
			"Slack:payments-team:S0FAKEPAYTEAM:usergroups_read_missing"}) ||
			!slices.Equal(cs, []string{"Bridge:bridge_no_group_mentions", "Mattermost:",
				"Slack:usergroups_read_missing", "Telegram:telegram_no_groups"}) {
			t.Fatalf("after the abilities were lost %v, %v", ms, cs)
		}
		var mm, sk int64
		if err := e.d.Pool.QueryRow(ctx, `SELECT (SELECT id FROM connections WHERE public_id = 'CN0000000000MM'),
			(SELECT id FROM connections WHERE public_id = 'CN0000000000SK')`).Scan(&mm, &sk); err != nil {
			t.Fatal(err)
		}
		// Through a caller's transaction as through the pool.
		tx, err := e.d.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m, ok, err := e.svc.ForConnection(ctx, tx, pay.ID, mm); err != nil || !ok || m.Group != "payments_team" {
			t.Fatalf("ForConnection mattermost in a transaction = %+v, %v, %v", m, ok, err)
		}
		_ = tx.Rollback(ctx)
		if _, _, err := e.svc.ForConnection(ctx, tx, pay.ID, mm); err == nil {
			t.Fatal("ForConnection through an ended transaction read through the pool")
		}
		if m, ok, err := e.svc.ForConnection(ctx, nil, pay.ID, mm); err != nil || !ok || m.Group != "payments_team" {
			t.Fatalf("ForConnection mattermost = %+v, %v, %v", m, ok, err)
		}
		if m, ok, err := e.svc.ForConnection(ctx, nil, pay.ID, sk); err != nil || ok {
			t.Fatalf("ForConnection slack without the scope = %+v, %v, %v", m, ok, err)
		}

		if err := e.svc.Delete(ctx, by, pay.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if n := e.count(t, `SELECT count(*) FROM team_mentions WHERE org_id = $1`, e.orgID); n != 0 {
			t.Fatalf("%d team mentions after the deletion of the Team", n)
		}
		if _, ok, err := e.svc.ForConnection(ctx, nil, pay.ID, mm); err != nil || ok {
			t.Fatalf("ForConnection after the deletion = %v, %v", ok, err)
		}
	})
}
