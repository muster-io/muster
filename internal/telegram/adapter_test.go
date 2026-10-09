// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

const groupPage = "http://localhost:8080/alert-groups/AG0000000000A1"

// targets are the Targets of the tests by Destination id; err answers every lookup when set, and awaitErr every wait
// for the updates of a Connection, which awaited records.
type targets struct {
	byID     map[int64]Target
	err      error
	awaitErr error
	awaited  []int64
}

func (ts *targets) AwaitUpdates(_ context.Context, id int64) error {
	ts.awaited = append(ts.awaited, id)
	return ts.awaitErr
}

func (ts *targets) TelegramTarget(_ context.Context, id int64) (Target, error) {
	if ts.err != nil {
		return Target{}, ts.err
	}
	t, ok := ts.byID[id]
	if !ok {
		return Target{}, ErrNoTarget
	}
	return t, nil
}

type adapterEnv struct {
	fake    *faketelegram.Fake
	adapter *Adapter
	targets *targets
	clock   *clock.Manual
}

// newAdapter is the adapter over the fake server: Destination 1 on @muster_alerts with its ids known, Destination 2
// on the same channel through the same Connection as entered only, Destination 3 on @no_comments without a group.
func newAdapter(t *testing.T) *adapterEnv {
	t.Helper()
	f := startFake(t)
	c := newClient(t, Settings{BaseURL: f.URL(), Token: testToken})
	ts := &targets{byID: map[int64]Target{
		1: {Client: c, ConnectionID: 5, Channel: "@muster_alerts", ChannelID: faketelegram.ChannelID,
			GroupID: faketelegram.GroupID},
		2: {Client: c, ConnectionID: 5, Channel: "@muster_alerts"},
		3: {Client: c, ConnectionID: 5, Channel: "@no_comments"},
	}}
	m := clock.NewManual(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	return &adapterEnv{fake: f, targets: ts, clock: m, adapter: &Adapter{Targets: ts, Clock: m}}
}

func call(destination int64) delivery.Call {
	return delivery.Call{Class: outbound.ClassDelivery, Destination: delivery.Destination{ID: destination,
		PublicID: "DS0000000000D1", Type: delivery.TypeTelegram}}
}

func loud(ts ...mentions.Target) delivery.Call {
	c := call(1)
	c.Loudness, c.Targets = groups.Loud, ts
	return c
}

// root is a Root message in a status, with a label that tries to mention the channel and write HTML, two Alerts, one
// of them resolved, and its buttons for three Snooze durations.
func root(status string) messages.Message {
	m := messages.Message{Kind: messages.KindRoot, Language: "en", TimeZone: "UTC", Colour: status,
		Heading:      &messages.Heading{Number: 7, Title: "CertExpiry", URL: groupPage},
		Environment:  "prod · started 2026-10-09 12:00 UTC",
		GroupLabels:  []messages.Label{{Name: "alertname", Value: "CertExpiry"}},
		CommonLabels: []messages.Label{{Name: "note", Value: messages.Value("@channel <b>x</b>")}},
		Summary:      "Certificates expire",
		Alerts: &messages.AlertList{Lines: []messages.AlertLine{{Text: "a.example.org"},
			{Text: "b.example.org", Resolved: true}}},
		Links:  []messages.Link{{Text: "Open in Muster", URL: groupPage}},
		Footer: "Acknowledged by bob", Footers: map[string]string{"telegram": "Acknowledged by @bob_tg"}}
	labels := map[string]string{buttons.CommandAcknowledge: "Ack", buttons.CommandUnacknowledge: "Unack",
		buttons.CommandResolve: "Resolve", buttons.CommandUnsnooze: "Unsnooze"}
	for _, b := range buttons.ForStatus(status, 3) {
		label := labels[b.Command]
		if b.Command == buttons.CommandSnooze {
			label = []string{"Snooze 1 h", "Snooze 4 h", "Snooze 24 h"}[b.Argument]
		}
		m.Buttons = append(m.Buttons, messages.Button{Command: b.Command, Argument: b.Argument, Label: label,
			ActionID: "a-" + b.Command + fmt.Sprint(b.Argument), KeyID: "k-00112233"})
	}
	return m
}

// keyboardOf is the labels of a recorded keyboard, row by row.
func keyboardOf(t *testing.T, raw json.RawMessage) [][]string {
	t.Helper()
	if raw == nil {
		return nil
	}
	var kb struct {
		InlineKeyboard [][]struct {
			Text string `json:"text"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal(raw, &kb); err != nil {
		t.Fatal(err)
	}
	out := [][]string{}
	for _, row := range kb.InlineKeyboard {
		var labels []string
		for _, b := range row {
			labels = append(labels, b.Text)
		}
		out = append(out, labels)
	}
	return out
}

// only is the one message of the channel.
func only(t *testing.T, f *faketelegram.Fake) faketelegram.Message {
	t.Helper()
	msgs := f.Messages(faketelegram.ChannelID)
	if len(msgs) != 1 {
		t.Fatalf("messages of the channel = %+v", msgs)
	}
	return msgs[0]
}

// TestPublishRootMessage is C-14.AC-17, C-14.FR-16, C-12.FR-1 and C-14.AC-8: a channel post in HTML with its label
// sections in an expandable blockquote, its Alerts as list lines, no table, alert data escaped and neutralized, the
// keyboard of its status, and a Loud post without disable_notification.
func TestPublishRootMessage(t *testing.T) {
	e := newAdapter(t)
	o := e.adapter.Publish(t.Context(), loud(), root(messages.ColourFiring))
	if o.Kind != delivery.OutcomeOK || o.MessageID != "1" || o.MessageURL != "https://t.me/muster_alerts/1" {
		t.Fatalf("publish = %+v", o)
	}
	m := only(t, e.fake)
	want := "🔴 <b><a href=\"" + groupPage + "\">#7 CertExpiry</a></b>\n" +
		"prod · started 2026-10-09 12:00 UTC\n" +
		"<blockquote expandable>alertname: CertExpiry\nnote: @\u200bchannel &lt;b&gt;x&lt;/b&gt;</blockquote>\n" +
		"<i>Certificates expire</i>\n" +
		"• a.example.org\n" +
		"• <s>b.example.org</s>\n" +
		"<a href=\"" + groupPage + "\">Open in Muster</a>\n" +
		"Acknowledged by @bob_tg"
	if m.ParseMode != "HTML" || m.Text != want || m.DisableNotification || m.ReplyParameters != nil ||
		strings.Contains(m.Text, "<table") || strings.Contains(m.Text, "<pre>") {
		t.Fatalf("post = %+v\n%s", m, m.Text)
	}
	if got := fmt.Sprint(keyboardOf(t, m.ReplyMarkup)); got != "[[Ack Resolve] [Snooze 1 h Snooze 4 h Snooze 24 h]]" {
		t.Fatalf("keyboard = %s", got)
	}
	var kb inlineKeyboard
	_ = json.Unmarshal(m.ReplyMarkup, &kb)
	if kb.InlineKeyboard[0][0].CallbackData != "a-acknowledge0" {
		t.Fatalf("callback data = %+v", kb)
	}
	n := channelNotifications(e.fake)
	if len(n) != 2 || !n[0].Sound || !n[1].Sound {
		t.Fatalf("notifications = %+v", n)
	}
}

// channelNotifications are the notifications of channel posts, without those of their copies in the group.
func channelNotifications(f *faketelegram.Fake) []faketelegram.Notification {
	var out []faketelegram.Notification
	for _, n := range f.Notifications() {
		if n.Chat == faketelegram.ChannelID {
			out = append(out, n)
		}
	}
	return out
}

// TestPublishQuietAndMentions is C-14.FR-6, C-11.FR-7 and C-12.FR-8: a Quiet post carries disable_notification and
// mentions nobody; a Loud one mentions a User with a Telegram Account link by a tg://user link and one without by
// display name; everyone and groups have no Telegram form.
func TestPublishQuietAndMentions(t *testing.T) {
	e := newAdapter(t)
	quiet := call(1)
	quiet.Targets = []mentions.Target{{Kind: mentions.TargetUser, User: &mentions.User{Name: "Ann", ExternalID: "42"}}}
	if o := e.adapter.Publish(t.Context(), quiet, root(messages.ColourFiring)); o.Kind != delivery.OutcomeOK {
		t.Fatal(o)
	}
	o := e.adapter.Publish(t.Context(), loud(
		mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{Name: "Ann <A>", ExternalID: "42"}},
		mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{Name: "Bob@ops", Username: "bob"}},
		mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{Name: "Ann <A>", ExternalID: "42"}},
		mentions.Target{Kind: mentions.TargetEveryone, Everyone: mentions.EveryoneChannel},
		mentions.Target{Kind: mentions.TargetGroup, Group: "ops"},
		mentions.Target{Kind: mentions.TargetUser}), root(messages.ColourFiring))
	if o.Kind != delivery.OutcomeOK {
		t.Fatal(o)
	}
	msgs := e.fake.Messages(faketelegram.ChannelID)
	if !msgs[0].DisableNotification || strings.Contains(msgs[0].Text, "tg://") {
		t.Fatalf("quiet = %+v", msgs[0])
	}
	lines := strings.Split(msgs[1].Text, "\n")
	if msgs[1].DisableNotification ||
		lines[1] != `<a href="tg://user?id=42">Ann &lt;A&gt;</a> Bob@`+"\u200b"+`ops` {
		t.Fatalf("loud = %+v", msgs[1])
	}
	n := channelNotifications(e.fake)
	if len(n) != 4 || n[0].Sound || !n[2].Sound {
		t.Fatalf("notifications = %+v", n)
	}
}

// TestUpdateKeepsTheKeyboard is C-14.AC-14 and C-14.FR-15: every edit carries the whole keyboard of the new state, an
// edit that changes nothing is success (F-017), and a resolved Root message gets an empty keyboard on purpose.
func TestUpdateKeepsTheKeyboard(t *testing.T) {
	e := newAdapter(t)
	o := e.adapter.Publish(t.Context(), loud(), root(messages.ColourFiring))
	steps := []struct {
		status string
		want   string
	}{
		{messages.ColourAcknowledged, "[[Unack Resolve] [Snooze 1 h Snooze 4 h Snooze 24 h]]"},
		{messages.ColourAcknowledged, "[[Unack Resolve] [Snooze 1 h Snooze 4 h Snooze 24 h]]"},
		{messages.ColourSnoozed, "[[Ack Unsnooze Resolve]]"},
		{messages.ColourResolved, "[]"},
	}
	for i, st := range steps {
		u := e.adapter.Update(t.Context(), loud(mentions.Target{Kind: mentions.TargetUser,
			User: &mentions.User{Name: "Ann", ExternalID: "42"}}), o.MessageID, root(st.status))
		if u.Kind != delivery.OutcomeOK || u.MessageID != o.MessageID {
			t.Fatalf("update %d = %+v", i, u)
		}
		m := only(t, e.fake)
		if got := fmt.Sprint(keyboardOf(t, m.ReplyMarkup)); got != st.want {
			t.Fatalf("update %d keyboard = %s", i, got)
		}
		if strings.Contains(m.Text, "tg://") {
			t.Fatalf("an edit mentioned: %s", m.Text)
		}
	}
	m := only(t, e.fake)
	// The post notified both accounts and its automatic copy member (F-013); no edit notified anybody.
	if len(m.Edits) != 3 || len(e.fake.Notifications()) != 3 {
		t.Fatalf("edits = %+v, notifications %+v", m.Edits, e.fake.Notifications())
	}
	for _, ed := range m.Edits {
		if ed.ReplyMarkup == nil {
			t.Fatalf("an edit without reply_markup: %+v", ed)
		}
	}
	if u := e.adapter.Update(t.Context(), call(1), "x1", root(messages.ColourFiring)); u.Kind !=
		delivery.OutcomeUnknown {
		t.Fatalf("a message id that is not Telegram's = %+v", u)
	}
	// Destination 2 knows its channel only as entered: the edit goes there.
	if u := e.adapter.Update(t.Context(), call(2), o.MessageID, root(messages.ColourFiring)); u.Kind !=
		delivery.OutcomeOK {
		t.Fatalf("update by username = %+v", u)
	}
	// C-14.FR-5: each edit first waited for the updates of its Connection, so that a press is answered before it; a
	// wait that fails edits nothing and is retried.
	if len(e.targets.awaited) != 5 || e.targets.awaited[0] != 5 {
		t.Fatalf("awaited %v", e.targets.awaited)
	}
	e.targets.awaitErr = errors.New("database down")
	edits := len(only(t, e.fake).Edits)
	if u := e.adapter.Update(t.Context(), call(1), o.MessageID, root(messages.ColourAcknowledged)); u.Kind !=
		delivery.OutcomeTransient || strings.Contains(string(u.Error), "database") ||
		len(only(t, e.fake).Edits) != edits {
		t.Fatalf("a failed wait = %+v", u)
	}
}

// TestLongMessagesFit is C-14.AC-8 and C-12.FR-11: 25 Alerts fit in 4,096 characters with their title, status, footer,
// buttons and the link to Muster; a message far too long is shortened and still accepted.
func TestLongMessagesFit(t *testing.T) {
	e := newAdapter(t)
	m := root(messages.ColourFiring)
	m.Alerts = &messages.AlertList{}
	for i := range 25 {
		m.Alerts.Lines = append(m.Alerts.Lines, messages.AlertLine{
			Text: fmt.Sprintf("alertname=CertExpiry domain=host-%02d.example.org", i)})
	}
	if o := e.adapter.Publish(t.Context(), call(1), m); o.Kind != delivery.OutcomeOK {
		t.Fatal(o)
	}
	text := only(t, e.fake).Text
	if length(text) > LengthLimit || strings.Count(text, "• ") != 25 || !strings.Contains(text, "#7 CertExpiry") ||
		!strings.Contains(text, "🔴") || !strings.Contains(text, "Open in Muster") ||
		!strings.HasSuffix(text, "Acknowledged by @bob_tg") {
		t.Fatalf("25 alerts = %d\n%s", length(text), text)
	}
	huge := root(messages.ColourFiring)
	huge.Heading.Title = strings.Repeat("Ü", 5000)
	for i := range 300 {
		huge.CommonAnnotations = append(huge.CommonAnnotations, messages.Label{Name: fmt.Sprint("a", i),
			Value: strings.Repeat("<&>", 50)})
		huge.Alerts.Lines = append(huge.Alerts.Lines, messages.AlertLine{Text: strings.Repeat("🔥", 40)})
	}
	if o := e.adapter.Publish(t.Context(), call(1), huge); o.Kind != delivery.OutcomeOK {
		t.Fatalf("huge = %+v", o)
	}
	text = e.fake.Messages(faketelegram.ChannelID)[1].Text
	if length(text) > LengthLimit || !strings.Contains(text, "Open in Muster") {
		t.Fatalf("huge = %d", length(text))
	}
	if e.adapter.LengthLimit() != 4096 || e.adapter.Markup() != messages.MarkupHTML {
		t.Fatal("limit or markup")
	}
}

// TestReplyIntoTheThread is C-14.FR-3, C-14.FR-6, C-12.FR-8 and C-14.AC-19: a Thread reply goes to the discussion
// group, never to the channel (F-015): as a reply to the post's automatic copy while attached, which puts it in the
// copy's comment Thread (F-007); as a reply to the last link of an unattached chain; or with no reply link as the first
// link. A Quiet reply carries disable_notification and no Mentions, a Loud one its Mentions first, and either notifies
// member only (F-014).
func TestReplyIntoTheThread(t *testing.T) {
	e := newAdapter(t)
	if o := e.adapter.Publish(t.Context(), call(1), root(messages.ColourFiring)); o.Kind != delivery.OutcomeOK ||
		o.MessageID != "1" {
		t.Fatalf("publish = %+v", o)
	}
	cp := e.fake.Messages(faketelegram.GroupID)
	if len(cp) != 1 || !cp[0].IsAutomaticForward {
		t.Fatalf("copy %+v", cp)
	}
	anchor := fmt.Sprint(cp[0].ID)
	reply := messages.Message{Kind: messages.KindReply, Language: "en", Colour: messages.ColourFiring,
		Lines: []string{"New alert: <c.example.org>"}, Buttons: []messages.Button{}}
	ann := mentions.Target{Kind: mentions.TargetUser, User: &mentions.User{Name: "Ann", ExternalID: "42"}}
	quiet := call(1)
	quiet.Targets = []mentions.Target{ann}
	o := e.adapter.Reply(t.Context(), quiet, delivery.Root{MessageID: "1", ThreadAnchorID: anchor}, reply)
	if o.Kind != delivery.OutcomeOK || o.MessageID != "2" || o.MessageURL != "https://t.me/c/1000000002/2" {
		t.Fatalf("reply = %+v", o)
	}
	o = e.adapter.Reply(t.Context(), loud(ann), delivery.Root{MessageID: "1", ThreadAnchorID: anchor}, reply)
	if o.Kind != delivery.OutcomeOK {
		t.Fatal(o)
	}
	msgs := e.fake.Messages(faketelegram.GroupID)
	if len(msgs) != 3 || !msgs[1].DisableNotification || string(msgs[1].ReplyParameters) != `{"message_id":1}` ||
		msgs[1].MessageThreadID != cp[0].ID || msgs[1].Text != "New alert: &lt;c.example.org&gt;" ||
		msgs[1].ReplyMarkup != nil ||
		msgs[2].Text != `<a href="tg://user?id=42">Ann</a>`+"\nNew alert: &lt;c.example.org&gt;" ||
		msgs[2].DisableNotification || msgs[2].MessageThreadID != cp[0].ID {
		t.Fatalf("group = %+v", msgs)
	}
	for _, n := range e.fake.Notifications() {
		if n.Chat == faketelegram.GroupID && n.Account != faketelegram.AccountMember {
			t.Errorf("a Thread reply notified %s", n.Account)
		}
	}
	// The first link of an unattached chain replies to nothing; the next one to the last link.
	if o = e.adapter.Reply(t.Context(), call(1), delivery.Root{MessageID: "1"}, reply); o.Kind != delivery.OutcomeOK {
		t.Fatal(o)
	}
	first := o.MessageID
	if o = e.adapter.Reply(t.Context(), call(1), delivery.Root{MessageID: "1", ChainLastID: first},
		reply); o.Kind != delivery.OutcomeOK {
		t.Fatal(o)
	}
	msgs = e.fake.Messages(faketelegram.GroupID)
	if len(msgs) != 5 || msgs[3].ReplyParameters != nil || msgs[3].MessageThreadID != 0 ||
		string(msgs[4].ReplyParameters) != `{"message_id":`+first+`}` || msgs[4].MessageThreadID != 0 {
		t.Fatalf("chain = %+v", msgs[3:])
	}
	if len(e.fake.Messages(faketelegram.ChannelID)) != 1 {
		t.Fatal("a reply went to the channel")
	}
	if o := e.adapter.Reply(t.Context(), call(2), delivery.Root{MessageID: "1"}, reply); o.Kind !=
		delivery.OutcomeFatal || string(o.Error) != errNoGroup {
		t.Fatalf("without a group = %+v", o)
	}
	if o := e.adapter.Reply(t.Context(), call(1), delivery.Root{MessageID: "1", ThreadAnchorID: "x"}, reply); o.Kind !=
		delivery.OutcomeUnknown {
		t.Fatalf("a copy id that is not Telegram's = %+v", o)
	}
}

// TestReplyToDeletedCopy is C-14.AC-16 at the adapter: with the copy deleted, a reply to it is refused with "message to
// be replied not found", a lost Thread; the same reply without the link goes to the group.
func TestReplyToDeletedCopy(t *testing.T) {
	e := newAdapter(t)
	e.adapter.Publish(t.Context(), call(1), root(messages.ColourFiring))
	cp := e.fake.Messages(faketelegram.GroupID)[0]
	if !e.fake.DeleteMessage(faketelegram.GroupID, cp.ID) {
		t.Fatal("no copy to delete")
	}
	reply := messages.Message{Kind: messages.KindReply, Language: "en", Lines: []string{"x"}}
	o := e.adapter.Reply(t.Context(), call(1), delivery.Root{MessageID: "1", ThreadAnchorID: fmt.Sprint(cp.ID)}, reply)
	if o.Kind != delivery.OutcomeThreadLost || !strings.Contains(string(o.Error), "message to be replied not found") {
		t.Fatalf("reply to the deleted copy = %+v", o)
	}
	if o = e.adapter.Reply(t.Context(), call(1), delivery.Root{MessageID: "1"}, reply); o.Kind != delivery.OutcomeOK {
		t.Fatalf("resend = %+v", o)
	}
}

// TestStormSummary: a message without a heading is its lines, posted to the channel.
func TestStormSummary(t *testing.T) {
	e := newAdapter(t)
	storm := messages.Message{Kind: messages.KindStorm, Language: "en", Colour: messages.ColourStorm,
		Lines: []string{"Storm: 30 new Alert Groups"}, Links: []messages.Link{{Text: "Open in Muster", URL: groupPage}},
		Buttons: []messages.Button{}}
	if o := e.adapter.Publish(t.Context(), call(1), storm); o.Kind != delivery.OutcomeOK {
		t.Fatal(o)
	}
	m := only(t, e.fake)
	if m.Text != "Storm: 30 new Alert Groups\n<a href=\""+groupPage+"\">Open in Muster</a>" || m.ReplyMarkup != nil {
		t.Fatalf("storm = %+v", m)
	}
}

// TestResponseMapping is C-14.FR-7 and C-11.FR-8, row by row, for sends and edits, with an error inside a 200 body
// classified the same way.
func TestResponseMapping(t *testing.T) {
	refusal := func(code int, description string) string {
		return fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q}`, code, description)
	}
	cases := []struct {
		name   string
		method string
		status int
		body   string
		kind   delivery.OutcomeKind
	}{
		{"edit not found", "editMessageText", 400, refusal(400, "Bad Request: message to edit not found"),
			delivery.OutcomeGone},
		{"can't be edited", "editMessageText", 400, refusal(400, "Bad Request: message can't be edited"),
			delivery.OutcomeGone},
		{"can't be edited in a 200", "editMessageText", 200, refusal(400, "Bad Request: message can't be edited"),
			delivery.OutcomeGone},
		{"not modified", "editMessageText", 400, refusal(400, "Bad Request: message is not modified"),
			delivery.OutcomeOK},
		{"reply not found", "sendMessage", 400, refusal(400, "Bad Request: message to be replied not found"),
			delivery.OutcomeThreadLost},
		{"markup", "sendMessage", 400, refusal(400, "Bad Request: can't parse entities: unsupported start tag"),
			delivery.OutcomeMarkupRejected},
		{"401", "sendMessage", 401, refusal(401, "Unauthorized"), delivery.OutcomeFatal},
		{"kicked", "sendMessage", 403, refusal(403, "Forbidden: bot was kicked from the channel chat"),
			delivery.OutcomeFatal},
		{"not enough rights", "sendMessage", 400, refusal(400, "Bad Request: not enough rights to send text messages to the chat"),
			delivery.OutcomeFatal},
		{"chat not found", "sendMessage", 400, refusal(400, "Bad Request: chat not found"), delivery.OutcomeFatal},
		{"need admin in a 200", "sendMessage", 200,
			refusal(400, "Bad Request: need administrator rights in the channel chat"), delivery.OutcomeFatal},
		{"not JSON", "sendMessage", 200, "<html>proxy</html>", delivery.OutcomeTransient},
		{"5xx", "sendMessage", 502, refusal(502, "Bad Gateway"), delivery.OutcomeTransient},
		{"anything else", "sendMessage", 400, refusal(400, "Bad Request: message text is empty"),
			delivery.OutcomeUnknown},
		{"no message id", "sendMessage", 200, `{"ok":true,"result":{"message_id":0}}`, delivery.OutcomeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAdapter(t)
			fault(t, e.fake, tc.method, tc.status, tc.body, 1)
			var o delivery.Outcome
			if tc.method == "editMessageText" {
				o = e.adapter.Update(t.Context(), call(1), "1", root(messages.ColourFiring))
			} else {
				o = e.adapter.Publish(t.Context(), call(1), root(messages.ColourFiring))
			}
			if o.Kind != tc.kind {
				t.Fatalf("outcome = %+v", o)
			}
		})
	}
	e := newAdapter(t)
	if o := e.adapter.Update(t.Context(), call(1), "99", root(messages.ColourFiring)); o.Kind !=
		delivery.OutcomeGone || !strings.Contains(string(o.Error), "message to edit not found") {
		t.Fatalf("edit of a deleted post = %+v", o)
	}
	_ = e.fake.SetMember(faketelegram.ChannelID, faketelegram.BotID, faketelegram.Member{
		Status: faketelegram.StatusAdministrator, CanEditMessages: true})
	if o := e.adapter.Publish(t.Context(), call(1), root(messages.ColourFiring)); o.Kind != delivery.OutcomeFatal ||
		string(o.Error) != "Telegram answered 400: "+faketelegram.DescriptionNeedAdmin {
		t.Fatalf("post without the right = %+v", o)
	}
}

// TestMarkupRejectedIsSentPlain: after "can't parse entities" the same text goes again without parse_mode, as plain
// text with its keyboard (C-11.FR-8).
func TestMarkupRejectedIsSentPlain(t *testing.T) {
	e := newAdapter(t)
	c := call(1)
	c.Plain = true
	if o := e.adapter.Publish(t.Context(), c, root(messages.ColourFiring)); o.Kind != delivery.OutcomeOK {
		t.Fatal(o)
	}
	m := only(t, e.fake)
	if m.ParseMode != "" || strings.Contains(m.Text, "<blockquote") || strings.Contains(m.Text, "<i>") ||
		!strings.Contains(m.Text, "#7 CertExpiry "+groupPage) || !strings.Contains(m.Text, "note: @\u200bchannel <b>x</b>") ||
		m.ReplyMarkup == nil {
		t.Fatalf("plain = %+v\n%s", m, m.Text)
	}
}

// TestRetryAfterScope is C-14.FR-7: a 429 waits exactly retry_after for its Destination, and for the whole
// Connection when a second Destination of it gets one within 60 seconds.
func TestRetryAfterScope(t *testing.T) {
	e := newAdapter(t)
	tooMany := `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`
	send := func(dest int64) delivery.Outcome {
		fault(t, e.fake, "sendMessage", 429, tooMany, 1)
		return e.adapter.Publish(t.Context(), call(dest), root(messages.ColourFiring))
	}
	steps := []struct {
		dest    int64
		advance time.Duration
		scope   delivery.Scope
	}{
		{1, 0, delivery.ScopeDestination},
		{1, 10 * time.Second, delivery.ScopeDestination},
		{2, 50 * time.Second, delivery.ScopeConnection},
		{1, 61 * time.Second, delivery.ScopeDestination},
	}
	for i, st := range steps {
		e.clock.Advance(st.advance)
		o := send(st.dest)
		if o.Kind != delivery.OutcomeRetryAfter || o.RetryAfter != 7*time.Second || o.Scope != st.scope {
			t.Fatalf("step %d = %+v", i, o)
		}
	}
	// Without a clock the system's is used.
	a := &Adapter{Targets: e.targets}
	fault(t, e.fake, "sendMessage", 429, tooMany, 1)
	if o := a.Publish(t.Context(), call(1), root(messages.ColourFiring)); o.Scope != delivery.ScopeDestination {
		t.Fatalf("system clock = %+v", o)
	}
}

// TestPerChatBudget is F-016 end to end: the 21st send or edit within a minute is answered 429 with the delay that
// is left, which the adapter waits exactly.
func TestPerChatBudget(t *testing.T) {
	e := newAdapter(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	e.fake.SetClock(func() time.Time { return now })
	o := e.adapter.Publish(t.Context(), call(1), root(messages.ColourFiring))
	for i := range 19 {
		st := []string{messages.ColourAcknowledged, messages.ColourFiring}[i%2]
		if u := e.adapter.Update(t.Context(), call(1), o.MessageID, root(st)); u.Kind != delivery.OutcomeOK {
			t.Fatalf("edit %d = %+v", i, u)
		}
	}
	u := e.adapter.Update(t.Context(), call(1), o.MessageID, root(messages.ColourSnoozed))
	if u.Kind != delivery.OutcomeRetryAfter || u.RetryAfter != 60*time.Second || u.Scope != delivery.ScopeDestination {
		t.Fatalf("21st = %+v", u)
	}
}

// TestTargetsAndCheck: a Destination without a Telegram Connection is Fatal, an unreadable one Transient; Check is the
// Destination check in the client class of the call.
func TestTargetsAndCheck(t *testing.T) {
	e := newAdapter(t)
	m := root(messages.ColourFiring)
	for _, f := range []func() delivery.Outcome{
		func() delivery.Outcome { return e.adapter.Publish(t.Context(), call(9), m) },
		func() delivery.Outcome { return e.adapter.Update(t.Context(), call(9), "1", m) },
		func() delivery.Outcome { return e.adapter.Reply(t.Context(), call(9), delivery.Root{}, m) },
		func() delivery.Outcome { return e.adapter.Check(t.Context(), call(9)) },
	} {
		if o := f(); o.Kind != delivery.OutcomeFatal || string(o.Error) != ErrNoTarget.Error() {
			t.Fatalf("no target = %+v", o)
		}
	}
	if o := e.adapter.Check(t.Context(), call(1)); o.Kind != delivery.OutcomeOK {
		t.Fatalf("check = %+v", o)
	}
	if o := e.adapter.Check(t.Context(), call(3)); o.Kind != delivery.OutcomeFatal ||
		string(o.Error) != MessageNoComments {
		t.Fatalf("check without comments = %+v", o)
	}
	// A channel whose comments moved to another group than the stored one fails the probe.
	moved := e.targets.byID[1]
	moved.GroupID = -1009
	e.targets.byID[4] = moved
	if o := e.adapter.Check(t.Context(), call(4)); o.Kind != delivery.OutcomeFatal || string(o.Error) != MessageMoved {
		t.Fatalf("moved = %+v", o)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if o := e.adapter.Check(ctx, call(1)); o.Kind == delivery.OutcomeOK {
		t.Fatalf("check with a cancelled context = %+v", o)
	}
	e.targets.err = errors.New("down")
	if o := e.adapter.Publish(t.Context(), call(1), m); o.Kind != delivery.OutcomeTransient {
		t.Fatalf("unreadable = %+v", o)
	}
}

// TestDeliveryErrorsAreRedacted is C-14.AC-7 and C-14.FR-12: a network error on a send leaves the token in no log
// line and no outcome error, which delivery stores in the Timeline and as a Destination's reason.
func TestDeliveryErrorsAreRedacted(t *testing.T) {
	var buf bytes.Buffer
	c, err := NewClient(network(t, &buf), Settings{BaseURL: "http://127.0.0.1:1", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Targets: &targets{byID: map[int64]Target{1: {Client: c, ConnectionID: 5, Channel: "@x",
		GroupID: 1}}}}
	m := root(messages.ColourFiring)
	for _, o := range []delivery.Outcome{
		a.Publish(t.Context(), call(1), m), a.Update(t.Context(), call(1), "1", m),
		a.Reply(t.Context(), call(1), delivery.Root{}, m), a.Check(t.Context(), call(1)),
	} {
		if o.Kind != delivery.OutcomeTransient || o.Error == "" || strings.Contains(string(o.Error), testToken) ||
			strings.Contains(string(o.Error), "AAE-s3cr3t") {
			t.Fatalf("outcome = %+v", o)
		}
	}
	if strings.Contains(buf.String(), "AAE-s3cr3t") {
		t.Fatalf("log = %s", buf.String())
	}
}
