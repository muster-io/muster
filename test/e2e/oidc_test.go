// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"encoding/base32"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/devmode"
	"github.com/muster-io/muster/internal/totp"
)

// The bootstrap Admin of development mode.
const (
	devAdmin         = "admin@example.org"
	devAdminPassword = "muster-dev-password"
)

// noFollow is a client that hands every redirect back, as the steps of the OIDC flows are checked one by one.
var noFollow = &http.Client{Timeout: 20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// agent is one browser: it keeps the cookies Muster sets and sends the CSRF token of its session.
type agent struct {
	t       *testing.T
	base    string
	cookies map[string]string
	csrf    string
}

func newAgent(t *testing.T, base string) *agent {
	return &agent{t: t, base: base, cookies: map[string]string{}}
}

// do sends a request to Muster, or to another server for an absolute URL, and keeps the cookies Muster sets.
func (a *agent) do(method, target, body string) answer {
	a.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = a.base + target
	}
	req, err := http.NewRequestWithContext(a.t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	if strings.HasPrefix(target, a.base) {
		for name, value := range a.cookies {
			req.AddCookie(&http.Cookie{Name: name, Value: value}) //nolint:gosec // G124: a request cookie carries no attributes
		}
		if a.csrf != "" {
			req.Header.Set("X-CSRF-Token", a.csrf)
		}
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.HasPrefix(target, a.base) {
		for _, c := range resp.Cookies() {
			if c.MaxAge < 0 {
				delete(a.cookies, c.Name)
			} else {
				a.cookies[c.Name] = c.Value
			}
		}
	}
	return answer{status: resp.StatusCode, header: resp.Header, body: b}
}

func (a *agent) json(method, target, body string, want int) map[string]any {
	a.t.Helper()
	got := a.do(method, target, body)
	if got.status != want {
		a.t.Fatalf("%s %s = %d %s, want %d", method, target, got.status, got.body, want)
	}
	var m map[string]any
	if len(got.body) > 0 {
		decode(a.t, got, &m)
	}
	return m
}

// signIn signs in locally and keeps the session and its CSRF token.
func (a *agent) signIn(login, password string) answer {
	a.t.Helper()
	body, _ := json.Marshal(map[string]string{"login": login, "password": password})
	got := a.do(http.MethodPost, "/api/v1/sessions", string(body))
	if got.status == http.StatusCreated {
		var s struct {
			CSRF string `json:"csrf_token"`
		}
		decode(a.t, got, &s)
		a.csrf = s.CSRF
	}
	return got
}

// follow sends the browser from Muster through the fake IdP and back to the callback on Muster, and returns where the
// callback sends it.
func (a *agent) follow(start answer) string {
	a.t.Helper()
	if start.status != http.StatusFound {
		a.t.Fatalf("the start answered %d %s", start.status, start.body)
	}
	return a.back(start.header.Get("Location"))
}

// back opens the authorization URL at the fake IdP and its redirect to Muster's callback.
func (a *agent) back(authorizationURL string) string {
	a.t.Helper()
	idp := a.do(http.MethodGet, authorizationURL, "")
	cb, err := url.Parse(idp.header.Get("Location"))
	if idp.status != http.StatusFound || err != nil {
		a.t.Fatalf("the fake IdP answered %d %s", idp.status, idp.header.Get("Location"))
	}
	done := a.do(http.MethodGet, cb.RequestURI(), "")
	if done.status != http.StatusFound {
		a.t.Fatalf("the callback answered %d %s", done.status, done.body)
	}
	return done.header.Get("Location")
}

// oidcSignIn signs the person the fake IdP approves next in through OIDC and returns where Muster sends the browser.
func (a *agent) oidcSignIn() string {
	a.t.Helper()
	to := a.follow(a.do(http.MethodGet, "/api/v1/sessions/oidc/start", ""))
	if s := a.do(http.MethodGet, "/api/v1/sessions/current", ""); s.status == http.StatusOK {
		var cur struct {
			CSRF string `json:"csrf_token"`
		}
		decode(a.t, s, &cur)
		a.csrf = cur.CSRF
	}
	return to
}

func nextUser(t *testing.T, sub, login string, groups ...string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"sub": sub, "preferred_username": login, "groups": groups})
	if a := call(t, http.MethodPost, "http://"+devmode.OIDCAddr+"/_fake/next-user", string(body)); a.status !=
		http.StatusNoContent {
		t.Fatalf("next-user = %d %s", a.status, a.body)
	}
}

// localUser creates a local user through the Admin and sets its password through the setup link.
func localUser(t *testing.T, admin *agent, login, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": login, "login": login, "role": "responder"})
	created := admin.json(http.MethodPost, "/api/v1/users", string(body), http.StatusCreated)
	link := created["password_setup_link"].(map[string]any)["url"].(string)
	token := link[strings.Index(link, "#token=")+len("#token="):]
	setup, _ := json.Marshal(map[string]string{"token": token, "password": password})
	admin.json(http.MethodPost, "/api/v1/password-setups", string(setup), http.StatusNoContent)
	return created["user"].(map[string]any)["id"].(string)
}

func userField(t *testing.T, admin *agent, q, field string) any {
	t.Helper()
	list := admin.json(http.MethodGet, "/api/v1/users?q="+q, "", http.StatusOK)
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("users?q=%s has %d items", q, len(items))
	}
	return items[0].(map[string]any)[field]
}

func (h *Harness) count(t *testing.T, sql string) int64 {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	var n int64
	if err := conn.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// TestOIDCLinkingAndConversion is C-03.AC-20 and C-03.AC-21 against `muster dev`: Alice links OIDC from her session —
// her other session ends, her password no longer signs in, she signs in through OIDC into the same account with her
// TOTP asked, and the Users list shows the method oidc; Bob's link of her identity is identity_linked_elsewhere; a link
// callback replayed in another session is invalid_request. The Admin's conversion removes her identity, ends her
// sessions and returns a setup link, and her next OIDC sign-in is login_taken.
func TestOIDCLinkingAndConversion(t *testing.T) {
	h := Start(t, DevProcess)
	base := h.Replicas[0].App
	admin := newAgent(t, base)
	if a := admin.signIn(devAdmin, devAdminPassword); a.status != http.StatusCreated {
		t.Fatalf("the Admin's sign-in = %d %s", a.status, a.body)
	}
	aliceID := localUser(t, admin, "alice", "alice-password-1")
	alice, aliceOther := newAgent(t, base), newAgent(t, base)
	for _, a := range []*agent{alice, aliceOther} {
		if got := a.signIn("alice", "alice-password-1"); got.status != http.StatusCreated {
			t.Fatalf("alice's sign-in = %d %s", got.status, got.body)
		}
	}
	// Alice enrols TOTP, which the link keeps.
	enrol := alice.json(http.MethodPost, "/api/v1/me/totp", "", http.StatusCreated)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrol["secret"].(string))
	if err != nil {
		t.Fatal(err)
	}
	code := totp.Code(seed, time.Now().Unix()/30)
	alice.json(http.MethodPost, "/api/v1/me/totp/confirmation", `{"code":"`+code+`"}`, http.StatusOK)

	t.Run("link", func(t *testing.T) {
		nextUser(t, "u-3", "alice", "oncall")
		start := alice.json(http.MethodPost, "/api/v1/me/oidc-identity", "", http.StatusCreated)
		if to := alice.back(start["authorization_url"].(string)); to != "/profile" {
			t.Fatalf("the link ended at %s", to)
		}
		if a := alice.do(http.MethodGet, "/api/v1/sessions/current", ""); a.status != http.StatusOK ||
			!strings.Contains(string(a.body), `"method":"oidc"`) {
			t.Errorf("the session that linked = %d %s", a.status, a.body)
		}
		if a := aliceOther.do(http.MethodGet, "/api/v1/me", ""); a.status != http.StatusUnauthorized {
			t.Errorf("alice's other session = %d", a.status)
		}
		if a := newAgent(t, base).signIn("alice", "alice-password-1"); a.status != http.StatusUnauthorized {
			t.Errorf("alice's password after the link = %d", a.status)
		}
		oidcAlice := newAgent(t, base)
		if to := oidcAlice.oidcSignIn(); to != "/" {
			t.Fatalf("alice's OIDC sign-in ended at %s", to)
		}
		cur := oidcAlice.json(http.MethodGet, "/api/v1/sessions/current", "", http.StatusOK)
		if cur["state"] != "totp_required" || cur["user"].(map[string]any)["id"] != aliceID {
			t.Errorf("alice's OIDC session = %v", cur)
		}
		if m := userField(t, admin, "alice", "sign_in_method"); m != "oidc" {
			t.Errorf("users shows alice's method %v", m)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		localUser(t, admin, "bob", "bob-password-123")
		bob := newAgent(t, base)
		bob.signIn("bob", "bob-password-123")
		nextUser(t, "u-3", "alice", "oncall")
		start := bob.json(http.MethodPost, "/api/v1/me/oidc-identity", "", http.StatusCreated)
		if to := bob.back(start["authorization_url"].(string)); to != "/profile?error=identity_linked_elsewhere" {
			t.Errorf("Bob's link of Alice's identity ended at %s", to)
		}
		localUser(t, admin, "carol", "carol-password-1")
		carol := newAgent(t, base)
		carol.signIn("carol", "carol-password-1")
		nextUser(t, "u-4", "carol")
		start = carol.json(http.MethodPost, "/api/v1/me/oidc-identity", "", http.StatusCreated)
		idp := carol.do(http.MethodGet, start["authorization_url"].(string), "")
		cb, _ := url.Parse(idp.header.Get("Location"))
		if a := bob.do(http.MethodGet, cb.RequestURI(), ""); a.header.Get("Location") != "/profile?error=invalid_request" {
			t.Errorf("the callback replayed in Bob's session went to %s", a.header.Get("Location"))
		}
		if a := carol.do(http.MethodGet, cb.RequestURI(), ""); a.header.Get("Location") != "/profile" {
			t.Errorf("the callback in Carol's session went to %s", a.header.Get("Location"))
		}
	})

	t.Run("convert", func(t *testing.T) {
		got := admin.json(http.MethodPost, "/api/v1/users/"+aliceID+"/convert-to-local", "", http.StatusOK)
		if got["user"].(map[string]any)["sign_in_method"] != "local" ||
			!strings.Contains(got["password_setup_link"].(map[string]any)["url"].(string), "#token=") {
			t.Fatalf("convert-to-local = %v", got)
		}
		if a := alice.do(http.MethodGet, "/api/v1/me", ""); a.status != http.StatusUnauthorized {
			t.Errorf("alice's session after the conversion = %d", a.status)
		}
		nextUser(t, "u-3", "alice", "oncall")
		if to := newAgent(t, base).oidcSignIn(); to != "/sign-in?error=login_taken" {
			t.Errorf("alice's OIDC sign-in after the conversion ended at %s", to)
		}
		if n := h.count(t, `SELECT count(*) FROM oidc_checks c JOIN users u ON u.id = c.user_id
			WHERE u.login = 'alice'`); n != 0 {
			t.Errorf("alice has %d re-checks after the conversion", n)
		}
	})
}

// TestOIDCFallbackLifetime is C-03.AC-24 against `muster dev`: without offline_access an OIDC session ends
// auth.oidc_fallback_session_lifetime (12 h) after sign-in and no re-check exists for the user; with it, the session
// has auth.session_lifetime (7 days) and the user is re-checked.
func TestOIDCFallbackLifetime(t *testing.T) {
	h := Start(t, DevProcess)
	base := h.Replicas[0].App
	config := "http://" + devmode.OIDCAddr + "/_fake/config"
	if a := call(t, http.MethodPost, config, `{"grant_offline_access":false}`); a.status != http.StatusNoContent {
		t.Fatalf("config = %d", a.status)
	}
	nextUser(t, "u-6", "nora", "oncall")
	nora := newAgent(t, base)
	before := time.Now()
	if to := nora.oidcSignIn(); to != "/" {
		t.Fatalf("nora's sign-in ended at %s", to)
	}
	cur := nora.json(http.MethodGet, "/api/v1/sessions/current", "", http.StatusOK)
	expires, _ := time.Parse(time.RFC3339, cur["expires_at"].(string))
	if d := expires.Sub(before); d < 12*time.Hour-time.Minute || d > 12*time.Hour+time.Minute {
		t.Errorf("without offline_access the session ends %v after sign-in, want 12h", d)
	}
	if n := h.count(t, `SELECT count(*) FROM oidc_checks`); n != 0 {
		t.Errorf("%d re-checks without an offline token", n)
	}
	admin := newAgent(t, base)
	admin.signIn(devAdmin, devAdminPassword)
	if v := userField(t, admin, "nora", "oidc_offline_access"); v != false {
		t.Errorf("nora's oidc_offline_access = %v", v)
	}

	call(t, http.MethodPost, config, `{"grant_offline_access":true}`)
	if to := nora.oidcSignIn(); to != "/" {
		t.Fatalf("nora's second sign-in ended at %s", to)
	}
	cur = nora.json(http.MethodGet, "/api/v1/sessions/current", "", http.StatusOK)
	expires, _ = time.Parse(time.RFC3339, cur["expires_at"].(string))
	if d := time.Until(expires); d < 7*24*time.Hour-2*time.Minute {
		t.Errorf("with offline_access the session ends in %v, want 7 days", d)
	}
	if n := h.count(t, `SELECT count(*) FROM oidc_checks`); n != 1 {
		t.Errorf("%d re-checks with an offline token", n)
	}
	if v := userField(t, admin, "nora", "oidc_offline_access"); v != true {
		t.Errorf("nora's oidc_offline_access = %v", v)
	}
}
