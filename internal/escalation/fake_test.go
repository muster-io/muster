// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"cmp"
	"context"
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
	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/logging"
)

const orgID = 7

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

var by = Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportAPI}

// The objects of the fake Organization.
const (
	teamPay      = "TM0000000000PA"
	teamDev      = "TM0000000000DV"
	teamGone     = "TM0000000000GN"
	schedMain    = "SC0000000000MA"
	schedSecond  = "SC0000000000S2"
	userPetr     = "SR0000000000PE"
	userIvan     = "SR0000000000AV"
	userVic      = "SR0000000000VC"
	userDora     = "SR0000000000DR"
	userGone     = "SR0000000000GN"
	gatewayOne   = "CN0000000000G1"
	gatewayTwo   = "CN0000000000G2"
	destHook     = "DS0000000000WH"
	destTemplate = "DS0000000000WT"
	destMM       = "DS0000000000MM"
	destTG       = "DS0000000000TG"
	destGone     = "DS0000000000GN"
)

type fakeTeam struct {
	id                   int64
	publicID, name, slug string
	deleted              bool
	own                  *dbgen.GetTeamWorkingHoursRow
}

type fakeSchedule struct {
	id             int64
	publicID, name string
	team           int64
}

type fakeStep struct {
	id    int64
	p     dbgen.InsertStepParams
	users []int64
}

type fakeRouteRef struct {
	chain, route   int64
	publicID, name string
	condition      string
}

type fakeTeamRef struct {
	chain, team int64
	condition   string
}

// fakeStore is the database of the package in memory; a transaction that fails leaves the chains and their steps as
// they were.
type fakeStore struct {
	teams        []fakeTeam
	schedules    []fakeSchedule
	users        []dbgen.ResolveUsersRow
	gateways     []dbgen.ListGatewaysRow
	destinations []dbgen.ResolveDestinationsRow
	userTeams    map[int64][]int64
	org          dbgen.GetOrganizationWorkingHoursRow

	chains     []dbgen.GetChainRow
	steps      []fakeStep
	routeRefs  []fakeRouteRef
	teamRefs   []fakeTeamRef
	chainDests map[int64][]dbgen.ListChainDestinationsRow

	audit  []auditdb.InsertAuditEntryParams
	hints  []db.Hint
	fail   map[string]error
	nextID int64
	// locks are the row locks the transactions took, in order, as "kind:public_id".
	locks []string
}

func newStore() *fakeStore {
	s := &fakeStore{fail: map[string]error{}, nextID: 100, userTeams: map[int64][]int64{},
		chainDests: map[int64][]dbgen.ListChainDestinationsRow{},
		org: dbgen.GetOrganizationWorkingHoursRow{WorkingDays: []string{"monday", "tuesday", "wednesday", "thursday",
			"friday"}, WorkdayStart: "09:00", WorkdayEnd: "18:00", TimeZone: "UTC"}}
	s.teams = []fakeTeam{{id: 1, publicID: teamPay, name: "payments", slug: "payments",
		own: &dbgen.GetTeamWorkingHoursRow{OwnWorkingHours: true, WorkingDays: []string{"monday"},
			WorkingStart: pgtype.Text{String: "11:00", Valid: true}, WorkingEnd: pgtype.Text{String: "19:00", Valid: true},
			WorkingTimeZone: pgtype.Text{String: "Europe/Berlin", Valid: true}}},
		{id: 2, publicID: teamDev, name: "devops", slug: "devops"},
		{id: 3, publicID: teamGone, name: "gone", slug: "gone", deleted: true}}
	s.schedules = []fakeSchedule{{id: 11, publicID: schedMain, name: "Main", team: 1},
		{id: 12, publicID: schedSecond, name: "Second line", team: 1}}
	s.users = []dbgen.ResolveUsersRow{
		{ID: 21, PublicID: userPetr, Name: "Petr", Login: "petr", Role: "responder", Status: "active"},
		{ID: 22, PublicID: userIvan, Name: "Ivan", Login: "ivan", Role: "admin", Status: "active"},
		{ID: 23, PublicID: userVic, Name: "Vic", Login: "vic", Role: "viewer", Status: "active"},
		{ID: 24, PublicID: userDora, Name: "Dora", Login: "dora", Role: "responder", Status: "disabled"},
		{ID: 25, PublicID: userGone, Name: "deleted-user-25", Login: "deleted-user-25", Role: "responder",
			Status: "deleted"}}
	s.destinations = []dbgen.ResolveDestinationsRow{
		{ID: 41, PublicID: destHook, Name: "hook", Type: "webhook", WebhookMode: pgtype.Text{String: "events", Valid: true}},
		{ID: 42, PublicID: destTemplate, Name: "template", Type: "webhook",
			WebhookMode: pgtype.Text{String: "template", Valid: true}},
		{ID: 43, PublicID: destMM, Name: "payments-critical", Type: "mattermost"},
		{ID: 44, PublicID: destTG, Name: "payments-tg", Type: "telegram"},
		{ID: 45, PublicID: destGone, Name: "gone", Type: "webhook", WebhookMode: pgtype.Text{String: "both", Valid: true},
			Deleted: true}}
	return s
}

func (s *fakeStore) err(name string) error { return s.fail[name] }

func (s *fakeStore) teamByID(id int64) (fakeTeam, bool) {
	for _, t := range s.teams {
		if t.id == id {
			return t, true
		}
	}
	return fakeTeam{}, false
}

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	chains, steps := slices.Clone(s.chains), slices.Clone(s.steps)
	audits, hints := len(s.audit), len(s.hints)
	if err := f(s); err != nil {
		s.chains, s.steps, s.audit, s.hints = chains, steps, s.audit[:audits], s.hints[:hints]
		return err
	}
	return nil
}

func (s *fakeStore) GetOrganizationWorkingHours(context.Context, int64) (dbgen.GetOrganizationWorkingHoursRow, error) {
	return s.org, s.err("GetOrganizationWorkingHours")
}

func (s *fakeStore) GetTeamWorkingHours(_ context.Context, arg dbgen.GetTeamWorkingHoursParams) (
	dbgen.GetTeamWorkingHoursRow, error) {
	if err := s.err("GetTeamWorkingHours"); err != nil {
		return dbgen.GetTeamWorkingHoursRow{}, err
	}
	t, ok := s.teamByID(arg.ID)
	if !ok {
		return dbgen.GetTeamWorkingHoursRow{}, pgx.ErrNoRows
	}
	if t.own == nil {
		return dbgen.GetTeamWorkingHoursRow{}, nil
	}
	return *t.own, nil
}

// withTeam is a chain row with its owning Team's fields.
func (s *fakeStore) withTeam(c dbgen.GetChainRow) dbgen.GetChainRow {
	c.TeamPublicID, c.TeamName, c.TeamSlug, c.TeamDeleted = pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, false
	if t, ok := s.teamByID(c.OwningTeamID.Int64); ok && c.OwningTeamID.Valid {
		c.TeamPublicID = pgtype.Text{String: t.publicID, Valid: true}
		c.TeamName = pgtype.Text{String: t.name, Valid: true}
		c.TeamSlug = pgtype.Text{String: t.slug, Valid: true}
		c.TeamDeleted = t.deleted
	}
	return c
}

func (s *fakeStore) ListChains(_ context.Context, arg dbgen.ListChainsParams) ([]dbgen.ListChainsRow, error) {
	if err := s.err("ListChains"); err != nil {
		return nil, err
	}
	var out []dbgen.ListChainsRow
	for _, c := range s.chains {
		c = s.withTeam(c)
		name := strings.ToLower(c.Name)
		switch {
		case arg.Q.Valid && !strings.Contains(name, strings.ToLower(arg.Q.String)):
			continue
		case arg.TeamIds != nil && !slices.Contains(arg.TeamIds, c.OwningTeamID.Int64):
			continue
		case arg.AfterID.Valid && cmp.Or(cmp.Compare(name, arg.AfterName.String), cmp.Compare(c.ID, arg.AfterID.Int64)) <= 0:
			continue
		}
		out = append(out, dbgen.ListChainsRow{ID: c.ID, PublicID: c.PublicID, Name: c.Name, Description: c.Description,
			CreatedAt: c.CreatedAt, Version: c.Version, ManagedBy: c.ManagedBy, OwningTeamID: c.OwningTeamID,
			TeamPublicID: c.TeamPublicID, TeamName: c.TeamName, TeamSlug: c.TeamSlug, TeamDeleted: c.TeamDeleted,
			SortName: name})
	}
	slices.SortFunc(out, func(a, b dbgen.ListChainsRow) int {
		return cmp.Or(cmp.Compare(a.SortName, b.SortName), cmp.Compare(a.ID, b.ID))
	})
	return out[:min(len(out), int(arg.PageSize))], nil
}

func (s *fakeStore) GetChain(_ context.Context, arg dbgen.GetChainParams) (dbgen.GetChainRow, error) {
	if err := s.err("GetChain"); err != nil {
		return dbgen.GetChainRow{}, err
	}
	for _, c := range s.chains {
		if c.PublicID == arg.PublicID && arg.OrgID == orgID {
			return s.withTeam(c), nil
		}
	}
	return dbgen.GetChainRow{}, pgx.ErrNoRows
}

func (s *fakeStore) lockChain(name, publicID string) (int64, error) {
	if err := s.err(name); err != nil {
		return 0, err
	}
	for _, c := range s.chains {
		if c.PublicID == publicID {
			s.locks = append(s.locks, "chain:"+publicID)
			return c.ID, nil
		}
	}
	return 0, pgx.ErrNoRows
}

func (s *fakeStore) LockChain(_ context.Context, arg dbgen.LockChainParams) (int64, error) {
	return s.lockChain("LockChain", arg.PublicID)
}

func (s *fakeStore) LockChainForDeletion(_ context.Context, arg dbgen.LockChainForDeletionParams) (int64, error) {
	return s.lockChain("LockChainForDeletion", arg.PublicID)
}

func (s *fakeStore) LockOwningTeam(_ context.Context, arg dbgen.LockOwningTeamParams) (dbgen.LockOwningTeamRow,
	error) {
	if err := s.err("LockOwningTeam"); err != nil {
		return dbgen.LockOwningTeamRow{}, err
	}
	for _, t := range s.teams {
		if t.publicID == arg.PublicID && !t.deleted {
			s.locks = append(s.locks, "team:"+t.publicID)
			return dbgen.LockOwningTeamRow{ID: t.id, PublicID: t.publicID, Name: t.name, Slug: t.slug}, nil
		}
	}
	return dbgen.LockOwningTeamRow{}, pgx.ErrNoRows
}

func (s *fakeStore) nameTaken(name string, except int64) error {
	for _, c := range s.chains {
		if c.ID != except && strings.EqualFold(c.Name, name) {
			return &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameKey}
		}
	}
	return nil
}

func (s *fakeStore) InsertChain(_ context.Context, arg dbgen.InsertChainParams) (int64, error) {
	if err := s.err("InsertChain"); err != nil {
		return 0, err
	}
	if err := s.nameTaken(arg.Name, 0); err != nil {
		return 0, err
	}
	s.nextID++
	s.chains = append(s.chains, dbgen.GetChainRow{ID: s.nextID, PublicID: arg.PublicID, Name: arg.Name,
		Description: arg.Description, CreatedAt: arg.Now, Version: 1, OwningTeamID: arg.OwningTeamID,
		ManagedBy: arg.ManagedBy})
	return s.nextID, nil
}

func (s *fakeStore) UpdateChain(_ context.Context, arg dbgen.UpdateChainParams) error {
	if err := s.err("UpdateChain"); err != nil {
		return err
	}
	if err := s.nameTaken(arg.Name, arg.ID); err != nil {
		return err
	}
	for i, c := range s.chains {
		if c.ID == arg.ID {
			s.chains[i].Name, s.chains[i].Description, s.chains[i].OwningTeamID = arg.Name, arg.Description,
				arg.OwningTeamID
			s.chains[i].ManagedBy = arg.ManagedBy
			s.chains[i].Version++
		}
	}
	return nil
}

func (s *fakeStore) DeleteChain(_ context.Context, arg dbgen.DeleteChainParams) error {
	if err := s.err("DeleteChain"); err != nil {
		return err
	}
	s.chains = slices.DeleteFunc(s.chains, func(c dbgen.GetChainRow) bool { return c.ID == arg.ID })
	s.steps = slices.DeleteFunc(s.steps, func(st fakeStep) bool { return st.p.ChainID == arg.ID })
	return nil
}

func (s *fakeStore) ListSteps(_ context.Context, arg dbgen.ListStepsParams) ([]dbgen.ListStepsRow, error) {
	if err := s.err("ListSteps"); err != nil {
		return nil, err
	}
	var out []dbgen.ListStepsRow
	for _, st := range s.steps {
		if !slices.Contains(arg.ChainIds, st.p.ChainID) {
			continue
		}
		p := st.p
		r := dbgen.ListStepsRow{ID: st.id, ChainID: p.ChainID, Position: p.Position, Kind: p.Kind,
			Importance: p.Importance, Everyone: p.Everyone, CallTarget: p.CallTarget, WaitSeconds: p.WaitSeconds,
			RepeatFrom: p.RepeatFrom, RepeatTimes: p.RepeatTimes, WorkingHoursOnly: p.WorkingHoursOnly,
			SeverityCondition: p.SeverityCondition}
		for _, sc := range s.schedules {
			if p.ScheduleID.Valid && sc.id == p.ScheduleID.Int64 {
				t, _ := s.teamByID(sc.team)
				r.SchedulePublicID, r.ScheduleName = pgtype.Text{String: sc.publicID, Valid: true},
					pgtype.Text{String: sc.name, Valid: true}
				r.ScheduleTeamPublicID, r.ScheduleTeamName, r.ScheduleTeamSlug = pgtype.Text{String: t.publicID,
					Valid: true}, pgtype.Text{String: t.name, Valid: true}, pgtype.Text{String: t.slug, Valid: true}
			}
		}
		if t, ok := s.teamByID(p.TeamID.Int64); ok && p.TeamID.Valid {
			r.TeamPublicID, r.TeamName, r.TeamSlug, r.TeamDeleted = pgtype.Text{String: t.publicID, Valid: true},
				pgtype.Text{String: t.name, Valid: true}, pgtype.Text{String: t.slug, Valid: true}, t.deleted
		}
		for _, g := range s.gateways {
			if p.GatewayConnectionID.Valid && g.ID == p.GatewayConnectionID.Int64 {
				r.GatewayPublicID, r.GatewayName, r.GatewayLive = pgtype.Text{String: g.PublicID, Valid: true},
					pgtype.Text{String: g.Name, Valid: true}, g.Live
			}
		}
		for _, d := range s.destinations {
			if p.DestinationID.Valid && d.ID == p.DestinationID.Int64 {
				r.DestinationPublicID, r.DestinationName = pgtype.Text{String: d.PublicID, Valid: true},
					pgtype.Text{String: d.Name, Valid: true}
				r.DestinationType, r.DestinationMode, r.DestinationDeleted = pgtype.Text{String: d.Type, Valid: true},
					d.WebhookMode, d.Deleted
			}
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b dbgen.ListStepsRow) int {
		return cmp.Or(cmp.Compare(a.ChainID, b.ChainID), cmp.Compare(a.Position, b.Position))
	})
	return out, nil
}

func (s *fakeStore) ListStepUsers(_ context.Context, arg dbgen.ListStepUsersParams) ([]dbgen.ListStepUsersRow, error) {
	if err := s.err("ListStepUsers"); err != nil {
		return nil, err
	}
	var out []dbgen.ListStepUsersRow
	for _, st := range s.steps {
		if !slices.Contains(arg.StepIds, st.id) {
			continue
		}
		for _, id := range st.users {
			for _, u := range s.users {
				if u.ID == id {
					out = append(out, dbgen.ListStepUsersRow{StepID: st.id, PublicID: u.PublicID, Name: u.Name,
						Login: u.Login, Role: u.Role, Status: u.Status})
				}
			}
		}
	}
	return out, nil
}

func (s *fakeStore) DeleteSteps(_ context.Context, arg dbgen.DeleteStepsParams) error {
	if err := s.err("DeleteSteps"); err != nil {
		return err
	}
	s.steps = slices.DeleteFunc(slices.Clone(s.steps), func(st fakeStep) bool { return st.p.ChainID == arg.ChainID })
	return nil
}

func (s *fakeStore) InsertStep(_ context.Context, arg dbgen.InsertStepParams) (int64, error) {
	if err := s.err("InsertStep"); err != nil {
		return 0, err
	}
	s.nextID++
	s.steps = append(s.steps, fakeStep{id: s.nextID, p: arg})
	return s.nextID, nil
}

func (s *fakeStore) InsertStepUsers(_ context.Context, arg dbgen.InsertStepUsersParams) error {
	if err := s.err("InsertStepUsers"); err != nil {
		return err
	}
	for i := range s.steps {
		if s.steps[i].id == arg.StepID {
			s.steps[i].users = slices.Clone(arg.UserIds)
		}
	}
	return nil
}

func (s *fakeStore) ResolveSchedules(_ context.Context, arg dbgen.ResolveSchedulesParams) (
	[]dbgen.ResolveSchedulesRow, error) {
	if err := s.err("ResolveSchedules"); err != nil {
		return nil, err
	}
	var out []dbgen.ResolveSchedulesRow
	for _, sc := range s.schedules {
		if slices.Contains(arg.PublicIds, sc.publicID) {
			t, _ := s.teamByID(sc.team)
			s.locks = append(s.locks, "schedule:"+sc.publicID)
			out = append(out, dbgen.ResolveSchedulesRow{ID: sc.id, PublicID: sc.publicID, Name: sc.name,
				TeamPublicID: t.publicID, TeamName: t.name, TeamSlug: t.slug, TeamDeleted: t.deleted})
		}
	}
	return out, nil
}

func (s *fakeStore) ResolveUsers(_ context.Context, arg dbgen.ResolveUsersParams) ([]dbgen.ResolveUsersRow, error) {
	if err := s.err("ResolveUsers"); err != nil {
		return nil, err
	}
	var out []dbgen.ResolveUsersRow
	for _, u := range s.users {
		if slices.Contains(arg.PublicIds, u.PublicID) {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *fakeStore) ResolveTeams(_ context.Context, arg dbgen.ResolveTeamsParams) ([]dbgen.ResolveTeamsRow, error) {
	if err := s.err("ResolveTeams"); err != nil {
		return nil, err
	}
	var out []dbgen.ResolveTeamsRow
	for _, t := range s.teams {
		if slices.Contains(arg.PublicIds, t.publicID) {
			out = append(out, dbgen.ResolveTeamsRow{ID: t.id, PublicID: t.publicID, Name: t.name, Slug: t.slug,
				Deleted: t.deleted})
		}
	}
	return out, nil
}

func (s *fakeStore) ListGateways(context.Context, int64) ([]dbgen.ListGatewaysRow, error) {
	return s.gateways, s.err("ListGateways")
}

func (s *fakeStore) ResolveDestinations(_ context.Context, arg dbgen.ResolveDestinationsParams) (
	[]dbgen.ResolveDestinationsRow, error) {
	if err := s.err("ResolveDestinations"); err != nil {
		return nil, err
	}
	var out []dbgen.ResolveDestinationsRow
	for _, d := range s.destinations {
		if slices.Contains(arg.PublicIds, d.PublicID) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *fakeStore) ListRouteReferrers(_ context.Context, arg dbgen.ListRouteReferrersParams) (
	[]dbgen.ListRouteReferrersRow, error) {
	if err := s.err("ListRouteReferrers"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRouteReferrersRow
	for _, r := range s.routeRefs {
		if slices.Contains(arg.ChainIds, r.chain) {
			out = append(out, dbgen.ListRouteReferrersRow{ChainID: r.chain, ID: r.route, PublicID: r.publicID,
				Name: r.name, SeverityCondition: r.condition})
		}
	}
	return out, nil
}

func (s *fakeStore) ListTeamReferrers(_ context.Context, arg dbgen.ListTeamReferrersParams) (
	[]dbgen.ListTeamReferrersRow, error) {
	if err := s.err("ListTeamReferrers"); err != nil {
		return nil, err
	}
	var out []dbgen.ListTeamReferrersRow
	for _, r := range s.teamRefs {
		if t, ok := s.teamByID(r.team); ok && slices.Contains(arg.ChainIds, r.chain) {
			out = append(out, dbgen.ListTeamReferrersRow{ChainID: r.chain, ID: t.id, PublicID: t.publicID,
				Name: t.name, Slug: t.slug, SeverityCondition: r.condition})
		}
	}
	return out, nil
}

func (s *fakeStore) ListChainDestinations(_ context.Context, arg dbgen.ListChainDestinationsParams) (
	[]dbgen.ListChainDestinationsRow, error) {
	if err := s.err("ListChainDestinations"); err != nil {
		return nil, err
	}
	var out []dbgen.ListChainDestinationsRow
	for _, id := range arg.ChainIds {
		for _, d := range s.chainDests[id] {
			d.ChainID = id
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *fakeStore) ListUserTeams(_ context.Context, arg dbgen.ListUserTeamsParams) ([]int64, error) {
	return s.userTeams[arg.UserID], s.err("ListUserTeams")
}

func (s *fakeStore) GetTeamID(_ context.Context, arg dbgen.GetTeamIDParams) (int64, error) {
	if err := s.err("GetTeamID"); err != nil {
		return 0, err
	}
	for _, t := range s.teams {
		if t.publicID == arg.PublicID && !t.deleted {
			return t.id, nil
		}
	}
	return 0, pgx.ErrNoRows
}

func (s *fakeStore) ListChainInfo(context.Context, int64) ([]dbgen.ListChainInfoRow, error) {
	if err := s.err("ListChainInfo"); err != nil {
		return nil, err
	}
	out := make([]dbgen.ListChainInfoRow, len(s.chains))
	for i, c := range s.chains {
		out[i] = dbgen.ListChainInfoRow{PublicID: c.PublicID, Name: c.Name}
	}
	return out, nil
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.err("InsertAuditEntry"); err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

func (s *fakeStore) Notify(_ context.Context, h db.Hint) error {
	if err := s.err("Notify"); err != nil {
		return err
	}
	s.hints = append(s.hints, h)
	return nil
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }

func newService(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	store := newStore()
	c := clock.NewManual(t0)
	return New(Config{OrgID: orgID, Store: store, Audit: audit.NewWriter(logging.New(nilWriter{},
		logging.LevelInfo), c), Business: c}), store
}

func ptr[T any](v T) *T { return &v }

// fieldError is err as a FieldError, failing the test when it is none.
func fieldError(t *testing.T, err error) *FieldError {
	t.Helper()
	f, ok := err.(*FieldError) //nolint:errorlint // the package returns it unwrapped
	if !ok {
		t.Fatalf("error %v, want a FieldError", err)
	}
	return f
}
