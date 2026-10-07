// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
)

// TestCommands is S-032 against `muster dev`, with E2E_REPLICAS=2 on two replicas, the reads on the second one: a
// Viewer's Acknowledge refused by the dispatcher with only a log line (C-10.AC-4), two users acknowledging at once
// leaving one Owner and a single Takeover (C-10.AC-3), the Audit log of Commands made through the API (C-10.AC-5),
// Resolve and Unresolve with the Grace period (C-10.FR-7), and the median time to acknowledge of a Route whose second
// Alert Group was unacknowledged, acknowledged again, taken over and reopened, and whose third was resolved without an
// acknowledgement (C-10.AC-14).
func TestCommands(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	reader := h.Replicas[len(h.Replicas)-1]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"commands","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	pat := patClient{t: t, base: r.App, token: token}
	read := func(path string) map[string]any {
		t.Helper()
		return (patClient{t: t, base: reader.App, token: token}).json(http.MethodGet, path, "", http.StatusOK)
	}
	// Bob, a Responder, signed in, and a Service account with the Role viewer.
	created := admin.json(http.MethodPost, "/api/v1/users", `{"name":"bob","login":"bob","role":"responder"}`,
		http.StatusCreated)
	link, _ := url.Parse(created["password_setup_link"].(map[string]any)["url"].(string))
	setup, _ := strings.CutPrefix(link.Fragment, "token=")
	if a := admin.do(http.MethodPost, "/api/v1/password-setups", `{"token":"`+setup+`","password":"bob-password-1"}`); a.status !=
		http.StatusNoContent {
		t.Fatalf("password setup = %d %s", a.status, a.body)
	}
	bob := newAgent(t, r.App)
	if a := bob.signIn("bob", "bob-password-1"); a.status != http.StatusCreated {
		t.Fatalf("bob = %d %s", a.status, a.body)
	}
	watcher := admin.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"watcher","role":"viewer"}`,
		http.StatusCreated)["id"].(string)
	viewer := patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/service-accounts/"+watcher+
		"/tokens", `{"name":"watcher"}`, http.StatusCreated)["value"].(string)}

	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"cmd","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	intID := in["id"].(string)
	intToken := admin.json(http.MethodPost, "/api/v1/integrations/"+intID+"/tokens", `{"name":"cmd"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"cmd","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		intToken+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	var profiles struct {
		Items []struct {
			Policy json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, admin.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	newRoute := func(team string) string {
		return admin.json(http.MethodPost, "/api/v1/routes", `{"name":"`+team+`","matchers":[{"label":"team",`+
			`"op":"=","value":"`+team+`"}],"urgent":false,"group_key":["alertname","cluster"],"destination_ids":[],`+
			`"policy":`+string(profiles.Items[0].Policy)+`}`, http.StatusCreated)["id"].(string)
	}
	newRoute("ops")
	stats := newRoute("stats")
	put := func(path, body string) {
		t.Helper()
		if a := call(t, http.MethodPut, fam+path, body); a.status/100 != 2 {
			t.Fatalf("PUT %s = %d %s", path, a.status, a.body)
		}
	}
	processed := func() int64 {
		return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`)
	}
	notify := func(reason string) {
		t.Helper()
		before := processed()
		if a := call(t, http.MethodPost, fam+"/groups/o1/notify", `{"reason":"`+reason+`"}`); a.status != http.StatusOK {
			t.Fatalf("notify = %d %s", a.status, a.body)
		}
		eventually(t, "the snapshot to be processed", func() bool { return processed() > before })
	}
	alert := func(team, name, cluster, extra string) {
		put("/groups/o1/alerts/"+name, `{"labels":{"team":"`+team+`","cluster":"`+cluster+`","severity":"warning",`+
			`"n":"`+name+`"}`+extra+`}`)
	}
	groupOf := func(cluster string) string {
		t.Helper()
		items := read("/api/v1/integrations/" + intID + "/alerts?" + url.Values{"label": {`cluster="` + cluster +
			`"`}}.Encode())["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no alert of cluster %s", cluster)
		}
		return items[0].(map[string]any)["alert_group"].(map[string]any)["id"].(string)
	}
	command := func(id, cmd, body string, want int) map[string]any {
		t.Helper()
		return pat.json(http.MethodPost, "/api/v1/alert-groups/"+id+"/"+cmd, body, want)
	}
	events := func(id, event string) int {
		t.Helper()
		n := 0
		for _, e := range read("/api/v1/alert-groups/" + id + "/timeline?limit=500")["items"].([]any) {
			if e.(map[string]any)["event"] == event {
				n++
			}
		}
		return n
	}

	put("/groups/o1", `{"receiver":"cmd","route":"{}","labels":{"alertname":"QueueFull"}}`)
	for _, c := range []string{"a", "b", "c"} {
		alert("ops", c, c, "")
	}
	notify("first notification")
	ga, gb, gc := groupOf("a"), groupOf("b"), groupOf("c")

	// C-10.AC-4: the Viewer's Acknowledge reaches the dispatcher, which refuses it and writes only the log line.
	if a := viewer.do(http.MethodPost, "/api/v1/alert-groups/"+ga+"/acknowledge", ""); a.status != http.StatusForbidden ||
		!strings.Contains(string(a.body), "alert-groups:acknowledge") {
		t.Errorf("viewer = %d %s", a.status, a.body)
	}
	if line, _, ok := r.LogLine("command_refused"); !ok || line["code"] != "forbidden" || line["command"] != "acknowledge" ||
		line["group"] != ga {
		t.Errorf("command_refused = %v", line)
	}
	if n := h.count(t, `SELECT count(*) FROM audit_log WHERE action LIKE 'alert_group.%'`); n != 0 {
		t.Errorf("%d audit entries after a refusal", n)
	}

	// C-10.AC-3: two users acknowledging at once leave one Owner and a single Takeover.
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i, send := range []func() (*http.Request, error){
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, r.App+"/api/v1/alert-groups/"+gb+
				"/acknowledge", nil)
			if err == nil {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			return req, err
		},
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, r.App+"/api/v1/alert-groups/"+gb+
				"/acknowledge", nil)
			if err == nil {
				for name, value := range bob.cookies {
					req.AddCookie(&http.Cookie{Name: name, Value: value}) //nolint:gosec // G124: a request cookie carries no attributes
				}
				req.Header.Set("X-CSRF-Token", bob.csrf)
			}
			return req, err
		},
	} {
		wg.Go(func() {
			req, err := send()
			if err != nil {
				return
			}
			resp, err := noFollow.Do(req)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			statuses[i] = resp.StatusCode
		})
	}
	wg.Wait()
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusOK {
		t.Fatalf("concurrent acknowledges = %v", statuses)
	}
	if n, m := events(gb, "takeover"), events(gb, "acknowledged"); n != 1 || m != 1 {
		t.Errorf("%d takeovers and %d acknowledgements", n, m)
	}
	v := read("/api/v1/alert-groups/" + gb)
	owner, _ := v["owner"].(map[string]any)
	if v["status"] != "acknowledged" || owner == nil {
		t.Fatalf("after the race = %v", v)
	}
	for _, e := range read("/api/v1/alert-groups/" + gb + "/timeline?limit=500")["items"].([]any) {
		if e := e.(map[string]any); e["event"] == "takeover" && e["actor"].(map[string]any)["id"] != owner["id"] {
			t.Errorf("the later one %v is not the owner %v", e["actor"], owner)
		}
	}

	// C-10.AC-5 and C-10.FR-7: Resolve and Unresolve through the API, each once in the Audit log.
	if v := command(gc, "resolve", `{}`, http.StatusOK)["alert_group"].(map[string]any); v["status"] != "resolved" ||
		!strings.Contains(fmt.Sprint(v["allowed_commands"]), "unresolve") {
		t.Fatalf("resolve = %v", v)
	}
	if n := h.count(t, `SELECT count(*) FROM timers WHERE kind = 'grace_period_end'`); n != 1 {
		t.Errorf("%d grace period timers", n)
	}
	if v := command(gc, "unresolve", "", http.StatusOK)["alert_group"].(map[string]any); v["status"] != "firing" ||
		v["owner"] != nil {
		t.Errorf("unresolve = %v", v)
	}
	if n := h.count(t, `SELECT count(*) FROM timers WHERE kind = 'grace_period_end'`); n != 0 {
		t.Errorf("%d grace period timers after unresolve", n)
	}
	for _, action := range []string{"alert_group.resolved", "alert_group.unresolved"} {
		items := read("/api/v1/audit-log?action=" + action)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["transport"] != "api" ||
			items[0].(map[string]any)["token_name"] != "commands" {
			t.Errorf("%s = %v", action, items)
		}
	}

	// C-10.AC-14: three new Alert Groups of another Route start together; the first is acknowledged after 5 minutes, the
	// second after 15, then unacknowledged, acknowledged again, taken over and reopened; the third is resolved
	// without an acknowledgement.
	for _, c := range []string{"s1", "s2", "s3"} {
		alert("stats", c, c, "")
	}
	notify("new alerts added")
	s1, s2, s3 := groupOf("s1"), groupOf("s2"), groupOf("s3")
	advance(t, r, 300)
	command(s1, "acknowledge", "", http.StatusOK)
	advance(t, r, 600)
	command(s2, "acknowledge", "", http.StatusOK)
	command(s2, "unacknowledge", "", http.StatusOK)
	advance(t, r, 60)
	command(s2, "acknowledge", "", http.StatusOK)
	bob.signIn("bob", "bob-password-1")
	if a := bob.do(http.MethodPost, "/api/v1/alert-groups/"+s2+"/acknowledge", ""); a.status != http.StatusOK {
		t.Fatalf("takeover = %d %s", a.status, a.body)
	}
	alert("stats", "s2", "s2", `,"status":"resolved"`)
	notify("some alerts resolved")
	advance(t, r, 120)
	alert("stats", "s2b", "s2", "")
	notify("new alerts added")
	if v := read("/api/v1/alert-groups/" + s2); v["status"] != "acknowledged" || v["reopen_count"] != 1.0 {
		t.Errorf("s2 after the reopen = %v", v)
	}
	command(s3, "resolve", `{}`, http.StatusOK)
	st := read("/api/v1/alert-group-statistics?group_by=route&route=" + stats)
	item := st["items"].([]any)[0].(map[string]any)
	ack := item["time_to_acknowledge"].(map[string]any)
	if ack["count"] != 2.0 || ack["median_seconds"] != 600.0 {
		t.Errorf("time to acknowledge = %v", ack)
	}
	t.Logf("statistics of the route: alert_group_count=%v time_to_acknowledge=%v", item["alert_group_count"], ack)
}
