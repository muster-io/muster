// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package links holds the links of C-12.FR-9: the Organization's Lookup tables — named columns and rows of a key and
// one value per column, which a template reads one cell of with `lookup "<table>" <key> "<column>"` — and its Link
// rules — a name, Matchers checked against an Alert Group's common labels, a scope and a URL template run in the
// sandbox of ADR-0012 — with the built-in "Explore" rule. It computes the links of an Alert Group: one per matching
// rule, or per value of a label, then the runbook_url and dashboard_url annotations and the generatorURL as "Source",
// each kept only as an http(s) URL. A rule that fails is left out and counted. Every change of a table or a rule is
// recorded in the Audit log; neither changes a message by itself.
package links

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/links/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/templates"
)

// The Audit log actions and resource types of Lookup tables and Link rules (C-03.FR-14).
const (
	ActionTableCreated = "lookup_table.created"
	ActionTableUpdated = "lookup_table.updated"
	ActionTableDeleted = "lookup_table.deleted"
	ActionRuleCreated  = "link_rule.created"
	ActionRuleUpdated  = "link_rule.updated"
	ActionRuleDeleted  = "link_rule.deleted"
	ResourceTable      = "lookup_table"
	ResourceRule       = "link_rule"
)

// The limits of a Lookup table and of the lookups of one render: names and column names in characters, the
// description, a key or a value, the columns and rows of a table, and the rows one render reads.
const (
	MaxNameLength        = 200
	MaxDescriptionLength = 2000
	MaxValueLength       = 4096
	MaxColumns           = 50
	MaxEntries           = 10_000
	MaxLookups           = 100
	// MaxTablesPage bounds a page of Lookup tables, which carry their rows; the list continues with its cursor.
	MaxTablesPage = 50
)

const (
	// uniqueViolation is the SQLSTATE of a unique constraint that refused a row; tableNameKey and ruleNameKey are the
	// constraints of the names.
	uniqueViolation = "23505"
	tableNameKey    = "lookup_tables_org_id_name_key"
	ruleNameKey     = "link_rules_org_id_name_key"
)

var (
	// ErrTableNotFound is a Lookup table that does not exist in the Organization.
	ErrTableNotFound = errors.New("no such lookup table")
	// ErrRuleNotFound is a Link rule that does not exist in the Organization.
	ErrRuleNotFound = errors.New("no such link rule")
	// ErrTableNameTaken is a name another Lookup table has.
	ErrTableNameTaken = errors.New("another lookup table has this name")
	// ErrRuleNameTaken is a name another Link rule has.
	ErrRuleNameTaken = errors.New("another link rule has this name")
	// ErrVersionMismatch is an If-Match that names another version of the table or the rule.
	ErrVersionMismatch = errors.New("the lookup table or link rule changed since it was read")
	// ErrInUse is the deletion of a Lookup table that the URL template of a Link rule reads.
	ErrInUse = errors.New("a link rule reads the lookup table")
	// ErrBuiltinImmutable is the deletion of the built-in "Explore" rule, or a change of its name or scope.
	ErrBuiltinImmutable = errors.New("the built-in link rule cannot be deleted, renamed or given another scope")
)

// The codes of FieldError, as the API names them.
const (
	CodeInvalidFormat   = "invalid_format"
	CodeTooLong         = "too_long"
	CodeDuplicate       = "duplicate"
	CodeColumnMismatch  = "column_mismatch"
	CodeInvalidRegex    = "invalid_regex"
	CodeRequired        = "required"
	CodeTemplateSyntax  = templates.CodeSyntax
	CodeUnknownFunction = templates.CodeUnknownFunction
)

// FieldError is a field of a request that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem and, for a template, the line and column.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
	Line    int
	Column  int
}

func (e *FieldError) Error() string {
	return e.Pointer + ": " + e.Detail
}

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// DBTX is the pool, connection or transaction a render reads through.
type DBTX = dbgen.DBTX

// Queries are the queries of the package, with the insert of the Audit log.
type Queries interface {
	ListLookupTables(ctx context.Context, arg dbgen.ListLookupTablesParams) ([]dbgen.ListLookupTablesRow, error)
	GetLookupTable(ctx context.Context, arg dbgen.GetLookupTableParams) (dbgen.GetLookupTableRow, error)
	LockLookupTable(ctx context.Context, arg dbgen.LockLookupTableParams) (int64, error)
	ListLookupEntries(ctx context.Context, arg dbgen.ListLookupEntriesParams) ([]dbgen.ListLookupEntriesRow, error)
	InsertLookupTable(ctx context.Context, arg dbgen.InsertLookupTableParams) (int64, error)
	UpdateLookupTable(ctx context.Context, arg dbgen.UpdateLookupTableParams) error
	DeleteLookupEntries(ctx context.Context, arg dbgen.DeleteLookupEntriesParams) error
	InsertLookupEntries(ctx context.Context, arg dbgen.InsertLookupEntriesParams) error
	DeleteLookupTable(ctx context.Context, arg dbgen.DeleteLookupTableParams) error
	GetLookupCells(ctx context.Context, arg dbgen.GetLookupCellsParams) ([]byte, error)
	ListLinkRules(ctx context.Context, arg dbgen.ListLinkRulesParams) ([]dbgen.ListLinkRulesRow, error)
	GetLinkRule(ctx context.Context, arg dbgen.GetLinkRuleParams) (dbgen.GetLinkRuleRow, error)
	LockLinkRule(ctx context.Context, arg dbgen.LockLinkRuleParams) (int64, error)
	ListLinkRuleMatchers(ctx context.Context, arg dbgen.ListLinkRuleMatchersParams) (
		[]dbgen.ListLinkRuleMatchersRow, error)
	ListLinkRuleTemplates(ctx context.Context, orgID int64) ([]dbgen.ListLinkRuleTemplatesRow, error)
	InsertLinkRule(ctx context.Context, arg dbgen.InsertLinkRuleParams) (int64, error)
	UpdateLinkRule(ctx context.Context, arg dbgen.UpdateLinkRuleParams) error
	DeleteLinkRuleMatchers(ctx context.Context, arg dbgen.DeleteLinkRuleMatchersParams) error
	InsertLinkRuleMatcher(ctx context.Context, arg dbgen.InsertLinkRuleMatcherParams) error
	DeleteLinkRule(ctx context.Context, arg dbgen.DeleteLinkRuleParams) error
	EnsureExploreRule(ctx context.Context, arg dbgen.EnsureExploreRuleParams) (string, error)
	GetLinkSnapshotRetention(ctx context.Context, orgID int64) (int64, error)
	ListRecentSnapshots(ctx context.Context, arg dbgen.ListRecentSnapshotsParams) ([][]byte, error)
	GetLinkGroup(ctx context.Context, arg dbgen.GetLinkGroupParams) (dbgen.GetLinkGroupRow, error)
	ListLinkAlerts(ctx context.Context, arg dbgen.ListLinkAlertsParams) ([]dbgen.ListLinkAlertsRow, error)
	audit.Store
}

// Store runs the queries over the main pool, alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: newQueries(pool), pool: pool}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
}

func newQueries(d dbgen.DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d)}
}

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(newQueries(tx))
	})
}

// Config is what a Service needs: the Organization, the Store, the Audit log writer, the business clock that dates the
// rows and the templates' `now`, the real clock of the sandbox's execution limit and of the render durations,
// MUSTER_PUBLIC_URL for the links to Alert Groups in the template data, and the logger.
type Config struct {
	OrgID     int64
	Store     Store
	Audit     *audit.Writer
	Business  clock.Clock
	Real      clock.Clock
	PublicURL string
	Log       *logging.Logger
}

// Service holds the Lookup tables and Link rules of an Organization and computes links.
type Service struct {
	orgID     int64
	store     Store
	audit     *audit.Writer
	business  clock.Clock
	real      clock.Clock
	publicURL string
	log       *logging.Logger
	sandbox   *templates.Sandbox
	// over are the queries over a pool or a transaction a render reads through; tests replace them.
	over func(db DBTX) Queries

	mu    sync.Mutex
	cache map[string]*templates.Template
	// logged are the rules and Alert Groups whose failure was logged, so that link_rule_failed is logged once each.
	logged map[string]bool
}

// New returns the Service of cfg.
func New(cfg Config) *Service {
	return &Service{orgID: cfg.OrgID, store: cfg.Store, audit: cfg.Audit, business: cfg.Business, real: cfg.Real,
		publicURL: strings.TrimSuffix(cfg.PublicURL, "/"), log: cfg.Log,
		sandbox: templates.New(cfg.Business, cfg.Real), cache: map[string]*templates.Template{},
		logged: map[string]bool{}, over: func(db DBTX) Queries { return newQueries(db) }}
}

// q are the queries over db, the Store for a nil db.
func (s *Service) q(db DBTX) Queries {
	if db == nil {
		return s.store
	}
	return s.over(db)
}

// cacheSize bounds the parsed templates and the logged failures the Service keeps.
const cacheSize = 512

// parse parses a URL template once and keeps it; the cache is emptied when it grows past its size.
func (s *Service) parse(source string) (*templates.Template, error) {
	s.mu.Lock()
	t, ok := s.cache[source]
	s.mu.Unlock()
	if ok {
		return t, nil
	}
	t, err := s.sandbox.Parse(ResourceRule, source)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.cache) >= cacheSize {
		clear(s.cache)
	}
	s.cache[source] = t
	s.mu.Unlock()
	return t, nil
}

// Entry is one row of a Lookup table: its key and one value per column.
type Entry struct {
	Key    string            `json:"key"`
	Values map[string]string `json:"values"`
}

// Table is a Lookup table as the API reads it.
type Table struct {
	ID          int64
	PublicID    string
	Name        string
	Description string
	Columns     []string
	Entries     []Entry
	CreatedAt   time.Time
	Version     int64
}

// TableInput is what createLookupTable and updateLookupTable write; the rows replace the stored ones as a whole. A
// nil Description keeps the stored one on an update and is empty on a creation.
type TableInput struct {
	Name        string
	Description *string
	Columns     []string
	Entries     []Entry
}

// ListFilter selects a page in the order of creation, after the id After when it is set.
type ListFilter struct {
	After *int64
	Limit int
}

// TablePage is a page of Lookup tables; Next, the id to continue after, is nil on the last page.
type TablePage struct {
	Tables []Table
	Next   *int64
}

// tableView is a Lookup table as its Audit log diff shows it.
type tableView struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Columns     []string `json:"columns"`
	Entries     []Entry  `json:"entries"`
}

func viewOfTable(t Table) tableView {
	return tableView{Name: t.Name, Description: t.Description, Columns: t.Columns, Entries: t.Entries}
}

func tableResource(t Table) audit.Resource {
	return audit.Resource{Type: ResourceTable, PublicID: t.PublicID, Name: t.Name}
}

// ListTables lists the Lookup tables with their rows.
func (s *Service) ListTables(ctx context.Context, f ListFilter) (TablePage, error) {
	limit := min(max(f.Limit, 1), MaxTablesPage)
	p := dbgen.ListLookupTablesParams{OrgID: s.orgID, PageSize: int32(limit) + 1}
	if f.After != nil {
		p.AfterID = pgtype.Int8{Int64: *f.After, Valid: true}
	}
	rows, err := s.store.ListLookupTables(ctx, p)
	if err != nil {
		return TablePage{}, fmt.Errorf("list the lookup tables: %w", err)
	}
	var page TablePage
	for i, r := range rows {
		if i == limit {
			last := page.Tables[limit-1].ID
			page.Next = &last
			break
		}
		page.Tables = append(page.Tables, tableOf(dbgen.GetLookupTableRow(r)))
	}
	if err := s.withEntries(ctx, s.store, page.Tables); err != nil {
		return TablePage{}, err
	}
	return page, nil
}

func tableOf(r dbgen.GetLookupTableRow) Table {
	return Table{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Description: r.Description,
		Columns: slices.Clone(r.ColumnNames), Entries: []Entry{}, CreatedAt: r.CreatedAt.UTC(), Version: r.Version}
}

// withEntries reads the rows of the tables.
func (s *Service) withEntries(ctx context.Context, q Queries, list []Table) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	byID := make(map[int64]int, len(list))
	for i, t := range list {
		ids[i], byID[t.ID] = t.ID, i
	}
	rows, err := q.ListLookupEntries(ctx, dbgen.ListLookupEntriesParams{OrgID: s.orgID, LookupTableIds: ids})
	if err != nil {
		return fmt.Errorf("read the rows of the lookup tables: %w", err)
	}
	for _, r := range rows {
		i, ok := byID[r.LookupTableID]
		if !ok {
			continue
		}
		values := map[string]string{}
		if err := json.Unmarshal(r.Cells, &values); err != nil {
			return fmt.Errorf("read a row of the lookup table %s: %w", list[i].PublicID, err)
		}
		list[i].Entries = append(list[i].Entries, Entry{Key: r.Key, Values: values})
	}
	return nil
}

// GetTable reads the Lookup table publicID with its rows.
func (s *Service) GetTable(ctx context.Context, publicID string) (Table, error) {
	return s.getTable(ctx, s.store, publicID)
}

func (s *Service) getTable(ctx context.Context, q Queries, publicID string) (Table, error) {
	id, err := publicid.Parse(publicid.LookupTable, publicID)
	if err != nil {
		return Table{}, ErrTableNotFound
	}
	r, err := q.GetLookupTable(ctx, dbgen.GetLookupTableParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Table{}, ErrTableNotFound
	}
	if err != nil {
		return Table{}, fmt.Errorf("read the lookup table %s: %w", id, err)
	}
	list := []Table{tableOf(r)}
	if err := s.withEntries(ctx, q, list); err != nil {
		return Table{}, err
	}
	return list[0], nil
}

// lockTable locks the Lookup table publicID for a change and reads it.
func (s *Service) lockTable(ctx context.Context, q Queries, publicID string) (Table, error) {
	id, err := publicid.Parse(publicid.LookupTable, publicID)
	if err != nil {
		return Table{}, ErrTableNotFound
	}
	if _, err := q.LockLookupTable(ctx, dbgen.LockLookupTableParams{OrgID: s.orgID, PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Table{}, ErrTableNotFound
		}
		return Table{}, fmt.Errorf("lock the lookup table %s: %w", id, err)
	}
	return s.getTable(ctx, q, id)
}

// checkName refuses an empty name or one longer than MaxNameLength characters, at pointer.
func checkName(pointer, name string) error {
	if strings.TrimSpace(name) == "" {
		return &FieldError{Pointer: pointer, Code: CodeInvalidFormat, Detail: "The name is empty."}
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return &FieldError{Pointer: pointer, Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", MaxNameLength)}
	}
	return nil
}

// checkTable refuses a TableInput that is not valid: the name, the description, the columns — at least one, unique
// and named — and the rows — at most MaxEntries, each with a unique key and exactly one value per column
// (column_mismatch otherwise).
func checkTable(in TableInput) error {
	if err := checkName("/name", in.Name); err != nil {
		return err
	}
	if in.Description != nil && utf8.RuneCountInString(*in.Description) > MaxDescriptionLength {
		return &FieldError{Pointer: "/description", Code: CodeTooLong,
			Detail: fmt.Sprintf("The description is longer than %d characters.", MaxDescriptionLength)}
	}
	if len(in.Columns) == 0 {
		return &FieldError{Pointer: "/columns", Code: CodeRequired, Detail: "A Lookup table has at least one column."}
	}
	if len(in.Columns) > MaxColumns {
		return &FieldError{Pointer: "/columns", Code: CodeTooLong,
			Detail: fmt.Sprintf("A Lookup table has at most %d columns.", MaxColumns)}
	}
	seen := map[string]bool{}
	for i, c := range in.Columns {
		pointer := "/columns/" + strconv.Itoa(i)
		if err := checkName(pointer, c); err != nil {
			return err
		}
		if seen[c] {
			return &FieldError{Pointer: pointer, Code: CodeDuplicate, Detail: "The column is already in the table."}
		}
		seen[c] = true
	}
	if len(in.Entries) > MaxEntries {
		return &FieldError{Pointer: "/entries", Code: CodeTooLong,
			Detail: fmt.Sprintf("A Lookup table has at most %d rows.", MaxEntries)}
	}
	keys := map[string]bool{}
	for i, e := range in.Entries {
		pointer := "/entries/" + strconv.Itoa(i)
		if err := checkValue(pointer+"/key", e.Key, true); err != nil {
			return err
		}
		if keys[e.Key] {
			return &FieldError{Pointer: pointer + "/key", Code: CodeDuplicate, Detail: "Another row has this key."}
		}
		keys[e.Key] = true
		if len(e.Values) != len(in.Columns) || !allIn(e.Values, seen) {
			return &FieldError{Pointer: pointer + "/values", Code: CodeColumnMismatch,
				Detail: "The values do not match the columns: the row needs exactly one value per column."}
		}
		for _, c := range slices.Sorted(maps.Keys(e.Values)) {
			if err := checkValue(pointer+"/values/"+escapePointer(c), e.Values[c], false); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkValue refuses a key or value longer than MaxValueLength characters, and an empty key.
func checkValue(pointer, v string, key bool) error {
	if key && v == "" {
		return &FieldError{Pointer: pointer, Code: CodeInvalidFormat, Detail: "The key is empty."}
	}
	if utf8.RuneCountInString(v) > MaxValueLength {
		return &FieldError{Pointer: pointer, Code: CodeTooLong,
			Detail: fmt.Sprintf("The value is longer than %d characters.", MaxValueLength)}
	}
	return nil
}

func allIn(values map[string]string, columns map[string]bool) bool {
	for c := range values {
		if !columns[c] {
			return false
		}
	}
	return true
}

func escapePointer(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}

// nameTaken maps the refusal of the unique constraint key to taken.
func nameTaken(err error, key string, taken error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation &&
		pgErr.ConstraintName == key {
		return taken
	}
	return err
}

// normalize trims the name and keeps the rows in the order of their keys, as they are read back.
func normalize(in TableInput) TableInput {
	in.Name = strings.TrimSpace(in.Name)
	in.Entries = slices.Clone(in.Entries)
	slices.SortFunc(in.Entries, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	return in
}

// CreateTable creates a Lookup table with its rows. A name another table has is ErrTableNameTaken.
func (s *Service) CreateTable(ctx context.Context, r Requester, in TableInput) (Table, error) {
	if err := checkTable(in); err != nil {
		return Table{}, err
	}
	in = normalize(in)
	var created Table
	err := s.store.InTx(ctx, func(q Queries) error {
		id := publicid.New(publicid.LookupTable)
		description := ""
		if in.Description != nil {
			description = *in.Description
		}
		tableID, err := q.InsertLookupTable(ctx, dbgen.InsertLookupTableParams{OrgID: s.orgID, PublicID: id,
			Name: in.Name, Description: description, ColumnNames: in.Columns, Now: s.business.Now().UTC()})
		if err != nil {
			return fmt.Errorf("create the lookup table: %w", nameTaken(err, tableNameKey, ErrTableNameTaken))
		}
		if err := s.writeEntries(ctx, q, tableID, in.Entries); err != nil {
			return err
		}
		if created, err = s.getTable(ctx, q, id); err != nil {
			return err
		}
		return s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionTableCreated, Resource: tableResource(created), Diff: audit.Created(viewOfTable(created)),
			SourceAddress: r.Address})
	})
	if err != nil {
		return Table{}, err
	}
	return created, nil
}

// writeEntries writes the rows of the Lookup table tableID.
func (s *Service) writeEntries(ctx context.Context, q Queries, tableID int64, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	keys := make([]string, len(entries))
	cells := make([][]byte, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
		b, err := json.Marshal(e.Values)
		if err != nil {
			return fmt.Errorf("encode a row of the lookup table: %w", err)
		}
		cells[i] = b
	}
	if err := q.InsertLookupEntries(ctx, dbgen.InsertLookupEntriesParams{LookupTableID: tableID, OrgID: s.orgID,
		Keys: keys, Cells: cells}); err != nil {
		return fmt.Errorf("write the rows of the lookup table: %w", err)
	}
	return nil
}

// UpdateTable replaces the fields and the rows of the Lookup table publicID; a non-nil version must be its current
// one (If-Match).
func (s *Service) UpdateTable(ctx context.Context, r Requester, publicID string, version *int64, in TableInput) (Table,
	error) {
	if err := checkTable(in); err != nil {
		return Table{}, err
	}
	in = normalize(in)
	var updated Table
	err := s.store.InTx(ctx, func(q Queries) error {
		before, err := s.lockTable(ctx, q, publicID)
		if err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		next := before
		next.Name, next.Columns, next.Entries = in.Name, in.Columns, in.Entries
		if in.Description != nil {
			next.Description = *in.Description
		}
		changes := audit.Diff(viewOfTable(before), viewOfTable(next))
		if len(changes) == 0 {
			updated = before
			return nil
		}
		if err := q.UpdateLookupTable(ctx, dbgen.UpdateLookupTableParams{OrgID: s.orgID, ID: before.ID,
			Name: next.Name, Description: next.Description, ColumnNames: next.Columns,
			Now: s.business.Now().UTC()}); err != nil {
			return fmt.Errorf("update the lookup table %s: %w", before.PublicID,
				nameTaken(err, tableNameKey, ErrTableNameTaken))
		}
		if err := q.DeleteLookupEntries(ctx, dbgen.DeleteLookupEntriesParams{OrgID: s.orgID,
			LookupTableID: before.ID}); err != nil {
			return fmt.Errorf("replace the rows of the lookup table %s: %w", before.PublicID, err)
		}
		if err := s.writeEntries(ctx, q, before.ID, next.Entries); err != nil {
			return err
		}
		if updated, err = s.getTable(ctx, q, before.PublicID); err != nil {
			return err
		}
		return s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionTableUpdated, Resource: tableResource(updated), Diff: changes, SourceAddress: r.Address})
	})
	if err != nil {
		return Table{}, err
	}
	return updated, nil
}

// DeleteTable deletes the Lookup table publicID with its rows; a non-nil version must be its current one. A table
// that the URL template of a Link rule reads by its name is ErrInUse.
func (s *Service) DeleteTable(ctx context.Context, r Requester, publicID string, version *int64) error {
	return s.store.InTx(ctx, func(q Queries) error {
		before, err := s.lockTable(ctx, q, publicID)
		if err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		used, err := s.tableUsed(ctx, q, before.Name)
		if err != nil {
			return err
		}
		if used {
			return ErrInUse
		}
		if err := q.DeleteLookupTable(ctx, dbgen.DeleteLookupTableParams{OrgID: s.orgID, ID: before.ID}); err != nil {
			return fmt.Errorf("delete the lookup table %s: %w", before.PublicID, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionTableDeleted, Resource: tableResource(before),
			Diff: audit.Diff(viewOfTable(before), tableView{}), SourceAddress: r.Address})
	})
}

// tableUsed reports whether the URL template of a Link rule calls `lookup` with the table's name.
func (s *Service) tableUsed(ctx context.Context, q Queries, name string) (bool, error) {
	rows, err := q.ListLinkRuleTemplates(ctx, s.orgID)
	if err != nil {
		return false, fmt.Errorf("read the link rules: %w", err)
	}
	for _, r := range rows {
		t, err := s.parse(r.UrlTemplate)
		if err != nil {
			continue // saved templates parse; one that does not reads nothing
		}
		if slices.Contains(t.LookupTables(), name) {
			return true, nil
		}
	}
	return false, nil
}

// lookups is the `lookup` of one render: the cells it read, by table and key, and how many rows it may still read.
type lookups struct {
	ctx   context.Context
	q     Queries
	orgID int64
	cells map[[2]string]map[string]string
	left  int
}

func (s *Service) newLookups(ctx context.Context, q Queries) *lookups {
	return &lookups{ctx: ctx, q: q, orgID: s.orgID, cells: map[[2]string]map[string]string{}, left: MaxLookups}
}

// errTooManyLookups is a render that reads more than MaxLookups rows.
var errTooManyLookups = fmt.Errorf("lookup: the templates read more than %d rows", MaxLookups)

// lookup reads one cell: a missing table, key or column is the empty string.
func (l *lookups) lookup(table, key, column string) (string, error) {
	if !readable(table) || !readable(key) {
		return "", nil // no table or key has such a name, and the database would refuse it
	}
	k := [2]string{table, key}
	row, ok := l.cells[k]
	if !ok {
		if l.left <= 0 {
			return "", errTooManyLookups
		}
		l.left--
		b, err := l.q.GetLookupCells(l.ctx, dbgen.GetLookupCellsParams{OrgID: l.orgID, Name: table, Key: key})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return "", fmt.Errorf("lookup: read the lookup table: %w", err)
		default:
			_ = json.Unmarshal(b, &row) // cells are JSON objects of strings (schema.md)
		}
		l.cells[k] = row
	}
	return row[column], nil
}

// readable reports whether a name or key can exist: valid UTF-8 without NUL and not longer than a key may be, so
// that a lookup never sends the database a value it refuses, which would abort the transaction of the render.
func readable(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && len(s) <= 4*MaxValueLength
}

// env is the template Env of the render.
func (l *lookups) env() templates.Env {
	return templates.Env{Lookup: l.lookup}
}
