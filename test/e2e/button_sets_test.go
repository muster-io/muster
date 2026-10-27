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
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// rootIn waits for the live Root message of Alert Group #n in the Mattermost channel and returns it.
func (e *mm) rootIn(n int, channel string) mmPost {
	e.t.Helper()
	var live mmPost
	eventually(e.t, fmt.Sprintf("the root message of #%d in %s", n, channel), func() bool {
		for _, p := range e.roots(n) {
			if p.ChannelID == channel && p.DeleteAt == 0 {
				live = p
			}
		}
		return live.ID != ""
	})
	return live
}

// labels are the names of the actions of a Mattermost post, none without an attachment.
func labels(p mmPost) []string {
	if len(p.Attachments) == 0 {
		return []string{}
	}
	return actionNames(p.Attachments[0])
}

// waitLabels waits until got gives want, and reports the last value it gave otherwise.
func waitLabels(t *testing.T, what string, want []string, got func() []string) {
	t.Helper()
	var last []string
	eventually(t, fmt.Sprintf("%s %q", what, want), func() bool {
		last = got()
		return slices.Equal(last, want)
	})
	if !slices.Equal(last, want) {
		t.Errorf("%s: %q, want %q", what, last, want)
	}
}

// putButtons replaces the button set of the Destination dest by set, a DestinationButtonSet, with the ETag it reads,
// or with etag when it is not empty.
func (e *mm) putButtons(dest, set, etag string) answer {
	e.t.Helper()
	read := e.api.do(http.MethodGet, "/api/v1/destinations/"+dest, "")
	var d map[string]any
	decode(e.t, read, &d)
	for _, k := range []string{"id", "etag", "health", "routes", "created_at", "template_error", "team_name",
		"channel_name", "channel_title", "channel_chat_id", "discussion_group_id", "discussion_group_title"} {
		delete(d, k)
	}
	d["buttons"] = json.RawMessage(set)
	body, _ := json.Marshal(d)
	if etag == "" {
		etag = read.header.Get("ETag")
	}
	return e.api.do(http.MethodPut, "/api/v1/destinations/"+dest, string(body), "If-Match", etag)
}

// buttonsOf is the button set of the Destination dest as it reads.
func (e *mm) buttonsOf(dest string) string {
	e.t.Helper()
	b, _ := json.Marshal(e.api.json(http.MethodGet, "/api/v1/destinations/"+dest, "", http.StatusOK)["buttons"])
	return string(b)
}

// refusal is the status of an answer with the pointer and the code of its first error.
func refusal(t *testing.T, a answer) string {
	t.Helper()
	var p struct {
		Errors []struct{ Pointer, Code string }
	}
	_ = json.Unmarshal(a.body, &p)
	if len(p.Errors) == 0 {
		return fmt.Sprintf("%d", a.status)
	}
	return fmt.Sprintf("%d %s %s", a.status, p.Errors[0].Pointer, p.Errors[0].Code)
}

// TestButtonSets is the Verification of S-113 against `muster dev` and its fake Mattermost, Telegram and bridge
// servers (C-28.FR-8 to FR-10, C-28.AC-7, C-10.AC-23, C-16.FR-4, C-20.FR-1). On a Route with the Snooze durations
// 1 h, 4 h and 24 h, the Mattermost Destination "alerts" with its own set — Acknowledge, "Snooze 24 h" and "Until
// tomorrow" — shows on a firing Root message exactly Ack, "Snooze 24 h" and "Until tomorrow 09:00", acknowledged Unack,
// "Snooze 24 h" and "Until tomorrow 09:00", snoozed Ack and Unsnooze; "alerts-prod", which follows the Organization's
// set of every button, shows every button. The change of the set re-renders the open Root messages as edits through an
// open_group_updates row destination_buttons, and a press on a button of the full set sent before still runs its
// Command; a Reminder keeps "Still on it" and "Unack" and the API still resolves. A Telegram Destination with its own
// set lays out only its buttons. A change of the Organization's set re-renders the Destinations that follow it, the
// bridge's included, through a row organization_buttons; with the set back at every button, a Snooze duration of 2 h
// added to the Route shows on the next re-render. The refusals are 422 at the item's pointer and a stale If-Match is
// 412; the changes are in the Audit log; the test message and the preview show the Destination's set; and the Text
// command resolve through a Bridge whose own set has no Resolve still resolves.
func TestButtonSets(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := reminderEnv(t, h, r)
	prod := e.destination("alerts-prod", e.mmConn, fakemattermost.ChannelAlertsProd, nil, 50)
	b := &brd{mm: e.mm, fb: h.Fakes.Bridge + "/_fake"}
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
		if c := item.(map[string]any); c["name"] == devmode.BridgeConnectionName {
			b.conn = c["id"].(string)
		}
	}
	br := b.destination("alerts-br", fakebridge.ChannelAlerts)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
		display_name, created_at) SELECT 1, 'AK0000000000S7', u.id, 'bridge', c.id, '`+fakebridge.BobID+`', u.name, now()
		FROM users u, connections c WHERE u.login = 'bob' AND c.public_id = '`+b.conn+`'`)
	route := e.route("bs", "bs", []string{e.mmDest, prod, e.tgDest, br}, func(p map[string]any) {
		p["snooze_durations_seconds"] = []int64{3600, 14400, 86400}
		p["ack_timeout"] = map[string]any{"enabled": false, "first_interval_seconds": 300}
		p["reminders"] = map[string]any{"enabled": true, "first_interval_seconds": 14400, "cap_seconds": 86400}
	})
	every := []string{"Ack", "Resolve", "Snooze 1 h", "Snooze 4 h", "Snooze 24 h", "Until tomorrow 09:00",
		"Until Monday 09:00"}
	ac7 := `{"source":"own","items":[{"button":"acknowledge"},{"button":"snooze","snooze_seconds":86400},` +
		`{"button":"snooze_preset","preset":"tomorrow"}]}`
	tgRows := func(post int64) [][]string {
		for _, m := range e.messages(faketelegram.ChannelID) {
			if m.ID == post {
				return keyboardRows(t, m.ReplyMarkup)
			}
		}
		return nil
	}
	done := func(kind string) int {
		return len(b.lines("open_group_update_done", map[string]any{"kind": kind}))
	}

	var g, g2, g3 string
	var n, n2 int
	var stale mmAction
	var staleRoot mmPost
	t.Run("the default sets", func(t *testing.T) {
		e := e.on(t)
		org, _ := json.Marshal(e.api.json(http.MethodGet, "/api/v1/organization", "", http.StatusOK)["buttons"])
		if string(org) != `{"all":true}` {
			t.Errorf("the organization's set %s", org)
		}
		for _, d := range []string{e.mmDest, prod, e.tgDest, br} {
			if got := e.buttonsOf(d); got != `{"source":"organization"}` {
				t.Errorf("%s: %s", d, got)
			}
		}
		g, g2, g3 = e.fire("bs1", "DiskFull", "bs", true), e.fire("bs2", "HostDown", "bs", true),
			e.fire("bs3", "CPUHigh", "bs", true)
		n, n2 = e.number(g), e.number(g2)
		waitLabels(t, "the root message in alerts", every, func() []string { return labels(e.rootIn(n, "ch-alerts")) })
		staleRoot = e.rootIn(n2, "ch-alerts")
		for _, a := range staleRoot.attachment(t).Actions {
			if a.Name == "Resolve" {
				stale = a
			}
		}
		if stale.Integration.URL == "" {
			t.Fatalf("no Resolve on %+v", staleRoot)
		}
	})

	t.Run("the own set of alerts", func(t *testing.T) {
		e := e.on(t)
		edits := len(e.calls(http.MethodPut, "/api/v4/posts/"+e.rootIn(n, "ch-alerts").ID+"/patch"))
		before := done("destination_buttons")
		if a := e.putButtons(e.mmDest, ac7, `"99"`); a.status != http.StatusPreconditionFailed {
			t.Errorf("a stale If-Match = %d %s", a.status, a.body)
		}
		if a := e.putButtons(e.mmDest, ac7, ""); a.status != http.StatusOK || !strings.Contains(string(a.body),
			`"buttons":{"items":[{"button":"acknowledge"}`) {
			t.Fatalf("its own set = %d %s", a.status, a.body)
		}
		want := []string{"Ack", "Snooze 24 h", "Until tomorrow 09:00"}
		waitLabels(t, "the firing root message in alerts", want, func() []string {
			return labels(e.rootIn(n, "ch-alerts"))
		})
		waitLabels(t, "the firing root message in alerts-prod", every, func() []string {
			return labels(e.rootIn(n, "ch-alerts-prod"))
		})
		if got := len(e.calls(http.MethodPut, "/api/v4/posts/"+e.rootIn(n, "ch-alerts").ID+"/patch")); got <= edits {
			t.Errorf("the root message was not edited: %d edits", got)
		}
		eventually(t, "the row destination_buttons to run", func() bool { return done("destination_buttons") > before })
		if c := h.count(t, `SELECT count(*) FROM open_group_updates`); c != 0 {
			t.Errorf("%d open_group_updates rows left", c)
		}
		var page struct {
			Items []struct {
				Action string           `json:"action"`
				Diff   []map[string]any `json:"diff"`
			} `json:"items"`
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/audit-log?action=destination.updated&limit=1", ""), &page)
		if len(page.Items) != 1 || len(page.Items[0].Diff) != 1 || page.Items[0].Diff[0]["pointer"] != "/buttons" {
			t.Errorf("the audit log %+v", page.Items)
		}
		for _, c := range []struct{ set, want string }{
			{`{"source":"own","items":[{"button":"resolve"},{"button":"resolve"}]}`, "422 /buttons/items/1 duplicate"},
			{`{"source":"own","items":[{"button":"snooze"}]}`, "422 /buttons/items/0 required"},
			{`{"source":"own","items":[{"button":"snooze","snooze_seconds":0}]}`, "422 /buttons/items/0 out_of_range"},
			{`{"source":"own"}`, "422 /buttons/items required"},
		} {
			if got := refusal(t, e.putButtons(e.mmDest, c.set, "")); got != c.want {
				t.Errorf("%s = %s, want %s", c.set, got, c.want)
			}
		}

		// A press on Resolve of the full set, sent before the set changed, still runs its Command.
		body, _ := json.Marshal(map[string]any{"user_id": fakemattermost.BobUserID, "user_name": "bob",
			"channel_id": "ch-alerts", "team_id": fakemattermost.TeamID, "post_id": staleRoot.ID,
			"context": stale.Integration.Context})
		if a := call(t, http.MethodPost, stale.Integration.URL, string(body)); a.status != http.StatusOK {
			t.Errorf("the press = %d %s", a.status, a.body)
		}
		eventually(t, "the press on the earlier Resolve", func() bool { return e.alertGroup(g2)["status"] == "resolved" })
	})

	t.Run("acknowledged, the reminder and resolve from the API", func(t *testing.T) {
		e := e.on(t)
		e.ack(e.bob, g)
		waitLabels(t, "the acknowledged root message in alerts", []string{"Unack", "Snooze 24 h",
			"Until tomorrow 09:00"}, func() []string { return labels(e.rootIn(n, "ch-alerts")) })
		waitLabels(t, "the acknowledged root message in alerts-prod", append([]string{"Unack"}, every[1:]...),
			func() []string { return labels(e.rootIn(n, "ch-alerts-prod")) })
		e.adv(14400)
		root := e.rootIn(n, "ch-alerts")
		var reminder []string
		for _, p := range e.replies(root.ID) {
			if len(p.Attachments) == 1 && len(p.Attachments[0].Actions) > 0 {
				reminder = labels(p)
			}
		}
		if !slices.Equal(reminder, []string{"Still on it", "Unack"}) {
			t.Errorf("the reminder in alerts: %q", reminder)
		}
		if out := e.command(g3, "resolve"); out == "" || e.alertGroup(g3)["status"] != "resolved" {
			t.Errorf("resolve from the API: %q", out)
		}
	})

	t.Run("a telegram set", func(t *testing.T) {
		e := e.on(t)
		post := e.rootPost(g)
		eventually(t, "the telegram keyboard with every button", func() bool {
			return len(tgRows(post)) == 3 && slices.Equal(tgRows(post)[0], []string{"Unack", "Resolve"})
		})
		if a := e.putButtons(e.tgDest, `{"source":"own","items":[{"button":"resolve"},{"button":"snooze",`+
			`"snooze_seconds":3600}]}`, ""); a.status != http.StatusOK {
			t.Fatalf("the telegram set = %d %s", a.status, a.body)
		}
		eventually(t, "the telegram keyboard of its set", func() bool {
			rows := tgRows(post)
			return len(rows) == 2 && slices.Equal(rows[0], []string{"Resolve"}) &&
				slices.Equal(rows[1], []string{"Snooze 1 h"})
		})
		if rows := tgRows(post); len(rows) != 2 {
			t.Errorf("the telegram keyboard %q", rows)
		}
	})

	t.Run("the organization's set", func(t *testing.T) {
		e := e.on(t)
		before := done("organization_buttons")
		for _, c := range []struct{ set, want string }{
			{`{"all":false,"items":[{"button":"resolve"},{"button":"resolve"}]}`, "422 /buttons/items/1 duplicate"},
			{`{"all":false,"items":[{"button":"snooze_preset"}]}`, "422 /buttons/items/0 required"},
			{`{"all":false,"items":[{"button":"snooze","snooze_seconds":-1}]}`, "422 /buttons/items/0 out_of_range"},
		} {
			if got := refusal(t, putOrganization(e.api, func(m map[string]any) {
				m["buttons"] = json.RawMessage(c.set)
			})); got != c.want {
				t.Errorf("%s = %s, want %s", c.set, got, c.want)
			}
		}
		read := e.api.do(http.MethodGet, "/api/v1/organization", "")
		if a := putOrganization(e.api, func(m map[string]any) {
			m["buttons"] = json.RawMessage(`{"all":false,"items":[{"button":"acknowledge"},` +
				`{"button":"snooze","snooze_seconds":86400}]}`)
		}); a.status != http.StatusOK {
			t.Fatalf("the organization's set = %d %s", a.status, a.body)
		}
		var m map[string]any
		decode(t, read, &m)
		delete(m, "id")
		delete(m, "etag")
		m["outgoing_heartbeat"] = map[string]any{"proxy": map[string]any{"enabled": false}}
		stale, _ := json.Marshal(m)
		if a := e.api.do(http.MethodPut, "/api/v1/organization", string(stale), "If-Match",
			read.header.Get("ETag")); a.status != http.StatusPreconditionFailed {
			t.Errorf("a stale If-Match = %d %s", a.status, a.body)
		}
		waitLabels(t, "the root message in alerts-prod", []string{"Unack", "Snooze 24 h"}, func() []string {
			return labels(e.rootIn(n, "ch-alerts-prod"))
		})
		waitLabels(t, "the root message in alerts", []string{"Unack", "Snooze 24 h", "Until tomorrow 09:00"},
			func() []string { return labels(e.rootIn(n, "ch-alerts")) })
		eventually(t, "the bridge root message", func() bool {
			got := b.posted("DiskFull")
			return len(got) == 1 && slices.Equal(latest(got[0]), []string{"Unack", "Snooze 24 h"})
		})
		eventually(t, "the row organization_buttons to run", func() bool {
			return done("organization_buttons") > before
		})
		var page struct {
			Items []struct {
				Diff []map[string]any `json:"diff"`
			} `json:"items"`
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/audit-log?action=organization.updated&limit=1", ""), &page)
		if len(page.Items) != 1 || len(page.Items[0].Diff) != 1 || page.Items[0].Diff[0]["pointer"] != "/buttons" {
			t.Errorf("the audit log %+v", page.Items)
		}

		e.cmd(e.bob, g, "snooze", `{"no_end":true}`)
		waitLabels(t, "the snoozed root message in alerts", []string{"Ack", "Unsnooze"}, func() []string {
			return labels(e.rootIn(n, "ch-alerts"))
		})
		waitLabels(t, "the snoozed root message in alerts-prod", []string{"Ack", "Unsnooze"}, func() []string {
			return labels(e.rootIn(n, "ch-alerts-prod"))
		})
	})

	t.Run("a duration added to the route", func(t *testing.T) {
		e := e.on(t)
		if a := putOrganization(e.api, func(m map[string]any) {
			m["buttons"] = json.RawMessage(`{"all":true}`)
		}); a.status != http.StatusOK {
			t.Fatalf("every button = %d %s", a.status, a.body)
		}
		a := e.api.do(http.MethodGet, "/api/v1/routes/"+route, "")
		var rt map[string]any
		decode(t, a, &rt)
		policy := rt["policy"].(map[string]any)
		policy["snooze_durations_seconds"] = []int64{3600, 7200, 14400, 86400}
		body, _ := json.Marshal(map[string]any{"name": rt["name"], "matchers": rt["matchers"],
			"group_key": rt["group_key"], "destination_ids": rt["destination_ids"], "policy": policy})
		e.api.json(http.MethodPut, "/api/v1/routes/"+route, string(body), http.StatusOK, "If-Match", a.header.Get("ETag"))
		e.cmd(e.bob, g, "unsnooze", "")
		waitLabels(t, "the root message in alerts-prod after the next re-render", []string{"Ack", "Resolve",
			"Snooze 1 h", "Snooze 2 h", "Snooze 4 h", "Snooze 24 h", "Until tomorrow 09:00", "Until Monday 09:00"},
			func() []string { return labels(e.rootIn(n, "ch-alerts-prod")) })
		waitLabels(t, "the root message in alerts", []string{"Ack", "Snooze 24 h", "Until tomorrow 09:00"},
			func() []string { return labels(e.rootIn(n, "ch-alerts")) })
	})

	t.Run("the test message and the preview", func(t *testing.T) {
		e := e.on(t)
		if a := e.putButtons(e.mmDest, `{"source":"own","items":[{"button":"acknowledge"},{"button":"resolve"}]}`,
			""); a.status != http.StatusOK {
			t.Fatalf("acknowledge and resolve = %d %s", a.status, a.body)
		}
		waitLabels(t, "the root message in alerts", []string{"Ack", "Resolve"}, func() []string {
			return labels(e.rootIn(n, "ch-alerts"))
		})
		e.destTest(e.mmDest, example)
		var test []string
		for _, p := range e.posts() {
			if p.ChannelID == "ch-alerts" && len(p.Attachments) == 1 &&
				strings.Contains(p.Attachments[0].Text+p.Attachments[0].Title, "Test message") {
				test = labels(p)
			}
		}
		if !slices.Equal(test, []string{"Ack", "Resolve"}) {
			t.Errorf("the test message %q", test)
		}
		var p struct {
			Items []struct {
				Request struct {
					Body string `json:"body"`
				} `json:"request"`
			} `json:"items"`
		}
		decode(t, e.api.do(http.MethodPost, "/api/v1/destinations/"+e.mmDest+"/previews", `{"source":`+example+`}`), &p)
		var post struct {
			Props json.RawMessage `json:"props"`
		}
		if len(p.Items) != 1 || json.Unmarshal([]byte(p.Items[0].Request.Body), &post) != nil {
			t.Fatalf("the preview %+v", p)
		}
		if got := actionNames(parseProps(post.Props)[0]); !slices.Equal(got, []string{"Ack", "Resolve"}) {
			t.Errorf("the preview %q", got)
		}
	})

	t.Run("a text command through a bridge without resolve", func(t *testing.T) {
		if a := b.on(t).putButtons(br, `{"source":"own","items":[{"button":"acknowledge"}]}`, ""); a.status !=
			http.StatusOK {
			t.Fatalf("the bridge set = %d %s", a.status, a.body)
		}
		eventually(t, "the bridge root message of its set", func() bool {
			got := b.posted("DiskFull")
			return len(got) == 1 && slices.Equal(latest(got[0]), []string{"Ack"})
		})
		root := b.posted("DiskFull")[0]
		if a := b.answer(b.say(root.ID, fakebridge.BobID, "resolve")); a.Text != fmt.Sprintf("Done: Resolved #%d", n) {
			t.Errorf("the answer %+v", a)
		}
		eventually(t, "the text command resolve", func() bool { return e.alertGroup(g)["status"] == "resolved" })
	})
}
