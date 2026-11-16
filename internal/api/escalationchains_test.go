// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/escalation"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/teams"
)

const (
	chainID         = "EC0000000000PA"
	chainsAdmin     = "mstr_pat_chains_admin"
	chainsResponder = "mstr_pat_chains_responder"
	chainsViewer    = "mstr_pat_chains_viewer"
	chainsSA        = "mstr_sat_chains"
)

// fakeChains stands for internal/escalation: one chain with a step of each kind, and what the API asked.
type fakeChains struct {
	chain    escalation.Chain
	filters  []escalation.ListFilter
	inputs   []escalation.Input
	versions []*int64
	deleted  []string
	err      error
}

func newFakeChains() *fakeChains {
	owner := &escalation.TeamRef{ID: 1, PublicID: teamID, Name: "Payments", Slug: "payments"}
	petr := escalation.UserRef{PublicID: bobPublicID, Name: "Petr", Login: "petr", Status: "active"}
	gone := escalation.UserRef{PublicID: "SRCCCCCCCCCCCC", Name: "deleted-user-5", Login: "deleted-user-5",
		Status: "deleted"}
	main := &escalation.ScheduleRef{PublicID: scheduleID, Name: "Main", Team: *owner}
	steps := []escalation.Step{
		{Number: 1, Kind: escalation.KindPageOnCall, Importance: "normal", Schedule: main, Condition: "any"},
		{Number: 2, Kind: escalation.KindWait, WaitSeconds: 600},
		{Number: 3, Kind: escalation.KindPagePeople, Importance: "important", Users: []escalation.UserRef{petr, gone},
			Condition: "critical"},
		{Number: 4, Kind: escalation.KindPageTeam, WorkingHoursOnly: true, Condition: "any"},
		{Number: 5, Kind: escalation.KindPageTeam, Team: &escalation.TeamRef{PublicID: "TM0000000000DV",
			Name: "devops", Slug: "devops"}, Condition: "any"},
		{Number: 6, Kind: escalation.KindPageTeam, Everyone: "here", Condition: "any"},
		{Number: 7, Kind: escalation.KindCall, CallTarget: escalation.CallOnCall, Schedule: main,
			Gateway: &escalation.GatewayRef{PublicID: "CN0000000000G1", Name: "gw"}, Condition: "any"},
		{Number: 8, Kind: escalation.KindCall, CallTarget: escalation.CallPeople, Users: []escalation.UserRef{petr},
			Gateway: &escalation.GatewayRef{PublicID: "CN0000000000G1", Name: "gw"}, Condition: "any"},
		{Number: 9, Kind: escalation.KindWebhook, Destination: &escalation.DestinationTarget{PublicID: "DS0000000000WH",
			Name: "hook", Deleted: true}, Condition: "below_critical"},
		{Number: 10, Kind: escalation.KindRepeat, RepeatFrom: 3, RepeatTimes: 2},
	}
	reason := "gone"
	c := escalation.Chain{ID: 1, PublicID: chainID, Name: "payments", Description: "Pages payments", OwningTeam: owner,
		Steps: steps, CreatedAt: t0, Version: 4,
		Referrers: escalation.Referrers{Routes: []escalation.RouteReferrer{{PublicID: routeID, Name: "Infrastructure",
			Condition: "critical"}}, Teams: []escalation.TeamReferrer{{Team: *owner, Condition: "any"}}},
		Destinations: []escalation.DestinationRef{{PublicID: "DS0000000000TG", Name: "payments-tg", Type: "telegram",
			Health: "broken", BrokenSince: &t0, BrokenReason: &reason}},
		Warnings: []escalation.Warning{{Kind: escalation.WarningEveryoneUnsupported, Step: 4,
			Destination: &escalation.NamedRef{PublicID: "DS0000000000TG", Name: "payments-tg"},
			Reason:      "telegram_no_groups"}, {Kind: escalation.WarningDestinationDeleted, Step: 9,
			Destination: &escalation.NamedRef{PublicID: "DS0000000000WH", Name: "hook"}},
			{Kind: escalation.WarningTeamDeleted, Step: 5}, {Kind: escalation.WarningPagesNobody}}}
	return &fakeChains{chain: c}
}

func (f *fakeChains) List(_ context.Context, lf escalation.ListFilter) (escalation.Page, error) {
	f.filters = append(f.filters, lf)
	return escalation.Page{Chains: []escalation.Chain{f.chain}, Next: &escalation.Position{Name: "payments", ID: 1}},
		f.err
}

func (f *fakeChains) Get(_ context.Context, id string) (escalation.Chain, error) {
	if id != chainID {
		return escalation.Chain{}, escalation.ErrNotFound
	}
	return f.chain, f.err
}

func (f *fakeChains) Create(_ context.Context, _ escalation.Requester, in escalation.Input) (escalation.Chain, error) {
	f.inputs = append(f.inputs, in)
	return f.chain, f.err
}

func (f *fakeChains) Update(_ context.Context, _ escalation.Requester, _ string, version *int64,
	in escalation.Input) (escalation.Chain, error) {
	f.inputs, f.versions = append(f.inputs, in), append(f.versions, version)
	return f.chain, f.err
}

func (f *fakeChains) Delete(_ context.Context, _ escalation.Requester, id string, version *int64) error {
	f.deleted, f.versions = append(f.deleted, id), append(f.versions, version)
	return f.err
}

// newChainsAPI adds the fake chains and four tokens: an Admin's, a Responder's, a Viewer's and a Service account's.
func newChainsAPI(t *testing.T) (*testAPI, *fakeChains, *fakeTeams, *fakeRoutes) {
	t.Helper()
	x, fteams, froutes := newTeamsAPI(t)
	ft := x.srv.tokens.(*fakeTokens)
	owner := ft.idents[fullToken].Session
	responder := auth.Session{User: auth.Principal{ID: 2, PublicID: bobPublicID, Name: "Petr", Role: auth.RoleResponder}}
	viewer := auth.Session{User: auth.Principal{ID: 4, PublicID: "SRDDDDDDDDDDDD", Name: "Vic", Role: auth.RoleViewer}}
	ft.idents[chainsAdmin] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"escalation-chains:read",
		"escalation-chains:write", "routes:read", "routes:write", "teams:read", "teams:write"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 81, Name: "chains"}}
	ft.idents[chainsResponder] = &auth.Identity{Session: responder, Permissions: []auth.Permission{
		"escalation-chains:read", "routes:read", "teams:read"}, Transport: audit.TransportAPI,
		Token: &auth.Token{ID: 82, Name: "responder"}}
	ft.idents[chainsViewer] = &auth.Identity{Session: viewer, Permissions: []auth.Permission{"escalation-chains:read"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 83, Name: "viewer"}}
	ft.idents[chainsSA] = &auth.Identity{Permissions: []auth.Permission{"escalation-chains:read"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 84, Name: "ci",
			ServiceAccount: &auth.Principal{ID: 3, PublicID: saPublicID, Name: "terraform", Role: "viewer"}}}
	fc := newFakeChains()
	x.srv.escalationChains = fc
	return x, fc, fteams, froutes
}

const chainBody = `{"name":"payments","owning_team_id":"` + teamID + `","steps":[
	{"kind":"page_on_call","schedule_id":"` + scheduleID + `","importance":"normal"},
	{"kind":"wait","wait_seconds":600},
	{"kind":"page_people","user_ids":["` + bobPublicID + `"],"importance":"important","severity_condition":"critical"},
	{"kind":"page_team","working_hours_only":true},
	{"kind":"page_team","everyone":"here"},
	{"kind":"call","call_target":"people","user_ids":["` + bobPublicID + `"],"gateway_id":"CN0000000000G1"},
	{"kind":"webhook","destination_id":"DS0000000000WH"},
	{"kind":"repeat","repeat_from":3,"repeat_times":2}]}`

// TestEscalationChainsAPI is C-32.FR-1, FR-2, FR-3, FR-17, FR-19 and C-32.AC-15 at the API: the list with its
// cursor, team and q; a chain with each kind of step and only its fields, its referrers, Destinations and warnings;
// ETag and Location on creation, If-Match on update (428 without, 412 stale); the deletion; the refusals of the domain
// as problems, chain_in_use with in_use_by and not_pageable with its reason; 403 for a Responder and a Viewer that
// change a chain, while every Role reads them.
func TestEscalationChainsAPI(t *testing.T) {
	x, fc, _, _ := newChainsAPI(t)
	a := x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains?team="+teamID+"&q=pay&limit=1", "")
	var list struct {
		Items []struct {
			ID        string                `json:"id"`
			Owning    struct{ Slug string } `json:"owning_team"`
			OwningID  *string               `json:"owning_team_id"`
			Steps     []map[string]any      `json:"steps"`
			Referrers struct {
				Routes []struct {
					Route struct{ Name string } `json:"route"`
					Cond  string                `json:"severity_condition"`
				} `json:"routes"`
				Teams []struct {
					Team struct{ Slug string } `json:"team"`
				} `json:"teams"`
			} `json:"referrers"`
			Destinations []struct {
				Name   string                 `json:"name"`
				Health struct{ State string } `json:"health"`
			} `json:"destinations"`
			Warnings []struct {
				Kind        string                 `json:"kind"`
				Step        *int                   `json:"step"`
				Destination *struct{ Name string } `json:"destination"`
				Reason      *string                `json:"reason"`
			} `json:"warnings"`
			Etag string `json:"etag"`
		} `json:"items"`
		Next string `json:"next_cursor"`
	}
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 1 || fc.filters[0].Team != teamID || fc.filters[0].Query != "pay" ||
		fc.filters[0].Limit != 1 || list.Items[0].Etag != `"4"` || list.Items[0].Owning.Slug != "payments" ||
		list.Items[0].OwningID == nil || *list.Items[0].OwningID != teamID {
		t.Fatalf("list = %d %s, %+v", a.status, a.body, fc.filters)
	}
	c := list.Items[0]
	keys := func(m map[string]any) string {
		var out []string
		for _, k := range []string{"importance", "schedule_id", "schedule", "user_ids", "users", "team_id", "team",
			"everyone", "call_target", "gateway_id", "gateway", "wait_seconds", "destination_id", "destination",
			"repeat_from", "repeat_times", "working_hours_only", "severity_condition"} {
			if _, ok := m[k]; ok {
				out = append(out, k)
			}
		}
		return strings.Join(out, ",")
	}
	want := []string{
		"importance,schedule_id,schedule,working_hours_only,severity_condition",
		"wait_seconds",
		"importance,user_ids,users,working_hours_only,severity_condition",
		"team,working_hours_only,severity_condition",
		"team_id,team,working_hours_only,severity_condition",
		"everyone,working_hours_only,severity_condition",
		"schedule_id,schedule,call_target,gateway_id,gateway,working_hours_only,severity_condition",
		"user_ids,users,call_target,gateway_id,gateway,working_hours_only,severity_condition",
		"destination_id,destination,working_hours_only,severity_condition",
		"repeat_from,repeat_times",
	}
	for i, w := range want {
		if got := keys(c.Steps[i]); got != w || c.Steps[i]["number"] != float64(i+1) {
			t.Errorf("step %d has %s, want %s", i+1, got, w)
		}
	}
	if c.Steps[3]["team"].(map[string]any)["slug"] != "payments" || c.Steps[4]["team_id"] != "TM0000000000DV" ||
		c.Steps[2]["users"].([]any)[1].(map[string]any)["deactivated"] != true {
		t.Errorf("steps %+v", c.Steps)
	}
	if len(c.Referrers.Routes) != 1 || c.Referrers.Routes[0].Cond != "critical" || c.Referrers.Teams[0].Team.Slug !=
		"payments" || c.Destinations[0].Health.State != "broken" || len(c.Warnings) != 4 ||
		*c.Warnings[0].Reason != "telegram_no_groups" || c.Warnings[1].Reason != nil ||
		c.Warnings[1].Destination.Name != "hook" || *c.Warnings[0].Step != 4 ||
		c.Warnings[2].Kind != "team_deleted" || c.Warnings[2].Step == nil || *c.Warnings[2].Step != 5 ||
		c.Warnings[2].Destination != nil || c.Warnings[3].Kind != "pages_nobody" || c.Warnings[3].Step != nil {
		t.Errorf("referrers, destinations and warnings %+v", c)
	}
	if a = x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains?cursor="+list.Next, ""); a.status !=
		http.StatusOK || *fc.filters[1].After != (escalation.Position{Name: "payments", ID: 1}) {
		t.Errorf("second page = %d, %+v", a.status, fc.filters[1])
	}
	if a = x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains?team=mine", ""); a.status !=
		http.StatusOK || fc.filters[2].MemberID != 4 {
		t.Errorf("mine = %d, %+v", a.status, fc.filters[2])
	}
	if status, _, ptr, code := firstProblemError(t, x.as(t, chainsSA, http.MethodGet,
		"/api/v1/escalation-chains?team=mine", "")); status != http.StatusUnprocessableEntity || ptr != "/query/team" ||
		code != fieldUnsupported {
		t.Errorf("mine of a service account = %d %s %s", status, ptr, code)
	}
	if a = x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains?cursor=x", ""); a.status != http.StatusBadRequest {
		t.Errorf("a bad cursor = %d", a.status)
	}

	a = x.as(t, chainsAdmin, http.MethodPost, "/api/v1/escalation-chains", chainBody)
	if a.status != http.StatusCreated || a.header.Get("Location") != "/api/v1/escalation-chains/"+chainID ||
		a.header.Get("ETag") != `"4"` {
		t.Fatalf("create = %d %v %s", a.status, a.header, a.body)
	}
	in := fc.inputs[0]
	if in.Name != "payments" || *in.OwningTeamID != teamID || len(in.Steps) != 8 || *in.Steps[0].ScheduleID != scheduleID ||
		*in.Steps[0].Importance != "normal" || *in.Steps[1].WaitSeconds != 600 || *in.Steps[2].Condition != "critical" ||
		(*in.Steps[2].UserIDs)[0] != bobPublicID || !*in.Steps[3].WorkingHoursOnly || in.Steps[3].TeamID != nil ||
		*in.Steps[4].Everyone != "here" || *in.Steps[5].CallTarget != "people" || *in.Steps[5].GatewayID != "CN0000000000G1" ||
		*in.Steps[6].DestinationID != "DS0000000000WH" || *in.Steps[7].RepeatFrom != 3 || *in.Steps[7].RepeatTimes != 2 ||
		in.Steps[1].Condition != nil || in.Description != nil {
		t.Errorf("input %+v", in)
	}
	for _, token := range []string{chainsResponder, chainsViewer} {
		for _, r := range []struct{ method, path string }{{http.MethodPost, "/api/v1/escalation-chains"},
			{http.MethodPut, "/api/v1/escalation-chains/" + chainID},
			{http.MethodDelete, "/api/v1/escalation-chains/" + chainID}} {
			if a = x.as(t, token, r.method, r.path, chainBody, "If-Match", `"4"`); a.status != http.StatusForbidden {
				t.Errorf("%s %s by %s = %d", r.method, r.path, token, a.status)
			}
		}
	}
	if a = x.as(t, chainsResponder, http.MethodGet, "/api/v1/escalation-chains/"+chainID, ""); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"4"` {
		t.Errorf("get by a responder = %d", a.status)
	}
	if a = x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains/EC0000000000ZZ", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown = %d", a.status)
	}
	if a = x.as(t, chainsAdmin, http.MethodPut, "/api/v1/escalation-chains/"+chainID, chainBody); a.status !=
		http.StatusPreconditionRequired {
		t.Errorf("update without If-Match = %d", a.status)
	}
	if a = x.as(t, chainsAdmin, http.MethodPut, "/api/v1/escalation-chains/"+chainID,
		`{"name":"payments","owning_team_id":null,"description":"","steps":[]}`, "If-Match", `"4"`); a.status != http.StatusOK ||
		*fc.versions[0] != 4 || *fc.inputs[1].OwningTeamID != "" || *fc.inputs[1].Description != "" {
		t.Errorf("update = %d %s, %+v", a.status, a.body, fc.inputs[1])
	}
	if a = x.as(t, chainsAdmin, http.MethodDelete, "/api/v1/escalation-chains/"+chainID, "", "If-Match", `"4"`); a.status !=
		http.StatusNoContent || fc.deleted[0] != chainID || fc.versions[len(fc.versions)-1] == nil ||
		*fc.versions[len(fc.versions)-1] != 4 {
		t.Errorf("delete = %d %s", a.status, a.body)
	}
	for _, c := range []struct {
		err          error
		status       int
		code, ptr, f string
	}{
		{escalation.ErrNameTaken, http.StatusConflict, codeNameTaken, "", ""},
		{&escalation.FieldError{Pointer: "/steps/0/wait_seconds", Code: escalation.CodeOutOfRange},
			http.StatusUnprocessableEntity, "", "/steps/0/wait_seconds", "out_of_range"},
		{&escalation.FieldError{Pointer: "/steps/0", Code: escalation.CodeGatewayRequired},
			http.StatusUnprocessableEntity, "", "/steps/0", "gateway_required"},
		{&escalation.FieldError{Pointer: "/steps/6/destination_id", Code: escalation.CodeWebhookEventsRequired},
			http.StatusUnprocessableEntity, "", "/steps/6/destination_id", "webhook_events_required"},
	} {
		fc.err = c.err
		status, code, ptr, f := firstProblemError(t, x.as(t, chainsAdmin, http.MethodPost, "/api/v1/escalation-chains",
			chainBody))
		if status != c.status || code != c.code || ptr != c.ptr || f != c.f {
			t.Errorf("%v = %d %s %s %s", c.err, status, code, ptr, f)
		}
	}
	fc.err = &escalation.FieldError{Pointer: "/steps/2/user_ids/0", Code: escalation.CodeNotPageable,
		Reason: escalation.ReasonViewer}
	if a = x.as(t, chainsAdmin, http.MethodPost, "/api/v1/escalation-chains", chainBody); a.status !=
		http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"reason":"viewer"`) ||
		!strings.Contains(string(a.body), `"pointer":"/steps/2/user_ids/0"`) {
		t.Errorf("not pageable = %d %s", a.status, a.body)
	}
	fc.err = escalation.ErrVersionMismatch
	if a = x.as(t, chainsAdmin, http.MethodPut, "/api/v1/escalation-chains/"+chainID, chainBody, "If-Match",
		`"1"`); a.status != http.StatusPreconditionFailed {
		t.Errorf("stale = %d", a.status)
	}
	fc.err = &escalation.InUseError{Holders: []escalation.Holder{{Type: escalation.HolderRoute, PublicID: routeID,
		Name: "Infrastructure"}, {Type: escalation.HolderTeam, PublicID: teamID, Name: "payments"}}}
	a = x.as(t, chainsAdmin, http.MethodDelete, "/api/v1/escalation-chains/"+chainID, "")
	var held struct {
		Code    string                            `json:"code"`
		Detail  string                            `json:"detail"`
		InUseBy []struct{ Type, ID, Name string } `json:"in_use_by"`
	}
	decodeInto(t, a, &held)
	if a.status != http.StatusConflict || held.Code != codeChainInUse || len(held.InUseBy) != 2 ||
		held.InUseBy[0].Type != "route" || held.InUseBy[1].Type != "team" || !strings.Contains(held.Detail, `"payments"`) {
		t.Errorf("chain in use = %d %s", a.status, a.body)
	}
	fc.err = errors.New("boom")
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/escalation-chains", ""},
		{http.MethodGet, "/api/v1/escalation-chains/" + chainID, ""},
		{http.MethodPost, "/api/v1/escalation-chains", chainBody},
		{http.MethodPut, "/api/v1/escalation-chains/" + chainID, chainBody},
		{http.MethodDelete, "/api/v1/escalation-chains/" + chainID, ""},
	} {
		if a = x.as(t, chainsAdmin, r.method, r.path, r.body, "If-Match", `"4"`); a.status != http.StatusInternalServerError {
			t.Errorf("%s %s failed = %d", r.method, r.path, a.status)
		}
	}
}

// TestChainEntriesAPI is C-32.FR-2 and C-08.FR-1 at the API: Routes and Teams take chain_entries, the condition any
// when omitted, and read them with their chains; naming a chain on a Route needs routes:write, and a Team's
// deletion refused for a chain it owns names it in in_use_by with the type escalation_chain (C-25.FR-10).
func TestChainEntriesAPI(t *testing.T) {
	x, _, fteams, froutes := newChainsAPI(t)
	owner := &teams.ChainOwner{PublicID: teamID, Name: "Payments", Slug: "payments"}
	fteams.team.ChainEntries = []teams.ChainEntry{{ChainID: chainID, Condition: "critical"}}
	fteams.team.Chains = []teams.ChainRef{{PublicID: chainID, Name: "payments", OwningTeam: owner},
		{PublicID: "EC0000000000ZZ", Name: "unowned"}}
	a := x.as(t, chainsAdmin, http.MethodGet, "/api/v1/teams/"+teamID, "")
	if a.status != http.StatusOK || !strings.Contains(string(a.body),
		`"chain_entries":[{"chain_id":"`+chainID+`","severity_condition":"critical"}]`) ||
		!strings.Contains(string(a.body), `"name":"unowned","owning_team":null`) {
		t.Errorf("team read = %d %s", a.status, a.body)
	}
	a = x.as(t, chainsAdmin, http.MethodPut, "/api/v1/teams/"+teamID, `{"name":"Payments","slug":"payments",
		"own_working_hours":false,"chain_entries":[{"chain_id":"`+chainID+`"},{"chain_id":"EC0000000000ZZ",
		"severity_condition":"below_critical"}]}`, "If-Match", `"1"`)
	in := fteams.inputs[len(fteams.inputs)-1]
	if a.status != http.StatusOK || in.ChainEntries == nil || len(*in.ChainEntries) != 2 ||
		(*in.ChainEntries)[0].Condition != "" || (*in.ChainEntries)[1].Condition != "below_critical" {
		t.Errorf("team update = %d %s, %+v", a.status, a.body, in.ChainEntries)
	}
	fteams.err = &teams.InUseError{Holders: []teams.Holder{{Type: teams.HolderChain, PublicID: chainID,
		Name: "checkout"}}}
	a = x.as(t, chainsAdmin, http.MethodDelete, "/api/v1/teams/"+teamID, "")
	if a.status != http.StatusConflict || !strings.Contains(string(a.body), `"type":"escalation_chain"`) ||
		!strings.Contains(string(a.body), `"code":"team_in_use"`) {
		t.Errorf("team in use = %d %s", a.status, a.body)
	}
	fteams.err = nil

	rt := froutes.list.Routes[0]
	rt.ChainEntries = []routing.ChainEntry{{ChainID: chainID, Condition: routing.ConditionCritical}}
	rt.Chains = []routing.ChainRef{{PublicID: chainID, Name: "devops", OwningTeam: &routing.TeamRef{PublicID: teamID,
		Name: "Payments", Slug: "payments"}}}
	froutes.list.Routes[0] = rt
	if a = x.as(t, chainsAdmin, http.MethodGet, "/api/v1/routes/"+rt.PublicID, ""); a.status != http.StatusOK ||
		!strings.Contains(string(a.body), `"chain_entries":[{"chain_id":"`+chainID+`","severity_condition":"critical"}]`) ||
		!strings.Contains(string(a.body), `"chains":[{"id":"`+chainID+`","name":"devops","owning_team":{`) {
		t.Errorf("route read = %d %s", a.status, a.body)
	}
	body := strings.Replace(routeBody, `"destination_ids":[]`, `"destination_ids":[],"chain_entries":[{"chain_id":"`+
		chainID+`","severity_condition":"critical"}]`, 1)
	if a = x.as(t, chainsAdmin, http.MethodPut, "/api/v1/routes/"+rt.PublicID, body, "If-Match", "*"); a.status !=
		http.StatusOK {
		t.Fatalf("route update = %d %s", a.status, a.body)
	}
	if rin := froutes.inputs[len(froutes.inputs)-1]; rin.ChainEntries == nil ||
		(*rin.ChainEntries)[0].Condition != routing.ConditionCritical {
		t.Errorf("route input %+v", rin.ChainEntries)
	}
	if a = x.as(t, chainsResponder, http.MethodPut, "/api/v1/routes/"+rt.PublicID, body, "If-Match", "*"); a.status !=
		http.StatusForbidden {
		t.Errorf("naming a chain without routes:write = %d", a.status)
	}
}
