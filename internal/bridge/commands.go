// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
)

// The outcomes of a Text command, as bridge_text_command logs them, besides those it shares with presses.
const (
	outcomeDropped         = "dropped"
	outcomeUnknownDuration = "unknown_duration"
)

// textCommand is a Text command: the Command it runs, and for a Snooze the duration it was written with, in seconds.
type textCommand struct {
	command string
	seconds int64
}

// vocabulary are the words of Text commands (bridge.text_commands) in every language of the built-in texts, as
// normalize leaves them: the whole text of each Command without a duration, the word that a Snooze duration follows,
// and the unit of each kind of duration in seconds.
type vocabulary struct {
	commands map[string]string
	snooze   map[string]bool
	units    map[string]int64
}

// commandWords is the vocabulary of Text commands, from the texts the commands line of a Root message shows them with
// (textCommand.*), so that a person writes back what the message offers.
var commandWords = newVocabulary()

// textKeys are the keys of the texts of the Commands a Text command runs; a Snooze is textCommand.snooze.
var textKeys = map[string]string{
	buttons.CommandAcknowledge:   "acknowledge",
	buttons.CommandUnacknowledge: "unacknowledge",
	buttons.CommandResolve:       "resolve",
	buttons.CommandUnsnooze:      "unsnooze",
	buttons.CommandStillOnIt:     "stillOnIt",
	buttons.CommandSnooze:        "snooze",
}

func newVocabulary() vocabulary {
	v := vocabulary{commands: map[string]string{}, snooze: map[string]bool{}, units: map[string]int64{}}
	units := map[string]int64{"minutes": 60, "hours": 3600, "days": 86400}
	for _, lang := range messages.Languages {
		for command, key := range textKeys {
			text := messages.T(lang, "textCommand."+key, messages.Args{"duration": ""})
			if command == buttons.CommandSnooze {
				v.snooze[normalize(text)] = true
				continue
			}
			v.commands[normalize(text)] = command
		}
		for key, seconds := range units {
			v.units[normalize(messages.T(lang, "textCommand."+key, messages.Args{"count": ""}))] = seconds
		}
	}
	return v
}

// normalize is a text as Text commands compare it (C-22.FR-14): trimmed, without one leading slash, its runs of white
// space one space, in lower case, with ё as е, so that "ещё работаю" and "еще работаю" are one Text command.
func normalize(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "/")
	return strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(s), " ")), "ё", "е")
}

// durationRe is a Snooze duration as a Text command writes it: a count of up to four digits and its unit, as 30m, 4h,
// 1d, 30м, 4ч or 1д.
var durationRe = regexp.MustCompile(`^([0-9]{1,4})(\pL+)$`)

// parseTextCommand reads text as a Text command of C-22.FR-14: the whole text, trimmed, case-insensitive, with an
// optional leading slash, is one of ack, unack, resolve, snooze <duration>, unsnooze and still on it, or their Russian
// synonyms, in either language whatever the person's; a duration is a count of minutes, hours or days. Anything else is
// no Text command.
func parseTextCommand(text string) (textCommand, bool) {
	s := normalize(text)
	if command, ok := commandWords.commands[s]; ok {
		return textCommand{command: command}, true
	}
	word, duration, ok := strings.Cut(s, " ")
	if !ok || !commandWords.snooze[word] {
		return textCommand{}, false
	}
	m := durationRe.FindStringSubmatch(duration)
	if m == nil {
		return textCommand{}, false
	}
	unit, ok := commandWords.units[m[2]]
	if !ok {
		return textCommand{}, false
	}
	count, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return textCommand{}, false
	}
	return textCommand{command: buttons.CommandSnooze, seconds: count * unit}, true
}

// thread handles a thread_message event (C-22.FR-14): a text that is no Text command is neither answered nor logged;
// a Text command returns the bridge_text_command line and the answer, which Handle makes once the claim committed. Its
// error — a read of the binding or of the Account link that failed, or a Command that ended with ctx — fails the
// event, so that it comes again.
func (e *Events) thread(ctx context.Context, c Conn, ev Event) (handled, error) {
	tc, ok := parseTextCommand(ev.Text)
	if !ok {
		return handled{}, nil
	}
	r, err := e.textCommandOf(ctx, c, ev, tc)
	if err != nil {
		return handled{}, err
	}
	return r.handled(logging.BridgeTextCommand, c, ev), nil
}

// textCommandOf runs the Text command tc of ev in the Thread of a message of the Connection c. One older than MaxAge by
// the real clock is dropped, unanswered. The message must be a current or older Root message of a Bridge Destination
// of the Connection, else the Text command is ignored without an answer. The account is mapped to its User through its
// Account link in the Connection's identity space; an account without a link and a disabled User are answered as
// their presses are. A Snooze for a duration that is not one of the Route's is answered with the Route's. The Command
// runs as that User with the Transport bridge through the dispatcher — "still on it" answers the open Reminder
// (C-17.FR-10) — and is answered "Done: {command} #N", or with its refusal (C-10.FR-2), in the profile language of the
// linked User.
func (e *Events) textCommandOf(ctx context.Context, c Conn, ev Event, tc textCommand) (pressed, error) {
	r := pressed{command: tc.command}
	if _, old := e.tooOld(ev); old {
		r.outcome = outcomeDropped
		return r, nil
	}
	b, err := e.Bindings.BridgeThreadBinding(ctx, c.ID, ev.MessageID)
	if errors.Is(err, delivery.ErrNotBound) {
		r.outcome = outcomeUnknownMessage
		return r, nil
	}
	if err != nil {
		return pressed{}, err
	}
	r.group = b.GroupPublicID
	u, linked, err := e.lookup(ctx, c, ev)
	if err != nil {
		return pressed{}, err
	}
	lang := language(u, linked)
	switch {
	case !linked:
		r.outcome, r.answer = outcomeNotLinked, e.notLinked()
		return r, nil
	case u.Status == accountlinks.StatusDisabled:
		r.outcome, r.answer = outcomeDisabled, messages.T(lang, "press.disabled", nil)
		return r, nil
	case tc.command == buttons.CommandSnooze && !slices.Contains(b.SnoozeSeconds, tc.seconds):
		durations := make([]string, len(b.SnoozeSeconds))
		for i, s := range b.SnoozeSeconds {
			durations[i] = messages.CommandDuration(lang, s)
		}
		r.outcome = outcomeUnknownDuration
		r.answer = messages.T(lang, "bridge.snoozeDurations", messages.Args{"durations": strings.Join(durations, ", ")})
		return r, nil
	}
	res, err := e.run(ctx, e.caller(u), tc.command, b.GroupPublicID, time.Duration(tc.seconds)*time.Second)
	if err == nil {
		r.outcome = string(res.Outcome)
		r.answer = messages.T(lang, "bridge.done", messages.Args{
			"command": messages.T(lang, "bridge.did."+textKeys[tc.command], nil),
			"number":  strconv.FormatInt(res.Group.Number, 10)})
		return r, nil
	}
	if ctx.Err() != nil {
		return pressed{}, fmt.Errorf("run the command: %w", err)
	}
	r.outcome, r.answer, r.err = refusal(lang, err)
	return r, nil
}
