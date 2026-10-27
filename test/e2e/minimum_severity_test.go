// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/muster-io/muster/internal/fakes/fakemattermost"
)

// TestMinimumSeverity is S-111 against `muster dev`: the Route profiles set no Minimum severity; a Route created with
// the Minimum severity critical and without urgent reads urgent, stores routes.urgent and keeps both when an update
// sends urgent false; "critical is Urgent" reads true whatever an update sends (C-26.FR-10, C-26.AC-8). A warning
// Alert of that Route starts a critical, Urgent Alert Group while the Alert reads warning in the Alerts views and in
// the created webhook event (C-26.FR-8, C-26.AC-7); a plain Route makes a critical Alert Group Urgent and a warning one
// not. Raising the Minimum severity of a Route with an acknowledged warning Alert Group records the Quiet
// urgency_raised with the reason minimum_severity, keeps the acknowledgement and its Owner, edits the Root message
// without a Thread reply and empties open_group_updates; raising it to warning records the Quiet severity_raised on an
// info Alert Group; lowering it records nothing (C-26.FR-9, C-26.AC-11, C-09.AC-8).
func TestMinimumSeverity(t *testing.T) {
	h := Start(t, DevProcess)
	e := newMM(t, h, h.Replicas[0])
	api := e.api

	var profiles struct {
		Items []struct {
			ID              string          `json:"id"`
			MinimumSeverity string          `json:"minimum_severity"`
			Urgent          bool            `json:"urgent"`
			Policy          json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	for _, p := range profiles.Items {
		if p.MinimumSeverity != "none" || p.Urgent {
			t.Errorf("profile %s: %s, urgent %v", p.ID, p.MinimumSeverity, p.Urgent)
		}
	}
	policy := string(profiles.Items[0].Policy)
	route := func(name, minimum string, destinations ...string) string {
		t.Helper()
		ids, _ := json.Marshal(append([]string{}, destinations...))
		return api.json(http.MethodPost, "/api/v1/routes", `{"name":"`+name+`","matchers":[{"label":"team","op":"=",`+
			`"value":"`+name+`"}],"minimum_severity":"`+minimum+`","group_key":["alertname","cluster"],`+
			`"destination_ids":`+string(ids)+`,"policy":`+policy+`}`, http.StatusCreated)["id"].(string)
	}
	type routeView struct {
		MinimumSeverity string `json:"minimum_severity"`
		Urgent          bool   `json:"urgent"`
	}
	readRoute := func(id string) (routeView, map[string]any, string) {
		t.Helper()
		a := api.do(http.MethodGet, "/api/v1/routes/"+id, "")
		var v routeView
		var full map[string]any
		decode(t, a, &v)
		decode(t, a, &full)
		return v, full, a.header.Get("ETag")
	}
	update := func(id string, change func(body map[string]any)) {
		t.Helper()
		_, full, etag := readRoute(id)
		body := map[string]any{"name": full["name"], "matchers": full["matchers"], "group_key": full["group_key"],
			"destination_ids": full["destination_ids"], "policy": full["policy"]}
		change(body)
		b, _ := json.Marshal(body)
		if a := api.do(http.MethodPut, "/api/v1/routes/"+id, string(b), "If-Match", etag); a.status != http.StatusOK {
			t.Fatalf("update %s = %d %s", id, a.status, a.body)
		}
	}

	// C-26.FR-10, C-26.AC-8: urgent follows the Minimum severity and is ignored in requests.
	hook := e.webhook("sev-events", "sev", `[]`)
	hb := route("hb", "critical", hook["id"].(string))
	if v, _, _ := readRoute(hb); v != (routeView{"critical", true}) ||
		h.count(t, `SELECT count(*) FROM routes WHERE name = 'hb' AND urgent`) != 1 {
		t.Errorf("created %+v", v)
	}
	update(hb, func(b map[string]any) { b["urgent"] = false })
	if v, _, _ := readRoute(hb); v != (routeView{"critical", true}) {
		t.Errorf("after urgent false %+v", v)
	}
	for _, change := range []func(m map[string]any){
		func(m map[string]any) { m["critical_is_urgent"] = false },
		func(m map[string]any) { delete(m, "critical_is_urgent") },
	} {
		a := putOrganization(api, change)
		var got map[string]any
		decode(t, a, &got)
		if a.status != http.StatusOK || got["critical_is_urgent"] != true {
			t.Errorf("organization update = %d %s", a.status, a.body)
		}
	}
	if org := api.json(http.MethodGet, "/api/v1/organization", "", http.StatusOK); org["critical_is_urgent"] != true {
		t.Errorf("organization %v", org["critical_is_urgent"])
	}

	// C-26.AC-7: a warning Alert is critical and Urgent in its Alert Group, warning on its own.
	e.group("hb1", "Lag")
	e.alert("hb1", "a", `{"team":"hb","cluster":"hb1","severity":"warning"}`, "")
	e.notify("hb1", "first notification")
	g := e.groupOf("hb1")
	if v := e.alertGroup(g); v["severity_level"] != "critical" || v["urgent"] != true {
		t.Errorf("hb1 = %v %v", v["severity_level"], v["urgent"])
	}
	var integrationAlerts struct {
		Items []struct {
			SeverityLevel string `json:"severity_level"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/integrations/"+e.integration+"/alerts?"+
		url.Values{"label": {`cluster="hb1"`}}.Encode(), ""), &integrationAlerts)
	var groupAlerts struct {
		Items []struct {
			Labels map[string]string `json:"labels"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/alert-groups/"+g+"/alerts", ""), &groupAlerts)
	if len(integrationAlerts.Items) != 1 || integrationAlerts.Items[0].SeverityLevel != "warning" ||
		len(groupAlerts.Items) != 1 || groupAlerts.Items[0].Labels["severity"] != "warning" {
		t.Errorf("the alert: %+v %+v", integrationAlerts, groupAlerts)
	}
	var created map[string]any
	eventually(t, "the created event", func() bool {
		var got []struct {
			Body map[string]any `json:"body"`
		}
		decode(t, call(t, http.MethodGet, webhookBase+"/_fake/received/sev", ""), &got)
		for _, x := range got {
			if x.Body["event"] == "created" {
				created = x.Body
			}
		}
		return created != nil
	})
	group := created["alert_group"].(map[string]any)
	alerts := created["alerts"].([]any)
	if group["severity_level"] != "critical" || group["urgent"] != true || len(alerts) != 1 ||
		alerts[0].(map[string]any)["labels"].(map[string]any)["severity"] != "warning" {
		t.Errorf("created event %v", created)
	}

	// C-26.FR-8: Urgent exactly when critical on a Route without a Minimum severity.
	route("plain", "none")
	e.group("pl", "Plain")
	e.alert("pl", "c", `{"team":"plain","cluster":"pc","severity":"critical"}`, "")
	e.alert("pl", "w", `{"team":"plain","cluster":"pw","severity":"warning"}`, "")
	e.notify("pl", "first notification")
	if c, w := e.alertGroup(e.groupOf("pc")), e.alertGroup(e.groupOf("pw")); c["urgent"] != true ||
		w["urgent"] != false || w["severity_level"] != "warning" {
		t.Errorf("plain route: critical %v, warning %v", c["urgent"], w["urgent"])
	}

	// C-26.FR-9, C-26.AC-11, C-09.AC-8: raising the Minimum severity of a Route with an acknowledged warning Alert
	// Group and an info one.
	conn := e.connection(50)
	dest := e.destination("sev-mm", conn, fakemattermost.ChannelAlertsProd, nil, 50)
	db := route("db", "none", dest)
	e.group("d1", "Slow")
	e.alert("d1", "b", `{"team":"db","cluster":"d1","severity":"warning"}`, "")
	e.alert("d1", "i", `{"team":"db","cluster":"d2","severity":"info"}`, "")
	e.notify("d1", "first notification")
	g2, g3 := e.groupOf("d1"), e.groupOf("d2")
	if e.command(g2, "acknowledge") == "" {
		t.Fatal("acknowledge")
	}
	root := e.root(e.number(g2))
	replies := len(e.replies(root.ID))
	edits := len(e.calls(http.MethodPut, "/api/v4/posts/"+root.ID+"/patch"))
	type entry struct {
		Event      string  `json:"event"`
		Loudness   string  `json:"loudness"`
		ReasonCode *string `json:"reason_code"`
		Reason     *string `json:"reason"`
		Actor      struct {
			Transport string `json:"transport"`
		} `json:"actor"`
	}
	last := func(id string) entry {
		t.Helper()
		var page struct {
			Items []entry `json:"items"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/alert-groups/"+id+"/timeline?limit=1&kind=status&kind=alerts", ""),
			&page)
		if len(page.Items) == 0 {
			t.Fatalf("no timeline entries of %s", id)
		}
		return page.Items[0]
	}
	settled := func(what string) {
		t.Helper()
		eventually(t, what, func() bool { return h.count(t, `SELECT count(*) FROM open_group_updates`) == 0 })
	}
	update(db, func(b map[string]any) { b["minimum_severity"] = "critical" })
	settled("the raise to critical to reach the open alert groups")
	v := e.alertGroup(g2)
	owner, _ := v["owner"].(map[string]any)
	if v["status"] != "acknowledged" || v["severity_level"] != "critical" || v["urgent"] != true || owner == nil ||
		owner["name"] == nil {
		t.Errorf("g2 after the raise = %v", v)
	}
	if x := last(g2); x.Event != "urgency_raised" || x.Loudness != "quiet" || x.ReasonCode == nil ||
		*x.ReasonCode != "minimum_severity" || x.Reason == nil || *x.Reason != "Minimum severity of the Route" ||
		x.Actor.Transport != "system" {
		t.Errorf("g2 entry %+v", x)
	}
	eventually(t, "the root message to be edited", func() bool {
		return len(e.calls(http.MethodPut, "/api/v4/posts/"+root.ID+"/patch")) > edits
	})
	if got := len(e.replies(root.ID)); got != replies {
		t.Errorf("%d thread replies after the raise, want %d", got, replies)
	}

	// Lowered to none: both fall to the level of their Alerts without an entry, g2 still acknowledged.
	update(db, func(b map[string]any) { b["minimum_severity"] = "none" })
	settled("the change to none")
	if v := e.alertGroup(g2); v["severity_level"] != "warning" || v["urgent"] != false || v["status"] != "acknowledged" ||
		last(g2).Event != "urgency_raised" {
		t.Errorf("g2 after the fall = %v %v", v["severity_level"], last(g2))
	}
	if v := e.alertGroup(g3); v["severity_level"] != "info" || last(g3).Event != "urgency_raised" {
		t.Errorf("g3 after the fall = %v %+v", v["severity_level"], last(g3))
	}
	// Raised to warning: the info Alert Group records the Quiet severity_raised with the reason.
	update(db, func(b map[string]any) { b["minimum_severity"] = "warning" })
	settled("the change to warning")
	if x := last(g3); e.alertGroup(g3)["severity_level"] != "warning" || x.Event != "severity_raised" ||
		x.Loudness != "quiet" || x.ReasonCode == nil || *x.ReasonCode != "minimum_severity" {
		t.Errorf("g3 = %+v", x)
	}
	if n := h.count(t, fmt.Sprintf(`SELECT count(*) FROM routes WHERE public_id = '%s' AND NOT urgent`, db)); n != 1 {
		t.Errorf("routes.urgent of db after warning: %d", n)
	}
}
