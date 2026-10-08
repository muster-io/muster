// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/organization"
)

// listQueries are the queries of the list, the counts, the related Alert Groups, the statistics and the open count of
// the Integrations.
type listQueries interface {
	ListGroupsStartedDesc(ctx context.Context, arg dbgen.ListGroupsStartedDescParams) (
		[]dbgen.ListGroupsStartedDescRow, error)
	ListGroupsStartedAsc(ctx context.Context, arg dbgen.ListGroupsStartedAscParams) (
		[]dbgen.ListGroupsStartedAscRow, error)
	ListGroupsChangedDesc(ctx context.Context, arg dbgen.ListGroupsChangedDescParams) (
		[]dbgen.ListGroupsChangedDescRow, error)
	ListGroupsChangedAsc(ctx context.Context, arg dbgen.ListGroupsChangedAscParams) (
		[]dbgen.ListGroupsChangedAscRow, error)
	CountGroups(ctx context.Context, arg dbgen.CountGroupsParams) ([]dbgen.CountGroupsRow, error)
	GetListSettings(ctx context.Context, orgID int64) (dbgen.GetListSettingsRow, error)
	ListRoutesByPublicID(ctx context.Context, arg dbgen.ListRoutesByPublicIDParams) (
		[]dbgen.ListRoutesByPublicIDRow, error)
	ListIntegrationsByPublicID(ctx context.Context, arg dbgen.ListIntegrationsByPublicIDParams) (
		[]dbgen.ListIntegrationsByPublicIDRow, error)
	ListUsersByPublicID(ctx context.Context, arg dbgen.ListUsersByPublicIDParams) ([]dbgen.ListUsersByPublicIDRow,
		error)
	GetGroupKey(ctx context.Context, arg dbgen.GetGroupKeyParams) (dbgen.GetGroupKeyRow, error)
	ListRelatedGroups(ctx context.Context, arg dbgen.ListRelatedGroupsParams) ([]dbgen.ListRelatedGroupsRow, error)
	RouteStatistics(ctx context.Context, arg dbgen.RouteStatisticsParams) ([]dbgen.RouteStatisticsRow, error)
	IntegrationStatistics(ctx context.Context, arg dbgen.IntegrationStatisticsParams) (
		[]dbgen.IntegrationStatisticsRow, error)
	ListStatisticsRoutes(ctx context.Context, arg dbgen.ListStatisticsRoutesParams) (
		[]dbgen.ListStatisticsRoutesRow, error)
	ListStatisticsIntegrations(ctx context.Context, arg dbgen.ListStatisticsIntegrationsParams) (
		[]dbgen.ListStatisticsIntegrationsRow, error)
	CountOpenGroupsByIntegration(ctx context.Context, arg dbgen.CountOpenGroupsByIntegrationParams) (
		[]dbgen.CountOpenGroupsByIntegrationRow, error)
}

// Sort is the order of the list: by start or by last change, a leading - for newest first.
type Sort string

// The orders of the list (AgSort).
const (
	SortStartedDesc Sort = "-started_at"
	SortStarted     Sort = "started_at"
	SortChangedDesc Sort = "-last_changed_at"
	SortChanged     Sort = "last_changed_at"
)

// ListPosition is the place of an Alert Group in the list: the time it is sorted by and its id.
type ListPosition struct {
	At time.Time
	ID int64
}

// ListRequest is a page of the list: the filters, the order, the position after which the page starts, its size and
// the labels whose shared value each item carries.
type ListRequest struct {
	Filter
	Sort         Sort
	After        *ListPosition
	Limit        int
	LabelColumns []string
}

// ListPage is a page of the list; Next is nil on the last page.
type ListPage struct {
	Groups []View
	Next   *ListPosition
}

// The batches of a list whose label Matchers Go applies: their size, and how many rows one page reads at most before
// it returns what it found with the position to go on from.
const (
	minBatch = 100
	maxBatch = 1000
	maxScan  = 10000
)

// listRow is a row of any of the four orders of the list.
type listRow = dbgen.ListGroupsStartedDescRow

// List reads a page of the Alert Group list (C-09.FR-13): the summary rows that match the filters, in the order of
// the request, after its position. Label Matchers other than = with a value are matched in Go on the common labels,
// batch after batch, until the page is full; a page that has read maxScan rows returns what it found and the position
// to go on from. Each item carries its Route, Integrations, resolution, urgency, details_removed and label_values.
func (s *Service) List(ctx context.Context, r ListRequest) (ListPage, error) {
	now := s.clock.Now().UTC()
	q, err := s.resolve(ctx, r.Filter, now)
	if err != nil {
		return ListPage{}, err
	}
	settings, err := s.store.GetListSettings(ctx, s.orgID)
	if err != nil {
		return ListPage{}, fmt.Errorf("read the list settings: %w", err)
	}
	limit := max(r.Limit, 1)
	size := limit + 1
	if len(q.rowwise) > 0 {
		size = min(max(2*limit, minBatch), maxBatch)
	}
	var rows []listRow
	var next *ListPosition
	after, scanned := r.After, 0
	for {
		batch, err := s.listBatch(ctx, q, r.Sort, after, size)
		if err != nil {
			return ListPage{}, err
		}
		full := false
		for _, row := range batch {
			if ok, err := matchesLabels(q.rowwise, row.CommonLabels); err != nil {
				return ListPage{}, err
			} else if !ok {
				continue
			}
			if len(rows) == limit {
				p := positionOf(rows[limit-1], r.Sort)
				next, full = &p, true
				break
			}
			rows = append(rows, row)
		}
		if full || len(batch) < size {
			break
		}
		p := positionOf(batch[len(batch)-1], r.Sort)
		after = &p
		if scanned += len(batch); scanned >= maxScan {
			next = after
			break
		}
	}
	views, err := s.views(ctx, rows, settings.RetentionAlertDetailsDays, now, r.LabelColumns)
	if err != nil {
		return ListPage{}, err
	}
	return ListPage{Groups: views, Next: next}, nil
}

// matchesLabels reports whether the common labels, as JSON, match every Matcher.
func matchesLabels(ms []matchers.Matcher, raw []byte) (bool, error) {
	if len(ms) == 0 {
		return true, nil
	}
	var labels map[string]string
	if err := json.Unmarshal(raw, &labels); err != nil {
		return false, fmt.Errorf("read the common labels: %w", err)
	}
	return matchers.All(ms, labels), nil
}

// positionOf is the place of a row in the order.
func positionOf(r listRow, sort Sort) ListPosition {
	if sort == SortChanged || sort == SortChangedDesc {
		return ListPosition{At: r.LastChangedAt.UTC(), ID: r.ID}
	}
	return ListPosition{At: r.CreatedAt.UTC(), ID: r.ID}
}

// listBatch reads up to size rows of the list in the order, after the position when given.
func (s *Service) listBatch(ctx context.Context, q query, sort Sort, after *ListPosition, size int) ([]listRow,
	error) {
	p := dbgen.ListGroupsStartedDescParams{OrgID: s.orgID, Statuses: q.statuses, Number: q.number, RangeTo: q.to,
		RangeFrom: q.from, RouteIds: q.routes, IntegrationIds: q.integrations, Severities: q.severities,
		Urgent: q.urgent, ResolvedBy: q.resolvedBy, ResolveReason: q.reason, Reopened: q.reopened,
		Contains: q.contains, Pattern: q.pattern, OwnerSet: q.ownerSet, OwnerID: q.owner, SnoozedNoEnd: q.snoozedNoEnd,
		DeliveryProblem: q.problem, Lim: int32(size)} //nolint:gosec // G115: size is at most maxBatch
	if after != nil {
		p.AfterAt = pgtype.Timestamptz{Time: after.At, Valid: true}
		p.AfterID = pgtype.Int8{Int64: after.ID, Valid: true}
	}
	db := s.store.CustomPlans()
	var out []listRow
	var err error
	switch sort {
	case SortStarted:
		var rows []dbgen.ListGroupsStartedAscRow
		rows, err = db.ListGroupsStartedAsc(ctx, dbgen.ListGroupsStartedAscParams(p))
		for _, r := range rows {
			out = append(out, listRow(r))
		}
	case SortChangedDesc:
		var rows []dbgen.ListGroupsChangedDescRow
		rows, err = db.ListGroupsChangedDesc(ctx, dbgen.ListGroupsChangedDescParams(p))
		for _, r := range rows {
			out = append(out, listRow(r))
		}
	case SortChanged:
		var rows []dbgen.ListGroupsChangedAscRow
		rows, err = db.ListGroupsChangedAsc(ctx, dbgen.ListGroupsChangedAscParams(p))
		for _, r := range rows {
			out = append(out, listRow(r))
		}
	default:
		out, err = db.ListGroupsStartedDesc(ctx, p)
	}
	if err != nil {
		return nil, fmt.Errorf("list the alert groups: %w", err)
	}
	return out, nil
}

// views turns the rows of a page into Alert Groups, naming their Integrations, and who resolved, owns or snoozed them,
// in one read each.
func (s *Service) views(ctx context.Context, rows []listRow, retentionDays int64, now time.Time,
	columns []string) ([]View, error) {
	out := make([]View, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	var integrationIDs, userIDs, accountIDs []int64
	for _, r := range rows {
		integrationIDs = append(integrationIDs, r.IntegrationIds...)
		userIDs = append(userIDs, r.ResolvedByUserID.Int64, r.OwnerUserID.Int64, r.SnoozedByUserID.Int64)
		accountIDs = append(accountIDs, r.ResolvedByServiceAccountID.Int64, r.SnoozedByServiceAccountID.Int64)
	}
	slices.Sort(integrationIDs)
	integrationIDs = slices.Compact(integrationIDs)
	names := map[int64]Ref{}
	if len(integrationIDs) > 0 {
		refs, err := s.store.ListIntegrationRefs(ctx, dbgen.ListIntegrationRefsParams{OrgID: s.orgID,
			Ids: integrationIDs})
		if err != nil {
			return nil, fmt.Errorf("read the integrations of the alert groups: %w", err)
		}
		for _, ref := range refs {
			names[ref.ID] = Ref{PublicID: ref.PublicID, Name: ref.Name}
		}
	}
	actors, err := s.refs(ctx, userIDs, accountIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		v := View{PublicID: r.PublicID, Number: r.Number, Title: r.Title, Summary: textOf(r.Summary),
			Status: Status(r.Status), Severity: organization.SeverityLevel(r.SeverityLevel), Urgent: r.Urgent,
			Route: Ref{PublicID: r.RoutePublicID, Name: r.RouteName}, StartedAt: r.CreatedAt.UTC(),
			LastChangedAt: r.LastChangedAt.UTC(), ResolvedAt: timeOf(r.ResolvedAt), ReopenCount: r.ReopenCount,
			FiringCount: r.FiringAlertCount, ResolvedCount: r.ResolvedAlertCount, Integrations: []Ref{},
			DetailsRemoved: detailsRemoved(r.ResolvedAt, retentionDays, now)}
		for _, id := range r.IntegrationIds {
			if ref, ok := names[id]; ok {
				v.Integrations = append(v.Integrations, ref)
			}
		}
		slices.SortFunc(v.Integrations, func(a, b Ref) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.PublicID, b.PublicID))
		})
		if r.ResolvedByKind.Valid {
			v.Resolution = &Resolution{By: r.ResolvedByKind.String, Reason: textOf(r.ResolveReasonText),
				ReasonCode: textOf(r.ResolveReason),
				Actor:      actors.actor(r.ResolvedByUserID, r.ResolvedByServiceAccountID)}
		}
		v.owned(actors, r.OwnerUserID, r.SnoozedByUserID, r.SnoozedByServiceAccountID, r.SnoozeUntil, r.NewerPublicID,
			r.NewerNumber)
		v.stillFiring, v.routeDeleted, v.DeliveryProblem = r.FiringAlertCount, r.RouteDeleted, r.DeliveryProblem
		if len(columns) > 0 {
			var common map[string]string
			if err := json.Unmarshal(r.CommonLabels, &common); err != nil {
				return nil, fmt.Errorf("read the common labels of alert group #%d: %w", r.Number, err)
			}
			v.LabelValues = map[string]string{}
			for _, c := range columns {
				if value, ok := common[c]; ok {
					v.LabelValues[c] = value
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}
