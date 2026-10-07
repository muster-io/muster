// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package db

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/logging"
)

const (
	// MinServerVersion is PostgreSQL 14, the first with idle_session_timeout, which the Leader lease relies on
	// (ADR-0006, ADR-0007).
	MinServerVersion = 140000

	// sessionCheckChannel is the channel of the LISTEN/NOTIFY round trip.
	sessionCheckChannel = "muster_session_check"
)

// sessionCheckTimeout bounds the lock and LISTEN/NOTIFY round trip of the session check; tests shorten it.
var sessionCheckTimeout = 5 * time.Second

// The advisory lock keys of Muster, one per use; each is a constant so that every replica takes the same lock.
const (
	// MigrationLockKey is taken around migrations and, from S-007 on, the start-up steps that create the first data.
	MigrationLockKey int64 = 0x6d75_7374_6572_0001
	// sessionCheckLockKey is taken and released by the session check.
	sessionCheckLockKey int64 = 0x6d75_7374_6572_0002
)

// RouteMembershipLockClass is the first key of the transaction advisory locks of the Destinations of a Route, whose
// second key is hashint8 of the Route's id: a change of its Destinations takes it exclusively (routing), an Enqueue on
// the Route shared (delivery). The two-key advisory locks do not overlap the one-key locks above.
const RouteMembershipLockClass int32 = 0x6d75_0002

// querier is what the checks need of the main pool or of a connection.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// sessionConn is a session connection that receives notifications.
type sessionConn interface {
	querier
	WaitForNotification(ctx context.Context) (*pgconn.Notification, error)
}

// Check connects both connections and runs the startup checks: the server version on each, the session state of the
// session connection, then the TLS report of each as database_connection_security.
func (d *DB) Check(ctx context.Context, log *logging.Logger) error {
	if err := d.Ping(ctx); err != nil {
		return fmt.Errorf("connect to the database (main connection): %w", err)
	}
	if err := CheckVersion(ctx, d.pool); err != nil {
		return err
	}
	session, err := d.connect(ctx)
	if err != nil {
		return err
	}
	defer closeQuietly(ctx, session)
	if err := CheckVersion(ctx, session); err != nil {
		return err
	}
	if err := CheckSession(ctx, session, d.pool); err != nil {
		return err
	}
	for _, c := range []struct {
		name string
		q    querier
		mode string
	}{
		{ConnectionMain, d.pool, d.main.SSLMode},
		{ConnectionSession, session, d.session.SSLMode},
	} {
		encrypted, err := Encrypted(ctx, c.q)
		if err != nil {
			return fmt.Errorf("read the TLS state of the %s database connection: %w", c.name, err)
		}
		ReportSecurity(ctx, log, c.name, c.mode, encrypted)
	}
	return nil
}

// CheckVersion refuses a PostgreSQL older than MinServerVersion.
func CheckVersion(ctx context.Context, q querier) error {
	var s string
	if err := q.QueryRow(ctx, "SHOW server_version_num").Scan(&s); err != nil {
		return fmt.Errorf("read the PostgreSQL version: %w", err)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("read the PostgreSQL version: server_version_num is %q", s)
	}
	if n < MinServerVersion {
		return fmt.Errorf("PostgreSQL %d.x is not supported: Muster needs PostgreSQL 14 or newer", n/10000)
	}
	return nil
}

// PoolerError says that the session connection does not keep session state, probably because it goes through a
// transaction pooler.
type PoolerError struct {
	Err error
}

func (e *PoolerError) Error() string {
	return fmt.Sprintf("the session connection does not keep session state (advisory locks, LISTEN): %v; it probably "+
		"goes through a transaction pooler such as PgBouncer in transaction mode: set MUSTER_DATABASE_SESSION_URL "+
		"(or MUSTER_DATABASE_SESSION_HOST and MUSTER_DATABASE_SESSION_PORT) to reach PostgreSQL directly or through "+
		"a session pooler", e.Err)
}

func (e *PoolerError) Unwrap() error { return e.Err }

// CheckSession takes and releases a session advisory lock on session, then completes a LISTEN/NOTIFY round trip:
// session listens and notifier, another connection, notifies. It fails with a PoolerError when either does not work
// within five seconds. PgBouncer has no SQL that reports its pool mode, so the check is functional.
func CheckSession(ctx context.Context, session sessionConn, notifier querier) error {
	ctx, cancel := context.WithTimeout(ctx, sessionCheckTimeout)
	defer cancel()
	if err := checkSession(ctx, session, notifier); err != nil {
		return &PoolerError{Err: err}
	}
	return nil
}

func checkSession(ctx context.Context, session sessionConn, notifier querier) error {
	if _, err := session.Exec(ctx, "SELECT pg_advisory_lock($1)", sessionCheckLockKey); err != nil {
		return fmt.Errorf("take an advisory lock: %w", err)
	}
	var released bool
	if err := session.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", sessionCheckLockKey).Scan(&released); err != nil {
		return fmt.Errorf("release the advisory lock: %w", err)
	}
	if !released {
		return errors.New("the advisory lock taken by the connection was not held by its session when released")
	}
	if _, err := session.Exec(ctx, "LISTEN "+sessionCheckChannel); err != nil {
		return fmt.Errorf("LISTEN: %w", err)
	}
	payload := rand.Text()
	if _, err := notifier.Exec(ctx, "SELECT pg_notify($1, $2)", sessionCheckChannel, payload); err != nil {
		return fmt.Errorf("NOTIFY: %w", err)
	}
	for {
		n, err := session.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("no notification arrived within %v of NOTIFY", sessionCheckTimeout)
			}
			return fmt.Errorf("wait for the notification: %w", err)
		}
		if n.Channel == sessionCheckChannel && n.Payload == payload {
			break
		}
	}
	if _, err := session.Exec(ctx, "UNLISTEN "+sessionCheckChannel); err != nil {
		return fmt.Errorf("UNLISTEN: %w", err)
	}
	return nil
}

// Encrypted reports whether the connection behind q uses TLS, from pg_stat_ssl.
func Encrypted(ctx context.Context, q querier) (bool, error) {
	var ssl bool
	err := q.QueryRow(ctx, "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()").Scan(&ssl)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ssl, err
}

// ReportSecurity logs database_connection_security, at WARN when the connection is not encrypted.
func ReportSecurity(ctx context.Context, log *logging.Logger, connection, sslMode string, encrypted bool) {
	level := logging.LevelInfo
	if !encrypted {
		level = logging.LevelWarn
	}
	log.LogAt(ctx, logging.DatabaseConnectionSecurity, level, logging.F("connection", connection),
		logging.F("sslmode", sslMode), logging.F("encrypted", encrypted))
}
