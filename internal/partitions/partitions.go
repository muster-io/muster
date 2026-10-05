// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package partitions maintains the partitions of the five partitioned tables (C-02.FR-11, design/db/schema.md §6): it
// creates the partitions Muster will need ahead of time, at startup before serving and hourly on the Leader, and
// drops a partition whose whole range is older than the Organization's retention period. Partition maintenance is
// runtime work, never a migration, and is safe to run twice: it creates only what is missing and drops only what
// exists.
package partitions

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/partitions/dbgen"
)

const (
	// DaysAhead and MonthsAhead are how far ahead partitions are created: daily partitions for today and the next 7
	// days, monthly partitions for this and the next 2 months.
	DaysAhead   = 7
	MonthsAhead = 2

	// lockTimeout bounds the wait for a table lock, so that maintenance fails and is retried at the next run instead
	// of queueing behind a long transaction, with every writer of the table queued behind it.
	lockTimeout = "2s"

	// maintenanceLockKey is the session advisory lock that serialises partition maintenance across replicas, so that
	// two overlapping Leaders, or a starting replica and the Leader, take turns. It follows the keys of internal/db.
	maintenanceLockKey int64 = 0x6d75_7374_6572_0004

	closeTimeout = 5 * time.Second
	day          = 24 * time.Hour
)

// period is how a table is partitioned.
type period int

const (
	daily period = iota + 1
	monthly
)

// retention selects the retention period, in days, that applies to a table.
type retention func(dbgen.ListRetentionRow) int64

// table is one partitioned table.
type table struct {
	name   string
	period period
	// dateBounds is set for a table partitioned on a date column, whose bounds are dates.
	dateBounds bool
	retention  retention
}

// tables are the partitioned tables of the schema, with the retention period of each.
var tables = []table{
	{name: "stored_snapshots", period: daily,
		retention: func(r dbgen.ListRetentionRow) int64 { return r.RetentionStoredSnapshotsDays }},
	{name: "snapshot_bodies", period: daily, dateBounds: true,
		retention: func(r dbgen.ListRetentionRow) int64 { return r.RetentionStoredSnapshotsDays }},
	{name: "timeline_entries", period: monthly,
		retention: func(r dbgen.ListRetentionRow) int64 { return r.RetentionAlertDetailsDays }},
	{name: "delivery_events", period: monthly,
		retention: func(r dbgen.ListRetentionRow) int64 { return r.RetentionAlertDetailsDays }},
	{name: "audit_log", period: monthly,
		retention: func(r dbgen.ListRetentionRow) int64 { return r.RetentionAuditLogDays }},
}

// Session is a session connection. Maintenance sets lock_timeout and holds an advisory lock for the life of the
// connection, which a transaction pooler would not keep, and DETACH CONCURRENTLY runs outside a transaction block.
// *pgx.Conn implements it.
type Session interface {
	dbgen.DBTX
	Close(ctx context.Context) error
}

// Store is the queries of maintenance; *dbgen.Queries implements it.
type Store interface {
	ListPartitions(ctx context.Context, parent string) ([]dbgen.ListPartitionsRow, error)
	ListDetachedPartitions(ctx context.Context, prefix string) ([]string, error)
	ListRetention(ctx context.Context) ([]dbgen.ListRetentionRow, error)
}

// Maintainer creates and drops partitions over a session connection it opens for each run.
type Maintainer struct {
	connect  func(context.Context) (Session, error)
	business clock.Clock
	log      *logging.Logger
	newStore func(dbgen.DBTX) Store
}

// New returns a Maintainer that opens its session connections with connect and reads the date from the business
// clock, which retention follows. connect bounds each session like the Leader's lock session, so that the advisory
// lock of a frozen or cut-off process goes within leader.server_bound.
func New(connect func(context.Context) (Session, error), business clock.Clock, log *logging.Logger) *Maintainer {
	return &Maintainer{connect: connect, business: business, log: log,
		newStore: func(db dbgen.DBTX) Store { return dbgen.New(db) }}
}

// Create creates the partitions that are missing, as the start-up step before serving; it drops nothing. When none is
// missing it takes no lock, so a start never waits for the Leader's run.
func (m *Maintainer) Create(ctx context.Context) error {
	return m.run(ctx, false)
}

// Maintain creates the partitions that are missing and drops those past their retention period, as the hourly Leader
// task. A failure is logged as partition_maintenance_failed and returned; the next run retries.
func (m *Maintainer) Maintain(ctx context.Context) error {
	err := m.run(ctx, true)
	if err != nil && ctx.Err() == nil {
		m.log.Log(ctx, logging.PartitionMaintenanceFailed, logging.F("error", err.Error()))
	}
	return err
}

func (m *Maintainer) run(ctx context.Context, drop bool) (err error) {
	s, err := m.connect(ctx)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		// Closing the session also releases the advisory lock.
		_ = s.Close(closeCtx)
	}()
	store := m.newStore(s)
	now := m.business.Now().UTC()
	if !drop {
		missing, err := anyMissing(ctx, store, now)
		if err != nil || !missing {
			return err
		}
	}
	if err := lock(ctx, s, drop); err != nil {
		return err
	}
	var created, dropped []string
	defer func() {
		if len(created) > 0 || len(dropped) > 0 {
			m.log.Log(ctx, logging.PartitionsMaintained, logging.F("created", nonNil(created)),
				logging.F("dropped", nonNil(dropped)))
		}
	}()
	var periods []dbgen.ListRetentionRow
	if drop {
		if periods, err = store.ListRetention(ctx); err != nil {
			return fmt.Errorf("read the retention periods: %w", err)
		}
	}
	for _, t := range tables {
		existing, err := store.ListPartitions(ctx, t.name)
		if err != nil {
			return fmt.Errorf("list the partitions of %s: %w", t.name, err)
		}
		names, err := createMissing(ctx, s, t, existing, now)
		created = append(created, names...)
		if err != nil {
			return err
		}
		if keep, ok := longest(periods, t.retention); ok {
			detached, err := store.ListDetachedPartitions(ctx, t.name+"_p")
			if err != nil {
				return fmt.Errorf("list the detached partitions of %s: %w", t.name, err)
			}
			names, err := dropExpired(ctx, s, t, existing, detached, now.Add(-keep))
			dropped = append(dropped, names...)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// lock takes the maintenance lock and sets lock_timeout. The Leader's run waits for the lock at most lock_timeout and
// is retried at the next run; a starting replica that misses partitions waits for the lock, then lock_timeout
// applies to the DDL.
func lock(ctx context.Context, s Session, leader bool) error {
	setTimeout := func() error {
		if _, err := s.Exec(ctx, "SET lock_timeout = '"+lockTimeout+"'"); err != nil {
			return fmt.Errorf("set lock_timeout: %w", err)
		}
		return nil
	}
	take := func() error {
		if _, err := s.Exec(ctx, "SELECT pg_advisory_lock($1)", maintenanceLockKey); err != nil {
			return fmt.Errorf("take the partition maintenance lock: %w", err)
		}
		return nil
	}
	if leader {
		if err := setTimeout(); err != nil {
			return err
		}
		return take()
	}
	if err := take(); err != nil {
		return err
	}
	return setTimeout()
}

// anyMissing reports whether a partition that must exist at now is missing.
func anyMissing(ctx context.Context, store Store, now time.Time) (bool, error) {
	for _, t := range tables {
		existing, err := store.ListPartitions(ctx, t.name)
		if err != nil {
			return false, fmt.Errorf("list the partitions of %s: %w", t.name, err)
		}
		for _, p := range wanted(t, now) {
			if !slices.ContainsFunc(existing, func(e dbgen.ListPartitionsRow) bool { return e.Name == p.name }) {
				return true, nil
			}
		}
	}
	return false, nil
}

func nonNil(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

// partition is one partition of a table: its name and its range [from, to).
type partition struct {
	name     string
	from, to time.Time
}

// wanted are the partitions of t that must exist at now: today and the next DaysAhead days, or this and the next
// MonthsAhead months, in UTC.
func wanted(t table, now time.Time) []partition {
	now = now.UTC()
	var out []partition
	switch t.period {
	case daily:
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		for i := range DaysAhead + 1 {
			from := start.AddDate(0, 0, i)
			out = append(out, partition{name: dailyName(t.name, from), from: from, to: from.AddDate(0, 0, 1)})
		}
	case monthly:
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		for i := range MonthsAhead + 1 {
			from := start.AddDate(0, i, 0)
			out = append(out, partition{name: monthlyName(t.name, from), from: from, to: from.AddDate(0, 1, 0)})
		}
	}
	return out
}

func dailyName(table string, from time.Time) string { return table + "_p" + from.Format("20060102") }

func monthlyName(table string, from time.Time) string { return table + "_p" + from.Format("200601") }

// parse reads the range of a partition of t from its name; ok is false for a name Muster did not give.
func parse(t table, name string) (partition, bool) {
	suffix, found := strings.CutPrefix(name, t.name+"_p")
	if !found {
		return partition{}, false
	}
	switch t.period {
	case daily:
		from, err := time.Parse("20060102", suffix)
		if err != nil || len(suffix) != len("20060102") {
			return partition{}, false
		}
		return partition{name: name, from: from, to: from.AddDate(0, 0, 1)}, true
	case monthly:
		from, err := time.Parse("200601", suffix)
		if err != nil || len(suffix) != len("200601") {
			return partition{}, false
		}
		return partition{name: name, from: from, to: from.AddDate(0, 1, 0)}, true
	}
	return partition{}, false
}

func createMissing(ctx context.Context, s Session, t table, existing []dbgen.ListPartitionsRow, now time.Time,
) ([]string, error) {
	var created []string
	for _, p := range wanted(t, now) {
		if slices.ContainsFunc(existing, func(e dbgen.ListPartitionsRow) bool { return e.Name == p.name }) {
			continue
		}
		if _, err := s.Exec(ctx, createSQL(t, p)); err != nil {
			return created, fmt.Errorf("create the partition %s: %w", p.name, err)
		}
		created = append(created, p.name)
	}
	return created, nil
}

func createSQL(t table, p partition) string {
	layout := "2006-01-02 15:04:05-07"
	if t.dateBounds {
		layout = "2006-01-02"
	}
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
		ident(p.name), ident(t.name), p.from.Format(layout), p.to.Format(layout))
}

// longest is the longest retention period of any Organization for a table; ok is false without an Organization.
// Partitions are shared by the Organizations, so a partition goes only when every Organization is done with it.
func longest(periods []dbgen.ListRetentionRow, r retention) (time.Duration, bool) {
	if len(periods) == 0 {
		return 0, false
	}
	days := r(slices.MaxFunc(periods, func(a, b dbgen.ListRetentionRow) int { return cmp.Compare(r(a), r(b)) }))
	return time.Duration(days) * day, true
}

// dropExpired detaches without blocking writers, then drops, every partition of t whose whole range ends at or
// before cutoff. A detach that an earlier run started and could not finish is finalized instead, and a partition that
// an earlier run detached and could not drop is dropped.
func dropExpired(ctx context.Context, s Session, t table, existing []dbgen.ListPartitionsRow, detached []string,
	cutoff time.Time,
) ([]string, error) {
	var dropped []string
	for _, name := range detached {
		p, ok := parse(t, name)
		if !ok || p.to.After(cutoff) {
			continue
		}
		if _, err := s.Exec(ctx, "DROP TABLE IF EXISTS "+ident(p.name)); err != nil {
			return dropped, fmt.Errorf("drop the detached partition %s: %w", p.name, err)
		}
		dropped = append(dropped, p.name)
	}
	for _, e := range existing {
		p, ok := parse(t, e.Name)
		if !ok || p.to.After(cutoff) {
			continue
		}
		mode := "CONCURRENTLY"
		if e.DetachPending {
			mode = "FINALIZE"
		}
		if _, err := s.Exec(ctx, fmt.Sprintf("ALTER TABLE %s DETACH PARTITION %s %s", ident(t.name), ident(p.name),
			mode)); err != nil {
			return dropped, fmt.Errorf("detach the partition %s: %w", p.name, err)
		}
		if _, err := s.Exec(ctx, "DROP TABLE IF EXISTS "+ident(p.name)); err != nil {
			return dropped, fmt.Errorf("drop the partition %s: %w", p.name, err)
		}
		dropped = append(dropped, p.name)
	}
	return dropped, nil
}

func ident(name string) string {
	return pgx.Identifier{name}.Sanitize()
}
