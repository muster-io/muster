// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db/dbtest"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
	mentionsdb "github.com/muster-io/muster/internal/mentions/dbgen"
)

// TestEnsureStormSummaryRevives is C-11.FR-6 on PostgreSQL: EnsureStormSummary of a Storm summary that left its
// Destination — retired after it was published, or withheld before — publishes it afresh, as RejoinDelivery does a
// delivery of an Alert Group: pending and due now, without its message, Thread, extracted values, notes or desired
// hash, so that the caller stores its Desired state again; Quiet when it was published, since bringing it back is a
// change of configuration (ADR-0018), Loud when it never was, its first Publication. A summary that did not leave is
// returned as it is.
func TestEnsureStormSummaryRevives(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		q := deliverydb.New(l.d.Pool)
		var storm int64
		if err := l.d.Pool.QueryRow(ctx, `INSERT INTO storms (org_id, route_id, started_at, alert_group_count)
			VALUES ($1, $2, $3, 3) RETURNING id`, l.orgID, l.routeID, l.business.Now()).Scan(&storm); err != nil {
			t.Fatal(err)
		}
		ensure := func(dest int64) deliverydb.EnsureStormSummaryRow {
			t.Helper()
			row, err := q.EnsureStormSummary(ctx, deliverydb.EnsureStormSummaryParams{OrgID: l.orgID,
				DestinationID: dest, StormID: pgtype.Int8{Int64: storm, Valid: true}, Now: l.business.Now()})
			if err != nil {
				t.Fatal(err)
			}
			return row
		}
		type summary struct {
			state, values                                         string
			message, published, hash, thread, opened, late, again bool
			loud                                                  *bool
		}
		read := func(id int64) summary {
			t.Helper()
			var sm summary
			if err := l.d.Pool.QueryRow(ctx, `SELECT state, response_values::text, message_id IS NOT NULL,
				published_at IS NOT NULL, desired_hash IS NOT NULL,
				thread_anchor_id IS NOT NULL OR thread_chain_last_id IS NOT NULL OR thread_state <> 'none',
				thread_opened, late_note, republished_after_delete, publication_loud
				FROM deliveries WHERE id = $1`, id).Scan(&sm.state, &sm.values, &sm.message, &sm.published, &sm.hash,
				&sm.thread, &sm.opened, &sm.late, &sm.again, &sm.loud); err != nil {
				t.Fatal(err)
			}
			return sm
		}
		for _, c := range []struct {
			name      string
			published bool
			state     string
			loud      bool
		}{{"retired after its publication", true, "retired", false}, {"withheld before it", false, "withheld", true}} {
			dest := l.dests[0]
			if !c.published {
				dest = l.dests[1]
			}
			row := ensure(dest)
			l.exec(t, `UPDATE deliveries SET desired_hash = '\x01', desired_text = 'storm', state = $2,
				response_values = '{"k":"v"}', late_note = $3, republished_after_delete = $3, thread_opened = $3,
				message_id = CASE WHEN $3 THEN 'm1' END, message_url = CASE WHEN $3 THEN 'https://x/m1' END,
				published_at = CASE WHEN $3 THEN $4::timestamptz END,
				publication_started_at = CASE WHEN $3 THEN $4::timestamptz END,
				thread_state = CASE WHEN $3 THEN 'attached' ELSE 'none' END,
				thread_anchor_id = CASE WHEN $3 THEN 'm1' END, thread_chain_last_id = CASE WHEN $3 THEN 'r1' END,
				actual_version = CASE WHEN $3 THEN 1 END, actual_hash = CASE WHEN $3 THEN '\x01'::bytea END
				WHERE id = $1`, row.ID, c.state, c.published, l.business.Now())
			again := ensure(dest)
			sm := read(again.ID)
			if again.ID != row.ID || again.DesiredHash != nil || sm.state != "pending" || sm.message || sm.published ||
				sm.hash || sm.thread || sm.opened || sm.late || sm.again || sm.values != "{}" || sm.loud == nil ||
				*sm.loud != c.loud {
				t.Errorf("%s: revived %+v (loud %v), desired hash %x", c.name, sm, deref(sm.loud), again.DesiredHash)
			}
		}

		// A summary that did not leave is returned as it is.
		row := ensure(l.dests[0])
		l.exec(t, `UPDATE deliveries SET state = 'delivered', desired_hash = '\x02', message_id = 'm2',
			published_at = $2 WHERE id = $1`, row.ID, l.business.Now())
		if again := ensure(l.dests[0]); string(again.DesiredHash) != "\x02" {
			t.Errorf("a live summary changed: desired hash %x", again.DesiredHash)
		}
		if sm := read(row.ID); sm.state != "delivered" || !sm.message || !sm.published {
			t.Errorf("a live summary changed: %+v", sm)
		}
	})
}

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// TestStormAlertGroups is C-11.FR-6 and C-28.FR-7 on PostgreSQL: StartStorm records the Alert Group that starts a
// Storm, JoinStorm each one that joins it — after another transaction started it first too, whose StartStorm records
// nothing — and ListStormTeams reads the Teams of exactly those: not of an Alert Group of the Route created later
// within the window that the Storm did not count, nor of one moved onto the Route, nor a deleted Team, but still of a
// counted one moved off it.
func TestStormAlertGroups(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		l.fresh(t, 1000, 1)
		q := deliverydb.New(l.d.Pool)
		now := l.business.Now()
		group := func(n int, route int64, team string) int64 {
			t.Helper()
			publicID := fmt.Sprintf("AGSTRM%08d", n)
			var id int64
			if err := l.d.Pool.QueryRow(ctx, `INSERT INTO alert_groups (org_id, public_id, number, route_id,
				group_key_labels, group_key_values, group_key_sha256, title, common_labels, common_annotations,
				integration_ids, status, severity_level, urgent, created_at, last_changed_at)
				VALUES ($1, $2, $3, $4, '{alertname}', '{}', sha256(convert_to($2::text, 'UTF8')), 'g', '{}', '{}',
				'{}', 'firing', 'critical', false, $5, $5) RETURNING id`, l.orgID, publicID, 900000+n, route,
				now.Add(time.Duration(n)*time.Second)).Scan(&id); err != nil {
				t.Fatal(err)
			}
			l.exec(t, `INSERT INTO teams (org_id, public_id, name, slug, created_at, updated_at)
				VALUES ($1, $2, $3, lower($3), $4, $4) ON CONFLICT DO NOTHING`, l.orgID, "TMSTRM"+team, team, now)
			l.exec(t, `INSERT INTO alert_group_teams (org_id, alert_group_id, team_id, rule, joined_at)
				SELECT $1, $2, id, 'label', $3 FROM teams WHERE org_id = $1 AND public_id = $4`, l.orgID, id, now,
				"TMSTRM"+team)
			return id
		}
		members := func(storm int64) []int64 {
			t.Helper()
			rows, err := l.d.Pool.Query(ctx, `SELECT alert_group_id FROM storm_alert_groups
				WHERE org_id = $1 AND storm_id = $2 ORDER BY alert_group_id`, l.orgID, storm)
			if err != nil {
				t.Fatal(err)
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}

		first, second, third := group(1, l.routeID, "AAAAAAA1"), group(2, l.routeID, "AAAAAAA2"),
			group(3, l.routeID, "AAAAAAA3")
		started, err := q.StartStorm(ctx, deliverydb.StartStormParams{OrgID: l.orgID, RouteID: l.routeID, Now: now,
			AlertGroupID: first})
		if err != nil || started.AlertGroupCount != 1 {
			t.Fatalf("start: %+v %v", started, err)
		}
		storm := started.ID
		joined, err := q.JoinStorm(ctx, deliverydb.JoinStormParams{OrgID: l.orgID, RouteID: l.routeID,
			AlertGroupID: second})
		if err != nil || joined.ID != storm || joined.AlertGroupCount != 2 {
			t.Fatalf("join: %+v %v", joined, err)
		}
		// The start that lost the race to another transaction records nothing; its join records the Alert Group.
		if _, err := q.StartStorm(ctx, deliverydb.StartStormParams{OrgID: l.orgID, RouteID: l.routeID, Now: now,
			AlertGroupID: third}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("a second start = %v", err)
		}
		if got := members(storm); !slices.Equal(got, []int64{first, second}) {
			t.Fatalf("members after the lost start %v", got)
		}
		joined, err = q.JoinStorm(ctx, deliverydb.JoinStormParams{OrgID: l.orgID, RouteID: l.routeID,
			AlertGroupID: third})
		if err != nil || joined.AlertGroupCount != 3 {
			t.Fatalf("join after the lost start: %+v %v", joined, err)
		}
		if got := members(storm); !slices.Equal(got, []int64{first, second, third}) {
			t.Fatalf("members %v", got)
		}

		// An Alert Group of the Route the Storm did not count, created later inside the window; one moved onto the
		// Route; a counted one moved off it.
		group(4, l.routeID, "BBBBBBB4")
		moved := group(5, l.defaultID, "BBBBBBB5")
		l.exec(t, `UPDATE alert_groups SET route_id = $1 WHERE id = $2`, l.routeID, moved)
		l.exec(t, `UPDATE alert_groups SET route_id = $1 WHERE id = $2`, l.defaultID, second)
		l.exec(t, `UPDATE storms SET ended_at = $2 WHERE id = $1`, storm, now.Add(time.Minute))
		// A deleted Team of a counted Alert Group is left out.
		group(6, l.defaultID, "CCCCCCC6")
		l.exec(t, `INSERT INTO alert_group_teams (org_id, alert_group_id, team_id, rule, joined_at)
			SELECT $1, $2, id, 'label', $3 FROM teams WHERE org_id = $1 AND public_id = 'TMSTRMCCCCCCC6'`, l.orgID,
			third, now)
		l.exec(t, `UPDATE teams SET deleted_at = $2 WHERE org_id = $1 AND public_id = 'TMSTRMCCCCCCC6'`, l.orgID, now)
		teams, err := mentionsdb.New(l.d.Pool).ListStormTeams(ctx, mentionsdb.ListStormTeamsParams{OrgID: l.orgID,
			StormID: storm})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tm := range teams {
			got = append(got, tm.Name)
		}
		if want := []string{"AAAAAAA1", "AAAAAAA2", "AAAAAAA3"}; !slices.Equal(got, want) {
			t.Errorf("storm teams %v, want %v", got, want)
		}
		var count int64
		if err := l.d.Pool.QueryRow(ctx, `SELECT alert_group_count FROM storms WHERE id = $1`, storm).Scan(
			&count); err != nil || count != int64(len(members(storm))) {
			t.Errorf("alert_group_count %d of %d members: %v", count, len(members(storm)), err)
		}
	})
}
