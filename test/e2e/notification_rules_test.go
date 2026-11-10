// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// rulesOf is the state and the important and normal rule of a read of Notification rules, each action as its kind,
// a wait by its seconds and an unavailable call with its reason: "call mention 300 mention / mention".
func rulesOf(t *testing.T, a answer) string {
	t.Helper()
	if a.status != http.StatusOK {
		return strconv.Itoa(a.status) + " " + string(a.body)
	}
	var r struct {
		State     string           `json:"state"`
		Normal    []map[string]any `json:"normal"`
		Important []map[string]any `json:"important"`
	}
	decode(t, a, &r)
	words := func(rule []map[string]any) string {
		var out []string
		for _, x := range rule {
			switch {
			case x["kind"] == "wait":
				out = append(out, strconv.Itoa(int(x["wait_seconds"].(float64))))
			case x["available"] == false:
				out = append(out, x["kind"].(string)+":"+x["unavailable_reason"].(string))
			default:
				out = append(out, x["kind"].(string))
			}
		}
		return strings.Join(out, " ")
	}
	return r.State + ": " + words(r.Important) + " / " + words(r.Normal)
}

// rulesETag is the ETag of a read of Notification rules.
func rulesETag(t *testing.T, a answer) string {
	t.Helper()
	if a.status != http.StatusOK || a.header.Get("ETag") == "" {
		t.Fatalf("read = %d %s", a.status, a.body)
	}
	return a.header.Get("ETag")
}

// putRules changes the rules at path from the session s with the ETag of a read, or as an Admin with api when s is
// nil.
func (e *contactsEnv) putRules(s *agent, path, body string) answer {
	e.t.Helper()
	if s == nil {
		tag := rulesETag(e.t, e.api.do(http.MethodGet, path, ""))
		return e.api.do(http.MethodPut, path, body, "If-Match", tag)
	}
	tag := rulesETag(e.t, s.do(http.MethodGet, path, ""))
	req, err := http.NewRequestWithContext(e.t.Context(), http.MethodPut, s.base+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	return s.send(req, tag)
}

// send sends req from the session with If-Match.
func (a *agent) send(req *http.Request, ifMatch string) answer {
	a.t.Helper()
	for name, value := range a.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value}) //nolint:gosec // G124: a request cookie carries no attributes
	}
	req.Header.Set("X-CSRF-Token", a.csrf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", ifMatch)
	resp, err := noFollow.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{status: resp.StatusCode, header: resp.Header, body: b}
}

// oddETag reports whether an ETag is that of the state organization: an odd number in quotes.
func oddETag(tag string) bool {
	n, err := strconv.Atoi(strings.Trim(tag, `"`))
	return err == nil && n%2 == 1
}

// organizationRules sets the Organization's important rule.
func (e *contactsEnv) organizationRules(important string) {
	e.t.Helper()
	a := putOrganization(e.api, func(m map[string]any) {
		var rule []any
		if err := json.Unmarshal([]byte(important), &rule); err != nil {
			e.t.Fatal(err)
		}
		m["notification_rules"].(map[string]any)["important"] = rule
	})
	if a.status != http.StatusOK {
		e.t.Fatalf("the Organization's rules = %d %s", a.status, a.body)
	}
}

// confirmAt confirms contact for p at the gateway gw, whose notifications go to the fake bridge's bot.
func (e *contactsEnv) confirmAt(p contactPerson, gw, contact string, bot int) string {
	e.t.Helper()
	c := p.session.json(http.MethodPost, "/api/v1/me/gateway-contacts", `{"connection_id":"`+gw+`","contact":"`+
		contact+`"}`, http.StatusAccepted)
	all := e.notifications()
	n := all[len(all)-1]
	m := contactCode.FindStringSubmatch(n.Text)
	if n.Contact != contact || n.Bot != bot || m == nil {
		e.t.Fatalf("the notification with the code %+v", n)
	}
	p.session.json(http.MethodPost, "/api/v1/me/gateway-contacts/"+c["id"].(string)+"/confirmation",
		`{"code":"`+m[1]+`"}`, http.StatusOK)
	return c["id"].(string)
}

// TestNotificationRules is the Verification of S-152 against `muster dev` and the demo Notification gateway of its
// fake bridge (C-31.FR-1, FR-2, FR-4, FR-5, C-31.AC-3, AC-4, AC-6, C-20.FR-1): a User reads the state organization with
// the Organization's default rules; "Call" makes the important rule a call, a Mention, a wait of 5 minutes and a
// Mention, and a Personal access token cannot read or change the own rules; a change of the Organization's important
// rule reaches ivan in organization at once and leaves petr in call as he chose it; an Admin sets three Mentions,
// custom, with the Audit log entry and its diff, and the limits hold; a stale If-Match is 412; reset brings back
// organization; call without a contact is contact_required; no_call on an Organization whose important rule calls
// removes the call, and call on it adds no second call through that gateway; with two gateways at which petr has
// confirmed contacts gateway_id is required and named; a contact removed after call leaves the call unavailable; with
// no gateway left, call is gateway_required; deleting the User deletes the rules.
func TestNotificationRules(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := &contactsEnv{mm: newMM(t, h, r), fb: h.Fakes.Bridge + "/_fake"}
	t.Cleanup(func() {
		restore(t, http.MethodPut, e.fb+"/config", `{"token":"`+fakebridge.SecondToken+`","roles":null}`)
	})
	e.gw = e.connectionNamed(devmode.GatewayConnectionName)
	petr, ivan := e.person("Petr", "petr"), e.person("Ivan", "ivan")
	e.confirmAt(petr, e.gw, "+10000000002", 3)
	const mine = "/api/v1/me/notification-rules"
	of := func(p contactPerson) string { return "/api/v1/users/" + p.id + "/notification-rules" }

	if got := rulesOf(t, petr.session.do(http.MethodGet, mine, "")); got != "organization: mention 300 mention / mention" {
		t.Fatalf("petr at first: %s", got)
	}

	// "Call": a call at the same moment as the first Mention; a token cannot.
	a := e.putRules(petr.session, mine, `{"state":"call","gateway_id":"`+e.gw+`"}`)
	if got := rulesOf(t, a); got != "call: call mention 300 mention / mention" {
		t.Errorf("call: %s", got)
	}
	if g := a.body; !strings.Contains(string(g), `"gateway":{"id":"`+e.gw+`","name":"Example Gateway"}`) {
		t.Errorf("the gateway of call: %s", g)
	}
	for _, x := range []answer{
		petr.pat.do(http.MethodGet, mine, ""),
		petr.pat.do(http.MethodPut, mine, `{"state":"no_call"}`, "If-Match", "*"),
		petr.pat.do(http.MethodPost, mine+"/reset", ""),
	} {
		if x.status != http.StatusForbidden || codeOf(t, x) != "session_required" {
			t.Errorf("a token = %d %s", x.status, x.body)
		}
	}
	if got := fieldError(t, e.putRules(ivan.session, mine, `{"state":"call"}`)); got != "contact_required /gateway_id" {
		t.Errorf("call without a contact: %s", got)
	}

	// The Organization changes its important rule: ivan follows, petr stays.
	e.organizationRules(`[{"kind":"mention"},{"kind":"wait","wait_seconds":600},{"kind":"mention"}]`)
	if got := rulesOf(t, e.api.do(http.MethodGet, of(ivan), "")); got != "organization: mention 600 mention / mention" {
		t.Errorf("ivan after the change: %s", got)
	}
	if got := rulesOf(t, e.api.do(http.MethodGet, of(petr), "")); got != "call: call mention 300 mention / mention" {
		t.Errorf("petr after the change: %s", got)
	}

	// An Admin sets three Mentions; the Audit log has the diff; a Responder cannot; the limits hold.
	if got := rulesOf(t, e.putRules(nil, of(petr), `{"state":"custom","normal":[{"kind":"mention"},{"kind":"mention"},`+
		`{"kind":"mention"}],"important":[{"kind":"mention"}]}`)); got != "custom: mention / mention mention mention" {
		t.Errorf("custom: %s", got)
	}
	var log struct {
		Items []struct {
			Actor      map[string]any   `json:"actor"`
			ResourceID string           `json:"resource_id"`
			Diff       []map[string]any `json:"diff"`
		} `json:"items"`
	}
	decode(t, e.api.do(http.MethodGet, "/api/v1/audit-log?action=notification_rules.updated", ""), &log)
	if len(log.Items) != 2 || log.Items[0].ResourceID != petr.id || log.Items[0].Actor["kind"] != "user" ||
		len(log.Items[0].Diff) != 4 || log.Items[0].Diff[0]["pointer"] != "/state" ||
		log.Items[0].Diff[0]["before"] != "call" || log.Items[0].Diff[0]["after"] != "custom" {
		t.Errorf("the Audit log: %+v", log.Items)
	}
	if a := ivan.session.do(http.MethodPut, of(petr), `{"state":"no_call"}`); a.status != http.StatusForbidden {
		t.Errorf("a Responder's update = %d %s", a.status, a.body)
	}
	eleven := strings.Repeat(`{"kind":"mention"},`, 10) + `{"kind":"mention"}`
	for body, want := range map[string]string{
		`{"state":"custom","normal":[{"kind":"mention"},{"kind":"wait","wait_seconds":90}],"important":[{"kind":"mention"}]}`:   "out_of_range /normal/1/wait_seconds",
		`{"state":"custom","normal":[{"kind":"mention"},{"kind":"wait","wait_seconds":0}],"important":[{"kind":"mention"}]}`:    "out_of_range /normal/1/wait_seconds",
		`{"state":"custom","normal":[{"kind":"mention"},{"kind":"wait","wait_seconds":3660}],"important":[{"kind":"mention"}]}`: "out_of_range /normal/1/wait_seconds",
		`{"state":"custom","normal":[` + eleven + `],"important":[{"kind":"mention"}]}`:                                         "out_of_range /normal",
		`{"state":"custom","normal":[{"kind":"mention"}],"important":[{"kind":"wait","wait_seconds":60}]}`:                      "required /important",
	} {
		if got := fieldError(t, e.putRules(nil, of(petr), body)); got != want {
			t.Errorf("%s: %s, want %s", body, got, want)
		}
	}
	if a := e.api.do(http.MethodPut, of(petr), `{"state":"no_call"}`, "If-Match", `"1"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("a stale If-Match = %d %s", a.status, a.body)
	}
	a = petr.session.do(http.MethodPost, mine+"/reset", "")
	resetTag := a.header.Get("ETag")
	if got := rulesOf(t, a); got != "organization: mention 600 mention / mention" || !oddETag(resetTag) {
		t.Errorf("reset: %s %s", got, resetTag)
	}

	// An Organization whose important rule calls: no_call removes the call, call adds no second one. The ETag of
	// petr, who follows the Organization, changes with it, and the one read before is stale.
	e.organizationRules(`[{"kind":"mention"},{"kind":"call","gateway_id":"` + e.gw + `"},{"kind":"wait",` +
		`"wait_seconds":300},{"kind":"mention"}]`)
	if tag := rulesETag(t, petr.session.do(http.MethodGet, mine, "")); tag == resetTag || !oddETag(tag) {
		t.Errorf("the ETag after the Organization's change: %s, before %s", tag, resetTag)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, petr.session.base+mine,
		strings.NewReader(`{"state":"no_call"}`))
	if a := petr.session.send(req, resetTag); a.status != http.StatusPreconditionFailed {
		t.Errorf("an If-Match from before the Organization's change = %d %s", a.status, a.body)
	}
	// ivan, in organization, adding a call through the gateway the Organization's rule names: he has no contact.
	if got := fieldError(t, e.putRules(ivan.session, mine, `{"state":"custom","normal":[{"kind":"call",`+
		`"gateway_id":"`+e.gw+`"}],"important":[{"kind":"mention"}]}`)); got != "contact_required /normal/0" {
		t.Errorf("a call of the Organization's gateway without a contact: %s", got)
	}
	if got := rulesOf(t, ivan.session.do(http.MethodGet, mine, "")); got !=
		"organization: mention call:no_confirmed_contact 300 mention / mention" {
		t.Errorf("ivan on an Organization that calls: %s", got)
	}
	if got := rulesOf(t, e.putRules(ivan.session, mine, `{"state":"no_call"}`)); got != "no_call: mention 300 mention / mention" {
		t.Errorf("no_call on an Organization that calls: %s", got)
	}
	if got := rulesOf(t, e.putRules(petr.session, mine, `{"state":"call"}`)); got != "call: mention call 300 mention / mention" {
		t.Errorf("call on an Organization that calls through the gateway: %s", got)
	}

	// Two gateways with petr's confirmed contacts: gateway_id is required, then named.
	e.fake(http.MethodPut, e.fb+"/config", `{"token":"`+fakebridge.SecondToken+`","roles":["gateway"]}`)
	second := e.api.json(http.MethodPost, "/api/v1/connections", `{"type":"bridge","name":"Second Gateway",`+
		`"base_url":"`+h.Fakes.Bridge+`","event_delivery":"poll","bot_token":"`+fakebridge.SecondToken+`",`+
		`"proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	if res := e.api.json(http.MethodPost, "/api/v1/connections/"+second+"/checks", "", http.StatusOK); res["ok"] != true {
		t.Fatalf("the check of the second gateway = %v", res)
	}
	contact := e.confirmAt(petr, second, "+10000000003", 2)
	if got := fieldError(t, e.putRules(petr.session, mine, `{"state":"call"}`)); got != "required /gateway_id" {
		t.Errorf("call with two gateways: %s", got)
	}
	a = e.putRules(petr.session, mine, `{"state":"call","gateway_id":"`+second+`"}`)
	if got := rulesOf(t, a); got != "call: call mention call 300 mention / mention" ||
		!strings.Contains(string(a.body), `"name":"Second Gateway"`) {
		t.Errorf("call through the second gateway: %s", got)
	}

	// The contact removed after call: the call stays, unavailable.
	petr.session.json(http.MethodDelete, "/api/v1/me/gateway-contacts/"+contact, "", http.StatusNoContent)
	if got := rulesOf(t, petr.session.do(http.MethodGet, mine, "")); got !=
		"call: call:no_confirmed_contact mention call 300 mention / mention" {
		t.Errorf("without the contact: %s", got)
	}

	// No gateway left: call is gateway_required; the calls read gateway_deleted.
	for _, gw := range []string{second, e.gw} {
		e.api.json(http.MethodDelete, "/api/v1/connections/"+gw, "", http.StatusNoContent)
	}
	if got := fieldError(t, e.putRules(ivan.session, mine, `{"state":"call"}`)); got != "gateway_required /gateway_id" {
		t.Errorf("call without a gateway: %s", got)
	}
	if got := fieldError(t, e.putRules(ivan.session, mine, `{"state":"custom","normal":[{"kind":"call"}],`+
		`"important":[{"kind":"mention"}]}`)); got != "gateway_required /normal/0" {
		t.Errorf("a custom call without a gateway: %s", got)
	}
	if got := rulesOf(t, petr.session.do(http.MethodGet, mine, "")); got !=
		"call: call:gateway_deleted mention call:gateway_deleted 300 mention / mention" {
		t.Errorf("the gateways deleted: %s", got)
	}

	// Deleting the User deletes the rules.
	rows := `SELECT count(*) FROM notification_rules WHERE user_id = (SELECT id FROM users WHERE public_id = '` +
		petr.id + `')`
	if n := e.h.count(t, rows); n != 1 {
		t.Errorf("%d rows of petr before the deletion", n)
	}
	if a := e.api.do(http.MethodDelete, "/api/v1/users/"+petr.id, ""); a.status/100 != 2 {
		t.Fatalf("delete petr = %d %s", a.status, a.body)
	}
	if n := e.h.count(t, rows); n != 0 {
		t.Errorf("%d rows of petr after the deletion", n)
	}
	if a := e.api.do(http.MethodGet, of(petr), ""); a.status != http.StatusNotFound {
		t.Errorf("the rules of a deleted User = %d", a.status)
	}
}
