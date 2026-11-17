// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// groupChain is a run of the chains of an Alert Group as getAlertGroup reads it.
type groupChain struct {
	RunID string `json:"run_id"`
	Chain *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"chain"`
	ChainName string `json:"chain_name"`
	Sources   []struct {
		Kind string `json:"kind"`
	} `json:"sources"`
	State string `json:"state"`
	Step  *struct {
		Number int    `json:"number"`
		Kind   string `json:"kind"`
	} `json:"step"`
	NextStepAt *time.Time `json:"next_step_at"`
}

// chainRun is a run of a chain as listEscalationChainRuns lists it.
type chainRun struct {
	State      string `json:"state"`
	AlertGroup struct {
		ID string `json:"id"`
	} `json:"alert_group"`
	StoppedBy *struct {
		Command string `json:"command"`
		Actor   *struct {
			Name string `json:"name"`
		} `json:"actor"`
	} `json:"stopped_by"`
	Steps []struct {
		Number  int     `json:"number"`
		Outcome string  `json:"outcome"`
		Reason  *string `json:"reason"`
	} `json:"steps"`
}

// runs drives the checks of the runs of Escalation chains.
type runs struct {
	*mm
}

// chains are the runs of the current start of the chains of the Alert Group g, with unclaimed.
func (r *runs) chains(g string) ([]groupChain, bool) {
	r.t.Helper()
	var v struct {
		Chains    []groupChain `json:"escalation_chains"`
		Unclaimed bool         `json:"unclaimed"`
	}
	decode(r.t, r.api.do(http.MethodGet, "/api/v1/alert-groups/"+g, ""), &v)
	return v.Chains, v.Unclaimed
}

// summary is each run of g as name/step/state, sorted, with "-" for no step.
func (r *runs) summary(g string) []string {
	r.t.Helper()
	cs, _ := r.chains(g)
	out := []string{}
	for _, c := range cs {
		step := "-"
		if c.Step != nil {
			step = strconv.Itoa(c.Step.Number)
		}
		out = append(out, c.ChainName+"/"+step+"/"+c.State)
	}
	slices.Sort(out)
	return out
}

// waitSummary waits until summary(g) is want.
func (r *runs) waitSummary(what, g string, want ...string) {
	r.t.Helper()
	slices.Sort(want)
	var got []string
	eventually(r.t, what, func() bool {
		got = r.summary(g)
		return slices.Equal(got, want)
	})
}

// skipped are the escalation_step_skipped of g as chain/step/reason, in their order.
func (r *runs) skipped(g string) []string {
	r.t.Helper()
	var page struct {
		Items []struct {
			Event string `json:"event"`
			Chain *struct {
				Name string `json:"name"`
			} `json:"chain"`
			Step       *int    `json:"step"`
			SkipReason *string `json:"skip_reason"`
		} `json:"items"`
	}
	decode(r.t, r.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?kind=escalation&order=asc&limit=200",
		""), &page)
	out := []string{}
	for _, x := range page.Items {
		if x.Event != "escalation_step_skipped" || x.Chain == nil || x.Step == nil || x.SkipReason == nil {
			continue
		}
		out = append(out, x.Chain.Name+"/"+strconv.Itoa(*x.Step)+"/"+*x.SkipReason)
	}
	return out
}

// events counts the lifecycle event of g.
func (r *runs) events(g, event string) int {
	r.t.Helper()
	n := 0
	for _, x := range r.timeline(g, "") {
		if x.Event == event {
			n++
		}
	}
	return n
}

// chainRuns are the recent runs of the chain id, newest first.
func (r *runs) chainRuns(id string) []chainRun {
	r.t.Helper()
	var page struct {
		Items []chainRun `json:"items"`
	}
	decode(r.t, r.api.do(http.MethodGet, "/api/v1/escalation-chains/"+id+"/runs", ""), &page)
	return page.Items
}

// put updates the object at path with edit, reading it with its ETag first and leaving out the read-only fields.
func (r *runs) put(path string, readOnly []string, edit func(m map[string]any)) {
	r.t.Helper()
	a := r.api.do(http.MethodGet, path, "")
	var m map[string]any
	decode(r.t, a, &m)
	for _, k := range readOnly {
		delete(m, k)
	}
	edit(m)
	b, _ := json.Marshal(m)
	r.api.json(http.MethodPut, path, string(b), http.StatusOK, "If-Match", a.header.Get("ETag"))
}

// routeReadOnly are the fields of a Route's read that its update does not take.
var routeReadOnly = []string{"id", "etag", "position", "is_default", "destinations", "storm_active",
	"open_alert_group_count", "owning_team", "created_at", "chains", "destination_ids"}

// putTeam updates the Team id with edit over its name, slug and Working hours, reading its ETag first.
func (r *runs) putTeam(id string, edit func(m map[string]any)) {
	r.t.Helper()
	a := r.api.do(http.MethodGet, "/api/v1/teams/"+id, "")
	var m map[string]any
	decode(r.t, a, &m)
	body := map[string]any{"name": m["name"], "slug": m["slug"], "own_working_hours": m["own_working_hours"]}
	if m["own_working_hours"] == true {
		body["working_hours"] = m["working_hours"]
	}
	edit(body)
	b, _ := json.Marshal(body)
	r.api.json(http.MethodPut, "/api/v1/teams/"+id, string(b), http.StatusOK, "If-Match", a.header.Get("ETag"))
}

// TestEscalationRuns is S-169 against `muster dev`, as its Verification, and with E2E_REPLICAS=2 on two replicas: the
// Route "Infrastructure" with its chain devops and the Team payments with its chain payments run on a warning Alert
// Group of payments, each at step 2; step 3 of payments is skipped for its Severity condition, and no ack timeout
// notice is recorded; Acknowledge stops payments and leaves devops, which ran out, as it was; Unacknowledge starts
// both over; nobody acknowledges and the Alert Group is Unclaimed, the step for the Working hours of payments skipped
// outside them; "How it went" lists the runs (C-32.FR-4 to FR-7, FR-14, FR-18, AC-3, AC-4, AC-5, AC-8, AC-17). Beyond
// it: a Team that joins the Unclaimed Alert Group starts its chain, which ends Unclaimed while it runs and makes it
// Unclaimed again once it ran out (C-32.AC-5); a Route without "Run the Teams' Escalation chains" starts no chain of
// its Teams (C-32.AC-12); turning it on, saving a Team its Alerts now belong to, naming a chain on the Route and
// raising the Minimum severity start nothing, and after an Acknowledge and an Unacknowledge the chains that now apply
// start, the chain the Route and the Team both name once (C-32.AC-18, AC-16, P-70); a change of a chain while a run
// goes on leaves the run on its old steps and the next run takes the new ones (C-32.FR-16, P-73); and "Repeat from
// step 3, up to 2 times" on a critical Alert Group inside the Working hours of payments makes steps 3 to 6 three times
// (C-32.AC-8).
func TestEscalationRuns(t *testing.T) {
	h := Start(t, DevProcess)
	rep := h.Replicas[len(h.Replicas)-1]
	e := newMM(t, h, h.Replicas[0])
	r := &runs{mm: e}
	api := e.api
	admin := newAgentSignedIn(t, h.Replicas[0])
	petr, _ := personWithToken(t, admin, h.Replicas[0].App, "petr", "responder")

	conn := e.connection(200)
	dests := map[string]string{}
	for _, name := range []string{"infra-critical", "infra-notice", "payments-critical", "payments-notice",
		"search-notice"} {
		dests[name] = e.destination(name, conn, "ch-"+name, nil, 50)
	}
	entry := func(name, cond string) map[string]string {
		return map[string]string{"destination_id": dests[name], "severity_condition": cond}
	}
	team := func(slug string, entries []map[string]string, extra map[string]any) string {
		t.Helper()
		if entries == nil {
			entries = []map[string]string{}
		}
		in := map[string]any{"name": slug, "slug": slug, "own_working_hours": false, "destination_entries": entries}
		for k, v := range extra {
			in[k] = v
		}
		b, _ := json.Marshal(in)
		id := api.json(http.MethodPost, "/api/v1/teams", string(b), http.StatusCreated)["id"].(string)
		api.json(http.MethodPost, "/api/v1/teams/"+id+"/users", `{"user_id":"`+petr+`"}`, http.StatusCreated)
		return id
	}
	tp := team("payments", []map[string]string{entry("payments-critical", "critical"),
		entry("payments-notice", "below_critical")}, map[string]any{"own_working_hours": true,
		"working_hours": map[string]any{"days": []string{"monday", "tuesday", "wednesday", "thursday", "friday"},
			"start": "11:00", "end": "19:00", "time_zone": "Europe/Berlin"}})
	td := team("devops", nil, nil)
	ts := team("search", []map[string]string{entry("search-notice", "below_critical")}, nil)
	monday := readClock(t, rep).Now.UTC().AddDate(0, 0, -7).Format(time.DateOnly)
	schedule := func(teamID, name string) string {
		t.Helper()
		return api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+teamID+`","name":"`+name+`",`+
			`"time_zone":"UTC","rotation_layers":[{"name":"Week","user_ids":["`+petr+`"],"shift_length":"week",`+
			`"handover_time":"10:00","handover_day":"monday","start_date":"`+monday+`","limits":null}]}`,
			http.StatusCreated)["id"].(string)
	}
	sm, sl, dm := schedule(tp, "Main"), schedule(tp, "Second line"), schedule(td, "Main")
	chain := func(name, owner, steps string) string {
		t.Helper()
		body := `{"name":"` + name + `","steps":` + steps
		if owner != "" {
			body += `,"owning_team_id":"` + owner + `"`
		}
		return api.json(http.MethodPost, "/api/v1/escalation-chains", body+"}", http.StatusCreated)["id"].(string)
	}
	cp := chain("payments", tp, `[{"kind":"page_on_call","schedule_id":"`+sm+`","importance":"normal"},
		{"kind":"wait","wait_seconds":600},
		{"kind":"page_on_call","schedule_id":"`+sm+`","importance":"important","severity_condition":"critical"},
		{"kind":"wait","wait_seconds":900},
		{"kind":"page_on_call","schedule_id":"`+sl+`","importance":"important","severity_condition":"critical"},
		{"kind":"page_team","working_hours_only":true},{"kind":"repeat","repeat_from":3,"repeat_times":2}]`)
	cd := chain("devops", td, `[{"kind":"page_on_call","schedule_id":"`+dm+`"},{"kind":"wait","wait_seconds":600},
		{"kind":"page_on_call","schedule_id":"`+dm+`","importance":"important"}]`)
	cs := chain("search", ts, `[{"kind":"page_people","user_ids":["`+petr+`"]},{"kind":"wait","wait_seconds":600},
		{"kind":"page_people","user_ids":["`+petr+`"]}]`)
	chainEntries := func(ids ...string) []map[string]string {
		out := []map[string]string{}
		for _, id := range ids {
			out = append(out, map[string]string{"chain_id": id})
		}
		return out
	}
	r.putTeam(tp, func(m map[string]any) { m["chain_entries"] = chainEntries(cp) })
	r.putTeam(ts, func(m map[string]any) { m["chain_entries"] = chainEntries(cs) })
	var profiles struct {
		Items []struct {
			ID     string         `json:"id"`
			Policy map[string]any `json:"policy"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	var paging map[string]any
	for _, p := range profiles.Items {
		if p.ID == "paging" {
			paging = p.Policy
		}
	}
	route := func(name, category string, entries []map[string]string, teamChains bool, chains []string) string {
		t.Helper()
		if entries == nil {
			entries = []map[string]string{}
		}
		b, _ := json.Marshal(map[string]any{"name": name, "group_key": []string{"alertname", "system"},
			"deliver_to_teams": true, "run_team_chains": teamChains, "destination_entries": entries,
			"chain_entries": chainEntries(chains...), "policy": paging,
			"matchers": []map[string]string{{"label": "category", "op": "=", "value": category}}})
		return api.json(http.MethodPost, "/api/v1/routes", string(b), http.StatusCreated)["id"].(string)
	}
	route("Infrastructure", "infra", []map[string]string{entry("infra-critical", "critical"),
		entry("infra-notice", "below_critical")}, true, []string{cd})

	// The development clock only moves forward: to a Wednesday 03:10 in Berlin, 7 to 13 days ahead.
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := readClock(t, rep).Now.In(berlin)
	weekday := (int(now.Weekday())+6)%7 + 1
	day := now.AddDate(0, 0, (10-weekday)%7+7)
	target := time.Date(day.Year(), day.Month(), day.Day(), 3, 10, 0, 0, berlin)
	if target.Weekday() != time.Wednesday {
		t.Fatalf("target %v", target)
	}
	advance(t, rep, int(target.Sub(readClock(t, rep).Now).Seconds()))

	// The Verification: both chains at step 2.
	e.group("e1", "KubernetesContainerOOMKilled")
	e.alert("e1", "a", `{"category":"infra","system":"k8s","team":"payments","severity":"warning","cluster":"e1a"}`,
		"")
	e.notify("e1", "first notification")
	g := e.groupOf("e1a")
	r.waitSummary("both chains at step 2", g, "devops/2/running", "payments/2/running")
	if got := r.skipped(g); len(got) != 0 {
		t.Errorf("skipped before the wait %v", got)
	}
	cs1, _ := r.chains(g)
	for _, c := range cs1 {
		if c.NextStepAt == nil || c.Chain == nil {
			t.Errorf("a run without its next step %+v", c)
		}
	}
	advance(t, rep, 600)
	r.waitSummary("devops ran out, payments waits at step 4", g, "devops/-/ran_out", "payments/4/running")
	if got := r.skipped(g); !slices.Equal(got, []string{"payments/3/severity"}) {
		t.Errorf("skipped %v", got)
	}
	if n := r.events(g, "ack_timeout"); n != 0 {
		t.Errorf("%d ack timeout notices beside the chains", n)
	}

	// Acknowledge stops payments and leaves devops as it was; Unacknowledge starts both over.
	if out := e.command(g, "acknowledge"); out != "done" {
		t.Fatalf("acknowledge %s", out)
	}
	r.waitSummary("after the acknowledge", g, "devops/-/ran_out", "payments/-/stopped")
	e.command(g, "unacknowledge")
	r.waitSummary("after the unacknowledge", g, "devops/2/running", "payments/2/running")

	// Nobody acknowledges: both run out, the step for the Working hours of payments skipped at night.
	advance(t, rep, 7200)
	r.waitSummary("both ran out", g, "devops/-/ran_out", "payments/-/ran_out")
	if _, unclaimed := r.chains(g); !unclaimed || r.events(g, "unclaimed") != 1 {
		t.Errorf("unclaimed %v, %d entries", unclaimed, r.events(g, "unclaimed"))
	}
	if got := r.skipped(g); !slices.Contains(got, "payments/6/outside_working_hours") ||
		!slices.Contains(got, "payments/5/severity") {
		t.Errorf("skipped %v", got)
	}
	if runs := r.chainRuns(cp); len(runs) != 2 || runs[0].State != "ran_out" || runs[1].State != "stopped" ||
		runs[1].StoppedBy == nil || runs[1].StoppedBy.Command != "acknowledge" || runs[1].StoppedBy.Actor == nil ||
		runs[0].AlertGroup.ID != g || len(runs[0].Steps) < 7 {
		t.Errorf("how it went %+v", runs)
	}

	// A Team that joins the Unclaimed Alert Group starts its chain: no longer Unclaimed while it runs.
	e.alert("e1", "b", `{"category":"infra","system":"k8s","team":"search","severity":"warning","cluster":"e1b"}`,
		"")
	e.notify("e1", "new alerts")
	r.waitSummary("search joined", g, "devops/-/ran_out", "payments/-/ran_out", "search/2/running")
	if _, unclaimed := r.chains(g); unclaimed {
		t.Error("still Unclaimed while the chain of search runs")
	}
	advance(t, rep, 600)
	r.waitSummary("search ran out", g, "devops/-/ran_out", "payments/-/ran_out", "search/-/ran_out")
	eventually(t, "Unclaimed again", func() bool {
		_, unclaimed := r.chains(g)
		return unclaimed && r.events(g, "unclaimed") == 2
	})

	// A Route without "Run the Teams' Escalation chains": the Team's Destination receives it, its chain does not start.
	rtest := route("Test", "test", nil, false, nil)
	e.group("e2", "DiskFull")
	e.alert("e2", "a", `{"category":"test","system":"k8s","team":"payments","severity":"warning","cluster":"e2a"}`,
		"")
	e.notify("e2", "first notification")
	g2 := e.groupOf("e2a")
	n2 := e.number(g2)
	e.rootIn(n2, "ch-payments-notice")
	eventually(t, "the first publication", func() bool {
		return h.count(t, `SELECT count(*) FROM alert_groups WHERE public_id = '`+g2+
			`' AND first_published_at IS NOT NULL`) == 1
	})
	if got := r.summary(g2); len(got) != 0 {
		t.Errorf("chains without run_team_chains %v", got)
	}

	// C-32.AC-18: changes of configuration start nothing.
	r.put("/api/v1/routes/"+rtest, routeReadOnly, func(m map[string]any) { m["run_team_chains"] = true })
	ck := chain("checkout", "", `[{"kind":"page_people","user_ids":["`+petr+`"]}]`)
	team("checkout", nil, map[string]any{"matchers": []map[string]string{{"label": "cluster", "op": "=",
		"value": "e2a"}}, "chain_entries": chainEntries(ck)})
	r.put("/api/v1/routes/"+rtest, routeReadOnly, func(m map[string]any) { m["chain_entries"] = chainEntries(cp) })
	r.put("/api/v1/routes/"+rtest, routeReadOnly, func(m map[string]any) { m["minimum_severity"] = "critical" })
	eventually(t, "the minimum severity and the team evaluation applied", func() bool {
		ag := e.alertGroup(g2)
		teams, _ := ag["teams"].([]any)
		return ag["severity_level"] == "critical" && len(teams) == 2
	})
	if got := r.summary(g2); len(got) != 0 || h.count(t, `SELECT count(*) FROM escalation_run_steps s
		JOIN escalation_runs x ON x.id = s.run_id JOIN alert_groups a ON a.id = x.alert_group_id
		WHERE a.public_id = '`+g2+`'`) != 0 {
		t.Errorf("chains after changes of configuration %v", got)
	}
	e.command(g2, "acknowledge")
	e.command(g2, "unacknowledge")
	r.waitSummary("the chains that now apply", g2, "checkout/-/ran_out", "payments/2/running")
	cs2, _ := r.chains(g2)
	for _, c := range cs2 {
		if c.ChainName == "payments" && len(c.Sources) != 2 {
			t.Errorf("payments, named by the Route and the Team, runs with the sources %+v", c.Sources)
		}
	}
	e.command(g2, "acknowledge")

	// A change of a chain while a run goes on.
	cx := chain("change", "", `[{"kind":"page_people","user_ids":["`+petr+`"]},{"kind":"wait","wait_seconds":600},
		{"kind":"page_people","user_ids":["`+petr+`"]}]`)
	route("Change", "change", []map[string]string{entry("infra-notice", "any")}, false, []string{cx})
	e.group("e3", "QueueFull")
	e.alert("e3", "a", `{"category":"change","system":"k8s","severity":"warning","cluster":"e3a"}`, "")
	e.notify("e3", "first notification")
	g3 := e.groupOf("e3a")
	r.waitSummary("the change chain at step 2", g3, "change/2/running")
	ca := api.do(http.MethodGet, "/api/v1/escalation-chains/"+cx, "")
	api.json(http.MethodPut, "/api/v1/escalation-chains/"+cx, `{"name":"change","steps":[{"kind":"page_people",`+
		`"user_ids":["`+petr+`"]}]}`, http.StatusOK, "If-Match", ca.header.Get("ETag"))
	advance(t, rep, 600)
	r.waitSummary("the run on its old steps", g3, "change/-/ran_out")
	if runs := r.chainRuns(cx); len(runs) != 1 || len(runs[0].Steps) != 3 {
		t.Errorf("the run of the old steps %+v", runs)
	}
	e.command(g3, "acknowledge")
	e.command(g3, "unacknowledge")
	eventually(t, "the next run on the new steps", func() bool {
		runs := r.chainRuns(cx)
		return len(runs) == 2 && runs[0].State == "ran_out" && len(runs[0].Steps) == 1
	})
	e.command(g3, "acknowledge")

	// "Repeat from step 3, up to 2 times" on a critical Alert Group inside the Working hours of payments.
	noon := time.Date(target.Year(), target.Month(), target.Day(), 12, 0, 0, 0, berlin)
	advance(t, rep, int(noon.Sub(readClock(t, rep).Now).Seconds()))
	e.group("e4", "CheckoutQueueBacklog")
	e.alert("e4", "a", `{"category":"infra","system":"shop","team":"payments","severity":"critical","cluster":"e4a"}`,
		"")
	e.notify("e4", "first notification")
	g4 := e.groupOf("e4a")
	r.waitSummary("the critical alert group's chains", g4, "devops/2/running", "payments/2/running")
	advance(t, rep, 3600)
	r.waitSummary("both ran out", g4, "devops/-/ran_out", "payments/-/ran_out")
	var last chainRun
	for _, x := range r.chainRuns(cp) {
		if x.AlertGroup.ID == g4 {
			last = x
		}
	}
	counts := map[int]int{}
	for _, s := range last.Steps {
		if s.Outcome != "made" {
			t.Errorf("a step skipped on the critical alert group in the working hours: %+v", s)
		}
		counts[s.Number]++
	}
	for n, want := range map[int]int{1: 1, 2: 1, 3: 3, 4: 3, 5: 3, 6: 3, 7: 3} {
		if counts[n] != want {
			t.Errorf("step %d made %d times, want %d: %v", n, counts[n], want, counts)
		}
	}
	if got := r.skipped(g4); len(got) != 0 {
		t.Errorf("skipped %v", got)
	}
	logged := ""
	for _, x := range h.Replicas {
		logged += x.allOutput()
	}
	for _, event := range []string{"escalation_run_started", "escalation_run_stopped", "escalation_run_ran_out"} {
		if !strings.Contains(logged, `"event":"`+event+`"`) {
			t.Errorf("no %s logged", event)
		}
	}
}
