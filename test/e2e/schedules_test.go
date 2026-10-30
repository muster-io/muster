// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

// messagePaths are the paths of the fake messenger servers that send, edit or open a message or a direct message.
var messagePaths = []string{"/api/v4/posts", "/api/v4/channels/direct", "/v1/messages", "/v1/direct-messages",
	"sendMessage", "editMessage", "chat.postMessage", "chat.postEphemeral", "chat.update", "conversations.open"}

// sentMessages counts the requests of every fake messenger server that send or open a message, and the delivery
// events Muster recorded, which every outgoing webhook request adds to.
func sentMessages(t *testing.T, h *Harness) string {
	t.Helper()
	n := 0
	for _, u := range []string{h.Fakes.Mattermost, h.Fakes.MattermostSecond, h.Fakes.Telegram, h.Fakes.Bridge,
		h.Fakes.Slack} {
		for _, p := range recordedPaths(t, u) {
			if slices.ContainsFunc(messagePaths, func(m string) bool { return strings.Contains(p, m) }) {
				n++
			}
		}
	}
	return fmt.Sprintf("%d messages, %d delivery events", n, h.count(t, `SELECT count(*) FROM delivery_events`))
}

// TestSchedules is S-131 against `muster dev`, as its Verification: the Schedule "Main" of payments with the two
// Rotation layers of scenario 1 in Europe/Berlin and its On-call (C-27.AC-1) across the change of the clocks; a window
// across midnight on Friday (C-27.FR-2); a Viewer refused in a Rotation layer; an Override over both layers with no
// message (C-27.AC-2, FR-11); the precondition of Overrides (C-27.AC-3); two overlapping Overrides and a stale If-Match
// (C-27.AC-10); an Override that has ended; people disabled, turned into a Viewer and taken out of the Team
// (C-27.AC-4); the Permissions of a Responder; the Team that keeps its Schedules (C-25.AC-9); another Schedule made main
// and the main one deleted (C-27.AC-8), with muster_schedule_info and the Audit log.
func TestSchedules(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	api := patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"schedules","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)}
	petr, p := personWithToken(t, admin, r.App, "petr", "responder")
	sergey, _ := personWithToken(t, admin, r.App, "sergey", "responder")
	olga, o := personWithToken(t, admin, r.App, "olga", "responder")
	bob, b := personWithToken(t, admin, r.App, "bob", "responder")
	vic, v := personWithToken(t, admin, r.App, "vic", "viewer")
	tp := api.json(http.MethodPost, "/api/v1/teams", `{"name":"Payments","slug":"payments","own_working_hours":false}`,
		http.StatusCreated)["id"].(string)
	for _, id := range []string{petr, sergey, olga, vic} {
		api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+id+`"}`, http.StatusCreated)
	}
	before := sentMessages(t, h)

	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := readClock(t, r).Now.UTC()
	mon := now.AddDate(0, 0, 15-((int(now.Weekday())+6)%7+1)).Format(time.DateOnly) // a Monday 8 to 14 days ahead
	day := func(n int) string {
		d, _ := time.Parse(time.DateOnly, mon)
		return d.AddDate(0, 0, n).Format(time.DateOnly)
	}
	at := func(date, clock string) string {
		w, err := time.ParseInLocation("2006-01-02 15:04", date+" "+clock, berlin)
		if err != nil {
			t.Fatal(err)
		}
		return w.UTC().Format(time.RFC3339)
	}
	layers := `[{"name":"Around the clock","user_ids":["` + petr + `","` + sergey + `","` + olga + `"],` +
		`"shift_length":"week","handover_time":"10:00","handover_day":"monday","start_date":"` + mon + `","limits":null},` +
		`{"name":"Working hours 11-19","user_ids":["` + olga + `","` + petr + `"],"shift_length":"week",` +
		`"handover_time":"11:00","handover_day":"monday","start_date":"` + mon + `","limits":{"days":["monday",` +
		`"tuesday","wednesday","thursday","friday"],"start":"11:00","end":"19:00"}}]`
	sc := api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Main",`+
		`"time_zone":"Europe/Berlin","rotation_layers":`+layers+`}`, http.StatusCreated)
	scID := sc["id"].(string)
	if rl := sc["rotation_layers"].([]any); sc["main"] != true || len(rl) != 2 ||
		rl[0].(map[string]any)["number"] != 1.0 || rl[1].(map[string]any)["number"] != 2.0 {
		t.Fatalf("schedule %v", sc)
	}
	who := func(id, from string) string {
		t.Helper()
		to, _ := time.Parse(time.RFC3339, from)
		var res struct {
			Segments []struct {
				User *struct{ Login string } `json:"user"`
			} `json:"segments"`
		}
		decode(t, api.do(http.MethodGet, "/api/v1/schedules/"+id+"/on-call?from="+from+"&to="+
			to.Add(time.Minute).Format(time.RFC3339), ""), &res)
		if len(res.Segments) == 0 || res.Segments[0].User == nil {
			return "gap"
		}
		return res.Segments[0].User.Login
	}
	check := func(what, id string, cases [][3]string) {
		t.Helper()
		for _, c := range cases {
			if got := who(id, at(c[0], c[1])); got != c[2] {
				t.Errorf("%s: On-call at %s %s = %s, want %s", what, c[0], c[1], got, c[2])
			}
		}
	}

	// C-27.AC-1: week 1 — lower petr, upper olga; the handover stays at 10:00 in Berlin in March.
	check("scenario 1", scID, [][3]string{{day(2), "15:00", "olga"}, {day(2), "21:00", "petr"},
		{day(5), "15:00", "petr"}})
	y := now.Year() + 1
	lastSunday := time.Date(y, 3, 31, 0, 0, 0, 0, time.UTC)
	lastSunday = lastSunday.AddDate(0, 0, -int(lastSunday.Weekday()))
	var shifts struct {
		RotationLayers []struct {
			Shifts []struct{ Start string } `json:"shifts"`
		} `json:"rotation_layers"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/schedules/"+scID+"/on-call?from="+
		lastSunday.AddDate(0, 0, -7).Format(time.RFC3339)+"&to="+lastSunday.AddDate(0, 0, 2).Format(time.RFC3339), ""),
		&shifts)
	var handovers []string
	for _, s := range shifts.RotationLayers[0].Shifts {
		if strings.Contains(s.Start, "T09:00") || strings.Contains(s.Start, "T08:00") {
			handovers = append(handovers, s.Start)
		}
	}
	if want := []string{lastSunday.AddDate(0, 0, -6).Format("2006-01-02") + "T09:00:00Z",
		lastSunday.AddDate(0, 0, 1).Format("2006-01-02") + "T08:00:00Z"}; !slices.Equal(handovers, want) {
		t.Errorf("handovers in March %v, want %v", handovers, want)
	}

	// C-27.FR-2: Friday 19:00–09:00 belongs to Friday; an end equal to its start is refused.
	nights := api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Nights",`+
		`"time_zone":"Europe/Berlin","rotation_layers":[{"name":"Friday night","user_ids":["`+olga+`"],`+
		`"shift_length":"week","handover_time":"10:00","handover_day":"monday","start_date":"`+mon+`",`+
		`"limits":{"days":["friday"],"start":"19:00","end":"09:00"}}]}`, http.StatusCreated)
	snID := nights["id"].(string)
	if nights["main"] != false {
		t.Errorf("a second schedule is main: %v", nights)
	}
	check("Friday night", snID, [][3]string{{day(4), "23:00", "olga"}, {day(5), "08:00", "olga"},
		{day(5), "10:00", "gap"}, {day(3), "23:00", "gap"}})
	a := api.do(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Equal","rotation_layers":[`+
		`{"name":"x","user_ids":["`+olga+`"],"shift_length":"day","handover_time":"09:00","start_date":"`+mon+`",`+
		`"limits":{"days":["friday"],"start":"19:00","end":"19:00"}}]}`)
	if ptr, code := firstError(t, a); a.status != http.StatusUnprocessableEntity ||
		ptr != "/rotation_layers/0/limits/end" || code != "out_of_range" {
		t.Errorf("an end equal to the start = %d %s", a.status, a.body)
	}

	// C-27.FR-2, C-27.AC-3: a Viewer in a Rotation layer; shift_seconds of 5400.
	a = api.do(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Bad","rotation_layers":[`+
		`{"name":"x","user_ids":["`+vic+`"],"shift_length":"day","handover_time":"09:00","start_date":"`+mon+`",`+
		`"limits":null}]}`)
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body),
		`"pointer":"/rotation_layers/0/user_ids/0"`) || !strings.Contains(string(a.body), `"reason":"viewer"`) {
		t.Errorf("a viewer in a layer = %d %s", a.status, a.body)
	}
	a = api.do(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Bad","rotation_layers":[`+
		`{"name":"x","user_ids":["`+olga+`"],"shift_length":"hours","shift_seconds":5400,"handover_time":"09:00",`+
		`"start_date":"`+mon+`","limits":null}]}`)
	if ptr, code := firstError(t, a); a.status != http.StatusUnprocessableEntity ||
		ptr != "/rotation_layers/0/shift_seconds" || code != "out_of_range" {
		t.Errorf("5400 seconds = %d %s", a.status, a.body)
	}

	// C-27.AC-2: an Override on Wednesday of week 3, when both layers have olga, makes petr On-call 11:00–21:00.
	ov := func(c patClient, body string) answer {
		t.Helper()
		return c.do(http.MethodPost, "/api/v1/schedules/"+scID+"/overrides", body)
	}
	span := func(user, date, from, to string) string {
		return `{"user_id":"` + user + `","start":"` + at(date, from) + `","end":"` + at(date, to) + `"}`
	}
	a = ov(p, span(petr, day(16), "11:00", "21:00"))
	if a.status != http.StatusCreated || !strings.Contains(string(a.body), `"login":"petr"`) {
		t.Fatalf("petr's override = %d %s", a.status, a.body)
	}
	check("the Override", scID, [][3]string{{day(16), "10:00", "olga"}, {day(16), "15:00", "petr"},
		{day(16), "20:00", "petr"}, {day(16), "22:00", "olga"}})
	entries := api.json(http.MethodGet, "/api/v1/audit-log?action=schedule_override.created", "",
		http.StatusOK)["items"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["actor"].(map[string]any)["name"] != "petr" {
		t.Errorf("audit %v", entries)
	}

	// C-27.AC-3: the precondition.
	if a = ov(b, span(olga, day(17), "11:00", "12:00")); a.status != http.StatusConflict ||
		!strings.Contains(string(a.body), `"code":"not_in_team"`) {
		t.Errorf("bob outside the team = %d %s", a.status, a.body)
	}
	if a = ov(v, span(olga, day(17), "11:00", "12:00")); a.status != http.StatusForbidden {
		t.Errorf("a viewer = %d %s", a.status, a.body)
	}
	if a = ov(o, span(bob, day(17), "11:00", "12:00")); a.status != http.StatusUnprocessableEntity ||
		!strings.Contains(string(a.body), `"pointer":"/user_id"`) ||
		!strings.Contains(string(a.body), `"reason":"not_in_team"`) {
		t.Errorf("bob taking a shift = %d %s", a.status, a.body)
	}
	if a = ov(o, `{"user_id":"`+olga+`","replaces_user_id":"`+bob+`","start":"`+at(day(17), "11:00")+`","end":"`+
		at(day(17), "12:00")+`"}`); a.status != http.StatusCreated ||
		!strings.Contains(string(a.body), `"login":"bob"`) {
		t.Errorf("replacing bob = %d %s", a.status, a.body)
	}
	if a = ov(api, span(olga, day(17), "13:00", "14:00")); a.status != http.StatusCreated {
		t.Errorf("an Admin outside the team = %d %s", a.status, a.body)
	}
	if a = ov(o, `{"user_id":"`+olga+`","start":"2020-01-01T00:00:00Z","end":"2020-01-01T01:00:00Z"}`); a.status !=
		http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"pointer":"/end"`) {
		t.Errorf("an override in the past = %d %s", a.status, a.body)
	}

	// C-27.AC-10: of two overlapping Overrides the one created last wins; a stale If-Match is refused.
	first := ov(p, span(sergey, day(18), "11:00", "15:00"))
	ov(o, span(petr, day(18), "13:00", "17:00"))
	check("overlapping Overrides", scID, [][3]string{{day(18), "12:00", "sergey"}, {day(18), "14:00", "petr"},
		{day(18), "16:00", "petr"}})
	var firstOverride struct {
		ID string `json:"id"`
	}
	decode(t, first, &firstOverride)
	path := "/api/v1/schedules/" + scID + "/overrides/" + firstOverride.ID
	if a = p.do(http.MethodPut, path, span(sergey, day(18), "11:00", "14:00"), "If-Match", `"7"`); a.status !=
		http.StatusPreconditionFailed {
		t.Errorf("a stale If-Match = %d %s", a.status, a.body)
	}
	if a = p.do(http.MethodPut, path, span(sergey, day(18), "11:00", "14:00"), "If-Match", first.header.Get("ETag")); a.status != http.StatusOK {
		t.Errorf("the change = %d %s", a.status, a.body)
	}
	if a = p.do(http.MethodDelete, path, ""); a.status != http.StatusNoContent {
		t.Errorf("the deletion = %d %s", a.status, a.body)
	}

	// C-27.FR-11: nothing was sent to a messenger or an outgoing webhook.
	if after := sentMessages(t, h); after != before {
		t.Errorf("before %s, after %s", before, after)
	}

	// C-27.AC-10: a Responder may not change Schedules.
	for _, req := range [][3]string{{http.MethodPost, "/api/v1/schedules", `{"team_id":"` + tp + `","name":"x",` +
		`"rotation_layers":[]}`}, {http.MethodPut, "/api/v1/schedules/" + scID, `{"team_id":"` + tp + `","name":"x",` +
		`"rotation_layers":[]}`}, {http.MethodDelete, "/api/v1/schedules/" + scID, ""}} {
		if a = o.do(req[0], req[1], req[2], "If-Match", `"1"`); a.status != http.StatusForbidden {
			t.Errorf("a responder %s %s = %d", req[0], req[1], a.status)
		}
	}
	if a = v.do(http.MethodGet, "/api/v1/schedules/"+scID+"/overrides", ""); a.status != http.StatusOK {
		t.Errorf("a viewer reads the overrides = %d", a.status)
	}

	// C-27.AC-4: petr disabled, olga turned into a Viewer, sergey taken out of the Team.
	api.json(http.MethodPost, "/api/v1/users/"+petr+"/disable", "", http.StatusOK)
	check("petr disabled", scID, [][3]string{{day(9), "15:00", "sergey"}})
	var res struct {
		Warnings []struct {
			Kind          string
			User          struct{ Login string }
			RotationLayer *struct{ Name string } `json:"rotation_layer"`
		} `json:"warnings"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/schedules/"+scID+"/on-call?from="+day(0)+"T00:00:00Z&to="+day(14)+
		"T00:00:00Z", ""), &res)
	var warned []string
	for _, w := range res.Warnings {
		if w.Kind == "disabled" && w.RotationLayer != nil {
			warned = append(warned, w.User.Login+" in "+w.RotationLayer.Name)
		}
	}
	if want := []string{"petr in Around the clock", "petr in Working hours 11-19"}; !slices.Equal(warned, want) {
		t.Errorf("warnings %v", warned)
	}
	user := api.do(http.MethodGet, "/api/v1/users/"+olga, "")
	api.json(http.MethodPut, "/api/v1/users/"+olga, `{"name":"olga","role":"viewer"}`, http.StatusOK, "If-Match",
		user.header.Get("ETag"))
	check("olga a Viewer, petr disabled", scID, [][3]string{{day(2), "15:00", "gap"}})
	if a = api.do(http.MethodDelete, "/api/v1/teams/"+tp+"/users/"+sergey, ""); a.status != http.StatusNoContent {
		t.Fatalf("sergey out = %d %s", a.status, a.body)
	}
	check("sergey out of the team", scID, [][3]string{{day(9), "15:00", "gap"}})

	// An Override that has ended can be neither changed nor deleted.
	soon := api.json(http.MethodPost, "/api/v1/schedules/"+scID+"/overrides", `{"user_id":"`+vic+`","start":"`+
		now.Format(time.RFC3339)+`","end":"`+now.Add(30*time.Minute).Format(time.RFC3339)+`"}`,
		http.StatusUnprocessableEntity)
	if !strings.Contains(fmt.Sprint(soon), "not_pageable") {
		t.Errorf("vic taking a shift = %v", soon)
	}
	api.json(http.MethodPut, "/api/v1/users/"+olga, `{"name":"olga","role":"responder"}`, http.StatusOK, "If-Match",
		api.do(http.MethodGet, "/api/v1/users/"+olga, "").header.Get("ETag"))
	ended := api.do(http.MethodPost, "/api/v1/schedules/"+scID+"/overrides", `{"user_id":"`+olga+`","start":"`+
		now.Format(time.RFC3339)+`","end":"`+now.Add(30*time.Minute).Format(time.RFC3339)+`"}`)
	var endedID struct {
		ID string `json:"id"`
	}
	decode(t, ended, &endedID)
	advance(t, r, 3600)
	endedPath := "/api/v1/schedules/" + scID + "/overrides/" + endedID.ID
	if a = api.do(http.MethodPut, endedPath, `{"user_id":"`+olga+`","start":"`+now.Format(time.RFC3339)+`","end":"`+
		now.Add(3*time.Hour).Format(time.RFC3339)+`"}`, "If-Match", ended.header.Get("ETag")); a.status !=
		http.StatusConflict || !strings.Contains(string(a.body), `"code":"override_ended"`) {
		t.Errorf("changing an ended override = %d %s", a.status, a.body)
	}
	if a = api.do(http.MethodDelete, endedPath, ""); a.status != http.StatusConflict ||
		!strings.Contains(string(a.body), `"code":"override_ended"`) {
		t.Errorf("deleting an ended override = %d %s", a.status, a.body)
	}

	// C-25.AC-9: the Team keeps its Schedules.
	var held struct {
		Code    string                  `json:"code"`
		InUseBy []struct{ Name string } `json:"in_use_by"`
	}
	decode(t, api.do(http.MethodDelete, "/api/v1/teams/"+tp, ""), &held)
	if held.Code != "team_in_use" || len(held.InUseBy) != 2 || held.InUseBy[0].Name != "Main" ||
		held.InUseBy[1].Name != "Nights" {
		t.Errorf("deleting the team = %+v", held)
	}

	// C-27.AC-8: another Schedule made main; the main Schedule deleted leaves the Team without one.
	etag := api.do(http.MethodGet, "/api/v1/schedules/"+snID, "").header.Get("ETag")
	if a = api.do(http.MethodPut, "/api/v1/schedules/"+scID, `{"team_id":"`+tp+`","name":"Main","main":false,`+
		`"rotation_layers":[]}`, "If-Match", api.do(http.MethodGet, "/api/v1/schedules/"+scID, "").header.Get("ETag")); a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"pointer":"/main"`) {
		t.Errorf("unmarking main = %d %s", a.status, a.body)
	}
	promoted := api.json(http.MethodPut, "/api/v1/schedules/"+snID, `{"team_id":"`+tp+`","name":"Nights","main":true,`+
		`"rotation_layers":[{"id":"`+nights["rotation_layers"].([]any)[0].(map[string]any)["id"].(string)+`",`+
		`"name":"Friday night","user_ids":["`+olga+`"],"shift_length":"week","handover_time":"10:00",`+
		`"handover_day":"monday","start_date":"`+mon+`","limits":{"days":["friday"],"start":"19:00","end":"09:00"}}]}`,
		http.StatusOK, "If-Match", etag)
	if promoted["main"] != true || api.json(http.MethodGet, "/api/v1/schedules/"+scID, "", http.StatusOK)["main"] != false {
		t.Errorf("Nights made main: %v", promoted)
	}
	mains := func() int {
		n := 0
		for _, it := range api.json(http.MethodGet, "/api/v1/schedules?team="+tp, "", http.StatusOK)["items"].([]any) {
			if it.(map[string]any)["main"] == true {
				n++
			}
		}
		return n
	}
	if a = api.do(http.MethodDelete, "/api/v1/schedules/"+snID, ""); a.status != http.StatusNoContent || mains() != 0 {
		t.Errorf("deleting the main schedule = %d, %d main", a.status, mains())
	}
	eventually(t, "one muster_schedule_info series", func() bool {
		return strings.Count(string(call(t, http.MethodGet, r.Internal+"/metrics", "").body), "muster_schedule_info{") == 1
	})
	for _, action := range []string{"schedule.created", "schedule.updated", "schedule.deleted",
		"schedule_override.created", "schedule_override.updated", "schedule_override.deleted"} {
		if got := h.count(t, `SELECT count(*) FROM audit_log WHERE action = '`+action+`'`); got < 1 {
			t.Errorf("no %s in the Audit log", action)
		}
	}
	if a = api.do(http.MethodDelete, "/api/v1/schedules/"+scID, ""); a.status != http.StatusNoContent {
		t.Fatalf("deleting Main = %d %s", a.status, a.body)
	}
	if a = api.do(http.MethodDelete, "/api/v1/teams/"+tp, ""); a.status != http.StatusNoContent {
		t.Errorf("the team without schedules = %d %s", a.status, a.body)
	}
}
