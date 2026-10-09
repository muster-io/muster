// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/delivery/dbgen"
)

// recoveryQueries are the queries of the recovery of a Destination.
type recoveryQueries interface {
	DropDueReplies(ctx context.Context, arg dbgen.DropDueRepliesParams) error
	ResetDestinationBudgets(ctx context.Context, arg dbgen.ResetDestinationBudgetsParams) error
	WithholdResolvedUnpublished(ctx context.Context, arg dbgen.WithholdResolvedUnpublishedParams) error
	RecoverUnpublished(ctx context.Context, arg dbgen.RecoverUnpublishedParams) error
	RecoverPublished(ctx context.Context, arg dbgen.RecoverPublishedParams) error
	ResetWebhookEventBudgets(ctx context.Context, arg dbgen.ResetWebhookEventBudgetsParams) error
}

// recoverDestination is the recovery of a Destination that is healthy again, in one pass and in the transaction that
// ended its Broken state (C-11.FR-19): reconciliation sends only the current state. Thread replies that came due while
// it was Broken are dropped; the waiting deliveries and Thread replies get a fresh Transient budget, which breaking it
// left alone (breakDestination); an Alert Group never published there that resolved meanwhile is withheld; one never
// published and still open is published now, Urgent first by the claim, Loud with new_alert_group when it is firing
// and Quiet otherwise; a published Root message gets one edit to its current state. Deliveries a Storm holds wait for
// their Storm. The waiting events of an outgoing webhook get a fresh Transient budget too and go out in order, none
// dropped (C-11.FR-19).
func recoverDestination(ctx context.Context, q queries, org, id int64, now time.Time) error {
	if err := q.DropDueReplies(ctx, dbgen.DropDueRepliesParams{OrgID: org, DestinationID: id, Now: now}); err != nil {
		return fmt.Errorf("drop the thread replies that came due: %w", err)
	}
	if err := q.ResetDestinationBudgets(ctx, dbgen.ResetDestinationBudgetsParams{OrgID: org,
		DestinationID: id}); err != nil {
		return fmt.Errorf("reset the transient budgets: %w", err)
	}
	if err := q.WithholdResolvedUnpublished(ctx, dbgen.WithholdResolvedUnpublishedParams{OrgID: org,
		DestinationID: id, Now: now}); err != nil {
		return fmt.Errorf("withhold the resolved alert groups never published: %w", err)
	}
	if err := q.RecoverUnpublished(ctx, dbgen.RecoverUnpublishedParams{OrgID: org, DestinationID: id,
		Now: now}); err != nil {
		return fmt.Errorf("publish the open alert groups: %w", err)
	}
	if err := q.RecoverPublished(ctx, dbgen.RecoverPublishedParams{OrgID: org, DestinationID: id,
		Now: now}); err != nil {
		return fmt.Errorf("edit the published root messages: %w", err)
	}
	if err := q.ResetWebhookEventBudgets(ctx, dbgen.ResetWebhookEventBudgetsParams{OrgID: org,
		DestinationID: id, Now: now}); err != nil {
		return fmt.Errorf("reset the transient budgets of the webhook events: %w", err)
	}
	return nil
}
