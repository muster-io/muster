// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5/pgtype"
)

// Page is a Page as the Timeline entry of an event of the kind escalation names it (C-32.FR-19): the people it names,
// the Notification gateway of a call, nil for a Page by Mention, and the reason a Page was not delivered. groups knows
// no package of Paging (S-169); the callers fill it.
type Page struct {
	Users   []PagedUser
	Gateway *PageGateway
	Reason  string
}

// PagedUser is a person a Page names: a User by internal id, public_id, name and login.
type PagedUser struct {
	ID       int64
	PublicID string
	Name     string
	Login    string
}

// PageGateway is the Notification gateway of a call: its Connection by internal id, public_id and name.
type PageGateway struct {
	ID       int64
	PublicID string
	Name     string
}

// The reasons of page_not_delivered for a call through a Notification gateway (C-30.FR-7, PageNotDeliveredReason):
// the gateway refused it, answered what Muster cannot classify, kept failing until delivery.transient_budget was
// used, or the person had no confirmed contact at the gateway at its first attempt. S-149, S-155 and S-157 add
// theirs.
const (
	PageCallRefused        = "call_refused"
	PageCallUnknown        = "call_unknown"
	PageCallBudgetUsed     = "call_budget_used"
	PageNoConfirmedContact = "no_confirmed_contact"
)

// PageReasons are the reasons of page_not_delivered, a closed list.
var PageReasons = []string{PageCallRefused, PageCallUnknown, PageCallBudgetUsed, PageNoConfirmedContact}

func (p *Page) userIDs() []int64 {
	out := make([]int64, len(p.Users))
	for i, u := range p.Users {
		out[i] = u.ID
	}
	return out
}

func (p *Page) gatewayID() pgtype.Int8 {
	if p.Gateway == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: p.Gateway.ID, Valid: true}
}

// pageNotDelivered records the Quiet page_not_delivered of a Page on an Alert Group, whatever its status: a system
// transition that changes no status.
type pageNotDelivered struct {
	page *Page
}

func (pageNotDelivered) precondition(*Group) error { return nil }

func (t pageNotDelivered) apply(_ context.Context, _ Queries, _ *Group, c *change) error {
	c.add(entry{Event: EventPageNotDelivered, Reason: t.page.Reason, Page: t.page})
	return nil
}

// PageNotDelivered records page_not_delivered on each of the Alert Groups ids, through the dispatcher as a system
// transition in the transaction tx of its caller (C-30.FR-7, C-09.FR-22): the gateway sender, for a call that ended
// Not delivered, before it ends the call in the same transaction. The Alert Groups are locked in id order first, as
// every change of Alert Groups locks them, so the caller takes them before any lock of its own; one that no longer
// exists is skipped. It returns what to run once tx committed.
func (s *Service) PageNotDelivered(ctx context.Context, tx DBTX, ids []int64, p Page) (func(context.Context),
	error) {
	if !slices.Contains(PageReasons, p.Reason) {
		return nil, fmt.Errorf("page_not_delivered with the reason %q", p.Reason)
	}
	if len(p.Users) == 0 {
		return nil, errors.New("page_not_delivered names nobody")
	}
	q := s.queries(tx)
	locked, err := lock(ctx, q, s.orgID, ids)
	if err != nil {
		return nil, err
	}
	var after committed
	for _, id := range slices.Sorted(maps.Keys(locked)) {
		page := p
		if err := s.d.dispatch(ctx, q, locked[id], System, pageNotDelivered{page: &page}, &after); err != nil {
			return nil, err
		}
	}
	return after.run, nil
}
