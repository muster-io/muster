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

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
)

// assigneeView is an Assignee as the API reads them.
type assigneeView struct {
	User struct {
		ID    string `json:"id"`
		Login string `json:"login"`
	} `json:"user"`
	Team *struct {
		Slug string `json:"slug"`
	} `json:"team"`
	Chain *struct {
		ID string `json:"id"`
	} `json:"chain"`
	Since string `json:"since"`
}

// assigneesOf are the Assignees of the Alert Group g as login/team, sorted, and whether it is Unclaimed.
func (e *mm) assigneesOf(g string) ([]string, bool) {
	e.t.Helper()
	var v struct {
		Assignees []assigneeView `json:"assignees"`
		Unclaimed bool           `json:"unclaimed"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g, ""), &v)
	out := []string{}
	for _, a := range v.Assignees {
		s := a.User.Login
		if a.Team != nil {
			s += "/" + a.Team.Slug
		}
		if a.Chain == nil || a.Since == "" {
			e.t.Errorf("assignee %+v without its chain or since", a)
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out, v.Unclaimed
}

// waitAssignees waits for the Assignees of g to be want.
func (e *mm) waitAssignees(g string, want ...string) {
	e.t.Helper()
	slices.Sort(want)
	if want == nil {
		want = []string{}
	}
	var got []string
	eventually(e.t, fmt.Sprintf("the assignees %v of %s", want, g), func() bool {
		got, _ = e.assigneesOf(g)
		return slices.Equal(got, want)
	})
}

// assigned reports whether the list of the Alert Groups where the caller's filter assignee selects an Assignee has g.
func assigned(t *testing.T, do func(method, target, body string) answer, assignee, g string) bool {
	t.Helper()
	a := do(http.MethodGet, "/api/v1/alert-groups?assignee="+assignee+"&limit=100", "")
	if a.status != http.StatusOK {
		t.Fatalf("assignee=%s = %d %s", assignee, a.status, a.body)
	}
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decode(t, a, &page)
	return slices.ContainsFunc(page.Items, func(i struct {
		ID string `json:"id"`
	}) bool {
		return i.ID == g
	})
}

// changesOf are the Assignees after each assignees_changed of g as comma-joined logins, oldest first.
func (e *mm) changesOf(g string) []string {
	e.t.Helper()
	var page struct {
		Items []struct {
			Event     string         `json:"event"`
			Loudness  string         `json:"loudness"`
			Assignees []assigneeView `json:"assignees"`
		} `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?kind=escalation&order=asc&limit=200",
		""), &page)
	var out []string
	for _, x := range page.Items {
		if x.Event != "assignees_changed" {
			continue
		}
		if x.Loudness != "quiet" {
			e.t.Errorf("a Loud assignees_changed")
		}
		var logins []string
		for _, a := range x.Assignees {
			logins = append(logins, a.User.Login)
		}
		slices.Sort(logins)
		out = append(out, strings.Join(logins, ","))
	}
	return out
}

// rootText is the text of the live Root message of g in the channel of the fake Mattermost server.
func (e *mm) rootText(g, channel string) string {
	e.t.Helper()
	return e.rootIn(e.number(g), channel).attachment(e.t).Text
}

// TestAssignees is S-168 against `muster dev`, as its Verification: the Route "Infrastructure" with the chain devops
// and the Team payments with the chain payments Page ivan (devops) and petr (payments), both linked to the fake
// Mattermost server; the Alert Group reads its Assignees with their Teams, the Root messages name them as Mentions,
// "Assignees: @ivan (devops), @petr (payments)", and `assignee=me` lists it for ivan (C-32.FR-12, FR-18, AC-6, AC-10,
// AC-13); a bridge Destination added to the Route gets a `layout` naming each Assignee with their Team and the `ref`
// of a linked one's Mention entry; the events of an outgoing webhook carry the Assignees; a Service account gets 422
// for `me`; ivan's acknowledgement, while devops still runs, clears them with assignees_changed and reads
// "Acknowledged by @ivan · Escalation chains stopped", the Owner named without a Team (C-32.AC-2, AC-11, P-89), and the
// Alert Group leaves his list. Beyond it: a person Paged without an Account link never becomes an Assignee and a later
// step replaces them; a chain that ran out keeps its Assignees on an Unclaimed Alert Group (C-32.AC-10); Snooze and
// Resolve clear them.
func TestAssignees(t *testing.T) {
	h := Start(t, DevProcess)
	rep := h.Replicas[len(h.Replicas)-1]
	e := newMM(t, h, h.Replicas[0])
	r := &runs{mm: e}
	api := e.api
	admin := newAgentSignedIn(t, h.Replicas[0])
	ivan, ivanSession := pagedPerson(t, admin, h.Replicas[0].App, "ivan")
	petr, petrSession := pagedPerson(t, admin, h.Replicas[0].App, "petr")
	olga, _ := pagedPerson(t, admin, h.Replicas[0].App, "olga")

	conn := e.connection(200)
	e.linkMattermost(ivanSession, conn, fakemattermost.IvanUsername, fakemattermost.IvanUserID)
	e.linkMattermost(petrSession, conn, fakemattermost.PetrUsername, fakemattermost.PetrUserID)
	dests := map[string]string{}
	for _, name := range []string{"infra-critical", "infra-notice", "payments-critical", "payments-notice"} {
		dests[name] = e.destination(name, conn, "ch-"+name, nil, 100)
	}
	entry := func(id, cond string) map[string]string {
		return map[string]string{"destination_id": id, "severity_condition": cond}
	}
	team := func(slug, member string, entries []map[string]string) string {
		t.Helper()
		if entries == nil {
			entries = []map[string]string{}
		}
		b, _ := json.Marshal(map[string]any{"name": slug, "slug": slug, "own_working_hours": false,
			"destination_entries": entries})
		id := api.json(http.MethodPost, "/api/v1/teams", string(b), http.StatusCreated)["id"].(string)
		api.json(http.MethodPost, "/api/v1/teams/"+id+"/users", `{"user_id":"`+member+`"}`, http.StatusCreated)
		return id
	}
	tp := team("payments", petr, []map[string]string{entry(dests["payments-critical"], "critical"),
		entry(dests["payments-notice"], "below_critical")})
	td := team("devops", ivan, nil)
	monday := readClock(t, rep).Now.UTC().AddDate(0, 0, -7).Format("2006-01-02")
	schedule := func(teamID, person string) string {
		t.Helper()
		return api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+teamID+`","name":"Main",`+
			`"time_zone":"UTC","rotation_layers":[{"name":"Week","user_ids":["`+person+`"],"shift_length":"week",`+
			`"handover_time":"10:00","handover_day":"monday","start_date":"`+monday+`","limits":null}]}`,
			http.StatusCreated)["id"].(string)
	}
	pm, dm := schedule(tp, petr), schedule(td, ivan)
	chain := func(name, owner, steps string) string {
		t.Helper()
		return api.json(http.MethodPost, "/api/v1/escalation-chains", `{"name":"`+name+`","owning_team_id":"`+owner+
			`","steps":`+steps+`}`, http.StatusCreated)["id"].(string)
	}
	cp := chain("payments", tp, `[{"kind":"page_on_call","schedule_id":"`+pm+`","importance":"normal"}]`)
	// devops still runs, waiting for its step 3, when ivan acknowledges, so that the acknowledgement stops it.
	cd := chain("devops", td, `[{"kind":"page_on_call","schedule_id":"`+dm+`","importance":"normal"},
		{"kind":"wait","wait_seconds":3600},{"kind":"page_on_call","schedule_id":"`+dm+`","importance":"important"}]`)
	chainEntries := func(ids ...string) []map[string]string {
		out := []map[string]string{}
		for _, id := range ids {
			out = append(out, map[string]string{"chain_id": id})
		}
		return out
	}
	r.putTeam(tp, func(m map[string]any) { m["chain_entries"] = chainEntries(cp) })
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
	route := func(name, category string, entries []map[string]string, chains ...string) string {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"name": name, "group_key": []string{"alertname", "system"},
			"deliver_to_teams": true, "run_team_chains": true, "destination_entries": entries,
			"chain_entries": chainEntries(chains...), "policy": paging,
			"matchers": []map[string]string{{"label": "category", "op": "=", "value": category}}})
		return api.json(http.MethodPost, "/api/v1/routes", string(b), http.StatusCreated)["id"].(string)
	}
	fire := func(group, alertname, labels string) string {
		t.Helper()
		e.group(group, alertname)
		e.alert(group, "a", labels, "")
		e.notify(group, "first notification")
		var l map[string]string
		_ = json.Unmarshal([]byte(labels), &l)
		return e.groupOf(l["cluster"])
	}

	// The Verification, with an outgoing webhook in events mode on the Route.
	hook := e.webhook("assignees-events", "assignees", `[]`)["id"].(string)
	infra := route("Infrastructure", "infra", []map[string]string{entry(dests["infra-critical"], "critical"),
		entry(dests["infra-notice"], "below_critical"), entry(hook, "any")}, cd)
	g := fire("a1", "KubernetesContainerOOMKilled",
		`{"category":"infra","system":"k8s","team":"payments","severity":"warning","cluster":"a1a"}`)
	e.waitAssignees(g, "ivan/devops", "petr/payments")
	t.Logf("assignees of %s: ivan/devops, petr/payments", g)
	both := "Assignees: @ivan (devops), @petr (payments)"
	var infraText, paymentsText string
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("root messages: infra %q, payments %q", infraText, paymentsText)
		}
	})
	eventually(t, "the Assignees in both Root messages", func() bool {
		infraText, paymentsText = e.rootText(g, "ch-infra-notice"), e.rootText(g, "ch-payments-notice")
		return strings.Contains(infraText, both) && strings.Contains(paymentsText, both)
	})
	t.Logf("root message: %q", both)
	byAdmin := func(method, target, body string) answer { return api.do(method, target, body) }
	if !assigned(t, ivanSession.do, "me", g) || !assigned(t, byAdmin, petr, g) || assigned(t, byAdmin, olga, g) {
		t.Errorf("the filter assignee before the acknowledgement")
	}

	// A bridge Destination added to the Route publishes a new Root message whose layout names the Assignees with their
	// Teams, ivan, linked through its Connection, with the ref of his entry in `mentions`.
	b := &brd{mm: e, fb: h.Fakes.Bridge + "/_fake"}
	for _, item := range api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
		if c := item.(map[string]any); c["name"] == devmode.BridgeConnectionName {
			b.conn = c["id"].(string)
		}
	}
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
		display_name, created_at) SELECT 1, 'AK0000000000JV', u.id, 'bridge', c.id, '`+fakebridge.BobID+`', u.name, now()
		FROM users u, connections c WHERE u.login = 'ivan' AND c.public_id = '`+b.conn+`'`)
	bridgeDest := b.destination("assignees-bridge", fakebridge.ChannelAlerts)
	a := api.do(http.MethodGet, "/api/v1/routes/"+infra, "")
	var rt map[string]any
	decode(t, a, &rt)
	entries := rt["destination_entries"].([]any)
	entries = append(entries, map[string]any{"destination_id": bridgeDest, "severity_condition": "any"})
	body, _ := json.Marshal(map[string]any{"name": rt["name"], "group_key": rt["group_key"],
		"deliver_to_teams": rt["deliver_to_teams"], "run_team_chains": rt["run_team_chains"],
		"destination_entries": entries, "chain_entries": rt["chain_entries"], "policy": rt["policy"],
		"matchers": rt["matchers"], "urgent": rt["urgent"]})
	api.json(http.MethodPut, "/api/v1/routes/"+infra, string(body), http.StatusOK, "If-Match", a.header.Get("ETag"))
	root := b.waitIn(fakebridge.ChannelAlerts, "KubernetesContainerOOMKilled")
	l := root.Envelope.Layout
	if l == nil || len(l.Assignees) != 2 {
		t.Fatalf("the bridge layout %+v", l)
	}
	refs := map[string]string{}
	for _, p := range l.Assignees {
		ref, err := p.Ref.Get()
		if err != nil {
			ref = "null"
		}
		team := ""
		if p.Team != nil {
			team = p.Team.Name
		}
		refs[p.Name+"/"+team] = ref
	}
	i := slices.IndexFunc(root.Envelope.Mentions, func(m gen.Mention) bool { return m.ExternalId == fakebridge.BobID })
	if i < 0 || refs["ivan/devops"] != root.Envelope.Mentions[i].Ref || refs["petr/payments"] != "null" ||
		ownerOf(t, l) != "null" || slices.ContainsFunc(l.Notices, func(n string) bool {
		return strings.Contains(n, "Assignees")
	}) {
		t.Errorf("the bridge layout: assignees %v, mentions %+v, notices %q", refs, root.Envelope.Mentions, l.Notices)
	}
	t.Logf("bridge layout assignees: %v", refs)

	// A Service account is no Assignee.
	sa := api.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"assignees","role":"viewer"}`,
		http.StatusCreated)["id"].(string)
	sat := patClient{t: t, base: h.Replicas[0].App, token: admin.json(http.MethodPost,
		"/api/v1/service-accounts/"+sa+"/tokens", `{"name":"assignees"}`, http.StatusCreated)["value"].(string)}
	if a := sat.do(http.MethodGet, "/api/v1/alert-groups?assignee=me", ""); a.status !=
		http.StatusUnprocessableEntity || !strings.Contains(string(a.body), "/query/assignee") {
		t.Errorf("assignee=me by a service account = %d %s", a.status, a.body)
	}

	if a := ivanSession.do(http.MethodPost, "/api/v1/alert-groups/"+g+"/acknowledge", ""); a.status != http.StatusOK {
		t.Fatalf("acknowledge = %d %s", a.status, a.body)
	}
	e.waitAssignees(g)
	stopped := "Acknowledged by @ivan · Escalation chains stopped"
	eventually(t, "the acknowledgement in the Root message", func() bool {
		return strings.Contains(e.rootText(g, "ch-payments-notice"), stopped)
	})
	if text := e.rootText(g, "ch-payments-notice"); strings.Contains(text, "(devops)") ||
		strings.Contains(text, "Assignees") {
		t.Errorf("after the acknowledgement %q", text)
	}
	t.Logf("root message: %q", stopped)
	if got := e.changesOf(g); len(got) < 2 || got[len(got)-1] != "" ||
		!slices.Contains(got, "ivan,petr") {
		t.Errorf("assignees_changed %q", got)
	}
	if assigned(t, ivanSession.do, "me", g) {
		t.Error("the acknowledged Alert Group is still in ivan's list")
	}

	// The events of the webhook carry the Assignees: two before the acknowledgement, none after.
	eventually(t, "the assignees_changed events", func() bool {
		var recs []struct {
			Body struct {
				Event      string `json:"event"`
				AlertGroup struct {
					ID        string `json:"id"`
					Assignees []struct {
						User struct {
							Login string `json:"login"`
						} `json:"user"`
						Team *struct {
							Slug string `json:"slug"`
						} `json:"team"`
					} `json:"assignees"`
				} `json:"alert_group"`
			} `json:"body"`
		}
		decode(t, call(t, http.MethodGet, webhookBase+"/_fake/received/assignees", ""), &recs)
		full, cleared := false, false
		for _, rec := range recs {
			if rec.Body.Event != "assignees_changed" || rec.Body.AlertGroup.ID != g {
				continue
			}
			as := rec.Body.AlertGroup.Assignees
			full = full || (len(as) == 2 && as[0].Team != nil && as[1].Team != nil)
			cleared = cleared || len(as) == 0
		}
		return full && cleared
	})

	t.Run("an unlinked person and a later step", func(t *testing.T) {
		e := e.on(t)
		rc := chain("replace", td, `[{"kind":"page_people","user_ids":["`+olga+`","`+ivan+`"],"importance":"normal"},
			{"kind":"wait","wait_seconds":600},{"kind":"page_people","user_ids":["`+petr+`"],"importance":"normal"}]`)
		route("Replace", "replace", []map[string]string{entry(dests["infra-notice"], "any")}, rc)
		g := fire("a2", "Replace", `{"category":"replace","system":"r","cluster":"a2a"}`)
		e.waitAssignees(g, "ivan/devops")
		eventually(t, "olga not delivered", func() bool {
			return slices.Contains(e.pageEntries(g, "page_not_delivered"), "page_not_delivered/no_account_link/olga")
		})
		advance(t, rep, 600)
		e.waitAssignees(g, "petr/devops")
		if got := e.changesOf(g); !slices.Equal(got, []string{"ivan", "petr"}) {
			t.Errorf("assignees_changed %q", got)
		}
		if strings.Contains(e.rootText(g, "ch-infra-notice"), "ivan") {
			t.Errorf("the replaced assignee in %q", e.rootText(g, "ch-infra-notice"))
		}
	})

	t.Run("snooze and resolve clear them", func(t *testing.T) {
		e := e.on(t)
		cl := chain("clear", td, `[{"kind":"page_people","user_ids":["`+ivan+`"],"importance":"normal"}]`)
		route("Clear", "clear", []map[string]string{entry(dests["infra-notice"], "any")}, cl)
		snoozed := fire("a3", "ClearSnooze", `{"category":"clear","system":"s","cluster":"a3a"}`)
		resolved := fire("a4", "ClearResolve", `{"category":"clear","system":"v","cluster":"a4a"}`)
		e.waitAssignees(snoozed, "ivan/devops")
		e.waitAssignees(resolved, "ivan/devops")
		// The one-step chain ran out: Unclaimed, and the Assignees stay with the Alert Group in ivan's list.
		eventually(t, "unclaimed", func() bool {
			got, unclaimed := e.assigneesOf(snoozed)
			return unclaimed && len(got) == 1
		})
		if !assigned(t, ivanSession.do, "me", snoozed) {
			t.Errorf("an Unclaimed Alert Group left the list of its Assignee")
		}
		api.json(http.MethodPost, "/api/v1/alert-groups/"+snoozed+"/snooze", `{"no_end":true}`, http.StatusOK)
		api.json(http.MethodPost, "/api/v1/alert-groups/"+resolved+"/resolve", "", http.StatusOK)
		for _, g := range []string{snoozed, resolved} {
			e.waitAssignees(g)
			if got := e.changesOf(g); !slices.Equal(got, []string{"ivan", ""}) {
				t.Errorf("%s: assignees_changed %q", g, got)
			}
			if assigned(t, ivanSession.do, "me", g) {
				t.Errorf("%s is still in ivan's list", g)
			}
		}
	})
}
