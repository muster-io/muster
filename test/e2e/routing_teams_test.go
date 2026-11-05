// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
)

// routingTeams drives the checks of routing to Teams by Severity (C-26): the Destinations by name, the Paging policy
// and the helpers that create Teams and Routes with Destination entries.
type routingTeams struct {
	*mm
	dests  map[string]string
	policy map[string]any
}

// entries are Destination entries of the Destinations named, each followed by its Severity condition.
func (rt *routingTeams) entries(pairs ...string) []map[string]string {
	out := []map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]string{"destination_id": rt.dests[pairs[i]], "severity_condition": pairs[i+1]})
	}
	return out
}

// team creates a Team with its slug as name and the Destination entries.
func (rt *routingTeams) team(slug string, entries []map[string]string) string {
	rt.t.Helper()
	body, _ := json.Marshal(map[string]any{"name": slug, "slug": slug, "own_working_hours": false,
		"destination_entries": entries})
	return rt.api.json(http.MethodPost, "/api/v1/teams", string(body), http.StatusCreated)["id"].(string)
}

// route creates a Route for category=category with the Group key, the option deliver_to_teams, the owning Team when
// given and the Destination entries, and returns it as read.
func (rt *routingTeams) route(name, category string, key []string, toTeams bool, owner string,
	entries []map[string]string, edit func(policy map[string]any)) map[string]any {
	rt.t.Helper()
	policy := map[string]any{}
	for k, v := range rt.policy {
		policy[k] = v
	}
	if edit != nil {
		edit(policy)
	}
	in := map[string]any{"name": name, "group_key": key, "deliver_to_teams": toTeams, "destination_entries": entries,
		"matchers": []map[string]string{{"label": "category", "op": "=", "value": category}}, "policy": policy}
	if owner != "" {
		in["owning_team_id"] = owner
	}
	body, _ := json.Marshal(in)
	return rt.api.json(http.MethodPost, "/api/v1/routes", string(body), http.StatusCreated)
}

// channelsOf are the channels with a Root message of Alert Group #n, sorted.
func (rt *routingTeams) channelsOf(n int) []string {
	rt.t.Helper()
	out := []string{}
	for _, p := range rt.roots(n) {
		if !slices.Contains(out, p.ChannelID) {
			out = append(out, p.ChannelID)
		}
	}
	slices.Sort(out)
	return out
}

// waitChannels waits until Alert Group #n has Root messages in exactly the channels want, and reports otherwise.
func (rt *routingTeams) waitChannels(what string, n int, want ...string) {
	rt.t.Helper()
	slices.Sort(want)
	var got []string
	eventually(rt.t, what, func() bool {
		got = rt.channelsOf(n)
		return slices.Equal(got, want)
	})
	if !slices.Equal(got, want) {
		rt.t.Errorf("%s: root messages in %v, want %v", what, got, want)
	}
}

// joined are the destination_joined entries of the Timeline of g as destination/loudness/mentions.
func (rt *routingTeams) joined(g string) []string {
	rt.t.Helper()
	var page struct {
		Items []struct {
			Event       string   `json:"event"`
			Kind        string   `json:"kind"`
			SystemEvent string   `json:"system_event"`
			Loudness    string   `json:"loudness"`
			Mentions    []string `json:"mentions"`
			Destination *struct {
				Name string `json:"name"`
			} `json:"destination"`
		} `json:"items"`
	}
	decode(rt.t, rt.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?limit=200&order=asc", ""), &page)
	out := []string{}
	for _, x := range page.Items {
		if x.Event != "destination_joined" {
			continue
		}
		name := "?"
		if x.Destination != nil {
			name = x.Destination.Name
		}
		if x.Kind != "system" || x.SystemEvent != "destination_joined" {
			name += "/" + x.Kind + "/" + x.SystemEvent
		}
		out = append(out, name+"/"+x.Loudness+"/"+strings.Join(x.Mentions, "+"))
	}
	return out
}

// entriesOf are the entries of the deliveries of g by Destination name, as kind[/team slug]/condition.
func (rt *routingTeams) entriesOf(g string) map[string]string {
	rt.t.Helper()
	var page struct {
		Items []struct {
			Destination struct {
				Name string `json:"name"`
			} `json:"destination"`
			State   string `json:"state"`
			Entries []struct {
				Kind string `json:"kind"`
				Team *struct {
					Slug string `json:"slug"`
				} `json:"team"`
				SeverityCondition string `json:"severity_condition"`
			} `json:"entries"`
		} `json:"items"`
	}
	decode(rt.t, rt.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/deliveries", ""), &page)
	out := map[string]string{}
	for _, d := range page.Items {
		var parts []string
		for _, e := range d.Entries {
			p := e.Kind
			if e.Team != nil {
				p += "/" + e.Team.Slug
			}
			parts = append(parts, p+"/"+e.SeverityCondition)
		}
		out[d.Destination.Name] = strings.Join(parts, ",")
	}
	return out
}

// states are the delivery states of g, sorted and without duplicates.
func (rt *routingTeams) states(g string) []string {
	rt.t.Helper()
	var page struct {
		Items []struct {
			State string `json:"state"`
		} `json:"items"`
	}
	decode(rt.t, rt.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/deliveries", ""), &page)
	out := []string{}
	for _, d := range page.Items {
		if !slices.Contains(out, d.State) {
			out = append(out, d.State)
		}
	}
	slices.Sort(out)
	return out
}

// published waits until the first Publication of g is recorded, from which a Destination it reaches joins it.
func (rt *routingTeams) published(g string) {
	rt.t.Helper()
	eventually(rt.t, "the first publication of "+g, func() bool {
		return rt.h.count(rt.t, `SELECT count(*) FROM alert_groups WHERE public_id = '`+g+
			`' AND first_published_at IS NOT NULL`) == 1
	})
}

// loudIn reports whether the Root message of #n in the channel mentions the channel, as a Loud Publication with the
// Mentions of a new Alert Group does there.
func (rt *routingTeams) loudIn(n int, channel string) bool {
	rt.t.Helper()
	m, _ := rt.rootIn(n, channel).mentionsAndBody()
	return strings.Contains(m, "@channel")
}

// linkAlice links the Admin's account to the Mattermost user alice of the fake, from the Admin's web session, through
// the code the bot sends alice in a direct message, so that her presses act as the Admin.
func (rt *routingTeams) linkAlice(r *Replica, conn string) {
	rt.t.Helper()
	admin := newAgent(rt.t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		rt.t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	a := admin.do(http.MethodPost, "/api/v1/me/account-links/mattermost",
		`{"connection_id":"`+conn+`","username":"`+fakemattermost.AliceUsername+`"}`)
	var started struct {
		RequestID string `json:"request_id"`
	}
	if a.status != http.StatusAccepted {
		rt.t.Fatalf("start the mattermost link = %d %s", a.status, a.body)
	}
	decode(rt.t, a, &started)
	var dms []fakemattermost.DM
	decode(rt.t, call(rt.t, http.MethodGet, rt.fmm+"/dms", ""), &dms)
	if len(dms) == 0 || dms[len(dms)-1].UserID != fakemattermost.AliceUserID {
		rt.t.Fatalf("no code for alice in %+v", dms)
	}
	_, code, _ := strings.Cut(dms[len(dms)-1].Message, "\n\n")
	admin.json(http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation",
		`{"request_id":"`+started.RequestID+`","code":"`+code+`"}`, http.StatusCreated)
}

// hookEvent is an events-mode request the fake receiver recorded, with the destination of destination_joined.
type hookEvent struct {
	Body struct {
		Event       string `json:"event"`
		Notify      bool   `json:"notify"`
		Destination *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"destination"`
	} `json:"body"`
}

// openGroupUpdatesDone counts the lines open_group_update_done of a kind that the replicas logged.
func openGroupUpdatesDone(h *Harness, kind string) int {
	n := 0
	for _, r := range h.Replicas {
		n += strings.Count(r.allOutput(), `"event":"open_group_update_done","kind":"`+kind+`"`)
	}
	return n
}

// TestRoutingTeams is S-121 against `muster dev`, as its Verification: the Route "Infrastructure" with its critical and
// notice Destinations and "Also to the Teams from the label" on, the Teams payments and search with theirs. A warning
// Alert of payments makes one Alert Group with Root messages in infra-notice and payments-notice and none in the
// critical channels, each delivery naming its entry (C-26.AC-1); an Alert of search joins it Loudly in search-notice
// with destination_joined, which an outgoing webhook of search receives with its destination (C-26.AC-4, C-15.FR-2); a
// rise to critical publishes it in the critical channels and keeps the notice ones, all delivered after the critical
// Alert resolves (C-26.AC-5); an Acknowledge pressed in payments-notice edits every Root message with the same Owner
// (C-26.AC-1); an acknowledged Alert Group that a Team joins arrives there Quietly (C-26.AC-4); a Reopen by a critical
// Alert of an Alert Group that resolved at warning joins the critical channels Loudly, and a Reopen at warning none
// (C-26.FR-4, FR-5); a Destination named by a Route for critical and by a Team below critical receives a warning and a
// critical Alert Group, one Root message each (C-26.AC-3); a Storm of a Route counts a non-Urgent Alert Group in a Team
// Destination's summary and publishes an Urgent one there on its own (C-26.FR-5); a Minimum severity raised to critical
// publishes a warning Alert Group Quietly in the critical Destinations without destination_joined (C-26.AC-11);
// creating the Team checkout for an open Alert Group publishes it Quietly in the Team's Destination (C-25.AC-13); a
// Route owned by payments without the option, and the option turned off, reach only the Route's own Destination
// (C-26.AC-2, AC-10); and no warning Alert Group ever has a Root message in a critical channel.
func TestRoutingTeams(t *testing.T) {
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
	hook := e.webhook("search-events", "search", `[]`)
	rt.dests["search-events"] = hook["id"].(string)
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
	rt.team("search", rt.entries("search-notice", "below_critical", "search-events", "any"))
	infra := rt.route("Infrastructure", "infra", []string{"alertname", "system"}, true, "",
		rt.entries("infra-critical", "critical", "infra-notice", "below_critical"), nil)
	if ids := infra["destination_ids"].([]any); len(ids) != 2 || infra["deliver_to_teams"] != true ||
		infra["run_team_chains"] != true {
		t.Errorf("route %v", infra)
	}

	// C-26.AC-1: one Alert Group, two Root messages, each delivery with its entry.
	e.group("o1", "OOMKilled")
	e.alert("o1", "a", `{"category":"infra","system":"k8s","team":"payments","severity":"warning","cluster":"o1a"}`, "")
	e.notify("o1", "first notification")
	g := e.groupOf("o1a")
	n := e.number(g)
	rt.waitChannels("the warning alert group of payments", n, "ch-infra-notice", "ch-payments-notice")
	if got, want := rt.entriesOf(g), map[string]string{"infra-notice": "route/below_critical",
		"payments-notice": "team/payments/below_critical"}; !mapsEqual(got, want) {
		t.Errorf("entries %v, want %v", got, want)
	}
	rt.published(g)

	// C-26.AC-4: search joins the firing, unowned Alert Group Loudly in search-notice; its webhook receives the event.
	e.alert("o1", "b", `{"category":"infra","system":"k8s","team":"search","severity":"warning","cluster":"o1b"}`, "")
	e.notify("o1", "new alerts")
	rt.waitChannels("search joining", n, "ch-infra-notice", "ch-payments-notice", "ch-search-notice")
	if got := rt.joined(g); !slices.Equal(got, []string{"search-notice/loud/new_alert_group"}) {
		t.Errorf("destination_joined %v", got)
	}
	if !rt.loudIn(n, "ch-search-notice") {
		t.Errorf("the root message in search-notice is quiet: %+v", rt.rootIn(n, "ch-search-notice"))
	}
	var events []hookEvent
	eventually(t, "the destination_joined webhook event", func() bool {
		decode(t, call(t, http.MethodGet, webhookBase+"/_fake/received/search", ""), &events)
		return slices.ContainsFunc(events, func(x hookEvent) bool { return x.Body.Event == "destination_joined" })
	})
	for _, x := range events {
		if x.Body.Event == "destination_joined" && (x.Body.Destination == nil ||
			x.Body.Destination.Name != "search-notice" || x.Body.Destination.Type != "mattermost" || !x.Body.Notify) {
			t.Errorf("destination_joined event %+v", x.Body)
		}
		if x.Body.Event != "destination_joined" && x.Body.Destination != nil {
			t.Errorf("%s carries a destination", x.Body.Event)
		}
	}

	// C-26.AC-5: a rise to critical joins the critical Destinations; the notice ones stay; all delivered after the
	// critical Alert resolves.
	e.alert("o1", "c", `{"category":"infra","system":"k8s","team":"payments","severity":"critical","cluster":"o1c"}`,
		"")
	e.notify("o1", "new alerts")
	rt.waitChannels("the rise to critical", n, "ch-infra-critical", "ch-infra-notice", "ch-payments-critical",
		"ch-payments-notice", "ch-search-notice")
	if got := rt.joined(g); !slices.Equal(got, []string{"search-notice/loud/new_alert_group",
		"infra-critical/loud/new_alert_group", "payments-critical/loud/new_alert_group"}) {
		t.Errorf("destination_joined after the rise %v", got)
	}
	e.alert("o1", "c", `{"category":"infra","system":"k8s","team":"payments","severity":"critical","cluster":"o1c"}`,
		`,"status":"resolved"`)
	e.notify("o1", "resolved")
	eventually(t, "the level to fall to warning", func() bool { return e.alertGroup(g)["severity_level"] == "warning" })
	eventually(t, "every delivery delivered", func() bool { return slices.Equal(rt.states(g), []string{"delivered"}) })
	rt.waitChannels("the fall to warning", n, "ch-infra-critical", "ch-infra-notice", "ch-payments-critical",
		"ch-payments-notice", "ch-search-notice")
	if got := rt.states(g); !slices.Equal(got, []string{"delivered"}) {
		t.Errorf("states after the critical alert resolved %v", got)
	}

	// C-26.AC-1: Acknowledge pressed in payments-notice edits every Root message with the same Owner.
	rt.linkAlice(r, conn)
	pn := rt.rootIn(n, fakemattermost.ChannelPaymentsNotice)
	if res := e.press(pn.ID, "ack", fakemattermost.AliceUserID); res.Status != http.StatusOK {
		t.Fatalf("the press in payments-notice = %+v", res)
	}
	owners := map[string]string{}
	for _, ch := range []string{"ch-infra-critical", "ch-infra-notice", "ch-payments-critical", "ch-payments-notice",
		"ch-search-notice"} {
		eventually(t, "the acknowledgement in "+ch, func() bool {
			_, owner, ok := strings.Cut(rt.rootIn(n, ch).body(), "Acknowledged by ")
			owner, _, _ = strings.Cut(owner, "\n")
			owners[ch] = owner
			return ok
		})
	}
	if owner := owners["ch-payments-notice"]; owner == "" || slices.ContainsFunc(slices.Collect(maps.Values(owners)),
		func(o string) bool { return o != owner }) {
		t.Errorf("owners by channel %v", owners)
	}

	// C-26.AC-4: an acknowledged Alert Group that search joins arrives in search-notice Quietly.
	e.group("o2", "OOMKilled")
	e.alert("o2", "a", `{"category":"infra","system":"db","team":"payments","severity":"warning","cluster":"o2a"}`, "")
	e.notify("o2", "first notification")
	g2 := e.groupOf("o2a")
	n2 := e.number(g2)
	rt.waitChannels("the second alert group", n2, "ch-infra-notice", "ch-payments-notice")
	rt.published(g2)
	e.command(g2, "acknowledge")
	e.alert("o2", "b", `{"category":"infra","system":"db","team":"search","severity":"warning","cluster":"o2b"}`, "")
	e.notify("o2", "new alerts")
	rt.waitChannels("search joining the acknowledged alert group", n2, "ch-infra-notice", "ch-payments-notice",
		"ch-search-notice")
	if got := rt.joined(g2); !slices.Equal(got, []string{"search-notice/quiet/"}) {
		t.Errorf("destination_joined of the acknowledged alert group %v", got)
	}
	if rt.loudIn(n2, "ch-search-notice") {
		t.Error("the acknowledged alert group arrived loudly")
	}

	// C-26.AC-3: a Destination named by a Route for critical and by a Team below critical gets one Root message for a
	// warning and one for a critical Alert Group.
	rt.team("storage", rt.entries("shared", "below_critical"))
	rt.route("Shared", "shared", []string{"alertname"}, true, "", rt.entries("shared", "critical"), nil)
	e.group("s1", "VolumeFull")
	e.alert("s1", "a", `{"category":"shared","team":"storage","severity":"warning","cluster":"s1a"}`, "")
	e.notify("s1", "first notification")
	e.group("s2", "VolumeLost")
	e.alert("s2", "a", `{"category":"shared","team":"storage","severity":"critical","cluster":"s2a"}`, "")
	e.notify("s2", "first notification")
	ws, cs := e.groupOf("s1a"), e.groupOf("s2a")
	rt.waitChannels("the warning alert group of storage", e.number(ws), fakemattermost.ChannelAlertsProd)
	rt.waitChannels("the critical alert group of storage", e.number(cs), fakemattermost.ChannelAlertsProd)
	for g, want := range map[string]string{ws: "team/storage/below_critical", cs: "route/critical"} {
		if got := rt.entriesOf(g)["shared"]; got != want {
			t.Errorf("entries of %s in shared %q, want %q", g, got, want)
		}
		if roots := e.roots(e.number(g)); len(roots) != 1 {
			t.Errorf("%d root messages of %s", len(roots), g)
		}
	}

	// C-26.FR-4, FR-5: a Reopen by a critical Alert of an Alert Group that resolved at warning is a rise by Alerts:
	// it joins the critical Destinations Loudly; a Reopen at the level it resolved at joins nothing.
	reopenAt := func(group, system, level string) (string, int) {
		t.Helper()
		e.group(group, "OOMKilled")
		labels := `{"category":"infra","system":"` + system + `","team":"payments","severity":"warning","cluster":"` +
			group + `a"}`
		e.alert(group, "a", labels, "")
		e.notify(group, "first notification")
		g := e.groupOf(group + "a")
		n := e.number(g)
		rt.waitChannels("the warning alert group to reopen", n, "ch-infra-notice", "ch-payments-notice")
		rt.published(g)
		e.alert(group, "a", labels, `,"status":"resolved"`)
		e.notify(group, "resolved")
		eventually(t, "the alert group to resolve", func() bool { return e.alertGroup(g)["status"] == "resolved" })
		e.alert(group, "b", `{"category":"infra","system":"`+system+`","team":"payments","severity":"`+level+
			`","cluster":"`+group+`b"}`, "")
		e.notify(group, "new alerts")
		eventually(t, "the alert group to reopen", func() bool { return e.alertGroup(g)["status"] == "firing" })
		return g, n
	}
	gr, nr := reopenAt("r1", "rr", "critical")
	rt.waitChannels("the reopen at critical", nr, "ch-infra-critical", "ch-infra-notice", "ch-payments-critical",
		"ch-payments-notice")
	if got := rt.joined(gr); !slices.Equal(got, []string{"infra-critical/loud/new_alert_group",
		"payments-critical/loud/new_alert_group"}) {
		t.Errorf("destination_joined after the reopen at critical %v", got)
	}
	if !rt.loudIn(nr, "ch-infra-critical") || !rt.loudIn(nr, "ch-payments-critical") {
		t.Error("the reopen at critical arrived quietly in a critical channel")
	}
	gs, ns := reopenAt("r2", "rs", "warning")
	eventually(t, "every delivery of the reopen at warning delivered", func() bool {
		return slices.Equal(rt.states(gs), []string{"delivered"})
	})
	if got := rt.channelsOf(ns); !slices.Equal(got, []string{"ch-infra-notice", "ch-payments-notice"}) {
		t.Errorf("channels after the reopen at warning %v", got)
	}
	if got := rt.joined(gs); len(got) != 0 {
		t.Errorf("destination_joined after the reopen at warning %v", got)
	}

	// C-26.FR-5, C-11.FR-6: during a Storm of a Route, payments-notice counts the Alert Groups in its own summary, and
	// an Urgent one is published in payments-critical on its own.
	rt.route("Stormy", "storm", []string{"alertname"}, true, "", rt.entries(), func(p map[string]any) {
		p["storm_threshold"] = 1
	})
	for i := range 4 {
		name := fmt.Sprintf("st%d", i)
		e.group(name, fmt.Sprintf("Burst%d", i))
		e.alert(name, "a", fmt.Sprintf(`{"category":"storm","team":"payments","severity":"warning","cluster":"%sa"}`,
			name), "")
		e.notify(name, "first notification")
	}
	eventually(t, "the storm summary in payments-notice", func() bool {
		for _, p := range e.posts() {
			if p.ChannelID == fakemattermost.ChannelPaymentsNotice && p.RootID == "" &&
				strings.Contains(p.body(), "Storm on Stormy") {
				return true
			}
		}
		return false
	})
	if held := h.count(t, `SELECT count(*) FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id
		WHERE ds.name = 'payments-notice' AND d.held_by_storm_id IS NOT NULL`); held == 0 {
		t.Error("the storm holds nothing in payments-notice")
	}
	e.group("st9", "BurstUrgent")
	e.alert("st9", "a", `{"category":"storm","team":"payments","severity":"critical","cluster":"st9a"}`, "")
	e.notify("st9", "first notification")
	rt.waitChannels("the urgent alert group of the storm", e.number(e.groupOf("st9a")), "ch-payments-critical")

	// C-26.AC-11: the Minimum severity raised to critical publishes a warning Alert Group Quietly in the critical
	// Destinations, without destination_joined.
	minimum := rt.route("Minimum", "minimum", []string{"alertname"}, false, "",
		rt.entries("infra-critical", "critical", "infra-notice", "below_critical"), nil)
	e.group("m1", "Latency")
	e.alert("m1", "a", `{"category":"minimum","severity":"warning","cluster":"m1a"}`, "")
	e.notify("m1", "first notification")
	gm := e.groupOf("m1a")
	nm := e.number(gm)
	rt.waitChannels("the warning alert group of Minimum", nm, "ch-infra-notice")
	rt.published(gm)
	before := openGroupUpdatesDone(h, "route_minimum_severity")
	update := map[string]any{}
	for _, k := range []string{"name", "matchers", "group_key", "destination_entries", "deliver_to_teams", "policy"} {
		update[k] = minimum[k]
	}
	update["minimum_severity"] = "critical"
	body, _ := json.Marshal(update)
	if a := e.api.do(http.MethodPut, "/api/v1/routes/"+minimum["id"].(string), string(body),
		"If-Match", minimum["etag"].(string)); a.status != http.StatusOK {
		t.Fatalf("raise the minimum severity = %d %s", a.status, a.body)
	}
	eventually(t, "the minimum severity applied", func() bool {
		return openGroupUpdatesDone(h, "route_minimum_severity") > before
	})
	rt.waitChannels("the raised alert group", nm, "ch-infra-critical", "ch-infra-notice")
	if rt.loudIn(nm, "ch-infra-critical") || len(rt.joined(gm)) != 0 {
		t.Errorf("the raise published loudly or joined: %v", rt.joined(gm))
	}

	// C-25.AC-13: the Team checkout created for the open Alert Group of team="checkout" publishes it Quietly in the
	// Team's Destination, without destination_joined.
	e.group("c1", "Checkout")
	e.alert("c1", "a", `{"category":"infra","system":"pay","team":"checkout","severity":"warning","cluster":"c1a"}`,
		"")
	e.notify("c1", "first notification")
	gc := e.groupOf("c1a")
	nc := e.number(gc)
	rt.waitChannels("the alert group of checkout before its team", nc, "ch-infra-notice")
	rt.published(gc)
	evaluated := openGroupUpdatesDone(h, "team_evaluation")
	rt.team("checkout", rt.entries("checkout-notice", "any"))
	eventually(t, "the evaluation of checkout", func() bool {
		return openGroupUpdatesDone(h, "team_evaluation") > evaluated
	})
	rt.waitChannels("the alert group of checkout after its team", nc, fakemattermost.ChannelAlerts, "ch-infra-notice")
	if rt.loudIn(nc, fakemattermost.ChannelAlerts) || len(rt.joined(gc)) != 0 {
		t.Errorf("the team checkout published loudly or joined: %v", rt.joined(gc))
	}

	// C-26.AC-2, AC-10: a Route owned by payments without the option, and the option turned off, reach only the
	// Route's own Destination.
	rt.route("Platform", "platform", []string{"alertname"}, false, tp, rt.entries("infra-notice", "any"), nil)
	e.group("p1", "DiskFull")
	e.alert("p1", "a", `{"category":"platform","team":"payments","severity":"warning","cluster":"p1a"}`, "")
	e.notify("p1", "first notification")
	np := e.number(e.groupOf("p1a"))
	rt.waitChannels("the alert group of the platform route", np, "ch-infra-notice")
	infra = e.api.json(http.MethodGet, "/api/v1/routes/"+infra["id"].(string), "", http.StatusOK)
	off := map[string]any{"deliver_to_teams": false}
	for _, k := range []string{"name", "matchers", "group_key", "destination_ids", "policy"} {
		off[k] = infra[k]
	}
	body, _ = json.Marshal(off)
	if a := e.api.do(http.MethodPut, "/api/v1/routes/"+infra["id"].(string), string(body), "If-Match",
		infra["etag"].(string)); a.status != http.StatusOK {
		t.Fatalf("turn deliver_to_teams off = %d %s", a.status, a.body)
	}
	got := e.api.json(http.MethodGet, "/api/v1/routes/"+infra["id"].(string), "", http.StatusOK)
	if got["deliver_to_teams"] != false || len(got["destination_entries"].([]any)) != 2 {
		t.Errorf("after destination_ids alone %v", got)
	}
	e.group("o5", "OOMKilled")
	e.alert("o5", "a", `{"category":"infra","system":"lb","team":"payments","severity":"warning","cluster":"o5a"}`, "")
	e.notify("o5", "first notification")
	n5 := e.number(e.groupOf("o5a"))
	rt.waitChannels("the alert group without the teams", n5, "ch-infra-notice")

	// No warning Alert Group ever has a Root message in a critical channel.
	for _, n := range []int{n2, e.number(ws), np, n5, ns} {
		for _, ch := range rt.channelsOf(n) {
			if strings.HasSuffix(ch, "-critical") {
				t.Errorf("the warning alert group #%d has a root message in %s", n, ch)
			}
		}
	}
}

// mapsEqual reports whether two maps of strings are equal.
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
