// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/accountlinks"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/buttons"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/ingest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/requestlog"
)

// CallbackPattern is the pattern of the callback of button presses on the callback mux of the ingest listener.
const CallbackPattern = CallbackPath + "{connection_id}"

// ErrNoConnection is a callback for a Connection that is unknown, deleted or not a Mattermost one.
var ErrNoConnection = errors.New("no such mattermost connection")

// Connection is a Mattermost Connection as the callback of its button presses needs it: BotUserID is the user id of
// its bot, which a Connection check learns, empty before one did.
type Connection struct {
	ID        int64
	PublicID  string
	BotUserID string
	Client    *Client
}

// Connections find the Connection of a callback; *connections.Service implements it.
type Connections interface {
	Connection(ctx context.Context, publicID string) (Connection, error)
}

// Bindings are delivery's reads of what a press is bound to; *delivery.Service implements it.
type Bindings interface {
	PressBinding(ctx context.Context, connectionID int64, groupPublicID, postID string) (delivery.Binding, error)
	ReplyBinding(ctx context.Context, connectionID int64, groupPublicID, postID string) (delivery.Binding, error)
	PostDestination(ctx context.Context, connectionID int64, postID, channelID string) (delivery.Destination, bool,
		error)
	TestDestination(ctx context.Context, connectionID int64, publicID, channelID string) (delivery.Destination, bool,
		error)
}

// Links map a Mattermost account to its User; *accountlinks.Service implements it.
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
	AnswerReminder(ctx context.Context, c groups.Caller, publicID string) (groups.Result, error)
}

// CallbackConfig is what the callback of button presses needs.
type CallbackConfig struct {
	Connections Connections
	Bindings    Bindings
	Links       Links
	Commands    Commands
	// Roles are the Permissions of each Role, which the linked User presses with.
	Roles auth.Roles
	// Keys verify the signed action ids: the Keyring.
	Keys buttons.Keys
	// Path is the interactive path the ephemeral posts of the answers go through.
	Path Path
	// Budget bounds the ephemeral post of an answer, its limiter tokens included: delivery.interactive_budget; zero
	// takes delivery.InteractiveBudget.
	Budget time.Duration
	// Business is the business clock, from which a Snooze lasts its duration.
	Business clock.Clock
	// PublicURL is MUSTER_PUBLIC_URL, the base of the profile link.
	PublicURL string
	// BodyLimit is ingest.body_limit; zero takes ingest.BodyLimit.
	BodyLimit int64
	// TestPresses tell the Destination test waiting on any replica that the bot's own press of its test message
	// arrived (C-16.FR-3); nil tells nobody.
	TestPresses TestPresses
	Log         *logging.Logger
}

// How the person who pressed was answered, as mattermost_press logs it (D284): an ephemeral post in the channel, or
// the ephemeral_text of the callback's answer.
const (
	answerPost = "ephemeral_post"
	answerText = "ephemeral_text"
)

// errorPermissions is the error id of a call the bot lacks the permission for, such as an ephemeral post by a bot
// without the system admin role (F-063).
const errorPermissions = "api.context.permissions.app_error"

// The outcomes of a press, as mattermost_press logs them.
const (
	outcomeRefused           = "refused"
	outcomeForbidden         = "forbidden"
	outcomeNotLinked         = "not_linked"
	outcomeDisabled          = "disabled"
	outcomeNotVerified       = "not_verified"
	outcomeExpired           = "expired"
	outcomeInvalidRequest    = "invalid_request"
	outcomeUnknownConnection = "unknown_connection"
	outcomeFailed            = "failed"
	// outcomeTest is a press of a test message by a person, answered that nothing changed; outcomeTestPress the bot's
	// own press of it during a Destination test, answered with nothing (C-16.FR-3).
	outcomeTest      = "test"
	outcomeTestPress = "test_press"
)

// errUnverifiable is a verified action id that names no Command of its Route: a Snooze duration that no longer exists.
var errUnverifiable = errors.New("the button names no command of its route")

type pressHandler struct {
	cfg CallbackConfig
}

// NewCallback is the callback of the button presses of Mattermost Connections, mattermostAction (C-13.FR-4): it
// verifies the signed action id, binds the press to the Root message or the Reminder it was issued for, maps the
// Mattermost user to a User through its Account link, and runs the Command as that User with the Transport mattermost.
// Every request is answered 200 with a JSON object, never with update. Whatever the person who pressed must read goes
// first as an ephemeral post through the interactive path, which shows in the channel view (F-026) — or, for a press
// on a Reminder, in the Thread of the Root message it replies to (F-025) — and the answer is empty; when that post is
// not made — refused with 403 to a bot without the create_post_ephemeral permission (F-063), or failed any other way —
// the text is the answer's ephemeral_text, which needs no permission and shows in the Thread (F-025, F-062), so that it
// is never lost (D284). A press on a Reminder whose key left the Keyring is answered that the button has expired
// (C-17.FR-6). Every request for a known Connection, refused ones included, is in its Request log through the
// annotation of requestlog.Inbound, with the Destination and the Alert Group once the press is matched (C-23.FR-1).
func NewCallback(cfg CallbackConfig) http.Handler {
	if cfg.BodyLimit <= 0 {
		cfg.BodyLimit = ingest.BodyLimit
	}
	if cfg.Budget <= 0 {
		cfg.Budget = delivery.InteractiveBudget
	}
	cfg.PublicURL = strings.TrimSuffix(cfg.PublicURL, "/")
	return &pressHandler{cfg: cfg}
}

// pressRequest is the part of a MattermostActionRequest that Muster reads.
type pressRequest struct {
	UserID    string        `json:"user_id"`
	ChannelID string        `json:"channel_id"`
	PostID    string        `json:"post_id"`
	Context   actionContext `json:"context"`
}

// press is what a request led to, as mattermost_press logs it: answer is how the person was answered, and text the
// ephemeral_text of the callback's answer when the ephemeral post was not made.
type press struct {
	connection, group, command, outcome, err string
	answer, text                             string
}

// pressAnswer is the answer to a press, MattermostActionAnswer: never update, which only the delivery worker makes
// (ADR-0005); ephemeral_text is taken as it is, without Mattermost's Slack link parsing, as a post's message is.
type pressAnswer struct {
	EphemeralText    string `json:"ephemeral_text,omitempty"`
	SkipSlackParsing bool   `json:"skip_slack_parsing,omitempty"`
}

func (h *pressHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.recovered(ctx, w, r)
	fields := []logging.Field{logging.F("connection", p.connection), logging.F("group", p.group),
		logging.F("command", p.command), logging.F("outcome", p.outcome)}
	if p.answer != "" {
		fields = append(fields, logging.F("answer", p.answer))
	}
	if p.err != "" {
		fields = append(fields, logging.F("error", p.err))
	}
	h.cfg.Log.Log(ctx, logging.MattermostPress, fields...)
	a := pressAnswer{}
	if p.text != "" {
		a = pressAnswer{EphemeralText: p.text, SkipSlackParsing: true}
	}
	body, err := json.Marshal(a)
	if err != nil {
		body = []byte("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// recovered handles one press; a panic is a failed press, logged without its value, which may carry anything.
func (h *pressHandler) recovered(ctx context.Context, w http.ResponseWriter, r *http.Request) (p press) {
	defer func() {
		if recover() != nil {
			p = press{outcome: outcomeFailed, err: "the press failed unexpectedly"}
		}
	}()
	return h.handle(ctx, w, r)
}

// handle runs one press and reports what it led to.
func (h *pressHandler) handle(ctx context.Context, w http.ResponseWriter, r *http.Request) press {
	// The Connection is read first, so that every request for a known one is in its Request log, refused ones included
	// (C-23.FR-1); the answers keep their order.
	conn, connErr := h.cfg.Connections.Connection(ctx, r.PathValue("connection_id"))
	if connErr == nil {
		requestlog.Annotate(ctx, requestlog.Links{Connection: conn.ID, ConnectionID: conn.PublicID})
	}
	if r.Method != http.MethodPost {
		return press{outcome: outcomeInvalidRequest, err: "the request is not a POST"}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.BodyLimit))
	if err != nil {
		return press{outcome: outcomeInvalidRequest, err: "the body could not be read"}
	}
	var req pressRequest
	if err := json.Unmarshal(body, &req); err != nil || req.UserID == "" || req.ChannelID == "" ||
		req.PostID == "" || req.Context.Action == "" || req.Context.KeyID == "" {
		return press{outcome: outcomeInvalidRequest, err: "the body is not a MattermostActionRequest"}
	}
	if errors.Is(connErr, ErrNoConnection) {
		return press{outcome: outcomeUnknownConnection}
	}
	if connErr != nil {
		return press{outcome: outcomeFailed, err: connErr.Error()}
	}
	p := press{connection: conn.PublicID}
	a, err := buttons.Verify(h.cfg.Keys, req.Context.Action, req.Context.KeyID)
	if err == nil && a.Subject == buttons.SubjectTest {
		return h.testPress(ctx, p, conn, req, a)
	}
	if errors.Is(err, buttons.ErrExpired) && a.Subject == buttons.SubjectReply {
		return h.expired(ctx, p, conn, req, a)
	}
	if err != nil || (a.Subject != buttons.SubjectRoot && a.Subject != buttons.SubjectReply) {
		return h.notVerified(ctx, p, conn, req)
	}
	p.group, p.command = a.PublicID, a.Command
	bind := h.cfg.Bindings.PressBinding
	if a.Subject == buttons.SubjectReply {
		bind = h.cfg.Bindings.ReplyBinding
	}
	b, err := bind(ctx, conn.ID, a.PublicID, req.PostID)
	if errors.Is(err, delivery.ErrNotBound) || (err == nil && b.ChannelID != req.ChannelID) {
		return h.notVerified(ctx, p, conn, req)
	}
	if err != nil {
		p.outcome, p.err = outcomeFailed, err.Error()
		return p
	}
	requestlog.Annotate(ctx, requestlog.Links{Destination: b.Destination.ID, AlertGroup: b.AlertGroup})
	answer := func(outcome, text string, cause error) press {
		p.outcome = outcome
		return h.answer(ctx, p, conn, b.Destination, req, b.RootMessageID, text, cause)
	}
	u, err := h.cfg.Links.Lookup(ctx, accountlinks.SpaceMattermost(conn.ID), req.UserID)
	switch {
	case errors.Is(err, accountlinks.ErrNotLinked):
		return answer(outcomeNotLinked, messages.T(b.Language, "press.notLinked",
			messages.Args{"link": h.cfg.PublicURL + "/profile"}), nil)
	case err != nil:
		return answer(outcomeFailed, messages.T(b.Language, "press.failed", nil), err)
	case u.Status == accountlinks.StatusDisabled:
		return answer(outcomeDisabled, messages.T(b.Language, "press.disabled", nil), nil)
	}
	c := groups.Caller{Actor: audit.User(u.ID, u.PublicID), Transport: audit.TransportMattermost,
		Permissions: h.cfg.Roles.Permissions(u.Role)}
	res, err := h.dispatch(ctx, c, a, b)
	var forbidden *groups.ForbiddenError
	var refused *groups.RefusedError
	switch {
	case err == nil && a.Command == buttons.CommandStillOnIt:
		// Nothing else shows that "Still on it" was taken: the Root message does not change.
		return answer(string(res.Outcome), messages.Done(b.Language, a.Command), nil)
	case err == nil:
		p.outcome = string(res.Outcome)
		return p
	case errors.Is(err, errUnverifiable):
		return answer(outcomeNotVerified, messages.T(b.Language, "press.notVerified", nil), nil)
	case errors.As(err, &forbidden):
		return answer(outcomeForbidden, messages.T(b.Language, "press.forbidden", nil), nil)
	case errors.As(err, &refused):
		return answer(outcomeRefused, refused.Detail(), nil)
	}
	return answer(outcomeFailed, messages.T(b.Language, "press.failed", nil), err)
}

// dispatch runs the Command of the button a as c; a Snooze lasts the duration of the Route at the button's index. A
// Reminder carries only "Still on it" and Unacknowledge, and only a Reminder carries "Still on it".
func (h *pressHandler) dispatch(ctx context.Context, c groups.Caller, a buttons.Action, b delivery.Binding) (
	groups.Result, error) {
	if !onSubject(a) {
		return groups.Result{}, errUnverifiable
	}
	switch a.Command {
	case buttons.CommandStillOnIt:
		return h.cfg.Commands.AnswerReminder(ctx, c, a.PublicID)
	case buttons.CommandAcknowledge:
		return h.cfg.Commands.Acknowledge(ctx, c, a.PublicID)
	case buttons.CommandUnacknowledge:
		return h.cfg.Commands.Unacknowledge(ctx, c, a.PublicID)
	case buttons.CommandResolve:
		return h.cfg.Commands.Resolve(ctx, c, a.PublicID, nil)
	case buttons.CommandUnsnooze:
		return h.cfg.Commands.Unsnooze(ctx, c, a.PublicID)
	case buttons.CommandSnooze:
		if a.Argument >= len(b.SnoozeSeconds) {
			return groups.Result{}, errUnverifiable
		}
		until := h.cfg.Business.Now().Add(time.Duration(b.SnoozeSeconds[a.Argument]) * time.Second)
		return h.cfg.Commands.Snooze(ctx, c, a.PublicID, groups.SnoozeEnd{Until: &until})
	}
	return groups.Result{}, errUnverifiable
}

// onSubject is whether the Command of a is one its subject carries: "Still on it" and Unacknowledge on a Reminder,
// any other on a Root message.
func onSubject(a buttons.Action) bool {
	if a.Subject == buttons.SubjectReply {
		return a.Command == buttons.CommandStillOnIt || a.Command == buttons.CommandUnacknowledge
	}
	return a.Command != buttons.CommandStillOnIt
}

// expired answers a press on a Reminder whose action id names a key that is no longer in the Keyring (C-17.FR-6): it
// changes nothing and, when the post is a sent Reminder of the Alert Group the action id names in a Destination of the
// Connection in the channel of the press, is answered in the Thread of the Root message that the button has expired;
// otherwise it is not verified. The action id is not verified, so neither its Alert Group nor its Command is logged.
func (h *pressHandler) expired(ctx context.Context, p press, conn Connection, req pressRequest,
	a buttons.Action) press {
	b, err := h.cfg.Bindings.ReplyBinding(ctx, conn.ID, a.PublicID, req.PostID)
	switch {
	case errors.Is(err, delivery.ErrNotBound) || (err == nil && b.ChannelID != req.ChannelID):
		return h.notVerified(ctx, p, conn, req)
	case err != nil:
		p.outcome, p.err = outcomeFailed, err.Error()
		return p
	}
	requestlog.Annotate(ctx, requestlog.Links{Destination: b.Destination.ID, AlertGroup: b.AlertGroup})
	p.outcome = outcomeExpired
	return h.answer(ctx, p, conn, b.Destination, req, b.RootMessageID, messages.T(b.Language, "press.expired", nil),
		nil)
}

// testPress handles a press of a test message, whose verified action id names the Destination under test and changes
// nothing (C-16.FR-3). The bot's own press during the test is answered with nothing and, with the nonce of its test,
// tells the waiting test that presses reach Muster. A person who pressed is answered privately that it is a test
// message, limited by that Destination, when it is a Destination of the Connection in the channel of the press;
// otherwise the press is not verified and gets the empty answer.
func (h *pressHandler) testPress(ctx context.Context, p press, conn Connection, req pressRequest,
	a buttons.Action) press {
	p.command = a.Command
	if conn.BotUserID != "" && req.UserID == conn.BotUserID {
		p.outcome = outcomeTestPress
		if req.Context.Test != "" && h.cfg.TestPresses != nil {
			if err := h.cfg.TestPresses.Pressed(ctx, req.Context.Test); err != nil {
				p.err = err.Error()
			}
		}
		return p
	}
	d, ok, err := h.cfg.Bindings.TestDestination(ctx, conn.ID, a.PublicID, req.ChannelID)
	switch {
	case err != nil:
		p.outcome, p.err = outcomeFailed, err.Error()
		return p
	case !ok:
		p.outcome = outcomeNotVerified
		return p
	}
	requestlog.Annotate(ctx, requestlog.Links{Destination: d.ID})
	p.outcome = outcomeTest
	return h.answer(ctx, p, conn, d, req, "", messages.T(messages.LanguageEnglish, "press.test", nil), nil)
}

// notVerified refuses a press whose action id or binding does not hold. It is answered only on a post that Muster
// delivered to a Destination of the Connection in the channel of the press, so that a forged callback can neither
// make the bot post anywhere else nor spend a Destination's limiter tokens with made-up posts, and its empty answer
// tells it nothing about which Connections and posts exist.
func (h *pressHandler) notVerified(ctx context.Context, p press, conn Connection, req pressRequest) press {
	p.outcome = outcomeNotVerified
	d, ok, err := h.cfg.Bindings.PostDestination(ctx, conn.ID, req.PostID, req.ChannelID)
	if err != nil {
		p.err = err.Error()
		return p
	}
	if !ok {
		return p
	}
	requestlog.Annotate(ctx, requestlog.Links{Destination: d.ID})
	return h.answer(ctx, p, conn, d, req, "", messages.T(messages.LanguageEnglish, "press.notVerified", nil), nil)
}

// answer shows text to the person who pressed (D284): first as one ephemeral post in the channel of the press through
// the interactive path, limited by the Destination d and bounded by the budget, without root_id so that it shows in
// the channel view (F-026) — or, for a press on a Reminder, with the Root message rootID as root_id, so that it shows
// in the Thread the Reminder is in (F-025). When the post is not made, text becomes the ephemeral_text of the
// callback's answer (F-062), which the server shows in the Thread of the pressed post: after a 403 for the missing
// permission, which a bot with the role Member always gets (F-063) and which is therefore not an error, and after any
// other failure, which is logged with cause.
func (h *pressHandler) answer(ctx context.Context, p press, conn Connection, d delivery.Destination, req pressRequest,
	rootID, text string, cause error) press {
	ctx, cancel := context.WithTimeout(ctx, h.cfg.Budget)
	defer cancel()
	var r Result
	out, err := h.cfg.Path.Do(ctx, delivery.Subject{Destination: &d}, delivery.AnswerOp(
		func(ctx context.Context, c delivery.Call) delivery.Outcome {
			r = conn.Client.ephemeralPost(ctx, c.Class, req.UserID, req.ChannelID, rootID, text)
			return r.Outcome
		}))
	switch {
	case err != nil:
		cause = errors.Join(cause, fmt.Errorf("the ephemeral post was not sent: %w", err))
	case out.Kind == delivery.OutcomeOK:
		p.answer = answerPost
	case r.Status != http.StatusForbidden || r.ErrorID != errorPermissions:
		cause = errors.Join(cause, fmt.Errorf("the ephemeral post was not sent: %s", out.Error))
	}
	if p.answer == "" {
		p.answer, p.text = answerText, text
	}
	if cause != nil {
		p.err = cause.Error()
	}
	return p
}
