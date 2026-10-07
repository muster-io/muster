// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package groups is the Alert Group lifecycle (C-09, ADR-0003, ADR-0004, ADR-0016): grouping of newly firing Alerts,
// the state machine with its system transitions, the Timeline with the lifecycle events, and the reads of one Alert
// Group. Every change of an Alert Group passes through the dispatcher; only this package writes alert_groups,
// alert_group_alerts, timeline_entries, notes and alert_group_counters (lint 2).
package groups

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/publicid"
)

// Status is the status of an Alert Group (C-09.FR-1).
type Status string

// The four statuses of ADR-0004.
const (
	StatusFiring       Status = "firing"
	StatusAcknowledged Status = "acknowledged"
	StatusSnoozed      Status = "snoozed"
	StatusResolved     Status = "resolved"
)

// The kinds of who resolved an Alert Group.
const (
	ResolvedByUser   = "user"
	ResolvedBySystem = "system"
)

// The timer kinds of this capability.
const (
	TimerReopenWindowEnd = "reopen_window_end"
	TimerGracePeriodEnd  = "grace_period_end"
)

// TimersChannel is the LISTEN/NOTIFY channel that wakes the timer workers of every replica when a timer is set.
const TimersChannel = "muster_timers"

var (
	// ErrNotFound is an Alert Group that does not exist in the Organization.
	ErrNotFound = errors.New("no such alert group")
	// ErrRouteNotFound is a Route that does not exist in the Organization or is deleted.
	ErrRouteNotFound = errors.New("no such route")
	// ErrDefaultRoute is a move of the Default route's own Alert Groups.
	ErrDefaultRoute = errors.New("the default route has no alert groups to move")
)

// Queries are the queries of the package, with the insert of the Audit log.
type Queries interface {
	GetGroupingSettings(ctx context.Context, orgID int64) (dbgen.GetGroupingSettingsRow, error)
	ListChangedAlerts(ctx context.Context, arg dbgen.ListChangedAlertsParams) ([]dbgen.ListChangedAlertsRow, error)
	ListFiringMemberships(ctx context.Context, arg dbgen.ListFiringMembershipsParams) (
		[]dbgen.ListFiringMembershipsRow, error)
	LockRoutes(ctx context.Context, arg dbgen.LockRoutesParams) ([]dbgen.LockRoutesRow, error)
	LockDefaultRoute(ctx context.Context, orgID int64) (dbgen.LockDefaultRouteRow, error)
	GetRoutePolicy(ctx context.Context, arg dbgen.GetRoutePolicyParams) (dbgen.GetRoutePolicyRow, error)
	FindOpenGroup(ctx context.Context, arg dbgen.FindOpenGroupParams) (int64, error)
	FindReopenableGroup(ctx context.Context, arg dbgen.FindReopenableGroupParams) (int64, error)
	PeekGroup(ctx context.Context, arg dbgen.PeekGroupParams) (dbgen.PeekGroupRow, error)
	LockGroups(ctx context.Context, arg dbgen.LockGroupsParams) ([]dbgen.LockGroupsRow, error)
	EnsureCounter(ctx context.Context, orgID int64) error
	LockCounter(ctx context.Context, orgID int64) (int64, error)
	NextNumber(ctx context.Context, orgID int64) (int64, error)
	InsertGroup(ctx context.Context, arg dbgen.InsertGroupParams) (int64, error)
	SaveGroup(ctx context.Context, arg dbgen.SaveGroupParams) error
	IsActiveUser(ctx context.Context, arg dbgen.IsActiveUserParams) (bool, error)
	InsertMemberships(ctx context.Context, arg dbgen.InsertMembershipsParams) error
	ResolveMemberships(ctx context.Context, arg dbgen.ResolveMembershipsParams) error
	MoveMemberships(ctx context.Context, arg dbgen.MoveMembershipsParams) error
	UpdateMemberships(ctx context.Context, arg dbgen.UpdateMembershipsParams) error
	ListGroupFiringAlerts(ctx context.Context, arg dbgen.ListGroupFiringAlertsParams) (
		[]dbgen.ListGroupFiringAlertsRow, error)
	InsertTimelineEntry(ctx context.Context, arg dbgen.InsertTimelineEntryParams) error
	ListOpenGroupIDs(ctx context.Context, orgID int64) ([]int64, error)
	InsertDowntimeEntries(ctx context.Context, arg dbgen.InsertDowntimeEntriesParams) error
	UpsertTimer(ctx context.Context, arg dbgen.UpsertTimerParams) error
	DeleteTimer(ctx context.Context, arg dbgen.DeleteTimerParams) error
	NotifyTimers(ctx context.Context, channel string) error
	LockRouteForMove(ctx context.Context, arg dbgen.LockRouteForMoveParams) (dbgen.LockRouteForMoveRow, error)
	GetDefaultRouteID(ctx context.Context, orgID int64) (int64, error)
	ListOpenGroupsOfRoute(ctx context.Context, arg dbgen.ListOpenGroupsOfRouteParams) ([]int64, error)
	// Notify sends a live-update hint once the transaction commits.
	Notify(ctx context.Context, h db.Hint) error
	readQueries
	listQueries
	retentionQueries
	audit.Store
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
	// CustomPlans are the queries over a connection that plans each statement with its parameters, for the list,
	// whose optional filters a generic plan of a prepared statement could not use the indexes of.
	CustomPlans() Queries
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{Queries: newQueries(pool), pool: pool, custom: newQueries(customPlans{db: pool})}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
	exec db.Execer
}

// newQueries are the queries over a pool or a transaction.
func newQueries(d dbgen.DBTX) Queries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d), exec: d}
}

func (q pgQueries) Notify(ctx context.Context, h db.Hint) error {
	return db.NotifyHint(ctx, q.exec, h)
}

type pgStore struct {
	Queries
	pool   *pgxpool.Pool
	custom Queries
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(newQueries(tx)) })
}

func (s pgStore) CustomPlans() Queries { return s.custom }

// customPlans runs the statements of a pool as unnamed statements, which PostgreSQL plans with the parameters they
// are bound to, instead of the cached prepared statements whose generic plan ignores them: pgx takes a QueryExecMode
// as the first argument.
type customPlans struct {
	db dbgen.DBTX
}

func (c customPlans) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return c.db.Exec(ctx, sql, append([]any{pgx.QueryExecModeDescribeExec}, args...)...)
}

func (c customPlans) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return c.db.Query(ctx, sql, append([]any{pgx.QueryExecModeDescribeExec}, args...)...)
}

func (c customPlans) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return c.db.QueryRow(ctx, sql, append([]any{pgx.QueryExecModeDescribeExec}, args...)...)
}

// Actor is who changes an Alert Group and through which Transport. System transitions are made by Muster itself
// with the Transport system; S-032 adds the people and automation of Commands.
type Actor struct {
	Kind      audit.ActorKind
	Transport audit.Transport
}

// System is Muster as the actor of a system transition.
var System = Actor{Kind: audit.ActorSystem, Transport: audit.TransportSystem}

// Prior is the status an Alert Group returns to when it reopens within its Reopen window (C-09.FR-4).
type Prior struct {
	Status                  Status
	OwnerUserID             *int64
	SnoozeUntil             *time.Time
	SnoozeNoEnd             bool
	SnoozedWhileUrgent      bool
	SnoozedByUserID         *int64
	SnoozedByServiceAccount *int64
}

// Group is the working state of one alert_groups row while a transaction holds its lock.
type Group struct {
	ID       int64
	PublicID string
	Number   int64
	RouteID  int64
	// MovedFromRouteID is the deleted Route of an Alert Group moved to the Default route.
	MovedFromRouteID *int64
	KeyLabels        []string
	KeyValues        map[string]string
	KeySHA           []byte
	Title            string
	TitleFromKey     bool
	Summary          *string
	CommonLabels     map[string]string
	CommonAnnots     map[string]string
	IntegrationIDs   []int64
	Status           Status
	Severity         organization.SeverityLevel
	Urgent           bool

	OwnerUserID             *int64
	AcknowledgedAt          *time.Time
	SnoozeUntil             *time.Time
	SnoozeNoEnd             bool
	SnoozedWhileUrgent      bool
	SnoozedByUserID         *int64
	SnoozedByServiceAccount *int64

	FiringCount, ResolvedCount, ReopenCount int64

	ResolvedAt               *time.Time
	ResolvedByKind           *string
	ResolvedByUserID         *int64
	ResolvedByServiceAccount *int64
	ResolveReason            *string
	ResolveReasonText        *string

	ReopenDeadline     *time.Time
	Prior              *Prior
	GraceDeadline      *time.Time
	FiringAgainAfterID *int64

	EventSeq      int64
	CreatedAt     time.Time
	LastChangedAt time.Time
}

// groupOf reads a locked row.
func groupOf(r dbgen.LockGroupsRow) (*Group, error) {
	g := &Group{
		ID: r.ID, PublicID: r.PublicID, Number: r.Number, RouteID: r.RouteID, MovedFromRouteID: int8Of(r.MovedFromRouteID),
		KeyLabels: r.GroupKeyLabels, KeySHA: r.GroupKeySha256, Title: r.Title, TitleFromKey: r.TitleFromGroupKey,
		Summary: textOf(r.Summary), IntegrationIDs: r.IntegrationIds, Status: Status(r.Status),
		Severity: organization.SeverityLevel(r.SeverityLevel), Urgent: r.Urgent, OwnerUserID: int8Of(r.OwnerUserID),
		AcknowledgedAt: timeOf(r.AcknowledgedAt), SnoozeUntil: timeOf(r.SnoozeUntil), SnoozeNoEnd: r.SnoozeNoEnd,
		SnoozedWhileUrgent: r.SnoozedWhileUrgent, SnoozedByUserID: int8Of(r.SnoozedByUserID),
		SnoozedByServiceAccount: int8Of(r.SnoozedByServiceAccountID), FiringCount: r.FiringAlertCount,
		ResolvedCount: r.ResolvedAlertCount, ReopenCount: r.ReopenCount, ResolvedAt: timeOf(r.ResolvedAt),
		ResolvedByKind: textOf(r.ResolvedByKind), ResolvedByUserID: int8Of(r.ResolvedByUserID),
		ResolvedByServiceAccount: int8Of(r.ResolvedByServiceAccountID), ResolveReason: textOf(r.ResolveReason),
		ResolveReasonText: textOf(r.ResolveReasonText), ReopenDeadline: timeOf(r.ReopenDeadline),
		GraceDeadline: timeOf(r.GraceDeadline), FiringAgainAfterID: int8Of(r.FiringAgainAfterID),
		EventSeq: r.EventSeq, CreatedAt: r.CreatedAt, LastChangedAt: r.LastChangedAt,
	}
	if r.PriorStatus.Valid {
		g.Prior = &Prior{Status: Status(r.PriorStatus.String), OwnerUserID: int8Of(r.PriorOwnerUserID),
			SnoozeUntil: timeOf(r.PriorSnoozeUntil), SnoozeNoEnd: r.PriorSnoozeNoEnd.Bool,
			SnoozedWhileUrgent: r.PriorSnoozedWhileUrgent.Bool, SnoozedByUserID: int8Of(r.PriorSnoozedByUserID),
			SnoozedByServiceAccount: int8Of(r.PriorSnoozedByServiceAccountID)}
	}
	for _, f := range []struct {
		raw  []byte
		into *map[string]string
	}{{r.GroupKeyValues, &g.KeyValues}, {r.CommonLabels, &g.CommonLabels}, {r.CommonAnnotations, &g.CommonAnnots}} {
		if err := json.Unmarshal(f.raw, f.into); err != nil {
			return nil, fmt.Errorf("read alert group %s: %w", r.PublicID, err)
		}
	}
	return g, nil
}

// saveParams are the columns a change writes back.
func (g *Group) saveParams(orgID int64) dbgen.SaveGroupParams {
	p := dbgen.SaveGroupParams{
		OrgID: orgID, ID: g.ID, RouteID: g.RouteID, MovedFromRouteID: nullInt(g.MovedFromRouteID), Title: g.Title,
		TitleFromGroupKey: g.TitleFromKey, Summary: text(g.Summary), CommonLabels: jsonOf(g.CommonLabels),
		CommonAnnotations: jsonOf(g.CommonAnnots), IntegrationIds: g.IntegrationIDs, Status: string(g.Status),
		SeverityLevel: string(g.Severity), Urgent: g.Urgent, OwnerUserID: nullInt(g.OwnerUserID),
		AcknowledgedAt: timestamptz(g.AcknowledgedAt), SnoozeUntil: timestamptz(g.SnoozeUntil),
		SnoozeNoEnd: g.SnoozeNoEnd, SnoozedWhileUrgent: g.SnoozedWhileUrgent, SnoozedByUserID: nullInt(g.SnoozedByUserID),
		SnoozedByServiceAccountID: nullInt(g.SnoozedByServiceAccount), FiringAlertCount: g.FiringCount,
		ResolvedAlertCount: g.ResolvedCount, ReopenCount: g.ReopenCount, ResolvedAt: timestamptz(g.ResolvedAt),
		ResolvedByKind: text(g.ResolvedByKind), ResolvedByUserID: nullInt(g.ResolvedByUserID),
		ResolvedByServiceAccountID: nullInt(g.ResolvedByServiceAccount), ResolveReason: text(g.ResolveReason),
		ResolveReasonText: text(g.ResolveReasonText), ReopenDeadline: timestamptz(g.ReopenDeadline),
		GraceDeadline: timestamptz(g.GraceDeadline), EventSeq: g.EventSeq, LastChangedAt: g.LastChangedAt,
	}
	if g.Prior != nil {
		p.PriorStatus = pgtype.Text{String: string(g.Prior.Status), Valid: true}
		p.PriorOwnerUserID = nullInt(g.Prior.OwnerUserID)
		p.PriorSnoozeUntil = timestamptz(g.Prior.SnoozeUntil)
		p.PriorSnoozeNoEnd = pgtype.Bool{Bool: g.Prior.SnoozeNoEnd, Valid: true}
		p.PriorSnoozedWhileUrgent = pgtype.Bool{Bool: g.Prior.SnoozedWhileUrgent, Valid: true}
		p.PriorSnoozedByUserID = nullInt(g.Prior.SnoozedByUserID)
		p.PriorSnoozedByServiceAccountID = nullInt(g.Prior.SnoozedByServiceAccount)
	}
	return p
}

// entry is one Timeline entry a transition records: a lifecycle event of the table, or a system entry.
type entry struct {
	Event         Event
	Variant       Variant
	System        SystemEvent
	Reason        string
	From, To      Status
	OwnerUserID   *int64
	PreviousOwner *int64
	SnoozeUntil   *time.Time
	Fingerprints  []string
	ReplacedLabel string
	Conflicts     []string
	PeriodFrom    *time.Time
	PeriodTo      *time.Time
}

// change is what one transition does to a locked Alert Group: the entries it records and, for a status change, the
// reason it logs. A change without entries that still writes the row — the end of a Reopen window or of a Grace
// period — sets written; it records nothing and leaves the time of the last change.
type change struct {
	entries []entry
	reason  string
	written bool
}

func (c *change) add(e entry) { c.entries = append(c.entries, e) }

// transition is one change of an Alert Group: its precondition on the locked row and the transition itself, which
// changes g and records what it did in c.
type transition interface {
	precondition(g *Group) error
	apply(ctx context.Context, q Queries, g *Group, c *change) error
}

// committed collects what runs once the transaction that changed Alert Groups committed: the counters and the log
// lines of status changes, which must not count a change that rolled back.
type committed []func(ctx context.Context)

func (c *committed) add(f func(ctx context.Context)) { *c = append(*c, f) }

// run calls every collected function.
func (c committed) run(ctx context.Context) {
	for _, f := range c {
		f(ctx)
	}
}

// dispatcher is the one way an Alert Group changes (ADR-0004, ADR-0016): permission → precondition → transition →
// Audit log → Timeline → re-render, inside the caller's transaction, on a row the caller locked FOR UPDATE, and the
// live-update hints of the change once the transaction commits. System transitions need no Permission and write no
// Audit log entry; S-032 adds both steps for Commands.
type dispatcher struct {
	orgID int64
	clock clock.Clock
	log   *logging.Logger
	// rerender is the re-render hook that delivery fills from S-034; nil until then.
	rerender func(ctx context.Context, q Queries, g *Group) error
	// routeIDs names Routes by public_id for the metrics and the log lines.
	routeIDs func(ctx context.Context, q Queries, id int64) (string, error)
}

// dispatch runs t on the locked Alert Group g as actor and records what it did; after are the counters and log lines
// to run once the transaction committed.
func (d *dispatcher) dispatch(ctx context.Context, q Queries, g *Group, actor Actor, t transition,
	after *committed) error {
	if err := t.precondition(g); err != nil {
		return err
	}
	from := g.Status
	var c change
	now := d.clock.Now().UTC()
	if err := t.apply(ctx, q, g, &c); err != nil {
		return err
	}
	if len(c.entries) == 0 && !c.written {
		return nil
	}
	if len(c.entries) > 0 {
		if err := d.record(ctx, q, g, actor, now, c.entries); err != nil {
			return err
		}
		g.LastChangedAt = now
	}
	if err := q.SaveGroup(ctx, g.saveParams(d.orgID)); err != nil {
		return fmt.Errorf("save alert group #%d: %w", g.Number, err)
	}
	if d.rerender != nil {
		if err := d.rerender(ctx, q, g); err != nil {
			return err
		}
	}
	if err := d.hint(ctx, q, g, from); err != nil {
		return err
	}
	if from != g.Status {
		if err := d.statusChanged(ctx, q, g, from, c.reason, actor, after); err != nil {
			return err
		}
	}
	return nil
}

// record writes the entries of one change to the Timeline, numbering the lifecycle events of g.
func (d *dispatcher) record(ctx context.Context, q Queries, g *Group, actor Actor, at time.Time, es []entry) error {
	for _, e := range es {
		p := dbgen.InsertTimelineEntryParams{
			OrgID: d.orgID, PublicID: publicid.New(publicid.TimelineEntry), AlertGroupID: g.ID, At: at,
			Mentions: []string{}, ActorKind: string(actor.Kind), Transport: string(actor.Transport),
			Reason: nonEmpty(e.Reason), FromStatus: nonEmpty(string(e.From)), ToStatus: nonEmpty(string(e.To)),
			OwnerUserID: nullInt(e.OwnerUserID), PreviousOwnerUserID: nullInt(e.PreviousOwner),
			SnoozeUntil: timestamptz(e.SnoozeUntil), Fingerprints: e.Fingerprints,
			ReplacedLabel: nonEmpty(e.ReplacedLabel), LabelConflicts: e.Conflicts,
			PeriodFrom: timestamptz(e.PeriodFrom), PeriodTo: timestamptz(e.PeriodTo),
		}
		if e.Event != "" {
			row := rowOf(e.Event, e.Variant)
			g.EventSeq++
			p.Kind = string(row.Kind)
			p.Event = pgtype.Text{String: string(row.Event), Valid: true}
			p.EventSeq = pgtype.Int8{Int64: g.EventSeq, Valid: true}
			p.Loudness = pgtype.Text{String: string(row.Loudness), Valid: true}
			for _, m := range row.Mentions {
				p.Mentions = append(p.Mentions, string(m))
			}
		}
		if e.System != "" {
			p.Kind = string(KindSystem)
			p.SystemEvent = pgtype.Text{String: string(e.System), Valid: true}
		}
		if err := q.InsertTimelineEntry(ctx, p); err != nil {
			return fmt.Errorf("record %s on alert group #%d: %w", cmp.Or(string(e.Event), string(e.System)), g.Number,
				err)
		}
	}
	return nil
}

// statusChanged counts a status change and logs it once the transaction committed.
func (d *dispatcher) statusChanged(ctx context.Context, q Queries, g *Group, from Status, reason string, actor Actor,
	after *committed) error {
	route, err := d.routeIDs(ctx, q, g.RouteID)
	if err != nil {
		return err
	}
	number, to, started := g.Number, g.Status, g.CreatedAt
	resolvedAt := g.LastChangedAt
	by := ResolvedBySystem
	if g.ResolvedByKind != nil {
		by = *g.ResolvedByKind
	}
	after.add(func(ctx context.Context) {
		switch {
		case from == "":
			metrics.AlertGroupsCreated.With(route).Inc()
		case from == StatusResolved:
			metrics.AlertGroupsReopened.With(route).Inc()
		case to == StatusResolved:
			metrics.AlertGroupsResolved.With(route, by).Inc()
			metrics.AlertGroupTimeToResolve.With(route).Update(max(resolvedAt.Sub(started).Seconds(), 0))
		}
		d.log.Log(ctx, logging.AlertGroupStatusChanged, logging.F("group", fmt.Sprintf("#%d", number)),
			logging.F("route", route), logging.F("from", string(from)), logging.F("to", string(to)),
			logging.F("reason", reason), logging.F("transport", string(actor.Transport)))
	})
	return nil
}

// lock locks the Alert Groups ids in id order and returns them by id.
func lock(ctx context.Context, q Queries, orgID int64, ids []int64) (map[int64]*Group, error) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	rows, err := q.LockGroups(ctx, dbgen.LockGroupsParams{OrgID: orgID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("lock alert groups: %w", err)
	}
	out := make(map[int64]*Group, len(rows))
	for _, r := range rows {
		g, err := groupOf(r)
		if err != nil {
			return nil, err
		}
		out[g.ID] = g
	}
	return out, nil
}

// setTimer sets the deadline of an Alert Group's timer and wakes the timer workers once the transaction commits.
func setTimer(ctx context.Context, q Queries, orgID, groupID int64, kind string, deadline, now time.Time) error {
	if err := q.UpsertTimer(ctx, dbgen.UpsertTimerParams{OrgID: orgID, AlertGroupID: groupID, Kind: kind,
		Deadline: deadline, Now: now}); err != nil {
		return fmt.Errorf("set the %s timer: %w", kind, err)
	}
	if err := q.NotifyTimers(ctx, TimersChannel); err != nil {
		return fmt.Errorf("wake the timer workers: %w", err)
	}
	return nil
}

// stopTimer deletes an Alert Group's timer of a kind.
func stopTimer(ctx context.Context, q Queries, orgID, groupID int64, kind string) error {
	if err := q.DeleteTimer(ctx, dbgen.DeleteTimerParams{OrgID: orgID, AlertGroupID: groupID, Kind: kind}); err != nil {
		return fmt.Errorf("stop the %s timer: %w", kind, err)
	}
	return nil
}

func int8Of(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func nullInt(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func timestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func textOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func text(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func nonEmpty(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// jsonOf encodes a map of strings, which always encodes.
func jsonOf(m map[string]string) []byte {
	if m == nil {
		m = map[string]string{}
	}
	b, _ := json.Marshal(m)
	return b
}

func ptr[T any](v T) *T { return &v }
