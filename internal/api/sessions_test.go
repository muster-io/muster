// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/users"
)

// TestSignInOptions is C-03.FR-24: public, and it says only whether OIDC is enabled.
func TestSignInOptions(t *testing.T) {
	x := newTestAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/sign-in-options", "")
	if a.status != http.StatusOK || strings.TrimSpace(string(a.body)) != `{"oidc":{"enabled":false}}` {
		t.Errorf("= %d %s", a.status, a.body)
	}
	if a.header.Get("Cache-Control") != "no-store" {
		t.Error("API answers are not kept by caches")
	}
}

// TestCreateSession is C-03.FR-3 and FR-24: a sign-in answers 201 with an active Session and sets the cookie
// HttpOnly, Secure and SameSite=Lax; refusals are 401 invalid_credentials, and throttled attempts 429 with
// Retry-After.
func TestCreateSession(t *testing.T) {
	x := newTestAPI(t)
	var got auth.SignInRequest
	x.sessions.signIn = func(req auth.SignInRequest) (auth.Session, error) {
		got = req
		if req.Password != "right password" {
			return auth.Session{}, auth.ErrInvalidCredentials
		}
		return x.sessions.sessions["admin-cookie"], nil
	}
	a := x.call(t, http.MethodPost, "/api/v1/sessions", `{"login":"ADMIN@example.org","password":"right password"}`,
		"User-Agent", "agent/1", "X-Forwarded-For", "198.51.100.7")
	if a.status != http.StatusCreated {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	body := a.json(t)
	if body["state"] != "active" || body["method"] != "local" || body["csrf_token"] != "csrf-admin-cookie" ||
		body["user"].(map[string]any)["login"] != "admin@example.org" {
		t.Errorf("session = %s", a.body)
	}
	cookie := a.header.Get("Set-Cookie")
	for _, attr := range []string{auth.CookieName + "=", "Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(cookie, attr) {
			t.Errorf("Set-Cookie %q lacks %s", cookie, attr)
		}
	}
	if got.Login != "ADMIN@example.org" || got.UserAgent != "agent/1" ||
		got.Address != netip.MustParseAddr("192.0.2.1") {
		t.Errorf("sign-in request = %+v (the peer is not a trusted proxy, so X-Forwarded-For is ignored)", got)
	}

	wrong := x.call(t, http.MethodPost, "/api/v1/sessions", `{"login":"admin@example.org","password":"wrong"}`)
	unknown := x.call(t, http.MethodPost, "/api/v1/sessions", `{"login":"nobody@example.org","password":"wrong"}`)
	if wrong.status != http.StatusUnauthorized || wrong.code(t) != codeInvalidCredentials ||
		!bytes.Equal(wrong.body, unknown.body) || wrong.header.Get("Set-Cookie") != "" {
		t.Errorf("refusals = %d %s / %s", wrong.status, wrong.body, unknown.body)
	}

	x.sessions.signIn = func(auth.SignInRequest) (auth.Session, error) {
		return auth.Session{}, &auth.ThrottledError{RetryAfter: 1500 * time.Millisecond}
	}
	a = x.call(t, http.MethodPost, "/api/v1/sessions", `{"login":"a","password":"b"}`)
	if a.status != http.StatusTooManyRequests || a.header.Get("Retry-After") != "2" ||
		a.json(t)["retry_after_seconds"] != 2.0 || a.json(t)["type"] != problemBase+"rate-limited" {
		t.Errorf("throttled = %d %v %s", a.status, a.header, a.body)
	}

	x.sessions.signIn = func(auth.SignInRequest) (auth.Session, error) {
		return x.sessions.sessions["admin-cookie"], nil
	}
	x.users.err = errors.New("db down")
	if a := x.call(t, http.MethodPost, "/api/v1/sessions", `{"login":"a","password":"b"}`); a.status != 500 {
		t.Errorf("a failed read of the user = %d", a.status)
	}
	x.users.err = nil
	x.sessions.err = errors.New("no key")
	if a := x.call(t, http.MethodPost, "/api/v1/sessions", `{"login":"a","password":"b"}`); a.status != 500 {
		t.Errorf("a failed CSRF token = %d", a.status)
	}
}

// TestCurrentSession: the session with its CSRF token, and signing out, which clears the cookie.
func TestCurrentSession(t *testing.T) {
	x := newTestAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/sessions/current", "", "Cookie", "viewer-cookie")
	if a.status != http.StatusOK || a.json(t)["csrf_token"] != "csrf-viewer-cookie" ||
		len(a.json(t)["permissions"].([]any)) != 2 {
		t.Errorf("= %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodDelete, "/api/v1/sessions/current", "", "Cookie", "viewer-cookie", auth.CSRFHeader,
		"csrf-viewer-cookie")
	if a.status != http.StatusNoContent || !strings.Contains(a.header.Get("Set-Cookie"), "Max-Age=0") ||
		len(x.sessions.signedOut) != 1 {
		t.Errorf("sign out = %d %v", a.status, a.header)
	}
	x.sessions.err = errors.New("db down")
	if a := x.call(t, http.MethodDelete, "/api/v1/sessions/current", "", "Cookie", "viewer-cookie",
		auth.CSRFHeader, "csrf-viewer-cookie"); a.status != http.StatusInternalServerError {
		t.Errorf("a failed sign-out = %d", a.status)
	}
}

// TestProfile is C-03.FR-12: getMe returns the user and the effective Permissions, updateMe changes the name, the
// time zone and the language — an omitted field keeps its value and null clears it.
func TestProfile(t *testing.T) {
	x := newTestAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/me", "", "Cookie", "viewer-cookie")
	if a.status != http.StatusOK || len(a.json(t)["permissions"].([]any)) != 2 ||
		a.json(t)["user"].(map[string]any)["id"] != "SRBBBBBBBBBBBB" {
		t.Fatalf("getMe = %d %s", a.status, a.body)
	}
	write := []string{"Cookie", "viewer-cookie", auth.CSRFHeader, "csrf-viewer-cookie"}
	a = x.call(t, http.MethodPut, "/api/v1/me", `{"name":"Vera","time_zone":"Europe/Berlin","language":"ru"}`, write...)
	u := a.json(t)["user"].(map[string]any)
	if a.status != http.StatusOK || u["name"] != "Vera" || u["time_zone"] != "Europe/Berlin" || u["language"] != "ru" {
		t.Fatalf("updateMe = %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodPut, "/api/v1/me", `{"name":"Vera"}`, write...)
	if u := a.json(t)["user"].(map[string]any); u["time_zone"] != "Europe/Berlin" || u["language"] != "ru" {
		t.Errorf("omitted fields changed: %s", a.body)
	}
	a = x.call(t, http.MethodPut, "/api/v1/me", `{"name":"Vera","time_zone":null,"language":null}`, write...)
	if u := a.json(t)["user"].(map[string]any); u["time_zone"] != nil || u["language"] != nil {
		t.Errorf("null did not clear: %s", a.body)
	}
	a = x.call(t, http.MethodPut, "/api/v1/me", `{"name":"Vera","time_zone":"Mars/Base"}`, write...)
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"pointer":"/time_zone"`) {
		t.Errorf("an unknown time zone = %d %s", a.status, a.body)
	}
	x.users.err = users.ErrNotFound
	if a := x.call(t, http.MethodGet, "/api/v1/me", "", "Cookie", "viewer-cookie"); a.status != http.StatusUnauthorized {
		t.Errorf("a vanished user = %d", a.status)
	}
	if a := x.call(t, http.MethodPut, "/api/v1/me", `{"name":"V"}`, write...); a.status != http.StatusUnauthorized {
		t.Errorf("updating a vanished user = %d", a.status)
	}
	x.users.err = nil
	delete(x.users.users, 2)
	if a := x.call(t, http.MethodPut, "/api/v1/me", `{"name":"V"}`, write...); a.status != http.StatusUnauthorized {
		t.Errorf("a user gone during the update = %d", a.status)
	}
}

// TestChangePassword: 204 on success; a short new password is 422 too_short, a wrong current one 401
// invalid_credentials, an OIDC account 409 local_user_only.
func TestChangePassword(t *testing.T) {
	x := newTestAPI(t)
	write := []string{"Cookie", "admin-cookie", auth.CSRFHeader, "csrf-admin-cookie"}
	body := `{"current_password":"old password","new_password":"new long password"}`
	a := x.call(t, http.MethodPut, "/api/v1/me/password", body, write...)
	if a.status != http.StatusNoContent || len(x.sessions.changes) != 1 ||
		x.sessions.changes[0].New != "new long password" || x.sessions.changes[0].Current != "old password" {
		t.Fatalf("= %d %s", a.status, a.body)
	}
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		auth.ErrPasswordTooShort:   {http.StatusUnprocessableEntity, ""},
		auth.ErrInvalidCredentials: {http.StatusUnauthorized, codeInvalidCredentials},
		auth.ErrNotLocal:           {http.StatusConflict, codeLocalUserOnly},
	} {
		x.sessions.err = err
		a := x.call(t, http.MethodPut, "/api/v1/me/password", body, write...)
		if a.status != want.status || a.code(t) != want.code || a.header.Get("Set-Cookie") != "" {
			t.Errorf("%v = %d %s", err, a.status, a.body)
		}
		if errors.Is(err, auth.ErrPasswordTooShort) && !strings.Contains(string(a.body), `"code":"too_short"`) {
			t.Errorf("too short = %s", a.body)
		}
		if bytes.Contains(a.body, []byte("new long password")) {
			t.Error("the password is echoed")
		}
	}
	a = x.call(t, http.MethodPut, "/api/v1/me/password", `{"new_password":"x"}`, write...)
	if a.status != http.StatusBadRequest || !strings.Contains(string(a.body), `"/current_password"`) {
		t.Errorf("no current password = %d %s", a.status, a.body)
	}
}

// TestMySessions is C-03.FR-9 and FR-12: the user's sessions with the current one marked, and signing out
// everywhere, which clears the cookie.
func TestMySessions(t *testing.T) {
	x := newTestAPI(t)
	x.sessions.list = []auth.SessionInfo{
		{PublicID: "SNAAAAAAAAAAA1", Method: "local", Address: netip.MustParseAddr("192.0.2.1"), UserAgent: "ua",
			CreatedAt: t0, LastUsedAt: t0, Current: true},
		{PublicID: "SNAAAAAAAAAAA9", Method: "local", CreatedAt: t0, LastUsedAt: t0},
	}
	a := x.call(t, http.MethodGet, "/api/v1/me/sessions", "", "Cookie", "admin-cookie")
	var list struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(a.body, &list)
	if a.status != http.StatusOK || len(list.Items) != 2 || list.Items[0]["current"] != true ||
		list.Items[0]["address"] != "192.0.2.1" || list.Items[1]["address"] != nil || list.Items[1]["user_agent"] != nil {
		t.Errorf("= %d %s", a.status, a.body)
	}
	a = x.call(t, http.MethodDelete, "/api/v1/me/sessions", "", "Cookie", "admin-cookie", auth.CSRFHeader,
		"csrf-admin-cookie")
	if a.status != http.StatusNoContent || !strings.Contains(a.header.Get("Set-Cookie"), "Max-Age=0") ||
		x.sessions.signedOut[0] != "all:SRAAAAAAAAAAAA" {
		t.Errorf("sign out everywhere = %d %v", a.status, x.sessions.signedOut)
	}
	x.sessions.err = errors.New("db down")
	if a := x.call(t, http.MethodGet, "/api/v1/me/sessions", "", "Cookie", "admin-cookie"); a.status != 500 {
		t.Errorf("a failed list = %d", a.status)
	}
	if a := x.call(t, http.MethodDelete, "/api/v1/me/sessions", "", "Cookie", "admin-cookie", auth.CSRFHeader,
		"csrf-admin-cookie"); a.status != 500 {
		t.Errorf("a failed sign-out everywhere = %d", a.status)
	}
}

// TestHandlersWithoutIdentity: a handler reached without an identity answers 401 instead of failing.
func TestHandlersWithoutIdentity(t *testing.T) {
	x := newTestAPI(t)
	ctx := t.Context()
	for name, call := range map[string]func() error{
		"getCurrentSession": func() error { _, err := x.srv.GetCurrentSession(ctx, struct{}{}); return err },
		"deleteCurrentSession": func() error {
			_, err := x.srv.DeleteCurrentSession(ctx, struct{}{})
			return err
		},
		"getMe":            func() error { _, err := x.srv.GetMe(ctx, struct{}{}); return err },
		"listMySessions":   func() error { _, err := x.srv.ListMySessions(ctx, struct{}{}); return err },
		"deleteMySessions": func() error { _, err := x.srv.DeleteMySessions(ctx, struct{}{}); return err },
	} {
		if err := call(); !errors.Is(err, errUnauthenticated) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
