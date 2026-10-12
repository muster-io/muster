// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/publicid"
)

// MattermostAccount is a Mattermost user found by username.
type MattermostAccount struct {
	ID       string
	Username string
}

// MattermostBot is the bot of a Mattermost Connection while it holds one limiter token of the interactive path.
type MattermostBot interface {
	// FindUser finds the user username with GET /api/v4/users/username/{username}; an unknown username, a bot or a
	// deactivated account is ErrMessengerUserNotFound.
	FindUser(ctx context.Context, username string) (MattermostAccount, error)
	// SendDirect sends text to the user userID in the direct channel of the bot and the user (F-028).
	SendDirect(ctx context.Context, userID, text string) error
}

// MattermostBots run the calls of a link through the bot of a Mattermost Connection; *mattermost.LinkBots implements
// it.
type MattermostBots interface {
	// Session takes one limiter token of the Connection connectionPublicID on the interactive path and runs f with its
	// bot, returning what f returns. No token within the interactive budget is a *delivery.LimitedError and runs
	// nothing; a Connection that went away is ErrNoConnection.
	Session(ctx context.Context, connectionPublicID string, f func(ctx context.Context, b MattermostBot) error) error
}

// MattermostStarted is a started Mattermost link: the request to confirm, when its code expires and how many
// attempts it has.
type MattermostStarted struct {
	RequestPublicID   string
	ExpiresAt         time.Time
	AttemptsRemaining int
}

// StartMattermost starts a Mattermost link of the requesting User to the account username of the Mattermost
// Connection connectionPublicID (C-18.FR-3): a leading @ is dropped. Code requests beyond
// account_link.mattermost_code_requests within the last hour — of the User, or for the target account — are a
// *RateLimitedError until the oldest one counted is an hour old; the User's are counted before the account is looked
// up, and both again in the transaction that stores the request, under the lock of the User and of the target
// account, so that concurrent requests cannot pass the limits. With one limiter token of the interactive path, taken
// before anything is stored: the account is found by its username (unknown: FieldError messenger_user_not_found); an
// account linked to another User is ErrLinkedElsewhere, recorded as link_rejected with conflict; otherwise the User's
// pending requests for the Connection are voided, a code of account_link.mattermost_code is stored as its SHA-256,
// and the bot sends it in a direct message. A message that fails voids the request. A Connection that is missing,
// deleted or not a Mattermost one is a FieldError unknown_id.
func (s *Service) StartMattermost(ctx context.Context, r Requester, connectionPublicID, username string) (
	MattermostStarted, error) {
	conn, err := s.connection(ctx, connectionPublicID, MessengerMattermost)
	if err != nil {
		return MattermostStarted{}, err
	}
	username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	if username == "" {
		return MattermostStarted{}, &FieldError{Pointer: pointerUsername, Code: CodeRequired,
			Detail: "The username is empty."}
	}
	if err := s.userCodeLimit(ctx, s.cfg.Store, r.UserID, s.now()); err != nil {
		return MattermostStarted{}, err
	}
	ref := connRef{id: conn.ID, publicID: conn.PublicID, name: conn.Name}
	var started MattermostStarted
	err = s.cfg.Mattermost.Session(ctx, conn.PublicID, func(ctx context.Context, b MattermostBot) error {
		var err error
		started, err = s.startMattermost(ctx, b, r, ref, username)
		return err
	})
	if errors.Is(err, ErrNoConnection) {
		return MattermostStarted{}, &FieldError{Pointer: pointerConnection, Code: CodeUnknownID,
			Detail: "No Mattermost Connection has this id."}
	}
	return started, err
}

// startMattermost is the part of StartMattermost that holds the limiter token.
func (s *Service) startMattermost(ctx context.Context, b MattermostBot, r Requester, conn connRef, username string) (
	MattermostStarted, error) {
	acct, err := b.FindUser(ctx, username)
	if errors.Is(err, ErrMessengerUserNotFound) {
		return MattermostStarted{}, &FieldError{Pointer: pointerUsername, Code: CodeMessengerUserNotFound,
			Detail: "The Mattermost server has no user with this username."}
	}
	if err != nil {
		return MattermostStarted{}, err
	}
	now := s.now()
	a := account{messenger: MessengerMattermost, conn: conn, space: SpaceMattermost(conn.id), external: acct.ID,
		username: &acct.Username}
	code, err := newCode(s.cfg.Random)
	if err != nil {
		return MattermostStarted{}, err
	}
	var (
		conflict bool
		lang     string
		reqID    int64
		started  = MattermostStarted{RequestPublicID: publicid.New(publicid.AccountLinkRequest),
			ExpiresAt: now.Add(CodeTTL), AttemptsRemaining: MaxAttempts}
	)
	err = s.cfg.Store.InTx(ctx, func(q Queries) error {
		u, err := q.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: r.UserID})
		if err != nil {
			return fmt.Errorf("lock the user: %w", err)
		}
		lang = language(u.Language)
		if err := s.userCodeLimit(ctx, q, u.ID, now); err != nil {
			return err
		}
		if err := s.targetCodeLimit(ctx, q, conn.id, acct.ID, now); err != nil {
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
		if _, err := q.VoidPendingMattermostRequests(ctx, dbgen.VoidPendingMattermostRequestsParams{Now: now,
			OrgID: s.cfg.OrgID, UserID: u.ID, ConnectionID: conn.id}); err != nil {
			return fmt.Errorf("void the earlier link requests: %w", err)
		}
		reqID, err = q.InsertLinkRequest(ctx, dbgen.InsertLinkRequestParams{OrgID: s.cfg.OrgID,
			PublicID: started.RequestPublicID, UserID: u.ID, Messenger: MessengerMattermost, ConnectionID: conn.id,
			SecretHash: hash(code), TargetExternalID: optText(&acct.ID), TargetUsername: optText(&acct.Username),
			CreatedAt: now, ExpiresAt: started.ExpiresAt})
		if err != nil {
			return fmt.Errorf("store the link request: %w", err)
		}
		return nil
	})
	if err != nil {
		return MattermostStarted{}, err
	}
	if conflict {
		s.logRejected(ctx, MessengerMattermost, conn.publicID, ReasonConflict, nil)
		return MattermostStarted{}, ErrLinkedElsewhere
	}
	if err := b.SendDirect(ctx, acct.ID, messages.T(lang, "accountLink.code", messages.Args{"code": code})); err != nil {
		if verr := s.cfg.Store.VoidLinkRequest(context.WithoutCancel(ctx), dbgen.VoidLinkRequestParams{Now: now,
			OrgID: s.cfg.OrgID, ID: reqID}); verr != nil {
			return MattermostStarted{}, errors.Join(err, fmt.Errorf("void the link request: %w", verr))
		}
		return MattermostStarted{}, err
	}
	return started, nil
}

// userCodeLimit refuses a code request of the User userID beyond account_link.mattermost_code_requests within the
// hour before now with a *RateLimitedError. In the transaction that stores the request the User's row is locked, so
// that the requests of one User are counted one at a time.
func (s *Service) userCodeLimit(ctx context.Context, q Queries, userID int64, now time.Time) error {
	n, err := q.CountUserCodeRequests(ctx, dbgen.CountUserCodeRequestsParams{Since: now.Add(-CodeRequestWindow),
		OrgID: s.cfg.OrgID, UserID: userID})
	if err != nil {
		return fmt.Errorf("count the link code requests: %w", err)
	}
	if n.Requests >= CodeRequestsPerUser {
		return &RateLimitedError{RetryAfter: n.Oldest.Add(CodeRequestWindow).Sub(now)}
	}
	return nil
}

// targetCodeLimit takes the lock of the code requests for the account target of the Connection connID until the
// transaction of q ends, then refuses a request beyond account_link.mattermost_code_requests within the hour before
// now with a *RateLimitedError.
func (s *Service) targetCodeLimit(ctx context.Context, q Queries, connID int64, target string, now time.Time) error {
	if err := q.LockCodeTarget(ctx, dbgen.LockCodeTargetParams{LockClass: db.AccountLinkCodeTargetLockClass,
		OrgID: s.cfg.OrgID, ConnectionID: connID, TargetExternalID: target}); err != nil {
		return fmt.Errorf("lock the link code requests of the account: %w", err)
	}
	n, err := q.CountTargetCodeRequests(ctx, dbgen.CountTargetCodeRequestsParams{Since: now.Add(-CodeRequestWindow),
		OrgID: s.cfg.OrgID, ConnectionID: connID, TargetExternalID: optText(&target)})
	if err != nil {
		return fmt.Errorf("count the link code requests: %w", err)
	}
	if n.Requests >= CodeRequestsPerTarget {
		return &RateLimitedError{RetryAfter: n.Oldest.Add(CodeRequestWindow).Sub(now)}
	}
	return nil
}

// ConfirmMattermost confirms the Mattermost link request requestPublicID of the requesting User with code
// (C-18.FR-3, AC-2); a request of another User is ErrNotFound. With one limiter token of the interactive path, taken
// before the code is checked: a consumed, void or expired request, or a wrong code, is a FieldError link_code_invalid
// at /code, never saying which, recorded as link_rejected with token_used, wrong_code or token_expired; a wrong code
// counts an attempt and the one that reaches account_link.mattermost_code's limit voids the request. A right code
// links as LinkTelegram does — conflict check, replacement, the link, the hint — consumes the request, and the bot
// confirms "Linked to Muster user {name}" in the direct channel; a confirmation that fails is only logged.
func (s *Service) ConfirmMattermost(ctx context.Context, r Requester, requestPublicID, code string) (Link, error) {
	norm, err := publicid.Parse(publicid.AccountLinkRequest, requestPublicID)
	if err != nil {
		return Link{}, ErrNotFound
	}
	req, err := s.cfg.Store.GetLinkRequest(ctx, dbgen.GetLinkRequestParams{OrgID: s.cfg.OrgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (req.UserID != r.UserID || req.Messenger != MessengerMattermost)) {
		return Link{}, ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("read the link request: %w", err)
	}
	invalid := &FieldError{Pointer: pointerCode, Code: CodeLinkCodeInvalid,
		Detail: "The code is wrong, used or expired; request a new one after five wrong codes."}
	var link Link
	err = s.cfg.Mattermost.Session(ctx, req.ConnectionPublicID, func(ctx context.Context, b MattermostBot) error {
		var err error
		link, err = s.confirmMattermost(ctx, b, r, req, code)
		return err
	})
	switch {
	case errors.Is(err, ErrNoConnection), errors.Is(err, errRejected):
		return Link{}, invalid
	case err != nil:
		return Link{}, err
	}
	return link, nil
}

// errRejected is a confirmation that was rejected and recorded.
var errRejected = errors.New("the link code was rejected")

// confirmMattermost is the part of ConfirmMattermost that holds the limiter token.
func (s *Service) confirmMattermost(ctx context.Context, b MattermostBot, r Requester, req dbgen.GetLinkRequestRow,
	code string) (Link, error) {
	a := account{messenger: MessengerMattermost, conn: connRef{id: req.ConnectionID,
		publicID: req.ConnectionPublicID, name: req.ConnectionName}, space: SpaceMattermost(req.ConnectionID),
		external: req.TargetExternalID.String, username: textPtr(req.TargetUsername)}
	var (
		reason string
		link   Link
		old    *view
		u      dbgen.LockLinkUserRow
	)
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		reason, old = "", nil
		row, err := q.LockLinkRequest(ctx, dbgen.LockLinkRequestParams{OrgID: s.cfg.OrgID, ID: req.ID})
		if err != nil {
			return fmt.Errorf("lock the link request: %w", err)
		}
		if u, err = q.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: r.UserID}); err != nil {
			return fmt.Errorf("lock the user: %w", err)
		}
		now := s.now()
		switch {
		case row.ConsumedAt.Valid:
			reason = ReasonTokenUsed
		case row.VoidedAt.Valid:
			reason = ReasonWrongCode
		case !row.ExpiresAt.After(now):
			reason = ReasonTokenExpired
		case subtle.ConstantTimeCompare(hash(normalizeCode(code)), row.SecretHash) != 1:
			reason = ReasonWrongCode
			if _, err := q.RecordWrongCode(ctx, dbgen.RecordWrongCodeParams{MaxAttempts: MaxAttempts, Now: now,
				OrgID: s.cfg.OrgID, ID: row.ID}); err != nil {
				return fmt.Errorf("count the wrong code: %w", err)
			}
		}
		if reason != "" {
			return s.reject(ctx, q, r.Actor, r.Transport, r.Address, a, reason, u.PublicID)
		}
		link, old, err = s.link(ctx, q, u, a, r.Actor, r.Transport, r.Address)
		if errors.Is(err, ErrLinkedElsewhere) {
			reason = ReasonConflict
			return s.reject(ctx, q, r.Actor, r.Transport, r.Address, a, reason, u.PublicID)
		}
		if err != nil {
			return err
		}
		if _, err := q.ConsumeLinkRequest(ctx, dbgen.ConsumeLinkRequestParams{Now: now, OrgID: s.cfg.OrgID,
			ID: row.ID}); err != nil {
			return fmt.Errorf("consume the link request: %w", err)
		}
		return nil
	})
	if err != nil {
		return Link{}, err
	}
	switch reason {
	case "":
	case ReasonConflict:
		s.logRejected(ctx, MessengerMattermost, a.conn.publicID, reason, nil)
		return Link{}, ErrLinkedElsewhere
	default:
		s.logRejected(ctx, MessengerMattermost, a.conn.publicID, reason, nil)
		return Link{}, errRejected
	}
	replyErr := b.SendDirect(ctx, a.external, messages.T(language(u.Language), "accountLink.linkedMattermost",
		messages.Args{"name": u.Name}))
	s.logLinked(ctx, link, old, u.PublicID, replyErr)
	return link, nil
}

// newCode draws a code of CodeLength characters of CodeAlphabet from random, without bias.
func newCode(random io.Reader) (string, error) {
	const limit = 256 - 256%len(CodeAlphabet)
	out := make([]byte, 0, CodeLength)
	buf := make([]byte, 2*CodeLength)
	for len(out) < CodeLength {
		if _, err := io.ReadFull(random, buf); err != nil {
			return "", fmt.Errorf("generate the link code: %w", err)
		}
		for _, c := range buf {
			if int(c) < limit && len(out) < CodeLength {
				out = append(out, CodeAlphabet[int(c)%len(CodeAlphabet)])
			}
		}
	}
	return string(out), nil
}

// normalizeCode is a code as typed, compared without case and surrounding spaces.
func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}
