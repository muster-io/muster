// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/groups"
)

// said is a thread_message event id of the account externalID in the Thread of the message messageID with text.
func (e *eventsEnv) said(t *testing.T, id, messageID, externalID, text string) Event {
	t.Helper()
	return e.event(t, TypeThreadMessage, member("id", id), member("message_id", messageID), member("text", text),
		userMember("external_id", externalID))
}

// textLine is the start of the bridge_text_command line of a Text command.
func textLine(group, command, outcome, externalID string) string {
	return fmt.Sprintf(`"event":"bridge_text_command","connection":"CN0000000000B3","group":%q,"command":%q,`+
		`"outcome":%q,"external_id":%q`, group, command, outcome, externalID)
}

// texts are the texts of the answers the bridge was asked to show, in order.
func (e *pressEnv) texts(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, a := range e.answers(t) {
		out = append(out, a.text)
	}
	return out
}

// TestTextCommandGrammar covers C-22.FR-14 and bridge.text_commands: the whole text, trimmed, case-insensitive and
// with an optional leading slash, is a Text command in English or in Russian, ё written as е or not; a Snooze takes a
// count of minutes, hours or days in either language; any other text is none.
func TestTextCommandGrammar(t *testing.T) {
	for _, tc := range []struct {
		text    string
		command string
		seconds int64
	}{
		{"ack", buttons.CommandAcknowledge, 0},
		{"/ACK", buttons.CommandAcknowledge, 0},
		{"  Ack \n", buttons.CommandAcknowledge, 0},
		{"unack", buttons.CommandUnacknowledge, 0},
		{"resolve", buttons.CommandResolve, 0},
		{"unsnooze", buttons.CommandUnsnooze, 0},
		{"still on it", buttons.CommandStillOnIt, 0},
		{"Still  On\tIt", buttons.CommandStillOnIt, 0},
		{"/still on it", buttons.CommandStillOnIt, 0},
		{"взял", buttons.CommandAcknowledge, 0},
		{"отпускаю", buttons.CommandUnacknowledge, 0},
		{"Решено", buttons.CommandResolve, 0},
		{"/решено", buttons.CommandResolve, 0},
		{"вернуть", buttons.CommandUnsnooze, 0},
		{"ещё работаю", buttons.CommandStillOnIt, 0},
		{"еще работаю", buttons.CommandStillOnIt, 0},
		{"ЕЩЁ РАБОТАЮ", buttons.CommandStillOnIt, 0},
		{"snooze 30m", buttons.CommandSnooze, 1800},
		{"snooze 4h", buttons.CommandSnooze, 14400},
		{"/Snooze 4H", buttons.CommandSnooze, 14400},
		{"snooze 1d", buttons.CommandSnooze, 86400},
		{"snooze 24h", buttons.CommandSnooze, 86400},
		{"snooze 0m", buttons.CommandSnooze, 0},
		{"snooze 9999d", buttons.CommandSnooze, 9999 * 86400},
		{"отложить 30м", buttons.CommandSnooze, 1800},
		{"отложить 4ч", buttons.CommandSnooze, 14400},
		{"Отложить 1Д", buttons.CommandSnooze, 86400},
		{"snooze 4ч", buttons.CommandSnooze, 14400},
		{"отложить 4h", buttons.CommandSnooze, 14400},
	} {
		got, ok := parseTextCommand(tc.text)
		if !ok || got != (textCommand{command: tc.command, seconds: tc.seconds}) {
			t.Errorf("%q = %+v, %v", tc.text, got, ok)
		}
	}
	for _, text := range []string{"", " ", "/", "looking into it", "ack please", "please ack", "acknowledge", "ack!",
		"//ack", "snooze", "snooze 4", "snooze 4 h", "snooze h", "snooze 4x", "snooze 4hours", "snooze 10000m",
		"snooze -4h", "snooze 4h now", "отложить", "отложить 4 ч", "still on", "on it", "ещё", "/ack@bot",
		"unsnooze 4h", "snooze4h"} {
		if got, ok := parseTextCommand(text); ok {
			t.Errorf("%q is a Text command: %+v", text, got)
		}
	}
}

// TestTextCommandRuns covers C-22.FR-14 and AC-16: a Text command from an account linked to a Responder, in the Thread
// of the current Root message, runs its Command on the Alert Group as that User with the Permissions of its Role and
// the Transport bridge, looked up in the identity space of the Connection, and is answered "Done: {command} #N";
// bridge_text_command logs the Alert Group, the Command, the outcome and the account.
func TestTextCommandRuns(t *testing.T) {
	e := newPressEnv(t)
	for i, text := range []string{"ack", "unack", "resolve", "unsnooze", "still on it"} {
		e.handle(t, e.said(t, fmt.Sprintf("ev-%d", i), "m1", "u-bob", text))
	}
	calls := e.commands.ran()
	var names []string
	for _, r := range calls {
		names = append(names, r.command)
		if r.group != pressGroup || r.caller.Actor != audit.User(7, "SR0000000000B1") ||
			r.caller.Transport != audit.TransportBridge ||
			!slices.Equal(r.caller.Permissions, pressRoles.Permissions(auth.RoleResponder)) {
			t.Errorf("ran %+v", r)
		}
	}
	if !slices.Equal(names, []string{"acknowledge", "unacknowledge", "resolve", "unsnooze", "still_on_it"}) {
		t.Fatalf("commands %v", names)
	}
	if got := e.texts(t); !slices.Equal(got, []string{"Done: Acknowledged #1", "Done: Unacknowledged #1",
		"Done: Resolved #1", "Done: Unsnoozed #1", "Done: Still on it #1"}) {
		t.Errorf("answers %q", got)
	}
	for i, a := range e.answers(t) {
		if a.eventID != fmt.Sprintf("ev-%d", i) || a.key != "answer-"+a.eventID {
			t.Errorf("answer %d %+v", i, a)
		}
	}
	got := e.log.String()
	for _, c := range names {
		if !strings.Contains(got, textLine(pressGroup, c, "done", "u-bob")+"}") {
			t.Errorf("the log lacks %s:\n%s", c, got)
		}
	}
	if strings.Count(got, "\n") != 5 || !slices.Equal(e.links.spaces, slices.Repeat([]string{"bridge:3"}, 5)) {
		t.Errorf("logged %s, looked up in %v", got, e.links.spaces)
	}
	if !e.store.recorded(e.conn.ID, "ev-4") {
		t.Error("the Text command was not recorded")
	}
}

// TestTextCommandInRussian covers C-22.FR-13 and FR-14: the Russian synonyms run their Commands, and the answers are in
// the profile language of the linked User — Russian for a Russian-speaking one whatever the language of the words,
// English for one who wrote in Russian with an English profile.
func TestTextCommandInRussian(t *testing.T) {
	e := newPressEnv(t)
	e.handle(t, e.said(t, "ev-1", "m1", "u-ivan", "Взял"))
	e.handle(t, e.said(t, "ev-2", "m1", "u-ivan", "еще работаю"))
	e.handle(t, e.said(t, "ev-3", "m1", "u-ivan", "отложить 4ч"))
	e.handle(t, e.said(t, "ev-4", "m1", "u-ivan", "resolve"))
	e.handle(t, e.said(t, "ev-5", "m1", "u-bob", "решено"))
	if got := e.texts(t); !slices.Equal(got, []string{"Готово: Подтверждено #1", "Готово: Ещё работаю #1",
		"Готово: Отложено #1", "Готово: Закрыто #1", "Done: Resolved #1"}) {
		t.Errorf("answers %q", got)
	}
	var names []string
	for _, r := range e.commands.ran() {
		names = append(names, r.command)
	}
	if !slices.Equal(names, []string{"acknowledge", "still_on_it", "snooze", "resolve", "resolve"}) {
		t.Errorf("commands %v", names)
	}
}

// TestTextCommandSnoozeDurations covers C-22.FR-14 and AC-16: a Snooze runs for one of the Route's durations however
// it is written, from the business clock; any other duration changes nothing and is answered with the Route's
// durations as Text commands write them, in the profile language.
func TestTextCommandSnoozeDurations(t *testing.T) {
	e := newPressEnv(t)
	for i, text := range []string{"/Snooze 4h", "snooze 1d", "snooze 24h", "snooze 60m", "отложить 240м"} {
		e.handle(t, e.said(t, fmt.Sprintf("ev-%d", i), "m1", "u-bob", text))
	}
	calls := e.commands.ran()
	if len(calls) != 5 {
		t.Fatalf("commands %+v", calls)
	}
	for i, want := range []time.Duration{4 * time.Hour, 24 * time.Hour, 24 * time.Hour, time.Hour, 4 * time.Hour} {
		if c := calls[i]; c.command != buttons.CommandSnooze || c.end == nil || c.end.Until == nil ||
			!c.end.Until.Equal(eventTime.Add(want)) || c.end.NoEnd {
			t.Errorf("%d: snoozed %+v, want %v", i, c.end, want)
		}
	}
	if got := e.texts(t); !slices.Equal(got, slices.Repeat([]string{"Done: Snoozed #1"}, 5)) {
		t.Errorf("answers %q", got)
	}

	e = newPressEnv(t)
	e.handle(t, e.said(t, "ev-1", "m1", "u-bob", "snooze 2h"))
	e.handle(t, e.said(t, "ev-2", "m1", "u-ivan", "отложить 2ч"))
	e.handle(t, e.said(t, "ev-3", "m1", "u-bob", "snooze 0m"))
	if calls := e.commands.ran(); len(calls) != 0 {
		t.Errorf("a duration the Route does not have ran %+v", calls)
	}
	if got := e.texts(t); !slices.Equal(got, []string{"Snooze for one of: 1h, 4h, 24h",
		"Отложить можно на: 1ч, 4ч, 24ч", "Snooze for one of: 1h, 4h, 24h"}) {
		t.Errorf("answers %q", got)
	}
	got := e.log.String()
	if strings.Count(got, textLine(pressGroup, "snooze", "unknown_duration", "u-bob")+"}") != 2 ||
		strings.Count(got, textLine(pressGroup, "snooze", "unknown_duration", "u-ivan")+"}") != 1 {
		t.Errorf("logged %s", got)
	}
}

// TestTextCommandSnoozeWithoutDurations covers C-22.FR-14: on a Route without Snooze durations, a Snooze from the
// Thread changes nothing and is answered that the Route has none, in the profile language, never with an empty list.
func TestTextCommandSnoozeWithoutDurations(t *testing.T) {
	e := newPressEnv(t)
	for id, b := range e.bindings.bound {
		b.SnoozeSeconds = nil
		e.bindings.bound[id] = b
	}
	e.handle(t, e.said(t, "ev-1", "m1", "u-bob", "snooze 4h"))
	e.handle(t, e.said(t, "ev-2", "m0", "u-ivan", "отложить 4ч"))
	if calls := e.commands.ran(); len(calls) != 0 {
		t.Errorf("a Snooze without durations ran %+v", calls)
	}
	if got := e.texts(t); !slices.Equal(got, []string{
		"This Alert Group's Route has no Snooze durations; nothing was changed.",
		"У маршрута этой группы алертов нет сроков откладывания; ничего не изменилось."}) {
		t.Errorf("answers %q", got)
	}
	got := e.log.String()
	if !strings.Contains(got, textLine(pressGroup, "snooze", "unknown_duration", "u-bob")+"}") ||
		!strings.Contains(got, textLine(pressGroup, "snooze", "unknown_duration", "u-ivan")+"}") {
		t.Errorf("logged %s", got)
	}
}

// TestTextCommandStillOnIt covers C-22.FR-14 and C-17.FR-10: "still on it" answers the open Reminder through the
// dispatcher, which refuses it when no Reminder waits or the writer is not the Owner, and the refusal is the answer.
func TestTextCommandStillOnIt(t *testing.T) {
	e := newPressEnv(t)
	e.handle(t, e.said(t, "ev-1", "m1", "u-bob", "still on it"))
	e.commands.err = &groups.RefusedError{Code: groups.CodeNoReminderPending,
		Message: "no Reminder waits for an answer"}
	e.handle(t, e.said(t, "ev-2", "m1", "u-bob", "Still on it"))
	if calls := e.commands.ran(); len(calls) != 2 || calls[0].command != buttons.CommandStillOnIt ||
		calls[1].command != buttons.CommandStillOnIt {
		t.Errorf("commands %+v", calls)
	}
	if got := e.texts(t); !slices.Equal(got, []string{"Done: Still on it #1", "No Reminder waits for an answer."}) {
		t.Errorf("answers %q", got)
	}
	if got := e.log.String(); !strings.Contains(got, textLine(pressGroup, "still_on_it", "refused", "u-bob")+"}") {
		t.Errorf("logged %s", got)
	}
}

// TestTextCommandRefusals covers C-22.FR-14 and AC-16: a Viewer's Text command is refused with C-10's text, a disabled
// User's and an unlinked account's are answered as their presses are, a refusal of the dispatcher is answered with its
// text and a failed Command that nothing changed, logged with its error; none of them changes anything else.
func TestTextCommandRefusals(t *testing.T) {
	e := newPressEnv(t)
	e.handle(t, e.said(t, "ev-1", "m1", "u-vera", "resolve"))
	e.handle(t, e.said(t, "ev-2", "m1", "u-dora", "ack"))
	e.handle(t, e.said(t, "ev-3", "m1", "u-alice", "snooze 2h"))
	e.commands.err = &groups.RefusedError{Code: "already_resolved", Message: "the Alert Group is resolved"}
	e.handle(t, e.said(t, "ev-4", "m1", "u-bob", "ack"))
	e.commands.err = errors.New("the dispatcher broke")
	e.handle(t, e.said(t, "ev-5", "m1", "u-ivan", "ack"))
	if got := e.texts(t); !slices.Equal(got, []string{"You are not permitted to do this",
		"Your Muster account is disabled", notLinked, "The Alert Group is resolved.",
		"Muster не смог выполнить эту команду; ничего не изменилось."}) {
		t.Errorf("answers %q", got)
	}
	if calls := e.commands.ran(); len(calls) != 3 || calls[0].caller.Actor != audit.User(8, "SR0000000000V1") {
		t.Errorf("commands %+v", calls)
	}
	got := e.log.String()
	for _, want := range []string{textLine(pressGroup, "resolve", "forbidden", "u-vera") + "}",
		textLine(pressGroup, "acknowledge", "disabled", "u-dora") + "}",
		textLine(pressGroup, "snooze", "not_linked", "u-alice") + "}",
		textLine(pressGroup, "acknowledge", "refused", "u-bob") + "}",
		textLine(pressGroup, "acknowledge", "failed", "u-ivan") + `,"error":"the dispatcher broke"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %s:\n%s", want, got)
		}
	}

	e = newPressEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.commands.hook = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}
	err := e.events.Handle(ctx, e.conn, e.said(t, "ev-1", "m1", "u-bob", "ack"))
	if !errors.Is(err, context.Canceled) || e.store.recorded(e.conn.ID, "ev-1") || len(e.answers(t)) != 0 ||
		e.log.String() != "" {
		t.Errorf("a Command cut short = %v, logged %s", err, e.log.String())
	}
}

// TestTextCommandOtherTextIgnored covers C-22.FR-14 and AC-16: a Thread message that is no Text command changes
// nothing, reads nothing, and is neither answered nor logged; its event is recorded.
func TestTextCommandOtherTextIgnored(t *testing.T) {
	e := newPressEnv(t)
	for i, text := range []string{"looking into it", "ack please", "snooze", "snooze 4 h", "/ack@bot"} {
		e.handle(t, e.said(t, fmt.Sprintf("ev-%d", i), "m1", "u-bob", text))
		if !e.store.recorded(e.conn.ID, fmt.Sprintf("ev-%d", i)) {
			t.Errorf("%q was not recorded", text)
		}
	}
	if e.log.String() != "" || len(e.answers(t)) != 0 || len(e.commands.ran()) != 0 || e.bindings.count() != 0 ||
		len(e.links.spaces) != 0 {
		t.Errorf("other text: logged %s, answers %+v, commands %+v", e.log.String(), e.answers(t), e.commands.ran())
	}
}

// TestTextCommandTooOld covers C-22.FR-14 and AC-16: a Text command older than bridge.event_max_age by the real clock
// is dropped — nothing read, run or answered — and logged with the outcome dropped; one at the limit runs.
func TestTextCommandTooOld(t *testing.T) {
	e := newPressEnv(t)
	e.real.Set(eventTime.Add(EventMaxAge + time.Second))
	e.handle(t, e.said(t, "ev-1", "m1", "u-bob", "ack"))
	if len(e.answers(t)) != 0 || len(e.commands.ran()) != 0 || e.bindings.count() != 0 ||
		e.log.String() == "" || !strings.Contains(e.log.String(), textLine("", "acknowledge", "dropped", "u-bob")+"}") {
		t.Errorf("an old Text command: logged %s, answers %+v", e.log.String(), e.answers(t))
	}
	if !e.store.recorded(e.conn.ID, "ev-1") {
		t.Error("a dropped Text command was not recorded")
	}
	e.real.Set(eventTime.Add(EventMaxAge))
	e.handle(t, e.said(t, "ev-2", "m1", "u-bob", "ack"))
	if len(e.commands.ran()) != 1 {
		t.Errorf("a Text command at the limit did not run: %s", e.log.String())
	}
	e.real.Set(eventTime.Add(2 * EventMaxAge))
	e.handle(t, e.said(t, "ev-3", "m1", "u-bob", "looking into it"))
	if strings.Count(e.log.String(), "\n") != 2 {
		t.Errorf("an old other text was logged: %s", e.log.String())
	}
}

// TestTextCommandUnknownMessage covers C-22.FR-11 and FR-14: a Text command in the Thread of a message Muster did not
// post as a Root message through this Connection — an unknown one, or a Reminder, which is a reply — changes nothing
// and is not answered, logged with the outcome unknown_message, the account never looked up.
func TestTextCommandUnknownMessage(t *testing.T) {
	e := newPressEnv(t)
	e.handle(t, e.said(t, "ev-1", "m9", "u-bob", "ack"))
	e.handle(t, e.said(t, "ev-2", "r1", "u-bob", "still on it"))
	other := e.conn
	other.ID = pressConnID + 1
	if err := e.events.Handle(t.Context(), other, e.said(t, "ev-3", "m1", "u-bob", "ack")); err != nil {
		t.Fatal(err)
	}
	if len(e.answers(t)) != 0 || len(e.commands.ran()) != 0 || len(e.links.spaces) != 0 {
		t.Errorf("answers %+v, commands %+v", e.answers(t), e.commands.ran())
	}
	got := e.log.String()
	if strings.Count(got, textLine("", "acknowledge", "unknown_message", "u-bob")+"}") != 2 ||
		!strings.Contains(got, textLine("", "still_on_it", "unknown_message", "u-bob")+"}") {
		t.Errorf("logged %s", got)
	}
}

// TestTextCommandOnTheOlderRootMessage covers C-22.FR-9 and FR-14: a Text command in the Thread of an older Root
// message, replaced past the bridge's edit window, acts on the Alert Group as on the current one.
func TestTextCommandOnTheOlderRootMessage(t *testing.T) {
	e := newPressEnv(t)
	e.handle(t, e.said(t, "ev-1", "m0", "u-bob", "ack"))
	if calls := e.commands.ran(); len(calls) != 1 || calls[0].group != pressGroup ||
		calls[0].command != buttons.CommandAcknowledge {
		t.Errorf("commands %+v", calls)
	}
	if got := e.texts(t); !slices.Equal(got, []string{"Done: Acknowledged #1"}) {
		t.Errorf("answers %q", got)
	}
}

// TestTextCommandReadFailures covers C-22.FR-11: a read of the binding or of the Account link that fails fails the
// event — logged as bridge_event_failed, unanswered and unrecorded, so that it comes again.
func TestTextCommandReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(e *pressEnv)
	}{
		{"binding", func(e *pressEnv) { e.bindings.err = errors.New("the database went away") }},
		{"link", func(e *pressEnv) { e.links.err = errors.New("the database went away") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPressEnv(t)
			tc.set(e)
			err := e.events.Handle(t.Context(), e.conn, e.said(t, "ev-1", "m1", "u-bob", "ack"))
			if err == nil || e.store.recorded(e.conn.ID, "ev-1") || len(e.answers(t)) != 0 ||
				len(e.commands.ran()) != 0 || !strings.Contains(e.log.String(), `"event":"bridge_event_failed"`) ||
				strings.Contains(e.log.String(), "bridge_text_command") {
				t.Errorf("Handle = %v, logged %s", err, e.log.String())
			}
		})
	}
}
