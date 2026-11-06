// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/devmode"
)

// TestGroupKeyPreview is S-026 against `muster dev`, with E2E_REPLICAS=2 on two replicas: the Group key preview of
// an unsaved and of a saved Route over Snapshots the fake Alertmanager sent, a missing cluster label grouping with the
// others missing it (C-08.AC-2, AC-12), its refusals, and the heartbeat_lost suggestion: offered while only the
// Default route takes MusterHeartbeatLost, accepted at the top of the list once (C-08.AC-8, AC-11), dismissed per
// user and refused to a Service account (C-08.FR-11).
func TestGroupKeyPreview(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"pv","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	token := admin.json(http.MethodPost, "/api/v1/integrations/"+in["id"].(string)+"/tokens", `{"name":"pv"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"pv","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		token+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	put := func(path, body string) {
		t.Helper()
		if a := call(t, http.MethodPut, fam+path, body); a.status/100 != 2 {
			t.Fatalf("PUT %s = %d %s", path, a.status, a.body)
		}
	}
	put("/groups/k1", `{"receiver":"pv","route":"{}","labels":{"alertname":"Disk"}}`)
	for _, x := range []string{"a1", "a2"} {
		put("/groups/k1/alerts/"+x, `{"labels":{"cluster":"a","node":"`+x+`"}}`)
	}
	for _, x := range []string{"m1", "m2"} {
		put("/groups/k1/alerts/"+x, `{"labels":{"node":"`+x+`"}}`)
	}
	before := h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`)
	if a := call(t, http.MethodPost, fam+"/groups/k1/notify", `{"reason":"first notification"}`); a.status !=
		http.StatusOK {
		t.Fatalf("notify = %d %s", a.status, a.body)
	}
	eventually(t, "the snapshot to be processed", func() bool {
		return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`) > before
	})

	type side struct {
		Count    int `json:"alert_group_count"`
		Examples []struct {
			Values map[string]string `json:"group_key_values"`
			Count  int               `json:"alert_count"`
		} `json:"examples"`
	}
	type preview struct {
		Period    int   `json:"period_seconds"`
		Truncated bool  `json:"truncated"`
		Current   *side `json:"current"`
		Proposed  side  `json:"proposed"`
	}
	clusters := func(s side) string {
		var out []string
		for _, e := range s.Examples {
			v, ok := e.Values["cluster"]
			if !ok {
				v = "<absent>"
			}
			out = append(out, v+"="+strconv.Itoa(e.Count))
		}
		return strings.Join(out, " ")
	}

	// C-08.AC-12 and C-08.AC-2: an unsaved Route shows the proposed side alone.
	var p preview
	a := admin.do(http.MethodPost, "/api/v1/group-key-previews",
		`{"matchers":[{"label":"alertname","op":"=","value":"Disk"}],"proposed_group_key":["alertname","cluster"]}`)
	decode(t, a, &p)
	if a.status != http.StatusOK || p.Current != nil || p.Proposed.Count != 2 || clusters(p.Proposed) != "=2 a=2" ||
		p.Truncated || p.Period != 86400 {
		t.Errorf("unsaved = %d %s", a.status, a.body)
	}
	if line, _ := r.WaitLogLine(t, "group_key_previewed"); line["route"] != "" || line["snapshots_read"] != 1.0 {
		t.Errorf("log line %v", line)
	}

	var profiles struct {
		Items []struct {
			ID     string          `json:"id"`
			Policy json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, admin.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	policy := string(profiles.Items[0].Policy)
	disk := admin.json(http.MethodPost, "/api/v1/routes", `{"name":"disk","matchers":[{"label":"alertname","op":"=",
		"value":"Disk"}],"urgent":false,"group_key":["alertname"],"destination_ids":[],"policy":`+policy+`}`,
		http.StatusCreated)["id"].(string)
	p = preview{}
	a = admin.do(http.MethodPost, "/api/v1/group-key-previews",
		`{"route_id":"`+disk+`","proposed_group_key":["alertname","cluster"]}`)
	decode(t, a, &p)
	if a.status != http.StatusOK || p.Current == nil || p.Current.Count != 1 || p.Proposed.Count != 2 ||
		p.Period != 86400 || p.Truncated {
		t.Errorf("saved = %d %s", a.status, a.body)
	}
	if a = admin.do(http.MethodPost, "/api/v1/group-key-previews", `{"proposed_group_key":["alertname"]}`); a.status !=
		http.StatusUnprocessableEntity || errorCode(t, a) != "one_of_required" {
		t.Errorf("neither = %d %s", a.status, a.body)
	}
	if a = admin.do(http.MethodPost, "/api/v1/group-key-previews", `{"route_id":"`+disk+
		`","proposed_group_key":[],"period_seconds":2000000}`); a.status != http.StatusUnprocessableEntity ||
		errorCode(t, a) != "out_of_range" {
		t.Errorf("past the retention = %d %s", a.status, a.body)
	}

	// C-08.AC-8 and C-08.AC-11: an Integration with its Heartbeat on, MusterHeartbeatLost only for the Default route.
	admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"hb","connection_mode":"webhook_only",
		"static_labels":{"env":"prod"},"duplicate_window_seconds":45,"heartbeat":{"enabled":true}}`, http.StatusCreated)
	type suggestion struct {
		ID    string `json:"id"`
		Route struct {
			Name            string `json:"name"`
			MinimumSeverity string `json:"minimum_severity"`
			Urgent          bool   `json:"urgent"`
			Matchers        []struct {
				Label, Op, Value string
			} `json:"matchers"`
			GroupKey []string `json:"group_key"`
		} `json:"route"`
	}
	suggestions := func(ag *agent) []suggestion {
		t.Helper()
		var list struct {
			Items []suggestion `json:"items"`
		}
		a := ag.do(http.MethodGet, "/api/v1/route-suggestions", "")
		if a.status != http.StatusOK {
			t.Fatalf("suggestions = %d %s", a.status, a.body)
		}
		decode(t, a, &list)
		return list.Items
	}
	got := suggestions(admin)
	if len(got) != 1 || got[0].ID != "heartbeat_lost" || got[0].Route.Name != "Muster: Heartbeat lost" ||
		got[0].Route.MinimumSeverity != "critical" || !got[0].Route.Urgent || len(got[0].Route.Matchers) != 1 ||
		got[0].Route.Matchers[0].Value != "MusterHeartbeatLost" ||
		strings.Join(got[0].Route.GroupKey, ",") != "alertname,integration" {
		t.Errorf("suggestions %+v", got)
	}
	_, etag := routeList(t, admin)
	a = admin.do(http.MethodPost, "/api/v1/route-suggestions/heartbeat_lost/accept", "")
	var accepted struct {
		Name            string `json:"name"`
		MinimumSeverity string `json:"minimum_severity"`
		Urgent          bool   `json:"urgent"`
	}
	decode(t, a, &accepted)
	if a.status != http.StatusCreated || accepted.Name != "Muster: Heartbeat lost" || a.header.Get("ETag") == "" ||
		accepted.MinimumSeverity != "critical" || !accepted.Urgent {
		t.Errorf("accept = %d %s", a.status, a.body)
	}
	names, after := routeList(t, admin)
	if names[0] != "Muster: Heartbeat lost" || strings.Join(names, ",") != "Muster: Heartbeat lost,disk,Default" ||
		after == etag {
		t.Errorf("after accepting %v %s %s", names, etag, after)
	}
	for _, rep := range h.Replicas {
		other := newAgent(t, rep.App)
		other.signIn(devmode.AdminEmail, devmode.AdminPassword)
		if got := suggestions(other); len(got) != 0 {
			t.Errorf("suggestions on %s after accepting %+v", rep.App, got)
		}
	}
	if a = admin.do(http.MethodPost, "/api/v1/route-suggestions/heartbeat_lost/accept", ""); a.status !=
		http.StatusConflict || codeOf(t, a) != "suggestion_obsolete" {
		t.Errorf("accept twice = %d %s", a.status, a.body)
	}
	if a = admin.do(http.MethodPost, "/api/v1/route-suggestions/internal_alerts/accept", `{"destination_ids":[]}`); a.status !=
		http.StatusConflict || codeOf(t, a) != "suggestion_obsolete" {
		t.Errorf("internal_alerts = %d %s", a.status, a.body)
	}

	// Dismissal per user: the Route deleted again, the Admin dismisses, the Responder still sees it.
	hl := admin.json(http.MethodGet, "/api/v1/routes", "", http.StatusOK)["items"].([]any)[0].(map[string]any)["id"].(string)
	admin.json(http.MethodDelete, "/api/v1/routes/"+hl, "", http.StatusNoContent)
	localUser(t, admin, "resp", "responder-password-1")
	resp := newAgent(t, r.App)
	if a := resp.signIn("resp", "responder-password-1"); a.status != http.StatusCreated {
		t.Fatalf("responder sign in = %d %s", a.status, a.body)
	}
	if a = admin.do(http.MethodPost, "/api/v1/route-suggestions/heartbeat_lost/dismiss", ""); a.status !=
		http.StatusNoContent {
		t.Errorf("dismiss = %d %s", a.status, a.body)
	}
	if got := suggestions(admin); len(got) != 0 {
		t.Errorf("the Admin after dismissing %+v", got)
	}
	if got := suggestions(resp); len(got) != 1 {
		t.Errorf("the Responder %+v", got)
	}
	if a = resp.do(http.MethodPost, "/api/v1/route-suggestions/heartbeat_lost/accept", ""); a.status !=
		http.StatusForbidden {
		t.Errorf("accept by the Responder = %d", a.status)
	}
	if a = resp.do(http.MethodPost, "/api/v1/route-suggestions/heartbeat_lost/dismiss", ""); a.status !=
		http.StatusNoContent {
		t.Errorf("dismiss by the Responder = %d %s", a.status, a.body)
	}
	if got := suggestions(resp); len(got) != 0 {
		t.Errorf("the Responder after dismissing %+v", got)
	}
	sa := admin.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"terraform","role":"admin"}`,
		http.StatusCreated)
	sat := admin.json(http.MethodPost, "/api/v1/service-accounts/"+sa["id"].(string)+"/tokens", `{"name":"ci"}`,
		http.StatusCreated)["value"].(string)
	if a = bearerPost(t, r.App+"/api/v1/route-suggestions/heartbeat_lost/dismiss", sat); a.status !=
		http.StatusForbidden || codeOf(t, a) != "service_account_not_allowed" {
		t.Errorf("dismiss by a service account = %d %s", a.status, a.body)
	}
	if a = bearerGet(t, r.App+"/api/v1/route-suggestions", sat); a.status != http.StatusOK ||
		!strings.Contains(string(a.body), `"heartbeat_lost"`) {
		t.Errorf("suggestions for a service account = %d %s", a.status, a.body)
	}
}

// errorCode is the code of the first error of a validation problem.
func errorCode(t *testing.T, a answer) string {
	t.Helper()
	var p struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	decode(t, a, &p)
	if len(p.Errors) == 0 {
		return ""
	}
	return p.Errors[0].Code
}

// routeList is the names of the Routes in evaluation order with the list ETag.
func routeList(t *testing.T, ag *agent) ([]string, string) {
	t.Helper()
	a := ag.do(http.MethodGet, "/api/v1/routes", "")
	var list struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	decode(t, a, &list)
	var out []string
	for _, r := range list.Items {
		out = append(out, r.Name)
	}
	return out, a.header.Get("ETag")
}

// bearerPost sends POST url without a body with the bearer token value.
func bearerPost(t *testing.T, url, value string) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+value)
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{status: resp.StatusCode, header: resp.Header, body: b}
}

// deliveryPreview is the answer of previewGroupKey for the preview "Where alerts go".
type deliveryPreview struct {
	Delivery *struct {
		AlertGroups []struct {
			Values map[string]string `json:"group_key_values"`
			Count  int               `json:"alert_count"`
			Level  string            `json:"severity_level"`
			Teams  []struct {
				Slug string `json:"slug"`
			} `json:"teams"`
			Destinations []struct {
				Destination struct {
					Name string `json:"name"`
				} `json:"destination"`
				Entries []struct {
					Kind string `json:"kind"`
				} `json:"entries"`
			} `json:"destinations"`
		} `json:"alert_groups"`
		Counts []struct {
			Destination struct {
				Name string `json:"name"`
			} `json:"destination"`
			Count int `json:"alert_group_count"`
		} `json:"destination_counts"`
		SharedKeyWarning bool `json:"shared_key_warning"`
	} `json:"delivery"`
}

// lines are the Alert Groups of the preview as team slugs>level>destination:kinds, sorted.
func (p deliveryPreview) lines() []string {
	out := []string{}
	for _, g := range p.Delivery.AlertGroups {
		var teams, dests []string
		for _, t := range g.Teams {
			teams = append(teams, t.Slug)
		}
		for _, d := range g.Destinations {
			var kinds []string
			for _, e := range d.Entries {
				kinds = append(kinds, e.Kind)
			}
			dests = append(dests, d.Destination.Name+":"+strings.Join(kinds, "+"))
		}
		out = append(out, strings.Join(teams, ",")+">"+g.Level+">"+strings.Join(dests, ","))
	}
	slices.Sort(out)
	return out
}

// TestRoutingPreviewDelivery is S-122 against `muster dev`, as its Verification: with the Route "Infrastructure" of
// S-121 and Alerts of payments (warning) and devops (critical) in the period, the settings of the Route sent unsaved
// list the Alert Group of payments in infra-notice (the Route) and payments-notice (the Team) and that of devops in
// infra-critical alone; without "Also to the Teams from the label" the first reaches infra-notice only; a Group key
// without the Team label warns while the option is on and does not otherwise; and the preview changes no Route
// (C-26.FR-12).
func TestRoutingPreviewDelivery(t *testing.T) {
	h := Start(t, DevProcess)
	e := newMM(t, h, h.Replicas[0])
	rt := &routingTeams{mm: e, dests: map[string]string{}}
	conn := e.connection(200)
	for _, name := range []string{"infra-critical", "infra-notice", "payments-notice"} {
		rt.dests[name] = e.destination(name, conn, "ch-"+name, map[string]string{}, 50)
	}
	var profiles struct {
		Items []struct {
			ID     string         `json:"id"`
			Policy map[string]any `json:"policy"`
		} `json:"items"`
	}
	decode(t, e.api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	rt.policy = profiles.Items[0].Policy
	rt.team("payments", rt.entries("payments-notice", "below_critical"))
	infra := rt.route("Infrastructure", "infra", []string{"alertname", "system", "team"}, true, "",
		rt.entries("infra-critical", "critical", "infra-notice", "below_critical"), nil)["id"].(string)

	e.group("w1", "Disk")
	e.alert("w1", "a", `{"category":"infra","system":"k8s","team":"payments","severity":"warning"}`, "")
	e.alert("w1", "b", `{"category":"infra","system":"k8s","team":"devops","severity":"critical"}`, "")
	e.notify("w1", "first notification")

	routes := func() string {
		return fmt.Sprint(h.count(t, `SELECT count(*) FROM routes`), "/",
			h.count(t, `SELECT coalesce(sum(version), 0) FROM routes`))
	}
	audit := h.count(t, `SELECT count(*) FROM audit_log WHERE action LIKE 'route.%'`)
	before := routes()
	preview := func(key string, toTeams bool) deliveryPreview {
		t.Helper()
		var p deliveryPreview
		a := e.api.do(http.MethodPost, "/api/v1/group-key-previews", fmt.Sprintf(`{"route_id":"%s","matchers":[{"label":"category",
			"op":"=","value":"infra"}],"proposed_group_key":%s,"deliver_to_teams":%v,"destination_entries":[
			{"destination_id":"%s","severity_condition":"critical"},
			{"destination_id":"%s","severity_condition":"below_critical"}]}`, infra, key, toTeams,
			rt.dests["infra-critical"], rt.dests["infra-notice"]))
		decode(t, a, &p)
		if a.status != http.StatusOK || p.Delivery == nil {
			t.Fatalf("preview = %d %s", a.status, a.body)
		}
		return p
	}
	p := preview(`["alertname","system","team"]`, true)
	if got, want := p.lines(), []string{">critical>infra-critical:route",
		"payments>warning>infra-notice:route,payments-notice:team"}; !slices.Equal(got, want) {
		t.Errorf("deliver to Teams: %q, want %q", got, want)
	}
	counts := map[string]int{}
	for _, c := range p.Delivery.Counts {
		counts[c.Destination.Name] = c.Count
	}
	if p.Delivery.SharedKeyWarning || counts["infra-critical"] != 1 || counts["infra-notice"] != 1 ||
		counts["payments-notice"] != 1 || len(counts) != 3 {
		t.Errorf("counts %v, warning %v", counts, p.Delivery.SharedKeyWarning)
	}
	if got, want := preview(`["alertname","system","team"]`, false).lines(), []string{">critical>infra-critical:route",
		"payments>warning>infra-notice:route"}; !slices.Equal(got, want) {
		t.Errorf("without deliver_to_teams: %q, want %q", got, want)
	}
	if !preview(`["alertname","system"]`, true).Delivery.SharedKeyWarning {
		t.Error("no shared_key_warning with the Team label missing from the Group key")
	}
	if preview(`["alertname","system"]`, false).Delivery.SharedKeyWarning {
		t.Error("shared_key_warning without deliver_to_teams")
	}
	// The saved Route's own settings answer when none are sent.
	var saved deliveryPreview
	decode(t, e.api.do(http.MethodPost, "/api/v1/group-key-previews",
		`{"route_id":"`+infra+`","proposed_group_key":["alertname","system","team"]}`), &saved)
	if got, want := saved.lines(), []string{">critical>infra-critical:route",
		"payments>warning>infra-notice:route,payments-notice:team"}; !slices.Equal(got, want) {
		t.Errorf("saved Route: %q, want %q", got, want)
	}
	if after := routes(); after != before || h.count(t, `SELECT count(*) FROM audit_log WHERE action LIKE 'route.%'`) != audit {
		t.Errorf("the previews changed the Routes: %s, now %s", before, after)
	}
}
