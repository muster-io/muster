// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// writeSite writes a built site with an index page, a metrics page and the file marker.
func writeSite(t *testing.T, marker string) string {
	t.Helper()
	return writeSiteWith(t, marker, nil)
}

// writeSiteWith writes the site of writeSite with the extra files.
func writeSiteWith(t *testing.T, marker string, extra map[string]string) string {
	t.Helper()
	site := t.TempDir()
	files := map[string]string{
		"index.html":                       "<html>home</html>",
		"reference/metrics/index.html":     "<html>muster_leader</html>",
		"assets/" + marker + ".js":         marker,
		"reference/api/spec/index.html":    "<html>getSystemStatus</html>",
		"brand/muster-mark-on-dark.svg":    "<svg/>",
		"reference/environment/index.html": "<html>MUSTER_SECRET_KEYS</html>",
	}
	maps.Copy(files, extra)
	for name, content := range files {
		path := filepath.Join(site, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return site
}

func publishVersion(t *testing.T, site, target, version string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run([]string{"--site", site, "--target", target, "--version", version}, &stdout, &stderr)
	return code, stderr.String()
}

func mustPublish(t *testing.T, site, target, version string) {
	t.Helper()
	if code, stderr := publishVersion(t, site, target, version); code != 0 {
		t.Fatalf("publish %s = %d: %s", version, code, stderr)
	}
}

func versions(t *testing.T, target string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(target, versionsFile))
	if err != nil {
		t.Fatal(err)
	}
	var entries []entry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Title != e.Version || e.Aliases == nil || len(e.Aliases) != 0 {
			t.Errorf("entry %+v, want the version as its title and no aliases", e)
		}
		out = append(out, e.Version)
	}
	if !bytes.Contains(data, []byte(`"aliases": []`)) {
		t.Errorf("versions.json writes aliases as null:\n%s", data)
	}
	return out
}

var redirectRe = regexp.MustCompile(`url=([^"]*)`)

func redirect(t *testing.T, target string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(target, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	m := redirectRe.FindAllStringSubmatch(string(data), -1)
	if len(m) != 1 {
		t.Fatalf("index.html has %d redirects:\n%s", len(m), data)
	}
	return m[0][1]
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestPublishLatestBeforeRelease(t *testing.T) {
	site, target := writeSite(t, "a"), t.TempDir()
	mustPublish(t, site, target, "latest")
	if got := versions(t, target); !slices.Equal(got, []string{"latest"}) {
		t.Errorf("versions = %v", got)
	}
	if got := redirect(t, target); got != "latest/" {
		t.Errorf("the root redirects to %q, want latest/ while no release is published", got)
	}
	for _, f := range []string{".nojekyll", "latest/index.html", "latest/reference/metrics/index.html",
		"latest/reference/api/spec/index.html"} {
		if !exists(t, filepath.Join(target, f)) {
			t.Errorf("%s is missing", f)
		}
	}
}

func TestPublishFirstMinor(t *testing.T) {
	site := writeSite(t, "a")
	target := filepath.Join(t.TempDir(), "gh-pages")
	mustPublish(t, site, target, "0.4")
	if got := versions(t, target); !slices.Equal(got, []string{"0.4"}) {
		t.Errorf("versions = %v", got)
	}
	if got := redirect(t, target); got != "0.4/" {
		t.Errorf("the root redirects to %q", got)
	}
}

// TestPublishSequence is the sequence of the story's verification, then a newer minor.
func TestPublishSequence(t *testing.T) {
	site, target := writeSite(t, "a"), t.TempDir()
	mustPublish(t, site, target, "latest")
	mustPublish(t, site, target, "0.4")
	if got := versions(t, target); !slices.Equal(got, []string{"latest", "0.4"}) {
		t.Errorf("versions = %v", got)
	}
	if got := redirect(t, target); got != "0.4/" {
		t.Errorf("the root redirects to %q", got)
	}
	mustPublish(t, site, target, "0.5")
	mustPublish(t, site, target, "latest")
	if got := versions(t, target); !slices.Equal(got, []string{"latest", "0.5", "0.4"}) {
		t.Errorf("versions = %v", got)
	}
	if got := redirect(t, target); got != "0.5/" {
		t.Errorf("the root redirects to %q", got)
	}
}

// llmsSite writes the site of writeSite with an llms.txt that names the marker.
func llmsSite(t *testing.T, marker string) string {
	t.Helper()
	return writeSiteWith(t, marker, map[string]string{llmsFile: "# Muster " + marker + "\n"})
}

// rootLLMS returns the root llms.txt, or "" when the root has none.
func rootLLMS(t *testing.T, target string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(target, llmsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRootLLMSFollowsTheRedirect(t *testing.T) {
	target := t.TempDir()
	mustPublish(t, llmsSite(t, "latest"), target, "latest")
	if got := rootLLMS(t, target); got != "# Muster latest\n" {
		t.Errorf("the root llms.txt is %q, want that of latest/", got)
	}
	mustPublish(t, llmsSite(t, "0.4"), target, "0.4")
	if got := rootLLMS(t, target); got != "# Muster 0.4\n" {
		t.Errorf("the root llms.txt is %q, want that of 0.4/", got)
	}
	mustPublish(t, llmsSite(t, "latest again"), target, "latest")
	if got := rootLLMS(t, target); got != "# Muster 0.4\n" {
		t.Errorf("the root llms.txt is %q, want that of 0.4/, where the root still redirects", got)
	}
	if got := redirect(t, target); got != "0.4/" {
		t.Errorf("the root redirects to %q", got)
	}
}

func TestRootLLMSRemovedWhenTheVersionHasNone(t *testing.T) {
	target := t.TempDir()
	mustPublish(t, llmsSite(t, "latest"), target, "latest")
	if rootLLMS(t, target) == "" {
		t.Fatal("the root has no llms.txt after publishing latest with one")
	}
	mustPublish(t, writeSite(t, "a"), target, "0.4")
	if got := rootLLMS(t, target); got != "" {
		t.Errorf("the root keeps the llms.txt of latest/ while it redirects to 0.4/, which has none: %q", got)
	}

	stale := t.TempDir()
	if err := os.WriteFile(filepath.Join(stale, llmsFile), []byte("# Muster earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustPublish(t, writeSite(t, "a"), stale, "latest")
	if got := rootLLMS(t, stale); got != "" {
		t.Errorf("a root llms.txt of an earlier publication is left: %q", got)
	}
}

// TestRootLLMSNeverFollowsALink publishes over a root llms.txt that is a symbolic link to another file of the target,
// which stays as it was.
func TestRootLLMSNeverFollowsALink(t *testing.T) {
	target := t.TempDir()
	keep := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("keep.txt", filepath.Join(target, llmsFile)); err != nil {
		t.Fatal(err)
	}
	mustPublish(t, llmsSite(t, "latest"), target, "latest")
	if data, err := os.ReadFile(keep); err != nil || string(data) != "keep\n" {
		t.Errorf("the file the link pointed at is %q, %v; want it unchanged", data, err)
	}
	info, err := os.Lstat(filepath.Join(target, llmsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || rootLLMS(t, target) != "# Muster latest\n" {
		t.Errorf("the root llms.txt is %v with %q, want a file with that of latest/", info.Mode(), rootLLMS(t, target))
	}
}

// TestPublishReplacesVersion covers a documentation-only correction of the newest minor: its directory is replaced.
func TestPublishReplacesVersion(t *testing.T) {
	target := t.TempDir()
	mustPublish(t, writeSite(t, "old"), target, "0.4")
	mustPublish(t, writeSite(t, "new"), target, "0.4")
	if exists(t, filepath.Join(target, "0.4/assets/old.js")) {
		t.Error("a file of the replaced build is left")
	}
	if !exists(t, filepath.Join(target, "0.4/assets/new.js")) {
		t.Error("the new build is missing")
	}
	if got := versions(t, target); !slices.Equal(got, []string{"0.4"}) {
		t.Errorf("versions = %v", got)
	}
}

func TestPublishReplacesOnlyItsVersion(t *testing.T) {
	target := t.TempDir()
	mustPublish(t, writeSite(t, "latest-build"), target, "latest")
	mustPublish(t, writeSite(t, "first"), target, "0.4")
	mustPublish(t, writeSite(t, "second"), target, "0.4")
	if !exists(t, filepath.Join(target, "latest/assets/latest-build.js")) {
		t.Error("replacing 0.4 changed latest")
	}
	if exists(t, filepath.Join(target, "0.4/assets/first.js")) || !exists(t, filepath.Join(target, "0.4/assets/second.js")) {
		t.Error("0.4 is not the second build")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".docspublish-") {
			t.Errorf("the temporary directory %s is left", e.Name())
		}
	}
}

func TestPublishRefusesOverlappingSiteAndTarget(t *testing.T) {
	target := t.TempDir()
	mustPublish(t, writeSite(t, "a"), target, "0.4")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	site := writeSite(t, "b")
	for name, tc := range map[string]struct{ site, target, want string }{
		"the site is the version":     {filepath.Join(target, "0.4"), target, "lies inside the target"},
		"the site is the target":      {target, target, "lies inside the target"},
		"the site through a link":     {filepath.Join(link, "0.4"), target, "lies inside the target"},
		"the target inside the site":  {site, filepath.Join(site, "gh-pages"), "lies inside the site"},
		"a relative target with dots": {filepath.Join(target, "0.4"), filepath.Join(target, "x", ".."), "lies inside"},
	} {
		if code, stderr := publishVersion(t, tc.site, tc.target, "0.4"); code != 2 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: run = %d: %s", name, code, stderr)
		}
	}
	if !exists(t, filepath.Join(target, "0.4/assets/a.js")) {
		t.Error("a refused run changed the published version")
	}
	sibling := filepath.Join(filepath.Dir(target), filepath.Base(target)+"-site")
	if within, err := within(sibling, target); err != nil || within {
		t.Errorf("within(%s, %s) = %v, %v", sibling, target, within, err)
	}
}

func TestPublishRefusesOlderMinor(t *testing.T) {
	site, target := writeSite(t, "a"), t.TempDir()
	mustPublish(t, site, target, "latest")
	mustPublish(t, site, target, "0.4")
	before := map[string][]byte{}
	for _, f := range []string{versionsFile, "index.html"} {
		data, err := os.ReadFile(filepath.Join(target, f))
		if err != nil {
			t.Fatal(err)
		}
		before[f] = data
	}
	code, stderr := publishVersion(t, site, target, "0.3")
	if code != 1 {
		t.Errorf("publish 0.3 = %d", code)
	}
	if want := "version 0.3 is older than the newest published minor 0.4; older minors are not changed\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if exists(t, filepath.Join(target, "0.3")) {
		t.Error("0.3/ was written")
	}
	for f, data := range before {
		after, err := os.ReadFile(filepath.Join(target, f))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, after) {
			t.Errorf("%s changed", f)
		}
	}
}

func TestMinorOrderIsNumeric(t *testing.T) {
	site, target := writeSite(t, "a"), t.TempDir()
	mustPublish(t, site, target, "0.9")
	mustPublish(t, site, target, "0.10")
	mustPublish(t, site, target, "1.0")
	if got := versions(t, target); !slices.Equal(got, []string{"1.0", "0.10", "0.9"}) {
		t.Errorf("versions = %v", got)
	}
	if got := redirect(t, target); got != "1.0/" {
		t.Errorf("the root redirects to %q", got)
	}
	if code, _ := publishVersion(t, site, target, "0.10"); code != 1 {
		t.Errorf("publish 0.10 after 1.0 = %d", code)
	}
}

func TestInvalidArguments(t *testing.T) {
	site, target := writeSite(t, "a"), t.TempDir()
	for _, version := range []string{"1", "v0.4", "0.4.1", "01.2", "0.04", "../x", "LATEST"} {
		if code, stderr := publishVersion(t, site, target, version); code != 2 ||
			!strings.Contains(stderr, "is not latest or <major>.<minor>") {
			t.Errorf("version %q: run = %d: %s", version, code, stderr)
		}
	}
	for name, args := range map[string][]string{
		"an unknown flag":    {"--unknown"},
		"no target":          {"--version", "latest"},
		"no version":         {"--target", target},
		"an extra argument":  {"--target", target, "--version", "latest", "extra"},
		"an empty --version": {"--target", target, "--version", ""},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%s: run = %d", name, code)
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the target has %d entries after refused runs", len(entries))
	}
}

func TestPublishErrors(t *testing.T) {
	site := writeSite(t, "a")
	if code, stderr := publishVersion(t, t.TempDir(), t.TempDir(), "latest"); code != 1 ||
		!strings.Contains(stderr, "has no index.html; run make docs") {
		t.Errorf("a site without index.html: run = %d: %s", code, stderr)
	}
	for name, content := range map[string]string{
		"malformed":          "{",
		"an unknown version": `[{"version": "dev", "title": "dev", "aliases": []}]`,
	} {
		target := t.TempDir()
		if err := os.WriteFile(filepath.Join(target, versionsFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, stderr := publishVersion(t, site, target, "latest"); code != 1 ||
			!strings.Contains(stderr, versionsFile) {
			t.Errorf("%s versions.json: run = %d: %s", name, code, stderr)
		}
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := publishVersion(t, site, filepath.Join(file, "gh-pages"), "latest"); code != 1 {
		t.Errorf("a target under a file: run = %d", code)
	}
}
