// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package users

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
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
	"github.com/muster-io/muster/internal/users/dbgen"
)

// setupRow is a password setup link of adminStore.
type setupRow struct {
	id         int64
	userID     int64
	hash       []byte
	createdBy  pgtype.Int8
	expiresAt  time.Time
	used       bool
	superseded bool
}

// adminStore keeps users, sessions and setup links in memory like the queries do; fail makes a method fail.
type adminStore struct {
	users    []dbgen.GetUserRow
	setups   []setupRow
	sessions map[int64]int64 // live sessions per user
	reasons  []string
	audit    []auditdb.InsertAuditEntryParams
	fail     map[string]error
	locked   []string
	// roleSync is whether OIDC and oidc.sync_role are both on; dropped are the users whose re-check was removed.
	roleSync bool
	dropped  []int64
	// tokens are the usable Personal access tokens per user, revoked the ones revoked with owner_deleted.
	tokens  map[int64]int64
	revoked map[int64]int64
}

func (s *adminStore) InTx(_ context.Context, f func(AdminQueries) error) error {
	if err := s.fail["InTx"]; err != nil {
		return err
	}
	// The transaction rolls back on an error: keep a copy to restore.
	users, setups := slices.Clone(s.users), slices.Clone(s.setups)
	sessions := map[int64]int64{}
	for k, v := range s.sessions {
		sessions[k] = v
	}
	audits := len(s.audit)
	if err := f(s); err != nil {
		s.users, s.setups, s.sessions, s.audit = users, setups, sessions, s.audit[:audits]
		return err
	}
	return nil
}

func (s *adminStore) user(id int64) *dbgen.GetUserRow {
	for i := range s.users {
		if s.users[i].ID == id {
			return &s.users[i]
		}
	}
	return nil
}

func (s *adminStore) GetUser(_ context.Context, arg dbgen.GetUserParams) (dbgen.GetUserRow, error) {
	if err := s.fail["GetUser"]; err != nil {
		return dbgen.GetUserRow{}, err
	}
	if u := s.user(arg.ID); u != nil && arg.OrgID == orgID {
		return *u, nil
	}
	return dbgen.GetUserRow{}, pgx.ErrNoRows
}

func (s *adminStore) GetUserByPublicID(_ context.Context, arg dbgen.GetUserByPublicIDParams) (
	dbgen.GetUserByPublicIDRow, error) {
	if err := s.fail["GetUserByPublicID"]; err != nil {
		return dbgen.GetUserByPublicIDRow{}, err
	}
	for _, u := range s.users {
		if u.PublicID == arg.PublicID && arg.OrgID == orgID {
			return dbgen.GetUserByPublicIDRow(u), nil
		}
	}
	return dbgen.GetUserByPublicIDRow{}, pgx.ErrNoRows
}

func (s *adminStore) GetUserByLogin(_ context.Context, arg dbgen.GetUserByLoginParams) (dbgen.GetUserByLoginRow,
	error) {
	if err := s.fail["GetUserByLogin"]; err != nil {
		return dbgen.GetUserByLoginRow{}, err
	}
	for _, u := range s.users {
		if strings.EqualFold(u.Login, arg.Login) {
			return dbgen.GetUserByLoginRow{ID: u.ID, PublicID: u.PublicID, Name: u.Name, Status: u.Status,
				HasOidcIdentity: u.HasOidcIdentity}, nil
		}
	}
	return dbgen.GetUserByLoginRow{}, pgx.ErrNoRows
}

func (s *adminStore) ListUsers(_ context.Context, arg dbgen.ListUsersParams) ([]dbgen.ListUsersRow, error) {
	if err := s.fail["ListUsers"]; err != nil {
		return nil, err
	}
	var out []dbgen.ListUsersRow
	for _, u := range s.users {
		name := strings.ToLower(u.Name)
		if arg.Role.Valid && u.Role != arg.Role.String || arg.Status.Valid && u.Status != arg.Status.String ||
			arg.Source.Valid && u.Source != arg.Source.String ||
			arg.Q.Valid && !strings.Contains(name+" "+strings.ToLower(u.Login), strings.ToLower(arg.Q.String)) ||
			arg.AfterName.Valid && (name < arg.AfterName.String || name == arg.AfterName.String && u.ID <= arg.AfterID.Int64) {
			continue
		}
		out = append(out, dbgen.ListUsersRow{ID: u.ID, PublicID: u.PublicID, Login: u.Login, Name: u.Name,
			Email: u.Email, Role: u.Role, Source: u.Source, Status: u.Status, Version: u.Version, SortName: name})
	}
	slices.SortFunc(out, func(a, b dbgen.ListUsersRow) int {
		if c := strings.Compare(a.SortName, b.SortName); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	})
	return out[:min(len(out), int(arg.PageSize))], nil
}

func (s *adminStore) LockActiveAdmins(context.Context, int64) ([]int64, error) {
	if err := s.fail["LockActiveAdmins"]; err != nil {
		return nil, err
	}
	s.locked = append(s.locked, "admins")
	var ids []int64
	for _, u := range s.users {
		if u.Role == auth.RoleAdmin && u.Status == StatusActive {
			ids = append(ids, u.ID)
		}
	}
	return ids, nil
}

func (s *adminStore) LockUser(_ context.Context, arg dbgen.LockUserParams) (int64, error) {
	if err := s.fail["LockUser"]; err != nil {
		return 0, err
	}
	s.locked = append(s.locked, arg.PublicID)
	for _, u := range s.users {
		if u.PublicID == arg.PublicID {
			return u.ID, nil
		}
	}
	return 0, pgx.ErrNoRows
}

func (s *adminStore) CreateUser(_ context.Context, arg dbgen.CreateUserParams) (int64, error) {
	if err := s.fail["CreateUser"]; err != nil {
		return 0, err
	}
	for _, u := range s.users {
		if strings.EqualFold(u.Login, arg.Login) {
			return 0, &pgconn.PgError{Code: uniqueViolation, ConstraintName: loginIndex}
		}
	}
	id := int64(len(s.users) + 1)
	s.users = append(s.users, dbgen.GetUserRow{ID: id, PublicID: arg.PublicID, Login: arg.Login, Name: arg.Name,
		Email: arg.Email, Role: arg.Role, Source: arg.Source, Status: StatusActive, CreatedAt: arg.CreatedAt, Version: 1})
	return id, nil
}

func (s *adminStore) UpdateUser(_ context.Context, arg dbgen.UpdateUserParams) (int64, error) {
	if err := s.fail["UpdateUser"]; err != nil {
		return 0, err
	}
	u := s.user(arg.ID)
	if u == nil || u.Version != arg.Version || u.Status == StatusDeleted {
		return 0, nil
	}
	u.Name, u.Email, u.Role = arg.Name, arg.Email, arg.Role
	u.Version++
	return 1, nil
}

func (s *adminStore) SetUserStatus(_ context.Context, arg dbgen.SetUserStatusParams) (int64, error) {
	if err := s.fail["SetUserStatus"]; err != nil {
		return 0, err
	}
	u := s.user(arg.ID)
	u.Status = arg.Status
	u.Version++
	return 1, nil
}

func (s *adminStore) PseudonymizeUser(_ context.Context, arg dbgen.PseudonymizeUserParams) (int64, error) {
	if err := s.fail["PseudonymizeUser"]; err != nil {
		return 0, err
	}
	u := s.user(arg.ID)
	u.Status, u.Name, u.Login, u.Email, u.HasPassword = StatusDeleted, arg.Pseudonym, arg.Pseudonym, pgtype.Text{}, false
	u.Version++
	return 1, nil
}

func (s *adminStore) SetUserPassword(_ context.Context, arg dbgen.SetUserPasswordParams) (int64, error) {
	if err := s.fail["SetUserPassword"]; err != nil {
		return 0, err
	}
	u := s.user(arg.ID)
	if u == nil || u.Status == StatusDeleted || u.HasOidcIdentity {
		return 0, nil
	}
	u.HasPassword = arg.PasswordHash.Valid
	u.Version++
	return 1, nil
}

func (s *adminStore) RoleSyncOn(context.Context, int64) (bool, error) {
	return s.roleSync, s.fail["RoleSyncOn"]
}

func (s *adminStore) ConvertToLocal(_ context.Context, arg dbgen.ConvertToLocalParams) (int64, error) {
	if err := s.fail["ConvertToLocal"]; err != nil {
		return 0, err
	}
	u := s.user(arg.ID)
	if u == nil || u.Status == StatusDeleted || !u.HasOidcIdentity {
		return 0, nil
	}
	u.HasOidcIdentity, u.HasOfflineToken, u.RoleLocked = false, false, false
	u.Version++
	return 1, nil
}

func (s *adminStore) ResetUserPassword(_ context.Context, arg dbgen.ResetUserPasswordParams) (int64, error) {
	if err := s.fail["ResetUserPassword"]; err != nil {
		return 0, err
	}
	u := s.user(arg.ID)
	if u == nil || u.Status == StatusDeleted {
		return 0, nil
	}
	u.HasPassword, u.HasOidcIdentity, u.HasOfflineToken, u.RoleLocked = true, false, false, false
	u.Version++
	return 1, nil
}

func (s *adminStore) DeleteOIDCCheck(_ context.Context, arg dbgen.DeleteOIDCCheckParams) error {
	if err := s.fail["DeleteOIDCCheck"]; err != nil {
		return err
	}
	s.dropped = append(s.dropped, arg.UserID)
	return nil
}

func (s *adminStore) EndSessionsOfUser(_ context.Context, arg dbgen.EndSessionsOfUserParams) (int64, error) {
	if err := s.fail["EndSessionsOfUser"]; err != nil {
		return 0, err
	}
	n := s.sessions[arg.UserID]
	delete(s.sessions, arg.UserID)
	s.reasons = append(s.reasons, arg.EndReason.String)
	return n, nil
}

func (s *adminStore) RevokeTokensOfUser(_ context.Context, arg dbgen.RevokeTokensOfUserParams) (int64, error) {
	if err := s.fail["RevokeTokensOfUser"]; err != nil {
		return 0, err
	}
	n := s.tokens[arg.UserID.Int64]
	delete(s.tokens, arg.UserID.Int64)
	if s.revoked == nil {
		s.revoked = map[int64]int64{}
	}
	s.revoked[arg.UserID.Int64] += n
	return n, nil
}

func (s *adminStore) InsertPasswordSetup(_ context.Context, arg dbgen.InsertPasswordSetupParams) error {
	if err := s.fail["InsertPasswordSetup"]; err != nil {
		return err
	}
	s.setups = append(s.setups, setupRow{id: int64(len(s.setups) + 1), userID: arg.UserID, hash: arg.TokenHash,
		createdBy: arg.CreatedByUserID, expiresAt: arg.ExpiresAt})
	return nil
}

func (s *adminStore) SupersedePasswordSetups(_ context.Context, arg dbgen.SupersedePasswordSetupsParams) (int64,
	error) {
	if err := s.fail["SupersedePasswordSetups"]; err != nil {
		return 0, err
	}
	var n int64
	for i := range s.setups {
		if p := &s.setups[i]; p.userID == arg.UserID && !p.used && !p.superseded {
			p.superseded = true
			n++
		}
	}
	return n, nil
}

func (s *adminStore) GetPasswordSetup(_ context.Context, arg dbgen.GetPasswordSetupParams) (dbgen.GetPasswordSetupRow,
	error) {
	if err := s.fail["GetPasswordSetup"]; err != nil {
		return dbgen.GetPasswordSetupRow{}, err
	}
	for _, p := range s.setups {
		if bytes.Equal(p.hash, arg.TokenHash) {
			u := s.user(p.userID)
			return dbgen.GetPasswordSetupRow{ID: p.id, UserID: p.userID, ExpiresAt: p.expiresAt,
				UsedAt: pgtype.Timestamptz{Time: t0, Valid: p.used}, SupersededAt: pgtype.Timestamptz{Time: t0, Valid: p.superseded},
				UserPublicID: u.PublicID, UserName: u.Name}, nil
		}
	}
	return dbgen.GetPasswordSetupRow{}, pgx.ErrNoRows
}

func (s *adminStore) MarkPasswordSetupUsed(_ context.Context, arg dbgen.MarkPasswordSetupUsedParams) (int64, error) {
	if err := s.fail["MarkPasswordSetupUsed"]; err != nil {
		return 0, err
	}
	s.setups[arg.ID-1].used = true
	return 1, nil
}

func (s *adminStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.fail["InsertAuditEntry"]; err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

const (
	adminID = "SRADM1N0000000"
	bobID   = "SRB0B000000000"
)

func adminRow(id int64, publicID, name string) dbgen.GetUserRow {
	return dbgen.GetUserRow{ID: id, PublicID: publicID, Login: name + "@example.org", Name: name,
		Email: pgtype.Text{String: name + "@example.org", Valid: true}, Role: auth.RoleAdmin, Source: SourceLocal,
		Status: StatusActive, HasPassword: true, CreatedAt: t0, Version: 1}
}

func newAdmin(t *testing.T, s *adminStore) (*Admin, *clock.Manual) {
	t.Helper()
	if s.fail == nil {
		s.fail = map[string]error{}
	}
	if s.sessions == nil {
		s.sessions = map[int64]int64{}
	}
	c := clock.NewManual(t0)
	w := audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), c)
	base, _ := url.Parse("https://muster.example.org/base")
	return NewAdmin(orgID, s, w, c, base), c
}

var byAdmin = Requester{Actor: audit.User(1, adminID), Transport: audit.TransportUI,
	Address: netip.MustParseAddr("192.0.2.1")}

func lastAudit(t *testing.T, s *adminStore) (auditdb.InsertAuditEntryParams, []audit.Change) {
	t.Helper()
	if len(s.audit) == 0 {
		t.Fatal("no Audit log entry")
	}
	e := s.audit[len(s.audit)-1]
	var diff []audit.Change
	if err := json.Unmarshal(e.Diff, &diff); err != nil {
		t.Fatal(err)
	}
	return e, diff
}

func tokenOf(t *testing.T, link SetupLink) string {
	t.Helper()
	u, err := url.Parse(link.URL)
	if err != nil {
		t.Fatal(err)
	}
	token, ok := strings.CutPrefix(u.Fragment, "token=")
	if !ok || u.Path != "/base/password-setup" || u.RawQuery != "" {
		t.Fatalf("link %s", link.URL)
	}
	return token
}

// TestCreate is C-03.FR-3 and C-03.AC-19: a local user without a password, its login kept as entered, a setup link in
// the URL fragment valid for 24 hours, and user.created with the diff of the values.
func TestCreate(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin")}}
	a, _ := newAdmin(t, s)
	u, link, err := a.Create(t.Context(), byAdmin, NewUser{Name: " Alice Smith ", Login: "Alice.Smith",
		Email: ptr(" alice@example.org "), Role: auth.RoleResponder})
	if err != nil {
		t.Fatal(err)
	}
	if u.Login != "Alice.Smith" || u.Name != "Alice Smith" || u.Email != "alice@example.org" || u.HasPassword ||
		u.Source != SourceLocal || u.Status != StatusActive || !strings.HasPrefix(u.PublicID, "SR") {
		t.Errorf("user = %+v", u)
	}
	token := tokenOf(t, link)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token %q: %v", token, err)
	}
	hash := sha256.Sum256(raw)
	if p := s.setups[0]; !bytes.Equal(p.hash, hash[:]) || !p.expiresAt.Equal(t0.Add(24*time.Hour)) ||
		!link.ExpiresAt.Equal(p.expiresAt) || p.createdBy.Int64 != 1 {
		t.Errorf("setup = %+v, link %+v", p, link)
	}
	e, diff := lastAudit(t, s)
	if e.Action != audit.ActionUserCreated || e.ResourcePublicID.String != u.PublicID || e.ActorUserID.Int64 != 1 ||
		e.Transport != "ui" || len(diff) != 5 || diff[0].Pointer != "/name" || diff[0].Before != nil {
		t.Errorf("entry %+v, diff %+v", e, diff)
	}
	if _, _, err := a.Create(t.Context(), byAdmin, NewUser{Name: "Other", Login: "ALICE.SMITH",
		Role: auth.RoleViewer}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a login in another case: %v", err)
	}
	if len(s.users) != 2 || len(s.audit) != 1 {
		t.Errorf("a refused create changed something: %d users, %d entries", len(s.users), len(s.audit))
	}
	s.fail["CreateUser"] = &pgconn.PgError{Code: uniqueViolation, ConstraintName: "users_org_id_public_id_key"}
	if _, _, err := a.Create(t.Context(), byAdmin, NewUser{Name: "C", Login: "carol", Role: auth.RoleViewer}); err == nil ||
		errors.Is(err, ErrNameTaken) {
		t.Errorf("a public_id collision: %v", err)
	}
}

func TestCreateValidates(t *testing.T) {
	for name, c := range map[string]struct {
		n             NewUser
		pointer, code string
	}{
		"empty name":  {NewUser{Name: " ", Login: "a"}, "/name", "required"},
		"empty login": {NewUser{Name: "A"}, "/login", "required"},
		"long login":  {NewUser{Name: "A", Login: strings.Repeat("a", 201)}, "/login", "too_long"},
		"space":       {NewUser{Name: "A", Login: "a b"}, "/login", "invalid_format"},
		"control":     {NewUser{Name: "A", Login: "a\x00"}, "/login", "invalid_format"},
		"reserved":    {NewUser{Name: "A", Login: "Deleted-User-SR1"}, "/login", "invalid_format"},
		"email":       {NewUser{Name: "A", Login: "a", Email: ptr("not an address")}, "/email", "invalid_format"},
		"email name":  {NewUser{Name: "A", Login: "a", Email: ptr("A <a@example.org>")}, "/email", "invalid_format"},
		"long email":  {NewUser{Name: "A", Login: "a", Email: ptr(strings.Repeat("a", 250) + "@b.cd")}, "/email", "invalid_format"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &adminStore{}
			a, _ := newAdmin(t, s)
			_, _, err := a.Create(t.Context(), byAdmin, c.n)
			f, ok := errors.AsType[*FieldError](err)
			if !ok || f.Pointer != c.pointer || f.Code != c.code || f.Error() == "" {
				t.Errorf("err = %v", err)
			}
			if len(s.users) != 0 {
				t.Error("an invalid user was created")
			}
		})
	}
}

func TestCreateFailures(t *testing.T) {
	for _, method := range []string{"CreateUser", "GetUser", "SupersedePasswordSetups", "InsertPasswordSetup",
		"InsertAuditEntry", "InTx"} {
		s := &adminStore{fail: map[string]error{method: errors.New("boom")}}
		a, _ := newAdmin(t, s)
		if _, _, err := a.Create(t.Context(), byAdmin, NewUser{Name: "A", Login: "a", Role: auth.RoleViewer}); err == nil {
			t.Errorf("%s failed but Create did not", method)
		}
		if len(s.users) != 0 || len(s.setups) != 0 {
			t.Errorf("%s: a failed create left rows", method)
		}
	}
}

func TestGetAndList(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "Zed"), adminRow(2, bobID, "bob"),
		adminRow(3, "SRC0000000000C", "Carol")}}
	s.users[1].Role, s.users[2].Status = auth.RoleViewer, StatusDisabled
	a, _ := newAdmin(t, s)
	u, err := a.Get(t.Context(), strings.ToLower(bobID))
	if err != nil || u.PublicID != bobID {
		t.Errorf("Get = %+v, %v", u, err)
	}
	for _, id := range []string{"SRNOSUCHUSER00", "not-an-id", "RT0000000000000"} {
		if _, err := a.Get(t.Context(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%s) = %v", id, err)
		}
	}
	page, err := a.List(t.Context(), ListFilter{Limit: 2})
	if err != nil || len(page.Users) != 2 || page.Users[0].Name != "bob" || page.Users[1].Name != "Carol" ||
		page.Next == nil || *page.Next != (Cursor{Name: "carol", ID: 3}) {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, err = a.List(t.Context(), ListFilter{Limit: 2, After: page.Next})
	if err != nil || len(page.Users) != 1 || page.Users[0].Name != "Zed" || page.Next != nil {
		t.Errorf("last page = %+v, %v", page, err)
	}
	page, _ = a.List(t.Context(), ListFilter{Limit: 50, Role: auth.RoleAdmin, Status: StatusActive, Source: SourceLocal,
		Q: "ZE"})
	if len(page.Users) != 1 || page.Users[0].PublicID != adminID {
		t.Errorf("filtered = %+v", page)
	}
	s.fail["ListUsers"] = errors.New("boom")
	if _, err := a.List(t.Context(), ListFilter{Limit: 1}); err == nil {
		t.Error("a failed list")
	}
	s.fail["GetUserByPublicID"] = errors.New("boom")
	if _, err := a.Get(t.Context(), bobID); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("a failed read: %v", err)
	}
}

// TestUpdate is C-03.FR-3 and FR-9: name, email and Role change at the version of If-Match; a Role change ends the
// sessions and is user.role_changed, other changes user.updated.
func TestUpdate(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), adminRow(2, bobID, "bob")},
		sessions: map[int64]int64{2: 3}}
	s.users[1].Role = auth.RoleViewer
	a, _ := newAdmin(t, s)
	v := int64(1)
	u, err := a.Update(t.Context(), byAdmin, bobID, &v, Changes{Name: "Bob B", Role: auth.RoleViewer})
	if err != nil || u.Name != "Bob B" || u.Email != "bob@example.org" || u.Version != 2 || s.sessions[2] != 3 {
		t.Fatalf("update = %+v, %v", u, err)
	}
	e, diff := lastAudit(t, s)
	if e.Action != audit.ActionUserUpdated || len(diff) != 1 || diff[0].Pointer != "/name" || diff[0].Before != "bob" {
		t.Errorf("entry %s %+v", e.Action, diff)
	}
	if _, err := a.Update(t.Context(), byAdmin, bobID, &v, Changes{Name: "x", Role: auth.RoleViewer}); !errors.Is(err,
		ErrVersionMismatch) {
		t.Errorf("a stale version: %v", err)
	}
	u, err = a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "Bob B", Role: auth.RoleResponder, EmailSet: true})
	if err != nil || u.Role != auth.RoleResponder || u.Email != "" || s.sessions[2] != 0 ||
		s.reasons[len(s.reasons)-1] != auth.EndRoleChanged {
		t.Fatalf("role change = %+v, %v, %v", u, err, s.reasons)
	}
	e, diff = lastAudit(t, s)
	if e.Action != audit.ActionUserRoleChanged || len(diff) != 2 || !strings.Contains(string(e.Details), `"sessions_ended":3`) {
		t.Errorf("entry %s %+v %s", e.Action, diff, e.Details)
	}
	entries := len(s.audit)
	if u, err = a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "Bob B", Role: auth.RoleResponder}); err != nil ||
		u.Version != 3 || len(s.audit) != entries {
		t.Errorf("an update that changes nothing = %+v, %v, %d entries", u, err, len(s.audit)-entries)
	}
	if _, err := a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "", Role: auth.RoleViewer}); err == nil {
		t.Error("an empty name")
	}
	if _, err := a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "B", Role: auth.RoleViewer, EmailSet: true,
		Email: ptr("x")}); err == nil {
		t.Error("an invalid email")
	}
	if _, err := a.Update(t.Context(), byAdmin, "SRNOSUCHUSER00", nil, Changes{Name: "B"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown user: %v", err)
	}
	s.users[1].Status = StatusDeleted
	if _, err := a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "B", Role: auth.RoleViewer}); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("a deleted user: %v", err)
	}
}

func TestUpdateFailures(t *testing.T) {
	for _, method := range []string{"LockActiveAdmins", "LockUser", "GetUser", "UpdateUser", "EndSessionsOfUser",
		"InsertAuditEntry"} {
		s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), adminRow(2, bobID, "bob")},
			fail: map[string]error{method: errors.New("boom")}}
		a, _ := newAdmin(t, s)
		if _, err := a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "B", Role: auth.RoleViewer}); err == nil {
			t.Errorf("%s failed but Update did not", method)
		}
		if s.users[1].Name != "bob" {
			t.Errorf("%s: a failed update changed the user", method)
		}
	}
}

// TestLastAdmin is C-03.FR-31 and C-03.AC-25: with one active Admin, disabling, deleting and lowering the Role of that
// Admin — by themselves too — are ErrLastAdmin and change nothing; with a second active Admin they succeed.
func TestLastAdmin(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), adminRow(2, bobID, "bob")}}
	s.users[1].Status = StatusDisabled // a disabled Admin does not count
	a, _ := newAdmin(t, s)
	refuse := map[string]func() error{
		"disable": func() error { _, err := a.Disable(t.Context(), byAdmin, adminID); return err },
		"delete":  func() error { return a.Delete(t.Context(), byAdmin, adminID, nil) },
		"lower": func() error {
			_, err := a.Update(t.Context(), byAdmin, adminID, nil, Changes{Name: "admin", Role: auth.RoleResponder,
				Email: ptr("admin@example.org"), EmailSet: true})
			return err
		},
	}
	for name, f := range refuse {
		if err := f(); !errors.Is(err, ErrLastAdmin) {
			t.Errorf("%s: %v", name, err)
		}
		if u := s.user(1); u.Role != auth.RoleAdmin || u.Status != StatusActive || u.Version != 1 || len(s.audit) != 0 {
			t.Errorf("%s changed the last Admin: %+v", name, u)
		}
		if s.locked[0] != "admins" {
			t.Errorf("%s locked %v: the Admins first", name, s.locked)
		}
		s.locked = nil
	}
	// Renaming the last Admin keeps the Role: allowed.
	if _, err := a.Update(t.Context(), byAdmin, adminID, nil, Changes{Name: "root", Role: auth.RoleAdmin}); err != nil {
		t.Errorf("rename: %v", err)
	}
	if _, err := a.Enable(t.Context(), byAdmin, bobID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Disable(t.Context(), byAdmin, adminID); err != nil {
		t.Errorf("with a second active Admin, disable: %v", err)
	}
}

// TestDisableEnableDelete is C-03.FR-3, FR-9 and FR-13.
func TestDisableEnableDelete(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), adminRow(2, bobID, "bob")},
		sessions: map[int64]int64{2: 2}}
	s.users[1].Role = auth.RoleViewer
	a, _ := newAdmin(t, s)
	u, err := a.Disable(t.Context(), byAdmin, bobID)
	if err != nil || u.Status != StatusDisabled || s.sessions[2] != 0 || s.reasons[0] != auth.EndUserDisabled {
		t.Fatalf("disable = %+v, %v", u, err)
	}
	e, diff := lastAudit(t, s)
	if e.Action != audit.ActionUserDisabled || len(diff) != 1 || diff[0].After != StatusDisabled {
		t.Errorf("entry %s %+v", e.Action, diff)
	}
	entries := len(s.audit)
	if u, err = a.Disable(t.Context(), byAdmin, bobID); err != nil || u.Status != StatusDisabled || len(s.audit) != entries {
		t.Errorf("disabling a disabled user = %+v, %v", u, err)
	}
	if u, err = a.Enable(t.Context(), byAdmin, bobID); err != nil || u.Status != StatusActive {
		t.Fatalf("enable = %+v, %v", u, err)
	}
	if e, _ = lastAudit(t, s); e.Action != audit.ActionUserEnabled {
		t.Errorf("entry %s", e.Action)
	}
	if _, err := a.CreateSetupLink(t.Context(), byAdmin, bobID); err != nil {
		t.Fatal(err)
	}
	stale := int64(1)
	if err := a.Delete(t.Context(), byAdmin, bobID, &stale); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("a stale If-Match: %v", err)
	}
	s.sessions[2] = 1
	s.tokens = map[int64]int64{2: 2}
	current := s.user(2).Version
	if err := a.Delete(t.Context(), byAdmin, bobID, &current); err != nil {
		t.Fatal(err)
	}
	if s.revoked[2] != 2 || len(s.tokens) != 0 {
		t.Errorf("the tokens of the deleted user: %v revoked, %v left", s.revoked, s.tokens)
	}
	b := s.user(2)
	if b.Status != StatusDeleted || b.Name != "deleted-user-"+bobID || b.Login != b.Name || b.Email.Valid ||
		s.sessions[2] != 0 || s.reasons[len(s.reasons)-1] != auth.EndUserDeleted || !s.setups[0].superseded {
		t.Errorf("deleted = %+v, setups %+v", b, s.setups)
	}
	e, diff = lastAudit(t, s)
	if e.Action != audit.ActionUserDeleted || e.ResourceName.String != b.Name || len(diff) != 3 ||
		strings.Contains(string(e.Diff), "bob@example.org") {
		t.Errorf("entry %s %s %s", e.Action, e.ResourceName.String, e.Diff)
	}
	for name, f := range map[string]func() error{
		"delete":  func() error { return a.Delete(t.Context(), byAdmin, bobID, nil) },
		"enable":  func() error { _, err := a.Enable(t.Context(), byAdmin, bobID); return err },
		"disable": func() error { _, err := a.Disable(t.Context(), byAdmin, bobID); return err },
		"link":    func() error { _, err := a.CreateSetupLink(t.Context(), byAdmin, bobID); return err },
		"bad id":  func() error { return a.Delete(t.Context(), byAdmin, "x", nil) },
	} {
		if err := f(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s of a deleted user: %v", name, err)
		}
	}
}

func TestStatusFailures(t *testing.T) {
	ops := map[string]func(*Admin) error{
		"disable": func(a *Admin) error { _, err := a.Disable(t.Context(), byAdmin, bobID); return err },
		"delete":  func(a *Admin) error { return a.Delete(t.Context(), byAdmin, bobID, nil) },
	}
	for _, method := range []string{"SetUserStatus", "EndSessionsOfUser", "GetUser", "InsertAuditEntry", "LockUser",
		"PseudonymizeUser", "SupersedePasswordSetups", "LockActiveAdmins", "RevokeTokensOfUser"} {
		failed := 0
		for name, op := range ops {
			s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), adminRow(2, bobID, "bob")},
				fail: map[string]error{method: errors.New("boom")}}
			a, _ := newAdmin(t, s)
			if op(a) == nil {
				continue
			}
			failed++
			if s.users[1].Status != StatusActive || len(s.audit) != 0 {
				t.Errorf("%s with %s failing changed the user to %s", name, method, s.users[1].Status)
			}
		}
		if failed == 0 {
			t.Errorf("%s failed but neither Disable nor Delete did", method)
		}
	}
}

// TestSetupLinks is C-03.FR-26 and C-03.AC-17 with a manual clock: unknown 404, expired link_expired, used and
// superseded link_used, too short a FieldError; none sets a password. A good token sets it once and ends the sessions.
func TestSetupLinks(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), adminRow(2, bobID, "bob")},
		sessions: map[int64]int64{2: 1}}
	s.users[1].HasPassword = false
	a, c := newAdmin(t, s)
	addr := netip.MustParseAddr("198.51.100.7")
	first, err := a.CreateSetupLink(t.Context(), byAdmin, bobID)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := lastAudit(t, s); e.Action != audit.ActionPasswordSetupLink || e.ResourcePublicID.String != bobID {
		t.Errorf("entry %s", e.Action)
	}
	second, err := a.CreateSetupLink(t.Context(), byAdmin, bobID)
	if err != nil {
		t.Fatal(err)
	}
	const good = "a-good-password-1"
	for name, c := range map[string]struct {
		token, password string
		want            error
	}{
		"unknown":    {base64.RawURLEncoding.EncodeToString(make([]byte, 32)), good, ErrLinkNotFound},
		"malformed":  {"!!", good, ErrLinkNotFound},
		"short":      {"AAAA", good, ErrLinkNotFound},
		"superseded": {tokenOf(t, first), good, ErrLinkUsed},
	} {
		if err := a.CompleteSetup(t.Context(), c.token, c.password, addr); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	err = a.CompleteSetup(t.Context(), tokenOf(t, second), "short", addr)
	if f, ok := errors.AsType[*FieldError](err); !ok || f.Pointer != "/password" || f.Code != "too_short" {
		t.Errorf("a short password: %v", err)
	}
	if s.user(2).HasPassword || s.setups[1].used {
		t.Fatal("a refused setup set the password or used the link")
	}
	c.Advance(24*time.Hour - time.Second)
	if err := a.CompleteSetup(t.Context(), tokenOf(t, second), good, addr); err != nil {
		t.Fatal(err)
	}
	if !s.user(2).HasPassword || !s.setups[1].used || s.sessions[2] != 0 ||
		s.reasons[len(s.reasons)-1] != auth.EndPasswordChanged {
		t.Errorf("after setup: %+v %+v", s.user(2), s.setups[1])
	}
	e, diff := lastAudit(t, s)
	if e.Action != audit.ActionPasswordSet || e.ActorKind != "system" || e.ResourcePublicID.String != bobID ||
		len(diff) != 1 || !diff[0].SecretChanged || e.SourceAddress == nil || *e.SourceAddress != addr ||
		strings.Contains(string(e.Diff)+string(e.Details), good) {
		t.Errorf("entry %+v", e)
	}
	if err := a.CompleteSetup(t.Context(), tokenOf(t, second), good, addr); !errors.Is(err, ErrLinkUsed) {
		t.Errorf("a used link: %v", err)
	}
	third, err := a.CreateSetupLink(t.Context(), byAdmin, bobID)
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(24 * time.Hour)
	if err := a.CompleteSetup(t.Context(), tokenOf(t, third), good, addr); !errors.Is(err, ErrLinkExpired) {
		t.Errorf("an expired link: %v", err)
	}
	if s.setups[2].used {
		t.Error("an expired link was used")
	}
}

func TestSetupLinkRefusals(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), adminRow(2, bobID, "bob")}}
	s.users[1].HasOidcIdentity = true
	a, _ := newAdmin(t, s)
	if _, err := a.CreateSetupLink(t.Context(), byAdmin, bobID); !errors.Is(err, auth.ErrNotLocal) {
		t.Errorf("an OIDC account: %v", err)
	}
	// A link issued before the account linked OIDC no longer sets a password.
	s.users[1].HasOidcIdentity = false
	link, err := a.CreateSetupLink(t.Context(), byAdmin, bobID)
	if err != nil {
		t.Fatal(err)
	}
	s.users[1].HasOidcIdentity = true
	if err := a.CompleteSetup(t.Context(), tokenOf(t, link), "a-good-password-1", netip.Addr{}); !errors.Is(err,
		ErrLinkUsed) {
		t.Errorf("a link of an OIDC account: %v", err)
	}
	for _, method := range []string{"GetPasswordSetup", "LockUser", "MarkPasswordSetupUsed", "SetUserPassword",
		"EndSessionsOfUser", "InsertAuditEntry"} {
		s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin")}}
		a, _ := newAdmin(t, s)
		link, err := a.CreateSetupLink(t.Context(), byAdmin, adminID)
		if err != nil {
			t.Fatal(err)
		}
		s.fail[method] = errors.New("boom")
		if err := a.CompleteSetup(t.Context(), tokenOf(t, link), "a-good-password-1", netip.Addr{}); err == nil {
			t.Errorf("%s failed but CompleteSetup did not", method)
		}
		if s.setups[0].used {
			t.Errorf("%s: a failed setup used the link", method)
		}
	}
}

// TestResetPassword is C-03.FR-11 and C-02.FR-15: the password is set on the account of the login, compared
// lowercased, its sessions end, its links are superseded, and user.password_reset names the CLI actor.
func TestResetPassword(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), adminRow(2, bobID, "Bob")},
		sessions: map[int64]int64{2: 2}}
	a, _ := newAdmin(t, s)
	if _, err := a.CreateSetupLink(t.Context(), byAdmin, bobID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ResetPassword(t.Context(), "", "bob@example.org", "a-good-password-1"); err == nil {
		t.Error("a reset without an actor")
	}
	if _, err := a.ResetPassword(t.Context(), "ops", "bob@example.org", "short"); !errors.Is(err,
		auth.ErrPasswordTooShort) {
		t.Errorf("a short password: %v", err)
	}
	if s.sessions[2] != 2 || len(s.audit) != 1 {
		t.Fatal("a refused reset changed something")
	}
	id, err := a.ResetPassword(t.Context(), "ops", "BOB@example.org", "a-good-password-1")
	if err != nil || id != bobID {
		t.Fatalf("reset = %s, %v", id, err)
	}
	if s.sessions[2] != 0 || !s.setups[0].superseded || s.user(2).Version != 2 {
		t.Errorf("after reset: sessions %v, setups %+v", s.sessions, s.setups)
	}
	e, diff := lastAudit(t, s)
	if e.Action != audit.ActionPasswordReset || e.ActorKind != "cli" || e.ActorName.String != "ops" ||
		e.Transport != "cli" || len(diff) != 1 || !diff[0].SecretChanged || e.ActorUserID.Valid {
		t.Errorf("entry %+v", e)
	}
	if _, err := a.ResetPassword(t.Context(), "ops", "nobody", "a-good-password-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown login: %v", err)
	}
	s.users[1].Status = StatusDeleted
	if _, err := a.ResetPassword(t.Context(), "ops", "bob@example.org", "a-good-password-1"); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("a deleted account: %v", err)
	}
	// C-03.FR-11, AC-21: on an account that signs in through OIDC the reset removes the identity and the offline
	// token in the same update, with the re-check, and the entry says so.
	s.users[1].Status, s.users[1].HasOidcIdentity, s.users[1].HasPassword = StatusActive, true, false
	s.users[1].HasOfflineToken, s.sessions[2] = true, 1
	if _, err := a.ResetPassword(t.Context(), "ops", "bob@example.org", "a-good-password-1"); err != nil {
		t.Fatalf("a reset of an OIDC account: %v", err)
	}
	if u := s.user(2); u.HasOidcIdentity || u.HasOfflineToken || !u.HasPassword || s.sessions[2] != 0 ||
		!slices.Equal(s.dropped, []int64{2}) {
		t.Errorf("after the reset of an OIDC account: %+v, dropped %v", u, s.dropped)
	}
	e, diff = lastAudit(t, s)
	var details map[string]any
	_ = json.Unmarshal(e.Details, &details)
	if e.ActorName.String != "ops" || details["oidc_identity_removed"] != true || len(diff) != 2 ||
		diff[1].Pointer != "/sign_in_method" || diff[1].After != "local" {
		t.Errorf("entry of the reset of an OIDC account: %+v %s", diff, e.Details)
	}
	for _, method := range []string{"GetUserByLogin", "LockUser", "ResetUserPassword", "EndSessionsOfUser",
		"SupersedePasswordSetups", "InsertAuditEntry"} {
		s.fail = map[string]error{method: errors.New("boom")}
		if _, err := a.ResetPassword(t.Context(), "ops", "bob@example.org", "a-good-password-1"); err == nil {
			t.Errorf("%s failed but the reset did not", method)
		}
	}
	s.fail = map[string]error{}
	s.users[1].Status = StatusDeleted // deleted between the read and the write
	s.fail["GetUserByLogin"] = nil
	racy := &racyLogin{adminStore: s}
	a2, _ := newAdmin(t, &adminStore{})
	a2.store = racy
	if _, err := a2.ResetPassword(t.Context(), "ops", "bob@example.org", "a-good-password-1"); !errors.Is(err,
		ErrNotFound) {
		t.Errorf("an account deleted meanwhile: %v", err)
	}
}

// racyLogin finds the account as active, though it is deleted by the time the password is written.
type racyLogin struct{ *adminStore }

func (r *racyLogin) GetUserByLogin(ctx context.Context, arg dbgen.GetUserByLoginParams) (dbgen.GetUserByLoginRow,
	error) {
	row, err := r.adminStore.GetUserByLogin(ctx, arg)
	row.Status = StatusActive
	return row, err
}

func (r *racyLogin) InTx(ctx context.Context, f func(AdminQueries) error) error {
	return r.adminStore.InTx(ctx, f)
}

type pruneQueries struct {
	arg dbgen.PrunePasswordSetupsParams
	err error
}

func (q *pruneQueries) PrunePasswordSetups(_ context.Context, arg dbgen.PrunePasswordSetupsParams) (int64, error) {
	q.arg = arg
	return 4, q.err
}

// TestPruner: links are deleted auth.password_setup_prune_after past their expiry.
func TestPruner(t *testing.T) {
	q := &pruneQueries{}
	n, err := NewPruner(q).PasswordSetups(t.Context(), orgID, t0, 1000)
	if err != nil || n != 4 || q.arg.OrgID != orgID || !q.arg.Before.Equal(t0.Add(-7*24*time.Hour)) ||
		q.arg.BatchSize != 1000 {
		t.Errorf("= %d, %v, %+v", n, err, q.arg)
	}
	q.err = errors.New("boom")
	if _, err := NewPruner(q).PasswordSetups(t.Context(), orgID, t0, 1000); err == nil {
		t.Error("a failed delete")
	}
}

// usedMeanwhile finds the link usable before the transaction, though another request used it before the lock.
type usedMeanwhile struct{ *adminStore }

func (u *usedMeanwhile) InTx(ctx context.Context, f func(AdminQueries) error) error {
	u.setups[0].used = true
	return u.adminStore.InTx(ctx, f)
}

// TestSetupLinkUsedMeanwhile: the transaction checks the link again under its lock, so two requests with one token
// set the password once.
func TestSetupLinkUsedMeanwhile(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin")}}
	a, _ := newAdmin(t, s)
	link, err := a.CreateSetupLink(t.Context(), byAdmin, adminID)
	if err != nil {
		t.Fatal(err)
	}
	a.store = &usedMeanwhile{s}
	before := s.user(1).Version
	if err := a.CompleteSetup(t.Context(), tokenOf(t, link), "a-good-password-1", netip.Addr{}); !errors.Is(err,
		ErrLinkUsed) {
		t.Errorf("= %v", err)
	}
	if s.user(1).Version != before {
		t.Error("the password was set twice")
	}
}

// TestConvertToLocal is C-03.FR-29 and C-03.AC-21: an Admin's conversion removes the identity and the offline token
// with the re-check, ends the sessions with converted_to_local and returns a password setup link; the TOTP enrolment
// stays. An account without an identity is ErrNotLinked.
func TestConvertToLocal(t *testing.T) {
	alice := adminRow(2, bobID, "Alice")
	alice.Role, alice.HasPassword, alice.HasOidcIdentity, alice.HasOfflineToken, alice.TotpEnabled =
		auth.RoleResponder, false, true, true, true
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), alice}, sessions: map[int64]int64{2: 2}}
	a, _ := newAdmin(t, s)
	u, link, err := a.ConvertToLocal(t.Context(), byAdmin, bobID)
	if err != nil {
		t.Fatal(err)
	}
	if u.SignInMethod() != "local" || u.HasOIDCIdentity || u.OfflineAccess || !u.TOTPEnabled || s.sessions[2] != 0 ||
		!slices.Equal(s.reasons, []string{auth.EndConvertedToLocal}) || !slices.Equal(s.dropped, []int64{2}) ||
		!strings.HasPrefix(link.URL, "https://muster.example.org/base/password-setup#token=") || len(s.setups) != 1 {
		t.Fatalf("converted %+v, link %+v, reasons %v", u, link, s.reasons)
	}
	e, diff := lastAudit(t, s)
	var details map[string]any
	_ = json.Unmarshal(e.Details, &details)
	if e.Action != ActionConvertedToLocal || len(diff) != 1 || diff[0].Before != "oidc" || diff[0].After != "local" ||
		details["sessions_ended"] != 2.0 || details["offline_token_wiped"] != true {
		t.Errorf("entry %+v %s", diff, e.Details)
	}
	if _, _, err := a.ConvertToLocal(t.Context(), byAdmin, bobID); !errors.Is(err, ErrNotLinked) {
		t.Errorf("a second conversion = %v, want ErrNotLinked", err)
	}
	if _, _, err := a.ConvertToLocal(t.Context(), byAdmin, "SRNOBODY000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown user = %v", err)
	}
	for _, method := range []string{"ConvertToLocal", "DeleteOIDCCheck", "EndSessionsOfUser", "InsertPasswordSetup",
		"InsertAuditEntry"} {
		s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), alice}}
		a, _ := newAdmin(t, s)
		s.fail[method] = errors.New("boom")
		if _, _, err := a.ConvertToLocal(t.Context(), byAdmin, bobID); err == nil || !s.user(2).HasOidcIdentity {
			t.Errorf("%s failed but the conversion did not: %v", method, err)
		}
	}
	s.users[1].Status = StatusDeleted
	s.users[1].HasOidcIdentity = true
	if _, _, err := a.ConvertToLocal(t.Context(), byAdmin, bobID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted account = %v", err)
	}
}

// TestRoleLocked is C-03.FR-29: while OIDC and oidc.sync_role are both on, the Role of an account that signs in
// through OIDC is refused with ErrRoleLocked; its name still changes, and a local account's Role changes.
func TestRoleLocked(t *testing.T) {
	olga := adminRow(2, bobID, "Olga")
	olga.Role, olga.HasPassword, olga.HasOidcIdentity, olga.RoleLocked = auth.RoleResponder, false, true, true
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), olga}, roleSync: true}
	a, _ := newAdmin(t, s)
	if _, err := a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "Olga", Role: auth.RoleViewer}); !errors.Is(
		err, ErrRoleLocked) {
		t.Errorf("a Role change of an OIDC account = %v, want ErrRoleLocked", err)
	}
	u, err := a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "Olga K", Role: auth.RoleResponder})
	if err != nil || u.Name != "Olga K" || !u.RoleLocked {
		t.Errorf("a name change of an OIDC account = %+v, %v", u, err)
	}
	s.fail["RoleSyncOn"] = errors.New("boom")
	if _, err := a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "Olga", Role: auth.RoleViewer}); err == nil {
		t.Error("a failed read of the Role sync")
	}
	s.fail = map[string]error{}
	s.roleSync = false
	if u, err := a.Update(t.Context(), byAdmin, bobID, nil, Changes{Name: "Olga", Role: auth.RoleViewer}); err != nil ||
		u.Role != auth.RoleViewer {
		t.Errorf("a Role change while the IdP does not decide it = %+v, %v", u, err)
	}
}

// TestDisableAndDeleteDropTheCheck is C-03.FR-30: disabling or deleting a user removes the re-check with the token.
func TestDisableAndDeleteDropTheCheck(t *testing.T) {
	s := &adminStore{users: []dbgen.GetUserRow{adminRow(1, adminID, "admin"), adminRow(2, bobID, "Bob")}}
	a, _ := newAdmin(t, s)
	if _, err := a.Disable(t.Context(), byAdmin, bobID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Enable(t.Context(), byAdmin, bobID); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(t.Context(), byAdmin, bobID, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.dropped, []int64{2, 2}) {
		t.Errorf("dropped re-checks = %v", s.dropped)
	}
	for _, f := range []func() error{
		func() error { _, err := a.Disable(t.Context(), byAdmin, adminID); return err },
	} {
		s.users = append(s.users, adminRow(3, "SRC0000000000C", "carol"))
		s.fail["DeleteOIDCCheck"] = errors.New("boom")
		if err := f(); err == nil {
			t.Error("a failed removal of the re-check")
		}
	}
	if err := a.Delete(t.Context(), byAdmin, "SRC0000000000C", nil); err == nil {
		t.Error("a delete whose re-check removal failed")
	}
}
