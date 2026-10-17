// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/telegram"
)

const textNotLinkedMM = "Your Mattermost account is not linked to Muster. Link it in your profile: " +
	devmode.PublicURL + "/profile"

var deepLinkB = regexp.MustCompile(`^https://t\.me/` + faketelegram.SecondBotUsername + `\?start=([A-Za-z0-9_-]{43})$`)

// several drives several Connections of one messenger (S-069): the linksEnv of S-051 — the Admin's Personal access
// token, the Integration "lab", the demo Telegram Connection — with the second fake Mattermost server and the second
// bots of the fake servers.
type several struct {
	*linksEnv
	fmm2 string
}

func newSeveral(t *testing.T, h *Harness, r *Replica) *several {
	t.Helper()
	return &several{linksEnv: &linksEnv{timersEnv: &timersEnv{tgd: newTGD(t, h, r)}},
		fmm2: h.Fakes.MattermostSecond + "/_fake"}
}

// server is the mm environment that reads the fake Mattermost server whose control URL is fmm.
func (e *several) server(fmm string) *mm {
	c := *e.mm
	c.fmm = fmm
	return &c
}

// mmConn creates a Mattermost Connection to the fake server at base with the bot token.
func (e *several) mmConn(name, base, token string) string {
	e.t.Helper()
	return e.api.json(http.MethodPost, "/api/v1/connections", fmt.Sprintf(`{"type":"mattermost","name":%q,
		"server_url":%q,"bot_token":%q,"proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":1}}`, name, base,
		token), http.StatusCreated)["id"].(string)
}

// tgConn creates a Telegram Connection in the long-polling mode with the bot token and waits for its first poll.
func (e *several) tgConn(name, token string) string {
	e.t.Helper()
	id := e.api.json(http.MethodPost, "/api/v1/connections", fmt.Sprintf(`{"type":"telegram","name":%q,"bot_token":%q,
		"bot_api_base_url":%q,"update_mode":"long_polling","proxy":{"enabled":false},
		"limiter":{"limit":30,"per_seconds":1}}`, name, token, e.h.Fakes.Telegram), http.StatusCreated)["id"].(string)
	e.r.waitFor(e.t, "the poll of "+name, func() bool {
		return slices.Contains(recordedPaths(e.t, e.h.Fakes.Telegram), "/bot"+token+"/getUpdates")
	})
	return id
}

// tgCreate is createDestination of a Telegram Destination on channel through the Connection conn.
func (e *several) tgCreate(name, conn, channel, mentions string) answer {
	e.t.Helper()
	return e.api.do(http.MethodPost, "/api/v1/destinations", fmt.Sprintf(`{"type":"telegram","name":%q,
		"connection_id":%q,"channel_id":%q,"mentions":%s,"limiter":{"limit":60,"per_seconds":60}}`, name, conn, channel,
		mentions))
}

// tgDest creates a Telegram Destination on @muster_alerts through conn and returns its id.
func (e *several) tgDest(name, conn, mentions string) string {
	e.t.Helper()
	a := e.tgCreate(name, conn, "@"+faketelegram.ChannelUsername, mentions)
	if a.status != http.StatusCreated {
		e.t.Fatalf("telegram destination %s = %d %s", name, a.status, a.body)
	}
	var d struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	decode(e.t, a, &d)
	return d.Destination.ID
}

// newAlert adds the Alert name, told apart by its label disk, to the fake Alertmanager's group of the cluster on the
// Route of team, sends the group's Snapshot and waits until the Alert Group has a Thread reply about it in each of its
// dests messenger Destinations, collecting until the end of its batching window on the business clock.
func (e *several) newAlert(cluster, name, team, group string, dests int) {
	e.t.Helper()
	replies := func() int64 {
		return e.h.count(e.t, `SELECT count(*) FROM thread_replies t JOIN alert_groups g ON g.id = t.alert_group_id
			WHERE t.org_id = 1 AND t.event = 'alerts_added' AND g.public_id = '`+group+`'`)
	}
	before := replies()
	e.alert(cluster, name, `{"team":"`+team+`","cluster":"`+cluster+`","severity":"warning","disk":"`+name+`"}`, "")
	e.notify(cluster, "new alerts added")
	eventually(e.t, "the thread replies about "+name, func() bool { return replies() >= before+int64(dests) })
}

// mentionOf are Mention settings with the User mentioned on a new Alert Group and nothing else.
func mentionOf(user string) string {
	m := map[string]any{}
	for _, event := range []string{"new_alert_group", "new_alerts", "reopen", "ack_timeout", "snooze_ended",
		"rise_to_urgent"} {
		m[event] = map[string]any{"everyone": "none", "user_ids": []string{}, "groups": []string{}}
	}
	m["new_alert_group"] = map[string]any{"everyone": "none", "user_ids": []string{user}, "groups": []string{}}
	b, _ := json.Marshal(m)
	return string(b)
}

// text is the one text value a query returns.
func (h *Harness) text(t *testing.T, sql string) string {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	var s string
	if err := conn.QueryRow(t.Context(), sql).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

// messageOf is the message id of the Root message of the Alert Group in the Destination, empty before there is one.
func (e *several) messageOf(group, dest string) string {
	e.t.Helper()
	return e.h.text(e.t, `SELECT coalesce((SELECT d.message_id FROM deliveries d
		JOIN destinations ds ON ds.id = d.destination_id JOIN alert_groups g ON g.id = d.alert_group_id
		WHERE ds.org_id = 1 AND ds.public_id = '`+dest+`' AND g.public_id = '`+group+`'), '')`)
}

// tgPostOf is the channel post of the Alert Group in the Telegram Destination.
func (e *several) tgPostOf(group, dest string) int64 {
	e.t.Helper()
	var id int64
	eventually(e.t, "the channel post of "+dest, func() bool {
		id, _ = strconv.ParseInt(e.messageOf(group, dest), 10, 64)
		return id != 0
	})
	return id
}

// replyIDs are the message ids of the sent Thread replies of event of the Alert Group in the Destination, oldest first.
func (e *several) replyIDs(group, dest, event string) []string {
	e.t.Helper()
	conn, err := pgx.Connect(e.t.Context(), e.h.DatabaseURL)
	if err != nil {
		e.t.Fatal(err)
	}
	defer conn.Close(e.t.Context())
	rows, err := conn.Query(e.t.Context(), `SELECT t.message_id FROM thread_replies t
		JOIN destinations ds ON ds.id = t.destination_id JOIN alert_groups g ON g.id = t.alert_group_id
		WHERE t.org_id = 1 AND t.state = 'sent' AND t.message_id IS NOT NULL AND t.event = $1
		AND ds.public_id = $2 AND g.public_id = $3 ORDER BY t.id`, event, dest, group)
	if err != nil {
		e.t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		e.t.Fatal(err)
	}
	return ids
}

// waitReply waits for a sent Thread reply of event of the Alert Group in the Destination and returns the newest.
func (e *several) waitReply(group, dest, event string) string {
	e.t.Helper()
	var ids []string
	deadline := time.Now().Add(15 * time.Second)
	for ids = e.replyIDs(group, dest, event); len(ids) == 0; ids = e.replyIDs(group, dest, event) {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s in %s; thread replies: %s", event, dest, e.h.text(e.t,
				`SELECT coalesce(string_agg(ds.name || ' ' || t.event || ' ' || t.state || ' ' ||
				coalesce(t.message_id, '-') || ' ' || coalesce(t.last_error, ''), '; ' ORDER BY t.id), '')
				FROM thread_replies t JOIN destinations ds ON ds.id = t.destination_id WHERE t.org_id = 1`))
		}
		time.Sleep(pollInterval)
	}
	return ids[len(ids)-1]
}

type deliveryItem struct {
	Destination struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"destination"`
	State             string `json:"state"`
	ThreadNotAttached bool   `json:"thread_not_attached"`
	MessageURL        string `json:"message_url"`
}

// deliveries is listAlertGroupDeliveries of the Alert Group, by Destination name.
func (e *several) deliveries(group string) map[string]deliveryItem {
	e.t.Helper()
	var page struct {
		Items []deliveryItem `json:"items"`
	}
	decode(e.t, e.api.do(http.MethodGet, "/api/v1/alert-groups/"+group+"/deliveries", ""), &page)
	out := map[string]deliveryItem{}
	for _, d := range page.Items {
		out[d.Destination.Name] = d
	}
	return out
}

// rootBy waits for the live Root message of Alert Group #n posted by the bot.
func rootBy(m *mm, n int, bot string) mmPost {
	m.t.Helper()
	var root mmPost
	eventually(m.t, fmt.Sprintf("the root message of #%d by %s", n, bot), func() bool {
		for _, p := range m.roots(n) {
			if p.DeleteAt == 0 && p.UserID == bot {
				root = p
			}
		}
		return root.ID != ""
	})
	return root
}

// wantCallbacks checks that every button of the post calls back the Connection conn.
func wantCallbacks(t *testing.T, p mmPost, conn string) {
	t.Helper()
	for _, a := range p.attachment(t).Actions {
		if a.Integration.URL != devmode.IngestURL+"/api/v1/callbacks/mattermost/"+conn {
			t.Errorf("button %s of post %s by %s calls %s, want Connection %s", a.ID, p.ID, p.UserID, a.Integration.URL,
				conn)
		}
	}
}

// patches are the edits of the post on the fake Mattermost server of m, with the token each came with.
func patches(m *mm, post string) (statuses []int, tokens []string) {
	m.t.Helper()
	for _, r := range m.calls(http.MethodPut, "/api/v4/posts/"+post+"/patch") {
		statuses = append(statuses, r.Status)
		auth := ""
		if v := r.Headers["Authorization"]; len(v) > 0 {
			auth = strings.TrimPrefix(v[0], "Bearer ")
		}
		tokens = append(tokens, auth)
	}
	return statuses, tokens
}

// wantPatchedBy waits for an edit of the post and checks that every edit came with token and succeeded.
func wantPatchedBy(m *mm, post, token string, atLeast int) {
	m.t.Helper()
	eventually(m.t, "the edits of "+post, func() bool {
		s, _ := patches(m, post)
		return len(s) >= atLeast
	})
	statuses, tokens := patches(m, post)
	for i := range statuses {
		if statuses[i] != http.StatusOK || tokens[i] != token {
			m.t.Errorf("edit %d of post %s answered %d with another bot's token: %t", i, post, statuses[i],
				tokens[i] != token)
		}
	}
}

// tgMessage is the message id of the chat in the fake Telegram server.
func (e *several) tgMessage(chat, id int64) faketelegram.Message {
	e.t.Helper()
	for _, m := range e.messages(chat) {
		if m.ID == id {
			return m
		}
	}
	e.t.Fatalf("no message %d in %d", id, chat)
	return faketelegram.Message{}
}

// editorsOf are the bots that edited the channel post.
func (e *several) editorsOf(post int64) []int64 {
	e.t.Helper()
	var by []int64
	for _, x := range e.tgMessage(faketelegram.ChannelID, post).Edits {
		by = append(by, x.By)
	}
	return by
}

// wantEditedBy waits for at least n edits of the channel post and checks that the bot made each.
func (e *several) wantEditedBy(post, bot int64, n int) {
	e.t.Helper()
	eventually(e.t, fmt.Sprintf("%d edits of post %d", n, post), func() bool { return len(e.editorsOf(post)) >= n })
	for _, by := range e.editorsOf(post) {
		if by != bot {
			e.t.Errorf("post %d edited by %d, want %d only: %v", post, by, bot, e.editorsOf(post))
		}
	}
}

// wantThreadReply checks that the message id of the discussion group is a reply of the bot to the copy.
func (e *several) wantThreadReply(id string, bot, copyID int64) {
	e.t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		e.t.Fatalf("message id %q", id)
	}
	m := e.tgMessage(faketelegram.GroupID, n)
	if m.From == nil || m.From.ID != bot || replyTo(m) != copyID || m.MessageThreadID != copyID {
		e.t.Errorf("thread reply %d by %+v replies to %d in thread %d, want by %d to the copy %d", n, m.From, replyTo(m),
			m.MessageThreadID, bot, copyID)
	}
}

// waitCopy waits for the automatic copy of the channel post and returns its id.
func (e *several) waitCopy(post int64) int64 {
	e.t.Helper()
	var cp *faketelegram.Message
	eventually(e.t, fmt.Sprintf("the copy of post %d", post), func() bool {
		cp = e.copyOf(post)
		return cp != nil
	})
	return cp.ID
}

// anchors are the Telegram deliveries of the Alert Group, by Destination name: thread state and anchor.
func (e *several) anchors(group string) map[string]string {
	e.t.Helper()
	conn, err := pgx.Connect(e.t.Context(), e.h.DatabaseURL)
	if err != nil {
		e.t.Fatal(err)
	}
	defer conn.Close(e.t.Context())
	rows, err := conn.Query(e.t.Context(), `SELECT ds.name, d.thread_state || ' ' || coalesce(d.thread_anchor_id, '')
		FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id JOIN alert_groups g ON g.id = d.alert_group_id
		WHERE d.org_id = 1 AND ds.type = 'telegram' AND g.public_id = $1`, group)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]string{}
	for rows.Next() {
		var name, v string
		if err := rows.Scan(&name, &v); err != nil {
			e.t.Fatal(err)
		}
		out[name] = v
	}
	return out
}

// pressAs presses the button label of the message id of chat as bob's Telegram account 5001; with token set, the
// update goes to that bot instead of the bot that sent the message. It returns the answer once one is recorded.
func (e *several) pressAs(chat, id int64, label, token string) faketelegram.Answer {
	e.t.Helper()
	before := len(e.answers())
	body, _ := json.Marshal(map[string]any{"chat": chat, "message_id": id, "button": label, "token": token,
		"from": map[string]any{"id": 5001, "username": "bob_t"}})
	e.fake(http.MethodPost, e.ftg+"/press", string(body))
	eventually(e.t, "the answer to "+label, func() bool { return len(e.answers()) > before })
	return e.answers()[before]
}

// lastAnswerBy is the bot token of the newest answerCallbackQuery recorded by the fake Telegram server.
func (e *several) lastAnswerBy() string {
	e.t.Helper()
	reqs := e.requests()
	i := lastIndex(reqs, "answerCallbackQuery")
	if i < 0 {
		return ""
	}
	token, _, _ := strings.Cut(strings.TrimPrefix(reqs[i].Path, "/bot"), "/")
	return token
}

// endPollsAfterClockMove ends the held long polls of both Telegram Connections after the development clock moved, as
// pollAfterClockMove does for the demo one.
func (e *several) endPollsAfterClockMove(tokens ...string) {
	e.t.Helper()
	for _, token := range tokens {
		dropped := func() int { return strings.Count(e.r.Output(), `"event":"telegram_update_dropped"`) }
		before := dropped()
		e.fake(http.MethodPost, e.ftg+"/updates", `{"token":"`+token+
			`","update":{"message":{"message_id":1,"date":0,"chat":{"id":1,"type":"none"}}}}`)
		eventually(e.t, "the update that ends the long poll", func() bool { return dropped() > before })
	}
}

// linkMM links the Mattermost account bob of p through the Connection conn, whose bot sends the code as a direct
// message on the fake server of m, and returns the link.
func (e *several) linkMM(p linker, m *mm, conn, bot string) map[string]any {
	e.t.Helper()
	dms := func() []fakemattermost.DM {
		var out []fakemattermost.DM
		decode(e.t, call(e.t, http.MethodGet, m.fmm+"/dms", ""), &out)
		return out
	}
	before := len(dms())
	a := p.session.do(http.MethodPost, "/api/v1/me/account-links/mattermost", `{"connection_id":"`+conn+
		`","username":"bob"}`)
	if a.status != http.StatusAccepted {
		e.t.Fatalf("start a mattermost link through %s = %d %s", conn, a.status, a.body)
	}
	var s struct {
		RequestID string `json:"request_id"`
	}
	decode(e.t, a, &s)
	got := dms()
	if len(got) != before+1 {
		e.t.Fatalf("%d direct messages after %d", len(got), before)
	}
	dm := got[len(got)-1]
	_, code, _ := strings.Cut(dm.Message, "\n\n")
	if dm.UserID != fakemattermost.BobUserID || dm.BotID != bot || !linkCode.MatchString(code) {
		e.t.Fatalf("the code message to %s from %s, want from %s", dm.UserID, dm.BotID, bot)
	}
	return p.session.json(http.MethodPost, "/api/v1/me/account-links/mattermost/confirmation", `{"request_id":"`+
		s.RequestID+`","code":"`+code+`"}`, http.StatusCreated)
}

// privateTo sends text from the Telegram account id to the bot of token and returns that bot's answer in its own
// conversation once it is there.
func (e *several) privateTo(token string, bot, id int64, username, text string) string {
	e.t.Helper()
	answers := func() []string {
		a := call(e.t, http.MethodGet, fmt.Sprintf("%s/messages?chat=%d&bot=%d", e.ftg, id, bot), "")
		if a.status == http.StatusNotFound {
			return nil
		}
		var msgs []faketelegram.Message
		decode(e.t, a, &msgs)
		var out []string
		for _, m := range msgs {
			if m.From != nil && m.From.ID == bot {
				out = append(out, m.Text)
			}
		}
		return out
	}
	before := len(answers())
	body, _ := json.Marshal(map[string]any{"token": token, "from": map[string]any{"id": id, "username": username},
		"text": text})
	e.fake(http.MethodPost, e.ftg+"/private", string(body))
	eventually(e.t, fmt.Sprintf("the answer of bot %d to %d", bot, id), func() bool { return len(answers()) > before })
	return answers()[before]
}

// startTG starts a Telegram link of p through the Connection conn and returns its deep link.
func (e *several) startTG(p linker, conn string) string {
	e.t.Helper()
	return fmt.Sprint(p.session.json(http.MethodPost, "/api/v1/me/account-links/telegram", `{"connection_id":"`+conn+`"}`,
		http.StatusCreated)["deep_link"])
}

// pressLogged reports whether the log has a press event of the messenger for the Connection with the outcome.
func (e *several) pressLogged(event, conn, outcome string) bool {
	for line := range strings.Lines(e.r.Output()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["event"] == event && m["connection"] == conn &&
			m["outcome"] == outcome {
			return true
		}
	}
	return false
}

// TestSeveralConnectionsRoute is S-069 against `muster dev` with two fake Mattermost servers and two Telegram bots: a
// Route with the Mattermost Destinations of two Connections on server 1 and one on server 2, two Telegram Destinations
// through two Connections in one channel and an outgoing webhook gives every Destination its own Root message and its
// own events (C-11.FR-1, C-14.FR-2); Acknowledge, a new Alert and Resolve reach each Root message and Thread through
// its own Connection only (C-13.FR-4, C-14.FR-4); listLinkableConnections lists every Connection of every messenger,
// the demo Bridge Connection "Dev Bridge" too (C-18.FR-10, C-22.FR-15).
func TestSeveralConnectionsRoute(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newSeveral(t, h, r)
	s1, s2 := e.mm, e.server(e.fmm2)
	ma := e.mmConn("mm-a", h.Fakes.Mattermost, devmode.ConnectionBotToken)
	mb := e.mmConn("mm-b", h.Fakes.Mattermost, devmode.MattermostSecondBotToken)
	m2 := e.mmConn("mm-2", h.Fakes.MattermostSecond, devmode.ConnectionBotToken)
	tb := e.tgConn("tg-b", devmode.TelegramSecondBotToken)
	da := e.destination("mm-a-alerts", ma, fakemattermost.ChannelAlerts, nil, 50)
	db := e.destination("mm-b-alerts", mb, fakemattermost.ChannelAlerts, nil, 50)
	d2 := e.destination("mm-2-alerts", m2, fakemattermost.ChannelAlerts, nil, 50)
	ta := e.tgDest("tg-a-alerts", e.conn, mentionSettings(nil))
	tdb := e.tgDest("tg-b-alerts", tb, mentionSettings(nil))
	hook := e.webhook("hook", "s069", "[]")["id"].(string)
	e.route("multi", "multi", []string{da, db, d2, ta, tdb, hook}, nil)

	g := e.fire("m1", "DiskFull", "multi", true)
	n := e.number(g)
	eventually(t, "five messenger deliveries", func() bool {
		delivered := 0
		for _, d := range e.deliveries(g) {
			if d.Destination.Type != "webhook" && d.State == "delivered" {
				delivered++
			}
		}
		return delivered == 5
	})
	urls := map[string]bool{}
	for name, d := range e.deliveries(g) {
		if d.Destination.Type != "webhook" {
			urls[d.MessageURL] = true
		}
		t.Logf("delivery %s: %s %s", name, d.State, d.MessageURL)
	}
	if len(urls) != 5 {
		t.Errorf("message urls %v, want five different ones", urls)
	}
	eventually(t, "the creation event at the webhook", func() bool {
		for _, x := range e.received("s069") {
			if x.Body.Event == "created" && x.Body.AlertGroup.ID == g {
				return true
			}
		}
		return false
	})

	// Mattermost: two Root messages on server 1, one per bot, and one on server 2, each calling back its Connection.
	rootA, rootB, root2 := rootBy(s1, n, fakemattermost.BotUserID), rootBy(s1, n, fakemattermost.SecondBotUserID),
		rootBy(s2, n, fakemattermost.BotUserID)
	if got := len(s1.roots(n)); got != 2 {
		t.Errorf("%d root messages on server 1", got)
	}
	if got := len(s2.roots(n)); got != 1 {
		t.Errorf("%d root messages on server 2", got)
	}
	wantCallbacks(t, rootA, ma)
	wantCallbacks(t, rootB, mb)
	wantCallbacks(t, root2, m2)
	if e.messageOf(g, da) != rootA.ID || e.messageOf(g, db) != rootB.ID || e.messageOf(g, d2) != root2.ID {
		t.Errorf("deliveries %s %s %s, posts %s %s %s", e.messageOf(g, da), e.messageOf(g, db), e.messageOf(g, d2),
			rootA.ID, rootB.ID, root2.ID)
	}

	// Telegram: two channel posts, by two bots; both copies reach both Connections, and each Thread attaches to the
	// copy of its own post.
	pa, pb := e.tgPostOf(g, ta), e.tgPostOf(g, tdb)
	if from := e.tgMessage(faketelegram.ChannelID, pa).From; from == nil || from.ID != faketelegram.BotID {
		t.Errorf("post of tg-a-alerts from %+v", from)
	}
	if from := e.tgMessage(faketelegram.ChannelID, pb).From; from == nil || from.ID != faketelegram.SecondBotID {
		t.Errorf("post of tg-b-alerts from %+v", from)
	}
	ca, cb := e.waitCopy(pa), e.waitCopy(pb)
	eventually(t, "both copies buffered for both Connections", func() bool {
		return h.count(t, `SELECT count(*) FROM telegram_post_copies WHERE org_id = 1`) == 4
	})
	eventually(t, "both threads attached", func() bool {
		a := e.anchors(g)
		return a["tg-a-alerts"] == fmt.Sprintf("attached %d", ca) && a["tg-b-alerts"] == fmt.Sprintf("attached %d", cb)
	})

	// Acknowledge edits each Root message through its own Connection.
	if out := e.command(g, "acknowledge"); out != "done" {
		t.Fatalf("acknowledge = %s", out)
	}
	e.wantEditedBy(pa, faketelegram.BotID, 1)
	e.wantEditedBy(pb, faketelegram.SecondBotID, 1)
	wantPatchedBy(s1, rootA.ID, devmode.ConnectionBotToken, 1)
	wantPatchedBy(s1, rootB.ID, devmode.MattermostSecondBotToken, 1)
	wantPatchedBy(s2, root2.ID, devmode.ConnectionBotToken, 1)
	eventually(t, "the acknowledged event at the webhook", func() bool {
		for _, x := range e.received("s069") {
			if x.Body.Event == "acknowledged" && x.Body.AlertGroup.ID == g {
				return true
			}
		}
		return false
	})

	// A new Alert: each Thread reply under its own Root message or copy, by its own bot.
	e.newAlert("m1", "b", "multi", g, 5)
	e.adv(61)
	for _, c := range []struct {
		m    *mm
		dest string
		root mmPost
		bot  string
	}{{s1, da, rootA, fakemattermost.BotUserID}, {s1, db, rootB, fakemattermost.SecondBotUserID},
		{s2, d2, root2, fakemattermost.BotUserID}} {
		id := e.waitReply(g, c.dest, "alerts_added")
		p := c.m.post(id)
		if p.RootID != c.root.ID || p.UserID != c.bot {
			t.Errorf("thread reply of %s: root %s by %s, want root %s by %s", c.dest, p.RootID, p.UserID, c.root.ID,
				c.bot)
		}
	}
	e.wantThreadReply(e.waitReply(g, ta, "alerts_added"), faketelegram.BotID, ca)
	e.wantThreadReply(e.waitReply(g, tdb, "alerts_added"), faketelegram.SecondBotID, cb)

	// Resolve: the final edits, again each through its own Connection.
	editsA, editsB := len(e.editorsOf(pa)), len(e.editorsOf(pb))
	if out := e.command(g, "resolve"); out != "done" {
		t.Fatalf("resolve = %s", out)
	}
	e.settle()
	e.wantEditedBy(pa, faketelegram.BotID, editsA+1)
	e.wantEditedBy(pb, faketelegram.SecondBotID, editsB+1)
	wantPatchedBy(s1, rootA.ID, devmode.ConnectionBotToken, 2)
	wantPatchedBy(s1, rootB.ID, devmode.MattermostSecondBotToken, 2)
	wantPatchedBy(s2, root2.ID, devmode.ConnectionBotToken, 2)
	for _, m := range []*mm{s1, s2} {
		for _, q := range m.requests() {
			if strings.HasSuffix(q.Path, "/patch") && q.Status != http.StatusOK {
				t.Errorf("an edit on %s answered %d: %s", m.fmm, q.Status, q.Path)
			}
		}
	}
	for _, q := range e.requests() {
		if strings.HasSuffix(q.Path, "/editMessageText") && q.Status != http.StatusOK {
			t.Errorf("an editMessageText answered %d: %s", q.Status, q.Body)
		}
	}

	// C-18.FR-10, C-22.FR-15: the Connections an account can be linked through, every messenger, the demo Bridge
	// Connection among them, and two or more of Mattermost and Telegram.
	var names []string
	for _, x := range e.api.json(http.MethodGet, "/api/v1/me/account-links/connections", "",
		http.StatusOK)["items"].([]any) {
		c := x.(map[string]any)
		names = append(names, fmt.Sprint(c["messenger"], " ", c["name"]))
	}
	want := []string{"bridge " + devmode.BridgeConnectionName, "mattermost " + devmode.ConnectionName,
		"mattermost mm-2", "mattermost mm-a", "mattermost mm-b", "telegram " + devmode.TelegramConnectionName,
		"telegram tg-b"}
	if !slices.Equal(names, want) {
		t.Errorf("linkable connections %v, want %v", names, want)
	}
}

// TestSeveralConnectionsMattermostSameChannel is S-069 against `muster dev`: two Mattermost Connections to one server,
// both in one channel, post two Root messages whose buttons call back their own Connection (C-13.FR-4); the Identity
// space is the Connection: a press through Connection B by bob, linked through Connection A only, is answered "not
// linked" and changes nothing, and runs as bob once he links through Connection B with the code from bot B (C-18.FR-4,
// FR-8, AC-3); a callback to one Connection carrying the other's post is refused and changes nothing; ack timeout
// notices and Reminders reply under their own Root message, by their own bot (C-17.FR-2, FR-4), and "Still on it" on
// bot B's Reminder is answered by bob; the Destination check sees each bot's own membership.
func TestSeveralConnectionsMattermostSameChannel(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newSeveral(t, h, r)
	ma := e.mmConn("mm-a", h.Fakes.Mattermost, devmode.ConnectionBotToken)
	mb := e.mmConn("mm-b", h.Fakes.Mattermost, devmode.MattermostSecondBotToken)
	da := e.destination("mm-a-alerts", ma, fakemattermost.ChannelAlerts, nil, 50)
	db := e.destination("mm-b-alerts", mb, fakemattermost.ChannelAlerts, nil, 50)
	e.route("t", "t", []string{da, db}, nil)
	bob := e.person("bob", "bob", "responder")

	g := e.fire("s1", "HostDown", "t", true)
	n := e.number(g)
	rootA, rootB := rootBy(e.mm, n, fakemattermost.BotUserID), rootBy(e.mm, n, fakemattermost.SecondBotUserID)
	wantCallbacks(t, rootA, ma)
	wantCallbacks(t, rootB, mb)
	if rootA.ID == rootB.ID || len(e.roots(n)) != 2 {
		t.Fatalf("root messages %+v", e.roots(n))
	}

	// bob links through Connection A: the code comes from bot A.
	if l := e.linkMM(bob, e.mm, ma, fakemattermost.BotUserID); l["connection"].(map[string]any)["id"] != ma {
		t.Errorf("the link through mm-a %v", l)
	}
	// A press through Connection B: not linked in its Identity space; nothing changes.
	if res := e.mm.press(rootB.ID, "ack", fakemattermost.BobUserID); string(res.Answer) != pressAnswer(textNotLinkedMM) {
		t.Errorf("a press through mm-b by bob linked through mm-a only: %s", res.Answer)
	}
	if s := e.alertGroup(g)["status"]; s != "firing" {
		t.Errorf("status after the press through mm-b = %v", s)
	}
	for _, x := range e.timeline(g, "") {
		if x.Event == "acknowledged" {
			t.Errorf("acknowledged by %+v", x.Actor)
		}
	}

	// A callback to one Connection that carries the other Connection's post is refused like a bad signature and
	// changes nothing; Muster answers nothing, since the post is no Root message of that Connection's Destinations.
	body := func(post mmPost) string {
		b, _ := json.Marshal(map[string]any{"user_id": fakemattermost.BobUserID, "channel_id": fakemattermost.ChannelAlerts,
			"post_id": post.ID, "context": post.attachment(t).Actions[0].Integration.Context})
		return string(b)
	}
	e.callback(http.MethodPost, mb, body(rootA), "{}")
	e.callback(http.MethodPost, ma, body(rootB), "{}")
	// The log line may reach the harness after the callback's answer.
	for _, c := range []string{ma, mb} {
		eventually(t, "mattermost_press not_verified for "+c, func() bool {
			return e.pressLogged("mattermost_press", c, "not_verified")
		})
	}
	if s := e.alertGroup(g)["status"]; s != "firing" {
		t.Errorf("status after the refused callbacks = %v", s)
	}

	// bob links through Connection B: the code comes from bot B; two links, one per Identity space.
	if l := e.linkMM(bob, e.mm, mb, fakemattermost.SecondBotUserID); l["connection"].(map[string]any)["id"] != mb {
		t.Errorf("the link through mm-b %v", l)
	}
	if got := h.count(t, `SELECT count(*) FROM account_links a JOIN connections c ON c.id = a.connection_id
		WHERE a.org_id = 1 AND a.external_id = 'u-bob' AND a.identity_space = 'mattermost:' || c.id::text
		AND c.public_id IN ('`+ma+`', '`+mb+`')`); got != 2 {
		t.Errorf("%d links of u-bob, want one per Connection", got)
	}
	if res := e.mm.press(rootB.ID, "ack", fakemattermost.BobUserID); string(res.Answer) != "{}" {
		t.Errorf("a press through mm-b by bob linked through mm-b: %s", res.Answer)
	}
	eventually(t, "the acknowledgement by bob", func() bool {
		items := e.timeline(g, "")
		return len(items) > 0 && items[0].Event == "acknowledged"
	})
	if first := e.timeline(g, "")[0]; first.Actor.Transport != "mattermost" || first.Actor.Name != "bob" {
		t.Errorf("acknowledged by %+v", first.Actor)
	}
	wantPatchedBy(e.mm, rootA.ID, devmode.ConnectionBotToken, 1)
	wantPatchedBy(e.mm, rootB.ID, devmode.MattermostSecondBotToken, 1)

	// C-17.FR-2: the ack timeout notices of a second Alert Group, each under its own Root message by its own bot.
	g2 := e.fire("s2", "DiskFull", "t", true)
	n2 := e.number(g2)
	rootA2, rootB2 := rootBy(e.mm, n2, fakemattermost.BotUserID), rootBy(e.mm, n2, fakemattermost.SecondBotUserID)
	e.adv(e.due(g2).Min*60 + 30)
	for _, c := range []struct {
		dest, root, bot string
	}{{da, rootA2.ID, fakemattermost.BotUserID}, {db, rootB2.ID, fakemattermost.SecondBotUserID}} {
		p := e.post(e.waitReply(g2, c.dest, "ack_timeout"))
		if p.RootID != c.root || p.UserID != c.bot {
			t.Errorf("ack timeout notice of %s: root %s by %s, want root %s by %s", c.dest, p.RootID, p.UserID, c.root,
				c.bot)
		}
	}

	// C-17.FR-4: the Reminders of the acknowledged Alert Group, each under its own Root message by its own bot; bob
	// answers bot B's with "Still on it".
	e.adv(e.due(g).Min*60 + 30)
	var reminderB mmPost
	for _, c := range []struct {
		dest, root, bot string
	}{{da, rootA.ID, fakemattermost.BotUserID}, {db, rootB.ID, fakemattermost.SecondBotUserID}} {
		p := e.post(e.waitReply(g, c.dest, "reminder"))
		if p.RootID != c.root || p.UserID != c.bot || len(p.Attachments) != 1 || len(p.Attachments[0].Actions) != 2 {
			t.Errorf("reminder of %s: root %s by %s, want root %s by %s: %+v", c.dest, p.RootID, p.UserID, c.root, c.bot,
				p.Attachments)
		}
		if c.dest == db {
			reminderB = p
		}
	}
	wantCallbacks(t, reminderB, mb)
	if res := e.mm.press(reminderB.ID, "stillonit", fakemattermost.BobUserID); string(res.Answer) !=
		pressAnswer("Done: Still on it") {
		t.Errorf("still on it on bot B's reminder: %s", res.Answer)
	}
	eventually(t, "the answer of the reminder", func() bool { return e.lastTimer(g).Event == "reminder_answered" })
	if x := e.lastTimer(g); x.Actor.Transport != "mattermost" || x.Actor.Name != "bob" {
		t.Errorf("reminder answered by %+v", x.Actor)
	}

	// The Destination check sees each bot's own membership: bot B leaves the channel, bot A stays.
	e.fake(http.MethodDelete, e.fmm+"/channels/"+fakemattermost.ChannelAlerts+"/members/"+
		fakemattermost.SecondBotUserID, "")
	if c := e.api.json(http.MethodPost, "/api/v1/destinations/"+db+"/checks", "", http.StatusOK); c["ok"] != false {
		t.Errorf("check of mm-b-alerts without bot B = %v", c)
	}
	if c := e.api.json(http.MethodPost, "/api/v1/destinations/"+da+"/checks", "", http.StatusOK); c["ok"] != true {
		t.Errorf("check of mm-a-alerts = %v", c)
	}
}

// TestSeveralConnectionsTelegramSameChat is S-069 against `muster dev`: two Telegram Connections in one channel and its
// discussion group post two channel posts; both copies reach both Connections and are buffered for both, yet each
// Thread attaches to the copy of its own post, and new Alerts, ack timeout notices and Reminders reply to it from
// their own bot (C-14.FR-3, C-17.FR-2, FR-4); a person's comment changes neither Thread; a copy withheld from one bot
// leaves only its Thread unattached, and a deleted copy only the other's; the Identity space is all of Telegram: bob,
// linked through Connection A, presses through bot B and is answered by bot B (C-14.FR-4, C-18.FR-4, FR-8, AC-3), while
// a press that reaches the other bot is not verified; a link token works only at its own bot, and the deep link of
// Connection B names its bot (C-18.FR-2); Alice, linked through Connection A only, is named and mentioned by bot B
// (C-18.AC-7); the Destination check sees each bot's own rights.
func TestSeveralConnectionsTelegramSameChat(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newSeveral(t, h, r)
	tokA, tokB := devmode.TelegramConnectionBotToken, devmode.TelegramSecondBotToken
	tb := e.tgConn("tg-b", tokB)
	alice := e.person("Alice Smith", "alice", "responder")
	bob := e.person("bob", "bob", "responder")
	ta := e.tgDest("tg-a-alerts", e.conn, mentionSettings(nil))
	tdb := e.tgDest("tg-b-alerts", tb, mentionOf(alice.id))
	e.route("t", "t", []string{ta, tdb}, nil)

	// C-18.FR-2: the deep link of Connection B names its bot; a token started for Connection A links nothing at bot B
	// and links at bot A. Alice links through Connection A only.
	if link := e.startTG(bob, tb); !deepLinkB.MatchString(link) {
		t.Errorf("the deep link of tg-b %q", link)
	}
	tok := e.startTelegram(bob, e.conn)
	if got := e.privateTo(tokB, faketelegram.SecondBotID, 5001, "bob_t", "/start "+tok); got != textLinkInvalid {
		t.Errorf("a token of Connection A at bot B: %q", got)
	}
	e.wantRejection("wrong_connection", "telegram", "5001")
	if got := e.privateTo(tokA, faketelegram.BotID, 5001, "bob_t", "/start "+tok); got !=
		"Linked to Muster user bob (bob)" {
		t.Errorf("the token at its own bot: %q", got)
	}
	tok = e.startTelegram(alice, e.conn)
	if got := e.privateTo(tokA, faketelegram.BotID, 7001, "alice_t", "/start "+tok); got !=
		"Linked to Muster user Alice Smith (alice)" {
		t.Errorf("alice's link: %q", got)
	}

	// Two posts by two bots, two copies, four buffered copies, each Thread on the copy of its own post.
	g := e.fire("s1", "HostDown", "t", true)
	pa, pb := e.tgPostOf(g, ta), e.tgPostOf(g, tdb)
	if a, b := e.tgMessage(faketelegram.ChannelID, pa).From, e.tgMessage(faketelegram.ChannelID, pb).From; a == nil ||
		b == nil || a.ID != faketelegram.BotID || b.ID != faketelegram.SecondBotID {
		t.Fatalf("posts from %+v and %+v", a, b)
	}
	ca, cb := e.waitCopy(pa), e.waitCopy(pb)
	eventually(t, "both copies buffered for both Connections", func() bool {
		return h.count(t, `SELECT count(*) FROM telegram_post_copies WHERE org_id = 1`) == 4
	})
	attached := map[string]string{"tg-a-alerts": fmt.Sprintf("attached %d", ca),
		"tg-b-alerts": fmt.Sprintf("attached %d", cb)}
	eventually(t, "both threads attached", func() bool {
		a := e.anchors(g)
		return a["tg-a-alerts"] == attached["tg-a-alerts"] && a["tg-b-alerts"] == attached["tg-b-alerts"]
	})

	// C-18.AC-7: bot B mentions Alice, linked through Connection A only, by a tg://user link; bot A does not.
	mentioned := map[int64]bool{}
	for _, chat := range []int64{faketelegram.ChannelID, faketelegram.GroupID} {
		for _, m := range e.messages(chat) {
			if m.From != nil && strings.Contains(m.Text, `tg://user?id=7001">Alice Smith</a>`) {
				mentioned[m.From.ID] = true
			}
		}
	}
	if !mentioned[faketelegram.SecondBotID] || mentioned[faketelegram.BotID] {
		t.Errorf("mentions of alice by %v, want by bot B only", mentioned)
	}

	// A person's comment under post A reaches both bots and changes neither Thread.
	e.fake(http.MethodPost, e.ftg+"/comment", fmt.Sprintf(`{"post_id":%d,"from":{"id":7002},"text":"looking"}`, pa))
	e.settle()
	if a := e.anchors(g); a["tg-a-alerts"] != attached["tg-a-alerts"] || a["tg-b-alerts"] != attached["tg-b-alerts"] {
		t.Errorf("threads after a comment %v", a)
	}

	// New Alerts: each reply to the copy of its own post, from its own bot.
	e.newAlert("s1", "b", "t", g, 2)
	e.adv(61)
	e.wantThreadReply(e.waitReply(g, ta, "alerts_added"), faketelegram.BotID, ca)
	e.wantThreadReply(e.waitReply(g, tdb, "alerts_added"), faketelegram.SecondBotID, cb)

	// C-18.FR-8, AC-3: bob, linked through Connection A, presses Ack on bot B's post: bot B answers, and each post is
	// edited by its own bot.
	if got := e.pressAs(faketelegram.ChannelID, pb, "Ack", ""); got.Text != "Done: Acknowledge" {
		t.Errorf("ack through bot B: %+v", got)
	}
	if by := e.lastAnswerBy(); by != tokB {
		t.Errorf("the press on bot B's post was answered by another bot: %t", by != tokB)
	}
	eventually(t, "the acknowledgement by bob", func() bool {
		items := e.timeline(g, "")
		return len(items) > 0 && items[0].Event == "acknowledged"
	})
	if first := e.timeline(g, ""); first[0].Actor.Transport != "telegram" || first[0].Actor.Name != "bob" {
		t.Errorf("acknowledged by %+v", first[0].Actor)
	}
	for _, p := range []int64{pa, pb} {
		eventually(t, fmt.Sprintf("the acknowledged keyboard of post %d", p), func() bool {
			return slices.Contains(buttons(t, e.tgMessage(faketelegram.ChannelID, p).ReplyMarkup), "Unack")
		})
	}
	e.wantEditedBy(pa, faketelegram.BotID, 1)
	e.wantEditedBy(pb, faketelegram.SecondBotID, 1)

	// C-14.FR-4: a press on one bot's post that reaches the other bot is not verified and changes nothing.
	entries := len(e.timeline(g, ""))
	for _, c := range []struct {
		post  int64
		token string
	}{{pa, tokB}, {pb, tokA}} {
		if got := e.pressAs(faketelegram.ChannelID, c.post, "Unack", c.token); got.Text != textNotVerified {
			t.Errorf("a press on post %d at the other bot: %+v", c.post, got)
		}
	}
	if s, _ := e.ownerOf(g); s != "acknowledged" || len(e.timeline(g, "")) != entries {
		t.Errorf("the presses at the other bot changed %s: %s", g, s)
	}

	// C-18.AC-7: bot B's Root message of an Alert Group Alice acknowledged names her Telegram username.
	g2 := e.fire("s2", "AliceTest", "t", true)
	pb2 := e.tgPostOf(g2, tdb)
	alice.pat.json(http.MethodPost, "/api/v1/alert-groups/"+g2+"/acknowledge", "", http.StatusOK)
	e.settle()
	footer := regexp.MustCompile(`Acknowledged by alice_t($|[^A-Za-z0-9_])`)
	eventually(t, "the footer of bot B's post", func() bool {
		return footer.MatchString(e.tgMessage(faketelegram.ChannelID, pb2).Text)
	})

	// C-17.FR-2: the ack timeout notices of a new Alert Group, each a reply to its own copy from its own bot.
	g3 := e.fire("s3", "DiskFull", "t", true)
	pa3, pb3 := e.tgPostOf(g3, ta), e.tgPostOf(g3, tdb)
	ca3, cb3 := e.waitCopy(pa3), e.waitCopy(pb3)
	e.adv(e.due(g3).Min*60 + 30)
	e.wantThreadReply(e.waitReply(g3, ta, "ack_timeout"), faketelegram.BotID, ca3)
	e.wantThreadReply(e.waitReply(g3, tdb, "ack_timeout"), faketelegram.SecondBotID, cb3)

	// C-17.FR-4: the Reminders of bob's Alert Group, each a reply to its own copy from its own bot; bob answers bot
	// B's with "Still on it" and bot B answers; a press on bot A's Reminder that reaches bot B is not verified.
	e.adv(e.due(g).Min*60 + 30)
	e.endPollsAfterClockMove(tokA, tokB)
	ra, rb := e.waitReply(g, ta, "reminder"), e.waitReply(g, tdb, "reminder")
	e.wantThreadReply(ra, faketelegram.BotID, ca)
	e.wantThreadReply(rb, faketelegram.SecondBotID, cb)
	rbID, _ := strconv.ParseInt(rb, 10, 64)
	raID, _ := strconv.ParseInt(ra, 10, 64)
	if rows := keyboardRows(t, e.tgMessage(faketelegram.GroupID, rbID).ReplyMarkup); len(rows) != 1 ||
		strings.Join(rows[0], ",") != "Still on it,Unack" {
		t.Errorf("keyboard of bot B's reminder %v", rows)
	}
	if got := e.pressAs(faketelegram.GroupID, raID, "Still on it", tokB); got.Text != textNotVerified {
		t.Errorf("a press on bot A's reminder at bot B: %+v", got)
	}
	if got := e.pressAs(faketelegram.GroupID, rbID, "Still on it", ""); got.Text != "Done: Still on it" {
		t.Errorf("still on it on bot B's reminder: %+v", got)
	}
	if by := e.lastAnswerBy(); by != tokB {
		t.Errorf("the press on bot B's reminder was answered by another bot: %t", by != tokB)
	}
	eventually(t, "the answer of the reminder", func() bool {
		x := e.lastTimer(g)
		return x.Event == "reminder_answered" && x.Actor.Transport == "telegram"
	})
	if x := e.lastTimer(g); x.Actor.Name != "bob" {
		t.Errorf("reminder answered by %+v", x.Actor)
	}

	// C-14.FR-3: a copy withheld from bot B leaves only tg-b-alerts' Thread "Thread not attached" until a person
	// comments under its post.
	e.fake(http.MethodPut, e.ftg+"/config", `{"withhold_copies_from":[`+strconv.Itoa(faketelegram.SecondBotID)+`]}`)
	g4 := e.fire("s4", "QueueFull", "t", true)
	pa4, pb4 := e.tgPostOf(g4, ta), e.tgPostOf(g4, tdb)
	ca4, cb4 := e.waitCopy(pa4), e.waitCopy(pb4)
	e.newAlert("s4", "b", "t", g4, 2)
	e.adv(61, 61)
	eventually(t, "the unattached thread of tg-b-alerts", func() bool {
		d := e.deliveries(g4)
		return d["tg-b-alerts"].ThreadNotAttached && !d["tg-a-alerts"].ThreadNotAttached
	})
	e.wantThreadReply(e.waitReply(g4, ta, "alerts_added"), faketelegram.BotID, ca4)
	e.fake(http.MethodPost, e.ftg+"/comment", fmt.Sprintf(`{"post_id":%d,"from":{"id":7002},"text":"looking"}`, pb4))
	eventually(t, "the comment to attach tg-b-alerts' thread", func() bool {
		return e.anchors(g4)["tg-b-alerts"] == fmt.Sprintf("attached %d", cb4)
	})
	if d := e.deliveries(g4); d["tg-b-alerts"].ThreadNotAttached || d["tg-a-alerts"].ThreadNotAttached {
		t.Errorf("deliveries after the comment %+v", d)
	}
	e.newAlert("s4", "c", "t", g4, 2)
	e.adv(61)
	eventually(t, "the attached reply of tg-b-alerts", func() bool { return len(e.replyIDs(g4, tdb, "alerts_added")) == 2 })
	ids := e.replyIDs(g4, tdb, "alerts_added")
	e.wantThreadReply(ids[1], faketelegram.SecondBotID, cb4)
	e.fake(http.MethodPut, e.ftg+"/config", `{"withhold_copies_from":[]}`)

	// C-14.FR-3: the copy of post A is deleted; tg-a-alerts loses its Thread, tg-b-alerts keeps its own.
	e.fake(http.MethodDelete, fmt.Sprintf("%s/chats/%d/messages/%d", e.ftg, faketelegram.GroupID, ca3), "")
	repliesB := len(e.replyIDs(g3, tdb, "alerts_added"))
	e.newAlert("s3", "b", "t", g3, 2)
	e.adv(61)
	eventually(t, "the lost thread of tg-a-alerts", func() bool { return e.deliveries(g3)["tg-a-alerts"].ThreadNotAttached })
	if d := e.deliveries(g3); d["tg-b-alerts"].ThreadNotAttached {
		t.Errorf("tg-b-alerts lost its thread with the copy of post A: %+v", d)
	}
	if got := deliveryEvents(e.timeline(g3, "&kind=delivery")); !slices.Contains(got, "thread_not_attached") {
		t.Errorf("delivery events %v", got)
	}
	eventually(t, "the reply of tg-b-alerts", func() bool { return len(e.replyIDs(g3, tdb, "alerts_added")) > repliesB })
	ids = e.replyIDs(g3, tdb, "alerts_added")
	e.wantThreadReply(ids[len(ids)-1], faketelegram.SecondBotID, cb3)
	if e.health(ta).State != "healthy" || e.health(tdb).State != "healthy" {
		t.Errorf("health %+v %+v", e.health(ta), e.health(tdb))
	}

	// The Destination check sees each bot's own rights: bot B is only a member of the discussion group.
	e.fake(http.MethodPut, fmt.Sprintf("%s/chats/%d/members/%d", e.ftg, faketelegram.GroupID, faketelegram.SecondBotID),
		`{"status":"member"}`)
	check := e.api.json(http.MethodPost, "/api/v1/destinations/"+tdb+"/checks", "", http.StatusOK)
	failed := ""
	for _, x := range check["checks"].([]any) {
		if c := x.(map[string]any); c["ok"] == false {
			failed = fmt.Sprint(c["name"], " ", c["code"], ": ", c["message"])
		}
	}
	if check["ok"] != false || !strings.HasPrefix(failed, "bot_rights_group bot_not_group_admin: The bot is not an "+
		"admin of the discussion group "+faketelegram.GroupTitle) {
		t.Errorf("check of tg-b-alerts with bot B a member = %v", check)
	}
	if c := e.api.json(http.MethodPost, "/api/v1/destinations/"+ta+"/checks", "", http.StatusOK); c["ok"] != true {
		t.Errorf("check of tg-a-alerts = %v", c)
	}
}

// TestSeveralConnectionsTelegramWithoutDiscussionGroup is S-069, C-14.FR-2 and C-14.AC-13 through a second Telegram
// Connection: createDestination on a channel without a discussion group is refused with the Destination check's
// message, and bot B posts nothing there.
func TestSeveralConnectionsTelegramWithoutDiscussionGroup(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := newSeveral(t, h, r)
	tb := e.tgConn("tg-b", devmode.TelegramSecondBotToken)
	a := e.tgCreate("tg-b-nocomments", tb, "@"+faketelegram.NoCommentsName, mentionSettings(nil))
	if fieldError(t, a) != "destination_check_failed /channel_id" || problemDetail(t, a) != telegram.MessageNoComments ||
		!strings.Contains(string(a.body), `"check_code":"comments_disabled"`) {
		t.Errorf("createDestination on @no_comments through tg-b = %d %s", a.status, a.body)
	}
	if telegram.MessageNoComments != "Comments are not enabled for this channel. Enable comments in the channel "+
		"settings in Telegram; this creates its discussion group." {
		t.Errorf("message %q", telegram.MessageNoComments)
	}
	if n := len(e.messages(faketelegram.NoCommentsID)); n != 0 {
		t.Errorf("%d messages in @no_comments", n)
	}
	if n := h.count(t, `SELECT count(*) FROM destinations WHERE org_id = 1 AND name = 'tg-b-nocomments'`); n != 0 {
		t.Errorf("%d destinations saved", n)
	}
	if !slices.ContainsFunc(e.requests(), func(q fakeserver.Request) bool {
		return strings.HasPrefix(q.Path, "/bot"+devmode.TelegramSecondBotToken+"/getChat")
	}) {
		t.Error("the check did not run through bot B")
	}
}
