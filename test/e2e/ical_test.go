// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
)

// personWithSession creates a local User with the Role, sets their password through the setup link and signs them
// in: their id, their web session and a Personal access token with every Permission of the Role.
func personWithSession(t *testing.T, admin *agent, base, login, role string) (string, *agent, patClient) {
	t.Helper()
	created := admin.json(http.MethodPost, "/api/v1/users", `{"name":"`+login+`","login":"`+login+`","role":"`+role+
		`"}`, http.StatusCreated)
	link, _ := url.Parse(created["password_setup_link"].(map[string]any)["url"].(string))
	setup, _ := strings.CutPrefix(link.Fragment, "token=")
	password := login + "-password-1"
	if a := admin.do(http.MethodPost, "/api/v1/password-setups", `{"token":"`+setup+`","password":"`+password+`"}`); a.status !=
		http.StatusNoContent {
		t.Fatalf("password setup of %s = %d %s", login, a.status, a.body)
	}
	person := newAgent(t, base)
	if a := person.signIn(login, password); a.status != http.StatusCreated {
		t.Fatalf("sign in of %s = %d %s", login, a.status, a.body)
	}
	perms, _ := json.Marshal(person.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := person.json(http.MethodPost, "/api/v1/me/personal-access-tokens", `{"name":"e2e","permissions":`+
		string(perms)+`}`, http.StatusCreated)["value"].(string)
	return created["user"].(map[string]any)["id"].(string), person, patClient{t: t, base: base, token: token}
}

// calendarSpans are the DTSTART/DTEND pairs of the events of an iCal document whose summary is summary, its folded
// lines joined.
func calendarSpans(doc []byte, summary string) []string {
	text := strings.ReplaceAll(string(doc), "\r\n ", "")
	var out []string
	for block := range strings.SplitSeq(text, "BEGIN:VEVENT\r\n") {
		props := map[string]string{}
		for line := range strings.SplitSeq(block, "\r\n") {
			if name, value, ok := strings.Cut(line, ":"); ok {
				props[name] = value
			}
		}
		if props["SUMMARY"] == summary {
			out = append(out, props["DTSTART"]+"/"+props["DTEND"])
		}
	}
	return out
}

// TestIcalFeed is S-134 against `muster dev`, as its Verification: petr issues his iCal feed from the web session, a
// token is refused, and the database holds only the hash (C-27.FR-10, C-27.AC-7); with the two Rotation layers of
// scenario 1 his week 1, when he has the lower Rotation layer and olga the upper one, is its nights and weekend at
// exact times, an Override of petr is an event of its own span, and a Rotation layer of Friday 19:00–09:00 is one
// event to Saturday 09:00; a new link revokes the old one and revoking the new one stops it; a disabled User's link
// answers 404 until they are enabled, and deleting the User deletes the feed; no log line carries a token.
func TestIcalFeed(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	api := patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"ical","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)}
	petr, hp, p := personWithSession(t, admin, r.App, "petr", "responder")
	sergey, _ := personWithToken(t, admin, r.App, "sergey", "responder")
	olga, o := personWithToken(t, admin, r.App, "olga", "responder")
	tp := api.json(http.MethodPost, "/api/v1/teams", `{"name":"payments","slug":"payments","own_working_hours":false}`,
		http.StatusCreated)["id"].(string)
	for _, id := range []string{petr, sergey, olga} {
		api.json(http.MethodPost, "/api/v1/teams/"+tp+"/users", `{"user_id":"`+id+`"}`, http.StatusCreated)
	}
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
	wall := func(date, clock string) time.Time {
		w, err := time.ParseInLocation("2006-01-02 15:04", date+" "+clock, berlin)
		if err != nil {
			t.Fatal(err)
		}
		return w.UTC()
	}
	ical := func(n int, clock string) string { return wall(day(n), clock).Format("20060102T150405Z") }
	span := func(a int, from string, b int, to string) string { return ical(a, from) + "/" + ical(b, to) }
	layers := `[{"name":"Around the clock","user_ids":["` + petr + `","` + sergey + `","` + olga + `"],` +
		`"shift_length":"week","handover_time":"10:00","handover_day":"monday","start_date":"` + mon + `","limits":null},` +
		`{"name":"Working hours 11-19","user_ids":["` + olga + `","` + petr + `"],"shift_length":"week",` +
		`"handover_time":"11:00","handover_day":"monday","start_date":"` + mon + `","limits":{"days":["monday",` +
		`"tuesday","wednesday","thursday","friday"],"start":"11:00","end":"19:00"}}]`
	scID := api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Main",`+
		`"time_zone":"Europe/Berlin","rotation_layers":`+layers+`}`, http.StatusCreated)["id"].(string)

	// C-27.FR-10, C-27.AC-7: the link is shown once, from the web session only, and the database holds its hash.
	issue := func() string {
		t.Helper()
		f := hp.json(http.MethodPost, "/api/v1/me/ical-feed", "", http.StatusCreated)
		u, err := url.Parse(f["url"].(string))
		if err != nil || !strings.HasPrefix(u.Path, "/ical/mstr_cal_") || !strings.HasSuffix(u.Path, ".ics") ||
			len(u.Path) != len("/ical/mstr_cal_")+52+len(".ics") {
			t.Fatalf("issued %v", f)
		}
		return r.App + u.Path
	}
	feed := func(link string) answer {
		t.Helper()
		return call(t, http.MethodGet, link, "")
	}
	url1 := issue()
	if a := p.do(http.MethodPost, "/api/v1/me/ical-feed", ""); a.status != http.StatusForbidden ||
		!strings.Contains(string(a.body), `"code":"session_required"`) {
		t.Errorf("a token issues = %d %s", a.status, a.body)
	}
	if a := p.do(http.MethodDelete, "/api/v1/me/ical-feed", ""); a.status != http.StatusForbidden {
		t.Errorf("a token revokes = %d %s", a.status, a.body)
	}
	if f := p.json(http.MethodGet, "/api/v1/me/ical-feed", "", http.StatusOK); f["exists"] != true ||
		f["issued_at"] == nil || strings.Contains(mustJSON(f), "mstr_cal_") {
		t.Errorf("getMyIcalFeed = %v", f)
	}
	if got := h.text(t, `SELECT length(token_hash)::text || '|' || position('mstr_cal_'::bytea in token_hash)::text
		FROM ical_feeds`); got != "32|0" {
		t.Errorf("stored %s", got)
	}

	// C-27.AC-7: week 1 — lower petr, upper olga: Monday 10:00–11:00, the four nights 19:00–11:00 and the weekend
	// from Friday 19:00 to Monday 10:00, Berlin time.
	a := feed(url1)
	if a.status != http.StatusOK || a.header.Get("Content-Type") != "text/calendar; charset=utf-8" ||
		!strings.Contains(string(a.body), "\r\nPRODID:-//Muster//Schedules//EN\r\n") {
		t.Fatalf("the feed = %d %v %s", a.status, a.header, a.body)
	}
	inWeek1 := func(spans []string) []string {
		var out []string
		for _, s := range spans {
			if s >= ical(0, "00:00") && s < ical(7, "00:00") {
				out = append(out, s)
			}
		}
		return out
	}
	want := []string{span(0, "10:00", 0, "11:00"), span(0, "19:00", 1, "11:00"), span(1, "19:00", 2, "11:00"),
		span(2, "19:00", 3, "11:00"), span(3, "19:00", 4, "11:00"), span(4, "19:00", 7, "10:00")}
	if got := inWeek1(calendarSpans(a.body, "payments · Main")); !slices.Equal(got, want) {
		t.Errorf("week 1 = %v\nwant %v", got, want)
	}

	// An Override of petr on Wednesday 11:00–19:00, olga's span, is an event of its own between his nights; one of
	// olga's on Thursday 19:00–23:00 takes that time out of his feed.
	for _, ov := range []struct {
		c    patClient
		user string
		date string
		from string
		to   string
	}{{p, petr, day(2), "11:00", "19:00"}, {o, olga, day(3), "19:00", "23:00"}} {
		ov.c.json(http.MethodPost, "/api/v1/schedules/"+scID+"/overrides", `{"user_id":"`+ov.user+`","start":"`+
			wall(ov.date, ov.from).Format(time.RFC3339)+`","end":"`+wall(ov.date, ov.to).Format(time.RFC3339)+`"}`,
			http.StatusCreated)
	}
	want = []string{span(0, "10:00", 0, "11:00"), span(0, "19:00", 1, "11:00"), span(1, "19:00", 2, "11:00"),
		span(2, "11:00", 2, "19:00"), span(2, "19:00", 3, "11:00"), span(3, "23:00", 4, "11:00"),
		span(4, "19:00", 7, "10:00")}
	if got := inWeek1(calendarSpans(feed(url1).body, "payments · Main")); !slices.Equal(got, want) {
		t.Errorf("week 1 with the Overrides = %v\nwant %v", got, want)
	}

	// C-27.FR-2, C-27.FR-10: a Rotation layer of Friday 19:00–09:00 is one event to Saturday 09:00.
	api.json(http.MethodPost, "/api/v1/schedules", `{"team_id":"`+tp+`","name":"Nights","time_zone":"Europe/Berlin",`+
		`"rotation_layers":[{"name":"Friday night","user_ids":["`+petr+`"],"shift_length":"week",`+
		`"handover_time":"10:00","handover_day":"monday","start_date":"`+mon+`",`+
		`"limits":{"days":["friday"],"start":"19:00","end":"09:00"}}]}`, http.StatusCreated)
	if nights := calendarSpans(feed(url1).body, "payments · Nights"); len(nights) == 0 ||
		nights[0] != span(4, "19:00", 5, "09:00") {
		t.Errorf("Friday nights = %v, want first %s", nights, span(4, "19:00", 5, "09:00"))
	}

	// A new link revokes the old one; revoking the new one stops it too; an unknown and a garbage token get 404 with
	// nothing about the token, its User or the Organization.
	url2 := issue()
	if a, b := feed(url1), feed(url2); a.status != http.StatusNotFound || b.status != http.StatusOK {
		t.Errorf("after a new link: old %d, new %d", a.status, b.status)
	}
	hp.json(http.MethodDelete, "/api/v1/me/ical-feed", "", http.StatusNoContent)
	if a := feed(url2); a.status != http.StatusNotFound {
		t.Errorf("after revoking: %d", a.status)
	}
	for _, link := range []string{r.App + "/ical/mstr_cal_" + strings.Repeat("a", 52) + ".ics",
		r.App + "/ical/garbage.ics"} {
		a := feed(link)
		var problem map[string]any
		decode(t, a, &problem)
		if a.status != http.StatusNotFound || len(problem) != 4 || problem["title"] != "Not found" ||
			strings.Contains(string(a.body), "mstr_cal_") || strings.Contains(string(a.body), "SR") {
			t.Errorf("%s = %d %s", link, a.status, a.body)
		}
	}

	// An Admin revokes a User's feed (users:write): the link answers 404 at once, the revoke is recorded with its
	// reason, a Responder is refused with 403 and the feed stays, and the User issues a new link afterwards; the
	// counter of fetches moves by outcome.
	count := func(outcome string) int {
		n, _ := strconv.Atoi(r.Metric(t, `muster_ical_feed_requests_total{outcome="`+outcome+`"}`))
		return n
	}
	served, notFound := count("served"), count("not_found")
	url4 := issue()
	if a := feed(url4); a.status != http.StatusOK {
		t.Fatalf("the feed before the revoke = %d", a.status)
	}
	if a := o.do(http.MethodDelete, "/api/v1/users/"+petr+"/ical-feed", ""); a.status != http.StatusForbidden {
		t.Errorf("a Responder revokes = %d %s", a.status, a.body)
	}
	if a := feed(url4); a.status != http.StatusOK {
		t.Errorf("the feed after a refused revoke = %d", a.status)
	}
	if a := api.do(http.MethodDelete, "/api/v1/users/"+petr+"/ical-feed", ""); a.status != http.StatusNoContent {
		t.Fatalf("an Admin revokes = %d %s", a.status, a.body)
	}
	if a := feed(url4); a.status != http.StatusNotFound {
		t.Errorf("the feed after the Admin's revoke = %d", a.status)
	}
	if got := h.count(t, `SELECT count(*) FROM audit_log WHERE action = 'ical_feed.revoked' AND
		details->>'reason' = 'revoked_by_admin' AND resource_public_id = '`+petr+`'`); got != 1 {
		t.Errorf("%d revoked_by_admin entries", got)
	}
	if a := feed(issue()); a.status != http.StatusOK {
		t.Errorf("a new link after the revoke = %d", a.status)
	}
	if gotServed, gotNotFound := count("served")-served, count("not_found")-notFound; gotServed != 3 ||
		gotNotFound != 1 {
		t.Errorf("served %d, not_found %d, want 3 and 1", gotServed, gotNotFound)
	}
	if a := api.do(http.MethodDelete, "/api/v1/users/SR000000000000/ical-feed", ""); a.status != http.StatusNotFound {
		t.Errorf("an unknown User = %d %s", a.status, a.body)
	}
	hp.json(http.MethodDelete, "/api/v1/me/ical-feed", "", http.StatusNoContent)

	// C-27.AC-7: a disabled User's link answers 404 and works again once they are enabled; deleting them deletes it.
	url3 := issue()
	api.json(http.MethodPost, "/api/v1/users/"+petr+"/disable", "", http.StatusOK)
	if a := feed(url3); a.status != http.StatusNotFound {
		t.Errorf("a disabled User's link = %d", a.status)
	}
	if line, _, ok := r.LogLine("ical_feed_not_found"); !ok || line["path"] != "/ical/[redacted].ics" {
		t.Errorf("logged %v", line)
	}
	api.json(http.MethodPost, "/api/v1/users/"+petr+"/enable", "", http.StatusOK)
	if a := feed(url3); a.status != http.StatusOK {
		t.Errorf("an enabled User's link = %d", a.status)
	}
	if a := api.do(http.MethodDelete, "/api/v1/users/"+petr, ""); a.status != http.StatusNoContent {
		t.Fatalf("delete petr = %d %s", a.status, a.body)
	}
	if a := feed(url3); a.status != http.StatusNotFound || h.count(t, `SELECT count(*) FROM ical_feeds`) != 0 {
		t.Errorf("a deleted User's link = %d, %d feeds", a.status, h.count(t, `SELECT count(*) FROM ical_feeds`))
	}

	// C-27.FR-10: no log line carries a token, and every 404 names the masked path.
	out := r.Output()
	if strings.Contains(out, "mstr_cal_") {
		t.Error("a log line carries an iCal token")
	}
	if n := strings.Count(out, `"path":"/ical/[redacted].ics"`); n < 6 {
		t.Errorf("%d log lines name the masked path, want 6 or more", n)
	}
}
