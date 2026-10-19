// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/fakes/faketelegram"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/leader"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// The fixtures of presses: Connection 5, whose Destination 51 posts into the channel of the fake; Alert Group A,
// posted there, and its Route's Snooze durations of 1 h and 4 h.
const (
	pressGroup = "AG0000000000A1"
	bobTG      = 5001
	carolTG    = 6001
)

var (
	pressConn = int64(5)
	pressDest = delivery.Destination{ID: 51, PublicID: "DS0000000000T1", Type: delivery.TypeTelegram,
		Connection: &pressConn}
	pressRoles = auth.Roles{"responder": {groups.PermissionAcknowledge, groups.PermissionResolve,
		groups.PermissionSnooze}}
	bobUser = accountlinks.User{ID: 7, PublicID: "SR0000000000B1", Login: "bob", Name: "Bob", Role: "responder",
		Status: accountlinks.StatusActive}
	press0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
)

// keyState keeps keyring_state in memory.
type keyState struct {
	keyring.Store
	row *kdb.GetKeyringStateRow
}

func (s *keyState) GetKeyringState(context.Context) (kdb.GetKeyringStateRow, error) {
	if s.row == nil {
		return kdb.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.row, nil
}

func (s *keyState) CreateKeyringState(_ context.Context, p kdb.CreateKeyringStateParams) (int64, error) {
	s.row = &kdb.GetKeyringStateRow{ActiveKeyID: p.ActiveKeyID, CanaryKeyID: p.ActiveKeyID,
		CanaryCiphertext: p.CanaryCiphertext}
	return 1, nil
}

func openKeyring(t *testing.T, seed byte) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{seed}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(t.Context(), &keyState{}, press0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	return k
}

// replyKey is a Reminder of an Alert Group: the Connection, the discussion group and the message.
type replyKey struct {
	conn, chat, message int64
	group               string
}

type pressBindings struct {
	bound   map[[3]int64]delivery.Binding
	replies map[replyKey]delivery.Binding
	err     error
}

func (f *pressBindings) TelegramReplyBinding(_ context.Context, conn int64, group string, chat, message int64) (
	delivery.Binding, error) {
	if f.err != nil {
		return delivery.Binding{}, f.err
	}
	b, ok := f.replies[replyKey{conn, chat, message, group}]
	if !ok {
		return delivery.Binding{}, delivery.ErrNotBound
	}
	return b, nil
}

func (f *pressBindings) TelegramPressBinding(_ context.Context, conn int64, group string, chat, message int64) (
	delivery.Binding, error) {
	if f.err != nil {
		return delivery.Binding{}, f.err
	}
	b, ok := f.bound[[3]int64{conn, chat, message}]
	if !ok || group != pressGroup {
		return delivery.Binding{}, delivery.ErrNotBound
	}
	return b, nil
}

type pressLinks struct {
	users           map[string]accountlinks.User
	space, external string
	err             error
}

func (f *pressLinks) Lookup(_ context.Context, space, external string) (accountlinks.User, error) {
	f.space, f.external = space, external
	if f.err != nil {
		return accountlinks.User{}, f.err
	}
	u, ok := f.users[external]
	if !ok {
		return accountlinks.User{}, accountlinks.ErrNotLinked
	}
	return u, nil
}

// ran is a Command the handler ran.
type ran struct {
	command, group string
	caller         groups.Caller
	end            *groups.SnoozeEnd
}

type pressCommands struct {
	calls  []ran
	result groups.Result
	err    error
	// before runs before each Command, as the dispatcher would before its re-render.
	before func()
}

func (f *pressCommands) run(r ran) (groups.Result, error) {
	if f.before != nil {
		f.before()
	}
	f.calls = append(f.calls, r)
	return f.result, f.err
}

func (f *pressCommands) Acknowledge(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(ran{command: "acknowledge", group: id, caller: c})
}

func (f *pressCommands) Unacknowledge(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(ran{command: "unacknowledge", group: id, caller: c})
}

func (f *pressCommands) Resolve(_ context.Context, c groups.Caller, id string, _ *string) (groups.Result, error) {
	return f.run(ran{command: "resolve", group: id, caller: c})
}

func (f *pressCommands) Snooze(_ context.Context, c groups.Caller, id string, end groups.SnoozeEnd) (groups.Result,
	error) {
	return f.run(ran{command: "snooze", group: id, caller: c, end: &end})
}

func (f *pressCommands) Unsnooze(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(ran{command: "unsnooze", group: id, caller: c})
}

func (f *pressCommands) AnswerReminder(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(ran{command: "still_on_it", group: id, caller: c})
}

// recordingPath is the interactive path of the tests: it records the subject and the outcome of each answer and
// answers as deliverytest.Unlimited, or fails with err.
type recordingPath struct {
	path     *delivery.Interactive
	subjects []delivery.Subject
	outcomes []delivery.Outcome
	err      error
}

func (a *recordingPath) Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error) {
	a.subjects = append(a.subjects, s)
	if a.err != nil {
		return delivery.Outcome{}, a.err
	}
	out, err := a.path.Do(ctx, s, op)
	a.outcomes = append(a.outcomes, out)
	return out, err
}

type pressEnv struct {
	fake     *faketelegram.Fake
	client   *Client
	keys     *keyring.Keyring
	bindings *pressBindings
	links    *pressLinks
	commands *pressCommands
	path     *recordingPath
	business *clock.Manual
	log      *bytes.Buffer
	presses  *Presses
	// post is the channel post of Alert Group A, and reply a reply in its comment Thread.
	post, reply int64
}

func newPressEnv(t *testing.T) *pressEnv {
	t.Helper()
	f := startFake(t)
	e := &pressEnv{fake: f, client: newClient(t, Settings{BaseURL: f.URL(), Token: testToken}),
		keys: openKeyring(t, 'b'), links: &pressLinks{users: map[string]accountlinks.User{"5001": bobUser}},
		commands: &pressCommands{result: groups.Result{Outcome: groups.OutcomeDone}},
		business: clock.NewManual(press0), log: &bytes.Buffer{}}
	e.path = &recordingPath{path: deliverytest.Unlimited(1, clock.Clocks{Business: e.business,
		Real: clock.NewManual(press0)})}
	e.post = e.send(t, faketelegram.ChannelID, 0, e.keyboard(t, e.keys, buttons.SubjectRoot, pressGroup))
	e.reply = e.send(t, faketelegram.GroupID, e.copyOf(t), e.keyboard(t, e.keys, buttons.SubjectReply, pressGroup))
	e.bindings = &pressBindings{bound: map[[3]int64]delivery.Binding{
		{pressConn, faketelegram.ChannelID, e.post}: {Destination: pressDest, ChannelID: "-1001000000001",
			Language: "en", SnoozeSeconds: []int64{3600, 14400}},
	}, replies: map[replyKey]delivery.Binding{}}
	e.presses = &Presses{Bindings: e.bindings, Links: e.links, Commands: e.commands, Roles: pressRoles, Keys: e.keys,
		Path: e.path, Business: e.business, PublicURL: "http://localhost:8080/",
		Log: logging.New(e.log, logging.LevelInfo)}
	return e
}

// keyboard is the keyboard of a message about group: Ack, Resolve, Snooze 1 h, 4 h and 24 h, the last one a duration
// the Route no longer has, and the Snooze presets.
func (e *pressEnv) keyboard(t *testing.T, k buttons.Keys, s buttons.Subject, group string) *inlineKeyboard {
	t.Helper()
	sign := func(command string, arg int) string {
		id, _, err := buttons.Sign(k, buttons.Action{Subject: s, PublicID: group, Command: command, Argument: arg})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	return &inlineKeyboard{InlineKeyboard: [][]inlineButton{
		{{Text: "Ack", CallbackData: sign(buttons.CommandAcknowledge, 0)},
			{Text: "Resolve", CallbackData: sign(buttons.CommandResolve, 0)}},
		{{Text: "Snooze 1 h", CallbackData: sign(buttons.CommandSnooze, 0)},
			{Text: "Snooze 4 h", CallbackData: sign(buttons.CommandSnooze, 1)},
			{Text: "Snooze 24 h", CallbackData: sign(buttons.CommandSnooze, 2)}},
		{{Text: "Unack", CallbackData: sign(buttons.CommandUnacknowledge, 0)},
			{Text: "Unsnooze", CallbackData: sign(buttons.CommandUnsnooze, 0)}},
		{{Text: "Until tomorrow 09:00", CallbackData: sign(buttons.CommandSnooze, buttons.ArgumentTomorrow)},
			{Text: "Until Monday 09:00", CallbackData: sign(buttons.CommandSnooze, buttons.ArgumentWeekStart)}},
	}}
}

// send sends a message with kb to chat, as a reply to replyTo when it is set, and returns its id.
func (e *pressEnv) send(t *testing.T, chat, replyTo int64, kb *inlineKeyboard) int64 {
	t.Helper()
	m := outgoing{ChatID: chat, Text: "message", ReplyMarkup: kb}
	if replyTo != 0 {
		m.ReplyParameters = &replyParameters{MessageID: replyTo}
	}
	sent, r := e.client.sendMessage(t.Context(), outbound.ClassDelivery, m)
	if !r.OK() {
		t.Fatalf("send = %+v", r)
	}
	return sent.MessageID
}

// copyOf is the automatic copy of the channel post in the discussion group.
func (e *pressEnv) copyOf(t *testing.T) int64 {
	t.Helper()
	for _, m := range e.fake.Messages(faketelegram.GroupID) {
		if m.IsAutomaticForward {
			return m.ID
		}
	}
	t.Fatal("no copy")
	return 0
}

// press presses a button on the fake and returns the update that carries it.
func (e *pressEnv) press(t *testing.T, body string) Update {
	t.Helper()
	var p faketelegram.Press
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	if _, status, failure := e.fake.Press(t.Context(), p); failure != "" {
		t.Fatalf("press %s = %d %s", body, status, failure)
	}
	for {
		updates, r := e.client.GetUpdates(t.Context(), nil, 0)
		if !r.OK() {
			t.Fatalf("getUpdates = %+v", r)
		}
		for _, u := range updates {
			next := u.UpdateID + 1
			e.client.GetUpdates(t.Context(), &next, 0)
			if u.CallbackQuery != nil {
				return u
			}
		}
	}
}

// pressAs presses the button label on the channel post as the Telegram account from, handles the update and returns
// the text of the answer the fake recorded, empty without one.
func (e *pressEnv) pressAs(t *testing.T, from int64, label string) string {
	t.Helper()
	return e.handle(t, e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":%q}`,
		faketelegram.ChannelID, e.post, from, label)))
}

// handle handles u and returns the text of the answer it made, empty without one.
func (e *pressEnv) handle(t *testing.T, u Update) string {
	t.Helper()
	before := len(e.fake.Answers())
	if err := e.presses.Handle(t.Context(), Conn{ID: pressConn, PublicID: "CN0000000000T1", Client: e.client},
		u); err != nil {
		t.Fatalf("handle = %v", err)
	}
	a := e.fake.Answers()
	if len(a) == before {
		return ""
	}
	if len(a) != before+1 || !a[before].OK || a[before].CallbackQueryID != u.CallbackQuery.ID {
		t.Fatalf("answers %+v", a[before:])
	}
	return a[before].Text
}

// last is the last line of event.
func (e *pressEnv) last(t *testing.T, event string) map[string]any {
	t.Helper()
	var out map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(e.log.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["event"] == event {
			out = m
		}
	}
	if out == nil {
		t.Fatalf("no %s in %s", event, e.log)
	}
	return out
}

// TestPressRunsTheCommandAndAnswersFirst covers C-14.FR-4, FR-5, AC-18 and C-10.FR-3: a press from a linked account
// runs the Command as its User with the Transport telegram and is answered "Done: {command}" through the interactive
// path, limited by the Connection alone (D295), once the Command ran and before anything else; telegram_press logs
// it.
func TestPressRunsTheCommandAndAnswersFirst(t *testing.T) {
	e := newPressEnv(t)
	var answersAtCommand int
	e.commands.before = func() { answersAtCommand = len(e.fake.Answers()) }
	if got := e.pressAs(t, bobTG, "Ack"); got != "Done: Acknowledge" {
		t.Fatalf("answer %q", got)
	}
	c := e.commands.calls
	if len(c) != 1 || c[0].command != "acknowledge" || c[0].group != pressGroup ||
		c[0].caller.Transport != audit.TransportTelegram || c[0].caller.Actor != audit.User(7, "SR0000000000B1") ||
		!slices.Contains(c[0].caller.Permissions, groups.PermissionAcknowledge) || answersAtCommand != 0 {
		t.Fatalf("commands %+v, %d answers before the Command", c, answersAtCommand)
	}
	if e.links.space != accountlinks.SpaceTelegram || e.links.external != "5001" {
		t.Errorf("lookup in %s of %s", e.links.space, e.links.external)
	}
	if len(e.path.subjects) != 1 || e.path.subjects[0].Destination != nil || e.path.subjects[0].Connection == nil ||
		*e.path.subjects[0].Connection != pressConn {
		t.Errorf("answered on %+v", e.path.subjects)
	}
	l := e.last(t, "telegram_press")
	if l["connection"] != "CN0000000000T1" || l["group"] != pressGroup || l["command"] != "acknowledge" ||
		l["outcome"] != "done" || l["error"] != nil {
		t.Errorf("log %v", l)
	}
	for _, r := range e.fake.Requests() {
		if strings.HasSuffix(r.Path, "/answerCallbackQuery") && !strings.Contains(r.Body, `"callback_query_id":"cq-1"`) {
			t.Errorf("answer request %s", r.Body)
		}
	}
	e.commands.result = groups.Result{Outcome: groups.OutcomeUnchanged}
	e.bindings.bound[[3]int64{pressConn, faketelegram.ChannelID, e.post}] = delivery.Binding{Destination: pressDest,
		Language: "ru", SnoozeSeconds: []int64{3600}}
	if got := e.pressAs(t, bobTG, "Resolve"); got != "Готово: Закрыть" || e.last(t, "telegram_press")["outcome"] !=
		"unchanged" {
		t.Errorf("a Russian answer %q", got)
	}
}

// TestPressSnoozesForThePressedDuration covers C-10.FR-6: a Snooze button snoozes until the business time plus the
// Route's duration at its index; a duration the Route no longer has is a button that cannot be verified.
func TestPressSnoozesForThePressedDuration(t *testing.T) {
	e := newPressEnv(t)
	if got := e.pressAs(t, bobTG, "Snooze 4 h"); got != "Done: Snooze" {
		t.Fatalf("answer %q", got)
	}
	c := e.commands.calls
	if len(c) != 1 || c[0].command != "snooze" || c[0].end == nil || c[0].end.Until == nil ||
		!c[0].end.Until.Equal(press0.Add(4*time.Hour)) {
		t.Fatalf("commands %+v", c)
	}
	for _, label := range []string{"Unack", "Unsnooze"} {
		e.pressAs(t, bobTG, label)
	}
	if got := e.pressAs(t, bobTG, "Snooze 24 h"); got != "This button could not be verified; nothing was changed." {
		t.Errorf("a duration the Route no longer has = %q", got)
	}
	if n := len(e.commands.calls); n != 3 || e.commands.calls[1].command != "unacknowledge" ||
		e.commands.calls[2].command != "unsnooze" {
		t.Errorf("commands %+v", e.commands.calls)
	}
}

// TestPressSnoozePresets covers C-10.FR-18 and C-10.AC-23: a press on "Until tomorrow" or "Until {day}" from a linked
// account runs a Snooze with that Snooze preset and no time zone — the dispatcher takes the User's profile time zone,
// else the Organization's — as its User with the Transport telegram, answered "Done: Snooze"; on a Root message whose
// Route lost its Snooze durations after it was published, it runs nothing and is answered "This button could not be
// verified; nothing was changed."; on a test message, "This is a test message; nothing was changed".
func TestPressSnoozePresets(t *testing.T) {
	e := newPressEnv(t)
	for _, c := range []struct {
		label  string
		preset groups.SnoozePreset
	}{{"Until tomorrow 09:00", groups.PresetTomorrow}, {"Until Monday 09:00", groups.PresetWeekStart}} {
		if got := e.pressAs(t, bobTG, c.label); got != "Done: Snooze" {
			t.Errorf("%s: answer %q", c.label, got)
		}
		r := e.commands.calls[len(e.commands.calls)-1]
		if r.command != buttons.CommandSnooze || r.group != pressGroup || r.caller.Transport != audit.TransportTelegram ||
			r.caller.Actor != audit.User(7, "SR0000000000B1") || r.end == nil || r.end.Preset != c.preset ||
			r.end.TimeZone != nil || r.end.Until != nil || r.end.NoEnd {
			t.Errorf("%s: ran %+v, the end %+v", c.label, r, r.end)
		}
		if l := e.last(t, "telegram_press"); l["command"] != buttons.CommandSnooze || l["outcome"] != "done" {
			t.Errorf("%s: log %v", c.label, l)
		}
	}
	e.bindings.bound[[3]int64{pressConn, faketelegram.ChannelID, e.post}] = delivery.Binding{Destination: pressDest,
		ChannelID: "-1001000000001", Language: "en"}
	for _, label := range []string{"Until tomorrow 09:00", "Until Monday 09:00"} {
		if got := e.pressAs(t, bobTG, label); got != "This button could not be verified; nothing was changed." ||
			e.last(t, "telegram_press")["outcome"] != "not_verified" {
			t.Errorf("%s without durations: answer %q", label, got)
		}
	}
	if len(e.commands.calls) != 2 {
		t.Errorf("a preset without durations ran %+v", e.commands.calls[2:])
	}
	post := e.send(t, faketelegram.ChannelID, 0, e.keyboard(t, e.keys, buttons.SubjectTest, "DS0000000000T1"))
	text := e.handle(t, e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":5001},"button":%q}`,
		faketelegram.ChannelID, post, "Until Monday 09:00")))
	if l := e.last(t, "telegram_press"); text != "This is a test message; nothing was changed" ||
		len(e.commands.calls) != 2 || l["outcome"] != "test" || l["command"] != buttons.CommandSnooze {
		t.Errorf("a preset of a test message: answer %q, %v", text, l)
	}
}

// TestPressFromAnUnlinkedAccount covers C-14.AC-9 and C-10.FR-11: a press from a Telegram account without an Account
// link runs nothing and is answered with the link to the profile, in the language of the Route, without a word of
// Mattermost.
func TestPressFromAnUnlinkedAccount(t *testing.T) {
	e := newPressEnv(t)
	got := e.pressAs(t, carolTG, "Ack")
	if got != "Your Telegram account is not linked to Muster. Link it in your profile: http://localhost:8080/profile" ||
		len(e.commands.calls) != 0 || e.last(t, "telegram_press")["outcome"] != "not_linked" {
		t.Fatalf("answer %q, commands %+v", got, e.commands.calls)
	}
	e.bindings.bound[[3]int64{pressConn, faketelegram.ChannelID, e.post}] = delivery.Binding{Destination: pressDest,
		Language: "ru"}
	if got := e.pressAs(t, carolTG, "Ack"); got != "Ваша учётная запись Telegram не связана с Muster. Свяжите её в "+
		"профиле: http://localhost:8080/profile" || strings.Contains(got, "Mattermost") {
		t.Errorf("Russian answer %q", got)
	}
}

// TestPressRefusals: a disabled User, a Viewer, a refused Command and failures change nothing and are answered with
// their refusal, cut to 200 characters.
func TestPressRefusals(t *testing.T) {
	e := newPressEnv(t)
	e.links.users["5002"] = accountlinks.User{ID: 8, PublicID: "SR0000000000C1", Role: "responder",
		Status: accountlinks.StatusDisabled}
	if got := e.pressAs(t, 5002, "Ack"); got != "Your Muster account is disabled" || len(e.commands.calls) != 0 {
		t.Errorf("disabled = %q", got)
	}
	e.commands.err = &groups.ForbiddenError{Permission: groups.PermissionAcknowledge}
	if got := e.pressAs(t, bobTG, "Ack"); got != "You are not permitted to do this" ||
		e.last(t, "telegram_press")["outcome"] != "forbidden" {
		t.Errorf("forbidden = %q", got)
	}
	e.commands.err = &groups.RefusedError{Code: groups.CodeAlreadyResolved, Message: strings.Repeat("already resolved ",
		20)}
	if got := e.pressAs(t, bobTG, "Ack"); len([]rune(got)) != MaxAnswerLength ||
		!strings.HasPrefix(got, "Already resolved") || e.last(t, "telegram_press")["outcome"] != "refused" {
		t.Errorf("refused = %q (%d)", got, len([]rune(got)))
	}
	e.commands.err = errors.New("database down")
	if got := e.pressAs(t, bobTG, "Ack"); got != "Muster could not run this command; nothing was changed." ||
		e.last(t, "telegram_press")["error"] != "database down" {
		t.Errorf("a failed Command = %q, %v", got, e.last(t, "telegram_press"))
	}
	e.commands.err = nil
	e.links.err = errors.New("links down")
	if got := e.pressAs(t, bobTG, "Ack"); got != "Muster could not run this command; nothing was changed." ||
		e.last(t, "telegram_press")["outcome"] != "failed" {
		t.Errorf("a failed lookup = %q", got)
	}
	e.links.err = nil
	e.bindings.err = errors.New("bindings down")
	if got := e.pressAs(t, bobTG, "Ack"); got != "Muster could not run this command; nothing was changed." ||
		e.path.subjects[len(e.path.subjects)-1].Connection == nil {
		t.Errorf("a failed binding = %q", got)
	}
	if n := len(e.commands.calls); n != 3 {
		t.Errorf("%d commands", n)
	}
}

// TestPressBinding covers C-14.FR-4: button data must be signed by a key of the Keyring, at most 64 bytes, about a
// Root message, and belong to the chat and message pressed; anything else is answered "This button could not be
// verified" on the Connection's limiter and runs nothing.
func TestPressBinding(t *testing.T) {
	e := newPressEnv(t)
	other := e.send(t, faketelegram.ChannelID, 0, e.keyboard(t, openKeyring(t, 'c'), buttons.SubjectRoot, pressGroup))
	ack, _ := buttonData(t, e.fake, faketelegram.ChannelID, e.post, "Ack")
	tampered := ack[:len(ack)-2] + map[bool]string{true: "AA", false: "BB"}[!strings.HasSuffix(ack, "AA")]
	for name, body := range map[string]string{
		"another message":     fmt.Sprintf(`{"chat":%d,"message_id":999999,"data_from":%d,"from":{"id":%d},"button":"Ack"}`, faketelegram.ChannelID, e.post, bobTG),
		"another key":         fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"Ack"}`, faketelegram.ChannelID, other, bobTG),
		"a thread reply":      fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"Ack"}`, faketelegram.GroupID, e.reply, bobTG),
		"a forged signature":  fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"x","data":%q}`, faketelegram.ChannelID, e.post, bobTG, tampered),
		"garbage":             fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"x","data":"garbage"}`, faketelegram.ChannelID, e.post, bobTG),
		"more than 64 bytes":  fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"x","data":%q}`, faketelegram.ChannelID, e.post, bobTG, ack+"AAAA"),
		"another alert group": fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"x","data":%q}`, faketelegram.ChannelID, e.post, bobTG, e.signed(t, "AG0000000000B1")),
	} {
		t.Run(name, func(t *testing.T) {
			got := e.handle(t, e.press(t, body))
			if got != "This button could not be verified; nothing was changed." || len(e.commands.calls) != 0 ||
				e.last(t, "telegram_press")["outcome"] != "not_verified" {
				t.Errorf("answer %q, commands %+v", got, e.commands.calls)
			}
			if s := e.path.subjects[len(e.path.subjects)-1]; s.Destination != nil || s.Connection == nil ||
				*s.Connection != pressConn {
				t.Errorf("answered on %+v", s)
			}
		})
	}
	// A press without a message, or an update without a press, is logged and answers nothing.
	u := e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"Ack"}`, faketelegram.ChannelID,
		e.post, bobTG))
	u.CallbackQuery.Message = nil
	if got := e.handle(t, u); got != "This button could not be verified; nothing was changed." {
		t.Errorf("a press without its message = %q", got)
	}
	if got := e.handle(t, Update{UpdateID: 1, CallbackQuery: &CallbackQuery{}}); got != "" ||
		e.last(t, "telegram_press")["outcome"] != "not_verified" {
		t.Errorf("a press without an id = %q", got)
	}
}

// signed is the Ack button data of group's Root message.
func (e *pressEnv) signed(t *testing.T, group string) string {
	t.Helper()
	id, _, err := buttons.Sign(e.keys, buttons.Action{Subject: buttons.SubjectRoot, PublicID: group,
		Command: buttons.CommandAcknowledge})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// buttonData is the callback_data of the button label of the message id of chat on the fake.
func buttonData(t *testing.T, f *faketelegram.Fake, chat, id int64, label string) (string, bool) {
	t.Helper()
	for _, m := range f.Messages(chat) {
		if m.ID != id {
			continue
		}
		var kb inlineKeyboard
		_ = json.Unmarshal(m.ReplyMarkup, &kb)
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if b.Text == label {
					return b.CallbackData, true
				}
			}
		}
	}
	t.Fatalf("no button %s", label)
	return "", false
}

// TestPressAfterAGapIsDropped covers C-14.AC-4: a press whose update arrived after the Connection received no updates
// for longer than telegram.press_max_age changes nothing, is not answered and is logged telegram_press_dropped; one
// after a gap of exactly that long is handled.
func TestPressAfterAGapIsDropped(t *testing.T) {
	e := newPressEnv(t)
	u := e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"Ack"}`, faketelegram.ChannelID,
		e.post, bobTG))
	u.Gap = PressMaxAge + time.Second
	if got := e.handle(t, u); got != "" || len(e.commands.calls) != 0 || len(e.path.subjects) != 0 {
		t.Fatalf("a press after the gap = %q, %+v", got, e.commands.calls)
	}
	if l := e.last(t, "telegram_press_dropped"); l["connection"] != "CN0000000000T1" || l["gap_seconds"] != 3601.0 ||
		l["level"] != "INFO" {
		t.Errorf("log %v", l)
	}
	u = e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"Ack"}`, faketelegram.ChannelID,
		e.post, bobTG))
	u.Gap = PressMaxAge
	if got := e.handle(t, u); got != "Done: Acknowledge" {
		t.Errorf("a press after a gap of exactly the age = %q", got)
	}
}

// TestPressAnswerFailures: an answer that Telegram refuses because it came after the deadline (F-010), or that finds
// no limiter token, is logged with why, masked of the bot token; the Command ran all the same and the update is
// confirmed.
func TestPressAnswerFailures(t *testing.T) {
	e := newPressEnv(t)
	u := e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"Ack","pressed_ms_ago":16000}`,
		faketelegram.ChannelID, e.post, bobTG))
	if err := e.presses.Handle(t.Context(), Conn{ID: pressConn, PublicID: "CN0000000000T1", Client: e.client},
		u); err != nil {
		t.Fatal(err)
	}
	l := e.last(t, "telegram_press")
	a := e.fake.Answers()
	if len(a) != 1 || a[0].OK || l["outcome"] != "done" || len(e.commands.calls) != 1 ||
		!strings.Contains(fmt.Sprint(l["error"]), faketelegram.DescriptionQueryTooOld) {
		t.Fatalf("a late answer: %+v, %v", a, l)
	}
	if strings.Contains(e.log.String(), testToken) {
		t.Error("the bot token reached the log")
	}
	e.path.err = &delivery.LimitedError{RetryAfter: time.Second}
	if got := e.pressAs(t, bobTG, "Ack"); got != "" || len(e.commands.calls) != 2 ||
		!strings.Contains(fmt.Sprint(e.last(t, "telegram_press")["error"]), "no limiter token") {
		t.Errorf("a limited answer = %q, %v", got, e.last(t, "telegram_press"))
	}
	// A context that ends leaves the update unconfirmed.
	e.path.err = nil
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := e.presses.Handle(ctx, Conn{ID: pressConn, PublicID: "CN0000000000T1", Client: e.client}, u); err == nil {
		t.Error("a cancelled press was confirmed")
	}
}

// TestQuietSince: a replica that starts receiving at start without having received before counts the outage just
// before it — a downtime that ended no earlier than leader.AbsenceNotice before start, or an alive mark older than
// that — and otherwise start.
func TestQuietSince(t *testing.T) {
	start := press0
	for _, c := range []struct {
		name  string
		o     Outage
		since time.Time
		out   bool
	}{
		{"nothing recorded", Outage{}, start, false},
		{"a recent alive mark", Outage{AliveAt: start.Add(-30 * time.Second)}, start, false},
		{"a stale alive mark", Outage{AliveAt: start.Add(-2 * time.Hour)}, start.Add(-2 * time.Hour), true},
		{"a downtime just recorded", Outage{AliveAt: start, DowntimeStart: start.Add(-3 * time.Hour),
			DowntimeEnd: start.Add(-time.Second)}, start.Add(-3 * time.Hour), true},
		{"an old downtime", Outage{AliveAt: start, DowntimeStart: start.Add(-5 * time.Hour),
			DowntimeEnd: start.Add(-leader.AbsenceNotice - time.Second)}, start, false},
	} {
		since, out := c.o.quietSince(start)
		if !since.Equal(c.since) || out != c.out {
			t.Errorf("%s: %v %v, want %v %v", c.name, since, out, c.since, c.out)
		}
	}
}

// TestPressAnswerSpendsOnlyTheConnection covers C-14.FR-5 with D295: an answer posts nothing to the channel, so every
// answer — done, not linked, refused or not verified — is limited by the Connection alone and spends no token of the
// Destination; a 429 on an answer holds the whole Connection, since Telegram limits answers per bot, not per chat.
func TestPressAnswerSpendsOnlyTheConnection(t *testing.T) {
	e := newPressEnv(t)
	e.pressAs(t, bobTG, "Ack")
	e.pressAs(t, 6001, "Ack")
	e.commands.err = &groups.RefusedError{Code: groups.CodeAlreadyResolved, Message: "already resolved"}
	e.pressAs(t, bobTG, "Resolve")
	e.commands.err = nil
	e.handle(t, e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":999999,"data_from":%d,"from":{"id":%d},"button":"Ack"}`,
		faketelegram.ChannelID, e.post, bobTG)))
	if len(e.path.subjects) != 4 {
		t.Fatalf("answers %+v", e.path.subjects)
	}
	for i, s := range e.path.subjects {
		if s.Destination != nil || s.Connection == nil || *s.Connection != pressConn {
			t.Errorf("answer %d limited by %+v", i, s)
		}
	}
	fault(t, e.fake, "answerCallbackQuery", 429,
		`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`, 1)
	e.pressAs(t, bobTG, "Ack")
	if o := e.path.outcomes[len(e.path.outcomes)-1]; o.Kind != delivery.OutcomeRetryAfter ||
		o.Scope != delivery.ScopeConnection || o.RetryAfter != 7*time.Second {
		t.Errorf("a 429 on an answer = %+v", o)
	}
}

// The fixtures of Reminder presses: Alert Group B, whose Reminder is in the same discussion group.
const pressGroupB = "AG0000000000B1"

// reminderKeyboard is the keyboard of a Reminder of group signed by k: "Still on it" and "Unack" on one row.
func reminderKeyboard(t *testing.T, k buttons.Keys, group string) *inlineKeyboard {
	t.Helper()
	var bs []messages.Button
	for _, c := range []struct{ command, label string }{{buttons.CommandStillOnIt, "Still on it"},
		{buttons.CommandUnacknowledge, "Unack"}} {
		id, kid, err := buttons.Sign(k, buttons.Action{Subject: buttons.SubjectReply, PublicID: group,
			Command: c.command})
		if err != nil {
			t.Fatal(err)
		}
		bs = append(bs, messages.Button{Command: c.command, Label: c.label, ActionID: id, KeyID: kid})
	}
	return keyboard(bs)
}

// reminder sends a Reminder of group signed by k into the Thread of the channel post, binds it and returns its id.
func (e *pressEnv) reminder(t *testing.T, k buttons.Keys, group, lang string) int64 {
	t.Helper()
	id := e.send(t, faketelegram.GroupID, e.copyOf(t), reminderKeyboard(t, k, group))
	e.bindings.replies[replyKey{pressConn, faketelegram.GroupID, id, group}] = delivery.Binding{
		Destination: pressDest, ChannelID: "-1001000000002", Language: lang, SnoozeSeconds: []int64{3600}}
	return id
}

// pressIn presses the button label of the message id of the discussion group as from, with the button data of the
// message dataFrom when it is not 0, and returns the text of the answer.
func (e *pressEnv) pressIn(t *testing.T, id, dataFrom, from int64, label string) string {
	t.Helper()
	return e.handle(t, e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":%d,"data_from":%d,"from":{"id":%d},`+
		`"button":%q}`, faketelegram.GroupID, id, dataFrom, from, label)))
}

const (
	textNotVerified = "This button could not be verified; nothing was changed."
	textExpired     = "This button has expired; use the buttons on the Alert Group's message"
)

// TestReminderPress covers C-17.FR-10 and C-14.FR-4 on Reminders: "Still on it" from the Owner's linked account
// answers the Reminder as that User with the Transport telegram and is answered "Done: Still on it", "Unack"
// unacknowledges and is answered "Done: Unacknowledge", in the language of the Route; an unlinked account gets the
// link to the profile and runs nothing.
func TestReminderPress(t *testing.T) {
	e := newPressEnv(t)
	r := e.reminder(t, e.keys, pressGroup, "en")
	if got := e.pressIn(t, r, 0, bobTG, "Still on it"); got != "Done: Still on it" {
		t.Fatalf("still on it = %q", got)
	}
	c := e.commands.calls
	if len(c) != 1 || c[0].command != "still_on_it" || c[0].group != pressGroup ||
		c[0].caller.Transport != audit.TransportTelegram || c[0].caller.Actor != audit.User(7, "SR0000000000B1") {
		t.Fatalf("commands %+v", c)
	}
	if l := e.last(t, "telegram_press"); l["group"] != pressGroup || l["command"] != "still_on_it" ||
		l["outcome"] != "done" {
		t.Errorf("log %v", l)
	}
	if got := e.pressIn(t, r, 0, bobTG, "Unack"); got != "Done: Unacknowledge" ||
		e.commands.calls[1].command != "unacknowledge" {
		t.Errorf("unack = %q, %+v", got, e.commands.calls)
	}
	rb := e.reminder(t, e.keys, pressGroupB, "ru")
	if got := e.pressIn(t, rb, 0, bobTG, "Still on it"); got != "Готово: Ещё работаю" {
		t.Errorf("a Russian answer %q", got)
	}
	if got := e.pressIn(t, r, 0, carolTG, "Still on it"); !strings.HasPrefix(got,
		"Your Telegram account is not linked") || len(e.commands.calls) != 3 {
		t.Errorf("unlinked = %q, %+v", got, e.commands.calls)
	}
	e.commands.err = &groups.RefusedError{Code: groups.CodeNotOwner, Message: "only the Owner answers a Reminder"}
	if got := e.pressIn(t, r, 0, bobTG, "Still on it"); got != "Only the Owner answers a Reminder." ||
		e.last(t, "telegram_press")["outcome"] != "refused" {
		t.Errorf("refused = %q", got)
	}
}

// TestReminderPressBinding covers C-14.FR-4 on Reminders: a press must come from the Reminder's chat and message, and a
// Reminder carries only "Still on it" and Unack; the data of another Alert Group's Reminder, a Reminder's data on the
// channel post or on another message, a Root message's data on a Reminder and another Command of a Reminder are
// answered "This button could not be verified; nothing was changed." and run nothing.
func TestReminderPressBinding(t *testing.T) {
	e := newPressEnv(t)
	r := e.reminder(t, e.keys, pressGroup, "en")
	rb := e.reminder(t, e.keys, pressGroupB, "en")
	stillOnIt, resolve := e.replySigned(t, buttons.CommandStillOnIt), e.replySigned(t, buttons.CommandResolve)
	const copied = `{"chat":%d,"message_id":%d,"data_from":%d,"from":{"id":%d},"button":"Still on it"}`
	const data = `{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"x","data":%q}`
	for name, body := range map[string]string{
		"another alert group's reminder": fmt.Sprintf(copied, faketelegram.GroupID, r, rb, bobTG),
		"another message":                fmt.Sprintf(copied, faketelegram.GroupID, e.reply, r, bobTG),
		"the channel post":               fmt.Sprintf(data, faketelegram.ChannelID, e.post, bobTG, stillOnIt),
		"a root message's data":          fmt.Sprintf(data, faketelegram.GroupID, r, bobTG, e.signed(t, pressGroup)),
		"another command":                fmt.Sprintf(data, faketelegram.GroupID, r, bobTG, resolve),
	} {
		t.Run(name, func(t *testing.T) {
			if got := e.handle(t, e.press(t, body)); got != textNotVerified || len(e.commands.calls) != 0 ||
				e.last(t, "telegram_press")["outcome"] != "not_verified" {
				t.Errorf("answer %q, commands %+v", got, e.commands.calls)
			}
		})
	}
}

// replySigned is the data of a Reminder button of Alert Group A that runs command.
func (e *pressEnv) replySigned(t *testing.T, command string) string {
	t.Helper()
	id, _, err := buttons.Sign(e.keys, buttons.Action{Subject: buttons.SubjectReply, PublicID: pressGroup,
		Command: command})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestExpiredButton covers C-17.FR-6 with a Keyring that lacks the signing key: a press on a Reminder is answered "This
// button has expired; use the buttons on the Alert Group's message" in the language of the Route and runs nothing,
// while a Root message button signed by the same key keeps "This button could not be verified; nothing was changed.";
// an expired Reminder button on a message that is not that Reminder is not verified, and a failed binding is answered
// as a failure.
func TestExpiredButton(t *testing.T) {
	e := newPressEnv(t)
	gone := openKeyring(t, 'z')
	r := e.reminder(t, gone, pressGroup, "en")
	if got := e.pressIn(t, r, 0, bobTG, "Still on it"); got != textExpired {
		t.Fatalf("an expired Reminder = %q", got)
	}
	if l := e.last(t, "telegram_press"); l["outcome"] != "expired" || l["group"] != "" || l["command"] != "" {
		t.Errorf("log %v", l)
	}
	rr := e.reminder(t, gone, pressGroup, "ru")
	if got := e.pressIn(t, rr, 0, bobTG, "Unack"); got != "Срок действия этой кнопки истёк; используйте кнопки в "+
		"сообщении группы алертов" {
		t.Errorf("a Russian expiry = %q", got)
	}
	root := e.send(t, faketelegram.ChannelID, 0, e.keyboard(t, gone, buttons.SubjectRoot, pressGroup))
	if got := e.handle(t, e.press(t, fmt.Sprintf(`{"chat":%d,"message_id":%d,"from":{"id":%d},"button":"Ack"}`,
		faketelegram.ChannelID, root, bobTG))); got != textNotVerified {
		t.Errorf("an expired Root message button = %q", got)
	}
	if got := e.pressIn(t, e.reply, r, bobTG, "Still on it"); got != textNotVerified {
		t.Errorf("an expired Reminder button on another message = %q", got)
	}
	if len(e.commands.calls) != 0 {
		t.Fatalf("an expired press ran %+v", e.commands.calls)
	}
	e.bindings.err = errors.New("bindings down")
	if got := e.pressIn(t, r, 0, bobTG, "Still on it"); got != "Muster could not run this command; nothing was "+
		"changed." || e.last(t, "telegram_press")["error"] != "bindings down" {
		t.Errorf("a failed binding = %q", got)
	}
}
