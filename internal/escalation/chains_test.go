// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/escalation/dbgen"
	"github.com/muster-io/muster/internal/metrics"
)

// infoSeries is the muster_escalation_chain_info series of the chain id, as /metrics shows it.
func infoSeries(t *testing.T, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler(nil).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	var out []string
	for line := range strings.Lines(rec.Body.String()) {
		if strings.HasPrefix(line, `muster_escalation_chain_info{chain="`+id+`"`) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, "\n")
}

func diffOf(t *testing.T, e []byte) map[string]audit.Change {
	t.Helper()
	var diff []audit.Change
	if err := json.Unmarshal(e, &diff); err != nil {
		t.Fatal(err)
	}
	out := map[string]audit.Change{}
	for _, c := range diff {
		out[c.Pointer] = c
	}
	return out
}

// TestChains is C-32.FR-1 and FR-19: a chain is created with its owning Team and the payments steps of scenario 1,
// numbered from 1; read, listed and updated with If-Match; a name taken ignoring case is refused; every change is an
// Audit log entry with a before/after diff and a live hint, and muster_escalation_chain_info follows the name; an
// update that changes nothing writes nothing; the owning Team is read before the chain's row, the Schedules after it.
func TestChains(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	in := Input{Name: " payments ", Description: ptr("Pages payments"), OwningTeamID: ptr(teamPay),
		Steps: paymentsSteps()}
	c, err := svc.Create(ctx, by, in)
	if err != nil {
		t.Fatal(err)
	}
	numbers := make([]int, len(c.Steps))
	for i, st := range c.Steps {
		numbers[i] = st.Number
	}
	if c.Name != "payments" || c.OwningTeam == nil || c.OwningTeam.Slug != "payments" || c.Version != 1 ||
		!slices.Equal(numbers, []int{1, 2, 3, 4, 5, 6, 7}) || !strings.HasPrefix(c.PublicID, "EC") ||
		c.Steps[4].Schedule.Name != "Second line" || c.Steps[2].Importance != ImportanceImportant ||
		c.Steps[2].Condition != ConditionCritical || !c.Steps[5].WorkingHoursOnly || c.Steps[6].RepeatTimes != 2 ||
		!c.CreatedAt.Equal(t0) {
		t.Fatalf("created %+v", c)
	}
	e := store.audit[0]
	d := diffOf(t, e.Diff)
	if e.Action != ActionCreated || e.ResourceType.String != ResourceChain || d["/name"].After != "payments" ||
		d["/owning_team_id"].After != teamPay || d["/steps"].After == nil {
		t.Errorf("creation entry %+v %s", e, e.Diff)
	}
	if len(store.hints) != 1 || store.hints[0] != (db.Hint{OrgID: orgID, Type: Hint, ID: c.PublicID}) {
		t.Errorf("hints %+v", store.hints)
	}
	if got := infoSeries(t, c.PublicID); got != `muster_escalation_chain_info{chain="`+c.PublicID+`",name="payments"} 1` {
		t.Errorf("info %q", got)
	}
	if len(store.locks) < 2 || store.locks[0] != "team:"+teamPay || !strings.HasPrefix(store.locks[1], "schedule:") {
		t.Errorf("lock order %v", store.locks)
	}

	if _, err := svc.Create(ctx, by, Input{Name: "PAYMENTS"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a taken name = %v", err)
	}
	got, err := svc.Get(ctx, strings.ToLower(c.PublicID))
	if err != nil || got.Name != "payments" || len(got.Steps) != 7 {
		t.Fatalf("read %+v, %v", got, err)
	}
	for _, id := range []string{"EC0000000000XX", "nope"} {
		if _, err := svc.Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("read %s = %v", id, err)
		}
	}

	same := Input{Name: "payments", Steps: paymentsSteps()}
	store.locks = nil
	if got, err := svc.Update(ctx, by, c.PublicID, ptr(int64(1)), same); err != nil || got.Version != 1 ||
		len(store.audit) != 1 || got.Description != "Pages payments" || got.OwningTeam == nil {
		t.Errorf("an update that changes nothing: %+v, %v, %d entries", got, err, len(store.audit))
	}
	if _, err := svc.Update(ctx, by, c.PublicID, ptr(int64(7)), same); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	renamed := Input{Name: "payments-main", OwningTeamID: ptr(""), Steps: paymentsSteps()[:5]}
	got, err = svc.Update(ctx, by, c.PublicID, nil, renamed)
	if err != nil || got.Version != 2 || got.OwningTeam != nil || len(got.Steps) != 5 {
		t.Fatalf("renamed %+v, %v", got, err)
	}
	d = diffOf(t, store.audit[1].Diff)
	if store.audit[1].Action != ActionUpdated || d["/name"].Before != "payments" || d["/owning_team_id"].Before != teamPay ||
		d["/owning_team_id"].After != nil || d["/steps"].Before == nil || d["/description"].Before != nil {
		t.Errorf("update diff %s", store.audit[1].Diff)
	}
	if info := infoSeries(t, c.PublicID); !strings.Contains(info, `name="payments-main"`) ||
		strings.Contains(info, `name="payments"}`) {
		t.Errorf("info after the rename %q", info)
	}
	// An update locks a new owner, then the chain, then the Schedules of its steps.
	store.locks = nil
	if _, err := svc.Update(ctx, by, c.PublicID, nil, Input{Name: "payments-main", OwningTeamID: ptr(teamDev),
		Steps: paymentsSteps()[:1]}); err != nil {
		t.Fatal(err)
	}
	if len(store.locks) != 3 || store.locks[0] != "team:"+teamDev || store.locks[1] != "chain:"+c.PublicID ||
		store.locks[2] != "schedule:"+schedMain {
		t.Errorf("lock order of an update %v", store.locks)
	}
	if _, err := svc.Update(ctx, by, c.PublicID, nil, renamed); err != nil {
		t.Fatal(err)
	}
	// The chain has no owning Team now: a step for "the whole Team" needs one.
	f := fieldError(t, func() error {
		_, err := svc.Update(ctx, by, c.PublicID, nil, Input{Name: "payments-main", Steps: paymentsSteps()})
		return err
	}())
	if f.Pointer != "/steps/5/team_id" || f.Code != CodeRequired {
		t.Errorf("the whole Team without an owner: %+v", f)
	}
	if _, err := svc.Update(ctx, by, "EC0000000000XX", nil, same); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of an unknown chain = %v", err)
	}
	other, err := svc.Create(ctx, by, Input{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, by, other.PublicID, nil, Input{Name: "Payments-Main"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a rename to a taken name = %v", err)
	}

	for name, c := range map[string]struct {
		in      Input
		pointer string
		code    string
	}{
		"empty name":       {Input{Name: "  "}, "/name", CodeRequired},
		"long name":        {Input{Name: strings.Repeat("x", MaxNameLength+1)}, "/name", CodeTooLong},
		"long description": {Input{Name: "x", Description: ptr(strings.Repeat("x", MaxDescriptionLength+1))}, "/description", CodeTooLong},
		"deleted owner":    {Input{Name: "x", OwningTeamID: ptr(teamGone)}, "/owning_team_id", CodeUnknownID},
		"malformed owner":  {Input{Name: "x", OwningTeamID: ptr("nope")}, "/owning_team_id", CodeUnknownID},
	} {
		if f := fieldError(t, func() error { _, err := svc.Create(ctx, by, c.in); return err }()); f.Pointer != c.pointer ||
			f.Code != c.code {
			t.Errorf("%s: %+v", name, f)
		}
		if f := fieldError(t, func() error {
			_, err := svc.Update(ctx, by, other.PublicID, nil, c.in)
			return err
		}()); f.Pointer != c.pointer || f.Code != c.code {
			t.Errorf("update %s: %+v", name, f)
		}
	}
}

// TestList is the filters of listEscalationChains: by name with a cursor, by q, by an owning Team or "My Teams", and a
// Team that names nothing or is deleted matches nothing.
func TestList(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	for _, in := range []Input{{Name: "payments", OwningTeamID: ptr(teamPay)}, {Name: "devops", OwningTeamID: ptr(teamDev)},
		{Name: "Checkout", OwningTeamID: ptr(teamPay)}, {Name: "nobody"}} {
		if _, err := svc.Create(ctx, by, in); err != nil {
			t.Fatal(err)
		}
	}
	names := func(p Page) []string {
		out := make([]string, len(p.Chains))
		for i, c := range p.Chains {
			out[i] = c.Name
		}
		return out
	}
	page, err := svc.List(ctx, ListFilter{Limit: 2})
	if err != nil || !slices.Equal(names(page), []string{"Checkout", "devops"}) || page.Next == nil {
		t.Fatalf("first page %v %+v, %v", names(page), page.Next, err)
	}
	page, err = svc.List(ctx, ListFilter{Limit: 2, After: page.Next})
	if err != nil || !slices.Equal(names(page), []string{"nobody", "payments"}) || page.Next != nil {
		t.Errorf("second page %v, %v", names(page), err)
	}
	store.userTeams[5] = []int64{2}
	for name, c := range map[string]struct {
		f    ListFilter
		want []string
	}{
		"q":       {ListFilter{Query: " PAY "}, []string{"payments"}},
		"team":    {ListFilter{Team: strings.ToLower(teamPay)}, []string{"Checkout", "payments"}},
		"mine":    {ListFilter{MemberID: 5}, []string{"devops"}},
		"no team": {ListFilter{MemberID: 6}, []string{}},
		"deleted": {ListFilter{Team: teamGone}, []string{}},
		"bad id":  {ListFilter{Team: "nope"}, []string{}},
	} {
		c.f.Limit = 50
		if page, err := svc.List(ctx, c.f); err != nil || !slices.Equal(names(page), c.want) {
			t.Errorf("%s: %v, %v", name, names(page), err)
		}
	}
	for _, query := range []string{"ListChains", "ListUserTeams", "GetTeamID", "ListSteps", "ListStepUsers",
		"ListRouteReferrers", "ListTeamReferrers", "ListChainDestinations"} {
		store.fail = map[string]error{query: errBoom}
		f := ListFilter{}
		switch query {
		case "ListUserTeams":
			f.MemberID = 5
		case "GetTeamID":
			f.Team = teamPay
		}
		if query == "ListStepUsers" {
			if _, err := svc.Create(ctx, by, Input{Name: "people " + query, Steps: []StepInput{{Kind: KindPagePeople,
				UserIDs: &[]string{userPetr}}}}); !errors.Is(err, errBoom) {
				t.Errorf("create with %s failing: %v", query, err)
			}
			continue
		}
		if _, err := svc.List(ctx, f); !errors.Is(err, errBoom) {
			t.Errorf("%s fails: %v", query, err)
		}
	}
}

// TestDelete is C-32.FR-2, C-32.AC-15: a chain that a Route or a Team names is refused with chain_in_use, naming
// them, the Routes first; once nothing names it, it is deleted with its steps, recorded as escalation_chain.deleted with
// the values before, and its info series goes; a stale If-Match is refused.
func TestDelete(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	c, err := svc.Create(ctx, by, Input{Name: "payments", OwningTeamID: ptr(teamPay), Steps: paymentsSteps()})
	if err != nil {
		t.Fatal(err)
	}
	store.routeRefs = []fakeRouteRef{{chain: c.ID, route: 9, publicID: "RT0000000000NF", name: "Infrastructure",
		condition: ConditionCritical}}
	store.teamRefs = []fakeTeamRef{{chain: c.ID, team: 1, condition: ConditionAny}}
	got, err := svc.Get(ctx, c.PublicID)
	if err != nil || len(got.Referrers.Routes) != 1 || got.Referrers.Routes[0].Condition != ConditionCritical ||
		len(got.Referrers.Teams) != 1 || got.Referrers.Teams[0].Team.Slug != "payments" {
		t.Fatalf("referrers %+v, %v", got.Referrers, err)
	}
	err = svc.Delete(ctx, by, c.PublicID, nil)
	use, ok := errors.AsType[*InUseError](err)
	if !ok || !errors.Is(err, ErrInUse) || err.Error() != ErrInUse.Error() || len(use.Holders) != 2 ||
		use.Holders[0] != (Holder{Type: HolderRoute, PublicID: "RT0000000000NF", Name: "Infrastructure"}) ||
		use.Holders[1] != (Holder{Type: HolderTeam, PublicID: teamPay, Name: "payments"}) {
		t.Fatalf("a named chain = %v %+v", err, use)
	}
	if len(store.chains) != 1 || len(store.audit) != 1 {
		t.Errorf("the refused deletion changed something")
	}
	store.routeRefs, store.teamRefs = nil, nil
	if err := svc.Delete(ctx, by, c.PublicID, ptr(int64(5))); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale = %v", err)
	}
	for _, query := range []string{"LockChainForDeletion", "DeleteChain", "InsertAuditEntry", "Notify", "GetChain"} {
		store.fail = map[string]error{query: errBoom}
		if err := svc.Delete(ctx, by, c.PublicID, nil); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", query, err)
		}
	}
	store.fail = map[string]error{}
	if err := svc.Delete(ctx, by, c.PublicID, ptr(int64(1))); err != nil {
		t.Fatal(err)
	}
	if len(store.chains) != 0 || len(store.steps) != 0 {
		t.Errorf("left %+v %+v", store.chains, store.steps)
	}
	e := store.audit[len(store.audit)-1]
	if d := diffOf(t, e.Diff); e.Action != ActionDeleted || d["/name"].Before != "payments" || d["/name"].After != nil {
		t.Errorf("deletion entry %+v %s", e, e.Diff)
	}
	if got := infoSeries(t, c.PublicID); got != "" {
		t.Errorf("info of a deleted chain %q", got)
	}
	for _, id := range []string{c.PublicID, "nope"} {
		if err := svc.Delete(ctx, by, id, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete %s = %v", id, err)
		}
	}
}

// TestFailures: a failing query fails the save, which writes nothing.
func TestFailures(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	c, err := svc.Create(ctx, by, Input{Name: "payments", OwningTeamID: ptr(teamPay), Steps: paymentsSteps()})
	if err != nil {
		t.Fatal(err)
	}
	people := []StepInput{{Kind: KindPagePeople, UserIDs: &[]string{userPetr}}}
	for _, query := range []string{"LockOwningTeam", "InsertChain", "InsertStep", "InsertStepUsers", "GetChain",
		"InsertAuditEntry", "Notify"} {
		store.fail = map[string]error{query: errBoom}
		if _, err := svc.Create(ctx, by, Input{Name: "x " + query, OwningTeamID: ptr(teamPay), Steps: people}); !errors.Is(err, errBoom) {
			t.Errorf("create with %s failing = %v", query, err)
		}
	}
	for _, query := range []string{"LockOwningTeam", "LockChain", "UpdateChain", "DeleteSteps", "InsertStep",
		"InsertStepUsers", "InsertAuditEntry", "Notify"} {
		store.fail = map[string]error{query: errBoom}
		in := Input{Name: "renamed", OwningTeamID: ptr(teamDev), Steps: people}
		if _, err := svc.Update(ctx, by, c.PublicID, nil, in); !errors.Is(err, errBoom) {
			t.Errorf("update with %s failing = %v", query, err)
		}
	}
	store.fail = map[string]error{}
	got, err := svc.Get(ctx, c.PublicID)
	if err != nil || got.Name != "payments" || len(got.Steps) != 7 || len(store.chains) != 1 {
		t.Errorf("after the failures %+v, %v", got, err)
	}
}

// TestRefreshInfo is C-32.FR-19: muster_escalation_chain_info lists exactly the chains the database has after a
// refresh, which InfoChanged and the ticks of RunInfo start.
func TestRefreshInfo(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	store.chains = []dbgen.GetChainRow{{ID: 1, PublicID: "EC00000000NF01", Name: "a"}}
	svc.exportInfo("EC00000000NF02", "stale")
	if err := svc.RefreshInfo(ctx); err != nil {
		t.Fatal(err)
	}
	if infoSeries(t, "EC00000000NF01") == "" || infoSeries(t, "EC00000000NF02") != "" {
		t.Errorf("after the refresh: %q %q", infoSeries(t, "EC00000000NF01"), infoSeries(t, "EC00000000NF02"))
	}
	store.fail["ListChainInfo"] = errBoom
	if err := svc.RefreshInfo(ctx); !errors.Is(err, errBoom) {
		t.Errorf("a failing refresh = %v", err)
	}
	delete(store.fail, "ListChainInfo")
	store.chains = append(store.chains, dbgen.GetChainRow{ID: 2, PublicID: "EC00000000NF03", Name: "c"})
	ctx, cancel := context.WithCancel(ctx)
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		svc.RunInfo(ctx, ticks)
		close(done)
	}()
	svc.InfoChanged()
	svc.InfoChanged()
	ticks <- t0
	cancel()
	<-done
	if infoSeries(t, "EC00000000NF03") == "" {
		t.Error("RunInfo did not refresh")
	}
	svc.unexportInfo("EC00000000NF03")
	svc.unexportInfo("EC00000000NF01")
}

// TestUpdateKeepsSteps: a rename or a change of the description writes no step again, so the steps keep their rows,
// while a change of the steps writes them anew; a change of the name or of the owning Team sends the hints route and
// team of the Routes and Teams that name the chain, whose reads show it, and a change of the steps alone does not.
func TestUpdateKeepsSteps(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	c, err := svc.Create(ctx, by, Input{Name: "payments", OwningTeamID: ptr(teamPay), Steps: paymentsSteps()})
	if err != nil {
		t.Fatal(err)
	}
	ids := func() []int64 {
		out := make([]int64, len(store.steps))
		for i, st := range store.steps {
			out[i] = st.id
		}
		return out
	}
	stored := ids()
	store.routeRefs = []fakeRouteRef{{chain: c.ID, route: 9, publicID: "RT0000000000NF", name: "Infrastructure",
		condition: ConditionAny}}
	store.teamRefs = []fakeTeamRef{{chain: c.ID, team: 1, condition: ConditionAny}}
	hints := func(from int) []db.Hint { return slices.Clone(store.hints[from:]) }

	from := len(store.hints)
	got, err := svc.Update(ctx, by, c.PublicID, nil, Input{Name: "payments-main", Description: ptr("renamed"),
		Steps: paymentsSteps()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "renamed" || got.Name != "payments-main" || len(got.Steps) != 7 {
		t.Errorf("renamed %+v", got)
	}
	if got := ids(); !slices.Equal(got, stored) {
		t.Errorf("a rename wrote the steps again: %v, were %v", got, stored)
	}
	if got := hints(from); !slices.Equal(got, []db.Hint{{OrgID: orgID, Type: Hint, ID: c.PublicID},
		{OrgID: orgID, Type: hintRoute, ID: "RT0000000000NF"}, {OrgID: orgID, Type: hintTeam, ID: teamPay}}) {
		t.Errorf("hints of a rename %+v", got)
	}

	from = len(store.hints)
	if _, err := svc.Update(ctx, by, c.PublicID, nil, Input{Name: "payments-main",
		Steps: paymentsSteps()[:2]}); err != nil {
		t.Fatal(err)
	}
	if got := ids(); len(got) != 2 || slices.Contains(stored, got[0]) {
		t.Errorf("a change of the steps kept the rows: %v", got)
	}
	if got := hints(from); len(got) != 1 || got[0].Type != Hint {
		t.Errorf("hints of a change of the steps %+v", got)
	}

	from = len(store.hints)
	if _, err := svc.Update(ctx, by, c.PublicID, nil, Input{Name: "payments-main", OwningTeamID: ptr(teamDev),
		Steps: paymentsSteps()[:2]}); err != nil {
		t.Fatal(err)
	}
	if got := hints(from); len(got) != 3 {
		t.Errorf("hints of a change of the owner %+v", got)
	}
	store.fail["Notify"] = errBoom
	if _, err := svc.Update(ctx, by, c.PublicID, nil, Input{Name: "payments-x", Steps: paymentsSteps()[:2]}); !errors.Is(err, errBoom) {
		t.Errorf("a failing hint = %v", err)
	}
}
