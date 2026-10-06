// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/oidc/dbgen"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/publicid"
)

const (
	// AuthRequestTTL is oidc.auth_request_ttl: an OIDC redirect must come back to the callback within it.
	AuthRequestTTL = 10 * time.Minute
	// AuthRequestPruneAfter is how long after its expiry a request is deleted by short-lived pruning.
	AuthRequestPruneAfter = time.Hour
	// FallbackSessionLifetime is auth.oidc_fallback_session_lifetime: an OIDC session of a user without an offline
	// token ends this long after sign-in, since no background re-check can end it earlier.
	FallbackSessionLifetime = 12 * time.Hour

	// MaxPendingRequests bounds the OIDC redirects in flight per Organization: a start needs no credentials, and each
	// writes a row that lives until AuthRequestTTL after it plus AuthRequestPruneAfter.
	MaxPendingRequests = 10_000

	// CallbackPath is the redirect URI of sign-in below MUSTER_PUBLIC_URL.
	CallbackPath = "api/v1/sessions/oidc/callback"
	// SignInPage is the page of the SPA a failed sign-in returns to, with ?error=<code>.
	SignInPage = "/sign-in"

	purposeSignIn       = "sign_in"
	fieldCodeVerifier   = "oidc_auth_requests.code_verifier"
	randomBytes         = 32
	maxReturnTo         = 2000
	maxLoginLength      = 200
	maxNameLength       = 200
	maxEmailLength      = 254
	deletedLoginPrefix  = "deleted-user-"
	uniqueViolation     = "23505"
	loginIndex          = "users_login_key"
	identityIndex       = "users_oidc_subject_key"
	endReasonRoleChange = "role_changed"

	// The places Role sync runs at, as user.role_sync_kept_admin records them.
	syncAtSignIn  = "sign_in"
	syncAtRecheck = "recheck"
)

// The callback errors of a failed sign-in (C-03.FR-25), as /sign-in?error=<code> carries them.
const (
	ErrorNoAccess        = "no_access"
	ErrorLoginTaken      = "login_taken"
	ErrorAccountDisabled = "account_disabled"
	ErrorOIDCDisabled    = "oidc_disabled"
	ErrorInvalidRequest  = "invalid_request"
	ErrorIDPError        = "idp_error"
)

// Start is the redirect that starts a sign-in: the authorization URL and the state, which the caller also sets in the
// browser-binding cookie.
type Start struct {
	URL   string
	State string
}

// StartSignIn stores a sign-in request — the hash of a new state, the nonce, the PKCE verifier encrypted and
// returnTo when it is a relative path — valid for AuthRequestTTL, and returns the redirect to the identity provider.
// OIDC that is off is ErrNotEnabled; a failed discovery is a *BackChannelError.
func (s *Service) StartSignIn(ctx context.Context, returnTo string) (Start, error) {
	st, row, err := s.read(ctx, s.cfg.Store)
	if err != nil {
		return Start{}, err
	}
	if !st.Configured || !st.Enabled {
		return Start{}, ErrNotEnabled
	}
	p, err := s.providerFor(row, st)
	if err != nil {
		return Start{}, err
	}
	d, err := p.Discover(ctx)
	if err != nil {
		return Start{}, err
	}
	state, nonce, verifier := randomToken(), randomToken(), randomToken()
	ct, keyID, err := s.cfg.Keyring.Encrypt(fieldCodeVerifier, []byte(verifier))
	if err != nil {
		return Start{}, fmt.Errorf("encrypt the PKCE verifier: %w", err)
	}
	now := s.cfg.Clocks.Business.Now().UTC()
	hash := sha256.Sum256([]byte(state))
	n, err := s.cfg.Store.InsertAuthRequest(ctx, dbgen.InsertAuthRequestParams{
		StateHash: hash[:], OrgID: s.cfg.OrgID, Purpose: purposeSignIn, Nonce: nonce, CodeVerifierCiphertext: ct,
		CodeVerifierKeyID: keyID, ReturnTo: optText(SafeReturnTo(returnTo)), CreatedAt: now,
		ExpiresAt: now.Add(AuthRequestTTL), MaxPending: MaxPendingRequests,
	})
	if err != nil {
		return Start{}, fmt.Errorf("store the sign-in request: %w", err)
	}
	if n == 0 {
		return Start{}, ErrTooManyRequests
	}
	u, err := p.AuthURL(d, AuthRequest{RedirectURI: s.redirectURI(), Scopes: st.Scopes, State: state, Nonce: nonce,
		Verifier: verifier})
	if err != nil {
		return Start{}, err
	}
	return Start{URL: u, State: state}, nil
}

func (s *Service) redirectURI() string {
	u := s.cfg.PublicURL.JoinPath(CallbackPath)
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// SafeReturnTo is returnTo when it is a relative path that starts with a single / — never //host, /\host, a scheme or
// a control character — and the empty string otherwise, so the flow is no open redirect (C-03.FR-25).
func SafeReturnTo(returnTo string) string {
	if returnTo == "" || len(returnTo) > maxReturnTo || returnTo[0] != '/' {
		return ""
	}
	if len(returnTo) > 1 && (returnTo[1] == '/' || returnTo[1] == '\\') {
		return ""
	}
	if strings.IndexFunc(returnTo, func(r rune) bool { return unicode.IsControl(r) || r == '\\' }) >= 0 {
		return ""
	}
	u, err := url.Parse(returnTo)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return ""
	}
	return returnTo
}

func randomToken() string {
	b := make([]byte, randomBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// Callback is what the identity provider sent the browser back with, and the browser's binding cookie.
type Callback struct {
	Code        string
	State       string
	Error       string
	CookieState string
	Address     netip.Addr
	UserAgent   string
}

// Outcome is where a callback sends the browser: a session and its page, or the sign-in page with an error code.
type Outcome struct {
	Session  *auth.Session
	Redirect string
	Error    string
}

func failure(code string) Outcome {
	return Outcome{Redirect: SignInPage + "?error=" + code, Error: code}
}

// CompleteSignIn finishes a sign-in (C-03.FR-25, FR-28, FR-7, FR-10): it takes the request of the state once, exchanges
// the code, verifies the ID token, reads the groups and resolves the account, then opens the session. Every outcome
// is a redirect; a failure of the identity provider or of Muster itself is idp_error and is logged as
// oidc_sign_in_failed.
func (s *Service) CompleteSignIn(ctx context.Context, cb Callback) Outcome {
	out, reason, err := s.complete(ctx, cb)
	if err != nil {
		s.cfg.Log.Log(ctx, logging.OIDCSignInFailed, logging.F("reason", reason), logging.F("error", masked(err)))
		return failure(ErrorIDPError)
	}
	return out
}

func (s *Service) complete(ctx context.Context, cb Callback) (Outcome, string, error) {
	if cb.State == "" || cb.CookieState == "" || cb.State != cb.CookieState {
		return failure(ErrorInvalidRequest), "", nil
	}
	hash := sha256.Sum256([]byte(cb.State))
	req, err := s.cfg.Store.TakeAuthRequest(ctx, dbgen.TakeAuthRequestParams{OrgID: s.cfg.OrgID, StateHash: hash[:]})
	if errors.Is(err, pgx.ErrNoRows) {
		return failure(ErrorInvalidRequest), "", nil
	}
	if err != nil {
		return Outcome{}, "request", fmt.Errorf("take the sign-in request: %w", err)
	}
	if req.Purpose != purposeSignIn || !s.cfg.Clocks.Business.Now().Before(req.ExpiresAt) {
		return failure(ErrorInvalidRequest), "", nil
	}
	st, row, err := s.read(ctx, s.cfg.Store)
	if err != nil {
		return Outcome{}, "settings", err
	}
	if !st.Configured || !st.Enabled {
		return failure(ErrorOIDCDisabled), "", nil
	}
	if cb.Error != "" {
		return Outcome{}, "authorization", &BackChannelError{Step: "authorization",
			msg: "the identity provider answered the error " + printable(cb.Error)}
	}
	if cb.Code == "" {
		return failure(ErrorInvalidRequest), "", nil
	}
	verifier, err := s.cfg.Keyring.Decrypt(fieldCodeVerifier, req.CodeVerifierKeyID, req.CodeVerifierCiphertext)
	if err != nil {
		return Outcome{}, "request", err
	}
	p, err := s.providerFor(row, st)
	if err != nil {
		return Outcome{}, "back channel", err
	}
	d, err := p.Discover(ctx)
	if err != nil {
		return Outcome{}, "discovery", err
	}
	tokens, err := p.Exchange(ctx, d, cb.Code, string(verifier), s.redirectURI())
	if err != nil {
		return Outcome{}, "token exchange", err
	}
	idt, err := p.Verify(ctx, d, tokens.IDToken, req.Nonce)
	if errors.Is(err, ErrNonce) {
		return failure(ErrorInvalidRequest), "", nil
	}
	if err != nil {
		return Outcome{}, "id token", err
	}
	groups, found, err := s.groups(ctx, p, d, st.GroupsClaim, idt, tokens.AccessToken)
	if err != nil {
		return Outcome{}, "userinfo", err
	}
	if found == st.GroupsClaimMissing {
		if err := s.setGroupsClaimMissing(ctx, !found); err != nil {
			return Outcome{}, "settings", err
		}
	}
	return s.signIn(ctx, cb, st, identityOf(d.Issuer, idt, groups, offlineToken(tokens)), req.ReturnTo.String)
}

// identity is what a verified callback says about the person: the identity, its claims, its groups and the offline
// token the identity provider granted, if any.
type identity struct {
	issuer  string
	idt     IDToken
	groups  []string
	offline logging.Secret
}

func identityOf(issuer string, idt IDToken, groups []string, offline logging.Secret) identity {
	return identity{issuer: issuer, idt: idt, groups: groups, offline: offline}
}

// groups reads the groups claim from the ID token, and from userinfo when the ID token lacks it (D247); found is false
// when neither carries it.
func (s *Service) groups(ctx context.Context, p *Provider, d Discovery, claim string, idt IDToken,
	access logging.Secret) ([]string, bool, error) {
	if v, ok := idt.Claims[claim]; ok {
		return stringList(v), true, nil
	}
	info, err := p.UserInfo(ctx, d, access)
	if err != nil {
		return nil, false, err
	}
	if info == nil {
		return []string{}, false, nil
	}
	if sub, _ := info["sub"].(string); sub != idt.Subject {
		return nil, false, &BackChannelError{Step: "userinfo", msg: "userinfo names another subject than the ID token"}
	}
	v, ok := info[claim]
	if !ok {
		return []string{}, false, nil
	}
	return stringList(v), true, nil
}

// conflict is a new account that a unique index refused: the login (users_login_key) or the identity
// (users_oidc_subject_key) was taken by another transaction after the checks of this one.
type conflict struct {
	index string
	login string
}

func (c *conflict) Error() string { return "create the account: " + c.index + " refused it" }

// refusal is a sign-in refused for a reason the person can act on; it is recorded after the transaction.
type refusal struct {
	code    string
	user    *dbgen.GetIdentityUserRow
	details map[string]any
}

// signIn resolves the account of the identity and opens its session: with an offline token it lives
// auth.session_lifetime, since the re-checks can end it, and auth.oidc_fallback_session_lifetime without one.
func (s *Service) signIn(ctx context.Context, cb Callback, st Settings, id identity, returnTo string) (Outcome, string,
	error) {
	role, mapped := MapRole(id.groups, st.GroupMappings, st.UnmatchedRole)
	var (
		user dbgen.GetIdentityUserRow
		ref  *refusal
	)
	// A unique index that refuses the new account aborts the transaction, so the conflict leaves it as an error and is
	// handled after the rollback: a login taken meanwhile is a refusal, the identity created by a parallel callback of
	// the same person is found by a second attempt.
	var err error
	for range 2 {
		ref = nil
		err = s.cfg.Store.InTx(ctx, func(q Queries) error {
			var err error
			user, ref, err = s.resolve(ctx, q, st, id, role, mapped)
			return err
		})
		if c, ok := errors.AsType[*conflict](err); ok {
			if c.index == loginIndex {
				ref, err = &refusal{code: ErrorLoginTaken, details: map[string]any{"login": c.login}}, nil
				break
			}
			continue
		}
		break
	}
	if err != nil {
		return Outcome{}, "account", err
	}
	if ref != nil {
		return s.refuse(ctx, cb, ref)
	}
	mfa := assertsMFA(id.idt.AMR)
	lifetime := FallbackSessionLifetime
	if id.offline != "" {
		lifetime = auth.SessionLifetime
	}
	state := auth.StateActive
	switch {
	case user.TotpEnrolled && (!st.SkipTOTPWithIDPMFA || !mfa):
		state = auth.StateTOTPRequired
	case !user.TotpEnrolled && organization.TOTPPolicy(user.TotpRequired).Covers(false):
		state = auth.StateTOTPEnrolmentRequired
	}
	sess, err := s.cfg.Sessions.OpenOIDCSession(ctx, auth.OIDCSignIn{
		User:  auth.Principal{ID: user.ID, PublicID: user.PublicID, Name: user.Name, Role: user.Role},
		State: state, IDPMFA: mfa, Lifetime: lifetime, Address: cb.Address, UserAgent: cb.UserAgent,
	})
	if err != nil {
		return Outcome{}, "session", err
	}
	if returnTo == "" {
		returnTo = "/"
	}
	return Outcome{Session: &sess, Redirect: returnTo}, "", nil
}

// resolve finds or creates the account of the identity inside a transaction, applies Role sync and keeps the offline
// token. Accounts are never merged by login or email: a new identity whose login is taken is refused (C-03.FR-28).
func (s *Service) resolve(ctx context.Context, q Queries, st Settings, id identity, role string,
	mapped bool) (dbgen.GetIdentityUserRow, *refusal, error) {
	issuer, idt, groups := id.issuer, id.idt, id.groups
	key := dbgen.GetIdentityUserParams{OrgID: s.cfg.OrgID, Issuer: optText(issuer), Subject: optText(idt.Subject)}
	user, err := q.GetIdentityUser(ctx, key)
	known := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return user, nil, fmt.Errorf("find the account of the identity: %w", err)
	}
	now := s.cfg.Clocks.Business.Now().UTC()
	switch {
	case known && user.Status != "active":
		return user, &refusal{code: ErrorAccountDisabled, user: &user, details: map[string]any{}}, nil
	case !mapped:
		r := &refusal{code: ErrorNoAccess, details: map[string]any{"groups": auditGroups(groups)}}
		if known {
			r.user = &user
		}
		return user, r, nil
	case known:
		if st.SyncRole && role != user.Role {
			if err := s.syncRole(ctx, q, user, role, now, syncAtSignIn); err != nil {
				return user, nil, err
			}
		}
	default:
		login := loginOf(idt)
		taken, err := q.LoginTaken(ctx, dbgen.LoginTakenParams{OrgID: s.cfg.OrgID, Login: login})
		if err != nil {
			return user, nil, fmt.Errorf("check the login: %w", err)
		}
		if taken {
			return user, &refusal{code: ErrorLoginTaken, details: map[string]any{"login": login}}, nil
		}
		if err := s.create(ctx, q, issuer, idt, login, role, now); err != nil {
			return user, nil, err
		}
	}
	if user, err = q.GetIdentityUser(ctx, key); err != nil {
		return user, nil, fmt.Errorf("read the account of the identity: %w", err)
	}
	if err := q.RecordContact(ctx, dbgen.RecordContactParams{OrgID: s.cfg.OrgID, ID: user.ID, Now: now}); err != nil {
		return user, nil, fmt.Errorf("record the contact with the identity provider: %w", err)
	}
	return user, nil, s.keepOfflineToken(ctx, q, user.ID, id.offline, now)
}

// create creates the account of a new identity with the mapped Role and records user.created.
func (s *Service) create(ctx context.Context, q Queries, issuer string, idt IDToken, login, role string,
	now time.Time) error {
	name := strings.TrimSpace(idt.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLength || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		name = login
	}
	email := validEmail(idt.Email)
	pid := publicid.New(publicid.User)
	id, err := q.CreateIdentityUser(ctx, dbgen.CreateIdentityUserParams{
		OrgID: s.cfg.OrgID, PublicID: pid, Login: login, Name: name, Email: optText(email), Role: role,
		Issuer: optText(issuer), Subject: optText(idt.Subject), CreatedAt: now,
	})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation &&
		(pgErr.ConstraintName == loginIndex || pgErr.ConstraintName == identityIndex) {
		return &conflict{index: pgErr.ConstraintName, login: login}
	}
	if err != nil {
		return fmt.Errorf("create the account: %w", err)
	}
	type created struct {
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email,omitempty"`
		Role  string `json:"role"`
	}
	return s.cfg.Audit.Record(ctx, q, audit.Entry{
		OrgID: s.cfg.OrgID, Actor: audit.User(id, pid), Transport: audit.TransportUI, Action: audit.ActionUserCreated,
		Resource: audit.Resource{Type: audit.ResourceUser, PublicID: pid, Name: name},
		Diff:     audit.Created(created{Login: login, Name: name, Email: email, Role: role}),
		Details:  map[string]any{"source": "oidc", "method": auth.MethodOIDC},
	})
}

// syncRole gives the user the Role the identity provider maps them to (oidc.sync_role) at a sign-in or a re-check, at,
// under the lock of the last_admin check: it never lowers the last active Admin, and records user.role_sync_kept_admin
// instead (C-03.FR-32). A changed Role ends the user's sessions (C-03.FR-9); at a sign-in the new one opens after.
func (s *Service) syncRole(ctx context.Context, q Queries, u dbgen.GetIdentityUserRow, mapped string, now time.Time,
	at string) error {
	transport := audit.TransportUI
	if at == syncAtRecheck {
		transport = audit.TransportSystem
	}
	admins, err := q.LockActiveAdmins(ctx, s.cfg.OrgID)
	if err != nil {
		return fmt.Errorf("lock the active admins: %w", err)
	}
	cur, err := q.LockUser(ctx, dbgen.LockUserParams{OrgID: s.cfg.OrgID, ID: u.ID})
	if err != nil {
		return fmt.Errorf("lock the user %s: %w", u.PublicID, err)
	}
	if cur.Role == mapped {
		return nil
	}
	resource := audit.Resource{Type: audit.ResourceUser, PublicID: u.PublicID, Name: u.Name}
	if cur.Role == auth.RoleAdmin && cur.Status == "active" && len(admins) <= 1 {
		return s.cfg.Audit.Record(ctx, q, audit.Entry{
			OrgID: s.cfg.OrgID, Actor: audit.System, Transport: transport, Action: ActionRoleSyncKeptAdmin,
			Resource: resource, Details: map[string]any{"mapped_role": mapped, "at": at},
		})
	}
	if _, err := q.SetRole(ctx, dbgen.SetRoleParams{OrgID: s.cfg.OrgID, ID: u.ID, Role: mapped, Now: now}); err != nil {
		return fmt.Errorf("change the Role of %s: %w", u.PublicID, err)
	}
	ended, err := q.EndUserSessions(ctx, dbgen.EndUserSessionsParams{OrgID: s.cfg.OrgID, UserID: u.ID, Now: now,
		EndReason: optText(endReasonRoleChange)})
	if err != nil {
		return fmt.Errorf("end the sessions of %s: %w", u.PublicID, err)
	}
	return s.cfg.Audit.Record(ctx, q, audit.Entry{
		OrgID: s.cfg.OrgID, Actor: audit.System, Transport: transport, Action: audit.ActionUserRoleChanged,
		Resource: resource, Diff: []audit.Change{{Pointer: "/role", Before: cur.Role, After: mapped}},
		Details: map[string]any{"source": "oidc_sync", "at": at, "sessions_ended": ended},
	})
}

// refuse records a refused sign-in — session.oidc_refused with its reason, and muster_login_failures_total{method=
// "oidc"} — and sends the browser to the sign-in page with the error.
func (s *Service) refuse(ctx context.Context, cb Callback, r *refusal) (Outcome, string, error) {
	metrics.LoginFailures.With(auth.MethodOIDC).Inc()
	r.details["reason"] = r.code
	e := audit.Entry{OrgID: s.cfg.OrgID, Actor: audit.System, Transport: audit.TransportUI,
		Action: ActionSignInRefused, Details: r.details, SourceAddress: cb.Address}
	if r.user != nil {
		e.Resource = audit.Resource{Type: audit.ResourceUser, PublicID: r.user.PublicID, Name: r.user.Name}
	}
	if err := s.cfg.Audit.Record(ctx, s.cfg.Store, e); err != nil {
		return Outcome{}, "audit", err
	}
	return failure(r.code), "", nil
}

// The groups an Audit log entry lists are bounded, since the claim comes from outside.
const (
	maxAuditGroups     = 100
	maxAuditGroupBytes = 200
)

// auditGroups are the groups of a refusal as the Audit log records them: at most maxAuditGroups, each cut to
// maxAuditGroupBytes; a cut list ends with "…".
func auditGroups(groups []string) []string {
	out := make([]string, 0, min(len(groups), maxAuditGroups+1))
	for i, g := range groups {
		if i == maxAuditGroups {
			out = append(out, "…")
			break
		}
		if len(g) > maxAuditGroupBytes {
			g = strings.ToValidUTF8(g[:maxAuditGroupBytes], "")
		}
		out = append(out, g)
	}
	return out
}

// roleRank orders the Roles: when groups map to several, the highest wins (C-03.FR-5).
var roleRank = map[string]int{auth.RoleViewer: 1, auth.RoleResponder: 2, auth.RoleAdmin: 3}

// MapRole maps groups to a Role: the highest Role of the mappings that name one of the groups, else the Role for
// unmatched users; ok is false when that is none.
func MapRole(groups []string, mappings []GroupMapping, unmatched string) (string, bool) {
	best := ""
	for _, m := range mappings {
		if slices.Contains(groups, m.Group) && roleRank[m.Role] > roleRank[best] {
			best = m.Role
		}
	}
	if best != "" {
		return best, true
	}
	if unmatched == UnmatchedNone || roleRank[unmatched] == 0 {
		return "", false
	}
	return unmatched, true
}

// assertsMFA reports whether amr (RFC 8176) asserts multi-factor authentication: mfa, or a knowledge factor with a
// possession or inherence factor.
func assertsMFA(amr []string) bool {
	if slices.Contains(amr, "mfa") {
		return true
	}
	knowledge := slices.ContainsFunc(amr, func(m string) bool { return m == "pwd" || m == "pin" || m == "kba" })
	second := slices.ContainsFunc(amr, func(m string) bool {
		return slices.Contains([]string{"otp", "hwk", "swk", "sms", "tel", "sc", "fpt", "face", "iris", "retina", "vbm"}, m)
	})
	return knowledge && second
}

// loginOf is the login of a new account: preferred_username, else the email, else sub — the first that is a valid
// login — and otherwise a name derived from sub.
func loginOf(idt IDToken) string {
	for _, c := range []string{idt.PreferredUsername, idt.Email, idt.Subject} {
		if c = strings.TrimSpace(c); validLogin(c) {
			return c
		}
	}
	sum := sha256.Sum256([]byte(idt.Subject))
	return "oidc-" + hex.EncodeToString(sum[:6])
}

func validLogin(login string) bool {
	return login != "" && utf8.ValidString(login) && utf8.RuneCountInString(login) <= maxLoginLength &&
		strings.IndexFunc(login, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0 &&
		!strings.HasPrefix(strings.ToLower(login), deletedLoginPrefix)
}

func validEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > maxEmailLength {
		return ""
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return ""
	}
	return email
}

// printable keeps an error code from outside short and free of control characters.
func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.ToValidUTF8(s, "")
}

// PruneQueries are the deletes of short-lived pruning; *dbgen.Queries implements them.
type PruneQueries interface {
	PruneAuthRequests(ctx context.Context, arg dbgen.PruneAuthRequestsParams) (int64, error)
}

// Pruner deletes the OIDC redirects that expired AuthRequestPruneAfter ago, for the short_lived_pruning Leader task.
type Pruner struct {
	q PruneQueries
}

// NewPruner returns the Pruner over q.
func NewPruner(q PruneQueries) Pruner {
	return Pruner{q: q}
}

// AuthRequests deletes at most limit requests of the Organization that expired AuthRequestPruneAfter before now.
func (p Pruner) AuthRequests(ctx context.Context, orgID int64, now time.Time, limit int32) (int64, error) {
	n, err := p.q.PruneAuthRequests(ctx, dbgen.PruneAuthRequestsParams{
		OrgID: orgID, Before: now.Add(-AuthRequestPruneAfter), BatchSize: limit,
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired OIDC requests: %w", err)
	}
	return n, nil
}
