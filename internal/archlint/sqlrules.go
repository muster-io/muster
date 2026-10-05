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
// through ALTER TABLE, or as a partition, child or LIKE copy of such a table, and the views that output org_id.
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
	if vs := n.GetViewStmt(); vs != nil {
		setOrg(org, vs.GetView().GetRelname(), viewHasOrg(org, vs))
	}
	if rs := n.GetRenameStmt(); rs != nil && rs.GetRenameType() == pg.ObjectType_OBJECT_TABLE {
		old := rs.GetRelation().GetRelname()
		setOrg(org, rs.GetNewname(), org[old])
		delete(org, old)
	}
}

// viewHasOrg reports whether the output columns of a view include org_id: through its column list, a select-list
// entry named org_id, or a "*" over a table with org_id.
func viewHasOrg(org map[string]bool, v *pg.ViewStmt) bool {
	aliases := v.GetAliases()
	if slices.ContainsFunc(aliases, func(a *pg.Node) bool { return a.GetString_().GetSval() == orgColumn }) {
		return true
	}
	sel := v.GetQuery().GetSelectStmt()
	for sel.GetLarg() != nil {
		sel = sel.GetLarg()
	}
	for _, t := range sel.GetTargetList()[min(len(aliases), len(sel.GetTargetList())):] {
		rt := t.GetResTarget()
		if rt.GetName() != "" {
			if rt.GetName() == orgColumn {
				return true
			}
			continue
		}
		fields := columnFields(rt.GetVal().GetColumnRef())
		if len(fields) == 0 {
			continue
		}
		last := fields[len(fields)-1]
		if last == orgColumn || (last == "*" && readsOrgTable(org, sel)) {
			return true
		}
	}
	return false
}

func readsOrgTable(org map[string]bool, sel *pg.SelectStmt) bool {
	found := false
	var visit func(m protoreflect.Message)
	visit = func(m protoreflect.Message) {
		if rv, ok := m.Interface().(*pg.RangeVar); ok && org[rv.GetRelname()] {
			found = true
		}
		if !found {
			eachChild(m, visit)
		}
	}
	for _, from := range sel.GetFromClause() {
		visit(from.ProtoReflect())
	}
	return found
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
	// valued memoizes orgValued per CTE or subquery body.
	valued map[any]bool
}

// checkStatement checks one statement. The comments in front of it belong to it: the sqlc "-- name:" header and an
// "-- archlint:org-exempt <reason>" exemption from rule 1.
func (f *queryFile) checkStatement(raw *pg.RawStmt) {
	start := int(raw.GetStmtLocation())
	end := len(f.src)
	if raw.GetStmtLen() > 0 {
		end = start + int(raw.GetStmtLen())
	}
	comments, code := leadingComments(f.src[start:end], start > 0)
	s := &statement{file: f, start: start + code, valued: map[any]bool{}}
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
	s.walk(raw.GetStmt().ProtoReflect(), nil, nil)
}

// leadingComments returns the "--" comment lines in front of a statement and the offset of its first token. After a
// previous statement (afterStatement), the comments on the rest of that statement's last line belong to it, so this
// statement's comments start on the next line.
func leadingComments(seg string, afterStatement bool) (comments []string, code int) {
	trailing := afterStatement
	for code < len(seg) {
		rest := seg[code:]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		if strings.Contains(rest[:len(rest)-len(trimmed)], "\n") {
			trailing = false
		}
		code += len(rest) - len(trimmed)
		switch {
		case strings.HasPrefix(trimmed, "--"):
			line, _, _ := strings.Cut(trimmed, "\n")
			if !trailing {
				comments = append(comments, strings.TrimSpace(line))
			}
			code += len(line)
		case strings.HasPrefix(trimmed, "/*"):
			body, after, ok := strings.Cut(trimmed[2:], "*/")
			if !ok {
				return comments, len(seg)
			}
			if strings.Contains(body, "\n") {
				trailing = false
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

// walk visits every node. Each SELECT, UPDATE, DELETE and MERGE, at any depth, is one query level, checked on its
// own; outer is the level the node is nested in.
func (s *statement) walk(m protoreflect.Message, ctes *cteScope, outer *level) {
	switch n := m.Interface().(type) {
	case *pg.InsertStmt:
		s.checkWrite(n.GetRelation())
		s.checkInsert(n)
	case *pg.UpdateStmt:
		s.checkWrite(n.GetRelation())
	case *pg.DeleteStmt:
		s.checkWrite(n.GetRelation())
	case *pg.MergeStmt:
		s.checkWrite(n.GetRelation())
	case *pg.TruncateStmt:
		s.checkTruncate(n)
	}
	ctes = ctes.with(withClause(m), outer)
	if lv := s.newLevel(m, ctes, outer); lv != nil {
		s.checkLevel(lv)
		outer = lv
	}
	eachChild(m, func(child protoreflect.Message) { s.walk(child, ctes, outer) })
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

func withClause(m protoreflect.Message) *pg.WithClause {
	switch n := m.Interface().(type) {
	case *pg.SelectStmt:
		return n.GetWithClause()
	case *pg.InsertStmt:
		return n.GetWithClause()
	case *pg.UpdateStmt:
		return n.GetWithClause()
	case *pg.DeleteStmt:
		return n.GetWithClause()
	case *pg.MergeStmt:
		return n.GetWithClause()
	}
	return nil
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

func (s *statement) checkTruncate(n *pg.TruncateStmt) {
	for _, rel := range n.GetRelations() {
		rv := rel.GetRangeVar()
		s.checkWrite(rv)
		if s.file.org[rv.GetRelname()] {
			s.report(1, rv.GetLocation(), "truncates %s, which holds the rows of every Organization; delete by %s instead",
				rv.GetRelname(), orgColumn)
		}
	}
}

// cteScope holds the CTEs of one WITH clause; outer is the level of the statement that has the clause.
type cteScope struct {
	ctes   []*pg.CommonTableExpr
	outer  *level
	parent *cteScope
}

func (c *cteScope) with(w *pg.WithClause, outer *level) *cteScope {
	if len(w.GetCtes()) == 0 {
		return c
	}
	inner := &cteScope{outer: outer, parent: c}
	for _, cte := range w.GetCtes() {
		inner.ctes = append(inner.ctes, cte.GetCommonTableExpr())
	}
	return inner
}

// lookup returns the CTE a table name refers to and the scope that defines it.
func (c *cteScope) lookup(name string) (*pg.CommonTableExpr, *cteScope) {
	for ; c != nil; c = c.parent {
		for _, cte := range c.ctes {
			if cte.GetCtename() == name {
				return cte, c
			}
		}
	}
	return nil, nil
}

// fromItem is a FROM item of one query level: a table, or a CTE reference, subquery or function.
type fromItem struct {
	name   string // the alias, or the table name
	table  string // empty unless the item is a table
	org    bool
	verb   string
	offset int32
	// body is the query of a CTE or subquery, with the CTEs and the level it is evaluated in.
	body  *pg.Node
	ctes  *cteScope
	outer *level
}

// cond is a condition that can filter the items in scope, or every item of the level when scope is nil.
type cond struct {
	expr  *pg.Node
	scope []*fromItem
}

// link carries a filter from one item to another: a comparison of their org_id columns.
type link struct {
	from, to *fromItem
}

type level struct {
	org   map[string]bool
	ctes  *cteScope
	outer *level
	items []*fromItem
	conds []cond
	links []link
}

// newLevel returns the query level of a SELECT, UPDATE, DELETE or MERGE, with its FROM items and conditions, or nil
// for any other node. ctes must include the statement's own WITH clause.
func (s *statement) newLevel(m protoreflect.Message, ctes *cteScope, outer *level) *level {
	lv := &level{org: s.file.org, ctes: ctes, outer: outer}
	switch n := m.Interface().(type) {
	case *pg.SelectStmt:
		lv.addFroms(n.GetFromClause())
		lv.addConds(n.GetWhereClause(), nil)
	case *pg.UpdateStmt:
		lv.addRelation(n.GetRelation(), "updates")
		lv.addFroms(n.GetFromClause())
		lv.addConds(n.GetWhereClause(), nil)
	case *pg.DeleteStmt:
		lv.addRelation(n.GetRelation(), "deletes from")
		lv.addFroms(n.GetUsingClause())
		lv.addConds(n.GetWhereClause(), nil)
	case *pg.MergeStmt:
		lv.addRelation(n.GetRelation(), "merges into")
		lv.addFrom(n.GetSourceRelation())
		lv.addConds(n.GetJoinCondition(), nil)
	default:
		return nil
	}
	return lv
}

func (lv *level) addRelation(rv *pg.RangeVar, verb string) {
	it := &fromItem{name: rv.GetRelname(), verb: verb, offset: rv.GetLocation()}
	if alias := rv.GetAlias().GetAliasname(); alias != "" {
		it.name = alias
	}
	if cte, scope := lv.ctes.lookup(rv.GetRelname()); cte != nil && rv.GetSchemaname() == "" {
		it.body, it.ctes, it.outer = cte.GetCtequery(), scope, scope.outer
	} else {
		it.table = rv.GetRelname()
		it.org = lv.org[it.table]
	}
	lv.items = append(lv.items, it)
}

func (lv *level) addFroms(froms []*pg.Node) {
	for _, from := range froms {
		lv.addFrom(from)
	}
}

// addFrom adds the items of one FROM entry and the conditions of its joins, and returns the items it added. The ON
// condition of an outer join filters only the side the join can make null.
func (lv *level) addFrom(n *pg.Node) []*fromItem {
	start := len(lv.items)
	switch {
	case n.GetRangeVar() != nil:
		lv.addRelation(n.GetRangeVar(), "reads")
	case n.GetJoinExpr() != nil:
		j := n.GetJoinExpr()
		left := lv.addFrom(j.GetLarg())
		right := lv.addFrom(j.GetRarg())
		scope := []*fromItem{}
		switch j.GetJointype() {
		case pg.JoinType_JOIN_LEFT:
			scope = append(scope, right...)
		case pg.JoinType_JOIN_RIGHT:
			scope = append(scope, left...)
		case pg.JoinType_JOIN_FULL:
		default:
			scope = slices.Concat(scope, left, right)
		}
		lv.addConds(j.GetQuals(), scope)
		if slices.ContainsFunc(j.GetUsingClause(), func(u *pg.Node) bool { return u.GetString_().GetSval() == orgColumn }) {
			for _, l := range left {
				for _, r := range right {
					lv.addLink(l, r, scope)
				}
			}
		}
	case n.GetRangeSubselect() != nil:
		rs := n.GetRangeSubselect()
		lv.items = append(lv.items, &fromItem{name: rs.GetAlias().GetAliasname(), body: rs.GetSubquery(), ctes: lv.ctes, outer: lv})
	case n.GetRangeFunction() != nil:
		lv.items = append(lv.items, &fromItem{name: n.GetRangeFunction().GetAlias().GetAliasname()})
	case n.GetRangeTableSample() != nil:
		lv.addFrom(n.GetRangeTableSample().GetRelation())
	}
	return slices.Clone(lv.items[start:])
}

// addConds adds the operands of a top-level AND; a condition under OR or NOT filters nothing for sure.
func (lv *level) addConds(n *pg.Node, scope []*fromItem) {
	if n == nil {
		return
	}
	if b := n.GetBoolExpr(); b != nil && b.GetBoolop() == pg.BoolExprType_AND_EXPR {
		for _, arg := range b.GetArgs() {
			lv.addConds(arg, scope)
		}
		return
	}
	lv.conds = append(lv.conds, cond{expr: n, scope: scope})
}

// addLink records that a and b have equal org_id under a condition that can filter the items in scope.
func (lv *level) addLink(a, b *fromItem, scope []*fromItem) {
	if scope == nil || slices.Contains(scope, b) {
		lv.links = append(lv.links, link{from: a, to: b})
	}
	if scope == nil || slices.Contains(scope, a) {
		lv.links = append(lv.links, link{from: b, to: a})
	}
}

func (lv *level) item(name string) *fromItem {
	for _, it := range lv.items {
		if it.name == name {
			return it
		}
	}
	return nil
}

// checkLevel reports every org-scoped table of the level that anchor does not find filtered.
func (s *statement) checkLevel(lv *level) {
	if !slices.ContainsFunc(lv.items, func(it *fromItem) bool { return it.org }) {
		return
	}
	anchored := s.anchor(lv)
	for _, it := range lv.items {
		if it.org && !anchored[it] {
			s.report(1, it.offset, "%s %s without filtering by %s at this query level", it.verb, it.table, orgColumn)
		}
	}
}

// anchor returns the org-scoped tables of the level that are filtered by org_id: a condition compares their org_id
// with a value (see isValue), or with the org_id of a table that is filtered.
func (s *statement) anchor(lv *level) map[*fromItem]bool {
	anchored := map[*fromItem]bool{}
	links := slices.Clone(lv.links)
	for _, c := range lv.conds {
		for _, p := range comparisons(c.expr) {
			it := lv.orgRef(p[0])
			if it == nil || (c.scope != nil && !slices.Contains(c.scope, it)) {
				continue
			}
			if other := lv.orgRef(p[1]); other != nil {
				links = append(links, link{from: other, to: it})
			} else if s.isValue(lv, p[1]) {
				anchored[it] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, l := range links {
			if anchored[l.from] && !anchored[l.to] {
				anchored[l.to] = true
				changed = true
			}
		}
	}
	return anchored
}

// comparisons returns the (operand, other operand) pairs of a condition that can filter by org_id: both directions
// of =, IS NOT DISTINCT FROM and each column of a row comparison, and the left operand of IN, = ANY and IN (subquery).
func comparisons(c *pg.Node) [][2]*pg.Node {
	if e := c.GetAExpr(); e != nil && operator(e.GetName()) == "=" {
		l, r := e.GetLexpr(), e.GetRexpr()
		switch e.GetKind() {
		case pg.A_Expr_Kind_AEXPR_OP, pg.A_Expr_Kind_AEXPR_NOT_DISTINCT:
			ls, rs := rowArgs(l), rowArgs(r)
			if len(ls) != len(rs) {
				ls, rs = []*pg.Node{l}, []*pg.Node{r}
			}
			var pairs [][2]*pg.Node
			for i := range ls {
				pairs = append(pairs, [2]*pg.Node{ls[i], rs[i]}, [2]*pg.Node{rs[i], ls[i]})
			}
			return pairs
		case pg.A_Expr_Kind_AEXPR_IN, pg.A_Expr_Kind_AEXPR_OP_ANY:
			return [][2]*pg.Node{{l, r}}
		default:
		}
	}
	if sl := c.GetSubLink(); sl != nil && sl.GetSubLinkType() == pg.SubLinkType_ANY_SUBLINK &&
		(len(sl.GetOperName()) == 0 || operator(sl.GetOperName()) == "=") {
		return [][2]*pg.Node{{sl.GetTestexpr(), c}}
	}
	return nil
}

func rowArgs(n *pg.Node) []*pg.Node {
	if row := n.GetRowExpr(); row != nil {
		return row.GetArgs()
	}
	return []*pg.Node{n}
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

// isValue reports whether n is a value an org_id filter can compare with. It must take a parameter ($1, @name,
// sqlc.arg, sqlc.narg, sqlc.slice), a column of an outer query level, or the org_id of a CTE or subquery that
// orgValued accepts, and no other column of this level. A subquery counts when it takes a parameter or orgValued
// accepts it. A constant, or an unfiltered read of organizations, is not a value.
func (s *statement) isValue(lv *level, n *pg.Node) bool {
	source, local := false, false
	var visit func(m protoreflect.Message)
	visit = func(m protoreflect.Message) {
		if isParam(m) {
			source = true
			return
		}
		switch x := m.Interface().(type) {
		case *pg.SubLink:
			if takesParam(x.GetSubselect()) || s.orgValued(x.GetSubselect(), lv.ctes, lv.outer) {
				source = true
			}
			return
		case *pg.ColumnRef:
			fields := columnFields(x)
			it, depth := lv.resolve(fields)
			switch {
			case depth < 0:
			case it != nil && it.body != nil && fields[len(fields)-1] == orgColumn:
				if s.orgValued(it.body, it.ctes, it.outer) {
					source = true
				} else if depth == 0 {
					local = true
				}
			case depth == 0:
				local = true
			default:
				source = true
			}
			return
		}
		eachChild(m, visit)
	}
	if n != nil {
		visit(n.ProtoReflect())
	}
	return source && !local
}

// orgValued reports whether the org_id of a CTE or subquery body is a value: the body reads, at its own level, an
// org-scoped table that is filtered by org_id, or another CTE or subquery that orgValued accepts. A set operation
// needs it in every branch. A recursive CTE's reference to itself is taken to carry the filter of its other branch.
func (s *statement) orgValued(body *pg.Node, ctes *cteScope, outer *level) bool {
	switch {
	case body.GetSelectStmt() != nil:
		return s.stmtOrgValued(body.GetSelectStmt().ProtoReflect(), ctes, outer)
	case body.GetUpdateStmt() != nil:
		return s.stmtOrgValued(body.GetUpdateStmt().ProtoReflect(), ctes, outer)
	case body.GetDeleteStmt() != nil:
		return s.stmtOrgValued(body.GetDeleteStmt().ProtoReflect(), ctes, outer)
	}
	return false
}

func (s *statement) stmtOrgValued(m protoreflect.Message, ctes *cteScope, outer *level) bool {
	key := m.Interface()
	if v, ok := s.valued[key]; ok {
		return v
	}
	s.valued[key] = true
	ctes = ctes.with(withClause(m), outer)
	v := false
	if sel, ok := key.(*pg.SelectStmt); ok && sel.GetLarg() != nil {
		v = s.stmtOrgValued(sel.GetLarg().ProtoReflect(), ctes, outer) && s.stmtOrgValued(sel.GetRarg().ProtoReflect(), ctes, outer)
	} else if lv := s.newLevel(m, ctes, outer); lv != nil {
		anchored := s.anchor(lv)
		v = slices.ContainsFunc(lv.items, func(it *fromItem) bool {
			return anchored[it] || (it.body != nil && s.orgValued(it.body, it.ctes, it.outer))
		})
	}
	s.valued[key] = v
	return v
}

// resolve finds the FROM item a column belongs to and how many query levels out it is: 0 for this level, -1 when it
// is not known. An unqualified column belongs to the nearest level that has FROM items.
func (lv *level) resolve(fields []string) (*fromItem, int) {
	depth := 0
	for l := lv; l != nil; l, depth = l.outer, depth+1 {
		if len(fields) == 1 {
			if len(l.items) > 0 {
				return nil, depth
			}
			continue
		}
		if it := l.item(fields[len(fields)-2]); it != nil {
			return it, depth
		}
	}
	return nil, -1
}

// isParam reports whether m is a query parameter: $n, the sqlc forms @name, sqlc.arg, sqlc.narg and sqlc.slice.
func isParam(m protoreflect.Message) bool {
	switch x := m.Interface().(type) {
	case *pg.ParamRef:
		return true
	case *pg.A_Expr:
		return x.GetKind() == pg.A_Expr_Kind_AEXPR_OP && x.GetLexpr() == nil && operator(x.GetName()) == "@"
	case *pg.FuncCall:
		names := x.GetFuncname()
		return len(names) == 2 && names[0].GetString_().GetSval() == "sqlc"
	}
	return false
}

func takesParam(n *pg.Node) bool {
	found := false
	var visit func(m protoreflect.Message)
	visit = func(m protoreflect.Message) {
		if found = found || isParam(m); !found {
			eachChild(m, visit)
		}
	}
	visit(n.ProtoReflect())
	return found
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
