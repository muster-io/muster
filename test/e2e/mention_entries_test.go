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
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// mentionEntry is a Mention entry as a read gives it, with what it resolves to.
type mentionEntry struct {
	Target struct {
		Kind     string `json:"kind"`
		Everyone string `json:"everyone"`
		Team     *struct {
			Slug string `json:"slug"`
		} `json:"team"`
	} `json:"target"`
	Events     []string `json:"events"`
	Available  bool     `json:"available"`
	ResolvesTo []struct {
		Team *struct {
			Slug string `json:"slug"`
		} `json:"team"`
		Mentions []struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Linked bool   `json:"linked"`
		} `json:"mentions"`
	} `json:"resolves_to"`
}

// TestMentionEntries is S-140 against `muster dev`, as its Verification: a Telegram Destination refuses everyone and
// takes the On-call of a Team (C-28.AC-3); "On-call of the Team from the label" for every event and @here for a new
// Alert Group only mention, in a Mattermost Destination, the On-call of payments' main Schedule and @here on a new
// Alert Group of payments, and only the On-call on its Reopen (C-28.AC-1); "Team payments" resolves to its Team mention
// and "Team search", which has none, to nobody (C-28.AC-2); naming Destinations of the Team adds "Team payments" there,
// Telegram excepted (C-28.AC-8); the L1 form is computed from the entries, and a request with the L1 form alone is
// converted, keeping the entries L1 cannot express (C-28.FR-5). Beyond it: a Slack Destination refuses everyone and all
// and takes a user group only after its Connection's check found usergroups:read, a Bridge Destination takes channel
// only when its bridge declares it (C-28.AC-3); the first publication of a Storm summary mentions the entries for
// storm_started, and the Root messages of the Storm's first Alert Groups do not (C-28.AC-6); an outgoing webhook
// receives a Team and the On-call as data (C-15.FR-11); a Reminder mentions the Owner, whom no entry names (C-28.FR-4).
func TestMentionEntries(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	rt := &routingTeams{mm: e, dests: map[string]string{}}
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
	conns := map[string]string{}
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
		c := item.(map[string]any)
		conns[c["name"].(string)] = c["id"].(string)
	}
	cm := e.connection(200)
	rt.linkAlice(r, cm)
	me := e.api.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["user"].(map[string]any)
	admin, adminLogin := me["id"].(string), me["login"].(string)

	// DPUT sends back the Destination as read with mention_entries replaced; mentions are left out.
	dput := func(id, entries string) answer {
		t.Helper()
		read := e.api.do(http.MethodGet, "/api/v1/destinations/"+id, "")
		var body map[string]any
		decode(t, read, &body)
		for _, k := range []string{"id", "etag", "mentions", "health", "routes", "teams", "created_at", "owning_team",
			"team_name", "channel_name", "channel_title", "channel_chat_id", "discussion_group_id",
			"discussion_group_title", "signing_secret_status", "warnings", "template_error"} {
			delete(body, k)
		}
		body["mention_entries"] = json.RawMessage(entries)
		b, _ := json.Marshal(body)
		return e.api.do(http.MethodPut, "/api/v1/destinations/"+id, string(b), "If-Match", read.header.Get("ETag"))
	}
	entriesOf := func(a answer) []mentionEntry {
		t.Helper()
		var d struct {
			Entries []mentionEntry `json:"mention_entries"`
		}
		decode(t, a, &d)
		return d.Entries
	}
	saved := func(what string, a answer) []mentionEntry {
		t.Helper()
		if a.status != http.StatusOK {
			t.Fatalf("%s = %d %s", what, a.status, a.body)
		}
		return entriesOf(a)
	}
	refused := func(what string, a answer, pointer string) {
		t.Helper()
		if p, c := firstError(t, a); a.status != http.StatusUnprocessableEntity || c != "mention_unsupported" ||
			p != pointer {
			t.Errorf("%s = %d %s", what, a.status, a.body)
		}
	}
	read := func(id string) []mentionEntry {
		t.Helper()
		return entriesOf(e.api.do(http.MethodGet, "/api/v1/destinations/"+id, ""))
	}
	firstLine := func(channel string) string {
		t.Helper()
		var last mmPost
		for _, p := range e.posts() {
			if p.ChannelID == channel {
				last = p
			}
		}
		text := last.Message
		if text == "" && len(last.Attachments) > 0 {
			text = last.Attachments[0].Text
		}
		line, _, _ := strings.Cut(text, "\n")
		return line
	}

	// The Teams: payments with its Team mention payments_team for the Connection and a main Schedule whose On-call is
	// the Admin, linked to alice; search with neither.
	tp := e.api.json(http.MethodPost, "/api/v1/teams", `{"name":"payments","slug":"payments","own_working_hours":false,`+
		`"team_mentions":[{"connection_id":"`+cm+`","group":"payments_team"}]}`, http.StatusCreated)["id"].(string)
	ts := e.api.json(http.MethodPost, "/api/v1/teams", `{"name":"search","slug":"search","own_working_hours":false}`,
		http.StatusCreated)["id"].(string)
	e.api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+admin+`"}`, http.StatusCreated)
	now := readClock(t, r).Now.UTC()
	monday := now.AddDate(0, 0, -7-(int(now.Weekday())+6)%7).Format(time.DateOnly)
	e.api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Main","time_zone":"UTC",`+
		`"rotation_layers":[{"name":"Everyone","user_ids":["`+admin+`"],"shift_length":"week","handover_time":"00:00",`+
		`"handover_day":"monday","start_date":"`+monday+`","limits":null}]}`, http.StatusCreated)

	in := e.destination("infra-notice", cm, "ch-infra-notice", nil, 50)
	pn := e.destination("payments-notice", cm, "ch-payments-notice", nil, 50)
	rt.dests["infra-notice"] = in
	created := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"webhook","name":"infra-events",
		"mode":"events","events":{"url":%q,"headers":[]},"proxy":{"enabled":false},"limiter":{"limit":50,
		"per_seconds":1},"mention_entries":[{"target":{"kind":"team","team_id":%q}},{"target":{"kind":
		"teams_from_label"}},{"target":{"kind":"on_call_of_team","team_id":%q}}]}`, webhookBase+"/hook/infra", tp, tp),
		http.StatusCreated)
	secrets, _ := json.Marshal([]string{created["signing_secret"].(string)})
	e.fake(http.MethodPut, webhookBase+"/_fake/secrets/infra", string(secrets))
	rt.dests["infra-events"] = created["destination"].(map[string]any)["id"].(string)
	rt.route("Infrastructure", "infra", []string{"alertname", "system"}, false, "",
		rt.entries("infra-notice", "any", "infra-events", "any"), func(p map[string]any) {
			p["ack_timeout"] = map[string]any{"enabled": false, "first_interval_seconds": 300}
			p["reminders"] = map[string]any{"enabled": true, "first_interval_seconds": 600, "cap_seconds": 3600}
		})

	// The demo Telegram Destination.
	member, _ := json.Marshal(faketelegram.Member{Status: faketelegram.StatusAdministrator, CanPostMessages: true})
	e.fake(http.MethodPut, fmt.Sprintf("%s/_fake/chats/%d/members/%d", h.Fakes.Telegram, faketelegram.GroupID,
		faketelegram.BotID), string(member))
	dt := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"telegram","name":"tg-alerts",
		"connection_id":%q,"channel_id":"@muster_alerts","limiter":{"limit":60,"per_seconds":60}}`,
		conns[devmode.TelegramConnectionName]), http.StatusCreated)["destination"].(map[string]any)["id"].(string)

	t.Run("C-28.AC-3 telegram", func(t *testing.T) {
		refused("everyone in telegram", dput(dt, `[{"target":{"kind":"everyone","everyone":"channel"}}]`),
			"/mention_entries/0/target")
		refused("a team in telegram", dput(dt, `[{"target":{"kind":"team","team_id":"`+tp+`"}}]`),
			"/mention_entries/0/target")
		got := saved("the on-call in telegram", dput(dt, `[{"target":{"kind":"on_call_of_team","team_id":"`+tp+`"}}]`))
		if len(got) != 1 || len(got[0].Events) != 7 {
			t.Errorf("telegram entries %+v", got)
		}
	})

	var g string
	t.Run("C-28.AC-1", func(t *testing.T) {
		got := saved("infra-notice", dput(in, `[{"target":{"kind":"on_call_of_teams_from_label"}},`+
			`{"target":{"kind":"everyone","everyone":"here"},"events":["new_alert_group"]}]`))
		var names []string
		for _, r := range got[0].ResolvesTo {
			if r.Team != nil && r.Team.Slug == "payments" {
				for _, m := range r.Mentions {
					names = append(names, m.Name)
				}
			}
		}
		if !slices.Equal(names, []string{fakemattermost.AliceUsername}) {
			t.Errorf("the on-call of payments resolves to %v: %+v", names, got)
		}
		labels := `{"category":"infra","system":"k8s","team":"payments","severity":"warning","cluster":"c1"}`
		e.fake(http.MethodPut, e.fam+"/groups/m1", `{"receiver":"lab","route":"{}","labels":{"alertname":"OOMKilled"}}`)
		e.alert("m1", "a", labels, "")
		e.notify("m1", "first notification")
		eventually(t, "the root message in infra-notice", func() bool {
			return firstLine("ch-infra-notice") == "@"+fakemattermost.AliceUsername+" @here"
		})
		if got := firstLine("ch-infra-notice"); got != "@"+fakemattermost.AliceUsername+" @here" {
			t.Errorf("the new alert group mentions %q", got)
		}
		g = e.groupOf("c1")
		e.alert("m1", "a", labels, `,"status":"resolved"`)
		e.notify("m1", "resolved")
		eventually(t, "the alert group to resolve", func() bool { return e.alertGroup(g)["status"] == "resolved" })
		e.alert("m1", "b", strings.Replace(labels, `"cluster":"c1"`, `"cluster":"c1b"`, 1), "")
		e.notify("m1", "new alerts")
		eventually(t, "the alert group to reopen", func() bool { return e.alertGroup(g)["status"] == "firing" })
		eventually(t, "the reopen in infra-notice", func() bool {
			return firstLine("ch-infra-notice") == "@"+fakemattermost.AliceUsername
		})
		if got := firstLine("ch-infra-notice"); got != "@"+fakemattermost.AliceUsername {
			t.Errorf("the reopen mentions %q", got)
		}
	})

	t.Run("C-15.FR-11 webhook", func(t *testing.T) {
		var bodies []struct {
			Body struct {
				Event    string           `json:"event"`
				Mentions []map[string]any `json:"mentions"`
			} `json:"body"`
		}
		created := -1
		eventually(t, "the created event", func() bool {
			decode(t, call(t, http.MethodGet, webhookBase+"/_fake/received/infra", ""), &bodies)
			created = slices.IndexFunc(bodies, func(b struct {
				Body struct {
					Event    string           `json:"event"`
					Mentions []map[string]any `json:"mentions"`
				} `json:"body"`
			}) bool {
				return b.Body.Event == "created"
			})
			return created >= 0
		})
		if created < 0 {
			return
		}
		var got []string
		for _, m := range bodies[created].Body.Mentions {
			got = append(got, fmt.Sprintf("%v:%v:%v", m["type"], m["slug"], m["login"]))
		}
		if !slices.Equal(got, []string{"team:payments:<nil>", "user:<nil>:" + adminLogin}) {
			t.Errorf("the created event mentions %v", got)
		}
	})

	t.Run("C-28.FR-4 reminder", func(t *testing.T) {
		if g == "" {
			t.Skip("no alert group")
		}
		count := func() int {
			n := 0
			for _, p := range e.posts() {
				if p.ChannelID == "ch-infra-notice" {
					n++
				}
			}
			return n
		}
		e.command(g, "acknowledge")
		eventually(t, "the acknowledgement", func() bool { return e.alertGroup(g)["status"] == "acknowledged" })
		before := count()
		advance(t, r, 700)
		eventually(t, "the reminder in infra-notice", func() bool {
			return count() > before && strings.HasPrefix(firstLine("ch-infra-notice"), "@"+fakemattermost.AliceUsername)
		})
		if count() <= before || !strings.HasPrefix(firstLine("ch-infra-notice"), "@"+fakemattermost.AliceUsername) {
			t.Errorf("the reminder mentions %q", firstLine("ch-infra-notice"))
		}
	})

	t.Run("C-28.AC-2", func(t *testing.T) {
		got := saved("payments-notice", dput(pn, `[{"target":{"kind":"team","team_id":"`+tp+`"}},`+
			`{"target":{"kind":"team","team_id":"`+ts+`"}}]`))
		var names [][]string
		for _, en := range got {
			n := []string{}
			for _, r := range en.ResolvesTo {
				for _, m := range r.Mentions {
					n = append(n, m.Name)
				}
			}
			names = append(names, n)
		}
		if fmt.Sprint(names) != "[[payments_team] []]" {
			t.Errorf("team entries resolve to %v", names)
		}
	})

	t.Run("C-28.AC-8", func(t *testing.T) {
		d2 := e.destination("payments-critical", cm, "ch-payments-critical", nil, 30)
		teamRead := e.api.do(http.MethodGet, "/api/v1/teams/"+tp, "")
		up := map[string]any{"name": "payments", "slug": "payments", "own_working_hours": false,
			"destination_entries": []map[string]string{{"destination_id": pn, "severity_condition": "below_critical"},
				{"destination_id": d2, "severity_condition": "critical"}, {"destination_id": dt,
					"severity_condition": "any"}}}
		b, _ := json.Marshal(up)
		if a := e.api.do(http.MethodPut, "/api/v1/teams/"+tp, string(b), "If-Match",
			teamRead.header.Get("ETag")); a.status != http.StatusOK {
			t.Fatalf("the team's destinations = %d %s", a.status, a.body)
		}
		if got := read(d2); len(got) != 1 || got[0].Target.Kind != "team" || got[0].Target.Team == nil ||
			got[0].Target.Team.Slug != "payments" || len(got[0].Events) != 7 {
			t.Errorf("payments-critical %+v", got)
		}
		if got := read(dt); len(got) != 1 || got[0].Target.Kind != "on_call_of_team" {
			t.Errorf("the telegram destination %+v", got)
		}
		if got := read(pn); len(got) != 2 {
			t.Errorf("payments-notice, which named the team, %+v", got)
		}
		if n := h.count(t, `SELECT count(*) FROM audit_log WHERE action = 'destination.updated'
			AND resource_public_id = '`+d2+`' AND diff::text LIKE '%mention_entries%'`); n != 1 {
			t.Errorf("%d audit log entries of the team entry", n)
		}
	})

	t.Run("C-28.FR-5", func(t *testing.T) {
		d := e.api.json(http.MethodGet, "/api/v1/destinations/"+in, "", http.StatusOK)
		if m := d["mentions"].(map[string]any)["new_alert_group"].(map[string]any); m["everyone"] != "here" {
			t.Errorf("the l1 form %v", m)
		}
		before := e.api.do(http.MethodGet, "/api/v1/destinations/"+in, "")
		var body map[string]any
		decode(t, before, &body)
		for _, k := range []string{"id", "etag", "mention_entries", "health", "routes", "teams", "created_at",
			"owning_team", "team_name", "channel_name"} {
			delete(body, k)
		}
		body["mentions"].(map[string]any)["new_alerts"].(map[string]any)["everyone"] = "channel"
		b, _ := json.Marshal(body)
		got := saved("the l1 form", e.api.do(http.MethodPut, "/api/v1/destinations/"+in, string(b), "If-Match",
			before.header.Get("ETag")))
		var shape []string
		for _, en := range got {
			shape = append(shape, fmt.Sprintf("%s/%s/%d", en.Target.Kind, en.Target.Everyone, len(en.Events)))
		}
		if !slices.Equal(shape, []string{"everyone/here/1", "everyone/channel/1", "on_call_of_teams_from_label//7"}) {
			t.Errorf("converted %v", shape)
		}
		d = e.api.json(http.MethodGet, "/api/v1/destinations/"+in, "", http.StatusOK)
		m := d["mentions"].(map[string]any)
		if m["new_alert_group"].(map[string]any)["everyone"] != "here" ||
			m["new_alerts"].(map[string]any)["everyone"] != "channel" {
			t.Errorf("the l1 form after %v", m)
		}
	})

	t.Run("C-28.AC-3 slack", func(t *testing.T) {
		cs := conns[devmode.SlackConnectionName]
		fsl := h.Fakes.Slack + "/_fake"
		t.Cleanup(func() { restore(t, http.MethodPut, fsl+"/config", `{"scopes":{"a":null}}`) })
		e.api.json(http.MethodPost, "/api/v1/connections/"+cs+"/checks", "", http.StatusOK)
		sd := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"slack","name":"alerts-sl",
			"connection_id":%q,"channel_id":%q,"limiter":{"limit":50,"per_seconds":1}}`, cs, fakeslack.ChannelAlerts),
			http.StatusCreated)["destination"].(map[string]any)["id"].(string)
		refused("everyone in slack", dput(sd, `[{"target":{"kind":"everyone","everyone":"everyone"}}]`),
			"/mention_entries/0/target")
		refused("all in slack", dput(sd, `[{"target":{"kind":"everyone","everyone":"all"}}]`),
			"/mention_entries/0/target")
		group := `[{"target":{"kind":"group","group":"payments-team","group_id":"` + fakeslack.UserGroupPayments + `"}}]`
		refused("a user group without the scope", dput(sd, group), "/mention_entries/0/target")
		e.fake(http.MethodPut, fsl+"/config", `{"scopes":{"a":"chat:write,channels:read,groups:read,im:write,`+
			`users:read,users:read.email,usergroups:read"}}`)
		e.api.json(http.MethodPost, "/api/v1/connections/"+cs+"/checks", "", http.StatusOK)
		if got := saved("a user group with the scope", dput(sd, group)); len(got) != 1 || !got[0].Available {
			t.Errorf("slack entries %+v", got)
		}
	})

	t.Run("C-28.AC-3 bridge", func(t *testing.T) {
		cb := conns[devmode.BridgeConnectionName]
		fb := h.Fakes.Bridge + "/_fake"
		def, _ := json.Marshal(fakebridge.DefaultConfig().Capabilities)
		t.Cleanup(func() { restore(t, http.MethodPut, fb+"/config", `{"capabilities":`+string(def)+`}`) })
		bd := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":"alerts-br",
			"connection_id":%q,"channel_id":%q,"limiter":{"limit":50,"per_seconds":1}}`, cb, fakebridge.ChannelAlerts),
			http.StatusCreated)["destination"].(map[string]any)["id"].(string)
		channel := `[{"target":{"kind":"everyone","everyone":"channel"}},{"target":{"kind":"user","user_id":"` + admin +
			`"}}]`
		refused("channel through a bridge that does not declare it", dput(bd, channel), "/mention_entries/0/target")
		var caps map[string]any
		_ = json.Unmarshal(def, &caps)
		caps["everyone_mentions"] = []string{"channel"}
		b, _ := json.Marshal(map[string]any{"capabilities": caps})
		e.fake(http.MethodPut, fb+"/config", string(b))
		e.api.json(http.MethodPost, "/api/v1/connections/"+cb+"/checks", "", http.StatusOK)
		if got := saved("channel through a bridge that declares it", dput(bd, channel)); len(got) != 2 {
			t.Errorf("bridge entries %+v", got)
		}
	})

	t.Run("C-28.AC-6", func(t *testing.T) {
		sd := e.destination("storm-alerts", cm, fakemattermost.ChannelAlertsProd, nil, 50)
		// The Alert Groups of the Storm are of payments, whose On-call the entry from the label takes from them.
		saved("storm-alerts", dput(sd, `[{"target":{"kind":"everyone","everyone":"channel"},"events":["storm_started"]},`+
			`{"target":{"kind":"on_call_of_teams_from_label"},"events":["storm_started"]}]`))
		e.route("storm", "payments", []string{sd}, func(p map[string]any) { p["storm_threshold"] = 3 })
		e.group("s1", "Surge")
		for i := 1; i <= 6; i++ {
			e.alert("s1", fmt.Sprint("s", i), fmt.Sprintf(`{"team":"payments","cluster":"s%d"}`, i), "")
		}
		e.notify("s1", "first notification")
		var summary mmPost
		eventually(t, "the storm summary", func() bool {
			for _, p := range e.posts() {
				if p.ChannelID == fakemattermost.ChannelAlertsProd && p.RootID == "" &&
					strings.Contains(p.body(), "Storm on") {
					summary = p
				}
			}
			return summary.ID != ""
		})
		if m, _ := summary.mentionsAndBody(); m != "@channel @"+fakemattermost.AliceUsername {
			t.Errorf("the summary mentions %q", m)
		}
		for _, p := range e.posts() {
			if p.ChannelID == fakemattermost.ChannelAlertsProd && p.ID != summary.ID {
				if m, _ := p.mentionsAndBody(); m != "" {
					t.Errorf("a root message of the storm mentions %q", m)
				}
			}
		}
	})
}
