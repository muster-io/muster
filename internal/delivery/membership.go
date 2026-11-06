// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/routing"
)

// Destinations joining and leaving Routes and Teams (C-11.FR-14, C-26.FR-6, C-09.FR-19): a change of a Route's or a
// Team's Destination entries, or of the Route's "Also to the Teams from the label", publishes the open Alert Groups
// Quietly in each Destination their reach now has without a delivery there, and gives the final Quiet edit — "No
// longer updated here; current state in Muster: {link}" — to each delivery whose entries hold an entry the change took
// away when no entry that still exists names its Destination for the Alert Group, met or not; otherwise the delivery
// stays and is kept current until its final state (C-26.FR-4). A condition alone never removes a Root message. After
// the final edit the delivery is retired and receives nothing more, and one never published there is withheld. A
// deleted Destination gets the final edit everywhere, and its secrets are wiped once none of its deliveries is pending.

// connectionDeleted is the error of the final edits a deleted Connection abandons.
const connectionDeleted = "the Connection was deleted"

// membershipQueries are the queries of Destinations joining and leaving Routes and Teams.
type membershipQueries interface {
	ListOpenGroupsOfRoute(ctx context.Context, arg dbgen.ListOpenGroupsOfRouteParams) (
		[]dbgen.ListOpenGroupsOfRouteRow, error)
	ListGroupTeams(ctx context.Context, arg dbgen.ListGroupTeamsParams) ([]int64, error)
	ListEntryDeliveries(ctx context.Context, arg dbgen.ListEntryDeliveriesParams) ([]dbgen.ListEntryDeliveriesRow,
		error)
	AppendDeliveryEntries(ctx context.Context, arg dbgen.AppendDeliveryEntriesParams) error
	ShareTeamMembership(ctx context.Context, arg dbgen.ShareTeamMembershipParams) error
	RejoinDelivery(ctx context.Context, arg dbgen.RejoinDeliveryParams) (int64, error)
	RetireDeliveries(ctx context.Context, arg dbgen.RetireDeliveriesParams) ([]int64, error)
	RetireGroupDeliveries(ctx context.Context, arg dbgen.RetireGroupDeliveriesParams) ([]int64, error)
	RetireDestinationDeliveries(ctx context.Context, arg dbgen.RetireDestinationDeliveriesParams) ([]int64, error)
	SettleDestinationLeftovers(ctx context.Context, arg dbgen.SettleDestinationLeftoversParams) error
	DropDeliveryReplies(ctx context.Context, arg dbgen.DropDeliveryRepliesParams) error
	DropDestinationReplies(ctx context.Context, arg dbgen.DropDestinationRepliesParams) error
	RecordRetired(ctx context.Context, arg dbgen.RecordRetiredParams) (int64, error)
	WithholdLeased(ctx context.Context, arg dbgen.WithholdLeasedParams) error
	GetDestinationState(ctx context.Context, arg dbgen.GetDestinationStateParams) (dbgen.GetDestinationStateRow, error)
	WipeDestinationSecrets(ctx context.Context, arg dbgen.WipeDestinationSecretsParams) error
	AbandonConnectionDeliveries(ctx context.Context, arg dbgen.AbandonConnectionDeliveriesParams) (
		[]dbgen.AbandonConnectionDeliveriesRow, error)
	ListDeletedDestinationsOfConnection(ctx context.Context, arg dbgen.ListDeletedDestinationsOfConnectionParams) (
		[]int64, error)
}

// link is the page of the Alert Group publicID in Muster, under MUSTER_PUBLIC_URL.
func (w *Worker) link(publicID string) string {
	return strings.TrimSuffix(w.PublicURL, "/") + "/alert-groups/" + publicID
}

// shareTeams takes the membership locks of the Teams ids shared, in id order (db.TeamMembershipLockClass), after the
// Route's and before the Teams' entries are read.
func (s *Service) shareTeams(ctx context.Context, q queries, ids []int64) error {
	for _, id := range slices.Sorted(slices.Values(ids)) {
		if err := q.ShareTeamMembership(ctx, dbgen.ShareTeamMembershipParams{LockClass: db.TeamMembershipLockClass,
			TeamID: id}); err != nil {
			return fmt.Errorf("lock the destinations of team %d: %w", id, err)
		}
	}
	return nil
}

// naming is what names the Destinations of a Route's open Alert Groups after a change: the Route's entries and, while
// it delivers to the Teams, the entries of each Team, with the rows of their Destinations.
type naming struct {
	route   []routing.Entry
	toTeams bool
	teams   map[int64][]routing.Entry
	dests   map[int64]dbgen.ListRouteDestinationsRow
}

// namingOf reads the entries of the Route routeID and, with toTeams, of the Teams teams, through q.
func (s *Service) namingOf(ctx context.Context, q queries, routeID int64, toTeams bool, teams []int64) (naming,
	error) {
	n := naming{toTeams: toTeams, teams: map[int64][]routing.Entry{}, dests: map[int64]dbgen.ListRouteDestinationsRow{}}
	rows, err := q.ListRouteDestinations(ctx, dbgen.ListRouteDestinationsParams{OrgID: s.orgID, RouteID: routeID})
	if err != nil {
		return naming{}, fmt.Errorf("list the destinations of route %d: %w", routeID, err)
	}
	for _, d := range rows {
		n.dests[d.ID] = d
		n.route = append(n.route, routing.Entry{DestinationID: d.ID,
			Condition: routing.SeverityCondition(d.SeverityCondition)})
	}
	if !toTeams || len(teams) == 0 {
		return n, nil
	}
	trs, err := q.ListTeamDestinations(ctx, dbgen.ListTeamDestinationsParams{OrgID: s.orgID, TeamIds: teams})
	if err != nil {
		return naming{}, fmt.Errorf("list the destinations of the teams of route %d: %w", routeID, err)
	}
	for _, t := range trs {
		if _, ok := n.dests[t.ID]; !ok {
			n.dests[t.ID] = dbgen.ListRouteDestinationsRow{ID: t.ID, PublicID: t.PublicID, Name: t.Name, Type: t.Type,
				ConnectionID: t.ConnectionID, Health: t.Health, WebhookMode: t.WebhookMode,
				WebhookReplies: t.WebhookReplies, Buttons: t.Buttons, OrganizationButtons: t.OrganizationButtons}
		}
		n.teams[t.TeamID] = append(n.teams[t.TeamID], routing.Entry{DestinationID: t.ID,
			Condition: routing.SeverityCondition(t.SeverityCondition)})
	}
	return n, nil
}

// reach is the reach of an open Alert Group of the Severity level and the Teams teams under n (routing.Reach).
func (n naming) reach(level string, teams []int64) []routing.Reached {
	var ts []routing.TeamEntries
	for _, id := range teams {
		if es := n.teams[id]; len(es) > 0 {
			ts = append(ts, routing.TeamEntries{TeamID: id, Entries: es})
		}
	}
	return routing.Reach(level, n.route, n.toTeams, ts)
}

// names are the entries that still name the Destination dest for an open Alert Group of the Teams teams, met or not:
// the Route's first, then, while the Route delivers to the Teams, those of its Teams by id.
func (n naming) names(dest int64, teams []int64) []routing.Reaching {
	var out []routing.Reaching
	for _, e := range n.route {
		if e.DestinationID == dest {
			out = append(out, routing.Reaching{Kind: routing.EntryRoute, Condition: orAny(e.Condition)})
		}
	}
	if !n.toTeams {
		return out
	}
	for _, id := range slices.Sorted(slices.Values(teams)) {
		for _, e := range n.teams[id] {
			if e.DestinationID == dest {
				out = append(out, routing.Reaching{Kind: routing.EntryTeam, TeamID: id, Condition: orAny(e.Condition)})
			}
		}
	}
	return out
}

// orAny is the condition, any for an empty one.
func orAny(c routing.SeverityCondition) routing.SeverityCondition {
	if c == "" {
		return routing.ConditionAny
	}
	return c
}

// entryDelivery is a delivery of an open Alert Group with the entries that reached it.
type entryDelivery struct {
	dbgen.ListEntryDeliveriesRow
	reaching []routing.Reaching
}

// live reports whether the delivery is kept current: it has not ended and waits for no final edit.
func (d entryDelivery) live() bool {
	return !d.DesiredRetire && !ended(d.State)
}

// revivable reports whether the delivery left its Destination — its final edit is done or waits, or it was withheld —
// so that a Destination reached again publishes there afresh, or edits a Root message whose final edit still waits.
func (d entryDelivery) revivable() bool {
	return d.DesiredRetire || d.State == "retired" || d.State == "withheld"
}

// holds reports whether an entry of the kind, and of the Team team for a Team's entry, reached the delivery; a
// delivery made before the entries were recorded holds its Route's entry (deliveries.entries is empty).
func (d entryDelivery) holds(kind string, team int64) bool {
	if kind == routing.EntryRoute && len(d.reaching) == 0 {
		return true
	}
	return slices.ContainsFunc(d.reaching, func(e routing.Reaching) bool {
		return e.Kind == kind && (kind == routing.EntryRoute || team == 0 || e.TeamID == team)
	})
}

// missing are the entries of names that the delivery does not hold yet, each Route or Team once.
func (d entryDelivery) missing(names []routing.Reaching) []routing.Reaching {
	var out []routing.Reaching
	for _, e := range names {
		if !slices.ContainsFunc(d.reaching, func(h routing.Reaching) bool {
			return h.Kind == e.Kind && h.TeamID == e.TeamID
		}) {
			out = append(out, e)
		}
	}
	return out
}

// entryDeliveries are the deliveries of the Alert Groups ids by Alert Group, each with the entries that reached it.
func (s *Service) entryDeliveries(ctx context.Context, q queries, ids []int64) (map[int64][]entryDelivery, error) {
	rows, err := q.ListEntryDeliveries(ctx, dbgen.ListEntryDeliveriesParams{OrgID: s.orgID, AlertGroupIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list the deliveries of the alert groups: %w", err)
	}
	out := map[int64][]entryDelivery{}
	for _, r := range rows {
		d := entryDelivery{ListEntryDeliveriesRow: r}
		if err := json.Unmarshal(r.Entries, &d.reaching); err != nil {
			return nil, fmt.Errorf("read the entries of delivery %d: %w", r.ID, err)
		}
		out[r.AlertGroupID] = append(out[r.AlertGroupID], d)
	}
	return out, nil
}

// settle applies the rule of the final edits to the deliveries cands of one open Alert Group of the Teams teams that
// lost an entry (C-26.FR-6, C-11.FR-14): a delivery whose Destination an entry still names stays, with the entries that
// name it appended, so that the loss of those later finds it; the others get the final edit. It returns the
// Destinations of the deliveries it retired.
func (s *Service) settle(ctx context.Context, q queries, group string, cands []entryDelivery, n naming,
	teams []int64, now time.Time) ([]int64, error) {
	var ids, dests []int64
	for _, d := range cands {
		if names := n.names(d.DestinationID, teams); len(names) > 0 {
			missing := d.missing(names)
			if len(missing) == 0 {
				continue
			}
			b, _ := json.Marshal(missing) // a slice of fixed structs always encodes
			if err := q.AppendDeliveryEntries(ctx, dbgen.AppendDeliveryEntriesParams{OrgID: s.orgID, ID: d.ID,
				Entries: b}); err != nil {
				return nil, fmt.Errorf("record the entries of delivery %d: %w", d.ID, err)
			}
			continue
		}
		ids = append(ids, d.ID)
		dests = append(dests, d.DestinationID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	retired, err := q.RetireDeliveries(ctx, dbgen.RetireDeliveriesParams{OrgID: s.orgID, Ids: ids, Now: now})
	if err != nil {
		return nil, fmt.Errorf("give the final edit to the deliveries of alert group %s: %w", group, err)
	}
	if len(retired) == 0 {
		return nil, nil
	}
	if err := hintGroup(ctx, q, s.orgID, group); err != nil {
		return nil, err
	}
	if err := s.dropReplies(ctx, q, retired); err != nil {
		return nil, err
	}
	return dests, nil
}

// retireSummaries retires the Storm summary of the Route routeID's active Storm in each Destination of dests the
// Route no longer names, once no open Alert Group of the Route is kept current there (RetireStormSummary).
func (s *Service) retireSummaries(ctx context.Context, q queries, routeID int64, dests []int64, n naming,
	now time.Time) error {
	for _, d := range slices.Compact(slices.Sorted(slices.Values(dests))) {
		if slices.ContainsFunc(n.route, func(e routing.Entry) bool { return e.DestinationID == d }) {
			continue
		}
		if err := q.RetireStormSummary(ctx, dbgen.RetireStormSummaryParams{OrgID: s.orgID, RouteID: routeID,
			DestinationID: d, Now: now}); err != nil {
			return fmt.Errorf("retire the storm summary of route %d in destination %d: %w", routeID, d, err)
		}
	}
	return nil
}

// RouteDestinationsChanged is the membership hook of the Routes (C-26.FR-6, C-11.FR-14), in the transaction tx of a
// save of the Route c.RouteID that changed its Destination entries or "Also to the Teams from the label", which holds
// the Route's membership lock exclusively. Under the new entries, for each open Alert Group of the Route at its current
// Severity level and with its Teams: each Destination that an entry the save added or changed, or a Team's entry when
// the save turned the option on, now reaches (touchedBy) without a delivery kept current there gets a Quiet
// Publication, within the limiters, with the entries that reach it — during a Storm, held where the Storm holds the
// Alert Group, with the Storm summary there, and no summary where no held Alert Group arrives — and each delivery whose entries hold an entry the save took away — the Route's
// entry of a Destination taken off it, or every Team's entry when the option was turned off — gets the final edit when
// no entry still names its Destination (settle); a Destination taken off the Route then has the summary of an active
// Storm retired once nothing of the Route is kept current there. A changed condition removes nothing; a fallen level
// gains no Destination its condition no longer meets. Locks: after the Route's membership lock, the membership locks
// of the Teams of its open Alert Groups shared, by Team id, when the Route delivers to the Teams, so that no Team save
// commits between the read of its entries and the deliveries; then the Route's active Storm FOR SHARE
// (LockActiveStorm), before any delivery is written, as in the lock order of worker.go: a calm check waits until the
// deliveries this change holds for the Storm are committed, or ends the Storm before this change reads it. It wakes the
// delivery workers once tx commits.
func (s *Service) RouteDestinationsChanged(ctx context.Context, tx dbgen.DBTX, c routing.MembershipChange) error {
	if !c.Changed() {
		return nil
	}
	q := s.store.queries(tx)
	routeID := c.RouteID
	open, err := q.ListOpenGroupsOfRoute(ctx, dbgen.ListOpenGroupsOfRouteParams{OrgID: s.orgID, RouteID: routeID})
	if err != nil {
		return fmt.Errorf("list the open alert groups of route %d: %w", routeID, err)
	}
	var teams []int64
	for _, g := range open {
		for _, t := range g.TeamIds {
			if !slices.Contains(teams, t) {
				teams = append(teams, t)
			}
		}
	}
	if c.ToTeamsAfter {
		if err := s.shareTeams(ctx, q, teams); err != nil {
			return err
		}
	}
	var st *storm
	active, err := q.LockActiveStorm(ctx, dbgen.LockActiveStormParams{OrgID: s.orgID, RouteID: routeID})
	switch {
	case err == nil:
		st = &storm{id: active.ID, started: active.StartedAt, count: active.AlertGroupCount,
			urgent: active.UrgentCount}
		if open, err = q.ListOpenGroupsOfRoute(ctx, dbgen.ListOpenGroupsOfRouteParams{OrgID: s.orgID,
			RouteID: routeID, StormID: pgtype.Int8{Int64: st.id, Valid: true},
			StormStarted: pgtype.Timestamptz{Time: st.started, Valid: true}}); err != nil {
			return fmt.Errorf("list the open alert groups of route %d: %w", routeID, err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("lock the storm of route %d: %w", routeID, err)
	}
	if len(open) == 0 && st == nil {
		return nil
	}
	n, err := s.namingOf(ctx, q, routeID, c.ToTeamsAfter, teams)
	if err != nil {
		return err
	}
	ids := make([]int64, len(open))
	for i, g := range open {
		ids[i] = g.ID
	}
	had, err := s.entryDeliveries(ctx, q, ids)
	if err != nil {
		return err
	}
	removed := c.Removed()
	teamsOff := c.ToTeamsBefore && !c.ToTeamsAfter
	touched := touchedBy(c)
	now := s.clock.Now().UTC()
	changed := false
	var left []int64
	var summaries []stormDest
	for _, g := range open {
		view := GroupView{ID: g.ID, PublicID: g.PublicID, Number: g.Number, Title: g.Title,
			Status: groups.Status(g.Status), Urgent: g.Urgent}
		var cands []entryDelivery
		for _, d := range had[g.ID] {
			if d.live() && ((slices.Contains(removed, d.DestinationID) && d.holds(routing.EntryRoute, 0)) ||
				(teamsOff && d.holds(routing.EntryTeam, 0))) {
				cands = append(cands, d)
			}
		}
		retired, err := s.settle(ctx, q, g.PublicID, cands, n, g.TeamIds, now)
		if err != nil {
			return err
		}
		left = append(left, retired...)
		changed = changed || len(retired) > 0
		var heldBy *int64
		if g.Held {
			heldBy = &st.id
		}
		gained, err := s.gain(ctx, tx, q, view, n.reach(g.SeverityLevel, g.TeamIds), touched, had[g.ID], n, heldBy,
			now)
		if err != nil {
			return err
		}
		changed = changed || len(gained) > 0
		if heldBy != nil {
			summaries = append(summaries, gained...)
		}
	}
	if st != nil {
		left = append(left, removed...)
		if err := s.retireSummaries(ctx, q, routeID, left, n, now); err != nil {
			return err
		}
		// Only a Destination where a held Alert Group was published gets the Storm summary: a Loud summary where
		// the Storm holds nothing would be the change of configuration's Loud message (ADR-0018).
		if len(summaries) > 0 {
			route, err := q.GetRouteDelivery(ctx, dbgen.GetRouteDeliveryParams{OrgID: s.orgID, ID: routeID})
			if err != nil {
				return fmt.Errorf("read the route %d: %w", routeID, err)
			}
			slices.SortFunc(summaries, func(a, b stormDest) int { return cmp.Compare(a.id, b.id) })
			if err := s.renderSummaries(ctx, q, st, stormRoute{publicID: route.PublicID, name: route.Name,
				language: route.Language}, summaries, now); err != nil {
				return err
			}
			changed = true
		}
	}
	return s.wake(ctx, q, changed)
}

// touchedBy reports, for the save c of a Route, whether an entry that reaches a Destination is one the save added or
// whose condition it changed, or a Team's entry when the save turned "Also to the Teams from the label" on: what a
// change of configuration may add to the reach of the open Alert Groups (C-26.FR-6). Every other entry was there
// before the save, and the reach did not grow by it then — after a fall of the level, say — so it does not now either
// (C-26.FR-4).
func touchedBy(c routing.MembershipChange) func(dest int64, e routing.Reaching) bool {
	route := map[int64]bool{}
	for _, a := range c.After {
		if i := slices.IndexFunc(c.Before, func(b routing.Entry) bool { return b.DestinationID == a.DestinationID }); i < 0 ||
			orAny(c.Before[i].Condition) != orAny(a.Condition) {
			route[a.DestinationID] = true
		}
	}
	teamsOn := !c.ToTeamsBefore && c.ToTeamsAfter
	return func(dest int64, e routing.Reaching) bool {
		if e.Kind == routing.EntryTeam {
			return teamsOn
		}
		return route[dest]
	}
}

// gain publishes the open Alert Group g Quietly in each Destination of its reach reached that an entry the save
// touched reaches (touched) and that has no delivery of it kept current there, with the entries that reach it (join),
// held by the Storm heldBy when one holds g; an outgoing
// webhook in the events mode publishes nothing, it receives the events from now on. g is rendered once for the
// markups of those Destinations, through tx; the counter and log lines of a Route template that fails here are left to
// the next render through the dispatcher, which records them in the Timeline too. It returns the Destinations it
// published in.
func (s *Service) gain(ctx context.Context, tx dbgen.DBTX, q queries, g GroupView, reached []routing.Reached,
	touched func(dest int64, e routing.Reaching) bool, had []entryDelivery, n naming, heldBy *int64,
	now time.Time) ([]stormDest, error) {
	var dests []dbgen.ListRouteDestinationsRow
	var entries [][]byte
	for _, rc := range reached {
		d, ok := n.dests[rc.DestinationID]
		if !ok || !hasRoot(d) || !slices.ContainsFunc(rc.Entries, func(e routing.Reaching) bool {
			return touched(rc.DestinationID, e)
		}) {
			continue
		}
		if i := slices.IndexFunc(had, func(x entryDelivery) bool { return x.DestinationID == d.ID }); i >= 0 &&
			!had[i].revivable() {
			continue
		}
		b, _ := json.Marshal(rc.Entries) // a slice of fixed structs always encodes
		dests, entries = append(dests, d), append(entries, b)
	}
	if len(dests) == 0 {
		return nil, nil
	}
	types := make([]string, len(dests))
	for i, d := range dests {
		types[i] = d.Type
	}
	roots, err := s.renderer.Roots(ctx, tx, g, markupsOf(types))
	if err != nil {
		return nil, fmt.Errorf("render alert group #%d: %w", g.Number, err)
	}
	out := make([]stormDest, len(dests))
	for i, d := range dests {
		dst := Destination{ID: d.ID, PublicID: d.PublicID, Name: d.Name, Type: d.Type, Connection: int8Of(d.ConnectionID),
			Buttons: buttonSetOf(d.Buttons, d.OrganizationButtons)}
		if err := s.join(ctx, tx, q, g, dst, entries[i], roots, heldBy, now); err != nil {
			return nil, err
		}
		out[i] = stormDest{id: d.ID, publicID: d.PublicID, typ: d.Type}
	}
	return out, nil
}

// FinalEditsFor gives, in the transaction tx that re-rendered the open Alert Group g after a change of the Destination
// entries of its Team team or the Team's deletion (groups.FinalEdits, C-26.FR-6, C-25.FR-10), the final edit to each
// delivery of g whose entries hold an entry of the Team when no entry still names its Destination — an entry of its
// Route or, while the Route delivers to the Teams, of one of its Teams, met or not; a delivery whose Destination one
// still names stays, with those entries appended (settle). The re-render step took the Route's membership lock and the
// Teams' before, and the Route's Storm; a Destination the Route does not name has the summary of an active Storm
// retired once nothing of the Route is kept current there. It returns how many deliveries got the final edit, none
// the second time, and wakes the delivery workers once tx commits when there are some.
func (s *Service) FinalEditsFor(ctx context.Context, tx groups.DBTX, g *groups.Group, team int64) (int, error) {
	q := s.store.queries(tx)
	had, err := s.entryDeliveries(ctx, q, []int64{g.ID})
	if err != nil {
		return 0, err
	}
	var cands []entryDelivery
	for _, d := range had[g.ID] {
		if d.live() && d.holds(routing.EntryTeam, team) {
			cands = append(cands, d)
		}
	}
	if len(cands) == 0 {
		return 0, nil
	}
	route, err := q.GetRouteDelivery(ctx, dbgen.GetRouteDeliveryParams{OrgID: s.orgID, ID: g.RouteID})
	if err != nil {
		return 0, fmt.Errorf("read the route of alert group #%d: %w", g.Number, err)
	}
	teams, err := q.ListGroupTeams(ctx, dbgen.ListGroupTeamsParams{OrgID: s.orgID, AlertGroupID: g.ID})
	if err != nil {
		return 0, fmt.Errorf("read the teams of alert group #%d: %w", g.Number, err)
	}
	n, err := s.namingOf(ctx, q, g.RouteID, route.DeliverToTeams, teams)
	if err != nil {
		return 0, err
	}
	now := s.clock.Now().UTC()
	left, err := s.settle(ctx, q, g.PublicID, cands, n, teams, now)
	if err != nil || len(left) == 0 {
		return 0, err
	}
	if err := s.retireSummaries(ctx, q, g.RouteID, left, n, now); err != nil {
		return 0, err
	}
	return len(left), s.wake(ctx, q, true)
}

// join makes the delivery of an open Alert Group current in a Destination its reach gained by a change of
// configuration: a Quiet Publication, held by the Storm heldBy when one holds the Alert Group, or an edit of a Root
// message whose final edit still waited; the entries that reach it are appended to those of the delivery. A change of
// configuration publishes Quietly (ADR-0018), even a delivery Not delivered before its first Publication.
func (s *Service) join(ctx context.Context, tx dbgen.DBTX, q queries, g GroupView, d Destination, entries []byte,
	roots Roots, heldBy *int64, now time.Time) error {
	groupID := g.ID
	loud := deliveryEvent(DeliveryAddedDestination).loud(g.Status == groups.StatusFiring)
	row, err := q.EnsureDelivery(ctx, dbgen.EnsureDeliveryParams{OrgID: s.orgID, DestinationID: d.ID,
		AlertGroupID: pgtype.Int8{Int64: groupID, Valid: true}, HeldByStormID: nullInt(heldBy), Urgent: g.Urgent,
		PublicationLoud: pgtype.Bool{Bool: loud, Valid: true}, Entries: entries, Now: now})
	if err != nil {
		return fmt.Errorf("create the delivery of alert group #%d to %s: %w", g.Number, d.PublicID, err)
	}
	if !row.Inserted {
		if _, err := q.RejoinDelivery(ctx, dbgen.RejoinDeliveryParams{OrgID: s.orgID, AlertGroupID: groupID,
			DestinationID: d.ID, Loud: loud, HeldByStormID: nullInt(heldBy), Now: now}); err != nil {
			return fmt.Errorf("publish alert group #%d in %s again: %w", g.Number, d.PublicID, err)
		}
	}
	msg, err := s.desiredOf(ctx, tx, d, roots)
	if err != nil {
		return fmt.Errorf("render the request of alert group #%d in %s: %w", g.Number, d.PublicID, err)
	}
	if bytes.Equal(row.DesiredHash, msg.hash) {
		return nil
	}
	if _, err := q.SetDesired(ctx, dbgen.SetDesiredParams{OrgID: s.orgID, ID: row.ID, DesiredText: msg.text,
		DesiredPayload: msg.payload, DesiredHash: msg.hash, ButtonKeyID: nonEmpty(msg.keyID), Open: true,
		Firing: loud, Urgent: g.Urgent, Quiet: true, Now: now}); err != nil {
		return fmt.Errorf("set the desired state of alert group #%d in %s: %w", g.Number, d.PublicID, err)
	}
	return nil
}

// leave gives the deliveries of a moved Alert Group in the Destinations other than dests, those of its new Route, the
// final edit (C-09.FR-19), and returns how many it changed.
func (s *Service) leave(ctx context.Context, q queries, g *groups.Group, dests []dbgen.ListRouteDestinationsRow,
	now time.Time) (int, error) {
	keep := make([]int64, 0, len(dests))
	for _, d := range dests {
		keep = append(keep, d.ID)
	}
	ids, err := q.RetireGroupDeliveries(ctx, dbgen.RetireGroupDeliveriesParams{OrgID: s.orgID, AlertGroupID: g.ID,
		Keep: keep, Now: now})
	if err != nil {
		return 0, fmt.Errorf("retire the deliveries of the moved alert group #%d: %w", g.Number, err)
	}
	return len(ids), s.dropReplies(ctx, q, ids)
}

// dropReplies drops the Thread replies still to be sent of deliveries that get their final edit.
func (s *Service) dropReplies(ctx context.Context, q queries, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if err := q.DropDeliveryReplies(ctx, dbgen.DropDeliveryRepliesParams{OrgID: s.orgID,
		DeliveryIds: ids}); err != nil {
		return fmt.Errorf("drop the thread replies of the retired deliveries: %w", err)
	}
	return nil
}

// DestinationRenamed is the rename hook of the Destinations, in the transaction tx that renamed the Destination
// publicID to name: the Internal alerts about it, MusterDestinationBroken among them, take the new name as the same
// firing.
func (s *Service) DestinationRenamed(ctx context.Context, tx dbgen.DBTX, publicID, name string) error {
	return s.internal.Renamed(ctx, s.store.queries(tx), s.clock.Now().UTC(), internalalerts.EntityDestination,
		publicID, name)
}

// RetireDestination is the deletion hook of the Destinations (C-11.FR-14), in the transaction tx that deleted the
// Destination destinationID: the open Root messages there get the final edit, its other pending deliveries end without
// a call — retired when published, withheld otherwise — its pending Thread replies are dropped, its waiting outgoing
// webhook events end as Not delivered with no final event sent (C-15.FR-12), MusterDestinationBroken about it is
// resolved, and its secrets are wiped at once when nothing of it is pending and no call of it is in flight. It wakes
// the delivery workers once tx commits.
func (s *Service) RetireDestination(ctx context.Context, tx dbgen.DBTX, destinationID int64) error {
	q := s.store.queries(tx)
	now := s.clock.Now().UTC()
	st, err := q.GetDestinationState(ctx, dbgen.GetDestinationStateParams{OrgID: s.orgID, ID: destinationID})
	if err != nil {
		return fmt.Errorf("read the destination %d: %w", destinationID, err)
	}
	if _, err := q.RetireDestinationDeliveries(ctx, dbgen.RetireDestinationDeliveriesParams{OrgID: s.orgID,
		DestinationID: destinationID, Now: now}); err != nil {
		return fmt.Errorf("retire the deliveries of destination %s: %w", st.PublicID, err)
	}
	if err := q.SettleDestinationLeftovers(ctx, dbgen.SettleDestinationLeftoversParams{OrgID: s.orgID,
		DestinationID: destinationID, Now: now}); err != nil {
		return fmt.Errorf("end the other deliveries of destination %s: %w", st.PublicID, err)
	}
	if err := q.DropDestinationReplies(ctx, dbgen.DropDestinationRepliesParams{OrgID: s.orgID,
		DestinationID: destinationID}); err != nil {
		return fmt.Errorf("drop the thread replies of destination %s: %w", st.PublicID, err)
	}
	if st.Health == healthBroken {
		if err := s.internal.Resolve(ctx, q, now, internalalerts.DestinationBroken, st.PublicID); err != nil {
			return fmt.Errorf("resolve MusterDestinationBroken of %s: %w", st.PublicID, err)
		}
	}
	if st.TemplateErrorSet {
		if err := s.internal.ResolveFor(ctx, q, now, internalalerts.TemplateError, internalalerts.EntityDestination,
			st.PublicID); err != nil {
			return fmt.Errorf("resolve MusterTemplateError of %s: %w", st.PublicID, err)
		}
	}
	if err := s.abandonEvents(ctx, q, destinationID, now); err != nil {
		return err
	}
	if err := wipeSecrets(ctx, q, s.orgID, destinationID, s.real.Now().UTC()); err != nil {
		return err
	}
	return s.wake(ctx, q, true)
}

// wipeSecrets wipes the secrets of the deleted Destination id once none of its deliveries is pending and no call of
// it holds a lease at the real time now (C-11.FR-14, C-15.FR-12): a call in flight still makes its final edit or signs
// its request with them, and the worker that records the last one wipes them. It changes nothing otherwise, and
// nothing the second time.
func wipeSecrets(ctx context.Context, q queries, org, id int64, now time.Time) error {
	if err := q.WipeDestinationSecrets(ctx, dbgen.WipeDestinationSecretsParams{OrgID: org,
		ID: pgtype.Int8{Int64: id, Valid: true}, Now: now}); err != nil {
		return fmt.Errorf("wipe the secrets of destination %d: %w", id, err)
	}
	return nil
}

// AbandonConnection ends, in the transaction tx that deletes the Connection connectionID, the final edits still pending
// in its deleted Destinations as Not delivered with the error "the Connection was deleted", each with its not_delivered
// delivery event, drops their pending Thread replies and wipes their secrets (C-11.FR-14). The caller runs the
// returned function once tx committed: it logs delivery_not_delivered for each delivery it ended. deleteConnection
// calls it from S-039.
func (s *Service) AbandonConnection(ctx context.Context, tx dbgen.DBTX, connectionID int64) (func(context.Context),
	error) {
	q := s.store.queries(tx)
	now := s.clock.Now().UTC()
	conn := pgtype.Int8{Int64: connectionID, Valid: true}
	rows, err := q.AbandonConnectionDeliveries(ctx, dbgen.AbandonConnectionDeliveriesParams{OrgID: s.orgID,
		ConnectionID: conn, Error: connectionDeleted, Now: now})
	if err != nil {
		return nil, fmt.Errorf("abandon the deliveries of connection %d: %w", connectionID, err)
	}
	var logs after
	for _, r := range rows {
		if err := RecordEvent(ctx, q, s.orgID, Event{At: now, DestinationID: r.DestinationID,
			AlertGroupID: int8Of(r.AlertGroupID), StormID: int8Of(r.StormID), Kind: EventNotDelivered,
			ErrorClass: string(OutcomeUnknown), Error: outbound.Untrusted(connectionDeleted)}); err != nil {
			return nil, err
		}
		logs.add(notDeliveredLine(r.DestinationPublicID, r.AlertGroupPublicID, abandonedKind(r), string(OutcomeUnknown)))
	}
	dests, err := q.ListDeletedDestinationsOfConnection(ctx, dbgen.ListDeletedDestinationsOfConnectionParams{
		OrgID: s.orgID, ConnectionID: conn})
	if err != nil {
		return nil, fmt.Errorf("list the deleted destinations of connection %d: %w", connectionID, err)
	}
	for _, d := range dests {
		if err := q.DropDestinationReplies(ctx, dbgen.DropDestinationRepliesParams{OrgID: s.orgID,
			DestinationID: d}); err != nil {
			return nil, fmt.Errorf("drop the thread replies of destination %d: %w", d, err)
		}
		if err := wipeSecrets(ctx, q, s.orgID, d, s.real.Now().UTC()); err != nil {
			return nil, err
		}
	}
	return func(ctx context.Context) { logs.run(ctx, s.log) }, nil
}

// abandonedKind is the kind of the call an abandoned delivery waited for: its final edit, a call of a Storm summary, a
// Publication or an edit.
func abandonedKind(r dbgen.AbandonConnectionDeliveriesRow) string {
	switch {
	case r.FinalEdit:
		return kindFinalEdit
	case r.StormID.Valid:
		return kindStormSummary
	case r.Unpublished:
		return kindPublication
	}
	return kindUpdate
}
