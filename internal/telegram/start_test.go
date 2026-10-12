// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// linked is a `/start` the Linker received.
type linked struct {
	conn  accountlinks.TelegramConnection
	token string
	from  accountlinks.TelegramAccount
}

// fakeLinker answers each `/start` with reply, or fails with err.
type fakeLinker struct {
	calls    []linked
	reply    string
	replyErr error
	err      error
}

func (f *fakeLinker) LinkTelegram(ctx context.Context, conn accountlinks.TelegramConnection, token string,
	from accountlinks.TelegramAccount, reply func(context.Context, string) error) error {
	f.calls = append(f.calls, linked{conn, token, from})
	if f.err != nil {
		return f.err
	}
	f.replyErr = reply(ctx, f.reply)
	return nil
}

// startUpdate is a message of the person from in their private chat with the bot.
func startUpdate(t *testing.T, from int64, username, text string, edited, bot bool) Update {
	t.Helper()
	kind := "message"
	if edited {
		kind = "edited_message"
	}
	raw, _ := json.Marshal(map[string]any{"update_id": 1, kind: map[string]any{"message_id": 1,
		"chat": map[string]any{"id": from, "type": "private"}, "text": text,
		"from": map[string]any{"id": from, "is_bot": bot, "first_name": "A", "username": username}}})
	u, err := ParseUpdate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestStart is C-14.FR-8 and C-18.FR-2: `/start <token>` in a private chat, also as `/start@<bot> <token>`, reaches
// the Linker with the account that sent it; the reply goes to that private chat as plain text without link previews
// through the interactive path, limited by the Connection only. Other texts, edits and bots are ignored without a
// reply, and an error of the Linker leaves the update unconfirmed.
func TestStart(t *testing.T) {
	f := startFake(t)
	c := Conn{ID: 5, PublicID: "CN0000000000T1", Client: newClient(t, Settings{BaseURL: f.URL(), Token: testToken})}
	if _, _, err := f.SendPrivate(t.Context(), faketelegram.Private{Token: testToken,
		From: faketelegram.Sender{ID: 7001, Username: "alice_t"}, Text: "/start x"}); err != nil {
		t.Fatal(err)
	}
	links := &fakeLinker{reply: "Linked to Muster user Alice Smith (alice) <b>"}
	path := &recordingPath{path: deliverytest.Unlimited(5, clock.Clocks{Business: clock.NewManual(press0),
		Real: clock.NewManual(press0)})}
	s := &Start{Links: links, Path: path}
	for _, text := range []string{"/start tok-1", " /start@muster_dev_bot   tok-2 "} {
		if err := s.Handle(t.Context(), c, startUpdate(t, 7001, "alice_t", text, false, false)); err != nil {
			t.Fatal(err)
		}
	}
	if len(links.calls) != 2 || links.calls[0] != (linked{accountlinks.TelegramConnection{ID: 5,
		PublicID: "CN0000000000T1"}, "tok-1", accountlinks.TelegramAccount{ID: 7001, Username: "alice_t"}}) ||
		links.calls[1].token != "tok-2" || links.replyErr != nil {
		t.Fatalf("calls %+v, reply %v", links.calls, links.replyErr)
	}
	if len(path.subjects) != 2 || path.subjects[0].Connection == nil || *path.subjects[0].Connection != 5 ||
		path.subjects[0].Destination != nil {
		t.Errorf("subjects %+v", path.subjects)
	}
	msgs := f.Messages(7001)
	if last := msgs[len(msgs)-1]; last.Text != "Linked to Muster user Alice Smith (alice) <b>" ||
		last.ParseMode != "" || last.From.ID != faketelegram.BotID {
		t.Errorf("reply %+v", last)
	}
	sent := false
	for _, r := range f.Requests() {
		if strings.HasSuffix(r.Path, "/sendMessage") {
			sent = strings.Contains(r.Body, `"link_preview_options":{"is_disabled":true}`)
		}
	}
	if !sent {
		t.Error("the reply shows link previews")
	}
	for _, u := range []Update{
		startUpdate(t, 7001, "", "/start", false, false),
		startUpdate(t, 7001, "", "/start a b", false, false),
		startUpdate(t, 7001, "", "hello there", false, false),
		startUpdate(t, 7001, "", "/started tok", false, false),
		startUpdate(t, 7001, "", "/start tok", true, false),
		startUpdate(t, 7001, "", "/start tok", false, true),
		{UpdateID: 2, Message: &Message{Chat: Chat{ID: 7001, Type: "private"}, Text: "/start tok"}},
		{UpdateID: 3, Message: &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 7001},
			Text: "/start tok"}},
	} {
		if err := s.Handle(t.Context(), c, u); err != nil || len(links.calls) != 2 {
			t.Errorf("%+v reached the Linker: %v", u.Message, err)
		}
	}
	// A reply to a private chat the person never opened fails, and so does one without a limiter token.
	var me *accountlinks.MessengerError
	if err := s.Handle(t.Context(), c, startUpdate(t, 7002, "", "/start tok", false, false)); err != nil ||
		!errors.As(links.replyErr, &me) || me.Call != "the reply" || me.Status != 400 ||
		strings.Contains(links.replyErr.Error(), "chat not found") {
		t.Errorf("a reply to an unopened chat = %v", links.replyErr)
	}
	path.err = &delivery.LimitedError{RetryAfter: time.Second}
	if err := s.Handle(t.Context(), c, startUpdate(t, 7001, "", "/start tok", false, false)); err != nil ||
		!errors.As(links.replyErr, new(*delivery.LimitedError)) {
		t.Errorf("no limiter token = %v", links.replyErr)
	}
	path.err = nil
	if err := f.SetFault(fakeserver.Fault{Path: "/bot" + testToken + "/sendMessage", Status: 429, RetryAfterSeconds: 3,
		Times: 1, Body: `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 3",` +
			`"parameters":{"retry_after":3}}`}); err != nil {
		t.Fatal(err)
	}
	var limited *delivery.LimitedError
	if err := s.Handle(t.Context(), c, startUpdate(t, 7001, "", "/start tok", false, false)); err != nil ||
		!errors.As(links.replyErr, &limited) || limited.RetryAfter != 3*time.Second {
		t.Errorf("a 429 = %v", links.replyErr)
	}
	if out := path.outcomes[len(path.outcomes)-1]; out.Kind != delivery.OutcomeRetryAfter ||
		out.Scope != delivery.ScopeConnection {
		t.Errorf("the 429 holds %+v", out)
	}
	links.err = errors.New("database down")
	if err := s.Handle(t.Context(), c, startUpdate(t, 7001, "", "/start tok", false, false)); !errors.Is(err,
		links.err) {
		t.Errorf("a failed link = %v", err)
	}
}

// conns finds one Telegram Connection.
type conns struct {
	conn Conn
	err  error
}

func (f conns) TelegramConn(_ context.Context, id string) (Conn, error) {
	if f.err != nil || id != f.conn.PublicID {
		return Conn{}, errors.Join(errors.New("no such connection"), f.err)
	}
	return f.conn, nil
}

// TestBotsUsername is C-18.FR-2: the username of a bot whose Connection check has not stored it is read with getMe
// on the interactive path; no limiter token and a 429 are a LimitedError, any other failure a MessengerError
// without Telegram's text.
func TestBotsUsername(t *testing.T) {
	f := startFake(t)
	c := Conn{ID: 5, PublicID: "CN0000000000T1", Client: newClient(t, Settings{BaseURL: f.URL(), Token: testToken})}
	path := &recordingPath{path: deliverytest.Unlimited(5, clock.Clocks{Business: clock.NewManual(press0),
		Real: clock.NewManual(press0)})}
	b := Bots{Conns: conns{conn: c}, Path: path}
	if name, err := b.Username(t.Context(), c.PublicID); err != nil || name != faketelegram.BotUsername ||
		*path.subjects[0].Connection != 5 {
		t.Fatalf("Username = %q, %v", name, err)
	}
	if _, err := b.Username(t.Context(), "CN0000000000Z9"); err == nil {
		t.Error("an unknown Connection")
	}
	for _, ft := range []fakeserver.Fault{
		{Path: "/bot" + testToken + "/getMe", Status: 401, Times: 1,
			Body: `{"ok":false,"error_code":401,"description":"Unauthorized"}`},
		{Path: "/bot" + testToken + "/getMe", Status: 200, Times: 1, Body: `{"ok":true,"result":{"id":1}}`},
	} {
		if err := f.SetFault(ft); err != nil {
			t.Fatal(err)
		}
		var me *accountlinks.MessengerError
		if _, err := b.Username(t.Context(), c.PublicID); !errors.As(err, &me) || me.Call != "getMe" ||
			strings.Contains(err.Error(), "Unauthorized") {
			t.Errorf("a failed getMe = %v", err)
		}
	}
	if err := f.SetFault(fakeserver.Fault{Path: "/bot" + testToken + "/getMe", Status: 429, Times: 1,
		Body: `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":4}}`}); err != nil {
		t.Fatal(err)
	}
	var limited *delivery.LimitedError
	if _, err := b.Username(t.Context(), c.PublicID); !errors.As(err, &limited) || limited.RetryAfter != 4*time.Second {
		t.Errorf("a 429 = %v", err)
	}
	path.err = &delivery.LimitedError{RetryAfter: time.Second}
	if _, err := b.Username(t.Context(), c.PublicID); !errors.As(err, &limited) {
		t.Errorf("no limiter token = %v", err)
	}
}
