// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package users_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
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
	"github.com/muster-io/muster/internal/users"
	usersdb "github.com/muster-io/muster/internal/users/dbgen"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

const bootstrapPassword = "ops-bootstrap-pass"

// env is a migrated database with the Organization, the Audit log partition of October 2026 and the bootstrap Admin
// ops@example.org, with the administration, sign-in and the Audit log reader on one manual business clock.
type env struct {
	d       *db.DB
	orgID   int64
	clock   *clock.Manual
	admin   *users.Admin
	auth    *auth.Service
	reader  *audit.Reader
	ops     users.User
	byOps   users.Requester
	address netip.Addr
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
		users.Bootstrap{Email: "ops@example.org", Password: bootstrapPassword}, t0); err != nil {
		t.Fatal(err)
	}
	roles, err := auth.LoadRoles(ctx, auth.NewStore(d.Pool))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("http://localhost:8080")
	e := &env{
		d: d, orgID: org.ID, clock: c, address: netip.MustParseAddr("192.0.2.10"),
		admin:  users.NewAdmin(org.ID, users.NewAdminStore(d.Pool), w, c, base),
		auth:   auth.NewService(org.ID, auth.NewStore(d.Pool), nil, w, c, roles),
		reader: audit.NewReader(org.ID, audit.NewListQueries(d.Pool)),
	}
	page, err := e.admin.List(ctx, users.ListFilter{Q: "ops@example.org", Limit: 1})
	if err != nil || len(page.Users) != 1 {
		t.Fatalf("the bootstrap Admin: %+v, %v", page, err)
	}
	e.ops = page.Users[0]
	e.byOps = users.Requester{Actor: audit.User(e.ops.ID, e.ops.PublicID), Transport: audit.TransportUI,
		Address: e.address}
	return e
}

// signIn opens a session of login; the cookie authenticates later requests.
func (e *env) signIn(t *testing.T, login, password string) (auth.Session, error) {
	t.Helper()
	return e.auth.SignIn(t.Context(), auth.SignInRequest{Login: login, Password: password, Address: e.address})
}

// create makes a user with a password set through its setup link.
func (e *env) create(t *testing.T, name, login, role, password string) users.User {
	t.Helper()
	u, link, err := e.admin.Create(t.Context(), e.byOps, users.NewUser{Name: name, Login: login, Role: role})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.admin.CompleteSetup(t.Context(), token(t, link), password, e.address); err != nil {
		t.Fatal(err)
	}
	u, err = e.admin.Get(t.Context(), u.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func ptr(s string) *string { return &s }

func usersQueries(e *env) users.PruneQueries { return usersdb.New(e.d.Pool) }

func token(t *testing.T, link users.SetupLink) string {
	t.Helper()
	_, tok, ok := strings.Cut(link.URL, "#token=")
	if !ok || !strings.HasPrefix(link.URL, "http://localhost:8080/password-setup#token=") {
		t.Fatalf("link %s", link.URL)
	}
	return tok
}

// live reports whether the session of sess still authenticates.
func (e *env) live(t *testing.T, sess auth.Session) bool {
	t.Helper()
	_, err := e.auth.Authenticate(t.Context(), sess.Cookie())
	if err != nil && !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal(err)
	}
	return err == nil
}

// TestIntegrationUsers is S-011 step 2: every operation against PostgreSQL — create with the case-insensitive login,
// read, list, update, disable and enable, delete with pseudonymization — and the sessions ending on disable, Role
// change and delete.
func TestIntegrationUsers(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		alice := e.create(t, "Alice Smith", "Alice.Smith", auth.RoleResponder, "alice-password-1")
		sess, err := e.signIn(t, "alice.smith", "alice-password-1")
		if err != nil {
			t.Fatal(err)
		}
		if u, err := e.admin.Get(ctx, alice.PublicID); err != nil || u.Login != "Alice.Smith" || !u.HasPassword {
			t.Errorf("Alice = %+v, %v", u, err)
		}
		if _, _, err := e.admin.Create(ctx, e.byOps, users.NewUser{Name: "Other", Login: "ALICE.SMITH",
			Role: auth.RoleViewer}); !errors.Is(err, users.ErrNameTaken) {
			t.Errorf("ALICE.SMITH: %v", err)
		}

		// A change of name and email keeps the sessions; a stale version is refused.
		stale := alice.Version
		alice, err = e.admin.Update(ctx, e.byOps, alice.PublicID, &alice.Version, users.Changes{Name: "Alice S.",
			Role: auth.RoleResponder, Email: ptr("alice@example.org"), EmailSet: true})
		if err != nil || alice.Email != "alice@example.org" || !e.live(t, sess) {
			t.Fatalf("update = %+v, %v", alice, err)
		}
		if _, err := e.admin.Update(ctx, e.byOps, alice.PublicID, &stale, users.Changes{Name: "x",
			Role: auth.RoleResponder}); !errors.Is(err, users.ErrVersionMismatch) {
			t.Errorf("a stale version: %v", err)
		}
		// A Role change ends the sessions.
		if alice, err = e.admin.Update(ctx, e.byOps, alice.PublicID, nil, users.Changes{Name: "Alice S.",
			Role: auth.RoleViewer}); err != nil || alice.Role != auth.RoleViewer || alice.Email != "alice@example.org" {
			t.Fatalf("role change = %+v, %v", alice, err)
		}
		if e.live(t, sess) {
			t.Error("the session survived the Role change")
		}
		// Disable ends the sessions and refuses sign-in until enable.
		sess, _ = e.signIn(t, "alice.smith", "alice-password-1")
		if alice, err = e.admin.Disable(ctx, e.byOps, alice.PublicID); err != nil || alice.Status != "disabled" {
			t.Fatalf("disable = %+v, %v", alice, err)
		}
		if e.live(t, sess) {
			t.Error("the session survived disabling")
		}
		if _, err := e.signIn(t, "alice.smith", "alice-password-1"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("a disabled user signs in: %v", err)
		}
		if alice, err = e.admin.Enable(ctx, e.byOps, alice.PublicID); err != nil || alice.Status != "active" {
			t.Fatalf("enable = %+v, %v", alice, err)
		}
		if sess, err = e.signIn(t, "alice.smith", "alice-password-1"); err != nil {
			t.Fatalf("sign-in after enable: %v", err)
		}
		// Delete pseudonymizes, ends the sessions and frees the login.
		if err := e.admin.Delete(ctx, e.byOps, alice.PublicID, &alice.Version); err != nil {
			t.Fatal(err)
		}
		if e.live(t, sess) {
			t.Error("the session survived deletion")
		}
		gone, err := e.admin.Get(ctx, alice.PublicID)
		pseudonym := "deleted-user-" + alice.PublicID
		if err != nil || gone.Status != "deleted" || gone.Name != pseudonym || gone.Login != pseudonym ||
			gone.Email != "" || gone.HasPassword {
			t.Errorf("deleted = %+v, %v", gone, err)
		}
		var reasons []string
		rows, err := e.d.Pool.Query(ctx, `SELECT end_reason FROM sessions WHERE user_id = $1 ORDER BY id`, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				t.Fatal(err)
			}
			reasons = append(reasons, r)
		}
		if strings.Join(reasons, ",") != "role_changed,user_disabled,user_deleted" {
			t.Errorf("end reasons %v", reasons)
		}
		if _, err := e.admin.Disable(ctx, e.byOps, alice.PublicID); !errors.Is(err, users.ErrNotFound) {
			t.Errorf("disabling a deleted user: %v", err)
		}
		e.create(t, "Alice again", "alice.smith", auth.RoleViewer, "alice-password-9")

		// The list filters and pages by name.
		page, err := e.admin.List(ctx, users.ListFilter{Status: "deleted", Limit: 10})
		if err != nil || len(page.Users) != 1 || page.Users[0].PublicID != alice.PublicID {
			t.Errorf("deleted users = %+v, %v", page, err)
		}
		// Pages of one; a user created before the cursor between the pages does not shift them.
		var names []string
		var after *users.Cursor
		for i := 0; ; i++ {
			page, err := e.admin.List(ctx, users.ListFilter{Limit: 1, After: after})
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range page.Users {
				names = append(names, u.Name)
			}
			if i == 0 {
				e.create(t, "Aaron", "aaron", auth.RoleViewer, "aaron-password-1")
			}
			if after = page.Next; after == nil {
				break
			}
		}
		if strings.Join(names, ",") != "Alice again,"+pseudonym+",ops" {
			t.Errorf("pages = %v", names)
		}
		for f, want := range map[users.ListFilter]int{
			{Q: "ALICE", Limit: 10}: 1, {Q: "example.org", Limit: 10}: 1, {Role: "admin", Limit: 10}: 1,
			{Source: "local", Limit: 10}: 3, {Source: "bootstrap", Limit: 10}: 1, {Status: "active", Limit: 10}: 3,
		} {
			if page, err := e.admin.List(ctx, f); err != nil || len(page.Users) != want {
				t.Errorf("%+v = %d users, %v", f, len(page.Users), err)
			}
		}
	})
}

// TestIntegrationLastAdmin is C-03.FR-31 and C-03.AC-25 against PostgreSQL: the last active Admin can neither be
// disabled, deleted nor lowered, also by themselves, and two Admins that disable each other at once leave one active.
func TestIntegrationLastAdmin(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		for name, f := range map[string]func() error{
			"disable": func() error { _, err := e.admin.Disable(ctx, e.byOps, e.ops.PublicID); return err },
			"delete":  func() error { return e.admin.Delete(ctx, e.byOps, e.ops.PublicID, nil) },
			"lower": func() error {
				_, err := e.admin.Update(ctx, e.byOps, e.ops.PublicID, nil, users.Changes{Name: "ops",
					Role: auth.RoleResponder})
				return err
			},
		} {
			if err := f(); !errors.Is(err, users.ErrLastAdmin) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if u, _ := e.admin.Get(ctx, e.ops.PublicID); u.Version != e.ops.Version || u.Status != "active" {
			t.Errorf("the last Admin changed: %+v", u)
		}
		second := e.create(t, "Second", "second", auth.RoleAdmin, "second-password-1")
		bySecond := users.Requester{Actor: audit.User(second.ID, second.PublicID), Transport: audit.TransportAPI}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, c := range []struct {
			by     users.Requester
			target string
		}{{e.byOps, second.PublicID}, {bySecond, e.ops.PublicID}} {
			wg.Go(func() { _, errs[i] = e.admin.Disable(ctx, c.by, c.target) })
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) || !errors.Is(errors.Join(errs...), users.ErrLastAdmin) {
			t.Errorf("concurrent disables: %v", errs)
		}
		page, err := e.admin.List(ctx, users.ListFilter{Role: "admin", Status: "active", Limit: 10})
		if err != nil || len(page.Users) != 1 {
			t.Errorf("active Admins after the race: %+v, %v", page, err)
		}
		active := page.Users[0].PublicID
		other := map[string]string{e.ops.PublicID: second.PublicID, second.PublicID: e.ops.PublicID}[active]
		if _, err := e.admin.Enable(ctx, e.byOps, other); err != nil {
			t.Fatal(err)
		}
		third := e.create(t, "Third", "third", auth.RoleAdmin, "third-password-1")
		if err := e.admin.Delete(ctx, e.byOps, third.PublicID, nil); err != nil {
			t.Errorf("with three active Admins, delete: %v", err)
		}
		if _, err := e.admin.Update(ctx, e.byOps, active, nil, users.Changes{Name: "lowered",
			Role: auth.RoleResponder}); err != nil {
			t.Errorf("with a second active Admin, lowering: %v", err)
		}
	})
}

// TestIntegrationSetupLinks is S-011 step 3 with a manual clock: link_expired past 24 h, link_used after use and
// after a newer link, unknown tokens; none sets a password. The pruning deletes links a week past their expiry.
func TestIntegrationSetupLinks(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		bob, first, err := e.admin.Create(ctx, e.byOps, users.NewUser{Name: "Bob", Login: "bob", Role: auth.RoleViewer})
		if err != nil {
			t.Fatal(err)
		}
		second, err := e.admin.CreateSetupLink(ctx, e.byOps, bob.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		check := func(tok string, want error) {
			t.Helper()
			if err := e.admin.CompleteSetup(ctx, tok, "bob-password-1", e.address); !errors.Is(err, want) {
				t.Errorf("token: %v, want %v", err, want)
			}
		}
		check(token(t, first), users.ErrLinkUsed)
		check("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", users.ErrLinkNotFound)
		e.clock.Advance(24 * time.Hour)
		check(token(t, second), users.ErrLinkExpired)
		if u, _ := e.admin.Get(ctx, bob.PublicID); u.HasPassword {
			t.Fatal("a refused link set the password")
		}
		third, err := e.admin.CreateSetupLink(ctx, e.byOps, bob.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		check(token(t, third), nil)
		check(token(t, third), users.ErrLinkUsed)
		if _, err := e.signIn(t, "BOB", "bob-password-1"); err != nil {
			t.Errorf("sign-in with the new password: %v", err)
		}
		var hashes int
		if err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM password_setups WHERE octet_length(token_hash) = 32`).
			Scan(&hashes); err != nil || hashes != 3 {
			t.Errorf("stored links %d, %v", hashes, err)
		}

		pruner := users.NewPruner(usersQueries(e))
		if n, err := pruner.PasswordSetups(ctx, e.orgID, t0.Add(24*time.Hour+7*24*time.Hour), 1000); err != nil || n != 0 {
			t.Errorf("pruned %d at a week past the first expiry, %v", n, err)
		}
		n, err := pruner.PasswordSetups(ctx, e.orgID, t0.Add(48*time.Hour+7*24*time.Hour), 1)
		if err != nil || n != 1 {
			t.Errorf("pruned %d with a batch of 1, %v", n, err)
		}
		n, err = pruner.PasswordSetups(ctx, e.orgID, t0.Add(60*24*time.Hour), 1000)
		if err != nil || n != 2 {
			t.Errorf("pruned %d, %v", n, err)
		}
		check(token(t, third), users.ErrLinkNotFound)
	})
}

// TestIntegrationResetPassword is C-03.FR-11 and C-02.FR-15: the reset sets the password, ends the sessions and
// writes user.password_reset by the CLI actor.
func TestIntegrationResetPassword(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		bob := e.create(t, "Bob", "bob", auth.RoleViewer, "bob-password-1")
		sess, err := e.signIn(t, "bob", "bob-password-1")
		if err != nil {
			t.Fatal(err)
		}
		id, err := e.admin.ResetPassword(ctx, "ops", "BOB", "bob-new-password")
		if err != nil || id != bob.PublicID {
			t.Fatalf("reset = %s, %v", id, err)
		}
		if e.live(t, sess) {
			t.Error("the session survived the reset")
		}
		if _, err := e.signIn(t, "bob", "bob-new-password"); err != nil {
			t.Errorf("sign-in with the new password: %v", err)
		}
		page, err := e.reader.List(ctx, audit.Filter{Action: audit.ActionPasswordReset, Limit: 10})
		if err != nil || len(page.Entries) != 1 {
			t.Fatalf("entries = %+v, %v", page, err)
		}
		if got := page.Entries[0]; got.Actor.Kind != audit.ActorCLI || got.Actor.Name != "ops" ||
			got.Transport != audit.TransportCLI || got.ResourceID != bob.PublicID || !got.Diff[0].SecretChanged {
			t.Errorf("entry = %+v", got)
		}
	})
}

// TestIntegrationAuditLog is S-011 step 4 and C-03.FR-14, FR-15, AC-4 and AC-11: newest first, every filter, cursor
// pages that stay stable across inserts, the current name of a deleted actor and the bootstrap entry.
func TestIntegrationAuditLog(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		e.clock.Advance(time.Minute)
		alice := e.create(t, "Alice", "alice", auth.RoleResponder, "alice-password-1")
		e.clock.Advance(time.Minute)
		if _, err := e.signIn(t, "alice", "alice-password-1"); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
		if _, err := e.signIn(t, "alice", "alice-password-1"); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(time.Minute)
		if err := e.admin.Delete(ctx, e.byOps, alice.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		all, err := e.reader.List(ctx, audit.Filter{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var actions []string
		for i, en := range all.Entries {
			actions = append(actions, en.Action)
			if i > 0 && en.At.After(all.Entries[i-1].At) {
				t.Errorf("entry %d is newer than the one before", i)
			}
		}
		want := "user.deleted,session.signed_in,session.signed_in,user.password_set,user.created,user.created"
		if strings.Join(actions, ",") != want || all.Next != nil {
			t.Errorf("actions = %v", actions)
		}
		boot := all.Entries[len(all.Entries)-1]
		if boot.Actor.Kind != audit.ActorBootstrap || boot.Actor.Name != "bootstrap" || boot.ResourceName != "ops" {
			t.Errorf("the bootstrap entry = %+v", boot)
		}
		created := all.Entries[len(all.Entries)-2]
		if created.Actor.Name != "ops" || created.Actor.PublicID != e.ops.PublicID || len(created.Diff) == 0 ||
			created.Transport != audit.TransportUI {
			t.Errorf("user.created = %+v", created)
		}
		about, err := e.reader.List(ctx, audit.Filter{ResourceID: alice.PublicID, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, en := range about.Entries {
			if en.ResourceName != "deleted-user-"+alice.PublicID {
				t.Errorf("%s about Alice names %q", en.Action, en.ResourceName)
			}
		}
		byAlice, err := e.reader.List(ctx, audit.Filter{Actor: strings.ToLower(alice.PublicID), Limit: 100})
		if err != nil || len(byAlice.Entries) != 2 {
			t.Fatalf("by Alice = %+v, %v", byAlice, err)
		}
		for _, en := range byAlice.Entries {
			if en.Action != audit.ActionSignedIn || en.Actor.Name != "deleted-user-"+alice.PublicID {
				t.Errorf("Alice's entry = %+v", en)
			}
		}
		for name, c := range map[string]struct {
			f    audit.Filter
			want int
		}{
			"from":          {audit.Filter{From: new(t0.Add(2 * time.Minute)), Limit: 100}, 3},
			"to":            {audit.Filter{To: new(t0.Add(2 * time.Minute)), Limit: 100}, 3},
			"from to":       {audit.Filter{From: new(t0.Add(time.Minute)), To: new(t0.Add(3 * time.Minute)), Limit: 100}, 3},
			"action":        {audit.Filter{Action: "session.signed_in", Limit: 100}, 2},
			"resource type": {audit.Filter{ResourceType: "user", Limit: 100}, 4},
			"resource id":   {audit.Filter{ResourceID: alice.PublicID, Limit: 100}, 3},
			"actor":         {audit.Filter{Actor: e.ops.PublicID, Limit: 100}, 2},
			"unknown actor": {audit.Filter{Actor: "SR0000000000AA", Limit: 100}, 0},
			"service acct":  {audit.Filter{Actor: "SA0000000000AA", Limit: 100}, 0},
			"bad actor":     {audit.Filter{Actor: "nonsense", Limit: 100}, 0},
			"combined":      {audit.Filter{Actor: e.ops.PublicID, Action: "user.deleted", Limit: 100}, 1},
		} {
			page, err := e.reader.List(ctx, c.f)
			if err != nil || len(page.Entries) != c.want {
				t.Errorf("%s: %d entries, %v", name, len(page.Entries), err)
			}
		}
		// Pages of two; an entry written between the pages does not shift them.
		first, err := e.reader.List(ctx, audit.Filter{Limit: 2})
		if err != nil || len(first.Entries) != 2 || first.Next == nil {
			t.Fatalf("first page = %+v, %v", first, err)
		}
		e.clock.Advance(time.Minute)
		if _, err := e.admin.CreateSetupLink(ctx, e.byOps, e.ops.PublicID); err != nil {
			t.Fatal(err)
		}
		var paged []string
		for _, en := range first.Entries {
			paged = append(paged, en.Action)
		}
		for after := first.Next; after != nil; {
			page, err := e.reader.List(ctx, audit.Filter{Limit: 2, After: after})
			if err != nil {
				t.Fatal(err)
			}
			for _, en := range page.Entries {
				paged = append(paged, en.Action)
			}
			after = page.Next
		}
		if strings.Join(paged, ",") != want {
			t.Errorf("paged = %v", paged)
		}

		// Deleting a user masks the values it erased in the earlier diffs when they are read; the rows stay.
		e.clock.Advance(time.Minute)
		erin, _, err := e.admin.Create(ctx, e.byOps, users.NewUser{Name: "Erin", Login: "erin",
			Email: ptr("erin@example.org"), Role: auth.RoleViewer})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.admin.Update(ctx, e.byOps, erin.PublicID, nil, users.Changes{Name: "Erin", Role: auth.RoleViewer,
			Email: ptr("erin@example.com"), EmailSet: true}); err != nil {
			t.Fatal(err)
		}
		if err := e.admin.Delete(ctx, e.byOps, erin.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		about, err = e.reader.List(ctx, audit.Filter{ResourceID: erin.PublicID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		diffs := map[string][]audit.Change{}
		for _, en := range about.Entries {
			diffs[en.Action] = en.Diff
		}
		find := func(action, pointer string) audit.Change {
			for _, c := range diffs[action] {
				if c.Pointer == pointer {
					return c
				}
			}
			t.Fatalf("%s has no %s in %+v", action, pointer, diffs[action])
			return audit.Change{}
		}
		for _, p := range []string{"/name", "/login", "/email"} {
			if c := find(audit.ActionUserCreated, p); c.After != audit.Erased || c.Before != nil {
				t.Errorf("user.created %s = %+v", p, c)
			}
		}
		if c := find(audit.ActionUserCreated, "/role"); c.After != auth.RoleViewer {
			t.Errorf("user.created /role = %+v", c)
		}
		if c := find(audit.ActionUserUpdated, "/email"); c.Before != audit.Erased || c.After != audit.Erased {
			t.Errorf("user.updated /email = %+v", c)
		}
		var raw string
		if err := e.d.Pool.QueryRow(ctx, `SELECT string_agg(diff::text, ' ' ORDER BY id) FROM audit_log
			WHERE resource_public_id = $1`, erin.PublicID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"erin@example.org", "erin@example.com", `"Erin"`, `"erin"`} {
			if !strings.Contains(raw, v) {
				t.Errorf("the stored rows lost %s: %s", v, raw)
			}
		}
	})
}

// TestIntegrationSetupLinkRaces: two requests with one token set the password once, and completing a link while an
// Admin deletes the user or issues a newer link never deadlocks: each pair ends in one of the outcomes of the contract.
func TestIntegrationSetupLinkRaces(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		for i := range 4 {
			u, link, err := e.admin.Create(ctx, e.byOps, users.NewUser{Name: "Racer", Login: fmt.Sprintf("racer-%d", i),
				Role: auth.RoleViewer})
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			var first, second error
			wg.Go(func() { first = e.admin.CompleteSetup(ctx, token(t, link), "racer-password-1", e.address) })
			switch i {
			case 0:
				wg.Go(func() { second = e.admin.CompleteSetup(ctx, token(t, link), "racer-password-2", e.address) })
			case 1, 2:
				wg.Go(func() { second = e.admin.Delete(ctx, e.byOps, u.PublicID, nil) })
			default:
				wg.Go(func() { _, second = e.admin.CreateSetupLink(ctx, e.byOps, u.PublicID) })
			}
			wg.Wait()
			switch {
			case i == 0 && (first == nil) == (second == nil):
				t.Errorf("one token twice: %v, %v", first, second)
			case i == 0 && !errors.Is(errors.Join(first, second), users.ErrLinkUsed):
				t.Errorf("one token twice: %v, %v", first, second)
			case i > 0 && second != nil:
				t.Errorf("race %d: the Admin's change failed: %v", i, second)
			case i > 0 && first != nil && !errors.Is(first, users.ErrLinkUsed):
				t.Errorf("race %d: the setup failed with %v", i, first)
			}
		}
	})
}

// TestIntegrationUserDirectory is C-10.FR-13 and C-10.AC-19 against PostgreSQL: the directory lists every user by
// name, a deleted one as deactivated under its pseudonym, pages by cursor and matches q on the name and the login,
// never the email.
func TestIntegrationUserDirectory(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		e := setup(t, s)
		ctx := t.Context()
		bob, _, err := e.admin.Create(ctx, e.byOps, users.NewUser{Name: "Bob", Login: "bob", Role: auth.RoleResponder,
			Email: ptr("bob@corp.test")})
		if err != nil {
			t.Fatal(err)
		}
		e.create(t, "Carol", "carol", auth.RoleViewer, "carol-password-1")
		if err := e.admin.Delete(ctx, e.byOps, bob.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		d := users.NewDirectory(e.orgID, users.NewAdminStore(e.d.Pool))
		var got []string
		var after *users.Cursor
		for {
			page, err := d.List(ctx, "", after, 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, x := range page.Entries {
				got = append(got, fmt.Sprintf("%s %s %v", x.Name, x.Login, x.Deactivated))
			}
			if after = page.Next; after == nil {
				break
			}
		}
		pseudonym := users.DeletedPrefix + bob.PublicID
		if want := []string{"Carol carol false", pseudonym + " " + pseudonym + " true", "ops " + e.ops.Login +
			" false"}; !slices.Equal(got, want) {
			t.Errorf("directory = %v, want %v", got, want)
		}
		for q, want := range map[string]int{"CAR": 1, "carol": 1, "corp.test": 0, "deleted-user": 1, "zzz": 0} {
			if page, err := d.List(ctx, q, nil, 10); err != nil || len(page.Entries) != want {
				t.Errorf("q %q = %d, %v", q, len(page.Entries), err)
			}
		}
	})
}
