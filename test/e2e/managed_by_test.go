// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/fakes/fakemattermost"
)

// markKind is a kind of configuration that takes managed_by: its collection and a valid write body with a name.
type markKind struct {
	name string
	path string
	body func(name string) map[string]any
}

// markAbsent leaves managed_by out of a write body.
var markAbsent = struct{}{}

// TestManagedBy is S-179 against `muster dev`: the specification it serves is MIT (C-33.FR-2); migration 0056 adds
// managed_by to the twelve tables; for an Integration, a Route, the Default route, a Team, a Schedule, an Escalation
// chain, a Lookup table and a Link rule, a create with managed_by terraform reads it back, one without reads null, an
// update without the field keeps it, one with null clears it, Terraform and a value of 33 characters are refused with
// 422 invalid_format at /managed_by and change nothing, and the Audit log entry of each change shows /managed_by in its
// diff (C-33.FR-11, C-33.AC-3). The mark changes nothing else: a Route marked terraform routes, delivers and is
// reordered, and an update through the web session with its CSRF token succeeds on a marked Team and keeps the mark.
func TestManagedBy(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	// The writes go through the Admin's Personal access token, as a tool such as the Terraform provider sends them; the
	// reads that check them go through the Admin's web session, which api.rate_limit does not count.
	api := e.api
	web := newAgentSignedIn(t, r)

	// C-33.FR-2: the specification the binary serves is MIT.
	spec := call(t, http.MethodGet, r.App+"/api/v1/openapi.yaml", "")
	if spec.status != http.StatusOK ||
		!strings.Contains(string(spec.body), "  license:\n    name: MIT\n    identifier: MIT\n") {
		t.Errorf("openapi.yaml = %d, its license is not MIT", spec.status)
	}
	if n := h.count(t, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema()
		AND column_name = 'managed_by' AND data_type = 'text' AND is_nullable = 'YES'`); n != 12 {
		t.Errorf("%d columns managed_by", n)
	}

	var profiles struct {
		Items []struct {
			ID     string         `json:"id"`
			Policy map[string]any `json:"policy"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	var paging map[string]any
	for _, p := range profiles.Items {
		if p.ID == "paging" {
			paging = p.Policy
		}
	}
	owners := api.json(http.MethodPost, "/api/v1/teams", `{"name":"Owners","slug":"owners","own_working_hours":false}`,
		http.StatusCreated)["id"].(string)

	write := func(method, target string, body map[string]any, mark any, header ...string) answer {
		t.Helper()
		b := map[string]any{}
		for k, v := range body {
			b[k] = v
		}
		if mark != markAbsent {
			b["managed_by"] = mark
		}
		raw, _ := json.Marshal(b)
		return api.do(method, target, string(raw), header...)
	}
	read := func(target string) (map[string]any, string) {
		t.Helper()
		a := web.do(http.MethodGet, target, "")
		if a.status != http.StatusOK {
			t.Fatalf("GET %s = %d %s", target, a.status, a.body)
		}
		var m map[string]any
		decode(t, a, &m)
		return m, a.header.Get("ETag")
	}
	update := func(target string, body map[string]any, mark any) answer {
		t.Helper()
		_, etag := read(target)
		return write(http.MethodPut, target, body, mark, "If-Match", etag)
	}
	// markOf is the managed_by of a read, which always carries the field.
	markOf := func(what string, m map[string]any) any {
		t.Helper()
		v, ok := m["managed_by"]
		if !ok {
			t.Errorf("%s: a read without managed_by: %v", what, m)
		}
		return v
	}
	// newest is the newest Audit log entry of the object id; an object without one fails the test, since every object
	// here was created or changed through the API.
	newest := func(id string) map[string]any {
		t.Helper()
		page := web.json(http.MethodGet, "/api/v1/audit-log?limit=1&resource_id="+id, "", http.StatusOK)
		items, _ := page["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no Audit log entry of %s", id)
		}
		return items[0].(map[string]any)
	}
	// lastChange is the change of /managed_by in the newest Audit log entry of the object id, and whether it has one.
	lastChange := func(id string) (map[string]any, bool) {
		t.Helper()
		diff, _ := newest(id)["diff"].([]any)
		for _, d := range diff {
			if c := d.(map[string]any); c["pointer"] == "/managed_by" {
				return c, true
			}
		}
		return nil, false
	}
	// updates runs the updates of C-33.AC-3 on the object id at target, marked terraform, with the bodies of body:
	// without the field it keeps the mark, Terraform and 33 characters are refused and change nothing, null clears it.
	updates := func(what, target, id string, body func(name string) map[string]any, name string) {
		t.Helper()
		kept := body(name + "-2")
		if a := update(target, kept, markAbsent); a.status != http.StatusOK {
			t.Fatalf("%s: update without managed_by = %d %s", what, a.status, a.body)
		}
		if got, _ := read(target); markOf(what, got) != "terraform" || got["name"] != kept["name"] {
			t.Errorf("%s: update without managed_by: %v, %v", what, got["name"], got["managed_by"])
		}
		for _, v := range []string{"Terraform", strings.Repeat("a", 33)} {
			before, etag := read(target)
			entry := newest(id)["id"]
			a := update(target, body(name+"-3"), v)
			if p, c := firstError(t, a); a.status != http.StatusUnprocessableEntity || p != "/managed_by" ||
				c != "invalid_format" {
				t.Errorf("%s: managed_by %q = %d %s", what, v, a.status, a.body)
			}
			if after, now := read(target); now != etag || after["name"] != before["name"] ||
				markOf(what, after) != "terraform" {
				t.Errorf("%s: a refused managed_by %q changed it: %v, %v", what, v, after["name"], after["managed_by"])
			}
			if got := newest(id)["id"]; got != entry {
				t.Errorf("%s: a refused managed_by %q wrote the Audit log entry %v", what, v, got)
			}
		}
		a := update(target, kept, nil)
		var got map[string]any
		decode(t, a, &got)
		if a.status != http.StatusOK || markOf(what, got) != nil {
			t.Errorf("%s: update with null = %d %s", what, a.status, a.body)
		}
		if got, _ := read(target); markOf(what, got) != nil {
			t.Errorf("%s: read after null = %v", what, got["managed_by"])
		}
		if c, ok := lastChange(id); !ok || c["before"] != "terraform" || c["after"] != nil {
			t.Errorf("%s: the Audit log of clearing: %v", what, c)
		}
	}

	kinds := []markKind{
		{"integration", "/api/v1/integrations", func(name string) map[string]any {
			return map[string]any{"name": name, "connection_mode": "webhook_only", "static_labels": map[string]any{},
				"duplicate_window_seconds": 45, "heartbeat": map[string]any{"enabled": false}}
		}},
		{"route", "/api/v1/routes", func(name string) map[string]any {
			return map[string]any{"name": name, "matchers": []map[string]string{{"label": "team", "op": "=",
				"value": "mark-" + name}}, "group_key": []string{"alertname"}, "policy": paging}
		}},
		{"team", "/api/v1/teams", func(name string) map[string]any {
			return map[string]any{"name": name, "slug": name, "own_working_hours": false}
		}},
		{"schedule", "/api/v1/schedules", func(name string) map[string]any {
			return map[string]any{"team_id": owners, "name": name, "rotation_layers": []any{}}
		}},
		{"escalation-chain", "/api/v1/escalation-chains", func(name string) map[string]any {
			return map[string]any{"name": name, "steps": []any{}}
		}},
		{"lookup-table", "/api/v1/lookup-tables", func(name string) map[string]any {
			return map[string]any{"name": name, "columns": []string{"address"}, "entries": []any{}}
		}},
		{"link-rule", "/api/v1/link-rules", func(name string) map[string]any {
			return map[string]any{"name": name, "matchers": []any{}, "scope": map[string]any{"type": "alert_group"},
				"url_template": "https://links.example.org/" + name}
		}},
	}
	var markedTeam string
	for _, k := range kinds {
		name := "tf-" + k.name
		a := write(http.MethodPost, k.path, k.body(name), "terraform")
		var created map[string]any
		decode(t, a, &created)
		if a.status != http.StatusCreated || markOf(k.name, created) != "terraform" {
			t.Fatalf("%s: create with terraform = %d %s", k.name, a.status, a.body)
		}
		id := created["id"].(string)
		if got, _ := read(k.path + "/" + id); markOf(k.name, got) != "terraform" {
			t.Errorf("%s: read = %v", k.name, got["managed_by"])
		}
		if c, ok := lastChange(id); !ok || c["after"] != "terraform" || c["before"] != nil {
			t.Errorf("%s: the Audit log of the creation: %v", k.name, c)
		}
		a = write(http.MethodPost, k.path, k.body("plain-"+k.name), markAbsent)
		var plain map[string]any
		decode(t, a, &plain)
		if a.status != http.StatusCreated || markOf(k.name, plain) != nil {
			t.Errorf("%s: create without = %d %s", k.name, a.status, a.body)
		} else if got, _ := read(k.path + "/" + plain["id"].(string)); markOf(k.name, got) != nil {
			t.Errorf("%s: read without = %v", k.name, got["managed_by"])
		}
		items, _ := web.json(http.MethodGet, k.path, "", http.StatusOK)["items"].([]any)
		if i := slices.IndexFunc(items, func(x any) bool { return x.(map[string]any)["id"] == id }); i < 0 ||
			markOf(k.name, items[i].(map[string]any)) != "terraform" {
			t.Errorf("%s: the list misses the mark of %s", k.name, id)
		}
		updates(k.name, k.path+"/"+id, id, k.body, name)
		if k.name == "team" {
			markedTeam = id
			if a := update(k.path+"/"+id, k.body(name), "terraform"); a.status != http.StatusOK {
				t.Fatalf("mark the Team again = %d %s", a.status, a.body)
			}
		}
	}

	// The built-in Link rule "Explore" takes the mark with its own name and scope, and loses it with null (C-33.FR-14).
	rules, _ := web.json(http.MethodGet, "/api/v1/link-rules", "", http.StatusOK)["items"].([]any)
	i := slices.IndexFunc(rules, func(x any) bool { return x.(map[string]any)["builtin"] == true })
	if i < 0 {
		t.Fatal("no built-in Link rule")
	}
	explore := rules[i].(map[string]any)
	exploreID := explore["id"].(string)
	exploreBody := map[string]any{"name": explore["name"], "matchers": explore["matchers"], "scope": explore["scope"],
		"url_template": explore["url_template"]}
	for _, mark := range []any{"terraform", nil} {
		a := update("/api/v1/link-rules/"+exploreID, exploreBody, mark)
		var got map[string]any
		decode(t, a, &got)
		if a.status != http.StatusOK || got["builtin"] != true || markOf("explore", got) != mark {
			t.Errorf("mark the built-in rule %v = %d %s", mark, a.status, a.body)
		}
		if c, ok := lastChange(exploreID); !ok || c["after"] != mark {
			t.Errorf("the Audit log of marking the built-in rule %v: %v", mark, c)
		}
	}

	// The Default route takes the mark like any Route.
	var routes struct {
		Items []map[string]any `json:"items"`
	}
	decode(t, web.do(http.MethodGet, "/api/v1/routes", ""), &routes)
	var def map[string]any
	for _, rt := range routes.Items {
		if rt["is_default"] == true {
			def = rt
		}
	}
	if def == nil {
		t.Fatal("no Default route")
	}
	defID := def["id"].(string)
	if markOf("default route", def) != nil {
		t.Errorf("the Default route starts marked %v", def["managed_by"])
	}
	defBody := func(name string) map[string]any {
		return map[string]any{"name": name, "matchers": []any{}, "group_key": def["group_key"], "policy": def["policy"]}
	}
	a := update("/api/v1/routes/"+defID, defBody(def["name"].(string)), "terraform")
	var got map[string]any
	decode(t, a, &got)
	if a.status != http.StatusOK || markOf("default route", got) != "terraform" || got["is_default"] != true {
		t.Fatalf("mark the Default route = %d %s", a.status, a.body)
	}
	if c, ok := lastChange(defID); !ok || c["after"] != "terraform" {
		t.Errorf("the Audit log of marking the Default route: %v", c)
	}
	updates("default route", "/api/v1/routes/"+defID, defID, defBody, def["name"].(string))

	// The mark changes nothing else: a Route marked terraform routes and delivers ...
	conn := e.connection(100)
	dest := e.destination("tf-alerts", conn, fakemattermost.ChannelAlerts, nil, 100)
	a = write(http.MethodPost, "/api/v1/routes", map[string]any{"name": "tf-paging", "matchers": []map[string]string{
		{"label": "team", "op": "=", "value": "tf"}}, "group_key": []string{"alertname", "cluster"},
		"destination_ids": []string{dest}, "policy": paging}, "terraform")
	var marked map[string]any
	decode(t, a, &marked)
	if a.status != http.StatusCreated || marked["managed_by"] != "terraform" {
		t.Fatalf("create the marked Route = %d %s", a.status, a.body)
	}
	tfRoute := marked["id"].(string)
	e.group("tf1", "Lag")
	e.alert("tf1", "a", `{"team":"tf","cluster":"tf1"}`, "")
	e.notify("tf1", "first notification")
	g := e.groupOf("tf1")
	if rt, _ := e.alertGroup(g)["route"].(map[string]any); rt == nil || rt["id"] != tfRoute {
		t.Errorf("the Alert Group's Route = %v", rt)
	}
	if root := e.root(e.number(g)); root.ChannelID != fakemattermost.ChannelAlerts {
		t.Errorf("the Root message went to %s", root.ChannelID)
	}

	// ... and is reordered.
	var list struct {
		Items []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"is_default"`
			ManagedBy any    `json:"managed_by"`
		} `json:"items"`
	}
	listed := web.do(http.MethodGet, "/api/v1/routes", "")
	decode(t, listed, &list)
	order := []string{tfRoute}
	for _, rt := range list.Items {
		if !rt.IsDefault && rt.ID != tfRoute {
			order = append(order, rt.ID)
		}
	}
	if list.Items[0].ID == tfRoute {
		t.Fatal("the marked Route is first before the reorder")
	}
	raw, _ := json.Marshal(map[string]any{"route_ids": order})
	reordered := api.do(http.MethodPut, "/api/v1/route-order", string(raw), "If-Match", listed.header.Get("ETag"))
	if reordered.status != http.StatusOK {
		t.Fatalf("reorder = %d %s", reordered.status, reordered.body)
	}
	decode(t, web.do(http.MethodGet, "/api/v1/routes", ""), &list)
	if list.Items[0].ID != tfRoute || list.Items[0].ManagedBy != "terraform" {
		t.Errorf("after the reorder the first Route is %+v", list.Items[0])
	}

	// An update through the web session with its CSRF token succeeds on a marked Team and keeps the mark.
	if markedTeam == "" {
		t.Fatal("no marked Team")
	}
	team, etag := read("/api/v1/teams/" + markedTeam)
	if team["managed_by"] != "terraform" {
		t.Fatalf("the Team is not marked: %v", team["managed_by"])
	}
	a = web.putWithIfMatch("/api/v1/teams/"+markedTeam, `{"name":"Renamed in the web UI","slug":"`+
		team["slug"].(string)+`","own_working_hours":false}`, etag)
	var saved map[string]any
	decode(t, a, &saved)
	if a.status != http.StatusOK || saved["name"] != "Renamed in the web UI" || saved["managed_by"] != "terraform" {
		t.Errorf("a save from the web session = %d %s", a.status, a.body)
	}
	if c, ok := lastChange(markedTeam); ok {
		t.Errorf("a save from the web session changed the mark: %v", c)
	}
}
