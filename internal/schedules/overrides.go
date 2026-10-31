// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/schedules/dbgen"
)

// Actor is who made an Override: a User or a Service account, deactivated once deleted.
type Actor struct {
	Kind        string
	PublicID    string
	Name        string
	Login       string
	Deactivated bool
}

// Override puts User On-call from Start to End on top of every Rotation layer; Replaces, the person whose Shift it
// takes, changes nothing in the On-call (C-27.FR-3).
type Override struct {
	ID        int64
	PublicID  string
	User      Person
	Replaces  *Person
	Start     time.Time
	End       time.Time
	CreatedBy Actor
	CreatedAt time.Time
	Version   int64
}

// OverrideInput is what createScheduleOverride and updateScheduleOverride write; an empty ReplacesUserID is none.
type OverrideInput struct {
	UserID         string
	ReplacesUserID string
	Start          time.Time
	End            time.Time
}

// OverrideFilter selects a page of the Overrides of a Schedule by start: those that end after From, the business
// clock's now when nil, and start before To when set, after the position After.
type OverrideFilter struct {
	From  *time.Time
	To    *time.Time
	After *OverridePosition
	Limit int
}

// OverridePosition is the place of an Override in the list: its start and its id.
type OverridePosition struct {
	Start time.Time `json:"s"`
	ID    int64     `json:"i"`
}

// OverridePage is a page of Overrides; Next is nil on the last page.
type OverridePage struct {
	Overrides []Override
	Next      *OverridePosition
}

// overrideRow is a row of an Override with its people as every read gives it.
type overrideRow struct {
	ID                       int64
	PublicID                 string
	UserID                   int64
	StartsAt                 time.Time
	EndsAt                   time.Time
	Version                  int64
	CreatedAt                time.Time
	UserPublicID             string
	UserName                 string
	UserLogin                string
	UserStatus               string
	ReplacesPublicID         pgtype.Text
	ReplacesName             pgtype.Text
	ReplacesLogin            pgtype.Text
	ReplacesStatus           pgtype.Text
	ByUserPublicID           pgtype.Text
	ByUserName               pgtype.Text
	ByUserLogin              pgtype.Text
	ByUserStatus             pgtype.Text
	ByServiceAccountPublicID pgtype.Text
	ByServiceAccountName     pgtype.Text
	ByServiceAccountStatus   pgtype.Text
}

func overrideOf(r overrideRow) Override {
	o := Override{ID: r.ID, PublicID: r.PublicID, Start: r.StartsAt.UTC(), End: r.EndsAt.UTC(),
		CreatedAt: r.CreatedAt.UTC(), Version: r.Version,
		User: Person{ID: r.UserID, PublicID: r.UserPublicID, Name: r.UserName, Login: r.UserLogin,
			Status: r.UserStatus}}
	if r.ReplacesPublicID.Valid {
		o.Replaces = &Person{PublicID: r.ReplacesPublicID.String, Name: r.ReplacesName.String,
			Login: r.ReplacesLogin.String, Status: r.ReplacesStatus.String}
	}
	if r.ByUserPublicID.Valid {
		o.CreatedBy = Actor{Kind: string(audit.ActorUser), PublicID: r.ByUserPublicID.String, Name: r.ByUserName.String,
			Login: r.ByUserLogin.String, Deactivated: r.ByUserStatus.String == "deleted"}
	} else {
		o.CreatedBy = Actor{Kind: string(audit.ActorServiceAccount), PublicID: r.ByServiceAccountPublicID.String,
			Name: r.ByServiceAccountName.String, Deactivated: r.ByServiceAccountStatus.String == "deleted"}
	}
	return o
}

// Overrides lists a page of the Overrides of the Schedule scheduleID by start.
func (s *Service) Overrides(ctx context.Context, scheduleID string, f OverrideFilter) (OverridePage, error) {
	sc, err := s.get(ctx, s.store, scheduleID)
	if err != nil {
		return OverridePage{}, err
	}
	limit := min(max(f.Limit, 1), 500)
	p := dbgen.ListScheduleOverridesParams{OrgID: s.orgID, ScheduleID: sc.ID, PeriodStart: s.clock.Now().UTC(),
		PageSize: int32(limit) + 1}
	if f.From != nil {
		p.PeriodStart = f.From.UTC()
	}
	if f.To != nil {
		p.PeriodEnd = pgtype.Timestamptz{Time: f.To.UTC(), Valid: true}
	}
	if f.After != nil {
		p.AfterStart = pgtype.Timestamptz{Time: f.After.Start, Valid: true}
		p.AfterID = pgtype.Int8{Int64: f.After.ID, Valid: true}
	}
	rows, err := s.store.ListScheduleOverrides(ctx, p)
	if err != nil {
		return OverridePage{}, fmt.Errorf("list the overrides of the schedule %s: %w", sc.PublicID, err)
	}
	page := OverridePage{Overrides: make([]Override, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			page.Next = &OverridePosition{Start: last.StartsAt.UTC(), ID: last.ID}
			break
		}
		page.Overrides = append(page.Overrides, overrideOf(overrideRow{ID: r.ID, PublicID: r.PublicID,
			StartsAt: r.StartsAt, EndsAt: r.EndsAt, Version: r.Version, CreatedAt: r.CreatedAt,
			UserPublicID: r.UserPublicID, UserName: r.UserName, UserLogin: r.UserLogin, UserStatus: r.UserStatus,
			ReplacesPublicID: r.ReplacesPublicID, ReplacesName: r.ReplacesName, ReplacesLogin: r.ReplacesLogin,
			ReplacesStatus: r.ReplacesStatus, ByUserPublicID: r.ByUserPublicID, ByUserName: r.ByUserName,
			ByUserLogin: r.ByUserLogin, ByUserStatus: r.ByUserStatus,
			ByServiceAccountPublicID: r.ByServiceAccountPublicID, ByServiceAccountName: r.ByServiceAccountName,
			ByServiceAccountStatus: r.ByServiceAccountStatus}))
	}
	return page, nil
}

// lockSchedule locks the Schedule publicID for a change of its Overrides and reads it.
func (s *Service) lockSchedule(ctx context.Context, q Queries, publicID string) (Schedule, error) {
	id, err := publicid.Parse(publicid.Schedule, publicID)
	if err != nil {
		return Schedule{}, ErrNotFound
	}
	if _, err := q.LockSchedule(ctx, dbgen.LockScheduleParams{OrgID: s.orgID, PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Schedule{}, ErrNotFound
		}
		return Schedule{}, fmt.Errorf("lock the schedule %s: %w", id, err)
	}
	return s.get(ctx, q, id)
}

// getOverride reads the Override publicID of the Schedule sc, locked for a change.
func (s *Service) getOverride(ctx context.Context, q Queries, sc Schedule, publicID string) (Override, error) {
	id, err := publicid.Parse(publicid.ScheduleOverride, publicID)
	if err != nil {
		return Override{}, ErrOverrideNotFound
	}
	if _, err := q.LockScheduleOverride(ctx, dbgen.LockScheduleOverrideParams{OrgID: s.orgID, ScheduleID: sc.ID,
		PublicID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Override{}, ErrOverrideNotFound
		}
		return Override{}, fmt.Errorf("lock the override %s: %w", id, err)
	}
	r, err := q.GetScheduleOverride(ctx, dbgen.GetScheduleOverrideParams{OrgID: s.orgID, ScheduleID: sc.ID,
		PublicID: id})
	if err != nil {
		return Override{}, fmt.Errorf("read the override %s: %w", id, err)
	}
	return overrideOf(overrideRow{ID: r.ID, PublicID: r.PublicID, UserID: r.UserID, StartsAt: r.StartsAt,
		EndsAt: r.EndsAt, Version: r.Version, CreatedAt: r.CreatedAt, UserPublicID: r.UserPublicID,
		UserName: r.UserName, UserLogin: r.UserLogin, UserStatus: r.UserStatus, ReplacesPublicID: r.ReplacesPublicID,
		ReplacesName: r.ReplacesName, ReplacesLogin: r.ReplacesLogin, ReplacesStatus: r.ReplacesStatus,
		ByUserPublicID: r.ByUserPublicID, ByUserName: r.ByUserName, ByUserLogin: r.ByUserLogin,
		ByUserStatus: r.ByUserStatus, ByServiceAccountPublicID: r.ByServiceAccountPublicID,
		ByServiceAccountName: r.ByServiceAccountName, ByServiceAccountStatus: r.ByServiceAccountStatus}), nil
}

// precondition refuses an Override by someone who is neither in the Team of the Schedule nor an Admin (C-27.FR-6),
// checked after the Permission.
func (s *Service) precondition(ctx context.Context, q Queries, r Requester, sc Schedule) error {
	if r.Admin {
		return nil
	}
	if r.Actor.Kind != audit.ActorUser {
		return ErrNotInTeam
	}
	in, err := q.IsTeamUser(ctx, dbgen.IsTeamUserParams{OrgID: s.orgID, TeamID: sc.Team.ID, UserID: r.Actor.ID})
	if err != nil {
		return fmt.Errorf("read whether the caller is in the team %s: %w", sc.Team.PublicID, err)
	}
	if !in {
		return ErrNotInTeam
	}
	return nil
}

// overridePeople checks an OverrideInput against the Schedule sc and the business clock's now: the person who takes
// the Shift exists and may be Paged, the person whose Shift it takes exists, and the end is after the start and after
// now (P-91). It returns the ids of the two people, 0 for none.
func (s *Service) overridePeople(ctx context.Context, q Queries, sc Schedule, in OverrideInput, now time.Time) (
	int64, int64, error) {
	user, err := s.lookup(ctx, q, in.UserID, "/user_id")
	if err != nil {
		return 0, 0, err
	}
	var replaces int64
	if in.ReplacesUserID != "" {
		if replaces, err = s.lookup(ctx, q, in.ReplacesUserID, "/replaces_user_id"); err != nil {
			return 0, 0, err
		}
	}
	reasons, err := s.pageable(ctx, q, sc.Team.ID, []int64{user})
	if err != nil {
		return 0, 0, err
	}
	if reason := reasons[user]; reason != "" {
		return 0, 0, notPageable("/user_id", reason)
	}
	switch {
	case !in.End.After(in.Start):
		return 0, 0, &FieldError{Pointer: "/end", Code: CodeOutOfRange, Detail: "The end is not after the start."}
	case !in.End.After(now):
		return 0, 0, &FieldError{Pointer: "/end", Code: CodeOutOfRange,
			Detail: "An Override may not end in the past."}
	}
	return user, replaces, nil
}

// lookup reads the internal id of the User publicID who is not deleted; another is an unknown_id FieldError at
// pointer.
func (s *Service) lookup(ctx context.Context, q Queries, publicID, pointer string) (int64, error) {
	unknown := &FieldError{Pointer: pointer, Code: CodeUnknownID, Detail: "No such User."}
	id, err := publicid.Parse(publicid.User, publicID)
	if err != nil {
		return 0, unknown
	}
	rows, err := q.LookupUsers(ctx, dbgen.LookupUsersParams{OrgID: s.orgID, PublicIds: []string{id}})
	if err != nil {
		return 0, fmt.Errorf("read the user %s: %w", id, err)
	}
	if len(rows) == 0 {
		return 0, unknown
	}
	return rows[0].ID, nil
}

// createdBy are the columns of the maker of an Override: the User, else the Service account.
func createdBy(a audit.Actor) (pgtype.Int8, pgtype.Int8) {
	if a.Kind == audit.ActorServiceAccount {
		return pgtype.Int8{}, pgtype.Int8{Int64: a.ID, Valid: true}
	}
	return pgtype.Int8{Int64: a.ID, Valid: true}, pgtype.Int8{}
}

// CreateOverride creates an Override of the Schedule scheduleID (C-27.FR-3, FR-6): no approval and no message.
func (s *Service) CreateOverride(ctx context.Context, r Requester, scheduleID string, in OverrideInput) (Override,
	error) {
	var created Override
	err := s.store.InTx(ctx, func(q Queries) error {
		sc, err := s.lockSchedule(ctx, q, scheduleID)
		if err != nil {
			return err
		}
		if err := s.precondition(ctx, q, r, sc); err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		user, replaces, err := s.overridePeople(ctx, q, sc, in, now)
		if err != nil {
			return err
		}
		byUser, bySA := createdBy(r.Actor)
		id := publicid.New(publicid.ScheduleOverride)
		if _, err := q.InsertScheduleOverride(ctx, dbgen.InsertScheduleOverrideParams{OrgID: s.orgID, PublicID: id,
			ScheduleID: sc.ID, UserID: user, ReplacesUserID: pgtype.Int8{Int64: replaces, Valid: replaces != 0},
			StartsAt: in.Start.UTC(), EndsAt: in.End.UTC(), CreatedByUserID: byUser,
			CreatedByServiceAccountID: bySA, Now: now}); err != nil {
			return fmt.Errorf("create the override: %w", err)
		}
		if created, err = s.getOverride(ctx, q, sc, id); err != nil {
			return err
		}
		return s.recordOverride(ctx, q, r, sc, ActionOverrideCreated, created, audit.Created(overrideViewOf(created)))
	})
	if err != nil {
		return Override{}, err
	}
	return created, nil
}

// UpdateOverride replaces the people and the span of the Override overrideID of the Schedule scheduleID; a non-nil
// version must be its current one. An Override that has ended is ErrOverrideEnded (settled 3).
func (s *Service) UpdateOverride(ctx context.Context, r Requester, scheduleID, overrideID string, version *int64,
	in OverrideInput) (Override, error) {
	var updated Override
	err := s.store.InTx(ctx, func(q Queries) error {
		sc, before, now, err := s.lockOverride(ctx, q, r, scheduleID, overrideID, version)
		if err != nil {
			return err
		}
		user, replaces, err := s.overridePeople(ctx, q, sc, in, now)
		if err != nil {
			return err
		}
		if err := q.UpdateScheduleOverride(ctx, dbgen.UpdateScheduleOverrideParams{OrgID: s.orgID, ID: before.ID,
			UserID: user, ReplacesUserID: pgtype.Int8{Int64: replaces, Valid: replaces != 0},
			StartsAt: in.Start.UTC(), EndsAt: in.End.UTC(), Now: now}); err != nil {
			return fmt.Errorf("update the override %s: %w", before.PublicID, err)
		}
		if updated, err = s.getOverride(ctx, q, sc, before.PublicID); err != nil {
			return err
		}
		return s.recordOverride(ctx, q, r, sc, ActionOverrideUpdated, updated,
			audit.Diff(overrideViewOf(before), overrideViewOf(updated)))
	})
	if err != nil {
		return Override{}, err
	}
	return updated, nil
}

// DeleteOverride deletes the Override overrideID of the Schedule scheduleID; a non-nil version must be its current
// one. An Override that has ended is ErrOverrideEnded, so that the past Result stays as it was.
func (s *Service) DeleteOverride(ctx context.Context, r Requester, scheduleID, overrideID string,
	version *int64) error {
	return s.store.InTx(ctx, func(q Queries) error {
		sc, before, _, err := s.lockOverride(ctx, q, r, scheduleID, overrideID, version)
		if err != nil {
			return err
		}
		if err := q.DeleteScheduleOverride(ctx, dbgen.DeleteScheduleOverrideParams{OrgID: s.orgID,
			ID: before.ID}); err != nil {
			return fmt.Errorf("delete the override %s: %w", before.PublicID, err)
		}
		return s.recordOverride(ctx, q, r, sc, ActionOverrideDeleted, before, deleted(overrideViewOf(before)))
	})
}

// lockOverride locks the Schedule and its Override for a change by r and checks, in order, the precondition, the
// version and that the Override has not ended; it returns the business clock's now.
func (s *Service) lockOverride(ctx context.Context, q Queries, r Requester, scheduleID, overrideID string,
	version *int64) (Schedule, Override, time.Time, error) {
	sc, err := s.lockSchedule(ctx, q, scheduleID)
	if err != nil {
		return Schedule{}, Override{}, time.Time{}, err
	}
	o, err := s.getOverride(ctx, q, sc, overrideID)
	if err != nil {
		return Schedule{}, Override{}, time.Time{}, err
	}
	if err := s.precondition(ctx, q, r, sc); err != nil {
		return Schedule{}, Override{}, time.Time{}, err
	}
	if version != nil && *version != o.Version {
		return Schedule{}, Override{}, time.Time{}, ErrVersionMismatch
	}
	now := s.clock.Now().UTC()
	if !o.End.After(now) {
		return Schedule{}, Override{}, time.Time{}, ErrOverrideEnded
	}
	return sc, o, now, nil
}

// recordOverride records a change of an Override in the Audit log, with its people and span, and announces the
// Schedule with its live hint.
func (s *Service) recordOverride(ctx context.Context, q Queries, r Requester, sc Schedule, action string, o Override,
	diff []audit.Change) error {
	details := map[string]any{"schedule": sc.PublicID, "schedule_name": sc.Name, "user": o.User.PublicID,
		"start": o.Start, "end": o.End}
	if o.Replaces != nil {
		details["replaces_user"] = o.Replaces.PublicID
	}
	if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
		Action: action, Resource: audit.Resource{Type: ResourceOverride, PublicID: o.PublicID, Name: sc.Name},
		Diff: diff, Details: details, SourceAddress: r.Address}); err != nil {
		return err
	}
	return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: sc.PublicID})
}

// overrideView is an Override as its Audit log diff shows it.
type overrideView struct {
	UserID         string    `json:"user_id"`
	ReplacesUserID *string   `json:"replaces_user_id"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
}

func overrideViewOf(o Override) overrideView {
	v := overrideView{UserID: o.User.PublicID, Start: o.Start, End: o.End}
	if o.Replaces != nil {
		id := o.Replaces.PublicID
		v.ReplacesUserID = &id
	}
	return v
}

// OnCall is the Result of the Schedule scheduleID from from to to: by default from the business clock's now to
// schedule.gap_view after it. A from or a to more than Reach from now is an out_of_range FieldError at /query/from or
// /query/to; so is a to that is not after from, or later than the length of ical.window after it.
func (s *Service) OnCall(ctx context.Context, scheduleID string, from, to *time.Time) (Result, error) {
	now := s.clock.Now().UTC()
	start := now
	if from != nil {
		start = from.UTC()
	}
	end := start.Add(GapView)
	if to != nil {
		end = to.UTC()
	}
	if start.Before(now.Add(-Reach)) || start.After(now.Add(Reach)) {
		return Result{}, &FieldError{Pointer: "/query/from", Code: CodeOutOfRange,
			Detail: "The start of the period is at most 10 years from now."}
	}
	if end.Before(now.Add(-Reach)) || end.After(now.Add(Reach)) {
		return Result{}, &FieldError{Pointer: "/query/to", Code: CodeOutOfRange,
			Detail: "The end of the period is at most 10 years from now."}
	}
	if !end.After(start) || end.Sub(start) > MaxPeriod {
		return Result{}, &FieldError{Pointer: "/query/to", Code: CodeOutOfRange,
			Detail: "The end of the period is after its start and at most 67 days after it."}
	}
	sc, err := s.get(ctx, s.store, scheduleID)
	if err != nil {
		return Result{}, err
	}
	return s.result(ctx, s.store, sc, start, end)
}

// OnCallAt is the person On-call in the Schedule scheduleID at t and what decides it, nil in a gap (C-27.FR-4).
func (s *Service) OnCallAt(ctx context.Context, scheduleID string, t time.Time) (Segment, error) {
	sc, err := s.get(ctx, s.store, scheduleID)
	if err != nil {
		return Segment{}, err
	}
	res, err := s.result(ctx, s.store, sc, t, t.Add(time.Second))
	if err != nil {
		return Segment{}, err
	}
	return res.Segments[0], nil
}

// result is the Result of the Schedule sc from from to to, with whether its people may be Paged read once from their
// current state.
func (s *Service) result(ctx context.Context, q Queries, sc Schedule, from, to time.Time) (Result, error) {
	loc, err := location(sc.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	rows, err := q.ListPeriodOverrides(ctx, dbgen.ListPeriodOverridesParams{OrgID: s.orgID, ScheduleID: sc.ID,
		PeriodStart: from, PeriodEnd: to})
	if err != nil {
		return Result{}, fmt.Errorf("list the overrides of the schedule %s: %w", sc.PublicID, err)
	}
	overrides := make([]Override, len(rows))
	var ids []int64
	for i, r := range rows {
		overrides[i] = overrideOf(overrideRow(r))
		ids = append(ids, r.UserID)
	}
	for _, l := range sc.Layers {
		for _, u := range l.Users {
			ids = append(ids, u.ID)
		}
	}
	skipped, err := s.pageable(ctx, q, sc.Team.ID, ids)
	if err != nil {
		return Result{}, err
	}
	return compute(loc, sc.Layers, overrides, skipped, from, to), nil
}
