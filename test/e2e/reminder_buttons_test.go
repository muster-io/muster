// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/leader"
	"github.com/muster-io/muster/internal/logging"
)

const (
	textNotVerified = "This button could not be verified; nothing was changed."
	textExpired     = "This button has expired; use the buttons on the Alert Group's message"
)

// reminderEnv is the timersEnv of S-049 with the Route "t" to the Mattermost Destination "alerts" (channel ch-alerts)
// and the Telegram Destination "alerts" (discussion group GroupID), and bob's Account links set up directly: u-bob in
// Mattermost, as in S-061, and 5001 in Telegram, as in S-067.
func reminderEnv(t *testing.T, h *Harness, r *Replica) *timersEnv {
	t.Helper()
	e := &timersEnv{tgd: newTGD(t, h, r)}
	_, e.bob = e.responder("bob")
	e.mmConn = e.connection(50)
	e.mmDest = e.destination("alerts", e.mmConn, fakemattermost.ChannelAlerts, nil, 50)
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
	e.route("t", "t", []string{e.mmDest, e.tgDest}, nil)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		created_at) SELECT 1, 'AK0000000000B1', u.id, 'mattermost', c.id, 'u-bob', 'bob', now() FROM users u,
		connections c WHERE u.login = 'bob' AND c.name = 'mm'`)
	h.exec(t, `INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username,
		created_at) SELECT 1, 'AK0000000000T1', u.id, 'telegram', c.id, '5001', 'bob_tg', now() FROM users u,
		connections c WHERE u.login = 'bob' AND c.name = '`+devmode.TelegramConnectionName+`'`)
	return e
}

// reminderPost is the last Reminder in the Thread of the live Root message of the Alert Group in the fake Mattermost:
// the reply with two buttons, and the Root message.
func (e *timersEnv) reminderPost(id string) (mmPost, mmPost) {
	e.t.Helper()
	root := e.root(e.number(id))
	var reply mmPost
	for _, p := range e.replies(root.ID) {
		if len(p.Attachments) == 1 && len(p.Attachments[0].Actions) == 2 {
			reply = p
		}
	}
	if reply.ID == "" {
		e.t.Fatalf("no reminder with buttons in the thread of %s", root.ID)
	}
	return reply, root
}

// tgReminder is the message id of the last Reminder of the Alert Group in the discussion group.
func (e *timersEnv) tgReminder(id string) int64 {
	e.t.Helper()
	return e.h.count(e.t, `SELECT t.message_id::bigint FROM thread_replies t
		JOIN destinations d ON d.id = t.destination_id JOIN alert_groups g ON g.id = t.alert_group_id
		WHERE d.type = 'telegram' AND t.event = 'reminder' AND t.state = 'sent' AND g.public_id = '`+id+`'
		ORDER BY t.id DESC LIMIT 1`)
}

// tgMessage is the message id of the discussion group in the fake Telegram server.
func (e *timersEnv) tgMessage(id int64) faketelegram.Message {
	e.t.Helper()
	for _, m := range e.messages(faketelegram.GroupID) {
		if m.ID == id {
			return m
		}
	}
	e.t.Fatalf("no message %d in the discussion group", id)
	return faketelegram.Message{}
}

// keyboardRows are the labels of a keyboard, row by row.
func keyboardRows(t *testing.T, raw json.RawMessage) [][]string {
	t.Helper()
	var kb struct {
		InlineKeyboard [][]struct {
			Text         string `json:"text"`
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal(raw, &kb); err != nil {
		t.Fatalf("keyboard %s: %v", raw, err)
	}
	out := [][]string{}
	for _, row := range kb.InlineKeyboard {
		labels := []string{}
		for _, b := range row {
			labels = append(labels, b.Text)
		}
		out = append(out, labels)
	}
	return out
}

// callbackData is the data of the button label of the message id of chat.
func (e *tgd) callbackData(chat, id int64, label string) string {
	e.t.Helper()
	for _, m := range e.messages(chat) {
		if m.ID != id {
			continue
		}
		var kb struct {
			InlineKeyboard [][]struct {
				Text         string `json:"text"`
				CallbackData string `json:"callback_data"`
			} `json:"inline_keyboard"`
		}
		_ = json.Unmarshal(m.ReplyMarkup, &kb)
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if b.Text == label {
					return b.CallbackData
				}
			}
		}
	}
	e.t.Fatalf("no button %s on message %d of %d", label, id, chat)
	return ""
}

// pollAfterClockMove ends the long poll of the development Telegram Connection that was held while the development
// clock moved, with a message of a chat type that has no handler, and waits until it is dropped. The gap of the next press
// is then measured from the end of that poll, after the move: a poll that started before the move and brought the
// press would measure the move as a gap longer than telegram.press_max_age and drop the press (C-14.FR-4).
func (e *tgd) pollAfterClockMove() {
	e.t.Helper()
	dropped := func() int { return strings.Count(e.r.Output(), `"event":"telegram_update_dropped"`) }
	before := dropped()
	e.fake(http.MethodPost, e.ftg+"/updates", `{"token":"`+devmode.TelegramConnectionBotToken+
		`","update":{"message":{"message_id":1,"date":0,"chat":{"id":1,"type":"none"}}}}`)
	eventually(e.t, "the update that ends the long poll", func() bool { return dropped() > before })
}

// pressIn presses the button label of the message id of chat as bob's Telegram account 5001, with extra fields of the
// press such as `,"data_from":12`, and returns the answer to it once one is recorded.
func (e *tgd) pressIn(chat, id int64, label, extra string) faketelegram.Answer {
	e.t.Helper()
	before := len(e.answers())
	e.fake(http.MethodPost, e.ftg+"/press", fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":5001,"username":"u5001"},
		"button":%q%s}`, chat, id, label, extra))
	eventually(e.t, "the answer to "+label, func() bool { return len(e.answers()) > before })
	return e.answers()[before]
}

// lastTimer is the newest Timeline entry of the kind timers of the Alert Group.
func (e *timersEnv) lastTimer(id string) timelineEntry {
	e.t.Helper()
	items := e.timeline(id, "&kind=timers")
	if len(items) == 0 {
		e.t.Fatalf("no timers entries of %s", id)
	}
	return items[0]
}

// TestReminderButtons is the Verification of S-068 against `muster dev`: the buttons "Still on it" and "Unack" on a
// Reminder in Mattermost and Telegram with the reply's message id and key stored (C-17.FR-4, C-17.AC-7); an unlinked
// press and bob's "Still on it" answered in the Thread of the Root message with a Member bot and with an admin bot
// (C-17.AC-7, C-13.FR-4, D284), "Still on it" not moving the next Reminder (C-17.FR-10); a callback bound to another
// post refused like a bad signature (C-13.FR-4); in Telegram a press whose data come from another Alert Group's
// Reminder refused, "Still on it" answered "Done: Still on it" with the Transport telegram and Unack answered "Done:
// Unacknowledge" (C-14.FR-4); and Reminder messages never edited.
func TestReminderButtons(t *testing.T) {
	h := Start(t, DevProcess)
	r := h.Replicas[0]
	e := reminderEnv(t, h, r)
	g := e.fire("r1", "HostDown", "t", true)
	g2 := e.fire("r2", "DiskFull", "t", true)
	e.ack(e.bob, g)
	e.ack(e.bob, g2)
	e.adv(14400)
	e.pollAfterClockMove()

	rp, root := e.reminderPost(g)
	rp2, _ := e.reminderPost(g2)
	t.Run("the buttons in mattermost", func(t *testing.T) {
		e := e.on(t)
		a := rp.attachment(t)
		if len(a.Actions) != 2 || a.Actions[0].ID != "stillonit" || a.Actions[0].Name != "Still on it" ||
			a.Actions[1].ID != "unack" || a.Actions[1].Name != "Unack" {
			t.Errorf("actions %+v", a.Actions)
		}
		if rp.RootID != root.ID || !strings.Contains(rp.Message, "@bob") ||
			!strings.Contains(a.Text, "Reminder: bob, are you still on it?") ||
			a.Actions[0].Integration.URL != root.attachment(t).Actions[0].Integration.URL {
			t.Errorf("reminder %+v", rp)
		}
		if n := e.h.count(t, `SELECT count(*) FROM thread_replies WHERE event = 'reminder' AND message_id = '`+rp.ID+
			`' AND button_key_id IS NOT NULL`); n != 1 {
			t.Errorf("%d stored rows of the reminder %s", n, rp.ID)
		}
		if n := e.h.count(t, `SELECT count(*) FROM thread_replies WHERE event = 'reminder' AND state = 'sent'
			AND (message_id IS NULL OR button_key_id IS NULL)`); n != 0 {
			t.Errorf("%d sent reminders without their message id or key", n)
		}
	})

	t.Run("presses in mattermost", func(t *testing.T) {
		e := e.on(t)
		notLinked := "Your Mattermost account is not linked to Muster. Link it in your profile: " + devmode.PublicURL +
			"/profile"
		if res := e.mm.press(rp.ID, "stillonit", fakemattermost.AliceUserID); string(res.Answer) != pressAnswer(notLinked) {
			t.Errorf("unlinked press %s", res.Answer)
		}
		eph := e.ephemeral()
		if x := eph[len(eph)-1]; x.UserID != fakemattermost.AliceUserID || x.From != "System" || x.RootID != root.ID ||
			x.Message != notLinked {
			t.Errorf("unlinked answer %+v", x)
		}
		before := e.due(g)
		if res := e.mm.press(rp.ID, "stillonit", fakemattermost.BobUserID); string(res.Answer) !=
			pressAnswer("Done: Still on it") {
			t.Errorf("still on it %s", res.Answer)
		}
		eph = e.ephemeral()
		if x := eph[len(eph)-1]; x.Message != "Done: Still on it" || x.RootID != root.ID || x.UserID !=
			fakemattermost.BobUserID {
			t.Errorf("still on it answer %+v", x)
		}
		eventually(t, "the answer of the reminder", func() bool { return e.lastTimer(g).Event == "reminder_answered" })
		if x := e.lastTimer(g); x.Actor.Transport != "mattermost" || x.Actor.Name != "bob" {
			t.Errorf("answered by %+v", x.Actor)
		}
		e.wantDue(g, before)

		// An admin bot: the same answer is an ephemeral post by the bot in the Thread of the Root message.
		e.fake(http.MethodPut, e.fmm+"/config", `{"bot_system_admin":true}`)
		if res := e.mm.press(rp.ID, "stillonit", fakemattermost.AliceUserID); string(res.Answer) != "{}" {
			t.Errorf("unlinked press with an admin bot %s", res.Answer)
		}
		eph = e.ephemeral()
		if x := eph[len(eph)-1]; x.From != fakemattermost.BotUsername || x.RootID != root.ID ||
			x.ShownIn != fakemattermost.ShownInThread || x.Message != notLinked {
			t.Errorf("unlinked answer with an admin bot %+v", x)
		}
		e.fake(http.MethodPut, e.fmm+"/config", `{"bot_system_admin":false}`)

		// A callback with the Reminder's context on the Root message is refused like a bad signature; on another
		// Alert Group's Reminder it gets the empty answer, since that post is no Root message Muster answers on.
		ctx, _ := json.Marshal(rp.attachment(t).Actions[0].Integration.Context)
		body := func(post string) string {
			return fmt.Sprintf(`{"user_id":"u-bob","channel_id":%q,"post_id":%q,"context":%s}`,
				fakemattermost.ChannelAlerts, post, ctx)
		}
		e.callback(http.MethodPost, e.mmConn, body(root.ID), pressAnswer(textNotVerified))
		e.callback(http.MethodPost, e.mmConn, body(rp2.ID), "{}")
		if s, _ := e.ownerOf(g2); s != "acknowledged" {
			t.Errorf("g2 is %s", s)
		}
	})

	t.Run("presses in telegram", func(t *testing.T) {
		e := e.on(t)
		tr, tr2 := e.tgReminder(g), e.tgReminder(g2)
		m := e.tgMessage(tr)
		if rows := keyboardRows(t, m.ReplyMarkup); len(rows) != 1 || strings.Join(rows[0], ",") != "Still on it,Unack" {
			t.Errorf("keyboard %v", rows)
		}
		if !strings.Contains(m.Text, "Reminder: bob, are you still on it?") {
			t.Errorf("reminder text %q", m.Text)
		}
		if got := e.pressIn(faketelegram.GroupID, tr, "Still on it", fmt.Sprintf(`,"data_from":%d`, tr2)); got.Text !=
			textNotVerified {
			t.Errorf("data from another alert group's reminder: %+v", got)
		}
		if got := e.pressIn(faketelegram.GroupID, tr2, "Still on it", ""); got.Text != "Done: Still on it" {
			t.Errorf("still on it in telegram: %+v", got)
		}
		eventually(t, "the answer through telegram", func() bool {
			x := e.lastTimer(g2)
			return x.Event == "reminder_answered" && x.Actor.Transport == "telegram"
		})
		if got := e.pressIn(faketelegram.GroupID, tr, "Unack", ""); got.Text != "Done: Unacknowledge" {
			t.Errorf("unack in telegram: %+v", got)
		}
		eventually(t, "the unacknowledgement", func() bool { s, _ := e.ownerOf(g); return s == "firing" })
		if s, _ := e.ownerOf(g2); s != "acknowledged" {
			t.Errorf("g2 is %s", s)
		}
	})

	t.Run("reminders are never edited", func(t *testing.T) {
		e := e.on(t)
		e.settle()
		for _, p := range []mmPost{rp, rp2} {
			if calls := e.calls(http.MethodPut, "/api/v4/posts/"+p.ID+"/patch"); len(calls) != 0 {
				t.Errorf("the reminder %s was edited: %+v", p.ID, calls)
			}
		}
		for _, id := range []int64{e.tgReminder(g), e.tgReminder(g2)} {
			if m := e.tgMessage(id); len(m.Edits) != 0 {
				t.Errorf("the telegram reminder %d was edited: %+v", id, m.Edits)
			}
		}
	})
}

// TestReminderButtonsExpired is C-17.FR-6 against a replica restarted with a Keyring that no longer holds the key of a
// Reminder sent before: a press on that Reminder is answered "This button has expired; use the buttons on the Alert
// Group's message" in both messengers and changes nothing, while a press on a Root message signed with the same key is
// answered "This button could not be verified; nothing was changed."; the next Reminder is signed with the new key.
func TestReminderButtonsExpired(t *testing.T) {
	h := Start(t, FakesInProcess)
	r := h.StartReplica(t, ReplicaOptions{ListenApp: ":8080", ListenIngest: ":8081", ListenInternal: ":8082"})
	e := reminderEnv(t, h, r)
	g := e.fire("x1", "HostDown", "t", true)
	e.ack(e.bob, g)
	e.adv(14400)
	rp, root := e.reminderPost(g)
	rootCtx, _ := json.Marshal(root.attachment(t).Actions[0].Integration.Context)
	tr := e.tgReminder(g)
	post := e.h.count(t, `SELECT d.message_id::bigint FROM deliveries d JOIN destinations ds ON ds.id = d.destination_id
		JOIN alert_groups g ON g.id = d.alert_group_id WHERE ds.type = 'telegram' AND g.public_id = '`+g+`'`)
	rootData := e.callbackData(faketelegram.ChannelID, post, "Unack")
	answered := len(e.timeline(g, "&kind=timers"))

	// The development clock moved by hours since the last alive mark: a restart before the Leader marks it again looks
	// like an outage that long, after which the first press is dropped (C-14.AC-4). The restart waits for the mark.
	waitAliveMark(t, h, readClock(t, r).Now)
	r.Stop(t)
	k2 := rotateKeyring(t, h)
	r.env = append(r.env, "MUSTER_SECRET_KEYS="+k2)
	r.Start(t)

	if res := e.mm.press(rp.ID, "stillonit", fakemattermost.BobUserID); string(res.Answer) != pressAnswer(textExpired) {
		t.Errorf("an expired reminder in mattermost: %s", res.Answer)
	}
	eph := e.ephemeral()
	if x := eph[len(eph)-1]; x.Message != textExpired || x.RootID != root.ID {
		t.Errorf("the expiry answer %+v", x)
	}
	expired := false
	for line := range strings.Lines(r.Output()) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["event"] == "mattermost_press" && m["outcome"] == "expired" {
			expired = true
		}
	}
	if !expired {
		t.Error("no mattermost_press with the outcome expired")
	}
	e.callback(http.MethodPost, e.mmConn, fmt.Sprintf(`{"user_id":"u-bob","channel_id":%q,"post_id":%q,"context":%s}`,
		fakemattermost.ChannelAlerts, root.ID, rootCtx), pressAnswer(textNotVerified))
	if got := e.pressIn(faketelegram.GroupID, tr, "Still on it", ""); got.Text != textExpired {
		t.Errorf("an expired reminder in telegram: %+v", got)
	}
	if got := e.pressIn(faketelegram.ChannelID, post, "x", fmt.Sprintf(`,"data":%q`, rootData)); got.Text !=
		textNotVerified {
		t.Errorf("an expired root message in telegram: %+v", got)
	}
	if strings.Contains(r.Output(), `"event":"telegram_press_dropped"`) {
		t.Error("a press after the restart was dropped")
	}
	if s, _ := e.ownerOf(g); s != "acknowledged" || len(e.timeline(g, "&kind=timers")) != answered {
		t.Errorf("an expired press changed %s: %s, %d timers entries", g, s, len(e.timeline(g, "&kind=timers")))
	}

	// The next Reminder is signed with the new key; Min is rounded, so half a minute more reaches it.
	e.adv(e.due(g).Min*60 + 30)
	material, _ := base64.StdEncoding.DecodeString(k2)
	if n := e.h.count(t, `SELECT count(*) FROM thread_replies WHERE event = 'reminder' AND state = 'sent'
		AND button_key_id = '`+keyring.KeyID(material)+`'`); n != 2 {
		t.Errorf("%d reminders signed with the new key", n)
	}
}

// waitAliveMark waits until the Leader's alive mark has reached the business time at; the Leader writes it every
// leader.alive_mark_interval of real time.
func waitAliveMark(t *testing.T, h *Harness, at time.Time) {
	t.Helper()
	q := `SELECT count(*) FROM runtime_state WHERE alive_at >= '` + at.UTC().Format(time.RFC3339Nano) + `'`
	deadline := time.Now().Add(2 * leader.AliveMarkInterval)
	for h.count(t, q) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the alive mark did not reach %s", at)
		}
		time.Sleep(pollInterval)
	}
}

// rotateKeyring replaces the Keyring of the stopped replicas of h with a new key: in one transaction, every encrypted
// value is decrypted with the development key and encrypted with the new one, and the key canary is written again for
// it. It returns the new key as MUSTER_SECRET_KEYS takes it. The development key leaves the Keyring, as after the
// rotation of S-055.
func rotateKeyring(t *testing.T, h *Harness) string {
	t.Helper()
	ctx := t.Context()
	material := make([]byte, keyring.KeySize)
	_, _ = rand.Read(material)
	k2 := base64.StdEncoding.EncodeToString(material)
	dev, err := base64.StdEncoding.DecodeString(devmode.DevelopmentKey)
	if err != nil {
		t.Fatal(err)
	}
	oldRing, err := keyring.New([][]byte{dev}, true)
	if err != nil {
		t.Fatal(err)
	}
	newRing, err := keyring.New([][]byte{append([]byte{}, material...)}, false)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM keyring_state`); err != nil {
		t.Fatal(err)
	}
	st, err := newRing.Establish(ctx, keyring.NewStore(tx), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := newRing.Open(ctx, logging.New(io.Discard, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `SELECT field, row_key, key_id FROM encrypted_values`)
	if err != nil {
		t.Fatal(err)
	}
	type value struct{ field, row, key string }
	var values []value
	for rows.Next() {
		var v value
		if err := rows.Scan(&v.field, &v.row, &v.key); err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// The row of each table that holds encrypted values, by the row_key of encrypted_values.
	keys := map[string]string{"organizations": "id", "oidc_settings": "org_id", "users": "id", "connections": "id",
		"destinations": "id"}
	for _, v := range values {
		table, column, _ := strings.Cut(v.field, ".")
		key, ok := keys[table]
		if !ok {
			t.Fatalf("the encrypted value %s is not re-encrypted by the test", v.field)
		}
		var ciphertext []byte
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s_ciphertext FROM %s WHERE %s::text = $1`, column, table, key),
			v.row).Scan(&ciphertext); err != nil {
			t.Fatalf("%s of %s: %v", v.field, v.row, err)
		}
		plain, err := oldRing.Decrypt(v.field, v.key, ciphertext)
		if err != nil {
			t.Fatalf("decrypt %s of %s: %v", v.field, v.row, err)
		}
		sealed, kid, err := newRing.Encrypt(v.field, plain)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s_ciphertext = $1, %s_key_id = $2 WHERE %s::text = $3`,
			table, column, column, key), sealed, kid, v.row); err != nil {
			t.Fatalf("re-encrypt %s of %s: %v", v.field, v.row, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return k2
}
