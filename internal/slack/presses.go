// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package slack

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
)

// ParallelPresses is slack.parallel_presses: the most Slack presses one process handles at a time, over all its Slack
// Connections and both event modes; the others wait for a slot, already confirmed.
const ParallelPresses = 2

// PressDeadline bounds each press from the moment it is handed over to the end of its Command, its wait for a slot
// included, so that a Command stuck on the database frees its slot and the presses waiting behind it end: the 30 s that
// Mattermost waits for the answer to a press (F-032), which bounds a Mattermost press the same way. The answer to the
// person keeps its own Budget, so that a press that ran out of time is still told that it failed.
const PressDeadline = 30 * time.Second

// MaxWaitingPresses bounds the presses one process keeps waiting for a slot; one more is dropped, unanswered, and
// logged as failed. A slot frees in the time of one Command, so 64 waiting presses are seconds of work for the
// ParallelPresses slots and more than the people of a team press at once, while the payloads they hold stay under
// 64 MiB, MaxRequestBytes each, however often a captured signed request is replayed within slack.request_tolerance.
const MaxWaitingPresses = 64

// typeBlockActions is the type of the interaction of a press (F-093).
const typeBlockActions = "block_actions"

// Bindings are delivery's reads of what a press is bound to; *delivery.Service implements them.
type Bindings interface {
	SlackPressBinding(ctx context.Context, connectionID int64, channelID, ts string) (delivery.SlackBinding, error)
	SlackTestDestination(ctx context.Context, connectionID int64, publicID, channelID string) (delivery.Destination,
		bool, error)
}

// Links map a Slack account to its User; *accountlinks.Service implements them.
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

// The outcomes of a press, as slack_press logs them, besides the outcomes of a Command that ran.
const (
	outcomeRefused     = "refused"
	outcomeForbidden   = "forbidden"
	outcomeNotLinked   = "not_linked"
	outcomeDisabled    = "disabled"
	outcomeNotVerified = "not_verified"
	outcomeExpired     = "expired"
	outcomeTest        = "test"
	outcomeIgnored     = "ignored"
	outcomeFailed      = "failed"
)

// errUnverifiable is a verified button that names no Command of its message: a Snooze duration the Route no longer
// has, a Snooze preset of a Route that no longer has Snooze durations, or a Command its kind of message does not carry.
var errUnverifiable = errors.New("the button names no command of its message")

// The errors of a press that cannot be handled: a payload that is not the JSON of a press, one that names no user,
// message or action, and a handling that panicked, whose value may carry anything and is not logged.
var (
	errNotJSON    = errors.New("the payload is not the JSON of a press")
	errIncomplete = errors.New("the press names no user, message or action")
	errPanicked   = errors.New("the press failed unexpectedly")
)

// The errors of a press that ran out of room or time: one more than MaxWaitingPresses waiting for a slot, and one not
// handled within PressDeadline.
var (
	errBusy     = errors.New("too many presses waiting")
	errDeadline = errors.New("the press was not handled within its deadline")
)

// snoozePresets are the Snooze presets by the argument of their buttons (C-10.FR-18).
var snoozePresets = map[int]groups.SnoozePreset{
	buttons.ArgumentTomorrow:  groups.PresetTomorrow,
	buttons.ArgumentWeekStart: groups.PresetWeekStart,
}

// Presses is the one handler of Slack presses for both event modes (C-24.FR-10): the callback of http mode hands it
// every press it confirmed through Go, and Socket Mode every interactive envelope. It handles at most Parallel presses
// at a time; the others wait for a slot, already confirmed, MaxWaitingPresses at most, and each press has
// PressDeadline for its wait and its Command. Each press is verified, bound to its message, run as the linked User
// through the dispatcher and, when there is something to say, answered privately (answers.go); it is logged as
// slack_press. Wait lets the shutdown drain the presses handed to Go.
type Presses struct {
	// Bindings find the message a press names among the Connection's deliveries.
	Bindings Bindings
	// Links map the account that pressed to its User.
	Links Links
	// Commands run the Commands of presses through the dispatcher.
	Commands Commands
	// Roles are the Permissions of each Role, which a linked User presses with.
	Roles auth.Roles
	// Keys verify the signed action ids: the Keyring.
	Keys buttons.Keys
	// Path is the interactive path, which the answers take.
	Path Path
	// Budget is delivery.interactive_budget, within which each way of an answer is made or fails; zero is
	// delivery.InteractiveBudget.
	Budget time.Duration
	// Business is the business clock, from which a Snooze lasts its duration.
	Business clock.Clock
	// PublicURL is MUSTER_PUBLIC_URL, the base of the profile link of an account without an Account link.
	PublicURL string
	// Parallel is slack.parallel_presses; zero is ParallelPresses.
	Parallel int
	Log      *logging.Logger

	slotsOnce sync.Once
	slots     chan struct{}
	// parked, when set, is called when a press finds no free slot and starts to wait for one; tests set it.
	parked func()
	// maxWaiting and deadline replace MaxWaitingPresses and PressDeadline when set; tests set them.
	maxWaiting int
	deadline   time.Duration

	mu sync.Mutex
	// waiting counts the presses waiting for a slot, and running those handed to Go that have not ended.
	waiting, running int
	// idle is closed, for Wait, when running drops to zero.
	idle chan struct{}
}

// interaction is the part of an interaction payload that Muster reads (F-093).
type interaction struct {
	Type string `json:"type"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Container struct {
		MessageTS string `json:"message_ts"`
		ChannelID string `json:"channel_id"`
	} `json:"container"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	ResponseURL string         `json:"response_url"`
	Actions     []pressedValue `json:"actions"`
}

// pressedValue is an action of a press: a button's value, or the option of the Snooze menu that was chosen.
type pressedValue struct {
	Value          string `json:"value"`
	SelectedOption *struct {
		Value string `json:"value"`
	} `json:"selected_option"`
}

// value is the signed action id the action carries.
func (v pressedValue) value() string {
	if v.SelectedOption != nil {
		return v.SelectedOption.Value
	}
	return v.Value
}

// pressed is what a press led to, as slack_press logs it, and the answer to show the person who pressed, empty for
// none, with where it goes.
type pressed struct {
	group, command, outcome string
	answer                  private
	err                     error
}

// slot waits until fewer than Parallel presses are being handled, or ctx ends, and returns the release of the slot it
// took. When MaxWaitingPresses presses already wait, it does not wait and returns errBusy.
func (p *Presses) slot(ctx context.Context) (func(), error) {
	p.slotsOnce.Do(func() {
		n := p.Parallel
		if n <= 0 {
			n = ParallelPresses
		}
		p.slots = make(chan struct{}, n)
	})
	release := func() { <-p.slots }
	select {
	case p.slots <- struct{}{}:
		return release, nil
	default:
	}
	if !p.park() {
		return nil, errBusy
	}
	defer p.unpark()
	if p.parked != nil {
		p.parked()
	}
	select {
	case p.slots <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// park counts a press that starts to wait for a slot, unless MaxWaitingPresses already wait.
func (p *Presses) park() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiting >= cmp.Or(p.maxWaiting, MaxWaitingPresses) {
		return false
	}
	p.waiting++
	return true
}

func (p *Presses) unpark() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiting--
}

// Go handles the interaction payload of the Connection c, which Slack already had confirmed, in the background, in
// ctx: a context of the process, never a request's, whose end ends the press as failed. Wait waits for it.
func (p *Presses) Go(ctx context.Context, c Conn, payload []byte) {
	p.mu.Lock()
	p.running++
	p.mu.Unlock()
	go func() {
		defer p.ended()
		p.Handle(ctx, c, payload)
	}()
}

func (p *Presses) ended() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running--
	if p.running == 0 && p.idle != nil {
		close(p.idle)
		p.idle = nil
	}
}

// Wait waits until every press handed to Go has ended, or until ctx ends, and then returns the cause of its end: the
// shutdown drains the presses that were answered 200 within its grace period (C-02.FR-16).
func (p *Presses) Wait(ctx context.Context) error {
	p.mu.Lock()
	if p.running == 0 {
		p.mu.Unlock()
		return nil
	}
	if p.idle == nil {
		p.idle = make(chan struct{})
	}
	idle := p.idle
	p.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// Handle handles the interaction payload of the Connection c, which Slack already had confirmed (C-24.FR-10): another
// kind of interaction than block_actions is ignored at once; a press waits for a slot, is verified, bound and run
// within PressDeadline, and once its slot is free again the person who pressed is answered, when there is something to
// say (C-24.FR-11), unless ctx has ended, so that nothing can reach Slack any more. A press that finds
// MaxWaitingPresses presses waiting is dropped and not answered. The answer is made once: a failure of both its ways is
// logged, never retried. A panic is a failed press, logged without its value.
func (p *Presses) Handle(ctx context.Context, c Conn, payload []byte) {
	defer func() {
		if recover() != nil {
			p.log(ctx, c, pressed{outcome: outcomeFailed, err: errPanicked}, "")
		}
	}()
	var in interaction
	err := json.Unmarshal(payload, &in)
	if err == nil && in.Type != typeBlockActions {
		p.log(ctx, c, pressed{outcome: outcomeIgnored}, "")
		return
	}
	r := p.slotted(ctx, c, in, err)
	how := ""
	if r.answer.text != "" && ctx.Err() == nil {
		var answerErr error
		how, answerErr = p.answer(ctx, c, r.answer)
		r.err = errors.Join(r.err, answerErr)
	}
	p.log(ctx, c, r, how)
}

// slotted handles the press in of the Connection c, whose payload decoded with err, holding a slot, within
// PressDeadline from now.
func (p *Presses) slotted(ctx context.Context, c Conn, in interaction, err error) pressed {
	ctx, cancel := context.WithTimeoutCause(ctx, cmp.Or(p.deadline, PressDeadline), errDeadline)
	defer cancel()
	release, slotErr := p.slot(ctx)
	if slotErr != nil {
		return pressed{outcome: outcomeFailed, err: slotErr}
	}
	defer release()
	if err != nil {
		return pressed{outcome: outcomeNotVerified, err: errNotJSON}
	}
	return p.press(ctx, c, in)
}

// press verifies, binds and runs the press in of the Connection c (C-24.FR-10). The account is mapped to its User
// through its Account link in the Connection's identity space first, so that every answer is in the profile language
// of the linked User, in English without a link (C-24.FR-11). The value of the first action — a button's value, or the
// chosen option of the Snooze menu — is the signed action id, verified with the key of the Keyring its prefix names.
// Verified data of a test message changes nothing and is answered so when it names a Destination of the Connection on
// the channel of the press (C-24.FR-13). Otherwise the pressed message — by the press's channel and ts — must be a Root
// message or a sent Reminder of a Slack Destination of the Connection, and the action id must have been issued for it:
// its kind and its Alert Group; else it is answered "not verified", like a bad signature, or, for a Reminder whose key
// left the Keyring, that the button expired (C-17.FR-6). The Command runs as the linked User with the Transport slack
// through the dispatcher; one that ran is not answered, the edited Root message shows it, except "Still on it", which
// changes no message and is answered "Done: Still on it" as C-17 says. A refusal, a failure, an account without a link
// and a disabled User are answered.
func (p *Presses) press(ctx context.Context, c Conn, in interaction) pressed {
	to := private{channel: cmp.Or(in.Container.ChannelID, in.Channel.ID), user: in.User.ID,
		responseURL: in.ResponseURL}
	answered := func(r pressed, text string) pressed {
		if to.channel != "" && to.user != "" {
			r.answer = to
			r.answer.text = text
		}
		return r
	}
	if in.User.ID == "" || in.Container.ChannelID == "" || in.Container.MessageTS == "" || len(in.Actions) == 0 {
		return answered(pressed{outcome: outcomeNotVerified, err: errIncomplete},
			messages.T(messages.LanguageEnglish, "press.notVerified", nil))
	}
	u, linked, err := p.lookup(ctx, c, in.User.ID)
	if err != nil {
		return answered(pressed{outcome: outcomeFailed, err: err},
			messages.T(messages.LanguageEnglish, "press.failed", nil))
	}
	lang := language(u, linked)
	a, v := p.verify(in.Actions[0].value())
	if v == verified && a.Subject == buttons.SubjectTest {
		d, ok, err := p.Bindings.SlackTestDestination(ctx, c.ID, a.PublicID, in.Container.ChannelID)
		switch {
		case err != nil:
			return answered(pressed{outcome: outcomeFailed, err: err}, messages.T(lang, "press.failed", nil))
		case !ok:
			return answered(pressed{outcome: outcomeNotVerified}, messages.T(lang, "press.notVerified", nil))
		}
		to.links = outbound.Links{Destination: d.ID}
		return answered(pressed{command: a.Command, outcome: outcomeTest}, messages.T(lang, "press.test", nil))
	}
	b, err := p.Bindings.SlackPressBinding(ctx, c.ID, in.Container.ChannelID, in.Container.MessageTS)
	switch {
	case errors.Is(err, delivery.ErrNotBound):
		return answered(pressed{outcome: outcomeNotVerified}, messages.T(lang, "press.notVerified", nil))
	case err != nil:
		return answered(pressed{outcome: outcomeFailed, err: err}, messages.T(lang, "press.failed", nil))
	}
	to.links = outbound.Links{Destination: b.Destination.ID, AlertGroup: b.AlertGroup}
	if b.Kind == buttons.SubjectReply {
		to.threadTS = b.RootTS
	}
	switch {
	case v == expired && a.Subject == buttons.SubjectReply && b.Kind == buttons.SubjectReply &&
		a.PublicID == b.GroupPublicID:
		return answered(pressed{outcome: outcomeExpired}, messages.T(lang, "press.expired", nil))
	case v != verified || a.Subject != b.Kind || a.PublicID != b.GroupPublicID:
		return answered(pressed{outcome: outcomeNotVerified}, messages.T(lang, "press.notVerified", nil))
	}
	r := pressed{group: a.PublicID, command: a.Command}
	switch {
	case !linked:
		r.outcome = outcomeNotLinked
		return answered(r, p.notLinked())
	case u.Status == accountlinks.StatusDisabled:
		r.outcome = outcomeDisabled
		return answered(r, messages.T(lang, "press.disabled", nil))
	}
	res, err := p.run(ctx, p.caller(u), a, b)
	switch {
	case err == nil && a.Command == buttons.CommandStillOnIt:
		// Nothing else shows that "Still on it" was taken: no message changes.
		r.outcome = string(res.Outcome)
		return answered(r, messages.Done(lang, a.Command))
	case err == nil:
		r.outcome = string(res.Outcome)
		return r
	}
	var text string
	r.outcome, text, r.err = refusal(lang, err)
	return answered(r, text)
}

// verdict is how the action id of a press verified.
type verdict int

// The verdicts on the action id of a press: verified; expired, a well-formed action id whose key left the Keyring,
// whose unverified Action may only choose an answer; invalid, anything else.
const (
	verified verdict = iota
	expired
	invalid
)

// verify reads and verifies the action id of a press: longer than buttons.MaxLen it is not an action id at all.
func (p *Presses) verify(id string) (buttons.Action, verdict) {
	if len(id) > buttons.MaxLen {
		return buttons.Action{}, invalid
	}
	a, err := buttons.Verify(p.Keys, id, "")
	switch {
	case err == nil:
		return a, verified
	case errors.Is(err, buttons.ErrExpired):
		return a, expired
	}
	return buttons.Action{}, invalid
}

// lookup is the User the Slack account user is linked to in the identity space of the Connection c, and whether there
// is one; an error other than ErrNotLinked is the read's.
func (p *Presses) lookup(ctx context.Context, c Conn, user string) (accountlinks.User, bool, error) {
	u, err := p.Links.Lookup(ctx, accountlinks.SpaceSlack(c.ID), user)
	switch {
	case errors.Is(err, accountlinks.ErrNotLinked):
		return accountlinks.User{}, false, nil
	case err != nil:
		return accountlinks.User{}, false, err
	}
	return u, true, nil
}

// notLinked is the answer to an account without an Account link for the Connection, in English: the link to the
// profile, where the person links it.
func (p *Presses) notLinked() string {
	return messages.T(messages.LanguageEnglish, "press.slackNotLinked",
		messages.Args{"link": strings.TrimSuffix(p.PublicURL, "/") + "/profile"})
}

// language is the language of the answers to a person: the profile language of the User the account is linked to —
// messages.T takes an empty or unknown one for English — and English for an account without a link.
func language(u accountlinks.User, linked bool) string {
	if !linked {
		return messages.LanguageEnglish
	}
	return u.Language
}

// caller is the linked User u as the dispatcher takes it: with the Permissions of its Role and the Transport slack.
func (p *Presses) caller(u accountlinks.User) groups.Caller {
	return groups.Caller{Actor: audit.User(u.ID, u.PublicID), Transport: audit.TransportSlack,
		Permissions: p.Roles.Permissions(u.Role)}
}

// run runs the Command of the button a on its message b as c through the dispatcher. A Reminder carries only "Still
// on it" and Unacknowledge, and only a Reminder carries "Still on it". A Snooze lasts the Route's duration at the
// button's index from now on the business clock, or ends at the Snooze preset its argument names, with no time zone,
// so that the dispatcher takes the User's, while the Route has Snooze durations (C-10.FR-18); an index the Route no
// longer has, or a preset of a Route without durations, is errUnverifiable.
func (p *Presses) run(ctx context.Context, c groups.Caller, a buttons.Action, b delivery.SlackBinding) (
	groups.Result, error) {
	carried := a.Command != buttons.CommandStillOnIt
	if b.Kind == buttons.SubjectReply {
		carried = a.Command == buttons.CommandStillOnIt || a.Command == buttons.CommandUnacknowledge
	}
	if !carried {
		return groups.Result{}, errUnverifiable
	}
	switch a.Command {
	case buttons.CommandStillOnIt:
		return p.Commands.AnswerReminder(ctx, c, a.PublicID)
	case buttons.CommandAcknowledge:
		return p.Commands.Acknowledge(ctx, c, a.PublicID)
	case buttons.CommandUnacknowledge:
		return p.Commands.Unacknowledge(ctx, c, a.PublicID)
	case buttons.CommandResolve:
		return p.Commands.Resolve(ctx, c, a.PublicID, nil)
	case buttons.CommandUnsnooze:
		return p.Commands.Unsnooze(ctx, c, a.PublicID)
	}
	if preset, ok := snoozePresets[a.Argument]; ok && len(b.SnoozeSeconds) > 0 {
		return p.Commands.Snooze(ctx, c, a.PublicID, groups.SnoozeEnd{Preset: preset})
	}
	if a.Argument >= len(b.SnoozeSeconds) {
		return groups.Result{}, errUnverifiable
	}
	until := p.Business.Now().Add(time.Duration(b.SnoozeSeconds[a.Argument]) * time.Second)
	return p.Commands.Snooze(ctx, c, a.PublicID, groups.SnoozeEnd{Until: &until})
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

// log logs slack_press for the press r of the Connection c, answered how — ephemeral or response_url, empty for not
// at all.
func (p *Presses) log(ctx context.Context, c Conn, r pressed, how string) {
	fields := []logging.Field{logging.F("connection", c.PublicID), logging.F("group", r.group),
		logging.F("command", r.command), logging.F("outcome", r.outcome)}
	if how != "" {
		fields = append(fields, logging.F("answer", how))
	}
	if r.err != nil {
		fields = append(fields, logging.F("error", r.err.Error()))
	}
	p.Log.Log(ctx, logging.SlackPress, fields...)
}
