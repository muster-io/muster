// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package escalation holds the Escalation chains of C-32 as configuration: a name unique in the Organization, a
// description, an optional owning Team and the ordered steps within escalation.limits (steps.go), the Routes and Teams
// that name a chain, the Destinations it writes in and its warnings (warnings.go). Only Admins change chains; every
// change is recorded in the Audit log with a before/after diff, announced with the live hint escalation-chain, and the
// package exports muster_escalation_chain_info. Routes and Teams name chains through their own packages, which read
// escalation_chains with their own queries; a chain that one of them names is never deleted (chain_in_use).
//
// Lock order. A transaction that locks rows of more than one of these locks them in this order: the Organization's row
// of the Teams (a save or the deletion of a Team), the Teams, the Routes, the Escalation chains, the Schedules. A save of
// a chain reads its new owning Team FOR SHARE before the chain's row — a Team's deletion locks the Team and then reads
// the chains it owns — and the Schedules its steps name FOR KEY SHARE after it, in id order — a Schedule's deletion
// locks the Schedule FOR UPDATE and then reads the steps that name it; KEY SHARE does not wait for a change of a
// Schedule that keeps its key, such as making another one main, which locks the new main Schedule and then updates the
// old one. A save of a Route or a Team locks its own row and then the chains it names FOR
// KEY SHARE, and a chain's deletion locks the chain FOR UPDATE and then reads the Routes and Teams that name it, taking
// no lock on them. No transaction of a chain locks a Route or a Team for a change.
package escalation

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/publicid"
)

// The Audit log actions and resource type, and the live hint, of Escalation chains (C-32.FR-19).
const (
	ActionCreated = "escalation_chain.created"
	ActionUpdated = "escalation_chain.updated"
	ActionDeleted = "escalation_chain.deleted"
	ResourceChain = "escalation_chain"
	Hint          = "escalation-chain"
)

// The live hints of the Routes and Teams that name a chain, as their packages name them.
const (
	hintRoute = "route"
	hintTeam  = "team"
)

// The limits of a chain, in characters.
const (
	MaxNameLength        = 200
	MaxDescriptionLength = 2000
)

// The codes of FieldError, as the API names them.
const (
	CodeRequired              = "required"
	CodeTooLong               = "too_long"
	CodeOutOfRange            = "out_of_range"
	CodeDuplicate             = "duplicate"
	CodeUnknownID             = "unknown_id"
	CodeUnsupported           = "unsupported"
	CodeNotPageable           = "not_pageable"
	CodeGatewayRequired       = "gateway_required"
	CodeWebhookEventsRequired = "webhook_events_required"
)

// Why a person may not be Paged (NotPageableReason): a Viewer or a disabled User.
const (
	ReasonViewer   = "viewer"
	ReasonDisabled = "disabled"
)

// The kinds of what holds a chain, as in_use_by names them.
const (
	HolderRoute = "route"
	HolderTeam  = "team"
)

const (
	// uniqueViolation is the SQLSTATE of a unique index that refused a row; nameKey is the index of the names.
	uniqueViolation = "23505"
	nameKey         = "escalation_chains_name_key"
)

var (
	// ErrNotFound is a chain that does not exist in the Organization.
	ErrNotFound = errors.New("no such escalation chain")
	// ErrNameTaken is a name another chain has, ignoring case.
	ErrNameTaken = errors.New("another escalation chain has this name")
	// ErrVersionMismatch is an If-Match that names another version of the chain.
	ErrVersionMismatch = errors.New("the escalation chain changed since it was read")
	// ErrInUse is the deletion of a chain that a Route or a Team names (chain_in_use); InUseError names them.
	ErrInUse = errors.New("routes or teams name the escalation chain")
)

// Holder is a Route or a Team that names a chain, which keeps it from being deleted.
type Holder struct {
	Type     string
	PublicID string
	Name     string
}

// InUseError is ErrInUse with the Routes and Teams that name the chain.
type InUseError struct {
	Holders []Holder
}

func (e *InUseError) Error() string { return ErrInUse.Error() }

func (e *InUseError) Unwrap() error { return ErrInUse }

// FieldError is a field of a request that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem; Reason says why a person may not be Paged with not_pageable.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
	Reason  string
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// TeamRef names a Team, marked deleted once deleted.
type TeamRef struct {
	ID       int64
	PublicID string
	Name     string
	Slug     string
	Deleted  bool
}

// Chain is an Escalation chain as the API reads it, with its steps in their order, the Routes and Teams that name it,
// the Destinations it writes in and its warnings.
type Chain struct {
	ID           int64
	PublicID     string
	Name         string
	Description  string
	OwningTeam   *TeamRef
	Steps        []Step
	Referrers    Referrers
	Destinations []DestinationRef
	Warnings     []Warning
	CreatedAt    time.Time
	Version      int64
}

// Referrers are the Routes and the Teams that are not deleted and name a chain, by name.
type Referrers struct {
	Routes []RouteReferrer
	Teams  []TeamReferrer
}

// RouteReferrer is a Route that names a chain with its Severity condition.
type RouteReferrer struct {
	PublicID  string
	Name      string
	Condition string
}

// TeamReferrer is a Team that names a chain with its Severity condition.
type TeamReferrer struct {
	Team      TeamRef
	Condition string
}

// DestinationRef is a Destination a chain writes in, with its health and what its Connection can mention.
type DestinationRef struct {
	ID           int64
	PublicID     string
	Name         string
	Type         string
	Health       string
	BrokenSince  *time.Time
	BrokenReason *string
	connection   connectionAbilities
}

// Input is what createEscalationChain and updateEscalationChain write. A nil Description keeps the stored one on an
// update and is empty on a creation; a nil OwningTeamID keeps the stored owner on an update and is none on a creation,
// and an empty string clears it. Steps replace the stored ones as a whole.
type Input struct {
	Name         string
	Description  *string
	OwningTeamID *string
	Steps        []StepInput
}

// ListFilter selects a page of chains by name: matching Query when it is set, owned by Team (a public_id) or by the
// Teams of the User MemberID when one is set, after the position After.
type ListFilter struct {
	Query    string
	Team     string
	MemberID int64
	After    *Position
	Limit    int
}

// Position is the place of a chain in the list by name: its lowered name and its id.
type Position struct {
	Name string `json:"n"`
	ID   int64  `json:"i"`
}

// Page is a page of chains; Next, the position to continue after, is nil on the last page.
type Page struct {
	Chains []Chain
	Next   *Position
}

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// DBTX is the pool or transaction the queries run in.
type DBTX = dbgen.DBTX

// Pool is the main pool: the queries, and the transactions of a change.
type Pool interface {
	DBTX
	db.Beginner
}

// Queries are the queries of the package, with the insert of the Audit log and the live hint.
type Queries interface {
	GetOrganizationWorkingHours(ctx context.Context, orgID int64) (dbgen.GetOrganizationWorkingHoursRow, error)
	GetTeamWorkingHours(ctx context.Context, arg dbgen.GetTeamWorkingHoursParams) (dbgen.GetTeamWorkingHoursRow, error)
	ListChains(ctx context.Context, arg dbgen.ListChainsParams) ([]dbgen.ListChainsRow, error)
	GetChain(ctx context.Context, arg dbgen.GetChainParams) (dbgen.GetChainRow, error)
	LockChain(ctx context.Context, arg dbgen.LockChainParams) (int64, error)
	LockChainForDeletion(ctx context.Context, arg dbgen.LockChainForDeletionParams) (int64, error)
	LockOwningTeam(ctx context.Context, arg dbgen.LockOwningTeamParams) (dbgen.LockOwningTeamRow, error)
	InsertChain(ctx context.Context, arg dbgen.InsertChainParams) (int64, error)
	UpdateChain(ctx context.Context, arg dbgen.UpdateChainParams) error
	DeleteChain(ctx context.Context, arg dbgen.DeleteChainParams) error
	ListSteps(ctx context.Context, arg dbgen.ListStepsParams) ([]dbgen.ListStepsRow, error)
	ListStepUsers(ctx context.Context, arg dbgen.ListStepUsersParams) ([]dbgen.ListStepUsersRow, error)
	DeleteSteps(ctx context.Context, arg dbgen.DeleteStepsParams) error
	InsertStep(ctx context.Context, arg dbgen.InsertStepParams) (int64, error)
	InsertStepUsers(ctx context.Context, arg dbgen.InsertStepUsersParams) error
	ResolveSchedules(ctx context.Context, arg dbgen.ResolveSchedulesParams) ([]dbgen.ResolveSchedulesRow, error)
	ResolveUsers(ctx context.Context, arg dbgen.ResolveUsersParams) ([]dbgen.ResolveUsersRow, error)
	ResolveTeams(ctx context.Context, arg dbgen.ResolveTeamsParams) ([]dbgen.ResolveTeamsRow, error)
	ListGateways(ctx context.Context, orgID int64) ([]dbgen.ListGatewaysRow, error)
	ResolveDestinations(ctx context.Context, arg dbgen.ResolveDestinationsParams) ([]dbgen.ResolveDestinationsRow,
		error)
	ListRouteReferrers(ctx context.Context, arg dbgen.ListRouteReferrersParams) ([]dbgen.ListRouteReferrersRow, error)
	ListTeamReferrers(ctx context.Context, arg dbgen.ListTeamReferrersParams) ([]dbgen.ListTeamReferrersRow, error)
	ListChainDestinations(ctx context.Context, arg dbgen.ListChainDestinationsParams) (
		[]dbgen.ListChainDestinationsRow, error)
	ListUserTeams(ctx context.Context, arg dbgen.ListUserTeamsParams) ([]int64, error)
	GetTeamID(ctx context.Context, arg dbgen.GetTeamIDParams) (int64, error)
	ListChainInfo(ctx context.Context, orgID int64) ([]dbgen.ListChainInfoRow, error)
	audit.Store
	Notify(ctx context.Context, h db.Hint) error
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(p Pool) Store {
	return pgStore{pgQueries: queriesOver(p), pool: p}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
	exec DBTX
}

func queriesOver(d DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d), exec: d}
}

func (q pgQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.exec, h)
}

type pgStore struct {
	pgQueries
	pool Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(queriesOver(tx)) })
}

// Config is what a Service needs: the Organization, the Store, the Audit log writer and the business clock, which
// dates the rows.
type Config struct {
	OrgID    int64
	Store    Store
	Audit    *audit.Writer
	Business clock.Clock
}

// Service holds the Escalation chains of the Organization.
type Service struct {
	orgID int64
	store Store
	audit *audit.Writer
	clock clock.Clock

	// info holds the muster_escalation_chain_info series this replica exports, by public_id to name.
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

// chainRow is a row of a chain as every read gives it.
type chainRow struct {
	ID           int64
	PublicID     string
	Name         string
	Description  string
	CreatedAt    time.Time
	Version      int64
	OwningTeamID pgtype.Int8
	TeamPublicID pgtype.Text
	TeamName     pgtype.Text
	TeamSlug     pgtype.Text
	TeamDeleted  bool
}

func chainOf(r chainRow) Chain {
	c := Chain{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Description: r.Description, CreatedAt: r.CreatedAt.UTC(),
		Version: r.Version, Steps: []Step{}, Destinations: []DestinationRef{}, Warnings: []Warning{},
		Referrers: Referrers{Routes: []RouteReferrer{}, Teams: []TeamReferrer{}}}
	if r.OwningTeamID.Valid {
		c.OwningTeam = &TeamRef{ID: r.OwningTeamID.Int64, PublicID: r.TeamPublicID.String, Name: r.TeamName.String,
			Slug: r.TeamSlug.String, Deleted: r.TeamDeleted}
	}
	return c
}

// List lists a page of the chains by name.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := min(max(f.Limit, 1), 500)
	p := dbgen.ListChainsParams{OrgID: s.orgID, PageSize: int32(limit) + 1}
	if q := strings.TrimSpace(f.Query); q != "" {
		p.Q = pgtype.Text{String: q, Valid: true}
	}
	if f.After != nil {
		p.AfterName = pgtype.Text{String: f.After.Name, Valid: true}
		p.AfterID = pgtype.Int8{Int64: f.After.ID, Valid: true}
	}
	switch {
	case f.MemberID != 0:
		ids, err := s.store.ListUserTeams(ctx, dbgen.ListUserTeamsParams{OrgID: s.orgID, UserID: f.MemberID})
		if err != nil {
			return Page{}, fmt.Errorf("list the teams of the user: %w", err)
		}
		p.TeamIds = append([]int64{}, ids...)
	case f.Team != "":
		p.TeamIds = []int64{}
		if id, err := publicid.Parse(publicid.Team, f.Team); err == nil {
			team, err := s.store.GetTeamID(ctx, dbgen.GetTeamIDParams{OrgID: s.orgID, PublicID: id})
			switch {
			case err == nil:
				p.TeamIds = []int64{team}
			case !errors.Is(err, pgx.ErrNoRows):
				return Page{}, fmt.Errorf("read the team %s: %w", id, err)
			}
		}
	}
	rows, err := s.store.ListChains(ctx, p)
	if err != nil {
		return Page{}, fmt.Errorf("list the escalation chains: %w", err)
	}
	page := Page{Chains: make([]Chain, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			page.Next = &Position{Name: last.SortName, ID: last.ID}
			break
		}
		page.Chains = append(page.Chains, chainOf(chainRow{ID: r.ID, PublicID: r.PublicID, Name: r.Name,
			Description: r.Description, CreatedAt: r.CreatedAt, Version: r.Version, OwningTeamID: r.OwningTeamID,
			TeamPublicID: r.TeamPublicID, TeamName: r.TeamName, TeamSlug: r.TeamSlug, TeamDeleted: r.TeamDeleted}))
	}
	if err := s.complete(ctx, s.store, page.Chains); err != nil {
		return Page{}, err
	}
	return page, nil
}

// complete reads the steps, the referrers and the Destinations of each chain and sets its warnings.
func (s *Service) complete(ctx context.Context, q Queries, list []Chain) error {
	if len(list) == 0 {
		return nil
	}
	if err := s.withSteps(ctx, q, list); err != nil {
		return err
	}
	if err := s.withReferrers(ctx, q, list); err != nil {
		return err
	}
	if err := s.withDestinations(ctx, q, list); err != nil {
		return err
	}
	for i := range list {
		list[i].Warnings = warningsOf(list[i])
	}
	return nil
}

// withReferrers sets the Routes and the Teams that name each chain.
func (s *Service) withReferrers(ctx context.Context, q Queries, list []Chain) error {
	ids, index := chainIndex(list)
	routes, err := q.ListRouteReferrers(ctx, dbgen.ListRouteReferrersParams{OrgID: s.orgID, ChainIds: ids})
	if err != nil {
		return fmt.Errorf("list the routes of the escalation chains: %w", err)
	}
	for _, r := range routes {
		if i, ok := index[r.ChainID]; ok {
			list[i].Referrers.Routes = append(list[i].Referrers.Routes, RouteReferrer{PublicID: r.PublicID, Name: r.Name,
				Condition: r.SeverityCondition})
		}
	}
	teams, err := q.ListTeamReferrers(ctx, dbgen.ListTeamReferrersParams{OrgID: s.orgID, ChainIds: ids})
	if err != nil {
		return fmt.Errorf("list the teams of the escalation chains: %w", err)
	}
	for _, t := range teams {
		if i, ok := index[t.ChainID]; ok {
			list[i].Referrers.Teams = append(list[i].Referrers.Teams, TeamReferrer{Condition: t.SeverityCondition,
				Team: TeamRef{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Slug: t.Slug}})
		}
	}
	return nil
}

// withDestinations sets the Destinations each chain writes in.
func (s *Service) withDestinations(ctx context.Context, q Queries, list []Chain) error {
	ids, index := chainIndex(list)
	rows, err := q.ListChainDestinations(ctx, dbgen.ListChainDestinationsParams{OrgID: s.orgID, ChainIds: ids})
	if err != nil {
		return fmt.Errorf("list the destinations of the escalation chains: %w", err)
	}
	for _, r := range rows {
		i, ok := index[r.ChainID]
		if !ok {
			continue
		}
		d := DestinationRef{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Type: r.Type, Health: r.Health,
			connection: connectionAbilities{UsergroupsRead: r.UsergroupsRead, GroupMentions: r.GroupMentions,
				Messenger: r.Messenger, Everyone: r.EveryoneMentions}}
		if r.BrokenSince.Valid {
			since := r.BrokenSince.Time.UTC()
			d.BrokenSince = &since
		}
		if r.BrokenReason.Valid {
			reason := r.BrokenReason.String
			d.BrokenReason = &reason
		}
		list[i].Destinations = append(list[i].Destinations, d)
	}
	return nil
}

func chainIndex(list []Chain) ([]int64, map[int64]int) {
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, c := range list {
		ids[i], index[c.ID] = c.ID, i
	}
	return ids, index
}

// Get reads the chain publicID; an unknown one is ErrNotFound.
func (s *Service) Get(ctx context.Context, publicID string) (Chain, error) {
	return s.get(ctx, s.store, publicID)
}

func (s *Service) get(ctx context.Context, q Queries, publicID string) (Chain, error) {
	id, err := publicid.Parse(publicid.EscalationChain, publicID)
	if err != nil {
		return Chain{}, ErrNotFound
	}
	r, err := q.GetChain(ctx, dbgen.GetChainParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Chain{}, ErrNotFound
	}
	if err != nil {
		return Chain{}, fmt.Errorf("read the escalation chain %s: %w", id, err)
	}
	out := []Chain{chainOf(chainRow(r))}
	if err := s.complete(ctx, q, out); err != nil {
		return Chain{}, err
	}
	return out[0], nil
}

// lock locks the chain publicID for a change and reads it.
func (s *Service) lock(ctx context.Context, q Queries, publicID string) (Chain, error) {
	id, err := publicid.Parse(publicid.EscalationChain, publicID)
	if err != nil {
		return Chain{}, ErrNotFound
	}
	if _, err := q.LockChain(ctx, dbgen.LockChainParams{OrgID: s.orgID, PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Chain{}, ErrNotFound
		}
		return Chain{}, fmt.Errorf("lock the escalation chain %s: %w", id, err)
	}
	return s.get(ctx, q, id)
}

// check refuses a name or a description that is not valid and returns the input with its name trimmed.
func check(in Input) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return in, &FieldError{Pointer: "/name", Code: CodeRequired, Detail: "The name is empty."}
	case utf8.RuneCountInString(in.Name) > MaxNameLength:
		return in, &FieldError{Pointer: "/name", Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", MaxNameLength)}
	case in.Description != nil && utf8.RuneCountInString(*in.Description) > MaxDescriptionLength:
		return in, &FieldError{Pointer: "/description", Code: CodeTooLong,
			Detail: fmt.Sprintf("The description is longer than %d characters.", MaxDescriptionLength)}
	}
	return in, nil
}

// resolveOwner reads the Team raw names as the owner of a chain (C-25.FR-2) in share mode, before the chain's row: nil
// keeps stored, an empty string clears it, and a Team that does not exist or is deleted is an unknown_id FieldError.
func (s *Service) resolveOwner(ctx context.Context, q Queries, raw *string, stored *TeamRef) (*TeamRef, error) {
	if raw == nil {
		return stored, nil
	}
	if *raw == "" {
		return nil, nil
	}
	unknown := &FieldError{Pointer: "/owning_team_id", Code: CodeUnknownID, Detail: "No such Team."}
	id, err := publicid.Parse(publicid.Team, *raw)
	if err != nil {
		return nil, unknown
	}
	t, err := q.LockOwningTeam(ctx, dbgen.LockOwningTeamParams{OrgID: s.orgID, PublicID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, unknown
	}
	if err != nil {
		return nil, fmt.Errorf("read the owning team %s: %w", id, err)
	}
	return &TeamRef{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Slug: t.Slug}, nil
}

func ownerID(t *TeamRef) pgtype.Int8 {
	if t == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: t.ID, Valid: true}
}

// Create creates a chain with its steps. A name another chain has is ErrNameTaken.
func (s *Service) Create(ctx context.Context, r Requester, in Input) (Chain, error) {
	in, err := check(in)
	if err != nil {
		return Chain{}, err
	}
	var created Chain
	err = s.store.InTx(ctx, func(q Queries) error {
		owner, err := s.resolveOwner(ctx, q, in.OwningTeamID, nil)
		if err != nil {
			return err
		}
		steps, err := s.resolveSteps(ctx, q, in.Steps, owner, nil)
		if err != nil {
			return err
		}
		description := ""
		if in.Description != nil {
			description = *in.Description
		}
		now := s.clock.Now().UTC()
		publicID := publicid.New(publicid.EscalationChain)
		id, err := q.InsertChain(ctx, dbgen.InsertChainParams{OrgID: s.orgID, PublicID: publicID, Name: in.Name,
			Description: description, OwningTeamID: ownerID(owner), Now: now})
		if err != nil {
			return fmt.Errorf("create the escalation chain: %w", taken(err))
		}
		if err := s.writeSteps(ctx, q, id, steps, false); err != nil {
			return err
		}
		if created, err = s.get(ctx, q, publicID); err != nil {
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
		return Chain{}, err
	}
	s.exportInfo(created.PublicID, created.Name)
	return created, nil
}

// Update replaces the name and the steps of the chain publicID, and its description and owner when given; a non-nil
// version must be its current one (If-Match). An update that changes nothing writes nothing. The people, the gateway
// and the Destination that the stored steps name pass as they are; what the update adds is checked (steps.go).
func (s *Service) Update(ctx context.Context, r Requester, publicID string, version *int64, in Input) (Chain, error) {
	in, err := check(in)
	if err != nil {
		return Chain{}, err
	}
	var before, updated Chain
	err = s.store.InTx(ctx, func(q Queries) error {
		// A new owner is read before the chain is locked: a Team's deletion locks the Team, then reads its chains.
		owner, err := s.resolveOwner(ctx, q, in.OwningTeamID, nil)
		if err != nil {
			return err
		}
		if before, err = s.lock(ctx, q, publicID); err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		if in.OwningTeamID == nil {
			owner = before.OwningTeam
		}
		steps, err := s.resolveSteps(ctx, q, in.Steps, owner, before.Steps)
		if err != nil {
			return err
		}
		next := before
		next.Name, next.OwningTeam, next.Steps = in.Name, owner, stepsOf(steps)
		if in.Description != nil {
			next.Description = *in.Description
		}
		was, will := viewOf(before), viewOf(next)
		changes := audit.Diff(was, will)
		if len(changes) == 0 {
			updated = before
			return nil
		}
		if err := q.UpdateChain(ctx, dbgen.UpdateChainParams{OrgID: s.orgID, ID: before.ID, Name: next.Name,
			Description: next.Description, OwningTeamID: ownerID(next.OwningTeam),
			Now: s.clock.Now().UTC()}); err != nil {
			return fmt.Errorf("update the escalation chain %s: %w", before.PublicID, taken(err))
		}
		// The steps are written again only when they changed, so that a rename keeps their rows.
		if !reflect.DeepEqual(was.Steps, will.Steps) {
			if err := s.writeSteps(ctx, q, before.ID, steps, true); err != nil {
				return err
			}
		}
		if updated, err = s.get(ctx, q, before.PublicID); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionUpdated, Resource: resourceOf(updated), Diff: changes, SourceAddress: r.Address}); err != nil {
			return err
		}
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: updated.PublicID}); err != nil {
			return err
		}
		// The Routes and Teams that name the chain read it by name with its owning Team (their chains).
		if was.Name == will.Name && reflect.DeepEqual(was.OwningTeamID, will.OwningTeamID) {
			return nil
		}
		return s.notifyReferrers(ctx, q, updated.Referrers)
	})
	if err != nil {
		return Chain{}, err
	}
	if updated.Name != before.Name {
		s.exportInfo(updated.PublicID, updated.Name)
	}
	return updated, nil
}

// Delete deletes the chain publicID with its steps; a non-nil version must be its current one. A chain that a Route or
// a Team that is not deleted names is refused with InUseError, read under the lock FOR UPDATE that the saves naming it
// wait for (C-32.FR-2).
func (s *Service) Delete(ctx context.Context, r Requester, publicID string, version *int64) error {
	var before Chain
	err := s.store.InTx(ctx, func(q Queries) error {
		id, err := publicid.Parse(publicid.EscalationChain, publicID)
		if err != nil {
			return ErrNotFound
		}
		if _, err := q.LockChainForDeletion(ctx, dbgen.LockChainForDeletionParams{OrgID: s.orgID,
			PublicID: id}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock the escalation chain %s: %w", id, err)
		}
		if before, err = s.get(ctx, q, id); err != nil {
			return err
		}
		if version != nil && *version != before.Version {
			return ErrVersionMismatch
		}
		if holders := holdersOf(before.Referrers); len(holders) > 0 {
			return &InUseError{Holders: holders}
		}
		if err := q.DeleteChain(ctx, dbgen.DeleteChainParams{OrgID: s.orgID, ID: before.ID}); err != nil {
			return fmt.Errorf("delete the escalation chain %s: %w", before.PublicID, err)
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

// notifyReferrers sends the live hints route and team of the Routes and Teams that name a chain.
func (s *Service) notifyReferrers(ctx context.Context, q Queries, r Referrers) error {
	for _, rt := range r.Routes {
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: hintRoute, ID: rt.PublicID}); err != nil {
			return err
		}
	}
	for _, t := range r.Teams {
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: hintTeam, ID: t.Team.PublicID}); err != nil {
			return err
		}
	}
	return nil
}

// holdersOf are the Routes, then the Teams, that name a chain.
func holdersOf(r Referrers) []Holder {
	out := make([]Holder, 0, len(r.Routes)+len(r.Teams))
	for _, rt := range r.Routes {
		out = append(out, Holder{Type: HolderRoute, PublicID: rt.PublicID, Name: rt.Name})
	}
	for _, t := range r.Teams {
		out = append(out, Holder{Type: HolderTeam, PublicID: t.Team.PublicID, Name: t.Team.Name})
	}
	return out
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

// resourceOf is a chain as the Audit log names it.
func resourceOf(c Chain) audit.Resource {
	return audit.Resource{Type: ResourceChain, PublicID: c.PublicID, Name: c.Name}
}

// view is a chain as its Audit log diff shows it: its name, description, owning Team and steps.
type view struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	OwningTeamID *string    `json:"owning_team_id"`
	Steps        []stepView `json:"steps"`
}

func viewOf(c Chain) view {
	v := view{Name: c.Name, Description: c.Description, Steps: make([]stepView, len(c.Steps))}
	if c.OwningTeam != nil {
		id := c.OwningTeam.PublicID
		v.OwningTeamID = &id
	}
	for i, st := range c.Steps {
		v.Steps[i] = st.view()
	}
	return v
}

// exportInfo sets the muster_escalation_chain_info series of a chain.
func (s *Service) exportInfo(publicID, name string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok && old != name {
		metrics.EscalationChainInfo.Delete(publicID, old)
	}
	s.info[publicID] = name
	metrics.EscalationChainInfo.With(publicID, name).Set(1)
}

// unexportInfo removes the muster_escalation_chain_info series of a chain.
func (s *Service) unexportInfo(publicID string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if old, ok := s.info[publicID]; ok {
		metrics.EscalationChainInfo.Delete(publicID, old)
		delete(s.info, publicID)
	}
}

// RefreshInfo makes muster_escalation_chain_info list exactly the chains the database has, whichever replica changed
// them (C-32.FR-19).
func (s *Service) RefreshInfo(ctx context.Context) error {
	// The read happens under the lock, so that a local change exported meanwhile is never undone by an older read.
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	rows, err := s.store.ListChainInfo(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the escalation chains for their info metric: %w", err)
	}
	want := make(map[string]string, len(rows))
	for _, r := range rows {
		want[r.PublicID] = r.Name
	}
	for id, name := range s.info {
		if want[id] != name {
			metrics.EscalationChainInfo.Delete(id, name)
			delete(s.info, id)
		}
	}
	for id, name := range want {
		s.info[id] = name
		metrics.EscalationChainInfo.With(id, name).Set(1)
	}
	return nil
}

// InfoChanged asks RunInfo to refresh muster_escalation_chain_info: a hint said a chain changed, maybe on another
// replica. It never blocks.
func (s *Service) InfoChanged() {
	select {
	case s.infoChanged <- struct{}{}:
	default:
	}
}

// RunInfo keeps muster_escalation_chain_info current until ctx ends: it refreshes it at once, after InfoChanged and at
// every tick, which also repairs a refresh that failed.
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
