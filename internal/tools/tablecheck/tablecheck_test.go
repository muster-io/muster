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

const repoDocs = "../../../docs"

func TestCheck(t *testing.T) {
	tests := map[string]struct {
		content string
		want    []string
	}{
		"short code in a table": {
			content: "| Value | Meaning |\n|---|---|\n| `retention.alert_details` | 90 days |\n",
		},
		"long code in the name column": {
			content: "Intro\n\n| Value | Renders |\n|---|---|\n| `dashboard.grafanaDashboard.enabled` | a dashboard |\n",
			want: []string{"p.md:5: a table holds inline code of 34 characters, more than 24: " +
				"dashboard.grafanaDashboard.enabled"},
		},
		"long code in another column and in the header": {
			content: "| `a_long_header_name_of_the_column` | b |\n| :--- | ---: |\n| x | see `organization.critical_is_urgent` |\n",
			want: []string{
				"p.md:1: a table holds inline code of 32 characters, more than 24: a_long_header_name_of_the_column",
				"p.md:3: a table holds inline code of 31 characters, more than 24: organization.critical_is_urgent",
			},
		},
		"every long span of a row": {
			content: "| a | b |\n|---|---|\n| `MUSTER_DATABASE_SESSION_HOST`, `MUSTER_DATABASE_SESSION_PORT` | x |\n",
			want: []string{
				"p.md:3: a table holds inline code of 28 characters, more than 24: MUSTER_DATABASE_SESSION_HOST",
				"p.md:3: a table holds inline code of 28 characters, more than 24: MUSTER_DATABASE_SESSION_PORT",
			},
		},
		"long code outside tables": {
			content: "Set `dashboard.grafanaDashboard.enabled`.\n\n`dashboard.grafanaDashboard.enabled`\n:   renders | a " +
				"dashboard\n",
		},
		"a table ends at a blank line": {
			content: "| a | b |\n|---|---|\n| x | y |\n\nText with a | and `dashboard.grafanaDashboard.enabled`\n",
		},
		"a table inside a code fence": {
			content: "```markdown\n| a | b |\n|---|---|\n| `dashboard.grafanaDashboard.enabled` | y |\n```\n",
		},
		"a table after a code fence": {
			content: "~~~~\n```\n~~~~\n\n| a | b |\n|---|---|\n| `dashboard.grafanaDashboard.enabled` | y |\n",
			want: []string{"p.md:7: a table holds inline code of 34 characters, more than 24: " +
				"dashboard.grafanaDashboard.enabled"},
		},
		"allowed with a reason": {
			content: "<!-- tablecheck: allow URLs to copy, longer than a phone line anyway -->\n\n| a | b |\n|---|---|\n" +
				"| URI | `https://muster.example.org/api/v1/sessions/oidc/callback` |\n",
		},
		"an allow comment covers only the next table": {
			content: "<!-- tablecheck: allow URLs -->\n\n| a | b |\n|---|---|\n| x | y |\n\nText\n\n| a | b |\n|---|---|\n" +
				"| x | `https://muster.example.org/api/v1/sessions/oidc/callback` |\n",
			want: []string{"p.md:11: a table holds inline code of 56 characters, more than 24: " +
				"https://muster.example.org/api/v1/sessions/oidc/callback"},
		},
		"an allow comment without a reason": {
			content: "<!-- tablecheck: allow -->\n\n| a | b |\n|---|---|\n| x | `dashboard.grafanaDashboard.enabled` |\n",
			want: []string{
				"p.md:1: a tablecheck comment needs a reason: <!-- tablecheck: allow <reason> -->",
				"p.md:5: a table holds inline code of 34 characters, more than 24: dashboard.grafanaDashboard.enabled",
			},
		},
		"characters, not bytes": {
			content: "| a | b |\n|---|---|\n| `отложить на час` | `отложить на двадцать минут` |\n",
			want: []string{"p.md:3: a table holds inline code of 26 characters, more than 24: " +
				"отложить на двадцать минут"},
		},
		"no delimiter row, no table": {
			content: "| a | b |\n| `dashboard.grafanaDashboard.enabled` | y |\n",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := check("p.md", tt.content, defaultMax); !slices.Equal(got, tt.want) {
				t.Errorf("check() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCodeSpans(t *testing.T) {
	tests := map[string][]string{
		"| `a` and `b c` |":           {"a", "b c"},
		"`` a`b `` and ``` `` ```":    {"a`b", "``"},
		"an `unclosed span":           nil,
		"\\`not code\\` but `code`":   {"code"},
		"`` ` ``":                     {"`"},
		"` `":                         {" "},
		"a ``double`` and `single`":   {"double", "single"},
		"three ``` closes ``` only 3": {"closes"},
	}
	for line, want := range tests {
		if got := codeSpans(line); !slices.Equal(got, want) {
			t.Errorf("codeSpans(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestIsDelimiterRow(t *testing.T) {
	tests := map[string]bool{
		"|---|---|":          true,
		"| :--- | ---: |":    true,
		"|:-:|":              true,
		"---|---":            true,
		"---":                false,
		"| a | b |":          false,
		"|---|---| text":     false,
		"| --- | | --- |":    false,
		"`code` | not a row": false,
	}
	for line, want := range tests {
		if got := isDelimiterRow(line); got != want {
			t.Errorf("isDelimiterRow(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("ok.md", "| a | b |\n|---|---|\n| `short` | x |\n")
	write("notes.txt", "| a | b |\n|---|---|\n| `dashboard.grafanaDashboard.enabled` | x |\n")

	var stdout, stderr bytes.Buffer
	if code := run([]string{dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() = %d, want 0; stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "no table holds inline code longer than 24 characters") {
		t.Errorf("stdout = %q", stdout.String())
	}

	write("sub/long.md", "| a | b |\n|---|---|\n| `dashboard.grafanaDashboard.enabled` | x |\n")
	stdout.Reset()
	if code := run([]string{dir}, &stdout, &stderr); code != 1 {
		t.Fatalf("run() = %d, want 1", code)
	}
	out := stdout.String()
	if !strings.Contains(out, filepath.Join(dir, "sub", "long.md")+":3: a table holds inline code of 34 characters") ||
		!strings.Contains(out, "a definition list with the name as the term") {
		t.Errorf("stdout = %q", out)
	}

	stdout.Reset()
	if code := run([]string{"-max", "40", dir}, &stdout, &stderr); code != 0 {
		t.Errorf("run(-max 40) = %d, want 0; stdout %q", code, stdout.String())
	}
	if code := run([]string{filepath.Join(dir, "missing")}, &stdout, &stderr); code != 1 {
		t.Errorf("run(missing) = %d, want 1", code)
	}
	if code := run([]string{"-max", "x"}, &stdout, &stderr); code != 2 {
		t.Errorf("run(-max x) = %d, want 2", code)
	}
}

// TestRepositoryDocs keeps the documentation of this repository within the rule.
func TestRepositoryDocs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{repoDocs}, &stdout, &stderr); code != 0 {
		t.Errorf("run(%s) = %d:\n%s%s", repoDocs, code, stdout.String(), stderr.String())
	}
}
