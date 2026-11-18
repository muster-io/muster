// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package schedules_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/schedules"
)

// reachVector is the part of a vector of internal/routing/testdata/reach that names the Destination entries of the
// Route and of each Team.
type reachVector struct {
	Name        string                     `json:"name"`
	Route       []routing.Entry            `json:"route"`
	TeamEntries map[string][]routing.Entry `json:"team_entries"`
}

func loadReachVectors(t *testing.T) []reachVector {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "routing", "testdata", "reach", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors: %v", err)
	}
	var out []reachVector
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var vs []reachVector
		if err := json.Unmarshal(raw, &vs); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for i := range vs {
			vs[i].Name = filepath.Base(f) + ": " + vs[i].Name
		}
		out = append(out, vs...)
	}
	return out
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.d.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) id(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := e.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

// webhook inserts an outgoing webhook Destination in template mode named name with the template config tmpl.
func (e *env) webhook(t *testing.T, publicID, name, tmpl string) int64 {
	t.Helper()
	return e.id(t, `INSERT INTO destinations (org_id, public_id, type, name, webhook_mode, webhook_template_config,
		signing_secret_ciphertext, signing_secret_key_id, signing_secret_updated_at, mentions, limiter_limit,
		limiter_per_seconds, health, created_at, updated_at)
		VALUES ($1, $2, 'webhook', $3, 'template', $4, '\x00', 'k1', $5, '{}', 1, 1, 'healthy', $5, $5) RETURNING id`,
		e.orgID, publicID, name, tmpl, t0)
}

// name sets the Destinations of the Team payments to entries, mapping each destination_id of a vector to a Destination.
func (e *env) name(t *testing.T, dests map[int64]int64, entries []routing.Entry) {
	t.Helper()
	e.exec(t, `DELETE FROM team_destinations`)
	for _, en := range entries {
		id, ok := dests[en.DestinationID]
		if !ok {
			id = e.webhook(t, "DS"+strconv.FormatInt(1000000000000+en.DestinationID, 10)[1:], "d"+
				strconv.FormatInt(en.DestinationID, 10), `{}`)
			dests[en.DestinationID] = id
		}
		e.exec(t, `INSERT INTO team_destinations (org_id, team_id, destination_id, severity_condition, added_at)
			SELECT $1, id, $2, $3, $4 FROM teams WHERE public_id = $5`, e.orgID, id, string(en.Condition), t0,
			e.team.PublicID)
	}
}

// dayInput is a Schedule of payments in UTC with day Shifts of the people from 2 November, Monday.
func (e *env) dayInput(people ...string) schedules.Input {
	ids := make([]string, len(people))
	for i, p := range people {
		ids[i] = e.people[p]
	}
	return schedules.Input{TeamID: e.team.PublicID, Name: "Days", TimeZone: ptr("UTC"),
		RotationLayers: []schedules.RotationLayerInput{{Name: "Days", UserIDs: ids,
			ShiftLength: schedules.ShiftDay, HandoverTime: "00:00", StartDate: time.Date(2026, 11, 2, 0, 0, 0, 0,
				time.UTC)}}}
}

// unreachable are the Destinations named in the warnings unreachable of the first segment of the Schedule's
// Result, with their reasons.
func unreachable(res schedules.Result) map[string]string {
	out := map[string]string{}
	for _, w := range res.Warnings {
		if w.Kind == schedules.WarningUnreachable && w.Start.Equal(res.Segments[0].Start) {
			out[w.Unreachable.Destination.Name] = w.Unreachable.Reason
		}
	}
	return out
}

// TestIntegrationReachVectors is C-27.FR-8 against the reach rule of C-26.FR-1 (plan review, M4): for each Team of
// each vector of internal/routing/testdata/reach, with its entries as the Destinations of payments — outgoing
// webhooks in template mode without "reply in thread", which warn whoever is On-call — the Destinations a warning
// unreachable names are those that routing.Reach finds for the Team at some Severity level, conditions aside, since
// an On-call is warned about every Destination of their Team.
func TestIntegrationReachVectors(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.clock.Set(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
		sc, err := e.svc.Create(ctx, admin, e.dayInput("petr"))
		if err != nil {
			t.Fatal(err)
		}
		dests := map[int64]int64{}
		checked := 0
		for _, v := range loadReachVectors(t) {
			for _, key := range slices.Sorted(maps.Keys(v.TeamEntries)) {
				entries := v.TeamEntries[key]
				e.name(t, dests, entries)
				team, _ := strconv.ParseInt(key, 10, 64)
				want := map[string]string{}
				for _, level := range []string{"info", "warning", "critical"} {
					for _, r := range routing.Reach(level, nil, true, []routing.TeamEntries{{TeamID: team,
						Entries: entries}}) {
						want["d"+strconv.FormatInt(r.DestinationID, 10)] = schedules.ReachWebhookNoThreadReply
					}
				}
				res, err := e.svc.OnCall(ctx, sc.PublicID, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				if got := unreachable(res); !maps.Equal(got, want) {
					t.Errorf("%s, Team %s: unreachable %v, want %v", v.Name, key, got, want)
				}
				checked++
			}
		}
		if checked == 0 {
			t.Fatal("no Team in the vectors")
		}
	})
}

// TestIntegrationReachReads is C-27.FR-8 on PostgreSQL, each reason from what the queries read: petr, without a
// Mattermost Account link and with a Telegram one, whom the check of tg-payments saw outside its discussion group, is
// unreachable in payments-notice (no_account_link), in tg-payments (not_in_discussion_group), in the Bridge
// Destinations whose info declares no native_mentions or was never read (bridge_no_native_mentions) and in the
// outgoing webhook without "reply in thread" (webhook_no_thread_reply); the webhook with it, the Bridge whose info
// declares native_mentions with petr's link, and a deleted Destination warn of nothing. olga, linked in every space and not
// absent, is unreachable only where the reason does not depend on her.
func TestIntegrationReachReads(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.clock.Set(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
		mm := e.id(t, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at)
			VALUES ($1, 'CNAAAAAAAAAAM1', 'mattermost', 'mm', 'http://mm', '\x02', 'k1', $2, 1, 1, $2, $2)
			RETURNING id`, e.orgID, t0)
		tg := e.id(t, `INSERT INTO connections (org_id, public_id, type, name, telegram_bot_api_base_url,
			telegram_update_mode, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit,
			limiter_per_seconds, created_at, updated_at)
			VALUES ($1, 'CNAAAAAAAAAAT1', 'telegram', 'tg', 'http://tg', 'long_polling', '\x02', 'k1', $2, 1, 1, $2,
				$2) RETURNING id`, e.orgID, t0)
		bridge := func(publicID, info string) int64 {
			return e.id(t, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url,
				bridge_event_delivery, bridge_info, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at,
				limiter_limit, limiter_per_seconds, created_at, updated_at)
				VALUES ($1, $2, 'bridge', $2, 'http://br', 'poll', $3::jsonb, '\x02', 'k1', $4, 1, 1, $4, $4)
				RETURNING id`, e.orgID, publicID, info, t0)
		}
		plain := bridge("CNAAAAAAAAAAB1", `{"capabilities": {"native_mentions": false}}`)
		native := bridge("CNAAAAAAAAAAB2", `{"capabilities": {"native_mentions": true}}`)
		unread := e.id(t, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url,
			bridge_event_delivery, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit,
			limiter_per_seconds, created_at, updated_at)
			VALUES ($1, 'CNAAAAAAAAAAB3', 'bridge', 'unread', 'http://br', 'poll', '\x02', 'k1', $2, 1, 1, $2, $2)
			RETURNING id`, e.orgID, t0)
		var named []int64
		named = append(named, e.id(t, `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
			mattermost_team_id, mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health,
			created_at, updated_at)
			VALUES ($1, 'DSAAAAAAAAAAM1', 'mattermost', 'payments-notice', $2, 'team', 'chan', '{}', 1, 1, 'healthy',
				$3, $3) RETURNING id`, e.orgID, mm, t0))
		named = append(named, e.id(t, `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
			telegram_channel_id, telegram_absent_user_ids, telegram_members_checked_at, mentions, limiter_limit,
			limiter_per_seconds, health, created_at, updated_at)
			SELECT $1, 'DSAAAAAAAAAAT1', 'telegram', 'tg-payments', $2, '@muster_alerts', ARRAY[u.id], $3, '{}', 1, 1,
				'healthy', $3, $3 FROM users u WHERE u.public_id = $4 RETURNING id`, e.orgID, tg, t0,
			e.people["petr"]))
		for i, c := range []int64{plain, native, unread} {
			named = append(named, e.id(t, `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
				bridge_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at)
				VALUES ($1, $2, 'bridge', $3, $4, 'ch', '{}', 1, 1, 'healthy', $5, $5) RETURNING id`, e.orgID,
				[]string{"DSAAAAAAAAAAB1", "DSAAAAAAAAAAB2", "DSAAAAAAAAAAB3"}[i],
				[]string{"bridge-plain", "bridge-native", "bridge-unread"}[i], c, t0))
		}
		named = append(named, e.webhook(t, "DSAAAAAAAAAAW1", "hook-template", `{}`),
			e.webhook(t, "DSAAAAAAAAAAW2", "hook-reply",
				`{"reply_in_thread": {"method": "POST", "url": "https://example.org/reply"}}`),
			e.webhook(t, "DSAAAAAAAAAAW3", "hook-deleted", `{}`))
		e.exec(t, `UPDATE destinations SET deleted_at = $1 WHERE public_id = 'DSAAAAAAAAAAW3'`, t0)
		for _, id := range named {
			e.exec(t, `INSERT INTO team_destinations (org_id, team_id, destination_id, severity_condition, added_at)
				SELECT $1, id, $2, 'critical', $3 FROM teams WHERE public_id = $4`, e.orgID, id, t0, e.team.PublicID)
		}
		link := func(login, messenger string, conn int64, external string) {
			e.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
				username, created_at)
				SELECT $1, $2, id, $3, $4, $5, $5, $6 FROM users WHERE public_id = $7`, e.orgID,
				"AK0000000"+external, messenger, conn, external, t0, e.people[login])
		}
		link("petr", "telegram", tg, "00001")
		link("petr", "bridge", native, "00002")
		link("olga", "mattermost", mm, "00003")
		link("olga", "telegram", tg, "00004")
		link("olga", "bridge", native, "00005")

		sc, err := e.svc.Create(ctx, admin, e.dayInput("petr", "olga"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.OnCall(ctx, sc.PublicID, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"payments-notice": schedules.ReachNoAccountLink,
			"tg-payments":   schedules.ReachNotInDiscussionGroup,
			"bridge-plain":  schedules.ReachBridgeNoNativeMentions,
			"bridge-unread": schedules.ReachBridgeNoNativeMentions,
			"hook-template": schedules.ReachWebhookNoThreadReply}
		if got := unreachable(res); res.Segments[0].User == nil || res.Segments[0].User.Login != "petr" ||
			!maps.Equal(got, want) {
			t.Errorf("petr: unreachable %v, want %v", got, want)
		}
		var names []string
		for _, w := range res.Warnings {
			if w.Kind == schedules.WarningUnreachable && w.Start.Equal(res.Segments[0].Start) {
				names = append(names, w.Unreachable.Destination.Name)
			}
		}
		if !slices.Equal(names, []string{"bridge-plain", "bridge-unread", "hook-template", "payments-notice",
			"tg-payments"}) {
			t.Errorf("the Destinations by name = %v", names)
		}
		e.clock.Set(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
		list, err := e.svc.OnCallNow(ctx, 0)
		if err != nil || len(list) != 1 || list[0].Current.User == nil || list[0].Current.User.Login != "olga" {
			t.Fatalf("on call now = %+v, %v", list, err)
		}
		got := map[string]string{}
		for _, u := range list[0].Unreachable {
			got[u.Destination.Name] = u.Messenger + "/" + u.Reason
		}
		if !maps.Equal(got, map[string]string{"bridge-plain": "bridge/" + schedules.ReachBridgeNoNativeMentions,
			"bridge-unread": "bridge/" + schedules.ReachBridgeNoNativeMentions,
			"hook-template": "webhook/" + schedules.ReachWebhookNoThreadReply}) {
			t.Errorf("olga now: %v", got)
		}
	})
}

// TestIntegrationGatewayReaches is C-27.FR-8 on PostgreSQL: of petr, with a confirmed contact at a gateway that is
// Broken, olga, whose contact there was sent a code and not confirmed, and sergey, with a confirmed contact at a bridge
// that is only a messenger and at one that was deleted, and vic, without any, only petr's warnings say that a call
// reaches him; the state of the gateway is not read.
func TestIntegrationGatewayReaches(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.clock.Set(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
		bridge := func(publicID, info string, deleted bool) int64 {
			id := e.id(t, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url,
				bridge_event_delivery, bridge_info, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at,
				limiter_limit, limiter_per_seconds, gateway_broken_since, gateway_broken_reason,
				gateway_next_probe_at, created_at, updated_at)
				VALUES ($1, $2, 'bridge', $2, 'http://br', 'poll', $3::jsonb, '\x02', 'k1', $4, 1, 1, $4, 'refused',
				$4, $4, $4) RETURNING id`, e.orgID, publicID, info, t0)
			if deleted {
				e.exec(t, `UPDATE connections SET deleted_at = $1 WHERE id = $2`, t0, id)
			}
			return id
		}
		gateway := bridge("CNAAAAAAAAAAG1", `{"roles": ["gateway"]}`, false)
		messenger := bridge("CNAAAAAAAAAAG2", `{"roles": ["messenger"]}`, false)
		gone := bridge("CNAAAAAAAAAAG3", `{"roles": ["gateway"]}`, true)
		contact := func(login string, conn int64, n string, confirmed bool) {
			var at any
			if confirmed {
				at = t0
			}
			e.exec(t, `INSERT INTO gateway_contacts (org_id, public_id, user_id, connection_id, contact, confirmed_at,
				created_at)
				SELECT $1, $2, id, $3, $4, $5, $6 FROM users WHERE public_id = $7`, e.orgID, "GC00000000"+n+"AA",
				conn, "+1000000000"+n, at, t0, e.people[login])
		}
		contact("petr", gateway, "01", true)
		contact("olga", gateway, "02", false)
		contact("sergey", messenger, "03", true)
		contact("sergey", gone, "04", true)
		e.webhook(t, "DSAAAAAAAAAAW1", "hook-template", `{}`)
		e.exec(t, `INSERT INTO team_destinations (org_id, team_id, destination_id, severity_condition, added_at)
			SELECT $1, t.id, d.id, 'critical', $2 FROM teams t, destinations d WHERE t.public_id = $3
			AND d.public_id = 'DSAAAAAAAAAAW1'`, e.orgID, t0, e.team.PublicID)
		sc, err := e.svc.Create(ctx, admin, e.dayInput("petr", "olga", "sergey"))
		if err != nil {
			t.Fatal(err)
		}
		to := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
		res, err := e.svc.OnCall(ctx, sc.PublicID, nil, &to)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, w := range res.Warnings {
			if w.Kind == schedules.WarningUnreachable {
				got[w.User.Login] = w.Unreachable.GatewayReaches
			}
		}
		want := map[string]bool{"petr": true, "olga": false, "sergey": false}
		if !maps.Equal(got, want) {
			t.Errorf("gateway_reaches by person = %v, want %v", got, want)
		}
	})
}

// route inserts a Route named name and returns its id.
func (e *env) route(t *testing.T, publicID, name string) int64 {
	t.Helper()
	return e.id(t, `INSERT INTO routes (org_id, public_id, name, position, urgent, group_key, reopen_window_seconds,
		grace_period_seconds, urgent_rise_removes_ack, snooze_durations_seconds, thread_batching_window_seconds,
		storm_threshold, language, ack_timeout_enabled, ack_timeout_first_interval_seconds, reminders_enabled,
		reminders_first_interval_seconds, reminders_cap_seconds, auto_unacknowledge, created_at, updated_at)
		VALUES ($1, $2, $3, 1, false, '{alertname}', 0, 0, true, '{}', 0, 10, 'en', false, 60, false, 60, 60, false,
			$4, $4) RETURNING id`, e.orgID, publicID, name, t0)
}

// chain inserts an Escalation chain named name with one step of the kind on the Schedule schedule — page_on_call, or
// a call of the On-call through the Notification gateway gateway — and returns its id.
func (e *env) chain(t *testing.T, publicID, name, kind string, schedule, gateway int64) int64 {
	t.Helper()
	id := e.id(t, `INSERT INTO escalation_chains (org_id, public_id, name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4) RETURNING id`, e.orgID, publicID, name, t0)
	switch kind {
	case "call":
		e.exec(t, `INSERT INTO escalation_steps (org_id, chain_id, position, kind, call_target, gateway_connection_id,
			schedule_id) VALUES ($1, $2, 1, 'call', 'on_call', $3, $4)`, e.orgID, id, gateway, schedule)
	case "page_people":
		e.exec(t, `INSERT INTO escalation_steps (org_id, chain_id, position, kind, importance)
			VALUES ($1, $2, 1, 'page_people', 'normal')`, e.orgID, id)
	default:
		e.exec(t, `INSERT INTO escalation_steps (org_id, chain_id, position, kind, importance, schedule_id)
			VALUES ($1, $2, 1, 'page_on_call', 'normal', $3)`, e.orgID, id, schedule)
	}
	return id
}

// newTeam inserts a Team with the slug and returns its id.
func (e *env) newTeam(t *testing.T, publicID, slug string) int64 {
	t.Helper()
	return e.id(t, `INSERT INTO teams (org_id, public_id, name, slug, created_at, updated_at)
		VALUES ($1, $2, $3, $3, $4, $4) RETURNING id`, e.orgID, publicID, slug, t0)
}

// warned counts the warnings unreachable of the first segment of the Schedule's Result by Destination.
func warned(res schedules.Result) map[string]int {
	out := map[string]int{}
	for _, w := range res.Warnings {
		if w.Kind == schedules.WarningUnreachable && w.Start.Equal(res.Segments[0].Start) {
			out[w.Unreachable.Destination.Name]++
		}
	}
	return out
}

// TestIntegrationChainReachVectors is C-27.FR-8 for the chains that Page a Schedule against the reach rule of C-26.FR-1
// (plan review, M4): for each vector of internal/routing/testdata/reach, with its entries as the Destinations of a
// Route and of the Team ops — outgoing webhooks in template mode without "reply in thread", which warn whoever is
// On-call — and a chain with a step that Pages the Schedule of payments, which has no Destination of its own, named
// by the Route, by ops, or by both, the Destinations a warning unreachable names are those that routing.Reach finds
// for those sources at some Severity level, conditions aside, each once: the Destinations ListChainReach finds are
// those of the sources the vectors expect.
func TestIntegrationChainReachVectors(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.clock.Set(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
		sc, err := e.svc.Create(ctx, admin, e.dayInput("petr"))
		if err != nil {
			t.Fatal(err)
		}
		route := e.route(t, "RT0000000000V1", "vectors")
		ops := e.newTeam(t, "TM0000000000V1", "ops")
		ch := e.chain(t, "EC0000000000V1", "vectors", "page_on_call", sc.ID, 0)
		dests := map[int64]int64{}
		dest := func(id int64) int64 {
			if d, ok := dests[id]; ok {
				return d
			}
			dests[id] = e.webhook(t, "DS"+strconv.FormatInt(1000000000000+id, 10)[1:], "d"+strconv.FormatInt(id, 10),
				`{}`)
			return dests[id]
		}
		reached := func(want map[string]int, route []routing.Entry, team []routing.Entry) {
			for _, level := range []string{"info", "warning", "critical"} {
				var teams []routing.TeamEntries
				if team != nil {
					teams = []routing.TeamEntries{{TeamID: ops, Entries: team}}
				}
				for _, r := range routing.Reach(level, route, true, teams) {
					want["d"+strconv.FormatInt(r.DestinationID, 10)] = 1
				}
			}
		}
		type source struct {
			name        string
			route, team bool
			key         string
		}
		checked := 0
		for _, v := range loadReachVectors(t) {
			cases := []source{{name: "the Route", route: true}}
			for _, key := range slices.Sorted(maps.Keys(v.TeamEntries)) {
				cases = append(cases, source{"the Team " + key, false, true, key},
					source{"the Route and the Team " + key, true, true, key})
			}
			for _, c := range cases {
				e.exec(t, `DELETE FROM route_escalation_chains`)
				e.exec(t, `DELETE FROM team_escalation_chains`)
				e.exec(t, `DELETE FROM route_destinations`)
				e.exec(t, `DELETE FROM team_destinations`)
				for _, en := range v.Route {
					e.exec(t, `INSERT INTO route_destinations (org_id, route_id, destination_id, severity_condition,
						added_at) VALUES ($1, $2, $3, $4, $5)`, e.orgID, route, dest(en.DestinationID),
						string(en.Condition), t0)
				}
				for _, en := range v.TeamEntries[c.key] {
					e.exec(t, `INSERT INTO team_destinations (org_id, team_id, destination_id, severity_condition,
						added_at) VALUES ($1, $2, $3, $4, $5)`, e.orgID, ops, dest(en.DestinationID),
						string(en.Condition), t0)
				}
				want := map[string]int{}
				if c.route {
					e.exec(t, `INSERT INTO route_escalation_chains (org_id, route_id, chain_id, added_at)
						VALUES ($1, $2, $3, $4)`, e.orgID, route, ch, t0)
					reached(want, v.Route, nil)
				}
				if c.team {
					e.exec(t, `INSERT INTO team_escalation_chains (org_id, team_id, chain_id, added_at)
						VALUES ($1, $2, $3, $4)`, e.orgID, ops, ch, t0)
					reached(want, nil, v.TeamEntries[c.key])
				}
				res, err := e.svc.OnCall(ctx, sc.PublicID, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				if got := warned(res); !maps.Equal(got, want) {
					t.Errorf("%s, a chain of %s: unreachable %v, want %v", v.Name, c.name, got, want)
				}
				checked++
			}
		}
		if checked == 0 {
			t.Fatal("no vector")
		}
	})
}

// TestIntegrationChainReachReads is C-27.FR-8 for the chains that Page a Schedule on PostgreSQL (C-32.FR-8): petr,
// On-call in the Schedule of payments, without a Mattermost Account link and with a Telegram one, whom the check of
// tg-ops saw outside its discussion group, is warned about infra-notice (no_account_link), a Mattermost Destination of
// the Route "Infrastructure" whose chain devops Pages the On-call of the Schedule, about tg-ops
// (not_in_discussion_group), the Telegram Destination of the Team ops whose chain Pages it too, and about
// payments-notice (no_account_link), which payments and the Route both name, once; not about the Destinations of a
// chain whose step Pages people, of a Route or a Team that is deleted, of a chain that Pages another Schedule, of a
// Team's or a Route's chain that only calls the On-call through a Notification gateway, which writes nothing in a
// Thread (C-32.FR-9), nor a deleted Destination of the chain. "On call now" marks the same.
func TestIntegrationChainReachReads(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.clock.Set(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
		mm := e.id(t, `INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at)
			VALUES ($1, 'CNAAAAAAAAAAM1', 'mattermost', 'mm', 'http://mm', '\x02', 'k1', $2, 1, 1, $2, $2)
			RETURNING id`, e.orgID, t0)
		tg := e.id(t, `INSERT INTO connections (org_id, public_id, type, name, telegram_bot_api_base_url,
			telegram_update_mode, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit,
			limiter_per_seconds, created_at, updated_at)
			VALUES ($1, 'CNAAAAAAAAAAT1', 'telegram', 'tg', 'http://tg', 'long_polling', '\x02', 'k1', $2, 1, 1, $2,
				$2) RETURNING id`, e.orgID, t0)
		gw := e.id(t, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url, bridge_event_delivery,
			bridge_info, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit,
			limiter_per_seconds, created_at, updated_at)
			VALUES ($1, 'CNAAAAAAAAAAG1', 'bridge', 'gw', 'http://gw', 'poll', '{"roles": ["gateway"]}', '\x02', 'k1',
				$2, 1, 1, $2, $2) RETURNING id`, e.orgID, t0)
		mattermost := func(publicID, name string) int64 {
			return e.id(t, `INSERT INTO destinations (org_id, public_id, type, name, connection_id, mattermost_team_id,
				mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at)
				VALUES ($1, $2, 'mattermost', $3, $4, 'team', 'chan', '{}', 1, 1, 'healthy', $5, $5) RETURNING id`,
				e.orgID, publicID, name, mm, t0)
		}
		infra, pn := mattermost("DSAAAAAAAAAAM1", "infra-notice"), mattermost("DSAAAAAAAAAAM2", "payments-notice")
		gone := mattermost("DSAAAAAAAAAAM3", "deleted")
		e.exec(t, `UPDATE destinations SET deleted_at = $1 WHERE id = $2`, t0, gone)
		other := []int64{mattermost("DSAAAAAAAAAAM4", "people-only"), mattermost("DSAAAAAAAAAAM5", "deleted-route"),
			mattermost("DSAAAAAAAAAAM6", "deleted-team"), mattermost("DSAAAAAAAAAAM7", "other-schedule"),
			mattermost("DSAAAAAAAAAAM8", "call-only"), mattermost("DSAAAAAAAAAAM9", "route-call-only")}
		tgOps := e.id(t, `INSERT INTO destinations (org_id, public_id, type, name, connection_id, telegram_channel_id,
			telegram_absent_user_ids, telegram_members_checked_at, mentions, limiter_limit, limiter_per_seconds,
			health, created_at, updated_at)
			SELECT $1, 'DSAAAAAAAAAAT1', 'telegram', 'tg-ops', $2, '@muster_alerts', ARRAY[u.id], $3, '{}', 1, 1,
				'healthy', $3, $3 FROM users u WHERE u.public_id = $4 RETURNING id`, e.orgID, tg, t0,
			e.people["petr"])
		e.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
			username, created_at)
			SELECT $1, 'AK000000000001', id, 'telegram', $2, '1', '1', $3 FROM users WHERE public_id = $4`, e.orgID, tg,
			t0, e.people["petr"])
		e.exec(t, `INSERT INTO team_destinations (org_id, team_id, destination_id, added_at)
			SELECT $1, id, $2, $3 FROM teams WHERE public_id = $4`, e.orgID, pn, t0, e.team.PublicID)

		sc, err := e.svc.Create(ctx, admin, e.dayInput("petr"))
		if err != nil {
			t.Fatal(err)
		}
		in := e.dayInput("olga")
		in.Name = "Other"
		sc2, err := e.svc.Create(ctx, admin, in)
		if err != nil {
			t.Fatal(err)
		}
		infraRoute := e.route(t, "RT0000000000N1", "Infrastructure")
		devops := e.chain(t, "EC0000000000D1", "devops", "page_on_call", sc.ID, 0)
		e.exec(t, `INSERT INTO route_escalation_chains (org_id, route_id, chain_id, added_at) VALUES ($1, $2, $3, $4)`,
			e.orgID, infraRoute, devops, t0)
		for _, d := range []int64{infra, pn, gone} {
			e.exec(t, `INSERT INTO route_destinations (org_id, route_id, destination_id, added_at)
				VALUES ($1, $2, $3, $4)`, e.orgID, infraRoute, d, t0)
		}
		ops := e.newTeam(t, "TM0000000000P2", "ops")
		opsChain := e.chain(t, "EC0000000000C1", "ops", "page_on_call", sc.ID, 0)
		e.exec(t, `INSERT INTO team_escalation_chains (org_id, team_id, chain_id, added_at) VALUES ($1, $2, $3, $4)`,
			e.orgID, ops, opsChain, t0)
		e.exec(t, `INSERT INTO team_destinations (org_id, team_id, destination_id, added_at) VALUES ($1, $2, $3, $4)`,
			e.orgID, ops, tgOps, t0)
		// The Destinations that warn of nothing: of a chain that Pages people, of a deleted Route and a deleted Team
		// naming devops, of a chain that Pages the other Schedule, and of a Team's and a Route's chain that only calls
		// the On-call of the Schedule through a Notification gateway, which writes nothing in a Thread (C-32.FR-9).
		names := func(table, col string, owner, chain, dest int64) {
			e.exec(t, `INSERT INTO `+table+`_escalation_chains (org_id, `+col+`, chain_id, added_at)
				VALUES ($1, $2, $3, $4)`, e.orgID, owner, chain, t0)
			e.exec(t, `INSERT INTO `+table+`_destinations (org_id, `+col+`, destination_id, added_at)
				VALUES ($1, $2, $3, $4)`, e.orgID, owner, dest, t0)
		}
		names("route", "route_id", e.route(t, "RT0000000000P1", "people"),
			e.chain(t, "EC0000000000P1", "people", "page_people", 0, 0), other[0])
		deletedRoute := e.route(t, "RT0000000000X1", "deleted")
		names("route", "route_id", deletedRoute, devops, other[1])
		e.exec(t, `UPDATE routes SET deleted_at = $1 WHERE id = $2`, t0, deletedRoute)
		deletedTeam := e.newTeam(t, "TM0000000000X1", "deleted")
		names("team", "team_id", deletedTeam, devops, other[2])
		e.exec(t, `UPDATE teams SET deleted_at = $1 WHERE id = $2`, t0, deletedTeam)
		names("team", "team_id", e.newTeam(t, "TM0000000000S1", "second"),
			e.chain(t, "EC0000000000S1", "second", "page_on_call", sc2.ID, 0), other[3])
		names("team", "team_id", e.newTeam(t, "TM0000000000K1", "calls"),
			e.chain(t, "EC0000000000K1", "calls", "call", sc.ID, gw), other[4])
		names("route", "route_id", e.route(t, "RT0000000000K1", "calls"),
			e.chain(t, "EC0000000000K2", "route-calls", "call", sc.ID, gw), other[5])

		res, err := e.svc.OnCall(ctx, sc.PublicID, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"infra-notice": schedules.ReachNoAccountLink,
			"payments-notice": schedules.ReachNoAccountLink, "tg-ops": schedules.ReachNotInDiscussionGroup}
		if got := unreachable(res); !maps.Equal(got, want) {
			t.Errorf("petr: unreachable %v, want %v", got, want)
		}
		if got := warned(res); !maps.Equal(got, map[string]int{"infra-notice": 1, "payments-notice": 1,
			"tg-ops": 1}) {
			t.Errorf("the warnings by Destination = %v", got)
		}
		list, err := e.svc.OnCallNow(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		var marks []string
		for _, it := range list {
			if it.Team.Slug == "payments" {
				for _, u := range it.Unreachable {
					marks = append(marks, u.Destination.Name+"/"+u.Messenger+"/"+u.Reason)
				}
			}
		}
		if !slices.Equal(marks, []string{"infra-notice/mattermost/no_account_link",
			"payments-notice/mattermost/no_account_link", "tg-ops/telegram/not_in_discussion_group"}) {
			t.Errorf("on call now = %v", marks)
		}
	})
}
