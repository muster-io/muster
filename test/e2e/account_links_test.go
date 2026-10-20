// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeslack"
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
	deepLink    = regexp.MustCompile(`^https://t\.me/muster_dev_bot\?start=([A-Za-z0-9_-]{43})$`)
	linkCode    = regexp.MustCompile(`^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{8}$`)
	bridgeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
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

// attemptsRemaining is the attempts_remaining of a link_code_invalid refusal, -1 without it.
func attemptsRemaining(t *testing.T, a answer) int {
	t.Helper()
	var p struct {
		AttemptsRemaining *int `json:"attempts_remaining"`
	}
	decode(t, a, &p)
	if p.AttemptsRemaining == nil {
		return -1
	}
	return *p.AttemptsRemaining
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

// hasHint reports whether the stream got the account-links hint that names the link id.
func (e *linksEnv) hasHint(s *stream, id string) bool {
	return slices.ContainsFunc(s.hints(), func(d string) bool {
		return strings.Contains(d, `"type":"account-links"`) && strings.Contains(d, `"id":"`+id+`"`)
	})
}

// retryAfter is the Retry-After of a 429 link_code_requests_exceeded.
func retryAfter(t *testing.T, a answer) int {
	t.Helper()
	if a.status != http.StatusTooManyRequests || !strings.Contains(string(a.body), `"code":"link_code_requests_exceeded"`) {
		t.Fatalf("answer %d %s, want 429 link_code_requests_exceeded", a.status, a.body)
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
// refused in both messengers (C-18.FR-4, FR-5); the Connections to link through, the demo Bridge Connection with its
// link methods among them (C-18.FR-10, C-22.FR-15); tokens refused where only the web session may act, the Bridge
// operations too, Admins listing and removing links, and 405 for a method a path does not have (C-18.FR-6,
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
	aliceLinks := alice.session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK)
	if got := links(t, aliceLinks); !slices.Equal(
		got, []string{"telegram " + devmode.TelegramConnectionName + " 7001 alice_t"}) {
		t.Errorf("alice's links %v", got)
	}
	if id, _ := linkOf(t, aliceLinks, "telegram")["id"].(string); !e.hasHint(aliceHints, id) {
		t.Errorf("the hint does not name the link %s: %v", id, aliceHints.hints())
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
		a := e.confirm(bob, rid, wrong)
		if got := fieldError(t, a); got != "link_code_invalid /code" || attemptsRemaining(t, a) != 4-i {
			t.Errorf("wrong code %d: %s, %d left", i+1, got, attemptsRemaining(t, a))
		}
	}
	if a := e.confirm(bob, rid, code); fieldError(t, a) != "link_code_invalid /code" || attemptsRemaining(t, a) != 0 {
		t.Errorf("the right code after five wrong ones: %s", a.body)
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
	if a := e.confirm(bob, rid, code); fieldError(t, a) != "link_code_invalid /code" || attemptsRemaining(t, a) != 0 {
		t.Errorf("a consumed request: %s", a.body)
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

	// C-18.FR-10, C-22.FR-15: the Connections to link through, with only id, name and messenger — and the link methods
	// and the lookup kind on Bridge rows only — for a Viewer; not for a Service account.
	conns := dana.session.json(http.MethodGet, "/api/v1/me/account-links/connections", "", http.StatusOK)["items"].([]any)
	var names []string
	var bridgeConn string
	for _, x := range conns {
		c := x.(map[string]any)
		keys := slices.Sorted(func(yield func(string) bool) {
			for k := range c {
				if !yield(k) {
					return
				}
			}
		})
		want := []string{"id", "messenger", "name"}
		if c["messenger"] == "bridge" {
			want = []string{"handle_kind", "id", "link_methods", "messenger", "name"}
			bridgeConn, _ = c["id"].(string)
		}
		if !slices.Equal(keys, want) {
			t.Errorf("connection keys %v of %v", keys, c["messenger"])
		}
		names = append(names, fmt.Sprint(c["messenger"], " ", c["name"]))
	}
	if !slices.Contains(names, "mattermost mm") || !slices.Contains(names, "telegram "+devmode.TelegramConnectionName) ||
		!slices.Contains(names, "bridge "+devmode.BridgeConnectionName) || !slices.IsSorted(names) {
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
		{alice.pat, http.MethodPost, "/api/v1/me/account-links/bridge", `{"connection_id":"` + bridgeConn + `"}`},
		{alice.pat, http.MethodPost, "/api/v1/me/account-links/bridge/codes", `{"connection_id":"` + bridgeConn +
			`","handle":"alice"}`},
		{alice.pat, http.MethodPost, "/api/v1/me/account-links/bridge/confirmation", `{"request_id":"` + rid +
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

// bridgeLinksEnv is the brd environment with the people and Audit log checks of linksEnv: accounts linked through the
// demo Connection "Dev Bridge" of the fake bridge's first bot and through "Lab Bridge" of its second.
type bridgeLinksEnv struct {
	*linksEnv
	b   *brd
	lab string
}

// check runs the check of the Connection conn, which reads its info again, and expects it to pass.
func (e *bridgeLinksEnv) check(conn string) {
	e.t.Helper()
	if c := e.api.json(http.MethodPost, "/api/v1/connections/"+conn+"/checks", "", http.StatusOK); c["ok"] != true {
		e.t.Fatalf("the check of %s: %v", conn, c)
	}
}

// linkable is what p sees of the Bridge Connections to link through: by name, the link methods and the lookup kind.
func (e *bridgeLinksEnv) linkable(p linker) map[string]string {
	e.t.Helper()
	out := map[string]string{}
	for _, x := range p.session.json(http.MethodGet, "/api/v1/me/account-links/connections", "",
		http.StatusOK)["items"].([]any) {
		if c := x.(map[string]any); c["messenger"] == "bridge" {
			out[c["name"].(string)] = fmt.Sprint(c["link_methods"], " ", c["handle_kind"])
		}
	}
	return out
}

// startBridge starts a link of p through the Bridge Connection conn by token and returns the token, whose link the
// link_url_template of the fake carries.
func (e *bridgeLinksEnv) startBridge(p linker, conn string) string {
	e.t.Helper()
	m := p.session.json(http.MethodPost, "/api/v1/me/account-links/bridge", `{"connection_id":"`+conn+`"}`,
		http.StatusCreated)
	token, _ := m["token"].(string)
	if !bridgeToken.MatchString(token) || m["link_url"] != e.h.Fakes.Bridge+"/bot?start="+token ||
		m["expires_at"] == nil {
		e.t.Fatalf("a started bridge link %v", m)
	}
	e.secrets = append(e.secrets, token)
	return token
}

// bridgeAnswers are the answers Muster made through the fake bridge.
func (e *bridgeLinksEnv) bridgeAnswers() []fakebridge.Answer {
	e.t.Helper()
	var out []fakebridge.Answer
	decode(e.t, call(e.t, http.MethodGet, e.b.fb+"/answers", ""), &out)
	return out
}

// sendToken has the person userID of the fake messenger send token to the fake's bot, and returns Muster's answer
// once it is there.
func (e *bridgeLinksEnv) sendToken(bot int, userID, token string) fakebridge.Answer {
	e.t.Helper()
	before := len(e.bridgeAnswers())
	e.fake(http.MethodPost, e.b.fb+"/link", fmt.Sprintf(`{"user_id":%q,"token":%q,"bot":%d}`, userID, token, bot))
	var got []fakebridge.Answer
	eventually(e.t, "the answer to the token of "+userID, func() bool {
		got = e.bridgeAnswers()
		return len(got) > before
	})
	a := got[before]
	if a.Bot != bot || a.EventType != fakebridge.TypeAccountLink || a.UserID != userID || a.ShownAs != "direct" {
		e.t.Errorf("the answer %+v", a)
	}
	return a
}

func (e *bridgeLinksEnv) requestCode(p linker, conn, handle string) answer {
	e.t.Helper()
	return p.session.do(http.MethodPost, "/api/v1/me/account-links/bridge/codes",
		fmt.Sprintf(`{"connection_id":%q,"handle":%q}`, conn, handle))
}

func (e *bridgeLinksEnv) confirmCode(p linker, request, code string) answer {
	e.t.Helper()
	return p.session.do(http.MethodPost, "/api/v1/me/account-links/bridge/confirmation", `{"request_id":"`+request+
		`","code":"`+code+`"}`)
}

func (e *bridgeLinksEnv) bridgeDMs() []fakebridge.DirectMessage {
	e.t.Helper()
	var out []fakebridge.DirectMessage
	decode(e.t, call(e.t, http.MethodGet, e.b.fb+"/direct-messages", ""), &out)
	return out
}

// codeFor requests a code for p through the Connection Lab Bridge, the fake's second bot, to the account with handle,
// and returns the request id and the code from the bot's direct message to userID.
func (e *bridgeLinksEnv) codeFor(p linker, handle, userID string) (string, string) {
	e.t.Helper()
	before := len(e.bridgeDMs())
	a := e.requestCode(p, e.lab, handle)
	var s struct {
		RequestID string  `json:"request_id"`
		Attempts  int     `json:"attempts_remaining"`
		ExpiresAt *string `json:"expires_at"`
	}
	if a.status != http.StatusAccepted {
		e.t.Fatalf("a bridge code for %s = %d %s", handle, a.status, a.body)
	}
	decode(e.t, a, &s)
	if s.Attempts != 5 || s.ExpiresAt == nil {
		e.t.Errorf("the started code request %s", a.body)
	}
	dms := e.bridgeDMs()
	if len(dms) != before+1 {
		e.t.Fatalf("%d direct messages after %d", len(dms), before)
	}
	dm := dms[len(dms)-1]
	text, code, _ := strings.Cut(dm.Body, "\n\n")
	if dm.Bot != 2 || dm.UserExternalID != userID || dm.Key != "dm-"+s.RequestID || text != textCodeRequest ||
		!linkCode.MatchString(code) {
		e.t.Fatalf("the code message %+v", dm)
	}
	e.secrets = append(e.secrets, code)
	return s.RequestID, code
}

// TestBridgeAccountLinks is the Verification of S-076 against `muster dev` and the fake bridge, with two Bridge
// Connections to it — "Dev Bridge" of its first bot and "Lab Bridge" of its second — and no link row inserted by hand:
// the link methods and the lookup kind each Connection offers from its info (C-22.FR-15, AC-19); a token sent to the
// bot links the sender's account with the name the bridge reported, and is answered in the direct chat (C-22.AC-18,
// FR-13); the same token again, a token of the other Connection, an expired and an unknown token link nothing and get
// the same answer, recorded with their reasons; each Connection needs its own link; the code by direct message with an unknown address, a person the bot cannot write to, five wrong codes, the right
// code without a confirming message, and the limit for one account (C-22.AC-19); an account linked to another User
// (C-18.FR-5); no code without a user lookup, and no link without a link_url_template; the Mention of Alice by a marker and her entry, Bob without a link for the
// Connection as text, and the footer naming Alice by her link's name although her link has no username (C-22.AC-20,
// FR-16); and no token or code in the log.
func TestBridgeAccountLinks(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	b := newBRD(t, h, r)
	t.Cleanup(func() {
		restore(t, http.MethodPut, b.fb+"/config", `{"user_lookup":"login","link_url_template":null,"dm_blocked":[]}`)
	})
	e := &bridgeLinksEnv{linksEnv: &linksEnv{timersEnv: &timersEnv{tgd: &tgd{mm: b.mm}}}, b: b}
	e.lab = e.api.json(http.MethodPost, "/api/v1/connections", `{"type":"bridge","name":"Lab Bridge","base_url":"`+
		h.Fakes.Bridge+`","bot_token":"`+fakebridge.SecondToken+`","event_delivery":"poll","proxy":{"enabled":false},
		"limiter":{"limit":10,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	alice := e.person("Alice A.", "alice", "responder")
	bob := e.person("Bob B.", "bob", "responder")
	b.configure(`{"user_lookup":"none","link_url_template":"` + h.Fakes.Bridge + `/bot?start={token}"}`)
	e.check(b.conn)
	e.check(e.lab)
	if got := e.linkable(alice); got["Dev Bridge"] != "[token] <nil>" || got["Lab Bridge"] != "[token] <nil>" {
		t.Errorf("linkable without a user lookup %v", got)
	}

	// C-22.AC-18, FR-13: a token sent to the bot of Dev Bridge links Alice's account with the name the bridge reported.
	tok := e.startBridge(alice, b.conn)
	if a := e.sendToken(1, fakebridge.AliceID, tok); a.Text != "Linked to Muster user Alice A. (alice)" {
		t.Errorf("the answer to the token %q", a.Text)
	}
	l := linkOf(t, alice.session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK), "bridge")
	if l["external_id"] != fakebridge.AliceID || l["display_name"] != "Alice Smith" || l["username"] != nil ||
		l["connection"].(map[string]any)["name"] != devmode.BridgeConnectionName {
		t.Errorf("alice's bridge link %v", l)
	}
	if d, _, transport := e.lastAudit("account_link.link"); d["messenger"] != "bridge" ||
		d["external_id"] != fakebridge.AliceID || d["display_name"] != "Alice Smith" || d["connection"] != b.conn ||
		transport != "bridge" {
		t.Errorf("the link entry %v through %s", d, transport)
	}
	if a := e.sendToken(1, fakebridge.BobID, tok); a.Text != textLinkInvalid {
		t.Errorf("a used token: %q", a.Text)
	}
	e.wantRejection("token_used", "bridge", fakebridge.BobID)

	// A token of Lab Bridge sent to the bot of Dev Bridge links nothing; Lab Bridge needs a link of its own.
	tok = e.startBridge(alice, e.lab)
	if a := e.sendToken(1, fakebridge.AliceID, tok); a.Text != textLinkInvalid {
		t.Errorf("a token at the other Connection's bot: %q", a.Text)
	}
	e.wantRejection("wrong_connection", "bridge", fakebridge.AliceID)
	if a := e.sendToken(2, fakebridge.AliceID, tok); a.Text != "Linked to Muster user Alice A. (alice)" {
		t.Errorf("the token at its own bot: %q", a.Text)
	}
	if got := links(t, alice.session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK)); !slices.Equal(
		got, []string{"bridge Dev Bridge u-alice <nil>", "bridge Lab Bridge u-alice <nil>"}) {
		t.Errorf("alice's links %v", got)
	}
	tok = e.startBridge(alice, e.lab)
	advance(t, r, 601)
	if a := e.sendToken(2, fakebridge.AliceID, tok); a.Text != textLinkInvalid {
		t.Errorf("an expired token: %q", a.Text)
	}
	e.wantRejection("token_expired", "bridge", fakebridge.AliceID)
	unknown := make([]byte, 32)
	_, _ = rand.Read(unknown)
	e.secrets = append(e.secrets, base64.RawURLEncoding.EncodeToString(unknown))
	if a := e.sendToken(2, fakebridge.VeraID, base64.RawURLEncoding.EncodeToString(unknown)); a.Text != textLinkInvalid {
		t.Errorf("an unknown token: %q", a.Text)
	}
	e.wantRejection("token_unknown", "bridge", fakebridge.VeraID)

	// C-22.AC-19: a code by direct message through Lab Bridge, whose bridge looks people up by email.
	b.configure(`{"user_lookup":"email"}`)
	e.check(e.lab)
	if got := e.linkable(bob); got["Lab Bridge"] != "[token code] email" {
		t.Errorf("linkable with a lookup by email %v", got)
	}
	if got := fieldError(t, e.requestCode(bob, e.lab, "nobody@example.org")); got != "handle_not_found /handle" {
		t.Errorf("an unknown address: %s", got)
	}
	b.configure(`{"dm_blocked":["u-bob"]}`)
	if got := fieldError(t, e.requestCode(bob, e.lab, "bob@example.org")); got != "direct_message_refused /handle" {
		t.Errorf("a person the bot cannot write to: %s", got)
	}
	b.configure(`{"dm_blocked":[]}`)
	rid, code := e.codeFor(bob, "bob@example.org", fakebridge.BobID)
	wrong := "22222222"
	if code == wrong {
		wrong = "33333333"
	}
	for i := range 5 {
		a := e.confirmCode(bob, rid, wrong)
		if got := fieldError(t, a); got != "link_code_invalid /code" || attemptsRemaining(t, a) != 4-i {
			t.Errorf("wrong code %d: %s, %d left", i+1, got, attemptsRemaining(t, a))
		}
	}
	if a := e.confirmCode(bob, rid, code); fieldError(t, a) != "link_code_invalid /code" || attemptsRemaining(t, a) != 0 {
		t.Errorf("the right code after five wrong ones: %s", a.body)
	}
	e.wantRejection("wrong_code", "bridge", fakebridge.BobID)
	rid, code = e.codeFor(bob, "BOB@example.org", fakebridge.BobID)
	dms := len(e.bridgeDMs())
	l = bob.session.json(http.MethodPost, "/api/v1/me/account-links/bridge/confirmation", `{"request_id":"`+rid+
		`","code":"`+strings.ToLower(code)+`"}`, http.StatusCreated)
	if l["messenger"] != "bridge" || l["external_id"] != fakebridge.BobID || l["display_name"] != "Bob Brown" ||
		l["username"] != "bob@example.org" || l["connection"].(map[string]any)["name"] != "Lab Bridge" {
		t.Errorf("bob's bridge link %v", l)
	}
	if n := len(e.bridgeDMs()); n != dms {
		t.Errorf("%d direct messages after the confirmation, %d before", n, dms)
	}
	if d, _, transport := e.lastAudit("account_link.link"); d["external_id"] != fakebridge.BobID || transport != "ui" {
		t.Errorf("the link entry %v through %s", d, transport)
	}
	// The refused direct message and the two codes sent count toward the three codes an hour for one account.
	if n := retryAfter(t, e.requestCode(bob, e.lab, "bob@example.org")); n < 1 || n > 3600 {
		t.Errorf("the fourth request for bob's account: Retry-After %d", n)
	}
	// C-18.FR-5: an account linked to another User.
	if a := e.sendToken(2, fakebridge.BobID, e.startBridge(alice, e.lab)); a.Text != textLinkConflict {
		t.Errorf("bob's account for alice: %q", a.Text)
	}
	e.wantRejection("conflict", "bridge", fakebridge.BobID)
	b.configure(`{"user_lookup":"none"}`)
	e.check(e.lab)
	if a := e.requestCode(bob, e.lab, "bob@example.org"); a.status != http.StatusConflict ||
		codeOf(t, a) != "code_not_offered" {
		t.Errorf("a code without a user lookup = %d %s", a.status, a.body)
	}
	if got := e.linkable(bob); got["Lab Bridge"] != "[token] <nil>" {
		t.Errorf("linkable without a user lookup again %v", got)
	}
	// Without a link_url_template the token comes without a link.
	b.configure(`{"link_url_template":null}`)
	e.check(e.lab)
	if m := alice.session.json(http.MethodPost, "/api/v1/me/account-links/bridge", `{"connection_id":"`+e.lab+`"}`,
		http.StatusCreated); m["link_url"] != nil {
		t.Errorf("a link without a link_url_template %v", m["link_url"])
	} else {
		e.secrets = append(e.secrets, m["token"].(string))
	}

	// C-22.AC-20, FR-16: a Destination of Dev Bridge that mentions Alice and Bob for new Alert Groups.
	mentions := map[string]any{}
	for _, kind := range []string{"new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended",
		"rise_to_urgent"} {
		mentions[kind] = map[string]any{"everyone": "none", "user_ids": []string{}, "groups": []string{}}
	}
	mentions["new_alert_group"] = map[string]any{"everyone": "none", "user_ids": []string{alice.id, bob.id},
		"groups": []string{}}
	dest := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"bridge","name":"alerts",
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":30,"per_seconds":60}}`, b.conn,
		fakebridge.ChannelAlerts, mustJSON(mentions)), http.StatusCreated)["destination"].(map[string]any)["id"].(string)
	b.route("links", "links", []string{dest}, nil)
	g := b.start("ml", "Mention", "links", "links")
	env := b.waitPosted("Mention", 1)[0].Envelope
	first, _, _ := strings.Cut(env.Body, "\n")
	if mustJSON(env.Mentions) != `[{"display_name":"Alice Smith","external_id":"u-alice","ref":"m1"}]` ||
		first != "[@Alice Smith](muster-mention:m1) Bob B." || !env.Notify {
		t.Errorf("the Root message: mentions %s, first line %q, notify %v", mustJSON(env.Mentions), first, env.Notify)
	}
	alice.pat.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/acknowledge", "", http.StatusOK)
	eventually(t, "the footer naming alice", func() bool {
		edits := b.posted("Mention")[0].Edits
		return len(edits) > 0 && strings.HasSuffix(edits[len(edits)-1].Body, "Acknowledged by Alice Smith")
	})

	// Secrets (lint 5): no link token or code reaches the log.
	out := r.Output()
	for _, s := range e.secrets {
		if s != "" && strings.Contains(out, s) {
			t.Error("a link token or code reached the log")
		}
	}
	if len(e.secrets) < 8 {
		t.Errorf("only %d secrets were checked", len(e.secrets))
	}
}

// slackLinksEnv is the slk environment with the people and Audit log checks of linksEnv: accounts linked through the
// Connection "sl" of the fake Slack server's second app and through the demo Connection "Dev Slack" of its first.
type slackLinksEnv struct {
	*linksEnv
	s   *slk
	dev string
}

func (e *slackLinksEnv) requestSlackCode(p linker, conn, handle string) answer {
	e.t.Helper()
	return p.session.do(http.MethodPost, "/api/v1/me/account-links/slack",
		fmt.Sprintf(`{"connection_id":%q,"handle":%q}`, conn, handle))
}

func (e *slackLinksEnv) confirmSlackCode(p linker, request, code string) answer {
	e.t.Helper()
	return p.session.do(http.MethodPost, "/api/v1/me/account-links/slack/confirmation", `{"request_id":"`+request+
		`","code":"`+code+`"}`)
}

// slackDMs are the direct messages the bot of the fake's app sent to user.
func (e *slackLinksEnv) slackDMs(app, user string) []fakeslack.Message {
	e.t.Helper()
	var out []fakeslack.Message
	decode(e.t, call(e.t, http.MethodGet, e.s.fs+"/dms?"+url.Values{"app": {app}, "user": {user}}.Encode(), ""), &out)
	return out
}

// slackCodeFor requests a code for p through the Connection conn to the account with handle, and returns the request
// id and the code from the direct message the bot of app sent to user.
func (e *slackLinksEnv) slackCodeFor(p linker, conn, handle, app, user string) (string, string) {
	e.t.Helper()
	before := len(e.slackDMs(app, user))
	a := e.requestSlackCode(p, conn, handle)
	var st struct {
		RequestID string  `json:"request_id"`
		Attempts  int     `json:"attempts_remaining"`
		ExpiresAt *string `json:"expires_at"`
	}
	if a.status != http.StatusAccepted {
		e.t.Fatalf("a slack code for %s = %d %s", handle, a.status, a.body)
	}
	decode(e.t, a, &st)
	if st.Attempts != 5 || st.ExpiresAt == nil {
		e.t.Errorf("the started code request %s", a.body)
	}
	dms := e.slackDMs(app, user)
	if len(dms) != before+1 {
		e.t.Fatalf("%d direct messages to %s after %d", len(dms), user, before)
	}
	text, code, _ := strings.Cut(dms[len(dms)-1].Text, "\n\n")
	if text != textCodeRequest || !linkCode.MatchString(code) {
		e.t.Fatalf("the code message %q", dms[len(dms)-1].Text)
	}
	e.secrets = append(e.secrets, code)
	return st.RequestID, code
}

// slackProblem is the code, pointer and detail of the first error of a 422.
func slackProblem(t *testing.T, a answer) string {
	t.Helper()
	var p struct {
		Errors []struct {
			Code    string `json:"code"`
			Pointer string `json:"pointer"`
			Detail  string `json:"detail"`
		} `json:"errors"`
	}
	decode(t, a, &p)
	if a.status != http.StatusUnprocessableEntity || len(p.Errors) == 0 {
		return fmt.Sprintf("%d %s", a.status, a.body)
	}
	return p.Errors[0].Code + " " + p.Errors[0].Pointer + " " + p.Errors[0].Detail
}

// TestSlackAccountLinks is the Verification of S-102 against `muster dev` and the fake Slack server, with no link row
// inserted by hand: the Slack Connections to link through (C-24.FR-12); a code for an email in other letter case,
// sent by direct message to the account's user id, and the link it makes with the account's id, name and display name,
// confirmed by the bot (C-24.AC-14); a name found on the second page of users.list, five wrong codes and the right one
// refused after them; no account, a name two accounts share, the direct messages Slack refuses and the app's Messages
// tab off; the limits of
// account_link.slack_code_requests; an account linked to another User (C-18.FR-5); a second link through one
// Connection replacing the first (C-18.FR-4); tokens refused where only the web session may act; the codes masked in
// the Request log (C-23.FR-3); Alice mentioned as <@U0FAKEALICE> and Dora by her name, and the footer naming Alice by
// her link's display name (C-24.FR-6, AC-15, C-12.FR-12); a press of Alice's account running as her, and answered "not
// linked" once she unlinked it; and no code in the log.
func TestSlackAccountLinks(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	s := newSlack(t, h, r)
	t.Cleanup(func() {
		restore(t, http.MethodDelete, s.fs+"/faults", "")
		restore(t, http.MethodPut, s.fs+"/config", `{"request_url":{"b":null}}`)
	})
	e := &slackLinksEnv{linksEnv: &linksEnv{timersEnv: &timersEnv{tgd: &tgd{mm: s.mm}}}, s: s}
	e.dev = s.connectionNamed(devmode.SlackConnectionName)
	alice := e.person("Alice A.", "alice", "responder")
	bob := e.person("Bob B.", "bob", "responder")
	dora := e.person("Dora D.", "dora", "responder")
	slackLink := func(p linker) map[string]any {
		t.Helper()
		for _, x := range p.session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK)["items"].([]any) {
			if l := x.(map[string]any); l["messenger"] == "slack" && l["connection"].(map[string]any)["id"] == s.conn {
				return l
			}
		}
		return nil
	}

	// C-24.FR-12: the Slack Connections that are not deleted, with only their id, name and messenger.
	var got []string
	for _, x := range alice.session.json(http.MethodGet, "/api/v1/me/account-links/connections", "",
		http.StatusOK)["items"].([]any) {
		c := x.(map[string]any)
		if c["messenger"] != "slack" {
			continue
		}
		got = append(got, c["name"].(string))
		if keys := slices.Sorted(maps.Keys(c)); !slices.Equal(keys, []string{"id", "messenger", "name"}) {
			t.Errorf("keys %v of %v", keys, c["name"])
		}
	}
	if !slices.Equal(got, []string{devmode.SlackConnectionName, "sl"}) {
		t.Errorf("slack connections %v", got)
	}

	// C-24.FR-12, AC-14: an email in other letter case, the code by direct message, the link and its confirmation.
	rid, code := e.slackCodeFor(alice, s.conn, "Alice@Example.ORG", fakeslack.AppB, fakeslack.UserAlice)
	l := alice.session.json(http.MethodPost, "/api/v1/me/account-links/slack/confirmation", `{"request_id":"`+rid+
		`","code":"`+strings.ToLower(code)+`"}`, http.StatusCreated)
	if l["messenger"] != "slack" || l["external_id"] != fakeslack.UserAlice || l["username"] != "alice" ||
		l["display_name"] != "Alice Smith" || l["connection"].(map[string]any)["name"] != "sl" {
		t.Errorf("alice's slack link %v", l)
	}
	dms := e.slackDMs(fakeslack.AppB, fakeslack.UserAlice)
	if got := dms[len(dms)-1].Text; got != "Linked to Muster user Alice A." {
		t.Errorf("the confirmation %q", got)
	}
	if d, _, transport := e.lastAudit("account_link.link"); d["messenger"] != "slack" ||
		d["external_id"] != fakeslack.UserAlice || d["display_name"] != "Alice Smith" || transport != "ui" {
		t.Errorf("the link entry %v through %s", d, transport)
	}

	// C-24.AC-14: a name on the second page of users.list; five wrong codes, then the right one, refused.
	pages := len(s.requests("users.list"))
	rid, code = e.slackCodeFor(bob, s.conn, "@BOB", fakeslack.AppB, fakeslack.UserBob)
	lists := s.requests("users.list")[pages:]
	if len(lists) != 3 {
		t.Fatalf("%d pages of users.list", len(lists))
	}
	for i, req := range lists {
		form, _ := url.ParseQuery(req.Body)
		if form.Get("limit") != "200" || (form.Get("cursor") == "") != (i == 0) {
			t.Errorf("page %d: %s", i+1, req.Body)
		}
	}
	wrong := "22222222"
	if code == wrong {
		wrong = "33333333"
	}
	for i := range 5 {
		a := e.confirmSlackCode(bob, rid, wrong)
		if got := fieldError(t, a); got != "link_code_invalid /code" || attemptsRemaining(t, a) != 4-i {
			t.Errorf("wrong code %d: %s, %d left", i+1, got, attemptsRemaining(t, a))
		}
	}
	if a := e.confirmSlackCode(bob, rid, code); fieldError(t, a) != "link_code_invalid /code" ||
		attemptsRemaining(t, a) != 0 {
		t.Errorf("the right code after five wrong ones: %s", a.body)
	}
	e.wantRejection("wrong_code", "slack", fakeslack.UserBob)
	rid, code = e.slackCodeFor(bob, s.conn, "bob", fakeslack.AppB, fakeslack.UserBob)
	l = bob.session.json(http.MethodPost, "/api/v1/me/account-links/slack/confirmation", `{"request_id":"`+rid+
		`","code":"`+code+`"}`, http.StatusCreated)
	if l["external_id"] != fakeslack.UserBob || l["username"] != "bob" || l["display_name"] != "Bob Jones" {
		t.Errorf("bob's slack link %v", l)
	}

	// C-24.FR-12: no account, a name two accounts share, and the direct messages Slack refuses for the person.
	for handle, want := range map[string]string{
		"Sam":                 "handle_ambiguous /handle Several Slack accounts have this name. Enter the email instead.",
		"nobody":              "handle_not_found /handle No Slack account with this email or name was found.",
		"bob.old@example.org": "handle_not_found /handle No Slack account with this email or name was found.",
		"helper":              "handle_not_found /handle No Slack account with this email or name was found.",
	} {
		if got := slackProblem(t, e.requestSlackCode(dora, s.conn, handle)); got != want {
			t.Errorf("%s: %s", handle, got)
		}
	}
	for _, code := range []string{"cannot_dm_bot", "user_not_found", "channel_not_found"} {
		s.fault("chat.postMessage", http.StatusOK, code, 0)
		if got := slackProblem(t, e.requestSlackCode(dora, s.conn, "carol")); got !=
			"direct_message_refused /handle The bot cannot send a direct message to this Slack account." {
			t.Errorf("%s: %s", code, got)
		}
	}
	if dms := e.slackDMs(fakeslack.AppB, fakeslack.UserCarol); len(dms) != 0 {
		t.Errorf("a refused direct message reached carol: %+v", dms)
	}

	// C-24.FR-12: with the Messages tab of the Slack app off, Slack refuses every direct message, which is 409
	// messages_tab_disabled; eve and dora's account have no request yet, so no limit is near.
	eve := e.person("Eve E.", "eve", "responder")
	s.fake(http.MethodPut, s.fs+"/config", `{"messages_tab_off":["b"]}`)
	a := e.requestSlackCode(eve, s.conn, "dora@example.org")
	s.fake(http.MethodPut, s.fs+"/config", `{"messages_tab_off":[]}`)
	if a.status != http.StatusConflict || codeOf(t, a) != "messages_tab_disabled" {
		t.Errorf("the Messages tab off = %d %s", a.status, a.body)
	}
	if dms := e.slackDMs(fakeslack.AppB, fakeslack.UserDora); len(dms) != 0 {
		t.Errorf("a direct message reached dora with the Messages tab off: %+v", dms)
	}

	// C-18.FR-5: an account linked to another User; it is checked after the limits, so it comes before bob's third
	// request for his account.
	if a := e.requestSlackCode(alice, s.conn, "bob@example.org"); a.status != http.StatusConflict ||
		codeOf(t, a) != "account_linked_elsewhere" {
		t.Errorf("bob's account for alice = %d %s", a.status, a.body)
	}
	e.wantRejection("conflict", "slack", fakeslack.UserBob)

	// account_link.slack_code_requests: bob's two earlier codes and this one are the three an hour for his account.
	e.slackCodeFor(bob, s.conn, "bob", fakeslack.AppB, fakeslack.UserBob)
	if n := retryAfter(t, e.requestSlackCode(bob, s.conn, "bob")); n < 1 || n > 3600 {
		t.Errorf("the fourth request for bob's account: Retry-After %d", n)
	}

	// C-18.FR-4: a second link of dora through Dev Slack replaces her first.
	rid, code = e.slackCodeFor(dora, e.dev, "carol", fakeslack.AppA, fakeslack.UserCarol)
	dora.session.json(http.MethodPost, "/api/v1/me/account-links/slack/confirmation", `{"request_id":"`+rid+
		`","code":"`+code+`"}`, http.StatusCreated)
	rid, code = e.slackCodeFor(dora, e.dev, "dora@example.org", fakeslack.AppA, fakeslack.UserDora)
	dora.session.json(http.MethodPost, "/api/v1/me/account-links/slack/confirmation", `{"request_id":"`+rid+
		`","code":"`+code+`"}`, http.StatusCreated)
	if got := links(t, dora.session.json(http.MethodGet, "/api/v1/me/account-links", "", http.StatusOK)); !slices.Equal(
		got, []string{"slack " + devmode.SlackConnectionName + " " + fakeslack.UserDora + " dora"}) {
		t.Errorf("dora's links %v", got)
	}
	if d, _, _ := e.lastAudit("account_link.unlink"); d["reason"] != "replaced" ||
		d["external_id"] != fakeslack.UserCarol || d["messenger"] != "slack" {
		t.Errorf("the replaced link %v", d)
	}

	// C-18.AC-4: tokens, an Admin's included, cannot start or confirm a link.
	for _, who := range []patClient{alice.pat, e.api} {
		for path, body := range map[string]string{
			"/api/v1/me/account-links/slack":              `{"connection_id":"` + s.conn + `","handle":"alice"}`,
			"/api/v1/me/account-links/slack/confirmation": `{"request_id":"ARAAAAAAAAAAA1","code":"22222222"}`,
		} {
			if a := who.do(http.MethodPost, path, body); a.status != http.StatusForbidden ||
				codeOf(t, a) != "session_required" {
				t.Errorf("POST %s with a token = %d %s", path, a.status, a.body)
			}
		}
	}

	// C-23.FR-3: the codes are masked in the Request log of the direct messages.
	masked := 0
	for _, conn := range []string{s.conn, e.dev} {
		for _, rec := range s.requestLog("connection=" + conn + "&operation=chat.postMessage") {
			full, raw := s.requestRecord(rec["id"].(string))
			for _, code := range e.secrets {
				if strings.Contains(raw, code) {
					t.Errorf("a code in the record %s", rec["id"])
				}
			}
			if body, _ := full["request_body"].(string); strings.Contains(body, textCodeRequest) &&
				strings.Contains(body, "[redacted]") {
				masked++
			}
		}
	}
	if masked < 6 {
		t.Errorf("%d records with a masked code", masked)
	}

	// C-24.FR-6, AC-15, C-12.FR-12: Alice by <@U0FAKEALICE>, Dora by her name, and the footer by Alice's link.
	d := s.api.do(http.MethodGet, "/api/v1/destinations/"+s.dest, "")
	var dest map[string]any
	decode(t, d, &dest)
	mentions := dest["mentions"].(map[string]any)
	mentions["new_alert_group"] = map[string]any{"everyone": "none", "user_ids": []string{alice.id, dora.id},
		"groups": []string{}}
	body, _ := json.Marshal(map[string]any{"type": dest["type"], "name": dest["name"],
		"connection_id": dest["connection_id"], "channel_id": dest["channel_id"], "limiter": dest["limiter"],
		"mentions": mentions})
	s.api.json(http.MethodPut, "/api/v1/destinations/"+s.dest, string(body), http.StatusOK, "If-Match",
		d.header.Get("ETag"))
	s.fire("m1", "MentionMe", "u1", `{"team":"sl","cluster":"m","pod":"u1"}`)
	g := s.groupOf("m")
	root := s.root(s.number(g), "MentionMe")
	top := ""
	if len(root.Top) > 0 && root.Top[0].Text != nil {
		top = root.Top[0].Text.Text
	}
	for _, text := range []string{root.Text, top} {
		if !strings.Contains(text, "<@"+fakeslack.UserAlice+">") || !strings.Contains(text, "Dora D.") ||
			strings.Contains(text, "<@"+fakeslack.UserDora+">") {
			t.Errorf("the mentions in %q", text)
		}
	}
	if strings.Contains(string(root.Message.Attachments), "<@") {
		t.Errorf("a Mention in the attachment %s", root.Message.Attachments)
	}
	alice.pat.json(http.MethodPost, "/api/v1/alert-groups/"+g+"/acknowledge", "", http.StatusOK)
	eventually(t, "the footer naming alice", func() bool {
		return strings.HasSuffix(s.message(root.TS).body(t), "Acknowledged by Alice Smith")
	})

	// C-24.AC-14: a press of Alice's account runs as her; once she unlinked it, a press is answered "not linked".
	s.httpMode()
	s.pressed(root.TS, "unack", fakeslack.UserAlice, nil)
	eventually(t, "the unacknowledgement by alice", func() bool { return s.alertGroup(g)["status"] == "firing" })
	var unacked []timelineEntry
	for _, x := range s.timeline(g, "") {
		if x.Kind == "status" && x.Event == "unacknowledged" {
			unacked = append(unacked, x)
		}
	}
	if len(unacked) != 1 || unacked[0].Actor.Transport != "slack" || unacked[0].Actor.Name != "Alice A." {
		t.Errorf("unacknowledged %+v", unacked)
	}
	l = slackLink(alice)
	if l == nil {
		t.Fatal("alice has no link through sl")
	}
	if a := alice.session.do(http.MethodDelete, "/api/v1/me/account-links/"+l["id"].(string), ""); a.status !=
		http.StatusNoContent {
		t.Fatalf("unlink = %d %s", a.status, a.body)
	}
	s.waitActions(root.TS, "ack")
	before := len(s.ephemerals())
	s.pressed(root.TS, "ack", fakeslack.UserAlice, nil)
	if got := s.answered(before, 1)[0]; got.Text != slackNotLinked || got.User != fakeslack.UserAlice {
		t.Errorf("the answer to an unlinked press %+v", got)
	}
	if st := s.alertGroup(g)["status"]; st != "firing" {
		t.Errorf("an unlinked press changed %s: %v", g, st)
	}

	// Secrets (lint 5): no link code reaches the log.
	out := r.Output()
	for _, c := range e.secrets {
		if c != "" && strings.Contains(out, c) {
			t.Error("a link code reached the log")
		}
	}
	if len(e.secrets) < 6 {
		t.Errorf("only %d codes were checked", len(e.secrets))
	}
}
