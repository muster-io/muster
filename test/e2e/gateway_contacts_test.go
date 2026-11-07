// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// contactCode is the code in the text of a notification with a code.
var contactCode = regexp.MustCompile(`^Your Muster code: ([0-9]{6})\. It is valid for 10 minutes\. Do not share it\.$`)

// contactsEnv drives the contacts at the demo Notification gateway "Example Gateway": the mm environment, the fake
// bridge, and every contact and code it saw, which the log must not carry.
type contactsEnv struct {
	*mm
	fb      string
	gw      string
	secrets []string
}

// contactPerson is a signed-in person: their id, their web sessions and a Personal access token.
type contactPerson struct {
	id       string
	session  *agent
	session2 *agent
	pat      patClient
}

// person creates the Responder login, sets a password through the setup link and signs them in twice.
func (e *contactsEnv) person(name, login string) contactPerson {
	e.t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "login": login, "role": "responder"})
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
	p := contactPerson{id: created["user"].(map[string]any)["id"].(string)}
	for _, s := range []**agent{&p.session, &p.session2} {
		*s = newAgent(e.t, e.r.App)
		if a := (*s).signIn(login, login+"-password-1"); a.status != http.StatusCreated {
			e.t.Fatalf("sign in %s = %d %s", login, a.status, a.body)
		}
	}
	token := p.session.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"cli","permissions":["alert-groups:read"]}`, http.StatusCreated)["value"].(string)
	p.pat = patClient{t: e.t, base: e.r.App, token: token}
	return p
}

// notifications are the notifications the fake bridge's bots sent.
func (e *contactsEnv) notifications() []fakebridge.Notification {
	e.t.Helper()
	var out []fakebridge.Notification
	decode(e.t, call(e.t, http.MethodGet, e.fb+"/notifications", ""), &out)
	return out
}

// lastCode is the code of the last notification, which must go to contact without action data.
func (e *contactsEnv) lastCode(contact string) string {
	e.t.Helper()
	all := e.notifications()
	if len(all) == 0 {
		e.t.Fatal("no notification")
	}
	n := all[len(all)-1]
	m := contactCode.FindStringSubmatch(n.Text)
	if n.Contact != contact || n.Action != nil || n.Bot != 3 || n.Language != "en" || m == nil {
		e.t.Fatalf("the notification with the code %+v", n)
	}
	e.secrets = append(e.secrets, m[1])
	return m[1]
}

// add adds contact for p from the session s and returns the contact's id and the code the gateway got.
func (e *contactsEnv) add(s *agent, contact string) (string, string) {
	e.t.Helper()
	c := s.json(http.MethodPost, "/api/v1/me/gateway-contacts", `{"connection_id":"`+e.gw+`","contact":"`+contact+
		`"}`, http.StatusAccepted)
	if c["contact"] != contact || c["confirmed"] != false {
		e.t.Fatalf("created %v", c)
	}
	return c["id"].(string), e.lastCode(contact)
}

func (e *contactsEnv) newCode(s *agent, id string) answer {
	e.t.Helper()
	return s.do(http.MethodPost, "/api/v1/me/gateway-contacts/"+id+"/code", "")
}

func (e *contactsEnv) confirm(s *agent, id, code string) answer {
	e.t.Helper()
	return s.do(http.MethodPost, "/api/v1/me/gateway-contacts/"+id+"/confirmation", `{"code":"`+code+`"}`)
}

// codeRefused is the attempts_remaining of a 422 contact_code_invalid at /code, -1 for any other answer.
func codeRefused(t *testing.T, a answer) int {
	t.Helper()
	if fieldError(t, a) != "contact_code_invalid /code" {
		return -1
	}
	return attemptsRemaining(t, a)
}

// requestsExceeded is the Retry-After of a 429 contact_code_requests_exceeded, 0 for any other answer.
func requestsExceeded(t *testing.T, a answer) int {
	t.Helper()
	if a.status != http.StatusTooManyRequests ||
		!strings.Contains(string(a.body), `"code":"contact_code_requests_exceeded"`) {
		return 0
	}
	n, _ := strconv.Atoi(a.header.Get("Retry-After"))
	return n
}

// contactsOf are the contacts of a list as "contact confirmed replaces".
func contactsOf(m map[string]any) []string {
	out := []string{}
	for _, x := range m["items"].([]any) {
		c := x.(map[string]any)
		replaces := "-"
		if r, ok := c["replaces"].(string); ok {
			replaces = r
		}
		out = append(out, c["contact"].(string)+" "+strconv.FormatBool(c["confirmed"].(bool))+" "+replaces)
	}
	return out
}

// TestGatewayContacts is the Verification of S-167 against `muster dev` and the demo Notification gateway of its fake
// bridge (C-30.FR-3, FR-10, AC-2, C-03.FR-13, C-18.FR-6, C-23.FR-3): a contact added from the web session gets its
// code through sendNotification without action data, a wrong code is refused with the attempts left, the right one
// confirms it and is void after; a Personal access token cannot change the contacts and an Admin cannot set one but
// lists and removes them; three codes per hour for one contact, a code of another session, an expired code, a new
// contact that replaces the confirmed one only once confirmed, the sixth request of one User across two contacts,
// five wrong codes and deleteMyGatewayContact; the Request log masks the contacts and the codes; deleting the User
// erases the contacts, shown as [erased] in the Audit log; and the log carries neither.
func TestGatewayContacts(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := &contactsEnv{mm: newMM(t, h, r), fb: h.Fakes.Bridge + "/_fake"}
	e.gw = e.connectionNamed(devmode.GatewayConnectionName)
	petr := e.person("Petr", "petr")
	const first, second, third, fourth = "+10000000002", "+10000000003", "+10000000004", "+10000000005"
	e.secrets = append(e.secrets, first, second, third, fourth)

	// Added and confirmed with the code the gateway got.
	c, code := e.add(petr.session, first)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	if n := codeRefused(t, e.confirm(petr.session, c, wrong)); n != 4 {
		t.Errorf("a wrong code: %d left", n)
	}
	got := petr.session.json(http.MethodPost, "/api/v1/me/gateway-contacts/"+c+"/confirmation", `{"code":"`+code+`"}`,
		http.StatusOK)
	if got["confirmed"] != true || got["contact"] != first || got["confirmed_at"] == nil {
		t.Fatalf("confirmed %v", got)
	}
	if n := codeRefused(t, e.confirm(petr.session, c, code)); n != 0 {
		t.Errorf("the code again: %d left", n)
	}

	// A token cannot; an Admin cannot set; an Admin lists.
	for _, a := range []answer{
		petr.pat.do(http.MethodDelete, "/api/v1/me/gateway-contacts/"+c, ""),
		petr.pat.do(http.MethodPost, "/api/v1/me/gateway-contacts", `{"connection_id":"`+e.gw+`","contact":"+1"}`),
		petr.pat.do(http.MethodPost, "/api/v1/me/gateway-contacts/"+c+"/code", ""),
	} {
		if a.status != http.StatusForbidden || codeOf(t, a) != "session_required" {
			t.Errorf("a token = %d %s", a.status, a.body)
		}
	}
	a := e.api.do(http.MethodPost, "/api/v1/users/"+petr.id+"/gateway-contacts", `{"contact":"+1"}`)
	if a.status != http.StatusForbidden || !strings.Contains(string(a.body),
		`"detail":"Only the person sets their contact, from their own web session."`) {
		t.Errorf("an Admin sets a contact = %d %s", a.status, a.body)
	}
	if got := contactsOf(e.api.json(http.MethodGet, "/api/v1/users/"+petr.id+"/gateway-contacts", "",
		http.StatusOK)); strings.Join(got, ",") != first+" true -" {
		t.Errorf("an Admin's list %v", got)
	}

	// Three code requests per hour for one contact; the create was the first.
	for i := range 2 {
		if a := e.newCode(petr.session, c); a.status != http.StatusAccepted {
			t.Fatalf("code %d = %d %s", i+2, a.status, a.body)
		}
		e.lastCode(first)
	}
	if n := requestsExceeded(t, e.newCode(petr.session, c)); n < 1 || n > 3600 {
		t.Errorf("the fourth code: Retry-After %d", n)
	}

	// A code is checked only in the session that asked for it, and expires on the business clock.
	advance(t, r, 3601)
	if a := e.newCode(petr.session2, c); a.status != http.StatusAccepted {
		t.Fatalf("a code for the second session = %d %s", a.status, a.body)
	}
	code = e.lastCode(first)
	if n := codeRefused(t, e.confirm(petr.session, c, code)); n != 4 {
		t.Errorf("the right code from another session: %d left", n)
	}
	if a := e.newCode(petr.session, c); a.status != http.StatusAccepted {
		t.Fatalf("a new code = %d %s", a.status, a.body)
	}
	code = e.lastCode(first)
	advance(t, r, 660)
	if n := codeRefused(t, e.confirm(petr.session, c, code)); n != 0 {
		t.Errorf("an expired code: %d left", n)
	}

	// A new contact replaces the confirmed one only once it is confirmed.
	advance(t, r, 3601)
	b, code := e.add(petr.session, second)
	mine := petr.session.json(http.MethodGet, "/api/v1/me/gateway-contacts", "", http.StatusOK)
	if got := contactsOf(mine); strings.Join(got, ",") != first+" true -,"+second+" false "+c {
		t.Errorf("before the confirmation %v", got)
	}
	petr.session.json(http.MethodPost, "/api/v1/me/gateway-contacts/"+b+"/confirmation", `{"code":"`+code+`"}`,
		http.StatusOK)
	if got := contactsOf(petr.session.json(http.MethodGet, "/api/v1/me/gateway-contacts", "",
		http.StatusOK)); strings.Join(got, ",") != second+" true -" {
		t.Errorf("after the confirmation %v", got)
	}

	// The sixth request of one User within an hour, across two contacts; the replacement above was the first.
	d, _ := e.add(petr.session, third)
	e.newCode(petr.session, d)
	e.lastCode(third)
	f, _ := e.add(petr.session, fourth)
	e.newCode(petr.session, f)
	e.lastCode(fourth)
	if n := requestsExceeded(t, e.newCode(petr.session, f)); n < 1 {
		t.Errorf("the sixth request of the User: %d", n)
	}

	// Five wrong codes void the code.
	advance(t, r, 3601)
	if a := e.newCode(petr.session, f); a.status != http.StatusAccepted {
		t.Fatalf("a new code = %d %s", a.status, a.body)
	}
	code = e.lastCode(fourth)
	wrong = "000000"
	if code == wrong {
		wrong = "111111"
	}
	for want := 4; want >= 0; want-- {
		if n := codeRefused(t, e.confirm(petr.session, f, wrong)); n != want {
			t.Errorf("a wrong code with %d left: %d", want, n)
		}
	}
	if n := codeRefused(t, e.confirm(petr.session, f, code)); n != 0 {
		t.Errorf("the right code after five wrong ones: %d left", n)
	}
	if a := petr.session.do(http.MethodDelete, "/api/v1/me/gateway-contacts/"+f, ""); a.status != http.StatusNoContent {
		t.Errorf("deleteMyGatewayContact = %d %s", a.status, a.body)
	}
	if got := contactsOf(e.api.json(http.MethodGet, "/api/v1/users/"+petr.id+"/gateway-contacts", "",
		http.StatusOK)); strings.Join(got, ",") != second+" true -" {
		t.Errorf("after the removal %v", got)
	}

	// The Request log masks the contacts and the codes of sendNotification.
	records := e.requestLog("connection=" + e.gw + "&operation=sendNotification")
	if len(records) < 10 {
		t.Fatalf("%d records of sendNotification", len(records))
	}
	for _, rec := range records[:3] {
		_, text := e.requestRecord(rec["id"].(string))
		if !strings.Contains(text, `\"contact\":\"[redacted]\"`) {
			t.Errorf("record %s", text)
		}
		for _, s := range e.secrets {
			if regexp.MustCompile(`(^|[^0-9])` + regexp.QuoteMeta(s) + `([^0-9]|$)`).MatchString(text) {
				t.Errorf("the record %s carries %s", rec["id"], s)
			}
		}
	}

	// Deleting the User erases the contacts; the Audit log shows [erased].
	if a := e.api.do(http.MethodDelete, "/api/v1/users/"+petr.id, ""); a.status/100 != 2 {
		t.Fatalf("delete petr = %d %s", a.status, a.body)
	}
	if n := e.h.count(t, `SELECT (SELECT count(*) FROM gateway_contacts) + (SELECT count(*) FROM gateway_contact_codes
		WHERE user_id = (SELECT id FROM users WHERE public_id = '`+petr.id+`'))`); n != 0 {
		t.Errorf("%d contacts and codes left", n)
	}
	var page struct {
		Items []struct {
			Diff []struct {
				Pointer string `json:"pointer"`
				Before  any    `json:"before"`
				After   any    `json:"after"`
			} `json:"diff"`
		} `json:"items"`
	}
	decode(t, e.api.do(http.MethodGet, "/api/v1/audit-log?resource_type=gateway_contact&limit=100", ""), &page)
	erased := 0
	for _, item := range page.Items {
		for _, ch := range item.Diff {
			if ch.Pointer != "/contact" {
				continue
			}
			for _, v := range []any{ch.Before, ch.After} {
				switch v {
				case nil:
				case "[erased]":
					erased++
				default:
					t.Errorf("the Audit log shows %v", v)
				}
			}
		}
	}
	if erased < 8 {
		t.Errorf("%d values [erased] in %d entries", erased, len(page.Items))
	}

	// The log of muster dev carries no contact and no code.
	for _, s := range e.secrets {
		if regexp.MustCompile(`(^|[^0-9])` + regexp.QuoteMeta(s) + `([^0-9]|$)`).MatchString(r.Output()) {
			t.Errorf("%s reached the log of muster dev", s)
		}
	}
}
