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
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/organization"
)

// putOrganization reads the organization resource through api, changes the body of its update and sends it with the
// ETag it read.
func putOrganization(api patClient, change func(m map[string]any)) answer {
	api.t.Helper()
	read := api.do(http.MethodGet, "/api/v1/organization", "")
	var m map[string]any
	decode(api.t, read, &m)
	delete(m, "id")
	delete(m, "etag")
	m["outgoing_heartbeat"] = map[string]any{"proxy": map[string]any{"enabled": false}}
	change(m)
	body, _ := json.Marshal(m)
	return api.do(http.MethodPut, "/api/v1/organization", string(body), "If-Match", read.header.Get("ETag"))
}

// putPolicy replaces the outbound address policy through api.
func putPolicy(api patClient, policy string, allowed ...string) {
	api.t.Helper()
	read := api.do(http.MethodGet, "/api/v1/organization/outbound-policy", "")
	body, _ := json.Marshal(map[string]any{"policy": policy, "allowed": append([]string{}, allowed...),
		"denied": []string{}})
	if a := api.do(http.MethodPut, "/api/v1/organization/outbound-policy", string(body), "If-Match",
		read.header.Get("ETag")); a.status != http.StatusOK {
		api.t.Fatalf("put the policy = %d %s", a.status, a.body)
	}
}

// firstError is the first error of a problem: its pointer and code.
func firstError(t *testing.T, a answer) (string, string) {
	t.Helper()
	var p struct {
		Errors []struct{ Pointer, Code string }
	}
	decode(t, a, &p)
	if len(p.Errors) == 0 {
		return "", ""
	}
	return p.Errors[0].Pointer, p.Errors[0].Code
}

// testClass runs the Destination test of dest from the example source and returns the first error class that is not
// none, empty when every step passed.
func testClass(api patClient, dest string) string {
	api.t.Helper()
	var res destTestResult
	a := api.do(http.MethodPost, "/api/v1/destinations/"+dest+"/tests", `{"source":{"kind":"example"}}`)
	if a.status != http.StatusOK {
		api.t.Fatalf("test of %s = %d %s", dest, a.status, a.body)
	}
	decode(api.t, a, &res)
	for _, s := range res.Steps {
		if s.ErrorClass != "none" {
			return s.ErrorClass
		}
	}
	return ""
}

// logLines are the lines of the replica's output with the event.
func logLines(r *Replica, event string) []map[string]any {
	var out []map[string]any
	for line := range strings.Lines(r.Output()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["event"] == event {
			out = append(out, m)
		}
	}
	return out
}

// organizationHints counts the organization hints on the stream.
func organizationHints(s *stream) int {
	n := 0
	for _, h := range s.hints() {
		if strings.Contains(h, `"type":"organization"`) {
			n++
		}
	}
	return n
}

// TestOrganizationSettings is S-055 against two `muster dev --replica` processes with the fakes in the test process:
// the retention order and a stale If-Match (C-20.AC-6, FR-8), full field pointers, the values seen and a mapping that
// changes later Alerts only (C-20.AC-4, AC-3, FR-2), the time zone of messages and the Instance labels (C-20.FR-1),
// the token grace (C-04.FR-8), and the outbound address policy (C-20.FR-3, AC-1, C-02.FR-21): a change on replica A
// applies at once on replica B, whose warm connection the notification closes, 169.254.169.254 stays blocked, and the
// strict policy refuses a private address. Every replica start allows loopback again, so both start first and the
// policy steps come last.
func TestOrganizationSettings(t *testing.T) {
	h := Start(t, FakesInProcess)
	a := h.StartReplica(t, ReplicaOptions{})
	b := h.StartReplica(t, ReplicaOptions{ListenApp: ":8080", ListenIngest: ":8081", ListenInternal: ":8082"})
	e := newMM(t, h, b)
	conn := e.connection(50)
	md := e.destination("alerts", conn, fakemattermost.ChannelAlerts, nil, 50)
	e.route("t", "t", []string{md}, nil)
	fire := func(e *mm, cluster, severity string) string {
		e.t.Helper()
		e.group(cluster, "Sev")
		e.alert(cluster, "a", `{"team":"t","cluster":"`+cluster+`","severity":"`+severity+`"}`, "")
		e.notify(cluster, "first notification")
		return e.groupOf(cluster)
	}

	t.Run("retention and validation", func(t *testing.T) {
		e := e.on(t)
		api := patClient{t: t, base: a.App, token: e.api.token}
		got := putOrganization(api, func(m map[string]any) {
			r := m["retention"].(map[string]any)
			r["alert_group_summaries_days"], r["alert_details_days"] = 30, 90
		})
		if p, c := firstError(t, got); got.status != http.StatusUnprocessableEntity ||
			p != "/retention/alert_group_summaries_days" || c != "retention_order" {
			t.Errorf("summaries below details = %d %s", got.status, got.body)
		}
		r := api.json(http.MethodGet, "/api/v1/organization", "", http.StatusOK)["retention"].(map[string]any)
		if r["alert_group_summaries_days"] != 730.0 || r["alert_details_days"] != 90.0 {
			t.Errorf("a refused update changed the retention: %v", r)
		}
		got = putOrganization(e.api, func(m map[string]any) {
			r := m["retention"].(map[string]any)
			r["alert_group_summaries_days"], r["alert_details_days"] = 90, 90
		})
		if got.status != http.StatusOK {
			t.Fatalf("equal periods = %d %s", got.status, got.body)
		}
		if lines := logLines(b, "organization_updated"); len(lines) != 1 ||
			lines[0]["fields"] != "/retention/alert_group_summaries_days" {
			t.Errorf("organization_updated %v", lines)
		}
		var audit struct {
			Items []struct {
				Diff []struct{ Pointer string } `json:"diff"`
			} `json:"items"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/audit-log?action=organization.updated&limit=1", ""), &audit)
		if len(audit.Items) != 1 || len(audit.Items[0].Diff) != 1 ||
			audit.Items[0].Diff[0].Pointer != "/retention/alert_group_summaries_days" {
			t.Errorf("audit %+v", audit)
		}
		read := api.do(http.MethodGet, "/api/v1/organization", "")
		if a := api.do(http.MethodPut, "/api/v1/organization", string(read.body), "If-Match", `"1"`); a.status !=
			http.StatusPreconditionFailed {
			t.Errorf("a stale If-Match = %d %s", a.status, a.body)
		}
		for _, c := range []struct {
			change        func(m map[string]any)
			status        int
			pointer, code string
		}{
			{func(m map[string]any) { m["time_zone"] = "Mars/Olympus" }, http.StatusUnprocessableEntity, "/time_zone",
				"invalid_format"},
			{func(m map[string]any) { m["severity_styles"].([]any)[1].(map[string]any)["color"] = "red" },
				http.StatusUnprocessableEntity, "/severity_styles/1/color", "invalid_format"},
			{func(m map[string]any) {
				m["severity_mapping"] = append(m["severity_mapping"].([]any), map[string]any{"value": "critical",
					"level": "info"})
			}, http.StatusUnprocessableEntity, "/severity_mapping/4/value", "duplicate"},
			{func(m map[string]any) { m["retention"].(map[string]any)["audit_log_days"] = 36501 },
				http.StatusBadRequest, "/retention/audit_log_days", "invalid_format"},
		} {
			got := putOrganization(api, c.change)
			if p, code := firstError(t, got); got.status != c.status || p != c.pointer || code != c.code {
				t.Errorf("%s = %d %s", c.pointer, got.status, got.body)
			}
		}
	})

	t.Run("values seen and mapping", func(t *testing.T) {
		e := e.on(t)
		api := patClient{t: t, base: a.App, token: e.api.token}
		old := fire(e, "v1", "P1")
		fire(e, "v2", "P5")
		type value struct {
			Value  string `json:"value"`
			Count  int    `json:"count"`
			Level  string `json:"level"`
			Mapped bool   `json:"mapped"`
		}
		values := func() (map[string]value, int) {
			t.Helper()
			var list struct {
				Items      []value `json:"items"`
				WindowDays int     `json:"window_days"`
			}
			decode(t, api.do(http.MethodGet, "/api/v1/organization/severity-values", ""), &list)
			out := map[string]value{}
			for _, v := range list.Items {
				out[v.Value] = v
			}
			return out, list.WindowDays
		}
		seen, window := values()
		if seen["P1"] != (value{"P1", 1, "warning", false}) || seen["P5"] != (value{"P5", 1, "warning", false}) ||
			window != 14 {
			t.Errorf("values %v over %d days", seen, window)
		}
		if got := putOrganization(api, func(m map[string]any) {
			m["severity_mapping"] = append(m["severity_mapping"].([]any), map[string]any{"value": "P1",
				"level": "critical"})
		}); got.status != http.StatusOK {
			t.Fatalf("map P1 = %d %s", got.status, got.body)
		}
		if g := e.alertGroup(old); g["status"] != "firing" || g["severity_level"] != "warning" || g["urgent"] != false {
			t.Errorf("the open Alert Group changed: %v %v %v", g["status"], g["severity_level"], g["urgent"])
		}
		if g := e.alertGroup(fire(e, "v3", "P1")); g["severity_level"] != "critical" || g["urgent"] != true {
			t.Errorf("the new Alert Group = %v %v", g["severity_level"], g["urgent"])
		}
		if seen, _ := values(); seen["P1"] != (value{"P1", 2, "critical", true}) {
			t.Errorf("P1 after the mapping = %+v", seen["P1"])
		}
	})

	t.Run("time zone", func(t *testing.T) {
		e := e.on(t)
		if got := putOrganization(e.api, func(m map[string]any) { m["time_zone"] = "Europe/Berlin" }); got.status !=
			http.StatusOK {
			t.Fatalf("time zone = %d %s", got.status, got.body)
		}
		root := e.root(e.number(fire(e, "v4", "warning")))
		if text := root.Message + string(root.Props); !regexp.MustCompile(` CES?T\b`).MatchString(text) {
			t.Errorf("the root message has no time in Europe/Berlin: %s", text)
		}
	})

	t.Run("instance labels", func(t *testing.T) {
		e := e.on(t)
		events := func(id string) []map[string]any {
			t.Helper()
			var page struct {
				Items []map[string]any `json:"items"`
			}
			decode(t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+id+"/timeline?limit=200", ""), &page)
			return page.Items
		}
		replaced := func(id string) []string {
			t.Helper()
			var out []string
			for _, x := range events(id) {
				if x["event"] == "alert_replaced" {
					out = append(out, fmt.Sprint(x["replaced_label"]))
				}
			}
			slices.Sort(out)
			return out
		}
		labels := func(cluster, pod, node string) string {
			return `{"team":"t","cluster":"` + cluster + `","pod":"` + pod + `","node":"` + node + `"}`
		}
		e.group("i1", "Inst")
		e.alert("i1", "a1", labels("i1", "p1", "n1"), "")
		e.notify("i1", "first notification")
		g1 := e.groupOf("i1")
		e.alert("i1", "a2", labels("i1", "p2", "n1"), "")
		e.notify("i1", "new alerts added")
		if got := replaced(g1); !slices.Equal(got, []string{"pod"}) {
			t.Errorf("with the default Instance labels: %v", got)
		}
		if got := putOrganization(e.api, func(m map[string]any) { m["instance_labels"] = []string{"node"} }); got.status !=
			http.StatusOK {
			t.Fatalf("instance labels = %d %s", got.status, got.body)
		}
		e.group("i2", "Inst")
		e.alert("i2", "a1", labels("i2", "p1", "n1"), "")
		e.notify("i2", "first notification")
		g2 := e.groupOf("i2")
		e.alert("i2", "a2", labels("i2", "p2", "n1"), "")
		e.notify("i2", "new alerts added")
		if got := replaced(g2); len(got) != 0 {
			t.Errorf("a pod is no Instance label any more, yet %v", got)
		}
		e.alert("i2", "a3", labels("i2", "p1", "n2"), "")
		e.notify("i2", "new alerts added")
		if got := replaced(g2); !slices.Equal(got, []string{"node"}) {
			t.Errorf("with the Instance label node: %v", got)
		}
		if got := replaced(g1); !slices.Equal(got, []string{"pod"}) {
			t.Errorf("the earlier Alert Group changed: %v", got)
		}
	})

	t.Run("token grace", func(t *testing.T) {
		e := e.on(t)
		if got := call(t, http.MethodPost, "http://"+devmode.OIDCAddr+"/_fake/config",
			`{"grant_offline_access":false}`); got.status != http.StatusNoContent {
			t.Fatalf("config = %d", got.status)
		}
		nextUser(t, "u-olga", "olga", "oncall")
		olga := newAgent(t, b.App)
		if to := olga.oidcSignIn(); to != "/" {
			t.Fatalf("olga's sign-in ended at %s", to)
		}
		token := olga.createToken("olga-scripts")
		if got := putOrganization(e.api, func(m map[string]any) { m["oidc_token_grace_seconds"] = 3600 }); got.status !=
			http.StatusOK {
			t.Fatalf("grace = %d %s", got.status, got.body)
		}
		h.exec(t, `UPDATE users SET oidc_last_contact_at = oidc_last_contact_at - interval '2 hours'
			WHERE login = 'olga'`)
		if got := withToken(t, a.App, token); got.status != http.StatusUnauthorized ||
			codeOf(t, got) != "oidc_recheck_required" {
			t.Errorf("past a grace of an hour = %d %s", got.status, got.body)
		}
		if got := putOrganization(e.api, func(m map[string]any) { m["oidc_token_grace_seconds"] = 604800 }); got.status !=
			http.StatusOK {
			t.Fatalf("grace = %d %s", got.status, got.body)
		}
		if got := withToken(t, a.App, token); got.status != http.StatusOK {
			t.Errorf("within a grace of 7 days = %d %s", got.status, got.body)
		}
	})

	t.Run("outbound policy", func(t *testing.T) {
		e := e.on(t)
		apiA := patClient{t: t, base: a.App, token: e.api.token}
		wd := e.webhook("auto", "auto", "[]")["id"].(string)
		var policy struct {
			Policy        string   `json:"policy"`
			Allowed       []string `json:"allowed"`
			AlwaysBlocked []string `json:"always_blocked"`
			Etag          string   `json:"etag"`
		}
		read := apiA.do(http.MethodGet, "/api/v1/organization/outbound-policy", "")
		decode(t, read, &policy)
		if policy.Policy != "standard" || !slices.Equal(policy.Allowed, []string{"127.0.0.0/8"}) ||
			len(policy.AlwaysBlocked) == 0 || read.header.Get("ETag") != policy.Etag {
			t.Fatalf("policy %+v", policy)
		}
		admin := newAgent(t, b.App)
		admin.signIn(devAdmin, devAdminPassword)
		s := openStream(t, admin)
		if s.status != http.StatusOK {
			t.Fatalf("stream = %d", s.status)
		}
		// change sets the policy on replica A and waits until replica B announced it on its stream, which it does
		// only after it dropped its cached policy and closed its idle connections.
		change := func(mode string, allowed ...string) time.Time {
			t.Helper()
			before := organizationHints(s)
			start := time.Now()
			putPolicy(apiA, mode, allowed...)
			eventually(t, "the organization hint on replica B", func() bool { return organizationHints(s) > before })
			return start
		}
		if got := testClass(e.api, wd); got != "" {
			t.Fatalf("the warming test = %q", got)
		}
		sent := len(e.received("auto"))
		start := change("standard")
		if got := testClass(e.api, wd); got != "blocked" {
			t.Errorf("over the warm connection after the change = %q", got)
		}
		if took := time.Since(start); took >= organization.OutboundPolicyMaxAge {
			t.Errorf("replica B applied the change after %v", took)
		}
		if got := len(e.received("auto")); got != sent {
			t.Errorf("the blocked test reached the endpoint: %d requests, %d before", got, sent)
		}
		change("standard", "127.0.0.0/8")
		if got := testClass(e.api, wd); got != "" {
			t.Errorf("loopback allowed again = %q", got)
		}

		meta := e.api.json(http.MethodPost, "/api/v1/destinations", `{"type":"webhook","name":"meta","mode":"events",
			"events":{"url":"http://169.254.169.254/latest","headers":[]},"proxy":{"enabled":false},"mentions":`+
			mentionSettings(nil)+`,"limiter":{"limit":5,"per_seconds":1}}`, http.StatusCreated)["destination"].(map[string]any)["id"].(string)
		change("standard", "127.0.0.0/8", "169.254.0.0/16")
		if got := testClass(apiA, meta); got != "blocked" {
			t.Errorf("169.254.169.254 with 169.254.0.0/16 allowed = %q", got)
		}
		found := false
		for _, l := range logLines(a, "outbound_blocked") {
			if l["host"] == "169.254.169.254" && strings.Contains(fmt.Sprint(l["rule"]), "always blocked") {
				found = true
			}
		}
		if !found {
			t.Errorf("replica A logged no outbound_blocked for 169.254.169.254")
		}

		private := e.api.json(http.MethodPost, "/api/v1/destinations", `{"type":"webhook","name":"private",
			"mode":"events","events":{"url":"http://10.0.0.1:9/hook","headers":[]},"proxy":{"enabled":false},
			"mentions":`+mentionSettings(nil)+`,"limiter":{"limit":5,"per_seconds":1}}`,
			http.StatusCreated)["destination"].(map[string]any)["id"].(string)
		change("strict", "127.0.0.0/8")
		if got := testClass(e.api, wd); got != "" {
			t.Errorf("strict with loopback allowed = %q", got)
		}
		if got := testClass(e.api, private); got != "blocked" {
			t.Errorf("a private address under the strict policy = %q", got)
		}
		found = false
		for _, l := range logLines(b, "outbound_blocked") {
			if l["host"] == "10.0.0.1" && strings.Contains(fmt.Sprint(l["rule"]), "not a public address") {
				found = true
			}
		}
		if !found {
			t.Errorf("replica B logged no strict refusal of 10.0.0.1")
		}
		change("strict")
		if got := testClass(e.api, wd); got != "blocked" {
			t.Errorf("strict without loopback = %q", got)
		}
		change("standard", "127.0.0.0/8")

		var audit struct {
			Items []struct {
				Diff []struct{ Pointer string } `json:"diff"`
			} `json:"items"`
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/audit-log?action=outbound_policy.updated&limit=50", ""), &audit)
		if len(audit.Items) != 6 || len(audit.Items[0].Diff) == 0 {
			t.Errorf("audit %+v", audit)
		}
		if len(logLines(a, "outbound_policy_updated")) != 6 {
			t.Errorf("replica A logged %d outbound_policy_updated", len(logLines(a, "outbound_policy_updated")))
		}
		// A client that leaves its stream is not an error.
		s.cancel()
		eventually(t, "the stream to close", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.closed
		})
	})

	for _, r := range []*Replica{a, b} {
		e.api.do(http.MethodGet, "/api/v1/organization", "")
		if lines := logLines(r, "api_request_failed"); len(lines) != 0 {
			t.Errorf("%s logged api_request_failed: %v", r.App, lines)
		}
	}
}
