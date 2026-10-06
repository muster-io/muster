// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package tokens

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/audit"
	auditdb "github.com/muster-io/muster/internal/audit/dbgen"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/tokens/dbgen"
)

const orgID = 7

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

var roles = auth.Roles{
	auth.RoleAdmin:     {"alert-groups:read", "service-accounts:write", "users:read", "users:write"},
	auth.RoleResponder: {"alert-groups:acknowledge", "alert-groups:read"},
	auth.RoleViewer:    {"alert-groups:read"},
}

// owner is a user as the token lookup joins it.
type owner struct {
	publicID    string
	role        string
	status      string
	oidc        bool
	offline     bool
	lastContact *time.Time
	refusedAt   *time.Time
}

type saRow struct {
	dbgen.GetServiceAccountRow
}

type tokenRow struct {
	id, userID, saID       int64
	publicID, kind, name   string
	hash                   []byte
	expires, revoked, used *time.Time
	reason                 string
	address                *netip.Addr
}

// fakeStore is the database of the package in memory.
type fakeStore struct {
	users   map[int64]*owner
	sas     []*saRow
	tokens  []*tokenRow
	perms   map[int64][]string
	audit   []auditdb.InsertAuditEntryParams
	fail    map[string]error
	grace   time.Duration
	touches int
	nextID  int64
}

func newStore() *fakeStore {
	return &fakeStore{
		users: map[int64]*owner{
			1: {publicID: "SRAAAAAAAAAAAA", role: auth.RoleAdmin, status: StatusActive},
			2: {publicID: "SRBBBBBBBBBBBB", role: auth.RoleResponder, status: StatusActive},
		},
		perms: map[int64][]string{}, fail: map[string]error{}, grace: 7 * 24 * time.Hour,
	}
}

func (s *fakeStore) id() int64 {
	s.nextID++
	return s.nextID
}

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	if err := s.fail["InTx"]; err != nil {
		return err
	}
	tokens, sas, audits := len(s.tokens), cloneSAs(s.sas), len(s.audit)
	revoked := make([]*time.Time, len(s.tokens))
	for i, t := range s.tokens {
		revoked[i] = t.revoked
	}
	if err := f(s); err != nil {
		s.tokens, s.sas, s.audit = s.tokens[:tokens], sas, s.audit[:audits]
		for i, t := range s.tokens {
			t.revoked = revoked[i]
		}
		return err
	}
	return nil
}

func cloneSAs(in []*saRow) []*saRow {
	out := make([]*saRow, len(in))
	for i, sa := range in {
		c := *sa
		out[i] = &c
	}
	return out
}

func (s *fakeStore) InsertToken(_ context.Context, arg dbgen.InsertTokenParams) (int64, error) {
	if err := s.fail["InsertToken"]; err != nil {
		return 0, err
	}
	t := &tokenRow{id: s.id(), userID: arg.UserID.Int64, saID: arg.ServiceAccountID.Int64, publicID: arg.PublicID,
		kind: arg.Kind, name: arg.Name, hash: arg.TokenHash, expires: timeOf(arg.ExpiresAt)}
	s.tokens = append(s.tokens, t)
	return t.id, nil
}

func (s *fakeStore) InsertTokenPermissions(_ context.Context, arg dbgen.InsertTokenPermissionsParams) error {
	if err := s.fail["InsertTokenPermissions"]; err != nil {
		return err
	}
	s.perms[arg.ApiTokenID] = slices.Clone(arg.Permissions)
	return nil
}

func (s *fakeStore) ListUserTokens(_ context.Context, arg dbgen.ListUserTokensParams) ([]dbgen.ListUserTokensRow,
	error) {
	if err := s.fail["ListUserTokens"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListUserTokensRow
	for _, t := range slices.Backward(s.tokens) {
		if t.kind == KindPersonal && t.userID == arg.UserID.Int64 && t.revoked == nil {
			out = append(out, dbgen.ListUserTokensRow{ID: t.id, PublicID: t.publicID, Name: t.name,
				ExpiresAt: timestamptz(t.expires), CreatedAt: t0, LastUsedAt: timestamptz(t.used),
				LastUsedAddress: t.address, Permissions: s.perms[t.id]})
		}
	}
	return out, nil
}

func (s *fakeStore) ListServiceAccountTokens(_ context.Context, arg dbgen.ListServiceAccountTokensParams) (
	[]dbgen.ListServiceAccountTokensRow, error) {
	if err := s.fail["ListServiceAccountTokens"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListServiceAccountTokensRow
	for _, t := range slices.Backward(s.tokens) {
		if t.kind == KindServiceAccount && t.saID == arg.ServiceAccountID.Int64 && t.revoked == nil {
			out = append(out, dbgen.ListServiceAccountTokensRow{ID: t.id, PublicID: t.publicID, Name: t.name,
				ExpiresAt: timestamptz(t.expires), CreatedAt: t0, LastUsedAt: timestamptz(t.used),
				LastUsedAddress: t.address})
		}
	}
	return out, nil
}

func (s *fakeStore) revoke(kind string, owner int64, publicID string, now time.Time) (int64, string, error) {
	for _, t := range s.tokens {
		o := t.userID
		if kind == KindServiceAccount {
			o = t.saID
		}
		if t.kind == kind && o == owner && t.publicID == publicID && t.revoked == nil {
			t.revoked, t.reason = &now, "revoked"
			return t.id, t.name, nil
		}
	}
	return 0, "", pgx.ErrNoRows
}

func (s *fakeStore) RevokeUserToken(_ context.Context, arg dbgen.RevokeUserTokenParams) (dbgen.RevokeUserTokenRow,
	error) {
	if err := s.fail["RevokeUserToken"]; err != nil {
		return dbgen.RevokeUserTokenRow{}, err
	}
	id, name, err := s.revoke(KindPersonal, arg.UserID.Int64, arg.PublicID, arg.Now)
	return dbgen.RevokeUserTokenRow{ID: id, Name: name}, err
}

func (s *fakeStore) RevokeServiceAccountToken(_ context.Context, arg dbgen.RevokeServiceAccountTokenParams) (
	dbgen.RevokeServiceAccountTokenRow, error) {
	if err := s.fail["RevokeServiceAccountToken"]; err != nil {
		return dbgen.RevokeServiceAccountTokenRow{}, err
	}
	id, name, err := s.revoke(KindServiceAccount, arg.ServiceAccountID.Int64, arg.PublicID, arg.Now)
	return dbgen.RevokeServiceAccountTokenRow{ID: id, Name: name}, err
}

func (s *fakeStore) RevokeServiceAccountTokens(_ context.Context, arg dbgen.RevokeServiceAccountTokensParams) (int64,
	error) {
	if err := s.fail["RevokeServiceAccountTokens"]; err != nil {
		return 0, err
	}
	var n int64
	for _, t := range s.tokens {
		if t.kind == KindServiceAccount && t.saID == arg.ServiceAccountID.Int64 && t.revoked == nil {
			t.revoked, t.reason = &arg.Now, "owner_deleted"
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) GetTokenByHash(_ context.Context, arg dbgen.GetTokenByHashParams) (dbgen.GetTokenByHashRow,
	error) {
	if err := s.fail["GetTokenByHash"]; err != nil {
		return dbgen.GetTokenByHashRow{}, err
	}
	for _, t := range s.tokens {
		if t.kind != arg.Kind || !bytes.Equal(t.hash, arg.TokenHash) {
			continue
		}
		row := dbgen.GetTokenByHashRow{ID: t.id, PublicID: t.publicID, Kind: t.kind, Name: t.name,
			TokenHash: t.hash, ExpiresAt: timestamptz(t.expires), RevokedAt: timestamptz(t.revoked),
			LastUsedAt: timestamptz(t.used), LastUsedAddress: t.address,
			OidcTokenGraceSeconds: int64(s.grace / time.Second), Permissions: s.perms[t.id]}
		if u, ok := s.users[t.userID]; ok {
			row.UserID = pgtype.Int8{Int64: t.userID, Valid: true}
			row.UserPublicID = pgtype.Text{String: u.publicID, Valid: true}
			row.UserName = pgtype.Text{String: "user", Valid: true}
			row.UserRole = pgtype.Text{String: u.role, Valid: true}
			row.UserStatus = pgtype.Text{String: u.status, Valid: true}
			row.UserOidc, row.UserOfflineToken = u.oidc, u.offline
			row.UserOidcLastContactAt, row.UserOidcRefusedAt = timestamptz(u.lastContact), timestamptz(u.refusedAt)
		}
		for _, sa := range s.sas {
			if sa.ID == t.saID {
				row.ServiceAccountID = pgtype.Int8{Int64: sa.ID, Valid: true}
				row.ServiceAccountPublicID = pgtype.Text{String: sa.PublicID, Valid: true}
				row.ServiceAccountName = pgtype.Text{String: sa.Name, Valid: true}
				row.ServiceAccountRole = pgtype.Text{String: sa.Role, Valid: true}
				row.ServiceAccountStatus = pgtype.Text{String: sa.Status, Valid: true}
			}
		}
		return row, nil
	}
	return dbgen.GetTokenByHashRow{}, pgx.ErrNoRows
}

func (s *fakeStore) TouchToken(_ context.Context, arg dbgen.TouchTokenParams) error {
	if err := s.fail["TouchToken"]; err != nil {
		return err
	}
	for _, t := range s.tokens {
		if t.id == arg.ID && (t.used == nil || !t.used.After(arg.StaleBefore)) {
			now := arg.Now
			t.used, t.address = &now, arg.Address
			s.touches++
		}
	}
	return nil
}

func (s *fakeStore) count(saID int64) int64 {
	var n int64
	for _, t := range s.tokens {
		if t.saID == saID && t.revoked == nil {
			n++
		}
	}
	return n
}

func (s *fakeStore) ListServiceAccounts(_ context.Context, arg dbgen.ListServiceAccountsParams) (
	[]dbgen.ListServiceAccountsRow, error) {
	if err := s.fail["ListServiceAccounts"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListServiceAccountsRow
	for _, sa := range s.sas {
		if sa.Status == StatusDeleted || (arg.AfterID.Valid && sa.ID <= arg.AfterID.Int64) {
			continue
		}
		if len(out) == int(arg.PageSize) {
			break
		}
		out = append(out, dbgen.ListServiceAccountsRow{ID: sa.ID, PublicID: sa.PublicID, Name: sa.Name,
			Role: sa.Role, Status: sa.Status, CreatedAt: sa.CreatedAt, Version: sa.Version, TokenCount: s.count(sa.ID)})
	}
	return out, nil
}

func (s *fakeStore) find(publicID string) *saRow {
	for _, sa := range s.sas {
		if sa.PublicID == publicID && sa.Status != StatusDeleted {
			return sa
		}
	}
	return nil
}

func (s *fakeStore) byID(id int64) *saRow {
	for _, sa := range s.sas {
		if sa.ID == id {
			return sa
		}
	}
	return nil
}

func (s *fakeStore) GetServiceAccount(_ context.Context, arg dbgen.GetServiceAccountParams) (
	dbgen.GetServiceAccountRow, error) {
	if err := s.fail["GetServiceAccount"]; err != nil {
		return dbgen.GetServiceAccountRow{}, err
	}
	sa := s.find(arg.PublicID)
	if sa == nil {
		return dbgen.GetServiceAccountRow{}, pgx.ErrNoRows
	}
	row := sa.GetServiceAccountRow
	row.TokenCount = s.count(sa.ID)
	return row, nil
}

func (s *fakeStore) LockServiceAccount(_ context.Context, arg dbgen.LockServiceAccountParams) (int64, error) {
	if err := s.fail["LockServiceAccount"]; err != nil {
		return 0, err
	}
	sa := s.find(arg.PublicID)
	if sa == nil {
		return 0, pgx.ErrNoRows
	}
	return sa.ID, nil
}

func (s *fakeStore) taken(name string, except int64) bool {
	return slices.ContainsFunc(s.sas, func(sa *saRow) bool {
		return sa.ID != except && sa.Status != StatusDeleted && strings.EqualFold(sa.Name, name)
	})
}

var errUnique = &pgconn.PgError{Code: uniqueViolation, ConstraintName: nameIndex}

func (s *fakeStore) InsertServiceAccount(_ context.Context, arg dbgen.InsertServiceAccountParams) (int64, error) {
	if err := s.fail["InsertServiceAccount"]; err != nil {
		return 0, err
	}
	if s.taken(arg.Name, 0) {
		return 0, errUnique
	}
	sa := &saRow{dbgen.GetServiceAccountRow{ID: s.id(), PublicID: arg.PublicID, Name: arg.Name, Role: arg.Role,
		Status: StatusActive, CreatedAt: arg.Now, Version: 1}}
	s.sas = append(s.sas, sa)
	return sa.ID, nil
}

func (s *fakeStore) UpdateServiceAccount(_ context.Context, arg dbgen.UpdateServiceAccountParams) error {
	if err := s.fail["UpdateServiceAccount"]; err != nil {
		return err
	}
	if s.taken(arg.Name, arg.ID) {
		return errUnique
	}
	sa := s.byID(arg.ID)
	sa.Name, sa.Role = arg.Name, arg.Role
	sa.Version++
	return nil
}

func (s *fakeStore) SetServiceAccountStatus(_ context.Context, arg dbgen.SetServiceAccountStatusParams) error {
	if err := s.fail["SetServiceAccountStatus"]; err != nil {
		return err
	}
	sa := s.byID(arg.ID)
	sa.Status = arg.Status
	sa.Version++
	return nil
}

func (s *fakeStore) DeleteServiceAccount(_ context.Context, arg dbgen.DeleteServiceAccountParams) error {
	if err := s.fail["DeleteServiceAccount"]; err != nil {
		return err
	}
	sa := s.byID(arg.ID)
	sa.Status = StatusDeleted
	sa.Version++
	return nil
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.fail["InsertAuditEntry"]; err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

func (s *fakeStore) lastAudit(t *testing.T) (auditdb.InsertAuditEntryParams, map[string]any) {
	t.Helper()
	if len(s.audit) == 0 {
		t.Fatal("no Audit log entry")
	}
	e := s.audit[len(s.audit)-1]
	var details map[string]any
	if err := json.Unmarshal(e.Details, &details); err != nil {
		t.Fatal(err)
	}
	return e, details
}

// newService is the Service over s with a manual business clock and a Limiter on a manual real clock.
func newService(s *fakeStore) (*Service, *clock.Manual, *clock.Manual, *bytes.Buffer) {
	business, realClock := clock.NewManual(t0), clock.NewManual(t0)
	var log bytes.Buffer
	w := audit.NewWriter(logging.New(&log, logging.LevelInfo), business)
	return New(orgID, s, w, business, roles, NewLimiter(realClock)), business, realClock, &log
}

var byAdmin = Requester{Actor: audit.User(1, "SRAAAAAAAAAAAA"), Transport: audit.TransportUI,
	Address: netip.MustParseAddr("192.0.2.1")}

var admin = Owner{ID: 1, PublicID: "SRAAAAAAAAAAAA"}

// TestGenerate is S-016 step 1: both prefixes, 32 random bytes in lowercase base32, the SHA-256 of the whole value
// stored in place of the value, and the shapes the authenticator accepts.
func TestGenerate(t *testing.T) {
	seen := map[string]bool{}
	for _, prefix := range []string{PrefixPersonal, PrefixServiceAccount} {
		for range 3 {
			v, hash, err := Generate(prefix)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(v, prefix) || len(v) != len(prefix)+52 || seen[v] {
				t.Errorf("token %q", v[:len(prefix)])
			}
			seen[v] = true
			if len(hash) != 32 || !bytes.Equal(hash, Hash(v)) || bytes.Contains([]byte(v), hash) {
				t.Errorf("the hash of a %s token", prefix)
			}
			if strings.ToLower(v) != v {
				t.Errorf("a %s token is not lowercase", prefix)
			}
		}
	}
	pat, _, _ := Generate(PrefixPersonal)
	sat, _, _ := Generate(PrefixServiceAccount)
	integration, _, _ := Generate(PrefixIntegration)
	for value, want := range map[string]string{pat: KindPersonal, sat: KindServiceAccount, integration: "",
		pat[:len(pat)-1]: "", pat + "a": "", PrefixPersonal + strings.Repeat("!", 52): "", "": "",
		"mstr_xyz_" + pat[len(PrefixPersonal):]: ""} {
		kind, ok := kindOf(value)
		if kind != want || ok != (want != "") {
			t.Errorf("kindOf = %q, %v; want %q", kind, ok, want)
		}
	}
}

// TestCreatePersonal is S-016 step 2: the Permissions of a token must be held by its creator (permission_not_held at
// /permissions/<i>), duplicates collapse, the name and the expiry are checked, and the entry api_token.created
// names the token and never carries its value.
func TestCreatePersonal(t *testing.T) {
	s := newStore()
	svc, business, _, log := newService(s)
	held := roles.Permissions(auth.RoleAdmin)
	ctx := t.Context()
	_, err := svc.CreatePersonal(ctx, byAdmin, admin, roles.Permissions(auth.RoleViewer),
		NewPersonal{Name: "x", Permissions: []auth.Permission{"alert-groups:read", "users:write"}})
	if f, ok := errors.AsType[*FieldError](err); !ok || f.Code != CodePermissionNotHeld || f.Pointer != "/permissions/1" {
		t.Errorf("a Permission not held: %v", err)
	}
	past := t0.Add(-time.Second)
	for name, n := range map[string]NewPersonal{
		"no name":     {Name: " ", Permissions: []auth.Permission{"users:read"}},
		"long name":   {Name: strings.Repeat("n", maxNameLength+1), Permissions: []auth.Permission{"users:read"}},
		"past expiry": {Name: "x", Permissions: []auth.Permission{"users:read"}, ExpiresAt: &past},
		"none":        {Name: "x"},
	} {
		if _, err := svc.CreatePersonal(ctx, byAdmin, admin, held, n); !errors.As(err, new(*FieldError)) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(s.tokens) != 0 || len(s.audit) != 0 {
		t.Fatal("a refused token was stored")
	}
	expires := t0.Add(90 * 24 * time.Hour)
	created, err := svc.CreatePersonal(ctx, byAdmin, admin, held, NewPersonal{Name: "laptop-scripts",
		Permissions: []auth.Permission{"users:read", "alert-groups:read", "users:read"}, ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	tok := created.Token
	if !strings.HasPrefix(created.Value, PrefixPersonal) || !strings.HasPrefix(tok.PublicID, "PT") ||
		!slices.Equal(tok.Permissions, []auth.Permission{"alert-groups:read", "users:read"}) ||
		!tok.ExpiresAt.Equal(expires) || !tok.CreatedAt.Equal(t0) {
		t.Errorf("created = %+v", tok)
	}
	row := s.tokens[0]
	if !bytes.Equal(row.hash, Hash(created.Value)) || row.kind != KindPersonal || row.userID != 1 ||
		!slices.Equal(s.perms[row.id], []string{"alert-groups:read", "users:read"}) {
		t.Errorf("stored = %+v %v", row, s.perms[row.id])
	}
	e, details := s.lastAudit(t)
	if e.Action != audit.ActionAPITokenCreated || e.ResourcePublicID.String != tok.PublicID ||
		e.ResourceName.String != "laptop-scripts" || details["owner"] != admin.PublicID ||
		details["expires_at"] != expires.Format(time.RFC3339) || details["kind"] != KindPersonal {
		t.Errorf("entry %s %s", e.Action, e.Details)
	}
	if bytes.Contains(e.Details, []byte(created.Value)) || strings.Contains(log.String(), created.Value) {
		t.Error("the value reached the Audit log or the log")
	}
	business.Advance(time.Hour)
	if _, err := svc.CreatePersonal(ctx, byAdmin, admin, held, NewPersonal{Name: "forever",
		Permissions: []auth.Permission{"users:read"}}); err != nil {
		t.Fatal(err)
	}
	if _, details := s.lastAudit(t); details["expires_at"] != nil {
		t.Errorf("a token without expiry: %v", details)
	}
	list, err := svc.ListPersonal(ctx, 1)
	if err != nil || len(list) != 2 || list[0].Name != "forever" || list[0].ExpiresAt != nil ||
		list[1].PublicID != tok.PublicID || len(list[1].Permissions) != 2 {
		t.Errorf("list = %+v, %v", list, err)
	}
	if other, _ := svc.ListPersonal(ctx, 2); len(other) != 0 {
		t.Errorf("another user's list = %+v", other)
	}
}

// TestRevokePersonal: a token is revoked at once and listed no more; a token of another user, an unknown, malformed
// or already revoked one is ErrNotFound.
func TestRevokePersonal(t *testing.T) {
	s := newStore()
	svc, _, _, _ := newService(s)
	ctx := t.Context()
	created, err := svc.CreatePersonal(ctx, byAdmin, admin, roles.Permissions(auth.RoleAdmin),
		NewPersonal{Name: "ci", Permissions: []auth.Permission{"users:read"}})
	if err != nil {
		t.Fatal(err)
	}
	bob := Owner{ID: 2, PublicID: "SRBBBBBBBBBBBB"}
	for name, f := range map[string]func() error{
		"another user's": func() error { return svc.RevokePersonal(ctx, byAdmin, bob, created.Token.PublicID) },
		"unknown":        func() error { return svc.RevokePersonal(ctx, byAdmin, admin, "PTAAAAAAAAAAAA") },
		"malformed":      func() error { return svc.RevokePersonal(ctx, byAdmin, admin, "x") },
	} {
		if err := f(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := svc.RevokePersonal(ctx, byAdmin, admin, strings.ToLower(created.Token.PublicID)); err != nil {
		t.Fatal(err)
	}
	if e, details := s.lastAudit(t); e.Action != audit.ActionAPITokenRevoked || e.ResourceName.String != "ci" ||
		details["reason"] != "revoked" {
		t.Errorf("entry %s %s", e.Action, e.Details)
	}
	if list, _ := svc.ListPersonal(ctx, 1); len(list) != 0 {
		t.Errorf("a revoked token is listed: %+v", list)
	}
	if _, err := svc.Authenticate(ctx, created.Value, netip.Addr{}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a revoked token: %v", err)
	}
	if err := svc.RevokePersonal(ctx, byAdmin, admin, created.Token.PublicID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked twice: %v", err)
	}
}

// TestServiceAccountLifecycle is S-016 step 3: create, read, list, update with If-Match, disable and enable, tokens,
// delete revoking the tokens; a name taken case-insensitively is ErrNameTaken.
func TestServiceAccountLifecycle(t *testing.T) {
	s := newStore()
	svc, _, _, _ := newService(s)
	ctx := t.Context()
	sa, err := svc.CreateServiceAccount(ctx, byAdmin, ServiceAccountInput{Name: "terraform", Role: auth.RoleAdmin})
	if err != nil || sa.Status != StatusActive || !strings.HasPrefix(sa.PublicID, "SA") || sa.Version != 1 {
		t.Fatalf("create = %+v, %v", sa, err)
	}
	if e, _ := s.lastAudit(t); e.Action != audit.ActionServiceAccountCreated || !strings.Contains(string(e.Diff),
		`"/role"`) {
		t.Errorf("entry %s %s", e.Action, e.Diff)
	}
	if _, err := svc.CreateServiceAccount(ctx, byAdmin, ServiceAccountInput{Name: "TERRAFORM",
		Role: auth.RoleViewer}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a taken name: %v", err)
	}
	for name, in := range map[string]ServiceAccountInput{
		"no name":  {Name: "", Role: auth.RoleViewer},
		"no role":  {Name: "x", Role: "owner"},
		"too long": {Name: strings.Repeat("n", maxNameLength+1), Role: auth.RoleViewer},
	} {
		if _, err := svc.CreateServiceAccount(ctx, byAdmin, in); !errors.As(err, new(*FieldError)) {
			t.Errorf("%s: %v", name, err)
		}
	}
	other, err := svc.CreateServiceAccount(ctx, byAdmin, ServiceAccountInput{Name: "grafana", Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}

	stale := int64(9)
	if _, err := svc.UpdateServiceAccount(ctx, byAdmin, sa.PublicID, &stale, ServiceAccountInput{Name: "tf",
		Role: auth.RoleAdmin}); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("a stale version: %v", err)
	}
	if _, err := svc.UpdateServiceAccount(ctx, byAdmin, sa.PublicID, nil, ServiceAccountInput{Name: "Grafana",
		Role: auth.RoleAdmin}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a rename to a taken name: %v", err)
	}
	updated, err := svc.UpdateServiceAccount(ctx, byAdmin, sa.PublicID, &sa.Version,
		ServiceAccountInput{Name: "tf", Role: auth.RoleResponder})
	if err != nil || updated.Name != "tf" || updated.Role != auth.RoleResponder || updated.Version != 2 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if e, _ := s.lastAudit(t); e.Action != audit.ActionServiceAccountUpdated ||
		!strings.Contains(string(e.Diff), `"before":"terraform"`) {
		t.Errorf("entry %s %s", e.Action, e.Diff)
	}
	entries := len(s.audit)
	if same, err := svc.UpdateServiceAccount(ctx, byAdmin, sa.PublicID, nil, ServiceAccountInput{Name: "tf",
		Role: auth.RoleResponder}); err != nil || same.Version != 2 || len(s.audit) != entries {
		t.Errorf("an update without changes = %+v, %v", same, err)
	}

	created, err := svc.CreateServiceAccountToken(ctx, byAdmin, sa.PublicID, NewToken{Name: "ci"})
	if err != nil || !strings.HasPrefix(created.Value, PrefixServiceAccount) || !strings.HasPrefix(
		created.Token.PublicID, "ST") {
		t.Fatalf("token = %+v, %v", created.Token, err)
	}
	if e, details := s.lastAudit(t); e.Action != audit.ActionAPITokenCreated ||
		details["service_account"] != sa.PublicID || bytes.Contains(e.Details, []byte(created.Value)) {
		t.Errorf("entry %s %s", e.Action, e.Details)
	}
	second, _ := svc.CreateServiceAccountToken(ctx, byAdmin, sa.PublicID, NewToken{Name: "ci-2"})
	if got, _ := svc.GetServiceAccount(ctx, sa.PublicID); got.TokenCount != 2 {
		t.Errorf("token_count = %d", got.TokenCount)
	}
	list, err := svc.ListServiceAccountTokens(ctx, sa.PublicID)
	if err != nil || len(list) != 2 || list[0].Name != "ci-2" {
		t.Errorf("tokens = %+v, %v", list, err)
	}
	if err := svc.RevokeServiceAccountToken(ctx, byAdmin, other.PublicID, created.Token.PublicID); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("revoking through another account: %v", err)
	}
	if err := svc.RevokeServiceAccountToken(ctx, byAdmin, sa.PublicID, created.Token.PublicID); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.lastAudit(t); e.Action != audit.ActionAPITokenRevoked || e.ResourceName.String != "ci" {
		t.Errorf("entry %s", e.Action)
	}
	if _, err := svc.Authenticate(ctx, created.Value, netip.Addr{}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a revoked token: %v", err)
	}

	// Disable stops the tokens until enable; both are idempotent.
	if got, err := svc.DisableServiceAccount(ctx, byAdmin, sa.PublicID); err != nil || got.Status != StatusDisabled {
		t.Fatalf("disable = %+v, %v", got, err)
	}
	if e, _ := s.lastAudit(t); e.Action != audit.ActionServiceAccountDisabled {
		t.Errorf("entry %s", e.Action)
	}
	if _, err := svc.Authenticate(ctx, second.Value, netip.Addr{}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token of a disabled account: %v", err)
	}
	entries = len(s.audit)
	if got, err := svc.DisableServiceAccount(ctx, byAdmin, sa.PublicID); err != nil || got.Status != StatusDisabled ||
		len(s.audit) != entries {
		t.Errorf("disable twice = %+v, %v", got, err)
	}
	if got, err := svc.EnableServiceAccount(ctx, byAdmin, sa.PublicID); err != nil || got.Status != StatusActive {
		t.Fatalf("enable = %+v, %v", got, err)
	}
	if e, _ := s.lastAudit(t); e.Action != audit.ActionServiceAccountEnabled {
		t.Errorf("entry %s", e.Action)
	}
	if id, err := svc.Authenticate(ctx, second.Value, netip.Addr{}); err != nil || !id.IsServiceAccount() {
		t.Errorf("a token of an enabled account: %+v, %v", id, err)
	}

	page, err := svc.ListServiceAccounts(ctx, ListFilter{Limit: 1})
	if err != nil || len(page.ServiceAccounts) != 1 || page.Next == nil || page.ServiceAccounts[0].Name != "tf" {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	page, err = svc.ListServiceAccounts(ctx, ListFilter{Limit: 1, After: page.Next})
	if err != nil || len(page.ServiceAccounts) != 1 || page.Next != nil || page.ServiceAccounts[0].Name != "grafana" {
		t.Fatalf("page 2 = %+v, %v", page, err)
	}

	cur, _ := svc.GetServiceAccount(ctx, sa.PublicID)
	stale = cur.Version - 1
	if err := svc.DeleteServiceAccount(ctx, byAdmin, sa.PublicID, &stale); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("a stale delete: %v", err)
	}
	if err := svc.DeleteServiceAccount(ctx, byAdmin, sa.PublicID, &cur.Version); err != nil {
		t.Fatal(err)
	}
	if e, details := s.lastAudit(t); e.Action != audit.ActionServiceAccountDeleted || details["tokens_revoked"] != 1.0 {
		t.Errorf("entry %s %s", e.Action, e.Details)
	}
	if s.tokens[len(s.tokens)-1].reason != "owner_deleted" {
		t.Errorf("the token of the deleted account: %+v", s.tokens[len(s.tokens)-1])
	}
	for name, f := range map[string]func() error{
		"get":    func() error { _, err := svc.GetServiceAccount(ctx, sa.PublicID); return err },
		"delete": func() error { return svc.DeleteServiceAccount(ctx, byAdmin, sa.PublicID, nil) },
		"enable": func() error { _, err := svc.EnableServiceAccount(ctx, byAdmin, sa.PublicID); return err },
		"tokens": func() error { _, err := svc.ListServiceAccountTokens(ctx, sa.PublicID); return err },
		"token": func() error {
			_, err := svc.CreateServiceAccountToken(ctx, byAdmin, sa.PublicID, NewToken{Name: "x"})
			return err
		},
		"bad id":    func() error { _, err := svc.GetServiceAccount(ctx, "x"); return err },
		"bad token": func() error { return svc.RevokeServiceAccountToken(ctx, byAdmin, other.PublicID, "x") },
		"bad update": func() error {
			_, err := svc.UpdateServiceAccount(ctx, byAdmin, "x", nil, ServiceAccountInput{Name: "y", Role: auth.RoleViewer})
			return err
		},
	} {
		if err := f(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s of a deleted account: %v", name, err)
		}
	}
	if _, err := svc.Authenticate(ctx, second.Value, netip.Addr{}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token of a deleted account: %v", err)
	}
	// The name is free again once the account is deleted.
	if _, err := svc.CreateServiceAccount(ctx, byAdmin, ServiceAccountInput{Name: "terraform",
		Role: auth.RoleViewer}); err != nil {
		t.Errorf("the name of a deleted account: %v", err)
	}
	past := t0.Add(-time.Hour)
	if _, err := svc.CreateServiceAccountToken(ctx, byAdmin, other.PublicID, NewToken{Name: "old",
		ExpiresAt: &past}); !errors.As(err, new(*FieldError)) {
		t.Errorf("a token that expired already: %v", err)
	}
	if _, err := svc.CreateServiceAccountToken(ctx, byAdmin, other.PublicID, NewToken{Name: ""}); !errors.As(err,
		new(*FieldError)) {
		t.Errorf("a token without a name: %v", err)
	}
}

// TestStoreFailures: a failing query fails the operation, changes nothing and records no Audit log entry.
func TestStoreFailures(t *testing.T) {
	boom := errors.New("boom")
	held := roles.Permissions(auth.RoleAdmin)
	ops := map[string]func(*Service, *fakeStore) error{
		"create personal": func(svc *Service, _ *fakeStore) error {
			_, err := svc.CreatePersonal(t.Context(), byAdmin, admin, held, NewPersonal{Name: "x",
				Permissions: []auth.Permission{"users:read"}})
			return err
		},
		"list personal": func(svc *Service, _ *fakeStore) error {
			_, err := svc.ListPersonal(t.Context(), 1)
			return err
		},
		"revoke personal": func(svc *Service, s *fakeStore) error {
			return svc.RevokePersonal(t.Context(), byAdmin, admin, s.tokens[0].publicID)
		},
		"create account": func(svc *Service, _ *fakeStore) error {
			_, err := svc.CreateServiceAccount(t.Context(), byAdmin, ServiceAccountInput{Name: "y", Role: "viewer"})
			return err
		},
		"list accounts": func(svc *Service, _ *fakeStore) error {
			_, err := svc.ListServiceAccounts(t.Context(), ListFilter{Limit: 5})
			return err
		},
		"update account": func(svc *Service, s *fakeStore) error {
			_, err := svc.UpdateServiceAccount(t.Context(), byAdmin, s.sas[0].PublicID, nil,
				ServiceAccountInput{Name: "z", Role: "viewer"})
			return err
		},
		"disable account": func(svc *Service, s *fakeStore) error {
			_, err := svc.DisableServiceAccount(t.Context(), byAdmin, s.sas[0].PublicID)
			return err
		},
		"delete account": func(svc *Service, s *fakeStore) error {
			return svc.DeleteServiceAccount(t.Context(), byAdmin, s.sas[0].PublicID, nil)
		},
		"list tokens": func(svc *Service, s *fakeStore) error {
			_, err := svc.ListServiceAccountTokens(t.Context(), s.sas[0].PublicID)
			return err
		},
		"create token": func(svc *Service, s *fakeStore) error {
			_, err := svc.CreateServiceAccountToken(t.Context(), byAdmin, s.sas[0].PublicID, NewToken{Name: "t"})
			return err
		},
		"revoke token": func(svc *Service, s *fakeStore) error {
			return svc.RevokeServiceAccountToken(t.Context(), byAdmin, s.sas[0].PublicID, s.tokens[1].publicID)
		},
		"authenticate": func(svc *Service, _ *fakeStore) error {
			_, err := svc.Authenticate(t.Context(), satValue, netip.Addr{})
			return err
		},
	}
	for _, method := range []string{"InTx", "InsertToken", "InsertTokenPermissions", "ListUserTokens",
		"RevokeUserToken", "InsertServiceAccount", "ListServiceAccounts", "GetServiceAccount", "LockServiceAccount",
		"UpdateServiceAccount", "SetServiceAccountStatus", "DeleteServiceAccount", "RevokeServiceAccountTokens",
		"ListServiceAccountTokens", "RevokeServiceAccountToken", "InsertAuditEntry", "GetTokenByHash", "TouchToken"} {
		failed := 0
		for name, op := range ops {
			s := seeded(t)
			svc, _, _, _ := newService(s)
			audits := len(s.audit)
			s.fail[method] = boom
			if err := op(svc, s); !errors.Is(err, boom) {
				continue
			}
			failed++
			if len(s.audit) != audits || s.sas[0].Status != StatusActive || s.tokens[0].revoked != nil {
				t.Errorf("%s with %s failing changed the state", name, method)
			}
		}
		if failed == 0 {
			t.Errorf("no operation failed with %s failing", method)
		}
	}
}

// satValue is the value of the Service account token of seeded.
var satValue string

// seeded is a store with a Personal access token of the Admin and a Service account with one token.
func seeded(t *testing.T) *fakeStore {
	t.Helper()
	s := newStore()
	svc, _, _, _ := newService(s)
	if _, err := svc.CreatePersonal(t.Context(), byAdmin, admin, roles.Permissions(auth.RoleAdmin),
		NewPersonal{Name: "p", Permissions: []auth.Permission{"users:read"}}); err != nil {
		t.Fatal(err)
	}
	sa, err := svc.CreateServiceAccount(t.Context(), byAdmin, ServiceAccountInput{Name: "sa", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateServiceAccountToken(t.Context(), byAdmin, sa.PublicID, NewToken{Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	satValue = created.Value
	return s
}
