// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// Storms (C-11.FR-6): the new Alert Groups of a Route are counted over the last minute; the one that makes the count
// exceed route.storm_threshold starts a Storm. Each Destination of the Route then gets one Storm summary, Loud at its
// first Publication and edited Quietly as Alert Groups join; a new Alert Group that is not Urgent is held, not
// published, while an Urgent one is published at once, ahead in the queue. The calm check ends the Storm once the count
// stayed at or below the threshold for delivery.storm_calm_period: the summary gets its final state — Quiet even where
// it was never published — the held Alert Groups still open are published Quietly within the limiters, and those that
// resolved are withheld. A Destination that joins the Route during a Storm gets its summary and holds the Alert Groups
// the Storm holds; one that leaves the Route retires its summary there. The summary texts are fixed English lines in
// UTC until S-036.

// TimerStormCalmCheck is the timers kind of the calm check of a Storm.
const TimerStormCalmCheck = "storm_calm_check"

// hintRoute is the live-update hint of a Route, whose Storm state changed.
const hintRoute = "route"

// stormQueries are the queries of Storms.
type stormQueries interface {
	CountRecentGroups(ctx context.Context, arg dbgen.CountRecentGroupsParams) (int64, error)
	JoinStorm(ctx context.Context, arg dbgen.JoinStormParams) (dbgen.JoinStormRow, error)
	StartStorm(ctx context.Context, arg dbgen.StartStormParams) (dbgen.StartStormRow, error)
	SetStormTimer(ctx context.Context, arg dbgen.SetStormTimerParams) error
	EnsureStormSummary(ctx context.Context, arg dbgen.EnsureStormSummaryParams) (dbgen.EnsureStormSummaryRow, error)
	ListStormSummaries(ctx context.Context, arg dbgen.ListStormSummariesParams) ([]dbgen.ListStormSummariesRow, error)
	ReleaseHeld(ctx context.Context, arg dbgen.ReleaseHeldParams) error
	LockStorm(ctx context.Context, arg dbgen.LockStormParams) (dbgen.LockStormRow, error)
	GetStormRankTime(ctx context.Context, arg dbgen.GetStormRankTimeParams) (time.Time, error)
	SetStormCalmSince(ctx context.Context, arg dbgen.SetStormCalmSinceParams) error
	CountHeldOpen(ctx context.Context, arg dbgen.CountHeldOpenParams) (int64, error)
	ReleaseHeldOpen(ctx context.Context, arg dbgen.ReleaseHeldOpenParams) error
	WithholdHeldResolved(ctx context.Context, arg dbgen.WithholdHeldResolvedParams) ([]string, error)
	EndStorm(ctx context.Context, arg dbgen.EndStormParams) error
	QuietStormSummaries(ctx context.Context, arg dbgen.QuietStormSummariesParams) error
	GetActiveStorm(ctx context.Context, arg dbgen.GetActiveStormParams) (dbgen.GetActiveStormRow, error)
	RetireStormSummary(ctx context.Context, arg dbgen.RetireStormSummaryParams) error
	ListStormActivity(ctx context.Context, orgID int64) ([]dbgen.ListStormActivityRow, error)
}

// storm is the active Storm of a Route that a new Alert Group joined: its counts and when it started.
type storm struct {
	id            int64
	started       time.Time
	count, urgent int64
}

// joinStorm counts a new Alert Group of the Route against its Storm: it joins the active Storm, or starts one when
// the count of the last minute exceeds the Storm threshold — with the calm check, the hint and the storm_started line,
// which later logs once the dispatcher's transaction committed — and returns the Storm, or nil when there is none.
func (s *Service) joinStorm(ctx context.Context, q queries, g *groups.Group, route dbgen.GetRouteDeliveryRow,
	now time.Time, later func(func(ctx context.Context))) (*storm, error) {
	joined, err := q.JoinStorm(ctx, dbgen.JoinStormParams{OrgID: s.orgID, RouteID: g.RouteID, Urgent: g.Urgent})
	if err == nil {
		return &storm{id: joined.ID, started: joined.StartedAt, count: joined.AlertGroupCount,
			urgent: joined.UrgentCount}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("join the storm of route %s: %w", route.PublicID, err)
	}
	n, err := q.CountRecentGroups(ctx, dbgen.CountRecentGroupsParams{OrgID: s.orgID, RouteID: g.RouteID,
		Since: now.Add(-StormWindow)})
	if err != nil {
		return nil, fmt.Errorf("count the new alert groups of route %s: %w", route.PublicID, err)
	}
	if n <= route.StormThreshold {
		return nil, nil
	}
	started, err := q.StartStorm(ctx, dbgen.StartStormParams{OrgID: s.orgID, RouteID: g.RouteID, Now: now,
		Urgent: g.Urgent})
	if errors.Is(err, pgx.ErrNoRows) {
		// Another transaction started it first and committed: join it.
		joined, err := q.JoinStorm(ctx, dbgen.JoinStormParams{OrgID: s.orgID, RouteID: g.RouteID, Urgent: g.Urgent})
		if err != nil {
			return nil, fmt.Errorf("join the storm of route %s: %w", route.PublicID, err)
		}
		return &storm{id: joined.ID, started: joined.StartedAt, count: joined.AlertGroupCount,
			urgent: joined.UrgentCount}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("start the storm of route %s: %w", route.PublicID, err)
	}
	st := &storm{id: started.ID, started: started.StartedAt, count: started.AlertGroupCount,
		urgent: started.UrgentCount}
	if err := q.SetStormTimer(ctx, dbgen.SetStormTimerParams{OrgID: s.orgID, StormID: st.id,
		Deadline: now.Add(StormCalmPeriod), Now: now}); err != nil {
		return nil, fmt.Errorf("set the calm check of the storm of route %s: %w", route.PublicID, err)
	}
	if err := q.NotifyDelivery(ctx, groups.TimersChannel); err != nil {
		return nil, fmt.Errorf("wake the timer workers: %w", err)
	}
	if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: hintRoute, ID: route.PublicID}); err != nil {
		return nil, err
	}
	routeID, count, urgent := route.PublicID, st.count, st.urgent
	later(func(ctx context.Context) {
		s.log.Log(ctx, logging.StormStarted, logging.F("route", routeID), logging.F("alert_groups", count),
			logging.F("urgent", urgent))
	})
	return st, nil
}

// stormRoute is the Route of a Storm as its summary names it, in its language.
type stormRoute struct {
	publicID, name, language string
}

// renderSummaries sets the Desired state of the Storm summary of st in each Destination of its Route, creating the
// summaries a new Storm needs; an update of what it shows is a Quiet edit.
func (s *Service) renderSummaries(ctx context.Context, q queries, st *storm, route stormRoute,
	dests []dbgen.ListRouteDestinationsRow, now time.Time) error {
	m := s.renderer.Storm(route.publicID, route.name, route.language, st.count, st.urgent)
	for _, d := range dests {
		row, err := q.EnsureStormSummary(ctx, dbgen.EnsureStormSummaryParams{OrgID: s.orgID, DestinationID: d.ID,
			StormID: pgtype.Int8{Int64: st.id, Valid: true}, Now: now})
		if err != nil {
			return fmt.Errorf("create the storm summary in %s: %w", d.PublicID, err)
		}
		msg, err := s.summaryOf(ctx, q.DB(), d.ID, d.Type, route.language, m, StormState{Route: route.name,
			AlertGroupCount: st.count, UrgentCount: st.urgent})
		if err != nil {
			return fmt.Errorf("render the storm summary in %s: %w", d.PublicID, err)
		}
		if err := s.setSummary(ctx, q, row.ID, row.DesiredHash, msg, now); err != nil {
			return err
		}
	}
	return nil
}

// setSummary stores msg as the Desired state of a Storm summary when what it shows changed.
func (s *Service) setSummary(ctx context.Context, q queries, id int64, hash []byte, msg encoded, now time.Time) error {
	if bytes.Equal(hash, msg.hash) {
		return nil
	}
	if _, err := q.SetDesired(ctx, dbgen.SetDesiredParams{OrgID: s.orgID, ID: id, DesiredText: msg.text,
		DesiredPayload: msg.payload, DesiredHash: msg.hash, Now: now}); err != nil {
		return fmt.Errorf("set the desired state of the storm summary %d: %w", id, err)
	}
	return nil
}

// CheckStormCalm is the handler of the timers kind storm_calm_check, in the transaction tx of the timer (C-11.FR-6):
// the count of the last minute last fell to the Route's Storm threshold one minute after the Alert Group one above the
// threshold was created, its calm_since; once delivery.storm_calm_period passed since then, the Storm ends; until then
// the check moves to calm_since plus the period, free of its lease. At the end the summaries get their final state,
// the held Alert Groups still open are published Quietly within the limiters and the resolved ones are withheld; the
// function it returns logs storm_ended once tx committed. A Storm that ended already changes nothing.
func (s *Service) CheckStormCalm(ctx context.Context, tx dbgen.DBTX, stormID int64) (func(context.Context), error) {
	q := s.store.queries(tx)
	st, err := q.LockStorm(ctx, dbgen.LockStormParams{OrgID: s.orgID, ID: stormID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock the storm %d: %w", stormID, err)
	}
	now := s.clock.Now().UTC()
	calm := st.StartedAt
	at, err := q.GetStormRankTime(ctx, dbgen.GetStormRankTimeParams{OrgID: s.orgID, RouteID: st.RouteID,
		Floor: st.StartedAt.Add(-StormWindow), Rank: st.StormThreshold + 1})
	switch {
	case err == nil:
		calm = at.Add(StormWindow)
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("read when the storm %d calmed: %w", stormID, err)
	}
	if now.Before(calm.Add(StormCalmPeriod)) {
		var since pgtype.Timestamptz
		if !calm.After(now) {
			since = pgtype.Timestamptz{Time: calm, Valid: true}
		}
		if err := q.SetStormCalmSince(ctx, dbgen.SetStormCalmSinceParams{OrgID: s.orgID, ID: st.ID,
			CalmSince: since}); err != nil {
			return nil, fmt.Errorf("record the calm of the storm %d: %w", stormID, err)
		}
		if err := q.SetStormTimer(ctx, dbgen.SetStormTimerParams{OrgID: s.orgID, StormID: st.ID,
			Deadline: calm.Add(StormCalmPeriod), Now: now}); err != nil {
			return nil, fmt.Errorf("move the calm check of the storm %d: %w", stormID, err)
		}
		return nil, nil
	}
	return s.endStorm(ctx, q, st, calm, now)
}

// endStorm ends the locked Storm st that is calm since calm.
func (s *Service) endStorm(ctx context.Context, q queries, st dbgen.LockStormRow, calm, now time.Time) (
	func(context.Context), error) {
	open, err := q.CountHeldOpen(ctx, dbgen.CountHeldOpenParams{OrgID: s.orgID, StormID: st.ID})
	if err != nil {
		return nil, fmt.Errorf("count the open alert groups of the storm %d: %w", st.ID, err)
	}
	summaries, err := q.ListStormSummaries(ctx, dbgen.ListStormSummariesParams{OrgID: s.orgID, StormID: st.ID})
	if err != nil {
		return nil, fmt.Errorf("list the summaries of the storm %d: %w", st.ID, err)
	}
	m := s.renderer.StormOver(st.RoutePublicID, st.RouteLanguage, open)
	if err := q.QuietStormSummaries(ctx, dbgen.QuietStormSummariesParams{OrgID: s.orgID, StormID: st.ID,
		Now: now}); err != nil {
		return nil, fmt.Errorf("make the unpublished summaries of the storm %d quiet: %w", st.ID, err)
	}
	for _, sm := range summaries {
		msg, err := s.summaryOf(ctx, q.DB(), sm.DestinationID, sm.DestinationType, st.RouteLanguage, m,
			StormState{Route: st.RouteName, AlertGroupCount: st.AlertGroupCount, UrgentCount: st.UrgentCount,
				Final: true})
		if err != nil {
			return nil, fmt.Errorf("render the final storm summary %d: %w", sm.ID, err)
		}
		if err := s.setSummary(ctx, q, sm.ID, sm.DesiredHash, msg, now); err != nil {
			return nil, err
		}
	}
	if err := q.ReleaseHeldOpen(ctx, dbgen.ReleaseHeldOpenParams{OrgID: s.orgID, StormID: st.ID,
		Loud: deliveryEvent(DeliveryAfterStorm).loud(true), Now: now}); err != nil {
		return nil, fmt.Errorf("publish the open alert groups of the storm %d: %w", st.ID, err)
	}
	withheld, err := q.WithholdHeldResolved(ctx, dbgen.WithholdHeldResolvedParams{OrgID: s.orgID, StormID: st.ID,
		Now: now})
	if err != nil {
		return nil, fmt.Errorf("withhold the resolved alert groups of the storm %d: %w", st.ID, err)
	}
	for _, g := range withheld {
		if err := hintGroup(ctx, q, s.orgID, g); err != nil {
			return nil, err
		}
	}
	if err := q.EndStorm(ctx, dbgen.EndStormParams{OrgID: s.orgID, ID: st.ID, Now: now,
		CalmSince: pgtype.Timestamptz{Time: calm, Valid: true}}); err != nil {
		return nil, fmt.Errorf("end the storm %d: %w", st.ID, err)
	}
	if err := q.NotifyDelivery(ctx, Channel); err != nil {
		return nil, fmt.Errorf("wake the delivery workers: %w", err)
	}
	if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: hintRoute, ID: st.RoutePublicID}); err != nil {
		return nil, err
	}
	route, count, urgent := st.RoutePublicID, st.AlertGroupCount, st.UrgentCount
	return func(ctx context.Context) {
		s.log.Log(ctx, logging.StormEnded, logging.F("route", route), logging.F("alert_groups", count),
			logging.F("urgent", urgent))
	}, nil
}

// stormSeries are the Routes whose muster_storm_active series this replica exports.
var (
	stormMu     sync.Mutex
	stormSeries = map[string]bool{}
)

// ExportStorms sets muster_storm_active to 1 for each Route of the Organization that is not deleted and has an active
// Storm and 0 for the others, as a Leader task; the series of a Route that is gone is removed. Running it twice sets
// the same values.
func (s *Service) ExportStorms(ctx context.Context) error {
	rows, err := s.store.q().ListStormActivity(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("read the storms of the routes: %w", err)
	}
	stormMu.Lock()
	defer stormMu.Unlock()
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[r.PublicID] = true
		stormSeries[r.PublicID] = true
		v := 0.0
		if r.Active {
			v = 1
		}
		metrics.StormActive.With(r.PublicID).Set(v)
	}
	for id := range stormSeries {
		if !seen[id] {
			metrics.StormActive.Delete(id)
			delete(stormSeries, id)
		}
	}
	return nil
}
