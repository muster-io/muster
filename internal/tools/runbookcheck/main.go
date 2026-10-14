// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command runbookcheck checks that the Runbooks section of the documentation site has exactly one page per alert
// Muster can raise (C-21.FR-3, C-19.FR-9): the names of the Internal alert registry and of the chart rules of
// reference.md must equal the pages <pages>/<alertname>.md, and every page must have the headings "## Meaning",
// "## Impact", "## Diagnosis" and "## Fix", each once and in that order. It prints one line per problem and exits
// with 1; make docs runs it before the build.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/muster-io/muster/internal/internalalerts"
)

const (
	chartRulesHeading = "## Chart rules"
	runbookDir        = "operations/runbooks/"
	indexPage         = "index.md"
)

// sections are the headings every runbook page has, in this order.
var sections = []string{"## Meaning", "## Impact", "## Diagnosis", "## Fix"}

var ruleNameRe = regexp.MustCompile(`^Muster[A-Z][A-Za-z0-9]*$`)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("runbookcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pages := flags.String("pages", "docs/operations/runbooks", "the directory of the runbook pages")
	reference := flags.String("reference", "design/prd/l1/reference.md",
		"the PRD page whose Chart rules table lists the chart rules")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	names, err := expectedNames(*reference)
	if err != nil {
		fmt.Fprintf(stderr, "runbookcheck: %v\n", err)
		return 1
	}
	problems, err := check(names, *pages)
	if err != nil {
		fmt.Fprintf(stderr, "runbookcheck: %v\n", err)
		return 1
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stdout, p)
		}
		return 1
	}
	fmt.Fprintf(stdout, "runbook pages match the Internal alerts and the chart rules: %d\n", len(names))
	return 0
}

// expectedNames is the union of the Internal alerts and the chart rules; an Internal alert's runbook must be a page
// of the runbooks directory.
func expectedNames(reference string) (map[string]bool, error) {
	defs, err := internalalerts.Definitions()
	if err != nil {
		return nil, fmt.Errorf("invalid registry: %w", err)
	}
	names := map[string]bool{}
	for _, d := range defs {
		page := strings.TrimSuffix(strings.TrimPrefix(d.Runbook(), runbookDir), "/")
		if page != d.Name || !strings.HasPrefix(d.Runbook(), runbookDir) {
			return nil, fmt.Errorf("internal alert %s: runbook %q is not %s%s/", d.Name, d.Runbook(), runbookDir, d.Name)
		}
		names[d.Name] = true
	}
	data, err := os.ReadFile(reference)
	if err != nil {
		return nil, err
	}
	rules, err := readRules(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", reference, err)
	}
	for _, r := range rules {
		names[r] = true
	}
	return names, nil
}

// readRules reads the rule names from the first column of the table under "## Chart rules".
func readRules(r io.Reader) ([]string, error) {
	rows, err := tableRows(r, chartRulesHeading)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, cells := range rows {
		name := strings.Trim(cells[0], "`")
		if !ruleNameRe.MatchString(name) {
			return nil, fmt.Errorf("chart rule %q is not Muster<Name>", cells[0])
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no rules under %q", chartRulesHeading)
	}
	return out, nil
}

// check compares the expected names with the pages of dir: the missing pages, then the extra ones, then the pages
// whose headings are incomplete, each group sorted by name.
func check(names map[string]bool, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	pages := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || e.Name() == indexPage || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		pages[strings.TrimSuffix(e.Name(), ".md")] = true
	}
	var missing, extra, incomplete []string
	for _, n := range sortedKeys(names) {
		if !pages[n] {
			missing = append(missing, "missing runbook page: "+n)
		}
	}
	for _, p := range sortedKeys(pages) {
		if !names[p] {
			extra = append(extra, "extra runbook page: "+p)
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, p+".md"))
		if err != nil {
			return nil, err
		}
		if detail := headingProblem(data); detail != "" {
			incomplete = append(incomplete, "incomplete runbook page: "+p+": "+detail)
		}
	}
	return slices.Concat(missing, extra, incomplete), nil
}

// headingProblem names what is wrong with the section headings of a page, or returns "" when it has each of them
// once and in order. Lines inside fenced code blocks are not headings.
func headingProblem(data []byte) string {
	count := map[string]int{}
	var order []string
	fenced := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced || !slices.Contains(sections, line) {
			continue
		}
		count[line]++
		order = append(order, line)
	}
	var problems []string
	for _, s := range sections {
		switch count[s] {
		case 0:
			problems = append(problems, "lacks "+s)
		case 1:
		default:
			problems = append(problems, fmt.Sprintf("has %s %d times", s, count[s]))
		}
	}
	if len(problems) > 0 {
		return strings.Join(problems, ", ")
	}
	for i, s := range order {
		if s != sections[i] {
			return fmt.Sprintf("%s before %s", s, sections[i])
		}
	}
	return ""
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// tableRows returns the cells of the rows of the first table under the line heading, without its header and
// separator rows.
func tableRows(r io.Reader, heading string) ([][]string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var rows [][]string
	found, inTable, seen := false, false, 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case !found:
			found = line == heading
		case strings.HasPrefix(line, "|"):
			inTable = true
			if seen++; seen > 2 {
				rows = append(rows, splitRow(line))
			}
		case inTable:
			return rows, nil
		case strings.HasPrefix(line, "#"):
			return nil, fmt.Errorf("no table under %q", heading)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !inTable {
		return nil, fmt.Errorf("no table under %q", heading)
	}
	return rows, nil
}

// splitRow splits a Markdown table row into its trimmed cells; an escaped pipe stays in its cell.
func splitRow(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	var cells []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			b.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(line[i])
		}
	}
	return append(cells, strings.TrimSpace(b.String()))
}
