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
)

// CallbackPattern is the pattern of the callback of button presses on the callback mux of the ingest listener.
const CallbackPattern = CallbackPath + "{connection_id}"

// ErrNoConnection is a callback for a Connection that is unknown, deleted or not a Mattermost one.
var ErrNoConnection = errors.New("no such mattermost connection")

// Connection is a Mattermost Connection as the callback of its button presses needs it.
type Connection struct {
	ID       int64
	PublicID string
	Client   *Client
}

// Connections find the Connection of a callback; *connections.Service implements it.
type Connections interface {
	Connection(ctx context.Context, publicID string) (Connection, error)
}

// Bindings are delivery's reads of what a press is bound to; *delivery.Service implements it.
type Bindings interface {
	PressBinding(ctx context.Context, connectionID int64, groupPublicID, postID string) (delivery.Binding, error)
	PostDestination(ctx context.Context, connectionID int64, postID, channelID string) (delivery.Destination, bool,
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
	Log       *logging.Logger
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
	outcomeInvalidRequest    = "invalid_request"
	outcomeUnknownConnection = "unknown_connection"
	outcomeFailed            = "failed"
)

// errUnverifiable is a verified action id that names no Command of its Route: a Snooze duration that no longer exists.
var errUnverifiable = errors.New("the button names no command of its route")

type pressHandler struct {
	cfg CallbackConfig
}

// NewCallback is the callback of the button presses of Mattermost Connections, mattermostAction (C-13.FR-4): it
// verifies the signed action id, binds the press to the Root message it was issued for, maps the Mattermost user to
// a User through its Account link, and runs the Command as that User with the Transport mattermost. Every request is
// answered 200 with a JSON object, never with update. Whatever the person who pressed must read goes first as an
// ephemeral post through the interactive path, which shows in the channel view (F-026), and the answer is empty; when
// that post is not made — refused with 403 to a bot without the create_post_ephemeral permission (F-063), or failed
// any other way — the text is the answer's ephemeral_text, which needs no permission and shows in the Thread (F-025,
// F-062), so that it is never lost (D284).
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
	conn, err := h.cfg.Connections.Connection(ctx, r.PathValue("connection_id"))
	if errors.Is(err, ErrNoConnection) {
		return press{outcome: outcomeUnknownConnection}
	}
	if err != nil {
		return press{outcome: outcomeFailed, err: err.Error()}
	}
	p := press{connection: conn.PublicID}
	a, err := buttons.Verify(h.cfg.Keys, req.Context.Action, req.Context.KeyID)
	if err != nil || a.Subject != buttons.SubjectRoot {
		return h.notVerified(ctx, p, conn, req)
	}
	p.group, p.command = a.PublicID, a.Command
	b, err := h.cfg.Bindings.PressBinding(ctx, conn.ID, a.PublicID, req.PostID)
	if errors.Is(err, delivery.ErrNotBound) || (err == nil && b.ChannelID != req.ChannelID) {
		return h.notVerified(ctx, p, conn, req)
	}
	if err != nil {
		p.outcome, p.err = outcomeFailed, err.Error()
		return p
	}
	answer := func(outcome, text string, cause error) press {
		p.outcome = outcome
		return h.answer(ctx, p, conn, b.Destination, req, text, cause)
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

// dispatch runs the Command of the button a as c; a Snooze lasts the duration of the Route at the button's index.
func (h *pressHandler) dispatch(ctx context.Context, c groups.Caller, a buttons.Action, b delivery.Binding) (
	groups.Result, error) {
	switch a.Command {
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
	return h.answer(ctx, p, conn, d, req, messages.T(messages.LanguageEnglish, "press.notVerified", nil), nil)
}

// answer shows text to the person who pressed (D284): first as one ephemeral post in the channel of the press through
// the interactive path, limited by the Destination d and bounded by the budget, without root_id so that it shows in
// the channel view (F-026). When the post is not made, text becomes the ephemeral_text of the callback's answer (F-062):
// after a 403 for the missing permission, which a bot with the role Member always gets (F-063) and which is therefore
// not an error, and after any other failure, which is logged with cause.
func (h *pressHandler) answer(ctx context.Context, p press, conn Connection, d delivery.Destination, req pressRequest,
	text string, cause error) press {
	ctx, cancel := context.WithTimeout(ctx, h.cfg.Budget)
	defer cancel()
	var r Result
	out, err := h.cfg.Path.Do(ctx, delivery.Subject{Destination: &d}, delivery.AnswerOp(
		func(ctx context.Context, c delivery.Call) delivery.Outcome {
			r = conn.Client.ephemeralPost(ctx, c.Class, req.UserID, req.ChannelID, "", text)
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
