// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
)

// personWithToken creates a local User with the Role, sets their password through the setup link, signs them in and
// returns their id and a Personal access token with every Permission of the Role.
func personWithToken(t *testing.T, admin *agent, base, login, role string) (string, patClient) {
	t.Helper()
	created := admin.json(http.MethodPost, "/api/v1/users", `{"name":"`+login+`","login":"`+login+`","role":"`+role+
		`"}`, http.StatusCreated)
	link, _ := url.Parse(created["password_setup_link"].(map[string]any)["url"].(string))
	setup, _ := strings.CutPrefix(link.Fragment, "token=")
	password := login + "-password-1"
	if a := admin.do(http.MethodPost, "/api/v1/password-setups", `{"token":"`+setup+`","password":"`+password+`"}`); a.status !=
		http.StatusNoContent {
		t.Fatalf("password setup of %s = %d %s", login, a.status, a.body)
	}
	person := newAgent(t, base)
	if a := person.signIn(login, password); a.status != http.StatusCreated {
		t.Fatalf("sign in of %s = %d %s", login, a.status, a.body)
	}
	perms, _ := json.Marshal(person.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := person.json(http.MethodPost, "/api/v1/me/personal-access-tokens", `{"name":"e2e","permissions":`+
		string(perms)+`}`, http.StatusCreated)["value"].(string)
	return created["user"].(map[string]any)["id"].(string), patClient{t: t, base: base, token: token}
}

// TestTeams is S-115 against `muster dev`, as its Verification: a Team with its slug rules and conflicts, the people
// added by hand with their Role and source, a Team that grants nothing (C-25.AC-6), Working hours that end later than
// they start and follow the Organization's start (C-25.AC-8), the owning Team of a Route and "My Teams" (C-25.AC-11),
// the deletion (C-25.AC-9) and the people of deleted and disabled Users (C-25.AC-12), with muster_team_info.
func TestTeams(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	api := patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"teams","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)}
	bob, b := personWithToken(t, admin, r.App, "bob", "responder")
	vic, v := personWithToken(t, admin, r.App, "vic", "viewer")
	sa := admin.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"ci","role":"viewer"}`,
		http.StatusCreated)["id"].(string)
	ci := patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/service-accounts/"+sa+"/tokens",
		`{"name":"ci"}`, http.StatusCreated)["value"].(string)}

	// C-25.FR-1, FR-11: the Team and its slug.
	team := api.json(http.MethodPost, "/api/v1/teams", `{"name":"Payments","slug":"payments","description":"",`+
		`"own_working_hours":false}`, http.StatusCreated)
	tp := team["id"].(string)
	if w := team["working_hours"].(map[string]any); team["slug"] != "payments" || team["own_working_hours"] != false ||
		w["start"] != "09:00" || w["end"] != "18:00" || w["time_zone"] != "UTC" || len(w["days"].([]any)) != 5 {
		t.Errorf("team %v", team)
	}
	for _, slug := range []string{"Payments", "-x", strings.Repeat("a", 64)} {
		a := api.do(http.MethodPost, "/api/v1/teams", `{"name":"Payments B","slug":"`+slug+`","own_working_hours":false}`)
		if p, c := firstError(t, a); a.status != http.StatusUnprocessableEntity || p != "/slug" || c != "invalid_format" {
			t.Errorf("slug %q = %d %s", slug, a.status, a.body)
		}
	}
	for body, code := range map[string]string{
		`{"name":"Other","slug":"payments","own_working_hours":false}`: "slug_taken",
		`{"name":"PAYMENTS","slug":"other","own_working_hours":false}`: "name_taken",
	} {
		if a := api.do(http.MethodPost, "/api/v1/teams", body); a.status != http.StatusConflict ||
			!strings.Contains(string(a.body), `"code":"`+code+`"`) {
			t.Errorf("%s = %d %s", code, a.status, a.body)
		}
	}
	etag := api.do(http.MethodGet, "/api/v1/teams/"+tp, "").header.Get("ETag")
	if a := api.do(http.MethodPut, "/api/v1/teams/"+tp, `{"name":"Payments","slug":"payments","own_working_hours":false}`); a.status != http.StatusPreconditionRequired {
		t.Errorf("without If-Match = %d", a.status)
	}
	if a := api.do(http.MethodPut, "/api/v1/teams/"+tp, `{"name":"Payments","slug":"payments","description":"Pays",`+
		`"own_working_hours":false}`, "If-Match", etag); a.status != http.StatusOK {
		t.Fatalf("update = %d %s", a.status, a.body)
	}
	if a := api.do(http.MethodPut, "/api/v1/teams/"+tp, `{"name":"Payments","slug":"payments","own_working_hours":false}`,
		"If-Match", etag); a.status != http.StatusPreconditionFailed {
		t.Errorf("stale = %d", a.status)
	}

	// C-25.FR-3: people by hand; C-25.AC-6: a Team grants nothing.
	api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+bob+`"}`, http.StatusCreated)
	api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+vic+`"}`, http.StatusCreated)
	people := api.json(http.MethodGet, "/api/v1/teams/"+tp+"/users", "", http.StatusOK)["items"].([]any)
	first := people[0].(map[string]any)
	if len(people) != 2 || first["user"].(map[string]any)["name"] != "bob" || first["role"] != "responder" ||
		first["sources"].([]any)[0] != "by_hand" {
		t.Errorf("people %v", people)
	}
	for _, c := range []patClient{b, v} {
		for _, req := range [][3]string{{http.MethodDelete, "/api/v1/teams/" + tp, ""},
			{http.MethodPut, "/api/v1/teams/" + tp, `{"name":"x","slug":"x","own_working_hours":false}`},
			{http.MethodPost, "/api/v1/teams/" + tp + "/users", `{"user_id":"` + bob + `"}`}} {
			if a := c.do(req[0], req[1], req[2], "If-Match", etag); a.status != http.StatusForbidden {
				t.Errorf("%s %s = %d", req[0], req[1], a.status)
			}
		}
		if a := c.do(http.MethodGet, "/api/v1/teams", ""); a.status != http.StatusOK {
			t.Errorf("read = %d", a.status)
		}
	}

	// C-25.FR-9, C-25.AC-8: Working hours.
	a := api.do(http.MethodPost, "/api/v1/teams", `{"name":"Search","slug":"search","own_working_hours":true,`+
		`"working_hours":{"days":["monday","friday"],"start":"19:00","end":"11:00","time_zone":"Europe/Berlin"}}`)
	if p, c := firstError(t, a); a.status != http.StatusUnprocessableEntity || p != "/working_hours/end" ||
		c != "out_of_range" {
		t.Errorf("end before start = %d %s", a.status, a.body)
	}
	a = putOrganization(api, func(m map[string]any) { delete(m, "working_hours"); m["workday_end"] = "08:00" })
	if p, c := firstError(t, a); a.status != http.StatusUnprocessableEntity || p != "/workday_end" || c != "out_of_range" {
		t.Errorf("the Organization's end before its start = %d %s", a.status, a.body)
	}
	if a := putOrganization(api, func(m map[string]any) { delete(m, "working_hours"); m["workday_start"] = "08:00" }); a.status != http.StatusOK {
		t.Fatalf("workday_start = %d %s", a.status, a.body)
	}
	if got := api.json(http.MethodGet, "/api/v1/teams/"+tp, "", http.StatusOK)["working_hours"].(map[string]any); got["start"] != "08:00" {
		t.Errorf("the Team's start = %v", got)
	}

	// C-25.FR-2, C-25.AC-11: the owning Team and "My Teams".
	var profiles struct {
		Items []struct {
			Policy json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	route := api.json(http.MethodPost, "/api/v1/routes", `{"name":"pay","matchers":[{"label":"team","op":"=",`+
		`"value":"payments"}],"owning_team_id":"`+tp+`","urgent":false,"group_key":["alertname"],"destination_ids":[],`+
		`"policy":`+string(profiles.Items[0].Policy)+`}`, http.StatusCreated)
	rid := route["id"].(string)
	names := func(c patClient, query string) string {
		t.Helper()
		var out []string
		for _, it := range c.json(http.MethodGet, "/api/v1/routes?"+query, "", http.StatusOK)["items"].([]any) {
			out = append(out, it.(map[string]any)["name"].(string))
		}
		return strings.Join(out, ",")
	}
	if got := names(b, "team=mine"); got != "pay" {
		t.Errorf("bob's teams: %q", got)
	}
	if got := names(v, "team=mine"); got != "pay" {
		t.Errorf("vic, a Viewer in Payments: %q", got)
	}

	// C-25.AC-6: a Viewer in a Team is refused Acknowledge as any Viewer is.
	in := api.json(http.MethodPost, "/api/v1/integrations", `{"name":"teams","connection_mode":"webhook_only",`+
		`"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)["id"].(string)
	intToken := api.json(http.MethodPost, "/api/v1/integrations/"+in+"/tokens", `{"name":"teams"}`,
		http.StatusCreated)["value"].(string)
	if code := postIngest(t, r.Ingest+"/api/v1/ingest", intToken, `{"version":"4","groupKey":"{}:{alertname=\"PayDown\"}",`+
		`"status":"firing","receiver":"teams","alerts":[{"status":"firing","labels":{"alertname":"PayDown",`+
		`"team":"payments"},"annotations":{},"startsAt":"2026-10-27T08:00:00Z","endsAt":"0001-01-01T00:00:00Z",`+
		`"fingerprint":"teams-1"}]}`); code != http.StatusAccepted {
		t.Fatalf("ingest = %d", code)
	}
	var group string
	eventually(t, "the Alert Group of payments", func() bool {
		items, _ := api.json(http.MethodGet, "/api/v1/alert-groups?route="+rid, "", http.StatusOK)["items"].([]any)
		if len(items) > 0 {
			group = items[0].(map[string]any)["id"].(string)
		}
		return group != ""
	})
	if a := v.do(http.MethodPost, "/api/v1/alert-groups/"+group+"/acknowledge", ""); a.status != http.StatusForbidden ||
		!strings.Contains(string(a.body), "alert-groups:acknowledge") {
		t.Errorf("vic's Acknowledge = %d %s", a.status, a.body)
	}
	if got := names(api, "team=mine"); got != "" {
		t.Errorf("the Admin is in no Team: %q", got)
	}
	if got := names(api, "team="+tp); got != "pay" {
		t.Errorf("payments: %q", got)
	}
	if a := ci.do(http.MethodGet, "/api/v1/routes?team=mine", ""); a.status != http.StatusUnprocessableEntity {
		t.Errorf("a service account's mine = %d %s", a.status, a.body)
	}
	owner := api.json(http.MethodGet, "/api/v1/routes/"+rid, "", http.StatusOK)["owning_team"].(map[string]any)
	if owner["id"] != tp || owner["name"] != "Payments" || owner["slug"] != "payments" || owner["deleted"] != false {
		t.Errorf("owning team %v", owner)
	}
	if a := api.do(http.MethodPost, "/api/v1/routes", `{"name":"x","matchers":[],"owning_team_id":"TM0000000000ZZ",`+
		`"urgent":false,"group_key":[],"destination_ids":[],"policy":`+string(profiles.Items[0].Policy)+`}`); a.status !=
		http.StatusUnprocessableEntity || !strings.Contains(string(a.body), "unknown_id") {
		t.Errorf("an unknown team = %d %s", a.status, a.body)
	}

	// C-25.FR-10, C-25.AC-9: deletion.
	routeEtag := api.do(http.MethodGet, "/api/v1/routes/"+rid, "").header.Get("ETag")
	if a := api.do(http.MethodDelete, "/api/v1/teams/"+tp, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", a.status, a.body)
	}
	after := api.do(http.MethodGet, "/api/v1/routes/"+rid, "")
	if !strings.Contains(string(after.body), `"owning_team":null`) || after.header.Get("ETag") == routeEtag {
		t.Errorf("the route after the deletion: %s %s", after.header.Get("ETag"), after.body)
	}
	if got := h.count(t, `SELECT count(*) FROM teams WHERE public_id = '`+tp+`' AND deleted_at IS NOT NULL`); got != 1 {
		t.Error("the deleted Team's row is not kept")
	}
	if got := h.count(t, `SELECT count(*) FROM audit_log WHERE action = 'route.updated' AND actor_kind = 'system'
		AND resource_public_id = '`+rid+`'`); got != 1 {
		t.Errorf("%d route.updated by the system", got)
	}
	tp2 := api.json(http.MethodPost, "/api/v1/teams", `{"name":"Payments","slug":"payments","own_working_hours":false}`,
		http.StatusCreated)["id"].(string)

	// C-25.AC-12: disabling keeps a User in their Teams, deleting takes them out.
	api.json(http.MethodPost, "/api/v1/teams/"+tp2+"/users", `{"user_id":"`+bob+`"}`, http.StatusCreated)
	api.json(http.MethodPost, "/api/v1/users/"+bob+"/disable", "", http.StatusOK)
	if got := api.json(http.MethodGet, "/api/v1/teams/"+tp2, "", http.StatusOK)["user_count"]; got != 1.0 {
		t.Errorf("after disable: %v", got)
	}
	if a := api.do(http.MethodDelete, "/api/v1/users/"+bob, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete bob = %d %s", a.status, a.body)
	}
	if got := api.json(http.MethodGet, "/api/v1/teams/"+tp2, "", http.StatusOK)["user_count"]; got != 0.0 {
		t.Errorf("after delete: %v", got)
	}
	for _, action := range []string{"team.created", "team.updated", "team.deleted", "team.user_added",
		"team.user_removed"} {
		if got := h.count(t, `SELECT count(*) FROM audit_log WHERE action = '`+action+`'`); got < 1 {
			t.Errorf("no %s in the Audit log", action)
		}
	}
	eventually(t, "one muster_team_info series", func() bool {
		return strings.Count(string(call(t, http.MethodGet, r.Internal+"/metrics", "").body), "muster_team_info{") == 1
	})
}
