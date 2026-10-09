// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/internalalerts"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// Destinations joining and leaving Routes (C-11.FR-14, C-09.FR-19): a Destination added to a Route publishes the
// Route's open Alert Groups there Quietly; one removed from it, or deleted, gives each of their open Root messages one
// final Quiet edit — "No longer updated here; current state in Muster: {link}" — after which the delivery is retired
// and receives nothing more, and one never published there is withheld. The secrets of a deleted Destination are wiped
// once none of its deliveries is pending.

// connectionDeleted is the error of the final edits a deleted Connection abandons.
const connectionDeleted = "the Connection was deleted"

// membershipQueries are the queries of Destinations joining and leaving Routes.
type membershipQueries interface {
	ListOpenGroupsOfRoute(ctx context.Context, arg dbgen.ListOpenGroupsOfRouteParams) (
		[]dbgen.ListOpenGroupsOfRouteRow, error)
	RejoinDelivery(ctx context.Context, arg dbgen.RejoinDeliveryParams) (int64, error)
	RetireRouteDeliveries(ctx context.Context, arg dbgen.RetireRouteDeliveriesParams) (
		[]dbgen.RetireRouteDeliveriesRow, error)
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

// RouteDestinationsChanged is the membership hook of the Routes (C-11.FR-14), in the transaction tx of the Route's
// change, which holds the Route's membership lock: the Destinations added to the Route routeID publish its open Alert
// Groups Quietly — during a Storm they get its Storm summary and hold the Alert Groups it holds — and those removed
// give their open Root messages the final edit and retire the summary of an active Storm, with the alert-group hint of
// each Alert Group whose delivery that retires or withholds. It wakes the delivery workers once tx commits.
func (s *Service) RouteDestinationsChanged(ctx context.Context, tx dbgen.DBTX, routeID int64, added,
	removed []int64) error {
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	q := s.store.queries(tx)
	now := s.clock.Now().UTC()
	for _, d := range removed {
		rows, err := q.RetireRouteDeliveries(ctx, dbgen.RetireRouteDeliveriesParams{OrgID: s.orgID, RouteID: routeID,
			DestinationID: d, Now: now})
		if err != nil {
			return fmt.Errorf("retire the deliveries of route %d in destination %d: %w", routeID, d, err)
		}
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
			if err := hintGroup(ctx, q, s.orgID, r.PublicID); err != nil {
				return err
			}
		}
		if err := s.dropReplies(ctx, q, ids); err != nil {
			return err
		}
		if err := q.RetireStormSummary(ctx, dbgen.RetireStormSummaryParams{OrgID: s.orgID, RouteID: routeID,
			DestinationID: d, Now: now}); err != nil {
			return fmt.Errorf("retire the storm summary of route %d in destination %d: %w", routeID, d, err)
		}
	}
	if len(added) > 0 {
		if err := s.publishRoute(ctx, tx, q, routeID, added, now); err != nil {
			return err
		}
	}
	return s.wake(ctx, q, true)
}

// publishRoute publishes the open Alert Groups of the Route routeID Quietly in the Destinations added to it; during a
// Storm of the Route each of them gets its Storm summary, and the Alert Groups the Storm holds are held there too.
// Each Alert Group is rendered once for the markups of the added Destinations, through tx; the counter and log lines
// of a Route template that fails here are left to the next render through the dispatcher, which records them in the
// Timeline too.
func (s *Service) publishRoute(ctx context.Context, tx dbgen.DBTX, q queries, routeID int64, added []int64,
	now time.Time) error {
	var st *storm
	active, err := q.GetActiveStorm(ctx, dbgen.GetActiveStormParams{OrgID: s.orgID, RouteID: routeID})
	switch {
	case err == nil:
		st = &storm{id: active.ID, started: active.StartedAt, count: active.AlertGroupCount,
			urgent: active.UrgentCount}
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("read the storm of route %d: %w", routeID, err)
	}
	p := dbgen.ListOpenGroupsOfRouteParams{OrgID: s.orgID, RouteID: routeID}
	if st != nil {
		p.StormID = pgtype.Int8{Int64: st.id, Valid: true}
		p.StormStarted = pgtype.Timestamptz{Time: st.started, Valid: true}
	}
	open, err := q.ListOpenGroupsOfRoute(ctx, p)
	if err != nil {
		return fmt.Errorf("list the open alert groups of route %d: %w", routeID, err)
	}
	if len(open) == 0 && st == nil {
		return nil
	}
	dests, err := q.ListRouteDestinations(ctx, dbgen.ListRouteDestinationsParams{OrgID: s.orgID, RouteID: routeID})
	if err != nil {
		return fmt.Errorf("list the destinations of route %d: %w", routeID, err)
	}
	route, err := q.GetRouteDelivery(ctx, dbgen.GetRouteDeliveryParams{OrgID: s.orgID, ID: routeID})
	if err != nil {
		return fmt.Errorf("read the route %d: %w", routeID, err)
	}
	dests = slices.DeleteFunc(dests, func(d dbgen.ListRouteDestinationsRow) bool { return !slices.Contains(added, d.ID) })
	types := make([]string, len(dests))
	for i, d := range dests {
		types[i] = d.Type
	}
	roots := make([]Roots, len(open))
	for i, g := range open {
		view := GroupView{ID: g.ID, PublicID: g.PublicID, Number: g.Number, Title: g.Title,
			Status: groups.Status(g.Status), Urgent: g.Urgent}
		if roots[i], err = s.renderer.Roots(ctx, tx, view, markupsOf(types)); err != nil {
			return fmt.Errorf("render alert group #%d: %w", g.Number, err)
		}
	}
	for _, d := range dests {
		dst := Destination{ID: d.ID, PublicID: d.PublicID, Name: d.Name, Type: d.Type, Connection: int8Of(d.ConnectionID)}
		for i, g := range open {
			view := GroupView{ID: g.ID, PublicID: g.PublicID, Number: g.Number, Title: g.Title,
				Status: groups.Status(g.Status), Urgent: g.Urgent}
			var heldBy *int64
			if g.Held {
				heldBy = &st.id
			}
			if err := s.join(ctx, q, view, dst, roots[i].Messages[markupOf(dst.Type)], heldBy, now); err != nil {
				return err
			}
		}
	}
	if st != nil {
		return s.renderSummaries(ctx, q, st, stormRoute{publicID: route.PublicID, name: route.Name,
			language: route.Language}, dests, now)
	}
	return nil
}

// join makes the delivery of an open Alert Group current in a Destination that joined its Route: a Quiet
// Publication, held by the Storm heldBy when one holds the Alert Group, or an edit of a Root message whose final edit
// still waited.
func (s *Service) join(ctx context.Context, q queries, g GroupView, d Destination, rd messages.Rendered,
	heldBy *int64, now time.Time) error {
	groupID := g.ID
	loud := deliveryEvent(DeliveryAddedDestination).loud(g.Status == groups.StatusFiring)
	row, err := q.EnsureDelivery(ctx, dbgen.EnsureDeliveryParams{OrgID: s.orgID, DestinationID: d.ID,
		AlertGroupID: pgtype.Int8{Int64: groupID, Valid: true}, HeldByStormID: nullInt(heldBy), Urgent: g.Urgent,
		PublicationLoud: pgtype.Bool{Bool: loud, Valid: true}, Now: now})
	if err != nil {
		return fmt.Errorf("create the delivery of alert group #%d to %s: %w", g.Number, d.PublicID, err)
	}
	if !row.Inserted {
		if _, err := q.RejoinDelivery(ctx, dbgen.RejoinDeliveryParams{OrgID: s.orgID, AlertGroupID: groupID,
			DestinationID: d.ID, Loud: loud, HeldByStormID: nullInt(heldBy), Now: now}); err != nil {
			return fmt.Errorf("publish alert group #%d in %s again: %w", g.Number, d.PublicID, err)
		}
	}
	msg := encodeRoot(rd)
	if bytes.Equal(row.DesiredHash, msg.hash) {
		return nil
	}
	if _, err := q.SetDesired(ctx, dbgen.SetDesiredParams{OrgID: s.orgID, ID: row.ID, DesiredText: msg.text,
		DesiredPayload: msg.payload, DesiredHash: msg.hash, ButtonKeyID: nonEmpty(msg.keyID), Open: true,
		Firing: loud, Urgent: g.Urgent,
		Now: now}); err != nil {
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

// RetireDestination is the deletion hook of the Destinations (C-11.FR-14), in the transaction tx that deleted the
// Destination destinationID: the open Root messages there get the final edit, its other pending deliveries end without
// a call — retired when published, withheld otherwise — its pending Thread replies are dropped, MusterDestinationBroken
// about it is resolved, and its secrets are wiped at once when nothing of it is pending. It wakes the delivery workers
// once tx commits.
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
	if err := wipeSecrets(ctx, q, s.orgID, destinationID); err != nil {
		return err
	}
	return s.wake(ctx, q, true)
}

// wipeSecrets wipes the secrets of the deleted Destination id once none of its deliveries is pending; it changes
// nothing otherwise, and nothing the second time.
func wipeSecrets(ctx context.Context, q queries, org, id int64) error {
	if err := q.WipeDestinationSecrets(ctx, dbgen.WipeDestinationSecretsParams{OrgID: org, ID: id}); err != nil {
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
		logs.add(notDeliveredLine(r.DestinationPublicID, r.AlertGroupPublicID, abandonedKind(r)))
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
		if err := wipeSecrets(ctx, q, s.orgID, d); err != nil {
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
