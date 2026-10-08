// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakemattermost_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/fakes/fakemattermost"
)

const token = "bot-token"

// clock is the fake's manual time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	t     *testing.T
	fake  *fakemattermost.Fake
	clock *clock
}

func start(t *testing.T) *fixture {
	t.Helper()
	f := fakemattermost.New()
	c := &clock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	f.Now = c.Now
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return &fixture{t: t, fake: f, clock: c}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) String() string { return fmt.Sprintf("%d %s", r.status, r.body) }

func do(t *testing.T, method, url, body string, header ...string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: b}
}

// api calls the REST API with the bot's token.
func (fx *fixture) api(method, path, body string) response {
	fx.t.Helper()
	return do(fx.t, method, fx.fake.URL()+path, body, "Authorization", "Bearer "+token)
}

// control calls a control endpoint.
func (fx *fixture) control(method, path, body string) response {
	fx.t.Helper()
	return do(fx.t, method, fx.fake.URL()+path, body)
}

func decode[T any](t *testing.T, r response) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
	return v
}

type appError struct {
	ID            string `json:"id"`
	Message       string `json:"message"`
	DetailedError string `json:"detailed_error"`
	RequestID     string `json:"request_id"`
	StatusCode    int    `json:"status_code"`
}

// wantAppError checks an error answer of the API.
func wantAppError(t *testing.T, r response, status int, id string) appError {
	t.Helper()
	if r.status != status {
		t.Fatalf("answer %s, want %d %s", r, status, id)
	}
	e := decode[appError](t, r)
	if e.ID != id || e.StatusCode != status || len(e.RequestID) != 26 || e.Message == "" {
		t.Errorf("error %+v, want id %s and status_code %d", e, id, status)
	}
	return e
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (fx *fixture) post(channel, message, rootID, props string) fakemattermost.Post {
	fx.t.Helper()
	if props == "" {
		props = "null"
	}
	body := fmt.Sprintf(`{"channel_id":%q,"message":%s,"root_id":%q,"props":%s}`, channel, jsonString(message), rootID,
		props)
	r := fx.api(http.MethodPost, "/api/v4/posts", body)
	if r.status != http.StatusCreated {
		fx.t.Fatalf("POST /api/v4/posts %s: %s", body, r)
	}
	return decode[fakemattermost.Post](fx.t, r)
}

func (fx *fixture) getPost(id string) fakemattermost.Post {
	fx.t.Helper()
	r := fx.api(http.MethodGet, "/api/v4/posts/"+id, "")
	if r.status != http.StatusOK {
		fx.t.Fatalf("GET post %s: %s", id, r)
	}
	return decode[fakemattermost.Post](fx.t, r)
}

func (fx *fixture) notifications() []fakemattermost.Notification {
	fx.t.Helper()
	return decode[[]fakemattermost.Notification](fx.t, fx.control(http.MethodGet, "/_fake/notifications", ""))
}

func (fx *fixture) ephemeral() []fakemattermost.Ephemeral {
	fx.t.Helper()
	return decode[[]fakemattermost.Ephemeral](fx.t, fx.control(http.MethodGet, "/_fake/ephemeral", ""))
}

func (fx *fixture) serverLog() []fakemattermost.LogEntry {
	fx.t.Helper()
	return decode[[]fakemattermost.LogEntry](fx.t, fx.control(http.MethodGet, "/_fake/server-log", ""))
}

func (fx *fixture) setConfig(body string) {
	fx.t.Helper()
	if r := fx.control(http.MethodPut, "/_fake/config", body); r.status != http.StatusOK {
		fx.t.Fatalf("PUT /_fake/config %s: %s", body, r)
	}
}

// press presses the button ack.
func (fx *fixture) press(postID, userID string) fakemattermost.PressResult {
	fx.t.Helper()
	body := fmt.Sprintf(`{"post_id":%q,"action_id":"ack","user_id":%q}`, postID, userID)
	r := fx.control(http.MethodPost, "/_fake/press", body)
	if r.status != http.StatusOK {
		fx.t.Fatalf("POST /_fake/press %s: %s", body, r)
	}
	return decode[fakemattermost.PressResult](fx.t, r)
}

// receiver is an integration URL that records presses and answers them.
type receiver struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []map[string]json.RawMessage
	headers  []http.Header
	status   int
	answer   string
}

func newReceiver(t *testing.T, status int, answer string) *receiver {
	t.Helper()
	rc := &receiver{status: status, answer: answer}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		rc.mu.Lock()
		rc.requests = append(rc.requests, body)
		rc.headers = append(rc.headers, r.Header.Clone())
		status, answer := rc.status, rc.answer
		rc.mu.Unlock()
		if status == 0 {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) received() []map[string]json.RawMessage {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return slices.Clone(rc.requests)
}

func (rc *receiver) set(status int, answer string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.status, rc.answer = status, answer
}

func buttons(url string) string {
	return fmt.Sprintf(`{"attachments":[{"text":"KubePodCrashLooping","actions":[`+
		`{"id":"ack","name":"Ack","integration":{"url":%q,"context":{"action":"ack","group":"G-1"}}},`+
		`{"id":"nourl","name":"No URL"}]}]}`, url)
}

func str(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode %s as a string: %v", raw, err)
	}
	return s
}

func TestFacts(t *testing.T) {
	t.Run("F-022", func(t *testing.T) {
		fx := start(t)
		rc := newReceiver(t, http.StatusOK, `{}`)
		p := fx.post(fakemattermost.ChannelAlerts, "", "", buttons(rc.srv.URL+"/press"))

		res := fx.press(p.ID, fakemattermost.AliceUserID)
		if res.PersonStatus != http.StatusBadRequest || res.Error != fakemattermost.ActionIntegrationError ||
			res.Status != 0 || string(res.Answer) != "null" {
			t.Errorf("press to a loopback address = %+v, want the person's 400 and no call", res)
		}
		if n := len(rc.received()); n != 0 {
			t.Errorf("the receiver got %d presses, want none", n)
		}
		log := fx.serverLog()
		if len(log) != 1 || log[0].Level != "error" ||
			!strings.Contains(log[0].Message, "address forbidden") ||
			!strings.Contains(log[0].Message, "127.0.0.1 resolves to 127.0.0.1, in a reserved range and not in "+
				"AllowedUntrustedInternalConnections") {
			t.Errorf("server log %+v", log)
		}
		presses := fx.fake.Presses()
		if len(presses) != 1 || presses[0].Request != nil || presses[0].Error != res.Error {
			t.Errorf("presses %+v, want the refused press without a request", presses)
		}

		for _, allowed := range []string{"localhost 127.0.0.1", "10.0.0.0/8, 127.0.0.0/8", "::1,127.0.0.1"} {
			fx.setConfig(`{"allowed_untrusted_internal_connections":` + jsonString(allowed) + `}`)
			if res := fx.press(p.ID, fakemattermost.AliceUserID); res.PersonStatus != http.StatusOK {
				t.Errorf("press with %q allowed = %+v", allowed, res)
			}
		}
		fx.fake.SetAllowedUntrustedInternalConnections("localhost")
		if fx.fake.Config().AllowedUntrustedInternalConnections != "localhost" {
			t.Errorf("config %+v", fx.fake.Config())
		}
		byName := strings.Replace(rc.srv.URL, "127.0.0.1", "localhost", 1)
		p2 := fx.post(fakemattermost.ChannelAlerts, "", "", buttons(byName+"/press"))
		if res := fx.press(p2.ID, fakemattermost.AliceUserID); res.PersonStatus != http.StatusOK {
			t.Errorf("press to localhost with the name allowed = %+v", res)
		}
		if res := fx.press(p.ID, fakemattermost.AliceUserID); res.PersonStatus != http.StatusBadRequest {
			t.Errorf("press to 127.0.0.1 with only the name localhost allowed = %+v, want 400", res)
		}
		if n := len(rc.received()); n != 4 {
			t.Errorf("the receiver got %d presses, want 4", n)
		}
	})

	t.Run("F-023", func(t *testing.T) {
		fx := start(t)
		fx.setConfig(`{"allowed_untrusted_internal_connections":"127.0.0.1"}`)
		answer := `{"update":{"message":"","props":{"attachments":[{"text":"Acknowledged by @bob"}]}}}`
		rc := newReceiver(t, http.StatusOK, answer)
		p := fx.post(fakemattermost.ChannelAlerts, "", "", buttons(rc.srv.URL+"/press"))
		fx.clock.Advance(time.Minute)

		res := fx.press(p.ID, fakemattermost.AliceUserID)
		if res.PersonStatus != http.StatusOK || res.Status != http.StatusOK || res.Error != "" ||
			string(res.Answer) != answer {
			t.Errorf("press = %+v", res)
		}
		got := rc.received()
		if len(got) != 1 {
			t.Fatalf("the receiver got %d presses, want 1", len(got))
		}
		req := got[0]
		want := map[string]string{
			"user_id":      fakemattermost.AliceUserID,
			"user_name":    fakemattermost.AliceUsername,
			"channel_id":   fakemattermost.ChannelAlerts,
			"channel_name": "alerts",
			"team_id":      fakemattermost.TeamID,
			"team_domain":  fakemattermost.TeamName,
			"post_id":      p.ID,
			"type":         "",
			"data_source":  "",
		}
		for k, v := range want {
			if s := str(t, req[k]); s != v {
				t.Errorf("%s = %q, want %q", k, s, v)
			}
		}
		if len(str(t, req["trigger_id"])) != 26 {
			t.Errorf("trigger_id = %s", req["trigger_id"])
		}
		if string(req["context"]) != `{"action":"ack","group":"G-1"}` {
			t.Errorf("context = %s", req["context"])
		}
		if len(req) != len(want)+2 {
			t.Errorf("the press carries %d fields, want %d: %v", len(req), len(want)+2, req)
		}
		if ct := rc.headers[0].Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}

		edited := fx.getPost(p.ID)
		if edited.EditAt != fx.clock.Now().UnixMilli() || edited.UpdateAt != edited.EditAt ||
			!strings.Contains(string(edited.Props), "Acknowledged by @bob") {
			t.Errorf("the post after the update = %+v, want it edited at once", edited)
		}
		if n := fx.notifications(); len(n) != 0 {
			t.Errorf("the update notified %+v", n)
		}
		presses := fx.fake.Presses()
		if len(presses) != 1 || presses[0].URL != rc.srv.URL+"/press" || presses[0].Status != http.StatusOK ||
			!strings.Contains(string(presses[0].Request), p.ID) || string(presses[0].Answer) != answer {
			t.Errorf("presses %+v", presses)
		}
	})

	t.Run("F-024", func(t *testing.T) {
		fx := start(t)
		fx.setConfig(`{"allowed_untrusted_internal_connections":"127.0.0.1"}`)
		rc := newReceiver(t, http.StatusOK,
			`{"goto_location":"https://example.org/","skip_slack_parsing":true,"extra":{"a":1}}`)
		p := fx.post(fakemattermost.ChannelAlerts, "firing", "", buttons(rc.srv.URL))
		if res := fx.press(p.ID, fakemattermost.BobUserID); res.PersonStatus != http.StatusOK {
			t.Errorf("press = %+v", res)
		}
		if got := fx.getPost(p.ID); got.EditAt != 0 || got.Message != "firing" {
			t.Errorf("the post changed: %+v", got)
		}
		if e := fx.ephemeral(); len(e) != 0 {
			t.Errorf("ephemeral posts %+v, want none", e)
		}
		rc.set(http.StatusOK, "")
		if res := fx.press(p.ID, fakemattermost.BobUserID); res.PersonStatus != http.StatusOK ||
			string(res.Answer) != "null" {
			t.Errorf("press with an empty answer = %+v", res)
		}
	})

	t.Run("F-025", func(t *testing.T) {
		fx := start(t)
		fx.setConfig(`{"allowed_untrusted_internal_connections":"127.0.0.1"}`)
		rc := newReceiver(t, http.StatusOK, `{"ephemeral_text":"You acknowledged #12"}`)
		root := fx.post(fakemattermost.ChannelAlerts, "", "", buttons(rc.srv.URL))
		reply := fx.post(fakemattermost.ChannelAlerts, "", root.ID, buttons(rc.srv.URL))
		fx.press(root.ID, fakemattermost.AliceUserID)
		fx.press(reply.ID, fakemattermost.BobUserID)
		e := fx.ephemeral()
		if len(e) != 2 {
			t.Fatalf("ephemeral posts %+v, want 2", e)
		}
		for i, want := range []struct{ user string }{{fakemattermost.AliceUserID}, {fakemattermost.BobUserID}} {
			if e[i].UserID != want.user || e[i].From != "System" || e[i].RootID != root.ID ||
				e[i].ShownIn != fakemattermost.ShownInThread || e[i].Message != "You acknowledged #12" ||
				e[i].ChannelID != fakemattermost.ChannelAlerts || len(e[i].PostID) != 26 {
				t.Errorf("ephemeral %d = %+v", i, e[i])
			}
		}
		if n := fx.notifications(); len(n) != 0 {
			t.Errorf("ephemeral text notified %+v", n)
		}
	})

	t.Run("F-064", func(t *testing.T) {
		fx := start(t)
		got := decode[[]fakemattermost.Role](t, fx.api(http.MethodPost, "/api/v4/roles/names",
			`["system_user","system_admin","nobody"]`))
		if len(got) != 2 || got[0].Name != "system_user" || slices.Contains(got[0].Permissions, "create_post_ephemeral") ||
			got[1].Name != "system_admin" || !slices.Contains(got[1].Permissions, "create_post_ephemeral") {
			t.Errorf("roles %+v", got)
		}
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/roles/names", `[]`), http.StatusBadRequest,
			"api.context.invalid_body_param.app_error")
	})

	t.Run("F-063", func(t *testing.T) {
		fx := start(t)
		if me := decode[fakemattermost.User](t, fx.api(http.MethodGet, "/api/v4/users/me", "")); me.Roles !=
			"system_user" {
			t.Errorf("the bot's roles %q", me.Roles)
		}
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts/ephemeral",
			`{"user_id":"u-alice","post":{"channel_id":"ch-alerts","message":"Only you"}}`),
			http.StatusForbidden, "api.context.permissions.app_error")
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts/ephemeral",
			`{"user_id":"u-alice","post":{"channel_id":"ch-gone","message":"x"}}`),
			http.StatusForbidden, "api.context.permissions.app_error")
		if e := fx.fake.Ephemeral(); len(e) != 0 {
			t.Errorf("ephemeral posts %+v", e)
		}
	})

	t.Run("F-026", func(t *testing.T) {
		fx := start(t)
		fx.control(http.MethodPut, "/_fake/config", `{"bot_system_admin":true}`)
		if me := decode[fakemattermost.User](t, fx.api(http.MethodGet, "/api/v4/users/me", "")); me.Roles !=
			"system_admin system_user" {
			t.Errorf("the admin bot's roles %q", me.Roles)
		}
		root := fx.post(fakemattermost.ChannelAlerts, "root", "", "")
		r := fx.api(http.MethodPost, "/api/v4/posts/ephemeral",
			`{"user_id":"u-alice","post":{"channel_id":"ch-alerts","message":"Only you"}}`)
		if r.status != http.StatusCreated {
			t.Fatalf("ephemeral post = %s", r)
		}
		p := decode[fakemattermost.Post](t, r)
		if p.Type != "system_ephemeral" || p.UserID != fakemattermost.BotUserID || p.Message != "Only you" ||
			len(p.ID) != 26 {
			t.Errorf("ephemeral post %+v", p)
		}
		fx.api(http.MethodPost, "/api/v4/posts/ephemeral",
			`{"user_id":"u-bob","post":{"channel_id":"ch-alerts","message":"In the thread","root_id":"`+root.ID+`"}}`)
		e := fx.fake.Ephemeral()
		if len(e) != 2 || e[0].ShownIn != fakemattermost.ShownInChannel || e[0].UserID != fakemattermost.AliceUserID ||
			e[0].From != fakemattermost.BotUsername || e[0].PostID != p.ID ||
			e[1].ShownIn != fakemattermost.ShownInThread || e[1].RootID != root.ID {
			t.Errorf("ephemeral posts %+v", e)
		}
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts/ephemeral",
			`{"user_id":"u-alice","post":{"channel_id":"ch-gone","message":"x"}}`),
			http.StatusNotFound, "app.channel.get.existing.app_error")
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts/ephemeral",
			`{"user_id":"u-nobody","post":{"channel_id":"ch-alerts","message":"x"}}`),
			http.StatusBadRequest, "api.context.invalid_body_param.app_error")
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts/ephemeral", `[`),
			http.StatusBadRequest, "api.context.invalid_body_param.app_error")
	})

	t.Run("F-027", func(t *testing.T) {
		fx := start(t)
		root := fx.post(fakemattermost.ChannelAlerts, "root", "", "")
		reply := fx.post(fakemattermost.ChannelAlerts, "@bob, please look.", root.ID, "")
		fx.post(fakemattermost.ChannelAlerts, "second", root.ID, "")
		if reply.RootID != root.ID {
			t.Errorf("reply %+v", reply)
		}
		if got := fx.getPost(root.ID); got.ReplyCount != 2 {
			t.Errorf("root reply_count = %d, want 2", got.ReplyCount)
		}
		posts := fx.fake.Posts()
		if len(posts) != 3 || posts[0].ID != root.ID ||
			!slices.Equal(posts[0].Followers, []string{fakemattermost.BotUserID, fakemattermost.BobUserID}) {
			t.Errorf("posts %+v, want bob following the root's thread", posts)
		}
		n := fx.notifications()
		if len(n) != 1 || n[0].UserID != fakemattermost.BobUserID || n[0].Kind != fakemattermost.KindMention ||
			n[0].RootID != root.ID || n[0].PostID != reply.ID || n[0].Username != "bob" {
			t.Errorf("notifications %+v", n)
		}

		other := fx.post(fakemattermost.ChannelAlertsProd, "elsewhere", "", "")
		for _, rootID := range []string{"nosuchpost", reply.ID, other.ID} {
			body := `{"channel_id":"ch-alerts","message":"x","root_id":"` + rootID + `"}`
			wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts", body),
				http.StatusBadRequest, "api.post.create_post.root_id.app_error")
		}
	})

	t.Run("F-028", func(t *testing.T) {
		fx := start(t)
		r := fx.api(http.MethodPost, "/api/v4/channels/direct", `["u-alice","musterdevbotuserfake000000"]`)
		if r.status != http.StatusCreated {
			t.Fatalf("direct channel = %s", r)
		}
		c := decode[fakemattermost.Channel](t, r)
		if c.Type != "D" || c.TeamID != "" || c.ID != fakemattermost.BotUserID+"__"+fakemattermost.AliceUserID {
			t.Errorf("direct channel %+v", c)
		}
		again := decode[fakemattermost.Channel](t, fx.api(http.MethodPost, "/api/v4/channels/direct",
			`["musterdevbotuserfake000000","u-alice"]`))
		if again.ID != c.ID {
			t.Errorf("a second call gave %s, want %s", again.ID, c.ID)
		}
		p := fx.post(c.ID, "You are on call", "", "")
		n := fx.notifications()
		if len(n) != 1 || n[0].UserID != fakemattermost.AliceUserID || n[0].Kind != fakemattermost.KindDirect ||
			n[0].Preview != "You are on call" || n[0].PostID != p.ID {
			t.Errorf("notifications %+v", n)
		}
		if r := fx.api(http.MethodGet, "/api/v4/channels/"+c.ID, ""); r.status != http.StatusOK {
			t.Errorf("GET the direct channel = %s", r)
		}
		for _, body := range []string{`["u-alice","u-nobody"]`, `["u-alice"]`, `{}`} {
			wantAppError(t, fx.api(http.MethodPost, "/api/v4/channels/direct", body),
				http.StatusBadRequest, "api.context.invalid_body_param.app_error")
		}
		people := decode[fakemattermost.Channel](t, fx.api(http.MethodPost, "/api/v4/channels/direct",
			`["u-alice","u-bob"]`))
		wantAppError(t, fx.api(http.MethodGet, "/api/v4/channels/"+people.ID, ""),
			http.StatusForbidden, "api.context.permissions.app_error")
	})

	t.Run("F-029", func(t *testing.T) {
		fx := start(t)
		p := fx.post(fakemattermost.ChannelAlerts, "firing", "", "")
		r := fx.api(http.MethodPut, "/api/v4/posts/"+p.ID+"/patch", `{"message":"firing @alice"}`)
		if r.status != http.StatusOK {
			t.Fatalf("patch = %s", r)
		}
		if got := decode[fakemattermost.Post](t, r); got.Message != "firing @alice" || got.EditAt == 0 {
			t.Errorf("patched %+v", got)
		}
		if n := fx.notifications(); len(n) != 0 {
			t.Errorf("an edit notified %+v", n)
		}
		fx.post(fakemattermost.ChannelAlerts, "firing @Alice", "", "")
		if n := fx.notifications(); len(n) != 1 || n[0].UserID != fakemattermost.AliceUserID ||
			n[0].Kind != fakemattermost.KindMention || n[0].Preview != "firing @Alice" {
			t.Errorf("a new post with a Mention notified %+v", n)
		}
	})

	t.Run("F-030", func(t *testing.T) {
		fx := start(t)
		send := func(remote string) *httptest.ResponseRecorder {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v4/users/me", nil)
			req.RemoteAddr = remote
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			fx.fake.ServeHTTP(rec, req)
			return rec
		}
		for i := range 300 {
			if rec := send("192.0.2.1:1000"); rec.Code != http.StatusOK || rec.Header().Get("X-Ratelimit-Limit") != "" {
				t.Fatalf("request %d with the limit off = %d, X-Ratelimit-Limit %q", i, rec.Code,
					rec.Header().Get("X-Ratelimit-Limit"))
			}
		}
		fx.setConfig(`{"rate_limit":{"enabled":true}}`)
		allowed := 0
		for i := range 300 {
			if send("192.0.2.1:"+strconv.Itoa(2000+i)).Code == http.StatusOK {
				allowed++
			}
		}
		if allowed != fakemattermost.RateLimitBurst+1 {
			t.Errorf("%d of 300 requests at once allowed, want %d", allowed, fakemattermost.RateLimitBurst+1)
		}
		if rec := send("192.0.2.2:1000"); rec.Code != http.StatusOK {
			t.Errorf("another address shares the bucket: %d", rec.Code)
		}
		fx.clock.Advance(100 * time.Millisecond)
		if rec := send("192.0.2.1:1000"); rec.Code != http.StatusOK {
			t.Errorf("after 100ms: %d, want one more request allowed", rec.Code)
		}
		if rec := send("192.0.2.1:1000"); rec.Code != http.StatusTooManyRequests {
			t.Errorf("the second request after 100ms: %d, want 429", rec.Code)
		}
		fx.clock.Advance(time.Second)
		allowed = 0
		for range 20 {
			if send("192.0.2.1:1000").Code == http.StatusOK {
				allowed++
			}
		}
		if allowed != fakemattermost.RateLimitPerSecond {
			t.Errorf("%d requests allowed after a second, want %d", allowed, fakemattermost.RateLimitPerSecond)
		}
		fx.setConfig(`{"rate_limit":{"enabled":false}}`)
		if rec := send("192.0.2.1:1000"); rec.Code != http.StatusOK {
			t.Errorf("with the limit off again: %d", rec.Code)
		}
	})

	t.Run("F-031", func(t *testing.T) {
		fx := start(t)
		fx.setConfig(`{"rate_limit":{"enabled":true}}`)
		var last response
		for i := range fakemattermost.RateLimitBurst + 1 {
			last = fx.api(http.MethodGet, "/api/v4/users/me", "")
			if last.status != http.StatusOK || last.header.Get("X-Ratelimit-Limit") != "101" ||
				last.header.Get("X-Ratelimit-Remaining") != strconv.Itoa(fakemattermost.RateLimitBurst-i) ||
				last.header.Get("X-Ratelimit-Reset") == "" {
				t.Fatalf("request %d = %s, headers %v", i, last, last.header)
			}
		}
		r := do(t, http.MethodGet, fx.fake.URL()+"/api/v4/users/me", "")
		if r.status != http.StatusTooManyRequests || string(r.body) != "limit exceeded" ||
			r.header.Get("Content-Type") != "text/plain; charset=utf-8" || r.header.Get("Retry-After") != "1" ||
			r.header.Get("X-Ratelimit-Limit") != "101" || r.header.Get("X-Ratelimit-Remaining") != "0" ||
			r.header.Get("X-Ratelimit-Reset") != "11" {
			t.Errorf("429 answer %s, headers %v", r, r.header)
		}
	})

	t.Run("F-032", func(t *testing.T) {
		if got := fakemattermost.New().PressTimeout; got != 30*time.Second {
			t.Errorf("default PressTimeout = %v, want 30s", got)
		}
		fx := start(t)
		fx.fake.PressTimeout = 100 * time.Millisecond
		fx.setConfig(`{"allowed_untrusted_internal_connections":"127.0.0.1"}`)
		rc := newReceiver(t, 0, "")
		p := fx.post(fakemattermost.ChannelAlerts, "", "", buttons(rc.srv.URL))
		begin := time.Now()
		res := fx.press(p.ID, fakemattermost.AliceUserID)
		if elapsed := time.Since(begin); elapsed > 3*time.Second {
			t.Errorf("the press took %v with a 100ms timeout", elapsed)
		}
		if res.PersonStatus != http.StatusBadRequest || res.Status != 0 {
			t.Errorf("press past the timeout = %+v", res)
		}
		if log := fx.serverLog(); len(log) != 1 || !strings.Contains(log[0].Message, "deadline exceeded") {
			t.Errorf("server log %+v", log)
		}

		rc.set(http.StatusInternalServerError, `{"error":"boom"}`)
		res = fx.press(p.ID, fakemattermost.AliceUserID)
		if res.PersonStatus != http.StatusBadRequest || res.Status != http.StatusInternalServerError ||
			string(res.Answer) != `{"error":"boom"}` {
			t.Errorf("press answered 500 = %+v", res)
		}

		fx.clock.Advance(400 * 24 * time.Hour)
		if r := fx.api(http.MethodPut, "/api/v4/posts/"+p.ID+"/patch", `{"message":"still editable"}`); r.status !=
			http.StatusOK {
			t.Errorf("edit of an old post = %s", r)
		}
	})

	t.Run("F-054", func(t *testing.T) {
		fx := start(t)
		fx.setConfig(`{"allowed_untrusted_internal_connections":"127.0.0.1"}`)
		rc := newReceiver(t, http.StatusOK, `{}`)
		p := fx.post(fakemattermost.ChannelAlerts, "", "", buttons(rc.srv.URL))
		r := fx.api(http.MethodPost, "/api/v4/posts/"+p.ID+"/actions/ack", `{}`)
		if r.status != http.StatusOK {
			t.Fatalf("bot press = %s", r)
		}
		ok := decode[map[string]string](t, r)
		got := rc.received()
		if ok["status"] != "OK" || len(got) != 1 || str(t, got[0]["trigger_id"]) != ok["trigger_id"] ||
			str(t, got[0]["user_name"]) != fakemattermost.BotUsername ||
			str(t, got[0]["user_id"]) != fakemattermost.BotUserID {
			t.Errorf("answer %v, press %v", ok, got)
		}

		rc.set(http.StatusInternalServerError, "")
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts/"+p.ID+"/actions/ack", `{}`),
			http.StatusBadRequest, "api.post.do_action.action_integration.app_error")
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts/"+p.ID+"/actions/nope", `{}`),
			http.StatusNotFound, "api.post.do_action.action_id.app_error")
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts/nosuchpost/actions/ack", `{}`),
			http.StatusNotFound, "app.post.get.app_error")
	})

	t.Run("F-055", func(t *testing.T) {
		fx := start(t)
		fx.setConfig(`{"allowed_untrusted_internal_connections":"127.0.0.1"}`)
		rc := newReceiver(t, http.StatusOK, `{"update":{"message":"done"}}`)
		root := fx.post(fakemattermost.ChannelAlerts, "root", "", "")
		reply := fx.post(fakemattermost.ChannelAlerts, "", root.ID, buttons(rc.srv.URL))
		if res := fx.press(reply.ID, fakemattermost.AliceUserID); res.PersonStatus != http.StatusOK {
			t.Errorf("press on a reply = %+v", res)
		}
		if got := rc.received(); len(got) != 1 || str(t, got[0]["post_id"]) != reply.ID {
			t.Errorf("presses %v", got)
		}
		if got := fx.getPost(reply.ID); got.Message != "done" || string(got.Props) != "{}" || got.EditAt == 0 {
			t.Errorf("the reply after the update = %+v", got)
		}
	})

	t.Run("F-056", func(t *testing.T) {
		fx := start(t)
		fx.post(fakemattermost.ChannelAlerts, "", "", `{"attachments":[{"text":"Escalated to @alice"}]}`)
		fx.post(fakemattermost.ChannelAlerts, "@bob above", "", `{"attachments":[{"text":"details"}]}`)
		fx.post(fakemattermost.ChannelAlerts, "", "",
			`{"attachments":[{"pretext":"x","title":"y","fields":[{"title":"On call","value":"@bob"},{"value":3}]}]}`)
		n := fx.fake.Notifications()
		if len(n) != 3 {
			t.Fatalf("notifications %+v, want 3", n)
		}
		if n[0].UserID != fakemattermost.AliceUserID || n[0].Kind != fakemattermost.KindMention || n[0].Preview != "" {
			t.Errorf("the attachment Mention = %+v, want alice with an empty preview", n[0])
		}
		if n[1].UserID != fakemattermost.BobUserID || n[1].Preview != "@bob above" {
			t.Errorf("the message Mention = %+v", n[1])
		}
		if n[2].UserID != fakemattermost.BobUserID || n[2].Preview != "" {
			t.Errorf("the field Mention = %+v", n[2])
		}
	})

	t.Run("F-057", func(t *testing.T) {
		fx := start(t)
		r := fx.api(http.MethodGet, "/api/v4/config/client?format=old", "")
		cfg := decode[map[string]string](t, r)
		if r.status != http.StatusOK || cfg["MaxPostSize"] != "16383" || cfg["Version"] != "11.2.2" {
			t.Errorf("client config = %s", r)
		}
		wantAppError(t, fx.api(http.MethodGet, "/api/v4/config/client", ""),
			http.StatusNotImplemented, "fake.not_implemented")

		longest := strings.Repeat("é", fakemattermost.MaxPostSize)
		p := fx.post(fakemattermost.ChannelAlerts, longest, "", "")
		body := `{"channel_id":"ch-alerts","message":"` + longest + `x"}`
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts", body),
			http.StatusBadRequest, "model.post.is_valid.message_length.app_error")
		wantAppError(t, fx.api(http.MethodPut, "/api/v4/posts/"+p.ID+"/patch", `{"message":"`+longest+`x"}`),
			http.StatusBadRequest, "model.post.is_valid.message_length.app_error")
	})

	t.Run("F-058", func(t *testing.T) {
		fx := start(t)
		root := fx.post(fakemattermost.ChannelAlerts, "root", "", "")
		live := fx.post(fakemattermost.ChannelAlerts, "live", "", "")
		fx.clock.Advance(time.Minute)
		if r := fx.control(http.MethodDelete, "/_fake/posts/"+root.ID, ""); r.status != http.StatusNoContent {
			t.Fatalf("delete = %s", r)
		}
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts",
			`{"channel_id":"ch-alerts","message":"reply","root_id":"`+root.ID+`"}`),
			http.StatusBadRequest, "api.post.create_post.root_id.app_error")
		wantAppError(t, fx.api(http.MethodPut, "/api/v4/posts/"+root.ID+"/patch", `{"message":"edit"}`),
			http.StatusForbidden, "api.context.permissions.app_error")
		wantAppError(t, fx.api(http.MethodGet, "/api/v4/posts/"+root.ID, ""),
			http.StatusNotFound, "app.post.get.app_error")
		r := fx.api(http.MethodGet, "/api/v4/posts/"+root.ID+"?include_deleted=true", "")
		if got := decode[fakemattermost.Post](t, r); r.status != http.StatusOK ||
			got.DeleteAt != fx.clock.Now().UnixMilli() {
			t.Errorf("read with include_deleted = %s", r)
		}
		if got := fx.getPost(live.ID); got.DeleteAt != 0 {
			t.Errorf("live post %+v", got)
		}
		e := wantAppError(t, fx.api(http.MethodPut, "/api/v4/posts/nosuchpost/patch", `{"message":"x"}`),
			http.StatusNotFound, "app.post.get.app_error")
		if e.Message != "post not found" {
			t.Errorf("message %q", e.Message)
		}
	})
}

func TestAuth(t *testing.T) {
	fx := start(t)
	for _, header := range [][]string{nil, {"Authorization", "Bearer "}, {"Authorization", "Basic abc"}} {
		e := wantAppError(t, do(t, http.MethodGet, fx.fake.URL()+"/api/v4/users/me", "", header...),
			http.StatusUnauthorized, "api.context.session_expired.app_error")
		if e.Message != "Invalid or expired session, please login again." || e.DetailedError != "" {
			t.Errorf("401 %+v", e)
		}
	}
	r := do(t, http.MethodGet, fx.fake.URL()+"/api/v4/users/me", "", "Authorization", "bearer anything")
	me := decode[fakemattermost.User](t, r)
	if r.status != http.StatusOK || me.ID != fakemattermost.BotUserID || me.Username != fakemattermost.BotUsername ||
		!me.IsBot || me.Roles != "system_user" {
		t.Errorf("users/me = %s", r)
	}

	fx.setConfig(`{"revoked_tokens":["old"]}`)
	wantAppError(t, do(t, http.MethodGet, fx.fake.URL()+"/api/v4/users/me", "", "Authorization", "Bearer old"),
		http.StatusUnauthorized, "api.context.session_expired.app_error")
	if r := fx.api(http.MethodGet, "/api/v4/users/me", ""); r.status != http.StatusOK {
		t.Errorf("another token = %s", r)
	}
	cfg := decode[fakemattermost.Config](t, fx.control(http.MethodGet, "/_fake/config", ""))
	if !slices.Equal(cfg.RevokedTokens, []string{"old"}) || cfg.RateLimit.Enabled ||
		cfg.AllowedUntrustedInternalConnections != "" {
		t.Errorf("config %+v", cfg)
	}
	if r := fx.control(http.MethodPut, "/_fake/config", `{"nope":1}`); r.status != http.StatusBadRequest {
		t.Errorf("PUT /_fake/config with an unknown field = %s", r)
	}
}

func TestNotImplemented(t *testing.T) {
	fx := start(t)
	wantAppError(t, do(t, http.MethodPost, fx.fake.URL()+"/hooks/abc", `{"text":"x"}`),
		http.StatusNotImplemented, "fake.not_implemented")
	wantAppError(t, fx.api(http.MethodGet, "/api/v4/system/ping", ""), http.StatusNotImplemented, "fake.not_implemented")
	wantAppError(t, fx.api(http.MethodDelete, "/api/v4/posts", ""), http.StatusNotImplemented, "fake.not_implemented")
	wantAppError(t, do(t, http.MethodGet, fx.fake.URL()+"/api/v4/system/ping", ""),
		http.StatusUnauthorized, "api.context.session_expired.app_error")
	reqs := fx.fake.Requests()
	if len(reqs) != 4 || reqs[0].Path != "/hooks/abc" || reqs[0].Status != http.StatusNotImplemented {
		t.Errorf("recorded %+v", reqs)
	}
}

func TestTeamsAndChannels(t *testing.T) {
	fx := start(t)
	teams := decode[[]fakemattermost.Team](t, fx.api(http.MethodGet, "/api/v4/users/me/teams", ""))
	if len(teams) != 1 || teams[0].ID != fakemattermost.TeamID || teams[0].Name != "dev" || teams[0].Type != "O" {
		t.Errorf("teams %+v", teams)
	}
	if got := decode[fakemattermost.Team](t, fx.api(http.MethodGet, "/api/v4/teams/team-dev", "")); got != teams[0] {
		t.Errorf("team %+v", got)
	}
	wantAppError(t, fx.api(http.MethodGet, "/api/v4/teams/nope", ""), http.StatusNotFound, "app.team.get.find.app_error")
	wantAppError(t, fx.api(http.MethodGet, "/api/v4/users/me/teams/nope/channels", ""),
		http.StatusNotFound, "app.team.get.find.app_error")

	names := func() []string {
		cs := decode[[]fakemattermost.Channel](t, fx.api(http.MethodGet, "/api/v4/users/me/teams/team-dev/channels", ""))
		out := []string{}
		for _, c := range cs {
			out = append(out, c.Name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"alerts", "alerts-prod"}) {
		t.Errorf("channels %v", got)
	}

	c := decode[fakemattermost.Channel](t, fx.api(http.MethodGet, "/api/v4/channels/ch-nobot", ""))
	if c.ID != fakemattermost.ChannelNoBot || c.Name != "no-bot" || c.DisplayName != "No bot" ||
		c.TeamID != fakemattermost.TeamID || c.Type != "O" {
		t.Errorf("channel %+v", c)
	}
	e := wantAppError(t, fx.api(http.MethodGet, "/api/v4/channels/ch-gone", ""),
		http.StatusNotFound, "app.channel.get.existing.app_error")
	if !strings.Contains(e.Message, "ch-gone") {
		t.Errorf("message %q does not name the channel", e.Message)
	}

	r := fx.api(http.MethodGet, "/api/v4/channels/ch-alerts/members/me", "")
	if m := decode[map[string]string](t, r); r.status != http.StatusOK || m["channel_id"] != "ch-alerts" ||
		m["user_id"] != fakemattermost.BotUserID || m["roles"] != "channel_user" {
		t.Errorf("membership %s", r)
	}
	wantAppError(t, fx.api(http.MethodGet, "/api/v4/channels/ch-nobot/members/me", ""),
		http.StatusNotFound, "app.channel.get_member.missing.app_error")
	wantAppError(t, fx.api(http.MethodGet, "/api/v4/channels/ch-gone/members/me", ""),
		http.StatusNotFound, "app.channel.get.existing.app_error")

	if r := fx.control(http.MethodPost, "/_fake/channels/ch-alerts-prod/archive", ""); r.status != http.StatusNoContent {
		t.Fatalf("archive = %s", r)
	}
	if got := names(); !slices.Equal(got, []string{"alerts"}) {
		t.Errorf("channels after the archive %v", got)
	}
	if r := fx.api(http.MethodGet, "/api/v4/channels/ch-alerts-prod/members/me", ""); r.status != http.StatusOK {
		t.Errorf("membership of an archived channel = %s", r)
	}
	archived := decode[fakemattermost.Channel](t, fx.api(http.MethodGet, "/api/v4/channels/ch-alerts-prod", ""))
	if archived.DeleteAt == 0 {
		t.Errorf("archived channel %+v", archived)
	}
	if r := fx.control(http.MethodPost, "/_fake/channels/ch-gone/archive", ""); r.status != http.StatusNotFound {
		t.Errorf("archive of an unknown channel = %s", r)
	}
}

func TestPostChecks(t *testing.T) {
	fx := start(t)
	p := fx.post(fakemattermost.ChannelAlerts, "first", "", `{"attachments":[]}`)
	if p.UserID != fakemattermost.BotUserID || p.CreateAt != fx.clock.Now().UnixMilli() || p.UpdateAt != p.CreateAt ||
		p.EditAt != 0 || p.Type != "" || string(p.Props) != `{"attachments":[]}` || len(p.ID) != 26 {
		t.Errorf("post %+v", p)
	}
	wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts", `{"channel_id":"ch-nobot","message":"x"}`),
		http.StatusForbidden, "api.context.permissions.app_error")
	e := wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts", `{"channel_id":"ch-gone","message":"x"}`),
		http.StatusNotFound, "app.channel.get.existing.app_error")
	if !strings.Contains(e.Message, "ch-gone") {
		t.Errorf("message %q does not name the channel", e.Message)
	}
	for _, body := range []string{`{`, `{"channel_id":"ch-alerts","props":[1]}`} {
		wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts", body),
			http.StatusBadRequest, "api.context.invalid_body_param.app_error")
	}
	for _, body := range []string{`{`, `{"props":"x"}`} {
		wantAppError(t, fx.api(http.MethodPut, "/api/v4/posts/"+p.ID+"/patch", body),
			http.StatusBadRequest, "api.context.invalid_body_param.app_error")
	}
	r := fx.api(http.MethodPut, "/api/v4/posts/"+p.ID+"/patch", `{"props":{"a":1}}`)
	if got := decode[fakemattermost.Post](t, r); got.Message != "first" || string(got.Props) != `{"a":1}` {
		t.Errorf("patch of the props only = %s", r)
	}

	if r := fx.control(http.MethodDelete, "/_fake/channels/ch-alerts/members/"+fakemattermost.BotUserID, ""); r.status !=
		http.StatusNoContent {
		t.Fatalf("remove the bot = %s", r)
	}
	wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts", `{"channel_id":"ch-alerts","message":"x"}`),
		http.StatusForbidden, "api.context.permissions.app_error")
	wantAppError(t, fx.api(http.MethodPut, "/api/v4/posts/"+p.ID+"/patch", `{"message":"x"}`),
		http.StatusForbidden, "api.context.permissions.app_error")
	if r := fx.control(http.MethodPut, "/_fake/channels/ch-alerts/members/"+fakemattermost.BotUserID, ""); r.status !=
		http.StatusNoContent {
		t.Fatalf("add the bot = %s", r)
	}
	fx.post(fakemattermost.ChannelAlerts, "back", "", "")

	fx.control(http.MethodPost, "/_fake/channels/ch-alerts/archive", "")
	wantAppError(t, fx.api(http.MethodPost, "/api/v4/posts", `{"channel_id":"ch-alerts","message":"x"}`),
		http.StatusNotFound, "app.channel.get.existing.app_error")

	for _, path := range []string{"/_fake/channels/ch-gone/members/u-bob", "/_fake/channels/ch-alerts/members/u-nobody"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			if r := fx.control(method, path, ""); r.status != http.StatusNotFound {
				t.Errorf("%s %s = %s, want 404", method, path, r)
			}
		}
	}
}

func TestMentions(t *testing.T) {
	fx := start(t)
	tests := []struct {
		message string
		want    map[string]string
	}{
		{"@channel firing", map[string]string{"alice": "channel", "bob": "channel"}},
		{"@all and @here", map[string]string{"alice": "all", "bob": "all"}},
		{"@here", map[string]string{"alice": "here", "bob": "here"}},
		{"@channel, @alice!", map[string]string{"alice": "mention", "bob": "channel"}},
		{"mail alice@example.org", map[string]string{}},
		{"@alicex @muster-dev-bot", map[string]string{}},
		{"(@bob)", map[string]string{"bob": "mention"}},
	}
	for _, tt := range tests {
		fx.control(http.MethodDelete, "/_fake/notifications", "")
		fx.post(fakemattermost.ChannelAlerts, tt.message, "", "")
		got := map[string]string{}
		for _, n := range fx.notifications() {
			got[n.Username] = n.Kind
		}
		if len(got) != len(tt.want) {
			t.Errorf("%q notified %v, want %v", tt.message, got, tt.want)
			continue
		}
		for k, v := range tt.want {
			if got[k] != v {
				t.Errorf("%q notified %v, want %v", tt.message, got, tt.want)
			}
		}
	}

	fx.control(http.MethodDelete, "/_fake/channels/ch-alerts/members/u-bob", "")
	fx.control(http.MethodDelete, "/_fake/notifications", "")
	fx.post(fakemattermost.ChannelAlerts, "@bob @channel", "", "")
	if n := fx.notifications(); len(n) != 1 || n[0].Username != "alice" {
		t.Errorf("a Mention of a non-member notified %+v", n)
	}
}

func TestPressErrors(t *testing.T) {
	fx := start(t)
	fx.setConfig(`{"allowed_untrusted_internal_connections":"127.0.0.1"}`)
	rc := newReceiver(t, http.StatusOK, `not json`)
	p := fx.post(fakemattermost.ChannelAlerts, "", "", buttons(rc.srv.URL))
	for _, tt := range []struct {
		body   string
		status int
	}{
		{`{"post_id":"nope","action_id":"ack","user_id":"u-alice"}`, http.StatusNotFound},
		{`{"post_id":"` + p.ID + `","action_id":"nope","user_id":"u-alice"}`, http.StatusNotFound},
		{`{"post_id":"` + p.ID + `","action_id":"nourl","user_id":"u-alice"}`, http.StatusNotFound},
		{`{"post_id":"` + p.ID + `","action_id":"ack","user_id":"u-nobody"}`, http.StatusBadRequest},
		{`{"post_id":1}`, http.StatusBadRequest},
	} {
		if r := fx.control(http.MethodPost, "/_fake/press", tt.body); r.status != tt.status {
			t.Errorf("press %s = %s, want %d", tt.body, r, tt.status)
		}
	}
	if _, err := fx.fake.Press(t.Context(), "nope", "ack", "u-alice"); !errors.Is(err,
		fakemattermost.ErrUnknownPost) {
		t.Errorf("Press of an unknown post = %v", err)
	}

	res := fx.press(p.ID, fakemattermost.AliceUserID)
	if res.PersonStatus != http.StatusBadRequest || res.Status != http.StatusOK || string(res.Answer) != "null" {
		t.Errorf("press answered with text = %+v", res)
	}
	rc.set(http.StatusOK, `{"update":{"props":[1]}}`)
	if res := fx.press(p.ID, fakemattermost.AliceUserID); res.PersonStatus != http.StatusBadRequest {
		t.Errorf("press answered with bad props = %+v", res)
	}

	for _, target := range []string{"http:///nohost", "http://[::1"} {
		q := fx.post(fakemattermost.ChannelAlerts, "", "", buttons(target))
		if res := fx.press(q.ID, fakemattermost.AliceUserID); res.PersonStatus != http.StatusBadRequest {
			t.Errorf("press to %s = %+v", target, res)
		}
	}
	if log := fx.serverLog(); len(log) != 4 {
		t.Errorf("server log %+v, want 4 lines", log)
	}

	fx.control(http.MethodDelete, "/_fake/posts/"+p.ID, "")
	if r := fx.control(http.MethodPost, "/_fake/press",
		`{"post_id":"`+p.ID+`","action_id":"ack","user_id":"u-alice"}`); r.status != http.StatusNotFound {
		t.Errorf("press on a deleted post = %s", r)
	}
}

func TestPressOnAPostDeletedMeanwhile(t *testing.T) {
	fx := start(t)
	fx.setConfig(`{"allowed_untrusted_internal_connections":"127.0.0.1"}`)
	var postID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodDelete, fx.fake.URL()+"/_fake/posts/"+postID, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
		_, _ = io.WriteString(w, `{"ephemeral_text":"done"}`)
	}))
	t.Cleanup(srv.Close)
	postID = fx.post(fakemattermost.ChannelAlerts, "", "", buttons(srv.URL)).ID
	if res := fx.press(postID, fakemattermost.AliceUserID); res.PersonStatus != http.StatusBadRequest {
		t.Errorf("press on a post deleted during the call = %+v", res)
	}
}

func TestControlLists(t *testing.T) {
	fx := start(t)
	fx.setConfig(`{"allowed_untrusted_internal_connections":"127.0.0.1","rate_limit":{"enabled":true},` +
		`"revoked_tokens":["x"]}`)
	rc := newReceiver(t, http.StatusOK, `{"ephemeral_text":"ok"}`)
	p := fx.post(fakemattermost.ChannelAlerts, "@alice", "", buttons(rc.srv.URL))
	fx.press(p.ID, fakemattermost.AliceUserID)
	fx.fake.SetAllowedUntrustedInternalConnections("")
	fx.press(p.ID, fakemattermost.AliceUserID)
	fx.control(http.MethodDelete, "/_fake/channels/ch-alerts/members/u-bob", "")
	fx.control(http.MethodPost, "/_fake/channels/ch-alerts-prod/archive", "")

	lists := map[string]int{"posts": 1, "notifications": 1, "ephemeral": 1, "presses": 2, "server-log": 1}
	for name, want := range lists {
		got := decode[[]json.RawMessage](t, fx.control(http.MethodGet, "/_fake/"+name, ""))
		if len(got) != want {
			t.Errorf("GET /_fake/%s has %d entries, want %d", name, len(got), want)
		}
		if r := fx.control(http.MethodDelete, "/_fake/"+name, ""); r.status != http.StatusNoContent {
			t.Errorf("DELETE /_fake/%s = %s", name, r)
		}
		if got := decode[[]json.RawMessage](t, fx.control(http.MethodGet, "/_fake/"+name, "")); len(got) != 0 {
			t.Errorf("GET /_fake/%s after the reset has %d entries", name, len(got))
		}
	}
	if r := fx.control(http.MethodDelete, "/_fake/posts/nope", ""); r.status != http.StatusNotFound {
		t.Errorf("delete an unknown post = %s", r)
	}

	if r := fx.control(http.MethodPost, "/_fake/reset", ""); r.status != http.StatusNoContent {
		t.Fatalf("reset = %s", r)
	}
	cfg := fx.fake.Config()
	if cfg.RateLimit.Enabled || len(cfg.RevokedTokens) != 0 || cfg.AllowedUntrustedInternalConnections != "" {
		t.Errorf("config after the reset %+v", cfg)
	}
	fx.post(fakemattermost.ChannelAlertsProd, "@bob", "", "")
	if n := fx.fake.Notifications(); len(n) != 1 || n[0].Username != "bob" {
		t.Errorf("after the reset the archive and the members stay: %+v", n)
	}
	if len(fx.fake.ServerLog()) != 0 || len(fx.fake.Presses()) != 0 || len(fx.fake.Ephemeral()) != 0 ||
		len(fx.fake.Posts()) != 1 {
		t.Error("the reset kept records")
	}
	if len(fx.fake.Requests()) == 0 {
		t.Error("the reset dropped the harness's recorded requests")
	}
}
