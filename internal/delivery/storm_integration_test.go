// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db/dbtest"
	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
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
