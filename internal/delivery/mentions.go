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

// MentionDetail is the detail of a mention_not_delivered or mention_delivered delivery event: the person's id, the
// entry's id, position and target, the Loud event and, for mention_not_delivered, the reason. The person and the
// target identify the warning across saves of the Destination, which write its entries again.
type MentionDetail struct {
	UserID   int64                `json:"user_id"`
	EntryID  int64                `json:"entry_id"`
	Position int                  `json:"position"`
	Target   mentions.EntryTarget `json:"target"`
	Event    string               `json:"event"`
	Reason   string               `json:"reason,omitempty"`
}

// detailMap is d as the detail of a delivery event.
func (d MentionDetail) detailMap() map[string]any {
	m := map[string]any{"user_id": d.UserID, "entry_id": d.EntryID, "position": d.Position, "target": d.Target,
		"event": d.Event}
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
		if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: d.ID, AlertGroupID: group, StormID: storm,
			Kind: EventMentionNotDelivered, Loudness: r.Loudness, Mentions: r.Mentions, Detail: MentionDetail{UserID: u.UserID, EntryID: u.Entry.ID,
				Position: u.Entry.Position, Target: u.Entry.Target, Event: u.Event, Reason: u.Reason}.detailMap()},
		); err != nil {
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
		ids = append(ids, strconv.FormatInt(m.UserID, 10))
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
			Kind: EventMentionDelivered, Loudness: r.Loudness, Detail: MentionDetail{UserID: m.UserID, EntryID: m.Entry.ID,
				Position: m.Entry.Position, Target: m.Entry.Target, Event: m.Event}.detailMap()}); err != nil {
			return err
		}
	}
	return nil
}

// asDelivered is the resolution res of a delivered message as its outcome out reports it: the people whose Mentions
// it left out for length and those it named without a marker are Undelivered.
func asDelivered(res mentions.Result, out Outcome) mentions.Result {
	return res.LeftOut(out.LeftOut).Plain(out.Plain)
}

// openFor reports whether the open warnings include the person and the entry's target of m.
func openFor(open []dbgen.ListOpenMentionsRow, m mentions.Mentioned) bool {
	for _, o := range open {
		var t mentions.EntryTarget
		if o.UserID == m.UserID && json.Unmarshal(o.Target, &t) == nil && t == m.Entry.Target {
			return true
		}
	}
	return false
}
