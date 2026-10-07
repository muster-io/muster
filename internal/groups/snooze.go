// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

// snoozeDeadline is when the Snooze of g ends by itself: its end while it is snoozed with one, nil otherwise.
func snoozeDeadline(g *Group) *time.Time {
	if g.Status != StatusSnoozed || g.SnoozeNoEnd || g.SnoozeUntil == nil {
		return nil
	}
	end := *g.SnoozeUntil
	return &end
}

// syncSnoozeTimer keeps the snooze_end timer of g with its Snooze after a change (C-09.FR-12): a Snooze with an end
// that starts or moves sets the timer at that end, and one that ends — Unsnooze, Acknowledge, Resolve, a resolution by
// the system, a rise to Urgent, the end itself — or that no longer has an end stops it. before is the deadline
// snoozeDeadline gave before the change.
func (d *dispatcher) syncSnoozeTimer(ctx context.Context, q Queries, g *Group, before *time.Time, now time.Time) error {
	after := snoozeDeadline(g)
	switch {
	case after != nil && (before == nil || !before.Equal(*after)):
		return setTimer(ctx, q, d.orgID, g.ID, TimerSnoozeEnd, *after, now)
	case after == nil && before != nil:
		return stopTimer(ctx, q, d.orgID, g.ID, TimerSnoozeEnd)
	}
	return nil
}

// endSnooze is the end of a Snooze at its end (C-09.FR-8, C-10.FR-6): the Alert Group is firing without an Owner,
// with a Loud snooze_ended that lists the Alerts that joined it during the Snooze. A Snooze that ended or moved since
// the timer was set is not due.
type endSnooze struct {
	now   time.Time
	orgID int64
}

func (t endSnooze) precondition(g *Group) error {
	if end := snoozeDeadline(g); end == nil || end.After(t.now) {
		return errNotDue
	}
	return nil
}

func (t endSnooze) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	joined, err := q.ListJoinedDuringSnooze(ctx, dbgen.ListJoinedDuringSnoozeParams{OrgID: t.orgID,
		AlertGroupID: g.ID})
	if err != nil {
		return fmt.Errorf("read the alerts that joined alert group #%d during its snooze: %w", g.Number, err)
	}
	clearSnooze(g)
	g.Status = StatusFiring
	c.reason = string(EventSnoozeEnded)
	c.add(entry{Event: EventSnoozeEnded, From: StatusSnoozed, To: StatusFiring, Fingerprints: orEmpty(joined)})
	return nil
}

// EndSnooze is the timer snooze_end of an Alert Group (C-09.FR-8, FR-12), through the dispatcher as a system
// transition in the timer's transaction tx; a Snooze that ended or moved meanwhile changes nothing, so the timer is
// safe to fire twice. It returns what to run once that transaction committed.
func (s *Service) EndSnooze(ctx context.Context, tx dbgen.DBTX, groupID int64) (func(context.Context), error) {
	q := s.queries(tx)
	locked, err := lock(ctx, q, s.orgID, []int64{groupID})
	if err != nil {
		return nil, err
	}
	g := locked[groupID]
	if g == nil {
		return nil, nil
	}
	var after committed
	err = s.d.dispatch(ctx, q, g, System, endSnooze{now: s.clock.Now().UTC(), orgID: s.orgID}, &after)
	if errors.Is(err, errNotDue) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return after.run, nil
}
