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
	"github.com/muster-io/muster/internal/fakes/fakebridge"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
)

// mmButtons are the actions of the attachment of a post, each its id, its name and its style, "-" for an action
// without a style field.
func mmButtons(t *testing.T, p mmPost) string {
	t.Helper()
	styles := strings.Fields(mmStyles(t, p))
	names := actionNames(p.attachment(t))
	out := make([]string, len(names))
	for i, name := range names {
		id, style, _ := strings.Cut(styles[i], ":")
		out[i] = id + "=" + name + ":" + style
	}
	return strings.Join(out, ", ")
}

// bridgeButtons are the buttons a message was posted with, each its label and its style hint.
func bridgeButtons(m fakebridge.Message) []string {
	out := []string{}
	for _, b := range m.Envelope.Buttons {
		out = append(out, b.Label+":"+string(b.Style))
	}
	return out
}

// lastTwo are the last two of s.
func lastTwo(s []string) []string {
	return s[max(len(s)-2, 0):]
}

// TestSnoozePresetButtons is the Verification of S-093 against `muster dev` and its fake Mattermost, Telegram and bridge
// servers (C-10.FR-18, C-10.AC-23, C-22.FR-5, C-22.FR-14, C-22.AC-27): the Root message of a firing and of an
// acknowledged Alert Group offers "Until tomorrow 09:00" and "Until Monday 09:00" after its Snooze durations, as the
// Mattermost actions snoozetomorrow and snoozeweek and a third Telegram keyboard row, without a style, and a snoozed
// one, a Route without Snooze durations and nothing else offer them; a Route in Russian reads «До завтра 09:00» and
// «До понедельника 09:00». At 02:00 UTC on the development clock, bob's press in Mattermost ends the Snooze at 09:00 in
// his profile's Asia/Tokyo and the Admin's press in Telegram at 09:00 in the Organization's UTC, with the preset and
// the time zone in the Audit log. A new start of the working day shows at the next re-render, and a press on a button
// rendered before ends at the new start. A press on a preset of a message whose Route lost its Snooze durations is
// refused as unverifiable, and one on a Destination test message changes nothing. A bridge with max_per_message 5, 6
// and 10 gets the durations, then the tomorrow preset, then both, and the Text commands take tomorrow, завтра and the
// first day of the week, list them when the day is another, and refuse them on a Route without Snooze durations.
func TestSnoozePresetButtons(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := reminderEnv(t, h, r)
	session := newAgent(t, r.App)
	if a := session.signIn("bob", "bob-password-1"); a.status != http.StatusCreated {
		t.Fatalf("sign in bob = %d %s", a.status, a.body)
	}
	session.json(http.MethodPut, "/api/v1/me", `{"name":"bob","time_zone":"Asia/Tokyo"}`, http.StatusOK)
	// The Admin's Telegram account 5002; the Admin has no profile time zone, and the Organization's is UTC.
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		created_at) SELECT 1, 'AK0000000000T2', u.id, 'telegram', c.id, '5002', 'admin_tg', now() FROM users u,
		connections c WHERE u.source = 'bootstrap' AND c.name = '`+devmode.TelegramConnectionName+`'`)
	if zones := h.text(t, `SELECT coalesce((SELECT time_zone FROM users WHERE source = 'bootstrap'), '-') || ' ' ||
		(SELECT time_zone FROM organizations)`); zones != "- UTC" {
		t.Fatalf("the Admin's and the Organization's time zones: %s", zones)
	}
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}

	// 02:00 UTC on the development clock, before any Alert Group, so that no timer fires on the way.
	if d := ((2*3600-int(readClock(t, r).Now.Unix()%86400))%86400 + 86400) % 86400; d > 0 {
		advance(t, r, d)
	}
	e.settle()
	e.pollAfterClockMove()
	day := readClock(t, r).Now.UTC().Truncate(24 * time.Hour)
	at := func(hours, minutes int) time.Time {
		return day.Add(time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute)
	}

	const (
		mmDurations = ", snooze0=Snooze 1 h:-, snooze1=Snooze 4 h:-, snooze2=Snooze 24 h:-"
		mmPresets   = ", snoozetomorrow=Until tomorrow 09:00:-, snoozeweek=Until Monday 09:00:-"
		tgDurations = " [Snooze 1 h:-, Snooze 4 h:-, Snooze 24 h:-]"
		tgPresets   = " [Until tomorrow 09:00:-, Until Monday 09:00:-]"
		mmFiring    = "ack=Ack:primary, resolve=Resolve:success" + mmDurations + mmPresets
		tgFiring    = "[Ack:primary, Resolve:success]" + tgDurations + tgPresets
	)
	ruPresets := func(start string) []string {
		return []string{"До завтра " + start, "До понедельника " + start}
	}
	tgRows := func(e *timersEnv, post int64) [][]string {
		e.t.Helper()
		for _, m := range e.messages(faketelegram.ChannelID) {
			if m.ID == post {
				return keyboardRows(e.t, m.ReplyMarkup)
			}
		}
		e.t.Fatalf("no channel post %d", post)
		return nil
	}
	lastRow := func(e *timersEnv, post int64) []string {
		rows := tgRows(e, post)
		if len(rows) == 0 {
			return nil
		}
		return rows[len(rows)-1]
	}
	snoozedUntil := func(e *timersEnv, g string) time.Time {
		e.t.Helper()
		var until time.Time
		eventually(e.t, g+" snoozed", func() bool {
			ag := e.alertGroup(g)
			until, _ = time.Parse(time.RFC3339, fmt.Sprint(ag["snooze_until"]))
			return ag["status"] == "snoozed"
		})
		return until
	}
	type auditEntry struct {
		Transport string         `json:"transport"`
		Details   map[string]any `json:"details"`
	}
	lastSnoozed := func(e *timersEnv) auditEntry {
		e.t.Helper()
		var page struct {
			Items []auditEntry `json:"items"`
		}
		decode(e.t, e.api.do(http.MethodGet, "/api/v1/audit-log?action=alert_group.snoozed&limit=1", ""), &page)
		if len(page.Items) == 0 {
			e.t.Fatal("no alert_group.snoozed entry in the Audit log")
		}
		return page.Items[0]
	}
	snoozes := func() int64 { return h.count(t, `SELECT count(*) FROM audit_log WHERE action = 'alert_group.snoozed'`) }
	organization := func(e *timersEnv, start string) {
		e.t.Helper()
		if a := putOrganization(e.api, func(m map[string]any) { m["workday_start"] = start }); a.status != http.StatusOK {
			e.t.Fatalf("workday_start %s = %d %s", start, a.status, a.body)
		}
	}

	var g, gr string
	t.Run("the buttons in both messengers", func(t *testing.T) {
		e := e.on(t)
		g = e.fire("p1", "HostDown", "t", true)
		n, post := e.number(g), e.rootPost(g)
		waitStyles(t, "the firing mattermost root message", mmFiring, func() string { return mmButtons(t, e.root(n)) })
		waitStyles(t, "the firing telegram root message", tgFiring, func() string { return e.tgPostStyles(post) })

		e.route("ru", "ru", []string{e.mmDest, e.tgDest}, func(p map[string]any) { p["language"] = "ru" })
		gr = e.fire("p2", "DiskFull", "ru", true)
		if names := actionNames(e.root(e.number(gr)).attachment(t)); !slices.Equal(lastTwo(names), ruPresets("09:00")) {
			t.Errorf("the russian mattermost root message %v", names)
		}
		if row := lastRow(e, e.rootPost(gr)); !slices.Equal(row, ruPresets("09:00")) {
			t.Errorf("the last row of the russian telegram keyboard %v", row)
		}

		e.route("nod", "nod", []string{e.mmDest, e.tgDest}, func(p map[string]any) {
			p["snooze_durations_seconds"] = []int64{}
		})
		gn := e.fire("p3", "NoDur", "nod", true)
		if got := mmButtons(t, e.root(e.number(gn))); got != "ack=Ack:primary, resolve=Resolve:success" {
			t.Errorf("the mattermost root message without snooze durations: %s", got)
		}
		if got := e.tgPostStyles(e.rootPost(gn)); got != "[Ack:primary, Resolve:success]" {
			t.Errorf("the telegram root message without snooze durations: %s", got)
		}

		ga := e.fire("p4", "Acked", "t", true)
		na, postA := e.number(ga), e.rootPost(ga)
		e.ack(e.bob, ga)
		waitStyles(t, "the acknowledged mattermost root message", "unack=Unack:-, resolve=Resolve:success"+mmDurations+
			mmPresets, func() string { return mmButtons(t, e.root(na)) })
		waitStyles(t, "the acknowledged telegram root message", "[Unack:-, Resolve:success]"+tgDurations+tgPresets,
			func() string { return e.tgPostStyles(postA) })
		e.cmd(e.bob, ga, "snooze", `{"no_end":true}`)
		waitStyles(t, "the snoozed mattermost root message", "ack=Ack:primary, unsnooze=Unsnooze:-, resolve=Resolve:success",
			func() string { return mmButtons(t, e.root(na)) })
		waitStyles(t, "the snoozed telegram root message", "[Ack:primary, Unsnooze:-, Resolve:success]",
			func() string { return e.tgPostStyles(postA) })
	})

	t.Run("presses at 02:00 UTC", func(t *testing.T) {
		e := e.on(t)
		if now := readClock(t, r).Now; now.Before(at(2, 0)) || !now.Before(at(3, 0)) {
			t.Fatalf("the development clock is at %v", now)
		}
		// bob, whose profile time zone is Asia/Tokyo, in Mattermost: 09:00 in Tokyo is 00:00 UTC tomorrow.
		if res := e.mm.press(e.root(e.number(g)).ID, "snoozetomorrow", fakemattermost.BobUserID); string(res.Answer) !=
			"{}" {
			t.Errorf("bob's press %+v %s", res, res.Answer)
		}
		if until := snoozedUntil(e, g); !until.Equal(at(24, 0)) {
			t.Errorf("bob's preset ends at %v, want %v", until, at(24, 0))
		}
		if a := lastSnoozed(e); a.Transport != "mattermost" || a.Details["preset"] != "tomorrow" ||
			a.Details["time_zone"] != "Asia/Tokyo" {
			t.Errorf("the Audit log entry of bob's press %+v", a)
		}
		if tl := e.timeline(g, "&kind=status"); len(tl) == 0 || tl[0].Event != "snoozed" ||
			tl[0].Actor.Transport != "mattermost" || tl[0].Actor.Name != "bob" {
			t.Errorf("the status entries of %s: %+v", g, tl)
		}

		// The Admin, without a profile time zone, in Telegram: 09:00 in the Organization's UTC, today.
		g2 := e.fire("p5", "Latency", "t", true)
		if a := e.press(e.rootPost(g2), 5002, "Until tomorrow 09:00", ""); a.Text != "Done: Snooze" {
			t.Errorf("the Admin's press %+v", a)
		}
		if until := snoozedUntil(e, g2); !until.Equal(at(9, 0)) {
			t.Errorf("the Admin's preset ends at %v, want %v", until, at(9, 0))
		}
		if a := lastSnoozed(e); a.Transport != "telegram" || a.Details["preset"] != "tomorrow" ||
			a.Details["time_zone"] != "UTC" {
			t.Errorf("the Audit log entry of the Admin's press %+v", a)
		}
	})

	t.Run("a new start of the working day at the next re-render", func(t *testing.T) {
		e := e.on(t)
		old := e.fire("p6", "Backlog", "t", true)
		postOld := e.rootPost(old)
		organization(e, "08:30")
		defer organization(e, "09:00")

		g3 := e.fire("p7", "Queue", "t", true)
		want := []string{"Until tomorrow 08:30", "Until Monday 08:30"}
		if names := actionNames(e.root(e.number(g3)).attachment(t)); !slices.Equal(lastTwo(names), want) {
			t.Errorf("a new mattermost root message %v", names)
		}
		if row := lastRow(e, e.rootPost(g3)); !slices.Equal(row, want) {
			t.Errorf("a new telegram root message %v", row)
		}
		// The Russian Root message keeps its labels until its next change of status.
		nr, postR := e.number(gr), e.rootPost(gr)
		if names := actionNames(e.root(nr).attachment(t)); !slices.Equal(lastTwo(names), ruPresets("09:00")) {
			t.Errorf("the russian mattermost root message before its re-render %v", names)
		}
		if row := lastRow(e, postR); !slices.Equal(row, ruPresets("09:00")) {
			t.Errorf("the russian telegram root message before its re-render %v", row)
		}
		e.ack(e.api, gr)
		acked := append([]string{"Снять подтверждение", "Закрыть", "Отложить на 1 ч", "Отложить на 4 ч",
			"Отложить на 24 ч"}, ruPresets("08:30")...)
		waitStyles(t, "the acknowledged russian mattermost root message", strings.Join(acked, ","), func() string {
			return strings.Join(actionNames(e.root(nr).attachment(t)), ",")
		})
		waitStyles(t, "the acknowledged russian telegram root message", fmt.Sprint([][]string{acked[:2], acked[2:5],
			acked[5:]}), func() string { return fmt.Sprint(tgRows(e, postR)) })

		// A press on a button rendered before the change ends at the new start.
		if row := lastRow(e, postOld); !slices.Equal(row, []string{"Until tomorrow 09:00", "Until Monday 09:00"}) {
			t.Errorf("the telegram root message rendered before the change %v", row)
		}
		if a := e.press(postOld, 5002, "Until tomorrow 09:00", ""); a.Text != "Done: Snooze" {
			t.Errorf("the press on the old button %+v", a)
		}
		if until := snoozedUntil(e, old); !until.Equal(at(8, 30)) {
			t.Errorf("the old button ends at %v, want %v", until, at(8, 30))
		}
	})

	t.Run("a route that lost its snooze durations", func(t *testing.T) {
		e := e.on(t)
		route := e.route("lose", "lose", []string{e.mmDest, e.tgDest}, nil)
		gl := e.fire("p8", "Lost", "lose", true)
		nl, postL := e.number(gl), e.rootPost(gl)
		waitStyles(t, "the mattermost root message", mmFiring, func() string { return mmButtons(t, e.root(nl)) })
		waitStyles(t, "the telegram root message", tgFiring, func() string { return e.tgPostStyles(postL) })
		a := e.api.do(http.MethodGet, "/api/v1/routes/"+route, "")
		var rt map[string]any
		decode(t, a, &rt)
		policy := rt["policy"].(map[string]any)
		policy["snooze_durations_seconds"] = []int64{}
		body, _ := json.Marshal(map[string]any{"name": rt["name"], "matchers": rt["matchers"], "urgent": rt["urgent"],
			"group_key": rt["group_key"], "destination_ids": rt["destination_ids"], "policy": policy})
		e.api.json(http.MethodPut, "/api/v1/routes/"+route, string(body), http.StatusOK, "If-Match", a.header.Get("ETag"))

		before := snoozes()
		if res := e.mm.press(e.root(nl).ID, "snoozetomorrow", fakemattermost.BobUserID); string(res.Answer) !=
			pressAnswer(textNotVerified) {
			t.Errorf("the mattermost press %s", res.Answer)
		}
		if a := e.press(postL, 5001, "Until Monday 09:00", ""); a.Text != textNotVerified {
			t.Errorf("the telegram press %+v", a)
		}
		if s, _ := e.ownerOf(gl); s != "firing" || snoozes() != before {
			t.Errorf("after the presses %s is %s, with %d Snoozes in the Audit log, want %d", gl, s, snoozes(), before)
		}
	})

	t.Run("destination test messages", func(t *testing.T) {
		e := e.on(t)
		groups, before := h.count(t, `SELECT count(*) FROM alert_groups`), snoozes()
		if res := e.destTest(e.mmDest, example); classes(res) != "message:none,press:none" {
			t.Fatalf("the mattermost test %+v", res)
		}
		var test mmPost
		for _, p := range slices.Backward(e.posts()) {
			if p.RootID == "" && len(p.Attachments) == 1 && strings.HasPrefix(p.Attachments[0].Title, "🧪 Test message") {
				test = p
				break
			}
		}
		if test.ID == "" {
			t.Fatal("no mattermost test message")
		}
		if got := mmButtons(t, test); got != mmFiring {
			t.Errorf("the mattermost test message %s", got)
		}
		e.mm.press(test.ID, "snoozetomorrow", fakemattermost.BobUserID)
		if eph := e.ephemeral(); len(eph) == 0 || eph[len(eph)-1].Message != textTestPress ||
			eph[len(eph)-1].UserID != fakemattermost.BobUserID {
			t.Errorf("the answer to the mattermost press %+v", eph)
		}

		if res := e.destTest(e.tgDest, example); classes(res) != "message:none" {
			t.Fatalf("the telegram test %+v", res)
		}
		var tg faketelegram.Message
		for _, m := range slices.Backward(e.messages(faketelegram.ChannelID)) {
			if strings.HasPrefix(m.Text, "🧪 Test message") {
				tg = m
				break
			}
		}
		if got := tgStyles(t, tg.ReplyMarkup); got != tgFiring {
			t.Errorf("the telegram test message %s", got)
		}
		if a := e.press(tg.ID, 5001, "Until Monday 09:00", ""); a.Text != textTestPress {
			t.Errorf("the answer to the telegram press %+v", a)
		}
		if n := h.count(t, `SELECT count(*) FROM alert_groups`); n != groups || snoozes() != before {
			t.Errorf("the presses on test messages changed something: %d Alert Groups, %d Snoozes", n, snoozes())
		}
	})

	t.Run("bridges", func(t *testing.T) {
		b := &brd{mm: e.mm.on(t), fb: h.Fakes.Bridge + "/_fake"}
		for _, item := range b.api.json(http.MethodGet, "/api/v1/connections", "", http.StatusOK)["items"].([]any) {
			if c := item.(map[string]any); c["name"] == devmode.BridgeConnectionName {
				b.conn = c["id"].(string)
			}
		}
		if b.conn == "" {
			t.Fatal("no demo Bridge Connection")
		}
		// The fake's server would wait for an idle connection of the test when muster dev stops.
		t.Cleanup(client.CloseIdleConnections)
		h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id,
			display_name, created_at) SELECT 1, 'AK0000000000B7', u.id, 'bridge', c.id, '`+fakebridge.BobID+`', u.name,
			now() FROM users u, connections c WHERE u.login = 'bob' AND c.public_id = '`+b.conn+`'`)
		bd := b.destination("bridge-alerts", fakebridge.ChannelAlerts)
		b.route("br", "br", []string{bd}, calm)
		b.route("brnod", "brnod", []string{bd}, func(p map[string]any) {
			calm(p)
			p["snooze_durations_seconds"] = []int64{}
		})
		limit := func(n int) {
			t.Helper()
			c := fakebridge.DefaultConfig().Capabilities
			c.Buttons.MaxPerMessage = n
			caps, _ := json.Marshal(c)
			b.configure(`{"capabilities":` + string(caps) + `}`)
			if check := b.api.json(http.MethodPost, "/api/v1/connections/"+b.conn+"/checks", "",
				http.StatusOK); check["ok"] != true {
				t.Fatalf("the connection check with max_per_message %d: %v", n, check)
			}
		}

		// C-22.FR-5: beyond max_per_message the week_start preset goes first, then the tomorrow preset.
		firing := []string{"Ack:primary", "Resolve:success", "Snooze 1 h:neutral", "Snooze 4 h:neutral",
			"Snooze 24 h:neutral", "Until tomorrow 09:00:neutral", "Until Monday 09:00:neutral"}
		roots, ids := map[string]fakebridge.Message{}, map[string]string{}
		for _, c := range []struct {
			limit, buttons int
			title          string
		}{{5, 5, "Five"}, {6, 6, "Six"}, {10, 7, "Seven"}} {
			limit(c.limit)
			ids[c.title] = b.start("b"+c.title, c.title, "br", strings.ToLower(c.title))
			roots[c.title] = b.waitPosted(c.title, 1)[0]
			if got := bridgeButtons(roots[c.title]); !slices.Equal(got, firing[:c.buttons]) {
				t.Errorf("max_per_message %d: %v", c.limit, got)
			}
		}

		// C-22.FR-14: the preset words of the Text commands, in bob's profile time zone.
		said := func(title, text, want string) {
			t.Helper()
			if a := b.answer(b.say(roots[title].ID, fakebridge.BobID, text)); a.Text != want {
				t.Errorf("the answer to %q: %+v, want %q", text, a, want)
			}
		}
		done := func(title string) string { return fmt.Sprintf("Done: Snoozed #%d", b.number(ids[title])) }
		bridgeUntil := func(title string) time.Time {
			t.Helper()
			until, err := time.Parse(time.RFC3339, fmt.Sprint(b.alertGroup(ids[title])["snooze_until"]))
			if err != nil || b.status(ids[title]) != "snoozed" {
				t.Errorf("%s is %s until %v", title, b.status(ids[title]), b.alertGroup(ids[title])["snooze_until"])
			}
			return until
		}
		said("Seven", "snooze friday", "Snooze for one of: 1h, 4h, 24h, tomorrow, monday")
		if s := b.status(ids["Seven"]); s != "firing" {
			t.Errorf("after snooze friday: %s", s)
		}
		said("Seven", "/Snooze Tomorrow", done("Seven"))
		if until := bridgeUntil("Seven"); !until.Equal(at(24, 0)) {
			t.Errorf("/Snooze Tomorrow ends at %v, want %v", until, at(24, 0))
		}
		if a := lastSnoozed(e.on(t)); a.Transport != "bridge" || a.Details["preset"] != "tomorrow" ||
			a.Details["time_zone"] != "Asia/Tokyo" {
			t.Errorf("the Audit log entry of /Snooze Tomorrow %+v", a)
		}
		local := readClock(t, r).Now.In(tokyo)
		monday := time.Date(local.Year(), local.Month(), local.Day()+(int(time.Monday)-int(local.Weekday())+7)%7, 9, 0,
			0, 0, tokyo)
		if !monday.After(local) {
			monday = monday.AddDate(0, 0, 7)
		}
		said("Six", "отложить понедельник", done("Six"))
		if until := bridgeUntil("Six"); !until.Equal(monday) {
			t.Errorf("отложить понедельник ends at %v, want %v", until, monday)
		}
		said("Five", "отложить завтра", done("Five"))
		if until := bridgeUntil("Five"); !until.Equal(at(24, 0)) {
			t.Errorf("отложить завтра ends at %v, want %v", until, at(24, 0))
		}

		// A Route without Snooze durations offers no preset, as a button or as a Text command.
		nod := b.start("bNoDur", "NoDurations", "brnod", "nodurations")
		roots["NoDurations"] = b.waitPosted("NoDurations", 1)[0]
		if got := bridgeButtons(roots["NoDurations"]); !slices.Equal(got, firing[:2]) {
			t.Errorf("the bridge root message without snooze durations: %v", got)
		}
		said("NoDurations", "snooze tomorrow", "This Alert Group's Route has no Snooze durations; nothing was changed.")
		if s := b.status(nod); s != "firing" {
			t.Errorf("after snooze tomorrow without snooze durations: %s", s)
		}

		// A press on a preset of a test message changes nothing.
		before := snoozes()
		b.destTest(bd, example)
		tests := b.testMessages()
		if len(tests) != 1 || !slices.Equal(bridgeButtons(tests[0]), firing) {
			t.Fatalf("the bridge test messages %+v", tests)
		}
		if a := b.answer(b.press(tests[0].ID, fakebridge.BobID, "Until tomorrow 09:00")); a.Text != textTestPress {
			t.Errorf("the answer to the press on the test message %+v", a)
		}
		if snoozes() != before {
			t.Errorf("the press on the test message snoozed: %d Snoozes, want %d", snoozes(), before)
		}
	})
}
