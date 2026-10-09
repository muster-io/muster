// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

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
//
// In Telegram (C-14.FR-3, copies.go) a reply goes to the discussion group: as a reply to the post's automatic copy
// while the Thread is attached; while it waits for the copy, at most telegram.copy_wait from the Publication, after
// which it starts a chain of replies not attached to the post, each a reply to the last, and the Thread is unattached
// with the thread_not_attached delivery event. A reply the messenger refuses because the copy is gone (thread_lost,
// F-008) is sent again at once without its reply link and makes the Thread unattached the same way.

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

// replyAttempt is a Thread reply that has its tokens and is about to be sent: under root, and as a link of an
// unattached Telegram chain when chain is set; anchor is the copy its Thread was attached to when it was prepared.
type replyAttempt struct {
	row         dbgen.GetLeasedReplyRow
	destination Destination
	call        Call
	message     Message
	root        Root
	chain       bool
	anchor      string
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
	err := w.Store.inTxWith(ctx, func(tx dbgen.DBTX, q queries) error {
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
		root, chain := Root{MessageID: row.MessageID.String}, false
		if d.Type == TypeTelegram {
			var until time.Time
			if root, chain, until, err = telegramThread(ctx, q, org, row, now); err != nil {
				return err
			}
			if !until.IsZero() {
				if err := q.RescheduleReply(ctx, dbgen.RescheduleReplyParams{OrgID: org, ID: id, Owner: w.Lease.Owner,
					At: until}); err != nil {
					return fmt.Errorf("let the thread reply wait for the copy: %w", err)
				}
				return nil
			}
		}
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
		g := GroupView{ID: row.AlertGroupID, PublicID: row.AlertGroupPublicID, Number: row.Number, Title: row.Title,
			Status: groups.Status(row.Status), Urgent: row.Urgent}
		mentions := make([]groups.Mention, len(row.Mentions))
		for i, m := range row.Mentions {
			mentions[i] = groups.Mention(m)
		}
		var seq int64
		if len(row.EventSeqs) > 0 {
			seq = row.EventSeqs[0]
		}
		var msg Message
		var webhook *WebhookCall
		if sendsRequests(d.Type) {
			// The request templates render the Alert Group's latest Desired state and the event the reply carries.
			webhook = &WebhookCall{State: row.DesiredPayload, Response: responseValues(row.ResponseValues),
				ThreadOpened: row.ThreadOpened, Event: row.Event}
		} else if msg, err = w.Renderer.Reply(ctx, tx, ReplyView{Event: groups.Event(row.Event), Seq: seq,
			Fingerprints: row.Fingerprints, Language: row.Language, Listed: ThreadAlertsListed}, g); err != nil {
			return fmt.Errorf("render the thread reply: %w", err)
		}
		targets, err := w.targets(ctx, tx, org, d, row.AlertGroupID, seq, groups.Loudness(row.Loudness), mentions)
		if err != nil {
			return err
		}
		a = replyAttempt{row: row, destination: d,
			call: Call{Class: outbound.ClassDelivery, Destination: d, Loudness: groups.Loudness(row.Loudness),
				Mentions: mentions, Targets: targets, Webhook: webhook},
			message: msg, root: root, chain: chain, anchor: root.ThreadAnchorID}
		ok = true
		return nil
	})
	return a, ok, err
}

// sendReply sends a prepared Thread reply outside any transaction and records its outcome by its rule (outcomes.go);
// a markup the messenger rejects is sent again without markup, and a reply whose Thread is lost again without its reply
// link, in the same attempt.
func (w *Worker) sendReply(ctx context.Context, org int64, a replyAttempt) error {
	start := w.Lease.Clocks.Real.Now()
	c := a.call
	var lost *Outcome
	send := func(c Call) Outcome {
		out := w.sendReplyCall(ctx, a, c)
		if out.Kind != OutcomeThreadLost || (a.root.ThreadAnchorID == "" && a.root.ChainLastID == "") {
			return out
		}
		first := out
		lost = &first
		w.attempted(ctx, a.destination, a.row.Number, kindReply, out, a.row.Attempts+1,
			w.Lease.Clocks.Real.Now().Sub(start))
		a.root, a.chain = Root{MessageID: a.root.MessageID}, true
		return w.sendReplyCall(ctx, a, c)
	}
	out := send(c)
	var rejected *Outcome
	if out.Kind == OutcomeMarkupRejected {
		first := out
		rejected = &first
		w.attempted(ctx, a.destination, a.row.Number, kindReply, out, a.row.Attempts+1,
			w.Lease.Clocks.Real.Now().Sub(start))
		c.Plain = true
		out = plain(func() Outcome { return send(c) })
	}
	took := w.Lease.Clocks.Real.Now().Sub(start)
	var logs after
	err := w.Store.inTx(ctx, func(q queries) error {
		logs = nil
		return w.recordReply(ctx, q, org, a, out, rejected, lost, &logs)
	})
	w.attempted(ctx, a.destination, a.row.Number, kindReply, out, a.row.Attempts+1, took)
	if err == nil {
		logs.run(ctx, w.Log)
	}
	return err
}

// sendReplyCall is one adapter call of a prepared Thread reply; a type no adapter serves is an unknown response.
func (w *Worker) sendReplyCall(ctx context.Context, a replyAttempt, c Call) Outcome {
	adapter := w.Adapters[a.destination.Type]
	if adapter == nil {
		return Outcome{Kind: OutcomeUnknown, Error: outbound.Untrusted("no adapter for the destination type " +
			a.destination.Type)}
	}
	return adapter.Reply(ctx, c, a.root, a.message)
}

// telegramThread is where a Telegram Thread reply goes: under the copy of an attached Thread; as the next link of an
// unattached chain; or, while the Thread waits for the copy, under the copy once it is known — looked up again under
// the lock of the post, which attaches the Thread — and otherwise, after telegram.copy_wait from the Publication, as the
// first link of a chain. until is when a reply that still waits for the copy is due again, zero for one to send now.
func telegramThread(ctx context.Context, q queries, org int64, row dbgen.GetLeasedReplyRow, now time.Time) (
	Root, bool, time.Time, error) {
	root := Root{MessageID: row.MessageID.String}
	state, anchor, chain := row.ThreadState, row.ThreadAnchorID.String, row.ThreadChainLastID.String
	if state != threadAttached && state != threadUnattached {
		if p, ok := telegramPost(row.ConnectionID, row.TelegramChannelChatID, row.TelegramDiscussionChatID,
			row.MessageID.String); ok {
			if err := lockPost(ctx, q, p); err != nil {
				return root, false, time.Time{}, err
			}
			th, err := q.LockDeliveryThread(ctx, dbgen.LockDeliveryThreadParams{OrgID: org, ID: row.DeliveryID})
			if err != nil {
				return root, false, time.Time{}, fmt.Errorf("read the thread of the delivery: %w", err)
			}
			state, anchor, chain = th.ThreadState, th.ThreadAnchorID.String, th.ThreadChainLastID.String
			if state != threadAttached && state != threadUnattached {
				known, err := knownCopy(ctx, q, org, p)
				if err != nil {
					return root, false, time.Time{}, err
				}
				if known != "" {
					if err := setThread(ctx, q, org, row.DeliveryID, threadAttached, known, "", now); err != nil {
						return root, false, time.Time{}, err
					}
					state, anchor = threadAttached, known
				}
			}
		}
	}
	switch state {
	case threadAttached:
		root.ThreadAnchorID = anchor
		return root, false, time.Time{}, nil
	case threadUnattached:
		root.ChainLastID = chain
		return root, true, time.Time{}, nil
	}
	if started := row.PublicationStartedAt; started.Valid && now.Before(started.Time.Add(CopyWait)) {
		return root, false, started.Time.Add(CopyWait).UTC(), nil
	}
	return root, true, time.Time{}, nil
}

// changesThread reports whether the outcome of a Thread reply changes its Telegram Thread: a chain link sent, or a
// lost Thread.
func changesThread(a replyAttempt, out Outcome, lost *Outcome) bool {
	return a.chain && (out.Kind == OutcomeOK || lost != nil)
}

// lockThread locks the Thread of a reply's delivery, before the reply's row is recorded, so that a transaction that
// records a reply takes the delivery's row before the reply's, as the deletion of a Root message does.
func lockThread(ctx context.Context, q queries, org int64, a replyAttempt) (dbgen.LockDeliveryThreadRow, error) {
	th, err := q.LockDeliveryThread(ctx, dbgen.LockDeliveryThreadParams{OrgID: org, ID: a.row.DeliveryID})
	if err != nil {
		return th, fmt.Errorf("read the thread of the delivery: %w", err)
	}
	return th, nil
}

// recordThread records, after the outcome of a Telegram Thread reply, what it did to the Thread th, locked before:
// a chain link sent becomes the last reply of the unattached chain, and a lost Thread is unattached, its chain
// starting over; a Thread that becomes unattached records the thread_not_attached delivery event and sends the
// alert-group hint. A chain link sent while the copy was learned leaves the attached Thread alone, and a reply under
// a Root message that was published again meanwhile leaves the new Thread alone.
func (w *Worker) recordThread(ctx context.Context, q queries, org int64, a replyAttempt, th dbgen.LockDeliveryThreadRow,
	out Outcome, lost *Outcome, now time.Time) error {
	if th.MessageID.String != a.row.MessageID.String ||
		(th.ThreadState == threadAttached && (lost == nil || th.ThreadAnchorID.String != a.anchor)) {
		return nil
	}
	chain := ""
	if out.Kind == OutcomeOK {
		chain = out.MessageID
	}
	if err := setThread(ctx, q, org, a.row.DeliveryID, threadUnattached, th.ThreadAnchorID.String, chain,
		now); err != nil {
		return err
	}
	if th.ThreadState == threadUnattached {
		return nil
	}
	e := Event{At: now, DestinationID: a.destination.ID, AlertGroupID: &a.row.AlertGroupID,
		Kind: EventThreadNotAttached}
	if lost != nil {
		e.Error = outbound.Untrusted(failure(*lost, "the thread was lost"))
	}
	if err := RecordEvent(ctx, q, org, e); err != nil {
		return err
	}
	return hintGroup(ctx, q, org, a.row.AlertGroupPublicID)
}
