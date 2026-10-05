// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package db holds the database connections (ADR-0006): the main pool, for every query, and the session connection,
// for what needs a real session — the migration lock here, the Leader lock and LISTEN later. It runs the startup
// checks of C-02.FR-5 and FR-14 and the embedded migrations.
package db

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

const (
	// connectTimeout bounds one attempt to open a connection.
	connectTimeout = 10 * time.Second
	// pingTimeout bounds the readiness check (C-02.FR-4).
	pingTimeout = time.Second
)

// The names of the two connections in log lines.
const (
	ConnectionMain    = "main"
	ConnectionSession = "session"
)

// DB is the main pool and the settings of the session connection.
type DB struct {
	Pool *pgxpool.Pool

	main, session config.Database
	sessionConfig *pgx.ConnConfig

	// pool, connect and newMigrator reach the database: Pool, ConnectSession and golang-migrate, or fakes in tests.
	pool        querier
	connect     func(context.Context) (conn, error)
	newMigrator func(context.Context, conn) (migrator, error)
}

// conn is a session connection that the caller closes.
type conn interface {
	sessionConn
	Close(ctx context.Context) error
}

// migrator is what Migrate needs of golang-migrate.
type migrator interface {
	Version() (version uint, dirty bool, err error)
	Up() error
	Close() (source, database error)
}

// Open prepares the main pool and the session settings without connecting; Check connects.
func Open(ctx context.Context, main, session config.Database) (*DB, error) {
	poolConfig, err := pgxpool.ParseConfig(string(main.URL))
	if err != nil {
		return nil, fmt.Errorf("the main database connection: %w", err)
	}
	onlyFromURL(poolConfig.ConnConfig, main.URL)
	sessionConfig, err := pgx.ParseConfig(string(session.URL))
	if err != nil {
		return nil, fmt.Errorf("the session database connection: %w", err)
	}
	onlyFromURL(sessionConfig, session.URL)
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("the main database connection: %w", err)
	}
	d := &DB{Pool: pool, main: main, session: session, sessionConfig: sessionConfig, pool: pool}
	d.connect = func(ctx context.Context) (conn, error) { return d.ConnectSession(ctx) }
	d.newMigrator = newMigrate
	return d, nil
}

// onlyFromURL undoes what pgx took from the PG* variables and the password file rather than from the URL: the
// password, the connect timeout and the application name (ADR-0010: only MUSTER_* variables are read).
func onlyFromURL(c *pgx.ConnConfig, raw logging.Secret) {
	u, err := url.Parse(string(raw))
	if err != nil {
		return // pgx parsed it, so it is a URL
	}
	c.Password, _ = u.User.Password()
	c.ConnectTimeout = connectTimeout
	if s := u.Query().Get("connect_timeout"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			c.ConnectTimeout = time.Duration(n) * time.Second
		}
	}
	if name := u.Query().Get("application_name"); name != "" {
		c.RuntimeParams["application_name"] = name
	} else {
		delete(c.RuntimeParams, "application_name")
	}
}

// Close closes the pool.
func (d *DB) Close() {
	d.Pool.Close()
}

// ConnectSession opens a new session connection; the caller closes it.
func (d *DB) ConnectSession(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, d.sessionConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("connect to the database (session connection): %w", err)
	}
	return conn, nil
}

// Ping runs SELECT 1 on the main pool, within a second: the readiness check.
func (d *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if _, err := d.pool.Exec(ctx, "SELECT 1"); err != nil {
		return fmt.Errorf("the database does not answer: %w", err)
	}
	return nil
}

var (
	metricsOnce sync.Once
	// metricsPool is the pool the metrics read: the one most recently given to RegisterMetrics.
	metricsPool atomic.Pointer[pgxpool.Pool]
)

// RegisterMetrics exports the metrics of the main pool, read at every scrape.
func (d *DB) RegisterMetrics() {
	metricsPool.Store(d.Pool)
	metricsOnce.Do(func() {
		stat := func(f func(*pgxpool.Stat) float64) func() float64 {
			return func() float64 {
				p := metricsPool.Load()
				if p == nil {
					return 0
				}
				return f(p.Stat())
			}
		}
		metrics.DBPoolConnections.Func(stat(func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) }), "acquired")
		metrics.DBPoolConnections.Func(stat(func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) }), "idle")
		metrics.DBPoolConnections.Func(stat(func(s *pgxpool.Stat) float64 {
			return float64(s.ConstructingConns())
		}), "constructing")
		metrics.DBPoolMaxConnections.Func(stat(func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) }))
		metrics.DBPoolAcquires.Func(stat(func(s *pgxpool.Stat) float64 { return float64(s.AcquireCount()) }))
		metrics.DBPoolAcquireWait.Func(stat(func(s *pgxpool.Stat) float64 { return s.AcquireDuration().Seconds() }))
	})
}

// closeQuietly closes a session connection whatever the state of ctx.
func closeQuietly(ctx context.Context, c conn) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), connectTimeout)
	defer cancel()
	_ = c.Close(ctx)
}
