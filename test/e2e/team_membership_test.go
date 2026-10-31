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
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

// useFlavor makes the fake IdP behave as flavor and saves the OIDC settings under another display name, so that Muster
// reads discovery again instead of the copy it keeps.
func useFlavor(t *testing.T, api patClient, flavor string) {
	t.Helper()
	if a := call(t, http.MethodPost, "http://"+devmode.OIDCAddr+"/_fake/config", `{"flavor":"`+flavor+`"}`); a.status !=
		http.StatusNoContent {
		t.Fatalf("config = %d %s", a.status, a.body)
	}
	putOIDC(t, api, func(m map[string]any) { m["display_name"] = "Example SSO (" + flavor + ")" })
}

// teamMember is a person of listTeamUsers.
type teamMember struct {
	User struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
	Role       string              `json:"role"`
	Sources    []string            `json:"sources"`
	IdpEntries []map[string]string `json:"idp_entries"`
}

// members lists the people in the Team tp as "name:source+source", by name.
func members(t *testing.T, api patClient, tp string) string {
	t.Helper()
	var page struct {
		Items []teamMember `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/teams/"+tp+"/users", ""), &page)
	out := make([]string, 0, len(page.Items))
	for _, m := range page.Items {
		out = append(out, m.User.Name+":"+strings.Join(m.Sources, "+"))
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

// userID is the public_id of the User with the login.
func userID(t *testing.T, api patClient, login string) string {
	t.Helper()
	items := api.json(http.MethodGet, "/api/v1/users?q="+login, "", http.StatusOK)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("users?q=%s has %d items", login, len(items))
	}
	return items[0].(map[string]any)["id"].(string)
}

// signInAs scripts the person and signs them in through OIDC, failing unless the sign-in lands on the start page;
// the session stays live, so that a re-check of the person is not skipped.
func signInAs(t *testing.T, base string, user map[string]any) {
	t.Helper()
	scriptUser(t, user)
	if to := newAgent(t, base).oidcSignIn(); to != "/" {
		t.Fatalf("%s's sign-in ended at %s", user["preferred_username"], to)
	}
}

// TestTeamMembershipFromIdP is C-25.FR-4, C-25.AC-3 and C-25.AC-4 against `muster dev`: a Team with the entry
// teams/payments/* takes in a person in teams/payments and one in teams/payments/oncall at their sign-ins, not one in
// teams/paymentsx; listTeamUsers gives them the source idp with the entry; a person added by hand stays; after the
// second person's group changes, their next sign-in takes them out; removing by hand a person in the Team only through
// the identity provider is 404, and removing by hand a person with both sources keeps them with idp. The Audit log has
// the changes by the system with the source idp and the entry, and nobody's Role changes. An entry naming GitLab's
// maintainer claim matches when userinfo carries it (the GitLab mode) and when the ID token does (Keycloak), and with
// the Keycloak flavor and an offline token a re-check, due after an advance of the development clock, takes a person
// whose group was removed out of the Team without a sign-in.
func TestTeamMembershipFromIdP(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	base := r.App
	api := adminAPI(t, base)
	putOIDC(t, api, func(m map[string]any) {
		m["group_mappings"] = []map[string]any{{"group": "teams/*", "role": "responder"},
			{"group": "oncall", "role": "responder"}}
		m["unmatched_role"], m["sync_role"] = "viewer", true
	})
	team := api.json(http.MethodPost, "/api/v1/teams", `{"name":"Payments","slug":"payments","own_working_hours":false,`+
		`"idp_entries":[{"group":"teams/payments/*"},{"claim":"`+maintainerClaim+`","group":"teams/billing"}]}`,
		http.StatusCreated)
	tp := team["id"].(string)
	if got, _ := json.Marshal(team["idp_entries"]); string(got) !=
		`[{"group":"teams/payments/*"},{"claim":"`+maintainerClaim+`","group":"teams/billing"}]` {
		t.Errorf("idp_entries = %s", got)
	}

	// C-25.AC-3: the subtree at / boundaries.
	signInAs(t, base, map[string]any{"sub": "t-1", "preferred_username": "olga", "groups": []string{"teams/payments"}})
	signInAs(t, base, map[string]any{"sub": "t-2", "preferred_username": "petr",
		"groups": []string{"teams/payments/oncall"}})
	signInAs(t, base, map[string]any{"sub": "t-3", "preferred_username": "rita", "groups": []string{"teams/paymentsx"}})
	if got := members(t, api, tp); got != "olga:idp petr:idp" {
		t.Fatalf("Payments = %s", got)
	}
	var page struct {
		Items []teamMember `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/teams/"+tp+"/users", ""), &page)
	if len(page.Items[0].IdpEntries) != 1 || page.Items[0].IdpEntries[0]["group"] != "teams/payments/*" {
		t.Errorf("the entries that matched = %+v", page.Items[0].IdpEntries)
	}
	api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+userID(t, api, "rita")+`"}`,
		http.StatusCreated)
	signInAs(t, base, map[string]any{"sub": "t-2", "preferred_username": "petr", "groups": []string{"teams/other"}})
	signInAs(t, base, map[string]any{"sub": "t-3", "preferred_username": "rita", "groups": []string{"teams/paymentsx"}})
	if got := members(t, api, tp); got != "olga:idp rita:by_hand" {
		t.Errorf("Payments after petr's group changed = %s", got)
	}
	for _, login := range []string{"olga", "petr", "rita"} {
		items := api.json(http.MethodGet, "/api/v1/users?q="+login, "", http.StatusOK)["items"].([]any)
		if role := items[0].(map[string]any)["role"]; role != "responder" {
			t.Errorf("%s's Role = %v", login, role)
		}
	}
	audit := func(action string) []string {
		var page struct {
			Items []struct {
				Actor   map[string]any `json:"actor"`
				Details struct {
					Source string            `json:"source"`
					Entry  map[string]string `json:"entry"`
					User   string            `json:"user_name"`
				} `json:"details"`
			} `json:"items"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/audit-log?action="+action+"&limit=50", ""), &page)
		var out []string
		for _, it := range page.Items {
			if it.Details.Source == "idp" {
				out = append(out, fmt.Sprintf("%s %s %s %s", it.Actor["kind"], it.Details.User, it.Details.Entry["claim"],
					it.Details.Entry["group"]))
			}
		}
		slices.Sort(out)
		return out
	}
	if got := audit("team.user_added"); fmt.Sprint(got) !=
		"[system olga groups teams/payments/* system petr groups teams/payments/*]" {
		t.Errorf("team.user_added by the identity provider = %v", got)
	}
	if got := audit("team.user_removed"); fmt.Sprint(got) != "[system petr groups teams/payments/*]" {
		t.Errorf("team.user_removed by the identity provider = %v", got)
	}

	// C-25.FR-4: removing by hand takes away only by_hand.
	olga := userID(t, api, "olga")
	if a := api.do(http.MethodDelete, "/api/v1/teams/"+tp+"/users/"+olga, ""); a.status != http.StatusNotFound {
		t.Errorf("removing by hand a person only through the identity provider = %d %s", a.status, a.body)
	}
	added := api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+olga+`"}`, http.StatusCreated)
	if fmt.Sprint(added["sources"]) != "[by_hand idp]" {
		t.Errorf("olga added by hand = %v", added["sources"])
	}
	api.json(http.MethodDelete, "/api/v1/teams/"+tp+"/users/"+olga, "", http.StatusNoContent)
	if got := members(t, api, tp); got != "olga:idp rita:by_hand" {
		t.Errorf("after removing olga by hand = %s", got)
	}
	var removed struct {
		Items []struct {
			Diff []map[string]any `json:"diff"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/audit-log?action=team.user_removed&limit=1", ""), &removed)
	if d, _ := json.Marshal(removed.Items[0].Diff); !strings.Contains(string(d),
		`"before":{"sources":["by_hand","idp"]}`) || !strings.Contains(string(d), `"after":{"sources":["idp"]}`) {
		t.Errorf("the removal by hand = %s", d)
	}

	// C-25.AC-4: the maintainer claim in the ID token (Keycloak) …
	signInAs(t, base, map[string]any{"sub": "k-1", "preferred_username": "kim", "groups": []string{"oncall"},
		"role_claims": map[string]any{"maintainer": []string{"teams/billing"}}})
	// … and in userinfo (GitLab).
	config := "http://" + devmode.OIDCAddr + "/_fake/config"
	t.Cleanup(func() { restore(t, http.MethodPost, config, `{"flavor":"keycloak"}`) })
	useFlavor(t, api, "gitlab")
	signInAs(t, base, map[string]any{"sub": "g-1", "preferred_username": "gus", "groups": []string{"oncall"},
		"role_claims": map[string]any{"maintainer": []string{"teams/billing"}}})
	if got := members(t, api, tp); got != "gus:idp kim:idp olga:idp rita:by_hand" {
		t.Errorf("the maintainer claim = %s", got)
	}
	useFlavor(t, api, "keycloak")

	// C-03.FR-30: with an offline token, a re-check takes olga out once her group is removed at the IdP.
	signInAs(t, base, map[string]any{"sub": "t-1", "preferred_username": "olga", "groups": []string{"teams/payments"}})
	scriptUser(t, map[string]any{"sub": "t-1", "preferred_username": "olga", "groups": []string{"oncall"}})
	advance(t, r, 900)
	deadline := time.Now().Add(30 * time.Second)
	for members(t, api, tp) != "gus:idp kim:idp rita:by_hand" {
		if time.Now().After(deadline) {
			t.Fatalf("the re-check did not take olga out: %s", members(t, api, tp))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n := h.count(t, `SELECT count(*) FROM oidc_checks WHERE last_outcome = 'ok'`); n < 1 {
		t.Errorf("%d re-checks ended ok", n)
	}
}
