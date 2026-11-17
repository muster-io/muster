// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/notificationrules"
)

// fakeRuleRuns are the runs of the Notification rules: the people whose first action is a Mention, and the later
// Mention Advance makes.
type fakeRuleRuns struct {
	starts    []notificationrules.Start
	mentioned func(ids []int64) []int64
	again     int
	stopped   []int64
	group     int64
	due       *notificationrules.Due
	firing    []bool
	err       error
}

func (f *fakeRuleRuns) Start(_ context.Context, _ notificationrules.DBTX, s notificationrules.Start) (
	notificationrules.Started, error) {
	f.starts = append(f.starts, s)
	if f.err != nil {
		return notificationrules.Started{}, f.err
	}
	out := notificationrules.Started{Mentioned: s.Users, AgainSeconds: f.again}
	if f.mentioned != nil {
		out.Mentioned = f.mentioned(s.Users)
	}
	return out, nil
}

func (f *fakeRuleRuns) StopForGroup(_ context.Context, _ notificationrules.DBTX, group int64, _ time.Time) error {
	f.stopped = append(f.stopped, group)
	return f.err
}

func (f *fakeRuleRuns) Group(context.Context, notificationrules.DBTX, int64) (int64, bool, error) {
	return f.group, f.group != 0, f.err
}

func (f *fakeRuleRuns) Advance(_ context.Context, _ notificationrules.DBTX, _ int64, _ time.Time, firing bool) (
	*notificationrules.Due, error) {
	f.firing = append(f.firing, firing)
	return f.due, f.err
}

// fakePageTargets are where Pages go: everywhere, or nowhere.
type fakePageTargets struct {
	none bool
	reqs []groups.PageRequest
	err  error
}

func (f *fakePageTargets) PageTargets(_ context.Context, _ groups.DBTX, _ *groups.Group, r groups.PageRequest) (
	[]groups.PageTarget, error) {
	f.reqs = append(f.reqs, r)
	if f.none {
		return nil, f.err
	}
	return []groups.PageTarget{{DestinationID: 11}}, f.err
}

var pageRun = RunRef{ID: 501, ChainID: 1, ChainPublicID: "EC000000000001", ChainName: "devops", OwningTeamID: 7,
	Sources: []RunSource{{Kind: SourceRoute}, {Kind: SourceTeam, TeamID: 3}}}

// TestMakeStep is C-32.FR-3 and C-31.FR-3: a page_on_call step Pages the On-call of its Schedule now, a page_people
// step its people, each through a run of their rule of the step's importance, and the people whose rule Mentions them
// at once are one Page of the step with the shortest wait until a next Mention; a gap of the Schedule skips the step;
// a page_team step Pages the Team it names, else the owning Team the run had, else the first Team that named the
// chain, or everyone in the chat, and is skipped with no_mention_possible where no Destination can carry it.
func TestMakeStep(t *testing.T) {
	rules := &fakeRuleRuns{again: 300, mentioned: func(ids []int64) []int64 { return ids[:1] }}
	targets := &fakePageTargets{}
	p := NewPages(PagesConfig{OnCalls: &fakeOnCalls{onCall: map[string]int64{schedMain: 21, "SC00000000GAP1": 0}},
		Rules: rules, Targets: targets, Business: clock.NewManual(t0)})
	g := &groups.Group{ID: 9}
	made, err := p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Number: 1, Kind: KindPageOnCall,
		Importance: ImportanceImportant, ScheduleID: 11, Schedule: schedMain}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(made.Pages) != 1 || !slices.Equal(made.Pages[0].People, []int64{21}) || made.Pages[0].Schedule != 11 ||
		made.Pages[0].Importance != ImportanceImportant || made.Pages[0].AgainSeconds != 300 ||
		made.Pages[0].Run != 501 || made.Pages[0].ChainPublicID != "EC000000000001" || made.Pages[0].OwningTeam != 7 ||
		len(made.Pages[0].Sources) != 2 || made.Pages[0].Sources[1].TeamID != 3 {
		t.Fatalf("on-call %+v", made)
	}
	if s := rules.starts[0]; s.Group != 9 || s.Run != 501 || s.Step != 1 || !slices.Equal(s.Users, []int64{21}) ||
		s.Importance != ImportanceImportant {
		t.Errorf("start %+v", s)
	}
	made, _ = p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Number: 2, Kind: KindPagePeople,
		Importance: ImportanceNormal}, []int64{21, 22})
	if len(made.Pages) != 1 || !slices.Equal(made.Pages[0].People, []int64{21}) || made.Pages[0].Schedule != 0 {
		t.Errorf("people %+v", made)
	}
	for _, sched := range []string{"SC00000000GAP1", "SC00000000NONE"} {
		if made, err := p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Number: 3, Kind: KindPageOnCall,
			Schedule: sched}, nil); err != nil || made.Skip != groups.SkipScheduleGap {
			t.Errorf("%s: %+v %v", sched, made, err)
		}
	}
	for _, c := range []struct {
		step     StepRef
		run      RunRef
		team     int64
		everyone string
	}{
		{StepRef{Number: 4, Kind: KindPageTeam, TeamID: 5}, pageRun, 5, ""},
		{StepRef{Number: 4, Kind: KindPageTeam}, pageRun, 7, ""},
		{StepRef{Number: 4, Kind: KindPageTeam}, RunRef{Sources: pageRun.Sources}, 3, ""},
		{StepRef{Number: 4, Kind: KindPageTeam, Everyone: "here"}, pageRun, 0, "here"},
	} {
		made, err := p.MakeStep(t.Context(), nil, g, c.run, c.step, nil)
		if err != nil || len(made.Pages) != 1 || made.Pages[0].Team != c.team || made.Pages[0].Everyone != c.everyone {
			t.Errorf("%+v: %+v %v", c.step, made, err)
		}
	}
	if made, _ := p.MakeStep(t.Context(), nil, g, RunRef{}, StepRef{Kind: KindPageTeam}, nil); made.Skip !=
		groups.SkipNoMentionPossible {
		t.Errorf("a Team of nobody %+v", made)
	}
	targets.none = true
	if made, _ := p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Kind: KindPageTeam, TeamID: 5}, nil); made.Skip !=
		groups.SkipNoMentionPossible {
		t.Errorf("nowhere %+v", made)
	}
	targets.none, targets.err = false, errors.New("boom")
	if _, err := p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Kind: KindPageTeam, TeamID: 5}, nil); err == nil {
		t.Error("a failing target: no error")
	}
	if made, err := p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Kind: KindCall}, []int64{21}); err != nil ||
		len(made.Pages) != 0 {
		t.Errorf("a call %+v", made)
	}
	rules.mentioned = func([]int64) []int64 { return nil }
	if made, _ := p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Kind: KindPagePeople}, []int64{21}); len(made.Pages) != 0 {
		t.Errorf("no Mention at once %+v", made)
	}
	rules.err = errors.New("boom")
	if _, err := p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Kind: KindPagePeople}, []int64{21}); err == nil {
		t.Error("a failing rule: no error")
	}
	if _, err := NewPages(PagesConfig{OnCalls: failingOnCalls{}, Business: clock.NewManual(t0)}).MakeStep(
		t.Context(), nil, g, pageRun, StepRef{Kind: KindPageOnCall, Schedule: schedMain}, nil); err == nil {
		t.Error("a failing on-call: no error")
	}
	if made, err := NewPages(PagesConfig{Business: clock.NewManual(t0)}).MakeStep(t.Context(), nil, g, pageRun,
		StepRef{Kind: KindPagePeople}, []int64{21}); err != nil || len(made.Pages) != 0 {
		t.Errorf("without rules %+v %v", made, err)
	}
}

// TestFireRule is C-31.FR-3 for the timer of a rule run: its Alert Group is locked through the dispatcher before the
// rule run advances, whether it is firing goes with it, and a later Mention is a Page of the step "again" of the
// person, in the Threads of its chain's sources; nothing else is a Page.
func TestFireRule(t *testing.T) {
	rules := &fakeRuleRuns{group: 9, due: &notificationrules.Due{RuleRun: 70, User: 21, Run: 501, Chain: 1,
		ChainPublicID: "EC000000000001", ChainName: "devops", OwningTeam: 7, Step: 3, Importance: ImportanceImportant,
		Schedule: 11, Sources: []notificationrules.Source{{Kind: SourceRoute}}, Again: true}}
	p := NewPages(PagesConfig{Rules: rules, Business: clock.NewManual(t0)})
	d := &fakeDispatcher{group: &groups.Group{ID: 9, Status: groups.StatusFiring}}
	after, err := p.FireRule(t.Context(), d, nil, 70, t0)
	if err != nil || after == nil {
		t.Fatal(err)
	}
	pg := d.progress[0].Pages
	if len(pg) != 1 || !pg[0].Again || pg[0].RuleRun != 70 || !slices.Equal(pg[0].People, []int64{21}) ||
		pg[0].Step != 3 || pg[0].Schedule != 11 || pg[0].OwningTeam != 7 || len(pg[0].Sources) != 1 ||
		!d.progress[0].Changed || !slices.Equal(rules.firing, []bool{true}) {
		t.Errorf("progress %+v", d.progress)
	}
	// The first Mention of a rule that starts with a call and a wait is no later one.
	first := *rules.due
	first.Again = false
	rules.due = &first
	if _, err := p.FireRule(t.Context(), d, nil, 70, t0); err != nil || len(d.progress[1].Pages) != 1 ||
		d.progress[1].Pages[0].Again {
		t.Errorf("a first Mention after a wait %+v %v", d.progress, err)
	}
	d.progress = d.progress[:1]
	rules.firing = rules.firing[:1]
	rules.due = nil
	d.group.Status = groups.StatusAcknowledged
	if _, err := p.FireRule(t.Context(), d, nil, 70, t0); err != nil || len(d.progress[1].Pages) != 0 ||
		rules.firing[1] {
		t.Errorf("acknowledged %+v %v", d.progress, err)
	}
	rules.group = 0
	if after, err := p.FireRule(t.Context(), d, nil, 70, t0); err != nil || after != nil {
		t.Errorf("a rule run that is gone: %v", err)
	}
	if after, err := NewPages(PagesConfig{}).FireRule(t.Context(), d, nil, 70, t0); err != nil || after != nil {
		t.Errorf("without rules: %v", err)
	}
	if err := NewPages(PagesConfig{}).StopRules(t.Context(), nil, 9, t0); err != nil {
		t.Error(err)
	}
	if err := p.StopRules(t.Context(), nil, 9, t0); err != nil || !slices.Equal(rules.stopped, []int64{9}) {
		t.Errorf("stop %v %v", err, rules.stopped)
	}
}
