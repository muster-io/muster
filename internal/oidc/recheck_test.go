// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/fakes/fakeoidc"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/metrics"
	"github.com/muster-io/muster/internal/oidc/dbgen"
	"github.com/muster-io/muster/internal/outbound"
)

// memCheck is a row of oidc_checks in memory.
type memCheck struct {
	deadline time.Time
	owner    string
	until    time.Time
	outcome  string
}

func (s *memStore) TakeLinkRequest(_ context.Context, a dbgen.TakeLinkRequestParams) (dbgen.TakeLinkRequestRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[string(a.StateHash)]
	if !ok || r.Purpose != purposeLink || r.LinkSessionID != a.SessionID || r.LinkUserID != a.UserID {
		return dbgen.TakeLinkRequestRow{}, pgx.ErrNoRows
	}
	delete(s.requests, string(a.StateHash))
	return dbgen.TakeLinkRequestRow{Nonce: r.Nonce, CodeVerifierCiphertext: r.CodeVerifierCiphertext,
		CodeVerifierKeyID: r.CodeVerifierKeyID, ExpiresAt: r.ExpiresAt}, nil
}

func (s *memStore) LockLinkUser(_ context.Context, a dbgen.LockLinkUserParams) (dbgen.LockLinkUserRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID(a.ID)
	if u == nil {
		return dbgen.LockLinkUserRow{}, pgx.ErrNoRows
	}
	return dbgen.LockLinkUserRow{ID: u.id, PublicID: u.publicID, Name: u.name, Status: u.status,
		HasPassword: u.password, HasIdentity: u.subject != ""}, nil
}

func (s *memStore) LinkIdentity(_ context.Context, a dbgen.LinkIdentityParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.users {
		if o.issuer == a.Issuer.String && o.subject == a.Subject.String {
			return 0, &pgconn.PgError{Code: uniqueViolation, ConstraintName: identityIndex}
		}
	}
	u := s.byID(a.ID)
	if u.status != "active" || !u.password || u.subject != "" {
		return 0, nil
	}
	t := a.Now
	u.issuer, u.subject, u.password, u.lastContact, u.refusedAt = a.Issuer.String, a.Subject.String, false, &t, nil
	return 1, nil
}

func (s *memStore) EndOtherUserSessions(_ context.Context, a dbgen.EndOtherUserSessionsParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID(a.UserID)
	n := max(u.liveSessions-1, 0)
	u.liveSessions, u.endedCount = u.liveSessions-n, u.endedCount+n
	u.endReasons = append(u.endReasons, a.EndReason.String)
	return int64(n), nil
}

func (s *memStore) ContinueAsOIDCSession(_ context.Context, a dbgen.ContinueAsOIDCSessionParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionGone {
		return 0, nil
	}
	s.continued = append(s.continued, a)
	return 1, nil
}

func (s *memStore) CapOIDCSessions(_ context.Context, a dbgen.CapOIDCSessionsParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.capped = append(s.capped, a)
	return 1, nil
}

func (s *memStore) StoreOfflineToken(_ context.Context, a dbgen.StoreOfflineTokenParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, t := s.byID(a.ID), a.Now
	u.token, u.tokenKey, u.tokenAt = a.Ciphertext, a.KeyID.String, &t
	return nil
}

func (s *memStore) WipeOfflineToken(_ context.Context, a dbgen.WipeOfflineTokenParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID(a.ID)
	u.token, u.tokenKey, u.tokenAt = nil, "", nil
	return nil
}

func (s *memStore) ScheduleCheck(_ context.Context, a dbgen.ScheduleCheckParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[a.UserID] = &memCheck{deadline: a.Deadline}
	return nil
}

func (s *memStore) DeleteCheck(_ context.Context, a dbgen.DeleteCheckParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.checks, a.UserID)
	return nil
}

func (s *memStore) GetCheckUser(_ context.Context, a dbgen.GetCheckUserParams) (dbgen.GetCheckUserRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return dbgen.GetCheckUserRow{}, s.fail
	}
	u := s.byID(a.ID)
	if u == nil {
		return dbgen.GetCheckUserRow{}, pgx.ErrNoRows
	}
	row := dbgen.GetCheckUserRow{ID: u.id, PublicID: u.publicID, Name: u.name, Role: u.role, Status: u.status,
		OidcSubject: pgtype.Text{String: u.subject, Valid: u.subject != ""}, OidcOfflineTokenCiphertext: u.token,
		OidcOfflineTokenKeyID: pgtype.Text{String: u.tokenKey, Valid: u.tokenKey != ""},
		LiveSession:           u.liveSessions > 0, UsableToken: u.usableToken}
	if u.tokenAt != nil {
		row.OidcOfflineTokenUpdatedAt = pgtype.Timestamptz{Time: *u.tokenAt, Valid: true}
	}
	return row, nil
}

func (s *memStore) LockCheckUser(_ context.Context, a dbgen.LockCheckUserParams) (dbgen.LockCheckUserRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID(a.ID)
	row := dbgen.LockCheckUserRow{Role: u.role, Status: u.status}
	if u.tokenAt != nil {
		row.OidcOfflineTokenUpdatedAt = pgtype.Timestamptz{Time: *u.tokenAt, Valid: true}
	}
	return row, nil
}

func (s *memStore) held(userID int64, owner pgtype.Text, now time.Time) *memCheck {
	c := s.checks[userID]
	if c == nil || c.owner != owner.String || !c.until.After(now) {
		return nil
	}
	return c
}

func (s *memStore) FinishCheck(_ context.Context, a dbgen.FinishCheckParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.held(a.UserID, a.Owner, a.Now)
	if c == nil {
		return 0, nil
	}
	c.deadline, c.outcome, c.owner, c.until = a.Deadline, a.Outcome.String, "", time.Time{}
	return 1, nil
}

func (s *memStore) DropCheck(_ context.Context, a dbgen.DropCheckParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held(a.UserID, a.Owner, a.Now) == nil {
		return 0, nil
	}
	delete(s.checks, a.UserID)
	return 1, nil
}

func (s *memStore) RefuseUser(_ context.Context, a dbgen.RefuseUserParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, t := s.byID(a.ID), a.Now
	u.refusedAt, u.token, u.tokenKey, u.tokenAt = &t, nil, "", nil
	return nil
}

// claimer claims the due rows of memStore with a lease of the replica r1, as NewClaimer does in PostgreSQL.
func (e *env) claimer() Claimer {
	const owner = "r1"
	return func(_ context.Context, _ int64, limit int32) ([]int64, error) {
		e.store.mu.Lock()
		defer e.store.mu.Unlock()
		lease := db.Lease{Owner: owner, Duration: RecheckLease, Clocks: e.svc.cfg.Clocks}.Params(limit)
		var ids []int64
		for id, c := range e.store.checks {
			if len(ids) < int(limit) && !c.deadline.After(lease.Due) && !c.until.After(lease.Now) {
				c.owner, c.until = lease.Owner, lease.LeaseUntil
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		return ids, nil
	}
}

// recheck runs one round of re-checks of the Organization as the replica r1.
func (e *env) recheck(t *testing.T) int {
	t.Helper()
	n, err := e.svc.Recheck(t.Context(), e.claimer(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func checks(outcome string) uint64 {
	return metrics.OIDCChecks.With(outcome).Get()
}

// tokenRequests counts the requests the fake IdP's token endpoint got.
func (e *env) tokenRequests() int {
	return len(slices.DeleteFunc(e.idp.Requests(), func(r fakeserver.Request) bool { return r.Path != "/token" }))
}

var olgaIDP = fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"oncall"}}

// signedInWithToken signs olga in with an offline token and gives her two live sessions.
func (e *env) signedInWithToken(t *testing.T) *memUser {
	t.Helper()
	if out := e.signIn(t, olgaIDP, ""); out.Session == nil {
		t.Fatalf("sign-in = %+v; log %s", out, e.log)
	}
	olga := e.user("olga")
	olga.liveSessions = 2
	if olga.token == nil || e.store.checks[olga.id] == nil {
		t.Fatalf("no offline token was kept: %+v", olga)
	}
	return olga
}

// TestOfflineTokenAtSignIn is C-03.FR-30 and C-03.AC-24: a granted offline token is kept encrypted and schedules the
// re-check one interval later, and the session lives auth.session_lifetime; without offline_access the token and the
// re-check go and the session ends auth.oidc_fallback_session_lifetime after sign-in.
func TestOfflineTokenAtSignIn(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	plain, err := e.keyring.Decrypt(fieldOfflineToken, olga.tokenKey, olga.token)
	if err != nil || len(plain) == 0 || strings.Contains(string(olga.token), string(plain)) {
		t.Fatalf("the offline token is not stored encrypted: %v", err)
	}
	if c := e.store.checks[olga.id]; !c.deadline.Equal(t0.Add(RecheckInterval)) {
		t.Errorf("the re-check is due at %v, want %v", c.deadline, t0.Add(RecheckInterval))
	}
	if got := e.sessions.opened[0].Lifetime; got != auth.SessionLifetime {
		t.Errorf("the session lifetime with an offline token = %v", got)
	}

	no := false
	e.idp.Configure(fakeoidc.Config{GrantOfflineAccess: &no})
	if out := e.signIn(t, olgaIDP, ""); out.Session == nil {
		t.Fatalf("sign-in without offline access = %+v", out)
	}
	if olga.token != nil || e.store.checks[olga.id] != nil {
		t.Errorf("the offline token or the re-check stayed: %+v", olga)
	}
	if got := e.sessions.opened[1].Lifetime; got != FallbackSessionLifetime {
		t.Errorf("the session lifetime without an offline token = %v", got)
	}
	// The sessions opened with the wiped token are no longer re-checked: they get the fallback lifetime too.
	if c := e.store.capped; len(c) != 1 || c[0].UserID != olga.id ||
		!c[0].ExpiresAt.Equal(e.business.Now().Add(FallbackSessionLifetime)) {
		t.Errorf("the earlier sessions were bounded with %+v", c)
	}
	// Without a re-check row, a round runs nothing for the user.
	e.business.Advance(RecheckInterval)
	if n := e.recheck(t); n != 0 {
		t.Errorf("a round ran %d re-checks for a user without an offline token", n)
	}
}

// TestRecheckRefusal is C-03.FR-30 and C-03.AC-22: when the IdP disables the user, the next re-check ends every
// session with idp_refused, records the refusal, wipes the token and removes the re-check; the next sign-in lifts it.
func TestRecheckRefusal(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	if n := e.recheck(t); n != 0 {
		t.Fatalf("a re-check ran before it was due: %d", n)
	}
	e.idp.SetDisabled("u-1", true)
	e.business.Advance(RecheckInterval)
	before := checks(CheckRefused)
	if n := e.recheck(t); n != 1 {
		t.Fatalf("a round ran %d re-checks", n)
	}
	if !slices.Contains(olga.endReasons, auth.EndIDPRefused) || olga.liveSessions != 0 || olga.refusedAt == nil ||
		olga.token != nil || e.store.checks[olga.id] != nil {
		t.Fatalf("after the refusal: %+v, check %+v", olga, e.store.checks[olga.id])
	}
	refused := e.store.audited(ActionUserRefused)
	if len(refused) != 1 || refused[0]["details"].(map[string]any)["reason"] != "invalid_grant" ||
		refused[0]["details"].(map[string]any)["sessions_ended"] != 2.0 || refused[0]["resource"] != olga.publicID {
		t.Errorf("user.oidc_refused = %v", refused)
	}
	if checks(CheckRefused) != before+1 {
		t.Error("muster_oidc_checks_total{outcome=\"refused\"} did not grow")
	}

	e.idp.SetDisabled("u-1", false)
	if out := e.signIn(t, olgaIDP, ""); out.Session == nil {
		t.Fatalf("the sign-in after the refusal = %+v", out)
	}
	if olga.refusedAt != nil || olga.token == nil || e.store.checks[olga.id] == nil {
		t.Errorf("the next sign-in did not lift the refusal: %+v", olga)
	}
}

// TestRecheckUnavailable is C-03.FR-30 and C-03.AC-23: while the IdP answers 503, the re-checks change nothing, are
// counted as unavailable and logged as oidc_check_failed, and come one interval later; after it recovers, the next one
// succeeds.
func TestRecheckUnavailable(t *testing.T) {
	e := newEnv(t)
	e.svc.cfg.RecheckBudget = 300 * time.Millisecond
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	token := slices.Clone(olga.token)
	if err := e.idp.SetFault(fakeserver.Fault{Path: "/token", Status: http.StatusServiceUnavailable}); err != nil {
		t.Fatal(err)
	}
	before := checks(CheckUnavailable)
	for range 3 {
		e.business.Advance(RecheckInterval)
		if n := e.recheck(t); n != 1 {
			t.Fatalf("a round ran %d re-checks", n)
		}
		if c := e.store.checks[olga.id]; c == nil || c.outcome != CheckUnavailable || c.owner != "" ||
			!c.deadline.Equal(e.business.Now().Add(RecheckInterval)) {
			t.Fatalf("the re-check after an unavailable IdP = %+v", c)
		}
	}
	if olga.liveSessions != 2 || olga.refusedAt != nil || !slices.Equal(olga.token, token) ||
		len(e.store.audited(ActionUserRefused)) != 0 {
		t.Errorf("an unavailable IdP changed the user: %+v", olga)
	}
	if got := checks(CheckUnavailable) - before; got != 3 {
		t.Errorf("muster_oidc_checks_total{outcome=\"unavailable\"} grew by %d, want 3", got)
	}
	if !strings.Contains(e.log.String(), `"event":"oidc_check_failed","user":"`+olga.publicID+`","reason":"refresh"`) {
		t.Errorf("no oidc_check_failed for the refresh:\n%s", e.log)
	}

	e.idp.ResetFaults()
	ok := checks(CheckOK)
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	if c := e.store.checks[olga.id]; c.outcome != CheckOK || checks(CheckOK) != ok+1 || slices.Equal(olga.token, token) {
		t.Errorf("the re-check after the IdP recovered = %+v, token rotated %v", c, !slices.Equal(olga.token, token))
	}
	if !olga.lastContact.Equal(e.business.Now()) {
		t.Errorf("the contact was not recorded: %v", olga.lastContact)
	}
}

// TestRecheckSkipped is C-03.FR-30: a user without a live session and without a usable Personal access token is
// skipped without calling the IdP; one with a usable token is re-checked.
func TestRecheckSkipped(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	olga.liveSessions = 0
	e.idp.ResetRequests()
	before := checks(CheckSkipped)
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	if c := e.store.checks[olga.id]; c.outcome != CheckSkipped || e.tokenRequests() != 0 ||
		checks(CheckSkipped) != before+1 {
		t.Fatalf("skipped = %+v, %d token requests", c, e.tokenRequests())
	}
	olga.usableToken = true
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	if c := e.store.checks[olga.id]; c.outcome != CheckOK || e.tokenRequests() != 1 {
		t.Errorf("a user with a usable token = %+v, %d token requests", c, e.tokenRequests())
	}

	// With OIDC switched off nobody is re-checked.
	e.configure(t, func(in *Input) { in.Enabled = false })
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	if c := e.store.checks[olga.id]; c.outcome != CheckSkipped || e.tokenRequests() != 1 {
		t.Errorf("OIDC off = %+v", c)
	}
}

// TestRecheckRoleSync is C-03.FR-30, FR-32 and C-03.AC-26 at a re-check: the Role follows the groups of the refreshed
// ID token, except that the last active Admin keeps Admin with user.role_sync_kept_admin and the warning
// last_admin_kept; once a second Admin is active the next re-check applies the Role and ends the sessions.
func TestRecheckRoleSync(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	ada := fakeoidc.User{Subject: "u-9", PreferredUsername: "ada", Groups: []string{"muster-admins"}}
	if out := e.signIn(t, ada, ""); out.Session == nil {
		t.Fatal(out)
	}
	u := e.user("ada")
	u.liveSessions = 1
	e.idp.SetNextUser(fakeoidc.User{Subject: "u-9", PreferredUsername: "ada", Groups: []string{"oncall"}})
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	kept := e.store.audited(ActionRoleSyncKeptAdmin)
	if u.role != auth.RoleAdmin || u.liveSessions != 1 || len(kept) != 1 ||
		kept[0]["details"].(map[string]any)["mapped_role"] != auth.RoleResponder ||
		kept[0]["details"].(map[string]any)["at"] != syncAtRecheck {
		t.Fatalf("the last active Admin at a re-check: %+v, entries %v", u, kept)
	}
	st, err := e.svc.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(st.Warnings, func(w Warning) bool { return w.Kind == WarningLastAdminKept }); i < 0 ||
		st.Warnings[i].User.Name != "ada" || st.Warnings[i].Role != auth.RoleResponder {
		t.Fatalf("warnings = %+v", st.Warnings)
	}

	e.store.users = append(e.store.users, &memUser{id: 99, publicID: "SR00000000000B", login: "bea", name: "Bea",
		role: auth.RoleAdmin, status: "active"})
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	if u.role != auth.RoleResponder || !slices.Contains(u.endReasons, endReasonRoleChange) || u.liveSessions != 0 {
		t.Fatalf("with a second Admin: %+v", u)
	}
	changed := e.store.audited("user.role_changed")
	if len(changed) != 1 || changed[0]["details"].(map[string]any)["at"] != syncAtRecheck {
		t.Errorf("user.role_changed = %v", changed)
	}
	if st, _ := e.svc.Get(t.Context()); slices.ContainsFunc(st.Warnings, func(w Warning) bool {
		return w.Kind == WarningLastAdminKept
	}) {
		t.Errorf("the warning stayed: %+v", st.Warnings)
	}
}

// TestRecheckGroups is C-03.FR-30 and D247: groups that map to no Role with no Role for unmatched users are a refusal;
// a refreshed ID token without the groups claim leaves the Role unchanged.
func TestRecheckGroups(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	e.idp.SetNextUser(fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"oncall"},
		GroupsInUserinfoOnly: true})
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	if c := e.store.checks[olga.id]; c.outcome != CheckOK || olga.role != auth.RoleResponder {
		t.Fatalf("no groups claim = %+v, %+v", c, olga)
	}
	e.idp.SetNextUser(fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"contractors"}})
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	refused := e.store.audited(ActionUserRefused)
	if len(refused) != 1 || refused[0]["details"].(map[string]any)["reason"] != reasonNoAccess ||
		olga.refusedAt == nil || e.store.checks[olga.id] != nil {
		t.Errorf("groups that map to nothing = %v, %+v", refused, olga)
	}
}

// TestRecheckLeaseLost: a re-check whose lease another replica took, or whose token a sign-in replaced, records
// nothing; a stale row of a user without a token is removed.
func TestRecheckLeaseLost(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	e.idp.SetDisabled("u-1", true)
	e.business.Advance(RecheckInterval)
	stolen := func(ctx context.Context, org int64, limit int32) ([]int64, error) {
		ids, err := e.claimer()(ctx, org, limit)
		e.store.checks[olga.id].owner = "r2"
		return ids, err
	}
	if _, err := e.svc.Recheck(t.Context(), stolen, "r1"); err != nil {
		t.Fatal(err)
	}
	if olga.refusedAt != nil || olga.liveSessions != 2 || e.store.checks[olga.id] == nil {
		t.Fatalf("a re-check without its lease recorded a refusal: %+v", olga)
	}
	if !strings.Contains(e.log.String(), errLeaseLost.Error()) {
		t.Errorf("the lost lease is not logged:\n%s", e.log)
	}

	// The token changed while the refresh ran: nothing is recorded.
	u := dbgen.GetCheckUserRow{ID: olga.id, PublicID: olga.publicID,
		OidcOfflineTokenUpdatedAt: pgtype.Timestamptz{Time: t0.Add(-time.Minute), Valid: true}}
	if _, err := e.svc.lockCheck(t.Context(), e.store, u); !errors.Is(err, errStale) {
		t.Errorf("lockCheck of a replaced token = %v", err)
	}

	olga.token, olga.tokenKey = nil, ""
	e.store.checks[olga.id] = &memCheck{deadline: t0}
	e.recheck(t)
	if e.store.checks[olga.id] != nil {
		t.Error("the stale re-check of a user without a token stayed")
	}
	e.store.fail = errors.New("database down")
	e.store.checks[olga.id] = &memCheck{deadline: t0}
	e.recheck(t)
	if !strings.Contains(e.log.String(), `"reason":"read"`) {
		t.Errorf("a failed read is not logged:\n%s", e.log)
	}
}

// TestRechecker runs the worker: it goes through the Organizations and the Service of each, and a failure of the
// Organizations or of a claim is logged and backs off.
func TestRechecker(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	e.business.Advance(RecheckInterval)
	var buf strings.Builder
	log := logging.New(&buf, logging.LevelInfo)
	r := Rechecker{Claim: e.claimer(), Owner: "r1", Log: log,
		Organizations: func(context.Context) ([]int64, error) { return []int64{1, 2}, nil },
		Service:       func(org int64) (*Service, bool) { return e.svc, org == 1 }}
	if err := r.Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c := e.store.checks[olga.id]; c.outcome != CheckOK {
		t.Fatalf("the round = %+v", c)
	}
	r.Organizations = func(context.Context) ([]int64, error) { return nil, errors.New("no organizations") }
	if err := r.Round(t.Context()); err == nil || !strings.Contains(buf.String(), `"reason":"organizations"`) {
		t.Errorf("a failed list of Organizations = %v:\n%s", err, buf.String())
	}
	r.Organizations = func(context.Context) ([]int64, error) { return []int64{1}, nil }
	r.Claim = func(context.Context, int64, int32) ([]int64, error) { return nil, errors.New("claim failed") }
	if err := r.Round(t.Context()); err == nil || !strings.Contains(buf.String(), `"reason":"claim"`) {
		t.Errorf("a failed claim = %v:\n%s", err, buf.String())
	}

	ctx, cancel := context.WithCancel(t.Context())
	rounds := 0
	r.Poller = db.Poller{Interval: time.Millisecond, Random: func() float64 { return 0.5 },
		Wait: func(time.Duration) <-chan time.Time {
			rounds++
			if rounds == 3 {
				cancel()
			}
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		}}
	r.Run(ctx)
	if rounds < 3 {
		t.Errorf("the worker ran %d rounds", rounds)
	}
}

// TestRefusedBy is the classification of C-03.FR-30: invalid_grant and other 4xx except 408 and 429 refuse; anything
// else is unavailability.
func TestRefusedBy(t *testing.T) {
	for _, c := range []struct {
		err    error
		reason string
		ok     bool
	}{
		{&BackChannelError{Step: refreshStep, Status: 400, Code: "invalid_grant"}, "invalid_grant", true},
		{&BackChannelError{Step: refreshStep, Status: 403}, "http_403", true},
		{&BackChannelError{Step: refreshStep, Status: 401, Code: "invalid_client"}, "", false},
		{&BackChannelError{Step: refreshStep, Status: 400, Code: "invalid_client"}, "", false},
		{&BackChannelError{Step: refreshStep, Status: 400, Code: "unauthorized_client"}, "", false},
		{&BackChannelError{Step: refreshStep, Status: 401}, "", false},
		{&BackChannelError{Step: refreshStep, Status: 400, Code: "invalid_scope"}, "invalid_scope", true},
		{&BackChannelError{Step: refreshStep, Status: 408}, "", false},
		{&BackChannelError{Step: refreshStep, Status: 429}, "", false},
		{&BackChannelError{Step: refreshStep, Status: 407}, "", false},
		{&BackChannelError{Step: refreshStep, Status: 503}, "", false},
		{&BackChannelError{Step: refreshStep, Status: 0, Outcome: outbound.OutcomeBlocked}, "", false},
		{&BackChannelError{Step: "discovery", Status: 404}, "", false},
		{context.DeadlineExceeded, "", false},
	} {
		if reason, ok := refusedBy(c.err); reason != c.reason || ok != c.ok {
			t.Errorf("refusedBy(%v) = %q, %v; want %q, %v", c.err, reason, ok, c.reason, c.ok)
		}
	}
}

func TestOfflineTokenOfAnswer(t *testing.T) {
	for _, c := range []struct {
		t    Tokens
		want logging.Secret
	}{
		{Tokens{RefreshToken: "r", Scope: []string{"openid", "offline_access"}}, "r"},
		{Tokens{RefreshToken: "r"}, "r"},
		{Tokens{RefreshToken: "r", Scope: []string{"openid"}}, ""},
		{Tokens{Scope: []string{"offline_access"}}, ""},
	} {
		if got := offlineToken(c.t); got != c.want {
			t.Errorf("offlineToken(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}

// TestRecheckIDTokenDoesNotVerify: a refresh whose ID token does not verify decides nothing — the Role and the
// sessions stay, the check is unavailable — but the rotated refresh token is kept, so the next re-check can refresh.
func TestRecheckIDTokenDoesNotVerify(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	token := slices.Clone(olga.token)
	e.idp.SetClock(func() time.Time { return e.rc.Now().Add(-2 * time.Hour) })
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	if c := e.store.checks[olga.id]; c.outcome != CheckUnavailable || slices.Equal(olga.token, token) ||
		olga.liveSessions != 2 || olga.refusedAt != nil {
		t.Fatalf("a refreshed ID token that does not verify = %+v, %+v", c, olga)
	}
	if !strings.Contains(e.log.String(), `"reason":"id token"`) {
		t.Errorf("no oidc_check_failed for the ID token:\n%s", e.log)
	}
	e.idp.SetClock(e.rc.Now)
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	if c := e.store.checks[olga.id]; c.outcome != CheckOK {
		t.Errorf("the next re-check with the rotated token = %+v", c)
	}
}

// TestRecheckKeepsTheRotatedToken: a granted refresh whose lease was lost still keeps the token the IdP rotated, so the
// next re-check does not turn into a refusal; an outcome is recorded even when the worker's context has ended.
func TestRecheckKeepsTheRotatedToken(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	token := slices.Clone(olga.token)
	e.business.Advance(RecheckInterval)
	ok := checks(CheckOK)
	stolen := func(ctx context.Context, org int64, limit int32) ([]int64, error) {
		ids, err := e.claimer()(ctx, org, limit)
		e.store.checks[olga.id].owner = "r2"
		return ids, err
	}
	if _, err := e.svc.Recheck(t.Context(), stolen, "r1"); err != nil {
		t.Fatal(err)
	}
	if slices.Equal(olga.token, token) || checks(CheckOK) != ok {
		t.Fatalf("the rotated token of a re-check that lost its lease was dropped: %+v", olga)
	}
	if c := e.store.checks[olga.id]; c.owner != "r2" || c.outcome != "" {
		t.Errorf("the outcome of a lost lease was recorded: %+v", c)
	}

	// The worker stops: the refresh fails, and its outcome is still recorded.
	e.store.checks[olga.id] = &memCheck{deadline: t0}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.svc.Recheck(ctx, e.claimer(), "r1"); err != nil {
		t.Fatal(err)
	}
	if c := e.store.checks[olga.id]; c.outcome != CheckUnavailable || c.owner != "" {
		t.Errorf("the outcome after the worker stopped = %+v", c)
	}
}

// TestRecheckClientError is the maintainer's decision on C-03.FR-30: a refresh refused with invalid_client — a broken
// client secret — is about Muster's client, not the user: nothing changes, the check is unavailable and logged.
func TestRecheckClientError(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := e.signedInWithToken(t)
	if err := e.idp.SetFault(fakeserver.Fault{Path: "/token", Status: http.StatusUnauthorized,
		Body: `{"error":"invalid_client"}`}); err != nil {
		t.Fatal(err)
	}
	before := checks(CheckUnavailable)
	e.business.Advance(RecheckInterval)
	e.recheck(t)
	if c := e.store.checks[olga.id]; c == nil || c.outcome != CheckUnavailable || olga.liveSessions != 2 ||
		olga.refusedAt != nil || olga.token == nil || len(e.store.audited(ActionUserRefused)) != 0 ||
		checks(CheckUnavailable) != before+1 {
		t.Fatalf("a client error = %+v, %+v", c, olga)
	}
	if !strings.Contains(e.log.String(), `"level":"WARN","event":"oidc_check_failed","user":"`+olga.publicID+
		`","reason":"refresh","error":"the token endpoint answered 401 with the error invalid_client"`) {
		t.Errorf("no oidc_check_failed for the client error:\n%s", e.log)
	}
}
