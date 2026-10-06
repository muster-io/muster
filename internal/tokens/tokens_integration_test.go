// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package tokens_test

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/tokens"
	"github.com/muster-io/muster/internal/users"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

var addr = netip.MustParseAddr("192.0.2.10")

// env is a migrated database with the Organization, the Audit log partition of October 2026 and the bootstrap Admin
// ops@example.org, with the tokens, the administration of users and the Audit log reader on one manual business
// clock.
type env struct {
	d      *db.DB
	clock  *clock.Manual
	roles  auth.Roles
	tokens *tokens.Service
	admin  *users.Admin
	reader *audit.Reader
	ops    users.User
	byOps  tokens.Requester
}

func setup(t *testing.T, s dbtest.Server) *env {
	t.Helper()
	ctx := t.Context()
	conn := config.Database{URL: logging.Secret(s.NewDatabase(t)), SSLMode: "disable"}
	d, err := db.Open(ctx, conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	log := logging.New(&bytes.Buffer{}, logging.LevelInfo)
	if err := d.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	if err := organization.Ensure(ctx, organization.NewStore(d.Pool), log, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE audit_log_p202610 PARTITION OF audit_log
		FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	org, err := organization.NewStore(d.Pool).GetOrganization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewManual(t0)
	w := audit.NewWriter(log, c)
	if err := users.EnsureBootstrapAdmin(ctx, users.NewStore(d.Pool), w, log, org.ID,
		users.Bootstrap{Email: "ops@example.org", Password: "ops-bootstrap-pass"}, t0); err != nil {
		t.Fatal(err)
	}
	roles, err := auth.LoadRoles(ctx, auth.NewStore(d.Pool))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("http://localhost:8080")
	e := &env{
		d: d, clock: c, roles: roles,
		tokens: tokens.New(org.ID, tokens.NewStore(d.Pool), w, c, roles, tokens.NewLimiter(clock.NewManual(t0))),
		admin:  users.NewAdmin(org.ID, users.NewAdminStore(d.Pool), w, c, base),
		reader: audit.NewReader(org.ID, audit.NewListQueries(d.Pool)),
	}
	page, err := e.admin.List(ctx, users.ListFilter{Q: "ops@example.org", Limit: 1})
	if err != nil || len(page.Users) != 1 {
		t.Fatalf("the bootstrap Admin: %+v, %v", page, err)
	}
	e.ops = page.Users[0]
	e.byOps = tokens.Requester{Actor: audit.User(e.ops.ID, e.ops.PublicID), Transport: audit.TransportUI, Address: addr}
	return e
}

func (e *env) usersBy() users.Requester {
	return users.Requester{Actor: e.byOps.Actor, Transport: e.byOps.Transport, Address: e.byOps.Address}
}

// create makes a user of role.
func (e *env) create(t *testing.T, login, role string) users.User {
	t.Helper()
	u, _, err := e.admin.Create(t.Context(), e.usersBy(), users.NewUser{Name: login, Login: login, Role: role})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// personal issues a Personal access token of u, narrowed to perms, with the Permissions of u's Role as those held.
func (e *env) personal(t *testing.T, u users.User, name string, perms ...auth.Permission) tokens.Created {
	t.Helper()
	created, err := e.tokens.CreatePersonal(t.Context(), e.byOps, tokens.Owner{ID: u.ID, PublicID: u.PublicID},
		e.roles.Permissions(u.Role), tokens.NewPersonal{Name: name, Permissions: perms})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.d.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// TestIntegrationPersonalAccessTokens is S-016 step 2 against PostgreSQL: the subset rule (permission_not_held), the
// hash-only storage, a Role change shrinking a token, the last use, revoking, and the owner disabled (401 until
// enabled) and deleted (revoked with owner_deleted), C-04.AC-4.
func TestIntegrationPersonalAccessTokens(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		alice := e.create(t, "alice", auth.RoleResponder)
		_, err := e.tokens.CreatePersonal(ctx, e.byOps, tokens.Owner{ID: alice.ID, PublicID: alice.PublicID},
			e.roles.Permissions(alice.Role), tokens.NewPersonal{Name: "x",
				Permissions: []auth.Permission{"alert-groups:read", "users:write"}})
		if f, ok := errors.AsType[*tokens.FieldError](err); !ok || f.Code != tokens.CodePermissionNotHeld ||
			f.Pointer != "/permissions/1" {
			t.Errorf("a Permission alice does not hold: %v", err)
		}
		created := e.personal(t, alice, "scripts", "alert-groups:acknowledge", "alert-groups:read")
		var kind string
		var hashLen int
		var hashMatches bool
		if err := e.d.Pool.QueryRow(ctx, `SELECT kind, octet_length(token_hash), token_hash = sha256($1::bytea)
			FROM api_tokens WHERE name = 'scripts'`, []byte(created.Value)).Scan(&kind, &hashLen,
			&hashMatches); err != nil || kind != "personal" || hashLen != 32 || !hashMatches {
			t.Errorf("stored %s %d %v, %v", kind, hashLen, hashMatches, err)
		}
		id, err := e.tokens.Authenticate(ctx, created.Value, addr)
		if err != nil || !slices.Equal(id.Permissions, []auth.Permission{"alert-groups:acknowledge",
			"alert-groups:read"}) || id.Session.User.ID != alice.ID {
			t.Fatalf("identity = %+v, %v", id, err)
		}
		list, err := e.tokens.ListPersonal(ctx, alice.ID)
		if err != nil || len(list) != 1 || list[0].LastUsedAt == nil || !list[0].LastUsedAt.Equal(t0) ||
			list[0].LastUsedAddress != addr || len(list[0].Permissions) != 2 {
			t.Errorf("list = %+v, %v", list, err)
		}
		// A lower Role shrinks the token at the next request.
		if alice, err = e.admin.Update(ctx, e.usersBy(), alice.PublicID, nil, users.Changes{Name: "alice",
			Role: auth.RoleViewer}); err != nil {
			t.Fatal(err)
		}
		if id, err = e.tokens.Authenticate(ctx, created.Value, addr); err != nil ||
			!slices.Equal(id.Permissions, []auth.Permission{"alert-groups:read"}) {
			t.Errorf("after the Role change = %+v, %v", id, err)
		}
		// Disabled: refused until enabled.
		if _, err := e.admin.Disable(ctx, e.usersBy(), alice.PublicID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.tokens.Authenticate(ctx, created.Value, addr); !errors.Is(err, tokens.ErrInvalidToken) {
			t.Errorf("a disabled owner: %v", err)
		}
		if _, err := e.admin.Enable(ctx, e.usersBy(), alice.PublicID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.tokens.Authenticate(ctx, created.Value, addr); err != nil {
			t.Errorf("an enabled owner: %v", err)
		}
		// Revoke.
		other := e.personal(t, alice, "other", "alert-groups:read")
		if err := e.tokens.RevokePersonal(ctx, e.byOps, tokens.Owner{ID: e.ops.ID}, other.Token.PublicID); !errors.Is(err,
			tokens.ErrNotFound) {
			t.Errorf("revoking another user's token: %v", err)
		}
		if err := e.tokens.RevokePersonal(ctx, e.byOps, tokens.Owner{ID: alice.ID, PublicID: alice.PublicID},
			other.Token.PublicID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.tokens.Authenticate(ctx, other.Value, addr); !errors.Is(err, tokens.ErrInvalidToken) {
			t.Errorf("a revoked token: %v", err)
		}
		// Deleted: the tokens are revoked with owner_deleted.
		if err := e.admin.Delete(ctx, e.usersBy(), alice.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.tokens.Authenticate(ctx, created.Value, addr); !errors.Is(err, tokens.ErrInvalidToken) {
			t.Errorf("a deleted owner: %v", err)
		}
		var reasons []string
		rows, _ := e.d.Pool.Query(ctx, `SELECT revoked_reason FROM api_tokens WHERE user_id = $1 ORDER BY id`, alice.ID)
		for rows.Next() {
			var r string
			_ = rows.Scan(&r)
			reasons = append(reasons, r)
		}
		if !slices.Equal(reasons, []string{"owner_deleted", "revoked"}) {
			t.Errorf("revoked reasons %v", reasons)
		}
		// An expired token.
		expires := t0.Add(time.Hour)
		bob := e.create(t, "bob", auth.RoleResponder)
		short, err := e.tokens.CreatePersonal(ctx, e.byOps, tokens.Owner{ID: bob.ID, PublicID: bob.PublicID},
			e.roles.Permissions(bob.Role), tokens.NewPersonal{Name: "short",
				Permissions: []auth.Permission{"alert-groups:read"}, ExpiresAt: &expires})
		if err != nil {
			t.Fatal(err)
		}
		e.clock.Set(expires)
		if _, err := e.tokens.Authenticate(ctx, short.Value, addr); !errors.Is(err, tokens.ErrInvalidToken) {
			t.Errorf("an expired token: %v", err)
		}
	})
}

// TestIntegrationAttribution is C-04.FR-6 and C-04.AC-5: a user created with a Personal access token reads back from
// the Audit log as "{user} via token {name}" with the Transport api; issuing and revoking are entries too.
func TestIntegrationAttribution(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		created := e.personal(t, e.ops, "full", "users:read", "users:write")
		id, err := e.tokens.Authenticate(ctx, created.Value, addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.admin.Create(ctx, users.Requester{Actor: id.Actor(), Transport: id.Transport, Address: addr},
			users.NewUser{Name: "Finn", Login: "finn", Role: auth.RoleViewer}); err != nil {
			t.Fatal(err)
		}
		page, err := e.reader.List(ctx, audit.Filter{Action: audit.ActionUserCreated, Limit: 1})
		if err != nil || len(page.Entries) != 1 {
			t.Fatalf("entries %+v, %v", page, err)
		}
		got := page.Entries[0]
		if got.Actor.Name+" via token "+got.TokenName != e.ops.Name+" via token full" ||
			got.Transport != audit.TransportAPI || got.TokenPublicID != created.Token.PublicID {
			t.Errorf("entry %+v", got)
		}
		if err := e.tokens.RevokePersonal(ctx, e.byOps, tokens.Owner{ID: e.ops.ID, PublicID: e.ops.PublicID},
			created.Token.PublicID); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{audit.ActionAPITokenCreated, audit.ActionAPITokenRevoked} {
			page, err := e.reader.List(ctx, audit.Filter{Action: action, Limit: 5})
			if err != nil || len(page.Entries) != 1 || page.Entries[0].ResourceName != "full" {
				t.Errorf("%s: %+v, %v", action, page, err)
			}
		}
	})
}

// TestIntegrationServiceAccounts is S-016 step 3 and C-04.AC-8 against PostgreSQL: the lifecycle, name_taken among
// accounts that are not deleted, If-Match, a disabled account's tokens refused until enable, and delete revoking the
// tokens; an account acts in the Audit log as itself with its token.
func TestIntegrationServiceAccounts(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		sa, err := e.tokens.CreateServiceAccount(ctx, e.byOps, tokens.ServiceAccountInput{Name: "terraform",
			Role: auth.RoleAdmin})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.tokens.CreateServiceAccount(ctx, e.byOps, tokens.ServiceAccountInput{Name: "Terraform",
			Role: auth.RoleViewer}); !errors.Is(err, tokens.ErrNameTaken) {
			t.Errorf("a taken name: %v", err)
		}
		stale := sa.Version + 1
		if _, err := e.tokens.UpdateServiceAccount(ctx, e.byOps, sa.PublicID, &stale, tokens.ServiceAccountInput{
			Name: "tf", Role: auth.RoleAdmin}); !errors.Is(err, tokens.ErrVersionMismatch) {
			t.Errorf("a stale version: %v", err)
		}
		if sa, err = e.tokens.UpdateServiceAccount(ctx, e.byOps, sa.PublicID, &sa.Version,
			tokens.ServiceAccountInput{Name: "terraform", Role: auth.RoleResponder}); err != nil || sa.Version != 2 {
			t.Fatalf("update = %+v, %v", sa, err)
		}
		first, err := e.tokens.CreateServiceAccountToken(ctx, e.byOps, sa.PublicID, tokens.NewToken{Name: "ci"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := e.tokens.CreateServiceAccountToken(ctx, e.byOps, sa.PublicID, tokens.NewToken{Name: "ci-2"})
		if err != nil {
			t.Fatal(err)
		}
		id, err := e.tokens.Authenticate(ctx, first.Value, addr)
		if err != nil || !id.IsServiceAccount() || !slices.Equal(id.Permissions, e.roles.Permissions(auth.RoleResponder)) {
			t.Fatalf("identity = %+v, %v", id, err)
		}
		if got, err := e.tokens.GetServiceAccount(ctx, sa.PublicID); err != nil || got.TokenCount != 2 {
			t.Errorf("token_count = %+v, %v", got, err)
		}
		if err := e.tokens.RevokeServiceAccountToken(ctx, e.byOps, sa.PublicID, first.Token.PublicID); err != nil {
			t.Fatal(err)
		}
		list, err := e.tokens.ListServiceAccountTokens(ctx, sa.PublicID)
		if err != nil || len(list) != 1 || list[0].PublicID != second.Token.PublicID {
			t.Errorf("tokens = %+v, %v", list, err)
		}
		if _, err := e.tokens.DisableServiceAccount(ctx, e.byOps, sa.PublicID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.tokens.Authenticate(ctx, second.Value, addr); !errors.Is(err, tokens.ErrInvalidToken) {
			t.Errorf("a disabled account: %v", err)
		}
		if _, err := e.tokens.EnableServiceAccount(ctx, e.byOps, sa.PublicID); err != nil {
			t.Fatal(err)
		}
		id, err = e.tokens.Authenticate(ctx, second.Value, addr)
		if err != nil {
			t.Fatalf("an enabled account: %v", err)
		}
		// The account acts as itself with its token.
		if _, _, err := e.admin.Create(ctx, users.Requester{Actor: id.Actor(), Transport: id.Transport},
			users.NewUser{Name: "Gus", Login: "gus", Role: auth.RoleViewer}); err != nil {
			t.Fatal(err)
		}
		page, _ := e.reader.List(ctx, audit.Filter{Action: audit.ActionUserCreated, Limit: 1})
		if len(page.Entries) != 1 || page.Entries[0].Actor.Kind != audit.ActorServiceAccount ||
			page.Entries[0].Actor.Name != "terraform" || page.Entries[0].TokenName != "ci-2" {
			t.Errorf("entry %+v", page.Entries)
		}
		other, _ := e.tokens.CreateServiceAccount(ctx, e.byOps, tokens.ServiceAccountInput{Name: "grafana",
			Role: auth.RoleViewer})
		p1, err := e.tokens.ListServiceAccounts(ctx, tokens.ListFilter{Limit: 1})
		if err != nil || len(p1.ServiceAccounts) != 1 || p1.Next == nil {
			t.Fatalf("page 1 = %+v, %v", p1, err)
		}
		p2, err := e.tokens.ListServiceAccounts(ctx, tokens.ListFilter{Limit: 1, After: p1.Next})
		if err != nil || len(p2.ServiceAccounts) != 1 || p2.ServiceAccounts[0].PublicID != other.PublicID ||
			p2.Next != nil {
			t.Errorf("page 2 = %+v, %v", p2, err)
		}
		if err := e.tokens.DeleteServiceAccount(ctx, e.byOps, sa.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.tokens.Authenticate(ctx, second.Value, addr); !errors.Is(err, tokens.ErrInvalidToken) {
			t.Errorf("a deleted account: %v", err)
		}
		var revoked int
		_ = e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE revoked_reason = 'owner_deleted'`).
			Scan(&revoked)
		if revoked != 1 {
			t.Errorf("%d tokens revoked with owner_deleted", revoked)
		}
		if _, err := e.tokens.GetServiceAccount(ctx, sa.PublicID); !errors.Is(err, tokens.ErrNotFound) {
			t.Errorf("a deleted account reads: %v", err)
		}
		if _, err := e.tokens.CreateServiceAccount(ctx, e.byOps, tokens.ServiceAccountInput{Name: "terraform",
			Role: auth.RoleViewer}); err != nil {
			t.Errorf("the name of a deleted account: %v", err)
		}
	})
}

// TestIntegrationOIDCRules is S-016 step 5 and C-04.FR-8 against PostgreSQL with a manual clock: without an offline
// token the grace of the Organization counts from the last OIDC sign-in; a refusal at a re-check refuses the token
// even with an offline token; the next OIDC sign-in makes the same token work, and a Service account token of the
// same age is not affected.
func TestIntegrationOIDCRules(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		olga := e.create(t, "olga", auth.RoleResponder)
		e.exec(t, `UPDATE users SET oidc_issuer = 'https://idp.example.org', oidc_subject = 'u-olga',
			oidc_last_contact_at = $2 WHERE id = $1`, olga.ID, t0)
		created := e.personal(t, olga, "olga", "alert-groups:read")
		sa, _ := e.tokens.CreateServiceAccount(ctx, e.byOps, tokens.ServiceAccountInput{Name: "old",
			Role: auth.RoleViewer})
		sat, _ := e.tokens.CreateServiceAccountToken(ctx, e.byOps, sa.PublicID, tokens.NewToken{Name: "old"})

		e.clock.Set(t0.Add(7*24*time.Hour + time.Second))
		if _, err := e.tokens.Authenticate(ctx, created.Value, addr); !errors.Is(err, tokens.ErrOIDCRecheckRequired) {
			t.Errorf("past the grace: %v", err)
		}
		if _, err := e.tokens.Authenticate(ctx, sat.Value, addr); err != nil {
			t.Errorf("a Service account token of the same age: %v", err)
		}
		e.exec(t, `UPDATE organizations SET oidc_token_grace_seconds = 30 * 86400`)
		if _, err := e.tokens.Authenticate(ctx, created.Value, addr); err != nil {
			t.Errorf("within a longer grace: %v", err)
		}
		e.exec(t, `UPDATE organizations SET oidc_token_grace_seconds = 7 * 86400`)
		e.exec(t, `UPDATE users SET oidc_last_contact_at = $2 WHERE id = $1`, olga.ID, e.clock.Now())
		if _, err := e.tokens.Authenticate(ctx, created.Value, addr); err != nil {
			t.Errorf("after the next OIDC sign-in: %v", err)
		}
		// With an offline token there is no grace; a refusal at a re-check refuses the token.
		e.exec(t, `UPDATE users SET oidc_offline_token_ciphertext = '\x00', oidc_offline_token_key_id = 'k1'
			WHERE id = $1`, olga.ID)
		e.clock.Advance(30 * 24 * time.Hour)
		if _, err := e.tokens.Authenticate(ctx, created.Value, addr); err != nil {
			t.Errorf("with an offline token past the grace: %v", err)
		}
		// A refusal refuses the token on its own: the offline token is still held and the last contact is recent.
		e.exec(t, `UPDATE users SET oidc_refused_at = $2, oidc_last_contact_at = $2 WHERE id = $1`, olga.ID,
			e.clock.Now())
		if _, err := e.tokens.Authenticate(ctx, created.Value, addr); !errors.Is(err, tokens.ErrOIDCRecheckRequired) {
			t.Errorf("after a refusal: %v", err)
		}
		e.exec(t, `UPDATE users SET oidc_refused_at = NULL, oidc_last_contact_at = $2 WHERE id = $1`, olga.ID,
			e.clock.Now())
		if _, err := e.tokens.Authenticate(ctx, created.Value, addr); err != nil {
			t.Errorf("after the next OIDC sign-in: %v", err)
		}
		var revoked bool
		_ = e.d.Pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM api_tokens WHERE public_id = $1`,
			created.Token.PublicID).Scan(&revoked)
		if revoked {
			t.Error("the token was revoked")
		}
	})
}
