// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

// gapStart is the annotation of MusterScheduleGap that says when the nearest gap starts.
const gapStart = "gap_start"

// GapScan is the Leader task schedule_gap_scan (C-27.FR-7): every schedule.gap_scan_interval it computes the Result of
// each Schedule whose gap alert is active from the business clock's now to schedule.gap_alert_horizon ahead, raises
// MusterScheduleGap for each with a gap now or within the horizon, and resolves it for the others, those whose gap
// alert is no longer active and the deleted ones. It reads the open alerts from the database, the pending raises
// included, and writes only what changed, so a second run writes nothing, and two runs at once write the same raise
// twice, which the Internal alert's fingerprint deduplicates (ADR-0007). It sets muster_schedule_gap.
type GapScan struct {
	Store Store
	// Business is the business clock, on which the Schedules compute their On-call (C-27.FR-4).
	Business clock.Clock
	Log      *logging.Logger
	// RunbookBase is MUSTER_RUNBOOK_BASE_URL.
	RunbookBase string

	mu sync.Mutex
	// exported are the Schedules of the muster_schedule_gap series this replica exports, by Organization.
	exported map[int64]map[string]bool
}

// gapLookback is how far back the scan looks for the start of a gap that has begun: schedule.gap_view.
const gapLookback = GapView

// gapOf is a Schedule with a gap now or within the horizon: the nearest one, from start, which for a gap that has
// begun is when it began, as far back as gapLookback and no earlier than the Schedule's creation; before marks a gap
// that began before the lookback, of a Schedule created before it too.
type gapOf struct {
	schedule Schedule
	start    time.Time
	before   bool
}

// Scan runs the gap scan of the Organization orgID.
func (g *GapScan) Scan(ctx context.Context, orgID int64) error {
	now := g.Business.Now().UTC()
	s := &Service{orgID: orgID, store: g.Store, clock: g.Business}
	rows, err := g.Store.ListGapScanSchedules(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list the schedules for the gap scan: %w", err)
	}
	all := make([]Schedule, len(rows))
	var active []Schedule
	for i, r := range rows {
		all[i] = Schedule{ID: r.ID, PublicID: r.PublicID, Name: r.Name, TimeZone: r.TimeZone, Main: r.Main,
			GapAlert: r.GapAlert, CreatedAt: r.CreatedAt.UTC(), Version: r.Version,
			Team: TeamRef{ID: r.TeamID, PublicID: r.TeamPublicID, Name: r.TeamName, Slug: r.TeamSlug,
				Deleted: r.TeamDeleted}}
		if all[i].GapAlertActive() {
			active = append(active, all[i])
		}
	}
	if err := s.withLayers(ctx, g.Store, active); err != nil {
		return err
	}
	res, err := s.results(ctx, g.Store, active, now, now.Add(GapAlertHorizon))
	if err != nil {
		return err
	}
	want := map[string]gapOf{}
	var begun []Schedule
	for i, sc := range active {
		if gaps := res[i].Gaps(); len(gaps) > 0 {
			want[sc.PublicID] = gapOf{schedule: sc, start: whole(gaps[0].Start)}
			if gaps[0].Start.Equal(now) {
				begun = append(begun, sc)
			}
		}
	}
	if err := g.began(ctx, s, now, begun, want); err != nil {
		return err
	}
	// The gauge is the backup of the Internal alert, so it follows the Result even when the alerts cannot be written.
	g.export(orgID, all, want)
	label, err := g.Store.GetOrganizationTeamLabel(ctx, orgID)
	if err != nil {
		return fmt.Errorf("read the team label: %w", err)
	}
	return g.write(ctx, orgID, now, label, want)
}

// began finds when each gap of begun, which the Result from now cuts at now, began: the start of the gap that ends at
// now in the Result of the gapLookback before it, read together for all of them, or the Schedule's creation when that
// is later; created_at never changes, so that start stays from scan to scan.
func (g *GapScan) began(ctx context.Context, s *Service, now time.Time, begun []Schedule, want map[string]gapOf) error {
	if len(begun) == 0 {
		return nil
	}
	from := now.Add(-gapLookback)
	window := whole(from)
	res, err := s.results(ctx, g.Store, begun, from, now)
	if err != nil {
		return err
	}
	for i, sc := range begun {
		gaps := res[i].Gaps()
		if n := len(gaps); n > 0 && gaps[n-1].End.Equal(now) {
			w := want[sc.PublicID]
			start := whole(later(gaps[n-1].Start, sc.CreatedAt))
			w.start, w.before = start, start.Equal(window)
			want[sc.PublicID] = w
		}
	}
	return nil
}

// whole is t in UTC to the second, as gap_start carries it (RFC 3339), so that a start computed again compares equal
// to the one raised: created_at and the bounds of Overrides keep fractions of a second.
func whole(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

// write raises MusterScheduleGap for the Schedules of want that it does not fire for yet, raises it again where their
// names, their Team's slug, the Team label or the nearest gap changed, and resolves the others, in one transaction.
func (g *GapScan) write(ctx context.Context, orgID int64, now time.Time, label string, want map[string]gapOf) error {
	d := internalalerts.ScheduleGap
	raiser := internalalerts.NewRaiser(orgID, g.RunbookBase)
	var detected []gapOf
	var cleared [][2]string
	err := g.Store.InAlertsTx(ctx, func(q internalalerts.Store) error {
		detected, cleared = nil, nil
		open, err := raiser.Open(ctx, q, d)
		if err != nil {
			return err
		}
		for _, id := range slices.Sorted(maps.Keys(want)) {
			w := want[id]
			sc := w.schedule
			entity := internalalerts.Entity{ID: sc.PublicID, Name: sc.Name}
			extra := internalalerts.TeamExtra(sc.Team.PublicID, sc.Team.Name, label, sc.Team.Slug)
			values := map[string]string{gapStart: w.start.Format(time.RFC3339)}
			labels := d.AlertLabels(entity, extra)
			o, ok := open[id]
			gap := ok && sameGap(o, w)
			if gap {
				values[gapStart] = o.Annotations[gapStart] // the start it was raised with, which may be older
			}
			switch {
			case !ok:
				if err := raiser.RaiseWith(ctx, q, now, d, entity, extra, values); err != nil {
					return fmt.Errorf("raise %s of %s: %w", d.Name, id, err)
				}
				detected = append(detected, w)
			case gap && maps.Equal(o.Labels, labels):
			default:
				if err := raiser.Refresh(ctx, q, now, d, o, labels, values); err != nil {
					return fmt.Errorf("raise %s of %s again: %w", d.Name, id, err)
				}
			}
		}
		for _, id := range slices.Sorted(maps.Keys(open)) {
			if _, ok := want[id]; ok {
				continue
			}
			if err := raiser.Resolve(ctx, q, now, d, id); err != nil {
				return fmt.Errorf("resolve %s of %s: %w", d.Name, id, err)
			}
			cleared = append(cleared, [2]string{id, open[id].Labels[internalalerts.LabelTeamID]})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("write the gap alerts: %w", err)
	}
	for _, w := range detected {
		g.Log.Log(ctx, logging.ScheduleGapDetected, logging.F("schedule", w.schedule.PublicID),
			logging.F("team", w.schedule.Team.PublicID), logging.F("gap_start", w.start))
	}
	for _, c := range cleared {
		g.Log.Log(ctx, logging.ScheduleGapCleared, logging.F("schedule", c[0]), logging.F("team", c[1]))
	}
	return nil
}

// sameGap reports whether the open alert o was raised for the nearest gap w: with the same start, or, for a gap that
// began before the lookback, with any start at or before the lookback's, which moves with every scan; another gap,
// which began after the one raised ended, is not the same. A raise again for new labels keeps the start o has.
func sameGap(o internalalerts.Open, w gapOf) bool {
	raised, err := time.Parse(time.RFC3339, o.Annotations[gapStart])
	if err != nil {
		return false
	}
	return raised.Equal(w.start) || (w.before && !raised.After(w.start))
}

// Forget removes every muster_schedule_gap series this replica exports: it stops leading, and the next Leader exports
// its own.
func (g *GapScan) Forget() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, ids := range g.exported {
		for id := range ids {
			metrics.ScheduleGap.Delete(id)
		}
	}
	g.exported = nil
}

// export sets muster_schedule_gap of each Schedule of the Organization orgID: 1 for those of want and 0 for the
// others, and removes the series of the Schedules it exported before that no longer exist.
func (g *GapScan) export(orgID int64, all []Schedule, want map[string]gapOf) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exported == nil {
		g.exported = map[int64]map[string]bool{}
	}
	now := make(map[string]bool, len(all))
	for _, sc := range all {
		v := 0.0
		if _, ok := want[sc.PublicID]; ok {
			v = 1
		}
		metrics.ScheduleGap.With(sc.PublicID).Set(v)
		now[sc.PublicID] = true
	}
	for id := range g.exported[orgID] {
		if !now[id] {
			metrics.ScheduleGap.Delete(id)
		}
	}
	g.exported[orgID] = now
}
