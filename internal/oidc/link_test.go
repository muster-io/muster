// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/fakes/fakeoidc"
)

// local adds a local user with a password and live sessions, and returns it with its web session id.
func (e *env) local(id int64, login string) (*memUser, auth.Session) {
	u := &memUser{id: id, publicID: fmt.Sprintf("SR%012d", id), login: login, name: login,
		role: auth.RoleResponder, status: "active", password: true, totp: true, liveSessions: 3}
	e.store.users = append(e.store.users, u)
	return u, auth.Session{ID: 100 + id, PublicID: "SN0000000000S1", State: auth.StateActive, Method: auth.MethodLocal,
		User: auth.Principal{ID: u.id, PublicID: u.publicID, Name: u.name, Role: u.role}}
}

// startLink starts a link in sess and follows the redirect to the fake IdP, which approves who.
func (e *env) startLink(t *testing.T, sess auth.Session, who fakeoidc.User) Callback {
	t.Helper()
	e.idp.SetNextUser(who)
	start, err := e.svc.StartLink(t.Context(), sess)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("redirect_uri") != "http://localhost:8080/"+LinkCallbackPath ||
		!strings.HasPrefix(q.Get("scope"), "openid offline_access") || q.Get("code_challenge_method") != "S256" ||
		!start.ExpiresAt.Equal(e.business.Now().Add(AuthRequestTTL)) {
		t.Fatalf("the link start = %+v", start)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, start.URL, nil)
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound || back.Path != "/"+LinkCallbackPath {
		t.Fatalf("authorize = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	return Callback{Code: back.Query().Get("code"), State: back.Query().Get("state")}
}

// TestLink is C-03.FR-29, FR-9 and C-03.AC-20: Alice links an identity from her web session; the password goes, the
// TOTP enrolment stays, her other sessions end, the session continues as an OIDC session and the offline token is kept.
func TestLink(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	alice, sess := e.local(1, "Alice")
	out := e.svc.CompleteLink(t.Context(), sess, e.startLink(t, sess, fakeoidc.User{Subject: "u-3",
		PreferredUsername: "alice", Groups: []string{"oncall"}, AMR: []string{"pwd", "otp"}}))
	if out.Redirect != ProfilePage || out.Error != "" || out.Session != nil {
		t.Fatalf("outcome = %+v; log %s", out, e.log)
	}
	if alice.subject != "u-3" || alice.issuer != e.idp.URL() || alice.password || !alice.totp || alice.token == nil ||
		e.store.checks[alice.id] == nil || alice.liveSessions != 1 ||
		!slices.Equal(alice.endReasons, []string{auth.EndOIDCLinked}) {
		t.Fatalf("after the link: %+v", alice)
	}
	if c := e.store.continued; len(c) != 1 || c[0].ID != sess.ID || !c[0].IdpMfa ||
		!c[0].ExpiresAt.Equal(e.business.Now().Add(auth.SessionLifetime)) {
		t.Errorf("the session continues as %+v", c)
	}
	linked := e.store.audited(ActionLinked)
	if len(linked) != 1 || linked[0]["details"].(map[string]any)["sessions_ended"] != 2.0 ||
		linked[0]["resource"] != alice.publicID {
		t.Errorf("user.oidc_linked = %v", linked)
	}
	if _, err := e.svc.StartLink(t.Context(), sess); !errors.Is(err, ErrAlreadyLinked) {
		t.Errorf("a second link = %v, want ErrAlreadyLinked", err)
	}
	// Alice now signs in through OIDC into the same account, with her TOTP asked.
	in := e.signIn(t, fakeoidc.User{Subject: "u-3", PreferredUsername: "alice", Groups: []string{"oncall"}}, "")
	if in.Session == nil || in.Session.User.ID != alice.id || in.Session.State != auth.StateTOTPRequired {
		t.Errorf("the OIDC sign-in after the link = %+v", in)
	}
}

// TestLinkRefusals is C-03.FR-29 and C-03.AC-20: the identity of another user is refused with
// identity_linked_elsewhere and recorded; a callback in another session is invalid_request and leaves the link to the
// session that started it.
func TestLinkRefusals(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	alice, aliceSess := e.local(1, "Alice")
	alice.issuer, alice.subject, alice.password = e.idp.URL(), "u-3", false
	bob, bobSess := e.local(2, "Bob")
	out := e.svc.CompleteLink(t.Context(), bobSess, e.startLink(t, bobSess, fakeoidc.User{Subject: "u-3",
		PreferredUsername: "alice", Groups: []string{"oncall"}}))
	if out.Redirect != ProfilePage+"?error="+ErrorIdentityLinkedElsewhere || bob.subject != "" || !bob.password ||
		bob.liveSessions != 3 {
		t.Fatalf("the identity of another user = %+v, bob %+v", out, bob)
	}
	if r := e.store.audited(ActionLinkRefused); len(r) != 1 || r[0]["resource"] != bob.publicID ||
		r[0]["details"].(map[string]any)["reason"] != ErrorIdentityLinkedElsewhere {
		t.Errorf("user.oidc_link_refused = %v", r)
	}

	// A parallel link of the same identity that commits first: the unique index refuses the second.
	carl, carlSess := e.local(3, "Carl")
	cb := e.startLink(t, carlSess, fakeoidc.User{Subject: "u-7", PreferredUsername: "carl", Groups: []string{"oncall"}})
	other := &memUser{id: 9, publicID: "SR00000000000Z", login: "zed", name: "zed", status: "active",
		issuer: e.idp.URL(), subject: "u-7"}
	e.store.users = append(e.store.users, other)
	if out := e.svc.CompleteLink(t.Context(), carlSess, cb); out.Error != ErrorIdentityLinkedElsewhere ||
		carl.subject != "" {
		t.Errorf("a link that lost the race = %+v", out)
	}

	// A callback replayed in another session.
	dave, daveSess := e.local(4, "Dave")
	cb = e.startLink(t, daveSess, fakeoidc.User{Subject: "u-4", PreferredUsername: "dave", Groups: []string{"oncall"}})
	if out := e.svc.CompleteLink(t.Context(), aliceSess, cb); out.Redirect != ProfilePage+"?error="+ErrorInvalidRequest {
		t.Errorf("a callback in another session = %+v", out)
	}
	if out := e.svc.CompleteLink(t.Context(), daveSess, cb); out.Redirect != ProfilePage || dave.subject != "u-4" {
		t.Errorf("the callback in its session after the replay = %+v; log %s", out, e.log)
	}
	if out := e.svc.CompleteLink(t.Context(), daveSess, cb); out.Error != ErrorInvalidRequest {
		t.Errorf("a callback used twice = %+v", out)
	}
}

// TestLinkWithoutOfflineAccess: without an offline token the session that linked ends
// auth.oidc_fallback_session_lifetime after the link at the latest, and no re-check is scheduled.
func TestLinkWithoutOfflineAccess(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	no := false
	e.idp.Configure(fakeoidc.Config{GrantOfflineAccess: &no})
	erin, sess := e.local(5, "Erin")
	out := e.svc.CompleteLink(t.Context(), sess, e.startLink(t, sess, fakeoidc.User{Subject: "u-5",
		PreferredUsername: "erin", Groups: []string{"oncall"}}))
	if out.Redirect != ProfilePage || erin.token != nil || e.store.checks[erin.id] != nil {
		t.Fatalf("outcome %+v, erin %+v", out, erin)
	}
	if c := e.store.continued; len(c) != 1 || !c[0].ExpiresAt.Equal(e.business.Now().Add(FallbackSessionLifetime)) {
		t.Errorf("the session continues as %+v", c)
	}
}

// TestLinkFailures: OIDC off, an error of the IdP, a missing code, an expired request and an account that cannot take
// an identity.
func TestLinkFailures(t *testing.T) {
	e := newEnv(t)
	if _, sess := e.local(1, "Alice"); true {
		if _, err := e.svc.StartLink(t.Context(), sess); !errors.Is(err, ErrNotEnabled) {
			t.Errorf("a link while OIDC is not configured = %v", err)
		}
	}
	e.configure(t, nil)
	_, sess := e.local(2, "Bob")
	who := fakeoidc.User{Subject: "u-2", PreferredUsername: "bob", Groups: []string{"oncall"}}

	cb := e.startLink(t, sess, who)
	cb.Error, cb.Code = "access_denied", ""
	if out := e.svc.CompleteLink(t.Context(), sess, cb); out.Error != ErrorIDPError ||
		!strings.Contains(e.log.String(), `"reason":"link authorization"`) {
		t.Errorf("an error of the IdP = %+v; log %s", out, e.log)
	}
	cb = e.startLink(t, sess, who)
	cb.Code = ""
	if out := e.svc.CompleteLink(t.Context(), sess, cb); out.Error != ErrorInvalidRequest {
		t.Errorf("a callback without a code = %+v", out)
	}
	if out := e.svc.CompleteLink(t.Context(), sess, Callback{}); out.Error != ErrorInvalidRequest {
		t.Errorf("a callback without a state = %+v", out)
	}
	cb = e.startLink(t, sess, who)
	e.business.Advance(AuthRequestTTL)
	if out := e.svc.CompleteLink(t.Context(), sess, cb); out.Error != ErrorInvalidRequest {
		t.Errorf("an expired link = %+v", out)
	}
	cb = e.startLink(t, sess, who)
	e.configure(t, func(in *Input) { in.Enabled = false })
	if out := e.svc.CompleteLink(t.Context(), sess, cb); out.Error != ErrorOIDCDisabled {
		t.Errorf("a callback after OIDC was switched off = %+v", out)
	}
	if _, err := e.svc.StartLink(t.Context(), sess); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("a link while OIDC is off = %v", err)
	}

	// The session that started the link ended before the callback: nothing is linked.
	e.configure(t, nil)
	_, gsess := e.local(7, "Gina")
	cb = e.startLink(t, gsess, fakeoidc.User{Subject: "u-17", PreferredUsername: "gina", Groups: []string{"oncall"}})
	e.store.sessionGone = true
	if out := e.svc.CompleteLink(t.Context(), gsess, cb); out.Error != ErrorInvalidRequest {
		t.Errorf("a link whose session ended = %+v", out)
	}
	e.store.sessionGone = false

	// The account lost its password between the start and the callback: nothing is linked.
	e.configure(t, nil)
	frank, fsess := e.local(6, "Frank")
	cb = e.startLink(t, fsess, fakeoidc.User{Subject: "u-6", PreferredUsername: "frank", Groups: []string{"oncall"}})
	frank.password = false
	if out := e.svc.CompleteLink(t.Context(), fsess, cb); out.Error != ErrorInvalidRequest || frank.subject != "" {
		t.Errorf("a link of an account without a password = %+v", out)
	}
	if _, err := e.svc.StartLink(t.Context(), fsess); !errors.Is(err, ErrAlreadyLinked) {
		t.Errorf("a link start of an account without a password = %v", err)
	}
}

// TestLinkNoAccess is the maintainer's decision on C-03.FR-29: a link whose groups map to no Role, with no Role for
// unmatched users, is refused with no_access and recorded with the groups, as a sign-in is; the account is unchanged.
func TestLinkNoAccess(t *testing.T) {
	e := newEnv(t)
	e.configure(t, nil)
	hank, sess := e.local(8, "Hank")
	who := fakeoidc.User{Subject: "u-8", PreferredUsername: "hank", Groups: []string{"contractors"}}
	out := e.svc.CompleteLink(t.Context(), sess, e.startLink(t, sess, who))
	if out.Redirect != ProfilePage+"?error="+ErrorNoAccess || hank.subject != "" || !hank.password ||
		hank.liveSessions != 3 || hank.token != nil {
		t.Fatalf("a link without access = %+v, hank %+v", out, hank)
	}
	r := e.store.audited(ActionLinkRefused)
	if len(r) != 1 || r[0]["details"].(map[string]any)["reason"] != ErrorNoAccess ||
		len(r[0]["details"].(map[string]any)["groups"].([]any)) != 1 {
		t.Errorf("user.oidc_link_refused = %v", r)
	}
	// With a Role for unmatched users the same groups link.
	e.configure(t, func(in *Input) { in.UnmatchedRole = UnmatchedViewer })
	if out := e.svc.CompleteLink(t.Context(), sess, e.startLink(t, sess, who)); out.Redirect != ProfilePage ||
		hank.subject != "u-8" {
		t.Errorf("a link with a Role for unmatched users = %+v", out)
	}
}
