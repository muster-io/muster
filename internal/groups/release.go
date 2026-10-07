// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/muster-io/muster/internal/groups/dbgen"
	usersdb "github.com/muster-io/muster/internal/users/dbgen"
)

// The reasons of the release of an Owner who was disabled or deleted (C-03.FR-13, C-09.FR-22).
const (
	ReasonOwnerDisabled = "owner_disabled"
	ReasonOwnerDeleted  = "owner_deleted"
)

// release frees the Alert Groups of a disabled or deleted User: an acknowledged one they own becomes firing without an
// Owner, with a Loud unacknowledged that names them as the previous Owner and gives the reason; a system-resolved one
// that would reopen acknowledged by them inside its Reopen window will reopen into firing instead, without a Timeline
// entry. An Alert Group they no longer own changes nothing.
type release struct {
	user   int64
	reason string
}

func (release) precondition(*Group) error { return nil }

func (t release) apply(_ context.Context, _ Queries, g *Group, c *change) error {
	switch {
	case g.Status == StatusAcknowledged && g.OwnerUserID != nil && *g.OwnerUserID == t.user:
		previous := g.OwnerUserID
		g.Status, g.OwnerUserID, g.AcknowledgedAt = StatusFiring, nil, nil
		c.reason = t.reason
		c.add(entry{Event: EventUnacknowledged, Variant: VariantOwnerReleased, Reason: t.reason,
			From: StatusAcknowledged, To: StatusFiring, PreviousOwner: previous})
	case g.Status == StatusResolved && g.Prior != nil && g.Prior.Status == StatusAcknowledged &&
		g.Prior.OwnerUserID != nil && *g.Prior.OwnerUserID == t.user:
		g.Prior.Status, g.Prior.OwnerUserID = StatusFiring, nil
		c.written = true
	}
	return nil
}

// ReleaseOwner releases the acknowledgements of the User userID, whom an Admin disabled (ReasonOwnerDisabled) or
// deleted (ReasonOwnerDeleted), in the transaction tx of that change (C-03.FR-13): each Alert Group they own goes
// through the dispatcher as a system transition — actor and Transport system, no Permission and no Audit log entry of
// its own, since the user's own entry records the cause. It locks as grouping does: the counter row, then the Alert
// Groups in id order. It returns what to run once tx committed.
func (s *Service) ReleaseOwner(ctx context.Context, tx usersdb.DBTX, userID int64, reason string) (
	func(context.Context), error) {
	if reason != ReasonOwnerDisabled && reason != ReasonOwnerDeleted {
		return nil, fmt.Errorf("release the owner: unknown reason %q", reason)
	}
	q := s.queries(tx)
	ids, err := q.ListOwnedGroups(ctx, dbgen.ListOwnedGroupsParams{OrgID: s.orgID, UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list the alert groups of user %d: %w", userID, err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := q.EnsureCounter(ctx, s.orgID); err != nil {
		return nil, fmt.Errorf("create the alert group counter: %w", err)
	}
	if _, err := q.LockCounter(ctx, s.orgID); err != nil {
		return nil, fmt.Errorf("lock the alert group counter: %w", err)
	}
	locked, err := lock(ctx, q, s.orgID, ids)
	if err != nil {
		return nil, err
	}
	var after committed
	for _, id := range slices.Sorted(maps.Keys(locked)) {
		if err := s.d.dispatch(ctx, q, locked[id], System, release{user: userID, reason: reason}, &after); err != nil {
			return nil, err
		}
	}
	return after.run, nil
}
