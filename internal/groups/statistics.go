// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

// The subjects of the statistics (group_by).
const (
	ByRoute       = "route"
	ByIntegration = "integration"
)

// StatisticsRequest asks for the statistics per Route or per Integration: GroupBy is ByRoute or ByIntegration, Routes
// or Integrations (public_ids, the kind of GroupBy only) narrow the subjects, From and To are the period of start
// (the last ListRange by default) and TimeZone the IANA zone that splits the days (organization.time_zone by default).
type StatisticsRequest struct {
	GroupBy      string
	Routes       []string
	Integrations []string
	From, To     *time.Time
	TimeZone     string
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

// StatisticsItem is the statistics of one Route or Integration: totals and the days with Alert Groups.
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

// Statistics aggregates the summary rows that started in the period per Route or per Integration (C-09.FR-15): the
// number of Alert Groups, time to resolve of the resolved ones and time to acknowledge of those acknowledged at least
// once, as count, median and 95th percentile, in total and per day of start in the time zone. An Alert Group with
// Alerts from several Integrations counts for each. The items are every Route or Integration that is not deleted and
// the deleted ones with Alert Groups in the period, or only the chosen ones, by name.
func (s *Service) Statistics(ctx context.Context, r StatisticsRequest) (Statistics, error) {
	switch {
	case r.GroupBy == ByRoute && len(r.Integrations) > 0:
		return Statistics{}, &FieldError{Pointer: "/query/integration", Code: CodeUnsupported,
			Detail: "Integrations narrow the statistics per Integration only."}
	case r.GroupBy == ByIntegration && len(r.Routes) > 0:
		return Statistics{}, &FieldError{Pointer: "/query/route", Code: CodeUnsupported,
			Detail: "Routes narrow the statistics per Route only."}
	}
	from, to, err := Range(r.From, r.To, s.clock.Now())
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
	out := Statistics{GroupBy: r.GroupBy, From: from, To: to, Items: []StatisticsItem{}}
	var only []int64
	var rows []statisticsRow
	if r.GroupBy == ByIntegration {
		if only, err = s.integrationIDs(ctx, r.Integrations, "/query/integration"); err != nil {
			return Statistics{}, err
		}
		got, err := s.store.IntegrationStatistics(ctx, dbgen.IntegrationStatisticsParams{OrgID: s.orgID,
			TimeZone: zone, RangeFrom: from, RangeTo: to, IntegrationIds: orNone(only)})
		if err != nil {
			return Statistics{}, zoneError(fmt.Errorf("aggregate the alert groups per integration: %w", err))
		}
		for _, g := range got {
			rows = append(rows, statisticsRow(g))
		}
	} else {
		if only, err = s.routeIDs(ctx, r.Routes, "/query/route"); err != nil {
			return Statistics{}, err
		}
		if rows, err = s.store.RouteStatistics(ctx, dbgen.RouteStatisticsParams{OrgID: s.orgID, TimeZone: zone,
			RangeFrom: from, RangeTo: to, RouteIds: orNone(only)}); err != nil {
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

// subjects lists the Routes or Integrations the items stand for: only the chosen ones, or every one that is not
// deleted and those with Alert Groups in the rows.
func (s *Service) subjects(ctx context.Context, groupBy string, only []int64, rows []statisticsRow) ([]idRef,
	error) {
	with := []int64{}
	for _, r := range rows {
		with = append(with, r.SubjectID)
	}
	var out []idRef
	if groupBy == ByIntegration {
		got, err := s.store.ListStatisticsIntegrations(ctx, dbgen.ListStatisticsIntegrationsParams{OrgID: s.orgID,
			OnlyIds: only, WithIds: with})
		if err != nil {
			return nil, fmt.Errorf("list the integrations of the statistics: %w", err)
		}
		for _, r := range got {
			out = append(out, idRef(r))
		}
		return out, nil
	}
	got, err := s.store.ListStatisticsRoutes(ctx, dbgen.ListStatisticsRoutesParams{OrgID: s.orgID, OnlyIds: only,
		WithIds: with})
	if err != nil {
		return nil, fmt.Errorf("list the routes of the statistics: %w", err)
	}
	for _, r := range got {
		out = append(out, idRef(r))
	}
	return out, nil
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
