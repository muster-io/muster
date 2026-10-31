// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package schedules holds the Schedules of C-27: a Team's plans of who is On-call and when, each with a name unique in
// the Team, a time zone and its Rotation layers, with Overrides on top. One Schedule of a Team is its main Schedule,
// whose On-call is the Team's. The package computes the Shifts of the Rotation layers and the Result of a Schedule —
// its segments, each with the person On-call or nobody in a gap — on the business clock, skipping the people who may
// not be Paged. Every change is recorded in the Audit log and announced with the live hint schedule; nothing is sent
// to a messenger (C-27.FR-11). The package exports muster_schedule_info.
package schedules

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/schedules/dbgen"
)

// The Audit log actions and resource types, and the live hint, of Schedules and Overrides (C-27.FR-6, FR-13).
const (
	ActionCreated         = "schedule.created"
	ActionUpdated         = "schedule.updated"
	ActionDeleted         = "schedule.deleted"
	ActionOverrideCreated = "schedule_override.created"
	ActionOverrideUpdated = "schedule_override.updated"
	ActionOverrideDeleted = "schedule_override.deleted"
	ResourceSchedule      = "schedule"
	ResourceOverride      = "schedule_override"
	Hint                  = "schedule"
)

// The settings of Schedules that bound the periods it reads.
const (
	// GapView is schedule.gap_view, the default period of the On-call.
	GapView = 14 * 24 * time.Hour
	// ICalWindowBack and ICalWindowAhead are ical.window (P-81): the iCal feed reaches from 7 days back to 60 days
	// ahead.
	ICalWindowBack  = 7 * 24 * time.Hour
	ICalWindowAhead = 60 * 24 * time.Hour
	// MaxPeriod is the length of ical.window: the longest period of the On-call.
	MaxPeriod = ICalWindowBack + ICalWindowAhead
	// Reach is how far from the business clock's now a period of the On-call may start or end.
	Reach = 10 * 365 * 24 * time.Hour
	// MaxShiftSeconds is the longest Shift in hours: 28 days.
	MaxShiftSeconds = 28 * 24 * 60 * 60
	// MaxRotationLayers and MaxRotationLayerUsers are the most Rotation layers of a Schedule and people of a Rotation
	// layer.
	MaxRotationLayers     = 20
	MaxRotationLayerUsers = 100
	// OverrideGrace is how far before the business clock's now an Override may start: the time it takes to fill the
	// form.
	OverrideGrace = 5 * time.Minute
	// MaxOverrideLength is the longest Override: 31 days.
	MaxOverrideLength = 31 * 24 * time.Hour
	// MaxOverlappingOverrides is how many Overrides of a Schedule a new span may overlap, short of one more.
	MaxOverlappingOverrides = 1000
)

// The first and the last start date of a Rotation layer.
var (
	FirstStartDate = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	LastStartDate  = time.Date(2100, 12, 31, 0, 0, 0, 0, time.UTC)
)

// The limits of a Schedule, in characters.
const (
	MaxNameLength     = 200
	maxTimeZoneLength = 64
)

// The codes of FieldError, as the API names them.
const (
	CodeRequired      = "required"
	CodeInvalidFormat = "invalid_format"
	CodeTooLong       = "too_long"
	CodeOutOfRange    = "out_of_range"
	CodeDuplicate     = "duplicate"
	CodeUnknownID     = "unknown_id"
	CodeUnsupported   = "unsupported"
	CodeNotPageable   = "not_pageable"
)

const (
	// uniqueViolation is the SQLSTATE of a unique index that refused a row; nameKey is the index of the names of a
	// Team's Schedules.
	uniqueViolation = "23505"
	nameKey         = "schedules_name_key"
)

// timeOfDay is HH:MM on the 24-hour clock, the pattern of the API and of the CHECKs.
var timeOfDay = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

var (
	// ErrNotFound is a Schedule that does not exist in the Organization.
	ErrNotFound = errors.New("no such schedule")
	// ErrOverrideNotFound is an Override that the Schedule does not have.
	ErrOverrideNotFound = errors.New("no such override")
	// ErrNameTaken is a name another Schedule of the Team has, ignoring case.
	ErrNameTaken = errors.New("another schedule of the team has this name")
	// ErrVersionMismatch is an If-Match that names another version.
	ErrVersionMismatch = errors.New("the schedule or override changed since it was read")
	// ErrNotInTeam is an Override by someone who is neither in the Schedule's Team nor an Admin (not_in_team).
	ErrNotInTeam = errors.New("the caller is not in the team of the schedule")
	// ErrOverrideEnded is a change or deletion of an Override that has ended (override_ended).
	ErrOverrideEnded = errors.New("the override has ended")
	// ErrInUse is the deletion of a Schedule that an Escalation step names (schedule_in_use); InUseError names them.
	ErrInUse = errors.New("escalation steps name the schedule")
)

// FieldError is a field of a request that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem; Reason says why a person may not be Paged with not_pageable.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
	Reason  string
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// Referrer is an object that holds a Schedule: its kind, public_id and name.
type Referrer struct {
	Type     string
	PublicID string
	Name     string
}

// InUseError is ErrInUse with the objects that name the Schedule.
type InUseError struct {
	Referrers []Referrer
}

func (e *InUseError) Error() string { return ErrInUse.Error() }

func (e *InUseError) Unwrap() error { return ErrInUse }

// TeamRef names the Team of a Schedule.
type TeamRef struct {
	ID       int64
	PublicID string
	Name     string
	Slug     string
	Deleted  bool
}

// Schedule is a Schedule as the API reads it, with its Rotation layers from the lowest.
type Schedule struct {
	ID             int64
	PublicID       string
	Name           string
	TimeZone       string
	Main           bool
	Team           TeamRef
	RotationLayers []RotationLayer
	CreatedAt      time.Time
	Version        int64
}

// Input is what createSchedule and updateSchedule write. A nil TimeZone is the Organization's on a creation and keeps
// the stored one on an update; a nil Main is false on a creation, unless the Team has no Schedule, and keeps the
// stored value on an update.
type Input struct {
	TeamID         string
	Name           string
	TimeZone       *string
	Main           *bool
	RotationLayers []RotationLayerInput
}

// RotationLayerInput is a Rotation layer of an Input; an ID names one of the Schedule's, which keeps its identity.
type RotationLayerInput struct {
	ID           string
	Name         string
	UserIDs      []string
	ShiftLength  string
	ShiftSeconds *int
	HandoverTime string
	HandoverDay  string
	// StartDate is a calendar date, as midnight UTC.
	StartDate time.Time
	Limits    *Limits
}

// ListFilter selects a page of Schedules by name: of the Team Team, or of the Teams of the User MemberID, when set,
// matching Query, after the position After.
type ListFilter struct {
	Team     string
	MemberID int64
	Query    string
	After    *Position
	Limit    int
}

// Position is the place of a Schedule in the list by name: its lowered name and its id.
type Position struct {
	Name string `json:"n"`
	ID   int64  `json:"i"`
}

// Page is a page of Schedules; Next, the position to continue after, is nil on the last page.
type Page struct {
	Schedules []Schedule
	Next      *Position
}

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, the client address,
// and whether the caller's Role is Admin, which the precondition of Overrides reads.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
	Admin     bool
}

// Queries are the queries of the package, with the insert of the Audit log and the live hint.
type Queries interface {
	GetOrganizationTimeZone(ctx context.Context, orgID int64) (string, error)
	LockScheduleTeam(ctx context.Context, arg dbgen.LockScheduleTeamParams) (dbgen.LockScheduleTeamRow, error)
	LockScheduleTeamByID(ctx context.Context, arg dbgen.LockScheduleTeamByIDParams) (dbgen.LockScheduleTeamByIDRow,
		error)
	CountTeamSchedules(ctx context.Context, arg dbgen.CountTeamSchedulesParams) (int64, error)
	ListSchedules(ctx context.Context, arg dbgen.ListSchedulesParams) ([]dbgen.ListSchedulesRow, error)
	GetSchedule(ctx context.Context, arg dbgen.GetScheduleParams) (dbgen.GetScheduleRow, error)
	FindScheduleTeam(ctx context.Context, arg dbgen.FindScheduleTeamParams) (int64, error)
	LockSchedule(ctx context.Context, arg dbgen.LockScheduleParams) (int64, error)
	InsertSchedule(ctx context.Context, arg dbgen.InsertScheduleParams) (int64, error)
	UpdateSchedule(ctx context.Context, arg dbgen.UpdateScheduleParams) error
	ClearMainSchedule(ctx context.Context, arg dbgen.ClearMainScheduleParams) ([]dbgen.ClearMainScheduleRow, error)
	DeleteSchedule(ctx context.Context, arg dbgen.DeleteScheduleParams) error
	ListRotationLayers(ctx context.Context, arg dbgen.ListRotationLayersParams) ([]dbgen.ListRotationLayersRow, error)
	ListRotationLayerUsers(ctx context.Context, arg dbgen.ListRotationLayerUsersParams) (
		[]dbgen.ListRotationLayerUsersRow, error)
	InsertRotationLayer(ctx context.Context, arg dbgen.InsertRotationLayerParams) (int64, error)
	UpdateRotationLayer(ctx context.Context, arg dbgen.UpdateRotationLayerParams) error
	DeleteRotationLayers(ctx context.Context, arg dbgen.DeleteRotationLayersParams) error
	DeleteRotationLayerUsers(ctx context.Context, arg dbgen.DeleteRotationLayerUsersParams) error
	InsertRotationLayerUsers(ctx context.Context, arg dbgen.InsertRotationLayerUsersParams) error
	LookupUsers(ctx context.Context, arg dbgen.LookupUsersParams) ([]dbgen.LookupUsersRow, error)
	ListPageable(ctx context.Context, arg dbgen.ListPageableParams) ([]dbgen.ListPageableRow, error)
	IsTeamUser(ctx context.Context, arg dbgen.IsTeamUserParams) (bool, error)
	ListScheduleOverrides(ctx context.Context, arg dbgen.ListScheduleOverridesParams) (
		[]dbgen.ListScheduleOverridesRow, error)
	ListPeriodOverrides(ctx context.Context, arg dbgen.ListPeriodOverridesParams) ([]dbgen.ListPeriodOverridesRow,
		error)
	GetScheduleOverride(ctx context.Context, arg dbgen.GetScheduleOverrideParams) (dbgen.GetScheduleOverrideRow, error)
	CountOverlappingOverrides(ctx context.Context, arg dbgen.CountOverlappingOverridesParams) (int64, error)
	LockScheduleOverride(ctx context.Context, arg dbgen.LockScheduleOverrideParams) (int64, error)
	InsertScheduleOverride(ctx context.Context, arg dbgen.InsertScheduleOverrideParams) (int64, error)
	UpdateScheduleOverride(ctx context.Context, arg dbgen.UpdateScheduleOverrideParams) error
	DeleteScheduleOverride(ctx context.Context, arg dbgen.DeleteScheduleOverrideParams) error
	ListScheduleInfo(ctx context.Context, orgID int64) ([]dbgen.ListScheduleInfoRow, error)
	audit.Store
	Notify(ctx context.Context, h db.Hint) error
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: newQueries(pool), pool: pool}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
	exec dbgen.DBTX
}

func newQueries(d dbgen.DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d), exec: d}
}

func (q pgQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.exec, h)
}

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(newQueries(tx))
	})
}

// Config is what a Service needs: the Organization, the Store, the Audit log writer and the business clock, which
// dates the rows and is the only time the On-call reads (C-27.FR-4).
type Config struct {
	OrgID    int64
	Store    Store
	Audit    *audit.Writer
	Business clock.Clock
}

// Service holds the Schedules of the Organization.
type Service struct {
	orgID int64
	store Store
	audit *audit.Writer
	clock clock.Clock

	// info holds the muster_schedule_info series this replica exports, by public_id to name.
	infoMu sync.Mutex
	info   map[string]string
	// infoChanged wakes RunInfo.
	infoChanged chan struct{}
}

// New returns the Service of the Organization in cfg.
func New(cfg Config) *Service {
	return &Service{orgID: cfg.OrgID, store: cfg.Store, audit: cfg.Audit, clock: cfg.Business, info: map[string]string{},
		infoChanged: make(chan struct{}, 1)}
}

// List lists a page of the Schedules by name.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := min(max(f.Limit, 1), 500)
	p := dbgen.ListSchedulesParams{OrgID: s.orgID, PageSize: int32(limit) + 1}
	if f.Team != "" {
		// A value that is not a Team's public_id names no Team, and the query matches nothing.
		p.Team = pgtype.Text{String: f.Team, Valid: true}
		if team, err := publicid.Parse(publicid.Team, f.Team); err == nil {
			p.Team.String = team
		}
	}
	if f.MemberID != 0 {
		p.MemberID = pgtype.Int8{Int64: f.MemberID, Valid: true}
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		p.Q = pgtype.Text{String: q, Valid: true}
	}
	if f.After != nil {
		p.AfterName = pgtype.Text{String: f.After.Name, Valid: true}
		p.AfterID = pgtype.Int8{Int64: f.After.ID, Valid: true}
	}
	rows, err := s.store.ListSchedules(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the schedules: %w", err)
	}
	page := Page{Schedules: make([]Schedule, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			page.Next = &Position{Name: last.SortName, ID: last.ID}
			break
		}
		page.Schedules = append(page.Schedules, Schedule{ID: r.ID, PublicID: r.PublicID, Name: r.Name,
			TimeZone: r.TimeZone, Main: r.Main, CreatedAt: r.CreatedAt.UTC(), Version: r.Version,
			Team: TeamRef{PublicID: r.TeamPublicID, Name: r.TeamName, Slug: r.TeamSlug, Deleted: r.TeamDeleted}})
	}
	if err := s.withLayers(ctx, s.store, page.Schedules); err != nil {
		return Page{}, err
	}
	return page, nil
}

// Get reads the Schedule publicID with its Rotation layers.
func (s *Service) Get(ctx context.Context, publicID string) (Schedule, error) {
	return s.get(ctx, s.store, publicID)
}

func (s *Service) get(ctx context.Context, q Queries, publicID string) (Schedule, error) {
	id, err := publicid.Parse(publicid.Schedule, publicID)
	if err != nil {
		return Schedule{}, ErrNotFound
	}
	r, err := q.GetSchedule(ctx, dbgen.GetScheduleParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("read the schedule %s: %w", id, err)
	}
	list := []Schedule{{ID: r.ID, PublicID: r.PublicID, Name: r.Name, TimeZone: r.TimeZone, Main: r.Main,
		CreatedAt: r.CreatedAt.UTC(), Version: r.Version,
		Team: TeamRef{ID: r.TeamID, PublicID: r.TeamPublicID, Name: r.TeamName, Slug: r.TeamSlug,
			Deleted: r.TeamDeleted}}}
	if err := s.withLayers(ctx, q, list); err != nil {
		return Schedule{}, err
	}
	return list[0], nil
}

// withLayers reads the Rotation layers of the Schedules with their people.
func (s *Service) withLayers(ctx context.Context, q Queries, list []Schedule) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, sc := range list {
		ids[i], index[sc.ID] = sc.ID, i
		list[i].RotationLayers = []RotationLayer{}
	}
	rows, err := q.ListRotationLayers(ctx, dbgen.ListRotationLayersParams{OrgID: s.orgID, ScheduleIds: ids})
	if err != nil {
		return fmt.Errorf("list the rotation layers: %w", err)
	}
	layerIDs := make([]int64, len(rows))
	where := make(map[int64][2]int, len(rows))
	for i, r := range rows {
		layerIDs[i] = r.ID
		at := index[r.ScheduleID]
		where[r.ID] = [2]int{at, len(list[at].RotationLayers)}
		list[at].RotationLayers = append(list[at].RotationLayers, rotationLayerOf(r))
	}
	if len(rows) == 0 {
		return nil
	}
	users, err := q.ListRotationLayerUsers(ctx, dbgen.ListRotationLayerUsersParams{OrgID: s.orgID,
		RotationLayerIds: layerIDs})
	if err != nil {
		return fmt.Errorf("list the people of the rotation layers: %w", err)
	}
	for _, u := range users {
		w := where[u.RotationLayerID]
		l := &list[w[0]].RotationLayers[w[1]]
		l.Users = append(l.Users, Person{ID: u.ID, PublicID: u.PublicID, Name: u.Name, Login: u.Login, Role: u.Role,
			Status: u.Status})
	}
	return nil
}

// rotationLayerOf is the Rotation layer of a row, without its people.
func rotationLayerOf(r dbgen.ListRotationLayersRow) RotationLayer {
	l := RotationLayer{ID: r.ID, PublicID: r.PublicID, Number: int(r.Position), Name: r.Name, Users: []Person{},
		ShiftLength: r.ShiftLength, ShiftSeconds: int(r.ShiftSeconds.Int32), HandoverTime: r.HandoverTime,
		HandoverDay: r.HandoverDay.String, StartDate: r.StartDate.Time}
	if r.LimitDays != nil {
		l.Limits = &Limits{Days: r.LimitDays, Start: r.LimitStart.String, End: r.LimitEnd.String}
	}
	return l
}

// check refuses an Input that is not valid, with the pointer of the field, before any read: the name, the time zone,
// and each Rotation layer. It returns the input normalized.
func check(in Input) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return in, &FieldError{Pointer: "/name", Code: CodeRequired, Detail: "The name is empty."}
	case utf8.RuneCountInString(in.Name) > MaxNameLength:
		return in, &FieldError{Pointer: "/name", Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", MaxNameLength)}
	}
	if in.TimeZone != nil {
		if _, err := location(*in.TimeZone); err != nil {
			return in, &FieldError{Pointer: "/time_zone", Code: CodeInvalidFormat,
				Detail: "The time zone is not an IANA time zone, such as Europe/Berlin."}
		}
	}
	if len(in.RotationLayers) > MaxRotationLayers {
		return in, &FieldError{Pointer: "/rotation_layers", Code: CodeOutOfRange,
			Detail: fmt.Sprintf("A Schedule has at most %d Rotation layers.", MaxRotationLayers)}
	}
	ids := map[string]bool{}
	for i := range in.RotationLayers {
		l, err := checkRotationLayer(in.RotationLayers[i], "/rotation_layers/"+strconv.Itoa(i))
		if err != nil {
			return in, err
		}
		if l.ID != "" {
			if ids[l.ID] {
				return in, &FieldError{Pointer: "/rotation_layers/" + strconv.Itoa(i) + "/id", Code: CodeDuplicate,
					Detail: "The Rotation layer appears twice."}
			}
			ids[l.ID] = true
		}
		in.RotationLayers[i] = l
	}
	return in, nil
}

// location is the IANA time zone name; the empty name and Local are not one.
func location(name string) (*time.Location, error) {
	if name == "" || name == "Local" || len(name) > maxTimeZoneLength {
		return nil, errors.New("not an IANA time zone")
	}
	return time.LoadLocation(name)
}

// checkRotationLayer refuses a Rotation layer that is not valid, at the pointer base.
func checkRotationLayer(l RotationLayerInput, base string) (RotationLayerInput, error) {
	l.Name = strings.TrimSpace(l.Name)
	if l.ID != "" {
		id, err := publicid.Parse(publicid.RotationLayer, l.ID)
		if err != nil {
			return l, &FieldError{Pointer: base + "/id", Code: CodeUnknownID, Detail: "No such Rotation layer."}
		}
		l.ID = id
	}
	switch {
	case l.Name == "":
		return l, &FieldError{Pointer: base + "/name", Code: CodeRequired, Detail: "The name is empty."}
	case utf8.RuneCountInString(l.Name) > MaxNameLength:
		return l, &FieldError{Pointer: base + "/name", Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", MaxNameLength)}
	case len(l.UserIDs) == 0:
		return l, &FieldError{Pointer: base + "/user_ids", Code: CodeRequired,
			Detail: "A Rotation layer needs at least one person."}
	case len(l.UserIDs) > MaxRotationLayerUsers:
		return l, &FieldError{Pointer: base + "/user_ids", Code: CodeOutOfRange,
			Detail: fmt.Sprintf("A Rotation layer has at most %d people.", MaxRotationLayerUsers)}
	case !timeOfDay.MatchString(l.HandoverTime):
		return l, &FieldError{Pointer: base + "/handover_time", Code: CodeInvalidFormat,
			Detail: "The handover time is not a time HH:MM on the 24-hour clock, such as 10:00."}
	}
	for j, u := range l.UserIDs {
		id, err := publicid.Parse(publicid.User, u)
		if err != nil {
			return l, &FieldError{Pointer: base + "/user_ids/" + strconv.Itoa(j), Code: CodeUnknownID,
				Detail: "No such User."}
		}
		l.UserIDs[j] = id
	}
	if l.StartDate.Before(FirstStartDate) || l.StartDate.After(LastStartDate) {
		return l, &FieldError{Pointer: base + "/start_date", Code: CodeOutOfRange,
			Detail: "The start date is from 2000-01-01 to 2100-12-31."}
	}
	if err := checkShift(l, base); err != nil {
		return l, err
	}
	if l.Limits != nil {
		limits, err := checkLimits(*l.Limits, base+"/limits")
		if err != nil {
			return l, err
		}
		l.Limits = &limits
	}
	return l, nil
}

// checkShift refuses a Shift length with the wrong fields: shift_seconds exactly with hours, as whole hours from one
// hour, and handover_day exactly with week.
func checkShift(l RotationLayerInput, base string) error {
	switch l.ShiftLength {
	case ShiftDay, ShiftWeek, ShiftHours:
	default:
		return &FieldError{Pointer: base + "/shift_length", Code: CodeInvalidFormat,
			Detail: "The Shift length is day, week or hours."}
	}
	switch {
	case l.ShiftLength == ShiftHours && l.ShiftSeconds == nil:
		return &FieldError{Pointer: base + "/shift_seconds", Code: CodeRequired,
			Detail: "A Shift length in hours needs shift_seconds."}
	case l.ShiftLength != ShiftHours && l.ShiftSeconds != nil:
		return &FieldError{Pointer: base + "/shift_seconds", Code: CodeUnsupported,
			Detail: "shift_seconds is given only with the Shift length hours."}
	case l.ShiftSeconds != nil && (*l.ShiftSeconds < 3600 || *l.ShiftSeconds%3600 != 0 ||
		*l.ShiftSeconds > MaxShiftSeconds):
		return &FieldError{Pointer: base + "/shift_seconds", Code: CodeOutOfRange,
			Detail: "A Shift lasts whole hours from one hour to 28 days: a multiple of 3600 from 3600 to 2419200."}
	case l.ShiftLength == ShiftWeek && l.HandoverDay == "":
		return &FieldError{Pointer: base + "/handover_day", Code: CodeRequired,
			Detail: "Weekly Shifts need the day of the handover."}
	case l.ShiftLength != ShiftWeek && l.HandoverDay != "":
		return &FieldError{Pointer: base + "/handover_day", Code: CodeUnsupported,
			Detail: "handover_day is given only with weekly Shifts."}
	case l.HandoverDay != "" && !slices.Contains(Weekdays, l.HandoverDay):
		return &FieldError{Pointer: base + "/handover_day", Code: CodeInvalidFormat,
			Detail: "The day is not a day from monday to sunday."}
	}
	return nil
}

// checkLimits refuses limits that are not valid: no day, a day that is not a weekday or is given twice, a start
// without an end or the other way round, a time that is not HH:MM, and an end equal to the start. It returns the
// limits with their days in the order of the week.
func checkLimits(lm Limits, base string) (Limits, error) {
	if len(lm.Days) == 0 {
		return lm, &FieldError{Pointer: base + "/days", Code: CodeRequired, Detail: "The limits need at least one day."}
	}
	seen := map[string]bool{}
	for i, d := range lm.Days {
		pointer := base + "/days/" + strconv.Itoa(i)
		switch {
		case !slices.Contains(Weekdays, d):
			return lm, &FieldError{Pointer: pointer, Code: CodeInvalidFormat,
				Detail: "The day is not a day from monday to sunday."}
		case seen[d]:
			return lm, &FieldError{Pointer: pointer, Code: CodeDuplicate, Detail: "The day appears twice."}
		}
		seen[d] = true
	}
	days := make([]string, 0, len(lm.Days))
	for _, d := range Weekdays {
		if seen[d] {
			days = append(days, d)
		}
	}
	lm.Days = days
	switch {
	case lm.Start == "" && lm.End == "":
		return lm, nil
	case lm.Start == "":
		return lm, &FieldError{Pointer: base + "/start", Code: CodeRequired, Detail: "An end needs a start."}
	case lm.End == "":
		return lm, &FieldError{Pointer: base + "/end", Code: CodeRequired, Detail: "A start needs an end."}
	case !timeOfDay.MatchString(lm.Start):
		return lm, &FieldError{Pointer: base + "/start", Code: CodeInvalidFormat,
			Detail: "The start is not a time HH:MM on the 24-hour clock, such as 11:00."}
	case !timeOfDay.MatchString(lm.End):
		return lm, &FieldError{Pointer: base + "/end", Code: CodeInvalidFormat,
			Detail: "The end is not a time HH:MM on the 24-hour clock, such as 19:00."}
	case lm.End == lm.Start:
		return lm, &FieldError{Pointer: base + "/end", Code: CodeOutOfRange,
			Detail: "The end equals the start; an end earlier than the start runs until the next day."}
	}
	return lm, nil
}

// lockTeam locks the Team publicID, which is not deleted, for a change of its Schedules; an unknown or deleted one is
// an unknown_id FieldError at /team_id.
func (s *Service) lockTeam(ctx context.Context, q Queries, publicID string) (TeamRef, error) {
	unknown := &FieldError{Pointer: "/team_id", Code: CodeUnknownID, Detail: "No such Team."}
	id, err := publicid.Parse(publicid.Team, publicID)
	if err != nil {
		return TeamRef{}, unknown
	}
	r, err := q.LockScheduleTeam(ctx, dbgen.LockScheduleTeamParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return TeamRef{}, unknown
	}
	if err != nil {
		return TeamRef{}, fmt.Errorf("lock the team %s: %w", id, err)
	}
	return TeamRef{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Slug: r.Slug}, nil
}

// lock locks the Team of the Schedule publicID and then the Schedule, the order every change takes, and reads it.
func (s *Service) lock(ctx context.Context, q Queries, publicID string) (Schedule, error) {
	id, err := publicid.Parse(publicid.Schedule, publicID)
	if err != nil {
		return Schedule{}, ErrNotFound
	}
	team, err := q.FindScheduleTeam(ctx, dbgen.FindScheduleTeamParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("read the team of the schedule %s: %w", id, err)
	}
	if _, err := q.LockScheduleTeamByID(ctx, dbgen.LockScheduleTeamByIDParams{OrgID: s.orgID, ID: team}); err != nil {
		return Schedule{}, fmt.Errorf("lock the team of the schedule %s: %w", id, err)
	}
	if _, err := q.LockSchedule(ctx, dbgen.LockScheduleParams{OrgID: s.orgID, PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Schedule{}, ErrNotFound
		}
		return Schedule{}, fmt.Errorf("lock the schedule %s: %w", id, err)
	}
	return s.get(ctx, q, id)
}

// pageable reads whether people may be Paged in the Team: why each may not, by User id, empty when they may.
func (s *Service) pageable(ctx context.Context, q Queries, teamID int64, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListPageable(ctx, dbgen.ListPageableParams{OrgID: s.orgID, TeamID: teamID, UserIds: ids})
	if err != nil {
		return nil, fmt.Errorf("read whether the people may be paged: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = skipReason(Person{Role: r.Role, Status: r.Status}, r.InTeam)
	}
	return out, nil
}

// resolve reads the people the Rotation layers of in name, by public_id. The people a layer keeps, compared with the
// stored layer of the same id in stored, are taken as stored, deleted Users included, who stay with their warnings
// (C-27.FR-5, settled 6). The people it adds must exist and not be deleted, else unknown_id at their pointer, and be
// able to be Paged in the Team, else not_pageable with the reason (C-27.FR-2); a stored id that the Schedule does not
// have is unknown_id.
func (s *Service) resolve(ctx context.Context, q Queries, team int64, in Input, stored []RotationLayer) (
	map[string]Person, error) {
	var ids []string
	for _, l := range in.RotationLayers {
		ids = append(ids, l.UserIDs...)
	}
	found := map[string]Person{} // the Users that are not deleted
	if len(ids) > 0 {
		rows, err := q.LookupUsers(ctx, dbgen.LookupUsersParams{OrgID: s.orgID, PublicIds: ids})
		if err != nil {
			return nil, fmt.Errorf("read the people of the rotation layers: %w", err)
		}
		for _, r := range rows {
			found[r.PublicID] = Person{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Login: r.Login, Role: r.Role,
				Status: r.Status}
		}
	}
	people := map[string]Person{}
	byID := make(map[string]RotationLayer, len(stored))
	storedPeople := map[string]Person{}
	for _, l := range stored {
		byID[l.PublicID] = l
		for _, u := range l.Users {
			storedPeople[u.PublicID] = u
		}
	}
	var added []int64
	addedAt := map[int64]string{}
	for i, l := range in.RotationLayers {
		base := "/rotation_layers/" + strconv.Itoa(i)
		kept := map[string]bool{}
		if l.ID != "" {
			old, ok := byID[l.ID]
			if !ok {
				return nil, &FieldError{Pointer: base + "/id", Code: CodeUnknownID,
					Detail: "The Schedule has no such Rotation layer."}
			}
			for _, u := range old.Users {
				kept[u.PublicID] = true
			}
		}
		for j, u := range l.UserIDs {
			pointer := base + "/user_ids/" + strconv.Itoa(j)
			if kept[u] {
				people[u] = storedPeople[u] // deleted since, maybe
				continue
			}
			p, ok := found[u]
			if !ok {
				return nil, &FieldError{Pointer: pointer, Code: CodeUnknownID, Detail: "No such User."}
			}
			people[u] = p
			if _, seen := addedAt[p.ID]; !seen {
				added = append(added, p.ID)
				addedAt[p.ID] = pointer
			}
		}
	}
	reasons, err := s.pageable(ctx, q, team, added)
	if err != nil {
		return nil, err
	}
	for _, id := range added {
		if reason := reasons[id]; reason != "" {
			return nil, notPageable(addedAt[id], reason)
		}
	}
	return people, nil
}

// notPageable is the refusal of a person who may not be Paged, at pointer.
func notPageable(pointer, reason string) *FieldError {
	detail := map[string]string{ReasonViewer: "The person is a Viewer and may not be Paged.",
		ReasonDisabled:  "The person is disabled and may not be Paged.",
		ReasonNotInTeam: "The person is not in the Team of the Schedule."}[reason]
	return &FieldError{Pointer: pointer, Code: CodeNotPageable, Detail: detail, Reason: reason}
}

// Create creates a Schedule of a Team (C-27.FR-1). The first Schedule of a Team is its main one (P-90); Main true
// makes a later one main instead of the previous one.
func (s *Service) Create(ctx context.Context, r Requester, in Input) (Schedule, error) {
	in, err := check(in)
	if err != nil {
		return Schedule{}, err
	}
	var created Schedule
	err = s.store.InTx(ctx, func(q Queries) error {
		team, err := s.lockTeam(ctx, q, in.TeamID)
		if err != nil {
			return err
		}
		n, err := q.CountTeamSchedules(ctx, dbgen.CountTeamSchedulesParams{OrgID: s.orgID, TeamID: team.ID})
		if err != nil {
			return fmt.Errorf("count the schedules of the team %s: %w", team.PublicID, err)
		}
		main := n == 0
		if in.Main != nil {
			if !*in.Main && main {
				return &FieldError{Pointer: "/main", Code: CodeUnsupported,
					Detail: "The first Schedule of a Team is its main Schedule."}
			}
			main = *in.Main
		}
		var zone string
		if in.TimeZone != nil {
			zone = *in.TimeZone
		} else if zone, err = q.GetOrganizationTimeZone(ctx, s.orgID); err != nil {
			return fmt.Errorf("read the time zone of the organization: %w", err)
		}
		people, err := s.resolve(ctx, q, team.ID, in, nil)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		id := publicid.New(publicid.Schedule)
		if main && n > 0 {
			if err := s.clearMain(ctx, q, r, team.ID, 0, id, now); err != nil {
				return err
			}
		}
		sid, err := q.InsertSchedule(ctx, dbgen.InsertScheduleParams{OrgID: s.orgID, PublicID: id, TeamID: team.ID,
			Name: in.Name, TimeZone: zone, Main: main, Now: now})
		if err != nil {
			return fmt.Errorf("create the schedule: %w", taken(err))
		}
		if err := s.writeLayers(ctx, q, sid, in.RotationLayers, nil, people); err != nil {
			return err
		}
		if created, err = s.get(ctx, q, id); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionCreated, Resource: resourceOf(created), Diff: audit.Created(viewOf(created)),
			SourceAddress: r.Address}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: created.PublicID})
	})
	if err != nil {
		return Schedule{}, err
	}
	s.exportInfo(created.PublicID, created.Name)
	return created, nil
}

// clearMain takes the mark main off the Team's main Schedule other than the Schedule except, recording each change by
// r with the Schedule now made main, schedule.
func (s *Service) clearMain(ctx context.Context, q Queries, r Requester, team, except int64, schedule string,
	now time.Time) error {
	rows, err := q.ClearMainSchedule(ctx, dbgen.ClearMainScheduleParams{OrgID: s.orgID, TeamID: team, KeepID: except,
		Now: now})
	if err != nil {
		return fmt.Errorf("take the mark main off the schedule of the team: %w", err)
	}
	for _, row := range rows {
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionUpdated, Resource: audit.Resource{Type: ResourceSchedule, PublicID: row.PublicID,
				Name: row.Name},
			Diff:          []audit.Change{{Pointer: "/main", Before: true, After: false}},
			Details:       map[string]any{"reason": "main_schedule_changed", "main_schedule": schedule},
			SourceAddress: r.Address}); err != nil {
			return err
		}
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: row.PublicID}); err != nil {
			return err
		}
	}
	return nil
}

// writeLayers writes the Rotation layers of the Schedule sid from the lowest: those with the id of a stored one are
// updated, the others inserted, and the stored ones left out deleted; the people of each are written again.
func (s *Service) writeLayers(ctx context.Context, q Queries, sid int64, layers []RotationLayerInput, stored []RotationLayer,
	people map[string]Person) error {
	storedID := make(map[string]int64, len(stored))
	for _, l := range stored {
		storedID[l.PublicID] = l.ID
	}
	keep := []int64{}
	for _, l := range layers {
		if id, ok := storedID[l.ID]; ok {
			keep = append(keep, id)
		}
	}
	if err := q.DeleteRotationLayers(ctx, dbgen.DeleteRotationLayersParams{OrgID: s.orgID, ScheduleID: sid,
		Keep: keep}); err != nil {
		return fmt.Errorf("delete the rotation layers left out: %w", err)
	}
	for i, l := range layers {
		f := rotationLayerFields(l)
		position := int32(i + 1)
		id, ok := storedID[l.ID]
		if ok {
			if err := q.UpdateRotationLayer(ctx, dbgen.UpdateRotationLayerParams{OrgID: s.orgID, ID: id,
				Position: position, Name: l.Name, ShiftLength: l.ShiftLength, ShiftSeconds: f.seconds,
				HandoverTime: l.HandoverTime, HandoverDay: f.day, StartDate: f.start, LimitDays: f.days,
				LimitStart: f.from, LimitEnd: f.to}); err != nil {
				return fmt.Errorf("update the rotation layer %s: %w", l.ID, err)
			}
			if err := q.DeleteRotationLayerUsers(ctx, dbgen.DeleteRotationLayerUsersParams{OrgID: s.orgID,
				RotationLayerID: id}); err != nil {
				return fmt.Errorf("remove the people of the rotation layer %s: %w", l.ID, err)
			}
		} else {
			var err error
			id, err = q.InsertRotationLayer(ctx, dbgen.InsertRotationLayerParams{OrgID: s.orgID,
				PublicID: publicid.New(publicid.RotationLayer), ScheduleID: sid, Position: position, Name: l.Name,
				ShiftLength: l.ShiftLength, ShiftSeconds: f.seconds, HandoverTime: l.HandoverTime, HandoverDay: f.day,
				StartDate: f.start, LimitDays: f.days, LimitStart: f.from, LimitEnd: f.to})
			if err != nil {
				return fmt.Errorf("add a rotation layer: %w", err)
			}
		}
		users := make([]int64, len(l.UserIDs))
		for j, u := range l.UserIDs {
			users[j] = people[u].ID
		}
		if err := q.InsertRotationLayerUsers(ctx, dbgen.InsertRotationLayerUsersParams{OrgID: s.orgID,
			RotationLayerID: id, UserIds: users}); err != nil {
			return fmt.Errorf("write the people of a rotation layer: %w", err)
		}
	}
	return nil
}

// fields are the nullable columns of a Rotation layer.
type fields struct {
	seconds  pgtype.Int4
	day      pgtype.Text
	start    pgtype.Date
	days     []string
	from, to pgtype.Text
}

func rotationLayerFields(l RotationLayerInput) fields {
	f := fields{start: pgtype.Date{Time: l.StartDate, Valid: true}}
	if l.ShiftSeconds != nil {
		f.seconds = pgtype.Int4{Int32: int32(*l.ShiftSeconds), Valid: true} //nolint:gosec // G115: at most MaxShiftSeconds
	}
	if l.HandoverDay != "" {
		f.day = pgtype.Text{String: l.HandoverDay, Valid: true}
	}
	if l.Limits != nil {
		f.days = l.Limits.Days
		if l.Limits.Start != "" {
			f.from = pgtype.Text{String: l.Limits.Start, Valid: true}
			f.to = pgtype.Text{String: l.Limits.End, Valid: true}
		}
	}
	return f
}

// Update replaces the configured fields and the Rotation layers of the Schedule publicID; a non-nil version must be
// its current one (If-Match). The Team stays (settled 4), the main Schedule cannot be unmarked (settled 5), and only
// the people a save adds to a Rotation layer must be able to be Paged (settled 6). An update that changes nothing
// writes nothing.
func (s *Service) Update(ctx context.Context, r Requester, publicID string, version *int64, in Input) (Schedule,
	error) {
	in, err := check(in)
	if err != nil {
		return Schedule{}, err
	}
	var before, updated Schedule
	err = s.store.InTx(ctx, func(q Queries) error {
		var err error
		if before, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		if team, err := publicid.Parse(publicid.Team, in.TeamID); err != nil || team != before.Team.PublicID {
			return &FieldError{Pointer: "/team_id", Code: CodeUnsupported,
				Detail: "A Schedule stays in its Team; create a Schedule in the other Team instead."}
		}
		main := before.Main
		if in.Main != nil {
			if before.Main && !*in.Main {
				return &FieldError{Pointer: "/main", Code: CodeUnsupported,
					Detail: "The main Schedule cannot be unmarked; mark another Schedule of the Team main instead."}
			}
			main = *in.Main
		}
		zone := before.TimeZone
		if in.TimeZone != nil {
			zone = *in.TimeZone
		}
		people, err := s.resolve(ctx, q, before.Team.ID, in, before.RotationLayers)
		if err != nil {
			return err
		}
		next := before
		next.Name, next.TimeZone, next.Main = in.Name, zone, main
		next.RotationLayers = make([]RotationLayer, len(in.RotationLayers))
		for i, l := range in.RotationLayers {
			next.RotationLayers[i] = rotationLayerFromInput(l, people)
		}
		changes := audit.Diff(viewOf(before), viewOf(next))
		if len(changes) == 0 {
			updated = before
			return nil
		}
		now := s.clock.Now().UTC()
		if main && !before.Main {
			if err := s.clearMain(ctx, q, r, before.Team.ID, before.ID, before.PublicID, now); err != nil {
				return err
			}
		}
		if err := q.UpdateSchedule(ctx, dbgen.UpdateScheduleParams{OrgID: s.orgID, ID: before.ID, Name: in.Name,
			TimeZone: zone, Main: main, Now: now}); err != nil {
			return fmt.Errorf("update the schedule %s: %w", before.PublicID, taken(err))
		}
		if err := s.writeLayers(ctx, q, before.ID, in.RotationLayers, before.RotationLayers, people); err != nil {
			return err
		}
		if updated, err = s.get(ctx, q, before.PublicID); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionUpdated, Resource: resourceOf(updated), Diff: audit.Diff(viewOf(before), viewOf(updated)),
			SourceAddress: r.Address}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: updated.PublicID})
	})
	if err != nil {
		return Schedule{}, err
	}
	if updated.Name != before.Name {
		s.exportInfo(updated.PublicID, updated.Name)
	}
	return updated, nil
}

// rotationLayerFromInput is the Rotation layer an input describes, as the Audit log diff compares it with the stored one.
func rotationLayerFromInput(l RotationLayerInput, people map[string]Person) RotationLayer {
	out := RotationLayer{PublicID: l.ID, Name: l.Name, ShiftLength: l.ShiftLength, HandoverTime: l.HandoverTime,
		HandoverDay: l.HandoverDay, StartDate: l.StartDate, Limits: l.Limits, Users: make([]Person, len(l.UserIDs))}
	if l.ShiftSeconds != nil {
		out.ShiftSeconds = *l.ShiftSeconds
	}
	for i, u := range l.UserIDs {
		out.Users[i] = people[u]
	}
	return out
}

// Delete deletes the Schedule publicID with its Rotation layers and Overrides; a non-nil version must be its current
// one. A Schedule that Referrers names is refused with InUseError (C-27.FR-12); deleting a Team's main Schedule leaves
// the Team without one (P-66).
func (s *Service) Delete(ctx context.Context, r Requester, publicID string, version *int64) error {
	var before Schedule
	err := s.store.InTx(ctx, func(q Queries) error {
		var err error
		if before, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		refs, err := s.Referrers(ctx, q, before)
		if err != nil {
			return err
		}
		if len(refs) > 0 {
			return &InUseError{Referrers: refs}
		}
		if err := q.DeleteSchedule(ctx, dbgen.DeleteScheduleParams{OrgID: s.orgID, ID: before.ID}); err != nil {
			return fmt.Errorf("delete the schedule %s: %w", before.PublicID, err)
		}
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionDeleted, Resource: resourceOf(before), Diff: deleted(viewOf(before)),
			SourceAddress: r.Address}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: before.PublicID})
	})
	if err != nil {
		return err
	}
	s.unexportInfo(before.PublicID)
	return nil
}

// Referrers lists what names the Schedule, which refuses its deletion: nothing until the Escalation steps (S-154).
func (s *Service) Referrers(context.Context, Queries, Schedule) ([]Referrer, error) {
	return nil, nil
}

// deleted is the diff of a deletion: each configured value before, none after.
func deleted[T any](before T) []audit.Change {
	out := audit.Created(before)
	for i := range out {
		out[i].Before, out[i].After = out[i].After, nil
	}
	return out
}

// taken maps the refusal of the unique index of the names to ErrNameTaken.
func taken(err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation &&
		pgErr.ConstraintName == nameKey {
		return ErrNameTaken
	}
	return err
}

// resourceOf is a Schedule as the Audit log names it.
func resourceOf(sc Schedule) audit.Resource {
	return audit.Resource{Type: ResourceSchedule, PublicID: sc.PublicID, Name: sc.Name}
}

// view is a Schedule as its Audit log diff shows it.
type view struct {
	TeamID         string              `json:"team_id"`
	Name           string              `json:"name"`
	TimeZone       string              `json:"time_zone"`
	Main           bool                `json:"main"`
	RotationLayers []rotationLayerView `json:"rotation_layers"`
}

// rotationLayerView is a Rotation layer as the Audit log diff shows it.
type rotationLayerView struct {
	ID           string   `json:"id,omitempty"`
	Name         string   `json:"name"`
	UserIDs      []string `json:"user_ids"`
	ShiftLength  string   `json:"shift_length"`
	ShiftSeconds *int     `json:"shift_seconds"`
	HandoverTime string   `json:"handover_time"`
	HandoverDay  *string  `json:"handover_day"`
	StartDate    string   `json:"start_date"`
	Limits       *Limits  `json:"limits"`
}

func viewOf(sc Schedule) view {
	v := view{TeamID: sc.Team.PublicID, Name: sc.Name, TimeZone: sc.TimeZone, Main: sc.Main,
		RotationLayers: make([]rotationLayerView, len(sc.RotationLayers))}
	for i, l := range sc.RotationLayers {
		lv := rotationLayerView{ID: l.PublicID, Name: l.Name, UserIDs: make([]string, len(l.Users)),
			ShiftLength: l.ShiftLength, HandoverTime: l.HandoverTime, StartDate: l.StartDate.Format(time.DateOnly),
			Limits: l.Limits}
		for j, u := range l.Users {
			lv.UserIDs[j] = u.PublicID
		}
		if l.ShiftLength == ShiftHours {
			seconds := l.ShiftSeconds
			lv.ShiftSeconds = &seconds
		}
		if l.HandoverDay != "" {
			day := l.HandoverDay
			lv.HandoverDay = &day
		}
		v.RotationLayers[i] = lv
	}
	return v
}

// exportInfo sets the muster_schedule_info series of a Schedule.
func (s *Service) exportInfo(publicID, name string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok && old != name {
		metrics.ScheduleInfo.Delete(publicID, old)
	}
	s.info[publicID] = name
	metrics.ScheduleInfo.With(publicID, name).Set(1)
}

// unexportInfo removes the muster_schedule_info series of a Schedule.
func (s *Service) unexportInfo(publicID string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok {
		metrics.ScheduleInfo.Delete(publicID, old)
		delete(s.info, publicID)
	}
}

// RefreshInfo makes muster_schedule_info list exactly the Schedules the database has, whichever replica changed them
// (C-27.FR-13).
func (s *Service) RefreshInfo(ctx context.Context) error {
	// The read happens under the lock, so that a local change exported meanwhile is never undone by an older read.
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	rows, err := s.store.ListScheduleInfo(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the schedules for their info metric: %w", err)
	}
	want := make(map[string]string, len(rows))
	for _, r := range rows {
		want[r.PublicID] = r.Name
	}
	for id, name := range s.info {
		if want[id] != name {
			metrics.ScheduleInfo.Delete(id, name)
			delete(s.info, id)
		}
	}
	for id, name := range want {
		s.info[id] = name
		metrics.ScheduleInfo.With(id, name).Set(1)
	}
	return nil
}

// InfoChanged asks RunInfo to refresh muster_schedule_info: a hint said a Schedule changed, maybe on another replica.
// It never blocks.
func (s *Service) InfoChanged() {
	select {
	case s.infoChanged <- struct{}{}:
	default:
	}
}

// RunInfo keeps muster_schedule_info current until ctx ends: it refreshes it at once, after InfoChanged and at every
// tick, which also repairs a refresh that failed.
func (s *Service) RunInfo(ctx context.Context, ticks <-chan time.Time) {
	for {
		_ = s.RefreshInfo(ctx) // a failed refresh is repeated at the next tick
		select {
		case <-ctx.Done():
			return
		case <-s.infoChanged:
		case <-ticks:
		}
	}
}
