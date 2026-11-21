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
)

// RefreshButtons has the Root message of the open Alert Group groupID in the Destination destinationID edited again,
// Quietly, as it is rendered now, and reports whether it changed the delivery. The Mattermost callback calls it for a
// press without a callback secret on a Root message bound to it: a Root message laid out before buttons carried the
// secret keeps the buttons it was posted with until it is edited, and the adapter lays the edit out with the secret
// (C-13.FR-4). The adapter writes the secret when it sends, so the rendering's hash does not change: the stored
// Desired state is made due again with the repair hash of its next version, as a repair of a stored footer is. A
// second call before any other change of the Desired state finds that repair hash and changes nothing, so presses
// without a secret edit a Root message at most once per version. A stored state that differs from the rendering, as
// after a rotation, is replaced by it. Nothing changes when the Alert Group resolved or a Command holds it, whose own
// re-render edits the Root message, or when the delivery has no Root message to edit or no buttons.
//
// It locks in the order of the re-render after a rotation (rerenderGroup): the Alert Group, its Route's membership
// lock, then its deliveries with buttons, of which it changes the one of destinationID only.
func (s *Service) RefreshButtons(ctx context.Context, groupID, destinationID int64) (bool, error) {
	changed := false
	var settled func(context.Context)
	err := s.store.inTxWith(ctx, func(tx dbgen.DBTX, q queries) error {
		changed, settled = false, nil
		g, err := q.LockRerenderGroup(ctx, dbgen.LockRerenderGroupParams{OrgID: s.orgID, ID: groupID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock the alert group %d: %w", groupID, err)
		}
		if err := q.ShareRouteMembership(ctx, dbgen.ShareRouteMembershipParams{
			LockClass: db.RouteMembershipLockClass, RouteID: g.RouteID}); err != nil {
			return fmt.Errorf("lock the destinations of the route of alert group #%d: %w", g.Number, err)
		}
		// No key is the empty key id: every delivery with buttons and a Root message to edit is selected.
		rows, err := q.LockRerenderDeliveries(ctx, dbgen.LockRerenderDeliveriesParams{OrgID: s.orgID,
			AlertGroupID: g.ID, ActiveKeyID: ""})
		if err != nil {
			return fmt.Errorf("lock the deliveries of alert group #%d: %w", g.Number, err)
		}
		var row *dbgen.LockRerenderDeliveriesRow
		for i := range rows {
			if rows[i].DestinationID == destinationID {
				row = &rows[i]
			}
		}
		if row == nil {
			return nil
		}
		status := groups.Status(g.Status)
		view := GroupView{ID: g.ID, PublicID: g.PublicID, Number: g.Number, Title: g.Title, Status: status,
			Urgent: g.Urgent}
		roots, err := s.renderer.Roots(ctx, tx, view, markupsOf([]string{row.DestinationType}))
		if err != nil {
			return fmt.Errorf("render alert group #%d: %w", g.Number, err)
		}
		settled = roots.After
		d := Destination{ID: row.DestinationID, PublicID: row.DestinationPublicID, Name: row.DestinationName,
			Type: row.DestinationType, Connection: int8Of(row.ConnectionID),
			Buttons: buttonSetOf(row.Buttons, row.OrganizationButtons)}
		msg, err := s.desiredOf(ctx, tx, d, roots)
		if err != nil {
			return fmt.Errorf("render alert group #%d in %s: %w", g.Number, d.PublicID, err)
		}
		if row.DesiredButtonKeyID.String == msg.keyID {
			if bytes.Equal(row.DesiredHash, repairHash(msg.hash, row.DesiredVersion)) {
				return nil
			}
			if bytes.Equal(row.DesiredHash, msg.hash) {
				msg.hash = repairHash(msg.hash, row.DesiredVersion+1)
			}
		}
		if _, err := q.SetDesired(ctx, dbgen.SetDesiredParams{OrgID: s.orgID, ID: row.ID, DesiredText: msg.text,
			DesiredPayload: msg.payload, DesiredHash: msg.hash, ButtonKeyID: nonEmpty(msg.keyID), Open: true,
			Firing: deliveryEvent(DeliveryAfterRecovery).loud(status == groups.StatusFiring), Urgent: g.Urgent,
			Quiet: true, ConfigurationOnly: true, Now: s.clock.Now().UTC()}); err != nil {
			return fmt.Errorf("set the desired state of alert group #%d in %s: %w", g.Number, d.PublicID, err)
		}
		changed = true
		return s.wake(ctx, q, true)
	})
	if err != nil {
		return false, err
	}
	if settled != nil {
		settled(ctx)
	}
	return changed, nil
}
