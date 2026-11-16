// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

// TestStatistics is S-174 against `muster dev`, with E2E_REPLICAS=2 the reads on the second replica, as its
// Verification with the development clock: the first acknowledgement stays through a Takeover, an Unacknowledge,
// another Acknowledge and a Reopen (C-09.AC-27); By people with the numbers of C-09.AC-28; the item "Unknown" adding up
// with By Route (C-09.AC-30); By Team (C-09.AC-31); the filters of every view, a Matcher =~ included (C-09.AC-32); the
// same values for a Viewer, a Responder and an Admin, and the refusals (C-09.AC-33); and the period by start
// (C-09.AC-29).
func TestStatistics(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	reader := h.Replicas[len(h.Replicas)-1]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	A := patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"statistics","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)}
	aliceID, AL := personWithToken(t, admin, r.App, "alice", "responder")
	_, BO := personWithToken(t, admin, r.App, "bob", "responder")
	_, CA := personWithToken(t, admin, r.App, "carol", "responder")
	_, V := personWithToken(t, admin, r.App, "victor", "viewer")
	sa := admin.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"terraform","role":"responder"}`,
		http.StatusCreated)["id"].(string)
	SAT := patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/service-accounts/"+sa+"/tokens",
		`{"name":"terraform"}`, http.StatusCreated)["value"].(string)}
	at := func(c patClient) patClient { return patClient{t: t, base: reader.App, token: c.token} }
	tp := A.json(http.MethodPost, "/api/v1/teams", `{"name":"payments","slug":"payments","description":"",`+
		`"own_working_hours":false}`, http.StatusCreated)["id"].(string)
	ts := A.json(http.MethodPost, "/api/v1/teams", `{"name":"search","slug":"search","description":"",`+
		`"own_working_hours":false}`, http.StatusCreated)["id"].(string)
	A.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+aliceID+`"}`, http.StatusCreated)

	in := A.json(http.MethodPost, "/api/v1/integrations", `{"name":"sev","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	intID := in["id"].(string)
	intToken := A.json(http.MethodPost, "/api/v1/integrations/"+intID+"/tokens", `{"name":"sev"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"sev","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		intToken+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	var profiles struct {
		Items []struct {
			Policy json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, A.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	// The Informational profile: no ack timeout or Reminders while the clock moves by days.
	route := A.json(http.MethodPost, "/api/v1/routes", `{"name":"stats","matchers":[{"label":"scope","op":"=",`+
		`"value":"stats"}],"urgent":false,"group_key":["alertname","cluster"],"destination_ids":[],"policy":`+
		string(profiles.Items[1].Policy)+`}`, http.StatusCreated)["id"].(string)

	processed := func() int64 {
		return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`)
	}
	send := func(method, path, body string) {
		t.Helper()
		if a := call(t, method, fam+path, body); a.status/100 != 2 {
			t.Fatalf("%s %s = %d %s", method, path, a.status, a.body)
		}
	}
	notify := func(group, reason string) {
		t.Helper()
		before := processed()
		send(http.MethodPost, "/groups/"+group+"/notify", `{"reason":"`+reason+`"}`)
		eventually(t, "the snapshot to be processed", func() bool { return processed() > before })
	}
	fire := func(cluster, alert, severity, extra string) {
		t.Helper()
		send(http.MethodPut, "/groups/"+cluster+"/alerts/"+alert, `{"labels":{"scope":"stats","cluster":"`+cluster+
			`","severity":"`+severity+`"`+extra+`}}`)
	}
	groupOf := func(cluster string) string {
		t.Helper()
		items := A.json(http.MethodGet, "/api/v1/integrations/"+intID+"/alerts?"+url.Values{"label": {`cluster="` +
			cluster + `"`}}.Encode(), "", http.StatusOK)["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no alert of cluster %s", cluster)
		}
		return items[0].(map[string]any)["alert_group"].(map[string]any)["id"].(string)
	}
	// NEW starts the Alert Group of the cluster with one Alert of the Severity level and the extra labels.
	NEW := func(cluster, severity, extra string) string {
		t.Helper()
		send(http.MethodPut, "/groups/"+cluster, `{"receiver":"sev","route":"{}","labels":{"alertname":"`+cluster+`"}}`)
		fire(cluster, "a", severity, extra)
		notify(cluster, "first notification")
		return groupOf(cluster)
	}
	resolve := func(cluster string) {
		t.Helper()
		send(http.MethodDelete, "/groups/"+cluster+"/alerts/a", "")
		notify(cluster, "resolved")
	}
	DO := func(c patClient, id, cmd string) {
		t.Helper()
		body := ""
		if cmd == "resolve" {
			body = `{}`
		}
		c.json(http.MethodPost, "/api/v1/alert-groups/"+id+"/"+cmd, body, http.StatusOK)
	}
	clock := func(seconds int) { advance(t, r, seconds) }
	type item struct {
		name, kind string
		a, r, n    int
		m, p       float64
		null       bool
	}
	people := func(c patClient, query string) []item {
		t.Helper()
		var out []item
		for _, raw := range at(c).json(http.MethodGet, "/api/v1/alert-group-actor-statistics?"+query, "",
			http.StatusOK)["items"].([]any) {
			it := raw.(map[string]any)
			ack := it["time_to_acknowledge"].(map[string]any)
			x := item{a: int(it["acknowledged_first_count"].(float64)), r: int(it["resolved_count"].(float64)),
				n: int(ack["count"].(float64))}
			if actor, ok := it["actor"].(map[string]any); ok {
				x.name, x.kind = actor["name"].(string), actor["kind"].(string)
			}
			if m, ok := ack["median_seconds"].(float64); ok {
				x.m, x.p = m, ack["p95_seconds"].(float64)
			} else {
				x.null = true
			}
			out = append(out, x)
		}
		return out
	}
	short := func(items []item) string {
		var parts []string
		for _, x := range items {
			name := x.name
			if name == "" {
				name = "unknown"
			}
			parts = append(parts, fmt.Sprintf("%s:%d:%d", name, x.a, x.r))
		}
		return strings.Join(parts, " ")
	}
	near := func(got, want, slack float64) bool { return got >= want && got <= want+slack }
	sum := func(c patClient, groupBy, query, field string) int {
		t.Helper()
		n := 0
		for _, raw := range at(c).json(http.MethodGet, "/api/v1/alert-group-statistics?group_by="+groupBy+"&"+query, "",
			http.StatusOK)["items"].([]any) {
			it := raw.(map[string]any)
			if field == "ack" {
				n += int(it["time_to_acknowledge"].(map[string]any)["count"].(float64))
			} else {
				n += int(it["alert_group_count"].(float64))
			}
		}
		return n
	}
	from := url.QueryEscape(readClock(t, r).Now.UTC().Format(time.RFC3339Nano))
	label := func(matcher string) string { return "label=" + url.QueryEscape(matcher) + "&from=" + from }

	// C-09.AC-27: the first acknowledgement stays.
	began := time.Now()
	g0 := NEW("s0", "warning", "")
	clock(300)
	DO(AL, g0, "acknowledge")
	slack := math.Ceil(time.Since(began).Seconds()) + 1
	DO(BO, g0, "acknowledge")
	DO(BO, g0, "unacknowledge")
	DO(CA, g0, "acknowledge")
	first := func() {
		t.Helper()
		if n := h.count(t, `SELECT count(*) FROM alert_groups g JOIN users u ON u.id = g.first_acknowledged_by_user_id
			WHERE g.public_id = '`+g0+`' AND u.login = 'alice' AND extract(epoch FROM g.first_acknowledged_at -
			g.created_at) BETWEEN 300 AND `+fmt.Sprint(300+slack)); n != 1 {
			t.Errorf("%s is not first acknowledged by alice after 300 seconds", g0)
		}
	}
	first()
	resolve("s0")
	// A new Alert of the same Group key, inside the Reopen window of the Route.
	clock(60)
	fire("s0", "b", "warning", "")
	notify("s0", "new alerts")
	v := A.json(http.MethodGet, "/api/v1/alert-groups/"+g0, "", http.StatusOK)
	if owner, _ := v["owner"].(map[string]any); v["status"] != "acknowledged" || owner["login"] != "carol" ||
		v["reopen_count"] != 1.0 {
		t.Fatalf("after the reopen = %v", v)
	}
	first()
	if got := people(A, label(`cluster="s0"`)); len(got) != 1 || got[0].name != "alice" || got[0].a != 1 ||
		got[0].r != 0 || !near(got[0].m, 300, slack) {
		t.Errorf("by people of s0 = %+v", got)
	}

	// C-09.AC-28: By people.
	began = time.Now()
	g1 := NEW("s1", "warning", "")
	clock(120)
	DO(AL, g1, "acknowledge")
	g2 := NEW("s2", "warning", "")
	clock(240)
	DO(AL, g2, "acknowledge")
	g3 := NEW("s3", "warning", "")
	clock(1800)
	DO(AL, g3, "acknowledge")
	g4 := NEW("s4", "warning", "")
	clock(600)
	DO(BO, g4, "acknowledge")
	slack = math.Ceil(time.Since(began).Seconds()) + 1
	DO(BO, g1, "resolve")
	DO(BO, g2, "resolve")
	DO(SAT, g4, "resolve")
	resolve("s3")
	S := label(`cluster=~"s[1-4]"`)
	got := people(A, S)
	if short(got) != "alice:3:0 bob:1:2 terraform:0:1" || got[0].kind != "user" || got[2].kind != "service_account" ||
		!near(got[0].m, 240, slack) || !near(got[0].p, 1644, slack) || !near(got[1].m, 600, slack) ||
		!near(got[1].p, 600, slack) || !got[2].null || got[2].n != 0 {
		t.Errorf("by people = %+v", got)
	}
	// C-09.AC-33: a Viewer and a Responder read the values an Admin reads, for one period.
	fixed := S + "&to=" + url.QueryEscape(readClock(t, r).Now.UTC().Add(time.Hour).Format(time.RFC3339Nano))
	want := string(at(A).do(http.MethodGet, "/api/v1/alert-group-actor-statistics?"+fixed, "").body)
	for name, c := range map[string]patClient{"viewer": V, "responder": AL} {
		if a := at(c).do(http.MethodGet, "/api/v1/alert-group-actor-statistics?"+fixed, ""); a.status != http.StatusOK ||
			string(a.body) != want {
			t.Errorf("%s = %d %s, want %s", name, a.status, a.body, want)
		}
		for _, by := range []string{"route", "team"} {
			path := "/api/v1/alert-group-statistics?group_by=" + by + "&" + fixed
			if a, b := at(c).do(http.MethodGet, path, ""), at(A).do(http.MethodGet, path, ""); a.status != http.StatusOK ||
				string(a.body) != string(b.body) {
				t.Errorf("%s by %s = %d %s, want %s", name, by, a.status, a.body, b.body)
			}
		}
	}

	// C-09.AC-30: the item "Unknown" adds up with By Route; as for an Alert Group acknowledged before the upgrade.
	h.exec(t, `UPDATE alert_groups SET first_acknowledged_by_user_id = NULL WHERE public_id = '`+g4+`'`)
	if got := short(people(A, S)); got != "alice:3:0 bob:0:2 terraform:0:1 unknown:1:0" {
		t.Errorf("with unknown = %s", got)
	}
	if n := sum(A, "route", S, "ack"); n != 4 {
		t.Errorf("time to acknowledge by route counts %d", n)
	}
	if got := short(people(AL, S+"&first_acknowledged_by=me")); got != "alice:3:0 bob:0:2" {
		t.Errorf("first acknowledged by me = %s", got)
	}

	// C-09.AC-31: By Team, with an Alert Group of both Teams, one of payments and one without a Team.
	g5 := NEW("t1", "warning", `,"team":"payments"`)
	fire("t1", "b", "warning", `,"team":"search"`)
	notify("t1", "new alerts")
	g6 := NEW("t2", "warning", `,"team":"payments"`)
	NEW("t3", "warning", "")
	DO(AL, g5, "acknowledge")
	DO(BO, g6, "acknowledge")
	T := label(`cluster=~"t[1-3]"`)
	var teams []string
	for _, raw := range at(A).json(http.MethodGet, "/api/v1/alert-group-statistics?group_by=team&"+T, "",
		http.StatusOK)["items"].([]any) {
		it := raw.(map[string]any)
		teams = append(teams, fmt.Sprintf("%s:%v", it["subject"].(map[string]any)["name"], it["alert_group_count"]))
	}
	if strings.Join(teams, " ") != "payments:2 search:1" {
		t.Errorf("by team = %v", teams)
	}
	if got := short(people(A, T+"&team="+ts)); got != "alice:1:0" {
		t.Errorf("by people of search = %s", got)
	}
	if got := short(people(A, T+"&team="+tp)); got != "alice:1:0 bob:1:0" {
		t.Errorf("by people of payments = %s", got)
	}
	if got := short(people(AL, T+"&team=mine")); got != "alice:1:0 bob:1:0" {
		t.Errorf("by people of alice's teams = %s", got)
	}

	// C-09.AC-32: the filters reach every view, a Matcher =~ included; all four are Alert Groups of payments.
	g8 := NEW("f1", "critical", `,"namespace":"payments","team":"payments"`)
	g9 := NEW("f2", "critical", `,"namespace":"payments","team":"payments"`)
	NEW("f3", "warning", `,"namespace":"payments","team":"payments"`)
	NEW("f4", "critical", `,"namespace":"search","team":"payments"`)
	DO(AL, g8, "acknowledge")
	DO(BO, g9, "acknowledge")
	F := "severity=critical&label=" + url.QueryEscape(`namespace="payments"`) + "&" + label(`cluster=~"f."`) +
		"&route=" + route + "&integration=" + intID
	for _, by := range []string{"route", "integration", "team"} {
		if n := sum(A, by, F, "count"); n != 2 {
			t.Errorf("by %s counts %d", by, n)
		}
		if n := sum(AL, by, F+"&first_acknowledged_by=me", "count"); n != 1 {
			t.Errorf("by %s, first acknowledged by me, counts %d", by, n)
		}
	}
	if got := short(people(A, F)); got != "alice:1:0 bob:1:0" {
		t.Errorf("by people of the filters = %s", got)
	}
	if got := short(people(AL, F+"&first_acknowledged_by=me")); got != "alice:1:0" {
		t.Errorf("by people of the filters, first acknowledged by me = %s", got)
	}
	refused := func(c patClient, path, code string) {
		t.Helper()
		a := at(c).do(http.MethodGet, path, "")
		var p struct {
			Errors []struct {
				Code    string `json:"code"`
				Pointer string `json:"pointer"`
			} `json:"errors"`
		}
		decode(t, a, &p)
		if a.status != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Code != code {
			t.Errorf("%s = %d %s", path, a.status, a.body)
		}
	}
	for _, op := range []string{"/api/v1/alert-group-actor-statistics?",
		"/api/v1/alert-group-statistics?group_by=route&"} {
		refused(A, op+"first_acknowledged_by=SR0000000000ZZ", "unknown_id")
		refused(A, op+"first_acknowledged_by="+sa, "unknown_id")
		refused(A, op+"team=TM0000000000ZZ", "unknown_id")
		refused(SAT, op+"team=mine", "unsupported")
		refused(SAT, op+"first_acknowledged_by=me", "unsupported")
		if a := call(t, http.MethodGet, reader.App+strings.TrimSuffix(strings.TrimSuffix(op, "&"), "?"), ""); a.status !=
			http.StatusUnauthorized {
			t.Errorf("%s without authentication = %d", op, a.status)
		}
	}

	// C-09.AC-29: the period selects the Alert Groups by their start. p0 starts the day before the 7-day period and Bob
	// acknowledges it inside it; p1 starts on its last day and Alice acknowledges it a day after it ends.
	p0 := NEW("p0", "warning", "")
	clock(24*3600 + 60)
	start := readClock(t, r).Now.UTC()
	clock(6 * 24 * 3600)
	p1 := NEW("p1", "warning", "")
	DO(BO, p0, "acknowledge")
	clock(2 * 24 * 3600)
	DO(AL, p1, "acknowledge")
	period := label(`cluster=~"p."`)
	period = strings.Replace(period, "from="+from, "from="+url.QueryEscape(start.Format(time.RFC3339Nano))+"&to="+
		url.QueryEscape(start.Add(7*24*time.Hour).Format(time.RFC3339Nano)), 1)
	if got := people(A, period); short(got) != "alice:1:0" || got[0].m <= 24*3600 {
		t.Errorf("by people of the period = %+v", got)
	}
	if n := sum(A, "route", period, "count"); n != 1 {
		t.Errorf("by route of the period counts %d", n)
	}
}
