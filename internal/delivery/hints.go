// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"fmt"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/live"
)

// The alert-group hints of delivery (C-09.FR-25, C-11.FR-16): a change of a delivery that the Alert Group page or the
// Delivery problem mark of the list shows — delivered, Not delivered, retired, withheld (also when a Storm ends),
// deleted in the messenger, published again, a possible duplicate, the final edit or withholding after its Destination
// left the Route, or waiting for a Broken Destination and back — sends the alert-group hint of its Alert Group in the
// transaction that records it, as the dispatcher does for its changes: the hint reaches the streams once that
// transaction commits, and never when it rolls back. A Storm summary has no Alert Group and sends none.

// hintGroup sends the alert-group hint of the Alert Group group, a public_id; empty for a Storm summary.
func hintGroup(ctx context.Context, q queries, org int64, group string) error {
	if group == "" {
		return nil
	}
	return q.Notify(ctx, db.Hint{OrgID: org, Type: live.HintAlertGroup, ID: group})
}

// hintPendingGroups sends the alert-group hint of every Alert Group with a pending delivery to the Destination
// destination, whose state a change of the Destination's health shows as waiting for it, or no longer.
func hintPendingGroups(ctx context.Context, q queries, org, destination int64) error {
	groups, err := q.ListPendingGroups(ctx, dbgen.ListPendingGroupsParams{OrgID: org, DestinationID: destination})
	if err != nil {
		return fmt.Errorf("list the alert groups waiting for destination %d: %w", destination, err)
	}
	for _, g := range groups {
		if err := hintGroup(ctx, q, org, g); err != nil {
			return err
		}
	}
	return nil
}
