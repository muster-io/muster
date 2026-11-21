// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/mentions"
)

// Undelivered Mentions (C-28.FR-3, C-11.FR-21 revised for L2). Once a Loud message is delivered — a first Publication
// or a Thread reply, in the transaction that records it delivered under its lease, so that a retry after a crash
// records them once — its delivery records, for each person whom a Mention entry named, once per entry:
// mention_not_delivered when the message does not mention them as their messenger mentions them, with the reason —
// message_too_long for a person whose Mention the adapter left out for the message's length (Outcome.LeftOut), and
// bridge_cannot_mention_people for one it named by the plain name for want of a marker (Outcome.Plain); and
// mention_delivered when it does and their latest such event through that entry on the Destination is
// mention_not_delivered, which closes that warning. Nothing is posted for either (ADR-0005). A message that ends Not
// delivered, a Quiet one and an edit record neither. The events of the first publication of a Storm summary name its
// Storm and no Alert Group, so that they warn on the Destination and appear in no Timeline.
//
// Through a bridge the same holds for each group and everyone in the chat that an entry named — a group entry, a Team
// mention, everyone — keyed by its Mention instead of a person: mention_not_delivered with message_too_long when the
// line left it out (Outcome.LeftOutMentions, C-12.FR-11), and mention_delivered when a later message carries it while
// its warning is open. A template's token that the line left out names no entry: it records mention_not_delivered with
// its Mention and the reason alone, which only the Timeline shows, so none for a Storm summary, and no warning.

// MentionDetail is the detail of a mention_not_delivered or mention_delivered delivery event: the person's id — or,
// for a group or everyone in the chat, its Mention — the entry's id, position and target, the Loud event and, for
// mention_not_delivered, the reason. The person or the Mention and the target identify the warning across saves of the
// Destination, which write its entries again. A template's token has neither an entry nor an event.
type MentionDetail struct {
	UserID   int64                `json:"user_id,omitempty"`
	Mention  mentions.Mention     `json:"mention,omitzero"`
	EntryID  int64                `json:"entry_id,omitempty"`
	Position int                  `json:"position"`
	Target   mentions.EntryTarget `json:"target,omitzero"`
	Event    string               `json:"event,omitempty"`
	Reason   string               `json:"reason,omitempty"`
}

// mentionDetailOf is the detail of a mention event about the person userID or the Mention mention named by the entry
// for the Loud event, with the reason of an undelivered one.
func mentionDetailOf(userID int64, mention mentions.Mention, entry mentions.EntryRef, event,
	reason string) MentionDetail {
	return MentionDetail{UserID: userID, Mention: mention, EntryID: entry.ID, Position: entry.Position,
		Target: entry.Target, Event: event, Reason: reason}
}

// detailMap is d as the detail of a delivery event: user_id for a person and mention for a group or everyone, and the
// entry and the event unless it names none.
func (d MentionDetail) detailMap() map[string]any {
	m := map[string]any{}
	if d.Mention.Kind != "" {
		m["mention"] = d.Mention
	} else {
		m["user_id"] = d.UserID
	}
	if d.EntryID != 0 {
		m["entry_id"], m["position"], m["target"], m["event"] = d.EntryID, d.Position, d.Target, d.Event
	}
	if d.Reason != "" {
		m["reason"] = d.Reason
	}
	return m
}

// recordMentions records, through q in the transaction that recorded a Loud message delivered at now to d, the
// undelivered Mentions of its resolution res and the delivered ones that close an open warning, about the Alert Group
// group or the Storm storm.
func recordMentions(ctx context.Context, q queries, org int64, d Destination, group, storm *int64,
	res mentions.Result, now time.Time) error {
	r := deliveryEvent(DeliveryMentionNotDelivered)
	for _, u := range res.Undelivered {
		if u.Entry.ID == 0 && group == nil {
			// A template's token, which only the Timeline shows, and a Storm summary has none.
			continue
		}
		if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: d.ID, AlertGroupID: group, StormID: storm,
			Kind: EventMentionNotDelivered, Loudness: r.Loudness, Mentions: r.Mentions,
			Detail: mentionDetailOf(u.UserID, u.Mention, u.Entry, u.Event, u.Reason).detailMap()}); err != nil {
			return err
		}
	}
	if len(res.Mentioned) == 0 {
		return nil
	}
	days, err := q.GetRetentionDetailsDays(ctx, org)
	if err != nil {
		return fmt.Errorf("read the retention of the delivery events: %w", err)
	}
	ids := make([]string, 0, len(res.Mentioned))
	for _, m := range res.Mentioned {
		if m.Mention.Kind == "" {
			ids = append(ids, strconv.FormatInt(m.UserID, 10))
		}
	}
	open, err := q.ListOpenMentions(ctx, dbgen.ListOpenMentionsParams{OrgID: org, DestinationID: d.ID,
		Since: now.AddDate(0, 0, -int(days)), UserIds: ids})
	if err != nil {
		return fmt.Errorf("read the open warnings about mentions of destination %s: %w", d.PublicID, err)
	}
	for _, m := range res.Mentioned {
		if !openFor(open, m) {
			continue
		}
		if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: d.ID, AlertGroupID: group, StormID: storm,
			Kind: EventMentionDelivered, Loudness: r.Loudness,
			Detail: mentionDetailOf(m.UserID, m.Mention, m.Entry, m.Event, "").detailMap()}); err != nil {
			return err
		}
	}
	return nil
}

// asDelivered is the resolution res of a delivered message as its outcome out reports it: the people, groups and
// mentions of everyone whose Mentions it left out for length and the people it named without a marker are
// Undelivered.
func asDelivered(res mentions.Result, out Outcome) mentions.Result {
	return res.LeftOut(out.LeftOut).Plain(out.Plain).LeftOutMentions(out.LeftOutMentions)
}

// openFor reports whether the open warnings include the person or the Mention, and the entry's target, of m.
func openFor(open []dbgen.ListOpenMentionsRow, m mentions.Mentioned) bool {
	for _, o := range open {
		var t mentions.EntryTarget
		var n mentions.Mention
		if o.UserID != m.UserID || json.Unmarshal(o.Target, &t) != nil || t != m.Entry.Target ||
			len(o.Mention) > 0 && json.Unmarshal(o.Mention, &n) != nil {
			continue
		}
		if n == m.Mention {
			return true
		}
	}
	return false
}
