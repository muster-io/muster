// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build lint

// Package archlint holds the architecture lints of ADR-0016 that golangci-lint cannot express: the SQL rules over the
// query files (1, 2), the Go call-site rules (3, 4) and the secret leak harness (5). Rules 6 to 8 are forbidigo and
// depguard settings in .golangci.yml. The fixtures of all eight rules live under testdata.
package archlint

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type Diagnostic struct {
	Rule int
	// Path is module-relative with forward slashes; for rule 5 it is the probe name.
	Path    string
	Line    int
	Col     int
	Message string
}

func (d Diagnostic) String() string {
	if d.Line == 0 {
		return fmt.Sprintf("%s: archlint rule %d: %s", d.Path, d.Rule, d.Message)
	}
	return fmt.Sprintf("%s:%d:%d: archlint rule %d: %s", d.Path, d.Line, d.Col, d.Rule, d.Message)
}

// Config names the packages, files and tables the rules protect. Paths are module-relative with forward slashes.
type Config struct {
	Module string

	// MigrationsDir holds the *.up.sql migrations that rule 1 reads the tables with an org_id column from.
	MigrationsDir string
	// QueryGlob selects the sqlc query files; "**" matches any number of directories.
	QueryGlob string

	// GroupsDir is the only directory whose query files may write GroupTables (rule 2).
	GroupsDir   string
	GroupTables []string

	Messenger MessengerConfig

	// HTTPExempt lists the directories where rule 4 allows HTTP clients and transports.
	HTTPExempt []string
}

// MessengerConfig describes rule 3: the messenger adapter interface, its send and edit methods, and the only files
// that may call them.
type MessengerConfig struct {
	Package      string
	Interface    string
	Methods      []string
	AllowedFiles []string
}

func DefaultConfig() Config {
	return Config{
		Module:        "github.com/muster-io/muster",
		MigrationsDir: "internal/db/migrations",
		QueryGlob:     "internal/**/query.sql",
		GroupsDir:     "internal/groups",
		GroupTables:   []string{"alert_groups", "alert_group_alerts", "timeline_entries", "notes", "alert_group_counters"},
		Messenger: MessengerConfig{
			Package:   "internal/delivery",
			Interface: "Adapter",
			Methods:   []string{"Publish", "Update", "Reply"},
			AllowedFiles: []string{
				"internal/delivery/worker.go",
				"internal/delivery/threads.go",
				"internal/delivery/interactive.go",
			},
		},
		HTTPExempt: []string{"internal/outbound", "internal/fakes", "internal/devmode", "test/load", "pkg/apiclient"},
	}
}

// Run checks rules 1 to 4 over the module rooted at root and returns the findings sorted by position.
func Run(root string, cfg Config) ([]Diagnostic, error) {
	root, err := filepath.Abs(root)
	if err == nil {
		// The go command reports file names below the resolved directory.
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	sqlDiags, err := checkSQL(root, cfg)
	if err != nil {
		return nil, err
	}
	goDiags, err := checkGo(root, cfg)
	if err != nil {
		return nil, err
	}
	diags := slices.Concat(sqlDiags, goDiags)
	slices.SortFunc(diags, func(a, b Diagnostic) int {
		return cmp.Or(
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Col, b.Col),
			cmp.Compare(a.Rule, b.Rule),
			cmp.Compare(a.Message, b.Message),
		)
	})
	return slices.Compact(diags), nil
}

// inDir reports whether the slash path name is dir or lies below it.
func inDir(name, dir string) bool {
	return name == dir || strings.HasPrefix(name, dir+"/")
}
