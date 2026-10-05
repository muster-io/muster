// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build lint

package archlint

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

const (
	loadMode = packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo

	httpPkg    = "net/http"
	httpSuffix = "; HTTP clients and transports come only from internal/outbound"
)

var (
	httpDefaults = []string{"DefaultClient", "DefaultTransport"}
	httpFuncs    = []string{"Get", "Post", "Head", "PostForm"}
)

// checkGo runs the Go part of rule 2 (imports of the generated groups queries), rule 3 (messenger sends and edits)
// and rule 4 (HTTP clients and transports) over the non-test files of the module, the code behind the lint build tag
// included.
func checkGo(root string, cfg Config) ([]Diagnostic, error) {
	pkgs, err := packages.Load(&packages.Config{
		Mode:       loadMode,
		Dir:        root,
		Env:        append(os.Environ(), "GOWORK=off"),
		BuildFlags: []string{"-tags=lint"},
	}, "./...")
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	var errs []error
	for _, p := range pkgs {
		for _, e := range p.Errors {
			errs = append(errs, e)
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("load packages: %w", errors.Join(errs...))
	}
	adapter, err := findAdapter(pkgs, cfg)
	if err != nil {
		return nil, err
	}
	var diags []Diagnostic
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			rel, ok := relPath(root, p.Fset.File(f.FileStart).Name())
			if !ok || strings.HasSuffix(rel, "_test.go") {
				continue
			}
			c := &goFile{pkg: p, rel: rel}
			if !inDir(rel, cfg.GroupsDir) {
				c.checkGroupImports(f, cfg)
			}
			if adapter != nil && !slices.Contains(cfg.Messenger.AllowedFiles, rel) {
				c.adapter = adapter
			}
			c.checkHTTP = !slices.ContainsFunc(cfg.HTTPExempt, func(dir string) bool { return inDir(rel, dir) })
			c.inspect(f)
			diags = append(diags, c.diags...)
		}
	}
	return diags, nil
}

func relPath(root, name string) (string, bool) {
	rel, err := filepath.Rel(root, name)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

type adapter struct {
	// sigs maps the name of each send and edit method to its signature.
	sigs    map[string]string
	allowed []string
}

// findAdapter returns the messenger adapter interface of rule 3, or nil while its package does not exist yet. A
// package without the configured interface is an error, so a rename cannot switch the rule off silently.
func findAdapter(pkgs []*packages.Package, cfg Config) (*adapter, error) {
	pkgPath := cfg.Module + "/" + cfg.Messenger.Package
	i := slices.IndexFunc(pkgs, func(p *packages.Package) bool { return p.PkgPath == pkgPath })
	if i < 0 {
		return nil, nil
	}
	obj, ok := pkgs[i].Types.Scope().Lookup(cfg.Messenger.Interface).(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("rule 3: %s has no type %s; update the messenger adapter in the lint configuration",
			cfg.Messenger.Package, cfg.Messenger.Interface)
	}
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		return nil, fmt.Errorf("rule 3: %s.%s is not an interface", cfg.Messenger.Package, cfg.Messenger.Interface)
	}
	sigs := map[string]string{}
	for m := range iface.Methods() {
		sigs[m.Name()] = signatureKey(m.Signature())
	}
	a := &adapter{sigs: map[string]string{}, allowed: cfg.Messenger.AllowedFiles}
	for _, name := range cfg.Messenger.Methods {
		sig, ok := sigs[name]
		if !ok {
			return nil, fmt.Errorf("rule 3: %s.%s has no method %s; update the messenger adapter in the lint configuration",
				cfg.Messenger.Package, cfg.Messenger.Interface, name)
		}
		a.sigs[name] = sig
	}
	return a, nil
}

// matches reports whether fn is a messenger send or edit: a method with the name and the signature of one of the
// adapter's, whatever type it is selected on, so that an interface the caller declares cannot hide the call. The
// signatures are compared as strings, because packages are type-checked one at a time and the same named type may be
// two distinct objects.
func (a *adapter) matches(fn *types.Func) bool {
	sig, ok := a.sigs[fn.Name()]
	return ok && signatureKey(fn.Signature()) == sig
}

func signatureKey(sig *types.Signature) string {
	var b strings.Builder
	for v := range sig.Params().Variables() {
		b.WriteString(types.TypeString(v.Type(), (*types.Package).Path) + ",")
	}
	if sig.Variadic() {
		b.WriteString("...")
	}
	b.WriteString("->")
	for v := range sig.Results().Variables() {
		b.WriteString(types.TypeString(v.Type(), (*types.Package).Path) + ",")
	}
	return b.String()
}

type goFile struct {
	pkg       *packages.Package
	rel       string
	adapter   *adapter
	checkHTTP bool
	diags     []Diagnostic
}

func (c *goFile) report(rule int, pos token.Pos, format string, args ...any) {
	p := c.pkg.Fset.Position(pos)
	c.diags = append(c.diags, Diagnostic{Rule: rule, Path: c.rel, Line: p.Line, Col: p.Column, Message: fmt.Sprintf(format, args...)})
}

// checkGroupImports reports imports of the generated groups queries, or of a package below them, so that the queries
// that write Alert Group tables are called only from the groups package.
func (c *goFile) checkGroupImports(f *ast.File, cfg Config) {
	queries := cfg.Module + "/" + cfg.GroupQueries
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err == nil && (path == queries || strings.HasPrefix(path, queries+"/")) {
			c.report(2, imp.Path.Pos(), "imports %s outside %s; Alert Group tables change only through the groups dispatcher",
				strings.TrimPrefix(path, cfg.Module+"/"), cfg.GroupsDir)
		}
	}
}

func (c *goFile) inspect(f *ast.File) {
	if c.adapter == nil && !c.checkHTTP {
		return
	}
	info := c.pkg.TypesInfo
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			c.selector(sel, info.Selections[sel])
		}
		if c.checkHTTP {
			c.httpNode(n, info)
		}
		return true
	})
}

// httpNode reports rule 4 constructs other than method selections: references to the package-level defaults and
// helpers, and every way to make an http.Client or http.Transport value.
func (c *goFile) httpNode(n ast.Node, info *types.Info) {
	switch n := n.(type) {
	case *ast.Ident:
		c.httpObject(n, info.Uses[n])
	case *ast.CompositeLit:
		if name := httpType(info.TypeOf(n)); name != "" {
			c.report(4, n.Pos(), "http.%s literal%s", name, httpSuffix)
		}
	case *ast.CallExpr:
		id, ok := ast.Unparen(n.Fun).(*ast.Ident)
		if !ok || len(n.Args) != 1 {
			return
		}
		if b, ok := info.Uses[id].(*types.Builtin); ok && b.Name() == "new" {
			if name := httpType(info.TypeOf(n.Args[0])); name != "" {
				c.report(4, n.Pos(), "new(http.%s)%s", name, httpSuffix)
			}
		}
	case *ast.ValueSpec:
		if n.Type == nil {
			return
		}
		if name := httpType(info.TypeOf(n.Type)); name != "" {
			c.report(4, n.Pos(), "variable of type http.%s%s", name, httpSuffix)
		}
	case *ast.StructType:
		for _, field := range n.Fields.List {
			if name := httpType(info.TypeOf(field.Type)); name != "" {
				c.report(4, field.Pos(), "field of type http.%s%s", name, httpSuffix)
			}
		}
	}
}

func (c *goFile) selector(n *ast.SelectorExpr, sel *types.Selection) {
	if sel == nil || sel.Kind() == types.FieldVal {
		return
	}
	name := sel.Obj().Name()
	if fn, ok := sel.Obj().(*types.Func); ok && c.adapter != nil && c.adapter.matches(fn) {
		recv := types.TypeString(sel.Recv(), (*types.Package).Name)
		if strings.HasPrefix(recv, "*") {
			recv = "(" + recv + ")"
		}
		c.report(3, n.Sel.Pos(), "%s.%s sends or edits a messenger message outside the delivery worker and the interactive path (%s)",
			recv, name, strings.Join(c.adapter.allowed, ", "))
	}
	if c.checkHTTP && name == "Clone" && httpType(deref(sel.Recv())) == "Transport" {
		c.report(4, n.Sel.Pos(), "(*http.Transport).Clone%s", httpSuffix)
	}
}

// httpObject reports references to the package-level default client and transport and to the helper functions that
// use them.
func (c *goFile) httpObject(id *ast.Ident, obj types.Object) {
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != httpPkg || obj.Pkg().Scope().Lookup(obj.Name()) != obj {
		return
	}
	switch obj.(type) {
	case *types.Var:
		if slices.Contains(httpDefaults, obj.Name()) {
			c.report(4, id.Pos(), "http.%s%s", obj.Name(), httpSuffix)
		}
	case *types.Func:
		if slices.Contains(httpFuncs, obj.Name()) {
			c.report(4, id.Pos(), "http.%s uses http.DefaultClient%s", obj.Name(), httpSuffix)
		}
	}
}

// httpType returns "Client" or "Transport" when t is that net/http type, through aliases, and "" otherwise.
func httpType(t types.Type) string {
	if t == nil {
		return ""
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != httpPkg {
		return ""
	}
	if name := named.Obj().Name(); name == "Client" || name == "Transport" {
		return name
	}
	return ""
}

func deref(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}
