// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/oidc"
	"github.com/muster-io/muster/internal/teams/dbgen"
)

const orgID = 7

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

var by = Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportAPI}

var errBoom = errors.New("boom")

// teamRec is a row of teams.
type teamRec struct {
	dbgen.GetTeamRow
	deleted bool
}

// owned is a Route or a Destination with its owning Team, 0 for none.
type owned struct {
	publicID, name string
	team           int64
	version        int64
	deleted        bool
}

// fakeStore is the database of the package in memory.
type fakeStore struct {
	teams  []*teamRec
	people map[int64][]dbgen.GetTeamUserRow
	users  map[string]dbgen.LockTeamUserRow
	links  []dbgen.ListLinkedMessengersRow
	dests  []*owned
	routes []*owned
	org    dbgen.GetOrganizationWorkingHoursRow
	audit  []auditdb.InsertAuditEntryParams
	hints  []db.Hint
	fail   map[string]error
	nextID int64
	listed []dbgen.ListTeamsParams
	// matchers are the Team matchers of each Team, version organizations.teams_version, and locked how often the
	// Organization's row was locked for a change of a Team.
	matchers map[int64][]dbgen.ListTeamMatchersRow
	version  int64
	locked   int
	// lockedThen runs after LockTeamMember read the row, as what another transaction would have done to it had the lock
	// not held it off.
	lockedThen func()
	// entries are the identity provider entries of each Team, in their order.
	entries map[int64][]dbgen.ListTeamIdpEntriesRow
}

func newStore() *fakeStore {
	return &fakeStore{people: map[int64][]dbgen.GetTeamUserRow{}, entries: map[int64][]dbgen.ListTeamIdpEntriesRow{},
		users: map[string]dbgen.LockTeamUserRow{
			"SR0000000000BB": {ID: 2, PublicID: "SR0000000000BB", Name: "bob", Login: "bob", Role: "responder",
				Status: "active"},
			"SR0000000000VV": {ID: 3, PublicID: "SR0000000000VV", Name: "vic", Login: "vic", Role: "viewer",
				Status: "disabled"},
		},
		org: dbgen.GetOrganizationWorkingHoursRow{WorkingDays: []string{"monday", "tuesday", "wednesday", "thursday",
			"friday"}, WorkdayStart: "09:00", WorkdayEnd: "18:00", TimeZone: "UTC"},
		fail: map[string]error{}}
}

func (s *fakeStore) live() []*teamRec {
	var out []*teamRec
	for _, t := range s.teams {
		if !t.deleted {
			out = append(out, t)
		}
	}
	return out
}

func (s *fakeStore) byID(id int64) *teamRec {
	for _, t := range s.teams {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// unique refuses a name or a slug another Team that is not deleted has, as the unique indexes do.
func (s *fakeStore) unique(name, slug string, except int64) error {
	for _, t := range s.live() {
		switch {
		case t.ID == except:
		case strings.EqualFold(t.Name, name):
			return &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameKey}
		case t.Slug == slug:
			return &pgconn.PgError{Code: uniqueViolation, ConstraintName: slugKey}
		}
	}
	return nil
}

func (s *fakeStore) withCount(t *teamRec) dbgen.GetTeamRow {
	r := t.GetTeamRow
	r.UserCount = int64(len(s.people[t.ID]))
	return r
}

func (s *fakeStore) GetOrganizationWorkingHours(_ context.Context, org int64) (dbgen.GetOrganizationWorkingHoursRow,
	error) {
	if org != orgID {
		return dbgen.GetOrganizationWorkingHoursRow{}, pgx.ErrNoRows
	}
	return s.org, s.fail["GetOrganizationWorkingHours"]
}

func (s *fakeStore) ListTeams(_ context.Context, arg dbgen.ListTeamsParams) ([]dbgen.ListTeamsRow, error) {
	s.listed = append(s.listed, arg)
	if err := s.fail["ListTeams"]; err != nil {
		return nil, err
	}
	live := s.live()
	slices.SortFunc(live, func(a, b *teamRec) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	var out []dbgen.ListTeamsRow
	for _, t := range live {
		if arg.Q.Valid && !strings.Contains(strings.ToLower(t.Name), strings.ToLower(arg.Q.String)) &&
			!strings.Contains(t.Slug, strings.ToLower(arg.Q.String)) {
			continue
		}
		if arg.AfterID.Valid && cmp.Or(strings.Compare(strings.ToLower(t.Name), arg.AfterName.String),
			cmp.Compare(t.ID, arg.AfterID.Int64)) <= 0 {
			continue
		}
		if len(out) == int(arg.PageSize) {
			break
		}
		r := s.withCount(t)
		out = append(out, dbgen.ListTeamsRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Slug: r.Slug,
			Description: r.Description, OwnWorkingHours: r.OwnWorkingHours, WorkingDays: r.WorkingDays,
			WorkingStart: r.WorkingStart, WorkingEnd: r.WorkingEnd, WorkingTimeZone: r.WorkingTimeZone,
			CreatedAt: r.CreatedAt, Version: r.Version, UserCount: r.UserCount, SortName: strings.ToLower(r.Name)})
	}
	return out, nil
}

func (s *fakeStore) GetTeam(_ context.Context, arg dbgen.GetTeamParams) (dbgen.GetTeamRow, error) {
	if err := s.fail["GetTeam"]; err != nil {
		return dbgen.GetTeamRow{}, err
	}
	for _, t := range s.live() {
		if t.PublicID == arg.PublicID && arg.OrgID == orgID {
			return s.withCount(t), nil
		}
	}
	return dbgen.GetTeamRow{}, pgx.ErrNoRows
}

func (s *fakeStore) LockTeam(_ context.Context, arg dbgen.LockTeamParams) (int64, error) {
	if err := s.fail["LockTeam"]; err != nil {
		return 0, err
	}
	for _, t := range s.live() {
		if t.PublicID == arg.PublicID && arg.OrgID == orgID {
			return t.ID, nil
		}
	}
	return 0, pgx.ErrNoRows
}

func (s *fakeStore) InsertTeam(_ context.Context, arg dbgen.InsertTeamParams) (int64, error) {
	if err := s.fail["InsertTeam"]; err != nil {
		return 0, err
	}
	if err := s.unique(arg.Name, arg.Slug, 0); err != nil {
		return 0, err
	}
	s.nextID++
	s.teams = append(s.teams, &teamRec{GetTeamRow: dbgen.GetTeamRow{ID: s.nextID, PublicID: arg.PublicID,
		Name: arg.Name, Slug: arg.Slug, Description: arg.Description, OwnWorkingHours: arg.OwnWorkingHours,
		WorkingDays: arg.WorkingDays, WorkingStart: arg.WorkingStart, WorkingEnd: arg.WorkingEnd,
		WorkingTimeZone: arg.WorkingTimeZone, CreatedAt: arg.Now, Version: 1}})
	return s.nextID, nil
}

func (s *fakeStore) UpdateTeam(_ context.Context, arg dbgen.UpdateTeamParams) error {
	if err := s.fail["UpdateTeam"]; err != nil {
		return err
	}
	if err := s.unique(arg.Name, arg.Slug, arg.ID); err != nil {
		return err
	}
	t := s.byID(arg.ID)
	t.Name, t.Slug, t.Description, t.OwnWorkingHours = arg.Name, arg.Slug, arg.Description, arg.OwnWorkingHours
	t.WorkingDays, t.WorkingStart, t.WorkingEnd = arg.WorkingDays, arg.WorkingStart, arg.WorkingEnd
	t.WorkingTimeZone, t.Version = arg.WorkingTimeZone, t.Version+1
	return nil
}

func (s *fakeStore) DeleteTeam(_ context.Context, arg dbgen.DeleteTeamParams) error {
	if err := s.fail["DeleteTeam"]; err != nil {
		return err
	}
	t := s.byID(arg.ID)
	t.deleted, t.Version = true, t.Version+1
	return nil
}

func (s *fakeStore) DeleteTeamUsers(_ context.Context, arg dbgen.DeleteTeamUsersParams) error {
	if err := s.fail["DeleteTeamUsers"]; err != nil {
		return err
	}
	delete(s.people, arg.TeamID)
	return nil
}

// release leaves the objects in list that the Team owns without an owner, each with a new version.
func release[R any](list []*owned, team int64, row func(o *owned) R) []R {
	var out []R
	for _, o := range list {
		if o.team == team && !o.deleted {
			o.team, o.version = 0, o.version+1
			out = append(out, row(o))
		}
	}
	return out
}

func (s *fakeStore) ReleaseOwnedDestinations(_ context.Context, arg dbgen.ReleaseOwnedDestinationsParams) (
	[]dbgen.ReleaseOwnedDestinationsRow, error) {
	if err := s.fail["ReleaseOwnedDestinations"]; err != nil {
		return nil, err
	}
	return release(s.dests, arg.TeamID.Int64, func(o *owned) dbgen.ReleaseOwnedDestinationsRow {
		return dbgen.ReleaseOwnedDestinationsRow{PublicID: o.publicID, Name: o.name}
	}), nil
}

func (s *fakeStore) ReleaseOwnedRoutes(_ context.Context, arg dbgen.ReleaseOwnedRoutesParams) (
	[]dbgen.ReleaseOwnedRoutesRow, error) {
	if err := s.fail["ReleaseOwnedRoutes"]; err != nil {
		return nil, err
	}
	return release(s.routes, arg.TeamID.Int64, func(o *owned) dbgen.ReleaseOwnedRoutesRow {
		return dbgen.ReleaseOwnedRoutesRow{PublicID: o.publicID, Name: o.name}
	}), nil
}

func (s *fakeStore) ListTeamUsers(_ context.Context, arg dbgen.ListTeamUsersParams) ([]dbgen.ListTeamUsersRow,
	error) {
	if err := s.fail["ListTeamUsers"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListTeamUsersRow
	for _, m := range s.people[arg.TeamID] {
		out = append(out, dbgen.ListTeamUsersRow(m))
	}
	return out, nil
}

func (s *fakeStore) ListLinkedMessengers(_ context.Context, arg dbgen.ListLinkedMessengersParams) (
	[]dbgen.ListLinkedMessengersRow, error) {
	if err := s.fail["ListLinkedMessengers"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListLinkedMessengersRow
	for _, l := range s.links {
		if slices.Contains(arg.UserIds, l.UserID) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s *fakeStore) LockTeamUser(_ context.Context, arg dbgen.LockTeamUserParams) (dbgen.LockTeamUserRow, error) {
	if err := s.fail["LockTeamUser"]; err != nil {
		return dbgen.LockTeamUserRow{}, err
	}
	u, ok := s.users[arg.PublicID]
	if !ok || u.Status == "deleted" {
		return dbgen.LockTeamUserRow{}, pgx.ErrNoRows
	}
	return u, nil
}

func (s *fakeStore) GetTeamUser(_ context.Context, arg dbgen.GetTeamUserParams) (dbgen.GetTeamUserRow, error) {
	if err := s.fail["GetTeamUser"]; err != nil {
		return dbgen.GetTeamUserRow{}, err
	}
	for _, m := range s.people[arg.TeamID] {
		if m.PublicID == arg.PublicID {
			return m, nil
		}
	}
	return dbgen.GetTeamUserRow{}, pgx.ErrNoRows
}

func (s *fakeStore) LockTeamMember(ctx context.Context, arg dbgen.LockTeamMemberParams) (dbgen.LockTeamMemberRow,
	error) {
	if err := s.fail["LockTeamMember"]; err != nil {
		return dbgen.LockTeamMemberRow{}, err
	}
	r, err := s.GetTeamUser(ctx, dbgen.GetTeamUserParams(arg))
	if s.lockedThen != nil {
		s.lockedThen()
	}
	return dbgen.LockTeamMemberRow(r), err
}

func (s *fakeStore) InsertTeamUser(_ context.Context, arg dbgen.InsertTeamUserParams) (int64, error) {
	if err := s.fail["InsertTeamUser"]; err != nil {
		return 0, err
	}
	for i, m := range s.people[arg.TeamID] {
		if m.ID == arg.UserID {
			if m.ByHand {
				return 0, nil
			}
			s.people[arg.TeamID][i].ByHand = true
			return 1, nil
		}
	}
	for _, u := range s.users {
		if u.ID == arg.UserID {
			s.people[arg.TeamID] = append(s.people[arg.TeamID], dbgen.GetTeamUserRow{ID: u.ID,
				PublicID: u.PublicID, Name: u.Name, Login: u.Login, Role: u.Role, Status: u.Status, ByHand: true})
		}
	}
	return 1, nil
}

func (s *fakeStore) DeleteTeamUser(_ context.Context, arg dbgen.DeleteTeamUserParams) (int64, error) {
	if err := s.fail["DeleteTeamUser"]; err != nil {
		return 0, err
	}
	before := len(s.people[arg.TeamID])
	s.people[arg.TeamID] = slices.DeleteFunc(s.people[arg.TeamID], func(m dbgen.GetTeamUserRow) bool {
		return m.ID == arg.UserID
	})
	return int64(before - len(s.people[arg.TeamID])), nil
}

func (s *fakeStore) DeleteUserTeams(_ context.Context, arg dbgen.DeleteUserTeamsParams) ([]dbgen.DeleteUserTeamsRow,
	error) {
	if err := s.fail["DeleteUserTeams"]; err != nil {
		return nil, err
	}
	var out []dbgen.DeleteUserTeamsRow
	for _, t := range s.teams {
		for _, m := range s.people[t.ID] {
			if m.ID == arg.UserID {
				out = append(out, dbgen.DeleteUserTeamsRow{PublicID: t.PublicID, Name: t.Name,
					UserPublicID: m.PublicID, UserName: "deleted-user-" + m.PublicID, ByHand: m.ByHand, ByIdp: m.ByIdp})
			}
		}
		s.people[t.ID] = slices.DeleteFunc(s.people[t.ID], func(m dbgen.GetTeamUserRow) bool {
			return m.ID == arg.UserID
		})
	}
	return out, nil
}

func (s *fakeStore) ListTeamInfo(_ context.Context, org int64) ([]dbgen.ListTeamInfoRow, error) {
	if err := s.fail["ListTeamInfo"]; err != nil || org != orgID {
		return nil, err
	}
	var out []dbgen.ListTeamInfoRow
	for _, t := range s.live() {
		out = append(out, dbgen.ListTeamInfoRow{PublicID: t.PublicID, Name: t.Name})
	}
	return out, nil
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.fail["InsertAuditEntry"]; err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

func (s *fakeStore) Notify(_ context.Context, h db.Hint) error {
	if err := s.fail["Notify"]; err != nil {
		return err
	}
	s.hints = append(s.hints, h)
	return nil
}

func (s *fakeStore) ClearByHand(_ context.Context, arg dbgen.ClearByHandParams) (int64, error) {
	if err := s.fail["ClearByHand"]; err != nil {
		return 0, err
	}
	if m := s.member(arg.TeamID, arg.UserID); m != nil && m.ByHand && m.ByIdp {
		m.ByHand = false
		return 1, nil
	}
	return 0, nil
}

// member is the row of the User in the Team, nil when they are not in it.
func (s *fakeStore) member(teamID, userID int64) *dbgen.GetTeamUserRow {
	for i := range s.people[teamID] {
		if s.people[teamID][i].ID == userID {
			return &s.people[teamID][i]
		}
	}
	return nil
}

func (s *fakeStore) ListTeamIdpEntries(_ context.Context, arg dbgen.ListTeamIdpEntriesParams) (
	[]dbgen.ListTeamIdpEntriesRow, error) {
	if err := s.fail["ListTeamIdpEntries"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListTeamIdpEntriesRow
	for _, id := range arg.TeamIds {
		out = append(out, s.entries[id]...)
	}
	return out, nil
}

func (s *fakeStore) DeleteTeamIdpEntries(_ context.Context, arg dbgen.DeleteTeamIdpEntriesParams) error {
	if err := s.fail["DeleteTeamIdpEntries"]; err != nil {
		return err
	}
	delete(s.entries, arg.TeamID)
	return nil
}

func (s *fakeStore) InsertTeamIdpEntries(_ context.Context, arg dbgen.InsertTeamIdpEntriesParams) error {
	if err := s.fail["InsertTeamIdpEntries"]; err != nil {
		return err
	}
	for i, g := range arg.GroupNames {
		s.entries[arg.TeamID] = append(s.entries[arg.TeamID], dbgen.ListTeamIdpEntriesRow{TeamID: arg.TeamID,
			Claim: pgtype.Text{String: arg.Claims[i], Valid: arg.Claims[i] != ""}, GroupName: g})
	}
	return nil
}

func (s *fakeStore) ListIdpEntryClaims(context.Context, int64) ([]string, error) {
	if err := s.fail["ListIdpEntryClaims"]; err != nil {
		return nil, err
	}
	var out []string
	for _, t := range s.live() {
		for _, e := range s.entries[t.ID] {
			if !slices.Contains(out, e.Claim.String) {
				out = append(out, e.Claim.String)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func (s *fakeStore) ListIdpEntries(context.Context, int64) ([]dbgen.ListIdpEntriesRow, error) {
	if err := s.fail["ListIdpEntries"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListIdpEntriesRow
	for _, t := range s.live() {
		for _, e := range s.entries[t.ID] {
			out = append(out, dbgen.ListIdpEntriesRow{TeamID: t.ID, PublicID: t.PublicID, Name: t.Name,
				Claim: e.Claim, GroupName: e.GroupName})
		}
	}
	return out, nil
}

func (s *fakeStore) LockUserMemberships(_ context.Context, arg dbgen.LockUserMembershipsParams) (
	[]dbgen.LockUserMembershipsRow, error) {
	if err := s.fail["LockUserMemberships"]; err != nil {
		return nil, err
	}
	var out []dbgen.LockUserMembershipsRow
	for _, t := range s.live() {
		if m := s.member(t.ID, arg.UserID); m != nil {
			out = append(out, dbgen.LockUserMembershipsRow{TeamID: t.ID, PublicID: t.PublicID, Name: t.Name,
				ByHand: m.ByHand, ByIdp: m.ByIdp, IdpMatches: m.IdpMatches})
		}
	}
	return out, nil
}

func (s *fakeStore) GetMember(_ context.Context, arg dbgen.GetMemberParams) (dbgen.GetMemberRow, error) {
	if err := s.fail["GetMember"]; err != nil {
		return dbgen.GetMemberRow{}, err
	}
	for _, u := range s.users {
		if u.ID == arg.ID && u.Status != "deleted" {
			return dbgen.GetMemberRow{PublicID: u.PublicID, Name: u.Name}, nil
		}
	}
	return dbgen.GetMemberRow{}, pgx.ErrNoRows
}

func (s *fakeStore) JoinThroughIdp(_ context.Context, arg dbgen.JoinThroughIdpParams) error {
	if err := s.fail["JoinThroughIdp"]; err != nil {
		return err
	}
	if m := s.member(arg.TeamID, arg.UserID); m != nil {
		m.ByIdp, m.IdpMatches = true, arg.IdpMatches
		return nil
	}
	for _, u := range s.users {
		if u.ID == arg.UserID {
			s.people[arg.TeamID] = append(s.people[arg.TeamID], dbgen.GetTeamUserRow{ID: u.ID,
				PublicID: u.PublicID, Name: u.Name, Login: u.Login, Role: u.Role, Status: u.Status, ByIdp: true,
				IdpMatches: arg.IdpMatches})
		}
	}
	return nil
}

func (s *fakeStore) SetIdpMatches(_ context.Context, arg dbgen.SetIdpMatchesParams) error {
	if err := s.fail["SetIdpMatches"]; err != nil {
		return err
	}
	s.member(arg.TeamID, arg.UserID).IdpMatches = arg.IdpMatches
	return nil
}

func (s *fakeStore) LeaveThroughIdp(_ context.Context, arg dbgen.LeaveThroughIdpParams) error {
	if err := s.fail["LeaveThroughIdp"]; err != nil {
		return err
	}
	if m := s.member(arg.TeamID, arg.UserID); m != nil && m.ByHand {
		m.ByIdp, m.IdpMatches = false, []byte(`[]`)
	}
	return nil
}

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error { return f(s) }

func (s *fakeStore) On(dbgen.DBTX) Queries { return s }

func newService(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	store := newStore()
	c := clock.NewManual(t0)
	return New(Config{OrgID: orgID, Store: store, Audit: audit.NewWriter(logging.New(nilWriter{},
		logging.LevelInfo), c), Business: c}), store
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }

func ptr[T any](v T) *T { return &v }

func fieldError(t *testing.T, err error) *FieldError {
	t.Helper()
	f, ok := errors.AsType[*FieldError](err)
	if !ok {
		t.Fatalf("err = %v, want a FieldError", err)
	}
	return f
}

// infoSeries are the muster_team_info series of the Team id, as /metrics shows them.
func infoSeries(t *testing.T, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	var out []string
	for line := range strings.Lines(rec.Body.String()) {
		if strings.HasPrefix(line, `muster_team_info{team="`+id+`"`) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, "\n")
}

var berlin = WorkingHours{Days: []string{"friday", "monday"}, Start: "11:00", End: "19:00", TimeZone: "Europe/Berlin"}

// TestCheck is C-25.FR-1, FR-9 and team.slug_format (P-74): the name, the slug, the description and the Working hours,
// each refused at its pointer with its code; a valid slug of 1 to 63 characters passes.
func TestCheck(t *testing.T) {
	valid := Input{Name: " Payments ", Slug: "payments", OwnWorkingHours: false, WorkingHours: &berlin}
	got, err := check(valid)
	if err != nil || got.Name != "Payments" || got.WorkingHours != nil {
		t.Fatalf("valid = %+v, %v", got, err)
	}
	for _, slug := range []string{"a", "0", "a.b-c_d", strings.Repeat("a", 63), "9to5"} {
		if _, err := check(Input{Name: "x", Slug: slug}); err != nil {
			t.Errorf("%q: %v", slug, err)
		}
	}
	hours := func(f func(w *WorkingHours)) Input {
		w := berlin
		w.Days = slices.Clone(berlin.Days)
		f(&w)
		return Input{Name: "x", Slug: "x", OwnWorkingHours: true, WorkingHours: &w}
	}
	for _, c := range []struct {
		in            Input
		pointer, code string
	}{
		{Input{Name: "  ", Slug: "x"}, "/name", CodeRequired},
		{Input{Name: strings.Repeat("n", 201), Slug: "x"}, "/name", CodeTooLong},
		{Input{Name: "x", Slug: "Payments"}, "/slug", CodeInvalidFormat},
		{Input{Name: "x", Slug: "-x"}, "/slug", CodeInvalidFormat},
		{Input{Name: "x", Slug: strings.Repeat("a", 64)}, "/slug", CodeInvalidFormat},
		{Input{Name: "x", Slug: ""}, "/slug", CodeInvalidFormat},
		{Input{Name: "x", Slug: "pay ments"}, "/slug", CodeInvalidFormat},
		{Input{Name: "x", Slug: "x", Description: ptr(strings.Repeat("d", 2001))}, "/description", CodeTooLong},
		{hours(func(w *WorkingHours) { w.Days = nil }), "/working_hours/days", CodeRequired},
		{hours(func(w *WorkingHours) { w.Days = []string{"monday", "funday"} }), "/working_hours/days/1",
			CodeInvalidFormat},
		{hours(func(w *WorkingHours) { w.Days = []string{"monday", "monday"} }), "/working_hours/days/1",
			CodeDuplicate},
		{hours(func(w *WorkingHours) { w.Start = "9:00" }), "/working_hours/start", CodeInvalidFormat},
		{hours(func(w *WorkingHours) { w.End = "24:00" }), "/working_hours/end", CodeInvalidFormat},
		{hours(func(w *WorkingHours) { w.Start, w.End = "19:00", "11:00" }), "/working_hours/end", CodeOutOfRange},
		{hours(func(w *WorkingHours) { w.Start, w.End = "11:00", "11:00" }), "/working_hours/end", CodeOutOfRange},
		{hours(func(w *WorkingHours) { w.TimeZone = "Mars/Olympus" }), "/working_hours/time_zone",
			CodeInvalidFormat},
		{hours(func(w *WorkingHours) { w.TimeZone = "Local" }), "/working_hours/time_zone", CodeInvalidFormat},
	} {
		_, err := check(c.in)
		if f := fieldError(t, err); f.Pointer != c.pointer || f.Code != c.code || f.Error() == "" {
			t.Errorf("%+v: %+v", c.in, f)
		}
	}
	got, err = check(hours(func(*WorkingHours) {}))
	if err != nil || !slices.Equal(got.WorkingHours.Days, []string{"monday", "friday"}) {
		t.Errorf("the days in the order of the week: %+v, %v", got.WorkingHours, err)
	}
}

// TestWorkingHours is C-25.FR-9: a Team without its own Working hours follows the Organization's, and Contains tells
// an instant inside them, in their time zone, from one outside.
func TestWorkingHours(t *testing.T) {
	org := WorkingHours{Days: []string{"monday", "tuesday", "wednesday", "thursday", "friday"}, Start: "09:00",
		End: "18:00", TimeZone: "UTC"}
	if got := Effective(Team{}, org); !slices.Equal(got.Days, org.Days) || got.Start != "09:00" {
		t.Errorf("without own hours = %+v", got)
	}
	own := berlin
	if got := Effective(Team{Own: &own}, org); got.TimeZone != "Europe/Berlin" {
		t.Errorf("with own hours = %+v", got)
	}
	// Monday 2026-10-26: 09:00 UTC is 10:00 in Berlin, before 11:00; 10:00 UTC is 11:00, inside; 18:00 UTC is
	// 19:00, the end, outside. Tuesday is not a day of these hours.
	monday := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{monday.Add(9 * time.Hour), false},
		{monday.Add(10 * time.Hour), true},
		{monday.Add(17*time.Hour + 59*time.Minute), true},
		{monday.Add(18 * time.Hour), false},
		{monday.Add(34 * time.Hour), false},
	} {
		if got := berlin.Contains(c.at); got != c.want {
			t.Errorf("%s: %v", c.at, got)
		}
	}
	if (WorkingHours{Days: []string{"monday"}, Start: "00:00", End: "23:59", TimeZone: "Nowhere/Land"}).Contains(
		monday) {
		t.Error("hours with an unknown time zone contain an instant")
	}
	if !ValidTimeZone("Europe/Berlin") || ValidTimeZone("") || ValidTimeZone(strings.Repeat("x", 65)) {
		t.Error("ValidTimeZone")
	}
}

// TestCreate is C-25.FR-1 and FR-11: a Team with a name, a slug, a description and its own Working hours, recorded as
// team.created with its values, announced with the hint team and exported as muster_team_info; a name taken ignoring
// case is ErrNameTaken and a slug taken ErrSlugTaken; own Working hours without hours are required.
func TestCreate(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	team, err := svc.Create(ctx, by, Input{Name: "Payments", Slug: "payments", Description: ptr("Pays"),
		OwnWorkingHours: true, WorkingHours: &berlin})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(team.PublicID, "TM") || team.Name != "Payments" || team.Description != "Pays" ||
		team.Own == nil || team.WorkingHours.TimeZone != "Europe/Berlin" || team.Version != 1 ||
		!team.CreatedAt.Equal(t0) {
		t.Fatalf("created %+v", team)
	}
	e := store.audit[0]
	var diff []audit.Change
	_ = json.Unmarshal(e.Diff, &diff)
	if e.Action != ActionCreated || e.ResourceType.String != ResourceTeam || e.ResourcePublicID.String !=
		team.PublicID || len(diff) != 9 || !strings.Contains(string(e.Diff), `"pointer":"/slug","after":"payments"`) {
		t.Errorf("audit %+v %s", e, e.Diff)
	}
	if len(store.hints) != 1 || store.hints[0] != (db.Hint{OrgID: orgID, Type: Hint, ID: team.PublicID}) {
		t.Errorf("hints %+v", store.hints)
	}
	if got := infoSeries(t, team.PublicID); got != `muster_team_info{team="`+team.PublicID+`",name="Payments"} 1` {
		t.Errorf("info %q", got)
	}

	plain, err := svc.Create(ctx, by, Input{Name: "Search", Slug: "search"})
	if err != nil || plain.Own != nil || plain.WorkingHours.Start != "09:00" || plain.Description != "" {
		t.Errorf("following the Organization = %+v, %v", plain, err)
	}
	if _, err := svc.Create(ctx, by, Input{Name: "PAYMENTS", Slug: "other"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	if _, err := svc.Create(ctx, by, Input{Name: "Other", Slug: "payments"}); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("slug taken = %v", err)
	}
	if f := fieldError(t, func() error {
		_, err := svc.Create(ctx, by, Input{Name: "Other", Slug: "other", OwnWorkingHours: true})
		return err
	}()); f.Pointer != "/working_hours" || f.Code != CodeRequired {
		t.Errorf("own hours without them = %+v", f)
	}
	if f := fieldError(t, func() error { _, err := svc.Create(ctx, by, Input{Name: "x", Slug: "X"}); return err }()); f.Pointer != "/slug" {
		t.Errorf("slug = %+v", f)
	}
	for _, name := range []string{"InsertTeam", "GetTeam", "GetOrganizationWorkingHours", "InsertAuditEntry",
		"Notify"} {
		store.fail[name] = errBoom
		if _, err := svc.Create(ctx, by, Input{Name: "z" + name, Slug: strings.ToLower(name)}); !errors.Is(err,
			errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
}

// TestGetAndList: a Team is read with the number of people in it and its effective Working hours, which follow a
// change of the Organization's; an unknown or deleted Team is ErrNotFound; the list goes by name with its cursor and q.
func TestGetAndList(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	var ids []string
	for _, n := range []string{"Search", "payments", "Checkout"} {
		team, err := svc.Create(ctx, by, Input{Name: n, Slug: strings.ToLower(n)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, team.PublicID)
	}
	store.org.WorkdayStart = "08:00"
	got, err := svc.Get(ctx, strings.ToLower(ids[0]))
	if err != nil || got.Name != "Search" || got.WorkingHours.Start != "08:00" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if hours, err := svc.OrganizationHours(ctx); err != nil || hours.Start != "08:00" || hours.End != "18:00" {
		t.Errorf("organization hours = %+v, %v", hours, err)
	}
	for _, id := range []string{"TM0000000000ZZ", "RT0000000000ZZ", "x"} {
		if _, err := svc.Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s = %v", id, err)
		}
	}
	page, err := svc.List(ctx, ListFilter{Limit: 2})
	if err != nil || len(page.Teams) != 2 || page.Teams[0].Name != "Checkout" || page.Teams[1].Name != "payments" ||
		page.Next == nil || *page.Next != (Position{Name: "payments", ID: 2}) {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, err = svc.List(ctx, ListFilter{Limit: 2, After: page.Next})
	if err != nil || len(page.Teams) != 1 || page.Teams[0].Name != "Search" || page.Next != nil {
		t.Errorf("second page = %+v, %v", page, err)
	}
	page, err = svc.List(ctx, ListFilter{Query: " PAY ", Limit: 0})
	if err != nil || len(page.Teams) != 1 || store.listed[len(store.listed)-1].Q.String != "PAY" ||
		store.listed[len(store.listed)-1].PageSize != 2 {
		t.Errorf("q = %+v, %v", page, err)
	}
	for _, name := range []string{"ListTeams", "GetOrganizationWorkingHours"} {
		store.fail[name] = errBoom
		if _, err := svc.List(ctx, ListFilter{}); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	store.fail["GetTeam"] = errBoom
	if _, err := svc.Get(ctx, ids[0]); !errors.Is(err, errBoom) {
		t.Errorf("GetTeam fails = %v", err)
	}
}

// TestUpdate is C-25.FR-9 and FR-11: If-Match, a change recorded as team.updated with its diff, an update that changes
// nothing writing nothing, own Working hours kept when omitted and required when there are none, a return to the
// Organization's, a rename that moves muster_team_info, and the conflicts.
func TestUpdate(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	team, err := svc.Create(ctx, by, Input{Name: "Payments", Slug: "payments", Description: ptr("Pays")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, by, Input{Name: "Search", Slug: "search"}); err != nil {
		t.Fatal(err)
	}
	audits := len(store.audit)
	if _, err := svc.Update(ctx, by, team.PublicID, ptr(int64(9)), Input{Name: "Payments", Slug: "payments"}); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	same, err := svc.Update(ctx, by, team.PublicID, ptr(int64(1)), Input{Name: "Payments", Slug: "payments"})
	if err != nil || same.Version != 1 || len(store.audit) != audits {
		t.Errorf("no change = %+v, %v", same, err)
	}
	if f := fieldError(t, func() error {
		_, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Payments", Slug: "payments",
			OwnWorkingHours: true})
		return err
	}()); f.Pointer != "/working_hours" || f.Code != CodeRequired {
		t.Errorf("own hours without them = %+v", f)
	}
	own, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Payments", Slug: "payments",
		OwnWorkingHours: true, WorkingHours: &berlin})
	if err != nil || own.Own == nil || own.WorkingHours.Start != "11:00" || own.Version != 2 ||
		own.Description != "Pays" {
		t.Fatalf("own hours = %+v, %v", own, err)
	}
	if e := store.audit[len(store.audit)-1]; e.Action != ActionUpdated || string(e.Diff) !=
		`[{"pointer":"/own_working_hours","before":false,"after":true},{"pointer":"/working_hours/days",`+
			`"before":null,"after":["monday","friday"]},{"pointer":"/working_hours/start","before":"","after":"11:00"},`+
			`{"pointer":"/working_hours/end","before":"","after":"19:00"},{"pointer":"/working_hours/time_zone",`+
			`"before":"","after":"Europe/Berlin"}]` {
		t.Errorf("diff %s", e.Diff)
	}
	kept, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Payments", Slug: "payments",
		OwnWorkingHours: true})
	if err != nil || kept.Own == nil || kept.Version != 2 {
		t.Errorf("own hours kept = %+v, %v", kept, err)
	}
	back, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Pay", Slug: "pay", Description: ptr(""),
		WorkingHours: &berlin})
	if err != nil || back.Own != nil || back.WorkingHours.TimeZone != "UTC" || back.Description != "" ||
		back.Version != 3 {
		t.Errorf("back to the Organization's = %+v, %v", back, err)
	}
	if got := infoSeries(t, team.PublicID); got != `muster_team_info{team="`+team.PublicID+`",name="Pay"} 1` {
		t.Errorf("info after the rename %q", got)
	}
	if _, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "search", Slug: "pay"}); !errors.Is(err,
		ErrNameTaken) {
		t.Errorf("name taken = %v", err)
	}
	if _, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Pay", Slug: "search"}); !errors.Is(err,
		ErrSlugTaken) {
		t.Errorf("slug taken = %v", err)
	}
	if _, err := svc.Update(ctx, by, "TM0000000000ZZ", nil, Input{Name: "x", Slug: "x"}); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	if _, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "", Slug: "x"}); err == nil {
		t.Error("an empty name was saved")
	}
	for _, name := range []string{"LockTeam", "UpdateTeam", "InsertAuditEntry"} {
		store.fail[name] = errBoom
		if _, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Pay" + name, Slug: "pay"}); !errors.Is(err,
			errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	store.fail["GetTeam"] = errBoom
	if _, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "x", Slug: "x"}); !errors.Is(err, errBoom) {
		t.Errorf("GetTeam fails = %v", err)
	}
}

// TestDelete is C-25.FR-10 and AC-9: the Team is marked deleted with its people removed, the Destinations and then the
// Routes it owned have no owner and a new version, each with destination.updated or route.updated by the system and
// its hint, team.deleted is recorded, muster_team_info loses its series, and the slug is free at once.
func TestDelete(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	team, err := svc.Create(ctx, by, Input{Name: "Payments", Slug: "payments"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Create(ctx, by, Input{Name: "Search", Slug: "search"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AddUser(ctx, by, team.PublicID, "SR0000000000BB"); err != nil {
		t.Fatal(err)
	}
	store.dests = []*owned{{publicID: "DS0000000000AA", name: "alerts", team: 1, version: 3},
		{publicID: "DS0000000000BB", name: "other", team: 2, version: 1},
		{publicID: "DS0000000000CC", name: "gone", team: 1, version: 4, deleted: true}}
	store.routes = []*owned{{publicID: "RT0000000000AA", name: "pay", team: 1, version: 5}}
	if err := svc.Delete(ctx, by, team.PublicID, ptr(int64(7))); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	audits, hints := len(store.audit), len(store.hints)
	if err := svc.Delete(ctx, by, team.PublicID, ptr(int64(1))); err != nil {
		t.Fatal(err)
	}
	if store.dests[0].team != 0 || store.dests[0].version != 4 || store.dests[1].team != 2 ||
		store.dests[2].team != 1 || store.routes[0].team != 0 || store.routes[0].version != 6 {
		t.Errorf("owners %+v %+v %+v %+v", *store.dests[0], *store.dests[1], *store.dests[2], *store.routes[0])
	}
	entries := store.audit[audits:]
	if len(entries) != 3 || entries[0].Action != "destination.updated" || entries[0].ActorKind != "system" ||
		entries[0].Transport != "system" || entries[0].ResourcePublicID.String != "DS0000000000AA" ||
		string(entries[0].Diff) != `[{"pointer":"/owning_team_id","before":"`+team.PublicID+`"}]` ||
		entries[1].Action != "route.updated" || entries[1].ResourcePublicID.String != "RT0000000000AA" ||
		entries[2].Action != ActionDeleted || !strings.Contains(string(entries[2].Details), `"users_removed":1`) {
		t.Errorf("audit %+v", entries)
	}
	if got := store.hints[hints:]; len(got) != 3 || got[0].Type != "destination" || got[1].Type != "route" ||
		got[2] != (db.Hint{OrgID: orgID, Type: Hint, ID: team.PublicID}) {
		t.Errorf("hints %+v", got)
	}
	if len(store.people[1]) != 0 || !store.teams[0].deleted {
		t.Errorf("after the deletion %+v %+v", store.people, store.teams[0])
	}
	if _, err := svc.Get(ctx, team.PublicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted team reads %v", err)
	}
	if got := infoSeries(t, team.PublicID); got != "" {
		t.Errorf("info of a deleted team %q", got)
	}
	again, err := svc.Create(ctx, by, Input{Name: "Payments", Slug: "payments"})
	if err != nil || again.PublicID == team.PublicID {
		t.Errorf("the slug is free = %+v, %v", again, err)
	}
	if err := svc.Delete(ctx, by, team.PublicID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted again = %v", err)
	}
	for _, name := range []string{"ReleaseOwnedDestinations", "ReleaseOwnedRoutes", "DeleteTeamUsers", "DeleteTeam",
		"InsertAuditEntry", "Notify"} {
		store.dests = []*owned{{publicID: "DS0000000000AA", name: "alerts", team: 2}}
		store.routes = []*owned{{publicID: "RT0000000000AA", name: "pay", team: 2}}
		store.fail[name] = errBoom
		if err := svc.Delete(ctx, by, other.PublicID, nil); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	store.routes = []*owned{{publicID: "RT0000000000AA", name: "pay", team: 2}}
	store.dests = nil
	store.fail["Notify"] = errBoom
	if err := svc.Delete(ctx, by, other.PublicID, nil); !errors.Is(err, errBoom) {
		t.Errorf("a route's hint fails = %v", err)
	}
}

// TestPeople is C-25.FR-3 and AC-12: an Admin adds Users by hand — a disabled one too — each recorded as
// team.user_added, a User already in the Team is not added again, a User may be in several Teams, the people read with
// their Role, source and linked messengers, a removal is recorded as team.user_removed, and a deleted User leaves
// every Team.
func TestPeople(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	pay, _ := svc.Create(ctx, by, Input{Name: "Payments", Slug: "payments"})
	search, _ := svc.Create(ctx, by, Input{Name: "Search", Slug: "search"})
	store.links = []dbgen.ListLinkedMessengersRow{{UserID: 2, Type: "mattermost", PublicID: "CN0000000000AA",
		Name: "chat"}}
	audits := len(store.audit)
	m, added, err := svc.AddUser(ctx, by, pay.PublicID, "sr0000000000bb")
	if err != nil || !added || m.Name != "bob" || m.Role != "responder" || !slices.Equal(m.Sources,
		[]string{SourceByHand}) || len(m.Linked) != 1 || m.Linked[0].ConnectionName != "chat" {
		t.Fatalf("add = %+v, %v, %v", m, added, err)
	}
	if e := store.audit[audits]; e.Action != ActionUserAdded || e.ResourcePublicID.String != pay.PublicID ||
		string(e.Diff) != `[{"pointer":"/users/SR0000000000BB","after":{"sources":["by_hand"]}}]` {
		t.Errorf("audit %+v %s", e, e.Diff)
	}
	if _, added, err := svc.AddUser(ctx, by, pay.PublicID, "SR0000000000BB"); err != nil || added ||
		len(store.audit) != audits+1 {
		t.Errorf("again = %v, %v", added, err)
	}
	if m, _, err := svc.AddUser(ctx, by, pay.PublicID, "SR0000000000VV"); err != nil || m.Name != "vic" {
		t.Errorf("a disabled user = %+v, %v", m, err)
	}
	if _, _, err := svc.AddUser(ctx, by, search.PublicID, "SR0000000000BB"); err != nil {
		t.Fatal(err)
	}
	people, err := svc.Users(ctx, pay.PublicID)
	if err != nil || len(people) != 2 || people[0].Name != "bob" || people[1].Name != "vic" ||
		len(people[1].Linked) != 0 {
		t.Errorf("people = %+v, %v", people, err)
	}
	if got, _ := svc.Get(ctx, pay.PublicID); got.UserCount != 2 {
		t.Errorf("user count %d", got.UserCount)
	}
	for _, id := range []string{"SR0000000000ZZ", "TM0000000000AA"} {
		_, _, err := svc.AddUser(ctx, by, pay.PublicID, id)
		if f := fieldError(t, err); f.Pointer != "/user_id" || f.Code != CodeUnknownID {
			t.Errorf("%s: %+v", id, f)
		}
	}
	if _, _, err := svc.AddUser(ctx, by, "TM0000000000ZZ", "SR0000000000BB"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown team = %v", err)
	}
	if _, err := svc.Users(ctx, "TM0000000000ZZ"); !errors.Is(err, ErrNotFound) {
		t.Errorf("people of an unknown team = %v", err)
	}

	if err := svc.RemoveUser(ctx, by, pay.PublicID, "SR0000000000VV"); err != nil {
		t.Fatal(err)
	}
	if e := store.audit[len(store.audit)-1]; e.Action != ActionUserRemoved ||
		string(e.Diff) != `[{"pointer":"/users/SR0000000000VV","before":{"sources":["by_hand"]}}]` {
		t.Errorf("removed %+v %s", e, e.Diff)
	}
	for _, id := range []string{"SR0000000000VV", "x"} {
		if err := svc.RemoveUser(ctx, by, pay.PublicID, id); !errors.Is(err, ErrNotInTeam) {
			t.Errorf("%s not in the team = %v", id, err)
		}
	}
	if err := svc.RemoveUser(ctx, by, "TM0000000000ZZ", "SR0000000000BB"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown team = %v", err)
	}

	audits, hints := len(store.audit), len(store.hints)
	after, err := svc.RemoveUserTeams(ctx, nil, 2, audit.User(1, "SRAAAAAAAAAAAA"), audit.TransportUI, by.Address)
	if err != nil || after != nil || len(store.people[1])+len(store.people[2]) != 0 {
		t.Fatalf("a deleted user = %v, %+v", err, store.people)
	}
	if got := store.audit[audits:]; len(got) != 2 || got[0].Action != ActionUserRemoved ||
		string(got[0].Details) != `{"reason":"user_deleted","user":"SR0000000000BB",`+
			`"user_name":"deleted-user-SR0000000000BB"}` ||
		string(got[0].Diff) != `[{"pointer":"/users/SR0000000000BB","before":{"sources":["by_hand"]}}]` ||
		len(store.hints[hints:]) != 2 {
		t.Errorf("audit %+v", got)
	}

	// A person in the Team other than by hand — later, through the identity provider — is added by hand too.
	store.people[1] = []dbgen.GetTeamUserRow{{ID: 3, PublicID: "SR0000000000VV", Name: "vic", ByHand: false}}
	if m, added, err := svc.AddUser(ctx, by, pay.PublicID, "SR0000000000VV"); err != nil || !added ||
		!slices.Equal(m.Sources, []string{SourceByHand}) {
		t.Errorf("by hand too = %+v, %v, %v", m, added, err)
	}

	for _, name := range []string{"LockTeamUser", "InsertTeamUser", "GetTeamUser", "ListLinkedMessengers",
		"InsertAuditEntry", "Notify"} {
		store.people[1] = nil
		store.fail[name] = errBoom
		if _, _, err := svc.AddUser(ctx, by, pay.PublicID, "SR0000000000BB"); !errors.Is(err, errBoom) {
			t.Errorf("add: %s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	bob := dbgen.GetTeamUserRow{ID: 2, PublicID: "SR0000000000BB", Name: "bob", ByHand: true}
	for _, name := range []string{"GetTeamUser", "DeleteTeamUser", "InsertAuditEntry", "Notify"} {
		store.people[1] = []dbgen.GetTeamUserRow{bob}
		store.fail[name] = errBoom
		if err := svc.RemoveUser(ctx, by, pay.PublicID, "SR0000000000BB"); !errors.Is(err, errBoom) {
			t.Errorf("remove: %s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	for _, name := range []string{"ListTeamUsers", "ListLinkedMessengers"} {
		store.people[1] = []dbgen.GetTeamUserRow{bob}
		store.fail[name] = errBoom
		if _, err := svc.Users(ctx, pay.PublicID); !errors.Is(err, errBoom) {
			t.Errorf("people: %s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	for _, name := range []string{"DeleteUserTeams", "InsertAuditEntry", "Notify"} {
		store.people[1] = []dbgen.GetTeamUserRow{{ID: 2, PublicID: "SR0000000000BB"}}
		store.fail[name] = errBoom
		if _, err := svc.RemoveUserTeams(ctx, nil, 2, by.Actor, by.Transport, by.Address); !errors.Is(err, errBoom) {
			t.Errorf("a deleted user: %s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
}

// TestInfo is C-25.FR-11: muster_team_info lists exactly the Teams that are not deleted, whichever replica changed
// them, and RunInfo refreshes it until its context ends.
func TestInfo(t *testing.T) {
	svc, store := newService(t)
	store.nextID = 100
	store.teams = append(store.teams, &teamRec{GetTeamRow: dbgen.GetTeamRow{ID: 1, PublicID: "TM0000000000R1",
		Name: "Elsewhere"}})
	svc.info["TM0000000000R2"] = "Gone"
	metrics.TeamInfo.With("TM0000000000R2", "Gone").Set(1)
	if err := svc.RefreshInfo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if infoSeries(t, "TM0000000000R1") == "" || infoSeries(t, "TM0000000000R2") != "" {
		t.Errorf("info %q %q", infoSeries(t, "TM0000000000R1"), infoSeries(t, "TM0000000000R2"))
	}
	store.fail["ListTeamInfo"] = errBoom
	if err := svc.RefreshInfo(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("failed refresh = %v", err)
	}
	delete(store.fail, "ListTeamInfo")
	store.teams[0].deleted = true
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		svc.RunInfo(ctx, ticks)
		close(done)
	}()
	svc.InfoChanged()
	svc.InfoChanged()
	ticks <- t0
	cancel()
	<-done
	if got := infoSeries(t, "TM0000000000R1"); got != "" {
		t.Errorf("a deleted team still has %q", got)
	}
}

func (s *fakeStore) ListTeamMatchers(_ context.Context, arg dbgen.ListTeamMatchersParams) (
	[]dbgen.ListTeamMatchersRow, error) {
	if err := s.fail["ListTeamMatchers"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListTeamMatchersRow
	for _, id := range slices.Sorted(slices.Values(arg.TeamIds)) {
		out = append(out, s.matchers[id]...)
	}
	return out, nil
}

func (s *fakeStore) DeleteTeamMatchers(_ context.Context, arg dbgen.DeleteTeamMatchersParams) error {
	if err := s.fail["DeleteTeamMatchers"]; err != nil {
		return err
	}
	delete(s.matchers, arg.TeamID)
	return nil
}

func (s *fakeStore) InsertTeamMatchers(_ context.Context, arg dbgen.InsertTeamMatchersParams) error {
	if err := s.fail["InsertTeamMatchers"]; err != nil {
		return err
	}
	if s.matchers == nil {
		s.matchers = map[int64][]dbgen.ListTeamMatchersRow{}
	}
	for i := range arg.Labels {
		s.matchers[arg.TeamID] = append(s.matchers[arg.TeamID], dbgen.ListTeamMatchersRow{TeamID: arg.TeamID,
			Label: arg.Labels[i], Op: arg.Ops[i], Value: arg.MatcherValues[i]})
	}
	return nil
}

func (s *fakeStore) LockTeamsVersion(context.Context, int64) (int64, error) {
	s.locked++
	return s.version, s.fail["LockTeamsVersion"]
}

func (s *fakeStore) BumpTeamsVersion(context.Context, int64) error {
	if err := s.fail["BumpTeamsVersion"]; err != nil {
		return err
	}
	s.version++
	return nil
}

// TestMatchers is C-25.FR-1, FR-5 and FR-11: a Team takes Team matchers in the syntax of Route Matchers, read back in
// their order, replaced as a whole and kept when an update omits them, each change recorded in the diff of
// team.updated; an invalid regular expression is refused with invalid_regex at its pointer, an empty label or an
// unknown operator with invalid_format; every change of a Team — a creation, an update, a deletion — raises
// organizations.teams_version after locking the Organization's row, and an update that changes nothing raises nothing.
func TestMatchers(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	ms := []Matcher{{Label: "namespace", Op: "=~", Value: "checkout|billing"}, {Label: "pod", Op: "=~",
		Value: "checkout-.*"}}
	team, err := svc.Create(ctx, by, Input{Name: "Checkout", Slug: "checkout-team", Matchers: &ms})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(team.Matchers, ms) || store.version != 1 || store.locked != 1 {
		t.Fatalf("created %+v, version %d, locked %d", team.Matchers, store.version, store.locked)
	}
	if !strings.Contains(string(store.audit[0].Diff), `"pointer":"/matchers","after":[{"label":"namespace","op":"=~",`+
		`"value":"checkout|billing"},{"label":"pod","op":"=~","value":"checkout-.*"}]`) {
		t.Errorf("created diff %s", store.audit[0].Diff)
	}
	kept, err := svc.Update(ctx, by, team.PublicID, ptr(int64(1)), Input{Name: "Checkout", Slug: "checkout-team"})
	if err != nil || !slices.Equal(kept.Matchers, ms) || kept.Version != 1 || store.version != 1 ||
		len(store.audit) != 1 {
		t.Errorf("kept = %+v, %v, version %d", kept, err, store.version)
	}
	one := ms[:1]
	changed, err := svc.Update(ctx, by, team.PublicID, ptr(int64(1)), Input{Name: "Checkout", Slug: "checkout-team",
		Matchers: &one})
	if err != nil || !slices.Equal(changed.Matchers, one) || changed.Version != 2 || store.version != 2 {
		t.Fatalf("changed = %+v, %v, version %d", changed, err, store.version)
	}
	if d := string(store.audit[1].Diff); store.audit[1].Action != ActionUpdated || d !=
		`[{"pointer":"/matchers","before":[{"label":"namespace","op":"=~","value":"checkout|billing"},`+
			`{"label":"pod","op":"=~","value":"checkout-.*"}],"after":[{"label":"namespace","op":"=~",`+
			`"value":"checkout|billing"}]}]` {
		t.Errorf("updated diff %s", d)
	}
	if got, err := svc.Get(ctx, team.PublicID); err != nil || !slices.Equal(got.Matchers, one) {
		t.Errorf("get = %+v, %v", got.Matchers, err)
	}
	page, err := svc.List(ctx, ListFilter{Limit: 10})
	if err != nil || len(page.Teams) != 1 || !slices.Equal(page.Teams[0].Matchers, one) {
		t.Errorf("list = %+v, %v", page.Teams, err)
	}
	plain, err := svc.Create(ctx, by, Input{Name: "Search", Slug: "search"})
	if err != nil || plain.Matchers == nil || len(plain.Matchers) != 0 || store.version != 3 {
		t.Errorf("without matchers = %+v, %v", plain.Matchers, err)
	}
	for i, c := range []struct {
		m             Matcher
		pointer, code string
	}{
		{Matcher{Label: "pod", Op: "=~", Value: "checkout-("}, "/matchers/1/value", CodeInvalidRegex},
		{Matcher{Label: "", Op: "=", Value: "x"}, "/matchers/1/label", CodeInvalidFormat},
		{Matcher{Label: "pod", Op: "~", Value: "x"}, "/matchers/1/op", CodeInvalidFormat},
	} {
		bad := []Matcher{ms[0], c.m}
		f := fieldError(t, func() error {
			_, err := svc.Update(ctx, by, team.PublicID, nil, Input{Name: "Checkout", Slug: "checkout-team",
				Matchers: &bad})
			return err
		}())
		if f.Pointer != c.pointer || f.Code != c.code {
			t.Errorf("%d: %+v", i, f)
		}
	}
	if err := svc.Delete(ctx, by, plain.PublicID, nil); err != nil || store.version != 4 {
		t.Errorf("delete = %v, version %d", err, store.version)
	}
	if err := svc.Delete(ctx, by, team.PublicID, nil); err != nil || store.matchers[team.ID] != nil {
		t.Errorf("delete = %v, matchers %v", err, store.matchers[team.ID])
	}
	for _, name := range []string{"LockTeamsVersion", "BumpTeamsVersion", "InsertTeamMatchers", "DeleteTeamMatchers",
		"ListTeamMatchers"} {
		store.fail[name] = errBoom
		if _, err := svc.Create(ctx, by, Input{Name: "z" + name, Slug: strings.ToLower(name),
			Matchers: &ms}); !errors.Is(err, errBoom) {
			t.Errorf("create, %s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	other, err := svc.Create(ctx, by, Input{Name: "Other", Slug: "other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LockTeamsVersion", "BumpTeamsVersion", "DeleteTeamMatchers"} {
		store.fail[name] = errBoom
		if _, err := svc.Update(ctx, by, other.PublicID, nil, Input{Name: "Other", Slug: "other",
			Matchers: &ms}); !errors.Is(err, errBoom) {
			t.Errorf("update, %s fails = %v", name, err)
		}
		if err := svc.Delete(ctx, by, other.PublicID, nil); !errors.Is(err, errBoom) {
			t.Errorf("delete, %s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	store.fail["ListTeamMatchers"] = errBoom
	if _, err := svc.List(ctx, ListFilter{Limit: 10}); !errors.Is(err, errBoom) {
		t.Errorf("list, ListTeamMatchers fails = %v", err)
	}
}

// TestIdpEntries is C-25.FR-1 and FR-11: the entries are part of the Team, validated at their pointers, saved with
// If-Match, recorded in the diff of team.updated and replaced as a whole; omitted on an update they are kept.
func TestIdpEntries(t *testing.T) {
	svc, s := newService(t)
	for _, tc := range []struct {
		entries []IdpEntry
		pointer string
		code    string
	}{
		{[]IdpEntry{{Group: " "}}, "/idp_entries/0/group", CodeInvalidFormat},
		{[]IdpEntry{{Group: "a"}, {Group: "teams/*/x"}}, "/idp_entries/1/group", CodeInvalidFormat},
		{[]IdpEntry{{Group: "*"}}, "/idp_entries/0/group", CodeInvalidFormat},
		{[]IdpEntry{{Group: strings.Repeat("g", 201)}}, "/idp_entries/0/group", CodeTooLong},
		{[]IdpEntry{{Claim: strings.Repeat("c", 201), Group: "g"}}, "/idp_entries/0/claim", CodeTooLong},
		{[]IdpEntry{{Group: "a"}, {Group: " a "}}, "/idp_entries/1", CodeDuplicate},
		{make([]IdpEntry, MaxIdpEntries+1), "/idp_entries", CodeTooLong},
	} {
		_, err := svc.Create(t.Context(), by, Input{Name: "X", Slug: "x", IdpEntries: &tc.entries})
		if fe := fieldError(t, err); fe.Pointer != tc.pointer || fe.Code != tc.code {
			t.Errorf("%v: %+v", tc.entries, fe)
		}
	}
	team := idpTeamOf(t, svc, "Payments", IdpEntry{Claim: " " + maintainer + " ", Group: " teams/payments/* "})
	if !slices.Equal(team.IdpEntries, []IdpEntry{{Claim: maintainer, Group: "teams/payments/*"}}) {
		t.Fatalf("created entries = %+v", team.IdpEntries)
	}
	got, err := svc.Get(t.Context(), team.PublicID)
	if err != nil || !slices.Equal(got.IdpEntries, team.IdpEntries) {
		t.Errorf("read entries = %+v, %v", got.IdpEntries, err)
	}
	page, err := svc.List(t.Context(), ListFilter{Limit: 10})
	if err != nil || len(page.Teams) != 1 || !slices.Equal(page.Teams[0].IdpEntries, team.IdpEntries) {
		t.Errorf("listed entries = %+v, %v", page, err)
	}
	stale := team.Version + 5
	if _, err := svc.Update(t.Context(), by, team.PublicID, &stale, Input{Name: team.Name, Slug: team.Slug,
		IdpEntries: &[]IdpEntry{}}); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("a stale If-Match = %v", err)
	}
	updated, err := svc.Update(t.Context(), by, team.PublicID, &team.Version, Input{Name: team.Name,
		Slug: team.Slug, IdpEntries: &[]IdpEntry{{Group: "teams/payments"}, {Group: "teams/billing/*"}}})
	if err != nil || updated.Version != team.Version+1 || len(updated.IdpEntries) != 2 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	last := s.audit[len(s.audit)-1]
	if last.Action != ActionUpdated || !strings.Contains(string(last.Diff), `"pointer":"/idp_entries"`) ||
		!strings.Contains(string(last.Diff), `"teams/billing/*"`) {
		t.Errorf("team.updated = %s %s", last.Action, last.Diff)
	}
	kept, err := svc.Update(t.Context(), by, team.PublicID, nil, Input{Name: "Payments 2", Slug: team.Slug})
	if err != nil || len(kept.IdpEntries) != 2 {
		t.Errorf("an update without entries = %+v, %v", kept.IdpEntries, err)
	}
	for _, q := range []string{"InsertTeamIdpEntries", "DeleteTeamIdpEntries", "ListTeamIdpEntries"} {
		s.fail[q] = errBoom
		if _, err := svc.Update(t.Context(), by, team.PublicID, nil, Input{Name: "P", Slug: team.Slug,
			IdpEntries: &[]IdpEntry{{Group: q}}}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
		delete(s.fail, q)
	}
	s.fail["ListTeamIdpEntries"] = errBoom
	if _, err := svc.List(t.Context(), ListFilter{Limit: 10}); !errors.Is(err, errBoom) {
		t.Errorf("list = %v", err)
	}
}

// TestRemoveByHandKeepsIdp is C-25.FR-4: removeTeamUser takes away only by_hand, so a person also in the Team through
// the identity provider stays with idp, and team.user_removed records the sources before and after; a person in it only
// through the identity provider is ErrNotInTeam. Adding such a person by hand gives them both sources.
func TestRemoveByHandKeepsIdp(t *testing.T) {
	svc, s := newService(t)
	team := idpTeamOf(t, svc, "Payments", IdpEntry{Group: "a"})
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", oidc.Claims{"groups": {"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveUser(t.Context(), by, team.PublicID, "SR0000000000BB"); !errors.Is(err, ErrNotInTeam) {
		t.Errorf("removing by hand a person in it through the identity provider = %v", err)
	}
	p, added, err := svc.AddUser(t.Context(), by, team.PublicID, "SR0000000000BB")
	if err != nil || !added || !slices.Equal(p.Sources, []string{SourceByHand, SourceIdp}) {
		t.Fatalf("adding by hand = %+v %v %v", p, added, err)
	}
	last := s.audit[len(s.audit)-1]
	if last.Action != ActionUserAdded || string(last.Diff) !=
		`[{"pointer":"/users/SR0000000000BB","before":{"sources":["idp"]},"after":{"sources":["by_hand","idp"]}}]` {
		t.Errorf("team.user_added = %s", last.Diff)
	}
	if err := svc.RemoveUser(t.Context(), by, team.PublicID, "SR0000000000BB"); err != nil {
		t.Fatal(err)
	}
	if got, _ := sources(s, team.ID, 2); !slices.Equal(got, []string{SourceIdp}) {
		t.Errorf("after removing by hand = %v", got)
	}
	last = s.audit[len(s.audit)-1]
	if last.Action != ActionUserRemoved || string(last.Diff) !=
		`[{"pointer":"/users/SR0000000000BB","before":{"sources":["by_hand","idp"]},"after":{"sources":["idp"]}}]` {
		t.Errorf("team.user_removed = %s", last.Diff)
	}
	if _, _, err := svc.AddUser(t.Context(), by, team.PublicID, "SR0000000000BB"); err != nil {
		t.Fatal(err)
	}
	// A row that changed although it was locked is no removal: nothing is recorded.
	entries := len(s.audit)
	s.lockedThen = func() { s.member(team.ID, 2).ByIdp = false }
	if err := svc.RemoveUser(t.Context(), by, team.PublicID, "SR0000000000BB"); err == nil ||
		len(s.audit) != entries {
		t.Errorf("a row that changed under the lock = %v, %d entries", err, len(s.audit)-entries)
	}
	s.lockedThen = nil
	s.member(team.ID, 2).ByIdp = true
	for _, q := range []string{"LockTeamMember", "ClearByHand"} {
		s.fail[q] = errBoom
		if err := svc.RemoveUser(t.Context(), by, team.PublicID, "SR0000000000BB"); !errors.Is(err, errBoom) {
			t.Errorf("a failing %s = %v", q, err)
		}
		delete(s.fail, q)
	}
}
