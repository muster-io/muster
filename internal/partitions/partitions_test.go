// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package partitions

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/partitions/dbgen"
)

// fakeDB is a session and a store over in-memory partitions: Exec applies CREATE, DETACH and DROP to them.
type fakeDB struct {
	mu         sync.Mutex
	partitions map[string][]dbgen.ListPartitionsRow
	// detached are the tables that were detached and not dropped.
	detached  []string
	retention []dbgen.ListRetentionRow
	execs     []string
	closed    int
	// failOn fails an Exec whose statement contains it; listErr, detachedErr and retentionErr fail the queries.
	failOn                             string
	listErr, detachedErr, retentionErr error
}

func newFakeDB() *fakeDB {
	return &fakeDB{partitions: map[string][]dbgen.ListPartitionsRow{}}
}

func (f *fakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, sql)
	if f.failOn != "" && strings.Contains(sql, f.failOn) {
		return pgconn.CommandTag{}, errors.New("canceling statement due to lock timeout")
	}
	fields := strings.Fields(strings.ReplaceAll(sql, `"`, ""))
	switch {
	case strings.HasPrefix(sql, "CREATE TABLE IF NOT EXISTS"):
		name, parent := fields[5], fields[8]
		if !slices.ContainsFunc(f.partitions[parent], func(r dbgen.ListPartitionsRow) bool { return r.Name == name }) {
			f.partitions[parent] = append(f.partitions[parent], dbgen.ListPartitionsRow{Name: name})
		}
	case strings.HasPrefix(sql, "ALTER TABLE"):
		parent, name := fields[2], fields[5]
		f.partitions[parent] = slices.DeleteFunc(f.partitions[parent],
			func(r dbgen.ListPartitionsRow) bool { return r.Name == name })
		f.detached = append(f.detached, name)
	case strings.HasPrefix(sql, "DROP TABLE IF EXISTS"):
		f.detached = slices.DeleteFunc(f.detached, func(name string) bool { return name == fields[4] })
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("the store is faked")
}

func (f *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (f *fakeDB) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakeDB) ListPartitions(_ context.Context, parent string) ([]dbgen.ListPartitionsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return slices.Clone(f.partitions[parent]), nil
}

func (f *fakeDB) ListDetachedPartitions(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detachedErr != nil {
		return nil, f.detachedErr
	}
	var out []string
	for _, name := range f.detached {
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out, nil
}

func (f *fakeDB) ListRetention(context.Context) ([]dbgen.ListRetentionRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.retention, f.retentionErr
}

func (f *fakeDB) names(parent string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.partitions[parent] {
		out = append(out, r.Name)
	}
	slices.Sort(out)
	return out
}

func newMaintainer(f *fakeDB, now time.Time, log *bytes.Buffer) *Maintainer {
	m := New(func(context.Context) (Session, error) { return f, nil }, clock.NewManual(now),
		logging.New(log, logging.LevelInfo))
	m.newStore = func(dbgen.DBTX) Store { return f }
	return m
}

var defaultRetention = dbgen.ListRetentionRow{ID: 1, RetentionStoredSnapshotsDays: 14, RetentionAlertDetailsDays: 90,
	RetentionAuditLogDays: 365}

func TestCreate(t *testing.T) {
	f := newFakeDB()
	var log bytes.Buffer
	now := time.Date(2026, 12, 30, 23, 30, 0, 0, time.FixedZone("UTC+3", 3*3600)) // 20:30 UTC
	m := newMaintainer(f, now, &log)
	if err := m.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	wantDaily := []string{"20261230", "20261231", "20270101", "20270102", "20270103", "20270104", "20270105", "20270106"}
	for _, table := range []string{"stored_snapshots", "snapshot_bodies"} {
		var want []string
		for _, d := range wantDaily {
			want = append(want, table+"_p"+d)
		}
		if got := f.names(table); !slices.Equal(got, want) {
			t.Errorf("%s partitions = %v, want %v", table, got, want)
		}
	}
	for _, table := range []string{"timeline_entries", "delivery_events", "audit_log"} {
		want := []string{table + "_p202612", table + "_p202701", table + "_p202702"}
		if got := f.names(table); !slices.Equal(got, want) {
			t.Errorf("%s partitions = %v, want %v", table, got, want)
		}
	}
	for _, want := range []string{
		`CREATE TABLE IF NOT EXISTS "stored_snapshots_p20261230" PARTITION OF "stored_snapshots" FOR VALUES ` +
			`FROM ('2026-12-30 00:00:00+00') TO ('2026-12-31 00:00:00+00')`,
		`CREATE TABLE IF NOT EXISTS "snapshot_bodies_p20261231" PARTITION OF "snapshot_bodies" FOR VALUES ` +
			`FROM ('2026-12-31') TO ('2027-01-01')`,
		`CREATE TABLE IF NOT EXISTS "audit_log_p202702" PARTITION OF "audit_log" FOR VALUES ` +
			`FROM ('2027-02-01 00:00:00+00') TO ('2027-03-01 00:00:00+00')`,
	} {
		if !slices.Contains(f.execs, want) {
			t.Errorf("no statement %s in %v", want, f.execs)
		}
	}
	if f.execs[0] != "SELECT pg_advisory_lock($1)" || f.execs[1] != "SET lock_timeout = '2s'" {
		t.Errorf("the start-up run starts with %v, want the advisory lock and then lock_timeout", f.execs[:2])
	}
	if f.closed != 1 {
		t.Errorf("the session was closed %d times, want 1", f.closed)
	}
	if !strings.Contains(log.String(), `"event":"partitions_maintained"`) ||
		!strings.Contains(log.String(), `"dropped":[]`) {
		t.Errorf("log %s", log.String())
	}

	// A second start finds every partition and takes no lock; it creates nothing and logs nothing.
	f.execs, f.closed = nil, 0
	log.Reset()
	if err := m.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 0 || log.Len() != 0 || f.closed != 1 {
		t.Errorf("second run: statements %v, log %s, closed %d", f.execs, log.String(), f.closed)
	}
}

func TestMaintainDropsExpiredPartitions(t *testing.T) {
	f := newFakeDB()
	f.retention = []dbgen.ListRetentionRow{defaultRetention}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	f.partitions["stored_snapshots"] = []dbgen.ListPartitionsRow{
		{Name: "stored_snapshots_p20260920"}, // ends 2026-09-21, the cutoff: dropped
		{Name: "stored_snapshots_p20260921"}, // ends 2026-09-22, after the cutoff: kept
		{Name: "stored_snapshots_p20260901", DetachPending: true},
		{Name: "stored_snapshots_manual"}, // not named by Muster: kept
	}
	f.partitions["snapshot_bodies"] = []dbgen.ListPartitionsRow{{Name: "snapshot_bodies_p20260920"}}
	f.partitions["timeline_entries"] = []dbgen.ListPartitionsRow{
		{Name: "timeline_entries_p202606"}, // ends 2026-07-01, 96 days ago: dropped
		{Name: "timeline_entries_p202607"}, // ends 2026-08-01, 65 days ago: kept
	}
	f.partitions["audit_log"] = []dbgen.ListPartitionsRow{
		{Name: "audit_log_p202509"}, // ends 2025-10-01, more than a year ago: dropped
		{Name: "audit_log_p202510"}, // ends 2025-11-01: kept
	}
	var log bytes.Buffer
	m := newMaintainer(f, now, &log)
	if err := m.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	for table, gone := range map[string][]string{
		"stored_snapshots": {"stored_snapshots_p20260920", "stored_snapshots_p20260901"},
		"snapshot_bodies":  {"snapshot_bodies_p20260920"},
		"timeline_entries": {"timeline_entries_p202606"},
		"audit_log":        {"audit_log_p202509"},
	} {
		names := f.names(table)
		for _, g := range gone {
			if slices.Contains(names, g) {
				t.Errorf("%s still has %s: %v", table, g, names)
			}
		}
	}
	for table, kept := range map[string][]string{
		"stored_snapshots": {"stored_snapshots_p20260921", "stored_snapshots_manual", "stored_snapshots_p20261005"},
		"timeline_entries": {"timeline_entries_p202607", "timeline_entries_p202610"},
		"audit_log":        {"audit_log_p202510"},
	} {
		names := f.names(table)
		for _, k := range kept {
			if !slices.Contains(names, k) {
				t.Errorf("%s lost %s: %v", table, k, names)
			}
		}
	}
	for _, want := range []string{
		`ALTER TABLE "stored_snapshots" DETACH PARTITION "stored_snapshots_p20260920" CONCURRENTLY`,
		`DROP TABLE IF EXISTS "stored_snapshots_p20260920"`,
		`ALTER TABLE "stored_snapshots" DETACH PARTITION "stored_snapshots_p20260901" FINALIZE`,
	} {
		if !slices.Contains(f.execs, want) {
			t.Errorf("no statement %s", want)
		}
	}
	if !strings.Contains(log.String(), `"timeline_entries_p202606"`) {
		t.Errorf("log %s", log.String())
	}
	if f.execs[0] != "SET lock_timeout = '2s'" || f.execs[1] != "SELECT pg_advisory_lock($1)" {
		t.Errorf("the Leader's run starts with %v, want lock_timeout and then the advisory lock", f.execs[:2])
	}

	// A second run, as an overlapping Leader would make it, drops nothing more.
	f.execs = nil
	log.Reset()
	if err := m.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 2 || log.Len() != 0 {
		t.Errorf("second run: statements %v, log %s", f.execs, log.String())
	}
}

// TestMaintainDropsWhatAnEarlierRunDetached: a drop that failed after its detach is retried from the detached
// tables at the next run.
func TestMaintainDropsWhatAnEarlierRunDetached(t *testing.T) {
	f := newFakeDB()
	f.retention = []dbgen.ListRetentionRow{defaultRetention}
	f.partitions["audit_log"] = []dbgen.ListPartitionsRow{{Name: "audit_log_p202001"}}
	f.detached = []string{"audit_log_p202002", "audit_log_p209901", "audit_log_pnotmuster"}
	f.failOn = "DROP TABLE IF EXISTS \"audit_log_p202001\""
	m := newMaintainer(f, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), &bytes.Buffer{})
	if err := m.Maintain(t.Context()); err == nil {
		t.Fatal("the failed drop was not reported")
	}
	f.failOn = ""
	if err := m.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"audit_log_p209901", "audit_log_pnotmuster"}; !slices.Equal(f.detached, want) {
		t.Errorf("detached tables %v, want %v", f.detached, want)
	}
}

func TestMaintainKeepsWhatAnyOrganizationKeeps(t *testing.T) {
	f := newFakeDB()
	longer := defaultRetention
	longer.ID, longer.RetentionAlertDetailsDays = 2, 200
	f.retention = []dbgen.ListRetentionRow{defaultRetention, longer}
	f.partitions["delivery_events"] = []dbgen.ListPartitionsRow{{Name: "delivery_events_p202606"}}
	m := newMaintainer(f, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), &bytes.Buffer{})
	if err := m.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.names("delivery_events"), "delivery_events_p202606") {
		t.Error("a partition that one Organization still keeps was dropped")
	}

	// Without an Organization there is no retention period, and nothing is dropped.
	f.retention = nil
	if err := m.Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.names("delivery_events"), "delivery_events_p202606") {
		t.Error("a partition was dropped without an Organization")
	}
}

func TestCreateFailures(t *testing.T) {
	f := newFakeDB()
	f.listErr = errors.New("boom")
	m := newMaintainer(f, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), &bytes.Buffer{})
	if err := m.Create(t.Context()); err == nil || !strings.Contains(err.Error(), "list the partitions") {
		t.Errorf("Create = %v", err)
	}
	f.listErr, f.failOn = nil, "lock_timeout"
	if err := m.Create(t.Context()); err == nil || !strings.Contains(err.Error(), "set lock_timeout") {
		t.Errorf("Create = %v", err)
	}
	f.failOn = "pg_advisory_lock"
	if err := m.Create(t.Context()); err == nil || !strings.Contains(err.Error(), "take the partition maintenance lock") {
		t.Errorf("Create = %v", err)
	}
}

func TestCreateDropsNothing(t *testing.T) {
	f := newFakeDB()
	f.retention = []dbgen.ListRetentionRow{defaultRetention}
	f.partitions["audit_log"] = []dbgen.ListPartitionsRow{{Name: "audit_log_p202001"}}
	m := newMaintainer(f, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), &bytes.Buffer{})
	if err := m.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.names("audit_log"), "audit_log_p202001") {
		t.Error("the start-up step dropped a partition")
	}
}

func TestMaintainFailures(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name    string
		setup   func(*fakeDB)
		connect error
		want    string
	}{
		{name: "connect", connect: errors.New("connection refused"), want: "connection refused"},
		{name: "advisory lock", setup: func(f *fakeDB) { f.failOn = "pg_advisory_lock" },
			want: "take the partition maintenance lock"},
		{name: "lock timeout setting", setup: func(f *fakeDB) { f.failOn = "lock_timeout" }, want: "set lock_timeout"},
		{name: "retention", setup: func(f *fakeDB) { f.retentionErr = errors.New("boom") },
			want: "read the retention periods"},
		{name: "list", setup: func(f *fakeDB) { f.listErr = errors.New("boom") }, want: "list the partitions"},
		{name: "list detached", setup: func(f *fakeDB) { f.detachedErr = errors.New("boom") },
			want: "list the detached partitions"},
		{name: "create", setup: func(f *fakeDB) { f.failOn = "CREATE TABLE" }, want: "create the partition"},
		{name: "detach", setup: func(f *fakeDB) {
			f.failOn = "DETACH"
			f.partitions["audit_log"] = []dbgen.ListPartitionsRow{{Name: "audit_log_p202001"}}
		}, want: "detach the partition audit_log_p202001"},
		{name: "drop", setup: func(f *fakeDB) {
			f.failOn = "DROP TABLE"
			f.partitions["audit_log"] = []dbgen.ListPartitionsRow{{Name: "audit_log_p202001"}}
		}, want: "drop the partition audit_log_p202001"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeDB()
			f.retention = []dbgen.ListRetentionRow{defaultRetention}
			if tt.setup != nil {
				tt.setup(f)
			}
			var log bytes.Buffer
			m := newMaintainer(f, now, &log)
			if tt.connect != nil {
				m.connect = func(context.Context) (Session, error) { return nil, tt.connect }
			}
			err := m.Maintain(t.Context())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Maintain = %v, want an error with %q", err, tt.want)
			}
			if !strings.Contains(log.String(), `"level":"WARN","event":"partition_maintenance_failed"`) {
				t.Errorf("log %s", log.String())
			}
		})
	}

	// A run that a shutdown cancels is not a failure to log.
	f := newFakeDB()
	var log bytes.Buffer
	m := newMaintainer(f, now, &log)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m.connect = func(ctx context.Context) (Session, error) { return nil, ctx.Err() }
	if err := m.Maintain(ctx); !errors.Is(err, context.Canceled) || log.Len() != 0 {
		t.Errorf("Maintain = %v, log %s", err, log.String())
	}
}

func TestParse(t *testing.T) {
	daily, monthly := tables[0], tables[2]
	for _, tt := range []struct {
		t    table
		name string
		ok   bool
		from time.Time
	}{
		{daily, "stored_snapshots_p20261005", true, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)},
		{daily, "stored_snapshots_p2026105", false, time.Time{}},
		{daily, "stored_snapshots_p202610", false, time.Time{}},
		{daily, "stored_snapshots_pabcdefgh", false, time.Time{}},
		{daily, "other_p20261005", false, time.Time{}},
		{monthly, "timeline_entries_p202610", true, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{monthly, "timeline_entries_p20261005", false, time.Time{}},
		{table{name: "x", period: 0}, "x_p202610", false, time.Time{}},
	} {
		p, ok := parse(tt.t, tt.name)
		if ok != tt.ok || !p.from.Equal(tt.from) {
			t.Errorf("parse(%s) = %+v, %v", tt.name, p, ok)
		}
	}
}
