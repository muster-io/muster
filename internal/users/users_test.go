// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package users

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
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

const orgID = 7

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// fakeStore keeps users in memory; fail makes the named method fail.
type fakeStore struct {
	users   []dbgen.GetUserRow
	created []dbgen.CreateUserParams
	audit   []auditdb.InsertAuditEntryParams
	admins  int64
	fail    map[string]error
}

func (s *fakeStore) InTx(_ context.Context, f func(Queries) error) error {
	if err := s.fail["InTx"]; err != nil {
		return err
	}
	return f(s)
}

func (s *fakeStore) GetUser(_ context.Context, arg dbgen.GetUserParams) (dbgen.GetUserRow, error) {
	if err := s.fail["GetUser"]; err != nil {
		return dbgen.GetUserRow{}, err
	}
	for _, u := range s.users {
		if u.ID == arg.ID && arg.OrgID == orgID {
			return u, nil
		}
	}
	return dbgen.GetUserRow{}, pgx.ErrNoRows
}

func (s *fakeStore) UpdateProfile(_ context.Context, arg dbgen.UpdateProfileParams) (int64, error) {
	if err := s.fail["UpdateProfile"]; err != nil {
		return 0, err
	}
	for i, u := range s.users {
		if u.ID == arg.ID {
			s.users[i].Name, s.users[i].TimeZone, s.users[i].Language = arg.Name, arg.TimeZone, arg.Language
			s.users[i].Version++
			return 1, nil
		}
	}
	return 0, nil
}

func (s *fakeStore) CountAdmins(_ context.Context, id int64) (int64, error) {
	if id != orgID {
		return 0, errors.New("wrong organization")
	}
	return s.admins, s.fail["CountAdmins"]
}

func (s *fakeStore) CreateUser(_ context.Context, arg dbgen.CreateUserParams) (int64, error) {
	if err := s.fail["CreateUser"]; err != nil {
		return 0, err
	}
	s.created = append(s.created, arg)
	return int64(len(s.created)), nil
}

func (s *fakeStore) InsertAuditEntry(_ context.Context, arg auditdb.InsertAuditEntryParams) error {
	if err := s.fail["InsertAuditEntry"]; err != nil {
		return err
	}
	s.audit = append(s.audit, arg)
	return nil
}

func alice() dbgen.GetUserRow {
	return dbgen.GetUserRow{
		ID: 1, PublicID: "SRAAAAAAAAAAAA", Login: "Alice@Example.org", Name: "Alice",
		Email: pgtype.Text{String: "alice@example.org", Valid: true}, Role: "responder", Source: "local",
		Status: "active", HasPassword: true, TimeZone: pgtype.Text{String: "Europe/Berlin", Valid: true},
		LastSignInAt: pgtype.Timestamptz{Time: t0, Valid: true}, CreatedAt: t0, Version: 3,
	}
}

func newService(s *fakeStore) (*Service, *bytes.Buffer) {
	var log bytes.Buffer
	if s.fail == nil {
		s.fail = map[string]error{}
	}
	w := audit.NewWriter(logging.New(&log, logging.LevelInfo), clock.NewManual(t0))
	return NewService(orgID, s, w, clock.NewManual(t0)), &log
}

func ptr(s string) *string { return &s }

func TestGet(t *testing.T) {
	svc, _ := newService(&fakeStore{users: []dbgen.GetUserRow{alice()}})
	u, err := svc.Get(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if u.PublicID != "SRAAAAAAAAAAAA" || u.Email != "alice@example.org" || *u.TimeZone != "Europe/Berlin" ||
		u.Language != nil || !u.LastSignInAt.Equal(t0) || u.SignInMethod() != "local" || u.ETag() != `"3"` {
		t.Errorf("user = %+v", u)
	}
	if _, err := svc.Get(t.Context(), 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	u.HasOIDCIdentity = true
	if u.SignInMethod() != "oidc" {
		t.Error("an account with an identity signs in through OIDC")
	}
	s := &fakeStore{fail: map[string]error{"GetUser": errors.New("boom")}}
	svc, _ = newService(s)
	if _, err := svc.Get(t.Context(), 1); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("a failed read: %v", err)
	}
}

// TestUpdateProfile is C-03.FR-12: the name, the time zone and the language change, with an Audit log entry whose
// diff shows what changed; invalid values are field errors.
func TestUpdateProfile(t *testing.T) {
	s := &fakeStore{users: []dbgen.GetUserRow{alice()}}
	svc, log := newService(s)
	addr := netip.MustParseAddr("192.0.2.1")
	u, err := svc.UpdateProfile(t.Context(), 1, audit.User(1, "SRAAAAAAAAAAAA"),
		Profile{Name: " Alice B ", TimeZone: nil, Language: ptr("ru")}, addr)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "Alice B" || u.TimeZone != nil || *u.Language != "ru" || u.Version != 4 {
		t.Errorf("user = %+v", u)
	}
	e := s.audit[0]
	if e.Action != audit.ActionProfileUpdated || e.Transport != "ui" || e.ActorUserID.Int64 != 1 ||
		string(e.Diff) != `[{"pointer":"/name","before":"Alice","after":"Alice B"},`+
			`{"pointer":"/time_zone","before":"Europe/Berlin"},{"pointer":"/language","after":"ru"}]` {
		t.Errorf("entry %s diff %s", e.Action, e.Diff)
	}
	if !strings.Contains(log.String(), `"action":"user.profile_updated"`) {
		t.Error("the entry was not logged")
	}

	for name, c := range map[string]struct {
		p       Profile
		pointer string
		code    string
	}{
		"empty name":   {Profile{Name: "  "}, "/name", "required"},
		"long name":    {Profile{Name: strings.Repeat("я", 201)}, "/name", "too_long"},
		"time zone":    {Profile{Name: "A", TimeZone: ptr("Mars/Base")}, "/time_zone", "invalid_format"},
		"local zone":   {Profile{Name: "A", TimeZone: ptr("Local")}, "/time_zone", "invalid_format"},
		"path zone":    {Profile{Name: "A", TimeZone: ptr("../../etc/passwd")}, "/time_zone", "invalid_format"},
		"empty zone":   {Profile{Name: "A", TimeZone: ptr("")}, "/time_zone", "invalid_format"},
		"language":     {Profile{Name: "A", Language: ptr("de")}, "/language", "invalid_format"},
		"longest zone": {Profile{Name: "A", TimeZone: ptr(strings.Repeat("a", 65))}, "/time_zone", "invalid_format"},
	} {
		_, err := svc.UpdateProfile(t.Context(), 1, audit.User(1, "SRAAAAAAAAAAAA"), c.p, addr)
		fe, ok := errors.AsType[*FieldError](err)
		if !ok || fe.Pointer != c.pointer || fe.Code != c.code || fe.Error() == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (Profile{Name: "Ok", TimeZone: ptr("America/New_York"), Language: ptr("en")}).Validate(); err != nil {
		t.Errorf("a valid profile: %v", err)
	}
}

func TestUpdateProfileFailures(t *testing.T) {
	for _, method := range []string{"InTx", "GetUser", "UpdateProfile", "InsertAuditEntry"} {
		s := &fakeStore{users: []dbgen.GetUserRow{alice()}, fail: map[string]error{method: errors.New("boom")}}
		svc, _ := newService(s)
		if _, err := svc.UpdateProfile(t.Context(), 1, audit.User(1, "SRAAAAAAAAAAAA"), Profile{Name: "A"},
			netip.Addr{}); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	svc, _ := newService(&fakeStore{})
	if _, err := svc.UpdateProfile(t.Context(), 1, audit.User(1, "SRAAAAAAAAAAAA"), Profile{Name: "A"},
		netip.Addr{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	s := &fakeStore{users: []dbgen.GetUserRow{alice()}}
	svc, _ = newService(s)
	s.users[0].ID = 1
	gone := &vanishingStore{fakeStore: s}
	svc = NewService(orgID, gone, svc.audit, svc.clock)
	if _, err := svc.UpdateProfile(t.Context(), 1, audit.User(1, "SRAAAAAAAAAAAA"), Profile{Name: "A"},
		netip.Addr{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a user deleted during the update: %v", err)
	}
}

// vanishingStore updates no row, as when the user went away between the read and the update.
type vanishingStore struct{ *fakeStore }

func (s *vanishingStore) InTx(_ context.Context, f func(Queries) error) error { return f(s) }

func (s *vanishingStore) UpdateProfile(context.Context, dbgen.UpdateProfileParams) (int64, error) {
	return 0, nil
}

// TestEnsureBootstrapAdmin is C-03.FR-22: a new Organization gets a local Admin named after the email, with the
// email as login, recorded by the actor bootstrap; with an Admin the variables are ignored with a warning; without
// variables the missing Admin is a warning.
func TestEnsureBootstrapAdmin(t *testing.T) {
	ctx := t.Context()
	s := &fakeStore{}
	svc, log := newService(s)
	logger := logging.New(log, logging.LevelInfo)
	b := Bootstrap{Email: "ops@example.org", Password: "ops-bootstrap-pass"}
	if err := EnsureBootstrapAdmin(ctx, s, svc.audit, logger, orgID, b, t0); err != nil {
		t.Fatal(err)
	}
	c := s.created[0]
	if c.Login != "ops@example.org" || c.Email.String != "ops@example.org" || c.Name != "ops" || c.Role != "admin" ||
		c.Source != "bootstrap" || !c.CreatedAt.Equal(t0) || !strings.HasPrefix(c.PublicID, "SR") {
		t.Errorf("created %+v", c)
	}
	if ok, err := auth.VerifyPassword(t.Context(), c.PasswordHash.String, "ops-bootstrap-pass"); !ok || err != nil {
		t.Error("the password is not the variable's")
	}
	e := s.audit[0]
	if e.Action != audit.ActionUserCreated || e.ActorKind != "bootstrap" || e.ResourceName.String != "ops" ||
		strings.Contains(string(e.Diff)+string(e.Details), "ops-bootstrap-pass") {
		t.Errorf("entry %+v", e)
	}
	if !strings.Contains(log.String(), `"actor_kind":"bootstrap","transport":"system","action":"user.created"`) {
		t.Errorf("log = %s", log)
	}

	log.Reset()
	s.admins = 1
	if err := EnsureBootstrapAdmin(ctx, s, svc.audit, logger, orgID, b, t0); err != nil {
		t.Fatal(err)
	}
	if len(s.created) != 1 || !strings.Contains(log.String(), `"event":"bootstrap_admin_ignored"`) ||
		!strings.Contains(log.String(), "MUSTER_BOOTSTRAP_ADMIN_EMAIL") {
		t.Errorf("with an Admin: %d created, log %s", len(s.created), log)
	}
	log.Reset()
	if err := EnsureBootstrapAdmin(ctx, s, svc.audit, logger, orgID, Bootstrap{}, t0); err != nil ||
		log.Len() != 0 {
		t.Errorf("with an Admin and no variables: %v, %s", err, log)
	}
	s.admins = 0
	if err := EnsureBootstrapAdmin(ctx, s, svc.audit, logger, orgID, Bootstrap{}, t0); err != nil ||
		!strings.Contains(log.String(), `"event":"bootstrap_admin_missing"`) {
		t.Errorf("no Admin, no variables: %v, %s", err, log)
	}
}

func TestEnsureBootstrapAdminRefuses(t *testing.T) {
	ctx := t.Context()
	logger := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	short := Bootstrap{Email: "ops@example.org", Password: "short-pass"}
	s := &fakeStore{}
	svc, _ := newService(s)
	err := EnsureBootstrapAdmin(ctx, s, svc.audit, logger, orgID, short, t0)
	if err == nil || !strings.Contains(err.Error(), "MUSTER_BOOTSTRAP_ADMIN_PASSWORD") ||
		strings.Contains(err.Error(), "short-pass") {
		t.Errorf("a short password: %v", err)
	}
	err = EnsureBootstrapAdmin(ctx, s, svc.audit, logger, orgID, Bootstrap{Email: "ops@example.org"}, t0)
	if err == nil || !strings.Contains(err.Error(), "MUSTER_BOOTSTRAP_ADMIN_PASSWORD") {
		t.Errorf("no password: %v", err)
	}
	ok := Bootstrap{Email: "ops@example.org", Password: "ops-bootstrap-pass"}
	s.fail["CreateUser"] = &pgconn.PgError{Code: "23505"}
	if err := EnsureBootstrapAdmin(ctx, s, svc.audit, logger, orgID, ok, t0); err == nil ||
		!strings.Contains(err.Error(), "exists but is not an Admin") {
		t.Errorf("a login taken: %v", err)
	}
	for _, method := range []string{"CountAdmins", "CreateUser", "InsertAuditEntry", "InTx"} {
		s := &fakeStore{fail: map[string]error{method: errors.New("boom")}}
		svc, _ := newService(s)
		if err := EnsureBootstrapAdmin(ctx, s, svc.audit, logger, orgID, ok, t0); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
}
