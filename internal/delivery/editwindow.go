// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// The edit windows of Bridge Root messages (C-22.FR-9). A bridge whose info declares limits.edit_max_age_seconds edits
// a Root message only that long after its Publication; Muster stops EditWindowMargin earlier, on the business clock,
// and a refusal of an edit as `gone` with the reason `edit_window_closed` marks the Root message past its window at
// once. Past its window, a Root message takes no edit: a Desired state with the status the message shows makes no
// call and leaves the delivery delivered, flagged past_edit_window; a new status publishes a new Root message Quietly,
// with the key of the next Publication, replies under the old one with a link to the new one (the pointer reply,
// part of the republication: no Thread reply row, never retried, its failure recorded with the delivery event), and
// later Thread replies go under the new one; the old message's id is kept in superseded_message_ids. A Storm summary
// is never published again. A final edit (C-11.FR-14) is sent as a Thread reply with the same text. A Root message
// published again after a deletion (C-11.FR-13) has a window of its own.
//
// A republication keeps its start as a first Publication does (C-22.FR-6): recorded before the call
// (republication_started_at), kept while its answer was lost, so that a new Root message the bridge may have posted is
// never abandoned with a stale status and live buttons (C-11.FR-14). While it is kept, the next call publishes again
// with the same key whatever the status — carrying the final edit when the Destination left meanwhile, which then ends
// the delivery unless replayed — and a retry that is not replayed records a possible duplicate.

// EditWindowMargin is bridge.edit_window_margin: a Root message is past its bridge's edit window this long before
// edit_max_age_seconds.
const EditWindowMargin = time.Minute

// editWindowQueries are the queries of the edit windows.
type editWindowQueries interface {
	StartRepublication(ctx context.Context, arg dbgen.StartRepublicationParams) error
	RecordRepublished(ctx context.Context, arg dbgen.RecordRepublishedParams) (int64, error)
	RecordEditWindowClosed(ctx context.Context, arg dbgen.RecordEditWindowClosedParams) (int64, error)
}

// editWindow is the edit window a bridge declares: max, when ok; edits never expire otherwise.
type editWindow struct {
	max time.Duration
	ok  bool
}

// pastWindow reports whether a Root message published at publishedAt is past an edit window of length at now: as old
// as length less EditWindowMargin, or older.
func pastWindow(publishedAt, now time.Time, length time.Duration) bool {
	return !now.Before(publishedAt.Add(length - EditWindowMargin))
}

// The rules of a call to a published Root message of an EditWindowed adapter.
type editRule int

const (
	// ruleEdit is an edit, within the window.
	ruleEdit editRule = iota
	// ruleNoCall leaves a Root message past its window as it is.
	ruleNoCall
	// ruleRepublish publishes a new Root message for a new status.
	ruleRepublish
	// ruleFinalReply sends the final edit as a Thread reply.
	ruleFinalReply
)

// ruleOf is the rule of the leased delivery row, published, whose Desired state shows the status desired, at now
// under the window win: a republication again while one started, whatever the status and the window; an edit within
// the window; past it — flagged so, or as old as the window says — the final edit as a Thread reply, no call for a
// Storm summary or for a status the message shows already, and a republication otherwise, also when the status the
// message shows is unknown.
func ruleOf(row dbgen.GetLeasedDeliveryRow, desired string, win editWindow, now time.Time) editRule {
	past := row.PastEditWindow || (win.ok && pastWindow(row.PublishedAt.Time, now, win.max))
	switch {
	case republishing(row):
		return ruleRepublish
	case !past:
		return ruleEdit
	case row.DesiredRetire:
		return ruleFinalReply
	case row.StormID.Valid, row.ActualStatus.Valid && row.ActualStatus.String == desired:
		return ruleNoCall
	}
	return ruleRepublish
}

// republishing reports whether a republication of the leased delivery row started and may have reached the bridge:
// its start is kept until its outcome is recorded.
func republishing(row dbgen.GetLeasedDeliveryRow) bool {
	return row.RepublicationStartedAt.Valid && !unpublished(row)
}

// editWindowed is the adapter of the Destination type typ when its messenger has edit windows (C-22.FR-9).
func (w *Worker) editWindowed(typ string) (EditWindowed, bool) {
	e, ok := w.Adapters[typ].(EditWindowed)
	return e, ok
}

// editWindow reads the edit window of the Destination d outside any transaction, in the delivery client class.
func (w *Worker) editWindow(ctx context.Context, d Destination) *editWindow {
	win := &editWindow{}
	if e, ok := w.editWindowed(d.Type); ok {
		win.max, win.ok = e.EditWindow(ctx, Call{Class: outbound.ClassDelivery, Destination: d})
	}
	return win
}

// pointerMessage is the pointer reply under a Root message replaced by m's: "This message is no longer updated. The
// current one: {link}", in m's language.
func pointerMessage(m Message, link string) Message {
	return Message{Kind: messages.KindReply, Language: m.Language, TimeZone: m.TimeZone, Colour: m.Colour,
		Lines: []string{messages.T(m.Language, "reply.pointer", messages.Args{"link": link})}, Buttons: []messages.Button{}}
}

// republished records a republication past the edit window that the messenger accepted: the new Root message
// replaces the old one, whose id is kept; the possible duplicate of a retry after a started republication that was not
// replayed; the republication itself (republication); the release of the old buttons' key, which the old message,
// whose buttons still work, keeps in use while its Alert Group is open; the mark of the Alert Group's first
// Publication when it is still unmarked; and the end of a Broken state that a probe reached. A republication that
// carried the final edit and was not replayed posted the final message itself: the delivery is retired as a final
// edit is. The outcome locks the delivery row only, as every outcome does.
func (w *Worker) republished(ctx context.Context, q queries, org int64, a attempt, out Outcome, now time.Time,
	logs *after) (bool, error) {
	duplicate := a.duplicate(out)
	if a.row.DesiredRetire && !out.Replayed {
		return w.retired(ctx, q, org, a, now, true, duplicate, func() error {
			return w.republication(ctx, q, org, a, out, now, logs)
		}, logs)
	}
	key := buttonKeyOf(a.message)
	n, err := q.RecordRepublished(ctx, dbgen.RecordRepublishedParams{OrgID: org, ID: a.row.ID, Owner: w.Lease.Owner,
		MessageID: out.MessageID, MessageUrl: nonEmpty(out.MessageURL), ThreadRef: nonEmpty(out.ThreadRef),
		Version: a.row.DesiredVersion, Hash: a.row.DesiredHash, ActualStatus: nonEmpty(a.message.Colour),
		ButtonKeyID: nonEmpty(key), Replayed: out.Replayed, PossibleDuplicate: duplicate, Now: now})
	if err != nil {
		return false, fmt.Errorf("record the republication past the edit window: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	if err := hintGroup(ctx, q, org, a.row.AlertGroupPublicID); err != nil {
		return false, err
	}
	if duplicate {
		if err := duplicateRecorded(ctx, q, org, a.row, now, logs); err != nil {
			return false, err
		}
	}
	if err := released(ctx, q, org, a.row.ActualButtonKeyID.String, key); err != nil {
		return false, err
	}
	if err := w.republication(ctx, q, org, a, out, now, logs); err != nil {
		return false, err
	}
	if a.row.GroupUnpublished {
		logs.add(w.markPublished(a.row.AlertGroupID, a.row.PublishedAt.Time))
	}
	if a.row.DestinationHealth == healthBroken {
		return true, markHealthy(ctx, q, w.raiser(org), org, a.destination.ID, now, logs)
	}
	return true, nil
}

// republication records the republication past the edit window the messenger accepted as out: the
// republished_past_edit_window delivery event, with the pointer reply's failure when it failed, the hold of the
// limiter a pointer reply answered with RetryAfter names — the pointer reply is never retried, but the next call waits
// as asked — and, once committed, the bridge_republished_past_edit_window line.
func (w *Worker) republication(ctx context.Context, q queries, org int64, a attempt, out Outcome, now time.Time,
	logs *after) error {
	old := a.row.MessageID.String
	r := deliveryEvent(DeliveryPastEditWindow)
	e := Event{At: now, DestinationID: a.destination.ID, AlertGroupID: a.group(), Kind: EventRepublishedPastEditWindow,
		Loudness: r.Loudness, Mentions: r.Mentions,
		Detail: map[string]any{"old_message_id": old, "new_message_id": out.MessageID, "pointer": "sent"}}
	if p := a.pointer; p == nil || p.Kind != OutcomeOK {
		failed := Outcome{Kind: OutcomeUnknown}
		if p != nil {
			failed = *p
		}
		e.Detail["pointer"] = "failed"
		e.ErrorClass, e.Error = cmp.Or(errorClass(failed.Kind), string(OutcomeUnknown)),
			outbound.Untrusted(failure(failed, unknownText))
	}
	if err := RecordEvent(ctx, q, org, e); err != nil {
		return err
	}
	if p := a.pointer; p != nil && p.Kind == OutcomeRetryAfter {
		if err := holdBucket(ctx, q, org, a.destination, *p, now.Add(p.RetryAfter)); err != nil {
			return err
		}
	}
	destination, group, id := a.destination.PublicID, a.row.AlertGroupPublicID, out.MessageID
	logs.add(func(ctx context.Context, log *logging.Logger) {
		log.Log(ctx, logging.BridgeRepublishedPastEditWindow, logging.F("destination", destination),
			logging.F("group", group), logging.F("old_message_id", old), logging.F("new_message_id", id))
	})
	return nil
}

// editWindowClosed records an edit the bridge refused only because of the Root message's age (C-22.FR-8): not the
// deleted Root message flow, but a Root message past its window, due at once, so that the rules of the window decide
// the Desired state the edit carried.
func (w *Worker) editWindowClosed(ctx context.Context, q queries, org int64, a attempt, now time.Time) (bool, error) {
	n, err := q.RecordEditWindowClosed(ctx, dbgen.RecordEditWindowClosedParams{OrgID: org, ID: a.row.ID,
		Owner: w.Lease.Owner, Now: now})
	if err != nil {
		return false, fmt.Errorf("record the closed edit window: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	return true, hintGroup(ctx, q, org, a.row.AlertGroupPublicID)
}
