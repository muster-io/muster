// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/fakes/fakemattermost"
)

// finalEdit is the note of the final edit of a Root message (C-11.FR-14).
const finalEdit = "No longer updated here"

// putRoute saves the Route id with the fields of its read, edited by edit, as the Route form does.
func (rt *routingTeams) putRoute(id string, edit func(in map[string]any)) {
	rt.t.Helper()
	a := rt.api.do(http.MethodGet, "/api/v1/routes/"+id, "")
	var read map[string]any
	decode(rt.t, a, &read)
	in := map[string]any{}
	for _, k := range []string{"name", "matchers", "group_key", "destination_entries", "deliver_to_teams", "policy"} {
		in[k] = read[k]
	}
	edit(in)
	body, _ := json.Marshal(in)
	if b := rt.api.do(http.MethodPut, "/api/v1/routes/"+id, string(body), "If-Match",
		a.header.Get("ETag")); b.status != http.StatusOK {
		rt.t.Fatalf("save the route = %d %s", b.status, b.body)
	}
}

// putTeam replaces the Destination entries of the Team id, keeping its name, slug and Team matchers.
func (rt *routingTeams) putTeam(id string, entries []map[string]string) {
	rt.t.Helper()
	a := rt.api.do(http.MethodGet, "/api/v1/teams/"+id, "")
	var read map[string]any
	decode(rt.t, a, &read)
	body, _ := json.Marshal(map[string]any{"name": read["name"], "slug": read["slug"], "own_working_hours": false,
		"destination_entries": entries})
	if b := rt.api.do(http.MethodPut, "/api/v1/teams/"+id, string(body), "If-Match",
		a.header.Get("ETag")); b.status != http.StatusOK {
		rt.t.Fatalf("save the team = %d %s", b.status, b.body)
	}
}

// waitFinal waits until the Root message of #n in the channel shows the final edit, or not when final is false, and
// reports otherwise.
func (rt *routingTeams) waitFinal(what string, n int, channel string, final bool) {
	rt.t.Helper()
	eventually(rt.t, what, func() bool {
		return strings.Contains(rt.rootIn(n, channel).body(), finalEdit) == final
	})
}

// settled waits until the open Alert Group updates are done and every delivery of the Alert Groups is settled, so that
// what is not there by then never comes.
func (rt *routingTeams) settled(gs ...string) {
	rt.t.Helper()
	eventually(rt.t, "the open alert group updates and the deliveries", func() bool {
		if rt.h.count(rt.t, `SELECT count(*) FROM open_group_updates`) != 0 {
			return false
		}
		for _, g := range gs {
			if slices.Contains(rt.states(g), "pending") {
				return false
			}
		}
		return true
	})
}

// joinedNone reports a destination_joined in the Timeline of g.
func (rt *routingTeams) joinedNone(what, g string) {
	rt.t.Helper()
	if got := rt.joined(g); len(got) != 0 {
		rt.t.Errorf("%s recorded destination_joined %v", what, got)
	}
}

// TestRoutingChanges is S-170 against `muster dev`, as its Verification: the Route "Infrastructure" with infra-critical
// for critical, infra-notice below critical and "Also to the Teams from the label" on, the Team payments with
// payments-critical for critical and payments-notice below critical, and search with search-notice. Widening the
// condition of infra-critical to any publishes the warning Alert Group there Quietly, without destination_joined, and
// narrowing it back removes nothing (C-26.AC-6); a Destination added to the Route with critical receives no warning
// Alert Group and a critical one Quietly; payments-notice, named by the Route for critical and taken off the Team,
// keeps its Root message (C-26.FR-6); deleting search gives search-notice the final edit, and the updates drain
// (C-25.FR-10); turning the option off gives payments-critical the final edit and keeps payments-notice (C-26.AC-10);
// a Team Destination added while an Alert Group is acknowledged publishes it there Quietly (C-26.FR-6).
func TestRoutingChanges(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	rt := &routingTeams{mm: e, dests: map[string]string{}}
	conn := e.connection(200)
	loud := map[string]string{"new_alert_group": "channel"}
	for _, name := range []string{"infra-critical", "infra-notice", "payments-critical", "payments-notice",
		"search-notice"} {
		rt.dests[name] = e.destination(name, conn, "ch-"+name, loud, 50)
	}
	rt.dests["shared"] = e.destination("shared", conn, fakemattermost.ChannelAlertsProd, loud, 50)
	rt.dests["checkout-notice"] = e.destination("checkout-notice", conn, fakemattermost.ChannelAlerts, loud, 50)
	var profiles struct {
		Items []struct {
			ID     string         `json:"id"`
			Policy map[string]any `json:"policy"`
		} `json:"items"`
	}
	decode(t, e.api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	for _, p := range profiles.Items {
		if p.ID == "paging" {
			rt.policy = p.Policy
		}
	}
	tp := rt.team("payments", rt.entries("payments-critical", "critical", "payments-notice", "below_critical"))
	ts := rt.team("search", rt.entries("search-notice", "below_critical"))
	infra := rt.route("Infrastructure", "infra", []string{"alertname", "system"}, true, "",
		rt.entries("infra-critical", "critical", "infra-notice", "below_critical"), nil)
	route := infra["id"].(string)

	e.group("q1", "Latency")
	e.alert("q1", "a", `{"category":"infra","system":"api","team":"payments","severity":"warning","cluster":"q1a"}`,
		"")
	e.notify("q1", "first notification")
	g := e.groupOf("q1a")
	n := e.number(g)
	rt.waitChannels("the warning alert group", n, "ch-infra-notice", "ch-payments-notice")
	rt.published(g)

	// C-26.AC-6: a condition widened publishes Quietly, without destination_joined; narrowed again it removes nothing.
	rt.putRoute(route, func(in map[string]any) {
		in["destination_entries"] = rt.entries("infra-critical", "any", "infra-notice", "below_critical")
	})
	rt.waitChannels("the widened condition", n, "ch-infra-critical", "ch-infra-notice", "ch-payments-notice")
	if rt.loudIn(n, "ch-infra-critical") {
		t.Error("the widened condition published loudly")
	}
	rt.joinedNone("the widened condition", g)
	rt.putRoute(route, func(in map[string]any) {
		in["destination_entries"] = rt.entries("infra-critical", "critical", "infra-notice", "below_critical")
	})
	rt.settled(g)
	rt.waitFinal("infra-critical after the narrowed condition", n, "ch-infra-critical", false)

	// A Destination added with critical: no warning Alert Group, a critical one Quietly.
	e.group("q2", "Disk")
	e.alert("q2", "a", `{"category":"infra","system":"db","team":"payments","severity":"critical","cluster":"q2a"}`,
		"")
	e.notify("q2", "first notification")
	g2 := e.groupOf("q2a")
	n2 := e.number(g2)
	rt.waitChannels("the critical alert group", n2, "ch-infra-critical", "ch-payments-critical")
	rt.published(g2)
	rt.putRoute(route, func(in map[string]any) {
		in["destination_entries"] = rt.entries("infra-critical", "critical", "infra-notice", "below_critical",
			"shared", "critical")
	})
	rt.waitChannels("the critical alert group in shared", n2, "ch-infra-critical", "ch-payments-critical",
		fakemattermost.ChannelAlertsProd)
	if rt.loudIn(n2, fakemattermost.ChannelAlertsProd) {
		t.Error("the added destination published the critical alert group loudly")
	}
	rt.joinedNone("the added destination", g2)
	rt.settled(g, g2)
	if got := rt.channelsOf(n); slices.Contains(got, fakemattermost.ChannelAlertsProd) {
		t.Errorf("the warning alert group in the critical-only destination: %v", got)
	}

	// A remaining entry that is not met keeps the Root message: payments-notice is named by the Route for critical.
	rt.putRoute(route, func(in map[string]any) {
		in["destination_entries"] = rt.entries("infra-critical", "critical", "infra-notice", "below_critical",
			"shared", "critical", "payments-notice", "critical")
	})
	before := openGroupUpdatesDone(h, "team_destinations")
	rt.putTeam(tp, rt.entries("payments-critical", "critical"))
	eventually(t, "the team's destinations applied", func() bool {
		return openGroupUpdatesDone(h, "team_destinations") > before
	})
	rt.settled(g, g2)
	rt.waitFinal("payments-notice named by the route for critical", n, "ch-payments-notice", false)

	// C-25.FR-10: an Alert of search brings search-notice; deleting the Team gives it the final edit.
	e.alert("q1", "b", `{"category":"infra","system":"api","team":"search","severity":"warning","cluster":"q1b"}`,
		"")
	e.notify("q1", "new alerts")
	rt.waitChannels("search joining", n, "ch-infra-critical", "ch-infra-notice", "ch-payments-notice",
		"ch-search-notice")
	rt.settled(g)
	before = openGroupUpdatesDone(h, "team_destinations")
	if a := e.api.do(http.MethodDelete, "/api/v1/teams/"+ts, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete the team = %d %s", a.status, a.body)
	}
	eventually(t, "the deleted team applied", func() bool {
		return openGroupUpdatesDone(h, "team_destinations") > before
	})
	rt.waitFinal("search-notice after the team's deletion", n, "ch-search-notice", true)
	rt.settled(g)
	if n := h.count(t, `SELECT count(*) FROM open_group_updates`); n != 0 {
		t.Errorf("%d open alert group updates left", n)
	}

	// C-26.AC-10: a rise to critical brings payments-critical through the Team; the option off gives it the final
	// edit, and payments-notice, which the Route names, keeps its Root message.
	e.alert("q1", "c", `{"category":"infra","system":"api","team":"payments","severity":"critical","cluster":"q1c"}`,
		"")
	e.notify("q1", "new alerts")
	rt.waitChannels("the rise to critical", n, "ch-infra-critical", "ch-infra-notice", "ch-payments-critical",
		"ch-payments-notice", "ch-search-notice", fakemattermost.ChannelAlertsProd)
	rt.settled(g)
	rt.putRoute(route, func(in map[string]any) { in["deliver_to_teams"] = false })
	rt.waitFinal("payments-critical after the option off", n, "ch-payments-critical", true)
	rt.settled(g, g2)
	rt.waitFinal("payments-notice after the option off", n, "ch-payments-notice", false)
	rt.waitFinal("infra-critical after the option off", n, "ch-infra-critical", false)
	rt.waitFinal("payments-critical of the critical alert group", n2, "ch-payments-critical", true)

	// A Team Destination added while an Alert Group is acknowledged publishes it there Quietly.
	rt.putRoute(route, func(in map[string]any) { in["deliver_to_teams"] = true })
	e.command(g2, "acknowledge")
	eventually(t, "the acknowledgement", func() bool { return e.alertGroup(g2)["status"] == "acknowledged" })
	before = openGroupUpdatesDone(h, "team_destinations")
	rt.putTeam(tp, rt.entries("payments-critical", "critical", "checkout-notice", "any"))
	eventually(t, "the added team destination applied", func() bool {
		return openGroupUpdatesDone(h, "team_destinations") > before
	})
	rt.waitChannels("the acknowledged alert group in checkout-notice", n2, "ch-infra-critical",
		"ch-payments-critical", fakemattermost.ChannelAlertsProd, fakemattermost.ChannelAlerts)
	if rt.loudIn(n2, fakemattermost.ChannelAlerts) {
		t.Error("the acknowledged alert group arrived loudly")
	}
	rt.joinedNone("the added team destination", g2)
}
