// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package db

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db/dbtest"
)

// claimRows is a claim over the table claimable, in the shape of schema.md §5: due rows on the business clock whose
// lease is free or ran out on the real clock, locked with FOR UPDATE SKIP LOCKED, then leased.
func claimRows(ctx context.Context, tx pgx.Tx, p ClaimParams) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM claimable
		WHERE org_id = 1 AND deadline <= $1 AND (lease_until IS NULL OR lease_until <= $2)
		ORDER BY deadline, id LIMIT $3 FOR UPDATE SKIP LOCKED`, p.Due, p.Now, p.Limit)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(ids) == 0 {
		return ids, err
	}
	_, err = tx.Exec(ctx, `UPDATE claimable SET lease_owner = $1, lease_until = $2 WHERE org_id = 1 AND id = ANY($3)`,
		p.Owner, p.LeaseUntil, ids)
	return ids, err
}

func claimTable(t *testing.T, s dbtest.Server) *DB {
	t.Helper()
	d := openFresh(t, s)
	if _, err := d.Pool.Exec(t.Context(), `CREATE TABLE claimable (
		id bigint PRIMARY KEY, org_id bigint NOT NULL, deadline timestamptz NOT NULL, lease_owner text,
		lease_until timestamptz)`); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestIntegrationClaim is S-062 step 1: the lease follows the real clock and due times the business clock; a row whose
// lease ran out is claimed again by another replica; two claimers never take the same row.
func TestIntegrationClaim(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		d := claimTable(t, s)
		business, realClock := clock.NewManual(business0), clock.NewManual(real0)
		clocks := clock.Clocks{Business: business, Real: realClock}
		r1 := Lease{Owner: "r1", Duration: time.Minute, Clocks: clocks}
		r2 := Lease{Owner: "r2", Duration: time.Minute, Clocks: clocks}
		if _, err := d.Pool.Exec(ctx, `INSERT INTO claimable (id, org_id, deadline) VALUES (1, 1, $1), (2, 1, $2)`,
			business0.Add(-time.Minute), business0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}

		ids, err := Claim(ctx, d.Pool, r1, 10, claimRows)
		if err != nil || !slices.Equal(ids, []int64{1}) {
			t.Fatalf("the first claim = %v, %v; want the due row 1", ids, err)
		}
		var owner string
		var until time.Time
		if err := d.Pool.QueryRow(ctx, `SELECT lease_owner, lease_until FROM claimable WHERE id = 1`).Scan(&owner,
			&until); err != nil || owner != "r1" || !until.Equal(real0.Add(time.Minute)) {
			t.Fatalf("lease = %s until %v, %v; want r1 until the real time plus a minute", owner, until, err)
		}

		// The lease is held on the real clock: moving the business clock does not free it.
		business.Advance(30 * time.Minute)
		if ids, _ := Claim(ctx, d.Pool, r2, 10, claimRows); len(ids) != 0 {
			t.Fatalf("a held lease was claimed again: %v", ids)
		}
		// The due time is on the business clock: row 2 becomes due when it passes, the real clock unchanged.
		business.Advance(30 * time.Minute)
		if ids, _ := Claim(ctx, d.Pool, r2, 10, claimRows); !slices.Equal(ids, []int64{2}) {
			t.Fatalf("at the business time of row 2 = %v", ids)
		}
		// The lease of row 1 runs out on the real clock and another replica claims it again.
		realClock.Advance(time.Minute)
		if ids, _ := Claim(ctx, d.Pool, r2, 10, claimRows); !slices.Equal(ids, []int64{1, 2}) {
			t.Fatalf("after the leases ran out = %v", ids)
		}
		if err := d.Pool.QueryRow(ctx, `SELECT lease_owner FROM claimable WHERE id = 1`).Scan(&owner); err != nil ||
			owner != "r2" {
			t.Errorf("row 1 is leased by %s, %v", owner, err)
		}
	})
}

// TestIntegrationClaimersNeverShare: replicas claiming at the same time take every row once.
func TestIntegrationClaimersNeverShare(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		d := claimTable(t, s)
		const rows = 300
		if _, err := d.Pool.Exec(ctx, `INSERT INTO claimable (id, org_id, deadline)
			SELECT g, 1, $1 FROM generate_series(1, $2::int) g`, business0, rows); err != nil {
			t.Fatal(err)
		}
		clocks := clock.Clocks{Business: clock.NewManual(business0), Real: clock.NewManual(real0)}
		var (
			mu      sync.Mutex
			claimed = map[int64]string{}
			wg      sync.WaitGroup
			dup     []string
		)
		for r := range 4 {
			owner := fmt.Sprintf("r%d", r)
			wg.Go(func() {
				l := Lease{Owner: owner, Duration: time.Minute, Clocks: clocks}
				for {
					ids, err := Claim(ctx, d.Pool, l, 7, claimRows)
					if err != nil {
						t.Error(err)
						return
					}
					if len(ids) == 0 {
						return
					}
					mu.Lock()
					for _, id := range ids {
						if prev, ok := claimed[id]; ok {
							dup = append(dup, fmt.Sprintf("%d by %s and %s", id, prev, owner))
						}
						claimed[id] = owner
					}
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if len(claimed) != rows || len(dup) != 0 {
			t.Errorf("%d rows claimed of %d; taken twice: %v", len(claimed), rows, dup)
		}
	})
}
