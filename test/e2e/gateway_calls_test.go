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

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

// callsPath is the path of sendNotification of the demo gateway, under the fake bridge's prefix of its gateway bot.
const callsPath = fakebridge.GatewayPrefix + "/v1/notifications"

// calls are the notifications of the fake gateway bot that carry action data: the calls, without the codes.
func (e *contactsEnv) calls() []fakebridge.Notification {
	e.t.Helper()
	var out []fakebridge.Notification
	for _, n := range e.notifications() {
		if n.Bot == 3 && n.Action != nil {
			out = append(out, n)
		}
	}
	return out
}

// requestsOf are the requests to sendNotification of the demo gateway with the Idempotency-Key key.
func (e *contactsEnv) requestsOf(key string) []fakeserver.Request {
	e.t.Helper()
	var all, out []fakeserver.Request
	decode(e.t, call(e.t, http.MethodGet, e.fb+"/requests", ""), &all)
	for _, r := range all {
		if r.Path == callsPath && slices.Contains(r.Headers["Idempotency-Key"], key) {
			out = append(out, r)
		}
	}
	return out
}

// queueCall queues, through the hook of muster dev on the replica r, a call of the demo gateway to the User user
// about the Alert Groups groups.
func (e *contactsEnv) queueCall(r *Replica, user string, groups ...string) {
	e.t.Helper()
	ids, _ := json.Marshal(groups)
	if a := call(e.t, http.MethodPost, r.Internal+devmode.GatewayCallsPath, `{"user_id":"`+user+`","connection_id":"`+
		e.gw+`","alert_group_ids":`+string(ids)+`}`); a.status != http.StatusNoContent {
		e.t.Fatalf("queue a call = %d %s", a.status, a.body)
	}
}

// callState is the state, the reason and the attempts of the latest call to the User user.
func (e *contactsEnv) callState(user string) string {
	e.t.Helper()
	return e.h.text(e.t, `SELECT c.state || ' ' || coalesce(c.reason, '-') || ' ' || c.attempts
		FROM gateway_calls c JOIN users u ON u.id = c.user_id WHERE u.public_id = '`+user+`'
		ORDER BY c.id DESC LIMIT 1`)
}

// TestGatewayCalls is the Verification of S-148 against `muster dev` and its demo Notification gateway, with
// E2E_REPLICAS=2 on two replicas (C-30.FR-2, FR-4, FR-5, FR-7, FR-10, AC-3, AC-7, C-11.FR-2): the first call goes at
// once; of the next two within gateway.combine_window, one acknowledged meanwhile, one call about the third goes once
// the window has passed, its action data within 64 bytes; a call answered transient twice is retried with the same
// Idempotency-Key and text until accepted; a call being retried ends dropped once its Alert Groups are acknowledged,
// and one whose Alert Groups were all acknowledged before its first attempt is never sent; a retry the gateway answers
// replayed counts as accepted and calls nobody again; a call refused as invalid ends Not delivered with
// page_not_delivered naming the person and the gateway; critical calls go first; every call reaches the gateway
// once, whichever replica sends it; muster_gateway_calls_total counts them; the log carries no contact and no text.
func TestGatewayCalls(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := &contactsEnv{mm: newMM(t, h, r), fb: h.Fakes.Bridge + "/_fake"}
	e.gw = e.connectionNamed(devmode.GatewayConnectionName)
	const petrContact, olgaContact = "+10000000002", "+10000000003"
	e.secrets = append(e.secrets, petrContact, olgaContact)
	petr, olga := e.person("Petr", "petr"), e.person("Olga", "olga")
	for _, p := range []struct {
		who     contactPerson
		contact string
	}{{petr, petrContact}, {olga, olgaContact}} {
		id, code := e.add(p.who.session, p.contact)
		if a := e.confirm(p.who.session, id, code); a.status != http.StatusOK {
			t.Fatalf("confirm %s = %d %s", p.contact, a.status, a.body)
		}
	}
	group := func(n int, severity string) string {
		t.Helper()
		name := fmt.Sprintf("g%d", n)
		e.group(name, fmt.Sprintf("Disk%d", n))
		e.alert(name, "a", fmt.Sprintf(`{"cluster":"gw%d","severity":%q}`, n, severity), "")
		e.notify(name, "first notification")
		return e.groupOf(fmt.Sprintf("gw%d", n))
	}
	g1, g2, g3 := group(1, "critical"), group(2, "critical"), group(3, "critical")
	number := func(id string) int { return e.number(id) }
	// Any replica may send a call: the queue is shared.
	other := h.Replicas[len(h.Replicas)-1]

	// The first call goes at once.
	e.queueCall(r, petr.id, g1)
	eventually(t, "the first call", func() bool { return len(e.calls()) == 1 })
	first := e.calls()[0]
	if first.Contact != petrContact || first.Language != "en" ||
		first.Text != fmt.Sprintf("Muster: 1 Alert Group needs you. #%d Disk1, critical. Press 1 to acknowledge.",
			number(g1)) || !regexp.MustCompile(`^calls/CA[0-9A-Z]{12}$`).MatchString(first.Key) ||
		len(first.Action.Data) > 64 || first.Action.Label != "Press 1 to acknowledge." {
		t.Fatalf("the first call %+v", first)
	}

	// Two within the window; the second acknowledged meanwhile: one call about the third once the window passed.
	e.queueCall(other, petr.id, g2)
	e.queueCall(r, petr.id, g3)
	e.command(g2, "acknowledge")
	if n := len(e.calls()); n != 1 {
		t.Fatalf("%d calls within the window", n)
	}
	advance(t, r, 61)
	eventually(t, "the call after the window", func() bool { return len(e.calls()) == 2 })
	second := e.calls()[1]
	if second.Text != fmt.Sprintf("Muster: 1 Alert Group needs you. #%d Disk3, critical. Press 1 to acknowledge.",
		number(g3)) || len(second.Action.Data) > 64 || second.Key == first.Key {
		t.Errorf("the second call %+v", second)
	}

	// Answered transient twice: retried with the same key and text until accepted.
	advance(t, r, 61)
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"`+callsPath+`","status":503,"times":2,
		"body":"{\"type\":\"about:blank\",\"title\":\"Busy\",\"status\":503,\"class\":\"transient\"}"}`)
	e.queueCall(r, petr.id, g1)
	eventually(t, "the first attempt", func() bool { return e.callState(petr.id) == "sending - 1" })
	advance(t, r, 2)
	eventually(t, "the second attempt", func() bool { return e.callState(petr.id) == "sending - 2" })
	advance(t, r, 4)
	eventually(t, "the accepted retry", func() bool { return len(e.calls()) == 3 })
	retried := e.calls()[2]
	attempts := e.requestsOf(retried.Key)
	if len(attempts) != 3 || e.callState(petr.id) != "accepted - 3" {
		t.Fatalf("%d attempts of %s, the call %s", len(attempts), retried.Key, e.callState(petr.id))
	}
	for _, a := range attempts {
		if a.Body != attempts[0].Body {
			t.Errorf("an attempt sent %s, the first %s", a.Body, attempts[0].Body)
		}
	}

	// Being retried until all its Alert Groups were acknowledged: dropped.
	advance(t, r, 61)
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"`+callsPath+`","status":503,"times":5,
		"body":"{\"type\":\"about:blank\",\"title\":\"Busy\",\"status\":503,\"class\":\"transient\"}"}`)
	e.queueCall(r, petr.id, g3)
	eventually(t, "the first attempt", func() bool { return e.callState(petr.id) == "sending - 1" })
	e.command(g3, "acknowledge")
	advance(t, r, 2)
	eventually(t, "the dropped call", func() bool { return e.callState(petr.id) == "dropped - 1" })
	e.fake(http.MethodDelete, e.fb+"/faults", "")

	// All acknowledged before the first attempt: never sent.
	advance(t, r, 61)
	sent := len(e.calls())
	e.queueCall(r, petr.id, g2)
	eventually(t, "the call dropped unsent", func() bool { return e.callState(petr.id) == "dropped - 0" })
	if len(e.calls()) != sent {
		t.Errorf("a call about an acknowledged Alert Group was sent")
	}

	// A retry the gateway answers replayed is accepted and calls nobody again.
	advance(t, r, 61)
	g4 := group(4, "critical")
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"`+callsPath+`","status":503,"times":1,
		"body":"{\"type\":\"about:blank\",\"title\":\"Busy\",\"status\":503,\"class\":\"transient\"}"}`)
	e.queueCall(r, petr.id, g4)
	eventually(t, "the failed attempt", func() bool { return e.callState(petr.id) == "sending - 1" })
	var key, body string
	{
		var all []fakeserver.Request
		decode(t, call(t, http.MethodGet, e.fb+"/requests", ""), &all)
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].Path == callsPath {
				key, body = all[i].Headers["Idempotency-Key"][0], all[i].Body
				break
			}
		}
	}
	// The earlier attempt reached the gateway after all: its key is known there.
	if a := call(t, http.MethodPost, h.Fakes.Bridge+callsPath, body, "Authorization",
		"Bearer "+fakebridge.GatewayToken, "Idempotency-Key", key, "Content-Type", "application/json"); a.status !=
		http.StatusCreated {
		t.Fatalf("the earlier attempt = %d %s", a.status, a.body)
	}
	before := len(e.calls())
	advance(t, r, 2)
	eventually(t, "the replayed retry", func() bool { return e.callState(petr.id) == "accepted - 2" })
	if len(e.calls()) != before || len(e.requestsOf(key)) != 3 {
		t.Errorf("the replayed retry called again: %d calls, %d requests", len(e.calls()), len(e.requestsOf(key)))
	}

	// Refused as invalid: Not delivered, page_not_delivered.
	advance(t, r, 61)
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"`+callsPath+`","status":400,"times":1,
		"body":"{\"type\":\"about:blank\",\"title\":\"Bad number\",\"status\":400,\"class\":\"invalid\"}"}`)
	e.queueCall(r, petr.id, g1)
	eventually(t, "the refused call", func() bool { return e.callState(petr.id) == "not_delivered call_refused 1" })
	var page struct {
		Items []struct {
			Kind      string `json:"kind"`
			Event     string `json:"event"`
			Loudness  string `json:"loudness"`
			PagedUser []struct {
				Login string `json:"login"`
			} `json:"paged_users"`
			Gateway *struct {
				Name string `json:"name"`
			} `json:"gateway"`
			Reason string `json:"reason"`
		} `json:"items"`
	}
	decode(t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+g1+"/timeline?kind=escalation", ""), &page)
	if len(page.Items) != 1 || page.Items[0].Event != "page_not_delivered" || page.Items[0].Kind != "escalation" ||
		page.Items[0].Loudness != "quiet" || len(page.Items[0].PagedUser) != 1 ||
		page.Items[0].PagedUser[0].Login != "petr" || page.Items[0].Gateway == nil ||
		page.Items[0].Gateway.Name != devmode.GatewayConnectionName || page.Items[0].Reason != "call_refused" {
		t.Errorf("the escalation timeline %+v", page.Items)
	}

	// Critical first: both are due at once, the warning one earlier.
	advance(t, r, 61)
	g5, g6 := group(5, "warning"), group(6, "critical")
	e.queueCall(r, olga.id, g5)
	eventually(t, "olga's first call", func() bool { return strings.HasPrefix(e.callState(olga.id), "accepted") })
	advance(t, r, 1)
	e.queueCall(r, petr.id, g6)
	eventually(t, "petr's call", func() bool { return strings.HasPrefix(e.callState(petr.id), "accepted") })
	e.queueCall(r, olga.id, g5)
	e.queueCall(r, petr.id, g6)
	sent = len(e.calls())
	advance(t, r, 70)
	eventually(t, "the two calls", func() bool { return len(e.calls()) == sent+2 })
	if len(h.Replicas) == 1 {
		if got := e.calls()[sent:]; got[0].Contact != petrContact || got[1].Contact != olgaContact {
			t.Errorf("sent to %s before %s", got[0].Contact, got[1].Contact)
		}
	}

	// Every call reached the gateway once.
	seen := map[string]int{}
	for _, c := range e.calls() {
		seen[c.Key]++
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s reached the gateway %d times", k, n)
		}
	}
	metrics := ""
	for _, rep := range h.Replicas {
		metrics += string(call(t, http.MethodGet, rep.Internal+"/metrics", "").body)
	}
	if strings.Count(metrics, "\nmuster_gateway_calls_total{") < 2 ||
		!strings.Contains(metrics, `muster_gateway_calls_total{connection="`+e.gw+`",outcome="accepted"}`) {
		t.Errorf("muster_gateway_calls_total:\n%s", metrics)
	}
	for _, rep := range h.Replicas {
		out := rep.Output()
		for _, s := range append(slices.Clone(e.secrets), "Alert Group needs you", "Press 1") {
			if regexp.MustCompile(`(^|[^0-9])` + regexp.QuoteMeta(s) + `([^0-9]|$)`).MatchString(out) {
				t.Errorf("%s reached the log of muster dev", s)
			}
		}
	}
}
