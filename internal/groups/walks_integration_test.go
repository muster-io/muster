// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package groups_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/groups/dbgen"
)

// TestIntegrationWalksTakeQueuedFinalEdits is C-12.FR-12 and C-26.FR-9 for the lists of the open Alert Group updates
// on PostgreSQL: besides the open Alert Groups of its scope, each walk lists the resolved ones in its scope whose final
// edit still waits — a pending delivery, in the Destination of a walk by Destination, in a Destination that follows
// the Organization's button set for that walk — and leaves out a resolved one whose final edit went out and a
// resolved one with a pending delivery outside its scope: another Destination, Route, Team or person. Each walk lists
// by id after its cursor, a batch at a time, through both branches.
func TestIntegrationWalksTakeQueuedFinalEdits(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := e.d.Pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		id := func(sql string, args ...any) int64 {
			t.Helper()
			var out int64
			if err := e.d.Pool.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
			return out
		}
		r1, r2 := e.route(t, "one", "one", "alertname").ID, e.route(t, "two", "two", "alertname").ID
		conn := id(`INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at) VALUES ($1, 'CNAAAAAAAAAAA1', 'mattermost', 'bot', 'https://mm.example.org',
			'\x00', 'k1', $2, 1000, 1, $2, $2) RETURNING id`, e.orgID, t0)
		dest := func(publicID string, buttons any) int64 {
			t.Helper()
			return id(`INSERT INTO destinations (org_id, public_id, type, name, connection_id, mattermost_team_id,
				mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, buttons, created_at,
				updated_at) VALUES ($1, $2, 'mattermost', $2, $3, 'team', $2, '{}', 1000, 1, 'healthy', $4::jsonb, $5,
				$5) RETURNING id`, e.orgID, publicID, conn, buttons, t0)
		}
		// d1 follows the Organization's button set; d2 and d3 have their own.
		d1, d2 := dest("DSAAAAAAAAAAA1", nil), dest("DSAAAAAAAAAAA2", `["acknowledge"]`)
		d3 := dest("DSAAAAAAAAAAA3", `["resolve"]`)
		team := func(publicID, slug string) int64 {
			t.Helper()
			return id(`INSERT INTO teams (org_id, public_id, name, slug, own_working_hours, created_at, updated_at)
				VALUES ($1, $2, $3, $3, false, $4, $4) RETURNING id`, e.orgID, publicID, slug, t0)
		}
		t1, t2 := team("TM0000000000T1", "one"), team("TM0000000000T2", "two")
		u1 := e.userID(t)
		u2 := e.user(t, "SRAAAAAAAAAAB2", "other")
		sa := id(`INSERT INTO service_accounts (org_id, public_id, name, role, status, created_at, updated_at)
			VALUES ($1, 'SAAAAAAAAAAAF1', 'robot', 'responder', 'active', $2, $2) RETURNING id`, e.orgID, t0)

		// open fires; queued, resolved by u1, waits for its final edit in d1; sent, resolved by u1, had it sent;
		// other, resolved by u2 on Route two in Team two, waits for it in d2; robot, resolved by the Service account,
		// waits for it in d1.
		type group struct {
			route, team, dest int64
			status, state     string
		}
		fixtures := []struct {
			name string
			group
		}{
			{"open", group{r1, t1, d1, "firing", "delivered"}},
			{"queued", group{r1, t1, d1, "resolved", "pending"}},
			{"sent", group{r1, t1, d1, "resolved", "delivered"}},
			{"other", group{r2, t2, d2, "resolved", "pending"}},
			{"robot", group{r1, t1, d1, "resolved", "pending"}},
		}
		ids := map[string]int64{}
		for i, f := range fixtures {
			r := &row{number: int64(i + 1), route: f.route, ints: []int64{e.intID}, severity: "warning",
				status: f.status, created: t0, changed: t0, labels: map[string]string{"alertname": f.name},
				title: f.name}
			if f.status == "resolved" {
				r.resolved, r.by = &t0, groups.ResolvedByUser
			}
			e.insert(t, r)
			ids[f.name] = r.id
			exec(`INSERT INTO alert_group_teams (org_id, alert_group_id, team_id, rule, joined_at)
				VALUES ($1, $2, $3, 'label', $4)`, e.orgID, r.id, f.team, t0)
			exec(`INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, next_attempt_at, created_at,
				updated_at) VALUES ($1, $2, $3, $4, $5, $5, $5)`, e.orgID, f.dest, r.id, f.state, t0)
		}
		// other waits for its final edit in d3 too: a walk lists it once, and a batch that starts with it still fills
		// up with the Alert Groups after it.
		exec(`INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, next_attempt_at, created_at,
			updated_at) VALUES ($1, $2, $3, 'pending', $4, $4, $4)`, e.orgID, d3, ids["other"], t0)
		exec(`UPDATE alert_groups SET resolved_by_user_id = $2 WHERE id = $1`, ids["other"], u2)
		exec(`UPDATE alert_groups SET resolved_by_user_id = NULL, resolved_by_service_account_id = $2 WHERE id = $1`,
			ids["robot"], sa)

		q := dbgen.New(e.d.Pool)
		org := e.orgID
		user := func(id int64) pgtype.Int8 { return pgtype.Int8{Int64: id, Valid: true} }
		for _, c := range []struct {
			name string
			list func(ctx context.Context, after int64, lim int32) ([]int64, error)
			want []string
		}{
			{"destination d1", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsOfDestinationAfter(ctx, dbgen.ListOpenGroupsOfDestinationAfterParams{
					OrgID: org, DestinationID: d1, AfterID: after, Lim: lim})
			}, []string{"open", "queued", "robot"}},
			{"destination d2", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsOfDestinationAfter(ctx, dbgen.ListOpenGroupsOfDestinationAfterParams{
					OrgID: org, DestinationID: d2, AfterID: after, Lim: lim})
			}, []string{"other"}},
			{"destination d3", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsOfDestinationAfter(ctx, dbgen.ListOpenGroupsOfDestinationAfterParams{
					OrgID: org, DestinationID: d3, AfterID: after, Lim: lim})
			}, []string{"other"}},
			{"following the organization", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsFollowingOrganizationAfter(ctx,
					dbgen.ListOpenGroupsFollowingOrganizationAfterParams{OrgID: org, AfterID: after, Lim: lim})
			}, []string{"open", "queued", "robot"}},
			{"team one", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsOfTeamAfter(ctx, dbgen.ListOpenGroupsOfTeamAfterParams{OrgID: org, TeamID: t1,
					AfterID: after, Lim: lim})
			}, []string{"open", "queued", "robot"}},
			{"route one", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsOfRouteAfter(ctx, dbgen.ListOpenGroupsOfRouteAfterParams{OrgID: org,
					RouteID: r1, AfterID: after, Lim: lim})
			}, []string{"open", "queued", "robot"}},
			{"organization", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsAfter(ctx, dbgen.ListOpenGroupsAfterParams{OrgID: org, AfterID: after, Lim: lim})
			}, []string{"open", "queued", "other", "robot"}},
			{"user u1", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsOfUserAfter(ctx, dbgen.ListOpenGroupsOfUserAfterParams{OrgID: org,
					UserID: user(u1), AfterID: after, Lim: lim})
			}, []string{"queued"}},
			{"user u2", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsOfUserAfter(ctx, dbgen.ListOpenGroupsOfUserAfterParams{OrgID: org,
					UserID: user(u2), AfterID: after, Lim: lim})
			}, []string{"other"}},
			{"service account", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListOpenGroupsOfServiceAccountAfter(ctx, dbgen.ListOpenGroupsOfServiceAccountAfterParams{
					OrgID: org, ServiceAccountID: user(sa), AfterID: after, Lim: lim})
			}, []string{"robot"}},
			{"evaluated", func(ctx context.Context, after int64, lim int32) ([]int64, error) {
				return q.ListEvaluatedGroupsAfter(ctx, dbgen.ListEvaluatedGroupsAfterParams{OrgID: org,
					AfterID: after, Lim: lim})
			}, []string{"open", "queued", "other", "robot"}},
		} {
			var want []int64
			for _, n := range c.want {
				want = append(want, ids[n])
			}
			// In one batch, then two at a time after the cursor, as the worker walks.
			got, err := c.list(ctx, 0, 50)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: listed %v, want %v (%v)", c.name, got, want, ids)
			}
			var walked []int64
			for after, rounds := int64(0), 0; ; rounds++ {
				if rounds > len(fixtures) {
					t.Fatalf("%s: the walk did not end after %d batches: %v", c.name, rounds, walked)
				}
				batch, err := c.list(ctx, after, 2)
				if err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				walked = append(walked, batch...)
				if len(batch) < 2 {
					break
				}
				if batch[len(batch)-1] <= after {
					t.Fatalf("%s: the cursor did not advance past %d: %v", c.name, after, batch)
				}
				after = batch[len(batch)-1]
			}
			if !slices.Equal(walked, want) {
				t.Errorf("%s: walked %v, want %v", c.name, walked, want)
			}
		}
	})
}

// TestIntegrationEvaluatedWalkTakesGrace is C-25.FR-13 and #629 for the list of the evaluation of the Teams on
// PostgreSQL: behind a history of resolved Alert Groups, it lists by id the open ones, firing or acknowledged, those a
// person resolved that are still in their Grace period, and a resolved one whose final edit waits, and leaves out a
// resolved one outside its Grace period; the same rows in the same order in one batch and in batches of three after
// the cursor. In the plan of a batch, custom and generic, the branch of the open Alert Groups and those in their Grace
// period reads alert_groups_evaluated_idx, not the primary key from the cursor nor the whole table.
func TestIntegrationEvaluatedWalkTakesGrace(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := e.d.Pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		route := e.route(t, "one", "one", "alertname").ID
		// The history: 2,000 Alert Groups the system resolved, with lower ids than the fixtures.
		exec(`INSERT INTO alert_groups (org_id, public_id, number, route_id, group_key_labels, group_key_values,
			group_key_sha256, title, common_labels, common_annotations, integration_ids, status, severity_level, urgent,
			resolved_at, resolved_by_kind, resolve_reason, created_at, last_changed_at)
			SELECT $1, 'AG' || lpad(upper(to_hex(n)), 12, '0'), n, $2, '{alertname}', '{}',
			       sha256(convert_to('history' || n, 'UTF8')), 'history', '{}', '{}', $3, 'resolved', 'warning', false,
			       $4, 'system', 'gone', $4, $4
			FROM generate_series(1001, 3000) n`, e.orgID, route, []int64{e.intID}, t0)
		var conn int64
		if err := e.d.Pool.QueryRow(ctx, `INSERT INTO connections (org_id, public_id, type, name,
			mattermost_server_url, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit,
			limiter_per_seconds, created_at, updated_at) VALUES ($1, 'CNAAAAAAAAAAA1', 'mattermost', 'bot',
			'https://mm.example.org', '\x00', 'k1', $2, 1000, 1, $2, $2) RETURNING id`, e.orgID, t0).Scan(&conn); err != nil {
			t.Fatal(err)
		}
		var dest int64
		if err := e.d.Pool.QueryRow(ctx, `INSERT INTO destinations (org_id, public_id, type, name, connection_id,
			mattermost_team_id, mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health,
			created_at, updated_at) VALUES ($1, 'DSAAAAAAAAAAA1', 'mattermost', 'd1', $2, 'team', 'd1', '{}', 1000, 1,
			'healthy', $3, $3) RETURNING id`, e.orgID, conn, t0).Scan(&dest); err != nil {
			t.Fatal(err)
		}

		// open fires; gone was resolved by the system; grace and later were resolved by a person and are in their
		// Grace period; acked is acknowledged; over was resolved by a person after its Grace period ended; queued was
		// resolved by the system and waits for its final edit; last fires.
		fixtures := []struct {
			name, status, by, state string
			grace                   bool
		}{
			{"open", "firing", "", "delivered", false},
			{"gone", "resolved", groups.ResolvedBySystem, "delivered", false},
			{"grace", "resolved", groups.ResolvedByUser, "delivered", true},
			{"acked", "firing", "", "delivered", false},
			{"over", "resolved", groups.ResolvedByUser, "delivered", false},
			{"later", "resolved", groups.ResolvedByUser, "delivered", true},
			{"queued", "resolved", groups.ResolvedBySystem, "pending", false},
			{"last", "firing", "", "delivered", false},
		}
		ids := map[string]int64{}
		for i, f := range fixtures {
			r := &row{number: int64(i + 1), route: route, ints: []int64{e.intID}, severity: "warning",
				status: f.status, created: t0, changed: t0, labels: map[string]string{"alertname": f.name},
				title: f.name}
			if f.status == "resolved" {
				r.resolved, r.by, r.reason = &t0, f.by, "gone"
			}
			e.insert(t, r)
			ids[f.name] = r.id
			if f.grace {
				exec(`UPDATE alert_groups SET grace_deadline = $2 WHERE id = $1`, r.id, t0.Add(10*time.Minute))
			}
			exec(`INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, next_attempt_at, created_at,
				updated_at) VALUES ($1, $2, $3, $4, $5, $5, $5)`, e.orgID, dest, r.id, f.state, t0)
		}
		exec(`UPDATE alert_groups SET status = 'acknowledged', owner_user_id = $2, acknowledged_at = $3 WHERE id = $1`,
			ids["acked"], e.userID(t), t0)
		exec(`ANALYZE alert_groups`)
		exec(`ANALYZE deliveries`)

		q := dbgen.New(e.d.Pool)
		list := func(after int64, lim int32) []int64 {
			t.Helper()
			got, err := q.ListEvaluatedGroupsAfter(ctx, dbgen.ListEvaluatedGroupsAfterParams{OrgID: e.orgID,
				AfterID: after, Lim: lim})
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		var want []int64
		for _, n := range []string{"open", "grace", "acked", "later", "queued", "last"} {
			want = append(want, ids[n])
		}
		if got := list(0, 50); !slices.Equal(got, want) {
			t.Errorf("listed %v, want %v (%v)", got, want, ids)
		}
		var batches [][]int64
		for after := int64(0); len(batches) <= len(fixtures); {
			batch := list(after, 3)
			batches = append(batches, batch)
			if len(batch) < 3 {
				break
			}
			after = batch[len(batch)-1]
		}
		if len(batches) != 3 || !slices.Equal(slices.Concat(batches...), want) || len(batches[2]) != 0 {
			t.Errorf("walked %v three at a time, want %v in two full batches", batches, want)
		}

		// The plan of a batch from the cursor 0, as the session prepares it, custom and generic.
		c := &captured{}
		_, _ = dbgen.New(c).ListEvaluatedGroupsAfter(ctx, dbgen.ListEvaluatedGroupsAfterParams{OrgID: e.orgID,
			AfterID: 0, Lim: 50})
		pc, err := e.d.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Release()
		if _, err := pc.Exec(ctx, "PREPARE evaluated AS "+c.sql); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		defer func() {
			_, _ = pc.Exec(ctx, "DEALLOCATE evaluated")
			_, _ = pc.Exec(ctx, "RESET plan_cache_mode")
		}()
		for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
			if _, err := pc.Exec(ctx, "SET plan_cache_mode = "+mode); err != nil {
				t.Fatal(err)
			}
			rows, err := pc.Query(ctx, fmt.Sprintf("EXPLAIN EXECUTE evaluated(%d, 0, 50)", e.orgID))
			if err != nil {
				t.Fatalf("%s: explain: %v", mode, err)
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatalf("%s: explain: %v", mode, err)
			}
			plan := strings.Join(lines, "\n")
			if !strings.Contains(plan, "using alert_groups_evaluated_idx on alert_groups g ") {
				t.Errorf("%s: the open and Grace branch does not read alert_groups_evaluated_idx\n%s", mode, plan)
			}
		}
	})
}

// TestIntegrationWalksReadPendingDeliveries is #642 for the walks of a change of configuration on PostgreSQL: behind a
// history of resolved Alert Groups whose deliveries ended, the plan of a batch of each walk, custom and generic, reads
// the branch of the resolved Alert Groups whose final edit waits from deliveries_alert_group_pending_idx — the pending
// deliveries in the order of their Alert Group, from the cursor on — and looks up the Alert Group of each by its
// primary key alone; it reads neither deliveries_alert_group_key, every delivery ever made, nor the whole table, nor
// alert_groups_pkey from the cursor.
func TestIntegrationWalksReadPendingDeliveries(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := e.d.Pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		id := func(sql string, args ...any) int64 {
			t.Helper()
			var out int64
			if err := e.d.Pool.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
			return out
		}
		route := e.route(t, "one", "one", "alertname").ID
		conn := id(`INSERT INTO connections (org_id, public_id, type, name, mattermost_server_url,
			bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds,
			created_at, updated_at) VALUES ($1, 'CNAAAAAAAAAAA1', 'mattermost', 'bot', 'https://mm.example.org',
			'\x00', 'k1', $2, 1000, 1, $2, $2) RETURNING id`, e.orgID, t0)
		dest := id(`INSERT INTO destinations (org_id, public_id, type, name, connection_id, mattermost_team_id,
			mattermost_channel_id, mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at)
			VALUES ($1, 'DSAAAAAAAAAAA1', 'mattermost', 'd1', $2, 'team', 'd1', '{}', 1000, 1, 'healthy', $3, $3)
			RETURNING id`, e.orgID, conn, t0)
		team := id(`INSERT INTO teams (org_id, public_id, name, slug, own_working_hours, created_at, updated_at)
			VALUES ($1, 'TM0000000000T1', 'one', 'one', false, $2, $2) RETURNING id`, e.orgID, t0)
		user := e.userID(t)
		sa := id(`INSERT INTO service_accounts (org_id, public_id, name, role, status, created_at, updated_at)
			VALUES ($1, 'SAAAAAAAAAAAF1', 'robot', 'responder', 'active', $2, $2) RETURNING id`, e.orgID, t0)

		// The history: 3,000 Alert Groups the User resolved, in the Team, whose final edit went out; then two that
		// wait for it, one the User resolved and one the Service account resolved, and one that fires.
		exec(`INSERT INTO alert_groups (org_id, public_id, number, route_id, group_key_labels, group_key_values,
			group_key_sha256, title, common_labels, common_annotations, integration_ids, status, severity_level, urgent,
			resolved_at, resolved_by_kind, resolved_by_user_id, created_at, last_changed_at)
			SELECT $1, 'AG' || lpad(upper(to_hex(n)), 12, '0'), n, $2, '{alertname}', '{}',
			       sha256(convert_to('history' || n, 'UTF8')), 'history', '{}', '{}', $3, 'resolved', 'warning', false,
			       $4, 'user', $5, $4, $4
			FROM generate_series(1001, 4000) n`, e.orgID, route, []int64{e.intID}, t0, user)
		exec(`UPDATE alert_groups SET status = 'firing', resolved_at = NULL, resolved_by_kind = NULL,
			resolved_by_user_id = NULL WHERE org_id = $1 AND number = 4000`, e.orgID)
		exec(`UPDATE alert_groups SET resolved_by_user_id = NULL, resolved_by_service_account_id = $2
			WHERE org_id = $1 AND number = 3999`, e.orgID, sa)
		exec(`INSERT INTO alert_group_teams (org_id, alert_group_id, team_id, rule, joined_at)
			SELECT org_id, id, $2, 'label', $3 FROM alert_groups WHERE org_id = $1`, e.orgID, team, t0)
		exec(`INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, next_attempt_at, created_at,
			updated_at)
			SELECT org_id, $2, id, CASE WHEN number >= 3998 THEN 'pending' ELSE 'delivered' END, $3, $3, $3
			FROM alert_groups WHERE org_id = $1`, e.orgID, dest, t0)
		exec(`ANALYZE alert_groups`)
		exec(`ANALYZE alert_group_teams`)
		exec(`ANALYZE deliveries`)

		userID, saID := pgtype.Int8{Int64: user, Valid: true}, pgtype.Int8{Int64: sa, Valid: true}
		org := e.orgID
		pc, err := e.d.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Release()
		defer func() { _, _ = pc.Exec(ctx, "RESET plan_cache_mode") }()
		for _, w := range []struct {
			name string
			call func(q *dbgen.Queries)
		}{
			{"ListOpenGroupsOfDestinationAfter", func(q *dbgen.Queries) {
				_, _ = q.ListOpenGroupsOfDestinationAfter(ctx, dbgen.ListOpenGroupsOfDestinationAfterParams{OrgID: org,
					DestinationID: dest, AfterID: 0, Lim: 50})
			}},
			{"ListOpenGroupsFollowingOrganizationAfter", func(q *dbgen.Queries) {
				_, _ = q.ListOpenGroupsFollowingOrganizationAfter(ctx,
					dbgen.ListOpenGroupsFollowingOrganizationAfterParams{OrgID: org, AfterID: 0, Lim: 50})
			}},
			{"ListOpenGroupsOfTeamAfter", func(q *dbgen.Queries) {
				_, _ = q.ListOpenGroupsOfTeamAfter(ctx, dbgen.ListOpenGroupsOfTeamAfterParams{OrgID: org, TeamID: team,
					AfterID: 0, Lim: 50})
			}},
			{"ListOpenGroupsOfRouteAfter", func(q *dbgen.Queries) {
				_, _ = q.ListOpenGroupsOfRouteAfter(ctx, dbgen.ListOpenGroupsOfRouteAfterParams{OrgID: org,
					RouteID: route, AfterID: 0, Lim: 50})
			}},
			{"ListOpenGroupsAfter", func(q *dbgen.Queries) {
				_, _ = q.ListOpenGroupsAfter(ctx, dbgen.ListOpenGroupsAfterParams{OrgID: org, AfterID: 0, Lim: 50})
			}},
			{"ListOpenGroupsOfUserAfter", func(q *dbgen.Queries) {
				_, _ = q.ListOpenGroupsOfUserAfter(ctx, dbgen.ListOpenGroupsOfUserAfterParams{OrgID: org,
					UserID: userID, AfterID: 0, Lim: 50})
			}},
			{"ListOpenGroupsOfServiceAccountAfter", func(q *dbgen.Queries) {
				_, _ = q.ListOpenGroupsOfServiceAccountAfter(ctx, dbgen.ListOpenGroupsOfServiceAccountAfterParams{
					OrgID: org, ServiceAccountID: saID, AfterID: 0, Lim: 50})
			}},
			{"ListEvaluatedGroupsAfter", func(q *dbgen.Queries) {
				_, _ = q.ListEvaluatedGroupsAfter(ctx, dbgen.ListEvaluatedGroupsAfterParams{OrgID: org, AfterID: 0,
					Lim: 50})
			}},
		} {
			// The plan of a batch from the cursor 0, as the session prepares the statement, custom and generic.
			c := &captured{}
			w.call(dbgen.New(c))
			args := make([]string, len(c.args))
			for i, a := range c.args {
				switch v := a.(type) {
				case pgtype.Int8:
					args[i] = fmt.Sprint(v.Int64)
				default:
					args[i] = fmt.Sprint(v)
				}
			}
			if _, err := pc.Exec(ctx, "PREPARE walk AS "+c.sql); err != nil {
				t.Fatalf("%s: prepare: %v", w.name, err)
			}
			for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
				if _, err := pc.Exec(ctx, "SET plan_cache_mode = "+mode); err != nil {
					t.Fatal(err)
				}
				rows, err := pc.Query(ctx, "EXPLAIN (COSTS OFF) EXECUTE walk("+strings.Join(args, ", ")+")")
				if err != nil {
					t.Fatalf("%s %s: explain: %v", w.name, mode, err)
				}
				lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					t.Fatalf("%s %s: explain: %v", w.name, mode, err)
				}
				// Only the branch of the resolved Alert Groups reads deliveries as p; the scan starts at the cursor, and
				// the Alert Group of each pending delivery is looked up by its primary key alone, not walked from the
				// cursor.
				var scans []string
				cond, lookup := "", false
				for i, l := range lines {
					if strings.TrimSpace(l) == "Index Cond: (id = p.alert_group_id)" {
						lookup = true
					}
					if strings.HasSuffix(l, " on deliveries p") {
						scans = append(scans, strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "->")))
						if i+1 < len(lines) && strings.Contains(lines[i+1], "Index Cond:") {
							cond = strings.TrimSpace(lines[i+1])
						}
					}
				}
				const want = "using deliveries_alert_group_pending_idx on deliveries p"
				if len(scans) != 1 || !strings.HasSuffix(scans[0], want) || !strings.Contains(cond, "(org_id = ") ||
					!strings.Contains(cond, "(alert_group_id > ") || !lookup {
					t.Errorf("%s %s: the pending branch reads %q (%s, lookup by id %v), not "+
						"deliveries_alert_group_pending_idx from the cursor and each Alert Group by id\n%s", w.name, mode,
						scans, cond, lookup, strings.Join(lines, "\n"))
				}
			}
			if _, err := pc.Exec(ctx, "DEALLOCATE walk"); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// TestIntegrationUserWalkTakesAssignees is #628, C-12.FR-12 and C-32.FR-12 for the walk of a User on PostgreSQL:
// besides the Alert Groups the User owns, snoozed or resolved, it lists the firing ones the User is an Assignee of —
// once, also when two chains made them one — and leaves out a firing one without them among its Assignees and one
// that stopped firing, even with an Assignee row of theirs left on it. It lists by id after its cursor, a batch at a
// time.
func TestIntegrationUserWalkTakesAssignees(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := e.d.Pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		r := e.route(t, "one", "one", "alertname").ID
		ivan, petr := e.user(t, "SR0000000000JV", "ivan"), e.user(t, "SR0000000000PE", "petr")

		// two fires with ivan an Assignee of two chains; one fires with ivan and petr; petr fires with petr; owned
		// is acknowledged by ivan; left is acknowledged by petr and resolved was resolved by someone else, each with
		// an Assignee row of ivan left on it; plain fires without Assignees.
		fixtures := []struct {
			name, status string
			assignees    [][]int64
		}{
			{"two", "firing", [][]int64{{ivan}, {ivan}}},
			{"one", "firing", [][]int64{{ivan, petr}}},
			{"petr", "firing", [][]int64{{petr}}},
			{"owned", "firing", nil},
			{"left", "firing", [][]int64{{ivan}}},
			{"resolved", "resolved", [][]int64{{ivan}}},
			{"plain", "firing", nil},
		}
		ids := map[string]int64{}
		runs := 0
		for i, f := range fixtures {
			g := &row{number: int64(i + 1), route: r, ints: []int64{e.intID}, severity: "warning", status: f.status,
				created: t0, changed: t0, labels: map[string]string{"alertname": f.name}, title: f.name}
			if f.status == "resolved" {
				g.resolved, g.by = &t0, groups.ResolvedByUser
			}
			e.insert(t, g)
			ids[f.name] = g.id
			for _, people := range f.assignees {
				runs++
				var run int64
				if err := e.d.Pool.QueryRow(ctx, `INSERT INTO escalation_runs (org_id, public_id, alert_group_id,
					chain_name, sources, steps, state, generation, started_at)
					VALUES ($1, $2, $3, 'devops', '[{"kind":"route"}]', '[]', 'running', 1, $4) RETURNING id`,
					e.orgID, fmt.Sprintf("ER%012d", runs), g.id, t0).Scan(&run); err != nil {
					t.Fatal(err)
				}
				for _, u := range people {
					exec(`INSERT INTO alert_group_assignees (org_id, alert_group_id, escalation_run_id, step_number,
						user_id, since) VALUES ($1, $2, $3, 1, $4, $5)`, e.orgID, g.id, run, u, t0)
				}
			}
		}
		ack := func(name string, owner int64) {
			t.Helper()
			exec(`UPDATE alert_groups SET status = 'acknowledged', owner_user_id = $2, acknowledged_at = $3
				WHERE id = $1`, ids[name], owner, t0)
		}
		ack("owned", ivan)
		ack("left", petr)

		q := dbgen.New(e.d.Pool)
		for _, c := range []struct {
			name string
			user int64
			want []string
		}{
			{"ivan", ivan, []string{"two", "one", "owned"}},
			{"petr", petr, []string{"one", "petr", "left"}},
		} {
			list := func(after int64, lim int32) []int64 {
				t.Helper()
				got, err := q.ListOpenGroupsOfUserAfter(ctx, dbgen.ListOpenGroupsOfUserAfterParams{OrgID: e.orgID,
					UserID: pgtype.Int8{Int64: c.user, Valid: true}, AfterID: after, Lim: lim})
				if err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				return got
			}
			var want []int64
			for _, n := range c.want {
				want = append(want, ids[n])
			}
			if got := list(0, 50); !slices.Equal(got, want) {
				t.Errorf("%s: listed %v, want %v (%v)", c.name, got, want, ids)
			}
			// One and two at a time after the cursor, as the worker walks.
			for _, lim := range []int32{1, 2} {
				var walked []int64
				for after, rounds := int64(0), 0; ; rounds++ {
					if rounds > len(fixtures) {
						t.Fatalf("%s: the walk did not end after %d batches: %v", c.name, rounds, walked)
					}
					batch := list(after, lim)
					walked = append(walked, batch...)
					if len(batch) < int(lim) {
						break
					}
					after = batch[len(batch)-1]
				}
				if !slices.Equal(walked, want) {
					t.Errorf("%s: walked %v %d at a time, want %v", c.name, walked, lim, want)
				}
			}
		}
	})
}
