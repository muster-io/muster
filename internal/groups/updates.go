// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// The open Alert Group updates (C-26.FR-9, schema.md §5): a change of configuration that must reach the open Alert
// Groups of its scope after the save is one open_group_updates row, inserted in the transaction of the change. The
// worker of every replica claims a row with a lease on the real clock and walks the open Alert Groups of its scope by
// id, a batch at a time, each batch in a transaction of its own that checks the lease, changes the Alert Groups through
// the dispatcher as system transitions, and records how far it got; the transaction of the last batch deletes the
// row. A lost lease leaves the row to any replica, which goes on after the last batch that committed, and an Alert
// Group the change already reached is left as it is, so a row that runs twice changes nothing the second time
// (ADR-0007). The worker is not a Leader task.

// The kinds of open_group_updates rows: a changed Minimum severity of a Route, which routing writes, and a changed
// button set of a Destination or of the Organization (C-28.FR-9), which destinations and organization write; and
// OpenGroupUpdatesChannel, the LISTEN/NOTIFY channel that wakes the workers of every replica when a row is inserted.
const (
	KindRouteMinimumSeverity = "route_minimum_severity"
	KindDestinationButtons   = "destination_buttons"
	KindOrganizationButtons  = "organization_buttons"
	OpenGroupUpdatesChannel  = "muster_open_group_updates"
)

// updateKinds are the kinds of open_group_updates rows this version handles: a replica claims only these, so that a row
// of a kind a later version adds waits for a replica that knows it.
var updateKinds = []string{KindRouteMinimumSeverity, KindDestinationButtons, KindOrganizationButtons}

// The defaults of the worker: the lease of a claimed row, renewed with each batch, the open Alert Groups one batch
// takes, the longest wait between rounds, which bounds what a missed notification costs, the shortest one, and the
// first wait after a failed round.
const (
	UpdateLease          = time.Minute
	UpdateBatch          = 50
	UpdateMaxWait        = 30 * time.Second
	updateMinWait        = 100 * time.Millisecond
	updateFailureBackoff = time.Second
)

// updateQueries are the queries of the open Alert Group updates.
type updateQueries interface {
	ClaimOpenGroupUpdate(ctx context.Context, arg dbgen.ClaimOpenGroupUpdateParams) (
		[]dbgen.ClaimOpenGroupUpdateRow, error)
	LockOpenGroupUpdate(ctx context.Context, arg dbgen.LockOpenGroupUpdateParams) (dbgen.LockOpenGroupUpdateRow, error)
	AdvanceOpenGroupUpdate(ctx context.Context, arg dbgen.AdvanceOpenGroupUpdateParams) error
	DeleteOpenGroupUpdate(ctx context.Context, arg dbgen.DeleteOpenGroupUpdateParams) error
	NextOpenGroupUpdate(ctx context.Context, arg dbgen.NextOpenGroupUpdateParams) (time.Time, error)
	ListOpenGroupsOfRouteAfter(ctx context.Context, arg dbgen.ListOpenGroupsOfRouteAfterParams) ([]int64, error)
	ListOpenGroupsOfDestinationAfter(ctx context.Context, arg dbgen.ListOpenGroupsOfDestinationAfterParams) ([]int64,
		error)
	ListOpenGroupsFollowingOrganizationAfter(ctx context.Context,
		arg dbgen.ListOpenGroupsFollowingOrganizationAfterParams) ([]int64, error)
	ListFiringLevels(ctx context.Context, arg dbgen.ListFiringLevelsParams) ([]dbgen.ListFiringLevelsRow, error)
}

// update is a claimed open_group_updates row: its kind and the Route or the Destination its kind names, with their
// public_ids for the log lines.
type update struct {
	id             int64
	kind           string
	routeID        int64
	routePID       string
	destinationPID string
}

// minimumSeverityChanged is the system transition of an open Alert Group whose Route's Minimum severity changed
// (C-26.FR-9, C-09.FR-9): its level is computed again from its firing Alerts, at alerts. A rise records the Quiet
// severity_raised or, when it reaches critical, the Quiet urgency_raised that removes nothing — a change of
// configuration never removes an acknowledgement or a Snooze — both with ReasonMinimumSeverity; a fall writes the level
// silently, as a falling Severity level does. An Alert Group already at that level is left as it is; changed says
// whether it was not.
type minimumSeverityChanged struct {
	route   *routeInfo
	alerts  organization.SeverityLevel
	changed bool
}

// errNotOpen is an Alert Group resolved since the batch listed it.
var errNotOpen = errors.New("the alert group is no longer open")

func (*minimumSeverityChanged) precondition(g *Group) error {
	if g.Status == StatusResolved {
		return errNotOpen
	}
	return nil
}

func (t *minimumSeverityChanged) apply(_ context.Context, _ Queries, g *Group, c *change) error {
	level := t.route.raise(t.alerts)
	if level == g.Severity && g.Urgent == urgentOf(level) {
		return nil
	}
	before := g.Severity
	g.Severity, g.Urgent, t.changed = level, urgentOf(level), true
	if !minimumRise(c, g, before) {
		c.written = true
	}
	return nil
}

// minimumRise records a rise of g from the level before to its level now that the Route's Minimum severity made rather
// than an Alert (C-26.FR-9, C-09.FR-9): the Quiet urgency_raised that removes nothing when it reaches critical, else the
// Quiet severity_raised, both with ReasonMinimumSeverity. A level that did not rise records nothing, as a falling
// Severity level does; it reports whether it recorded.
func minimumRise(c *change, g *Group, before organization.SeverityLevel) bool {
	switch {
	case rank(g.Severity) <= rank(before):
		return false
	case urgentOf(g.Severity) && !urgentOf(before):
		c.add(entry{Event: EventUrgencyRaised, Variant: VariantRemovesNone, From: g.Status, To: g.Status,
			Reason: ReasonMinimumSeverity})
	default:
		c.add(entry{Event: EventSeverityRaised, Reason: ReasonMinimumSeverity})
	}
	return true
}

// applyMinimumSeverity applies the Minimum severity of the Route routeID, as it is now, to its open Alert Groups ids,
// locked in id order, in the transaction of q, and returns how many it changed; after collects what runs once the
// transaction committed.
func (s *Service) applyMinimumSeverity(ctx context.Context, q Queries, routeID int64, ids []int64,
	after *committed) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	r, err := q.GetRoutePolicy(ctx, dbgen.GetRoutePolicyParams{OrgID: s.orgID, ID: routeID})
	if err != nil {
		return 0, fmt.Errorf("read route %d: %w", routeID, err)
	}
	route := policyOf(r)
	locked, err := lock(ctx, q, s.orgID, ids)
	if err != nil {
		return 0, err
	}
	levels, err := firingLevels(ctx, q, s.orgID, ids)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, id := range ids {
		g := locked[id]
		if g == nil || g.RouteID != routeID {
			continue
		}
		t := &minimumSeverityChanged{route: route, alerts: levels[id]}
		if err := s.d.dispatch(ctx, q, g, System, t, after); errors.Is(err, errNotOpen) {
			continue
		} else if err != nil {
			return 0, err
		}
		if t.changed {
			changed++
		}
	}
	return changed, nil
}

// rerenderButtons runs the re-render step of the dispatcher, without a lifecycle event, on the open Alert Groups ids,
// locked in id order in the transaction of q, after a change of a button set (C-28.FR-9): delivery edits, Quietly,
// each Root message whose Desired state the set changed and leaves the others, whose hashes are unchanged, so that a
// second run changes nothing. The Alert Groups themselves do not change, so nothing is saved and no live-update hint is
// sent; a Route template that failed is recorded as the dispatcher records it. It returns how many Alert Groups had a
// Desired state changed; after collects what runs once the transaction committed.
func (s *Service) rerenderButtons(ctx context.Context, q Queries, ids []int64, after *committed) (int, error) {
	if len(ids) == 0 || s.d.rerender == nil {
		return 0, nil
	}
	locked, err := lock(ctx, q, s.orgID, ids)
	if err != nil {
		return 0, err
	}
	now := s.d.clock.Now().UTC()
	n := 0
	for _, id := range ids {
		g := locked[id]
		if g == nil || g.Status == StatusResolved {
			continue
		}
		changed := false
		var failures []TemplateFailure
		if err := s.d.rerender(ctx, q, Rendering{Group: g, Actor: System, After: after.add,
			Fallback: func(f TemplateFailure) { failures = append(failures, f) },
			Changed:  func() { changed = true }}); err != nil {
			return 0, fmt.Errorf("re-render alert group #%d: %w", g.Number, err)
		}
		if err := s.d.recordFallbacks(ctx, q, g, failures, now); err != nil {
			return 0, err
		}
		if changed {
			n++
		}
	}
	return n, nil
}

// firingLevels are the highest Severity levels of the firing Alerts of the Alert Groups ids, info for one without any.
func firingLevels(ctx context.Context, q Queries, orgID int64, ids []int64) (map[int64]organization.SeverityLevel,
	error) {
	rows, err := q.ListFiringLevels(ctx, dbgen.ListFiringLevelsParams{OrgID: orgID, AlertGroupIds: ids})
	if err != nil {
		return nil, fmt.Errorf("read the levels of the alerts firing in the alert groups: %w", err)
	}
	out := make(map[int64]organization.SeverityLevel, len(ids))
	for _, id := range ids {
		out[id] = organization.SeverityInfo
	}
	for _, r := range rows {
		out[r.AlertGroupID] = organization.SeverityLevel(r.SeverityLevel)
	}
	return out, nil
}

// claimUpdate leases the earliest open_group_updates row that is free under l, in a short transaction; false when
// there is none.
func (s *Service) claimUpdate(ctx context.Context, l db.Lease) (update, bool, error) {
	var rows []dbgen.ClaimOpenGroupUpdateRow
	now := l.Clocks.Real.Now().UTC()
	err := s.store.InTx(ctx, func(q Queries) error {
		var err error
		rows, err = q.ClaimOpenGroupUpdate(ctx, dbgen.ClaimOpenGroupUpdateParams{OrgID: s.orgID, Owner: l.Owner,
			Now: now, LeaseUntil: now.Add(l.Duration), Kinds: updateKinds})
		return err
	})
	if err != nil {
		return update{}, false, fmt.Errorf("claim an open alert group update: %w", err)
	}
	if len(rows) == 0 {
		return update{}, false, nil
	}
	r := rows[0]
	return update{id: r.ID, kind: r.Kind, routeID: r.RouteID.Int64, routePID: r.RoutePublicID,
		destinationPID: r.DestinationPublicID}, true, nil
}

// batchOutcome is what one batch of a claimed row did: how many Alert Groups it changed, whether it was the last,
// and whether the lease was lost to another replica, which then did nothing.
type batchOutcome struct {
	changed int
	done    bool
	lost    bool
}

// updateBatch runs the next batch of at most limit open Alert Groups of the claimed row u in one transaction that
// still holds its lease under l: it changes them, then deletes the row when the batch was the last, or records the
// last Alert Group it reached and renews the lease.
func (s *Service) updateBatch(ctx context.Context, l db.Lease, u update, limit int) (batchOutcome, error) {
	var out batchOutcome
	var after committed
	err := s.store.InTx(ctx, func(q Queries) error {
		out, after = batchOutcome{}, nil
		row, err := q.LockOpenGroupUpdate(ctx, dbgen.LockOpenGroupUpdateParams{OrgID: s.orgID, ID: u.id,
			Owner: l.Owner, Now: l.Clocks.Real.Now().UTC()})
		if errors.Is(err, pgx.ErrNoRows) {
			out.lost = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("check the lease: %w", err)
		}
		var ids []int64
		lim := int32(limit) //nolint:gosec // G115: a batch is UpdateBatch or the small one of a test
		switch row.Kind {
		case KindRouteMinimumSeverity:
			if ids, err = q.ListOpenGroupsOfRouteAfter(ctx, dbgen.ListOpenGroupsOfRouteAfterParams{OrgID: s.orgID,
				RouteID: row.RouteID.Int64, AfterID: row.AfterAlertGroupID, Lim: lim}); err != nil {
				return fmt.Errorf("list the open alert groups of route %d: %w", row.RouteID.Int64, err)
			}
			if out.changed, err = s.applyMinimumSeverity(ctx, q, row.RouteID.Int64, ids, &after); err != nil {
				return err
			}
		case KindDestinationButtons:
			if ids, err = q.ListOpenGroupsOfDestinationAfter(ctx, dbgen.ListOpenGroupsOfDestinationAfterParams{
				OrgID: s.orgID, DestinationID: row.DestinationID.Int64, AfterID: row.AfterAlertGroupID,
				Lim: lim}); err != nil {
				return fmt.Errorf("list the open alert groups of destination %d: %w", row.DestinationID.Int64, err)
			}
			if out.changed, err = s.rerenderButtons(ctx, q, ids, &after); err != nil {
				return err
			}
		case KindOrganizationButtons:
			if ids, err = q.ListOpenGroupsFollowingOrganizationAfter(ctx,
				dbgen.ListOpenGroupsFollowingOrganizationAfterParams{OrgID: s.orgID, AfterID: row.AfterAlertGroupID,
					Lim: lim}); err != nil {
				return fmt.Errorf("list the open alert groups that follow the organization's buttons: %w", err)
			}
			if out.changed, err = s.rerenderButtons(ctx, q, ids, &after); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no handler for the kind %q", row.Kind)
		}
		if len(ids) < limit {
			out.done = true
			if err := q.DeleteOpenGroupUpdate(ctx, dbgen.DeleteOpenGroupUpdateParams{OrgID: s.orgID,
				ID: u.id}); err != nil {
				return fmt.Errorf("delete the done open alert group update: %w", err)
			}
			return nil
		}
		if err := q.AdvanceOpenGroupUpdate(ctx, dbgen.AdvanceOpenGroupUpdateParams{OrgID: s.orgID, ID: u.id,
			AfterAlertGroupID: ids[len(ids)-1],
			LeaseUntil:        l.Clocks.Real.Now().UTC().Add(l.Duration)}); err != nil {
			return fmt.Errorf("record the progress of the open alert group update: %w", err)
		}
		return nil
	})
	if err != nil {
		return batchOutcome{}, err
	}
	after.run(ctx)
	return out, nil
}

// nextUpdate is how long until a row can be claimed, at most limit: none when one is free, else until the end of the
// earliest lease still held.
func (s *Service) nextUpdate(ctx context.Context, l db.Lease, limit time.Duration) (time.Duration, error) {
	now := l.Clocks.Real.Now().UTC()
	next, err := s.store.NextOpenGroupUpdate(ctx, dbgen.NextOpenGroupUpdateParams{OrgID: s.orgID, Now: now,
		Kinds: updateKinds})
	if err != nil {
		return 0, fmt.Errorf("read the next open alert group update: %w", err)
	}
	if next.Year() <= 1 {
		return limit, nil
	}
	return min(limit, max(next.Sub(now), 0)), nil
}

// UpdateWorker runs the open Alert Group updates of this replica (C-26.FR-9).
type UpdateWorker struct {
	// Service is the Alert Group lifecycle of the Organization, set before Run.
	Service *Service
	// Lease names this replica in leased_by and carries the clocks: leases on the real clock.
	Lease db.Lease
	Log   *logging.Logger
	// MaxWait bounds the wait between rounds, and Batch the open Alert Groups of one batch; zero is the default.
	MaxWait time.Duration
	Batch   int
	// Wait waits for d or until a wake arrives; nil waits on the real clock.
	Wait func(ctx context.Context, d time.Duration, wake <-chan struct{})

	once sync.Once
	wake chan struct{}
}

func (w *UpdateWorker) init() {
	w.once.Do(func() { w.wake = make(chan struct{}, 1) })
}

// Wake makes the worker run a round at once: a row was inserted, the development clock moved, or the LISTEN is back.
func (w *UpdateWorker) Wake() {
	w.init()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run runs the updates until ctx ends: a round at once, then a wait until a row can be claimed, between the minimum
// and MaxWait, or until woken; after a failed round, a backoff.
func (w *UpdateWorker) Run(ctx context.Context) {
	w.init()
	wait := w.Wait
	if wait == nil {
		wait = waitReal
	}
	failures := 0
	for ctx.Err() == nil {
		next, err := w.Round(ctx)
		d := max(next, updateMinWait)
		if err != nil {
			failures++
			d = db.Backoff(failures, updateFailureBackoff, w.maxWait(), rand.Float64)
		} else {
			failures = 0
		}
		wait(ctx, d, w.wake)
	}
}

func (w *UpdateWorker) maxWait() time.Duration {
	if w.MaxWait <= 0 {
		return UpdateMaxWait
	}
	return w.MaxWait
}

func (w *UpdateWorker) batch() int {
	if w.Batch <= 0 {
		return UpdateBatch
	}
	return w.Batch
}

func waitReal(ctx context.Context, d time.Duration, wake <-chan struct{}) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-t.C:
	}
}

// Round claims and runs every row that is free, one at a time, and returns how long until the next one can be
// claimed. A row that fails is logged and runs again on any replica once its lease runs out; the round goes on.
func (w *UpdateWorker) Round(ctx context.Context) (time.Duration, error) {
	for {
		u, ok, err := w.Service.claimUpdate(ctx, w.Lease)
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
		w.run(ctx, u)
	}
	return w.Service.nextUpdate(ctx, w.Lease, w.maxWait())
}

// run runs the batches of a claimed row until it is done, fails or its lease is lost.
func (w *UpdateWorker) run(ctx context.Context, u update) {
	scope := scopeOf(u)
	total := 0
	for {
		out, err := w.Service.updateBatch(ctx, w.Lease, u, w.batch())
		if err != nil {
			if ctx.Err() == nil {
				w.Log.Log(ctx, logging.OpenGroupUpdateFailed, append(scope, logging.F("error", err.Error()))...)
			}
			return
		}
		total += out.changed
		if out.lost {
			return
		}
		if out.done {
			w.Log.Log(ctx, logging.OpenGroupUpdateDone, append(scope, logging.F("alert_groups", total))...)
			return
		}
	}
}

// scopeOf is the kind of a row and the scope its kind names, as its log lines carry them.
func scopeOf(u update) []logging.Field {
	out := []logging.Field{logging.F("kind", u.kind)}
	if u.routePID != "" {
		out = append(out, logging.F("route", u.routePID))
	}
	if u.destinationPID != "" {
		out = append(out, logging.F("destination", u.destinationPID))
	}
	return out
}
