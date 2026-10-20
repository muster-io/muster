// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	buttonids "github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
	"github.com/muster-io/muster/internal/slack"
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

// The answers of C-18 and C-24.FR-13 that the presses below meet, as the fake shows them.
const (
	slackForbidden   = "You are not permitted to do this"
	slackNotVerified = "This button could not be verified; nothing was changed."
	slackTest        = "This is a test message; nothing was changed"
	slackDisabledRU  = "Ваша учётная запись Muster отключена"
	slackStillOnIt   = "Done: Still on it"
)

// slackNotLinked is the answer to an account without an Account link.
var slackNotLinked = "Your Slack account is not linked to Muster. Link it in your profile: " + devmode.PublicURL + "/profile"

// httpMode switches the Connection to the http event mode with the Slack signing secret of the fake's second app, and
// points the app's request_url at its callback.
func (s *slk) httpMode() {
	s.t.Helper()
	etag := s.api.do(http.MethodGet, "/api/v1/connections/"+s.conn, "").header.Get("ETag")
	conn := s.api.json(http.MethodPut, "/api/v1/connections/"+s.conn, `{"type":"slack","name":"sl","event_mode":"http",
		"slack_signing_secret":"`+fakeslack.SecondSigningSecret+`","proxy":{"enabled":false},
		"limiter":{"limit":50,"per_seconds":1}}`, http.StatusOK, "If-Match", etag)
	if conn["event_mode"] != "http" || conn["callback_url"] != devmode.IngestURL+slack.CallbackPath+s.conn {
		s.t.Fatalf("the connection in http mode %v", conn)
	}
	s.requestURL(s.conn)
}

// requestURL points the request_url of the fake's second app at the callback of the Connection conn.
func (s *slk) requestURL(conn string) {
	s.t.Helper()
	s.fake(http.MethodPut, s.fs+"/config", `{"request_url":{"b":"`+s.r.Ingest+slack.CallbackPath+conn+`"}}`)
}

// press presses action on the message ts of the channel alerts of the fake's second app as user, with the extra
// fields of /_fake/press, and returns Muster's answer.
func (s *slk) press(ts, action, user string, extra map[string]any) fakeslack.PressResult {
	s.t.Helper()
	body := map[string]any{"channel": fakeslack.ChannelAlerts, "ts": ts, "action": action, "user": user}
	maps.Copy(body, extra)
	b, _ := json.Marshal(body)
	a := call(s.t, http.MethodPost, s.fs+"/press", string(b))
	if a.status != http.StatusOK {
		s.t.Fatalf("press %s = %d %s", b, a.status, a.body)
	}
	var res fakeslack.PressResult
	decode(s.t, a, &res)
	return res
}

// pressed presses as press does and fails unless Muster confirmed the press at once.
func (s *slk) pressed(ts, action, user string, extra map[string]any) {
	s.t.Helper()
	if res := s.press(ts, action, user, extra); res.Status != http.StatusOK || res.Body != "" {
		s.t.Fatalf("press %s on %s by %s = %+v", action, ts, user, res)
	}
}

func (s *slk) ephemerals() []fakeslack.Ephemeral {
	s.t.Helper()
	var out []fakeslack.Ephemeral
	decode(s.t, call(s.t, http.MethodGet, s.fs+"/ephemeral", ""), &out)
	return out
}

func (s *slk) responses() []fakeslack.Response {
	s.t.Helper()
	var out []fakeslack.Response
	decode(s.t, call(s.t, http.MethodGet, s.fs+"/responses", ""), &out)
	return out
}

// answered waits until the fake showed n ephemeral messages after the first before, and returns them, by user and
// then text.
func (s *slk) answered(before, n int) []fakeslack.Ephemeral {
	s.t.Helper()
	var got []fakeslack.Ephemeral
	eventually(s.t, fmt.Sprintf("%d ephemeral answers", n), func() bool {
		got = s.ephemerals()
		return len(got) >= before+n
	})
	got = got[before:]
	slices.SortFunc(got, func(a, b fakeslack.Ephemeral) int {
		return strings.Compare(a.User+a.Text, b.User+b.Text)
	})
	return got
}

// pressLines waits until n slack_press lines have the Alert Group group — empty for a press bound to none — and the
// outcome, and returns them.
func (s *slk) pressLines(group, outcome string, n int) []map[string]any {
	s.t.Helper()
	var got []map[string]any
	eventually(s.t, fmt.Sprintf("%d slack_press lines %s", n, outcome), func() bool {
		got = nil
		for _, l := range logLines(s.r, "slack_press") {
			if g, _ := l["group"].(string); g == group && l["outcome"] == outcome {
				got = append(got, l)
			}
		}
		return len(got) >= n
	})
	return got
}

// pressLine waits for the first slack_press line of the Alert Group group with the outcome.
func (s *slk) pressLine(group, outcome string) map[string]any {
	s.t.Helper()
	return s.pressLines(group, outcome, 1)[0]
}

// rejected are the reasons of the slack_press_rejected lines of the Connection conn, "" for an unknown one.
func (s *slk) rejected(conn string) []string {
	var out []string
	for _, l := range logLines(s.r, "slack_press_rejected") {
		if l["connection"] == conn {
			out = append(out, fmt.Sprint(l["reason"]))
		}
	}
	return out
}

// audit is the newest Audit log entry of action.
func (s *slk) audit(action string) map[string]any {
	s.t.Helper()
	items := s.api.json(http.MethodGet, "/api/v1/audit-log?limit=1&action="+action, "", http.StatusOK)["items"].([]any)
	if len(items) == 0 {
		s.t.Fatalf("no %s entry", action)
	}
	return items[0].(map[string]any)
}

// replyActions are the action ids and values of the actions block of a Thread reply.
func replyActions(r fakeslack.Reply) map[string]string {
	var blocks []slackBlock
	_ = json.Unmarshal(r.Blocks, &blocks)
	out := map[string]string{}
	for _, b := range blocks {
		for _, el := range b.Elements {
			if b.Type == "actions" {
				out[el.ActionID] = el.Value
			}
		}
	}
	return out
}

// buttonValues are the signed action ids of every button and option of the channel's messages and their replies.
func (s *slk) buttonValues() []string {
	s.t.Helper()
	var out []string
	for _, m := range s.messages() {
		for _, el := range m.actions() {
			out = append(out, el.Value)
			out = append(out, optionValues(el)...)
		}
		for _, r := range m.Replies {
			out = append(out, slices.Collect(maps.Values(replyActions(r)))...)
		}
	}
	return slices.DeleteFunc(out, func(v string) bool { return v == "" })
}

// actionIDs are the action ids of the Root message ts.
func (s *slk) actionIDs(ts string) []string {
	var out []string
	for _, el := range s.message(ts).actions() {
		out = append(out, el.ActionID)
	}
	return out
}

// waitActions waits until the Root message ts offers the action, after its edit.
func (s *slk) waitActions(ts, action string) {
	s.t.Helper()
	eventually(s.t, "the action "+action+" on "+ts, func() bool { return slices.Contains(s.actionIDs(ts), action) })
}

// changes counts the Timeline entries of the Alert Group id that a Command could make: all but the delivery events.
func (s *slk) changes(id string) int {
	s.t.Helper()
	n := 0
	for _, x := range s.timeline(id, "") {
		if x.Kind != "delivery" {
			n++
		}
	}
	return n
}

// presetEnd is the end of the Snooze preset tomorrow from now in loc, the working day starting at 09:00 (C-10.FR-17).
func presetEnd(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.In(loc).Date()
	for day := d; ; day++ {
		if end := time.Date(y, m, day, 9, 0, 0, 0, loc); end.After(now) {
			return end
		}
	}
}

// slackLeak finds what no record of the Request log may hold: a response_url's path in any form, a Slack signature,
// the fake's Slack signing secret and verification token of either app.
var slackLeak = regexp.MustCompile(`actions%2F|/actions/|actions\\/|v0=[0-9a-f]{64}|muster-dev-slack-signing-secret|` +
	`muster-dev-slack-verification-token`)

// TestSlackPresses is the Verification of S-100 against `muster dev` and the fake Slack server, with E2E_REPLICAS=2 on
// two replicas: the Connection "sl" in http mode, presses of the fake's people sent to its callback signed with the
// app's Slack signing secret. A press of Ack by an account linked to a Responder runs as that User with the Transport
// slack and is not answered (C-24.FR-9, FR-10, AC-11, AC-12); a wrong signature, a timestamp 6 minutes old or ahead, an
// unknown and a deleted Connection and one in socket mode are refused alike, a body without a payload is 400
// (C-24.AC-11); the Snooze menu's 4h and tomorrow (C-24.AC-12); a Viewer, an account without a link, a disabled User in
// Russian, another message's value and a tampered value are answered privately (C-24.FR-11, AC-12, AC-13, C-18.FR-8);
// a refused chat.postEphemeral falls back to the response_url (C-24.AC-13); the Reminder's buttons (C-24.FR-5, FR-10);
// a test message (C-24.AC-16); and the Request log, masked (C-24.FR-14, AC-17).
func TestSlackPresses(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	s := newSlack(t, h, r)
	t.Cleanup(func() {
		restore(t, http.MethodDelete, h.Fakes.Slack+"/_fake/faults", "")
		restore(t, http.MethodPut, h.Fakes.Slack+"/_fake/config", `{"request_url":{"b":null}}`)
	})
	s.httpMode()
	ids := map[string]string{}
	for _, u := range []struct{ login, role, external, link string }{
		{"alice", "responder", fakeslack.UserAlice, "AK0000000000S1"},
		{"vic", "viewer", fakeslack.UserBob, "AK0000000000S2"},
		{"dora", "responder", fakeslack.UserDora, "AK0000000000S4"},
	} {
		ids[u.login] = s.api.json(http.MethodPost, "/api/v1/users", `{"name":"`+u.login+`","login":"`+u.login+
			`","role":"`+u.role+`"}`, http.StatusCreated)["user"].(map[string]any)["id"].(string)
		h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
			username, display_name, created_at) SELECT 1, '`+u.link+`', u.id, 'slack', c.id, '`+u.external+`', '`+
			u.login+`', u.name, now() FROM users u, connections c WHERE u.login = '`+u.login+`' AND c.public_id = '`+
			s.conn+`'`)
	}
	h.exec(t, `UPDATE users SET time_zone = 'Asia/Tokyo' WHERE login = 'alice'`)
	h.exec(t, `UPDATE users SET language = 'ru' WHERE login = 'dora'`)
	var g, g2 string
	var root, root2 slackMessage

	t.Run("http_press", func(t *testing.T) {
		s := s.on(t)
		s.fire("p1", "DiskFull", "q1", `{"team":"sl","cluster":"a","pod":"q1"}`)
		g = s.groupOf("a")
		root = s.root(s.number(g), "DiskFull")
		before := len(s.ephemerals())
		s.pressed(root.TS, "ack", fakeslack.UserAlice, nil)
		eventually(t, "the acknowledgement by alice", func() bool { return s.alertGroup(g)["status"] == "acknowledged" })
		if owner, _ := s.alertGroup(g)["owner"].(map[string]any); owner["login"] != "alice" {
			t.Errorf("owner %v", owner)
		}
		var acked []timelineEntry
		for _, x := range s.timeline(g, "") {
			if x.Kind == "status" && x.Event == "acknowledged" {
				acked = append(acked, x)
			}
		}
		if len(acked) != 1 || acked[0].Actor.Transport != "slack" || acked[0].Actor.Name != "alice" {
			t.Errorf("acknowledged %+v", acked)
		}
		if a := s.audit("alert_group.acknowledged"); a["transport"] != "slack" ||
			a["actor"].(map[string]any)["name"] != "alice" {
			t.Errorf("audit %v", a)
		}
		if got := r.Metric(t, `muster_commands_total{command="acknowledge",transport="slack",outcome="done"}`); got !=
			"1" {
			t.Errorf("muster_commands_total = %q", got)
		}
		if l := s.pressLine(g, "done"); l["connection"] != s.conn || l["command"] != "acknowledge" ||
			l["answer"] != nil || l["error"] != nil {
			t.Errorf("slack_press %v", l)
		}
		if got := s.ephemerals(); len(got) != before {
			t.Errorf("a successful press was answered: %+v", got[before:])
		}
		s.waitActions(root.TS, "unack")
	})

	t.Run("refused_requests", func(t *testing.T) {
		s := s.on(t)
		entries := s.changes(g)
		var bodies []string
		refuse := func(extra map[string]any) {
			t.Helper()
			res := s.press(root.TS, "resolve", fakeslack.UserAlice, extra)
			if res.Status != http.StatusUnauthorized {
				t.Errorf("%v = %+v", extra, res)
			}
			bodies = append(bodies, res.Body)
		}
		refuse(map[string]any{"signature": "wrong"})
		refuse(map[string]any{"timestamp_offset_seconds": -360})
		refuse(map[string]any{"timestamp_offset_seconds": 360})
		s.requestURL("CN0000000000ZZ")
		refuse(nil)
		gone := s.api.json(http.MethodPost, "/api/v1/connections", `{"type":"slack","name":"gone","event_mode":"http",
			"bot_token":"`+fakeslack.SecondBotToken+`","slack_signing_secret":"`+fakeslack.SecondSigningSecret+`",
			"proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
		s.api.json(http.MethodDelete, "/api/v1/connections/"+gone, "", http.StatusNoContent)
		s.requestURL(gone)
		refuse(nil)
		dev := s.connectionNamed(devmode.SlackConnectionName)
		s.requestURL(dev)
		refuse(nil)
		s.requestURL(s.conn)
		for _, b := range bodies[1:] {
			if b != bodies[0] || !strings.Contains(b, `"status":401`) {
				t.Errorf("refusals answered %q and %q", bodies[0], b)
			}
		}
		body := "foo=bar"
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		if a := call(t, http.MethodPost, r.Ingest+slack.CallbackPath+s.conn, body, "Content-Type",
			"application/x-www-form-urlencoded", slack.HeaderTimestamp, ts, slack.HeaderSignature,
			fakeslack.Sign(fakeslack.SecondSigningSecret, ts, []byte(body))); a.status != http.StatusBadRequest {
			t.Errorf("no payload = %d %s", a.status, a.body)
		}
		eventually(t, "the refusals in the log", func() bool {
			return len(s.rejected(s.conn)) >= 4 && len(s.rejected("")) >= 2 && len(s.rejected(dev)) >= 1
		})
		mine := s.rejected(s.conn)
		slices.Sort(mine)
		if !slices.Equal(mine, []string{"bad_signature", "no_payload", "stale", "stale"}) ||
			!slices.Equal(s.rejected(""), []string{"unknown_connection", "unknown_connection"}) ||
			!slices.Equal(s.rejected(dev), []string{"wrong_mode"}) {
			t.Errorf("slack_press_rejected %v, %v, %v", mine, s.rejected(""), s.rejected(dev))
		}
		if st := s.alertGroup(g)["status"]; st != "acknowledged" || s.changes(g) != entries {
			t.Errorf("a refused press changed %s: %v", g, st)
		}
	})

	t.Run("menu_and_presets", func(t *testing.T) {
		s := s.on(t)
		before := readClock(t, r).Now
		s.pressed(root.TS, "snooze", fakeslack.UserAlice, map[string]any{"option": 1})
		eventually(t, "the snooze for 4h", func() bool { return s.alertGroup(g)["status"] == "snoozed" })
		after := readClock(t, r).Now
		until, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(s.alertGroup(g)["snooze_until"]))
		if until.Before(before.Add(4*time.Hour-time.Second)) || until.After(after.Add(4*time.Hour+time.Second)) {
			t.Errorf("snoozed until %v, want 4 hours after %v", until, before)
		}
		if a := s.audit("alert_group.snoozed"); a["transport"] != "slack" {
			t.Errorf("audit %v", a)
		}
		s.waitActions(root.TS, "unsnooze")
		s.pressed(root.TS, "unsnooze", fakeslack.UserAlice, nil)
		eventually(t, "the unsnooze", func() bool { return s.alertGroup(g)["status"] != "snoozed" })
		s.waitActions(root.TS, "snooze")
		tokyo, err := time.LoadLocation("Asia/Tokyo")
		if err != nil {
			t.Fatal(err)
		}
		before = readClock(t, r).Now
		s.pressed(root.TS, "snooze", fakeslack.UserAlice, map[string]any{"option": 3})
		eventually(t, "the snooze until tomorrow", func() bool { return s.alertGroup(g)["status"] == "snoozed" })
		after = readClock(t, r).Now
		until, _ = time.Parse(time.RFC3339Nano, fmt.Sprint(s.alertGroup(g)["snooze_until"]))
		if want := presetEnd(before, tokyo); !until.Equal(want) && !until.Equal(presetEnd(after, tokyo)) {
			t.Errorf("snoozed until %v, want %v", until, want)
		}
		a := s.audit("alert_group.snoozed")
		if d, _ := a["details"].(map[string]any); a["transport"] != "slack" || d["preset"] != "tomorrow" ||
			d["time_zone"] != "Asia/Tokyo" {
			t.Errorf("audit %v", a)
		}
		s.command(g, "unsnooze")
	})

	t.Run("answers", func(t *testing.T) {
		s := s.on(t)
		s.fire("p2", "CpuHigh", "q2", `{"team":"sl","cluster":"b","pod":"q2"}`)
		g2 = s.groupOf("b")
		root2 = s.root(s.number(g2), "CpuHigh")
		s.api.json(http.MethodPost, "/api/v1/users/"+ids["dora"]+"/disable", "", http.StatusOK)
		entries, entries2, status := s.changes(g), s.changes(g2), s.alertGroup(g)["status"]
		var resolve string
		for _, el := range root2.actions() {
			if el.ActionID == "resolve" {
				resolve = el.Value
			}
		}
		tampered := resolve[:len(resolve)-4] + "AAAA"
		if tampered == resolve {
			tampered = resolve[:len(resolve)-4] + "BBBB"
		}
		before := len(s.ephemerals())
		s.pressed(root2.TS, "resolve", fakeslack.UserBob, nil)
		s.pressed(root2.TS, "resolve", fakeslack.UserCarol, nil)
		s.pressed(root2.TS, "resolve", fakeslack.UserDora, nil)
		s.pressed(root2.TS, "resolve", fakeslack.UserAlice, map[string]any{"value_of_ts": root.TS})
		s.pressed(root2.TS, "resolve", fakeslack.UserAlice, map[string]any{"value": tampered})
		got := s.answered(before, 5)
		want := []string{fakeslack.UserAlice + " " + slackNotVerified, fakeslack.UserAlice + " " + slackNotVerified,
			fakeslack.UserBob + " " + slackForbidden, fakeslack.UserCarol + " " + slackNotLinked,
			fakeslack.UserDora + " " + slackDisabledRU}
		for i, e := range got {
			if e.User+" "+e.Text != want[i] || e.Channel != fakeslack.ChannelAlerts || e.ThreadTS != nil || e.App != "b" {
				t.Errorf("answer %d %+v, want %s", i, e, want[i])
			}
		}
		for _, o := range []string{"forbidden", "not_linked", "disabled"} {
			if l := s.pressLine(g2, o); l["answer"] != "ephemeral" || l["command"] != "resolve" {
				t.Errorf("slack_press %v", l)
			}
		}
		for _, l := range s.pressLines("", "not_verified", 2) {
			if l["answer"] != "ephemeral" || l["connection"] != s.conn {
				t.Errorf("slack_press %v", l)
			}
		}
		if st := s.alertGroup(g2)["status"]; st != "firing" || s.alertGroup(g)["status"] != status ||
			s.changes(g2) != entries2 || s.changes(g) != entries {
			t.Errorf("a refused press changed %s: %v, %d entries of %d, %d of %d", g2, st, s.changes(g2), entries2,
				s.changes(g), entries)
		}
	})

	t.Run("response_url_fallback", func(t *testing.T) {
		s := s.on(t)
		s.fault("chat.postEphemeral", http.StatusOK, "user_not_in_channel", 0)
		before, answers := len(s.responses()), len(s.ephemerals())
		s.pressed(root2.TS, "resolve", fakeslack.UserCarol, nil)
		var got []fakeslack.Response
		eventually(t, "the answer to the response_url", func() bool {
			got = s.responses()
			return len(got) > before
		})
		last := got[len(got)-1]
		if last.ResponseType == nil || *last.ResponseType != "ephemeral" || last.ReplaceOriginal == nil ||
			*last.ReplaceOriginal || last.Text != slackNotLinked || last.User != fakeslack.UserCarol ||
			last.Channel != fakeslack.ChannelAlerts {
			t.Errorf("response_url answer %+v", last)
		}
		if l := s.pressLines(g2, "not_linked", 2)[1]; l["answer"] != "response_url" ||
			!strings.Contains(fmt.Sprint(l["error"]), "user_not_in_channel") {
			t.Errorf("slack_press %v", l)
		}
		if n := len(s.ephemerals()); n != answers {
			t.Errorf("%d ephemeral answers, want %d", n, answers)
		}
		for _, req := range fakeRequests(t, h.Fakes.Slack) {
			if strings.Contains(req.Body, `"replace_original":true`) || strings.Contains(req.Body, "delete_original") {
				t.Errorf("a request replaces the message: %s %s", req.Path, req.Body)
			}
		}
	})

	t.Run("reminder_buttons", func(t *testing.T) {
		s := s.on(t)
		s.fire("p3", "MemHigh", "q3", `{"team":"sl","cluster":"c","pod":"q3"}`)
		g3 := s.groupOf("c")
		root3 := s.root(s.number(g3), "MemHigh")
		s.pressed(root3.TS, "ack", fakeslack.UserAlice, nil)
		eventually(t, "the acknowledgement of #3", func() bool { return s.alertGroup(g3)["status"] == "acknowledged" })
		s.settled()
		advance(t, r, 600)
		var reminder fakeslack.Reply
		eventually(t, "the reminder", func() bool {
			for _, rp := range s.message(root3.TS).Replies {
				if a := replyActions(rp); a["still_on_it"] != "" && a["unack"] != "" {
					reminder = rp
					return true
				}
			}
			return false
		})
		before := len(s.ephemerals())
		s.pressed(reminder.TS, "still_on_it", fakeslack.UserBob, nil)
		if got := s.answered(before, 1); got[0].Text != slackForbidden || got[0].ThreadTS == nil ||
			*got[0].ThreadTS != root3.TS || got[0].User != fakeslack.UserBob {
			t.Errorf("vic's answer %+v", got[0])
		}
		s.pressed(reminder.TS, "still_on_it", fakeslack.UserAlice, nil)
		if got := s.answered(before+1, 1); got[0].Text != slackStillOnIt || got[0].ThreadTS == nil ||
			*got[0].ThreadTS != root3.TS || got[0].User != fakeslack.UserAlice {
			t.Errorf("alice's answer %+v", got[0])
		}
		if x := s.timeline(g3, "&kind=timers"); len(x) == 0 || x[0].Event != "reminder_answered" ||
			x[0].Actor.Transport != "slack" || x[0].Actor.Name != "alice" {
			t.Errorf("the answer of the Reminder %+v", x)
		}
		s.pressed(reminder.TS, "unack", fakeslack.UserAlice, nil)
		eventually(t, "the unacknowledgement", func() bool { return s.alertGroup(g3)["status"] == "firing" })
		var unack map[string]any
		eventually(t, "slack_press of the unack", func() bool {
			for _, l := range logLines(s.r, "slack_press") {
				if l["group"] == g3 && l["command"] == "unacknowledge" {
					unack = l
				}
			}
			return unack != nil
		})
		if unack["outcome"] != "done" || unack["answer"] != nil {
			t.Errorf("slack_press %v", unack)
		}
	})

	t.Run("test_message", func(t *testing.T) {
		s := s.on(t)
		s.settled()
		entries := s.changes(g)
		s.api.json(http.MethodPost, "/api/v1/destinations/"+s.dest+"/tests", `{"source":`+example+`}`, http.StatusOK)
		var test slackMessage
		eventually(t, "the test message", func() bool {
			for _, m := range s.messages() {
				if strings.Contains(m.Text, "Test message") {
					test = m
				}
			}
			return test.TS != ""
		})
		before := len(s.ephemerals())
		s.pressed(test.TS, "ack", fakeslack.UserAlice, nil)
		if got := s.answered(before, 1); got[0].Text != slackTest || got[0].ThreadTS != nil {
			t.Errorf("the answer %+v", got[0])
		}
		if l := s.pressLine("", "test"); l["command"] != "acknowledge" || l["answer"] != "ephemeral" {
			t.Errorf("slack_press %v", l)
		}
		if got := s.changes(g); got != entries {
			t.Errorf("the press changed a timeline: %d entries, want %d", got, entries)
		}
	})

	t.Run("request_log", func(t *testing.T) {
		s := s.on(t)
		records := s.requestLog("connection=" + s.conn)
		inbound := ofOperation(records, "interactive")
		statuses := map[float64]int{}
		for _, rec := range inbound {
			statuses[rec["status"].(float64)]++
			full, _ := s.requestRecord(rec["id"].(string))
			headers, _ := full["request_headers"].(map[string]any)
			sig, _ := headers["X-Slack-Signature"].([]any)
			form, err := url.ParseQuery(full["request_body"].(string))
			var p map[string]any
			if rec["direction"] != "inbound" || rec["connection_id"] != s.conn || rec["destination"] != nil ||
				rec["alert_group"] != nil || len(sig) != 1 || sig[0] != "[redacted]" {
				t.Errorf("inbound record %v, X-Slack-Signature %v", rec, sig)
			}
			if rec["status"] != 400.0 && (err != nil || json.Unmarshal([]byte(form.Get("payload")), &p) != nil ||
				p["token"] != "[redacted]" || p["response_url"] != "[redacted]" || p["type"] != "block_actions") {
				t.Errorf("inbound body %v", full["request_body"])
			}
		}
		if statuses[200] < 10 || statuses[401] != 3 || statuses[400] != 1 {
			t.Errorf("inbound statuses %v", statuses)
		}
		linked := func(op, group string) []map[string]any {
			var out []map[string]any
			for _, rec := range ofOperation(records, op) {
				d, _ := rec["destination"].(map[string]any)
				ag, _ := rec["alert_group"].(map[string]any)
				if d != nil && d["id"] == s.dest && ag != nil && ag["id"] == group {
					out = append(out, rec)
				}
			}
			return out
		}
		if got := linked("chat.postEphemeral", g2); len(got) < 5 {
			t.Errorf("%d chat.postEphemeral records of %s", len(got), g2)
		}
		answers := linked("response_url", g2)
		if len(answers) != 1 || answers[0]["status"] != 200.0 || strings.Contains(fmt.Sprint(answers[0]["url"]),
			"/actions/") || !strings.HasSuffix(fmt.Sprint(answers[0]["url"]), "[redacted]") {
			t.Errorf("response_url records %v", answers)
		}
		values := s.buttonValues()
		if len(values) == 0 {
			t.Fatal("no button values")
		}
		for _, rec := range records {
			_, text := s.requestRecord(rec["id"].(string))
			if m := slackLeak.FindString(text); m != "" {
				t.Errorf("record %s %s holds %q", rec["id"], rec["operation"], m)
			}
			for _, v := range values {
				if strings.Contains(text, v) {
					t.Errorf("record %s %s holds a signed action id", rec["id"], rec["operation"])
				}
			}
		}
	})
}

// socketWait bounds the waits on a Socket Mode connection: a hello, a log line, a reconnection after the first back-off
// of slack.socket_backoff, which starts at 1 s, and a socket that closes; socketPoll is how often they look.
const (
	socketWait = 15 * time.Second
	socketPoll = 50 * time.Millisecond
)

// socketDirect are the members of the Connection sl in socket mode without a proxy.
const socketDirect = `"event_mode":"socket","proxy":{"enabled":false}`

// socketLeak finds what no record of the Request log and no log line may hold: an app-level token of the fake and the
// ticket of a Socket Mode address.
var socketLeak = regexp.MustCompile(`xapp-muster-dev-slack-token|ticket=[^\[]`)

// within polls cond every socketPoll until it holds and fails t after d.
func within(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("waited %v for %s", d, what)
		}
		time.Sleep(socketPoll)
	}
}

// leading waits for the replica that leads and returns it.
func leading(t *testing.T, h *Harness) *Replica {
	t.Helper()
	var got *Replica
	eventually(t, "a replica to lead", func() bool {
		for _, r := range h.Replicas {
			if r.Running() && r.Metric(t, "muster_leader") == "1" {
				got = r
				return true
			}
		}
		return false
	})
	return got
}

// connLines are the log lines of event for the Connection conn in the output of r's current run.
func connLines(r *Replica, conn, event string) []map[string]any {
	var out []map[string]any
	for _, l := range logLines(r, event) {
		if l["connection"] == conn {
			out = append(out, l)
		}
	}
	return out
}

// socketLines are the log lines of event for the Connection conn of every replica.
func (s *slk) socketLines(conn, event string) []map[string]any {
	var out []map[string]any
	for _, r := range s.h.Replicas {
		out = append(out, connLines(r, conn, event)...)
	}
	return out
}

// waitSocketLines waits until the replicas logged more than after lines of event for the Connection conn, and returns
// those after the first after.
func (s *slk) waitSocketLines(conn, event string, after int) []map[string]any {
	s.t.Helper()
	var got []map[string]any
	within(s.t, fmt.Sprintf("a log line %s of %s", event, conn), socketWait, func() bool {
		got = s.socketLines(conn, event)
		return len(got) > after
	})
	return got[after:]
}

// socket is the Socket Mode state of the fake Slack server.
func (s *slk) socket() fakeslack.SocketState {
	s.t.Helper()
	var st fakeslack.SocketState
	decode(s.t, call(s.t, http.MethodGet, s.fs+"/socket", ""), &st)
	return st
}

// appB is the Socket Mode state of the fake's second app.
func (s *slk) appB() fakeslack.AppSockets {
	s.t.Helper()
	return s.socket().Apps[fakeslack.AppB]
}

// script carries out the socket script body, a fakeslack.SocketScript, and returns the state it answers.
func (s *slk) script(body string) fakeslack.SocketState {
	s.t.Helper()
	a := call(s.t, http.MethodPost, s.fs+"/socket", body)
	if a.status != http.StatusOK {
		s.t.Fatalf("socket script %s = %d %s", body, a.status, a.body)
	}
	var st fakeslack.SocketState
	decode(s.t, a, &st)
	return st
}

// pressedOverSocket presses action on the message ts as alice through the socket of the fake's second app and fails
// unless Muster confirmed the envelope.
func (s *slk) pressedOverSocket(ts, action string) fakeslack.PressResult {
	s.t.Helper()
	res := s.press(ts, action, fakeslack.UserAlice, map[string]any{"transport": fakeslack.TransportSocket})
	if res.Transport != fakeslack.TransportSocket || res.Confirmed == nil || !*res.Confirmed || res.ConfirmMS == nil {
		s.t.Fatalf("press %s on %s over the socket = %+v", action, ts, res)
	}
	return res
}

// save saves the Connection sl with fields, the members of its event mode and proxy, and returns it.
func (s *slk) save(fields string) map[string]any {
	s.t.Helper()
	etag := s.api.do(http.MethodGet, "/api/v1/connections/"+s.conn, "").header.Get("ETag")
	return s.api.json(http.MethodPut, "/api/v1/connections/"+s.conn, `{"type":"slack","name":"sl",`+fields+
		`,"limiter":{"limit":50,"per_seconds":1}}`, http.StatusOK, "If-Match", etag)
}

// otherConnections is socket.other_connections of the Connection sl, -1 while its socket is null.
func (s *slk) otherConnections() float64 {
	s.t.Helper()
	sock, _ := s.api.json(http.MethodGet, "/api/v1/connections/"+s.conn, "", http.StatusOK)["socket"].(map[string]any)
	if sock == nil {
		return -1
	}
	n, _ := sock["other_connections"].(float64)
	return n
}

// check runs the check of the Connection sl and returns whether it passed, its warnings and its steps by name.
func (s *slk) check() (bool, []string, map[string]bool) {
	s.t.Helper()
	var res struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
		Steps    []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"steps"`
	}
	a := s.api.do(http.MethodPost, "/api/v1/connections/"+s.conn+"/checks", "")
	if a.status != http.StatusOK {
		s.t.Fatalf("check = %d %s", a.status, a.body)
	}
	decode(s.t, a, &res)
	steps := map[string]bool{}
	for _, st := range res.Steps {
		steps[st.Name] = st.OK
	}
	return res.OK, res.Warnings, steps
}

// links counts the Socket Mode connections the fake Slack server accepted or refused.
func (s *slk) links() int {
	s.t.Helper()
	return countRequests(s.t, s.h.Fakes.Slack, "/link/")
}

// tickets are the tickets of every Socket Mode address dialled on the fake Slack server.
func (s *slk) tickets() []string {
	s.t.Helper()
	var out []string
	for _, r := range fakeRequests(s.t, s.h.Fakes.Slack) {
		if q, err := url.ParseQuery(r.Query); r.Path == "/link/" && err == nil && q.Get("ticket") != "" {
			out = append(out, q.Get("ticket"))
		}
	}
	return out
}

// envelopeRecord is the record of the envelope id, nil without one.
func (s *slk) envelopeRecord(id string) map[string]any {
	s.t.Helper()
	for _, rec := range s.inbound(s.conn, "interactive") {
		if full, _ := s.requestRecord(rec["id"].(string)); strings.Contains(fmt.Sprint(full["request_body"]), id) {
			return rec
		}
	}
	return nil
}

// deadAddress is a loopback address where nothing listens.
func deadAddress(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestSlackSocket is the Verification of S-101 against `muster dev` and the fake Slack server, with E2E_REPLICAS=2 on
// two replicas: the Connection "sl" in socket mode, whose Socket Mode connection only the Leader holds; a press through
// it is confirmed at once and acknowledges as the linked User with the Transport slack, and an envelope of another type
// is confirmed and ignored (C-24.FR-9, AC-10); a planned refresh opens the next connection before the old one closes; a
// second program on the same app is counted, shown, warned about by the check and muster doctor, and cleared by the
// next hello (C-24.FR-2); the check calls apps.connections.open without a socket; link_disabled and
// too_many_websockets back off and break nothing; the Request log of envelopes, without a status, and the ticket
// masked everywhere (C-24.FR-14, AC-17, C-23.FR-3); the socket through the fake HTTP proxy, and a dead proxy
// (C-24.AC-3); and the socket closed within a second of a switch to http mode and of a deletion. The back-off starts
// at 1 s (slack.socket_backoff), so every reconnection below is a second away and the waits poll for it.
func TestSlackSocket(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	s := newSlack(t, h, r)
	s.api.json(http.MethodPost, "/api/v1/users", `{"name":"alice","login":"alice","role":"responder"}`,
		http.StatusCreated)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		display_name, created_at) SELECT 1, 'AK0000000000S1', u.id, 'slack', c.id, '`+fakeslack.UserAlice+`', 'alice',
		u.name, now() FROM users u, connections c WHERE u.login = 'alice' AND c.public_id = '`+s.conn+`'`)
	connected := func() int { return len(s.socketLines(s.conn, "slack_socket_connected")) }
	failed := func() int { return len(s.socketLines(s.conn, "slack_socket_failed")) }
	var leader *Replica
	var g string
	var root slackMessage

	t.Run("socket_press", func(t *testing.T) {
		s := s.on(t)
		hello := s.waitSocketLines(s.conn, "slack_socket_connected", 0)[0]
		leader = leading(t, h)
		if hello["num_connections"] != 1.0 || hello["held"] != 1.0 || hello["error"] != nil {
			t.Errorf("slack_socket_connected %v", hello)
		}
		for i, rep := range h.Replicas {
			if n := len(connLines(rep, s.conn, "slack_socket_connected")); (rep == leader) != (n > 0) {
				t.Errorf("replica %d (leader %t) logged slack_socket_connected %d times", i, rep == leader, n)
			}
		}
		within(t, "one open socket", socketWait, func() bool { return s.appB().Open == 1 })
		s.fire("w1", "DiskFull", "u1", `{"team":"sl","cluster":"a","pod":"u1"}`)
		g = s.groupOf("a")
		root = s.root(s.number(g), "DiskFull")
		res := s.pressedOverSocket(root.TS, "ack")
		if *res.ConfirmMS >= 1000 {
			t.Errorf("the envelope was confirmed after %d ms", *res.ConfirmMS)
		}
		t.Logf("the press over the socket was confirmed after %d ms", *res.ConfirmMS)
		eventually(t, "the acknowledgement by alice", func() bool { return s.alertGroup(g)["status"] == "acknowledged" })
		if owner, _ := s.alertGroup(g)["owner"].(map[string]any); owner["login"] != "alice" {
			t.Errorf("owner %v", owner)
		}
		var acked []timelineEntry
		for _, x := range s.timeline(g, "") {
			if x.Kind == "status" && x.Event == "acknowledged" {
				acked = append(acked, x)
			}
		}
		if len(acked) != 1 || acked[0].Actor.Transport != "slack" || acked[0].Actor.Name != "alice" {
			t.Errorf("acknowledged %+v", acked)
		}
		var press map[string]any
		eventually(t, "slack_press on the leader", func() bool {
			for _, l := range logLines(leader, "slack_press") {
				if l["group"] == g && l["outcome"] == "done" {
					press = l
				}
			}
			return press != nil
		})
		if press["connection"] != s.conn || press["command"] != "acknowledge" || press["answer"] != nil {
			t.Errorf("slack_press %v", press)
		}

		// An envelope of another type is confirmed and changes nothing.
		entries := s.changes(g)
		sent := s.script(`{"app":"b","send":{"type":"events_api"}}`).Envelopes
		id := sent[len(sent)-1].EnvelopeID
		within(t, "the confirmation of the events_api envelope", socketWait, func() bool {
			return slices.ContainsFunc(s.socket().Envelopes, func(e fakeslack.Envelope) bool {
				return e.EnvelopeID == id && e.Type == "events_api" && e.Confirmed
			})
		})
		if got := s.changes(g); got != entries {
			t.Errorf("the events_api envelope changed the timeline: %d entries, want %d", got, entries)
		}
	})

	t.Run("refresh", func(t *testing.T) {
		s := s.on(t)
		for _, reason := range []string{"refresh_requested", "warning"} {
			before, fails, hellos := connected(), failed(), s.appB().Hellos
			s.script(`{"app":"b","disconnect":"` + reason + `"}`)
			within(t, "the overlapped refresh after "+reason, socketWait, func() bool {
				b := s.appB()
				return b.Overlapped && b.Open == 1
			})
			hello := s.waitSocketLines(s.conn, "slack_socket_connected", before)[0]
			if hello["num_connections"] != 2.0 || hello["held"] != 2.0 {
				t.Errorf("the hello of the refresh after %s %v", reason, hello)
			}
			if b := s.appB(); b.Hellos != hellos+1 || b.Open != 1 {
				t.Errorf("after %s: %+v, %d hellos before", reason, b, hellos)
			}
			if n := failed(); n != fails {
				t.Errorf("the refresh after %s logged slack_socket_failed: %v", reason,
					s.socketLines(s.conn, "slack_socket_failed")[fails:])
			}
		}
	})

	t.Run("shared", func(t *testing.T) {
		s := s.on(t)
		etag := s.api.do(http.MethodGet, "/api/v1/connections/"+s.conn, "").header.Get("ETag")
		shared, before := len(s.socketLines(s.conn, "slack_socket_shared")), connected()
		s.fake(http.MethodPut, s.fs+"/config", `{"extra_connections":{"b":1}}`)
		s.script(`{"app":"b","disconnect":"refresh_requested"}`)
		if l := s.waitSocketLines(s.conn, "slack_socket_shared", shared)[0]; l["other_connections"] != 1.0 ||
			l["level"] != "WARN" {
			t.Errorf("slack_socket_shared %v", l)
		}
		if l := s.waitSocketLines(s.conn, "slack_socket_connected", before)[0]; l["num_connections"] != 3.0 ||
			l["held"] != 2.0 {
			t.Errorf("slack_socket_connected %v", l)
		}
		if n := s.otherConnections(); n != 1 {
			t.Errorf("socket.other_connections %v, want 1", n)
		}
		if got := s.api.do(http.MethodGet, "/api/v1/connections/"+s.conn, "").header.Get("ETag"); got != etag {
			t.Errorf("the hello changed the ETag from %s to %s", etag, got)
		}
		ok, warnings, _ := s.check()
		if !ok || !slices.Equal(warnings, []string{"another_client_connected"}) {
			t.Errorf("check ok %t, warnings %v", ok, warnings)
		}
		warn := "WARN connection sl: " + slack.HintAnotherClientConnected + "\n"
		if code, out := h.runCLIStdout(t, "doctor"); !strings.Contains(out, warn) {
			t.Errorf("doctor (exit %d) has no line %q:\n%s", code, warn, out)
		}

		before = connected()
		s.fake(http.MethodPut, s.fs+"/config", `{"extra_connections":{"b":0}}`)
		s.script(`{"app":"b","disconnect":"refresh_requested"}`)
		if l := s.waitSocketLines(s.conn, "slack_socket_connected", before)[0]; l["num_connections"] != 2.0 {
			t.Errorf("slack_socket_connected %v", l)
		}
		if n := s.otherConnections(); n != 0 {
			t.Errorf("socket.other_connections %v, want 0", n)
		}
		if ok, warnings, _ := s.check(); !ok || len(warnings) != 0 {
			t.Errorf("check ok %t, warnings %v", ok, warnings)
		}
		if code, out := h.runCLIStdout(t, "doctor"); strings.Contains(out, "WARN connection sl:") {
			t.Errorf("doctor (exit %d) still warns:\n%s", code, out)
		}
		if n := len(s.socketLines(s.conn, "slack_socket_shared")); n != shared+1 {
			t.Errorf("slack_socket_shared %d times, want %d", n, shared+1)
		}
	})

	t.Run("check_open", func(t *testing.T) {
		s := s.on(t)
		within(t, "one open socket", socketWait, func() bool { return s.appB().Open == 1 })
		opens, links := len(s.requests("apps.connections.open")), s.links()
		ok, warnings, steps := s.check()
		if !ok || len(warnings) != 0 || !steps["app_token"] {
			t.Errorf("check ok %t, warnings %v, steps %v", ok, warnings, steps)
		}
		if got := s.requests("apps.connections.open")[opens:]; len(got) != 1 || got[0].Status != http.StatusOK ||
			!carries(got[0], fakeslack.SecondAppToken) {
			t.Errorf("the check's apps.connections.open %d requests: %v", len(got), described(got))
		}
		if got := s.links(); got != links {
			t.Errorf("the check opened %d sockets", got-links)
		}
		if b := s.appB(); b.Open != 1 {
			t.Errorf("after the check %+v", b)
		}
	})

	t.Run("backoff", func(t *testing.T) {
		s := s.on(t)
		before, fails := connected(), failed()
		s.script(`{"app":"b","disconnect":"link_disabled"}`)
		f := s.waitSocketLines(s.conn, "slack_socket_failed", fails)[0]
		wait, _ := f["backoff_seconds"].(float64)
		if f["reason"] != "link_disabled" || f["error"] != nil || wait < 0.5 || wait > 1 {
			t.Errorf("slack_socket_failed %v", f)
		}
		if l := s.waitSocketLines(s.conn, "slack_socket_connected", before)[0]; l["num_connections"] != 1.0 ||
			l["held"] != 1.0 {
			t.Errorf("slack_socket_connected after link_disabled %v", l)
		}
		within(t, "one open socket", socketWait, func() bool { return s.appB().Open == 1 })
		if hl := s.health(s.dest); hl.State != "healthy" {
			t.Errorf("health after link_disabled %+v", hl)
		}

		// too_many_websockets refuses the next connection of a refresh; the old one keeps taking presses.
		before, fails = connected(), failed()
		s.script(`{"app":"b","too_many_websockets":1,"disconnect":"refresh_requested"}`)
		f = s.waitSocketLines(s.conn, "slack_socket_failed", fails)[0]
		wait, _ = f["backoff_seconds"].(float64)
		if f["reason"] != "too_many_websockets" || f["error"] != nil || wait < 0.5 || wait > 1 {
			t.Errorf("slack_socket_failed %v", f)
		}
		s.waitActions(root.TS, "unack")
		s.pressedOverSocket(root.TS, "unack")
		eventually(t, "the unacknowledgement", func() bool { return s.alertGroup(g)["status"] == "firing" })
		if l := s.waitSocketLines(s.conn, "slack_socket_connected", before)[0]; l["held"] != 2.0 {
			t.Errorf("the old socket did not stay open until the next one's hello: %v", l)
		}
		within(t, "one open socket", socketWait, func() bool { return s.appB().Open == 1 })
		if n := failed(); n != fails+1 {
			t.Errorf("slack_socket_failed %v", s.socketLines(s.conn, "slack_socket_failed")[fails:])
		}
		if hl := s.health(s.dest); hl.State != "healthy" {
			t.Errorf("health after too_many_websockets %+v", hl)
		}
	})

	t.Run("request_log", func(t *testing.T) {
		s := s.on(t)
		types := map[string]string{}
		for _, e := range s.socket().Envelopes {
			if e.App == fakeslack.AppB && e.Confirmed {
				types[e.EnvelopeID] = e.Type
			}
		}
		var records []map[string]any
		eventually(t, "a record of each envelope", func() bool {
			records = s.requestLog("connection=" + s.conn + "&direction=inbound")
			return len(records) >= len(types)
		})
		seen := map[string]int{}
		for _, rec := range records {
			full, _ := s.requestRecord(rec["id"].(string))
			var env struct {
				EnvelopeID string         `json:"envelope_id"`
				Type       string         `json:"type"`
				Payload    map[string]any `json:"payload"`
			}
			_ = json.Unmarshal([]byte(fmt.Sprint(full["request_body"])), &env)
			headers, _ := full["request_headers"].(map[string]any)
			if types[env.EnvelopeID] != env.Type || rec["operation"] != env.Type || rec["status"] != nil ||
				rec["method"] != "WEBSOCKET" || rec["url"] != "ws://"+devmode.SlackAddr+"/link/" ||
				rec["via"] != "direct" || rec["destination"] != nil || rec["alert_group"] != nil ||
				full["response_headers"] != nil || headers == nil || len(headers) != 0 || full["error"] != nil ||
				full["response_body"] != `{"envelope_id":"`+env.EnvelopeID+`"}` {
				t.Errorf("record %v: %v", rec, full)
			}
			if env.Type == "interactive" && (env.Payload["token"] != "[redacted]" ||
				env.Payload["response_url"] != "[redacted]" || len(signedStrings(env.Payload)) != 0) {
				t.Errorf("the payload of %s is not masked: %v", env.EnvelopeID, full["request_body"])
			}
			seen[env.Type]++
		}
		if len(records) != len(types) || seen["interactive"] < 2 || seen["events_api"] != 1 {
			t.Errorf("%d records of %d envelopes: %v", len(records), len(types), seen)
		}

		opens := s.requestLog("connection=" + s.conn + "&operation=apps.connections.open")
		if len(opens) == 0 {
			t.Fatal("no record of apps.connections.open")
		}
		for _, rec := range opens {
			full, _ := s.requestRecord(rec["id"].(string))
			if body := fmt.Sprint(full["response_body"]); rec["direction"] != "outbound" ||
				rec["status"] != 200.0 || !strings.Contains(body, "ticket=[redacted]") {
				t.Errorf("record %v answered %s", rec, body)
			}
		}

		tickets, values := s.tickets(), s.buttonValues()
		if len(tickets) == 0 || len(values) == 0 {
			t.Fatalf("%d tickets, %d button values", len(tickets), len(values))
		}
		for _, rec := range s.requestLog("connection=" + s.conn) {
			_, text := s.requestRecord(rec["id"].(string))
			if m := socketLeak.FindString(text) + slackLeak.FindString(text); m != "" {
				t.Errorf("record %s %s holds %q", rec["id"], rec["operation"], m)
			}
			for _, v := range append(slices.Clone(tickets), values...) {
				if strings.Contains(text, v) {
					t.Errorf("record %s %s holds a ticket or a signed action id", rec["id"], rec["operation"])
				}
			}
		}
		for i, rep := range h.Replicas {
			out := rep.Output()
			if m := socketLeak.FindString(out); m != "" {
				t.Errorf("the output of replica %d holds %q", i, m)
			}
			for _, ticket := range tickets {
				if strings.Contains(out, ticket) {
					t.Errorf("the output of replica %d holds a ticket", i)
				}
			}
		}
	})

	t.Run("proxy", func(t *testing.T) {
		s := s.on(t)
		resetProxy(t)
		before := connected()
		s.save(`"event_mode":"socket","proxy":{"enabled":true,"type":"http","address":"` + devmode.HTTPProxyAddr + `"}`)
		s.waitSocketLines(s.conn, "slack_socket_connected", before)
		within(t, "one open socket", socketWait, func() bool { return s.appB().Open == 1 })
		// The confirmation goes from Muster to the fake over the socket, which only a CONNECT tunnel carries through
		// the fake proxy.
		s.waitActions(root.TS, "ack")
		res := s.pressedOverSocket(root.TS, "ack")
		eventually(t, "the acknowledgement through the proxy", func() bool {
			return s.alertGroup(g)["status"] == "acknowledged"
		})
		targets := proxyTargets(t)
		if len(targets) < 2 || slices.ContainsFunc(targets, func(x string) bool { return x != devmode.SlackAddr }) {
			t.Errorf("the proxy connected to %v, want only %s", targets, devmode.SlackAddr)
		}
		var rec map[string]any
		eventually(t, "the record of the press", func() bool {
			rec = s.envelopeRecord(res.EnvelopeID)
			return rec != nil
		})
		if rec["via"] != "proxy" {
			t.Errorf("record %v", rec)
		}

		// A dead proxy: apps.connections.open is retried in the background class until its bound of 20 s, then
		// the connection fails as dial.
		fails := failed()
		s.save(`"event_mode":"socket","proxy":{"enabled":true,"type":"http","address":"` + deadAddress(t) + `"}`)
		var f map[string]any
		within(t, "slack_socket_failed through the dead proxy", 45*time.Second, func() bool {
			if got := s.socketLines(s.conn, "slack_socket_failed"); len(got) > fails {
				f = got[fails]
			}
			return f != nil
		})
		msg, _ := f["error"].(string)
		if f["reason"] != "dial" || !strings.Contains(msg, "apps.connections.open") || socketLeak.MatchString(msg) ||
			strings.Contains(msg, fakeslack.SecondBotToken) {
			t.Errorf("slack_socket_failed %v", f)
		}
		if b := s.appB(); b.Open != 0 {
			t.Errorf("a socket is open through the dead proxy: %+v", b)
		}
		before = connected()
		s.save(socketDirect)
		s.waitSocketLines(s.conn, "slack_socket_connected", before)
		within(t, "one open socket", socketWait, func() bool { return s.appB().Open == 1 })
	})

	t.Run("mode_switch", func(t *testing.T) {
		s := s.on(t)
		within(t, "one open socket", socketWait, func() bool { return s.appB().Open == 1 })
		conn := s.save(`"event_mode":"http","slack_signing_secret":"` + fakeslack.SecondSigningSecret +
			`","proxy":{"enabled":false}`)
		if conn["event_mode"] != "http" {
			t.Fatalf("the connection %v", conn)
		}
		within(t, "the socket to close after the switch to http", time.Second, func() bool {
			return s.appB().Open == 0
		})

		gone := s.api.json(http.MethodPost, "/api/v1/connections", `{"type":"slack","name":"gone",
			"event_mode":"socket","bot_token":"`+fakeslack.SecondBotToken+`","app_token":"`+fakeslack.SecondAppToken+
			`","proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
		s.waitSocketLines(gone, "slack_socket_connected", 0)
		within(t, "the socket of gone", socketWait, func() bool { return s.appB().Open == 1 })
		s.api.json(http.MethodDelete, "/api/v1/connections/"+gone, "", http.StatusNoContent)
		within(t, "the socket to close after the deletion", time.Second, func() bool { return s.appB().Open == 0 })

		before := connected()
		s.save(socketDirect)
		s.waitSocketLines(s.conn, "slack_socket_connected", before)
		within(t, "one open socket", socketWait, func() bool { return s.appB().Open == 1 })
	})

	for i, rep := range h.Replicas {
		if m := socketLeak.FindString(rep.Output()); m != "" {
			t.Errorf("the output of replica %d holds %q", i, m)
		}
		if leader == nil || rep == leader {
			continue
		}
		for _, event := range []string{"slack_socket_connected", "slack_socket_shared", "slack_socket_failed"} {
			if n := len(connLines(rep, s.conn, event)); n != 0 {
				t.Errorf("replica %d, which does not lead, logged %s %d times", i, event, n)
			}
		}
	}
}

// TestSlackSocketLeader is C-24.FR-9 on two replicas: only the Leader holds the Socket Mode connection of a Connection
// in socket mode, and after it stops the new Leader opens one, so that the fake Slack server sees one open socket.
func TestSlackSocketLeader(t *testing.T) {
	h := Start(t, FakesInProcess)
	a := h.StartReplica(t, ReplicaOptions{})
	a.waitFor(t, "replica A to lead", func() bool { return a.Metric(t, "muster_leader") == "1" })
	b := h.StartReplica(t, ReplicaOptions{ListenApp: ":8080", ListenIngest: ":8081", ListenInternal: ":8082"})
	s := newSlack(t, h, b)
	fake := h.InProcess.Slack
	a.waitFor(t, "A's socket", func() bool {
		return len(connLines(a, s.conn, "slack_socket_connected")) > 0 && fake.Sockets().Apps[fakeslack.AppB].Open == 1
	})
	if n := len(connLines(b, s.conn, "slack_socket_connected")); n != 0 || b.Metric(t, "muster_leader") == "1" {
		t.Errorf("replica B, which does not lead, logged slack_socket_connected %d times", n)
	}
	opened := fake.Sockets().Apps[fakeslack.AppB].Opened

	a.Stop(t)
	b.waitFor(t, "replica B to lead", func() bool { return b.Metric(t, "muster_leader") == "1" })
	b.waitFor(t, "B's socket", func() bool {
		return len(connLines(b, s.conn, "slack_socket_connected")) > 0 && fake.Sockets().Apps[fakeslack.AppB].Open == 1
	})
	if l := connLines(b, s.conn, "slack_socket_connected")[0]; l["num_connections"] != 1.0 || l["held"] != 1.0 {
		t.Errorf("replica B's slack_socket_connected %v", l)
	}
	if st := fake.Sockets().Apps[fakeslack.AppB]; st.Open != 1 || st.Opened != opened+1 {
		t.Errorf("after the handover %+v, %d opened before", st, opened)
	}
}
