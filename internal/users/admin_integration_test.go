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
	"regexp"
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
	"github.com/muster-io/muster/internal/delivery"
	"github.com/muster-io/muster/internal/delivery/deliverytest"
	"github.com/muster-io/muster/internal/gateways"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/schedules"
	"github.com/muster-io/muster/internal/teams"
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

// TestIntegrationTeamsOfDeletedUsers is C-03.FR-13 revised for L2, C-25.FR-3 and C-25.AC-12: a disabled user stays in
// their Teams, and deleting a user takes them out of every Team in the transaction of the deletion, each recorded as
// team.user_removed with the reason user_deleted.
func TestIntegrationTeamsOfDeletedUsers(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		svc := teams.New(teams.Config{OrgID: e.orgID, Store: teams.NewStore(e.d.Pool),
			Audit: audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), e.clock), Business: e.clock})
		e.admin.SetTeamRemover(svc)
		bob := e.create(t, "Bob", "bob", auth.RoleResponder, "bob-password-1")
		byOps := teams.Requester{Actor: e.byOps.Actor, Transport: audit.TransportUI}
		var ids []string
		for _, slug := range []string{"payments", "search"} {
			team, err := svc.Create(ctx, byOps, teams.Input{Name: slug, Slug: slug})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := svc.AddUser(ctx, byOps, team.PublicID, bob.PublicID); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, team.PublicID)
		}
		count := func() int64 {
			t.Helper()
			var n int64
			for _, id := range ids {
				team, err := svc.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				n += team.UserCount
			}
			return n
		}
		if _, err := e.admin.Disable(ctx, e.byOps, bob.PublicID); err != nil {
			t.Fatal(err)
		}
		if got := count(); got != 2 {
			t.Errorf("a disabled user is in %d Teams, want 2", got)
		}
		if err := e.admin.Delete(ctx, e.byOps, bob.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if got := count(); got != 0 {
			t.Errorf("a deleted user is in %d Teams", got)
		}
		var removed int
		if err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'team.user_removed'
			AND details ->> 'reason' = 'user_deleted' AND actor_user_id = $1 AND details ->> 'user' = $2
			AND diff -> 0 ->> 'pointer' = '/users/' || $2`, e.ops.ID, bob.PublicID).Scan(&removed); err != nil ||
			removed != 2 {
			t.Errorf("%d team.user_removed entries, %v", removed, err)
		}
	})
}

// failingFeeds is a FeedRemover that fails, so that the delete rolls back.
type failingFeeds struct{}

func (failingFeeds) RemoveUserFeed(context.Context, usersdb.DBTX, int64, string, audit.Actor, audit.Transport,
	netip.Addr) (func(context.Context), error) {
	return nil, errors.New("the feeds are unavailable")
}

// TestIntegrationIcalFeedsOfDeletedUsers is C-27.FR-10 and C-27.AC-7: disabling a user keeps their iCal feed, which
// the lookup refuses until they are enabled again; deleting them deletes it in the transaction of the delete,
// recorded as ical_feed.revoked with the reason user_deleted, and a failure there rolls the delete back.
func TestIntegrationIcalFeedsOfDeletedUsers(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		w := audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), e.clock)
		store := schedules.NewStore(e.d.Pool)
		feeds := schedules.NewFeeds(schedules.FeedsConfig{OrgID: e.orgID, Store: schedules.NewFeedStore(e.d.Pool),
			Audit: w, Business: e.clock, PublicURL: "https://muster.example.org/",
			Schedules: schedules.New(schedules.Config{OrgID: e.orgID, Store: store, Audit: w, Business: e.clock})})
		bob := e.create(t, "Bob", "bob", auth.RoleResponder, "bob-password-1")
		owner := schedules.FeedUser{ID: bob.ID, PublicID: bob.PublicID}
		issued, err := feeds.Issue(ctx, schedules.Requester{Actor: audit.User(bob.ID, bob.PublicID),
			Transport: audit.TransportUI}, owner)
		if err != nil {
			t.Fatal(err)
		}
		value := strings.TrimSuffix(strings.TrimPrefix(issued.URL, "https://muster.example.org/ical/"), ".ics")
		lookup := func() (schedules.FeedUser, error) {
			t.Helper()
			return feeds.Lookup(ctx, value)
		}
		if u, err := lookup(); err != nil || u != owner {
			t.Fatalf("lookup = %+v, %v", u, err)
		}
		if _, err := e.admin.Disable(ctx, e.byOps, bob.PublicID); err != nil {
			t.Fatal(err)
		}
		if u, err := lookup(); !errors.Is(err, schedules.ErrFeedNotFound) || u.PublicID != bob.PublicID {
			t.Errorf("the feed of a disabled user = %+v, %v", u, err)
		}
		if _, err := e.admin.Enable(ctx, e.byOps, bob.PublicID); err != nil {
			t.Fatal(err)
		}
		if _, err := lookup(); err != nil {
			t.Errorf("the feed of a user enabled again: %v", err)
		}
		feedRows := func() int {
			t.Helper()
			var n int
			if err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM ical_feeds WHERE user_id = $1`, bob.ID).
				Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}

		e.admin.SetFeedRemover(failingFeeds{})
		if err := e.admin.Delete(ctx, e.byOps, bob.PublicID, nil); err == nil {
			t.Fatal("a delete whose feed could not be deleted succeeded")
		}
		if u, err := e.admin.Get(ctx, bob.PublicID); err != nil || u.Status != users.StatusActive || feedRows() != 1 {
			t.Errorf("after a failed delete: %+v, %v, %d feeds", u, err, feedRows())
		}

		e.admin.SetFeedRemover(feeds)
		if err := e.admin.Delete(ctx, e.byOps, bob.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if n := feedRows(); n != 0 {
			t.Errorf("a deleted user has %d feeds", n)
		}
		if _, err := lookup(); !errors.Is(err, schedules.ErrFeedNotFound) {
			t.Errorf("the feed of a deleted user: %v", err)
		}
		if _, err := feeds.Issue(ctx, schedules.Requester{Actor: audit.User(bob.ID, bob.PublicID),
			Transport: audit.TransportUI}, owner); !errors.Is(err, schedules.ErrFeedUserInactive) || feedRows() != 0 {
			t.Errorf("issuing for a deleted user: %v, %d feeds", err, feedRows())
		}
		var revoked int
		if err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'ical_feed.revoked'
			AND details ->> 'reason' = 'user_deleted' AND actor_user_id = $1 AND resource_type = 'user'
			AND resource_public_id = $2`, e.ops.ID, bob.PublicID).Scan(&revoked); err != nil || revoked != 1 {
			t.Errorf("%d ical_feed.revoked entries, %v", revoked, err)
		}
		var leaked int
		if err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE details::text LIKE '%mstr_cal_%'
			OR diff::text LIKE '%mstr_cal_%'`).Scan(&leaked); err != nil || leaked != 0 {
			t.Errorf("%d Audit log entries carry the token, %v", leaked, err)
		}
	})
}

// codeGateway is a Notification gateway that keeps the text of the last notification it was sent.
type codeGateway struct {
	mu   sync.Mutex
	text string
}

func (g *codeGateway) Notify(_ context.Context, _ delivery.Call, n delivery.Notification) delivery.Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.text = n.Text
	return delivery.Outcome{Kind: delivery.OutcomeOK, MessageID: "n1"}
}

// TestIntegrationGatewayContactsOfDeletedUsers is C-03.FR-13 revised for L2 and C-30.AC-2: deleting a user erases their
// contacts at Notification gateways and their codes in the transaction of the delete, each recorded as
// gateway_contact.deleted with the reason user_deleted; the Audit log keeps the entries about the contact, which show
// /contact as the contact while the user exists and as [erased] once they are deleted; disabling keeps the contacts.
func TestIntegrationGatewayContactsOfDeletedUsers(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		e := setup(t, s)
		const contact = "+10000000002"
		if _, err := e.d.Pool.Exec(ctx, `INSERT INTO connections (org_id, public_id, type, name, bridge_base_url,
			bridge_event_delivery, bot_token_ciphertext,
			bot_token_key_id, bot_token_updated_at, limiter_limit, limiter_per_seconds, bridge_info, created_at,
			updated_at) VALUES ($1, 'CN0000000000G1', 'bridge', 'Gateway', 'http://gw', 'poll', '\x00', 'k1', $2, 1, 1,
			'{"roles":["gateway"]}', $2, $2)`, e.orgID, t0); err != nil {
			t.Fatal(err)
		}
		gw := &codeGateway{}
		w := audit.NewWriter(logging.New(&bytes.Buffer{}, logging.LevelInfo), e.clock)
		contacts := gateways.New(gateways.Config{OrgID: e.orgID, Store: gateways.NewStore(e.d.Pool), Audit: w,
			Business: e.clock, Log: logging.New(&bytes.Buffer{}, logging.LevelInfo), Gateway: gw,
			Path: deliverytest.Unlimited(e.orgID, clock.Clocks{Business: e.clock, Real: clock.Real{}})})
		e.admin.SetContactRemover(contacts)
		bob := e.create(t, "Bob", "bob", auth.RoleResponder, "bob-password-1")
		sess, err := e.signIn(t, "bob", "bob-password-1")
		if err != nil {
			t.Fatal(err)
		}
		by := gateways.Requester{UserID: bob.ID, UserPublicID: bob.PublicID, SessionID: sess.ID,
			Actor: audit.User(bob.ID, bob.PublicID), Transport: audit.TransportUI}
		c, err := contacts.Create(ctx, by, "CN0000000000G1", contact)
		if err != nil {
			t.Fatal(err)
		}
		code := regexp.MustCompile(`[0-9]{6}`).FindString(gw.text)
		if _, err := contacts.Confirm(ctx, by, c.PublicID, code); err != nil {
			t.Fatal(err)
		}
		rows := func() (n int) {
			t.Helper()
			if err := e.d.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM gateway_contacts WHERE user_id = $1) +
				(SELECT count(*) FROM gateway_contact_codes WHERE user_id = $1)`, bob.ID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		shown := func() any {
			t.Helper()
			page, err := e.reader.List(ctx, audit.Filter{Action: gateways.ActionConfirmed, Limit: 10})
			if err != nil || len(page.Entries) != 1 {
				t.Fatalf("the confirmation: %+v, %v", page, err)
			}
			for _, ch := range page.Entries[0].Diff {
				if ch.Pointer == audit.ContactPointer {
					return ch.After
				}
			}
			return nil
		}
		if got := shown(); got != contact || rows() != 2 {
			t.Errorf("before the delete: /contact %v, %d rows", got, rows())
		}
		if _, err := e.admin.Disable(ctx, e.byOps, bob.PublicID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.admin.Enable(ctx, e.byOps, bob.PublicID); err != nil {
			t.Fatal(err)
		}
		if rows() != 2 {
			t.Errorf("disabling removed the contacts: %d rows", rows())
		}
		if err := e.admin.Delete(ctx, e.byOps, bob.PublicID, nil); err != nil {
			t.Fatal(err)
		}
		if n := rows(); n != 0 {
			t.Errorf("a deleted user has %d contacts and codes", n)
		}
		if got := shown(); got != audit.Erased {
			t.Errorf("after the delete: /contact %v", got)
		}
		page, err := e.reader.List(ctx, audit.Filter{ResourceType: gateways.ResourceContact, Limit: 10})
		if err != nil || len(page.Entries) != 3 {
			t.Fatalf("the entries about the contact: %+v, %v", page, err)
		}
		for _, entry := range page.Entries {
			for _, ch := range entry.Diff {
				if ch.Pointer == audit.ContactPointer && (ch.After != nil && ch.After != audit.Erased ||
					ch.Before != nil && ch.Before != audit.Erased) {
					t.Errorf("%s shows the contact: %+v", entry.Action, ch)
				}
			}
		}
		if page.Entries[0].Action != gateways.ActionDeleted || page.Entries[0].Details["reason"] != "user_deleted" ||
			page.Entries[0].Actor.PublicID != e.ops.PublicID {
			t.Errorf("the deletion entry: %+v", page.Entries[0])
		}
		var leaked int
		if err := e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE details::text LIKE '%' || $1 || '%'
			OR details::text LIKE '%' || $2 || '%' OR diff::text LIKE '%"' || $2 || '"%'`, contact, code).
			Scan(&leaked); err != nil || leaked != 0 {
			t.Errorf("%d entries carry the contact in their details or the code, %v", leaked, err)
		}
	})
}
