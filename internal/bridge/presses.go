// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
)

// Bindings are delivery's read of what an event about a message of a Bridge Connection is bound to, declared by
// their consumer; *delivery.Service implements them.
type Bindings interface {
	BridgePressBinding(ctx context.Context, connectionID int64, messageID string) (delivery.BridgeBinding, error)
}

// Links map an account of a Bridge Connection to its User; *accountlinks.Service implements them.
type Links interface {
	Lookup(ctx context.Context, identitySpace, externalID string) (accountlinks.User, error)
}

// Commands are the Commands a press runs; *groups.Service implements them.
type Commands interface {
	Acknowledge(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
	Unacknowledge(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
	Resolve(ctx context.Context, c groups.Caller, publicID string, note *string) (groups.Result, error)
	Snooze(ctx context.Context, c groups.Caller, publicID string, end groups.SnoozeEnd) (groups.Result, error)
	Unsnooze(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
	AnswerReminder(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
}

// The outcomes of a press, as bridge_press logs them, besides the outcomes of a Command that ran.
const (
	outcomeRefused        = "refused"
	outcomeForbidden      = "forbidden"
	outcomeNotLinked      = "not_linked"
	outcomeDisabled       = "disabled"
	outcomeNotVerified    = "not_verified"
	outcomeExpired        = "expired"
	outcomeFailed         = "failed"
	outcomeTest           = "test"
	outcomeUnknownMessage = "unknown_message"
)

// errUnverifiable is a verified button that names no Command of its message: a Snooze duration the Route no longer
// has, or a Command its kind of message does not carry.
var errUnverifiable = errors.New("the button names no command of its message")

// pressed is what a press led to, as bridge_press logs it, with the text it is answered with, empty for none.
type pressed struct {
	group, command, outcome string
	answer                  string
	err                     error
}

// press handles a press that is recent enough (C-22.FR-12): it returns the bridge_press line and the answer, which
// Handle makes once the claim committed. Its error — a read of the binding or of the Account link that failed, or a
// Command that ended with ctx — fails the event, so that it comes again.
func (e *Events) press(ctx context.Context, c Conn, ev Event) (handled, error) {
	r, err := e.pressOf(ctx, c, ev)
	if err != nil {
		return handled{}, err
	}
	return handled{line: logging.BridgePress, fields: []logging.Field{logging.F("connection", c.PublicID),
		logging.F("group", r.group), logging.F("command", r.command), logging.F("outcome", r.outcome),
		logging.F("external_id", ev.User.ExternalId)}, answer: r.answer, err: r.err}, nil
}

// pressOf verifies, binds and runs the press ev of the Connection c. The data — the compact action id, its key id
// prefix and its signature — is verified with the key of the Keyring its prefix names. Verified data of a test message
// (C-22.FR-17) changes nothing and is answered so. Otherwise the pressed message must be a current or older Root message
// or a sent Reminder of a Bridge Destination of the Connection, else the press is ignored without an answer; and the
// data must have been issued for it — its kind and its Alert Group — else it is answered "not verified", like a bad
// signature, or, for a Reminder whose key left the Keyring, that the button expired (C-17.FR-6). The account is mapped
// to its User through its Account link in the Connection's identity space before any answer, so that every answer is
// in the profile language of the linked User, in English without a link (C-22.FR-13). The Command runs as that User
// with the Transport bridge through the dispatcher, which edits no message here: delivery reconciles the current Root
// message, never an older one (C-22.FR-9). A Command that ran is not answered; a refusal, a failure, an account without
// a link and a disabled User are.
func (e *Events) pressOf(ctx context.Context, c Conn, ev Event) (pressed, error) {
	a, v := e.verify(ev.Data)
	if v == verified && a.Subject == buttons.SubjectTest {
		u, linked, err := e.lookup(ctx, c, ev)
		if err != nil {
			return pressed{}, err
		}
		return pressed{command: a.Command, outcome: outcomeTest,
			answer: messages.T(language(u, linked), "press.test", nil)}, nil
	}
	b, err := e.Bindings.BridgePressBinding(ctx, c.ID, ev.MessageID)
	if errors.Is(err, delivery.ErrNotBound) {
		return pressed{outcome: outcomeUnknownMessage}, nil
	}
	if err != nil {
		return pressed{}, err
	}
	u, linked, err := e.lookup(ctx, c, ev)
	if err != nil {
		return pressed{}, err
	}
	lang := language(u, linked)
	switch {
	case v == expired && a.Subject == buttons.SubjectReply && b.Kind == buttons.SubjectReply &&
		a.PublicID == b.GroupPublicID:
		return pressed{outcome: outcomeExpired, answer: messages.T(lang, "press.expired", nil)}, nil
	case v != verified || a.Subject != b.Kind || a.PublicID != b.GroupPublicID:
		return pressed{outcome: outcomeNotVerified, answer: messages.T(lang, "press.notVerified", nil)}, nil
	}
	r := pressed{group: a.PublicID, command: a.Command}
	switch {
	case !linked:
		r.outcome = outcomeNotLinked
		r.answer = messages.T(messages.LanguageEnglish, "bridge.notLinked",
			messages.Args{"link": strings.TrimSuffix(e.PublicURL, "/") + "/profile"})
		return r, nil
	case u.Status == accountlinks.StatusDisabled:
		r.outcome, r.answer = outcomeDisabled, messages.T(lang, "press.disabled", nil)
		return r, nil
	}
	snooze, err := snoozeOf(a, b)
	if err == nil {
		var res groups.Result
		res, err = e.run(ctx, e.caller(u), a.Command, a.PublicID, snooze)
		if err == nil {
			r.outcome = string(res.Outcome)
			return r, nil
		}
	}
	if ctx.Err() != nil {
		return pressed{}, fmt.Errorf("run the command: %w", err)
	}
	r.outcome, r.answer, r.err = refusal(lang, err)
	return r, nil
}

// verdict is how the data of a press verified.
type verdict int

// The verdicts on the data of a press: verified; expired, a well-formed action id whose key left the Keyring, whose
// unverified Action may only choose an answer; invalid, anything else.
const (
	verified verdict = iota
	expired
	invalid
)

// verify reads and verifies the data of a press: longer than buttons.MaxLen it is not an action id at all.
func (e *Events) verify(data string) (buttons.Action, verdict) {
	if len(data) > buttons.MaxLen {
		return buttons.Action{}, invalid
	}
	a, err := buttons.Verify(e.Keys, data, "")
	switch {
	case err == nil:
		return a, verified
	case errors.Is(err, buttons.ErrExpired):
		return a, expired
	}
	return buttons.Action{}, invalid
}

// lookup is the User the account of ev is linked to in the identity space of the Connection c, and whether there is
// one; an error other than ErrNotLinked is the read's.
func (e *Events) lookup(ctx context.Context, c Conn, ev Event) (accountlinks.User, bool, error) {
	u, err := e.Links.Lookup(ctx, accountlinks.SpaceBridge(c.ID), ev.User.ExternalId)
	switch {
	case errors.Is(err, accountlinks.ErrNotLinked):
		return accountlinks.User{}, false, nil
	case err != nil:
		return accountlinks.User{}, false, err
	}
	return u, true, nil
}

// language is the language of the answers to a person: the profile language of the User the account is linked to —
// messages.T takes an empty or unknown one for English — and English for an account without a link.
func language(u accountlinks.User, linked bool) string {
	if !linked {
		return messages.LanguageEnglish
	}
	return u.Language
}

// caller is the linked User u as the dispatcher takes it: with the Permissions of its Role and the Transport bridge.
func (e *Events) caller(u accountlinks.User) groups.Caller {
	return groups.Caller{Actor: audit.User(u.ID, u.PublicID), Transport: audit.TransportBridge,
		Permissions: e.Roles.Permissions(u.Role)}
}

// snoozeOf is the Snooze duration of the button a on its message b, the Route's duration at the button's index, zero
// for any other Command; errUnverifiable for a Command the kind of message does not carry — "Still on it" and
// Unacknowledge on a Reminder, any other on a Root message — or an index the Route no longer has.
func snoozeOf(a buttons.Action, b delivery.BridgeBinding) (time.Duration, error) {
	carried := a.Command != buttons.CommandStillOnIt
	if b.Kind == buttons.SubjectReply {
		carried = a.Command == buttons.CommandStillOnIt || a.Command == buttons.CommandUnacknowledge
	}
	switch {
	case !carried:
		return 0, errUnverifiable
	case a.Command != buttons.CommandSnooze:
		return 0, nil
	case a.Argument >= len(b.SnoozeSeconds):
		return 0, errUnverifiable
	}
	return time.Duration(b.SnoozeSeconds[a.Argument]) * time.Second, nil
}

// run runs command on the Alert Group publicID as c through the dispatcher; a Snooze lasts snooze from now on the
// business clock.
func (e *Events) run(ctx context.Context, c groups.Caller, command, publicID string, snooze time.Duration) (
	groups.Result, error) {
	switch command {
	case buttons.CommandStillOnIt:
		return e.Commands.AnswerReminder(ctx, c, publicID)
	case buttons.CommandAcknowledge:
		return e.Commands.Acknowledge(ctx, c, publicID)
	case buttons.CommandUnacknowledge:
		return e.Commands.Unacknowledge(ctx, c, publicID)
	case buttons.CommandResolve:
		return e.Commands.Resolve(ctx, c, publicID, nil)
	case buttons.CommandUnsnooze:
		return e.Commands.Unsnooze(ctx, c, publicID)
	case buttons.CommandSnooze:
		until := e.Clocks.Business.Now().Add(snooze)
		return e.Commands.Snooze(ctx, c, publicID, groups.SnoozeEnd{Until: &until})
	}
	return groups.Result{}, errUnverifiable
}

// refusal is the outcome and the answer, in lang, of a Command that did not run because of err, and the error to log:
// a button that names no Command of its message is not verified, a missing Permission forbidden (C-18.FR-8), a
// precondition that does not hold the dispatcher's refusal (C-10.FR-2), anything else a failure, logged with err.
func refusal(lang string, err error) (outcome, answer string, logged error) {
	var forbidden *groups.ForbiddenError
	var refused *groups.RefusedError
	switch {
	case errors.Is(err, errUnverifiable):
		return outcomeNotVerified, messages.T(lang, "press.notVerified", nil), nil
	case errors.As(err, &forbidden):
		return outcomeForbidden, messages.T(lang, "press.forbidden", nil), nil
	case errors.As(err, &refused):
		return outcomeRefused, refused.Detail(), nil
	}
	return outcomeFailed, messages.T(lang, "press.failed", nil), err
}

// answer shows text to the person behind the event eventID of the Connection c with answerEvent on the interactive
// path (C-22.FR-13): limited by the Connection's limiter only, whose token it takes ahead of deliveries, within
// Budget, in the interactive client class; a RetryAfter it gets holds the Connection. It is made once, never again:
// its error says why it was not made.
func (e *Events) answer(ctx context.Context, c Conn, eventID, text string) error {
	budget := e.Budget
	if budget <= 0 {
		budget = delivery.InteractiveBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	id := c.ID
	out, err := e.Path.Do(ctx, delivery.Subject{Connection: &id}, delivery.AnswerOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			_, r := c.Client.AnswerEvent(ctx, call.Class, eventID, text)
			o := r.Outcome()
			if o.Kind == delivery.OutcomeRetryAfter {
				o.Scope = delivery.ScopeConnection
			}
			return o
		}))
	switch {
	case err != nil:
		return fmt.Errorf("the event was not answered: %w", err)
	case out.Kind != delivery.OutcomeOK:
		return errors.New("the event was not answered: " + cmp.Or(string(out.Error), string(out.Kind)))
	}
	return nil
}
