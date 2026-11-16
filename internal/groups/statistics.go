// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

// statisticsQueries are the queries of the statistics per Team and by people, and of the Alert Groups whose labels
// Go matches.
type statisticsQueries interface {
	TeamStatistics(ctx context.Context, arg dbgen.TeamStatisticsParams) ([]dbgen.TeamStatisticsRow, error)
	ActorStatistics(ctx context.Context, arg dbgen.ActorStatisticsParams) ([]dbgen.ActorStatisticsRow, error)
	ListStatisticsGroupLabels(ctx context.Context, arg dbgen.ListStatisticsGroupLabelsParams) (
		[]dbgen.ListStatisticsGroupLabelsRow, error)
	ListStatisticsTeams(ctx context.Context, arg dbgen.ListStatisticsTeamsParams) ([]dbgen.ListStatisticsTeamsRow,
		error)
}

// The subjects of the statistics (group_by).
const (
	ByRoute       = "route"
	ByIntegration = "integration"
	ByTeam        = "team"
)

// StatisticsRequest asks for the statistics per Route, per Integration or per Team: GroupBy is ByRoute,
// ByIntegration or ByTeam; Filter selects the Alert Groups (C-09.FR-30) — its Routes, Integrations, Severities,
// Urgent, Team, Reopened, Matchers and FirstAcknowledgedBy, with Me the caller, and From and To the period of start,
// the last ListRange by default; the list's other filters are not read. The filter of the kind of GroupBy also narrows the
// subjects. TimeZone is the IANA zone that splits the days (organization.time_zone by default).
type StatisticsRequest struct {
	GroupBy  string
	Filter   Filter
	TimeZone string
}

// DurationStats are a count of durations with their median and 95th percentile in seconds, nil when Count is 0.
type DurationStats struct {
	Count  int64
	Median *int64
	P95    *int64
}

// StatisticsDay is one day of start in the time zone, with its Alert Groups.
type StatisticsDay struct {
	Date              time.Time
	AlertGroupCount   int64
	TimeToAcknowledge DurationStats
	TimeToResolve     DurationStats
}

// StatisticsItem is the statistics of one Route, Integration or Team: totals and the days with Alert Groups.
type StatisticsItem struct {
	Subject           Ref
	AlertGroupCount   int64
	TimeToAcknowledge DurationStats
	TimeToResolve     DurationStats
	PerDay            []StatisticsDay
}

// Statistics are the statistics of a period (AlertGroupStatistics).
type Statistics struct {
	GroupBy  string
	From, To time.Time
	Items    []StatisticsItem
}

// statisticsRow is a row of either aggregation: a total per subject, or one day of a subject.
type statisticsRow = dbgen.RouteStatisticsRow

// selection is the Alert Groups the statistics aggregate: the filters resolved as the list resolves them, and the
// ids that passed the Matchers Go applies, when there are any.
type selection struct {
	q      query
	idsSet bool
	ids    []int64
}

// selection resolves f for the statistics at now and, when f has Matchers that the database does not apply, reads
// the common labels of the Alert Groups that pass the other filters and keeps the ids of those that match, as Counts
// matches them, so that the aggregation runs over the same Alert Groups either way.
func (s *Service) selection(ctx context.Context, f Filter, now time.Time) (selection, error) {
	q, err := s.resolve(ctx, f, now)
	if err != nil {
		return selection{}, err
	}
	sel := selection{q: q, ids: []int64{}}
	if len(q.rowwise) == 0 {
		return sel, nil
	}
	rows, err := s.store.CustomPlans().ListStatisticsGroupLabels(ctx, dbgen.ListStatisticsGroupLabelsParams{
		OrgID: s.orgID, RangeFrom: q.from, RangeTo: q.to, RouteIds: q.routes, IntegrationIds: q.integrations,
		Severities: q.severities, Urgent: q.urgent, Reopened: q.reopened, TeamSet: q.teamSet, TeamIds: q.teamIDs,
		Contains: q.contains, FirstAcknowledgedBy: q.firstAckBy})
	if err != nil {
		return selection{}, fmt.Errorf("read the labels of the alert groups of the statistics: %w", err)
	}
	sel.idsSet = true
	for _, r := range rows {
		if ok, err := matchesLabels(q.rowwise, r.CommonLabels); err != nil {
			return selection{}, err
		} else if ok {
			sel.ids = append(sel.ids, r.Ids...)
		}
	}
	return sel, nil
}

// params are the parameters of the aggregations per subject in the time zone.
func (sel selection) params(orgID int64, zone string) dbgen.RouteStatisticsParams {
	q := sel.q
	return dbgen.RouteStatisticsParams{TimeZone: zone, OrgID: orgID, RangeFrom: q.from, RangeTo: q.to,
		RouteIds: q.routes, IntegrationIds: q.integrations, Severities: q.severities, Urgent: q.urgent,
		Reopened: q.reopened, TeamSet: q.teamSet, TeamIds: q.teamIDs, Contains: q.contains,
		FirstAcknowledgedBy: q.firstAckBy, IdsSet: sel.idsSet, Ids: sel.ids}
}

// Statistics aggregates the summary rows that started in the period and match the filters per Route, per Integration
// or per Team (C-09.FR-15, FR-29, FR-30): the number of Alert Groups, time to resolve of the resolved ones and time to
// acknowledge of those acknowledged at least once, as count, median and 95th percentile, in total and per day of start
// in the time zone. An Alert Group with Alerts from several Integrations counts for each, and one with several Teams
// for each Team. The items are every Route, Integration or Team that is not deleted and the deleted ones with Alert
// Groups in the period, or only the chosen ones — the caller's Teams for TeamMine, none when the caller is in
// none — by name.
func (s *Service) Statistics(ctx context.Context, r StatisticsRequest) (Statistics, error) {
	sel, err := s.selection(ctx, r.Filter, s.clock.Now())
	if err != nil {
		return Statistics{}, err
	}
	zone := r.TimeZone
	if zone == "" {
		settings, err := s.store.GetListSettings(ctx, s.orgID)
		if err != nil {
			return Statistics{}, fmt.Errorf("read the time zone of the organization: %w", err)
		}
		zone = settings.TimeZone
	}
	if _, err := time.LoadLocation(zone); err != nil || zone == "Local" {
		return Statistics{}, errUnknownZone
	}
	out := Statistics{GroupBy: r.GroupBy, From: sel.q.from, To: sel.q.to, Items: []StatisticsItem{}}
	p := sel.params(s.orgID, zone)
	db := s.store.CustomPlans()
	var only []int64
	var rows []statisticsRow
	switch r.GroupBy {
	case ByIntegration:
		only = sel.q.integrations
		got, err := db.IntegrationStatistics(ctx, dbgen.IntegrationStatisticsParams(p))
		if err != nil {
			return Statistics{}, zoneError(fmt.Errorf("aggregate the alert groups per integration: %w", err))
		}
		for _, g := range got {
			rows = append(rows, statisticsRow(g))
		}
	case ByTeam:
		if sel.q.teamSet && len(sel.q.teamIDs) == 0 {
			return out, nil
		}
		only = sel.q.teamIDs
		got, err := db.TeamStatistics(ctx, dbgen.TeamStatisticsParams(p))
		if err != nil {
			return Statistics{}, zoneError(fmt.Errorf("aggregate the alert groups per team: %w", err))
		}
		for _, g := range got {
			rows = append(rows, statisticsRow(g))
		}
	default:
		only = sel.q.routes
		if rows, err = db.RouteStatistics(ctx, p); err != nil {
			return Statistics{}, zoneError(fmt.Errorf("aggregate the alert groups per route: %w", err))
		}
	}
	subjects, err := s.subjects(ctx, r.GroupBy, orNone(only), rows)
	if err != nil {
		return Statistics{}, err
	}
	bySubject := map[int64]*StatisticsItem{}
	for _, ref := range subjects {
		out.Items = append(out.Items, StatisticsItem{Subject: Ref{PublicID: ref.PublicID, Name: ref.Name},
			PerDay: []StatisticsDay{}})
	}
	for i, ref := range subjects {
		bySubject[ref.ID] = &out.Items[i]
	}
	for _, row := range rows {
		item := bySubject[row.SubjectID]
		if item == nil {
			continue
		}
		ack := durationStats(row.AckCount, row.AckMedian, row.AckP95)
		resolve := durationStats(row.ResolveCount, row.ResolveMedian, row.ResolveP95)
		if row.Total {
			item.AlertGroupCount, item.TimeToAcknowledge, item.TimeToResolve = row.AlertGroupCount, ack, resolve
			continue
		}
		item.PerDay = append(item.PerDay, StatisticsDay{Date: dateOf(row.Day), AlertGroupCount: row.AlertGroupCount,
			TimeToAcknowledge: ack, TimeToResolve: resolve})
	}
	return out, nil
}

// subjects lists the Routes, Integrations or Teams the items stand for: only the chosen ones, or every one that is
// not deleted and those with Alert Groups in the rows.
func (s *Service) subjects(ctx context.Context, groupBy string, only []int64, rows []statisticsRow) ([]idRef,
	error) {
	with := []int64{}
	for _, r := range rows {
		with = append(with, r.SubjectID)
	}
	var out []idRef
	switch groupBy {
	case ByIntegration:
		got, err := s.store.ListStatisticsIntegrations(ctx, dbgen.ListStatisticsIntegrationsParams{OrgID: s.orgID,
			OnlyIds: only, WithIds: with})
		if err != nil {
			return nil, fmt.Errorf("list the integrations of the statistics: %w", err)
		}
		for _, r := range got {
			out = append(out, idRef(r))
		}
	case ByTeam:
		got, err := s.store.ListStatisticsTeams(ctx, dbgen.ListStatisticsTeamsParams{OrgID: s.orgID, OnlyIds: only,
			WithIds: with})
		if err != nil {
			return nil, fmt.Errorf("list the teams of the statistics: %w", err)
		}
		for _, r := range got {
			out = append(out, idRef(r))
		}
	default:
		got, err := s.store.ListStatisticsRoutes(ctx, dbgen.ListStatisticsRoutesParams{OrgID: s.orgID,
			OnlyIds: only, WithIds: with})
		if err != nil {
			return nil, fmt.Errorf("list the routes of the statistics: %w", err)
		}
		for _, r := range got {
			out = append(out, idRef(r))
		}
	}
	return out, nil
}

// ActorStatisticsItem is one row of the statistics by people: a User or a Service account, or the Alert Groups whose
// first acknowledgement names nobody when Actor is nil. TimeToAcknowledge is over the Alert Groups it acknowledged
// first, whose number is AcknowledgedFirst.
type ActorStatisticsItem struct {
	Actor             *ActorRef
	AcknowledgedFirst int64
	Resolved          int64
	TimeToAcknowledge DurationStats
}

// ActorStatistics are the statistics by people of a period (AlertGroupActorStatistics).
type ActorStatistics struct {
	From, To time.Time
	Items    []ActorStatisticsItem
}

// errUnknownActor is a row of the statistics whose User or Service account cannot be read.
var errUnknownActor = errors.New("the statistics name a user or service account that does not exist")

// ActorStatistics aggregates the Alert Groups that started in the period and match f by people (C-09.FR-28): per User
// the Alert Groups they acknowledged first, with time to acknowledge, and those they resolved; per Service account
// those it resolved; and the Alert Groups acknowledged whose first acknowledgement names nobody as an item without
// an Actor, last. The items go by AcknowledgedFirst, most first, then by name and public_id; deleted Users and
// Service accounts are marked Deactivated.
func (s *Service) ActorStatistics(ctx context.Context, f Filter) (ActorStatistics, error) {
	sel, err := s.selection(ctx, f, s.clock.Now())
	if err != nil {
		return ActorStatistics{}, err
	}
	out := ActorStatistics{From: sel.q.from, To: sel.q.to, Items: []ActorStatisticsItem{}}
	p := sel.params(s.orgID, "")
	rows, err := s.store.CustomPlans().ActorStatistics(ctx, dbgen.ActorStatisticsParams{OrgID: p.OrgID,
		RangeFrom: p.RangeFrom, RangeTo: p.RangeTo, RouteIds: p.RouteIds, IntegrationIds: p.IntegrationIds,
		Severities: p.Severities, Urgent: p.Urgent, Reopened: p.Reopened, TeamSet: p.TeamSet, TeamIds: p.TeamIds,
		Contains: p.Contains, FirstAcknowledgedBy: p.FirstAcknowledgedBy, IdsSet: p.IdsSet, Ids: p.Ids})
	if err != nil {
		return ActorStatistics{}, fmt.Errorf("aggregate the alert groups by people: %w", err)
	}
	var userIDs, accountIDs []int64
	for _, r := range rows {
		userIDs = append(userIDs, r.AcknowledgedByUserID.Int64, r.ResolvedByUserID.Int64)
		accountIDs = append(accountIDs, r.ServiceAccountID.Int64)
	}
	actors, err := s.refs(ctx, userIDs, accountIDs)
	if err != nil {
		return ActorStatistics{}, err
	}
	for _, r := range rows {
		item := ActorStatisticsItem{AcknowledgedFirst: r.AcknowledgedFirstCount, Resolved: r.ResolvedCount,
			TimeToAcknowledge: durationStats(r.AcknowledgedFirstCount, r.AckMedian, r.AckP95)}
		user := r.AcknowledgedByUserID
		if r.AcknowledgedFirstCount == 0 {
			user = r.ResolvedByUserID
		}
		if user.Valid || r.ServiceAccountID.Valid {
			if item.Actor = actors.actor(user, r.ServiceAccountID); item.Actor == nil {
				return ActorStatistics{}, errUnknownActor
			}
		}
		out.Items = append(out.Items, item)
	}
	slices.SortFunc(out.Items, func(a, b ActorStatisticsItem) int {
		if a.Actor == nil || b.Actor == nil {
			return cmp.Compare(boolRank(a.Actor == nil), boolRank(b.Actor == nil))
		}
		return cmp.Or(cmp.Compare(b.AcknowledgedFirst, a.AcknowledgedFirst), cmp.Compare(a.Actor.Name, b.Actor.Name),
			cmp.Compare(a.Actor.PublicID, b.Actor.PublicID))
	})
	return out, nil
}

// boolRank orders false before true.
func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// errUnknownZone is a time zone that is not an IANA name, for Go or for the database.
var errUnknownZone = &FieldError{Pointer: "/query/time_zone", Code: CodeInvalid,
	Detail: "The time zone is not an IANA time zone name."}

// zoneError is errUnknownZone when the database does not know a time zone that Go knows (invalid_parameter_value,
// 22023, as its time zone data is older), err otherwise.
func zoneError(err error) error {
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok && pe.Code == "22023" {
		return errUnknownZone
	}
	return err
}

// durationStats are the stats of count durations, rounded to whole seconds; median and p95 are nil without any.
func durationStats(count int64, median, p95 float64) DurationStats {
	out := DurationStats{Count: count}
	if count > 0 {
		m, p := int64(math.Round(median)), int64(math.Round(p95))
		out.Median, out.P95 = &m, &p
	}
	return out
}

// dateOf is a calendar date as midnight UTC.
func dateOf(d pgtype.Date) time.Time {
	return time.Date(d.Time.Year(), d.Time.Month(), d.Time.Day(), 0, 0, 0, 0, time.UTC)
}
