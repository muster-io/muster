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
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

// tg drives one Muster replica and the fake Telegram server of a harness with an Admin's Personal access token.
type tg struct {
	t   *testing.T
	h   *Harness
	api patClient
	ftg string
}

func newTG(t *testing.T, h *Harness) *tg {
	t.Helper()
	admin := newAgent(t, h.Replicas[0].App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"telegram","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	return &tg{t: t, h: h, api: patClient{t: t, base: h.Replicas[0].App, token: token}, ftg: h.Fakes.Telegram + "/_fake"}
}

// connection creates a Telegram Connection in the long-polling mode and returns the answer.
func (e *tg) connection(name, token, base, proxy string) answer {
	e.t.Helper()
	return e.api.do(http.MethodPost, "/api/v1/connections", fmt.Sprintf(`{"type":"telegram","name":%q,"bot_token":%q,
		"bot_api_base_url":%q,"update_mode":"long_polling","proxy":%s,"limiter":{"limit":15,"per_seconds":1}}`,
		name, token, base, proxy))
}

type checkStep struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped"`
	Via     string `json:"via"`
	Message string `json:"message"`
}

type checkResult struct {
	OK             bool        `json:"ok"`
	Steps          []checkStep `json:"steps"`
	BotName        *string     `json:"bot_name"`
	WebhookSet     *bool       `json:"webhook_set"`
	PendingUpdates *int        `json:"pending_updates"`
}

func (e *tg) check(id, body string) (checkResult, answer) {
	e.t.Helper()
	a := e.api.do(http.MethodPost, "/api/v1/connections/"+id+"/checks", body)
	var r checkResult
	if a.status == http.StatusOK {
		decode(e.t, a, &r)
	}
	return r, a
}

func (e *tg) fake(method, path, body string) {
	e.t.Helper()
	if a := call(e.t, method, e.ftg+path, body); a.status/100 != 2 {
		e.t.Fatalf("%s %s = %d %s", method, path, a.status, a.body)
	}
}

func steps(r checkResult) string {
	var out []string
	for _, s := range r.Steps {
		state := "fail"
		switch {
		case s.Skipped:
			state = "skip"
		case s.OK:
			state = "ok"
		}
		out = append(out, s.Name+":"+state)
	}
	return strings.Join(out, " ")
}

// waitLog waits until the replica logged a line of event that matches re.
func waitLog(t *testing.T, r *Replica, what string, re *regexp.Regexp) {
	t.Helper()
	r.waitFor(t, what, func() bool { return re.MatchString(r.Output()) })
}

// TestTelegramConnection is S-041's end-to-end check against `muster dev` and the fake Telegram server: the base URL
// rules and warning (C-14.FR-10), the three check steps (C-14.FR-11, AC-5), an unsaved base URL (C-14.AC-6), a path
// prefix and a SOCKS5 proxy (C-14.AC-10), the 409 of a second poller (C-14.AC-3), the webhook mode and its secret token
// (C-14.AC-11), private messages reaching the router (C-14.FR-8), a network error that leaves the token nowhere
// (C-14.AC-7) and muster doctor (C-02.FR-14).
func TestTelegramConnection(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newTG(t, h)
	token := "777001:verify-token"

	// C-14.FR-10: base URL rules and the http warning.
	a := e.connection("bad", token, "https://user:pw@api.example.org/?x=1", `{"enabled":false}`)
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"pointer":"/bot_api_base_url"`) {
		t.Fatalf("bad base URL = %d %s", a.status, a.body)
	}
	a = e.connection("tg", token, h.Fakes.Telegram+"/", `{"enabled":false}`)
	var created struct {
		ID       string   `json:"id"`
		Base     string   `json:"bot_api_base_url"`
		Warnings []string `json:"warnings"`
	}
	decode(t, a, &created)
	if a.status != http.StatusCreated || created.Base != h.Fakes.Telegram ||
		!slices.Equal(created.Warnings, []string{"base_url_uses_http"}) || strings.Contains(string(a.body), token) {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	id := created.ID

	// C-14.FR-11, AC-5: the three steps, and a server that is not a Bot API.
	res, a := e.check(id, "{}")
	if !res.OK || steps(res) != "dry_probe:ok get_me:ok get_webhook_info:ok" || *res.BotName != "muster_dev_bot" ||
		*res.WebhookSet || *res.PendingUpdates != 0 || res.Steps[0].Via != "direct" {
		t.Fatalf("check = %s", a.body)
	}
	e.fake(http.MethodPut, "/config", `{"mode":"html"}`)
	res, a = e.check(id, "")
	if res.OK || res.Steps[0].Message != "This is not a Bot API." || steps(res) != "dry_probe:fail get_me:skip get_webhook_info:skip" {
		t.Fatalf("html = %s", a.body)
	}
	e.fake(http.MethodPut, "/config", `{"mode":"bot_api"}`)
	var getMes []string
	for _, p := range recordedPaths(t, h.Fakes.Telegram) {
		if strings.HasSuffix(p, "/getMe") && (strings.Contains(p, "bot0:x") || strings.Contains(p, "777001")) &&
			!slices.Contains(getMes, p) {
			getMes = append(getMes, p)
		}
	}
	slices.Sort(getMes)
	if !slices.Equal(getMes, []string{"/bot0:x/getMe", "/bot777001:verify-token/getMe"}) {
		t.Fatalf("getMe paths %v", getMes)
	}

	// C-14.AC-6: an unsaved base URL runs only the dry probe there.
	e.fake(http.MethodDelete, "/requests", "")
	res, a = e.check(id, `{"base_url":"`+h.Fakes.Telegram+`/other/"}`)
	if steps(res) != "dry_probe:fail get_me:skip get_webhook_info:skip" {
		t.Fatalf("unsaved = %s", a.body)
	}
	var other []string
	for _, p := range recordedPaths(t, h.Fakes.Telegram) {
		if !strings.HasPrefix(p, "/bot123456:") && !slices.Contains(other, p) {
			other = append(other, p)
		}
	}
	if !slices.Equal(other, []string{"/other/bot0:x/getMe"}) {
		t.Fatalf("paths of the unsaved check %v", other)
	}

	// C-14.AC-10: a path prefix, and a SOCKS5 proxy.
	e.fake(http.MethodPut, "/config", `{"path_prefix":"/k3x9"}`)
	e.fake(http.MethodDelete, "/requests", "")
	a = e.connection("pref", "777002:prefix-token", h.Fakes.Telegram+"/k3x9/",
		`{"enabled":true,"type":"socks5","address":"`+devmode.SOCKSProxyAddr+`"}`)
	decode(t, a, &created)
	res, a = e.check(created.ID, "")
	if !res.OK || res.Steps[1].Via != "proxy" {
		t.Fatalf("prefix check = %s", a.body)
	}
	r.waitFor(t, "the poll through the prefix", func() bool {
		return slices.Contains(recordedPaths(t, h.Fakes.Telegram), "/k3x9/bot777002:prefix-token/getUpdates")
	})
	for _, p := range recordedPaths(t, h.Fakes.Telegram) {
		if strings.Contains(p, "777002") && !strings.HasPrefix(p, "/k3x9/bot777002:prefix-token/") {
			t.Fatalf("a request outside the prefix %s", p)
		}
	}
	var conns []struct {
		Target string `json:"target"`
	}
	decode(t, call(t, http.MethodGet, "http://"+devmode.SOCKSProxyAddr+"/_fake/requests", ""), &conns)
	for _, c := range conns {
		if c.Target != devmode.TelegramAddr {
			t.Fatalf("proxy target %s", c.Target)
		}
	}
	if len(conns) == 0 {
		t.Fatal("nothing went through the proxy")
	}
	e.fake(http.MethodPut, "/config", `{"path_prefix":""}`)

	// C-14.AC-3: a second poller interrupts; Muster backs off and nothing is Broken.
	e.fake(http.MethodPost, "/conflict", `{"token":"`+token+`"}`)
	waitLog(t, r, "telegram_poll_conflict", regexp.MustCompile(`"event":"telegram_poll_conflict","connection":"`+id+
		`","backoff_ms":\d+`))
	if n := len(e.api.json(http.MethodGet, "/api/v1/destinations?health=broken", "", http.StatusOK)["items"].([]any)); n != 0 {
		t.Fatalf("%d broken destinations", n)
	}

	// C-14.FR-1, AC-11: the webhook mode and its secret token header.
	etag := e.api.json(http.MethodGet, "/api/v1/connections/"+id, "", http.StatusOK)["etag"].(string)
	m := e.api.json(http.MethodPut, "/api/v1/connections/"+id, `{"type":"telegram","name":"tg","bot_api_base_url":"`+
		h.Fakes.Telegram+`","update_mode":"webhook","proxy":{"enabled":false},"limiter":{"limit":15,"per_seconds":1}}`,
		http.StatusOK, "If-Match", etag)
	if m["update_mode"] != "webhook" {
		t.Fatalf("update %v", m)
	}
	var info struct {
		Result struct {
			URL string `json:"url"`
		} `json:"result"`
	}
	decode(t, call(t, http.MethodGet, h.Fakes.Telegram+"/bot"+token+"/getWebhookInfo", ""), &info)
	if info.Result.URL != r.Ingest+"/api/v1/callbacks/telegram/"+id &&
		info.Result.URL != devmode.IngestURL+"/api/v1/callbacks/telegram/"+id {
		t.Fatalf("webhook URL %q", info.Result.URL)
	}
	u := `{"update_id":9001,"message":{"message_id":1,"chat":{"id":42,"type":"private"},"text":"hello"}}`
	hook := r.Ingest + "/api/v1/callbacks/telegram/"
	for _, c := range []struct{ id, secret string }{{id, ""}, {id, "wrong"}, {"CN000000000000", "x"}} {
		var header []string
		if c.secret != "" {
			header = []string{"X-Telegram-Bot-Api-Secret-Token", c.secret}
		}
		if a := call(t, http.MethodPost, hook+c.id, u, header...); a.status != http.StatusUnauthorized {
			t.Fatalf("webhook %s with %q = %d", c.id, c.secret, a.status)
		}
	}
	if strings.Contains(r.Output(), `"event":"telegram_update_dropped","connection":"`+id) {
		t.Fatal("a refused webhook request reached the router")
	}
	e.fake(http.MethodPost, "/updates", `{"token":"`+token+`","update":`+u+`}`)
	dropped := regexp.MustCompile(`"event":"telegram_update_dropped","connection":"` + id + `","kind":"private_message"`)
	waitLog(t, r, "the private message to reach the router", dropped)
	time.Sleep(500 * time.Millisecond)
	if n := len(dropped.FindAllString(r.Output(), -1)); n != 1 {
		t.Fatalf("the private message was dropped %d times", n)
	}

	// C-14.AC-7: a network error leaves the token nowhere.
	a = e.connection("down", "777003:down-token", "http://127.0.0.1:1/", `{"enabled":false}`)
	decode(t, a, &created)
	res, a = e.check(created.ID, "")
	if res.OK || !strings.Contains(res.Steps[0].Message, "connection refused") || strings.Contains(string(a.body),
		"down-token") {
		t.Fatalf("down = %s", a.body)
	}
	if strings.Contains(r.Output(), "down-token") || strings.Contains(r.Output(), "verify-token") ||
		strings.Contains(r.Output(), "prefix-token") {
		t.Fatal("a bot token reached the log")
	}

	// C-02.FR-14: one line per Telegram Connection.
	code, out := h.runCLIStdout(t, "doctor")
	for _, want := range []string{"OK   connection tg: ok", "OK   connection " + devmode.TelegramConnectionName + ": ok",
		"FAIL connection down: dry_probe: "} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor (exit %d) has no line %q:\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "down-token") {
		t.Errorf("doctor printed a token:\n%s", out)
	}
}
