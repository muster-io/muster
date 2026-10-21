// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	testDigest  = "sha256:53a40bce74b27f95452cc39c196bd7b8eb7308f838a6b5a024480181f7e00ee6"
	testImage   = "ghcr.io/muster-io/muster"
	repoURL     = "https://github.com/muster-io/muster"
	chartSource = `# SPDX-License-Identifier: AGPL-3.0-only
# Copyright The Muster Authors

apiVersion: v2
name: muster
version: 0.0.0
keywords:
  - alerting
annotations:
  artifacthub.io/category: monitoring-logging
  artifacthub.io/license: AGPL-3.0-only
  artifacthub.io/prerelease: "false"
`
	// The index of a release as goreleaser pushes it: one image per platform and their SBOM attestations.
	indexJSON = `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "digest": "` + testDigest + `",
  "size": 1609,
  "manifests": [
    {"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:aa", "size": 1,
     "platform": {"architecture": "arm64", "os": "linux"}},
    {"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:bb", "size": 1,
     "platform": {"architecture": "amd64", "os": "linux"}},
    {"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:cc", "size": 1,
     "annotations": {"vnd.docker.reference.digest": "sha256:aa", "vnd.docker.reference.type": "attestation-manifest"},
     "platform": {"architecture": "unknown", "os": "unknown"}},
    {"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:dd", "size": 1,
     "platform": {"architecture": "arm", "os": "linux", "variant": "v7"}}
  ]
}`
	changelogText = `<!-- Maintained by release-please from the conventional commits of merged pull requests. Do not edit by hand. -->

## [0.2.0](` + repoURL + `/compare/v0.1.0-rc.1...v0.2.0) (2026-11-30)


### Features

* **web:** a later feature ([#400](` + repoURL + `/issues/400)) ([1111111](` + repoURL + `/commit/1111111aaaa))

## [0.1.0-rc.1](` + repoURL + `/compare/v0.0.0...v0.1.0-rc.1) (2026-10-30)


### ⚠ BREAKING CHANGES

* **api:** rename the field ` + "`foo`" + ` to ` + "`bar`" + `
  in every response

### Features

* **slack:** add slack connections ([#331](` + repoURL + `/issues/331)) ([4b32468](` + repoURL + `/commit/4b32468abc))
* add the [Helm chart](https://example.org/chart) ([#10](` + repoURL + `/issues/10)) ([abcdef0](` + repoURL + `/commit/abcdef0)), closes [#5](` + repoURL + `/issues/5) [#6](` + repoURL + `/issues/6)

### Bug Fixes

* **security:** refuse unsigned callbacks ([#332](` + repoURL + `/issues/332)) ([562400b](` + repoURL + `/commit/562400b))
* **ingest:** fix CVE-2026-12345 in the parser ([#333](` + repoURL + `/issues/333))
* **slack:** neutralize mentions ([#334](` + repoURL + `/issues/334)) ([488ca84](` + repoURL + `/commit/488ca84))

### Performance Improvements

* faster grouping ([#7](` + repoURL + `/issues/7))

### Documentation

* **messengers:** add connect slack ([#335](` + repoURL + `/issues/335))

### Dependencies

* bump github.com/jackc/pgx/v5 to v5.9.0 ([#336](` + repoURL + `/issues/336))

## 0.0.1 (2026-01-01)

### Features

* the first entry
`
)

func link1(n, path string) link {
	return link{Name: n, URL: repoURL + path}
}

func TestParseChangelog(t *testing.T) {
	changes, found := parseChangelog([]byte(changelogText), "0.1.0-rc.1")
	if !found {
		t.Fatal("the section of 0.1.0-rc.1 was not found")
	}
	want := []change{
		{Kind: kindChanged, Description: "Breaking: api: rename the field `foo` to `bar` in every response"},
		{Kind: kindAdded, Description: "slack: add slack connections",
			Links: []link{link1("#331", "/issues/331"), link1("Commit 4b32468", "/commit/4b32468abc")}},
		{Kind: kindAdded, Description: "add the Helm chart", Links: []link{
			{Name: "Helm chart", URL: "https://example.org/chart"}, link1("#10", "/issues/10"), link1("Commit abcdef0", "/commit/abcdef0"),
			link1("#5", "/issues/5"), link1("#6", "/issues/6"),
		}},
		{Kind: kindSecurity, Description: "security: refuse unsigned callbacks",
			Links: []link{link1("#332", "/issues/332"), link1("Commit 562400b", "/commit/562400b")}},
		{Kind: kindSecurity, Description: "ingest: fix CVE-2026-12345 in the parser",
			Links: []link{link1("#333", "/issues/333")}},
		{Kind: kindFixed, Description: "slack: neutralize mentions",
			Links: []link{link1("#334", "/issues/334"), link1("Commit 488ca84", "/commit/488ca84")}},
		{Kind: kindChanged, Description: "faster grouping", Links: []link{link1("#7", "/issues/7")}},
		{Kind: kindChanged, Description: "bump github.com/jackc/pgx/v5 to v5.9.0",
			Links: []link{link1("#336", "/issues/336")}},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Errorf("changes:\n got %+v\nwant %+v", changes, want)
	}

	if changes, found := parseChangelog([]byte(changelogText), "0.0.1"); !found || len(changes) != 1 ||
		!reflect.DeepEqual(changes[0], change{Kind: kindAdded, Description: "the first entry"}) {
		t.Errorf("0.0.1, a heading without a link: %v %+v", found, changes)
	}
	if _, found := parseChangelog([]byte(changelogText), "0.1.0"); found {
		t.Error("0.1.0 must not match the section of 0.1.0-rc.1")
	}
}

func TestParseChangelogPatchHeadings(t *testing.T) {
	text := `## [0.2.0](x) (2026-12-01)

### Features

* the minor

### [0.1.2](x) (2026-11-20)

### Bug Fixes

* the second patch

### [0.1.1](x) (2026-11-10)

### Bug Fixes

* the first patch
`
	for version, want := range map[string]string{
		"0.2.0": "the minor", "0.1.2": "the second patch", "0.1.1": "the first patch",
	} {
		changes, found := parseChangelog([]byte(text), version)
		if !found || len(changes) != 1 || changes[0].Description != want {
			t.Errorf("%s: %v %+v, want only %q", version, found, changes, want)
		}
	}
}

func TestParseChangelogContinuationLines(t *testing.T) {
	text := `## [1.0.0](x) (2026-12-01)

### ⚠ BREAKING CHANGES

* **api:** the field foo is renamed
to bar, and clients must
  read bar.

### Bug Fixes

* a fix
`
	changes, _ := parseChangelog([]byte(text), "1.0.0")
	want := []change{
		{Kind: kindChanged, Description: "Breaking: api: the field foo is renamed to bar, and clients must read bar."},
		{Kind: kindFixed, Description: "a fix"},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Errorf("got %+v\nwant %+v", changes, want)
	}
}

func TestParseChangelogOwnTrailingLink(t *testing.T) {
	text := "## [1.0.0](x) (2026-12-01)\n\n### Features\n\n" +
		"* add the thing ([details](https://example.org/d)) ([#9](" + repoURL + "/issues/9))\n"
	changes, _ := parseChangelog([]byte(text), "1.0.0")
	want := []change{{Kind: kindAdded, Description: "add the thing (details)", Links: []link{
		{Name: "details", URL: "https://example.org/d"}, link1("#9", "/issues/9"),
	}}}
	if !reflect.DeepEqual(changes, want) {
		t.Errorf("got %+v\nwant %+v", changes, want)
	}
}

// release-please lists a feat! commit twice: under BREAKING CHANGES without links, and under Features with them.
func TestParseChangelogBreakingFeatureOnce(t *testing.T) {
	text := `## [1.0.0](x) (2026-12-01)

### ⚠ BREAKING CHANGES

* **api:** drop the v0 endpoints

### Features

* **api:** add the v1 endpoints ([#8](` + repoURL + `/issues/8)) ([1111111](` + repoURL + `/commit/1111111))
* **api:** drop the v0 endpoints ([#9](` + repoURL + `/issues/9)) ([abcdef0](` + repoURL + `/commit/abcdef0))
`
	changes, _ := parseChangelog([]byte(text), "1.0.0")
	want := []change{
		{Kind: kindChanged, Description: "Breaking: api: drop the v0 endpoints",
			Links: []link{link1("#9", "/issues/9"), link1("Commit abcdef0", "/commit/abcdef0")}},
		{Kind: kindAdded, Description: "api: add the v1 endpoints",
			Links: []link{link1("#8", "/issues/8"), link1("Commit 1111111", "/commit/1111111")}},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Errorf("got %+v\nwant %+v", changes, want)
	}
}

// The release-please shape of Renovate's security updates and of fixes that name an advisory.
func TestParseChangelogSecurity(t *testing.T) {
	text := `## [1.0.1](x) (2026-12-01)

### Bug Fixes

* **ingest:** reject oversized bodies, GHSA-abcd-2345-wxyz ([#11](` + repoURL + `/issues/11))
* **auth:** fix GO-2026-4321 in the session check ([#12](` + repoURL + `/issues/12))
* **web:** fix the layout ([#13](` + repoURL + `/issues/13))

### Dependencies

* **deps:** update module golang.org/x/net to v0.47.0 [security] ([#350](` + repoURL + `/issues/350)) ([abc1234](` + repoURL + `/commit/abc1234def))
* **deps:** update module golang.org/x/text to v0.39.0 \[SECURITY\] ([#351](` + repoURL + `/issues/351))
* **deps:** update module github.com/jackc/pgx/v5 to v5.9.0 ([#352](` + repoURL + `/issues/352))
`
	changes, _ := parseChangelog([]byte(text), "1.0.1")
	kinds := make([]string, 0, len(changes))
	for _, c := range changes {
		kinds = append(kinds, c.Kind)
	}
	want := []string{kindSecurity, kindSecurity, kindFixed, kindSecurity, kindSecurity, kindChanged}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("kinds %v, want %v: %+v", kinds, want, changes)
	}
	if got := changes[3]; got.Description != "deps: update module golang.org/x/net to v0.47.0 [security]" ||
		!reflect.DeepEqual(got.Links, []link{link1("#350", "/issues/350"), link1("Commit abc1234", "/commit/abc1234def")}) {
		t.Errorf("the [security] entry: %+v", got)
	}

	s := newSetup(t, text)
	_, _, _, chart, _ := s.run(t, "v1.0.1")
	if chart["annotations"].(map[string]any)[annotationSecurity] != "true" {
		t.Errorf("containsSecurityUpdates is not true: %#v", chart["annotations"])
	}
}

// Lines inside an entry that start with # are its text: a "## " line of a breaking-change footer does not end the
// section, and "#123 …" is not a heading.
func TestParseChangelogHashLinesInEntries(t *testing.T) {
	text := `## [1.0.0](x) (2026-12-01)

### ⚠ BREAKING CHANGES

* **api:** the v0 endpoints are gone
## Migration
use the v1 endpoints
#123 tracks the removal

### Features

* a feature
`
	changes, _ := parseChangelog([]byte(text), "1.0.0")
	want := []change{
		{Kind: kindChanged, Description: "Breaking: api: the v0 endpoints are gone ## Migration use the v1 endpoints " +
			"#123 tracks the removal"},
		{Kind: kindAdded, Description: "a feature"},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Errorf("got %+v\nwant %+v", changes, want)
	}
}

func TestSectionKind(t *testing.T) {
	for title, want := range map[string]string{
		"Features": kindAdded, "Bug Fixes": kindFixed, "⚠ BREAKING CHANGES": kindChanged, "Dependencies": kindChanged,
		"Reverts": kindChanged, "Performance Improvements": kindChanged, "Security": kindSecurity,
		"Documentation": kindSkip, "Miscellaneous Chores": kindSkip, "Something new": kindChanged,
	} {
		if got, _ := sectionKind(title); got != want {
			t.Errorf("sectionKind(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestParseIndex(t *testing.T) {
	e, err := parseIndex([]byte(indexJSON))
	if err != nil {
		t.Fatal(err)
	}
	if e.digest != testDigest || !reflect.DeepEqual(e.Platforms, []string{"linux/amd64", "linux/arm/v7", "linux/arm64"}) {
		t.Errorf("got %+v", e)
	}
	single, err := parseIndex([]byte(`{"schemaVersion": 2, "digest": "` + testDigest + `", "layers": []}`))
	if err != nil || single.Platforms != nil {
		t.Errorf("a single manifest: %+v %v", single, err)
	}
	for _, bad := range []string{`not json`, `{"digest": "sha256:short"}`, `{}`} {
		if _, err := parseIndex([]byte(bad)); err == nil {
			t.Errorf("parseIndex(%s) did not fail", bad)
		}
	}
}

type setup struct {
	dir, chart, index, changelog string
}

func newSetup(t *testing.T, changelog string) setup {
	t.Helper()
	dir := t.TempDir()
	s := setup{dir: dir, chart: filepath.Join(dir, "muster"), index: filepath.Join(dir, "index.json"),
		changelog: filepath.Join(dir, "CHANGELOG.md")}
	if err := os.Mkdir(s.chart, 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.chart, "Chart.yaml"), chartSource)
	write(t, s.index, indexJSON)
	if changelog != "" {
		write(t, s.changelog, changelog)
	}
	return s
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (s setup) run(t *testing.T, tag string) (code int, stdout, stderr string, chart map[string]any, raw string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run([]string{"-chart", s.chart, "-tag", tag, "-changelog", s.changelog, "-image", testImage,
		"-index", s.index}, &out, &errOut)
	data, err := os.ReadFile(filepath.Join(s.chart, "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatalf("Chart.yaml after chartannotate: %v\n%s", err, data)
	}
	return code, out.String(), errOut.String(), chart, string(data)
}

func annotation(t *testing.T, chart map[string]any, key string, into any) bool {
	t.Helper()
	ann, _ := chart["annotations"].(map[string]any)
	v, ok := ann[key]
	if !ok {
		return false
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s is not a string: %#v", key, v)
	}
	if into != nil {
		if err := yaml.Unmarshal([]byte(s), into); err != nil {
			t.Fatalf("%s is not YAML: %v", key, err)
		}
	}
	return true
}

func TestRunPrerelease(t *testing.T) {
	s := newSetup(t, changelogText)
	code, stdout, stderr, chart, raw := s.run(t, "v0.1.0-rc.1")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "8 changes, prerelease true") {
		t.Errorf("stdout: %q", stdout)
	}
	ann := chart["annotations"].(map[string]any)
	if ann[annotationPrerelease] != "true" || ann[annotationSecurity] != "true" ||
		ann["artifacthub.io/license"] != "AGPL-3.0-only" || ann["artifacthub.io/category"] != "monitoring-logging" {
		t.Errorf("annotations: %#v", ann)
	}
	var images []map[string]any
	annotation(t, chart, annotationImages, &images)
	wantImages := []map[string]any{{
		"name": "muster", "image": testImage + ":v0.1.0-rc.1@" + testDigest,
		"platforms": []any{"linux/amd64", "linux/arm/v7", "linux/arm64"},
	}}
	if !reflect.DeepEqual(images, wantImages) {
		t.Errorf("images: %#v", images)
	}
	var changes []change
	annotation(t, chart, annotationChanges, &changes)
	if len(changes) != 8 || changes[3].Kind != kindSecurity || changes[1].Links[0].Name != "#331" {
		t.Errorf("changes: %+v", changes)
	}
	if !strings.HasPrefix(raw, "# SPDX-License-Identifier: AGPL-3.0-only\n") || !strings.Contains(raw, "keywords:\n  - alerting") {
		t.Errorf("the rest of Chart.yaml changed:\n%s", raw)
	}
}

func TestRunRelease(t *testing.T) {
	s := newSetup(t, strings.ReplaceAll(changelogText, "0.1.0-rc.1", "0.1.0"))
	code, _, _, chart, _ := s.run(t, "v0.1.0")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	ann := chart["annotations"].(map[string]any)
	if ann[annotationPrerelease] != "false" || !annotation(t, chart, annotationChanges, nil) {
		t.Errorf("annotations: %#v", ann)
	}
}

func TestRunWithoutChanges(t *testing.T) {
	for name, changelog := range map[string]string{
		"no section":         "## [0.0.9](x) (2026-01-01)\n\n### Features\n\n* old\n",
		"no changelog":       "",
		"only documentation": "## [0.1.0](x) (2026-10-30)\n\n### Documentation\n\n* docs\n",
	} {
		t.Run(name, func(t *testing.T) {
			s := newSetup(t, changelog)
			code, _, stderr, chart, _ := s.run(t, "v0.1.0")
			if code != 0 || !strings.Contains(stderr, "warning:") {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			if annotation(t, chart, annotationChanges, nil) || annotation(t, chart, annotationSecurity, nil) ||
				!annotation(t, chart, annotationImages, nil) {
				t.Errorf("annotations: %#v", chart["annotations"])
			}
		})
	}
}

func TestRunAgainRemovesStaleChanges(t *testing.T) {
	s := newSetup(t, changelogText)
	if code, _, _, chart, _ := s.run(t, "v0.1.0-rc.1"); code != 0 || !annotation(t, chart, annotationChanges, nil) {
		t.Fatalf("the first run: exit %d", code)
	}
	write(t, s.changelog, "")
	code, _, _, chart, _ := s.run(t, "v0.1.0-rc.1")
	if code != 0 || annotation(t, chart, annotationChanges, nil) || annotation(t, chart, annotationSecurity, nil) {
		t.Errorf("the second run: exit %d, annotations %#v", code, chart["annotations"])
	}
}

func TestRunReplacesImagesAndKeepsLinks(t *testing.T) {
	s := newSetup(t, changelogText)
	links := "- name: Source\n  url: " + repoURL + "\n"
	write(t, filepath.Join(s.chart, "Chart.yaml"), chartSource+
		"  artifacthub.io/images: |\n    - name: old\n      image: example.org/old:1\n"+
		"  artifacthub.io/links: |\n    - name: Source\n      url: "+repoURL+"\n")
	code, _, _, chart, _ := s.run(t, "v0.1.0-rc.1")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var images []map[string]any
	annotation(t, chart, annotationImages, &images)
	if len(images) != 1 || images[0]["image"] != testImage+":v0.1.0-rc.1@"+testDigest {
		t.Errorf("images: %#v", images)
	}
	if got := chart["annotations"].(map[string]any)["artifacthub.io/links"]; got != links {
		t.Errorf("links: %q, want %q", got, links)
	}
}

func TestRunWithoutAnnotations(t *testing.T) {
	s := newSetup(t, changelogText)
	write(t, filepath.Join(s.chart, "Chart.yaml"), "apiVersion: v2\nname: muster\nversion: 0.0.0\n")
	code, _, _, chart, _ := s.run(t, "v0.1.0-rc.1")
	if code != 0 || !annotation(t, chart, annotationImages, nil) {
		t.Errorf("exit %d, chart %#v", code, chart)
	}
}

func TestRunFails(t *testing.T) {
	s := newSetup(t, changelogText)
	var out, errOut bytes.Buffer
	if code := run([]string{"-chart", s.chart}, &out, &errOut); code != 2 {
		t.Errorf("missing flags: exit %d", code)
	}
	if code := run([]string{"-unknown"}, &out, &errOut); code != 2 {
		t.Errorf("an unknown flag: exit %d", code)
	}
	// In order: each case keeps what the one before it broke.
	for _, c := range []struct {
		name   string
		mutate func() string
	}{
		{"a tag that is not a version", func() string { return "release-1" }},
		{"no index", func() string {
			if err := os.Remove(s.index); err != nil {
				t.Fatal(err)
			}
			return "v0.1.0"
		}},
		{"no chart", func() string {
			write(t, s.index, indexJSON)
			if err := os.RemoveAll(s.chart); err != nil {
				t.Fatal(err)
			}
			return "v0.1.0"
		}},
	} {
		tag := c.mutate()
		errOut.Reset()
		code := run([]string{"-chart", s.chart, "-tag", tag, "-changelog", s.changelog, "-image", testImage,
			"-index", s.index}, &out, &errOut)
		if code != 1 || !strings.HasPrefix(errOut.String(), "chartannotate: ") {
			t.Errorf("%s: exit %d, stderr %q", c.name, code, errOut.String())
		}
	}
}
