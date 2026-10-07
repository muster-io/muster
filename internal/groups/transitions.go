// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
)

// creation is the transition of a new Alert Group: its first Alerts, recorded as one `created` entry.
type creation struct {
	e    *engine
	slot *slot
}

func (*creation) precondition(*Group) error { return nil }

func (t *creation) apply(ctx context.Context, _ Queries, g *Group, c *change) error {
	if err := t.e.join(ctx, g, t.slot.joins); err != nil {
		return err
	}
	g.Status = StatusFiring
	c.reason = string(EventCreated)
	c.add(entry{Event: EventCreated, To: StatusFiring, Fingerprints: fingerprints(t.slot.joins),
		Conflicts: conflicts(t.slot.joins)})
	return nil
}

// snapshotChange is what one transaction of processing, or the end of a Grace period, does to an existing Alert
// Group: Alerts join it or reopen it, Continuations, annotation changes, a rise in Severity level, and resolutions,
// recorded in that order.
type snapshotChange struct {
	e     *engine
	b     *batch
	route *routeInfo
}

func (*snapshotChange) precondition(*Group) error { return nil }

func (t *snapshotChange) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	before := g.Severity
	if s := t.b.slot; s != nil {
		if err := t.joins(ctx, q, g, s, c); err != nil {
			return err
		}
	}
	if err := t.members(ctx, q, g, c); err != nil {
		return err
	}
	if s := t.b.slot; s != nil && s.reopen {
		// A reopened Alert Group fires only its new Alerts: its level is theirs, and the Reopen records it.
		if err := t.level(ctx, g, levelOf(s.joins)); err != nil {
			return err
		}
	} else if err := t.rise(ctx, g, before, c); err != nil {
		return err
	}
	return t.resolutions(ctx, q, g, c)
}

// level sets the Severity level of g and the urgency it gives on its Route (C-08.FR-6).
func (t *snapshotChange) level(ctx context.Context, g *Group, level organization.SeverityLevel) error {
	st, err := t.e.grouping(ctx)
	if err != nil {
		return err
	}
	g.Severity, g.Urgent = level, urgentOf(t.route, level, st.CriticalIsUrgent)
	return nil
}

// levelOf is the highest Severity level of Alerts, info for none.
func levelOf(alerts []*alertRow) organization.SeverityLevel {
	level := organization.SeverityInfo
	for _, a := range alerts {
		level = higher(level, a.Severity)
	}
	return level
}

// joins adds the newly firing Alerts of a slot: a Reopen (C-09.FR-4), or Alerts joining an open Alert Group as
// Replacements (C-09.FR-7) and additions (C-09.FR-6).
func (t *snapshotChange) joins(ctx context.Context, q Queries, g *Group, s *slot, c *change) error {
	if err := t.e.join(ctx, g, s.joins); err != nil {
		return err
	}
	for _, a := range s.joins {
		g.joined(a)
	}
	if s.reopen {
		return t.reopen(ctx, q, g, s, c)
	}
	st, err := t.e.grouping(ctx)
	if err != nil {
		return err
	}
	var added []*alertRow
	for _, a := range s.joins {
		label := ""
		for _, m := range t.e.members[g.ID] {
			if label = replacedLabelOf(a, m, st.InstanceLabels); label != "" {
				break
			}
		}
		if label == "" {
			added = append(added, a)
			continue
		}
		c.add(entry{Event: EventAlertReplaced, Fingerprints: []string{a.Fingerprint}, ReplacedLabel: label,
			Conflicts: conflicts([]*alertRow{a})})
	}
	if len(added) > 0 {
		c.add(entry{Event: EventAlertsAdded, Variant: statusVariant(g.Status), Fingerprints: fingerprints(added),
			Conflicts: conflicts(added)})
	}
	return nil
}

// replacedLabelOf compares a new Alert with an Alert firing in the Alert Group.
func replacedLabelOf(a *alertRow, m dbgen.ListGroupFiringAlertsRow, instance []string) string {
	firing := &alertRow{ID: m.AlertID}
	if err := readLabels(m.Labels, []byte("{}"), firing); err != nil {
		return ""
	}
	return replacedLabel(a.Labels, firing.Labels, instance)
}

// reopen returns a system-resolved Alert Group to the status it had (C-09.FR-4): acknowledged with the same Owner
// while that Owner is active, snoozed while the Snooze end is ahead, firing otherwise.
func (t *snapshotChange) reopen(ctx context.Context, q Queries, g *Group, s *slot, c *change) error {
	p := g.Prior
	to := StatusFiring
	if p != nil {
		switch {
		case p.Status == StatusAcknowledged && p.OwnerUserID != nil:
			active, err := q.IsActiveUser(ctx, dbgen.IsActiveUserParams{OrgID: t.e.s.orgID, ID: *p.OwnerUserID})
			if err != nil {
				return fmt.Errorf("read the owner of alert group #%d: %w", g.Number, err)
			}
			if active {
				to = StatusAcknowledged
				g.OwnerUserID, g.AcknowledgedAt = p.OwnerUserID, &t.e.now
			}
		case p.Status == StatusSnoozed && (p.SnoozeNoEnd || (p.SnoozeUntil != nil && p.SnoozeUntil.After(t.e.now))) &&
			(p.SnoozedByUserID != nil || p.SnoozedByServiceAccount != nil):
			to = StatusSnoozed
			g.SnoozeUntil, g.SnoozeNoEnd, g.SnoozedWhileUrgent = p.SnoozeUntil, p.SnoozeNoEnd, p.SnoozedWhileUrgent
			g.SnoozedByUserID, g.SnoozedByServiceAccount = p.SnoozedByUserID, p.SnoozedByServiceAccount
			if g.SnoozeNoEnd {
				g.SnoozeUntil = nil
			}
		}
	}
	g.Status = to
	g.ResolvedAt, g.ResolvedByKind, g.ResolvedByUserID, g.ResolvedByServiceAccount = nil, nil, nil, nil
	g.ResolveReason, g.ResolveReasonText, g.ReopenDeadline, g.Prior = nil, nil, nil, nil
	g.ReopenCount++
	if err := stopTimer(ctx, q, t.e.s.orgID, g.ID, TimerReopenWindowEnd); err != nil {
		return err
	}
	c.reason = string(EventReopened)
	c.add(entry{Event: EventReopened, Variant: statusVariant(to), From: StatusResolved, To: to,
		OwnerUserID: g.OwnerUserID, SnoozeUntil: g.SnoozeUntil, Fingerprints: fingerprints(s.joins),
		Conflicts: conflicts(s.joins)})
	return nil
}

// members records the annotation changes and Continuations of Alerts firing in the Alert Group (C-06.FR-12,
// FR-11), in that order: the membership keeps the annotations and the startsAt as last seen.
func (t *snapshotChange) members(ctx context.Context, q Queries, g *Group, c *change) error {
	if len(t.b.continued) == 0 && len(t.b.annotated) == 0 {
		return nil
	}
	changed := map[int64]*alertRow{}
	for _, a := range slices.Concat(t.b.continued, t.b.annotated) {
		changed[a.ID] = a
	}
	p := dbgen.UpdateMembershipsParams{OrgID: t.e.s.orgID}
	for _, id := range slices.Sorted(maps.Keys(changed)) {
		a, m := changed[id], t.b.members[id]
		startsAt, annotations := m.StartsAt, m.Annotations
		if slices.Contains(t.b.continued, a) {
			startsAt = a.StartsAt
		}
		if slices.Contains(t.b.annotated, a) {
			annotations = a.Annotations
		}
		p.Ids = append(p.Ids, m.ID)
		p.StartsAts = append(p.StartsAts, startsAt)
		p.Annotations = append(p.Annotations, jsonOf(annotations))
	}
	if err := q.UpdateMemberships(ctx, p); err != nil {
		return fmt.Errorf("record the changes of the alerts of alert group #%d: %w", g.Number, err)
	}
	if len(t.b.annotated) > 0 {
		c.add(entry{Event: EventAnnotationsChanged, Fingerprints: fingerprints(t.b.annotated)})
	}
	if len(t.b.continued) > 0 {
		c.add(entry{Event: EventAlertContinued, Fingerprints: fingerprints(t.b.continued)})
		number, prints := g.Number, fingerprints(t.b.continued)
		t.e.after.add(func(ctx context.Context) {
			for _, f := range prints {
				t.e.s.log.Log(ctx, logging.AlertContinued, logging.F("group", fmt.Sprintf("#%d", number)),
					logging.F("fingerprint", f))
			}
		})
	}
	return nil
}

// rise records a rise in Severity level (C-08.FR-6, C-09.FR-9). Urgency is judged on the Route and the settings as
// they are now, before and after the rise: a rise that makes the Alert Group Urgent ends a Snooze, unless it was set
// while Urgent, and removes the acknowledgement when the Route says so; any other rise — urgency unchanged, also
// after a configuration edit made it Urgent already — is a Quiet severity_raised that removes nothing.
func (t *snapshotChange) rise(ctx context.Context, g *Group, before organization.SeverityLevel, c *change) error {
	level := higher(before, levelOf(t.b.slot.joinsOrNil()))
	if rank(level) <= rank(before) {
		return nil
	}
	st, err := t.e.grouping(ctx)
	if err != nil {
		return err
	}
	was, now := urgentOf(t.route, before, st.CriticalIsUrgent), urgentOf(t.route, level, st.CriticalIsUrgent)
	g.Severity, g.Urgent = level, now
	if was || !now {
		c.add(entry{Event: EventSeverityRaised})
		return nil
	}
	from := g.Status
	var previous *int64
	removes := false
	switch {
	case g.Status == StatusSnoozed && !g.SnoozedWhileUrgent:
		g.SnoozeUntil, g.SnoozeNoEnd, g.SnoozedByUserID, g.SnoozedByServiceAccount = nil, false, nil, nil
		g.Status, removes = StatusFiring, true
	case g.Status == StatusAcknowledged && t.route.RemovesAck:
		previous = g.OwnerUserID
		g.OwnerUserID, g.AcknowledgedAt = nil, nil
		g.Status, removes = StatusFiring, true
	}
	variant := VariantRemovesNone
	if removes {
		variant = VariantRemoves
		c.reason = string(EventUrgencyRaised)
	}
	c.add(entry{Event: EventUrgencyRaised, Variant: variant, From: from, To: g.Status, PreviousOwner: previous})
	return nil
}

// joinsOrNil are the Alerts of a slot, none for a batch without one.
func (s *slot) joinsOrNil() []*alertRow {
	if s == nil {
		return nil
	}
	return s.joins
}

// resolutions ends the memberships of Alerts that resolved (C-06.FR-4): while others still fire each is an
// `alert_resolved`; when the last one resolves, the system resolves the Alert Group with its reason (C-09.FR-3).
func (t *snapshotChange) resolutions(ctx context.Context, q Queries, g *Group, c *change) error {
	if len(t.b.resolved) == 0 {
		return nil
	}
	p := dbgen.ResolveMembershipsParams{OrgID: t.e.s.orgID, EndedAt: t.e.now}
	alerts := make([]*alertRow, 0, len(t.b.resolved))
	for _, r := range t.b.resolved {
		p.Ids = append(p.Ids, r.member.ID)
		p.Reasons = append(p.Reasons, r.reason)
		p.ReasonTexts = append(p.ReasonTexts, r.text)
		alerts = append(alerts, r.alert)
	}
	if err := q.ResolveMemberships(ctx, p); err != nil {
		return fmt.Errorf("resolve alerts of alert group #%d: %w", g.Number, err)
	}
	n := int64(len(t.b.resolved))
	g.FiringCount = max(g.FiringCount-n, 0)
	g.ResolvedCount += n
	if g.Status == StatusResolved || g.FiringCount > 0 {
		c.add(entry{Event: EventAlertResolved, Fingerprints: fingerprints(alerts)})
		if g.Status == StatusResolved {
			return nil
		}
		// The level is the highest of the Alerts still firing, recorded with the alert_resolved that changed it.
		rows, err := q.ListGroupFiringAlerts(ctx, dbgen.ListGroupFiringAlertsParams{OrgID: t.e.s.orgID,
			AlertGroupIds: []int64{g.ID}})
		if err != nil {
			return fmt.Errorf("read the alerts still firing in alert group #%d: %w", g.Number, err)
		}
		level := organization.SeverityInfo
		for _, r := range rows {
			level = higher(level, severityOf(r.SeverityLevel))
		}
		return t.level(ctx, g, level)
	}
	last := t.b.resolved[len(t.b.resolved)-1]
	return t.e.resolve(ctx, q, g, t.route, last.reason, last.text, fingerprints(alerts), c)
}

// resolve is the resolution by the system (C-09.FR-3, FR-4, FR-10): it ends any Snooze and acknowledgement, keeps
// the status to return to, and opens the Reopen window, except on an Alert Group moved to the Default route.
func (e *engine) resolve(ctx context.Context, q Queries, g *Group, route *routeInfo, reason, text string,
	prints []string, c *change) error {
	from := g.Status
	prior := &Prior{Status: from, OwnerUserID: g.OwnerUserID, SnoozeUntil: g.SnoozeUntil, SnoozeNoEnd: g.SnoozeNoEnd,
		SnoozedWhileUrgent: g.SnoozedWhileUrgent, SnoozedByUserID: g.SnoozedByUserID,
		SnoozedByServiceAccount: g.SnoozedByServiceAccount}
	g.Status = StatusResolved
	g.OwnerUserID, g.AcknowledgedAt = nil, nil
	g.SnoozeUntil, g.SnoozeNoEnd, g.SnoozedWhileUrgent, g.SnoozedByUserID, g.SnoozedByServiceAccount =
		nil, false, false, nil, nil
	g.ResolvedAt, g.ResolvedByKind = &e.now, ptr(ResolvedBySystem)
	g.ResolveReason, g.ResolveReasonText = &reason, &text
	g.Prior, g.ReopenDeadline = nil, nil
	if g.MovedFromRouteID == nil && route.ReopenWindow > 0 {
		deadline := e.now.Add(route.ReopenWindow)
		g.Prior, g.ReopenDeadline = prior, &deadline
		if err := setTimer(ctx, q, e.s.orgID, g.ID, TimerReopenWindowEnd, deadline, e.now); err != nil {
			return err
		}
	}
	c.reason = reason
	c.add(entry{Event: EventResolved, From: from, To: StatusResolved, Reason: reason, Fingerprints: prints})
	return nil
}

// endReopenWindow is the end of the Reopen window of a system-resolved Alert Group: it forgets the status it would
// have returned to, without a Timeline entry. A window that moved or ended already is not due.
type endReopenWindow struct{ now time.Time }

func (t endReopenWindow) precondition(g *Group) error {
	if g.ReopenDeadline == nil || g.ReopenDeadline.After(t.now) {
		return errNotDue
	}
	return nil
}

func (endReopenWindow) apply(_ context.Context, _ Queries, g *Group, c *change) error {
	g.ReopenDeadline, g.Prior, c.written = nil, nil, true
	return nil
}

// errNotDue is a timer whose subject changed since it was set: it changes nothing.
var errNotDue = errors.New("the timer is not due for its alert group")

// EndReopenWindow is the timer reopen_window_end of an Alert Group (C-09.FR-12), through the dispatcher in the
// timer's transaction tx; a window that moved or ended already changes nothing.
func (s *Service) EndReopenWindow(ctx context.Context, tx dbgen.DBTX, groupID int64) error {
	q := s.queries(tx)
	locked, err := lock(ctx, q, s.orgID, []int64{groupID})
	if err != nil {
		return err
	}
	g := locked[groupID]
	if g == nil {
		return nil
	}
	var after committed
	err = s.d.dispatch(ctx, q, g, System, endReopenWindow{now: s.clock.Now().UTC()}, &after)
	if errors.Is(err, errNotDue) {
		return nil
	}
	return err
}

// endGrace is the end of the Grace period of a person-resolved Alert Group whose Alerts still firing moved to the
// Alert Groups that took them; it writes no Timeline entry.
type endGrace struct{ moved int64 }

func (endGrace) precondition(*Group) error { return nil }

func (t endGrace) apply(_ context.Context, _ Queries, g *Group, c *change) error {
	g.GraceDeadline, c.written = nil, true
	g.FiringCount = max(g.FiringCount-t.moved, 0)
	return nil
}

// EndGracePeriod is the timer grace_period_end of an Alert Group a person resolved (C-09.FR-5): the Alerts still
// firing in it are grouped again on its Route as if they had just fired, and leave it for the Alert Group that takes
// them; one they start is marked as firing again after the manual resolve. It runs in the timer's transaction tx; a
// Grace period that moved or ended already changes nothing. A Continuation is never a new firing, so only the
// memberships still firing move.
func (s *Service) EndGracePeriod(ctx context.Context, tx dbgen.DBTX, groupID int64) (ingest.Routed, error) {
	e := s.newEngine(s.queries(tx), tx)
	// The tails are grouped as a Snapshot groups them — their Route FOR SHARE, then the counter row — and the Alert
	// Group with the targets of its tails, found without a lock, are then locked together in id order and checked
	// again.
	peek, err := e.q.PeekGroup(ctx, dbgen.PeekGroupParams{OrgID: s.orgID, ID: groupID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!peek.GraceDeadline.Valid ||
		peek.GraceDeadline.Time.After(e.now))) {
		return ingest.Routed{}, nil
	}
	if err != nil {
		return ingest.Routed{}, fmt.Errorf("read alert group %d: %w", groupID, err)
	}
	tails, err := e.tailsOf(ctx, groupID, peek.RouteID)
	if err != nil {
		return ingest.Routed{}, err
	}
	if err := e.place(ctx, tails); err != nil {
		return ingest.Routed{}, err
	}
	ids := []int64{groupID}
	for _, sl := range e.slots {
		if sl.target != 0 {
			ids = append(ids, sl.target)
		}
	}
	locked, err := lock(ctx, e.q, s.orgID, ids)
	if err != nil {
		return ingest.Routed{}, err
	}
	maps.Copy(e.groups, locked)
	g := e.groups[groupID]
	if g == nil || g.GraceDeadline == nil || g.GraceDeadline.After(e.now) {
		return ingest.Routed{}, nil
	}
	// Only the Alerts that still fire in it once it is locked move: one resolved meanwhile stays.
	still, err := e.tailsOf(ctx, groupID, g.RouteID)
	if err != nil {
		return ingest.Routed{}, err
	}
	firing := map[int64]bool{}
	for _, a := range still {
		firing[a.ID] = true
	}
	moved := 0
	for _, sl := range e.slots {
		sl.joins = slices.DeleteFunc(sl.joins, func(a *alertRow) bool { return !firing[a.ID] })
		moved += len(sl.joins)
	}
	e.slots = slices.DeleteFunc(e.slots, func(sl *slot) bool { return len(sl.joins) == 0 })
	e.source = g
	if err := e.lockTargets(ctx); err != nil {
		return ingest.Routed{}, err
	}
	if err := e.apply(ctx); err != nil {
		return ingest.Routed{}, err
	}
	if err := s.d.dispatch(ctx, e.q, g, System, endGrace{moved: int64(moved)}, &e.after); err != nil {
		return ingest.Routed{}, err
	}
	return e.routed(), nil
}

// tailsOf reads the Alerts still firing in an Alert Group, to be grouped again on the Route routeID.
func (e *engine) tailsOf(ctx context.Context, groupID, routeID int64) ([]*alertRow, error) {
	rows, err := e.q.ListGroupFiringAlerts(ctx, dbgen.ListGroupFiringAlertsParams{OrgID: e.s.orgID,
		AlertGroupIds: []int64{groupID}})
	if err != nil {
		return nil, fmt.Errorf("read the alerts still firing in alert group %d: %w", groupID, err)
	}
	if e.tails == nil {
		e.tails = map[int64]int64{}
	}
	out := make([]*alertRow, 0, len(rows))
	for _, r := range rows {
		a := &alertRow{ID: r.AlertID, IntegrationID: r.IntegrationID, Fingerprint: r.Fingerprint,
			Conflicts: r.StaticLabelConflicts, Firing: r.Status == "firing", StartsAt: r.StartsAt.UTC(),
			Episode: r.Episode, RouteID: routeID, Severity: severityOf(r.SeverityLevel)}
		if err := readLabels(r.Labels, r.Annotations, a); err != nil {
			return nil, err
		}
		if e.alerts[a.ID] == nil {
			e.alerts[a.ID] = a
		}
		e.tails[a.ID] = r.ID
		out = append(out, a)
	}
	return out, nil
}

// lockTargets locks the targets of the slots and checks them, as lockAll does without the changes of Alerts that
// fire somewhere.
func (e *engine) lockTargets(ctx context.Context) error {
	for range maxRediscoveries {
		var missing []int64
		for _, s := range e.slots {
			if s.target != 0 && e.groups[s.target] == nil {
				missing = append(missing, s.target)
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
		}
		stable := true
		for _, s := range e.slots {
			if s.target == 0 || e.valid(s) {
				continue
			}
			if err := e.find(ctx, s); err != nil {
				return err
			}
			stable = false
		}
		if stable {
			return nil
		}
	}
	return errors.New("the alert groups kept changing while grouping locked them")
}
