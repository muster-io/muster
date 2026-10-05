// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package auth signs people in and keeps their sessions (C-03): local sign-in with argon2id passwords and its
// throttling, sessions in PostgreSQL carried in the muster_session cookie, the CSRF token derived from the session,
// the password change, the client address behind trusted proxies, and the Permissions of the Roles. Every session
// time follows the business clock.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/publicid"
)

// CookieName is the session cookie (HttpOnly; Secure; SameSite=Lax; Path=/).
const CookieName = "muster_session"

const (
	// SessionIdleTimeout is auth.session_idle_timeout and SessionLifetime auth.session_lifetime.
	SessionIdleTimeout = 12 * time.Hour
	SessionLifetime    = 7 * 24 * time.Hour
	// touchInterval is how often at most last_used_at is written.
	touchInterval = time.Minute
	tokenBytes    = 32
	// maxUserAgent bounds the user agent kept with a session.
	maxUserAgent = 512
)

// SessionState is the state of a session; a limited session can only finish the second factor or sign out.
type SessionState string

const (
	StateActive                SessionState = "active"
	StateTOTPRequired          SessionState = "totp_required"
	StateTOTPEnrolmentRequired SessionState = "totp_enrolment_required"
)

// The sign-in methods.
const (
	MethodLocal = "local"
	MethodOIDC  = "oidc"
)

// The reasons a session ends, as sessions.end_reason stores them.
const (
	EndSignOut           = "sign_out"
	EndSignOutEverywhere = "sign_out_everywhere"
	EndPasswordChanged   = "password_changed"
	EndExpired           = "expired"
)

var (
	// ErrInvalidCredentials is a wrong login or password, or an account that cannot sign in; it never says which.
	ErrInvalidCredentials = errors.New("the login or the password is wrong")
	// ErrUnauthenticated is a request without a usable session.
	ErrUnauthenticated = errors.New("no valid session")
	// ErrSessionExpired is a session that ended by idle timeout or lifetime.
	ErrSessionExpired = errors.New("the session expired")
	// ErrNotLocal is a password change of an account that signs in through OIDC.
	ErrNotLocal = errors.New("the account signs in through OIDC and has no password")
)

// ThrottledError refuses a sign-in attempt that comes before the throttle allows the next one; it is not evaluated.
type ThrottledError struct {
	RetryAfter time.Duration
}

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("too many failed sign-ins: retry after %d s", e.Seconds())
}

// Seconds is RetryAfter rounded up to whole seconds, at least one.
func (e *ThrottledError) Seconds() int {
	return max(1, int((e.RetryAfter+time.Second-1)/time.Second))
}

// Principal is the user behind a session.
type Principal struct {
	ID       int64
	PublicID string
	Name     string
	Role     string
}

// Session is a session of a user. The cookie value is held only in memory, to derive the CSRF token.
type Session struct {
	ID            int64
	PublicID      string
	State         SessionState
	Method        string
	CreatedAt     time.Time
	LastUsedAt    time.Time
	IdleExpiresAt time.Time
	ExpiresAt     time.Time
	User          Principal
	token         []byte
}

// Cookie is the value of the session cookie.
func (s Session) Cookie() string {
	return base64.RawURLEncoding.EncodeToString(s.token)
}

// SessionInfo is a session as the profile lists it.
type SessionInfo struct {
	PublicID   string
	Method     string
	Address    netip.Addr
	UserAgent  string
	CreatedAt  time.Time
	LastUsedAt time.Time
	Current    bool
}

// Queries are the queries of the package, with the insert of the Audit log.
type Queries interface {
	ListRolePermissions(ctx context.Context) ([]dbgen.RolePermission, error)
	GetSignInUser(ctx context.Context, arg dbgen.GetSignInUserParams) (dbgen.GetSignInUserRow, error)
	GetUserPassword(ctx context.Context, arg dbgen.GetUserPasswordParams) (dbgen.GetUserPasswordRow, error)
	MarkSignedIn(ctx context.Context, arg dbgen.MarkSignedInParams) error
	SetPassword(ctx context.Context, arg dbgen.SetPasswordParams) (int64, error)
	CreateSession(ctx context.Context, arg dbgen.CreateSessionParams) (int64, error)
	GetSessionByToken(ctx context.Context, arg dbgen.GetSessionByTokenParams) (dbgen.GetSessionByTokenRow, error)
	TouchSession(ctx context.Context, arg dbgen.TouchSessionParams) error
	EndSession(ctx context.Context, arg dbgen.EndSessionParams) (int64, error)
	EndUserSessions(ctx context.Context, arg dbgen.EndUserSessionsParams) (int64, error)
	EndOtherUserSessions(ctx context.Context, arg dbgen.EndOtherUserSessionsParams) (int64, error)
	ListUserSessions(ctx context.Context, arg dbgen.ListUserSessionsParams) ([]dbgen.ListUserSessionsRow, error)
	GetThrottles(ctx context.Context, arg dbgen.GetThrottlesParams) ([]dbgen.GetThrottlesRow, error)
	RecordSignInFailure(ctx context.Context, arg dbgen.RecordSignInFailureParams) (int64, error)
	BlockSignIn(ctx context.Context, arg dbgen.BlockSignInParams) error
	ResetSignInThrottles(ctx context.Context, arg dbgen.ResetSignInThrottlesParams) error
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

// Service signs people in and keeps their sessions for the Organization.
type Service struct {
	orgID   int64
	store   Store
	keyring *keyring.Keyring
	audit   *audit.Writer
	clock   clock.Clock
	roles   Roles
}

// NewService returns the Service of the Organization orgID; clock is the business clock.
func NewService(orgID int64, s Store, k *keyring.Keyring, w *audit.Writer, business clock.Clock, roles Roles) *Service {
	// The series of muster_login_failures_total exist from the start, so that an increase is seen from zero.
	for _, method := range []string{MethodLocal, MethodOIDC, "totp"} {
		metrics.LoginFailures.With(method)
	}
	prepareDummy()
	return &Service{orgID: orgID, store: s, keyring: k, audit: w, clock: business, roles: roles}
}

// Roles is the allocation of Permissions to Roles.
func (s *Service) Roles() Roles {
	return s.roles
}

// Permissions are the Permissions a session grants: its user's Role's, none while it is limited.
func (s *Service) Permissions(sess Session) []Permission {
	if sess.State != StateActive {
		return []Permission{}
	}
	return s.roles.Permissions(sess.User.Role)
}

// SignInRequest is a local sign-in attempt.
type SignInRequest struct {
	Login     string
	Password  string
	Address   netip.Addr
	UserAgent string
}

// SignIn checks a login and a password and opens a session. The login is compared lowercased. A wrong login or
// password, a disabled or deleted account and an account without a password are all ErrInvalidCredentials, and an
// unknown login spends the time of a password check too. An attempt before the throttle allows one is a
// *ThrottledError and is not evaluated.
func (s *Service) SignIn(ctx context.Context, req SignInRequest) (Session, error) {
	account := accountSubject(req.Login)
	address := addressSubject(req.Address)
	if wait, err := s.throttled(ctx, account, address); err != nil {
		return Session{}, err
	} else if wait > 0 {
		return Session{}, &ThrottledError{RetryAfter: wait}
	}
	user, err := s.store.GetSignInUser(ctx, dbgen.GetSignInUserParams{OrgID: s.orgID, Login: req.Login})
	known := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Session{}, fmt.Errorf("find the account: %w", err)
	}
	ok := false
	if known && user.Status == "active" && user.PasswordHash.Valid {
		if ok, err = VerifyPassword(ctx, user.PasswordHash.String, req.Password); err != nil {
			return Session{}, fmt.Errorf("check the password of %s: %w", user.PublicID, err)
		}
	} else if err := verifyDummy(ctx, req.Password); err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, s.failSignIn(ctx, req, account, address, user, known)
	}
	return s.openSession(ctx, req, account, address, user)
}

func (s *Service) failSignIn(ctx context.Context, req SignInRequest, account, address string,
	user dbgen.GetSignInUserRow, known bool) error {
	if err := s.recordFailure(ctx, account, address); err != nil {
		return err
	}
	metrics.LoginFailures.With(MethodLocal).Inc()
	e := audit.Entry{
		OrgID: s.orgID, Actor: audit.System, Transport: audit.TransportUI, Action: audit.ActionSignInFailed,
		Details: map[string]any{"method": MethodLocal}, SourceAddress: req.Address,
	}
	if known {
		e.Resource = audit.Resource{Type: audit.ResourceUser, PublicID: user.PublicID, Name: user.Name}
	}
	if err := s.audit.Record(ctx, s.store, e); err != nil {
		return err
	}
	return ErrInvalidCredentials
}

func (s *Service) openSession(ctx context.Context, req SignInRequest, account, address string,
	user dbgen.GetSignInUserRow) (Session, error) {
	token := make([]byte, tokenBytes)
	if _, err := rand.Read(token); err != nil {
		return Session{}, fmt.Errorf("make a session token: %w", err)
	}
	now := s.clock.Now().UTC()
	sess := Session{
		PublicID: publicid.New(publicid.Session), State: StateActive, Method: MethodLocal, CreatedAt: now,
		LastUsedAt: now, IdleExpiresAt: now.Add(SessionIdleTimeout), ExpiresAt: now.Add(SessionLifetime),
		User: Principal{ID: user.ID, PublicID: user.PublicID, Name: user.Name, Role: user.Role}, token: token,
	}
	hash := sha256.Sum256(token)
	p := dbgen.CreateSessionParams{
		OrgID: s.orgID, PublicID: sess.PublicID, UserID: user.ID, TokenHash: hash[:], State: string(sess.State),
		Method: sess.Method, UserAgent: optText(truncate(req.UserAgent, maxUserAgent)), CreatedAt: now,
		IdleExpiresAt: sess.IdleExpiresAt, ExpiresAt: sess.ExpiresAt,
	}
	if req.Address.IsValid() {
		addr := req.Address
		p.Address = &addr
	}
	err := s.store.InTx(ctx, func(q Queries) error {
		if err := q.ResetSignInThrottles(ctx, dbgen.ResetSignInThrottlesParams{
			OrgID: s.orgID, Account: account, Address: address,
		}); err != nil {
			return fmt.Errorf("reset the sign-in throttle: %w", err)
		}
		id, err := q.CreateSession(ctx, p)
		if err != nil {
			return fmt.Errorf("create the session: %w", err)
		}
		sess.ID = id
		if err := q.MarkSignedIn(ctx, dbgen.MarkSignedInParams{OrgID: s.orgID, ID: user.ID, Now: now}); err != nil {
			return fmt.Errorf("record the sign-in: %w", err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: audit.User(user.ID, user.PublicID), Transport: audit.TransportUI,
			Action:   audit.ActionSignedIn,
			Resource: audit.Resource{Type: audit.ResourceSession, PublicID: sess.PublicID, Name: user.Name},
			Details:  map[string]any{"method": MethodLocal}, SourceAddress: req.Address,
		})
	})
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

// Authenticate returns the session of a cookie value. A value that names no session, an ended session or a user who
// is no longer active is ErrUnauthenticated; a session past its idle timeout or lifetime ends with the reason
// expired and is ErrSessionExpired. A session in use moves its idle expiry, written at most once a minute.
func (s *Service) Authenticate(ctx context.Context, cookie string) (Session, error) {
	token, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil || len(token) != tokenBytes {
		return Session{}, ErrUnauthenticated
	}
	hash := sha256.Sum256(token)
	row, err := s.store.GetSessionByToken(ctx, dbgen.GetSessionByTokenParams{OrgID: s.orgID, TokenHash: hash[:]})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrUnauthenticated
	}
	if err != nil {
		return Session{}, fmt.Errorf("find the session: %w", err)
	}
	if row.EndedAt.Valid {
		if row.EndReason.String == EndExpired {
			return Session{}, ErrSessionExpired
		}
		return Session{}, ErrUnauthenticated
	}
	if row.UserStatus != "active" {
		return Session{}, ErrUnauthenticated
	}
	now := s.clock.Now().UTC()
	if !now.Before(row.IdleExpiresAt) || !now.Before(row.ExpiresAt) {
		if _, err := s.store.EndSession(ctx, dbgen.EndSessionParams{
			OrgID: s.orgID, ID: row.ID, Now: now, EndReason: optText(EndExpired),
		}); err != nil {
			return Session{}, fmt.Errorf("end the expired session %s: %w", row.PublicID, err)
		}
		return Session{}, ErrSessionExpired
	}
	sess := Session{
		ID: row.ID, PublicID: row.PublicID, State: SessionState(row.State), Method: row.Method,
		CreatedAt: row.CreatedAt, LastUsedAt: row.LastUsedAt, IdleExpiresAt: row.IdleExpiresAt,
		ExpiresAt: row.ExpiresAt, token: token,
		User: Principal{ID: row.UserID, PublicID: row.UserPublicID, Name: row.UserName, Role: row.UserRole},
	}
	if now.Sub(row.LastUsedAt) >= touchInterval {
		sess.LastUsedAt, sess.IdleExpiresAt = now, now.Add(SessionIdleTimeout)
		if err := s.store.TouchSession(ctx, dbgen.TouchSessionParams{
			OrgID: s.orgID, ID: row.ID, Now: now, IdleExpiresAt: sess.IdleExpiresAt,
		}); err != nil {
			return Session{}, fmt.Errorf("record the use of the session %s: %w", row.PublicID, err)
		}
	}
	return sess, nil
}

// CSRFToken is the CSRF token of sess.
func (s *Service) CSRFToken(sess Session) (string, error) {
	return csrfToken(s.keyring, sess.token)
}

// CheckCSRF reports whether header carries the CSRF token of sess.
func (s *Service) CheckCSRF(sess Session, header string) bool {
	return len(sess.token) == tokenBytes && checkCSRF(s.keyring, sess.token, header)
}

// SignOut ends sess.
func (s *Service) SignOut(ctx context.Context, sess Session, addr netip.Addr) error {
	return s.store.InTx(ctx, func(q Queries) error {
		if _, err := q.EndSession(ctx, dbgen.EndSessionParams{
			OrgID: s.orgID, ID: sess.ID, Now: s.clock.Now().UTC(), EndReason: optText(EndSignOut),
		}); err != nil {
			return fmt.Errorf("end the session %s: %w", sess.PublicID, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: audit.User(sess.User.ID, sess.User.PublicID), Transport: audit.TransportUI,
			Action:        audit.ActionSignedOut,
			Resource:      audit.Resource{Type: audit.ResourceSession, PublicID: sess.PublicID, Name: sess.User.Name},
			SourceAddress: addr,
		})
	})
}

// SignOutEverywhere ends every session of sess's user, sess included.
func (s *Service) SignOutEverywhere(ctx context.Context, sess Session, addr netip.Addr) error {
	return s.store.InTx(ctx, func(q Queries) error {
		n, err := q.EndUserSessions(ctx, dbgen.EndUserSessionsParams{
			OrgID: s.orgID, UserID: sess.User.ID, Now: s.clock.Now().UTC(), EndReason: optText(EndSignOutEverywhere),
		})
		if err != nil {
			return fmt.Errorf("end the sessions of %s: %w", sess.User.PublicID, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: audit.User(sess.User.ID, sess.User.PublicID), Transport: audit.TransportUI,
			Action:   audit.ActionSessionsEnded,
			Resource: audit.Resource{Type: audit.ResourceUser, PublicID: sess.User.PublicID, Name: sess.User.Name},
			Details:  map[string]any{"sessions": n}, SourceAddress: addr,
		})
	})
}

// ListSessions lists the sessions of sess's user that have neither ended nor expired, marking sess.
func (s *Service) ListSessions(ctx context.Context, sess Session) ([]SessionInfo, error) {
	rows, err := s.store.ListUserSessions(ctx, dbgen.ListUserSessionsParams{
		OrgID: s.orgID, UserID: sess.User.ID, Now: s.clock.Now().UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("list the sessions of %s: %w", sess.User.PublicID, err)
	}
	out := make([]SessionInfo, 0, len(rows))
	for _, r := range rows {
		info := SessionInfo{
			PublicID: r.PublicID, Method: r.Method, UserAgent: r.UserAgent.String, CreatedAt: r.CreatedAt,
			LastUsedAt: r.LastUsedAt, Current: r.ID == sess.ID,
		}
		if r.Address != nil {
			info.Address = *r.Address
		}
		out = append(out, info)
	}
	return out, nil
}

// PasswordChange is a user's change of their own password.
type PasswordChange struct {
	Current string
	New     string
	Address netip.Addr
}

// ChangePassword replaces the password of sess's user after checking the current one, and ends the user's other
// sessions; sess continues. A new password that is too short is ErrPasswordTooShort, a wrong current password
// ErrInvalidCredentials, and an account that signs in through OIDC ErrNotLocal. A wrong current password counts in the
// sign-in throttle of the account and the address like a failed sign-in, so that a stolen session cannot try passwords
// faster than a sign-in could; an attempt the throttle refuses is a *ThrottledError.
func (s *Service) ChangePassword(ctx context.Context, sess Session, c PasswordChange) error {
	if err := CheckPasswordLength(c.New); err != nil {
		return err
	}
	row, err := s.store.GetUserPassword(ctx, dbgen.GetUserPasswordParams{OrgID: s.orgID, ID: sess.User.ID})
	if err != nil {
		return fmt.Errorf("read the password of %s: %w", sess.User.PublicID, err)
	}
	if row.HasOidcIdentity || !row.PasswordHash.Valid {
		return ErrNotLocal
	}
	account, address := accountSubject(row.Login), addressSubject(c.Address)
	if wait, err := s.throttled(ctx, account, address); err != nil {
		return err
	} else if wait > 0 {
		return &ThrottledError{RetryAfter: wait}
	}
	ok, err := VerifyPassword(ctx, row.PasswordHash.String, c.Current)
	if err != nil {
		return fmt.Errorf("check the password of %s: %w", sess.User.PublicID, err)
	}
	if !ok {
		if err := s.recordFailure(ctx, account, address); err != nil {
			return err
		}
		metrics.LoginFailures.With(MethodLocal).Inc()
		return ErrInvalidCredentials
	}
	hash, err := HashPassword(ctx, c.New)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	return s.store.InTx(ctx, func(q Queries) error {
		n, err := q.SetPassword(ctx, dbgen.SetPasswordParams{
			OrgID: s.orgID, ID: sess.User.ID, PasswordHash: optText(hash), Now: now,
		})
		if err != nil {
			return fmt.Errorf("set the password of %s: %w", sess.User.PublicID, err)
		}
		if n == 0 {
			return ErrNotLocal
		}
		ended, err := q.EndOtherUserSessions(ctx, dbgen.EndOtherUserSessionsParams{
			OrgID: s.orgID, UserID: sess.User.ID, KeepID: sess.ID, Now: now, EndReason: optText(EndPasswordChanged),
		})
		if err != nil {
			return fmt.Errorf("end the other sessions of %s: %w", sess.User.PublicID, err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: audit.User(sess.User.ID, sess.User.PublicID), Transport: audit.TransportUI,
			Action:   audit.ActionPasswordChanged,
			Resource: audit.Resource{Type: audit.ResourceUser, PublicID: sess.User.PublicID, Name: sess.User.Name},
			Diff:     []audit.Change{{Pointer: "/password", SecretChanged: true}},
			Details:  map[string]any{"sessions_ended": ended}, SourceAddress: c.Address,
		})
	})
}

func optText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// truncate cuts s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
