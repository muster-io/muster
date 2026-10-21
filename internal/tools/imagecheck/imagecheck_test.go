// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const config = "exclude_docs: |\n  brand/README.md\n  requirements.*\n  overrides/\n  llms.txt.tmpl\n\nnav:\n  - index.md\n"

// site writes the files of a documentation directory and its configuration into a temporary directory and returns
// both paths.
func site(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "docs")
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, "mkdocs.yml")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, cfg
}

func runOn(t *testing.T, root, cfg string) (int, []string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run([]string{"-config", cfg, root}, &out, &errOut)
	if errOut.Len() > 0 {
		t.Fatalf("stderr: %s", errOut.String())
	}
	return code, strings.Split(strings.TrimSpace(out.String()), "\n")
}

// TestFailures covers each failure of C-21.AC-6 and its message, naming the page, the line and the image: a missing
// file, Markdown or <img>; a screenshot no page references; an image without alternative text; and an image from
// another site, http:, https: or //.
func TestFailures(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n"
	root, cfg := site(t, map[string]string{
		"index.md": "# Home\n\n![The list](assets/screenshots/index/01-list.png){ .screenshot }\n" +
			"![](assets/screenshots/index/02-page.png)\n" +
			"![A missing one](assets/screenshots/index/03-missing.png)\n" +
			"<img src=\"assets/logo.svg\" alt=\"\">\n" +
			"<img alt=\"Gone\" src='assets/gone.svg'>\n",
		"messengers/telegram.md": "# Telegram\n\nText ![Remote](https://example.org/x.png) and " +
			"![Plain](http://example.org/y.png).\n\n![Protocol-relative](//cdn.example.org/z.png)\n" +
			"<img src=\"https://example.org/w.png\" alt=\"A remote tag\">\n" +
			"![Up a level](../assets/screenshots/messengers/telegram/01-new.png \"title\")\n" +
			"![Missing](../assets/screenshots/messengers/telegram/01-missing.png)\n",
		"assets/screenshots/index/01-list.png":                 png,
		"assets/screenshots/index/02-page.png":                 png,
		"assets/screenshots/messengers/telegram/01-new.png":    png,
		"assets/screenshots/messengers/telegram/99-orphan.png": png,
		"assets/screenshots/first-alert/01-orphan.png":         png,
		"assets/logo.svg": "<svg/>",
	})
	code, lines := runOn(t, root, cfg)
	want := []string{
		root + "/index.md:4: assets/screenshots/index/02-page.png has no alternative text",
		root + "/index.md:5: assets/screenshots/index/03-missing.png does not exist",
		root + "/index.md:6: assets/logo.svg has no alternative text",
		root + "/index.md:7: assets/gone.svg does not exist",
		root + "/messengers/telegram.md:3: https://example.org/x.png is on another site; keep the image under the " +
			"documentation",
		root + "/messengers/telegram.md:3: http://example.org/y.png is on another site; keep the image under the " +
			"documentation",
		root + "/messengers/telegram.md:5: //cdn.example.org/z.png is on another site; keep the image under the " +
			"documentation",
		root + "/messengers/telegram.md:6: https://example.org/w.png is on another site; keep the image under the " +
			"documentation",
		root + "/messengers/telegram.md:8: ../assets/screenshots/messengers/telegram/01-missing.png does not exist",
		root + "/assets/screenshots/first-alert/01-orphan.png is referenced by no page",
		root + "/assets/screenshots/messengers/telegram/99-orphan.png is referenced by no page",
	}
	if code != 1 || len(lines) != len(want)+1 || !slices.Equal(lines[:len(want)], want) {
		t.Errorf("exit %d, output:\n%s\nwant:\n%s", code, strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// TestPasses: a site whose images all exist, have alternative text and are referenced passes; images in fenced code
// blocks and inline code, the pages that exclude_docs leaves out, data URIs, titles, attribute lists, query strings,
// fragments and escaped names are understood; other files under assets/screenshots/ than PNGs are not required to be
// referenced.
func TestPasses(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n"
	root, cfg := site(t, map[string]string{
		"index.md": "# Home\n\n```markdown\n![](missing.png)\n```\n\nWrite `![alt](path.png)` for an image.\n" +
			"~~~\n<img src=\"https://example.org/a.png\">\n~~~\n" +
			"![A shot](assets/screenshots/index/01-a%20b.png?v=1#top){ .screenshot }\n" +
			"![An inline image](data:image/png;base64,iVBORw0KGgo=)\n" +
			"<img src=\"/assets/screenshots/index/02-abs.png\" alt=\"From the root\">\n",
		"guide/page.md":                       "![Up](../assets/screenshots/index/03-up.png 'title')\n",
		"brand/README.md":                     "![](nothing.png)\n",
		"overrides/main.md":                   "![](nothing.png)\n",
		"requirements.md":                     "![](nothing.png)\n",
		"assets/screenshots/index/01-a b.png": png,
		"assets/screenshots/index/02-abs.png": png,
		"assets/screenshots/index/03-up.png":  png,
		"assets/screenshots/index/notes.txt":  "not an image",
	})
	if code, lines := runOn(t, root, cfg); code != 0 || len(lines) != 1 ||
		lines[0] != "4 images on 2 pages: every one exists, has alternative text and comes from the site, and every "+
			"screenshot is referenced" {
		t.Errorf("exit %d, output:\n%s", code, strings.Join(lines, "\n"))
	}
}

// TestNoScreenshots: a site without assets/screenshots/ passes, and a missing configuration or directory is an error.
func TestNoScreenshots(t *testing.T) {
	root, cfg := site(t, map[string]string{"index.md": "# Home\n"})
	if code, _ := runOn(t, root, cfg); code != 0 {
		t.Errorf("exit %d", code)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"-config", filepath.Join(t.TempDir(), "none.yml"), root}, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "imagecheck:") {
		t.Errorf("a missing configuration: exit %d, %s", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"-config", cfg, filepath.Join(root, "none")}, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "imagecheck:") {
		t.Errorf("a missing directory: exit %d, %s", code, errOut.String())
	}
	if code := run([]string{"-bad"}, &out, &errOut); code != 2 {
		t.Errorf("a bad flag: exit %d", code)
	}
}

// TestExcluded is the matching of exclude_docs patterns.
func TestExcluded(t *testing.T) {
	patterns := []string{"brand/README.md", "requirements.*", "overrides/", "llms.txt.tmpl"}
	for name, want := range map[string]bool{
		"brand/README.md":       true,
		"README.md":             false,
		"other/brand/README.md": false,
		"requirements.md":       true,
		"sub/requirements.in":   true,
		"overrides/main.md":     true,
		"a/overrides/x.md":      true,
		"overrides.md":          false,
		"index.md":              false,
		"llms.txt.tmpl":         true,
	} {
		if got := isExcluded(name, patterns); got != want {
			t.Errorf("isExcluded(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestCommittedSite: the documentation of the repository passes.
func TestCommittedSite(t *testing.T) {
	if code, lines := runOn(t, "../../../docs", "../../../mkdocs.yml"); code != 0 {
		t.Errorf("exit %d, output:\n%s", code, strings.Join(lines, "\n"))
	}
}
