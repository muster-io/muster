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
	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// TestPageMentionsThroughBridge is #690 against `muster dev` (C-28.FR-3, C-12.FR-11, C-32.FR-9): the bridge declares
// group mentions and here at the floor of max_body_length, 500, read by a check of its Connection on every replica. A
// step that Pages the whole Team by its Team mention for the bridge — the longest group a Team mention may have, 64
// characters, each `_` escaped in its marker: 149 code points — and a step that Pages everyone in the chat make Thread
// replies whose line of Mentions, of 250 code points, carries the marker; their Pages are delivered, they record no
// "Mention not delivered" in the Timeline, and the Destination has no warning. A Page's line carries one such Mention
// and no person, so it always fits; the record of one left out for length is the unit test TestPageReplyLeftOutMentions
// and the integration test TestIntegrationPageReplyLeftOutMentionOnce.
func TestPageMentionsThroughBridge(t *testing.T) {
	h := Start(t, DevProcess)
	e := newMM(t, h, h.Replicas[0])
	api := e.api
	brConn := e.demoConnection("bridge", devmode.BridgeConnectionName)

	capabilities, _ := json.Marshal(fakebridge.DefaultConfig().Capabilities)
	limits, _ := json.Marshal(fakebridge.DefaultConfig().Limits)
	t.Cleanup(func() {
		restore(t, http.MethodPut, h.Fakes.Bridge+"/_fake/config", `{"capabilities":`+string(capabilities)+
			`,"limits":`+string(limits)+`}`)
	})
	var caps map[string]any
	if err := json.Unmarshal(capabilities, &caps); err != nil {
		t.Fatal(err)
	}
	caps["group_mentions"], caps["everyone_mentions"] = true, []string{"here"}
	declared, _ := json.Marshal(caps)
	e.fake(http.MethodPut, h.Fakes.Bridge+"/_fake/config", `{"capabilities":`+string(declared)+
		`,"limits":{"max_body_length":500,"edit_max_age_seconds":null}}`)
	for _, rep := range h.Replicas {
		c := patClient{t: t, base: rep.App, token: api.token}
		if got := c.json(http.MethodPost, "/api/v1/connections/"+brConn+"/checks", "", http.StatusOK); got["ok"] != true {
			t.Fatalf("the check of the bridge: %v", got)
		}
	}

	group := "d" + strings.Repeat("_", 63)
	team := api.json(http.MethodPost, "/api/v1/teams", `{"name":"devops","slug":"devops","own_working_hours":false,`+
		`"team_mentions":[{"connection_id":"`+brConn+`","group":"`+group+`"}]}`, http.StatusCreated)["id"].(string)
	br := api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":"bridge-pages",
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":60,"per_seconds":60}}`, brConn,
		fakebridge.ChannelAlerts, mentionSettings(nil)), http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	chain := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"Night","owning_team_id":"`+team+
		`","steps":[{"kind":"page_team"},{"kind":"page_team","everyone":"here"}]}`, http.StatusCreated)["id"].(string)
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
	route, _ := json.Marshal(map[string]any{"name": "Teams", "group_key": []string{"alertname", "system"},
		"deliver_to_teams": false, "run_team_chains": false, "policy": paging,
		"destination_entries": []map[string]string{{"destination_id": br, "severity_condition": "any"}},
		"chain_entries":       []map[string]string{{"chain_id": chain}},
		"matchers":            []map[string]string{{"label": "category", "op": "=", "value": "teams"}}})
	api.json(http.MethodPost, "/api/v1/routes", string(route), http.StatusCreated)

	e.group("pm1", "TeamsDown")
	e.alert("pm1", "a", `{"category":"teams","system":"t","cluster":"pm1a"}`, "")
	e.notify("pm1", "first notification")
	g := e.groupOf("pm1a")
	eventually(t, "the two Pages delivered", func() bool {
		return h.dump(t, `SELECT p.state FROM pages p JOIN alert_groups a ON a.id = p.alert_group_id
			WHERE a.public_id = '`+g+`' ORDER BY p.id`) == "delivered\ndelivered"
	})
	replies := bridgeReplies(t, h, "Chain of the Route Teams")
	// Each reply's Lead is "Step {n}: {line}, you are needed": the kind and name of each Mention it carries, with the
	// length of the line in code points.
	var got []string
	for _, r := range replies {
		lead, _, _ := strings.Cut(r.Input.Body, ", you are needed")
		_, line, _ := strings.Cut(lead, ": ")
		for _, m := range r.Input.Mentions {
			kind := "user"
			if m.Kind != nil {
				kind = string(*m.Kind)
			}
			got = append(got, fmt.Sprintf("%s:%s %d", kind, m.ExternalId, len([]rune(line))))
		}
		t.Logf("the Page reply through the bridge: %q, mentions %+v", r.Input.Body, r.Input.Mentions)
	}
	slices.Sort(got)
	if want := []string{"everyone:here 26", "group:" + group + " 149"}; !slices.Equal(got, want) {
		t.Errorf("the Mentions of the Page replies %v, want %v", got, want)
	}
	if events := deliveryEvents(e.timeline(g, "&kind=delivery")); slices.Contains(events, "mention_not_delivered") {
		t.Errorf("the timeline has a Mention not delivered: %v", events)
	}
	if n := h.count(t, `SELECT count(*) FROM delivery_events WHERE kind = 'mention_not_delivered'`); n != 0 {
		t.Errorf("%d mention_not_delivered", n)
	}
	var d struct {
		Warnings []json.RawMessage `json:"mention_warnings"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/destinations/"+br, ""), &d)
	if len(d.Warnings) != 0 {
		t.Errorf("the warnings of the Destination %s", d.Warnings)
	}
}
