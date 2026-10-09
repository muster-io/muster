// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package connections holds the Connections of C-13.FR-1, FR-6 and C-14.FR-1: a messenger server or bot that
// Destinations post through. A Mattermost Connection has a name, the server URL, a bot token (encrypted, write-only), a
// proxy and its limiter; a Telegram Connection has a name, a bot token, the Bot API base URL, a proxy, its limiter and
// its update mode, long polling or webhook. The package checks a Connection — the token of a Mattermost one, the steps
// with the dry probe of a Telegram one — lists the channels a Mattermost bot sees, and runs the Destination check of a
// Mattermost channel for the Destination write path, each on the interactive path (C-11.FR-2). It sets and deletes the
// webhook of a Telegram Connection when its update mode changes, and serves the Telegram Connections to the Leader's
// poller and to the webhook endpoint. A Connection used by a Destination that is not deleted cannot be deleted;
// deleting one wipes its secrets and abandons the final edits still pending for its deleted Destinations
// (C-11.FR-14). Every change is recorded in the Audit log and announced with the live hint connection.
package connections

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/connections/dbgen"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/destinations"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/mattermost"
	"github.com/muster-io/muster/internal/proxyconf"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/telegram"
)

// The Connection types.
const (
	TypeMattermost = "mattermost"
	TypeTelegram   = "telegram"
)

// The Audit log actions and resource type, and the live hint, of Connections (C-03.FR-14).
const (
	ActionCreated      = "connection.created"
	ActionUpdated      = "connection.updated"
	ActionDeleted      = "connection.deleted"
	ResourceConnection = "connection"
	Hint               = "connection"
)

// CallbackPath is where Mattermost calls Muster for the button presses of a Connection, under MUSTER_INGEST_URL, with
// the Connection's public_id after it (C-13.FR-13).
const CallbackPath = mattermost.CallbackPath

// The default limiter of a Mattermost Connection (connection.mattermost.limiter): 5 requests per second, half of the
// 10 that Mattermost's rate limit allows per client address when an admin turns it on (F-030).
const (
	DefaultLimit      = 5
	DefaultPerSeconds = 1
)

// The default limiter of a Telegram Connection (connection.telegram.limiter): 15 messages per second (P-28).
const (
	TelegramDefaultLimit      = 15
	TelegramDefaultPerSeconds = 1
)

// The update modes of a Telegram Connection (C-14.FR-1); connection.telegram.update_mode is long polling.
const (
	ModeLongPolling = "long_polling"
	ModeWebhook     = "webhook"
)

// The secret fields of a Connection, as the Keyring binds them.
const (
	fieldBotToken      = "connections.bot_token"
	fieldProxyPassword = "connections.proxy_password"
	fieldWebhookSecret = "connections.telegram_webhook_secret"
)

// The steps of the Connection check.
const StepToken = "token"

const (
	// maxNameLength bounds the name of a Connection, in characters.
	maxNameLength = 200
	// uniqueViolation is the SQLSTATE of a unique index that refused a row, and nameIndex the index of the names of
	// the Connections that are not deleted.
	uniqueViolation = "23505"
	nameIndex       = "connections_name_key"
	// demoLock is the advisory lock of the demo start-up step.
	demoLock = 0x6d75737465720039
)

var (
	// ErrNotFound is a Connection that does not exist in the Organization or is deleted.
	ErrNotFound = errors.New("no such connection")
	// ErrNameTaken is a name another Connection that is not deleted has.
	ErrNameTaken = errors.New("another connection has this name")
	// ErrVersionMismatch is an If-Match that names another version of the Connection.
	ErrVersionMismatch = errors.New("the connection changed since it was read")
	// ErrInUse is the deletion of a Connection that a Destination that is not deleted uses (C-13.FR-6).
	ErrInUse = errors.New("destinations use the connection")
	// ErrNotMattermost is a Mattermost operation, such as the channel list, on a Telegram Connection.
	ErrNotMattermost = errors.New("the connection is not a mattermost connection")
	// errNotTelegram is a Telegram operation on a Mattermost Connection.
	errNotTelegram = errors.New("the connection is not a telegram connection")
)

// MessengerError is a call to the messenger that failed for a reason other than its limiter, such as a channel list
// that the server refused; its text is masked of secrets and untrusted.
type MessengerError struct {
	Text string
}

func (e *MessengerError) Error() string { return "the messenger refused the call: " + e.Text }

// FieldError is a field of a request that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string { return e.Pointer + ": " + e.Detail }

// The codes of FieldError.
const (
	CodeRequired      = "required"
	CodeInvalidFormat = "invalid_format"
	CodeTooLong       = "too_long"
	CodeTooShort      = "too_short"
	CodeUnsupported   = "unsupported"
	// CodeWebhookCallFailed is a save of a Telegram Connection whose setWebhook or deleteWebhook failed; nothing was
	// saved.
	CodeWebhookCallFailed = "webhook_call_failed"
)

// Limiter is a rate limit of Limit calls per PerSeconds.
type Limiter struct {
	Limit      int64 `json:"limit"`
	PerSeconds int64 `json:"per_seconds"`
}

// Connection is a Connection as the API reads it; its secrets show only their status. ServerURL is a Mattermost
// Connection's; BotAPIBaseURL, UpdateMode and Warnings are a Telegram Connection's.
type Connection struct {
	ID               int64
	PublicID         string
	Type             string
	Name             string
	ServerURL        string
	BotAPIBaseURL    string
	UpdateMode       string
	Warnings         []string
	BotToken         keyring.SecretStatus
	BotUsername      *string
	BotUserID        *string
	Proxy            proxyconf.Config
	ProxyPassword    keyring.SecretStatus
	Limiter          Limiter
	DestinationCount int64
	CreatedAt        time.Time
	Version          int64
}

// Input is what createConnection and updateConnection write. An omitted bot token keeps the stored one on an update and
// is required on a creation. ServerURL is a Mattermost Connection's; BotAPIBaseURL and UpdateMode a Telegram one's.
type Input struct {
	Type          string
	Name          string
	ServerURL     string
	BotAPIBaseURL string
	UpdateMode    string
	BotToken      keyring.SecretInput
	Proxy         proxyconf.Input
	Limiter       Limiter
}

// ListFilter selects a page of Connections, of a type when set, in the order they were created, after the id After.
type ListFilter struct {
	Type  *string
	After *int64
	Limit int
}

// Page is a page of Connections; Next, the id to continue after, is nil on the last page.
type Page struct {
	Connections []Connection
	Next        *int64
}

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// Queries are the queries of the package, with the insert of the Audit log and the live hint.
type Queries interface {
	ListConnections(ctx context.Context, arg dbgen.ListConnectionsParams) ([]dbgen.ListConnectionsRow, error)
	GetConnection(ctx context.Context, arg dbgen.GetConnectionParams) (dbgen.GetConnectionRow, error)
	LockConnection(ctx context.Context, arg dbgen.LockConnectionParams) (dbgen.LockConnectionRow, error)
	FindConnectionByName(ctx context.Context, arg dbgen.FindConnectionByNameParams) (int64, error)
	InsertConnection(ctx context.Context, arg dbgen.InsertConnectionParams) (int64, error)
	UpdateConnection(ctx context.Context, arg dbgen.UpdateConnectionParams) error
	SetBotIdentity(ctx context.Context, arg dbgen.SetBotIdentityParams) error
	CountConnectionDestinations(ctx context.Context, arg dbgen.CountConnectionDestinationsParams) (int64, error)
	MarkConnectionDeleted(ctx context.Context, arg dbgen.MarkConnectionDeletedParams) error
	ListPollingConnections(ctx context.Context, orgID int64) ([]dbgen.ListPollingConnectionsRow, error)
	LockUpdateOffset(ctx context.Context, arg dbgen.LockUpdateOffsetParams) (pgtype.Int8, error)
	StoreUpdateOffset(ctx context.Context, arg dbgen.StoreUpdateOffsetParams) error
	LockDemo(ctx context.Context, key int64) error
	GetDestinationTarget(ctx context.Context, arg dbgen.GetDestinationTargetParams) (dbgen.GetDestinationTargetRow,
		error)
	audit.Store
	Notify(ctx context.Context, h db.Hint) error
	// DB is the pool or transaction the queries run in, which the Abandon hook writes through.
	DB() dbgen.DBTX
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: newQueries(pool), pool: pool}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
	exec dbgen.DBTX
}

func newQueries(d dbgen.DBTX) pgQueries {
	return pgQueries{Queries: dbgen.New(d), Store: audit.NewStore(d), exec: d}
}

func (q pgQueries) Notify(ctx context.Context, h db.Hint) error { return db.NotifyHint(ctx, q.exec, h) }

func (q pgQueries) DB() dbgen.DBTX { return q.exec }

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return f(newQueries(tx)) })
}

// Interactive is the interactive path, declared by its consumer; *delivery.Interactive implements it.
type Interactive interface {
	Do(ctx context.Context, s delivery.Subject, op delivery.Op) (delivery.Outcome, error)
}

// Abandon is delivery's hook that the deletion of the Connection connectionID runs in its transaction tx: the final
// edits still pending for its deleted Destinations end as Not delivered. The returned function runs once tx committed.
type Abandon func(ctx context.Context, tx dbgen.DBTX, connectionID int64) (func(context.Context), error)

// Config is what a Service needs.
type Config struct {
	OrgID   int64
	Store   Store
	Keyring *keyring.Keyring
	Audit   *audit.Writer
	// Clocks: the business clock dates the rows, the real clock measures the latency of a check.
	Clocks clock.Clocks
	// Network is what the Mattermost and Telegram clients need: the outbound address policy, the logger and a resolver.
	Network mattermost.Network
	// Interactive is the interactive path, which the checks and the channel list take their limiter tokens from.
	Interactive Interactive
	// IngestURL is MUSTER_INGEST_URL, the base of the callback address and of the webhook of a Telegram Connection.
	IngestURL *url.URL
	Abandon   Abandon
	Log       *logging.Logger
}

// Service holds the Connections of the Organization, and the clients the adapter posts through, built once per
// version of their Connection.
type Service struct {
	cfg Config

	mu        sync.Mutex
	clients   map[int64]cachedClient
	tgClients map[int64]cachedTelegram
}

// cachedClient is the client of a version of a Connection.
type cachedClient struct {
	version int64
	client  *mattermost.Client
}

// New returns the Service of the Organization in cfg.
func New(cfg Config) *Service {
	return &Service{cfg: cfg, clients: map[int64]cachedClient{}, tgClients: map[int64]cachedTelegram{}}
}

// CallbackURL is the read-only callback address of the Connection publicID: MUSTER_INGEST_URL followed by
// /api/v1/callbacks/mattermost/<public_id> (C-13.FR-13).
func (s *Service) CallbackURL(publicID string) string {
	base := ""
	if s.cfg.IngestURL != nil {
		base = strings.TrimSuffix(s.cfg.IngestURL.String(), "/")
	}
	return base + CallbackPath + publicID
}

// List reads a page of the Connections that are not deleted, in id order.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	limit := min(max(f.Limit, 1), 1000)
	rows, err := s.cfg.Store.ListConnections(ctx, dbgen.ListConnectionsParams{OrgID: s.cfg.OrgID,
		Type: text(f.Type), AfterID: nullInt(f.After), PageSize: int32(limit) + 1})
	if err != nil {
		return Page{}, fmt.Errorf("list the connections: %w", err)
	}
	page := Page{Connections: make([]Connection, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			last := page.Connections[limit-1].ID
			page.Next = &last
			break
		}
		c, err := connectionOf(dbgen.GetConnectionRow(r))
		if err != nil {
			return Page{}, err
		}
		page.Connections = append(page.Connections, c)
	}
	return page, nil
}

// Get reads the Connection publicID; a deleted or unknown one is ErrNotFound.
func (s *Service) Get(ctx context.Context, publicID string) (Connection, error) {
	row, err := s.row(ctx, s.cfg.Store, publicID)
	if err != nil {
		return Connection{}, err
	}
	return connectionOf(row)
}

// row reads the Connection publicID with its secrets as stored.
func (s *Service) row(ctx context.Context, q Queries, publicID string) (dbgen.GetConnectionRow, error) {
	id, err := publicid.Parse(publicid.Connection, publicID)
	if err != nil {
		return dbgen.GetConnectionRow{}, ErrNotFound
	}
	r, err := q.GetConnection(ctx, dbgen.GetConnectionParams{OrgID: s.cfg.OrgID,
		PublicID: pgtype.Text{String: id, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.GetConnectionRow{}, ErrNotFound
	}
	if err != nil {
		return dbgen.GetConnectionRow{}, fmt.Errorf("read the connection %s: %w", id, err)
	}
	return r, nil
}

// view is what the Audit log diff of a Connection shows; the secrets only as changed or not.
type view struct {
	Name          string           `json:"name"`
	ServerURL     string           `json:"server_url,omitempty"`
	BotAPIBaseURL string           `json:"bot_api_base_url,omitempty"`
	UpdateMode    string           `json:"update_mode,omitempty"`
	Proxy         proxyconf.Config `json:"proxy"`
	Limiter       Limiter          `json:"limiter"`
}

func viewOf(c Connection) view {
	return view{Name: c.Name, ServerURL: c.ServerURL, BotAPIBaseURL: c.BotAPIBaseURL, UpdateMode: c.UpdateMode,
		Proxy: c.Proxy, Limiter: c.Limiter}
}

func resourceOf(c Connection) audit.Resource {
	return audit.Resource{Type: ResourceConnection, PublicID: c.PublicID, Name: c.Name}
}

// check validates the fields of in other than its secrets and its proxy, and normalizes them; stored is nil on a
// creation.
func check(in *Input, stored *Connection) error {
	switch {
	case in.Type != TypeMattermost && in.Type != TypeTelegram:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The type is not mattermost or telegram."}
	case stored != nil && stored.Type != in.Type:
		return &FieldError{Pointer: "/type", Code: CodeInvalidFormat, Detail: "The type of a Connection cannot change."}
	}
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return &FieldError{Pointer: "/name", Code: CodeRequired, Detail: "The name is empty."}
	case utf8.RuneCountInString(in.Name) > maxNameLength:
		return &FieldError{Pointer: "/name", Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", maxNameLength)}
	}
	if err := checkAddress(in); err != nil {
		return err
	}
	if in.Limiter.Limit < 1 || in.Limiter.PerSeconds < 1 {
		return &FieldError{Pointer: "/limiter", Code: CodeInvalidFormat,
			Detail: "The limiter needs a limit and a period of at least 1."}
	}
	if stored == nil && !in.BotToken.Given {
		return &FieldError{Pointer: "/bot_token", Code: CodeRequired, Detail: "A new Connection needs its bot token."}
	}
	if in.BotToken.Null {
		return &FieldError{Pointer: "/bot_token", Code: CodeRequired, Detail: "The bot token cannot be cleared."}
	}
	// The stored token is sent only to the server it was entered for.
	switch {
	case stored != nil && in.Type == TypeMattermost && in.ServerURL != stored.ServerURL && !in.BotToken.Given:
		return &FieldError{Pointer: "/bot_token", Code: CodeRequired,
			Detail: "A new server URL needs the bot token again."}
	case stored != nil && in.Type == TypeTelegram && in.BotAPIBaseURL != stored.BotAPIBaseURL && !in.BotToken.Given:
		return &FieldError{Pointer: "/bot_token", Code: CodeRequired,
			Detail: "A new Bot API base URL needs the bot token again."}
	}
	return nil
}

// checkAddress validates and normalizes where the Connection of in calls: the server URL of a Mattermost Connection;
// the Bot API base URL (C-14.FR-10), stored without its trailing slashes, and the update mode of a Telegram one.
func checkAddress(in *Input) error {
	if in.Type == TypeTelegram {
		in.ServerURL = ""
		base, err := telegram.ParseBaseURL(in.BotAPIBaseURL)
		if err != nil {
			return &FieldError{Pointer: "/bot_api_base_url", Code: CodeInvalidFormat,
				Detail: "The Bot API base URL is not an absolute http or https URL without user information, query or " +
					"fragment."}
		}
		in.BotAPIBaseURL = base
		if in.UpdateMode != ModeLongPolling && in.UpdateMode != ModeWebhook {
			return &FieldError{Pointer: "/update_mode", Code: CodeInvalidFormat,
				Detail: "The update mode is not long_polling or webhook."}
		}
		return nil
	}
	in.BotAPIBaseURL, in.UpdateMode = "", ""
	in.ServerURL = strings.TrimSpace(in.ServerURL)
	u, err := url.Parse(in.ServerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || strings.Contains(in.ServerURL, "#") {
		return &FieldError{Pointer: "/server_url", Code: CodeInvalidFormat,
			Detail: "The server URL is not an absolute http or https URL without user information, query or fragment."}
	}
	return nil
}

// secrets applies the bot token and the proxy password of in to their stored values at now; it returns the fields to
// store and the Audit log changes of the secrets.
func (s *Service) secrets(in Input, token, password keyring.StoredSecret, now time.Time) (keyring.StoredSecret,
	keyring.StoredSecret, []audit.Change, error) {
	var diff []audit.Change
	token, changed, err := s.cfg.Keyring.ApplySecret(fieldBotToken, token, in.BotToken, now)
	if errors.Is(err, keyring.ErrEmptySecret) {
		return token, password, nil, &FieldError{Pointer: "/bot_token", Code: CodeTooShort,
			Detail: "The bot token is empty."}
	}
	if err != nil {
		return token, password, nil, err
	}
	if changed {
		diff = append(diff, audit.Change{Pointer: "/bot_token", SecretChanged: true})
	}
	password, changed, err = s.cfg.Keyring.ApplySecret(fieldProxyPassword, password, in.Proxy.Password, now)
	if errors.Is(err, keyring.ErrEmptySecret) {
		return token, password, nil, &FieldError{Pointer: "/proxy/password", Code: CodeTooShort,
			Detail: "The proxy password is empty; send null to clear it."}
	}
	if err != nil {
		return token, password, nil, err
	}
	if changed {
		diff = append(diff, audit.Change{Pointer: "/proxy/password", SecretChanged: true})
	}
	return token, password, diff, nil
}

// proxy applies the proxy of in to the stored one.
func proxy(in proxyconf.Input, stored proxyconf.Config) (proxyconf.Config, error) {
	p, err := in.Apply(stored)
	if fe, ok := errors.AsType[*proxyconf.FieldError](err); ok {
		return p, &FieldError{Pointer: "/proxy" + fe.Pointer, Code: fe.Code, Detail: fe.Detail}
	}
	return p, err
}

// Create creates a Mattermost Connection (C-13.FR-1) or a Telegram one (C-14.FR-1). The bot token is encrypted with
// the active key and never returned. A Telegram Connection created in the webhook mode gets a new secret token and its
// webhook is set before the creation commits; when setWebhook fails, nothing is created.
func (s *Service) Create(ctx context.Context, r Requester, in Input) (Connection, error) {
	if err := check(&in, nil); err != nil {
		return Connection{}, err
	}
	p, err := proxy(in.Proxy, proxyconf.Config{})
	if err != nil {
		return Connection{}, err
	}
	var created Connection
	var hooked string
	err = s.cfg.Store.InTx(ctx, func(q Queries) error {
		var err error
		var secret logging.Secret
		created, secret, err = s.insert(ctx, q, in, p)
		if err != nil {
			return err
		}
		if secret != "" {
			if hooked, err = s.setWebhook(ctx, q, created.PublicID, secret); err != nil {
				return err
			}
		}
		diff := append(audit.Created(viewOf(created)), audit.Change{Pointer: "/bot_token", SecretChanged: true})
		if created.ProxyPassword.Set {
			diff = append(diff, audit.Change{Pointer: "/proxy/password", SecretChanged: true})
		}
		return s.record(ctx, q, r, ActionCreated, created, diff)
	})
	if err != nil {
		return Connection{}, err
	}
	s.webhookSet(ctx, created.PublicID, hooked)
	return created, nil
}

// insert stores a new Connection; in the webhook mode it returns the new secret token, which setWebhook registers.
func (s *Service) insert(ctx context.Context, q Queries, in Input, p proxyconf.Config) (Connection, logging.Secret,
	error) {
	now := s.cfg.Clocks.Business.Now().UTC()
	token, password, _, err := s.secrets(in, keyring.StoredSecret{}, keyring.StoredSecret{}, now)
	if err != nil {
		return Connection{}, "", err
	}
	hook, secret, err := s.webhookSecret(in, nil, keyring.StoredSecret{}, now)
	if err != nil {
		return Connection{}, "", err
	}
	id := publicid.New(publicid.Connection)
	if _, err := q.InsertConnection(ctx, dbgen.InsertConnectionParams{OrgID: s.cfg.OrgID, PublicID: id, Type: in.Type,
		Name: in.Name, ServerUrl: nonEmpty(in.ServerURL), BotApiBaseUrl: nonEmpty(in.BotAPIBaseURL),
		UpdateMode: nonEmpty(in.UpdateMode), WebhookSecretCiphertext: hook.Ciphertext,
		WebhookSecretKeyID: nonEmpty(hook.KeyID), WebhookSecretUpdatedAt: timestamp(hook.UpdatedAt),
		BotTokenCiphertext: token.Ciphertext, BotTokenKeyID: nonEmpty(token.KeyID), BotTokenUpdatedAt: now,
		Proxy: p.JSON(), ProxyPasswordCiphertext: password.Ciphertext, ProxyPasswordKeyID: nonEmpty(password.KeyID),
		ProxyPasswordUpdatedAt: timestamp(password.UpdatedAt), LimiterLimit: in.Limiter.Limit,
		LimiterPerSeconds: in.Limiter.PerSeconds, Now: now}); err != nil {
		return Connection{}, "", fmt.Errorf("create the connection: %w", nameTaken(err))
	}
	row, err := s.row(ctx, q, id)
	if err != nil {
		return Connection{}, "", err
	}
	c, err := connectionOf(row)
	return c, secret, err
}

// Update replaces the configured fields of the Connection publicID; a non-nil version must be its current one
// (If-Match). An omitted bot token or proxy password keeps the stored one, except that a new server URL or Bot API base
// URL needs the bot token again, so that the stored one never reaches another server; a new address or bot token
// forgets the bot that the last check found. An update that changes nothing writes nothing. A Telegram Connection that
// enters the webhook mode, or changes its base URL or bot token in it, gets a new secret token and setWebhook; one that
// leaves it gets deleteWebhook; both run before the update commits, and when they fail nothing is saved.
func (s *Service) Update(ctx context.Context, r Requester, publicID string, version *int64, in Input) (Connection,
	error) {
	var out Connection
	var hooked string
	err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		id, err := publicid.Parse(publicid.Connection, publicID)
		if err != nil {
			return ErrNotFound
		}
		lock, err := q.LockConnection(ctx, dbgen.LockConnectionParams{OrgID: s.cfg.OrgID, PublicID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock the connection %s: %w", id, err)
		}
		if version != nil && *version != lock.Version {
			return ErrVersionMismatch
		}
		row, err := s.row(ctx, q, id)
		if err != nil {
			return err
		}
		before, err := connectionOf(row)
		if err != nil {
			return err
		}
		if err := check(&in, &before); err != nil {
			return err
		}
		p, err := proxy(in.Proxy, before.Proxy)
		if err != nil {
			return err
		}
		now := s.cfg.Clocks.Business.Now().UTC()
		token, password, secretDiff, err := s.secrets(in, storedToken(row), storedPassword(row), now)
		if err != nil {
			return err
		}
		hook, secret, err := s.webhookSecret(in, &before, storedHook(row), now)
		if err != nil {
			return err
		}
		after := before
		after.Name, after.ServerURL, after.BotAPIBaseURL, after.UpdateMode = in.Name, in.ServerURL, in.BotAPIBaseURL,
			in.UpdateMode
		after.Proxy, after.Limiter = p, in.Limiter
		diff := append(audit.Diff(viewOf(before), viewOf(after)), secretDiff...)
		if len(diff) == 0 {
			out = before
			return nil
		}
		tokenAt := row.BotTokenUpdatedAt
		if token.UpdatedAt != nil {
			tokenAt = *token.UpdatedAt
		}
		// The webhook that is left is the one of the saved bot and base URL, so it goes before the update.
		if before.UpdateMode == ModeWebhook && in.UpdateMode == ModeLongPolling {
			if err := s.deleteWebhook(ctx, row); err != nil {
				return err
			}
		}
		if err := q.UpdateConnection(ctx, dbgen.UpdateConnectionParams{OrgID: s.cfg.OrgID, ID: row.ID, Name: in.Name,
			ServerUrl: nonEmpty(in.ServerURL), BotApiBaseUrl: nonEmpty(in.BotAPIBaseURL),
			UpdateMode: nonEmpty(in.UpdateMode), WebhookSecretCiphertext: hook.Ciphertext,
			WebhookSecretKeyID: nonEmpty(hook.KeyID), WebhookSecretUpdatedAt: timestamp(hook.UpdatedAt),
			BotTokenCiphertext: token.Ciphertext, BotTokenKeyID: nonEmpty(token.KeyID), BotTokenUpdatedAt: tokenAt,
			ForgetBot: in.ServerURL != before.ServerURL || in.BotAPIBaseURL != before.BotAPIBaseURL ||
				in.BotToken.Given,
			ForgetUpdates: in.BotToken.Given,
			Proxy:         p.JSON(), ProxyPasswordCiphertext: password.Ciphertext,
			ProxyPasswordKeyID: nonEmpty(password.KeyID), ProxyPasswordUpdatedAt: timestamp(password.UpdatedAt),
			LimiterLimit: in.Limiter.Limit, LimiterPerSeconds: in.Limiter.PerSeconds, Now: now}); err != nil {
			return fmt.Errorf("update the connection %s: %w", id, nameTaken(err))
		}
		if row, err = s.row(ctx, q, id); err != nil {
			return err
		}
		if out, err = connectionOf(row); err != nil {
			return err
		}
		if secret != "" {
			if hooked, err = s.setWebhook(ctx, q, out.PublicID, secret); err != nil {
				return err
			}
		}
		return s.record(ctx, q, r, ActionUpdated, out, diff)
	})
	if err != nil {
		return Connection{}, err
	}
	s.webhookSet(ctx, out.PublicID, hooked)
	return out, nil
}

// Delete deletes the Connection publicID (C-13.FR-6); a non-nil version must be its current one. While a Destination
// that is not deleted uses it the deletion is ErrInUse. Otherwise, in one transaction, it is marked deleted and its
// secrets are wiped, delivery abandons the final edits still pending for its deleted Destinations (C-11.FR-14), and
// the Audit log entry connection.deleted is written.
func (s *Service) Delete(ctx context.Context, r Requester, publicID string, version *int64) error {
	id, err := publicid.Parse(publicid.Connection, publicID)
	if err != nil {
		return ErrNotFound
	}
	var done func(context.Context)
	err = s.cfg.Store.InTx(ctx, func(q Queries) error {
		lock, err := q.LockConnection(ctx, dbgen.LockConnectionParams{OrgID: s.cfg.OrgID, PublicID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock the connection %s: %w", id, err)
		}
		if version != nil && *version != lock.Version {
			return ErrVersionMismatch
		}
		n, err := q.CountConnectionDestinations(ctx, dbgen.CountConnectionDestinationsParams{OrgID: s.cfg.OrgID,
			ConnectionID: pgtype.Int8{Int64: lock.ID, Valid: true}})
		if err != nil {
			return fmt.Errorf("count the destinations of the connection %s: %w", id, err)
		}
		if n > 0 {
			return ErrInUse
		}
		row, err := s.row(ctx, q, id)
		if err != nil {
			return err
		}
		c, err := connectionOf(row)
		if err != nil {
			return err
		}
		now := s.cfg.Clocks.Business.Now().UTC()
		if err := q.MarkConnectionDeleted(ctx, dbgen.MarkConnectionDeletedParams{OrgID: s.cfg.OrgID, ID: lock.ID,
			Now: now}); err != nil {
			return fmt.Errorf("delete the connection %s: %w", id, err)
		}
		if s.cfg.Abandon != nil {
			if done, err = s.cfg.Abandon(ctx, q.DB(), lock.ID); err != nil {
				return fmt.Errorf("abandon the final edits of the connection %s: %w", id, err)
			}
		}
		return s.record(ctx, q, r, ActionDeleted, c, []audit.Change{{Pointer: "/deleted_at", After: now}})
	})
	if err != nil {
		return err
	}
	if done != nil {
		done(ctx)
	}
	return nil
}

// record writes the Audit log entry of a change of c and its live hint.
func (s *Service) record(ctx context.Context, q Queries, r Requester, action string, c Connection,
	diff []audit.Change) error {
	if err := s.cfg.Audit.Record(ctx, q, audit.Entry{OrgID: s.cfg.OrgID, Actor: r.Actor, Transport: r.Transport,
		Action: action, Resource: resourceOf(c), Diff: diff, SourceAddress: r.Address}); err != nil {
		return err
	}
	return q.Notify(ctx, db.Hint{OrgID: s.cfg.OrgID, Type: Hint, ID: c.PublicID})
}

// client builds the Mattermost client of the stored Connection row, with its decrypted bot token and proxy password.
func (s *Service) client(row dbgen.GetConnectionRow) (*mattermost.Client, error) {
	if row.Type != TypeMattermost {
		return nil, ErrNotMattermost
	}
	token, err := s.cfg.Keyring.OpenSecret(fieldBotToken, storedToken(row))
	if err != nil {
		return nil, fmt.Errorf("open the bot token of the connection %s: %w", row.PublicID, err)
	}
	password, err := s.cfg.Keyring.OpenSecret(fieldProxyPassword, storedPassword(row))
	if err != nil {
		return nil, fmt.Errorf("open the proxy password of the connection %s: %w", row.PublicID, err)
	}
	p, err := proxyconf.Parse(row.Proxy)
	if err != nil {
		return nil, err
	}
	return mattermost.NewClient(s.cfg.Network, mattermost.Settings{ServerURL: row.MattermostServerUrl.String,
		Token: token, Proxy: p.Outbound(password)})
}

// Target is where the Mattermost Destination destinationID posts, for the adapter: its team and channel and the client
// of its Connection, which is kept, with its outbound transports, until the Connection changes. A deleted Destination
// still has one, for its final edit. A Destination that is unknown, not a Mattermost one, or whose Connection is deleted
// is mattermost.ErrNoTarget.
func (s *Service) Target(ctx context.Context, destinationID int64) (mattermost.Target, error) {
	r, err := s.cfg.Store.GetDestinationTarget(ctx, dbgen.GetDestinationTargetParams{OrgID: s.cfg.OrgID,
		DestinationID: destinationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return mattermost.Target{}, mattermost.ErrNoTarget
	}
	if err != nil {
		return mattermost.Target{}, fmt.Errorf("read the connection of the destination %d: %w", destinationID, err)
	}
	c, err := s.cachedClient(dbgen.GetConnectionRow{ID: r.ID, PublicID: r.PublicID, Type: r.Type, Name: r.Name,
		MattermostServerUrl: r.MattermostServerUrl, BotTokenCiphertext: r.BotTokenCiphertext,
		BotTokenKeyID: r.BotTokenKeyID, BotTokenUpdatedAt: r.BotTokenUpdatedAt, Proxy: r.Proxy,
		ProxyPasswordCiphertext: r.ProxyPasswordCiphertext, ProxyPasswordKeyID: r.ProxyPasswordKeyID,
		ProxyPasswordUpdatedAt: r.ProxyPasswordUpdatedAt, Version: r.Version})
	if errors.Is(err, ErrNotMattermost) {
		return mattermost.Target{}, mattermost.ErrNoTarget
	}
	if err != nil {
		return mattermost.Target{}, err
	}
	return mattermost.Target{Client: c, ConnectionID: r.ID, ConnectionPublicID: r.PublicID,
		TeamID: r.MattermostTeamID.String, TeamName: r.MattermostTeamName.String,
		ChannelID: r.MattermostChannelID.String}, nil
}

// Connection is the Mattermost Connection publicID with its client, for the callback of its button presses; a
// Connection that is unknown, deleted or not a Mattermost one is mattermost.ErrNoConnection.
func (s *Service) Connection(ctx context.Context, publicID string) (mattermost.Connection, error) {
	r, err := s.row(ctx, s.cfg.Store, publicID)
	if errors.Is(err, ErrNotFound) {
		return mattermost.Connection{}, mattermost.ErrNoConnection
	}
	if err != nil {
		return mattermost.Connection{}, err
	}
	c, err := s.cachedClient(r)
	if errors.Is(err, ErrNotMattermost) {
		return mattermost.Connection{}, mattermost.ErrNoConnection
	}
	if err != nil {
		return mattermost.Connection{}, err
	}
	return mattermost.Connection{ID: r.ID, PublicID: r.PublicID, Client: c}, nil
}

// cachedClient is the client of the Connection of r, built again when the Connection's version changed.
func (s *Service) cachedClient(r dbgen.GetConnectionRow) (*mattermost.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cc, ok := s.clients[r.ID]; ok && cc.version == r.Version {
		return cc.client, nil
	}
	c, err := s.client(r)
	if err != nil {
		return nil, err
	}
	s.clients[r.ID] = cachedClient{version: r.Version, client: c}
	return c, nil
}

// Step is a step of a Connection check: whether it passed or was skipped, its latency and path, and when it failed
// what answered.
type Step struct {
	Name    string
	OK      bool
	Skipped bool
	Latency time.Duration
	Via     string
	Message string
}

// CheckResult is the result of a Connection check: its steps and the bot's username once the token works; for a
// Telegram Connection, once getWebhookInfo answered, whether a webhook is set and how many updates wait.
type CheckResult struct {
	OK      bool
	Steps   []Step
	BotName string
	// Warnings are hints that do not fail the check, such as WarningPressAnswersInThread.
	Warnings       []string
	WebhookSet     *bool
	PendingUpdates *int64
}

// WarningPressAnswersInThread is the warning of a Connection whose bot may not make ephemeral messages: answers to
// button presses then show in the Thread of the Root message, not in the channel view (D284, F-063).
const WarningPressAnswersInThread = "press_answers_in_thread"

// Check runs the Connection check of the Connection publicID on the interactive path, limited by the Connection. A
// Telegram Connection is checked in steps by checkTelegram, against the unsaved baseURL when it is set. A Mattermost
// one (C-13.FR-2), which takes no baseURL, is checked with GET /api/v4/users/me with the bot token. A token that works
// records the bot's username and user id on the Connection; a 401 fails with "The bot token is not valid.". No limiter
// token within the budget is a *delivery.LimitedError. Once the token works, a read of the bot's roles tells whether
// it may make ephemeral posts; when it may not, the check passes with WarningPressAnswersInThread (D284, F-064); a
// failed read adds nothing.
func (s *Service) Check(ctx context.Context, publicID string, baseURL *string) (CheckResult, error) {
	row, err := s.row(ctx, s.cfg.Store, publicID)
	if err != nil {
		return CheckResult{}, err
	}
	if row.Type == TypeTelegram {
		return s.checkTelegram(ctx, row, baseURL)
	}
	if baseURL != nil {
		return CheckResult{}, &FieldError{Pointer: "/base_url", Code: CodeUnsupported,
			Detail: "Only a Telegram Connection takes an unsaved base URL."}
	}
	c, err := s.client(row)
	if err != nil {
		return CheckResult{}, err
	}
	var user mattermost.User
	var r mattermost.Result
	var latency time.Duration
	id := row.ID
	if _, err := s.cfg.Interactive.Do(ctx, delivery.Subject{Connection: &id}, delivery.ReadOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			start := s.cfg.Clocks.Real.Now()
			user, r = c.Me(ctx, call.Class)
			latency = s.cfg.Clocks.Real.Now().Sub(start)
			return r.Outcome
		})); err != nil {
		return CheckResult{}, err
	}
	step := Step{Name: StepToken, OK: r.OK(), Latency: latency, Via: c.Via()}
	if !r.OK() {
		step.Message = string(r.Outcome.Error)
		if r.Status == http.StatusUnauthorized {
			step.Message = mattermost.MessageTokenInvalid
		}
		return CheckResult{Steps: []Step{step}}, nil
	}
	if err := s.cfg.Store.InTx(ctx, func(q Queries) error {
		if err := q.SetBotIdentity(ctx, dbgen.SetBotIdentityParams{OrgID: s.cfg.OrgID, ID: row.ID,
			BotUserID: nonEmpty(user.ID), BotUsername: nonEmpty(user.Username)}); err != nil {
			return fmt.Errorf("record the bot of the connection %s: %w", row.PublicID, err)
		}
		return q.Notify(ctx, db.Hint{OrgID: s.cfg.OrgID, Type: Hint, ID: row.PublicID})
	}); err != nil {
		return CheckResult{}, err
	}
	res := CheckResult{OK: true, Steps: []Step{step}, BotName: user.Username}
	var granted bool
	if _, err := s.cfg.Interactive.Do(ctx, delivery.Subject{Connection: &id}, delivery.ReadOp(
		func(ctx context.Context, call delivery.Call) delivery.Outcome {
			granted, r = c.Grants(ctx, call.Class, user, mattermost.PermissionEphemeralPosts)
			return r.Outcome
		})); err == nil && r.OK() && !granted {
		res.Warnings = []string{WarningPressAnswersInThread}
	}
	return res, nil
}

// Channel is a channel the bot of a Mattermost Connection is a member of, with its team.
type Channel struct {
	mattermost.Channel
	TeamName string
}

// Channels lists the channels the bot of the Mattermost Connection publicID belongs to, team by team, through the
// interactive path: the bot's teams, the team teamID only when it is set, then the bot's channels of each, those whose
// name or display name contains q when it is set, case-insensitively. A Telegram Connection is ErrNotMattermost; a
// refused call a *MessengerError.
func (s *Service) Channels(ctx context.Context, publicID, teamID, q string) ([]Channel, error) {
	row, err := s.row(ctx, s.cfg.Store, publicID)
	if err != nil {
		return nil, err
	}
	c, err := s.client(row)
	if err != nil {
		return nil, err
	}
	id := row.ID
	run := mattermost.Interactive(s.cfg.Interactive, delivery.Subject{Connection: &id})
	var teams []mattermost.Team
	if err := read(ctx, run, func(ctx context.Context, call delivery.Call) mattermost.Result {
		var r mattermost.Result
		teams, r = c.Teams(ctx, call.Class)
		return r
	}); err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := []Channel{}
	for _, t := range teams {
		if teamID != "" && t.ID != teamID {
			continue
		}
		var chans []mattermost.Channel
		if err := read(ctx, run, func(ctx context.Context, call delivery.Call) mattermost.Result {
			var r mattermost.Result
			chans, r = c.Channels(ctx, call.Class, t.ID)
			return r
		}); err != nil {
			return nil, err
		}
		for _, ch := range chans {
			if q != "" && !strings.Contains(strings.ToLower(ch.Name), q) &&
				!strings.Contains(strings.ToLower(ch.DisplayName), q) {
				continue
			}
			out = append(out, Channel{Channel: ch, TeamName: t.Name})
		}
	}
	slices.SortStableFunc(out, func(a, b Channel) int {
		return strings.Compare(a.TeamName+"\x00"+a.Name, b.TeamName+"\x00"+b.Name)
	})
	return out, nil
}

// read makes one read of the channel list through run; a refused call is a *MessengerError.
func read(ctx context.Context, run mattermost.Runner,
	f func(ctx context.Context, c delivery.Call) mattermost.Result) error {
	var r mattermost.Result
	if _, err := run(ctx, func(ctx context.Context, c delivery.Call) delivery.Outcome {
		r = f(ctx, c)
		return r.Outcome
	}); err != nil {
		return err
	}
	if !r.OK() {
		return &MessengerError{Text: string(r.Outcome.Error)}
	}
	return nil
}

// CheckChannel runs the Destination check of a Mattermost Destination being saved or checked, for the Destination
// write path (C-13.FR-2, FR-10): through the Mattermost Connection that ChannelCheck names, on the interactive path,
// limited by the Destination when it exists and by the Connection otherwise. A Connection that is missing, deleted or
// not a Mattermost one is destinations.ErrUnknownConnection.
func (s *Service) CheckChannel(ctx context.Context, in destinations.ChannelCheck) (destinations.ChannelChecked, error) {
	row, err := s.row(ctx, s.cfg.Store, in.Connection)
	if errors.Is(err, ErrNotFound) || (err == nil && row.Type != TypeMattermost) {
		return destinations.ChannelChecked{}, destinations.ErrUnknownConnection
	}
	if err != nil {
		return destinations.ChannelChecked{}, err
	}
	c, err := s.client(row)
	if err != nil {
		return destinations.ChannelChecked{}, err
	}
	conn := row.ID
	subject := delivery.Subject{Connection: &conn}
	if in.Destination != nil {
		subject = delivery.Subject{Destination: &delivery.Destination{ID: *in.Destination, Type: TypeMattermost,
			Connection: &conn}}
	}
	res, err := mattermost.CheckDestination(ctx, c, mattermost.Interactive(s.cfg.Interactive, subject), in.TeamID,
		in.ChannelID)
	if err != nil {
		return destinations.ChannelChecked{}, err
	}
	return destinations.ChannelChecked{ConnectionID: row.ID, Check: res}, nil
}

// Demo is a demo Connection of `muster dev`: a Mattermost Connection to the fake server, or, with the type telegram, a
// Telegram one to the fake Bot API in the long-polling mode.
type Demo struct {
	Type      string
	Name      string
	ServerURL string
	BaseURL   string
	BotToken  logging.Secret
}

// EnsureDemo creates the demo Connection d unless a Connection that is not deleted has its name, as a start-up step of
// `muster dev` that replicas starting together run once.
func (s *Service) EnsureDemo(ctx context.Context, d Demo) error {
	by := Requester{Actor: audit.System, Transport: audit.TransportSystem}
	return s.cfg.Store.InTx(ctx, func(q Queries) error {
		if err := q.LockDemo(ctx, demoLock); err != nil {
			return fmt.Errorf("lock the demo connection: %w", err)
		}
		_, err := q.FindConnectionByName(ctx, dbgen.FindConnectionByNameParams{OrgID: s.cfg.OrgID, Name: d.Name})
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("find the demo connection: %w", err)
		}
		in := Input{Type: TypeMattermost, Name: d.Name, ServerURL: d.ServerURL, BotToken: keyring.Replace(d.BotToken),
			Limiter: Limiter{Limit: DefaultLimit, PerSeconds: DefaultPerSeconds}}
		if d.Type == TypeTelegram {
			in = Input{Type: TypeTelegram, Name: d.Name, BotAPIBaseURL: d.BaseURL, UpdateMode: ModeLongPolling,
				BotToken: keyring.Replace(d.BotToken),
				Limiter:  Limiter{Limit: TelegramDefaultLimit, PerSeconds: TelegramDefaultPerSeconds}}
		}
		if err := check(&in, nil); err != nil {
			return err
		}
		c, _, err := s.insert(ctx, q, in, proxyconf.Config{})
		if err != nil {
			return err
		}
		diff := append(audit.Created(viewOf(c)), audit.Change{Pointer: "/bot_token", SecretChanged: true})
		if err := s.cfg.Audit.Record(ctx, q, audit.Entry{OrgID: s.cfg.OrgID, Actor: by.Actor, Transport: by.Transport,
			Action: ActionCreated, Resource: resourceOf(c), Diff: diff,
			Details: map[string]any{"development_demo": true}}); err != nil {
			return err
		}
		return q.Notify(ctx, db.Hint{OrgID: s.cfg.OrgID, Type: Hint, ID: c.PublicID})
	})
}

// connectionOf is the Connection of a row.
func connectionOf(r dbgen.GetConnectionRow) (Connection, error) {
	p, err := proxyconf.Parse(r.Proxy)
	if err != nil {
		return Connection{}, err
	}
	var warnings []string
	if r.Type == TypeTelegram {
		warnings = telegram.BaseURLWarnings(r.TelegramBotApiBaseUrl.String)
	}
	return Connection{ID: r.ID, PublicID: r.PublicID, Type: r.Type, Name: r.Name,
		ServerURL: r.MattermostServerUrl.String, BotAPIBaseURL: r.TelegramBotApiBaseUrl.String,
		UpdateMode: r.TelegramUpdateMode.String, Warnings: warnings,
		BotToken: storedToken(r).Status(), BotUsername: textOf(r.BotUsername),
		BotUserID: textOf(r.BotUserID), Proxy: p, ProxyPassword: storedPassword(r).Status(),
		Limiter:          Limiter{Limit: r.LimiterLimit, PerSeconds: r.LimiterPerSeconds},
		DestinationCount: r.DestinationCount, CreatedAt: r.CreatedAt.UTC(), Version: r.Version}, nil
}

func storedToken(r dbgen.GetConnectionRow) keyring.StoredSecret {
	at := r.BotTokenUpdatedAt.UTC()
	return keyring.StoredSecret{Ciphertext: r.BotTokenCiphertext, KeyID: r.BotTokenKeyID.String, UpdatedAt: &at}
}

func storedHook(r dbgen.GetConnectionRow) keyring.StoredSecret {
	s := keyring.StoredSecret{Ciphertext: r.TelegramWebhookSecretCiphertext, KeyID: r.TelegramWebhookSecretKeyID.String}
	if r.TelegramWebhookSecretUpdatedAt.Valid {
		at := r.TelegramWebhookSecretUpdatedAt.Time.UTC()
		s.UpdatedAt = &at
	}
	return s
}

func storedPassword(r dbgen.GetConnectionRow) keyring.StoredSecret {
	s := keyring.StoredSecret{Ciphertext: r.ProxyPasswordCiphertext, KeyID: r.ProxyPasswordKeyID.String}
	if r.ProxyPasswordUpdatedAt.Valid {
		at := r.ProxyPasswordUpdatedAt.Time.UTC()
		s.UpdatedAt = &at
	}
	return s
}

// nameTaken turns the refusal of the unique index of the names into ErrNameTaken.
func nameTaken(err error) error {
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok && pe.Code == uniqueViolation && pe.ConstraintName == nameIndex {
		return ErrNameTaken
	}
	return err
}

func timestamp(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func nonEmpty(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func textOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func text(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func nullInt(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}
