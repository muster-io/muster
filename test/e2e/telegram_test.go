// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
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

// botReplies are the bot's messages in the discussion group: its Thread replies, without the automatic copies and the
// comments.
func (e *tgd) botReplies() []faketelegram.Message {
	e.t.Helper()
	var out []faketelegram.Message
	for _, m := range e.messages(faketelegram.GroupID) {
		if m.From != nil && m.From.ID == faketelegram.BotID {
			out = append(out, m)
		}
	}
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
	// A Thread reply goes to the discussion group, never to the channel (F-015).
	e.alert("t1", "b", `{"team":"tg","cluster":"a","domain":"b.example.org"}`, "")
	e.notify("t1", "new alerts added")
	advance(t, r, 61)
	eventually(t, "the Thread reply in the discussion group", func() bool { return len(e.botReplies()) > 0 })
	if reply := e.botReplies()[0]; !strings.Contains(reply.Text, "b.example.org") ||
		len(e.messages(faketelegram.ChannelID)) != 2 {
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
	// The synthetic Snapshot of MusterDestinationBroken, stored with the Broken state, is processed first, so that the
	// resolution below is the Snapshot that notify waits for.
	eventually(t, "the internal alert's snapshot", func() bool {
		return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state = 'pending'`) == 0
	})
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

// copyOf is the automatic copy of the channel post in the discussion group, nil before it arrived.
func (e *tgd) copyOf(post int64) *faketelegram.Message {
	e.t.Helper()
	for _, m := range e.messages(faketelegram.GroupID) {
		if m.IsAutomaticForward && m.ForwardOrigin != nil && m.ForwardOrigin.MessageID == post {
			return &m
		}
	}
	return nil
}

// threadNotAttached is thread_not_attached of the delivery state of the Alert Group in its one Destination.
func (e *tgd) threadNotAttached(g string) bool {
	e.t.Helper()
	var page struct {
		Items []struct {
			ThreadNotAttached bool `json:"thread_not_attached"`
		} `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/deliveries", ""), &page)
	if len(page.Items) != 1 {
		e.t.Fatalf("deliveries %+v", page.Items)
	}
	return page.Items[0].ThreadNotAttached
}

// threadState is the Thread of the delivery whose Root message is the channel post.
func (e *tgd) threadState(post int64) string {
	e.t.Helper()
	states := map[string]int64{}
	for _, s := range []string{"none", "waiting_for_copy", "attached", "unattached"} {
		states[s] = e.h.count(e.t, fmt.Sprintf(`SELECT count(*) FROM deliveries WHERE message_id = '%d'
			AND thread_state = '%s'`, post, s))
		if states[s] == 1 {
			return s
		}
	}
	return fmt.Sprint(states)
}

// replyTo is the message a message replies to, 0 for none.
func replyTo(m faketelegram.Message) int64 {
	var rp struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(m.ReplyParameters, &rp)
	return rp.MessageID
}

// pendingUpdates are the updates of the demo Connection's bot that the fake has not handed over yet.
func (e *tgd) pendingUpdates() float64 {
	e.t.Helper()
	var info struct {
		Result struct {
			Pending float64 `json:"pending_update_count"`
		} `json:"result"`
	}
	decode(e.t, call(e.t, http.MethodGet, e.h.Fakes.Telegram+"/bot"+devmode.TelegramConnectionBotToken+
		"/getWebhookInfo", ""), &info)
	return info.Result.Pending
}

// TestTelegramThreads is S-066 against `muster dev` and the fake Telegram server: Thread replies attach to the
// automatic copy of the post (C-14.AC-1), in either order of copy and Publication (C-14.FR-3); the copy's edit_date and
// its edited_message change nothing (C-14.AC-15); the channel carries only the Root message (C-14.AC-19); Quiet and
// Loud replies (C-14.FR-6); a withheld copy makes the replies wait telegram.copy_wait and go unattached until a
// comment teaches the copy (C-14.AC-2); a deleted copy loses the Thread (C-14.AC-16); the copies notify member with
// sound and Thread replies member only (F-013, F-014); and short-lived pruning deletes the copies after a day.
func TestTelegramThreads(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newTGD(t, h, r)
	a := e.create("alerts", "@muster_alerts", 100)
	if a.status != http.StatusCreated {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	var created struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	decode(t, a, &created)
	dest := created.Destination.ID
	e.route("tg", "tg", []string{dest}, nil)

	// C-14.AC-1, C-01.FR-13: the post, its automatic copy a second later, after the answer to sendMessage, and a
	// reply about a new Alert to the copy, in its comment Thread.
	e.group("t1", "CertExpiry")
	e.alert("t1", "a", `{"team":"tg","cluster":"t1","domain":"a.example.org"}`, "")
	e.notify("t1", "first notification")
	g := e.groupOf("t1")
	eventually(t, "the channel post", func() bool { return len(e.messages(faketelegram.ChannelID)) == 1 })
	post := e.messages(faketelegram.ChannelID)[0].ID
	eventually(t, "the copy to attach the Thread", func() bool { return e.threadState(post) == "attached" })
	cp := e.copyOf(post)
	if cp == nil || cp.From == nil || cp.From.ID != faketelegram.TelegramUserID || cp.EditDate != cp.Date ||
		cp.ReplyMarkup != nil {
		t.Fatalf("copy %+v", cp)
	}
	e.alert("t1", "b", `{"team":"tg","cluster":"t1","domain":"b.example.org"}`, "")
	e.notify("t1", "new alerts added")
	eventually(t, "the reply to the copy", func() bool { return len(e.botReplies()) == 1 })
	reply := e.botReplies()[0]
	if replyTo(reply) != cp.ID || reply.MessageThreadID != cp.ID || !strings.Contains(reply.Text, "b.example.org") ||
		reply.DisableNotification {
		t.Fatalf("reply %+v", reply)
	}

	// F-013, F-014: the copy notified member with sound; the Thread reply notified member only.
	var all []faketelegram.Notification
	decode(t, call(t, http.MethodGet, e.ftg+"/notifications", ""), &all)
	var ofCopy, ofReply []faketelegram.Notification
	for _, n := range all {
		switch {
		case n.Chat == faketelegram.GroupID && n.MessageID == cp.ID:
			ofCopy = append(ofCopy, n)
		case n.Chat == faketelegram.GroupID && n.MessageID == reply.ID:
			ofReply = append(ofReply, n)
		}
	}
	if len(ofCopy) != 1 || ofCopy[0].Account != faketelegram.AccountMember || !ofCopy[0].Sound ||
		len(ofReply) != 1 || ofReply[0].Account != faketelegram.AccountMember {
		t.Errorf("notifications of the copy %+v and of the reply %+v", ofCopy, ofReply)
	}

	// C-14.AC-15: each edit of the post makes an edited_message update for the copy, which changes nothing: the
	// Acknowledge, the new Alert and the Snooze make three edits and no more, and no delivery event; C-14.FR-6: a new
	// Alert while acknowledged is a Quiet reply.
	edits0 := len(e.messages(faketelegram.ChannelID)[0].Edits)
	e.command(g, "acknowledge")
	eventually(t, "the edit to acknowledged", func() bool {
		return len(e.messages(faketelegram.ChannelID)[0].Edits) == edits0+1
	})
	e.alert("t1", "c", `{"team":"tg","cluster":"t1","domain":"c.example.org"}`, "")
	e.notify("t1", "new alerts added")
	advance(t, r, 61)
	eventually(t, "the Quiet reply", func() bool { return len(e.botReplies()) == 2 })
	if q := e.botReplies()[1]; !q.DisableNotification || replyTo(q) != cp.ID {
		t.Errorf("quiet reply %+v", q)
	}
	e.api.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/snooze", `{"until":"2030-01-01T00:00:00Z"}`, http.StatusOK)
	eventually(t, "the edit to snoozed and the edited copies handled", func() bool {
		return len(e.messages(faketelegram.ChannelID)[0].Edits) == edits0+3 &&
			len(e.copyOf(post).Edits) == edits0+3 && e.pendingUpdates() == 0
	})
	advance(t, r, 5)
	edits := 0
	for _, q := range e.requests() {
		if strings.HasSuffix(q.Path, "/editMessageText") {
			edits++
		}
	}
	if got := deliveryEvents(e.timeline(g, "&kind=delivery")); edits != edits0+3 ||
		len(e.messages(faketelegram.ChannelID)[0].Edits) != edits0+3 || strings.Join(got, ",") != "publication" {
		t.Errorf("edits %d of %d, delivery events %v", edits, edits0+3, got)
	}
	if strings.Contains(r.Output(), `"event":"telegram_update_dropped"`) {
		t.Error("a chat message was dropped as unhandled")
	}

	// C-14.AC-19: the channel holds the one Root message; everything else went to the group.
	if n := len(e.messages(faketelegram.ChannelID)); n != 1 {
		t.Errorf("%d channel posts", n)
	}

	// C-14.AC-2: the copy is withheld; the reply waits telegram.copy_wait, then goes unattached; after a person's
	// comment the next reply attaches to the copy.
	e.fake(http.MethodPut, e.ftg+"/config", `{"withhold_copies":true}`)
	e.group("t2", "DiskSlow")
	e.alert("t2", "a", `{"team":"tg","cluster":"t2","disk":"sda"}`, "")
	e.notify("t2", "first notification")
	g2 := e.groupOf("t2")
	eventually(t, "the second post", func() bool { return len(e.messages(faketelegram.ChannelID)) == 2 })
	p2 := e.messages(faketelegram.ChannelID)[1].ID
	eventually(t, "the withheld copy", func() bool { return e.copyOf(p2) != nil })
	if s := e.threadState(p2); s != "waiting_for_copy" {
		t.Fatalf("thread %s", s)
	}
	replies := len(e.botReplies())
	e.alert("t2", "b", `{"team":"tg","cluster":"t2","disk":"sdb"}`, "")
	e.notify("t2", "new alerts added")
	if len(e.botReplies()) != replies || e.threadNotAttached(g2) {
		t.Fatal("the reply did not wait for the copy")
	}
	advance(t, r, 61)
	eventually(t, "the unattached reply", func() bool { return len(e.botReplies()) == replies+1 })
	first := e.botReplies()[replies]
	if first.ReplyParameters != nil || first.MessageThreadID != 0 || !e.threadNotAttached(g2) {
		t.Fatalf("first link %+v", first)
	}
	if got := deliveryEvents(e.timeline(g2, "&kind=delivery")); !slices.Contains(got, "thread_not_attached") {
		t.Errorf("delivery events %v", got)
	}
	e.fake(http.MethodPost, e.ftg+"/comment", fmt.Sprintf(`{"post_id":%d,"from":{"id":7001},"text":"looking"}`, p2))
	eventually(t, "the comment to attach the Thread", func() bool { return e.threadState(p2) == "attached" })
	if e.threadNotAttached(g2) {
		t.Error("still not attached after the comment")
	}
	e.alert("t2", "c", `{"team":"tg","cluster":"t2","disk":"sdc"}`, "")
	e.notify("t2", "new alerts added")
	advance(t, r, 61)
	eventually(t, "the attached reply", func() bool { return len(e.botReplies()) == replies+2 })
	if att := e.botReplies()[replies+1]; replyTo(att) != e.copyOf(p2).ID {
		t.Fatalf("after the comment %+v", att)
	}
	e.fake(http.MethodPut, e.ftg+"/config", `{"withhold_copies":false}`)

	// C-14.FR-3: a copy that arrives before the answer to sendMessage attaches the Thread as well.
	e.fake(http.MethodPut, e.ftg+"/config", `{"copy_delay_ms":0}`)
	e.group("t3", "QueueFull")
	e.alert("t3", "a", `{"team":"tg","cluster":"t3","queue":"q1"}`, "")
	e.notify("t3", "first notification")
	eventually(t, "the third post", func() bool { return len(e.messages(faketelegram.ChannelID)) == 3 })
	p3 := e.messages(faketelegram.ChannelID)[2].ID
	eventually(t, "the early copy to attach the Thread", func() bool { return e.threadState(p3) == "attached" })
	e.fake(http.MethodPut, e.ftg+"/config", fmt.Sprintf(`{"copy_delay_ms":%d}`, devmode.TelegramCopyDelay.Milliseconds()))

	// C-14.AC-16: the copy of the first post is deleted; the next reply is refused, sent again without the link, the
	// Thread is not attached with a thread_not_attached delivery event and the Destination stays healthy; the reply
	// after it follows the chain.
	e.fake(http.MethodDelete, fmt.Sprintf("%s/chats/%d/messages/%d", e.ftg, faketelegram.GroupID, cp.ID), "")
	e.command(g, "unsnooze")
	replies = len(e.botReplies())
	e.alert("t1", "d", `{"team":"tg","cluster":"t1","domain":"d.example.org"}`, "")
	e.notify("t1", "new alerts added")
	advance(t, r, 61)
	eventually(t, "the reply after the lost Thread", func() bool {
		for _, m := range e.botReplies()[replies:] {
			if strings.Contains(m.Text, "d.example.org") {
				return true
			}
		}
		return false
	})
	var lost faketelegram.Message
	for _, m := range e.botReplies()[replies:] {
		if strings.Contains(m.Text, "d.example.org") {
			lost = m
		}
	}
	refused := 0
	for _, q := range e.requests() {
		if strings.HasSuffix(q.Path, "/sendMessage") && q.Status == http.StatusBadRequest &&
			strings.Contains(q.Body, fmt.Sprintf(`"reply_parameters":{"message_id":%d}`, cp.ID)) {
			refused++
		}
	}
	tl := e.timeline(g, "&kind=delivery")
	if lost.ReplyParameters != nil || refused != 1 || !e.threadNotAttached(g) || len(tl) == 0 ||
		tl[0].DeliveryEvent != "thread_not_attached" || e.health(dest).State != "healthy" {
		t.Fatalf("lost thread: reply %+v, refused %d, events %v, health %+v", lost, refused, deliveryEvents(tl),
			e.health(dest))
	}
	replies = len(e.botReplies())
	e.alert("t1", "e", `{"team":"tg","cluster":"t1","domain":"e.example.org"}`, "")
	e.notify("t1", "new alerts added")
	advance(t, r, 61)
	eventually(t, "the next link of the chain", func() bool { return len(e.botReplies()) > replies })
	if next := e.botReplies()[len(e.botReplies())-1]; replyTo(next) == 0 || replyTo(next) == cp.ID {
		t.Errorf("after the lost Thread %+v", next)
	}

	// Short-lived pruning: a day later the copies are gone and counted.
	if n := h.count(t, `SELECT count(*) FROM telegram_post_copies`); n != 3 {
		t.Errorf("%d copies buffered", n)
	}
	advance(t, r, 90000)
	eventually(t, "the copies to be pruned", func() bool {
		return h.count(t, `SELECT count(*) FROM telegram_post_copies`) == 0
	})
	if v := r.Metric(t, `muster_short_lived_rows_pruned_total{table="telegram_post_copies"}`); v != "3" {
		t.Errorf("pruned metric %q", v)
	}
}

// answers are the answers to presses the fake Telegram server recorded.
func (e *tgd) answers() []faketelegram.Answer {
	e.t.Helper()
	var out []faketelegram.Answer
	decode(e.t, call(e.t, http.MethodGet, e.ftg+"/answers", ""), &out)
	return out
}

// press presses the button label of the message id of the channel as the Telegram account from, with extra fields of
// the press such as `,"pressed_ms_ago":14000`, and returns the answer to it once one is recorded.
func (e *tgd) press(id int64, from int, label, extra string) faketelegram.Answer {
	e.t.Helper()
	before := len(e.answers())
	e.fake(http.MethodPost, e.ftg+"/press", fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d,"username":"u%d"},
		"button":%q%s}`, faketelegram.ChannelID, id, from, from, label, extra))
	eventually(e.t, "the answer to "+label, func() bool { return len(e.answers()) > before })
	return e.answers()[before]
}

// keyboardOf waits until the channel post id shows the keyboard want.
func (e *tgd) keyboardOf(id int64, want ...string) {
	e.t.Helper()
	eventually(e.t, fmt.Sprintf("the keyboard %v", want), func() bool {
		for _, m := range e.messages(faketelegram.ChannelID) {
			if m.ID == id {
				return slices.Equal(buttons(e.t, m.ReplyMarkup), want)
			}
		}
		return false
	})
}

// lastIndex is the index of the last request to the method of the Bot API, -1 for none.
func lastIndex(reqs []fakeserver.Request, method string) int {
	for i := len(reqs) - 1; i >= 0; i-- {
		if strings.HasSuffix(reqs[i].Path, "/"+method) {
			return i
		}
	}
	return -1
}

// setMode switches the demo Telegram Connection to the update mode.
func (e *tgd) setMode(mode string) {
	e.t.Helper()
	c := e.api.json(http.MethodGet, "/api/v1/connections/"+e.conn, "", http.StatusOK)
	limiter, _ := json.Marshal(c["limiter"])
	body := fmt.Sprintf(`{"type":"telegram","name":%q,"bot_api_base_url":%q,"update_mode":%q,"proxy":{"enabled":false},
		"limiter":%s}`, c["name"], c["bot_api_base_url"], mode, limiter)
	if m := e.api.json(http.MethodPut, "/api/v1/connections/"+e.conn, body, http.StatusOK, "If-Match",
		c["etag"].(string)); m["update_mode"] != mode {
		e.t.Fatalf("update %v", m)
	}
}

// TestTelegramPresses is S-067 against `muster dev` and the fake Telegram server: a press answered before the Root
// message is edited (C-14.AC-18, FR-5) by the linked Responder with the Transport telegram (C-14.FR-4, C-10.FR-3), a
// Snooze for the pressed duration (C-10.FR-6), an account without an Account link (C-14.AC-9, C-10.FR-11), a valid
// button pressed on another message (C-14.FR-4), answers just before and after the fake's 15-second deadline (F-010),
// a press in the webhook mode (C-14.AC-11) and a press after the Connection received no updates for longer than
// telegram.press_max_age (C-14.AC-4).
func TestTelegramPresses(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newTGD(t, h, r)
	a := e.create("alerts", "@muster_alerts", 100)
	if a.status != http.StatusCreated {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	var created struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	decode(t, a, &created)
	e.route("tg", "tg", []string{created.Destination.ID}, nil)
	e.api.json(http.MethodPost, "/api/v1/users", `{"name":"bob","login":"bob","role":"responder"}`, http.StatusCreated)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		created_at) SELECT 1, 'AK0000000000T1', u.id, 'telegram', c.id, '5001', 'bob_tg', now() FROM users u,
		connections c WHERE u.login = 'bob' AND c.name = '`+devmode.TelegramConnectionName+`'`)

	e.group("t1", "CertExpiry")
	e.alert("t1", "a", `{"team":"tg","cluster":"p1","domain":"a.example.org"}`, "")
	e.notify("t1", "first notification")
	g := e.groupOf("p1")
	eventually(t, "the channel post", func() bool { return len(e.messages(faketelegram.ChannelID)) == 1 })
	post := e.messages(faketelegram.ChannelID)[0].ID
	e.keyboardOf(post, "Ack", "Resolve", "Snooze 1 h", "Snooze 4 h", "Snooze 24 h")

	// C-14.AC-18, FR-4, C-10.FR-3: the answer first, then the edit with the keyboard of the new state.
	if got := e.press(post, 5001, "Ack", ""); got.Text != "Done: Acknowledge" || !got.OK {
		t.Fatalf("answer %+v", got)
	}
	e.keyboardOf(post, "Unack", "Resolve", "Snooze 1 h", "Snooze 4 h", "Snooze 24 h")
	reqs := e.requests()
	if answered, edited := lastIndex(reqs, "answerCallbackQuery"), lastIndex(reqs, "editMessageText"); answered < 0 ||
		edited < answered {
		t.Errorf("answerCallbackQuery at %d, editMessageText at %d", answered, edited)
	}
	if first := e.timeline(g, "")[0]; first.Event != "acknowledged" || first.Actor.Transport != "telegram" ||
		first.Actor.Name != "bob" {
		t.Errorf("timeline %+v", first)
	}

	// C-10.FR-6: a Snooze button snoozes for its duration.
	if got := e.press(post, 5001, "Snooze 4 h", ""); got.Text != "Done: Snooze" {
		t.Errorf("snooze answer %+v", got)
	}
	e.keyboardOf(post, "Ack", "Unsnooze", "Resolve")
	ag := e.alertGroup(g)
	until, err := time.Parse(time.RFC3339, fmt.Sprint(ag["snooze_until"]))
	if now := readClock(t, r).Now; ag["status"] != "snoozed" || err != nil ||
		until.Sub(now) < 4*time.Hour-time.Minute || until.Sub(now) > 4*time.Hour {
		t.Errorf("after the snooze %v %v", ag["status"], ag["snooze_until"])
	}

	// C-14.AC-9, C-10.FR-11: an account without an Account link changes nothing.
	if got := e.press(post, 6001, "Unsnooze", ""); !strings.HasPrefix(got.Text,
		"Your Telegram account is not linked to Muster. Link it in your profile: http") ||
		!strings.HasSuffix(got.Text, "/profile") || e.alertGroup(g)["status"] != "snoozed" {
		t.Errorf("unlinked answer %+v", got)
	}

	// C-14.FR-4: a valid button pressed on another message changes nothing.
	if got := e.press(999999, 5001, "Unsnooze", fmt.Sprintf(`,"data_from":%d`, post)); got.Text !=
		"This button could not be verified; nothing was changed." || e.alertGroup(g)["status"] != "snoozed" {
		t.Errorf("unbound answer %+v", got)
	}

	// F-010: a press that arrived 13 s late is answered before the 15-second deadline; one that arrived 16 s late
	// runs its Command, and Telegram refuses its answer, which the log names without the token.
	if got := e.press(post, 5001, "Unsnooze", `,"pressed_ms_ago":13000`); !got.OK || got.Text != "Done: Unsnooze" ||
		got.AfterMs < 13000 || got.AfterMs >= 15000 {
		t.Errorf("an answer just before the deadline %+v", got)
	}
	e.keyboardOf(post, "Ack", "Resolve", "Snooze 1 h", "Snooze 4 h", "Snooze 24 h")
	if got := e.press(post, 5001, "Resolve", `,"pressed_ms_ago":16000`); got.OK ||
		got.Description != faketelegram.DescriptionQueryTooOld {
		t.Errorf("an answer after the deadline %+v", got)
	}
	eventually(t, "the resolution", func() bool { return e.alertGroup(g)["status"] == "resolved" })
	waitLog(t, r, "the refused answer", regexp.MustCompile(`"event":"telegram_press","connection":"`+e.conn+
		`","group":"`+g+`","command":"resolve","outcome":"done","error":"the press was not answered: Telegram `+
		`answered 400: Bad Request: query is too old`))

	// C-14.AC-11: in the webhook mode a press without the secret token header is refused, and the same press posted
	// by Telegram with it is processed like a polled one.
	e.setMode("webhook")
	e.group("t2", "QueueFull")
	e.alert("t2", "a", `{"team":"tg","cluster":"p2"}`, "")
	e.notify("t2", "first notification")
	g2 := e.groupOf("p2")
	eventually(t, "the second post", func() bool { return len(e.messages(faketelegram.ChannelID)) == 2 })
	post2 := e.messages(faketelegram.ChannelID)[1].ID
	data := ""
	for _, m := range e.messages(faketelegram.ChannelID) {
		if m.ID == post2 {
			var kb struct {
				InlineKeyboard [][]struct {
					CallbackData string `json:"callback_data"`
				} `json:"inline_keyboard"`
			}
			_ = json.Unmarshal(m.ReplyMarkup, &kb)
			data = kb.InlineKeyboard[0][0].CallbackData
		}
	}
	forged := fmt.Sprintf(`{"update_id":777777,"callback_query":{"id":"forged","from":{"id":5001},"message":
		{"message_id":%d,"chat":{"id":%d,"type":"channel"}},"data":%q}}`, post2, faketelegram.ChannelID, data)
	if a := call(t, http.MethodPost, r.Ingest+telegram.WebhookPath+e.conn, forged); a.status != http.StatusUnauthorized {
		t.Errorf("a press without the secret token header = %d", a.status)
	}
	if got := e.press(post2, 5001, "Ack", ""); got.Text != "Done: Acknowledge" || !got.OK {
		t.Errorf("webhook answer %+v", got)
	}
	if e.alertGroup(g2)["status"] != "acknowledged" {
		t.Errorf("after the webhook press %v", e.alertGroup(g2)["status"])
	}
	if !slices.ContainsFunc(e.requests(), func(q fakeserver.Request) bool {
		return strings.HasSuffix(q.Path, "/setWebhook")
	}) {
		t.Error("no webhook was set")
	}

	// C-14.AC-4: back in long polling, a press that arrives after the Connection received no updates for longer than
	// telegram.press_max_age changes nothing and is logged; the next press is handled.
	e.setMode("long_polling")
	e.keyboardOf(post2, "Unack", "Resolve", "Snooze 1 h", "Snooze 4 h", "Snooze 24 h")
	e.press(post2, 5001, "Snooze 4 h", "")
	e.keyboardOf(post2, "Ack", "Unsnooze", "Resolve")
	answers := len(e.answers())
	advance(t, r, 3700)
	e.fake(http.MethodPost, e.ftg+"/press", fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":5001},
		"button":"Unsnooze"}`, faketelegram.ChannelID, post2))
	waitLog(t, r, "the dropped press", regexp.MustCompile(`"event":"telegram_press_dropped","connection":"`+e.conn+
		`","gap_seconds":3[67]\d\d`))
	time.Sleep(time.Second)
	if len(e.answers()) != answers || e.alertGroup(g2)["status"] != "snoozed" {
		t.Errorf("the dropped press was answered or changed the Alert Group: %d answers, %v", len(e.answers()),
			e.alertGroup(g2)["status"])
	}
	if got := e.press(post2, 5001, "Unsnooze", ""); got.Text != "Done: Unsnooze" {
		t.Errorf("the press after the dropped one %+v", got)
	}

	if strings.Contains(r.Output(), "dev-telegram-token") {
		t.Error("the bot token reached the log")
	}
	if n := strings.Count(r.Output(), `"event":"telegram_press",`); n != 9 {
		t.Errorf("%d telegram_press lines", n)
	}
}
