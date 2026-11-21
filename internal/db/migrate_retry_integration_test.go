// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package db

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
)

// retryEnv is an empty database migrated to version 1 of a test migration source, the migrator that runs it over a
// session connection, the log of the migrator and a second connection, the replica that holds the locks.
type retryEnv struct {
	d       *DB
	mm      *migrate.Migrate
	session *pgx.Conn
	replica *pgx.Conn
	out     *bytes.Buffer
	waits   []time.Duration
	// release ends what the replica holds; the first wait between attempts calls it.
	release func()
}

// newRetryEnv migrates to version 1 of the test migrations, which adds the tables first and second; version 2 is the
// SQL of the test. The waits between attempts are recorded instead of waited.
func newRetryEnv(t *testing.T, s dbtest.Server, second string) *retryEnv {
	t.Helper()
	d := openFresh(t, s)
	session, err := d.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(t.Context(), session) })
	replica, err := d.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(t.Context(), replica) })
	e := &retryEnv{d: d, session: session, replica: replica, out: &bytes.Buffer{}}
	drv, err := newDriver(t.Context(), logging.New(e.out, logging.LevelInfo), session)
	if err != nil {
		t.Fatal(err)
	}
	drv.wait = func(_ context.Context, d time.Duration) bool {
		e.waits = append(e.waits, d)
		if len(e.waits) == 1 && e.release != nil {
			e.release()
		}
		return true
	}
	fsys := fstest.MapFS{
		"m/1_tables.up.sql": {Data: []byte("CREATE TABLE first (id int);\nCREATE TABLE second (id int);\n")},
		"m/2_test.up.sql":   {Data: []byte(second)},
	}
	mm, err := migrateWith(fsys, "m", drv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = mm.Close() })
	if err := mm.Migrate(1); err != nil {
		t.Fatal(err)
	}
	e.mm = mm
	return e
}

func (e *retryEnv) replicaExec(t *testing.T, sql string) {
	t.Helper()
	if _, err := e.replica.Exec(t.Context(), sql); err != nil {
		t.Fatalf("the replica: %s: %v", sql, err)
	}
}

// replicaCommit commits the replica's transaction; it runs on the migration's goroutine, so it reports with Errorf.
func (e *retryEnv) replicaCommit(t *testing.T) {
	if _, err := e.replica.Exec(t.Context(), "COMMIT"); err != nil {
		t.Errorf("the replica: COMMIT: %v", err)
	}
}

// waitForLock returns once the session connection with pid waits for a lock.
func (e *retryEnv) waitForLock(t *testing.T, pid uint32, done <-chan error) {
	t.Helper()
	for {
		select {
		case err := <-done:
			t.Fatalf("the migration ended before it waited for the replica's lock: %v", err)
		default:
		}
		var waiting bool
		if err := e.d.Pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT FROM pg_stat_activity
			WHERE pid = $1 AND wait_event_type = 'Lock')`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
	}
}

// checkVersion2 checks that the recorded version is 2, the test migration's, reading it over the pool: a failed
// migration leaves the session connection in its aborted transaction.
func (e *retryEnv) checkVersion2(t *testing.T, wantDirty bool) {
	t.Helper()
	if v, dirty, err := SchemaVersion(t.Context(), e.d.Pool); err != nil || v != 2 || dirty != wantDirty {
		t.Errorf("version %d, dirty %v, %v; want 2, dirty %v", v, dirty, err, wantDirty)
	}
}

func (e *retryEnv) count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// lockTimeoutMigration commits a first transaction, then alters a table that a replica writes, giving up after 50 ms
// of waiting for its lock.
const lockTimeoutMigration = `BEGIN;
CREATE TABLE third (id int);
COMMIT;

BEGIN;
SET LOCAL lock_timeout = '50ms';
ALTER TABLE second ADD COLUMN note text;
COMMIT;
`

// TestIntegrationMigrationRetriesLockTimeout: the second transaction of a migration waits for the lock of a table
// that a replica of the previous version writes and gives up on lock_timeout (55P03); it is rolled back and runs
// again once the replica committed, without running the committed first transaction again. The schema ends at the
// new version, not dirty, and migration_retried records the retry.
func TestIntegrationMigrationRetriesLockTimeout(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newRetryEnv(t, s, lockTimeoutMigration)
		e.replicaExec(t, "BEGIN")
		e.replicaExec(t, "INSERT INTO second VALUES (1)")
		e.release = func() { e.replicaCommit(t) }
		if err := e.mm.Up(); err != nil {
			t.Fatalf("Up: %v", err)
		}
		e.checkVersion2(t, false)
		if len(e.waits) != 1 || e.waits[0] != time.Second {
			t.Errorf("waited %v, want one wait of a second", e.waits)
		}
		if n := e.count(t, `SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'second' AND column_name = 'note'`); n != 1 {
			t.Error("the retried transaction did not add second.note")
		}
		if n := e.count(t, "SELECT count(*) FROM second"); n != 1 {
			t.Errorf("second has %d rows, want the replica's one", n)
		}
		want := `"level":"WARN","event":"migration_retried","version":2,"attempt":1,"sqlstate":"55P03"`
		if !strings.Contains(e.out.String(), want) || strings.Count(e.out.String(), "migration_retried") != 1 {
			t.Errorf("logged %s, want once %s", e.out.String(), want)
		}
	})
}

// deadlockMigration locks first, then second; the replica holds second and then asks for first. The migration, which
// waited first, finds the deadlock after its deadlock_timeout of a second, long before the replica's.
const deadlockMigration = `BEGIN;
SET LOCAL deadlock_timeout = '1s';
LOCK TABLE first IN ACCESS EXCLUSIVE MODE;
LOCK TABLE second IN ACCESS EXCLUSIVE MODE;
ALTER TABLE second ADD COLUMN note text;
COMMIT;
`

// TestIntegrationMigrationRetriesDeadlock: a migration that deadlocks once with a replica (40P01) is rolled back and
// runs again once the replica committed; the schema ends at the new version, not dirty, and migration_retried records
// the retry.
func TestIntegrationMigrationRetriesDeadlock(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newRetryEnv(t, s, deadlockMigration)
		e.replicaExec(t, "SET deadlock_timeout = '1min'")
		e.replicaExec(t, "BEGIN")
		e.replicaExec(t, "LOCK TABLE second IN ACCESS EXCLUSIVE MODE")
		locked := make(chan error, 1)
		e.release = func() {
			// The replica takes first once the deadlock ended the migration's transaction.
			if err := <-locked; err != nil {
				t.Errorf("the replica's lock of first: %v", err)
			}
			e.replicaCommit(t)
		}
		pid := e.session.PgConn().PID()
		done := make(chan error, 1)
		go func() { done <- e.mm.Up() }()
		e.waitForLock(t, pid, done)
		go func() {
			_, err := e.replica.Exec(t.Context(), "LOCK TABLE first IN ACCESS EXCLUSIVE MODE")
			locked <- err
		}()
		if err := <-done; err != nil {
			t.Fatalf("Up: %v", err)
		}
		e.checkVersion2(t, false)
		if len(e.waits) != 1 {
			t.Errorf("waited %v, want one wait", e.waits)
		}
		want := `"level":"WARN","event":"migration_retried","version":2,"attempt":1,"sqlstate":"40P01"`
		if !strings.Contains(e.out.String(), want) || strings.Count(e.out.String(), "migration_retried") != 1 {
			t.Errorf("logged %s, want once %s", e.out.String(), want)
		}
	})
}

// TestIntegrationMigrationRetriesBounded: a lock that the replica never releases fails the migration after three
// attempts, waiting a second and then two between them; the schema stays dirty at the new version as before, with the
// committed first transaction applied once.
func TestIntegrationMigrationRetriesBounded(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newRetryEnv(t, s, lockTimeoutMigration)
		e.replicaExec(t, "BEGIN")
		e.replicaExec(t, "INSERT INTO second VALUES (1)")
		err := e.mm.Up()
		if err == nil || !strings.Contains(err.Error(), "SQLSTATE 55P03") {
			t.Fatalf("Up = %v, want the lock timeout", err)
		}
		e.checkVersion2(t, true)
		if len(e.waits) != 2 || e.waits[0] != time.Second || e.waits[1] != 2*time.Second {
			t.Errorf("waited %v, want 1s and 2s", e.waits)
		}
		if n := strings.Count(e.out.String(), `"event":"migration_retried"`); n != 2 {
			t.Errorf("logged %d retries, want 2:\n%s", n, e.out.String())
		}
		if n := e.count(t, "SELECT count(*) FROM information_schema.tables WHERE table_name = 'third'"); n != 1 {
			t.Errorf("third exists %d times", n)
		}
	})
}

// TestIntegrationMigrationNotRetried: an error other than a deadlock or a lock timeout fails the migration at once.
func TestIntegrationMigrationNotRetried(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := newRetryEnv(t, s, "BEGIN;\nCREATE TABLE first (id int);\nCOMMIT;\n")
		err := e.mm.Up()
		if err == nil || !strings.Contains(err.Error(), "SQLSTATE 42P07") {
			t.Fatalf("Up = %v, want relation already exists", err)
		}
		e.checkVersion2(t, true)
		if len(e.waits) != 0 || strings.Contains(e.out.String(), "migration_retried") {
			t.Errorf("waited %v, logged %s", e.waits, e.out.String())
		}
	})
}
