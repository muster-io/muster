// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"cmp"
	"context"
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
	"github.com/muster-io/muster/internal/schedules/dbgen"
)

const (
	orgID   = 7
	teamPay = "TM00000000PAY1"
	teamOps = "TM0000000000PS"
)

// t0 is Wednesday 28 October 2026, noon UTC: the Monday that follows is 2 November.
var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

var errBoom = errors.New("boom")

// The people of the tests besides petr, sergey and olga: bob, a Responder outside the Team; vic, a Viewer in it; dan,
// disabled in it; and ann, an Admin outside it.
var (
	bob = Person{ID: 4, PublicID: "SR00000000BB0B", Name: "Bob", Login: "bob", Role: "responder", Status: "active"}
	vic = Person{ID: 5, PublicID: "SR000000000VKC", Name: "Vic", Login: "vic", Role: "viewer", Status: "active"}
	dan = Person{ID: 6, PublicID: "SR000000000DAN", Name: "Dan", Login: "dan", Role: "responder", Status: "disabled"}
	ann = Person{ID: 8, PublicID: "SR000000000ANN", Name: "Ann", Login: "ann", Role: "admin", Status: "active"}
)

var (
	admin  = Requester{Actor: audit.User(ann.ID, ann.PublicID), Transport: audit.TransportAPI, Admin: true}
	asPetr = Requester{Actor: audit.User(petr.ID, petr.PublicID), Transport: audit.TransportAPI}
	asBob  = Requester{Actor: audit.User(bob.ID, bob.PublicID), Transport: audit.TransportAPI}
)

type teamRec struct {
	id                   int64
	publicID, name, slug string
}

type scheduleRec struct {
	id, team, version  int64
	publicID, name, tz string
	main               bool
	created            time.Time
}

type rotationLayerRec struct {
	dbgen.ListRotationLayersRow
	users []int64
}

type overrideRec struct {
	id, schedule, user, replaces, byUser, bySA, version int64
	publicID                                            string
	start, end, created                                 time.Time
}

// fakeStore is the database of the package in memory.
type fakeStore struct {
	zone      string
	teams     []teamRec
	inTeam    map[int64][]int64
	users     []Person
	schedules []*scheduleRec
	layers    []*rotationLayerRec
	overrides []*overrideRec
	sas       map[int64]string
	audit     []auditdb.InsertAuditEntryParams
	hints     []db.Hint
	fail      map[string]error
	nextID    int64
	listed    []dbgen.ListSchedulesParams
}

// newStore has the Teams payments, with petr, sergey, olga, vic and dan, and ops, with bob.
func newStore() *fakeStore {
	return &fakeStore{zone: "Europe/Berlin",
		teams:  []teamRec{{1, teamPay, "Payments", "payments"}, {2, teamOps, "Ops", "ops"}},
		inTeam: map[int64][]int64{1: {petr.ID, sergey.ID, olga.ID, vic.ID, dan.ID}, 2: {bob.ID}},
		users:  []Person{petr, sergey, olga, bob, vic, dan, ann},
		sas:    map[int64]string{9: "SA0000000000CI"}, fail: map[string]error{}, nextID: 100}
}

func (s *fakeStore) err(name string) error { return s.fail[name] }

func (s *fakeStore) id() int64 {
	s.nextID++
	return s.nextID
}

func (s *fakeStore) user(id int64) Person {
	for _, u := range s.users {
		if u.ID == id {
			return u
		}
	}
	return Person{}
}

func (s *fakeStore) team(id int64) teamRec {
	for _, t := range s.teams {
		if t.id == id {
			return t
		}
	}
	return teamRec{}
}

func (s *fakeStore) schedule(publicID string) *scheduleRec {
	for _, sc := range s.schedules {
		if sc.publicID == publicID {
			return sc
		}
	}
	return nil
}

func (s *fakeStore) GetOrganizationTimeZone(_ context.Context, org int64) (string, error) {
	if org != orgID {
		return "", pgx.ErrNoRows
	}
	return s.zone, s.err("GetOrganizationTimeZone")
}

func (s *fakeStore) LockScheduleTeam(_ context.Context, arg dbgen.LockScheduleTeamParams) (
	dbgen.LockScheduleTeamRow, error) {
	if err := s.err("LockScheduleTeam"); err != nil {
		return dbgen.LockScheduleTeamRow{}, err
	}
	for _, t := range s.teams {
		if t.publicID == arg.PublicID {
			return dbgen.LockScheduleTeamRow{ID: t.id, PublicID: t.publicID, Name: t.name, Slug: t.slug}, nil
		}
	}
	return dbgen.LockScheduleTeamRow{}, pgx.ErrNoRows
}

func (s *fakeStore) LockScheduleTeamByID(_ context.Context, arg dbgen.LockScheduleTeamByIDParams) (
	dbgen.LockScheduleTeamByIDRow, error) {
	t := s.team(arg.ID)
	return dbgen.LockScheduleTeamByIDRow{ID: t.id, PublicID: t.publicID, Name: t.name, Slug: t.slug},
		s.err("LockScheduleTeamByID")
}

func (s *fakeStore) CountTeamSchedules(_ context.Context, arg dbgen.CountTeamSchedulesParams) (int64, error) {
	var n int64
	for _, sc := range s.schedules {
		if sc.team == arg.TeamID {
			n++
		}
	}
	return n, s.err("CountTeamSchedules")
}

func (s *fakeStore) ListSchedules(_ context.Context, arg dbgen.ListSchedulesParams) ([]dbgen.ListSchedulesRow,
	error) {
	s.listed = append(s.listed, arg)
	if err := s.err("ListSchedules"); err != nil {
		return nil, err
	}
	list := slices.Clone(s.schedules)
	slices.SortFunc(list, func(a, b *scheduleRec) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)), cmp.Compare(a.id, b.id))
	})
	var out []dbgen.ListSchedulesRow
	for _, sc := range list {
		t := s.team(sc.team)
		switch {
		case arg.Team.Valid && t.publicID != arg.Team.String:
			continue
		case arg.MemberID.Valid && !slices.Contains(s.inTeam[sc.team], arg.MemberID.Int64):
			continue
		case arg.Q.Valid && !strings.Contains(strings.ToLower(sc.name), strings.ToLower(arg.Q.String)):
			continue
		case arg.AfterID.Valid && cmp.Or(strings.Compare(strings.ToLower(sc.name), arg.AfterName.String),
			cmp.Compare(sc.id, arg.AfterID.Int64)) <= 0:
			continue
		}
		if len(out) == int(arg.PageSize) {
			break
		}
		out = append(out, dbgen.ListSchedulesRow{ID: sc.id, PublicID: sc.publicID, Name: sc.name, TimeZone: sc.tz,
			Main: sc.main, Version: sc.version, CreatedAt: sc.created, TeamPublicID: t.publicID, TeamName: t.name,
			TeamSlug: t.slug, SortName: strings.ToLower(sc.name)})
	}
	return out, nil
}

func (s *fakeStore) GetSchedule(_ context.Context, arg dbgen.GetScheduleParams) (dbgen.GetScheduleRow, error) {
	if err := s.err("GetSchedule"); err != nil {
		return dbgen.GetScheduleRow{}, err
	}
	sc := s.schedule(arg.PublicID)
	if sc == nil || arg.OrgID != orgID {
		return dbgen.GetScheduleRow{}, pgx.ErrNoRows
	}
	t := s.team(sc.team)
	return dbgen.GetScheduleRow{ID: sc.id, PublicID: sc.publicID, Name: sc.name, TimeZone: sc.tz, Main: sc.main,
		Version: sc.version, CreatedAt: sc.created, TeamID: t.id, TeamPublicID: t.publicID, TeamName: t.name,
		TeamSlug: t.slug}, nil
}

func (s *fakeStore) FindScheduleTeam(_ context.Context, arg dbgen.FindScheduleTeamParams) (int64, error) {
	if err := s.err("FindScheduleTeam"); err != nil {
		return 0, err
	}
	if sc := s.schedule(arg.PublicID); sc != nil {
		return sc.team, nil
	}
	return 0, pgx.ErrNoRows
}

func (s *fakeStore) LockSchedule(_ context.Context, arg dbgen.LockScheduleParams) (int64, error) {
	if err := s.err("LockSchedule"); err != nil {
		return 0, err
	}
	if sc := s.schedule(arg.PublicID); sc != nil {
		return sc.id, nil
	}
	return 0, pgx.ErrNoRows
}

func (s *fakeStore) InsertSchedule(_ context.Context, arg dbgen.InsertScheduleParams) (int64, error) {
	if err := s.err("InsertSchedule"); err != nil {
		return 0, err
	}
	if err := s.unique(arg.TeamID, arg.Name, 0); err != nil {
		return 0, err
	}
	id := s.id()
	s.schedules = append(s.schedules, &scheduleRec{id: id, team: arg.TeamID, version: 1, publicID: arg.PublicID,
		name: arg.Name, tz: arg.TimeZone, main: arg.Main, created: arg.Now})
	return id, nil
}

// unique refuses a name another Schedule of the Team has, as the unique index does.
func (s *fakeStore) unique(team int64, name string, except int64) error {
	for _, sc := range s.schedules {
		if sc.team == team && sc.id != except && strings.EqualFold(sc.name, name) {
			return &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameKey}
		}
	}
	return nil
}

func (s *fakeStore) UpdateSchedule(_ context.Context, arg dbgen.UpdateScheduleParams) error {
	if err := s.err("UpdateSchedule"); err != nil {
		return err
	}
	for _, sc := range s.schedules {
		if sc.id == arg.ID {
			if err := s.unique(sc.team, arg.Name, sc.id); err != nil {
				return err
			}
			sc.name, sc.tz, sc.main, sc.version = arg.Name, arg.TimeZone, arg.Main, sc.version+1
		}
	}
	return nil
}

func (s *fakeStore) ClearMainSchedule(_ context.Context, arg dbgen.ClearMainScheduleParams) (
	[]dbgen.ClearMainScheduleRow, error) {
	if err := s.err("ClearMainSchedule"); err != nil {
		return nil, err
	}
	var out []dbgen.ClearMainScheduleRow
	for _, sc := range s.schedules {
		if sc.team == arg.TeamID && sc.main && sc.id != arg.KeepID {
			sc.main, sc.version = false, sc.version+1
			out = append(out, dbgen.ClearMainScheduleRow{PublicID: sc.publicID, Name: sc.name})
		}
	}
	return out, nil
}

func (s *fakeStore) DeleteSchedule(_ context.Context, arg dbgen.DeleteScheduleParams) error {
	if err := s.err("DeleteSchedule"); err != nil {
		return err
	}
	s.schedules = slices.DeleteFunc(s.schedules, func(sc *scheduleRec) bool { return sc.id == arg.ID })
	s.layers = slices.DeleteFunc(s.layers, func(l *rotationLayerRec) bool { return l.ScheduleID == arg.ID })
	s.overrides = slices.DeleteFunc(s.overrides, func(o *overrideRec) bool { return o.schedule == arg.ID })
	return nil
}

func (s *fakeStore) ListRotationLayers(_ context.Context, arg dbgen.ListRotationLayersParams) (
	[]dbgen.ListRotationLayersRow, error) {
	if err := s.err("ListRotationLayers"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRotationLayersRow
	for _, l := range s.layers {
		if slices.Contains(arg.ScheduleIds, l.ScheduleID) {
			out = append(out, l.ListRotationLayersRow)
		}
	}
	slices.SortFunc(out, func(a, b dbgen.ListRotationLayersRow) int {
		return cmp.Or(cmp.Compare(a.ScheduleID, b.ScheduleID), cmp.Compare(a.Position, b.Position))
	})
	return out, nil
}

func (s *fakeStore) ListRotationLayerUsers(_ context.Context, arg dbgen.ListRotationLayerUsersParams) (
	[]dbgen.ListRotationLayerUsersRow, error) {
	if err := s.err("ListRotationLayerUsers"); err != nil {
		return nil, err
	}
	var out []dbgen.ListRotationLayerUsersRow
	for _, id := range arg.RotationLayerIds {
		for _, l := range s.layers {
			if l.ID != id {
				continue
			}
			for i, u := range l.users {
				p := s.user(u)
				out = append(out, dbgen.ListRotationLayerUsersRow{RotationLayerID: l.ID, Position: int32(i + 1),
					ID: p.ID, PublicID: p.PublicID, Name: p.Name, Login: p.Login, Role: p.Role, Status: p.Status})
			}
		}
	}
	return out, nil
}

func (s *fakeStore) InsertRotationLayer(_ context.Context, arg dbgen.InsertRotationLayerParams) (int64, error) {
	if err := s.err("InsertRotationLayer"); err != nil {
		return 0, err
	}
	id := s.id()
	s.layers = append(s.layers, &rotationLayerRec{ListRotationLayersRow: dbgen.ListRotationLayersRow{ID: id,
		PublicID: arg.PublicID, ScheduleID: arg.ScheduleID, Position: arg.Position, Name: arg.Name,
		ShiftLength: arg.ShiftLength, ShiftSeconds: arg.ShiftSeconds, HandoverTime: arg.HandoverTime,
		HandoverDay: arg.HandoverDay, StartDate: arg.StartDate, LimitDays: arg.LimitDays, LimitStart: arg.LimitStart,
		LimitEnd: arg.LimitEnd}})
	return id, nil
}

func (s *fakeStore) UpdateRotationLayer(_ context.Context, arg dbgen.UpdateRotationLayerParams) error {
	if err := s.err("UpdateRotationLayer"); err != nil {
		return err
	}
	for _, l := range s.layers {
		if l.ID == arg.ID {
			l.Position, l.Name, l.ShiftLength, l.ShiftSeconds = arg.Position, arg.Name, arg.ShiftLength,
				arg.ShiftSeconds
			l.HandoverTime, l.HandoverDay, l.StartDate = arg.HandoverTime, arg.HandoverDay, arg.StartDate
			l.LimitDays, l.LimitStart, l.LimitEnd = arg.LimitDays, arg.LimitStart, arg.LimitEnd
		}
	}
	return nil
}

func (s *fakeStore) DeleteRotationLayers(_ context.Context, arg dbgen.DeleteRotationLayersParams) error {
	if err := s.err("DeleteRotationLayers"); err != nil {
		return err
	}
	s.layers = slices.DeleteFunc(s.layers, func(l *rotationLayerRec) bool {
		return l.ScheduleID == arg.ScheduleID && !slices.Contains(arg.Keep, l.ID)
	})
	return nil
}

func (s *fakeStore) DeleteRotationLayerUsers(_ context.Context, arg dbgen.DeleteRotationLayerUsersParams) error {
	for _, l := range s.layers {
		if l.ID == arg.RotationLayerID {
			l.users = nil
		}
	}
	return s.err("DeleteRotationLayerUsers")
}

func (s *fakeStore) InsertRotationLayerUsers(_ context.Context, arg dbgen.InsertRotationLayerUsersParams) error {
	for _, l := range s.layers {
		if l.ID == arg.RotationLayerID {
			l.users = slices.Clone(arg.UserIds)
		}
	}
	return s.err("InsertRotationLayerUsers")
}

func (s *fakeStore) LookupUsers(_ context.Context, arg dbgen.LookupUsersParams) ([]dbgen.LookupUsersRow, error) {
	if err := s.err("LookupUsers"); err != nil {
		return nil, err
	}
	var out []dbgen.LookupUsersRow
	for _, u := range s.users {
		if slices.Contains(arg.PublicIds, u.PublicID) && u.Status != "deleted" {
			out = append(out, dbgen.LookupUsersRow{ID: u.ID, PublicID: u.PublicID, Name: u.Name, Login: u.Login,
				Role: u.Role, Status: u.Status})
		}
	}
	return out, nil
}

func (s *fakeStore) ListPageable(_ context.Context, arg dbgen.ListPageableParams) ([]dbgen.ListPageableRow, error) {
	if err := s.err("ListPageable"); err != nil {
		return nil, err
	}
	var out []dbgen.ListPageableRow
	for _, u := range s.users {
		if slices.Contains(arg.UserIds, u.ID) {
			out = append(out, dbgen.ListPageableRow{ID: u.ID, PublicID: u.PublicID, Name: u.Name, Login: u.Login,
				Role: u.Role, Status: u.Status, InTeam: slices.Contains(s.inTeam[arg.TeamID], u.ID)})
		}
	}
	return out, nil
}

func (s *fakeStore) IsTeamUser(_ context.Context, arg dbgen.IsTeamUserParams) (bool, error) {
	return slices.Contains(s.inTeam[arg.TeamID], arg.UserID), s.err("IsTeamUser")
}

// overrideRow is the row of an Override with its people, as the queries join them.
func (s *fakeStore) overrideRow(o *overrideRec) overrideRow {
	u := s.user(o.user)
	r := overrideRow{ID: o.id, PublicID: o.publicID, UserID: o.user, StartsAt: o.start, EndsAt: o.end,
		Version: o.version, CreatedAt: o.created, UserPublicID: u.PublicID, UserName: u.Name, UserLogin: u.Login,
		UserStatus: u.Status}
	text := func(v string) pgtype.Text { return pgtype.Text{String: v, Valid: true} }
	if o.replaces != 0 {
		p := s.user(o.replaces)
		r.ReplacesPublicID, r.ReplacesName, r.ReplacesLogin, r.ReplacesStatus = text(p.PublicID), text(p.Name),
			text(p.Login), text(p.Status)
	}
	if o.byUser != 0 {
		p := s.user(o.byUser)
		r.ByUserPublicID, r.ByUserName, r.ByUserLogin, r.ByUserStatus = text(p.PublicID), text(p.Name),
			text(p.Login), text(p.Status)
	} else {
		r.ByServiceAccountPublicID, r.ByServiceAccountName, r.ByServiceAccountStatus = text(s.sas[o.bySA]),
			text("ci"), text("deleted")
	}
	return r
}

func (s *fakeStore) ListScheduleOverrides(_ context.Context, arg dbgen.ListScheduleOverridesParams) (
	[]dbgen.ListScheduleOverridesRow, error) {
	if err := s.err("ListScheduleOverrides"); err != nil {
		return nil, err
	}
	list := slices.Clone(s.overrides)
	slices.SortFunc(list, func(a, b *overrideRec) int {
		return cmp.Or(a.start.Compare(b.start), cmp.Compare(a.id, b.id))
	})
	var out []dbgen.ListScheduleOverridesRow
	for _, o := range list {
		switch {
		case o.schedule != arg.ScheduleID || !o.end.After(arg.PeriodStart):
			continue
		case arg.PeriodEnd.Valid && !o.start.Before(arg.PeriodEnd.Time):
			continue
		case arg.AfterID.Valid && cmp.Or(o.start.Compare(arg.AfterStart.Time), cmp.Compare(o.id, arg.AfterID.Int64)) <= 0:
			continue
		}
		if len(out) == int(arg.PageSize) {
			break
		}
		r := s.overrideRow(o)
		out = append(out, dbgen.ListScheduleOverridesRow{ID: r.ID, PublicID: r.PublicID, StartsAt: r.StartsAt,
			EndsAt: r.EndsAt, Version: r.Version, CreatedAt: r.CreatedAt, UserPublicID: r.UserPublicID,
			UserName: r.UserName, UserLogin: r.UserLogin, UserStatus: r.UserStatus,
			ReplacesPublicID: r.ReplacesPublicID, ReplacesName: r.ReplacesName, ReplacesLogin: r.ReplacesLogin,
			ReplacesStatus: r.ReplacesStatus, ByUserPublicID: r.ByUserPublicID, ByUserName: r.ByUserName,
			ByUserLogin: r.ByUserLogin, ByUserStatus: r.ByUserStatus,
			ByServiceAccountPublicID: r.ByServiceAccountPublicID, ByServiceAccountName: r.ByServiceAccountName,
			ByServiceAccountStatus: r.ByServiceAccountStatus})
	}
	return out, nil
}

func (s *fakeStore) ListPeriodOverrides(_ context.Context, arg dbgen.ListPeriodOverridesParams) (
	[]dbgen.ListPeriodOverridesRow, error) {
	if err := s.err("ListPeriodOverrides"); err != nil {
		return nil, err
	}
	var out []dbgen.ListPeriodOverridesRow
	for _, o := range s.overrides {
		if o.schedule == arg.ScheduleID && o.end.After(arg.PeriodStart) && o.start.Before(arg.PeriodEnd) {
			out = append(out, dbgen.ListPeriodOverridesRow(s.overrideRow(o)))
		}
	}
	return out, nil
}

func (s *fakeStore) findOverride(schedule int64, publicID string) *overrideRec {
	for _, o := range s.overrides {
		if o.schedule == schedule && o.publicID == publicID {
			return o
		}
	}
	return nil
}

func (s *fakeStore) GetScheduleOverride(_ context.Context, arg dbgen.GetScheduleOverrideParams) (
	dbgen.GetScheduleOverrideRow, error) {
	if err := s.err("GetScheduleOverride"); err != nil {
		return dbgen.GetScheduleOverrideRow{}, err
	}
	o := s.findOverride(arg.ScheduleID, arg.PublicID)
	if o == nil {
		return dbgen.GetScheduleOverrideRow{}, pgx.ErrNoRows
	}
	r := s.overrideRow(o)
	return dbgen.GetScheduleOverrideRow{ID: r.ID, PublicID: r.PublicID, UserID: r.UserID,
		ReplacesUserID: pgtype.Int8{Int64: o.replaces, Valid: o.replaces != 0}, StartsAt: r.StartsAt,
		EndsAt: r.EndsAt, Version: r.Version, CreatedAt: r.CreatedAt, UserPublicID: r.UserPublicID,
		UserName: r.UserName, UserLogin: r.UserLogin, UserStatus: r.UserStatus, ReplacesPublicID: r.ReplacesPublicID,
		ReplacesName: r.ReplacesName, ReplacesLogin: r.ReplacesLogin, ReplacesStatus: r.ReplacesStatus,
		ByUserPublicID: r.ByUserPublicID, ByUserName: r.ByUserName, ByUserLogin: r.ByUserLogin,
		ByUserStatus: r.ByUserStatus, ByServiceAccountPublicID: r.ByServiceAccountPublicID,
		ByServiceAccountName: r.ByServiceAccountName, ByServiceAccountStatus: r.ByServiceAccountStatus}, nil
}

func (s *fakeStore) CountOverlappingOverrides(_ context.Context, arg dbgen.CountOverlappingOverridesParams) (int64,
	error) {
	if err := s.err("CountOverlappingOverrides"); err != nil {
		return 0, err
	}
	var n int64
	for _, o := range s.overrides {
		if o.schedule == arg.ScheduleID && o.id != arg.ExceptID && o.end.After(arg.SpanStart) &&
			o.start.Before(arg.SpanEnd) {
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) LockScheduleOverride(_ context.Context, arg dbgen.LockScheduleOverrideParams) (int64, error) {
	if err := s.err("LockScheduleOverride"); err != nil {
		return 0, err
	}
	if o := s.findOverride(arg.ScheduleID, arg.PublicID); o != nil {
		return o.id, nil
	}
	return 0, pgx.ErrNoRows
}

func (s *fakeStore) InsertScheduleOverride(_ context.Context, arg dbgen.InsertScheduleOverrideParams) (int64,
	error) {
	if err := s.err("InsertScheduleOverride"); err != nil {
		return 0, err
	}
	id := s.id()
	s.overrides = append(s.overrides, &overrideRec{id: id, schedule: arg.ScheduleID, user: arg.UserID,
		replaces: arg.ReplacesUserID.Int64, byUser: arg.CreatedByUserID.Int64, bySA: arg.CreatedByServiceAccountID.Int64,
		version: 1, publicID: arg.PublicID, start: arg.StartsAt, end: arg.EndsAt, created: arg.Now})
	return id, nil
}

func (s *fakeStore) UpdateScheduleOverride(_ context.Context, arg dbgen.UpdateScheduleOverrideParams) error {
	if err := s.err("UpdateScheduleOverride"); err != nil {
		return err
	}
	for _, o := range s.overrides {
		if o.id == arg.ID {
			o.user, o.replaces, o.start, o.end, o.version = arg.UserID, arg.ReplacesUserID.Int64, arg.StartsAt,
				arg.EndsAt, o.version+1
		}
	}
	return nil
}

func (s *fakeStore) DeleteScheduleOverride(_ context.Context, arg dbgen.DeleteScheduleOverrideParams) error {
	if err := s.err("DeleteScheduleOverride"); err != nil {
		return err
	}
	s.overrides = slices.DeleteFunc(s.overrides, func(o *overrideRec) bool { return o.id == arg.ID })
	return nil
}

func (s *fakeStore) ListScheduleInfo(_ context.Context, org int64) ([]dbgen.ListScheduleInfoRow, error) {
	if err := s.err("ListScheduleInfo"); err != nil || org != orgID {
		return nil, err
	}
	var out []dbgen.ListScheduleInfoRow
	for _, sc := range s.schedules {
		out = append(out, dbgen.ListScheduleInfoRow{PublicID: sc.publicID, Name: sc.name})
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

// InTx runs f and, when it fails, puts back the rows as they were, as a rolled back transaction does.
func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	schedules, layers, overrides := s.snapshot()
	err := f(s)
	if err != nil {
		s.schedules, s.layers, s.overrides = schedules, layers, overrides
	}
	return err
}

func (s *fakeStore) snapshot() ([]*scheduleRec, []*rotationLayerRec, []*overrideRec) {
	schedules := make([]*scheduleRec, len(s.schedules))
	for i, sc := range s.schedules {
		c := *sc
		schedules[i] = &c
	}
	layers := make([]*rotationLayerRec, len(s.layers))
	for i, l := range s.layers {
		c := *l
		c.users = slices.Clone(l.users)
		layers[i] = &c
	}
	overrides := make([]*overrideRec, len(s.overrides))
	for i, o := range s.overrides {
		c := *o
		overrides[i] = &c
	}
	return schedules, layers, overrides
}

func newService(t *testing.T) (*Service, *fakeStore, *clock.Manual) {
	t.Helper()
	store := newStore()
	c := clock.NewManual(t0)
	return New(Config{OrgID: orgID, Store: store, Audit: audit.NewWriter(logging.New(nilWriter{},
		logging.LevelInfo), c), Business: c}), store, c
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

// infoSeries are the muster_schedule_info series of the Schedule id, as /metrics shows them.
func infoSeries(t *testing.T, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	var out []string
	for line := range strings.Lines(rec.Body.String()) {
		if strings.HasPrefix(line, `muster_schedule_info{schedule="`+id+`"`) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, "\n")
}

// mainInput is the Schedule "Main" of payments with the two Rotation layers of scenario 1 from 2 November 2026.
func mainInput() Input {
	return Input{TeamID: teamPay, Name: "Main", TimeZone: ptr("Europe/Berlin"), RotationLayers: []RotationLayerInput{
		{Name: "Around the clock", UserIDs: []string{petr.PublicID, sergey.PublicID, olga.PublicID},
			ShiftLength: ShiftWeek, HandoverTime: "10:00", HandoverDay: "monday", StartDate: date("2026-11-02")},
		{Name: "Working hours 11-19", UserIDs: []string{olga.PublicID, petr.PublicID}, ShiftLength: ShiftWeek,
			HandoverTime: "11:00", HandoverDay: "monday", StartDate: date("2026-11-02"),
			Limits: &Limits{Days: []string{"friday", "monday", "tuesday", "wednesday", "thursday"}, Start: "11:00",
				End: "19:00"}},
	}}
}

// inputOf is the input that saves sc as it is.
func inputOf(sc Schedule) Input {
	in := Input{TeamID: sc.Team.PublicID, Name: sc.Name, TimeZone: &sc.TimeZone}
	for _, l := range sc.RotationLayers {
		li := RotationLayerInput{ID: l.PublicID, Name: l.Name, ShiftLength: l.ShiftLength, HandoverTime: l.HandoverTime,
			HandoverDay: l.HandoverDay, StartDate: l.StartDate, Limits: l.Limits}
		if l.ShiftLength == ShiftHours {
			li.ShiftSeconds = ptr(l.ShiftSeconds)
		}
		for _, u := range l.Users {
			li.UserIDs = append(li.UserIDs, u.PublicID)
		}
		in.RotationLayers = append(in.RotationLayers, li)
	}
	return in
}

// TestCheck is C-27.FR-1 and FR-2: every field of a Schedule and of its Rotation layers refused at its pointer with its
// code — a Shift length in hours that is not whole hours from one hour, 5400 included, and a window whose end equals
// its start among them.
func TestCheck(t *testing.T) {
	layer := func(f func(l *RotationLayerInput)) Input {
		in := mainInput()
		f(&in.RotationLayers[1])
		return in
	}
	for _, c := range []struct {
		name          string
		in            Input
		pointer, code string
	}{
		{"no name", Input{Name: " "}, "/name", CodeRequired},
		{"a long name", Input{Name: strings.Repeat("x", 201)}, "/name", CodeTooLong},
		{"a time zone that is not one", Input{Name: "x", TimeZone: ptr("Mars/Olympus")}, "/time_zone",
			CodeInvalidFormat},
		{"Local is no time zone", Input{Name: "x", TimeZone: ptr("Local")}, "/time_zone", CodeInvalidFormat},
		{"a layer id that is not one", layer(func(l *RotationLayerInput) { l.ID = "TM0000000000AA" }),
			"/rotation_layers/1/id", CodeUnknownID},
		{"a layer without a name", layer(func(l *RotationLayerInput) { l.Name = "" }), "/rotation_layers/1/name",
			CodeRequired},
		{"a layer with a long name", layer(func(l *RotationLayerInput) { l.Name = strings.Repeat("x", 201) }),
			"/rotation_layers/1/name", CodeTooLong},
		{"a layer without people", layer(func(l *RotationLayerInput) { l.UserIDs = nil }), "/rotation_layers/1/user_ids",
			CodeRequired},
		{"a person who is not a User id", layer(func(l *RotationLayerInput) { l.UserIDs[1] = "TM0000000000AA" }),
			"/rotation_layers/1/user_ids/1", CodeUnknownID},
		{"a handover that is not HH:MM", layer(func(l *RotationLayerInput) { l.HandoverTime = "24:00" }),
			"/rotation_layers/1/handover_time", CodeInvalidFormat},
		{"an unknown Shift length", layer(func(l *RotationLayerInput) { l.ShiftLength = "month" }),
			"/rotation_layers/1/shift_length", CodeInvalidFormat},
		{"hours without a length", layer(func(l *RotationLayerInput) { l.ShiftLength, l.HandoverDay = ShiftHours, "" }),
			"/rotation_layers/1/shift_seconds", CodeRequired},
		{"a length with weeks", layer(func(l *RotationLayerInput) { l.ShiftSeconds = ptr(3600) }),
			"/rotation_layers/1/shift_seconds", CodeUnsupported},
		{"5400 seconds", layer(func(l *RotationLayerInput) {
			l.ShiftLength, l.HandoverDay, l.ShiftSeconds = ShiftHours, "", ptr(5400)
		}), "/rotation_layers/1/shift_seconds", CodeOutOfRange},
		{"less than an hour", layer(func(l *RotationLayerInput) {
			l.ShiftLength, l.HandoverDay, l.ShiftSeconds = ShiftHours, "", ptr(1800)
		}), "/rotation_layers/1/shift_seconds", CodeOutOfRange},
		{"weeks without a day", layer(func(l *RotationLayerInput) { l.HandoverDay = "" }), "/rotation_layers/1/handover_day",
			CodeRequired},
		{"a day with days", layer(func(l *RotationLayerInput) { l.ShiftLength = ShiftDay }), "/rotation_layers/1/handover_day",
			CodeUnsupported},
		{"a day that is not one", layer(func(l *RotationLayerInput) { l.HandoverDay = "funday" }),
			"/rotation_layers/1/handover_day", CodeInvalidFormat},
		{"limits without days", layer(func(l *RotationLayerInput) { l.Limits.Days = nil }), "/rotation_layers/1/limits/days",
			CodeRequired},
		{"limits with a day that is not one", layer(func(l *RotationLayerInput) { l.Limits.Days[2] = "x" }),
			"/rotation_layers/1/limits/days/2", CodeInvalidFormat},
		{"limits with a day twice", layer(func(l *RotationLayerInput) { l.Limits.Days[2] = "friday" }),
			"/rotation_layers/1/limits/days/2", CodeDuplicate},
		{"limits with an end alone", layer(func(l *RotationLayerInput) { l.Limits.Start = "" }),
			"/rotation_layers/1/limits/start", CodeRequired},
		{"limits with a start alone", layer(func(l *RotationLayerInput) { l.Limits.End = "" }),
			"/rotation_layers/1/limits/end", CodeRequired},
		{"limits with a malformed start", layer(func(l *RotationLayerInput) { l.Limits.Start = "7:00" }),
			"/rotation_layers/1/limits/start", CodeInvalidFormat},
		{"limits with a malformed end", layer(func(l *RotationLayerInput) { l.Limits.End = "7:00" }),
			"/rotation_layers/1/limits/end", CodeInvalidFormat},
		{"a window whose end equals its start", layer(func(l *RotationLayerInput) { l.Limits.End = "11:00" }),
			"/rotation_layers/1/limits/end", CodeOutOfRange},
		{"a start date before 2000", layer(func(l *RotationLayerInput) { l.StartDate = date("1999-12-31") }),
			"/rotation_layers/1/start_date", CodeOutOfRange},
		{"a start date of the year 1", layer(func(l *RotationLayerInput) { l.StartDate = date("0001-01-01") }),
			"/rotation_layers/1/start_date", CodeOutOfRange},
		{"a start date after 2100", layer(func(l *RotationLayerInput) { l.StartDate = date("2101-01-01") }),
			"/rotation_layers/1/start_date", CodeOutOfRange},
		{"101 people", layer(func(l *RotationLayerInput) {
			for len(l.UserIDs) <= MaxRotationLayerUsers {
				l.UserIDs = append(l.UserIDs, petr.PublicID)
			}
		}), "/rotation_layers/1/user_ids", CodeOutOfRange},
		{"21 Rotation layers", func() Input {
			in := mainInput()
			for len(in.RotationLayers) <= MaxRotationLayers {
				in.RotationLayers = append(in.RotationLayers, mainInput().RotationLayers[0])
			}
			return in
		}(), "/rotation_layers", CodeOutOfRange},
		{"more than 28 days", layer(func(l *RotationLayerInput) {
			l.ShiftLength, l.HandoverDay, l.ShiftSeconds = ShiftHours, "", ptr(MaxShiftSeconds+3600)
		}), "/rotation_layers/1/shift_seconds", CodeOutOfRange},
		{"a layer twice", func() Input {
			in := mainInput()
			in.RotationLayers[0].ID, in.RotationLayers[1].ID = "RY0000000000AA", "ry0000000000aa"
			return in
		}(), "/rotation_layers/1/id", CodeDuplicate},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := check(c.in)
			f := fieldError(t, err)
			if f.Pointer != c.pointer || f.Code != c.code || f.Error() == "" {
				t.Errorf("check = %+v, want %s %s", f, c.pointer, c.code)
			}
		})
	}
	in, err := check(layer(func(l *RotationLayerInput) { l.Limits = &Limits{Days: []string{"sunday", "monday"}} }))
	if err != nil || !slices.Equal(in.RotationLayers[1].Limits.Days, []string{"monday", "sunday"}) {
		t.Errorf("limits of days alone = %+v, %v", in.RotationLayers[1].Limits, err)
	}
	if _, err := check(layer(func(l *RotationLayerInput) { l.Limits.End = "09:00"; l.Limits.Start = "19:00" })); err != nil {
		t.Errorf("a window across midnight = %v", err)
	}
	for _, seconds := range []int{43200, MaxShiftSeconds} {
		if _, err := check(layer(func(l *RotationLayerInput) {
			l.ShiftLength, l.HandoverDay, l.ShiftSeconds = ShiftHours, "", ptr(seconds)
		})); err != nil {
			t.Errorf("%d seconds = %v", seconds, err)
		}
	}
	for _, d := range []string{"2000-01-01", "2100-12-31"} {
		if _, err := check(layer(func(l *RotationLayerInput) { l.StartDate = date(d) })); err != nil {
			t.Errorf("the start date %s = %v", d, err)
		}
	}
}

// TestCreate is C-27.FR-1, FR-13 and C-25.FR-1: a Schedule of a Team with its Rotation layers, the Organization's time
// zone when none is given (P-77), main as the first of its Team (P-90) and not as a later one, a name unique in the
// Team (name_taken), recorded as schedule.created with its hint and exported as muster_schedule_info.
func TestCreate(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Main || sc.TimeZone != "Europe/Berlin" || sc.Team.PublicID != teamPay || len(sc.RotationLayers) != 2 ||
		sc.RotationLayers[0].Number != 1 || sc.RotationLayers[1].Number != 2 || len(sc.RotationLayers[0].Users) != 3 ||
		sc.RotationLayers[0].Users[2].Login != "olga" || sc.RotationLayers[1].Limits == nil ||
		!slices.Equal(sc.RotationLayers[1].Limits.Days, []string{"monday", "tuesday", "wednesday", "thursday", "friday"}) ||
		sc.Version != 1 || !sc.CreatedAt.Equal(t0) || !strings.HasPrefix(sc.PublicID, "SC") ||
		!strings.HasPrefix(sc.RotationLayers[0].PublicID, "RY") {
		t.Fatalf("created %+v", sc)
	}
	if len(store.audit) != 1 || store.audit[0].Action != ActionCreated ||
		!strings.Contains(string(store.audit[0].Diff), `"pointer":"/rotation_layers"`) ||
		store.hints[0] != (db.Hint{OrgID: orgID, Type: Hint, ID: sc.PublicID}) {
		t.Errorf("audit %+v hints %+v", store.audit, store.hints)
	}
	if got := infoSeries(t, sc.PublicID); got != `muster_schedule_info{schedule="`+sc.PublicID+`",name="Main"} 1` {
		t.Errorf("info %q", got)
	}
	nights := Input{TeamID: teamPay, Name: "Nights", RotationLayers: []RotationLayerInput{{Name: "Friday night",
		UserIDs: []string{olga.PublicID}, ShiftLength: ShiftDay, HandoverTime: "10:00", StartDate: date("2026-11-02"),
		Limits: &Limits{Days: []string{"friday"}, Start: "19:00", End: "09:00"}}}}
	second, err := svc.Create(ctx, admin, nights)
	if err != nil || second.Main || second.TimeZone != "Europe/Berlin" {
		t.Errorf("a second schedule = %+v, %v", second, err)
	}
	store.zone = "UTC"
	if _, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: "main"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a name taken = %v", err)
	}
	ops, err := svc.Create(ctx, admin, Input{TeamID: teamOps, Name: "Main"})
	if err != nil || !ops.Main || ops.TimeZone != "UTC" || len(ops.RotationLayers) != 0 {
		t.Errorf("the same name in another team = %+v, %v", ops, err)
	}
	for _, c := range []struct {
		name          string
		in            Input
		pointer, code string
	}{
		{"an unknown team", Input{TeamID: "TM00000000NONE", Name: "x"}, "/team_id", CodeUnknownID},
		{"a team id that is not one", Input{TeamID: "SR0000000000AA", Name: "x"}, "/team_id", CodeUnknownID},
		{"main false on the first", Input{TeamID: "TM00000000PAY1", Name: "x", Main: ptr(false)}, "", ""},
	} {
		_ = c
	}
	store.teams = append(store.teams, teamRec{3, "TM00000000NEW1", "New", "new"})
	store.inTeam[3] = store.inTeam[1]
	f := fieldError(t, func() error {
		_, err := svc.Create(ctx, admin, Input{TeamID: "TM00000000NEW1", Name: "x", Main: ptr(false)})
		return err
	}())
	if f.Pointer != "/main" || f.Code != CodeUnsupported {
		t.Errorf("main false on the first = %+v", f)
	}
	for _, id := range []string{"TM00000000NONE", "SR0000000000AA"} {
		f := fieldError(t, func() error { _, err := svc.Create(ctx, admin, Input{TeamID: id, Name: "x"}); return err }())
		if f.Pointer != "/team_id" || f.Code != CodeUnknownID {
			t.Errorf("team %s = %+v", id, f)
		}
	}
	if _, err := svc.Create(ctx, admin, Input{Name: ""}); err == nil {
		t.Error("a schedule without a name was created")
	}
	for _, name := range []string{"LockScheduleTeam", "CountTeamSchedules", "GetOrganizationTimeZone", "InsertSchedule",
		"InsertRotationLayer", "InsertRotationLayerUsers", "GetSchedule", "InsertAuditEntry", "Notify", "LookupUsers",
		"ListPageable"} {
		store.fail[name] = errBoom
		if _, err := svc.Create(ctx, admin, Input{TeamID: "TM00000000NEW1", Name: "Fails " + name,
			RotationLayers: mainInput().RotationLayers}); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
}

// TestCreateMain is C-27.FR-1 and FR-12: main true on a later Schedule makes it its Team's main one and the previous
// one not main, each recorded in the Audit log.
func TestCreateMain(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	first, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: "Second", Main: ptr(true)})
	if err != nil || !second.Main {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if again, _ := svc.Get(ctx, first.PublicID); again.Main || again.Version != 2 {
		t.Errorf("the first after = %+v", again)
	}
	if len(store.audit) != 3 || store.audit[1].Action != ActionUpdated ||
		store.audit[1].ResourcePublicID.String != first.PublicID ||
		string(store.audit[1].Diff) != `[{"pointer":"/main","before":true,"after":false}]` ||
		store.audit[2].Action != ActionCreated {
		t.Errorf("audit %+v", store.audit)
	}
	store.fail["ClearMainSchedule"] = errBoom
	if _, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: "Third", Main: ptr(true)}); !errors.Is(err,
		errBoom) {
		t.Errorf("the clearing fails = %v", err)
	}
	delete(store.fail, "ClearMainSchedule")
	for _, name := range []string{"InsertAuditEntry", "Notify"} {
		store.fail[name] = errBoom
		if _, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: "Fourth", Main: ptr(true)}); !errors.Is(err,
			errBoom) {
			t.Errorf("%s of the clearing fails = %v", name, err)
		}
		delete(store.fail, name)
	}
}

// TestNotPageable is C-27.FR-2 and AC-3 with settled 6: a save that adds a Viewer, a disabled User or a User who is not
// in the Team to a Rotation layer is refused with not_pageable at the person's pointer and the reason; a person the
// stored version keeps — disabled since — does not refuse it, so a rename still saves; a Rotation layer id the
// Schedule does not have is unknown_id.
func TestNotPageable(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	for _, c := range []struct {
		who    Person
		reason string
	}{{vic, ReasonViewer}, {dan, ReasonDisabled}, {bob, ReasonNotInTeam}} {
		in := mainInput()
		in.RotationLayers[1].UserIDs = append(in.RotationLayers[1].UserIDs, c.who.PublicID)
		f := fieldError(t, func() error { _, err := svc.Create(ctx, admin, in); return err }())
		if f.Pointer != "/rotation_layers/1/user_ids/2" || f.Code != CodeNotPageable || f.Reason != c.reason {
			t.Errorf("%s = %+v", c.who.Login, f)
		}
	}
	in := mainInput()
	in.RotationLayers[0].UserIDs[1] = "SR00000000GONE"
	if f := fieldError(t, func() error { _, err := svc.Create(ctx, admin, in); return err }()); f.Pointer !=
		"/rotation_layers/0/user_ids/1" || f.Code != CodeUnknownID {
		t.Errorf("an unknown user = %+v", f)
	}
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	store.users[0].Status = "disabled" // petr, in both layers
	rename := inputOf(sc)
	rename.Name = "Main renamed"
	renamed, err := svc.Update(ctx, admin, sc.PublicID, nil, rename)
	if err != nil || renamed.Name != "Main renamed" || renamed.RotationLayers[0].Users[0].Status != "disabled" {
		t.Fatalf("a rename with a person disabled since = %+v, %v", renamed, err)
	}
	moved := inputOf(renamed)
	moved.RotationLayers[1].UserIDs = append(moved.RotationLayers[1].UserIDs, sergey.PublicID)
	moved.RotationLayers = append(moved.RotationLayers, RotationLayerInput{Name: "new", UserIDs: []string{petr.PublicID},
		ShiftLength: ShiftDay, HandoverTime: "09:00", StartDate: date("2026-11-02")})
	f := fieldError(t, func() error { _, err := svc.Update(ctx, admin, sc.PublicID, nil, moved); return err }())
	if f.Pointer != "/rotation_layers/2/user_ids/0" || f.Reason != ReasonDisabled {
		t.Errorf("petr added to a new layer = %+v", f)
	}
	store.users[2].Status = "deleted" // olga, kept in both layers, deleted since
	again := inputOf(renamed)
	again.Name = "Main renamed again"
	if got, err := svc.Update(ctx, admin, sc.PublicID, nil, again); err != nil ||
		got.RotationLayers[1].Users[0].Status != "deleted" {
		t.Errorf("a rename with a person deleted since = %+v, %v", got, err)
	}
	added := inputOf(renamed)
	added.RotationLayers = append(added.RotationLayers, RotationLayerInput{Name: "another", UserIDs: []string{olga.PublicID},
		ShiftLength: ShiftDay, HandoverTime: "09:00", StartDate: date("2026-11-02")})
	if f := fieldError(t, func() error { _, err := svc.Update(ctx, admin, sc.PublicID, nil, added); return err }()); f.Pointer != "/rotation_layers/2/user_ids/0" || f.Code != CodeUnknownID {
		t.Errorf("a deleted person added to a new layer = %+v", f)
	}
	store.users[2].Status = "active"
	foreign := inputOf(renamed)
	foreign.RotationLayers[0].ID = "RY00000000NONE"
	if f := fieldError(t, func() error { _, err := svc.Update(ctx, admin, sc.PublicID, nil, foreign); return err }()); f.Pointer != "/rotation_layers/0/id" || f.Code != CodeUnknownID {
		t.Errorf("a layer of another schedule = %+v", f)
	}
}

// TestUpdate is C-27.FR-1, FR-2 and FR-13: If-Match, the Team that stays (settled 4), the main Schedule that cannot be
// unmarked (settled 5), main true on another one, the time zone kept when omitted, Rotation layers kept by id,
// reordered, added and removed, a save that changes nothing, and the refusals of the queries.
func TestUpdate(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: "Second line"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, admin, sc.PublicID, ptr(int64(5)), inputOf(sc)); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	audits := len(store.audit)
	same, err := svc.Update(ctx, admin, sc.PublicID, ptr(int64(1)), inputOf(sc))
	if err != nil || same.Version != 1 || len(store.audit) != audits {
		t.Errorf("no change = %+v, %v", same, err)
	}
	moved := inputOf(sc)
	moved.TeamID = teamOps
	if f := fieldError(t, func() error { _, err := svc.Update(ctx, admin, sc.PublicID, nil, moved); return err }()); f.Pointer != "/team_id" || f.Code != CodeUnsupported {
		t.Errorf("another team = %+v", f)
	}
	unmark := inputOf(sc)
	unmark.Main = ptr(false)
	if f := fieldError(t, func() error { _, err := svc.Update(ctx, admin, sc.PublicID, nil, unmark); return err }()); f.Pointer != "/main" || f.Code != CodeUnsupported {
		t.Errorf("unmarking main = %+v", f)
	}
	in := inputOf(sc)
	in.TimeZone = nil
	in.Name = "Main"
	in.RotationLayers = []RotationLayerInput{in.RotationLayers[1], {Name: "Twelve hours", UserIDs: []string{sergey.PublicID, olga.PublicID},
		ShiftLength: ShiftHours, ShiftSeconds: ptr(43200), HandoverTime: "09:00", StartDate: date("2026-11-02")}}
	in.RotationLayers[0].Name = "Working hours"
	updated, err := svc.Update(ctx, admin, sc.PublicID, ptr(int64(1)), in)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.TimeZone != "Europe/Berlin" || len(updated.RotationLayers) != 2 ||
		updated.RotationLayers[0].PublicID != sc.RotationLayers[1].PublicID || updated.RotationLayers[0].Number != 1 ||
		updated.RotationLayers[0].Name != "Working hours" || updated.RotationLayers[1].ShiftSeconds != 43200 ||
		updated.RotationLayers[1].PublicID == sc.RotationLayers[0].PublicID || len(store.layers) != 2 {
		t.Errorf("updated %+v", updated)
	}
	if e := store.audit[len(store.audit)-1]; e.Action != ActionUpdated ||
		!strings.Contains(string(e.Diff), `"pointer":"/rotation_layers"`) {
		t.Errorf("audit %+v", e)
	}
	mark := inputOf(other)
	mark.Main = ptr(true)
	promoted, err := svc.Update(ctx, admin, other.PublicID, nil, mark)
	if err != nil || !promoted.Main {
		t.Fatalf("main true = %+v, %v", promoted, err)
	}
	if was, _ := svc.Get(ctx, sc.PublicID); was.Main {
		t.Error("the previous main schedule is still main")
	}
	clash := inputOf(promoted)
	clash.Name = "MAIN"
	if _, err := svc.Update(ctx, admin, other.PublicID, nil, clash); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a name taken = %v", err)
	}
	if _, err := svc.Update(ctx, admin, "SC00000000NONE", nil, inputOf(sc)); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown = %v", err)
	}
	if _, err := svc.Update(ctx, admin, "x", nil, inputOf(sc)); !errors.Is(err, ErrNotFound) {
		t.Errorf("malformed = %v", err)
	}
	if _, err := svc.Update(ctx, admin, sc.PublicID, nil, Input{}); err == nil {
		t.Error("an invalid input was saved")
	}
	rename := inputOf(updated)
	for _, name := range []string{"FindScheduleTeam", "LockScheduleTeamByID", "LockSchedule", "UpdateSchedule",
		"DeleteRotationLayers", "UpdateRotationLayer", "DeleteRotationLayerUsers", "ListRotationLayers",
		"ListRotationLayerUsers", "InsertAuditEntry", "Notify", "ClearMainSchedule"} {
		store.fail[name] = errBoom
		rename.Name = "Renamed by " + name
		rename.Main = ptr(true)
		if _, err := svc.Update(ctx, admin, sc.PublicID, nil, rename); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	if got := infoSeries(t, sc.PublicID); !strings.Contains(got, `name="Main"`) {
		t.Errorf("info after failures %q", got)
	}
	rename.Name, rename.Main = "Renamed", nil
	if _, err := svc.Update(ctx, admin, sc.PublicID, nil, rename); err != nil {
		t.Fatal(err)
	}
	if got := infoSeries(t, sc.PublicID); got != `muster_schedule_info{schedule="`+sc.PublicID+`",name="Renamed"} 1` {
		t.Errorf("info after a rename %q", got)
	}
}

// TestGetAndList is C-27.FR-1 and C-25.FR-2: the Schedules by name with their Team and Rotation layers, by Team, by
// the Teams of a User, matching q, page by page.
func TestGetAndList(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []Input{{TeamID: teamPay, Name: "nights"}, {TeamID: teamOps, Name: "Alpha"}} {
		if _, err := svc.Create(ctx, admin, in); err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.Get(ctx, strings.ToLower(sc.PublicID))
	if err != nil || got.PublicID != sc.PublicID || len(got.RotationLayers) != 2 || got.Team.Name != "Payments" {
		t.Errorf("get = %+v, %v", got, err)
	}
	for _, id := range []string{"SC00000000NONE", "TM00000000PAY1"} {
		if _, err := svc.Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("get %s = %v", id, err)
		}
	}
	names := func(p Page) string {
		var out []string
		for _, s := range p.Schedules {
			out = append(out, s.Name)
		}
		return strings.Join(out, ",")
	}
	page, err := svc.List(ctx, ListFilter{Limit: 2})
	if err != nil || names(page) != "Alpha,Main" || page.Next == nil || page.Next.Name != "main" ||
		len(page.Schedules[1].RotationLayers) != 2 {
		t.Fatalf("first page = %s %+v, %v", names(page), page.Next, err)
	}
	if page, err = svc.List(ctx, ListFilter{Limit: 2, After: page.Next}); err != nil || names(page) != "nights" ||
		page.Next != nil {
		t.Errorf("second page = %s, %v", names(page), err)
	}
	for _, c := range []struct {
		f    ListFilter
		want string
	}{
		{ListFilter{Team: strings.ToLower(teamOps), Limit: 50}, "Alpha"},
		{ListFilter{Team: "nope", Limit: 50}, ""},
		{ListFilter{MemberID: petr.ID, Limit: 50}, "Main,nights"},
		{ListFilter{Query: " IGH ", Limit: 50}, "nights"},
	} {
		if page, err := svc.List(ctx, c.f); err != nil || names(page) != c.want {
			t.Errorf("list %+v = %s, %v", c.f, names(page), err)
		}
	}
	for _, name := range []string{"ListSchedules", "ListRotationLayers", "ListRotationLayerUsers"} {
		store.fail[name] = errBoom
		if _, err := svc.List(ctx, ListFilter{Limit: 50}); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		if _, err := svc.Get(ctx, sc.PublicID); name != "ListSchedules" && !errors.Is(err, errBoom) {
			t.Errorf("get when %s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	store.fail["GetSchedule"] = errBoom
	if _, err := svc.Get(ctx, sc.PublicID); !errors.Is(err, errBoom) {
		t.Errorf("get fails = %v", err)
	}
}

// TestDelete is C-27.FR-12 with P-66: deleting the main Schedule leaves the Team without one, recorded as
// schedule.deleted, with the info series gone; a stale If-Match is refused, and a Schedule that Escalation steps
// name would be refused with schedule_in_use.
func TestDelete(t *testing.T) {
	svc, store, _ := newService(t)
	ctx := t.Context()
	sc, err := svc.Create(ctx, admin, mainInput())
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Create(ctx, admin, Input{TeamID: teamPay, Name: "Nights"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, admin, sc.PublicID, ptr(int64(3))); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	for _, name := range []string{"DeleteSchedule", "InsertAuditEntry", "Notify"} {
		store.fail[name] = errBoom
		if err := svc.Delete(ctx, admin, sc.PublicID, nil); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
	if err := svc.Delete(ctx, admin, sc.PublicID, ptr(int64(1))); err != nil {
		t.Fatal(err)
	}
	if len(store.schedules) != 1 || store.schedules[0].main || len(store.layers) != 0 {
		t.Errorf("after the deletion %+v %+v", store.schedules, store.layers)
	}
	e := store.audit[len(store.audit)-1]
	if e.Action != ActionDeleted || !strings.Contains(string(e.Diff), `{"pointer":"/main","before":true}`) {
		t.Errorf("audit %+v %s", e, e.Diff)
	}
	if got := infoSeries(t, sc.PublicID); got != "" {
		t.Errorf("info of a deleted schedule %q", got)
	}
	if err := svc.Delete(ctx, admin, sc.PublicID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted again = %v", err)
	}
	if refs, err := svc.Referrers(ctx, store, other); err != nil || len(refs) != 0 {
		t.Errorf("referrers = %v, %v", refs, err)
	}
	use := &InUseError{Referrers: []Referrer{{Type: "escalation_chain", PublicID: "EC0000000000AA", Name: "Pay"}}}
	if !errors.Is(use, ErrInUse) || use.Error() != ErrInUse.Error() {
		t.Errorf("InUseError %v", use)
	}
}

// TestInfo is C-27.FR-13: muster_schedule_info lists exactly the Schedules the database has after a refresh, which
// InfoChanged and the ticks of RunInfo start.
func TestInfo(t *testing.T) {
	svc, store, _ := newService(t)
	store.schedules = []*scheduleRec{{id: 1, team: 1, publicID: "SC0000000INFO1", name: "One"}}
	svc.exportInfo("SC0000000INFO2", "Two")
	svc.exportInfo("SC0000000INFO1", "Old")
	if err := svc.RefreshInfo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if infoSeries(t, "SC0000000INFO2") != "" || infoSeries(t, "SC0000000INFO1") !=
		`muster_schedule_info{schedule="SC0000000INFO1",name="One"} 1` {
		t.Errorf("after refresh %q %q", infoSeries(t, "SC0000000INFO1"), infoSeries(t, "SC0000000INFO2"))
	}
	store.fail["ListScheduleInfo"] = errBoom
	if err := svc.RefreshInfo(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("refresh fails = %v", err)
	}
	delete(store.fail, "ListScheduleInfo")
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	store.schedules = append(store.schedules, &scheduleRec{id: 2, team: 1, publicID: "SC0000000INFO3", name: "Three"})
	go func() {
		svc.RunInfo(ctx, ticks)
		close(done)
	}()
	svc.InfoChanged()
	svc.InfoChanged()
	ticks <- t0
	cancel()
	<-done
	if infoSeries(t, "SC0000000INFO3") == "" {
		t.Error("RunInfo did not refresh")
	}
	svc.unexportInfo("SC0000000INFO1")
	svc.unexportInfo("SC0000000INFO3")
	svc.unexportInfo("SC000000000NONE")
}
