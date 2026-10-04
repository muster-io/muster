// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build lint

package archlint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/archlint/testdata/rule5/probes"
)

// A fixture line that must be reported ends with a comment "want: N", one number per expected finding; every other
// line must stay silent.
var (
	wantRe = regexp.MustCompile(`(?:--|//)\s*want:\s*([0-9][0-9, ]*)`)
	ruleRe = regexp.MustCompile(`archlint rule (\d+)`)

	pinnedLintRe = regexp.MustCompile(`(?m)^GOLANGCI_LINT_VERSION\s*:?=\s*(v\d+\.\d+\.\d+)\s*$`)
)

func TestRules(t *testing.T) {
	t.Run("1_org_scope", func(t *testing.T) { testRunFixture(t, "rule1") })
	t.Run("2_group_tables", func(t *testing.T) { testRunFixture(t, "rule2") })
	t.Run("3_messenger_calls", func(t *testing.T) { testRunFixture(t, "rule3") })
	t.Run("4_http_clients", func(t *testing.T) { testRunFixture(t, "rule4") })
	t.Run("5_secret_leak", testSecretLeak)
	t.Run("6_context_background", func(t *testing.T) { testGolangciFixture(t, "rule6") })
	t.Run("7_logging", func(t *testing.T) { testGolangciFixture(t, "rule7") })
	t.Run("8_histograms", func(t *testing.T) { testGolangciFixture(t, "rule8") })
}

func testRunFixture(t *testing.T, name string) {
	dir := filepath.Join("testdata", name)
	diags, err := Run(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Run(%s): %v", dir, err)
	}
	got := findings{}
	for _, d := range diags {
		got.add(d.Path, d.Line, d.Rule, d.String())
	}
	got.compare(t, wantFindings(t, dir))
}

func testGolangciFixture(t *testing.T, name string) {
	dir, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	config, err := filepath.Abs(filepath.Join("..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G204: running the golangci-lint binary that golangciLint picked is the point of the test
	cmd := exec.CommandContext(t.Context(), golangciLint(t), "run", "--config", config,
		"--enable-only=forbidigo,depguard", "--output.json.path=stdout", "--issues-exit-code=0", "--show-stats=false",
		"./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, stderr.String())
	}
	var report struct {
		Issues []struct {
			FromLinter string
			Text       string
			Pos        struct {
				Filename string
				Line     int
			}
		}
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("parse golangci-lint output: %v\n%s", err, out)
	}
	got := findings{}
	for _, issue := range report.Issues {
		rule := 0
		if m := ruleRe.FindStringSubmatch(issue.Text); m != nil {
			rule, _ = strconv.Atoi(m[1])
		}
		file := issue.Pos.Filename
		if filepath.IsAbs(file) {
			if file, err = filepath.Rel(dir, file); err != nil {
				t.Fatal(err)
			}
		}
		got.add(filepath.ToSlash(file), issue.Pos.Line, rule, issue.FromLinter+": "+issue.Text)
	}
	got.compare(t, wantFindings(t, dir))
}

// golangciLint finds the linter for rules 6 to 8: GOLANGCI_LINT, else the version the Makefile pins once a make target
// has installed it, else golangci-lint on PATH.
func golangciLint(t *testing.T) string {
	if bin := os.Getenv("GOLANGCI_LINT"); bin != "" {
		return bin
	}
	const repo = "../.."
	if makefile, err := fs.ReadFile(os.DirFS(repo), "Makefile"); err == nil {
		if m := pinnedLintRe.FindSubmatch(makefile); m != nil {
			pinned := "bin/tools/golangci-lint-" + string(m[1])
			if _, err := fs.Stat(os.DirFS(repo), pinned); err == nil {
				if abs, err := filepath.Abs(filepath.Join(repo, pinned)); err == nil {
					return abs
				}
			}
		}
	}
	bin, err := exec.LookPath("golangci-lint")
	if err != nil {
		t.Fatalf("golangci-lint is needed for rules 6 to 8: run make lint-arch, or set GOLANGCI_LINT: %v", err)
	}
	return bin
}

func testSecretLeak(t *testing.T) {
	fixtures := []struct {
		probe Probe
		want  []string
	}{
		{Probe{"logs_secret", probes.LogsSecret}, []string{"secret 1 of 3 reached the log output"}},
		{Probe{"wraps_secret", probes.WrapsSecret}, []string{"secret 2 of 3 reached the returned error"}},
		{Probe{"hides_secret_in_cause", probes.HidesSecretInCause}, []string{"secret 3 of 3 reached the returned error"}},
		{Probe{"formats_secret", probes.FormatsSecret}, []string{"secret 1 of 3 reached the returned error"}},
		{Probe{"joins_secret", probes.JoinsSecret}, []string{"secret 2 of 3 reached the returned error"}},
		{Probe{"logs_basic_auth", probes.LogsBasicAuth}, []string{"secret 1 of 3 reached the log output (base64)"}},
		{Probe{"escapes_secret_in_url", probes.EscapesSecretInURL}, []string{"secret 2 of 3 reached the returned error (URL-escaped)"}},
		{Probe{"logs_secret_in_hex", probes.LogsSecretInHex}, []string{"secret 3 of 3 reached the log output (hex)"}},
		{Probe{"redacts", probes.Redacts}, nil},
		{Probe{"silent", probes.Silent}, nil},
	}
	for _, f := range fixtures {
		var got []string
		for _, d := range CheckProbe(f.probe) {
			if d.Rule != 5 || d.Path != f.probe.Name {
				t.Errorf("probe %s: unexpected diagnostic %s", f.probe.Name, d)
			}
			got = append(got, d.Message)
		}
		if !slices.Equal(got, f.want) {
			t.Errorf("probe %s: got %q, want %q", f.probe.Name, got, f.want)
		}
	}
}

// TestProbes runs the registered probes; it passes while the registry is empty.
func TestProbes(t *testing.T) {
	for _, p := range Probes() {
		t.Run(p.Name, func(t *testing.T) {
			for _, d := range CheckProbe(p) {
				t.Error(d)
			}
		})
	}
}

func TestRunErrors(t *testing.T) {
	const goMod = "module github.com/muster-io/muster\n\ngo 1.27.0\n"
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name: "query files without migrations",
			files: map[string]string{
				"internal/alerts/query.sql": "-- name: ListAlerts :many\nSELECT id FROM alerts;\n",
			},
			want: "rule 1: query files exist, but internal/db/migrations has no *.up.sql migration",
		},
		{
			name: "query that does not parse",
			files: map[string]string{
				"internal/db/migrations/0001_init.up.sql": "CREATE TABLE alerts (id bigint, org_id bigint);\n",
				"internal/alerts/query.sql":               "-- name: ListAlerts :many\nSELEC id FROM alerts;\n",
			},
			want: "internal/alerts/query.sql:2:1: syntax error",
		},
		{
			name: "adapter interface renamed",
			files: map[string]string{
				"go.mod":                       goMod,
				"internal/delivery/adapter.go": "package delivery\n\ntype Messenger interface{}\n",
			},
			want: "rule 3: internal/delivery has no type Adapter",
		},
		{
			name: "adapter method renamed",
			files: map[string]string{
				"go.mod": goMod,
				"internal/delivery/adapter.go": "package delivery\n\ntype Adapter interface {\n" +
					"\tPublish(text string) error\n\tUpdate(id, text string) error\n\tAnswer(id, text string) error\n}\n",
			},
			want: "rule 3: internal/delivery.Adapter has no method Reply",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				file := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Run(dir, DefaultConfig())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run: got error %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// findings counts the findings per "path:line: rule N" and keeps their texts for failure messages.
type findings map[string][]string

func findingKey(file string, line, rule int) string {
	return fmt.Sprintf("%s:%d: rule %d", file, line, rule)
}

func (f findings) add(file string, line, rule int, text string) {
	key := findingKey(file, line, rule)
	f[key] = append(f[key], text)
}

func (f findings) compare(t *testing.T, want map[string]int) {
	t.Helper()
	for _, key := range slices.Sorted(maps.Keys(want)) {
		if n := len(f[key]); n != want[key] {
			t.Errorf("bad fixture %s: want %d finding(s), got %d %q", key, want[key], n, f[key])
		}
	}
	for _, key := range slices.Sorted(maps.Keys(f)) {
		if want[key] == 0 {
			t.Errorf("good fixture reported: %s: %q", key, f[key])
		}
	}
}

// wantFindings reads the "want: N" markers of the .go and .sql files under dir. It fails unless the fixture has at
// least one bad case and one good file.
func wantFindings(t *testing.T, dir string) map[string]int {
	t.Helper()
	fsys := os.DirFS(dir)
	want := map[string]int{}
	goodFiles := 0
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (path.Ext(name) != ".go" && path.Ext(name) != ".sql") {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		marked := false
		for i, line := range strings.Split(string(data), "\n") {
			m := wantRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			marked = true
			for _, field := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' }) {
				rule, err := strconv.Atoi(field)
				if err != nil {
					return fmt.Errorf("%s:%d: %w", name, i+1, err)
				}
				want[findingKey(name, i+1, rule)]++
			}
		}
		if !marked {
			goodFiles++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 || goodFiles == 0 {
		t.Fatalf("fixture %s needs bad cases (want markers) and good files: %d marked lines, %d good files", dir, len(want), goodFiles)
	}
	return want
}
