// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

func TestRunWritesPages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reference")
	var stderr bytes.Buffer
	if code := run([]string{"-out", dir}, &stderr); code != 0 {
		t.Fatalf("run = %d: %s", code, stderr.String())
	}
	tests := map[string][]string{
		logEventsPage: {"DO NOT EDIT", "| `process_started` | INFO | version, commit | C-02 | "},
		metricsPage: {"DO NOT EDIT", "| `muster_build_info` | gauge | version, commit | C-02 | no | ",
			"| `muster_build_info` | `version` | info: "},
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
