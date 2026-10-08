// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package links

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/links/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/publicid"
)

const orgID = 7

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

var by = Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportUI}

type tableRow struct {
	dbgen.GetLookupTableRow
	entries map[string][]byte
}

type ruleRow struct {
	dbgen.GetLinkRuleRow
	matchers []dbgen.ListLinkRuleMatchersRow
}

// fakeStore is the database of the package in memory; fail makes the query of that name fail.
type fakeStore struct {
	mu        sync.Mutex
	nextID    int64
	tables    []*tableRow
	rules     []*ruleRow
	audit     []auditdb.InsertAuditEntryParams
	snapshots [][]byte
	group     *dbgen.GetLinkGroupRow
	alerts    []dbgen.ListLinkAlertsRow
	cells     int
	fail      map[string]error
}

func newFake() *fakeStore {
	return &fakeStore{fail: map[string]error{}}
}

func (f *fakeStore) err(name string) error { return f.fail[name] }

func (f *fakeStore) id() int64 {
	f.nextID++
	return f.nextID
}

func (f *fakeStore) InTx(_ context.Context, fn func(Queries) error) error {
	snap := f.snapshot()
	if err := fn(f); err != nil {
		f.restore(snap)
		return err
	}
	return nil
}

type snap struct {
	tables []*tableRow
	rules  []*ruleRow
	audit  int
}

func (f *fakeStore) snapshot() snap {
	s := snap{audit: len(f.audit)}
	for _, t := range f.tables {
		c := *t
		c.entries = map[string][]byte{}
		for k, v := range t.entries {
			c.entries[k] = v
		}
		s.tables = append(s.tables, &c)
	}
	for _, r := range f.rules {
		c := *r
		c.matchers = slices.Clone(r.matchers)
		s.rules = append(s.rules, &c)
	}
	return s
}

func (f *fakeStore) restore(s snap) {
	f.tables, f.rules, f.audit = s.tables, s.rules, f.audit[:s.audit]
}

func unique(constraint string) error {
	return &pgconn.PgError{Code: uniqueViolation, ConstraintName: constraint}
}

func (f *fakeStore) ListLookupTables(_ context.Context, arg dbgen.ListLookupTablesParams) (
	[]dbgen.ListLookupTablesRow, error) {
	if err := f.err("ListLookupTables"); err != nil {
		return nil, err
	}
	var out []dbgen.ListLookupTablesRow
	for _, t := range f.tables {
		if arg.AfterID.Valid && t.ID <= arg.AfterID.Int64 {
			continue
		}
		if len(out) == int(arg.PageSize) {
			break
		}
		out = append(out, dbgen.ListLookupTablesRow(t.GetLookupTableRow))
	}
	return out, nil
}

func (f *fakeStore) table(publicID string) *tableRow {
	for _, t := range f.tables {
		if t.PublicID == publicID {
			return t
		}
	}
	return nil
}

func (f *fakeStore) GetLookupTable(_ context.Context, arg dbgen.GetLookupTableParams) (dbgen.GetLookupTableRow, error) {
	if err := f.err("GetLookupTable"); err != nil {
		return dbgen.GetLookupTableRow{}, err
	}
	if t := f.table(arg.PublicID); t != nil {
		return t.GetLookupTableRow, nil
	}
	return dbgen.GetLookupTableRow{}, pgx.ErrNoRows
}

func (f *fakeStore) LockLookupTable(_ context.Context, arg dbgen.LockLookupTableParams) (int64, error) {
	if err := f.err("LockLookupTable"); err != nil {
		return 0, err
	}
	if t := f.table(arg.PublicID); t != nil {
		return t.ID, nil
	}
	return 0, pgx.ErrNoRows
}

func (f *fakeStore) ListLookupEntries(_ context.Context, arg dbgen.ListLookupEntriesParams) (
	[]dbgen.ListLookupEntriesRow, error) {
	if err := f.err("ListLookupEntries"); err != nil {
		return nil, err
	}
	var out []dbgen.ListLookupEntriesRow
	for _, t := range f.tables {
		if !slices.Contains(arg.LookupTableIds, t.ID) {
			continue
		}
		keys := make([]string, 0, len(t.entries))
		for k := range t.entries {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			out = append(out, dbgen.ListLookupEntriesRow{LookupTableID: t.ID, Key: k, Cells: t.entries[k]})
		}
	}
	return out, nil
}

func (f *fakeStore) InsertLookupTable(_ context.Context, arg dbgen.InsertLookupTableParams) (int64, error) {
	if err := f.err("InsertLookupTable"); err != nil {
		return 0, err
	}
	for _, t := range f.tables {
		if t.Name == arg.Name {
			return 0, unique(tableNameKey)
		}
	}
	id := f.id()
	f.tables = append(f.tables, &tableRow{GetLookupTableRow: dbgen.GetLookupTableRow{ID: id, PublicID: arg.PublicID,
		Name: arg.Name, Description: arg.Description, ColumnNames: arg.ColumnNames, CreatedAt: arg.Now, Version: 1},
		entries: map[string][]byte{}})
	return id, nil
}

func (f *fakeStore) UpdateLookupTable(_ context.Context, arg dbgen.UpdateLookupTableParams) error {
	if err := f.err("UpdateLookupTable"); err != nil {
		return err
	}
	for _, t := range f.tables {
		if t.Name == arg.Name && t.ID != arg.ID {
			return unique(tableNameKey)
		}
	}
	for _, t := range f.tables {
		if t.ID == arg.ID {
			t.Name, t.Description, t.ColumnNames = arg.Name, arg.Description, arg.ColumnNames
			t.Version++
		}
	}
	return nil
}

func (f *fakeStore) DeleteLookupEntries(_ context.Context, arg dbgen.DeleteLookupEntriesParams) error {
	if err := f.err("DeleteLookupEntries"); err != nil {
		return err
	}
	for _, t := range f.tables {
		if t.ID == arg.LookupTableID {
			t.entries = map[string][]byte{}
		}
	}
	return nil
}

func (f *fakeStore) InsertLookupEntries(_ context.Context, arg dbgen.InsertLookupEntriesParams) error {
	if err := f.err("InsertLookupEntries"); err != nil {
		return err
	}
	for _, t := range f.tables {
		if t.ID == arg.LookupTableID {
			for i, k := range arg.Keys {
				t.entries[k] = arg.Cells[i]
			}
		}
	}
	return nil
}

func (f *fakeStore) DeleteLookupTable(_ context.Context, arg dbgen.DeleteLookupTableParams) error {
	if err := f.err("DeleteLookupTable"); err != nil {
		return err
	}
	f.tables = slices.DeleteFunc(f.tables, func(t *tableRow) bool { return t.ID == arg.ID })
	return nil
}

func (f *fakeStore) GetLookupCells(_ context.Context, arg dbgen.GetLookupCellsParams) ([]byte, error) {
	f.mu.Lock()
	f.cells++
	f.mu.Unlock()
	if err := f.err("GetLookupCells"); err != nil {
		return nil, err
	}
	for _, t := range f.tables {
		if t.Name == arg.Name {
			if c, ok := t.entries[arg.Key]; ok {
				return c, nil
			}
		}
	}
	return nil, pgx.ErrNoRows
}

func (f *fakeStore) ListLinkRules(_ context.Context, arg dbgen.ListLinkRulesParams) ([]dbgen.ListLinkRulesRow, error) {
	if err := f.err("ListLinkRules"); err != nil {
		return nil, err
	}
	var out []dbgen.ListLinkRulesRow
	for _, r := range f.rules {
		if arg.AfterID.Valid && r.ID <= arg.AfterID.Int64 {
			continue
		}
		if len(out) == int(arg.PageSize) {
			break
		}
		out = append(out, dbgen.ListLinkRulesRow(r.GetLinkRuleRow))
	}
	return out, nil
}

func (f *fakeStore) rule(publicID string) *ruleRow {
	for _, r := range f.rules {
		if r.PublicID == publicID {
			return r
		}
	}
	return nil
}

func (f *fakeStore) GetLinkRule(_ context.Context, arg dbgen.GetLinkRuleParams) (dbgen.GetLinkRuleRow, error) {
	if err := f.err("GetLinkRule"); err != nil {
		return dbgen.GetLinkRuleRow{}, err
	}
	if r := f.rule(arg.PublicID); r != nil {
		return r.GetLinkRuleRow, nil
	}
	return dbgen.GetLinkRuleRow{}, pgx.ErrNoRows
}

func (f *fakeStore) LockLinkRule(_ context.Context, arg dbgen.LockLinkRuleParams) (int64, error) {
	if err := f.err("LockLinkRule"); err != nil {
		return 0, err
	}
	if r := f.rule(arg.PublicID); r != nil {
		return r.ID, nil
	}
	return 0, pgx.ErrNoRows
}

func (f *fakeStore) ListLinkRuleMatchers(_ context.Context, arg dbgen.ListLinkRuleMatchersParams) (
	[]dbgen.ListLinkRuleMatchersRow, error) {
	if err := f.err("ListLinkRuleMatchers"); err != nil {
		return nil, err
	}
	var out []dbgen.ListLinkRuleMatchersRow
	for _, r := range f.rules {
		if slices.Contains(arg.LinkRuleIds, r.ID) {
			out = append(out, r.matchers...)
		}
	}
	return out, nil
}

func (f *fakeStore) ListLinkRuleTemplates(context.Context, int64) ([]dbgen.ListLinkRuleTemplatesRow, error) {
	if err := f.err("ListLinkRuleTemplates"); err != nil {
		return nil, err
	}
	var out []dbgen.ListLinkRuleTemplatesRow
	for _, r := range f.rules {
		out = append(out, dbgen.ListLinkRuleTemplatesRow{PublicID: r.PublicID, Name: r.Name,
			UrlTemplate: r.UrlTemplate})
	}
	return out, nil
}

func (f *fakeStore) InsertLinkRule(_ context.Context, arg dbgen.InsertLinkRuleParams) (int64, error) {
	if err := f.err("InsertLinkRule"); err != nil {
		return 0, err
	}
	for _, r := range f.rules {
		if r.Name == arg.Name {
			return 0, unique(ruleNameKey)
		}
	}
	id := f.id()
	f.rules = append(f.rules, &ruleRow{GetLinkRuleRow: dbgen.GetLinkRuleRow{ID: id, PublicID: arg.PublicID,
		Name: arg.Name, ScopeType: arg.ScopeType, ScopeLabel: arg.ScopeLabel, UrlTemplate: arg.UrlTemplate,
		CreatedAt: arg.Now, Version: 1}})
	return id, nil
}

func (f *fakeStore) UpdateLinkRule(_ context.Context, arg dbgen.UpdateLinkRuleParams) error {
	if err := f.err("UpdateLinkRule"); err != nil {
		return err
	}
	for _, r := range f.rules {
		if r.Name == arg.Name && r.ID != arg.ID {
			return unique(ruleNameKey)
		}
	}
	for _, r := range f.rules {
		if r.ID == arg.ID {
			r.Name, r.ScopeType, r.ScopeLabel, r.UrlTemplate = arg.Name, arg.ScopeType, arg.ScopeLabel, arg.UrlTemplate
			r.Version++
		}
	}
	return nil
}

func (f *fakeStore) DeleteLinkRuleMatchers(_ context.Context, arg dbgen.DeleteLinkRuleMatchersParams) error {
	if err := f.err("DeleteLinkRuleMatchers"); err != nil {
		return err
	}
	for _, r := range f.rules {
		if r.ID == arg.LinkRuleID {
			r.matchers = nil
		}
	}
	return nil
}

func (f *fakeStore) InsertLinkRuleMatcher(_ context.Context, arg dbgen.InsertLinkRuleMatcherParams) error {
	if err := f.err("InsertLinkRuleMatcher"); err != nil {
		return err
	}
	for _, r := range f.rules {
		if r.ID == arg.LinkRuleID {
			r.matchers = append(r.matchers, dbgen.ListLinkRuleMatchersRow{LinkRuleID: r.ID, Label: arg.Label,
				Op: arg.Op, Value: arg.Value})
		}
	}
	return nil
}

func (f *fakeStore) DeleteLinkRule(_ context.Context, arg dbgen.DeleteLinkRuleParams) error {
	if err := f.err("DeleteLinkRule"); err != nil {
		return err
	}
	f.rules = slices.DeleteFunc(f.rules, func(r *ruleRow) bool { return r.ID == arg.ID && !r.Builtin })
	return nil
}

func (f *fakeStore) EnsureExploreRule(_ context.Context, arg dbgen.EnsureExploreRuleParams) (string, error) {
	if err := f.err("EnsureExploreRule"); err != nil {
		return "", err
	}
	for _, r := range f.rules {
		if r.Builtin {
			return "", pgx.ErrNoRows
		}
		if r.Name == arg.Name {
			return "", unique(ruleNameKey)
		}
	}
	f.rules = append(f.rules, &ruleRow{GetLinkRuleRow: dbgen.GetLinkRuleRow{ID: f.id(), PublicID: arg.PublicID,
		Name: arg.Name, Builtin: true, ScopeType: ScopeAlertGroup, UrlTemplate: arg.UrlTemplate, CreatedAt: arg.Now,
		Version: 1}})
	return arg.PublicID, nil
}

func (f *fakeStore) GetLinkSnapshotRetention(context.Context, int64) (int64, error) {
	return 14, f.err("GetLinkSnapshotRetention")
}

func (f *fakeStore) ListRecentSnapshots(_ context.Context, arg dbgen.ListRecentSnapshotsParams) ([][]byte, error) {
	if err := f.err("ListRecentSnapshots"); err != nil {
		return nil, err
	}
	return f.snapshots[:min(len(f.snapshots), int(arg.Lim))], nil
}

func (f *fakeStore) GetLinkGroup(context.Context, dbgen.GetLinkGroupParams) (dbgen.GetLinkGroupRow, error) {
	if err := f.err("GetLinkGroup"); err != nil {
		return dbgen.GetLinkGroupRow{}, err
	}
	if f.group == nil {
		return dbgen.GetLinkGroupRow{}, pgx.ErrNoRows
	}
	return *f.group, nil
}

func (f *fakeStore) ListLinkAlerts(context.Context, dbgen.ListLinkAlertsParams) ([]dbgen.ListLinkAlertsRow, error) {
	return f.alerts, f.err("ListLinkAlerts")
}

func (f *fakeStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := f.err("InsertAuditEntry"); err != nil {
		return err
	}
	f.audit = append(f.audit, arg)
	return nil
}

// newService is a Service over the fake, logging to log.
func newService(t *testing.T, f *fakeStore) (*Service, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	logger := logging.New(&log, logging.LevelInfo)
	s := New(Config{OrgID: orgID, Store: f, Audit: audit.NewWriter(logger, clock.NewManual(t0)),
		Business: clock.NewManual(t0), Real: clock.NewManual(t0), PublicURL: "https://muster.example.org/",
		Log: logger})
	s.over = func(DBTX) Queries { return f }
	return s, &log
}

func grafana() TableInput {
	return TableInput{Name: " grafana ", Columns: []string{"address", "datasource_uid"}, Entries: []Entry{
		{Key: "prod", Values: map[string]string{"address": "https://grafana.example.org", "datasource_uid": "PROM1"}},
		{Key: "dev", Values: map[string]string{"address": "https://dev.example.org", "datasource_uid": "PROM2"}},
	}}
}

func fieldError(t *testing.T, err error) *FieldError {
	t.Helper()
	f, ok := errors.AsType[*FieldError](err)
	if !ok {
		t.Fatalf("error %v, want a FieldError", err)
	}
	return f
}

func diffOf(t *testing.T, e auditdb.InsertAuditEntryParams) []audit.Change {
	t.Helper()
	var out []audit.Change
	if err := json.Unmarshal(e.Diff, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestLookupTables: a table is created, read, listed, updated with its rows replaced as a whole and deleted, each
// change with its Audit log entry and diff; the version follows If-Match.
func TestLookupTables(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	created, err := s.CreateTable(ctx, by, grafana())
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "grafana" || created.Description != "" || len(created.Entries) != 2 ||
		created.Entries[0].Key != "dev" || created.Entries[1].Values["datasource_uid"] != "PROM1" ||
		!strings.HasPrefix(created.PublicID, string(publicid.LookupTable)) || created.Version != 1 {
		t.Fatalf("created %+v", created)
	}
	if len(f.audit) != 1 || f.audit[0].Action != ActionTableCreated || f.audit[0].ResourceType.String != ResourceTable {
		t.Fatalf("audit %+v", f.audit)
	}
	got, err := s.GetTable(ctx, strings.ToLower(created.PublicID))
	if err != nil || got.Name != "grafana" || len(got.Entries) != 2 {
		t.Fatalf("get %+v %v", got, err)
	}
	if _, err := s.CreateTable(ctx, by, grafana()); !errors.Is(err, ErrTableNameTaken) {
		t.Errorf("same name: %v", err)
	}
	desc := "Grafana by environment"
	in := TableInput{Name: "grafana", Description: &desc, Columns: []string{"address"}, Entries: []Entry{
		{Key: "prod", Values: map[string]string{"address": "https://g.example.org"}}}}
	stale := int64(9)
	if _, err := s.UpdateTable(ctx, by, created.PublicID, &stale, in); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale version: %v", err)
	}
	v := created.Version
	updated, err := s.UpdateTable(ctx, by, created.PublicID, &v, in)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Description != desc || len(updated.Entries) != 1 ||
		updated.Entries[0].Values["address"] != "https://g.example.org" || !slices.Equal(updated.Columns, []string{"address"}) {
		t.Fatalf("updated %+v", updated)
	}
	changes := diffOf(t, f.audit[len(f.audit)-1])
	pointers := make([]string, len(changes))
	for i, c := range changes {
		pointers[i] = c.Pointer
	}
	if f.audit[len(f.audit)-1].Action != ActionTableUpdated ||
		!slices.Equal(pointers, []string{"/description", "/columns", "/entries"}) {
		t.Errorf("update audit %v", pointers)
	}
	same, err := s.UpdateTable(ctx, by, created.PublicID, nil, TableInput{Name: "grafana", Columns: []string{"address"},
		Entries: []Entry{{Key: "prod", Values: map[string]string{"address": "https://g.example.org"}}}})
	if err != nil || same.Version != 2 || len(f.audit) != 2 {
		t.Errorf("an update without changes: %+v %v, %d entries", same, err, len(f.audit))
	}
	other, err := s.CreateTable(ctx, by, TableInput{Name: "dc", Columns: []string{"site"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTable(ctx, by, other.PublicID, nil, TableInput{Name: "grafana", Columns: []string{"site"}}); !errors.Is(err, ErrTableNameTaken) {
		t.Errorf("rename to a taken name: %v", err)
	}
	if p, err := s.ListTables(ctx, ListFilter{Limit: 500}); err != nil || len(p.Tables) != 2 {
		t.Fatalf("a large limit %+v %v", p, err)
	}
	page, err := s.ListTables(ctx, ListFilter{Limit: 1})
	if err != nil || len(page.Tables) != 1 || page.Next == nil || page.Tables[0].Name != "grafana" {
		t.Fatalf("first page %+v %v", page, err)
	}
	page, err = s.ListTables(ctx, ListFilter{After: page.Next, Limit: 10})
	if err != nil || len(page.Tables) != 1 || page.Next != nil || page.Tables[0].Name != "dc" {
		t.Fatalf("second page %+v %v", page, err)
	}
	if err := s.DeleteTable(ctx, by, other.PublicID, &stale); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale delete: %v", err)
	}
	if err := s.DeleteTable(ctx, by, other.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if last := f.audit[len(f.audit)-1]; last.Action != ActionTableDeleted || last.ResourceName.String != "dc" {
		t.Errorf("delete audit %+v", last)
	}
	for _, id := range []string{other.PublicID, "x", "KRAAAAAAAAAAAA"} {
		if _, err := s.GetTable(ctx, id); !errors.Is(err, ErrTableNotFound) {
			t.Errorf("get %s: %v", id, err)
		}
		if err := s.DeleteTable(ctx, by, id, nil); !errors.Is(err, ErrTableNotFound) {
			t.Errorf("delete %s: %v", id, err)
		}
		if _, err := s.UpdateTable(ctx, by, id, nil, grafana()); !errors.Is(err, ErrTableNotFound) {
			t.Errorf("update %s: %v", id, err)
		}
	}
}

// TestLookupTableChecks: the columns are named, unique and at most MaxColumns; every row has a unique key and
// exactly one value per column (column_mismatch at /entries/<n>/values); lengths are bounded.
func TestLookupTableChecks(t *testing.T) {
	s, _ := newService(t, newFake())
	long := strings.Repeat("x", MaxValueLength+1)
	desc := strings.Repeat("d", MaxDescriptionLength+1)
	many := make([]string, MaxColumns+1)
	for i := range many {
		many[i] = "c" + strings.Repeat("x", i)
	}
	rows := make([]Entry, MaxEntries+1)
	row := func(key string, values map[string]string) []Entry { return []Entry{{Key: key, Values: values}} }
	for _, tc := range []struct {
		in            TableInput
		pointer, code string
	}{
		{TableInput{Name: " ", Columns: []string{"a"}}, "/name", CodeInvalidFormat},
		{TableInput{Name: strings.Repeat("n", MaxNameLength+1), Columns: []string{"a"}}, "/name", CodeTooLong},
		{TableInput{Name: "t", Description: &desc, Columns: []string{"a"}}, "/description", CodeTooLong},
		{TableInput{Name: "t"}, "/columns", CodeRequired},
		{TableInput{Name: "t", Columns: many}, "/columns", CodeTooLong},
		{TableInput{Name: "t", Columns: []string{"a", ""}}, "/columns/1", CodeInvalidFormat},
		{TableInput{Name: "t", Columns: []string{"a", "a"}}, "/columns/1", CodeDuplicate},
		{TableInput{Name: "t", Columns: []string{"a"}, Entries: rows}, "/entries", CodeTooLong},
		{TableInput{Name: "t", Columns: []string{"a"}, Entries: row("", map[string]string{"a": "1"})},
			"/entries/0/key", CodeInvalidFormat},
		{TableInput{Name: "t", Columns: []string{"a"}, Entries: row(long, map[string]string{"a": "1"})},
			"/entries/0/key", CodeTooLong},
		{TableInput{Name: "t", Columns: []string{"a"}, Entries: append(row("k", map[string]string{"a": "1"}),
			row("k", map[string]string{"a": "2"})...)}, "/entries/1/key", CodeDuplicate},
		{TableInput{Name: "t", Columns: []string{"a"}, Entries: row("k", map[string]string{"b": "x"})},
			"/entries/0/values", CodeColumnMismatch},
		{TableInput{Name: "t", Columns: []string{"a", "b"}, Entries: row("k", map[string]string{"a": "x"})},
			"/entries/0/values", CodeColumnMismatch},
		{TableInput{Name: "t", Columns: []string{"a"}, Entries: row("k", map[string]string{"a": "x", "b": "y"})},
			"/entries/0/values", CodeColumnMismatch},
		{TableInput{Name: "t", Columns: []string{"a/b"}, Entries: row("k", map[string]string{"a/b": long})},
			"/entries/0/values/a~1b", CodeTooLong},
	} {
		_, err := s.CreateTable(t.Context(), by, tc.in)
		fe := fieldError(t, err)
		if fe.Pointer != tc.pointer || fe.Code != tc.code || fe.Error() == "" {
			t.Errorf("%s: %+v, want %s %s", tc.pointer, fe, tc.pointer, tc.code)
		}
		if _, err := s.UpdateTable(t.Context(), by, "TBAAAAAAAAAAAA", nil, tc.in); err == nil {
			t.Errorf("%s: the update passed", tc.pointer)
		}
	}
}

// TestLookupTableInUse: a table that a Link rule's URL template reads by its name cannot be deleted; renaming the
// rule's table frees it.
func TestLookupTableInUse(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	table, err := s.CreateTable(ctx, by, grafana())
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureExplore(ctx, f, orgID, t0); err != nil {
		t.Fatal(err)
	}
	err = s.DeleteTable(ctx, by, table.PublicID, nil)
	var inUse *InUseError
	if !errors.Is(err, ErrInUse) || !errors.As(err, &inUse) || !slices.Equal(inUse.Rules, []string{"Explore"}) {
		t.Fatalf("a table the built-in rule reads: %v", err)
	}
	// A rule whose stored template does not parse reads nothing.
	f.rules[0].UrlTemplate = "{{ lookup"
	f.rules = append(f.rules, &ruleRow{GetLinkRuleRow: dbgen.GetLinkRuleRow{ID: 99, PublicID: "KRZZZZZZZZZZZZ",
		Name: "x", ScopeType: ScopeAlertGroup, UrlTemplate: `{{ lookup .T "k" "c" }}`}})
	if err := s.DeleteTable(ctx, by, table.PublicID, nil); err != nil {
		t.Fatalf("a table no rule reads by name: %v", err)
	}
	f.fail["ListLinkRuleTemplates"] = errors.New("boom")
	other, err := s.CreateTable(ctx, by, TableInput{Name: "dc", Columns: []string{"site"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTable(ctx, by, other.PublicID, nil); err == nil || errors.Is(err, ErrInUse) {
		t.Errorf("a failing read: %v", err)
	}
}

// TestLookupTableRenameInUse: a new name for a table that Link rules read by its name is refused with those rules
// named, since they would read nothing; new rows under the same name, and a new name for a table no rule reads, are
// saved.
func TestLookupTableRenameInUse(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	table, err := s.CreateTable(ctx, by, grafana())
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureExplore(ctx, f, orgID, t0); err != nil {
		t.Fatal(err)
	}
	f.rules = append(f.rules, &ruleRow{GetLinkRuleRow: dbgen.GetLinkRuleRow{ID: 99, PublicID: "KRZZZZZZZZZZZZ",
		Name: "Dashboard", ScopeType: ScopeAlertGroup,
		UrlTemplate: `{{ lookup "grafana" .Labels.cluster "address" }}`}})
	renamed := grafana()
	renamed.Name = "grafana-old"
	_, err = s.UpdateTable(ctx, by, table.PublicID, nil, renamed)
	var inUse *InUseError
	if !errors.Is(err, ErrInUse) || !errors.As(err, &inUse) ||
		!slices.Equal(inUse.Rules, []string{"Explore", "Dashboard"}) ||
		!strings.Contains(err.Error(), "Explore, Dashboard") {
		t.Fatalf("renaming a table two rules read: %v", err)
	}
	if got, err := s.GetTable(ctx, table.PublicID); err != nil || got.Name != "grafana" || got.Version !=
		table.Version {
		t.Errorf("the refused rename changed the table: %+v %v", got, err)
	}
	same := grafana()
	same.Entries = same.Entries[:1]
	if got, err := s.UpdateTable(ctx, by, table.PublicID, nil, same); err != nil || len(got.Entries) != 1 {
		t.Errorf("new rows under the same name: %+v %v", got, err)
	}
	f.fail["ListLinkRuleTemplates"] = errors.New("boom")
	if _, err := s.UpdateTable(ctx, by, table.PublicID, nil, renamed); err == nil || errors.Is(err, ErrInUse) {
		t.Errorf("a failing read: %v", err)
	}
	delete(f.fail, "ListLinkRuleTemplates")
	other, err := s.CreateTable(ctx, by, TableInput{Name: "dc", Columns: []string{"site"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.UpdateTable(ctx, by, other.PublicID, nil, TableInput{Name: "sites",
		Columns: []string{"site"}}); err != nil || got.Name != "sites" {
		t.Errorf("renaming a table no rule reads: %+v %v", got, err)
	}
}

// TestLookupCells: lookup reads one cell through a per-render cache; a missing table, key or column is the empty
// string, a failing read and more than MaxLookups rows are errors.
func TestLookupCells(t *testing.T) {
	ctx := t.Context()
	f := newFake()
	s, _ := newService(t, f)
	if _, err := s.CreateTable(ctx, by, grafana()); err != nil {
		t.Fatal(err)
	}
	l := s.newLookups(ctx, f)
	for _, tc := range []struct{ table, key, column, want string }{
		{"grafana", "prod", "address", "https://grafana.example.org"},
		{"grafana", "prod", "datasource_uid", "PROM1"},
		{"grafana", "prod", "nope", ""},
		{"grafana", "staging", "address", ""},
		{"nope", "prod", "address", ""},
	} {
		got, err := l.lookup(tc.table, tc.key, tc.column)
		if err != nil || got != tc.want {
			t.Errorf("lookup %s %s %s = %q %v", tc.table, tc.key, tc.column, got, err)
		}
	}
	if f.cells != 3 {
		t.Errorf("rows read %d, want 3: one per table and key", f.cells)
	}
	// A name or key that cannot exist is never sent to the database, which would refuse it.
	for _, k := range []string{"\xd0", "a\x00b", strings.Repeat("k", 4*MaxValueLength+1)} {
		if got, err := l.lookup("grafana", k, "address"); err != nil || got != "" {
			t.Errorf("unreadable key %q: %q %v", k, got, err)
		}
		if got, err := l.lookup(k, "prod", "address"); err != nil || got != "" {
			t.Errorf("unreadable table %q: %q %v", k, got, err)
		}
	}
	if f.cells != 3 {
		t.Errorf("unreadable names read rows: %d", f.cells)
	}
	for i := range MaxLookups {
		if _, err := l.lookup("grafana", strings.Repeat("k", i+1), "address"); err != nil && i < MaxLookups-3 {
			t.Fatalf("lookup %d: %v", i, err)
		}
	}
	if _, err := l.lookup("grafana", "one-more", "address"); !errors.Is(err, errTooManyLookups) {
		t.Errorf("past the cap: %v", err)
	}
	f.fail["GetLookupCells"] = errors.New("boom")
	if _, err := s.newLookups(ctx, f).lookup("grafana", "prod", "address"); err == nil {
		t.Error("a failing read is an error")
	}
}

// TestLookupStoreFailures: a failing query fails the change and writes no Audit log entry.
func TestLookupStoreFailures(t *testing.T) {
	ctx := t.Context()
	for _, name := range []string{"InsertLookupTable", "InsertLookupEntries", "GetLookupTable", "ListLookupEntries",
		"InsertAuditEntry"} {
		f := newFake()
		s, _ := newService(t, f)
		f.fail[name] = errors.New("boom")
		if _, err := s.CreateTable(ctx, by, grafana()); err == nil || len(f.audit) != 0 {
			t.Errorf("create with %s failing: %v", name, err)
		}
	}
	for _, name := range []string{"LockLookupTable", "UpdateLookupTable", "DeleteLookupEntries", "InsertLookupEntries",
		"InsertAuditEntry"} {
		f := newFake()
		s, _ := newService(t, f)
		table, err := s.CreateTable(ctx, by, grafana())
		if err != nil {
			t.Fatal(err)
		}
		f.fail[name] = errors.New("boom")
		if _, err := s.UpdateTable(ctx, by, table.PublicID, nil, TableInput{Name: "g2", Columns: []string{"a"},
			Entries: []Entry{{Key: "k", Values: map[string]string{"a": "1"}}}}); err == nil || len(f.audit) != 1 {
			t.Errorf("update with %s failing: %v", name, err)
		}
		if name == "UpdateLookupTable" || name == "DeleteLookupEntries" || name == "InsertLookupEntries" {
			continue
		}
		if err := s.DeleteTable(ctx, by, table.PublicID, nil); err == nil {
			t.Errorf("delete with %s failing", name)
		}
	}
	f := newFake()
	s, _ := newService(t, f)
	table, err := s.CreateTable(ctx, by, grafana())
	if err != nil {
		t.Fatal(err)
	}
	f.fail["DeleteLookupTable"] = errors.New("boom")
	if err := s.DeleteTable(ctx, by, table.PublicID, nil); err == nil {
		t.Error("a failing delete")
	}
	f.fail["ListLookupTables"] = errors.New("boom")
	if _, err := s.ListTables(ctx, ListFilter{}); err == nil {
		t.Error("a failing list")
	}
	delete(f.fail, "ListLookupTables")
	f.fail["ListLookupEntries"] = errors.New("boom")
	if _, err := s.ListTables(ctx, ListFilter{}); err == nil {
		t.Error("a failing read of the rows")
	}
	delete(f.fail, "ListLookupEntries")
	f.tables[0].entries["bad"] = []byte("[")
	if _, err := s.GetTable(ctx, table.PublicID); err == nil {
		t.Error("a row that does not decode")
	}
}
