// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
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
// Its part testManagedByConnections is S-180: Connections, Destinations and the objects that exist once.
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
	// newestOf is the newest Audit log entry that the filter query selects, nil for none.
	newestOf := func(query string) map[string]any {
		t.Helper()
		page := web.json(http.MethodGet, "/api/v1/audit-log?limit=1&"+query, "", http.StatusOK)
		items, _ := page["items"].([]any)
		if len(items) == 0 {
			return nil
		}
		return items[0].(map[string]any)
	}
	// newest is the newest Audit log entry of the object id; an object without one fails the test, since every object
	// here was created or changed through the API.
	newest := func(id string) map[string]any {
		t.Helper()
		e := newestOf("resource_id=" + id)
		if e == nil {
			t.Fatalf("no Audit log entry of %s", id)
		}
		return e
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

	testManagedByConnections(t, managedByEnv{h: h, e: e, web: web, write: write, read: read, update: update,
		markOf: markOf, newestOf: newestOf, lastChange: lastChange, updates: updates, paging: paging,
		api: func(c patClient) { api = c }})
}

// managedByEnv is what TestManagedBy shares with its part of S-180: the harness, the Mattermost environment, the web
// session, its helpers, the Paging policy and a setter of the token the writes go through.
type managedByEnv struct {
	h          *Harness
	e          *mm
	web        *agent
	write      func(method, target string, body map[string]any, mark any, header ...string) answer
	read       func(target string) (map[string]any, string)
	update     func(target string, body map[string]any, mark any) answer
	markOf     func(what string, m map[string]any) any
	newestOf   func(query string) map[string]any
	lastChange func(id string) (map[string]any, bool)
	updates    func(what, target, id string, body func(name string) map[string]any, name string)
	paging     map[string]any
	api        func(c patClient)
}

// markKind180 is a kind of S-180 that takes managed_by: its collection, the body of a creation, with its secrets, the
// body of an update, which leaves them out and so keeps them, and calls, when set, the count of the calls a save of it
// may make to the outside world, which a save of the mark alone does not make.
type markKind180 struct {
	name   string
	path   string
	create func(name string) map[string]any
	body   func(name string) map[string]any
	calls  func() int
}

// testManagedByConnections is S-180 against `muster dev` (C-33.FR-11, C-33.AC-3): for a Connection of each type and a
// Destination of each type, a create with managed_by terraform reads it back, one without reads null, the list carries
// it, a save of the mark alone moves the ETag, records /managed_by alone and calls nothing — no setWebhook of a
// Telegram Connection in the webhook mode, no read of a bridge's info — an update without the field keeps it, one with
// null clears it, and Terraform and a value of 33 characters are refused with 422 invalid_format at /managed_by and
// change nothing. The Organization's settings, the Outbound address policy and the OIDC settings read null until a
// write sets the mark and take it the same way. The mark changes nothing else: a marked Mattermost Destination on a
// marked Connection delivers, the Connection is checked, a marked Organization re-renders the open Root messages on a
// change of its time zone — and not on a save of the mark alone — and a Secret or a Signing secret of a marked outgoing
// webhook written alone keeps the mark.
func testManagedByConnections(t *testing.T, m managedByEnv) {
	h, e, web := m.h, m.e, m.web
	// The writes of S-180 go through a token of their own, as a tool would hold one, so that the writes of S-179 leave
	// them the whole of api.rate_limit.
	perms, _ := json.Marshal(web.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	m.api(patClient{t: t, base: h.Replicas[0].App, token: web.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"terraform","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)})

	without := func(body map[string]any, keys ...string) map[string]any {
		out := maps.Clone(body)
		for _, k := range keys {
			delete(out, k)
		}
		return out
	}
	limiter := func(n, per int) map[string]any { return map[string]any{"limit": n, "per_seconds": per} }
	noProxy := map[string]any{"enabled": false}
	created := func(a answer) map[string]any {
		t.Helper()
		var got map[string]any
		decode(t, a, &got)
		if d, ok := got["destination"].(map[string]any); ok {
			return d
		}
		return got
	}
	// markOnly saves the object at target with the body of its values and another mark: the save moves the ETag and
	// records /managed_by alone in the entry that query selects; then it marks the object terraform again.
	markOnly := func(what, target, query string, body map[string]any) {
		t.Helper()
		_, etag := m.read(target)
		if a := m.update(target, body, "opentofu"); a.status != http.StatusOK {
			t.Fatalf("%s: a save of the mark alone = %d %s", what, a.status, a.body)
		}
		if got, now := m.read(target); now == etag || m.markOf(what, got) != "opentofu" {
			t.Errorf("%s: a save of the mark alone: ETag %s, before %s, mark %v", what, now, etag, got["managed_by"])
		}
		diff, _ := m.newestOf(query)["diff"].([]any)
		if len(diff) != 1 || diff[0].(map[string]any)["pointer"] != "/managed_by" ||
			diff[0].(map[string]any)["before"] != "terraform" || diff[0].(map[string]any)["after"] != "opentofu" {
			t.Errorf("%s: the Audit log of the mark alone: %v", what, diff)
		}
		if a := m.update(target, body, "terraform"); a.status != http.StatusOK {
			t.Fatalf("%s: mark again = %d %s", what, a.status, a.body)
		}
	}
	// exercise runs C-33.AC-3 on the kind k and returns the id of the object created marked terraform, which ends
	// without a mark and named tf-<kind>-2.
	exercise := func(k markKind180) string {
		t.Helper()
		name := "tf-" + k.name
		a := m.write(http.MethodPost, k.path, k.create(name), "terraform")
		obj := created(a)
		if a.status != http.StatusCreated || m.markOf(k.name, obj) != "terraform" {
			t.Fatalf("%s: create with terraform = %d %s", k.name, a.status, a.body)
		}
		id := obj["id"].(string)
		target := k.path + "/" + id
		if got, _ := m.read(target); m.markOf(k.name, got) != "terraform" {
			t.Errorf("%s: read = %v", k.name, got["managed_by"])
		}
		if c, ok := m.lastChange(id); !ok || c["after"] != "terraform" || c["before"] != nil {
			t.Errorf("%s: the Audit log of the creation: %v", k.name, c)
		}
		a = m.write(http.MethodPost, k.path, k.create("plain-"+k.name), markAbsent)
		plain := created(a)
		if a.status != http.StatusCreated || m.markOf(k.name, plain) != nil {
			t.Errorf("%s: create without = %d %s", k.name, a.status, a.body)
		} else if got, _ := m.read(k.path + "/" + plain["id"].(string)); m.markOf(k.name, got) != nil {
			t.Errorf("%s: read without = %v", k.name, got["managed_by"])
		}
		items, _ := web.json(http.MethodGet, k.path+"?limit=500", "", http.StatusOK)["items"].([]any)
		if i := slices.IndexFunc(items, func(x any) bool { return x.(map[string]any)["id"] == id }); i < 0 ||
			m.markOf(k.name, items[i].(map[string]any)) != "terraform" {
			t.Errorf("%s: the list misses the mark of %s", k.name, id)
		}
		calls := 0
		if k.calls != nil {
			// The creation made the call, so that the counter is seen to count.
			if calls = k.calls(); calls == 0 {
				t.Errorf("%s: no call of the creation was recorded", k.name)
			}
		}
		markOnly(k.name, target, "resource_id="+id, k.body(name))
		if k.calls != nil && k.calls() != calls {
			t.Errorf("%s: a save of the mark alone made %d calls", k.name, k.calls()-calls)
		}
		m.updates(k.name, target, id, k.body, name)
		return id
	}

	// Connections of each type.
	tgToken := func(name string) string { return "777301:" + name }
	setWebhooks := func() int {
		n := 0
		for _, q := range fakeRequests(t, h.Fakes.Telegram) {
			if q.Path == "/bot"+tgToken("tf-telegram-connection")+"/setWebhook" {
				n++
			}
		}
		return n
	}
	infoReads := func() int {
		n := 0
		for _, q := range fakeRequests(t, h.Fakes.Bridge) {
			if q.Path == "/v1/info" && carries(q, fakebridge.SecondToken) {
				n++
			}
		}
		return n
	}
	mmConnection := func(name string) map[string]any {
		return map[string]any{"type": "mattermost", "name": name, "server_url": h.Fakes.Mattermost,
			"bot_token": "mm-dev-token", "proxy": noProxy, "limiter": limiter(100, 1)}
	}
	tgConnection := func(name string) map[string]any {
		return map[string]any{"type": "telegram", "name": name, "bot_api_base_url": h.Fakes.Telegram,
			"update_mode": "webhook", "bot_token": tgToken(name), "proxy": noProxy, "limiter": limiter(15, 1)}
	}
	brConnection := func(name string) map[string]any {
		return map[string]any{"type": "bridge", "name": name, "base_url": h.Fakes.Bridge, "event_delivery": "poll",
			"bot_token": fakebridge.SecondToken, "proxy": noProxy, "limiter": limiter(10, 1)}
	}
	slConnection := func(name string) map[string]any {
		return map[string]any{"type": "slack", "name": name, "event_mode": "http",
			"bot_token": devmode.SlackSecondBotToken, "slack_signing_secret": fakeslack.SecondSigningSecret,
			"proxy": noProxy, "limiter": limiter(50, 60)}
	}
	connections := map[string]string{}
	for _, k := range []markKind180{
		{"mattermost-connection", "/api/v1/connections", mmConnection,
			func(n string) map[string]any { return without(mmConnection(n), "bot_token") }, nil},
		{"telegram-connection", "/api/v1/connections", tgConnection,
			func(n string) map[string]any { return without(tgConnection(n), "bot_token") }, setWebhooks},
		{"bridge-connection", "/api/v1/connections", brConnection,
			func(n string) map[string]any { return without(brConnection(n), "bot_token") }, infoReads},
		{"slack-connection", "/api/v1/connections", slConnection,
			func(n string) map[string]any { return without(slConnection(n), "bot_token", "slack_signing_secret") }, nil},
	} {
		connections[k.name] = exercise(k)
	}

	// Destinations of each type: the Mattermost one through the Connection above, the others through the demo's.
	tgConn := e.demoConnection("telegram", devmode.TelegramConnectionName)
	brConn := e.demoConnection("bridge", devmode.BridgeConnectionName)
	slConn := e.demoConnection("slack", devmode.SlackConnectionName)
	e.fake(http.MethodPut, fmt.Sprintf("%s/_fake/chats/%d/members/%d", h.Fakes.Telegram, faketelegram.GroupID,
		faketelegram.BotID), `{"status":"administrator","can_post_messages":true}`)
	if c := web.json(http.MethodPost, "/api/v1/connections/"+slConn+"/checks", "", http.StatusOK); c["ok"] != true {
		t.Fatalf("the check of %s: %v", devmode.SlackConnectionName, c)
	}
	mmConn := connections["mattermost-connection"]
	mmDestination := func(name string) map[string]any {
		return map[string]any{"type": "mattermost", "name": name, "connection_id": mmConn,
			"team_id": fakemattermost.TeamID, "channel_id": fakemattermost.ChannelAlerts, "limiter": limiter(100, 1)}
	}
	tgDestination := func(name string) map[string]any {
		return map[string]any{"type": "telegram", "name": name, "connection_id": tgConn,
			"channel_id": "@" + faketelegram.ChannelUsername, "limiter": limiter(60, 60)}
	}
	brDestination := func(name string) map[string]any {
		return map[string]any{"type": "bridge", "name": name, "connection_id": brConn,
			"channel_id": fakebridge.ChannelAlerts, "limiter": limiter(60, 60)}
	}
	slDestination := func(name string) map[string]any {
		return map[string]any{"type": "slack", "name": name, "connection_id": slConn,
			"channel_id": fakeslack.ChannelAlerts, "limiter": limiter(50, 1)}
	}
	whDestination := func(name string) map[string]any {
		return map[string]any{"type": "webhook", "name": name, "mode": "events",
			"events": map[string]any{"url": webhookBase + "/hook/" + name, "headers": []any{}}, "proxy": noProxy,
			"limiter": limiter(50, 1)}
	}
	destinations := map[string]string{}
	for _, k := range []markKind180{
		{"mattermost-destination", "/api/v1/destinations", mmDestination, mmDestination, nil},
		{"telegram-destination", "/api/v1/destinations", tgDestination, tgDestination, nil},
		{"bridge-destination", "/api/v1/destinations", brDestination, brDestination, nil},
		{"slack-destination", "/api/v1/destinations", slDestination, slDestination, nil},
		{"webhook-destination", "/api/v1/destinations", whDestination, whDestination, nil},
	} {
		destinations[k.name] = exercise(k)
	}

	// The mark changes nothing else: the Connection marked terraform is checked, and the marked Mattermost Destination
	// through it takes the Root message of an Alert Group of its Route.
	mark := func(path, id string, body map[string]any) {
		t.Helper()
		if a := m.update(path+"/"+id, body, "terraform"); a.status != http.StatusOK {
			t.Fatalf("mark %s = %d %s", id, a.status, a.body)
		}
	}
	mark("/api/v1/connections", mmConn, without(mmConnection("tf-mattermost-connection-2"), "bot_token"))
	mmDest := destinations["mattermost-destination"]
	mark("/api/v1/destinations", mmDest, mmDestination("tf-mattermost-destination-2"))
	if c := web.json(http.MethodPost, "/api/v1/connections/"+mmConn+"/checks", "", http.StatusOK); c["ok"] != true {
		t.Errorf("the check of the marked Connection: %v", c)
	}
	a := m.write(http.MethodPost, "/api/v1/routes", map[string]any{"name": "tf-180", "matchers": []map[string]string{
		{"label": "team", "op": "=", "value": "tf180"}}, "group_key": []string{"alertname", "cluster"},
		"destination_ids": []string{mmDest}, "policy": m.paging}, markAbsent)
	if a.status != http.StatusCreated {
		t.Fatalf("create the Route of the marked Destination = %d %s", a.status, a.body)
	}
	e.group("tf180", "Lag180")
	e.alert("tf180", "a", `{"team":"tf180","cluster":"tf180"}`, "")
	e.notify("tf180", "first notification")
	n := e.number(e.groupOf("tf180"))
	root := e.root(n)
	if root.ChannelID != fakemattermost.ChannelAlerts {
		t.Errorf("the Root message of the marked Destination went to %s", root.ChannelID)
	}
	if got, _ := m.read("/api/v1/destinations/" + mmDest); m.markOf("destination", got) != "terraform" {
		t.Errorf("the delivery changed the mark: %v", got["managed_by"])
	}

	// A Secret and a Signing secret of a marked outgoing webhook, written alone, keep the mark.
	hook := destinations["webhook-destination"]
	mark("/api/v1/destinations", hook, whDestination("tf-webhook-destination-2"))
	for _, op := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPut, "/secrets/api_key", `{"value":"s3cr3t"}`, http.StatusOK},
		{http.MethodPost, "/signing-secret", "", http.StatusCreated},
		{http.MethodDelete, "/secrets/api_key", "", http.StatusNoContent},
	} {
		if a := web.do(op.method, "/api/v1/destinations/"+hook+op.path, op.body); a.status != op.status {
			t.Errorf("%s %s = %d %s", op.method, op.path, a.status, a.body)
		}
		if got, _ := m.read("/api/v1/destinations/" + hook); m.markOf("webhook", got) != "terraform" {
			t.Errorf("%s %s changed the mark: %v", op.method, op.path, got["managed_by"])
		}
	}

	// The objects that exist once.
	type single struct {
		what, path, query string
		body              func() map[string]any
	}
	orgBody := func() map[string]any {
		got, _ := m.read("/api/v1/organization")
		body := without(got, "id", "etag", "managed_by")
		body["outgoing_heartbeat"] = map[string]any{"proxy": noProxy}
		return body
	}
	policyBody := func() map[string]any {
		got, _ := m.read("/api/v1/organization/outbound-policy")
		return map[string]any{"policy": got["policy"], "allowed": got["allowed"], "denied": got["denied"]}
	}
	oidcBody := func() map[string]any {
		got, _ := m.read("/api/v1/oidc-settings")
		body := without(got, "etag", "client_secret_status", "warnings", "updated_at", "claims_supported", "managed_by")
		proxy := map[string]any{"enabled": got["proxy"].(map[string]any)["enabled"]}
		for _, k := range []string{"type", "address"} {
			if v, ok := got["proxy"].(map[string]any)[k]; ok && v != nil {
				proxy[k] = v
			}
		}
		body["proxy"] = proxy
		return body
	}
	singles := []single{
		{"organization", "/api/v1/organization", "resource_type=organization", orgBody},
		{"outbound policy", "/api/v1/organization/outbound-policy", "resource_type=outbound_policy", policyBody},
		{"oidc settings", "/api/v1/oidc-settings", "resource_type=oidc_settings", oidcBody},
	}
	entryOf := func(query string) any {
		if e := m.newestOf(query); e != nil {
			return e["id"]
		}
		return nil
	}
	settled := func() {
		t.Helper()
		eventually(t, "the open Alert Group updates to settle", func() bool {
			return h.count(t, `SELECT count(*) FROM open_group_updates`) == 0 &&
				h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0
		})
	}
	settled()
	// The re-renders of the Organization's settings and the edits of the open Root message, counted from here: the
	// save of the mark alone below adds none, the change of the time zone after it exactly one each.
	runs := messageSettingsDone(h, "organization_message_settings")
	patch := "/api/v4/posts/" + root.ID + "/patch"
	patches := len(e.calls(http.MethodPut, patch))
	for _, s := range singles {
		if got, _ := m.read(s.path); m.markOf(s.what, got) != nil {
			t.Errorf("%s: starts marked %v", s.what, got["managed_by"])
		}
		for _, v := range []string{"Terraform", strings.Repeat("a", 33)} {
			_, etag := m.read(s.path)
			entry := entryOf(s.query)
			a := m.update(s.path, s.body(), v)
			if p, c := firstError(t, a); a.status != http.StatusUnprocessableEntity || p != "/managed_by" ||
				c != "invalid_format" {
				t.Errorf("%s: managed_by %q = %d %s", s.what, v, a.status, a.body)
			}
			if got, now := m.read(s.path); now != etag || m.markOf(s.what, got) != nil {
				t.Errorf("%s: a refused managed_by %q changed it: %v", s.what, v, got["managed_by"])
			}
			if got := entryOf(s.query); got != entry {
				t.Errorf("%s: a refused managed_by %q wrote the Audit log entry %v", s.what, v, got)
			}
		}
		_, etag := m.read(s.path)
		if a := m.update(s.path, s.body(), "terraform"); a.status != http.StatusOK {
			t.Fatalf("%s: mark = %d %s", s.what, a.status, a.body)
		}
		got, now := m.read(s.path)
		if m.markOf(s.what, got) != "terraform" || now == etag {
			t.Errorf("%s: after the mark %v, ETag %s before %s", s.what, got["managed_by"], now, etag)
		}
		diff, _ := m.newestOf(s.query)["diff"].([]any)
		if len(diff) != 1 || diff[0].(map[string]any)["pointer"] != "/managed_by" ||
			diff[0].(map[string]any)["after"] != "terraform" || diff[0].(map[string]any)["before"] != nil {
			t.Errorf("%s: the Audit log of the mark: %v", s.what, diff)
		}
		if s.what == "organization" {
			settled()
			if got := len(e.calls(http.MethodPut, patch)) - patches; got != 0 {
				t.Errorf("a save of the mark alone of the Organization edited the Root message %d times", got)
			}
		}
		if a := m.update(s.path, s.body(), markAbsent); a.status != http.StatusOK {
			t.Errorf("%s: update without = %d %s", s.what, a.status, a.body)
		}
		if got, _ := m.read(s.path); m.markOf(s.what, got) != "terraform" {
			t.Errorf("%s: update without managed_by: %v", s.what, got["managed_by"])
		}
	}

	// A marked Organization re-renders the open Root messages on a change of its time zone, once, as before.
	settled()
	body := orgBody()
	body["time_zone"] = "Europe/Berlin"
	if a := m.update("/api/v1/organization", body, markAbsent); a.status != http.StatusOK {
		t.Fatalf("a new time zone of the marked Organization = %d %s", a.status, a.body)
	}
	berlin := regexp.MustCompile(` CES?T\b`)
	eventually(t, "the re-render of the Root message in the new time zone", func() bool {
		post := e.root(n)
		return h.count(t, `SELECT count(*) FROM open_group_updates`) == 0 &&
			h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0 &&
			berlin.MatchString(post.attachment(t).Text+" "+fieldValues(post.attachment(t)))
	})
	// Counted since before the save of the mark alone, whose re-render, had it run, would have run first.
	if got := len(e.calls(http.MethodPut, patch)) - patches; got != 1 {
		t.Errorf("the mark and the new time zone edited the Root message %d times, want 1", got)
	}
	eventually(t, "the run of the re-render to be logged", func() bool {
		return messageSettingsDone(h, "organization_message_settings") > runs
	})
	if got := messageSettingsDone(h, "organization_message_settings") - runs; got != 1 {
		t.Errorf("the mark and the new time zone ran %d re-renders of the Organization's settings, want 1", got)
	}
	if got, _ := m.read("/api/v1/organization"); m.markOf("organization", got) != "terraform" {
		t.Errorf("the new time zone changed the mark: %v", got["managed_by"])
	}

	// null clears the mark of each, and the Audit log shows the value before.
	for _, s := range singles {
		a := m.update(s.path, s.body(), nil)
		var got map[string]any
		decode(t, a, &got)
		if a.status != http.StatusOK || m.markOf(s.what, got) != nil {
			t.Errorf("%s: update with null = %d %s", s.what, a.status, a.body)
		}
		diff, _ := m.newestOf(s.query)["diff"].([]any)
		if len(diff) != 1 || diff[0].(map[string]any)["before"] != "terraform" || diff[0].(map[string]any)["after"] != nil {
			t.Errorf("%s: the Audit log of clearing: %v", s.what, diff)
		}
	}
}
