// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/muster-io/muster/internal/ingest"
	ingestdb "github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/matchers"
	"github.com/muster-io/muster/internal/routing/dbgen"
)

// evalQueries are the queries of routing, run in the transaction of the Snapshot.
type evalQueries interface {
	GetRoutingStamp(ctx context.Context, orgID int64) (dbgen.GetRoutingStampRow, error)
	ListRoutes(ctx context.Context, orgID int64) ([]dbgen.ListRoutesRow, error)
	ListRouteMatchers(ctx context.Context, arg dbgen.ListRouteMatchersParams) ([]dbgen.ListRouteMatchersRow, error)
	ListAlertLabels(ctx context.Context, arg dbgen.ListAlertLabelsParams) ([]dbgen.ListAlertLabelsRow, error)
	SetAlertRoutes(ctx context.Context, arg dbgen.SetAlertRoutesParams) error
}

// stamp identifies a state of the Routes: the version of the list, which creation, deletion and reordering bump,
// and the sum of the versions of the Routes, which every edit bumps.
type stamp struct {
	order, versions int64
}

// compiled is a Route in evaluation order with its Matchers compiled once.
type compiled struct {
	id       int64
	publicID string
	matchers []matchers.Matcher
}

// order is the evaluation order of the Routes that are not deleted, the Default route last, as of a stamp.
type order struct {
	stamp  stamp
	routes []compiled
}

// first is the index of the first Route whose Matchers all match the labels, a missing label counting as the empty
// value; -1 when none does, which the Default route, without Matchers, never lets happen.
func (o *order) first(labels map[string]string) int {
	for i, r := range o.routes {
		if matchers.All(r.matchers, labels) {
			return i
		}
	}
	return -1
}

// Router routes newly firing Alerts (C-08.FR-3, FR-6, FR-13): it is the Sink of Snapshot processing. Each Alert of a
// fired change is taken by the first Route in evaluation order whose Matchers match its labels, Static labels
// applied, and gets its Severity level; both are recorded on the Alert, which keeps them until it fires again, so an
// edit or a reorder applies to the next Alerts only. The order with its compiled Matchers is cached per replica: a
// route hint drops it, and every Snapshot compares the stamp of the Routes in its own transaction, so that a change
// made on any replica applies at once.
type Router struct {
	orgID   int64
	queries func(dbgen.DBTX) evalQueries

	mu     sync.Mutex
	cached *order
}

// NewRouter returns the Router of the Organization orgID.
func NewRouter(orgID int64) *Router {
	return &Router{orgID: orgID, queries: func(d dbgen.DBTX) evalQueries { return dbgen.New(d) }}
}

// Invalidate drops the cached evaluation order: a hint said that a Route changed.
func (r *Router) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cached = nil
}

// AlertChanges routes the Alerts of the fired changes in the Snapshot's transaction tx, and the listed Alerts that
// fire without a Route, such as one that fired before routing took it; other changes are not routed. It returns the
// Routes that took them, in evaluation order.
func (r *Router) AlertChanges(ctx context.Context, tx ingestdb.DBTX, changes []ingest.AlertChange) (ingest.Routed,
	error) {
	var ids, listed []int64
	seen := make(map[int64]bool, len(changes))
	for _, c := range changes {
		if c.Kind == ingest.ChangeFired && !seen[c.AlertID] {
			seen[c.AlertID] = true
			ids = append(ids, c.AlertID)
		}
	}
	for _, c := range changes {
		if c.Kind == ingest.ChangeListed && !seen[c.AlertID] {
			seen[c.AlertID] = true
			listed = append(listed, c.AlertID)
		}
	}
	if len(seen) == 0 {
		return ingest.Routed{}, nil
	}
	q := r.queries(tx)
	rows, err := q.ListAlertLabels(ctx, dbgen.ListAlertLabelsParams{OrgID: r.orgID, Ids: ids, ListedIds: listed})
	if err != nil {
		return ingest.Routed{}, fmt.Errorf("read the labels of the alerts: %w", err)
	}
	if len(rows) == 0 {
		return ingest.Routed{}, nil
	}
	st, err := q.GetRoutingStamp(ctx, r.orgID)
	if err != nil {
		return ingest.Routed{}, fmt.Errorf("read the routing stamp: %w", err)
	}
	severities, err := severitiesOf(st.SeverityLabel, st.SeverityMapping)
	if err != nil {
		return ingest.Routed{}, err
	}
	o, err := r.order(ctx, q, stamp{order: st.RouteOrderVersion, versions: st.RouteVersions})
	if err != nil {
		return ingest.Routed{}, err
	}
	set := dbgen.SetAlertRoutesParams{OrgID: r.orgID, Ids: make([]int64, 0, len(rows)),
		RouteIds: make([]int64, 0, len(rows)), SeverityLevels: make([]string, 0, len(rows)),
		SeverityRaws: make([]string, 0, len(rows))}
	took := make([]bool, len(o.routes))
	for _, row := range rows {
		var labels map[string]string
		if err := json.Unmarshal(row.Labels, &labels); err != nil {
			return ingest.Routed{}, fmt.Errorf("read the labels of alert %d: %w", row.ID, err)
		}
		routeID := int64(0)
		if i := o.first(labels); i >= 0 {
			routeID, took[i] = o.routes[i].id, true
		}
		sev := severities.Of(labels)
		set.Ids = append(set.Ids, row.ID)
		set.RouteIds = append(set.RouteIds, routeID)
		set.SeverityLevels = append(set.SeverityLevels, string(sev.Level))
		set.SeverityRaws = append(set.SeverityRaws, sev.Raw)
	}
	if err := q.SetAlertRoutes(ctx, set); err != nil {
		return ingest.Routed{}, fmt.Errorf("record the routes of the alerts: %w", err)
	}
	var out ingest.Routed
	for i, t := range took {
		if t {
			out.IDs = append(out.IDs, o.routes[i].id)
			out.PublicIDs = append(out.PublicIDs, o.routes[i].publicID)
		}
	}
	return out, nil
}

// order is the evaluation order as of the stamp: the cached one when its stamp is the same, otherwise the one read in
// the transaction, which then replaces the cache. The cache holds one order only.
func (r *Router) order(ctx context.Context, q evalQueries, st stamp) (*order, error) {
	r.mu.Lock()
	cached := r.cached
	r.mu.Unlock()
	if cached != nil && cached.stamp == st {
		return cached, nil
	}
	o, err := r.load(ctx, q, st)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cached = o
	r.mu.Unlock()
	return o, nil
}

// load reads the Routes that are not deleted in evaluation order and compiles their Matchers once.
func (r *Router) load(ctx context.Context, q evalQueries, st stamp) (*order, error) {
	rows, err := q.ListRoutes(ctx, r.orgID)
	if err != nil {
		return nil, fmt.Errorf("list the routes: %w", err)
	}
	o := &order{stamp: st, routes: make([]compiled, len(rows))}
	ids := make([]int64, len(rows))
	index := make(map[int64]int, len(rows))
	for i, row := range rows {
		o.routes[i] = compiled{id: row.ID, publicID: row.PublicID}
		ids[i], index[row.ID] = row.ID, i
	}
	ms, err := q.ListRouteMatchers(ctx, dbgen.ListRouteMatchersParams{OrgID: r.orgID, RouteIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list the matchers of the routes: %w", err)
	}
	for _, m := range ms {
		i, ok := index[m.RouteID]
		if !ok {
			continue
		}
		c, err := matchers.New(m.Label, matchers.Op(m.Op), m.Value)
		if err != nil {
			return nil, fmt.Errorf("compile a matcher of the route %s: %w", o.routes[i].publicID, err)
		}
		o.routes[i].matchers = append(o.routes[i].matchers, c)
	}
	return o, nil
}
