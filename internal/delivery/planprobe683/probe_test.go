// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build issue683

package planprobe683

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	deliverydb "github.com/muster-io/muster/internal/delivery/dbgen"
)

// TestAutoPlans runs the claims through dbgen on one pgx connection (QueryExecModeCacheStatement) with
// plan_cache_mode auto, then reads how PostgreSQL planned them.
func TestAutoPlans(t *testing.T) {
	ranks := strings.Split(os.Getenv("PROBE_RANKS"), ",")
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, dsn := range strings.Fields(os.Getenv("PROBE_DSNS")) {
		ctx := t.Context()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		var took [2]time.Duration
		for i := range 60 {
			r := ranks[i%len(ranks)]
			for k := range 2 {
				tx, err := conn.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				if k == 0 {
					_, err = deliverydb.New(tx).ClaimDueDeliveries(ctx, deliverydb.ClaimDueDeliveriesParams{Owner: "r1",
						LeaseUntil: at.Add(time.Minute), OrgID: 1, Due: at, Now: at, Urgent: r[0] == 't',
						ConfigurationOnly: r[1] == 't', Lim: 10})
				} else {
					_, err = deliverydb.New(tx).ClaimDueWebhookEvents(ctx, deliverydb.ClaimDueWebhookEventsParams{
						Owner: "r1", LeaseUntil: at.Add(time.Minute), OrgID: 1, Due: at, Now: at, Lim: 10})
				}
				if i >= 10 {
					took[k] += time.Since(start)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
			}
		}
		rows, err := conn.Query(ctx, `SELECT substring(statement FROM 'name: (\w+)'), generic_plans, custom_plans
			FROM pg_prepared_statements WHERE statement LIKE '%name: ClaimDue%' ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name string
			var generic, custom int64
			if err := rows.Scan(&name, &generic, &custom); err != nil {
				t.Fatal(err)
			}
			k := 0
			if name == "ClaimDueWebhookEvents" {
				k = 1
			}
			t.Logf("PROBE %s ranks=%v %s generic_plans=%d custom_plans=%d mean of executions 11-60=%v", dsn[strings.LastIndex(dsn, "@")+1:],
				ranks, name, generic, custom, took[k]/50)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close(ctx)
	}
}
