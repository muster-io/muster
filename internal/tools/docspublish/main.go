// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Command docspublish copies a built documentation site into a checkout of the gh-pages branch (C-21.FR-1, C-21.AC-1):
// it replaces <target>/<version>/, keeps versions.json in the format of mike, which the version selector of the site
// reads, and points the root index.html at the newest published minor, or at latest/ while no release is published.
// It copies the llms.txt of that version into the root, so that <site>/llms.txt answers with links into the version the
// root redirects to, and removes the root copy when that version has none (C-21.FR-8).
// It refuses a minor older than the newest published one, so the build of an older minor stays as it was published;
// a minor that is not published yet may always be published first.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	latest       = "latest"
	versionsFile = "versions.json"
	llmsFile     = "llms.txt"
)

var minorRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// errUsage marks an error in the arguments, which exits with 2.
var errUsage = errors.New("usage")

// refusal is the refusal to change an older minor, printed as it is.
type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

// entry is a published version as versions.json lists it.
type entry struct {
	Version string   `json:"version"`
	Title   string   `json:"title"`
	Aliases []string `json:"aliases"`
}

// minor is a release minor, major.minor.
type minor struct{ major, minor int }

func parseMinor(v string) (minor, bool) {
	m := minorRe.FindStringSubmatch(v)
	if m == nil {
		return minor{}, false
	}
	major, err1 := strconv.Atoi(m[1])
	mi, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return minor{}, false
	}
	return minor{major, mi}, true
}

func (a minor) compare(b minor) int {
	if a.major != b.major {
		return a.major - b.major
	}
	return a.minor - b.minor
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("docspublish", flag.ContinueOnError)
	flags.SetOutput(stderr)
	site := flags.String("site", "site", "the built site")
	target := flags.String("target", "", "the checkout of the gh-pages branch")
	version := flags.String("version", "", "the version to publish: latest or <major>.<minor>")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *target == "" || *version == "" || flags.NArg() > 0 {
		fmt.Fprintln(stderr, "docspublish: --target and --version are required, and nothing else")
		flags.Usage()
		return 2
	}
	if err := publish(*site, *target, *version, stdout); err != nil {
		if r, ok := errors.AsType[refusal](err); ok {
			fmt.Fprintln(stderr, r.msg)
			return 1
		}
		fmt.Fprintf(stderr, "docspublish: %v\n", err)
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	return 0
}

// publish copies site into target/version and updates versions.json and the root redirect; on a refusal nothing in
// target changes.
func publish(site, target, version string, stdout io.Writer) error {
	published, isMinor := parseMinor(version)
	if !isMinor && version != latest {
		return fmt.Errorf("%w: version %q is not latest or <major>.<minor>", errUsage, version)
	}
	if _, err := os.Stat(filepath.Join(site, "index.html")); err != nil {
		return fmt.Errorf("%s has no index.html; run make docs", site)
	}
	if err := checkApart(site, target); err != nil {
		return err
	}
	entries, err := readVersions(filepath.Join(target, versionsFile))
	if err != nil {
		return err
	}
	if newest, ok := newestMinor(entries); isMinor && ok {
		if n, _ := parseMinor(newest); published.compare(n) < 0 {
			return refusal{fmt.Sprintf(
				"version %s is older than the newest published minor %s; older minors are not changed", version, newest)}
		}
	}
	dir := filepath.Join(target, version)
	if err := replaceDir(site, target, dir); err != nil {
		return err
	}
	entries = upsert(entries, version)
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, versionsFile), append(data, '\n'), 0o600); err != nil {
		return err
	}
	redirect := latest + "/"
	if newest, ok := newestMinor(entries); ok {
		redirect = newest + "/"
	}
	if err := os.WriteFile(filepath.Join(target, "index.html"), []byte(redirectPage(redirect)), 0o600); err != nil {
		return err
	}
	rootLLMS, err := copyRootLLMS(target, redirect)
	if err != nil {
		return err
	}
	// GitHub Pages serves the files as they are, without Jekyll, which would drop the directories that start with _.
	if err := os.WriteFile(filepath.Join(target, ".nojekyll"), nil, 0o600); err != nil {
		return err
	}
	llms := "which has no " + llmsFile
	if rootLLMS {
		llms = "and serves its " + llmsFile
	}
	fmt.Fprintf(stdout, "published %s into %s; the root redirects to %s %s\n", version, dir, redirect, llms)
	return nil
}

// copyRootLLMS copies the llms.txt of the version that the root redirects to into the root, or leaves the root without
// one when that version has none, so that it never points into another version; it reports whether the root has one.
// The root copy is removed first, so that a symbolic link in its place is never written through.
func copyRootLLMS(target, redirect string) (bool, error) {
	root, err := os.OpenRoot(target)
	if err != nil {
		return false, err
	}
	defer root.Close()
	if err := root.Remove(llmsFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	src := filepath.Join(redirect, llmsFile)
	info, err := root.Lstat(src)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case !info.Mode().IsRegular():
		return false, nil
	}
	data, err := root.ReadFile(src)
	if err != nil {
		return false, err
	}
	return true, root.WriteFile(llmsFile, data, 0o600)
}

// replaceDir copies site into a temporary directory beside dir and then puts it in the place of dir, so that a failed
// copy leaves the published version as it was.
func replaceDir(site, target, dir string) error {
	if err := os.MkdirAll(target, 0o750); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(target, ".docspublish-")
	if err != nil {
		return err
	}
	// After the rename only the empty temporary directory is left to remove.
	defer os.RemoveAll(tmp)
	staged := filepath.Join(tmp, "site")
	if err := os.CopyFS(staged, os.DirFS(site)); err != nil {
		return fmt.Errorf("copy %s into %s: %w", site, staged, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(staged, dir)
}

// checkApart refuses a site that lies inside the target, which the replaced version could hold, and a target that
// lies inside the site, which the copy would walk into.
func checkApart(site, target string) error {
	siteInTarget, err := within(site, target)
	if err != nil {
		return err
	}
	if siteInTarget {
		return fmt.Errorf("%w: the site %s lies inside the target %s", errUsage, site, target)
	}
	targetInSite, err := within(target, site)
	if err != nil {
		return err
	}
	if targetInSite {
		return fmt.Errorf("%w: the target %s lies inside the site %s", errUsage, target, site)
	}
	return nil
}

// within reports whether path is dir or lies inside it, after both are made absolute and their symbolic links are
// resolved.
func within(path, dir string) (bool, error) {
	p, err := resolve(path)
	if err != nil {
		return false, err
	}
	d, err := resolve(dir)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(d, p)
	if err != nil {
		return false, nil //nolint:nilerr // paths on different volumes are not inside each other
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

// resolve makes path absolute and resolves the symbolic links of its longest existing part.
func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rest := ""
	for p := abs; ; p = filepath.Dir(p) {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if filepath.Dir(p) == p {
			return abs, nil
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// readVersions reads versions.json; a missing file is no version published yet.
func readVersions(path string) ([]entry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, e := range entries {
		if _, ok := parseMinor(e.Version); !ok && e.Version != latest {
			return nil, fmt.Errorf("%s: version %q is not latest or <major>.<minor>", path, e.Version)
		}
	}
	return entries, nil
}

func newestMinor(entries []entry) (string, bool) {
	var newest string
	var best minor
	for _, e := range entries {
		if m, ok := parseMinor(e.Version); ok && (newest == "" || m.compare(best) > 0) {
			newest, best = e.Version, m
		}
	}
	return newest, newest != ""
}

// upsert adds version unless it is listed, and sorts the list: latest first, then the minors from the newest.
func upsert(entries []entry, version string) []entry {
	if !slices.ContainsFunc(entries, func(e entry) bool { return e.Version == version }) {
		entries = append(entries, entry{Version: version, Title: version})
	}
	for i := range entries {
		if entries[i].Aliases == nil {
			entries[i].Aliases = []string{}
		}
	}
	slices.SortStableFunc(entries, func(a, b entry) int {
		ma, aMinor := parseMinor(a.Version)
		mb, bMinor := parseMinor(b.Version)
		switch {
		case !aMinor && !bMinor:
			return 0
		case !aMinor:
			return -1
		case !bMinor:
			return 1
		}
		return mb.compare(ma)
	})
	return entries
}

// redirectPage is the root index.html: it sends a visitor to the directory to.
func redirectPage(to string) string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Muster documentation</title>
<meta http-equiv="refresh" content="0; url=` + to + `">
<link rel="canonical" href="` + to + `">
</head>
<body><p><a href="` + to + `">Muster documentation</a></p></body>
</html>
`
}
