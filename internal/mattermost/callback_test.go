// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/muster-io/muster/internal/fakes/fakemattermost"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
)

// The fixtures of the callback: Connection 3, whose Destinations 31 and 32 post into ch-alerts and ch-alerts-prod;
// Alert Group A posted as post-a in ch-alerts, Alert Group B as post-b, and post-c in ch-alerts-prod.
const (
	groupA = "AG0000000000A1"
	groupB = "AG0000000000B1"
	postA  = "post-a"
	postB  = "post-b"
	postC  = "post-c"
)

var (
	press0  = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	connID  = int64(3)
	destA   = delivery.Destination{ID: 31, PublicID: "DS0000000000D1", Type: delivery.TypeMattermost, Connection: &connID}
	destB   = delivery.Destination{ID: 32, PublicID: "DS0000000000D2", Type: delivery.TypeMattermost, Connection: &connID}
	roles   = auth.Roles{"responder": {groups.PermissionAcknowledge, groups.PermissionResolve, groups.PermissionSnooze}}
	bobUser = accountlinks.User{ID: 7, PublicID: "SR0000000000B1", Login: "bob", Name: "Bob", Role: "responder",
		Status: accountlinks.StatusActive}
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

func openKeyring(t *testing.T) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{'b'}, keyring.KeySize)}, false)
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

type fakeConnections struct {
	conn mattermost.Connection
	err  error
}

func (f *fakeConnections) Connection(_ context.Context, publicID string) (mattermost.Connection, error) {
	if f.err != nil {
		return mattermost.Connection{}, f.err
	}
	if publicID != f.conn.PublicID {
		return mattermost.Connection{}, mattermost.ErrNoConnection
	}
	return f.conn, nil
}

type bindingKey struct {
	conn        int64
	group, post string
}

// postKey is a post in a channel.
type postKey struct{ post, channel string }

type fakeBindings struct {
	bound        map[bindingKey]delivery.Binding
	posts        map[postKey]delivery.Destination
	err, postErr error
}

func (f *fakeBindings) PressBinding(_ context.Context, conn int64, group, post string) (delivery.Binding, error) {
	if f.err != nil {
		return delivery.Binding{}, f.err
	}
	b, ok := f.bound[bindingKey{conn, group, post}]
	if !ok {
		return delivery.Binding{}, delivery.ErrNotBound
	}
	return b, nil
}

func (f *fakeBindings) PostDestination(_ context.Context, conn int64, post, channel string) (delivery.Destination,
	bool, error) {
	if f.postErr != nil {
		return delivery.Destination{}, false, f.postErr
	}
	d, ok := f.posts[postKey{post, channel}]
	return d, ok && conn == connID, nil
}

type fakeLinks struct {
	users map[string]accountlinks.User
	space string
	err   error
}

func (f *fakeLinks) Lookup(_ context.Context, space, external string) (accountlinks.User, error) {
	f.space = space
	if f.err != nil {
		return accountlinks.User{}, f.err
	}
	u, ok := f.users[external]
	if !ok {
		return accountlinks.User{}, accountlinks.ErrNotLinked
	}
	return u, nil
}

// dispatched is a Command the callback ran.
type dispatched struct {
	command, group string
	caller         groups.Caller
	end            *groups.SnoozeEnd
	note           *string
}

type fakeCommands struct {
	calls  []dispatched
	result groups.Result
	err    error
	panics bool
}

func (f *fakeCommands) run(d dispatched) (groups.Result, error) {
	f.calls = append(f.calls, d)
	if f.panics {
		panic("token xoxb-secret")
	}
	return f.result, f.err
}

func (f *fakeCommands) Acknowledge(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(dispatched{command: "acknowledge", group: id, caller: c})
}

func (f *fakeCommands) Unacknowledge(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(dispatched{command: "unacknowledge", group: id, caller: c})
}

func (f *fakeCommands) Resolve(_ context.Context, c groups.Caller, id string, note *string) (groups.Result, error) {
	return f.run(dispatched{command: "resolve", group: id, caller: c, note: note})
}

func (f *fakeCommands) Snooze(_ context.Context, c groups.Caller, id string, end groups.SnoozeEnd) (groups.Result,
	error) {
	return f.run(dispatched{command: "snooze", group: id, caller: c, end: &end})
}

func (f *fakeCommands) Unsnooze(_ context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(dispatched{command: "unsnooze", group: id, caller: c})
}

type callbackEnv struct {
	fake     *fakemattermost.Fake
	keys     *keyring.Keyring
	conns    *fakeConnections
	bindings *fakeBindings
	links    *fakeLinks
	commands *fakeCommands
	log      *bytes.Buffer
	cfg      mattermost.CallbackConfig
	business *clock.Manual
	// admin is whether the fake's bot has the system admin role, with which its ephemeral posts are made (F-063).
	admin bool
}

func newCallback(t *testing.T) *callbackEnv {
	t.Helper()
	f := startFake(t)
	c := newClient(t, f.URL(), nil)
	e := &callbackEnv{fake: f, keys: openKeyring(t),
		conns: &fakeConnections{conn: mattermost.Connection{ID: connID, PublicID: connectionPublicID, Client: c}},
		bindings: &fakeBindings{
			bound: map[bindingKey]delivery.Binding{
				{connID, groupA, postA}: {Destination: destA, ChannelID: fakemattermost.ChannelAlerts, Language: "en",
					SnoozeSeconds: []int64{3600, 14400}},
				{connID, groupB, postB}: {Destination: destA, ChannelID: fakemattermost.ChannelAlerts, Language: "ru",
					SnoozeSeconds: []int64{3600}},
			},
			posts: map[postKey]delivery.Destination{{postA, fakemattermost.ChannelAlerts}: destA,
				{postB, fakemattermost.ChannelAlerts}: destA, {postC, fakemattermost.ChannelAlertsProd}: destB},
		},
		links:    &fakeLinks{users: map[string]accountlinks.User{fakemattermost.BobUserID: bobUser}},
		commands: &fakeCommands{result: groups.Result{Outcome: groups.OutcomeDone}},
		log:      &bytes.Buffer{}, business: clock.NewManual(press0)}
	e.cfg = mattermost.CallbackConfig{Connections: e.conns, Bindings: e.bindings, Links: e.links,
		Commands: e.commands, Roles: roles, Keys: e.keys,
		Path:     deliverytest.Unlimited(1, clock.Clocks{Business: e.business, Real: clock.NewManual(press0)}),
		Business: e.business, PublicURL: "http://localhost:8080/", Log: logging.New(e.log, logging.LevelInfo)}
	return e
}

// newCallbackAs is newCallback with the fake's bot a system admin when admin is true, and a Member otherwise.
func newCallbackAs(t *testing.T, admin bool) *callbackEnv {
	t.Helper()
	e := newCallback(t)
	e.admin = admin
	e.fake.SetBotSystemAdmin(admin)
	return e
}

// bots runs test once with a bot that has the system admin role and once with a Member bot (D284).
func bots(t *testing.T, test func(t *testing.T, e *callbackEnv)) {
	t.Helper()
	for _, admin := range []bool{true, false} {
		t.Run(map[bool]string{true: "admin bot", false: "member bot"}[admin], func(t *testing.T) {
			test(t, newCallbackAs(t, admin))
		})
	}
}

// ephemeralCalls are the calls of POST /api/v4/posts/ephemeral the fake received, and how it answered them.
func (e *callbackEnv) ephemeralCalls() []int {
	var out []int
	for _, r := range e.fake.Requests() {
		if r.Method == http.MethodPost && r.Path == "/api/v4/posts/ephemeral" {
			out = append(out, r.Status)
		}
	}
	return out
}

// action is a signed action id of the Root message of group.
func (e *callbackEnv) action(t *testing.T, s buttons.Subject, group, command string, arg int) (string, string) {
	t.Helper()
	id, kid, err := buttons.Sign(e.keys, buttons.Action{Subject: s, PublicID: group, Command: command, Argument: arg})
	if err != nil {
		t.Fatal(err)
	}
	return id, kid
}

// body is a MattermostActionRequest.
func body(user, channel, post, action, keyID string) string {
	b, _ := json.Marshal(map[string]any{"user_id": user, "user_name": "someone", "channel_id": channel,
		"team_id": fakemattermost.TeamID, "post_id": post, "trigger_id": "tr", "type": "button",
		"context": map[string]string{"action": action, "key_id": keyID}})
	return string(b)
}

// press sends a request to the callback of connection, as mounted on the callback mux, checks the answer that every
// request gets — 200 with a JSON object that is either {} or carries only ephemeral_text with skip_slack_parsing,
// never update (C-13.AC-14) — and returns its ephemeral_text.
func (e *callbackEnv) press(t *testing.T, method, connection, payload string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(mattermost.CallbackPattern, mattermost.NewCallback(e.cfg))
	req := httptest.NewRequestWithContext(t.Context(), method, mattermost.CallbackPath+connection,
		strings.NewReader(payload))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("answer %d %q %v, want 200 JSON", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec.Body.String() == "{}" {
		return ""
	}
	var a mattermost.PressAnswer
	dec := json.NewDecoder(strings.NewReader(rec.Body.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil || a.EphemeralText == "" || !a.SkipSlackParsing {
		t.Fatalf("answer %q (%v), want {} or ephemeral_text with skip_slack_parsing", rec.Body.String(), err)
	}
	return a.EphemeralText
}

// pressAs presses the button command of group on post in channel as user and returns the answer's ephemeral_text.
func (e *callbackEnv) pressAs(t *testing.T, user, channel, post, group, command string, arg int) string {
	t.Helper()
	id, kid := e.action(t, buttons.SubjectRoot, group, command, arg)
	return e.press(t, http.MethodPost, connectionPublicID, body(user, channel, post, id, kid))
}

// last is the last mattermost_press line.
func (e *callbackEnv) last(t *testing.T) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(e.log.String()), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil || m["event"] != "mattermost_press" {
		t.Fatalf("last line %q: %v", lines[len(lines)-1], err)
	}
	return m
}

func (e *callbackEnv) expectLine(t *testing.T, group, command, outcome string) map[string]any {
	t.Helper()
	m := e.last(t)
	if m["level"] != "INFO" || m["group"] != group || m["command"] != command || m["outcome"] != outcome {
		t.Errorf("line %v, want group %q, command %q, outcome %q", m, group, command, outcome)
	}
	return m
}

// expectAnswer checks that the callback's answer carries the ephemeral_text want, "" for none.
func expectAnswer(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("ephemeral_text %q, want %q", got, want)
	}
}

// expectAnswered checks that the person who pressed read message exactly once more (D284): with an admin bot as an
// ephemeral post in the channel view of channel and an empty answer, logged as ephemeral_post; with a Member bot,
// whose ephemeral post the fake refuses with 403, as the answer's ephemeral_text (got), logged as ephemeral_text
// without an error.
func (e *callbackEnv) expectAnswered(t *testing.T, got, user, channel, message string) {
	t.Helper()
	calls := e.ephemeralCalls()
	eph := e.fake.Ephemeral()
	if len(calls) == 0 {
		t.Fatalf("no ephemeral post was tried")
	}
	m := e.last(t)
	if m["error"] != nil {
		t.Errorf("an answered press logged the error %v", m["error"])
	}
	if !e.admin {
		expectAnswer(t, got, message)
		if calls[len(calls)-1] != http.StatusForbidden || len(eph) != 0 || m["answer"] != "ephemeral_text" {
			t.Errorf("calls %v, ephemeral posts %+v, line %v", calls, eph, m)
		}
		return
	}
	expectAnswer(t, got, "")
	if calls[len(calls)-1] != http.StatusCreated || len(eph) != len(calls) || m["answer"] != "ephemeral_post" {
		t.Fatalf("calls %v, ephemeral posts %+v, line %v", calls, eph, m)
	}
	last := eph[len(eph)-1]
	if last.UserID != user || last.ChannelID != channel || last.RootID != "" ||
		last.ShownIn != fakemattermost.ShownInChannel || last.Message != message {
		t.Errorf("ephemeral %+v, want %q to %s in %s", last, message, user, channel)
	}
}

// expectUnanswered checks that a request got an empty answer and no ephemeral post was tried since before of them.
func (e *callbackEnv) expectUnanswered(t *testing.T, got string, before int) {
	t.Helper()
	expectAnswer(t, got, "")
	if calls := e.ephemeralCalls(); len(calls) != before {
		t.Errorf("ephemeral posts tried %v, want %d", calls, before)
	}
	if m := e.last(t); m["answer"] != nil {
		t.Errorf("line %v", m)
	}
}

// tamper changes the end of an action id.
func tamper(id string) string {
	if strings.HasSuffix(id, "AA") {
		return id[:len(id)-2] + "BB"
	}
	return id[:len(id)-2] + "AA"
}

const (
	textNotVerified = "This button could not be verified; nothing was changed."
	textNotLinked   = "Your Mattermost account is not linked to Muster. Link it in your profile: " +
		"http://localhost:8080/profile"
)

// TestPressRunsCommand is C-13.FR-4 and AC-14: a press from a linked Responder runs the Command as that User with the
// Transport mattermost — a Snooze for the pressed duration of the Route — answers {} and sends nothing else.
func TestPressRunsCommand(t *testing.T) {
	e := newCallbackAs(t, true)
	for _, c := range []struct {
		command string
		arg     int
	}{{buttons.CommandAcknowledge, 0}, {buttons.CommandUnacknowledge, 0}, {buttons.CommandResolve, 0},
		{buttons.CommandUnsnooze, 0}, {buttons.CommandSnooze, 1}} {
		e.expectUnanswered(t, e.pressAs(t, fakemattermost.BobUserID, fakemattermost.ChannelAlerts, postA, groupA,
			c.command, c.arg), 0)
		d := e.commands.calls[len(e.commands.calls)-1]
		if d.command != c.command || d.group != groupA || d.caller.Actor != audit.User(7, "SR0000000000B1") ||
			d.caller.Transport != audit.TransportMattermost ||
			!slices.Equal(d.caller.Permissions, roles.Permissions("responder")) || d.note != nil {
			t.Errorf("%s dispatched %+v", c.command, d)
		}
		e.expectLine(t, groupA, c.command, "done")
	}
	if len(e.commands.calls) != 5 || e.links.space != "mattermost:3" {
		t.Fatalf("calls %+v in %s", e.commands.calls, e.links.space)
	}
	if end := e.commands.calls[4].end; end == nil || end.NoEnd || end.Until == nil ||
		!end.Until.Equal(press0.Add(4*time.Hour)) {
		t.Errorf("the Snooze end %+v, want 4 h after the press", end)
	}
	e.commands.result = groups.Result{Outcome: groups.OutcomeUnchanged}
	e.expectUnanswered(t, e.pressAs(t, fakemattermost.BobUserID, fakemattermost.ChannelAlerts, postA, groupA,
		buttons.CommandAcknowledge, 0), 0)
	e.expectLine(t, groupA, buttons.CommandAcknowledge, "unchanged")
	if len(e.fake.Requests()) != 0 || strings.Contains(e.log.String(), `"error"`) {
		t.Errorf("requests %+v, log %s", e.fake.Requests(), e.log)
	}
}

// TestPressRefusals is C-13.AC-4 and C-18.FR-8: an account without an Account link, a disabled User, a User without
// the Permission and a refused Command change nothing more and are answered once each, in the language of the Route:
// in the channel view with an admin bot, in the answer's ephemeral_text with a Member bot (D284).
func TestPressRefusals(t *testing.T) {
	bots(t, func(t *testing.T, e *callbackEnv) {
		e.expectAnswered(t, e.pressAs(t, fakemattermost.AliceUserID, fakemattermost.ChannelAlerts, postA, groupA,
			buttons.CommandAcknowledge, 0), fakemattermost.AliceUserID, fakemattermost.ChannelAlerts, textNotLinked)
		e.expectLine(t, groupA, buttons.CommandAcknowledge, "not_linked")
		if len(e.commands.calls) != 0 {
			t.Fatalf("an unlinked press ran %+v", e.commands.calls)
		}
		e.expectAnswered(t, e.pressAs(t, fakemattermost.AliceUserID, fakemattermost.ChannelAlerts, postB, groupB,
			buttons.CommandAcknowledge, 0), fakemattermost.AliceUserID, fakemattermost.ChannelAlerts,
			"Ваша учётная запись Mattermost не связана с Muster. Свяжите её в профиле: http://localhost:8080/profile")

		disabled := bobUser
		disabled.Status = accountlinks.StatusDisabled
		e.links.users[fakemattermost.BobUserID] = disabled
		e.expectAnswered(t, e.pressAs(t, fakemattermost.BobUserID, fakemattermost.ChannelAlerts, postA, groupA,
			buttons.CommandResolve, 0), fakemattermost.BobUserID, fakemattermost.ChannelAlerts,
			"Your Muster account is disabled")
		e.expectLine(t, groupA, buttons.CommandResolve, "disabled")
		if len(e.commands.calls) != 0 {
			t.Fatalf("a disabled User's press ran %+v", e.commands.calls)
		}
		e.links.users[fakemattermost.BobUserID] = bobUser

		for _, c := range []struct {
			err     error
			text    string
			outcome string
		}{
			{&groups.ForbiddenError{Permission: groups.PermissionResolve}, "You are not permitted to do this",
				"forbidden"},
			{&groups.RefusedError{Code: groups.CodeAlreadyResolved, Message: "already resolved"}, "Already resolved.",
				"refused"},
		} {
			e.commands.err = c.err
			e.expectAnswered(t, e.pressAs(t, fakemattermost.BobUserID, fakemattermost.ChannelAlerts, postA, groupA,
				buttons.CommandResolve, 0), fakemattermost.BobUserID, fakemattermost.ChannelAlerts, c.text)
			e.expectLine(t, groupA, buttons.CommandResolve, c.outcome)
		}
		for _, fail := range []func(){
			func() { e.commands.err = errors.New("the database is down") },
			func() { e.commands.err, e.links.err = nil, errors.New("the database is down") },
		} {
			fail()
			got := e.pressAs(t, fakemattermost.BobUserID, fakemattermost.ChannelAlerts, postA, groupA,
				buttons.CommandResolve, 0)
			m := e.expectLine(t, groupA, buttons.CommandResolve, "failed")
			if m["error"] != "the database is down" ||
				m["answer"] != map[bool]string{true: "ephemeral_post", false: "ephemeral_text"}[e.admin] {
				t.Errorf("line %v", m)
			}
			want := map[bool]string{true: "", false: "Muster could not run this command; nothing was changed."}
			expectAnswer(t, got, want[e.admin])
		}
	})
}

// TestPressNotVerified is C-13.AC-2 and AC-11: a changed action id, an action id of one Alert Group with the post of
// another, a Thread reply's button and a Snooze duration the Route no longer has change nothing and are answered
// "This button could not be verified; nothing was changed." — but only on a post Muster delivered to a Destination of
// the Connection in the channel of the press, so that a forged callback makes the bot post nowhere else, spends no
// limiter token on a made-up post and gets {}.
func TestPressNotVerified(t *testing.T) {
	bots(t, func(t *testing.T, e *callbackEnv) {
		id, kid := e.action(t, buttons.SubjectRoot, groupA, buttons.CommandAcknowledge, 0)
		tampered := tamper(id)
		check := func(name, answer, channel, group, command string) {
			t.Helper()
			t.Run(name, func(t *testing.T) {
				e.expectAnswered(t, answer, fakemattermost.BobUserID, channel, textNotVerified)
				e.expectLine(t, group, command, "not_verified")
			})
		}
		alerts := fakemattermost.ChannelAlerts
		check("tampered", e.press(t, http.MethodPost, connectionPublicID, body(fakemattermost.BobUserID, alerts,
			postA, tampered, kid)), alerts, "", "")
		check("another key id", e.press(t, http.MethodPost, connectionPublicID, body(fakemattermost.BobUserID,
			alerts, postA, id, "k-ffffffffffffffff")), alerts, "", "")
		check("another post", e.pressAs(t, fakemattermost.BobUserID, alerts, postB, groupA,
			buttons.CommandAcknowledge, 0), alerts, groupA, buttons.CommandAcknowledge)
		reply, rkid := e.action(t, buttons.SubjectReply, groupA, buttons.CommandAcknowledge, 0)
		check("a Thread reply", e.press(t, http.MethodPost, connectionPublicID, body(fakemattermost.BobUserID,
			alerts, postA, reply, rkid)), alerts, "", "")
		check("a Snooze duration out of range", e.pressAs(t, fakemattermost.BobUserID, alerts, postA, groupA,
			buttons.CommandSnooze, 2), alerts, groupA, buttons.CommandSnooze)
		check("a Command of Thread replies", e.pressAs(t, fakemattermost.BobUserID, alerts, postA, groupA,
			buttons.CommandStillOnIt, 0), alerts, groupA, buttons.CommandStillOnIt)
		check("another Destination", e.press(t, http.MethodPost, connectionPublicID, body(fakemattermost.BobUserID,
			fakemattermost.ChannelAlertsProd, postC, tampered, kid)), fakemattermost.ChannelAlertsProd, "", "")

		n := len(e.ephemeralCalls())
		e.expectUnanswered(t, e.press(t, http.MethodPost, connectionPublicID, body(fakemattermost.BobUserID,
			"ch-foreign", postA, tampered, kid)), n)
		e.expectUnanswered(t, e.pressAs(t, fakemattermost.BobUserID, "ch-foreign", postA, groupA,
			buttons.CommandAcknowledge, 0), n)
		e.expectUnanswered(t, e.pressAs(t, fakemattermost.BobUserID, fakemattermost.ChannelAlertsProd, postA, groupA,
			buttons.CommandAcknowledge, 0), n)
		e.expectLine(t, groupA, buttons.CommandAcknowledge, "not_verified")
		if len(e.commands.calls) != 0 {
			t.Fatalf("an unverified press ran %+v", e.commands.calls)
		}
		e.bindings.postErr = errors.New("post read failed")
		e.expectUnanswered(t, e.press(t, http.MethodPost, connectionPublicID, body(fakemattermost.BobUserID, alerts,
			postA, tampered, kid)), n)
		if m := e.expectLine(t, "", "", "not_verified"); m["error"] != "post read failed" {
			t.Errorf("line %v", m)
		}
		for _, line := range strings.Split(strings.TrimSpace(e.log.String()), "\n") {
			if strings.Contains(line, tampered) || strings.Contains(line, id) || strings.Contains(line, kid) {
				t.Errorf("an action id or a key id reached the log: %s", line)
			}
		}
	})
}

// TestPressBadRequests is C-13.AC-2: a callback for a Connection that does not exist, a body that is not JSON, does
// not match MattermostActionRequest or is too long, and another method are answered 200 {} and change nothing.
func TestPressBadRequests(t *testing.T) {
	e := newCallbackAs(t, true)
	id, kid := e.action(t, buttons.SubjectRoot, groupA, buttons.CommandAcknowledge, 0)
	good := body(fakemattermost.BobUserID, fakemattermost.ChannelAlerts, postA, id, kid)
	e.expectUnanswered(t, e.press(t, http.MethodPost, "CN000000000000", good), 0)
	if m := e.expectLine(t, "", "", "unknown_connection"); m["connection"] != "" {
		t.Errorf("line %v", m)
	}
	for _, payload := range []string{"not json", `[]`, `{"user_id":"u-bob"}`, `{"user_id":1}`,
		body(fakemattermost.BobUserID, fakemattermost.ChannelAlerts, postA, "", kid)} {
		e.expectUnanswered(t, e.press(t, http.MethodPost, connectionPublicID, payload), 0)
		e.expectLine(t, "", "", "invalid_request")
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		e.expectUnanswered(t, e.press(t, method, connectionPublicID, good), 0)
		e.expectLine(t, "", "", "invalid_request")
	}
	e.cfg.BodyLimit = 16
	e.expectUnanswered(t, e.press(t, http.MethodPost, connectionPublicID, good), 0)
	if m := e.expectLine(t, "", "", "invalid_request"); m["error"] != "the body could not be read" {
		t.Errorf("line %v", m)
	}
	e.cfg.BodyLimit = 0
	e.conns.err = errors.New("connection read failed")
	e.expectUnanswered(t, e.press(t, http.MethodPost, connectionPublicID, good), 0)
	if m := e.expectLine(t, "", "", "failed"); m["error"] != "connection read failed" {
		t.Errorf("line %v", m)
	}
	e.conns.err = nil
	e.bindings.err = errors.New("binding read failed")
	e.expectUnanswered(t, e.press(t, http.MethodPost, connectionPublicID, good), 0)
	if m := e.expectLine(t, groupA, buttons.CommandAcknowledge, "failed"); m["error"] != "binding read failed" ||
		m["connection"] != connectionPublicID {
		t.Errorf("line %v", m)
	}
	if len(e.commands.calls) != 0 || len(e.fake.Requests()) != 0 {
		t.Fatalf("bad requests ran %+v and sent %+v", e.commands.calls, e.fake.Requests())
	}
}

// TestPressAnswerFallsBack is D284: an ephemeral post that fails in any way other than the 403 of a Member bot — a
// 5xx, no limiter token within the interactive budget, an answer slower than the budget — is logged, and its text
// goes into the answer's ephemeral_text, so that the person still reads it.
func TestPressAnswerFallsBack(t *testing.T) {
	e := newCallbackAs(t, true)
	pressAlice := func() string {
		return e.pressAs(t, fakemattermost.AliceUserID, fakemattermost.ChannelAlerts, postA, groupA,
			buttons.CommandAcknowledge, 0)
	}
	fault(t, e.fake, fakeserver.Fault{Path: "/api/v4/posts/ephemeral", Status: 500, Times: 1})
	expectAnswer(t, pressAlice(), textNotLinked)
	if m := e.expectLine(t, groupA, buttons.CommandAcknowledge, "not_linked"); m["answer"] != "ephemeral_text" ||
		!strings.HasPrefix(fmt.Sprint(m["error"]), "the ephemeral post was not sent: ") {
		t.Errorf("line %v", m)
	}
	fault(t, e.fake, fakeserver.Fault{Path: "/api/v4/posts/ephemeral", Status: 200, DelayMs: 2000, Times: 1})
	e.cfg.Budget = 50 * time.Millisecond
	expectAnswer(t, pressAlice(), textNotLinked)
	if m := e.expectLine(t, groupA, buttons.CommandAcknowledge, "not_linked"); m["answer"] != "ephemeral_text" ||
		!strings.HasPrefix(fmt.Sprint(m["error"]), "the ephemeral post was not sent: ") {
		t.Errorf("line %v", m)
	}
	e.cfg.Budget = 0
	limited := &limitedPath{}
	e.cfg.Path = limited
	expectAnswer(t, pressAlice(), textNotLinked)
	if m := e.expectLine(t, groupA, buttons.CommandAcknowledge, "not_linked"); m["answer"] != "ephemeral_text" ||
		!strings.Contains(fmt.Sprint(m["error"]), "interactive budget") {
		t.Errorf("line %v", m)
	}
	id, kid := e.action(t, buttons.SubjectRoot, groupA, buttons.CommandAcknowledge, 0)
	expectAnswer(t, e.press(t, http.MethodPost, connectionPublicID, body(fakemattermost.AliceUserID,
		fakemattermost.ChannelAlerts, postA, tamper(id), kid)), textNotVerified)
	if m := e.expectLine(t, "", "", "not_verified"); !strings.Contains(fmt.Sprint(m["error"]), "interactive budget") {
		t.Errorf("line %v", m)
	}
	if len(e.fake.Ephemeral()) != 0 || limited.calls != 2 {
		t.Errorf("ephemeral posts %+v, limited calls %d", e.fake.Ephemeral(), limited.calls)
	}
}

// TestPressMadeUpPost is the other half of C-13.AC-2: a press with a made-up post id in the channel of a Destination of
// the Connection is refused without an answer, whether its action id is tampered with or valid, and only logged; the
// same tampered id on a real Root message gets the answer.
func TestPressMadeUpPost(t *testing.T) {
	bots(t, func(t *testing.T, e *callbackEnv) {
		id, kid := e.action(t, buttons.SubjectRoot, groupA, buttons.CommandAcknowledge, 0)
		for _, action := range []string{tamper(id), id} {
			e.expectUnanswered(t, e.press(t, http.MethodPost, connectionPublicID, body(fakemattermost.BobUserID,
				fakemattermost.ChannelAlerts, "garbage-post", action, kid)), 0)
			if m := e.last(t); m["outcome"] != "not_verified" || m["error"] != nil {
				t.Errorf("line %v", m)
			}
		}
		e.expectAnswered(t, e.press(t, http.MethodPost, connectionPublicID, body(fakemattermost.BobUserID,
			fakemattermost.ChannelAlerts, postA, tamper(id), kid)), fakemattermost.BobUserID,
			fakemattermost.ChannelAlerts, textNotVerified)
		if len(e.commands.calls) != 0 {
			t.Fatalf("an unverified press ran %+v", e.commands.calls)
		}
	})
}

// TestPressPanics: a panic while a press is handled is a failed press, logged without the panic's value, and still
// answered 200 {}.
func TestPressPanics(t *testing.T) {
	e := newCallbackAs(t, true)
	e.commands.panics = true
	e.expectUnanswered(t, e.pressAs(t, fakemattermost.BobUserID, fakemattermost.ChannelAlerts, postA, groupA,
		buttons.CommandAcknowledge, 0), 0)
	if m := e.expectLine(t, "", "", "failed"); m["error"] != "the press failed unexpectedly" {
		t.Errorf("line %v", m)
	}
	if len(e.commands.calls) != 1 || len(e.fake.Requests()) != 0 || strings.Contains(e.log.String(), "xoxb") {
		t.Errorf("calls %+v, requests %+v, log %s", e.commands.calls, e.fake.Requests(), e.log)
	}
}
