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
	"github.com/muster-io/muster/internal/gateways"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/notificationrules"
)

// The Notification gateway of the call tests, by internal id and public_id.
const (
	testGateway       = 40
	testGatewayPublic = "CN00000000GW40"
)

// fakeGatewayCalls is the queue of calls: the gateway testGateway, live unless deleted, at which the people of
// contacts have a confirmed contact, and the calls it queued.
type fakeGatewayCalls struct {
	deleted  bool
	contacts []int64
	targets  []string
	queued   []gateways.PageCall
	err      error
}

func (f *fakeGatewayCalls) Target(_ context.Context, _ gateways.DBTX, gatewayID int64, gatewayPublicID string,
	users []int64) (gateways.CallTarget, error) {
	f.targets = append(f.targets, gatewayPublicID)
	if f.err != nil {
		return gateways.CallTarget{}, f.err
	}
	if f.deleted || (gatewayID != testGateway && gatewayPublicID != testGatewayPublic) {
		return gateways.CallTarget{}, nil
	}
	t := gateways.CallTarget{Gateway: groups.PageGateway{ID: testGateway, PublicID: testGatewayPublic,
		Name: "Example Gateway"}, Live: true}
	for _, u := range users {
		if slices.Contains(f.contacts, u) {
			t.Callable = append(t.Callable, u)
		}
	}
	return t, nil
}

func (f *fakeGatewayCalls) Queue(_ context.Context, _ gateways.DBTX, p gateways.PageCall) (int64,
	func(context.Context), error) {
	f.queued = append(f.queued, p)
	return 900, nil, f.err
}

// queueAll queues the calls of the Pages as the dispatcher does, each as the pages row 77 and on.
func queueAll(t *testing.T, pages []groups.StepPage) {
	t.Helper()
	for i, p := range pages {
		if p.Call == nil {
			continue
		}
		if call, _, err := p.Call.Queue(t.Context(), nil, int64(77+i)); err != nil || call != 900 {
			t.Fatalf("queue %d: %d %v", i, call, err)
		}
	}
}

// TestCallStep is C-32.FR-3, FR-14, C-32.AC-7 and C-30.FR-4 for a step "Call through a Notification gateway": it
// calls the On-call of its Schedule now, or each person it names, as one Page of the kind call per person through its
// gateway, with the stop check of the run; a person without a confirmed contact there is skipped with
// no_confirmed_contact, and the step with them when none is left; a deleted gateway skips the step with
// gateway_deleted; a gap of the Schedule with schedule_gap. Without the queue nobody is called.
func TestCallStep(t *testing.T) {
	calls := &fakeGatewayCalls{contacts: []int64{21}}
	onCalls := &fakeOnCalls{onCall: map[string]int64{schedMain: 21, "SC00000000GAP1": 0}}
	business := clock.NewManual(t0.Add(17 * time.Minute))
	p := NewPages(PagesConfig{OnCalls: onCalls, Calls: calls, Business: business})
	g := &groups.Group{ID: 9}
	onCall := StepRef{Seq: 3, Number: 2, Kind: KindCall, CallTarget: CallOnCall, ScheduleID: 11, Schedule: schedMain,
		GatewayID: testGateway}
	made, err := p.MakeStep(t.Context(), nil, g, pageRun, onCall, nil)
	if err != nil || made.Skip != "" || len(made.Skips) != 0 || len(made.Pages) != 1 {
		t.Fatalf("the on-call %+v %v", made, err)
	}
	// The On-call of the moment: the business clock's now.
	if len(onCalls.asked) != 1 || !onCalls.asked[0].Equal(business.Now()) {
		t.Errorf("the on-call asked at %v", onCalls.asked)
	}
	pg := made.Pages[0]
	if !slices.Equal(pg.People, []int64{21}) || pg.Schedule != 11 || pg.RunStepSeq != 3 || pg.Step != 2 ||
		pg.Run != 501 || pg.RuleRun != 0 || pg.Importance != "" || pg.Call == nil ||
		pg.Call.Gateway != (groups.PageGateway{ID: testGateway, PublicID: testGatewayPublic, Name: "Example Gateway"}) {
		t.Fatalf("the call %+v", pg)
	}
	queueAll(t, made.Pages)
	if q := calls.queued[0]; q != (gateways.PageCall{UserID: 21, GatewayID: testGateway, AlertGroupID: 9,
		Stop: gateways.StopChainRun, Run: 501, Page: 77}) {
		t.Errorf("queued %+v", q)
	}

	people := StepRef{Seq: 4, Number: 3, Kind: KindCall, CallTarget: CallPeople, GatewayID: testGateway}
	made, err = p.MakeStep(t.Context(), nil, g, pageRun, people, []int64{21, 22})
	if err != nil || made.Skip != "" || len(made.Pages) != 1 || !slices.Equal(made.Pages[0].People, []int64{21}) ||
		len(made.Skips) != 1 || made.Skips[0].Reason != groups.SkipNoConfirmedContact ||
		made.Skips[0].Person.ID != 22 || made.Skips[0].Step != 3 {
		t.Errorf("people %+v %v", made, err)
	}
	calls.contacts = nil
	made, err = p.MakeStep(t.Context(), nil, g, pageRun, people, []int64{21, 22})
	if err != nil || made.Skip != groups.SkipNoConfirmedContact || len(made.Pages) != 0 || len(made.Skips) != 2 {
		t.Errorf("nobody with a contact %+v %v", made, err)
	}
	calls.deleted = true
	if made, err := p.MakeStep(t.Context(), nil, g, pageRun, people, []int64{21}); err != nil ||
		made.Skip != groups.SkipGatewayDeleted || len(made.Skips) != 0 {
		t.Errorf("a deleted gateway %+v %v", made, err)
	}
	gap := onCall
	gap.Schedule = "SC00000000GAP1"
	for _, s := range []string{"SC00000000GAP1", "SC00000000NONE"} {
		gap.Schedule = s
		if made, err := p.MakeStep(t.Context(), nil, g, pageRun, gap, nil); err != nil ||
			made.Skip != groups.SkipScheduleGap {
			t.Errorf("%s: %+v %v", s, made, err)
		}
	}
	calls.deleted, calls.err = false, errors.New("boom")
	if _, err := p.MakeStep(t.Context(), nil, g, pageRun, people, []int64{21}); err == nil {
		t.Error("a failing target: no error")
	}
	if _, err := NewPages(PagesConfig{OnCalls: failingOnCalls{}, Calls: calls, Business: clock.NewManual(t0)}).
		MakeStep(t.Context(), nil, g, pageRun, onCall, nil); err == nil {
		t.Error("a failing on-call: no error")
	}
	if made, err := NewPages(PagesConfig{Business: clock.NewManual(t0)}).MakeStep(t.Context(), nil, g, pageRun,
		people, []int64{21}); err != nil || len(made.Pages) != 0 || made.Skip != "" {
		t.Errorf("without the queue %+v %v", made, err)
	}
}

// TestRuleCalls is C-31.FR-3, C-31.AC-2, C-31.AC-6 and C-30.FR-9 for the call actions of Notification rules: an
// important Page of a person whose rule calls at once Mentions them and calls them, each a Page of the step, the call
// with the stop check of the rule run; a call action whose gateway is gone skips its person with gateway_deleted, one
// of a person without a confirmed contact with no_confirmed_contact, and the Mentions are made all the same; a call
// after a wait is a Page of the rule run's step, with or without a Mention.
func TestRuleCalls(t *testing.T) {
	calls := &fakeGatewayCalls{contacts: []int64{21}}
	rules := &fakeRuleRuns{calls: func(s notificationrules.Start) []notificationrules.Call {
		var out []notificationrules.Call
		for i, u := range s.Users {
			out = append(out, notificationrules.Call{User: u, RuleRun: int64(70 + i), Gateway: testGatewayPublic})
		}
		return out
	}}
	p := NewPages(PagesConfig{OnCalls: &fakeOnCalls{onCall: map[string]int64{schedMain: 21}}, Rules: rules,
		Calls: calls, Business: clock.NewManual(t0)})
	g := &groups.Group{ID: 9, Status: groups.StatusFiring}
	made, err := p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Seq: 5, Number: 3, Kind: KindPageOnCall,
		Importance: ImportanceImportant, ScheduleID: 11, Schedule: schedMain}, nil)
	if err != nil || len(made.Pages) != 2 || len(made.Skips) != 0 {
		t.Fatalf("an important page with a call %+v %v", made, err)
	}
	mention, call := made.Pages[0], made.Pages[1]
	if mention.Call != nil || !slices.Equal(mention.People, []int64{21}) || call.Call == nil ||
		!slices.Equal(call.People, []int64{21}) || call.RuleRun != 70 || call.Importance != ImportanceImportant ||
		call.Schedule != 11 || call.RunStepSeq != 5 || call.AgainSeconds != 0 {
		t.Fatalf("the pages %+v", made.Pages)
	}
	queueAll(t, made.Pages)
	if q := calls.queued[0]; q.Stop != gateways.StopRuleRun || q.RuleRun != 70 || q.Run != 501 || q.Page != 78 ||
		q.UserID != 21 || q.GatewayID != testGateway {
		t.Errorf("queued %+v", q)
	}
	// ivan has no confirmed contact: the Mention of both, the call of petr only.
	made, err = p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Seq: 6, Number: 4, Kind: KindPagePeople,
		Importance: ImportanceImportant}, []int64{21, 22})
	if err != nil || len(made.Pages) != 2 || !slices.Equal(made.Pages[0].People, []int64{21, 22}) ||
		len(made.Skips) != 1 || made.Skips[0].Reason != groups.SkipNoConfirmedContact || made.Skips[0].Person.ID != 22 {
		t.Errorf("without a contact %+v %v", made, err)
	}
	calls.deleted = true
	made, err = p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Number: 4, Kind: KindPagePeople,
		Importance: ImportanceImportant}, []int64{21})
	if err != nil || len(made.Pages) != 1 || made.Pages[0].Call != nil || len(made.Skips) != 1 ||
		made.Skips[0].Reason != groups.SkipGatewayDeleted || made.Skips[0].Person.ID != 21 || made.Skip != "" {
		t.Errorf("a deleted gateway %+v %v", made, err)
	}
	calls.deleted = false

	// A call after a wait, without a Mention, and with one.
	rules.group = 9
	rules.due = &notificationrules.Due{RuleRun: 70, User: 21, Run: 501, Chain: 1, ChainPublicID: "EC000000000001",
		Step: 3, Importance: ImportanceImportant, Calls: []string{testGatewayPublic}}
	d := &fakeDispatcher{group: g}
	if _, err := p.FireRule(t.Context(), d, nil, 70, t0); err != nil {
		t.Fatal(err)
	}
	if pg := d.progress[0].Pages; len(pg) != 1 || pg[0].Call == nil || pg[0].RuleRun != 70 || pg[0].Step != 3 {
		t.Errorf("a call after a wait %+v", d.progress)
	}
	rules.due.Mention = true
	calls.contacts = nil
	if _, err := p.FireRule(t.Context(), d, nil, 70, t0); err != nil {
		t.Fatal(err)
	}
	if pr := d.progress[1]; len(pr.Pages) != 1 || pr.Pages[0].Call != nil || len(pr.Skips) != 1 ||
		pr.Skips[0].Reason != groups.SkipNoConfirmedContact {
		t.Errorf("a Mention and a call without a contact %+v", pr)
	}
	calls.err = errors.New("boom")
	if _, err := p.FireRule(t.Context(), d, nil, 70, t0); err == nil {
		t.Error("a failing target after a wait: no error")
	}
	if _, err := p.MakeStep(t.Context(), nil, g, pageRun, StepRef{Number: 4, Kind: KindPagePeople,
		Importance: ImportanceImportant}, []int64{21}); err == nil {
		t.Error("a failing target: no error")
	}
	if made, err := NewPages(PagesConfig{Rules: rules, Business: clock.NewManual(t0)}).MakeStep(t.Context(), nil, g,
		pageRun, StepRef{Number: 4, Kind: KindPagePeople, Importance: ImportanceImportant}, []int64{21}); err != nil ||
		len(made.Pages) != 1 || made.Pages[0].Call != nil {
		t.Errorf("without the queue %+v %v", made, err)
	}
}
