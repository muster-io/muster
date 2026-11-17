// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/escalation"
	"github.com/muster-io/muster/internal/groups"
)

// fakeChainRuns stands for the runs of internal/escalation: the runs of each Alert Group by public_id, and a page of
// the runs of the chain chainID.
type fakeChainRuns struct {
	groups map[string][]escalation.GroupChain
	page   escalation.RunPage
	after  []*escalation.RunPosition
	err    error
}

func (f *fakeChainRuns) GroupChains(_ context.Context, ids []string) (map[string][]escalation.GroupChain, error) {
	out := map[string][]escalation.GroupChain{}
	for _, id := range ids {
		if c, ok := f.groups[id]; ok {
			out[id] = c
		}
	}
	return out, f.err
}

func (f *fakeChainRuns) ChainRuns(_ context.Context, id string, after *escalation.RunPosition, _ int) (
	escalation.RunPage, error) {
	f.after = append(f.after, after)
	if id != chainID {
		return escalation.RunPage{}, escalation.ErrNotFound
	}
	return f.page, f.err
}

func newFakeChainRuns() *fakeChainRuns {
	next := t0.Add(10 * time.Minute)
	ended := t0.Add(20 * time.Minute)
	owner := &escalation.TeamRef{PublicID: teamID, Name: "Payments", Slug: "payments"}
	return &fakeChainRuns{
		groups: map[string][]escalation.GroupChain{groupID: {
			{RunID: "ER0000000000R1", Chain: &escalation.ChainRef{PublicID: chainID, Name: "payments", OwningTeam: owner},
				ChainName: "payments", Sources: []escalation.SourceRef{{Kind: "route"}, {Kind: "team", Team: owner}},
				State: "running", Step: &escalation.StepPosition{Number: 2, Kind: "wait"}, NextStepAt: &next,
				StartedAt: t0},
			{RunID: "ER0000000000R2", ChainName: "gone", Sources: []escalation.SourceRef{{Kind: "route"}},
				State: "ran_out", StartedAt: t0},
		}},
		page: escalation.RunPage{Runs: []escalation.ChainRun{
			{PublicID: "ER0000000000R3", Group: escalation.RunGroup{PublicID: groupID, Number: 412, Title: "DiskFull",
				Severity: "warning"}, State: "stopped", StartedAt: t0, EndedAt: &ended,
				StoppedBy: &escalation.StoppedBy{Command: "acknowledge", Actor: &escalation.Actor{Kind: "user",
					PublicID: bobPublicID, Name: "Petr", Login: "petr"}},
				Steps: []escalation.RunStepView{{Number: 1, Kind: "page_on_call", At: t0, Outcome: "made"},
					{Number: 3, Kind: "page_people", At: t0, Outcome: "skipped", Reason: "severity"}}},
			{PublicID: "ER0000000000R4", Group: escalation.RunGroup{PublicID: groupID, Number: 412, Title: "DiskFull",
				Severity: "warning"}, State: "stopped", StartedAt: t0, EndedAt: &ended,
				StoppedBy: &escalation.StoppedBy{Command: "resolve"}, Steps: []escalation.RunStepView{}},
		}, Next: &escalation.RunPosition{At: t0, ID: 9}},
	}
}

// TestEscalationChainRunsAPI is listEscalationChainRuns (C-32.FR-18, C-32.AC-17): the runs of a chain with their
// Alert Groups, their steps made and skipped with the reason, what stopped them — the Command and its actor, none for
// Muster — and a cursor; every Role reads them; an unknown chain is 404; without the runs wired, 404.
func TestEscalationChainRunsAPI(t *testing.T) {
	x, _, _, _ := newChainsAPI(t)
	fr := newFakeChainRuns()
	x.srv.chainRuns = fr
	a := x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains/"+chainID+"/runs", "")
	var list gen.EscalationRunList
	decodeInto(t, a, &list)
	if a.status != http.StatusOK || len(list.Items) != 2 || list.NextCursor.IsNull() {
		t.Fatalf("runs = %d %s", a.status, a.body)
	}
	r := list.Items[0]
	stop := r.StoppedBy.MustGet()
	if r.Id != "ER0000000000R3" || r.AlertGroup.Number != 412 || r.State != "stopped" || r.EndedAt.IsNull() ||
		stop.Command != "acknowledge" || stop.Actor.MustGet().Name != "Petr" || *stop.Actor.MustGet().Login != "petr" ||
		!stop.Destination.IsNull() || len(r.Steps) != 2 || !r.Steps[0].Reason.IsNull() ||
		r.Steps[1].Reason.MustGet() != "severity" || r.Steps[1].Outcome != "skipped" {
		t.Errorf("run %s", a.body)
	}
	if !list.Items[1].StoppedBy.MustGet().Actor.IsNull() {
		t.Errorf("a stop by Muster %s", a.body)
	}
	cursor := list.NextCursor.MustGet()
	if a := x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains/"+chainID+"/runs?cursor="+cursor,
		""); a.status != http.StatusOK || fr.after[1] == nil || fr.after[1].ID != 9 {
		t.Errorf("next page = %d %s, %+v", a.status, a.body, fr.after)
	}
	if a := x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains/EC0000000000ZZ/runs", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("unknown chain = %d %s", a.status, a.body)
	}
	fr.err = errors.New("boom")
	if a := x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains/"+chainID+"/runs", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failure = %d", a.status)
	}
	x.srv.chainRuns = nil
	if a := x.as(t, chainsViewer, http.MethodGet, "/api/v1/escalation-chains/"+chainID+"/runs", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("without runs = %d %s", a.status, a.body)
	}
}

// TestAlertGroupChainsAPI is C-32.FR-18 and C-09.FR-1 at the API: getAlertGroup and listAlertGroups give the runs of
// the current start of the chains of each Alert Group in escalation_chains, with the chain, null once deleted, its
// sources, state, step and next step; none without runs; a failure of the runs fails the read.
func TestAlertGroupChainsAPI(t *testing.T) {
	x, _, _ := newAlertGroupsAPI(t)
	fr := newFakeChainRuns()
	x.srv.chainRuns = fr
	a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID, "")
	var g gen.AlertGroup
	decodeInto(t, a, &g)
	if a.status != http.StatusOK || len(g.EscalationChains) != 2 {
		t.Fatalf("alert group = %d %s", a.status, a.body)
	}
	c := g.EscalationChains[0]
	if c.RunId != "ER0000000000R1" || c.Chain.MustGet().Name != "payments" ||
		c.Chain.MustGet().OwningTeam.MustGet().Slug != "payments" || len(c.Sources) != 2 ||
		!c.Sources[0].Team.IsNull() || c.Sources[1].Team.MustGet().Slug != "payments" || c.State != "running" ||
		c.Step.MustGet().Number != 2 || c.Step.MustGet().Kind != "wait" || c.NextStepAt.MustGet().IsZero() {
		t.Errorf("running chain %s", a.body)
	}
	if d := g.EscalationChains[1]; !d.Chain.IsNull() || d.ChainName != "gone" || !d.Step.IsNull() ||
		!d.NextStepAt.IsNull() {
		t.Errorf("a chain deleted since %s", a.body)
	}
	var list gen.AlertGroupList
	decodeInto(t, x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups", ""), &list)
	for _, item := range list.Items {
		if item.EscalationChains == nil || (item.Id == groupID && len(item.EscalationChains) != 2) {
			t.Errorf("list item %+v", item.EscalationChains)
		}
	}
	fr.err = errors.New("boom")
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups/"+groupID, ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failure of the runs = %d", a.status)
	}
	if a := x.as(t, groupsReader, http.MethodGet, "/api/v1/alert-groups", ""); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failure of the runs in the list = %d", a.status)
	}
}

// TestTimelineChainOf: an escalation entry of a chain names the chain, null once deleted, its name when its run
// started and the step; one of no chain names none.
func TestTimelineChainOf(t *testing.T) {
	step := 3
	chain, name, n := timelineChainOf(groups.TimelineEntry{Step: &step, ChainName: "payments",
		Chain: &groups.ChainRef{PublicID: chainID, Name: "payments", OwningTeam: &groups.TeamRef{PublicID: teamID,
			Name: "Payments", Slug: "payments"}}})
	if chain.MustGet().Id != chainID || chain.MustGet().OwningTeam.MustGet().Slug != "payments" ||
		name.MustGet() != "payments" || n.MustGet() != 3 {
		t.Errorf("of a chain: %v %v %v", chain, name, n)
	}
	chain, name, n = timelineChainOf(groups.TimelineEntry{Step: &step, ChainName: "gone"})
	if !chain.IsNull() || name.MustGet() != "gone" || n.MustGet() != 3 {
		t.Errorf("of a deleted chain: %v %v %v", chain, name, n)
	}
	chain, name, n = timelineChainOf(groups.TimelineEntry{})
	if !chain.IsNull() || !name.IsNull() || !n.IsNull() {
		t.Errorf("of no chain: %v %v %v", chain, name, n)
	}
}

// TestTimelineEscalationEntries: escalation_step_skipped gives its reason in skip_reason, with the chain and the step
// and the skipped person in paged_users, page_not_delivered keeps its reason in reason, and escalation_webhook names
// its outgoing webhook Destination in destination, with the chain and the step.
func TestTimelineEscalationEntries(t *testing.T) {
	step, skip, page := 3, "viewer", "call_refused"
	vic := &groups.ActorRef{Kind: "user", PublicID: "SRDDDDDDDDDDDD", Name: "Vic", Login: "vic"}
	e, err := timelineEntryOf(groups.TimelineEntry{PublicID: "TEAAAAAAAAAAB1", At: t0, Kind: groups.KindEscalation,
		Event: string(groups.EventEscalationStepSkipped), Loudness: groups.Quiet, Mentions: []string{},
		Actor: groups.TimelineActor{Kind: "system", Transport: "system"}, Reason: &skip, Step: &step,
		ChainName: "payments", Chain: &groups.ChainRef{PublicID: chainID, Name: "payments"},
		PagedUsers: []*groups.ActorRef{vic}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.AsTimelineEscalationEntry()
	if err != nil || v.Event != gen.LifecycleEventEscalationStepSkipped || v.SkipReason.MustGet() != "viewer" ||
		!v.Reason.IsNull() || v.Step.MustGet() != 3 || v.Chain.MustGet().Name != "payments" ||
		len(v.PagedUsers) != 1 || v.PagedUsers[0].Name != "Vic" {
		t.Errorf("skipped %+v, %v", v, err)
	}
	e, err = timelineEntryOf(groups.TimelineEntry{PublicID: "TEAAAAAAAAAAB2", At: t0, Kind: groups.KindEscalation,
		Event: string(groups.EventPageNotDelivered), Loudness: groups.Quiet, Mentions: []string{},
		Actor: groups.TimelineActor{Kind: "system", Transport: "system"}, Reason: &page})
	if err != nil {
		t.Fatal(err)
	}
	v, _ = e.AsTimelineEscalationEntry()
	if v.Reason.MustGet() != "call_refused" || !v.SkipReason.IsNull() || !v.Step.IsNull() || !v.Chain.IsNull() ||
		v.Destination != nil {
		t.Errorf("page not delivered %+v", v)
	}
	step = 1
	e, err = timelineEntryOf(groups.TimelineEntry{PublicID: "TEAAAAAAAAAAB3", At: t0, Kind: groups.KindEscalation,
		Event: string(groups.EventEscalationWebhook), Loudness: groups.Quiet, Mentions: []string{},
		Actor: groups.TimelineActor{Kind: "system", Transport: "system"}, Step: &step, ChainName: "payments",
		Chain: &groups.ChainRef{PublicID: chainID, Name: "payments"},
		Destination: &groups.TimelineDestination{PublicID: "DSAAAAAAAAAAWB", Name: "ops-bot", Type: "webhook",
			Health: "healthy"}})
	if err != nil {
		t.Fatal(err)
	}
	v, err = e.AsTimelineEscalationEntry()
	if err != nil || v.Event != gen.LifecycleEventEscalationWebhook || v.Destination == nil ||
		v.Destination.Id != "DSAAAAAAAAAAWB" || v.Destination.Name != "ops-bot" || v.Destination.Type != "webhook" ||
		v.Destination.Health.State != "healthy" || !v.SkipReason.IsNull() || v.Step.MustGet() != 1 ||
		v.Chain.MustGet().Name != "payments" || len(v.PagedUsers) != 0 {
		t.Errorf("escalation_webhook %+v, %v", v, err)
	}
}
