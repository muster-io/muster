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

const (
	repoReference = "../../../design/prd/l1/reference.md"
	repoPages     = "../../../docs/operations/runbooks"
	completePage  = "# MusterX\n\n## Meaning\n\ntext\n\n## Impact\n\n## Diagnosis\n\n### Step\n\n## Fix\n"
)

func writePages(t *testing.T, pages map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range pages {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestCheck(t *testing.T) {
	names := map[string]bool{"MusterA": true, "MusterB": true}
	tests := map[string]struct {
		pages map[string]string
		want  []string
	}{
		"complete": {
			pages: map[string]string{"MusterA.md": completePage, "MusterB.md": completePage, "index.md": "# Runbooks\n"},
		},
		"missing and extra": {
			pages: map[string]string{"MusterA.md": completePage, "MusterC.md": completePage, "notes.txt": "x"},
			want:  []string{"missing runbook page: MusterB", "extra runbook page: MusterC"},
		},
		"heading missing": {
			pages: map[string]string{"MusterA.md": completePage,
				"MusterB.md": "## Meaning\n## Impact\n## Diagnosis\n"},
			want: []string{"incomplete runbook page: MusterB: lacks ## Fix"},
		},
		"heading twice": {
			pages: map[string]string{"MusterA.md": completePage + "## Impact\n", "MusterB.md": completePage},
			want:  []string{"incomplete runbook page: MusterA: has ## Impact 2 times"},
		},
		"out of order": {
			pages: map[string]string{"MusterA.md": completePage,
				"MusterB.md": "## Meaning\n## Impact\n## Fix\n## Diagnosis\n"},
			want: []string{"incomplete runbook page: MusterB: ## Fix before ## Diagnosis"},
		},
		"heading inside a fence": {
			pages: map[string]string{"MusterA.md": completePage,
				"MusterB.md": "## Meaning\n## Impact\n## Diagnosis\n```md\n## Fix\n```\n"},
			want: []string{"incomplete runbook page: MusterB: lacks ## Fix"},
		},
		"subheading is not a section": {
			pages: map[string]string{"MusterA.md": completePage,
				"MusterB.md": "## Meaning\n## Impact\n## Diagnosis\n### Fix\n## Fixes\n"},
			want: []string{"incomplete runbook page: MusterB: lacks ## Fix"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := check(names, writePages(t, tc.pages))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("check = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheckWithoutDirectory(t *testing.T) {
	got, err := check(map[string]bool{"MusterA": true}, filepath.Join(t.TempDir(), "none"))
	if err != nil || !slices.Equal(got, []string{"missing runbook page: MusterA"}) {
		t.Errorf("check = %q, %v", got, err)
	}
}

func TestReadRules(t *testing.T) {
	const fixture = "# Reference\n\n## Chart rules\n\nText.\n\n| Rule | Severity |\n|---|---|\n" +
		"| `MusterDown` | critical |\n| `MusterSlow` | warning \\| info |\n\nAfter.\n\n| `MusterNot` | x |\n"
	got, err := readRules(strings.NewReader(fixture))
	if err != nil || !slices.Equal(got, []string{"MusterDown", "MusterSlow"}) {
		t.Errorf("readRules = %q, %v", got, err)
	}
	errs := map[string]string{
		"no table under": "## Chart rules\n\nText.\n\n## Next\n\n| a |\n|---|\n| `MusterA` |\n",
		"no table":       "## Metrics\n",
		"is not Muster":  "## Chart rules\n\n| Rule |\n|---|\n| `KubeDown` |\n",
		"no rules under": "## Chart rules\n\n| Rule |\n|---|\n",
	}
	for want, input := range errs {
		if _, err := readRules(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("readRules(%q) = %v, want an error with %q", input, err, want)
		}
	}
}

func TestRunOnRepository(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-reference", repoReference, "-pages", repoPages}, &stdout, &stderr); code != 0 {
		t.Fatalf("run = %d:\n%s%s", code, stdout.String(), stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "runbook pages match the Internal alerts and the chart rules: ") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestRunReportsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(repoPages)); err != nil {
		t.Fatal(err)
	}
	removed := "MusterClockSkew"
	if err := os.Remove(filepath.Join(dir, removed+".md")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-reference", repoReference, "-pages", dir}, &stdout, &stderr); code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	if first, _, _ := strings.Cut(stdout.String(), "\n"); first != "missing runbook page: "+removed {
		t.Errorf("first line = %q, want the missing page %s", first, removed)
	}
}

func TestRunErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-unknown"}, &stdout, &stderr); code != 2 {
		t.Errorf("unknown flag = %d, want 2", code)
	}
	missing := filepath.Join(t.TempDir(), "reference.md")
	if code := run([]string{"-reference", missing, "-pages", repoPages}, &stdout, &stderr); code != 1 {
		t.Errorf("missing reference = %d, want 1", code)
	}
	bad := filepath.Join(t.TempDir(), "reference.md")
	if err := os.WriteFile(bad, []byte("## Chart rules\n\n| Rule |\n|---|\n| `bad` |\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"-reference", bad, "-pages", repoPages}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "is not Muster<Name>") {
		t.Errorf("bad rule = %d: %s", code, stderr.String())
	}
	notDir := filepath.Join(t.TempDir(), "file.md")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-reference", repoReference, "-pages", notDir}, &stdout, &stderr); code != 1 {
		t.Errorf("pages is a file = %d, want 1", code)
	}
}

// writeManifest writes a rendered chart whose PrometheusRule and VMRule hold the rules of the repository's
// reference.md and extra, with the runbook_url of each name's page unless urls gives another.
func writeManifest(t *testing.T, extra []string, urls map[string]string) string {
	t.Helper()
	data, err := os.ReadFile(repoReference)
	if err != nil {
		t.Fatal(err)
	}
	names, err := readRules(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var rules strings.Builder
	for _, n := range slices.Concat(names, extra) {
		url, ok := urls[n]
		if !ok {
			url = "https://docs.example.org/latest/operations/runbooks/" + n + "/"
		}
		rules.WriteString("      - alert: " + n + "\n        expr: vector(1)\n        annotations:\n" +
			"          runbook_url: " + url + "\n")
	}
	spec := "spec:\n  groups:\n    - name: muster\n      rules:\n" + rules.String()
	content := "---\n# Source: muster/templates/empty.yaml\n---\nkind: Deployment\nspec:\n  replicas: 2\n" +
		"---\nkind: PrometheusRule\n" + spec + "---\nkind: VMRule\n" + spec
	return writeFile(t, content)
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chart.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunWithManifest(t *testing.T) {
	tests := map[string]struct {
		manifest func(t *testing.T) string
		code     int
		want     string
	}{
		"rendered rules match": {
			manifest: func(t *testing.T) string { return writeManifest(t, nil, nil) },
			want:     "runbook pages match the Internal alerts, the chart rules and the rendered chart rules: ",
		},
		"rendered rule without a page": {
			manifest: func(t *testing.T) string { return writeManifest(t, []string{"MusterExtraRule"}, nil) },
			code:     1,
			want:     "missing runbook page: MusterExtraRule\n",
		},
		"runbook_url of another page": {
			manifest: func(t *testing.T) string {
				return writeManifest(t, nil,
					map[string]string{"MusterNoLeader": "https://docs.example.org/latest/operations/runbooks/MusterDown/"})
			},
			code: 1,
			want: `chart rule MusterNoLeader: runbook_url "https://docs.example.org/latest/operations/runbooks/MusterDown/" ` +
				"does not end in /operations/runbooks/MusterNoLeader/\n",
		},
		"runbook_url before a missing page": {
			manifest: func(t *testing.T) string {
				return writeManifest(t, []string{"MusterExtraRule"}, map[string]string{"MusterExtraRule": ""})
			},
			code: 1,
			want: `chart rule MusterExtraRule: runbook_url "" does not end in /operations/runbooks/MusterExtraRule/` +
				"\nmissing runbook page: MusterExtraRule\n",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"-reference", repoReference, "-pages", repoPages, "-manifest", tc.manifest(t)},
				&stdout, &stderr)
			if code != tc.code || !strings.HasPrefix(stdout.String(), tc.want) ||
				(tc.code != 0 && stdout.String() != tc.want) {
				t.Errorf("run = %d:\n%s%s\nwant %d:\n%s", code, stdout.String(), stderr.String(), tc.code, tc.want)
			}
		})
	}
}

func TestRunManifestErrors(t *testing.T) {
	tests := map[string]struct {
		path string
		want string
	}{
		"no rules":      {writeFile(t, "kind: Deployment\nspec:\n  replicas: 1\n"), "no chart rules in "},
		"empty groups":  {writeFile(t, "kind: VMRule\nspec:\n  groups: []\n"), "no chart rules in "},
		"invalid YAML":  {writeFile(t, "kind: [\n"), "chart.yaml"},
		"invalid spec":  {writeFile(t, "kind: PrometheusRule\nspec:\n  groups: 3\n"), "the spec of a PrometheusRule"},
		"bad rule name": {writeFile(t, "kind: PrometheusRule\nspec:\n  groups:\n    - rules:\n        - alert: KubeDown\n"), "is not Muster<Name>"},
		"missing file":  {filepath.Join(t.TempDir(), "none.yaml"), "none.yaml"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"-reference", repoReference, "-pages", repoPages, "-manifest", tc.path}, &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("run = %d: %s%s, want an error with %q", code, stdout.String(), stderr.String(), tc.want)
			}
		})
	}
}
