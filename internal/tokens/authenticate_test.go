// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package tokens

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
)

var addr = netip.MustParseAddr("198.51.100.7")

// personal issues a Personal access token of the user ownerID narrowed to perms and returns its value.
func personal(t *testing.T, svc *Service, ownerID int64, perms ...auth.Permission) Created {
	t.Helper()
	held := []auth.Permission{"alert-groups:acknowledge", "alert-groups:read", "service-accounts:write", "users:read",
		"users:write"}
	created, err := svc.CreatePersonal(t.Context(), byAdmin, Owner{ID: ownerID, PublicID: "SRAAAAAAAAAAAA"}, held,
		NewPersonal{Name: "scripts", Permissions: perms})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// TestAuthenticatePersonal is C-04.FR-1 and FR-4: a Personal access token acts as its owner with the Transport api,
// Permissions narrowed to the token's and to the owner's current Role, and the Audit log actor "{user} via token
// {name}". An unknown, malformed, revoked or expired token, an Integration token and a token whose owner is disabled
// or deleted are ErrInvalidToken.
func TestAuthenticatePersonal(t *testing.T) {
	s := newStore()
	svc, business, _, _ := newService(s)
	ctx := t.Context()
	created := personal(t, svc, 1, "users:read", "alert-groups:read")
	id, err := svc.Authenticate(ctx, created.Value, addr)
	if err != nil {
		t.Fatal(err)
	}
	if id.Transport != audit.TransportAPI || id.Session.User.ID != 1 || id.IsServiceAccount() ||
		!slices.Equal(id.Permissions, []auth.Permission{"alert-groups:read", "users:read"}) || id.Can("users:write") {
		t.Errorf("identity = %+v", id)
	}
	if a := id.Actor(); a.Kind != audit.ActorUser || a.ID != 1 || a.TokenName != "scripts" ||
		a.TokenID != s.tokens[0].id {
		t.Errorf("actor = %+v", a)
	}
	// A lower Role shrinks the token with it (C-04.FR-1).
	s.users[1].role = auth.RoleViewer
	if id, err = svc.Authenticate(ctx, created.Value, addr); err != nil ||
		!slices.Equal(id.Permissions, []auth.Permission{"alert-groups:read"}) {
		t.Errorf("after a Role change = %+v, %v", id, err)
	}
	s.users[1].role = auth.RoleAdmin

	integration, _, _ := Generate(PrefixIntegration)
	sat, _, _ := Generate(PrefixServiceAccount)
	for name, value := range map[string]string{
		"unknown": PrefixPersonal + strings.Repeat("a", 52), "integration": integration, "malformed": "mstr_pat_x",
		"empty": "", "unknown service account token": sat, "other kind": PrefixServiceAccount + created.Value[9:],
	} {
		if _, err := svc.Authenticate(ctx, value, addr); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A disabled owner's tokens answer 401 until the owner is enabled again; a deleted owner's too.
	for _, status := range []string{StatusDisabled, StatusDeleted} {
		s.users[1].status = status
		if _, err := svc.Authenticate(ctx, created.Value, addr); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("a %s owner: %v", status, err)
		}
	}
	s.users[1].status = StatusActive
	if _, err := svc.Authenticate(ctx, created.Value, addr); err != nil {
		t.Errorf("an enabled owner: %v", err)
	}
	// Expiry.
	expires := t0.Add(time.Hour)
	expiring, err := svc.CreatePersonal(ctx, byAdmin, admin, roles.Permissions(auth.RoleAdmin),
		NewPersonal{Name: "short", Permissions: []auth.Permission{"users:read"}, ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, expiring.Value, addr); err != nil {
		t.Errorf("before the expiry: %v", err)
	}
	business.Set(expires)
	if _, err := svc.Authenticate(ctx, expiring.Value, addr); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("at the expiry: %v", err)
	}
}

// TestLastUsed is C-04.FR-3: the use of a token and its client address are written at most once a minute.
func TestLastUsed(t *testing.T) {
	s := newStore()
	svc, business, _, _ := newService(s)
	created := personal(t, svc, 1, "users:read")
	for range 3 {
		if _, err := svc.Authenticate(t.Context(), created.Value, addr); err != nil {
			t.Fatal(err)
		}
	}
	row := s.tokens[0]
	if s.touches != 1 || !row.used.Equal(t0) || *row.address != addr {
		t.Fatalf("touches %d, used %v from %v", s.touches, row.used, row.address)
	}
	business.Advance(59 * time.Second)
	_, _ = svc.Authenticate(t.Context(), created.Value, netip.MustParseAddr("192.0.2.99"))
	business.Advance(time.Second)
	_, _ = svc.Authenticate(t.Context(), created.Value, netip.Addr{})
	if s.touches != 2 || !row.used.Equal(t0.Add(time.Minute)) || row.address != nil {
		t.Errorf("touches %d, used %v from %v", s.touches, row.used, row.address)
	}
	list, _ := svc.ListPersonal(t.Context(), 1)
	if len(list) != 1 || list[0].LastUsedAt == nil || list[0].LastUsedAddress.IsValid() {
		t.Errorf("list = %+v", list)
	}
}

// TestAuthenticateServiceAccount is C-04.FR-2: a Service account token acts as the account with its Role's
// Permissions, and as the account and its token in the Audit log.
func TestAuthenticateServiceAccount(t *testing.T) {
	s := newStore()
	svc, _, _, _ := newService(s)
	sa, err := svc.CreateServiceAccount(t.Context(), byAdmin, ServiceAccountInput{Name: "ci", Role: auth.RoleResponder})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateServiceAccountToken(t.Context(), byAdmin, sa.PublicID, NewToken{Name: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.Authenticate(t.Context(), created.Value, addr)
	if err != nil {
		t.Fatal(err)
	}
	if !id.IsServiceAccount() || id.Token.ServiceAccount.PublicID != sa.PublicID || id.Session.User.ID != 0 ||
		!slices.Equal(id.Permissions, roles.Permissions(auth.RoleResponder)) {
		t.Errorf("identity = %+v", id)
	}
	if a := id.Actor(); a.Kind != audit.ActorServiceAccount || a.ID != sa.ID || a.TokenName != "deploy" {
		t.Errorf("actor = %+v", a)
	}
}

// TestOIDCRules is C-04.FR-8 and C-03.FR-30 with a manual clock: for an owner without an offline token the token is
// refused with ErrOIDCRecheckRequired once auth.oidc_token_grace passed since the last OIDC sign-in and works again
// after the next one; with an offline token there is no grace, and a refusal at a re-check refuses the token until
// the next OIDC sign-in. Nothing is revoked, and a Service account token is never affected.
func TestOIDCRules(t *testing.T) {
	s := newStore()
	svc, business, _, _ := newService(s)
	ctx := t.Context()
	signedIn := t0
	olga := &owner{publicID: "SROOOOOOOOOOOO", role: auth.RoleResponder, status: StatusActive, oidc: true,
		lastContact: &signedIn}
	s.users[3] = olga
	created := personal(t, svc, 3, "alert-groups:read")
	sa, _ := svc.CreateServiceAccount(ctx, byAdmin, ServiceAccountInput{Name: "old", Role: auth.RoleViewer})
	sat, _ := svc.CreateServiceAccountToken(ctx, byAdmin, sa.PublicID, NewToken{Name: "old"})

	business.Set(t0.Add(7 * 24 * time.Hour))
	if _, err := svc.Authenticate(ctx, created.Value, addr); err != nil {
		t.Errorf("at the end of the grace: %v", err)
	}
	business.Advance(time.Second)
	if _, err := svc.Authenticate(ctx, created.Value, addr); !errors.Is(err, ErrOIDCRecheckRequired) {
		t.Errorf("past the grace: %v", err)
	}
	if _, err := svc.Authenticate(ctx, sat.Value, addr); err != nil {
		t.Errorf("a Service account token of the same age: %v", err)
	}
	if s.tokens[0].revoked != nil {
		t.Error("the token was revoked")
	}
	// The next OIDC sign-in makes the same token work again.
	again := business.Now()
	olga.lastContact = &again
	if _, err := svc.Authenticate(ctx, created.Value, addr); err != nil {
		t.Errorf("after the next OIDC sign-in: %v", err)
	}
	// With an offline token the re-checks stand in for her activity: no grace.
	olga.offline = true
	business.Advance(30 * 24 * time.Hour)
	if _, err := svc.Authenticate(ctx, created.Value, addr); err != nil {
		t.Errorf("with an offline token past the grace: %v", err)
	}
	// The identity provider refuses her at a re-check, a minute after her last contact, while Muster still holds
	// her offline token: the refusal alone refuses the token.
	refused := business.Now()
	contact := refused.Add(-time.Minute)
	olga.refusedAt, olga.lastContact = &refused, &contact
	if _, err := svc.Authenticate(ctx, created.Value, addr); !errors.Is(err, ErrOIDCRecheckRequired) {
		t.Errorf("after a refusal: %v", err)
	}
	olga.refusedAt, olga.lastContact, olga.offline = nil, &refused, true
	if _, err := svc.Authenticate(ctx, created.Value, addr); err != nil {
		t.Errorf("after the next OIDC sign-in: %v", err)
	}
	// An OIDC account that never signed in through OIDC has no grace to count from.
	olga.offline, olga.lastContact = false, nil
	if _, err := svc.Authenticate(ctx, created.Value, addr); !errors.Is(err, ErrOIDCRecheckRequired) {
		t.Errorf("without an OIDC sign-in: %v", err)
	}
}

// TestRateLimitedAuthentication is C-04.FR-5: a token over api.rate_limit is a RateLimitedError while another token
// is unaffected; invalid tokens never take from a bucket.
func TestRateLimitedAuthentication(t *testing.T) {
	s := newStore()
	svc, _, realClock, _ := newService(s)
	a, b := personal(t, svc, 1, "users:read"), personal(t, svc, 1, "users:read")
	for range 2 * RateBurst {
		if _, err := svc.Authenticate(t.Context(), PrefixPersonal+strings.Repeat("a", 52), addr); !errors.Is(err,
			ErrInvalidToken) {
			t.Fatalf("an unknown token: %v", err)
		}
	}
	for i := range RateBurst {
		if _, err := svc.Authenticate(t.Context(), a.Value, addr); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	_, err := svc.Authenticate(t.Context(), a.Value, addr)
	if rl, ok := errors.AsType[*RateLimitedError](err); !ok || rl.Seconds() != 1 {
		t.Fatalf("over the burst: %v", err)
	}
	if _, err := svc.Authenticate(t.Context(), b.Value, addr); err != nil {
		t.Errorf("another token: %v", err)
	}
	realClock.Advance(time.Second / RateLimit)
	if _, err := svc.Authenticate(t.Context(), a.Value, addr); err != nil {
		t.Errorf("after the refill: %v", err)
	}
}
