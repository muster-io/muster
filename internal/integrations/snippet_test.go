// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package integrations

import (
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

const goldenSnippet = `# Alertmanager receiver for Muster, Integration prod-eu: add it to the receivers.
receivers:
  - name: muster-prod-eu
    webhook_configs:
      - url: http://localhost:8081/api/v1/ingest
        send_resolved: true
        max_alerts: 0
        http_config:
          authorization:
            type: Bearer
            credentials: mstr_int_abc

# Place this child route first under the top-level route: with continue it sends every alert to Muster
# and lets the routes after it notify as before.
route:
  routes:
    - receiver: muster-prod-eu
      continue: true
      # Keep repeat_interval between 5 and 15 minutes: Muster learns from the repeats that alerts still fire
      # and that missing ones are gone. Noise is controlled in Muster, not by long repeat intervals.
      repeat_interval: 10m
`

// TestSnippet is C-05.FR-5 and C-05.AC-6: the receiver sends to MUSTER_INGEST_URL with send_resolved, max_alerts 0
// and the token as a bearer credential; the route continues, with snippet.repeat_interval and the comment on its range.
func TestSnippet(t *testing.T) {
	got := Snippet("prod-eu", "http://localhost:8081/api/v1/ingest", "mstr_int_abc")
	if got != goldenSnippet {
		t.Errorf("snippet:\n%s\nwant:\n%s", got, goldenSnippet)
	}
}

// TestSnippetIsValidYAML: names that YAML would misread are quoted, and the snippet parses into the configuration it
// shows.
func TestSnippetIsValidYAML(t *testing.T) {
	for _, name := range []string{"prod-eu", "prod: eu #1", `say "hi"`, "true", "1.5", "кластер", "a\nb"} {
		var cfg struct {
			Receivers []struct {
				Name           string `yaml:"name"`
				WebhookConfigs []struct {
					URL          string `yaml:"url"`
					SendResolved bool   `yaml:"send_resolved"`
					MaxAlerts    int    `yaml:"max_alerts"`
					HTTPConfig   struct {
						Authorization struct {
							Type        string `yaml:"type"`
							Credentials string `yaml:"credentials"`
						} `yaml:"authorization"`
					} `yaml:"http_config"`
				} `yaml:"webhook_configs"`
			} `yaml:"receivers"`
			Route struct {
				Routes []struct {
					Receiver       string `yaml:"receiver"`
					Continue       bool   `yaml:"continue"`
					RepeatInterval string `yaml:"repeat_interval"`
				} `yaml:"routes"`
			} `yaml:"route"`
		}
		snippet := Snippet(name, "https://ingest.example.org/api/v1/ingest", "mstr_int_xyz")
		if err := yaml.Unmarshal([]byte(snippet), &cfg); err != nil {
			t.Fatalf("%q: %v\n%s", name, err, snippet)
		}
		r, w := cfg.Receivers[0], cfg.Receivers[0].WebhookConfigs[0]
		route := cfg.Route.Routes[0]
		if r.Name != "muster-"+name || w.URL != "https://ingest.example.org/api/v1/ingest" || !w.SendResolved ||
			w.MaxAlerts != 0 || w.HTTPConfig.Authorization.Type != "Bearer" ||
			w.HTTPConfig.Authorization.Credentials != "mstr_int_xyz" || route.Receiver != r.Name || !route.Continue ||
			route.RepeatInterval != "10m" {
			t.Errorf("%q parsed as %+v", name, cfg)
		}
		if strings.Contains(snippet, "\n#") && strings.Count(snippet, "# Alertmanager receiver") != 1 {
			t.Errorf("%q broke the comment", name)
		}
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{10 * time.Minute: "10m", 2 * time.Hour: "2h", 90 * time.Second: "90s"} {
		if got := duration(d); got != want {
			t.Errorf("duration(%v) = %s, want %s", d, got, want)
		}
	}
}
