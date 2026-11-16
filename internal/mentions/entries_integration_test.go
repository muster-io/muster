// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package mentions_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/mentions"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

// migrateTo runs the up migrations of internal/db/migrations through the number to, each file as golang-migrate runs
// it, in one call.
func migrateTo(t *testing.T, pool *pgxpool.Pool, from, to int) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "db", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	for _, f := range files {
		n, err := strconv.Atoi(strings.SplitN(filepath.Base(f), "_", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		if n < from || n > to {
			continue
		}
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), string(sql)); err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
	}
}

// TestIntegrationMigrationConversion is C-28.FR-5 and AC-5 against PostgreSQL: a database at 0042 with the L1 settings
// of a Destination of each type, those of AC-5 and settings that mention nobody, migrated by 0043, has for each
// exactly the entries mentions.FromSettings gives for the same settings — so that the migration and the conversion of
// requests cannot drift apart — whose L1 form reads the same as the settings; the Destination that mentioned nobody has
// none. Settings L1 refused on a type, which an earlier rule may have stored, are left out by both.
func TestIntegrationMigrationConversion(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		pool, err := pgxpool.New(ctx, s.NewDatabase(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		migrateTo(t, pool, 1, 42)
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		exec(`INSERT INTO organizations (public_id, name, time_zone, severity_label, severity_mapping,
			severity_styles, critical_is_urgent, instance_labels, retention_stored_snapshots_days,
			retention_alert_details_days, retention_alert_group_summaries_days, retention_audit_log_days, totp_required,
			oidc_token_grace_seconds, created_at, updated_at)
			VALUES ('RG0000000000AA', 'Muster', 'UTC', 'severity', '[]', '[]', true, '{}', 14, 90, 730, 365, 'nobody',
			604800, '2026-11-16', '2026-11-16')`)
		exec(`INSERT INTO users (org_id, public_id, login, name, role, source, status, deleted_at, created_at,
			updated_at)
			SELECT o.id, u.public_id, u.login, u.login, 'responder', 'local', u.status,
				CASE WHEN u.status = 'deleted' THEN '2026-11-16'::timestamptz END, '2026-11-16', '2026-11-16'
			FROM organizations o, (VALUES ('SR0000000000AA', 'alice', 'active'), ('SR0000000000BB', 'bob', 'active'),
				('SR0000000000GN', 'gone', 'deleted')) AS u (public_id, login, status)`)
		exec(`INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url, telegram_bot_api_base_url,
			telegram_update_mode, bridge_base_url, bridge_event_delivery, slack_event_mode,
			slack_signing_secret_ciphertext, slack_signing_secret_key_id, slack_signing_secret_updated_at,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at)
			SELECT o.id, c.public_id, c.type, c.type, c.server, c.bot_api, c.mode, c.base, c.delivery, c.slack_mode,
				c.signing, c.signing_key, c.signing_at::timestamptz, '\x01', 'k1', '2026-11-16', 1, 1, '2026-11-16',
				'2026-11-16'
			FROM organizations o, (VALUES
				('CN0000000000MM', 'mattermost', 'http://mm', NULL, NULL, NULL, NULL, NULL, NULL::bytea, NULL, NULL),
				('CN0000000000TG', 'telegram', NULL, 'http://tg', 'long_polling', NULL, NULL, NULL, NULL, NULL, NULL),
				('CN0000000000SK', 'slack', NULL, NULL, NULL, NULL, NULL, 'http', '\x02'::bytea, 'k1', '2026-11-16'),
				('CN0000000000BR', 'bridge', NULL, NULL, NULL, 'http://br', 'poll', NULL, NULL, NULL, NULL))
				AS c (public_id, type, server, bot_api, mode, base, delivery, slack_mode, signing, signing_key,
					signing_at)`)

		setting := func(everyone string, users, groups []string) mentions.Setting {
			return mentions.Setting{Everyone: everyone, UserIDs: append([]string{}, users...),
				Groups: append([]string{}, groups...)}
		}
		rich := mentions.Nobody()
		rich[mentions.KindNewAlertGroup] = setting(mentions.EveryoneChannel, nil, []string{"sre"})
		rich[mentions.KindNewAlerts] = setting(mentions.EveryoneNone, []string{"SR0000000000AA", "sr0000000000bb"},
			[]string{"sre", "ops"})
		rich[mentions.KindReopen] = setting(mentions.EveryoneAll, []string{"SR0000000000GN"}, nil)
		rich[mentions.KindAckTimeout] = setting(mentions.EveryoneHere, []string{"SR0000000000AA"}, nil)
		slack := mentions.Nobody()
		slack[mentions.KindNewAlertGroup] = setting(mentions.EveryoneHere, []string{"SR0000000000BB"}, nil)
		slack[mentions.KindNewAlerts] = setting(mentions.EveryoneChannel, []string{"SR0000000000AA"}, nil)
		slack[mentions.KindRiseToUrgent] = setting(mentions.EveryoneAll, nil, []string{"stored-before"})
		people := mentions.Nobody()
		people[mentions.KindNewAlertGroup] = setting(mentions.EveryoneNone, []string{"SR0000000000AA"}, nil)
		people[mentions.KindSnoozeEnded] = setting(mentions.EveryoneChannel, []string{"SR0000000000BB",
			"SR0000000000AA"}, []string{"ops"})
		ac5 := mentions.Nobody()
		ac5[mentions.KindNewAlertGroup] = setting(mentions.EveryoneChannel, nil, nil)
		ac5[mentions.KindAckTimeout] = setting(mentions.EveryoneNone, []string{"SR0000000000AA"}, nil)
		// A group named twice in one kind, which an earlier rule may have stored, is one choice.
		twice := mentions.Nobody()
		twice[mentions.KindNewAlertGroup] = setting(mentions.EveryoneNone, nil, []string{"sre", "sre", "ops"})
		// The L1 forms the migrated entries read: the settings without what L1 refused on the type, each User's
		// public_id canonical, each choice once.
		richForm := mentions.Nobody()
		for k, v := range rich {
			richForm[k] = v
		}
		richForm[mentions.KindNewAlerts] = setting(mentions.EveryoneNone, []string{"SR0000000000AA",
			"SR0000000000BB"}, []string{"sre", "ops"})
		slackForm := mentions.Nobody()
		slackForm[mentions.KindNewAlertGroup] = slack[mentions.KindNewAlertGroup]
		slackForm[mentions.KindNewAlerts] = slack[mentions.KindNewAlerts]
		peopleForm := mentions.Nobody()
		peopleForm[mentions.KindNewAlertGroup] = people[mentions.KindNewAlertGroup]
		// One entry per User, in the order of their first kind: within a kind the form lists them in that order.
		peopleForm[mentions.KindSnoozeEnded] = setting(mentions.EveryoneNone, []string{"SR0000000000AA",
			"SR0000000000BB"}, nil)
		twiceForm := mentions.Nobody()
		twiceForm[mentions.KindNewAlertGroup] = setting(mentions.EveryoneNone, nil, []string{"sre", "ops"})
		type dest struct {
			publicID, typ, conn, fields string
			set, form                   mentions.Settings
		}
		const mmFields = "mattermost_team_id, mattermost_channel_id"
		dests := []dest{
			{"DS0000000000MM", "mattermost", "CN0000000000MM", mmFields, rich, richForm},
			{"DS0000000000A5", "mattermost", "CN0000000000MM", mmFields, ac5, ac5},
			{"DS0000000000NB", "mattermost", "CN0000000000MM", mmFields, mentions.Nobody(), mentions.Nobody()},
			{"DS0000000000TW", "mattermost", "CN0000000000MM", mmFields, twice, twiceForm},
			{"DS0000000000TG", "telegram", "CN0000000000TG", "telegram_channel_id, telegram_channel_title", people,
				peopleForm},
			{"DS0000000000BR", "bridge", "CN0000000000BR", "bridge_channel_id, bridge_channel_name", people,
				peopleForm},
			{"DS0000000000SK", "slack", "CN0000000000SK", "slack_channel_id, slack_channel_name", slack, slackForm},
		}
		for _, d := range dests {
			b, err := json.Marshal(d.set)
			if err != nil {
				t.Fatal(err)
			}
			exec(`INSERT INTO destinations (org_id, public_id, type, name, connection_id, `+d.fields+`, mentions,
				limiter_limit, limiter_per_seconds, health, created_at, updated_at)
				SELECT o.id, $1, $2, $1, c.id, 'C0FAKEALERTS', 'alerts', $3::jsonb, 1, 1, 'healthy', '2026-11-16',
					'2026-11-16'
				FROM organizations o JOIN connections c ON c.org_id = o.id AND c.public_id = $4`,
				d.publicID, d.typ, string(b), d.conn)
		}
		b, _ := json.Marshal(rich)
		exec(`INSERT INTO destinations (org_id, public_id, type, name, webhook_mode, webhook_events_config,
			signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at, mentions, limiter_limit,
			limiter_per_seconds, health, created_at, updated_at)
			SELECT id, 'DS0000000000WH', 'webhook', 'hook', 'events', '{}', '\x00', 'k1', '2026-11-16', $1::jsonb, 1,
				1, 'healthy', '2026-11-16', '2026-11-16'
			FROM organizations`, string(b))
		dests = append(dests, dest{publicID: "DS0000000000WH", typ: "webhook", set: rich, form: richForm})

		migrateTo(t, pool, 43, 43)

		var org int64
		if err := pool.QueryRow(ctx, `SELECT id FROM organizations`).Scan(&org); err != nil {
			t.Fatal(err)
		}
		svc := mentions.New(org, pool)
		for _, d := range dests {
			var id int64
			if err := pool.QueryRow(ctx, `SELECT id FROM destinations WHERE public_id = $1`, d.publicID).Scan(
				&id); err != nil {
				t.Fatal(err)
			}
			conn := ""
			if d.typ != "webhook" {
				conn = d.conn
			}
			got, err := svc.Entries(ctx, nil, []mentions.EntryDestination{{ID: id, Type: d.typ, Connection: conn}})
			if err != nil {
				t.Fatal(err)
			}
			entries := mentions.Inputs(got[id])
			want := mentions.FromSettings(d.typ, d.set)
			if len(want) == 0 {
				want = []mentions.EntryInput{}
			}
			if !reflect.DeepEqual(entries, want) {
				t.Errorf("%s: migrated %+v, FromSettings %+v", d.publicID, entries, want)
			}
			// The events as stored, which a read puts in order and could deduplicate.
			rows, err := pool.Query(ctx, `SELECT events FROM mention_entries WHERE destination_id = $1
				ORDER BY position`, id)
			if err != nil {
				t.Fatal(err)
			}
			var raw [][]string
			for rows.Next() {
				var ev []string
				if err := rows.Scan(&ev); err != nil {
					t.Fatal(err)
				}
				raw = append(raw, ev)
			}
			rows.Close()
			var wantRaw [][]string
			for _, w := range want {
				wantRaw = append(wantRaw, w.Events)
			}
			if !reflect.DeepEqual(raw, wantRaw) {
				t.Errorf("%s: stored events %v, want %v", d.publicID, raw, wantRaw)
			}
			if form := mentions.L1Form(d.typ, entries); !reflect.DeepEqual(form, d.form) {
				t.Errorf("%s: the L1 form %+v, want %+v", d.publicID, form, d.form)
			}
		}
		// AC-5 in full: @channel for new_alert_group and storm_started, Alice for ack_timeout, reading the same form.
		var id int64
		if err := pool.QueryRow(ctx, `SELECT id FROM destinations WHERE public_id = 'DS0000000000A5'`).Scan(
			&id); err != nil {
			t.Fatal(err)
		}
		got, err := svc.Entries(ctx, nil, []mentions.EntryDestination{{ID: id, Type: "mattermost",
			Connection: "CN0000000000MM"}})
		if err != nil {
			t.Fatal(err)
		}
		want := []mentions.EntryInput{
			{Target: mentions.EntryTarget{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneChannel},
				Events: []string{mentions.KindNewAlertGroup, mentions.EventStormStarted}},
			{Target: mentions.EntryTarget{Kind: mentions.TargetUser, UserID: "SR0000000000AA"},
				Events: []string{mentions.KindAckTimeout}}}
		if entries := mentions.Inputs(got[id]); !reflect.DeepEqual(entries, want) ||
			!reflect.DeepEqual(mentions.L1Form("mattermost", entries), ac5) {
			t.Errorf("AC-5: %+v", entries)
		}
	})
}
