// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// rootEdits are the edits of the Root message of Alert Group #n so far: the patches of its Mattermost post and the
// edits of its Telegram channel post.
type rootEdits struct {
	mm, tg int
}

// edits counts the edits of the Root messages of the Alert Groups #ns in Mattermost and Telegram.
func (c *compact) edits(ns ...int) map[int]rootEdits {
	c.t.Helper()
	out := map[int]rootEdits{}
	tgs := c.tg.messages(faketelegram.ChannelID)
	for _, n := range ns {
		root := c.root(n)
		e := rootEdits{mm: len(c.calls(http.MethodPut, "/api/v4/posts/"+root.ID+"/patch"))}
		for _, m := range tgs {
			if strings.Contains(m.Text, fmt.Sprintf(">#%d ", n)) {
				e.tg = len(m.Edits)
			}
		}
		out[n] = e
	}
	return out
}

// messageSettingsDone counts the lines open_group_update_done of kind that the replicas logged.
func messageSettingsDone(h *Harness, kind string) int {
	n := 0
	for _, r := range h.Replicas {
		n += strings.Count(r.allOutput(), `"event":"open_group_update_done","kind":"`+kind+`"`)
	}
	return n
}

// TestMessageSettingsRerender is the Verification of the re-render of open Root messages after a change of what they
// show that is not a Route's (C-29.FR-1 to FR-3), against `muster dev` and the fake Mattermost and Telegram servers:
// changing the Organization's Severity label, Team label, Instance labels, time zone, start of the working day or first
// day of the week edits each open Root message once, Quietly — no new post, no notification — so that it shows the new
// label line, Alerts line or start time, and a Team label whose value names a Team brings the Team with the same one
// edit; a second save of the same values writes no row and edits nothing; a rename of a Team edits the Root message of
// the open Alert Group that has the Team once and not that of the Alert Group without it.
func TestMessageSettingsRerender(t *testing.T) {
	h := Start(t, DevProcess)
	c, dests := newCompact(t, h, h.Replicas[0])
	c.routeOf("Settings", "settings", []string{"alertname", "system", "severity"}, dests[:2], "")
	labels := func(team, pod, host string) map[string]string {
		return map[string]string{"category": "settings", "system": "kubernetes", "severity": "warning", "team": team,
			"owner": "ops", "pod": pod, "host": host}
	}
	g1 := c.number(c.fire("ms1", "SettingsMemory", nil, labels("payments", "api-1", "node-a"),
		labels("payments", "api-2", "node-b")))
	g2 := c.number(c.fire("ms2", "SettingsDisk", nil, labels("search", "db-1", "node-c"),
		labels("search", "db-2", "node-d")))
	for _, n := range []int{g1, g2} {
		c.tgText(n)
		if t1 := c.root(n).attachment(t).Text; !strings.Contains(t1, "system=kubernetes · team=") {
			t.Fatalf("#%d before: %s", n, t1)
		}
	}
	quiet := func(t *testing.T, what string, save func(), check func(mm, tg string) bool, edited ...int) {
		t.Helper()
		posts, tgPosts := len(c.posts()), len(c.tg.messages(faketelegram.ChannelID))
		notified, tgNotified := len(c.notifications()), len(c.tg.notifications())
		before := c.edits(g1, g2)
		save()
		eventually(t, what, func() bool {
			if h.count(t, `SELECT count(*) FROM open_group_updates`) != 0 ||
				h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) != 0 {
				return false
			}
			for _, n := range edited {
				if !check(c.root(n).attachment(t).Text+" "+fieldValues(c.root(n).attachment(t)), c.tgText(n)) {
					return false
				}
			}
			return true
		})
		after := c.edits(g1, g2)
		for _, n := range []int{g1, g2} {
			want := 0
			for _, e := range edited {
				if e == n {
					want = 1
				}
			}
			if d := (rootEdits{mm: after[n].mm - before[n].mm, tg: after[n].tg - before[n].tg}); d !=
				(rootEdits{mm: want, tg: want}) {
				t.Errorf("%s: #%d edited %+v times, want %d each", what, n, d, want)
			}
		}
		if len(c.posts()) != posts || len(c.tg.messages(faketelegram.ChannelID)) != tgPosts ||
			len(c.notifications()) != notified || len(c.tg.notifications()) != tgNotified {
			t.Errorf("%s: %d new posts, %d new telegram posts, %d notifications, %d telegram notifications", what,
				len(c.posts())-posts, len(c.tg.messages(faketelegram.ChannelID))-tgPosts,
				len(c.notifications())-notified, len(c.tg.notifications())-tgNotified)
		}
	}
	put := func(change func(m map[string]any)) func() {
		return func() {
			if a := putOrganization(c.api, change); a.status != http.StatusOK {
				t.Fatalf("put the organization = %d %s", a.status, a.body)
			}
		}
	}
	both := func(s string) func(mm, tg string) bool {
		return func(mm, tg string) bool { return strings.Contains(mm, s) && strings.Contains(tg, s) }
	}

	quiet(t, "the severity label", put(func(m map[string]any) { m["severity_label"] = "priority" }),
		both("severity=warning"), g1, g2)
	// The Team "Operations" (slug ops) has no Alerts while the Team label is team; the Team label owner, whose value
	// ops names it, brings it to both Alert Groups: the evaluation runs before the re-render, so that each Root message
	// is edited once, with the Team and the new label line.
	c.api.json(http.MethodPost, "/api/v1/teams", `{"name":"Operations","slug":"ops","own_working_hours":false}`,
		http.StatusCreated)
	eventually(t, "the evaluation of the new team", func() bool {
		return h.count(t, `SELECT count(*) FROM open_group_updates`) == 0 &&
			h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0
	})
	quiet(t, "the team label", put(func(m map[string]any) { m["team_label"] = "owner" }),
		func(mm, tg string) bool { return both("owner=ops")(mm, tg) && both("Operations")(mm, tg) }, g1, g2)
	if n := h.count(t, `SELECT count(*) FROM timeline_entries WHERE event = 'team_joined'
		AND team_id = (SELECT id FROM teams WHERE slug = 'ops')`); n != 2 {
		t.Errorf("the team label recorded %d team_joined of Operations, want 2", n)
	}
	quiet(t, "the instance labels", put(func(m map[string]any) { m["instance_labels"] = []string{"host"} }),
		both("host: node-"), g1, g2)
	berlin := regexp.MustCompile(` CES?T\b`)
	quiet(t, "the time zone", put(func(m map[string]any) { m["time_zone"] = "Europe/Berlin" }),
		func(mm, tg string) bool { return berlin.MatchString(mm) && berlin.MatchString(tg) }, g1, g2)
	if n := messageSettingsDone(h, "organization_message_settings"); n != 4 {
		t.Errorf("%d runs of organization_message_settings, want 4", n)
	}

	// A second save of the same values writes no row and edits nothing.
	before := c.edits(g1, g2)
	put(func(m map[string]any) { m["time_zone"] = "Europe/Berlin" })()
	if n := h.count(t, `SELECT count(*) FROM open_group_updates`); n != 0 {
		t.Errorf("the same values wrote %d rows", n)
	}
	if after := c.edits(g1, g2); after[g1] != before[g1] || after[g2] != before[g2] {
		t.Errorf("the same values edited: %+v, before %+v", after, before)
	}

	// A rename of the Team payments edits #g1, which has it, and not #g2, which does not.
	var teams struct {
		Items []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"items"`
	}
	decode(t, c.api.do(http.MethodGet, "/api/v1/teams", ""), &teams)
	team := ""
	for _, x := range teams.Items {
		if x.Slug == "payments" {
			team = x.ID
		}
	}
	rename := func() {
		etag := c.api.do(http.MethodGet, "/api/v1/teams/"+team, "").header.Get("ETag")
		if a := c.api.do(http.MethodPut, "/api/v1/teams/"+team, `{"name":"Payments squad","slug":"payments",
			"own_working_hours":false}`, "If-Match", etag); a.status != http.StatusOK {
			t.Fatalf("rename = %d %s", a.status, a.body)
		}
	}
	quiet(t, "the rename", rename, both("Payments squad"), g1)
	if n := messageSettingsDone(h, "team_name"); n != 1 {
		t.Errorf("%d runs of team_name, want 1", n)
	}
}

// fieldValues are the values of the fields of a Mattermost attachment, space-separated.
func fieldValues(a mmAttachment) string {
	out := make([]string, len(a.Fields))
	for i, f := range a.Fields {
		out[i] = f.Value
	}
	return strings.Join(out, " ")
}

// tgPost is the last channel post of Telegram that names #n, with its text and buttons as they are now.
func (c *compact) tgPost(n int) faketelegram.Message {
	c.t.Helper()
	var post faketelegram.Message
	eventually(c.t, fmt.Sprintf("the telegram post of #%d", n), func() bool {
		for _, m := range c.tg.messages(faketelegram.ChannelID) {
			if strings.Contains(m.Text, fmt.Sprintf(">#%d ", n)) {
				post = m
			}
		}
		return post.ID != 0
	})
	return post
}

// TestRouteSettingsAndLinksRerender is the Verification of the re-render of the open Root messages after a change of a
// Route's language, Snooze durations or template and of the Link rules (C-12.FR-2, FR-3, FR-9, C-10.FR-6), against
// `muster dev` and the fake Mattermost and Telegram servers, with E2E_REPLICAS=2 on two replicas: each change edits the
// Root message of each open Alert Group of the Route once in each messenger, Quietly — no new post, no notification —
// so that it shows the Russian buttons, the new Snooze buttons, the template's body or the link, and the Root message
// of the resolved Alert Group is not edited; a second save of the same values writes no row and edits nothing; a Link
// rule that matches no Alert Group writes a row whose run edits nothing.
func TestRouteSettingsAndLinksRerender(t *testing.T) {
	h := Start(t, DevProcess)
	c, dests := newCompact(t, h, h.Replicas[0])
	route := c.routeOf("Rerender", "rerender", []string{"alertname", "system"}, dests[:2], "")
	labels := func(pod string) map[string]string {
		return map[string]string{"category": "rerender", "system": "kubernetes", "severity": "warning", "pod": pod}
	}
	g1 := c.number(c.fire("rr1", "RerenderMemory", nil, labels("api-1")))
	g2 := c.number(c.fire("rr2", "RerenderDisk", nil, labels("db-1")))
	id3 := c.fire("rr3", "RerenderGone", nil, labels("gone-1"))
	g3 := c.number(id3)
	for _, n := range []int{g1, g2, g3} {
		c.tgText(n)
	}
	// The third Alert Group is resolved by hand: its Root messages show the final state and are not re-rendered.
	if out := c.command(id3, "resolve"); out != "done" {
		t.Fatalf("resolve #%d: %s", g3, out)
	}
	eventually(t, "the resolved root messages", func() bool {
		return h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0
	})

	shows := func(n int) string {
		b, _ := json.Marshal(c.root(n).attachment(t))
		p := c.tgPost(n)
		return string(b) + "\n" + p.Text + "\n" + string(p.ReplyMarkup)
	}
	quiet := func(t *testing.T, what string, save func(), want string, edited ...int) {
		t.Helper()
		posts, tgPosts := len(c.posts()), len(c.tg.messages(faketelegram.ChannelID))
		notified, tgNotified := len(c.notifications()), len(c.tg.notifications())
		before := c.edits(g1, g2, g3)
		save()
		eventually(t, what, func() bool {
			if h.count(t, `SELECT count(*) FROM open_group_updates`) != 0 ||
				h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) != 0 {
				return false
			}
			for _, n := range edited {
				if !strings.Contains(shows(n), want) {
					return false
				}
			}
			return true
		})
		after := c.edits(g1, g2, g3)
		for _, n := range []int{g1, g2, g3} {
			w := 0
			if slices.Contains(edited, n) {
				w = 1
			}
			if d := (rootEdits{mm: after[n].mm - before[n].mm, tg: after[n].tg - before[n].tg}); d !=
				(rootEdits{mm: w, tg: w}) {
				t.Errorf("%s: #%d edited %+v times, want %d each", what, n, d, w)
			}
		}
		if len(c.posts()) != posts || len(c.tg.messages(faketelegram.ChannelID)) != tgPosts ||
			len(c.notifications()) != notified || len(c.tg.notifications()) != tgNotified {
			t.Errorf("%s: %d new posts, %d new telegram posts, %d notifications, %d telegram notifications", what,
				len(c.posts())-posts, len(c.tg.messages(faketelegram.ChannelID))-tgPosts,
				len(c.notifications())-notified, len(c.tg.notifications())-tgNotified)
		}
	}
	// same saves again and checks that no row is written and nothing is edited.
	same := func(t *testing.T, what string, save func()) {
		t.Helper()
		before := c.edits(g1, g2, g3)
		save()
		if n := h.count(t, `SELECT count(*) FROM open_group_updates`); n != 0 {
			t.Errorf("%s again wrote %d rows", what, n)
		}
		if after := c.edits(g1, g2, g3); !maps.Equal(after, before) {
			t.Errorf("%s again edited: %+v, before %+v", what, after, before)
		}
	}
	put := func(change func(p map[string]any)) func() {
		return func() {
			rt := c.api.json(http.MethodGet, "/api/v1/routes/"+route, "", http.StatusOK)
			for _, k := range []string{"id", "etag", "position", "is_default", "destinations", "storm_active", "storm",
				"open_alert_group_count", "created_at", "urgent", "owning_team", "template_error"} {
				delete(rt, k)
			}
			change(rt["policy"].(map[string]any))
			b, _ := json.Marshal(rt)
			c.api.json(http.MethodPut, "/api/v1/routes/"+route, string(b), http.StatusOK, "If-Match", "*")
		}
	}

	language := put(func(p map[string]any) { p["language"] = "ru" })
	quiet(t, "the language", language, "Подтвердить", g1, g2)
	same(t, "the language", language)
	snooze := put(func(p map[string]any) { p["snooze_durations_seconds"] = []int{1800, 5400} })
	quiet(t, "the snooze durations", snooze, "Отложить на 30 мин", g1, g2)
	same(t, "the snooze durations", snooze)
	template := put(func(p map[string]any) {
		p["templates"] = map[string]any{"root_message": "Rerendered body of {{ .AlertGroup.Title }}"}
	})
	quiet(t, "the root message template", template, "Rerendered body of", g1, g2)
	same(t, "the root message template", template)
	if n := messageSettingsDone(h, "route_message_settings"); n != 3 {
		t.Errorf("%d runs of route_message_settings, want 3", n)
	}

	rule := func() {
		c.api.json(http.MethodPost, "/api/v1/link-rules", `{"name":"Pod","matchers":[{"label":"category","op":"=",
			"value":"rerender"}],"scope":{"type":"alert_group"},
			"url_template":"https://dash.example.org/pods/{{ .Labels.pod }}"}`, http.StatusCreated)
	}
	quiet(t, "a link rule", rule, "https://dash.example.org/pods/", g1, g2)
	// A rule that no Alert Group matches writes a row, whose run edits nothing.
	quiet(t, "a link rule that matches nothing", func() {
		c.api.json(http.MethodPost, "/api/v1/link-rules", `{"name":"None","matchers":[{"label":"category","op":"=",
			"value":"nothing"}],"scope":{"type":"alert_group"},"url_template":"https://none.example.org/"}`,
			http.StatusCreated)
	}, "https://dash.example.org/pods/")
	if n := messageSettingsDone(h, "organization_links"); n != 2 {
		t.Errorf("%d runs of organization_links, want 2", n)
	}
}

// TestFooterUserRerender is the Verification of the re-render of open Root messages after a change of whom their
// footer names (C-12.FR-12), against `muster dev` and the fake Mattermost and Telegram servers: the bootstrap Admin
// acknowledges one Alert Group and snoozes another, a Service account snoozes a third, and a fourth fires. A rename of
// the Admin through the profile and by an Admin edits the Root messages of the first two once each, Quietly — no new
// post, no notification — to show the new name, and not those of the other two; a Telegram Account link of the Admin
// edits only their Telegram Root messages, whose footer now mentions the account, and its removal edits them back; a
// rename of the Service account edits the third Alert Group's Root messages. A second save of the same values writes no
// row and edits nothing.
func TestFooterUserRerender(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	c, dests := newCompact(t, h, r)
	c.routeOf("Footer", "footer", []string{"alertname", "system"}, dests[:2], "")
	labels := func(pod string) map[string]string {
		return map[string]string{"category": "footer", "system": "kubernetes", "severity": "warning", "pod": pod}
	}
	ids := []string{c.fire("fu1", "FooterAcked", nil, labels("a")), c.fire("fu2", "FooterSnoozed", nil, labels("b")),
		c.fire("fu3", "FooterRobot", nil, labels("c")), c.fire("fu4", "FooterControl", nil, labels("d"))}
	ns := make([]int, len(ids))
	for i, id := range ids {
		ns[i] = c.number(id)
		c.tgText(ns[i])
	}
	session := newAgentSignedIn(t, r)
	me := session.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["user"].(map[string]any)
	adminID := me["id"].(string)
	sa := c.api.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"footer-robot","role":"responder"}`,
		http.StatusCreated)["id"].(string)
	robot := patClient{t: t, base: r.App, token: session.json(http.MethodPost, "/api/v1/service-accounts/"+sa+"/tokens",
		`{"name":"footer"}`, http.StatusCreated)["value"].(string)}
	if out := c.command(ids[0], "acknowledge"); out != "done" {
		t.Fatalf("acknowledge #%d: %s", ns[0], out)
	}
	c.api.json(http.MethodPost, "/api/v1/alert-groups/"+ids[1]+"/snooze", `{"no_end":true}`, http.StatusOK)
	robot.json(http.MethodPost, "/api/v1/alert-groups/"+ids[2]+"/snooze", `{"no_end":true}`, http.StatusOK)
	eventually(t, "the root messages of the commands", func() bool {
		return h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0 &&
			h.count(t, `SELECT count(*) FROM open_group_updates`) == 0
	})

	shows := func(n int) string {
		b, _ := json.Marshal(c.root(n))
		return string(b) + "\n" + c.tgPost(n).Text
	}
	// quiet runs save and checks that it edited the Root messages of each Alert Group as want says, Mattermost and
	// Telegram apart, until they show text, with no new post and no notification.
	quiet := func(t *testing.T, what string, save func(), text string, want map[int]rootEdits) {
		t.Helper()
		posts, tgPosts := len(c.posts()), len(c.tg.messages(faketelegram.ChannelID))
		notified, tgNotified := len(c.notifications()), len(c.tg.notifications())
		before := c.edits(ns...)
		save()
		eventually(t, what, func() bool {
			if h.count(t, `SELECT count(*) FROM open_group_updates`) != 0 ||
				h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) != 0 {
				return false
			}
			for n := range want {
				if !strings.Contains(shows(n), text) {
					return false
				}
			}
			return true
		})
		after := c.edits(ns...)
		for _, n := range ns {
			if d := (rootEdits{mm: after[n].mm - before[n].mm, tg: after[n].tg - before[n].tg}); d != want[n] {
				t.Errorf("%s: #%d edited %+v times, want %+v", what, n, d, want[n])
			}
		}
		if len(c.posts()) != posts || len(c.tg.messages(faketelegram.ChannelID)) != tgPosts ||
			len(c.notifications()) != notified || len(c.tg.notifications()) != tgNotified {
			t.Errorf("%s: %d new posts, %d new telegram posts, %d notifications, %d telegram notifications", what,
				len(c.posts())-posts, len(c.tg.messages(faketelegram.ChannelID))-tgPosts,
				len(c.notifications())-notified, len(c.tg.notifications())-tgNotified)
		}
		for _, n := range ns {
			for _, everyone := range []string{"@channel", "@here", "@all"} {
				if strings.Contains(shows(n), everyone) {
					t.Errorf("%s: #%d shows %s", what, n, everyone)
				}
			}
		}
	}
	same := func(t *testing.T, what string, save func()) {
		t.Helper()
		before := c.edits(ns...)
		save()
		if n := h.count(t, `SELECT count(*) FROM open_group_updates`); n != 0 {
			t.Errorf("%s again wrote %d rows", what, n)
		}
		if after := c.edits(ns...); !maps.Equal(after, before) {
			t.Errorf("%s again edited: %+v, before %+v", what, after, before)
		}
	}
	both := rootEdits{mm: 1, tg: 1}
	admin := map[int]rootEdits{ns[0]: both, ns[1]: both}

	profile := func() { session.json(http.MethodPut, "/api/v1/me", `{"name":"Footer Admin"}`, http.StatusOK) }
	quiet(t, "a profile rename", profile, "Footer Admin", admin)
	same(t, "a profile rename", profile)
	update := func() {
		c.api.json(http.MethodPut, "/api/v1/users/"+adminID, `{"name":"Footer Renamed","role":"admin"}`,
			http.StatusOK, "If-Match", "*")
	}
	quiet(t, "an admin's rename", update, "Footer Renamed", admin)
	same(t, "an admin's rename", update)

	// A Telegram Account link: only the Telegram Root messages show the footer's Mention of the account.
	link := func() {
		m := session.json(http.MethodPost, "/api/v1/me/account-links/telegram", `{"connection_id":"`+c.tg.conn+`"}`,
			http.StatusCreated)
		got := deepLink.FindStringSubmatch(fmt.Sprint(m["deep_link"]))
		if got == nil {
			t.Fatalf("deep link %v", m["deep_link"])
		}
		body, _ := json.Marshal(map[string]any{"token": devmode.TelegramConnectionBotToken,
			"from": map[string]any{"id": 7105, "username": "footer_admin"}, "text": "/start " + got[1]})
		c.fake(http.MethodPost, c.tg.ftg+"/private", string(body))
	}
	tgOnly := map[int]rootEdits{ns[0]: {tg: 1}, ns[1]: {tg: 1}}
	quiet(t, "a telegram link", link, "tg://user?id=7105", tgOnly)
	links := session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK)["items"].([]any)
	if len(links) != 1 {
		t.Fatalf("account links %v", links)
	}
	linkID := links[0].(map[string]any)["id"].(string)
	quiet(t, "an unlink", func() {
		session.json(http.MethodDelete, "/api/v1/me/account-links/"+linkID, "", http.StatusNoContent)
	}, "Footer Renamed", tgOnly)
	for _, n := range ns {
		if strings.Contains(c.tgPost(n).Text, "tg://user?id=7105") {
			t.Errorf("#%d still mentions the unlinked account", n)
		}
	}

	rename := func() {
		c.api.json(http.MethodPut, "/api/v1/service-accounts/"+sa, `{"name":"footer-terraform","role":"responder"}`,
			http.StatusOK, "If-Match", "*")
	}
	quiet(t, "a service account rename", rename, "footer-terraform", map[int]rootEdits{ns[2]: both})
	same(t, "a service account rename", rename)
	for kind, want := range map[string]int{"user_name": 2, "account_links": 2, "service_account_name": 1} {
		if n := messageSettingsDone(h, kind); n != want {
			t.Errorf("%d runs of %s, want %d", n, kind, want)
		}
	}
}
