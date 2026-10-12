// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

const referencePath = "../../../design/prd/l1/reference.md"

func TestRunWritesPages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reference")
	var stderr bytes.Buffer
	if code := run([]string{"-out", dir, "-catalogue", referencePath}, &stderr); code != 0 {
		t.Fatalf("run = %d: %s", code, stderr.String())
	}
	tests := map[string][]string{
		logEventsPage: {"DO NOT EDIT", "| `process_started` | INFO | version, commit | C-02 | "},
		metricsPage: {"DO NOT EDIT", "| `muster_build_info` | gauge | version, commit | C-02 | no | ",
			"| `muster_build_info` | `version` | info: "},
		internalAlertsPage: {"DO NOT EDIT",
			"| `MusterSnapshotTruncated` | warning | `integration`, `integration_name` | C-06 | " +
				"`operations/runbooks/MusterSnapshotTruncated/` | ",
			"| `MusterOIDCSecretExpiring` | warning | — | C-19 | `operations/runbooks/MusterOIDCSecretExpiring/` | ",
			"(`latest` for a development build)"},
	}
	for name, wants := range tests {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s lacks %q:\n%s", name, want, data)
			}
		}
	}
}

func TestRunErrors(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"-unknown"}, &stderr); code != 2 {
		t.Errorf("an unknown flag: run = %d", code)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-out", filepath.Join(file, "reference")}, &stderr); code != 1 {
		t.Errorf("an output directory under a file: run = %d", code)
	}
	stderr.Reset()
	if code := run([]string{"-out", t.TempDir(), "-catalogue", filepath.Join(file, "none")}, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "none") {
		t.Errorf("a missing catalogue: run = %d: %s", code, stderr.String())
	}
}

// TestRunFailsOnCatalogueMismatch covers C-19.FR-2: a catalogue that differs from the registry fails the run, naming
// the metric and the field, after the pages are written.
func TestRunFailsOnCatalogueMismatch(t *testing.T) {
	data, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "| `muster_leader` | gauge | — |", "| `muster_leader` | counter | — |", 1)
	if edited == string(data) {
		t.Fatal("the muster_leader row was not found")
	}
	path := filepath.Join(t.TempDir(), "reference.md")
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil { //nolint:gosec // G703: a file under t.TempDir()
		t.Fatal(err)
	}
	dir := t.TempDir()
	var stderr bytes.Buffer
	if code := run([]string{"-out", dir, "-catalogue", path}, &stderr); code != 1 {
		t.Fatalf("run = %d: %s", code, stderr.String())
	}
	if want := `metric "muster_leader": type: counter in the catalogue, gauge in the registry`; !strings.Contains(
		stderr.String(), want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, metricsPage)); err != nil {
		t.Errorf("the pages were not written: %v", err)
	}
}

func TestCatalogueMatchesRegistry(t *testing.T) {
	f, err := os.Open(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cat, err := readCatalogue(f)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := metrics.Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat) != len(defs) {
		t.Errorf("the catalogue has %d metrics, the registry %d", len(cat), len(defs))
	}
	if errs := compareCatalogue(cat, registryMetrics(defs), "the registry"); len(errs) > 0 {
		t.Errorf("the registry differs: %v", errs)
	}
	page, err := readPage(metricsMarkdown(defs))
	if err != nil {
		t.Fatal(err)
	}
	if errs := compareCatalogue(cat, page, metricsPage); len(errs) > 0 {
		t.Errorf("the page differs: %v", errs)
	}
}

func TestReadCatalogueLabels(t *testing.T) {
	tests := []struct {
		cell string
		want []catalogueLabel
	}{
		{"—", nil},
		{"— (the total time spent waiting)", nil},
		{"`version`, `commit`", []catalogueLabel{{Name: "version"}, {Name: "commit"}}},
		{"`client` (`delivery`, `heartbeat` — the client classes of ADR-0015), `outcome` (`ok`, `fatal`)",
			[]catalogueLabel{{"client", []string{"delivery", "heartbeat"}}, {"outcome", []string{"ok", "fatal"}}}},
		{"`client`, `outcome` — the same sets", []catalogueLabel{{Name: "client"}, {Name: "outcome"}}},
		{"`method` (`local` — a wrong login; `oidc` — a refused sign-in; `totp` — a wrong code)",
			[]catalogueLabel{{"method", []string{"local", "oidc", "totp"}}}},
		{"`integration` (the token's Integration; `unknown` when none), `outcome` (`accepted`)",
			[]catalogueLabel{{Name: "integration"}, {"outcome", []string{"accepted"}}}},
		{"`table` — the short-lived table (`short_lived_pruning`)", []catalogueLabel{{Name: "table"}}},
		{"`route`, `destination` (one of them empty), `template` (`line`)",
			[]catalogueLabel{{Name: "route"}, {Name: "destination"}, {"template", []string{"line"}}}},
		{"`destination`; `kind` (`update`); `outcome` (`delivered`)",
			[]catalogueLabel{{Name: "destination"}, {"kind", []string{"update"}}, {"outcome", []string{"delivered"}}}},
		{"`outcome` (`ok`, `skipped`) — re-checks; `skipped` when the user has no session (C-03.FR-30)",
			[]catalogueLabel{{"outcome", []string{"ok", "skipped"}}}},
		{"`template` (as above)", []catalogueLabel{{Name: "template"}}},
	}
	for _, tt := range tests {
		got, err := parseLabels(tt.cell)
		if err != nil {
			t.Errorf("%q: %v", tt.cell, err)
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(tt.want) {
			t.Errorf("%q: got %v, want %v", tt.cell, got, tt.want)
		}
	}
	if _, err := parseLabels("route, `status`"); err == nil {
		t.Error("a label without backticks was read")
	}
}

func TestReadCatalogueErrors(t *testing.T) {
	for name, text := range map[string]string{
		"no heading":  "# Other\n\n| a | b |\n",
		"no table":    "## Metrics catalogue\n\nText.\n\n## Chart rules\n",
		"empty table": "## Metrics catalogue\n\nText.\n",
		"short row":   "## Metrics catalogue\n\n| M | T | L |\n|---|---|---|\n| `muster_x` |\n",
		"no type":     "## Metrics catalogue\n\n| M | T | L |\n|---|---|---|\n| `muster_x` | | — |\n",
		"bad labels":  "## Metrics catalogue\n\n| M | T | L |\n|---|---|---|\n| `muster_x` | gauge | route |\n",
	} {
		if _, err := readCatalogue(strings.NewReader(text)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	cat, err := readCatalogue(strings.NewReader("## Metrics catalogue\n\n| M | T | L |\n|---|---|---|\n" +
		"| `muster_x` | gauge (Leader) | `route` |\n| process metrics (`process_*`) | — | — |\n\nAfter.\n"))
	if err != nil || len(cat) != 1 || !cat[0].Leader || cat[0].Type != "gauge" || cat[0].Labels[0].Name != "route" {
		t.Errorf("readCatalogue = %+v, %v", cat, err)
	}
}

func TestCompareCatalogue(t *testing.T) {
	cat := []catalogueMetric{
		{Name: "muster_a_total", Type: "counter", Labels: []catalogueLabel{{"outcome", []string{"ok", "fatal"}}}},
		{Name: "muster_b", Type: "gauge", Leader: true, Labels: []catalogueLabel{{Name: "route"}}},
	}
	good := func() []catalogueMetric {
		return []catalogueMetric{
			{Name: "muster_a_total", Type: "counter", Labels: []catalogueLabel{{"outcome", []string{"fatal", "ok"}}}},
			{Name: "muster_b", Type: "gauge", Leader: true, Labels: []catalogueLabel{{Name: "route"}}},
		}
	}
	if errs := compareCatalogue(cat, good(), "the registry"); len(errs) > 0 {
		t.Fatalf("equal metrics: %v", errs)
	}
	tests := []struct {
		name   string
		mutate func(m []catalogueMetric) []catalogueMetric
		want   string
	}{
		{"missing from the registry", func(m []catalogueMetric) []catalogueMetric { return m[:1] },
			`metric "muster_b": in the catalogue, missing from the registry`},
		{"extra in the registry", func(m []catalogueMetric) []catalogueMetric {
			return append(m, catalogueMetric{Name: "muster_c", Type: "gauge"})
		}, `metric "muster_c": in the registry, missing from the catalogue`},
		{"type", func(m []catalogueMetric) []catalogueMetric { m[1].Type = "counter"; return m },
			`metric "muster_b": type: gauge in the catalogue, counter in the registry`},
		{"leader", func(m []catalogueMetric) []catalogueMetric { m[1].Leader = false; return m },
			`metric "muster_b": leader only: true in the catalogue, false in the registry`},
		{"labels", func(m []catalogueMetric) []catalogueMetric { m[1].Labels[0].Name = "alertname"; return m },
			`metric "muster_b": labels: [route] in the catalogue, [alertname] in the registry`},
		{"values", func(m []catalogueMetric) []catalogueMetric {
			m[0].Labels[0].Values = []string{"ok"}
			return m
		}, `metric "muster_a_total": label "outcome": values [fatal ok] in the catalogue, [ok] in the registry`},
		{"not closed", func(m []catalogueMetric) []catalogueMetric { m[0].Labels[0].Values = nil; return m },
			`metric "muster_a_total": label "outcome": values [fatal ok] in the catalogue, not a closed set`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := compareCatalogue(cat, tt.mutate(good()), "the registry")
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), tt.want) {
				t.Errorf("got %v, want one error containing %q", errs, tt.want)
			}
		})
	}
}

// TestComparePage: an edited row of the generated page differs from the catalogue.
func TestComparePage(t *testing.T) {
	defs, err := metrics.Catalogue()
	if err != nil {
		t.Fatal(err)
	}
	page := metricsMarkdown(defs)
	f, err := os.Open(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := readCatalogue(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string][3]string{
		"labels": {"muster_alert_groups", "| `muster_alert_groups` | gauge | route, status |",
			"| `muster_alert_groups` | gauge | route |"},
		"values": {"muster_client_requests_total", "| `muster_client_requests_total` | `outcome` | `ok`, `retry_after`, ",
			"| `muster_client_requests_total` | `outcome` | `retry_after`, "},
		"leader": {"muster_alert_groups", "| `muster_alert_groups` | gauge | route, status | C-09 | yes |",
			"| `muster_alert_groups` | gauge | route, status | C-09 | no |"},
	} {
		edited := strings.Replace(page, edit[1], edit[2], 1)
		if edited == page {
			t.Fatalf("%s: %q not found", name, edit[1])
		}
		got, err := readPage(edited)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		errs := compareCatalogue(cat, got, metricsPage)
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), `metric "`+edit[0]+`"`) ||
			!strings.Contains(errs[0].Error(), metricsPage) {
			t.Errorf("%s: got %v", name, errs)
		}
	}
	for name, page := range map[string]string{
		"no table":     "# Other\n",
		"bad row":      "# Metrics\n\n| a | b |\n|---|---|\n| x | y |\n",
		"no values":    "# Metrics\n\n| a | b | c | d | e | f |\n|---|---|---|---|---|---|\n| `muster_x` | gauge | — | C-02 | no | x |\n",
		"bad value":    page + "| x | y | z |\n",
		"unknown name": page + "| `muster_nothing` | `route` | entity: x |\n",
	} {
		if _, err := readPage(page); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestLogEventsMarkdown(t *testing.T) {
	got := logEventsMarkdown([]logging.EventInfo{
		{Name: "quiet_event", Level: logging.LevelWarn, Capability: "C-07", Description: "A | pipe\nand a newline."},
	})
	if want := "| `quiet_event` | WARN | — | C-07 | A \\| pipe and a newline. |\n"; !strings.HasSuffix(got, want) {
		t.Errorf("got\n%s\nwant it to end with\n%s", got, want)
	}
}

func TestMetricsMarkdown(t *testing.T) {
	got := metricsMarkdown([]metrics.Definition{
		{
			Name: "muster_delivery_attempts_total", Kind: metrics.KindCounter, Help: "Attempts.", Capability: "C-11",
			Labels: []metrics.Label{
				{Name: "destination", Kind: metrics.LabelEntity, Description: "the public_id of the destination"},
				{Name: "outcome", Kind: metrics.LabelClosed, Values: []string{"ok", "fatal"}, Description: "the result"},
				{Name: "odd", Description: "free-form"},
			},
		},
		{Name: "muster_delivery_latency_seconds", Kind: metrics.KindHistogram, Help: "Latency.", Capability: "C-11",
			Buckets: []float64{0.25, 1, 5}},
		{Name: "muster_delivery_queue", Kind: metrics.KindGauge, Help: "Queue.", Capability: "C-11", LeaderOnly: true},
	})
	for _, want := range []string{
		"| `muster_delivery_attempts_total` | counter | destination, outcome, odd | C-11 | no | Attempts. |\n",
		"| `muster_delivery_latency_seconds` | histogram | — | C-11 | no | Latency. Buckets (le): 0.25, 1, 5. |\n",
		"| `muster_delivery_queue` | gauge | — | C-11 | yes | Queue. |\n",
		"| `muster_delivery_attempts_total` | `destination` | entity: the public_id of the destination |\n",
		"| `muster_delivery_attempts_total` | `outcome` | `ok`, `fatal` — the result |\n",
		"| `muster_delivery_attempts_total` | `odd` | free-form |\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
}
