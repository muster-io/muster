// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package destinations_test

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/destinations/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// TestIntegrationChainPagedTelegramPeople is ListChainPagedTelegramPeople of C-27.FR-8 on PostgreSQL (C-32.FR-8): the
// people with a Telegram Account link whom a chain Paging in the Telegram Destination tg can Page through a Schedule,
// by name — ann, in a Rotation layer of the Schedule that the chain of the Route "Infrastructure", which names tg,
// Pages; bob, taking an Override of it that ends after now; gus, in a Rotation layer of the Schedule whose On-call the
// chain of the Team ops, which names tg, Pages. Not cat, whose Override ended before now, dan, without a Telegram
// link, eve, a Viewer, lou, disabled, both in the Rotation layer of ann, fay, in a Schedule whose chain Pages only in
// another Destination, hal and kim, in Schedules whose chains a deleted Route and a deleted Team name, jay, in a
// Schedule whose On-call the chain of a Team naming tg only calls through a Notification gateway, which writes nothing
// in a Thread (C-32.FR-9), nor ivy, in the Rotation layer of a Schedule that no step names and taking an Override of
// it that ends after now, while a chain of a Route naming tg Pages only people.
func TestIntegrationChainPagedTelegramPeople(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
		d, err := db.Open(ctx, conn, conn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
		if err := d.Migrate(ctx, logger); err != nil {
			t.Fatal(err)
		}
		t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
		if err := organization.Ensure(ctx, organization.NewStore(d.Pool), logger, t0); err != nil {
			t.Fatal(err)
		}
		org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
		if err != nil {
			t.Fatal(err)
		}
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		id := func(sql string, args ...any) int64 {
			t.Helper()
			var n int64
			if err := d.Pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
			return n
		}
		tgConn := id(`INSERT INTO connections (org_id, public_id, type, name, telegram_bot_api_base_url,
			telegram_update_mode, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit,
			limiter_per_seconds, created_at, updated_at)
			VALUES ($1, 'CN0000000000T1', 'telegram', 'tg', 'http://tg', 'long_polling', '\x02', 'k1', $2, 1, 1, $2,
				$2) RETURNING id`, org.ID, t0)
		mmConn := id(`INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at) VALUES ($1, 'CN0000000000M1', 'mattermost', 'mm', 'http://mm', '\x01', 'k1', $2, 1,
			1, $2, $2) RETURNING id`, org.ID, t0)
		gateway := id(`INSERT INTO connections (org_id, public_id, type, name, bridge_base_url, bridge_event_delivery,
			bridge_info, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit,
			limiter_per_seconds, created_at, updated_at)
			VALUES ($1, 'CN0000000000G1', 'bridge', 'gw', 'http://gw', 'poll', '{"roles": ["gateway"]}', '\x02', 'k1',
				$2, 1, 1, $2, $2) RETURNING id`, org.ID, t0)
		telegram := func(publicID, name string) int64 {
			return id(`INSERT INTO destinations (org_id, public_id, type, name, connection_id, telegram_channel_id,
				mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at)
				VALUES ($1, $2, 'telegram', $3, $4, '@muster_alerts', '{}', 1, 1, 'healthy', $5, $5) RETURNING id`,
				org.ID, publicID, name, tgConn, t0)
		}
		tg, other := telegram("DS0000000000T1", "tg"), telegram("DS0000000000T2", "other")

		people := map[string]int64{}
		for i, p := range []struct{ login, role string }{{"ann", "responder"}, {"bob", "responder"},
			{"cat", "responder"}, {"dan", "responder"}, {"eve", "viewer"}, {"fay", "responder"},
			{"gus", "responder"}, {"hal", "responder"}, {"ivy", "responder"}, {"jay", "responder"},
			{"kim", "responder"}, {"lou", "responder"}} {
			status := "active"
			if p.login == "lou" {
				status = "disabled"
			}
			people[p.login] = id(`INSERT INTO users (org_id, public_id, login, name, role, source, status, created_at,
				updated_at) VALUES ($1, $2, $3, $3, $4, 'local', $5, $6, $6) RETURNING id`, org.ID,
				fmt.Sprintf("SR%012d", i+1), p.login, p.role, status, t0)
			if p.login == "dan" {
				exec(`INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
					username, created_at) VALUES ($1, $2, $3, 'mattermost', $4, 'u-dan', 'dan', $5)`, org.ID,
					fmt.Sprintf("AK%012d", i+1), people[p.login], mmConn, t0)
				continue
			}
			exec(`INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
				username, created_at) VALUES ($1, $2, $3, 'telegram', $4, $5, $6, $7)`, org.ID,
				fmt.Sprintf("AK%012d", i+1), people[p.login], tgConn, strconv.Itoa(9000+i), p.login, t0)
		}
		team := func(publicID, slug string) int64 {
			return id(`INSERT INTO teams (org_id, public_id, name, slug, created_at, updated_at)
				VALUES ($1, $2, $3, $3, $4, $4) RETURNING id`, org.ID, publicID, slug, t0)
		}
		payments := team("TM0000000000P1", "payments")
		n := 0
		schedule := func(name string, logins ...string) int64 {
			n++
			sc := id(`INSERT INTO schedules (org_id, public_id, team_id, name, time_zone, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'UTC', $5, $5) RETURNING id`, org.ID, fmt.Sprintf("SC%012d", n), payments,
				name, t0)
			layer := id(`INSERT INTO rotation_layers (org_id, public_id, schedule_id, position, name, shift_length,
				handover_time, start_date) VALUES ($1, $2, $3, 1, 'Days', 'day', '00:00', '2026-11-02') RETURNING id`,
				org.ID, fmt.Sprintf("RY%012d", n), sc)
			for i, login := range logins {
				exec(`INSERT INTO rotation_layer_users (org_id, rotation_layer_id, position, user_id)
					VALUES ($1, $2, $3, $4)`, org.ID, layer, i+1, people[login])
			}
			return sc
		}
		override := func(publicID string, sc int64, login string, end time.Time) {
			exec(`INSERT INTO schedule_overrides (org_id, public_id, schedule_id, user_id, starts_at, ends_at,
				created_by_user_id, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $4, $5, $5)`, org.ID,
				publicID, sc, people[login], end.Add(-time.Hour), end)
		}
		chain := func(publicID, kind string, sc int64) int64 {
			c := id(`INSERT INTO escalation_chains (org_id, public_id, name, created_at, updated_at)
				VALUES ($1, $2, $2, $3, $3) RETURNING id`, org.ID, publicID, t0)
			switch kind {
			case "call":
				exec(`INSERT INTO escalation_steps (org_id, chain_id, position, kind, call_target,
					gateway_connection_id, schedule_id) VALUES ($1, $2, 1, 'call', 'on_call', $3, $4)`, org.ID, c,
					gateway, sc)
			case "page_people":
				exec(`INSERT INTO escalation_steps (org_id, chain_id, position, kind, importance)
					VALUES ($1, $2, 1, 'page_people', 'normal')`, org.ID, c)
			default:
				exec(`INSERT INTO escalation_steps (org_id, chain_id, position, kind, importance, schedule_id)
					VALUES ($1, $2, 1, 'page_on_call', 'normal', $3)`, org.ID, c, sc)
			}
			return c
		}
		route := func(publicID string, chain, dest int64, deleted bool) {
			r := id(`INSERT INTO routes (org_id, public_id, name, position, urgent, group_key, reopen_window_seconds,
				grace_period_seconds, urgent_rise_removes_ack, snooze_durations_seconds, thread_batching_window_seconds,
				storm_threshold, language, ack_timeout_enabled, ack_timeout_first_interval_seconds, reminders_enabled,
				reminders_first_interval_seconds, reminders_cap_seconds, auto_unacknowledge, created_at, updated_at)
				VALUES ($1, $2, $2, 1, false, '{alertname}', 0, 0, true, '{}', 0, 10, 'en', false, 60, false, 60, 60,
					false, $3, $3) RETURNING id`, org.ID, publicID, t0)
			exec(`INSERT INTO route_escalation_chains (org_id, route_id, chain_id, added_at) VALUES ($1, $2, $3, $4)`,
				org.ID, r, chain, t0)
			exec(`INSERT INTO route_destinations (org_id, route_id, destination_id, added_at) VALUES ($1, $2, $3, $4)`,
				org.ID, r, dest, t0)
			if deleted {
				exec(`UPDATE routes SET deleted_at = $1 WHERE id = $2`, t0, r)
			}
		}

		s1 := schedule("Main", "ann", "dan", "eve", "lou")
		override("VR0000000000B1", s1, "bob", t0.Add(time.Hour))
		override("VR0000000000C1", s1, "cat", t0)
		route("RT0000000000N1", chain("EC0000000000D1", "page_on_call", s1), tg, false)
		teamChain := func(publicID, slug string, chain int64, deleted bool) {
			tm := team(publicID, slug)
			exec(`INSERT INTO team_escalation_chains (org_id, team_id, chain_id, added_at) VALUES ($1, $2, $3, $4)`,
				org.ID, tm, chain, t0)
			exec(`INSERT INTO team_destinations (org_id, team_id, destination_id, added_at) VALUES ($1, $2, $3, $4)`,
				org.ID, tm, tg, t0)
			if deleted {
				exec(`UPDATE teams SET deleted_at = $1 WHERE id = $2`, t0, tm)
			}
		}
		teamChain("TM0000000000P2", "ops", chain("EC0000000000P2", "page_on_call", schedule("Ops", "gus")), false)
		teamChain("TM0000000000J1", "calls", chain("EC0000000000J1", "call", schedule("Jay", "jay")), false)
		teamChain("TM0000000000K1", "gone", chain("EC0000000000K1", "page_on_call", schedule("Kim", "kim")), true)
		route("RT0000000000F1", chain("EC0000000000F1", "page_on_call", schedule("Fay", "fay")), other, false)
		route("RT0000000000H1", chain("EC0000000000H1", "page_on_call", schedule("Hal", "hal")), tg, true)
		override("VR0000000000J1", schedule("Ivy", "ivy"), "ivy", t0.Add(time.Hour))
		route("RT0000000000V1", chain("EC0000000000V1", "page_people", 0), tg, false)

		rows, err := dbgen.New(d.Pool).ListChainPagedTelegramPeople(ctx, dbgen.ListChainPagedTelegramPeopleParams{
			OrgID: org.ID, DestinationID: tg, Now: t0})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r.Login+"/"+r.ExternalID)
		}
		if !slices.Equal(got, []string{"ann/9000", "bob/9001", "gus/9006"}) {
			t.Errorf("the people of the chains of tg = %v", got)
		}
		rows, err = dbgen.New(d.Pool).ListChainPagedTelegramPeople(ctx, dbgen.ListChainPagedTelegramPeopleParams{
			OrgID: org.ID, DestinationID: tg, Now: t0.Add(2 * time.Hour)})
		if err != nil || len(rows) != 2 || rows[0].Login != "ann" || rows[1].Login != "gus" {
			t.Errorf("after bob's Override ended = %+v, %v", rows, err)
		}
	})
}
