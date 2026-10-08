// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
)

// TestLinksAndLookupTables is S-037 against `muster dev`, with E2E_REPLICAS=2 on two replicas, the reads on the
// second one: a Lookup table with its column check; a Link rule reading it, previewed against a Stored Snapshot; the
// links of an Alert Group — the built-in Explore rule at a fixed Grafana Explore URL, the Dashboard rule, the runbook
// and the generatorURL as Source, never a javascript: link nor the failing rule, which is counted; the built-in rule
// that cannot be deleted and the table a rule reads that cannot be deleted (C-12.FR-7, FR-9, C-12.AC-1, AC-6,
// C-09.FR-14, C-01.FR-13); the trusted and the literal Mention of a template (C-12.FR-8).
func TestLinksAndLookupTables(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	reader := h.Replicas[len(h.Replicas)-1]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"links","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	read := patClient{t: t, base: reader.App, token: token}

	table := admin.json(http.MethodPost, "/api/v1/lookup-tables", `{"name":"grafana","columns":["address",
		"datasource_uid"],"entries":[{"key":"prod","values":{"address":"https://grafana.example.org",
		"datasource_uid":"PROM1"}}]}`, http.StatusCreated)
	if entries, _ := table["entries"].([]any); table["name"] != "grafana" || len(entries) != 1 {
		t.Fatalf("table = %v", table)
	}
	bad := admin.do(http.MethodPost, "/api/v1/lookup-tables",
		`{"name":"bad","columns":["a"],"entries":[{"key":"k","values":{"b":"x"}}]}`)
	var problem struct {
		Status int `json:"status"`
		Errors []struct {
			Code    string `json:"code"`
			Pointer string `json:"pointer"`
		} `json:"errors"`
	}
	decode(t, bad, &problem)
	if bad.status != http.StatusUnprocessableEntity || problem.Errors[0].Code != "column_mismatch" ||
		problem.Errors[0].Pointer != "/entries/0/values" {
		t.Errorf("column mismatch = %d %s", bad.status, bad.body)
	}
	const dashboard = `{{ lookup \"grafana\" .Labels.cluster \"address\" }}/d/latency?var-ns={{ .Labels.namespace }}`
	admin.json(http.MethodPost, "/api/v1/link-rules", `{"name":"Dashboard","matchers":[{"label":"cluster","op":"=~",
		"value":".+"}],"scope":{"type":"alert_group"},"url_template":"`+dashboard+`"}`, http.StatusCreated)
	admin.json(http.MethodPost, "/api/v1/link-rules", `{"name":"Broken","matchers":[],"scope":{"type":"alert_group"},
		"url_template":"https://x.example.org/{{ if eq .Labels.namespace \"api\" }}{{ (index .Alerts 5).Labels.pod }}{{ end }}"}`,
		http.StatusCreated)

	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"lnk","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	intID := in["id"].(string)
	intToken := admin.json(http.MethodPost, "/api/v1/integrations/"+intID+"/tokens", `{"name":"lnk"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"lnk","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		intToken+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	for path, body := range map[string]string{
		"/groups/l1": `{"receiver":"lnk","route":"{}","labels":{"alertname":"HighLatency"}}`,
		"/groups/l1/alerts/a": `{"labels":{"cluster":"prod","namespace":"api"},"annotations":{"runbook_url":` +
			`"https://wiki.example.org/latency"},"generator_url":"http://prometheus:9090/graph?g0.expr=up%3D%3D0"}`,
		"/groups/l1/alerts/b": `{"labels":{"cluster":"prod","namespace":"api","pod":"b"},"annotations":{"runbook_url":` +
			`"javascript:alert(1)"}}`,
	} {
		if a := call(t, http.MethodPut, fam+path, body); a.status/100 != 2 {
			t.Fatalf("PUT %s = %d %s", path, a.status, a.body)
		}
	}
	if a := call(t, http.MethodPost, fam+"/groups/l1/notify", `{"reason":"first notification"}`); a.status !=
		http.StatusOK {
		t.Fatalf("notify = %d %s", a.status, a.body)
	}
	var groupID string
	eventually(t, "the alert group", func() bool {
		items, _ := read.json(http.MethodGet, "/api/v1/integrations/"+intID+"/alerts?"+url.Values{
			"label": {`alertname="HighLatency"`}}.Encode(), "", http.StatusOK)["items"].([]any)
		if len(items) == 0 {
			return false
		}
		g, _ := items[0].(map[string]any)["alert_group"].(map[string]any)
		groupID, _ = g["id"].(string)
		return groupID != ""
	})
	snapshots := read.json(http.MethodGet, "/api/v1/stored-snapshots?integration="+intID+"&limit=1", "",
		http.StatusOK)["items"].([]any)
	snapshotID := snapshots[0].(map[string]any)["id"].(string)

	// C-12.AC-6: the preview of the rule against the Stored Snapshot.
	preview := admin.json(http.MethodPost, "/api/v1/template-previews", `{"kind":"link_rule","template":"`+dashboard+
		`","stored_snapshot_id":"`+snapshotID+`"}`, http.StatusOK)
	if preview["valid"] != true || preview["output"] != "https://grafana.example.org/d/latency?var-ns=api" ||
		preview["source"] != nil || preview["format"] != nil {
		t.Errorf("preview = %v", preview)
	}

	// C-12.FR-9, C-09.FR-14, C-12.AC-1: the links on read; the failing rule is counted once per read.
	group := read.json(http.MethodGet, "/api/v1/alert-groups/"+groupID, "", http.StatusOK)
	series := `muster_template_errors_total{route="` + group["route"].(map[string]any)["id"].(string) +
		`",destination="",template="link_rule"}`
	before, _ := strconv.Atoi(reader.Metric(t, series))
	group = read.json(http.MethodGet, "/api/v1/alert-groups/"+groupID, "", http.StatusOK)
	after, _ := strconv.Atoi(reader.Metric(t, series))
	var got []string
	for _, l := range group["links"].([]any) {
		m := l.(map[string]any)
		got = append(got, m["name"].(string)+" "+m["url"].(string))
	}
	panes := `{"muster":{"datasource":"PROM1","queries":[{"refId":"A","expr":"up==0","datasource":{"type":"prometheus",` +
		`"uid":"PROM1"}}],"range":{"from":"now-1h","to":"now"}}}`
	want := []string{
		"Explore https://grafana.example.org/explore?schemaVersion=1&orgId=1&panes=" + url.QueryEscape(panes),
		"Dashboard https://grafana.example.org/d/latency?var-ns=api",
		"Runbook https://wiki.example.org/latency",
		"Source http://prometheus:9090/graph?g0.expr=up%3D%3D0",
	}
	if len(got) != len(want) {
		t.Fatalf("links\n%v\nwant\n%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("link %d = %q, want %q", i, got[i], want[i])
		}
	}
	if after-before != 1 {
		t.Errorf("muster_template_errors_total{template=\"link_rule\"} grew by %d on one read, want 1", after-before)
	}
	if _, _, ok := reader.LogLine("link_rule_failed"); !ok {
		t.Error("no link_rule_failed")
	}

	// C-12.FR-9: the built-in rule cannot be deleted; the table a rule reads cannot be deleted.
	var rules struct {
		Items []struct {
			ID      string `json:"id"`
			Builtin bool   `json:"builtin"`
		} `json:"items"`
	}
	decode(t, admin.do(http.MethodGet, "/api/v1/link-rules", ""), &rules)
	for _, rule := range rules.Items {
		if !rule.Builtin {
			continue
		}
		if a := admin.do(http.MethodDelete, "/api/v1/link-rules/"+rule.ID, ""); a.status != http.StatusConflict ||
			codeOf(t, a) != "builtin_immutable" {
			t.Errorf("delete the built-in rule = %d %s", a.status, a.body)
		}
	}
	if a := admin.do(http.MethodDelete, "/api/v1/lookup-tables/"+table["id"].(string), ""); a.status !=
		http.StatusConflict || codeOf(t, a) != "in_use" {
		t.Errorf("delete the table in use = %d %s", a.status, a.body)
	}

	// C-12.FR-8: a trusted Mention and a literal one.
	out := admin.json(http.MethodPost, "/api/v1/template-previews",
		`{"kind":"root_message","template":"{{ mention \"all\" }} and @all"}`, http.StatusOK)["output"].(string)
	if !slices.Contains(strings.Split(out, "\n"), "@all and @\u200ball") {
		t.Errorf("mentions = %q", out)
	}
	t.Logf("links %v; the failing rule counted %d on one read", got, after-before)
}
