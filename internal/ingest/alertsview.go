// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/integrations"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/publicid"
)

// The sorts of the Alerts view; a leading - sorts descending.
const (
	SortLastSeen     = "last_seen_at"
	SortLastSeenDesc = "-last_seen_at"
	SortStarts       = "starts_at"
	SortStartsDesc   = "-starts_at"
)

// maxScan bounds the Alerts one request of the view reads to fill its page; a filter that matches few Alerts gets a
// shorter page with a cursor to continue from.
const maxScan = 10_000

// viewQueries are the queries of the Alerts view.
type viewQueries interface {
	FindViewIntegration(ctx context.Context, arg dbgen.FindViewIntegrationParams) (dbgen.FindViewIntegrationRow, error)
	ListViewAlertsByLastSeen(ctx context.Context, arg dbgen.ListViewAlertsByLastSeenParams) (
		[]dbgen.ListViewAlertsByLastSeenRow, error)
	ListViewAlertsByLastSeenAsc(ctx context.Context, arg dbgen.ListViewAlertsByLastSeenAscParams) (
		[]dbgen.ListViewAlertsByLastSeenAscRow, error)
	ListViewAlertsByStartsAt(ctx context.Context, arg dbgen.ListViewAlertsByStartsAtParams) (
		[]dbgen.ListViewAlertsByStartsAtRow, error)
	ListViewAlertsByStartsAtAsc(ctx context.Context, arg dbgen.ListViewAlertsByStartsAtAscParams) (
		[]dbgen.ListViewAlertsByStartsAtAscRow, error)
	ListViewGroupKeys(ctx context.Context, arg dbgen.ListViewGroupKeysParams) ([]dbgen.ListViewGroupKeysRow, error)
	ListViewAlertGroups(ctx context.Context, arg dbgen.ListViewAlertGroupsParams) ([]dbgen.ListViewAlertGroupsRow,
		error)
}

// AlertPosition is the sort key of an Alert in the view: the sorted time and the id.
type AlertPosition struct {
	At time.Time
	ID int64
}

// AlertFilter selects a page of the Alerts view of the Integration whose public_id is Integration: in State when it
// is set, matching every Matcher, with a label value containing Query (case-insensitive) when it is set, sorted by
// Sort, after the position After when it is set.
type AlertFilter struct {
	Integration string
	State       string
	Matchers    []matchers.Matcher
	Query       string
	Sort        string
	After       *AlertPosition
	Limit       int
}

// ViewAlert is an Alert as the view shows it (IntegrationAlert).
type ViewAlert struct {
	ID          int64
	Fingerprint string
	Labels      map[string]string
	Annotations map[string]string
	State       string
	Reason      *string
	ReasonText  *string
	StartsAt    time.Time
	ResolvedAt  *time.Time
	LastSeenAt  time.Time
	// GroupKeys are the groupKeys that listed it during its current or last firing; Warnings the Static labels it
	// already carried.
	GroupKeys []string
	Warnings  []string
	// Route is the Route that took its current or last firing, nil before routing; SeverityLevel its Severity level
	// and SeverityRaw the severity value as received when it has no mapping (C-06.FR-19, C-08.FR-13).
	Route         *RouteRef
	SeverityLevel *string
	SeverityRaw   *string
	// AlertGroup is the Alert Group it fires in, or last fired in; nil before grouping (C-06.FR-19).
	AlertGroup *AlertGroupRef
}

// AlertGroupRef names an Alert Group by its public_id and #N.
type AlertGroupRef struct {
	PublicID string
	Number   int64
}

// RouteRef names a Route by its public_id and name.
type RouteRef struct {
	PublicID string
	Name     string
}

// AlertPage is a page of the Alerts view; Next, the position to continue after, is nil on the last page.
type AlertPage struct {
	Alerts []ViewAlert
	Next   *AlertPosition
}

// AlertsView reads the Alerts view of the Integrations of an Organization (C-06.FR-19) and their learned
// Alertmanager routes (C-06.FR-18).
type AlertsView struct {
	orgID  int64
	store  viewQueries
	routes routeQueries
	clock  clock.Clock
}

// NewAlertsView returns the Alerts view of the Organization orgID; business is the business clock, which decides
// the retention of resolved Alerts.
func NewAlertsView(orgID int64, store ProcessQueries, business clock.Clock) *AlertsView {
	return &AlertsView{orgID: orgID, store: store, routes: store, clock: business}
}

// viewRow is a row of any of the four sorted queries.
type viewRow struct {
	ID                   int64
	Fingerprint          string
	Labels               []byte
	Annotations          []byte
	StaticLabelConflicts []string
	Status               string
	StartsAt             time.Time
	LastSeenAt           time.Time
	FiredAt              time.Time
	ResolvedAt           pgtype.Timestamptz
	ResolveReason        pgtype.Text
	ResolveReasonText    pgtype.Text
	RoutePublicID        pgtype.Text
	RouteName            pgtype.Text
	SeverityLevel        pgtype.Text
	SeverityRaw          pgtype.Text
}

// List lists the Alerts of an Integration that is not deleted: firing ones and those resolved within
// retention.alert_details. Matchers with = and a value are given to the database to narrow the scan; every Matcher
// and the text are then matched against each Alert's labels, the batches continuing until the page is full.
func (v *AlertsView) List(ctx context.Context, f AlertFilter) (AlertPage, error) {
	pid, err := publicid.Parse(publicid.Integration, f.Integration)
	if err != nil {
		return AlertPage{}, integrations.ErrNotFound
	}
	in, err := v.store.FindViewIntegration(ctx, dbgen.FindViewIntegrationParams{OrgID: v.orgID, PublicID: pid})
	if errors.Is(err, pgx.ErrNoRows) {
		return AlertPage{}, integrations.ErrNotFound
	}
	if err != nil {
		return AlertPage{}, fmt.Errorf("find the integration %s: %w", pid, err)
	}
	contains := map[string]string{}
	for _, m := range f.Matchers {
		if _, taken := contains[m.Name]; m.Op == matchers.Equal && m.Value != "" && !taken {
			contains[m.Name] = m.Value
		}
	}
	b := batchQuery{orgID: v.orgID, integrationID: in.ID, state: f.State, sort: f.Sort, after: f.After,
		resolvedSince: v.clock.Now().UTC().Add(-time.Duration(in.RetentionAlertDetailsDays) * 24 * time.Hour)}
	if b.contains, err = json.Marshal(contains); err != nil {
		return AlertPage{}, fmt.Errorf("encode the label filter: %w", err)
	}
	limit := min(max(f.Limit, 1), 1000)
	b.size = int32(min(max(2*limit, 100), 1000))
	query := strings.ToLower(f.Query)
	var page AlertPage
	scanned := 0
	for {
		rows, err := v.batch(ctx, b)
		if err != nil {
			return AlertPage{}, err
		}
		for _, r := range rows {
			a, err := viewAlertOf(r)
			if err != nil {
				return AlertPage{}, err
			}
			if !matchers.All(f.Matchers, a.Labels) || !containsValue(a.Labels, query) {
				continue
			}
			if len(page.Alerts) == limit {
				last := page.Alerts[limit-1]
				page.Next = &AlertPosition{At: positionOf(last, f.Sort), ID: last.ID}
				return v.withGroupKeys(ctx, page)
			}
			page.Alerts = append(page.Alerts, a)
		}
		if len(rows) < int(b.size) {
			return v.withGroupKeys(ctx, page)
		}
		last := rows[len(rows)-1]
		at := last.LastSeenAt
		if f.Sort == SortStarts || f.Sort == SortStartsDesc {
			at = last.StartsAt
		}
		b.after = &AlertPosition{At: at, ID: last.ID}
		if scanned += len(rows); scanned >= maxScan {
			page.Next = b.after
			return v.withGroupKeys(ctx, page)
		}
	}
}

func positionOf(a ViewAlert, sort string) time.Time {
	if sort == SortStarts || sort == SortStartsDesc {
		return a.StartsAt
	}
	return a.LastSeenAt
}

// containsValue reports whether a label value contains the lower-case query; an empty query matches.
func containsValue(labels map[string]string, query string) bool {
	if query == "" {
		return true
	}
	for _, v := range labels {
		if strings.Contains(strings.ToLower(v), query) {
			return true
		}
	}
	return false
}

// batchQuery is one batch of the scan of the Alerts view.
type batchQuery struct {
	orgID, integrationID int64
	state, sort          string
	after                *AlertPosition
	resolvedSince        time.Time
	contains             []byte
	size                 int32
}

// batch runs the query of the sort, the descending last seen time by default.
func (v *AlertsView) batch(ctx context.Context, b batchQuery) ([]viewRow, error) {
	status := pgtype.Text{String: b.state, Valid: b.state != ""}
	var afterAt pgtype.Timestamptz
	var afterID pgtype.Int8
	if b.after != nil {
		afterAt = pgtype.Timestamptz{Time: b.after.At, Valid: true}
		afterID = pgtype.Int8{Int64: b.after.ID, Valid: true}
	}
	var out []viewRow
	var err error
	switch b.sort {
	case SortLastSeen:
		var rows []dbgen.ListViewAlertsByLastSeenAscRow
		rows, err = v.store.ListViewAlertsByLastSeenAsc(ctx, dbgen.ListViewAlertsByLastSeenAscParams{OrgID: b.orgID,
			IntegrationID: b.integrationID, ResolvedSince: b.resolvedSince, Status: status, Contains: b.contains,
			AfterAt: afterAt, AfterID: afterID, BatchSize: b.size})
		for _, r := range rows {
			out = append(out, viewRow(r))
		}
	case SortStarts:
		var rows []dbgen.ListViewAlertsByStartsAtAscRow
		rows, err = v.store.ListViewAlertsByStartsAtAsc(ctx, dbgen.ListViewAlertsByStartsAtAscParams{OrgID: b.orgID,
			IntegrationID: b.integrationID, ResolvedSince: b.resolvedSince, Status: status, Contains: b.contains,
			AfterAt: afterAt, AfterID: afterID, BatchSize: b.size})
		for _, r := range rows {
			out = append(out, viewRow(r))
		}
	case SortStartsDesc:
		var rows []dbgen.ListViewAlertsByStartsAtRow
		rows, err = v.store.ListViewAlertsByStartsAt(ctx, dbgen.ListViewAlertsByStartsAtParams{OrgID: b.orgID,
			IntegrationID: b.integrationID, ResolvedSince: b.resolvedSince, Status: status, Contains: b.contains,
			AfterAt: afterAt, AfterID: afterID, BatchSize: b.size})
		for _, r := range rows {
			out = append(out, viewRow(r))
		}
	default:
		var rows []dbgen.ListViewAlertsByLastSeenRow
		rows, err = v.store.ListViewAlertsByLastSeen(ctx, dbgen.ListViewAlertsByLastSeenParams{OrgID: b.orgID,
			IntegrationID: b.integrationID, ResolvedSince: b.resolvedSince, Status: status, Contains: b.contains,
			AfterAt: afterAt, AfterID: afterID, BatchSize: b.size})
		for _, r := range rows {
			out = append(out, viewRow(r))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("list the alerts: %w", err)
	}
	return out, nil
}

func viewAlertOf(r viewRow) (ViewAlert, error) {
	a := ViewAlert{ID: r.ID, Fingerprint: r.Fingerprint, State: r.Status, StartsAt: r.StartsAt.UTC(),
		LastSeenAt: r.LastSeenAt.UTC(), ResolvedAt: timeOf(r.ResolvedAt), Reason: textOf(r.ResolveReason),
		ReasonText: textOf(r.ResolveReasonText), Warnings: r.StaticLabelConflicts, GroupKeys: []string{},
		SeverityLevel: textOf(r.SeverityLevel), SeverityRaw: textOf(r.SeverityRaw)}
	if r.RoutePublicID.Valid {
		a.Route = &RouteRef{PublicID: r.RoutePublicID.String, Name: r.RouteName.String}
	}
	if a.Warnings == nil {
		a.Warnings = []string{}
	}
	if err := json.Unmarshal(r.Labels, &a.Labels); err != nil {
		return ViewAlert{}, fmt.Errorf("read the labels of alert %d: %w", r.ID, err)
	}
	if err := json.Unmarshal(r.Annotations, &a.Annotations); err != nil {
		return ViewAlert{}, fmt.Errorf("read the annotations of alert %d: %w", r.ID, err)
	}
	return a, nil
}

// withGroupKeys adds the groupKeys and the Alert Group of each Alert of the page.
func (v *AlertsView) withGroupKeys(ctx context.Context, page AlertPage) (AlertPage, error) {
	if len(page.Alerts) == 0 {
		return page, nil
	}
	ids := make([]int64, len(page.Alerts))
	index := make(map[int64]int, len(page.Alerts))
	for i, a := range page.Alerts {
		ids[i], index[a.ID] = a.ID, i
	}
	rows, err := v.store.ListViewGroupKeys(ctx, dbgen.ListViewGroupKeysParams{OrgID: v.orgID, AlertIds: ids})
	if err != nil {
		return AlertPage{}, fmt.Errorf("list the groupKeys of the alerts: %w", err)
	}
	for _, r := range rows {
		a := &page.Alerts[index[r.AlertID]]
		a.GroupKeys = append(a.GroupKeys, r.GroupKey)
	}
	groups, err := v.store.ListViewAlertGroups(ctx, dbgen.ListViewAlertGroupsParams{OrgID: v.orgID, AlertIds: ids})
	if err != nil {
		return AlertPage{}, fmt.Errorf("list the alert groups of the alerts: %w", err)
	}
	for _, r := range groups {
		page.Alerts[index[r.AlertID]].AlertGroup = &AlertGroupRef{PublicID: r.PublicID, Number: r.Number}
	}
	return page, nil
}
