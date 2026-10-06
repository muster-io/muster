// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/fakes/fakeoidc"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/oidc/dbgen"
)

var browser = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// authorize follows the start's redirect to the fake IdP and returns the callback it sends the browser to.
func (e *env) authorize(t *testing.T, start Start) Callback {
	t.Helper()
	u, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") != start.State ||
		q.Get("nonce") == "" || q.Get("redirect_uri") != "http://localhost:8080/"+CallbackPath ||
		!strings.HasPrefix(q.Get("scope"), "openid offline_access") {
		t.Fatalf("the authorization URL = %s", start.URL)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, start.URL, nil)
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	return Callback{Code: back.Query().Get("code"), State: back.Query().Get("state"), Error: back.Query().Get("error"),
		CookieState: start.State}
}

// signIn runs a whole sign-in of u with returnTo.
func (e *env) signIn(t *testing.T, u fakeoidc.User, returnTo string) Outcome {
	t.Helper()
	e.idp.SetNextUser(u)
	start, err := e.svc.StartSignIn(t.Context(), returnTo)
	if err != nil {
		t.Fatal(err)
	}
	return e.svc.CompleteSignIn(t.Context(), e.authorize(t, start))
}

func (e *env) user(login string) *memUser {
	for _, u := range e.store.users {
		if strings.EqualFold(u.login, login) {
			return u
		}
	}
	return nil
}

// TestFirstSignInCreatesTheUser is C-03.FR-7, FR-28, AC-1 and AC-16: a mapped group creates the user with the mapped
// Role and lands on return_to; a person in no mapped group is refused with no_access and the groups in the Audit log.
func TestFirstSignInCreatesTheUser(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	out := e.signIn(t, fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Email: "olga@example.org",
		Name: "Olga", Groups: []string{"oncall", "staff"}}, "/profile")
	if out.Session == nil || out.Redirect != "/profile" || out.Error != "" {
		t.Fatalf("outcome = %+v; log %s", out, e.log)
	}
	olga := e.user("olga")
	if olga == nil || olga.role != auth.RoleResponder || olga.email != "olga@example.org" || olga.name != "Olga" ||
		olga.subject != "u-1" || olga.issuer != e.idp.URL() || olga.lastContact == nil {
		t.Fatalf("user = %+v", olga)
	}
	opened := e.sessions.opened[0]
	if opened.State != auth.StateActive || opened.User.ID != olga.id || opened.Lifetime != auth.SessionLifetime {
		t.Errorf("session = %+v", opened)
	}
	created := e.store.audited("user.created")
	if len(created) != 1 || created[0]["details"].(map[string]any)["method"] != "oidc" {
		t.Errorf("user.created = %v", created)
	}

	out = e.signIn(t, fakeoidc.User{Subject: "u-2", PreferredUsername: "nora", Groups: []string{"contractors"}}, "")
	if out.Session != nil || out.Redirect != "/sign-in?error=no_access" {
		t.Fatalf("no mapped group = %+v", out)
	}
	refused := e.store.audited(ActionSignInRefused)
	if len(refused) != 1 || refused[0]["details"].(map[string]any)["reason"] != "no_access" ||
		len(refused[0]["details"].(map[string]any)["groups"].([]any)) != 1 || e.user("nora") != nil {
		t.Errorf("refusal = %v", refused)
	}

	// return_to that is not a relative path is ignored: the flow is no open redirect.
	for _, rt := range []string{"//evil.example", "https://evil.example", "/\\evil.example", "evil", "/\x00x"} {
		out = e.signIn(t, fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"oncall"}}, rt)
		if out.Redirect != "/" {
			t.Errorf("return_to %q landed on %q", rt, out.Redirect)
		}
	}
}

// TestLoginTaken is C-03.FR-28: a new identity whose login a local user has is refused and changes nothing.
func TestLoginTaken(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	e.store.users = append(e.store.users, &memUser{id: 1, publicID: "SR0000000000AL", login: "Alice", name: "Alice",
		role: auth.RoleViewer, status: "active"})
	out := e.signIn(t, fakeoidc.User{Subject: "u-3", PreferredUsername: "alice", Groups: []string{"oncall"}}, "")
	if out.Redirect != "/sign-in?error=login_taken" || len(e.store.users) != 1 || e.store.users[0].subject != "" ||
		e.store.users[0].role != auth.RoleViewer {
		t.Fatalf("outcome %+v, users %+v", out, e.store.users[0])
	}
	if r := e.store.audited(ActionSignInRefused); len(r) != 1 || r[0]["details"].(map[string]any)["login"] != "alice" {
		t.Errorf("refusal = %v", r)
	}
	e.store.raceLogin = true
	out = e.signIn(t, fakeoidc.User{Subject: "u-4", PreferredUsername: "bob", Groups: []string{"oncall"}}, "")
	if out.Error != ErrorLoginTaken {
		t.Errorf("a login taken at the same time = %+v", out)
	}
	// A parallel callback of the same identity created the account: the second attempt signs into it.
	e.store.raceIdentity = true
	out = e.signIn(t, fakeoidc.User{Subject: "u-5", PreferredUsername: "carl", Groups: []string{"oncall"}}, "")
	if out.Session == nil || e.user("carl") == nil {
		t.Errorf("an identity created at the same time = %+v", out)
	}
}

// TestTOTPAfterOIDCSignIn is C-03.FR-10 and AC-13.
func TestTOTPAfterOIDCSignIn(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	u := fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"oncall"}, AMR: []string{"pwd", "otp"}}
	e.signIn(t, u, "")
	e.user("olga").totp = true
	if out := e.signIn(t, u, ""); out.Session.State != auth.StateTOTPRequired {
		t.Errorf("with TOTP = %s", out.Session.State)
	}
	e.configure(t, func(in *Input) { in.SkipTOTPWithIDPMFA = true })
	if out := e.signIn(t, u, ""); out.Session.State != auth.StateActive || !e.sessions.opened[2].IDPMFA {
		t.Errorf("with the IdP's MFA skipped = %s", out.Session.State)
	}
	u.AMR = []string{"pwd"}
	if out := e.signIn(t, u, ""); out.Session.State != auth.StateTOTPRequired {
		t.Errorf("without the IdP's MFA = %s", out.Session.State)
	}
	e.user("olga").totp = false
	e.store.policy = "everyone"
	if out := e.signIn(t, u, ""); out.Session.State != auth.StateTOTPEnrolmentRequired {
		t.Errorf("under the policy everyone = %s", out.Session.State)
	}
	e.store.policy = "local_users"
	if out := e.signIn(t, u, ""); out.Session.State != auth.StateActive {
		t.Errorf("under the policy local_users = %s", out.Session.State)
	}
}

// TestRoleSync is C-03.FR-5 and FR-32 (AC-26 at a sign-in): with sync_role the Role follows the mapping and the other
// sessions end, except that the last active Admin keeps Admin with the warning last_admin_kept until another Admin is
// active.
func TestRoleSync(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	ada := fakeoidc.User{Subject: "u-9", PreferredUsername: "ada", Groups: []string{"muster-admins"}}
	e.signIn(t, ada, "")
	if e.user("ada").role != auth.RoleAdmin {
		t.Fatal("ada is not an Admin")
	}
	ada.Groups = []string{"oncall"}
	e.business.Advance(time.Minute)
	if out := e.signIn(t, ada, ""); out.Session == nil || e.user("ada").role != auth.RoleAdmin {
		t.Fatalf("the last active Admin was lowered: %+v %s", out, e.user("ada").role)
	}
	kept := e.store.audited(ActionRoleSyncKeptAdmin)
	if len(kept) != 1 || kept[0]["details"].(map[string]any)["mapped_role"] != "responder" {
		t.Fatalf("kept = %v", kept)
	}
	st, _ := e.svc.Get(t.Context())
	i := slices.IndexFunc(st.Warnings, func(w Warning) bool { return w.Kind == WarningLastAdminKept })
	if i < 0 || st.Warnings[i].Role != "responder" || st.Warnings[i].User.Login != "ada" {
		t.Fatalf("warnings = %+v", st.Warnings)
	}

	// The IdP maps ada to Admin again: no warning, although she is still the last active Admin.
	ada.Groups = []string{"muster-admins"}
	e.business.Advance(time.Minute)
	e.signIn(t, ada, "")
	if st, _ := e.svc.Get(t.Context()); slices.Contains(kinds(st.Warnings), WarningLastAdminKept) {
		t.Error("the warning stayed after the IdP mapped the user to Admin again")
	}

	// With a second active Admin the lower Role applies and ada's other sessions end.
	e.store.users = append(e.store.users, &memUser{id: 99, publicID: "SR00000000000B", login: "bootstrap",
		role: auth.RoleAdmin, status: "active"})
	e.user("ada").liveSessions = 2
	ada.Groups = []string{"oncall"}
	e.business.Advance(time.Minute)
	e.signIn(t, ada, "")
	if u := e.user("ada"); u.role != auth.RoleResponder || u.endedCount != 2 || u.endReasons[0] != "role_changed" {
		t.Fatalf("after a second Admin: %+v", u)
	}
	if st, _ := e.svc.Get(t.Context()); slices.Contains(kinds(st.Warnings), WarningLastAdminKept) {
		t.Error("the warning stayed with a second active Admin")
	}
	changed := e.store.audited("user.role_changed")
	if len(changed) != 1 || changed[0]["details"].(map[string]any)["source"] != "oidc_sync" {
		t.Errorf("role_changed = %v", changed)
	}

	// With sync off the mapping applies only at creation.
	e.configure(t, func(in *Input) { in.SyncRole = false })
	ada.Groups = []string{"muster-admins"}
	e.signIn(t, ada, "")
	if e.user("ada").role != auth.RoleResponder {
		t.Error("the Role changed with sync_role off")
	}
}

// TestRefusals is C-03.FR-25: a disabled account, OIDC switched off, and invalid callbacks.
func TestRefusals(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"oncall"}}
	e.signIn(t, olga, "")
	e.user("olga").status = "disabled"
	if out := e.signIn(t, olga, ""); out.Error != ErrorAccountDisabled {
		t.Errorf("a disabled account = %+v", out)
	}
	e.user("olga").status = "active"

	e.idp.SetNextUser(olga)
	start, err := e.svc.StartSignIn(t.Context(), "/x")
	if err != nil {
		t.Fatal(err)
	}
	cb := e.authorize(t, start)
	for name, bad := range map[string]Callback{
		"no state":       {Code: cb.Code, CookieState: cb.CookieState},
		"no cookie":      {Code: cb.Code, State: cb.State},
		"another cookie": {Code: cb.Code, State: cb.State, CookieState: "other"},
		"unknown state":  {Code: cb.Code, State: "unknown", CookieState: "unknown"},
	} {
		if out := e.svc.CompleteSignIn(t.Context(), bad); out.Error != ErrorInvalidRequest {
			t.Errorf("%s = %+v", name, out)
		}
	}
	if out := e.svc.CompleteSignIn(t.Context(), cb); out.Session == nil {
		t.Fatalf("the valid callback = %+v; log %s", out, e.log)
	}
	if out := e.svc.CompleteSignIn(t.Context(), cb); out.Error != ErrorInvalidRequest {
		t.Errorf("a replayed callback = %+v", out)
	}

	// An expired request, a callback without a code, an IdP error, OIDC switched off meanwhile.
	start, _ = e.svc.StartSignIn(t.Context(), "")
	cb = e.authorize(t, start)
	e.business.Advance(AuthRequestTTL + time.Second)
	if out := e.svc.CompleteSignIn(t.Context(), cb); out.Error != ErrorInvalidRequest {
		t.Errorf("an expired request = %+v", out)
	}
	start, _ = e.svc.StartSignIn(t.Context(), "")
	if out := e.svc.CompleteSignIn(t.Context(), Callback{State: start.State, CookieState: start.State}); out.Error !=
		ErrorInvalidRequest {
		t.Errorf("no code = %+v", out)
	}
	start, _ = e.svc.StartSignIn(t.Context(), "")
	out := e.svc.CompleteSignIn(t.Context(), Callback{State: start.State, CookieState: start.State,
		Error: "access_denied\n"})
	if out.Error != ErrorIDPError || !strings.Contains(e.log.String(), `"event":"oidc_sign_in_failed"`) {
		t.Errorf("an IdP error = %+v; log %s", out, e.log)
	}
	start, _ = e.svc.StartSignIn(t.Context(), "")
	cb = e.authorize(t, start)
	e.configure(t, func(in *Input) { in.Enabled = false })
	if out := e.svc.CompleteSignIn(t.Context(), cb); out.Error != ErrorOIDCDisabled {
		t.Errorf("OIDC switched off = %+v", out)
	}
	if out := e.svc.CompleteSignIn(t.Context(), cb); out.Error != ErrorInvalidRequest {
		t.Errorf("the request of a refused callback is used up: %+v", out)
	}
}

// TestBackChannelFailures: a failed exchange, a token that does not verify, a nonce of another request and a session
// that cannot be opened end at idp_error or invalid_request, with no session and nothing in the log that is secret.
func TestBackChannelFailures(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	olga := fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"oncall"}}
	e.idp.SetNextUser(olga)
	start, _ := e.svc.StartSignIn(t.Context(), "")
	cb := e.authorize(t, start)
	cb.Code = "wrong-code"
	if out := e.svc.CompleteSignIn(t.Context(), cb); out.Error != ErrorIDPError || out.Session != nil {
		t.Errorf("a code the IdP refuses = %+v", out)
	}
	if !strings.Contains(e.log.String(), "invalid_grant") || strings.Contains(e.log.String(), "s3cr3t") {
		t.Errorf("log = %s", e.log)
	}

	// The nonce of another request: the code of the first request with the state of the second.
	first, _ := e.svc.StartSignIn(t.Context(), "")
	cb1 := e.authorize(t, first)
	second, _ := e.svc.StartSignIn(t.Context(), "")
	if out := e.svc.CompleteSignIn(t.Context(), Callback{Code: cb1.Code, State: second.State,
		CookieState: second.State}); out.Error != ErrorIDPError && out.Error != ErrorInvalidRequest {
		t.Errorf("a code of another request = %+v", out)
	}

	// A token whose clock is off by more than the leeway does not verify.
	e.idp.SetClock(func() time.Time { return e.rc.Now().Add(-time.Hour) })
	start, _ = e.svc.StartSignIn(t.Context(), "")
	if out := e.svc.CompleteSignIn(t.Context(), e.authorize(t, start)); out.Error != ErrorIDPError {
		t.Errorf("an expired ID token = %+v", out)
	}
	e.idp.SetClock(e.rc.Now)

	e.sessions.err = errors.New("db down")
	start, _ = e.svc.StartSignIn(t.Context(), "")
	if out := e.svc.CompleteSignIn(t.Context(), e.authorize(t, start)); out.Error != ErrorIDPError {
		t.Errorf("a session that cannot be opened = %+v", out)
	}
	e.sessions.err = nil

	if err := e.idp.SetFault(fakeserver.Fault{Path: "/.well-known/openid-configuration",
		Status: http.StatusNotFound}); err != nil {
		t.Fatal(err)
	}
	e.configure(t, func(in *Input) { in.ClientID = "other" }) // a new version reads discovery again
	var be *BackChannelError
	if _, err := e.svc.StartSignIn(t.Context(), ""); !errors.As(err, &be) || be.Step != "discovery" {
		t.Errorf("a failed discovery at the start = %v", err)
	}
	e.idp.ResetFaults()
}

// TestGroupsFromUserinfo is D247: the groups claim is read from userinfo when the ID token lacks it; a token and
// userinfo without it record groups_claim_missing, which a sign-in with the claim clears.
func TestGroupsFromUserinfo(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	out := e.signIn(t, fakeoidc.User{Subject: "u-1", PreferredUsername: "olga", Groups: []string{"oncall"},
		GroupsInUserinfoOnly: true}, "")
	if out.Session == nil || e.user("olga").role != auth.RoleResponder {
		t.Fatalf("groups from userinfo = %+v", out)
	}
	e.configure(t, func(in *Input) { in.GroupsClaim = "roles"; in.UnmatchedRole = UnmatchedViewer })
	e.signIn(t, fakeoidc.User{Subject: "u-2", PreferredUsername: "vic"}, "")
	st, _ := e.svc.Get(t.Context())
	if e.user("vic").role != auth.RoleViewer || !slices.Contains(kinds(st.Warnings), WarningGroupsClaimMissing) {
		t.Fatalf("without the claim: role %s, warnings %v", e.user("vic").role, kinds(st.Warnings))
	}
	e.configure(t, func(in *Input) { in.UnmatchedRole = UnmatchedViewer })
	e.signIn(t, fakeoidc.User{Subject: "u-2", PreferredUsername: "vic", Groups: []string{"x"}}, "")
	if st, _ := e.svc.Get(t.Context()); slices.Contains(kinds(st.Warnings), WarningGroupsClaimMissing) {
		t.Error("a sign-in with the claim did not clear groups_claim_missing")
	}
}

// TestPendingRequestsAreBounded: starts need no credentials, so the requests in flight per Organization are capped.
func TestPendingRequestsAreBounded(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	for range 3 {
		if _, err := e.svc.StartSignIn(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
	}
	for i := range MaxPendingRequests - 3 {
		e.store.requests[string(rune(i))+"x"] = dbgen.InsertAuthRequestParams{}
	}
	if _, err := e.svc.StartSignIn(t.Context(), ""); !errors.Is(err, ErrTooManyRequests) {
		t.Errorf("a start above the cap = %v", err)
	}
}

func TestAuditGroups(t *testing.T) {
	many := make([]string, 150)
	for i := range many {
		many[i] = strings.Repeat("g", 300)
	}
	got := auditGroups(many)
	if len(got) != maxAuditGroups+1 || got[maxAuditGroups] != "…" || len(got[0]) != maxAuditGroupBytes {
		t.Errorf("auditGroups = %d items, first %d bytes", len(got), len(got[0]))
	}
	if got := auditGroups(nil); got == nil || len(got) != 0 {
		t.Errorf("auditGroups(nil) = %#v", got)
	}
}

func TestMapRole(t *testing.T) {
	m := []GroupMapping{{Group: "a", Role: auth.RoleViewer}, {Group: "b", Role: auth.RoleAdmin},
		{Group: "c", Role: auth.RoleResponder}}
	for _, tc := range []struct {
		groups    []string
		unmatched string
		role      string
		ok        bool
	}{
		{[]string{"a", "c"}, UnmatchedNone, auth.RoleResponder, true},
		{[]string{"c", "b", "a"}, UnmatchedNone, auth.RoleAdmin, true},
		{[]string{"x"}, UnmatchedNone, "", false},
		{nil, UnmatchedViewer, auth.RoleViewer, true},
		{nil, "unknown", "", false},
	} {
		if role, ok := MapRole(tc.groups, m, tc.unmatched); role != tc.role || ok != tc.ok {
			t.Errorf("MapRole(%v, %s) = %s %v", tc.groups, tc.unmatched, role, ok)
		}
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{"/profile?tab=1": "/profile?tab=1", "/": "/", "": "", "//x": "",
		"/\\x": "", "https://x": "", "profile": "", "/a\\b": "", "/\tx": "", "/" + strings.Repeat("a", 2000): ""} {
		if got := SafeReturnTo(in); got != want {
			t.Errorf("SafeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
	for idt, want := range map[*IDToken]string{
		{PreferredUsername: "olga", Email: "o@example.org", Subject: "s"}:      "olga",
		{PreferredUsername: "has space", Email: "o@example.org", Subject: "s"}: "o@example.org",
		{PreferredUsername: "deleted-user-x", Subject: "s-1"}:                  "s-1",
		{Subject: "with space"}: "oidc-",
	} {
		if got := loginOf(*idt); !strings.HasPrefix(got, want) {
			t.Errorf("loginOf(%+v) = %q, want %q", idt, got, want)
		}
	}
	for amr, want := range map[string]bool{"mfa": true, "pwd otp": true, "pwd": false, "otp": false, "": false,
		"pin hwk": true} {
		if got := assertsMFA(strings.Fields(amr)); got != want {
			t.Errorf("assertsMFA(%q) = %v", amr, got)
		}
	}
	if validEmail("Olga <o@example.org>") != "" || validEmail("o@example.org") != "o@example.org" {
		t.Error("validEmail")
	}
	if got := printable("a\nb" + strings.Repeat("x", 100)); len(got) != 64 || strings.ContainsRune(got, '\n') {
		t.Errorf("printable = %q", got)
	}
}

type pruneQueries struct{ got dbgen.PruneAuthRequestsParams }

func (p *pruneQueries) PruneAuthRequests(_ context.Context, arg dbgen.PruneAuthRequestsParams) (int64, error) {
	p.got = arg
	return 3, nil
}

func TestPruner(t *testing.T) {
	q := &pruneQueries{}
	n, err := NewPruner(q).AuthRequests(t.Context(), 7, t0, 100)
	if err != nil || n != 3 || q.got.OrgID != 7 || !q.got.Before.Equal(t0.Add(-time.Hour)) || q.got.BatchSize != 100 {
		t.Errorf("prune = %d, %v, %+v", n, err, q.got)
	}
}
