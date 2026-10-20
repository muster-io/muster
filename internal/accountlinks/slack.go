// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/publicid"
)

// The settings of account_link.slack_* (defaults.md), each its own constant although its value is Mattermost's.
const (
	// SlackCodeTTL and SlackMaxAttempts are account_link.slack_code: CodeLength characters of CodeAlphabet, valid 10
	// minutes, at most 5 attempts.
	SlackCodeTTL     = 10 * time.Minute
	SlackMaxAttempts = 5
	// SlackCodeRequestsPerUser and SlackCodeRequestsPerTarget are account_link.slack_code_requests: code requests per
	// requesting User and per target account within CodeRequestWindow.
	SlackCodeRequestsPerUser   = 5
	SlackCodeRequestsPerTarget = 3
)

// CodeHandleAmbiguous is the field error of a name that several Slack accounts have.
const CodeHandleAmbiguous = "handle_ambiguous"

var (
	// ErrHandleAmbiguous is a name that several Slack accounts have (C-24.FR-12).
	ErrHandleAmbiguous = errors.New("several slack accounts have this name")
	// ErrMessagesTabDisabled is a direct message Slack refused because the Messages tab of the Slack app is off, which
	// C-24.FR-12 keeps on: no account of the Connection receives a code until an admin of the app turns it on.
	ErrMessagesTabDisabled = errors.New("the messages tab of the slack app is off")
)

// SlackAccount is a Slack account found by email or name: its user id, its name, and its display name — its
// profile's display_name, else its real_name, empty without either.
type SlackAccount struct {
	ID          string
	Name        string
	DisplayName string
}

// SlackBot is the bot of a Slack Connection while it holds one limiter token of the interactive path.
type SlackBot interface {
	// FindUser finds the account with handle, an email or a name (C-24.FR-12): none — a deleted account, a bot and
	// Slackbot included — is ErrMessengerUserNotFound, and several with the name ErrHandleAmbiguous.
	FindUser(ctx context.Context, handle string) (SlackAccount, error)
	// SendCode sends text, which holds code, by direct message to the account userID, with code masked in the Request
	// log; an account the bot cannot write to is ErrDirectMessageRefused, and an app whose Messages tab is off
	// ErrMessagesTabDisabled.
	SendCode(ctx context.Context, userID string, code logging.Secret, text string) error
	// SendLinked sends text by direct message to the account userID.
	SendLinked(ctx context.Context, userID, text string) error
}

// SlackBots run the calls of a link through the bot of a Slack Connection; slack.LinkBots implements it.
type SlackBots interface {
	// Session takes one limiter token of the Connection connectionPublicID on the interactive path and runs f with its
	// bot, returning what f returns. No token within the interactive budget is a *delivery.LimitedError and runs
	// nothing; a Connection that went away is ErrNoConnection.
	Session(ctx context.Context, connectionPublicID string, f func(ctx context.Context, b SlackBot) error) error
}

// SlackStarted is a started Slack link: the request to confirm, when its code expires and how many attempts it has.
type SlackStarted struct {
	RequestPublicID   string
	ExpiresAt         time.Time
	AttemptsRemaining int
}

// StartSlack starts a link of the requesting User to the account of the Slack Connection connectionPublicID that
// handle names, an email or a name (C-24.FR-12): handle is trimmed and may not be empty. Code requests beyond
// account_link.slack_code_requests within the last hour — of the User, or for the target account — are a
// *RateLimitedError until the oldest one counted is an hour old; the User's are counted before the account is looked
// up, and both again in the transaction that stores the request, under the lock of the User and of the target
// account. With one limiter token of the interactive path, taken before anything is stored: the account is found by
// the bot (none: FieldError handle_not_found; several with the name: FieldError handle_ambiguous); an account linked to
// another User is ErrLinkedElsewhere, recorded as link_rejected with conflict; otherwise the User's pending Slack
// requests for the Connection are voided, a code of account_link.slack_code is stored as its SHA-256 with the account's
// user id, name and display name, and the bot sends it by direct message. A message that fails voids the request, which
// still counts toward the limits; one Slack refuses for the account is a FieldError direct_message_refused, and one it
// refuses because the app's Messages tab is off ErrMessagesTabDisabled. A Connection that is missing, deleted or not a Slack one is a FieldError unknown_id.
func (s *Service) StartSlack(ctx context.Context, r Requester, connectionPublicID, handle string) (SlackStarted,
	error) {
	conn, err := s.connection(ctx, connectionPublicID, MessengerSlack)
	if err != nil {
		return SlackStarted{}, err
	}
	handle = strings.TrimSpace(handle)
	if strings.TrimSpace(strings.TrimPrefix(handle, "@")) == "" {
		return SlackStarted{}, &FieldError{Pointer: pointerHandle, Code: CodeRequired,
			Detail: "The email or name is empty."}
	}
	if err := s.slackUserCodeLimit(ctx, s.cfg.Store, r.UserID, s.now()); err != nil {
		return SlackStarted{}, err
	}
	ref := connRef{id: conn.ID, publicID: conn.PublicID, name: conn.Name}
	var started SlackStarted
	err = s.cfg.Slack.Session(ctx, conn.PublicID, func(ctx context.Context, b SlackBot) error {
		var err error
		started, err = s.startSlack(ctx, b, r, ref, handle)
		return err
	})
	if errors.Is(err, ErrNoConnection) {
		return SlackStarted{}, &FieldError{Pointer: pointerConnection, Code: CodeUnknownID,
			Detail: "No Slack Connection has this id."}
	}
	return started, err
}

// startSlack is the part of StartSlack that holds the limiter token.
func (s *Service) startSlack(ctx context.Context, b SlackBot, r Requester, conn connRef, handle string) (SlackStarted,
	error) {
	acct, err := b.FindUser(ctx, handle)
	switch {
	case errors.Is(err, ErrMessengerUserNotFound):
		return SlackStarted{}, &FieldError{Pointer: pointerHandle, Code: CodeHandleNotFound,
			Detail: "No Slack account with this email or name was found."}
	case errors.Is(err, ErrHandleAmbiguous):
		return SlackStarted{}, &FieldError{Pointer: pointerHandle, Code: CodeHandleAmbiguous,
			Detail: "Several Slack accounts have this name. Enter the email instead."}
	case err != nil:
		return SlackStarted{}, err
	}
	now := s.now()
	username := acct.Name
	a := account{messenger: MessengerSlack, conn: conn, space: SpaceSlack(conn.id), external: acct.ID,
		username: &username}
	if acct.DisplayName != "" {
		name := acct.DisplayName
		a.displayName = &name
	}
	code, err := newCode(s.cfg.Random)
	if err != nil {
		return SlackStarted{}, err
	}
	var (
		conflict bool
		lang     string
		reqID    int64
		started  = SlackStarted{RequestPublicID: publicid.New(publicid.AccountLinkRequest),
			ExpiresAt: now.Add(SlackCodeTTL), AttemptsRemaining: SlackMaxAttempts}
	)
	err = s.cfg.Store.InTx(ctx, func(q Queries) error {
		conflict = false
		u, err := q.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: r.UserID})
		if err != nil {
			return fmt.Errorf("lock the user: %w", err)
		}
		lang = language(u.Language)
		if err := s.slackUserCodeLimit(ctx, q, u.ID, now); err != nil {
			return err
		}
		if err := s.slackTargetCodeLimit(ctx, q, conn.id, acct.ID, now); err != nil {
			return err
		}
		other, err := q.GetAccountLinkBySpace(ctx, dbgen.GetAccountLinkBySpaceParams{OrgID: s.cfg.OrgID,
			IdentitySpace: a.space, ExternalID: a.external})
		switch {
		case err == nil && other.UserID != u.ID:
			conflict = true
			return s.reject(ctx, q, r.Actor, r.Transport, r.Address, a, ReasonConflict, u.PublicID)
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read the link of the account: %w", err)
		}
		if _, err := q.VoidPendingSlackRequests(ctx, dbgen.VoidPendingSlackRequestsParams{Now: now,
			OrgID: s.cfg.OrgID, UserID: u.ID, ConnectionID: conn.id}); err != nil {
			return fmt.Errorf("void the earlier link requests: %w", err)
		}
		reqID, err = q.InsertLinkRequest(ctx, dbgen.InsertLinkRequestParams{OrgID: s.cfg.OrgID,
			PublicID: started.RequestPublicID, UserID: u.ID, Messenger: MessengerSlack, ConnectionID: conn.id,
			SecretHash: hash(code), TargetExternalID: optText(&acct.ID), TargetUsername: optText(a.username),
			TargetDisplayName: optText(a.displayName), CreatedAt: now, ExpiresAt: started.ExpiresAt})
		if err != nil {
			return fmt.Errorf("store the link request: %w", err)
		}
		return nil
	})
	if err != nil {
		return SlackStarted{}, err
	}
	if conflict {
		s.logRejected(ctx, MessengerSlack, conn.publicID, ReasonConflict, nil)
		return SlackStarted{}, ErrLinkedElsewhere
	}
	err = b.SendCode(ctx, acct.ID, logging.Secret(code), messages.T(lang, "accountLink.code",
		messages.Args{"code": code}))
	if err == nil {
		return started, nil
	}
	if verr := s.cfg.Store.VoidLinkRequest(context.WithoutCancel(ctx), dbgen.VoidLinkRequestParams{Now: now,
		OrgID: s.cfg.OrgID, ID: reqID}); verr != nil {
		return SlackStarted{}, errors.Join(err, fmt.Errorf("void the link request: %w", verr))
	}
	if errors.Is(err, ErrDirectMessageRefused) {
		return SlackStarted{}, &FieldError{Pointer: pointerHandle, Code: CodeDirectMessageRefused,
			Detail: "The bot cannot send a direct message to this Slack account."}
	}
	if errors.Is(err, ErrMessagesTabDisabled) {
		return SlackStarted{}, ErrMessagesTabDisabled
	}
	return SlackStarted{}, err
}

// slackUserCodeLimit refuses a Slack code request of the User userID beyond account_link.slack_code_requests within
// the hour before now with a *RateLimitedError. In the transaction that stores the request the User's row is locked,
// so that the requests of one User are counted one at a time.
func (s *Service) slackUserCodeLimit(ctx context.Context, q Queries, userID int64, now time.Time) error {
	n, err := q.CountUserSlackCodeRequests(ctx, dbgen.CountUserSlackCodeRequestsParams{
		Since: now.Add(-CodeRequestWindow), OrgID: s.cfg.OrgID, UserID: userID})
	if err != nil {
		return fmt.Errorf("count the link code requests: %w", err)
	}
	if n.Requests >= SlackCodeRequestsPerUser {
		return &RateLimitedError{RetryAfter: n.Oldest.Add(CodeRequestWindow).Sub(now)}
	}
	return nil
}

// slackTargetCodeLimit takes the lock of the code requests for the account target of the Slack Connection connID
// until the transaction of q ends, then refuses a request beyond account_link.slack_code_requests within the hour
// before now with a *RateLimitedError.
func (s *Service) slackTargetCodeLimit(ctx context.Context, q Queries, connID int64, target string,
	now time.Time) error {
	if err := q.LockCodeTarget(ctx, dbgen.LockCodeTargetParams{LockClass: db.AccountLinkCodeTargetLockClass,
		OrgID: s.cfg.OrgID, ConnectionID: connID, TargetExternalID: target}); err != nil {
		return fmt.Errorf("lock the link code requests of the account: %w", err)
	}
	n, err := q.CountTargetSlackCodeRequests(ctx, dbgen.CountTargetSlackCodeRequestsParams{
		Since: now.Add(-CodeRequestWindow), OrgID: s.cfg.OrgID, ConnectionID: connID,
		TargetExternalID: optText(&target)})
	if err != nil {
		return fmt.Errorf("count the link code requests: %w", err)
	}
	if n.Requests >= SlackCodeRequestsPerTarget {
		return &RateLimitedError{RetryAfter: n.Oldest.Add(CodeRequestWindow).Sub(now)}
	}
	return nil
}

// ConfirmSlack confirms the Slack link request requestPublicID of the requesting User with code (C-24.FR-12,
// AC-14); a request of another User, or one that is not a Slack request, is ErrNotFound. With one limiter token of the
// interactive path, taken before the code is checked: a consumed, void or expired request, a wrong code, and a request
// whose Connection was deleted are a FieldError link_code_invalid at /code, never saying which, recorded as
// link_rejected with token_used, wrong_code or token_expired — the deleted Connection is not recorded — with the
// attempts the request has left; a wrong code counts an attempt and the one that reaches account_link.slack_code's
// limit voids the request, so that it has none left. A right code links as ConfirmBridge does — conflict check,
// replacement, the link with the account's user id, name and display name, the hint — consumes the request, and the
// bot confirms "Linked to Muster user {name}" by direct message; a confirmation that fails is only logged.
func (s *Service) ConfirmSlack(ctx context.Context, r Requester, requestPublicID, code string) (Link, error) {
	norm, err := publicid.Parse(publicid.AccountLinkRequest, requestPublicID)
	if err != nil {
		return Link{}, ErrNotFound
	}
	req, err := s.cfg.Store.GetLinkRequest(ctx, dbgen.GetLinkRequestParams{OrgID: s.cfg.OrgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (req.UserID != r.UserID || req.Messenger != MessengerSlack)) {
		return Link{}, ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("read the link request: %w", err)
	}
	invalid := func(left int) error {
		return &FieldError{Pointer: pointerCode, Code: CodeLinkCodeInvalid, AttemptsRemaining: &left,
			Detail: "The code is wrong, used or expired; request a new one after five wrong codes."}
	}
	if req.ConnectionDeleted {
		return Link{}, invalid(0)
	}
	a := account{messenger: MessengerSlack, conn: connRef{id: req.ConnectionID, publicID: req.ConnectionPublicID,
		name: req.ConnectionName}, space: SpaceSlack(req.ConnectionID), external: req.TargetExternalID.String,
		username: textPtr(req.TargetUsername), displayName: textPtr(req.TargetDisplayName)}
	var out codeOutcome
	err = s.cfg.Slack.Session(ctx, req.ConnectionPublicID, func(ctx context.Context, b SlackBot) error {
		var err error
		if out, err = s.checkCode(ctx, r, req.ID, a, code); err != nil || out.reason != "" {
			return err
		}
		replyErr := b.SendLinked(ctx, a.external, messages.T(out.lang, "accountLink.linkedSlack",
			messages.Args{"name": out.name}))
		s.logLinked(ctx, out.link, out.old, out.user, replyErr)
		return nil
	})
	switch {
	case errors.Is(err, ErrNoConnection):
		return Link{}, invalid(0)
	case err != nil:
		return Link{}, err
	case out.reason == "":
		return out.link, nil
	case out.reason == ReasonConflict:
		s.logRejected(ctx, MessengerSlack, a.conn.publicID, out.reason, nil)
		return Link{}, ErrLinkedElsewhere
	}
	s.logRejected(ctx, MessengerSlack, a.conn.publicID, out.reason, nil)
	return Link{}, invalid(out.left)
}
