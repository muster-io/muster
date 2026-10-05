// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/logging"
)

// The migrations in golang-migrate format, embedded in the binary; the same directory is the schema sqlc reads.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationsDir = "migrations"

// migrationsTable is golang-migrate's table of the schema version, in the current schema.
const migrationsTable = "schema_migrations"

// KnownVersion is the version of the newest embedded migration.
func KnownVersion() (uint, error) {
	return newestVersion(migrationFiles, migrationsDir)
}

func newestVersion(fsys fs.FS, dir string) (uint, error) {
	src, err := iofs.New(fsys, dir)
	if err != nil {
		return 0, fmt.Errorf("read the embedded migrations: %w", err)
	}
	defer src.Close()
	return lastVersion(src)
}

func lastVersion(src source.Driver) (uint, error) {
	v, err := src.First()
	if err != nil {
		return 0, fmt.Errorf("read the embedded migrations: %w", err)
	}
	for {
		next, err := src.Next(v)
		if errors.Is(err, fs.ErrNotExist) {
			return v, nil
		}
		if err != nil {
			return 0, fmt.Errorf("read the embedded migrations: %w", err)
		}
		v = next
	}
}

// SchemaError is a schema the binary refuses to run on.
type SchemaError struct {
	Event    logging.Event
	Database uint
	Known    uint
	msg      string
}

func (e *SchemaError) Error() string { return e.msg }

// checkSchema refuses a dirty schema and one newer than known; without migrating, it also refuses one older than
// known. It logs the refusal as its event.
func checkSchema(ctx context.Context, log *logging.Logger, version uint, dirty bool, known uint, migrating bool) error {
	var e *SchemaError
	switch {
	case dirty:
		e = &SchemaError{Event: logging.SchemaDirty, Database: version, Known: known, msg: fmt.Sprintf(
			"the database schema is dirty at version %d: a migration failed halfway; repair the schema, then clear "+
				"the dirty flag in %s", version, migrationsTable)}
		log.Log(ctx, e.Event, logging.F("database_version", version))
		return e
	case version > known:
		e = &SchemaError{Event: logging.SchemaTooNew, Database: version, Known: known, msg: fmt.Sprintf(
			"database schema version %d is newer than this binary knows (%d)", version, known)}
	case version < known && !migrating:
		e = &SchemaError{Event: logging.SchemaTooOld, Database: version, Known: known, msg: fmt.Sprintf(
			"database schema version %d is older than this binary needs (%d): run muster migrate or set "+
				"MUSTER_MIGRATE_ON_START=true", version, known)}
	default:
		return nil
	}
	log.Log(ctx, e.Event, logging.F("database_version", version), logging.F("known_version", known))
	return e
}

// CheckSchema reads the schema version over the main pool and refuses to run on a schema that is dirty, newer than
// the binary knows or, since the server did not migrate, older.
func (d *DB) CheckSchema(ctx context.Context, log *logging.Logger) error {
	known, err := KnownVersion()
	if err != nil {
		return err
	}
	version, dirty, err := schemaVersion(ctx, d.pool)
	if err != nil {
		return err
	}
	return checkSchema(ctx, log, version, dirty, known, false)
}

// schemaVersion reads the version golang-migrate recorded; a database without the table or the row has version 0.
func schemaVersion(ctx context.Context, q querier) (uint, bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", migrationsTable).Scan(&exists); err != nil {
		return 0, false, fmt.Errorf("read the schema version: %w", err)
	}
	if !exists {
		return 0, false, nil
	}
	var (
		version int64
		dirty   bool
	)
	err := q.QueryRow(ctx, "SELECT version, dirty FROM "+migrationsTable+" LIMIT 1").Scan(&version, &dirty)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read the schema version: %w", err)
	}
	if version < 0 {
		return 0, dirty, nil
	}
	return uint(version), dirty, nil
}

// Migrate applies the embedded migrations over a session connection that holds the migration advisory lock, and
// logs the versions applied. It refuses a dirty schema and one newer than the binary knows, naming both versions.
func (d *DB) Migrate(ctx context.Context, log *logging.Logger) error {
	return d.locked(ctx, func(ctx context.Context, c conn) error {
		return d.migrate(ctx, log, c)
	})
}

// WithMigrationLock runs f while this process holds the migration advisory lock on a session connection; it waits
// while another process holds it.
func (d *DB) WithMigrationLock(ctx context.Context, f func(context.Context) error) error {
	return d.locked(ctx, func(ctx context.Context, _ conn) error { return f(ctx) })
}

func (d *DB) locked(ctx context.Context, f func(context.Context, conn) error) error {
	c, err := d.connect(ctx)
	if err != nil {
		return err
	}
	defer closeQuietly(ctx, c)
	if _, err := c.Exec(ctx, "SELECT pg_advisory_lock($1)", MigrationLockKey); err != nil {
		return fmt.Errorf("take the migration lock: %w", err)
	}
	ferr := f(ctx, c)
	// The lock goes with the session anyway; releasing it lets a waiting replica go on at once.
	if _, err := c.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", MigrationLockKey); err != nil &&
		ferr == nil {
		return fmt.Errorf("release the migration lock: %w", err)
	}
	return ferr
}

func (d *DB) migrate(ctx context.Context, log *logging.Logger, c conn) error {
	known, err := KnownVersion()
	if err != nil {
		return err
	}
	m, err := d.newMigrator(ctx, c)
	if err != nil {
		return err
	}
	defer m.Close()
	from, dirty, err := version(m)
	if err != nil {
		return err
	}
	if err := checkSchema(ctx, log, from, dirty, known, true); err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply the migrations from version %d: %w", from, err)
	}
	to, _, err := version(m)
	if err != nil {
		return err
	}
	if to == from {
		log.Log(ctx, logging.MigrationsCurrent, logging.F("version", to))
		return nil
	}
	log.Log(ctx, logging.MigrationsApplied, logging.F("from", from), logging.F("to", to))
	return nil
}

// newMigrate is golang-migrate with the embedded migrations, run over c, which holds the migration lock.
func newMigrate(ctx context.Context, c conn) (migrator, error) {
	src, err := iofs.New(migrationFiles, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read the embedded migrations: %w", err)
	}
	driver, err := newDriver(ctx, c)
	if err != nil {
		_ = src.Close()
		return nil, err
	}
	m, err := migrate.NewWithInstance("iofs", src, "muster", driver)
	if err != nil {
		_ = src.Close()
		return nil, fmt.Errorf("prepare the migrations: %w", err)
	}
	return m, nil
}

func version(m migrator) (uint, bool, error) {
	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read the schema version: %w", err)
	}
	return v, dirty, nil
}

// driver is golang-migrate's database driver on Muster's session connection, which already holds the migration
// advisory lock, so its own Lock and Unlock take nothing more. It keeps golang-migrate's version table.
type driver struct {
	ctx  context.Context // the context of the migration run; golang-migrate's driver calls take none
	conn conn
}

func newDriver(ctx context.Context, c conn) (*driver, error) {
	_, err := c.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+migrationsTable+
		" (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)")
	if err != nil {
		return nil, fmt.Errorf("create the %s table: %w", migrationsTable, err)
	}
	return &driver{ctx: ctx, conn: c}, nil
}

func (d *driver) Open(string) (database.Driver, error) {
	return nil, errors.New("the migration driver runs over a session connection and is not opened by URL")
}

// Close leaves the connection to its owner.
func (d *driver) Close() error { return nil }

func (d *driver) Lock() error { return nil }

func (d *driver) Unlock() error { return nil }

// Run executes one migration file as one simple-protocol request, so that the file's own BEGIN and COMMIT apply.
func (d *driver) Run(migration io.Reader) error {
	sql, err := io.ReadAll(migration)
	if err != nil {
		return err
	}
	if _, err := d.conn.Exec(d.ctx, string(sql)); err != nil {
		return fmt.Errorf("run the migration: %w", err)
	}
	return nil
}

// SetVersion replaces the recorded version in one implicit transaction; a dirty nil version is kept, so that a failed
// first down migration is not mistaken for an empty schema.
func (d *driver) SetVersion(version int, dirty bool) error {
	sql := "TRUNCATE " + migrationsTable
	if version >= 0 || (version == database.NilVersion && dirty) {
		sql += fmt.Sprintf("; INSERT INTO %s (version, dirty) VALUES (%d, %t)", migrationsTable, version, dirty)
	}
	if _, err := d.conn.Exec(d.ctx, sql); err != nil {
		return fmt.Errorf("record the schema version %d: %w", version, err)
	}
	return nil
}

func (d *driver) Version() (int, bool, error) {
	var (
		version int64
		dirty   bool
	)
	err := d.conn.QueryRow(d.ctx, "SELECT version, dirty FROM "+migrationsTable+" LIMIT 1").Scan(&version, &dirty)
	if errors.Is(err, pgx.ErrNoRows) {
		return database.NilVersion, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read the schema version: %w", err)
	}
	return int(version), dirty, nil
}

// Drop is never used: Muster never drops its schema.
func (d *driver) Drop() error {
	return errors.New("the migration driver does not drop the schema")
}
