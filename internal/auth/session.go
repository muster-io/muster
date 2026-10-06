// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package auth signs people in and keeps their sessions (C-03): local sign-in with argon2id passwords and its
// throttling, the second step of a user with TOTP and the limited session states, sessions in PostgreSQL carried in
// the muster_session cookie, the CSRF token derived from the session, the password change, the client address behind
// trusted proxies, and the Permissions of the Roles. Every session time follows the business clock.
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
	"github.com/muster-io/muster/internal/organization"
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

// FailureTOTP is the method label of muster_login_failures_total for a wrong TOTP or recovery code.
const FailureTOTP = "totp"

// ActionSecondFactorFailed is the Audit log action of a wrong TOTP or recovery code at sign-in.
const ActionSecondFactorFailed = "session.second_factor_failed"

// The reasons a session ends, as sessions.end_reason stores them.
const (
	EndSignOut           = "sign_out"
	EndSignOutEverywhere = "sign_out_everywhere"
	EndPasswordChanged   = "password_changed"
	EndExpired           = "expired"
	// The administrative endings: an Admin changed the Role, disabled or deleted the user (C-03.FR-9, FR-13).
	EndRoleChanged  = "role_changed"
	EndUserDisabled = "user_disabled"
	EndUserDeleted  = "user_deleted"
)

var (
	// ErrInvalidCredentials is a wrong login, password or second factor, or an account that cannot sign in; it never
	// says which.
	ErrInvalidCredentials = errors.New("the login, the password or the code is wrong")
	// ErrUnauthenticated is a request without a usable session.
	ErrUnauthenticated = errors.New("no valid session")
	// ErrSessionExpired is a session that ended by idle timeout or lifetime.
	ErrSessionExpired = errors.New("the session expired")
	// ErrNotLocal is a password change of an account that signs in through OIDC.
	ErrNotLocal = errors.New("the account signs in through OIDC and has no password")
	// ErrTOTPNotPending is a second factor given to a session that does not wait for one.
	ErrTOTPNotPending = errors.New("the session is not waiting for a TOTP code")
)

// Proof is a second factor: a current TOTP code or a single-use recovery code.
type Proof struct {
	TOTPCode     string
	RecoveryCode string
}

// Given reports whether the proof carries a code.
func (p Proof) Given() bool {
	return p.TOTPCode != "" || p.RecoveryCode != ""
}

// SecondFactor checks the TOTP of the users; internal/totp implements it.
type SecondFactor interface {
	// Verify reports whether p is a current TOTP code of the user not used before, or one of the user's unused
	// recovery codes, and uses it up. A user without TOTP has no right code.
	Verify(ctx context.Context, userID int64, p Proof) (bool, error)
}

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
	CompleteSecondFactor(ctx context.Context, arg dbgen.CompleteSecondFactorParams) (int64, error)
	LiveSessions(ctx context.Context, arg dbgen.LiveSessionsParams) ([]int64, error)
	TryLockSignInSubject(ctx context.Context, arg dbgen.TryLockSignInSubjectParams) (bool, error)
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
	factor  SecondFactor
}

// NewService returns the Service of the Organization orgID; clock is the business clock.
func NewService(orgID int64, s Store, k *keyring.Keyring, w *audit.Writer, business clock.Clock, roles Roles) *Service {
	// The series of muster_login_failures_total exist from the start, so that an increase is seen from zero.
	for _, method := range []string{MethodLocal, MethodOIDC, FailureTOTP} {
		metrics.LoginFailures.With(method)
	}
	prepareDummy()
	return &Service{orgID: orgID, store: s, keyring: k, audit: w, clock: business, roles: roles}
}

// UseSecondFactor sets what checks the TOTP codes and recovery codes of the users; without it no code is right.
func (s *Service) UseSecondFactor(f SecondFactor) {
	s.factor = f
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

// SignInRequest is a local sign-in attempt, with the second factor when the user finishes in one request.
type SignInRequest struct {
	Login     string
	Password  string
	Proof     Proof
	Address   netip.Addr
	UserAgent string
}

// SignIn checks a login and a password and opens a session. The login is compared lowercased. A wrong login or
// password, a disabled or deleted account and an account without a password are all ErrInvalidCredentials, and an
// unknown login spends the time of a password check too. An attempt before the throttle allows one is a
// *ThrottledError and is not evaluated.
//
// The session of a user with TOTP is active when the request carries a right code and totp_required without one; a
// wrong code is ErrInvalidCredentials and counts like a wrong password (C-03.FR-24). A user without TOTP whom the TOTP
// policy covers gets a session in the state totp_enrolment_required. Only a session that is active resets the
// throttle, so that a known password never buys more guesses at the code.
func (s *Service) SignIn(ctx context.Context, req SignInRequest) (Session, error) {
	account := accountSubject(req.Login)
	address := addressSubject(req.Address)
	var (
		user          dbgen.GetSignInUserRow
		known         bool
		wrongPassword bool
	)
	ok, err := s.attempt(ctx, account, address, func(ctx context.Context) (bool, error) {
		var err error
		user, err = s.store.GetSignInUser(ctx, dbgen.GetSignInUserParams{OrgID: s.orgID, Login: req.Login})
		known = err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("find the account: %w", err)
		}
		right := false
		if known && user.Status == "active" && user.PasswordHash.Valid {
			if right, err = VerifyPassword(ctx, user.PasswordHash.String, req.Password); err != nil {
				return false, fmt.Errorf("check the password of %s: %w", user.PublicID, err)
			}
		} else if err := verifyDummy(ctx, req.Password); err != nil {
			return false, err
		}
		if !right {
			wrongPassword = true
			return false, nil
		}
		if !user.TotpEnrolled || !req.Proof.Given() {
			return true, nil
		}
		return s.verify(ctx, user.ID, req.Proof)
	})
	switch {
	case err != nil:
		return Session{}, err
	case wrongPassword:
		return Session{}, s.failSignIn(ctx, req, user, known)
	case !ok:
		return Session{}, s.failSecondFactor(ctx, req.Address, Principal{ID: user.ID, PublicID: user.PublicID,
			Name: user.Name})
	}
	state := StateActive
	switch {
	case user.TotpEnrolled && !req.Proof.Given():
		state = StateTOTPRequired
	case !user.TotpEnrolled && organization.TOTPPolicy(user.TotpRequired).Covers(true):
		state = StateTOTPEnrolmentRequired
	}
	return s.openSession(ctx, req, account, address, user, state)
}

// verify checks a second factor of the user; without a SecondFactor no code is right.
func (s *Service) verify(ctx context.Context, userID int64, p Proof) (bool, error) {
	if s.factor == nil || !p.Given() {
		return false, nil
	}
	return s.factor.Verify(ctx, userID, p)
}

// failSecondFactor records a wrong TOTP or recovery code that the throttle counted, and is ErrInvalidCredentials.
func (s *Service) failSecondFactor(ctx context.Context, addr netip.Addr, user Principal) error {
	metrics.LoginFailures.With(FailureTOTP).Inc()
	if err := s.audit.Record(ctx, s.store, audit.Entry{
		OrgID: s.orgID, Actor: audit.User(user.ID, user.PublicID), Transport: audit.TransportUI,
		Action:   ActionSecondFactorFailed,
		Resource: audit.Resource{Type: audit.ResourceUser, PublicID: user.PublicID, Name: user.Name},
		Details:  map[string]any{"method": MethodLocal}, SourceAddress: addr,
	}); err != nil {
		return err
	}
	return ErrInvalidCredentials
}

// failSignIn records a wrong login or password that the throttle counted, and is ErrInvalidCredentials.
func (s *Service) failSignIn(ctx context.Context, req SignInRequest, user dbgen.GetSignInUserRow, known bool) error {
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

// newSession makes a session of user with a fresh token, starting now on the business clock, and the parameters
// that store it; lifetime bounds its total life.
func (s *Service) newSession(user Principal, state SessionState, method string, lifetime time.Duration,
	addr netip.Addr, userAgent string) (Session, dbgen.CreateSessionParams, error) {
	token := make([]byte, tokenBytes)
	if _, err := rand.Read(token); err != nil {
		return Session{}, dbgen.CreateSessionParams{}, fmt.Errorf("make a session token: %w", err)
	}
	now := s.clock.Now().UTC()
	sess := Session{
		PublicID: publicid.New(publicid.Session), State: state, Method: method, CreatedAt: now, LastUsedAt: now,
		IdleExpiresAt: now.Add(SessionIdleTimeout), ExpiresAt: now.Add(lifetime), User: user, token: token,
	}
	hash := sha256.Sum256(token)
	p := dbgen.CreateSessionParams{
		OrgID: s.orgID, PublicID: sess.PublicID, UserID: user.ID, TokenHash: hash[:], State: string(sess.State),
		Method: sess.Method, UserAgent: optText(truncate(userAgent, maxUserAgent)), CreatedAt: now,
		IdleExpiresAt: sess.IdleExpiresAt, ExpiresAt: sess.ExpiresAt,
	}
	if addr.IsValid() {
		a := addr
		p.Address = &a
	}
	return sess, p, nil
}

func (s *Service) openSession(ctx context.Context, req SignInRequest, account, address string,
	user dbgen.GetSignInUserRow, state SessionState) (Session, error) {
	sess, p, err := s.newSession(Principal{ID: user.ID, PublicID: user.PublicID, Name: user.Name, Role: user.Role},
		state, MethodLocal, SessionLifetime, req.Address, req.UserAgent)
	if err != nil {
		return Session{}, err
	}
	now := sess.CreatedAt
	err = s.store.InTx(ctx, func(q Queries) error {
		if state == StateActive {
			if err := q.ResetSignInThrottles(ctx, dbgen.ResetSignInThrottlesParams{
				OrgID: s.orgID, Account: account, Address: address,
			}); err != nil {
				return fmt.Errorf("reset the sign-in throttle: %w", err)
			}
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
			Details:  map[string]any{"method": MethodLocal, "state": string(state)}, SourceAddress: req.Address,
		})
	})
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

// OIDCSignIn is a session to open for a user whom an OIDC sign-in identified; internal/oidc checked the identity and
// decided the state.
type OIDCSignIn struct {
	User  Principal
	State SessionState
	// IDPMFA records that the identity provider asserted multi-factor authentication (amr).
	IDPMFA bool
	// Lifetime bounds the session's total life: auth.session_lifetime, or auth.oidc_fallback_session_lifetime for a
	// user without an offline token. Zero is auth.session_lifetime.
	Lifetime  time.Duration
	Address   netip.Addr
	UserAgent string
}

// OpenOIDCSession opens a session with the method oidc after an OIDC sign-in and records the sign-in as
// session.signed_in with the method oidc (C-03.FR-25).
func (s *Service) OpenOIDCSession(ctx context.Context, in OIDCSignIn) (Session, error) {
	lifetime := SessionLifetime
	if in.Lifetime > 0 && in.Lifetime < lifetime {
		lifetime = in.Lifetime
	}
	sess, p, err := s.newSession(in.User, in.State, MethodOIDC, lifetime, in.Address, in.UserAgent)
	if err != nil {
		return Session{}, err
	}
	p.IdpMfa = in.IDPMFA
	err = s.store.InTx(ctx, func(q Queries) error {
		id, err := q.CreateSession(ctx, p)
		if err != nil {
			return fmt.Errorf("create the session: %w", err)
		}
		sess.ID = id
		if err := q.MarkSignedIn(ctx, dbgen.MarkSignedInParams{OrgID: s.orgID, ID: in.User.ID,
			Now: sess.CreatedAt}); err != nil {
			return fmt.Errorf("record the sign-in: %w", err)
		}
		return s.audit.Record(ctx, q, audit.Entry{
			OrgID: s.orgID, Actor: audit.User(in.User.ID, in.User.PublicID), Transport: audit.TransportUI,
			Action:        audit.ActionSignedIn,
			Resource:      audit.Resource{Type: audit.ResourceSession, PublicID: sess.PublicID, Name: in.User.Name},
			Details:       map[string]any{"method": MethodOIDC, "state": string(in.State), "idp_mfa": in.IDPMFA},
			SourceAddress: in.Address,
		})
	})
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

// SubmitSecondFactor completes a session in the state totp_required with a TOTP code or a recovery code of its user
// (C-03.FR-24) and returns it active. A session that waits for no code is ErrTOTPNotPending. The code is checked under
// the sign-in throttle of the user's account and of addr like a password: an attempt the throttle refuses is a
// *ThrottledError, and a wrong code is ErrInvalidCredentials, counted, recorded as session.second_factor_failed and
// in muster_login_failures_total{method="totp"}. A right code resets the throttle.
func (s *Service) SubmitSecondFactor(ctx context.Context, sess Session, p Proof, addr netip.Addr) (Session, error) {
	if sess.State != StateTOTPRequired {
		return Session{}, ErrTOTPNotPending
	}
	row, err := s.store.GetUserPassword(ctx, dbgen.GetUserPasswordParams{OrgID: s.orgID, ID: sess.User.ID})
	if err != nil {
		return Session{}, fmt.Errorf("read the account of %s: %w", sess.User.PublicID, err)
	}
	account, address := accountSubject(row.Login), addressSubject(addr)
	right, err := s.attempt(ctx, account, address, func(ctx context.Context) (bool, error) {
		return s.verify(ctx, sess.User.ID, p)
	})
	if err != nil {
		return Session{}, err
	}
	if !right {
		return Session{}, s.failSecondFactor(ctx, addr, sess.User)
	}
	err = s.store.InTx(ctx, func(q Queries) error {
		n, err := q.CompleteSecondFactor(ctx, dbgen.CompleteSecondFactorParams{OrgID: s.orgID, ID: sess.ID})
		if err != nil {
			return fmt.Errorf("complete the session %s: %w", sess.PublicID, err)
		}
		if n == 0 {
			return ErrTOTPNotPending
		}
		if err := q.ResetSignInThrottles(ctx, dbgen.ResetSignInThrottlesParams{
			OrgID: s.orgID, Account: account, Address: address,
		}); err != nil {
			return fmt.Errorf("reset the sign-in throttle: %w", err)
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	sess.State = StateActive
	return sess, nil
}

// Attempt checks a proof that sess's user gives on their own account — the password, a TOTP code or a recovery
// code — under the sign-in throttle of the account and of addr, so that a stolen session cannot guess faster than a
// sign-in could. An attempt the throttle refuses is a *ThrottledError and check does not run; a wrong proof counts
// as a failed sign-in, in muster_login_failures_total with method (local for a password, totp for a code), and is
// ErrInvalidCredentials.
func (s *Service) Attempt(ctx context.Context, sess Session, addr netip.Addr, method string,
	check func(context.Context) (bool, error)) error {
	row, err := s.store.GetUserPassword(ctx, dbgen.GetUserPasswordParams{OrgID: s.orgID, ID: sess.User.ID})
	if err != nil {
		return fmt.Errorf("read the account of %s: %w", sess.User.PublicID, err)
	}
	ok, err := s.attempt(ctx, accountSubject(row.Login), addressSubject(addr), check)
	if err != nil {
		return err
	}
	if !ok {
		metrics.LoginFailures.With(method).Inc()
		return ErrInvalidCredentials
	}
	return nil
}

// CheckPassword reports whether password is the current password of sess's user; an account without a password has
// no right one. It does not throttle: run it inside Attempt.
func (s *Service) CheckPassword(ctx context.Context, sess Session, password string) (bool, error) {
	row, err := s.store.GetUserPassword(ctx, dbgen.GetUserPasswordParams{OrgID: s.orgID, ID: sess.User.ID})
	if err != nil {
		return false, fmt.Errorf("read the password of %s: %w", sess.User.PublicID, err)
	}
	if !row.PasswordHash.Valid {
		return false, verifyDummy(ctx, password)
	}
	ok, err := VerifyPassword(ctx, row.PasswordHash.String, password)
	if err != nil {
		return false, fmt.Errorf("check the password of %s: %w", sess.User.PublicID, err)
	}
	return ok, nil
}

// LiveSessions returns the sessions of ids that are still usable — active, neither ended nor expired, of an active
// user — without counting the check as a use: the live-updates streams close the others.
func (s *Service) LiveSessions(ctx context.Context, ids []int64) ([]int64, error) {
	live, err := s.store.LiveSessions(ctx, dbgen.LiveSessionsParams{OrgID: s.orgID, Ids: ids, Now: s.clock.Now().UTC()})
	if err != nil {
		return nil, fmt.Errorf("check the sessions of the live-updates streams: %w", err)
	}
	return live, nil
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
	ok, err := s.attempt(ctx, accountSubject(row.Login), addressSubject(c.Address),
		func(ctx context.Context) (bool, error) {
			ok, err := VerifyPassword(ctx, row.PasswordHash.String, c.Current)
			if err != nil {
				return false, fmt.Errorf("check the password of %s: %w", sess.User.PublicID, err)
			}
			return ok, nil
		})
	if err != nil {
		return err
	}
	if !ok {
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
