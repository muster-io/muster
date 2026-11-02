// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

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
// changing the Organization's Severity label, Team label, Instance labels or time zone edits each open Root message
// once, Quietly — no new post, no notification — so that it shows the new label line, Alerts line or start time, and a
// Team label whose value names a Team brings the Team with the same one edit; a second save of the same values writes
// no row and edits nothing; a rename of a Team edits the Root message of the open Alert Group that has the Team once
// and not that of the Alert Group without it.
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
