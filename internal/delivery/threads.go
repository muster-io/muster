// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// Thread replies (C-11.FR-4, FR-5): follow-ups under a Root message, sent by the delivery worker once the Root message
// exists, one delivery's replies in id order. A collecting batch of new Alerts closes when its Thread batching window
// ends; a reply lists at most delivery.thread_alerts_listed new Alerts.

// claimReplies leases the due Thread replies of the Organization.
func (w *Worker) claimReplies(ctx context.Context, org int64) ([]int64, error) {
	rows, err := db.Claim(ctx, w.Store.begin, w.Lease, w.batch(),
		func(ctx context.Context, tx pgx.Tx, p db.ClaimParams) ([]dbgen.ClaimDueRepliesRow, error) {
			return w.Store.queries(tx).ClaimDueReplies(ctx, dbgen.ClaimDueRepliesParams{OrgID: org, Owner: p.Owner,
				LeaseUntil: p.LeaseUntil, Due: p.Due, Now: p.Now, Lim: p.Limit})
		})
	if err != nil {
		return nil, fmt.Errorf("claim the due thread replies: %w", err)
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// replyAttempt is a Thread reply that has its tokens and is about to be sent.
type replyAttempt struct {
	row         dbgen.GetLeasedReplyRow
	destination Destination
	call        Call
	message     Message
}

// reply sends one claimed Thread reply; a failure is logged and the reply is attempted again once its lease runs out.
func (w *Worker) reply(ctx context.Context, org, id int64) {
	a, ok, err := w.prepareReply(ctx, org, id)
	if err == nil && ok {
		err = w.sendReply(ctx, org, a)
	}
	if err != nil && ctx.Err() == nil {
		w.Log.Log(ctx, logging.DeliveryWorkFailed, logging.F("work", "thread_reply"), logging.F("error", err.Error()))
	}
}

// prepareReply re-reads a claimed Thread reply in a short transaction, while its lease is still held, takes its tokens
// and renews the lease for the call; one without tokens waits until they are due plus TokenMargin. ok says whether to
// send it.
func (w *Worker) prepareReply(ctx context.Context, org, id int64) (replyAttempt, bool, error) {
	var a replyAttempt
	ok := false
	err := w.Store.inTx(ctx, func(q queries) error {
		ok = false
		realNow := w.Lease.Clocks.Real.Now().UTC()
		row, err := q.GetLeasedReply(ctx, dbgen.GetLeasedReplyParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			Now: realNow})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the lease ran out: any replica claims it again
		}
		if err != nil {
			return fmt.Errorf("read the thread reply: %w", err)
		}
		now := w.Lease.Clocks.Business.Now().UTC()
		d := Destination{ID: row.DestinationID, PublicID: row.DestinationPublicID, Name: row.DestinationName,
			Type: row.DestinationType, Connection: int8Of(row.ConnectionID)}
		t, err := takeTokens(ctx, q, org, subjectOf(d), now)
		if err != nil {
			return err
		}
		if !t.ok {
			if err := q.RescheduleReply(ctx, dbgen.RescheduleReplyParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				At: t.due.Add(TokenMargin)}); err != nil {
				return fmt.Errorf("reschedule the thread reply: %w", err)
			}
			return nil
		}
		if err := q.RenewReplyLease(ctx, dbgen.RenewReplyLeaseParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
			LeaseUntil: realNow.Add(w.Lease.Duration)}); err != nil {
			return fmt.Errorf("renew the lease of the thread reply: %w", err)
		}
		g := GroupView{PublicID: row.AlertGroupPublicID, Number: row.Number, Title: row.Title,
			Status: groups.Status(row.Status), Urgent: row.Urgent}
		mentions := make([]groups.Mention, len(row.Mentions))
		for i, m := range row.Mentions {
			mentions[i] = groups.Mention(m)
		}
		a = replyAttempt{row: row, destination: d,
			call: Call{Class: outbound.ClassDelivery, Destination: d, Loudness: groups.Loudness(row.Loudness),
				Mentions: mentions},
			message: w.renderer().RenderReply(ReplyView{Event: groups.Event(row.Event), Fingerprints: row.Fingerprints,
				Language: row.Language, Listed: ThreadAlertsListed}, g, d)}
		ok = true
		return nil
	})
	return a, ok, err
}

func (w *Worker) renderer() Renderer {
	if w.Renderer == nil {
		return MinimalRenderer{}
	}
	return w.Renderer
}

// sendReply sends a prepared Thread reply outside any transaction and records its outcome.
func (w *Worker) sendReply(ctx context.Context, org int64, a replyAttempt) error {
	root := Root{MessageID: a.row.MessageID.String, ThreadAnchorID: a.row.ThreadAnchorID.String,
		ChainLastID: a.row.ThreadChainLastID.String}
	start := w.Lease.Clocks.Real.Now()
	var out Outcome
	if adapter := w.Adapters[a.destination.Type]; adapter != nil {
		out = adapter.Reply(ctx, a.call, root, a.message)
	} else {
		out = Outcome{Kind: OutcomeUnknown, Error: outbound.Untrusted("no adapter for the destination type " +
			a.destination.Type)}
	}
	took := w.Lease.Clocks.Real.Now().Sub(start)
	err := w.Store.inTx(ctx, func(q queries) error {
		now := w.Lease.Clocks.Business.Now().UTC()
		id := a.row.ID
		switch out.Kind {
		case OutcomeOK:
			if err := q.RecordReplySent(ctx, dbgen.RecordReplySentParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				MessageID: nonEmpty(out.MessageID), Now: now}); err != nil {
				return fmt.Errorf("record the sent thread reply: %w", err)
			}
			return nil
		case OutcomeRetryAfter:
			until := now.Add(out.RetryAfter)
			if err := q.RecordReplyRetry(ctx, dbgen.RecordReplyRetryParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				At: until.Add(TokenMargin), ErrorClass: nonEmpty(errorClass(out.Kind)),
				Error: nonEmpty(string(out.Error))}); err != nil {
				return fmt.Errorf("record the retry after of the thread reply: %w", err)
			}
			return holdBucket(ctx, q, org, a.destination, out, until)
		default:
			if err := q.RecordReplyRetry(ctx, dbgen.RecordReplyRetryParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
				At: now.Add(TransientFirstStep), ErrorClass: nonEmpty(errorClass(out.Kind)),
				Error: nonEmpty(string(out.Error))}); err != nil {
				return fmt.Errorf("record the failed thread reply: %w", err)
			}
			return nil
		}
	})
	w.attempted(ctx, a.destination, a.row.Number, kindReply, out, a.row.Attempts+1, took)
	return err
}
