// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/live"
)

// hint sends, in the transaction of a change of g, the live-update hints of C-09.FR-25: alert-group with its
// public_id after every change, and alert-groups without an id when g is new or reopened, as it may now match a
// list. A hint carries no data, so a stream re-reads through the API; the hints of a transaction that rolls back are
// never sent.
func (d *dispatcher) hint(ctx context.Context, q Queries, g *Group, from Status) error {
	if err := q.Notify(ctx, db.Hint{OrgID: d.orgID, Type: live.HintAlertGroup, ID: g.PublicID}); err != nil {
		return err
	}
	if from == "" || (from == StatusResolved && g.Status != StatusResolved) {
		return q.Notify(ctx, db.Hint{OrgID: d.orgID, Type: live.HintAlertGroups})
	}
	return nil
}
