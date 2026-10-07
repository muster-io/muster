// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
)

// putWithIfMatch sends a PUT with the agent's session and the If-Match header.
func (a *agent) putWithIfMatch(target, body, etag string) answer {
	a.t.Helper()
	req, err := http.NewRequestWithContext(a.t.Context(), http.MethodPut, a.base+target, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	for name, value := range a.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value}) //nolint:gosec // G124: a request cookie carries no attributes
	}
	req.Header.Set("X-CSRF-Token", a.csrf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", etag)
	resp, err := noFollow.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{status: resp.StatusCode, header: resp.Header, body: b}
}

// runCLI runs `muster dev <args>` against the harness's database and returns its exit code and standard error.
func (h *Harness) runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	//nolint:gosec // G204: the binary under test, from MUSTER_E2E_BINARY
	cmd := exec.CommandContext(t.Context(), h.binary, append([]string{"dev"}, args...)...)
	cmd.Env = append(os.Environ(), h.databaseEnv()...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode(), stderr.String()
	}
	if err != nil {
		t.Fatalf("run muster dev %v: %v", args, err)
	}
	return 0, stderr.String()
}

// TestInternalAlerts is S-021 against `muster dev`, with E2E_REPLICAS=2 on two replicas: the built-in Integration and
// its refusals (C-06.FR-14), MusterSnapshotTruncated raised and resolved (C-06.AC-5), kept through a rename
// (C-06.AC-11), the learned routes and warnings (C-06.FR-18, C-06.AC-3), a replay twice through the CLI that changes
// nothing the second time and counts no Stored Snapshot again (C-06.AC-6), and the deletion that resolves the open
// Alerts and the Internal alert (C-06.AC-7, C-06.AC-10).
func TestInternalAlerts(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	list := admin.json(http.MethodGet, "/api/v1/integrations", "", http.StatusOK)["items"].([]any)
	var builtin string
	for _, item := range list {
		if in := item.(map[string]any); in["builtin"] == true {
			heartbeat := in["heartbeat"].(map[string]any)
			if in["name"] != "Muster" || heartbeat["state"] != "not_configured" {
				t.Errorf("built-in %v", in)
			}
			builtin = in["id"].(string)
		}
	}
	if builtin == "" {
		t.Fatalf("no built-in integration in %v", list)
	}
	for _, a := range []answer{
		admin.do(http.MethodDelete, "/api/v1/integrations/"+builtin, ""),
		admin.do(http.MethodPost, "/api/v1/integrations/"+builtin+"/tokens", "{}"),
		admin.putWithIfMatch("/api/v1/integrations/"+builtin, `{"name":"x","connection_mode":"webhook_only",
			"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, `"1"`),
	} {
		var p struct{ Code string }
		decode(t, a, &p)
		if a.status != http.StatusConflict || p.Code != "builtin_immutable" {
			t.Errorf("refusal = %d %s", a.status, a.body)
		}
	}

	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"lab","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	id := in["id"].(string)
	token := admin.json(http.MethodPost, "/api/v1/integrations/"+id+"/tokens", `{"name":"lab"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"lab","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		token+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	put := func(path, body string) {
		t.Helper()
		if a := call(t, http.MethodPut, fam+path, body); a.status/100 != 2 {
			t.Fatalf("PUT %s = %d %s", path, a.status, a.body)
		}
	}
	idle := func() {
		t.Helper()
		eventually(t, "processing to settle", func() bool {
			return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state = 'pending'`) == 0
		})
	}
	notify := func(group, body string) {
		t.Helper()
		if a := call(t, http.MethodPost, fam+"/groups/"+group+"/notify", body); a.status != http.StatusOK {
			t.Fatalf("notify %s = %d %s", group, a.status, a.body)
		}
		idle()
	}
	type alert struct {
		Fingerprint string            `json:"fingerprint"`
		Labels      map[string]string `json:"labels"`
		Reason      *string           `json:"resolve_reason"`
		ReasonText  *string           `json:"resolve_reason_text"`
	}
	alerts := func(integration, query string) []alert {
		t.Helper()
		var page struct {
			Items []alert `json:"items"`
		}
		decode(t, admin.do(http.MethodGet, "/api/v1/integrations/"+integration+"/alerts?"+query, ""), &page)
		return page.Items
	}

	// The Internal alerts about lab: the demo Integration's MusterHeartbeatLost may come and go meanwhile, because the
	// development clock jumps further than its Heartbeat timeout.
	internal := func(query string) []alert {
		t.Helper()
		return alerts(builtin, query+"&label="+url.QueryEscape(`integration="`+id+`"`))
	}

	// C-06.AC-5: truncation raises MusterSnapshotTruncated, once.
	put("/groups/g2", `{"receiver":"lab","route":"{}","labels":{"alertname":"PodDown"}}`)
	for _, i := range "0123456789" {
		put("/groups/g2/alerts/x"+string(i), `{"labels":{"pod":"p`+string(i)+`"}}`)
	}
	notify("g2", `{"reason":"first notification"}`)
	notify("g2", `{"reason":"repeat interval elapsed","max_alerts":2}`)
	firing := internal("state=firing")
	if len(firing) != 1 || firing[0].Labels["alertname"] != "MusterSnapshotTruncated" ||
		firing[0].Labels["integration"] != id || firing[0].Labels["integration_name"] != "lab" ||
		firing[0].Labels["severity"] != "warning" || len(firing[0].Labels) != 4 {
		t.Fatalf("internal alerts %+v", firing)
	}
	fingerprint := firing[0].Fingerprint
	warnings := admin.json(http.MethodGet, "/api/v1/integrations/"+id, "", http.StatusOK)["warnings"].([]any)
	if len(warnings) != 2 || warnings[0].(map[string]any)["kind"] != "heartbeat_not_configured" ||
		warnings[1].(map[string]any)["kind"] != "snapshot_truncated" ||
		warnings[1].(map[string]any)["truncated_group_count"] != 1.0 {
		t.Errorf("warnings %v", warnings)
	}
	snapshots := func() []any {
		var out []any
		for _, s := range admin.json(http.MethodGet, "/api/v1/stored-snapshots?integration="+builtin, "",
			http.StatusOK)["items"].([]any) {
			if key, _ := s.(map[string]any)["group_key"].(string); strings.Contains(key, "MusterSnapshotTruncated") {
				out = append(out, s)
			}
		}
		return out
	}
	if s := snapshots(); len(s) != 1 || s[0].(map[string]any)["state"] != "processed" ||
		s[0].(map[string]any)["group_key"] != `{}/{muster="internal"}:{alertname="MusterSnapshotTruncated"}` {
		t.Errorf("stored snapshots of the built-in integration %v", s)
	}
	notify("g2", `{"reason":"repeat interval elapsed","max_alerts":2}`)
	if s := snapshots(); len(s) != 1 {
		t.Errorf("a second truncated snapshot raised again: %d", len(s))
	}

	// C-06.AC-11: a rename keeps the fingerprint.
	got := admin.do(http.MethodGet, "/api/v1/integrations/"+id, "")
	if a := admin.putWithIfMatch("/api/v1/integrations/"+id, `{"name":"lab-eu","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`,
		got.header.Get("ETag")); a.status != http.StatusOK {
		t.Fatalf("rename = %d %s", a.status, a.body)
	}
	idle()
	firing = internal("state=firing")
	if len(firing) != 1 || firing[0].Fingerprint != fingerprint || firing[0].Labels["integration_name"] != "lab-eu" {
		t.Errorf("after the rename %+v", firing)
	}
	notify("g2", `{"reason":"repeat interval elapsed"}`)
	if n := len(internal("state=firing")); n != 0 {
		t.Errorf("%d internal alerts fire after an untruncated snapshot", n)
	}

	// C-06.AC-3 and C-06.FR-18: learned routes, and a long interval.
	put("/groups/g6", `{"receiver":"lab","route":"{}/{team=\"web\"}","labels":{"alertname":"Slow"}}`)
	put("/groups/g6/alerts/s", `{"labels":{"host":"web-1"}}`)
	notify("g6", `{"reason":"first notification"}`)
	for range 3 {
		advance(t, r, 300)
		notify("g6", `{"reason":"repeat interval elapsed"}`)
	}
	put("/groups/g7", `{"receiver":"lab","route":"{}/{kind=\"info\"}","labels":{"alertname":"CertExpiry"}}`)
	put("/groups/g7/alerts/c", `{"labels":{"domain":"example.org"}}`)
	notify("g7", `{"reason":"first notification"}`)
	advance(t, r, 7200)
	notify("g7", `{"reason":"repeat interval elapsed"}`)
	routes := admin.json(http.MethodGet, "/api/v1/integrations/"+id+"/alertmanager-routes", "",
		http.StatusOK)["items"].([]any)
	byPath := map[string]map[string]any{}
	for _, item := range routes {
		route := item.(map[string]any)
		byPath[route["route_path"].(string)] = route
	}
	web, info := byPath[`{}/{team="web"}`], byPath[`{}/{kind="info"}`]
	if web == nil || web["learned_repeat_interval_seconds"].(float64) < 300 ||
		web["learned_repeat_interval_seconds"].(float64) > 310 || web["long_interval_warning"] != false ||
		web["resolve_by_absence_after_seconds"] != 3*web["learned_repeat_interval_seconds"].(float64) {
		t.Errorf("web route %v", web)
	}
	if info == nil || info["long_interval_warning"] != true || info["learned_repeat_interval_seconds"].(float64) < 7200 ||
		!strings.Contains(info["recommended_snippet"].(string), "repeat_interval: 10m") {
		t.Errorf("info route %v", info)
	}
	if u := byPath["{}"]; u == nil || u["truncated_group_count"] != 0.0 {
		t.Errorf("route {} %v", u)
	}

	// C-06.AC-6: replay twice through the CLI.
	if code, stderr := h.runCLI(t, "ingest", "replay", "--since", "3h", "--integration", "lab-eu"); code != 2 ||
		!strings.Contains(stderr, "--actor is required") {
		t.Errorf("without --actor = %d %s", code, stderr)
	}
	count := func() float64 {
		return admin.json(http.MethodGet, "/api/v1/integrations/"+id, "", http.StatusOK)["snapshot_count"].(float64)
	}
	count0 := count()
	code, stderr := h.runCLI(t, "ingest", "replay", "--since", "3h", "--integration", "lab-eu", "--actor", "ops-alice")
	if code != 0 || !strings.HasPrefix(stderr, "replayed ") || !strings.HasSuffix(stderr, " Stored Snapshots of lab-eu\n") ||
		strings.HasPrefix(stderr, "replayed 0 ") {
		t.Fatalf("replay = %d %s", code, stderr)
	}
	idle()
	lines := func() int {
		n := 0
		for _, rr := range h.Replicas {
			n += strings.Count(rr.Output(), `"event":"snapshot_processed"`)
		}
		return n
	}
	changes := func(skip []int) int {
		total := 0
		for i, rr := range h.Replicas {
			seen := 0
			for line := range strings.Lines(rr.Output()) {
				if !strings.Contains(line, `"event":"snapshot_processed"`) {
					continue
				}
				if seen++; seen <= skip[i] {
					continue
				}
				var m map[string]any
				if err := json.Unmarshal([]byte(line), &m); err != nil {
					t.Fatal(err)
				}
				total += int(m["fired"].(float64) + m["resolved"].(float64) + m["gone"].(float64) + m["continued"].(float64))
			}
		}
		return total
	}
	before := make([]int, len(h.Replicas))
	for i, rr := range h.Replicas {
		before[i] = strings.Count(rr.Output(), `"event":"snapshot_processed"`)
	}
	total := lines()
	if code, stderr := h.runCLI(t, "ingest", "replay", "--since", "3h", "--integration", "lab-eu", "--actor",
		"ops-alice"); code != 0 {
		t.Fatalf("second replay = %d %s", code, stderr)
	}
	idle()
	if lines() <= total {
		t.Error("the second replay processed nothing")
	}
	if n := changes(before); n != 0 {
		t.Errorf("the second replay made %d alert changes", n)
	}
	if c := count(); c != count0 {
		t.Errorf("snapshot_count %v after two replays, want %v", c, count0)
	}
	entries := admin.json(http.MethodGet, "/api/v1/audit-log?action=ingest.replayed", "", http.StatusOK)["items"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["transport"] != "cli" ||
		entries[0].(map[string]any)["actor"].(map[string]any)["name"] != "ops-alice" {
		t.Errorf("audit log %v", entries)
	}

	// C-06.AC-7 and C-06.AC-10: the deletion resolves the open Alerts and the Internal alert.
	notify("g2", `{"reason":"repeat interval elapsed","max_alerts":2}`)
	if n := len(internal("state=firing")); n != 1 {
		t.Fatalf("%d internal alerts fire", n)
	}
	open := len(alerts(id, "state=firing&limit=500"))
	series := `muster_alerts_resolved_total{integration="` + id + `",reason="integration_deleted"}`
	deleted0 := 0
	for _, rr := range h.Replicas {
		deleted0 += metricValue(t, rr, series)
	}
	if a := admin.do(http.MethodDelete, "/api/v1/integrations/"+id, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", a.status, a.body)
	}
	idle()
	resolved := alerts(id, "state=resolved&label="+url.QueryEscape(`pod="p0"`))
	if len(resolved) != 1 || *resolved[0].Reason != "integration_deleted" ||
		*resolved[0].ReasonText != "Integration lab-eu deleted" {
		t.Errorf("p0 after the deletion %+v", resolved)
	}
	if n := len(alerts(id, "state=firing&limit=500")); n != 0 || open == 0 {
		t.Errorf("%d of %d alerts fire after the deletion", n, open)
	}
	eventually(t, "the deletions on the metrics of a replica", func() bool {
		total := 0
		for _, rr := range h.Replicas {
			total += metricValue(t, rr, series)
		}
		return total-deleted0 == open
	})
	if n := len(internal("state=firing")); n != 0 {
		t.Errorf("%d internal alerts fire after the deletion", n)
	}
}
