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
)

// stepRecord is a request the fake receiver recorded, with the fields of the body that escalation_webhook carries.
type stepRecord struct {
	Status          int               `json:"status"`
	WebhookID       string            `json:"webhook_id"`
	SignaturesValid int               `json:"signatures_valid"`
	Headers         map[string]string `json:"headers"`
	Body            struct {
		Event      string `json:"event"`
		Notify     bool   `json:"notify"`
		Sequence   int64  `json:"sequence"`
		AlertGroup struct {
			ID string `json:"id"`
		} `json:"alert_group"`
		Mentions       []any `json:"mentions"`
		EscalationStep *struct {
			Chain *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"chain"`
			Step   int             `json:"step"`
			Reason *string         `json:"reason"`
			Users  json.RawMessage `json:"users"`
		} `json:"escalation_step"`
		Destination *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"destination"`
	} `json:"body"`
}

// stepReceived are the requests of hook for the Alert Group g, in arrival order, as "event#sequence".
func (e *mm) stepReceived(hook, g string) ([]stepRecord, []string) {
	e.t.Helper()
	var all []stepRecord
	decode(e.t, call(e.t, http.MethodGet, webhookBase+"/_fake/received/"+hook, ""), &all)
	var out []stepRecord
	var names []string
	for _, x := range all {
		if x.Body.AlertGroup.ID == g {
			out = append(out, x)
			names = append(names, fmt.Sprintf("%s#%d", x.Body.Event, x.Body.Sequence))
		}
	}
	return out, names
}

// TestEscalationWebhook is S-156 against `muster dev`, as its Verification: a chain on the Route "esc-wh" sends, in
// its step 1, escalation_webhook to the outgoing webhook ops-bot in the events mode on no Route, which receives it
// signed with its Signing secret and no other event of the Alert Group, with the chain and the step in
// escalation_step and itself in destination (C-32.FR-13, C-32.AC-9); the events-mode Destination of the Route gets
// every other event of the Alert Group but not escalation_webhook; an outgoing webhook in the mode both, on the Route
// and named by step 2, receives escalation_webhook once, among its events in the order of their sequence, and its
// chat gets the Root message and no Thread reply of it; step 3 names a Destination
// deleted after the chain was saved and is skipped with destination_deleted (C-32.FR-14). The Timeline has the Quiet
// escalation_webhook of the kind escalation with the chain, the step and the Destination (C-32.AC-11, C-09.FR-22),
// and "How it went" the steps made and skipped.
func TestEscalationWebhook(t *testing.T) {
	h := Start(t, DevProcess)
	e := newMM(t, h, h.Replicas[0])
	withoutTeamLabel(e.api)
	api := e.api

	ops := e.webhook("ops-bot", "esc-ops", `[]`)["id"].(string)
	onRoute := e.webhook("esc-route", "esc-route", `[]`)["id"].(string)
	both := e.templateWebhook("esc-both", "both", chatTemplate("esc-both", false), "esc-both", nil)
	gone := e.webhook("esc-gone", "esc-gone", `[]`)["id"].(string)
	chain := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"devops","steps":[
		{"kind":"webhook","destination_id":"`+ops+`"},{"kind":"webhook","destination_id":"`+both+`"},
		{"kind":"webhook","destination_id":"`+gone+`"}]}`, http.StatusCreated)["id"].(string)
	api.json(http.MethodDelete, "/api/v1/destinations/"+gone, "", http.StatusNoContent)
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
	b, _ := json.Marshal(map[string]any{"name": "esc-wh", "group_key": []string{"alertname", "cluster"},
		"matchers": []map[string]string{{"label": "team", "op": "=", "value": "esc-wh"}},
		"destination_entries": []map[string]string{{"destination_id": onRoute, "severity_condition": "any"},
			{"destination_id": both, "severity_condition": "any"}},
		"chain_entries": []map[string]string{{"chain_id": chain}}, "policy": paging})
	api.json(http.MethodPost, "/api/v1/routes", string(b), http.StatusCreated)

	e.group("ew", "EscalationWebhook")
	e.alert("ew", "a", `{"team":"esc-wh","cluster":"ew"}`, "")
	e.notify("ew", "first notification")
	g := e.groupOf("ew")
	eventually(t, "the chain to run out", func() bool {
		var v struct {
			Unclaimed bool `json:"unclaimed"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/alert-groups/"+g, ""), &v)
		return v.Unclaimed
	})
	eventually(t, "the webhook events to settle", func() bool {
		return h.count(t, `SELECT count(*) FROM webhook_events WHERE state = 'pending'`) == 0
	})

	// ops-bot, on no Route: escalation_webhook only, signed, with the chain, the step and itself.
	got, names := e.stepReceived("esc-ops", g)
	if len(got) != 1 || names[0] != fmt.Sprintf("escalation_webhook#%d", got[0].Body.Sequence) {
		t.Fatalf("ops-bot received %v", names)
	}
	x := got[0]
	if x.Status != http.StatusOK || x.SignaturesValid != 1 || x.Body.Notify || len(x.Body.Mentions) != 0 ||
		x.WebhookID == "" || x.Headers["webhook-id"] != x.WebhookID || x.Headers["webhook-timestamp"] == "" ||
		x.Body.EscalationStep == nil || x.Body.EscalationStep.Chain == nil ||
		x.Body.EscalationStep.Chain.Name != "devops" || x.Body.EscalationStep.Chain.ID != chain ||
		x.Body.EscalationStep.Step != 1 || x.Body.EscalationStep.Reason != nil ||
		string(x.Body.EscalationStep.Users) != "[]" || x.Body.Destination == nil || x.Body.Destination.ID != ops ||
		x.Body.Destination.Name != "ops-bot" || x.Body.Destination.Type != "webhook" {
		t.Errorf("ops-bot: %+v", x)
	}

	// The Route's events-mode Destination: every other event, no escalation_webhook.
	_, routeNames := e.stepReceived("esc-route", g)
	if len(routeNames) == 0 || routeNames[0] != "created#1" {
		t.Fatalf("esc-route received %v", routeNames)
	}
	for _, n := range routeNames {
		if strings.HasPrefix(n, "escalation_webhook") {
			t.Errorf("esc-route received %v", routeNames)
		}
	}

	// esc-both, on the Route and named by step 2: the same events, and escalation_webhook once, in sequence order.
	bothGot, bothNames := e.stepReceived("esc-both", g)
	var steps []string
	for i, x := range bothGot {
		if i > 0 && x.Body.Sequence <= bothGot[i-1].Body.Sequence {
			t.Errorf("esc-both: %s after %s", bothNames[i], bothNames[i-1])
		}
		if x.Body.Event == "escalation_webhook" {
			steps = append(steps, bothNames[i])
			if x.Body.EscalationStep == nil || x.Body.EscalationStep.Step != 2 || x.Body.Destination == nil ||
				x.Body.Destination.ID != both {
				t.Errorf("esc-both: %+v", x.Body)
			}
		}
	}
	var others []string
	for _, n := range bothNames {
		if !slices.Contains(steps, n) {
			others = append(others, n)
		}
	}
	if len(steps) != 1 || !slices.Equal(others, routeNames) {
		t.Errorf("esc-both received %v, esc-route %v", bothNames, routeNames)
	}
	chat := e.chat("esc-both")
	if len(chat.Messages) != 1 {
		t.Errorf("the chat of esc-both has %d messages, want its Root message", len(chat.Messages))
	}
	for _, m := range chat.Messages {
		for _, r := range m.Replies {
			if strings.HasPrefix(r.Text, "escalation_webhook") {
				t.Errorf("escalation_webhook in a Thread reply: %q", r.Text)
			}
		}
	}

	// The Timeline: escalation_webhook, Quiet, of the kind escalation, with the chain, the step and the Destination;
	// step 3 skipped with destination_deleted.
	var page struct {
		Items []struct {
			Kind     string   `json:"kind"`
			Event    string   `json:"event"`
			Loudness string   `json:"loudness"`
			Mentions []string `json:"mentions"`
			Chain    *struct {
				ID string `json:"id"`
			} `json:"chain"`
			Step        *int    `json:"step"`
			SkipReason  *string `json:"skip_reason"`
			Destination *struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"destination"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?kind=escalation&order=asc&limit=50", ""),
		&page)
	var timeline []string
	for _, x := range page.Items {
		switch x.Event {
		case "escalation_webhook":
			if x.Kind != "escalation" || x.Loudness != "quiet" || len(x.Mentions) != 0 || x.Chain == nil ||
				x.Chain.ID != chain || x.Step == nil || x.Destination == nil || x.SkipReason != nil {
				t.Errorf("entry %+v", x)
				continue
			}
			timeline = append(timeline, fmt.Sprintf("webhook/%d/%s", *x.Step, x.Destination.Name))
		case "escalation_step_skipped":
			if x.Step == nil || x.SkipReason == nil || x.Destination != nil {
				t.Errorf("entry %+v", x)
				continue
			}
			timeline = append(timeline, fmt.Sprintf("skipped/%d/%s", *x.Step, *x.SkipReason))
		}
	}
	if want := []string{"webhook/1/ops-bot", "webhook/2/esc-both", "skipped/3/destination_deleted"}; !slices.Equal(
		timeline, want) {
		t.Errorf("timeline %v, want %v", timeline, want)
	}

	// How it went.
	r := &runs{mm: e}
	cr := r.chainRuns(chain)
	if len(cr) != 1 || cr[0].State != "ran_out" || cr[0].AlertGroup.ID != g {
		t.Fatalf("runs %+v", cr)
	}
	var made []string
	for _, s := range cr[0].Steps {
		reason := ""
		if s.Reason != nil {
			reason = "(" + *s.Reason + ")"
		}
		made = append(made, fmt.Sprintf("%d:%s%s", s.Number, s.Outcome, reason))
	}
	if want := []string{"1:made", "2:made", "3:skipped(destination_deleted)"}; !slices.Equal(made, want) {
		t.Errorf("steps %v, want %v", made, want)
	}
	t.Logf("ops-bot %v; esc-route %v; esc-both %v; timeline %v; steps %v", names, routeNames, bothNames, timeline,
		made)
}
