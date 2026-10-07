// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/devmode"
)

// TestLifecycle is S-028 against `muster dev`, with E2E_REPLICAS=2 on two replicas: Alerts of one Route and Group key
// join one Alert Group (C-09.AC-6), a Replacement (C-09.AC-4), a Continuation (C-09.AC-10), a rise to Urgent
// (C-09.AC-12), the title switch (C-09.AC-20), Reopens by the same and another fingerprint and a new #N after the
// window, on the development clock (C-09.AC-1, AC-22, AC-2), configuration edits that leave open Alert Groups alone
// (C-09.AC-7, AC-8), the downtime entries (C-09.AC-11), the refusal to delete a Route with open Alert Groups and the
// move (C-09.AC-9), and the resolution when the Integration is deleted (C-09.AC-14). With two replicas, the reads go
// to the second one.
func TestLifecycle(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	reader := h.Replicas[len(h.Replicas)-1]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	// A Personal access token reads on every replica and survives the clock moves.
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"lifecycle","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	read := func(path string) map[string]any {
		t.Helper()
		a := (patClient{t: t, base: reader.App, token: token}).do(http.MethodGet, path, "")
		if a.status != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, a.status, a.body)
		}
		var m map[string]any
		decode(t, a, &m)
		return m
	}
	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"grp","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	intID := in["id"].(string)
	intToken := admin.json(http.MethodPost, "/api/v1/integrations/"+intID+"/tokens", `{"name":"grp"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"grp","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		intToken+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	var profiles struct {
		Items []struct {
			ID     string          `json:"id"`
			Policy json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, admin.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	policy := string(profiles.Items[0].Policy)
	route := func(name, team, key string) string {
		return admin.json(http.MethodPost, "/api/v1/routes", `{"name":"`+name+`","matchers":[{"label":"team","op":"=",`+
			`"value":"`+team+`"}],"urgent":false,"group_key":`+key+`,"destination_ids":[],"policy":`+policy+`}`,
			http.StatusCreated)["id"].(string)
	}
	rdb := route("db", "db", `["alertname","cluster"]`)
	route("net", "net", `["cluster"]`)
	put := func(path, body string) {
		t.Helper()
		if a := call(t, http.MethodPut, fam+path, body); a.status/100 != 2 {
			t.Fatalf("PUT %s = %d %s", path, a.status, a.body)
		}
	}
	processed := func() int64 {
		return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`)
	}
	notify := func(group, reason string) {
		t.Helper()
		before := processed()
		if a := call(t, http.MethodPost, fam+"/groups/"+group+"/notify", `{"reason":"`+reason+`"}`); a.status !=
			http.StatusOK {
			t.Fatalf("notify %s = %d %s", group, a.status, a.body)
		}
		eventually(t, "the snapshot to be processed", func() bool { return processed() > before })
	}
	groupOf := func(label string) string {
		t.Helper()
		page := read("/api/v1/integrations/" + intID + "/alerts?" + url.Values{"label": {label}}.Encode())
		items := page["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no alert %s", label)
		}
		g, _ := items[0].(map[string]any)["alert_group"].(map[string]any)
		if g == nil {
			t.Fatalf("alert %s is in no alert group", label)
		}
		return g["id"].(string)
	}
	group := func(id string) map[string]any { return read("/api/v1/alert-groups/" + id) }
	last := func(id string) map[string]any {
		t.Helper()
		items := read("/api/v1/alert-groups/" + id + "/timeline?limit=1")["items"].([]any)
		return items[0].(map[string]any)
	}
	alert := func(labels, extra string) string { return `{"labels":{` + labels + `}` + extra + `}` }
	db := func(cluster, pod, severity string) string {
		return `"team":"db","cluster":"` + cluster + `","pod":"` + pod + `","severity":"` + severity + `"`
	}

	// C-09.AC-6: one Alert Group per key; a missing label is an empty value.
	put("/groups/d1", `{"receiver":"grp","route":"{}","labels":{"alertname":"DiskFull"}}`)
	put("/groups/d1/alerts/i1", alert(db("a", "i1", "warning"), `,"annotations":{"summary":"Disk on i1 is full"}`))
	put("/groups/d1/alerts/i2", alert(db("a", "i2", "warning"), ""))
	put("/groups/d1/alerts/j1", alert(db("b", "j1", "warning"), ""))
	notify("d1", "first notification")
	g1, g2 := groupOf(`pod="i1"`), groupOf(`pod="j1"`)
	if groupOf(`pod="i2"`) != g1 || g1 == g2 {
		t.Fatalf("groups %s %s %s", g1, groupOf(`pod="i2"`), g2)
	}
	v := group(g1)
	if v["number"] != 1.0 || v["title"] != "DiskFull" || v["summary"] != "Disk on i1 is full" || v["status"] != "firing" ||
		v["severity_level"] != "warning" || v["urgent"] != false || v["route"].(map[string]any)["name"] != "db" ||
		v["firing_alert_count"] != 2.0 {
		t.Errorf("g1 = %v", v)
	}
	if e := last(g1); e["event"] != "created" || e["loudness"] != "loud" ||
		!slices.Equal(names(e["mentions"]), []string{"new_alert_group"}) || len(e["fingerprints"].([]any)) != 2 {
		t.Errorf("created = %v", e)
	}

	// C-09.AC-4: a Replacement differing only in pod.
	if a := call(t, http.MethodDelete, fam+"/groups/d1/alerts/i2", ""); a.status/100 != 2 {
		t.Fatalf("delete i2 = %d", a.status)
	}
	put("/groups/d1/alerts/i2b", alert(db("a", "i2b", "warning"), ""))
	notify("d1", "new alerts added")
	if e := last(g1); e["event"] != "alert_replaced" || e["loudness"] != "quiet" || e["replaced_label"] != "pod" {
		t.Errorf("replaced = %v", e)
	}
	if n := group(g1)["notices"].([]any); len(n) != 1 || n[0].(map[string]any)["kind"] != "replacement" {
		t.Errorf("notices = %v", n)
	}

	// C-09.AC-10: a Continuation.
	put("/groups/d1/alerts/i1", alert(db("a", "i1", "warning"), `,"starts_at":"now"`))
	notify("d1", "repeat interval elapsed")
	if e := last(g1); e["event"] != "alert_continued" || e["loudness"] != "quiet" || len(names(e["mentions"])) != 0 {
		t.Errorf("continued = %v", e)
	}

	// C-09.AC-12: a rise to critical makes the firing Alert Group Urgent, quietly.
	put("/groups/d1/alerts/i3", alert(db("a", "i3", "critical"), ""))
	notify("d1", "new alerts added")
	if v := group(g1); v["severity_level"] != "critical" || v["urgent"] != true || v["status"] != "firing" {
		t.Errorf("after the rise = %v", v)
	}
	if e := last(g1); e["event"] != "urgency_raised" || e["loudness"] != "quiet" {
		t.Errorf("urgency_raised = %v", e)
	}

	// C-09.AC-20: the title switches once in a Route whose key lacks alertname.
	put("/groups/n1", `{"receiver":"grp","route":"{}","labels":{"alertname":"LinkDown"}}`)
	put("/groups/n2", `{"receiver":"grp","route":"{}","labels":{"alertname":"HighLatency"}}`)
	put("/groups/n1/alerts/l1", alert(`"team":"net","cluster":"x","if":"eth0"`, `,"annotations":{"summary":"eth0 is down"}`))
	notify("n1", "first notification")
	gn := groupOf(`if="eth0"`)
	if group(gn)["title"] != "LinkDown" {
		t.Errorf("title %v", group(gn)["title"])
	}
	put("/groups/n2/alerts/h1", alert(`"team":"net","cluster":"x","if":"eth1"`, ""))
	notify("n2", "first notification")
	if v := group(gn); v["title"] != "cluster=x" || v["summary"] != "eth0 is down" {
		t.Errorf("after the switch %v", v)
	}

	// C-09.AC-1, AC-22, AC-2: Reopen by the same and another fingerprint, then a new #N after the window.
	put("/groups/d1/alerts/j1", alert(db("b", "j1", "warning"), `,"status":"resolved"`))
	notify("d1", "some alerts resolved")
	if v := group(g2); v["status"] != "resolved" || v["resolution"].(map[string]any)["by"] != "system" ||
		v["resolution"].(map[string]any)["reason_code"] != "resolved" {
		t.Errorf("resolved = %v", v)
	}
	advance(t, r, 480)
	put("/groups/d1/alerts/j1", alert(db("b", "j1", "warning"), `,"status":"firing","starts_at":"now"`))
	notify("d1", "new alerts added")
	if v := group(g2); v["number"] != 2.0 || v["status"] != "firing" || v["reopen_count"] != 1.0 {
		t.Errorf("reopened = %v", v)
	}
	if e := last(g2); e["event"] != "reopened" || e["loudness"] != "loud" || !slices.Equal(names(e["mentions"]),
		[]string{"reopen"}) {
		t.Errorf("reopened = %v", e)
	}
	put("/groups/d1/alerts/j1", alert(db("b", "j1", "warning"), `,"status":"resolved"`))
	notify("d1", "some alerts resolved")
	advance(t, r, 60)
	put("/groups/d1/alerts/j2", alert(db("b", "j2", "warning"), ""))
	notify("d1", "new alerts added")
	if groupOf(`pod="j2"`) != g2 || group(g2)["reopen_count"] != 2.0 {
		t.Errorf("another fingerprint did not reopen: %v", group(g2))
	}
	put("/groups/d1/alerts/j1", alert(db("b", "j1", "warning"), `,"status":"resolved"`))
	put("/groups/d1/alerts/j2", alert(db("b", "j2", "warning"), `,"status":"resolved"`))
	notify("d1", "some alerts resolved")
	advance(t, r, 960)
	// The Reopen window ended on the timer worker of a replica, woken by the clock move.
	eventually(t, "the reopen window to end", func() bool {
		return h.count(t, `SELECT count(*) FROM timers WHERE kind = 'reopen_window_end'`) == 0
	})
	put("/groups/d1/alerts/j3", alert(db("b", "j3", "warning"), ""))
	notify("d1", "new alerts added")
	if v := group(groupOf(`pod="j3"`)); v["number"].(float64) <= 2 || v["status"] != "firing" {
		t.Errorf("after the window = %v", v)
	}

	// C-09.AC-7 and C-09.AC-8: new Matchers and the urgent mark leave open Alert Groups alone.
	rt := admin.do(http.MethodGet, "/api/v1/routes/"+rdb, "")
	var saved map[string]any
	decode(t, rt, &saved)
	saved["matchers"] = []map[string]string{{"label": "team", "op": "=", "value": "dba"}}
	saved["urgent"] = true
	saved["destination_ids"] = []string{}
	body, _ := json.Marshal(map[string]any{"name": saved["name"], "matchers": saved["matchers"], "urgent": true,
		"group_key": saved["group_key"], "destination_ids": []string{}, "policy": saved["policy"]})
	if a := admin.putWithIfMatch("/api/v1/routes/"+rdb, string(body), rt.header.Get("ETag")); a.status != http.StatusOK {
		t.Fatalf("update = %d %s", a.status, a.body)
	}
	notify("d1", "repeat interval elapsed")
	if groupOf(`pod="i1"`) != g1 || group(g1)["status"] != "firing" {
		t.Errorf("an edit changed g1: %v", group(g1))
	}

	// C-09.AC-11: downtime, simulated as in S-008; the replicas, and with the first one the fakes, start again.
	for _, x := range h.Replicas {
		x.Stop(t)
	}
	conn, err := pgx.Connect(t.Context(), h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	if _, err := conn.Exec(t.Context(), "UPDATE runtime_state SET alive_at = alive_at - interval '10 minutes'"); err != nil {
		t.Fatal(err)
	}
	for _, x := range h.Replicas {
		x.Start(t)
	}
	eventually(t, "the downtime entries", func() bool {
		return h.count(t, `SELECT count(*) FROM timeline_entries WHERE system_event = 'muster_unavailable'`) > 0
	})
	items := read("/api/v1/alert-groups/" + g1 + "/timeline?kind=system")["items"].([]any)
	if e := items[0].(map[string]any); e["system_event"] != "muster_unavailable" || e["period_from"] == nil ||
		e["period_to"] == nil {
		t.Errorf("downtime = %v", e)
	}
	if n := h.count(t, `SELECT count(*) FROM alert_groups WHERE status <> 'resolved'`); n != h.count(t,
		`SELECT count(*) FROM timeline_entries WHERE system_event = 'muster_unavailable'`) {
		t.Errorf("%d open alert groups and another number of downtime entries", n)
	}

	// C-09.AC-9: a Route with open Alert Groups.
	a := admin.do(http.MethodDelete, "/api/v1/routes/"+rdb, "")
	var p map[string]any
	decode(t, a, &p)
	if a.status != http.StatusConflict || !strings.HasSuffix(p["type"].(string),
		"/route-has-open-alert-groups") || p["open_alert_group_count"] != 2.0 {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	if m := admin.json(http.MethodPost, "/api/v1/routes/"+rdb+"/move-open-alert-groups", "", http.StatusOK); m["moved"] !=
		2.0 {
		t.Errorf("moved = %v", m)
	}
	if v := group(g1); v["route"].(map[string]any)["name"] != "Default" {
		t.Errorf("g1 on %v", v["route"])
	}
	if e := last(g1); e["event"] != "moved_to_default_route" || e["loudness"] != "quiet" {
		t.Errorf("moved = %v", e)
	}
	if a := admin.do(http.MethodDelete, "/api/v1/routes/"+rdb, ""); a.status != http.StatusNoContent {
		t.Errorf("delete after the move = %d %s", a.status, a.body)
	}

	// C-09.AC-14: deleting the Integration resolves its open Alert Groups.
	if a := admin.do(http.MethodDelete, "/api/v1/integrations/"+intID, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete integration = %d %s", a.status, a.body)
	}
	eventually(t, "the alert group to resolve", func() bool { return group(g1)["status"] == "resolved" })
	if res := group(g1)["resolution"].(map[string]any); res["reason"] != "Integration grp deleted" ||
		res["reason_code"] != "integration_deleted" {
		t.Errorf("resolution = %v", res)
	}
	if h.count(t, `SELECT max(number) - count(*) FROM alert_groups`) != 0 {
		t.Error("gaps in #N")
	}
}

// names are the strings of a JSON list.
func names(v any) []string {
	out := []string{}
	list, _ := v.([]any)
	for _, x := range list {
		out = append(out, x.(string))
	}
	return out
}
