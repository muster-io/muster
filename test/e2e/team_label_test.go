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

// teamOf is a Team as the reads of Alerts and Alert Groups carry it.
type teamOf struct {
	Team struct {
		ID      string `json:"id"`
		Slug    string `json:"slug"`
		Name    string `json:"name"`
		Deleted bool   `json:"deleted"`
	} `json:"team"`
	Rule    string  `json:"rule"`
	Matcher *string `json:"matcher"`
}

// withoutTeamLabel moves the Team label (C-25.FR-5) off the label team, which a test routes by and asserts its
// Timeline or webhook events for, so that no unknown_team_value joins them: such a test is not about Teams.
func withoutTeamLabel(api patClient) {
	api.t.Helper()
	if a := putOrganization(api, func(m map[string]any) { m["team_label"] = "muster_team" }); a.status != http.StatusOK {
		api.t.Fatalf("move the team label = %d %s", a.status, a.body)
	}
}

// slugs are the slugs of Teams, sorted, with their rule and matcher.
func slugs(ts []teamOf) []string {
	out := []string{}
	for _, t := range ts {
		s := t.Team.Slug + "/" + t.Rule
		if t.Matcher != nil {
			s += "/" + *t.Matcher
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// TestTeamLabel is S-118 against `muster dev`, as its Verification: the Teams of newly firing Alerts by the Team label
// and by Team matchers, and a value without a Team (C-25.AC-1, C-25.AC-2); the Teams of the Alert Group with
// team_joined and unknown_team_value, which stay after their Alerts resolve (C-25.AC-5); "My Teams" and the filter by
// Team (C-25.AC-11); the Team label that an Internal alert sets refused (C-25.FR-5); a deleted Team kept by name
// (C-25.AC-9); and an outgoing webhook in events mode that receives team_joined with alert_group.teams (C-15.FR-2).
func TestTeamLabel(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	bob, b := personWithToken(t, admin, r.App, "bob", "responder")
	api := e.api

	team := func(body string) string {
		t.Helper()
		return api.json(http.MethodPost, "/api/v1/teams", body, http.StatusCreated)["id"].(string)
	}
	tp := team(`{"name":"payments","slug":"payments","own_working_hours":false}`)
	tc := team(`{"name":"checkout","slug":"checkout-team","own_working_hours":false,"matchers":[` +
		`{"label":"namespace","op":"=~","value":"checkout|billing"},{"label":"pod","op":"=~","value":"checkout-.*"}]}`)
	ts := team(`{"name":"search","slug":"search","own_working_hours":false}`)
	api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+bob+`"}`, http.StatusCreated)

	// C-25.FR-1, FR-11: an invalid regular expression is refused at its pointer.
	if a := api.do(http.MethodPost, "/api/v1/teams", `{"name":"bad","slug":"bad","own_working_hours":false,`+
		`"matchers":[{"label":"pod","op":"=~","value":"checkout-("}]}`); a.status != http.StatusUnprocessableEntity {
		t.Errorf("an invalid regex = %d %s", a.status, a.body)
	} else if p, c := firstError(t, a); p != "/matchers/0/value" || c != "invalid_regex" {
		t.Errorf("an invalid regex at %s %s", p, c)
	}

	// The Route takes every OOMKilled Alert into one Alert Group and sends its events to the fake receiver.
	hook := e.webhook("teams-events", "teams", `[]`)
	var profiles struct {
		Items []struct {
			ID     string         `json:"id"`
			Policy map[string]any `json:"policy"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	var policy map[string]any
	for _, p := range profiles.Items {
		if p.ID == "paging" {
			policy = p.Policy
		}
	}
	route, _ := json.Marshal(map[string]any{"name": "teams", "urgent": false, "group_key": []string{"alertname"},
		"matchers":        []map[string]string{{"label": "alertname", "op": "=", "value": "OOMKilled"}},
		"destination_ids": []string{hook["id"].(string)}, "policy": policy})
	api.json(http.MethodPost, "/api/v1/routes", string(route), http.StatusCreated)

	// C-25.AC-1, C-25.AC-2: by the label, an unknown value, by a matcher, and two Teams.
	e.group("k1", "OOMKilled")
	e.alert("k1", "a", `{"team":"payments","namespace":"billing","severity":"warning","cluster":"ta"}`, "")
	e.alert("k1", "b", `{"team":"Payments","pod":"x-1","severity":"warning","cluster":"tb"}`, "")
	e.alert("k1", "c", `{"namespace":"search","severity":"warning","cluster":"tc"}`, "")
	e.alert("k1", "p", `{"pod":"checkout-api-1","severity":"warning","cluster":"tp"}`, "")
	e.notify("k1", "first notification")
	alertTeams := func(cluster string) []string {
		t.Helper()
		var page struct {
			Items []struct {
				Teams []teamOf `json:"teams"`
			} `json:"items"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/integrations/"+e.integration+"/alerts?"+
			url.Values{"label": {`cluster="` + cluster + `"`}}.Encode(), ""), &page)
		if len(page.Items) != 1 || page.Items[0].Teams == nil {
			t.Fatalf("alerts of cluster %s: %+v", cluster, page)
		}
		return slugs(page.Items[0].Teams)
	}
	for cluster, want := range map[string][]string{
		"ta": {`checkout-team/matcher/namespace=~"checkout|billing"`, "payments/label"},
		"tb": {}, "tc": {}, "tp": {`checkout-team/matcher/pod=~"checkout-.*"`},
	} {
		if got := alertTeams(cluster); !slices.Equal(got, want) {
			t.Errorf("teams of the alert of %s = %v, want %v", cluster, got, want)
		}
	}
	if n := h.count(t, `SELECT count(*) FROM alerts WHERE labels->>'cluster' = 'tb'
		AND unknown_team_value = 'Payments'`); n != 1 {
		t.Errorf("%d alerts with the unknown value Payments", n)
	}
	g := e.groupOf("ta")
	groupTeams := func() []teamOf {
		t.Helper()
		var v struct {
			Teams []teamOf `json:"teams"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/alert-groups/"+g, ""), &v)
		return v.Teams
	}
	type entry struct {
		Event    string `json:"event"`
		Kind     string `json:"kind"`
		Loudness string `json:"loudness"`
		TeamRef  *struct {
			Slug string `json:"slug"`
		} `json:"team"`
		TeamRule  *string `json:"team_rule"`
		TeamValue *string `json:"team_value"`
	}
	teamEntries := func() []string {
		t.Helper()
		var page struct {
			Items []entry `json:"items"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?limit=100", ""), &page)
		var out []string
		for _, x := range page.Items {
			switch x.Event {
			case "team_joined":
				out = append(out, x.Event+"/"+x.Kind+"/"+x.Loudness+"/"+x.TeamRef.Slug+"/"+*x.TeamRule)
			case "unknown_team_value":
				out = append(out, x.Event+"/"+x.Kind+"/"+x.Loudness+"/"+*x.TeamValue)
			}
		}
		slices.Sort(out)
		return out
	}
	want := []string{"team_joined/alerts/quiet/checkout-team/matcher", "team_joined/alerts/quiet/payments/label",
		"unknown_team_value/system/quiet/Payments"}
	if got := teamEntries(); !slices.Equal(got, want) {
		t.Errorf("timeline %v, want %v", got, want)
	}
	if got := slugs(groupTeams()); !slices.Equal(got, []string{`checkout-team/matcher/namespace=~"checkout|billing"`,
		"payments/label"}) {
		t.Errorf("teams of the alert group %v", got)
	}

	// C-25.AC-5: a Team joins later and stays after its Alerts resolve; a value is recorded once.
	e.alert("k1", "d", `{"team":"search","severity":"warning","cluster":"td"}`, "")
	e.alert("k1", "f", `{"team":"Payments","severity":"warning","cluster":"tf"}`, "")
	e.notify("k1", "new alerts")
	want = append(want, "team_joined/alerts/quiet/search/label")
	slices.Sort(want)
	if got := teamEntries(); !slices.Equal(got, want) {
		t.Errorf("timeline after search %v, want %v", got, want)
	}
	e.fake(http.MethodDelete, e.fam+"/groups/k1/alerts/d", "")
	e.notify("k1", "resolved")
	three := []string{"checkout-team", "payments", "search"}
	got := []string{}
	for _, x := range groupTeams() {
		got = append(got, x.Team.Slug)
	}
	if slices.Sort(got); !slices.Equal(got, three) {
		t.Errorf("teams after the alert of search resolved %v", got)
	}
	if n := h.count(t, `SELECT count(*) FROM alert_group_teams gt JOIN alert_groups g ON g.id = gt.alert_group_id
		WHERE g.public_id = '`+g+`'`); n != 3 {
		t.Errorf("%d rows of alert_group_teams", n)
	}

	// C-25.AC-11: "My Teams" and the filter by Team.
	listed := func(c patClient, query string) []string {
		t.Helper()
		var page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		decode(t, c.do(http.MethodGet, "/api/v1/alert-groups?"+query, ""), &page)
		var out []string
		for _, x := range page.Items {
			out = append(out, x.ID)
		}
		return out
	}
	if got := listed(b, "team=mine"); !slices.Contains(got, g) {
		t.Errorf("bob's teams %v", got)
	}
	if got := listed(api, "team=mine"); len(got) != 0 {
		t.Errorf("the admin, in no team, %v", got)
	}
	if got := listed(api, "team="+ts); len(got) != 1 || got[0] != g {
		t.Errorf("search %v", got)
	}
	counts := b.json(http.MethodGet, "/api/v1/alert-group-counts?team=mine", "", http.StatusOK)
	if counts["firing"] != float64(1) {
		t.Errorf("counts %v", counts)
	}

	// C-25.FR-5, C-20.FR-1: the Team label may not be a label of an Internal alert.
	a := putOrganization(api, func(m map[string]any) { m["team_label"] = "team_id" })
	if p, c := firstError(t, a); a.status != http.StatusUnprocessableEntity || p != "/team_label" || c != "reserved_label" {
		t.Errorf("team_id = %d %s", a.status, a.body)
	}
	if o := api.json(http.MethodGet, "/api/v1/organization", "", http.StatusOK); o["team_label"] != "team" {
		t.Errorf("team label %v", o["team_label"])
	}

	// C-25.FR-11: matchers saved with If-Match and recorded in the diff of team.updated.
	etag := api.do(http.MethodGet, "/api/v1/teams/"+tc, "").header.Get("ETag")
	api.json(http.MethodPut, "/api/v1/teams/"+tc, `{"name":"checkout","slug":"checkout-team","own_working_hours":false,`+
		`"matchers":[{"label":"pod","op":"=~","value":"checkout-.*"}]}`, http.StatusOK, "If-Match", etag)
	var audit struct {
		Items []struct {
			Diff []map[string]any `json:"diff"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/audit-log?action=team.updated&limit=1", ""), &audit)
	if len(audit.Items) != 1 || len(audit.Items[0].Diff) != 1 || audit.Items[0].Diff[0]["pointer"] != "/matchers" {
		t.Errorf("team.updated %+v", audit.Items)
	}

	// C-25.AC-9: a deleted Team stays by name.
	api.json(http.MethodDelete, "/api/v1/teams/"+ts, "", http.StatusNoContent)
	for _, x := range groupTeams() {
		if x.Team.Slug == "search" && (!x.Team.Deleted || x.Team.Name != "search") {
			t.Errorf("deleted team %+v", x)
		}
	}

	// C-15.FR-2: the outgoing webhook receives team_joined and unknown_team_value with alert_group.teams.
	type record struct {
		Body struct {
			Event      string `json:"event"`
			Notify     bool   `json:"notify"`
			AlertGroup struct {
				ID    string `json:"id"`
				Teams []struct {
					ID   string `json:"id"`
					Slug string `json:"slug"`
				} `json:"teams"`
			} `json:"alert_group"`
		} `json:"body"`
	}
	var events []record
	eventually(t, "the team events at the receiver", func() bool {
		decode(t, call(t, http.MethodGet, webhookBase+"/_fake/received/teams", ""), &events)
		n := 0
		for _, x := range events {
			if x.Body.Event == "team_joined" {
				n++
			}
		}
		return n == 3
	})
	var names []string
	for _, x := range events {
		names = append(names, x.Body.Event)
		if x.Body.AlertGroup.ID != g || x.Body.AlertGroup.Teams == nil {
			t.Errorf("event %+v", x.Body)
		}
		if x.Body.Event == "team_joined" && x.Body.Notify {
			t.Error("team_joined is Loud")
		}
	}
	if !strings.Contains(strings.Join(names, ","), "created,team_joined,team_joined,unknown_team_value") ||
		strings.Count(strings.Join(names, ","), "unknown_team_value") != 1 {
		t.Errorf("events %v", names)
	}
	last := events[len(events)-1].Body.AlertGroup.Teams
	if len(last) != 3 || last[0].Slug != "payments" || last[1].Slug != "checkout-team" || last[2].Slug != "search" {
		t.Errorf("teams of the last event %+v", last)
	}
}
