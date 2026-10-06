// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package tokens holds the API tokens of C-04: Personal access tokens, which act as their User with Permissions
// narrowed to the ones chosen for them, and Service accounts with a Role and their tokens. A token is `mstr_pat_` or
// `mstr_sat_` followed by 32 random bytes; only its SHA-256 is stored and the value is shown once (ADR-0011). The
// package authenticates a bearer token for the API, with the OIDC rules of a Personal access token (C-04.FR-8), and
// limits the requests of each token (api.rate_limit). Every issue, revocation and change of a Service account is
// recorded in the Audit log.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/tokens/dbgen"
)

// The prefixes of the tokens Muster issues (C-04.FR-3): the kind is readable before any lookup, and secret scanners
// find a leaked token by it. Integration tokens (C-05) are never accepted by the API.
const (
	PrefixPersonal       = "mstr_pat_"
	PrefixServiceAccount = "mstr_sat_"
	PrefixIntegration    = "mstr_int_"
)

// The kinds of api_tokens.
const (
	KindPersonal       = "personal"
	KindServiceAccount = "service_account"
)

const (
	// randomBytes is the entropy of a token.
	randomBytes = 32
	// touchInterval is how often at most last_used_at and last_used_address are written.
	touchInterval = time.Minute
	// maxNameLength bounds the name of a token or a Service account, in characters.
	maxNameLength = 200
	// uniqueViolation is the SQLSTATE of a unique index that refused a row, and nameIndex the index of the names of
	// the Service accounts.
	uniqueViolation = "23505"
	nameIndex       = "service_accounts_name_key"
)

// encoding writes the random part of a token in lowercase base32 without padding: 52 characters that a double click
// selects whole and that need no escaping anywhere.
var encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// valueLength is the length of a token after its prefix.
var valueLength = encoding.EncodedLen(randomBytes)

var (
	// ErrInvalidToken is a token that is malformed, unknown, revoked or expired, an Integration token, or a token
	// whose owner is disabled or deleted; it never says which.
	ErrInvalidToken = errors.New("the token is not valid")
	// ErrOIDCRecheckRequired is a Personal access token of an account that signs in through OIDC while the identity
	// provider refused the owner, or past auth.oidc_token_grace without an offline token (C-04.FR-8).
	ErrOIDCRecheckRequired = errors.New("sign in through OIDC to make your tokens work again")
	// ErrNotFound is a token or a Service account that does not exist in the Organization.
	ErrNotFound = errors.New("no such token or service account")
	// ErrNameTaken is a Service account name that another Service account has, compared lowercased.
	ErrNameTaken = errors.New("another service account has this name")
	// ErrVersionMismatch is an If-Match that names another version of the Service account.
	ErrVersionMismatch = errors.New("the service account changed since it was read")
)

// FieldError is a field of a request that is not valid, at a JSON pointer of the request body, with a stable code of
// the validation-failed problem.
type FieldError struct {
	Pointer string
	Code    string
	Detail  string
}

func (e *FieldError) Error() string {
	return e.Pointer + ": " + e.Detail
}

// The codes of FieldError.
const (
	CodePermissionNotHeld = "permission_not_held"
	CodeOutOfRange        = "out_of_range"
	CodeTooLong           = "too_long"
	CodeInvalidFormat     = "invalid_format"
)

// Generate returns a new token of the kind prefix names and the SHA-256 that is stored in its place.
func Generate(prefix string) (string, []byte, error) {
	b := make([]byte, randomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generate a token: %w", err)
	}
	value := prefix + encoding.EncodeToString(b)
	return value, Hash(value), nil
}

// Hash is the SHA-256 of the whole token, prefix included, as api_tokens.token_hash stores it.
func Hash(value string) []byte {
	h := sha256.Sum256([]byte(value))
	return h[:]
}

// kindOf is the kind of an API token by its prefix, when value has the shape of one; an Integration token, a token
// of another shape and anything else is not one.
func kindOf(value string) (string, bool) {
	var kind, rest string
	switch {
	case strings.HasPrefix(value, PrefixPersonal):
		kind, rest = KindPersonal, value[len(PrefixPersonal):]
	case strings.HasPrefix(value, PrefixServiceAccount):
		kind, rest = KindServiceAccount, value[len(PrefixServiceAccount):]
	default:
		return "", false
	}
	if len(rest) != valueLength {
		return "", false
	}
	if _, err := encoding.DecodeString(rest); err != nil {
		return "", false
	}
	return kind, true
}

// Requester is who asks for a change and how: the actor and the Transport of the Audit log entry, and the client
// address.
type Requester struct {
	Actor     audit.Actor
	Transport audit.Transport
	Address   netip.Addr
}

// Queries are the queries of the package, with the insert of the Audit log.
type Queries interface {
	InsertToken(ctx context.Context, arg dbgen.InsertTokenParams) (int64, error)
	InsertTokenPermissions(ctx context.Context, arg dbgen.InsertTokenPermissionsParams) error
	ListUserTokens(ctx context.Context, arg dbgen.ListUserTokensParams) ([]dbgen.ListUserTokensRow, error)
	ListServiceAccountTokens(ctx context.Context, arg dbgen.ListServiceAccountTokensParams) (
		[]dbgen.ListServiceAccountTokensRow, error)
	RevokeUserToken(ctx context.Context, arg dbgen.RevokeUserTokenParams) (dbgen.RevokeUserTokenRow, error)
	RevokeServiceAccountToken(ctx context.Context, arg dbgen.RevokeServiceAccountTokenParams) (
		dbgen.RevokeServiceAccountTokenRow, error)
	RevokeServiceAccountTokens(ctx context.Context, arg dbgen.RevokeServiceAccountTokensParams) (int64, error)
	GetTokenByHash(ctx context.Context, arg dbgen.GetTokenByHashParams) (dbgen.GetTokenByHashRow, error)
	TouchToken(ctx context.Context, arg dbgen.TouchTokenParams) error
	ListServiceAccounts(ctx context.Context, arg dbgen.ListServiceAccountsParams) ([]dbgen.ListServiceAccountsRow, error)
	GetServiceAccount(ctx context.Context, arg dbgen.GetServiceAccountParams) (dbgen.GetServiceAccountRow, error)
	LockServiceAccount(ctx context.Context, arg dbgen.LockServiceAccountParams) (int64, error)
	InsertServiceAccount(ctx context.Context, arg dbgen.InsertServiceAccountParams) (int64, error)
	UpdateServiceAccount(ctx context.Context, arg dbgen.UpdateServiceAccountParams) error
	SetServiceAccountStatus(ctx context.Context, arg dbgen.SetServiceAccountStatusParams) error
	DeleteServiceAccount(ctx context.Context, arg dbgen.DeleteServiceAccountParams) error
	audit.Store
}

// Store runs the queries alone or in one transaction.
type Store interface {
	Queries
	InTx(ctx context.Context, f func(Queries) error) error
}

// NewStore is the Store over the main pool.
func NewStore(pool *pgxpool.Pool) Store {
	return pgStore{pgQueries: pgQueries{Queries: dbgen.New(pool), Store: audit.NewStore(pool)}, pool: pool}
}

type pgQueries struct {
	*dbgen.Queries
	audit.Store
}

type pgStore struct {
	pgQueries
	pool *pgxpool.Pool
}

func (s pgStore) InTx(ctx context.Context, f func(Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return f(pgQueries{Queries: dbgen.New(tx), Store: audit.NewStore(tx)})
	})
}

// Service holds the API tokens and Service accounts of the Organization.
type Service struct {
	orgID   int64
	store   Store
	audit   *audit.Writer
	clock   clock.Clock
	roles   auth.Roles
	limiter *Limiter
}

// New returns the Service of the Organization orgID; business is the business clock, which dates tokens, their
// expiry and their use, and limiter limits the requests of each token.
func New(orgID int64, s Store, w *audit.Writer, business clock.Clock, roles auth.Roles, limiter *Limiter) *Service {
	return &Service{orgID: orgID, store: s, audit: w, clock: business, roles: roles, limiter: limiter}
}

// checkName refuses an empty name or one longer than maxNameLength characters, at pointer.
func checkName(pointer, name string) error {
	if strings.TrimSpace(name) == "" {
		return &FieldError{Pointer: pointer, Code: CodeInvalidFormat, Detail: "The name is empty."}
	}
	if utf8.RuneCountInString(name) > maxNameLength {
		return &FieldError{Pointer: pointer, Code: CodeTooLong,
			Detail: fmt.Sprintf("The name is longer than %d characters.", maxNameLength)}
	}
	return nil
}

// checkExpiry refuses an expiry that is not in the future.
func checkExpiry(expires *time.Time, now time.Time) error {
	if expires != nil && !expires.After(now) {
		return &FieldError{Pointer: "/expires_at", Code: CodeOutOfRange, Detail: "The expiry is not in the future."}
	}
	return nil
}

func timestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func timeOf(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func address(a netip.Addr) *netip.Addr {
	if !a.IsValid() {
		return nil
	}
	return &a
}

func addressOf(a *netip.Addr) netip.Addr {
	if a == nil {
		return netip.Addr{}
	}
	return *a
}
