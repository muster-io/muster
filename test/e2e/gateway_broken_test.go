// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakebridge"
)

// gatewayInfoPath is the path of getInfo of the demo gateway, under the fake bridge's prefix of its gateway bot.
const gatewayInfoPath = fakebridge.GatewayPrefix + "/v1/info"

// gatewayState is the gateway state of the Connection id as its read gives it.
type gatewayState struct {
	Broken      bool    `json:"broken"`
	BrokenSince *string `json:"broken_since"`
	Reason      *string `json:"reason"`
	NextProbeAt *string `json:"next_probe_at"`
}

// gatewayOf is the gateway of the read of the Connection id; nil for null.
func (e *contactsEnv) gatewayOf(id string) *gatewayState {
	e.t.Helper()
	var c struct {
		Gateway *gatewayState `json:"gateway"`
	}
	a := e.api.do(http.MethodGet, "/api/v1/connections/"+id, "")
	if a.status != http.StatusOK || !strings.Contains(string(a.body), `"gateway":`) {
		e.t.Fatalf("the Connection %s = %d %s", id, a.status, a.body)
	}
	decode(e.t, a, &c)
	return c.Gateway
}

// broken reports whether the Connection id is a Broken Notification gateway.
func (e *contactsEnv) broken(id string) bool {
	e.t.Helper()
	g := e.gatewayOf(id)
	return g != nil && g.Broken
}

// queueCallOf queues, through the hook of muster dev on the replica r, a call through the gateway gw to the User user
// about the Alert Group group.
func (e *contactsEnv) queueCallOf(r *Replica, gw, user, group string) {
	e.t.Helper()
	if a := call(e.t, http.MethodPost, r.Internal+devmode.GatewayCallsPath, `{"user_id":"`+user+`","connection_id":"`+
		gw+`","alert_group_ids":["`+group+`"]}`); a.status != http.StatusNoContent {
		e.t.Fatalf("queue a call = %d %s", a.status, a.body)
	}
}

// gauge is muster_notification_gateway_broken of the Connection id on the replica that exports it, the Leader.
func (e *contactsEnv) gauge(id string) string {
	e.t.Helper()
	for _, r := range e.h.Replicas {
		if v := r.Metric(e.t, `muster_notification_gateway_broken{connection="`+id+`"}`); v != "" {
			return v
		}
	}
	return ""
}

// gaugeIs waits until the Leader's gauge of the Connection id is want, moving the development clock by a second at
// each look, which runs the Leader task gateway_broken_gauge.
func (e *contactsEnv) gaugeIs(r *Replica, id, want string) {
	e.t.Helper()
	eventually(e.t, "muster_notification_gateway_broken "+want, func() bool {
		if e.gauge(id) == want {
			return true
		}
		advance(e.t, r, 1)
		return false
	})
}

// alertGroupOf waits for the Alert whose cluster label is cluster and returns its Alert Group: the Internal alerts
// processed meanwhile may count as the Snapshot that notify waits for, so the Alert may come a moment later.
func (e *contactsEnv) alertGroupOf(cluster string) string {
	e.t.Helper()
	var id string
	eventually(e.t, "the Alert of "+cluster, func() bool {
		var page struct {
			Items []struct {
				Group struct {
					ID string `json:"id"`
				} `json:"alert_group"`
			} `json:"items"`
		}
		decode(e.t, e.api.do(http.MethodGet, "/api/v1/integrations/"+e.integration+"/alerts?"+
			url.Values{"label": {`cluster="` + cluster + `"`}}.Encode(), ""), &page)
		if len(page.Items) > 0 {
			id = page.Items[0].Group.ID
		}
		return id != ""
	})
	return id
}

// escalationReasons are the reasons of the page_not_delivered entries of the Alert Group id, newest first.
func (e *contactsEnv) escalationReasons(id string) []string {
	e.t.Helper()
	var page struct {
		Items []struct {
			Event  string `json:"event"`
			Reason string `json:"reason"`
		} `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+id+"/timeline?kind=escalation", ""), &page)
	var out []string
	for _, i := range page.Items {
		if i.Event == "page_not_delivered" {
			out = append(out, i.Reason)
		}
	}
	return out
}

// TestGatewayBroken is the Verification of S-149 against `muster dev` and its demo Notification gateway, with
// E2E_REPLICAS=2 on two replicas (C-30.FR-8, AC-5): a call the fake answers 401 makes the gateway Broken, which the
// Connection's read shows with the reason, MusterNotificationGatewayBroken fires about it and the Leader's gauge is 1;
// a probe through info that fails keeps it Broken; a call that comes due meanwhile is not sent and records
// page_not_delivered with gateway_broken; once the faults are gone the next probe ends the state, the alert resolves,
// the gauge is 0, gateway_recovered is logged and nothing is sent afterwards; a call that used up its retry budget makes
// it Broken again, and a Connection check ends that; a bridge of both roles whose gateway is Broken keeps delivering
// to its Destination, and a Broken Destination of it leaves its gateway in order and calling.
func TestGatewayBroken(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	other := h.Replicas[len(h.Replicas)-1]
	e := &contactsEnv{mm: newMM(t, h, r), fb: h.Fakes.Bridge + "/_fake"}
	t.Cleanup(func() {
		restore(t, http.MethodDelete, e.fb+"/faults", "")
		restore(t, http.MethodPut, e.fb+"/config", `{"token":"`+fakebridge.SecondToken+`","roles":null}`)
	})
	e.gw = e.connectionNamed(devmode.GatewayConnectionName)
	const petrContact = "+10000000002"
	e.secrets = append(e.secrets, petrContact)
	petr := e.person("Petr", "petr")
	id, code := e.add(petr.session, petrContact)
	if a := e.confirm(petr.session, id, code); a.status != http.StatusOK {
		t.Fatalf("confirm = %d %s", a.status, a.body)
	}
	group := func(n int) string {
		t.Helper()
		name := fmt.Sprintf("b%d", n)
		e.group(name, fmt.Sprintf("Down%d", n))
		e.alert(name, "a", fmt.Sprintf(`{"cluster":"gb%d","severity":"critical"}`, n), "")
		e.notify(name, "first notification")
		return e.alertGroupOf(fmt.Sprintf("gb%d", n))
	}
	if g := e.gatewayOf(e.gw); g == nil || g.Broken || g.BrokenSince != nil || g.Reason != nil || g.NextProbeAt != nil {
		t.Fatalf("the gateway before %+v", g)
	}

	// A call answered 401: the gateway is Broken.
	g1 := group(1)
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"`+callsPath+`","status":401,"times":1}`)
	e.queueCall(r, petr.id, g1)
	eventually(t, "the Broken gateway", func() bool { return e.broken(e.gw) })
	st := e.gatewayOf(e.gw)
	if st.BrokenSince == nil || st.Reason == nil || !strings.Contains(*st.Reason, "401") || st.NextProbeAt == nil {
		t.Fatalf("the Broken state %+v", st)
	}
	since, _ := time.Parse(time.RFC3339Nano, *st.BrokenSince)
	if next, _ := time.Parse(time.RFC3339Nano, *st.NextProbeAt); next.Sub(since) != 5*time.Minute {
		t.Errorf("the first probe %s after %s", *st.NextProbeAt, *st.BrokenSince)
	}
	if got := e.callState(petr.id); got != "not_delivered call_refused 1" {
		t.Errorf("the refused call %s", got)
	}
	eventually(t, "MusterNotificationGatewayBroken", func() bool {
		return strings.Contains(strings.Join(e.internalAlerts(), ","), "MusterNotificationGatewayBroken")
	})
	var groups struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	eventually(t, "the Alert Group of MusterNotificationGatewayBroken", func() bool {
		decode(t, e.api.do(http.MethodGet, "/api/v1/alert-groups?q=MusterNotificationGatewayBroken", ""), &groups)
		return len(groups.Items) > 0
	})
	if title := groups.Items[0].Title; !strings.Contains(title, "MusterNotificationGatewayBroken") &&
		!strings.Contains(title, "Notification gateway") {
		t.Errorf("the Alert Group %+v", groups.Items)
	}
	eventually(t, "gateway_broken", func() bool {
		return e.logged("gateway_broken", map[string]string{"connection": e.gw, "cause": "fatal"}) > 0
	})

	// The probe through info fails: still Broken, the gauge 1.
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"`+gatewayInfoPath+`","status":503}`)
	advance(t, r, 300)
	eventually(t, "the failed probe", func() bool {
		g := e.gatewayOf(e.gw)
		return g.Broken && g.Reason != nil && strings.Contains(*g.Reason, "503")
	})
	e.gaugeIs(r, e.gw, "1")

	// A call due while Broken is not sent.
	sent := len(e.calls())
	g2 := group(2)
	e.queueCall(other, petr.id, g2)
	eventually(t, "the call due while Broken", func() bool {
		return e.callState(petr.id) == "not_delivered gateway_broken 0"
	})
	if len(e.calls()) != sent {
		t.Fatalf("a call went through the Broken gateway")
	}
	if got := e.escalationReasons(g2); len(got) != 1 || got[0] != "gateway_broken" {
		t.Errorf("the escalation timeline %v", got)
	}

	// The faults go: the next probe ends the state, and nothing is replayed.
	e.fake(http.MethodDelete, e.fb+"/faults", "")
	advance(t, r, 300)
	eventually(t, "the recovered gateway", func() bool { return !e.broken(e.gw) })
	if g := e.gatewayOf(e.gw); g == nil || g.BrokenSince != nil || g.Reason != nil || g.NextProbeAt != nil {
		t.Errorf("the gateway after the recovery %+v", g)
	}
	eventually(t, "MusterNotificationGatewayBroken to resolve", func() bool {
		return !strings.Contains(strings.Join(e.internalAlerts(), ","), "MusterNotificationGatewayBroken")
	})
	e.gaugeIs(r, e.gw, "0")
	eventually(t, "gateway_recovered", func() bool {
		return e.logged("gateway_recovered", map[string]string{"connection": e.gw, "by": "probe"}) > 0
	})
	advance(t, r, 120)
	time.Sleep(time.Second)
	if len(e.calls()) != sent {
		t.Fatalf("a call due while Broken was sent after the recovery")
	}

	// A call that uses up its retry budget: Broken again; a Connection check ends it.
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"`+callsPath+`","status":503,
		"body":"{\"type\":\"about:blank\",\"title\":\"Busy\",\"status\":503,\"class\":\"transient\"}"}`)
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"`+gatewayInfoPath+`","status":503}`)
	g3 := group(3)
	e.queueCall(r, petr.id, g3)
	eventually(t, "the used budget", func() bool {
		if strings.HasPrefix(e.callState(petr.id), "not_delivered call_budget_used") {
			return true
		}
		advance(t, r, 301)
		return false
	})
	eventually(t, "Broken by the budget", func() bool {
		g := e.gatewayOf(e.gw)
		return g.Broken && g.Reason != nil && strings.HasPrefix(*g.Reason, "unavailable after repeated failures: ")
	})
	eventually(t, "gateway_broken of the budget", func() bool {
		return e.logged("gateway_broken", map[string]string{"connection": e.gw, "cause": "unavailable"}) > 0
	})
	e.fake(http.MethodDelete, e.fb+"/faults", "")
	if res := e.api.json(http.MethodPost, "/api/v1/connections/"+e.gw+"/checks", "", http.StatusOK); res["ok"] != true {
		t.Fatalf("the check = %v", res)
	}
	if e.broken(e.gw) {
		t.Fatal("the check did not end the Broken state")
	}
	eventually(t, "gateway_recovered by check", func() bool {
		return e.logged("gateway_recovered", map[string]string{"connection": e.gw, "by": "check"}) > 0
	})

	// A bridge of both roles.
	b := &brd{mm: e.mm, fb: e.fb}
	b.configure(`{"token":"` + fakebridge.SecondToken + `","roles":["messenger","gateway"]}`)
	b.conn = e.api.json(http.MethodPost, "/api/v1/connections", `{"type":"bridge","name":"both","base_url":"`+
		h.Fakes.Bridge+`","event_delivery":"poll","bot_token":"`+fakebridge.SecondToken+`","proxy":{"enabled":false},`+
		`"limiter":{"limit":30,"per_seconds":1}}`, http.StatusCreated)["id"].(string)
	if res := e.api.json(http.MethodPost, "/api/v1/connections/"+b.conn+"/checks", "", http.StatusOK); res["ok"] != true {
		t.Fatalf("the check of both = %v", res)
	}
	if g := e.gatewayOf(b.conn); g == nil || g.Broken {
		t.Fatalf("both as a gateway %+v", g)
	}
	const bothContact = "+10000000009"
	e.secrets = append(e.secrets, bothContact)
	c := petr.session.json(http.MethodPost, "/api/v1/me/gateway-contacts", `{"connection_id":"`+b.conn+
		`","contact":"`+bothContact+`"}`, http.StatusAccepted)
	all := e.notifications()
	m := contactCode.FindStringSubmatch(all[len(all)-1].Text)
	if m == nil || all[len(all)-1].Contact != bothContact {
		t.Fatalf("the code through both %+v", all[len(all)-1])
	}
	e.secrets = append(e.secrets, m[1])
	if a := e.confirm(petr.session, c["id"].(string), m[1]); a.status != http.StatusOK {
		t.Fatalf("confirm through both = %d %s", a.status, a.body)
	}
	dest := b.destination("both-alerts", fakebridge.ChannelAlerts)
	e.route("both", "both", []string{dest}, nil)
	start := func(n int) string {
		t.Helper()
		name := fmt.Sprintf("both%d", n)
		e.group(name, fmt.Sprintf("Both%d", n))
		e.alert(name, name, fmt.Sprintf(`{"team":"both","cluster":%q}`, name), "")
		e.notify(name, "first notification")
		return e.alertGroupOf(name)
	}

	// Its gateway Broken: its Destination stays healthy and delivers.
	advance(t, r, 61)
	ga := start(1)
	b.waitPosted("Both1", 1)
	e.fake(http.MethodPost, e.fb+"/faults", `{"path":"/v1/notifications","status":401,"times":1}`)
	e.queueCallOf(r, b.conn, petr.id, ga)
	eventually(t, "both's Broken gateway", func() bool { return e.broken(b.conn) })
	start(2)
	b.waitPosted("Both2", 1)
	if h := e.waitHealth(dest, "healthy"); h.Reason != nil {
		t.Errorf("the Destination of the Broken gateway %+v", h)
	}
	if e.broken(e.gw) {
		t.Error("the other gateway became Broken")
	}
	if res := e.api.json(http.MethodPost, "/api/v1/connections/"+b.conn+"/checks", "", http.StatusOK); res["ok"] != true ||
		e.broken(b.conn) {
		t.Fatalf("the check of both = %v", res)
	}

	// A Broken Destination of it: its gateway stays in order and calls.
	b.bridgeFault(problemFault("/v1/messages", http.StatusForbidden, "forbidden", "The bot was removed."))
	start(3)
	e.waitHealth(dest, "broken")
	if g := e.gatewayOf(b.conn); g == nil || g.Broken {
		t.Errorf("the gateway of the Broken Destination %+v", g)
	}
	before := len(e.notifications())
	advance(t, r, 61)
	e.queueCallOf(other, b.conn, petr.id, ga)
	eventually(t, "the call through both", func() bool { return len(e.notifications()) == before+1 })
	if g := e.gatewayOf(b.conn); g == nil || g.Broken {
		t.Errorf("both after the call %+v", g)
	}

	// The contacts never reach the log.
	for _, rep := range h.Replicas {
		for _, s := range e.secrets {
			if strings.Contains(rep.Output(), s) {
				t.Errorf("%s reached the log of muster dev", s)
			}
		}
	}
}
