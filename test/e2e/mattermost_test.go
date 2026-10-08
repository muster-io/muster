// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/mattermost"
)

// mm drives one Muster replica and the fake Alertmanager and Mattermost servers of a harness: an Admin's Personal
// access token, an Integration "lab" whose receiver lab the fake Alertmanager sends to, and readers of what the fake
// Mattermost recorded.
type mm struct {
	t           *testing.T
	h           *Harness
	r           *Replica
	api         patClient
	integration string
	fam, fmm    string
}

func newMM(t *testing.T, h *Harness, r *Replica) *mm {
	t.Helper()
	admin := newAgent(t, r.App)
	if a := admin.signIn(devmode.AdminEmail, devmode.AdminPassword); a.status != http.StatusCreated {
		t.Fatalf("sign in = %d %s", a.status, a.body)
	}
	perms, _ := json.Marshal(admin.json(http.MethodGet, "/api/v1/me", "", http.StatusOK)["permissions"])
	token := admin.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"mattermost","permissions":`+string(perms)+`}`, http.StatusCreated)["value"].(string)
	e := &mm{t: t, h: h, r: r, api: patClient{t: t, base: r.App, token: token},
		fam: h.Fakes.Alertmanager + "/_fake", fmm: h.Fakes.Mattermost + "/_fake"}
	e.integration = e.api.json(http.MethodPost, "/api/v1/integrations", `{"name":"lab","connection_mode":"webhook_only",
		"static_labels":{},"duplicate_window_seconds":45,"heartbeat":{"enabled":false}}`, http.StatusCreated)["id"].(string)
	value := e.api.json(http.MethodPost, "/api/v1/integrations/"+e.integration+"/tokens", `{"name":"lab"}`,
		http.StatusCreated)["value"].(string)
	if a := call(t, http.MethodPost, e.fam+"/receivers", `{"name":"lab","url":"`+r.Ingest+`/api/v1/ingest","token":"`+
		value+`"}`); a.status != http.StatusNoContent {
		t.Fatalf("register the receiver = %d %s", a.status, a.body)
	}
	return e
}

// on is e reporting to t, for a subtest.
func (e *mm) on(t *testing.T) *mm {
	c := *e
	c.t, c.api.t = t, t
	return &c
}

// connection creates the Connection "mm" to the fake Mattermost server.
func (e *mm) connection(limit int) string {
	e.t.Helper()
	return e.api.json(http.MethodPost, "/api/v1/connections", fmt.Sprintf(`{"type":"mattermost","name":"mm",
		"server_url":%q,"bot_token":"mm-dev-token","proxy":{"enabled":false},"limiter":{"limit":%d,"per_seconds":1}}`,
		e.h.Fakes.Mattermost, limit), http.StatusCreated)["id"].(string)
}

// mentionSettings are the Mention settings of a Destination: everyone for the events named in set, none otherwise.
func mentionSettings(set map[string]string) string {
	m := map[string]any{}
	for _, event := range []string{"new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended",
		"rise_to_urgent"} {
		m[event] = map[string]any{"everyone": cmp.Or(set[event], "none"), "user_ids": []string{}, "groups": []string{}}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (e *mm) destination(name, connection, channel string, mentions map[string]string, limit int) string {
	e.t.Helper()
	created := e.api.json(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"mattermost","name":%q,
		"connection_id":%q,"team_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":%d,"per_seconds":1}}`,
		name, connection, fakemattermost.TeamID, channel, mentionSettings(mentions), limit), http.StatusCreated)
	return created["destination"].(map[string]any)["id"].(string)
}

// route creates a Route for the label team=team with the Group key [alertname, cluster] and the On-call profile,
// changed by edit.
func (e *mm) route(name, team string, destinations []string, edit func(policy map[string]any)) string {
	e.t.Helper()
	var profiles struct {
		Items []struct {
			Policy map[string]any `json:"policy"`
		} `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/route-profiles", ""), &profiles)
	policy := profiles.Items[0].Policy
	if edit != nil {
		edit(policy)
	}
	body, _ := json.Marshal(map[string]any{"name": name, "urgent": false, "group_key": []string{"alertname", "cluster"},
		"matchers": []map[string]string{{"label": "team", "op": "=", "value": team}}, "destination_ids": destinations,
		"policy": policy})
	return e.api.json(http.MethodPost, "/api/v1/routes", string(body), http.StatusCreated)["id"].(string)
}

func (e *mm) fake(method, url, body string) {
	e.t.Helper()
	if a := call(e.t, method, url, body); a.status/100 != 2 {
		e.t.Fatalf("%s %s = %d %s", method, url, a.status, a.body)
	}
}

func (e *mm) group(name, alertname string) {
	e.t.Helper()
	e.fake(http.MethodPut, e.fam+"/groups/"+name, `{"receiver":"lab","route":"{}","labels":{"alertname":"`+alertname+`"}}`)
}

// alert sets the Alert name of the fake Alertmanager's group to labels, a JSON object, with extra fields of the
// AlertSpec such as `,"status":"resolved"`.
func (e *mm) alert(group, name, labels, extra string) {
	e.t.Helper()
	e.fake(http.MethodPut, e.fam+"/groups/"+group+"/alerts/"+name, `{"labels":`+labels+extra+`}`)
}

// notify sends the group's Snapshot and waits until Muster has processed it.
func (e *mm) notify(group, reason string) {
	e.t.Helper()
	processed := func() int64 {
		return e.h.count(e.t, `SELECT count(*) FROM stored_snapshots WHERE state <> 'pending'`)
	}
	before := processed()
	if a := call(e.t, http.MethodPost, e.fam+"/groups/"+group+"/notify", `{"reason":"`+reason+`"}`); a.status !=
		http.StatusOK {
		e.t.Fatalf("notify %s = %d %s", group, a.status, a.body)
	}
	eventually(e.t, "the snapshot of "+group+" to be processed", func() bool { return processed() > before })
}

// groupOf is the Alert Group of the Alert whose cluster label is cluster.
func (e *mm) groupOf(cluster string) string {
	e.t.Helper()
	var page struct {
		Items []struct {
			Group struct {
				ID string `json:"id"`
			} `json:"alert_group"`
		} `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/integrations/"+e.integration+"/alerts?"+
		url.Values{"label": {`cluster="` + cluster + `"`}}.Encode(), ""), &page)
	if len(page.Items) == 0 {
		e.t.Fatalf("no alert of cluster %s", cluster)
	}
	return page.Items[0].Group.ID
}

func (e *mm) alertGroup(id string) map[string]any {
	e.t.Helper()
	return e.api.json(http.MethodGet, "/api/v1/alert-groups/"+id, "", http.StatusOK)
}

func (e *mm) number(id string) int {
	e.t.Helper()
	return int(e.alertGroup(id)["number"].(float64))
}

func (e *mm) command(id, command string) string {
	e.t.Helper()
	body := ""
	if command == "resolve" {
		body = `{}`
	}
	return e.api.json(http.MethodPost, "/api/v1/alert-groups/"+id+"/"+command, body, http.StatusOK)["outcome"].(string)
}

type timelineEntry struct {
	Kind          string `json:"kind"`
	Event         string `json:"event"`
	DeliveryEvent string `json:"delivery_event"`
	SystemEvent   string `json:"system_event"`
	Actor         struct {
		Transport string `json:"transport"`
		Name      string `json:"name"`
	} `json:"actor"`
}

func (e *mm) timeline(id, query string) []timelineEntry {
	e.t.Helper()
	var page struct {
		Items []timelineEntry `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+id+"/timeline?limit=200"+query, ""), &page)
	return page.Items
}

func deliveryEvents(entries []timelineEntry) []string {
	var out []string
	for _, x := range entries {
		if x.Kind == "delivery" {
			out = append(out, x.DeliveryEvent)
		}
	}
	return out
}

type health struct {
	State  string  `json:"state"`
	Reason *string `json:"reason"`
}

func (e *mm) health(destination string) health {
	e.t.Helper()
	var d struct {
		Health health `json:"health"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/destinations/"+destination, ""), &d)
	return d.Health
}

func (e *mm) waitHealth(destination, state string) health {
	e.t.Helper()
	var got health
	eventually(e.t, "the destination to be "+state, func() bool {
		got = e.health(destination)
		return got.State == state
	})
	return got
}

// internalAlerts are the alertnames of the built-in Integration's firing Alerts.
func (e *mm) internalAlerts() []string {
	e.t.Helper()
	var builtin string
	for _, item := range e.api.json(http.MethodGet, "/api/v1/integrations", "", http.StatusOK)["items"].([]any) {
		if in := item.(map[string]any); in["builtin"] == true {
			builtin = in["id"].(string)
		}
	}
	var page struct {
		Items []struct {
			Labels map[string]string `json:"labels"`
		} `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/integrations/"+builtin+"/alerts?state=firing", ""), &page)
	var names []string
	for _, a := range page.Items {
		names = append(names, a.Labels["alertname"])
	}
	return names
}

// clientRequests is muster_client_requests_total of the client class and outcome, summed over the replicas.
func (e *mm) clientRequests(class, outcome string) float64 {
	e.t.Helper()
	var sum float64
	for _, r := range e.h.Replicas {
		v, _ := strconv.ParseFloat(r.Metric(e.t, `muster_client_requests_total{client="`+class+`",outcome="`+outcome+`"}`),
			64)
		sum += v
	}
	return sum
}

type mmAction struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Integration struct {
		URL     string            `json:"url"`
		Context map[string]string `json:"context"`
	} `json:"integration"`
}

type mmAttachment struct {
	Color      string     `json:"color"`
	Title      string     `json:"title"`
	TitleLink  string     `json:"title_link"`
	Text       string     `json:"text"`
	Footer     string     `json:"footer"`
	FooterIcon string     `json:"footer_icon"`
	Actions    []mmAction `json:"actions"`
}

type mmPost struct {
	fakemattermost.RecordedPost
	Attachments []mmAttachment
}

func (p mmPost) attachment(t *testing.T) mmAttachment {
	t.Helper()
	if len(p.Attachments) != 1 {
		t.Fatalf("post %s has %d attachments, want 1: %s", p.ID, len(p.Attachments), p.Props)
	}
	return p.Attachments[0]
}

func parseProps(raw json.RawMessage) []mmAttachment {
	var props struct {
		Attachments []mmAttachment `json:"attachments"`
	}
	_ = json.Unmarshal(raw, &props)
	return props.Attachments
}

func (e *mm) posts() []mmPost {
	e.t.Helper()
	var recorded []fakemattermost.RecordedPost
	decode(e.t, call(e.t, http.MethodGet, e.fmm+"/posts", ""), &recorded)
	out := make([]mmPost, len(recorded))
	for i, p := range recorded {
		out[i] = mmPost{RecordedPost: p, Attachments: parseProps(p.Props)}
	}
	return out
}

func (e *mm) post(id string) mmPost {
	e.t.Helper()
	for _, p := range e.posts() {
		if p.ID == id {
			return p
		}
	}
	e.t.Fatalf("no post %s", id)
	return mmPost{}
}

// roots are the Root messages of Alert Group #n, deleted ones included, in the order they were posted.
func (e *mm) roots(n int) []mmPost {
	e.t.Helper()
	var out []mmPost
	for _, p := range e.posts() {
		if p.RootID == "" && strings.Contains(p.Message+" ", fmt.Sprintf(" #%d ", n)) {
			out = append(out, p)
		}
	}
	return out
}

// root waits for the live Root message of Alert Group #n and returns it.
func (e *mm) root(n int) mmPost {
	e.t.Helper()
	var live mmPost
	eventually(e.t, fmt.Sprintf("the root message of #%d", n), func() bool {
		for _, p := range e.roots(n) {
			if p.DeleteAt == 0 {
				live = p
			}
		}
		return live.ID != ""
	})
	return live
}

func (e *mm) replies(rootID string) []mmPost {
	e.t.Helper()
	var out []mmPost
	for _, p := range e.posts() {
		if p.RootID == rootID {
			out = append(out, p)
		}
	}
	return out
}

func (e *mm) requests() []fakeserver.Request {
	e.t.Helper()
	var reqs []fakeserver.Request
	decode(e.t, call(e.t, http.MethodGet, e.fmm+"/requests", ""), &reqs)
	return reqs
}

// calls are the recorded requests by method and path.
func (e *mm) calls(method, path string) []fakeserver.Request {
	e.t.Helper()
	var out []fakeserver.Request
	for _, r := range e.requests() {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (e *mm) notifications() []fakemattermost.Notification {
	e.t.Helper()
	var out []fakemattermost.Notification
	decode(e.t, call(e.t, http.MethodGet, e.fmm+"/notifications", ""), &out)
	return out
}

func (e *mm) ephemeral() []fakemattermost.Ephemeral {
	e.t.Helper()
	var out []fakemattermost.Ephemeral
	decode(e.t, call(e.t, http.MethodGet, e.fmm+"/ephemeral", ""), &out)
	return out
}

func (e *mm) fault(f fakeserver.Fault) {
	e.t.Helper()
	b, _ := json.Marshal(f)
	if a := call(e.t, http.MethodPost, e.fmm+"/faults", string(b)); a.status != http.StatusNoContent {
		e.t.Fatalf("fault %s = %d %s", b, a.status, a.body)
	}
}

func (e *mm) press(postID, actionID, userID string) fakemattermost.PressResult {
	e.t.Helper()
	var res fakemattermost.PressResult
	a := call(e.t, http.MethodPost, e.fmm+"/press", fmt.Sprintf(`{"post_id":%q,"action_id":%q,"user_id":%q}`, postID,
		actionID, userID))
	if a.status != http.StatusOK {
		e.t.Fatalf("press %s on %s = %d %s", actionID, postID, a.status, a.body)
	}
	decode(e.t, a, &res)
	return res
}

// callback sends a button press to the callback handler directly, as Mattermost does, and checks the answer that
// every callback gets: 200 with want, the JSON answer.
func (e *mm) callback(method, connection, body, want string) {
	e.t.Helper()
	a := call(e.t, method, e.r.Ingest+"/api/v1/callbacks/mattermost/"+connection, body)
	if a.status != http.StatusOK || strings.TrimSpace(string(a.body)) != want ||
		!strings.HasPrefix(a.header.Get("Content-Type"), "application/json") {
		e.t.Errorf("%s callback %s = %d %q %s, want %s", method, body, a.status, a.header.Get("Content-Type"), a.body,
			want)
	}
}

// pressAnswer is the JSON answer to a press that shows text to the person who pressed.
func pressAnswer(text string) string {
	b, _ := json.Marshal(map[string]any{"ephemeral_text": text, "skip_slack_parsing": true})
	return string(b)
}

func actionNames(a mmAttachment) []string {
	names := make([]string, len(a.Actions))
	for i, x := range a.Actions {
		names[i] = x.Name
	}
	return names
}

func lastLine(s string) string {
	return s[strings.LastIndex(s, "\n")+1:]
}

func requestBody(t *testing.T, r fakeserver.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
		t.Fatalf("body of %s %s: %v", r.Method, r.Path, err)
	}
	return m
}

func rootIDOf(t *testing.T, r fakeserver.Request) string {
	t.Helper()
	id, _ := requestBody(t, r)["root_id"].(string)
	return id
}

var deletedNote = regexp.MustCompile(`The previous message was deleted at [0-9]{2}:[0-9]{2}`)

// TestMattermost is the Verification of S-061 against `muster dev` and the fake Mattermost server, with
// E2E_REPLICAS=2 on two replicas: the Root message layout (C-13.AC-13) with a label value that stays literal text
// (C-13.AC-5), the edit on Acknowledge and the Quiet and Loud Thread replies (C-13.AC-1, AC-6), presses from an
// unlinked and a linked account (C-13.AC-4, AC-14, FR-4), callbacks that cannot be verified (C-13.AC-2, AC-11), a 429
// that holds the Connection (C-13.AC-15), the bot removed from the channel and the recovery through the probe and
// through checkDestination (C-13.AC-3, AC-8), a deleted Root message republished with the note (C-13.AC-12), the
// Delivery problem filter (C-13.AC-10), the Internal alerts suggestion (C-13.AC-9) and `muster doctor` (C-02.FR-14).
func TestMattermost(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	conn := e.connection(5)
	dest := e.destination("alerts", conn, fakemattermost.ChannelAlerts, map[string]string{"new_alerts": "channel"}, 5)
	route := e.route("db", "db", []string{dest}, nil)
	bot := fakemattermost.BotUserID
	callbackURL := devmode.IngestURL + "/api/v1/callbacks/mattermost/" + conn

	// C-13.AC-13, AC-5: a new Alert Group with @channel <b>x</b> in a label.
	e.group("d1", "DiskFull")
	e.alert("d1", "i1", `{"team":"db","cluster":"a","pod":"i1","note":"@channel <b>x</b>"}`, "")
	e.notify("d1", "first notification")
	g := e.groupOf("a")
	n := e.number(g)
	root := e.root(n)
	att := root.attachment(t)
	if root.Message != fmt.Sprintf("🔴 #%d DiskFull", n) || att.Color != "#d32f2f" ||
		att.Title != fmt.Sprintf("#%d DiskFull", n) || !strings.HasSuffix(att.TitleLink, "/alert-groups/"+g) ||
		!strings.HasPrefix(att.Footer, "Muster v") || att.FooterIcon != devmode.PublicURL+"/muster-mark-256.png" {
		t.Errorf("root message %q, attachment %+v", root.Message, att)
	}
	if !strings.Contains(att.Text, "[Open in Muster]("+devmode.PublicURL+"/alert-groups/"+g+")") ||
		!strings.Contains(att.Text, "note: @\u200bchannel \\<b\\>x\\</b\\>") {
		t.Errorf("attachment text %q", att.Text)
	}
	var ids []string
	for _, a := range att.Actions {
		ids = append(ids, a.ID)
		if a.Type != "button" || a.Integration.URL != callbackURL || a.Integration.Context["action"] == "" ||
			a.Integration.Context["key_id"] == "" {
			t.Errorf("action %+v", a)
		}
	}
	if !slices.Equal(ids, []string{"ack", "resolve", "snooze0", "snooze1", "snooze2"}) {
		t.Errorf("action ids %v", ids)
	}
	if got := e.notifications(); len(got) != 0 {
		t.Errorf("notifications after the root message: %+v", got)
	}

	// C-13.AC-1, AC-6: Acknowledge edits the post; a new Alert is a Quiet Thread reply while acknowledged, a Loud one
	// with @channel once firing again.
	if out := e.command(g, "acknowledge"); out != "done" {
		t.Fatalf("acknowledge = %s", out)
	}
	patch := "/api/v4/posts/" + root.ID + "/patch"
	eventually(t, "the edit", func() bool { return len(e.calls(http.MethodPut, patch)) == 1 })
	edited := e.post(root.ID)
	ea := edited.attachment(t)
	if ea.Color != "#f57c00" || !slices.Equal(actionNames(ea), []string{"Unack", "Resolve", "Snooze 1 h", "Snooze 4 h",
		"Snooze 24 h"}) || !strings.HasPrefix(lastLine(ea.Text), "Acknowledged by ") || strings.Contains(edited.Message, "@") {
		t.Errorf("edited post %q: %+v", edited.Message, ea)
	}
	e.alert("d1", "i2", `{"team":"db","cluster":"a","disk":"i2"}`, "")
	e.notify("d1", "new alerts added")
	eventually(t, "the quiet thread reply", func() bool { return len(e.replies(root.ID)) == 1 })
	if quiet := e.replies(root.ID)[0]; strings.Contains(quiet.Message, "@") || e.post(root.ID).ReplyCount != 1 {
		t.Errorf("quiet reply %q, reply count %d", quiet.Message, e.post(root.ID).ReplyCount)
	}
	e.command(g, "unacknowledge")
	e.alert("d1", "i3", `{"team":"db","cluster":"a","disk":"i3"}`, "")
	e.notify("d1", "new alerts added")
	advance(t, r, 60)
	eventually(t, "the loud thread reply", func() bool { return len(e.replies(root.ID)) == 2 })
	if loud := e.replies(root.ID)[1]; !strings.HasPrefix(loud.Message, "@channel") {
		t.Errorf("loud reply %q", loud.Message)
	}
	eventually(t, "the channel notifications", func() bool { return len(e.notifications()) > 0 })
	for _, x := range e.notifications() {
		if x.Kind != fakemattermost.KindChannel || x.RootID != root.ID {
			t.Errorf("notification %+v", x)
		}
	}
	notified := len(e.notifications())

	// C-13.AC-4, AC-14 (D284): a press from an account without an Account link. The bot, which has the role Member,
	// first tries an ephemeral post, which the server refuses with 403 (F-063), then answers with ephemeral_text, which
	// the server shows to that person alone, from System, in the Thread of the Root message (F-025, F-062).
	notLinked := "Your Mattermost account is not linked to Muster. Link it in your profile: " + devmode.PublicURL +
		"/profile"
	if res := e.press(root.ID, "ack", fakemattermost.AliceUserID); res.Status != http.StatusOK ||
		string(res.Answer) != pressAnswer(notLinked) || res.PersonStatus != http.StatusOK {
		t.Errorf("unlinked press = %+v %s", res, res.Answer)
	}
	eph := e.ephemeral()
	if len(eph) != 1 || eph[0].UserID != fakemattermost.AliceUserID || eph[0].RootID != root.ID ||
		eph[0].From != "System" || eph[0].ChannelID != fakemattermost.ChannelAlerts || eph[0].Message != notLinked {
		t.Errorf("ephemeral posts %+v", eph)
	}
	if calls := e.calls(http.MethodPost, "/api/v4/posts/ephemeral"); len(calls) != 1 ||
		calls[0].Status != http.StatusForbidden {
		t.Errorf("ephemeral post calls %+v", calls)
	}
	// With the system admin role, the ephemeral post is made, shows in the channel view, and the answer is empty.
	e.fake(http.MethodPut, e.fmm+"/config", `{"bot_system_admin":true}`)
	if res := e.press(root.ID, "ack", fakemattermost.AliceUserID); string(res.Answer) != "{}" {
		t.Errorf("unlinked press with an admin bot = %+v %s", res, res.Answer)
	}
	if eph := e.ephemeral(); len(eph) != 2 || eph[1].UserID != fakemattermost.AliceUserID || eph[1].RootID != "" ||
		eph[1].From != fakemattermost.BotUsername || eph[1].ShownIn != fakemattermost.ShownInChannel ||
		eph[1].Message != notLinked {
		t.Errorf("ephemeral posts with an admin bot %+v", eph)
	}
	e.fake(http.MethodPut, e.fmm+"/config", `{"bot_system_admin":false}`)
	if s := e.alertGroup(g)["status"]; s != "firing" {
		t.Errorf("status after the unlinked presses = %v", s)
	}

	// C-13.FR-4, C-10.FR-3, FR-6: a press from Bob, a linked Responder, as Bob with the Transport mattermost; the
	// delivery worker edits the post.
	e.api.json(http.MethodPost, "/api/v1/users", `{"name":"bob","login":"bob","role":"responder"}`, http.StatusCreated)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		created_at) SELECT 1, 'AK0000000000B1', u.id, 'mattermost', c.id, 'u-bob', 'bob', now() FROM users u,
		connections c WHERE u.login = 'bob' AND c.name = 'mm'`)
	patches := len(e.calls(http.MethodPut, patch))
	if res := e.press(root.ID, "ack", fakemattermost.BobUserID); string(res.Answer) != "{}" {
		t.Errorf("linked press answer %s", res.Answer)
	}
	eventually(t, "the acknowledgement by bob", func() bool {
		items := e.timeline(g, "")
		return len(items) > 0 && items[0].Event == "acknowledged"
	})
	if first := e.timeline(g, "")[0]; first.Actor.Transport != "mattermost" || first.Actor.Name != "bob" {
		t.Errorf("acknowledged by %+v", first.Actor)
	}
	eventually(t, "the edit after the press", func() bool { return len(e.calls(http.MethodPut, patch)) > patches })
	if len(e.ephemeral()) != 2 {
		t.Errorf("a successful press got an ephemeral answer: %+v", e.ephemeral())
	}
	before := time.Now()
	e.press(root.ID, "snooze1", fakemattermost.BobUserID)
	eventually(t, "the snooze by bob", func() bool { return e.alertGroup(g)["status"] == "snoozed" })
	until, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(e.alertGroup(g)["snooze_until"]))
	offset := time.Duration(readClock(t, r).OffsetSeconds) * time.Second
	if want := before.Add(offset + 4*time.Hour); until.Before(want.Add(-time.Minute)) || until.After(want.Add(time.Minute)) {
		t.Errorf("snoozed until %v, want about %v", until, want)
	}
	eventually(t, "the snoozed post", func() bool {
		return slices.Contains(actionNames(e.post(root.ID).attachment(t)), "Unsnooze")
	})
	e.press(root.ID, "unsnooze", fakemattermost.BobUserID)
	eventually(t, "the unsnooze by bob", func() bool { return e.alertGroup(g)["status"] != "snoozed" })
	if s := e.alertGroup(g)["status"]; s != "acknowledged" {
		e.command(g, "acknowledge")
	}
	var presses []fakemattermost.Press
	decode(t, call(t, http.MethodGet, e.fmm+"/presses", ""), &presses)
	for i, p := range presses {
		if want := map[bool]string{true: pressAnswer(notLinked), false: "{}"}[i == 0]; string(p.Answer) != want {
			t.Errorf("press %d answer %s, want %s", i, p.Answer, want)
		}
	}

	// C-13.AC-2: a changed action id, an unknown Connection, a body that is not JSON, a GET.
	ctx := e.post(root.ID).attachment(t).Actions[0].Integration.Context
	action := ctx["action"]
	tampered := action[:len(action)-2] + "AA"
	if tampered == action {
		tampered = action[:len(action)-2] + "BB"
	}
	press := func(postID string, context map[string]string) string {
		b, _ := json.Marshal(map[string]any{"user_id": fakemattermost.BobUserID, "channel_id": fakemattermost.ChannelAlerts,
			"post_id": postID, "context": context})
		return string(b)
	}
	status := e.alertGroup(g)["status"]
	notVerified := "This button could not be verified; nothing was changed."
	e.callback(http.MethodPost, conn, press(root.ID, map[string]string{"action": tampered, "key_id": ctx["key_id"]}),
		pressAnswer(notVerified))
	e.callback(http.MethodPost, "CN000000000000", `{"user_id":"u-bob","channel_id":"x","post_id":"y",
		"context":{"action":"z","key_id":"k"}}`, "{}")
	e.callback(http.MethodPost, conn, "not json", "{}")
	e.callback(http.MethodPost, conn, `{"user_id":"u-bob"}`, "{}")
	e.callback(http.MethodGet, conn, "", "{}")

	// C-13.AC-11: the signed action id of the first Alert Group with the post_id of a second one.
	e.alert("d1", "c2", `{"team":"db","cluster":"c2"}`, "")
	e.notify("d1", "new alerts added")
	g2 := e.groupOf("c2")
	root2 := e.root(e.number(g2))
	entries, entries2 := len(e.timeline(g, "")), len(e.timeline(g2, ""))
	e.callback(http.MethodPost, conn, press(root2.ID, ctx), pressAnswer(notVerified))
	var statuses []int
	for _, c := range e.calls(http.MethodPost, "/api/v4/posts/ephemeral") {
		statuses = append(statuses, c.Status)
	}
	if !slices.Equal(statuses, []int{http.StatusForbidden, http.StatusCreated, http.StatusForbidden,
		http.StatusForbidden}) {
		t.Errorf("ephemeral post calls %v", statuses)
	}
	if e.alertGroup(g)["status"] != status || e.alertGroup(g2)["status"] != "firing" ||
		len(e.timeline(g, "")) != entries || len(e.timeline(g2, "")) != entries2 {
		t.Errorf("a refused press changed %v / %v", e.alertGroup(g)["status"], e.alertGroup(g2)["status"])
	}

	// C-13.AC-15: a 429 with a plain-text body and Retry-After: 1 holds the Connection.
	limited := e.clientRequests("delivery", "retry_after")
	e.fault(fakeserver.Fault{Path: "/api/v4/posts", Status: http.StatusTooManyRequests, RetryAfterSeconds: 1,
		ContentType: "text/plain", Body: "limit exceeded", Times: 1})
	sent := len(e.calls(http.MethodPost, "/api/v4/posts"))
	e.alert("d1", "i4", `{"team":"db","cluster":"a","disk":"i4"}`, "")
	e.notify("d1", "new alerts added")
	advance(t, r, 60)
	eventually(t, "the post after the 429", func() bool { return len(e.calls(http.MethodPost, "/api/v4/posts")) >= sent+2 })
	posts := e.calls(http.MethodPost, "/api/v4/posts")[sent:]
	if gap := posts[1].AtMs - posts[0].AtMs; posts[0].Status != http.StatusTooManyRequests || posts[1].Status !=
		http.StatusCreated || gap < 1000 || gap >= 2000 {
		t.Errorf("after the 429: statuses %d, %d, %d ms apart", posts[0].Status, posts[1].Status, gap)
	}
	if got := e.clientRequests("delivery", "retry_after"); got != limited+1 {
		t.Errorf("retry_after outcomes %v, want %v", got, limited+1)
	}

	// C-13.AC-3, AC-8: the bot leaves the channel; a new Alert Group meets 403 and resolves while Broken; the probe
	// ends the Broken state once the bot is back.
	members := e.fmm + "/channels/" + fakemattermost.ChannelAlerts + "/members/" + bot
	e.fake(http.MethodDelete, members, "")
	e.alert("d1", "j1", `{"team":"db","cluster":"b","disk":"j1"}`, "")
	e.notify("d1", "new alerts added")
	if hl := e.waitHealth(dest, "broken"); hl.Reason == nil || !strings.Contains(*hl.Reason, "403") {
		t.Errorf("broken health %+v", hl)
	}
	rt := e.api.json(http.MethodGet, "/api/v1/routes/"+route, "", http.StatusOK)
	if s := rt["destinations"].([]any)[0].(map[string]any)["health"].(map[string]any)["state"]; s != "broken" {
		t.Errorf("the route shows %v", s)
	}
	eventually(t, "MusterDestinationBroken", func() bool {
		return slices.Contains(e.internalAlerts(), "MusterDestinationBroken")
	})
	gj := e.groupOf("b")
	e.alert("d1", "j1", `{"team":"db","cluster":"b","disk":"j1"}`, `,"status":"resolved"`)
	e.notify("d1", "some alerts resolved")
	e.fake(http.MethodPut, members, "")
	ok := e.clientRequests("delivery", "ok")
	advance(t, r, 300)
	e.waitHealth(dest, "healthy")
	if got := e.clientRequests("delivery", "ok"); got-ok < 2 {
		t.Errorf("the probe made %v requests in the delivery class", got-ok)
	}
	eventually(t, "MusterDestinationBroken to resolve", func() bool {
		return !slices.Contains(e.internalAlerts(), "MusterDestinationBroken")
	})
	if got := e.roots(e.number(gj)); len(got) != 0 {
		t.Errorf("the alert group resolved while broken was published: %+v", got)
	}

	// The same through checkDestination, at once, in the interactive class; the 403 of the edit is confirmed by the
	// plain read of the post answering 200 (C-13.AC-12).
	e.fake(http.MethodDelete, members, "")
	e.alert("d1", "i5", `{"team":"db","cluster":"a","disk":"i5"}`, "")
	e.notify("d1", "new alerts added")
	e.waitHealth(dest, "broken")
	if p := e.calls(http.MethodPut, patch); p[len(p)-1].Status != http.StatusForbidden {
		t.Errorf("the edit without the bot answered %d", p[len(p)-1].Status)
	}
	if reads := e.calls(http.MethodGet, "/api/v4/posts/"+root.ID); len(reads) == 0 ||
		reads[len(reads)-1].Status != http.StatusOK {
		t.Errorf("reads of the post %+v", reads)
	}
	e.fake(http.MethodPut, members, "")
	interactive := e.clientRequests("interactive", "ok")
	check := e.api.json(http.MethodPost, "/api/v1/destinations/"+dest+"/checks", "", http.StatusOK)
	if check["ok"] != true || check["health"].(map[string]any)["state"] != "healthy" {
		t.Errorf("check = %v", check)
	}
	if got := e.clientRequests("interactive", "ok"); got-interactive < 2 {
		t.Errorf("the check made %v requests in the interactive class", got-interactive)
	}

	// C-13.AC-12: a deleted Root message answers the edit with 403 and the plain read with 404; it is published again
	// once, Quietly, with the note, and the Destination stays healthy.
	eventually(t, "the edit to catch up", func() bool {
		return h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0
	})
	e.fake(http.MethodDelete, e.fmm+"/posts/"+root.ID, "")
	e.command(g, "unacknowledge")
	republished := e.root(n)
	if republished.ID == root.ID {
		eventually(t, "the republication", func() bool { return e.root(n).ID != root.ID })
		republished = e.root(n)
	}
	if p := e.calls(http.MethodPut, patch); p[len(p)-1].Status != http.StatusForbidden {
		t.Errorf("the edit of the deleted post answered %d", p[len(p)-1].Status)
	}
	if reads := e.calls(http.MethodGet, "/api/v4/posts/"+root.ID); reads[len(reads)-1].Status != http.StatusNotFound {
		t.Errorf("the read of the deleted post answered %d", reads[len(reads)-1].Status)
	}
	if !deletedNote.MatchString(republished.attachment(t).Text) || len(e.roots(n)) != 2 {
		t.Errorf("%d root messages, the new one %q", len(e.roots(n)), republished.attachment(t).Text)
	}
	if got := len(e.notifications()); got != notified {
		t.Errorf("the republication notified: %d notifications, want %d", got, notified)
	}
	if hl := e.health(dest); hl.State != "healthy" {
		t.Errorf("health after the republication %+v", hl)
	}

	// C-13.AC-10: Not delivered, filtered, then delivered again.
	e.fault(fakeserver.Fault{Path: "/api/v4/posts/*/patch", Status: http.StatusBadRequest, Times: 1})
	e.command(g, "acknowledge")
	problem := func() []any {
		return e.api.json(http.MethodGet, "/api/v1/alert-groups?delivery_problem=true", "", http.StatusOK)["items"].([]any)
	}
	eventually(t, "the delivery problem", func() bool { return len(problem()) == 1 })
	if item := problem()[0].(map[string]any); item["id"] != g || item["delivery_problem"] != true {
		t.Errorf("delivery_problem=true lists %v", item)
	}
	if ev := e.timeline(g, "&kind=delivery"); len(ev) == 0 || ev[0].DeliveryEvent != "not_delivered" {
		t.Errorf("delivery entries %+v", ev)
	}
	counts := e.api.json(http.MethodGet, "/api/v1/alert-group-counts?delivery_problem=true", "", http.StatusOK)
	if counts["acknowledged"] != 1.0 || counts["firing"] != 0.0 {
		t.Errorf("counts with delivery_problem=true %v", counts)
	}
	e.command(g, "unacknowledge")
	eventually(t, "the delivery problem to clear", func() bool { return len(problem()) == 0 })
	if e.alertGroup(g)["delivery_problem"] != false {
		t.Errorf("delivery_problem after the later delivery %v", e.alertGroup(g)["delivery_problem"])
	}

	// C-13.FR-11, AC-9: the Internal alerts Route suggestion.
	var suggestions []string
	for _, s := range e.api.json(http.MethodGet, "/api/v1/route-suggestions", "", http.StatusOK)["items"].([]any) {
		suggestions = append(suggestions, s.(map[string]any)["id"].(string))
	}
	if !slices.Contains(suggestions, "internal_alerts") {
		t.Fatalf("suggestions %v", suggestions)
	}
	if a := e.api.do(http.MethodPost, "/api/v1/route-suggestions/internal_alerts/accept", `{"destination_ids":[]}`); a.status !=
		http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"/destination_ids"`) {
		t.Errorf("accept without destinations = %d %s", a.status, a.body)
	}
	if a := e.api.do(http.MethodPost, "/api/v1/route-suggestions/internal_alerts/accept",
		`{"destination_ids":["`+dest+`"]}`); a.status/100 != 2 {
		t.Fatalf("accept = %d %s", a.status, a.body)
	}
	first := e.api.json(http.MethodGet, "/api/v1/routes", "", http.StatusOK)["items"].([]any)[0].(map[string]any)
	matcher := first["matchers"].([]any)[0].(map[string]any)
	if first["name"] != "Muster internal alerts" || matcher["value"] != "Muster.*" ||
		first["destinations"].([]any)[0].(map[string]any)["id"] != dest {
		t.Errorf("first route %v", first)
	}

	// C-02.FR-14: one line per Mattermost Connection and Destination; a bot that may not make ephemeral posts is a
	// WARN with the hint that press answers show in the Thread (D284).
	code, out := h.runCLIStdout(t, "doctor")
	hint := "WARN connection %s: " + mattermost.HintPressAnswersInThread
	for _, want := range []string{fmt.Sprintf(hint, devmode.ConnectionName), fmt.Sprintf(hint, "mm"),
		"OK   destination alerts: ok"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("doctor (exit %d) has no line %q:\n%s", code, want, out)
		}
	}
	e.fake(http.MethodDelete, members, "")
	code, out = h.runCLIStdout(t, "doctor")
	if code == 0 || !strings.Contains(out, "FAIL destination alerts: The bot is not a member of this channel.") {
		t.Errorf("doctor without the bot exited %d:\n%s", code, out)
	}
	e.fake(http.MethodPut, members, "")
}

// TestMattermostMessages is the rest of S-061's end-to-end checks against `muster dev` and the fake Mattermost server:
// ten Alerts and an Acknowledge within 2 seconds (C-11.AC-1), a Storm (C-11.AC-3), a failing Route template (C-12.AC-3),
// a Russian Route (C-12.AC-5), an Alert Group of 600 Alerts within the length limit (C-12.FR-11, F-057), a Thread reply
// under a deleted Root message (C-13.AC-12, F-058) and an edit refused with 403 while the post exists (C-13.AC-12).
func TestMattermostMessages(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	conn := e.connection(50)
	dest := e.destination("prod", conn, fakemattermost.ChannelAlertsProd, nil, 50)

	t.Run("ten alerts and an acknowledge", func(t *testing.T) {
		e := e.on(t)
		e.route("ack", "ack", []string{dest}, nil)
		e.group("a1", "TenAlerts")
		for i := range 10 {
			e.alert("a1", fmt.Sprint("x", i), fmt.Sprintf(`{"team":"ack","cluster":"ten","disk":"d%d"}`, i), "")
		}
		begin := time.Now()
		e.notify("a1", "first notification")
		g := e.groupOf("ten")
		e.command(g, "acknowledge")
		if took := time.Since(begin); took > 2*time.Second {
			t.Logf("the alerts and the acknowledge took %v", took)
		}
		n := e.number(g)
		root := e.root(n)
		eventually(t, "the delivery to settle", func() bool {
			return h.count(t, `SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
				WHERE g.public_id = '`+g+`' AND d.state = 'delivered'`) == 1
		})
		if roots, edits := e.roots(n), e.calls(http.MethodPut, "/api/v4/posts/"+root.ID+"/patch"); len(roots) != 1 ||
			len(edits) > 1 || len(e.replies(root.ID)) != 0 {
			t.Errorf("%d root messages, %d edits, %d replies", len(roots), len(edits), len(e.replies(root.ID)))
		}
		if c := e.post(root.ID).attachment(t).Color; c != "#f57c00" {
			t.Errorf("the root message ends %s", c)
		}
	})

	t.Run("storm", func(t *testing.T) {
		e := e.on(t)
		e.route("storm", "storm", []string{dest}, func(p map[string]any) { p["storm_threshold"] = 3 })
		e.group("s1", "Surge")
		for i := 1; i <= 6; i++ {
			e.alert("s1", fmt.Sprint("s", i), fmt.Sprintf(`{"team":"storm","cluster":"s%d"}`, i), "")
		}
		e.notify("s1", "first notification")
		numbers := map[int]int{}
		for i := 1; i <= 6; i++ {
			numbers[i] = e.number(e.groupOf(fmt.Sprint("s", i)))
		}
		summary := func() []mmPost {
			var out []mmPost
			for _, p := range e.posts() {
				if p.ChannelID == fakemattermost.ChannelAlertsProd && p.RootID == "" &&
					(strings.HasPrefix(p.Message, "⛈ Storm on") || strings.HasPrefix(p.Message, "Storm over")) {
					out = append(out, p)
				}
			}
			return out
		}
		eventually(t, "the storm summary", func() bool { return len(summary()) == 1 })
		published := 0
		for i := 1; i <= 6; i++ {
			if len(e.roots(numbers[i])) > 0 {
				published++
			}
		}
		if s := summary()[0].Message; published != 3 || !strings.HasPrefix(s, "⛈ Storm on storm: 3 Alert Groups, 0 urgent") {
			t.Errorf("%d root messages and the summary %q", published, s)
		}
		held := []int{}
		for i := 1; i <= 6; i++ {
			if len(e.roots(numbers[i])) == 0 {
				held = append(held, i)
			}
		}
		if len(held) != 3 {
			t.Fatalf("held %v", held)
		}
		e.alert("s1", fmt.Sprint("s", held[2]), fmt.Sprintf(`{"team":"storm","cluster":"s%d"}`, held[2]),
			`,"status":"resolved"`)
		e.notify("s1", "some alerts resolved")
		advance(t, r, 360)
		eventually(t, "the storm to end", func() bool {
			return strings.HasPrefix(summary()[0].Message, "Storm over: 2 Alert Groups still open")
		})
		e.root(numbers[held[0]])
		e.root(numbers[held[1]])
		if got := e.roots(numbers[held[2]]); len(got) != 0 {
			t.Errorf("the resolved alert group was published after the storm: %+v", got)
		}
		for _, x := range e.notifications() {
			if x.ChannelID == fakemattermost.ChannelAlertsProd {
				t.Errorf("notification %+v", x)
			}
		}
	})

	t.Run("fallback template", func(t *testing.T) {
		e := e.on(t)
		route := e.route("tpl", "tpl", []string{dest}, func(p map[string]any) {
			p["templates"].(map[string]any)["root_message"] = `second alert {{ (index .Alerts 1).Labels.disk }}`
		})
		e.group("t1", "Templated")
		e.alert("t1", "t1", `{"team":"tpl","cluster":"tpl"}`, "")
		e.notify("t1", "first notification")
		root := e.root(e.number(e.groupOf("tpl")))
		if text := root.attachment(t).Text; !strings.Contains(text, "The message template of this Route failed") ||
			!strings.Contains(text, "team: tpl") {
			t.Errorf("fallback post %q", text)
		}
		eventually(t, "MusterTemplateError", func() bool { return slices.Contains(e.internalAlerts(), "MusterTemplateError") })
		if te := e.api.json(http.MethodGet, "/api/v1/routes/"+route, "", http.StatusOK)["template_error"]; te == nil {
			t.Error("the route shows no template error")
		}
	})

	t.Run("russian", func(t *testing.T) {
		e := e.on(t)
		e.route("ru", "ru", []string{dest}, func(p map[string]any) { p["language"] = "ru" })
		e.group("r1", "Russian")
		e.alert("r1", "r1", `{"team":"ru","cluster":"ru"}`, "")
		e.notify("r1", "first notification")
		root := e.root(e.number(e.groupOf("ru")))
		att := root.attachment(t)
		if names := actionNames(att); !slices.Equal(names, []string{"Подтвердить", "Закрыть", "Отложить на 1 ч",
			"Отложить на 4 ч", "Отложить на 24 ч"}) || !strings.Contains(att.Text, "[Открыть в Muster](") {
			t.Errorf("russian root message %v %q", names, att.Text)
		}
		e.alert("r1", "r2", `{"team":"ru","cluster":"ru","disk":"r2"}`, "")
		e.notify("r1", "new alerts added")
		eventually(t, "the russian reply", func() bool { return len(e.replies(root.ID)) == 1 })
		if m := e.replies(root.ID)[0].Message; !strings.HasPrefix(m, "Новые алерты (1):") {
			t.Errorf("russian reply %q", m)
		}
	})

	t.Run("600 alerts", func(t *testing.T) {
		e := e.on(t)
		e.route("big", "big", []string{dest}, nil)
		e.group("b1", "BigOne")
		// Ten distinct values of two labels take some 14,000 characters and a common label 4,000 more, so the Root
		// message is shortened and its Alert list keeps the distinct values.
		long, common := strings.Repeat("x", 700), strings.Repeat("c", 4000)
		for i := range 600 {
			e.alert("b1", fmt.Sprint("b", i), fmt.Sprintf(`{"team":"big","cluster":"big","disk":"disk-%03d-%s",`+
				`"node":"node-%02d-%s","info":%q}`, i, long, i%37, long, common), "")
		}
		e.notify("b1", "first notification")
		g := e.groupOf("big")
		root := e.root(e.number(g))
		text := root.attachment(t).Text
		if utf8.RuneCountInString(root.Message) > fakemattermost.MaxPostSize ||
			utf8.RuneCountInString(text) > fakemattermost.MaxPostSize || !regexp.MustCompile(`\+[0-9]+ more`).MatchString(text) ||
			!strings.Contains(text, "+590 more") || !strings.Contains(text, "+27 more") ||
			!strings.Contains(text, "Full list in Muster") || strings.Contains(text, common) ||
			!strings.Contains(text, "[Open in Muster]("+devmode.PublicURL+"/alert-groups/"+g+")") {
			t.Errorf("message of %d and text of %d characters: %.300q … %.300q", utf8.RuneCountInString(root.Message),
				utf8.RuneCountInString(text), text, text[max(len(text)-300, 0):])
		}
		t.Logf("600 alerts: the attachment text has %d characters", utf8.RuneCountInString(text))
	})

	t.Run("thread reply under a deleted root", func(t *testing.T) {
		e := e.on(t)
		e.route("gone", "gone", []string{dest}, nil)
		e.group("g1", "Gone")
		e.alert("g1", "g1", `{"team":"gone","cluster":"gone"}`, "")
		e.notify("g1", "first notification")
		g := e.groupOf("gone")
		n := e.number(g)
		root := e.root(n)
		patch := "/api/v4/posts/" + root.ID + "/patch"
		// The post is deleted right after the edit for the new Alert was made: the fault answers that edit as made, so
		// the Thread reply, sent before or after it, is what meets the deleted Root message.
		e.fake(http.MethodDelete, e.fmm+"/posts/"+root.ID, "")
		e.fault(fakeserver.Fault{Path: patch, Status: http.StatusOK, Times: 1, Body: fmt.Sprintf(
			`{"id":%q,"channel_id":%q,"message":%q}`, root.ID, fakemattermost.ChannelAlertsProd, root.Message)})
		e.alert("g1", "g2", `{"team":"gone","cluster":"gone","disk":"g2"}`, "")
		e.notify("g1", "new alerts added")
		eventually(t, "the republication", func() bool { return len(e.roots(n)) == 2 })
		var refused []fakeserver.Request
		for _, p := range e.calls(http.MethodPost, "/api/v4/posts") {
			if rootIDOf(t, p) == root.ID {
				refused = append(refused, p)
			}
		}
		if len(refused) != 1 || refused[0].Status != http.StatusBadRequest {
			t.Errorf("replies to the deleted root %+v", refused)
		}
		if reads := e.calls(http.MethodGet, "/api/v4/posts/"+root.ID); len(reads) != 0 {
			t.Errorf("the deleted root was read %d times", len(reads))
		}
		if again := e.root(n); !deletedNote.MatchString(again.attachment(t).Text) {
			t.Errorf("republished %q", again.attachment(t).Text)
		}
		if hl := e.health(dest); hl.State != "healthy" {
			t.Errorf("health %+v", hl)
		}
		e.fake(http.MethodDelete, e.fmm+"/faults", "")
	})

	t.Run("edit refused while the post exists", func(t *testing.T) {
		e := e.on(t)
		e.route("perm", "perm", []string{dest}, nil)
		e.group("p1", "Perm")
		e.alert("p1", "p1", `{"team":"perm","cluster":"perm"}`, "")
		e.notify("p1", "first notification")
		g := e.groupOf("perm")
		root := e.root(e.number(g))
		e.fault(fakeserver.Fault{Path: "/api/v4/posts/" + root.ID + "/patch", Status: http.StatusForbidden, Times: 1,
			Body: `{"id":"api.context.permissions.app_error","message":"You do not have the appropriate permissions.",` +
				`"status_code":403}`})
		e.command(g, "acknowledge")
		if hl := e.waitHealth(dest, "broken"); hl.Reason == nil || !strings.Contains(*hl.Reason, "403") {
			t.Errorf("broken %+v", hl)
		}
		if reads := e.calls(http.MethodGet, "/api/v4/posts/"+root.ID); len(reads) != 1 || reads[0].Status != http.StatusOK {
			t.Errorf("reads of the post %+v", reads)
		}
		if len(e.roots(e.number(g))) != 1 {
			t.Error("the root message was published again")
		}
		e.api.json(http.MethodPost, "/api/v1/destinations/"+dest+"/checks", "", http.StatusOK)
		e.waitHealth(dest, "healthy")
	})
}

// TestMattermostBrokenRecovery is C-11.AC-6 and AC-7 against `muster dev` and the fake Mattermost server: repeated
// 500s on a Publication until the Transient budget runs out make the Destination Broken as unavailable, raise
// MusterDestinationBroken and leave the delivery waiting; while it is Broken, A starts and keeps firing, B, published
// before, is acknowledged and gets a new Alert, and C starts and resolves; after a successful probe, A is published as
// a Loud new message, B's Root message is edited once with no Thread reply, and C is not published.
func TestMattermostBrokenRecovery(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newMM(t, h, r)
	conn := e.connection(50)
	dest := e.destination("prod", conn, fakemattermost.ChannelAlertsProd, map[string]string{"new_alert_group": "here"}, 50)
	e.route("c11", "c11", []string{dest}, nil)
	e.group("k1", "Recovery")
	e.alert("k1", "b", `{"team":"c11","cluster":"b"}`, "")
	e.notify("k1", "first notification")
	gb := e.groupOf("b")
	rootB := e.root(e.number(gb))

	e.fault(fakeserver.Fault{Path: "/api/v4/posts", Status: http.StatusInternalServerError})
	e.fault(fakeserver.Fault{Path: "/api/v4/posts/*/patch", Status: http.StatusInternalServerError})
	e.alert("k1", "z", `{"team":"c11","cluster":"z"}`, "")
	e.notify("k1", "new alerts added")
	gz := e.groupOf("z")
	failed := func() int {
		n := 0
		for _, p := range e.calls(http.MethodPost, "/api/v4/posts") {
			if p.Status == http.StatusInternalServerError {
				n++
			}
		}
		return n
	}
	eventually(t, "the first 500", func() bool { return failed() >= 1 })
	for i := 0; e.health(dest).State != "broken"; i++ {
		if i == 12 {
			t.Fatalf("still %+v after %d failed attempts", e.health(dest), failed())
		}
		before := failed()
		advance(t, r, 301)
		eventually(t, "the next attempt", func() bool { return failed() > before || e.health(dest).State == "broken" })
	}
	hl := e.health(dest)
	if hl.Reason == nil || !strings.Contains(*hl.Reason, "500") {
		t.Errorf("broken %+v", hl)
	}
	t.Logf("broken after %d failed attempts: %s", failed(), *hl.Reason)
	eventually(t, "MusterDestinationBroken", func() bool {
		return slices.Contains(e.internalAlerts(), "MusterDestinationBroken")
	})
	if ev := deliveryEvents(e.timeline(gz, "")); slices.Contains(ev, "not_delivered") {
		t.Errorf("the waiting delivery ended: %v", ev)
	}
	if e.alertGroup(gz)["delivery_problem"] != true {
		t.Error("the waiting delivery of a Broken destination is no delivery problem")
	}

	// While Broken: A starts, B is acknowledged and gets a new Alert, C starts and resolves.
	e.alert("k1", "a", `{"team":"c11","cluster":"a"}`, "")
	e.alert("k1", "c", `{"team":"c11","cluster":"c"}`, "")
	e.alert("k1", "b2", `{"team":"c11","cluster":"b","disk":"b2"}`, "")
	e.notify("k1", "new alerts added")
	ga, gc := e.groupOf("a"), e.groupOf("c")
	e.command(gb, "acknowledge")
	e.alert("k1", "c", `{"team":"c11","cluster":"c"}`, `,"status":"resolved"`)
	e.notify("k1", "some alerts resolved")
	if s := e.alertGroup(gc)["status"]; s != "resolved" {
		t.Fatalf("C is %v", s)
	}

	e.fake(http.MethodDelete, e.fmm+"/faults", "")
	e.fake(http.MethodDelete, e.fmm+"/requests", "")
	advance(t, r, 301)
	e.waitHealth(dest, "healthy")
	rootA := e.root(e.number(ga))
	e.root(e.number(gz))
	if !strings.Contains(rootA.Message, "@here") {
		t.Errorf("A was published quietly: %q", rootA.Message)
	}
	eventually(t, "the deliveries to settle", func() bool {
		return h.count(t, `SELECT count(*) FROM deliveries WHERE state = 'pending'`) == 0
	})
	if edits := e.calls(http.MethodPut, "/api/v4/posts/"+rootB.ID+"/patch"); len(edits) != 1 {
		t.Errorf("B's root message was edited %d times", len(edits))
	}
	if c := e.post(rootB.ID).attachment(t).Color; c != "#f57c00" {
		t.Errorf("B shows %s", c)
	}
	if replies := e.replies(rootB.ID); len(replies) != 0 {
		t.Errorf("B got thread replies %+v", replies)
	}
	if got := e.roots(e.number(gc)); len(got) != 0 {
		t.Errorf("C was published: %+v", got)
	}
	eventually(t, "MusterDestinationBroken to resolve", func() bool {
		return !slices.Contains(e.internalAlerts(), "MusterDestinationBroken")
	})
}

// TestMattermostConnectionHold is C-13.AC-15 for every Destination of a Connection, on one replica: two Destinations
// of one Connection, in ch-alerts and ch-alerts-prod, each with a Thread reply due when its batching window ends; the
// first reply meets a 429 with a plain-text body and Retry-After: 1, and the next post to each Destination, the other
// one's included, waits 1–2 s after it.
func TestMattermostConnectionHold(t *testing.T) {
	h := Start(t, FakesInProcess)
	r := h.StartReplica(t, ReplicaOptions{})
	e := newMM(t, h, r)
	conn := e.connection(50)
	alerts := e.destination("alerts", conn, fakemattermost.ChannelAlerts, nil, 50)
	prod := e.destination("prod", conn, fakemattermost.ChannelAlertsProd, nil, 50)
	e.route("db", "db", []string{alerts}, nil)
	e.route("web", "web", []string{prod}, nil)

	e.group("h1", "Hold")
	e.alert("h1", "a1", `{"team":"db","cluster":"a"}`, "")
	e.alert("h1", "w1", `{"team":"web","cluster":"w"}`, "")
	e.notify("h1", "first notification")
	rootA, rootW := e.root(e.number(e.groupOf("a"))), e.root(e.number(e.groupOf("w")))
	if rootA.ChannelID != fakemattermost.ChannelAlerts || rootW.ChannelID != fakemattermost.ChannelAlertsProd {
		t.Fatalf("roots in %s and %s", rootA.ChannelID, rootW.ChannelID)
	}
	// The first new Alerts are replied to at once and open the batching window, which collects the next ones until
	// it ends, for both Destinations at the same time.
	e.alert("h1", "a2", `{"team":"db","cluster":"a","disk":"a2"}`, "")
	e.alert("h1", "w2", `{"team":"web","cluster":"w","disk":"w2"}`, "")
	e.notify("h1", "new alerts added")
	eventually(t, "the first thread replies", func() bool {
		return len(e.replies(rootA.ID)) == 1 && len(e.replies(rootW.ID)) == 1
	})
	e.alert("h1", "a3", `{"team":"db","cluster":"a","disk":"a3"}`, "")
	e.alert("h1", "w3", `{"team":"web","cluster":"w","disk":"w3"}`, "")
	e.notify("h1", "new alerts added")
	sent := len(e.calls(http.MethodPost, "/api/v4/posts"))
	e.fault(fakeserver.Fault{Path: "/api/v4/posts", Status: http.StatusTooManyRequests, RetryAfterSeconds: 1,
		ContentType: "text/plain", Body: "limit exceeded", Times: 1})
	advance(t, r, 60)
	eventually(t, "the batched thread replies", func() bool {
		return len(e.replies(rootA.ID)) == 2 && len(e.replies(rootW.ID)) == 2
	})
	posts := e.calls(http.MethodPost, "/api/v4/posts")[sent:]
	if len(posts) != 3 || posts[0].Status != http.StatusTooManyRequests {
		t.Fatalf("posts after the batching window: %+v", posts)
	}
	held := requestBody(t, posts[0])["channel_id"]
	var waited, gaps []any
	for _, p := range posts[1:] {
		channel, gap := requestBody(t, p)["channel_id"], p.AtMs-posts[0].AtMs
		waited, gaps = append(waited, channel), append(gaps, gap)
		if p.Status != http.StatusCreated || gap < 1000 || gap >= 2000 {
			t.Errorf("the post to %v after the 429 in %v: status %d, %d ms later", channel, held, p.Status, gap)
		}
	}
	if !slices.Contains(waited, any(fakemattermost.ChannelAlerts)) ||
		!slices.Contains(waited, any(fakemattermost.ChannelAlertsProd)) {
		t.Errorf("the posts after the 429 went to %v", waited)
	}
	t.Logf("429 in %v; the next posts to %v followed it after %v ms", held, waited, gaps)
}

// TestMattermostRestart is C-11.AC-5 in the harness mode with the fakes in the test process: the fake Mattermost
// creates a new post and holds its answer; the `muster dev --replica` process is stopped while it waits and started
// again while the fake keeps its records; the Publication is made a second time and the Alert Group's Timeline has a
// possible_duplicate delivery event.
func TestMattermostRestart(t *testing.T) {
	h := Start(t, FakesInProcess)
	r := h.StartReplica(t, ReplicaOptions{})
	e := newMM(t, h, r)
	conn := e.connection(50)
	dest := e.destination("alerts", conn, fakemattermost.ChannelAlerts, nil, 50)
	e.route("dup", "dup", []string{dest}, nil)

	e.fault(fakeserver.Fault{Path: "/api/v4/posts", DelayMs: 60_000, Times: 1})
	e.group("u1", "Duplicate")
	e.alert("u1", "u1", `{"team":"dup","cluster":"dup"}`, "")
	e.notify("u1", "first notification")
	g := e.groupOf("dup")
	n := e.number(g)
	eventually(t, "the post whose answer is held", func() bool { return len(e.roots(n)) == 1 })
	r.Stop(t)
	// The row's lease runs on the real clock, which the development clock does not move; ending it stands in for the
	// minute a restarted replica would otherwise wait.
	h.exec(t, `UPDATE deliveries SET lease_until = now() WHERE lease_until IS NOT NULL`)
	r.Start(t)
	eventually(t, "the second post", func() bool { return len(e.roots(n)) == 2 })
	eventually(t, "the possible duplicate", func() bool {
		return slices.Contains(deliveryEvents(e.timeline(g, "")), "possible_duplicate")
	})
	if reqs := h.InProcess.Mattermost.Requests(); len(reqs) == 0 {
		t.Error("the fake lost its records")
	}
}
