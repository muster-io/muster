// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// ActionOpenAlertGroupsMoved is the Audit log action of moveOpenAlertGroups.
const ActionOpenAlertGroupsMoved = "route.open_alert_groups_moved"

// Requester is who asks for a change through the API and how: the actor and the Transport of the Audit log entry,
// and the client address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// moveToDefault is the system transition of an open Alert Group whose Route is to be deleted (C-09.FR-19): it moves
// to the Default route and keeps its Group key values, but leaves the open-key index, so it takes no new Alerts and
// never reopens.
type moveToDefault struct {
	from, to int64
}

func (t moveToDefault) precondition(g *Group) error {
	if g.Status == StatusResolved || g.RouteID != t.from || g.MovedFromRouteID != nil {
		return errNotMoved
	}
	return nil
}

func (t moveToDefault) apply(_ context.Context, _ Queries, g *Group, c *change) error {
	g.RouteID, g.MovedFromRouteID = t.to, &t.from
	c.add(entry{Event: EventMovedToDefaultRoute, System: SystemMovedToDefaultRoute})
	return nil
}

// errNotMoved is an Alert Group that was resolved or moved since the list of the Route's open ones was read.
var errNotMoved = errors.New("the alert group is no longer an open alert group of the route")

// MoveOpenAlertGroups moves the open Alert Groups of the Route publicID to the Default route (C-09.FR-19), each with
// a moved_to_default_route entry, and records one Audit log entry with their count, which it returns. The Route is
// locked against grouping meanwhile, so no Alert Group of it opens while they move. An unknown or deleted Route is
// ErrRouteNotFound, the Default route ErrDefaultRoute.
func (s *Service) MoveOpenAlertGroups(ctx context.Context, r Requester, publicID string) (int, error) {
	id, err := publicid.Parse(publicid.Route, publicID)
	if err != nil {
		return 0, ErrRouteNotFound
	}
	var moved int
	var after committed
	err = s.store.InTx(ctx, func(q Queries) error {
		moved, after = 0, nil
		route, err := q.LockRouteForMove(ctx, dbgen.LockRouteForMoveParams{OrgID: s.orgID, PublicID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRouteNotFound
		}
		if err != nil {
			return fmt.Errorf("lock the route %s: %w", id, err)
		}
		if route.IsDefault {
			return ErrDefaultRoute
		}
		def, err := q.GetDefaultRouteID(ctx, s.orgID)
		if err != nil {
			return fmt.Errorf("read the default route: %w", err)
		}
		ids, err := q.ListOpenGroupsOfRoute(ctx, dbgen.ListOpenGroupsOfRouteParams{OrgID: s.orgID,
			RouteID: route.ID})
		if err != nil {
			return fmt.Errorf("list the open alert groups of the route %s: %w", id, err)
		}
		locked, err := lock(ctx, q, s.orgID, ids)
		if err != nil {
			return err
		}
		for _, gid := range ids {
			g := locked[gid]
			if g == nil {
				continue
			}
			err := s.d.dispatch(ctx, q, g, System, moveToDefault{from: route.ID, to: def}, &after)
			if errors.Is(err, errNotMoved) {
				continue
			}
			if err != nil {
				return err
			}
			moved++
		}
		return s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: r.Actor, Transport: r.Transport,
			Action: ActionOpenAlertGroupsMoved, Resource: audit.Resource{Type: "route", PublicID: route.PublicID,
				Name: route.Name}, Details: map[string]any{"count": moved}, SourceAddress: r.Address})
	})
	if err != nil {
		return 0, err
	}
	after.run(ctx)
	return moved, nil
}
