// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
)

// TestRouting is S-025 against `muster dev`, with E2E_REPLICAS=2 on two replicas: the Route profiles (C-08.AC-11),
// the first matching Route taking each newly firing Alert before and after a reorder (C-08.AC-1), the refusals of a
// stale ETag, of the Default route and of another set (C-08.AC-6, AC-7, AC-11), invalid regular expressions
// (C-08.AC-10) and templates (C-12.AC-2), Severity levels (C-08.AC-9), a deletion leaving the evaluation order at once,
// muster_route_info on every replica (C-08.FR-12) and the Routes recorded on the Stored Snapshots.
func TestRouting(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"lab","connection_mode":"webhook_only",
		"static_labels":{"cluster":"b"},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`,
		http.StatusCreated)
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
	processed := func() int64 {
		return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`)
	}
	notify := func(group, body string) {
		t.Helper()
		before := processed()
		if a := call(t, http.MethodPost, fam+"/groups/"+group+"/notify", body); a.status != http.StatusOK {
			t.Fatalf("notify %s = %d %s", group, a.status, a.body)
		}
		eventually(t, "the snapshot to be processed", func() bool { return processed() > before })
	}
	type alert struct {
		Labels map[string]string `json:"labels"`
		Route  *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"route"`
		Level *string `json:"severity_level"`
		Raw   *string `json:"severity_raw"`
	}
	view := func(matchers ...string) []alert {
		t.Helper()
		q := url.Values{"label": matchers}
		a := admin.do(http.MethodGet, "/api/v1/integrations/"+id+"/alerts?"+q.Encode(), "")
		var page struct {
			Items []alert `json:"items"`
		}
		if a.status != http.StatusOK {
			t.Fatalf("view = %d %s", a.status, a.body)
		}
		decode(t, a, &page)
		return page.Items
	}
	routeOf := func(matchers ...string) string {
		t.Helper()
		items := view(matchers...)
		if len(items) != 1 || items[0].Route == nil {
			t.Fatalf("view %v = %+v", matchers, items)
		}
		return items[0].Route.Name
	}
	type route struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		IsDefault bool   `json:"is_default"`
		Etag      string `json:"etag"`
	}
	routes := func() ([]route, string) {
		t.Helper()
		a := admin.do(http.MethodGet, "/api/v1/routes", "")
		var list struct {
			Items []route `json:"items"`
		}
		if a.status != http.StatusOK {
			t.Fatalf("routes = %d %s", a.status, a.body)
		}
		decode(t, a, &list)
		return list.Items, a.header.Get("ETag")
	}
	routeNames := func() string {
		list, _ := routes()
		var out []string
		for _, rt := range list {
			out = append(out, rt.Name)
		}
		return strings.Join(out, ",")
	}
	problem := func(a answer) (int, string, string, string) {
		var p struct {
			Type   string `json:"type"`
			Errors []struct {
				Pointer string `json:"pointer"`
				Code    string `json:"code"`
			} `json:"errors"`
		}
		decode(t, a, &p)
		pointer, code := "", ""
		if len(p.Errors) > 0 {
			pointer, code = p.Errors[0].Pointer, p.Errors[0].Code
		}
		return a.status, p.Type[strings.LastIndex(p.Type, "/")+1:], pointer, code
	}

	// C-08.AC-11: the profiles.
	var profiles struct {
		Items []struct {
			ID     string          `json:"id"`
			Policy json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, admin.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	var onCall json.RawMessage
	for _, p := range profiles.Items {
		var pol struct {
			Snooze     []int `json:"snooze_durations_seconds"`
			AckTimeout struct {
				Enabled bool `json:"enabled"`
			} `json:"ack_timeout"`
			Reminders struct {
				Enabled bool `json:"enabled"`
			} `json:"reminders"`
		}
		if err := json.Unmarshal(p.Policy, &pol); err != nil {
			t.Fatal(err)
		}
		switch p.ID {
		case "on_call":
			onCall = p.Policy
			if !slices.Equal(pol.Snooze, []int{3600, 14400, 86400}) || !pol.AckTimeout.Enabled || !pol.Reminders.Enabled {
				t.Errorf("on-call %s", p.Policy)
			}
		case "informational":
			if !slices.Equal(pol.Snooze, []int{86400, 259200, 604800}) || pol.AckTimeout.Enabled || pol.Reminders.Enabled {
				t.Errorf("informational %s", p.Policy)
			}
		default:
			t.Errorf("profile %s", p.ID)
		}
	}
	mk := func(name, matchers string, policy json.RawMessage) answer {
		return admin.do(http.MethodPost, "/api/v1/routes", `{"name":"`+name+`","matchers":`+matchers+
			`,"urgent":false,"group_key":["alertname"],"destination_ids":[],"policy":`+string(policy)+`}`)
	}
	created := func(a answer) route {
		t.Helper()
		var rt route
		if a.status != http.StatusCreated {
			t.Fatalf("create = %d %s", a.status, a.body)
		}
		decode(t, a, &rt)
		return rt
	}
	ra := created(mk("A", `[{"label":"severity","op":"=","value":"critical"}]`, onCall))
	rb := created(mk("B", `[{"label":"team","op":"=","value":"x"}]`, onCall))
	if got := routeNames(); got != "A,B,Default" {
		t.Fatalf("routes %s", got)
	}

	// C-08.AC-1: the first Route takes the Alert; after the reorder the next one goes to the other.
	put("/groups/r1", `{"receiver":"lab","route":"{}","labels":{"alertname":"Both"}}`)
	put("/groups/r1/alerts/one", `{"labels":{"severity":"critical","team":"x","n":"1"}}`)
	notify("r1", `{"reason":"first notification"}`)
	if got := routeOf(`alertname="Both"`, `n="1"`); got != "A" {
		t.Errorf("first alert to %s", got)
	}
	_, etag := routes()
	order := `{"route_ids":["` + rb.ID + `","` + ra.ID + `"]}`
	if a := admin.putWithIfMatch("/api/v1/route-order", order, etag); a.status != http.StatusOK {
		t.Fatalf("reorder = %d %s", a.status, a.body)
	}
	if got := routeNames(); got != "B,A,Default" {
		t.Errorf("after the reorder %s", got)
	}
	put("/groups/r1/alerts/two", `{"labels":{"severity":"critical","team":"x","n":"2"}}`)
	notify("r1", `{"reason":"new alerts added"}`)
	if got := routeOf(`alertname="Both"`, `n="2"`); got != "B" {
		t.Errorf("second alert to %s", got)
	}
	if got := routeOf(`alertname="Both"`, `n="1"`); got != "A" {
		t.Errorf("first alert moved to %s", got)
	}

	// C-08.AC-7, AC-11 and AC-6.
	if a := admin.putWithIfMatch("/api/v1/route-order", order, etag); a.status != http.StatusPreconditionFailed {
		t.Errorf("stale reorder = %d %s", a.status, a.body)
	}
	list, etag2 := routes()
	def := list[2]
	if s, typ, _, _ := problem(admin.putWithIfMatch("/api/v1/route-order", `{"route_ids":["`+rb.ID+`","`+ra.ID+`","`+
		def.ID+`"]}`, etag2)); s != http.StatusConflict || typ != "default-route-immutable" {
		t.Errorf("reorder with the default route = %d %s", s, typ)
	}
	if s, _, pointer, code := problem(admin.putWithIfMatch("/api/v1/route-order", `{"route_ids":["`+ra.ID+`"]}`,
		etag2)); s != http.StatusUnprocessableEntity || code != "route_set_mismatch" || pointer != "/route_ids" {
		t.Errorf("another set = %d %s %s", s, pointer, code)
	}
	if s, typ, _, _ := problem(admin.do(http.MethodDelete, "/api/v1/routes/"+def.ID, "")); s != http.StatusConflict ||
		typ != "default-route-immutable" {
		t.Errorf("delete the default route = %d %s", s, typ)
	}
	a := admin.do(http.MethodGet, "/api/v1/routes/"+ra.ID, "")
	var full map[string]any
	decode(t, a, &full)
	body, _ := json.Marshal(map[string]any{"name": full["name"], "matchers": full["matchers"], "urgent": true,
		"group_key": full["group_key"], "destination_ids": []string{}, "policy": full["policy"]})
	if a = admin.putWithIfMatch("/api/v1/routes/"+ra.ID, string(body), a.header.Get("ETag")); a.status != http.StatusOK ||
		!strings.Contains(string(a.body), `"urgent":true`) {
		t.Errorf("update = %d %s", a.status, a.body)
	}
	if a = admin.putWithIfMatch("/api/v1/routes/"+ra.ID, string(body), full["etag"].(string)); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("stale update = %d", a.status)
	}

	// C-08.AC-10, and a template that calls an unregistered function (C-12.AC-2).
	if s, _, pointer, code := problem(mk("C", `[{"label":"pod","op":"=~","value":"api-("}]`, onCall)); s !=
		http.StatusUnprocessableEntity || pointer != "/matchers/0/value" || code != "invalid_regex" {
		t.Errorf("invalid regex = %d %s %s", s, pointer, code)
	}
	templated := strings.Replace(string(onCall), `"root_message":null`, `"root_message":"{{ env \"HOME\" }}"`, 1)
	if s, _, pointer, code := problem(mk("T", `[]`, json.RawMessage(templated))); s != http.StatusUnprocessableEntity ||
		pointer != "/policy/templates/root_message" || code != "unknown_function" {
		t.Errorf("template = %d %s %s", s, pointer, code)
	}

	// C-08.AC-9: Severity levels.
	put("/groups/r2", `{"receiver":"lab","route":"{}","labels":{"alertname":"Sev"}}`)
	put("/groups/r2/alerts/n", `{"labels":{"severity":"none","k":"none"}}`)
	put("/groups/r2/alerts/p", `{"labels":{"severity":"P5","k":"p5"}}`)
	put("/groups/r2/alerts/m", `{"labels":{"k":"missing"}}`)
	notify("r2", `{"reason":"first notification"}`)
	got := map[string]string{}
	for _, al := range view(`alertname="Sev"`) {
		raw := "null"
		if al.Raw != nil {
			raw = *al.Raw
		}
		got[al.Labels["k"]] = *al.Level + "/" + raw + "/" + al.Route.Name
	}
	if got["none"] != "info/null/Default" || got["p5"] != "warning/P5/Default" || got["missing"] != "info/null/Default" {
		t.Errorf("severity levels %v", got)
	}

	// C-08.FR-12 on every replica, the Default route included.
	series := func(rr *Replica) int {
		return strings.Count(rr.metrics(t), "\nmuster_route_info{")
	}
	for _, rr := range h.Replicas {
		eventually(t, "three muster_route_info series", func() bool { return series(rr) == 3 })
	}

	// C-08.FR-9: a deleted Route leaves the evaluation order at once and its series goes. Its open Alert Group moves
	// to the Default route first (C-09.FR-19).
	if a := admin.do(http.MethodPost, "/api/v1/routes/"+rb.ID+"/move-open-alert-groups", ""); a.status != http.StatusOK {
		t.Fatalf("move = %d %s", a.status, a.body)
	}
	if a := admin.do(http.MethodDelete, "/api/v1/routes/"+rb.ID, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", a.status, a.body)
	}
	put("/groups/r1/alerts/three", `{"labels":{"severity":"critical","team":"x","n":"3"}}`)
	notify("r1", `{"reason":"new alerts added"}`)
	if got := routeOf(`alertname="Both"`, `n="3"`); got != "A" {
		t.Errorf("after the deletion to %s", got)
	}
	if got := routeOf(`alertname="Both"`, `n="2"`); got != "B" {
		t.Errorf("an alert of the deleted route shows %s", got)
	}
	for _, rr := range h.Replicas {
		eventually(t, "two muster_route_info series", func() bool { return series(rr) == 2 })
	}
	if n := h.count(t, `SELECT count(*) FROM stored_snapshots s JOIN routes r ON r.id = ANY(s.route_ids)
		WHERE s.integration_id = (SELECT id FROM integrations WHERE name = 'lab')`); n != 4 {
		t.Errorf("%d routes recorded on the stored snapshots, want 4", n)
	}
}

// metrics is the whole /metrics page of the replica, with a leading newline so that every series starts after one.
func (r *Replica) metrics(t *testing.T) string {
	t.Helper()
	a := call(t, http.MethodGet, r.Internal+"/metrics", "")
	return "\n" + string(a.body)
}
