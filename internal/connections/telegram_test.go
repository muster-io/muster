// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package connections_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/connections"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/telegram"
)

const (
	tgToken      = "777001:AAE-tg-s3cr3t"
	tgOtherToken = "777002:AAE-tg-other"
)

// tgEnv is the env with the fake Telegram server.
type tgEnv struct {
	*env
	tg *faketelegram.Fake
}

func newTGEnv(t *testing.T) *tgEnv {
	t.Helper()
	f := faketelegram.New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	e := &tgEnv{env: newEnv(t), tg: f}
	t.Cleanup(func() {
		for _, s := range []string{tgToken, tgOtherToken} {
			if strings.Contains(e.log.String(), s) {
				t.Errorf("a bot token reached the log: %s", e.log.String())
			}
		}
		for _, h := range f.Webhooks() {
			if strings.Contains(e.log.String(), h.SecretToken) {
				t.Errorf("a webhook secret token reached the log: %s", e.log.String())
			}
		}
	})
	return e
}

func (e *tgEnv) input(name, mode string) connections.Input {
	return connections.Input{Type: connections.TypeTelegram, Name: name, BotAPIBaseURL: e.tg.URL() + "/",
		UpdateMode: mode, BotToken: keyring.Replace(tgToken),
		Limiter: connections.Limiter{Limit: connections.TelegramDefaultLimit, PerSeconds: 1}}
}

// TestTelegramCreate covers C-14.FR-1 and FR-10: a Telegram Connection with a write-only token, a normalized base URL,
// its update mode, limiter and warning; nothing is called in the long-polling mode.
func TestTelegramCreate(t *testing.T) {
	e := newTGEnv(t)
	c, err := e.svc.Create(t.Context(), by, e.input("tg", connections.ModeLongPolling))
	if err != nil {
		t.Fatal(err)
	}
	if c.Type != connections.TypeTelegram || c.BotAPIBaseURL != e.tg.URL() || c.ServerURL != "" ||
		c.UpdateMode != connections.ModeLongPolling || !slices.Equal(c.Warnings, []string{telegram.WarningBaseURLUsesHTTP}) ||
		!c.BotToken.Set || c.Limiter.Limit != 15 {
		t.Fatalf("created %+v", c)
	}
	d := diffOf(t, e.store.audit[0])
	if !secretChanged(d, "/bot_token") || !strings.Contains(string(e.store.audit[0].Diff), `"/bot_api_base_url"`) ||
		!strings.Contains(string(e.store.audit[0].Diff), `"long_polling"`) {
		t.Fatalf("diff %s", e.store.audit[0].Diff)
	}
	if len(e.tg.Requests()) != 0 || e.store.rows[c.ID].row.TelegramWebhookSecretCiphertext != nil {
		t.Fatal("a long-polling Connection called Telegram or has a webhook secret")
	}
	in := e.input("https", connections.ModeLongPolling)
	in.BotAPIBaseURL = "https://tg.example.org/k3x9/"
	h, err := e.svc.Create(t.Context(), by, in)
	if err != nil || h.BotAPIBaseURL != "https://tg.example.org/k3x9" || len(h.Warnings) != 0 || h.Warnings == nil {
		t.Fatalf("https = %+v, %v", h, err)
	}
}

func TestTelegramValidation(t *testing.T) {
	e := newTGEnv(t)
	for name, tc := range map[string]struct {
		change  func(in *connections.Input)
		pointer string
	}{
		"user information": {func(in *connections.Input) { in.BotAPIBaseURL = "https://user:pw@api.example.org/" },
			"/bot_api_base_url"},
		"query":       {func(in *connections.Input) { in.BotAPIBaseURL = "https://api.example.org/?x=1" }, "/bot_api_base_url"},
		"fragment":    {func(in *connections.Input) { in.BotAPIBaseURL = "https://api.example.org/#x" }, "/bot_api_base_url"},
		"not http":    {func(in *connections.Input) { in.BotAPIBaseURL = "ftp://api.example.org" }, "/bot_api_base_url"},
		"update mode": {func(in *connections.Input) { in.UpdateMode = "push" }, "/update_mode"},
		"no token":    {func(in *connections.Input) { in.BotToken = keyring.Keep }, "/bot_token"},
	} {
		in := e.input("tg", connections.ModeLongPolling)
		tc.change(&in)
		if fe := fieldError(func() error { _, err := e.svc.Create(t.Context(), by, in); return err }()); fe == nil ||
			fe.Pointer != tc.pointer {
			t.Errorf("%s = %v", name, fe)
		}
	}
	c, err := e.svc.Create(t.Context(), by, e.input("tg", connections.ModeLongPolling))
	if err != nil {
		t.Fatal(err)
	}
	in := e.input("tg", connections.ModeLongPolling)
	in.BotToken, in.BotAPIBaseURL = keyring.Keep, "https://other.example.org"
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/bot_token" ||
		fe.Detail != "A new Bot API base URL needs the bot token again." {
		t.Errorf("a new base URL without the token = %v", fe)
	}
	in = input("tg", e.fake.URL())
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || fe.Pointer != "/type" {
		t.Errorf("a changed type = %v", fe)
	}
}

// TestTelegramWebhookMode covers C-14.FR-1 and AC-11: the webhook mode sets the webhook at MUSTER_INGEST_URL with a new
// secret token, which Hooked gives the webhook endpoint; leaving it deletes the webhook; a failed call saves nothing.
func TestTelegramWebhookMode(t *testing.T) {
	e := newTGEnv(t)
	c, err := e.svc.Create(t.Context(), by, e.input("tg", connections.ModeWebhook))
	if err != nil {
		t.Fatal(err)
	}
	hook, ok := e.tg.Webhooks()[tgToken]
	want := "http://localhost:8081/api/v1/callbacks/telegram/" + c.PublicID
	if !ok || hook.URL != want || len(hook.SecretToken) != 43 || hook.MaxConnections != 1 ||
		!slices.Equal(hook.AllowedUpdates, telegram.AllowedUpdates) || e.svc.WebhookURL(c.PublicID) != want {
		t.Fatalf("webhook %+v", hook)
	}
	conn, secret, err := e.svc.Hooked(t.Context(), c.PublicID)
	if err != nil || string(secret) != hook.SecretToken || conn.ID != c.ID || conn.Client == nil {
		t.Fatalf("hooked = %+v, %v", conn, err)
	}
	if !strings.Contains(e.log.String(), `"event":"telegram_webhook_set","connection":"`+c.PublicID+
		`","host":"localhost:8081"`) {
		t.Fatalf("log %s", e.log)
	}
	// A save that changes the name keeps the webhook and its secret token.
	in := e.input("tg-renamed", connections.ModeWebhook)
	in.BotToken = keyring.Keep
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	if _, again, _ := e.svc.Hooked(t.Context(), c.PublicID); again != secret {
		t.Fatal("a rename changed the secret token")
	}
	// A new token sets the webhook again with a new secret token.
	in.BotToken = keyring.Replace(tgOtherToken)
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	if _, again, _ := e.svc.Hooked(t.Context(), c.PublicID); again == secret ||
		e.tg.Webhooks()[tgOtherToken].SecretToken != string(again) {
		t.Fatal("a new token kept the secret token")
	}
	// Leaving the webhook mode deletes the webhook and the secret token.
	in.UpdateMode = connections.ModeLongPolling
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.tg.Webhooks()[tgOtherToken]; ok {
		t.Fatal("the webhook is still set")
	}
	if _, _, err := e.svc.Hooked(t.Context(), c.PublicID); !errors.Is(err, telegram.ErrNoWebhook) {
		t.Fatalf("hooked in long polling = %v", err)
	}
	if e.store.rows[c.ID].row.TelegramWebhookSecretCiphertext != nil {
		t.Fatal("the secret token is still stored")
	}
	// A refused setWebhook saves nothing.
	revoked := []string{tgOtherToken}
	_ = e.tg.SetConfig(nil, nil, &revoked)
	version := e.store.rows[c.ID].row.Version
	in.UpdateMode = connections.ModeWebhook
	fe := fieldError(e.update(t, c.PublicID, in))
	if fe == nil || fe.Pointer != "/update_mode" || fe.Code != connections.CodeWebhookCallFailed ||
		!strings.Contains(fe.Detail, "setWebhook failed: Telegram answered 401: Unauthorized") ||
		e.store.rows[c.ID].row.Version != version || strings.Contains(fe.Detail, tgOtherToken) {
		t.Fatalf("refused setWebhook = %v", fe)
	}
	created := len(e.store.rows)
	if _, err := e.svc.Create(t.Context(), by, func() connections.Input {
		in := e.input("refused", connections.ModeWebhook)
		in.BotToken = keyring.Replace(tgOtherToken)
		return in
	}()); fieldError(err) == nil || len(e.store.rows) != created {
		t.Fatalf("a refused create = %v", err)
	}
	// A refused deleteWebhook saves nothing either.
	_ = e.tg.SetConfig(nil, nil, &[]string{})
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	_ = e.tg.SetConfig(nil, nil, &revoked)
	in.UpdateMode = connections.ModeLongPolling
	if fe := fieldError(e.update(t, c.PublicID, in)); fe == nil || !strings.Contains(fe.Detail, "deleteWebhook failed") ||
		e.store.rows[c.ID].row.TelegramUpdateMode.String != connections.ModeWebhook {
		t.Fatalf("refused deleteWebhook = %v", fe)
	}
	// Deleting the Connection wipes the secret token; the endpoint then refuses it.
	if err := e.svc.Delete(t.Context(), by, c.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Hooked(t.Context(), c.PublicID); !errors.Is(err, telegram.ErrNoWebhook) {
		t.Fatalf("hooked after delete = %v", err)
	}
}

func TestHooked(t *testing.T) {
	e := newTGEnv(t)
	mm := e.create(t, "mm")
	for _, id := range []string{"CN000000000000", "bad", mm.PublicID} {
		if _, _, err := e.svc.Hooked(t.Context(), id); !errors.Is(err, telegram.ErrNoWebhook) {
			t.Errorf("%s = %v", id, err)
		}
	}
	c, err := e.svc.Create(t.Context(), by, e.input("tg", connections.ModeWebhook))
	if err != nil {
		t.Fatal(err)
	}
	e.store.rows[c.ID].row.TelegramWebhookSecretCiphertext = []byte("garbage")
	if _, _, err := e.svc.Hooked(t.Context(), c.PublicID); err == nil || errors.Is(err, telegram.ErrNoWebhook) {
		t.Fatalf("an unreadable secret = %v", err)
	}
	e.store.fail["GetConnection"] = errors.New("down")
	if _, _, err := e.svc.Hooked(t.Context(), c.PublicID); err == nil || errors.Is(err, telegram.ErrNoWebhook) {
		t.Fatalf("a failed read = %v", err)
	}
}

// TestTelegramCheck covers C-14.FR-11, AC-5 and AC-6 through the Service: the three steps on the interactive path of the
// Connection, the bot recorded; with an unsaved base URL only the dry probe runs there.
func TestTelegramCheck(t *testing.T) {
	e := newTGEnv(t)
	c, err := e.svc.Create(t.Context(), by, e.input("tg", connections.ModeLongPolling))
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Check(t.Context(), c.PublicID, nil)
	if err != nil || !res.OK || len(res.Steps) != 3 || res.BotName != faketelegram.BotUsername ||
		res.WebhookSet == nil || *res.WebhookSet || *res.PendingUpdates != 0 || res.Steps[0].Via != telegram.ViaDirect ||
		res.Steps[0].Latency <= 0 {
		t.Fatalf("check = %+v, %v", res, err)
	}
	for _, s := range e.path.subjects {
		if *s.Connection != c.ID {
			t.Fatalf("subject %+v", s)
		}
	}
	got, _ := e.svc.Get(t.Context(), c.PublicID)
	if got.BotUsername == nil || *got.BotUsername != faketelegram.BotUsername ||
		e.store.rows[c.ID].row.BotUserID.String != "123456" {
		t.Fatalf("bot %+v", got)
	}
	e.tg.ResetRequests()
	other := e.tg.URL() + "/other/"
	res, err = e.svc.Check(t.Context(), c.PublicID, &other)
	if err != nil || res.OK || !res.Steps[1].Skipped || !res.Steps[2].Skipped ||
		res.Steps[0].Message != telegram.MessageWrongPrefix || res.WebhookSet != nil {
		t.Fatalf("unsaved = %+v, %v", res, err)
	}
	if reqs := e.tg.Requests(); len(reqs) != 1 || reqs[0].Path != "/other/bot0:x/getMe" {
		t.Fatalf("requests %+v", reqs)
	}
	bad := "ftp://x"
	if fe := fieldError(func() error { _, err := e.svc.Check(t.Context(), c.PublicID, &bad); return err }()); fe == nil ||
		fe.Pointer != "/base_url" {
		t.Fatalf("a bad base URL = %v", fe)
	}
	e.path.limited = true
	if _, err := e.svc.Check(t.Context(), c.PublicID, nil); !isLimited(err) {
		t.Fatalf("limited = %v", err)
	}
	e.path.limited = false
	e.store.fail["SetBotIdentity"] = errors.New("down")
	if _, err := e.svc.Check(t.Context(), c.PublicID, nil); err == nil {
		t.Fatal("a failed record of the bot")
	}
	delete(e.store.fail, "SetBotIdentity")
	e.store.rows[c.ID].row.ProxyPasswordCiphertext = []byte("garbage")
	e.store.rows[c.ID].row.ProxyPasswordKeyID = pgtype.Text{String: "k", Valid: true}
	if _, err := e.svc.Check(t.Context(), c.PublicID, &other); err == nil {
		t.Fatal("an unreadable proxy password")
	}
	e.store.rows[c.ID].row.BotTokenCiphertext = []byte("garbage")
	if _, err := e.svc.Check(t.Context(), c.PublicID, nil); err == nil || strings.Contains(err.Error(), tgToken) {
		t.Fatalf("an unreadable token = %v", err)
	}
}

// TestTelegramCheckThroughProxy: the steps travel through the Connection's proxy and say so.
func TestTelegramCheckThroughProxy(t *testing.T) {
	e := newTGEnv(t)
	in := e.input("tg", connections.ModeLongPolling)
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("socks5"), Address: new("127.0.0.1:1")}
	c, err := e.svc.Create(t.Context(), by, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Check(t.Context(), c.PublicID, nil)
	if err != nil || res.OK || res.Steps[0].Via != telegram.ViaProxy {
		t.Fatalf("check = %+v, %v", res, err)
	}
}

// TestPollingAndHandleOnce covers C-14.FR-1: the poller gets the long-polling Connections with their stored offsets and
// clients kept per version; HandleOnce runs an update once and raises the offset after it.
func TestPollingAndHandleOnce(t *testing.T) {
	e := newTGEnv(t)
	a, err := e.svc.Create(t.Context(), by, e.input("a", connections.ModeLongPolling))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(t.Context(), by, func() connections.Input {
		in := e.input("b", connections.ModeWebhook)
		in.BotToken = keyring.Replace(tgOtherToken)
		return in
	}()); err != nil {
		t.Fatal(err)
	}
	e.create(t, "mm")
	list, err := e.svc.Polling(t.Context())
	if err != nil || len(list) != 1 || list[0].ID != a.ID || list[0].Offset != nil || list[0].Version != a.Version {
		t.Fatalf("polling = %+v, %v", list, err)
	}
	ran, err := e.svc.HandleOnce(t.Context(), a.ID, 41, func(context.Context) error { return nil })
	if !ran || err != nil {
		t.Fatalf("first = %v %v", ran, err)
	}
	ran, err = e.svc.HandleOnce(t.Context(), a.ID, 40, func(context.Context) error {
		t.Fatal("an update before the offset ran")
		return nil
	})
	if ran || err != nil {
		t.Fatalf("an old update = %v %v", ran, err)
	}
	failed := errors.New("handler failed")
	if ran, err := e.svc.HandleOnce(t.Context(), a.ID, 42, func(context.Context) error { return failed }); ran ||
		!errors.Is(err, failed) {
		t.Fatalf("a failed handler = %v %v", ran, err)
	}
	again, _ := e.svc.Polling(t.Context())
	if *again[0].Offset != 42 || again[0].Client != list[0].Client {
		t.Fatalf("again = %+v", again)
	}
	if ran, err := e.svc.HandleOnce(t.Context(), 999, 1, func(context.Context) error { return nil }); ran ||
		err != nil {
		t.Fatalf("an unknown Connection = %v %v", ran, err)
	}
	for _, q := range []string{"LockUpdateOffset", "StoreUpdateOffset"} {
		e.store.fail[q] = errors.New("down")
		if _, err := e.svc.HandleOnce(t.Context(), a.ID, 50, func(context.Context) error { return nil }); err == nil {
			t.Errorf("%s failed = nil", q)
		}
		delete(e.store.fail, q)
	}
	e.store.rows[a.ID].row.BotTokenCiphertext = []byte("garbage")
	e.store.rows[a.ID].row.Version++
	if list, err := e.svc.Polling(t.Context()); err != nil || len(list) != 0 ||
		!strings.Contains(e.log.String(), `"event":"telegram_poll_failed","connection":"`+a.PublicID) {
		t.Fatalf("an unreadable token = %+v, %v", list, err)
	}
	e.store.fail["ListPollingConnections"] = errors.New("down")
	if _, err := e.svc.Polling(t.Context()); err == nil {
		t.Fatal("a failed list")
	}
}

// TestTelegramEnsureDemo: `muster dev` ensures "Dev Telegram" in the long-polling mode once.
func TestTelegramEnsureDemo(t *testing.T) {
	e := newTGEnv(t)
	d := connections.Demo{Type: connections.TypeTelegram, Name: "Dev Telegram", BaseURL: e.tg.URL(),
		BotToken: logging.Secret(tgToken)}
	for range 2 {
		if err := e.svc.EnsureDemo(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	page, _ := e.svc.List(t.Context(), connections.ListFilter{Limit: 10})
	if len(page.Connections) != 1 || page.Connections[0].UpdateMode != connections.ModeLongPolling ||
		page.Connections[0].Limiter.Limit != connections.TelegramDefaultLimit {
		t.Fatalf("demo %+v", page.Connections)
	}
}

// TestTelegramDoctor covers C-02.FR-14: muster doctor prints ok for a Telegram Connection whose steps pass, and the
// failing step with its message otherwise.
func TestTelegramDoctor(t *testing.T) {
	e := newTGEnv(t)
	if _, err := e.svc.Create(t.Context(), by, e.input("tg", connections.ModeLongPolling)); err != nil {
		t.Fatal(err)
	}
	in := e.input("revoked", connections.ModeLongPolling)
	in.BotToken = keyring.Replace(tgOtherToken)
	if _, err := e.svc.Create(t.Context(), by, in); err != nil {
		t.Fatal(err)
	}
	revoked := []string{tgOtherToken}
	_ = e.tg.SetConfig(nil, nil, &revoked)
	got := findings(t, e.svc, e.store)
	want := "connection tg: ok\nconnection revoked: get_me: " + telegram.MessageTokenInvalid
	if got != want {
		t.Fatalf("findings:\n%s", got)
	}
	noKeys := connections.New(connections.Config{OrgID: 1})
	found, err := noKeys.Doctor(t.Context(), e.store, 0)
	if err != nil || len(found) != 2 || !strings.Contains(found[0].Message, "master keys") {
		t.Fatalf("without keys = %+v, %v", found, err)
	}
}

// TestTelegramNewToken: a new bot token forgets the stored update offset, whose ids belong to the old bot; leaving the
// webhook mode with a new token deletes the webhook of the bot that set it.
func TestTelegramNewToken(t *testing.T) {
	e := newTGEnv(t)
	c, err := e.svc.Create(t.Context(), by, e.input("tg", connections.ModeWebhook))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.HandleOnce(t.Context(), c.ID, 500, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	in := e.input("tg", connections.ModeLongPolling)
	in.BotToken = keyring.Replace(tgOtherToken)
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	if len(e.tg.Webhooks()) != 0 {
		t.Fatalf("webhooks left %v", e.tg.Webhooks())
	}
	list, _ := e.svc.Polling(t.Context())
	if len(list) != 1 || list[0].Offset != nil {
		t.Fatalf("polling after a new token = %+v", list)
	}
	in.BotToken = keyring.Keep
	in.Name = "tg-2"
	if _, err := e.svc.HandleOnce(t.Context(), c.ID, 9, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.update(t, c.PublicID, in); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.svc.Polling(t.Context()); list[0].Offset == nil || *list[0].Offset != 10 {
		t.Fatalf("a rename forgot the offset: %+v", list)
	}
}
