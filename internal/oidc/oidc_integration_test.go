// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build integration

package oidc_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/db/dbtest"
	"github.com/muster-io/muster/internal/fakes/fakeoidc"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc"
	oidcdb "github.com/muster-io/muster/internal/oidc/dbgen"
	"github.com/muster-io/muster/internal/organization"
	"github.com/muster-io/muster/internal/users"
)

func TestMain(m *testing.M) {
	os.Exit(dbtest.Main(context.Background(), m))
}

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

type world struct {
	d        *db.DB
	orgID    int64
	svc      *oidc.Service
	idp      *fakeoidc.Fake
	business *clock.Manual
	admin    *users.Admin
	sessions *auth.Service
	keyring  *keyring.Keyring
}

// setup migrates a new database with the Organization, the Audit log partition of October 2026 and the bootstrap
// Admin ops@example.org, and runs OIDC against a fake IdP, which the outbound policy reaches on loopback through the
// demo step of development mode.
func setup(t *testing.T, s dbtest.Server) *world {
	return setupWith(t, s, nil)
}

// setupWith is setup with the store of the Service wrapped by wrap, when it is not nil.
func setupWith(t *testing.T, s dbtest.Server, wrap func(oidc.Store, *users.Admin) oidc.Store) *world {
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
	business := clock.NewManual(t0)
	w := audit.NewWriter(log, business)
	if err := users.EnsureBootstrapAdmin(ctx, users.NewStore(d.Pool), w, log, org.ID,
		users.Bootstrap{Email: "ops@example.org", Password: "ops-bootstrap-pass"}, t0); err != nil {
		t.Fatal(err)
	}
	k, err := keyring.New([][]byte{bytes.Repeat([]byte{'i'}, keyring.KeySize)}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.Establish(ctx, keyring.NewStore(d.Pool), t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(ctx, log, st); err != nil {
		t.Fatal(err)
	}
	roles, err := auth.LoadRoles(ctx, auth.NewStore(d.Pool))
	if err != nil {
		t.Fatal(err)
	}
	clocks := clock.Clocks{Business: business, Real: clock.Real{}}
	sessions := auth.NewService(org.ID, auth.NewStore(d.Pool), k, w, business, roles)
	idp := fakeoidc.New()
	if err := idp.Start(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idp.Close(context.WithoutCancel(ctx)) })
	public, _ := url.Parse("http://localhost:8080")
	admin := users.NewAdmin(org.ID, users.NewAdminStore(d.Pool), w, business, public)
	store := oidc.NewStore(d.Pool)
	if wrap != nil {
		store = wrap(store, admin)
	}
	svc := oidc.NewService(oidc.Config{OrgID: org.ID, Store: store, Keyring: k, Audit: w,
		Clocks: clocks, Log: log, Sessions: sessions, PublicURL: public, RecheckBudget: 500 * time.Millisecond,
		Network: oidc.Network{Policy: organization.NewOutboundPolicies(organization.NewStore(d.Pool), org.ID,
			clock.Real{}), Log: log, Real: clock.Real{}}})
	if err := svc.EnsureDemo(ctx, oidc.Demo{IssuerURL: idp.URL(), ClientID: "muster-dev", ClientSecret: "dev",
		DisplayName: "Dev IdP", AllowNetwork: "127.0.0.0/8", Mappings: []oidc.GroupMapping{
			{Group: "muster-admins", Role: auth.RoleAdmin}, {Group: "oncall", Role: auth.RoleResponder}}}); err != nil {
		t.Fatal(err)
	}
	return &world{d: d, orgID: org.ID, svc: svc, idp: idp, business: business, admin: admin, sessions: sessions,
		keyring: k}
}

var browser = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (w *world) signIn(t *testing.T, u fakeoidc.User, returnTo string) oidc.Outcome {
	t.Helper()
	w.idp.SetNextUser(u)
	start, err := w.svc.StartSignIn(t.Context(), returnTo)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, start.URL, nil)
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	return w.svc.CompleteSignIn(t.Context(), oidc.Callback{Code: back.Query().Get("code"),
		State: back.Query().Get("state"), CookieState: start.State})
}

func (w *world) scalar(t *testing.T, sql string, args ...any) any {
	t.Helper()
	var v any
	if err := w.d.Pool.QueryRow(t.Context(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (w *world) actions(t *testing.T, action string) []map[string]any {
	t.Helper()
	rows, err := w.d.Pool.Query(t.Context(), `SELECT details FROM audit_log WHERE org_id = $1 AND action = $2
		ORDER BY at, id`, w.orgID, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var m map[string]any
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// TestIntegrationSignIn runs the sign-in of C-03.AC-1, AC-16, FR-28 and AC-13 against PostgreSQL and the fake IdP.
func TestIntegrationSignIn(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		w := setup(t, s)
		out := w.signIn(t, fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"oncall"},
			AMR: []string{"pwd", "otp"}}, "/profile")
		if out.Session == nil || out.Redirect != "/profile" {
			t.Fatalf("first sign-in = %+v", out)
		}
		if role := w.scalar(t, `SELECT role || '/' || source FROM users WHERE login = 'olga'`); role != "responder/oidc" {
			t.Errorf("olga = %v", role)
		}
		if got := w.scalar(t, `SELECT method || '/' || idp_mfa::text || '/' ||
			(extract(epoch FROM expires_at - created_at) / 3600)::int FROM sessions WHERE public_id = $1`,
			out.Session.PublicID); got != "oidc/true/168" {
			t.Errorf("session = %v", got)
		}
		if got := w.scalar(t, `SELECT oidc_last_contact_at IS NOT NULL FROM users WHERE login = 'olga'`); got != true {
			t.Error("the contact with the IdP was not recorded")
		}
		if signed := w.actions(t, audit.ActionSignedIn); len(signed) != 1 || signed[0]["method"] != "oidc" {
			t.Errorf("session.signed_in = %v", signed)
		}

		if out = w.signIn(t, fakeoidc.User{Subject: "u-2", PreferredUsername: "nora", Groups: []string{"contractors"}},
			""); out.Redirect != "/sign-in?error=no_access" {
			t.Fatalf("no mapped group = %+v", out)
		}
		refused := w.actions(t, oidc.ActionSignInRefused)
		if len(refused) != 1 || refused[0]["reason"] != "no_access" || len(refused[0]["groups"].([]any)) != 1 {
			t.Errorf("refusal = %v", refused)
		}

		// The login of a local user is taken: nothing is created and Alice is unchanged.
		r := users.Requester{Actor: audit.System, Transport: audit.TransportUI}
		alice, _, err := w.admin.Create(t.Context(), r, users.NewUser{Name: "Alice", Login: "Alice", Role: auth.RoleViewer})
		if err != nil {
			t.Fatal(err)
		}
		if out = w.signIn(t, fakeoidc.User{Subject: "u-3", PreferredUsername: "alice", Groups: []string{"oncall"}},
			""); out.Redirect != "/sign-in?error=login_taken" {
			t.Fatalf("login taken = %+v", out)
		}
		after, _ := w.admin.Get(t.Context(), alice.PublicID)
		if after.Version != alice.Version || after.HasOIDCIdentity ||
			w.scalar(t, `SELECT count(*) FROM users WHERE oidc_subject = 'u-3'`) != int64(0) {
			t.Errorf("Alice changed or a user was created: %+v", after)
		}

		if n := w.scalar(t, `SELECT count(*) FROM oidc_auth_requests`); n != int64(0) {
			t.Errorf("%v requests left after their callbacks", n)
		}
		if _, err := w.svc.StartSignIn(t.Context(), "//evil.example"); err != nil {
			t.Fatal(err)
		}
		if rt := w.scalar(t, `SELECT coalesce(return_to, '') FROM oidc_auth_requests`); rt != "" {
			t.Errorf("return_to = %v", rt)
		}
		pruner := oidc.NewPruner(oidcdb.New(w.d.Pool))
		if n, err := pruner.AuthRequests(t.Context(), w.orgID, t0.Add(oidc.AuthRequestTTL+time.Hour), 10); err != nil ||
			n != 0 {
			t.Errorf("pruned a request within the hour: %d, %v", n, err)
		}
		if n, err := pruner.AuthRequests(t.Context(), w.orgID, t0.Add(oidc.AuthRequestTTL+time.Hour+time.Second), 10); err != nil ||
			n != 1 {
			t.Errorf("prune = %d, %v", n, err)
		}
	})
}

// TestIntegrationLastAdminKept is C-03.FR-32 and AC-26 at a sign-in, with the LatestKeptAdmin query.
func TestIntegrationLastAdminKept(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		w := setup(t, s)
		ada := fakeoidc.User{Subject: "u-9", PreferredUsername: "ada", Groups: []string{"muster-admins"}}
		w.signIn(t, ada, "")
		// ada becomes the only active Admin: the bootstrap Admin is disabled.
		if _, err := w.d.Pool.Exec(t.Context(), `UPDATE users SET status = 'disabled' WHERE login = 'ops@example.org'`); err != nil {
			t.Fatal(err)
		}
		ada.Groups = []string{"oncall"}
		w.business.Advance(time.Minute)
		first := w.signIn(t, ada, "")
		if first.Session == nil || w.scalar(t, `SELECT role FROM users WHERE login = 'ada'`) != "admin" {
			t.Fatalf("the last active Admin was lowered: %+v", first)
		}
		st, err := w.svc.Get(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(st.Warnings, func(x oidc.Warning) bool { return x.Kind == oidc.WarningLastAdminKept })
		if i < 0 || st.Warnings[i].Role != "responder" || st.Warnings[i].User.Login != "ada" {
			t.Fatalf("warnings = %+v", st.Warnings)
		}
		if _, err := w.d.Pool.Exec(t.Context(), `UPDATE users SET status = 'active' WHERE login = 'ops@example.org'`); err != nil {
			t.Fatal(err)
		}
		w.business.Advance(time.Minute)
		if out := w.signIn(t, ada, ""); out.Session == nil {
			t.Fatal(out)
		}
		if role := w.scalar(t, `SELECT role FROM users WHERE login = 'ada'`); role != "responder" {
			t.Errorf("with a second Admin ada is %v", role)
		}
		if ended := w.scalar(t, `SELECT count(*) FROM sessions s JOIN users u ON u.id = s.user_id
			WHERE u.login = 'ada' AND s.end_reason = 'role_changed'`); ended != int64(2) {
			t.Errorf("ended sessions = %v", ended)
		}
		st, _ = w.svc.Get(t.Context())
		if slices.ContainsFunc(st.Warnings, func(x oidc.Warning) bool { return x.Kind == oidc.WarningLastAdminKept }) {
			t.Error("the warning stayed")
		}
	})
}

// TestIntegrationSettings round-trips the settings and the check against PostgreSQL: the version, the encrypted secret
// and the groups claim found missing by a check.
func TestIntegrationSettings(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		w := setup(t, s)
		st, err := w.svc.Get(t.Context())
		if err != nil || st.Version != 1 || !st.ClientSecret.Set || st.ButtonName() != "Dev IdP" {
			t.Fatalf("demo = %+v, %v", st, err)
		}
		if allowed := w.scalar(t, `SELECT array_to_string(allowed, ',') FROM outbound_policies`); allowed != "127.0.0.0/8" {
			t.Errorf("allowed = %v", allowed)
		}
		scopes := []string{"profile"}
		one := int64(1)
		st, err = w.svc.Update(t.Context(), oidc.Requester{Actor: audit.System, Transport: audit.TransportUI}, &one,
			oidc.Input{Enabled: true, IssuerURL: w.idp.URL(), ClientID: "muster", ClientSecret: keyring.Replace("s3cr3t"),
				Scopes: &scopes, GroupsClaim: "groups", UnmatchedRole: oidc.UnmatchedNone, SyncRole: true})
		if err != nil || st.Version != 2 || !slices.Contains([]string{st.Warnings[0].Kind}, oidc.WarningNobodyCanSignIn) {
			t.Fatalf("update = %+v, %v", st, err)
		}
		if n := w.scalar(t, `SELECT count(*) FROM oidc_settings WHERE client_secret_ciphertext::text LIKE '%s3cr3t%'`); n != int64(0) {
			t.Error("the client secret is stored in plain text")
		}
		w.idp.Configure(fakeoidc.Config{OmitGroupsClaim: new(true)})
		res, err := w.svc.Check(t.Context())
		if err != nil || !res.OK || !slices.ContainsFunc(res.Warnings, func(x oidc.Warning) bool {
			return x.Kind == oidc.WarningGroupsClaimMissing
		}) {
			t.Fatalf("check = %+v, %v", res, err)
		}
		if v := w.scalar(t, `SELECT version FROM oidc_settings`); v != int64(2) {
			t.Errorf("a check moved the version to %v", v)
		}
	})
}

// racingStore creates a local user with the login of the identity right after the sign-in checked it, as a parallel
// request would, so that the unique login index refuses the account inside the sign-in's transaction.
type racingStore struct {
	oidc.Store
	admin *users.Admin
}

func (r racingStore) InTx(ctx context.Context, f func(oidc.Queries) error) error {
	return r.Store.InTx(ctx, func(q oidc.Queries) error { return f(racingQueries{Queries: q, admin: r.admin}) })
}

type racingQueries struct {
	oidc.Queries
	admin *users.Admin
}

func (q racingQueries) LoginTaken(ctx context.Context, arg oidcdb.LoginTakenParams) (bool, error) {
	_, _, err := q.admin.Create(ctx, users.Requester{Actor: audit.System, Transport: audit.TransportUI},
		users.NewUser{Name: arg.Login, Login: arg.Login, Role: auth.RoleViewer})
	return false, err
}

// TestIntegrationLoginTakenMeanwhile: a login taken between the check and the insert rolls the transaction back and
// refuses the sign-in with login_taken and its Audit log entry, not with idp_error.
func TestIntegrationLoginTakenMeanwhile(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		w := setupWith(t, s, func(st oidc.Store, admin *users.Admin) oidc.Store {
			return racingStore{Store: st, admin: admin}
		})
		out := w.signIn(t, fakeoidc.User{Subject: "u-7", PreferredUsername: "dave", Groups: []string{"oncall"}}, "")
		if out.Redirect != "/sign-in?error=login_taken" {
			t.Fatalf("outcome = %+v", out)
		}
		if n := w.scalar(t, `SELECT count(*) FROM users WHERE oidc_subject = 'u-7'`); n != int64(0) {
			t.Errorf("%v accounts hold the identity", n)
		}
		if refused := w.actions(t, oidc.ActionSignInRefused); len(refused) != 1 || refused[0]["reason"] != "login_taken" {
			t.Errorf("refusal = %v", refused)
		}
	})
}

// local creates a local user with a password through a setup link and signs it in twice; it returns the user and
// the two sessions.
func (w *world) local(t *testing.T, login, password string) (users.User, auth.Session, auth.Session) {
	t.Helper()
	r := users.Requester{Actor: audit.System, Transport: audit.TransportUI}
	u, link, err := w.admin.Create(t.Context(), r, users.NewUser{Name: login, Login: login, Role: auth.RoleResponder})
	if err != nil {
		t.Fatal(err)
	}
	token := link.URL[strings.Index(link.URL, "#token=")+len("#token="):]
	if err := w.admin.CompleteSetup(t.Context(), token, password, netip.Addr{}); err != nil {
		t.Fatal(err)
	}
	var sess [2]auth.Session
	for i := range sess {
		if sess[i], err = w.sessions.SignIn(t.Context(), auth.SignInRequest{Login: login, Password: password}); err != nil {
			t.Fatal(err)
		}
	}
	return u, sess[0], sess[1]
}

// link runs a link in sess through the fake IdP, which approves who.
func (w *world) link(t *testing.T, sess auth.Session, who fakeoidc.User) (oidc.Outcome, oidc.Callback) {
	t.Helper()
	w.idp.SetNextUser(who)
	start, err := w.svc.StartLink(t.Context(), sess)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, start.URL, nil)
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	cb := oidc.Callback{Code: back.Query().Get("code"), State: back.Query().Get("state")}
	return w.svc.CompleteLink(t.Context(), sess, cb), cb
}

// TestIntegrationLinkAndConvert is C-03.AC-20 and C-03.AC-21 against PostgreSQL: Alice links OIDC from her session —
// her other session ends, her password no longer signs in, she signs in through OIDC into the same account; Bob's link
// of her identity is refused; a callback in another session is invalid_request; conversion to local removes the
// identity, ends the sessions and returns a setup link, after which her OIDC sign-in is login_taken; the CLI reset of
// an OIDC account removes the identity.
func TestIntegrationLinkAndConvert(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		w := setup(t, s)
		alice, aliceSess, aliceOther := w.local(t, "alice", "alice-password-1")
		aliceIDP := fakeoidc.User{Subject: "u-3", PreferredUsername: "alice", Groups: []string{"oncall"}}
		if out, _ := w.link(t, aliceSess, aliceIDP); out.Redirect != oidc.ProfilePage {
			t.Fatalf("link = %+v", out)
		}
		if got := w.scalar(t, `SELECT (oidc_subject = 'u-3' AND password_hash IS NULL
			AND oidc_offline_token_ciphertext IS NOT NULL)::text FROM users WHERE id = $1`, alice.ID); got != "true" {
			t.Fatalf("alice after the link: %v", got)
		}
		if got := w.scalar(t, `SELECT string_agg(public_id || ':' || method || ':' || coalesce(end_reason, 'live'), ',' ORDER BY id)
			FROM sessions WHERE user_id = $1`, alice.ID); got != aliceSess.PublicID+":oidc:live,"+aliceOther.PublicID+
			":local:oidc_linked" {
			t.Errorf("sessions = %v", got)
		}
		if _, err := w.sessions.Authenticate(ctx, aliceSess.Cookie()); err != nil {
			t.Errorf("the session that linked: %v", err)
		}
		if n := w.scalar(t, `SELECT count(*) FROM oidc_checks WHERE user_id = $1`, alice.ID); n != int64(1) {
			t.Errorf("%v re-checks", n)
		}
		if _, err := w.sessions.SignIn(ctx, auth.SignInRequest{Login: "alice", Password: "alice-password-1"}); !errors.Is(
			err, auth.ErrInvalidCredentials) {
			t.Errorf("the password after the link: %v", err)
		}
		if out := w.signIn(t, aliceIDP, ""); out.Session == nil || out.Session.User.ID != alice.ID {
			t.Errorf("the OIDC sign-in after the link = %+v", out)
		}
		if u, _ := w.admin.Get(ctx, alice.PublicID); u.SignInMethod() != "oidc" || !u.RoleLocked {
			t.Errorf("users shows %+v", u)
		}
		admin := users.Requester{Actor: audit.System, Transport: audit.TransportUI}
		if _, err := w.admin.Update(ctx, admin, alice.PublicID, nil, users.Changes{Name: "alice",
			Role: auth.RoleViewer}); !errors.Is(err, users.ErrRoleLocked) {
			t.Errorf("a Role change of the linked account = %v, want role_locked", err)
		}
		if linked := w.actions(t, oidc.ActionLinked); len(linked) != 1 || linked[0]["sessions_ended"] != 1.0 {
			t.Errorf("user.oidc_linked = %v", linked)
		}

		bob, bobSess, _ := w.local(t, "bob", "bob-password-12")
		if out, _ := w.link(t, bobSess, aliceIDP); out.Redirect != "/profile?error=identity_linked_elsewhere" {
			t.Errorf("Bob's link of Alice's identity = %+v", out)
		}
		if got := w.scalar(t, `SELECT (oidc_subject IS NULL AND password_hash IS NOT NULL)::text FROM users
			WHERE id = $1`, bob.ID); got != "true" {
			t.Errorf("bob changed: %v", got)
		}
		if refused := w.actions(t, oidc.ActionLinkRefused); len(refused) != 1 ||
			refused[0]["reason"] != "identity_linked_elsewhere" {
			t.Errorf("user.oidc_link_refused = %v", refused)
		}
		// A callback replayed in another session.
		carol, carolSess, _ := w.local(t, "carol", "carol-password-1")
		_, cb := w.link(t, carolSess, fakeoidc.User{Subject: "u-4", PreferredUsername: "carol",
			Groups: []string{"oncall"}})
		if out := w.svc.CompleteLink(ctx, bobSess, cb); out.Redirect != "/profile?error=invalid_request" {
			t.Errorf("a replayed callback = %+v", out)
		}
		if got := w.scalar(t, `SELECT oidc_subject FROM users WHERE id = $1`, carol.ID); got != "u-4" {
			t.Errorf("carol = %v", got)
		}

		// Groups that map to no Role refuse the link with no_access, recorded with the groups.
		if out, _ := w.link(t, bobSess, fakeoidc.User{Subject: "u-6", PreferredUsername: "bob",
			Groups: []string{"contractors"}}); out.Redirect != "/profile?error=no_access" {
			t.Errorf("a link without access = %+v", out)
		}
		if refused := w.actions(t, oidc.ActionLinkRefused); len(refused) != 2 || refused[1]["reason"] != "no_access" ||
			len(refused[1]["groups"].([]any)) != 1 {
			t.Errorf("user.oidc_link_refused = %v", refused)
		}

		// Conversion back to local (C-03.AC-21).
		r := users.Requester{Actor: audit.System, Transport: audit.TransportUI}
		converted, link, err := w.admin.ConvertToLocal(ctx, r, alice.PublicID)
		if err != nil || converted.SignInMethod() != "local" || !strings.Contains(link.URL, "#token=") {
			t.Fatalf("convert = %+v, %+v, %v", converted, link, err)
		}
		if got := w.scalar(t, `SELECT (oidc_subject IS NULL AND oidc_offline_token_ciphertext IS NULL)::text
			FROM users WHERE id = $1`, alice.ID); got != "true" {
			t.Errorf("alice after the conversion: %v", got)
		}
		if n := w.scalar(t, `SELECT count(*) FROM sessions WHERE user_id = $1 AND ended_at IS NULL`, alice.ID); n !=
			int64(0) {
			t.Errorf("%v sessions live after the conversion", n)
		}
		if n := w.scalar(t, `SELECT count(*) FROM sessions WHERE user_id = $1 AND end_reason = 'converted_to_local'`,
			alice.ID); n != int64(2) {
			t.Errorf("%v sessions ended converted_to_local", n)
		}
		if n := w.scalar(t, `SELECT count(*) FROM oidc_checks WHERE user_id = $1`, alice.ID); n != int64(0) {
			t.Errorf("the re-check stayed")
		}
		if out := w.signIn(t, aliceIDP, ""); out.Redirect != "/sign-in?error=login_taken" {
			t.Errorf("the OIDC sign-in after the conversion = %+v", out)
		}

		// The CLI reset of an account created through OIDC.
		if out := w.signIn(t, fakeoidc.User{Subject: "u-5", PreferredUsername: "dan", Groups: []string{"oncall"}},
			""); out.Session == nil {
			t.Fatal(out)
		}
		if _, err := w.admin.ResetPassword(ctx, "ops", "dan", "dan-password-123"); err != nil {
			t.Fatal(err)
		}
		if got := w.scalar(t, `SELECT (oidc_subject IS NULL AND password_hash IS NOT NULL
			AND oidc_offline_token_ciphertext IS NULL)::text FROM users WHERE login = 'dan'`); got != "true" {
			t.Errorf("dan after the reset: %v", got)
		}
		if got := w.scalar(t, `SELECT actor_name || ':' || (details ->> 'oidc_identity_removed') FROM audit_log
			WHERE action = 'user.password_reset'`); got != "ops:true" {
			t.Errorf("user.password_reset = %v", got)
		}
		if _, err := w.sessions.SignIn(ctx, auth.SignInRequest{Login: "dan", Password: "dan-password-123"}); err != nil {
			t.Errorf("dan signs in with the password: %v", err)
		}
	})
}

// TestIntegrationRecheck is C-03.AC-22, AC-23, AC-24 and AC-26 at a re-check against PostgreSQL, with the shared claim
// and a manual business clock.
func TestIntegrationRecheck(t *testing.T) {
	dbtest.ForEach(t, func(t *testing.T, s dbtest.Server) {
		ctx := t.Context()
		w := setup(t, s)
		lease := db.Lease{Owner: "r1", Duration: oidc.RecheckLease,
			Clocks: clock.Clocks{Business: w.business, Real: clock.Real{}}}
		claim := oidc.NewClaimer(w.d.Pool, lease)
		recheck := func() int {
			t.Helper()
			n, err := w.svc.Recheck(ctx, claim, "r1")
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		olgaIDP := fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"oncall"}}
		out := w.signIn(t, olgaIDP, "")
		if out.Session == nil {
			t.Fatal(out)
		}
		if got := w.scalar(t, `SELECT (deadline = $1)::text FROM oidc_checks`, t0.Add(oidc.RecheckInterval)); got != "true" {
			t.Fatalf("the re-check is not due one interval after the sign-in")
		}
		var ct []byte
		var keyID string
		if err := w.d.Pool.QueryRow(ctx, `SELECT oidc_offline_token_ciphertext, oidc_offline_token_key_id FROM users
			WHERE login = 'olga'`).Scan(&ct, &keyID); err != nil {
			t.Fatal(err)
		}
		if plain, err := w.keyring.Decrypt("users.oidc_offline_token", keyID, ct); err != nil ||
			bytes.Contains(ct, plain) {
			t.Fatalf("the offline token is not encrypted: %v", err)
		}
		if n := recheck(); n != 0 {
			t.Fatalf("%d re-checks before the interval", n)
		}

		// Unavailable (C-03.AC-23): nothing changes, and the next attempt is one interval later.
		if err := w.idp.SetFault(fakeserver.Fault{Path: "/token", Status: http.StatusServiceUnavailable}); err != nil {
			t.Fatal(err)
		}
		w.business.Advance(oidc.RecheckInterval)
		if n := recheck(); n != 1 {
			t.Fatalf("%d re-checks at the interval", n)
		}
		if got := w.scalar(t, `SELECT last_outcome || ':' || (deadline = $1)::text || ':' || (lease_owner IS NULL)::text
			FROM oidc_checks`, w.business.Now().Add(oidc.RecheckInterval)); got != "unavailable:true:true" {
			t.Errorf("after an unavailable IdP: %v", got)
		}
		if _, err := w.sessions.Authenticate(ctx, out.Session.Cookie()); err != nil {
			t.Errorf("the session after an unavailable IdP: %v", err)
		}
		w.idp.ResetFaults()
		w.business.Advance(oidc.RecheckInterval)
		recheck()
		if got := w.scalar(t, `SELECT last_outcome FROM oidc_checks`); got != "ok" {
			t.Errorf("after the IdP recovered: %v", got)
		}

		// Refusal (C-03.AC-22).
		w.idp.SetDisabled("u-1", true)
		w.business.Advance(oidc.RecheckInterval)
		recheck()
		if _, err := w.sessions.Authenticate(ctx, out.Session.Cookie()); !errors.Is(err, auth.ErrOIDCSessionEnded) {
			t.Errorf("the session after the refusal: %v", err)
		}
		if got := w.scalar(t, `SELECT (oidc_refused_at IS NOT NULL AND oidc_offline_token_ciphertext IS NULL)::text
			FROM users WHERE login = 'olga'`); got != "true" {
			t.Errorf("olga after the refusal: %v", got)
		}
		if n := w.scalar(t, `SELECT count(*) FROM oidc_checks`); n != int64(0) {
			t.Errorf("the re-check stayed after the refusal")
		}
		if refused := w.actions(t, oidc.ActionUserRefused); len(refused) != 1 || refused[0]["reason"] != "invalid_grant" {
			t.Errorf("user.oidc_refused = %v", refused)
		}
		w.idp.SetDisabled("u-1", false)
		again := w.signIn(t, olgaIDP, "")
		if _, err := w.sessions.Authenticate(ctx, again.Session.Cookie()); err != nil {
			t.Errorf("the session after a new sign-in: %v", err)
		}
		if got := w.scalar(t, `SELECT (oidc_refused_at IS NULL)::text FROM users WHERE login = 'olga'`); got != "true" {
			t.Error("the new sign-in did not lift the refusal")
		}

		// Skipped: no live session and no Personal access token; the IdP is not called.
		if _, err := w.d.Pool.Exec(ctx, `UPDATE sessions SET ended_at = $1, end_reason = 'sign_out'
			WHERE ended_at IS NULL`, w.business.Now()); err != nil {
			t.Fatal(err)
		}
		w.idp.ResetRequests()
		w.business.Advance(oidc.RecheckInterval)
		recheck()
		if got := w.scalar(t, `SELECT last_outcome FROM oidc_checks`); got != "skipped" || len(w.idp.Requests()) != 0 {
			t.Errorf("without a live session: %v, %d requests to the IdP", got, len(w.idp.Requests()))
		}

		// Without offline_access (C-03.AC-24): the session ends at the fallback lifetime and no re-check exists.
		withToken := w.signIn(t, olgaIDP, "")
		w.idp.Configure(fakeoidc.Config{GrantOfflineAccess: new(false)})
		fallback := w.signIn(t, olgaIDP, "")
		// The session opened with the token wiped now is no longer re-checked: it gets the fallback lifetime too.
		if got := w.scalar(t, `SELECT (expires_at <= $2)::text FROM sessions WHERE public_id = $1`,
			withToken.Session.PublicID, w.business.Now().Add(oidc.FallbackSessionLifetime)); got != "true" {
			t.Errorf("the earlier session kept its lifetime")
		}
		if got := w.scalar(t, `SELECT (extract(epoch FROM expires_at - created_at) / 3600)::int FROM sessions
			WHERE public_id = $1`, fallback.Session.PublicID); got != int32(12) && got != int64(12) {
			t.Errorf("the fallback session lives %v hours", got)
		}
		if n := w.scalar(t, `SELECT count(*) FROM oidc_checks`); n != int64(0) {
			t.Errorf("a re-check exists without an offline token")
		}
		w.idp.Configure(fakeoidc.Config{GrantOfflineAccess: new(true)})

		// The last active Admin at a re-check (C-03.AC-26).
		ada := fakeoidc.User{Subject: "u-9", PreferredUsername: "ada", Groups: []string{"muster-admins"}}
		adaOut := w.signIn(t, ada, "")
		if _, err := w.d.Pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE login = 'ops@example.org'`); err != nil {
			t.Fatal(err)
		}
		ada.Groups = []string{"oncall"}
		w.idp.SetNextUser(ada)
		w.business.Advance(oidc.RecheckInterval)
		recheck()
		if role := w.scalar(t, `SELECT role FROM users WHERE login = 'ada'`); role != "admin" {
			t.Fatalf("the last active Admin was lowered at a re-check: %v", role)
		}
		if _, err := w.sessions.Authenticate(ctx, adaOut.Session.Cookie()); err != nil {
			t.Errorf("ada's session after the kept Role: %v", err)
		}
		kept := w.actions(t, oidc.ActionRoleSyncKeptAdmin)
		if len(kept) != 1 || kept[0]["mapped_role"] != "responder" || kept[0]["at"] != "recheck" {
			t.Errorf("user.role_sync_kept_admin = %v", kept)
		}
		st, _ := w.svc.Get(ctx)
		if !slices.ContainsFunc(st.Warnings, func(x oidc.Warning) bool { return x.Kind == oidc.WarningLastAdminKept }) {
			t.Errorf("warnings = %+v", st.Warnings)
		}
		if _, err := w.d.Pool.Exec(ctx, `UPDATE users SET status = 'active' WHERE login = 'ops@example.org'`); err != nil {
			t.Fatal(err)
		}
		w.business.Advance(oidc.RecheckInterval)
		recheck()
		if role := w.scalar(t, `SELECT role FROM users WHERE login = 'ada'`); role != "responder" {
			t.Errorf("with a second Admin ada is %v", role)
		}
		if _, err := w.sessions.Authenticate(ctx, adaOut.Session.Cookie()); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("ada's session after the Role change: %v", err)
		}
		st, _ = w.svc.Get(ctx)
		if slices.ContainsFunc(st.Warnings, func(x oidc.Warning) bool { return x.Kind == oidc.WarningLastAdminKept }) {
			t.Error("the warning stayed")
		}

		// Disabling the user wipes the token and the re-check.
		r := users.Requester{Actor: audit.System, Transport: audit.TransportUI}
		adaUser, _ := w.admin.List(ctx, users.ListFilter{Q: "ada", Limit: 1})
		if _, err := w.admin.Disable(ctx, r, adaUser.Users[0].PublicID); err != nil {
			t.Fatal(err)
		}
		if got := w.scalar(t, `SELECT (oidc_offline_token_ciphertext IS NULL)::text || ':' ||
			(SELECT count(*) FROM oidc_checks c WHERE c.user_id = u.id)::text FROM users u WHERE login = 'ada'`); got !=
			"true:0" {
			t.Errorf("ada after disable: %v", got)
		}
	})
}
