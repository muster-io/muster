// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	ingestdb "github.com/muster-io/muster/internal/ingest/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/publicid"
)

// maxRediscoveries bounds how often one transaction finds the Alert Group of a key again because a concurrent change
// resolved or reopened the one it found before taking the lock.
const maxRediscoveries = 5

// DBTX is the connection or transaction the lifecycle runs on: the Snapshot's, a timer's or the Leader's.
type DBTX = dbgen.DBTX

// Restamp records, through routing, that Alerts were grouped on another Route than the one routing stamped: the
// Default route, when their Route was deleted while the Snapshot waited for it.
type Restamp func(ctx context.Context, tx DBTX, alertIDs []int64, routeID int64) error

// Config is what a Service needs.
type Config struct {
	OrgID int64
	Store Store
	Audit *audit.Writer
	// Business is the business clock, which dates every change, window and deadline.
	Business clock.Clock
	Log      *logging.Logger
	Restamp  Restamp
}

// Service is the Alert Group lifecycle of an Organization: the grouping Sink of Snapshot processing, the system
// transitions of timers, downtime and Route deletion, the Commands, and the reads of Alert Groups.
type Service struct {
	orgID   int64
	store   Store
	audit   *audit.Writer
	clock   clock.Clock
	log     *logging.Logger
	restamp Restamp
	queries func(dbgen.DBTX) Queries
	d       *dispatcher
}

// New returns the Service of the Organization in cfg.
func New(cfg Config) *Service {
	s := &Service{orgID: cfg.OrgID, store: cfg.Store, audit: cfg.Audit, clock: cfg.Business, log: cfg.Log,
		restamp: cfg.Restamp, queries: newQueries}
	s.d = &dispatcher{orgID: cfg.OrgID, clock: cfg.Business, log: cfg.Log, audit: cfg.Audit,
		routeIDs: s.routePublicID}
	return s
}

// routePublicID names a Route, deleted or not, by its public_id.
func (s *Service) routePublicID(ctx context.Context, q Queries, id int64) (string, error) {
	r, err := q.GetRoutePolicy(ctx, dbgen.GetRoutePolicyParams{OrgID: s.orgID, ID: id})
	if err != nil {
		return "", fmt.Errorf("read route %d: %w", id, err)
	}
	return r.PublicID, nil
}

// AlertChanges is the grouping Sink (C-09.FR-3): in the Snapshot's transaction tx, after routing, the newly firing
// Alerts join the open Alert Group of their Route and Group key, reopen a system-resolved one inside its Reopen
// window or start a new one; Continuations, annotation changes and resolutions apply to the Alert Group each Alert
// fires in. Every firing, routed Alert belongs to an Alert Group: a listed Alert that fires in none — it fired before
// grouping existed, or a failure or a configuration change left it out — is grouped as if it had just fired. It
// returns the #N of the Alert Groups it created or changed, and what to count and log once the Snapshot committed.
func (s *Service) AlertChanges(ctx context.Context, tx ingestdb.DBTX, changes []ingest.AlertChange) (ingest.Routed,
	error) {
	e := s.newEngine(s.queries(tx), tx)
	if len(changes) > 0 {
		ctx = withSnapshot(ctx, changes[0].StoredSnapshotID)
	}
	if err := e.changes(ctx, changes); err != nil {
		return ingest.Routed{}, err
	}
	return e.routed(), nil
}

// alertRow is an Alert as grouping reads it.
type alertRow struct {
	ID            int64
	IntegrationID int64
	Fingerprint   string
	Labels        map[string]string
	Annotations   map[string]string
	Conflicts     []string
	Firing        bool
	StartsAt      time.Time
	Episode       int64
	RouteID       int64
	Severity      organization.SeverityLevel
}

// membership is the firing membership of an Alert in an Alert Group.
type membership struct {
	ID          int64
	GroupID     int64
	AlertID     int64
	StartsAt    time.Time
	Annotations map[string]string
}

// routeInfo is the policy of a Route that grouping and the system transitions use.
type routeInfo struct {
	ID           int64
	PublicID     string
	IsDefault    bool
	Urgent       bool
	Key          []string
	ReopenWindow time.Duration
	Grace        time.Duration
	RemovesAck   bool
}

// slot is the newly firing Alerts of one Route and Group key values, and where they go: an open Alert Group, a
// Reopen of a system-resolved one, or a new one (target 0).
type slot struct {
	route  *routeInfo
	values map[string]string
	sha    []byte
	joins  []*alertRow
	target int64
	reopen bool
}

// batch is what one transaction changes in one existing Alert Group.
type batch struct {
	slot      *slot
	continued []*alertRow
	annotated []*alertRow
	resolved  []resolution
	members   map[int64]*membership
}

// resolution is an Alert that resolved, with its reason.
type resolution struct {
	alert  *alertRow
	member *membership
	reason string
	text   string
}

// engine applies the changes of one transaction to the Alert Groups. It takes its locks in one order — the Routes
// FOR SHARE, the counter row when Alerts are grouped, then the Alert Groups by id — so that two transactions never
// wait for each other; a concurrent change found after the lock costs another lookup.
type engine struct {
	s   *Service
	q   Queries
	tx  dbgen.DBTX
	now time.Time

	settings      *dbgen.GetGroupingSettingsRow
	routes        map[int64]*routeInfo
	defaultRoute  *routeInfo
	policies      map[int64]*routeInfo
	counterLocked bool
	// recheck reads again, under the counter row, where the Alerts to group fire.
	recheck bool

	alerts map[int64]*alertRow
	firing map[int64]*membership
	// listed are the Alerts handed over only as listed, which grouping takes when they fire in no Alert Group.
	listed   map[int64]bool
	slots    []*slot
	groups   map[int64]*Group
	batches  map[int64]*batch
	members  map[int64][]dbgen.ListGroupFiringAlertsRow
	touched  []int64
	after    committed
	restamps bool
	// source is the person-resolved Alert Group whose Grace period ends, and tails the memberships of the Alerts
	// still firing in it, by Alert, which leave it for the Alert Group that takes them.
	source *Group
	tails  map[int64]int64
}

func (s *Service) newEngine(q Queries, tx dbgen.DBTX) *engine {
	return &engine{s: s, q: q, tx: tx, now: s.clock.Now().UTC(), routes: map[int64]*routeInfo{},
		policies: map[int64]*routeInfo{}, alerts: map[int64]*alertRow{}, firing: map[int64]*membership{},
		groups: map[int64]*Group{}, batches: map[int64]*batch{}, members: map[int64][]dbgen.ListGroupFiringAlertsRow{}}
}

// changes applies the Alert changes of a Snapshot.
func (e *engine) changes(ctx context.Context, changes []ingest.AlertChange) error {
	ids, err := e.handed(ctx, changes)
	if err != nil || len(ids) == 0 {
		return err
	}
	if err := e.loadAlerts(ctx, ids); err != nil {
		return err
	}
	var fired []*alertRow
	seen := map[int64]bool{}
	for _, c := range changes {
		a := e.alerts[c.AlertID]
		if a == nil || !a.Firing || seen[a.ID] || e.firing[a.ID] != nil {
			continue
		}
		if c.Kind == ingest.ChangeFired || (c.Kind == ingest.ChangeListed && a.RouteID != 0) {
			seen[a.ID] = true
			fired = append(fired, a)
		}
	}
	e.recheck = true
	if err := e.place(ctx, fired); err != nil {
		return err
	}
	if err := e.lockAll(ctx); err != nil {
		return err
	}
	for _, c := range changes {
		e.member(c)
	}
	return e.apply(ctx)
}

// handed are the Alerts of the changes that grouping reads: those of every change, and the listed ones that fire in
// no Alert Group, which it groups.
func (e *engine) handed(ctx context.Context, changes []ingest.AlertChange) ([]int64, error) {
	changed, listed := map[int64]bool{}, map[int64]bool{}
	for _, c := range changes {
		if c.Kind == ingest.ChangeListed {
			listed[c.AlertID] = true
		} else {
			changed[c.AlertID] = true
		}
	}
	for id := range changed {
		delete(listed, id)
	}
	ids := slices.Sorted(maps.Keys(changed))
	if len(listed) == 0 {
		return ids, nil
	}
	only := slices.Sorted(maps.Keys(listed))
	ms, err := e.q.ListFiringMemberships(ctx, dbgen.ListFiringMembershipsParams{OrgID: e.s.orgID, AlertIds: only})
	if err != nil {
		return nil, fmt.Errorf("read where the listed alerts fire: %w", err)
	}
	for _, m := range ms {
		delete(listed, m.AlertID)
	}
	e.listed = listed
	return append(ids, slices.Sorted(maps.Keys(listed))...), nil
}

// loadAlerts reads the Alerts and where they fire.
func (e *engine) loadAlerts(ctx context.Context, ids []int64) error {
	rows, err := e.q.ListChangedAlerts(ctx, dbgen.ListChangedAlertsParams{OrgID: e.s.orgID, Ids: ids})
	if err != nil {
		return fmt.Errorf("read the changed alerts: %w", err)
	}
	for _, r := range rows {
		a := &alertRow{ID: r.ID, IntegrationID: r.IntegrationID, Fingerprint: r.Fingerprint,
			Conflicts: r.StaticLabelConflicts, Firing: r.Status == "firing", StartsAt: r.StartsAt.UTC(),
			Episode: r.Episode, RouteID: r.RouteID.Int64, Severity: severityOf(r.SeverityLevel)}
		if err := readLabels(r.Labels, r.Annotations, a); err != nil {
			return err
		}
		e.alerts[a.ID] = a
	}
	return e.loadFiring(ctx, ids)
}

// loadFiring reads the firing memberships of the Alerts.
func (e *engine) loadFiring(ctx context.Context, ids []int64) error {
	ms, err := e.q.ListFiringMemberships(ctx, dbgen.ListFiringMembershipsParams{OrgID: e.s.orgID, AlertIds: ids})
	if err != nil {
		return fmt.Errorf("read where the alerts fire: %w", err)
	}
	clear(e.firing)
	for _, m := range ms {
		var annotations map[string]string
		if err := json.Unmarshal(m.Annotations, &annotations); err != nil {
			return fmt.Errorf("read the annotations of membership %d: %w", m.ID, err)
		}
		e.firing[m.AlertID] = &membership{ID: m.ID, GroupID: m.AlertGroupID, AlertID: m.AlertID,
			StartsAt: m.StartsAt.UTC(), Annotations: annotations}
	}
	return nil
}

// place finds where each newly firing Alert goes: its Route, locked FOR SHARE, or the Default route when it was
// deleted meanwhile; its Group key values; and the Alert Group of that Route and key.
func (e *engine) place(ctx context.Context, fired []*alertRow) error {
	if len(fired) == 0 {
		return nil
	}
	var ids []int64
	for _, a := range fired {
		ids = append(ids, a.RouteID)
	}
	if err := e.lockRoutes(ctx, ids); err != nil {
		return err
	}
	var moved []int64
	for _, a := range fired {
		r := e.routes[a.RouteID]
		if r == nil {
			d, err := e.lockDefault(ctx)
			if err != nil {
				return err
			}
			if a.RouteID != d.ID {
				moved = append(moved, a.ID)
			}
			r, a.RouteID = d, d.ID
		}
		e.slotOf(r, a)
	}
	if len(moved) > 0 && e.s.restamp != nil {
		if err := e.s.restamp(ctx, e.tx, moved, e.defaultRoute.ID); err != nil {
			return fmt.Errorf("route the alerts of a deleted route to the default route: %w", err)
		}
		e.restamps = true
	}
	// Grouping may start or reopen an Alert Group, which makes it open: the counter row comes before any Alert
	// Group lock, so that the lookups below see what another transaction made open, and no lookup after a lock has
	// to take it out of order.
	if !e.counterLocked {
		if err := e.lockCounter(ctx); err != nil {
			return err
		}
	}
	if e.recheck {
		if err := e.unplace(ctx); err != nil {
			return err
		}
	}
	return e.discover(ctx)
}

// unplace takes out of the slots the Alerts that fire in an Alert Group after all: another transaction grouped them
// after this one read where they fire, and committed before this one took the counter row, which every grouping
// takes. A listed Alert taken out is forgotten, so that its Alert Group is neither locked nor changed.
func (e *engine) unplace(ctx context.Context) error {
	if err := e.loadFiring(ctx, e.alertIDs()); err != nil {
		return err
	}
	slots := e.slots[:0]
	for _, s := range e.slots {
		s.joins = slices.DeleteFunc(s.joins, func(a *alertRow) bool {
			if e.firing[a.ID] == nil {
				return false
			}
			if e.listed[a.ID] {
				delete(e.alerts, a.ID)
				delete(e.firing, a.ID)
			}
			return true
		})
		if len(s.joins) > 0 {
			slots = append(slots, s)
		}
	}
	e.slots = slots
	return nil
}

// slotOf adds a to the slot of its Route and Group key values.
func (e *engine) slotOf(r *routeInfo, a *alertRow) {
	values, sha := keyOf(r.Key, a.Labels)
	for _, s := range e.slots {
		if s.route.ID == r.ID && bytes.Equal(s.sha, sha) {
			s.joins = append(s.joins, a)
			return
		}
	}
	e.slots = append(e.slots, &slot{route: r, values: values, sha: sha, joins: []*alertRow{a}})
}

// lockRoutes locks the Routes that are not deleted FOR SHARE (C-09.FR-19, C-08.FR-9).
func (e *engine) lockRoutes(ctx context.Context, ids []int64) error {
	slices.Sort(ids)
	ids = slices.Compact(ids)
	rows, err := e.q.LockRoutes(ctx, dbgen.LockRoutesParams{OrgID: e.s.orgID, Ids: ids})
	if err != nil {
		return fmt.Errorf("lock the routes: %w", err)
	}
	for _, r := range rows {
		e.routes[r.ID] = &routeInfo{ID: r.ID, PublicID: r.PublicID, IsDefault: r.IsDefault, Urgent: r.Urgent,
			Key: r.GroupKey, ReopenWindow: seconds(r.ReopenWindowSeconds), Grace: seconds(r.GracePeriodSeconds),
			RemovesAck: r.UrgentRiseRemovesAck}
		e.policies[r.ID] = e.routes[r.ID]
	}
	return nil
}

// lockDefault locks the Default route FOR SHARE.
func (e *engine) lockDefault(ctx context.Context) (*routeInfo, error) {
	if e.defaultRoute != nil {
		return e.defaultRoute, nil
	}
	r, err := e.q.LockDefaultRoute(ctx, e.s.orgID)
	if err != nil {
		return nil, fmt.Errorf("lock the default route: %w", err)
	}
	e.defaultRoute = &routeInfo{ID: r.ID, PublicID: r.PublicID, IsDefault: r.IsDefault, Urgent: r.Urgent,
		Key: r.GroupKey, ReopenWindow: seconds(r.ReopenWindowSeconds), Grace: seconds(r.GracePeriodSeconds),
		RemovesAck: r.UrgentRiseRemovesAck}
	e.routes[r.ID], e.policies[r.ID] = e.defaultRoute, e.defaultRoute
	return e.defaultRoute, nil
}

// policy is the policy of a Route, deleted or not.
func (e *engine) policy(ctx context.Context, id int64) (*routeInfo, error) {
	if p := e.policies[id]; p != nil {
		return p, nil
	}
	r, err := e.q.GetRoutePolicy(ctx, dbgen.GetRoutePolicyParams{OrgID: e.s.orgID, ID: id})
	if err != nil {
		return nil, fmt.Errorf("read route %d: %w", id, err)
	}
	p := &routeInfo{ID: r.ID, PublicID: r.PublicID, IsDefault: r.IsDefault, Urgent: r.Urgent, Key: r.GroupKey,
		ReopenWindow: seconds(r.ReopenWindowSeconds), Grace: seconds(r.GracePeriodSeconds),
		RemovesAck: r.UrgentRiseRemovesAck}
	e.policies[id] = p
	return p, nil
}

// discover finds the Alert Group of each slot in the order of C-09.FR-3: the open one, else one to reopen, else a
// new one. It runs under the counter row, which every creation and Reopen takes first, so no other transaction makes
// an Alert Group of these keys open until this one commits.
func (e *engine) discover(ctx context.Context) error {
	for _, s := range e.slots {
		if err := e.find(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// find looks up the target of a slot.
func (e *engine) find(ctx context.Context, s *slot) error {
	s.target, s.reopen = 0, false
	id, err := e.q.FindOpenGroup(ctx, dbgen.FindOpenGroupParams{OrgID: e.s.orgID, RouteID: s.route.ID,
		GroupKeySha256: s.sha})
	if err == nil {
		s.target = id
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find the open alert group: %w", err)
	}
	id, err = e.q.FindReopenableGroup(ctx, dbgen.FindReopenableGroupParams{OrgID: e.s.orgID, RouteID: s.route.ID,
		GroupKeySha256: s.sha, Now: e.now})
	if err == nil {
		s.target, s.reopen = id, true
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find an alert group to reopen: %w", err)
	}
	return nil
}

// lockCounter creates the Organization's counter row on first use and locks it.
func (e *engine) lockCounter(ctx context.Context) error {
	if err := e.q.EnsureCounter(ctx, e.s.orgID); err != nil {
		return fmt.Errorf("create the alert group counter: %w", err)
	}
	if _, err := e.q.LockCounter(ctx, e.s.orgID); err != nil {
		return fmt.Errorf("lock the alert group counter: %w", err)
	}
	e.counterLocked = true
	return nil
}

// lockAll locks the Alert Groups the transaction changes in id order, then checks that what it found before the
// lock still holds: an open target is still open, a target to reopen still inside its window, and every Alert
// still fires where it fired. What changed meanwhile is looked up again and locked too.
func (e *engine) lockAll(ctx context.Context) error {
	for range maxRediscoveries {
		var want []int64
		for _, s := range e.slots {
			if s.target != 0 {
				want = append(want, s.target)
			}
		}
		for _, m := range e.firing {
			want = append(want, m.GroupID)
		}
		var missing []int64
		for _, id := range want {
			if e.groups[id] == nil {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			locked, err := lock(ctx, e.q, e.s.orgID, missing)
			if err != nil {
				return err
			}
			for id, g := range locked {
				e.groups[id] = g
			}
			if err := e.loadFiring(ctx, e.alertIDs()); err != nil {
				return err
			}
		}
		stable := true
		for _, s := range e.slots {
			// A creation was decided under the counter row; an open or a reopenable target is checked now that it
			// is locked, and looked up again when a person resolved or moved it meanwhile.
			if s.target == 0 || e.valid(s) {
				continue
			}
			prev := s.target
			if err := e.find(ctx, s); err != nil {
				return err
			}
			if s.target != prev || (s.target != 0 && !e.valid(s)) {
				stable = false
			}
		}
		for _, m := range e.firing {
			if e.groups[m.GroupID] == nil {
				stable = false
			}
		}
		if stable && len(missing) == 0 {
			return nil
		}
	}
	return fmt.Errorf("the alert groups kept changing while grouping locked them")
}

// valid reports whether the locked target of a slot is still what the lookup found.
func (e *engine) valid(s *slot) bool {
	g := e.groups[s.target]
	if g == nil || g.RouteID != s.route.ID || !bytes.Equal(g.KeySHA, s.sha) || g.MovedFromRouteID != nil {
		return false
	}
	if s.reopen {
		return g.Status == StatusResolved && g.ReopenDeadline != nil && g.ReopenDeadline.After(e.now)
	}
	return g.Status != StatusResolved
}

// alertIDs are the Alerts the transaction knows.
func (e *engine) alertIDs() []int64 {
	ids := make([]int64, 0, len(e.alerts))
	for id := range e.alerts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// batchOf is the batch of a locked Alert Group.
func (e *engine) batchOf(id int64) *batch {
	b := e.batches[id]
	if b == nil {
		b = &batch{members: map[int64]*membership{}}
		e.batches[id] = b
	}
	return b
}

// member adds a change of an Alert that fires in an Alert Group to that Alert Group's batch: a Continuation, an
// annotation change or a resolution (C-06.FR-3, FR-4, FR-11, FR-12). A resolution applies to the Alert Group where
// the fingerprint fires.
func (e *engine) member(c ingest.AlertChange) {
	a, m := e.alerts[c.AlertID], e.firing[c.AlertID]
	if a == nil || m == nil || e.groups[m.GroupID] == nil {
		return
	}
	b := e.batchOf(m.GroupID)
	b.members[a.ID] = m
	switch c.Kind {
	case ingest.ChangeContinued:
		b.continued = append(b.continued, a)
	case ingest.ChangeAnnotations:
		b.annotated = append(b.annotated, a)
	case ingest.ChangeResolved:
		b.resolved = append(b.resolved, resolution{alert: a, member: m, reason: c.Reason, text: c.ReasonText})
	case ingest.ChangeFired, ingest.ChangeListed:
	}
}

// apply changes the existing Alert Groups in id order, then creates the new ones.
func (e *engine) apply(ctx context.Context) error {
	for _, s := range e.slots {
		if s.target != 0 {
			e.batchOf(s.target).slot = s
		}
	}
	targets := make([]int64, 0, len(e.batches))
	for id := range e.batches {
		targets = append(targets, id)
	}
	slices.Sort(targets)
	if err := e.loadMembers(ctx, targets); err != nil {
		return err
	}
	for _, id := range targets {
		g := e.groups[id]
		before := g.EventSeq
		policy, err := e.policy(ctx, g.RouteID)
		if err != nil {
			return err
		}
		if err := e.s.d.dispatch(ctx, e.q, g, System, &snapshotChange{e: e, b: e.batches[id], route: policy},
			&e.after); err != nil {
			return err
		}
		if g.EventSeq != before {
			e.touched = append(e.touched, g.Number)
		}
	}
	for _, s := range e.slots {
		if s.target == 0 {
			if err := e.create(ctx, s); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadMembers reads the Alerts firing in the Alert Groups that new Alerts join, for the Replacements.
func (e *engine) loadMembers(ctx context.Context, ids []int64) error {
	var joined []int64
	for _, id := range ids {
		if b := e.batches[id]; b.slot != nil && !b.slot.reopen {
			joined = append(joined, id)
		}
	}
	if len(joined) == 0 {
		return nil
	}
	rows, err := e.q.ListGroupFiringAlerts(ctx, dbgen.ListGroupFiringAlertsParams{OrgID: e.s.orgID,
		AlertGroupIds: joined})
	if err != nil {
		return fmt.Errorf("read the alerts firing in the alert groups: %w", err)
	}
	for _, r := range rows {
		e.members[r.AlertGroupID] = append(e.members[r.AlertGroupID], r)
	}
	return nil
}

// grouping reads the Organization's settings once per transaction.
func (e *engine) grouping(ctx context.Context) (*dbgen.GetGroupingSettingsRow, error) {
	if e.settings != nil {
		return e.settings, nil
	}
	st, err := e.q.GetGroupingSettings(ctx, e.s.orgID)
	if err != nil {
		return nil, fmt.Errorf("read the grouping settings: %w", err)
	}
	e.settings = &st
	return e.settings, nil
}

// create starts a new Alert Group with the Alerts of a slot, with the next #N, as one `created` entry.
func (e *engine) create(ctx context.Context, s *slot) error {
	st, err := e.grouping(ctx)
	if err != nil {
		return err
	}
	number, err := e.q.NextNumber(ctx, e.s.orgID)
	if err != nil {
		return fmt.Errorf("take the next alert group number: %w", err)
	}
	first := s.joins[0]
	title, fromKey := newTitle(first, s.route.Key, s.values)
	g := &Group{PublicID: publicid.New(publicid.AlertGroup), Number: number, RouteID: s.route.ID,
		KeyLabels: s.route.Key, KeyValues: s.values, KeySHA: s.sha, Title: title, TitleFromKey: fromKey,
		CommonLabels: first.Labels, CommonAnnots: first.Annotations, Severity: organization.SeverityInfo,
		CreatedAt: e.now, LastChangedAt: e.now}
	if e.source != nil {
		g.FiringAgainAfterID = &e.source.ID
	}
	for _, a := range s.joins {
		g.joined(a)
		g.Severity = higher(g.Severity, a.Severity)
	}
	g.Urgent = urgentOf(s.route, g.Severity, st.CriticalIsUrgent)
	id, err := e.q.InsertGroup(ctx, dbgen.InsertGroupParams{OrgID: e.s.orgID, PublicID: g.PublicID,
		Number: g.Number, RouteID: g.RouteID, GroupKeyLabels: g.KeyLabels, GroupKeyValues: jsonOf(g.KeyValues),
		GroupKeySha256: g.KeySHA, Title: g.Title, TitleFromGroupKey: g.TitleFromKey, Summary: text(g.Summary),
		CommonLabels: jsonOf(g.CommonLabels), CommonAnnotations: jsonOf(g.CommonAnnots),
		IntegrationIds: g.IntegrationIDs, SeverityLevel: string(g.Severity), Urgent: g.Urgent,
		FiringAgainAfterID: nullInt(g.FiringAgainAfterID), CreatedAt: e.now})
	if err != nil {
		return fmt.Errorf("create alert group #%d: %w", number, err)
	}
	g.ID = id
	s.target = id
	e.groups[id] = g
	if err := e.s.d.dispatch(ctx, e.q, g, System, &creation{e: e, slot: s}, &e.after); err != nil {
		return err
	}
	e.touched = append(e.touched, g.Number)
	return nil
}

// join inserts the memberships of Alerts that join g; the tails of a Grace period leave their Alert Group first, as
// an Alert fires in one Alert Group at a time.
func (e *engine) join(ctx context.Context, g *Group, alerts []*alertRow) error {
	if len(e.tails) > 0 {
		moved := dbgen.MoveMembershipsParams{OrgID: e.s.orgID, EndedAt: e.now}
		for _, a := range alerts {
			if id, ok := e.tails[a.ID]; ok {
				moved.Ids = append(moved.Ids, id)
				moved.MovedTos = append(moved.MovedTos, g.ID)
			}
		}
		if len(moved.Ids) > 0 {
			if err := e.q.MoveMemberships(ctx, moved); err != nil {
				return fmt.Errorf("move alerts into alert group #%d: %w", g.Number, err)
			}
		}
	}
	p := dbgen.InsertMembershipsParams{OrgID: e.s.orgID, JoinedAt: e.now}
	for _, a := range alerts {
		p.AlertGroupIds = append(p.AlertGroupIds, g.ID)
		p.AlertIds = append(p.AlertIds, a.ID)
		p.Episodes = append(p.Episodes, a.Episode)
		p.StartsAts = append(p.StartsAts, a.StartsAt)
		p.Annotations = append(p.Annotations, jsonOf(a.Annotations))
	}
	if err := e.q.InsertMemberships(ctx, p); err != nil {
		return fmt.Errorf("add alerts to alert group #%d: %w", g.Number, err)
	}
	g.FiringCount += int64(len(alerts))
	return nil
}

// routed is what the Sink returns: the Alert Groups it created or changed and, when Alerts of a deleted Route went to
// the Default route, that Route.
func (e *engine) routed() ingest.Routed {
	out := ingest.Routed{}
	if e.restamps {
		out.IDs, out.PublicIDs = []int64{e.defaultRoute.ID}, []string{e.defaultRoute.PublicID}
	}
	touched := slices.Clone(e.touched)
	slices.Sort(touched)
	out.AlertGroups = slices.Compact(touched)
	if len(e.after) > 0 {
		after := e.after
		out.Committed = func(ctx context.Context) { after.run(ctx) }
	}
	return out
}

// severityOf is the Severity level routing stamped, info when there is none.
func severityOf(t pgtype.Text) organization.SeverityLevel {
	if !t.Valid || t.String == "" {
		return organization.SeverityInfo
	}
	return organization.SeverityLevel(t.String)
}

// rank orders the Severity levels.
func rank(l organization.SeverityLevel) int {
	switch l {
	case organization.SeverityCritical:
		return 2
	case organization.SeverityWarning:
		return 1
	default:
		return 0
	}
}

// higher is the higher of two Severity levels.
func higher(a, b organization.SeverityLevel) organization.SeverityLevel {
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// urgentOf is the urgency of an Alert Group of the Route at the Severity level (C-08.FR-6).
func urgentOf(r *routeInfo, level organization.SeverityLevel, criticalIsUrgent bool) bool {
	return r.Urgent || (level == organization.SeverityCritical && criticalIsUrgent)
}

// readLabels reads the labels and annotations of an Alert.
func readLabels(labels, annotations []byte, a *alertRow) error {
	if err := json.Unmarshal(labels, &a.Labels); err != nil {
		return fmt.Errorf("read the labels of alert %d: %w", a.ID, err)
	}
	if err := json.Unmarshal(annotations, &a.Annotations); err != nil {
		return fmt.Errorf("read the annotations of alert %d: %w", a.ID, err)
	}
	return nil
}

func seconds(s int64) time.Duration { return time.Duration(s) * time.Second }

// fingerprints are the fingerprints of Alerts, sorted.
func fingerprints(alerts []*alertRow) []string {
	out := make([]string, len(alerts))
	for i, a := range alerts {
		out[i] = a.Fingerprint
	}
	slices.Sort(out)
	return out
}

// conflicts are the Static label conflicts of Alerts, sorted and once each; nil without any.
func conflicts(alerts []*alertRow) []string {
	var out []string
	for _, a := range alerts {
		out = append(out, a.Conflicts...)
	}
	if len(out) == 0 {
		return nil
	}
	slices.SortFunc(out, cmp.Compare)
	return slices.Compact(out)
}
