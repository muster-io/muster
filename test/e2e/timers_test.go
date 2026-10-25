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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/groups"
)

// timersEnv drives the ack timeouts and Reminders of C-17: the Admin's Personal access token of mm, the Responder bob
// with a token of his own, a Service account token with the Role responder, the Mattermost Destination "alerts" with
// the Mention @channel for ack timeout notices and the Telegram Destination "alerts". Every call uses a token, since
// the development clock moves by days.
type timersEnv struct {
	*tgd
	bob    patClient
	sat    patClient
	mmConn string
	mmDest string
	tgDest string
}

func (e *timersEnv) on(t *testing.T) *timersEnv {
	c := *e
	c.tgd = &tgd{mm: e.mm.on(t), ftg: e.ftg, conn: e.conn}
	c.bob.t, c.sat.t = t, t
	return &c
}

// responder creates the Responder name with a password, signs them in and returns their id and a Personal access
// token client.
func (e *timersEnv) responder(name string) (string, patClient) {
	e.t.Helper()
	created := e.api.json(http.MethodPost, "/api/v1/users", `{"name":"`+name+`","login":"`+name+
		`","role":"responder"}`, http.StatusCreated)
	id := created["user"].(map[string]any)["id"].(string)
	link, err := url.Parse(created["password_setup_link"].(map[string]any)["url"].(string))
	if err != nil {
		e.t.Fatal(err)
	}
	setup, _ := strings.CutPrefix(link.Fragment, "token=")
	if a := call(e.t, http.MethodPost, e.r.App+"/api/v1/password-setups", `{"token":"`+setup+
		`","password":"`+name+`-password-1"}`, "Content-Type", "application/json"); a.status != http.StatusNoContent {
		e.t.Fatalf("password setup of %s = %d %s", name, a.status, a.body)
	}
	session := newAgent(e.t, e.r.App)
	if a := session.signIn(name, name+"-password-1"); a.status != http.StatusCreated {
		e.t.Fatalf("sign in %s = %d %s", name, a.status, a.body)
	}
	perms, _ := json.Marshal(session.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := session.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"timers","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	return id, patClient{t: e.t, base: e.r.App, token: token}
}

// settle waits until every timer due on the development clock has fired and the messages and events they caused
// reached the healthy Destinations, so that the next advance never collapses notices that were due one by one.
func (e *timersEnv) settle() {
	e.t.Helper()
	now := readClock(e.t, e.r).Now.UTC().Format(time.RFC3339Nano)
	eventually(e.t, "the due timers and their messages", func() bool {
		return e.h.count(e.t, `SELECT (SELECT count(*) FROM timers WHERE deadline <= '`+now+`')
			+ (SELECT count(*) FROM thread_replies r JOIN destinations d ON d.id = r.destination_id
				WHERE r.state = 'pending' AND d.health = 'healthy')
			+ (SELECT count(*) FROM deliveries x JOIN destinations d ON d.id = x.destination_id
				WHERE x.state = 'pending' AND d.health = 'healthy')
			+ (SELECT count(*) FROM webhook_events WHERE state = 'pending')`) == 0
	})
}

// adv moves the development clock by seconds and settles.
func (e *timersEnv) adv(seconds ...int) {
	e.t.Helper()
	for _, s := range seconds {
		advance(e.t, e.r, s)
		e.settle()
	}
}

// fire starts a firing Alert Group of one warning Alert whose cluster label is cluster, on the Route of team, and
// waits until it is published where its Route has a Destination.
func (e *timersEnv) fire(cluster, alertname, team string, published bool) string {
	e.t.Helper()
	e.group(cluster, alertname)
	e.alert(cluster, "a", e.labels(team, cluster, "warning"), "")
	e.notify(cluster, "first notification")
	id := e.groupOf(cluster)
	if published {
		e.waitPublished(id)
	}
	e.settle()
	return id
}

func (e *timersEnv) labels(team, cluster, severity string) string {
	return `{"team":"` + team + `","cluster":"` + cluster + `","severity":"` + severity + `"}`
}

func (e *timersEnv) waitPublished(id string) {
	e.t.Helper()
	eventually(e.t, "the first publication of "+id, func() bool { return e.firstPublished(id) != "" })
}

// firstPublished is first_published_at of the Alert Group, empty while it has none.
func (e *timersEnv) firstPublished(id string) string {
	e.t.Helper()
	if e.h.count(e.t, `SELECT count(*) FROM alert_groups WHERE public_id = '`+id+
		`' AND first_published_at IS NOT NULL`) == 0 {
		return ""
	}
	return fmt.Sprint(e.h.count(e.t, `SELECT (extract(epoch FROM first_published_at) * 1000000)::bigint
		FROM alert_groups WHERE public_id = '`+id+`'`))
}

// notice is what the shell checks of the Verification print for an Alert Group: the kind of its next notice, the
// minutes until it on the development clock, -1 without one, and whether it is Unclaimed.
type notice struct {
	Kind      string
	Min       int
	Unclaimed bool
}

func (e *timersEnv) due(id string) notice {
	e.t.Helper()
	g := e.alertGroup(id)
	now := readClock(e.t, e.r).Now
	n := notice{Min: -1}
	n.Unclaimed, _ = g["unclaimed"].(bool)
	if next, ok := g["next_notice"].(map[string]any); ok && next != nil {
		n.Kind, _ = next["kind"].(string)
		at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(next["at"]))
		if err != nil {
			e.t.Fatalf("next_notice %v: %v", next, err)
		}
		n.Min = int(math.Round(at.Sub(now).Minutes()))
	}
	return n
}

func (e *timersEnv) wantDue(id string, want notice) {
	e.t.Helper()
	if got := e.due(id); got != want {
		e.t.Errorf("next notice of %s = %+v, want %+v", id, got, want)
	}
}

// timerEntry is a Timeline entry of a lifecycle event, with the fields of the kinds timers and status.
type timerEntry struct {
	Kind          string         `json:"kind"`
	Event         string         `json:"event"`
	Notice        *int           `json:"notice_number"`
	Missed        *int           `json:"missed_count"`
	Loudness      string         `json:"loudness"`
	Mentions      []string       `json:"mentions"`
	Reason        *string        `json:"reason"`
	ReasonCode    *string        `json:"reason_code"`
	PreviousOwner map[string]any `json:"previous_owner"`
	ToStatus      string         `json:"to_status"`
}

// entries are the Timeline entries of the Alert Group, oldest first, of the kinds in query (such as "&kind=timers").
func (e *timersEnv) entries(id, query string) []timerEntry {
	e.t.Helper()
	var page struct {
		Items []timerEntry `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+id+"/timeline?limit=200&order=asc"+query, ""), &page)
	return page.Items
}

func eventsOf(entries []timerEntry) []string {
	out := []string{}
	for _, x := range entries {
		out = append(out, x.Event)
	}
	return out
}

// notices are the notice numbers of the entries of event.
func noticesOf(entries []timerEntry, event string) []int {
	out := []int{}
	for _, x := range entries {
		if x.Event == event && x.Notice != nil {
			out = append(out, *x.Notice)
		}
	}
	return out
}

// threadOf are the Thread replies of the live Root message of the Alert Group in the fake Mattermost.
func (e *timersEnv) threadOf(id string) []mmPost {
	e.t.Helper()
	return e.replies(e.root(e.number(id)).ID)
}

func (e *timersEnv) still(c patClient, id string) answer {
	e.t.Helper()
	return c.do(http.MethodPost, "/api/v1/alert-groups/"+id+"/still-on-it", "")
}

func (e *timersEnv) ack(c patClient, id string) {
	e.t.Helper()
	c.json(http.MethodPost, "/api/v1/alert-groups/"+id+"/acknowledge", "", http.StatusOK)
	e.settle()
}

func (e *timersEnv) cmd(c patClient, id, command, body string) {
	e.t.Helper()
	c.json(http.MethodPost, "/api/v1/alert-groups/"+id+"/"+command, body, http.StatusOK)
	e.settle()
}

// offersStillOnIt says whether allowed_commands of the Alert Group, read by c, has still_on_it.
func (e *timersEnv) offersStillOnIt(c patClient, id string) bool {
	e.t.Helper()
	g := c.json(http.MethodGet, "/api/v1/alert-groups/"+id, "", http.StatusOK)
	for _, x := range g["allowed_commands"].([]any) {
		if x == "still_on_it" {
			return true
		}
	}
	return false
}

func (e *timersEnv) ownerOf(id string) (string, any) {
	e.t.Helper()
	g := e.alertGroup(id)
	return g["status"].(string), g["owner"]
}

// timerFired are the timer_fired log lines of the Alert Group from every replica: the replica that claims a timer
// fires it.
func (e *timersEnv) timerFired(id string) []map[string]any {
	var out []map[string]any
	for _, r := range e.h.Replicas {
		for line := range strings.Lines(r.Output()) {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && m["event"] == "timer_fired" && m["group"] == id {
				out = append(out, m)
			}
		}
	}
	return out
}

// TestTimers is S-049 against `muster dev`, with the time driven by the development clock: the ack timeout notices at
// 15, 45 and 105 minutes after the first Publication and Unclaimed (C-17.AC-1, AC-6, FR-3, FR-9), its end and return
// (C-17.AC-8), starting over (C-17.AC-2, C-09.FR-8) after Unacknowledge, a Snooze ending, Reopen into firing, Unsnooze,
// Unresolve and a rise to Urgent, no ack timeout before the first Publication and none started over by a
// republication (C-17.FR-1), the collapse of overdue notices (C-17.FR-7), the Informational profile (C-17.AC-4), the
// Reminders, a Takeover and "Still on it" (C-17.AC-3, FR-10), auto-unacknowledge (C-17.AC-5) and not with every
// Destination Broken (C-17.FR-5), a Reopen into acknowledged, the release of a disabled and a deleted Owner (C-17.FR-8,
// C-03.AC-27), and an events-mode outgoing webhook as the only Destination (C-17.FR-1, FR-5, C-15.AC-11, C-17.AC-9).
func TestTimers(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	base := newTGD(t, h, r)
	e := &timersEnv{tgd: base}
	_, e.bob = e.responder("bob")
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	sa := admin.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"pager","role":"responder"}`,
		http.StatusCreated)["id"].(string)
	e.sat = patClient{t: t, base: r.App, token: admin.json(http.MethodPost, "/api/v1/service-accounts/"+sa+"/tokens",
		`{"name":"pager"}`, http.StatusCreated)["value"].(string)}
	e.mmConn = e.connection(50)
	e.mmDest = e.destination("alerts", e.mmConn, fakemattermost.ChannelAlerts, map[string]string{"ack_timeout": "channel"},
		50)
	e.member(faketelegram.GroupID, faketelegram.Member{Status: faketelegram.StatusAdministrator, CanPostMessages: true})
	a := e.create("tg-alerts", "@muster_alerts", 60)
	if a.status != http.StatusCreated {
		t.Fatalf("telegram destination = %d %s", a.status, a.body)
	}
	var tg struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	decode(t, a, &tg)
	e.tgDest = tg.Destination.ID
	e.route("t", "t", []string{e.mmDest}, nil)

	t.Run("ack timeout and unclaimed", func(t *testing.T) {
		e := e.on(t)
		g1 := e.fire("g1", "DiskFull", "t", true)
		e.wantDue(g1, notice{"ack_timeout", 15, false})
		e.adv(900)
		tl := e.entries(g1, "&kind=timers")
		if len(tl) != 1 || tl[0].Event != "ack_timeout" || *tl[0].Notice != 1 || tl[0].Loudness != "loud" ||
			!slices.Equal(tl[0].Mentions, []string{"ack_timeout"}) || tl[0].Kind != "timers" {
			t.Errorf("after 15 minutes %+v", tl)
		}
		e.wantDue(g1, notice{"ack_timeout", 30, false})
		thread := e.threadOf(g1)
		if len(thread) != 1 || !strings.HasPrefix(thread[0].Message, "@channel") ||
			len(thread[0].Attachments) > 0 && len(thread[0].Attachments[0].Actions) > 0 {
			t.Errorf("the notice in the thread: %+v", thread)
		}
		e.adv(1800)
		e.wantDue(g1, notice{"ack_timeout", 60, false})
		e.adv(3600)
		tl = e.entries(g1, "&kind=timers")
		if got := eventsOf(tl); !slices.Equal(got, []string{"ack_timeout", "ack_timeout", "ack_timeout", "unclaimed"}) ||
			!slices.Equal(noticesOf(tl, "ack_timeout"), []int{1, 2, 3}) || tl[3].Loudness != "quiet" ||
			len(tl[3].Mentions) != 0 {
			t.Errorf("after 105 minutes %+v", tl)
		}
		e.wantDue(g1, notice{"", -1, true})
		var list struct {
			Items []struct {
				ID        string `json:"id"`
				Unclaimed bool   `json:"unclaimed"`
			} `json:"items"`
		}
		decode(t, e.api.do(http.MethodGet, "/api/v1/alert-groups?unclaimed=true", ""), &list)
		if len(list.Items) != 1 || list.Items[0].ID != g1 || !list.Items[0].Unclaimed {
			t.Errorf("unclaimed list %+v", list.Items)
		}
		if c := e.api.json(http.MethodGet, "/api/v1/alert-group-counts?unclaimed=true", "", http.StatusOK); c["all"] !=
			float64(1) {
			t.Errorf("unclaimed counts %v", c)
		}
		n := e.number(g1)
		eventually(t, "the unclaimed root message", func() bool {
			return strings.Contains(e.root(n).attachment(t).Text, "Nobody has taken this")
		})
		if got := len(e.threadOf(g1)); got != 3 {
			t.Errorf("%d thread replies, want the 3 notices", got)
		}
		e.adv(7200)
		if got := noticesOf(e.entries(g1, "&kind=timers"), "ack_timeout"); len(got) != 3 {
			t.Errorf("a fourth notice: %v", got)
		}

		// C-17.AC-8: Acknowledge ends Unclaimed; after Unacknowledge it returns with the third notice of the new one.
		e.ack(e.bob, g1)
		e.wantDue(g1, notice{"reminder", 240, false})
		eventually(t, "the mark to go", func() bool {
			return !strings.Contains(e.root(n).attachment(t).Text, "Nobody has taken this")
		})
		e.cmd(e.bob, g1, "unacknowledge", "")
		e.adv(900, 1800)
		e.wantDue(g1, notice{"ack_timeout", 60, false})
		e.adv(3600)
		e.wantDue(g1, notice{"", -1, true})
		// Snooze and Resolve end it too.
		e.cmd(e.api, g1, "snooze", `{"no_end":true}`)
		e.wantDue(g1, notice{"", -1, false})
		e.cmd(e.api, g1, "unsnooze", "")
		e.adv(900, 1800, 3600)
		e.wantDue(g1, notice{"", -1, true})
		e.cmd(e.api, g1, "resolve", `{}`)
		e.wantDue(g1, notice{"", -1, false})
		if c := e.api.json(http.MethodGet, "/api/v1/alert-group-counts?unclaimed=true", "", http.StatusOK); c["all"] !=
			float64(0) {
			t.Errorf("unclaimed counts after the resolve %v", c)
		}
	})

	t.Run("start over", func(t *testing.T) {
		e := e.on(t)
		// C-17.AC-2: acknowledged at minute 20, unacknowledged at minute 30, the next notice at minute 45.
		g2 := e.fire("g2", "QueueFull", "t", true)
		e.adv(1200)
		if got := len(e.entries(g2, "&kind=timers")); got != 1 {
			t.Errorf("%d timer entries at minute 20", got)
		}
		e.ack(e.bob, g2)
		if d := e.due(g2); d.Kind != "reminder" {
			t.Errorf("the acknowledgement left %+v", d)
		}
		e.adv(600)
		e.cmd(e.bob, g2, "unacknowledge", "")
		e.wantDue(g2, notice{"ack_timeout", 15, false})

		// C-09.FR-8: a Snooze that ends while the Alerts fire.
		g9 := e.fire("g9", "CertExpiry", "t", true)
		until := readClock(t, e.r).Now.Add(time.Hour).UTC().Format(time.RFC3339)
		e.cmd(e.api, g9, "snooze", `{"until":"`+until+`"}`)
		e.adv(3600)
		eventually(t, "the snooze to end", func() bool { s, _ := e.ownerOf(g9); return s == "firing" })
		last := e.entries(g9, "")
		if x := last[len(last)-1]; x.Event != "snooze_ended" || x.Loudness != "loud" {
			t.Errorf("the last entry %+v", x)
		}
		if _, owner := e.ownerOf(g9); owner != nil {
			t.Errorf("owner after the snooze %v", owner)
		}
		e.wantDue(g9, notice{"ack_timeout", 15, false})

		// Reopen into firing, Unsnooze, Unresolve and a rise to Urgent, ten minutes after the first Publication.
		g16 := e.fire("g16", "Reopen", "t", true)
		g17 := e.fire("g17", "Urgent", "t", true)
		g18 := e.fire("g18", "Unsnooze", "t", true)
		g19 := e.fire("g19", "Unresolve", "t", true)
		e.alert("g16", "a", e.labels("t", "g16", "warning"), `,"status":"resolved"`)
		e.notify("g16", "some alerts resolved")
		e.cmd(e.api, g17, "snooze", `{"no_end":true}`)
		e.cmd(e.api, g18, "snooze", `{"no_end":true}`)
		e.cmd(e.api, g19, "resolve", `{}`)
		e.adv(600)
		for _, g := range []string{g16, g17, g18, g19} {
			if d := e.due(g); d.Kind != "" {
				t.Errorf("%s has %+v while not firing", g, d)
			}
		}
		e.alert("g16", "a", e.labels("t", "g16", "warning"), `,"status":"firing","starts_at":"now"`)
		e.notify("g16", "new alerts added")
		e.alert("g17", "b", e.labels("t", "g17", "critical"), "")
		e.notify("g17", "new alerts added")
		e.cmd(e.api, g18, "unsnooze", "")
		e.cmd(e.api, g19, "unresolve", "")
		e.settle()
		for name, g := range map[string]string{"reopen": g16, "urgent": g17, "unsnooze": g18, "unresolve": g19} {
			if s, _ := e.ownerOf(g); s != "firing" {
				t.Errorf("%s: status %s", name, s)
			}
			if d := e.due(g); d != (notice{"ack_timeout", 15, false}) {
				t.Errorf("%s: next notice %+v, want the ack timeout started over", name, d)
			}
		}
		if got := eventsOf(e.entries(g17, "&kind=status")); !slices.Contains(got, "urgency_raised") {
			t.Errorf("g17 %v", got)
		}
		if got := eventsOf(e.entries(g16, "&kind=status")); !slices.Contains(got, "reopened") {
			t.Errorf("g16 %v", got)
		}
		for _, g := range []string{g2, g9, g16, g17, g18, g19} {
			e.cmd(e.api, g, "resolve", `{}`)
		}
	})

	t.Run("publication", func(t *testing.T) {
		e := e.on(t)
		// No ack timeout before the first Publication: a Route without Destinations, then one is added.
		nd := e.route("nd", "nd", []string{}, nil)
		g14 := e.fire("g14", "NoDest", "nd", false)
		e.wantDue(g14, notice{"", -1, false})
		e.adv(1200)
		if got := e.entries(g14, "&kind=timers"); len(got) != 0 {
			t.Errorf("timer entries without a publication %+v", got)
		}
		got := e.api.do(http.MethodGet, "/api/v1/routes/"+nd, "")
		var full map[string]any
		decode(t, got, &full)
		body, _ := json.Marshal(map[string]any{"name": full["name"], "matchers": full["matchers"],
			"urgent": full["urgent"], "group_key": full["group_key"], "destination_ids": []string{e.mmDest},
			"policy": full["policy"]})
		if a := e.api.do(http.MethodPut, "/api/v1/routes/"+nd, string(body), "If-Match",
			got.header.Get("ETag")); a.status != http.StatusOK {
			t.Fatalf("add the destination = %d %s", a.status, a.body)
		}
		e.waitPublished(g14)
		e.settle()
		e.wantDue(g14, notice{"ack_timeout", 15, false})

		// A Root message deleted in Mattermost and republished does not start the ack timeout over.
		g15 := e.fire("g15", "Deleted", "t", true)
		published := e.firstPublished(g15)
		e.adv(600)
		n := e.number(g15)
		root := e.root(n)
		e.fake(http.MethodDelete, e.fmm+"/posts/"+root.ID, "")
		e.alert("g15", "b", `{"team":"t","cluster":"g15","severity":"warning","pod":"b"}`, "")
		e.notify("g15", "new alerts added")
		eventually(t, "the republication", func() bool { return e.root(n).ID != root.ID })
		e.settle()
		if got := e.firstPublished(g15); got != published {
			t.Errorf("first_published_at %s became %s", published, got)
		}
		e.wantDue(g15, notice{"ack_timeout", 5, false})
		for _, g := range []string{g14, g15} {
			e.cmd(e.api, g, "resolve", `{}`)
		}
	})

	t.Run("collapse", func(t *testing.T) {
		e := e.on(t)
		// C-17.FR-7: notices overdue together collapse into one Thread reply.
		g5 := e.fire("g5", "Overdue", "t", true)
		e.adv(7200)
		tl := e.entries(g5, "&kind=timers")
		if len(tl) != 2 || tl[0].Event != "notices_missed" || tl[0].Missed == nil || *tl[0].Missed != 3 ||
			tl[0].Loudness != "loud" || !slices.Equal(tl[0].Mentions, []string{"ack_timeout"}) ||
			tl[1].Event != "unclaimed" || tl[1].Missed != nil {
			t.Errorf("collapsed %+v", tl)
		}
		thread := e.threadOf(g5)
		if len(thread) != 1 || !strings.Contains(thread[0].Message, "3 notices were missed while Muster was unavailable.") {
			t.Errorf("thread %+v", thread)
		}
		if lines := e.timerFired(g5); len(lines) != 1 || lines[0]["missed"] != float64(3) {
			t.Errorf("timer_fired lines %v", lines)
		}
		e.cmd(e.api, g5, "resolve", `{}`)
	})

	t.Run("informational", func(t *testing.T) {
		e := e.on(t)
		// C-17.AC-4: no next notice, no ack timeout notice and no Reminder.
		e.route("info", "info", []string{e.mmDest}, func(p map[string]any) {
			var profiles struct {
				Items []struct {
					ID     string         `json:"id"`
					Policy map[string]any `json:"policy"`
				} `json:"items"`
			}
			decode(t, e.api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
			for _, x := range profiles.Items {
				if x.ID == "informational" {
					clear(p)
					for k, v := range x.Policy {
						p[k] = v
					}
				}
			}
		})
		g6 := e.fire("g6", "Info", "info", true)
		e.wantDue(g6, notice{"", -1, false})
		e.adv(7200)
		e.ack(e.bob, g6)
		e.wantDue(g6, notice{"", -1, false})
		e.adv(18000)
		if got := e.entries(g6, "&kind=timers"); len(got) != 0 {
			t.Errorf("timer entries %+v", got)
		}
		e.cmd(e.api, g6, "resolve", `{}`)
	})

	t.Run("reminders and still on it", func(t *testing.T) {
		e := e.on(t)
		e.route("rem", "rem", []string{e.mmDest, e.tgDest}, nil)
		g3 := e.fire("g3", "HostDown", "rem", true)
		e.ack(e.bob, g3)
		if a := e.still(e.bob, g3); a.status != http.StatusConflict || codeOf(t, a) != "no_reminder_pending" {
			t.Errorf("before a reminder = %d %s", a.status, a.body)
		}
		e.adv(14400)
		tl := e.entries(g3, "&kind=timers")
		if x := tl[len(tl)-1]; x.Event != "reminder" || *x.Notice != 1 || x.Loudness != "loud" ||
			!slices.Equal(x.Mentions, []string{"owner"}) {
			t.Errorf("hour 4 %+v", tl)
		}
		if !e.offersStillOnIt(e.bob, g3) || e.offersStillOnIt(e.api, g3) ||
			e.offersStillOnIt(e.sat, g3) {
			t.Error("allowed_commands offers still_on_it to someone else than the Owner")
		}
		if a := e.still(e.api, g3); a.status != http.StatusConflict || codeOf(t, a) != "not_owner" {
			t.Errorf("the admin = %d %s", a.status, a.body)
		}
		if a := e.still(e.sat, g3); a.status != http.StatusConflict || codeOf(t, a) != "owner_must_be_user" {
			t.Errorf("the service account = %d %s", a.status, a.body)
		}
		e.adv(3600)
		if out := e.bob.json(http.MethodPost, "/api/v1/alert-groups/"+g3+"/still-on-it", "",
			http.StatusOK)["outcome"]; out != "done" {
			t.Errorf("still on it = %v", out)
		}
		if e.offersStillOnIt(e.bob, g3) {
			t.Error("still_on_it is offered after the answer")
		}
		e.wantDue(g3, notice{"reminder", 420, false})
		if got := metricValue(t, e.r,
			`muster_commands_total{command="still_on_it",transport="api",outcome="done"}`); got != 1 {
			t.Errorf("muster_commands_total still_on_it done = %d", got)
		}
		answered := e.entries(g3, "&kind=timers")
		if x := answered[len(answered)-1]; x.Event != "reminder_answered" || x.Loudness != "quiet" || len(x.Mentions) != 0 {
			t.Errorf("the answer %+v", x)
		}
		e.adv(25200, 57600, 86400)
		if got := noticesOf(e.entries(g3, "&kind=timers"), "reminder"); !slices.Equal(got, []int{1, 2, 3, 4}) {
			t.Errorf("reminders %v", got)
		}
		reminders := 0
		for _, p := range e.threadOf(g3) {
			if len(p.Attachments) == 1 && strings.Contains(p.Attachments[0].Text, "Reminder: bob, are you still on it?") {
				reminders++
				// The buttons of S-068: "Still on it" and "Unack".
				if a := p.Attachments[0].Actions; len(a) != 2 || a[0].ID != "stillonit" || a[1].ID != "unack" {
					t.Errorf("a reminder without its buttons %+v", p)
				}
			}
		}
		if reminders != 4 {
			t.Errorf("%d reminders in the mattermost thread", reminders)
		}
		inTelegram := 0
		for _, m := range e.botReplies() {
			if strings.Contains(m.Text, "are you still on it") {
				inTelegram++
			}
		}
		if inTelegram != 4 {
			t.Errorf("%d reminders in telegram", inTelegram)
		}

		// C-10.FR-4: a Takeover at hour 13 moves the next Reminder to hour 17 and mentions the new Owner.
		g4 := e.fire("g4", "HostDown2", "t", true)
		e.ack(e.api, g4)
		e.adv(14400, 28800, 3600)
		e.ack(e.bob, g4)
		e.wantDue(g4, notice{"reminder", 240, false})
		e.adv(14400)
		tl = e.entries(g4, "&kind=timers")
		if got := noticesOf(tl, "reminder"); !slices.Equal(got, []int{1, 2, 1}) {
			t.Errorf("reminders of g4 %v", got)
		}
		thread := e.threadOf(g4)
		if x := thread[len(thread)-1]; !strings.Contains(x.attachment(t).Text, "Reminder: bob, are you still on it?") {
			t.Errorf("the reminder after the takeover %+v", x)
		}

		// A snoozed Alert Group gets none.
		e.cmd(e.bob, g4, "snooze", `{"no_end":true}`)
		e.wantDue(g4, notice{"", -1, false})
		e.adv(86400)
		if got := noticesOf(e.entries(g4, "&kind=timers"), "reminder"); len(got) != 3 {
			t.Errorf("reminders while snoozed %v", got)
		}
		for _, g := range []string{g3, g4} {
			e.cmd(e.api, g, "resolve", `{}`)
		}
	})

	t.Run("auto-unacknowledge", func(t *testing.T) {
		e := e.on(t)
		// C-17.AC-5: two unanswered Reminders; a Note by the Owner at hour 5 answers the first one.
		e.route("au", "au", []string{e.mmDest}, func(p map[string]any) { p["auto_unacknowledge"] = true })
		g7 := e.fire("g7", "Disk", "au", true)
		g8 := e.fire("g8", "Disk2", "au", true)
		e.ack(e.bob, g7)
		e.ack(e.bob, g8)
		e.adv(14400, 3600)
		e.bob.json(http.MethodPost, "/api/v1/alert-groups/"+g8+"/notes", `{"body":"Still checking the disk."}`,
			http.StatusCreated)
		e.adv(25200, 57600)
		if s, owner := e.ownerOf(g7); s != "firing" || owner != nil {
			t.Errorf("g7 %s %v", s, owner)
		}
		st := e.entries(g7, "&kind=status")
		x := st[len(st)-1]
		if x.Event != "auto_unacknowledged" || x.Reason == nil || *x.Reason != "No answer to the last two Reminders" ||
			x.ReasonCode == nil || *x.ReasonCode != "reminders_unanswered" || x.Loudness != "loud" ||
			!slices.Equal(x.Mentions, []string{"owner"}) || x.PreviousOwner == nil {
			t.Errorf("g7 %+v", x)
		}
		if got := noticesOf(e.entries(g7, "&kind=timers"), "reminder"); !slices.Equal(got, []int{1, 2}) {
			t.Errorf("reminders of g7 %v", got)
		}
		thread := e.threadOf(g7)
		if m := thread[len(thread)-1].Message; !strings.Contains(m, "nobody answered the Reminders") ||
			!strings.Contains(m, "bob") {
			t.Errorf("the release in the thread %q", m)
		}
		e.wantDue(g7, notice{"ack_timeout", 15, false})
		if got := noticesOf(e.entries(g8, "&kind=timers"), "reminder"); !slices.Equal(got, []int{1, 2, 3}) {
			t.Errorf("reminders of g8 %v", got)
		}
		if s, _ := e.ownerOf(g8); s != "acknowledged" {
			t.Errorf("g8 %s", s)
		}
		for _, g := range []string{g7, g8} {
			e.cmd(e.api, g, "resolve", `{}`)
		}
	})

	t.Run("every destination broken", func(t *testing.T) {
		e := e.on(t)
		// C-17.FR-5: Reminders that reach no Destination are not unanswered, so no auto-unacknowledge follows.
		broken := e.destination("broken", e.mmConn, fakemattermost.ChannelAlertsProd, nil, 50)
		e.route("br", "br", []string{broken}, func(p map[string]any) { p["auto_unacknowledge"] = true })
		e.fake(http.MethodDelete, e.fmm+"/channels/"+fakemattermost.ChannelAlertsProd+"/members/"+
			fakemattermost.BotUserID, "")
		g12 := e.fire("g12", "Unreachable", "br", false)
		e.waitHealth(broken, "broken")
		e.ack(e.bob, g12)
		e.adv(14400, 28800, 57600)
		if got := noticesOf(e.entries(g12, "&kind=timers"), "reminder"); !slices.Equal(got, []int{1, 2, 3}) {
			t.Errorf("reminders %v", got)
		}
		if s, owner := e.ownerOf(g12); s != "acknowledged" || owner == nil {
			t.Errorf("g12 %s %v", s, owner)
		}
		if got := eventsOf(e.entries(g12, "&kind=status")); slices.Contains(got, "auto_unacknowledged") {
			t.Errorf("auto-unacknowledged while broken: %v", got)
		}
		e.cmd(e.api, g12, "resolve", `{}`)
	})

	t.Run("reopen into acknowledged", func(t *testing.T) {
		e := e.on(t)
		g20 := e.fire("g20", "Flap", "t", true)
		e.ack(e.bob, g20)
		e.adv(14400)
		e.alert("g20", "a", e.labels("t", "g20", "warning"), `,"status":"resolved"`)
		e.notify("g20", "some alerts resolved")
		e.settle()
		e.wantDue(g20, notice{"", -1, false})
		e.alert("g20", "a", e.labels("t", "g20", "warning"), `,"status":"firing","starts_at":"now"`)
		e.notify("g20", "new alerts added")
		e.settle()
		if s, _ := e.ownerOf(g20); s != "acknowledged" {
			t.Errorf("reopened into %s", s)
		}
		e.wantDue(g20, notice{"reminder", 480, false})
		e.adv(28800)
		if got := noticesOf(e.entries(g20, "&kind=timers"), "reminder"); !slices.Equal(got, []int{1, 2}) {
			t.Errorf("reminders %v", got)
		}
		e.cmd(e.api, g20, "resolve", `{}`)
	})

	t.Run("owner released", func(t *testing.T) {
		e := e.on(t)
		// C-17.FR-8, C-03.AC-27: disabling or deleting the Owner releases the Alert Group and ends its Reminders.
		carolID, carol := e.responder("carol")
		daveID, dave := e.responder("dave")
		g10 := e.fire("g10", "Disabled", "t", true)
		g11 := e.fire("g11", "Deleted", "t", true)
		e.ack(carol, g10)
		e.ack(dave, g11)
		e.adv(14400)
		e.api.json(http.MethodPost, "/api/v1/users/"+carolID+"/disable", "", http.StatusOK)
		if a := e.api.do(http.MethodDelete, "/api/v1/users/"+daveID, ""); a.status != http.StatusNoContent {
			t.Fatalf("delete dave = %d %s", a.status, a.body)
		}
		e.settle()
		for g, text := range map[string]string{
			g10: "The owner was disabled — this Alert Group has no owner now.",
			g11: "The owner was deleted — this Alert Group has no owner now.",
		} {
			if s, owner := e.ownerOf(g); s != "firing" || owner != nil {
				t.Errorf("%s %s %v", g, s, owner)
			}
			e.wantDue(g, notice{"ack_timeout", 15, false})
			found := false
			for _, p := range e.threadOf(g) {
				found = found || strings.Contains(p.Message, text)
			}
			if !found {
				t.Errorf("no %q in the thread of %s", text, g)
			}
			st := e.entries(g, "&kind=status")
			if x := st[len(st)-1]; x.Event != "unacknowledged" || x.Loudness != "loud" {
				t.Errorf("%s %+v", g, x)
			}
		}
		e.api.json(http.MethodPost, "/api/v1/users/"+carolID+"/enable", "", http.StatusOK)
		e.settle()
		if s, owner := e.ownerOf(g10); s != "firing" || owner != nil {
			t.Errorf("g10 after the enable %s %v", s, owner)
		}
		e.wantDue(g10, notice{"ack_timeout", 15, false})
		e.adv(900)
		for _, g := range []string{g10, g11} {
			tl := e.entries(g, "&kind=timers")
			if got := noticesOf(tl, "ack_timeout"); !slices.Equal(got, []int{1}) {
				t.Errorf("%s notices %v", g, got)
			}
			if got := noticesOf(tl, "reminder"); !slices.Equal(got, []int{1}) {
				t.Errorf("%s reminders %v", g, got)
			}
		}
		e.adv(1800, 3600)
		e.wantDue(g10, notice{"", -1, true})
		e.adv(86400)
		if got := noticesOf(e.entries(g10, "&kind=timers"), "reminder"); !slices.Equal(got, []int{1}) {
			t.Errorf("g10 reminders after the enable %v", got)
		}
		for _, g := range []string{g10, g11} {
			e.cmd(e.api, g, "resolve", `{}`)
		}
	})

	t.Run("events-mode webhook", func(t *testing.T) {
		e := e.on(t)
		// C-17.FR-1, FR-5, C-15.AC-11: the delivered created event is the first Publication, a delivered reminder
		// event reached its Destination, and every timer row sends one request.
		hook := e.webhook("timer-events", "timers", `[]`)
		e.route("wh", "wh", []string{hook["id"].(string)}, func(p map[string]any) { p["auto_unacknowledge"] = true })
		g13 := e.fire("g13", "Webhook", "wh", true)
		if n := e.h.count(t, `SELECT count(*) FROM alert_groups g JOIN webhook_events w ON w.alert_group_id = g.id
			AND w.event = 'created' AND w.state = 'delivered' WHERE g.public_id = '`+g13+`'
			AND g.first_published_at = w.delivered_at`); n != 1 {
			t.Errorf("first_published_at is not the delivered_at of the created event")
		}
		e.wantDue(g13, notice{"ack_timeout", 15, false})
		e.adv(900, 1800, 3600)
		e.ack(e.bob, g13)
		e.adv(14400, 28800, 57600)
		if s, owner := e.ownerOf(g13); s != "firing" || owner != nil {
			t.Errorf("not auto-unacknowledged after two reminders delivered as events: %s %v", s, owner)
		}
		e.wantDue(g13, notice{"ack_timeout", 15, false})
		e.adv(7200)
		e.ack(e.bob, g13)
		e.adv(14400)
		e.bob.json(http.MethodPost, "/api/v1/alert-groups/"+g13+"/still-on-it", "", http.StatusOK)
		e.settle()

		entries := e.entries(g13, "")
		var lifecycle []timerEntry
		for _, x := range entries {
			if x.Kind != "delivery" && x.Event != "" {
				lifecycle = append(lifecycle, x)
			}
		}
		got := e.received("timers")
		var sent []string
		for _, x := range got {
			sent = append(sent, x.Body.Event)
		}
		if !slices.Equal(sent, eventsOf(lifecycle)) {
			t.Fatalf("sent %v\nfor the timeline %v", sent, eventsOf(lifecycle))
		}
		for i, x := range got {
			if x.Body.Notify != (lifecycle[i].Loudness == "loud") || x.Status != http.StatusOK || x.SignaturesValid != 1 {
				t.Errorf("request %d (%s): notify %v for %s, status %d", i, x.Body.Event, x.Body.Notify,
					lifecycle[i].Loudness, x.Status)
			}
		}
		for _, row := range []string{"ack_timeout", "unclaimed", "reminder", "auto_unacknowledged", "notices_missed",
			"reminder_answered"} {
			if !slices.Contains(sent, row) {
				t.Errorf("no %s request: %v", row, sent)
			}
		}

		// C-17.AC-9: every timer entry has the kind, loudness and Mentions of its row.
		timerEvents := []groups.Event{groups.EventAckTimeout, groups.EventUnclaimed, groups.EventReminder,
			groups.EventReminderAnswered, groups.EventAutoUnacknowledged, groups.EventNoticesMissed}
		timerRows := map[string]groups.Row{}
		for _, row := range groups.Table {
			if slices.Contains(timerEvents, row.Event) {
				timerRows[string(row.Event)] = row
			}
		}
		checked := map[string]int{}
		for _, x := range lifecycle {
			row, ok := timerRows[x.Event]
			if !ok {
				continue
			}
			checked[x.Event]++
			mentions := []string{}
			for _, m := range row.Mentions {
				mentions = append(mentions, string(m))
			}
			if row.Event == groups.EventNoticesMissed {
				// notices_missed carries the Mentions of the notices it stands for.
				mentions = []string{"ack_timeout"}
			}
			if x.Kind != string(row.Kind) || x.Loudness != string(row.Loudness) || !slices.Equal(x.Mentions, mentions) {
				t.Errorf("%s: kind %s, loudness %s, mentions %v; the row says %s, %s, %v", x.Event, x.Kind, x.Loudness,
					x.Mentions, row.Kind, row.Loudness, mentions)
			}
		}
		if len(checked) != len(timerRows) {
			t.Errorf("timer rows in the timeline %v, want all of %d", checked, len(timerRows))
		}
		t.Logf("events: %v", sent)
	})
}
