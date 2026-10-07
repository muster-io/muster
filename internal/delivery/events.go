// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/publicid"
)

// EventKind is the kind of a delivery event (C-11.FR-21, DeliveryEventKind): what happened while delivering to a
// Destination. Delivery events are not lifecycle events: they reach neither messengers nor outgoing webhook events.
type EventKind string

// The kinds of delivery events; S-042 records EventThreadNotAttached, delivery the others.
const (
	EventPublication          EventKind = "publication"
	EventPossibleDuplicate    EventKind = "possible_duplicate"
	EventNotDelivered         EventKind = "not_delivered"
	EventDeliveredLate        EventKind = "delivered_late"
	EventDeletedInMessenger   EventKind = "deleted_in_messenger"
	EventRepublished          EventKind = "republished"
	EventThreadNotAttached    EventKind = "thread_not_attached"
	EventMarkupRejected       EventKind = "markup_rejected"
	EventDestinationBroken    EventKind = "destination_broken"
	EventDestinationRecovered EventKind = "destination_recovered"
	EventStormSummary         EventKind = "storm_summary"
	EventFinalEdit            EventKind = "final_edit"
)

// Event is one delivery event: when it happened, its Destination and, when it concerns one, its Alert Group or the
// Storm of a Storm summary; its loudness and symbolic Mentions; for a failure its error class and the provider's masked
// error text; and details.
type Event struct {
	At            time.Time
	DestinationID int64
	AlertGroupID  *int64
	StormID       *int64
	Kind          EventKind
	Loudness      groups.Loudness
	Mentions      []groups.Mention
	ErrorClass    string
	Error         outbound.Untrusted
	Detail        map[string]any
}

// RecordEvent writes one delivery event through q, in the transaction of what it records. Only delivery writes
// delivery events, never into the Alert Group tables; the Timeline of an Alert Group merges them in by time.
func RecordEvent(ctx context.Context, q queries, orgID int64, e Event) error {
	mentions := make([]string, len(e.Mentions))
	for i, m := range e.Mentions {
		mentions[i] = string(m)
	}
	loudness := e.Loudness
	if loudness == "" {
		loudness = groups.Quiet
	}
	var detail []byte
	if e.Detail != nil {
		var err error
		if detail, err = json.Marshal(e.Detail); err != nil {
			return fmt.Errorf("encode the details of the %s delivery event: %w", e.Kind, err)
		}
	}
	if err := q.InsertDeliveryEvent(ctx, dbgen.InsertDeliveryEventParams{OrgID: orgID,
		PublicID: publicid.New(publicid.DeliveryEvent), OccurredAt: e.At.UTC(), DestinationID: e.DestinationID,
		AlertGroupID: nullInt(e.AlertGroupID), StormID: nullInt(e.StormID), Kind: string(e.Kind), Loudness: string(loudness), Mentions: mentions,
		ErrorClass: nonEmpty(e.ErrorClass), Error: nonEmpty(string(e.Error)), Detail: detail}); err != nil {
		return fmt.Errorf("record the %s delivery event: %w", e.Kind, err)
	}
	return nil
}
