// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// chainRead is an Escalation chain as a read gives it.
type chainRead struct {
	ID         string                 `json:"id"`
	OwningTeam *struct{ Slug string } `json:"owning_team"`
	Steps      []map[string]any       `json:"steps"`
	Referrers  struct {
		Routes []struct {
			Route struct{ Name string } `json:"route"`
			Cond  string                `json:"severity_condition"`
		} `json:"routes"`
		Teams []struct {
			Team struct{ Slug string } `json:"team"`
		} `json:"teams"`
	} `json:"referrers"`
	Destinations []struct{ Name string } `json:"destinations"`
	Warnings     []struct {
		Kind        string                 `json:"kind"`
		Step        *int                   `json:"step"`
		Destination *struct{ Name string } `json:"destination"`
		Reason      *string                `json:"reason"`
	} `json:"warnings"`
}

// TestEscalationChains is S-154 against `muster dev`, as its Verification: the payments chain of scenario 1 of C-32
// with its owning Team and its steps numbered from 1 (C-32.FR-1, FR-3); a Viewer refused in a step with not_pageable
// (C-32.AC-15); a wait outside escalation.limits and one of 90 s; the chain named by its Team, which reads the Team and
// its Destinations, and refuses its deletion with chain_in_use (C-32.FR-2); a Schedule a step names refused with
// schedule_in_use (C-27.AC-8); a Team that owns a chain refused with team_in_use (C-25.FR-10); a Responder refused
// with 403 and a Viewer reading the chains of a Team; muster_escalation_chain_info. Beyond it: a call step with one
// gateway, with two (required at gateway_id) and with none (gateway_required), and a chain whose gateway was deleted
// (gateway_deleted, C-30.FR-9); a webhook step naming a Mattermost Destination (webhook_events_required) and one whose
// Destination was deleted (destination_deleted); the chain named by a Route with critical; a Telegram Destination of the
// Team that makes everyone_unsupported on step 6 (C-32.FR-17); pages_nobody for an empty chain and a webhook-only one,
// and team_deleted for a step naming a deleted Team (C-32.FR-17 revised for L2); a stale and a missing If-Match; a
// person disabled after the save, whom a rename of the chain keeps; the Audit log of each change.
func TestEscalationChains(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	api := e.api
	admin := newAgentSignedIn(t, r)
	_, rtok := personWithToken(t, admin, r.App, "rita", "responder")
	vic, vtok := personWithToken(t, admin, r.App, "vic", "viewer")
	petr, _ := personWithToken(t, admin, r.App, "petr", "responder")
	dora, _ := personWithToken(t, admin, r.App, "dora", "responder")
	conns := map[string]string{}
	for _, item := range api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
		c := item.(map[string]any)
		conns[c["name"].(string)] = c["id"].(string)
	}
	gw := conns[devmode.GatewayConnectionName]

	team := func(name string) string {
		t.Helper()
		return api.json(http.MethodPost, "/api/v1/teams", `{"name":"`+name+`","slug":"`+name+`","own_working_hours":false}`,
			http.StatusCreated)["id"].(string)
	}
	td, tp, tc := team("devops"), team("payments"), team("checkout")
	for _, id := range []string{petr, dora} {
		api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+id+`"}`, http.StatusCreated)
	}
	monday := readClock(t, r).Now.UTC().AddDate(0, 0, -7).Format(time.DateOnly)
	schedule := func(teamID, name string) string {
		t.Helper()
		return api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+teamID+`","name":"`+name+`",`+
			`"time_zone":"UTC","rotation_layers":[{"name":"Week","user_ids":["`+petr+`"],"shift_length":"week",`+
			`"handover_time":"10:00","handover_day":"monday","start_date":"`+monday+`","limits":null}]}`,
			http.StatusCreated)["id"].(string)
	}
	sm, sl := schedule(tp, "Main"), schedule(tp, "Second line")
	api.json(http.MethodPost, "/api/v1/teams/"+td+"/users", `{"user_id":"`+petr+`"}`, http.StatusCreated)
	dm := schedule(td, "Main")
	cm := e.connection(200)
	critical := e.destination("payments-critical", cm, "ch-payments-critical", nil, 50)
	notice := e.destination("payments-notice", cm, "ch-payments-notice", nil, 50)
	infra := e.destination("infra-notice", cm, "ch-infra-notice", nil, 50)

	read := func(id string) chainRead {
		t.Helper()
		var c chainRead
		decode(t, api.do(http.MethodGet, "/api/v1/escalation-chains/"+id, ""), &c)
		return c
	}
	refusal := func(what string, a answer, status int, pointer, code string) {
		t.Helper()
		if p, c := firstError(t, a); a.status != status || p != pointer || c != code {
			t.Errorf("%s = %d %s", what, a.status, a.body)
		}
	}
	post := func(body string) answer {
		t.Helper()
		return api.do(http.MethodPost, "/api/v1/escalation-chains", body)
	}

	// The payments chain of scenario 1.
	steps := `[{"kind":"page_on_call","schedule_id":"` + sm + `","importance":"normal"},{"kind":"wait","wait_seconds":600},
		{"kind":"page_on_call","schedule_id":"` + sm + `","importance":"important","severity_condition":"critical"},
		{"kind":"wait","wait_seconds":900},
		{"kind":"page_on_call","schedule_id":"` + sl + `","importance":"important","severity_condition":"critical"},
		{"kind":"page_team","working_hours_only":true},{"kind":"repeat","repeat_from":3,"repeat_times":2}]`
	created := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"payments","owning_team_id":"`+tp+
		`","steps":`+steps+`}`, http.StatusCreated)
	cp := created["id"].(string)
	if c := read(cp); len(c.Steps) != 7 || c.Steps[6]["number"] != 7.0 || c.OwningTeam == nil ||
		c.OwningTeam.Slug != "payments" || c.Steps[5]["team"].(map[string]any)["slug"] != "payments" {
		t.Fatalf("the payments chain %+v", c)
	}

	// A Viewer, a disabled person and the limits.
	var p struct {
		Errors []struct{ Pointer, Code, Reason string }
	}
	decode(t, post(`{"name":"x","steps":[{"kind":"page_people","user_ids":["`+vic+`"],"importance":"normal"}]}`), &p)
	if len(p.Errors) != 1 || p.Errors[0].Pointer != "/steps/0/user_ids/0" || p.Errors[0].Code != "not_pageable" ||
		p.Errors[0].Reason != "viewer" {
		t.Errorf("a Viewer in a step = %+v", p)
	}
	refusal("a wait of a day and a minute", post(`{"name":"y","steps":[{"kind":"wait","wait_seconds":86460}]}`),
		http.StatusUnprocessableEntity, "/steps/0/wait_seconds", "out_of_range")
	refusal("a wait of 90 s", post(`{"name":"y","steps":[{"kind":"wait","wait_seconds":90}]}`),
		http.StatusUnprocessableEntity, "/steps/0/wait_seconds", "out_of_range")
	refusal("21 steps", post(`{"name":"y","steps":[`+strings.TrimSuffix(strings.Repeat(
		`{"kind":"wait","wait_seconds":60},`, 21), ",")+`]}`), http.StatusUnprocessableEntity, "/steps", "out_of_range")
	refusal("a field of another kind", post(`{"name":"y","steps":[{"kind":"wait","wait_seconds":60,
		"severity_condition":"any"}]}`), http.StatusUnprocessableEntity, "/steps/0/severity_condition", "unsupported")
	refusal("a repeat from itself", post(`{"name":"y","steps":[{"kind":"repeat","repeat_from":1,"repeat_times":1}]}`),
		http.StatusUnprocessableEntity, "/steps/0/repeat_from", "out_of_range")
	refusal("the whole Team without an owner", post(`{"name":"y","steps":[{"kind":"page_team"}]}`),
		http.StatusUnprocessableEntity, "/steps/0/team_id", "required")
	if a := post(`{"name":"PAYMENTS","steps":[]}`); a.status != http.StatusConflict ||
		!strings.Contains(string(a.body), `"code":"name_taken"`) {
		t.Errorf("a taken name = %d %s", a.status, a.body)
	}

	// The Team names the chain; the Destinations of the Team are where it writes.
	putTeam := func(id string, edit func(m map[string]any)) {
		t.Helper()
		a := api.do(http.MethodGet, "/api/v1/teams/"+id, "")
		var m map[string]any
		decode(t, a, &m)
		body := map[string]any{"name": m["name"], "slug": m["slug"], "own_working_hours": m["own_working_hours"]}
		edit(body)
		b, _ := json.Marshal(body)
		api.json(http.MethodPut, "/api/v1/teams/"+id, string(b), http.StatusOK, "If-Match", a.header.Get("ETag"))
	}
	putTeam(tp, func(m map[string]any) {
		m["destination_entries"] = []map[string]string{{"destination_id": critical}, {"destination_id": notice}}
		m["chain_entries"] = []map[string]string{{"chain_id": cp}}
	})
	if c := read(cp); len(c.Referrers.Teams) != 1 || c.Referrers.Teams[0].Team.Slug != "payments" ||
		len(c.Destinations) != 2 || c.Destinations[0].Name != "payments-critical" ||
		c.Destinations[1].Name != "payments-notice" || len(c.Warnings) != 0 {
		t.Errorf("the chain of the Team %+v", c)
	}
	tread := api.json(http.MethodGet, "/api/v1/teams/"+tp, "", http.StatusOK)
	if chains := tread["chains"].([]any); len(chains) != 1 || chains[0].(map[string]any)["name"] != "payments" {
		t.Errorf("the Team's chains %v", tread["chains"])
	}
	inUse := func(what string, a answer, code string) []string {
		t.Helper()
		var held struct {
			Code    string                        `json:"code"`
			InUseBy []struct{ Type, Name string } `json:"in_use_by"`
		}
		decode(t, a, &held)
		if a.status != http.StatusConflict || held.Code != code {
			t.Errorf("%s = %d %s", what, a.status, a.body)
		}
		var out []string
		for _, x := range held.InUseBy {
			out = append(out, x.Type+":"+x.Name)
		}
		return out
	}
	if by := inUse("a named chain", api.do(http.MethodDelete, "/api/v1/escalation-chains/"+cp, ""), "chain_in_use"); len(by) != 1 ||
		by[0] != "team:payments" {
		t.Errorf("chain_in_use by %v", by)
	}
	if by := inUse("a schedule a step names", api.do(http.MethodDelete, "/api/v1/schedules/"+sl, ""), "schedule_in_use"); len(by) != 1 ||
		by[0] != "escalation_chain:payments" {
		t.Errorf("schedule_in_use by %v", by)
	}
	cc := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"checkout","owning_team_id":"`+tc+
		`","steps":[{"kind":"wait","wait_seconds":300}]}`, http.StatusCreated)["id"].(string)
	if by := inUse("a team owning a chain", api.do(http.MethodDelete, "/api/v1/teams/"+tc, ""), "team_in_use"); len(by) != 1 ||
		by[0] != "escalation_chain:checkout" {
		t.Errorf("team_in_use by %v", by)
	}

	// The Roles.
	for _, req := range []struct{ method, path, body string }{{http.MethodPost, "/api/v1/escalation-chains",
		`{"name":"r","steps":[]}`}, {http.MethodPut, "/api/v1/escalation-chains/" + cp, `{"name":"r","steps":[]}`},
		{http.MethodDelete, "/api/v1/escalation-chains/" + cp, ""}} {
		if a := rtok.do(req.method, req.path, req.body, "If-Match", `"1"`); a.status != http.StatusForbidden {
			t.Errorf("%s %s by a Responder = %d", req.method, req.path, a.status)
		}
	}
	var list struct {
		Items []struct{ Name string } `json:"items"`
	}
	decode(t, vtok.do(http.MethodGet, "/api/v1/escalation-chains?team="+tp, ""), &list)
	if len(list.Items) != 1 || list.Items[0].Name != "payments" {
		t.Errorf("a Viewer's list of payments = %+v", list)
	}
	if metric := string(call(t, http.MethodGet, r.Internal+"/metrics", "").body); !strings.Contains(metric,
		`muster_escalation_chain_info{chain="`+cp+`",name="payments"} 1`) {
		t.Errorf("no info series of the chain")
	}

	// Calls through gateways: one, two, none, and a deleted one.
	callStep := func(gateway string) string {
		s := `{"kind":"call","call_target":"on_call","schedule_id":"` + dm + `"`
		if gateway != "" {
			s += `,"gateway_id":"` + gateway + `"`
		}
		return `{"name":"calls","owning_team_id":"` + td + `","steps":[` + s + `}]}`
	}
	calls := api.json(http.MethodPost, "/api/v1/escalation-chains", callStep(""), http.StatusCreated)
	ccall := calls["id"].(string)
	if g := calls["steps"].([]any)[0].(map[string]any); g["gateway_id"] != gw {
		t.Errorf("the only gateway: %v", g)
	}
	e.fake(http.MethodPut, h.Fakes.Bridge+"/_fake/config", `{"token":"`+fakebridge.SecondToken+`","roles":["gateway"]}`)
	second := api.json(http.MethodPost, "/api/v1/connections", `{"type":"bridge","name":"Second Gateway",`+
		`"base_url":"`+h.Fakes.Bridge+`","event_delivery":"poll","bot_token":"`+fakebridge.SecondToken+`",`+
		`"proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	api.json(http.MethodPost, "/api/v1/connections/"+second+"/checks", "", http.StatusOK)
	refusal("a call with two gateways", post(strings.Replace(callStep(""), `"calls"`, `"calls2"`, 1)),
		http.StatusUnprocessableEntity, "/steps/0/gateway_id", "required")
	api.json(http.MethodPost, "/api/v1/escalation-chains", strings.Replace(callStep(second), `"calls"`, `"calls2"`, 1),
		http.StatusCreated)
	api.json(http.MethodDelete, "/api/v1/connections/"+second, "", http.StatusNoContent)
	api.json(http.MethodDelete, "/api/v1/connections/"+gw, "", http.StatusNoContent)
	refusal("a call without a gateway", post(strings.Replace(callStep(""), `"calls"`, `"calls3"`, 1)),
		http.StatusUnprocessableEntity, "/steps/0", "gateway_required")
	if c := read(ccall); len(c.Warnings) != 1 || c.Warnings[0].Kind != "gateway_deleted" || c.Warnings[0].Step == nil || *c.Warnings[0].Step != 1 {
		t.Errorf("the warnings of a chain whose gateway was deleted %+v", c.Warnings)
	}

	// Webhook steps.
	refusal("a webhook step naming a Mattermost Destination", post(`{"name":"hooks","steps":[{"kind":"webhook",
		"destination_id":"`+critical+`"}]}`), http.StatusUnprocessableEntity, "/steps/0/destination_id",
		"webhook_events_required")
	hook := api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"webhook","name":"chain-events",
		"mode":"events","events":{"url":%q,"headers":[]},"proxy":{"enabled":false},"limiter":{"limit":50,
		"per_seconds":1}}`, webhookBase+"/hook/chain"), http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	ch := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"hooks","steps":[{"kind":"webhook",
		"destination_id":"`+hook+`"}]}`, http.StatusCreated)["id"].(string)
	api.json(http.MethodDelete, "/api/v1/destinations/"+hook, "", http.StatusNoContent)
	// A webhook Pages nobody: the chain warns pages_nobody, without a step, before the step's warning.
	if c := read(ch); len(c.Warnings) != 2 || c.Warnings[0].Kind != "pages_nobody" || c.Warnings[0].Step != nil ||
		c.Warnings[1].Kind != "destination_deleted" || c.Warnings[1].Destination == nil ||
		c.Warnings[1].Destination.Name != "chain-events" {
		t.Errorf("the warnings of a chain whose webhook Destination was deleted %+v", c.Warnings)
	}
	empty := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"empty","steps":[]}`, http.StatusCreated)
	if w, _ := json.Marshal(empty["warnings"]); string(w) != `[{"destination":null,"kind":"pages_nobody","reason":null,"step":null}]` {
		t.Errorf("the warnings of an empty chain %s", w)
	}

	// The chain of devops named by the Route "Infrastructure" for critical.
	cd := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"devops","owning_team_id":"`+td+
		`","steps":[{"kind":"page_on_call","schedule_id":"`+dm+`"},{"kind":"wait","wait_seconds":600},`+
		`{"kind":"page_on_call","schedule_id":"`+dm+`","importance":"important"}]}`, http.StatusCreated)["id"].(string)
	ri := e.route("Infrastructure", "infra", []string{infra}, nil)
	ra := api.do(http.MethodGet, "/api/v1/routes/"+ri, "")
	var route map[string]any
	decode(t, ra, &route)
	route["chain_entries"] = []map[string]string{{"chain_id": cd, "severity_condition": "critical"}}
	for _, k := range []string{"id", "etag", "position", "is_default", "destinations", "storm_active",
		"open_alert_group_count", "owning_team", "created_at", "chains", "destination_ids"} {
		delete(route, k)
	}
	b, _ := json.Marshal(route)
	updated := api.json(http.MethodPut, "/api/v1/routes/"+ri, string(b), http.StatusOK, "If-Match", ra.header.Get("ETag"))
	if chains := updated["chains"].([]any); len(chains) != 1 || chains[0].(map[string]any)["name"] != "devops" {
		t.Errorf("the Route's chains %v", updated["chains"])
	}
	if c := read(cd); len(c.Referrers.Routes) != 1 || c.Referrers.Routes[0].Route.Name != "Infrastructure" ||
		c.Referrers.Routes[0].Cond != "critical" || len(c.Destinations) != 1 || c.Destinations[0].Name != "infra-notice" {
		t.Errorf("the chain of the Route %+v", c)
	}

	// A Telegram Destination of the Team: step 6 cannot mention the Team there.
	member, _ := json.Marshal(faketelegram.Member{Status: faketelegram.StatusAdministrator, CanPostMessages: true})
	e.fake(http.MethodPut, fmt.Sprintf("%s/_fake/chats/%d/members/%d", h.Fakes.Telegram, faketelegram.GroupID,
		faketelegram.BotID), string(member))
	tg := api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"telegram","name":"payments-tg",
		"connection_id":%q,"channel_id":"@muster_alerts","limiter":{"limit":60,"per_seconds":60}}`,
		conns[devmode.TelegramConnectionName]), http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	putTeam(tp, func(m map[string]any) {
		m["destination_entries"] = []map[string]string{{"destination_id": critical}, {"destination_id": notice},
			{"destination_id": tg}}
	})
	if c := read(cp); len(c.Warnings) != 1 || c.Warnings[0].Kind != "everyone_unsupported" || c.Warnings[0].Step == nil || *c.Warnings[0].Step != 6 ||
		c.Warnings[0].Destination.Name != "payments-tg" || *c.Warnings[0].Reason != "telegram_no_groups" ||
		len(c.Referrers.Teams) != 1 {
		t.Errorf("the warnings with a Telegram Destination %+v", c.Warnings)
	}

	// If-Match, and a person disabled after the save, whom a rename keeps.
	people := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"people","owning_team_id":"`+tp+
		`","steps":[{"kind":"page_people","user_ids":["`+petr+`","`+dora+`"],"importance":"normal"}]}`,
		http.StatusCreated)
	cpp := people["id"].(string)
	api.json(http.MethodPost, "/api/v1/users/"+dora+"/disable", "", http.StatusOK)
	got := api.do(http.MethodGet, "/api/v1/escalation-chains/"+cpp, "")
	var body map[string]any
	decode(t, got, &body)
	body["name"] = "people-renamed"
	for _, k := range []string{"id", "etag", "created_at", "referrers", "destinations", "warnings", "owning_team"} {
		delete(body, k)
	}
	rb, _ := json.Marshal(body)
	if a := api.do(http.MethodPut, "/api/v1/escalation-chains/"+cpp, string(rb)); a.status != http.StatusPreconditionRequired {
		t.Errorf("without If-Match = %d", a.status)
	}
	if a := api.do(http.MethodPut, "/api/v1/escalation-chains/"+cpp, string(rb), "If-Match", `"99"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("a stale If-Match = %d", a.status)
	}
	renamed := api.json(http.MethodPut, "/api/v1/escalation-chains/"+cpp, string(rb), http.StatusOK, "If-Match",
		got.header.Get("ETag"))
	if users := renamed["steps"].([]any)[0].(map[string]any)["user_ids"].([]any); renamed["name"] != "people-renamed" ||
		len(users) != 2 || users[1] != dora {
		t.Errorf("the rename with a disabled person %v", renamed)
	}
	refusal("adding a disabled person", post(`{"name":"z","steps":[{"kind":"page_people","user_ids":["`+dora+`"]}]}`),
		http.StatusUnprocessableEntity, "/steps/0/user_ids/0", "not_pageable")

	// The deletion once nothing names it, and the Audit log; a step that names checkout reads team_deleted after.
	named := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"names checkout","steps":[`+
		`{"kind":"wait","wait_seconds":60},{"kind":"page_team","team_id":"`+tc+`"}]}`, http.StatusCreated)["id"].(string)
	putTeam(tc, func(map[string]any) {})
	api.json(http.MethodDelete, "/api/v1/escalation-chains/"+cc, "", http.StatusNoContent)
	api.json(http.MethodDelete, "/api/v1/teams/"+tc, "", http.StatusNoContent)
	if c := read(named); len(c.Warnings) != 1 || c.Warnings[0].Kind != "team_deleted" || c.Warnings[0].Step == nil ||
		*c.Warnings[0].Step != 2 {
		t.Errorf("the warnings of a chain whose Team was deleted %+v", c.Warnings)
	}
	for action, n := range map[string]int64{"escalation_chain.created": 9, "escalation_chain.updated": 1,
		"escalation_chain.deleted": 1} {
		if got := h.count(t, `SELECT count(*) FROM audit_log WHERE action = '`+action+`'`); got != n {
			t.Errorf("%d %s entries, want %d", got, action, n)
		}
	}
	if got := h.count(t, `SELECT count(*) FROM audit_log WHERE action = 'escalation_chain.updated'
		AND diff @> '[{"pointer":"/name","before":"people","after":"people-renamed"}]'`); got != 1 {
		t.Errorf("the rename's diff: %d entries", got)
	}
}
