// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

// TestScheduleGaps is S-133 against `muster dev`, as its Verification with exact times: a gap 5 days ahead in the main
// Schedule of payments raises MusterScheduleGap with the Team's slug under the Team label, belongs to the Team and
// records no unknown_team_value, and a second scan raises nothing new (C-27.AC-5, C-25.FR-5); an Override closes it; a
// gap 8 days ahead raises nothing, 6 days ahead it raises, a started gap stays and its end resolves; the gaps of a
// Rotation layer of Friday 19:00–09:00 (C-27.FR-7); "On call now" (C-27.FR-9); a second Schedule raises only with
// gap_alert on; the gap alert moves with the main mark unless set on (C-27.AC-8); a renamed Team updates the open
// alert; a deleted Schedule resolves it. The development clock moves past auth.session_idle_timeout, so the checks use
// a Personal access token.
func TestScheduleGaps(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	api := patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"gaps","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)}
	olga, _ := personWithToken(t, admin, r.App, "olga", "responder")
	team := func(name, slug string) string {
		t.Helper()
		id := api.json(http.MethodPost, "/api/v1/teams", `{"name":"`+name+`","slug":"`+slug+`","own_working_hours":false}`,
			http.StatusCreated)["id"].(string)
		api.json(http.MethodPost, "/api/v1/teams/"+id+"/users", `{"user_id":"`+olga+`"}`, http.StatusCreated)
		return id
	}
	tp := team("payments", "payments")

	now := readClock(t, r).Now.UTC()
	today := now.Truncate(24 * time.Hour)
	day := func(n int) time.Time { return today.AddDate(0, 0, n) }
	weekday := func(d time.Time) string { return strings.ToLower(d.Weekday().String()) }
	stamp := func(at time.Time) string { return at.UTC().Format(time.RFC3339) }
	// layer is a Rotation layer of olga on every day but the weekday of left, from today.
	layer := func(left time.Time) string {
		var days []string
		for i := range 7 {
			if d := weekday(day(i)); d != weekday(left) {
				days = append(days, `"`+d+`"`)
			}
		}
		return `[{"name":"Days","user_ids":["` + olga + `"],"shift_length":"day","handover_time":"00:00",` +
			`"start_date":"` + today.Format(time.DateOnly) + `","limits":{"days":[` + strings.Join(days, ",") + `]}}]`
	}
	schedule := func(teamID, name string, left time.Time) map[string]any {
		t.Helper()
		return api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+teamID+`","name":"`+name+
			`","time_zone":"UTC","rotation_layers":`+layer(left)+`}`, http.StatusCreated)
	}
	sc := schedule(tp, "Main", day(5))
	scID := sc["id"].(string)
	got := api.json(http.MethodGet, "/api/v1/schedules/"+scID, "", http.StatusOK)
	if got["gap_alert"] != "by_default" || got["gap_alert_active"] != true ||
		got["next_gap"].(map[string]any)["start"] != stamp(day(5)) {
		t.Fatalf("schedule %v", got)
	}

	// firing counts the firing MusterScheduleGap of the Schedule id.
	firing := func(id string) int64 {
		return h.count(t, `SELECT count(*) FROM alerts WHERE status = 'firing'
			AND labels->>'alertname' = 'MusterScheduleGap' AND labels->>'schedule' = '`+id+`'`)
	}
	raises := func() int64 {
		return h.count(t, `SELECT count(*) FROM stored_snapshots s JOIN snapshot_bodies b
			ON b.org_id = s.org_id AND b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
			WHERE s.source = 'internal' AND convert_from(b.body, 'UTF8')::jsonb->'groupLabels'->>'alertname' = 'MusterScheduleGap'
			AND convert_from(b.body, 'UTF8')::jsonb->'commonLabels'->>'schedule' = '`+scID+`'`)
	}
	gauge := func(id string) string { return r.Metric(t, `muster_schedule_gap{schedule="`+id+`"}`) }
	// canary is a Schedule of payments with its gap alert on, no Rotation layer and an Override from a minute before at
	// for 30 days: a gap until then, none within the horizon from at. Its muster_schedule_gap is 0 only once a scan ran
	// at a business time from a minute before at on, which no scan before the move of the clock to at reaches. The 0
	// proves that a scan computed at the new time, not that its write was processed; the assertion that follows waits.
	canaries := 0
	canary := func(at time.Time) string {
		t.Helper()
		canaries++
		id := api.json(http.MethodPost, "/api/v1/schedules", fmt.Sprintf(`{"team_id":"%s","name":"Canary %d",`+
			`"time_zone":"UTC","gap_alert":"on","rotation_layers":[]}`, tp, canaries), http.StatusCreated)["id"].(string)
		from := at.Add(-time.Minute)
		api.json(http.MethodPost, "/api/v1/schedules/"+id+"/overrides", `{"user_id":"`+olga+`","start":"`+stamp(from)+
			`","end":"`+stamp(from.AddDate(0, 0, 30))+`"}`, http.StatusCreated)
		return id
	}
	// to moves the business clock to at exactly, which wakes the scan, and waits until the Schedule id fires as
	// want; with a want that does not change, it first waits until a scan at the new time ran, by a canary.
	to := func(at time.Time, id string, want int64) {
		t.Helper()
		var probe string
		if firing(id) == want {
			probe = canary(at)
		}
		advance(t, r, int(at.Sub(readClock(t, r).Now).Seconds()))
		if probe != "" {
			eventually(t, "a scan at "+stamp(at), func() bool { return gauge(probe) == "0" })
		}
		eventually(t, fmt.Sprintf("%s firing %d at %s", id, want, stamp(at)), func() bool { return firing(id) == want })
	}
	// tick moves the business clock two minutes on, which wakes the scan.
	tick := func(id string, want int64) {
		t.Helper()
		to(readClock(t, r).Now.Add(2*time.Minute), id, want)
	}

	// C-27.AC-5: raised 5 days ahead, with the Team label.
	tick(scID, 1)
	q := url.Values{"status": {"firing"}, "label": {`alertname="MusterScheduleGap"`}}
	groups := api.json(http.MethodGet, "/api/v1/alert-groups?"+q.Encode(), "", http.StatusOK)["items"].([]any)
	if len(groups) != 1 {
		t.Fatalf("alert groups %v", groups)
	}
	g := groups[0].(map[string]any)["id"].(string)
	alerts := api.json(http.MethodGet, "/api/v1/alert-groups/"+g+"/alerts", "", http.StatusOK)["items"].([]any)
	labels := alerts[0].(map[string]any)["labels"].(map[string]any)
	if len(alerts) != 1 || labels["schedule"] != scID || labels["schedule_name"] != "Main" || labels["team_id"] != tp ||
		labels["team_name"] != "payments" || labels["team"] != "payments" ||
		alerts[0].(map[string]any)["annotations"].(map[string]any)["gap_start"] != stamp(day(5)) {
		t.Fatalf("alerts %v", alerts)
	}
	var slugs []string
	for _, tm := range api.json(http.MethodGet, "/api/v1/alert-groups/"+g, "", http.StatusOK)["teams"].([]any) {
		slugs = append(slugs, tm.(map[string]any)["team"].(map[string]any)["slug"].(string))
	}
	if !slices.Equal(slugs, []string{"payments"}) {
		t.Errorf("teams of the alert group %v", slugs)
	}
	for _, e := range api.json(http.MethodGet, "/api/v1/alert-groups/"+g+"/timeline?limit=50", "",
		http.StatusOK)["items"].([]any) {
		if e.(map[string]any)["event"] == "unknown_team_value" {
			t.Errorf("timeline %v", e)
		}
	}
	if gauge(scID) != "1" {
		t.Errorf("gauge %q", gauge(scID))
	}
	tick(scID, 1)
	if n := raises(); n != 1 {
		t.Errorf("a second scan raised again: %d raises", n)
	}

	// An Override closes the gap: resolved at the next scan; the next gap is 12 days ahead, beyond the horizon.
	override := func(id string, from, until time.Time) {
		t.Helper()
		api.json(http.MethodPost, "/api/v1/schedules/"+id+"/overrides", `{"user_id":"`+olga+`","start":"`+
			stamp(from)+`","end":"`+stamp(until)+`"}`, http.StatusCreated)
	}
	override(scID, day(5), day(6))
	tick(scID, 0)
	if gauge(scID) != "0" {
		t.Errorf("gauge after the Override %q", gauge(scID))
	}
	// The weekday recurs: its next gap is 12 days ahead and the one after 19 days ahead, which an Override closes now.
	override(scID, day(19), day(20))
	g2 := day(12)
	to(g2.AddDate(0, 0, -8).Add(time.Hour), scID, 0) // it starts in 7 days 23 hours
	to(g2.AddDate(0, 0, -6), scID, 1)                // it starts in 6 days
	raised := raises()
	to(g2.Add(2*time.Hour), scID, 1) // inside the gap
	if raises() != raised {
		t.Errorf("a started gap raised again")
	}
	to(g2.AddDate(0, 0, 1).Add(time.Hour), scID, 0) // past it; the next one, 26 days from the start, is 13 days out

	// C-27.FR-2, C-27.FR-7: a Rotation layer of Friday 19:00–09:00 crosses midnight; its gaps are the rest of the week.
	cur := readClock(t, r).Now.UTC().Truncate(24 * time.Hour)
	fri := cur.AddDate(0, 0, (12-((int(cur.Weekday())+6)%7+1))%7+7) // a Friday 7 to 13 days ahead
	sn := api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Nights","time_zone":"UTC",`+
		`"rotation_layers":[{"name":"Friday night","user_ids":["`+olga+`"],"shift_length":"day","handover_time":"00:00",`+
		`"start_date":"`+today.Format(time.DateOnly)+`","limits":{"days":["friday"],"start":"19:00","end":"09:00"}}]}`,
		http.StatusCreated)["id"].(string)
	var oncall struct {
		Warnings []struct {
			Kind       string
			Start, End *string
		} `json:"warnings"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/schedules/"+sn+"/on-call?from="+stamp(fri)+"&to="+
		stamp(fri.AddDate(0, 0, 2)), ""), &oncall)
	var gaps [][2]string
	for _, w := range oncall.Warnings {
		if w.Kind == "gap" {
			gaps = append(gaps, [2]string{*w.Start, *w.End})
		}
	}
	if want := [][2]string{{stamp(fri), stamp(fri.Add(19 * time.Hour))},
		{stamp(fri.Add(33 * time.Hour)), stamp(fri.AddDate(0, 0, 2))}}; !slices.Equal(gaps, want) {
		t.Errorf("Friday night gaps %v, want %v", gaps, want)
	}

	// C-27.FR-9: On call now.
	var now2 struct {
		Items []struct {
			Team       struct{ Slug string }
			Schedule   *struct{ Name string }
			User       *struct{ Login string }
			NoSchedule bool `json:"no_schedule"`
		} `json:"items"`
	}
	tops := team("ops", "ops")
	decode(t, api.do(http.MethodGet, "/api/v1/on-call-now", ""), &now2)
	if len(now2.Items) != 2 || now2.Items[0].Team.Slug != "ops" || !now2.Items[0].NoSchedule ||
		now2.Items[1].Team.Slug != "payments" || now2.Items[1].Schedule.Name != "Main" ||
		now2.Items[1].User.Login != "olga" {
		t.Errorf("on call now %+v", now2)
	}

	// C-27.AC-5: a second Schedule of the Team with a gap raises nothing until its gap alert is on.
	left := readClock(t, r).Now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 3)
	second := schedule(tp, "Second", left)
	secondID := second["id"].(string)
	tick(secondID, 0)
	if gauge(secondID) != "0" || second["gap_alert_active"] != false {
		t.Errorf("second %v, gauge %q", second, gauge(secondID))
	}
	in := `{"team_id":"` + tp + `","name":"Second","rotation_layers":` + layer(left) + `,"gap_alert":"on"}`
	api.json(http.MethodPut, "/api/v1/schedules/"+secondID, in, http.StatusOK, "If-Match", second["etag"].(string))
	tick(secondID, 1)

	// C-27.AC-8: with by_default on two Schedules of ops, the gap alert moves with the main mark; one set on stays.
	a := schedule(tops, "A", left)
	b := schedule(tops, "B", left)
	tick(a["id"].(string), 1)
	if firing(b["id"].(string)) != 0 {
		t.Error("the second Schedule of ops raised by default")
	}
	b = api.json(http.MethodPut, "/api/v1/schedules/"+b["id"].(string), `{"team_id":"`+tops+`","name":"B","main":true,`+
		`"rotation_layers":`+layer(left)+`}`, http.StatusOK, "If-Match", b["etag"].(string))
	tick(b["id"].(string), 1)
	tick(a["id"].(string), 0)
	a = api.json(http.MethodGet, "/api/v1/schedules/"+a["id"].(string), "", http.StatusOK)
	api.json(http.MethodPut, "/api/v1/schedules/"+a["id"].(string), `{"team_id":"`+tops+`","name":"A",`+
		`"gap_alert":"on","rotation_layers":`+layer(left)+`}`, http.StatusOK, "If-Match", a["etag"].(string))
	tick(a["id"].(string), 1)

	// A renamed Team updates the open alert, without a second Alert; a deleted Schedule resolves it.
	ops := api.json(http.MethodGet, "/api/v1/teams/"+tops, "", http.StatusOK)
	api.json(http.MethodPut, "/api/v1/teams/"+tops, `{"name":"Operations","slug":"operations","own_working_hours":false}`,
		http.StatusOK, "If-Match", ops["etag"].(string))
	bID := b["id"].(string)
	tick(bID, 1)
	eventually(t, "the new names of B's alert", func() bool {
		return h.count(t, `SELECT count(*) FROM alerts WHERE labels->>'schedule' = '`+bID+`'
			AND labels->>'team_name' = 'Operations' AND labels->>'team' = 'operations' AND status = 'firing'`) == 1
	})
	if n := h.count(t, `SELECT count(*) FROM alerts WHERE labels->>'schedule' = '`+bID+`'`); n != 1 {
		t.Errorf("%d Alerts of B after the rename", n)
	}
	if a := api.do(http.MethodDelete, "/api/v1/schedules/"+bID, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", a.status, a.body)
	}
	tick(bID, 0)
	eventually(t, "the series of the deleted Schedule removed", func() bool { return gauge(bID) == "" })
	// The raise and the resolves of Main are logged, with its Team; a canary may log its own besides.
	logged := map[string]bool{}
	for line := range strings.Lines(r.Output()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["schedule"] == scID && m["team"] == tp {
			logged[fmt.Sprint(m["event"])] = true
		}
	}
	if !logged["schedule_gap_detected"] || !logged["schedule_gap_cleared"] {
		t.Errorf("the log lines of Main: %v", logged)
	}
}
