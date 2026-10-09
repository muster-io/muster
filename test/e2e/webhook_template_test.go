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
)

// chatText is the text the recipes write for an Alert Group — "#N title (status)" — or for a Storm summary.
const chatText = `{{ if .Storm }}{{ printf "Storm on %s: %d new, %d urgent, final %v" .Storm.Route ` +
	`.Storm.AlertGroupCount .Storm.UrgentCount .Storm.Final | toJson }}{{ else }}{{ printf "#%d %s (%s)" ` +
	`.AlertGroup.Number .AlertGroup.Title .AlertGroup.Status | toJson }}{{ end }}`

// chatTemplate is the request templates of a chat of the fake receiver as the documentation's recipes have them:
// "create" posts with the Secret token and the Mentions, "update" edits the message by its extracted id and carries
// the final edit's text; Thread replies go in one step under the message or, with twoStep, into a thread "open
// thread" creates first.
func chatTemplate(chat string, twoStep bool) string {
	base := webhookBase + "/chat/" + chat
	create := map[string]any{"method": "POST", "url": base + "/messages",
		"headers": []map[string]string{{"name": "Authorization", "value": "Bearer {{ .Secrets.token }}"}},
		"body":    `{"text":` + chatText + `,"who":"{{ range .Mentions }}<{{ mention . }}>{{ end }}"}`,
		"extract": []map[string]string{{"name": "id", "path": "$.data.id"}}}
	update := map[string]any{"method": "PUT", "url": base + "/messages/{{ .Response.id }}", "headers": []any{},
		"body": `{"text":` + chatText + `,"final":{{ .Final | toJson }}}`}
	reply := `{"text":{{ printf "%s %v" .Event .Notify | toJson }}}`
	t := map[string]any{"create": create, "update": update}
	if twoStep {
		t["open_thread"] = map[string]any{"method": "POST", "url": base + "/threads", "headers": []any{},
			"body": `{"root":"{{ .Response.id }}"}`, "extract": []map[string]string{{"name": "thread",
				"path": "$.thread.id"}}}
		t["reply_in_thread"] = map[string]any{"method": "POST", "url": base + "/threads/{{ .Response.thread }}/messages",
			"headers": []any{}, "body": reply}
	} else {
		t["reply_in_thread"] = map[string]any{"method": "POST", "url": base + "/messages/{{ .Response.id }}/replies",
			"headers": []any{}, "body": reply}
	}
	b, _ := json.Marshal(t)
	return string(b)
}

// chatState is a chat of the fake receiver.
type chatState struct {
	Messages []struct {
		ID      string               `json:"id"`
		Text    string               `json:"text"`
		Body    struct{ Who string } `json:"body"`
		Replies []struct {
			Text string `json:"text"`
		} `json:"replies"`
	} `json:"messages"`
	Edits []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
		Body struct {
			Final string `json:"final"`
		} `json:"body"`
	} `json:"edits"`
	Threads []struct {
		ID       string `json:"id"`
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
	} `json:"threads"`
}

func (e *mm) chat(name string) chatState {
	e.t.Helper()
	var c chatState
	decode(e.t, call(e.t, http.MethodGet, webhookBase+"/_fake/chat/"+name, ""), &c)
	return c
}

// templateWebhook creates an outgoing webhook in the mode, with the request templates template, the events request to
// /hook/{hook} in the mode both, and the Mention settings, and sets its Secret token. It returns the Destination's id.
func (e *mm) templateWebhook(name, mode, template, hook string, mentions map[string]string) string {
	e.t.Helper()
	events := ""
	if mode == "both" {
		events = `"events":{"url":"` + webhookBase + `/hook/` + hook + `","headers":[]},`
	}
	created := e.api.json(http.MethodPost, "/api/v1/destinations", `{"type":"webhook","name":"`+name+`","mode":"`+
		mode+`",`+events+`"template":`+template+`,"proxy":{"enabled":false},"mentions":`+mentionSettings(mentions)+
		`,"limiter":{"limit":50,"per_seconds":1}}`, http.StatusCreated)
	id := created["destination"].(map[string]any)["id"].(string)
	e.api.json(http.MethodPut, "/api/v1/destinations/"+id+"/secrets/token", `{"value":"chat-token-value"}`,
		http.StatusOK)
	return id
}

// webhookFault scripts a fault of the fake receiver; reset clears them all.
func webhookFault(t *testing.T, fault string) {
	t.Helper()
	if a := call(t, http.MethodPost, webhookBase+"/_fake/faults", fault); a.status/100 != 2 {
		t.Fatalf("fault = %d %s", a.status, a.body)
	}
}

func resetWebhookFaults(t *testing.T) {
	t.Helper()
	call(t, http.MethodDelete, webhookBase+"/_fake/faults", "")
}

// TestWebhookTemplate is the end-to-end check of the template mode of outgoing webhooks against `muster dev` and its
// fake chat (C-15.FR-3, FR-4, FR-7, FR-11, FR-12, AC-3, AC-9, AC-10; C-12.FR-4).
func TestWebhookTemplate(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)

	t.Run("two-step threads", func(t *testing.T) {
		// C-15.AC-3: create, extract, update with the id; FR-3: open thread once, then replies; FR-11: Mentions.
		e := e.on(t)
		d := e.templateWebhook("chat", "template", chatTemplate("ops", true), "", map[string]string{
			"new_alert_group": "all"})
		e.route("chat", "chat", []string{d}, nil)
		e.group("c1", "QueueStuck")
		e.alert("c1", "a", `{"team":"chat","cluster":"c1","queue":"q1"}`, "")
		e.notify("c1", "first notification")
		g := e.groupOf("c1")
		n := e.number(g)
		eventually(t, "the create", func() bool { return len(e.chat("ops").Messages) == 1 })
		e.command(g, "acknowledge")
		eventually(t, "the update", func() bool { return len(e.chat("ops").Edits) == 1 })
		c := e.chat("ops")
		if want := fmt.Sprintf("#%d QueueStuck (firing)", n); c.Messages[0].ID != "m1" || c.Messages[0].Text != want ||
			c.Messages[0].Body.Who != "<@all>" || c.Edits[0].ID != "m1" ||
			c.Edits[0].Text != fmt.Sprintf("#%d QueueStuck (acknowledged)", n) {
			t.Fatalf("chat %+v", c)
		}
		e.command(g, "unacknowledge")
		for i, pod := range []string{"q2", "q3"} {
			e.alert("c1", pod, `{"team":"chat","cluster":"c1","queue":"`+pod+`"}`, "")
			e.notify("c1", "new alerts added")
			advance(t, r, 61)
			eventually(t, "the reply", func() bool {
				c := e.chat("ops")
				return len(c.Threads) == 1 && len(c.Threads[0].Messages) == i+1
			})
		}
		c = e.chat("ops")
		if len(c.Threads) != 1 || !slices.Equal([]string{c.Threads[0].Messages[0].Text, c.Threads[0].Messages[1].Text},
			[]string{"alerts_added true", "alerts_added true"}) {
			t.Errorf("threads %+v", c.Threads)
		}
		t.Logf("chat ops: %d message, %d edits, %d thread with %d replies", len(c.Messages), len(c.Edits),
			len(c.Threads), len(c.Threads[0].Messages))

		// C-15.FR-4: a rule that finds nothing; C-15.FR-7: the next request needs the value.
		var tpl map[string]any
		_ = json.Unmarshal([]byte(chatTemplate("ops", true)), &tpl)
		tpl["create"].(map[string]any)["extract"] = []map[string]string{{"name": "id", "path": "$.nothing.here"}}
		b, _ := json.Marshal(tpl)
		read := e.api.json(http.MethodGet, "/api/v1/destinations/"+d, "", http.StatusOK)
		e.api.json(http.MethodPut, "/api/v1/destinations/"+d, `{"type":"webhook","name":"chat","mode":"template",`+
			`"template":`+string(b)+`,"proxy":{"enabled":false},"mentions":`+mentionSettings(nil)+
			`,"limiter":{"limit":50,"per_seconds":1}}`, http.StatusOK, "If-Match", read["etag"].(string))
		e.group("c2", "Backlog")
		e.alert("c2", "a", `{"team":"chat","cluster":"c2"}`, "")
		e.notify("c2", "first notification")
		g2 := e.groupOf("c2")
		eventually(t, "template_value_missing", func() bool {
			var page struct {
				Items []struct {
					SystemEvent string `json:"system_event"`
					Detail      string `json:"detail"`
				} `json:"items"`
			}
			decode(t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g2+"/timeline?kind=system", ""), &page)
			return len(page.Items) > 0 && page.Items[0].SystemEvent == "template_value_missing" &&
				page.Items[0].Detail == "id"
		})
		e.command(g2, "acknowledge")
		eventually(t, "the template error", func() bool {
			te, _ := e.api.json(http.MethodGet, "/api/v1/destinations/"+d, "", http.StatusOK)["template_error"].(map[string]any)
			return te != nil && te["fallback"] == "not_sent" && strings.Contains(te["error"].(string), "the value id is missing")
		})
		var deliveries struct {
			Items []struct {
				State string `json:"state"`
			} `json:"items"`
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g2+"/deliveries", ""), &deliveries)
		if len(deliveries.Items) != 1 || deliveries.Items[0].State != "not_delivered" {
			t.Errorf("deliveries %+v", deliveries)
		}
		eventually(t, "MusterTemplateError", func() bool { return slices.Contains(e.internalAlerts(), "MusterTemplateError") })

		// C-12.FR-4: a preview of a request template masks the Secret.
		p := e.api.json(http.MethodPost, "/api/v1/template-previews",
			`{"kind":"webhook_request","template":"Bearer {{ .Secrets.token }} for #{{ .AlertGroup.Number }}"}`,
			http.StatusOK)
		if p["output"] != "Bearer [redacted] for #1" || p["source"] != nil {
			t.Errorf("preview %v", p)
		}
	})

	t.Run("one-step threads", func(t *testing.T) {
		e := e.on(t)
		d := e.templateWebhook("chat-one", "template", chatTemplate("one", false), "", nil)
		e.route("chat-one", "chat-one", []string{d}, nil)
		e.group("o1", "OneStep")
		e.alert("o1", "a", `{"team":"chat-one","cluster":"o1","queue":"a"}`, "")
		e.notify("o1", "first notification")
		eventually(t, "the create", func() bool { return len(e.chat("one").Messages) == 1 })
		e.alert("o1", "b", `{"team":"chat-one","cluster":"o1","queue":"b"}`, "")
		e.notify("o1", "new alerts added")
		eventually(t, "the reply", func() bool {
			c := e.chat("one")
			return len(c.Messages[0].Replies) == 1 && c.Messages[0].Replies[0].Text == "alerts_added true"
		})
		if c := e.chat("one"); len(c.Threads) != 0 || len(c.Messages) != 1 {
			t.Errorf("chat %+v", c)
		}
	})

	t.Run("storm", func(t *testing.T) {
		// C-15.AC-10: 20 creates and one Storm summary in the chat, 30 created events at the events-mode webhook.
		e := e.on(t)
		d := e.templateWebhook("chat-storm", "template", chatTemplate("storm", false), "", nil)
		hook := e.webhook("storm-events-t", "storm-t", `[]`)
		e.route("chat-storm", "chat-storm", []string{d, hook["id"].(string)},
			func(p map[string]any) { p["storm_threshold"] = 20 })
		e.group("ts", "ChatSurge")
		for i := 1; i <= 30; i++ {
			e.alert("ts", fmt.Sprintf("s%02d", i), fmt.Sprintf(`{"team":"chat-storm","cluster":"ts%02d"}`, i), "")
		}
		e.notify("ts", "first notification")
		eventually(t, "30 created events", func() bool { return len(e.received("storm-t")) >= 30 })
		summary := func(c chatState) (groups, summaries int) {
			for _, m := range c.Messages {
				if strings.HasPrefix(m.Text, "Storm on chat-storm") {
					summaries++
				} else {
					groups++
				}
			}
			return groups, summaries
		}
		eventually(t, "the summary and 20 creates", func() bool {
			g, s := summary(e.chat("storm"))
			return g >= 20 && s == 1
		})
		g, s := summary(e.chat("storm"))
		if g != 20 || s != 1 {
			t.Errorf("%d creates and %d summaries, want 20 and 1", g, s)
		}
		created := 0
		for _, x := range e.received("storm-t") {
			if x.Body.Event == "created" {
				created++
			}
		}
		if created != 30 {
			t.Errorf("%d created events, want 30", created)
		}
		// The Storm ends: the summary's final state through "update".
		advance(t, r, 61)
		advance(t, r, 301)
		eventually(t, "the final state of the summary", func() bool {
			for _, x := range e.chat("storm").Edits {
				if strings.Contains(x.Text, "final true") && strings.HasPrefix(x.Text, "Storm on chat-storm: ") {
					return true
				}
			}
			return false
		})
		t.Logf("storm: %d creates and %d summary in the chat, %d created events", g, s, created)
	})

	t.Run("recovery sends the current state", func(t *testing.T) {
		// C-15.AC-9: a 404 makes the Destination Broken; after recovery one update per published Alert Group, no
		// reply for the events of the period and no create for an Alert Group that started and resolved meanwhile.
		e := e.on(t)
		d := e.templateWebhook("chat-rec", "template", chatTemplate("rec", false), "", nil)
		e.route("chat-rec", "chat-rec", []string{d}, nil)
		e.group("r1", "Recover")
		e.alert("r1", "a", `{"team":"chat-rec","cluster":"r1"}`, "")
		e.notify("r1", "first notification")
		a := e.groupOf("r1")
		eventually(t, "the create", func() bool { return len(e.chat("rec").Messages) == 1 })
		webhookFault(t, `{"path":"/chat/rec/*","status":404}`)
		webhookFault(t, `{"path":"/chat/rec/messages/*","status":404}`)
		e.command(a, "acknowledge")
		e.waitHealth(d, "broken")
		e.alert("r1", "b", `{"team":"chat-rec","cluster":"r1","queue":"b"}`, "")
		e.notify("r1", "new alerts added")
		e.group("r2", "Passing")
		e.alert("r2", "a", `{"team":"chat-rec","cluster":"r2"}`, "")
		e.notify("r2", "first notification")
		e.alert("r2", "a", `{"team":"chat-rec","cluster":"r2"}`, `,"status":"resolved"`)
		e.notify("r2", "some alerts resolved")
		resetWebhookFaults(t)
		advance(t, r, 301)
		e.waitHealth(d, "healthy")
		eventually(t, "the current state", func() bool { return len(e.chat("rec").Edits) == 1 })
		c := e.chat("rec")
		if len(c.Messages) != 1 || len(c.Messages[0].Replies) != 0 || !strings.HasSuffix(c.Edits[0].Text,
			"(acknowledged)") {
			t.Errorf("after recovery %+v", c)
		}
	})

	t.Run("deletion", func(t *testing.T) {
		// C-15.FR-12, C-11.FR-14: mode template — the final edit through "update", then the secrets are wiped; mode
		// both — the queued events end at once, the final edit goes out, then the secrets are wiped.
		e := e.on(t)
		tm := e.templateWebhook("chat-del", "template", chatTemplate("del", false), "", nil)
		both := e.templateWebhook("chat-both", "both", chatTemplate("both", false), "both", nil)
		e.route("chat-del", "chat-del", []string{tm, both}, nil)
		webhookFault(t, `{"path":"/hook/both","status":503}`)
		e.group("d1", "Deleted")
		e.alert("d1", "a", `{"team":"chat-del","cluster":"d1"}`, "")
		e.notify("d1", "first notification")
		eventually(t, "the creates", func() bool {
			return len(e.chat("del").Messages) == 1 && len(e.chat("both").Messages) == 1
		})
		for _, id := range []string{tm, both} {
			e.api.do(http.MethodDelete, "/api/v1/destinations/"+id, "")
		}
		if n := h.count(t, `SELECT count(*) FROM webhook_events w JOIN destinations d ON d.id = w.destination_id
			WHERE d.public_id = '`+both+`' AND w.state = 'pending'`); n != 0 {
			t.Errorf("%d events still queued", n)
		}
		for _, chat := range []string{"del", "both"} {
			eventually(t, "the final edit in "+chat, func() bool {
				edits := e.chat(chat).Edits
				return len(edits) == 1 && strings.HasPrefix(edits[0].Body.Final, "No longer updated here")
			})
		}
		for _, id := range []string{tm, both} {
			eventually(t, "the secrets of "+id+" wiped", func() bool {
				return h.count(t, `SELECT count(*) FROM destinations d WHERE d.public_id = '`+id+`'
					AND (d.signing_secret_ciphertext IS NOT NULL OR EXISTS (SELECT 1 FROM destination_secrets s
					WHERE s.destination_id = d.id))`) == 0
			})
		}
		resetWebhookFaults(t)
	})
}
