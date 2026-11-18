// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/logging"
)

// workClauseRe finds the "(work is a, b or c)" clauses of an event description; the description may break a clause
// over the concatenated lines of its declaration, so any whitespace separates the words.
var workClauseRe = regexp.MustCompile(`\(work\s+is\s+([^)]*)\)`)

// TestDeliveryWorkFailedWorkValuesAreRegistered checks that every work value the package logs with
// delivery_work_failed is named in the event's registered description, and that the description names no other.
func TestDeliveryWorkFailedWorkValuesAreRegistered(t *testing.T) {
	logged := loggedWorkValues(t)
	if len(logged) == 0 {
		t.Fatal("found no delivery_work_failed call with a work field")
	}
	registered := registeredWorkValues(t)
	for _, v := range logged {
		if !slices.Contains(registered, v) {
			t.Errorf("work %q is logged with delivery_work_failed but its description names only %v", v, registered)
		}
	}
	for _, v := range registered {
		if !slices.Contains(logged, v) {
			t.Errorf("the description of delivery_work_failed names work %q, which nothing logs", v)
		}
	}
}

func registeredWorkValues(t *testing.T) []string {
	t.Helper()
	events, err := logging.Events()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(events, func(e logging.EventInfo) bool { return e.Name == logging.DeliveryWorkFailed.Name() })
	if i < 0 {
		t.Fatal("delivery_work_failed is not registered")
	}
	var values []string
	for _, m := range workClauseRe.FindAllStringSubmatch(events[i].Description, -1) {
		list := strings.Join(strings.Fields(m[1]), " ")
		for _, part := range strings.Split(strings.ReplaceAll(list, " or ", ", "), ",") {
			if v := strings.TrimSpace(part); v != "" {
				values = append(values, v)
			}
		}
	}
	slices.Sort(values)
	return slices.Compact(values)
}

// loggedWorkValues parses the package's non-test sources and returns the work values of every delivery_work_failed
// call, each a string literal or a package-level string constant. It scans only internal/delivery, where every
// delivery_work_failed call is, and fails when the event is used other than as an argument of a call, such as through
// a variable, which the scan could not follow.
func loggedWorkValues(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	consts := map[string]string{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for j, name := range vs.Names {
					if j < len(vs.Values) {
						if s, ok := stringLit(vs.Values[j]); ok {
							consts[name.Name] = s
						}
					}
				}
			}
		}
	}
	var values []string
	uses, calls := 0, 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok && isDeliveryWorkFailed(e) {
				uses++
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || !slices.ContainsFunc(call.Args, isDeliveryWorkFailed) {
				return true
			}
			calls++
			found := false
			for _, arg := range call.Args {
				work, ok := workArg(arg)
				if !ok {
					continue
				}
				found = true
				v, ok := stringLit(work)
				if id, isIdent := work.(*ast.Ident); !ok && isIdent {
					v, ok = consts[id.Name]
				}
				if !ok {
					t.Errorf("%s: the work of delivery_work_failed is neither a string literal nor a string constant",
						fset.Position(work.Pos()))
					continue
				}
				values = append(values, v)
			}
			if !found {
				t.Errorf("%s: delivery_work_failed is logged without a work field", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if uses != calls {
		t.Errorf("logging.DeliveryWorkFailed appears %d times but only %d are arguments of a call the scan examines; "+
			"pass it directly to the logger", uses, calls)
	}
	slices.Sort(values)
	return slices.Compact(values)
}

func isDeliveryWorkFailed(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "DeliveryWorkFailed" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "logging"
}

// workArg returns X of an argument logging.F("work", X).
func workArg(e ast.Expr) (ast.Expr, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "F" {
		return nil, false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "logging" {
		return nil, false
	}
	if key, ok := stringLit(call.Args[0]); !ok || key != "work" {
		return nil, false
	}
	return call.Args[1], true
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}
