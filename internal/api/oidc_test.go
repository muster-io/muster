// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/keyring"
	"github.com/muster-io/muster/internal/oidc"
	"github.com/muster-io/muster/internal/proxyconf"
)

// fakeOIDC stands for internal/oidc.
type fakeOIDC struct {
	settings  oidc.Settings
	inputs    []oidc.Input
	versions  []*int64
	check     oidc.CheckResult
	start     oidc.Start
	startErr  error
	callbacks []oidc.Callback
	outcome   oidc.Outcome
	err       error
}

func (f *fakeOIDC) Get(context.Context) (oidc.Settings, error) { return f.settings, f.err }

func (f *fakeOIDC) Update(_ context.Context, _ oidc.Requester, version *int64, in oidc.Input) (oidc.Settings, error) {
	if f.err != nil {
		return oidc.Settings{}, f.err
	}
	f.inputs, f.versions = append(f.inputs, in), append(f.versions, version)
	if version != nil && *version != f.settings.Version {
		return oidc.Settings{}, oidc.ErrVersionMismatch
	}
	if in.ClientID == "bad" {
		return oidc.Settings{}, &oidc.FieldError{Pointer: "/client_id", Code: "invalid_format", Detail: "bad"}
	}
	f.settings.Version++
	f.settings.ClientID = in.ClientID
	return f.settings, nil
}

func (f *fakeOIDC) Check(context.Context) (oidc.CheckResult, error) { return f.check, f.err }

func (f *fakeOIDC) SignInOptions(context.Context) (bool, string, error) {
	return f.settings.Enabled, f.settings.ButtonName(), f.err
}

func (f *fakeOIDC) StartSignIn(_ context.Context, returnTo string) (oidc.Start, error) {
	f.callbacks = append(f.callbacks, oidc.Callback{State: "start:" + returnTo})
	return f.start, f.startErr
}

func (f *fakeOIDC) CompleteSignIn(_ context.Context, cb oidc.Callback) oidc.Outcome {
	f.callbacks = append(f.callbacks, cb)
	return f.outcome
}

// oidcSessions grants the Admin the OIDC Permissions, which the shared test Roles leave out.
type oidcSessions struct{ *fakeSessions }

func (s oidcSessions) Permissions(sess auth.Session) []auth.Permission {
	perms := s.fakeSessions.Permissions(sess)
	if sess.State == auth.StateActive && sess.User.Role == auth.RoleAdmin {
		perms = append(perms, "oidc:read", "oidc:write")
	}
	return perms
}

func newOIDCAPI(t *testing.T) (*testAPI, *fakeOIDC) {
	t.Helper()
	x := newTestAPI(t)
	expires := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	updated := t0
	f := &fakeOIDC{settings: oidc.Settings{
		Configured: true, Enabled: true, DisplayName: "Dev IdP", IssuerURL: "http://127.0.0.1:18090", ClientID: "muster",
		ClientSecret: keyring.SecretStatus{Set: true, UpdatedAt: &updated}, ClientSecretExpiresOn: &expires,
		Scopes: []string{"profile"}, GroupsClaim: "groups", UnmatchedRole: "none", SyncRole: true,
		GroupMappings: []oidc.GroupMapping{{Group: "oncall", Role: "responder"}},
		Proxy:         proxyconf.Config{Enabled: true, Type: "socks5", Address: "127.0.0.1:18092"},
		Warnings: []oidc.Warning{{Kind: oidc.WarningSecretExpiring, ExpiresOn: &expires},
			{Kind: oidc.WarningLastAdminKept, Role: "responder", User: &oidc.UserRef{PublicID: "SRAAAAAAAAAAAA",
				Name: "ada", Login: "ada"}}},
		Version: 3, UpdatedAt: &updated,
	}}
	x.srv.oidc = f
	x.srv.sessions = oidcSessions{x.sessions}
	return x, f
}

// TestSignInOptionsWithOIDC is C-03.FR-24 and AC-18: public, the OIDC button with its name, nothing about the version.
func TestSignInOptionsWithOIDC(t *testing.T) {
	x, f := newOIDCAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/sign-in-options", "")
	if a.status != http.StatusOK || string(a.body) != `{"oidc":{"display_name":"Dev IdP","enabled":true}}`+"\n" {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	f.settings.Enabled = false
	if a = x.call(t, http.MethodGet, "/api/v1/sign-in-options", ""); string(a.body) != `{"oidc":{"enabled":false}}`+"\n" {
		t.Errorf("OIDC off = %s", a.body)
	}
	f.err = errors.New("db down")
	if a = x.call(t, http.MethodGet, "/api/v1/sign-in-options", ""); a.status != http.StatusInternalServerError {
		t.Errorf("a failed read = %d", a.status)
	}
	x.srv.oidc = nil
	if a = x.call(t, http.MethodGet, "/api/v1/sign-in-options", ""); string(a.body) != `{"oidc":{"enabled":false}}`+"\n" {
		t.Errorf("without OIDC = %s", a.body)
	}
}

// TestGetOidcSettings is C-03.FR-5, FR-21 and AC-6: the secret only as its status, with the ETag and the warnings.
func TestGetOidcSettings(t *testing.T) {
	x, _ := newOIDCAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/oidc-settings", "", "Cookie", adminCookie)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"3"` {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	m := a.json(t)
	status := m["client_secret_status"].(map[string]any)
	if status["set"] != true || status["updated_at"] == nil || strings.Contains(string(a.body), "client_secret\"") {
		t.Errorf("client_secret_status = %v", status)
	}
	proxy := m["proxy"].(map[string]any)
	if proxy["type"] != "socks5" || proxy["password_status"].(map[string]any)["set"] != false {
		t.Errorf("proxy = %v", proxy)
	}
	w := m["warnings"].([]any)
	if len(w) != 2 || w[0].(map[string]any)["expires_on"] != "2026-10-16" ||
		w[1].(map[string]any)["user"].(map[string]any)["name"] != "ada" || w[1].(map[string]any)["role"] != "responder" {
		t.Errorf("warnings = %v", w)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/oidc-settings", "", "Cookie", viewerCookie); a.status != http.StatusForbidden {
		t.Errorf("a Viewer = %d", a.status)
	}
}

// TestUpdateOidcSettings: If-Match, the write-only secret and proxy password, validation and version mismatch.
func TestUpdateOidcSettings(t *testing.T) {
	x, f := newOIDCAPI(t)
	read := x.call(t, http.MethodGet, "/api/v1/oidc-settings", "", "Cookie", adminCookie).body
	var m map[string]any
	_ = json.Unmarshal(read, &m)
	for _, k := range []string{"etag", "client_secret_status", "warnings", "updated_at"} {
		delete(m, k)
	}
	m["client_secret"] = "s3cr3t-new"
	m["proxy"] = map[string]any{"enabled": true, "type": "http", "address": "127.0.0.1:18091", "username": nil,
		"password": nil}
	body, _ := json.Marshal(m)
	a := x.mutate(t, adminCookie, http.MethodPut, "/api/v1/oidc-settings", string(body), "If-Match", `"3"`)
	if a.status != http.StatusOK || a.header.Get("ETag") != `"4"` || strings.Contains(string(a.body), "s3cr3t") {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	in := f.inputs[0]
	if *f.versions[0] != 3 || !in.ClientSecret.Given || in.ClientSecret.Value != "s3cr3t-new" || !in.Proxy.Password.Null ||
		!in.Proxy.UsernameSet || in.Proxy.Username != nil || *in.Proxy.Type != "http" || !in.ExpiresOnSet ||
		in.ClientSecretExpiresOn.Format(time.DateOnly) != "2026-10-16" || in.GroupMappings[0].Role != "responder" ||
		*in.DisplayName != "Dev IdP" {
		t.Errorf("input = %+v", in)
	}
	delete(m, "client_secret")
	m["proxy"] = map[string]any{"enabled": false, "password": "pw", "username": "u"}
	delete(m, "client_secret_expires_on")
	body, _ = json.Marshal(m)
	if a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/oidc-settings", string(body), "If-Match", `"4"`); a.status != http.StatusOK {
		t.Fatalf("second = %d %s", a.status, a.body)
	}
	in = f.inputs[1]
	if in.ClientSecret.Given || in.ExpiresOnSet || in.Proxy.Password.Value != "pw" || *in.Proxy.Username != "u" {
		t.Errorf("omitted fields = %+v", in)
	}
	if a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/oidc-settings", string(body), "If-Match", `"4"`); a.status != http.StatusPreconditionFailed {
		t.Errorf("a stale ETag = %d", a.status)
	}
	if a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/oidc-settings", string(body)); a.status != http.StatusPreconditionRequired {
		t.Errorf("without If-Match = %d", a.status)
	}
	m["client_id"] = "bad"
	body, _ = json.Marshal(m)
	a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/oidc-settings", string(body), "If-Match", "*")
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"/client_id"`) {
		t.Errorf("a field error = %d %s", a.status, a.body)
	}
	if a = x.mutate(t, viewerCookie, http.MethodPut, "/api/v1/oidc-settings", string(body), "If-Match", "*"); a.status != http.StatusForbidden {
		t.Errorf("a Viewer = %d", a.status)
	}
	f.err = errors.New("db down")
	if a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/oidc-settings", string(body), "If-Match", "*"); a.status != http.StatusInternalServerError {
		t.Errorf("a failed update = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/oidc-settings", "", "Cookie", adminCookie); a.status != http.StatusInternalServerError {
		t.Errorf("a failed read = %d", a.status)
	}
}

// TestCheckOidcSettings is C-03.FR-8 and AC-8: the result names the path and, on failure, the masked error.
func TestCheckOidcSettings(t *testing.T) {
	x, f := newOIDCAPI(t)
	f.check = oidc.CheckResult{OK: true, ViaProxy: true, Latency: 12 * time.Millisecond,
		Discovery: oidc.Discovery{Issuer: "http://i", AuthorizationEndpoint: "http://i/a", TokenEndpoint: "http://i/t",
			JWKSURI: "http://i/k"}}
	a := x.mutate(t, adminCookie, http.MethodPost, "/api/v1/oidc-settings/checks", "")
	m := a.json(t)
	if a.status != http.StatusOK || m["ok"] != true || m["via"] != "proxy" || m["latency_ms"] != 12.0 ||
		m["token_endpoint"] != "http://i/t" || m["error"] != nil {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	f.check = oidc.CheckResult{Error: "blocked by the outbound address policy: proxy 169.254.169.254 is link-local (always blocked)"}
	m = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/oidc-settings/checks", "").json(t)
	if m["ok"] != false || m["via"] != "direct" || !strings.Contains(m["error"].(string), "link-local") {
		t.Errorf("a failed check = %v", m)
	}
	f.err = errors.New("db down")
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/oidc-settings/checks", ""); a.status != http.StatusInternalServerError {
		t.Errorf("a failed check = %d", a.status)
	}
}

// TestOidcSignInRedirects is C-03.FR-25 and AC-16: the start redirects with the binding cookie, the callback hands the
// cookie to the domain and redirects with the session cookie; a hostile return_to reaches the domain unrefused.
func TestOidcSignInRedirects(t *testing.T) {
	x, f := newOIDCAPI(t)
	f.start = oidc.Start{URL: "http://127.0.0.1:18090/authorize?state=st", State: "st"}
	a := x.call(t, http.MethodGet, "/api/v1/sessions/oidc/start?return_to=//evil.example", "")
	if a.status != http.StatusFound || a.header.Get("Location") != f.start.URL || f.callbacks[0].State != "start://evil.example" {
		t.Fatalf("start = %d %v %v", a.status, a.header, f.callbacks)
	}
	c := (&http.Response{Header: a.header}).Cookies()
	if len(c) != 1 || c[0].Name != oidcStateCookie || c[0].Value != "st" || !c[0].HttpOnly || !c[0].Secure ||
		c[0].SameSite != http.SameSiteLaxMode || c[0].Path != "/api/v1/" {
		t.Errorf("binding cookie = %+v", c)
	}

	f.outcome = oidc.Outcome{Session: &auth.Session{}, Redirect: "/profile"}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/sessions/oidc/callback?code=c&state=st", nil)
	r.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: "st", Secure: true, HttpOnly: true}) //nolint:gosec // G124: a request cookie carries no attributes
	w := httptest.NewRecorder()
	x.srv.ServeHTTP(w, r)
	got := f.callbacks[len(f.callbacks)-1]
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/profile" || got.CookieState != "st" ||
		got.Code != "c" || got.State != "st" {
		t.Fatalf("callback = %d %v %+v", w.Code, w.Header(), got)
	}
	names := map[string]int{}
	for _, ck := range w.Result().Cookies() {
		names[ck.Name] = ck.MaxAge
	}
	if _, ok := names[auth.CookieName]; names[oidcStateCookie] != -1 || !ok {
		t.Errorf("cookies = %v", names)
	}
	x.validate(t, http.MethodGet, "/api/v1/sessions/oidc/callback?code=c&state=st",
		answer{status: w.Code, header: w.Header(), body: w.Body.Bytes()})

	f.outcome = oidc.Outcome{Redirect: "/sign-in?error=no_access", Error: "no_access"}
	a = x.call(t, http.MethodGet, "/api/v1/sessions/oidc/callback?error=access_denied", "")
	if a.status != http.StatusFound || a.header.Get("Location") != "/sign-in?error=no_access" ||
		f.callbacks[len(f.callbacks)-1].Error != "access_denied" {
		t.Errorf("a refusal = %d %v", a.status, a.header)
	}

	f.startErr = &oidc.BackChannelError{Step: "discovery"}
	if a = x.call(t, http.MethodGet, "/api/v1/sessions/oidc/start", ""); a.status != http.StatusFound ||
		a.header.Get("Location") != "/sign-in?error=idp_error" || !strings.Contains(x.log.String(), "oidc_sign_in_failed") {
		t.Errorf("a failed discovery = %d %v", a.status, a.header)
	}
	f.startErr = oidc.ErrTooManyRequests
	if a = x.call(t, http.MethodGet, "/api/v1/sessions/oidc/start", ""); a.status != http.StatusTooManyRequests ||
		a.header.Get("Retry-After") != "60" {
		t.Errorf("too many starts = %d %v", a.status, a.header)
	}
	f.startErr = oidc.ErrNotEnabled
	if a = x.call(t, http.MethodGet, "/api/v1/sessions/oidc/start", ""); a.status != http.StatusConflict ||
		a.code(t) != "oidc_not_enabled" {
		t.Errorf("OIDC off = %d %s", a.status, a.body)
	}
	f.startErr = errors.New("db down")
	if a = x.call(t, http.MethodGet, "/api/v1/sessions/oidc/start", ""); a.status != http.StatusInternalServerError {
		t.Errorf("a failed start = %d", a.status)
	}
	x.srv.oidc = nil
	if a = x.call(t, http.MethodGet, "/api/v1/sessions/oidc/start", ""); a.status != http.StatusConflict {
		t.Errorf("without OIDC = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/sessions/oidc/callback", ""); a.header.Get("Location") !=
		"/sign-in?error=oidc_disabled" {
		t.Errorf("a callback without OIDC = %v", a.header)
	}
}
