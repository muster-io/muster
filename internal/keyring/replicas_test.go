// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package keyring

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

func migrated(t *testing.T, s dbtest.Server) *db.DB {
	t.Helper()
	u := logging.Secret(s.NewDatabase(t))
	conn := config.Database{URL: u, SSLMode: "disable"}
	d, err := db.Open(t.Context(), conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo)); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestIntegrationCanaryUnderTheMigrationLock: two replicas starting together on a new database write one canary,
// with the first key of the replica that took the lock first, and both open it.
func TestIntegrationCanaryUnderTheMigrationLock(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := migrated(t, s)
		store := NewStore(d.Pool)
		keyrings := []*Keyring{newKeyring(t, 'a', 'b'), newKeyring(t, 'b', 'a')}
		states := make([]State, len(keyrings))
		errs := make([]error, len(keyrings))
		var wg sync.WaitGroup
		for i, k := range keyrings {
			wg.Go(func() {
				errs[i] = d.WithMigrationLock(t.Context(), func(ctx context.Context) error {
					st, err := k.Establish(ctx, store, time.Now())
					states[i] = st
					if err != nil {
						return err
					}
					return k.Open(ctx, logging.New(&bytes.Buffer{}, logging.LevelInfo), st)
				})
			})
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
		if states[0].ActiveKeyID != states[1].ActiveKeyID || !bytes.Equal(states[0].Canary, states[1].Canary) {
			t.Errorf("the replicas saw two canaries: %+v, %+v", states[0], states[1])
		}
		if keyrings[0].ActiveKeyID() != keyrings[1].ActiveKeyID() {
			t.Errorf("active keys %s and %s", keyrings[0].ActiveKeyID(), keyrings[1].ActiveKeyID())
		}
		var rows int
		if err := d.Pool.QueryRow(t.Context(), "SELECT count(*) FROM keyring_state").Scan(&rows); err != nil || rows != 1 {
			t.Errorf("%d keyring_state rows, %v", rows, err)
		}

		// A later start with the second key first keeps the active key; another Keyring fails.
		later := newKeyring(t, 'b', 'a')
		st, err := later.Establish(t.Context(), store, time.Now())
		if err != nil || st.ActiveKeyID != states[0].ActiveKeyID {
			t.Errorf("a later start: %+v, %v", st, err)
		}
		if err := newKeyring(t, 'c').Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), st); !errors.Is(
			err, ErrKeyMismatch) {
			t.Errorf("another Keyring opened the canary: %v", err)
		}
	})
}

// TestIntegrationReplicaRecords drives the records with a manual real clock: the refresh, the live window, the stop
// on an active key that is not held and the removal at shutdown.
func TestIntegrationReplicaRecords(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		d := migrated(t, s)
		ctx := t.Context()
		store := NewStore(d.Pool)
		k := newKeyring(t, 'a', 'b')
		st, err := k.Establish(ctx, store, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := k.Open(ctx, logging.New(&bytes.Buffer{}, logging.LevelInfo), st); err != nil {
			t.Fatal(err)
		}
		realClock := clock.NewManual(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
		var log bytes.Buffer
		r := NewRecorder(k, store, realClock, logging.New(&log, logging.LevelInfo), NewReplicaID("muster-0"), "muster-0",
			"1.2.3")
		if err := r.Start(ctx); err != nil {
			t.Fatal(err)
		}
		other := NewRecorder(newKeyring(t, 'a'), store, realClock, logging.New(&bytes.Buffer{}, logging.LevelInfo),
			NewReplicaID(""), "", "1.2.3")
		realClock.Advance(time.Minute)
		if err := other.Start(ctx); err != nil {
			t.Fatal(err)
		}

		live, err := LiveReplicas(ctx, store, realClock.Now())
		if err != nil || len(live) != 2 {
			t.Fatalf("LiveReplicas = %+v, %v", live, err)
		}
		mine := live[slices.IndexFunc(live, func(x Replica) bool { return x.ID == r.ID() })]
		if !slices.Equal(mine.KeyIDs, k.KeyIDs()) || mine.Hostname != "muster-0" || mine.Version != "1.2.3" {
			t.Errorf("record %+v", mine)
		}

		// Two minutes after its last refresh the first replica no longer counts as live; a refresh brings it back. It
		// could not refresh for longer than ReregisterAfter, so it re-registers with a new start time.
		realClock.Advance(LiveExpiry - time.Minute)
		if live, _ := LiveReplicas(ctx, store, realClock.Now()); len(live) != 1 || live[0].ID != other.ID() {
			t.Errorf("live after LiveExpiry: %+v", live)
		}
		realClock.Advance(KeyRecordRefresh)
		if err := r.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		live, _ = LiveReplicas(ctx, store, realClock.Now())
		if i := slices.IndexFunc(live, func(x Replica) bool { return x.ID == r.ID() }); len(live) != 2 || i < 0 ||
			!live[i].RefreshedAt.Equal(realClock.Now()) ||
			!live[i].StartedAt.Equal(realClock.Now()) {
			t.Errorf("live after the refresh: %+v", live)
		}

		// An active key this replica does not hold stops it with the canary error.
		if _, err := d.Pool.Exec(ctx,
			"UPDATE keyring_state SET active_key_id = 'k-unknown', canary_key_id = 'k-unknown'"); err != nil {
			t.Fatal(err)
		}
		if err := r.Refresh(ctx); !errors.Is(err, ErrKeyMismatch) {
			t.Errorf("Refresh = %v", err)
		}
		if !bytes.Contains(log.Bytes(), []byte(`"event":"active_key_not_held"`)) {
			t.Errorf("log %s", log.String())
		}

		for _, rec := range []*Recorder{r, other} {
			if err := rec.Stop(ctx); err != nil {
				t.Fatal(err)
			}
		}
		var rows int
		if err := d.Pool.QueryRow(ctx, "SELECT count(*) FROM replicas").Scan(&rows); err != nil || rows != 0 {
			t.Errorf("%d replica records after the shutdown, %v", rows, err)
		}
	})
}
