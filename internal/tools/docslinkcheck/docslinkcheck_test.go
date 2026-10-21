// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// site writes a built site with its pages under a temporary directory and the list of links beside it.
func site(t *testing.T, links string, pages map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "site")
	for name, body := range pages {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	list := filepath.Join(dir, "docs-links.json")
	if err := os.WriteFile(list, []byte(links), 0o600); err != nil {
		t.Fatal(err)
	}
	return list, root
}

var builtPages = map[string]string{
	"concepts/routes-and-group-keys/index.html": `<html><body><h1 id="routes-and-group-keys">Routes</h1>
<h2 id=no-quotes>A</h2><h2 class="x" ID='single-quotes'>B</h2><p data-id="not-an-id">C</p></body></html>`,
	"messengers/index.html": `<h2 id="what-each-destination-type-can-do">What each</h2>`,
	"index.html":            `<h1 id="muster">Muster</h1>`,
}

// TestPassingList is C-21.FR-12: every page and anchor of the list exists, and the count is printed.
func TestPassingList(t *testing.T) {
	list, root := site(t, `{
  "page.connections": "messengers/index.md",
  "page.destinations": "messengers/index.md#what-each-destination-type-can-do",
  "page.home": "index.md#muster",
  "page.routes": "concepts/routes-and-group-keys.md",
  "page.routes.a": "concepts/routes-and-group-keys.md#no-quotes",
  "page.routes.b": "concepts/routes-and-group-keys.md#single-quotes"
}`, builtPages)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-links", list, "-site", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("run = %d:\n%s%s", code, stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "6 documentation links of the web UI: every page and anchor exists") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// TestProblems is C-21.AC-9: a missing page and a missing anchor fail, each named by its key and target.
func TestProblems(t *testing.T) {
	list, root := site(t, `{
  "page.alert_groups": "concepts/alert-groups.md",
  "page.bad": "../outside.md",
  "page.data": "concepts/routes-and-group-keys.md#not-an-id",
  "page.routes": "concepts/routes-and-group-keys.md#no-such-section",
  "page.ok": "messengers/index.md"
}`, builtPages)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-links", list, "-site", root}, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatalf("run = %d:\n%s%s", code, stdout.String(), stderr.String())
	}
	page := filepath.Join(root, "concepts", "routes-and-group-keys", "index.html")
	want := []string{
		"page.alert_groups: concepts/alert-groups.md: the site has no page " +
			filepath.Join(root, "concepts", "alert-groups", "index.html"),
		"page.bad: ../outside.md: not a page of docs/ (<path>.md or <path>.md#<anchor>)",
		"page.data: concepts/routes-and-group-keys.md#not-an-id: no element with this id in " + page,
		"page.routes: concepts/routes-and-group-keys.md#no-such-section: no element with this id in " + page,
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if len(lines) != len(want)+1 || strings.Join(lines[:len(want)], "\n") != strings.Join(want, "\n") {
		t.Errorf("stderr:\n%s\nwant:\n%s", stderr.String(), strings.Join(want, "\n"))
	}
}

// TestSiteNotBuilt: without the built site, or with a file in its place, one line says so instead of a line per entry.
func TestSiteNotBuilt(t *testing.T) {
	list, root := site(t,
		`{"page.routes": "concepts/routes-and-group-keys.md", "page.connections": "messengers/index.md"}`,
		map[string]string{"../file": "not a directory"})
	for _, dir := range []string{root, filepath.Join(filepath.Dir(root), "file")} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"-links", list, "-site", dir}, &stdout, &stderr); code != 1 ||
			stderr.String() != "site not built: "+dir+"\n" || stdout.Len() != 0 {
			t.Errorf("%s: run = %d, stdout %q, stderr %q", dir, code, stdout.String(), stderr.String())
		}
	}
}

func TestBadInput(t *testing.T) {
	list, root := site(t, `[]`, nil)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-links", list, "-site", root}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "docs-links.json") {
		t.Errorf("a list that is no object = %d %q", code, stderr.String())
	}
	list, root = site(t, `{}`, nil)
	stderr.Reset()
	if code := run([]string{"-links", list, "-site", root}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "holds no link") {
		t.Errorf("an empty list = %d %q", code, stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"-links", filepath.Join(root, "missing.json")}, &stdout, &stderr); code != 1 {
		t.Errorf("a missing list = %d", code)
	}
	if code := run([]string{"-no-such-flag"}, &stdout, &stderr); code != 2 {
		t.Errorf("a wrong flag = %d", code)
	}
}

func TestElementIDs(t *testing.T) {
	doc := `<!DOCTYPE html><!-- <a id="in-comment"> --><a id="double">x</a><b id='single'></b><i id=bare>` +
		`<script>var s = '<a id="in-script">';</script><style>p{}</style><p id = "spaced" ></p><br id=empty/>` +
		`<h2 id="caf&eacute;"></h2><div ID="Upper" hidden></div><span data-id="data"></span> a < b <p id=last`
	got := elementIDs(doc)
	for _, id := range []string{"double", "single", "bare", "spaced", "empty/", "café", "Upper", "last"} {
		if !got[id] {
			t.Errorf("id %q not found in %v", id, got)
		}
	}
	for _, id := range []string{"in-comment", "in-script", "data"} {
		if got[id] {
			t.Errorf("id %q found", id)
		}
	}
}
