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
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/telegram"
)

// tgd drives Telegram Destinations: the mm environment — an Admin's Personal access token, the Integration "lab" and
// the fake Alertmanager — with the demo Telegram Connection "Dev Telegram" of the fake Bot API.
type tgd struct {
	*mm
	ftg  string
	conn string
}

func newTGD(t *testing.T, h *Harness, r *Replica) *tgd {
	t.Helper()
	e := &tgd{mm: newMM(t, h, r), ftg: h.Fakes.Telegram + "/_fake"}
	for _, item := range e.api.json(http.MethodGet, "/api/v1/connections?type=telegram", "", http.StatusOK)["items"].([]any) {
		if c := item.(map[string]any); c["name"] == devmode.TelegramConnectionName {
			e.conn = c["id"].(string)
		}
	}
	if e.conn == "" {
		t.Fatal("no demo Telegram Connection")
	}
	return e
}

// create is createDestination of a Telegram Destination on channel, with a limiter of limit per minute.
func (e *tgd) create(name, channel string, limit int) answer {
	e.t.Helper()
	return e.api.do(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"telegram","name":%q,
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":%d,"per_seconds":60}}`, name, e.conn,
		channel, mentionSettings(nil), limit))
}

func (e *tgd) member(chat int64, m faketelegram.Member) {
	e.t.Helper()
	b, _ := json.Marshal(m)
	e.fake(http.MethodPut, fmt.Sprintf("%s/chats/%d/members/%d", e.ftg, chat, faketelegram.BotID), string(b))
}

func (e *tgd) messages(chat int64) []faketelegram.Message {
	e.t.Helper()
	var out []faketelegram.Message
	decode(e.t, call(e.t, http.MethodGet, fmt.Sprintf("%s/messages?chat=%d", e.ftg, chat), ""), &out)
	return out
}

// notifications are the notifications of channel posts; Thread replies in the discussion group are left out.
func (e *tgd) notifications() []faketelegram.Notification {
	e.t.Helper()
	var all, out []faketelegram.Notification
	decode(e.t, call(e.t, http.MethodGet, e.ftg+"/notifications", ""), &all)
	for _, n := range all {
		if n.Chat == faketelegram.ChannelID {
			out = append(out, n)
		}
	}
	return out
}

func (e *tgd) requests() []fakeserver.Request {
	e.t.Helper()
	var out []fakeserver.Request
	decode(e.t, call(e.t, http.MethodGet, e.ftg+"/requests", ""), &out)
	return out
}

// buttons are the labels of a keyboard, row after row.
func buttons(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var kb struct {
		InlineKeyboard [][]struct {
			Text string `json:"text"`
		} `json:"inline_keyboard"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &kb); err != nil {
			t.Fatal(err)
		}
	}
	out := []string{}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.Text)
		}
	}
	return out
}

func problemDetail(t *testing.T, a answer) string {
	t.Helper()
	var p struct {
		Errors []struct {
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	decode(t, a, &p)
	if len(p.Errors) == 0 {
		t.Fatalf("no errors in %d %s", a.status, a.body)
	}
	return p.Errors[0].Detail
}

// TestTelegram is S-042 against `muster dev` and the fake Telegram server: the Destination check on save
// (C-14.AC-13), a Loud Root message in HTML with its keyboard (C-14.AC-17, AC-8, FR-6), edits that keep the keyboard
// of the new state (C-14.AC-14), "message can't be edited" republishing the Root message Quietly (C-14.FR-7, F-065,
// F-012), the Broken probe (C-14.AC-12), no token anywhere (C-14.AC-7), muster doctor (C-02.FR-14) and the best-effort
// deleteWebhook of a deleted Connection.
func TestTelegram(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newTGD(t, h, r)

	// C-14.AC-13: the discussion group is found from the channel alone; two refusals save nothing.
	if a := e.create("nocomments", "@no_comments", 30); a.status != http.StatusUnprocessableEntity ||
		problemDetail(t, a) != telegram.MessageNoComments {
		t.Errorf("no comments = %d %s", a.status, a.body)
	}
	e.member(faketelegram.GroupID, faketelegram.Member{Status: faketelegram.StatusMember})
	if a := e.create("alerts", "@muster_alerts", 30); a.status != http.StatusUnprocessableEntity ||
		problemDetail(t, a) != "The bot is not an admin of the discussion group Muster alerts Chat. Make the bot an "+
			"admin there, allowed to post messages." {
		t.Errorf("not an admin of the group = %d %s", a.status, a.body)
	}
	e.member(faketelegram.GroupID, faketelegram.Member{Status: faketelegram.StatusAdministrator, CanPostMessages: true})
	a := e.create("alerts", "@muster_alerts", 30)
	if a.status != http.StatusCreated {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	var created struct {
		Destination struct {
			ID                   string `json:"id"`
			DiscussionGroupID    string `json:"discussion_group_id"`
			DiscussionGroupTitle string `json:"discussion_group_title"`
		} `json:"destination"`
	}
	decode(t, a, &created)
	dest := created.Destination.ID
	if created.Destination.DiscussionGroupID != "-1001000000002" ||
		created.Destination.DiscussionGroupTitle != faketelegram.GroupTitle {
		t.Errorf("created %s", a.body)
	}
	check := e.api.json(http.MethodPost, "/api/v1/destinations/"+dest+"/checks", "", http.StatusOK)
	if check["ok"] != true || len(check["checks"].([]any)) != 4 {
		t.Errorf("check = %v", check)
	}
	e.route("tg", "tg", []string{dest}, nil)

	// C-14.AC-17, AC-8, FR-6: a Loud channel post in HTML, its labels in an expandable quote, alert data literal.
	e.group("t1", "CertExpiry")
	e.alert("t1", "a", `{"team":"tg","cluster":"a","domain":"a.example.org","note":"@channel <b>x</b>"}`, "")
	e.notify("t1", "first notification")
	g := e.groupOf("a")
	eventually(t, "the channel post", func() bool { return len(e.messages(faketelegram.ChannelID)) == 1 })
	post := e.messages(faketelegram.ChannelID)[0]
	if post.ParseMode != "HTML" || !strings.Contains(post.Text, "<blockquote expandable>") ||
		strings.Contains(post.Text, "<table") || strings.Contains(post.Text, "<pre>") ||
		!strings.Contains(post.Text, "@\u200bchannel &lt;b&gt;x&lt;/b&gt;") || post.DisableNotification ||
		!strings.Contains(post.Text, "Open in Muster") {
		t.Errorf("post %+v\n%s", post, post.Text)
	}
	t.Logf("keyboard %v", buttons(t, post.ReplyMarkup))
	if got := buttons(t, post.ReplyMarkup); len(got) < 3 || got[0] != "Ack" || got[1] != "Resolve" ||
		!strings.HasPrefix(got[2], "Snooze") {
		t.Errorf("keyboard %v", got)
	}
	notified := e.notifications()
	if len(notified) != 2 || !notified[0].Sound || !notified[1].Sound {
		t.Errorf("notifications %+v", notified)
	}

	// C-14.AC-14: each edit carries the keyboard of the new state.
	e.command(g, "acknowledge")
	eventually(t, "the edit to acknowledged", func() bool {
		b := buttons(t, e.messages(faketelegram.ChannelID)[0].ReplyMarkup)
		return len(b) > 0 && b[0] == "Unack"
	})
	e.api.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/snooze", `{"until":"2030-01-01T00:00:00Z"}`, http.StatusOK)
	eventually(t, "the edit to snoozed", func() bool {
		return slices.Equal(buttons(t, e.messages(faketelegram.ChannelID)[0].ReplyMarkup),
			[]string{"Ack", "Unsnooze", "Resolve"})
	})
	post = e.messages(faketelegram.ChannelID)[0]
	if len(e.messages(faketelegram.ChannelID)) != 1 || len(post.Edits) < 2 {
		t.Errorf("channel %+v", e.messages(faketelegram.ChannelID))
	}
	for _, ed := range post.Edits {
		if ed.ReplyMarkup == nil || ed.ParseMode != "HTML" {
			t.Errorf("an edit without its keyboard: %+v", ed)
		}
	}
	if len(e.notifications()) != 2 {
		t.Errorf("an edit notified: %+v", e.notifications())
	}
	eventually(t, "the deliveries to settle", func() bool {
		return h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0
	})

	// C-14.FR-7, F-065: "message can't be edited" is a gone Root message, published again once, Quietly, with the
	// note; Quiet posts notify both accounts without sound (F-012).
	e.fake(http.MethodPost, e.ftg+"/faults", `{"path":"/bot*/editMessageText","status":400,"times":1,`+
		`"body":"{\"ok\":false,\"error_code\":400,\"description\":\"Bad Request: message can't be edited\"}"}`)
	e.command(g, "unsnooze")
	eventually(t, "the republication", func() bool { return len(e.messages(faketelegram.ChannelID)) == 2 })
	again := e.messages(faketelegram.ChannelID)[1]
	if !deletedNote.MatchString(again.Text) || !again.DisableNotification || again.ReplyMarkup == nil {
		t.Errorf("republished %+v\n%s", again, again.Text)
	}
	if n := e.notifications(); len(n) != 4 || n[2].Sound || n[3].Sound || n[2].Account == n[3].Account {
		t.Errorf("notifications of the Quiet post %+v", n)
	}
	if hl := e.health(dest); hl.State != "healthy" {
		t.Errorf("health after the republication %+v", hl)
	}
	// Until S-066, a Thread reply is a plain message in the discussion group, never in the channel (F-015).
	e.alert("t1", "b", `{"team":"tg","cluster":"a","domain":"b.example.org"}`, "")
	e.notify("t1", "new alerts added")
	advance(t, r, 61)
	eventually(t, "the Thread reply in the discussion group", func() bool {
		return len(e.messages(faketelegram.GroupID)) > 0
	})
	if reply := e.messages(faketelegram.GroupID)[0]; reply.ReplyParameters != nil ||
		!strings.Contains(reply.Text, "b.example.org") || len(e.messages(faketelegram.ChannelID)) != 2 {
		t.Errorf("thread reply %+v", reply)
	}

	// C-14.AC-12: without the right to post, the Destination breaks; with nothing waiting, the probe keeps it Broken
	// with the missing right, and ends the Broken state once the right is back, in the delivery class.
	e.member(faketelegram.ChannelID, faketelegram.Member{Status: faketelegram.StatusAdministrator,
		CanEditMessages: true})
	e.group("t3", "QueueFull")
	e.alert("t3", "a", `{"team":"tg","cluster":"q1"}`, "")
	e.notify("t3", "first notification")
	hl := e.waitHealth(dest, "broken")
	t.Logf("broken: %s", *hl.Reason)
	e.alert("t3", "a", `{"team":"tg","cluster":"q1"}`, `,"status":"resolved"`)
	e.notify("t3", "all alerts resolved")
	advance(t, r, 300)
	eventually(t, "the probe's reason", func() bool {
		hl = e.health(dest)
		return hl.Reason != nil && strings.Contains(*hl.Reason, "post messages")
	})
	if hl.State != "broken" || *hl.Reason != telegram.MessageMayNotPost {
		t.Errorf("after the probe %+v", hl)
	}
	e.member(faketelegram.ChannelID, faketelegram.Member{Status: faketelegram.StatusAdministrator,
		CanPostMessages: true, CanEditMessages: true})
	d0 := e.clientRequests("delivery", "ok")
	posts := len(e.messages(faketelegram.ChannelID))
	advance(t, r, 300)
	e.waitHealth(dest, "healthy")
	if got := e.clientRequests("delivery", "ok") - d0; got < 3 {
		t.Errorf("the probe made %v requests in the delivery class", got)
	}
	if len(e.messages(faketelegram.ChannelID)) != posts {
		t.Errorf("the probe posted: %d posts", len(e.messages(faketelegram.ChannelID)))
	}

	// C-14.AC-7: the token is in no log line and no stored error.
	if strings.Contains(r.Output(), "dev-telegram-token") {
		t.Error("the bot token reached the log")
	}
	if n := h.count(t, `SELECT count(*) FROM delivery_events WHERE error LIKE '%dev-telegram-token%'`) +
		h.count(t, `SELECT count(*) FROM destinations WHERE broken_reason LIKE '%dev-telegram-token%'`); n != 0 {
		t.Errorf("%d stored errors carry the token", n)
	}

	// C-02.FR-14: muster doctor prints the Telegram Destination.
	if code, out := h.runCLIStdout(t, "doctor"); !strings.Contains(out, "OK   destination alerts: ok\n") {
		t.Errorf("doctor (exit %d):\n%s", code, out)
	}

	// Deleting a Connection in the webhook mode removes its webhook as a best effort, even when the call fails.
	hooked := func(name, token string) string {
		return e.api.json(http.MethodPost, "/api/v1/connections", fmt.Sprintf(`{"type":"telegram","name":%q,
			"bot_token":%q,"bot_api_base_url":%q,"update_mode":"webhook","proxy":{"enabled":false},
			"limiter":{"limit":15,"per_seconds":1}}`, name, token, h.Fakes.Telegram), http.StatusCreated)["id"].(string)
	}
	deletes := func() int {
		n := 0
		for _, q := range e.requests() {
			if strings.HasSuffix(q.Path, "/deleteWebhook") {
				n++
			}
		}
		return n
	}
	w := hooked("hooked", "777020:hook-token")
	if a := e.api.do(http.MethodDelete, "/api/v1/connections/"+w, ""); a.status != http.StatusNoContent ||
		deletes() != 1 || !strings.Contains(r.Output(), `"event":"telegram_webhook_deleted","connection":"`+w+`"`) {
		t.Errorf("delete = %d, %d deleteWebhook", a.status, deletes())
	}
	w = hooked("refused", "777021:hook-token")
	e.fake(http.MethodPost, e.ftg+"/faults", `{"path":"/bot777021:hook-token/deleteWebhook","status":500,"times":1}`)
	if a := e.api.do(http.MethodDelete, "/api/v1/connections/"+w, ""); a.status != http.StatusNoContent ||
		deletes() != 2 {
		t.Errorf("delete with a failing deleteWebhook = %d, %d deleteWebhook", a.status, deletes())
	}
	eventually(t, "the outcome of deleteWebhook", func() bool {
		return strings.Contains(r.Output(), `"event":"telegram_webhook_delete_failed","connection":"`+w+`"`)
	})
	if strings.Contains(r.Output(), "hook-token") {
		t.Error("a webhook bot token reached the log")
	}
}

// TestTelegramChatBudget is F-016 end to end, on a replica beside the fake servers of the test: the 21st send or edit
// within a minute in the channel is answered 429 with the delay that is left, and Muster waits exactly that long.
func TestTelegramChatBudget(t *testing.T) {
	h := Start(t, FakesInProcess)
	r := h.StartReplica(t, ReplicaOptions{})
	e := newTGD(t, h, r)
	fake := h.InProcess.Telegram
	var mu sync.Mutex
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	fake.SetClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	})
	t.Cleanup(func() { fake.SetClock(nil) })
	advanceFake := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	a := e.create("budget", "@muster_alerts", 100)
	if a.status != http.StatusCreated {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	var created struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	decode(t, a, &created)
	e.route("budget", "tg", []string{created.Destination.ID}, nil)
	e.group("b1", "Budget")
	e.alert("b1", "a", `{"team":"tg","cluster":"b"}`, "")
	e.notify("b1", "first notification")
	g := e.groupOf("b")
	eventually(t, "the channel post", func() bool { return len(fake.Messages(faketelegram.ChannelID)) == 1 })
	eventually(t, "the deliveries to settle", func() bool {
		return h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0
	})
	// 19 more posts by another bot in the same chat spend its budget of 20 for this minute.
	for i := range 19 {
		if a := call(t, http.MethodPost, h.Fakes.Telegram+"/bot42:other/sendMessage",
			fmt.Sprintf(`{"chat_id":%d,"text":"filler %d"}`, faketelegram.ChannelID, i), "Content-Type",
			"application/json"); a.status != http.StatusOK {
			t.Fatalf("filler %d = %d %s", i, a.status, a.body)
		}
	}
	advanceFake(57 * time.Second)
	e.command(g, "acknowledge")
	var limited fakeserver.Request
	eventually(t, "the 429", func() bool {
		for _, q := range fake.Requests() {
			if strings.HasSuffix(q.Path, "/editMessageText") && q.Status == http.StatusTooManyRequests {
				limited = q
				return true
			}
		}
		return false
	})
	advanceFake(3 * time.Second)
	var edited fakeserver.Request
	eventually(t, "the edit after the wait", func() bool {
		for _, q := range fake.Requests() {
			if strings.HasSuffix(q.Path, "/editMessageText") && q.Status == http.StatusOK {
				edited = q
				return true
			}
		}
		return false
	})
	waited := time.Duration(edited.AtMs-limited.AtMs) * time.Millisecond
	t.Logf("waited %v after the 429", waited)
	if waited < 3*time.Second || waited > 6*time.Second {
		t.Errorf("waited %v, want retry_after 3 s", waited)
	}
	if b := buttons(t, fake.Messages(faketelegram.ChannelID)[0].ReplyMarkup); len(b) == 0 || b[0] != "Unack" {
		t.Errorf("keyboard after the wait %v", b)
	}
}
