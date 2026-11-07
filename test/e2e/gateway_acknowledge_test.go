// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// acknowledge acknowledges, through the fake gateway, as its person would, with the members of extra, a JSON object
// without its braces — data, bot or event_id — and returns the id of the event it queued.
func (e *contactsEnv) acknowledge(notification, extra string) string {
	e.t.Helper()
	body := `{"notification_id":"` + notification + `"`
	if extra != "" {
		body += "," + extra
	}
	var queued fakebridge.Event
	decode(e.t, call(e.t, http.MethodPost, e.fb+"/acknowledge", body+"}"), &queued)
	var ev struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(queued.Event, &ev); err != nil || ev.ID == "" {
		e.t.Fatalf("acknowledge %s = %+v", body, queued)
	}
	return ev.ID
}

// answersTo are the texts Muster answered the event id with.
func (e *contactsEnv) answersTo(id string) []string {
	e.t.Helper()
	var all []fakebridge.Answer
	decode(e.t, call(e.t, http.MethodGet, e.fb+"/answers", ""), &all)
	var out []string
	for _, a := range all {
		if a.EventID == id {
			out = append(out, a.Text)
		}
	}
	return out
}

// answerTo waits for the answer to the event id and returns it.
func (e *contactsEnv) answerTo(id string) string {
	e.t.Helper()
	eventually(e.t, "the answer to "+id, func() bool { return len(e.answersTo(id)) > 0 })
	return e.answersTo(id)[0]
}

// sentCall queues a call of the demo gateway to the User user about groups and waits until the gateway got it.
func (e *contactsEnv) sentCall(r *Replica, user string, groups ...string) fakebridge.Notification {
	e.t.Helper()
	before := len(e.calls())
	e.queueCall(r, user, groups...)
	eventually(e.t, "the call to "+user, func() bool { return len(e.calls()) > before })
	return e.calls()[len(e.calls())-1]
}

// logged are the lines of event on every replica whose fields include want.
func (e *contactsEnv) logged(event string, want map[string]string) int {
	n := 0
	for _, r := range e.h.Replicas {
	lines:
		for _, line := range logLines(r, event) {
			for k, v := range want {
				if fmt.Sprint(line[k]) != v {
					continue lines
				}
			}
			n++
		}
	}
	return n
}

// TestGatewayAcknowledge is the Verification of S-173 against `muster dev` and its demo Notification gateway, with
// E2E_REPLICAS=2 on two replicas (C-30.FR-6, AC-4, FR-10, C-03.FR-14, C-10.FR-1, FR-3, C-12.FR-3, C-22.FR-11): an
// acknowledgement in a call acknowledges an Alert Group nobody owns as the person the call was for, with the Transport
// gateway in the Timeline and the Audit log, answered "Acknowledged: #N"; the same event again changes nothing and is
// answered once; an Alert Group another User took stays theirs, refused and answered "Already taken by Ivan"; a
// resolved one is answered "Already resolved"; a call about two Alert Groups acknowledges both; a Viewer and a
// disabled person change nothing; tampered data, and the data of a call reported by another Connection, are refused
// with gateway_acknowledge_refused; muster_commands_total counts the Transport gateway.
func TestGatewayAcknowledge(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	other := h.Replicas[len(h.Replicas)-1]
	e := &contactsEnv{mm: newMM(t, h, r), fb: h.Fakes.Bridge + "/_fake"}
	e.gw = e.connectionNamed(devmode.GatewayConnectionName)
	bridgeConn := e.connectionNamed(devmode.BridgeConnectionName)
	people := map[string]contactPerson{}
	for i, login := range []string{"petr", "ivan", "vera", "dora"} {
		p := e.person(strings.ToUpper(login[:1])+login[1:], login)
		contact := fmt.Sprintf("+1000000010%d", i)
		e.secrets = append(e.secrets, contact)
		id, code := e.add(p.session, contact)
		if a := e.confirm(p.session, id, code); a.status != http.StatusOK {
			t.Fatalf("confirm %s = %d %s", contact, a.status, a.body)
		}
		people[login] = p
	}
	petr, ivan := people["petr"], people["ivan"]
	group := func(n int) string {
		t.Helper()
		name := fmt.Sprintf("ack%d", n)
		e.group(name, fmt.Sprintf("Ack%d", n))
		e.alert(name, "a", fmt.Sprintf(`{"cluster":"ack%d","severity":"critical"}`, n), "")
		e.notify(name, "first notification")
		return e.groupOf(fmt.Sprintf("ack%d", n))
	}
	g1, g2, g3, g4, g5, g6, g7 := group(1), group(2), group(3), group(4), group(5), group(6), group(7)
	owner := func(g string) string {
		t.Helper()
		o, _ := e.alertGroup(g)["owner"].(map[string]any)
		return fmt.Sprint(e.alertGroup(g)["status"]) + " " + fmt.Sprint(o["login"])
	}

	// An Alert Group nobody owns: acknowledged as petr, with the Transport gateway.
	first := e.sentCall(r, petr.id, g1)
	ev1 := e.acknowledge(first.ID, "")
	if got := e.answerTo(ev1); got != fmt.Sprintf("Acknowledged: #%d", e.number(g1)) {
		t.Errorf("the answer %q", got)
	}
	if got := owner(g1); got != "acknowledged petr" {
		t.Errorf("g1 %s", got)
	}
	var page struct {
		Items []struct {
			timelineEntry
			Gateway *struct {
				Name string `json:"name"`
			} `json:"gateway"`
		} `json:"items"`
	}
	decode(t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g1+"/timeline?kind=status", ""), &page)
	acked := ""
	for _, x := range page.Items {
		if x.Event == "acknowledged" && x.Gateway != nil {
			acked = x.Actor.Transport + " " + x.Actor.Name + " " + x.Gateway.Name
		}
	}
	if acked != "gateway Petr "+devmode.GatewayConnectionName {
		t.Errorf("the timeline entry %q", acked)
	}
	if got := h.text(t, `SELECT a.transport || ' ' || u.login FROM audit_log a JOIN users u ON u.id = a.actor_user_id
		WHERE a.action = 'alert_group.acknowledged' AND a.resource_public_id = '`+g1+`'`); got != "gateway petr" {
		t.Errorf("the audit log entry %q", got)
	}
	// The same event again, under its id: nothing changes, and the next answer comes after it was handled.
	e.acknowledge("", `"event_id":"`+ev1+`"`)

	// An Alert Group ivan took after the call went: it stays his.
	advance(t, r, 61)
	second := e.sentCall(other, petr.id, g2)
	ivan.session.json(http.MethodPost, "/api/v1/alert-groups/"+g2+"/acknowledge", "", http.StatusOK)
	ev2 := e.acknowledge(second.ID, "")
	if got := e.answerTo(ev2); got != "Already taken by Ivan" {
		t.Errorf("the answer %q", got)
	}
	if got := owner(g2); got != "acknowledged ivan" {
		t.Errorf("g2 %s", got)
	}
	if n := e.logged("command_refused", map[string]string{"group": g2, "transport": "gateway",
		"code": "owned_by_other", "actor": petr.id}); n != 1 {
		t.Errorf("%d refusals logged", n)
	}
	if got := e.answersTo(ev1); len(got) != 1 || owner(g1) != "acknowledged petr" ||
		h.count(t, `SELECT count(*) FROM bridge_events_seen WHERE event_id = '`+ev1+`'`) != 1 {
		t.Errorf("the replayed event: answers %v, g1 %s", got, owner(g1))
	}

	// A resolved one.
	advance(t, r, 61)
	third := e.sentCall(r, petr.id, g3)
	e.command(g3, "resolve")
	if got := e.answerTo(e.acknowledge(third.ID, "")); got != "Already resolved" {
		t.Errorf("the answer %q", got)
	}

	// A call about two Alert Groups acknowledges both.
	advance(t, r, 61)
	both := e.sentCall(r, petr.id, g4, g5)
	n4, n5 := e.number(g4), e.number(g5)
	want := fmt.Sprintf("Acknowledged: #%d, #%d", min(n4, n5), max(n4, n5))
	if got := e.answerTo(e.acknowledge(both.ID, "")); got != want {
		t.Errorf("the answer %q, want %q", got, want)
	}
	if owner(g4) != "acknowledged petr" || owner(g5) != "acknowledged petr" {
		t.Errorf("g4 %s, g5 %s", owner(g4), owner(g5))
	}

	// A Viewer changes nothing.
	vera := people["vera"]
	user := e.api.do(http.MethodGet, "/api/v1/users/"+vera.id, "")
	e.api.json(http.MethodPut, "/api/v1/users/"+vera.id, `{"name":"Vera","role":"viewer"}`, http.StatusOK,
		"If-Match", user.header.Get("ETag"))
	veras := e.sentCall(r, vera.id, g6)
	if got := e.answerTo(e.acknowledge(veras.ID, "")); got !=
		"You are not permitted to acknowledge Alert Groups; nothing was changed" {
		t.Errorf("the viewer's answer %q", got)
	}
	if got := owner(g6); got != "firing <nil>" {
		t.Errorf("g6 %s", got)
	}

	// A person disabled after the call went changes nothing.
	dora := people["dora"]
	doras := e.sentCall(r, dora.id, g7)
	e.api.json(http.MethodPost, "/api/v1/users/"+dora.id+"/disable", "", http.StatusOK)
	if got := e.answerTo(e.acknowledge(doras.ID, "")); got !=
		"Your Muster account is disabled; nothing was changed" {
		t.Errorf("the disabled person's answer %q", got)
	}
	if got := owner(g7); got != "firing <nil>" {
		t.Errorf("g7 %s", got)
	}
	if n := e.logged("gateway_acknowledge_refused", map[string]string{"connection": e.gw, "reason": "disabled"}); n != 1 {
		t.Errorf("%d disabled refusals logged", n)
	}

	// Tampered data, and the data of a call of this gateway reported by another Connection: refused.
	// A character in the middle, of the call's public_id: every bit of it is signed.
	data := []byte(veras.Action.Data)
	data[10] = map[bool]byte{true: 'A', false: 'B'}[data[10] != 'A']
	if got := e.answerTo(e.acknowledge(veras.ID, `"data":"`+string(data)+`"`)); got !=
		"This call could not be verified; nothing was changed" {
		t.Errorf("the tampered answer %q", got)
	}
	if got := e.answerTo(e.acknowledge(first.ID, `"bot":1`)); got !=
		"This call could not be verified; nothing was changed" {
		t.Errorf("the other connection's answer %q", got)
	}
	if n := e.logged("gateway_acknowledge_refused", map[string]string{"connection": e.gw, "call": "",
		"reason": "not_verified"}); n != 1 {
		t.Errorf("%d tampered refusals logged", n)
	}
	if n := e.logged("gateway_acknowledge_refused", map[string]string{"connection": bridgeConn,
		"reason": "unknown_call"}); n != 1 {
		t.Errorf("%d refusals of another connection logged", n)
	}
	if got := owner(g6); got != "firing <nil>" {
		t.Errorf("g6 after the refusals %s", got)
	}

	// muster_commands_total counts the Transport gateway.
	var done, refused float64
	for _, rep := range h.Replicas {
		for line := range strings.Lines(string(call(t, http.MethodGet, rep.Internal+"/metrics", "").body)) {
			var v float64
			switch {
			case strings.HasPrefix(line, `muster_commands_total{command="acknowledge",transport="gateway",outcome="done"}`):
				_, _ = fmt.Sscanf(line[strings.LastIndexByte(line, ' ')+1:], "%g", &v)
				done += v
			case strings.HasPrefix(line,
				`muster_commands_total{command="acknowledge",transport="gateway",outcome="refused"}`):
				_, _ = fmt.Sscanf(line[strings.LastIndexByte(line, ' ')+1:], "%g", &v)
				refused += v
			}
		}
	}
	if done != 3 || refused != 3 {
		t.Errorf("muster_commands_total{transport=\"gateway\"}: done %g, refused %g", done, refused)
	}
}
