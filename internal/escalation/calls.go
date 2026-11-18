// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/muster-io/muster/internal/gateways"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/notificationrules"
	"github.com/muster-io/muster/internal/schedules"
)

// The Pages by a call through a Notification gateway (C-32.FR-3, FR-10, FR-14, C-31.FR-3, C-30.FR-4). A call step
// calls the On-call of its Schedule at the business clock's now, or each person it names who may still be Paged,
// through its gateway; a call action of a Notification rule calls its person through the action's gateway. Each call
// is a Page of one person, a groups.StepPage with its StepCall, which the dispatcher records as the Quiet paged, writing
// nothing in any Thread, and then queues as a call of the queue of S-148 through gateways, where it joins the waiting
// call of that person through that gateway (combining, gateway.combine_window), with its stop check: the run of its
// chain still running in the same start of the chains, or ran out, for a call step; also the rule run not stopped,
// for a call action. A call depends on no Root message and no messenger. A step whose gateway was deleted, or is no
// Notification gateway any more, is skipped with gateway_deleted; a person without a confirmed contact there is
// skipped with no_confirmed_contact, and the step with them when none is left; a call action skips its person with
// either reason. The chain goes on with its next step at once.

// Calls queues the calls of Pages through Notification gateways, in the dispatcher's transaction:
// (*gateways.Calls).
type Calls interface {
	Target(ctx context.Context, tx gateways.DBTX, gatewayID int64, gatewayPublicID string, users []int64) (
		gateways.CallTarget, error)
	Queue(ctx context.Context, tx gateways.DBTX, p gateways.PageCall) (int64, func(context.Context), error)
}

// callStep makes the call step of page on g: its people, the On-call of its Schedule or those it names who may still
// be Paged, each called through its gateway.
func (p *Pages) callStep(ctx context.Context, tx groups.DBTX, g *groups.Group, page groups.StepPage, step StepRef,
	people []int64) (StepMade, error) {
	if step.CallTarget == CallOnCall || step.Schedule != "" {
		seg, err := p.onCalls.OnCallAtIn(ctx, tx, step.Schedule, p.clock.Now().UTC())
		switch {
		case errors.Is(err, schedules.ErrNotFound):
			return StepMade{Skip: groups.SkipScheduleGap}, nil
		case err != nil:
			return StepMade{}, fmt.Errorf("read the on-call of schedule %s: %w", step.Schedule, err)
		case seg.User == nil:
			return StepMade{Skip: groups.SkipScheduleGap}, nil
		}
		people = []int64{seg.User.ID}
		page.Schedule = step.ScheduleID
	}
	if len(people) == 0 || p.calls == nil {
		return StepMade{}, nil
	}
	t, err := p.calls.Target(ctx, tx, step.GatewayID, "", people)
	if err != nil {
		return StepMade{}, err
	}
	if !t.Live {
		return StepMade{Skip: groups.SkipGatewayDeleted}, nil
	}
	var made StepMade
	p.callPages(g, page, t, people, 0, &made)
	if len(made.Pages) == 0 {
		made.Skip = groups.SkipNoConfirmedContact
	}
	return made, nil
}

// ruleCall makes the call action c of a rule run, a Page of the step page, through the action's gateway, or skips
// its person with gateway_deleted or no_confirmed_contact.
func (p *Pages) ruleCall(ctx context.Context, tx groups.DBTX, g *groups.Group, page groups.StepPage,
	c notificationrules.Call, made *StepMade) error {
	if p.calls == nil {
		return nil
	}
	t, err := p.calls.Target(ctx, tx, 0, c.Gateway, []int64{c.User})
	if err != nil {
		return err
	}
	// A call is no Mention: it says no "again". Its rule run names the run step it belongs to (C-32.FR-12).
	page.AgainSeconds, page.Again = 0, false
	if !t.Live {
		made.Skips = append(made.Skips, groups.StepSkip{ChainEntry: page.ChainEntry, Reason: groups.SkipGatewayDeleted,
			Person: &groups.PagedUser{ID: c.User}})
		return nil
	}
	page.RuleRun = c.RuleRun
	p.callPages(g, page, t, []int64{c.User}, c.RuleRun, made)
	return nil
}

// callPages adds to made a Page of page by a call through the gateway of t for each of people with a confirmed
// contact there, and a skip with no_confirmed_contact for each without one; ruleRun is the rule run of a call action,
// 0 for a call step.
func (p *Pages) callPages(g *groups.Group, page groups.StepPage, t gateways.CallTarget, people []int64, ruleRun int64,
	made *StepMade) {
	for _, u := range people {
		if !slices.Contains(t.Callable, u) {
			made.Skips = append(made.Skips, groups.StepSkip{ChainEntry: page.ChainEntry,
				Reason: groups.SkipNoConfirmedContact, Person: &groups.PagedUser{ID: u}})
			continue
		}
		call := page
		call.People = []int64{u}
		call.Call = &groups.StepCall{Gateway: t.Gateway, Queue: p.queue(g.ID, u, t.Gateway.ID, page.Run, ruleRun)}
		made.Pages = append(made.Pages, call)
	}
}

// queue is the StepCall.Queue of a call of the person user through the gateway on the Alert Group group, with the stop
// check of the run run — and of the rule run ruleRun for a call action.
func (p *Pages) queue(group, user, gateway, run, ruleRun int64) func(context.Context, groups.DBTX, int64) (int64,
	func(context.Context), error) {
	stop := gateways.StopChainRun
	if ruleRun != 0 {
		stop = gateways.StopRuleRun
	}
	return func(ctx context.Context, tx groups.DBTX, page int64) (int64, func(context.Context), error) {
		return p.calls.Queue(ctx, tx, gateways.PageCall{UserID: user, GatewayID: gateway, AlertGroupID: group,
			Stop: stop, Run: run, RuleRun: ruleRun, Page: page})
	}
}
