// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package templates is the template sandbox of ADR-0012 (C-12.FR-4): Go text/template with the sprout registries
// strings, lists, maps, conversions and regular expressions (RE2) registered explicitly, Alertmanager's template
// functions under their Alertmanager names, and `now` from Muster's business clock. Anything else is
// unknown_function. A running template cannot be interrupted, so its execution is bounded instead: `until`, `seq`,
// `repeat` and a range over an integer are capped at LoopCap items, every range and every template call counts
// against a budget per execution, a function result longer than OutputCap characters or ResultItems items is an
// error, output stops at OutputCap characters, and the range and template guards stop an execution that ran for
// longer than ExecutionLimit on the real clock. One sandbox serves messages, Link rules and outgoing webhooks: `lookup`
// reads one cell of a Lookup table through the Env of an execution, and `mention` writes a trusted Mention token that
// only an adapter turns into a Mention (C-12.FR-8, FR-9).
package templates

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-sprout/sprout"
	"github.com/go-sprout/sprout/registry/conversion"
	"github.com/go-sprout/sprout/registry/maps"
	"github.com/go-sprout/sprout/registry/regex"
	sproutslices "github.com/go-sprout/sprout/registry/slices"
	sproutstrings "github.com/go-sprout/sprout/registry/strings"

	"github.com/muster-io/muster/internal/clock"
)

// The limits of the sandbox: template.output_cap, the cap of the functions that produce loops, the largest list or
// map a function may return, the items all the ranges of one execution may visit, the template calls of one
// execution, and how long an execution may run before its next range or template call stops it.
const (
	OutputCap      = 50_000
	LoopCap        = 1_000
	ResultItems    = 10_000
	RangeBudget    = 100_000
	CallBudget     = 10_000
	ExecutionLimit = 2 * time.Second
)

// The codes of Error, as the API names them (ProblemError).
const (
	CodeSyntax          = "template_syntax"
	CodeUnknownFunction = "unknown_function"
)

// Error is a template that does not parse, calls a function the sandbox does not have, or fails while it runs: its
// code, its 1-based position when known, and what went wrong.
type Error struct {
	Code   string
	Line   int
	Column int
	Detail string
}

func (e *Error) Error() string {
	switch {
	case e.Line > 0 && e.Column > 0:
		return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Column, e.Detail)
	case e.Line > 0:
		return fmt.Sprintf("line %d: %s", e.Line, e.Detail)
	}
	return e.Detail
}

// Sandbox parses and runs templates. Business is the clock of `now` and `since`, Real the clock of ExecutionLimit.
type Sandbox struct {
	business clock.Clock
	real     clock.Clock
	funcs    template.FuncMap
}

// builtins are the functions of text/template itself; print, printf and println are replaced by bounded versions.
var builtins = []string{"and", "call", "html", "index", "slice", "js", "len", "not", "or", "print", "printf",
	"println", "urlquery", "eq", "ge", "gt", "le", "lt", "ne"}

// The guards the sandbox adds to every template: a range's collection passes through rangeGuard, a template call's
// data through callGuard.
const (
	rangeGuard = "_range"
	callGuard  = "_call"
)

// New is the sandbox whose `now` follows business and whose execution limit follows real.
func New(business, realClock clock.Clock) *Sandbox {
	return &Sandbox{business: business, real: realClock, funcs: baseFuncs()}
}

// baseFuncs is the function map of every template: the sprout registries, Alertmanager's functions and the bounded
// replacements, each wrapped so that a result above the limits is an error.
func baseFuncs() template.FuncMap {
	fm := template.FuncMap{}
	h := sprout.New()
	for _, r := range []sprout.Registry{sproutstrings.NewRegistry(), sproutslices.NewRegistry(), maps.NewRegistry(),
		conversion.NewRegistry(), regex.NewRegistry()} {
		// Linking only; the handler's Build, which would wrap functions with notices logged to stdout, never runs.
		if err := r.LinkHandler(h); err != nil {
			panic(fmt.Sprintf("templates: link the sprout registry %s: %v", r.UID(), err))
		}
		if err := r.RegisterFunctions(fm); err != nil {
			panic(fmt.Sprintf("templates: register the sprout registry %s: %v", r.UID(), err))
		}
	}
	// Nondeterministic, and of no use in a message.
	delete(fm, "shuffle")
	// They change a dict in place, which can make it contain itself.
	for _, name := range []string{"set", "unset", "merge", "mergeOverwrite"} {
		delete(fm, name)
	}
	for name, f := range alertmanagerFuncs() {
		fm[name] = f
	}
	for name, f := range boundedFuncs(fm) {
		fm[name] = f
	}
	// lookup stands for the one of each execution, which reads through its Env.
	fm["lookup"] = func(string, string, string) (string, error) { return "", nil }
	fm["mention"] = mention
	for name, f := range fm {
		fm[name] = bounded(name, f)
	}
	return fm
}

// Names are the names of the functions a template may call besides those of text/template, sorted.
func (s *Sandbox) Names() []string {
	out := make([]string, 0, len(s.funcs)+2)
	for name := range s.funcs {
		out = append(out, name)
	}
	out = append(out, "now", "since")
	slices.Sort(out)
	return out
}

// errTooLarge is a function result above the limits.
var errTooLarge = errors.New("the result is too large")

// bounded wraps f so that a string result longer than OutputCap characters, or a list or map result with more than
// ResultItems items, is an error: a template cannot build an unbounded value that it never writes.
func bounded(name string, f any) any {
	v := reflect.ValueOf(f)
	t := v.Type()
	if t.Kind() != reflect.Func || t.NumOut() == 0 {
		return f
	}
	hasErr := t.NumOut() == 2 && t.Out(1) == reflect.TypeFor[error]()
	return reflect.MakeFunc(t, func(args []reflect.Value) []reflect.Value {
		var out []reflect.Value
		if t.IsVariadic() {
			out = v.CallSlice(args)
		} else {
			out = v.Call(args)
		}
		if err := checkSize(out[0]); err != nil {
			if !hasErr {
				panic(fmt.Errorf("%s: %w", name, err)) // text/template turns a panic of a function into its error
			}
			out[1] = reflect.ValueOf(fmt.Errorf("%s: %w", name, err)).Convert(t.Out(1))
		}
		return out
	}).Interface()
}

// checkSize refuses a value above the limits of a function result: a string longer than OutputCap characters, a
// list or map with more than ResultItems items, a value whose strings and items add up to more than maxBytes counted
// at every place they appear, and a value that contains itself, which fmt would print forever.
func checkSize(v reflect.Value) error {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		if v.Len() > OutputCap && utf8.RuneCountInString(v.String()) > OutputCap {
			return fmt.Errorf("%w: more than %d characters", errTooLarge, OutputCap)
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		if v.Len() > ResultItems {
			return fmt.Errorf("%w: more than %d items", errTooLarge, ResultItems)
		}
	default:
	}
	_, err := deepSize(v, map[uintptr]bool{}, 0)
	return err
}

// deepSize adds to total the bytes of the strings in v and one per item, wherever they appear, until it passes
// maxBytes; a map or slice found inside itself is an error. on holds the containers being walked.
func deepSize(v reflect.Value, on map[uintptr]bool, total int) (int, error) {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return total, nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return total, nil
	}
	total++
	if total > maxBytes {
		return total, errGrowth("the result")
	}
	switch v.Kind() {
	case reflect.String:
		total += v.Len()
	case reflect.Slice, reflect.Map:
		if v.Len() == 0 {
			break
		}
		p := v.Pointer()
		if on[p] {
			return total, fmt.Errorf("%w: it contains itself", errTooLarge)
		}
		on[p] = true
		defer delete(on, p)
		var err error
		if v.Kind() == reflect.Map {
			for it := v.MapRange(); it.Next() && err == nil; {
				if total, err = deepSize(it.Key(), on, total); err == nil {
					total, err = deepSize(it.Value(), on, total)
				}
			}
		} else {
			for i := 0; i < v.Len() && err == nil; i++ {
				total, err = deepSize(v.Index(i), on, total)
			}
		}
		if err != nil {
			return total, err
		}
	case reflect.Array:
		for i := range v.Len() {
			var err error
			if total, err = deepSize(v.Index(i), on, total); err != nil {
				return total, err
			}
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			var err error
			if total, err = deepSize(v.Field(i), on, total); err != nil {
				return total, err
			}
		}
	default:
	}
	if total > maxBytes {
		return total, errGrowth("the result")
	}
	return total, nil
}

// checkArgs refuses arguments that would print to more than maxBytes, or forever.
func checkArgs(name string, args []any) error {
	total := 0
	for _, a := range args {
		var err error
		if total, err = deepSize(reflect.ValueOf(a), map[uintptr]bool{}, total); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// maxBytes bounds what one function call may build before its result is checked: four bytes for each character of
// template.output_cap.
const maxBytes = 4 * OutputCap

// errGrowth is a call that would build a value larger than maxBytes.
func errGrowth(name string) error {
	return fmt.Errorf("%s: %w: more than %d bytes", name, errTooLarge, maxBytes)
}

// boundedFuncs replace the functions that would loop or allocate without a bound, so that no single call builds more
// than maxBytes before its result is checked: until, untilStep, seq and repeat produce at most LoopCap items, indent
// and nindent pad with at most LoopCap spaces, print, printf and println refuse widths and precisions above
// OutputCap, and repeat, indent, replace, join, wrap, wrapWith and the regular expression replacements refuse a
// result that would grow past maxBytes. base holds the registered functions they guard.
func boundedFuncs(base template.FuncMap) template.FuncMap {
	sproutJoin := base["join"].(func(string, any) string)
	sproutWrap := base["wrap"].(func(int, string) string)
	sproutWrapWith := base["wrapWith"].(func(int, string, string) string)
	return template.FuncMap{
		"replace": func(old, repl, s string) (string, error) {
			n := strings.Count(s, old)
			if len(s)+n*len(repl) > maxBytes {
				return "", errGrowth("replace")
			}
			return strings.ReplaceAll(s, old, repl), nil
		},
		"join": func(sep string, v any) (string, error) {
			if rv := reflect.ValueOf(v); (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) &&
				rv.Len()*len(sep) > maxBytes {
				return "", errGrowth("join")
			}
			if err := checkArgs("join", []any{v}); err != nil {
				return "", err
			}
			return sproutJoin(sep, v), nil
		},
		"wrap": func(length int, s string) (string, error) {
			return wrapWith(func(n int, _, s string) string { return sproutWrap(n, s) }, length, "\n", s)
		},
		"wrapWith": func(length int, sep, s string) (string, error) {
			return wrapWith(sproutWrapWith, length, sep, s)
		},
		"regexReplaceAll": func(pattern, repl, s string) (string, error) {
			return replaceAll("regexReplaceAll", pattern, repl, s, false)
		},
		"regexReplaceAllLiteral": func(pattern, repl, s string) (string, error) {
			return replaceAll("regexReplaceAllLiteral", pattern, repl, s, true)
		},
		"reReplaceAll": func(pattern, repl, s string) (string, error) {
			return replaceAll("reReplaceAll", pattern, repl, s, false)
		},
		"until": func(count int) ([]int, error) {
			step := 1
			if count < 0 {
				step = -1
			}
			return untilStep(0, count, step)
		},
		"untilStep": untilStep,
		"seq":       seq,
		"repeat": func(count int, s string) (string, error) {
			if count > LoopCap {
				return "", loopError("repeat")
			}
			if count*len(s) > maxBytes {
				return "", errGrowth("repeat")
			}
			return strings.Repeat(s, max(count, 0)), nil
		},
		"indent":  func(spaces int, s string) (string, error) { return indent(spaces, s, "") },
		"nindent": func(spaces int, s string) (string, error) { return indent(spaces, s, "\n") },
		"print": func(args ...any) (string, error) {
			if err := checkArgs("print", args); err != nil {
				return "", err
			}
			return fmt.Sprint(args...), nil
		},
		"println": func(args ...any) (string, error) {
			if err := checkArgs("println", args); err != nil {
				return "", err
			}
			return fmt.Sprintln(args...), nil
		},
		"printf": func(format string, args ...any) (string, error) {
			if err := checkFormat(format); err != nil {
				return "", err
			}
			if err := checkArgs("printf", args); err != nil {
				return "", err
			}
			return fmt.Sprintf(format, args...), nil
		},
	}
}

func loopError(name string) error {
	return fmt.Errorf("%s produces more than %d items", name, LoopCap)
}

// untilStep counts from start towards stop, exclusive, by step: at most LoopCap numbers.
func untilStep(start, stop, step int) ([]int, error) {
	if step == 0 || (step > 0 && start >= stop) || (step < 0 && start <= stop) {
		return []int{}, nil
	}
	n := (stop - start + step - sign(step)) / step
	if n > LoopCap {
		return nil, loopError("until")
	}
	out := make([]int, 0, n)
	for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
		out = append(out, i)
	}
	return out, nil
}

func sign(n int) int {
	if n < 0 {
		return -1
	}
	return 1
}

// seq is the seq of the GNU tool: `seq last`, `seq first last` or `seq first step last`, the numbers joined by
// spaces; at most LoopCap numbers.
func seq(params ...int) (string, error) {
	start, step, end := 1, 1, 0 //nolint:wastedassign // the zero end of a seq without parameters is never used
	switch len(params) {
	case 1:
		end = params[0]
	case 2:
		start, end = params[0], params[1]
	case 3:
		start, step, end = params[0], params[1], params[2]
	default:
		return "", nil
	}
	if len(params) < 3 && end < start {
		step = -1
	}
	if step == 0 {
		return "", nil
	}
	nums, err := untilStep(start, end+sign(step), step)
	if err != nil {
		return "", loopError("seq")
	}
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, " "), nil
}

// wrapWith is sprout's wrapWith with a line length of at least 1 and a result of at most maxBytes: each line break
// adds sep.
func wrapWith(f func(int, string, string) string, length int, sep, s string) (string, error) {
	if length < 1 {
		return "", errors.New("wrap: the line length is at least 1")
	}
	if len(s)+(len(s)/length+1)*len(sep) > maxBytes {
		return "", errGrowth("wrapWith")
	}
	return f(length, sep, s), nil
}

// replaceAll replaces the matches of pattern in s with repl, expanding $ references unless literal; a result that
// could grow past maxBytes — each match adds repl, and each $ reference at most s — is refused.
func replaceAll(name, pattern, repl, s string, literal bool) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	n := len(re.FindAllStringIndex(s, -1))
	refs := 0
	if !literal {
		refs = strings.Count(repl, "$")
	}
	if len(s)+n*len(repl)+refs*len(s) > maxBytes {
		return "", errGrowth(name)
	}
	if literal {
		return re.ReplaceAllLiteralString(s, repl), nil
	}
	return re.ReplaceAllString(s, repl), nil
}

// indent prefixes every line of s with spaces, after first.
func indent(spaces int, s, first string) (string, error) {
	if spaces > LoopCap {
		return "", loopError("indent")
	}
	if len(s)+(strings.Count(s, "\n")+1)*max(spaces, 0) > maxBytes {
		return "", errGrowth("indent")
	}
	pad := strings.Repeat(" ", max(spaces, 0))
	return first + pad + strings.ReplaceAll(s, "\n", "\n"+pad), nil
}

// formatWidth finds the widths and precisions of a format.
var formatWidth = regexp.MustCompile(`%[-+# 0]*(\*|\d+)?(?:\.(\*|\d+))?`)

// checkFormat refuses a format with a width or a precision above OutputCap, or taken from an argument.
func checkFormat(format string) error {
	for _, m := range formatWidth.FindAllStringSubmatch(format, -1) {
		for _, w := range m[1:] {
			if w == "*" {
				return errors.New("printf: a width or precision from an argument is not allowed")
			}
			if n, err := strconv.Atoi(w); err == nil && n > OutputCap {
				return fmt.Errorf("printf: a width or precision above %d", OutputCap)
			}
		}
	}
	return nil
}

// Template is a parsed template.
type Template struct {
	sandbox *Sandbox
	tmpl    *template.Template
	source  string
}

// Source is the template's text.
func (t *Template) Source() string { return t.source }

// Parse parses src as the template name. A syntax error is CodeSyntax with its line, a call of a function the sandbox
// does not have is CodeUnknownFunction with its line and column.
func (s *Sandbox) Parse(name, src string) (*Template, error) {
	trees := map[string]*parse.Tree{}
	pt := parse.New(name)
	pt.Mode = parse.SkipFuncCheck
	if _, err := pt.Parse(src, "", "", trees); err != nil {
		return nil, syntaxError(err)
	}
	// The trees parse without checking function names, so that an unknown one is reported with its column.
	for _, tree := range sortedTrees(trees) {
		if id := s.unknown(tree.Root); id != nil {
			line, column := position(src, int(id.Pos))
			return nil, &Error{Code: CodeUnknownFunction, Line: line, Column: column,
				Detail: fmt.Sprintf("function %q is not defined", id.Ident)}
		}
	}
	root := template.New(name).Funcs(s.funcs).Funcs(execFuncs(nil))
	for _, tree := range sortedTrees(trees) {
		guard(tree, tree.Root)
		if _, err := root.AddParseTree(tree.Name, tree); err != nil {
			return nil, syntaxError(err)
		}
	}
	return &Template{sandbox: s, tmpl: root, source: src}, nil
}

// sortedTrees are the trees of a parse, the main template first and then by name, so that the first error reported
// does not depend on map order.
func sortedTrees(trees map[string]*parse.Tree) []*parse.Tree {
	out := make([]*parse.Tree, 0, len(trees))
	for _, t := range trees {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b *parse.Tree) int {
		if a.Root == nil || b.Root == nil {
			return strings.Compare(a.Name, b.Name)
		}
		return int(a.Root.Pos) - int(b.Root.Pos)
	})
	return out
}

// unknown is the first identifier under n that names no function of the sandbox or of text/template.
func (s *Sandbox) unknown(n parse.Node) *parse.IdentifierNode {
	var found *parse.IdentifierNode
	walk(n, func(n parse.Node) {
		id, ok := n.(*parse.IdentifierNode)
		if !ok || (found != nil && found.Pos <= id.Pos) {
			return
		}
		if _, ok := s.funcs[id.Ident]; ok || slices.Contains(builtins, id.Ident) || id.Ident == "now" ||
			id.Ident == "since" {
			return
		}
		found = id
	})
	return found
}

// walk visits n and every node under it.
func walk(n parse.Node, visit func(parse.Node)) {
	if n == nil || reflect.ValueOf(n).IsNil() {
		return
	}
	visit(n)
	switch n := n.(type) {
	case *parse.ListNode:
		for _, c := range n.Nodes {
			walk(c, visit)
		}
	case *parse.ActionNode:
		walk(n.Pipe, visit)
	case *parse.PipeNode:
		for _, c := range n.Cmds {
			walk(c, visit)
		}
	case *parse.CommandNode:
		for _, a := range n.Args {
			walk(a, visit)
		}
	case *parse.ChainNode:
		walk(n.Node, visit)
	case *parse.IfNode:
		walkBranch(&n.BranchNode, visit)
	case *parse.RangeNode:
		walkBranch(&n.BranchNode, visit)
	case *parse.WithNode:
		walkBranch(&n.BranchNode, visit)
	case *parse.TemplateNode:
		walk(n.Pipe, visit)
	}
}

func walkBranch(b *parse.BranchNode, visit func(parse.Node)) {
	walk(b.Pipe, visit)
	walk(b.List, visit)
	walk(b.ElseList, visit)
}

// guard rewrites the tree so that the collection of every range passes through rangeGuard and the data of every
// template call through callGuard, which count them against the budget of the execution.
func guard(tree *parse.Tree, n parse.Node) {
	walk(n, func(n parse.Node) {
		switch n := n.(type) {
		case *parse.RangeNode:
			n.Pipe.Cmds = append(n.Pipe.Cmds, guardCommand(tree, n.Pipe.Pos, rangeGuard))
		case *parse.TemplateNode:
			if n.Pipe == nil {
				n.Pipe = &parse.PipeNode{NodeType: parse.NodePipe, Pos: n.Pos, Line: n.Line}
			}
			n.Pipe.Cmds = append(n.Pipe.Cmds, guardCommand(tree, n.Pos, callGuard))
		}
	})
}

func guardCommand(tree *parse.Tree, pos parse.Pos, name string) *parse.CommandNode {
	id := parse.NewIdentifier(name).SetTree(tree).SetPos(pos)
	return &parse.CommandNode{NodeType: parse.NodeCommand, Pos: pos, Args: []parse.Node{id}}
}

// position is the 1-based line and column, in characters, of the byte offset pos of src.
func position(src string, pos int) (int, int) {
	pos = min(max(pos, 0), len(src))
	before := src[:pos]
	line := 1 + strings.Count(before, "\n")
	column := 1 + utf8.RuneCountInString(before[strings.LastIndex(before, "\n")+1:])
	return line, column
}

// errorPosition reads the position text/template puts in its errors: "template: NAME:LINE[:COLUMN]: DETAIL".
var errorPosition = regexp.MustCompile(`^template: (?:.*?):(\d+)(?::(\d+))?: (?s)(.*)$`)

func syntaxError(err error) *Error {
	return positioned(CodeSyntax, err)
}

// positioned is err as an Error with the code and the position text/template wrote into it.
func positioned(code string, err error) *Error {
	e := &Error{Code: code, Detail: err.Error()}
	if m := errorPosition.FindStringSubmatch(err.Error()); m != nil {
		e.Line, _ = strconv.Atoi(m[1])
		e.Column, _ = strconv.Atoi(m[2])
		e.Detail = m[3]
	}
	return e
}

// budget is what one execution may still spend.
type budget struct {
	ranges   int
	calls    int
	deadline time.Time
	real     clock.Clock
}

// execFuncs are the functions bound to one execution: its guards, and `now` and `since` on the business clock. With a
// nil budget they only stand in for the names at parse time.
func execFuncs(b *budget) template.FuncMap {
	return template.FuncMap{
		rangeGuard: func(v any) (any, error) { return b.rangeOver(v) },
		callGuard: func(v ...any) (any, error) {
			if err := b.call(); err != nil {
				return nil, err
			}
			if len(v) == 0 {
				return nil, nil
			}
			return v[len(v)-1], nil
		},
	}
}

// errBudget is an execution that used up its budget.
var errBudget = errors.New("the template does too much work")

func (b *budget) expired() error {
	if b.real.Now().After(b.deadline) {
		return fmt.Errorf("%w: it ran for longer than %s", errBudget, ExecutionLimit)
	}
	return nil
}

// rangeOver counts the items of a range's collection; a range over an integer is capped at LoopCap.
func (b *budget) rangeOver(v any) (any, error) {
	if err := b.expired(); err != nil {
		return nil, err
	}
	n := 0
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
		n = rv.Len()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if rv.Int() > LoopCap {
			return nil, loopError("range")
		}
		n = int(max(rv.Int(), 0))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if u > LoopCap {
			return nil, loopError("range")
		}
		n = int(u)
	case reflect.Invalid, reflect.Pointer, reflect.Interface:
	default:
		return nil, fmt.Errorf("cannot range over %s", rv.Kind())
	}
	b.ranges -= n
	if b.ranges < 0 {
		return nil, fmt.Errorf("%w: the ranges visit more than %d items", errBudget, RangeBudget)
	}
	return v, nil
}

func (b *budget) call() error {
	if err := b.expired(); err != nil {
		return err
	}
	b.calls--
	if b.calls < 0 {
		return fmt.Errorf("%w: more than %d template calls", errBudget, CallBudget)
	}
	return nil
}

// errOutputCap is output that reached OutputCap.
var errOutputCap = fmt.Errorf("the output is longer than %d characters", OutputCap)

// capWriter keeps at most OutputCap characters and fails the write that goes past them.
type capWriter struct {
	b     strings.Builder
	chars int
}

func (w *capWriter) Write(p []byte) (int, error) {
	n := utf8.RuneCount(p)
	if w.chars+n > OutputCap {
		return 0, errOutputCap
	}
	w.chars += n
	return w.b.Write(p)
}

// Env is what one execution reads besides its data: Lookup reads one cell of a Lookup table, the empty string for a
// missing table, key or column (C-12.FR-9); nil reads nothing. Mention, when set, stands for `mention` in the
// execution: an outgoing webhook renders a Mention target of its data as plain text instead of a trusted token
// (C-15.FR-11).
type Env struct {
	Lookup  func(table, key, column string) (string, error)
	Mention func(target any) (string, error)
}

// Execute runs the template on data with a fresh budget and returns its output. Any failure is an Error with the
// code CodeSyntax and the position text/template reports.
func (t *Template) Execute(data any) (string, error) {
	return t.ExecuteIn(Env{}, data)
}

// ExecuteIn runs the template on data with a fresh budget and the Env env, as Execute does.
func (t *Template) ExecuteIn(env Env, data any) (out string, err error) {
	s := t.sandbox
	b := &budget{ranges: RangeBudget, calls: CallBudget, deadline: s.real.Now().Add(ExecutionLimit), real: s.real}
	tmpl, err := t.tmpl.Clone()
	if err != nil {
		return "", &Error{Code: CodeSyntax, Detail: err.Error()}
	}
	now := s.business.Now()
	fm := template.FuncMap{
		"now":   func() time.Time { return now },
		"since": now.Sub,
	}
	if env.Lookup != nil {
		fm["lookup"] = bounded("lookup", env.Lookup)
	}
	if env.Mention != nil {
		fm["mention"] = bounded("mention", env.Mention)
	}
	tmpl.Funcs(execFuncs(b)).Funcs(fm)
	var w capWriter
	defer func() {
		if r := recover(); r != nil {
			out, err = "", &Error{Code: CodeSyntax, Detail: fmt.Sprintf("the template failed: %v", r)}
		}
	}()
	if err := tmpl.Execute(&w, data); err != nil {
		if errors.Is(err, errOutputCap) {
			return "", &Error{Code: CodeSyntax, Detail: errOutputCap.Error()}
		}
		return "", positioned(CodeSyntax, err)
	}
	return w.b.String(), nil
}

// LookupTables are the Lookup tables the template reads by a literal name, `lookup "grafana" …`, sorted and without
// duplicates.
func (t *Template) LookupTables() []string {
	var out []string
	for _, tmpl := range t.tmpl.Templates() {
		if tmpl.Tree == nil {
			continue
		}
		walk(tmpl.Root, func(n parse.Node) {
			c, ok := n.(*parse.CommandNode)
			if !ok || len(c.Args) < 2 {
				return
			}
			if id, ok := c.Args[0].(*parse.IdentifierNode); !ok || id.Ident != "lookup" {
				return
			}
			if name, ok := c.Args[1].(*parse.StringNode); ok {
				out = append(out, name.Text)
			}
		})
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// The trusted Mention tokens of `mention` (C-12.FR-8, ADR-0012): a name, and the group of a group Mention, between
// characters of the Unicode private use area. Messages strip these characters from alert data, so a token in a
// message always comes from a template; it survives the escaping of every markup, and an adapter replaces it with
// its messenger's syntax, while every other `@` of a template's output is neutralized like alert data.
const (
	tokenStart = '\uE000'
	tokenSep   = '\uE001'
	tokenEnd   = '\uE002'
)

// The Mentions a template may write with `mention`: everyone in the chat as all, channel or here, the Owner, or a
// messenger group by its name.
const (
	MentionAll     = "all"
	MentionChannel = "channel"
	MentionHere    = "here"
	MentionOwner   = "owner"
	MentionGroup   = "group"
)

// groupName is the form of a messenger group name that `mention "group"` takes.
var groupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// mention is `{{ mention "all" }}`, `{{ mention "owner" }}` or `{{ mention "group" "oncall" }}`: the trusted token of
// the Mention.
func mention(name string, args ...string) (string, error) {
	switch name {
	case MentionAll, MentionChannel, MentionHere, MentionOwner:
		if len(args) > 0 {
			return "", fmt.Errorf("mention %q takes no group", name)
		}
		return MentionToken(Token{Name: name}), nil
	case MentionGroup:
		if len(args) != 1 || !groupName.MatchString(args[0]) {
			return "", errors.New(`mention "group" takes one group name of letters, digits, ".", "_" and "-"`)
		}
		return MentionToken(Token{Name: name, Group: args[0]}), nil
	}
	return "", fmt.Errorf("%q is not one of all, channel, here, owner and group", name)
}

// Token is a trusted Mention in a text: its name and, for a group, the group's name.
type Token struct {
	Name  string
	Group string
}

// MentionToken is the token of tk.
func MentionToken(tk Token) string {
	if tk.Group != "" {
		return string(tokenStart) + tk.Name + string(tokenSep) + tk.Group + string(tokenEnd)
	}
	return string(tokenStart) + tk.Name + string(tokenEnd)
}

// ReplaceTokens replaces every Mention token of s with what f returns for it; the token characters that form no
// token are dropped.
func ReplaceTokens(s string, f func(Token) string) string {
	if !strings.ContainsRune(s, tokenStart) {
		return StripTokens(s)
	}
	var b strings.Builder
	for {
		i := strings.IndexRune(s, tokenStart)
		if i < 0 {
			break
		}
		b.WriteString(StripTokens(s[:i]))
		rest := s[i+utf8.RuneLen(tokenStart):]
		j := strings.IndexRune(rest, tokenEnd)
		if j < 0 {
			s = rest
			continue
		}
		body := rest[:j]
		s = rest[j+utf8.RuneLen(tokenEnd):]
		name, group, _ := strings.Cut(body, string(tokenSep))
		if strings.ContainsRune(name, tokenStart) {
			b.WriteString(StripTokens(body))
			continue
		}
		b.WriteString(f(Token{Name: name, Group: group}))
	}
	b.WriteString(StripTokens(s))
	return b.String()
}

// Tokens are the Mention tokens of s in order.
func Tokens(s string) []Token {
	var out []Token
	ReplaceTokens(s, func(tk Token) string {
		out = append(out, tk)
		return ""
	})
	return out
}

// StripTokens removes the characters of Mention tokens from s: alert data never carry a token.
func StripTokens(s string) string {
	if !strings.ContainsFunc(s, isTokenRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isTokenRune(r) {
			return -1
		}
		return r
	}, s)
}

func isTokenRune(r rune) bool {
	return r == tokenStart || r == tokenSep || r == tokenEnd
}

// SafeURL is u, trimmed, when it is an absolute http or https link without user information, spaces, control or
// format characters or the characters of Mention tokens, and empty otherwise (C-12.FR-7): links are http(s) only.
func SafeURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" || strings.ContainsFunc(u, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || isTokenRune(r)
	}) {
		return ""
	}
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || p.Opaque != "" || p.User != nil {
		return ""
	}
	if s := strings.ToLower(p.Scheme); s != "http" && s != "https" {
		return ""
	}
	return u
}
