// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build lint

package archlint

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	pgparser "github.com/wasilibs/go-pgquery/parser"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const orgColumn = "org_id"

var (
	sqlcNameRe  = regexp.MustCompile(`^--\s*name:\s*(\S+)`)
	orgExemptRe = regexp.MustCompile(`^--\s*archlint:org-exempt(?:\s+(.*))?$`)
)

// checkSQL runs rule 1 (org_id scope) and rule 2 (Alert Group tables written only by the groups package) over the
// query files.
func checkSQL(root string, cfg Config) ([]Diagnostic, error) {
	fsys := os.DirFS(root)
	queries, err := queryFiles(fsys, cfg.QueryGlob)
	if err != nil || len(queries) == 0 {
		return nil, err
	}
	org, err := orgScopedTables(fsys, cfg.MigrationsDir)
	if err != nil {
		return nil, err
	}
	groupTables := make(map[string]bool, len(cfg.GroupTables))
	for _, t := range cfg.GroupTables {
		groupTables[t] = true
	}
	var diags []Diagnostic
	for _, name := range queries {
		src, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		res, err := parseSQL(name, string(src))
		if err != nil {
			return nil, err
		}
		f := &queryFile{
			name:        name,
			src:         string(src),
			lines:       newLineIndex(string(src)),
			org:         org,
			groupTables: groupTables,
			groupsDir:   cfg.GroupsDir,
			guardWrites: !inDir(name, cfg.GroupsDir),
		}
		for _, raw := range res.GetStmts() {
			f.checkStatement(raw)
		}
		diags = append(diags, f.diags...)
	}
	return diags, nil
}

// queryFiles walks the tree like "./..." does: it skips testdata, hidden and underscore directories, node_modules
// and nested modules.
func queryFiles(fsys fs.FS, glob string) ([]string, error) {
	var files []string
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if matchGlob(glob, name) {
				files = append(files, name)
			}
			return nil
		}
		if name == "." {
			return nil
		}
		base := d.Name()
		if base == "testdata" || base == "node_modules" || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") {
			return fs.SkipDir
		}
		if _, err := fs.Stat(fsys, path.Join(name, "go.mod")); err == nil {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("find query files: %w", err)
	}
	return files, nil
}

// matchGlob matches a slash path segment by segment with path.Match; a "**" segment matches any number of segments.
func matchGlob(glob, name string) bool {
	return matchSegments(strings.Split(glob, "/"), strings.Split(name, "/"))
}

func matchSegments(glob, segs []string) bool {
	for len(glob) > 0 {
		if glob[0] == "**" {
			for i := range len(segs) + 1 {
				if matchSegments(glob[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(glob[0], segs[0]); !ok {
			return false
		}
		glob, segs = glob[1:], segs[1:]
	}
	return len(segs) == 0
}

// orgScopedTables replays the up migrations in order and returns the tables that have an org_id column, directly,
// through ALTER TABLE, or as a partition, child or LIKE copy of such a table.
func orgScopedTables(fsys fs.FS, dir string) (map[string]bool, error) {
	migrations, err := fs.Glob(fsys, path.Join(dir, "*.up.sql"))
	if err != nil {
		return nil, fmt.Errorf("find migrations: %w", err)
	}
	if len(migrations) == 0 {
		return nil, fmt.Errorf("rule 1: query files exist, but %s has no *.up.sql migration to read the tables with %s from", dir, orgColumn)
	}
	org := map[string]bool{}
	for _, name := range migrations {
		src, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		res, err := parseSQL(name, string(src))
		if err != nil {
			return nil, err
		}
		for _, raw := range res.GetStmts() {
			applyDDL(org, raw.GetStmt())
		}
	}
	return org, nil
}

func applyDDL(org map[string]bool, n *pg.Node) {
	if cs := n.GetCreateStmt(); cs != nil {
		has := false
		for _, el := range cs.GetTableElts() {
			if el.GetColumnDef().GetColname() == orgColumn || org[el.GetTableLikeClause().GetRelation().GetRelname()] {
				has = true
			}
		}
		for _, parent := range cs.GetInhRelations() {
			if org[parent.GetRangeVar().GetRelname()] {
				has = true
			}
		}
		setOrg(org, cs.GetRelation().GetRelname(), has)
	}
	if at := n.GetAlterTableStmt(); at != nil {
		table := at.GetRelation().GetRelname()
		for _, c := range at.GetCmds() {
			cmd := c.GetAlterTableCmd()
			switch cmd.GetSubtype() {
			case pg.AlterTableType_AT_AddColumn:
				if cmd.GetDef().GetColumnDef().GetColname() == orgColumn {
					org[table] = true
				}
			case pg.AlterTableType_AT_DropColumn:
				if cmd.GetName() == orgColumn {
					delete(org, table)
				}
			case pg.AlterTableType_AT_AttachPartition:
				if org[table] {
					org[cmd.GetDef().GetPartitionCmd().GetName().GetRelname()] = true
				}
			default:
			}
		}
	}
	if rs := n.GetRenameStmt(); rs != nil && rs.GetRenameType() == pg.ObjectType_OBJECT_TABLE {
		old := rs.GetRelation().GetRelname()
		setOrg(org, rs.GetNewname(), org[old])
		delete(org, old)
	}
}

func setOrg(org map[string]bool, table string, has bool) {
	if has {
		org[table] = true
	} else {
		delete(org, table)
	}
}

func parseSQL(name, src string) (*pg.ParseResult, error) {
	res, err := pgquery.Parse(src)
	if err == nil {
		return res, nil
	}
	var perr *pgparser.Error
	if errors.As(err, &perr) && perr.Cursorpos > 0 {
		line, col := newLineIndex(src).position(perr.Cursorpos - 1)
		return nil, fmt.Errorf("%s:%d:%d: %w", name, line, col, err)
	}
	return nil, fmt.Errorf("%s: %w", name, err)
}

// lineIndex holds the byte offset at which each line starts.
type lineIndex []int

func newLineIndex(src string) lineIndex {
	idx := lineIndex{0}
	for i := range len(src) {
		if src[i] == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func (li lineIndex) position(offset int) (line, col int) {
	line = sort.Search(len(li), func(i int) bool { return li[i] > offset })
	return line, offset - li[line-1] + 1
}

type queryFile struct {
	name        string
	src         string
	lines       lineIndex
	org         map[string]bool
	groupTables map[string]bool
	groupsDir   string
	guardWrites bool
	diags       []Diagnostic
}

type statement struct {
	file      *queryFile
	label     string
	start     int
	orgExempt bool
}

// checkStatement checks one statement. The comments between the previous statement and this one belong to it: the
// sqlc "-- name:" header and an "-- archlint:org-exempt <reason>" exemption from rule 1.
func (f *queryFile) checkStatement(raw *pg.RawStmt) {
	start := int(raw.GetStmtLocation())
	end := len(f.src)
	if raw.GetStmtLen() > 0 {
		end = start + int(raw.GetStmtLen())
	}
	comments, code := leadingComments(f.src[start:end])
	s := &statement{file: f, start: start + code}
	line, _ := f.lines.position(s.start)
	s.label = fmt.Sprintf("query at line %d", line)
	for _, c := range comments {
		if m := sqlcNameRe.FindStringSubmatch(c); m != nil {
			s.label = "query " + m[1]
		}
	}
	for _, c := range comments {
		m := orgExemptRe.FindStringSubmatch(c)
		switch {
		case m == nil:
		case strings.TrimSpace(m[1]) == "":
			s.report(1, -1, "has an archlint:org-exempt comment without a reason")
		default:
			s.orgExempt = true
		}
	}
	s.walk(raw.GetStmt().ProtoReflect(), nil)
}

// leadingComments returns the "--" comment lines in front of a statement and the offset of its first token.
func leadingComments(seg string) (comments []string, code int) {
	for code < len(seg) {
		rest := seg[code:]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		code += len(rest) - len(trimmed)
		switch {
		case strings.HasPrefix(trimmed, "--"):
			line, _, _ := strings.Cut(trimmed, "\n")
			comments = append(comments, strings.TrimSpace(line))
			code += len(line)
		case strings.HasPrefix(trimmed, "/*"):
			_, after, ok := strings.Cut(trimmed[2:], "*/")
			if !ok {
				return comments, len(seg)
			}
			code += len(trimmed) - len(after)
		default:
			return comments, code
		}
	}
	return comments, code
}

func (s *statement) report(rule int, offset int32, format string, args ...any) {
	if rule == 1 && s.orgExempt {
		return
	}
	at := int(offset)
	if at < 0 {
		at = s.start
	}
	line, col := s.file.lines.position(at)
	s.file.diags = append(s.file.diags, Diagnostic{
		Rule:    rule,
		Path:    s.file.name,
		Line:    line,
		Col:     col,
		Message: s.label + " " + fmt.Sprintf(format, args...),
	})
}

// walk visits every node; each SELECT, INSERT, UPDATE, DELETE and MERGE, at any depth, is one query level.
func (s *statement) walk(m protoreflect.Message, ctes *cteScope) {
	switch n := m.Interface().(type) {
	case *pg.SelectStmt:
		ctes = ctes.with(n.GetWithClause())
		lv := s.newLevel(ctes)
		for _, from := range n.GetFromClause() {
			lv.addFrom(from)
		}
		lv.conds = conjuncts(n.GetWhereClause(), lv.conds)
		s.checkLevel(lv)
	case *pg.UpdateStmt:
		ctes = ctes.with(n.GetWithClause())
		s.checkWrite(n.GetRelation())
		lv := s.newLevel(ctes)
		lv.addRelation(n.GetRelation(), "updates")
		for _, from := range n.GetFromClause() {
			lv.addFrom(from)
		}
		lv.conds = conjuncts(n.GetWhereClause(), lv.conds)
		s.checkLevel(lv)
	case *pg.DeleteStmt:
		ctes = ctes.with(n.GetWithClause())
		s.checkWrite(n.GetRelation())
		lv := s.newLevel(ctes)
		lv.addRelation(n.GetRelation(), "deletes from")
		for _, from := range n.GetUsingClause() {
			lv.addFrom(from)
		}
		lv.conds = conjuncts(n.GetWhereClause(), lv.conds)
		s.checkLevel(lv)
	case *pg.InsertStmt:
		ctes = ctes.with(n.GetWithClause())
		s.checkWrite(n.GetRelation())
		s.checkInsert(n)
	case *pg.MergeStmt:
		ctes = ctes.with(n.GetWithClause())
		s.checkWrite(n.GetRelation())
		lv := s.newLevel(ctes)
		lv.addRelation(n.GetRelation(), "merges into")
		lv.addFrom(n.GetSourceRelation())
		lv.conds = conjuncts(n.GetJoinCondition(), lv.conds)
		s.checkLevel(lv)
	}
	eachChild(m, func(child protoreflect.Message) { s.walk(child, ctes) })
}

func eachChild(m protoreflect.Message, visit func(protoreflect.Message)) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.Message() == nil || fd.IsMap():
		case fd.IsList():
			list := v.List()
			for i := range list.Len() {
				visit(list.Get(i).Message())
			}
		default:
			visit(v.Message())
		}
		return true
	})
}

func (s *statement) checkWrite(rv *pg.RangeVar) {
	if s.file.guardWrites && s.file.groupTables[rv.GetRelname()] {
		s.report(2, rv.GetLocation(), "writes %s outside %s; Alert Group tables change only through the groups dispatcher",
			rv.GetRelname(), s.file.groupsDir)
	}
}

func (s *statement) checkInsert(n *pg.InsertStmt) {
	rv := n.GetRelation()
	if !s.file.org[rv.GetRelname()] {
		return
	}
	if !slices.ContainsFunc(n.GetCols(), func(c *pg.Node) bool { return c.GetResTarget().GetName() == orgColumn }) {
		s.report(1, rv.GetLocation(), "inserts into %s without an %s column", rv.GetRelname(), orgColumn)
	}
}

type cteScope struct {
	names  []string
	parent *cteScope
}

func (c *cteScope) with(w *pg.WithClause) *cteScope {
	if len(w.GetCtes()) == 0 {
		return c
	}
	inner := &cteScope{parent: c}
	for _, cte := range w.GetCtes() {
		inner.names = append(inner.names, cte.GetCommonTableExpr().GetCtename())
	}
	return inner
}

func (c *cteScope) has(name string) bool {
	for ; c != nil; c = c.parent {
		if slices.Contains(c.names, name) {
			return true
		}
	}
	return false
}

// fromItem is a FROM item of one query level: a table, a CTE reference, a subquery or a function.
type fromItem struct {
	name   string // the alias, or the table name
	table  string // empty unless the item is a table
	org    bool
	verb   string
	offset int32
}

type level struct {
	org   map[string]bool
	ctes  *cteScope
	items []*fromItem
	conds []*pg.Node
	links [][2]*fromItem
}

func (s *statement) newLevel(ctes *cteScope) *level {
	return &level{org: s.file.org, ctes: ctes}
}

func (lv *level) addRelation(rv *pg.RangeVar, verb string) {
	it := &fromItem{name: rv.GetRelname(), verb: verb, offset: rv.GetLocation()}
	if alias := rv.GetAlias().GetAliasname(); alias != "" {
		it.name = alias
	}
	if rv.GetSchemaname() != "" || !lv.ctes.has(rv.GetRelname()) {
		it.table = rv.GetRelname()
		it.org = lv.org[it.table]
	}
	lv.items = append(lv.items, it)
}

// addFrom adds the items of one FROM entry, with the conditions of its joins, and returns the items it added.
func (lv *level) addFrom(n *pg.Node) []*fromItem {
	start := len(lv.items)
	switch {
	case n.GetRangeVar() != nil:
		lv.addRelation(n.GetRangeVar(), "reads")
	case n.GetJoinExpr() != nil:
		j := n.GetJoinExpr()
		left := lv.addFrom(j.GetLarg())
		right := lv.addFrom(j.GetRarg())
		lv.conds = conjuncts(j.GetQuals(), lv.conds)
		if slices.ContainsFunc(j.GetUsingClause(), func(u *pg.Node) bool { return u.GetString_().GetSval() == orgColumn }) {
			for _, l := range left {
				for _, r := range right {
					lv.links = append(lv.links, [2]*fromItem{l, r})
				}
			}
		}
	case n.GetRangeSubselect() != nil:
		lv.items = append(lv.items, &fromItem{name: n.GetRangeSubselect().GetAlias().GetAliasname()})
	case n.GetRangeFunction() != nil:
		lv.items = append(lv.items, &fromItem{name: n.GetRangeFunction().GetAlias().GetAliasname()})
	case n.GetRangeTableSample() != nil:
		lv.addFrom(n.GetRangeTableSample().GetRelation())
	}
	return slices.Clone(lv.items[start:])
}

func (lv *level) item(name string) *fromItem {
	for _, it := range lv.items {
		if it.name == name {
			return it
		}
	}
	return nil
}

// conjuncts appends the operands of a top-level AND; a condition under OR or NOT filters nothing for sure.
func conjuncts(n *pg.Node, out []*pg.Node) []*pg.Node {
	if n == nil {
		return out
	}
	if b := n.GetBoolExpr(); b != nil && b.GetBoolop() == pg.BoolExprType_AND_EXPR {
		for _, arg := range b.GetArgs() {
			out = conjuncts(arg, out)
		}
		return out
	}
	return append(out, n)
}

// checkLevel reports every org-scoped table of the level that is not anchored. A table is anchored when a condition
// compares its org_id (=, IN, = ANY) with a value that does not come from a FROM item of the same level, or with the
// org_id of another table of the level that is anchored.
func (s *statement) checkLevel(lv *level) {
	if !slices.ContainsFunc(lv.items, func(it *fromItem) bool { return it.org }) {
		return
	}
	anchored := map[*fromItem]bool{}
	links := slices.Clone(lv.links)
	pair := func(x, y *pg.Node) {
		it := lv.orgRef(x)
		if it == nil {
			return
		}
		if other := lv.orgRef(y); other != nil {
			links = append(links, [2]*fromItem{it, other})
		} else if !lv.usesLocalColumn(y) {
			anchored[it] = true
		}
	}
	for _, c := range lv.conds {
		if e := c.GetAExpr(); e != nil && operator(e.GetName()) == "=" {
			switch e.GetKind() {
			case pg.A_Expr_Kind_AEXPR_OP:
				pair(e.GetLexpr(), e.GetRexpr())
				pair(e.GetRexpr(), e.GetLexpr())
			case pg.A_Expr_Kind_AEXPR_IN, pg.A_Expr_Kind_AEXPR_OP_ANY:
				pair(e.GetLexpr(), e.GetRexpr())
			default:
			}
		}
		if sl := c.GetSubLink(); sl != nil && sl.GetSubLinkType() == pg.SubLinkType_ANY_SUBLINK &&
			(len(sl.GetOperName()) == 0 || operator(sl.GetOperName()) == "=") {
			if it := lv.orgRef(sl.GetTestexpr()); it != nil {
				anchored[it] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, l := range links {
			if anchored[l[0]] != anchored[l[1]] {
				anchored[l[0]], anchored[l[1]] = true, true
				changed = true
			}
		}
	}
	for _, it := range lv.items {
		if it.org && !anchored[it] {
			s.report(1, it.offset, "%s %s without filtering by %s", it.verb, it.table, orgColumn)
		}
	}
}

// orgRef returns the org-scoped table of the level whose org_id column n is. An unqualified org_id counts only when
// the level has a single org-scoped table.
func (lv *level) orgRef(n *pg.Node) *fromItem {
	fields := columnFields(stripCasts(n).GetColumnRef())
	if len(fields) == 0 || fields[len(fields)-1] != orgColumn {
		return nil
	}
	if len(fields) == 1 {
		var only *fromItem
		for _, it := range lv.items {
			if it.org {
				if only != nil {
					return nil
				}
				only = it
			}
		}
		return only
	}
	if it := lv.item(fields[len(fields)-2]); it != nil && it.org {
		return it
	}
	return nil
}

// usesLocalColumn reports whether n reads a column of a FROM item of this level. Subqueries and sqlc parameters
// (@name, sqlc.arg, sqlc.narg, sqlc.slice) are values; so is the org_id of a CTE or subquery, which is checked at its
// own level.
func (lv *level) usesLocalColumn(n *pg.Node) bool {
	found := false
	var visit func(m protoreflect.Message)
	visit = func(m protoreflect.Message) {
		if found {
			return
		}
		switch x := m.Interface().(type) {
		case *pg.SubLink, *pg.SelectStmt:
			return
		case *pg.A_Expr:
			if x.GetKind() == pg.A_Expr_Kind_AEXPR_OP && x.GetLexpr() == nil && operator(x.GetName()) == "@" {
				return
			}
		case *pg.FuncCall:
			if names := x.GetFuncname(); len(names) == 2 && names[0].GetString_().GetSval() == "sqlc" {
				return
			}
		case *pg.ColumnRef:
			found = lv.isLocal(columnFields(x))
			return
		}
		eachChild(m, visit)
	}
	if n != nil {
		visit(n.ProtoReflect())
	}
	return found
}

func (lv *level) isLocal(fields []string) bool {
	if len(fields) == 1 {
		return len(lv.items) > 0
	}
	it := lv.item(fields[len(fields)-2])
	if it == nil {
		return false
	}
	return it.table != "" || fields[len(fields)-1] != orgColumn
}

func columnFields(cr *pg.ColumnRef) []string {
	fields := make([]string, 0, len(cr.GetFields()))
	for _, f := range cr.GetFields() {
		if s := f.GetString_(); s != nil {
			fields = append(fields, s.GetSval())
		} else {
			fields = append(fields, "*")
		}
	}
	return fields
}

func stripCasts(n *pg.Node) *pg.Node {
	for {
		switch {
		case n.GetTypeCast() != nil:
			n = n.GetTypeCast().GetArg()
		case n.GetCollateClause() != nil:
			n = n.GetCollateClause().GetArg()
		default:
			return n
		}
	}
}

// operator returns the operator name without its schema, so OPERATOR(pg_catalog.=) is "=".
func operator(name []*pg.Node) string {
	if len(name) == 0 {
		return ""
	}
	return name[len(name)-1].GetString_().GetSval()
}
