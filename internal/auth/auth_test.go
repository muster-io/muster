// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package auth

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/auth/dbgen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
)

const orgID = 7

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

type fakeUser struct {
	id                int64
	publicID, name    string
	login, role       string
	status            string
	hash              string
	oidc              bool
	totp              bool
	lastSignIn        time.Time
	passwordChangedAt time.Time
}

type fakeSession struct {
	dbgen.CreateSessionParams
	id         int64
	lastUsedAt time.Time
	endedAt    time.Time
	endReason  string
}

type fakeThrottle struct {
	failures     int64
	blockedUntil time.Time
}

// fakeStore keeps the rows of the package in memory; fail makes the named method fail.
type fakeStore struct {
	users     []*fakeUser
	sessions  []*fakeSession
	throttles map[string]*fakeThrottle
	audit     []auditdb.InsertAuditEntryParams
	roles     []dbgen.RolePermission
	fail      map[string]error
	touches   int
	// policy is the Organization's TOTP policy.
	policy string
	// lockedAccount is the account subject whose attempt lock another attempt holds.
	lockedAccount string
}

func newFakeStore(users ...*fakeUser) *fakeStore {
	return &fakeStore{users: users, throttles: map[string]*fakeThrottle{}, fail: map[string]error{}}
}

func (s *fakeStore) err(method string) error {
	return s.fail[method]
}

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	if err := s.err("InTx"); err != nil {
		return err
	}
	return f(s)
}

func (s *fakeStore) ListRolePermissions(context.Context) ([]dbgen.RolePermission, error) {
	return s.roles, s.err("ListRolePermissions")
}

func (s *fakeStore) user(id int64) *fakeUser {
	for _, u := range s.users {
		if u.id == id {
			return u
		}
	}
	return nil
}

func (s *fakeStore) GetSignInUser(_ context.Context, arg dbgen.GetSignInUserParams) (dbgen.GetSignInUserRow, error) {
	if err := s.err("GetSignInUser"); err != nil {
		return dbgen.GetSignInUserRow{}, err
	}
	for _, u := range s.users {
		if strings.EqualFold(u.login, arg.Login) && arg.OrgID == orgID {
			return dbgen.GetSignInUserRow{ID: u.id, PublicID: u.publicID, Name: u.name, Role: u.role, Status: u.status,
				PasswordHash: pgtype.Text{String: u.hash, Valid: u.hash != ""}, TotpEnrolled: u.totp,
				TotpRequired: cmp.Or(s.policy, "nobody")}, nil
		}
	}
	return dbgen.GetSignInUserRow{}, pgx.ErrNoRows
}

func (s *fakeStore) GetUserPassword(_ context.Context, arg dbgen.GetUserPasswordParams) (dbgen.GetUserPasswordRow,
	error) {
	if err := s.err("GetUserPassword"); err != nil {
		return dbgen.GetUserPasswordRow{}, err
	}
	u := s.user(arg.ID)
	return dbgen.GetUserPasswordRow{Login: u.login, PasswordHash: pgtype.Text{String: u.hash, Valid: u.hash != ""},
		HasOidcIdentity: u.oidc}, nil
}

func (s *fakeStore) MarkSignedIn(_ context.Context, arg dbgen.MarkSignedInParams) error {
	if err := s.err("MarkSignedIn"); err != nil {
		return err
	}
	s.user(arg.ID).lastSignIn = arg.Now
	return nil
}

func (s *fakeStore) SetPassword(_ context.Context, arg dbgen.SetPasswordParams) (int64, error) {
	if err := s.err("SetPassword"); err != nil {
		return 0, err
	}
	u := s.user(arg.ID)
	if u.oidc {
		return 0, nil
	}
	u.hash, u.passwordChangedAt = arg.PasswordHash.String, arg.Now
	return 1, nil
}

func (s *fakeStore) CreateSession(_ context.Context, arg dbgen.CreateSessionParams) (int64, error) {
	if err := s.err("CreateSession"); err != nil {
		return 0, err
	}
	id := int64(len(s.sessions) + 1)
	s.sessions = append(s.sessions, &fakeSession{CreateSessionParams: arg, id: id, lastUsedAt: arg.CreatedAt})
	return id, nil
}

func (s *fakeStore) GetSessionByToken(_ context.Context, arg dbgen.GetSessionByTokenParams) (
	dbgen.GetSessionByTokenRow, error) {
	if err := s.err("GetSessionByToken"); err != nil {
		return dbgen.GetSessionByTokenRow{}, err
	}
	for _, ss := range s.sessions {
		if bytes.Equal(ss.TokenHash, arg.TokenHash) {
			u := s.user(ss.UserID)
			return dbgen.GetSessionByTokenRow{
				ID: ss.id, PublicID: ss.PublicID, UserID: ss.UserID, State: ss.State, Method: ss.Method,
				Address: ss.Address, UserAgent: ss.UserAgent, CreatedAt: ss.CreatedAt, LastUsedAt: ss.lastUsedAt,
				IdleExpiresAt: ss.IdleExpiresAt, ExpiresAt: ss.ExpiresAt,
				EndedAt:      pgtype.Timestamptz{Time: ss.endedAt, Valid: !ss.endedAt.IsZero()},
				EndReason:    pgtype.Text{String: ss.endReason, Valid: ss.endReason != ""},
				UserPublicID: u.publicID, UserName: u.name, UserRole: u.role, UserStatus: u.status,
			}, nil
		}
	}
	return dbgen.GetSessionByTokenRow{}, pgx.ErrNoRows
}

func (s *fakeStore) TouchSession(_ context.Context, arg dbgen.TouchSessionParams) error {
	if err := s.err("TouchSession"); err != nil {
		return err
	}
	s.touches++
	for _, ss := range s.sessions {
		if ss.id == arg.ID {
			ss.lastUsedAt, ss.IdleExpiresAt = arg.Now, arg.IdleExpiresAt
		}
	}
	return nil
}

func (s *fakeStore) end(match func(*fakeSession) bool, now time.Time, reason string) int64 {
	var n int64
	for _, ss := range s.sessions {
		if ss.endedAt.IsZero() && match(ss) {
			ss.endedAt, ss.endReason = now, reason
			n++
		}
	}
	return n
}

func (s *fakeStore) EndSession(_ context.Context, arg dbgen.EndSessionParams) (int64, error) {
	if err := s.err("EndSession"); err != nil {
		return 0, err
	}
	return s.end(func(ss *fakeSession) bool { return ss.id == arg.ID }, arg.Now, arg.EndReason.String), nil
}

func (s *fakeStore) EndUserSessions(_ context.Context, arg dbgen.EndUserSessionsParams) (int64, error) {
	if err := s.err("EndUserSessions"); err != nil {
		return 0, err
	}
	return s.end(func(ss *fakeSession) bool { return ss.UserID == arg.UserID }, arg.Now, arg.EndReason.String), nil
}

func (s *fakeStore) TryLockSignInSubject(_ context.Context, arg dbgen.TryLockSignInSubjectParams) (bool, error) {
	if err := s.err("TryLockSignInSubject"); err != nil {
		return false, err
	}
	return arg.Subject != s.lockedAccount, nil
}

func (s *fakeStore) CompleteSecondFactor(_ context.Context, arg dbgen.CompleteSecondFactorParams) (int64, error) {
	if err := s.err("CompleteSecondFactor"); err != nil {
		return 0, err
	}
	for _, ss := range s.sessions {
		if ss.id == arg.ID && ss.State == string(StateTOTPRequired) && ss.endedAt.IsZero() {
			ss.State = string(StateActive)
			return 1, nil
		}
	}
	return 0, nil
}

func (s *fakeStore) LiveSessions(_ context.Context, arg dbgen.LiveSessionsParams) ([]int64, error) {
	if err := s.err("LiveSessions"); err != nil {
		return nil, err
	}
	out := []int64{}
	for _, ss := range s.sessions {
		u := s.user(ss.UserID)
		if slices.Contains(arg.Ids, ss.id) && ss.State == string(StateActive) && ss.endedAt.IsZero() &&
			ss.IdleExpiresAt.After(arg.Now) && ss.ExpiresAt.After(arg.Now) && u.status == "active" {
			out = append(out, ss.id)
		}
	}
	return out, nil
}

func (s *fakeStore) EndOtherUserSessions(_ context.Context, arg dbgen.EndOtherUserSessionsParams) (int64, error) {
	if err := s.err("EndOtherUserSessions"); err != nil {
		return 0, err
	}
	return s.end(func(ss *fakeSession) bool { return ss.UserID == arg.UserID && ss.id != arg.KeepID }, arg.Now,
		arg.EndReason.String), nil
}

func (s *fakeStore) ListUserSessions(_ context.Context, arg dbgen.ListUserSessionsParams) (
	[]dbgen.ListUserSessionsRow, error) {
	if err := s.err("ListUserSessions"); err != nil {
		return nil, err
	}
	var out []dbgen.ListUserSessionsRow
	for _, ss := range slices.Backward(s.sessions) {
		if ss.UserID == arg.UserID && ss.endedAt.IsZero() && ss.IdleExpiresAt.After(arg.Now) &&
			ss.ExpiresAt.After(arg.Now) {
			out = append(out, dbgen.ListUserSessionsRow{ID: ss.id, PublicID: ss.PublicID, Method: ss.Method,
				Address: ss.Address, UserAgent: ss.UserAgent, CreatedAt: ss.CreatedAt, LastUsedAt: ss.lastUsedAt})
		}
	}
	return out, nil
}

func (s *fakeStore) GetThrottles(_ context.Context, arg dbgen.GetThrottlesParams) ([]dbgen.GetThrottlesRow, error) {
	if err := s.err("GetThrottles"); err != nil {
		return nil, err
	}
	var out []dbgen.GetThrottlesRow
	for _, k := range []struct{ kind, subj string }{{subjectAccount, arg.Account}, {subjectAddress, arg.Address}} {
		if t, ok := s.throttles[k.kind+"|"+k.subj]; ok {
			out = append(out, dbgen.GetThrottlesRow{SubjectKind: k.kind, ConsecutiveFailures: t.failures,
				BlockedUntil: pgtype.Timestamptz{Time: t.blockedUntil, Valid: !t.blockedUntil.IsZero()}})
		}
	}
	return out, nil
}

func (s *fakeStore) RecordSignInFailure(_ context.Context, arg dbgen.RecordSignInFailureParams) (int64, error) {
	if err := s.err("RecordSignInFailure"); err != nil {
		return 0, err
	}
	k := arg.SubjectKind + "|" + arg.Subject
	t, ok := s.throttles[k]
	if !ok {
		t = &fakeThrottle{}
		s.throttles[k] = t
	}
	t.failures++
	return t.failures, nil
}

func (s *fakeStore) BlockSignIn(_ context.Context, arg dbgen.BlockSignInParams) error {
	if err := s.err("BlockSignIn"); err != nil {
		return err
	}
	s.throttles[arg.SubjectKind+"|"+arg.Subject].blockedUntil = arg.BlockedUntil.Time
	return nil
}

func (s *fakeStore) ResetSignInThrottles(_ context.Context, arg dbgen.ResetSignInThrottlesParams) error {
	if err := s.err("ResetSignInThrottles"); err != nil {
		return err
	}
	delete(s.throttles, subjectAccount+"|"+arg.Account)
	delete(s.throttles, subjectAddress+"|"+arg.Address)
	return nil
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.err("InsertAuditEntry"); err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

func (s *fakeStore) actions() []string {
	var out []string
	for _, a := range s.audit {
		out = append(out, a.Action)
	}
	return out
}

// keyStore is keyring_state in memory, enough to make a Keyring's first key active.
type keyStore struct {
	keyring.Store
	row *kdb.GetKeyringStateRow
}

func (s *keyStore) GetKeyringState(context.Context) (kdb.GetKeyringStateRow, error) {
	if s.row == nil {
		return kdb.GetKeyringStateRow{}, pgx.ErrNoRows
	}
	return *s.row, nil
}

func (s *keyStore) CreateKeyringState(_ context.Context, arg kdb.CreateKeyringStateParams) (int64, error) {
	s.row = &kdb.GetKeyringStateRow{ActiveKeyID: arg.ActiveKeyID, ActivatedAt: arg.ActivatedAt,
		CanaryCiphertext: arg.CanaryCiphertext, CanaryKeyID: arg.ActiveKeyID}
	return 1, nil
}

func activeKeyring(t *testing.T, fill byte) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{fill}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(t.Context(), &keyStore{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(t.Context(), logging.New(&bytes.Buffer{}, logging.LevelInfo), st); err != nil {
		t.Fatal(err)
	}
	return k
}

func testRoles() Roles {
	return Roles{
		RoleAdmin:     {"alert-groups:read", "users:read", "users:write"},
		RoleResponder: {"alert-groups:acknowledge", "alert-groups:read"},
		RoleViewer:    {"alert-groups:read"},
	}
}

type harness struct {
	svc   *Service
	store *fakeStore
	clock *clock.Manual
	log   *bytes.Buffer
	alice *fakeUser
}

const alicePassword = "correct horse battery"

func newHarness(t *testing.T) *harness {
	t.Helper()
	hash, err := HashPassword(t.Context(), alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	alice := &fakeUser{id: 1, publicID: "SRAAAAAAAAAAAA", name: "Alice", login: "Alice@Example.org",
		role: RoleResponder, status: "active", hash: hash}
	store := newFakeStore(alice)
	c := clock.NewManual(t0)
	var log bytes.Buffer
	w := audit.NewWriter(logging.New(&log, logging.LevelInfo), c)
	return &harness{
		svc: NewService(orgID, store, activeKeyring(t, 'k'), w, c, testRoles()), store: store, clock: c,
		log: &log, alice: alice,
	}
}

var addr = netip.MustParseAddr("192.0.2.10")

func (h *harness) signIn(t *testing.T, login, password string) (Session, error) {
	t.Helper()
	return h.svc.SignIn(t.Context(), SignInRequest{Login: login, Password: password, Address: addr,
		UserAgent: "test-agent"})
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword(t.Context(), "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("hash %q does not record the argon2id parameters", hash)
	}
	other, _ := HashPassword(t.Context(), "a long enough password")
	if other == hash {
		t.Error("two hashes of one password are equal: the salt is not random")
	}
	for pw, want := range map[string]bool{"a long enough password": true, "a long enough passworD": false, "": false} {
		if ok, err := VerifyPassword(t.Context(), hash, pw); err != nil || ok != want {
			t.Errorf("VerifyPassword(%q) = %v, %v; want %v", pw, ok, err, want)
		}
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=18$m=19456,t=2,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=9999999,t=2,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=19456,t=0,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$mx$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=19456,t=2,p=1$!!$a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$!!",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$a2V5",
	} {
		if _, err := VerifyPassword(t.Context(), bad, "x"); !errors.Is(err, errMalformedHash) {
			t.Errorf("VerifyPassword(%q) = %v, want errMalformedHash", bad, err)
		}
	}
}

func TestPasswordLength(t *testing.T) {
	for pw, want := range map[string]error{
		"elevenchars": ErrPasswordTooShort, "twelve chars": nil, "пароль-длина": nil, "пароль-длин": ErrPasswordTooShort,
	} {
		if err := CheckPasswordLength(pw); !errors.Is(err, want) {
			t.Errorf("CheckPasswordLength(%q) = %v, want %v", pw, err, want)
		}
	}
}

func TestThrottleDelay(t *testing.T) {
	want := []time.Duration{0, 0, 0, 1, 2, 4, 8, 16, 32, 60, 60, 60}
	for n, w := range want {
		if got := ThrottleDelay(int64(n)); got != w*time.Second {
			t.Errorf("ThrottleDelay(%d) = %v, want %v", n, got, w*time.Second)
		}
	}
	if (&ThrottledError{RetryAfter: 1500 * time.Millisecond}).Seconds() != 2 ||
		(&ThrottledError{RetryAfter: time.Millisecond}).Seconds() != 1 {
		t.Error("Retry-After is not rounded up to whole seconds")
	}
}

// TestClientAddress is C-03.FR-4's source address: without trusted proxies the socket address, whatever
// X-Forwarded-For says; behind a trusted proxy the first address from the right outside the trusted networks.
func TestClientAddress(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	for _, c := range []struct {
		name, remote string
		xff          []string
		trusted      []netip.Prefix
		want         string
	}{
		{"no proxies", "203.0.113.5:4000", []string{"198.51.100.1"}, nil, "203.0.113.5"},
		{"peer not trusted", "203.0.113.5:4000", []string{"198.51.100.1"}, trusted, "203.0.113.5"},
		{"trusted proxy", "10.0.0.2:4000", []string{"198.51.100.1"}, trusted, "198.51.100.1"},
		{"forged left-most entry", "10.0.0.2:4000", []string{"6.6.6.6, 198.51.100.1"}, trusted, "198.51.100.1"},
		{"chain of proxies", "10.0.0.2:4000", []string{"6.6.6.6, 198.51.100.1", "10.1.1.1"}, trusted, "198.51.100.1"},
		{"all trusted", "10.0.0.2:4000", []string{"10.3.3.3, 10.1.1.1"}, trusted, "10.3.3.3"},
		{"garbage stops the walk", "10.0.0.2:4000", []string{"198.51.100.1, nonsense, 10.1.1.1"}, trusted, "10.1.1.1"},
		{"no header", "10.0.0.2:4000", nil, trusted, "10.0.0.2"},
		{"IPv6 peer", "[fd00::1]:4000", []string{"2001:db8::7"}, trusted, "2001:db8::7"},
		{"mapped IPv4", "[::ffff:203.0.113.5]:4000", nil, nil, "203.0.113.5"},
		{"bare address", "203.0.113.9", nil, nil, "203.0.113.9"},
	} {
		got := ClientAddress(c.remote, c.xff, c.trusted)
		if got.String() != c.want {
			t.Errorf("%s: ClientAddress = %s, want %s", c.name, got, c.want)
		}
	}
	if ClientAddress("not an address", nil, trusted).IsValid() {
		t.Error("an unparsable peer gave an address")
	}
}

func TestIdentityContext(t *testing.T) {
	ctx := t.Context()
	if _, ok := IdentityFrom(ctx); ok {
		t.Error("an empty context has an identity")
	}
	if ClientAddressFrom(ctx).IsValid() {
		t.Error("an empty context has an address")
	}
	id := &Identity{Session: Session{User: Principal{ID: 3, PublicID: "SRBBBBBBBBBBBB"}},
		Permissions: []Permission{"users:read"}}
	ctx = WithClientAddress(WithIdentity(ctx, id), addr)
	got, ok := IdentityFrom(ctx)
	if !ok || got != id || ClientAddressFrom(ctx) != addr {
		t.Fatal("the context does not carry the identity and the address")
	}
	if !got.Can("users:read") || got.Can("users:write") {
		t.Error("Can does not follow the Permissions")
	}
	if a := got.Actor(); a.Kind != audit.ActorUser || a.ID != 3 || a.PublicID != "SRBBBBBBBBBBBB" {
		t.Errorf("Actor = %+v", a)
	}
}

func TestLoadRoles(t *testing.T) {
	s := newFakeStore()
	s.roles = []dbgen.RolePermission{{Role: "admin", Permission: "users:write"}, {Role: "admin", Permission: "alerts:read"},
		{Role: "responder", Permission: "alerts:read"}, {Role: "viewer", Permission: "alerts:read"}}
	r, err := LoadRoles(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Permissions(RoleAdmin); !slices.Equal(got, []Permission{"alerts:read", "users:write"}) {
		t.Errorf("admin = %v, want sorted", got)
	}
	if !r.Has(RoleAdmin, "users:write") || r.Has(RoleViewer, "users:write") || r.Has("nobody", "alerts:read") {
		t.Error("Has does not follow the allocation")
	}
	s.roles = s.roles[:3]
	if _, err := LoadRoles(t.Context(), s); err == nil || !strings.Contains(err.Error(), "viewer") {
		t.Errorf("a role without permissions: %v", err)
	}
	s.fail["ListRolePermissions"] = errors.New("boom")
	if _, err := LoadRoles(t.Context(), s); err == nil {
		t.Error("a failed read is not an error")
	}
}

// TestSignIn is C-03.FR-3 and FR-24: the login in any letter case and the right password open an active session
// with the Permissions of the Role, reset the throttle and write session.signed_in; the cookie authenticates.
func TestSignIn(t *testing.T) {
	h := newHarness(t)
	h.store.throttles["address|192.0.2.10"] = &fakeThrottle{failures: 2}
	sess, err := h.signIn(t, "ALICE@example.ORG", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	if sess.State != StateActive || sess.Method != MethodLocal || sess.User.Name != "Alice" ||
		!strings.HasPrefix(sess.PublicID, "SN") {
		t.Errorf("session = %+v", sess)
	}
	if !sess.IdleExpiresAt.Equal(t0.Add(12*time.Hour)) || !sess.ExpiresAt.Equal(t0.Add(7*24*time.Hour)) {
		t.Errorf("expiry %v / %v", sess.IdleExpiresAt, sess.ExpiresAt)
	}
	if len(h.store.throttles) != 0 {
		t.Errorf("the throttle was not reset: %v", h.store.throttles)
	}
	stored := h.store.sessions[0]
	if sum := sha256.Sum256(sess.token); !bytes.Equal(stored.TokenHash, sum[:]) {
		t.Error("the database does not hold the SHA-256 of the token")
	}
	if stored.UserAgent.String != "test-agent" || *stored.Address != addr || !h.alice.lastSignIn.Equal(t0) {
		t.Errorf("stored %+v, last sign-in %v", stored, h.alice.lastSignIn)
	}
	if got := h.store.actions(); !slices.Equal(got, []string{audit.ActionSignedIn}) {
		t.Errorf("audit = %v", got)
	}
	if !strings.Contains(h.log.String(), `"event":"audit_entry"`) {
		t.Error("the entry was not copied to the log")
	}
	if got := h.svc.Permissions(sess); !slices.Equal(got, testRoles()[RoleResponder]) {
		t.Errorf("Permissions = %v", got)
	}
	if !slices.Equal(h.svc.Roles()[RoleViewer], testRoles()[RoleViewer]) {
		t.Error("Roles is not the allocation")
	}
	again, err := h.svc.Authenticate(t.Context(), sess.Cookie())
	if err != nil || again.ID != sess.ID || again.User.Role != RoleResponder {
		t.Fatalf("Authenticate = %+v, %v", again, err)
	}
	limited := sess
	limited.State = StateTOTPRequired
	if len(h.svc.Permissions(limited)) != 0 {
		t.Error("a limited session has Permissions")
	}
}

// TestSignInRefusals: a wrong password, an unknown login, a disabled account and an account without a password all
// answer ErrInvalidCredentials and write session.sign_in_failed.
func TestSignInRefusals(t *testing.T) {
	h := newHarness(t)
	disabled := &fakeUser{id: 2, publicID: "SRCCCCCCCCCCCC", name: "Bob", login: "bob", role: RoleViewer,
		status: "disabled", hash: h.alice.hash}
	nopass := &fakeUser{id: 3, publicID: "SRDDDDDDDDDDDD", name: "Carol", login: "carol", role: RoleViewer,
		status: "active"}
	h.store.users = append(h.store.users, disabled, nopass)
	for _, c := range []struct{ login, password string }{
		{"alice@example.org", "wrong password"}, {"nobody@example.org", alicePassword}, {"bob", alicePassword},
		{"carol", alicePassword},
	} {
		if _, err := h.signIn(t, c.login, c.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v, want ErrInvalidCredentials", c.login, err)
		}
		h.store.throttles = map[string]*fakeThrottle{}
	}
	if len(h.store.sessions) != 0 {
		t.Error("a refused sign-in opened a session")
	}
	if n := len(h.store.audit); n != 4 {
		t.Fatalf("%d audit entries, want 4", n)
	}
	for i, a := range h.store.audit {
		if a.Action != audit.ActionSignInFailed || a.ActorKind != "system" || a.Transport != "ui" {
			t.Errorf("entry %d = %+v", i, a)
		}
	}
	if h.store.audit[0].ResourcePublicID.String != h.alice.publicID || h.store.audit[1].ResourcePublicID.Valid {
		t.Error("the entry of a known account names it and the entry of an unknown login names nothing")
	}
}

// TestSignInThrottle is C-03.FR-4 and C-03.AC-10 with a manual clock: after 3 failures an attempt waits 1, 2, 4 s;
// an attempt that comes too early is refused without being evaluated; six evaluated failures raise the metric by six
// and write six entries; a success resets the count.
func TestSignInThrottle(t *testing.T) {
	h := newHarness(t)
	counter := metrics.LoginFailures.With(MethodLocal)
	before := counter.Get()
	var waits []int
	for evaluated := 0; evaluated < 6; {
		_, err := h.signIn(t, "alice@example.org", "wrong")
		if te, ok := errors.AsType[*ThrottledError](err); ok {
			waits = append(waits, te.Seconds())
			h.clock.Advance(te.RetryAfter)
			continue
		}
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt: %v", err)
		}
		evaluated++
	}
	if !slices.Equal(waits, []int{1, 2, 4}) {
		t.Errorf("waits = %v, want [1 2 4]", waits)
	}
	if got := counter.Get() - before; got != 6 {
		t.Errorf("muster_login_failures_total{method=local} rose by %d, want 6", got)
	}
	if n := len(h.store.actions()); n != 6 {
		t.Errorf("%d session.sign_in_failed entries, want 6", n)
	}
	if th := h.store.throttles["account|"+accountSubject("alice@example.org")]; th.failures != 6 ||
		!th.blockedUntil.Equal(h.clock.Now().Add(8*time.Second)) {
		t.Errorf("account throttle = %+v", th)
	}
	// The block of the sixth failure is 8 s; the right password before it is refused, after it succeeds.
	if _, err := h.signIn(t, "alice@example.org", alicePassword); err == nil {
		t.Fatal("a blocked attempt was evaluated")
	}
	h.clock.Advance(8 * time.Second)
	if _, err := h.signIn(t, "alice@example.org", alicePassword); err != nil {
		t.Fatal(err)
	}
	if len(h.store.throttles) != 0 {
		t.Error("a success did not reset the throttle")
	}
}

// TestThrottlePerAddress: failures for different accounts from one address throttle the address.
func TestThrottlePerAddress(t *testing.T) {
	h := newHarness(t)
	for i := range 3 {
		if _, err := h.signIn(t, "user"+string(rune('a'+i)), "x"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	_, err := h.signIn(t, "someone-else", "x")
	if _, ok := errors.AsType[*ThrottledError](err); !ok {
		t.Errorf("the fourth attempt from the address: %v, want throttled", err)
	}
	if _, err := h.svc.SignIn(t.Context(), SignInRequest{Login: "alice@example.org", Password: alicePassword}); err != nil {
		t.Errorf("an attempt without an address is throttled by account only: %v", err)
	}
}

// TestSessionExpiry is C-03.FR-9 with a manual clock: use moves the idle expiry at most once a minute; a session
// unused for auth.session_idle_timeout, or older than auth.session_lifetime, ends as expired.
func TestSessionExpiry(t *testing.T) {
	h := newHarness(t)
	sess, err := h.signIn(t, "alice@example.org", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(30 * time.Second)
	if _, err := h.svc.Authenticate(t.Context(), sess.Cookie()); err != nil || h.store.touches != 0 {
		t.Fatalf("use within a minute: %v, %d writes", err, h.store.touches)
	}
	h.clock.Advance(time.Minute)
	got, err := h.svc.Authenticate(t.Context(), sess.Cookie())
	if err != nil || h.store.touches != 1 || !got.IdleExpiresAt.Equal(h.clock.Now().Add(SessionIdleTimeout)) {
		t.Fatalf("use after a minute: %v, %d writes, idle expiry %v", err, h.store.touches, got.IdleExpiresAt)
	}
	h.clock.Advance(SessionIdleTimeout)
	if _, err := h.svc.Authenticate(t.Context(), sess.Cookie()); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("after the idle timeout: %v", err)
	}
	if s := h.store.sessions[0]; s.endReason != EndExpired {
		t.Errorf("end reason %q", s.endReason)
	}
	if _, err := h.svc.Authenticate(t.Context(), sess.Cookie()); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("an expired session read again: %v", err)
	}

	// Used every 11 hours, the session lives until its lifetime ends.
	h.clock.Set(t0)
	sess, err = h.signIn(t, "alice@example.org", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	for h.clock.Now().Add(11 * time.Hour).Before(t0.Add(SessionLifetime)) {
		h.clock.Advance(11 * time.Hour)
		if _, err := h.svc.Authenticate(t.Context(), sess.Cookie()); err != nil {
			t.Fatalf("at %v: %v", h.clock.Now(), err)
		}
	}
	h.clock.Set(t0.Add(SessionLifetime))
	if _, err := h.svc.Authenticate(t.Context(), sess.Cookie()); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("at the end of the lifetime: %v", err)
	}
}

func TestAuthenticateRefusals(t *testing.T) {
	h := newHarness(t)
	sess, err := h.signIn(t, "alice@example.org", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []string{"", "not base64!", "c2hvcnQ", strings.Repeat("A", 43)} {
		if _, err := h.svc.Authenticate(t.Context(), cookie); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("cookie %q: %v", cookie, err)
		}
	}
	h.alice.status = "disabled"
	if _, err := h.svc.Authenticate(t.Context(), sess.Cookie()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("disabled user: %v", err)
	}
	h.alice.status = "active"
	if err := h.svc.SignOut(t.Context(), sess, addr); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Authenticate(t.Context(), sess.Cookie()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("signed out: %v", err)
	}
	h.store.fail["GetSessionByToken"] = errors.New("db down")
	if _, err := h.svc.Authenticate(t.Context(), sess.Cookie()); err == nil || errors.Is(err, ErrUnauthenticated) {
		t.Errorf("a failed read: %v", err)
	}
}

// TestCSRF is C-03.FR-9's token: derived from the session with the Keyring, never stored, refused for another
// session, another key or a malformed header.
func TestCSRF(t *testing.T) {
	h := newHarness(t)
	sess, _ := h.signIn(t, "alice@example.org", alicePassword)
	other, _ := h.signIn(t, "alice@example.org", alicePassword)
	token, err := h.svc.CSRFToken(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !h.svc.CheckCSRF(sess, token) {
		t.Fatal("the session's own token is refused")
	}
	again, _ := h.svc.Authenticate(t.Context(), sess.Cookie())
	if !h.svc.CheckCSRF(again, token) {
		t.Error("the token does not survive a new request")
	}
	otherToken, _ := h.svc.CSRFToken(other)
	keyID, _, _ := strings.Cut(token, ".")
	for _, bad := range []string{"", otherToken, "nodot", keyID + ".!!", "k-unknown." + strings.Split(token, ".")[1],
		token + "x"} {
		if h.svc.CheckCSRF(sess, bad) {
			t.Errorf("CheckCSRF(%q) passed", bad)
		}
	}
	if h.svc.CheckCSRF(Session{}, token) {
		t.Error("a session without a token passed")
	}
	foreign := NewService(orgID, h.store, activeKeyring(t, 'z'), nil, h.clock, testRoles())
	if foreign.CheckCSRF(sess, token) {
		t.Error("a token of another master key passed")
	}
}

// TestSignOut: signing out ends the session, signing out everywhere ends all of the user's sessions, each with its
// reason and Audit log entry.
func TestSignOut(t *testing.T) {
	h := newHarness(t)
	a, _ := h.signIn(t, "alice@example.org", alicePassword)
	b, _ := h.signIn(t, "alice@example.org", alicePassword)
	c, _ := h.signIn(t, "alice@example.org", alicePassword)
	if err := h.svc.SignOut(t.Context(), a, addr); err != nil {
		t.Fatal(err)
	}
	list, err := h.svc.ListSessions(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || !list[1].Current || list[0].PublicID != c.PublicID || list[1].Address != addr ||
		list[1].UserAgent != "test-agent" {
		t.Errorf("sessions = %+v", list)
	}
	if err := h.svc.SignOutEverywhere(t.Context(), b, addr); err != nil {
		t.Fatal(err)
	}
	for i, s := range h.store.sessions {
		want := []string{EndSignOut, EndSignOutEverywhere, EndSignOutEverywhere}[i]
		if s.endReason != want {
			t.Errorf("session %d ended with %q, want %q", i, s.endReason, want)
		}
	}
	got := h.store.actions()
	if !slices.Equal(got[3:], []string{audit.ActionSignedOut, audit.ActionSessionsEnded}) {
		t.Errorf("audit = %v", got)
	}
	h.store.fail["ListUserSessions"] = errors.New("boom")
	if _, err := h.svc.ListSessions(t.Context(), b); err == nil {
		t.Error("a failed list is not an error")
	}
}

// TestChangePassword is C-03.FR-9 and FR-12: the current password is needed, a new one shorter than
// auth.password_min_length is refused, and a change ends the user's other sessions while this one continues.
func TestChangePassword(t *testing.T) {
	h := newHarness(t)
	a, _ := h.signIn(t, "alice@example.org", alicePassword)
	b, _ := h.signIn(t, "alice@example.org", alicePassword)
	change := func(cur, next string) error {
		return h.svc.ChangePassword(t.Context(), a, PasswordChange{Current: cur, New: next, Address: addr})
	}
	if err := change(alicePassword, "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("short: %v", err)
	}
	if err := change("wrong current pw", "a new long password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong current: %v", err)
	}
	if err := change(alicePassword, "a new long password"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := VerifyPassword(t.Context(), h.alice.hash, "a new long password"); !ok {
		t.Error("the new password is not stored")
	}
	if _, err := h.svc.Authenticate(t.Context(), a.Cookie()); err != nil {
		t.Errorf("the session that changed the password ended: %v", err)
	}
	if _, err := h.svc.Authenticate(t.Context(), b.Cookie()); !errors.Is(err, ErrUnauthenticated) ||
		h.store.sessions[1].endReason != EndPasswordChanged {
		t.Errorf("the other session: %v, %q", err, h.store.sessions[1].endReason)
	}
	last := h.store.audit[len(h.store.audit)-1]
	if last.Action != audit.ActionPasswordChanged || !strings.Contains(string(last.Diff), `"secret_changed":true`) ||
		strings.Contains(string(last.Diff)+string(last.Details), "a new long password") {
		t.Errorf("entry %s diff %s", last.Action, last.Diff)
	}
	h.alice.oidc = true
	if err := change("a new long password", "another long password"); !errors.Is(err, ErrNotLocal) {
		t.Errorf("OIDC account: %v", err)
	}
}

// TestStoreFailures: a failing query is an error that names what failed, and never a refusal.
func TestStoreFailures(t *testing.T) {
	for _, method := range []string{"GetThrottles", "GetSignInUser", "RecordSignInFailure", "InsertAuditEntry"} {
		h := newHarness(t)
		h.store.fail[method] = errors.New("db down")
		if _, err := h.signIn(t, "alice@example.org", "wrong"); err == nil || errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v", method, err)
		}
	}
	for _, method := range []string{"ResetSignInThrottles", "CreateSession", "MarkSignedIn", "InTx"} {
		h := newHarness(t)
		h.store.fail[method] = errors.New("db down")
		if _, err := h.signIn(t, "alice@example.org", alicePassword); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	h := newHarness(t)
	h.store.users[0].hash = "$argon2id$broken"
	if _, err := h.signIn(t, "alice@example.org", alicePassword); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a malformed stored hash: %v", err)
	}
	for _, method := range []string{"BlockSignIn"} {
		h := newHarness(t)
		h.store.throttles["account|"+accountSubject("alice@example.org")] = &fakeThrottle{failures: 5}
		h.store.fail[method] = errors.New("db down")
		if _, err := h.signIn(t, "alice@example.org", "wrong"); err == nil || errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v", method, err)
		}
	}
	for _, method := range []string{"TouchSession", "EndSession"} {
		h := newHarness(t)
		sess, _ := h.signIn(t, "alice@example.org", alicePassword)
		h.store.fail[method] = errors.New("db down")
		h.clock.Advance(13 * time.Hour)
		if method == "TouchSession" {
			h.clock.Set(t0.Add(2 * time.Minute))
		}
		if _, err := h.svc.Authenticate(t.Context(), sess.Cookie()); err == nil {
			t.Errorf("%s: no error", method)
		}
		if err := h.svc.SignOut(t.Context(), sess, addr); method == "EndSession" && err == nil {
			t.Error("SignOut with EndSession failing: no error")
		}
	}
	for _, method := range []string{"EndUserSessions"} {
		h := newHarness(t)
		sess, _ := h.signIn(t, "alice@example.org", alicePassword)
		h.store.fail[method] = errors.New("db down")
		if err := h.svc.SignOutEverywhere(t.Context(), sess, addr); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	for _, method := range []string{"GetUserPassword", "SetPassword", "EndOtherUserSessions"} {
		h := newHarness(t)
		sess, _ := h.signIn(t, "alice@example.org", alicePassword)
		h.store.fail[method] = errors.New("db down")
		if err := h.svc.ChangePassword(t.Context(), sess, PasswordChange{Current: alicePassword,
			New: "a new long password"}); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	h = newHarness(t)
	sess, _ := h.signIn(t, "alice@example.org", alicePassword)
	h.alice.hash = "$argon2id$broken"
	if err := h.svc.ChangePassword(t.Context(), sess, PasswordChange{Current: alicePassword,
		New: "a new long password"}); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a malformed stored hash: %v", err)
	}
	h.alice.hash = ""
	if err := h.svc.ChangePassword(t.Context(), sess, PasswordChange{Current: alicePassword,
		New: "a new long password"}); !errors.Is(err, ErrNotLocal) {
		t.Errorf("no password: %v", err)
	}
}

// TestThrottleSubjects: the account subject has a fixed size whatever the login, the same for any letter case, and an
// IPv6 source is counted by its /64.
func TestThrottleSubjects(t *testing.T) {
	if accountSubject("Alice@Example.org") != accountSubject("alice@example.org") ||
		len(accountSubject(strings.Repeat("x", 100000))) != 64 {
		t.Error("accountSubject is not a fixed-size, case-insensitive key")
	}
	for in, want := range map[string]string{"192.0.2.7": "192.0.2.7", "2001:db8:1:2:aaaa::1": "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb::9": "2001:db8:1:2::/64"} {
		if got := addressSubject(netip.MustParseAddr(in)); got != want {
			t.Errorf("addressSubject(%s) = %s, want %s", in, got, want)
		}
	}
	if addressSubject(netip.Addr{}) != "" {
		t.Error("an unknown address has a subject")
	}
}

// TestChangePasswordThrottle: wrong current passwords count in the sign-in throttle and the metric, and a change
// before the throttle allows it is refused without checking the password.
func TestChangePasswordThrottle(t *testing.T) {
	h := newHarness(t)
	sess, err := h.signIn(t, "alice@example.org", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	counter := metrics.LoginFailures.With(MethodLocal)
	before := counter.Get()
	change := func(cur string) error {
		return h.svc.ChangePassword(t.Context(), sess, PasswordChange{Current: cur, New: "a new long password",
			Address: addr})
	}
	for range 3 {
		if err := change("wrong current pw"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if err := change(alicePassword); err == nil {
		t.Fatal("a change during the block was evaluated")
	} else if _, ok := errors.AsType[*ThrottledError](err); !ok {
		t.Fatalf("during the block: %v", err)
	}
	if counter.Get()-before != 3 {
		t.Errorf("the metric rose by %d, want 3", counter.Get()-before)
	}
	h.clock.Advance(time.Second)
	if err := change(alicePassword); err != nil {
		t.Errorf("after the block: %v", err)
	}
	h.store.fail["GetThrottles"] = errors.New("db down")
	if err := change("a new long password"); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a failed throttle read: %v", err)
	}
	delete(h.store.fail, "GetThrottles")
	h.store.fail["RecordSignInFailure"] = errors.New("db down")
	h.clock.Advance(time.Minute)
	if err := change("wrong current pw"); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a failed count: %v", err)
	}
}

// TestHashingGivesUpWhenTheContextEnds: a check waiting for a hashing slot stops when its request goes away.
func TestHashingGivesUpWhenTheContextEnds(t *testing.T) {
	for range cap(hashSlots) {
		hashSlots <- struct{}{}
	}
	defer func() {
		for range cap(hashSlots) {
			<-hashSlots
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := HashPassword(ctx, "a long enough password"); !errors.Is(err, context.Canceled) {
		t.Errorf("HashPassword = %v", err)
	}
	if err := verifyDummy(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("verifyDummy = %v", err)
	}
}

// fakeFactor accepts the TOTP code "123456" and the recovery code "rc" once each.
type fakeFactor struct {
	used  map[string]bool
	err   error
	calls int
}

func (f *fakeFactor) Verify(_ context.Context, _ int64, p Proof) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	code := p.TOTPCode
	if code == "" {
		code = p.RecoveryCode
	}
	if (code != "123456" && code != "rc") || f.used[code] {
		return false, nil
	}
	if f.used == nil {
		f.used = map[string]bool{}
	}
	f.used[code] = true
	return true, nil
}

func (h *harness) withTOTP() *fakeFactor {
	f := &fakeFactor{}
	h.alice.totp = true
	h.svc.UseSecondFactor(f)
	return f
}

// TestSignInSecondStep is C-03.FR-24 and C-03.AC-13: the right password of a user with TOTP opens a session in the
// state totp_required without a code, an active one with a right code; a wrong code opens nothing, counts in the
// throttle and in muster_login_failures_total{method="totp"} and writes session.second_factor_failed. Only an active
// session resets the throttle.
func TestSignInSecondStep(t *testing.T) {
	h := newHarness(t)
	f := h.withTOTP()
	h.store.throttles["address|192.0.2.10"] = &fakeThrottle{failures: 2}
	sess, err := h.signIn(t, "alice@example.org", alicePassword)
	if err != nil || sess.State != StateTOTPRequired || len(h.svc.Permissions(sess)) != 0 {
		t.Fatalf("without a code = %+v, %v", sess, err)
	}
	if len(h.store.throttles) != 1 || f.calls != 0 {
		t.Errorf("a limited session reset the throttle %v or checked a code", h.store.throttles)
	}
	totp := metrics.LoginFailures.With(FailureTOTP)
	before := totp.Get()
	_, err = h.svc.SignIn(t.Context(), SignInRequest{Login: "alice@example.org", Password: alicePassword,
		Proof: Proof{TOTPCode: "000000"}, Address: addr})
	if !errors.Is(err, ErrInvalidCredentials) || totp.Get()-before != 1 || len(h.store.sessions) != 1 {
		t.Fatalf("a wrong code = %v, metric +%d, %d sessions", err, totp.Get()-before, len(h.store.sessions))
	}
	if th := h.store.throttles["account|"+accountSubject("alice@example.org")]; th == nil || th.failures != 1 {
		t.Errorf("the wrong code was not counted: %+v", th)
	}
	if got := h.store.actions(); !slices.Equal(got, []string{audit.ActionSignedIn, ActionSecondFactorFailed}) {
		t.Errorf("audit = %v", got)
	}
	h.clock.Advance(time.Minute)
	sess, err = h.svc.SignIn(t.Context(), SignInRequest{Login: "alice@example.org", Password: alicePassword,
		Proof: Proof{TOTPCode: "123456"}, Address: addr})
	if err != nil || sess.State != StateActive || len(h.store.throttles) != 0 {
		t.Fatalf("a right code = %+v, %v, throttles %v", sess, err, h.store.throttles)
	}
	f.err = errors.New("db down")
	if _, err := h.svc.SignIn(t.Context(), SignInRequest{Login: "alice@example.org", Password: alicePassword,
		Proof: Proof{RecoveryCode: "rc"}, Address: addr}); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a failed check: %v", err)
	}
	h.svc.UseSecondFactor(nil)
	if _, err := h.svc.SignIn(t.Context(), SignInRequest{Login: "alice@example.org", Password: alicePassword,
		Proof: Proof{TOTPCode: "123456"}, Address: addr}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("without a SecondFactor a code is right: %v", err)
	}
}

// TestSignInEnrolmentRequired is C-03.FR-10 and FR-20: under a policy that covers local users, a user without TOTP
// gets a session in the state totp_enrolment_required; under nobody an active one.
func TestSignInEnrolmentRequired(t *testing.T) {
	for policy, want := range map[string]SessionState{
		"nobody": StateActive, "local_users": StateTOTPEnrolmentRequired, "everyone": StateTOTPEnrolmentRequired,
	} {
		h := newHarness(t)
		h.store.policy = policy
		sess, err := h.signIn(t, "alice@example.org", alicePassword)
		if err != nil || sess.State != want {
			t.Errorf("%s: %s, %v; want %s", policy, sess.State, err, want)
		}
		if h.store.audit[0].Details == nil || !strings.Contains(string(h.store.audit[0].Details), string(want)) {
			t.Errorf("%s: the sign-in entry does not record the state: %s", policy, h.store.audit[0].Details)
		}
	}
}

// TestSubmitSecondFactor is C-03.AC-13: the right code or an unused recovery code makes a totp_required session
// active; a wrong one is ErrInvalidCredentials and throttled; a session that waits for nothing is ErrTOTPNotPending.
func TestSubmitSecondFactor(t *testing.T) {
	h := newHarness(t)
	h.withTOTP()
	sess, err := h.signIn(t, "alice@example.org", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := h.svc.SubmitSecondFactor(t.Context(), sess, Proof{TOTPCode: "999999"}, addr); !errors.Is(err,
			ErrInvalidCredentials) {
			t.Fatalf("a wrong code: %v", err)
		}
	}
	if _, err := h.svc.SubmitSecondFactor(t.Context(), sess, Proof{RecoveryCode: "rc"}, addr); err == nil {
		t.Fatal("a code during the block was evaluated")
	} else if _, ok := errors.AsType[*ThrottledError](err); !ok {
		t.Fatalf("during the block: %v", err)
	}
	h.clock.Advance(time.Second)
	active, err := h.svc.SubmitSecondFactor(t.Context(), sess, Proof{RecoveryCode: "rc"}, addr)
	if err != nil || active.State != StateActive || h.store.sessions[0].State != string(StateActive) {
		t.Fatalf("a recovery code = %+v, %v", active, err)
	}
	if len(h.store.throttles) != 0 {
		t.Error("a right code did not reset the throttle")
	}
	if _, err := h.svc.SubmitSecondFactor(t.Context(), active, Proof{TOTPCode: "123456"}, addr); !errors.Is(err,
		ErrTOTPNotPending) {
		t.Errorf("an active session: %v", err)
	}
	if _, err := h.svc.SubmitSecondFactor(t.Context(), sess, Proof{TOTPCode: "123456"}, addr); !errors.Is(err,
		ErrTOTPNotPending) {
		t.Errorf("a session completed meanwhile: %v", err)
	}
	for _, method := range []string{"GetUserPassword", "GetThrottles", "CompleteSecondFactor", "ResetSignInThrottles",
		"Verify"} {
		h := newHarness(t)
		f := h.withTOTP()
		sess, _ := h.signIn(t, "alice@example.org", alicePassword)
		if method == "Verify" {
			f.err = errors.New("db down")
		} else {
			h.store.fail[method] = errors.New("db down")
		}
		if _, err := h.svc.SubmitSecondFactor(t.Context(), sess, Proof{TOTPCode: "123456"}, addr); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	h = newHarness(t)
	h.withTOTP()
	sess, _ = h.signIn(t, "alice@example.org", alicePassword)
	h.store.fail["InsertAuditEntry"] = errors.New("db down")
	if _, err := h.svc.SubmitSecondFactor(t.Context(), sess, Proof{TOTPCode: "1"}, addr); err == nil ||
		errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a wrong code whose entry fails: %v", err)
	}
	h.store.fail = map[string]error{"RecordSignInFailure": errors.New("db down")}
	if _, err := h.svc.SubmitSecondFactor(t.Context(), sess, Proof{TOTPCode: "1"}, addr); err == nil ||
		errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a wrong code whose count fails: %v", err)
	}
}

// TestAttempt: a proof on the caller's own account is checked under the sign-in throttle and counted by its method.
func TestAttempt(t *testing.T) {
	h := newHarness(t)
	sess, err := h.signIn(t, "alice@example.org", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	counter := metrics.LoginFailures.With(FailureTOTP)
	before := counter.Get()
	wrong := func(context.Context) (bool, error) { return false, nil }
	for range 3 {
		if err := h.svc.Attempt(t.Context(), sess, addr, FailureTOTP, wrong); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	ran := false
	err = h.svc.Attempt(t.Context(), sess, addr, FailureTOTP, func(context.Context) (bool, error) {
		ran = true
		return true, nil
	})
	if _, ok := errors.AsType[*ThrottledError](err); !ok || ran {
		t.Fatalf("during the block: %v, ran %v", err, ran)
	}
	if counter.Get()-before != 3 {
		t.Errorf("the metric rose by %d, want 3", counter.Get()-before)
	}
	h.clock.Advance(time.Second)
	if err := h.svc.Attempt(t.Context(), sess, addr, FailureTOTP, func(context.Context) (bool, error) {
		return true, nil
	}); err != nil {
		t.Errorf("a right proof: %v", err)
	}
	h.clock.Advance(time.Minute)
	boom := errors.New("boom")
	if err := h.svc.Attempt(t.Context(), sess, addr, FailureTOTP, func(context.Context) (bool, error) {
		return false, boom
	}); !errors.Is(err, boom) {
		t.Errorf("a failed check: %v", err)
	}
	for _, method := range []string{"GetUserPassword", "GetThrottles", "RecordSignInFailure"} {
		h.store.fail = map[string]error{method: errors.New("db down")}
		if err := h.svc.Attempt(t.Context(), sess, addr, FailureTOTP, wrong); err == nil ||
			errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v", method, err)
		}
	}
	h.store.fail = map[string]error{}
	for pw, want := range map[string]bool{alicePassword: true, "wrong": false} {
		if ok, err := h.svc.CheckPassword(t.Context(), sess, pw); err != nil || ok != want {
			t.Errorf("CheckPassword(%q) = %v, %v", pw, ok, err)
		}
	}
	h.alice.hash = ""
	if ok, err := h.svc.CheckPassword(t.Context(), sess, alicePassword); ok || err != nil {
		t.Errorf("an account without a password: %v, %v", ok, err)
	}
	h.alice.hash = "$argon2id$broken"
	if _, err := h.svc.CheckPassword(t.Context(), sess, alicePassword); err == nil {
		t.Error("a malformed hash: no error")
	}
	h.store.fail["GetUserPassword"] = errors.New("db down")
	if _, err := h.svc.CheckPassword(t.Context(), sess, alicePassword); err == nil {
		t.Error("a failed read: no error")
	}
}

// TestLiveSessions: the streams' check keeps active sessions that neither ended nor expired, without touching them.
func TestLiveSessions(t *testing.T) {
	h := newHarness(t)
	a, _ := h.signIn(t, "alice@example.org", alicePassword)
	b, _ := h.signIn(t, "alice@example.org", alicePassword)
	h.alice.totp = true
	h.svc.UseSecondFactor(&fakeFactor{})
	limited, _ := h.signIn(t, "alice@example.org", alicePassword)
	if err := h.svc.SignOut(t.Context(), b, addr); err != nil {
		t.Fatal(err)
	}
	live, err := h.svc.LiveSessions(t.Context(), []int64{a.ID, b.ID, limited.ID, 99})
	if err != nil || !slices.Equal(live, []int64{a.ID}) || h.store.touches != 0 {
		t.Errorf("LiveSessions = %v, %v; touches %d", live, err, h.store.touches)
	}
	h.clock.Advance(SessionIdleTimeout)
	if live, _ := h.svc.LiveSessions(t.Context(), []int64{a.ID}); len(live) != 0 {
		t.Errorf("an idle session is live: %v", live)
	}
	h.store.fail["LiveSessions"] = errors.New("db down")
	if _, err := h.svc.LiveSessions(t.Context(), []int64{a.ID}); err == nil {
		t.Error("a failed check: no error")
	}
}

// TestAttemptLock: while another attempt of the account is being evaluated, a sign-in, a second factor, a proof or a
// password change is refused as throttled without being evaluated, so that a burst of parallel guesses cannot slip
// past the throttle.
func TestAttemptLock(t *testing.T) {
	h := newHarness(t)
	f := h.withTOTP()
	sess, err := h.signIn(t, "alice@example.org", alicePassword)
	if err != nil {
		t.Fatal(err)
	}
	h.store.lockedAccount = accountSubject("alice@example.org")
	ran := false
	for name, call := range map[string]func() error{
		"sign-in": func() error { _, err := h.signIn(t, "alice@example.org", alicePassword); return err },
		"second factor": func() error {
			_, err := h.svc.SubmitSecondFactor(t.Context(), sess, Proof{TOTPCode: "123456"}, addr)
			return err
		},
		"proof": func() error {
			return h.svc.Attempt(t.Context(), sess, addr, FailureTOTP, func(context.Context) (bool, error) {
				ran = true
				return true, nil
			})
		},
		"password change": func() error {
			return h.svc.ChangePassword(t.Context(), sess, PasswordChange{Current: alicePassword,
				New: "a new long password", Address: addr})
		},
	} {
		if _, ok := errors.AsType[*ThrottledError](call()); !ok {
			t.Errorf("%s while the account is locked was evaluated", name)
		}
	}
	if ran || f.calls != 0 || len(h.store.throttles) != 0 {
		t.Errorf("an attempt ran: proof %v, %d codes checked, throttles %v", ran, f.calls, h.store.throttles)
	}
	h.store.lockedAccount = ""
	h.store.fail["TryLockSignInSubject"] = errors.New("db down")
	if _, err := h.signIn(t, "alice@example.org", alicePassword); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a failed lock: %v", err)
	}
}
