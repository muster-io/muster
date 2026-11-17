// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/notificationrules"
	"github.com/muster-io/muster/internal/schedules"
)

// The Pages of the steps of runs by Mention (C-32.FR-3, FR-8, FR-9; C-31.FR-3). A page_on_call step Pages the On-call
// of its Schedule at the business clock's now, a page_people step each person it names who may still be Paged, each by
// a run of their Notification rule of the step's importance; the first Mentions of the rules that come due at once are
// one Page of the step naming them all. A page_team step Pages the Team it names — the owning Team its chain had when
// its run started, by default, else the first Team that named the chain — by its Team mention, or everyone in the chat
// by the token, in those of its chain's Destinations whose Connection can mention it, and is skipped with
// no_mention_possible when none can. Each Page is a groups.StepPage, which the dispatcher records as paged and fans out
// into Thread replies. A call step and a rule's call actions make no call until S-157.

// RuleRuns are the runs of the Notification rules: (*notificationrules.Runs).
type RuleRuns interface {
	Start(ctx context.Context, tx notificationrules.DBTX, s notificationrules.Start) (notificationrules.Started, error)
	StopForGroup(ctx context.Context, tx notificationrules.DBTX, groupID int64, at time.Time) error
	Group(ctx context.Context, tx notificationrules.DBTX, id int64) (int64, bool, error)
	Advance(ctx context.Context, tx notificationrules.DBTX, id int64, deadline time.Time, firing bool) (
		*notificationrules.Due, error)
}

// Pages makes the Pages of the steps (StepPages).
type Pages struct {
	onCalls OnCalls
	rules   RuleRuns
	targets groups.PageTargets
	clock   clock.Clock
}

// PagesConfig is what the Pages need: the On-call of Schedules, the runs of the Notification rules, where a Page can
// go — delivery's PageTargets, for a page_team step — and the business clock.
type PagesConfig struct {
	OnCalls  OnCalls
	Rules    RuleRuns
	Targets  groups.PageTargets
	Business clock.Clock
}

// NewPages returns the Pages of cfg.
func NewPages(cfg PagesConfig) *Pages {
	return &Pages{onCalls: cfg.OnCalls, rules: cfg.Rules, targets: cfg.Targets, clock: cfg.Business}
}

// pageSources are the sources of a run as the Pages name them.
func pageSources(run RunRef) []groups.PageSource {
	out := make([]groups.PageSource, len(run.Sources))
	for i, s := range run.Sources {
		out[i] = groups.PageSource{Kind: s.Kind, TeamID: s.TeamID}
	}
	return out
}

// MakeStep makes the step of the run on g, locked in the dispatcher's transaction tx, with the people it may Page
// (StepPages): the Page of a page_on_call, page_people or page_team step, or the reason to skip a page_team step
// that no Destination of its chain can carry.
func (p *Pages) MakeStep(ctx context.Context, tx groups.DBTX, g *groups.Group, run RunRef, step StepRef,
	people []int64) (StepMade, error) {
	page := groups.StepPage{ChainEntry: groups.ChainEntry{Run: run.ID, Chain: run.ChainID,
		ChainPublicID: run.ChainPublicID, ChainName: run.ChainName, Step: step.Number}, Sources: pageSources(run),
		OwningTeam: run.OwningTeamID}
	switch step.Kind {
	case KindPageTeam:
		page.Team, page.Everyone = step.TeamID, step.Everyone
		if page.Team == 0 && page.Everyone == "" {
			page.Team = page.OwningTeam
		}
		if page.Team == 0 && page.Everyone == "" {
			for _, s := range page.Sources {
				if s.Kind == groups.PageSourceTeam {
					page.Team = s.TeamID
					break
				}
			}
		}
		if page.Team == 0 && page.Everyone == "" {
			return StepMade{Skip: groups.SkipNoMentionPossible}, nil
		}
		if p.targets != nil {
			targets, err := p.targets.PageTargets(ctx, tx, g, groups.PageRequest{Sources: page.Sources,
				Team: page.Team, Everyone: page.Everyone})
			if err != nil {
				return StepMade{}, err
			}
			if len(targets) == 0 {
				return StepMade{Skip: groups.SkipNoMentionPossible}, nil
			}
		}
		return StepMade{Pages: []groups.StepPage{page}}, nil
	case KindPageOnCall:
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
	case KindPagePeople:
	default:
		return StepMade{}, nil
	}
	if len(people) == 0 || p.rules == nil {
		return StepMade{}, nil
	}
	started, err := p.rules.Start(ctx, tx, notificationrules.Start{Group: g.ID, Run: run.ID, Step: step.Number,
		Importance: step.Importance, Users: people})
	if err != nil {
		return StepMade{}, err
	}
	if len(started.Mentioned) == 0 {
		return StepMade{}, nil
	}
	page.People, page.Importance, page.AgainSeconds = started.Mentioned, step.Importance, started.AgainSeconds
	return StepMade{Pages: []groups.StepPage{page}}, nil
}

// StopRules stops the rule runs of the Alert Group groupID that still run (StepPages): its chains stopped or started
// over.
func (p *Pages) StopRules(ctx context.Context, tx groups.DBTX, groupID int64, at time.Time) error {
	if p.rules == nil {
		return nil
	}
	return p.rules.StopForGroup(ctx, tx, groupID, at)
}

// RuleDispatcher is the dispatcher through which the action of a rule run changes its Alert Group:
// (*groups.Service).ChainStep.
type RuleDispatcher interface {
	ChainStep(ctx context.Context, tx groups.DBTX, groupID int64,
		step func(ctx context.Context, g *groups.Group) (groups.ChainProgress, error)) (func(context.Context), error)
}

// FireRule is the handler of the timer of a rule run, notification_rule_action, claimed with its deadline (C-31.FR-3):
// it reads the rule run by the timer's notification_rule_run_id, takes its Alert Group through it and locks it
// through the dispatcher (ChainStep), then lets the rule run make its actions due (notificationrules.Advance), which
// does nothing for a rule run that no longer runs or whose timer moved, and stops one whose Alert Group is no longer
// firing. A Mention it made is a Page of the step recorded as paged, "again" when the rule run Mentioned the person
// before. It returns what to run once tx committed.
func (p *Pages) FireRule(ctx context.Context, d RuleDispatcher, tx groups.DBTX, id int64, deadline time.Time) (
	func(context.Context), error) {
	if p.rules == nil {
		return nil, nil
	}
	groupID, ok, err := p.rules.Group(ctx, tx, id)
	if err != nil || !ok {
		return nil, err
	}
	return d.ChainStep(ctx, tx, groupID, func(ctx context.Context, g *groups.Group) (groups.ChainProgress, error) {
		due, err := p.rules.Advance(ctx, tx, id, deadline, g.Status == groups.StatusFiring)
		if err != nil || due == nil {
			return groups.ChainProgress{}, err
		}
		page := groups.StepPage{ChainEntry: groups.ChainEntry{Run: due.Run, Chain: due.Chain,
			ChainPublicID: due.ChainPublicID, ChainName: due.ChainName, Step: due.Step}, OwningTeam: due.OwningTeam,
			People: []int64{due.User}, Importance: due.Importance, Schedule: due.Schedule, Again: due.Again,
			RuleRun: due.RuleRun}
		for _, s := range due.Sources {
			page.Sources = append(page.Sources, groups.PageSource{Kind: s.Kind, TeamID: s.TeamID})
		}
		return groups.ChainProgress{Pages: []groups.StepPage{page}, Changed: true}, nil
	})
}
