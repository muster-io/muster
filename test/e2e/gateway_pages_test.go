// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
)

// gatewayPagedEntry is a paged entry of the Timeline with its loudness and gateway.
type gatewayPagedEntry struct {
	Event     string `json:"event"`
	Loudness  string `json:"loudness"`
	PagedUser []struct {
		Login string `json:"login"`
	} `json:"paged_users"`
	Gateway *struct {
		Name string `json:"name"`
	} `json:"gateway"`
	Step *int `json:"step"`
}

// gatewayPaged are the paged entries of the Alert Group g as loudness/logins/gateway, "-" for none, in their order.
func (e *contactsEnv) gatewayPaged(g string) []string {
	e.t.Helper()
	var page struct {
		Items []gatewayPagedEntry `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?kind=escalation&order=asc&limit=200",
		""), &page)
	out := []string{}
	for _, x := range page.Items {
		if x.Event != "paged" {
			continue
		}
		s := x.Loudness
		for _, u := range x.PagedUser {
			s += "/" + u.Login
		}
		if x.Gateway != nil {
			s += "/" + x.Gateway.Name
		} else {
			s += "/-"
		}
		out = append(out, s)
	}
	return out
}

// callPageOf is the state and the reason of the Pages of the kind call of the Alert Group g, in their order.
func (e *contactsEnv) callPagesOf(g string) string {
	e.t.Helper()
	return e.h.text(e.t, `SELECT coalesce(string_agg(p.state || ':' || coalesce(p.reason, '-'), ',' ORDER BY p.id), '')
		FROM pages p JOIN alert_groups a ON a.id = p.alert_group_id WHERE a.public_id = '`+g+`' AND p.kind = 'call'`)
}

// TestGatewayPages is S-157 against `muster dev`, as its Verification (C-32.FR-3, FR-10, FR-11, FR-12, FR-14, C-32.AC-7,
// C-30.FR-4, FR-7, FR-9, C-31.FR-3, C-31.AC-2, C-31.AC-6): petr, the On-call of payments' Main with a confirmed contact
// at the demo gateway and his rules in the state "call", is Paged importantly by step 3 of the chain payments for a
// critical Alert Group: his rule Mentions him and calls him at once through the gateway, the call recorded as the
// Quiet paged naming him and the gateway; the accepted call makes him an Assignee; he presses 1, the Alert Group is
// his and the chain stops; muster_pages_total counts the call. Beyond it: ivan, whose rules have no call, is Mentioned
// only, and olga, whose rule calls but who has no confirmed contact any more, is skipped with no_confirmed_contact; a
// Route with a chain and no Destination starts it at once, its Mention records page_not_delivered no_root_message and
// its call step calls the gateway (C-32.AC-7); two Alert Groups Paging petr within gateway.combine_window go in one
// call, from which the second is dropped once its chain started over on new steps; a call that ends Not delivered
// records page_not_delivered with the step and leaves the Assignees as they were; a call step whose gateway was deleted
// is skipped with gateway_deleted and the chain goes on with its next step at once.
func TestGatewayPages(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	rep := h.Replicas[len(h.Replicas)-1]
	e := &contactsEnv{mm: newMM(t, h, r), fb: h.Fakes.Bridge + "/_fake"}
	runs := &runs{mm: e.mm}
	api := e.api
	e.gw = e.connectionNamed(devmode.GatewayConnectionName)
	const petrContact, olgaContact = "+10000000002", "+10000000004"
	e.secrets = append(e.secrets, petrContact, olgaContact)
	petr, ivan, olga := e.person("Petr", "petr"), e.person("Ivan", "ivan"), e.person("Olga", "olga")
	e.confirmAt(petr, e.gw, petrContact, 3)
	const mine = "/api/v1/me/notification-rules"
	if a := e.putRules(petr.session, mine, `{"state":"call","gateway_id":"`+e.gw+`"}`); a.status != http.StatusOK {
		t.Fatalf("petr's rules = %d %s", a.status, a.body)
	}
	olgaContactID := e.confirmAt(olga, e.gw, olgaContact, 3)
	if a := e.putRules(olga.session, mine, `{"state":"call","gateway_id":"`+e.gw+`"}`); a.status != http.StatusOK {
		t.Fatalf("olga's rules = %d %s", a.status, a.body)
	}
	olga.session.json(http.MethodDelete, "/api/v1/me/gateway-contacts/"+olgaContactID, "", http.StatusNoContent)
	// A new contact that olga does not confirm is no contact to call.
	const olgaNewContact = "+10000000005"
	e.secrets = append(e.secrets, olgaNewContact)
	e.add(olga.session, olgaNewContact)

	conn := e.connection(200)
	dest := e.destination("payments-critical", conn, "ch-payments-critical", nil, 100)
	tp := api.json(http.MethodPost, "/api/v1/teams", `{"name":"payments","slug":"payments","own_working_hours":false,`+
		`"destination_entries":[]}`, http.StatusCreated)["id"].(string)
	api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+petr.id+`"}`, http.StatusCreated)
	monday := readClock(t, rep).Now.UTC().AddDate(0, 0, -7).Format("2006-01-02")
	main := api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Main","time_zone":"UTC",`+
		`"rotation_layers":[{"name":"Week","user_ids":["`+petr.id+`"],"shift_length":"week","handover_time":"10:00",`+
		`"handover_day":"monday","start_date":"`+monday+`","limits":null}]}`, http.StatusCreated)["id"].(string)
	chain := func(name, owner, steps string) string {
		t.Helper()
		body := `{"name":"` + name + `","steps":` + steps + `}`
		if owner != "" {
			body = `{"name":"` + name + `","owning_team_id":"` + owner + `","steps":` + steps + `}`
		}
		return api.json(http.MethodPost, "/api/v1/escalation-chains", body, http.StatusCreated)["id"].(string)
	}
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
	route := func(name, category string, withDestination bool, chainID string) {
		t.Helper()
		entries := []map[string]string{}
		if withDestination {
			entries = append(entries, map[string]string{"destination_id": dest, "severity_condition": "any"})
		}
		b, _ := json.Marshal(map[string]any{"name": name, "group_key": []string{"alertname", "system"},
			"deliver_to_teams": false, "run_team_chains": false, "destination_entries": entries,
			"chain_entries": []map[string]string{{"chain_id": chainID}}, "policy": paging,
			"matchers": []map[string]string{{"label": "category", "op": "=", "value": category}}})
		api.json(http.MethodPost, "/api/v1/routes", string(b), http.StatusCreated)
	}
	fire := func(group, alertname, category, severity string) string {
		t.Helper()
		e.group(group, alertname)
		e.alert(group, "a", fmt.Sprintf(`{"category":%q,"system":%q,"severity":%q,"cluster":%q}`, category, group,
			severity, group), "")
		e.notify(group, "first notification")
		return e.groupOf(group)
	}
	callsOf := func(g string) int {
		t.Helper()
		n := 0
		for _, c := range e.calls() {
			if strings.Contains(c.Text, fmt.Sprintf("#%d ", e.number(g))) {
				n++
			}
		}
		return n
	}
	metrics := func() string {
		out := ""
		for _, x := range h.Replicas {
			out += string(call(t, http.MethodGet, x.Internal+"/metrics", "").body)
		}
		return out
	}

	// The Verification: step 3 of payments Pages petr importantly; his rule calls him at once.
	cp := chain("payments", tp, `[{"kind":"page_on_call","schedule_id":"`+main+`","importance":"normal"},`+
		`{"kind":"wait","wait_seconds":600},{"kind":"page_on_call","schedule_id":"`+main+`","importance":"important",`+
		`"severity_condition":"critical"},{"kind":"wait","wait_seconds":900},`+
		`{"kind":"page_on_call","schedule_id":"`+main+`","importance":"important"}]`)
	route("Checkout", "checkout", true, cp)
	g := fire("c1", "CheckoutQueueBacklog", "checkout", "critical")
	eventually(t, "the first Page", func() bool { return len(e.gatewayPaged(g)) == 1 })
	n0 := len(e.calls())
	advance(t, rep, 600)
	eventually(t, "the call of step 3", func() bool { return len(e.calls()) == n0+1 })
	last := e.calls()[len(e.calls())-1]
	if last.Contact != petrContact || last.Language != "en" || last.Action == nil ||
		!strings.Contains(last.Text, fmt.Sprintf("#%d CheckoutQueueBacklog", e.number(g))) {
		t.Fatalf("the call %+v", last)
	}
	eventually(t, "the paged of the call", func() bool {
		return slices.Contains(e.gatewayPaged(g), "quiet/petr/"+devmode.GatewayConnectionName)
	})
	if got := e.gatewayPaged(g); len(got) != 3 || got[0] != "loud/petr/-" ||
		!slices.Contains(got, "loud/petr/-") || !slices.Contains(got, "quiet/petr/"+devmode.GatewayConnectionName) {
		t.Errorf("the paged entries %v", got)
	}
	e.waitAssignees(g, "petr/payments")
	eventually(t, "the call in How it went", func() bool {
		steps := e.runPages(cp)
		return len(steps) >= 3 && strings.Contains(steps[2], "call/delivered/petr")
	})
	// No reply in the Thread names the call (C-32.FR-9).
	for _, reply := range e.repliesIn("ch-payments-critical") {
		if strings.Contains(reply, devmode.GatewayConnectionName) || strings.Contains(reply, petrContact) {
			t.Errorf("a reply names the call: %q", reply)
		}
	}
	// petr presses 1 in the call: the Alert Group is his and the chain stops.
	e.fake(http.MethodPost, e.fb+"/acknowledge", `{"notification_id":"`+last.ID+`"}`)
	eventually(t, "the acknowledgement through the gateway", func() bool {
		ag := e.alertGroup(g)
		owner, _ := ag["owner"].(map[string]any)
		return ag["status"] == "acknowledged" && owner != nil && owner["login"] == "petr"
	})
	runs.waitSummary("the stopped chain", g, "payments/-/stopped")
	eventually(t, "the call counted", func() bool {
		return strings.Contains(metrics(), `muster_pages_total{chain="`+cp+`",kind="call",outcome="delivered"} 1`)
	})

	// ivan's rules have no call: Mentioned only; olga's rule calls, but her contact is gone: skipped.
	cpe := chain("people", "", `[{"kind":"page_people","user_ids":["`+ivan.id+`","`+olga.id+`"],`+
		`"importance":"important"}]`)
	route("People", "people", true, cpe)
	gp := fire("c2", "PeopleDown", "people", "critical")
	eventually(t, "the skip of olga's call", func() bool {
		return slices.Equal(e.pageEntries(gp, "escalation_step_skipped"),
			[]string{"escalation_step_skipped/no_confirmed_contact/olga"})
	})
	if got := e.gatewayPaged(gp); !slices.Equal(got, []string{"loud/ivan/olga/-"}) {
		t.Errorf("the pages of ivan and olga %v", got)
	}
	if n := callsOf(gp); n != 0 || e.callPagesOf(gp) != "" {
		t.Errorf("%d calls about the Alert Group of ivan and olga", n)
	}

	// A Route with a chain and no Destination: the chain starts at once, its Mention reaches nobody, its call does.
	advance(t, rep, 61)
	cn := chain("nodest", "", `[{"kind":"page_people","user_ids":["`+petr.id+`"]},`+
		`{"kind":"call","call_target":"people","user_ids":["`+petr.id+`"]}]`)
	route("NoDest", "nodest", false, cn)
	gn := fire("c3", "NoDestination", "nodest", "warning")
	eventually(t, "the call without a Destination", func() bool { return callsOf(gn) == 1 })
	eventually(t, "no_root_message", func() bool {
		return slices.Contains(e.pageEntries(gn, "page_not_delivered"), "page_not_delivered/no_root_message/petr")
	})
	eventually(t, "the accepted call of the step", func() bool { return e.callPagesOf(gn) == "delivered:-" })
	e.waitAssignees(gn, "petr")

	// Two Alert Groups Page petr within the window: one call, from which the second is dropped once its chain started
	// over on new steps, which call nobody.
	advance(t, rep, 61)
	cc := chain("combo", "", `[{"kind":"call","call_target":"people","user_ids":["`+petr.id+`"]}]`)
	route("Combo", "combo", true, cc)
	a0 := fire("c4", "ComboFirst", "combo", "warning")
	eventually(t, "the first call of combo", func() bool { return callsOf(a0) == 1 })
	before := len(e.calls())
	a1, a2 := fire("c5", "ComboSecond", "combo", "warning"), fire("c6", "ComboThird", "combo", "warning")
	eventually(t, "both Pages in the waiting call", func() bool {
		return e.callPagesOf(a1) == "pending:-" && e.callPagesOf(a2) == "pending:-"
	})
	if n := len(e.calls()); n != before {
		t.Fatalf("%d calls within the window", n-before)
	}
	ca := api.do(http.MethodGet, "/api/v1/escalation-chains/"+cc, "")
	api.json(http.MethodPut, "/api/v1/escalation-chains/"+cc, `{"name":"combo","steps":[{"kind":"page_people",`+
		`"user_ids":["`+ivan.id+`"]}]}`, http.StatusOK, "If-Match", ca.header.Get("ETag"))
	e.command(a2, "acknowledge")
	e.command(a2, "unacknowledge")
	advance(t, rep, 61)
	eventually(t, "the combined call", func() bool { return len(e.calls()) == before+1 })
	if c := e.calls()[before]; !strings.Contains(c.Text, fmt.Sprintf("#%d ComboSecond", e.number(a1))) ||
		strings.Contains(c.Text, "ComboThird") || !strings.Contains(c.Text, "1 Alert Group") {
		t.Errorf("the combined call %q", c.Text)
	}
	eventually(t, "the pages of the combined call", func() bool {
		return e.callPagesOf(a1) == "delivered:-" && e.callPagesOf(a2) == "not_delivered:-"
	})
	// The new steps Mention ivan, who has no linked account; the dropped call records nothing for petr.
	if got := e.pageEntries(a2, "page_not_delivered"); !slices.Equal(got,
		[]string{"page_not_delivered/no_account_link/ivan"}) {
		t.Errorf("the dropped Alert Group recorded %v", got)
	}

	// A call that ends Not delivered: page_not_delivered with its step, the Assignees as they were.
	advance(t, rep, 61)
	cr := chain("refusal", "", `[{"kind":"call","call_target":"people","user_ids":["`+petr.id+`"]},`+
		`{"kind":"wait","wait_seconds":600},{"kind":"call","call_target":"people","user_ids":["`+petr.id+`"]}]`)
	route("Refusal", "refusal", true, cr)
	gr := fire("c7", "Refused", "refusal", "warning")
	e.waitAssignees(gr, "petr")
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"`+callsPath+`","status":400,"times":1,
		"body":"{\"type\":\"about:blank\",\"title\":\"Bad number\",\"status\":400,\"class\":\"invalid\"}"}`)
	advance(t, rep, 600)
	eventually(t, "the refused call", func() bool {
		return slices.Equal(e.pageEntries(gr, "page_not_delivered"), []string{"page_not_delivered/call_refused/petr"})
	})
	var page struct {
		Items []gatewayPagedEntry `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/alert-groups/"+gr+"/timeline?kind=escalation&order=asc&limit=200", ""),
		&page)
	for _, x := range page.Items {
		if x.Event == "page_not_delivered" && (x.Step == nil || *x.Step != 3 || x.Gateway == nil) {
			t.Errorf("page_not_delivered %+v", x)
		}
	}
	if got, _ := e.assigneesOf(gr); !slices.Equal(got, []string{"petr"}) {
		t.Errorf("the assignees after a call not delivered %v", got)
	}
	if m := metrics(); !strings.Contains(m, `muster_pages_total{chain="`+cr+`",kind="call",outcome="not_delivered"} 1`) {
		t.Errorf("the call not delivered is not counted")
	}
	e.fake(http.MethodDelete, e.fb+"/faults", "")

	// The gateway is deleted: its call step is skipped, and the chain goes on at once.
	cg := chain("gone", "", `[{"kind":"call","call_target":"people","user_ids":["`+petr.id+`"]},`+
		`{"kind":"page_people","user_ids":["`+ivan.id+`"]}]`)
	route("Gone", "gone", true, cg)
	api.json(http.MethodDelete, "/api/v1/connections/"+e.gw, "", http.StatusNoContent)
	gg := fire("c8", "GatewayGone", "gone", "warning")
	eventually(t, "the skip of the deleted gateway", func() bool {
		return slices.Equal(runs.skipped(gg), []string{"gone/1/gateway_deleted"})
	})
	eventually(t, "the next step at once", func() bool {
		return slices.Equal(e.gatewayPaged(gg), []string{"loud/ivan/-"})
	})
	if n := callsOf(gg); n != 0 {
		t.Errorf("%d calls through a deleted gateway", n)
	}

	for _, x := range h.Replicas {
		out := x.Output()
		for _, s := range e.secrets {
			if strings.Contains(out, s) {
				t.Errorf("%s reached the log of muster dev", s)
			}
		}
	}
}
