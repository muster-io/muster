// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	adb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/fakes/fakeoidc"
	"github.com/muster-io/muster/internal/fakes/fakeproxy"
	"github.com/muster-io/muster/internal/keyring"
	kdb "github.com/muster-io/muster/internal/keyring/dbgen"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc/dbgen"
	"github.com/muster-io/muster/internal/outbound"
	"github.com/muster-io/muster/internal/proxyconf"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// memUser is a row of users in memory.
type memUser struct {
	id                       int64
	publicID, login, name    string
	email, role, status      string
	issuer, subject          string
	totp                     bool
	lastContact              *time.Time
	liveSessions, endedCount int
	endReasons               []string
}

// memStore is the tables of the package in memory, enough for the settings and the sign-in.
type memStore struct {
	mu       sync.Mutex
	settings *dbgen.OidcSetting
	requests map[string]dbgen.InsertAuthRequestParams
	users    []*memUser
	policy   string
	audits   []adb.InsertAuditEntryParams
	allowed  []string
	fail     error
	// raceLogin makes the next account creation fail on the unique login, as a concurrent one would; raceIdentity
	// creates the account as a parallel callback would and fails on the unique identity.
	raceLogin, raceIdentity bool
}

func newMemStore() *memStore {
	return &memStore{requests: map[string]dbgen.InsertAuthRequestParams{}, policy: "nobody"}
}

func (s *memStore) InTx(_ context.Context, f func(Queries) error) error { return f(s) }

func (s *memStore) GetSettings(context.Context, int64) (dbgen.OidcSetting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return dbgen.OidcSetting{}, s.fail
	}
	if s.settings == nil {
		return dbgen.OidcSetting{}, pgx.ErrNoRows
	}
	return *s.settings, nil
}

func (s *memStore) LockSettings(context.Context, int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		return 0, pgx.ErrNoRows
	}
	return s.settings.Version, nil
}

func (s *memStore) InsertSettings(_ context.Context, a dbgen.InsertSettingsParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings != nil {
		return 0, nil
	}
	s.settings = &dbgen.OidcSetting{OrgID: a.OrgID, Enabled: a.Enabled, DisplayName: a.DisplayName,
		IssuerUrl: a.IssuerUrl, ClientID: a.ClientID, ClientSecretCiphertext: a.ClientSecretCiphertext,
		ClientSecretKeyID: a.ClientSecretKeyID, ClientSecretUpdatedAt: a.ClientSecretUpdatedAt,
		ClientSecretExpiresOn: a.ClientSecretExpiresOn, Scopes: a.Scopes, GroupsClaim: a.GroupsClaim,
		GroupMappings: a.GroupMappings, UnmatchedRole: a.UnmatchedRole, SyncRole: a.SyncRole,
		SkipTotpWithIdpMfa: a.SkipTotpWithIdpMfa, Proxy: a.Proxy, ProxyPasswordCiphertext: a.ProxyPasswordCiphertext,
		ProxyPasswordKeyID: a.ProxyPasswordKeyID, ProxyPasswordUpdatedAt: a.ProxyPasswordUpdatedAt, Version: 1,
		UpdatedAt: a.UpdatedAt}
	return 1, nil
}

func (s *memStore) UpdateSettings(_ context.Context, a dbgen.UpdateSettingsParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil || s.settings.Version != a.Version {
		return 0, nil
	}
	missing := s.settings.GroupsClaimMissingSince
	if s.settings.GroupsClaim != a.GroupsClaim {
		missing = pgtype.Timestamptz{}
	}
	s.settings = &dbgen.OidcSetting{OrgID: a.OrgID, Enabled: a.Enabled, DisplayName: a.DisplayName,
		IssuerUrl: a.IssuerUrl, ClientID: a.ClientID, ClientSecretCiphertext: a.ClientSecretCiphertext,
		ClientSecretKeyID: a.ClientSecretKeyID, ClientSecretUpdatedAt: a.ClientSecretUpdatedAt,
		ClientSecretExpiresOn: a.ClientSecretExpiresOn, Scopes: a.Scopes, GroupsClaim: a.GroupsClaim,
		GroupMappings: a.GroupMappings, UnmatchedRole: a.UnmatchedRole, SyncRole: a.SyncRole,
		SkipTotpWithIdpMfa: a.SkipTotpWithIdpMfa, Proxy: a.Proxy, ProxyPasswordCiphertext: a.ProxyPasswordCiphertext,
		ProxyPasswordKeyID: a.ProxyPasswordKeyID, ProxyPasswordUpdatedAt: a.ProxyPasswordUpdatedAt,
		GroupsClaimMissingSince: missing, Version: a.Version + 1, UpdatedAt: a.UpdatedAt}
	return 1, nil
}

func (s *memStore) SetGroupsClaimMissing(_ context.Context, a dbgen.SetGroupsClaimMissingParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings != nil && (!a.Since.Valid || !s.settings.GroupsClaimMissingSince.Valid) {
		s.settings.GroupsClaimMissingSince = a.Since
	}
	return nil
}

func (s *memStore) LatestKeptAdmin(context.Context, int64) (dbgen.LatestKeptAdminRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.audits) - 1; i >= 0; i-- {
		a := s.audits[i]
		if a.Action != ActionRoleSyncKeptAdmin {
			continue
		}
		var details map[string]string
		_ = json.Unmarshal(a.Details, &details)
		u := s.byPublicID(a.ResourcePublicID.String)
		row := dbgen.LatestKeptAdminRow{At: a.At, MappedRole: details["mapped_role"], PublicID: u.publicID,
			Name: u.name, Login: u.login, Role: u.role, Status: u.status, ActiveAdmins: s.activeAdmins()}
		if u.lastContact != nil {
			row.OidcLastContactAt = pgtype.Timestamptz{Time: *u.lastContact, Valid: true}
		}
		return row, nil
	}
	return dbgen.LatestKeptAdminRow{}, pgx.ErrNoRows
}

func (s *memStore) byPublicID(id string) *memUser {
	for _, u := range s.users {
		if u.publicID == id {
			return u
		}
	}
	return &memUser{}
}

func (s *memStore) byID(id int64) *memUser {
	for _, u := range s.users {
		if u.id == id {
			return u
		}
	}
	return nil
}

func (s *memStore) activeAdmins() int64 {
	var n int64
	for _, u := range s.users {
		if u.role == auth.RoleAdmin && u.status == "active" {
			n++
		}
	}
	return n
}

func (s *memStore) InsertAuthRequest(_ context.Context, a dbgen.InsertAuthRequestParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if int64(len(s.requests)) >= a.MaxPending {
		return 0, nil
	}
	s.requests[string(a.StateHash)] = a
	return 1, nil
}

func (s *memStore) TakeAuthRequest(_ context.Context, a dbgen.TakeAuthRequestParams) (dbgen.TakeAuthRequestRow,
	error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[string(a.StateHash)]
	if !ok {
		return dbgen.TakeAuthRequestRow{}, pgx.ErrNoRows
	}
	delete(s.requests, string(a.StateHash))
	return dbgen.TakeAuthRequestRow{Purpose: r.Purpose, Nonce: r.Nonce, CodeVerifierCiphertext: r.CodeVerifierCiphertext,
		CodeVerifierKeyID: r.CodeVerifierKeyID, ReturnTo: r.ReturnTo, ExpiresAt: r.ExpiresAt}, nil
}

func (s *memStore) GetIdentityUser(_ context.Context, a dbgen.GetIdentityUserParams) (dbgen.GetIdentityUserRow,
	error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return dbgen.GetIdentityUserRow{}, s.fail
	}
	for _, u := range s.users {
		if u.issuer == a.Issuer.String && u.subject == a.Subject.String {
			return dbgen.GetIdentityUserRow{ID: u.id, PublicID: u.publicID, Login: u.login, Name: u.name, Role: u.role,
				Status: u.status, TotpEnrolled: u.totp, TotpRequired: s.policy}, nil
		}
	}
	return dbgen.GetIdentityUserRow{}, pgx.ErrNoRows
}

func (s *memStore) LoginTaken(_ context.Context, a dbgen.LoginTakenParams) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.users, func(u *memUser) bool { return strings.EqualFold(u.login, a.Login) }), nil
}

func (s *memStore) CreateIdentityUser(_ context.Context, a dbgen.CreateIdentityUserParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.raceLogin {
		s.raceLogin = false
		return 0, &pgconn.PgError{Code: uniqueViolation, ConstraintName: loginIndex}
	}
	u := &memUser{id: int64(len(s.users) + 1), publicID: a.PublicID, login: a.Login, name: a.Name,
		email: a.Email.String, role: a.Role, status: "active", issuer: a.Issuer.String, subject: a.Subject.String}
	s.users = append(s.users, u)
	if s.raceIdentity {
		s.raceIdentity = false
		return 0, &pgconn.PgError{Code: uniqueViolation, ConstraintName: identityIndex}
	}
	return u.id, nil
}

func (s *memStore) LockActiveAdmins(context.Context, int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []int64
	for _, u := range s.users {
		if u.role == auth.RoleAdmin && u.status == "active" {
			ids = append(ids, u.id)
		}
	}
	return ids, nil
}

func (s *memStore) LockUser(_ context.Context, a dbgen.LockUserParams) (dbgen.LockUserRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID(a.ID)
	return dbgen.LockUserRow{Role: u.role, Status: u.status}, nil
}

func (s *memStore) SetRole(_ context.Context, a dbgen.SetRoleParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID(a.ID).role = a.Role
	return 1, nil
}

func (s *memStore) EndUserSessions(_ context.Context, a dbgen.EndUserSessionsParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID(a.UserID)
	n := u.liveSessions
	u.liveSessions, u.endedCount = 0, u.endedCount+n
	u.endReasons = append(u.endReasons, a.EndReason.String)
	return int64(n), nil
}

func (s *memStore) RecordContact(_ context.Context, a dbgen.RecordContactParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := a.Now
	s.byID(a.ID).lastContact = &t
	return nil
}

func (s *memStore) InsertDemoSettings(_ context.Context, a dbgen.InsertDemoSettingsParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings != nil {
		return 0, nil
	}
	s.settings = &dbgen.OidcSetting{OrgID: a.OrgID, Enabled: true, DisplayName: a.DisplayName, IssuerUrl: a.IssuerUrl,
		ClientID: a.ClientID, ClientSecretCiphertext: a.ClientSecretCiphertext, ClientSecretKeyID: a.ClientSecretKeyID,
		ClientSecretUpdatedAt: a.UpdatedAt, Scopes: a.Scopes, GroupsClaim: a.GroupsClaim,
		GroupMappings: a.GroupMappings, UnmatchedRole: UnmatchedNone, SyncRole: true, Proxy: []byte(`{"enabled":false}`),
		Version: 1, UpdatedAt: a.UpdatedAt.Time}
	return 1, nil
}

func (s *memStore) AllowNetwork(_ context.Context, a dbgen.AllowNetworkParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.Contains(s.allowed, a.Network) {
		return 0, nil
	}
	s.allowed = append(s.allowed, a.Network)
	return 1, nil
}

func (s *memStore) InsertAuditEntry(_ context.Context, a adb.InsertAuditEntryParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, a)
	return nil
}

// audited returns the entries with action, oldest first, with their details and diff decoded.
func (s *memStore) audited(action string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, a := range s.audits {
		if a.Action != action {
			continue
		}
		var details map[string]any
		var diff []map[string]any
		_ = json.Unmarshal(a.Details, &details)
		_ = json.Unmarshal(a.Diff, &diff)
		out = append(out, map[string]any{"details": details, "diff": diff, "resource": a.ResourcePublicID.String,
			"actor": a.ActorKind, "at": a.At})
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

func activeKeyring(t *testing.T) *keyring.Keyring {
	t.Helper()
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{'o'}, keyring.KeySize)}, false)
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

// fakeSessions records the sessions an OIDC sign-in opens.
type fakeSessions struct {
	mu     sync.Mutex
	opened []auth.OIDCSignIn
	err    error
}

func (f *fakeSessions) OpenOIDCSession(_ context.Context, in auth.OIDCSignIn) (auth.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return auth.Session{}, f.err
	}
	f.opened = append(f.opened, in)
	return auth.Session{PublicID: "SN0000000000S1", State: in.State, Method: auth.MethodOIDC, User: in.User}, nil
}

// env is a Service over memStore, the fake IdP and real outbound clients that may reach loopback.
type env struct {
	svc      *Service
	store    *memStore
	idp      *fakeoidc.Fake
	sessions *fakeSessions
	business *clock.Manual
	rc       *clock.Manual
	log      *bytes.Buffer
	keyring  *keyring.Keyring
}

func network(t *testing.T, rc clock.Clock, log *logging.Logger) Network {
	t.Helper()
	policy, err := outbound.ParsePolicy("standard", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return Network{Policy: outbound.StaticPolicy(policy), Log: log, Real: rc}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	idp := fakeoidc.New()
	if err := idp.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idp.Close(context.WithoutCancel(t.Context())) })
	business, rc := clock.NewManual(t0), clock.NewManual(time.Now().UTC().Truncate(time.Second))
	idp.SetClock(rc.Now)
	var buf bytes.Buffer
	log := logging.New(&buf, logging.LevelInfo)
	e := &env{store: newMemStore(), idp: idp, sessions: &fakeSessions{}, business: business, rc: rc, log: &buf,
		keyring: activeKeyring(t)}
	public, _ := url.Parse("http://localhost:8080")
	e.svc = NewService(Config{OrgID: 1, Store: e.store, Keyring: e.keyring, Audit: audit.NewWriter(log, business),
		Clocks: clock.Clocks{Business: business, Real: rc}, Network: network(t, rc, log), Log: log,
		Sessions: e.sessions, PublicURL: public})
	return e
}

func admin() Requester {
	return Requester{Actor: audit.User(1, "SR0000000000A1"), Transport: audit.TransportUI}
}

// input is a valid update against the fake IdP: oncall → responder, muster-admins → admin.
func (e *env) input() Input {
	scopes := []string{"profile", "email"}
	return Input{Enabled: true, IssuerURL: e.idp.URL(), ClientID: "muster", ClientSecret: keyring.Replace("s3cr3t"),
		Scopes: &scopes, GroupsClaim: "groups", UnmatchedRole: UnmatchedNone, SyncRole: true,
		GroupMappings: []GroupMapping{{Group: "oncall", Role: auth.RoleResponder},
			{Group: "muster-admins", Role: auth.RoleAdmin}}}
}

func (e *env) configure(t *testing.T, change func(*Input)) Settings {
	t.Helper()
	in := e.input()
	if change != nil {
		change(&in)
	}
	st, err := e.svc.Update(t.Context(), admin(), nil, in)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func kinds(ws []Warning) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.Kind)
	}
	return out
}

func TestGetBeforeTheFirstSave(t *testing.T) {
	e := newEnv(t)
	st, err := e.svc.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Configured || st.Enabled || st.Version != 0 || st.GroupsClaim != "groups" || !st.SyncRole ||
		st.UnmatchedRole != UnmatchedNone || !slices.Equal(st.Scopes, DefaultScopes) || st.ClientSecret.Set {
		t.Fatalf("defaults = %+v", st)
	}
	if !slices.Equal(kinds(st.Warnings), []string{WarningNobodyCanSignIn}) {
		t.Errorf("warnings = %v", kinds(st.Warnings))
	}
	if on, _, err := e.svc.SignInOptions(t.Context()); err != nil || on {
		t.Errorf("sign-in options = %v, %v", on, err)
	}
	if _, err := e.svc.StartSignIn(t.Context(), "/"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("start = %v, want ErrNotEnabled", err)
	}
	e.store.fail = errors.New("db down")
	if _, err := e.svc.Get(t.Context()); err == nil {
		t.Error("a failed read is no error")
	}
}

// TestUpdateStoresTheSecretEncryptedAndWriteOnly is C-03.FR-5, FR-21 and AC-6.
func TestUpdateStoresTheSecretEncryptedAndWriteOnly(t *testing.T) {
	e := newEnv(t)
	zero := int64(0)
	st, err := e.svc.Update(t.Context(), admin(), &zero, e.input())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Configured || st.Version != 1 || !st.ClientSecret.Set || st.ClientSecret.UpdatedAt == nil ||
		st.ButtonName() != "127.0.0.1" {
		t.Fatalf("settings = %+v", st)
	}
	row := e.store.settings
	if bytes.Contains(row.ClientSecretCiphertext, []byte("s3cr3t")) || row.ClientSecretKeyID.String == "" {
		t.Fatal("the client secret is not stored encrypted")
	}
	if v, err := e.keyring.Decrypt(fieldClientSecret, row.ClientSecretKeyID.String, row.ClientSecretCiphertext); err != nil ||
		string(v) != "s3cr3t" {
		t.Fatalf("decrypt = %q, %v", v, err)
	}
	entries := e.store.audited(ActionSettingsUpdated)
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	var secret map[string]any
	for _, c := range entries[0]["diff"].([]map[string]any) {
		if c["pointer"] == "/client_secret" {
			secret = c
		}
	}
	if secret == nil || secret["secret_changed"] != true || secret["before"] != nil || secret["after"] != nil {
		t.Errorf("the diff of the secret = %v", secret)
	}
	if strings.Contains(e.log.String(), "s3cr3t") || bytes.Contains(e.store.audits[0].Diff, []byte("s3cr3t")) {
		t.Error("the secret reached the log or the Audit log")
	}

	// Keeping the secret and changing nothing writes nothing; a stale version is a mismatch.
	in := e.input()
	in.ClientSecret = keyring.Keep
	one := int64(1)
	same, err := e.svc.Update(t.Context(), admin(), &one, in)
	if err != nil || same.Version != 1 || len(e.store.audited(ActionSettingsUpdated)) != 1 {
		t.Fatalf("an update without a change = %+v, %v", same, err)
	}
	if _, err := e.svc.Update(t.Context(), admin(), &zero, in); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("a stale version = %v", err)
	}
	in.DisplayName = new("Corp SSO")
	in.Proxy = proxyconf.Input{Enabled: true, Type: new("http"), Address: new("127.0.0.1:3128"),
		Password: keyring.Replace("proxy-pw")}
	st, err = e.svc.Update(t.Context(), admin(), &one, in)
	if err != nil || st.Version != 2 || st.ButtonName() != "Corp SSO" || !st.ProxyPassword.Set || !st.ClientSecret.Set {
		t.Fatalf("second update = %+v, %v", st, err)
	}
	last := e.store.audited(ActionSettingsUpdated)[1]["diff"].([]map[string]any)
	var pointers []string
	for _, c := range last {
		pointers = append(pointers, c["pointer"].(string))
	}
	if !slices.Contains(pointers, "/display_name") || !slices.Contains(pointers, "/proxy/password") ||
		slices.Contains(pointers, "/client_secret") {
		t.Errorf("diff pointers = %v", pointers)
	}
	in.ClientSecret = keyring.Clear
	in.Proxy.Password = keyring.Keep
	if st, err = e.svc.Update(t.Context(), admin(), nil, in); err != nil || st.ClientSecret.Set || !st.ProxyPassword.Set {
		t.Fatalf("clearing the secret = %+v, %v", st, err)
	}
}

func TestUpdateValidates(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		pointer string
		change  func(*Input)
	}{
		{"/issuer_url", func(in *Input) { in.IssuerURL = "ftp://idp" }},
		{"/issuer_url", func(in *Input) { in.IssuerURL = "https://idp.example/?x=1" }},
		{"/client_id", func(in *Input) { in.ClientID = " " }},
		{"/groups_claim", func(in *Input) { in.GroupsClaim = "" }},
		{"/scopes", func(in *Input) { in.Scopes = &[]string{"a b"} }},
		{"/group_mappings/0/group", func(in *Input) { in.GroupMappings = []GroupMapping{{Role: "admin"}} }},
		{"/group_mappings/0/role", func(in *Input) { in.GroupMappings = []GroupMapping{{Group: "g", Role: "owner"}} }},
		{"/unmatched_role", func(in *Input) { in.UnmatchedRole = "admin" }},
		{"/display_name", func(in *Input) { in.DisplayName = new(strings.Repeat("x", 101)) }},
		{"/proxy/address", func(in *Input) { in.Proxy = proxyconf.Input{Enabled: true, Type: new("http")} }},
		{"/client_secret", func(in *Input) { in.ClientSecret = keyring.Replace("") }},
		{"/proxy/password", func(in *Input) { in.Proxy.Password = keyring.Replace("") }},
	} {
		in := e.input()
		tc.change(&in)
		_, err := e.svc.Update(t.Context(), admin(), nil, in)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != tc.pointer {
			t.Errorf("%s: err = %v", tc.pointer, err)
		}
	}
	if e.store.settings != nil {
		t.Error("an invalid update was stored")
	}
}

// TestWarnings is C-03.FR-6 and FR-5 (AC-7): nobody_can_sign_in, secret_expiring 10 days ahead but not 30.
func TestWarnings(t *testing.T) {
	e := newEnv(t)
	st := e.configure(t, func(in *Input) { in.GroupMappings = nil })
	if !slices.Equal(kinds(st.Warnings), []string{WarningNobodyCanSignIn}) {
		t.Errorf("an empty mapping = %v", kinds(st.Warnings))
	}
	st = e.configure(t, func(in *Input) { in.GroupMappings = nil; in.UnmatchedRole = UnmatchedViewer })
	if len(st.Warnings) != 0 {
		t.Errorf("unmatched viewers = %v", kinds(st.Warnings))
	}
	for _, tc := range []struct {
		days int
		warn bool
	}{{10, true}, {30, false}, {-1, true}} {
		d := t0.AddDate(0, 0, tc.days)
		st = e.configure(t, func(in *Input) { in.ExpiresOnSet = true; in.ClientSecretExpiresOn = &d })
		got := slices.IndexFunc(st.Warnings, func(w Warning) bool { return w.Kind == WarningSecretExpiring })
		if (got >= 0) != tc.warn {
			t.Errorf("%d days: warnings %v", tc.days, kinds(st.Warnings))
		}
		if got >= 0 && st.Warnings[got].ExpiresOn.Format(time.DateOnly) != d.Format(time.DateOnly) {
			t.Errorf("expires_on = %v", st.Warnings[got].ExpiresOn)
		}
	}
	st = e.configure(t, func(in *Input) { in.ExpiresOnSet = true })
	if st.ClientSecretExpiresOn != nil {
		t.Error("null did not clear the expiry date")
	}
}

// TestCheck is C-03.FR-8, FR-19 and AC-8: the check reaches discovery directly and through each proxy, records a
// missing groups claim, and fails naming the rule for a proxy at a blocked address.
func TestCheck(t *testing.T) {
	e := newEnv(t)
	res, err := e.svc.Check(t.Context())
	if err != nil || res.OK || !strings.Contains(res.Error, "not configured") {
		t.Fatalf("before the settings = %+v, %v", res, err)
	}
	e.configure(t, nil)
	res, err = e.svc.Check(t.Context())
	if err != nil || !res.OK || res.ViaProxy || res.Discovery.TokenEndpoint != e.idp.URL()+"/token" || res.Error != "" {
		t.Fatalf("direct = %+v, %v", res, err)
	}
	if slices.Contains(kinds(res.Warnings), WarningGroupsClaimMissing) {
		t.Error("the groups claim is advertised but warned about")
	}
	e.idp.Configure(fakeoidc.Config{OmitGroupsClaim: new(true)})
	if res, err = e.svc.Check(t.Context()); err != nil || !slices.Contains(kinds(res.Warnings), WarningGroupsClaimMissing) {
		t.Fatalf("without the groups claim = %+v, %v", res, err)
	}
	if st, _ := e.svc.Get(t.Context()); !slices.Contains(kinds(st.Warnings), WarningGroupsClaimMissing) {
		t.Error("the settings do not carry groups_claim_missing")
	}
	e.idp.Configure(fakeoidc.Config{OmitGroupsClaim: new(false)})
	if res, _ = e.svc.Check(t.Context()); slices.Contains(kinds(res.Warnings), WarningGroupsClaimMissing) {
		t.Error("a check that finds the claim again did not clear the warning")
	}

	for _, kind := range []fakeproxy.Kind{fakeproxy.HTTP, fakeproxy.SOCKS5} {
		p, err := fakeproxy.Start(t.Context(), kind, "127.0.0.1:0", fakeproxy.Options{Username: "u", Password: "pw"})
		if err != nil {
			t.Fatal(err)
		}
		e.configure(t, func(in *Input) {
			in.Proxy = proxyconf.Input{Enabled: true, Type: new(string(kind)), Address: new(p.Addr()),
				UsernameSet: true, Username: new("u"), Password: keyring.Replace("pw")}
		})
		res, err = e.svc.Check(t.Context())
		if err != nil || !res.OK || !res.ViaProxy {
			t.Fatalf("%s proxy = %+v, %v", kind, res, err)
		}
		host := strings.TrimPrefix(e.idp.URL(), "http://")
		if targets := p.Targets(); len(targets) == 0 || targets[len(targets)-1] != host {
			t.Errorf("%s proxy targets = %v, want %s", kind, targets, host)
		}
		_ = p.Close()
	}
	e.configure(t, func(in *Input) {
		in.Proxy = proxyconf.Input{Enabled: true, Type: new("socks5"), Address: new("169.254.169.254:1080")}
	})
	res, err = e.svc.Check(t.Context())
	if err != nil || res.OK || !res.ViaProxy ||
		res.Error != "blocked by the outbound address policy: proxy 169.254.169.254 is link-local (always blocked)" {
		t.Fatalf("a blocked proxy = %+v, %v", res, err)
	}
	e.configure(t, func(in *Input) { in.IssuerURL = "http://127.0.0.1:1" })
	if res, _ = e.svc.Check(t.Context()); res.OK || res.Error == "" {
		t.Errorf("an unreachable issuer = %+v", res)
	}
}

func TestSignInOptionsAndDemo(t *testing.T) {
	e := newEnv(t)
	demo := Demo{IssuerURL: e.idp.URL(), ClientID: "muster-dev", ClientSecret: "dev-secret", DisplayName: "Dev IdP",
		AllowNetwork: "127.0.0.0/8", Mappings: []GroupMapping{{Group: "oncall", Role: auth.RoleResponder}}}
	for range 2 {
		if err := e.svc.EnsureDemo(t.Context(), demo); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.store.audited(ActionSettingsUpdated)) != 1 || !slices.Equal(e.store.allowed, []string{"127.0.0.0/8"}) {
		t.Errorf("demo audits %v, allowed %v", e.store.audited(ActionSettingsUpdated), e.store.allowed)
	}
	on, name, err := e.svc.SignInOptions(t.Context())
	if err != nil || !on || name != "Dev IdP" {
		t.Errorf("sign-in options = %v %q %v", on, name, err)
	}
	st, err := e.svc.Get(t.Context())
	if err != nil || !st.ClientSecret.Set || len(st.GroupMappings) != 1 || !st.Enabled {
		t.Errorf("demo settings = %+v, %v", st, err)
	}
	if (Settings{IssuerURL: "::"}).ButtonName() != "OIDC" {
		t.Error("an issuer without a host has no fallback name")
	}
}
