// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

// patClient calls the API with a Personal access token, which outlives the jumps of the development clock that end a
// session.
type patClient struct {
	t     *testing.T
	base  string
	token string
}

func (c patClient) do(method, target, body string, header ...string) answer {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.base+target, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{status: resp.StatusCode, header: resp.Header, body: b}
}

func (c patClient) json(method, target, body string, want int, header ...string) map[string]any {
	c.t.Helper()
	a := c.do(method, target, body, header...)
	if a.status != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, target, a.status, a.body, want)
	}
	var m map[string]any
	if len(a.body) > 0 {
		decode(c.t, a, &m)
	}
	return m
}

// signal sends a Heartbeat signal by method to url with the bearer token, none when it is empty, and returns the
// status.
func signal(t *testing.T, method, target, token, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestHeartbeat is S-023 against `muster dev`, with E2E_REPLICAS=2 on two replicas, the time driven by the development
// clock, which wakes the Leader's Heartbeat check and Stale scan: the Heartbeat settings and states (C-07.FR-2,
// FR-3), the endpoint and its refusals (C-07.AC-3), nothing raised or Stale before the first signal (C-07.AC-2), the
// loss with MusterHeartbeatLost and its metric and the recovery (C-07.AC-1, FR-8), Stale at T+15 min and, after a
// loss, at T+25 min (C-07.AC-4), never Stale without a Heartbeat (C-06.AC-3), the Heartbeat snippet (C-07.FR-6), the
// fake Alertmanager's Heartbeat sender and the demo Integration's Heartbeat (C-01.FR-13), and the deletion that
// resolves MusterHeartbeatLost (C-07.AC-5).
func TestHeartbeat(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	pat := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"heartbeat","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	api := patClient{t: t, base: r.App, token: pat}

	var builtin, demo map[string]any
	for _, item := range api.json(http.MethodGet, "/api/v1/integrations", "", http.StatusOK)["items"].([]any) {
		switch in := item.(map[string]any); in["name"] {
		case "Muster":
			builtin = in
		case devmode.IntegrationName:
			demo = in
		}
	}
	if hb := demo["heartbeat"].(map[string]any); hb["enabled"] != true {
		t.Errorf("the demo integration has no heartbeat: %v", demo)
	}
	if len(builtin["warnings"].([]any)) != 0 {
		t.Errorf("the built-in integration warns: %v", builtin)
	}

	in := api.json(http.MethodPost, "/api/v1/integrations", `{"name":"hb","connection_mode":"webhook_only",
		"static_labels":{"env":"prod"},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	id := in["id"].(string)
	token := api.json(http.MethodPost, "/api/v1/integrations/"+id+"/tokens", `{"name":"hb"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"hb","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		token+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	if a := call(t, http.MethodPost, fam+"/heartbeats", `{"name":"hb","url":"`+r.Ingest+`/api/v1/heartbeat",
		"token":"`+token+`","token_in":"path"}`); a.status != http.StatusNoContent {
		t.Fatalf("heartbeat sender = %d %s", a.status, a.body)
	}
	hb := func() {
		t.Helper()
		a := call(t, http.MethodPost, fam+"/heartbeats/hb/send", "")
		if a.status != http.StatusOK || !strings.Contains(string(a.body), `"status":204`) {
			t.Fatalf("heartbeat = %d %s", a.status, a.body)
		}
	}
	get := func() map[string]any {
		t.Helper()
		return api.json(http.MethodGet, "/api/v1/integrations/"+id, "", http.StatusOK)
	}
	state := func() string {
		t.Helper()
		return get()["heartbeat"].(map[string]any)["state"].(string)
	}
	kinds := func(m map[string]any) []string {
		var out []string
		for _, w := range m["warnings"].([]any) {
			out = append(out, w.(map[string]any)["kind"].(string))
		}
		return out
	}
	idle := func() {
		t.Helper()
		eventually(t, "processing to settle", func() bool {
			return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state = 'pending'`) == 0
		})
	}
	notify := func(body string) {
		t.Helper()
		if a := call(t, http.MethodPost, fam+"/groups/s1/notify", body); a.status != http.StatusOK {
			t.Fatalf("notify = %d %s", a.status, a.body)
		}
		idle()
	}
	type alert struct {
		Labels map[string]string `json:"labels"`
		State  string            `json:"state"`
		Reason *string           `json:"resolve_reason"`
	}
	alerts := func(integration, query string) []alert {
		t.Helper()
		var page struct {
			Items []alert `json:"items"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/integrations/"+integration+"/alerts?"+query, ""), &page)
		return page.Items
	}
	lost := func() []alert {
		t.Helper()
		return alerts(builtin["id"].(string), "state=firing&label="+url.QueryEscape(`integration="`+id+`"`))
	}
	q := "label=" + url.QueryEscape(`svc="a"`)
	lostMetric := func() int {
		total := 0
		for _, rr := range h.Replicas {
			total += metricValue(t, rr, `muster_heartbeat_lost{integration="`+id+`"}`)
		}
		return total
	}

	if m := get(); m["heartbeat"].(map[string]any)["state"] != "not_configured" ||
		strings.Join(kinds(m), ",") != "heartbeat_not_configured" {
		t.Errorf("created %v", m)
	}

	// C-06.AC-3: without a Heartbeat nothing goes Stale, with the scan running.
	if a := call(t, http.MethodPut, fam+"/groups/s1", `{"receiver":"hb","route":"{}/{team=\"x\"}",
		"labels":{"alertname":"Quiet"}}`); a.status/100 != 2 {
		t.Fatalf("group = %d %s", a.status, a.body)
	}
	if a := call(t, http.MethodPut, fam+"/groups/s1/alerts/q", `{"labels":{"svc":"a"}}`); a.status/100 != 2 {
		t.Fatalf("alert = %d %s", a.status, a.body)
	}
	notify(`{"reason":"first notification"}`)
	for range 3 {
		advance(t, r, 300)
		notify(`{"reason":"repeat interval elapsed"}`)
	}
	advance(t, r, 108000)
	time.Sleep(time.Second) // the woken Stale scan, which must resolve nothing
	if got := alerts(id, "state=firing&"+q); len(got) != 1 {
		t.Fatalf("without a heartbeat after 30 h: %+v", got)
	}

	// C-07.FR-2, FR-3 and C-07.AC-2: on, it waits for the first signal, and nothing is raised however long it takes.
	got := api.do(http.MethodGet, "/api/v1/integrations/"+id, "")
	on := api.json(http.MethodPut, "/api/v1/integrations/"+id, `{"name":"hb","connection_mode":"webhook_only",
		"static_labels":{"env":"prod"},"duplicate_window_seconds":45,"heartbeat":{"enabled":true}}`, http.StatusOK,
		"If-Match", got.header.Get("ETag"))
	if hbOn := on["heartbeat"].(map[string]any); hbOn["state"] != "waiting" || hbOn["timeout_seconds"] != 300.0 ||
		strings.Join(kinds(on), ",") != "heartbeat_waiting" {
		t.Fatalf("turned on %v", on)
	}
	advance(t, r, 3600)
	time.Sleep(time.Second) // the woken Heartbeat check, which must raise nothing
	if n := len(lost()); n != 0 || state() != "waiting" {
		t.Fatalf("waiting: %d raised, %s", n, state())
	}

	// C-07.AC-3: GET and POST, bearer or path, whatever the body; wrong, API and revoked tokens are refused.
	second := api.json(http.MethodPost, "/api/v1/integrations/"+id+"/tokens", `{"name":"old"}`, http.StatusCreated)
	api.json(http.MethodDelete, "/api/v1/integrations/"+id+"/tokens/"+second["token"].(map[string]any)["id"].(string),
		"", http.StatusNoContent)
	for _, tt := range []struct {
		method, target, token, body string
		want                        int
	}{
		{http.MethodGet, r.Ingest + "/api/v1/heartbeat", token, "", http.StatusNoContent},
		{http.MethodPost, r.Ingest + "/api/v1/heartbeat/" + token, "", "anything", http.StatusNoContent},
		{http.MethodPost, r.Ingest + "/api/v1/heartbeat", "mstr_int_wrong", "", http.StatusUnauthorized},
		{http.MethodPost, r.Ingest + "/api/v1/heartbeat", pat, "", http.StatusUnauthorized},
		{http.MethodPost, r.Ingest + "/api/v1/heartbeat/" + second["value"].(string), "", "", http.StatusUnauthorized},
	} {
		if s := signal(t, tt.method, tt.target, tt.token, tt.body); s != tt.want {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.target, s, tt.want)
		}
	}
	if s := state(); s != "live" {
		t.Fatalf("after the first signal: %s", s)
	}

	// C-07.AC-1 and C-07.FR-8: lost after the timeout, with the Static labels; the next signal resolves it.
	advance(t, r, 360)
	eventually(t, "the heartbeat to be lost", func() bool { return state() == "lost" })
	m := get()
	w := m["warnings"].([]any)
	if len(w) != 1 || w[0].(map[string]any)["kind"] != "heartbeat_lost" || w[0].(map[string]any)["since"] == nil {
		t.Errorf("lost warnings %v", w)
	}
	idle()
	raised := lost()
	want := map[string]string{"alertname": "MusterHeartbeatLost", "env": "prod", "integration": id,
		"integration_name": "hb", "severity": "critical"}
	if len(raised) != 1 || len(raised[0].Labels) != len(want) {
		t.Fatalf("raised %+v", raised)
	}
	for k, v := range want {
		if raised[0].Labels[k] != v {
			t.Errorf("label %s = %q", k, raised[0].Labels[k])
		}
	}
	eventually(t, "muster_heartbeat_lost to be 1 on the Leader", func() bool { return lostMetric() == 1 })
	hb()
	idle()
	if n := len(lost()); n != 0 || state() != "live" {
		t.Errorf("after the signal: %d firing, %s", n, state())
	}
	advance(t, r, 0)
	eventually(t, "muster_heartbeat_lost to be 0", func() bool { return lostMetric() == 0 })

	// C-07.AC-4: the group of q last arrives at T; one signal a minute.
	// The business clock also runs in real time, so the checks keep a minute of margin each side of T+15 min.
	notify(`{"reason":"repeat interval elapsed"}`)
	for range 14 {
		advance(t, r, 60)
		hb()
	}
	time.Sleep(time.Second) // the woken Stale scan, which must resolve nothing yet
	if got := alerts(id, "state=firing&"+q); len(got) != 1 {
		t.Fatalf("at T+14 min: %+v", got)
	}
	for range 2 {
		advance(t, r, 60)
		hb()
	}
	eventually(t, "q to go stale", func() bool {
		got := alerts(id, "state=resolved&"+q)
		return len(got) == 1 && got[0].Reason != nil && *got[0].Reason == "stale"
	})
	// Again with a loss: q fires again at T2, and no signal comes from T2 to T2+10 min.
	notify(`{"reason":"first notification"}`)
	advance(t, r, 600)
	eventually(t, "the heartbeat to be lost", func() bool { return state() == "lost" })
	hb()
	for range 14 {
		advance(t, r, 60)
		hb()
	}
	time.Sleep(time.Second)
	if got := alerts(id, "state=firing&"+q); len(got) != 1 {
		t.Fatalf("at T2+24 min: %+v", got)
	}
	for range 2 {
		advance(t, r, 60)
		hb()
	}
	eventually(t, "q to go stale again", func() bool { return len(alerts(id, "state=firing&"+q)) == 0 })

	// C-07.FR-6: the Heartbeat snippet.
	snippet, _ := api.json(http.MethodPost, "/api/v1/integrations/"+id+"/tokens", `{"name":"hb-2"}`,
		http.StatusCreated)["heartbeat_snippet"].(string)
	for _, line := range []string{"expr: vector(1)", "repeat_interval: 1m", "- url: " + devmode.IngestURL + "/api/v1/heartbeat\n",
		"send_resolved: false"} {
		if !strings.Contains(snippet, line) {
			t.Errorf("the snippet lacks %q:\n%s", line, snippet)
		}
	}

	// C-07.AC-5: the deletion resolves MusterHeartbeatLost.
	advance(t, r, 360)
	eventually(t, "MusterHeartbeatLost to fire", func() bool { idle(); return len(lost()) == 1 })
	api.json(http.MethodDelete, "/api/v1/integrations/"+id, "", http.StatusNoContent)
	eventually(t, "MusterHeartbeatLost to resolve", func() bool { idle(); return len(lost()) == 0 })

	// No log line carries a token, from a path or a header.
	for _, rr := range h.Replicas {
		if out := rr.Output(); strings.Contains(out, token) || strings.Contains(out, second["value"].(string)) {
			t.Error("an integration token reached the log")
		}
	}
}
