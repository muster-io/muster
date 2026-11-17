// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"fmt"
	"slices"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
)

// Pages by Mention (C-32.FR-9, FR-10, C-11.FR-2 revised for L2): the fan-out of paged makes one Thread reply per
// Destination of its Page (where.go), pending at once without the Thread batching window, Loud and never batched with
// other replies. Its Mentions are the people of the Page with an Account link in the Destination's Identity space —
// paged_user_ids, decided when it was queued — or the Team mention or the token of a page_team step; a person without
// such a link is named in a line of the reply instead. The outcome of the replies goes back to the dispatcher
// (groups.SettlePage, PageReached and PageNotDelivered): a delivered reply that mentions a person delivers the Page to
// them, and once every reply of the Page ended — sent, Not delivered or withheld, as after a Broken Destination whose
// replies are dropped at its recovery (C-11.FR-19) — those it reached nowhere are not delivered. Each reply settles
// its Page once its outcome committed, and every round settles the Pages whose replies all ended without it.

// PageSettler records the outcome of the replies of a Page through the dispatcher, locking its Alert Group first:
// (*groups.Service).SettlePage.
type PageSettler interface {
	SettlePage(ctx context.Context, tx groups.DBTX, groupID int64, o groups.PageOutcome) (func(context.Context), error)
}

// pageReply is what the reply of a Page says and whom it mentions in the Destination d: its PageInput, and its
// targets — the people it mentions by their Account links, or the Team mention or the token of a page_team step.
func (w *Worker) pageReply(ctx context.Context, tx dbgen.DBTX, q queries, org int64, row dbgen.GetLeasedReplyRow,
	d Destination) (*messages.PageInput, []mentions.Target, error) {
	p, err := q.GetPageReply(ctx, dbgen.GetPageReplyParams{OrgID: org, ID: row.PageID.Int64})
	if err != nil {
		return nil, nil, fmt.Errorf("read the page of the thread reply: %w", err)
	}
	space := mentions.IdentitySpace(d.Type, d.Connection)
	users, err := q.ListPageReplyUsers(ctx, dbgen.ListPageReplyUsersParams{OrgID: org, PageID: row.PageID.Int64,
		IdentitySpace: space})
	if err != nil {
		return nil, nil, fmt.Errorf("read the people of the page of the thread reply: %w", err)
	}
	in := &messages.PageInput{Step: int(p.StepNumber), Messenger: d.Type, Source: row.PageSource.String,
		ChainTeam: p.ChainTeamName, Importance: p.Importance, AgainSeconds: int(p.AgainSeconds), Again: p.Again,
		Team: p.TeamName, Everyone: p.Everyone != ""}
	if p.OnCall {
		in.OnCallTeam, in.Schedule = p.ScheduleTeamName, p.ScheduleName
	}
	var targets []mentions.Target
	for _, u := range users {
		if !slices.Contains(row.PagedUserIds, u.ID) {
			in.Unlinked = append(in.Unlinked, u.Login)
		} else {
			targets = append(targets, mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{
				PublicID: u.PublicID, Name: u.Name, Login: u.Login, Username: u.Username, ExternalID: u.ExternalID,
				DisplayName: u.DisplayName}})
		}
		if !p.OnCall {
			in.People = append(in.People, u.Login)
		}
	}
	switch {
	case p.Everyone != "":
		targets = append(targets, mentions.Target{Kind: mentions.TargetEveryone, Everyone: p.Everyone})
	case p.TeamID.Valid && w.TeamMentions != nil && d.Connection != nil:
		m, ok, err := w.TeamMentions.ForConnection(ctx, tx, p.TeamID.Int64, *d.Connection)
		if err != nil {
			return nil, nil, fmt.Errorf("read the team mention of the page: %w", err)
		}
		if ok {
			targets = append(targets, mentions.Target{Kind: mentions.TargetGroup, Group: m.Group, GroupID: m.GroupID})
		}
	}
	in.Mentioned = len(targets) > 0
	return in, targets, nil
}

// settlePage reports the outcome of the replies of the Page id of the Alert Group groupID so far (groups.PageOutcome):
// the people its sent replies mentioned, whether every reply ended and whether one was sent.
func (w *Worker) settlePage(ctx context.Context, org, groupID, id int64) error {
	if w.Pages == nil {
		return nil
	}
	var done func(context.Context)
	err := w.Store.inTxWith(ctx, func(tx dbgen.DBTX, q queries) error {
		replies, err := q.ListPageReplies(ctx, dbgen.ListPageRepliesParams{OrgID: org, PageID: id})
		if err != nil {
			return fmt.Errorf("read the replies of page %d: %w", id, err)
		}
		o := groups.PageOutcome{Page: id, Ended: true}
		for _, r := range replies {
			switch r.State {
			case replySent:
				o.Delivered = true
				for _, u := range r.PagedUserIds {
					if !slices.Contains(o.Reached, u) {
						o.Reached = append(o.Reached, u)
					}
				}
			case replyPending, replyCollecting:
				o.Ended = false
			}
		}
		done, err = w.Pages.SettlePage(ctx, tx, groupID, o)
		return err
	})
	if err == nil && done != nil {
		done(ctx)
	}
	return err
}

// settlePages settles the Pages of the Organization whose replies all ended without a reply settling them, such as
// those dropped at the recovery of a Broken Destination, a batch at a time in id order after the last one, at most
// PageSettleBatches batches a round, so that a backlog does not hold back the deliveries and replies of the next
// round. The next round goes on after the last Page this one listed (pagesAfter), and after a short batch, which
// reached the newest, it starts again from the oldest. A Page that fails to settle is logged and the others are still
// settled, as probe does, so that failing Pages, even more than a round's batches of them, neither hold back the Pages
// after them nor put the worker in backoff: a later round reaches those, and the next start from the oldest tries the
// failing ones again. It reports whether the round stopped at PageSettleBatches full batches, so that more Pages may
// wait and Round makes the next round come soon.
func (w *Worker) settlePages(ctx context.Context, org int64) (bool, error) {
	if w.Pages == nil {
		return false, nil
	}
	if w.pagesAfter == nil {
		w.pagesAfter = map[int64]int64{}
	}
	after := w.pagesAfter[org]
	defer func() { w.pagesAfter[org] = after }()
	for range PageSettleBatches {
		rows, err := w.Store.queries(w.Store.db).ListEndedPages(ctx, dbgen.ListEndedPagesParams{OrgID: org,
			After: after, Lim: w.batch()})
		if err != nil {
			return false, fmt.Errorf("list the pages to settle: %w", err)
		}
		for _, r := range rows {
			if err := w.settlePage(ctx, org, r.AlertGroupID, r.ID); err != nil && ctx.Err() == nil {
				w.Log.Log(ctx, logging.DeliveryWorkFailed, logging.F("work", "page"), logging.F("error", err.Error()))
			}
		}
		if len(rows) < int(w.batch()) {
			after = 0
			return false, nil
		}
		after = rows[len(rows)-1].ID
	}
	return true, nil
}

// settleReply settles the Page of a reply once its outcome committed; a failure is logged, and the round settles the
// Page once all its replies ended.
func (w *Worker) settleReply(ctx context.Context, org int64, row dbgen.GetLeasedReplyRow) {
	if !row.PageID.Valid {
		return
	}
	if err := w.settlePage(ctx, org, row.AlertGroupID, row.PageID.Int64); err != nil && ctx.Err() == nil {
		w.Log.Log(ctx, logging.DeliveryWorkFailed, logging.F("work", "page"), logging.F("error", err.Error()))
	}
}
