// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
)

// Publications that need more than one call (C-11.FR-11, FR-12, FR-13): the late Publication of an Alert Group that
// resolved while its first Publication waited, the possible duplicate after a Publication whose outcome was never
// recorded, and the Root message deleted in the messenger. Their notes are fixed English lines in UTC until S-036
// renders them in the Organization's language and time zone; they are added at call time and never change the hash
// of the Desired state.

// The format of the times of the notes.
const noteClock = "15:04"

// lateNote is the note of a late Publication.
func lateNote(started, resolved time.Time) string {
	return fmt.Sprintf("Delivered late: started %s, resolved %s while this Destination was unavailable.",
		started.UTC().Format(noteClock), resolved.UTC().Format(noteClock))
}

// deletedNote is the note of a Root message published again after it was deleted in the messenger.
func deletedNote(at time.Time) string {
	return "The previous message was deleted at " + at.UTC().Format(noteClock)
}

// settleResolved decides, when an Alert Group resolves, its first Publication in a Destination that is still pending
// (C-11.FR-11, FR-19): withheld while the Destination is Broken; Quiet with the late note when it waited for a
// RetryAfter or for Transient retries within the budget.
func (s *Service) settleResolved(ctx context.Context, q queries, deliveryID int64, now time.Time) error {
	return q.SettleResolvedPublication(ctx, dbgen.SettleResolvedPublicationParams{OrgID: s.orgID, ID: deliveryID,
		Now: now})
}

// publicationNotes adds to the message of a first Publication its notes: the late note of an Alert Group still
// resolved, and the note of a Root message published again after a deletion. late says whether the late note is
// there.
func publicationNotes(ctx context.Context, q queries, org int64, row dbgen.GetLeasedDeliveryRow, m Message) (Message,
	bool, error) {
	late := false
	if row.LateNote && row.GroupStatus == string(groups.StatusResolved) && row.GroupResolvedAt.Valid {
		m.Sections = append(m.Sections, lateNote(row.GroupCreatedAt, row.GroupResolvedAt.Time))
		late = true
	}
	if row.RepublishedAfterDelete {
		at, err := q.GetLatestRepublishedAt(ctx, dbgen.GetLatestRepublishedAtParams{OrgID: org,
			AlertGroupID: row.AlertGroupID, DestinationID: row.DestinationID})
		if err != nil {
			return m, false, fmt.Errorf("read when the root message was deleted: %w", err)
		}
		if at.Year() > 1 {
			m.Sections = append(m.Sections, deletedNote(at))
		}
	}
	return m, late, nil
}

// possibleDuplicate records, before a Publication whose earlier start was never recorded as taken or refused, that
// the messenger may show it twice (C-11.FR-12): possible_duplicate, the delivery event and, once committed, the
// delivery_possible_duplicate line. The Publication goes ahead: never skipped, never searched for in the messenger.
func (w *Worker) possibleDuplicate(ctx context.Context, q queries, org int64, row dbgen.GetLeasedDeliveryRow,
	now time.Time, logs *after) error {
	if err := q.MarkPossibleDuplicate(ctx, dbgen.MarkPossibleDuplicateParams{OrgID: org, ID: row.ID,
		Owner: w.Lease.Owner, Now: now}); err != nil {
		return fmt.Errorf("mark the possible duplicate: %w", err)
	}
	if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: row.DestinationID, AlertGroupID: groupOf(row),
		StormID: int8Of(row.StormID), Kind: EventPossibleDuplicate}); err != nil {
		return err
	}
	destination, group := row.DestinationPublicID, row.AlertGroupPublicID
	logs.add(func(ctx context.Context, log *logging.Logger) {
		log.Log(ctx, logging.DeliveryPossibleDuplicate, logging.F("destination", destination),
			logging.F("group", group))
	})
	return nil
}

// rootOf is a delivery whose Root message the messenger reports deleted: its Alert Group, or the Storm of a Storm
// summary, whether the Alert Group is resolved, and whether the Root message was published again once already.
type rootOf struct {
	delivery, destination int64
	group, storm          *int64
	resolved, republished bool
}

// rootGone is the deleted Root message flow (C-11.FR-13): the Root message of an open Alert Group is published again
// once, Quietly, with the note and a new Thread, its pending Thread replies waiting for it; a Root message published
// again already, or one of a resolved Alert Group, marks the pair deleted in the messenger, which is left alone and
// whose pending Thread replies are dropped.
func rootGone(ctx context.Context, q queries, org int64, r rootOf, now time.Time) error {
	if !r.resolved && !r.republished {
		n, err := q.ResetForRepublish(ctx, dbgen.ResetForRepublishParams{OrgID: org, ID: r.delivery, Now: now})
		if err != nil {
			return fmt.Errorf("publish the deleted root message again: %w", err)
		}
		if n == 0 {
			return nil
		}
		if err := RecordEvent(ctx, q, org, Event{At: now, DestinationID: r.destination, AlertGroupID: r.group,
			StormID: r.storm, Kind: EventRepublished,
			Loudness: deliveryEvent(DeliveryRepublication).Loudness}); err != nil {
			return err
		}
		return q.NotifyDelivery(ctx, Channel)
	}
	n, err := q.MarkDeletedInMessenger(ctx, dbgen.MarkDeletedInMessengerParams{OrgID: org, ID: r.delivery, Now: now})
	if err != nil {
		return fmt.Errorf("mark the root message deleted in the messenger: %w", err)
	}
	if n == 0 {
		return nil
	}
	if err := q.DropPendingReplies(ctx, dbgen.DropPendingRepliesParams{OrgID: org,
		DeliveryID: r.delivery}); err != nil {
		return fmt.Errorf("drop the thread replies of the deleted root message: %w", err)
	}
	return RecordEvent(ctx, q, org, Event{At: now, DestinationID: r.destination, AlertGroupID: r.group,
		StormID: r.storm, Kind: EventDeletedInMessenger})
}
