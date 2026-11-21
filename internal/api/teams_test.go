// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/escalation"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/links"
	"github.com/muster-io/muster/internal/managedby"
	"github.com/muster-io/muster/internal/oidc"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/routing"
	"github.com/muster-io/muster/internal/schedules"
	"github.com/muster-io/muster/internal/teams"
)

const (
	teamID      = "TM0000000000PA"
	teamsAdmin  = "mstr_pat_teams_admin"
	teamsReader = "mstr_pat_teams_read"
	teamsSA     = "mstr_sat_teams"
)

// fakeTeams stands for internal/teams: one Team, Payments, with Bob in it, and what the API asked.
type fakeTeams struct {
	team     teams.Team
	people   []teams.Person
	filters  []teams.ListFilter
	inputs   []teams.Input
	versions []*int64
	by       []teams.Requester
	added    []string
	removed  []string
	deleted  []string
	err      error
	already  bool
	values   teams.LabelValues
}

func newFakeTeams() *fakeTeams {
	hours := teams.WorkingHours{Days: []string{"monday", "friday"}, Start: "09:00", End: "18:00", TimeZone: "UTC"}
	return &fakeTeams{team: teams.Team{ID: 1, PublicID: teamID, Name: "Payments", Slug: "payments",
		WorkingHours: hours, UserCount: 1, CreatedAt: t0, Version: 2},
		people: []teams.Person{{ID: 2, PublicID: bobPublicID, Name: "bob", Login: "bob", Role: "responder",
			Sources: []string{teams.SourceByHand}, Linked: []teams.LinkedMessenger{{Type: "mattermost",
				ConnectionPublicID: "CNAAAAAAAAAAA1", ConnectionName: "chat"}}}}}
}

func (f *fakeTeams) List(_ context.Context, lf teams.ListFilter) (teams.Page, error) {
	f.filters = append(f.filters, lf)
	return teams.Page{Teams: []teams.Team{f.team}, Next: &teams.Position{Name: "payments", ID: 1}}, f.err
}

func (f *fakeTeams) Get(_ context.Context, id string) (teams.Team, error) {
	if id != teamID {
		return teams.Team{}, teams.ErrNotFound
	}
	return f.team, f.err
}

func (f *fakeTeams) Create(_ context.Context, r teams.Requester, in teams.Input) (teams.Team, error) {
	f.inputs, f.by = append(f.inputs, in), append(f.by, r)
	return f.team, f.err
}

func (f *fakeTeams) Update(_ context.Context, r teams.Requester, _ string, version *int64, in teams.Input) (
	teams.Team, error) {
	f.inputs, f.versions, f.by = append(f.inputs, in), append(f.versions, version), append(f.by, r)
	return f.team, f.err
}

func (f *fakeTeams) Delete(_ context.Context, r teams.Requester, id string, version *int64) error {
	f.deleted, f.versions, f.by = append(f.deleted, id), append(f.versions, version), append(f.by, r)
	return f.err
}

func (f *fakeTeams) Users(context.Context, string) ([]teams.Person, error) { return f.people, f.err }

func (f *fakeTeams) AddUser(_ context.Context, _ teams.Requester, _, userID string) (teams.Person, bool, error) {
	f.added = append(f.added, userID)
	return f.people[0], !f.already, f.err
}

func (f *fakeTeams) RemoveUser(_ context.Context, _ teams.Requester, _, userID string) error {
	f.removed = append(f.removed, userID)
	return f.err
}

func (f *fakeTeams) LabelValues(context.Context) (teams.LabelValues, error) { return f.values, f.err }

// ListOwned is the filter of the Route list by Team: the fake keeps every Route and records the filter.
func (f *fakeRoutes) ListOwned(_ context.Context, of routing.OwnerFilter) (routing.List, error) {
	f.owned = append(f.owned, of)
	return f.list, f.err
}

// newTeamsAPI adds the fake Teams and three tokens: an Admin's, one that only reads Teams, as a Responder's or a
// Viewer's does, and a Service account's.
func newTeamsAPI(t *testing.T) (*testAPI, *fakeTeams, *fakeRoutes) {
	t.Helper()
	x, fr := newRoutesAPI(t)
	ft := x.srv.tokens.(*fakeTokens)
	owner := ft.idents[fullToken].Session
	ft.idents[teamsAdmin] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"teams:read",
		"teams:write", "routes:read"}, Transport: audit.TransportAPI, Token: &auth.Token{ID: 61, Name: "teams"}}
	ft.idents[teamsReader] = &auth.Identity{Session: owner, Permissions: []auth.Permission{"teams:read",
		"routes:read", "alert-groups:read"}, Transport: audit.TransportAPI, Token: &auth.Token{ID: 62, Name: "read"}}
	ft.idents[teamsSA] = &auth.Identity{Permissions: []auth.Permission{"teams:read", "routes:read"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 63, Name: "ci",
			ServiceAccount: &auth.Principal{ID: 3, PublicID: saPublicID, Name: "terraform", Role: "viewer"}}}
	fteams := newFakeTeams()
	x.srv.teams = fteams
	return x, fteams, fr
}

// firstProblemError is the status, the first pointer and the first code of a problem.
func firstProblemError(t *testing.T, a answer) (int, string, string, string) {
	t.Helper()
	var p struct {
		Code   string `json:"code"`
		Errors []struct {
			Pointer string `json:"pointer"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(a.body, &p)
	if len(p.Errors) == 0 {
		return a.status, p.Code, "", ""
	}
	return a.status, p.Code, p.Errors[0].Pointer, p.Errors[0].Code
}

// TestTeamsAPI is C-25.FR-1, FR-3, FR-9 and FR-11 at the API: the Team with its effective Working hours, ETag and
// Location on creation, If-Match on update (428 without), the list with its cursor and q, the people with their
// Role, source and linked messengers, 201 for an addition and 200 for a person already in the Team, and the refusals
// of the domain as problems.
func TestTeamsAPI(t *testing.T) {
	x, ft, _ := newTeamsAPI(t)
	a := x.as(t, teamsReader, http.MethodGet, "/api/v1/teams?q=pay&limit=1", "")
	m := a.json(t)
	items, _ := m["items"].([]any)
	if a.status != http.StatusOK || len(items) != 1 || ft.filters[0].Query != "pay" || ft.filters[0].Limit != 1 {
		t.Fatalf("list = %d %s, %+v", a.status, a.body, ft.filters)
	}
	team := items[0].(map[string]any)
	hours, _ := team["working_hours"].(map[string]any)
	if team["id"] != teamID || team["slug"] != "payments" || team["own_working_hours"] != false ||
		team["user_count"] != 1.0 || hours["start"] != "09:00" || hours["end"] != "18:00" || team["etag"] != `"2"` {
		t.Errorf("team %v", team)
	}
	cursor, _ := m["next_cursor"].(string)
	if a = x.as(t, teamsReader, http.MethodGet, "/api/v1/teams?cursor="+cursor, ""); a.status != http.StatusOK ||
		ft.filters[1].After == nil || *ft.filters[1].After != (teams.Position{Name: "payments", ID: 1}) {
		t.Errorf("second page = %d, %+v", a.status, ft.filters[1])
	}
	if a = x.as(t, teamsReader, http.MethodGet, "/api/v1/teams?cursor=x", ""); a.status != http.StatusBadRequest {
		t.Errorf("a bad cursor = %d", a.status)
	}

	body := `{"name":"Payments","slug":"payments","description":"Pays","own_working_hours":true,` +
		`"working_hours":{"days":["monday","friday"],"start":"11:00","end":"19:00","time_zone":"Europe/Berlin"}}`
	a = x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams", body)
	if a.status != http.StatusCreated || a.header.Get("Location") != "/api/v1/teams/"+teamID ||
		a.header.Get("ETag") != `"2"` {
		t.Fatalf("create = %d %v %s", a.status, a.header, a.body)
	}
	in := ft.inputs[0]
	if in.Name != "Payments" || in.Slug != "payments" || *in.Description != "Pays" || !in.OwnWorkingHours ||
		in.WorkingHours == nil || in.WorkingHours.TimeZone != "Europe/Berlin" || len(in.WorkingHours.Days) != 2 ||
		ft.by[0].Actor.TokenName != "teams" {
		t.Errorf("input %+v by %+v", in, ft.by[0])
	}
	if a = x.as(t, teamsAdmin, http.MethodGet, "/api/v1/teams/"+teamID, ""); a.status != http.StatusOK ||
		a.header.Get("ETag") != `"2"` {
		t.Errorf("get = %d %v", a.status, a.header)
	}
	if a = x.as(t, teamsAdmin, http.MethodGet, "/api/v1/teams/TM0000000000ZZ", ""); a.status != http.StatusNotFound {
		t.Errorf("unknown = %d %s", a.status, a.body)
	}
	if a = x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID, body); a.status !=
		http.StatusPreconditionRequired {
		t.Errorf("update without If-Match = %d", a.status)
	}
	if a = x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID, `{"name":"P","slug":"p",`+
		`"own_working_hours":false}`, "If-Match", `"2"`); a.status != http.StatusOK || *ft.versions[0] != 2 ||
		ft.inputs[1].WorkingHours != nil || ft.inputs[1].Description != nil {
		t.Errorf("update = %d %s, %+v", a.status, a.body, ft.inputs[1])
	}
	if a = x.as(t, teamsAdmin, http.MethodDelete, "/api/v1/teams/"+teamID, "", "If-Match", `"2"`); a.status !=
		http.StatusNoContent || ft.deleted[0] != teamID || *ft.versions[1] != 2 {
		t.Errorf("delete = %d %s", a.status, a.body)
	}

	a = x.as(t, teamsReader, http.MethodGet, "/api/v1/teams/"+teamID+"/users", "")
	var people struct {
		Items []struct {
			User struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"user"`
			Role    string   `json:"role"`
			Sources []string `json:"sources"`
			Linked  []struct {
				Type       string            `json:"type"`
				Connection map[string]string `json:"connection"`
			} `json:"linked_messengers"`
		} `json:"items"`
	}
	decodeInto(t, a, &people)
	if a.status != http.StatusOK || len(people.Items) != 1 || people.Items[0].User.Name != "bob" ||
		people.Items[0].Role != "responder" || len(people.Items[0].Sources) != 1 ||
		people.Items[0].Sources[0] != teams.SourceByHand || people.Items[0].Linked[0].Type != "mattermost" ||
		people.Items[0].Linked[0].Connection["name"] != "chat" {
		t.Errorf("people = %d %s", a.status, a.body)
	}
	add := `{"user_id":"` + bobPublicID + `"}`
	if a = x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams/"+strings.ToLower(teamID)+"/users", add); a.status !=
		http.StatusCreated || ft.added[0] != bobPublicID ||
		a.header.Get("Location") != "/api/v1/teams/"+teamID+"/users/"+bobPublicID {
		t.Errorf("add = %d %s", a.status, a.body)
	}
	ft.already = true
	if a = x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams/"+teamID+"/users", add); a.status != http.StatusOK {
		t.Errorf("add again = %d %s", a.status, a.body)
	}
	if a = x.as(t, teamsAdmin, http.MethodDelete, "/api/v1/teams/"+teamID+"/users/"+bobPublicID, ""); a.status !=
		http.StatusNoContent || ft.removed[0] != bobPublicID {
		t.Errorf("remove = %d %s", a.status, a.body)
	}

	for _, c := range []struct {
		err          error
		status       int
		code, ptr, f string
	}{
		{teams.ErrNameTaken, http.StatusConflict, codeNameTaken, "", ""},
		{teams.ErrSlugTaken, http.StatusConflict, codeSlugTaken, "", ""},
		{teams.ErrInUse, http.StatusConflict, codeTeamInUse, "", ""},
		{&teams.FieldError{Pointer: "/slug", Code: teams.CodeInvalidFormat}, http.StatusUnprocessableEntity, "",
			"/slug", "invalid_format"},
		{&teams.FieldError{Pointer: "/working_hours/end", Code: teams.CodeOutOfRange},
			http.StatusUnprocessableEntity, "", "/working_hours/end", "out_of_range"},
	} {
		ft.err = c.err
		status, code, ptr, f := firstProblemError(t, x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams", body))
		if status != c.status || code != c.code || ptr != c.ptr || f != c.f {
			t.Errorf("%v = %d %s %s %s", c.err, status, code, ptr, f)
		}
	}
	ft.err = teams.ErrVersionMismatch
	if a = x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID, body, "If-Match", `"1"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("stale = %d %s", a.status, a.body)
	}
	ft.err = teams.ErrNotInTeam
	if a = x.as(t, teamsAdmin, http.MethodDelete, "/api/v1/teams/"+teamID+"/users/"+bobPublicID, ""); a.status !=
		http.StatusNotFound {
		t.Errorf("not in the team = %d %s", a.status, a.body)
	}
	ft.err = errors.New("boom")
	for _, path := range []string{"/api/v1/teams", "/api/v1/teams/" + teamID + "/users"} {
		if a = x.as(t, teamsAdmin, http.MethodGet, path, ""); a.status != http.StatusInternalServerError {
			t.Errorf("%s failed = %d", path, a.status)
		}
	}
	for _, r := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/teams/" + teamID, body},
		{http.MethodDelete, "/api/v1/teams/" + teamID, ""},
		{http.MethodPost, "/api/v1/teams/" + teamID + "/users", add},
		{http.MethodDelete, "/api/v1/teams/" + teamID + "/users/" + bobPublicID, ""},
	} {
		if a = x.as(t, teamsAdmin, r.method, r.path, r.body, "If-Match", `"2"`); a.status !=
			http.StatusInternalServerError {
			t.Errorf("%s %s failed = %d", r.method, r.path, a.status)
		}
	}
}

// TestTeamIdpEntriesAPI is C-25.FR-1, FR-4 and FR-11 at the API: idp_entries of TeamInput reach internal/teams with
// their claims, omitted on an update to keep them; a Team reads them back, an empty list without any; listTeamUsers
// gives a person the source idp with the entries that matched; removeTeamUser of a person with no source by_hand is
// 404; the domain's refusal of an entry is 422 at its group.
func TestTeamIdpEntriesAPI(t *testing.T) {
	x, ft, _ := newTeamsAPI(t)
	maintainer := "https://gitlab.org/claims/groups/maintainer"
	a := x.as(t, teamsAdmin, http.MethodGet, "/api/v1/teams/"+teamID, "")
	if !strings.Contains(string(a.body), `"idp_entries":[]`) {
		t.Errorf("a Team without entries = %s", a.body)
	}
	ft.team.IdpEntries = []teams.IdpEntry{{Group: "teams/payments/*"}, {Claim: maintainer, Group: "teams/payments"}}
	ft.people[0].Sources = []string{teams.SourceByHand, teams.SourceIdp}
	ft.people[0].IdpEntries = []teams.IdpEntry{{Group: "teams/payments/*"}}
	body := `{"name":"Payments","slug":"payments","own_working_hours":false,"idp_entries":[` +
		`{"group":"teams/payments/*"},{"claim":"` + maintainer + `","group":"teams/payments"}]}`
	a = x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams", body)
	var team struct {
		IdpEntries []map[string]string `json:"idp_entries"`
	}
	decodeInto(t, a, &team)
	if a.status != http.StatusCreated || len(team.IdpEntries) != 2 || team.IdpEntries[0]["group"] != "teams/payments/*" ||
		len(team.IdpEntries[0]) != 1 || team.IdpEntries[1]["claim"] != maintainer {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	if in := ft.inputs[0].IdpEntries; in == nil || len(*in) != 2 || (*in)[0] != (teams.IdpEntry{Group: "teams/payments/*"}) ||
		(*in)[1] != (teams.IdpEntry{Claim: maintainer, Group: "teams/payments"}) {
		t.Errorf("input entries = %+v", in)
	}
	if a = x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID, `{"name":"P","slug":"p",`+
		`"own_working_hours":false}`, "If-Match", `"2"`); a.status != http.StatusOK || ft.inputs[1].IdpEntries != nil {
		t.Errorf("an update without entries = %d, %+v", a.status, ft.inputs[1].IdpEntries)
	}
	a = x.as(t, teamsReader, http.MethodGet, "/api/v1/teams/"+teamID+"/users", "")
	var people struct {
		Items []struct {
			Sources    []string            `json:"sources"`
			IdpEntries []map[string]string `json:"idp_entries"`
		} `json:"items"`
	}
	decodeInto(t, a, &people)
	if len(people.Items) != 1 || fmt.Sprint(people.Items[0].Sources) != "[by_hand idp]" ||
		len(people.Items[0].IdpEntries) != 1 || people.Items[0].IdpEntries[0]["group"] != "teams/payments/*" {
		t.Errorf("people = %s", a.body)
	}
	ft.err = teams.ErrNotInTeam
	if a = x.as(t, teamsAdmin, http.MethodDelete, "/api/v1/teams/"+teamID+"/users/"+bobPublicID, ""); a.status !=
		http.StatusNotFound {
		t.Errorf("a person only through the identity provider = %d %s", a.status, a.body)
	}
	ft.err = &teams.FieldError{Pointer: "/idp_entries/0/group", Code: teams.CodeInvalidFormat}
	status, _, ptr, code := firstProblemError(t, x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams",
		`{"name":"P","slug":"p","own_working_hours":false,"idp_entries":[{"group":""}]}`))
	if status != http.StatusUnprocessableEntity || ptr != "/idp_entries/0/group" || code != "invalid_format" {
		t.Errorf("an empty group = %d %s %s", status, ptr, code)
	}
}

// TestTeamsGrantNothing is C-25.FR-1 and C-25.AC-6 at the API: whoever only reads Teams — a Responder or a Viewer,
// in a Team or not — reads them, and gets 403 on updateTeam, addTeamUser, removeTeamUser, createTeam and deleteTeam;
// no Permission check looks at a Team.
func TestTeamsGrantNothing(t *testing.T) {
	x, ft, _ := newTeamsAPI(t)
	for _, path := range []string{"/api/v1/teams", "/api/v1/teams/" + teamID, "/api/v1/teams/" + teamID + "/users"} {
		if a := x.as(t, teamsReader, http.MethodGet, path, ""); a.status != http.StatusOK {
			t.Errorf("read %s = %d", path, a.status)
		}
	}
	for _, r := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/teams", `{"name":"x","slug":"x","own_working_hours":false}`},
		{http.MethodPut, "/api/v1/teams/" + teamID, `{"name":"x","slug":"x","own_working_hours":false}`},
		{http.MethodDelete, "/api/v1/teams/" + teamID, ""},
		{http.MethodPost, "/api/v1/teams/" + teamID + "/users", `{"user_id":"` + bobPublicID + `"}`},
		{http.MethodDelete, "/api/v1/teams/" + teamID + "/users/" + bobPublicID, ""},
	} {
		a := x.as(t, teamsReader, r.method, r.path, r.body, "If-Match", `"2"`)
		if a.status != http.StatusForbidden || !strings.Contains(string(a.body), "teams:write") {
			t.Errorf("%s %s = %d %s", r.method, r.path, a.status, a.body)
		}
	}
	if len(ft.inputs)+len(ft.deleted)+len(ft.added)+len(ft.removed) != 0 {
		t.Error("a refused change reached the Teams")
	}
}

// TestOwningTeamQuery is C-25.FR-2 and C-25.AC-11 at the API: listRoutes and listDestinations take team, a Team's
// public_id or mine for the Teams of the calling User; a Service account's mine is 422 unsupported at /query/team.
func TestOwningTeamQuery(t *testing.T) {
	x, _, fr := newTeamsAPI(t)
	if a := x.as(t, teamsReader, http.MethodGet, "/api/v1/routes?team=mine", ""); a.status != http.StatusOK ||
		len(fr.owned) != 1 || fr.owned[0].UserID == 0 || fr.owned[0].Team != "" {
		t.Errorf("mine = %d %s, %+v", a.status, a.body, fr.owned)
	}
	if a := x.as(t, teamsReader, http.MethodGet, "/api/v1/routes?team="+teamID, ""); a.status != http.StatusOK ||
		fr.owned[1] != (routing.OwnerFilter{Team: teamID}) {
		t.Errorf("a team = %d, %+v", a.status, fr.owned)
	}
	status, _, ptr, code := firstProblemError(t, x.as(t, teamsSA, http.MethodGet, "/api/v1/routes?team=mine", ""))
	if status != http.StatusUnprocessableEntity || ptr != "/query/team" || code != fieldUnsupported {
		t.Errorf("a service account's mine = %d %s %s", status, ptr, code)
	}
	fr.err = errors.New("boom")
	if a := x.as(t, teamsReader, http.MethodGet, "/api/v1/routes?team=mine", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("failed = %d", a.status)
	}
	fr.err = nil
	fr.list.Routes[0].OwningTeam = &routing.TeamRef{ID: 1, PublicID: teamID, Name: "Payments", Slug: "payments"}
	var list struct {
		Items []struct {
			OwningTeam   *map[string]any `json:"owning_team"`
			OwningTeamID *string         `json:"owning_team_id"`
		} `json:"items"`
	}
	decodeInto(t, x.as(t, teamsReader, http.MethodGet, "/api/v1/routes", ""), &list)
	if o := list.Items[0].OwningTeam; o == nil || (*o)["slug"] != "payments" || (*o)["deleted"] != false ||
		*list.Items[0].OwningTeamID != teamID || list.Items[1].OwningTeam != nil || list.Items[1].OwningTeamID != nil {
		t.Errorf("owning teams %+v", list.Items)
	}

	y, fd, _ := newDestinationsAPI(t)
	fty := y.srv.tokens.(*fakeTokens)
	fty.idents[teamsSA] = &auth.Identity{Permissions: []auth.Permission{"destinations:read"},
		Transport: audit.TransportAPI, Token: &auth.Token{ID: 63, Name: "ci",
			ServiceAccount: &auth.Principal{ID: 3, PublicID: saPublicID, Name: "terraform", Role: "viewer"}}}
	fd.list[0].OwningTeam = &destinations.TeamRef{ID: 1, PublicID: teamID, Name: "Payments", Slug: "payments"}
	a := y.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations?team=mine", "")
	if f := fd.filters[len(fd.filters)-1]; a.status != http.StatusOK || f.TeamUser == nil || f.Team != nil {
		t.Errorf("destinations mine = %d %s, %+v", a.status, a.body, f)
	}
	var dests struct {
		Items []map[string]any `json:"items"`
	}
	decodeInto(t, a, &dests)
	if o, _ := dests.Items[0]["owning_team"].(map[string]any); o["id"] != teamID || o["name"] != "Payments" ||
		dests.Items[0]["owning_team_id"] != teamID || dests.Items[1]["owning_team"] != nil {
		t.Errorf("destination owners %v", dests.Items)
	}
	a = y.as(t, destinationsReader, http.MethodGet, "/api/v1/destinations?team="+teamID, "")
	if f := fd.filters[len(fd.filters)-1]; a.status != http.StatusOK || f.Team == nil || *f.Team != teamID ||
		f.TeamUser != nil {
		t.Errorf("destinations of a team = %d, %+v", a.status, f)
	}
	status, _, ptr, code = firstProblemError(t, y.as(t, teamsSA, http.MethodGet, "/api/v1/destinations?team=mine", ""))
	if status != http.StatusUnprocessableEntity || ptr != "/query/team" || code != fieldUnsupported {
		t.Errorf("a service account's destinations mine = %d %s %s", status, ptr, code)
	}
}

// TestOwningTeamInputs: owning_team_id reaches the domain as given — omitted keeps, null clears, a value names the
// Team — on Routes and on every Destination type.
func TestOwningTeamInputs(t *testing.T) {
	if got := owningTeamOf(nullable.Nullable[string]{}); got != nil {
		t.Errorf("omitted = %v", *got)
	}
	if got := owningTeamOf(nullable.NewNullNullable[string]()); got == nil || *got != "" {
		t.Errorf("null = %v", got)
	}
	id := teamID
	if got := owningTeamOf(nullable.NewNullableWithValue(id)); got == nil || *got != teamID {
		t.Errorf("a value = %v", got)
	}
	x, fr := newRoutesAPI(t)
	body := `{"name":"p","matchers":[],"urgent":false,"group_key":[],"destination_ids":[],"owning_team_id":null,` +
		`"policy":{"reopen_window_seconds":900,"grace_period_seconds":900,"urgent_rise_removes_ack":true,` +
		`"snooze_durations_seconds":[],"thread_batching_window_seconds":60,"storm_threshold":20,"language":"en",` +
		`"templates":{},"ack_timeout":{"enabled":false,"first_interval_seconds":900},` +
		`"reminders":{"enabled":false,"first_interval_seconds":14400,"cap_seconds":86400},"auto_unacknowledge":false}}`
	if a := x.as(t, routesWriter, http.MethodPost, "/api/v1/routes", body); a.status != http.StatusCreated ||
		fr.inputs[0].OwningTeamID == nil || *fr.inputs[0].OwningTeamID != "" {
		t.Errorf("route = %d %s, %+v", a.status, a.body, fr.inputs)
	}
}

// TestSlugUnprocessable: a slug outside team.slug_format, refused by the request validation for the pattern and the
// length of the spec, answers the 422 invalid_format at /slug that the domain answers (x-unprocessable); a body that
// is also malformed elsewhere stays a 400.
func TestSlugUnprocessable(t *testing.T) {
	x, ft, _ := newTeamsAPI(t)
	for _, slug := range []string{"Payments", "-x", strings.Repeat("a", 64)} {
		status, _, ptr, code := firstProblemError(t, x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams",
			`{"name":"x","slug":"`+slug+`","own_working_hours":false}`))
		if status != http.StatusUnprocessableEntity || ptr != "/slug" || code != fieldInvalidFormat {
			t.Errorf("%q = %d %s %s", slug, status, ptr, code)
		}
	}
	for name, c := range map[string]struct{ body, code string }{
		"a missing slug":               {`{"name":"x","own_working_hours":false}`, fieldRequired},
		"a number":                     {`{"name":"x","slug":5,"own_working_hours":false}`, fieldInvalidFormat},
		"null":                         {`{"name":"x","slug":null,"own_working_hours":false}`, fieldInvalidFormat},
		"a bad slug and an empty name": {`{"name":"","slug":"Payments","own_working_hours":false}`, ""},
	} {
		a := x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams", c.body)
		status, _, ptr, code := firstProblemError(t, a)
		if status != http.StatusBadRequest || c.code != "" && (ptr != "/slug" || code != c.code) {
			t.Errorf("%s = %d %s", name, a.status, a.body)
		}
	}
	if len(ft.inputs) != 0 {
		t.Error("a refused slug reached the Teams")
	}
}

// TestManagedByAPI is C-33.FR-11 on the API: managed_by reaches the domain as given — omitted keeps, null clears, a
// value sets — a read always carries it, null for none, and a value outside the format is refused by the request
// validation with the 422 invalid_format at /managed_by that the domain answers (x-unprocessable), while null passes.
func TestManagedByAPI(t *testing.T) {
	if got := managedByIn(nullable.Nullable[string]{}); got.Given {
		t.Errorf("omitted = %+v", got)
	}
	if got := managedByIn(nullable.NewNullNullable[string]()); !got.Given || got.Value != nil {
		t.Errorf("null = %+v", got)
	}
	if got := managedByIn(nullable.NewNullableWithValue("terraform")); !got.Given || got.Value == nil ||
		*got.Value != "terraform" {
		t.Errorf("a value = %+v", got)
	}
	if got := managedByOf(nil); !got.IsSpecified() || !got.IsNull() {
		t.Errorf("read of none = %v", got)
	}
	mark := "terraform"
	if got := managedByOf(&mark); got.MustGet() != "terraform" {
		t.Errorf("read of terraform = %v", got)
	}
	x, ft, _ := newTeamsAPI(t)
	for _, v := range []string{"Terraform", strings.Repeat("a", 33), "-tf", "a b", ""} {
		status, _, ptr, code := firstProblemError(t, x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams",
			`{"name":"x","slug":"x","own_working_hours":false,"managed_by":"`+v+`"}`))
		if status != http.StatusUnprocessableEntity || ptr != "/managed_by" || code != fieldInvalidFormat {
			t.Errorf("%q = %d %s %s", v, status, ptr, code)
		}
	}
	if len(ft.inputs) != 0 {
		t.Error("a refused managed_by reached the Teams")
	}
	a := x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams", `{"name":"x","slug":"x","own_working_hours":false,`+
		`"managed_by":null}`)
	if a.status != http.StatusCreated || len(ft.inputs) != 1 || !ft.inputs[0].ManagedBy.Given ||
		ft.inputs[0].ManagedBy.Value != nil || !strings.Contains(string(a.body), `"managed_by":null`) {
		t.Errorf("null = %d %s, %+v", a.status, a.body, ft.inputs)
	}
}

// TestManagedByOperations: exactly the creates and updates of the twelve kinds of C-33.FR-11 carry managed_by marked
// x-unprocessable — those of S-179, the Connections and Destinations of every type through the variants of their
// bodies, and the updates of the Organization's settings, the Outbound address policy and the OIDC settings — so that
// a value outside its format answers the domain's 422 on each of them.
func TestManagedByOperations(t *testing.T) {
	doc, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, op := range readOperations(doc) {
		if op.unprocessable["/managed_by"] {
			got = append(got, op.id)
		}
	}
	slices.Sort(got)
	want := []string{"createConnection", "createDestination", "createEscalationChain", "createIntegration",
		"createLinkRule", "createLookupTable", "createRoute", "createSchedule", "createTeam", "updateConnection",
		"updateDestination", "updateEscalationChain", "updateIntegration", "updateLinkRule", "updateLookupTable",
		"updateOidcSettings", "updateOrganization", "updateOutboundPolicy", "updateRoute", "updateSchedule",
		"updateTeam"}
	if !slices.Equal(got, want) {
		t.Errorf("operations with managed_by x-unprocessable = %v, want %v", got, want)
	}
}

// TestUnprocessableVariantsAgree: the variants of a oneOf request body that share a top-level property agree on whether
// it is x-unprocessable, so that the set unprocessableFields merges by pointer is that of every variant; managed_by is
// x-unprocessable in every variant of the bodies of Connections and Destinations.
func TestUnprocessableVariantsAgree(t *testing.T) {
	doc, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	// flags are the top-level properties of a variant, through allOf, and whether each is x-unprocessable.
	var flags func(s *openapi3.Schema, out map[string]bool)
	flags = func(s *openapi3.Schema, out map[string]bool) {
		for name, prop := range s.Properties {
			if prop.Value != nil {
				out[name] = out[name] || prop.Value.Extensions[xUnprocessable] == true
			}
		}
		for _, sub := range s.AllOf {
			if sub.Value != nil {
				flags(sub.Value, out)
			}
		}
	}
	checked := map[string]bool{}
	var walk func(op string, s *openapi3.Schema)
	walk = func(op string, s *openapi3.Schema) {
		if len(s.OneOf) > 0 {
			seen := map[string]bool{}
			for i, sub := range s.OneOf {
				if sub.Value == nil {
					continue
				}
				variant := map[string]bool{}
				flags(sub.Value, variant)
				for name, unprocessable := range variant {
					if before, ok := seen[name]; ok && before != unprocessable {
						t.Errorf("%s: the variants disagree on whether %s is x-unprocessable (variant %d)", op, name, i)
					}
					seen[name] = unprocessable
				}
			}
			checked[op] = seen["managed_by"]
		}
		for _, sub := range append(slices.Clone(s.AllOf), s.OneOf...) {
			if sub.Value != nil {
				walk(op, sub.Value)
			}
		}
	}
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			if op.RequestBody == nil || op.RequestBody.Value == nil {
				continue
			}
			if media := op.RequestBody.Value.Content.Get("application/json"); media != nil && media.Schema != nil &&
				media.Schema.Value != nil {
				walk(op.OperationID, media.Schema.Value)
			}
		}
	}
	for _, op := range []string{"createConnection", "updateConnection", "createDestination", "updateDestination"} {
		if !checked[op] {
			t.Errorf("%s: no oneOf body whose variants mark managed_by x-unprocessable", op)
		}
	}
}

// TestManagedByBodies is C-33.FR-11 on the bodies of S-180: a value outside the format of managed_by in a
// Connection's or a Destination's body of each type, which validation reads through the variant of its type, or in
// the Organization's settings, the Outbound address policy or the OIDC settings, is the 422 invalid_format at
// /managed_by that the domain answers, and reaches no domain; null and a value reach it as given.
func TestManagedByBodies(t *testing.T) {
	with := func(body, mark string) string {
		return strings.TrimSuffix(body, "}") + `,"managed_by":` + mark + `}`
	}
	refused := func(what string, a answer) {
		t.Helper()
		status, _, ptr, code := firstProblemError(t, a)
		if status != http.StatusUnprocessableEntity || ptr != "/managed_by" || code != fieldInvalidFormat {
			t.Errorf("%s = %d %s %s: %s", what, status, ptr, code, a.body)
		}
	}
	bad := []string{`"Terraform"`, `"` + strings.Repeat("a", 33) + `"`, `""`}

	x, fc := newConnectionsAPI(t)
	connBodies := map[string]string{
		"mattermost": connectionBody,
		"telegram": `{"type":"telegram","name":"tg","bot_api_base_url":"https://api.telegram.org","update_mode":` +
			`"long_polling","bot_token":"1:x","proxy":{"enabled":false},"limiter":{"limit":15,"per_seconds":1}}`,
		"bridge": `{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094","event_delivery":"poll",` +
			`"bot_token":"t","proxy":{"enabled":false},"limiter":{"limit":5,"per_seconds":1}}`,
		"slack": `{"type":"slack","name":"sl","event_mode":"socket","bot_token":"xoxb-1","app_token":"xapp-1",` +
			`"proxy":{"enabled":false},"limiter":{"limit":5,"per_seconds":1}}`,
	}
	for kind, body := range connBodies {
		for _, v := range bad {
			refused("connection "+kind+" "+v, x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections",
				with(body, v)))
			refused("update of a connection "+kind+" "+v, x.as(t, connectionsWriter, http.MethodPut,
				"/api/v1/connections/"+connectionID, with(body, v), "If-Match", "*"))
		}
	}
	if len(fc.inputs) != 0 {
		t.Fatalf("a refused managed_by reached the Connections: %+v", fc.inputs)
	}
	for kind, body := range connBodies {
		a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections", with(body, `"terraform"`))
		if in := fc.inputs[len(fc.inputs)-1]; a.status != http.StatusCreated || !in.ManagedBy.Given ||
			in.ManagedBy.Value == nil || *in.ManagedBy.Value != "terraform" {
			t.Errorf("connection %s with terraform = %d %s", kind, a.status, a.body)
		}
	}

	xd, fd, _ := newDestinationsAPI(t)
	destBodies := map[string]string{
		"mattermost": mattermostBody,
		"webhook":    webhookBody,
		"telegram": `{"type":"telegram","name":"tg","connection_id":"CNAAAAAAAAAAA2","channel_id":"@alerts",` +
			`"limiter":{"limit":5,"per_seconds":1}}`,
		"bridge": `{"type":"bridge","name":"br","connection_id":"CNAAAAAAAAAAA3","channel_id":"ch-alerts",` +
			`"limiter":{"limit":5,"per_seconds":1}}`,
		"slack": `{"type":"slack","name":"sl","connection_id":"CNAAAAAAAAAAA8","channel_id":"C0FAKEALERTS",` +
			`"limiter":{"limit":5,"per_seconds":1}}`,
	}
	for kind, body := range destBodies {
		for _, v := range bad {
			refused("destination "+kind+" "+v, xd.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations",
				with(body, v)))
			refused("update of a destination "+kind+" "+v, xd.as(t, destinationsWriter, http.MethodPut,
				"/api/v1/destinations/"+mattermostID, with(body, v), "If-Match", "*"))
		}
	}
	if len(fd.saved) != 0 {
		t.Fatalf("a refused managed_by reached the Destinations: %+v", fd.saved)
	}
	for kind, body := range destBodies {
		xd.as(t, destinationsWriter, http.MethodPost, "/api/v1/destinations", with(body, `null`))
		if in := fd.saved[len(fd.saved)-1]; !in.ManagedBy.Given || in.ManagedBy.Value != nil {
			t.Errorf("destination %s with null = %+v", kind, in.ManagedBy)
		}
	}

	xo, fo := newOrganizationAPI(t)
	read := xo.call(t, http.MethodGet, "/api/v1/organization", "", "Cookie", adminCookie).body
	if !strings.Contains(string(read), `"managed_by":null`) {
		t.Errorf("the organization reads without managed_by: %s", read)
	}
	for _, v := range bad {
		refused("organization "+v, xo.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization",
			updateBody(t, read, func(m map[string]any) { m["managed_by"] = json.RawMessage(v) }), "If-Match", "*"))
		refused("outbound policy "+v, xo.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization/outbound-policy",
			`{"policy":"standard","allowed":[],"denied":[],"managed_by":`+v+`}`, "If-Match", "*"))
	}
	// A value of another type is no format failure: the 400 of the request validation, also from the JSON Schema
	// 2020-12 validator that checks the policy's body, and also when another field of the body breaks a pattern.
	for _, body := range []string{`{"policy":"standard","allowed":[],"denied":[],"managed_by":5}`,
		`{"policy":"standard","allowed":[],"denied":[],"managed_by":["terraform"]}`,
		`{"policy":"standard","allowed":5,"denied":[],"managed_by":"Terraform"}`} {
		if a := xo.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization/outbound-policy", body,
			"If-Match", "*"); a.status != http.StatusBadRequest {
			t.Errorf("outbound policy %s = %d %s", body, a.status, a.body)
		}
	}
	if a := x.as(t, connectionsWriter, http.MethodPost, "/api/v1/connections",
		with(connBodies["mattermost"], `5`)); a.status != http.StatusBadRequest {
		t.Errorf("connection with managed_by 5 = %d %s", a.status, a.body)
	}
	if len(fo.inputs) != 0 || len(fo.policyInputs) != 0 {
		t.Fatal("a refused managed_by reached the Organization")
	}
	a := xo.mutate(t, adminCookie, http.MethodPut, "/api/v1/organization/outbound-policy",
		`{"policy":"standard","allowed":[],"denied":[],"managed_by":"terraform"}`, "If-Match", "*")
	if in := fo.policyInputs[0]; a.status != http.StatusOK || in.ManagedBy.Value == nil ||
		*in.ManagedBy.Value != "terraform" || !strings.Contains(string(a.body), `"managed_by":null`) {
		t.Errorf("outbound policy with terraform = %d %s", a.status, a.body)
	}

	xi, fi := newOIDCAPI(t)
	var m map[string]any
	oidcRead := xi.call(t, http.MethodGet, "/api/v1/oidc-settings", "", "Cookie", adminCookie).body
	_ = json.Unmarshal(oidcRead, &m)
	if v, ok := m["managed_by"]; !ok || v != nil {
		t.Errorf("the OIDC settings read managed_by %v", v)
	}
	for _, k := range []string{"etag", "client_secret_status", "warnings", "updated_at", "claims_supported"} {
		delete(m, k)
	}
	m["proxy"] = map[string]any{"enabled": false}
	for _, v := range bad {
		m["managed_by"] = json.RawMessage(v)
		body, _ := json.Marshal(m)
		refused("oidc "+v, xi.mutate(t, adminCookie, http.MethodPut, "/api/v1/oidc-settings", string(body),
			"If-Match", "*"))
	}
	if len(fi.inputs) != 0 {
		t.Fatal("a refused managed_by reached the OIDC settings")
	}
}

// TestManagedByMappers: every input mapper of the seven kinds hands managed_by to its domain as given, and every read
// mapper sends the domain's mark, null for none.
func TestManagedByMappers(t *testing.T) {
	set := nullable.NewNullableWithValue("terraform")
	inputs := map[string]managedby.Input{
		"integration":      integrationInputOf(gen.IntegrationInput{ManagedBy: set}).ManagedBy,
		"route":            routeInputOf(gen.RouteInput{ManagedBy: set}).ManagedBy,
		"team":             teamInputOf(gen.TeamInput{ManagedBy: set}).ManagedBy,
		"schedule":         scheduleInputOf(gen.ScheduleInput{ManagedBy: set}).ManagedBy,
		"escalation chain": escalationInputOf(gen.EscalationChainInput{ManagedBy: set}).ManagedBy,
		"lookup table":     tableInputOf(gen.LookupTableBase{ManagedBy: set}).ManagedBy,
		"link rule":        ruleInputOf(gen.LinkRuleBase{ManagedBy: set}).ManagedBy,
		"organization":     inputOf(gen.UpdateOrganizationJSONRequestBody{ManagedBy: set}).ManagedBy,
		"outbound policy":  outboundPolicyInputOf(gen.OutboundPolicy{ManagedBy: set}).ManagedBy,
		"oidc settings":    oidcInputOf(gen.OidcSettingsInput{ManagedBy: set}).ManagedBy,
	}
	var conn gen.ConnectionInput
	for kind, from := range map[string]func() error{
		"mattermost connection": func() error {
			return conn.FromMattermostConnectionInput(gen.MattermostConnectionInput{
				Type: gen.MattermostConnectionInputTypeMattermost, ManagedBy: set})
		},
		"telegram connection": func() error {
			return conn.FromTelegramConnectionInput(gen.TelegramConnectionInput{
				Type: gen.TelegramConnectionInputTypeTelegram, ManagedBy: set})
		},
		"bridge connection": func() error {
			return conn.FromBridgeConnectionInput(gen.BridgeConnectionInput{
				Type: gen.BridgeConnectionInputTypeBridge, ManagedBy: set})
		},
		"slack connection": func() error {
			return conn.FromSlackConnectionInput(gen.SlackConnectionInput{
				Type: gen.SlackConnectionInputTypeSlack, ManagedBy: set})
		},
	} {
		if err := from(); err != nil {
			t.Fatal(err)
		}
		in, err := connectionInputOf(conn)
		if err != nil {
			t.Fatal(err)
		}
		inputs[kind] = in.ManagedBy
	}
	var dest gen.DestinationInput
	for kind, from := range map[string]func() error{
		"mattermost destination": func() error {
			return dest.FromMattermostDestinationInput(gen.MattermostDestinationInput{
				Type: gen.MattermostDestinationInputTypeMattermost, ManagedBy: set})
		},
		"telegram destination": func() error {
			return dest.FromTelegramDestinationInput(gen.TelegramDestinationInput{
				Type: gen.TelegramDestinationInputTypeTelegram, ManagedBy: set})
		},
		"bridge destination": func() error {
			return dest.FromBridgeDestinationInput(gen.BridgeDestinationInput{
				Type: gen.BridgeDestinationInputTypeBridge, ManagedBy: set})
		},
		"slack destination": func() error {
			return dest.FromSlackDestinationInput(gen.SlackDestinationInput{
				Type: gen.SlackDestinationInputTypeSlack, ManagedBy: set})
		},
		"webhook destination": func() error {
			return dest.FromWebhookDestinationInput(gen.WebhookDestinationInput{
				Type: gen.WebhookDestinationInputTypeWebhook, Mode: gen.WebhookDestinationInputModeEvents,
				ManagedBy: set})
		},
	} {
		if err := from(); err != nil {
			t.Fatal(err)
		}
		in, err := destinationInputOf(dest)
		if err != nil {
			t.Fatal(err)
		}
		inputs[kind] = in.ManagedBy
	}
	for kind, in := range inputs {
		if !in.Given || in.Value == nil || *in.Value != "terraform" {
			t.Errorf("%s input = %+v", kind, in)
		}
	}
	x, _, _ := newIntegrationsAPI(t)
	for _, mark := range []*string{nil, new("terraform")} {
		reads := map[string]nullable.Nullable[string]{
			"integration":      x.srv.integrationOf(integrations.Integration{ManagedBy: mark}, 0).ManagedBy,
			"route":            routeOf(routing.Route{ManagedBy: mark}).ManagedBy,
			"team":             teamOf(teams.Team{ManagedBy: mark}).ManagedBy,
			"schedule":         scheduleOf(schedules.Schedule{ManagedBy: mark}).ManagedBy,
			"escalation chain": escalationChainOf(escalation.Chain{ManagedBy: mark}).ManagedBy,
			"lookup table":     lookupTableOf(links.Table{ManagedBy: mark}).ManagedBy,
			"link rule":        linkRuleOf(links.Rule{ManagedBy: mark}).ManagedBy,
		}
		for kind, got := range reads {
			switch {
			case !got.IsSpecified():
				t.Errorf("%s read leaves managed_by out", kind)
			case mark == nil && !got.IsNull():
				t.Errorf("%s read of none = %v", kind, got)
			case mark != nil && (got.IsNull() || got.MustGet() != *mark):
				t.Errorf("%s read of %s = %v", kind, *mark, got)
			}
		}
	}

	// The kinds of S-180, whose reads are variants of a union or carry the field on the object that exists once.
	xc, _ := newConnectionsAPI(t)
	for _, mark := range []*string{nil, new("terraform")} {
		reads := map[string]any{
			"organization":    organizationOf(organization.Organization{ManagedBy: mark}),
			"outbound policy": outboundPolicyOf(organization.OutboundPolicyResource{ManagedBy: mark}),
			"oidc settings":   oidcSettingsOf(oidc.Settings{ManagedBy: mark}),
		}
		for _, typ := range []string{connections.TypeMattermost, connections.TypeTelegram, connections.TypeBridge,
			connections.TypeSlack} {
			c, err := xc.srv.connectionOf(connections.Connection{PublicID: connectionID, Type: typ, ManagedBy: mark})
			if err != nil {
				t.Fatal(err)
			}
			reads[typ+" connection"] = c
		}
		mode := "events"
		for _, typ := range []string{destinations.TypeMattermost, destinations.TypeTelegram, destinations.TypeBridge,
			destinations.TypeSlack, destinations.TypeWebhook} {
			d, err := destinationOf(destinations.Destination{PublicID: mattermostID, Type: typ, WebhookMode: &mode,
				ManagedBy: mark})
			if err != nil {
				t.Fatal(err)
			}
			reads[typ+" destination"] = d
		}
		for kind, read := range reads {
			raw, err := json.Marshal(read)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			got, ok := m["managed_by"]
			switch {
			case !ok:
				t.Errorf("%s read leaves managed_by out", kind)
			case mark == nil && got != nil:
				t.Errorf("%s read of none = %v", kind, got)
			case mark != nil && got != *mark:
				t.Errorf("%s read of %s = %v", kind, *mark, got)
			}
		}
	}
}

// TestTeamMatchersAPI is C-25.FR-1, FR-5 and FR-11 on the API: a Team takes matchers in the syntax of Route Matchers,
// given to the Teams as written and read back with the Team, saved with If-Match; an update that omits them keeps
// them; an invalid regular expression the Teams refuse answers 422 invalid_regex at its pointer.
func TestTeamMatchersAPI(t *testing.T) {
	x, ft, _ := newTeamsAPI(t)
	ft.team.Matchers = []teams.Matcher{{Label: "namespace", Op: "=~", Value: "checkout|billing"}}
	a := x.as(t, teamsAdmin, http.MethodPost, "/api/v1/teams", `{"name":"Checkout","slug":"checkout-team",`+
		`"own_working_hours":false,"matchers":[{"label":"namespace","op":"=~","value":"checkout|billing"},`+
		`{"label":"pod","op":"=~","value":"checkout-.*"}]}`)
	if a.status != http.StatusCreated || !strings.Contains(string(a.body),
		`"matchers":[{"label":"namespace","op":"=~","value":"checkout|billing"}]`) {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	if in := ft.inputs[0]; in.Matchers == nil || len(*in.Matchers) != 2 || (*in.Matchers)[1] != (teams.Matcher{
		Label: "pod", Op: "=~", Value: "checkout-.*"}) {
		t.Errorf("input %+v", in.Matchers)
	}
	a = x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID, `{"name":"Checkout","slug":"checkout-team",`+
		`"own_working_hours":false}`, "If-Match", `"1"`)
	if a.status != http.StatusOK || ft.inputs[1].Matchers != nil || *ft.versions[0] != 1 {
		t.Errorf("an update without matchers = %d %s, %+v", a.status, a.body, ft.inputs[1].Matchers)
	}
	ft.team.Matchers = []teams.Matcher{}
	if a := x.as(t, teamsReader, http.MethodGet, "/api/v1/teams/"+teamID, ""); a.status != http.StatusOK ||
		!strings.Contains(string(a.body), `"matchers":[]`) {
		t.Errorf("a team without matchers = %d %s", a.status, a.body)
	}
	ft.err = &teams.FieldError{Pointer: "/matchers/1/value", Code: teams.CodeInvalidRegex, Detail: "missing )"}
	status, _, ptr, code := firstProblemError(t, x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID,
		`{"name":"Checkout","slug":"checkout-team","own_working_hours":false,"matchers":[{"label":"a","op":"=",`+
			`"value":"b"},{"label":"pod","op":"=~","value":"checkout-("}]}`, "If-Match", `"1"`))
	if status != http.StatusUnprocessableEntity || ptr != "/matchers/1/value" || code != "invalid_regex" {
		t.Errorf("invalid regex = %d %s %s", status, ptr, code)
	}
}

// TestListTeamLabelValues is C-25.FR-7 and AC-1 at the API: the values of the Team label without a Team, each with
// when it was last seen in UTC, its number of Alerts and whether it is a valid slug, in the order the domain gives
// them, with the label and the window in days; every Role reads them with teams:read, a token without it gets 403, and
// a failure of the domain is a problem.
func TestListTeamLabelValues(t *testing.T) {
	x, ft, _ := newTeamsAPI(t)
	seen := t0.Add(-time.Hour)
	ft.values = teams.LabelValues{Label: "team", WindowDays: 14, Items: []teams.LabelValue{
		{Value: "checkout", LastSeenAt: seen.In(time.FixedZone("x", 3600)), Alerts: 2, ValidSlug: true},
		{Value: "Payments", LastSeenAt: seen, Alerts: 1}}}
	for _, token := range []string{teamsReader, teamsSA} {
		a := x.as(t, token, http.MethodGet, "/api/v1/team-label-values", "")
		want := `{"items":[{"alert_count":2,"last_seen_at":"` + seen.Format(time.RFC3339) + `","valid_slug":true,` +
			`"value":"checkout"},{"alert_count":1,"last_seen_at":"` + seen.Format(time.RFC3339) + `",` +
			`"valid_slug":false,"value":"Payments"}],"label":"team","window_days":14}`
		if a.status != http.StatusOK || strings.TrimSpace(string(a.body)) != want {
			t.Errorf("%s: %d %s, want %s", token, a.status, a.body, want)
		}
	}
	ft.values = teams.LabelValues{Label: "service_team", WindowDays: 14}
	if a := x.as(t, teamsReader, http.MethodGet, "/api/v1/team-label-values", ""); a.status != http.StatusOK ||
		strings.TrimSpace(string(a.body)) != `{"items":[],"label":"service_team","window_days":14}` {
		t.Errorf("no values = %d %s", a.status, a.body)
	}
	if a := x.as(t, routesReader, http.MethodGet, "/api/v1/team-label-values", ""); a.status != http.StatusForbidden ||
		!strings.Contains(string(a.body), "teams:read") {
		t.Errorf("without teams:read = %d %s", a.status, a.body)
	}
	ft.err = errors.New("db down")
	if a := x.as(t, teamsReader, http.MethodGet, "/api/v1/team-label-values", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failure = %d %s", a.status, a.body)
	}
}

// TestTeamMentionsAPI is C-25.FR-8 and FR-11 on the API: a Team takes team_mentions, given to the Teams as written and
// saved with If-Match, an update without them keeps them; a read carries team_mentions with their Connection, whether
// it can carry them and why not, and mention_connections with the Team mention of each Connection or null; the Teams'
// refusal group_mentions_unsupported answers 422 at its pointer, and a group or a user group id of another form is
// 422 invalid_format before the Teams.
func TestTeamMentionsAPI(t *testing.T) {
	x, ft, _ := newTeamsAPI(t)
	a := x.as(t, teamsAdmin, http.MethodGet, "/api/v1/teams/"+teamID, "")
	if !strings.Contains(string(a.body), `"team_mentions":[]`) || !strings.Contains(string(a.body),
		`"mention_connections":[]`) {
		t.Errorf("a Team without team mentions = %s", a.body)
	}
	mm := teams.Mention{MentionInput: teams.MentionInput{ConnectionID: "CNAAAAAAAAAAA1", Group: "payments_team"},
		Connection: teams.MentionConnectionRef{PublicID: "CNAAAAAAAAAAA1", Name: "chat", Type: "mattermost"},
		Available:  true}
	sl := teams.Mention{MentionInput: teams.MentionInput{ConnectionID: "CNAAAAAAAAAAS1", Group: "payments-team",
		GroupID: "S0FAKEPAYTEAM"}, Connection: teams.MentionConnectionRef{PublicID: "CNAAAAAAAAAAS1", Name: "slack",
		Type: "slack"}, UnavailableReason: teams.ReasonUsergroupsReadMissing}
	ft.team.Mentions = []teams.Mention{mm, sl}
	ft.team.MentionConnections = []teams.MentionConnection{
		{Connection: mm.Connection, Available: true, Mention: &mm},
		{Connection: sl.Connection, UnavailableReason: teams.ReasonUsergroupsReadMissing, Mention: &sl},
		{Connection: teams.MentionConnectionRef{PublicID: "CNAAAAAAAAAAT1", Name: "tg", Type: "telegram"},
			UnavailableReason: teams.ReasonTelegramNoGroups},
	}
	body := `{"name":"Payments","slug":"payments","own_working_hours":false,"team_mentions":[` +
		`{"connection_id":"CNAAAAAAAAAAA1","group":"payments_team"},` +
		`{"connection_id":"CNAAAAAAAAAAS1","group":"payments-team","group_id":"S0FAKEPAYTEAM"}]}`
	a = x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID, body, "If-Match", `"2"`)
	var team gen.Team
	decodeInto(t, a, &team)
	if a.status != http.StatusOK || len(team.TeamMentions) != 2 || len(team.MentionConnections) != 3 {
		t.Fatalf("update = %d %s", a.status, a.body)
	}
	if in := ft.inputs[0].Mentions; in == nil || len(*in) != 2 || (*in)[0] != (teams.MentionInput{
		ConnectionID: "CNAAAAAAAAAAA1", Group: "payments_team"}) || (*in)[1].GroupID != "S0FAKEPAYTEAM" ||
		*ft.versions[0] != 2 {
		t.Errorf("input team mentions = %+v", in)
	}
	got := team.TeamMentions[1]
	if got.Connection != (gen.ConnectionRef{Id: "CNAAAAAAAAAAS1", Name: "slack", Type: gen.ConnectionType("slack")}) ||
		got.Available || got.UnavailableReason.MustGet() != gen.UsergroupsReadMissing || *got.GroupId !=
		"S0FAKEPAYTEAM" || !team.TeamMentions[0].UnavailableReason.IsNull() || team.TeamMentions[0].GroupId != nil {
		t.Errorf("team mentions = %s", a.body)
	}
	tg := team.MentionConnections[2]
	if !tg.TeamMention.IsNull() || tg.Available || tg.UnavailableReason.MustGet() != gen.TelegramNoGroups ||
		tg.Type != "telegram" || tg.Connection.Name != "tg" ||
		team.MentionConnections[0].TeamMention.MustGet().Group != "payments_team" ||
		!team.MentionConnections[0].UnavailableReason.IsNull() {
		t.Errorf("mention connections = %s", a.body)
	}
	if a = x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID, `{"name":"P","slug":"p",`+
		`"own_working_hours":false}`, "If-Match", `"2"`); a.status != http.StatusOK || ft.inputs[1].Mentions != nil {
		t.Errorf("an update without team mentions = %d, %+v", a.status, ft.inputs[1].Mentions)
	}
	ft.err = &teams.FieldError{Pointer: "/team_mentions/0/connection_id", Code: teams.CodeGroupMentionsUnsupported,
		Detail: "Telegram has no group mentions."}
	status, _, ptr, code := firstProblemError(t, x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID,
		`{"name":"P","slug":"p","own_working_hours":false,"team_mentions":[{"connection_id":"CNAAAAAAAAAAT1",`+
			`"group":"x"}]}`, "If-Match", `"2"`))
	if status != http.StatusUnprocessableEntity || ptr != "/team_mentions/0/connection_id" ||
		code != "group_mentions_unsupported" {
		t.Errorf("telegram = %d %s %s", status, ptr, code)
	}
	ft.err = nil
	calls := len(ft.inputs)
	for _, mention := range []string{`{"group":"x"}`, `{"connection_id":"CNAAAAAAAAAAA1"}`,
		`{"connection_id":"CNAAAAAAAAAAA1","group":5}`} {
		if a := x.as(t, teamsAdmin, http.MethodPut, "/api/v1/teams/"+teamID, `{"name":"P","slug":"p",`+
			`"own_working_hours":false,"team_mentions":[`+mention+`]}`, "If-Match", `"2"`); a.status !=
			http.StatusBadRequest {
			t.Errorf("%s = %d %s", mention, a.status, a.body)
		}
	}
	if len(ft.inputs) != calls {
		t.Error("a request that does not match the schema reached the Teams")
	}
}
