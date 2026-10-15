// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/leader"
)

// pingURL is the dead man's switch of the checks: the fake receiving endpoint answers 200 to any method below
// /hook/dev/ and lists the requests at /_fake/received/dev.
var pingURL = "http://" + devmode.WebhookAddr + "/hook/dev/ping"

// pings are the requests the fake receiving endpoint got at pingURL.
func pings(t *testing.T) []struct{ Method string } {
	t.Helper()
	var all []struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	decode(t, call(t, http.MethodGet, "http://"+devmode.WebhookAddr+"/_fake/received/dev", ""), &all)
	var out []struct{ Method string }
	for _, r := range all {
		if r.Path == "/hook/dev/ping" {
			out = append(out, struct{ Method string }{r.Method})
		}
	}
	return out
}

// selfAPI is an Admin's Personal access token on a replica, which outlives the jumps of the development clock, and
// the Admin's session, for the live-updates stream.
func selfAPI(t *testing.T, r *Replica) (patClient, *agent) {
	t.Helper()
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"self-observation","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	return patClient{t: t, base: r.App, token: token}, admin
}

// setHeartbeat writes the outgoing heartbeat of the organization resource, read with its ETag first.
func setHeartbeat(api patClient, hb map[string]any) map[string]any {
	api.t.Helper()
	read := api.do(http.MethodGet, "/api/v1/organization", "")
	var org map[string]any
	decode(api.t, read, &org)
	delete(org, "id")
	delete(org, "etag")
	org["outgoing_heartbeat"] = hb
	body, _ := json.Marshal(org)
	return api.json(http.MethodPut, "/api/v1/organization", string(body), http.StatusOK, "If-Match",
		read.header.Get("ETag"))
}

func systemStatus(api patClient) map[string]any {
	api.t.Helper()
	return api.json(http.MethodGet, "/api/v1/system-status", "", http.StatusOK)
}

func heartbeatResult(api patClient) map[string]any {
	api.t.Helper()
	return systemStatus(api)["outgoing_heartbeat"].(map[string]any)
}

// quiet waits longer than the Leader's poll of the outgoing heartbeat and fails when a ping arrived meanwhile.
func quiet(t *testing.T, before int, what string) {
	t.Helper()
	time.Sleep(leader.OutgoingHeartbeatPoll + 2*time.Second)
	if n := len(pings(t)); n != before {
		t.Fatalf("%s: %d pings, want %d", what, n, before)
	}
}

// waitPing waits until the fake has more than before pings and returns the count.
func waitPing(t *testing.T, before int, what string) int {
	t.Helper()
	var n int
	eventually(t, what, func() bool { n = len(pings(t)); return n > before })
	return n
}

// TestOutgoingHeartbeat covers C-19.FR-7, C-19.AC-7 and C-19.AC-1 on two replicas against the fake receiving endpoint:
// the URL set through updateOrganization is a Secret; only the Leader sends GET once per outgoing_heartbeat.interval
// of the development clock; a 503 is counted, logged, shown in the System status and announced to Admins only, and
// the next interval is the retry; a handover keeps one send per interval; without a Leader nothing is sent and the
// sends resume with one; the Organization's proxy carries the request; a cleared URL sends nothing.
func TestOutgoingHeartbeat(t *testing.T) {
	h := Start(t, FakesInProcess)
	a := h.StartReplica(t, ReplicaOptions{})
	a.waitFor(t, "replica A to lead", func() bool { return a.Metric(t, "muster_leader") == "1" })
	b := h.StartReplica(t, ReplicaOptions{ListenApp: ":8080", ListenIngest: ":8081", ListenInternal: ":8082"})
	api, admin := selfAPI(t, a)
	okSeries := `muster_client_requests_total{client="heartbeat",outcome="ok"}`

	if st := systemStatus(api); fmt.Sprint(st["outgoing_heartbeat"]) !=
		"map[configured:false last_attempt_at:<nil> last_error:<nil> last_error_class:<nil> last_ok:<nil> last_status:<nil>]" {
		t.Fatalf("before a URL: %v", st["outgoing_heartbeat"])
	}
	org := setHeartbeat(api, map[string]any{"url": pingURL, "proxy": map[string]any{"enabled": false}})
	if org["outgoing_heartbeat"].(map[string]any)["url_status"].(map[string]any)["set"] != true {
		t.Fatalf("the URL is not set: %v", org["outgoing_heartbeat"])
	}
	audit := api.do(http.MethodGet, "/api/v1/audit-log?action=organization.updated&limit=1", "")
	if !strings.Contains(string(audit.body), `{"pointer":"/outgoing_heartbeat/url","secret_changed":true}`) ||
		strings.Contains(string(audit.body), "/hook/dev/ping") {
		t.Errorf("the audit entry %s", audit.body)
	}

	n := waitPing(t, 0, "the first ping")
	if got := pings(t); got[0].Method != http.MethodGet {
		t.Errorf("the ping is %s", got[0].Method)
	}
	if a.Metric(t, okSeries) == "" || b.Metric(t, okSeries) != "" {
		t.Errorf("heartbeat requests: Leader %q, other replica %q", a.Metric(t, okSeries), b.Metric(t, okSeries))
	}

	// C-19.AC-7: once per interval of Muster's clock.
	start, first := time.Now(), n
	for range 3 {
		advance(t, a, 60)
		n = waitPing(t, n, "the ping of the next interval")
	}
	if extra := n - first - 3; extra < 0 || extra > int(time.Since(start)/time.Minute) {
		t.Errorf("three intervals sent %d pings", n-first)
	}
	if res := heartbeatResult(api); res["configured"] != true || res["last_ok"] != true || res["last_error"] != nil {
		t.Errorf("after the pings: %v", res)
	}

	// A failure is counted, logged without the URL, shown and announced to Admins only; the next tick retries.
	responderAgent := newAgent(t, a.App)
	localUser(t, admin, "bob@example.org", "bob-password-1234")
	if r := responderAgent.signIn("bob@example.org", "bob-password-1234"); r.status != http.StatusCreated {
		t.Fatalf("bob signs in = %d %s", r.status, r.body)
	}
	adminStream, bobStream := openStream(t, admin), openStream(t, responderAgent)
	if err := h.InProcess.Webhook.SetFault(fakeserver.Fault{Path: "/hook/dev/ping", Status: 503}); err != nil {
		t.Fatal(err)
	}
	advance(t, a, 60)
	eventually(t, "the failed ping", func() bool { return heartbeatResult(api)["last_ok"] == false })
	res := heartbeatResult(api)
	if e, _ := res["last_error"].(string); !strings.Contains(e, "503") || strings.Contains(e, "hook/dev") ||
		res["last_error_class"] != "transient" || res["last_status"] != float64(503) {
		t.Errorf("last error %v, class %v, status %v", res["last_error"], res["last_error_class"], res["last_status"])
	}
	if c := strings.Count(a.Output(), `"event":"outgoing_heartbeat_failed"`); c != 1 ||
		strings.Contains(a.Output(), "/hook/dev/ping") {
		t.Errorf("outgoing_heartbeat_failed logged %d times", c)
	}
	if a.Metric(t, `muster_client_requests_total{client="heartbeat",outcome="transient"}`) != "1" {
		t.Errorf("transient heartbeat requests %q", a.Metric(t, `muster_client_requests_total{client="heartbeat",outcome="transient"}`))
	}
	eventually(t, "the system-status hint", func() bool {
		return slices.ContainsFunc(adminStream.hints(), func(s string) bool { return strings.Contains(s, "system-status") })
	})
	if slices.ContainsFunc(bobStream.hints(), func(s string) bool { return strings.Contains(s, "system-status") }) {
		t.Errorf("a Responder's stream got %v", bobStream.hints())
	}
	h.InProcess.Webhook.ResetFaults()
	advance(t, a, 60)
	eventually(t, "the recovered ping", func() bool { return heartbeatResult(api)["last_ok"] == true })
	a.WaitLogLine(t, "outgoing_heartbeat_recovered")

	// C-19.AC-7: a handover keeps one send per interval.
	a.Stop(t)
	b.waitFor(t, "replica B to lead", func() bool { return b.Metric(t, "muster_leader") == "1" })
	apiB := patClient{t: t, base: b.App, token: api.token}
	time.Sleep(leader.OutgoingHeartbeatPoll + time.Second)
	n = len(pings(t))
	last, _ := time.Parse(time.RFC3339Nano, heartbeatResult(apiB)["last_attempt_at"].(string))
	if wait := 60 - int(readClock(t, b).Now.Sub(last).Seconds()) - 10; wait > 0 {
		advance(t, b, wait)
	}
	quiet(t, n, "within the interval after the handover")
	advance(t, b, 15)
	n = waitPing(t, n, "the ping of the next interval on the new Leader")
	quiet(t, n, "after the ping of the new Leader")

	// C-19.AC-1: with no replica leading nothing is sent; once one leads again the sends resume.
	b.Stop(t)
	conn, err := pgx.Connect(t.Context(), h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	if _, err := conn.Exec(t.Context(), "SELECT pg_advisory_lock($1)", leader.LockKey); err != nil {
		t.Fatal(err)
	}
	a.Start(t)
	time.Sleep(2 * time.Second)
	if got := a.Metric(t, "muster_leader"); got != "0" {
		t.Fatalf("muster_leader %q while the lock is held elsewhere", got)
	}
	advance(t, a, 150)
	eventually(t, "the no_replica_leading notice", func() bool {
		return strings.Contains(string(api.do(http.MethodGet, "/api/v1/system-notices", "").body),
			"no_replica_leading")
	})
	quiet(t, n, "without a Leader")
	if _, err := conn.Exec(t.Context(), "SELECT pg_advisory_unlock($1)", leader.LockKey); err != nil {
		t.Fatal(err)
	}
	a.waitFor(t, "replica A to lead again", func() bool { return a.Metric(t, "muster_leader") == "1" })
	n = waitPing(t, n, "the ping once a replica leads again")

	// The Organization's heartbeat proxy carries the request.
	setHeartbeat(api, map[string]any{"proxy": map[string]any{"enabled": true, "type": "http",
		"address": devmode.HTTPProxyAddr}})
	advance(t, a, 60)
	n = waitPing(t, n, "the ping through the proxy")
	if !slices.Contains(h.InProcess.HTTPProxy.Targets(), devmode.WebhookAddr) {
		t.Errorf("the proxy made %v", h.InProcess.HTTPProxy.Targets())
	}

	// Without a URL nothing is sent.
	setHeartbeat(api, map[string]any{"url": nil, "proxy": map[string]any{"enabled": false}})
	if res := heartbeatResult(api); res["configured"] != false || res["last_ok"] != nil {
		t.Errorf("after clearing the URL: %v", res)
	}
	advance(t, a, 60)
	quiet(t, n, "without a URL")
}

// TestOIDCSecretExpiring covers C-19.FR-5, C-19.AC-5 and C-19.FR-9: the OIDC client secret expiring in 10 days raises
// MusterOIDCSecretExpiring with severity warning and the runbook of the development version, once; 30 days ahead,
// or no date, resolves it.
func TestOIDCSecretExpiring(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	api, _ := selfAPI(t, r)
	setExpiry := func(days int) {
		t.Helper()
		read := api.do(http.MethodGet, "/api/v1/oidc-settings", "")
		var s map[string]any
		decode(t, read, &s)
		for _, k := range []string{"etag", "client_secret_status", "warnings", "updated_at"} {
			delete(s, k)
		}
		if p, ok := s["proxy"].(map[string]any); ok {
			delete(p, "password_status")
		}
		s["client_secret_expires_on"] = nil
		if days != 0 {
			now := readClock(t, r).Now.UTC()
			day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			s["client_secret_expires_on"] = day.AddDate(0, 0, days).Format(time.DateOnly)
		}
		body, _ := json.Marshal(s)
		api.json(http.MethodPut, "/api/v1/oidc-settings", string(body), http.StatusOK, "If-Match",
			read.header.Get("ETag"))
	}
	var builtin string
	for _, item := range api.json(http.MethodGet, "/api/v1/integrations", "", http.StatusOK)["items"].([]any) {
		if in := item.(map[string]any); in["builtin"] == true {
			builtin = in["id"].(string)
		}
	}
	type alert struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	}
	firing := func() []alert {
		t.Helper()
		var page struct {
			Items []alert `json:"items"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/integrations/"+builtin+"/alerts?state=firing", ""), &page)
		var out []alert
		for _, a := range page.Items {
			if a.Labels["alertname"] == "MusterOIDCSecretExpiring" {
				out = append(out, a)
			}
		}
		return out
	}
	logged := func() int { return strings.Count(r.Output(), `"event":"oidc_secret_expiring"`) }

	setExpiry(10)
	advance(t, r, 300)
	eventually(t, "MusterOIDCSecretExpiring", func() bool { return len(firing()) == 1 })
	a := firing()[0]
	if a.Labels["severity"] != "warning" || len(a.Labels) != 2 || a.Annotations["runbook_url"] !=
		"https://muster-io.github.io/muster/latest/operations/runbooks/MusterOIDCSecretExpiring/" {
		t.Errorf("raised %+v", a)
	}
	advance(t, r, 300)
	time.Sleep(time.Second)
	if logged() != 1 || len(firing()) != 1 {
		t.Errorf("a second check: %d lines, %d firing", logged(), len(firing()))
	}
	setExpiry(30)
	advance(t, r, 300)
	eventually(t, "MusterOIDCSecretExpiring to resolve", func() bool { return len(firing()) == 0 })
	setExpiry(10)
	advance(t, r, 300)
	eventually(t, "MusterOIDCSecretExpiring again", func() bool { return len(firing()) == 1 })
	setExpiry(0)
	advance(t, r, 300)
	eventually(t, "MusterOIDCSecretExpiring to resolve without a date", func() bool { return len(firing()) == 0 })
	if logged() != 2 {
		t.Errorf("oidc_secret_expiring logged %d times, want 2", logged())
	}
}

// metricLabelRe reads the label names of a series line.
var metricLabelRe = regexp.MustCompile(`([a-z_]+)="`)

// allowedMetricLabels is the label policy of C-19.AC-3: the entity ids, the closed sets, the name of the *_info
// gauges, the version and commit of the build and le.
var allowedMetricLabels = []string{"integration", "route", "destination", "client", "outcome", "state",
	"route_pattern", "method", "code", "kind", "reason", "status", "by", "command", "transport", "template", "table",
	"name", "version", "commit", "le"}

// TestSystemStatus covers C-19.FR-10, C-19.AC-6, C-19.AC-2 and C-19.AC-3 on two replicas: a Broken Mattermost
// Destination raises MusterDestinationBroken, which reaches the Destination of the internal_alerts suggestion, and the
// System status shows it with its reason and its queue; a lost Heartbeat, a truncated Snapshot, a failing Route
// template and a Storm each appear; the replicas show the Leader with its key ids; a recovery after a simulated
// downtime is active with its period; a Responder gets 403 and nothing can be changed; every label exported on
// /metrics follows the label policy.
func TestSystemStatus(t *testing.T) {
	h := Start(t, FakesInProcess)
	a := h.StartReplica(t, ReplicaOptions{})
	a.waitFor(t, "replica A to lead", func() bool { return a.Metric(t, "muster_leader") == "1" })
	b := h.StartReplica(t, ReplicaOptions{ListenApp: ":8080", ListenIngest: ":8081", ListenInternal: ":8082"})
	e := newMM(t, h, a)
	api := e.api
	conn := e.connection(50)
	alerts := e.destination("alerts", conn, fakemattermost.ChannelAlerts, nil, 50)
	ops := e.destination("ops", conn, fakemattermost.ChannelAlertsProd, nil, 50)
	if r := api.do(http.MethodPost, "/api/v1/route-suggestions/internal_alerts/accept",
		`{"destination_ids":["`+ops+`"]}`); r.status/100 != 2 {
		t.Fatalf("accept = %d %s", r.status, r.body)
	}
	byName := func(section, key, name string) map[string]any {
		for _, item := range systemStatus(api)[section].([]any) {
			m := item.(map[string]any)
			if m[key].(map[string]any)["name"] == name {
				return m
			}
		}
		return nil
	}

	// C-19.AC-2: a Broken Destination reaches the admin Destination; the System status shows it and its queue.
	e.route("broken", "broken", []string{alerts}, nil)
	e.fake(http.MethodDelete, e.fmm+"/channels/"+fakemattermost.ChannelAlerts+"/members/"+fakemattermost.BotUserID, "")
	e.group("b1", "Broken")
	e.alert("b1", "b1", `{"team":"broken","cluster":"b1"}`, "")
	e.notify("b1", "first notification")
	eventually(t, "MusterDestinationBroken in the admin channel", func() bool {
		for _, p := range e.posts() {
			if p.ChannelID == fakemattermost.ChannelAlertsProd && p.RootID == "" && len(p.Attachments) > 0 &&
				strings.Contains(p.Attachments[0].Title, "MusterDestinationBroken") {
				return true
			}
		}
		return false
	})
	broken := byName("broken_destinations", "destination", "alerts")
	if broken == nil || !strings.Contains(broken["reason"].(string), "403") || broken["since"] == nil {
		t.Errorf("broken destinations %v", systemStatus(api)["broken_destinations"])
	}
	eventually(t, "the queue of the Broken Destination", func() bool {
		q := byName("delivery_queues", "destination", "alerts")
		return q != nil && q["queued"].(float64) >= 1
	})

	// A Storm, a failing Route template and a truncated Snapshot.
	e.route("storm", "storm", []string{ops}, func(p map[string]any) { p["storm_threshold"] = 2 })
	e.group("s1", "Surge")
	for i := 1; i <= 3; i++ {
		e.alert("s1", fmt.Sprint("s", i), fmt.Sprintf(`{"team":"storm","cluster":"s%d"}`, i), "")
	}
	e.notify("s1", "first notification")
	eventually(t, "the active storm", func() bool {
		s := byName("active_storms", "route", "storm")
		return s != nil && s["alert_group_count"].(float64) >= 1 && s["since"] != nil
	})
	e.route("tpl", "tpl", []string{ops}, func(p map[string]any) {
		p["templates"].(map[string]any)["root_message"] = `second alert {{ (index .Alerts 1).Labels.disk }}`
	})
	e.group("t1", "Templated")
	e.alert("t1", "t1", `{"team":"tpl","cluster":"tpl"}`, "")
	e.notify("t1", "first notification")
	eventually(t, "the template error", func() bool {
		te := byName("template_errors", "subject", "tpl")
		return te != nil && te["subject_kind"] == "route" && te["template"] == "root_message" && te["error"] != ""
	})
	e.group("x1", "Truncated")
	e.alert("x1", "x1", `{"team":"none","cluster":"x1"}`, "")
	e.alert("x1", "x2", `{"team":"none","cluster":"x2"}`, "")
	if r := call(t, http.MethodPost, e.fam+"/groups/x1/notify", `{"reason":"first notification","max_alerts":1}`); r.status !=
		http.StatusOK {
		t.Fatalf("notify = %d %s", r.status, r.body)
	}
	eventually(t, "the truncated snapshot", func() bool {
		i := byName("integrations_attention", "integration", "lab")
		return i != nil && i["issue"] == "snapshot_truncated" && i["since"] != nil
	})

	if st := systemStatus(api); st["version"] == "" || st["recovery"].(map[string]any)["active"] != false {
		t.Errorf("version %v, recovery %v", st["version"], st["recovery"])
	}

	// A lost Heartbeat.
	hbIn := api.json(http.MethodPost, "/api/v1/integrations", `{"name":"hb","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":true}}`, http.StatusCreated)
	hbToken := api.json(http.MethodPost, "/api/v1/integrations/"+hbIn["id"].(string)+"/tokens", `{"name":"hb"}`,
		http.StatusCreated)["value"].(string)
	if s := signal(t, http.MethodGet, a.Ingest+"/api/v1/heartbeat", hbToken, ""); s != http.StatusNoContent {
		t.Fatalf("signal = %d", s)
	}
	advance(t, a, 360)
	eventually(t, "the lost heartbeat", func() bool {
		i := byName("integrations_attention", "integration", "hb")
		return i != nil && i["issue"] == "heartbeat_lost" && i["since"] != nil
	})

	// The replicas, with the Leader and its key ids, after the development clock jumped past leader.absence_notice:
	// the jump wakes the alive mark, so the Leader stays flagged.
	eventually(t, "the Leader in the replicas", func() bool {
		replicas := systemStatus(api)["replicas"].([]any)
		leaders := 0
		for _, r := range replicas {
			m := r.(map[string]any)
			if m["leader"] == true && len(m["key_ids"].([]any)) > 0 {
				leaders++
			}
		}
		return len(replicas) == 2 && leaders == 1
	})

	// C-19.AC-6: a Responder gets 403, and nothing changes the System status.
	localUser(t, newAgentSignedIn(t, a), "bob@example.org", "bob-password-1234")
	bob := newAgent(t, a.App)
	if r := bob.signIn("bob@example.org", "bob-password-1234"); r.status != http.StatusCreated {
		t.Fatalf("bob signs in = %d %s", r.status, r.body)
	}
	if r := bob.do(http.MethodGet, "/api/v1/system-status", ""); r.status != http.StatusForbidden {
		t.Errorf("a Responder = %d %s", r.status, r.body)
	}
	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		if r := api.do(method, "/api/v1/system-status", "{}"); r.status != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d", method, r.status)
		}
	}

	// C-19.AC-3 live: every label of every muster_ series on both replicas follows the label policy.
	for _, r := range []*Replica{a, b} {
		scrape := call(t, http.MethodGet, r.Internal+"/metrics", "")
		for line := range strings.Lines(string(scrape.body)) {
			if !strings.HasPrefix(line, "muster_") {
				continue
			}
			for _, m := range metricLabelRe.FindAllStringSubmatch(line, -1) {
				if !slices.Contains(allowedMetricLabels, m[1]) {
					t.Errorf("%s exports %s", r.Internal, strings.TrimSpace(line))
				}
			}
		}
	}

	// The recovery state after a simulated downtime: both replicas stop, the alive mark moves 10 minutes back.
	a.Stop(t)
	b.Stop(t)
	h.exec(t, "UPDATE runtime_state SET alive_at = alive_at - interval '10 minutes'")
	a.Start(t)
	b.Start(t)
	eventually(t, "the recovery state", func() bool {
		rec := systemStatus(api)["recovery"].(map[string]any)
		return rec["active"] == true && rec["period_from"] != nil && rec["period_to"] != nil && rec["until"] != nil
	})
}

// newAgentSignedIn is the Admin's session on r.
func newAgentSignedIn(t *testing.T, r *Replica) *agent {
	t.Helper()
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	return admin
}
