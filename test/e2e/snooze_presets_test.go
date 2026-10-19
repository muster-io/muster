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

// TestSnoozePresets is S-092 against `muster dev`: the Organization's start of the working day and first day of the
// week, read, refused, updated with their Audit log diff and kept when omitted (C-20.FR-9, C-20.AC-7); at 02:00 UTC on
// the development clock a Snooze with the preset tomorrow in UTC ends at 09:00 the same day, with its Timeline entry
// and the Audit log details of the preset (C-10.AC-22); without a time zone, bob's Personal access token takes his
// profile's Asia/Tokyo and a Service account the Organization's UTC; listSnoozePresets names the zone it used; a bulk
// Snooze computes one end; the refusals of the request, of a Viewer and of an unknown time zone (C-10.AC-17); a later
// change of the settings never moves a Snooze, which ends like any other with a Loud snooze_ended; and at 08:59 the
// preset ends a minute later, at 09:00 sharp the next day (C-10.FR-17). Each harness has a database of its own, so
// the changed settings reach no other test.
func TestSnoozePresets(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	// The checks use Personal access tokens only: an advance of the development clock may end sessions.
	newPAT := func(s *agent, name string) patClient {
		t.Helper()
		perms, _ := json.Marshal(s.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
		return patClient{t: t, base: r.App, token: s.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
			`{"name":"`+name+`","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)}
	}
	api := newPAT(admin, "presets")
	created := admin.json(http.MethodPost, "/api/v1/users", `{"name":"bob","login":"bob","role":"responder"}`,
		http.StatusCreated)
	link, _ := url.Parse(created["password_setup_link"].(map[string]any)["url"].(string))
	setup, _ := strings.CutPrefix(link.Fragment, "token=")
	if a := admin.do(http.MethodPost, "/api/v1/password-setups", `{"token":"`+setup+`","password":"bob-password-1"}`); a.status !=
		http.StatusNoContent {
		t.Fatalf("password setup = %d %s", a.status, a.body)
	}
	session := newAgent(t, r.App)
	if a := session.signIn("bob", "bob-password-1"); a.status != http.StatusCreated {
		t.Fatalf("bob = %d %s", a.status, a.body)
	}
	if me := session.json(http.MethodPut, "/api/v1/me", `{"name":"bob","time_zone":"Asia/Tokyo"}`, http.StatusOK); me["user"].(map[string]any)["time_zone"] !=
		"Asia/Tokyo" {
		t.Fatalf("bob's profile = %v", me)
	}
	bob := newPAT(session, "bob")
	serviceAccountToken := func(name, role string) patClient {
		t.Helper()
		id := admin.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"`+name+`","role":"`+role+`"}`,
			http.StatusCreated)["id"].(string)
		return patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/service-accounts/"+id+"/tokens",
			`{"name":"`+name+`"}`, http.StatusCreated)["value"].(string)}
	}
	robot, viewer := serviceAccountToken("robot", "responder"), serviceAccountToken("watcher", "viewer")

	// The development clock: now, today's 00:00 UTC plus s seconds, and on to the next s seconds after 00:00 UTC.
	now := func() time.Time { return readClock(t, r).Now.UTC() }
	today := func(s int) time.Time { return now().Truncate(24 * time.Hour).Add(time.Duration(s) * time.Second) }
	at := func(s int) {
		t.Helper()
		if d := ((s-int(now().Unix()%86400))%86400 + 86400) % 86400; d > 0 {
			advance(t, r, d)
		}
	}
	presets := func(c patClient, zone string) (string, map[string]time.Time) {
		t.Helper()
		target := "/api/v1/snooze-presets"
		if zone != "" {
			target += "?time_zone=" + url.QueryEscape(zone)
		}
		var l struct {
			TimeZone string `json:"time_zone"`
			Items    []struct {
				Preset string    `json:"preset"`
				Until  time.Time `json:"until"`
			} `json:"items"`
		}
		decode(t, c.do(http.MethodGet, target, ""), &l)
		ends := map[string]time.Time{}
		var order []string
		for _, it := range l.Items {
			ends[it.Preset], order = it.Until, append(order, it.Preset)
		}
		if !slices.Equal(order, []string{"tomorrow", "week_start"}) {
			t.Errorf("the presets come in the order %v", order)
		}
		return l.TimeZone, ends
	}
	organization := func(change func(m map[string]any)) {
		t.Helper()
		if a := putOrganization(api, change); a.status != http.StatusOK {
			t.Fatalf("update the organization = %d %s", a.status, a.body)
		}
	}

	// C-20.FR-9, C-20.AC-7: the two settings.
	if got := h.text(t, `SELECT workday_start || '|' || week_start FROM organizations`); got != "09:00|monday" {
		t.Errorf("the database has %s", got)
	}
	if o := api.json(http.MethodGet, "/api/v1/organization", "", http.StatusOK); o["workday_start"] != "09:00" ||
		o["week_start"] != "monday" {
		t.Errorf("organization = %v %v", o["workday_start"], o["week_start"])
	}
	for _, c := range []struct {
		field, value, pointer string
	}{
		{"workday_start", "24:00", "/workday_start"},
		{"workday_start", "9:00", "/workday_start"},
		{"week_start", "weekend", "/week_start"},
	} {
		a := putOrganization(api, func(m map[string]any) { m[c.field] = c.value })
		if p, code := firstError(t, a); a.status != http.StatusBadRequest || p != c.pointer || code != "invalid_format" {
			t.Errorf("%s %s = %d %s", c.field, c.value, a.status, a.body)
		}
	}
	a := putOrganization(api, func(m map[string]any) { m["workday_start"], m["week_start"] = "08:30", "sunday" })
	var o map[string]any
	decode(t, a, &o)
	if a.status != http.StatusOK || o["workday_start"] != "08:30" || o["week_start"] != "sunday" {
		t.Fatalf("08:30 sunday = %d %s", a.status, a.body)
	}
	var audit struct {
		Items []struct {
			Diff []struct{ Pointer string } `json:"diff"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/audit-log?action=organization.updated&limit=1", ""), &audit)
	var pointers []string
	for _, d := range audit.Items[0].Diff {
		pointers = append(pointers, d.Pointer)
	}
	if slices.Sort(pointers); !slices.Equal(pointers, []string{"/week_start", "/workday_start"}) {
		t.Errorf("diff %v", pointers)
	}
	a = putOrganization(api, func(m map[string]any) { delete(m, "workday_start"); delete(m, "week_start") })
	decode(t, a, &o)
	if a.status != http.StatusOK || o["workday_start"] != "08:30" || o["week_start"] != "sunday" {
		t.Errorf("an update without them = %d %s", a.status, a.body)
	}
	before := now()
	zone, ends := presets(api, "UTC")
	if tomorrow := ends["tomorrow"]; zone != "UTC" || tomorrow.Format("15:04") != "08:30" || !tomorrow.After(before) ||
		tomorrow.Sub(before) > 24*time.Hour {
		t.Errorf("tomorrow at 08:30 = %s %v", zone, tomorrow)
	}
	if week := ends["week_start"]; week.Weekday() != time.Sunday || week.Format("15:04") != "08:30" ||
		!week.After(before) || week.Sub(before) > 7*24*time.Hour {
		t.Errorf("week_start on sunday 08:30 = %v", week)
	}
	organization(func(m map[string]any) { m["workday_start"], m["week_start"] = "09:00", "monday" })

	// Four firing Alert Groups of one Alertmanager group.
	in := admin.json(http.MethodPost, "/api/v1/integrations", `{"name":"cmd","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)
	intID := in["id"].(string)
	intToken := admin.json(http.MethodPost, "/api/v1/integrations/"+intID+"/tokens", `{"name":"cmd"}`,
		http.StatusCreated)["value"].(string)
	fam := h.Fakes.Alertmanager + "/_fake"
	if a := call(t, http.MethodPost, fam+"/receivers", `{"name":"cmd","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		intToken+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register = %d %s", a.status, a.body)
	}
	var profiles struct {
		Items []struct {
			Policy json.RawMessage `json:"policy"`
		} `json:"items"`
	}
	decode(t, admin.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	admin.json(http.MethodPost, "/api/v1/routes", `{"name":"ops","matchers":[{"label":"team","op":"=","value":"ops"}],`+
		`"urgent":false,"group_key":["alertname","cluster"],"destination_ids":[],"policy":`+
		string(profiles.Items[0].Policy)+`}`, http.StatusCreated)
	put := func(path, body string) {
		t.Helper()
		if a := call(t, http.MethodPut, fam+path, body); a.status/100 != 2 {
			t.Fatalf("PUT %s = %d %s", path, a.status, a.body)
		}
	}
	put("/groups/s1", `{"receiver":"cmd","route":"{}","labels":{"alertname":"Nightly"}}`)
	for _, c := range []string{"a", "b", "c", "d"} {
		put("/groups/s1/alerts/"+c, `{"labels":{"team":"ops","cluster":"`+c+`"}}`)
	}
	if a := call(t, http.MethodPost, fam+"/groups/s1/notify", `{"reason":"first notification"}`); a.status !=
		http.StatusOK {
		t.Fatalf("notify = %d %s", a.status, a.body)
	}
	eventually(t, "the snapshot to be processed", func() bool {
		return h.count(t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`) > 0
	})
	groupOf := func(cluster string) string {
		t.Helper()
		items := api.json(http.MethodGet, "/api/v1/integrations/"+intID+"/alerts?"+url.Values{"label": {`cluster="` +
			cluster + `"`}}.Encode(), "", http.StatusOK)["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no alert of cluster %s", cluster)
		}
		return items[0].(map[string]any)["alert_group"].(map[string]any)["id"].(string)
	}
	ga, gb, gc, gd := groupOf("a"), groupOf("b"), groupOf("c"), groupOf("d")
	snooze := func(c patClient, id, body string) answer {
		t.Helper()
		return c.do(http.MethodPost, "/api/v1/alert-groups/"+id+"/snooze", body)
	}
	snoozeUntil := func(c patClient, id, body string) time.Time {
		t.Helper()
		var res struct {
			AlertGroup struct {
				SnoozeUntil time.Time `json:"snooze_until"`
			} `json:"alert_group"`
		}
		a := snooze(c, id, body)
		if a.status != http.StatusOK {
			t.Fatalf("snooze %s %s = %d %s", id, body, a.status, a.body)
		}
		decode(t, a, &res)
		return res.AlertGroup.SnoozeUntil
	}
	group := func(id string) map[string]any {
		return api.json(http.MethodGet, "/api/v1/alert-groups/"+id, "", http.StatusOK)
	}
	newest := func(id string) map[string]any {
		t.Helper()
		return api.json(http.MethodGet, "/api/v1/alert-groups/"+id+"/timeline?limit=1", "",
			http.StatusOK)["items"].([]any)[0].(map[string]any)
	}
	at(2 * 3600) // 02:00 UTC

	// C-10.AC-22: the request's time zone, the Timeline and the Audit log.
	if got := snoozeUntil(api, ga, `{"preset":"tomorrow","time_zone":"UTC"}`); !got.Equal(today(9 * 3600)) {
		t.Errorf("tomorrow in UTC at 02:00 = %v, want %v", got, today(9*3600))
	}
	if e := newest(ga); e["event"] != "snoozed" || e["snooze_until"] != today(9*3600).Format(time.RFC3339) {
		t.Errorf("the newest entry %v", e)
	}
	var snoozed struct {
		Items []struct {
			Details map[string]any `json:"details"`
		} `json:"items"`
	}
	decode(t, api.do(http.MethodGet, "/api/v1/audit-log?action=alert_group.snoozed&limit=1", ""), &snoozed)
	if d := snoozed.Items[0].Details; d["preset"] != "tomorrow" || d["time_zone"] != "UTC" || d["no_end"] != false ||
		d["until"] != today(9*3600).Format(time.RFC3339) {
		t.Errorf("audit details %v", d)
	}

	// The caller's profile time zone, else the Organization's.
	if got := snoozeUntil(bob, gb, `{"preset":"tomorrow"}`); !got.Equal(today(86400)) {
		t.Errorf("bob in Asia/Tokyo = %v, want %v (09:00 in Tokyo)", got, today(86400))
	}
	if got := snoozeUntil(robot, gc, `{"preset":"tomorrow"}`); !got.Equal(today(9 * 3600)) {
		t.Errorf("a service account = %v, want %v", got, today(9*3600))
	}
	if zone, ends := presets(bob, ""); zone != "Asia/Tokyo" || !ends["tomorrow"].Equal(today(86400)) {
		t.Errorf("bob's presets = %s %v", zone, ends)
	}
	if zone, _ := presets(robot, ""); zone != "UTC" {
		t.Errorf("the service account's presets are in %s", zone)
	}

	// A bulk Snooze computes the end once; the week starts tomorrow (UTC) for this check.
	organization(func(m map[string]any) { m["week_start"] = strings.ToLower(today(86400).Weekday().String()) })
	var bulk struct {
		Results []struct {
			Outcome string `json:"outcome"`
		} `json:"results"`
	}
	decode(t, api.do(http.MethodPost, "/api/v1/alert-groups/bulk-commands", `{"command":"snooze","alert_group_ids":["`+
		ga+`","`+gd+`"],"snooze":{"preset":"week_start","time_zone":"UTC"}}`), &bulk)
	if len(bulk.Results) != 2 || bulk.Results[0].Outcome != "done" || bulk.Results[1].Outcome != "done" {
		t.Errorf("bulk = %+v", bulk)
	}
	for _, id := range []string{ga, gd} {
		if got := group(id)["snooze_until"]; got != today(86400+9*3600).Format(time.RFC3339) {
			t.Errorf("%s after the bulk snooze = %v, want %v", id, got, today(86400+9*3600))
		}
	}

	// C-10.AC-17, C-10.AC-22: refusals.
	until := today(86400 + 3600).Format(time.RFC3339)
	for _, c := range []struct {
		body          string
		status        int
		pointer, code string
	}{
		{`{"preset":"tomorrow","until":"` + until + `"}`, http.StatusUnprocessableEntity, "", "one_of_required"},
		{`{"until":"` + until + `","time_zone":"UTC"}`, http.StatusUnprocessableEntity, "/time_zone", "unsupported"},
		{`{"preset":"tomorrow","time_zone":"Mars/Base"}`, http.StatusUnprocessableEntity, "/time_zone", "invalid_format"},
		{`{}`, http.StatusUnprocessableEntity, "", "one_of_required"},
		{`{"preset":"someday"}`, http.StatusBadRequest, "/preset", "invalid_format"},
	} {
		a := snooze(api, gd, c.body)
		if p, code := firstError(t, a); a.status != c.status || p != c.pointer || code != c.code {
			t.Errorf("snooze %s = %d %s", c.body, a.status, a.body)
		}
	}
	a = api.do(http.MethodPost, "/api/v1/alert-groups/bulk-commands", `{"command":"snooze","alert_group_ids":["`+gd+
		`"],"snooze":{"until":"`+until+`","time_zone":"UTC"}}`)
	if p, code := firstError(t, a); a.status != http.StatusUnprocessableEntity || p != "/snooze/time_zone" ||
		code != "unsupported" {
		t.Errorf("bulk time zone without a preset = %d %s", a.status, a.body)
	}
	if a := viewer.do(http.MethodGet, "/api/v1/snooze-presets", ""); a.status != http.StatusForbidden {
		t.Errorf("a viewer's presets = %d %s", a.status, a.body)
	}
	a = api.do(http.MethodGet, "/api/v1/snooze-presets?time_zone=Mars/Base", "")
	if p, code := firstError(t, a); a.status != http.StatusUnprocessableEntity || p != "/query/time_zone" ||
		code != "invalid_format" {
		t.Errorf("presets in Mars/Base = %d %s", a.status, a.body)
	}

	// A change of the settings never moves a Snooze already set; the Snooze ends like any other.
	organization(func(m map[string]any) { m["workday_start"], m["week_start"] = "10:00", "monday" })
	if got := group(gc)["snooze_until"]; got != today(9*3600).Format(time.RFC3339) {
		t.Errorf("the settings moved the snooze to %v", got)
	}
	at(9*3600 + 60) // 09:01 UTC
	eventually(t, "the snooze to end", func() bool { return group(gc)["status"] == "firing" })
	if g := group(gc); g["owner"] != nil {
		t.Errorf("owner after the snooze %v", g["owner"])
	}
	if e := newest(gc); e["event"] != "snooze_ended" || e["loudness"] != "loud" ||
		fmt.Sprint(e["mentions"]) != "[snooze_ended]" {
		t.Errorf("the newest entry %v", e)
	}
	if _, ends := presets(api, "UTC"); !ends["tomorrow"].Equal(today(10 * 3600)) {
		t.Errorf("tomorrow at 09:01 with 10:00 = %v, want %v", ends["tomorrow"], today(10*3600))
	}

	// C-10.FR-17: at 08:59 the preset ends a minute later, at 09:00 sharp the next day.
	organization(func(m map[string]any) { m["workday_start"] = "09:00" })
	at(8*3600 + 59*60) // 08:59 UTC
	if got := snoozeUntil(api, gd, `{"preset":"tomorrow","time_zone":"UTC"}`); !got.Equal(today(9 * 3600)) {
		t.Errorf("tomorrow at 08:59 = %v, want %v", got, today(9*3600))
	}
	// 09:00 UTC and the fraction of a second the request takes; the exact instant is pinned by snoozepreset_test.go.
	at(9 * 3600)
	if got := snoozeUntil(api, gd, `{"preset":"tomorrow","time_zone":"UTC"}`); !got.Equal(today(86400 + 9*3600)) {
		t.Errorf("tomorrow at 09:00 = %v, want %v", got, today(86400+9*3600))
	}
}
