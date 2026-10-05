// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command licensecheck fails when a source file lacks the licence header. It checks the files git knows about,
// tracked or untracked but not ignored, and skips generated files, fixtures under testdata and the third-party
// paths listed in NOTICE. A source file has one of the source extensions or names, or starts with a shebang line.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
)

const (
	spdxMarker      = "SPDX-License-Identifier: AGPL-3.0-only"
	copyrightMarker = "Copyright The Muster Authors"

	noticeFile    = "NOTICE"
	noticeHeading = "Third-party files excluded from the licence header check:"
	noticeNone    = "(none)"

	headerLines    = 5
	generatedLines = 30
	readLimit      = 16 << 10
)

var (
	sourceExts      = []string{".go", ".sql", ".ts", ".tsx", ".js", ".mjs", ".css", ".sh", ".py"}
	sourceBasenames = []string{"Makefile", "Dockerfile"}
	sourceGlobs     = []string{"deploy/helm/*/templates/**", ".github/workflows/*.yml", ".github/workflows/*.yaml"}
	// generatedGlobs are the generated paths that AGENTS.md lists; their files need no "Code generated" line.
	generatedGlobs = []string{
		"internal/*/dbgen/**",
		"internal/api/gen/**",
		"pkg/apiclient/**",
		"web/src/api/gen/**",
		"web/src/routeTree.gen.ts",
		"docs/reference/**",
	}

	generatedRe = regexp.MustCompile(`^(//|--|#) Code generated .* DO NOT EDIT\.$`)
)

func main() {
	os.Exit(run(os.Stdout, os.Stderr))
}

func run(stdout, stderr io.Writer) int {
	root, files, err := gitFiles()
	if err != nil {
		fmt.Fprintf(stderr, "licensecheck: %v\n", err)
		return 1
	}
	fsys := os.DirFS(root)
	notice, err := fs.ReadFile(fsys, noticeFile)
	if err != nil {
		fmt.Fprintf(stderr, "licensecheck: %v\n", err)
		return 1
	}
	excluded, err := parseExclusions(string(notice))
	if err != nil {
		fmt.Fprintf(stderr, "licensecheck: %s: %v\n", noticeFile, err)
		return 1
	}
	problems, err := check(fsys, files, excluded)
	if err != nil {
		fmt.Fprintf(stderr, "licensecheck: %v\n", err)
		return 1
	}
	for _, p := range problems {
		fmt.Fprintln(stdout, p)
	}
	if len(problems) > 0 {
		fmt.Fprintln(stderr, "licensecheck: the files above need the licence header described in AGENTS.md (Quality bar)")
		return 1
	}
	return 0
}

func gitFiles() (string, []string, error) {
	out, err := git("", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, fmt.Errorf("find repository root: %w", err)
	}
	root := strings.TrimSpace(out)
	out, err = git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return "", nil, fmt.Errorf("list files: %w", err)
	}
	var files []string
	for f := range strings.SplitSeq(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return root, files, nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...) //nolint:noctx // one-shot build tool with nothing to cancel; rule 6 keeps context.Background() out of internal/tools
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// parseExclusions reads the list that follows noticeHeading up to the next blank line. Each entry is
// "- <glob> [free text]"; a single "(none)" line stands for an empty list.
func parseExclusions(notice string) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(notice, "\r\n", "\n"), "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == noticeHeading })
	if start < 0 {
		return nil, fmt.Errorf("heading %q not found", noticeHeading)
	}
	globs := []string{}
	for i, line := range lines[start+1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if line == noticeNone {
			continue
		}
		entry, ok := strings.CutPrefix(line, "- ")
		if !ok {
			return nil, fmt.Errorf("line %d: want \"- <glob>\" or %q, got %q", start+i+2, noticeNone, line)
		}
		glob := strings.Fields(entry)[0]
		if err := validateGlob(glob); err != nil {
			return nil, fmt.Errorf("line %d: %w", start+i+2, err)
		}
		globs = append(globs, glob)
	}
	return globs, nil
}

func validateGlob(glob string) error {
	dir, _ := strings.CutSuffix(glob, "/**")
	if strings.Contains(dir, "**") {
		return fmt.Errorf("glob %q: \"**\" is allowed only as a trailing \"/**\"", glob)
	}
	if _, err := path.Match(dir, ""); err != nil {
		return fmt.Errorf("glob %q: %w", glob, err)
	}
	return nil
}

// matchGlob matches segment by segment with path.Match; a trailing "/**" matches every path below the directory.
func matchGlob(glob, name string) bool {
	dir, recursive := strings.CutSuffix(glob, "/**")
	if !recursive {
		ok, _ := path.Match(glob, name)
		return ok
	}
	depth := strings.Count(dir, "/") + 1
	segments := strings.Split(name, "/")
	if len(segments) <= depth {
		return false
	}
	ok, _ := path.Match(dir, strings.Join(segments[:depth], "/"))
	return ok
}

func matchAny(globs []string, name string) bool {
	return slices.ContainsFunc(globs, func(g string) bool { return matchGlob(g, name) })
}

func isSource(name string) bool {
	return slices.Contains(sourceExts, path.Ext(name)) ||
		slices.Contains(sourceBasenames, path.Base(name)) ||
		matchAny(sourceGlobs, name)
}

func isFixture(name string) bool {
	return slices.Contains(strings.Split(name, "/"), "testdata")
}

func check(fsys fs.FS, files, excluded []string) ([]string, error) {
	var problems []string
	for _, name := range slices.Sorted(slices.Values(files)) {
		if isFixture(name) || matchAny(generatedGlobs, name) || matchAny(excluded, name) {
			continue
		}
		missing, err := checkFile(fsys, name, isSource(name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, m := range missing {
			problems = append(problems, fmt.Sprintf("%s: missing %q", name, m))
		}
	}
	return problems, nil
}

// checkFile returns the markers missing from the header of name. A file that is not a source by its name counts as
// one when it starts with a shebang line.
func checkFile(fsys fs.FS, name string, source bool) ([]string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.IsDir() {
		return nil, err
	}
	head, err := io.ReadAll(io.LimitReader(f, readLimit))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if !source && !bytes.HasPrefix(head, []byte("#!")) {
		return nil, nil
	}
	lines := strings.Split(string(bytes.ReplaceAll(head, []byte("\r\n"), []byte("\n"))), "\n")
	if slices.ContainsFunc(lines[:min(len(lines), generatedLines)], generatedRe.MatchString) {
		return nil, nil
	}
	if strings.HasPrefix(lines[0], "#!") {
		lines = lines[1:]
	}
	header := strings.Join(lines[:min(len(lines), headerLines)], "\n")
	var missing []string
	for _, marker := range []string{spdxMarker, copyrightMarker} {
		if !strings.Contains(header, marker) {
			missing = append(missing, marker)
		}
	}
	return missing, nil
}
