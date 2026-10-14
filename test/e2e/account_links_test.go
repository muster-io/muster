// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

const (
	textLinkInvalid  = "This link is invalid or expired. Start again from your Muster profile."
	textLinkConflict = "This account is linked to another user. Its owner or an admin can unlink it."
	textCodeRequest  = "You requested a link to Muster. Do not share this code."
	textForbidden    = "You are not permitted to do this"
	textDisabled     = "Your Muster account is disabled"
)

var (
	deepLink = regexp.MustCompile(`^https://t\.me/muster_dev_bot\?start=([A-Za-z0-9_-]{43})$`)
	linkCode = regexp.MustCompile(`^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{8}$`)
)

// linksEnv is the reminderEnv of S-068 without Account links inserted by hand: every link is made the way people make
// them, from the web session and the messengers. It keeps every link token and code it saw, which the log must not
// carry.
type linksEnv struct {
	*timersEnv
	secrets []string
}

// linker is a signed-in person: their id, their web session with its CSRF token and a Personal access token.
type linker struct {
	id      string
	session *agent
	pat     patClient
}

// person creates the user login with the display name and role, sets a password through the setup link and signs
// them in.
func (e *linksEnv) person(name, login, role string) linker {
	e.t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "login": login, "role": role})
	created := e.api.json(http.MethodPost, "/api/v1/users", string(body), http.StatusCreated)
	link, err := url.Parse(created["password_setup_link"].(map[string]any)["url"].(string))
	if err != nil {
		e.t.Fatal(err)
	}
	setup, _ := strings.CutPrefix(link.Fragment, "token=")
	if a := call(e.t, http.MethodPost, e.r.App+"/api/v1/password-setups", `{"token":"`+setup+`","password":"`+login+
		`-password-1"}`, "Content-Type", "application/json"); a.status != http.StatusNoContent {
		e.t.Fatalf("password setup of %s = %d %s", login, a.status, a.body)
	}
	s := newAgent(e.t, e.r.App)
	if a := s.signIn(login, login+"-password-1"); a.status != http.StatusCreated {
		e.t.Fatalf("sign in %s = %d %s", login, a.status, a.body)
	}
	perms, _ := json.Marshal(s.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := s.json(http.MethodPost, "/api/v1/me/personal-access-tokens", `{"name":"cli","permissions":`+string(perms)+
		`}`, http.StatusCreated)["value"].(string)
	return linker{id: created["user"].(map[string]any)["id"].(string), session: s,
		pat: patClient{t: e.t, base: e.r.App, token: token}}
}

// startTelegram starts a Telegram link of p through the Connection conn and returns the token of its deep link.
func (e *linksEnv) startTelegram(p linker, conn string) string {
	e.t.Helper()
	m := p.session.json(http.MethodPost, "/api/v1/me/account-links/telegram", `{"connection_id":"`+conn+`"}`,
		http.StatusCreated)
	got := deepLink.FindStringSubmatch(fmt.Sprint(m["deep_link"]))
	if got == nil {
		e.t.Fatalf("deep link %v", m["deep_link"])
	}
	e.secrets = append(e.secrets, got[1])
	return got[1]
}

// chat are the messages of a private chat with the bot, none before the person wrote.
func (e *linksEnv) chat(id int64) []faketelegram.Message {
	e.t.Helper()
	a := call(e.t, http.MethodGet, fmt.Sprintf("%s/messages?chat=%d", e.ftg, id), "")
	if a.status == http.StatusNotFound {
		return nil
	}
	var out []faketelegram.Message
	decode(e.t, a, &out)
	return out
}

// botAnswers are the bot's messages in the private chat of id.
func (e *linksEnv) botAnswers(id int64) []string {
	e.t.Helper()
	var out []string
	for _, m := range e.chat(id) {
		if m.From != nil && m.From.ID == faketelegram.BotID {
			out = append(out, m.Text)
		}
	}
	return out
}

// private sends text from the Telegram account id to the bot of token in their private chat and returns the bot's
// answer once it is there.
func (e *linksEnv) private(token string, id int64, username, text string) string {
	e.t.Helper()
	before := len(e.botAnswers(id))
	body, _ := json.Marshal(map[string]any{"token": token, "from": map[string]any{"id": id, "username": username},
		"text": text})
	e.fake(http.MethodPost, e.ftg+"/private", string(body))
	eventually(e.t, fmt.Sprintf("the bot's answer to %d", id), func() bool { return len(e.botAnswers(id)) > before })
	return e.botAnswers(id)[before]
}

// audits are the Audit log entries of action, newest first.
func (e *linksEnv) audits(action string) []map[string]any {
	e.t.Helper()
	var out []map[string]any
	for _, x := range e.api.json(http.MethodGet, "/api/v1/audit-log?limit=100&action="+action, "",
		http.StatusOK)["items"].([]any) {
		out = append(out, x.(map[string]any))
	}
	return out
}

// lastAudit is the details of the newest entry of action, with its actor name and Transport.
func (e *linksEnv) lastAudit(action string) (map[string]any, string, string) {
	e.t.Helper()
	items := e.audits(action)
	if len(items) == 0 {
		e.t.Fatalf("no %s entry", action)
	}
	d, _ := items[0]["details"].(map[string]any)
	return d, items[0]["actor"].(map[string]any)["name"].(string), items[0]["transport"].(string)
}

// wantRejection checks the newest account_link.link_rejected entry.
func (e *linksEnv) wantRejection(reason, messenger, externalID string) {
	e.t.Helper()
	d, actor, transport := e.lastAudit("account_link.link_rejected")
	if d["reason"] != reason || d["messenger"] != messenger || d["external_id"] != externalID {
		e.t.Errorf("rejection %v by %s through %s, want %s %s %s", d, actor, transport, reason, messenger, externalID)
	}
}

func (e *linksEnv) startMattermost(p linker, username string) answer {
	e.t.Helper()
	return p.session.do(http.MethodPost, "/api/v1/me/account-links/mattermost", `{"connection_id":"`+e.mmConn+
		`","username":"`+username+`"}`)
}

func (e *linksEnv) confirm(p linker, request, code string) answer {
	e.t.Helper()
	return p.session.do(http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation", `{"request_id":"`+
		request+`","code":"`+code+`"}`)
}

func (e *linksEnv) dms() []fakemattermost.DM {
	e.t.Helper()
	var out []fakemattermost.DM
	decode(e.t, call(e.t, http.MethodGet, e.fmm+"/dms", ""), &out)
	return out
}

// codeFor starts a Mattermost link of p to username and returns the request id and the code from the bot's direct
// message to userID.
func (e *linksEnv) codeFor(p linker, username, userID string) (string, string) {
	e.t.Helper()
	before := len(e.dms())
	a := e.startMattermost(p, username)
	var s struct {
		RequestID string `json:"request_id"`
		Attempts  int    `json:"attempts_remaining"`
	}
	if a.status != http.StatusAccepted {
		e.t.Fatalf("start a mattermost link to %s = %d %s", username, a.status, a.body)
	}
	decode(e.t, a, &s)
	if s.Attempts != 5 {
		e.t.Errorf("attempts_remaining %d", s.Attempts)
	}
	dms := e.dms()
	if len(dms) != before+1 {
		e.t.Fatalf("%d direct messages after %d", len(dms), before)
	}
	dm := dms[len(dms)-1]
	text, code, _ := strings.Cut(dm.Message, "\n\n")
	if dm.UserID != userID || text != textCodeRequest || !linkCode.MatchString(code) {
		e.t.Fatalf("the code message to %s: %s", dm.UserID, dm.Message)
	}
	e.secrets = append(e.secrets, code)
	return s.RequestID, code
}

// fieldError is the code and pointer of the first error of a 422.
func fieldError(t *testing.T, a answer) string {
	t.Helper()
	var p struct {
		Errors []struct {
			Code    string `json:"code"`
			Pointer string `json:"pointer"`
		} `json:"errors"`
	}
	decode(t, a, &p)
	if a.status != http.StatusUnprocessableEntity || len(p.Errors) == 0 {
		return fmt.Sprintf("%d %s", a.status, a.body)
	}
	return p.Errors[0].Code + " " + p.Errors[0].Pointer
}

// links are the Account links of a list: messenger, Connection name, external id and username of each.
func links(t *testing.T, m map[string]any) []string {
	t.Helper()
	out := []string{}
	for _, x := range m["items"].([]any) {
		l := x.(map[string]any)
		out = append(out, fmt.Sprintf("%v %v %v %v", l["messenger"], l["connection"].(map[string]any)["name"],
			l["external_id"], l["username"]))
	}
	return out
}

// linkOf is the Account link of the messenger in a list.
func linkOf(t *testing.T, m map[string]any, messenger string) map[string]any {
	t.Helper()
	for _, x := range m["items"].([]any) {
		if l := x.(map[string]any); l["messenger"] == messenger {
			return l
		}
	}
	t.Fatalf("no %s link in %v", messenger, m)
	return nil
}

// tgPost is the message id of the channel post of the Alert Group in the fake Telegram server.
func (e *linksEnv) tgPost(id string) int64 {
	e.t.Helper()
	return e.h.count(e.t, `SELECT d.message_id::bigint FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id
		JOIN alert_groups g ON g.id = d.alert_group_id WHERE ds.type = 'telegram' AND d.message_id IS NOT NULL
		AND g.public_id = '`+id+`'`)
}

func (e *linksEnv) countHints(s *stream) int {
	n := 0
	for _, d := range s.hints() {
		if strings.Contains(d, `"type":"account-links"`) {
			n++
		}
	}
	return n
}

// retryAfter is the Retry-After of a 429.
func retryAfter(t *testing.T, a answer) int {
	t.Helper()
	if a.status != http.StatusTooManyRequests {
		t.Fatalf("answer %d %s, want 429", a.status, a.body)
	}
	n, err := strconv.Atoi(a.header.Get("Retry-After"))
	if err != nil {
		t.Fatalf("Retry-After %q", a.header.Get("Retry-After"))
	}
	return n
}

// TestAccountLinks is the Verification of S-051 against `muster dev` and the fake Telegram and Mattermost servers,
// with no link row inserted by hand: the Telegram deep link, `/start` and the `account-links` hint of that User only
// (C-18.FR-1, FR-2, FR-7, C-14.FR-8); a used, unknown, expired and another Connection's token refused with one answer
// (C-18.AC-1); the Mattermost code and its five attempts (C-18.FR-3, AC-2, FR-11); the limits of code requests per
// target account and per User; a new link replacing the User's earlier one and an account linked to another User
// refused in both messengers (C-18.FR-4, FR-5); the Connections to link through (C-18.FR-10); tokens refused where only
// the web session may act, Admins listing and removing links, and 405 for a method a path does not have (C-18.FR-6,
// AC-4, C-03.AC-14); presses run as the linked User in both messengers with the last use set (C-18.FR-8, AC-3,
// C-10.FR-11); the Reminder buttons (C-18.AC-5, C-17.FR-10); a Viewer's and a disabled User's presses in both
// messengers (C-18.AC-6); Alice named alice_t in Telegram and Alice Smith in Mattermost (C-18.AC-7, C-12.FR-12);
// unlinking and the removal on user deletion with the Audit log (C-18.FR-9, C-03.FR-13, FR-14); and no token or code
// in the log.
func TestAccountLinks(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := &linksEnv{timersEnv: &timersEnv{tgd: newTGD(t, h, r)}}
	alice := e.person("Alice Smith", "alice", "responder")
	bob := e.person("bob", "bob", "responder")
	dana := e.person("dana", "dana", "viewer")
	e.mmConn = e.connection(50)
	e.mmDest = e.destination("alerts", e.mmConn, fakemattermost.ChannelAlerts, nil, 50)
	e.member(faketelegram.GroupID, faketelegram.Member{Status: faketelegram.StatusAdministrator, CanPostMessages: true})
	a := e.create("tg-alerts", "@muster_alerts", 60)
	if a.status != http.StatusCreated {
		t.Fatalf("telegram destination = %d %s", a.status, a.body)
	}
	var tgDest struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	decode(t, a, &tgDest)
	e.tgDest = tgDest.Destination.ID
	e.route("t", "t", []string{e.mmDest, e.tgDest}, nil)
	admin := e.api.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["user"].(map[string]any)["name"].(string)
	interactive := e.clientRequests("interactive", "ok")
	const botToken = devmode.TelegramConnectionBotToken

	// C-18.FR-1, FR-2, FR-7, C-14.FR-8: the deep link from the web session, /start, the reply and the hint to Alice's
	// streams only.
	aliceHints, bobHints := openStream(t, alice.session), openStream(t, bob.session)
	tok := e.startTelegram(alice, e.conn)
	if got := e.private(botToken, 7001, "alice_t", "/start "+tok); got != "Linked to Muster user Alice Smith (alice)" {
		t.Errorf("the reply to /start: %q", got)
	}
	eventually(t, "the account-links hint", func() bool { return e.countHints(aliceHints) == 1 })
	if n := e.countHints(bobHints); n != 0 {
		t.Errorf("bob's stream got %d account-links hints", n)
	}
	if got := links(t, alice.session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK)); !slices.Equal(
		got, []string{"telegram " + devmode.TelegramConnectionName + " 7001 alice_t"}) {
		t.Errorf("alice's links %v", got)
	}
	if d, _, transport := e.lastAudit("account_link.link"); d["messenger"] != "telegram" || d["external_id"] != "7001" ||
		d["username"] != "alice_t" || d["connection"] != e.conn || transport != "telegram" {
		t.Errorf("the link entry %v through %s", d, transport)
	}

	// C-18.AC-1: the same token again, and an unknown one, link nothing and get the same answer.
	if got := e.private(botToken, 7002, "mallory", "/start "+tok); got != textLinkInvalid {
		t.Errorf("a used token: %q", got)
	}
	e.wantRejection("token_used", "telegram", "7002")
	if _, actor, transport := e.lastAudit("account_link.link_rejected"); transport != "telegram" {
		t.Errorf("the rejection by %s through %s", actor, transport)
	}
	unknown := make([]byte, 32)
	_, _ = rand.Read(unknown)
	if got := e.private(botToken, 7002, "mallory", "/start "+base64.RawURLEncoding.EncodeToString(unknown)); got !=
		textLinkInvalid {
		t.Errorf("an unknown token: %q", got)
	}
	e.wantRejection("token_unknown", "telegram", "7002")
	if got := fieldError(t, alice.session.do(http.MethodPost, "/api/v1/me/account-links/telegram",
		`{"connection_id":"`+e.mmConn+`"}`)); got != "unknown_id /connection_id" {
		t.Errorf("a Mattermost Connection for Telegram: %s", got)
	}

	// C-18.FR-3, AC-2: the code in a direct message; five wrong codes void the request.
	rid, code := e.codeFor(bob, "@bob", fakemattermost.BobUserID)
	if got := fieldError(t, e.startMattermost(bob, "nobody")); got != "messenger_user_not_found /username" {
		t.Errorf("an unknown username: %s", got)
	}
	wrong := "22222222"
	if code == wrong {
		wrong = "33333333"
	}
	for i := range 5 {
		if got := fieldError(t, e.confirm(bob, rid, wrong)); got != "link_code_invalid /code" {
			t.Errorf("wrong code %d: %s", i+1, got)
		}
	}
	if got := fieldError(t, e.confirm(bob, rid, code)); got != "link_code_invalid /code" {
		t.Errorf("the right code after five wrong ones: %s", got)
	}
	e.wantRejection("wrong_code", "mattermost", fakemattermost.BobUserID)
	wrongCodes := 0
	for _, x := range e.audits("account_link.link_rejected") {
		if x["details"].(map[string]any)["reason"] == "wrong_code" {
			wrongCodes++
		}
	}
	if wrongCodes < 5 {
		t.Errorf("%d wrong_code entries", wrongCodes)
	}
	if n := e.h.count(t, `SELECT count(*) FROM account_links WHERE messenger = 'mattermost'`); n != 0 {
		t.Errorf("%d mattermost links after the void request", n)
	}
	rid, code = e.codeFor(bob, "bob", fakemattermost.BobUserID)
	l := bob.session.json(http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation", `{"request_id":"`+rid+
		`","code":"`+strings.ToLower(code)+`"}`, http.StatusCreated)
	if l["messenger"] != "mattermost" || l["external_id"] != fakemattermost.BobUserID || l["username"] != "bob" {
		t.Errorf("the mattermost link %v", l)
	}
	if dms := e.dms(); dms[len(dms)-1].Message != "Linked to Muster user bob" || dms[len(dms)-1].UserID !=
		fakemattermost.BobUserID {
		t.Errorf("the confirmation %+v", dms[len(dms)-1])
	}
	if got := fieldError(t, e.confirm(bob, rid, code)); got != "link_code_invalid /code" {
		t.Errorf("a consumed request: %s", got)
	}
	e.wantRejection("token_used", "mattermost", fakemattermost.BobUserID)
	if d := e.clientRequests("interactive", "ok") - interactive; d < 7 {
		t.Errorf("%v interactive calls", d)
	}

	// C-18.FR-5: an account linked to another User; C-18.FR-3: the limits per target account and per User.
	if a := e.startMattermost(alice, "bob"); a.status != http.StatusConflict || codeOf(t, a) != "account_linked_elsewhere" {
		t.Errorf("bob's account for alice = %d %s", a.status, a.body)
	}
	e.wantRejection("conflict", "mattermost", fakemattermost.BobUserID)
	for i := range 3 {
		if a := e.startMattermost(dana, "dana"); a.status != http.StatusAccepted {
			t.Fatalf("dana's request %d = %d %s", i+1, a.status, a.body)
		}
	}
	for _, dm := range e.dms() {
		if dm.UserID == fakemattermost.DanaUserID {
			_, c, _ := strings.Cut(dm.Message, "\n\n")
			e.secrets = append(e.secrets, c)
		}
	}
	a = e.startMattermost(dana, "dana")
	if n := retryAfter(t, a); n < 3590 || n > 3600 {
		t.Errorf("the fourth request for dana's account: Retry-After %d, %s", n, a.body)
	}
	if !strings.Contains(string(a.body), "/problems/rate-limited") {
		t.Errorf("the problem of the limit %s", a.body)
	}
	for range 3 {
		if a := e.startMattermost(bob, "alice"); a.status != http.StatusAccepted {
			t.Fatalf("bob's request for alice = %d %s", a.status, a.body)
		}
	}
	for _, dm := range e.dms() {
		if dm.UserID == fakemattermost.AliceUserID {
			_, c, _ := strings.Cut(dm.Message, "\n\n")
			e.secrets = append(e.secrets, c)
		}
	}
	dmsBefore := len(e.dms())
	if n := retryAfter(t, e.startMattermost(bob, "nobody")); n < 1 || n > 3600 {
		t.Errorf("bob's sixth request: Retry-After %d", n)
	}
	if len(e.dms()) != dmsBefore {
		t.Error("a refused request sent a code")
	}

	// C-18.FR-10: the Connections to link through, with only id, name and messenger, for a Viewer; not for a Service
	// account.
	conns := dana.session.json(http.MethodGet, "/api/v1/me/account-links/connections", "", http.StatusOK)["items"].([]any)
	var names []string
	for _, x := range conns {
		c := x.(map[string]any)
		keys := slices.Sorted(func(yield func(string) bool) {
			for k := range c {
				if !yield(k) {
					return
				}
			}
		})
		if !slices.Equal(keys, []string{"id", "messenger", "name"}) {
			t.Errorf("connection keys %v", keys)
		}
		names = append(names, fmt.Sprint(c["messenger"], " ", c["name"]))
	}
	if !slices.Contains(names, "mattermost mm") || !slices.Contains(names, "telegram "+devmode.TelegramConnectionName) ||
		!slices.IsSorted(names) {
		t.Errorf("linkable connections %v", names)
	}
	sa := e.api.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"pager","role":"viewer"}`,
		http.StatusCreated)["id"].(string)
	adminSession := newAgent(t, r.App)
	if a := adminSession.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	sat := patClient{t: t, base: r.App, token: adminSession.json(http.MethodPost, "/api/v1/service-accounts/"+sa+"/tokens",
		`{"name":"pager"}`, http.StatusCreated)["value"].(string)}
	for _, path := range []string{"/api/v1/me/account-links", "/api/v1/me/account-links/connections"} {
		if a := sat.do(http.MethodGet, path, ""); a.status != http.StatusForbidden ||
			codeOf(t, a) != "service_account_not_allowed" {
			t.Errorf("a service account on %s = %d %s", path, a.status, a.body)
		}
	}

	// C-18.AC-4, C-03.AC-14: tokens, an Admin's included, cannot start, confirm or remove one's own link, also not
	// through the Admin's operation on a User's links; Admins list a User's links and never create one.
	aliceLink := linkOf(t, alice.pat.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK),
		"telegram")["id"].(string)
	adminID := e.api.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["user"].(map[string]any)["id"].(string)
	for _, c := range []struct {
		who          patClient
		method, path string
		body         string
	}{
		{e.api, http.MethodPost, "/api/v1/me/account-links/telegram", `{"connection_id":"` + e.conn + `"}`},
		{alice.pat, http.MethodPost, "/api/v1/me/account-links/telegram", `{"connection_id":"` + e.conn + `"}`},
		{alice.pat, http.MethodPost, "/api/v1/me/account-links/mattermost", `{"connection_id":"` + e.mmConn +
			`","username":"alice"}`},
		{alice.pat, http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation", `{"request_id":"` + rid +
			`","code":"22222222"}`},
		{alice.pat, http.MethodDelete, "/api/v1/me/account-links/" + aliceLink, ""},
		{e.api, http.MethodDelete, "/api/v1/users/" + adminID + "/account-links/" + aliceLink, ""},
	} {
		if a := c.who.do(c.method, c.path, c.body); a.status != http.StatusForbidden || codeOf(t, a) != "session_required" {
			t.Errorf("%s %s with a token = %d %s", c.method, c.path, a.status, a.body)
		}
	}
	if got := links(t, e.api.json(http.MethodGet, "/api/v1/users/"+bob.id+"/account-links", "", http.StatusOK)); !slices.Equal(got,
		[]string{"mattermost mm u-bob bob"}) {
		t.Errorf("bob's links for the Admin %v", got)
	}
	a = e.api.do(http.MethodPost, "/api/v1/users/"+bob.id+"/account-links", `{}`)
	if a.status != http.StatusMethodNotAllowed || a.header.Get("Allow") != "GET" ||
		!strings.Contains(string(a.body), `"type":"https://muster-io.github.io/muster/problems/method-not-allowed"`) {
		t.Errorf("POST /users/{id}/account-links = %d %v %s", a.status, a.header.Get("Allow"), a.body)
	}

	// C-18.AC-1: a token sent to another Telegram Connection's bot; C-18.FR-4: a new link replaces the earlier one;
	// C-18.FR-5: an account linked to another User, from the bot.
	const otherBot = "777101:second-bot-token"
	tg2 := e.api.json(http.MethodPost, "/api/v1/connections", `{"type":"telegram","name":"tg2","bot_token":"`+otherBot+
		`","bot_api_base_url":"`+h.Fakes.Telegram+`","update_mode":"long_polling","proxy":{"enabled":false},
		"limiter":{"limit":15,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	r.waitFor(t, "the poll of the second bot", func() bool {
		return slices.Contains(recordedPaths(t, h.Fakes.Telegram), "/bot"+otherBot+"/getUpdates")
	})
	tok = e.startTelegram(bob, e.conn)
	if got := e.private(otherBot, 5001, "bob_t", "/start "+tok); got != textLinkInvalid {
		t.Errorf("a token at another bot: %q", got)
	}
	e.wantRejection("wrong_connection", "telegram", "5001")
	if got := e.private(botToken, 5001, "bob_t", "/start "+tok); got != "Linked to Muster user bob (bob)" {
		t.Errorf("the same token at its bot: %q", got)
	}
	tok = e.startTelegram(bob, e.conn)
	if got := e.private(botToken, 5002, "bob_t2", "/start "+tok); got != "Linked to Muster user bob (bob)" {
		t.Errorf("a second Telegram account of bob: %q", got)
	}
	if d, _, _ := e.lastAudit("account_link.unlink"); d["reason"] != "replaced" || d["external_id"] != "5001" {
		t.Errorf("the replaced link %v", d)
	}
	if d, _, _ := e.lastAudit("account_link.link"); d["external_id"] != "5002" || d["replaced"] == nil {
		t.Errorf("the replacing link %v", d)
	}
	if got := links(t, bob.session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK)); !slices.Equal(
		got, []string{"mattermost mm u-bob bob", "telegram " + devmode.TelegramConnectionName + " 5002 bob_t2"}) {
		t.Errorf("bob's links %v", got)
	}
	if got := e.private(botToken, 5002, "bob_t2", "/start "+e.startTelegram(alice, e.conn)); got != textLinkConflict {
		t.Errorf("bob's account for alice: %q", got)
	}
	e.wantRejection("conflict", "telegram", "5002")

	// C-18.AC-1: an expired token, with the development clock.
	tok = e.startTelegram(alice, tg2)
	e.adv(601)
	if got := e.private(otherBot, 7003, "carol", "/start "+tok); got != textLinkInvalid {
		t.Errorf("an expired token: %q", got)
	}
	e.wantRejection("token_expired", "telegram", "7003")

	// C-18.FR-8, AC-3, C-10.FR-11: presses run as the linked User and set the last use, in both messengers.
	g := e.fire("l1", "LinkTest", "t", true)
	root := e.root(e.number(g))
	if res := e.mm.press(root.ID, "ack", fakemattermost.BobUserID); string(res.Answer) != "{}" {
		t.Errorf("bob's ack in mattermost %s", res.Answer)
	}
	eventually(t, "bob's acknowledgement", func() bool { s, _ := e.ownerOf(g); return s == "acknowledged" })
	if x := e.timeline(g, "")[0]; x.Event != "acknowledged" || x.Actor.Transport != "mattermost" || x.Actor.Name != "bob" {
		t.Errorf("timeline %+v", x)
	}
	if mml := linkOf(t, bob.session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK),
		"mattermost"); mml["last_used_at"] == nil {
		t.Errorf("no last use of %v", mml)
	}
	g3 := e.fire("l3", "LinkTgTest", "t", true)
	post3 := e.tgPost(g3)
	if got := e.press(post3, 5002, "Ack", ""); got.Text != "Done: Acknowledge" {
		t.Errorf("bob's ack in telegram %+v", got)
	}
	eventually(t, "bob's acknowledgement through telegram", func() bool {
		x := e.timeline(g3, "")[0]
		return x.Event == "acknowledged" && x.Actor.Transport == "telegram" && x.Actor.Name == "bob"
	})

	// C-18.AC-5, C-17.FR-10: the Reminder buttons for the linked User.
	e.adv(14400)
	rp, _ := e.reminderPost(g)
	if res := e.mm.press(rp.ID, "stillonit", fakemattermost.BobUserID); string(res.Answer) !=
		pressAnswer("Done: Still on it") {
		t.Errorf("still on it %s", res.Answer)
	}
	eventually(t, "the answer of the reminder", func() bool { return e.lastTimer(g).Event == "reminder_answered" })
	if x := e.lastTimer(g); x.Actor.Transport != "mattermost" || x.Actor.Name != "bob" {
		t.Errorf("answered by %+v", x.Actor)
	}
	e.mm.press(rp.ID, "unack", fakemattermost.BobUserID)
	eventually(t, "the unacknowledgement", func() bool { s, _ := e.ownerOf(g); return s == "firing" })
	// The Root messages offer Ack again once delivery has edited them, after the Command: the presses below need it.
	e.settle()

	// C-18.AC-6: a Viewer's press and a disabled User's press change nothing, in both messengers.
	rid, code = e.codeFor(dana, "dana", fakemattermost.DanaUserID)
	dana.session.json(http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation", `{"request_id":"`+rid+
		`","code":"`+code+`"}`, http.StatusCreated)
	if got := e.private(botToken, 8001, "dana_t", "/start "+e.startTelegram(dana, e.conn)); got !=
		"Linked to Muster user dana (dana)" {
		t.Errorf("dana's telegram link: %q", got)
	}
	e.pollAfterClockMove()
	post := e.tgPost(g)
	if res := e.mm.press(root.ID, "ack", fakemattermost.DanaUserID); string(res.Answer) != pressAnswer(textForbidden) {
		t.Errorf("a viewer's press in mattermost %s", res.Answer)
	}
	if got := e.press(post, 8001, "Ack", ""); got.Text != textForbidden {
		t.Errorf("a viewer's press in telegram %+v", got)
	}
	e.api.json(http.MethodPost, "/api/v1/users/"+bob.id+"/disable", "", http.StatusOK)
	if res := e.mm.press(root.ID, "ack", fakemattermost.BobUserID); string(res.Answer) != pressAnswer(textDisabled) {
		t.Errorf("a disabled user's press in mattermost %s", res.Answer)
	}
	if got := e.press(post, 5002, "Ack", ""); got.Text != textDisabled {
		t.Errorf("a disabled user's press in telegram %+v", got)
	}
	if s, _ := e.ownerOf(g); s != "firing" {
		t.Errorf("after the refused presses %s is %s", g, s)
	}
	e.api.json(http.MethodPost, "/api/v1/users/"+bob.id+"/enable", "", http.StatusOK)

	// C-18.AC-7, C-12.FR-12: Alice is mentioned by a tg://user link with her display name and named alice_t in the
	// Telegram footer, and Alice Smith in Mattermost, where she has no link.
	d := e.api.do(http.MethodGet, "/api/v1/destinations/"+e.tgDest, "")
	var dest map[string]any
	decode(t, d, &dest)
	mentions := dest["mentions"].(map[string]any)
	mentions["new_alert_group"] = map[string]any{"everyone": "none", "user_ids": []string{alice.id}, "groups": []string{}}
	body, _ := json.Marshal(map[string]any{"type": dest["type"], "name": dest["name"], "connection_id": dest["connection_id"],
		"channel_id": dest["channel_id"], "limiter": dest["limiter"], "mentions": mentions})
	e.api.json(http.MethodPut, "/api/v1/destinations/"+e.tgDest, string(body), http.StatusOK, "If-Match",
		d.header.Get("ETag"))
	g2 := e.fire("l2", "AliceTest", "t", true)
	post2 := e.tgPost(g2)
	mention := false
	for _, chat := range []int64{faketelegram.ChannelID, faketelegram.GroupID} {
		for _, m := range e.messages(chat) {
			mention = mention || strings.Contains(m.Text, `tg://user?id=7001">Alice Smith</a>`)
		}
	}
	if !mention {
		t.Error("no mention of Alice Smith by tg://user?id=7001")
	}
	alice.pat.json(http.MethodPost, "/api/v1/alert-groups/"+g2+"/acknowledge", "", http.StatusOK)
	e.settle()
	footer := regexp.MustCompile(`Acknowledged by alice_t($|[^A-Za-z0-9_])`)
	eventually(t, "the telegram footer", func() bool {
		for _, m := range e.messages(faketelegram.ChannelID) {
			if m.ID == post2 {
				return footer.MatchString(m.Text)
			}
		}
		return false
	})
	eventually(t, "the mattermost footer", func() bool {
		return strings.Contains(e.root(e.number(g2)).attachment(t).Text, "Acknowledged by Alice Smith")
	})

	// C-18.FR-6, FR-9: unlinking by the User from the web session, and by an Admin with a token.
	if a := alice.session.do(http.MethodDelete, "/api/v1/me/account-links/"+aliceLink, ""); a.status != http.StatusNoContent {
		t.Errorf("alice unlinks = %d %s", a.status, a.body)
	}
	if d, actor, _ := e.lastAudit("account_link.unlink"); d["external_id"] != "7001" || actor != "Alice Smith" {
		t.Errorf("alice's unlink %v by %s", d, actor)
	}
	eventually(t, "the hint of the unlink", func() bool { return e.countHints(aliceHints) == 2 })
	bobTG := linkOf(t, e.api.json(http.MethodGet, "/api/v1/users/"+bob.id+"/account-links", "", http.StatusOK),
		"telegram")["id"].(string)
	if a := e.api.do(http.MethodDelete, "/api/v1/users/"+bob.id+"/account-links/"+bobTG, ""); a.status !=
		http.StatusNoContent {
		t.Errorf("the Admin unlinks bob = %d %s", a.status, a.body)
	}
	if d, actor, transport := e.lastAudit("account_link.unlink"); d["external_id"] != "5002" || actor != admin ||
		transport != "api" || d["user"] != bob.id {
		t.Errorf("the Admin's unlink %v by %s through %s", d, actor, transport)
	}

	// C-18.FR-8, C-03.FR-13: deleting a User removes their links and voids their pending requests.
	e.startTelegram(dana, e.conn)
	if a := e.api.do(http.MethodDelete, "/api/v1/users/"+dana.id, ""); a.status/100 != 2 {
		t.Fatalf("delete dana = %d %s", a.status, a.body)
	}
	if got := e.api.json(http.MethodGet, "/api/v1/users/"+dana.id+"/account-links", "", http.StatusOK)["items"].([]any); len(got) != 0 {
		t.Errorf("dana's links after the deletion %v", got)
	}
	deleted := 0
	for _, x := range e.audits("account_link.unlink") {
		if x["details"].(map[string]any)["reason"] == "user_deleted" {
			deleted++
		}
	}
	if deleted != 2 {
		t.Errorf("%d user_deleted unlinks", deleted)
	}
	if n := e.h.count(t, `SELECT count(*) FROM account_link_requests r JOIN users u ON u.id = r.user_id
		WHERE u.public_id = '`+dana.id+`' AND r.consumed_at IS NULL AND r.voided_at IS NULL`); n != 0 {
		t.Errorf("%d pending requests of dana", n)
	}

	// Secrets (lint 5): no link token or code reaches the log.
	out := r.Output()
	for _, s := range e.secrets {
		if s != "" && strings.Contains(out, s) {
			t.Error("a link token or code reached the log")
		}
	}
	if len(e.secrets) < 12 {
		t.Errorf("only %d secrets were checked", len(e.secrets))
	}
}
