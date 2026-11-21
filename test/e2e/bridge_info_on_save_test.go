// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// infoReadsOf counts the reads of info with the token at the fake bridge.
func infoReadsOf(t *testing.T, h *Harness, token string) int {
	t.Helper()
	n := 0
	for _, r := range fakeRequests(t, h.Fakes.Bridge) {
		if r.Method == http.MethodGet && r.Path == "/v1/info" &&
			slices.Contains(r.Headers["Authorization"], "Bearer "+token) {
			n++
		}
	}
	return n
}

// TestBridgeInfoOnSave is #658 against `muster dev`: a Bridge Connection to the fake bridge's second bot, which
// declares native_mentions, has its info read and stored when it is created, without a check; with every later read
// of info at the fake bridge failing, a Page of ivan, linked through the bridge, through a Destination of it is
// delivered with his marker, and ivan becomes an Assignee (C-32.FR-10, C-22.FR-16). A save of its name alone makes no
// request to the bridge; a Connection saved while its bridge is down is saved without info and its check fails, as
// before.
func TestBridgeInfoOnSave(t *testing.T) {
	h := Start(t, DevProcess)
	e := newMM(t, h, h.Replicas[0])
	api := e.api
	admin := newAgentSignedIn(t, h.Replicas[0])
	ivan, _ := pagedPerson(t, admin, h.Replicas[0].App, "ivan")
	token := fakebridge.SecondToken
	body := func(name string) string {
		return fmt.Sprintf(`{"type":"bridge","name":%q,"base_url":%q,"event_delivery":"poll","bot_token":%q,
			"proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}`, name, h.Fakes.Bridge, token)
	}

	reads := infoReadsOf(t, h, token)
	created := api.json(http.MethodPost, "/api/v1/connections", body("saved-br"), http.StatusCreated)
	conn := created["id"].(string)
	info, _ := api.json(http.MethodGet, "/api/v1/connections/"+conn, "", http.StatusOK)["info"].(map[string]any)
	if info == nil || info["capabilities"].(map[string]any)["native_mentions"] != true {
		t.Fatalf("the Connection has no info with native_mentions right after its creation: %v", info)
	}
	if n := infoReadsOf(t, h, token); n != reads+1 {
		t.Errorf("%d reads of info on creation", n-reads)
	}

	// A save of the name alone makes no request.
	reads = infoReadsOf(t, h, token)
	a := api.do(http.MethodGet, "/api/v1/connections/"+conn, "")
	var renamed map[string]any
	if err := json.Unmarshal([]byte(body("saved-bridge")), &renamed); err != nil {
		t.Fatal(err)
	}
	delete(renamed, "bot_token")
	b, _ := json.Marshal(renamed)
	api.json(http.MethodPut, "/api/v1/connections/"+conn, string(b), http.StatusOK, "If-Match", a.header.Get("ETag"))
	if n := infoReadsOf(t, h, token); n != reads {
		t.Errorf("%d reads of info on a new name", n-reads)
	}

	// From now on the bridge answers no read of info: the Page uses the info read on creation.
	t.Cleanup(func() { restore(t, http.MethodDelete, h.Fakes.Bridge+"/_fake/faults", "") })
	e.fake(http.MethodPost, h.Fakes.Bridge+"/_fake/faults", `{"path":"/v1/info","status":503}`)

	// A Connection saved while its bridge is down is saved without info, and its check fails.
	down := api.json(http.MethodPost, "/api/v1/connections", body("down-br"), http.StatusCreated)
	if down["info"] != nil {
		t.Errorf("a Connection saved while its bridge is down has info %v", down["info"])
	}
	if c := api.json(http.MethodPost, "/api/v1/connections/"+down["id"].(string)+"/checks", "",
		http.StatusOK); c["ok"] != false {
		t.Errorf("the check of the Connection whose bridge is down: %v", c)
	}

	dest := api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":"saved-br-alerts",
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":60,"per_seconds":60}}`, conn,
		fakebridge.ChannelAlerts, mentionSettings(nil)), http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
		display_name, created_at) SELECT 1, 'AK0000000000S8', u.id, 'bridge', c.id, 'u-ivan', 'Ivan', now()
		FROM users u, connections c WHERE u.public_id = '`+ivan+`' AND c.public_id = '`+conn+`'`)
	team := api.json(http.MethodPost, "/api/v1/teams", `{"name":"devops","slug":"devops","own_working_hours":false,
		"destination_entries":[]}`, http.StatusCreated)["id"].(string)
	api.json(http.MethodPost, "/api/v1/teams/"+team+"/users", `{"user_id":"`+ivan+`"}`, http.StatusCreated)
	chain := api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"saved","owning_team_id":"`+team+
		`","steps":[{"kind":"page_people","user_ids":["`+ivan+`"],"importance":"normal"}]}`,
		http.StatusCreated)["id"].(string)
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
	route, _ := json.Marshal(map[string]any{"name": "Saved", "group_key": []string{"alertname", "system"},
		"deliver_to_teams": true, "run_team_chains": true, "policy": paging,
		"destination_entries": []map[string]string{{"destination_id": dest, "severity_condition": "any"}},
		"chain_entries":       []map[string]string{{"chain_id": chain}},
		"matchers":            []map[string]string{{"label": "category", "op": "=", "value": "saved"}}})
	api.json(http.MethodPost, "/api/v1/routes", string(route), http.StatusCreated)

	e.group("s1", "Saved")
	e.alert("s1", "a", `{"category":"saved","system":"s","cluster":"s1a"}`, "")
	e.notify("s1", "first notification")
	g := e.groupOf("s1a")
	eventually(t, "the Page delivered", func() bool {
		return slices.Equal(e.pageEntries(g, "paged", "page_not_delivered"), []string{"paged/ivan"}) &&
			h.dump(t, `SELECT p.state FROM pages p JOIN alert_groups a ON a.id = p.alert_group_id
				WHERE a.public_id = '`+g+`'`) == "delivered"
	})
	e.waitAssignees(g, "ivan/devops")
	page := bridgeReplies(t, h, "Chain of the Route Saved")
	if len(page) != 1 || len(page[0].Input.Mentions) != 1 || page[0].Input.Mentions[0].ExternalId != "u-ivan" ||
		!page[0].Input.Notify {
		t.Errorf("the Page through the bridge %+v", page)
	}
}
