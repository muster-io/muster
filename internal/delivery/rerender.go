// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/live"
)

// RerenderBatch is how many Alert Groups one batch of the re-render after a key rotation lists.
const RerenderBatch = 50

// ErrButtonKeyNotActive is the re-render's error when the renderer signed the buttons with another key than the
// active key it was given: this replica's Keyring has not switched to that key, or switched away from it meanwhile.
var ErrButtonKeyNotActive = errors.New("the buttons are not signed with the active key")

// rerenderQueries are the queries of the re-render after a key rotation and of the last re-signed message.
type rerenderQueries interface {
	ListRerenderGroups(ctx context.Context, arg dbgen.ListRerenderGroupsParams) ([]int64, error)
	LockRerenderGroup(ctx context.Context, arg dbgen.LockRerenderGroupParams) (dbgen.LockRerenderGroupRow, error)
	LockRerenderDeliveries(ctx context.Context, arg dbgen.LockRerenderDeliveriesParams) (
		[]dbgen.LockRerenderDeliveriesRow, error)
	LockButtonKey(ctx context.Context, arg dbgen.LockButtonKeyParams) error
	ButtonKeyInUse(ctx context.Context, arg dbgen.ButtonKeyInUseParams) (bool, error)
}

// RerenderOpen is the work of the Leader task rerender_after_rotation (C-20.FR-7, AC-5; ADR-0007, ADR-0011): in
// batches, it recomputes the Desired state of every delivery of an open Alert Group whose buttons a key other than
// activeKeyID signed and that has a Root message to edit or still to publish, so that the renderer, whose Keyring has
// switched to activeKeyID, signs them with it and the delivery worker edits the Root messages within the limiters. It
// sends the organization hint after each batch that changed a delivery and returns how many it changed.
//
// Each Alert Group is one transaction in the dispatcher's lock order (worker.go): the Alert Group first, FOR NO KEY
// UPDATE SKIP LOCKED, then its Route's membership lock shared, then its deliveries in the order Enqueue locks them; it
// never locks a delivery before its Alert Group, and never a Storm. An Alert Group a Command holds is skipped: that
// Command's own re-render signs with the active key, and a delivery a replica signed with the old key before it
// switched is selected again by the next run. A delivery a Storm holds is re-rendered and stays held, as Enqueue
// renders it: the Storm's release publishes the stored Desired state, now signed with the active key. A second run
// finds nothing to change, and two overlapping Leaders lock each Alert Group in turn.
//
// An Alert Group that fails is left for the next run and the others go on; the errors are joined and returned at the
// end. The run stops at once when ctx ends, when a list or a hint fails, or with ErrButtonKeyNotActive, which every
// other Alert Group would meet too.
func (s *Service) RerenderOpen(ctx context.Context, activeKeyID string) (int, error) {
	if activeKeyID == "" {
		return 0, errors.New("re-render after a rotation: no active key")
	}
	total := 0
	var errs []error
	var after int64
	for {
		ids, err := s.store.q().ListRerenderGroups(ctx, dbgen.ListRerenderGroupsParams{OrgID: s.orgID, After: after,
			ActiveKeyID: activeKeyID, Lim: RerenderBatch})
		if err != nil {
			return total, errors.Join(append(errs, fmt.Errorf("list the alert groups to re-render: %w", err))...)
		}
		changed := 0
		var stop bool
		for _, id := range ids {
			n, err := s.rerenderGroup(ctx, id, activeKeyID)
			changed += n
			if err != nil {
				errs = append(errs, err)
				if stop = errors.Is(err, ErrButtonKeyNotActive) || ctx.Err() != nil; stop {
					break
				}
			}
		}
		total += changed
		if changed > 0 {
			if err := s.store.q().Notify(ctx, db.Hint{OrgID: s.orgID, Type: live.HintOrganization}); err != nil {
				return total, errors.Join(append(errs, err)...)
			}
		}
		if stop || len(ids) < RerenderBatch {
			return total, errors.Join(errs...)
		}
		after = ids[len(ids)-1]
	}
}

// rerenderGroup recomputes, in one transaction, the Desired state of the deliveries of the Alert Group id signed by
// another key than activeKeyID, and returns how many it changed; nothing when the Alert Group resolved or another
// transaction holds it.
func (s *Service) rerenderGroup(ctx context.Context, id int64, activeKeyID string) (int, error) {
	changed := 0
	var settled func(context.Context)
	err := s.store.inTxWith(ctx, func(tx dbgen.DBTX, q queries) error {
		changed, settled = 0, nil
		g, err := q.LockRerenderGroup(ctx, dbgen.LockRerenderGroupParams{OrgID: s.orgID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock the alert group %d: %w", id, err)
		}
		if err := q.ShareRouteMembership(ctx, dbgen.ShareRouteMembershipParams{
			LockClass: db.RouteMembershipLockClass, RouteID: g.RouteID}); err != nil {
			return fmt.Errorf("lock the destinations of the route of alert group #%d: %w", g.Number, err)
		}
		rows, err := q.LockRerenderDeliveries(ctx, dbgen.LockRerenderDeliveriesParams{OrgID: s.orgID,
			AlertGroupID: g.ID, ActiveKeyID: activeKeyID})
		if err != nil {
			return fmt.Errorf("lock the deliveries of alert group #%d: %w", g.Number, err)
		}
		if len(rows) == 0 {
			return nil
		}
		types := make([]string, len(rows))
		for i, r := range rows {
			types[i] = r.DestinationType
		}
		status := groups.Status(g.Status)
		view := GroupView{ID: g.ID, PublicID: g.PublicID, Number: g.Number, Title: g.Title, Status: status,
			Urgent: g.Urgent}
		roots, err := s.renderer.Roots(ctx, tx, view, markupsOf(types))
		if err != nil {
			return fmt.Errorf("render alert group #%d: %w", g.Number, err)
		}
		settled = roots.After
		now := s.clock.Now().UTC()
		for _, r := range rows {
			d := Destination{ID: r.DestinationID, PublicID: r.DestinationPublicID, Name: r.DestinationName,
				Type: r.DestinationType, Connection: int8Of(r.ConnectionID)}
			msg, err := s.desiredOf(ctx, tx, d, roots)
			if err != nil {
				return fmt.Errorf("render alert group #%d in %s: %w", g.Number, d.PublicID, err)
			}
			if msg.keyID != "" && msg.keyID != activeKeyID {
				return fmt.Errorf("render alert group #%d in %s: %w: signed with key %s, the active key is %s",
					g.Number, d.PublicID, ErrButtonKeyNotActive, msg.keyID, activeKeyID)
			}
			if bytes.Equal(r.DesiredHash, msg.hash) && r.DesiredButtonKeyID.String == msg.keyID {
				continue
			}
			if _, err := q.SetDesired(ctx, dbgen.SetDesiredParams{OrgID: s.orgID, ID: r.ID, DesiredText: msg.text,
				DesiredPayload: msg.payload, DesiredHash: msg.hash, ButtonKeyID: nonEmpty(msg.keyID), Open: true,
				Firing: deliveryEvent(DeliveryAfterRecovery).loud(status == groups.StatusFiring), Urgent: g.Urgent,
				Now: now}); err != nil {
				return fmt.Errorf("set the desired state of alert group #%d in %s: %w", g.Number, d.PublicID, err)
			}
			changed++
		}
		return s.wake(ctx, q, changed > 0)
	})
	if err != nil {
		return 0, err
	}
	if settled != nil {
		settled(ctx)
	}
	return changed, nil
}
