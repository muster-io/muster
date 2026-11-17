// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/groups"
)

// The step "Send to an outgoing webhook" (C-32.FR-13, FR-14): once the run decided to make it, as for every step that
// Pages or sends — its Severity condition met and, when limited to them, inside the Working hours of its chain — it
// reads its Destination through GetStepDestination, without a lock (Lock order of runs). A Destination that was
// deleted since the step was saved skips the step with destination_deleted, one that is no longer an outgoing webhook
// in the mode events or both with webhook_events_required; otherwise the step hands its webhook to ChainStep in its
// ChainProgress, which records the Quiet escalation_webhook through the system transition chainSteps, and delivery
// queues it for that Destination only. A Destination deleted while the step runs gets no event (the insert skips a
// deleted Destination), or at worst the escalation_webhook events of that one firing — several, when repeats make the
// step again without a wait — stay pending and are never claimed; one switched to the template mode afterwards ends a
// queued event as Not delivered when it is sent. Neither delivery nor webhooks is imported: the
// event reaches them as every lifecycle event does, through the re-render step of the dispatcher.

// webhook makes or skips a webhook step st: the skip or the webhook to record, one of them.
func (w *walk) webhook(ctx context.Context, st runStep) (*groups.StepSkip, *groups.StepWebhook, error) {
	reason, err := w.skipReason(ctx, st)
	if err != nil {
		return nil, nil, err
	}
	var dest dbgen.GetStepDestinationRow
	if reason == "" {
		dest, reason, err = w.stepDestination(ctx, st)
		if err != nil {
			return nil, nil, err
		}
	}
	if reason != "" {
		return &groups.StepSkip{ChainEntry: w.entry(st), Reason: reason}, nil,
			w.record(ctx, st, OutcomeSkipped, reason)
	}
	hook := &groups.StepWebhook{ChainEntry: w.entry(st), Destination: groups.JoinedDestination{ID: dest.ID,
		PublicID: dest.PublicID, Name: dest.Name, Type: dest.Type}}
	return nil, hook, w.record(ctx, st, OutcomeMade, "")
}

// stepDestination reads the Destination of a webhook step, and the reason to skip the step when it is gone
// or takes no events.
func (w *walk) stepDestination(ctx context.Context, st runStep) (dbgen.GetStepDestinationRow, string, error) {
	d, err := w.q.GetStepDestination(ctx, dbgen.GetStepDestinationParams{OrgID: w.r.s.orgID,
		ID: st.DestinationID})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return d, groups.SkipDestinationDeleted, nil
	case err != nil:
		return d, "", fmt.Errorf("read the destination of step %d of run %s: %w", st.Number, w.run.PublicID, err)
	case d.Deleted:
		return d, groups.SkipDestinationDeleted, nil
	case d.Type != typeWebhook || !d.WebhookMode.Valid || d.WebhookMode.String == modeTemplate:
		// The check of a step when it is saved (assign): an outgoing webhook in a mode other than template.
		return d, groups.SkipWebhookEventsRequired, nil
	}
	return d, "", nil
}
