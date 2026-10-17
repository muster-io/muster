// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
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
	"unicode/utf8"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/metrics"
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

// use makes id the active key.
func (k *pressKeys) use(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.active = id
}

// drop removes id from the Keyring.
func (k *pressKeys) drop(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.keys, id)
}

// The fixtures of presses: the Root message m1 of the Alert Group A in the Bridge Destination R of the Connection 3,
// its older Root message m0, replaced past its edit window, and its Reminder r1; the Route's Snooze durations of 1 h,
// 4 h and 24 h.
const (
	pressGroup = "AG0000000000A1"
	otherGroup = "AG0000000000B2"
)

var (
	pressConnID = int64(3)
	pressDest   = delivery.Destination{ID: 31, PublicID: "DS0000000000R1", Name: "alerts", Type: delivery.TypeBridge,
		Connection: &pressConnID}
	pressRoles = auth.Roles{auth.RoleResponder: {groups.PermissionAcknowledge, groups.PermissionResolve,
		groups.PermissionSnooze}, auth.RoleViewer: {"alert-groups:read"}}
	pressUsers = map[string]accountlinks.User{
		"u-bob": {ID: 7, PublicID: "SR0000000000B1", Login: "bob", Name: "Bob Brown", Role: auth.RoleResponder,
			Status: accountlinks.StatusActive},
		"u-vera": {ID: 8, PublicID: "SR0000000000V1", Login: "vera", Name: "Vera Viewer", Role: auth.RoleViewer,
			Status: accountlinks.StatusActive},
		"u-dora": {ID: 9, PublicID: "SR0000000000D1", Login: "dora", Name: "Dora", Role: auth.RoleResponder,
			Status: accountlinks.StatusDisabled},
		"u-ivan": {ID: 10, PublicID: "SR0000000000I1", Login: "ivan", Name: "Иван", Role: auth.RoleResponder,
			Status: accountlinks.StatusActive, Language: "ru"},
	}
)

// pressBindings bind the messages of the Connection 3: the Root message m1 and the older Root message m0 of Alert
// Group A, and its Reminder r1. It counts the reads, and records the messages RootDeleted was called for, which it
// reports as the current Root message m1 only, or fails with deleteErr.
type pressBindings struct {
	mu        sync.Mutex
	bound     map[string]delivery.BridgeBinding
	err       error
	reads     int
	deleted   []string
	deleteErr error
}

func newPressBindings() *pressBindings {
	root := delivery.BridgeBinding{Kind: buttons.SubjectRoot, Destination: pressDest, GroupPublicID: pressGroup,
		GroupNumber: 1, SnoozeSeconds: []int64{3600, 14400, 86400}}
	older, reminder := root, root
	older.Superseded = true
	reminder.Kind = buttons.SubjectReply
	return &pressBindings{bound: map[string]delivery.BridgeBinding{"m1": root, "m0": older, "r1": reminder}}
}

func (b *pressBindings) BridgePressBinding(_ context.Context, connectionID int64, messageID string) (
	delivery.BridgeBinding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads++
	if b.err != nil {
		return delivery.BridgeBinding{}, b.err
	}
	bound, ok := b.bound[messageID]
	if !ok || connectionID != pressConnID {
		return delivery.BridgeBinding{}, delivery.ErrNotBound
	}
	return bound, nil
}

func (b *pressBindings) BridgeThreadBinding(ctx context.Context, connectionID int64, messageID string) (
	delivery.BridgeBinding, error) {
	bound, err := b.BridgePressBinding(ctx, connectionID, messageID)
	if err == nil && bound.Kind != buttons.SubjectRoot {
		return delivery.BridgeBinding{}, delivery.ErrNotBound
	}
	return bound, err
}

func (b *pressBindings) RootDeleted(_ context.Context, connectionID int64, messageID string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted = append(b.deleted, fmt.Sprintf("%d/%s", connectionID, messageID))
	if b.deleteErr != nil {
		return false, b.deleteErr
	}
	return connectionID == pressConnID && messageID == "m1", nil
}

// deletions are the Connections and messages RootDeleted was called for, as connection/message.
func (b *pressBindings) deletions() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.deleted)
}

func (b *pressBindings) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reads
}

// pressLinks are the Account links of the Connection 3; it records the identity spaces looked up.
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
	if !ok || space != accountlinks.SpaceBridge(pressConnID) {
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
// replaces every other answer, and hook runs inside each Command.
type pressCommands struct {
	mu    sync.Mutex
	calls []ran
	err   error
	hook  func(ctx context.Context) error
}

func (f *pressCommands) run(ctx context.Context, r ran) (groups.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, r)
	err, hook := f.err, f.hook
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return groups.Result{}, err
		}
	}
	if p := groups.Command(r.command).Permission(); !slices.Contains(r.caller.Permissions, p) {
		return groups.Result{}, &groups.ForbiddenError{Permission: p}
	}
	if err != nil {
		return groups.Result{}, err
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

// answerPath is the interactive path of the tests: it records the subject, the kind of operation, the deadline and
// the outcome of each call and makes it as deliverytest.Unlimited does, or fails with err without calling.
type answerPath struct {
	path *delivery.Interactive

	mu        sync.Mutex
	subjects  []delivery.Subject
	ops       []string
	deadlines []time.Time
	outcomes  []delivery.Outcome
	err       error
}

func (p *answerPath) Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error) {
	p.mu.Lock()
	p.subjects = append(p.subjects, s)
	p.ops = append(p.ops, fmt.Sprintf("%T", op))
	deadline, _ := ctx.Deadline()
	p.deadlines = append(p.deadlines, deadline)
	err := p.err
	p.mu.Unlock()
	if err != nil {
		return delivery.Outcome{}, err
	}
	out, err := p.path.Do(ctx, s, op)
	p.mu.Lock()
	p.outcomes = append(p.outcomes, out)
	p.mu.Unlock()
	return out, err
}

func (p *answerPath) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.subjects)
}

// pressEnv is an eventsEnv whose Connection 3 has a bridge that records the answers it is asked to show and answers
// them with answerWith; the real and the business clock are at the time of the events.
type pressEnv struct {
	*eventsEnv
	bridge *standIn

	mu         sync.Mutex
	answerWith http.HandlerFunc
}

func newPressEnv(t *testing.T) *pressEnv {
	t.Helper()
	e := &pressEnv{eventsEnv: newEventsEnv(eventTime),
		answerWith: answerJSON(http.StatusOK, "application/json", `{"shown_as":"ephemeral","replayed":false}`)}
	e.bridge = startStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		h := e.answerWith
		e.mu.Unlock()
		h(w, r)
	})
	e.conn.Client = newClient(t, network(t, nil), Settings{BaseURL: e.bridge.srv.URL, Token: testToken})
	return e
}

// answerAll makes the bridge answer every answerEvent with h.
func (e *pressEnv) answerAll(h http.HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.answerWith = h
}

// answered is an answerEvent as the bridge received it.
type answered struct {
	key, eventID, text string
}

// answers are the answerEvent calls the bridge received, in order; any other request fails the test.
func (e *pressEnv) answers(t *testing.T) []answered {
	t.Helper()
	var out []answered
	for _, r := range e.bridge.seen() {
		var in gen.AnswerInput
		if r.Method != http.MethodPost || r.Path != "/v1/answers" || json.Unmarshal([]byte(r.Body), &in) != nil {
			t.Fatalf("the bridge got %s %s %s", r.Method, r.Path, r.Body)
		}
		out = append(out, answered{key: r.Header.Get("Idempotency-Key"), eventID: in.EventId, text: in.Text})
	}
	return out
}

// sign is the data of a button about group on a message of the kind subject, signed with the active key.
func (e *eventsEnv) sign(t *testing.T, subject buttons.Subject, group, command string, arg int) string {
	t.Helper()
	id, _, err := buttons.Sign(e.keys, buttons.Action{Subject: subject, PublicID: group, Command: command,
		Argument: arg})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// press is a press event id of the account externalID on the message messageID with data.
func (e *eventsEnv) press(t *testing.T, id, messageID, externalID, data string) Event {
	t.Helper()
	return e.event(t, TypePress, member("id", id), member("message_id", messageID), member("data", data),
		userMember("external_id", externalID))
}

// handle handles ev, which must succeed.
func (e *eventsEnv) handle(t *testing.T, ev Event) {
	t.Helper()
	if err := e.events.Handle(t.Context(), e.conn, ev); err != nil {
		t.Fatal(err)
	}
}

// pressLine is the start of the bridge_press line of a press.
func pressLine(group, command, outcome, externalID string) string {
	return fmt.Sprintf(`"event":"bridge_press","connection":"CN0000000000B3","group":%q,"command":%q,"outcome":%q,`+
		`"external_id":%q`, group, command, outcome, externalID)
}

// The answers of presses, in English.
const (
	notVerified = "This button could not be verified; nothing was changed."
	notLinked   = "Your account is not linked to Muster. Link it in your profile: http://localhost:8080/profile"
)

// TestPressRunsTheCommandAsTheLinkedUser covers C-22.FR-12 and AC-15: a press from an account linked to a Responder,
// with data signed for the pressed Root message, runs its Command as that User with the Permissions of its Role and
// the Transport bridge, looked up in the identity space of the Connection; it is not answered, and bridge_press logs
// the Alert Group, the Command, the outcome and the account. Every button of a Root message runs its Command, a Snooze
// for the Route's duration at its index from the business clock.
func TestPressRunsTheCommandAsTheLinkedUser(t *testing.T) {
	e := newPressEnv(t)
	e.handle(t, e.press(t, "ev-1", "m1", "u-bob", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandAcknowledge, 0)))
	calls := e.commands.ran()
	if len(calls) != 1 || calls[0].command != buttons.CommandAcknowledge || calls[0].group != pressGroup {
		t.Fatalf("commands %+v", calls)
	}
	c := calls[0].caller
	if c.Actor != audit.User(7, "SR0000000000B1") || c.Transport != audit.TransportBridge ||
		!slices.Equal(c.Permissions, pressRoles.Permissions(auth.RoleResponder)) {
		t.Errorf("caller %+v", c)
	}
	if !slices.Equal(e.links.spaces, []string{"bridge:3"}) {
		t.Errorf("looked up in %v", e.links.spaces)
	}
	if a := e.answers(t); len(a) != 0 || e.path.calls() != 0 {
		t.Errorf("a Command that ran was answered: %+v", a)
	}
	if got := e.log.String(); !strings.Contains(got, pressLine(pressGroup, "acknowledge", "done", "u-bob")+"}") ||
		strings.Count(got, "\n") != 1 {
		t.Errorf("logged %s", got)
	}
	if !e.store.recorded(e.conn.ID, "ev-1") {
		t.Error("the press was not recorded")
	}
	for i, tc := range []struct {
		command string
		arg     int
	}{{buttons.CommandUnacknowledge, 0}, {buttons.CommandResolve, 0}, {buttons.CommandUnsnooze, 0},
		{buttons.CommandSnooze, 1}} {
		e.handle(t, e.press(t, fmt.Sprintf("ev-b%d", i), "m1", "u-bob", e.sign(t, buttons.SubjectRoot, pressGroup,
			tc.command, tc.arg)))
	}
	calls = e.commands.ran()
	var names []string
	for _, r := range calls[1:] {
		names = append(names, r.command)
	}
	if !slices.Equal(names, []string{"unacknowledge", "resolve", "unsnooze", "snooze"}) {
		t.Fatalf("commands %v", names)
	}
	if end := calls[4].end; end == nil || end.Until == nil || !end.Until.Equal(eventTime.Add(4*time.Hour)) {
		t.Errorf("snoozed until %+v", end)
	}
	if a := e.answers(t); len(a) != 0 {
		t.Errorf("answered %+v", a)
	}
}

// TestPressTamperedData covers C-22.FR-12 and AC-15: a press whose data is not a valid signed action id — a changed
// signature, garbage, data longer than an action id — changes nothing and is answered "This button could not be
// verified; nothing was changed.", logged without an Alert Group or a Command.
func TestPressTamperedData(t *testing.T) {
	e := newPressEnv(t)
	good := e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandResolve, 0)
	last := "A"
	if strings.HasSuffix(good, "A") {
		last = "B"
	}
	for i, data := range []string{good[:len(good)-1] + last, "a9.k1.AAAAAAAAAAAA", good + strings.Repeat("A", 64)} {
		e.handle(t, e.press(t, fmt.Sprintf("ev-%d", i), "m1", "u-bob", data))
	}
	if calls := e.commands.ran(); len(calls) != 0 {
		t.Errorf("tampered data ran %+v", calls)
	}
	a := e.answers(t)
	if len(a) != 3 {
		t.Fatalf("answers %+v", a)
	}
	for i, x := range a {
		if x.text != notVerified || x.eventID != fmt.Sprintf("ev-%d", i) {
			t.Errorf("answer %d %+v", i, x)
		}
	}
	if n := strings.Count(e.log.String(), pressLine("", "", "not_verified", "u-bob")+"}"); n != 3 {
		t.Errorf("logged %s", e.log.String())
	}
}

// TestPressDataOfAnotherMessage covers C-22.FR-12 and AC-15: data issued for another message is refused like a bad
// signature — a Root message's button of another Alert Group, a Reminder's button on a Root message and a Root
// message's on a Reminder — and so is a button whose Command its message does not carry or whose Snooze duration the
// Route no longer has.
func TestPressDataOfAnotherMessage(t *testing.T) {
	e := newPressEnv(t)
	for i, tc := range []struct {
		message, data string
	}{
		{"m1", e.sign(t, buttons.SubjectRoot, otherGroup, buttons.CommandAcknowledge, 0)},
		{"m1", e.sign(t, buttons.SubjectReply, pressGroup, buttons.CommandUnacknowledge, 0)},
		{"r1", e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandAcknowledge, 0)},
		{"r1", e.sign(t, buttons.SubjectReply, otherGroup, buttons.CommandStillOnIt, 0)},
		{"m1", e.sign(t, buttons.SubjectTest, pressDest.PublicID, buttons.CommandAcknowledge, 0)[:10]},
		{"m1", e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandStillOnIt, 0)},
		{"r1", e.sign(t, buttons.SubjectReply, pressGroup, buttons.CommandResolve, 0)},
		{"m1", e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandSnooze, 3)},
	} {
		e.handle(t, e.press(t, fmt.Sprintf("ev-%d", i), tc.message, "u-bob", tc.data))
	}
	if calls := e.commands.ran(); len(calls) != 0 {
		t.Errorf("data of another message ran %+v", calls)
	}
	a := e.answers(t)
	if len(a) != 8 {
		t.Fatalf("answers %+v", a)
	}
	for i, x := range a {
		if x.text != notVerified {
			t.Errorf("answer %d %q", i, x.text)
		}
	}
	got := e.log.String()
	if n := strings.Count(got, pressLine("", "", "not_verified", "u-bob")); n != 5 {
		t.Errorf("%d lines without a group:\n%s", n, got)
	}
	for _, want := range []string{pressLine(pressGroup, "still_on_it", "not_verified", "u-bob"),
		pressLine(pressGroup, "resolve", "not_verified", "u-bob"),
		pressLine(pressGroup, "snooze", "not_verified", "u-bob")} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %s:\n%s", want, got)
		}
	}
}

// TestPressNotLinked covers C-22.FR-12 and AC-15: a press from an account without an Account link for this
// Connection changes nothing and is answered, in English, with the link to the profile below MUSTER_PUBLIC_URL; an
// account linked through another Connection has no link here.
func TestPressNotLinked(t *testing.T) {
	for _, base := range []string{"http://localhost:8080/", "http://localhost:8080"} {
		e := newPressEnv(t)
		e.events.PublicURL = base
		e.handle(t, e.press(t, "ev-1", "m1", "u-alice", e.sign(t, buttons.SubjectRoot, pressGroup,
			buttons.CommandResolve, 0)))
		if calls := e.commands.ran(); len(calls) != 0 {
			t.Errorf("an unlinked account ran %+v", calls)
		}
		if a := e.answers(t); len(a) != 1 || a[0].text != notLinked {
			t.Errorf("%s: answers %+v", base, a)
		}
		if !strings.Contains(e.log.String(), pressLine(pressGroup, "resolve", "not_linked", "u-alice")+"}") {
			t.Errorf("logged %s", e.log.String())
		}
	}
	e := newPressEnv(t)
	e.links.users = map[string]accountlinks.User{}
	e.handle(t, e.press(t, "ev-1", "m1", "u-bob", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandResolve, 0)))
	if a := e.answers(t); len(a) != 1 || a[0].text != notLinked || len(e.commands.ran()) != 0 {
		t.Errorf("answers %+v", a)
	}
}

// TestPressTooOld covers C-22.FR-12 and AC-15: a press older than bridge.event_max_age by the real clock changes
// nothing, reads nothing, is not answered, and logs bridge_press_dropped only.
func TestPressTooOld(t *testing.T) {
	e := newPressEnv(t)
	e.real.Set(eventTime.Add(EventMaxAge + time.Second))
	e.handle(t, e.press(t, "ev-1", "m1", "u-bob", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandAcknowledge, 0)))
	if len(e.commands.ran()) != 0 || e.bindings.count() != 0 || len(e.links.spaces) != 0 ||
		len(e.answers(t)) != 0 || e.path.calls() != 0 {
		t.Errorf("an old press was handled")
	}
	if got := e.log.String(); !strings.Contains(got, `"event":"bridge_press_dropped","connection":"CN0000000000B3",`+
		`"external_id":"u-bob","age_seconds":3601}`) || strings.Contains(got, `"bridge_press"`) {
		t.Errorf("logged %s", got)
	}
}

// TestPressUnknownMessage covers C-22.FR-11, FR-12 and AC-15: a press on a message Muster did not post through this
// Connection — any other message, a test message's id, a message of another Connection — changes nothing, looks up no
// Account link and is not answered; it is recorded and logged as unknown_message.
func TestPressUnknownMessage(t *testing.T) {
	e := newPressEnv(t)
	data := e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandAcknowledge, 0)
	e.handle(t, e.press(t, "ev-1", "m9", "u-bob", data))
	other := e.conn
	other.ID, other.PublicID = 4, "CN0000000000B4"
	if err := e.events.Handle(t.Context(), other, e.press(t, "ev-2", "m1", "u-bob", data)); err != nil {
		t.Fatal(err)
	}
	if len(e.commands.ran()) != 0 || len(e.links.spaces) != 0 || len(e.answers(t)) != 0 || e.path.calls() != 0 {
		t.Errorf("a press on an unknown message was handled")
	}
	got := e.log.String()
	if !strings.Contains(got, pressLine("", "", "unknown_message", "u-bob")) ||
		!strings.Contains(got, `"connection":"CN0000000000B4","group":"","command":"","outcome":"unknown_message"`) ||
		!e.store.recorded(e.conn.ID, "ev-1") || !e.store.recorded(4, "ev-2") {
		t.Errorf("logged %s", got)
	}
}

// TestPressOnTheOlderRootMessageRuns covers C-22.FR-9 and FR-12 on the event path: a press on the older Root message
// of a delivery republished past its edit window runs its Command, unanswered, and the event path makes no call to the
// bridge but answers — it never edits a message; delivery reconciles the current Root message only
// (internal/delivery TestPressOnTheOlderRootMessage).
func TestPressOnTheOlderRootMessageRuns(t *testing.T) {
	e := newPressEnv(t)
	e.handle(t, e.press(t, "ev-1", "m0", "u-bob", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandAcknowledge, 0)))
	if calls := e.commands.ran(); len(calls) != 1 || calls[0].command != "acknowledge" || calls[0].group != pressGroup {
		t.Fatalf("commands %+v", calls)
	}
	if len(e.bridge.seen()) != 0 || e.path.calls() != 0 {
		t.Errorf("the bridge got %+v", e.bridge.seen())
	}
	if !strings.Contains(e.log.String(), pressLine(pressGroup, "acknowledge", "done", "u-bob")) {
		t.Errorf("logged %s", e.log.String())
	}
}

// TestPressViewerAndDisabled covers C-22.FR-12 and C-18.FR-8: a Viewer's press is refused by the dispatcher and
// answered "You are not permitted to do this"; a disabled User's press runs nothing and is answered "Your Muster
// account is disabled".
func TestPressViewerAndDisabled(t *testing.T) {
	e := newPressEnv(t)
	e.handle(t, e.press(t, "ev-1", "m1", "u-vera", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandResolve, 0)))
	e.handle(t, e.press(t, "ev-2", "m1", "u-dora", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandResolve, 0)))
	if calls := e.commands.ran(); len(calls) != 1 || calls[0].caller.Actor.ID != 8 {
		t.Errorf("commands %+v", calls)
	}
	a := e.answers(t)
	if len(a) != 2 || a[0].text != "You are not permitted to do this" || a[1].text != "Your Muster account is disabled" {
		t.Errorf("answers %+v", a)
	}
	got := e.log.String()
	for _, want := range []string{pressLine(pressGroup, "resolve", "forbidden", "u-vera") + "}",
		pressLine(pressGroup, "resolve", "disabled", "u-dora") + "}"} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %s:\n%s", want, got)
		}
	}
}

// TestPressExpiredReminder covers C-17.FR-6 on a Bridge: a press on a Reminder whose button was signed by a key that
// left the Keyring changes nothing and is answered that the button expired; such a button on a Root message, or one
// naming another Alert Group, is not verified.
func TestPressExpiredReminder(t *testing.T) {
	e := newPressEnv(t)
	e.keys.use(pressKeyB)
	reply := e.sign(t, buttons.SubjectReply, pressGroup, buttons.CommandStillOnIt, 0)
	root := e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandAcknowledge, 0)
	other := e.sign(t, buttons.SubjectReply, otherGroup, buttons.CommandStillOnIt, 0)
	e.keys.use(pressKeyA)
	e.keys.drop(pressKeyB)
	e.handle(t, e.press(t, "ev-1", "r1", "u-bob", reply))
	e.handle(t, e.press(t, "ev-2", "m1", "u-bob", root))
	e.handle(t, e.press(t, "ev-3", "r1", "u-bob", other))
	if calls := e.commands.ran(); len(calls) != 0 {
		t.Errorf("an expired button ran %+v", calls)
	}
	a := e.answers(t)
	if len(a) != 3 || a[0].text != "This button has expired; use the buttons on the Alert Group's message" ||
		a[1].text != notVerified || a[2].text != notVerified {
		t.Errorf("answers %+v", a)
	}
	if !strings.Contains(e.log.String(), pressLine("", "", "expired", "u-bob")+"}") {
		t.Errorf("logged %s", e.log.String())
	}
}

// TestPressStillOnItNotAnswered covers C-22.FR-12 and C-17.FR-10: "Still on it" on a Reminder answers the open
// Reminder as the linked User and, like every Command that ran, is not answered; Unacknowledge on a Reminder runs too.
func TestPressStillOnItNotAnswered(t *testing.T) {
	e := newPressEnv(t)
	e.handle(t, e.press(t, "ev-1", "r1", "u-bob", e.sign(t, buttons.SubjectReply, pressGroup,
		buttons.CommandStillOnIt, 0)))
	e.handle(t, e.press(t, "ev-2", "r1", "u-bob", e.sign(t, buttons.SubjectReply, pressGroup,
		buttons.CommandUnacknowledge, 0)))
	calls := e.commands.ran()
	if len(calls) != 2 || calls[0].command != "still_on_it" || calls[0].group != pressGroup ||
		calls[1].command != "unacknowledge" {
		t.Fatalf("commands %+v", calls)
	}
	if a := e.answers(t); len(a) != 0 {
		t.Errorf("answers %+v", a)
	}
	if !strings.Contains(e.log.String(), pressLine(pressGroup, "still_on_it", "done", "u-bob")+"}") {
		t.Errorf("logged %s", e.log.String())
	}
}

// TestPressRefusedAndFailed covers C-22.FR-12 and the decision on what fails an event: a Command the dispatcher
// refuses is answered with its refusal; one that fails otherwise is answered that nothing changed, logged with its
// error and recorded, so that it never blocks the Connection's events; one whose context ends while it runs fails the
// event, unrecorded, so that it comes again.
func TestPressRefusedAndFailed(t *testing.T) {
	e := newPressEnv(t)
	data := e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandAcknowledge, 0)
	e.commands.err = &groups.RefusedError{Code: "already_resolved", Message: "the Alert Group is resolved"}
	e.handle(t, e.press(t, "ev-1", "m1", "u-bob", data))
	e.commands.err = errors.New("the dispatcher broke")
	e.handle(t, e.press(t, "ev-2", "m1", "u-bob", data))
	a := e.answers(t)
	if len(a) != 2 || a[0].text != "The Alert Group is resolved." ||
		a[1].text != "Muster could not run this command; nothing was changed." {
		t.Errorf("answers %+v", a)
	}
	got := e.log.String()
	if !strings.Contains(got, pressLine(pressGroup, "acknowledge", "refused", "u-bob")+"}") ||
		!strings.Contains(got, pressLine(pressGroup, "acknowledge", "failed", "u-bob")+
			`,"error":"the dispatcher broke"}`) || !e.store.recorded(e.conn.ID, "ev-2") {
		t.Errorf("logged %s", got)
	}

	e = newPressEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.commands.hook = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}
	err := e.events.Handle(ctx, e.conn, e.press(t, "ev-1", "m1", "u-bob", data))
	if !errors.Is(err, context.Canceled) || e.store.recorded(e.conn.ID, "ev-1") || len(e.answers(t)) != 0 ||
		e.log.String() != "" {
		t.Errorf("a Command cut short = %v, logged %s", err, e.log.String())
	}
}

// TestPressReadFailures covers C-22.FR-11: a read of the binding or of the Account link that fails fails the event —
// logged as bridge_event_failed, unanswered and unrecorded, so that it comes again.
func TestPressReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(e *pressEnv)
	}{
		{"binding", func(e *pressEnv) { e.bindings.err = errors.New("the database went away") }},
		{"link", func(e *pressEnv) { e.links.err = errors.New("the database went away") }},
	} {
		e := newPressEnv(t)
		tc.set(e)
		ev := e.press(t, "ev-1", "m1", "u-bob", e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandResolve, 0))
		if err := e.events.Handle(t.Context(), e.conn, ev); err == nil || e.store.recorded(e.conn.ID, "ev-1") {
			t.Errorf("%s: handled with %v", tc.name, err)
		}
		if len(e.answers(t)) != 0 || len(e.commands.ran()) != 0 ||
			!strings.Contains(e.log.String(), `"event":"bridge_event_failed"`) {
			t.Errorf("%s: logged %s", tc.name, e.log.String())
		}
	}
	e := newPressEnv(t)
	e.links.err = errors.New("the database went away")
	if err := e.events.Handle(t.Context(), e.conn, e.press(t, "ev-1", "m1", "u-bob",
		e.sign(t, buttons.SubjectTest, pressDest.PublicID, buttons.CommandAcknowledge, 0))); err == nil {
		t.Error("a test press whose link cannot be read was handled")
	}
}

// TestPressOnATestMessage covers C-22.FR-17 and AC-12: a press whose verified data names a test message changes
// nothing, reads no binding, and is answered "This is a test message; nothing was changed", in the profile language of
// a linked User.
func TestPressOnATestMessage(t *testing.T) {
	e := newPressEnv(t)
	data := e.sign(t, buttons.SubjectTest, pressDest.PublicID, buttons.CommandAcknowledge, 0)
	e.handle(t, e.press(t, "ev-1", "t-1", "u-alice", data))
	e.handle(t, e.press(t, "ev-2", "t-1", "u-ivan", data))
	if len(e.commands.ran()) != 0 || e.bindings.count() != 0 {
		t.Errorf("a test press ran %+v", e.commands.ran())
	}
	a := e.answers(t)
	if len(a) != 2 || a[0].text != "This is a test message; nothing was changed" ||
		a[1].text != "Это тестовое сообщение; ничего не изменилось" {
		t.Errorf("answers %+v", a)
	}
	if !strings.Contains(e.log.String(), pressLine("", "acknowledge", "test", "u-alice")+"}") {
		t.Errorf("logged %s", e.log.String())
	}
}

// TestAnswerThroughTheInteractivePath covers C-22.FR-13: an answer is answerEvent with the event's id, its text and the
// key answer-<event id>, made once the claim committed as an AnswerOp on the interactive path, limited by the
// Connection alone, in the interactive client class, within the budget; a RetryAfter it gets holds the Connection.
func TestAnswerThroughTheInteractivePath(t *testing.T) {
	e := newPressEnv(t)
	e.events.Budget = 2 * time.Second
	counter := metrics.ClientRequests.With(string(outbound.ClassInteractive), string(outbound.OutcomeOK))
	before := counter.Get()
	start := time.Now()
	e.handle(t, e.press(t, "ev-1", "m1", "u-alice", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandResolve, 0)))
	a := e.answers(t)
	if len(a) != 1 || a[0].key != "answer-ev-1" || a[0].eventID != "ev-1" || a[0].text != notLinked {
		t.Fatalf("answers %+v", a)
	}
	if r := e.bridge.seen()[0]; r.Header.Get("Authorization") != "Bearer "+testToken ||
		r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request %+v", r)
	}
	p := e.path
	if len(p.subjects) != 1 || p.subjects[0].Destination != nil || p.subjects[0].Connection == nil ||
		*p.subjects[0].Connection != pressConnID || p.ops[0] != "delivery.answerOp" {
		t.Errorf("path calls %+v %v", p.subjects, p.ops)
	}
	if d := p.deadlines[0]; d.IsZero() || d.Before(start) || d.After(time.Now().Add(2*time.Second)) {
		t.Errorf("deadline %v", d)
	}
	if counter.Get() != before+1 {
		t.Errorf("interactive requests %d, want %d", counter.Get(), before+1)
	}

	e = newPressEnv(t)
	e.answerAll(answerJSON(http.StatusTooManyRequests, problemJSON, `{"class":"rate_limited","scope":"channel"}`,
		"Retry-After", "7"))
	e.handle(t, e.press(t, "ev-1", "m1", "u-alice", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandResolve, 0)))
	if o := e.path.outcomes; len(o) != 1 || o[0].Kind != delivery.OutcomeRetryAfter ||
		o[0].Scope != delivery.ScopeConnection || o[0].RetryAfter != 7*time.Second {
		t.Errorf("outcomes %+v", o)
	}
}

// TestAnswerFailureLoggedNotRetried covers C-22.FR-13: an answer without a limiter token within the budget calls
// nothing, and one the bridge fails is made once; either is logged with the press's line and never made again, and the
// press stays recorded, so that its redelivery is acknowledged without an answer.
func TestAnswerFailureLoggedNotRetried(t *testing.T) {
	e := newPressEnv(t)
	e.path.err = &delivery.LimitedError{RetryAfter: time.Second}
	ev := e.press(t, "ev-1", "m1", "u-alice", e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandResolve, 0))
	e.handle(t, ev)
	e.handle(t, ev)
	if len(e.answers(t)) != 0 || e.path.calls() != 1 || !e.store.recorded(e.conn.ID, "ev-1") {
		t.Errorf("a limited answer was made: %d path calls", e.path.calls())
	}
	want := pressLine(pressGroup, "resolve", "not_linked", "u-alice") + `,"error":"the event was not answered: ` +
		`no limiter token was free within the interactive budget; retry after 1s"}`
	if got := e.log.String(); !strings.Contains(got, want) || strings.Count(got, "\n") != 1 {
		t.Errorf("logged %s", got)
	}

	e = newPressEnv(t)
	e.answerAll(answerJSON(http.StatusInternalServerError, problemJSON,
		`{"class":"transient","detail":"the messenger is down for `+testToken+`"}`))
	ev = e.press(t, "ev-1", "m1", "u-alice", e.sign(t, buttons.SubjectRoot, pressGroup, buttons.CommandResolve, 0))
	e.handle(t, ev)
	e.handle(t, ev)
	if a := e.answers(t); len(a) != 1 {
		t.Errorf("a failed answer was made %d times", len(a))
	}
	want = pressLine(pressGroup, "resolve", "not_linked", "u-alice") +
		`,"error":"the event was not answered: the messenger is down for [redacted]"}`
	if got := e.log.String(); !strings.Contains(got, want) || strings.Contains(got, testToken) {
		t.Errorf("logged %s", got)
	}

	e = newPressEnv(t)
	e.answerAll(answerJSON(http.StatusOK, "application/json", `{"shown_as":"banner","replayed":false}`))
	e.commands.err = errors.New("the dispatcher broke")
	e.handle(t, e.press(t, "ev-1", "m1", "u-bob", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandResolve, 0)))
	want = pressLine(pressGroup, "resolve", "failed", "u-bob") + `,"error":"the dispatcher broke\n` +
		`the event was not answered: POST answers: the answer is not the expected JSON"}`
	if got := e.log.String(); !strings.Contains(got, want) {
		t.Errorf("logged %s", got)
	}
}

// TestAnswerLanguage covers C-22.FR-13: answers are in the profile language of the User the account is linked to —
// Russian for a Russian-speaking User, English for one without a language — and in English for an account without a
// link.
func TestAnswerLanguage(t *testing.T) {
	e := newPressEnv(t)
	tampered := "a9.k1.AAAAAAAAAAAA"
	e.handle(t, e.press(t, "ev-1", "m1", "u-ivan", tampered))
	e.handle(t, e.press(t, "ev-2", "m1", "u-alice", tampered))
	e.handle(t, e.press(t, "ev-3", "m1", "u-bob", tampered))
	e.commands.err = errors.New("the dispatcher broke")
	e.handle(t, e.press(t, "ev-4", "m1", "u-ivan", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandResolve, 0)))
	e.handle(t, e.press(t, "ev-5", "m1", "u-alice", e.sign(t, buttons.SubjectRoot, pressGroup,
		buttons.CommandResolve, 0)))
	var texts []string
	for _, a := range e.answers(t) {
		texts = append(texts, a.text)
	}
	want := []string{"Не удалось проверить эту кнопку; ничего не изменилось.", notVerified, notVerified,
		"Muster не смог выполнить эту команду; ничего не изменилось.", notLinked}
	if !slices.Equal(texts, want) {
		t.Errorf("answers\n%q\nwant\n%q", texts, want)
	}
}

// TestAnswerCutTo500 covers answerEvent's text of at most 500 characters: a longer text is cut to 500 characters, not
// bytes; the answer is where the bridge showed it, and an answer without a shown_as of the protocol is invalid.
func TestAnswerCutTo500(t *testing.T) {
	b := startStandIn(t, answerJSON(http.StatusOK, "application/json", `{"shown_as":"thread","replayed":true}`))
	c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken})
	long := strings.Repeat("я", MaxAnswerLength+100)
	out, r := c.AnswerEvent(t.Context(), outbound.ClassInteractive, "ev-7", long)
	if !r.OK() || out.ShownAs != gen.AnswerShownShownAsThread || !out.Replayed {
		t.Fatalf("AnswerEvent = %+v, %+v", out, r)
	}
	var in gen.AnswerInput
	s := b.seen()[0]
	if err := json.Unmarshal([]byte(s.Body), &in); err != nil || in.EventId != "ev-7" ||
		utf8.RuneCountInString(in.Text) != MaxAnswerLength || in.Text != long[:2*MaxAnswerLength] ||
		s.Header.Get("Idempotency-Key") != "answer-ev-7" || s.Path != "/v1/answers" {
		t.Errorf("request %+v", s)
	}
	for _, tc := range []struct {
		status int
		body   string
		class  Class
		kind   delivery.OutcomeKind
	}{
		{http.StatusOK, `{"replayed":false}`, ClassInvalid, delivery.OutcomeUnknown},
		{http.StatusNotFound, `{"class":"gone","detail":"no such event"}`, ClassGone, delivery.OutcomeGone},
	} {
		ct := "application/json"
		if tc.status != http.StatusOK {
			ct = problemJSON
		}
		b := startStandIn(t, answerJSON(tc.status, ct, tc.body))
		c := newClient(t, network(t, nil), Settings{BaseURL: b.srv.URL, Token: testToken})
		if _, r := c.AnswerEvent(t.Context(), outbound.ClassInteractive, "ev-7", "x"); r.Classification.Class !=
			tc.class || r.Outcome().Kind != tc.kind {
			t.Errorf("%d %s = %+v", tc.status, tc.body, r)
		}
	}
}

// newTestPath is the interactive path of the tests: every call gets its token at once.
func newTestPath(now time.Time) *answerPath {
	return &answerPath{path: deliverytest.Unlimited(1, clock.Clocks{Business: clock.NewManual(now),
		Real: clock.NewManual(now)})}
}
