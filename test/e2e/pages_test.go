// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// zeroWidth is the zero-width space that keeps an `@` of a text from mentioning anyone.
var zeroWidth = string(rune(0x200b))

// pageEntry is an escalation entry of the Timeline.
type pageEntry struct {
	Event     string  `json:"event"`
	Reason    *string `json:"reason"`
	PagedUser []struct {
		Login string `json:"login"`
	} `json:"paged_users"`
	SkipReason *string `json:"skip_reason"`
	Team       *struct {
		Slug string `json:"slug"`
	} `json:"team"`
	Everyone   *string `json:"everyone"`
	Importance *string `json:"importance"`
}

// pageFields are the paged entries of the Alert Group g, in their order, each as its importance, team:<slug> or
// everyone:<word>, "-" for none.
func (e *mm) pageFields(g string) []string {
	e.t.Helper()
	var page struct {
		Items []pageEntry `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?kind=escalation&order=asc&limit=200",
		""), &page)
	out := []string{}
	for _, x := range page.Items {
		if x.Event != "paged" {
			continue
		}
		var s []string
		if x.Importance != nil {
			s = append(s, *x.Importance)
		}
		if x.Team != nil {
			s = append(s, "team:"+x.Team.Slug)
		}
		if x.Everyone != nil {
			s = append(s, "everyone:"+*x.Everyone)
		}
		if len(s) == 0 {
			s = []string{"-"}
		}
		out = append(out, strings.Join(s, "/"))
	}
	return out
}

// runPages are the steps of the newest run of the chain as number/outcome: and each of their Pages as
// kind/outcome/logins/team:<slug>/everyone:<word>, as "How it went" lists them.
func (e *mm) runPages(chain string) []string {
	e.t.Helper()
	var runs struct {
		Items []struct {
			Steps []struct {
				Number  int    `json:"number"`
				Outcome string `json:"outcome"`
				Pages   []struct {
					Kind  string `json:"kind"`
					Users []struct {
						Login string `json:"login"`
					} `json:"users"`
					Team *struct {
						Slug string `json:"slug"`
					} `json:"team"`
					Everyone *string `json:"everyone"`
					Outcome  string  `json:"outcome"`
				} `json:"pages"`
			} `json:"steps"`
		} `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/escalation-chains/"+chain+"/runs", ""), &runs)
	out := []string{}
	if len(runs.Items) == 0 {
		return out
	}
	for _, st := range runs.Items[0].Steps {
		s := fmt.Sprintf("%d/%s:", st.Number, st.Outcome)
		for _, p := range st.Pages {
			s += " " + p.Kind + "/" + p.Outcome
			for _, u := range p.Users {
				s += "/" + u.Login
			}
			if p.Team != nil {
				s += "/team:" + p.Team.Slug
			}
			if p.Everyone != nil {
				s += "/everyone:" + *p.Everyone
			}
		}
		out = append(out, s)
	}
	return out
}

// String is the entry as event/reason/logins.
func (p pageEntry) String() string {
	s := p.Event
	if p.Reason != nil {
		s += "/" + *p.Reason
	}
	if p.SkipReason != nil {
		s += "/" + *p.SkipReason
	}
	for _, u := range p.PagedUser {
		s += "/" + u.Login
	}
	return s
}

// pageEntries are the escalation entries of the Alert Group g of the events, in their order.
func (e *mm) pageEntries(g string, events ...string) []string {
	e.t.Helper()
	var page struct {
		Items []pageEntry `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?kind=escalation&order=asc&limit=200",
		""), &page)
	out := []string{}
	for _, x := range page.Items {
		if slices.Contains(events, x.Event) {
			out = append(out, x.String())
		}
	}
	return out
}

// repliesIn are the Thread replies of the fake Mattermost server in the channel, without the zero-width spaces.
func (e *mm) repliesIn(channel string) []string {
	e.t.Helper()
	var out []string
	for _, p := range e.posts() {
		if p.ChannelID == channel && p.RootID != "" {
			out = append(out, strings.ReplaceAll(p.Message, zeroWidth, ""))
		}
	}
	return out
}

// countIn counts the Thread replies in the channel that contain s.
func (e *mm) countIn(channel, s string) int {
	e.t.Helper()
	n := 0
	for _, r := range e.repliesIn(channel) {
		n += strings.Count(r, s)
	}
	return n
}

// pagedPerson creates a Responder with a password and returns their id and their signed-in web session.
func pagedPerson(t *testing.T, admin *agent, base, login string) (string, *agent) {
	t.Helper()
	created := admin.json(http.MethodPost, "/api/v1/users", `{"name":"`+login+`","login":"`+login+
		`","role":"responder"}`, http.StatusCreated)
	link, _ := url.Parse(created["password_setup_link"].(map[string]any)["url"].(string))
	setup, _ := strings.CutPrefix(link.Fragment, "token=")
	password := login + "-password-1"
	if a := admin.do(http.MethodPost, "/api/v1/password-setups", `{"token":"`+setup+`","password":"`+password+
		`"}`); a.status != http.StatusNoContent {
		t.Fatalf("password setup of %s = %d %s", login, a.status, a.body)
	}
	session := newAgent(t, base)
	if a := session.signIn(login, password); a.status != http.StatusCreated {
		t.Fatalf("sign in of %s = %d %s", login, a.status, a.body)
	}
	return created["user"].(map[string]any)["id"].(string), session
}

// linkMattermost links the person of the session to the Mattermost account username of the fake, through the code
// the bot sends them (C-18).
func (e *mm) linkMattermost(session *agent, conn, username, userID string) {
	e.t.Helper()
	var started struct {
		RequestID string `json:"request_id"`
	}
	a := session.do(http.MethodPost, "/api/v1/me/account-links/mattermost", `{"connection_id":"`+conn+
		`","username":"`+username+`"}`)
	if a.status != http.StatusAccepted {
		e.t.Fatalf("start the mattermost link of %s = %d %s", username, a.status, a.body)
	}
	decode(e.t, a, &started)
	var dms []fakemattermost.DM
	decode(e.t, call(e.t, http.MethodGet, e.fmm+"/dms", ""), &dms)
	if len(dms) == 0 || dms[len(dms)-1].UserID != userID {
		e.t.Fatalf("no code for %s in %+v", username, dms)
	}
	_, code, _ := strings.Cut(dms[len(dms)-1].Message, "\n\n")
	session.json(http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation",
		`{"request_id":"`+started.RequestID+`","code":"`+code+`"}`, http.StatusCreated)
}

// TestPages is S-155 against `muster dev`, as its Verification: the Route "Infrastructure" with its chain devops and
// the Team payments with its chain payments Page a warning Alert Group of payments each in the Threads of its own
// Destinations — "Step 1: @ivan, you are needed" in infra-notice, and in payments-notice the line of petr, who has no
// Mattermost Account link (C-32.AC-1, AC-6, FR-8, FR-9); the Timeline records paged and page_not_delivered
// no_account_link (C-32.AC-11); 10 minutes later devops Pages importantly, "again in 5 min", and the important rule
// Mentions Ivan again 5 minutes after (C-31.AC-1); muster_pages_total counts the Pages. Beyond it: an acknowledgement
// between the two Mentions of the important rule stops it (C-31.AC-1, FR-3); a chain that the Route and the Team both
// name Pages once in the Threads of both (C-32.AC-16); a Route with a chain and no Destination records
// page_not_delivered no_root_message (C-32.AC-7); a step "Page the whole Team" Pages the Team mention and everyone in
// the chat by the token, and a Team without a Team mention for the Connection is skipped with no_mention_possible
// (C-32.AC-13); a reply that the fake refuses ends Not delivered and records page_not_delivered not_delivered
// (C-32.FR-10); a Page in a Slack Thread mentions the person by <@U…> (C-32.FR-11); Pages in Telegram, through
// Bridges and to outgoing webhooks (testPagesBeyondMattermost).
func TestPages(t *testing.T) {
	h := Start(t, DevProcess)
	rep := h.Replicas[len(h.Replicas)-1]
	e := newMM(t, h, h.Replicas[0])
	r := &runs{mm: e}
	api := e.api
	admin := newAgentSignedIn(t, h.Replicas[0])
	ivan, ivanSession := pagedPerson(t, admin, h.Replicas[0].App, "ivan")
	petr, _ := pagedPerson(t, admin, h.Replicas[0].App, "petr")

	conn := e.connection(200)
	e.linkMattermost(ivanSession, conn, fakemattermost.IvanUsername, fakemattermost.IvanUserID)
	dests := map[string]string{}
	for _, name := range []string{"infra-critical", "infra-notice", "payments-critical", "payments-notice"} {
		dests[name] = e.destination(name, conn, "ch-"+name, nil, 100)
	}
	entry := func(name, cond string) map[string]string {
		return map[string]string{"destination_id": dests[name], "severity_condition": cond}
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
	tp := team("payments", petr, []map[string]string{entry("payments-critical", "critical"),
		entry("payments-notice", "below_critical")})
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
	devopsSteps := `[{"kind":"page_on_call","schedule_id":"` + dm + `"},{"kind":"wait","wait_seconds":600},
		{"kind":"page_on_call","schedule_id":"` + dm + `","importance":"important"}]`
	cd := chain("devops", td, devopsSteps)
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
	routeOf := func(name, category string, entries []map[string]string, toTeams bool, chains ...string) string {
		t.Helper()
		if entries == nil {
			entries = []map[string]string{}
		}
		b, _ := json.Marshal(map[string]any{"name": name, "group_key": []string{"alertname", "system"},
			"deliver_to_teams": toTeams, "run_team_chains": toTeams, "destination_entries": entries,
			"chain_entries": chainEntries(chains...), "policy": paging,
			"matchers": []map[string]string{{"label": "category", "op": "=", "value": category}}})
		return api.json(http.MethodPost, "/api/v1/routes", string(b), http.StatusCreated)["id"].(string)
	}
	route := func(name, category string, entries []map[string]string, chains ...string) string {
		t.Helper()
		return routeOf(name, category, entries, true, chains...)
	}
	route("Infrastructure", "infra", []map[string]string{entry("infra-critical", "critical"),
		entry("infra-notice", "below_critical")}, cd)
	fire := func(group, alertname, labels string) string {
		t.Helper()
		e.group(group, alertname)
		e.alert(group, "a", labels, "")
		e.notify(group, "first notification")
		var l map[string]string
		_ = json.Unmarshal([]byte(labels), &l)
		return e.groupOf(l["cluster"])
	}

	// The Verification.
	g := fire("p1", "KubernetesContainerOOMKilled",
		`{"category":"infra","system":"k8s","team":"payments","severity":"warning","cluster":"p1a"}`)
	infra := "Step 1: @ivan, you are needed\nChain of the Route Infrastructure · On-call of devops Main · normal"
	payments := "Page not delivered: @petr has no linked Mattermost account\n" +
		"Chain of the Team payments · On-call of payments Main · normal"
	eventually(t, "the Pages in both Threads", func() bool {
		return slices.Contains(e.repliesIn("ch-infra-notice"), infra) &&
			slices.Contains(e.repliesIn("ch-payments-notice"), payments)
	})
	if got := e.repliesIn("ch-infra-notice"); slices.Contains(got, payments) {
		t.Errorf("the payments chain Paged in infra-notice: %q", got)
	}
	if got := e.repliesIn("ch-payments-notice"); slices.Contains(got, infra) {
		t.Errorf("the devops chain Paged in payments-notice: %q", got)
	}
	want := []string{"page_not_delivered/no_account_link/petr", "paged/ivan", "paged/petr"}
	eventually(t, "the escalation entries", func() bool {
		return slices.Equal(sortedCopy(e.pageEntries(g, "paged", "page_not_delivered")), want)
	})
	advance(t, rep, 600)
	eventually(t, "the important Page", func() bool {
		return e.countIn("ch-infra-notice", "important, again in 5 min") == 1
	})
	advance(t, rep, 300)
	eventually(t, "the repeat of the important rule", func() bool {
		return e.countIn("ch-infra-notice", "you are needed") == 3 &&
			e.countIn("ch-infra-notice", "Step 3: @ivan, you are needed\n"+
				"Chain of the Route Infrastructure · On-call of devops Main · important, again") == 2
	})
	// A Page is counted by the replica that ended it, so the count is the sum over the replicas.
	pagesCounted := func(series string) int {
		n := 0
		for _, x := range h.Replicas {
			n += metricValue(t, x, series)
		}
		return n
	}
	eventually(t, "the Pages counted", func() bool {
		return pagesCounted(`muster_pages_total{chain="`+cp+`",kind="mention",outcome="not_delivered"}`) == 1 &&
			pagesCounted(`muster_pages_total{chain="`+cd+`",kind="mention",outcome="delivered"}`) == 3
	})
	// pagesOf counts the Pages of the Alert Group g in the database, those of the later Mentions of a rule with again.
	pagesOf := func(t *testing.T, g string, again bool) int {
		t.Helper()
		n, err := strconv.Atoi(h.dump(t, fmt.Sprintf(`SELECT count(*)::text FROM pages p
			JOIN alert_groups a ON a.id = p.alert_group_id WHERE a.public_id = '%s' AND p.again = %t`, g, again)))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// control fires an Alert Group that nobody acknowledges, whose important rule Mentions ivan again 5 minutes after
	// the first: once that later Mention is made, the timer worker has processed an advance past it, so an absent
	// Page of another Alert Group is evidence and not a race with the worker.
	ack := chain("ack", td, `[{"kind":"page_people","user_ids":["`+ivan+`"],"importance":"important"}]`)
	route("Control", "control", []map[string]string{entry("payments-critical", "any")}, ack)
	control := func(t *testing.T, system string) string {
		t.Helper()
		c := fire(system, "Control", `{"category":"control","system":"`+system+`","cluster":"`+system+`"}`)
		eventually(t, "the first Mention of the control", func() bool { return pagesOf(t, c, false) == 1 })
		return c
	}
	// The chain of g and its rule runs ended already, so this guards against Pages that would come of the
	// acknowledgement itself; the stop of a rule run that still waits is the subtest below.
	c1 := control(t, "pc1")
	before := pagesOf(t, g, false) + pagesOf(t, g, true)
	api.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/acknowledge", "", http.StatusOK)
	advance(t, rep, 1800)
	eventually(t, "the later Mention of the control", func() bool { return pagesOf(t, c1, true) == 1 })
	if n := e.countIn("ch-infra-notice", "you are needed"); n != 3 {
		t.Errorf("%d Pages after the acknowledgement, want 3", n)
	}
	if n := pagesOf(t, g, false) + pagesOf(t, g, true); n != before {
		t.Errorf("%d Pages after the acknowledgement, want %d", n, before)
	}
	// "How it went" lists the Pages of each step of the run of devops (C-32.AC-17, FR-18): step 1 Paged ivan, the
	// wait none, and step 3 its first Mention and the later Mention of the important rule it started, all delivered;
	// the paged entries of the Timeline carry their importance (C-32.FR-19).
	devopsPages := []string{"1/made: mention/delivered/ivan", "2/made:",
		"3/made: mention/delivered/ivan mention/delivered/ivan"}
	eventually(t, "the Pages of the run of devops", func() bool {
		return slices.Equal(e.runPages(cd), devopsPages)
	})
	if got := e.pageFields(g); !slices.Equal(got, []string{"normal", "normal", "important", "important"}) {
		t.Errorf("the importance of the paged entries %v", got)
	}

	t.Run("an acknowledgement between two Mentions of the rule", func(t *testing.T) {
		e := e.on(t)
		route("Ack", "ack", []map[string]string{entry("infra-notice", "any")}, ack)
		g := fire("p7", "Ack", `{"category":"ack","system":"a","cluster":"p7a"}`)
		eventually(t, "the first Mention", func() bool {
			return e.countIn("ch-infra-notice", "Chain of the Route Ack · @ivan · important, again in 5 min") == 1
		})
		c := control(t, "pc2")
		api.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/acknowledge", "", http.StatusOK)
		advance(t, rep, 600)
		eventually(t, "the later Mention of the control", func() bool { return pagesOf(t, c, true) == 1 })
		if n := e.countIn("ch-infra-notice", "Chain of the Route Ack"); n != 1 {
			t.Errorf("%d Mentions of the acknowledged Alert Group, want 1", n)
		}
		if n := pagesOf(t, g, true); n != 0 {
			t.Errorf("%d later Mentions of the acknowledged Alert Group, want 0", n)
		}
	})

	t.Run("a chain of the Route and the Team Pages once in both Threads", func(t *testing.T) {
		e := e.on(t)
		route("Shared", "shared", []map[string]string{entry("infra-notice", "any")}, cp)
		g := fire("p2", "Shared", `{"category":"shared","system":"s","team":"payments","severity":"warning",
			"cluster":"p2a"}`)
		eventually(t, "the Pages of the shared chain", func() bool {
			return e.countIn("ch-infra-notice", "Chain of the Route Shared · On-call of payments Main") == 1 &&
				e.countIn("ch-payments-notice", "Chain of the Team payments · On-call of payments Main") == 2
		})
		if got := e.pageEntries(g, "paged"); !slices.Equal(got, []string{"paged/petr"}) {
			t.Errorf("paged %v", got)
		}
	})

	t.Run("a Route with a chain and no Destination", func(t *testing.T) {
		e := e.on(t)
		lone := chain("lone", td, `[{"kind":"page_people","user_ids":["`+ivan+`"]}]`)
		routeOf("Lone", "lone", nil, false, lone)
		g := fire("p3", "Lone", `{"category":"lone","system":"l","cluster":"p3a"}`)
		eventually(t, "no_root_message", func() bool {
			return slices.Equal(e.pageEntries(g, "paged", "page_not_delivered"),
				[]string{"page_not_delivered/no_root_message/ivan"})
		})
	})

	t.Run("a step Page the whole Team", func(t *testing.T) {
		e := e.on(t)
		r.putTeam(td, func(m map[string]any) {
			m["team_mentions"] = []map[string]string{{"connection_id": conn, "group": "devops-oncall"}}
		})
		teams := chain("teams", td, `[{"kind":"page_team"},{"kind":"page_team","everyone":"here"},
			{"kind":"page_team","team_id":"`+tp+`"}]`)
		route("Teams", "teams", []map[string]string{entry("infra-notice", "any")}, teams)
		g := fire("p4", "Teams", `{"category":"teams","system":"t","cluster":"p4a"}`)
		eventually(t, "the Pages of the Team and of everyone", func() bool {
			return e.countIn("ch-infra-notice", "Step 1: @devops-oncall, you are needed\n"+
				"Chain of the Route Teams · Team devops") == 1 &&
				e.countIn("ch-infra-notice", "Step 2: @here, you are needed\nChain of the Route Teams · "+
					"everyone in the chat") == 1
		})
		if got := e.pageEntries(g, "paged", "escalation_step_skipped"); !slices.Equal(got, []string{"paged", "paged",
			"escalation_step_skipped/no_mention_possible"}) {
			t.Errorf("entries %v", got)
		}
		// The paged entries and "How it went" name the Team of step 1 and the word of step 2, without people or an
		// importance (C-32.FR-18, FR-19).
		if got := e.pageFields(g); !slices.Equal(got, []string{"team:devops", "everyone:here"}) {
			t.Errorf("the paged entries of a Team %v", got)
		}
		want := []string{"1/made: mention/delivered/team:devops", "2/made: mention/delivered/everyone:here",
			"3/skipped:"}
		eventually(t, "the Pages of the run of teams", func() bool {
			return slices.Equal(e.runPages(teams), want)
		})
	})

	t.Run("a reply that ends Not delivered", func(t *testing.T) {
		e := e.on(t)
		later := chain("later", td, `[{"kind":"wait","wait_seconds":60},{"kind":"page_people","user_ids":["`+ivan+
			`"]}]`)
		route("Later", "later", []map[string]string{entry("infra-critical", "any")}, later)
		g := fire("p5", "Later", `{"category":"later","system":"l","cluster":"p5a"}`)
		eventually(t, "the root message", func() bool {
			return slices.ContainsFunc(e.posts(), func(p mmPost) bool {
				return p.ChannelID == "ch-infra-critical" && p.RootID == ""
			})
		})
		// A 400 other than the deleted root's is an unknown answer, so the reply ends Not delivered; the root's own error
		// would make the Root message gone and publish it again instead.
		e.fault(fakeserver.Fault{Path: "/api/v4/posts", Status: 400,
			Body: `{"id":"api.context.invalid_body_param.app_error"}`})
		advance(t, rep, 60)
		eventually(t, "page_not_delivered not_delivered", func() bool {
			return slices.Contains(e.pageEntries(g, "page_not_delivered"), "page_not_delivered/not_delivered/ivan")
		})
		call(t, http.MethodDelete, e.fmm+"/faults", "")
	})

	t.Run("a reply retried after a transient error", func(t *testing.T) {
		e := e.on(t)
		retried := chain("retried", td, `[{"kind":"wait","wait_seconds":60},{"kind":"page_people","user_ids":["`+
			ivan+`"]}]`)
		route("Retried", "retried", []map[string]string{entry("infra-critical", "any")}, retried)
		roots := func() int {
			n := 0
			for _, p := range e.posts() {
				if p.ChannelID == "ch-infra-critical" && p.RootID == "" {
					n++
				}
			}
			return n
		}
		before := roots()
		g := fire("p8", "Retried", `{"category":"retried","system":"r","cluster":"p8a"}`)
		eventually(t, "the root message", func() bool { return roots() > before })
		// The first answer to the reply is a 503, a Transient error: the reply waits for its next attempt, and its
		// Page stays pending meanwhile, then ends delivered once the next attempt is accepted.
		e.fault(fakeserver.Fault{Path: "/api/v4/posts", Status: 503, Times: 1})
		advance(t, rep, 60)
		state := func() string {
			return h.dump(t, fmt.Sprintf(`SELECT coalesce(string_agg(p.state, ','), '') FROM pages p
				JOIN alert_groups a ON a.id = p.alert_group_id WHERE a.public_id = '%s'`, g))
		}
		eventually(t, "the Page delivered", func() bool {
			return state() == "delivered" &&
				e.countIn("ch-infra-critical", "Step 2: @ivan, you are needed\nChain of the Route Retried") == 1
		})
		if got := e.pageEntries(g, "page_not_delivered"); len(got) != 0 {
			t.Errorf("page_not_delivered after the retry: %v", got)
		}
	})

	t.Run("a Page in a Slack Thread", func(t *testing.T) {
		fs := h.Fakes.Slack + "/_fake"
		sl := api.json(http.MethodPost, "/api/v1/connections", `{"type":"slack","name":"sl","event_mode":"socket",
			"bot_token":"`+fakeslack.SecondBotToken+`","app_token":"`+fakeslack.SecondAppToken+`","proxy":{"enabled":false},
			"limiter":{"limit":50,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
		api.json(http.MethodPost, "/api/v1/connections/"+sl+"/checks", "", http.StatusOK)
		created := api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"slack","name":"slack",
			"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":50,"per_seconds":1}}`, sl,
			fakeslack.ChannelAlerts, mentionSettings(map[string]string{})), http.StatusCreated)
		sd := created["destination"].(map[string]any)["id"].(string)
		bob, bobSession := pagedPerson(t, admin, h.Replicas[0].App, "bobslack")
		a := bobSession.do(http.MethodPost, "/api/v1/me/account-links/slack",
			fmt.Sprintf(`{"connection_id":%q,"handle":"@BOB"}`, sl))
		var st struct {
			RequestID string `json:"request_id"`
		}
		decode(t, a, &st)
		var dms []fakeslack.Message
		decode(t, call(t, http.MethodGet, fs+"/dms?"+url.Values{"app": {fakeslack.AppB},
			"user": {fakeslack.UserBob}}.Encode(), ""), &dms)
		if len(dms) == 0 {
			t.Fatalf("no code: %s", a.body)
		}
		_, code, _ := strings.Cut(dms[len(dms)-1].Text, "\n\n")
		bobSession.json(http.MethodPost, "/api/v1/me/account-links/slack/confirmation", `{"request_id":"`+
			st.RequestID+`","code":"`+code+`"}`, http.StatusCreated)
		slackChain := chain("slack", td, `[{"kind":"page_people","user_ids":["`+bob+`"]}]`)
		route("Slack", "slack", []map[string]string{{"destination_id": sd, "severity_condition": "any"}}, slackChain)
		fire("p6", "SlackPage", `{"category":"slack","system":"s","cluster":"p6a"}`)
		eventually(t, "the Page in the Slack Thread", func() bool {
			var raw []fakeslack.Message
			decode(t, call(t, http.MethodGet, fs+"/messages?app=b&channel="+fakeslack.ChannelAlerts, ""), &raw)
			for _, m := range raw {
				for _, rp := range m.Replies {
					if rp.Text == "Step 1: <@"+fakeslack.UserBob+">, you are needed" {
						return true
					}
				}
			}
			return false
		})
	})

	t.Run("Pages in Telegram, through Bridges and to outgoing webhooks", func(t *testing.T) {
		testPagesBeyondMattermost(t, h, e.on(t), ivan, td, chain, route)
	})
}

// demoConnection is the id of the demo Connection of the type named name.
func (e *mm) demoConnection(typ, name string) string {
	e.t.Helper()
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections?type="+typ, "", http.StatusOK)["items"].([]any) {
		if c := item.(map[string]any); c["name"] == name {
			return c["id"].(string)
		}
	}
	e.t.Fatalf("no demo %s Connection %q", typ, name)
	return ""
}

// bridgeReplies are the Thread replies the fake bridge received whose body contains s.
func bridgeReplies(t *testing.T, h *Harness, s string) []fakebridge.Reply {
	t.Helper()
	var all []fakebridge.Message
	decode(t, call(t, http.MethodGet, h.Fakes.Bridge+"/_fake/messages", ""), &all)
	var out []fakebridge.Reply
	for _, m := range all {
		for _, r := range m.Replies {
			if strings.Contains(r.Input.Body, s) {
				out = append(out, r)
			}
		}
	}
	return out
}

// pagedEvent is the body of an events-mode request of paged as the fake receiver recorded it.
type pagedEvent struct {
	Body struct {
		Event    string `json:"event"`
		Notify   bool   `json:"notify"`
		Mentions []struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Name  string `json:"name"`
			Login string `json:"login"`
		} `json:"mentions"`
		Page *struct {
			Users []struct {
				Login string `json:"login"`
			} `json:"users"`
			Chain *struct {
				ID string `json:"id"`
			} `json:"chain"`
			Step       *int    `json:"step"`
			Importance *string `json:"importance"`
		} `json:"page"`
	} `json:"body"`
}

// testPagesBeyondMattermost is S-177 against `muster dev`, as its Verification: the chain fanout Pages ivan, linked
// in Telegram and through the demo Bridge, in the Threads of the Route's Telegram channel — in the discussion group,
// "Step 1: <a href="tg://user?id=5001">ivan</a>, you are needed" (C-32.FR-8, FR-9, C-14.FR-3) — and of its Bridge
// Destination, "Step 1: [@Ivan](muster-mention:m1), you are needed" with ivan's entry in mentions (C-22.FR-16); its
// events-mode outgoing webhook receives paged with ivan as a user target of mentions and the Page's chain, step and
// importance (C-15.FR-2, FR-11); its template-mode webhook with "reply in thread" gets the Page through that request
// with ivan as data, one without it no Page (C-32.FR-11). Beyond it: once the bridge no longer declares
// native_mentions, its reply names ivan by the plain name and, no other Destination of that chain reaching him, the
// Page records page_not_delivered not_delivered (C-32.FR-10, FR-11).
func testPagesBeyondMattermost(t *testing.T, h *Harness, e *mm, ivan, devops string,
	chain func(name, owner, steps string) string,
	route func(name, category string, entries []map[string]string, chains ...string) string) {
	tgConn := e.demoConnection("telegram", devmode.TelegramConnectionName)
	brConn := e.demoConnection("bridge", devmode.BridgeConnectionName)
	e.fake(http.MethodPut, fmt.Sprintf("%s/_fake/chats/%d/members/%d", h.Fakes.Telegram, faketelegram.GroupID,
		faketelegram.BotID), `{"status":"administrator","can_post_messages":true}`)
	tg := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"telegram","name":"tg-pages",
		"connection_id":%q,"channel_id":"@muster_alerts","mentions":%s,"limiter":{"limit":60,"per_seconds":60}}`,
		tgConn, mentionSettings(nil)), http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	br := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":"bridge-pages",
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":60,"per_seconds":60}}`, brConn,
		fakebridge.ChannelAlerts, mentionSettings(nil)), http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	events := e.webhook("pages-events", "pages", `[]`)["id"].(string)
	reply := map[string]any{}
	if err := json.Unmarshal([]byte(chatTemplate("pages", false)), &reply); err != nil {
		t.Fatal(err)
	}
	reply["reply_in_thread"].(map[string]any)["body"] = `{"text":"{{ .Event }} {{ .Notify }}` +
		`{{ range .Mentions }} {{ mention . }}{{ end }}"}`
	withReply, _ := json.Marshal(reply)
	delete(reply, "reply_in_thread")
	for _, k := range []string{"create", "update"} {
		r := reply[k].(map[string]any)
		r["url"] = strings.Replace(r["url"].(string), "/chat/pages/", "/chat/pages-silent/", 1)
	}
	withoutReply, _ := json.Marshal(reply)
	tmpl := e.templateWebhook("pages-chat", "template", string(withReply), "", nil)
	silent := e.templateWebhook("pages-silent", "template", string(withoutReply), "", nil)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		display_name, created_at) SELECT 1, 'AK0000000000T7', u.id, 'telegram', c.id, '5001', 'ivan_tg', 'Ivan',
		now() FROM users u, connections c WHERE u.public_id = '`+ivan+`' AND c.public_id = '`+tgConn+`'`)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
		display_name, created_at) SELECT 1, 'AK0000000000B7', u.id, 'bridge', c.id, 'u-ivan', 'Ivan', now()
		FROM users u, connections c WHERE u.public_id = '`+ivan+`' AND c.public_id = '`+brConn+`'`)
	anyLevel := func(ids ...string) []map[string]string {
		var out []map[string]string
		for _, id := range ids {
			out = append(out, map[string]string{"destination_id": id, "severity_condition": "any"})
		}
		return out
	}
	fanout := chain("fanout", devops, `[{"kind":"page_people","user_ids":["`+ivan+`"],"importance":"normal"}]`)
	route("Fanout", "fanout", anyLevel(tg, br, events, tmpl, silent), fanout)
	e.group("p9", "Fanout")
	e.alert("p9", "a", `{"category":"fanout","system":"f","cluster":"p9a"}`, "")
	e.notify("p9", "first notification")
	g := e.groupOf("p9a")

	var tgText string
	eventually(t, "the Page in the discussion group", func() bool {
		var msgs []faketelegram.Message
		decode(t, call(t, http.MethodGet, fmt.Sprintf("%s/_fake/messages?chat=%d", h.Fakes.Telegram,
			faketelegram.GroupID), ""), &msgs)
		for _, m := range msgs {
			if m.From != nil && m.From.ID == faketelegram.BotID && strings.Contains(m.Text, "you are needed") {
				tgText = strings.ReplaceAll(m.Text, zeroWidth, "")
				return true
			}
		}
		return false
	})
	if want := `Step 1: <a href="tg://user?id=5001">ivan</a>, you are needed` + "\n" +
		"Chain of the Route Fanout · @ivan · normal"; tgText != want {
		t.Errorf("telegram\n%q\nwant\n%q", tgText, want)
	}
	// The bridge's info is first read and stored by its own Publication, which may come after the chain started with
	// the Publication in Telegram: the Page then found no stored info and Named ivan there, reaching him in Telegram
	// only. Either way the reply carries the same marker, rendered from what the Page decided when it was queued.
	var page []fakebridge.Reply
	eventually(t, "the Page through the bridge", func() bool {
		page = bridgeReplies(t, h, "you are needed")
		return len(page) == 1
	})
	if in := page[0].Input; !strings.HasPrefix(in.Body, "Step 1: [@Ivan](muster-mention:m1), you are needed\n\n") ||
		len(in.Mentions) != 1 || in.Mentions[0].Ref != "m1" || in.Mentions[0].ExternalId != "u-ivan" || !in.Notify {
		t.Errorf("bridge %+v", in)
	}
	var paged pagedEvent
	eventually(t, "paged to the events-mode webhook", func() bool {
		var got []pagedEvent
		decode(t, call(t, http.MethodGet, webhookBase+"/_fake/received/pages", ""), &got)
		for _, x := range got {
			if x.Body.Event == "paged" {
				paged = x
				return true
			}
		}
		return false
	})
	if b := paged.Body; !b.Notify || len(b.Mentions) != 1 || b.Mentions[0].Type != "user" || b.Mentions[0].ID != ivan ||
		b.Mentions[0].Login != "ivan" || b.Page == nil || b.Page.Chain == nil || b.Page.Chain.ID != fanout ||
		b.Page.Step == nil || *b.Page.Step != 1 || b.Page.Importance == nil || *b.Page.Importance != "normal" ||
		len(b.Page.Users) != 1 || b.Page.Users[0].Login != "ivan" {
		t.Errorf("paged %+v", b)
	}
	eventually(t, "the Page through reply in thread", func() bool {
		for _, m := range e.chat("pages").Messages {
			for _, r := range m.Replies {
				if r.Text == "paged true @ivan" {
					return true
				}
			}
		}
		return false
	})
	eventually(t, "the Page delivered", func() bool {
		return slices.Equal(e.pageEntries(g, "paged", "page_not_delivered"), []string{"paged/ivan"}) &&
			h.dump(t, `SELECT p.state FROM pages p JOIN alert_groups a ON a.id = p.alert_group_id
				WHERE a.public_id = '`+g+`'`) == "delivered"
	})
	if c := e.chat("pages-silent"); len(c.Messages) != 1 || len(c.Messages[0].Replies) != 0 {
		t.Errorf("the webhook without reply in thread %+v", c)
	}

	// A bridge without native_mentions, read by a check of its Connection on every replica.
	capabilities, _ := json.Marshal(fakebridge.DefaultConfig().Capabilities)
	t.Cleanup(func() {
		restore(t, http.MethodPut, h.Fakes.Bridge+"/_fake/config", `{"capabilities":`+string(capabilities)+`}`)
	})
	e.fake(http.MethodPut, h.Fakes.Bridge+"/_fake/config", `{"capabilities":{"native_mentions":false}}`)
	for _, rep := range h.Replicas {
		api := patClient{t: t, base: rep.App, token: e.api.token}
		if c := api.json(http.MethodPost, "/api/v1/connections/"+brConn+"/checks", "", http.StatusOK); c["ok"] != true {
			t.Fatalf("the check of the bridge: %v", c)
		}
	}
	plain := chain("plain", devops, `[{"kind":"page_people","user_ids":["`+ivan+`"]}]`)
	route("Plain", "plain", anyLevel(br), plain)
	e.group("p10", "Plain")
	e.alert("p10", "a", `{"category":"plain","system":"p","cluster":"p10a"}`, "")
	e.notify("p10", "first notification")
	g = e.groupOf("p10a")
	eventually(t, "page_not_delivered not_delivered", func() bool {
		return slices.Equal(e.pageEntries(g, "paged", "page_not_delivered"),
			[]string{"paged/ivan", "page_not_delivered/not_delivered/ivan"})
	})
	eventually(t, "the plain name through the bridge", func() bool {
		return len(bridgeReplies(t, h, "Chain of the Route Plain")) == 1
	})
	if in := bridgeReplies(t, h, "Chain of the Route Plain")[0].Input; !strings.HasPrefix(in.Body,
		"Step 1: [@Ivan](muster-mention:m1), you are needed\n\n") || strings.Contains(in.Body, "has no linked") {
		t.Errorf("plain bridge %q", in.Body)
	}
}

// sortedCopy is s sorted.
func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
