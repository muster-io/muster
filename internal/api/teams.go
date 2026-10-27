// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"net/http"

	"github.com/oapi-codegen/nullable"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/teams"
)

// Teams is what the API needs of internal/teams: Teams, the people in them and the Organization's Working hours.
type Teams interface {
	List(ctx context.Context, f teams.ListFilter) (teams.Page, error)
	Get(ctx context.Context, publicID string) (teams.Team, error)
	Create(ctx context.Context, r teams.Requester, in teams.Input) (teams.Team, error)
	Update(ctx context.Context, r teams.Requester, publicID string, version *int64, in teams.Input) (teams.Team,
		error)
	Delete(ctx context.Context, r teams.Requester, publicID string, version *int64) error
	Users(ctx context.Context, publicID string) ([]teams.Person, error)
	AddUser(ctx context.Context, r teams.Requester, teamID, userID string) (teams.Person, bool, error)
	RemoveUser(ctx context.Context, r teams.Requester, teamID, userID string) error
}

// teamsCursor is the cursor of the Team list.
const teamsCursor = "teams"

// teamMine is the value of the parameter team that names the Teams of the calling User, "My Teams".
const teamMine = "mine"

// teamsRequester is who asks for a change of a Team through the API.
func teamsRequester(ctx context.Context) (teams.Requester, error) {
	id, err := identity(ctx)
	if err != nil {
		return teams.Requester{}, err
	}
	return teams.Requester{Actor: id.Actor(), Transport: id.Transport, Address: clientAddress(ctx)}, nil
}

// ListTeams is listTeams: the Teams that are not deleted by name, matching q.
func (s *Server) ListTeams(ctx context.Context, req gen.ListTeamsRequestObject) (gen.ListTeamsResponseObject, error) {
	p := req.Params
	f := teams.ListFilter{Limit: pageSize(p.Limit)}
	if p.Q != nil {
		f.Query = *p.Q
	}
	var after teams.Position
	if ok, err := decodeCursor(p.Cursor, teamsCursor, &after); err != nil {
		return nil, err
	} else if ok {
		f.After = &after
	}
	page, err := s.teams.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.TeamList{Items: make([]gen.Team, 0, len(page.Teams))}
	for _, t := range page.Teams {
		out.Items = append(out.Items, teamOf(t))
	}
	out.NextCursor.SetNull()
	if page.Next != nil {
		out.NextCursor.Set(encodeCursor(teamsCursor, *page.Next))
	}
	return gen.ListTeams200JSONResponse(out), nil
}

// CreateTeam is createTeam.
func (s *Server) CreateTeam(ctx context.Context, req gen.CreateTeamRequestObject) (gen.CreateTeamResponseObject,
	error) {
	r, err := teamsRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	t, err := s.teams.Create(ctx, r, teamInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag, location := etag(t.Version), BasePath+"/teams/"+t.PublicID
	return gen.CreateTeam201JSONResponse{Body: teamOf(t),
		Headers: gen.CreateTeam201ResponseHeaders{ETag: &tag, Location: &location}}, nil
}

// GetTeam is getTeam.
func (s *Server) GetTeam(ctx context.Context, req gen.GetTeamRequestObject) (gen.GetTeamResponseObject, error) {
	t, err := s.teams.Get(ctx, req.TeamId)
	if err != nil {
		return nil, err
	}
	tag := etag(t.Version)
	return gen.GetTeam200JSONResponse{Body: teamOf(t), Headers: gen.GetTeam200ResponseHeaders{ETag: &tag}}, nil
}

// UpdateTeam is updateTeam, with If-Match.
func (s *Server) UpdateTeam(ctx context.Context, req gen.UpdateTeamRequestObject) (gen.UpdateTeamResponseObject,
	error) {
	r, err := teamsRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ifMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	t, err := s.teams.Update(ctx, r, req.TeamId, version, teamInputOf(*req.Body))
	if err != nil {
		return nil, err
	}
	tag := etag(t.Version)
	return gen.UpdateTeam200JSONResponse{Body: teamOf(t), Headers: gen.UpdateTeam200ResponseHeaders{ETag: &tag}}, nil
}

// DeleteTeam is deleteTeam, with an optional If-Match.
func (s *Server) DeleteTeam(ctx context.Context, req gen.DeleteTeamRequestObject) (gen.DeleteTeamResponseObject,
	error) {
	r, err := teamsRequester(ctx)
	if err != nil {
		return nil, err
	}
	version, err := optionalVersion(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	if err := s.teams.Delete(ctx, r, req.TeamId, version); err != nil {
		return nil, err
	}
	return gen.DeleteTeam204Response{}, nil
}

// ListTeamUsers is listTeamUsers.
func (s *Server) ListTeamUsers(ctx context.Context, req gen.ListTeamUsersRequestObject) (
	gen.ListTeamUsersResponseObject, error) {
	list, err := s.teams.Users(ctx, req.TeamId)
	if err != nil {
		return nil, err
	}
	out := gen.TeamUserList{Items: make([]gen.TeamUser, 0, len(list))}
	for _, m := range list {
		out.Items = append(out.Items, teamUserOf(m))
	}
	return gen.ListTeamUsers200JSONResponse(out), nil
}

// AddTeamUser is addTeamUser: 201 when the person was added, 200 when they were in the Team by hand already.
func (s *Server) AddTeamUser(ctx context.Context, req gen.AddTeamUserRequestObject) (gen.AddTeamUserResponseObject,
	error) {
	r, err := teamsRequester(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBodyMissing
	}
	m, added, err := s.teams.AddUser(ctx, r, req.TeamId, req.Body.UserId)
	if err != nil {
		return nil, err
	}
	if added {
		team, _ := publicid.Parse(publicid.Team, req.TeamId) // the addition found the Team by it
		location := BasePath + "/teams/" + team + "/users/" + m.PublicID
		return gen.AddTeamUser201JSONResponse{Body: teamUserOf(m),
			Headers: gen.AddTeamUser201ResponseHeaders{Location: &location}}, nil
	}
	return gen.AddTeamUser200JSONResponse(teamUserOf(m)), nil
}

// RemoveTeamUser is removeTeamUser.
func (s *Server) RemoveTeamUser(ctx context.Context, req gen.RemoveTeamUserRequestObject) (
	gen.RemoveTeamUserResponseObject, error) {
	r, err := teamsRequester(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.teams.RemoveUser(ctx, r, req.TeamId, req.UserId); err != nil {
		return nil, err
	}
	return gen.RemoveTeamUser204Response{}, nil
}

// teamInputOf is a Team as internal/teams takes it.
func teamInputOf(b gen.TeamInput) teams.Input {
	in := teams.Input{Name: b.Name, Slug: b.Slug, Description: b.Description, OwnWorkingHours: b.OwnWorkingHours}
	if w := b.WorkingHours; w != nil {
		hours := workingHoursIn(*w)
		in.WorkingHours = &hours
	}
	return in
}

// workingHoursIn are Working hours as internal/teams takes them.
func workingHoursIn(w gen.WorkingHours) teams.WorkingHours {
	days := make([]string, len(w.Days))
	for i, d := range w.Days {
		days[i] = string(d)
	}
	return teams.WorkingHours{Days: days, Start: w.Start, End: w.End, TimeZone: w.TimeZone}
}

// workingHoursOf is the API form of Working hours.
func workingHoursOf(w teams.WorkingHours) gen.WorkingHours {
	days := make([]gen.Weekday, len(w.Days))
	for i, d := range w.Days {
		days[i] = gen.Weekday(d)
	}
	return gen.WorkingHours{Days: days, Start: w.Start, End: w.End, TimeZone: w.TimeZone}
}

// teamOf is the API form of a Team, with its effective Working hours.
func teamOf(t teams.Team) gen.Team {
	tag := etag(t.Version)
	return gen.Team{Id: t.PublicID, Name: t.Name, Slug: t.Slug, Description: t.Description,
		OwnWorkingHours: t.Own != nil, WorkingHours: workingHoursOf(t.WorkingHours), UserCount: int(t.UserCount),
		CreatedAt: t.CreatedAt.UTC(), Etag: &tag}
}

// teamUserOf is the API form of a person in a Team.
func teamUserOf(m teams.Person) gen.TeamUser {
	login := m.Login
	out := gen.TeamUser{User: gen.UserRef{Id: m.PublicID, Name: m.Name, Login: &login}, Role: gen.RoleName(m.Role),
		Sources: make([]gen.TeamUserSource, 0, len(m.Sources)), LinkedMessengers: make([]gen.LinkedMessenger, 0,
			len(m.Linked))}
	for _, src := range m.Sources {
		out.Sources = append(out.Sources, gen.TeamUserSource(src))
	}
	for _, l := range m.Linked {
		out.LinkedMessengers = append(out.LinkedMessengers, gen.LinkedMessenger{Type: gen.ConnectionType(l.Type),
			Connection: gen.EntityRef{Id: l.ConnectionPublicID, Name: l.ConnectionName}})
	}
	return out
}

// owningTeamOf is the owning Team of an input as the domain packages take it: nil when omitted, which keeps it on an
// update, an empty string for null, which clears it, else its public_id.
func owningTeamOf(v nullable.Nullable[gen.NullablePublicId]) *string {
	if !v.IsSpecified() {
		return nil
	}
	id := ""
	if !v.IsNull() {
		id = v.MustGet()
	}
	return &id
}

// teamRefOf is the API form of the Team that owns an object, null for none.
func teamRefOf(publicID, name, slug string, deleted, set bool) nullable.Nullable[gen.TeamRef] {
	var out nullable.Nullable[gen.TeamRef]
	if !set {
		out.SetNull()
		return out
	}
	out.Set(gen.TeamRef{Id: publicID, Name: name, Slug: slug, Deleted: deleted})
	return out
}

// owningTeamIDOf is the owning_team_id that a read carries, null for none.
func owningTeamIDOf(publicID string, set bool) nullable.Nullable[gen.NullablePublicId] {
	var out nullable.Nullable[gen.NullablePublicId]
	if !set {
		out.SetNull()
		return out
	}
	out.Set(publicID)
	return out
}

// errServiceAccountTeams is the refusal of team=mine for a Service account, which is in no Team.
var errServiceAccountTeams = fieldProblem(http.StatusUnprocessableEntity, "/query/team", fieldUnsupported,
	"A Service account is in no Team; name a Team instead of mine.")

// owningTeamQuery reads the parameter team of a list: the public_id of the Team that owns what the list keeps, or
// with mine the internal id of the calling User, whose Teams own it. A Service account's mine is refused with 422.
func owningTeamQuery(ctx context.Context, team *string) (string, int64, error) {
	if team == nil || *team != teamMine {
		if team == nil {
			return "", 0, nil
		}
		return *team, 0, nil
	}
	id, err := identity(ctx)
	if err != nil {
		return "", 0, err
	}
	if id.IsServiceAccount() {
		return "", 0, errServiceAccountTeams
	}
	return "", id.Session.User.ID, nil
}
