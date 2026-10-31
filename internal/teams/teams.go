// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package teams holds the Teams of C-25: a name and a slug, each unique among the Teams that are not deleted, a
// description, the people added by hand and the Working hours, the Team's own or the Organization's. A Team grants no
// Permission and limits none (ADR-0018). Every change is recorded in the Audit log and announced with the live hint
// team, and the package exports muster_team_info. Deleting a Team keeps its row, marked deleted, removes its people and
// Working hours and leaves the Routes and Destinations it owned without an owner, through its own queries, so that the
// package stays a leaf of the import graph.
package teams

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
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/teams/dbgen"
	usersdb "github.com/muster-io/muster/internal/users/dbgen"
)

// The Audit log actions and resource type, and the live hint, of Teams (C-25.FR-11).
const (
	ActionCreated     = "team.created"
	ActionUpdated     = "team.updated"
	ActionDeleted     = "team.deleted"
	ActionUserAdded   = "team.user_added"
	ActionUserRemoved = "team.user_removed"
	ResourceTeam      = "team"
	Hint              = "team"
)

// The Audit log actions, resource types and live hints of the objects a deleted Team owned, as their own packages
// name them.
const (
	actionRouteUpdated       = "route.updated"
	actionDestinationUpdated = "destination.updated"
	resourceRoute            = "route"
	resourceDestination      = "destination"
	hintRoute                = "route"
	hintDestination          = "destination"
)

// SourceByHand is how a person an Admin added is in a Team.
const SourceByHand = "by_hand"

// The limits of a Team, in characters.
const (
	MaxNameLength        = 200
	MaxDescriptionLength = 2000
)

// The codes of FieldError, as the API names them.
const (
	CodeRequired      = "required"
	CodeInvalidFormat = "invalid_format"
	CodeTooLong       = "too_long"
	CodeOutOfRange    = "out_of_range"
	CodeDuplicate     = "duplicate"
	CodeUnknownID     = "unknown_id"
	CodeInvalidRegex  = "invalid_regex"
)

const (
	// uniqueViolation is the SQLSTATE of a unique index that refused a row; nameKey and slugKey are the indexes of the
	// names and the slugs of the Teams that are not deleted.
	uniqueViolation = "23505"
	nameKey         = "teams_name_key"
	slugKey         = "teams_slug_key"
)

// slugFormat is team.slug_format (P-74): 1 to 63 characters — lowercase letters, digits, -, _ and . — starting with
// a letter or a digit, the pattern of the CHECK.
var slugFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

var (
	// ErrNotFound is a Team that does not exist in the Organization or is deleted.
	ErrNotFound = errors.New("no such team")
	// ErrNameTaken is a name another Team that is not deleted has, ignoring case.
	ErrNameTaken = errors.New("another team has this name")
	// ErrSlugTaken is a slug another Team that is not deleted has.
	ErrSlugTaken = errors.New("another team has this slug")
	// ErrVersionMismatch is an If-Match that names another version of the Team.
	ErrVersionMismatch = errors.New("the team changed since it was read")
	// ErrNotInTeam is a removal of a User who is not in the Team.
	ErrNotInTeam = errors.New("the user is not in the team")
	// ErrInUse is the deletion of a Team that has Schedules or, from the Escalation chains on, owns chains
	// (team_in_use, P-64); InUseError names them.
	ErrInUse = errors.New("the team has schedules or escalation chains")
)

// Holder is an object that keeps a Team from being deleted: its kind, public_id and name.
type Holder struct {
	Type     string
	PublicID string
	Name     string
}

// HolderSchedule is the kind of a Schedule that holds its Team.
const HolderSchedule = "schedule"

// InUseError is ErrInUse with the objects that hold the Team.
type InUseError struct {
	Holders []Holder
}

func (e *InUseError) Error() string { return ErrInUse.Error() }

func (e *InUseError) Unwrap() error { return ErrInUse }

// FieldError is a field of a request that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// Team is a Team as the API reads it. Own are its own Working hours, nil when it follows the Organization's;
// WorkingHours are the effective ones. Matchers are its Team matchers in their order (C-25.FR-5).
type Team struct {
	ID           int64
	PublicID     string
	Name         string
	Slug         string
	Description  string
	Own          *WorkingHours
	WorkingHours WorkingHours
	Matchers     []Matcher
	UserCount    int64
	CreatedAt    time.Time
	Version      int64
}

// Matcher is a Team matcher (C-25.FR-5): a condition on one label in the syntax of Route Matchers.
type Matcher struct {
	Label string `json:"label"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// Input is what createTeam and updateTeam write. A nil Description keeps the stored one on an update and is empty on a
// creation; WorkingHours are read only with OwnWorkingHours, and nil keeps the Team's own on an update; nil Matchers
// keep the stored ones on an update and are none on a creation, and others replace them as a whole.
type Input struct {
	Name            string
	Slug            string
	Description     *string
	OwnWorkingHours bool
	WorkingHours    *WorkingHours
	Matchers        *[]Matcher
}

// Person is someone in a Team with their Role, how they are in it and the messengers they linked.
type Person struct {
	ID       int64
	PublicID string
	Name     string
	Login    string
	Role     string
	Sources  []string
	Linked   []LinkedMessenger
}

// LinkedMessenger is an Account link of a person: the type of its Connection and the Connection.
type LinkedMessenger struct {
	Type               string
	ConnectionPublicID string
	ConnectionName     string
}

// ListFilter selects a page of Teams by name: matching Query when it is set, after the position After.
type ListFilter struct {
	Query string
	After *Position
	Limit int
}

// Position is the place of a Team in the list by name: its lowered name and its id.
type Position struct {
	Name string `json:"n"`
	ID   int64  `json:"i"`
}

// Page is a page of Teams; Next, the position to continue after, is nil on the last page.
type Page struct {
	Teams []Team
	Next  *Position
}

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// Queries are the queries of the package, with the insert of the Audit log and the live hint.
type Queries interface {
	GetOrganizationWorkingHours(ctx context.Context, orgID int64) (dbgen.GetOrganizationWorkingHoursRow, error)
	ListTeams(ctx context.Context, arg dbgen.ListTeamsParams) ([]dbgen.ListTeamsRow, error)
	GetTeam(ctx context.Context, arg dbgen.GetTeamParams) (dbgen.GetTeamRow, error)
	LockTeam(ctx context.Context, arg dbgen.LockTeamParams) (int64, error)
	InsertTeam(ctx context.Context, arg dbgen.InsertTeamParams) (int64, error)
	UpdateTeam(ctx context.Context, arg dbgen.UpdateTeamParams) error
	DeleteTeam(ctx context.Context, arg dbgen.DeleteTeamParams) error
	DeleteTeamUsers(ctx context.Context, arg dbgen.DeleteTeamUsersParams) error
	ListTeamSchedules(ctx context.Context, arg dbgen.ListTeamSchedulesParams) ([]dbgen.ListTeamSchedulesRow, error)
	ReleaseOwnedDestinations(ctx context.Context, arg dbgen.ReleaseOwnedDestinationsParams) (
		[]dbgen.ReleaseOwnedDestinationsRow, error)
	ReleaseOwnedRoutes(ctx context.Context, arg dbgen.ReleaseOwnedRoutesParams) ([]dbgen.ReleaseOwnedRoutesRow, error)
	ListTeamUsers(ctx context.Context, arg dbgen.ListTeamUsersParams) ([]dbgen.ListTeamUsersRow, error)
	ListLinkedMessengers(ctx context.Context, arg dbgen.ListLinkedMessengersParams) (
		[]dbgen.ListLinkedMessengersRow, error)
	LockTeamUser(ctx context.Context, arg dbgen.LockTeamUserParams) (dbgen.LockTeamUserRow, error)
	GetTeamUser(ctx context.Context, arg dbgen.GetTeamUserParams) (dbgen.GetTeamUserRow, error)
	InsertTeamUser(ctx context.Context, arg dbgen.InsertTeamUserParams) (int64, error)
	DeleteTeamUser(ctx context.Context, arg dbgen.DeleteTeamUserParams) (int64, error)
	DeleteUserTeams(ctx context.Context, arg dbgen.DeleteUserTeamsParams) ([]dbgen.DeleteUserTeamsRow, error)
	ListTeamInfo(ctx context.Context, orgID int64) ([]dbgen.ListTeamInfoRow, error)
	ListTeamMatchers(ctx context.Context, arg dbgen.ListTeamMatchersParams) ([]dbgen.ListTeamMatchersRow, error)
	DeleteTeamMatchers(ctx context.Context, arg dbgen.DeleteTeamMatchersParams) error
	InsertTeamMatchers(ctx context.Context, arg dbgen.InsertTeamMatchersParams) error
	LockTeamsVersion(ctx context.Context, orgID int64) (int64, error)
	BumpTeamsVersion(ctx context.Context, orgID int64) error
	audit.Store
	Notify(ctx context.Context, h db.Hint) error
}

// Store runs the queries alone, in one transaction, or in a caller's transaction tx (On).
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
	On(tx dbgen.DBTX) Queries
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

func (pgStore) On(tx dbgen.DBTX) Queries { return newQueries(tx) }

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(newQueries(tx))
	})
}

// Config is what a Service needs: the Organization, the Store, the Audit log writer and the business clock, which
// dates the rows.
type Config struct {
	OrgID    int64
	Store    Store
	Audit    *audit.Writer
	Business clock.Clock
}

// Service holds the Teams of the Organization.
type Service struct {
	orgID int64
	store Store
	audit *audit.Writer
	clock clock.Clock

	// info holds the muster_team_info series this replica exports, by public_id to name.
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

// teamRow is a row of a Team as every read gives it.
type teamRow struct {
	ID              int64
	PublicID        string
	Name            string
	Slug            string
	Description     string
	OwnWorkingHours bool
	WorkingDays     []string
	WorkingStart    pgtype.Text
	WorkingEnd      pgtype.Text
	WorkingTimeZone pgtype.Text
	CreatedAt       time.Time
	Version         int64
	UserCount       int64
}

// teamOf is the Team of a row with the Organization's Working hours org.
func teamOf(r teamRow, org WorkingHours) Team {
	t := Team{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Slug: r.Slug, Description: r.Description,
		UserCount: r.UserCount, CreatedAt: r.CreatedAt.UTC(), Version: r.Version}
	if r.OwnWorkingHours {
		t.Own = &WorkingHours{Days: r.WorkingDays, Start: r.WorkingStart.String, End: r.WorkingEnd.String,
			TimeZone: r.WorkingTimeZone.String}
	}
	t.WorkingHours = Effective(t, org)
	return t
}

// organizationHours reads the Organization's Working hours.
func (s *Service) organizationHours(ctx context.Context, q Queries) (WorkingHours, error) {
	r, err := q.GetOrganizationWorkingHours(ctx, s.orgID)
	if err != nil {
		return WorkingHours{}, fmt.Errorf("read the working hours of the organization: %w", err)
	}
	return WorkingHours{Days: r.WorkingDays, Start: r.WorkdayStart, End: r.WorkdayEnd, TimeZone: r.TimeZone}, nil
}

// OrganizationHours reads the Organization's Working hours, which every Team without its own follows.
func (s *Service) OrganizationHours(ctx context.Context) (WorkingHours, error) {
	return s.organizationHours(ctx, s.store)
}

// List lists a page of the Teams that are not deleted, by name.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := min(max(f.Limit, 1), 500)
	p := dbgen.ListTeamsParams{OrgID: s.orgID, PageSize: int32(limit) + 1}
	if q := strings.TrimSpace(f.Query); q != "" {
		p.Q = pgtype.Text{String: q, Valid: true}
	}
	if f.After != nil {
		p.AfterName = pgtype.Text{String: f.After.Name, Valid: true}
		p.AfterID = pgtype.Int8{Int64: f.After.ID, Valid: true}
	}
	org, err := s.organizationHours(ctx, s.store)
	if err != nil {
		return Page{}, err
	}
	rows, err := s.store.ListTeams(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the teams: %w", err)
	}
	page := Page{Teams: make([]Team, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			// The cursor carries the name as the database lowers it, the key the list is sorted by.
			last := rows[limit-1]
			page.Next = &Position{Name: last.SortName, ID: last.ID}
			break
		}
		page.Teams = append(page.Teams, teamOf(teamRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Slug: r.Slug,
			Description: r.Description, OwnWorkingHours: r.OwnWorkingHours, WorkingDays: r.WorkingDays,
			WorkingStart: r.WorkingStart, WorkingEnd: r.WorkingEnd, WorkingTimeZone: r.WorkingTimeZone,
			CreatedAt: r.CreatedAt, Version: r.Version, UserCount: r.UserCount}, org))
	}
	if err := s.withMatchers(ctx, s.store, page.Teams); err != nil {
		return Page{}, err
	}
	return page, nil
}

// withMatchers sets the Team matchers of each Team, none for a Team without them.
func (s *Service) withMatchers(ctx context.Context, q Queries, list []Team) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, t := range list {
		ids[i], index[t.ID] = t.ID, i
		list[i].Matchers = []Matcher{}
	}
	rows, err := q.ListTeamMatchers(ctx, dbgen.ListTeamMatchersParams{OrgID: s.orgID, TeamIds: ids})
	if err != nil {
		return fmt.Errorf("list the team matchers: %w", err)
	}
	for _, r := range rows {
		if i, ok := index[r.TeamID]; ok {
			list[i].Matchers = append(list[i].Matchers, Matcher{Label: r.Label, Op: r.Op, Value: r.Value})
		}
	}
	return nil
}

// Get reads the Team publicID; a deleted or unknown one is ErrNotFound.
func (s *Service) Get(ctx context.Context, publicID string) (Team, error) {
	return s.get(ctx, s.store, publicID)
}

func (s *Service) get(ctx context.Context, q Queries, publicID string) (Team, error) {
	id, err := publicid.Parse(publicid.Team, publicID)
	if err != nil {
		return Team{}, ErrNotFound
	}
	r, err := q.GetTeam(ctx, dbgen.GetTeamParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, ErrNotFound
	}
	if err != nil {
		return Team{}, fmt.Errorf("read the team %s: %w", id, err)
	}
	org, err := s.organizationHours(ctx, q)
	if err != nil {
		return Team{}, err
	}
	out := []Team{teamOf(teamRow(r), org)}
	if err := s.withMatchers(ctx, q, out); err != nil {
		return Team{}, err
	}
	return out[0], nil
}

// lock locks the Team publicID for a change and reads it.
func (s *Service) lock(ctx context.Context, q Queries, publicID string) (Team, error) {
	id, err := publicid.Parse(publicid.Team, publicID)
	if err != nil {
		return Team{}, ErrNotFound
	}
	if _, err := q.LockTeam(ctx, dbgen.LockTeamParams{OrgID: s.orgID, PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Team{}, ErrNotFound
		}
		return Team{}, fmt.Errorf("lock the team %s: %w", id, err)
	}
	return s.get(ctx, q, id)
}

// check refuses an Input that is not valid, with the pointer of the field: the name, the slug (team.slug_format), the
// description and the Working hours when they are given with own_working_hours. It returns the input normalized.
func check(in Input) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return in, &FieldError{Pointer: "/name", Code: CodeRequired, Detail: "The name is empty."}
	case utf8.RuneCountInString(in.Name) > MaxNameLength:
		return in, &FieldError{Pointer: "/name", Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", MaxNameLength)}
	case !slugFormat.MatchString(in.Slug):
		return in, &FieldError{Pointer: "/slug", Code: CodeInvalidFormat,
			Detail: "The slug is 1 to 63 characters — lowercase letters, digits, -, _ and . — starting with a letter " +
				"or a digit."}
	case in.Description != nil && utf8.RuneCountInString(*in.Description) > MaxDescriptionLength:
		return in, &FieldError{Pointer: "/description", Code: CodeTooLong,
			Detail: fmt.Sprintf("The description is longer than %d characters.", MaxDescriptionLength)}
	}
	if in.Matchers != nil {
		for i, m := range *in.Matchers {
			if err := checkMatcher(i, m); err != nil {
				return in, err
			}
		}
	}
	if !in.OwnWorkingHours {
		in.WorkingHours = nil
		return in, nil
	}
	if in.WorkingHours != nil {
		w, err := in.WorkingHours.Validate("/working_hours")
		if err != nil {
			return in, err
		}
		in.WorkingHours = &w
	}
	return in, nil
}

// checkMatcher refuses the Team matcher at index i whose label is empty, whose operator is not one of Alertmanager's,
// or whose regular expression does not compile, compiled as the classification compiles it.
func checkMatcher(i int, m Matcher) error {
	pointer := "/matchers/" + strconv.Itoa(i)
	if m.Label == "" {
		return &FieldError{Pointer: pointer + "/label", Code: CodeInvalidFormat, Detail: "The label name is empty."}
	}
	if _, err := matchers.New(m.Label, matchers.Op(m.Op), m.Value); err != nil {
		if re, ok := errors.AsType[*matchers.RegexpError](err); ok {
			return &FieldError{Pointer: pointer + "/value", Code: CodeInvalidRegex, Detail: re.Error()}
		}
		return &FieldError{Pointer: pointer + "/op", Code: CodeInvalidFormat,
			Detail: "The operator is one of =, !=, =~ and !~."}
	}
	return nil
}

// writeMatchers replaces the Team matchers of the Team id with ms.
func (s *Service) writeMatchers(ctx context.Context, q Queries, id int64, ms []Matcher) error {
	if err := q.DeleteTeamMatchers(ctx, dbgen.DeleteTeamMatchersParams{OrgID: s.orgID, TeamID: id}); err != nil {
		return fmt.Errorf("remove the team matchers: %w", err)
	}
	if len(ms) == 0 {
		return nil
	}
	p := dbgen.InsertTeamMatchersParams{OrgID: s.orgID, TeamID: id}
	for _, m := range ms {
		p.Labels = append(p.Labels, m.Label)
		p.Ops = append(p.Ops, m.Op)
		p.MatcherValues = append(p.MatcherValues, m.Value)
	}
	if err := q.InsertTeamMatchers(ctx, p); err != nil {
		return fmt.Errorf("write the team matchers: %w", err)
	}
	return nil
}

// lockVersion locks the Organization's row before a change of a Team, ahead of the Team's own row (LockTeamsVersion),
// so that the change can raise organizations.teams_version, the stamp of the classification (C-25.FR-5).
func (s *Service) lockVersion(ctx context.Context, q Queries) error {
	if _, err := q.LockTeamsVersion(ctx, s.orgID); err != nil {
		return fmt.Errorf("lock the teams of the organization: %w", err)
	}
	return nil
}

// bumpVersion raises organizations.teams_version after a change of a Team, so that every replica classifies the next
// Alerts with it (C-25.FR-5).
func (s *Service) bumpVersion(ctx context.Context, q Queries) error {
	if err := q.BumpTeamsVersion(ctx, s.orgID); err != nil {
		return fmt.Errorf("raise the version of the teams: %w", err)
	}
	return nil
}

// missingHours is the refusal of own Working hours that are not given and not stored.
func missingHours() *FieldError {
	return &FieldError{Pointer: "/working_hours", Code: CodeRequired,
		Detail: "A Team with its own Working hours needs them: days, a start, an end and a time zone."}
}

// Create creates a Team. A name or a slug another Team that is not deleted has is ErrNameTaken or ErrSlugTaken.
func (s *Service) Create(ctx context.Context, r Requester, in Input) (Team, error) {
	in, err := check(in)
	if err != nil {
		return Team{}, err
	}
	if in.OwnWorkingHours && in.WorkingHours == nil {
		return Team{}, missingHours()
	}
	var created Team
	err = s.store.InTx(ctx, func(q Queries) error {
		if err := s.lockVersion(ctx, q); err != nil {
			return err
		}
		description := ""
		if in.Description != nil {
			description = *in.Description
		}
		p := dbgen.InsertTeamParams{OrgID: s.orgID, PublicID: publicid.New(publicid.Team), Name: in.Name,
			Slug: in.Slug, Description: description, OwnWorkingHours: in.OwnWorkingHours, Now: s.clock.Now().UTC()}
		setHours(&p.WorkingDays, &p.WorkingStart, &p.WorkingEnd, &p.WorkingTimeZone, in.WorkingHours)
		id, err := q.InsertTeam(ctx, p)
		if err != nil {
			return fmt.Errorf("create the team: %w", taken(err))
		}
		if in.Matchers != nil {
			if err := s.writeMatchers(ctx, q, id, *in.Matchers); err != nil {
				return err
			}
		}
		if err := s.bumpVersion(ctx, q); err != nil {
			return err
		}
		if created, err = s.get(ctx, q, p.PublicID); err != nil {
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
		return Team{}, err
	}
	s.exportInfo(created.PublicID, created.Name)
	return created, nil
}

// setHours sets the Working hours columns of a write from w, all null for none.
func setHours(days *[]string, start, end, zone *pgtype.Text, w *WorkingHours) {
	if w == nil {
		return
	}
	*days = w.Days
	*start = pgtype.Text{String: w.Start, Valid: true}
	*end = pgtype.Text{String: w.End, Valid: true}
	*zone = pgtype.Text{String: w.TimeZone, Valid: true}
}

// Update replaces the configured fields of the Team publicID; a non-nil version must be its current one (If-Match).
// An update that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, r Requester, publicID string, version *int64, in Input) (Team, error) {
	in, err := check(in)
	if err != nil {
		return Team{}, err
	}
	var before, updated Team
	err = s.store.InTx(ctx, func(q Queries) error {
		if err := s.lockVersion(ctx, q); err != nil {
			return err
		}
		var err error
		if before, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		next := before
		next.Name, next.Slug, next.Own = in.Name, in.Slug, in.WorkingHours
		if in.Description != nil {
			next.Description = *in.Description
		}
		if in.Matchers != nil {
			next.Matchers = *in.Matchers
		}
		if in.OwnWorkingHours && next.Own == nil {
			if before.Own == nil {
				return missingHours()
			}
			next.Own = before.Own
		}
		changes := audit.Diff(viewOf(before), viewOf(next))
		if len(changes) == 0 {
			updated = before
			return nil
		}
		p := dbgen.UpdateTeamParams{OrgID: s.orgID, ID: before.ID, Name: next.Name, Slug: next.Slug,
			Description: next.Description, OwnWorkingHours: next.Own != nil, Now: s.clock.Now().UTC()}
		setHours(&p.WorkingDays, &p.WorkingStart, &p.WorkingEnd, &p.WorkingTimeZone, next.Own)
		if err := q.UpdateTeam(ctx, p); err != nil {
			return fmt.Errorf("update the team %s: %w", before.PublicID, taken(err))
		}
		if !slices.Equal(before.Matchers, next.Matchers) {
			if err := s.writeMatchers(ctx, q, before.ID, next.Matchers); err != nil {
				return err
			}
		}
		if err := s.bumpVersion(ctx, q); err != nil {
			return err
		}
		if updated, err = s.get(ctx, q, before.PublicID); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionUpdated, Resource: resourceOf(updated), Diff: changes, SourceAddress: r.Address}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: updated.PublicID})
	})
	if err != nil {
		return Team{}, err
	}
	if updated.Name != before.Name {
		s.exportInfo(updated.PublicID, updated.Name)
	}
	return updated, nil
}

// Delete deletes the Team publicID (C-25.FR-10); a non-nil version must be its current one. A Team that has Schedules
// is refused with InUseError, read under the lock that every change of its Schedules takes (P-64). The row stays,
// marked deleted, for the names that point at it, and its name and slug are free at once; its people and Working hours
// are removed, and the Destinations, then the Routes, it owned are left without an owner, each with a new version and
// its own Audit log entry by the system, in the same transaction.
func (s *Service) Delete(ctx context.Context, r Requester, publicID string, version *int64) error {
	var before Team
	err := s.store.InTx(ctx, func(q Queries) error {
		if err := s.lockVersion(ctx, q); err != nil {
			return err
		}
		var err error
		if before, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		schedules, err := q.ListTeamSchedules(ctx, dbgen.ListTeamSchedulesParams{OrgID: s.orgID, TeamID: before.ID})
		if err != nil {
			return fmt.Errorf("list the schedules of the team %s: %w", before.PublicID, err)
		}
		if len(schedules) > 0 {
			holders := make([]Holder, len(schedules))
			for i, sc := range schedules {
				holders[i] = Holder{Type: HolderSchedule, PublicID: sc.PublicID, Name: sc.Name}
			}
			return &InUseError{Holders: holders}
		}
		now := s.clock.Now().UTC()
		owner := pgtype.Int8{Int64: before.ID, Valid: true}
		dests, err := q.ReleaseOwnedDestinations(ctx, dbgen.ReleaseOwnedDestinationsParams{OrgID: s.orgID,
			TeamID: owner, Now: now})
		if err != nil {
			return fmt.Errorf("release the destinations of the team %s: %w", before.PublicID, err)
		}
		for _, d := range dests {
			if err := s.released(ctx, q, before, actionDestinationUpdated, resourceDestination, hintDestination,
				d.PublicID, d.Name); err != nil {
				return err
			}
		}
		routes, err := q.ReleaseOwnedRoutes(ctx, dbgen.ReleaseOwnedRoutesParams{OrgID: s.orgID, TeamID: owner,
			Now: now})
		if err != nil {
			return fmt.Errorf("release the routes of the team %s: %w", before.PublicID, err)
		}
		for _, rt := range routes {
			if err := s.released(ctx, q, before, actionRouteUpdated, resourceRoute, hintRoute, rt.PublicID,
				rt.Name); err != nil {
				return err
			}
		}
		if err := q.DeleteTeamUsers(ctx, dbgen.DeleteTeamUsersParams{OrgID: s.orgID, TeamID: before.ID}); err != nil {
			return fmt.Errorf("remove the people of the team %s: %w", before.PublicID, err)
		}
		if err := s.writeMatchers(ctx, q, before.ID, nil); err != nil {
			return err
		}
		if err := s.bumpVersion(ctx, q); err != nil {
			return err
		}
		if err := q.DeleteTeam(ctx, dbgen.DeleteTeamParams{OrgID: s.orgID, ID: before.ID,
			Now: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
			return fmt.Errorf("delete the team %s: %w", before.PublicID, err)
		}
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionDeleted, Resource: resourceOf(before),
			Diff: []audit.Change{{Pointer: "/deleted_at", After: now}},
			Details: map[string]any{"users_removed": before.UserCount, "routes_released": len(routes),
				"destinations_released": len(dests)},
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

// released records that the deletion of the Team t left a Route or a Destination without an owner: an Audit log entry
// by the system with the change of its owning_team_id, and its live hint.
func (s *Service) released(ctx context.Context, q Queries, t Team, action, resource, hint, publicID,
	name string) error {
	if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: audit.System,
		Transport: audit.TransportSystem, Action: action,
		Resource: audit.Resource{Type: resource, PublicID: publicID, Name: name},
		Diff:     []audit.Change{{Pointer: "/owning_team_id", Before: t.PublicID}},
		Details:  map[string]any{"reason": "team_deleted", "team": t.PublicID}}); err != nil {
		return err
	}
	return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: hint, ID: publicID})
}

// Users lists the people in the Team publicID, by name.
func (s *Service) Users(ctx context.Context, publicID string) ([]Person, error) {
	t, err := s.get(ctx, s.store, publicID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListTeamUsers(ctx, dbgen.ListTeamUsersParams{OrgID: s.orgID, TeamID: t.ID})
	if err != nil {
		return nil, fmt.Errorf("list the people of the team %s: %w", t.PublicID, err)
	}
	out := make([]Person, len(rows))
	for i, r := range rows {
		out[i] = personOf(dbgen.GetTeamUserRow(r))
	}
	if err := s.withLinks(ctx, s.store, out); err != nil {
		return nil, err
	}
	return out, nil
}

func personOf(r dbgen.GetTeamUserRow) Person {
	m := Person{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Login: r.Login, Role: r.Role,
		Sources: []string{}, Linked: []LinkedMessenger{}}
	if r.ByHand {
		m.Sources = append(m.Sources, SourceByHand)
	}
	return m
}

// withLinks sets the messengers each person linked.
func (s *Service) withLinks(ctx context.Context, q Queries, list []Person) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, m := range list {
		ids[i], index[m.ID] = m.ID, i
	}
	rows, err := q.ListLinkedMessengers(ctx, dbgen.ListLinkedMessengersParams{OrgID: s.orgID, UserIds: ids})
	if err != nil {
		return fmt.Errorf("list the account links of the people: %w", err)
	}
	for _, r := range rows {
		if i, ok := index[r.UserID]; ok {
			list[i].Linked = append(list[i].Linked, LinkedMessenger{Type: r.Type, ConnectionPublicID: r.PublicID,
				ConnectionName: r.Name})
		}
	}
	return nil
}

// unknownUser is the refusal of a User to add who does not exist or is deleted.
func unknownUser() *FieldError {
	return &FieldError{Pointer: "/user_id", Code: CodeUnknownID, Detail: "No such User."}
}

// AddUser adds the User userID to the Team teamID by hand (C-25.FR-3) and reports whether it added them: a User
// already in the Team by hand changes nothing. A User that does not exist or is deleted is an unknown_id FieldError;
// a disabled one may be added.
func (s *Service) AddUser(ctx context.Context, r Requester, teamID, userID string) (Person, bool, error) {
	uid, err := publicid.Parse(publicid.User, userID)
	if err != nil {
		return Person{}, false, unknownUser()
	}
	var (
		out   Person
		added bool
	)
	err = s.store.InTx(ctx, func(q Queries) error {
		t, err := s.lock(ctx, q, teamID)
		if err != nil {
			return err
		}
		u, err := q.LockTeamUser(ctx, dbgen.LockTeamUserParams{OrgID: s.orgID, PublicID: uid})
		if errors.Is(err, pgx.ErrNoRows) {
			return unknownUser()
		}
		if err != nil {
			return fmt.Errorf("read the user %s: %w", uid, err)
		}
		n, err := q.InsertTeamUser(ctx, dbgen.InsertTeamUserParams{OrgID: s.orgID, TeamID: t.ID, UserID: u.ID,
			Now: s.clock.Now().UTC()})
		if err != nil {
			return fmt.Errorf("add %s to the team %s: %w", uid, t.PublicID, err)
		}
		row, err := q.GetTeamUser(ctx, dbgen.GetTeamUserParams{OrgID: s.orgID, TeamID: t.ID, PublicID: uid})
		if err != nil {
			return fmt.Errorf("read %s in the team %s: %w", uid, t.PublicID, err)
		}
		list := []Person{personOf(row)}
		if err := s.withLinks(ctx, q, list); err != nil {
			return err
		}
		out, added = list[0], n > 0
		if !added {
			return nil
		}
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionUserAdded, Resource: resourceOf(t),
			Diff:    []audit.Change{{Pointer: "/users/" + u.PublicID, After: personView{Sources: out.Sources}}},
			Details: map[string]any{"user": u.PublicID, "user_name": u.Name}, SourceAddress: r.Address}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: t.PublicID})
	})
	if err != nil {
		return Person{}, false, err
	}
	return out, added, nil
}

// personView is a person in a Team as the Audit log diff shows them.
type personView struct {
	Sources []string `json:"sources"`
}

// RemoveUser takes the User userID out of the Team teamID (C-25.FR-3); a User who is not in it is ErrNotInTeam.
func (s *Service) RemoveUser(ctx context.Context, r Requester, teamID, userID string) error {
	uid, err := publicid.Parse(publicid.User, userID)
	if err != nil {
		return ErrNotInTeam
	}
	return s.store.InTx(ctx, func(q Queries) error {
		t, err := s.lock(ctx, q, teamID)
		if err != nil {
			return err
		}
		row, err := q.GetTeamUser(ctx, dbgen.GetTeamUserParams{OrgID: s.orgID, TeamID: t.ID, PublicID: uid})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotInTeam
		}
		if err != nil {
			return fmt.Errorf("read %s in the team %s: %w", uid, t.PublicID, err)
		}
		if _, err := q.DeleteTeamUser(ctx, dbgen.DeleteTeamUserParams{OrgID: s.orgID, TeamID: t.ID,
			UserID: row.ID}); err != nil {
			return fmt.Errorf("remove %s from the team %s: %w", uid, t.PublicID, err)
		}
		m := personOf(row)
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionUserRemoved, Resource: resourceOf(t),
			Diff:    []audit.Change{{Pointer: "/users/" + row.PublicID, Before: personView{Sources: m.Sources}}},
			Details: map[string]any{"user": row.PublicID, "user_name": row.Name}, SourceAddress: r.Address}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: t.PublicID})
	})
}

// RemoveUserTeams takes the deleted User userID out of every Team (C-03.FR-13 revised, C-25.FR-3), in the transaction
// tx of the deletion, each recorded as team.user_removed by the actor of the deletion, with the User — named as the
// deletion left them — and the reason user_deleted.
// It is the users package's TeamRemover; it has nothing to run once tx committed.
func (s *Service) RemoveUserTeams(ctx context.Context, tx usersdb.DBTX, userID int64, actor audit.Actor,
	t audit.Transport, addr netip.Addr) (func(context.Context), error) {
	q := s.store.On(tx)
	rows, err := q.DeleteUserTeams(ctx, dbgen.DeleteUserTeamsParams{OrgID: s.orgID, UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("remove the user %d from their teams: %w", userID, err)
	}
	for _, r := range rows {
		m := personOf(dbgen.GetTeamUserRow{ByHand: r.ByHand})
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: actor, Transport: t,
			Action: ActionUserRemoved, Resource: audit.Resource{Type: ResourceTeam, PublicID: r.PublicID, Name: r.Name},
			Diff:          []audit.Change{{Pointer: "/users/" + r.UserPublicID, Before: personView{Sources: m.Sources}}},
			Details:       map[string]any{"user": r.UserPublicID, "user_name": r.UserName, "reason": "user_deleted"},
			SourceAddress: addr}); err != nil {
			return nil, err
		}
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: r.PublicID}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// taken maps the refusal of the unique index of the names or of the slugs to ErrNameTaken or ErrSlugTaken.
func taken(err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case nameKey:
			return ErrNameTaken
		case slugKey:
			return ErrSlugTaken
		}
	}
	return err
}

// resourceOf is a Team as the Audit log names it.
func resourceOf(t Team) audit.Resource {
	return audit.Resource{Type: ResourceTeam, PublicID: t.PublicID, Name: t.Name}
}

// view is a Team as its Audit log diff shows it: its own Working hours, null when it follows the Organization's.
type view struct {
	Name            string        `json:"name"`
	Slug            string        `json:"slug"`
	Description     string        `json:"description"`
	OwnWorkingHours bool          `json:"own_working_hours"`
	WorkingHours    *WorkingHours `json:"working_hours"`
	Matchers        []Matcher     `json:"matchers"`
}

func viewOf(t Team) view {
	ms := t.Matchers
	if ms == nil {
		ms = []Matcher{}
	}
	return view{Name: t.Name, Slug: t.Slug, Description: t.Description, OwnWorkingHours: t.Own != nil,
		WorkingHours: t.Own, Matchers: ms}
}

// exportInfo sets the muster_team_info series of a Team.
func (s *Service) exportInfo(publicID, name string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok && old != name {
		metrics.TeamInfo.Delete(publicID, old)
	}
	s.info[publicID] = name
	metrics.TeamInfo.With(publicID, name).Set(1)
}

// unexportInfo removes the muster_team_info series of a Team.
func (s *Service) unexportInfo(publicID string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok {
		metrics.TeamInfo.Delete(publicID, old)
		delete(s.info, publicID)
	}
}

// RefreshInfo makes muster_team_info list exactly the Teams that are not deleted, as the database has them, whichever
// replica changed them (C-25.FR-11).
func (s *Service) RefreshInfo(ctx context.Context) error {
	// The read happens under the lock, so that a local change exported meanwhile is never undone by an older read.
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	rows, err := s.store.ListTeamInfo(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the teams for their info metric: %w", err)
	}
	want := make(map[string]string, len(rows))
	for _, r := range rows {
		want[r.PublicID] = r.Name
	}
	for id, name := range s.info {
		if want[id] != name {
			metrics.TeamInfo.Delete(id, name)
			delete(s.info, id)
		}
	}
	for id, name := range want {
		s.info[id] = name
		metrics.TeamInfo.With(id, name).Set(1)
	}
	return nil
}

// InfoChanged asks RunInfo to refresh muster_team_info: a hint said a Team changed, maybe on another replica. It
// never blocks.
func (s *Service) InfoChanged() {
	select {
	case s.infoChanged <- struct{}{}:
	default:
	}
}

// RunInfo keeps muster_team_info current until ctx ends: it refreshes it at once, after InfoChanged and at every tick,
// which also repairs a refresh that failed.
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
