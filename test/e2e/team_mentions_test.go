// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
)

// TestTeamMentions is S-139 against `muster dev`, as its Verification: a Team mention for the demo Mattermost
// Connection is saved and one for the demo Telegram Connection refused (C-25.AC-7); the demo Slack Connection is refused
// until its check finds usergroups:read, then lists its user groups and takes one (C-24.FR-1, FR-2); the Mattermost
// Connection lists its groups; the demo Bridge Connection can mention groups once the fake bridge declares them in
// info, which the check shows (C-22.FR-2); a lost ability keeps the Team mention unavailable (C-25.FR-8). Beyond the
// Verification: a Mattermost bot that may not read groups (the step groups passes and says so, groups_read false, a
// typed Team mention is still saved), a deleted Connection whose Team mentions go with an entry in the Team's Audit
// log (C-25.FR-11), listConnectionGroups of a Telegram and a Bridge Connection answering 409, and the demo Notification
// gateway never offered.
func TestTeamMentions(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	api := patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"mentions","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)}
	fsl, fb, fmm := h.Fakes.Slack+"/_fake", h.Fakes.Bridge+"/_fake", h.Fakes.Mattermost+"/_fake"
	capabilities, _ := json.Marshal(fakebridge.DefaultConfig().Capabilities)
	t.Cleanup(func() {
		restore(t, http.MethodPut, fsl+"/config", `{"scopes":{"a":null}}`)
		restore(t, http.MethodPut, fb+"/config", `{"capabilities":`+string(capabilities)+`}`)
		restore(t, http.MethodPut, fmm+"/config", `{"groups_forbidden":false}`)
	})
	ids := map[string]string{}
	for _, item := range api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
		c := item.(map[string]any)
		ids[c["name"].(string)] = c["id"].(string)
	}
	cm, ct, cs, cb := ids[devmode.ConnectionName], ids[devmode.TelegramConnectionName],
		ids[devmode.SlackConnectionName], ids[devmode.BridgeConnectionName]
	gw := ids[devmode.GatewayConnectionName]
	if cm == "" || ct == "" || cs == "" || cb == "" || gw == "" {
		t.Fatalf("demo connections %v", ids)
	}
	tp := api.json(http.MethodPost, "/api/v1/teams", `{"name":"Payments","slug":"payments","own_working_hours":false}`,
		http.StatusCreated)["id"].(string)

	team := func() map[string]any { return api.json(http.MethodGet, "/api/v1/teams/"+tp, "", http.StatusOK) }
	// tm saves the Team with mentions as its team_mentions, the rest as read, with the ETag of the read.
	tm := func(mentions string) answer {
		t.Helper()
		read := api.do(http.MethodGet, "/api/v1/teams/"+tp, "")
		var body map[string]any
		decode(t, read, &body)
		in := map[string]any{"team_mentions": json.RawMessage(mentions)}
		for _, k := range []string{"name", "slug", "description", "own_working_hours", "matchers", "idp_entries",
			"destination_entries"} {
			in[k] = body[k]
		}
		b, _ := json.Marshal(in)
		return api.do(http.MethodPut, "/api/v1/teams/"+tp, string(b), "If-Match", read.header.Get("ETag"))
	}
	check := func(id string) map[string]any {
		t.Helper()
		return api.json(http.MethodPost, "/api/v1/connections/"+id+"/checks", "", http.StatusOK)
	}
	configure := func(url, body string) {
		t.Helper()
		if a := call(t, http.MethodPut, url+"/config", body); a.status != http.StatusOK {
			t.Fatalf("PUT %s/config %s = %d %s", url, body, a.status, a.body)
		}
	}
	mentioned := func(m map[string]any) []string {
		var out []string
		for _, x := range m["team_mentions"].([]any) {
			v := x.(map[string]any)
			out = append(out, fmt.Sprintf("%s:%s:%v:%v", v["connection"].(map[string]any)["type"], v["group"],
				v["available"], v["unavailable_reason"]))
		}
		return out
	}
	connectionOf := func(m map[string]any, id string) map[string]any {
		for _, x := range m["mention_connections"].([]any) {
			if c := x.(map[string]any); c["connection"].(map[string]any)["id"] == id {
				return c
			}
		}
		return nil
	}
	without := `chat:write,channels:read,groups:read,im:write,users:read,users:read.email`

	// C-25.AC-7: Mattermost saved; Telegram refused.
	a := tm(`[{"connection_id":"` + cm + `","group":"payments_team"}]`)
	var saved map[string]any
	decode(t, a, &saved)
	if a.status != http.StatusOK || !slices.Equal(mentioned(saved), []string{"mattermost:payments_team:true:<nil>"}) {
		t.Fatalf("a Mattermost Team mention = %d %s", a.status, a.body)
	}
	a = tm(`[{"connection_id":"` + ct + `","group":"x"}]`)
	if p, c := firstError(t, a); a.status != http.StatusUnprocessableEntity || c != "group_mentions_unsupported" ||
		p != "/team_mentions/0/connection_id" {
		t.Fatalf("a Telegram Team mention = %d %s", a.status, a.body)
	}

	// Slack: refused until the check finds usergroups:read.
	check(cs)
	if got := api.json(http.MethodGet, "/api/v1/connections/"+cs, "", http.StatusOK)["usergroups_read"]; got != false {
		t.Fatalf("usergroups_read before the scope = %v", got)
	}
	slackMention := `{"connection_id":"` + cs + `","group":"payments-team","group_id":"` +
		fakeslack.UserGroupPayments + `"}`
	if _, c := firstError(t, tm(`[`+slackMention+`]`)); c != "group_mentions_unsupported" {
		t.Fatalf("a Slack Team mention without the scope = %s", c)
	}
	if a := api.do(http.MethodGet, "/api/v1/connections/"+cs+"/groups", ""); a.status != http.StatusConflict ||
		!strings.Contains(string(a.body), `"code":"groups_not_listed"`) {
		t.Errorf("the user groups without the scope = %d %s", a.status, a.body)
	}
	configure(fsl, `{"scopes":{"a":"`+without+`,usergroups:read"}}`)
	res := check(cs)
	var scopes map[string]any
	for _, s := range res["steps"].([]any) {
		if st := s.(map[string]any); st["name"] == "scopes" {
			scopes = st
		}
	}
	if fmt.Sprint(scopes["optional_scopes"]) != "[map[present:true scope:usergroups:read]]" || scopes["ok"] != true {
		t.Errorf("the step scopes %v", scopes)
	}
	if got := api.json(http.MethodGet, "/api/v1/connections/"+cs, "", http.StatusOK)["usergroups_read"]; got != true {
		t.Fatalf("usergroups_read with the scope = %v", got)
	}
	var groups struct {
		Items []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			MemberCount *int   `json:"member_count"`
		} `json:"items"`
		Truncated bool `json:"truncated"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/connections/"+cs+"/groups", ""), &groups)
	if len(groups.Items) != 1 || groups.Items[0].ID != fakeslack.UserGroupPayments ||
		groups.Items[0].Name != "payments-team" || *groups.Items[0].MemberCount != 1 || groups.Truncated {
		t.Fatalf("the user groups %+v", groups)
	}
	a = tm(`[{"connection_id":"` + cm + `","group":"payments_team"},` + slackMention + `]`)
	decode(t, a, &saved)
	if a.status != http.StatusOK || !slices.Equal(mentioned(saved), []string{"mattermost:payments_team:true:<nil>",
		"slack:payments-team:true:<nil>"}) {
		t.Fatalf("Mattermost and Slack = %d %s", a.status, a.body)
	}

	// The Mattermost groups; the bridge declares group mentions.
	var mmGroups struct {
		Items []struct {
			Name        string `json:"name"`
			MemberCount int    `json:"member_count"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/connections/"+cm+"/groups", ""), &mmGroups)
	if fmt.Sprint(mmGroups.Items) != "[{devops_team 1} {payments_team 1}]" {
		t.Errorf("the Mattermost groups %+v", mmGroups.Items)
	}
	if c := connectionOf(team(), cb); c == nil || c["available"] != false ||
		c["unavailable_reason"] != "bridge_no_group_mentions" || c["team_mention"] != nil {
		t.Errorf("the bridge before it declares group mentions %v", c)
	}
	if c := connectionOf(team(), gw); c != nil {
		t.Errorf("the Notification gateway is offered %v", c)
	}
	if c := connectionOf(team(), ct); c == nil || c["unavailable_reason"] != "telegram_no_groups" {
		t.Errorf("the Telegram Connection %v", c)
	}
	configure(fb, `{"capabilities":{"native_mentions":true,"group_mentions":true,"everyone_mentions":["channel","here"]}}`)
	check(cb)
	if c := connectionOf(team(), cb); c == nil || c["available"] != true || c["unavailable_reason"] != nil {
		t.Errorf("the bridge after it declares group mentions %v", c)
	}
	info := api.json(http.MethodGet, "/api/v1/connections/"+cb, "", http.StatusOK)["info"].(map[string]any)
	if caps := info["capabilities"].(map[string]any); caps["group_mentions"] != true ||
		fmt.Sprint(caps["everyone_mentions"]) != "[channel here]" {
		t.Errorf("the stored info %v", caps)
	}
	for _, id := range []string{ct, cb} {
		if a := api.do(http.MethodGet, "/api/v1/connections/"+id+"/groups", ""); a.status != http.StatusConflict ||
			!strings.Contains(string(a.body), `"code":"groups_not_listed"`) {
			t.Errorf("the groups of %s = %d %s", id, a.status, a.body)
		}
	}

	// A lost ability keeps the Team mention, unavailable.
	configure(fsl, `{"scopes":{"a":"`+without+`"}}`)
	check(cs)
	if got := mentioned(team()); !slices.Contains(got, "slack:payments-team:false:usergroups_read_missing") {
		t.Errorf("after the scope was lost %v", got)
	}
	// The full set sent again unchanged is saved; the unavailable Team mention stays.
	a = tm(`[{"connection_id":"` + cm + `","group":"payments_team"},` + slackMention + `]`)
	decode(t, a, &saved)
	if a.status != http.StatusOK || !slices.Equal(mentioned(saved), []string{"mattermost:payments_team:true:<nil>",
		"slack:payments-team:false:usergroups_read_missing"}) {
		t.Errorf("the unchanged set after the scope was lost = %d %s", a.status, a.body)
	}

	// A Mattermost bot that may not read groups: the step groups passes and says so; a typed Team mention is saved.
	configure(fmm, `{"groups_forbidden":true}`)
	res = check(cm)
	steps := res["steps"].([]any)
	last := steps[len(steps)-1].(map[string]any)
	if res["ok"] != true || len(steps) != 2 || last["name"] != "groups" || last["ok"] != true ||
		!strings.Contains(fmt.Sprint(last["message"]), "api.context.permissions.app_error") ||
		strings.Contains(fmt.Sprint(res["warnings"]), "group") {
		t.Errorf("the check of a bot that may not read groups %v", res)
	}
	if got := api.json(http.MethodGet, "/api/v1/connections/"+cm, "", http.StatusOK)["groups_read"]; got != false {
		t.Errorf("groups_read = %v", got)
	}
	if a := api.do(http.MethodGet, "/api/v1/connections/"+cm+"/groups", ""); a.status != http.StatusConflict {
		t.Errorf("the groups of a bot that may not read them = %d %s", a.status, a.body)
	}
	if a := tm(`[{"connection_id":"` + cm + `","group":"oncall_team"}]`); a.status != http.StatusOK ||
		!slices.Equal(mentioned(team()), []string{"mattermost:oncall_team:true:<nil>"}) {
		t.Errorf("a typed Team mention = %d %s", a.status, a.body)
	}
	configure(fmm, `{"groups_forbidden":false}`)
	check(cm)
	if got := api.json(http.MethodGet, "/api/v1/connections/"+cm, "", http.StatusOK)["groups_read"]; got != true {
		t.Errorf("groups_read after the read is allowed = %v", got)
	}

	// C-25.FR-11: a deleted Connection takes its Team mentions with it, recorded in the Team's Audit log.
	extra := api.json(http.MethodPost, "/api/v1/connections", `{"type":"mattermost","name":"Second Mattermost",`+
		`"server_url":"`+h.Fakes.MattermostSecond+`","bot_token":"`+devmode.MattermostSecondBotToken+`",`+
		`"proxy":{"enabled":false},"limiter":{"limit":5,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	if a := tm(`[{"connection_id":"` + cm + `","group":"oncall_team"},{"connection_id":"` + extra +
		`","group":"payments_team"}]`); a.status != http.StatusOK {
		t.Fatalf("a Team mention for the second Connection = %d %s", a.status, a.body)
	}
	before := api.do(http.MethodGet, "/api/v1/teams/"+tp, "").header.Get("ETag")
	if a := api.do(http.MethodDelete, "/api/v1/connections/"+extra, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete the second Connection = %d %s", a.status, a.body)
	}
	if got := mentioned(team()); !slices.Equal(got, []string{"mattermost:oncall_team:true:<nil>"}) {
		t.Errorf("the Team mentions after the deletion %v", got)
	}
	if after := api.do(http.MethodGet, "/api/v1/teams/"+tp, "").header.Get("ETag"); after == before {
		t.Errorf("the Team's ETag did not change: %s", after)
	}
	if c := connectionOf(team(), extra); c != nil {
		t.Errorf("the deleted Connection is offered %v", c)
	}
	var entries struct {
		Items []struct {
			Action  string          `json:"action"`
			Diff    json.RawMessage `json:"diff"`
			Details json.RawMessage `json:"details"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/audit-log?action=team.updated&resource_id="+tp+"&limit=1", ""),
		&entries)
	if len(entries.Items) != 1 || !strings.Contains(string(entries.Items[0].Diff), `"/team_mentions"`) ||
		!strings.Contains(string(entries.Items[0].Details), "connection_deleted") ||
		!strings.Contains(string(entries.Items[0].Details), extra) {
		t.Errorf("the Team's Audit log %+v", entries.Items)
	}
}
