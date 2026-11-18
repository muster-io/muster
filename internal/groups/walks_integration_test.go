// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package groups_test

import (
	"context"
	"slices"
	"testing"

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
