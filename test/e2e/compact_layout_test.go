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
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// compact are the four messengers of the compact layout test, on one muster dev: the fake Mattermost server, Telegram,
// Slack (the second app) and the fake bridge, each with a Destination of the Route "Infrastructure".
type compact struct {
	*mm
	tg *tgd
	sl *slk
	br *brd
}

// newCompact makes the Destinations of the four messengers and the Team payments.
func newCompact(t *testing.T, h *Harness, r *Replica) (*compact, []string) {
	t.Helper()
	e := newMM(t, h, r)
	c := &compact{mm: e, tg: &tgd{mm: e, ftg: h.Fakes.Telegram + "/_fake"}, sl: &slk{mm: e, fs: h.Fakes.Slack + "/_fake"},
		br: &brd{mm: e, fb: h.Fakes.Bridge + "/_fake"}}
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
		switch conn := item.(map[string]any); conn["name"] {
		case devmode.TelegramConnectionName:
			c.tg.conn = conn["id"].(string)
		case devmode.BridgeConnectionName:
			c.br.conn = conn["id"].(string)
		}
	}
	if c.tg.conn == "" || c.br.conn == "" {
		t.Fatal("no demo Telegram or Bridge Connection")
	}
	mmDest := e.destination("compact-mm", e.connection(50), fakemattermost.ChannelAlerts, nil, 50)
	c.tg.member(faketelegram.GroupID, faketelegram.Member{Status: faketelegram.StatusAdministrator,
		CanPostMessages: true})
	a := c.tg.create("compact-tg", "@muster_alerts", 60)
	if a.status != http.StatusCreated {
		t.Fatalf("telegram destination = %d %s", a.status, a.body)
	}
	var created struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	decode(t, a, &created)
	tgDest := created.Destination.ID
	c.sl.conn = e.api.json(http.MethodPost, "/api/v1/connections", `{"type":"slack","name":"sl","event_mode":"socket",
		"bot_token":"`+fakeslack.SecondBotToken+`","app_token":"`+fakeslack.SecondAppToken+`","proxy":{"enabled":false},
		"limiter":{"limit":50,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	if check := e.api.json(http.MethodPost, "/api/v1/connections/"+c.sl.conn+"/checks", "", http.StatusOK); check["ok"] !=
		true {
		t.Fatalf("the slack connection check %v", check)
	}
	slDest := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"slack","name":"compact-sl",
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":50,"per_seconds":1}}`, c.sl.conn,
		fakeslack.ChannelAlerts, mentionSettings(nil)), http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	brDest := c.br.destination("compact-br", fakebridge.ChannelAlerts)
	e.api.json(http.MethodPost, "/api/v1/teams", `{"name":"payments","slug":"payments","own_working_hours":false}`,
		http.StatusCreated)
	return c, []string{mmDest, tgDest, slDest, brDest}
}

// routeOf creates the Route name for category with the Group key and the Paging profile, its root_message template
// set when one is given.
func (c *compact) routeOf(name, category string, key, destinations []string, rootMessage string) string {
	c.t.Helper()
	var profiles struct {
		Items []struct {
			ID     string         `json:"id"`
			Policy map[string]any `json:"policy"`
		} `json:"items"`
	}
	decode(c.t, c.api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	policy := profiles.Items[slices.IndexFunc(profiles.Items, func(p struct {
		ID     string         `json:"id"`
		Policy map[string]any `json:"policy"`
	}) bool {
		return p.ID == "paging"
	})].Policy
	if rootMessage != "" {
		policy["templates"] = map[string]any{"root_message": rootMessage}
	}
	body, _ := json.Marshal(map[string]any{"name": name, "group_key": key, "destination_ids": destinations,
		"matchers": []map[string]string{{"label": "category", "op": "=", "value": category}}, "policy": policy})
	return c.api.json(http.MethodPost, "/api/v1/routes", string(body), http.StatusCreated)["id"].(string)
}

// fire puts the Alerts, one per label set with the annotations, into the fake Alertmanager's group of alertname and
// notifies it; it returns the Alert Group of the first Alert.
func (c *compact) fire(group, alertname string, annotations map[string]string, labels ...map[string]string) string {
	c.t.Helper()
	c.group(group, alertname)
	extra, _ := json.Marshal(annotations)
	for i, l := range labels {
		b, _ := json.Marshal(l)
		c.alert(group, fmt.Sprint(group, "-", i), string(b), `,"annotations":`+string(extra))
	}
	c.notify(group, "first notification")
	var page struct {
		Items []struct {
			Group struct {
				ID string `json:"id"`
			} `json:"alert_group"`
		} `json:"items"`
	}
	decode(c.t, c.api.do(http.MethodGet, "/api/v1/integrations/"+c.integration+"/alerts?"+
		url.Values{"label": {`alertname="` + alertname + `"`}}.Encode(), ""), &page)
	if len(page.Items) == 0 {
		c.t.Fatalf("no alert of %s", alertname)
	}
	return page.Items[0].Group.ID
}

// tgText is the text of the last channel post of Telegram that names #n, as it is now.
func (c *compact) tgText(n int) string {
	c.t.Helper()
	var text string
	eventually(c.t, fmt.Sprintf("the telegram post of #%d", n), func() bool {
		for _, m := range c.tg.messages(faketelegram.ChannelID) {
			if strings.Contains(m.Text, fmt.Sprintf(">#%d ", n)) {
				text = m.Text
			}
		}
		return text != ""
	})
	return text
}

// brBody is the body of the last message of the fake bridge titled with title, with its last edit.
func (c *compact) brBody(title string) string {
	c.t.Helper()
	m := c.br.waitPosted(title, 1)[0]
	if len(m.Edits) > 0 {
		return m.Edits[len(m.Edits)-1].Body
	}
	return m.Envelope.Body
}

// TestCompactLayout is the Verification of S-125 against `muster dev` and the fake Mattermost, Telegram, Slack and
// bridge servers: the Root message of three KubernetesContainerOOMKilled Alerts on a Route with the Group key
// alertname, system and team shows the title with the status as a word, the fields Severity, Route, Teams and Started,
// the summary and the description, `system=kubernetes · team=payments · namespace=checkout` and the line "pod:" with
// the three values, the links last — as attachment fields in Mattermost, a section's fields in Slack and lines in
// Telegram, and in the body of a bridge (C-29.AC-1); twelve Alerts list five values and "…and 7 more", two namespace
// values show "namespace: 2 values", one Alert shows no Alerts line and a label no Alert has is left out (C-29.AC-2,
// AC-3); a long description is cut first within Telegram's 4,096 (C-29.FR-6); a Route template keeps its body under
// Muster's title, fields and links (C-29.AC-5); adding cluster to "Labels in the message" edits the open Root messages
// Quietly (C-29.AC-4); the label suggestions answer routes:read and refuse a token without it; and previewTemplate
// carries the layout with its length against the messenger's limit (C-29.FR-7).
func TestCompactLayout(t *testing.T) {
	h := Start(t, DevProcess)
	c, dests := newCompact(t, h, h.Replicas[0])
	route := c.routeOf("Infrastructure", "infra", []string{"alertname", "system", "team"}, dests, "")
	infra := func(more map[string]string) map[string]string {
		l := map[string]string{"category": "infra", "system": "kubernetes", "team": "payments", "cluster": "prod",
			"severity": "warning"}
		for k, v := range more {
			l[k] = v
		}
		return l
	}
	oom := map[string]string{"summary": "Container killed for memory", "description": "The container exceeded its limit."}
	g := c.fire("c1", "KubernetesContainerOOMKilled", oom,
		infra(map[string]string{"namespace": "checkout", "pod": "checkout-api-7d9f4"}),
		infra(map[string]string{"namespace": "checkout", "pod": "checkout-api-5b2c1"}),
		infra(map[string]string{"namespace": "checkout", "pod": "checkout-worker-8e6a0"}))
	n := c.number(g)
	link := devmode.PublicURL + "/alert-groups/" + g
	labels := "system=kubernetes · team=payments · namespace=checkout"
	pods := "pod: checkout-api-5b2c1, checkout-api-7d9f4, checkout-worker-8e6a0"

	// C-29.AC-1 in Mattermost: the title, the four short fields, items 3 to 5, the links as the last line.
	root := c.root(n)
	att := root.attachment(t)
	values := make([]string, len(att.Fields))
	for i, f := range att.Fields {
		values[i] = f.Value
	}
	want := []string{"Container killed for memory", "The container exceeded its limit.", labels, pods,
		"[Open in Muster](" + link + ")"}
	if att.Title != fmt.Sprintf("#%d KubernetesContainerOOMKilled — Firing", n) ||
		fieldTitles(att) != "Severity Route Teams Started" || !slices.Equal(values[:3], []string{"warning",
		"Infrastructure", "payments"}) || !strings.HasSuffix(values[3], " UTC") ||
		!slices.Equal(strings.Split(att.Text, "\n"), want) {
		t.Errorf("mattermost title %q, fields %+v, text\n%s", att.Title, att.Fields, att.Text)
	}
	t.Logf("mattermost: %s | %v | %q", att.Title, values, att.Text)

	// In Telegram: one HTML message, the fields as lines, the status emoji beside the word.
	text := c.tgText(n)
	lines := strings.Split(text, "\n")
	if len(lines) != 10 || lines[0] != fmt.Sprintf(`<b><a href="%s">#%d KubernetesContainerOOMKilled</a></b> — `+
		`🔴 Firing`, link, n) || !slices.Equal(lines[1:4], []string{"Severity: warning", "Route: Infrastructure",
		"Teams: payments"}) || !strings.HasPrefix(lines[4], "Started: ") || !slices.Equal(lines[5:9],
		want[:4]) || lines[9] != `<a href="`+link+`">Open in Muster</a>` {
		t.Errorf("telegram\n%s", text)
	}
	t.Logf("telegram: %q", text)

	// In Slack: the heading with the word, one section's fields, the body sections and the key labels as a context.
	sm := c.sl.root(n, "KubernetesContainerOOMKilled")
	sa := sm.Attachments[0]
	var fields []string
	for _, f := range sa.Blocks[1].Fields {
		fields = append(fields, f.Text)
	}
	if sa.Blocks[0].Text.Text != fmt.Sprintf("*<%s|#%d KubernetesContainerOOMKilled>* — Firing", link, n) ||
		len(fields) != 4 || fields[0] != "*Severity*\nwarning" || fields[2] != "*Teams*\npayments" ||
		sm.body(t) != "Container killed for memory\nThe container exceeded its limit.\n"+labels+"\n"+pods+"\n<"+link+
			"|Open in Muster>" || sa.Blocks[3].Type != "context" {
		t.Errorf("slack %s", sm.Message.Attachments)
	}
	t.Logf("slack: %s | %q | %q", sa.Blocks[0].Text.Text, fields, sm.body(t))

	// Through a bridge: the envelope of L1 with the compact items in its CommonMark body.
	body := c.brBody("KubernetesContainerOOMKilled")
	if !strings.HasPrefix(body, "Severity: warning\\\nRoute: Infrastructure\\\nTeams: payments\\\nStarted: ") ||
		!strings.Contains(body, "\n\nContainer killed for memory\n\nThe container exceeded its limit.\n\n"+labels+"\n\n"+
			pods+"\n\n[Open in Muster]("+link+")") {
		t.Errorf("bridge body %q", body)
	}
	t.Logf("bridge: %q", body)

	// The label suggestions: routes:read, with pod among them; a token without routes:read is refused.
	sugg := c.api.json(http.MethodGet, "/api/v1/routes/"+route+"/label-suggestions", "", http.StatusOK)
	var suggested []string
	for _, it := range sugg["items"].([]any) {
		item := it.(map[string]any)
		suggested = append(suggested, item["label"].(string))
		if item["label"] == "pod" && (item["alert_group_count"] != float64(1) || !strings.HasPrefix(
			item["example_value"].(string), "checkout-")) {
			t.Errorf("suggestion %v", item)
		}
	}
	if !slices.Contains(suggested, "pod") || !slices.Contains(suggested, "namespace") {
		t.Errorf("suggestions %v", suggested)
	}
	admin := newAgent(t, c.r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	narrow := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"no-routes","permissions":["alert-groups:read"]}`, http.StatusCreated)["value"].(string)
	if a := (patClient{t: t, base: c.r.App, token: narrow}).do(http.MethodGet,
		"/api/v1/routes/"+route+"/label-suggestions", ""); a.status != http.StatusForbidden {
		t.Errorf("without routes:read = %d %s", a.status, a.body)
	}

	// previewTemplate of the Alert Group: the layout with its length against Telegram's limit.
	preview := c.api.json(http.MethodPost, "/api/v1/template-previews", fmt.Sprintf(`{"kind":"root_message",
		"template":"","format":"html","route_id":%q,"alert_group_id":%q,"length_limit":4096}`, route, g), http.StatusOK)
	layout, _ := preview["layout"].(map[string]any)
	var names []string
	for _, f := range layout["fields"].([]any) {
		names = append(names, f.(map[string]any)["name"].(string))
	}
	if layout["number"] != float64(n) || layout["title"] != "KubernetesContainerOOMKilled" ||
		!slices.Equal(names, []string{"Severity", "Route", "Teams", "Started"}) || preview["messenger_limit"] !=
		float64(4096) || preview["length"].(float64) <= 0 || layout["alerts"].(map[string]any)["total"] != float64(3) {
		t.Errorf("preview %v", preview)
	}

	// C-29.AC-3: twelve Alerts list five nodes and "…and 7 more"; C-29.AC-2: two namespace values; one Alert, no
	// Alerts line and no namespace when no Alert has one.
	var nodes []map[string]string
	for i := 1; i <= 12; i++ {
		nodes = append(nodes, infra(map[string]string{"namespace": "kube-system", "node": fmt.Sprintf("node-%02d", i)}))
	}
	twelve := c.number(c.fire("c2", "NodeDiskPressure", nil, nodes...))
	if text := c.root(twelve).attachment(t).Text; !strings.Contains(text,
		"\nnode: node-01, node-02, node-03, node-04, node-05 …and 7 more\n") {
		t.Errorf("twelve alerts\n%s", text)
	}
	two := c.number(c.fire("c3", "PodCrashLooping", nil, infra(map[string]string{"namespace": "a", "pod": "p1"}),
		infra(map[string]string{"namespace": "b", "pod": "p2"})))
	if text := c.root(two).attachment(t).Text; !strings.Contains(text,
		"system=kubernetes · team=payments · namespace: 2 values\npod: p1, p2\n") {
		t.Errorf("two namespaces\n%s", text)
	}
	one := c.number(c.fire("c4", "NodeDown", map[string]string{"summary": "A node is down"}, infra(nil)))
	if text := c.root(one).attachment(t).Text; text != "A node is down\nsystem=kubernetes · team=payments\n"+
		"[Open in Muster]("+devmode.PublicURL+"/alert-groups/"+c.groupByName("NodeDown")+")" {
		t.Errorf("one alert\n%s", text)
	}

	// C-29.FR-6: a long description and summary within Telegram's 4,096 lose the description first.
	long := c.number(c.fire("c5", "DiskAlmostFull", map[string]string{"summary": strings.Repeat("s", 1000),
		"description": strings.Repeat("d", 4000)}, infra(map[string]string{"disk": "sda"}),
		infra(map[string]string{"disk": "sdb"})))
	if text := c.tgText(long); utf16Length(text) > 4096 || strings.Contains(text, "ddd") ||
		!strings.Contains(text, "\n"+strings.Repeat("s", 1000)+"\n") || !strings.Contains(text, "\ndisk: sda, sdb\n") ||
		!strings.Contains(text, "\nSeverity: warning\nRoute: Infrastructure\n") {
		t.Errorf("long description (%d)\n%.300s", utf16Length(text), text)
	}

	// C-29.AC-5: a Route template keeps its body under Muster's title, fields and links.
	c.routeOf("Templated", "tmpl", []string{"alertname"}, dests[:1], `Custom body for {{ .AlertGroup.Title }}`)
	tg := c.fire("c6", "TemplatedAlert", nil, map[string]string{"category": "tmpl", "pod": "t1"},
		map[string]string{"category": "tmpl", "pod": "t2"})
	ta := c.root(c.number(tg)).attachment(t)
	if ta.Title != fmt.Sprintf("#%d TemplatedAlert — Firing", c.number(tg)) || fieldTitles(ta) !=
		"Severity Route Started" || ta.Text != "Custom body for TemplatedAlert\n[Open in Muster]("+devmode.PublicURL+
		"/alert-groups/"+tg+")" {
		t.Errorf("templated %q %+v\n%s", ta.Title, ta.Fields, ta.Text)
	}

	// C-29.AC-4: cluster joins "Labels in the message"; the open Root messages of the Route are edited Quietly.
	posts, notifications := len(c.posts()), len(c.notifications())
	rt := c.api.json(http.MethodGet, "/api/v1/routes/"+route, "", http.StatusOK)
	if rt["own_message_labels"] != false || fmt.Sprint(rt["message_labels"]) != "[system team namespace]" {
		t.Errorf("route %v %v", rt["own_message_labels"], rt["message_labels"])
	}
	for _, k := range []string{"id", "etag", "position", "is_default", "destinations", "storm_active", "storm",
		"open_alert_group_count", "created_at", "urgent", "owning_team", "template_error"} {
		delete(rt, k)
	}
	rt["own_message_labels"], rt["message_labels"] = true, []string{"system", "team", "namespace", "cluster"}
	b, _ := json.Marshal(rt)
	c.api.json(http.MethodPut, "/api/v1/routes/"+route, string(b), http.StatusOK, "If-Match", "*")
	clustered := "system=kubernetes · team=payments · namespace=checkout · cluster=prod"
	eventually(t, "the edited root messages", func() bool {
		return strings.Contains(c.post(root.ID).attachment(t).Text, "\n"+clustered+"\n") &&
			strings.Contains(c.tgText(n), "\n"+clustered+"\n") &&
			strings.Contains(c.sl.root(n, "KubernetesContainerOOMKilled").body(t), "\n"+clustered+"\n") &&
			strings.Contains(c.brBody("KubernetesContainerOOMKilled"), "\n\n"+clustered+"\n\n")
	})
	if len(c.posts()) != posts || len(c.notifications()) != notifications {
		t.Errorf("the edit posted %d and notified %d", len(c.posts())-posts, len(c.notifications())-notifications)
	}
	t.Logf("after cluster joined: mattermost %q", c.post(root.ID).attachment(t).Text)
}

// groupByName is the Alert Group of the Alert of alertname.
func (c *compact) groupByName(alertname string) string {
	c.t.Helper()
	var page struct {
		Items []struct {
			Group struct {
				ID string `json:"id"`
			} `json:"alert_group"`
		} `json:"items"`
	}
	decode(c.t, c.api.do(http.MethodGet, "/api/v1/integrations/"+c.integration+"/alerts?"+
		url.Values{"label": {`alertname="` + alertname + `"`}}.Encode(), ""), &page)
	return page.Items[0].Group.ID
}

// utf16Length is the length of s in UTF-16 code units, as Telegram counts it.
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}
