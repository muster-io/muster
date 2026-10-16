// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package faketelegram_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

const token = "777001:test-token"

func start(t *testing.T) (*faketelegram.Fake, string) {
	t.Helper()
	f := faketelegram.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(t.Context()) })
	return f, f.URL()
}

type answer struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func call(t *testing.T, method, url, body string) (int, string, answer) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var a answer
	_ = json.Unmarshal(b, &a)
	return resp.StatusCode, string(b), a
}

func TestDryProbeIsAlways401(t *testing.T) {
	_, base := start(t)
	status, body, a := call(t, http.MethodGet, base+"/bot0:x/getMe", "")
	if status != http.StatusUnauthorized || a.OK || a.ErrorCode != 401 || a.Description != "Unauthorized" {
		t.Fatalf("dry probe = %d %s", status, body)
	}
	status, _, a = call(t, http.MethodGet, base+"/bot"+token+"/getMe", "")
	var me faketelegram.User
	_ = json.Unmarshal(a.Result, &me)
	if status != http.StatusOK || !a.OK || me.Username != faketelegram.BotUsername || me.ID != faketelegram.BotID {
		t.Fatalf("getMe = %d %+v", status, a)
	}
}

func TestRevokedTokens(t *testing.T) {
	_, base := start(t)
	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"revoked_tokens":["`+token+`"]}`); s != http.StatusOK {
		t.Fatalf("config = %d %s", s, b)
	}
	if s, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getMe", ""); s != http.StatusUnauthorized || a.OK {
		t.Fatalf("revoked getMe = %d", s)
	}
	if s, _, _ := call(t, http.MethodGet, base+"/botother:token/getMe", ""); s != http.StatusOK {
		t.Fatalf("other token = %d", s)
	}
}

func TestPathPrefixAndHTMLMode(t *testing.T) {
	f, base := start(t)
	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"path_prefix":"/k3x9/"}`); s != http.StatusOK ||
		f.Config().PathPrefix != "/k3x9" {
		t.Fatalf("config = %d %s", s, b)
	}
	if s, _, _ := call(t, http.MethodGet, base+"/k3x9/bot"+token+"/getMe", ""); s != http.StatusOK {
		t.Fatalf("under the prefix = %d", s)
	}
	for _, p := range []string{"/bot" + token + "/getMe", "/other/bot0:x/getMe", "/k3x9/getMe"} {
		if s, _, a := call(t, http.MethodGet, base+p, ""); s != http.StatusNotFound || a.ErrorCode != 404 {
			t.Fatalf("%s = %d", p, s)
		}
	}
	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"path_prefix":"","mode":"html"}`); s != http.StatusOK {
		t.Fatalf("config = %d %s", s, b)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/bot0:x/getMe", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
		string(b) != faketelegram.HTMLPage {
		t.Fatalf("html mode = %d %q", resp.StatusCode, b)
	}
	for _, bad := range []string{`{"mode":"xml"}`, `{"path_prefix":"k3x9"}`, `{"unknown":1}`} {
		if s, _, _ := call(t, http.MethodPut, base+"/_fake/config", bad); s != http.StatusBadRequest {
			t.Fatalf("config %s = %d", bad, s)
		}
	}
}

func TestGetUpdatesWithOffsetAndAllowedUpdates(t *testing.T) {
	f, base := start(t)
	for _, u := range []string{`{"message":{"message_id":1,"chat":{"id":1,"type":"private"}}}`,
		`{"chat_member":{"date":1}}`, `{"update_id":40,"callback_query":{"id":"q"}}`} {
		if s, b, _ := call(t, http.MethodPost, base+"/_fake/updates", `{"token":"`+token+`","update":`+u+`}`); s !=
			http.StatusOK {
			t.Fatalf("enqueue = %d %s", s, b)
		}
	}
	_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates",
		`{"timeout":0,"allowed_updates":["message","callback_query"]}`)
	var got []map[string]any
	_ = json.Unmarshal(a.Result, &got)
	if !a.OK || len(got) != 2 || got[0]["update_id"] != float64(1) || got[1]["update_id"] != float64(40) {
		t.Fatalf("getUpdates = %+v", got)
	}
	if f.Pending(token) != 2 {
		t.Fatalf("pending = %d, want 2 unconfirmed", f.Pending(token))
	}
	_, _, a = call(t, http.MethodGet, base+"/bot"+token+"/getUpdates?offset=41&timeout=0", "")
	if string(a.Result) != "[]" || f.Pending(token) != 0 {
		t.Fatalf("after the offset = %s, pending %d", a.Result, f.Pending(token))
	}
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"offset":"x"}`); s != 400 {
		t.Fatalf("bad offset = %d", s)
	}
}

func TestLongPollWaitsForAnUpdate(t *testing.T) {
	_, base := start(t)
	done := make(chan answer, 1)
	go func() {
		_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":5}`)
		done <- a
	}()
	waitPolling(t, base)
	call(t, http.MethodPost, base+"/_fake/updates", `{"token":"`+token+`","update":{"message":{"text":"hi"}}}`)
	select {
	case a := <-done:
		if !a.OK || !strings.Contains(string(a.Result), `"hi"`) {
			t.Fatalf("long poll = %+v", a)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the long poll did not end with the update")
	}
}

// waitPolling waits until a getUpdates is recorded and has not been answered yet.
func waitPolling(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, body, _ := call(t, http.MethodGet, base+"/_fake/requests", "")
		var reqs []struct {
			Path   string `json:"path"`
			Status int    `json:"status"`
		}
		_ = json.Unmarshal([]byte(body), &reqs)
		for _, r := range reqs {
			if strings.HasSuffix(r.Path, "/getUpdates") && r.Status == 0 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no long poll is running")
}

func TestSecondPollerEndsTheFirstWith409(t *testing.T) {
	_, base := start(t)
	first := make(chan answer, 1)
	go func() {
		_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":10}`)
		first <- a
	}()
	waitPolling(t, base)
	second := make(chan answer, 1)
	go func() {
		_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":1}`)
		second <- a
	}()
	a := <-first
	if a.OK || a.ErrorCode != http.StatusConflict ||
		!strings.HasPrefix(a.Description, "Conflict: terminated by other getUpdates request") {
		t.Fatalf("first poller = %+v", a)
	}
	if a := <-second; !a.OK {
		t.Fatalf("second poller = %+v", a)
	}
}

func TestConflictEndpoint(t *testing.T) {
	_, base := start(t)
	// Without a running poll, the next getUpdates gets the 409.
	_, body, _ := call(t, http.MethodPost, base+"/_fake/conflict", `{"token":"`+token+`"}`)
	if !strings.Contains(body, `"ended_running_poll":false`) {
		t.Fatalf("conflict = %s", body)
	}
	if s, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":0}`); s != 409 ||
		a.ErrorCode != 409 {
		t.Fatalf("owed conflict = %d", s)
	}
	running := make(chan int, 1)
	go func() {
		s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":10}`)
		running <- s
	}()
	waitPolling(t, base)
	_, body, _ = call(t, http.MethodPost, base+"/_fake/conflict", `{"token":"`+token+`"}`)
	if s := <-running; s != http.StatusConflict || !strings.Contains(body, `"ended_running_poll":true`) {
		t.Fatalf("running poll = %d, %s", s, body)
	}
	if s, _, _ := call(t, http.MethodPost, base+"/_fake/conflict", `{}`); s != http.StatusBadRequest {
		t.Fatalf("conflict without token = %d", s)
	}
}

func TestWebhookDelivery(t *testing.T) {
	f, base := start(t)
	var mu sync.Mutex
	var got []string
	var headers []string
	accept := true
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		headers = append(headers, r.Header.Get(faketelegram.SecretTokenHeader))
		if !accept {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got = append(got, string(b))
	}))
	defer hook.Close()
	running := make(chan answer, 1)
	go func() {
		_, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":10}`)
		running <- a
	}()
	waitPolling(t, base)
	if s, b, _ := call(t, http.MethodPost, base+"/bot"+token+"/setWebhook", `{"url":"`+hook.URL+
		`/hook","secret_token":"s3cr3t_-x","allowed_updates":["message"],"max_connections":1}`); s != 200 {
		t.Fatalf("setWebhook = %d %s", s, b)
	}
	if a := <-running; a.ErrorCode != 409 || a.Description != "Conflict: terminated by setWebhook request" {
		t.Fatalf("running poll after setWebhook = %+v", a)
	}
	if s, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":0}`); s != 409 ||
		!strings.Contains(a.Description, "webhook is active") {
		t.Fatalf("getUpdates with a webhook = %d %+v", s, a)
	}
	_, _, a := call(t, http.MethodGet, base+"/bot"+token+"/getWebhookInfo", "")
	var info faketelegram.WebhookInfo
	_ = json.Unmarshal(a.Result, &info)
	if info.URL != hook.URL+"/hook" || info.PendingUpdateCount != 0 || info.MaxConnections != 1 {
		t.Fatalf("getWebhookInfo = %+v", info)
	}
	call(t, http.MethodPost, base+"/_fake/updates", `{"token":"`+token+`","update":{"update_id":9001,"message":{}}}`)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	mu.Lock()
	if headers[0] != "s3cr3t_-x" || !strings.Contains(got[0], `"update_id":9001`) {
		t.Fatalf("posted %v with %v", got, headers)
	}
	accept = false
	mu.Unlock()
	call(t, http.MethodPost, base+"/_fake/updates", `{"token":"`+token+`","update":{"message":{}}}`)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(headers) == 2 })
	waitFor(t, func() bool { return f.Pending(token) == 1 })
	if wh := f.Webhooks()[token]; wh.SecretToken != "s3cr3t_-x" {
		t.Fatalf("webhooks = %+v", f.Webhooks())
	}
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/setWebhook", `{"url":"x","secret_token":"a b"}`); s !=
		400 {
		t.Fatalf("bad secret = %d", s)
	}
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/deleteWebhook", ""); s != 200 ||
		len(f.Webhooks()) != 0 {
		t.Fatalf("deleteWebhook = %d", s)
	}
	if s, _, a := call(t, http.MethodPost, base+"/bot"+token+"/getUpdates", `{"timeout":0}`); s != 200 ||
		!strings.Contains(string(a.Result), "update_id") {
		t.Fatalf("getUpdates after deleteWebhook = %d %s", s, a.Result)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("the condition did not hold in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnknownMethodsAndPaths(t *testing.T) {
	_, base := start(t)
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/sendPhoto", `{}`); s != http.StatusNotImplemented {
		t.Fatalf("sendPhoto = %d", s)
	}
	if s, _, _ := call(t, http.MethodDelete, base+"/bot"+token+"/getMe", ""); s != http.StatusNotFound {
		t.Fatalf("DELETE = %d", s)
	}
	if s, _, _ := call(t, http.MethodPost, base+"/bot"+token+"/getMe", `[`); s != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d", s)
	}
	if s, _, _ := call(t, http.MethodPost, base+"/_fake/updates", `{"token":"","update":{}}`); s !=
		http.StatusBadRequest {
		t.Fatalf("update without token = %d", s)
	}
}

// bot calls method of the Bot API with a JSON body.
func bot(t *testing.T, base, method, body string) (int, answer) {
	t.Helper()
	status, _, a := call(t, http.MethodPost, base+"/bot"+token+"/"+method, body)
	return status, a
}

func member(t *testing.T, base string, chat int64, status string, post, edit bool) {
	t.Helper()
	body, _ := json.Marshal(faketelegram.Member{Status: status, CanPostMessages: post, CanEditMessages: edit})
	if s, b, _ := call(t, http.MethodPut, fmt.Sprintf("%s/_fake/chats/%d/members/%d", base, chat, faketelegram.BotID),
		string(body)); s != http.StatusOK {
		t.Fatalf("set member = %d %s", s, b)
	}
}

// F-001: a bot joins a channel only as an admin; added without rights, its status stays left.
func TestBotJoinsChannelOnlyAsAdmin(t *testing.T) {
	_, base := start(t)
	var m struct {
		Status          string `json:"status"`
		CanPostMessages bool   `json:"can_post_messages"`
		CanEditMessages bool   `json:"can_edit_messages"`
	}
	read := func() {
		t.Helper()
		status, a := bot(t, base, "getChatMember", fmt.Sprintf(`{"chat_id":"@muster_alerts","user_id":%d}`,
			faketelegram.BotID))
		if status != http.StatusOK || json.Unmarshal(a.Result, &m) != nil {
			t.Fatalf("getChatMember = %d %+v", status, a)
		}
	}
	read()
	if m.Status != faketelegram.StatusAdministrator || !m.CanPostMessages || !m.CanEditMessages {
		t.Fatalf("default = %+v", m)
	}
	member(t, base, faketelegram.ChannelID, faketelegram.StatusLeft, false, false)
	read()
	if m.Status != faketelegram.StatusLeft {
		t.Fatalf("without admin rights = %+v", m)
	}
	if s, a := bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"x"}`); s != http.StatusForbidden ||
		a.Description != "Forbidden: bot is not a member of the channel chat" {
		t.Fatalf("send while left = %d %+v", s, a)
	}
	member(t, base, faketelegram.ChannelID, faketelegram.StatusAdministrator, false, true)
	if s, a := bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"x"}`); s != http.StatusBadRequest ||
		a.Description != faketelegram.DescriptionNeedAdmin {
		t.Fatalf("send without can_post_messages = %d %+v", s, a)
	}
}

// F-002: getChat names linked_chat_id only for a channel with comments enabled.
func TestLinkedChatOnlyWithComments(t *testing.T) {
	_, base := start(t)
	var ch faketelegram.Chat
	get := func(chat string) {
		t.Helper()
		status, a := bot(t, base, "getChat", `{"chat_id":`+chat+`}`)
		ch = faketelegram.Chat{}
		if status != http.StatusOK || json.Unmarshal(a.Result, &ch) != nil {
			t.Fatalf("getChat %s = %d %+v", chat, status, a)
		}
	}
	get(`"@muster_alerts"`)
	if ch.ID != faketelegram.ChannelID || ch.Type != "channel" || ch.LinkedChatID != faketelegram.GroupID {
		t.Fatalf("channel = %+v", ch)
	}
	get(`"@no_comments"`)
	if ch.ID != faketelegram.NoCommentsID || ch.LinkedChatID != 0 {
		t.Fatalf("channel without comments = %+v", ch)
	}
	get("-1001000000002")
	if ch.Title != faketelegram.GroupTitle || ch.Type != "supergroup" {
		t.Fatalf("group = %+v", ch)
	}
	if s, b, _ := call(t, http.MethodPut, base+"/_fake/chats/@no_comments", `{"linked_chat_id":-1001000000002}`); s !=
		http.StatusOK {
		t.Fatalf("enable comments = %d %s", s, b)
	}
	get(`"@no_comments"`)
	if ch.LinkedChatID != faketelegram.GroupID {
		t.Fatalf("after enabling comments = %+v", ch)
	}
	get(`"@muster_alerts"`)
	if ch.LinkedChatID != 0 {
		t.Fatalf("the group moved to the other channel, yet = %+v", ch)
	}
	if s, a := bot(t, base, "getChat", `{"chat_id":"@nobody"}`); s != http.StatusBadRequest ||
		a.Description != faketelegram.DescriptionChatNotFound {
		t.Fatalf("unknown chat = %d %+v", s, a)
	}
}

// F-003: a bot outside the discussion group gets 403 from getChatMember there.
func TestBotOutsideGroupIsForbidden(t *testing.T) {
	_, base := start(t)
	member(t, base, faketelegram.GroupID, faketelegram.StatusLeft, false, false)
	s, a := bot(t, base, "getChatMember", fmt.Sprintf(`{"chat_id":-1001000000002,"user_id":%d}`, faketelegram.BotID))
	if s != http.StatusForbidden || a.Description != "Forbidden: bot is not a member of the supergroup chat" {
		t.Fatalf("getChatMember outside the group = %d %+v", s, a)
	}
	member(t, base, faketelegram.GroupID, faketelegram.StatusMember, false, false)
	s, a = bot(t, base, "getChatMember", fmt.Sprintf(`{"chat_id":-1001000000002,"user_id":%d}`, faketelegram.BotID))
	if s != http.StatusOK || !strings.Contains(string(a.Result), `"status":"member"`) {
		t.Fatalf("getChatMember as a member = %d %+v", s, a)
	}
}

const keyboard = `{"inline_keyboard":[[{"text":"Ack","callback_data":"a"}]]}`

// F-011 and F-017: an edit without reply_markup removes the keyboard; an edit that changes nothing is refused.
func TestEditsAndKeyboards(t *testing.T) {
	f, base := start(t)
	s, a := bot(t, base, "sendMessage", `{"chat_id":"@muster_alerts","text":"<b>one</b>","parse_mode":"HTML",
		"reply_markup":`+keyboard+`}`)
	if s != http.StatusOK {
		t.Fatalf("send = %d %+v", s, a)
	}
	edit := func(body string) (int, answer) {
		return bot(t, base, "editMessageText", `{"chat_id":-1001000000001,"message_id":1,`+body+`}`)
	}
	if s, a := edit(`"text":"<b>one</b>","parse_mode":"HTML","reply_markup":` + keyboard); s != http.StatusBadRequest ||
		a.Description != faketelegram.DescriptionNotModified {
		t.Fatalf("unchanged edit = %d %+v", s, a)
	}
	if s, a := edit(`"text":"<b>two</b>","parse_mode":"HTML","reply_markup":` + keyboard); s != http.StatusOK {
		t.Fatalf("edit with keyboard = %d %+v", s, a)
	}
	if s, a := edit(`"text":"<b>three</b>","parse_mode":"HTML"`); s != http.StatusOK {
		t.Fatalf("edit without keyboard = %d %+v", s, a)
	}
	msgs := f.Messages(faketelegram.ChannelID)
	if len(msgs) != 1 || len(msgs[0].Edits) != 2 || msgs[0].Text != "<b>three</b>" || msgs[0].ReplyMarkup != nil ||
		msgs[0].Edits[0].ReplyMarkup == nil {
		t.Fatalf("messages = %+v", msgs)
	}
	if s, a := bot(t, base, "editMessageText", `{"chat_id":-1001000000001,"message_id":9,"text":"x"}`); s !=
		http.StatusBadRequest || a.Description != faketelegram.DescriptionEditNotFound {
		t.Fatalf("edit of a missing message = %d %+v", s, a)
	}
	member(t, base, faketelegram.ChannelID, faketelegram.StatusAdministrator, true, false)
	if s, a := edit(`"text":"four"`); s != http.StatusBadRequest || a.Description != faketelegram.DescriptionNeedAdmin {
		t.Fatalf("edit without can_edit_messages = %d %+v", s, a)
	}
}

// F-012, F-013: a channel post notifies both accounts, without sound with disable_notification, and its automatic copy
// notifies member with sound even then; an edit notifies nobody.
func TestNotificationsOfChannelPosts(t *testing.T) {
	f, base := start(t)
	bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"loud"}`)
	bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"quiet","disable_notification":true}`)
	bot(t, base, "editMessageText", `{"chat_id":-1001000000001,"message_id":1,"text":"edited"}`)
	bot(t, base, "sendMessage", `{"chat_id":-1001000000002,"text":"group"}`)
	want := []faketelegram.Notification{
		{Account: "member", Chat: faketelegram.ChannelID, MessageID: 1, Sound: true},
		{Account: "subscriber", Chat: faketelegram.ChannelID, MessageID: 1, Sound: true},
		{Account: "member", Chat: faketelegram.GroupID, MessageID: 1, Sound: true},
		{Account: "member", Chat: faketelegram.ChannelID, MessageID: 2, Sound: false},
		{Account: "subscriber", Chat: faketelegram.ChannelID, MessageID: 2, Sound: false},
		{Account: "member", Chat: faketelegram.GroupID, MessageID: 2, Sound: true},
		{Account: "member", Chat: faketelegram.GroupID, MessageID: 3, Sound: true},
	}
	if got := f.Notifications(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("notifications = %+v", got)
	}
	_, body, _ := call(t, http.MethodGet, base+"/_fake/notifications", "")
	if !strings.Contains(body, `"account":"subscriber"`) {
		t.Fatalf("GET notifications = %s", body)
	}
}

// F-016: sends and edits share a budget of 20 per minute per chat and bot; the 21st answers 429 with the exact
// retry_after, while another chat and another bot have their own budgets.
func TestChatBudget(t *testing.T) {
	f, base := start(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	f.SetClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	})
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	for i := range 20 {
		method, body := "sendMessage", `{"chat_id":-1001000000002,"text":"m`+fmt.Sprint(i)+`"}`
		if i%2 == 1 {
			method, body = "editMessageText", `{"chat_id":-1001000000002,"message_id":1,"text":"e`+fmt.Sprint(i)+`"}`
		}
		if s, a := bot(t, base, method, body); s != http.StatusOK {
			t.Fatalf("call %d = %d %+v", i+1, s, a)
		}
		advance(1500 * time.Millisecond)
	}
	_, body, a := call(t, http.MethodPost, base+"/bot"+token+"/sendMessage",
		`{"chat_id":-1001000000002,"text":"over"}`)
	var p struct {
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	_ = json.Unmarshal([]byte(body), &p)
	if a.ErrorCode != http.StatusTooManyRequests || p.Parameters.RetryAfter != 30 ||
		a.Description != "Too Many Requests: retry after 30" {
		t.Fatalf("21st call = %s", body)
	}
	if s, _ := bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"other chat"}`); s != http.StatusOK {
		t.Fatalf("another chat has its own budget: %d", s)
	}
	if s, _ := as(t, base, faketelegram.SecondBotToken, "sendMessage", `{"chat_id":-1001000000002,"text":"bot B"}`); s !=
		http.StatusOK {
		t.Fatalf("another bot has its own budget in the chat: %d", s)
	}
	advance(30 * time.Second)
	if s, a := bot(t, base, "sendMessage", `{"chat_id":-1001000000002,"text":"again"}`); s != http.StatusOK {
		t.Fatalf("after retry_after = %d %+v", s, a)
	}
}

// Telegram HTML is parsed: a known tag that is not closed, an unknown tag and a bare entity are refused; the length
// counts the visible text; callback_data is at most 64 bytes.
func TestMessageContent(t *testing.T) {
	_, base := start(t)
	for body, want := range map[string]string{
		`"text":"<b>x","parse_mode":"HTML"`:             `Bad Request: can't parse entities: can't find end tag`,
		`"text":"<table>x</table>","parse_mode":"HTML"`: `Bad Request: can't parse entities: unsupported start tag`,
		`"text":"a & b","parse_mode":"HTML"`:            `Bad Request: can't parse entities: unsupported entity`,
		`"text":"a > b","parse_mode":"HTML"`:            `Bad Request: can't parse entities: character '>'`,
		`"text":"<b>x</i>","parse_mode":"HTML"`:         `Bad Request: can't parse entities: unmatched end tag`,
		`"text":"<b></b>","parse_mode":"HTML"`:          faketelegram.DescriptionTextEmpty,
		`"text":"` + strings.Repeat("x", 4097) + `"`:    faketelegram.DescriptionTooLong,
		`"text":"x","reply_markup":{"inline_keyboard":[[{"text":"a","callback_data":"` + strings.Repeat("d", 65) + `"}]]}`: faketelegram.DescriptionButtonData,
	} {
		if s, a := bot(t, base, "sendMessage", `{"chat_id":-1001000000001,`+body+`}`); s != http.StatusBadRequest ||
			!strings.HasPrefix(a.Description, want) {
			t.Errorf("%.60s = %d %q, want %q", body, s, a.Description, want)
		}
	}
	long := strings.Repeat("&lt;", 4096)
	if s, a := bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"<blockquote expandable>`+long+
		`</blockquote>","parse_mode":"HTML"}`); s != http.StatusOK {
		t.Fatalf("4,096 visible characters = %d %+v", s, a)
	}
	if s, a := bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"&#64;&#x40; <a href=\"x\">y</a>",
		"parse_mode":"HTML","reply_parameters":{"message_id":7}}`); s != http.StatusBadRequest ||
		a.Description != faketelegram.DescriptionReplyNotFound {
		t.Fatalf("reply to a missing message = %d %+v", s, a)
	}
}

// F-066 and F-067: a send or an edit whose inline button has a style is refused unless the style is primary, danger,
// success or default, the whole call failing; the style it accepts is kept in the message's reply_markup and listed by
// the control endpoint.
func TestButtonStyles(t *testing.T) {
	f, base := start(t)
	markup := func(style string) string {
		return `"reply_markup":{"inline_keyboard":[[{"text":"a","callback_data":"a"},` +
			`{"text":"b","callback_data":"b","style":` + style + `}]]}`
	}
	accepted := []string{`"primary"`, `"danger"`, `"success"`, `"default"`}
	for i, style := range accepted {
		if s, a := bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"x",`+markup(style)+`}`); s !=
			http.StatusOK {
			t.Fatalf("send with the style %s = %d %+v", style, s, a)
		}
		if s, a := bot(t, base, "editMessageText", fmt.Sprintf(`{"chat_id":-1001000000001,"message_id":1,`+
			`"text":"e%d",%s}`, i, markup(style))); s != http.StatusOK {
			t.Fatalf("edit with the style %s = %d %+v", style, s, a)
		}
	}
	msgs := f.Messages(faketelegram.ChannelID)
	if len(msgs) != len(accepted) || len(msgs[0].Edits) != len(accepted) {
		t.Fatalf("messages = %+v", msgs)
	}
	// Message i was sent with style i; the first one was then edited to each style in turn, ending with default.
	for i, style := range accepted {
		if !strings.Contains(string(msgs[0].Edits[i].ReplyMarkup), `"style":`+style) ||
			i > 0 && !strings.Contains(string(msgs[i].ReplyMarkup), `"style":`+style) {
			t.Errorf("the style %s is not kept: %s, edit %s", style, msgs[i].ReplyMarkup, msgs[0].Edits[i].ReplyMarkup)
		}
	}
	if !strings.Contains(string(msgs[0].ReplyMarkup), `"style":"default"`) {
		t.Errorf("the edited keyboard = %s", msgs[0].ReplyMarkup)
	}
	if _, body, _ := call(t, http.MethodGet, base+"/_fake/messages?chat=@muster_alerts", ""); !strings.Contains(body,
		`"style":"danger"`) {
		t.Errorf("listed messages = %s", body)
	}
	for _, style := range []string{`"neutral"`, `""`, `"Primary"`, `42`, `null`} {
		for _, c := range []struct{ method, body string }{
			{"sendMessage", `{"chat_id":-1001000000002,"text":"x",` + markup(style) + `}`},
			{"editMessageText", `{"chat_id":-1001000000001,"message_id":1,"text":"y",` + markup(style) + `}`},
		} {
			if s, a := bot(t, base, c.method, c.body); s != http.StatusBadRequest || a.ErrorCode != http.StatusBadRequest ||
				a.Description != faketelegram.DescriptionButtonStyle {
				t.Errorf("%s with the style %s = %d %+v", c.method, style, s, a)
			}
		}
	}
	if msgs := f.Messages(faketelegram.ChannelID); len(msgs) != len(accepted) || len(msgs[0].Edits) != len(accepted) ||
		msgs[0].Text != "e3" {
		t.Errorf("a refused call changed the messages: %+v", msgs)
	}
}

// The control endpoints list messages by chat and refuse what they cannot apply.
func TestChatControl(t *testing.T) {
	_, base := start(t)
	bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"p","disable_notification":true,
		"reply_markup":`+keyboard+`}`)
	_, body, _ := call(t, http.MethodGet, base+"/_fake/messages?chat=@muster_alerts", "")
	var msgs []faketelegram.Message
	if err := json.Unmarshal([]byte(body), &msgs); err != nil || len(msgs) != 1 || !msgs[0].DisableNotification ||
		!strings.Contains(string(msgs[0].ReplyMarkup), `"Ack"`) {
		t.Fatalf("messages = %s", body)
	}
	_, body, _ = call(t, http.MethodGet, base+"/_fake/messages", "")
	if !strings.Contains(body, `"text":"p"`) {
		t.Fatalf("all messages = %s", body)
	}
	_, body, _ = call(t, http.MethodGet, base+"/_fake/chats", "")
	if !strings.Contains(body, faketelegram.GroupTitle) {
		t.Fatalf("chats = %s", body)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/_fake/messages?chat=@nobody", ""},
		{http.MethodPut, "/_fake/chats/@nobody", `{"linked_chat_id":0}`},
		{http.MethodPut, "/_fake/chats/-1/members/1", `{"status":"member"}`},
		{http.MethodPut, "/_fake/chats/@muster_alerts/members/1", `{"status":"owner"}`},
		{http.MethodPut, "/_fake/chats/@muster_alerts/members/x", `{"status":"member"}`},
		{http.MethodPut, "/_fake/chats/@muster_alerts/members/1", `{`},
	} {
		if s, _, _ := call(t, c.method, base+c.path, c.body); s/100 != 4 {
			t.Errorf("%s %s = %d", c.method, c.path, s)
		}
	}
}

// updates reads the queued updates of the test bot and confirms them.
func updates(t *testing.T, base string) []map[string]json.RawMessage {
	t.Helper()
	_, a := bot(t, base, "getUpdates", `{"timeout":0}`)
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(a.Result, &out); err != nil {
		t.Fatalf("updates %s: %v", a.Result, err)
	}
	if len(out) > 0 {
		var id int64
		_ = json.Unmarshal(out[len(out)-1]["update_id"], &id)
		bot(t, base, "getUpdates", fmt.Sprintf(`{"timeout":0,"offset":%d}`, id+1))
	}
	return out
}

// copyMessage is the part of a message update that comment Threads use.
type copyMessage struct {
	MessageID int64 `json:"message_id"`
	Date      int64 `json:"date"`
	EditDate  int64 `json:"edit_date"`
	From      struct {
		ID int64 `json:"id"`
	} `json:"from"`
	SenderChat *struct {
		ID int64 `json:"id"`
	} `json:"sender_chat"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	IsAutomaticForward bool `json:"is_automatic_forward"`
	ForwardOrigin      *struct {
		Type string `json:"type"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		MessageID int64 `json:"message_id"`
	} `json:"forward_origin"`
	MessageThreadID int64           `json:"message_thread_id"`
	ReplyMarkup     json.RawMessage `json:"reply_markup"`
	ReplyToMessage  *copyMessage    `json:"reply_to_message"`
	Text            string          `json:"text"`
}

func messageOf(t *testing.T, u map[string]json.RawMessage, kind string) copyMessage {
	t.Helper()
	var m copyMessage
	if err := json.Unmarshal(u[kind], &m); err != nil || u[kind] == nil {
		t.Fatalf("no %s in %v", kind, u)
	}
	return m
}

func post(t *testing.T, base, text string) int64 {
	t.Helper()
	s, a := bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"`+text+`","reply_markup":`+keyboard+`}`)
	var m struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(a.Result, &m)
	if s != http.StatusOK || m.MessageID == 0 {
		t.Fatalf("post = %d %+v", s, a)
	}
	return m.MessageID
}

// F-003, F-005: the automatic copy of a post reaches an admin bot as a message in the discussion group from Telegram,
// on behalf of the channel, naming the post, with edit_date equal to its date and no keyboard; the bot gets no update
// about its own post. A bot that is only a member of the group, or with copies withheld, gets none, while the copy
// exists all the same.
func TestAutomaticCopy(t *testing.T) {
	f, base := start(t)
	p := post(t, base, "first")
	u := updates(t, base)
	if len(u) != 1 {
		t.Fatalf("updates %v", u)
	}
	m := messageOf(t, u[0], "message")
	if m.From.ID != faketelegram.TelegramUserID || m.SenderChat == nil || m.SenderChat.ID != faketelegram.ChannelID ||
		m.Chat.ID != faketelegram.GroupID || m.Chat.Type != "supergroup" || !m.IsAutomaticForward ||
		m.ForwardOrigin == nil || m.ForwardOrigin.Type != "channel" || m.ForwardOrigin.Chat.ID != faketelegram.ChannelID ||
		m.ForwardOrigin.MessageID != p || m.EditDate == 0 || m.EditDate != m.Date || m.ReplyMarkup != nil ||
		m.Text != "first" {
		t.Fatalf("copy %+v", m)
	}
	group := f.Messages(faketelegram.GroupID)
	if len(group) != 1 || group[0].ID != m.MessageID || !group[0].IsAutomaticForward || group[0].From == nil ||
		group[0].From.ID != faketelegram.TelegramUserID || group[0].ForwardOrigin.MessageID != p {
		t.Fatalf("group %+v", group)
	}
	if ch := f.Messages(faketelegram.ChannelID); len(ch) != 1 || ch[0].From == nil || ch[0].From.ID != faketelegram.BotID {
		t.Fatalf("channel %+v", ch)
	}

	member(t, base, faketelegram.GroupID, faketelegram.StatusMember, false, false)
	post(t, base, "second")
	if u := updates(t, base); len(u) != 0 || len(f.Messages(faketelegram.GroupID)) != 2 {
		t.Fatalf("a member bot got %v", u)
	}
	member(t, base, faketelegram.GroupID, faketelegram.StatusAdministrator, true, false)
	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"withhold_copies":true}`); s != http.StatusOK ||
		!strings.Contains(b, `"withhold_copies":true`) {
		t.Fatalf("withhold = %d %s", s, b)
	}
	post(t, base, "third")
	if u := updates(t, base); len(u) != 0 || len(f.Messages(faketelegram.GroupID)) != 3 {
		t.Fatalf("a withheld copy reached the bot: %v", u)
	}
}

// F-005: the copy arrives copy_delay_ms after the answer to sendMessage.
func TestCopyDelay(t *testing.T) {
	f, base := start(t)
	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"copy_delay_ms":500}`); s != http.StatusOK ||
		!strings.Contains(b, `"copy_delay_ms":500`) || f.Config().CopyDelayMS != 500 {
		t.Fatalf("config = %d %s", s, b)
	}
	if s, _, _ := call(t, http.MethodPut, base+"/_fake/config", `{"copy_delay_ms":-1}`); s != http.StatusBadRequest {
		t.Fatalf("negative delay = %d", s)
	}
	p := post(t, base, "late")
	if len(f.Messages(faketelegram.GroupID)) != 0 || f.Pending(token) != 0 {
		t.Fatal("the copy came before the delay")
	}
	if s, b, _ := call(t, http.MethodPost, base+"/_fake/comment", fmt.Sprintf(`{"post_id":%d,"text":"x"}`, p)); s !=
		http.StatusConflict {
		t.Fatalf("comment before the copy = %d %s", s, b)
	}
	waitFor(t, func() bool { return f.Pending(token) == 1 })
	if g := f.Messages(faketelegram.GroupID); len(g) != 1 || g[0].ForwardOrigin.MessageID != p {
		t.Fatalf("group %+v", g)
	}
}

// F-006: an edit of a post edits its copy, and the bot gets an edited_message for the copy, never one for the post.
func TestEditOfPostEditsCopy(t *testing.T) {
	f, base := start(t)
	p := post(t, base, "before")
	cp := messageOf(t, updates(t, base)[0], "message")
	if s, a := bot(t, base, "editMessageText", fmt.Sprintf(`{"chat_id":-1001000000001,"message_id":%d,"text":"after",
		"reply_markup":%s}`, p, keyboard)); s != http.StatusOK {
		t.Fatalf("edit = %d %+v", s, a)
	}
	u := updates(t, base)
	if len(u) != 1 {
		t.Fatalf("updates after the edit %v", u)
	}
	m := messageOf(t, u[0], "edited_message")
	if m.MessageID != cp.MessageID || m.Text != "after" || !m.IsAutomaticForward || m.EditDate == 0 ||
		m.ForwardOrigin.MessageID != p {
		t.Fatalf("edited copy %+v", m)
	}
	if g := f.Messages(faketelegram.GroupID); len(g) != 1 || g[0].Text != "after" || len(g[0].Edits) != 1 {
		t.Fatalf("group %+v", g)
	}
	// A withheld copy's edits are withheld too.
	call(t, http.MethodPut, base+"/_fake/config", `{"withhold_copies":true}`)
	q := post(t, base, "hidden")
	bot(t, base, "editMessageText", fmt.Sprintf(`{"chat_id":-1001000000001,"message_id":%d,"text":"hidden 2"}`, q))
	if u := updates(t, base); len(u) != 0 {
		t.Fatalf("withheld edits %v", u)
	}
}

// F-007, F-014: a reply to the copy is a comment in its Thread, and so is a reply within the Thread; a person's
// comment reaches the bot with the copy it replies to, even when the copy was withheld. A reply in a Thread notifies
// member only, even when it mentions subscriber.
func TestRepliesToCopyAreComments(t *testing.T) {
	f, base := start(t)
	call(t, http.MethodPut, base+"/_fake/config", `{"withhold_copies":true}`)
	p := post(t, base, "post")
	cp := f.Messages(faketelegram.GroupID)[0]
	s, a := bot(t, base, "sendMessage", fmt.Sprintf(`{"chat_id":-1001000000002,"text":"reply <a href=\"tg://user?id=42\">subscriber</a>",
		"parse_mode":"HTML","disable_notification":true,"reply_parameters":{"message_id":%d}}`, cp.ID))
	var sent struct {
		MessageID       int64 `json:"message_id"`
		MessageThreadID int64 `json:"message_thread_id"`
	}
	_ = json.Unmarshal(a.Result, &sent)
	if s != http.StatusOK || sent.MessageThreadID != cp.ID {
		t.Fatalf("reply to the copy = %d %+v", s, a)
	}
	n := f.Notifications()
	if last := n[len(n)-1]; last.Account != faketelegram.AccountMember || last.Chat != faketelegram.GroupID ||
		last.MessageID != sent.MessageID || last.Sound {
		t.Fatalf("notifications %+v", n)
	}
	for _, x := range n {
		if x.Account == faketelegram.AccountSubscriber && x.Chat == faketelegram.GroupID {
			t.Fatalf("subscriber notified in the group: %+v", n)
		}
	}
	// A reply to the reply stays in the Thread.
	_, a = bot(t, base, "sendMessage", fmt.Sprintf(`{"chat_id":-1001000000002,"text":"next",
		"reply_parameters":{"message_id":%d}}`, sent.MessageID))
	_ = json.Unmarshal(a.Result, &sent)
	if sent.MessageThreadID != cp.ID {
		t.Fatalf("reply in the thread %+v", a)
	}
	// A plain group message is in no Thread.
	_, a = bot(t, base, "sendMessage", `{"chat_id":-1001000000002,"text":"plain"}`)
	sent.MessageThreadID = 0
	_ = json.Unmarshal(a.Result, &sent)
	if sent.MessageThreadID != 0 {
		t.Fatalf("plain message %+v", a)
	}

	s, b, _ := call(t, http.MethodPost, base+"/_fake/comment",
		fmt.Sprintf(`{"post_id":%d,"from":{"id":7001},"text":"looking"}`, p))
	var c faketelegram.Message
	_ = json.Unmarshal([]byte(b), &c)
	if s != http.StatusOK || c.MessageThreadID != cp.ID || c.From == nil || c.From.ID != 7001 {
		t.Fatalf("comment = %d %s", s, b)
	}
	u := updates(t, base)
	if len(u) != 1 {
		t.Fatalf("updates %v", u)
	}
	m := messageOf(t, u[0], "message")
	if m.From.ID != 7001 || m.MessageThreadID != cp.ID || m.ReplyToMessage == nil ||
		m.ReplyToMessage.MessageID != cp.ID || !m.ReplyToMessage.IsAutomaticForward ||
		m.ReplyToMessage.ForwardOrigin.MessageID != p || m.Text != "looking" {
		t.Fatalf("comment update %+v", m)
	}
	for _, bad := range []string{`{"post_id":99,"text":"x"}`, `{"post_id":1,"chat":"@nobody","text":"x"}`, `{`} {
		if s, _, _ := call(t, http.MethodPost, base+"/_fake/comment", bad); s/100 != 4 {
			t.Errorf("comment %s = %d", bad, s)
		}
	}
}

// F-008: once the copy is deleted, a reply to it fails with "message to be replied not found", and so does a comment.
func TestDeletedCopyLosesThread(t *testing.T) {
	f, base := start(t)
	p := post(t, base, "post")
	cp := f.Messages(faketelegram.GroupID)[0]
	path := fmt.Sprintf("%s/_fake/chats/%d/messages/%d", base, faketelegram.GroupID, cp.ID)
	if s, b, _ := call(t, http.MethodDelete, path, ""); s != http.StatusOK {
		t.Fatalf("delete = %d %s", s, b)
	}
	if s, _, _ := call(t, http.MethodDelete, path, ""); s != http.StatusNotFound {
		t.Fatalf("second delete = %d", s)
	}
	for _, bad := range []string{"/_fake/chats/@nobody/messages/1", "/_fake/chats/@muster_alerts/messages/x"} {
		if s, _, _ := call(t, http.MethodDelete, base+bad, ""); s/100 != 4 {
			t.Errorf("delete %s = %d", bad, s)
		}
	}
	s, a := bot(t, base, "sendMessage", fmt.Sprintf(`{"chat_id":-1001000000002,"text":"r",
		"reply_parameters":{"message_id":%d}}`, cp.ID))
	if s != http.StatusBadRequest || a.Description != faketelegram.DescriptionReplyNotFound {
		t.Fatalf("reply to the deleted copy = %d %+v", s, a)
	}
	if s, b, _ := call(t, http.MethodPost, base+"/_fake/comment", fmt.Sprintf(`{"post_id":%d,"text":"x"}`, p)); s !=
		http.StatusBadRequest || !strings.Contains(b, faketelegram.DescriptionReplyNotFound) {
		t.Fatalf("comment under the deleted copy = %d %s", s, b)
	}
	// The edit of the post finds no copy to mirror.
	updates(t, base)
	bot(t, base, "editMessageText", fmt.Sprintf(`{"chat_id":-1001000000001,"message_id":%d,"text":"edited"}`, p))
	if u := updates(t, base); len(u) != 0 {
		t.Fatalf("edit of a post without its copy %v", u)
	}
}

// F-013, F-015: a reply inside the channel is a new post that notifies both accounts and gets a copy of its own, which
// notifies member with sound even when the post is Quiet. A channel without comments gets no copies.
func TestReplyInsideChannelIsNewPost(t *testing.T) {
	f, base := start(t)
	p := post(t, base, "post")
	s, _ := bot(t, base, "sendMessage", fmt.Sprintf(`{"chat_id":-1001000000001,"text":"inside","disable_notification":true,
		"reply_parameters":{"message_id":%d}}`, p))
	if s != http.StatusOK || len(f.Messages(faketelegram.ChannelID)) != 2 || len(f.Messages(faketelegram.GroupID)) != 2 {
		t.Fatalf("reply inside the channel = %d, %+v", s, f.Messages(0))
	}
	n := f.Notifications()
	want := []faketelegram.Notification{
		{Account: "member", Chat: faketelegram.ChannelID, MessageID: 2, Sound: false},
		{Account: "subscriber", Chat: faketelegram.ChannelID, MessageID: 2, Sound: false},
		{Account: "member", Chat: faketelegram.GroupID, MessageID: 2, Sound: true},
	}
	if fmt.Sprint(n[3:]) != fmt.Sprint(want) {
		t.Fatalf("notifications %+v", n)
	}
	bot(t, base, "sendMessage", `{"chat_id":-1001000000003,"text":"no comments"}`)
	if len(f.Messages(faketelegram.NoCommentsID)) != 1 || len(f.Messages(0)) != 5 {
		t.Fatalf("a channel without comments got a copy: %+v", f.Messages(0))
	}
}

// pressUpdate is the callback_query of an update.
type pressUpdate struct {
	ID   string `json:"id"`
	From struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
	Message struct {
		MessageID       int64 `json:"message_id"`
		MessageThreadID int64 `json:"message_thread_id"`
		Chat            struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
	ChatInstance string `json:"chat_instance"`
	Data         string `json:"data"`
}

func pressOf(t *testing.T, u map[string]json.RawMessage) pressUpdate {
	t.Helper()
	var q pressUpdate
	if err := json.Unmarshal(u["callback_query"], &q); err != nil || u["callback_query"] == nil {
		t.Fatalf("no callback_query in %v", u)
	}
	return q
}

func press(t *testing.T, base, body string) (int, faketelegram.Pressed) {
	t.Helper()
	s, b, _ := call(t, http.MethodPost, base+"/_fake/press", body)
	var p faketelegram.Pressed
	_ = json.Unmarshal([]byte(b), &p)
	return s, p
}

// F-009: a press on a channel post's button arrives as a callback_query from the person, with the post in the channel
// and the button's data; a press on a bot's reply in a comment Thread carries the discussion group and
// message_thread_id. data_from presses a valid button on the wrong message, and data a forged one.
func TestPressesOnPostsAndComments(t *testing.T) {
	f, base := start(t)
	p := post(t, base, "post")
	cp := messageOf(t, updates(t, base)[0], "message")
	s, a := bot(t, base, "sendMessage", fmt.Sprintf(`{"chat_id":-1001000000002,"text":"reminder",
		"reply_parameters":{"message_id":%d},"reply_markup":{"inline_keyboard":[[{"text":"Still on it","callback_data":"k"}]]}}`,
		cp.MessageID))
	var reply struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(a.Result, &reply)
	if s != http.StatusOK {
		t.Fatalf("reply = %d %+v", s, a)
	}

	s, pr := press(t, base, fmt.Sprintf(`{"chat":-1001000000001,"message_id":%d,"from":{"id":5001,"username":"bob_tg"},
		"button":"Ack"}`, p))
	if s != http.StatusOK || pr.CallbackQueryID == "" || pr.UpdateID == 0 {
		t.Fatalf("press = %d %+v", s, pr)
	}
	u := updates(t, base)
	if len(u) != 1 {
		t.Fatalf("updates %v", u)
	}
	q := pressOf(t, u[0])
	if q.ID != pr.CallbackQueryID || q.From.ID != 5001 || q.From.Username != "bob_tg" || q.Message.MessageID != p ||
		q.Message.Chat.ID != faketelegram.ChannelID || q.Message.Chat.Type != "channel" || q.Data != "a" ||
		q.Message.MessageThreadID != 0 || q.ChatInstance == "" {
		t.Fatalf("press on the post %+v", q)
	}

	press(t, base, fmt.Sprintf(`{"chat":"-1001000000002","message_id":%d,"from":{"id":5001},"button":"Still on it"}`,
		reply.MessageID))
	q = pressOf(t, updates(t, base)[0])
	if q.Message.Chat.ID != faketelegram.GroupID || q.Message.Chat.Type != "supergroup" ||
		q.Message.MessageThreadID != cp.MessageID || q.Data != "k" {
		t.Fatalf("press on the comment %+v", q)
	}

	press(t, base, fmt.Sprintf(`{"chat":-1001000000001,"message_id":999999,"from":{"id":5001},"button":"Ack",
		"data_from":%d}`, p))
	q = pressOf(t, updates(t, base)[0])
	if q.Message.MessageID != 999999 || q.Message.Chat.ID != faketelegram.ChannelID || q.Data != "a" {
		t.Fatalf("press with data_from %+v", q)
	}
	press(t, base, fmt.Sprintf(`{"chat":"@muster_alerts","message_id":%d,"from":{"id":5001},"button":"x","data":"forged"}`,
		p))
	if q = pressOf(t, updates(t, base)[0]); q.Data != "forged" {
		t.Fatalf("forged press %+v", q)
	}
	for _, bad := range []string{
		fmt.Sprintf(`{"chat":-1001000000001,"message_id":%d,"from":{"id":5001},"button":"Nope"}`, p),
		fmt.Sprintf(`{"chat":-1001000000001,"message_id":%d,"button":"Ack"}`, p),
		`{"chat":-1001000000001,"message_id":424242,"from":{"id":5001},"button":"Ack"}`,
		`{"chat":"@nobody","message_id":1,"from":{"id":5001},"button":"Ack"}`,
		fmt.Sprintf(`{"chat":-1001000000001,"message_id":%d,"from":{"id":5001},"button":"Ack","pressed_ms_ago":-1}`, p),
		`{`,
	} {
		if s, _ := press(t, base, bad); s/100 != 4 {
			t.Errorf("press %s = %d", bad, s)
		}
	}
	if len(f.Answers()) != 0 {
		t.Fatalf("answers %+v", f.Answers())
	}
}

// F-010: a press is answered once, with at most 200 characters, within the answer deadline (15 s by default); a later
// answer fails with "query is too old and response timeout expired or query ID is invalid". Every answer is recorded
// with its time after the press.
func TestAnswerDeadline(t *testing.T) {
	f, base := start(t)
	if f.Config().AnswerDeadlineMS != 15000 {
		t.Fatalf("default deadline %d", f.Config().AnswerDeadlineMS)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	f.SetClock(func() time.Time { return now })
	p := post(t, base, "post")
	pressAt := func(agoMs int64) string {
		_, pr := press(t, base, fmt.Sprintf(`{"chat":-1001000000001,"message_id":%d,"from":{"id":5001},"button":"Ack",
			"pressed_ms_ago":%d}`, p, agoMs))
		return pr.CallbackQueryID
	}
	answer := func(id, text string) (int, answer) {
		b, _ := json.Marshal(map[string]string{"callback_query_id": id, "text": text})
		return bot(t, base, "answerCallbackQuery", string(b))
	}

	early := pressAt(14_900)
	if s, a := answer(early, "Done: Acknowledge"); s != http.StatusOK || !a.OK {
		t.Fatalf("answer just before the deadline = %d %+v", s, a)
	}
	if s, a := answer(early, "again"); s != http.StatusBadRequest || a.Description != faketelegram.DescriptionQueryTooOld {
		t.Fatalf("second answer = %d %+v", s, a)
	}
	late := pressAt(15_001)
	if s, a := answer(late, "late"); s != http.StatusBadRequest || a.Description != faketelegram.DescriptionQueryTooOld {
		t.Fatalf("answer after the deadline = %d %+v", s, a)
	}
	fresh := pressAt(0)
	if s, a := answer(fresh, strings.Repeat("я", 201)); s != http.StatusBadRequest ||
		a.Description != faketelegram.DescriptionAnswerTooLong {
		t.Fatalf("long answer = %d %+v", s, a)
	}
	if s, a := answer(fresh, strings.Repeat("я", 200)); s != http.StatusOK || !a.OK {
		t.Fatalf("answer of 200 characters = %d %+v", s, a)
	}
	if s, _ := answer("unknown", "x"); s != http.StatusBadRequest {
		t.Fatalf("unknown query = %d", s)
	}
	if s, _ := answer("", "x"); s != http.StatusBadRequest {
		t.Fatalf("no query = %d", s)
	}

	_, b, _ := call(t, http.MethodGet, base+"/_fake/answers", "")
	var got []faketelegram.Answer
	if err := json.Unmarshal([]byte(b), &got); err != nil || len(got) != 7 {
		t.Fatalf("answers %s", b)
	}
	if !got[0].OK || got[0].Text != "Done: Acknowledge" || got[0].AfterMs != 14_900 || got[0].AtMs != now.UnixMilli() ||
		got[1].OK || got[2].OK || got[2].AfterMs != 15_001 || got[3].OK || !got[4].OK {
		t.Fatalf("answers %+v", got)
	}

	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"answer_deadline_ms":100}`); s != http.StatusOK ||
		!strings.Contains(b, `"answer_deadline_ms":100`) {
		t.Fatalf("config = %d %s", s, b)
	}
	if s, a := answer(pressAt(101), "x"); s != http.StatusBadRequest || a.OK {
		t.Fatalf("answer after a short deadline = %d %+v", s, a)
	}
	if s, _, _ := call(t, http.MethodPut, base+"/_fake/config", `{"answer_deadline_ms":0}`); s != http.StatusBadRequest {
		t.Fatalf("a zero deadline = %d", s)
	}
	f.SetAnswerDeadline(0)
	if f.Config().AnswerDeadlineMS != 15000 {
		t.Fatalf("deadline %d", f.Config().AnswerDeadlineMS)
	}
	f.SetAnswerDeadline(time.Second)
	if s, _ := answer(pressAt(999), "x"); s != http.StatusOK {
		t.Fatalf("answer within a deadline of 1 s = %d", s)
	}
}

// TestPrivateChats is the private chat of S-051: a person's message to the bot opens their private chat — its id is
// their user id — and reaches the bot as a message update from them; the bot can then answer there, plain text being
// recorded under GET /_fake/messages?chat=<user id>, and before the person wrote, sendMessage answers "chat not found".
func TestPrivateChats(t *testing.T) {
	_, base := start(t)
	if s, a := bot(t, base, "sendMessage", `{"chat_id":7001,"text":"hello"}`); s != http.StatusBadRequest ||
		a.Description != faketelegram.DescriptionChatNotFound {
		t.Fatalf("a send before the person wrote = %d %+v", s, a)
	}
	if s, _, _ := call(t, http.MethodGet, base+"/_fake/messages?chat=7001", ""); s != http.StatusNotFound {
		t.Fatalf("the chat before the first message = %d", s)
	}
	s, b, _ := call(t, http.MethodPost, base+"/_fake/private",
		`{"token":"`+token+`","from":{"id":7001,"username":"alice_t"},"text":"/start abc"}`)
	if s != http.StatusOK || !strings.Contains(b, `"update_id":1`) {
		t.Fatalf("private = %d %s", s, b)
	}
	u := updates(t, base)
	if len(u) != 1 {
		t.Fatalf("updates %v", u)
	}
	m := messageOf(t, u[0], "message")
	var from struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
		IsBot     bool   `json:"is_bot"`
	}
	_ = json.Unmarshal(u[0]["message"], &struct {
		From *struct {
			ID        int64  `json:"id"`
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
			IsBot     bool   `json:"is_bot"`
		} `json:"from"`
	}{From: &from})
	if m.Chat.ID != 7001 || m.Chat.Type != faketelegram.TypePrivate || m.Text != "/start abc" || from.ID != 7001 ||
		from.Username != "alice_t" || from.FirstName != "User 7001" || from.IsBot {
		t.Fatalf("message %+v from %+v", m, from)
	}
	if s, a := bot(t, base, "sendMessage", `{"chat_id":7001,"text":"Linked","link_preview_options":{"is_disabled":true}}`); s != http.StatusOK || !a.OK {
		t.Fatalf("the reply = %d %+v", s, a)
	}
	_, b, _ = call(t, http.MethodGet, base+"/_fake/messages?chat=7001", "")
	var msgs []faketelegram.Message
	if err := json.Unmarshal([]byte(b), &msgs); err != nil || len(msgs) != 2 || msgs[0].Text != "/start abc" ||
		msgs[0].From.ID != 7001 || msgs[1].Text != "Linked" || msgs[1].From.ID != faketelegram.BotID {
		t.Fatalf("messages %s", b)
	}
	call(t, http.MethodPost, base+"/_fake/private", `{"token":"`+token+`","from":{"id":7001},"text":"again"}`)
	if u := updates(t, base); len(u) != 1 || messageOf(t, u[0], "message").MessageID != 3 {
		t.Errorf("a second message %v", u)
	}
	for _, body := range []string{`{"from":{"id":7001},"text":"x"}`, `{"token":"t","from":{"id":0}}`, `not json`} {
		if s, _, _ := call(t, http.MethodPost, base+"/_fake/private", body); s != http.StatusBadRequest {
			t.Errorf("%s = %d", body, s)
		}
	}
}

// as calls a Bot API method with a token.
func as(t *testing.T, base, tok, method, body string) (int, answer) {
	t.Helper()
	status, _, a := call(t, http.MethodPost, base+"/bot"+tok+"/"+method, body)
	return status, a
}

// updatesOf reads and confirms the queued updates of a token.
func updatesOf(t *testing.T, base, tok string) []map[string]json.RawMessage {
	t.Helper()
	_, a := as(t, base, tok, "getUpdates", `{"timeout":0}`)
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(a.Result, &out); err != nil {
		t.Fatalf("updates of %s %s: %v", tok, a.Result, err)
	}
	if len(out) > 0 {
		var id int64
		_ = json.Unmarshal(out[len(out)-1]["update_id"], &id)
		as(t, base, tok, "getUpdates", fmt.Sprintf(`{"timeout":0,"offset":%d}`, id+1))
	}
	return out
}

// postAs posts to the channel @muster_alerts as the bot of tok.
func postAs(t *testing.T, base, tok, text string) int64 {
	t.Helper()
	s, a := as(t, base, tok, "sendMessage", `{"chat_id":-1001000000001,"text":"`+text+`","reply_markup":`+keyboard+`}`)
	var m struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(a.Result, &m)
	if s != http.StatusOK || m.MessageID == 0 {
		t.Fatalf("post as %s = %d %+v", tok, s, a)
	}
	return m.MessageID
}

func kinds(t *testing.T, u []map[string]json.RawMessage) []string {
	t.Helper()
	out := []string{}
	for _, x := range u {
		for k := range x {
			if k != "update_id" {
				out = append(out, k)
			}
		}
	}
	return out
}

// S-069: the second bot has its own identity for every token that starts with 234567:, its own sender and edits;
// bot A's other tokens keep the first bot.
func TestSecondBotIdentity(t *testing.T) {
	f, base := start(t)
	for tok, want := range map[string]int64{"123456:x": faketelegram.BotID, token: faketelegram.BotID,
		"777101:second-bot-token": faketelegram.BotID, faketelegram.SecondBotToken: faketelegram.SecondBotID,
		"234567:other": faketelegram.SecondBotID} {
		_, a := as(t, base, tok, "getMe", "")
		var me faketelegram.User
		_ = json.Unmarshal(a.Result, &me)
		if me.ID != want || !me.IsBot {
			t.Errorf("getMe with %s = %+v, want %d", tok, me, want)
		}
		if want == faketelegram.SecondBotID && me.Username != faketelegram.SecondBotUsername {
			t.Errorf("bot B's username %q", me.Username)
		}
	}

	_, a := as(t, base, faketelegram.SecondBotToken, "getChatMember",
		fmt.Sprintf(`{"chat_id":%d,"user_id":%d}`, faketelegram.GroupID, faketelegram.SecondBotID))
	var cm struct {
		Status string            `json:"status"`
		User   faketelegram.User `json:"user"`
	}
	_ = json.Unmarshal(a.Result, &cm)
	if cm.Status != faketelegram.StatusAdministrator || cm.User.Username != faketelegram.SecondBotUsername {
		t.Errorf("bot B in the group %+v", cm)
	}

	postAs(t, base, token, "by A")
	pb := postAs(t, base, faketelegram.SecondBotToken, "by B")
	ch := f.Messages(faketelegram.ChannelID)
	if len(ch) != 2 || ch[0].From.ID != faketelegram.BotID || ch[1].From.ID != faketelegram.SecondBotID ||
		ch[1].From.Username != faketelegram.SecondBotUsername {
		t.Fatalf("channel %+v", ch)
	}
	edit := func(tok string, chat, id int64, text string) (int, answer) {
		return as(t, base, tok, "editMessageText", fmt.Sprintf(`{"chat_id":%d,"message_id":%d,"text":%q}`, chat, id,
			text))
	}
	if s, a := edit(faketelegram.SecondBotToken, faketelegram.ChannelID, pb, "B edits B"); s != http.StatusOK {
		t.Fatalf("bot B edits its post = %d %+v", s, a)
	}
	if s, a := edit(token, faketelegram.ChannelID, pb, "A edits B as a channel admin"); s != http.StatusOK {
		t.Fatalf("bot A edits bot B's channel post = %d %+v", s, a)
	}
	ch = f.Messages(faketelegram.ChannelID)
	if e := ch[1].Edits; len(e) != 2 || e[0].By != faketelegram.SecondBotID || e[1].By != faketelegram.BotID {
		t.Errorf("edits of post B %+v", e)
	}

	copyB := f.Messages(faketelegram.GroupID)[1].ID
	s, a := as(t, base, faketelegram.SecondBotToken, "sendMessage",
		fmt.Sprintf(`{"chat_id":%d,"text":"reply B","reply_parameters":{"message_id":%d}}`, faketelegram.GroupID, copyB))
	var reply struct {
		MessageID int64 `json:"message_id"`
	}
	_ = json.Unmarshal(a.Result, &reply)
	if s != http.StatusOK {
		t.Fatalf("bot B replies = %d %+v", s, a)
	}
	if s, a := edit(token, faketelegram.GroupID, reply.MessageID, "A edits B's reply"); s != http.StatusBadRequest ||
		a.Description != faketelegram.DescriptionCantBeEdited {
		t.Errorf("bot A edits bot B's group message = %d %+v", s, a)
	}
	if s, _ := edit(faketelegram.SecondBotToken, faketelegram.GroupID, reply.MessageID, "B edits B's reply"); s !=
		http.StatusOK {
		t.Errorf("bot B edits its own group message = %d", s)
	}

	member := fmt.Sprintf("%s/_fake/chats/%d/members/%d", base, faketelegram.GroupID, faketelegram.SecondBotID)
	call(t, http.MethodPut, member, `{"status":"left"}`)
	if s, _ := as(t, base, faketelegram.SecondBotToken, "getChatMember",
		fmt.Sprintf(`{"chat_id":%d,"user_id":%d}`, faketelegram.GroupID, faketelegram.SecondBotID)); s !=
		http.StatusForbidden {
		t.Errorf("bot B outside the group = %d, want 403", s)
	}
	if s, _ := as(t, base, token, "getChatMember",
		fmt.Sprintf(`{"chat_id":%d,"user_id":%d}`, faketelegram.GroupID, faketelegram.SecondBotID)); s != http.StatusOK {
		t.Errorf("bot A asks about bot B = %d, want 200", s)
	}
}

// S-069: the automatic copy, its edited_message and people's comments reach every admin bot of the discussion group
// that receives updates; a bot's own messages reach no other bot; another token of the posting bot gets nothing;
// withhold_copies_from keeps copies from the bots it names.
func TestCopyFanOut(t *testing.T) {
	_, base := start(t)
	tokA, tokB, tokA2 := token, faketelegram.SecondBotToken, "777101:second-token-of-a"
	for _, tok := range []string{tokA, tokB, tokA2} {
		updatesOf(t, base, tok)
	}

	p := postAs(t, base, tokA, "post A")
	for tok, want := range map[string]int{tokA: 1, tokB: 1, tokA2: 0} {
		u := updatesOf(t, base, tok)
		if len(u) != want {
			t.Fatalf("%s got %v after post A, want %d", tok, kinds(t, u), want)
		}
		if want == 1 && messageOf(t, u[0], "message").ForwardOrigin.MessageID != p {
			t.Errorf("%s got the wrong copy", tok)
		}
	}

	as(t, base, tokA, "editMessageText", fmt.Sprintf(`{"chat_id":%d,"message_id":%d,"text":"post A edited"}`,
		faketelegram.ChannelID, p))
	for _, tok := range []string{tokA, tokB} {
		if u := updatesOf(t, base, tok); len(u) != 1 || u[0]["edited_message"] == nil {
			t.Errorf("%s got %v after the edit, want an edited_message", tok, kinds(t, u))
		}
	}

	if s, b, _ := call(t, http.MethodPost, base+"/_fake/comment",
		fmt.Sprintf(`{"post_id":%d,"from":{"id":5001},"text":"looking"}`, p)); s != http.StatusOK {
		t.Fatalf("comment = %d %s", s, b)
	}
	for _, tok := range []string{tokA, tokB} {
		if u := updatesOf(t, base, tok); len(u) != 1 || messageOf(t, u[0], "message").Text != "looking" {
			t.Errorf("%s got %v after the comment", tok, kinds(t, u))
		}
	}
	if u := updatesOf(t, base, tokA2); len(u) != 0 {
		t.Errorf("another token of bot A got %v", kinds(t, u))
	}

	copyID := int64(1)
	as(t, base, tokA, "sendMessage", fmt.Sprintf(`{"chat_id":%d,"text":"thread reply","reply_parameters":{"message_id":%d}}`,
		faketelegram.GroupID, copyID))
	for _, tok := range []string{tokA, tokB} {
		if u := updatesOf(t, base, tok); len(u) != 0 {
			t.Errorf("bot A's own reply reached %s: %v", tok, kinds(t, u))
		}
	}

	if s, b, _ := call(t, http.MethodPut, base+"/_fake/config", `{"withhold_copies_from":[234567]}`); s !=
		http.StatusOK || !strings.Contains(b, `"withhold_copies_from":[234567]`) {
		t.Fatalf("withhold_copies_from = %d %s", s, b)
	}
	p2 := postAs(t, base, tokA, "post A2")
	if u := updatesOf(t, base, tokA); len(u) != 1 {
		t.Errorf("bot A got %v, want its copy", kinds(t, u))
	}
	if u := updatesOf(t, base, tokB); len(u) != 0 {
		t.Errorf("a copy withheld from bot B reached it: %v", kinds(t, u))
	}
	call(t, http.MethodPost, base+"/_fake/comment", fmt.Sprintf(`{"post_id":%d,"from":{"id":5001},"text":"c2"}`, p2))
	for _, tok := range []string{tokA, tokB} {
		if u := updatesOf(t, base, tok); len(u) != 1 {
			t.Errorf("the comment under a withheld copy did not reach %s: %v", tok, kinds(t, u))
		}
	}
	call(t, http.MethodPut, base+"/_fake/config", `{"withhold_copies_from":[]}`)

	call(t, http.MethodPut, fmt.Sprintf("%s/_fake/chats/%d/members/%d", base, faketelegram.GroupID,
		faketelegram.SecondBotID), `{"status":"member"}`)
	postAs(t, base, tokA, "post A3")
	if u := updatesOf(t, base, tokA); len(u) != 1 {
		t.Errorf("bot A got %v", kinds(t, u))
	}
	if u := updatesOf(t, base, tokB); len(u) != 0 {
		t.Errorf("bot B, a mere member of the group, got %v", kinds(t, u))
	}
	call(t, http.MethodPut, fmt.Sprintf("%s/_fake/chats/%d/members/%d", base, faketelegram.GroupID,
		faketelegram.SecondBotID), `{"status":"administrator","can_post_messages":true,"can_edit_messages":true}`)

	pb := postAs(t, base, tokB, "post B")
	for _, tok := range []string{tokA, tokB} {
		u := updatesOf(t, base, tok)
		if len(u) != 1 || messageOf(t, u[0], "message").ForwardOrigin.MessageID != pb {
			t.Errorf("%s got %v after post B, want its copy", tok, kinds(t, u))
		}
	}
}

// S-069: a person's private chats with two bots are two conversations: each bot sees and writes only its own.
func TestPrivateChatsPerBot(t *testing.T) {
	_, base := start(t)
	tokB := faketelegram.SecondBotToken
	for _, tok := range []string{token, tokB} {
		if s, b, _ := call(t, http.MethodPost, base+"/_fake/private",
			`{"token":"`+tok+`","from":{"id":5001},"text":"/start `+tok[:6]+`"}`); s != http.StatusOK {
			t.Fatalf("private to %s = %d %s", tok, s, b)
		}
	}
	call(t, http.MethodPost, base+"/_fake/private", `{"token":"`+token+`","from":{"id":6001},"text":"/start"}`)
	if u := updatesOf(t, base, tokB); len(u) != 1 || messageOf(t, u[0], "message").Text != "/start 234567" {
		t.Errorf("bot B's updates %v", u)
	}
	if s, a := as(t, base, tokB, "sendMessage", `{"chat_id":5001,"text":"hi from B"}`); s != http.StatusOK {
		t.Fatalf("bot B answers 5001 = %d %+v", s, a)
	}
	if s, a := as(t, base, tokB, "sendMessage", `{"chat_id":6001,"text":"hi"}`); s != http.StatusBadRequest ||
		a.Description != faketelegram.DescriptionChatNotFound {
		t.Errorf("bot B writes to a person who wrote only to bot A = %d %+v", s, a)
	}
	read := func(q string) []faketelegram.Message {
		_, b, _ := call(t, http.MethodGet, base+"/_fake/messages?"+q, "")
		var msgs []faketelegram.Message
		if err := json.Unmarshal([]byte(b), &msgs); err != nil {
			t.Fatalf("messages %s: %s", q, b)
		}
		return msgs
	}
	if m := read("chat=5001&bot=234567"); len(m) != 2 || m[1].Text != "hi from B" ||
		m[1].From.ID != faketelegram.SecondBotID {
		t.Errorf("bot B's conversation %+v", m)
	}
	if m := read("chat=5001&bot=123456"); len(m) != 1 || m[0].Text != "/start 777001" {
		t.Errorf("bot A's conversation %+v", m)
	}
	if m := read("chat=5001"); len(m) != 3 {
		t.Errorf("the whole chat %+v", m)
	}
	if s, _, _ := call(t, http.MethodGet, base+"/_fake/messages?chat=5001&bot=x", ""); s != http.StatusBadRequest {
		t.Errorf("a bot that is not an id = %d", s)
	}
}

// S-069: a press goes to the bot that sent the message pressed, also with the data of another bot's message.
func TestPressGoesToTheBotOfTheMessage(t *testing.T) {
	_, base := start(t)
	tokB := faketelegram.SecondBotToken
	pa, pb := postAs(t, base, token, "post A"), postAs(t, base, tokB, "post B")
	updatesOf(t, base, token)
	updatesOf(t, base, tokB)
	if s, _ := press(t, base, fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":5001},"button":"Ack","data_from":%d}`,
		faketelegram.ChannelID, pb, pa)); s != http.StatusOK {
		t.Fatalf("press = %d", s)
	}
	if u := updatesOf(t, base, token); len(u) != 0 {
		t.Errorf("bot A got the press on bot B's post: %v", kinds(t, u))
	}
	u := updatesOf(t, base, tokB)
	if len(u) != 1 || u[0]["callback_query"] == nil {
		t.Fatalf("bot B's updates %v", kinds(t, u))
	}
	var q struct {
		Message struct {
			MessageID int64 `json:"message_id"`
		} `json:"message"`
	}
	_ = json.Unmarshal(u[0]["callback_query"], &q)
	if q.Message.MessageID != pb {
		t.Errorf("the press names message %d, want %d", q.Message.MessageID, pb)
	}
}
