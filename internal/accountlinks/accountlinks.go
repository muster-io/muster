// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package accountlinks holds the Account links of C-18 and C-22.FR-15: which messenger account, in which identity
// space, acts as which User. A Telegram account lives in the one identity space telegram, a Mattermost or a Bridge
// account in the space of its Connection. Every link starts in the User's web session and the secret travels from there
// to the messenger account (ADR-0013): a Telegram deep link whose token the bot receives with `/start`, a token the
// person sends to a bridge's bot, or a code that a Mattermost or a bridge's bot sends as a direct message and the User
// types into the profile. The package links, replaces and unlinks, records the Audit log, sends the account-links hint
// to the link's User, and looks links up for the button presses of the messengers. It never calls a messenger itself:
// the bots of the messenger packages do, on the interactive path.
package accountlinks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/accountlinks/dbgen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/messages"
	"github.com/muster-io/muster/internal/publicid"
	usersdb "github.com/muster-io/muster/internal/users/dbgen"
)

// The settings of account_link.* (defaults.md).
const (
	// TelegramTokenTTL is account_link.telegram_token_ttl: how long a deep-link token is valid.
	TelegramTokenTTL = 10 * time.Minute
	// CodeTTL, CodeLength, CodeAlphabet and MaxAttempts are account_link.mattermost_code: 8 characters without
	// look-alikes, valid 10 minutes, at most 5 attempts.
	CodeTTL      = 10 * time.Minute
	CodeLength   = 8
	CodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	MaxAttempts  = 5
	// CodeRequestsPerUser and CodeRequestsPerTarget are account_link.mattermost_code_requests: code requests per
	// requesting User and per target account within CodeRequestWindow.
	CodeRequestsPerUser   = 5
	CodeRequestsPerTarget = 3
	CodeRequestWindow     = time.Hour
	// RequestPruneAfter is account_link.request_prune_after: a link request is deleted this long after it expired.
	RequestPruneAfter = 24 * time.Hour
	// TouchInterval is how often a link's last use is written at most.
	TouchInterval = time.Minute
	// tokenBytes is the size of a Telegram deep-link token before it is encoded.
	tokenBytes = 32
	// uniqueViolation and accountKey are the error of an insert whose account another User linked meanwhile: the
	// constraint of one User per messenger account (schema.md).
	uniqueViolation = "23505"
	accountKey      = "account_links_account_key"
)

// The messengers of a link.
const (
	MessengerTelegram   = "telegram"
	MessengerMattermost = "mattermost"
	MessengerBridge     = "bridge"
)

// HintType is the live-update hint of a User whose Account link appeared, changed or disappeared; it names the link.
const HintType = "account-links"

// The Audit log actions and resource of C-18.FR-9.
const (
	ActionLink         = "account_link.link"
	ActionUnlink       = "account_link.unlink"
	ActionLinkRejected = "account_link.link_rejected"
	ResourceLink       = "account_link"
	// resourceConnection is the resource of a rejection: the Connection the link was tried through.
	resourceConnection = "connection"
)

// The reasons of a rejection.
const (
	ReasonTokenUnknown    = "token_unknown"
	ReasonTokenUsed       = "token_used"
	ReasonTokenExpired    = "token_expired"
	ReasonWrongConnection = "wrong_connection"
	ReasonConflict        = "conflict"
	ReasonWrongCode       = "wrong_code"
)

// The reasons of an unlink, as account_link_unlinked logs them; the Audit log entry of an unlink that was asked for
// has none.
const (
	ReasonRemoved     = "removed"
	ReasonReplaced    = "replaced"
	ReasonUserDeleted = "user_deleted"
)

// The codes of the field errors.
const (
	CodeUnknownID             = "unknown_id"
	CodeRequired              = "required"
	CodeMessengerUserNotFound = "messenger_user_not_found"
	CodeLinkCodeInvalid       = "link_code_invalid"
	pointerConnection         = "/connection_id"
	pointerUsername           = "/username"
	pointerCode               = "/code"
)

var (
	// ErrNotFound is an Account link or a link request that does not exist or is not the caller's.
	ErrNotFound = errors.New("no such account link")
	// ErrUserNotFound is a User an Admin names that does not exist.
	ErrUserNotFound = errors.New("no such user")
	// ErrLinkedElsewhere is a messenger account linked to another User (C-18.FR-5).
	ErrLinkedElsewhere = errors.New("the messenger account is linked to another user")
	// ErrMessengerUserNotFound is a username the messenger does not know, or an account that cannot be linked, such
	// as a bot or a deactivated one.
	ErrMessengerUserNotFound = errors.New("the messenger has no such user")
	// ErrNoConnection is a Connection that went away while a link used it.
	ErrNoConnection = errors.New("no such connection")
)

// FieldError is a request field that is not valid; Detail never repeats a token or a code. AttemptsRemaining is set
// on link_code_invalid: the codes the link request still takes, 0 once it takes none.
type FieldError struct {
	Pointer           string
	Code              string
	Detail            string
	AttemptsRemaining *int
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// RateLimitedError refuses a code request beyond account_link.mattermost_code_requests or
// account_link.bridge_code_requests until RetryAfter passed.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("too many link code requests; retry after %s", e.RetryAfter)
}

// Seconds is the wait in whole seconds, rounded up, at least 1, for Retry-After.
func (e *RateLimitedError) Seconds() int {
	return max(int(math.Ceil(e.RetryAfter.Seconds())), 1)
}

// MessengerError is a messenger call of a link that failed: what was called and how it ended, never the answer's
// text, which may repeat the message and so the code.
type MessengerError struct {
	Call    string
	Outcome string
	Status  int
}

func (e *MessengerError) Error() string {
	return fmt.Sprintf("the messenger refused %s (%s, status %d)", e.Call, e.Outcome, e.Status)
}

// noAddress is the client address of a change that comes from a messenger, which has none.
var noAddress netip.Addr

// DBTX is the pool or transaction the queries run in.
type DBTX = dbgen.DBTX

// Pool is the main pool: the queries, and the transactions of a change.
type Pool interface {
	DBTX
	db.Beginner
}

// Queries are the queries of the package, with the insert of the Audit log and the hint of a change.
type Queries interface {
	LookupAccountLink(ctx context.Context, arg dbgen.LookupAccountLinkParams) (dbgen.LookupAccountLinkRow, error)
	TouchAccountLink(ctx context.Context, arg dbgen.TouchAccountLinkParams) error
	GetLinkConnection(ctx context.Context, arg dbgen.GetLinkConnectionParams) (dbgen.GetLinkConnectionRow, error)
	ListLinkableConnections(ctx context.Context, orgID int64) ([]dbgen.ListLinkableConnectionsRow, error)
	LockLinkUser(ctx context.Context, arg dbgen.LockLinkUserParams) (dbgen.LockLinkUserRow, error)
	GetUserByPublicID(ctx context.Context, arg dbgen.GetUserByPublicIDParams) (dbgen.GetUserByPublicIDRow, error)
	ListAccountLinksOfUser(ctx context.Context, arg dbgen.ListAccountLinksOfUserParams) (
		[]dbgen.ListAccountLinksOfUserRow, error)
	GetAccountLinkOfUser(ctx context.Context, arg dbgen.GetAccountLinkOfUserParams) (dbgen.GetAccountLinkOfUserRow,
		error)
	GetAccountLinkBySpace(ctx context.Context, arg dbgen.GetAccountLinkBySpaceParams) (dbgen.GetAccountLinkBySpaceRow,
		error)
	GetUserLinkInSpace(ctx context.Context, arg dbgen.GetUserLinkInSpaceParams) (dbgen.GetUserLinkInSpaceRow, error)
	DeleteAccountLink(ctx context.Context, arg dbgen.DeleteAccountLinkParams) (int64, error)
	InsertAccountLink(ctx context.Context, arg dbgen.InsertAccountLinkParams) (int64, error)
	UpdateAccountLink(ctx context.Context, arg dbgen.UpdateAccountLinkParams) error
	InsertLinkRequest(ctx context.Context, arg dbgen.InsertLinkRequestParams) (int64, error)
	GetTelegramRequestByHash(ctx context.Context, arg dbgen.GetTelegramRequestByHashParams) (
		dbgen.GetTelegramRequestByHashRow, error)
	GetLinkRequest(ctx context.Context, arg dbgen.GetLinkRequestParams) (dbgen.GetLinkRequestRow, error)
	LockLinkRequest(ctx context.Context, arg dbgen.LockLinkRequestParams) (dbgen.LockLinkRequestRow, error)
	ConsumeLinkRequest(ctx context.Context, arg dbgen.ConsumeLinkRequestParams) (int64, error)
	RecordWrongCode(ctx context.Context, arg dbgen.RecordWrongCodeParams) (int64, error)
	VoidPendingMattermostRequests(ctx context.Context, arg dbgen.VoidPendingMattermostRequestsParams) (int64, error)
	GetBridgeTokenRequestByHash(ctx context.Context, arg dbgen.GetBridgeTokenRequestByHashParams) (
		dbgen.GetBridgeTokenRequestByHashRow, error)
	VoidPendingBridgeCodeRequests(ctx context.Context, arg dbgen.VoidPendingBridgeCodeRequestsParams) (int64, error)
	CountUserBridgeCodeRequests(ctx context.Context, arg dbgen.CountUserBridgeCodeRequestsParams) (
		dbgen.CountUserBridgeCodeRequestsRow, error)
	CountTargetBridgeCodeRequests(ctx context.Context, arg dbgen.CountTargetBridgeCodeRequestsParams) (
		dbgen.CountTargetBridgeCodeRequestsRow, error)
	VoidLinkRequest(ctx context.Context, arg dbgen.VoidLinkRequestParams) error
	LockCodeTarget(ctx context.Context, arg dbgen.LockCodeTargetParams) error
	CountUserCodeRequests(ctx context.Context, arg dbgen.CountUserCodeRequestsParams) (
		dbgen.CountUserCodeRequestsRow, error)
	CountTargetCodeRequests(ctx context.Context, arg dbgen.CountTargetCodeRequestsParams) (
		dbgen.CountTargetCodeRequestsRow, error)
	DeleteUserAccountLinks(ctx context.Context, arg dbgen.DeleteUserAccountLinksParams) (
		[]dbgen.DeleteUserAccountLinksRow, error)
	VoidUserLinkRequests(ctx context.Context, arg dbgen.VoidUserLinkRequestsParams) (int64, error)
	audit.Store
	// Notify sends a live-update hint in the transaction of the queries, so that it reaches the streams once that
	// commits.
	Notify(ctx context.Context, h db.Hint) error
	// Savepoint runs f in a savepoint of the transaction of the queries and rolls back to it when f fails, so that the
	// transaction goes on after a statement that failed.
	Savepoint(ctx context.Context, f func(Queries) error) error
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(p Pool) Store {
	return pgStore{pgQueries: queriesOver(p), pool: p}
}

// pgQueries are the queries over a pool or a transaction.
type pgQueries struct {
	*dbgen.Queries
	audit.Store
	tx DBTX
}

func queriesOver(d DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d), tx: d}
}

func (q pgQueries) Notify(ctx context.Context, h db.Hint) error { return db.NotifyHint(ctx, q.tx, h) }

// Savepoint runs f in a savepoint when the queries run in a transaction, or in a transaction of their own over the
// pool; over anything else, f runs as it is.
func (q pgQueries) Savepoint(ctx context.Context, f func(Queries) error) error {
	b, ok := q.tx.(db.Beginner)
	if !ok {
		return f(q)
	}
	return pgx.BeginFunc(ctx, b, func(tx pgx.Tx) error { return f(queriesOver(tx)) })
}

type pgStore struct {
	pgQueries
	pool Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(queriesOver(tx)) })
}

// Config is what the Service needs.
type Config struct {
	OrgID int64
	Store Store
	Audit *audit.Writer
	// Business is the business clock: link times, expiries and the request limits.
	Business clock.Clock
	Log      *logging.Logger
	// Telegram, Mattermost and Bridge are the bots that call the messengers on the interactive path.
	Telegram   TelegramBots
	Mattermost MattermostBots
	Bridge     BridgeBots
	// Random is where tokens and codes come from; nil is crypto/rand.
	Random io.Reader
}

// Service links, unlinks and looks up the Account links of an Organization.
type Service struct {
	cfg Config
}

// New returns the Service of cfg.
func New(cfg Config) *Service {
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{cfg: cfg}
}

// Requester is who asks through the API: the User whose own links change, or the Admin who removes a link; the actor,
// the Transport and the client address of the Audit log entry.
type Requester struct {
	UserID       int64
	UserPublicID string
	Actor        audit.Actor
	Transport    audit.Transport
	Address      netip.Addr
}

// Link is an Account link as the profile lists it (C-18.FR-10). DisplayName is the account's name as a bridge
// reported it, nil for a Telegram or a Mattermost link.
type Link struct {
	PublicID           string
	Messenger          string
	ConnectionPublicID string
	ConnectionName     string
	ExternalID         string
	Username           *string
	DisplayName        *string
	LastUsedAt         *time.Time
	CreatedAt          time.Time
}

// LinkableConnection is a Connection a link can be made through, as the profile sees it: no other field is shown.
// LinkMethods and HandleKind are a Bridge Connection's, from its stored info (C-22.FR-15): the ways a link can be
// made, and the kind of handle a code needs, empty when no code is offered; both are empty for other messengers.
type LinkableConnection struct {
	PublicID    string
	Name        string
	Messenger   string
	LinkMethods []string
	HandleKind  string
}

// Connections lists the Connections that are not deleted, by messenger and name (C-18.FR-10), each Bridge Connection
// with how its accounts can be linked as its stored info says.
func (s *Service) Connections(ctx context.Context) ([]LinkableConnection, error) {
	rows, err := s.cfg.Store.ListLinkableConnections(ctx, s.cfg.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list the connections to link through: %w", err)
	}
	out := make([]LinkableConnection, len(rows))
	for i, r := range rows {
		out[i] = LinkableConnection{PublicID: r.PublicID, Name: r.Name, Messenger: r.Type}
		if r.Type == MessengerBridge {
			o := BridgeOffer{Read: r.InfoRead, UserLookup: r.UserLookup, BotCanStart: r.BotCanStart}
			out[i].LinkMethods, out[i].HandleKind = o.Methods(), o.HandleKind()
		}
	}
	return out, nil
}

// List lists the links of the User userID.
func (s *Service) List(ctx context.Context, userID int64) ([]Link, error) {
	rows, err := s.cfg.Store.ListAccountLinksOfUser(ctx, dbgen.ListAccountLinksOfUserParams{OrgID: s.cfg.OrgID,
		UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list the account links: %w", err)
	}
	out := make([]Link, len(rows))
	for i, r := range rows {
		out[i] = Link{PublicID: r.PublicID, Messenger: r.Messenger, ConnectionPublicID: r.ConnectionPublicID,
			ConnectionName: r.ConnectionName, ExternalID: r.ExternalID, Username: textPtr(r.Username),
			DisplayName: textPtr(r.DisplayName), LastUsedAt: timePtr(r.LastUsedAt), CreatedAt: r.CreatedAt}
	}
	return out, nil
}

// ListOfUser lists the links of the User userPublicID, for an Admin; an unknown User is ErrUserNotFound, and a
// deleted one has none.
func (s *Service) ListOfUser(ctx context.Context, userPublicID string) ([]Link, error) {
	u, err := s.user(ctx, s.cfg.Store, userPublicID)
	if err != nil {
		return nil, err
	}
	return s.List(ctx, u.ID)
}

// user reads the User publicID, deleted ones included.
func (s *Service) user(ctx context.Context, q Queries, publicID string) (dbgen.GetUserByPublicIDRow, error) {
	norm, err := publicid.Parse(publicid.User, publicID)
	if err != nil {
		return dbgen.GetUserByPublicIDRow{}, ErrUserNotFound
	}
	u, err := q.GetUserByPublicID(ctx, dbgen.GetUserByPublicIDParams{OrgID: s.cfg.OrgID, PublicID: norm})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.GetUserByPublicIDRow{}, ErrUserNotFound
	}
	if err != nil {
		return dbgen.GetUserByPublicIDRow{}, fmt.Errorf("read the user: %w", err)
	}
	return u, nil
}

// Unlink removes the link linkPublicID of the requesting User from the web session (C-18.FR-6).
func (s *Service) Unlink(ctx context.Context, r Requester, linkPublicID string) error {
	return s.unlink(ctx, r, r.UserID, r.UserPublicID, linkPublicID)
}

// UnlinkOfUser removes the link linkPublicID of the User userPublicID, for an Admin (C-18.FR-6).
func (s *Service) UnlinkOfUser(ctx context.Context, r Requester, userPublicID, linkPublicID string) error {
	u, err := s.user(ctx, s.cfg.Store, userPublicID)
	if err != nil {
		return err
	}
	return s.unlink(ctx, r, u.ID, u.PublicID, linkPublicID)
}

// unlink deletes the link of the owner with the Audit log entry account_link.unlink, by r, and the hint to the owner.
func (s *Service) unlink(ctx context.Context, r Requester, ownerID int64, ownerPublicID, linkPublicID string) error {
	norm, err := publicid.Parse(publicid.AccountLink, linkPublicID)
	if err != nil {
		return ErrNotFound
	}
	var gone dbgen.GetAccountLinkOfUserRow
	err = s.cfg.Store.InTx(ctx, func(q Queries) error {
		l, err := q.GetAccountLinkOfUser(ctx, dbgen.GetAccountLinkOfUserParams{OrgID: s.cfg.OrgID, UserID: ownerID,
			PublicID: norm})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock the account link: %w", err)
		}
		if _, err := q.DeleteAccountLink(ctx, dbgen.DeleteAccountLinkParams{OrgID: s.cfg.OrgID, ID: l.ID}); err != nil {
			return fmt.Errorf("delete the account link %s: %w", l.PublicID, err)
		}
		v := viewOf(l.Messenger, l.ConnectionPublicID, l.ExternalID, textPtr(l.Username), textPtr(l.DisplayName))
		if err := s.record(ctx, q, r.Actor, r.Transport, r.Address, ActionUnlink, linkResource(l.PublicID, v),
			removed(v), details(v, "", "", ownerPublicID)); err != nil {
			return err
		}
		gone = l
		return q.Notify(ctx, s.hint(ownerPublicID, l.PublicID))
	})
	if err != nil {
		return err
	}
	s.logUnlinked(ctx, gone.Messenger, gone.ConnectionPublicID, ownerPublicID, ReasonRemoved)
	return nil
}

// RemoveUserLinks deletes the links of the User userID in the transaction tx of their deletion (C-03.FR-13), each
// recorded as account_link.unlink with the reason user_deleted by actor, and voids their pending link requests. It
// returns what to log once tx committed.
func (s *Service) RemoveUserLinks(ctx context.Context, tx usersdb.DBTX, userID int64, actor audit.Actor,
	t audit.Transport, addr netip.Addr) (func(context.Context), error) {
	q := queriesOver(tx)
	return s.removeUserLinks(ctx, q, userID, actor, t, addr)
}

func (s *Service) removeUserLinks(ctx context.Context, q Queries, userID int64, actor audit.Actor,
	t audit.Transport, addr netip.Addr) (func(context.Context), error) {
	u, err := q.LockLinkUser(ctx, dbgen.LockLinkUserParams{OrgID: s.cfg.OrgID, ID: userID})
	if err != nil {
		return nil, fmt.Errorf("read the user %d: %w", userID, err)
	}
	rows, err := q.DeleteUserAccountLinks(ctx, dbgen.DeleteUserAccountLinksParams{OrgID: s.cfg.OrgID, UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("delete the account links of %s: %w", u.PublicID, err)
	}
	for _, l := range rows {
		v := viewOf(l.Messenger, l.ConnectionPublicID, l.ExternalID, textPtr(l.Username), textPtr(l.DisplayName))
		if err := s.record(ctx, q, actor, t, addr, ActionUnlink, linkResource(l.PublicID, v), removed(v),
			details(v, ReasonUserDeleted, "", u.PublicID)); err != nil {
			return nil, err
		}
	}
	if _, err := q.VoidUserLinkRequests(ctx, dbgen.VoidUserLinkRequestsParams{Now: s.now(), OrgID: s.cfg.OrgID,
		UserID: userID}); err != nil {
		return nil, fmt.Errorf("void the link requests of %s: %w", u.PublicID, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if err := q.Notify(ctx, s.hint(u.PublicID, "")); err != nil {
		return nil, err
	}
	return func(ctx context.Context) {
		for _, l := range rows {
			s.logUnlinked(ctx, l.Messenger, l.ConnectionPublicID, u.PublicID, ReasonUserDeleted)
		}
	}, nil
}

// account is a messenger account to link through a Connection; displayName is a Bridge account's name.
type account struct {
	messenger   string
	conn        connRef
	space       string
	external    string
	username    *string
	displayName *string
}

// connRef names a Connection.
type connRef struct {
	id       int64
	publicID string
	name     string
}

// link links a to the User u, locked by the caller, in the transaction of q (C-18.FR-4, FR-5): an account linked to
// another User is ErrLinkedElsewhere and changes nothing; an account already linked to u keeps its link, with the
// username and the Connection of this one; otherwise the User's link in the same identity space is replaced, recorded
// as account_link.unlink with the reason replaced. The link is recorded as account_link.link and the hint goes to u.
// The replacement and the new link run in a savepoint: an account that another User linked in a transaction that
// committed after the check fails the insert on account_links_account_key, which rolls back to the savepoint and is
// ErrLinkedElsewhere too, so that the caller can still record the rejection. The links of one User change only under
// the lock of the User, so account_links_user_space_key never fails here; if it did, it would be an error.
func (s *Service) link(ctx context.Context, q Queries, u dbgen.LockLinkUserRow, a account, actor audit.Actor,
	t audit.Transport, addr netip.Addr) (Link, *view, error) {
	other, err := q.GetAccountLinkBySpace(ctx, dbgen.GetAccountLinkBySpaceParams{OrgID: s.cfg.OrgID,
		IdentitySpace: a.space, ExternalID: a.external})
	switch {
	case err == nil && other.UserID != u.ID:
		return Link{}, nil, ErrLinkedElsewhere
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return Link{}, nil, fmt.Errorf("read the link of the account: %w", err)
	}
	same := err == nil
	var replaced *dbgen.GetUserLinkInSpaceRow
	prev, err := q.GetUserLinkInSpace(ctx, dbgen.GetUserLinkInSpaceParams{OrgID: s.cfg.OrgID, UserID: u.ID,
		IdentitySpace: a.space})
	switch {
	case err == nil && same:
		return s.relink(ctx, q, u, a, prev, actor, t, addr)
	case err == nil:
		replaced = &prev
	case !errors.Is(err, pgx.ErrNoRows):
		return Link{}, nil, fmt.Errorf("read the user's account link: %w", err)
	}
	var (
		l   Link
		old *view
	)
	err = q.Savepoint(ctx, func(q Queries) error {
		var err error
		l, old, err = s.insertLink(ctx, q, u, a, replaced, actor, t, addr)
		return err
	})
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok && pe.Code == uniqueViolation && pe.ConstraintName == accountKey {
		return Link{}, nil, ErrLinkedElsewhere
	}
	if err != nil {
		return Link{}, nil, err
	}
	return l, old, nil
}

// insertLink deletes the link prev of the User u, if any, recorded as account_link.unlink with the reason replaced,
// inserts the link of a, recorded as account_link.link, and sends the hint to u.
func (s *Service) insertLink(ctx context.Context, q Queries, u dbgen.LockLinkUserRow, a account,
	prev *dbgen.GetUserLinkInSpaceRow, actor audit.Actor, t audit.Transport, addr netip.Addr) (Link, *view, error) {
	replaced := ""
	var old *view
	if prev != nil {
		if _, err := q.DeleteAccountLink(ctx, dbgen.DeleteAccountLinkParams{OrgID: s.cfg.OrgID, ID: prev.ID}); err != nil {
			return Link{}, nil, fmt.Errorf("delete the replaced account link %s: %w", prev.PublicID, err)
		}
		v := viewOf(prev.Messenger, prev.ConnectionPublicID, prev.ExternalID, textPtr(prev.Username),
			textPtr(prev.DisplayName))
		if err := s.record(ctx, q, actor, t, addr, ActionUnlink, linkResource(prev.PublicID, v), removed(v),
			details(v, ReasonReplaced, "", u.PublicID)); err != nil {
			return Link{}, nil, err
		}
		replaced, old = prev.PublicID, &v
	}
	now := s.now()
	l := Link{PublicID: publicid.New(publicid.AccountLink), Messenger: a.messenger,
		ConnectionPublicID: a.conn.publicID, ConnectionName: a.conn.name, ExternalID: a.external, Username: a.username,
		DisplayName: a.displayName, CreatedAt: now}
	_, err := q.InsertAccountLink(ctx, dbgen.InsertAccountLinkParams{OrgID: s.cfg.OrgID, PublicID: l.PublicID,
		UserID: u.ID, Messenger: a.messenger, ConnectionID: a.conn.id, ExternalID: a.external,
		Username: optText(a.username), DisplayName: optText(a.displayName), CreatedAt: now})
	if err != nil {
		return Link{}, nil, fmt.Errorf("insert the account link: %w", err)
	}
	v := a.view()
	if err := s.record(ctx, q, actor, t, addr, ActionLink, linkResource(l.PublicID, v), audit.Created(v),
		details(v, "", replaced, u.PublicID)); err != nil {
		return Link{}, nil, err
	}
	return l, old, q.Notify(ctx, s.hint(u.PublicID, l.PublicID))
}

// relink keeps the link prev of the User u whose account a is linked again: the username, the display name and the
// Connection become a's, and the link is recorded as account_link.link with what changed, replacing nothing.
func (s *Service) relink(ctx context.Context, q Queries, u dbgen.LockLinkUserRow, a account,
	prev dbgen.GetUserLinkInSpaceRow, actor audit.Actor, t audit.Transport, addr netip.Addr) (Link, *view, error) {
	if err := q.UpdateAccountLink(ctx, dbgen.UpdateAccountLinkParams{Username: optText(a.username),
		DisplayName: optText(a.displayName), ConnectionID: a.conn.id, OrgID: s.cfg.OrgID, ID: prev.ID}); err != nil {
		return Link{}, nil, fmt.Errorf("update the account link %s: %w", prev.PublicID, err)
	}
	before := viewOf(prev.Messenger, prev.ConnectionPublicID, prev.ExternalID, textPtr(prev.Username),
		textPtr(prev.DisplayName))
	v := a.view()
	if err := s.record(ctx, q, actor, t, addr, ActionLink, linkResource(prev.PublicID, v), audit.Diff(before, v),
		details(v, "", "", u.PublicID)); err != nil {
		return Link{}, nil, err
	}
	l := Link{PublicID: prev.PublicID, Messenger: a.messenger, ConnectionPublicID: a.conn.publicID,
		ConnectionName: a.conn.name, ExternalID: a.external, Username: a.username, DisplayName: a.displayName,
		LastUsedAt: timePtr(prev.LastUsedAt), CreatedAt: prev.CreatedAt.UTC()}
	return l, nil, q.Notify(ctx, s.hint(u.PublicID, l.PublicID))
}

// logLinked logs a link that committed, with the link it replaced, and the error of the bot's confirmation, if any.
func (s *Service) logLinked(ctx context.Context, l Link, old *view, userPublicID string, replyErr error) {
	if old != nil {
		s.logUnlinked(ctx, old.Messenger, old.Connection, userPublicID, ReasonReplaced)
	}
	fields := []logging.Field{logging.F("messenger", l.Messenger), logging.F("connection", l.ConnectionPublicID),
		logging.F("user", userPublicID)}
	if replyErr != nil {
		fields = append(fields, logging.F("error", replyErr.Error()))
	}
	s.cfg.Log.Log(ctx, logging.AccountLinkLinked, fields...)
}

// logRejected logs a rejected link, and the error of the bot's answer, if any.
func (s *Service) logRejected(ctx context.Context, messenger, connection, reason string, replyErr error) {
	fields := []logging.Field{logging.F("messenger", messenger), logging.F("connection", connection),
		logging.F("reason", reason)}
	if replyErr != nil {
		fields = append(fields, logging.F("error", replyErr.Error()))
	}
	s.cfg.Log.Log(ctx, logging.AccountLinkRejected, fields...)
}

func (s *Service) logUnlinked(ctx context.Context, messenger, connection, userPublicID, reason string) {
	s.cfg.Log.Log(ctx, logging.AccountLinkUnlinked, logging.F("messenger", messenger),
		logging.F("connection", connection), logging.F("user", userPublicID), logging.F("reason", reason))
}

// reject records the refused link of a as account_link.link_rejected with reason, naming userPublicID when the token
// or the request is known.
func (s *Service) reject(ctx context.Context, q Queries, actor audit.Actor, t audit.Transport, addr netip.Addr,
	a account, reason, userPublicID string) error {
	v := a.view()
	return s.record(ctx, q, actor, t, addr, ActionLinkRejected,
		audit.Resource{Type: resourceConnection, PublicID: a.conn.publicID, Name: a.conn.name}, nil,
		details(v, reason, "", userPublicID))
}

func (s *Service) record(ctx context.Context, q Queries, actor audit.Actor, t audit.Transport, addr netip.Addr,
	action string, res audit.Resource, diff []audit.Change, d map[string]any) error {
	return s.cfg.Audit.Record(ctx, q, audit.Entry{OrgID: s.cfg.OrgID, Actor: actor, Transport: t, Action: action,
		Resource: res, Diff: diff, Details: d, SourceAddress: addr})
}

// hint is the hint to the User userPublicID that the link linkPublicID appeared, changed or disappeared; "" names
// none, when several links changed at once.
func (s *Service) hint(userPublicID, linkPublicID string) db.Hint {
	return db.Hint{OrgID: s.cfg.OrgID, Type: HintType, ID: linkPublicID, User: userPublicID}
}

func (s *Service) now() time.Time { return s.cfg.Business.Now().UTC() }

// view is what the Audit log diff of a link shows; a Bridge link shows its display name too.
type view struct {
	Messenger   string  `json:"messenger,omitempty"`
	Connection  string  `json:"connection,omitempty"`
	ExternalID  string  `json:"external_id,omitempty"`
	Username    *string `json:"username,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
}

func viewOf(messenger, connection, external string, username, displayName *string) view {
	return view{Messenger: messenger, Connection: connection, ExternalID: external, Username: username,
		DisplayName: displayName}
}

// view is the view of the account a.
func (a account) view() view {
	return viewOf(a.messenger, a.conn.publicID, a.external, a.username, a.displayName)
}

// removed is the diff of a link that went: each value with nothing after it.
func removed(v view) []audit.Change {
	out := audit.Diff(v, view{})
	for i := range out {
		out[i].After = nil
	}
	return out
}

// linkResource is the resource of an entry about a link, named by its username or else its external id.
func linkResource(publicID string, v view) audit.Resource {
	name := v.ExternalID
	if v.Username != nil && *v.Username != "" {
		name = *v.Username
	}
	return audit.Resource{Type: ResourceLink, PublicID: publicID, Name: name}
}

// details are the details of an entry (C-18.FR-9): the messenger, the Connection, the account, and the reason, the
// replaced link and the User when there are.
func details(v view, reason, replaced, userPublicID string) map[string]any {
	d := map[string]any{"messenger": v.Messenger, "connection": v.Connection, "external_id": v.ExternalID}
	if v.Username != nil {
		d["username"] = *v.Username
	}
	if v.DisplayName != nil {
		d["display_name"] = *v.DisplayName
	}
	for k, val := range map[string]string{"reason": reason, "replaced": replaced, "user": userPublicID} {
		if val != "" {
			d[k] = val
		}
	}
	return d
}

// language is the language of the bot's messages to the User: their profile's, else English.
func language(l pgtype.Text) string {
	if l.Valid && l.String == messages.LanguageRussian {
		return messages.LanguageRussian
	}
	return messages.LanguageEnglish
}

// hash is the SHA-256 of a token or a code, as it is stored.
func hash(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func optText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// Pruner deletes the link requests that expired RequestPruneAfter ago, for the short_lived_pruning Leader task.
type Pruner struct {
	q interface {
		PruneLinkRequests(ctx context.Context, arg dbgen.PruneLinkRequestsParams) (int64, error)
	}
}

// NewPruner returns the Pruner over d.
func NewPruner(d DBTX) Pruner {
	return Pruner{q: dbgen.New(d)}
}

// Requests deletes at most limit link requests of the Organization that expired RequestPruneAfter before now,
// consumed and voided ones included.
func (p Pruner) Requests(ctx context.Context, orgID int64, now time.Time, limit int32) (int64, error) {
	n, err := p.q.PruneLinkRequests(ctx, dbgen.PruneLinkRequestsParams{OrgID: orgID, Before: now.Add(-RequestPruneAfter),
		BatchSize: limit})
	if err != nil {
		return 0, fmt.Errorf("delete expired account link requests: %w", err)
	}
	return n, nil
}
