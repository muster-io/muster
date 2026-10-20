// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// The keys of the press tests, whose ids differ in their first bytes, as action ids name a key by them.
const (
	pressKeyA = "k-0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"
	pressKeyB = "k-0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"
)

// pressKeys sign and verify action ids with HMAC-SHA256 under named keys, as the Keyring does with its active key; a
// dropped key has left the Keyring, so that the action ids it signed are expired.
type pressKeys struct {
	mu     sync.Mutex
	active string
	keys   map[string][]byte
}

func newPressKeys() *pressKeys {
	return &pressKeys{active: pressKeyA, keys: map[string][]byte{pressKeyA: []byte("key a"), pressKeyB: []byte("key b")}}
}

func (k *pressKeys) mac(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

func (k *pressKeys) Sign(_ keyring.Purpose, msg []byte) ([]byte, string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.mac(k.keys[k.active], msg), k.active, nil
}

func (k *pressKeys) VerifyPrefix(_ keyring.Purpose, keyID string, msg, prefix []byte) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key, ok := k.keys[keyID]
	if !ok {
		return false, errors.New("no such key")
	}
	return hmac.Equal(k.mac(key, msg)[:len(prefix)], prefix), nil
}

func (k *pressKeys) KeyIDs() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Sorted(maps.Keys(k.keys))
}

func (k *pressKeys) use(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.active = id
}

func (k *pressKeys) drop(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, id)
}

// The fixtures of presses: in the channel C0ALERTS of the Connection 3, the Root message 1.000001 of Alert Group A
// (internal id 41) in the Slack Destination 31, its Reminder 1.000003, and the Root message 1.000005 of Alert Group B;
// the Route in English with the Snooze durations of 1 h, 4 h and 24 h; the Destination T1 on C0ALERTS, whose test
// message a person may press.
const (
	pressGroup      = "AG0000000000A1"
	otherGroup      = "AG0000000000B2"
	pressChannel    = "C0ALERTS"
	pressRootTS     = "1.000001"
	pressReminderTS = "1.000003"
	otherRootTS     = "1.000005"
	pressTestDest   = "DS0000000000T1"
	pressAlertGroup = int64(41)
)

var (
	pressConnID = int64(3)
	pressDest   = delivery.Destination{ID: 31, PublicID: "DS0000000000S1", Name: "alerts", Type: delivery.TypeSlack,
		Connection: &pressConnID}
	pressRoles = auth.Roles{auth.RoleResponder: {groups.PermissionAcknowledge, groups.PermissionResolve,
		groups.PermissionSnooze}, auth.RoleViewer: {"alert-groups:read"}}
	pressUsers = map[string]accountlinks.User{
		"U0BOB": {ID: 7, PublicID: "SR0000000000B1", Login: "bob", Name: "Bob Brown", Role: auth.RoleResponder,
			Status: accountlinks.StatusActive},
		"U0VERA": {ID: 8, PublicID: "SR0000000000V1", Login: "vera", Name: "Vera Viewer", Role: auth.RoleViewer,
			Status: accountlinks.StatusActive},
		"U0DORA": {ID: 9, PublicID: "SR0000000000D1", Login: "dora", Name: "Dora", Role: auth.RoleResponder,
			Status: accountlinks.StatusDisabled},
		"U0IVAN": {ID: 10, PublicID: "SR0000000000I1", Login: "ivan", Name: "Иван", Role: auth.RoleResponder,
			Status: accountlinks.StatusActive, Language: "ru"},
	}
	pressNow = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)
)

// pressBindings bind the messages of the Connection 3 by channel and ts, and the test messages by Destination and
// channel; err and testErr fail the reads.
type pressBindings struct {
	mu      sync.Mutex
	bound   map[string]delivery.SlackBinding
	tests   map[string]delivery.Destination
	err     error
	testErr error
}

func newPressBindings() *pressBindings {
	root := delivery.SlackBinding{Kind: buttons.SubjectRoot, GroupPublicID: pressGroup, AlertGroup: pressAlertGroup,
		Destination: pressDest, Language: "en", SnoozeSeconds: []int64{3600, 14400, 86400}}
	reminder, other := root, root
	reminder.Kind, reminder.RootTS = buttons.SubjectReply, pressRootTS
	other.GroupPublicID, other.AlertGroup = otherGroup, 42
	test := delivery.Destination{ID: 32, PublicID: pressTestDest, Name: "test", Type: delivery.TypeSlack,
		Connection: &pressConnID}
	return &pressBindings{bound: map[string]delivery.SlackBinding{pressChannel + "/" + pressRootTS: root,
		pressChannel + "/" + pressReminderTS: reminder, pressChannel + "/" + otherRootTS: other},
		tests: map[string]delivery.Destination{pressTestDest + "/" + pressChannel: test}}
}

func (b *pressBindings) SlackPressBinding(_ context.Context, connectionID int64, channelID, ts string) (
	delivery.SlackBinding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return delivery.SlackBinding{}, b.err
	}
	bound, ok := b.bound[channelID+"/"+ts]
	if !ok || connectionID != pressConnID {
		return delivery.SlackBinding{}, delivery.ErrNotBound
	}
	return bound, nil
}

func (b *pressBindings) SlackTestDestination(_ context.Context, connectionID int64, publicID, channelID string) (
	delivery.Destination, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.testErr != nil {
		return delivery.Destination{}, false, b.testErr
	}
	d, ok := b.tests[publicID+"/"+channelID]
	return d, ok && connectionID == pressConnID, nil
}

// pressLinks are the Account links of the Connection 3; err fails every lookup.
type pressLinks struct {
	mu     sync.Mutex
	users  map[string]accountlinks.User
	err    error
	spaces []string
}

func (l *pressLinks) Lookup(_ context.Context, space, externalID string) (accountlinks.User, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.spaces = append(l.spaces, space)
	if l.err != nil {
		return accountlinks.User{}, l.err
	}
	u, ok := l.users[externalID]
	if !ok || space != accountlinks.SpaceSlack(pressConnID) {
		return accountlinks.User{}, accountlinks.ErrNotLinked
	}
	return u, nil
}

// ran is a Command a press ran.
type ran struct {
	command, group string
	caller         groups.Caller
	end            *groups.SnoozeEnd
}

// pressCommands is the dispatcher as presses see it: a caller without the Command's Permission is forbidden, err
// replaces every other answer, unchanged makes a Command that ran change nothing, and hook runs inside each Command.
type pressCommands struct {
	mu        sync.Mutex
	calls     []ran
	err       error
	unchanged bool
	hook      func(ctx context.Context)
}

func (f *pressCommands) run(ctx context.Context, r ran) (groups.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, r)
	err, hook, unchanged := f.err, f.hook, f.unchanged
	f.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	if p := groups.Command(r.command).Permission(); !slices.Contains(r.caller.Permissions, p) {
		return groups.Result{}, &groups.ForbiddenError{Permission: p}
	}
	if err != nil {
		return groups.Result{}, err
	}
	if unchanged {
		return groups.Result{Outcome: groups.OutcomeUnchanged}, nil
	}
	return groups.Result{Outcome: groups.OutcomeDone, Group: groups.View{PublicID: r.group, Number: 1}}, nil
}

func (f *pressCommands) ran() []ran {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *pressCommands) Acknowledge(ctx context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(ctx, ran{command: buttons.CommandAcknowledge, group: id, caller: c})
}

func (f *pressCommands) Unacknowledge(ctx context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(ctx, ran{command: buttons.CommandUnacknowledge, group: id, caller: c})
}

func (f *pressCommands) Resolve(ctx context.Context, c groups.Caller, id string, _ *string) (groups.Result, error) {
	return f.run(ctx, ran{command: buttons.CommandResolve, group: id, caller: c})
}

func (f *pressCommands) Snooze(ctx context.Context, c groups.Caller, id string, end groups.SnoozeEnd) (groups.Result,
	error) {
	return f.run(ctx, ran{command: buttons.CommandSnooze, group: id, caller: c, end: &end})
}

func (f *pressCommands) Unsnooze(ctx context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(ctx, ran{command: buttons.CommandUnsnooze, group: id, caller: c})
}

func (f *pressCommands) AnswerReminder(ctx context.Context, c groups.Caller, id string) (groups.Result, error) {
	return f.run(ctx, ran{command: buttons.CommandStillOnIt, group: id, caller: c})
}

// pressPath is the interactive path of the press tests: it records the subject, the kind of operation and the deadline
// of each call and makes it as deliverytest.Unlimited does; limited answers a call limited by a Connection with no
// token within the budget, and err fails every call.
type pressPath struct {
	path *delivery.Interactive

	mu        sync.Mutex
	subjects  []delivery.Subject
	ops       []string
	deadlines []time.Time
	limited   bool
	err       error
}

func (p *pressPath) Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error) {
	p.mu.Lock()
	p.subjects = append(p.subjects, s)
	p.ops = append(p.ops, fmt.Sprintf("%T", op))
	deadline, _ := ctx.Deadline()
	p.deadlines = append(p.deadlines, deadline)
	limited, err := p.limited && s.Connection != nil, p.err
	p.mu.Unlock()
	switch {
	case err != nil:
		return delivery.Outcome{}, err
	case limited:
		return delivery.Outcome{}, &delivery.LimitedError{RetryAfter: time.Second}
	}
	return p.path.Do(ctx, s, op)
}

func (p *pressPath) calls() []delivery.Subject {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.subjects)
}

// pressEnv is a Presses of the Connection 3, whose client calls a stand-in Slack: chat.postEphemeral answers with
// ephemeral, the press's response_url — on the stand-in, as under `muster dev` — with hook.
type pressEnv struct {
	presses  *Presses
	conn     Conn
	slack    *standIn
	keys     *pressKeys
	bindings *pressBindings
	links    *pressLinks
	commands *pressCommands
	path     *pressPath
	log      *lockedBuffer
	rec      *records
	business *clock.Manual

	mu        sync.Mutex
	ephemeral http.HandlerFunc
	hook      http.HandlerFunc
}

func newPressEnv(t *testing.T) *pressEnv {
	t.Helper()
	e := &pressEnv{keys: newPressKeys(), bindings: newPressBindings(), links: &pressLinks{users: pressUsers},
		commands: &pressCommands{}, log: &lockedBuffer{}, rec: &records{}, business: clock.NewManual(pressNow),
		ephemeral: answerJSON(http.StatusOK, `{"ok":true,"message_ts":"1.000100"}`),
		hook:      answerJSON(http.StatusOK, `{"ok":true}`)}
	e.slack = startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		ephemeral, hook := e.ephemeral, e.hook
		e.mu.Unlock()
		switch {
		case r.URL.Path == "/api/chat.postEphemeral":
			ephemeral(w, r)
		case strings.HasPrefix(r.URL.Path, "/actions/"):
			hook(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	n := network(t, nil)
	n.Recorder = e.rec
	e.conn = Conn{ID: pressConnID, PublicID: "CN0000000000S1", Mode: EventModeHTTP,
		Client: newClient(t, n, Settings{BotToken: testBot, SigningSecret: testSigning, Route: to(e.slack.base()),
			Owner: outbound.Owner{Connection: pressConnID, PublicID: "CN0000000000S1"}})}
	e.path = &pressPath{path: deliverytest.Unlimited(1, clock.Clocks{Business: e.business, Real: e.business})}
	e.presses = &Presses{Bindings: e.bindings, Links: e.links, Commands: e.commands, Roles: pressRoles, Keys: e.keys,
		Path: e.path, Budget: 2 * time.Second, Business: e.business, PublicURL: "http://localhost:8080/",
		Log: logging.New(e.log, logging.LevelInfo)}
	return e
}

// answerWith sets how the stand-in answers chat.postEphemeral and the response_url.
func (e *pressEnv) answerWith(ephemeral, hook http.HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ephemeral != nil {
		e.ephemeral = ephemeral
	}
	if hook != nil {
		e.hook = hook
	}
}

// responseURL is the response_url of the presses: an address of the stand-in, as the fake server's under muster dev.
func (e *pressEnv) responseURL() string {
	return e.slack.srv.URL + "/actions/T0FAKE/1/0123456789abcdef01234567"
}

// sign is the signed action id of a.
func (e *pressEnv) sign(t *testing.T, a buttons.Action) string {
	t.Helper()
	id, _, err := buttons.Sign(e.keys, a)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// act is a press of the Command on the Root message of Alert Group A.
func (e *pressEnv) act(t *testing.T, command string, argument int) string {
	t.Helper()
	return e.sign(t, buttons.Action{Subject: buttons.SubjectRoot, PublicID: pressGroup, Command: command,
		Argument: argument})
}

// remind is a press of the Command on the Reminder of Alert Group A.
func (e *pressEnv) remind(t *testing.T, command string) string {
	t.Helper()
	return e.sign(t, buttons.Action{Subject: buttons.SubjectReply, PublicID: pressGroup, Command: command})
}

// pressIn is a press as the payload of F-093 carries it; menu sends the value as the chosen option of the Snooze menu.
type pressIn struct {
	typ, user, channel, ts, value, responseURL string
	menu                                       bool
	noActions                                  bool
}

func (e *pressEnv) payload(t *testing.T, p pressIn) []byte {
	t.Helper()
	action := map[string]any{"action_id": "ack", "block_id": "muster_actions", "type": "button", "value": p.value,
		"action_ts": "1760952600.000001"}
	if p.menu {
		action = map[string]any{"action_id": "snooze", "block_id": "muster_actions", "type": "static_select",
			"selected_option": map[string]any{"text": map[string]string{"type": "plain_text", "text": "4h"},
				"value": p.value}, "action_ts": "1760952600.000001"}
	}
	in := map[string]any{"type": cmp.Or(p.typ, typeBlockActions), "token": "verification-token",
		"team": map[string]string{"id": "T0FAKE"}, "api_app_id": "A0FAKE",
		"user": map[string]string{"id": p.user, "username": "someone", "team_id": "T0FAKE"},
		"container": map[string]any{"type": "message_attachment", "message_ts": p.ts, "channel_id": p.channel,
			"is_ephemeral": false},
		"channel": map[string]string{"id": p.channel, "name": "alerts"}, "trigger_id": "1.2.3",
		"response_url": cmp.Or(p.responseURL, e.responseURL()), "actions": []any{action}}
	if p.noActions {
		in["actions"] = []any{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// press hands the press p of Bob, on the Root message of Alert Group A unless p says otherwise, to Handle and returns
// the slack_press line it logged.
func (e *pressEnv) press(t *testing.T, p pressIn) map[string]any {
	t.Helper()
	p.user, p.channel, p.ts = cmp.Or(p.user, "U0BOB"), cmp.Or(p.channel, pressChannel), cmp.Or(p.ts, pressRootTS)
	before := len(e.log.lines(t, "slack_press"))
	e.presses.Handle(t.Context(), e.conn, e.payload(t, p))
	lines := e.log.lines(t, "slack_press")
	if len(lines) != before+1 {
		t.Fatalf("lines %v", lines)
	}
	return lines[before]
}

// ephemerals are the bodies of the requests to chat.postEphemeral, decoded.
func (e *pressEnv) ephemerals(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, s := range e.slack.seen() {
		if s.Path != "/api/chat.postEphemeral" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(s.Body), &m); err != nil {
			t.Fatalf("body %q: %v", s.Body, err)
		}
		out = append(out, m)
	}
	return out
}

// responses are the raw bodies of the requests to the response_url.
func (e *pressEnv) responses() []string {
	var out []string
	for _, s := range e.slack.seen() {
		if strings.HasPrefix(s.Path, "/actions/") {
			out = append(out, s.Body)
		}
	}
	return out
}

// field is the field of a log line, empty when it is absent.
func field(line map[string]any, name string) string {
	v, _ := line[name].(string)
	return v
}

// TestPressRunsTheCommand is C-24.FR-10, AC-12 and C-18.FR-8: a press of each button of a Root message from an account
// linked to a Responder runs its Command as that User with the Transport slack, looked up in the identity space of the
// Connection; a Command that ran is not answered, and slack_press logs the Connection, the Alert Group, the Command and
// the outcome, done or unchanged.
func TestPressRunsTheCommand(t *testing.T) {
	e := newPressEnv(t)
	for _, command := range []string{buttons.CommandAcknowledge, buttons.CommandUnacknowledge, buttons.CommandResolve,
		buttons.CommandUnsnooze} {
		line := e.press(t, pressIn{value: e.act(t, command, 0)})
		if field(line, "connection") != "CN0000000000S1" || field(line, "group") != pressGroup ||
			field(line, "command") != command || field(line, "outcome") != "done" || line["answer"] != nil ||
			line["error"] != nil || line["level"] != "INFO" {
			t.Errorf("%s: %v", command, line)
		}
	}
	got := e.commands.ran()
	if len(got) != 4 {
		t.Fatalf("ran %+v", got)
	}
	for _, r := range got {
		if r.group != pressGroup || r.caller.Transport != audit.TransportSlack || r.caller.Actor != audit.User(7,
			"SR0000000000B1") || !slices.Equal(r.caller.Permissions, pressRoles.Permissions(auth.RoleResponder)) {
			t.Errorf("ran %+v", r)
		}
	}
	e.commands.unchanged = true
	if line := e.press(t, pressIn{value: e.act(t, buttons.CommandAcknowledge, 0)}); field(line, "outcome") !=
		"unchanged" || line["answer"] != nil {
		t.Errorf("unchanged: %v", line)
	}
	if n := len(e.slack.seen()); n != 0 {
		t.Errorf("%d answers", n)
	}
	for _, s := range e.links.spaces {
		if s != accountlinks.SpaceSlack(pressConnID) {
			t.Errorf("looked up in %s", s)
		}
	}
}

// TestSnoozeMenu is C-24.FR-5, AC-12 and C-10.FR-18: the option "4h" of the Snooze menu snoozes for 4 hours from the
// business clock, the option of the preset tomorrow — and of the start of the week — ends at the preset in the User's
// profile time zone, which the dispatcher takes for a preset without a zone; an option the Route no longer has, or a
// preset of a Route without Snooze durations, is not verified.
func TestSnoozeMenu(t *testing.T) {
	e := newPressEnv(t)
	for _, arg := range []int{1, buttons.ArgumentTomorrow, buttons.ArgumentWeekStart} {
		if line := e.press(t, pressIn{value: e.act(t, buttons.CommandSnooze, arg), menu: true}); field(line,
			"outcome") != "done" || field(line, "command") != buttons.CommandSnooze {
			t.Errorf("option %d: %v", arg, line)
		}
	}
	got := e.commands.ran()
	if len(got) != 3 || got[0].end == nil || got[0].end.Until == nil ||
		!got[0].end.Until.Equal(pressNow.Add(4*time.Hour)) || got[0].end.Preset != "" ||
		got[1].end == nil || got[1].end.Preset != groups.PresetTomorrow || got[1].end.TimeZone != nil ||
		got[1].end.Until != nil || got[2].end == nil || got[2].end.Preset != groups.PresetWeekStart {
		t.Fatalf("ran %+v", got)
	}
	if line := e.press(t, pressIn{value: e.act(t, buttons.CommandSnooze, 7), menu: true}); field(line, "outcome") !=
		outcomeNotVerified {
		t.Errorf("an option the Route no longer has: %v", line)
	}
	e.bindings.mu.Lock()
	b := e.bindings.bound[pressChannel+"/"+pressRootTS]
	b.SnoozeSeconds = nil
	e.bindings.bound[pressChannel+"/"+pressRootTS] = b
	e.bindings.mu.Unlock()
	if line := e.press(t, pressIn{value: e.act(t, buttons.CommandSnooze, buttons.ArgumentTomorrow),
		menu: true}); field(line, "outcome") != outcomeNotVerified {
		t.Errorf("a preset without durations: %v", line)
	}
	if n := len(e.commands.ran()); n != 3 {
		t.Errorf("%d Commands ran", n)
	}
	for _, m := range e.ephemerals(t) {
		if m["text"] != "This button could not be verified; nothing was changed." {
			t.Errorf("answer %v", m)
		}
	}
}

// TestRefusals is C-24.FR-10, AC-12 and C-18.FR-8: a Viewer's press is forbidden, a press from an account without a
// link is answered with the profile link and changes nothing, a disabled User's press is refused, a precondition that
// does not hold is answered with the dispatcher's detail, and a Command, a link lookup or a binding that fails is
// answered that nothing changed and logs its error; each answer goes privately to the person in the channel.
func TestRefusals(t *testing.T) {
	e := newPressEnv(t)
	resolve := e.act(t, buttons.CommandResolve, 0)
	cases := []struct {
		name, user, outcome, text string
		setup                     func()
		logged                    bool
	}{
		{"viewer", "U0VERA", outcomeForbidden, "You are not permitted to do this", nil, false},
		{"not linked", "U0CAROL", outcomeNotLinked,
			"Your Slack account is not linked to Muster. Link it in your profile: http://localhost:8080/profile", nil,
			false},
		{"disabled", "U0DORA", outcomeDisabled, "Your Muster account is disabled", nil, false},
		{"refused", "U0BOB", outcomeRefused, "The Alert Group is resolved.", func() {
			e.commands.err = &groups.RefusedError{Code: "already_resolved", Message: "the Alert Group is resolved"}
		}, false},
		{"command failed", "U0BOB", outcomeFailed, "Muster could not run this command; nothing was changed.", func() {
			e.commands.err = errors.New("the database went away")
		}, true},
		{"links failed", "U0BOB", outcomeFailed, "Muster could not run this command; nothing was changed.", func() {
			e.commands.err, e.links.err = nil, errors.New("the links went away")
		}, true},
		{"binding failed", "U0BOB", outcomeFailed, "Muster could not run this command; nothing was changed.", func() {
			e.links.err, e.bindings.err = nil, errors.New("the bindings went away")
		}, true},
	}
	for i, c := range cases {
		if c.setup != nil {
			c.setup()
		}
		line := e.press(t, pressIn{user: c.user, value: resolve})
		if field(line, "outcome") != c.outcome || field(line, "answer") != answeredEphemeral ||
			(line["error"] != nil) != c.logged {
			t.Errorf("%s: %v", c.name, line)
		}
		got := e.ephemerals(t)
		if len(got) != i+1 || got[i]["text"] != c.text || got[i]["user"] != c.user ||
			got[i]["channel"] != pressChannel || got[i]["thread_ts"] != nil {
			t.Errorf("%s: answers %v", c.name, got)
		}
	}
	for _, r := range e.commands.ran() {
		if r.caller.Actor == audit.User(9, "SR0000000000D1") {
			t.Errorf("the disabled User ran %+v", r)
		}
	}
	if n := len(e.commands.ran()); n != 3 {
		t.Errorf("%d Commands ran: the Viewer's, the refused and the failed one", n)
	}
}

// TestNotVerified is C-24.FR-10 and AC-12: a value that is tampered with, too long or missing, a value issued for
// another message — another Alert Group, another kind of message, a message of another channel — and a message that is
// not a Root message or a Reminder of a Destination of the Connection change nothing and are answered "This button
// could not be verified; nothing was changed."; a press that names no user, channel, message or action is not
// verified, and answered only when it names the person and the channel.
func TestNotVerified(t *testing.T) {
	e := newPressEnv(t)
	ack := e.act(t, buttons.CommandAcknowledge, 0)
	tampered := []byte(ack)
	tampered[len(tampered)-3] ^= 1
	other := e.sign(t, buttons.Action{Subject: buttons.SubjectRoot, PublicID: otherGroup,
		Command: buttons.CommandAcknowledge})
	cases := []pressIn{
		{value: string(tampered)},
		{value: ack + "A"},
		{value: strings.Repeat("A", buttons.MaxLen+1)},
		{value: ""},
		{value: other},
		{value: e.remind(t, buttons.CommandUnacknowledge)},
		{value: e.remind(t, buttons.CommandStillOnIt), ts: pressRootTS},
		{value: e.sign(t, buttons.Action{Subject: buttons.SubjectRoot, PublicID: pressGroup,
			Command: buttons.CommandStillOnIt})},
		{value: ack, ts: otherRootTS},
		{value: ack, ts: "9.999999"},
		{value: ack, channel: "C0OTHER"},
		{value: ack, noActions: true},
	}
	for i, c := range cases {
		line := e.press(t, c)
		// The action id of "Still on it" on its own Root message verifies, so its Alert Group is logged.
		if field(line, "outcome") != outcomeNotVerified || (field(line, "group") != "") != (i == 7) ||
			field(line, "answer") != answeredEphemeral {
			t.Errorf("case %d: %v", i, line)
		}
	}
	for _, m := range e.ephemerals(t) {
		if m["text"] != "This button could not be verified; nothing was changed." {
			t.Errorf("answer %v", m)
		}
	}
	if n := len(e.ephemerals(t)); n != len(cases) {
		t.Errorf("%d answers", n)
	}
	before := len(e.slack.seen())
	e.presses.Handle(t.Context(), e.conn, e.payload(t, pressIn{channel: pressChannel, ts: pressRootTS, value: ack}))
	e.presses.Handle(t.Context(), e.conn, []byte(`{"type":"block_actions","actions":[{"value":1}]}`))
	lines := e.log.lines(t, "slack_press")
	if n := len(e.slack.seen()); n != before {
		t.Errorf("a press without a user was answered: %d", n-before)
	}
	for _, l := range lines[len(lines)-2:] {
		if field(l, "outcome") != outcomeNotVerified || l["answer"] != nil || l["error"] == nil {
			t.Errorf("line %v", l)
		}
	}
	if n := len(e.commands.ran()); n != 0 {
		t.Errorf("%d Commands ran", n)
	}
}

// TestReminderButtons is C-24.FR-5 and FR-10 with C-17: "Still on it" on a Reminder answers it, which changes no
// message, so it is answered "Done: Still on it" in the Thread of the Root message; "Unack" unacknowledges the Alert
// Group and is not answered; a Command a Reminder does not carry is not verified, answered in the Thread.
func TestReminderButtons(t *testing.T) {
	e := newPressEnv(t)
	line := e.press(t, pressIn{ts: pressReminderTS, value: e.remind(t, buttons.CommandStillOnIt)})
	if field(line, "outcome") != "done" || field(line, "command") != buttons.CommandStillOnIt ||
		field(line, "answer") != answeredEphemeral {
		t.Errorf("still on it: %v", line)
	}
	if line := e.press(t, pressIn{ts: pressReminderTS, value: e.remind(t, buttons.CommandUnacknowledge)}); field(line,
		"outcome") != "done" || line["answer"] != nil {
		t.Errorf("unack: %v", line)
	}
	if line := e.press(t, pressIn{ts: pressReminderTS, value: e.sign(t, buttons.Action{Subject: buttons.SubjectReply,
		PublicID: pressGroup, Command: buttons.CommandResolve})}); field(line, "outcome") != outcomeNotVerified {
		t.Errorf("resolve on a reminder: %v", line)
	}
	got := e.commands.ran()
	if len(got) != 2 || got[0].command != buttons.CommandStillOnIt || got[1].command != buttons.CommandUnacknowledge {
		t.Fatalf("ran %+v", got)
	}
	answers := e.ephemerals(t)
	if len(answers) != 2 || answers[0]["text"] != "Done: Still on it" || answers[0]["thread_ts"] != pressRootTS ||
		answers[1]["text"] != "This button could not be verified; nothing was changed." ||
		answers[1]["thread_ts"] != pressRootTS {
		t.Errorf("answers %v", answers)
	}
}

// TestExpired is C-17.FR-6 for Slack: a press on a Reminder whose button was signed by a key that left the Keyring is
// answered in the Thread that the button has expired and changes nothing, its unverified Alert Group and Command not
// logged; an expired action id on a Root message, or one naming another Alert Group, is not verified.
func TestExpired(t *testing.T) {
	e := newPressEnv(t)
	e.keys.use(pressKeyB)
	still, ack := e.remind(t, buttons.CommandStillOnIt), e.act(t, buttons.CommandAcknowledge, 0)
	foreign := e.sign(t, buttons.Action{Subject: buttons.SubjectReply, PublicID: otherGroup,
		Command: buttons.CommandStillOnIt})
	e.keys.use(pressKeyA)
	e.keys.drop(pressKeyB)
	line := e.press(t, pressIn{ts: pressReminderTS, value: still})
	if field(line, "outcome") != outcomeExpired || field(line, "group") != "" || field(line, "command") != "" {
		t.Errorf("expired: %v", line)
	}
	for _, p := range []pressIn{{value: ack}, {ts: pressReminderTS, value: foreign}, {value: still}} {
		if line := e.press(t, p); field(line, "outcome") != outcomeNotVerified {
			t.Errorf("%+v: %v", p, line)
		}
	}
	answers := e.ephemerals(t)
	if len(answers) != 4 || answers[0]["text"] != "This button has expired; use the buttons on the Alert Group's "+
		"message" || answers[0]["thread_ts"] != pressRootTS {
		t.Errorf("answers %v", answers)
	}
	if n := len(e.commands.ran()); n != 0 {
		t.Errorf("%d Commands ran", n)
	}
}

// TestTestMessagePress is C-24.FR-13 and AC-16: a press on a test message of a Destination of the Connection, in its
// channel, changes nothing and is answered "This is a test message; nothing was changed", with its Command logged and
// the answer's request linked to the Destination; in another channel it is not verified; a failed read fails.
func TestTestMessagePress(t *testing.T) {
	e := newPressEnv(t)
	test := e.sign(t, buttons.Action{Subject: buttons.SubjectTest, PublicID: pressTestDest,
		Command: buttons.CommandAcknowledge})
	line := e.press(t, pressIn{ts: "1.000009", value: test})
	if field(line, "outcome") != outcomeTest || field(line, "command") != buttons.CommandAcknowledge ||
		field(line, "group") != "" || field(line, "answer") != answeredEphemeral {
		t.Errorf("test press: %v", line)
	}
	if line := e.press(t, pressIn{ts: "1.000009", channel: "C0OTHER", value: test}); field(line, "outcome") !=
		outcomeNotVerified {
		t.Errorf("another channel: %v", line)
	}
	e.bindings.testErr = errors.New("down")
	if line := e.press(t, pressIn{ts: "1.000009", value: test}); field(line, "outcome") != outcomeFailed ||
		field(line, "error") != "down" {
		t.Errorf("a failed read: %v", line)
	}
	answers := e.ephemerals(t)
	if len(answers) != 3 || answers[0]["text"] != "This is a test message; nothing was changed" ||
		answers[1]["text"] != "This button could not be verified; nothing was changed." {
		t.Errorf("answers %v", answers)
	}
	recs := e.rec.got()
	if len(recs) == 0 || recs[0].Links.Destination != 32 || recs[0].Links.AlertGroup != 0 {
		t.Errorf("records %+v", recs)
	}
	if n := len(e.commands.ran()); n != 0 {
		t.Errorf("%d Commands ran", n)
	}
}

// TestOtherInteraction is C-24.FR-10: another kind of interaction, signed and posted to the callback, is answered 200
// and ignored — logged as ignored, nothing run, nothing answered.
func TestOtherInteraction(t *testing.T) {
	e := newPressEnv(t)
	cb := newCBEnv(t)
	done := make(chan struct{})
	cb.cfg.Presses = handlerFunc(func(ctx context.Context, c Conn, payload []byte) {
		defer close(done)
		e.presses.Handle(ctx, c, payload)
	})
	cb.mount()
	w := cb.signedPost(t, 0, pressForm(string(e.payload(t, pressIn{typ: "view_submission", user: "U0BOB",
		channel: pressChannel, ts: pressRootTS, value: e.act(t, buttons.CommandAcknowledge, 0)}))))
	<-done
	lines := e.log.lines(t, "slack_press")
	if w.Code != http.StatusOK || len(lines) != 1 || field(lines[0], "outcome") != outcomeIgnored ||
		field(lines[0], "connection") != cbHTTP || lines[0]["answer"] != nil {
		t.Fatalf("answer %d, lines %v", w.Code, lines)
	}
	if len(e.commands.ran()) != 0 || len(e.slack.seen()) != 0 || len(e.links.spaces) != 0 {
		t.Error("an interaction that is no press was handled")
	}
}

type handlerFunc func(ctx context.Context, c Conn, payload []byte)

func (f handlerFunc) Handle(ctx context.Context, c Conn, payload []byte) { f(ctx, c, payload) }

// TestParallelPresses is C-24.FR-10 and slack.parallel_presses: a replica handles at most Parallel presses at a time;
// the others wait for a slot, already confirmed, and run once one is free. A press whose context ends while it waits
// fails; a press that panics fails and frees its slot.
func TestParallelPresses(t *testing.T) {
	e := newPressEnv(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 3)
	e.commands.hook = func(context.Context) {
		entered <- struct{}{}
		<-release
	}
	parked := make(chan struct{}, 3)
	e.presses.parked = func() { parked <- struct{}{} }
	ack := e.act(t, buttons.CommandAcknowledge, 0)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			e.presses.Handle(t.Context(), e.conn, e.payload(t, pressIn{user: "U0BOB", channel: pressChannel,
				ts: pressRootTS, value: ack}))
		})
	}
	<-entered
	<-entered
	<-parked
	select {
	case <-entered:
		t.Fatal("a third press ran while two held their slots")
	default:
	}
	close(release)
	wg.Wait()
	if n := len(e.commands.ran()); n != 3 {
		t.Fatalf("%d presses ran", n)
	}
	for _, l := range e.log.lines(t, "slack_press") {
		if field(l, "outcome") != "done" {
			t.Errorf("line %v", l)
		}
	}

	e.commands.hook = func(context.Context) { panic("boom") }
	if line := e.press(t, pressIn{value: ack}); field(line, "outcome") != outcomeFailed ||
		field(line, "error") != errPanicked.Error() || strings.Contains(e.log.String(), "boom") {
		t.Errorf("a panic: %v", line)
	}
	e.commands.hook = nil
	if line := e.press(t, pressIn{value: ack}); field(line, "outcome") != "done" {
		t.Errorf("after a panic: %v", line)
	}

	full := &Presses{Parallel: 1, Log: e.presses.Log}
	hold, err := full.slot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	ctx, cancel := context.WithCancel(t.Context())
	full.parked = cancel
	before := len(e.log.lines(t, "slack_press"))
	full.Handle(ctx, e.conn, e.payload(t, pressIn{user: "U0BOB", channel: pressChannel, ts: pressRootTS,
		value: ack}))
	lines := e.log.lines(t, "slack_press")
	if len(lines) != before+1 || field(lines[before], "outcome") != outcomeFailed ||
		field(lines[before], "error") != context.Canceled.Error() {
		t.Errorf("a press whose context ended while it waited: %v", lines[before:])
	}
}
