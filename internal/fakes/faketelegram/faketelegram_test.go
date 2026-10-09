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

// F-012: a channel post notifies both accounts, without sound with disable_notification; an edit notifies nobody.
func TestNotificationsOfChannelPosts(t *testing.T) {
	f, base := start(t)
	bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"loud"}`)
	bot(t, base, "sendMessage", `{"chat_id":-1001000000001,"text":"quiet","disable_notification":true}`)
	bot(t, base, "editMessageText", `{"chat_id":-1001000000001,"message_id":1,"text":"edited"}`)
	bot(t, base, "sendMessage", `{"chat_id":-1001000000002,"text":"group"}`)
	want := []faketelegram.Notification{
		{Account: "member", Chat: faketelegram.ChannelID, MessageID: 1, Sound: true},
		{Account: "subscriber", Chat: faketelegram.ChannelID, MessageID: 1, Sound: true},
		{Account: "member", Chat: faketelegram.ChannelID, MessageID: 2, Sound: false},
		{Account: "subscriber", Chat: faketelegram.ChannelID, MessageID: 2, Sound: false},
		{Account: "member", Chat: faketelegram.GroupID, MessageID: 1, Sound: true},
	}
	if got := f.Notifications(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("notifications = %+v", got)
	}
	_, body, _ := call(t, http.MethodGet, base+"/_fake/notifications", "")
	if !strings.Contains(body, `"account":"subscriber"`) {
		t.Fatalf("GET notifications = %s", body)
	}
}

// F-016: sends and edits share a budget of 20 per minute per chat; the 21st answers 429 with the exact retry_after.
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
