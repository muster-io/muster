// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/routing"
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
