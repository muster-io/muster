// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package accountlinks

import (
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/publicid"
)

// The settings of account_link.bridge_* (defaults.md), each its own constant although its value is Telegram's or
// Mattermost's.
const (
	// BridgeTokenTTL is account_link.bridge_token_ttl: how long a token sent to a bridge's bot is valid.
	BridgeTokenTTL = 10 * time.Minute
	// BridgeCodeTTL and BridgeMaxAttempts are account_link.bridge_code: CodeLength characters of CodeAlphabet, valid 10
	// minutes, at most 5 attempts.
	BridgeCodeTTL     = 10 * time.Minute
	BridgeMaxAttempts = 5
	// BridgeCodeRequestsPerUser and BridgeCodeRequestsPerTarget are account_link.bridge_code_requests: code requests
	// per requesting User and per target account within CodeRequestWindow.
	BridgeCodeRequestsPerUser   = 5
	BridgeCodeRequestsPerTarget = 3
)

// The methods of a Bridge link (C-22.FR-15): a token the person sends to the bot, or a code the bot sends them.
const (
	MethodToken = "token"
	MethodCode  = "code"
)

// The kinds of handle a bridge looks people up by, its info's user_lookup (api/bridge.yaml).
const (
	LookupEmail    = "email"
	LookupLogin    = "login"
	LookupUsername = "username"
	LookupUserID   = "user_id"
	LookupNone     = "none"
)

// The codes of the field errors of a Bridge code request.
const (
	CodeHandleNotFound       = "handle_not_found"
	CodeDirectMessageRefused = "direct_message_refused"
	pointerHandle            = "/handle"
)

var (
	// ErrCodeNotOffered is a code request through a Bridge Connection whose info offers no code.
	ErrCodeNotOffered = errors.New("the bridge offers no link code")
	// ErrDirectMessageRefused is a direct message the bridge refused as forbidden: the account cannot receive one from
	// the bot.
	ErrDirectMessageRefused = errors.New("the bridge refused the direct message")
)

// lookupNames name the kinds of handle in the detail of handle_not_found.
var lookupNames = map[string]string{LookupEmail: "email", LookupLogin: "login", LookupUsername: "username",
	LookupUserID: "user id"}

// BridgeOffer is what the info of a Bridge Connection says of its Account links (C-22.FR-15): whether Muster has read
// the info at all, its user_lookup, and whether its bot can start a direct chat.
type BridgeOffer struct {
	Read        bool
	UserLookup  string
	BotCanStart bool
}

// Code reports whether a code by direct message is offered: the info declares direct_messages with bot_can_start and
// a user_lookup other than none; never while Muster has not read the info.
func (o BridgeOffer) Code() bool {
	_, known := lookupNames[o.UserLookup]
	return o.Read && o.BotCanStart && known
}

// Methods are the ways an account can be linked: the token always, and the code where it is offered.
func (o BridgeOffer) Methods() []string {
	if o.Code() {
		return []string{MethodToken, MethodCode}
	}
	return []string{MethodToken}
}

// HandleKind is the kind of handle a code needs, empty when no code is offered.
func (o BridgeOffer) HandleKind() string {
	if o.Code() {
		return o.UserLookup
	}
	return ""
}

// BridgeAccount is an account of a bridge's messenger: its external id, its name and its handle, empty without one.
type BridgeAccount struct {
	ExternalID  string
	DisplayName string
	Handle      string
}

// BridgeBot is the bot of a Bridge Connection while it holds one limiter token of the interactive path.
type BridgeBot interface {
	// Offer is what the Connection's info offers, read again first when Muster's copy is older than
	// bridge.info_max_age (C-22.FR-2); the stale copy when that read fails, and nothing read without one.
	Offer() BridgeOffer
	// FindUser finds the account with handle through findUsers; none is ErrMessengerUserNotFound.
	FindUser(ctx context.Context, handle string) (BridgeAccount, error)
	// SendDirectMessage sends text to the account externalID through sendDirectMessage with the Idempotency-Key key;
	// an account the bot cannot write to is ErrDirectMessageRefused.
	SendDirectMessage(ctx context.Context, key, externalID, text string) error
}

// BridgeBots run the calls of a link by code through the bot of a Bridge Connection; bridge.LinkBots implements it.
type BridgeBots interface {
	// Session takes one limiter token of the Connection connectionPublicID on the interactive path and runs f with its
	// bot, returning what f returns. No token within the interactive budget is a *delivery.LimitedError and runs
	// nothing; a Connection that went away is ErrNoConnection.
	Session(ctx context.Context, connectionPublicID string, f func(ctx context.Context, b BridgeBot) error) error
}

// BridgeStart is a started Bridge link by token: the token, the link that hands it to the bot, nil without one, and
// when the token expires.
type BridgeStart struct {
	Token     string
	LinkURL   *string
	ExpiresAt time.Time
}

// StartBridge starts a link of the requesting User through the Bridge Connection connectionPublicID by token
// (C-22.FR-15): 32 random bytes in base64url, of which only the SHA-256 is stored with the User, the Connection and the
// expiry account_link.bridge_token_ttl from now, and the link built from the link_url_template of the Connection's
// stored info, kept only when it is an absolute http or https link. It calls no bridge. A Connection that is missing,
// deleted or not a Bridge one is a FieldError unknown_id.
func (s *Service) StartBridge(ctx context.Context, r Requester, connectionPublicID string) (BridgeStart, error) {
	conn, err := s.connection(ctx, connectionPublicID, MessengerBridge)
	if err != nil {
		return BridgeStart{}, err
	}
	raw := make([]byte, tokenBytes)
	if _, err := io.ReadFull(s.cfg.Random, raw); err != nil {
		return BridgeStart{}, fmt.Errorf("generate the link token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	expires := now.Add(BridgeTokenTTL)
	method := MethodToken
	if _, err := s.cfg.Store.InsertLinkRequest(ctx, dbgen.InsertLinkRequestParams{OrgID: s.cfg.OrgID,
		PublicID: publicid.New(publicid.AccountLinkRequest), UserID: r.UserID, Messenger: MessengerBridge,
		ConnectionID: conn.ID, Method: optText(&method), SecretHash: hash(token), CreatedAt: now,
		ExpiresAt: expires}); err != nil {
		return BridgeStart{}, fmt.Errorf("store the link request: %w", err)
	}
	return BridgeStart{Token: token, LinkURL: bridgeLinkURL(conn.LinkUrlTemplate, token), ExpiresAt: expires}, nil
}

// bridgeLinkURL is template with token in place of {token}, nil unless the result is an absolute http or https link.
func bridgeLinkURL(template, token string) *string {
	if !strings.Contains(template, "{token}") {
		return nil
	}
	u := strings.ReplaceAll(template, "{token}", token)
	if messages.SafeURL(u) != u {
		return nil
	}
	return &u
}

// BridgeConnection is the Bridge Connection whose bot received a token.
type BridgeConnection struct {
	ID       int64
	PublicID string
}

// BridgeLinked is how an account_link event ended: Answer, the text to answer the person with, and Log, which logs
// account_link_linked or account_link_rejected once the answer was made or failed, with answerErr, the error of the
// answer, if any; the caller strips the token from it, since a bridge may repeat what it was sent.
type BridgeLinked struct {
	Answer string
	Log    func(ctx context.Context, answerErr error)
}

// bridgeOutcome is how an account_link event ended in its transaction: the link, the reason it was rejected, or that
// the token had already linked this account, and the answer.
type bridgeOutcome struct {
	link   Link
	old    *view
	user   string
	reason string
	again  bool
	answer string
}

// LinkBridge handles an account_link event: the account from sent token to the bot of conn (C-22.FR-15, C-18.FR-4,
// FR-5). The token is looked up by its hash among the Bridge tokens; it must be unconsumed, not void, not expired and
// bound to conn. Otherwise nothing is linked, account_link.link_rejected is recorded with the reason token_unknown,
// token_used, token_expired or wrong_connection, and the answer is "This link is invalid or expired. Start again from
// your Muster profile." — in English for an unknown token, else in the profile language of the User it is bound to. An
// account linked to another User is rejected with conflict and the answer that it is linked to another user, the
// request kept. Otherwise, in one transaction, the User's previous link for the Connection is replaced, the account is
// linked with its external id, display name and handle, the request consumed, the link recorded with the Transport
// bridge and the hint sent; the answer is "Linked to Muster user {name} ({login})". A consumed token whose User has
// the sender's account linked in the Connection's identity space — the event again after a crash between this
// transaction and the commit of its claim — is answered the same again, with nothing recorded or logged. It answers
// nothing itself: the caller answers once the event's claim committed and then calls Log. An error is a database
// failure, which leaves the event to come again.
func (s *Service) LinkBridge(ctx context.Context, conn BridgeConnection, token string, from BridgeAccount) (
	BridgeLinked, error) {
	name := from.DisplayName
	a := account{messenger: MessengerBridge, conn: connRef{id: conn.ID, publicID: conn.PublicID},
		space: SpaceBridge(conn.ID), external: from.ExternalID, displayName: &name}
	if from.Handle != "" {
		handle := from.Handle
		a.username = &handle
	}
	var out bridgeOutcome
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		out = bridgeOutcome{}
		c, err := q.GetLinkConnection(ctx, dbgen.GetLinkConnectionParams{OrgID: s.cfg.OrgID, PublicID: conn.PublicID})
		if err != nil {
			return fmt.Errorf("read the connection %s: %w", conn.PublicID, err)
		}
		a.conn.name = c.Name
		return s.linkBridge(ctx, q, a, conn, token, &out)
	})
	if err != nil {
		return BridgeLinked{}, err
	}
	return BridgeLinked{Answer: out.answer, Log: func(ctx context.Context, answerErr error) {
		switch {
		case out.again:
		case out.reason != "":
			s.logRejected(ctx, MessengerBridge, conn.PublicID, out.reason, answerErr)
		default:
			s.logLinked(ctx, out.link, out.old, out.user, answerErr)
		}
	}}, nil
}

// linkBridge is the transaction of LinkBridge; out says how it ended. It reads the request by the token's hash without
// a lock and then locks the User before the request, as the deletion of a User locks the User before it voids the
// User's pending requests, so that a link and the deletion of its User do not deadlock; the request's state comes from
// its locked row, and a request pruned in between is an unknown token.
func (s *Service) linkBridge(ctx context.Context, q Queries, a account, conn BridgeConnection, token string,
	out *bridgeOutcome) error {
	system := audit.System
	invalid := func(lang string) string { return messages.T(lang, "accountLink.invalid", nil) }
	unknown := func() error {
		out.reason, out.answer = ReasonTokenUnknown, invalid(messages.LanguageEnglish)
		return s.reject(ctx, q, system, audit.TransportBridge, noAddress, a, out.reason, "")
	}
	r, err := q.GetBridgeTokenRequestByHash(ctx, dbgen.GetBridgeTokenRequestByHashParams{OrgID: s.cfg.OrgID,
		SecretHash: hash(token)})
	if errors.Is(err, pgx.ErrNoRows) {
		return unknown()
	}
	if err != nil {
		return fmt.Errorf("read the link request: %w", err)
	}
	u, err := q.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: r.UserID})
	if err != nil {
		return fmt.Errorf("lock the user %s: %w", r.UserPublicID, err)
	}
	row, err := q.LockLinkRequest(ctx, dbgen.LockLinkRequestParams{OrgID: s.cfg.OrgID, ID: r.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return unknown()
	}
	if err != nil {
		return fmt.Errorf("lock the link request: %w", err)
	}
	lang := language(u.Language)
	linked := messages.T(lang, "accountLink.linkedBridge", messages.Args{"name": u.Name, "login": u.Login})
	switch {
	case row.ConsumedAt.Valid && r.ConnectionID == conn.ID && u.Status != statusDeleted:
		mine, err := q.GetUserLinkInSpace(ctx, dbgen.GetUserLinkInSpaceParams{OrgID: s.cfg.OrgID, UserID: u.ID,
			IdentitySpace: a.space})
		switch {
		case err == nil && mine.ExternalID == a.external:
			out.again, out.answer = true, linked
			return nil
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read the user's account link: %w", err)
		}
		out.reason = ReasonTokenUsed
	case row.ConsumedAt.Valid || row.VoidedAt.Valid || u.Status == statusDeleted:
		out.reason = ReasonTokenUsed
	case !row.ExpiresAt.After(s.now()):
		out.reason = ReasonTokenExpired
	case r.ConnectionID != conn.ID:
		out.reason = ReasonWrongConnection
	}
	if out.reason != "" {
		out.answer = invalid(lang)
		return s.reject(ctx, q, system, audit.TransportBridge, noAddress, a, out.reason, u.PublicID)
	}
	l, old, err := s.link(ctx, q, u, a, audit.User(u.ID, u.PublicID), audit.TransportBridge, noAddress)
	if errors.Is(err, ErrLinkedElsewhere) {
		out.reason, out.answer = ReasonConflict, messages.T(lang, "accountLink.conflict", nil)
		return s.reject(ctx, q, system, audit.TransportBridge, noAddress, a, out.reason, u.PublicID)
	}
	if err != nil {
		return err
	}
	if _, err := q.ConsumeLinkRequest(ctx, dbgen.ConsumeLinkRequestParams{Now: s.now(), OrgID: s.cfg.OrgID,
		ID: r.ID}); err != nil {
		return fmt.Errorf("consume the link request: %w", err)
	}
	out.link, out.old, out.user, out.answer = l, old, u.PublicID, linked
	return nil
}

// BridgeCodeStarted is a started Bridge link by code: the request to confirm, when its code expires and how many
// attempts it has.
type BridgeCodeStarted struct {
	RequestPublicID   string
	ExpiresAt         time.Time
	AttemptsRemaining int
}

// StartBridgeCode starts a link of the requesting User to the account with handle of the Bridge Connection
// connectionPublicID by code (C-22.FR-15): handle is trimmed and may not be empty. With one limiter token of the
// interactive path: a Connection whose info offers no code is ErrCodeNotOffered, whatever the limits; code requests
// beyond account_link.bridge_code_requests within the last hour — of the User, or for the target account — are a
// *RateLimitedError until the oldest one counted is an hour old, the User's counted before the bridge looks the
// account up, and both again in the transaction that stores the request, under the lock of the User and of the target
// account; the account is found with findUsers (none: FieldError handle_not_found); an account linked to another User is
// ErrLinkedElsewhere, recorded as link_rejected with conflict; otherwise the User's pending code requests for the
// Connection are voided, a code of account_link.bridge_code is stored as its SHA-256 with the account's external id,
// handle and display name, and the bot sends it with sendDirectMessage, key dm-<request public_id>. A message that
// fails voids the request, which still counts toward the limits; one the bridge refuses as forbidden is a FieldError
// direct_message_refused. A Connection that is missing, deleted or not a Bridge one is a FieldError unknown_id.
func (s *Service) StartBridgeCode(ctx context.Context, r Requester, connectionPublicID, handle string) (
	BridgeCodeStarted, error) {
	conn, err := s.connection(ctx, connectionPublicID, MessengerBridge)
	if err != nil {
		return BridgeCodeStarted{}, err
	}
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return BridgeCodeStarted{}, &FieldError{Pointer: pointerHandle, Code: CodeRequired, Detail: "The handle is empty."}
	}
	ref := connRef{id: conn.ID, publicID: conn.PublicID, name: conn.Name}
	var started BridgeCodeStarted
	err = s.cfg.Bridge.Session(ctx, conn.PublicID, func(ctx context.Context, b BridgeBot) error {
		var err error
		started, err = s.startBridgeCode(ctx, b, r, ref, handle)
		return err
	})
	if errors.Is(err, ErrNoConnection) {
		return BridgeCodeStarted{}, &FieldError{Pointer: pointerConnection, Code: CodeUnknownID,
			Detail: "No Bridge Connection has this id."}
	}
	return started, err
}

// startBridgeCode is the part of StartBridgeCode that holds the limiter token.
func (s *Service) startBridgeCode(ctx context.Context, b BridgeBot, r Requester, conn connRef, handle string) (
	BridgeCodeStarted, error) {
	offer := b.Offer()
	if !offer.Code() {
		return BridgeCodeStarted{}, ErrCodeNotOffered
	}
	if err := s.bridgeUserCodeLimit(ctx, s.cfg.Store, r.UserID, s.now()); err != nil {
		return BridgeCodeStarted{}, err
	}
	acct, err := b.FindUser(ctx, handle)
	if errors.Is(err, ErrMessengerUserNotFound) {
		return BridgeCodeStarted{}, &FieldError{Pointer: pointerHandle, Code: CodeHandleNotFound,
			Detail: "No account with this " + lookupNames[offer.UserLookup] + " was found."}
	}
	if err != nil {
		return BridgeCodeStarted{}, err
	}
	now := s.now()
	username, name := cmp.Or(acct.Handle, handle), acct.DisplayName
	a := account{messenger: MessengerBridge, conn: conn, space: SpaceBridge(conn.id), external: acct.ExternalID,
		username: &username, displayName: &name}
	code, err := newCode(s.cfg.Random)
	if err != nil {
		return BridgeCodeStarted{}, err
	}
	var (
		conflict bool
		lang     string
		reqID    int64
		method   = MethodCode
		started  = BridgeCodeStarted{RequestPublicID: publicid.New(publicid.AccountLinkRequest),
			ExpiresAt: now.Add(BridgeCodeTTL), AttemptsRemaining: BridgeMaxAttempts}
	)
	err = s.cfg.Store.InTx(ctx, func(q Queries) error {
		conflict = false
		u, err := q.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: r.UserID})
		if err != nil {
			return fmt.Errorf("lock the user: %w", err)
		}
		lang = language(u.Language)
		if err := s.bridgeUserCodeLimit(ctx, q, u.ID, now); err != nil {
			return err
		}
		if err := s.bridgeTargetCodeLimit(ctx, q, conn.id, acct.ExternalID, now); err != nil {
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
		if _, err := q.VoidPendingBridgeCodeRequests(ctx, dbgen.VoidPendingBridgeCodeRequestsParams{Now: now,
			OrgID: s.cfg.OrgID, UserID: u.ID, ConnectionID: conn.id}); err != nil {
			return fmt.Errorf("void the earlier link requests: %w", err)
		}
		reqID, err = q.InsertLinkRequest(ctx, dbgen.InsertLinkRequestParams{OrgID: s.cfg.OrgID,
			PublicID: started.RequestPublicID, UserID: u.ID, Messenger: MessengerBridge, ConnectionID: conn.id,
			Method: optText(&method), SecretHash: hash(code), TargetExternalID: optText(&acct.ExternalID),
			TargetUsername: optText(&username), TargetDisplayName: optText(&name), CreatedAt: now,
			ExpiresAt: started.ExpiresAt})
		if err != nil {
			return fmt.Errorf("store the link request: %w", err)
		}
		return nil
	})
	if err != nil {
		return BridgeCodeStarted{}, err
	}
	if conflict {
		s.logRejected(ctx, MessengerBridge, conn.publicID, ReasonConflict, nil)
		return BridgeCodeStarted{}, ErrLinkedElsewhere
	}
	// The code is masked in the Request log's record of the direct message (C-23.FR-3).
	err = b.SendDirectMessage(outbound.WithSecrets(ctx, logging.Secret(code)), "dm-"+started.RequestPublicID,
		acct.ExternalID, messages.T(lang, "accountLink.code", messages.Args{"code": code}))
	if err == nil {
		return started, nil
	}
	if verr := s.cfg.Store.VoidLinkRequest(context.WithoutCancel(ctx), dbgen.VoidLinkRequestParams{Now: now,
		OrgID: s.cfg.OrgID, ID: reqID}); verr != nil {
		return BridgeCodeStarted{}, errors.Join(err, fmt.Errorf("void the link request: %w", verr))
	}
	if errors.Is(err, ErrDirectMessageRefused) {
		return BridgeCodeStarted{}, &FieldError{Pointer: pointerHandle, Code: CodeDirectMessageRefused,
			Detail: "The bot cannot send you a direct message. Send it the token instead."}
	}
	return BridgeCodeStarted{}, err
}

// bridgeUserCodeLimit refuses a Bridge code request of the User userID beyond account_link.bridge_code_requests
// within the hour before now with a *RateLimitedError. In the transaction that stores the request the User's row is
// locked, so that the requests of one User are counted one at a time.
func (s *Service) bridgeUserCodeLimit(ctx context.Context, q Queries, userID int64, now time.Time) error {
	n, err := q.CountUserBridgeCodeRequests(ctx, dbgen.CountUserBridgeCodeRequestsParams{
		Since: now.Add(-CodeRequestWindow), OrgID: s.cfg.OrgID, UserID: userID})
	if err != nil {
		return fmt.Errorf("count the link code requests: %w", err)
	}
	if n.Requests >= BridgeCodeRequestsPerUser {
		return &RateLimitedError{RetryAfter: n.Oldest.Add(CodeRequestWindow).Sub(now)}
	}
	return nil
}

// bridgeTargetCodeLimit takes the lock of the code requests for the account target of the Bridge Connection connID
// until the transaction of q ends, then refuses a request beyond account_link.bridge_code_requests within the hour
// before now with a *RateLimitedError.
func (s *Service) bridgeTargetCodeLimit(ctx context.Context, q Queries, connID int64, target string,
	now time.Time) error {
	if err := q.LockCodeTarget(ctx, dbgen.LockCodeTargetParams{LockClass: db.AccountLinkCodeTargetLockClass,
		OrgID: s.cfg.OrgID, ConnectionID: connID, TargetExternalID: target}); err != nil {
		return fmt.Errorf("lock the link code requests of the account: %w", err)
	}
	n, err := q.CountTargetBridgeCodeRequests(ctx, dbgen.CountTargetBridgeCodeRequestsParams{
		Since: now.Add(-CodeRequestWindow), OrgID: s.cfg.OrgID, ConnectionID: connID,
		TargetExternalID: optText(&target)})
	if err != nil {
		return fmt.Errorf("count the link code requests: %w", err)
	}
	if n.Requests >= BridgeCodeRequestsPerTarget {
		return &RateLimitedError{RetryAfter: n.Oldest.Add(CodeRequestWindow).Sub(now)}
	}
	return nil
}

// ConfirmBridge confirms the Bridge code request requestPublicID of the requesting User with code (C-22.FR-15); a
// request of another User, or one that is not a Bridge code request, is ErrNotFound. A consumed, void or expired
// request, a wrong code, and a request whose Connection was deleted are a FieldError link_code_invalid at /code, never
// saying which, recorded as link_rejected with token_used, wrong_code or token_expired — the deleted Connection is not
// recorded — with the attempts the request has left; a wrong code counts an attempt and the one that reaches
// account_link.bridge_code's limit voids the request, so that it has none left. A right code links as LinkBridge does
// — conflict check, replacement, the link with the account's external id, handle and display name, the hint — and
// consumes the request, with the Transport of r; no direct message confirms it, and no bridge is called.
func (s *Service) ConfirmBridge(ctx context.Context, r Requester, requestPublicID, code string) (Link, error) {
	norm, err := publicid.Parse(publicid.AccountLinkRequest, requestPublicID)
	if err != nil {
		return Link{}, ErrNotFound
	}
	req, err := s.cfg.Store.GetLinkRequest(ctx, dbgen.GetLinkRequestParams{OrgID: s.cfg.OrgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (req.UserID != r.UserID || req.Messenger != MessengerBridge ||
		req.Method.String != MethodCode)) {
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
	a := account{messenger: MessengerBridge, conn: connRef{id: req.ConnectionID, publicID: req.ConnectionPublicID,
		name: req.ConnectionName}, space: SpaceBridge(req.ConnectionID), external: req.TargetExternalID.String,
		username: textPtr(req.TargetUsername), displayName: textPtr(req.TargetDisplayName)}
	out, err := s.checkCode(ctx, r, req.ID, a, code)
	if err != nil {
		return Link{}, err
	}
	switch out.reason {
	case "":
		s.logLinked(ctx, out.link, out.old, out.user, nil)
		return out.link, nil
	case ReasonConflict:
		s.logRejected(ctx, MessengerBridge, a.conn.publicID, out.reason, nil)
		return Link{}, ErrLinkedElsewhere
	}
	s.logRejected(ctx, MessengerBridge, a.conn.publicID, out.reason, nil)
	return Link{}, invalid(out.left)
}

// codeOutcome is how the check of a Bridge code ended: the link, or the reason it was rejected with the attempts the
// request has left.
type codeOutcome struct {
	link   Link
	old    *view
	user   string
	reason string
	left   int
}

// checkCode is the transaction of ConfirmBridge: it checks code against the request requestID of the requesting User
// and links the account a when it is right. It locks the User before the request, as StartBridgeCode locks the User
// before it voids the User's pending requests, so that a confirmation and a new request of one User do not deadlock.
func (s *Service) checkCode(ctx context.Context, r Requester, requestID int64, a account, code string) (codeOutcome,
	error) {
	var out codeOutcome
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		out = codeOutcome{}
		u, err := q.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: r.UserID})
		if err != nil {
			return fmt.Errorf("lock the user: %w", err)
		}
		row, err := q.LockLinkRequest(ctx, dbgen.LockLinkRequestParams{OrgID: s.cfg.OrgID, ID: requestID})
		if err != nil {
			return fmt.Errorf("lock the link request: %w", err)
		}
		now := s.now()
		switch {
		case row.ConsumedAt.Valid:
			out.reason = ReasonTokenUsed
		case row.VoidedAt.Valid:
			out.reason = ReasonWrongCode
		case !row.ExpiresAt.After(now):
			out.reason = ReasonTokenExpired
		case subtle.ConstantTimeCompare(hash(normalizeCode(code)), row.SecretHash) != 1:
			out.reason = ReasonWrongCode
			attempts, err := q.RecordWrongCode(ctx, dbgen.RecordWrongCodeParams{MaxAttempts: BridgeMaxAttempts,
				Now: now, OrgID: s.cfg.OrgID, ID: row.ID})
			if err != nil {
				return fmt.Errorf("count the wrong code: %w", err)
			}
			out.left = max(BridgeMaxAttempts-int(attempts), 0)
		}
		if out.reason != "" {
			return s.reject(ctx, q, r.Actor, r.Transport, r.Address, a, out.reason, u.PublicID)
		}
		out.link, out.old, err = s.link(ctx, q, u, a, r.Actor, r.Transport, r.Address)
		if errors.Is(err, ErrLinkedElsewhere) {
			out.reason = ReasonConflict
			return s.reject(ctx, q, r.Actor, r.Transport, r.Address, a, out.reason, u.PublicID)
		}
		if err != nil {
			return err
		}
		if _, err := q.ConsumeLinkRequest(ctx, dbgen.ConsumeLinkRequestParams{Now: now, OrgID: s.cfg.OrgID,
			ID: row.ID}); err != nil {
			return fmt.Errorf("consume the link request: %w", err)
		}
		out.user = u.PublicID
		return nil
	})
	return out, err
}
