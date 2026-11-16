// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the expected outputs under testdata/want")

const base = "https://muster-io.github.io/muster/latest/"

// navPages are the pages of the fixture's navigation, in its order.
var navPages = []string{
	"index.md", "guide.md", "section/index.md", "section/titled.md", "section/nested/deep.md", "after.md",
	"reference/api.md",
}

// sectionFiles are the files of the fixture's full text and their pages, in their order: the top-level pages outside
// any section, then each section.
var sectionFiles = []struct {
	name  string
	pages []string
}{
	{"llms-full-home.txt", []string{"index.md", "guide.md", "after.md"}},
	{"llms-full-section.txt", []string{"section/index.md", "section/titled.md", "section/nested/deep.md"}},
	{"llms-full-reference.txt", []string{"reference/api.md"}},
}

// outputs are every file of the full text, the list and the guide included, in the order of the summary.
var outputs = []string{
	"llms-full.txt", "llms-full-guide.txt", "llms-full-home.txt", "llms-full-section.txt", "llms-full-reference.txt",
}

// fixture copies the fixture repository into a temporary directory, which a run and the test may change.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("testdata/repo")); err != nil {
		t.Fatal(err)
	}
	return root
}

func generateIn(t *testing.T, root string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(append([]string{"-root", root}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func mustGenerate(t *testing.T, root string, args ...string) string {
	t.Helper()
	code, stdout, stderr := generateIn(t, root, args...)
	if code != 0 {
		t.Fatalf("run = %d:\n%s", code, stderr)
	}
	return stdout
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func replaceIn(t *testing.T, root, rel, old, replacement string) {
	t.Helper()
	content := readFile(t, root, rel)
	if !strings.Contains(content, old) {
		t.Fatalf("%s does not contain %q", rel, old)
	}
	writeFile(t, root, rel, strings.Replace(content, old, replacement, 1))
}

func removeFile(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

func exists(t *testing.T, root, rel string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// golden compares an output with its expected file under testdata/want, which -update rewrites.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "want", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s:\n%s", name, path, got)
	}
}

// TestGenerate covers the order of a nested navigation, top-level pages before and after a section, titles from the
// navigation and from headings, descriptions, every template function with a present value, and the line of sizes.
// The mkdocs.yml of the fixture carries a !!python/name: tag, which the run reads past.
func TestGenerate(t *testing.T) {
	root := fixture(t)
	stdout := mustGenerate(t, root)
	llms := readFile(t, root, "site/llms.txt")
	want := fmt.Sprintf("site/llms.txt: %d bytes\n", len(llms))
	texts := map[string]string{}
	for _, name := range outputs {
		text := readFile(t, root, "site/"+name)
		texts[name] = text
		want += fmt.Sprintf("site/%s: %d bytes, about %d tokens", name, len(text), len(text)/4)
		for _, f := range sectionFiles {
			if f.name == name {
				want += "; " + countPages(len(f.pages))
			}
		}
		want += "\n"
		golden(t, name, text)
		if !strings.Contains(llms, "("+base+name+")") {
			t.Errorf("llms.txt does not link %s:\n%s", name, llms)
		}
	}
	want += "7 pages in 3 section files\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	for name, text := range texts {
		if strings.Contains(text, "\n## Rules\n") != (name == "llms-full-guide.txt") {
			t.Errorf("the template \"full\" is not in llms-full-guide.txt alone: %s:\n%s", name, text)
		}
	}
	if strings.Contains(llms, "## Rules") {
		t.Errorf("the template \"full\" is in llms.txt:\n%s", llms)
	}
	golden(t, "llms.txt", llms)
	for _, f := range sectionFiles {
		text := texts[f.name]
		line := fmt.Sprintf("): %s, %d bytes, about %d tokens.", countPages(len(f.pages)), len(text), len(text)/4)
		if !strings.Contains(llms, f.name+line) || !strings.Contains(texts["llms-full.txt"], f.name+line) {
			t.Errorf("llms.txt or llms-full.txt does not give the size of %s", f.name)
		}
	}
	if !strings.Contains(llms, "to build (RFC 6238) the site, with **bold**.\n") {
		t.Errorf("llms.txt: the links of a description are not reduced to their text:\n%s", llms)
	}
	for _, s := range []string{"rfc-editor.org", "](section/", "!["} {
		if strings.Contains(llms, s) {
			t.Errorf("llms.txt: a description keeps a link or an image (%q):\n%s", s, llms)
		}
	}
	if got := mustGenerate(t, root); got != stdout || readFile(t, root, "site/llms.txt") != llms {
		t.Errorf("a second run differs: %q", got)
	}
}

func TestMarkdownVersionsAndSpecs(t *testing.T) {
	root := fixture(t)
	mustGenerate(t, root)
	for _, p := range navPages {
		if got, want := readFile(t, root, "site/"+p), readFile(t, root, "docs/"+p); got != want {
			t.Errorf("site/%s is not its source byte for byte:\n%s", p, got)
		}
	}
	for _, s := range []string{"api/openapi.yaml", "api/bridge.yaml"} {
		if readFile(t, root, "site/"+s) != readFile(t, root, s) {
			t.Errorf("site/%s is not a copy of %s", s, s)
		}
	}
	if exists(t, root, "site/llms.txt.tmpl") {
		t.Error("the template is copied into the site")
	}
}

var sourceRe = regexp.MustCompile(`(?m)^<!-- Source: (.*) -->$`)

func TestFullText(t *testing.T) {
	root := fixture(t)
	mustGenerate(t, root)
	var all []string
	var full string
	for _, f := range sectionFiles {
		text := readFile(t, root, "site/"+f.name)
		full += text
		var sources []string
		for _, m := range sourceRe.FindAllStringSubmatch(text, -1) {
			sources = append(sources, strings.TrimPrefix(m[1], base))
		}
		if !slices.Equal(sources, f.pages) {
			t.Errorf("%s: the Source lines name %v, want %v in the order of the navigation", f.name, sources, f.pages)
		}
		all = append(all, sources...)
	}
	slices.Sort(all)
	want := slices.Clone(navPages)
	slices.Sort(want)
	if !slices.Equal(all, want) {
		t.Errorf("the files of the full text hold %v, want every page of the navigation once %v", all, want)
	}
	section := readFile(t, root, "site/llms-full-section.txt")
	header := "# Muster documentation, version latest: Section\n\n" +
		"This file holds the full text of the section Section. The index of the documentation is " + base +
		"llms.txt, and " + base + "llms-full.txt lists the files of the full text; the rules for AI agents are in " +
		base + "llms-full-guide.txt.\n\n<!-- Source: " + base + "section/index.md -->\n"
	if !strings.HasPrefix(section, header) {
		t.Errorf("llms-full-section.txt does not start with its title and the line naming the index:\n%s", section)
	}
	if home := readFile(t, root, "site/llms-full-home.txt"); !strings.HasPrefix(home,
		"# Muster documentation, version latest: Home, Guide, After\n\nThis file holds the full text of the pages outside any section.") {
		t.Errorf("llms-full-home.txt is not titled after its pages:\n%s", home)
	}
	guide := readFile(t, root, "site/llms-full-guide.txt")
	if !strings.HasPrefix(guide, "# Rules and scenarios for AI agents, Muster documentation version latest\n\n") ||
		strings.Contains(guide, "<!-- Source: ") {
		t.Errorf("llms-full-guide.txt is not the guide alone:\n%s", guide)
	}
	for _, want := range []string{
		"[site](" + base + `section/index.md "The section")`,
		"[deep](" + base + "section/nested/deep.md#deep-part)",
		"[here](" + base + "guide.md#links)",
		"[after](" + base + "after.md?view=1#top)",
		"![logo](" + base + "assets/logo.png)",
		"[idx]: " + base + "index.md\n",
		"[after\n](" + base + "after.md)",
		"[after](<" + base + "after.md>)",
		"[section](" + base + "section/)",
		"[up](" + base + "after.md)",
		"[API reference](" + base + "reference/api/spec/index.html)",
		"[RFC 6238](https://www.rfc-editor.org/rfc/rfc6238)",
		"[external](https://example.org/a.md)",
		"[mail](mailto:someone@example.org)",
		"[mention](muster-mention:ref)",
		"`[kept](kept.md)`",
		"```md\n[in a fence](fenced.md)\n```\n",
		"```\n\n<!-- Source: " + base + "after.md -->\nAfter the section",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("the full text lacks %q", want)
		}
	}
	if !strings.HasSuffix(full, "`api/openapi.yaml`. More.\n") {
		t.Error("llms-full-reference.txt does not end its last page with one newline")
	}
}

func TestNote(t *testing.T) {
	long := strings.Repeat("word ", 30)
	tests := map[string]string{
		"":                               "",
		"short":                          "short",
		strings.Repeat("a", 100):         strings.Repeat("a", 100),
		long:                             strings.TrimSpace(strings.Repeat("word ", 20)) + "…",
		strings.Repeat("a", 99) + " b c": strings.Repeat("a", 99) + "…",
		strings.Repeat("a", 90) + " — bbbbbbbbbbbbbb": strings.Repeat("a", 90) + "…",
		strings.Repeat("é", 150):                      strings.Repeat("é", 100) + "…",
		strings.Repeat("a, ", 40):                     strings.Repeat("a, ", 32) + "a…",
	}
	for in, want := range tests {
		if got := note(in); got != want {
			t.Errorf("note(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTitleAndDescription(t *testing.T) {
	tests := map[string]struct{ src, title, description string }{
		"a heading and a paragraph": {"# T\n\nOne. Two.\n", "T", "One."},
		"no heading":                {"First line\nsecond line. Third.\n", "", "First line second line."},
		"comments": {
			"<!-- Code generated. DO NOT EDIT. -->\n\n# T\n\n<!--\nmulti. line\n-->\nText. More.\n", "T", "Text."},
		"an admonition":            {"# T\n\n!!! note \"N. x\"\n    Body. x\n\n    More body.\n\nText.\n", "T", "Text."},
		"a collapsible admonition": {"# T\n???+ tip\n    Body. x\nText. x\n\nNext. x\n", "T", "Next."},
		"an indented tilde fence":  {"# T\n\n   ~~~\n   code. x\n\n   more\n   ~~~\n\nText.\n", "T", "Text."},
		"a list with a continuation and a paragraph": {
			"# T\n\n- item. x\n  more\n\n  item paragraph. x\n\n1. one. x\n\nText.\n", "T", "Text."},
		"other blocks": {"# T\n\n| a. | b |\n|---|---|\n\n> quote. x\n\n<div>html. x</div>\n\n![img. x](a.png)\n\n" +
			"[r]: https://example.org/a.b\n\n    indented code. x\n\n---\n\nText.\n", "T", "Text."},
		"headings":                 {"# T\n\n## Sub\n\n### Sub sub\n\nText.\n", "T", "Text."},
		"a paragraph after a list": {"# T\n\n- item\nlazy. x\n\nText.\n", "T", "Text."},
		"code spans, links and images": {
			"# T\n\nUse `a. b` and [c](e.md#f.g) or [h][i] and ![j](k.png) with **bold**? Yes.\n",
			"T", "Use `a. b` and c or h and with **bold**?"},
		"a link in parentheses": {"# T\n\nTOTP ([RFC 6238](https://www.rfc-editor.org/rfc/rfc6238)). Next.\n", "T",
			"TOTP (RFC 6238)."},
		"a shortcut reference stays":  {"# T\n\nA [redacted] value. x\n", "T", "A [redacted] value."},
		"punctuation inside a word":   {"# T\n\nWow!great v1.2 and (`x.y`). End\n", "T", "Wow!great v1.2 and (`x.y`)."},
		"an escaped dot":              {"# T\n\nA \\. b. c\n", "T", "A \\. b."},
		"an unclosed backtick":        {"# T\n\nA ` tick. Rest\n", "T", "A ` tick."},
		"no sentence end":             {"# T\n\nNo end at all\nacross lines\n", "T", "No end at all across lines"},
		"a list ends a paragraph":     {"# T\n\nText without an end\n- item\n", "T", "Text without an end"},
		"a rule ends a paragraph":     {"# T\n\nText without an end\n***\n", "T", "Text without an end"},
		"no paragraph":                {"# T\n\n- only a list\n\n```\ncode\n```\n", "T", ""},
		"a heading in a fence":        {"```\n# not\n```\n\n# Real\n\nText.\n", "Real", "Text."},
		"closing hashes and an attr":  {"# Title ## {#id}\n\nText.\n", "Title", "Text."},
		"a link in the heading":       {"# The [CLI](cli.md)\n", "The CLI", ""},
		"a second level heading only": {"## Not a title\n\nText.\n", "", "Text."},
		"a backtick run with a backtick after it is no fence": {
			"# T\n\n```x``` is code. More\n", "T", "```x``` is code."},
	}
	for name, tc := range tests {
		title, description := titleAndDescription(tc.src)
		if title != tc.title || description != tc.description {
			t.Errorf("%s: titleAndDescription = %q, %q, want %q, %q", name, title, description, tc.title, tc.description)
		}
	}
}

func TestResolveLink(t *testing.T) {
	tests := []struct{ page, target, want string }{
		{"operations/monitoring.md", "../reference/metrics.md#x", base + "reference/metrics.md#x"},
		{"operations/monitoring.md", "#frag", base + "operations/monitoring.md#frag"},
		{"operations/monitoring.md", "?q=1", base + "operations/monitoring.md?q=1"},
		{"operations/monitoring.md", "../", base},
		{"operations/monitoring.md", "./runbooks/", base + "operations/runbooks/"},
		{"index.md", "https://example.org/x.md", "https://example.org/x.md"},
		{"index.md", "mailto:a@example.org", "mailto:a@example.org"},
		{"index.md", "/absolute.md", "/absolute.md"},
		{"index.md", "", ""},
	}
	for _, tc := range tests {
		if got, ok := resolveLink(base, tc.page, tc.target); !ok || got != tc.want {
			t.Errorf("resolveLink(%s, %s) = %q, %v, want %q", tc.page, tc.target, got, ok, tc.want)
		}
	}
	if got, ok := resolveLink(base, "operations/monitoring.md", "../../outside.md"); ok {
		t.Errorf("a link out of docs/ resolves to %q", got)
	}
}

func TestTrimURL(t *testing.T) {
	for in, want := range map[string]string{
		base + "a.md).":                   base + "a.md",
		base + "a.md**,":                  base + "a.md",
		"https://example.org/wiki/A_(b))": "https://example.org/wiki/A_(b)",
		base + "llms.txt.":                base + "llms.txt",
		base + "x/]:;!?>":                 base + "x/",
		base + "a.md_":                    base + "a.md",
		base + "a.md__*.":                 base + "a.md",
		"https://example.org/a_b":         "https://example.org/a_b",
	} {
		if got := trimURL(in); got != want {
			t.Errorf("trimURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestURLs(t *testing.T) {
	got := urls("[" + base + "a.md](" + base + "a.md), [https://example.org/x](https://example.org/y) and _" +
		base + "b.md_. ![A screen](" + base + "assets/screenshots/c/01-d.png){ .screenshot }")
	want := []string{base + "a.md", "https://example.org/x", "https://example.org/y", base + "b.md",
		base + "assets/screenshots/c/01-d.png"}
	if !slices.Equal(got, want) {
		t.Errorf("urls = %q, want %q", got, want)
	}
}

func TestCodeSpanAfter(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{"`a` b", 3},
		{"``a ` b`` c", 9},
		{"`a\nb` c", 5},
		{"`a\n\nb` c", 1},
		{"`a\n \t\nb` c", 1},
		{"``a` b", 2},
	}
	for _, tc := range tests {
		if got := codeSpanAfter(tc.s, 0); got != tc.want {
			t.Errorf("codeSpanAfter(%q) = %d, want %d", tc.s, got, tc.want)
		}
	}
}

// TestAbsoluteLinks covers the forms of links and reference definitions that llms-full.txt makes absolute, and the
// code it leaves as it is.
func TestAbsoluteLinks(t *testing.T) {
	const dir = base + "dir/"
	tests := map[string]struct{ src, want string }{
		"a backtick that no run closes before a blank line": {
			"A ` tick\n\n[x](y.md) and `[z](z.md)`\n", "A ` tick\n\n[x](" + dir + "y.md) and `[z](z.md)`\n"},
		"a code span over a line ending": {"`a\n[x](y.md)` b\n", "`a\n[x](y.md)` b\n"},
		"spaces after the parenthesis": {
			"[x]( y.md ) and ![i](\t<i.png> \"t\")\n", "[x]( " + dir + "y.md ) and ![i](\t<" + dir + "i.png> \"t\")\n"},
		"a line ending after the parenthesis":           {"[x](\n  y.md)\n", "[x](\n  " + dir + "y.md)\n"},
		"a blank line after the parenthesis is no link": {"[x](\n\ny.md)\n", "[x](\n\ny.md)\n"},
		"a definition under a list item":                {"- item\n\n    [r]: y.md\n", "- item\n\n    [r]: " + dir + "y.md\n"},
		"a definition under a nested list item": {
			"- a\n  - b\n\n      [r]: y.md\n", "- a\n  - b\n\n      [r]: " + dir + "y.md\n"},
		"a definition in an admonition": {
			"!!! note\n    Text.\n\n    [r]: <y.md> \"t\"\n", "!!! note\n    Text.\n\n    [r]: <" + dir + "y.md> \"t\"\n"},
		"a definition after a fence in an admonition": {
			"!!! note\n    ```sh\n    x\n    ```\n\n    [r]: y.md\n",
			"!!! note\n    ```sh\n    x\n    ```\n\n    [r]: " + dir + "y.md\n"},
		"a definition after a list marker":    {"1. [r]: y.md\n", "1. [r]: " + dir + "y.md\n"},
		"a definition in a block quote":       {"> - [r]: y.md\n", "> - [r]: " + dir + "y.md\n"},
		"a definition in indented code stays": {"Text.\n\n    [r]: y.md\n", "Text.\n\n    [r]: y.md\n"},
		"indented code after a list":          {"- item\n\nText.\n\n    [r]: y.md\n", "- item\n\nText.\n\n    [r]: y.md\n"},
		"a footnote is no definition":         {"[^1]: y.md\n", "[^1]: y.md\n"},
		"a backtick run with a backtick after it is no fence": {
			"```x``` is [x](y.md)\n", "```x``` is [x](" + dir + "y.md)\n"},
		"a fence stays": {"```md\n[x](y.md)\n```\n[r]: y.md\n", "```md\n[x](y.md)\n```\n[r]: " + dir + "y.md\n"},
	}
	for name, tc := range tests {
		g := &generator{base: base, docsDir: "docs", reported: map[string]bool{}}
		got := g.absoluteLinks(&page{path: "dir/p.md", source: []byte(tc.src)})
		if got != tc.want || len(g.problems) > 0 {
			t.Errorf("%s: absoluteLinks =\n%q, problems %q, want\n%q", name, got, g.problems, tc.want)
		}
	}
}

// TestLinkTargets covers the scan that checks the rewritten text: it finds every destination outside fenced code and
// code spans, a reference definition at any indentation included.
func TestLinkTargets(t *testing.T) {
	src := "[a](a.md) [b]( b.md ) [c](\nc.md) ![d](<d e.png>) [f](https://x/f.md) [g](wiki/A_(g)) `[h](h.md)` \\](i.md)\n" +
		"` open\n\n[n](n.md)\n    [j]: j.md\n> - [k]: <k.md>\n[^1]: footnote\n```\n[l](l.md)\n```\n[m](\n\nm.md)\n"
	want := []string{"a.md", "b.md", "c.md", "d e.png", "https://x/f.md", "wiki/A_(g)", "n.md", "", "j.md", "k.md"}
	if got := linkTargets(src); !slices.Equal(got, want) {
		t.Errorf("linkTargets = %q, want %q", got, want)
	}
}

func TestFailures(t *testing.T) {
	const tmpl = "docs/llms.txt.tmpl"
	tests := map[string]struct {
		change func(t *testing.T, root string)
		want   []string
		// wantPrefix holds lines given by their start, for messages of the standard library.
		wantPrefix []string
	}{
		"a page without a title": {
			change: func(t *testing.T, root string) { writeFile(t, root, "docs/guide.md", "No heading here.\n") },
			want:   []string{"docs/guide.md: no title: neither the nav of mkdocs.yml nor a # heading gives one"},
		},
		"a page of the navigation that does not exist": {
			change: func(t *testing.T, root string) { removeFile(t, root, "docs/after.md") },
			want:   []string{"mkdocs.yml: the nav names docs/after.md, which does not exist"},
		},
		"a page twice in the navigation": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, "mkdocs.yml", "  - After: after.md\n", "  - After: after.md\n  - guide.md\n")
			},
			want: []string{"mkdocs.yml: the nav names docs/guide.md twice"},
		},
		"an external URL in the navigation": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, "mkdocs.yml", "  - After: after.md\n",
					"  - After: after.md\n  - GitHub: https://github.com/muster-io/muster\n")
			},
			want: []string{
				"mkdocs.yml: line 26: the nav entry https://github.com/muster-io/muster is no Markdown page under docs/"},
		},
		"a navigation entry that is no page and no section": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, "mkdocs.yml", "  - After: after.md\n", "  - After: after.md\n  - Odd: {a: b}\n")
			},
			want: []string{"mkdocs.yml: line 26: a nav entry is a page, a titled page or a titled section"},
		},
		"a site that is not built": {
			change:     func(t *testing.T, root string) { removeFile(t, root, "site/index.html") },
			wantPrefix: []string{"site/index.html: "},
		},
		"a missing specification": {
			change: func(t *testing.T, root string) { removeFile(t, root, "api/openapi.yaml") },
			want:   []string{"api/openapi.yaml: no such file or directory"},
		},
		"a link that leaves docs": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, "docs/guide.md", "## Links\n", "## Links\n\nSee [outside](../outside.md).\n")
			},
			want: []string{"docs/guide.md: the link ../outside.md leaves docs/"},
		},
		"a definition that stays relative": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, "docs/guide.md", "## Links\n", "## Links\n\n    [code]: code.md\n")
			},
			want: []string{"docs/guide.md: the link code.md is not made absolute"},
		},
		"relative links in llms.txt": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, "## Optional", "See [guide](guide.md), [top](#top) and `[code](code.md)`.\n\n## Optional")
			},
			want: []string{
				"site/llms.txt: the link guide.md is not absolute",
				"site/llms.txt: the link #top is not absolute",
			},
		},
		"a relative link in the full template": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, "## Rules\n", "## Rules\n\nSee [guide](guide.md) and https://example.org/x.\n")
			},
			want: []string{
				"site/llms-full-guide.txt: the link guide.md is not absolute",
				"site/llms-full-guide.txt: https://example.org/x leaves the site and is no repository under " +
					"https://github.com/muster-io/",
			},
		},
		"a missing page named twice": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, "{{map}}", `{{page "section/nope.md"}} {{page "section/nope.md#x"}}`+"\n\n{{map}}")
			},
			want: []string{"docs/llms.txt.tmpl: section/nope.md is not a page of the navigation"},
		},
		"a broken link in llms-full.txt": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, "docs/guide.md", "## Links\n", "## Links\n\nSee [missing](missing.md#x).\n")
			},
			want: []string{"site/llms-full-home.txt: " + base + "missing.md#x does not resolve under site/"},
		},
		"a section named like the guide": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, "mkdocs.yml", "  - Section:\n", "  - Guide:\n")
			},
			want: []string{
				`mkdocs.yml: the section "Guide" would write site/llms-full-guide.txt, the file of the guide for AI agents; ` +
					"rename one of them"},
		},
		"two sections with one file": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, "mkdocs.yml", "  - Reference:\n", "  - Sec-tion: []\n  - Reference:\n")
				replaceIn(t, root, "mkdocs.yml", "  - Section:\n", "  - Sec tion:\n")
			},
			want: []string{`mkdocs.yml: the section "Sec-tion" would write site/llms-full-sec-tion.txt, ` +
				"the file of the section Sec tion; rename one of them"},
		},
		"a section without a letter or a digit": {
			change: func(t *testing.T, root string) { replaceIn(t, root, "mkdocs.yml", "  - Section:\n", "  - \"—\":\n") },
			want: []string{
				`mkdocs.yml: the section "—" has no letter or digit to name its file llms-full-<name>.txt`},
		},
		"a file of the full text that llms.txt does not link": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, "{{range .Sections}}", `{{range .Sections}}{{if ne .Name "llms-full-section.txt"}}`)
				replaceIn(t, root, tmpl, "tokens.\n{{end -}}", "tokens.\n{{end}}{{end -}}")
			},
			want: []string{"site/llms.txt: does not link " + base +
				"llms-full-section.txt; it links every file of the full text"},
		},
		"a broken link in llms.txt": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, "## Optional", "See {{.Base}}missing.md.\n\n## Optional")
			},
			want: []string{"site/llms.txt: " + base + "missing.md does not resolve under site/"},
		},
		"links of llms.txt out of the site": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, "## Optional",
					"See [x](https://example.org/x), https://github.com/other/repo, https://muster-io.github.io/muster/llms.txt "+
						"and `https://api.telegram.org` in code.\n\n## Optional")
			},
			want: []string{
				"site/llms.txt: https://example.org/x leaves the site and is no repository under https://github.com/muster-io/",
				"site/llms.txt: https://github.com/other/repo leaves the site and is no repository under " +
					"https://github.com/muster-io/",
				"site/llms.txt: https://muster-io.github.io/muster/llms.txt leaves the site and is no repository under " +
					"https://github.com/muster-io/",
			},
		},
		"a phrase its source no longer has": {
			change: func(t *testing.T, root string) { replaceIn(t, root, tmpl, "at most 3 seconds", "at most 2 seconds") },
			want:   []string{`docs/llms.txt.tmpl: api/bridge.yaml does not say "at most 2 seconds"`},
		},
		"a quote of a missing file": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, `quote "docs/index.md"`, `quote "docs/missing.md"`)
			},
			want: []string{"docs/llms.txt.tmpl: docs/missing.md: no such file or directory"},
		},
		"a page out of the navigation": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, `page "section/nested/deep.md#deep-part"`, `page "section/nope.md"`)
			},
			want: []string{"docs/llms.txt.tmpl: section/nope.md is not a page of the navigation"},
		},
		"an anchor the built page lacks": {
			change: func(t *testing.T, root string) { replaceIn(t, root, tmpl, "#deep-part", "#nope") },
			want: []string{
				`docs/llms.txt.tmpl: section/nested/deep.md#nope: site/section/nested/deep/index.html has no id "nope"`},
		},
		"an anchor that is only a data-id": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, `page "section/nested/deep.md#deep-part"`, `page "after.md#top"`)
			},
			want: []string{`docs/llms.txt.tmpl: after.md#top: site/after/index.html has no id "top"`},
		},
		"an unknown specification": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, `spec "api/bridge.yaml"`, `spec "api/other.yaml"`)
			},
			want: []string{
				"docs/llms.txt.tmpl: api/other.yaml is no specification: spec takes api/openapi.yaml or api/bridge.yaml"},
		},
		"a missing setting": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, `setting "plain.value"`, `setting "bridge.nope"`)
			},
			want: []string{"docs/llms.txt.tmpl: design/prd/l1/defaults.md has no setting bridge.nope"},
		},
		"a missing enum": {
			change: func(t *testing.T, root string) { replaceIn(t, root, tmpl, `"ErrorClass"`, `"Nope"`) },
			want:   []string{"docs/llms.txt.tmpl: api/bridge.yaml has no enum components.schemas.Nope"},
		},
		"every failed function of a run": {
			change: func(t *testing.T, root string) {
				replaceIn(t, root, tmpl, "at most 3 seconds", "at most 2 seconds")
				replaceIn(t, root, tmpl, `setting "plain.value"`, `setting "bridge.nope"`)
				replaceIn(t, root, tmpl, `"ErrorClass"`, `"Nope"`)
			},
			want: []string{
				"docs/llms.txt.tmpl: api/bridge.yaml has no enum components.schemas.Nope",
				`docs/llms.txt.tmpl: api/bridge.yaml does not say "at most 2 seconds"`,
				"docs/llms.txt.tmpl: design/prd/l1/defaults.md has no setting bridge.nope",
			},
		},
		"map called twice": {
			change: func(t *testing.T, root string) { replaceIn(t, root, tmpl, "{{map}}", "{{map}}\n{{map}}") },
			want:   []string{"docs/llms.txt.tmpl: calls map 2 times; it must call it once"},
		},
		"map never called": {
			change: func(t *testing.T, root string) { replaceIn(t, root, tmpl, "{{map}}", "") },
			want:   []string{"docs/llms.txt.tmpl: calls map 0 times; it must call it once"},
		},
		"a template that does not parse": {
			change: func(t *testing.T, root string) { replaceIn(t, root, tmpl, "## Optional", "## Optional {{nope}}") },
			want:   []string{`docs/llms.txt.tmpl:20: function "nope" not defined`},
		},
		"a field the data lacks": {
			change:     func(t *testing.T, root string) { replaceIn(t, root, tmpl, "{{.Version}}", "{{.Nope}}") },
			wantPrefix: []string{"docs/llms.txt.tmpl:14:"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := fixture(t)
			mustGenerate(t, root)
			tc.change(t, root)
			code, stdout, stderr := generateIn(t, root)
			if code != 1 || stdout != "" {
				t.Fatalf("run = %d, stdout %q, stderr:\n%s", code, stdout, stderr)
			}
			lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
			for _, want := range tc.want {
				if !slices.Contains(lines, want) {
					t.Errorf("stderr lacks the line\n%s\nin:\n%s", want, stderr)
				}
			}
			for _, want := range tc.wantPrefix {
				if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, want) }) {
					t.Errorf("stderr lacks a line starting %q:\n%s", want, stderr)
				}
			}
			if len(lines) != len(tc.want)+len(tc.wantPrefix) {
				t.Errorf("stderr has %d lines, want %d:\n%s", len(lines), len(tc.want)+len(tc.wantPrefix), stderr)
			}
			for _, l := range lines {
				if !regexp.MustCompile(`^(mkdocs\.yml|docs/|site/|api/)`).MatchString(l) {
					t.Errorf("the line %q does not start with the file it concerns", l)
				}
			}
			for _, f := range append([]string{"llms.txt"}, outputs...) {
				if exists(t, root, "site/"+f) {
					t.Errorf("site/%s of the earlier run outlives the failed run", f)
				}
			}
		})
	}
}

// TestFullTextLimit pads a page of the top-level pages and one of the section so that each of their files is just
// under 1 MiB, more than 1 MiB together, then the section's file to exactly 1 MiB: the limit holds per file.
func TestFullTextLimit(t *testing.T) {
	baseline := fixture(t)
	mustGenerate(t, baseline)
	home := len(readFile(t, baseline, "site/llms-full-home.txt"))
	section := len(readFile(t, baseline, "site/llms-full-section.txt"))
	for _, total := range []int{maxFullBytes - 1, maxFullBytes} {
		root := fixture(t)
		replaceIn(t, root, "docs/after.md", "More text.\n",
			"More text.\n"+strings.Repeat("x", maxFullBytes-1-home-1)+"\n")
		replaceIn(t, root, "docs/section/titled.md", "Yes.\n", "Yes.\n"+strings.Repeat("x", total-section-1)+"\n")
		code, stdout, stderr := generateIn(t, root)
		if total < maxFullBytes {
			for _, want := range []string{
				fmt.Sprintf("site/llms-full-home.txt: %d bytes, about %d tokens; 3 pages\n", total, total/4),
				fmt.Sprintf("site/llms-full-section.txt: %d bytes, about %d tokens; 3 pages\n", total, total/4),
			} {
				if code != 0 || !strings.Contains(stdout, want) {
					t.Errorf("%d bytes: run = %d, stdout %q lacks %q, stderr:\n%s", total, code, stdout, want, stderr)
				}
			}
			continue
		}
		want := "site/llms-full-section.txt: 1048576 bytes; it must be smaller than 1 MiB (1048576 bytes); " +
			"split the section Section of the nav of mkdocs.yml\n"
		if code != 1 || stderr != want {
			t.Errorf("%d bytes: run = %d, stderr %q, want %q", total, code, stderr, want)
		}
	}
	root := fixture(t)
	replaceIn(t, root, "docs/after.md", "More text.\n", "More text.\n"+strings.Repeat("x", maxFullBytes-home-1)+"\n")
	want := "site/llms-full-home.txt: 1048576 bytes; it must be smaller than 1 MiB (1048576 bytes); " +
		"move pages outside any section of the nav of mkdocs.yml into a section\n"
	if code, _, stderr := generateIn(t, root); code != 1 || stderr != want {
		t.Errorf("the top-level pages: run = %d, stderr %q, want %q", code, stderr, want)
	}
}

// TestNoGuide runs a template without the template "full": no llms-full-guide.txt, and no file of the full text names
// one.
func TestNoGuide(t *testing.T) {
	root := fixture(t)
	content := readFile(t, root, "docs/llms.txt.tmpl")
	start, end := strings.Index(content, "{{define"), strings.Index(content, "{{end -}}\n# Fixture")
	content = content[:start] + content[end+len("{{end -}}\n"):]
	content = strings.Replace(content, "- [Guide]({{.Guide.URL}}): {{.Guide.Bytes}} bytes, about {{.Guide.Tokens}} tokens; ", "- ", 1)
	writeFile(t, root, "docs/llms.txt.tmpl", content)
	stdout := mustGenerate(t, root)
	if exists(t, root, "site/llms-full-guide.txt") || strings.Contains(stdout, "guide") {
		t.Errorf("a template without \"full\" writes a guide: %q", stdout)
	}
	for _, name := range []string{"llms-full.txt", "llms-full-home.txt", "llms-full-section.txt"} {
		if text := readFile(t, root, "site/"+name); strings.Contains(text, "guide.txt") {
			t.Errorf("%s names a guide:\n%s", name, text)
		}
	}
}

// TestRenamedSection renames a section: the file of its old name does not outlive the run.
func TestRenamedSection(t *testing.T) {
	root := fixture(t)
	mustGenerate(t, root)
	replaceIn(t, root, "mkdocs.yml", "  - Section:\n", "  - Renamed section:\n")
	stdout := mustGenerate(t, root)
	if exists(t, root, "site/llms-full-section.txt") || !exists(t, root, "site/llms-full-renamed-section.txt") ||
		!strings.Contains(stdout, "site/llms-full-renamed-section.txt: ") {
		t.Errorf("the renamed section's file: stdout %q", stdout)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Concepts": "concepts", "Outgoing webhooks": "outgoing-webhooks", "Sign-in": "sign-in",
		" API & tokens (v2) ": "api-tokens-v2", "—": "", "Déjà vu": "d-j-vu",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLLMSLimit pads the template so that llms.txt is exactly 32 KiB, then a byte more.
func TestLLMSLimit(t *testing.T) {
	baseline := fixture(t)
	mustGenerate(t, baseline)
	size := len(readFile(t, baseline, "site/llms.txt"))
	for _, total := range []int{maxLLMSBytes, maxLLMSBytes + 1} {
		root := fixture(t)
		replaceIn(t, root, "docs/llms.txt.tmpl", "## Optional", strings.Repeat("x", total-size-1)+"\n## Optional")
		code, stdout, stderr := generateIn(t, root)
		if total == maxLLMSBytes {
			if code != 0 || !strings.HasPrefix(stdout, "site/llms.txt: 32768 bytes\n") {
				t.Errorf("%d bytes: run = %d, stdout %q, stderr:\n%s", total, code, stdout, stderr)
			}
			continue
		}
		want := "site/llms.txt: 32769 bytes; it must be at most 32 KiB (32768 bytes)\n"
		if code != 1 || stderr != want {
			t.Errorf("%d bytes: run = %d, stderr %q, want %q", total, code, stderr, want)
		}
	}
}

func TestVersion(t *testing.T) {
	root := fixture(t)
	mustGenerate(t, root, "-version", "0.4")
	llms, full := readFile(t, root, "site/llms.txt"), readFile(t, root, "site/llms-full-section.txt")
	for _, name := range append([]string{"llms.txt"}, outputs...) {
		text := readFile(t, root, "site/"+name)
		if strings.Contains(text, "/latest/") || !strings.Contains(text, "https://muster-io.github.io/muster/0.4/") {
			t.Errorf("%s does not link into 0.4/:\n%s", name, text)
		}
	}
	if want := "- [Section](https://muster-io.github.io/muster/0.4/llms-full-section.txt): "; !strings.Contains(llms, want) {
		t.Errorf("llms.txt lacks %q", want)
	}
	for _, want := range []string{
		"- [Home](https://muster-io.github.io/muster/0.4/index.md)",
	} {
		if !strings.Contains(llms, want) {
			t.Errorf("llms.txt lacks %q", want)
		}
	}
	guide := readFile(t, root, "site/llms-full-guide.txt")
	if want := "[the deep part](https://muster-io.github.io/muster/0.4/section/nested/deep.md#deep-part)"; !strings.Contains(guide, want) {
		t.Errorf("llms-full-guide.txt lacks %q", want)
	}
	if !strings.HasPrefix(full, "# Muster documentation, version 0.4: Section\n") {
		t.Errorf("llms-full-section.txt does not name version 0.4:\n%s", full)
	}

	for name, args := range map[string][]string{
		"a word":          {"-version", "foo"},
		"a leading zero":  {"-version", "0.04"},
		"a v prefix":      {"-version", "v0.4"},
		"a patch":         {"-version", "0.4.1"},
		"an empty value":  {"-version", ""},
		"an unknown flag": {"-unknown"},
		"an argument":     {"extra"},
	} {
		root := fixture(t)
		code, _, stderr := generateIn(t, root, args...)
		if code != 2 || stderr == "" {
			t.Errorf("%s: run = %d, stderr %q", name, code, stderr)
		}
		if args[0] == "-version" && !strings.Contains(stderr, "is not latest or <major>.<minor>") {
			t.Errorf("%s: stderr does not name the version: %q", name, stderr)
		}
		if exists(t, root, "site/llms.txt") || exists(t, root, "site/index.md") {
			t.Errorf("%s: a refused run wrote into the site", name)
		}
	}
}
