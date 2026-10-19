// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command tablecheck fails when a table on a page of the documentation site holds inline code longer than -max
// characters. Such a name, a setting, a chart value, a variable, a code or a path, does not fit a table column on a
// phone and often breaks in the middle on the desktop too, so the page lays those entries out as a block each: a
// definition list with the name as the term and its description under it. A table that must stay a table is preceded
// by the comment <!-- tablecheck: allow <reason> -->. It reads the Markdown files under the given directories (docs by
// default), prints one line per such code span and exits with 1; make docs-check runs it before the build.
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const defaultMax = 24

var (
	allowRe    = regexp.MustCompile(`^<!--\s*tablecheck:\s*allow\s+\S.*-->$`)
	allowAnyRe = regexp.MustCompile(`^<!--\s*tablecheck:`)
	delimCell  = regexp.MustCompile(`^:?-+:?$`)
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("tablecheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	maxLen := flags.Int("max", defaultMax, "the most characters of inline code that a table cell may hold")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	roots := flags.Args()
	if len(roots) == 0 {
		roots = []string{"docs"}
	}
	var problems []string
	for _, root := range roots {
		fsys := os.DirFS(root)
		err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || path.Ext(name) != ".md" {
				return err
			}
			content, err := fs.ReadFile(fsys, name)
			if err != nil {
				return err
			}
			problems = append(problems, check(filepath.Join(root, name), string(content), *maxLen)...)
			return nil
		})
		if err != nil {
			fmt.Fprintf(stderr, "tablecheck: %v\n", err)
			return 1
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stdout, p)
		}
		fmt.Fprintln(stdout, "Lay such a table out as a block per entry: a definition list with the name as the term, "+
			"then a line starting with \":   \" and its description. A table that must stay one is preceded by "+
			"<!-- tablecheck: allow <reason> -->.")
		return 1
	}
	fmt.Fprintf(stdout, "no table holds inline code longer than %d characters\n", *maxLen)
	return 0
}

// check returns one problem per code span longer than maxLen in a table of the Markdown page content, and one per
// tablecheck comment without a reason. Fenced code blocks are skipped.
func check(file, content string, maxLen int) []string {
	lines := strings.Split(content, "\n")
	var problems []string
	fence := ""
	for i := 0; i < len(lines); i++ {
		if fence = fenceState(fence, lines[i]); fence != "" {
			continue
		}
		if allowAnyRe.MatchString(strings.TrimSpace(lines[i])) && !allowRe.MatchString(strings.TrimSpace(lines[i])) {
			problems = append(problems, fmt.Sprintf("%s:%d: a tablecheck comment needs a reason: "+
				"<!-- tablecheck: allow <reason> -->", file, i+1))
		}
		if i+1 >= len(lines) || !strings.Contains(lines[i], "|") || !isDelimiterRow(lines[i+1]) {
			continue
		}
		end := i + 2
		for end < len(lines) && strings.TrimSpace(lines[end]) != "" && strings.Contains(lines[end], "|") {
			end++
		}
		if !allowed(lines, i) {
			for n := i; n < end; n++ {
				for _, code := range codeSpans(lines[n]) {
					if l := utf8.RuneCountInString(code); l > maxLen {
						problems = append(problems, fmt.Sprintf("%s:%d: a table holds inline code of %d characters, "+
							"more than %d: %s", file, n+1, l, maxLen, code))
					}
				}
			}
		}
		i = end - 1
	}
	return problems
}

// fenceState returns the code fence that is open after line, given the one open before it; "" is none.
func fenceState(open, line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if open != "" {
		if strings.HasPrefix(trimmed, open) && strings.Trim(trimmed, open[:1]+" \t") == "" {
			return ""
		}
		return open
	}
	for _, c := range []byte{'`', '~'} {
		if run := leadingRun(trimmed, c); len(run) >= 3 {
			return run
		}
	}
	return ""
}

func leadingRun(s string, c byte) string {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return s[:n]
}

// allowed reports whether the nearest non-blank line before the table at line start is an allow comment.
func allowed(lines []string, start int) bool {
	for k := start - 1; k >= 0; k-- {
		if t := strings.TrimSpace(lines[k]); t != "" {
			return allowRe.MatchString(t)
		}
	}
	return false
}

// isDelimiterRow reports whether line is the row under the header of a table, such as |---|:--:|.
func isDelimiterRow(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.Contains(t, "|") {
		return false
	}
	t = strings.TrimSuffix(strings.TrimPrefix(t, "|"), "|")
	for cell := range strings.SplitSeq(t, "|") {
		if !delimCell.MatchString(strings.TrimSpace(cell)) {
			return false
		}
	}
	return true
}

// codeSpans returns the contents of the inline code spans of line as CommonMark reads them: a run of backticks opens a
// span that the next run of the same length closes, and one space is stripped from each end when both ends have one.
func codeSpans(line string) []string {
	var spans []string
	for i := 0; i < len(line); {
		if line[i] == '\\' {
			i += 2
			continue
		}
		if line[i] != '`' {
			i++
			continue
		}
		open := leadingRun(line[i:], '`')
		start := i + len(open)
		closeAt := closingRun(line, start, len(open))
		if closeAt < 0 {
			i = start
			continue
		}
		code := line[start:closeAt]
		if len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
			code = code[1 : len(code)-1]
		}
		spans = append(spans, code)
		i = closeAt + len(open)
	}
	return spans
}

// closingRun returns the index of the next run of exactly n backticks in line at or after from, or -1.
func closingRun(line string, from, n int) int {
	for j := from; j < len(line); {
		if line[j] != '`' {
			j++
			continue
		}
		run := leadingRun(line[j:], '`')
		if len(run) == n {
			return j
		}
		j += len(run)
	}
	return -1
}
