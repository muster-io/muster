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

	buttonids "github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
)

// slk drives a Slack Connection "sl" of the fake Slack server's second app and its Destination "alerts" on the channel
// C0FAKEALERTS, which the Route "sl" sends to, on top of mm's Integration and fake Alertmanager.
type slk struct {
	*mm
	fs         string
	conn, dest string
}

// The label value of C-24.AC-6, and what Slack receives of it.
const (
	slackLabel   = "@channel <!here> <b>x</b> *y* <https://evil.example|z> &"
	slackEscaped = "@\u200bchannel &lt;!here&gt; &lt;b&gt;x&lt;/b&gt; *\u200by*\u200b &lt;https://evil.example|z&gt; &amp;"
)

func newSlack(t *testing.T, h *Harness, r *Replica) *slk {
	t.Helper()
	s := &slk{mm: newMM(t, h, r), fs: h.Fakes.Slack + "/_fake"}
	s.conn = s.api.json(http.MethodPost, "/api/v1/connections", `{"type":"slack","name":"sl","event_mode":"socket",
		"bot_token":"`+fakeslack.SecondBotToken+`","app_token":"`+fakeslack.SecondAppToken+`","proxy":{"enabled":false},
		"limiter":{"limit":50,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	if c := s.api.json(http.MethodPost, "/api/v1/connections/"+s.conn+"/checks", "", http.StatusOK); c["ok"] != true {
		t.Fatalf("the connection check %v", c)
	}
	created := s.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"slack","name":"alerts",
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":50,"per_seconds":1}}`, s.conn,
		fakeslack.ChannelAlerts, mentionSettings(map[string]string{"new_alert_group": "here"})), http.StatusCreated)
	s.dest = created["destination"].(map[string]any)["id"].(string)
	// No ack timeout; a Reminder 600 seconds after an acknowledgement, a reply alone, never unacknowledging.
	s.route("sl", "sl", []string{s.dest}, func(p map[string]any) {
		p["ack_timeout"] = map[string]any{"enabled": false, "first_interval_seconds": 900}
		p["reminders"] = map[string]any{"enabled": true, "first_interval_seconds": 600, "cap_seconds": 14400}
		p["auto_unacknowledge"] = false
	})
	return s
}

func (s *slk) on(t *testing.T) *slk {
	c := *s
	c.mm = s.mm.on(t)
	return &c
}

// fire puts one firing Alert of the label set labels into a new Alertmanager group and notifies it.
func (s *slk) fire(group, alertname, alert, labels string) {
	s.t.Helper()
	s.group(group, alertname)
	s.alert(group, alert, labels, "")
	s.notify(group, "first notification")
}

// slackElement is an element of a block: a button, the Snooze menu or a text of a context.
type slackElement struct {
	Type        string          `json:"type"`
	ActionID    string          `json:"action_id"`
	Style       *string         `json:"style"`
	Value       string          `json:"value"`
	Text        json.RawMessage `json:"text"`
	Placeholder *struct {
		Text string `json:"text"`
	} `json:"placeholder"`
	Options []struct {
		Text struct {
			Text string `json:"text"`
		} `json:"text"`
		Value string `json:"value"`
	} `json:"options"`
}

// label is the text of a button, the placeholder of a menu or the text of a context's element.
func (e slackElement) label() string {
	if e.Placeholder != nil {
		return e.Placeholder.Text
	}
	var obj struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(e.Text, &obj) == nil {
		return obj.Text
	}
	var s string
	_ = json.Unmarshal(e.Text, &s)
	return s
}

type slackBlock struct {
	Type    string `json:"type"`
	BlockID string `json:"block_id"`
	Text    *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"text"`
	Elements []slackElement `json:"elements"`
}

type slackAttachment struct {
	Color    string       `json:"color"`
	Fallback string       `json:"fallback"`
	Blocks   []slackBlock `json:"blocks"`
}

// slackMessage is a message of the fake's channel with its blocks and attachments read.
type slackMessage struct {
	fakeslack.Message
	Top         []slackBlock
	Attachments []slackAttachment
}

// body is the text of the body section of the message's attachment.
func (m slackMessage) body(t *testing.T) string {
	t.Helper()
	if len(m.Attachments) != 1 || len(m.Attachments[0].Blocks) < 2 || m.Attachments[0].Blocks[1].Text == nil {
		t.Fatalf("message %s has no body: %s", m.TS, m.Message.Attachments)
	}
	return m.Attachments[0].Blocks[1].Text.Text
}

// actions are the elements of the actions block of the message's attachment, none without one.
func (m slackMessage) actions() []slackElement {
	if len(m.Attachments) != 1 {
		return nil
	}
	for _, b := range m.Attachments[0].Blocks {
		if b.Type == "actions" {
			return b.Elements
		}
	}
	return nil
}

// messages are the messages of the channel alerts of the fake's second app, in the order they were posted.
func (s *slk) messages() []slackMessage {
	s.t.Helper()
	var raw []fakeslack.Message
	decode(s.t, call(s.t, http.MethodGet, s.fs+"/messages?app=b&channel="+fakeslack.ChannelAlerts, ""), &raw)
	out := make([]slackMessage, len(raw))
	for i, m := range raw {
		out[i].Message = m
		_ = json.Unmarshal(m.Blocks, &out[i].Top)
		_ = json.Unmarshal(m.Attachments, &out[i].Attachments)
	}
	return out
}

// roots are the Root messages of Alert Group #n titled title, in the order they were posted; a tombstone has no title.
func (s *slk) roots(n int, title string) []slackMessage {
	s.t.Helper()
	var out []slackMessage
	for _, m := range s.messages() {
		if strings.Contains(m.Text, fmt.Sprintf("#%d %s", n, title)) && !strings.Contains(m.Text, "Test message") {
			out = append(out, m)
		}
	}
	return out
}

// root waits for the last Root message of Alert Group #n other than those of the ts in not, and returns it.
func (s *slk) root(n int, title string, not ...string) slackMessage {
	s.t.Helper()
	var got slackMessage
	eventually(s.t, fmt.Sprintf("the root message of #%d", n), func() bool {
		for _, m := range s.roots(n, title) {
			if !slices.Contains(not, m.TS) {
				got = m
			}
		}
		return got.TS != ""
	})
	return got
}

// message is the message ts of the channel, failing without one.
func (s *slk) message(ts string) slackMessage {
	s.t.Helper()
	for _, m := range s.messages() {
		if m.TS == ts {
			return m
		}
	}
	s.t.Fatalf("no message %s", ts)
	return slackMessage{}
}

// requests are the requests the fake received for the method, in order.
func (s *slk) requests(method string) []fakeserver.Request {
	s.t.Helper()
	var out []fakeserver.Request
	for _, r := range fakeRequests(s.t, s.h.Fakes.Slack) {
		if r.Path == "/api/"+method {
			out = append(out, r)
		}
	}
	return out
}

// fault makes the next answer to method the status with Slack's error code, with Retry-After when retryAfter is set.
func (s *slk) fault(method string, status int, code string, retryAfter int) {
	s.t.Helper()
	body, _ := json.Marshal(map[string]any{"ok": false, "error": code})
	fakeFault(s.t, s.h.Fakes.Slack, fakeserver.Fault{Path: "/api/" + method, Status: status,
		RetryAfterSeconds: retryAfter, ContentType: "application/json", Body: string(body), Times: 1})
}

// member adds the bot to the channel alerts of the second app, or removes it.
func (s *slk) member(in bool) {
	s.t.Helper()
	s.fake(http.MethodPut, s.fs+"/config", fmt.Sprintf(`{"channels":{"b":{%q:{"name":"alerts","member":%t}}}}`,
		fakeslack.ChannelAlerts, in))
}

// remove deletes the message ts as a person would.
func (s *slk) remove(ts string) {
	s.t.Helper()
	s.fake(http.MethodPost, s.fs+"/delete", fmt.Sprintf(`{"channel":%q,"ts":%q}`, fakeslack.ChannelAlerts, ts))
}

// settled waits until no delivery and no Thread reply of the Destination is pending.
func (s *slk) settled() {
	s.t.Helper()
	eventually(s.t, "the deliveries to settle", func() bool {
		return s.h.count(s.t, `SELECT (SELECT count(*) FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id
			WHERE ds.public_id = '`+s.dest+`' AND d.state = 'pending') + (SELECT count(*) FROM thread_replies r
			JOIN destinations ds ON ds.id = r.destination_id WHERE ds.public_id = '`+s.dest+`'
			AND r.state = 'pending')`) == 0
	})
}

// deliveryOf is the delivery of the Alert Group to the Destination.
func (s *slk) deliveryOf(group string) map[string]any {
	s.t.Helper()
	for _, item := range s.api.json(http.MethodGet, "/api/v1/alert-groups/"+group+"/deliveries", "",
		http.StatusOK)["items"].([]any) {
		if d := item.(map[string]any); d["destination"].(map[string]any)["id"] == s.dest {
			return d
		}
	}
	s.t.Fatalf("no delivery of %s to %s", group, s.dest)
	return nil
}

func count(events []string, event string) int {
	n := 0
	for _, e := range events {
		if e == event {
			n++
		}
	}
	return n
}

// signedStrings are the strings of a JSON value that look like signed action ids.
func signedStrings(v any) []string {
	var out []string
	switch x := v.(type) {
	case string:
		if buttonids.LooksSigned(x) {
			out = append(out, x)
		}
	case []any:
		for _, e := range x {
			out = append(out, signedStrings(e)...)
		}
	case map[string]any:
		for _, e := range x {
			out = append(out, signedStrings(e)...)
		}
	}
	return out
}

// TestSlack is the Verification of S-099 against `muster dev` and the fake Slack server, with E2E_REPLICAS=2 on two
// replicas: the Root message in blocks with its colour, buttons and Snooze menu, escaped alert data and the Loud
// Mention (C-24.AC-5, AC-6, AC-15), the edit and the Thread reply (C-24.FR-6), the Request log (C-24.FR-14), a Root
// message deleted after a reply and one deleted before any (C-24.AC-8), 429, rejected blocks and a closed edit window
// (C-24.AC-8), the test message and the preview (C-24.AC-16, C-16.FR-3), and Broken and the probe (C-24.AC-9).
func TestSlack(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	s := newSlack(t, h, r)
	t.Cleanup(func() { restore(t, http.MethodDelete, h.Fakes.Slack+"/_fake/faults", "") })
	var g string
	var n int
	var root slackMessage

	t.Run("root_message", func(t *testing.T) {
		s := s.on(t)
		labels, _ := json.Marshal(map[string]string{"team": "sl", "cluster": "a", "pod": "i1", "note": slackLabel})
		s.fire("s1", "DiskFull", "i1", string(labels))
		g = s.groupOf("a")
		n = s.number(g)
		root = s.root(n, "DiskFull")
		summary := fmt.Sprintf("🔴 #%d DiskFull", n)
		if root.Text != summary+" <!here>" || len(root.Top) != 1 || root.Top[0].Type != "section" ||
			root.Top[0].Text.Text != summary+"\n<!here>" || root.UnfurlLinks == nil || *root.UnfurlLinks ||
			root.UnfurlMedia == nil || *root.UnfurlMedia {
			t.Errorf("root message %q, top %s, unfurl %v %v", root.Text, root.Blocks, root.UnfurlLinks, root.UnfurlMedia)
		}
		if len(root.Attachments) != 1 {
			t.Fatalf("attachments %s", root.Message.Attachments)
		}
		att := root.Attachments[0]
		var types []string
		for _, b := range att.Blocks {
			types = append(types, b.Type)
		}
		heading := fmt.Sprintf("*<%s/alert-groups/%s|#%d DiskFull>*", devmode.PublicURL, g, n)
		if att.Color != "#d32f2f" || !slices.Equal(types, []string{"section", "section", "context", "actions"}) ||
			att.Blocks[0].Text.Text != heading || !strings.HasPrefix(att.Blocks[2].Elements[0].label(), "Muster v") ||
			att.Blocks[2].Elements[0].Type != "plain_text" {
			t.Errorf("attachment %s", root.Message.Attachments)
		}
		body := root.body(t)
		if !strings.Contains(body, "<"+devmode.PublicURL+"/alert-groups/"+g+"|Open in Muster>") ||
			!strings.Contains(body, "note: "+slackEscaped) || strings.Contains(body, "<https://evil") ||
			strings.Contains(string(root.Message.Attachments), "<!here>") {
			t.Errorf("body %q", body)
		}
		var got []string
		for _, el := range root.actions() {
			style := ""
			if el.Style != nil {
				style = *el.Style
			}
			got = append(got, el.Type+":"+el.ActionID+":"+style+":"+el.label())
			for _, v := range append([]string{el.Value}, optionValues(el)...) {
				if v != "" && !buttonids.LooksSigned(v) {
					t.Errorf("value %q of %s is no signed action id", v, el.ActionID)
				}
			}
		}
		if !slices.Equal(got, []string{"button:ack:primary:Ack", "button:resolve::Resolve",
			"static_select:snooze::Snooze…"}) {
			t.Errorf("actions %v", got)
		}
		var options []string
		for _, o := range root.actions()[2].Options {
			options = append(options, o.Text.Text)
		}
		if !slices.Equal(options, []string{"1h", "4h", "24h", "Until tomorrow 09:00", "Until Monday 09:00"}) {
			t.Errorf("options %v", options)
		}
		url := fmt.Sprintf("https://fake-workspace-b.slack.com/archives/%s/p%s", fakeslack.ChannelAlerts,
			strings.ReplaceAll(root.TS, ".", ""))
		if d := s.deliveryOf(g); d["message_url"] != url || d["state"] != "delivered" {
			t.Errorf("delivery %v, want the link %s", d, url)
		}

		// C-24.FR-6: Acknowledge edits the Root message, Quietly; a new Alert is a Quiet Thread reply.
		edits := len(s.requests("chat.update"))
		if out := s.command(g, "acknowledge"); out != "done" {
			t.Fatalf("acknowledge = %s", out)
		}
		eventually(t, "the edit", func() bool { return len(s.requests("chat.update")) > edits })
		var edit map[string]any
		_ = json.Unmarshal([]byte(s.requests("chat.update")[edits].Body), &edit)
		_, hasBlocks := edit["blocks"]
		_, hasAttachments := edit["attachments"]
		if !hasBlocks || !hasAttachments || edit["ts"] != root.TS || strings.Contains(edit["text"].(string), "<") {
			t.Errorf("the edit %v", edit)
		}
		eventually(t, "the acknowledged root message", func() bool {
			m := s.message(root.TS)
			return len(m.Attachments) == 1 && m.Attachments[0].Color == "#f57c00"
		})
		got = nil
		for _, el := range s.message(root.TS).actions() {
			style := ""
			if el.Style != nil {
				style = *el.Style
			}
			got = append(got, el.label()+":"+style)
		}
		if !slices.Equal(got, []string{"Unack:", "Resolve:primary", "Snooze…:"}) {
			t.Errorf("acknowledged actions %v", got)
		}
		replies := len(s.message(root.TS).Replies)
		s.alert("s1", "i2", `{"team":"sl","cluster":"a","pod":"i2"}`, "")
		s.notify("s1", "new alerts added")
		advance(t, r, 61)
		eventually(t, "the thread reply", func() bool { return len(s.message(root.TS).Replies) > replies })
		reply := s.message(root.TS).Replies[replies]
		posts := s.requests("chat.postMessage")
		var sent map[string]any
		_ = json.Unmarshal([]byte(posts[len(posts)-1].Body), &sent)
		_, broadcast := sent["reply_broadcast"]
		if reply.ThreadTS != root.TS || reply.ReplyBroadcast || broadcast || sent["thread_ts"] != root.TS ||
			strings.Contains(reply.Text, "<!") || string(reply.Attachments) != "[]" {
			t.Errorf("reply %+v, sent %v", reply, sent)
		}
	})

	t.Run("request_log", func(t *testing.T) {
		s := s.on(t)
		records := s.requestLog("destination=" + s.dest + "&alert_group=" + g)
		posts, edits := ofOperation(records, "chat.postMessage"), ofOperation(records, "chat.update")
		if len(posts) < 2 || len(edits) < 1 {
			t.Fatalf("records %v", recordIDs(records))
		}
		for _, rec := range append(posts, edits...) {
			d, _ := rec["destination"].(map[string]any)
			ag, _ := rec["alert_group"].(map[string]any)
			if rec["connection_id"] != s.conn || d["id"] != s.dest || ag["id"] != g || rec["status"] != 200.0 {
				t.Errorf("record %v", rec)
			}
			full, text := s.requestRecord(rec["id"].(string))
			headers, _ := full["request_headers"].(map[string]any)
			auth, _ := headers["Authorization"].([]any)
			var sent any
			_ = json.Unmarshal([]byte(full["request_body"].(string)), &sent)
			if strings.Contains(text, fakeslack.SecondBotToken) || len(auth) != 1 || auth[0] != "[redacted]" ||
				len(signedStrings(sent)) != 0 {
				t.Errorf("record %s", text)
			}
		}
		var redacted int
		for _, rec := range posts {
			full, _ := s.requestRecord(rec["id"].(string))
			redacted += strings.Count(full["request_body"].(string), `"value":"[redacted]"`)
		}
		if redacted == 0 {
			t.Error("no signed action id was masked")
		}
	})

	t.Run("deleted_root", func(t *testing.T) {
		s := s.on(t)
		// After a reply: the Root message is a tombstone; the next edit finds it deleted, and the Alert Group is
		// published again once, Quietly, with the note; the Destination stays healthy.
		s.settled()
		s.remove(root.TS)
		if m := s.message(root.TS); m.Subtype != "tombstone" || len(m.Replies) != 1 {
			t.Fatalf("tombstone %+v", m.Message)
		}
		updates, reads := len(s.requests("chat.update")), len(s.requests("conversations.info"))
		s.command(g, "unacknowledge")
		again := s.root(n, "DiskFull", root.TS)
		if !deletedNote.MatchString(again.body(t)) || strings.Contains(again.Text, "<!") ||
			len(s.roots(n, "DiskFull")) != 1 {
			t.Errorf("republished %q: %s", again.Text, again.body(t))
		}
		if u := s.requests("chat.update")[updates:]; len(u) == 0 || !strings.Contains(u[0].Body, root.TS) {
			t.Errorf("the edit of the deleted root %+v", u)
		}
		if len(s.requests("conversations.info")) <= reads {
			t.Error("the channel was not read")
		}
		if hl := s.health(s.dest); hl.State != "healthy" {
			t.Errorf("health %+v", hl)
		}
		if got := count(deliveryEvents(s.timeline(g, "")), "republished"); got != 1 {
			t.Errorf("republished %d times", got)
		}
		root = again

		// Before any reply: the Reminder of an acknowledged Alert Group, a reply alone, lands in the channel and is
		// deleted at once; the Alert Group is published again once and the Reminder goes under the new Root message.
		s.fire("s2", "CpuHigh", "j1", `{"team":"sl","cluster":"b","pod":"j1"}`)
		g2 := s.groupOf("b")
		n2 := s.number(g2)
		first := s.root(n2, "CpuHigh")
		s.command(g2, "acknowledge")
		eventually(t, "the edit of #2", func() bool {
			m := s.message(first.TS)
			return len(m.Attachments) == 1 && m.Attachments[0].Color == "#f57c00"
		})
		s.settled()
		s.remove(first.TS)
		deletes := len(s.requests("chat.delete"))
		advance(t, r, 600)
		eventually(t, "the deletion of the misplaced reply", func() bool { return len(s.requests("chat.delete")) > deletes })
		second := s.root(n2, "CpuHigh", first.TS)
		eventually(t, "the reminder under the new root message", func() bool {
			return len(s.message(second.TS).Replies) > 0
		})
		if !deletedNote.MatchString(second.body(t)) || len(s.roots(n2, "CpuHigh")) != 1 ||
			!strings.HasPrefix(s.message(second.TS).Replies[0].Text, "Reminder") {
			t.Errorf("republished %q: %s", second.Text, second.body(t))
		}
		if got := count(deliveryEvents(s.timeline(g2, "")), "republished"); got != 1 {
			t.Errorf("republished %d times", got)
		}
		deleted := ofOperation(s.requestLog("destination="+s.dest+"&alert_group="+g2), "chat.delete")
		if len(deleted) == 0 || deleted[0]["status"] != 200.0 {
			t.Errorf("the deletion in the request log %v", deleted)
		}
		if hl := s.health(s.dest); hl.State != "healthy" {
			t.Errorf("health %+v", hl)
		}
		s.settled()

		// C-24.AC-8: a 429 with Retry-After: 1 holds the Connection; the edits after it wait 1 to 2 seconds and the
		// 429 is no attempt.
		limited := s.clientRequests("delivery", "retry_after")
		s.fault("chat.update", http.StatusTooManyRequests, "ratelimited", 1)
		start := len(s.requests("chat.update"))
		s.command(g, "acknowledge")
		eventually(t, "the 429", func() bool {
			u := s.requests("chat.update")[start:]
			return len(u) > 0 && u[0].Status == http.StatusTooManyRequests
		})
		s.command(g2, "unacknowledge")
		eventually(t, "the edits after the 429", func() bool { return len(s.requests("chat.update")[start:]) >= 3 })
		u := s.requests("chat.update")[start:]
		for _, x := range u[1:] {
			if gap := x.AtMs - u[0].AtMs; x.Status != http.StatusOK || gap < 1000 || gap >= 2000 {
				t.Errorf("an edit %d ms after the 429, answered %d", gap, x.Status)
			}
		}
		if got := s.clientRequests("delivery", "retry_after"); got != limited+1 {
			t.Errorf("retry_after outcomes %v, want %v", got, limited+1)
		}
		s.settled()
		for _, id := range []string{g, g2} {
			if d := s.deliveryOf(id); d["state"] != "delivered" {
				t.Errorf("delivery of %s %v", id, d)
			}
		}
	})

	t.Run("error_classes", func(t *testing.T) {
		s := s.on(t)
		// C-24.AC-8: blocks rejected on a Publication: the message is sent again as text alone.
		s.settled()
		s.fault("chat.postMessage", http.StatusOK, "invalid_blocks", 0)
		s.fire("s3", "MemHigh", "k1", `{"team":"sl","cluster":"c","pod":"k1"}`)
		g3 := s.groupOf("c")
		n3 := s.number(g3)
		plain := s.root(n3, "MemHigh")
		if len(plain.Top) != 0 || len(plain.Attachments) != 0 || plain.Text == "" || len(plain.Text) > 3000 ||
			!strings.HasPrefix(plain.Text, fmt.Sprintf("🔴 #%d MemHigh\n<!here>", n3)) {
			t.Errorf("plain message %+v", plain.Message)
		}
		eventually(t, "markup_rejected", func() bool {
			return count(deliveryEvents(s.timeline(g3, "")), "markup_rejected") > 0
		})
		if got := count(deliveryEvents(s.timeline(g3, "")), "markup_rejected"); got != 1 {
			t.Errorf("markup_rejected %d times", got)
		}
		// edit_window_closed on an edit: Not delivered, and the Destination stays healthy.
		s.settled()
		s.fault("chat.update", http.StatusOK, "edit_window_closed", 0)
		s.command(g3, "acknowledge")
		eventually(t, "not delivered", func() bool { return s.deliveryOf(g3)["state"] == "not_delivered" })
		if hl := s.health(s.dest); hl.State != "healthy" {
			t.Errorf("health %+v", hl)
		}
	})

	t.Run("test_message", func(t *testing.T) {
		s := s.on(t)
		s.settled()
		entries := len(s.timeline(g, ""))
		res := s.api.json(http.MethodPost, "/api/v1/destinations/"+s.dest+"/tests", `{"source":`+example+`}`,
			http.StatusOK)
		raw, _ := json.Marshal(res)
		var tested destTestResult
		_ = json.Unmarshal(raw, &tested)
		if len(tested.Steps) != 1 || tested.Steps[0].Name != "message" || tested.Steps[0].ErrorClass != "none" ||
			!strings.HasSuffix(tested.Steps[0].Request.URL, "/api/chat.postMessage") ||
			header(tested.Steps[0], "Authorization") != "Bearer [redacted]" ||
			strings.Contains(string(raw), fakeslack.SecondBotToken) {
			t.Errorf("test %s", raw)
		}
		var test slackMessage
		eventually(t, "the test message", func() bool {
			for _, m := range s.messages() {
				if strings.Contains(m.Text, "Test message") {
					test = m
				}
			}
			return test.TS != ""
		})
		if !strings.HasPrefix(test.Text, "🧪 Test message · ") || len(test.Top) == 0 || len(test.actions()) == 0 ||
			len(test.Replies) != 0 {
			t.Errorf("test message %+v", test.Message)
		}
		if got := len(s.timeline(g, "")); got != entries {
			t.Errorf("the test changed a timeline: %d entries, want %d", got, entries)
		}
		run := runOf(t, res)
		if recs := s.requestLog("run=" + run); len(recs) != 1 || recs[0]["operation"] != "chat.postMessage" {
			t.Errorf("the records of the test %v", recs)
		}
		before := len(s.messages())
		a := s.api.do(http.MethodPost, "/api/v1/destinations/"+s.dest+"/previews", `{"source":`+example+`}`)
		var p struct {
			Items []struct {
				Name    string `json:"name"`
				Format  string `json:"format"`
				Text    string `json:"text"`
				Request struct {
					URL  string `json:"url"`
					Body string `json:"body"`
				} `json:"request"`
			} `json:"items"`
		}
		decode(t, a, &p)
		var shown map[string]any
		if a.status != http.StatusOK || len(p.Items) != 1 || p.Items[0].Name != "message" || p.Items[0].Format != "json" ||
			json.Unmarshal([]byte(p.Items[0].Text), &shown) != nil || shown["channel"] != fakeslack.ChannelAlerts ||
			shown["blocks"] == nil || shown["attachments"] == nil ||
			!strings.HasSuffix(p.Items[0].Request.URL, "/api/chat.postMessage") ||
			strings.Contains(string(a.body), fakeslack.SecondBotToken) {
			t.Errorf("preview = %d %s", a.status, a.body)
		}
		if got := len(s.messages()); got != before {
			t.Errorf("the preview posted: %d messages, want %d", got, before)
		}
	})

	t.Run("broken_and_probe", func(t *testing.T) {
		s := s.on(t)
		s.settled()
		s.member(false)
		s.fire("s4", "NetDown", "l1", `{"team":"sl","cluster":"d","pod":"l1"}`)
		if hl := s.waitHealth(s.dest, "broken"); hl.Reason == nil || !strings.Contains(*hl.Reason, "not_in_channel") {
			t.Errorf("broken %+v", hl)
		}
		eventually(t, "MusterDestinationBroken", func() bool {
			return slices.Contains(s.internalAlerts(), "MusterDestinationBroken")
		})
		g4 := s.groupOf("d")
		s.command(g4, "resolve")
		s.member(true)
		ok := s.clientRequests("delivery", "ok")
		advance(t, r, 360)
		s.waitHealth(s.dest, "healthy")
		if got := s.clientRequests("delivery", "ok"); got-ok < 1 {
			t.Errorf("the probe made %v requests in the delivery class", got-ok)
		}
		eventually(t, "MusterDestinationBroken to resolve", func() bool {
			return !slices.Contains(s.internalAlerts(), "MusterDestinationBroken")
		})
		if got := s.roots(s.number(g4), "NetDown"); len(got) != 0 {
			t.Errorf("the alert group resolved while broken was published: %+v", got)
		}
		if d := s.deliveryOf(g4); d["state"] != "withheld" {
			t.Errorf("delivery of the resolved alert group %v", d)
		}
	})
}

// optionValues are the values of the options of a menu.
func optionValues(el slackElement) []string {
	out := make([]string, len(el.Options))
	for i, o := range el.Options {
		out[i] = o.Value
	}
	return out
}
