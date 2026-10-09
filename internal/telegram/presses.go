// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/leader"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
)

// PressMaxAge is telegram.press_max_age (C-14.FR-4): a press whose update arrives after the Connection received no
// updates for longer is dropped.
const PressMaxAge = time.Hour

// MaxAnswerLength is the longest text of an answer to a press, in characters (C-14.FR-5).
const MaxAnswerLength = 200

// Outage is when Muster was last known not to run, as the Leader recorded it: its alive mark, and the start and end
// of the latest downtime period; the zero time for none.
type Outage struct {
	AliveAt       time.Time
	DowntimeStart time.Time
	DowntimeEnd   time.Time
}

// Outages read the Outage; *connections.Service implements it.
type Outages interface {
	Outage(ctx context.Context) (Outage, error)
}

// quietSince is when the updates of a Connection were last received, for a replica that starts receiving them at
// start without having received one before, and whether Muster was not running just before start: then it is the
// start of that outage — a downtime that ended no earlier than leader.AbsenceNotice before start, which the Leader
// records when it takes over, or, before it recorded it, an alive mark older than that. Otherwise it is start: Muster
// was running and receiving.
func (o Outage) quietSince(start time.Time) (time.Time, bool) {
	since, out := start, false
	if !o.DowntimeEnd.IsZero() && !o.DowntimeEnd.Before(start.Add(-leader.AbsenceNotice)) &&
		o.DowntimeStart.Before(since) {
		since, out = o.DowntimeStart, true
	}
	if !o.AliveAt.IsZero() && o.AliveAt.Before(start.Add(-leader.AbsenceNotice)) && o.AliveAt.Before(since) {
		since, out = o.AliveAt, true
	}
	return since, out
}

// Bindings are delivery's read of what a press is bound to; *delivery.Service implements it.
type Bindings interface {
	TelegramPressBinding(ctx context.Context, connectionID int64, groupPublicID string, chatID, messageID int64) (
		delivery.Binding, error)
}

// Links map a Telegram account to its User; *accountlinks.Service implements it.
type Links interface {
	Lookup(ctx context.Context, identitySpace, externalID string) (accountlinks.User, error)
}

// Commands are the Commands a button runs; *groups.Service implements it.
type Commands interface {
	Acknowledge(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
	Unacknowledge(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
	Resolve(ctx context.Context, c groups.Caller, publicID string, note *string) (groups.Result, error)
	Snooze(ctx context.Context, c groups.Caller, publicID string, end groups.SnoozeEnd) (groups.Result, error)
	Unsnooze(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
}

// Presses is the handler of button presses, the callback_query updates of the router (C-14.FR-4, FR-5).
type Presses struct {
	Bindings Bindings
	Links    Links
	Commands Commands
	// Roles are the Permissions of each Role, which the linked User presses with.
	Roles auth.Roles
	// Keys verify the signed button data: the Keyring.
	Keys buttons.Keys
	// Path answers the presses on the interactive path.
	Path Path
	// Business is the business clock, from which a Snooze lasts its duration.
	Business clock.Clock
	// PublicURL is MUSTER_PUBLIC_URL, the base of the profile link.
	PublicURL string
	Log       *logging.Logger
}

// The outcomes of a press, as telegram_press logs them, besides the outcomes of a Command that ran.
const (
	outcomeRefused     = "refused"
	outcomeForbidden   = "forbidden"
	outcomeNotLinked   = "not_linked"
	outcomeDisabled    = "disabled"
	outcomeNotVerified = "not_verified"
	outcomeFailed      = "failed"
	// outcomeTest is a press of a test message, answered that nothing changed (C-16.FR-3).
	outcomeTest = "test"
)

// errUnverifiable is a verified button that names no Command of its Route: a Snooze duration that no longer exists.
var errUnverifiable = errors.New("the button names no command of its route")

// pressed is what a press led to, as telegram_press logs it.
type pressed struct {
	group, command, outcome string
	err                     error
}

// Handle handles one callback_query of the Connection c (C-14.FR-4, FR-5). A press whose update arrived after the
// Connection received no updates for longer than PressMaxAge is dropped unanswered and logged telegram_press_dropped.
// Otherwise the button data — at most 64 bytes: the compact action id, the key id and the signature — is verified
// with any key of the Keyring and must name the Alert Group whose Root message is the channel post pressed, in the
// channel of a Telegram Destination of the Connection; anything else is answered "This button could not be verified;
// nothing was changed." The Telegram account from.id is mapped to its User through its Account link in the one
// identity space of Telegram; the Command runs as that User through the dispatcher with the Transport telegram, and
// the press is answered with answerCallbackQuery — "Done: {command}", the refusal, or for an account without a link
// the link to the profile — cut to MaxAnswerLength characters, in the language of the Route. The Root message is
// edited through delivery, never here, and only after the answer: the handler runs under the Connection's update
// lock, which the delivery worker tries before an edit, rescheduling the edit while it is held. The update is
// confirmed whatever the press led to, so that a press never runs twice; only a cancelled context leaves it
// unconfirmed. A press of a test message, whose verified data name the Destination under test, changes nothing and is
// answered "This is a test message; nothing was changed" (C-16.FR-3).
func (p *Presses) Handle(ctx context.Context, c Conn, u Update) error {
	q := u.CallbackQuery
	if q == nil || q.ID == "" {
		p.Log.Log(ctx, logging.TelegramPress, logging.F("connection", c.PublicID), logging.F("group", ""),
			logging.F("command", ""), logging.F("outcome", outcomeNotVerified))
		return nil
	}
	if u.Gap > PressMaxAge {
		p.Log.Log(ctx, logging.TelegramPressDropped, logging.F("connection", c.PublicID),
			logging.F("gap_seconds", int64(u.Gap/time.Second)))
		return nil
	}
	r := p.handle(ctx, c, q)
	fields := []logging.Field{logging.F("connection", c.PublicID), logging.F("group", r.group),
		logging.F("command", r.command), logging.F("outcome", r.outcome)}
	if r.err != nil {
		fields = append(fields, logging.F("error", r.err.Error()))
	}
	p.Log.Log(ctx, logging.TelegramPress, fields...)
	return ctx.Err()
}

// handle verifies, binds and runs one press and answers it.
func (p *Presses) handle(ctx context.Context, c Conn, q *CallbackQuery) pressed {
	a, err := buttons.Verify(p.Keys, q.Data, "")
	if len(q.Data) <= buttons.MaxLen && err == nil && a.Subject == buttons.SubjectTest {
		r := pressed{command: a.Command, outcome: outcomeTest}
		p.answer(ctx, c, q, &r, messages.T(messages.LanguageEnglish, "press.test", nil))
		return r
	}
	if len(q.Data) > buttons.MaxLen || err != nil || a.Subject != buttons.SubjectRoot || q.Message == nil {
		return p.notVerified(ctx, c, q, pressed{})
	}
	r := pressed{group: a.PublicID, command: a.Command}
	b, err := p.Bindings.TelegramPressBinding(ctx, c.ID, a.PublicID, q.Message.Chat.ID, q.Message.MessageID)
	if errors.Is(err, delivery.ErrNotBound) {
		return p.notVerified(ctx, c, q, r)
	}
	if err != nil {
		r.outcome, r.err = outcomeFailed, err
		p.answer(ctx, c, q, &r, messages.T(messages.LanguageEnglish, "press.failed", nil))
		return r
	}
	p.answer(ctx, c, q, &r, p.run(ctx, q, a, b, &r))
	return r
}

// run maps the account that pressed to its User, runs the Command as that User and returns the answer's text.
func (p *Presses) run(ctx context.Context, q *CallbackQuery, a buttons.Action, b delivery.Binding, r *pressed) string {
	u, err := p.Links.Lookup(ctx, accountlinks.SpaceTelegram, strconv.FormatInt(q.From.ID, 10))
	switch {
	case errors.Is(err, accountlinks.ErrNotLinked):
		r.outcome = outcomeNotLinked
		return messages.T(b.Language, "press.telegramNotLinked",
			messages.Args{"link": strings.TrimSuffix(p.PublicURL, "/") + "/profile"})
	case err != nil:
		r.outcome, r.err = outcomeFailed, err
		return messages.T(b.Language, "press.failed", nil)
	case u.Status == accountlinks.StatusDisabled:
		r.outcome = outcomeDisabled
		return messages.T(b.Language, "press.disabled", nil)
	}
	caller := groups.Caller{Actor: audit.User(u.ID, u.PublicID), Transport: audit.TransportTelegram,
		Permissions: p.Roles.Permissions(u.Role)}
	res, err := p.dispatch(ctx, caller, a, b)
	var forbidden *groups.ForbiddenError
	var refused *groups.RefusedError
	switch {
	case err == nil:
		r.outcome = string(res.Outcome)
		return messages.T(b.Language, "press.done",
			messages.Args{"command": messages.T(b.Language, "command."+a.Command, nil)})
	case errors.Is(err, errUnverifiable):
		r.outcome = outcomeNotVerified
		return messages.T(b.Language, "press.notVerified", nil)
	case errors.As(err, &forbidden):
		r.outcome = outcomeForbidden
		return messages.T(b.Language, "press.forbidden", nil)
	case errors.As(err, &refused):
		r.outcome = outcomeRefused
		return refused.Detail()
	}
	r.outcome, r.err = outcomeFailed, err
	return messages.T(b.Language, "press.failed", nil)
}

// dispatch runs the Command of the button a as c; a Snooze lasts the duration of the Route at the button's index.
func (p *Presses) dispatch(ctx context.Context, c groups.Caller, a buttons.Action, b delivery.Binding) (
	groups.Result, error) {
	switch a.Command {
	case buttons.CommandAcknowledge:
		return p.Commands.Acknowledge(ctx, c, a.PublicID)
	case buttons.CommandUnacknowledge:
		return p.Commands.Unacknowledge(ctx, c, a.PublicID)
	case buttons.CommandResolve:
		return p.Commands.Resolve(ctx, c, a.PublicID, nil)
	case buttons.CommandUnsnooze:
		return p.Commands.Unsnooze(ctx, c, a.PublicID)
	case buttons.CommandSnooze:
		if a.Argument >= len(b.SnoozeSeconds) {
			return groups.Result{}, errUnverifiable
		}
		until := p.Business.Now().Add(time.Duration(b.SnoozeSeconds[a.Argument]) * time.Second)
		return p.Commands.Snooze(ctx, c, a.PublicID, groups.SnoozeEnd{Until: &until})
	}
	return groups.Result{}, errUnverifiable
}

// notVerified answers a press whose data or binding does not hold: it changes nothing.
func (p *Presses) notVerified(ctx context.Context, c Conn, q *CallbackQuery, r pressed) pressed {
	r.outcome = outcomeNotVerified
	p.answer(ctx, c, q, &r, messages.T(messages.LanguageEnglish, "press.notVerified", nil))
	return r
}

// answer answers the press q with text, cut to MaxAnswerLength characters, through the interactive path, limited by
// the Connection's limiter only: an answer posts nothing to the channel, so it spends no token of the Destination's
// (C-14.FR-5, D295), and a RetryAfter it gets holds the Connection. An answer that failed, Telegram's refusal of a
// late one included, is r's error.
func (p *Presses) answer(ctx context.Context, c Conn, q *CallbackQuery, r *pressed, text string) {
	text = cut(text, MaxAnswerLength)
	id := c.ID
	out, err := p.Path.Do(ctx, delivery.Subject{Connection: &id}, delivery.AnswerOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			o := c.Client.answerCallbackQuery(ctx, call.Class, q.ID, text).Outcome
			if o.Kind == delivery.OutcomeRetryAfter {
				o.Scope = delivery.ScopeConnection
			}
			return o
		}))
	switch {
	case err != nil:
		r.err = errors.Join(r.err, errors.New("the press was not answered: "+err.Error()))
	case out.Kind != delivery.OutcomeOK:
		r.err = errors.Join(r.err, errors.New("the press was not answered: "+string(out.Error)))
	}
}

// cut is s cut to at most n characters.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
