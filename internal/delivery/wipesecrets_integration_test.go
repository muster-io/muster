// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package delivery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/db/dbtest"
)

// TestWipeDestinationSecretsLockOrder is #616 against PostgreSQL 14 and 17: the retention of outgoing webhook events
// wipes the secrets of every deleted Destination of the Organization that qualifies, and locks them in id order, so it
// runs at the same time as a transaction that locks several deleted Destinations in id order, as key activation locks
// the outgoing webhooks, without a deadlock. Every scan but the one by id meets the second Destination first: its
// public_id is lower, which the index on (org_id, public_id) the wipe scans by orders, and the first's row is rewritten
// after it. The transaction holds the first when the wipe starts, and takes the second once the wipe waits.
func TestWipeDestinationSecretsLockOrder(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		l := setupLive(t, s)
		ctx := t.Context()
		first := l.eventsHook(t, "DSAAAAAAAAAAWZ", "first-hook", true)
		second := l.eventsHook(t, "DSAAAAAAAAAAWB", "second-hook", true)
		for _, id := range []int64{first, second} {
			l.exec(t, `INSERT INTO destination_secrets (destination_id, org_id, name, value_ciphertext, value_key_id,
				value_updated_at) VALUES ($1, $2, 'token', '\x01', 'k1', $3)`, id, l.orgID, t0)
		}
		l.exec(t, `UPDATE destinations SET name = 'first-hook-renamed' WHERE org_id = $1 AND id = $2`, l.orgID, first)
		if l.count(t, `SELECT count(*) FROM destinations a, destinations b WHERE a.org_id = $1 AND b.org_id = $1
			AND a.id = $2 AND b.id = $3 AND a.id < b.id AND a.public_id > b.public_id AND a.ctid > b.ctid`, l.orgID,
			first, second) != 1 {
			t.Fatal("a scan meets the first destination first")
		}

		// The transaction runs under its own context; the cleanup cancels it and waits for the goroutine that takes
		// the second Destination, if it started, before the rollback, so that the connection is never used twice at
		// once.
		txCtx, cancel := context.WithCancel(ctx)
		tx, err := l.d.Pool.Begin(txCtx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var txDone chan struct{}
		t.Cleanup(func() {
			cancel()
			if txDone != nil {
				<-txDone
			}
			_ = tx.Rollback(context.WithoutCancel(ctx))
		})
		lock := func(id int64) error {
			_, err := tx.Exec(txCtx, `SELECT id FROM destinations WHERE org_id = $1 AND id = $2 FOR NO KEY UPDATE`,
				l.orgID, id)
			return err
		}
		if err := lock(first); err != nil {
			t.Fatal(err)
		}
		wiped := make(chan error, 1)
		go func() {
			_, err := l.svc.PruneWebhookEvents(ctx, l.business.Now())
			wiped <- err
		}()
		for deadline := time.Now().Add(10 * time.Second); l.count(t, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			AND query LIKE '-- name: WipeDestinationSecrets %'`) != 1; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the wipe did not wait for the first destination")
			}
		}
		locked := make(chan error, 1)
		txDone = make(chan struct{})
		go func() {
			defer close(txDone)
			err := lock(second)
			if err == nil {
				err = tx.Commit(txCtx)
			}
			locked <- err
		}()

		for name, done := range map[string]<-chan error{"the transaction": locked, "the wipe": wiped} {
			select {
			case err := <-done:
				if pe, ok := errors.AsType[*pgconn.PgError](err); ok && pe.Code == "40P01" {
					t.Errorf("%s: a deadlock: %v", name, err)
				} else if err != nil {
					t.Errorf("%s: %v", name, err)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("%s did not end", name)
			}
		}
		if n := l.count(t, `SELECT count(*) FROM destinations d WHERE d.org_id = $1 AND d.id IN ($2, $3)
			AND (d.signing_secret_ciphertext IS NOT NULL OR EXISTS (SELECT 1 FROM destination_secrets s
				WHERE s.org_id = d.org_id AND s.destination_id = d.id))`, l.orgID, first, second); n != 0 {
			t.Errorf("%d deleted destinations kept their secrets", n)
		}
	})
}
