// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/api/gen"
	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/tokens"
)

// The bearer tokens of the fake: an Admin's Personal access tokens narrowed to reading, and to users:read and
// users:write, and a Service account token with the Admin Role.
const (
	readOnlyToken = "mstr_pat_read"
	fullToken     = "mstr_pat_full"
	serviceToken  = "mstr_sat_ci"
	saPublicID    = "SAAAAAAAAAAAAA"
)

// fakeTokens stands for internal/tokens; err answers every call but Authenticate.
type fakeTokens struct {
	idents  map[string]*auth.Identity
	authErr map[string]error
	limiter *tokens.Limiter
	addrs   []netip.Addr
	list    []tokens.Token
	by      []tokens.Requester
	owners  []tokens.Owner
	held    [][]auth.Permission
	created []tokens.NewPersonal
	revoked []string
	filter  tokens.ListFilter
	page    tokens.Page
	inputs  []tokens.ServiceAccountInput
	version []*int64
	saToken []tokens.NewToken
	err     error
}

func newFakeTokens(realClock clock.Clock, x *testAPI) *fakeTokens {
	admin := x.users.users[1]
	owner := auth.Session{User: auth.Principal{ID: admin.ID, PublicID: admin.PublicID, Name: admin.Name,
		Role: admin.Role}}
	return &fakeTokens{
		idents: map[string]*auth.Identity{
			readOnlyToken: {Session: owner, Permissions: []auth.Permission{"alert-groups:read", "users:read"},
				Transport: audit.TransportAPI, Token: &auth.Token{ID: 11, PublicID: "PTAAAAAAAAAAAA", Name: "read-only"}},
			fullToken: {Session: owner, Permissions: roles.Permissions(auth.RoleAdmin), Transport: audit.TransportAPI,
				Token: &auth.Token{ID: 12, PublicID: "PTBBBBBBBBBBBB", Name: "full"}},
			serviceToken: {Permissions: roles.Permissions(auth.RoleAdmin), Transport: audit.TransportAPI,
				Token: &auth.Token{ID: 13, PublicID: "STAAAAAAAAAAAA", Name: "ci",
					ServiceAccount: &auth.Principal{ID: 3, PublicID: saPublicID, Name: "terraform", Role: "admin"}}},
		},
		authErr: map[string]error{
			"mstr_int_x":    tokens.ErrInvalidToken,
			"mstr_pat_old":  tokens.ErrInvalidToken,
			"mstr_pat_oidc": tokens.ErrOIDCRecheckRequired,
			"mstr_pat_down": errors.New("database down"),
		},
		limiter: tokens.NewLimiter(realClock),
	}
}

func (f *fakeTokens) Authenticate(_ context.Context, value string, addr netip.Addr) (*auth.Identity, error) {
	f.addrs = append(f.addrs, addr)
	if err, ok := f.authErr[value]; ok {
		return nil, err
	}
	id, ok := f.idents[value]
	if !ok {
		return nil, tokens.ErrInvalidToken
	}
	if err := f.limiter.Allow(id.Token.ID); err != nil {
		return nil, err
	}
	return id, nil
}

func (f *fakeTokens) ListPersonal(context.Context, int64) ([]tokens.Token, error) {
	return f.list, f.err
}

func (f *fakeTokens) CreatePersonal(_ context.Context, r tokens.Requester, o tokens.Owner, held []auth.Permission,
	n tokens.NewPersonal) (tokens.Created, error) {
	f.by, f.owners, f.held, f.created = append(f.by, r), append(f.owners, o), append(f.held, held),
		append(f.created, n)
	if f.err != nil {
		return tokens.Created{}, f.err
	}
	return tokens.Created{Token: tokens.Token{PublicID: "PTCCCCCCCCCCCC", Name: n.Name, ExpiresAt: n.ExpiresAt,
		CreatedAt: t0, Permissions: n.Permissions}, Value: "mstr_pat_new"}, nil
}

func (f *fakeTokens) RevokePersonal(_ context.Context, r tokens.Requester, o tokens.Owner, id string) error {
	f.by, f.owners, f.revoked = append(f.by, r), append(f.owners, o), append(f.revoked, id)
	return f.err
}

func (f *fakeTokens) ListServiceAccounts(_ context.Context, fl tokens.ListFilter) (tokens.Page, error) {
	f.filter = fl
	return f.page, f.err
}

func serviceAccount(version int64, status string) tokens.ServiceAccount {
	return tokens.ServiceAccount{ID: 3, PublicID: saPublicID, Name: "terraform", Role: "admin", Status: status,
		TokenCount: 2, CreatedAt: t0, Version: version}
}

func (f *fakeTokens) GetServiceAccount(context.Context, string) (tokens.ServiceAccount, error) {
	return serviceAccount(1, tokens.StatusActive), f.err
}

func (f *fakeTokens) CreateServiceAccount(_ context.Context, r tokens.Requester, in tokens.ServiceAccountInput) (
	tokens.ServiceAccount, error) {
	f.by, f.inputs = append(f.by, r), append(f.inputs, in)
	return serviceAccount(1, tokens.StatusActive), f.err
}

func (f *fakeTokens) UpdateServiceAccount(_ context.Context, r tokens.Requester, _ string, version *int64,
	in tokens.ServiceAccountInput) (tokens.ServiceAccount, error) {
	f.by, f.inputs, f.version = append(f.by, r), append(f.inputs, in), append(f.version, version)
	return serviceAccount(2, tokens.StatusActive), f.err
}

func (f *fakeTokens) DisableServiceAccount(_ context.Context, r tokens.Requester, _ string) (tokens.ServiceAccount,
	error) {
	f.by = append(f.by, r)
	return serviceAccount(2, tokens.StatusDisabled), f.err
}

func (f *fakeTokens) EnableServiceAccount(_ context.Context, r tokens.Requester, _ string) (tokens.ServiceAccount,
	error) {
	f.by = append(f.by, r)
	return serviceAccount(3, tokens.StatusActive), f.err
}

func (f *fakeTokens) DeleteServiceAccount(_ context.Context, r tokens.Requester, _ string, version *int64) error {
	f.by, f.version = append(f.by, r), append(f.version, version)
	return f.err
}

func (f *fakeTokens) ListServiceAccountTokens(context.Context, string) ([]tokens.Token, error) {
	return f.list, f.err
}

func (f *fakeTokens) CreateServiceAccountToken(_ context.Context, r tokens.Requester, _ string, n tokens.NewToken) (
	tokens.Created, error) {
	f.by, f.saToken = append(f.by, r), append(f.saToken, n)
	return tokens.Created{Token: tokens.Token{PublicID: "STBBBBBBBBBBBB", Name: n.Name, CreatedAt: t0},
		Value: "mstr_sat_new"}, f.err
}

func (f *fakeTokens) RevokeServiceAccountToken(_ context.Context, r tokens.Requester, _, id string) error {
	f.by, f.revoked = append(f.by, r), append(f.revoked, id)
	return f.err
}

func newTokensAPI(t *testing.T) (*testAPI, *fakeTokens, *fakeAdmin, *clock.Manual) {
	t.Helper()
	x, fa := newAdminAPI(t)
	realClock := clock.NewManual(t0)
	ft := newFakeTokens(realClock, x)
	x.srv.tokens = ft
	return x, ft, fa, realClock
}

func bearer(token string) []string {
	return []string{"Authorization", "Bearer " + token}
}

// TestBearerAuthentication is C-04.FR-4, C-04.AC-1 and C-04.AC-5 at the API: a Personal access token acts with its
// narrowed Permissions and needs no CSRF token, with the Transport api and the token in the Audit log actor; the same
// Admin's session may do what the read-only token may not. An Integration token, an unknown or revoked one and a
// failed lookup answer 401 or 500 without the token in the answer; a bearer token wins over a cookie.
func TestBearerAuthentication(t *testing.T) {
	x, ft, fa, _ := newTokensAPI(t)
	a := x.call(t, http.MethodGet, "/api/v1/me", "", bearer(readOnlyToken)...)
	perms, _ := a.json(t)["permissions"].([]any)
	if a.status != http.StatusOK || len(perms) != 2 || ft.addrs[0] != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("GET /me = %d %s", a.status, a.body)
	}
	body := `{"name":"Eve","login":"eve","role":"viewer"}`
	if a = x.call(t, http.MethodPost, "/api/v1/users", body, bearer(readOnlyToken)...); a.status != http.StatusForbidden {
		t.Errorf("the read-only token creates a user: %d %s", a.status, a.body)
	}
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users", body); a.status != http.StatusCreated {
		t.Errorf("the Admin's session = %d %s", a.status, a.body)
	}
	if a = x.call(t, http.MethodPost, "/api/v1/users", body, bearer(fullToken)...); a.status != http.StatusCreated {
		t.Fatalf("the full token = %d %s", a.status, a.body)
	}
	by := fa.by[len(fa.by)-1]
	if by.Actor.Kind != audit.ActorUser || by.Actor.ID != 1 || by.Actor.TokenName != "full" || by.Actor.TokenID != 12 ||
		by.Transport != audit.TransportAPI {
		t.Errorf("requester %+v", by)
	}
	for value, want := range map[string]string{"mstr_int_x": codeInvalidCredentials,
		"mstr_pat_old": codeInvalidCredentials, "mstr_pat_unknown": codeInvalidCredentials,
		"mstr_pat_oidc": codeOIDCRecheckRequired} {
		a = x.call(t, http.MethodGet, "/api/v1/me", "", append(bearer(value), "Cookie", adminCookie)...)
		if a.status != http.StatusUnauthorized || a.code(t) != want || strings.Contains(string(a.body), value) {
			t.Errorf("%s = %d %s", value, a.status, a.body)
		}
	}
	a = x.call(t, http.MethodGet, "/api/v1/me", "", bearer("mstr_pat_oidc")...)
	if a.json(t)["detail"] != "Sign in through OIDC to make your tokens work again." {
		t.Errorf("oidc_recheck_required = %s", a.body)
	}
	a = x.call(t, http.MethodGet, "/api/v1/me", "", bearer("mstr_pat_down")...)
	if a.status != http.StatusInternalServerError || strings.Contains(x.log.String(), "mstr_pat_down") {
		t.Errorf("a failed lookup = %d %s", a.status, a.body)
	}
	// The scheme is case-insensitive; another scheme falls back to the session.
	if a = x.call(t, http.MethodGet, "/api/v1/me", "", "Authorization", "bearer "+fullToken); a.status != http.StatusOK {
		t.Errorf("a lowercase scheme = %d", a.status)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/me", "", "Authorization", "Basic x", "Cookie", adminCookie); a.status !=
		http.StatusOK {
		t.Errorf("another scheme with a cookie = %d", a.status)
	}
	x.srv.tokens = nil
	if a = x.call(t, http.MethodGet, "/api/v1/me", "", bearer(fullToken)...); a.status != http.StatusUnauthorized {
		t.Errorf("without tokens = %d", a.status)
	}
}

// TestTokenRefusals is C-03.FR-27, C-03.AC-14 and C-04.AC-7: a token, even an Admin's with all Permissions, gets 403
// session_required on creating and revoking tokens, changing the password and TOTP, whatever its body, and on the
// live-updates stream, which follows a web session; a Service account token gets 403 service_account_not_allowed
// under /me and works elsewhere with its Role.
func TestTokenRefusals(t *testing.T) {
	x, _, _, _ := newTokensAPI(t)
	for _, req := range [][3]string{
		{http.MethodPost, "/api/v1/me/personal-access-tokens", `{"name":"x","permissions":[]}`},
		{http.MethodDelete, "/api/v1/me/personal-access-tokens/PTAAAAAAAAAAAA", ""},
		{http.MethodPut, "/api/v1/me/password", `{"current_password":"a","new_password":"another-password-1"}`},
		{http.MethodPost, "/api/v1/me/totp", ""},
		{http.MethodPost, "/api/v1/me/totp/removal", `{"password":"x"}`},
		{http.MethodPut, "/api/v1/me", `{"name":"x"}`},
		{http.MethodDelete, "/api/v1/me/sessions", ""},
		{http.MethodPost, "/api/v1/service-accounts/" + saPublicID + "/tokens", `{"name":"x"}`},
		{http.MethodDelete, "/api/v1/service-accounts/" + saPublicID + "/tokens/STAAAAAAAAAAAA", ""},
		{http.MethodGet, "/api/v1/live-updates", ""},
	} {
		for _, token := range []string{fullToken, serviceToken} {
			a := x.call(t, req[0], req[1], req[2], bearer(token)...)
			if a.status != http.StatusForbidden || a.code(t) != codeSessionRequired {
				t.Errorf("%s %s with %s = %d %s", req[0], req[1], token, a.status, a.body)
			}
		}
	}
	for _, path := range []string{"/api/v1/me", "/api/v1/me/personal-access-tokens", "/api/v1/me/totp"} {
		a := x.call(t, http.MethodGet, path, "", bearer(serviceToken)...)
		if a.status != http.StatusForbidden || a.code(t) != codeServiceAccountDenied {
			t.Errorf("a Service account on %s = %d %s", path, a.status, a.body)
		}
	}
	if a := x.call(t, http.MethodGet, "/api/v1/me/personal-access-tokens", "", bearer(fullToken)...); a.status !=
		http.StatusOK {
		t.Errorf("a Personal access token lists its owner's tokens: %d", a.status)
	}
	if a := x.call(t, http.MethodGet, "/api/v1/users", "", bearer(serviceToken)...); a.status != http.StatusOK {
		t.Errorf("a Service account with the Admin Role lists users: %d %s", a.status, a.body)
	}
}

// TestTokenRateLimit is C-04.FR-5 and C-04.AC-3 at the API: past the burst one token gets 429 with Retry-After while
// another token and the web session are unaffected.
func TestTokenRateLimit(t *testing.T) {
	x, _, _, realClock := newTokensAPI(t)
	for i := range tokens.RateBurst {
		if a := x.call(t, http.MethodGet, "/api/v1/me", "", bearer(readOnlyToken)...); a.status != http.StatusOK {
			t.Fatalf("request %d = %d", i, a.status)
		}
	}
	a := x.call(t, http.MethodGet, "/api/v1/me", "", bearer(readOnlyToken)...)
	if a.status != http.StatusTooManyRequests || a.header.Get("Retry-After") != "1" ||
		a.json(t)["type"] != problemBase+typeRateLimited {
		t.Fatalf("over the limit = %d %v %s", a.status, a.header, a.body)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/me", "", bearer(fullToken)...); a.status != http.StatusOK {
		t.Errorf("another token = %d", a.status)
	}
	for i := range 2 * tokens.RateBurst {
		if a = x.call(t, http.MethodGet, "/api/v1/me", "", "Cookie", adminCookie); a.status != http.StatusOK {
			t.Fatalf("session request %d = %d", i, a.status)
		}
	}
	realClock.Advance(time.Second)
	if a = x.call(t, http.MethodGet, "/api/v1/me", "", bearer(readOnlyToken)...); a.status != http.StatusOK {
		t.Errorf("after a second = %d", a.status)
	}
}

// TestPersonalAccessTokensAPI: the list never carries a value; create passes the session's Permissions as those held,
// answers the value once and maps permission_not_held to 422; revoke answers 204 and 404.
func TestPersonalAccessTokensAPI(t *testing.T) {
	x, ft, _, _ := newTokensAPI(t)
	used := t0.Add(time.Hour)
	ft.list = []tokens.Token{
		{PublicID: "PTAAAAAAAAAAAA", Name: "a", CreatedAt: t0, Permissions: []auth.Permission{"users:read"},
			LastUsedAt: &used, LastUsedAddress: netip.MustParseAddr("192.0.2.5")},
		{PublicID: "PTBBBBBBBBBBBB", Name: "b", CreatedAt: t0, ExpiresAt: &used, Permissions: []auth.Permission{}},
	}
	a := x.call(t, http.MethodGet, "/api/v1/me/personal-access-tokens", "", "Cookie", adminCookie)
	if a.status != http.StatusOK || strings.Contains(string(a.body), `"value"`) ||
		!strings.Contains(string(a.body), `"last_used_address":"192.0.2.5"`) {
		t.Errorf("list = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"ci","permissions":["users:read"],"expires_at":"2026-12-01T00:00:00Z"}`)
	if a.status != http.StatusCreated || a.json(t)["value"] != "mstr_pat_new" {
		t.Fatalf("create = %d %s", a.status, a.body)
	}
	n := ft.created[0]
	if n.Name != "ci" || n.ExpiresAt == nil || !slices.Equal(ft.held[0], roles.Permissions(auth.RoleAdmin)) ||
		ft.owners[0].ID != 1 || ft.by[0].Transport != audit.TransportUI || ft.by[0].Actor.TokenName != "" {
		t.Errorf("created %+v by %+v held %v", n, ft.by[0], ft.held[0])
	}
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"ci","permissions":["users:read"],"expires_at":null}`); a.status != http.StatusCreated ||
		ft.created[1].ExpiresAt != nil {
		t.Errorf("a null expiry = %d", a.status)
	}
	ft.err = &tokens.FieldError{Pointer: "/permissions/0", Code: tokens.CodePermissionNotHeld, Detail: "x"}
	a = x.mutate(t, viewerCookie, http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"ci","permissions":["users:write"]}`)
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(string(a.body), `"permission_not_held"`) ||
		!strings.Contains(string(a.body), `"/permissions/0"`) {
		t.Errorf("permission_not_held = %d %s", a.status, a.body)
	}
	ft.err = nil
	if a = x.mutate(t, adminCookie, http.MethodDelete, "/api/v1/me/personal-access-tokens/PTAAAAAAAAAAAA", ""); a.status !=
		http.StatusNoContent || ft.revoked[0] != "PTAAAAAAAAAAAA" {
		t.Errorf("revoke = %d", a.status)
	}
	ft.err = tokens.ErrNotFound
	if a = x.mutate(t, adminCookie, http.MethodDelete, "/api/v1/me/personal-access-tokens/PTAAAAAAAAAAAA", ""); a.status !=
		http.StatusNotFound {
		t.Errorf("revoke unknown = %d", a.status)
	}
	ft.err = errors.New("boom")
	if a = x.call(t, http.MethodGet, "/api/v1/me/personal-access-tokens", "", "Cookie", adminCookie); a.status !=
		http.StatusInternalServerError {
		t.Errorf("a failed list = %d", a.status)
	}
	if a = x.call(t, http.MethodPost, "/api/v1/me/personal-access-tokens", `{"name":"ci","permissions":["users:read"]}`,
		"Cookie", adminCookie); a.status != http.StatusForbidden || a.code(t) != codeCSRFInvalid {
		t.Errorf("without the CSRF token = %d %s", a.status, a.body)
	}
}

// TestServiceAccountsAPI is C-04.AC-8 at the API: list with a cursor, create with ETag and Location, read, update
// with If-Match (428 without, 412 stale, 409 name_taken), disable, enable, delete, and the tokens of an account; a
// Viewer gets 403.
func TestServiceAccountsAPI(t *testing.T) {
	x, ft, _, _ := newTokensAPI(t)
	next := int64(3)
	ft.page = tokens.Page{ServiceAccounts: []tokens.ServiceAccount{serviceAccount(1, tokens.StatusActive)}, Next: &next}
	a := x.call(t, http.MethodGet, "/api/v1/service-accounts?limit=1", "", "Cookie", adminCookie)
	if a.status != http.StatusOK || ft.filter.Limit != 1 {
		t.Fatalf("list = %d %s", a.status, a.body)
	}
	cursor, _ := a.json(t)["next_cursor"].(string)
	ft.page = tokens.Page{}
	a = x.call(t, http.MethodGet, "/api/v1/service-accounts?cursor="+cursor, "", "Cookie", adminCookie)
	if a.status != http.StatusOK || ft.filter.After == nil || *ft.filter.After != 3 || a.json(t)["next_cursor"] != nil {
		t.Errorf("page 2 = %d %s %+v", a.status, a.body, ft.filter)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/service-accounts?cursor="+encodeCursor(usersCursor, userKey{}), "",
		"Cookie", adminCookie); a.status != http.StatusBadRequest {
		t.Errorf("a cursor of users = %d", a.status)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/service-accounts", `{"name":"terraform","role":"admin"}`)
	if a.status != http.StatusCreated || a.header.Get("ETag") != `"1"` ||
		a.header.Get("Location") != "/api/v1/service-accounts/"+saPublicID || ft.inputs[0].Role != "admin" {
		t.Errorf("create = %d %v %s", a.status, a.header, a.body)
	}
	if a = x.call(t, http.MethodGet, "/api/v1/service-accounts/"+saPublicID, "", "Cookie", adminCookie); a.status !=
		http.StatusOK || a.header.Get("ETag") != `"1"` {
		t.Errorf("get = %d %v", a.status, a.header)
	}
	body := `{"name":"tf","role":"viewer"}`
	if a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/service-accounts/"+saPublicID, body); a.status !=
		http.StatusPreconditionRequired {
		t.Errorf("an update without If-Match = %d", a.status)
	}
	if a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/service-accounts/"+saPublicID, body, "If-Match",
		`"1"`); a.status != http.StatusOK || a.header.Get("ETag") != `"2"` || *ft.version[0] != 1 {
		t.Errorf("update = %d %v", a.status, a.header)
	}
	if a = x.call(t, http.MethodPut, "/api/v1/service-accounts/"+saPublicID, body, append(bearer(fullToken),
		"If-Match", `"2"`)...); a.status != http.StatusOK {
		t.Errorf("an update with a token = %d %s", a.status, a.body)
	}
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/service-accounts/"+saPublicID+"/disable", ""); a.status !=
		http.StatusOK || a.json(t)["status"] != "disabled" {
		t.Errorf("disable = %d %s", a.status, a.body)
	}
	if a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/service-accounts/"+saPublicID+"/enable", ""); a.status !=
		http.StatusOK || a.json(t)["status"] != "active" {
		t.Errorf("enable = %d %s", a.status, a.body)
	}
	a = x.mutate(t, adminCookie, http.MethodPost, "/api/v1/service-accounts/"+saPublicID+"/tokens", `{"name":"ci"}`)
	if a.status != http.StatusCreated || a.json(t)["value"] != "mstr_sat_new" || ft.saToken[0].Name != "ci" {
		t.Errorf("create a token = %d %s", a.status, a.body)
	}
	ft.list = []tokens.Token{{PublicID: "STBBBBBBBBBBBB", Name: "ci", CreatedAt: t0}}
	if a = x.call(t, http.MethodGet, "/api/v1/service-accounts/"+saPublicID+"/tokens", "", "Cookie", adminCookie); a.status !=
		http.StatusOK || strings.Contains(string(a.body), `"value"`) || !strings.Contains(string(a.body), `"ci"`) {
		t.Errorf("tokens = %d %s", a.status, a.body)
	}
	if a = x.mutate(t, adminCookie, http.MethodDelete, "/api/v1/service-accounts/"+saPublicID+"/tokens/STBBBBBBBBBBBB",
		""); a.status != http.StatusNoContent {
		t.Errorf("revoke a token = %d", a.status)
	}
	if a = x.mutate(t, adminCookie, http.MethodDelete, "/api/v1/service-accounts/"+saPublicID, "", "If-Match",
		`"3"`); a.status != http.StatusNoContent || *ft.version[len(ft.version)-1] != 3 {
		t.Errorf("delete = %d", a.status)
	}
	if a = x.mutate(t, adminCookie, http.MethodDelete, "/api/v1/service-accounts/"+saPublicID, ""); a.status !=
		http.StatusNoContent || ft.version[len(ft.version)-1] != nil {
		t.Errorf("delete without If-Match = %d", a.status)
	}
	if a = x.mutate(t, adminCookie, http.MethodDelete, "/api/v1/service-accounts/"+saPublicID, "", "If-Match",
		"nope"); a.status != http.StatusPreconditionFailed {
		t.Errorf("delete with a bad If-Match = %d", a.status)
	}
	if a = x.mutate(t, viewerCookie, http.MethodPost, "/api/v1/service-accounts", `{"name":"x","role":"viewer"}`); a.status !=
		http.StatusForbidden {
		t.Errorf("a viewer creates a Service account: %d", a.status)
	}
	for err, want := range map[error]int{tokens.ErrNameTaken: http.StatusConflict,
		tokens.ErrVersionMismatch: http.StatusPreconditionFailed, tokens.ErrNotFound: http.StatusNotFound,
		&tokens.FieldError{Pointer: "/name", Code: tokens.CodeTooLong, Detail: "x"}: http.StatusUnprocessableEntity} {
		ft.err = err
		if a = x.mutate(t, adminCookie, http.MethodPut, "/api/v1/service-accounts/"+saPublicID, body, "If-Match",
			`"1"`); a.status != want {
			t.Errorf("%v = %d %s", err, a.status, a.body)
		}
	}
	ft.err = errors.New("boom")
	for _, req := range [][3]string{
		{http.MethodGet, "/api/v1/service-accounts", ""},
		{http.MethodGet, "/api/v1/service-accounts/" + saPublicID, ""},
		{http.MethodGet, "/api/v1/service-accounts/" + saPublicID + "/tokens", ""},
		{http.MethodPost, "/api/v1/service-accounts", `{"name":"x","role":"viewer"}`},
		{http.MethodPost, "/api/v1/service-accounts/" + saPublicID + "/disable", ""},
		{http.MethodPost, "/api/v1/service-accounts/" + saPublicID + "/enable", ""},
		{http.MethodPost, "/api/v1/service-accounts/" + saPublicID + "/tokens", `{"name":"x"}`},
		{http.MethodDelete, "/api/v1/service-accounts/" + saPublicID + "/tokens/STBBBBBBBBBBBB", ""},
		{http.MethodDelete, "/api/v1/service-accounts/" + saPublicID, ""},
		{http.MethodPost, "/api/v1/me/personal-access-tokens", `{"name":"x","permissions":["users:read"]}`},
	} {
		if a = x.mutate(t, adminCookie, req[0], req[1], req[2]); a.status != http.StatusInternalServerError {
			t.Errorf("%s %s failing = %d", req[0], req[1], a.status)
		}
	}
}

// TestTokenHandlersWithoutIdentity: every handler of this file refuses a request without an identity, and without a
// body where it needs one.
func TestTokenHandlersWithoutIdentity(t *testing.T) {
	x, _, _, _ := newTokensAPI(t)
	ctx := t.Context()
	sa := gen.ServiceAccountId(saPublicID)
	calls := []func(context.Context) error{
		func(ctx context.Context) error {
			_, err := x.srv.ListPersonalAccessTokens(ctx, gen.ListPersonalAccessTokensRequestObject{})
			return err
		},
		func(ctx context.Context) error {
			_, err := x.srv.CreatePersonalAccessToken(ctx, gen.CreatePersonalAccessTokenRequestObject{})
			return err
		},
		func(ctx context.Context) error {
			_, err := x.srv.RevokePersonalAccessToken(ctx, gen.RevokePersonalAccessTokenRequestObject{})
			return err
		},
		func(ctx context.Context) error {
			_, err := x.srv.CreateServiceAccount(ctx, gen.CreateServiceAccountRequestObject{})
			return err
		},
		func(ctx context.Context) error {
			_, err := x.srv.UpdateServiceAccount(ctx, gen.UpdateServiceAccountRequestObject{ServiceAccountId: sa,
				Params: gen.UpdateServiceAccountParams{IfMatch: `"1"`}})
			return err
		},
		func(ctx context.Context) error {
			_, err := x.srv.DeleteServiceAccount(ctx, gen.DeleteServiceAccountRequestObject{ServiceAccountId: sa})
			return err
		},
		func(ctx context.Context) error {
			_, err := x.srv.DisableServiceAccount(ctx, gen.DisableServiceAccountRequestObject{ServiceAccountId: sa})
			return err
		},
		func(ctx context.Context) error {
			_, err := x.srv.EnableServiceAccount(ctx, gen.EnableServiceAccountRequestObject{ServiceAccountId: sa})
			return err
		},
		func(ctx context.Context) error {
			_, err := x.srv.CreateServiceAccountToken(ctx, gen.CreateServiceAccountTokenRequestObject{
				ServiceAccountId: sa})
			return err
		},
		func(ctx context.Context) error {
			_, err := x.srv.RevokeServiceAccountToken(ctx, gen.RevokeServiceAccountTokenRequestObject{
				ServiceAccountId: sa})
			return err
		},
	}
	for i, call := range calls {
		if err := call(ctx); !errors.Is(err, errUnauthenticated) {
			t.Errorf("handler %d: %v", i, err)
		}
	}
	id := &auth.Identity{Session: session(1, x.users.users[1], auth.StateActive), Transport: audit.TransportUI}
	ctx = auth.WithIdentity(ctx, id)
	for _, i := range []int{1, 3, 4, 8} {
		if err := calls[i](ctx); err == nil || errors.Is(err, errUnauthenticated) {
			t.Errorf("handler %d without a body: %v", i, err)
		}
	}
	if _, err := x.srv.UpdateServiceAccount(ctx, gen.UpdateServiceAccountRequestObject{ServiceAccountId: sa,
		Params: gen.UpdateServiceAccountParams{IfMatch: "nope"}}); !errors.Is(err, errPreconditionFailed) {
		t.Errorf("a bad If-Match: %v", err)
	}
}

// TestProblemCodesOfTokens: the codes this story answers are catalogued in the specification.
func TestProblemCodesOfTokens(t *testing.T) {
	doc, err := LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc.Components.Schemas["Problem"].Value.Extensions["x-problem-codes"])
	var catalogue []struct {
		Type  string `json:"type"`
		Codes []struct {
			Code string `json:"code"`
		} `json:"codes"`
	}
	if err := json.Unmarshal(raw, &catalogue); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, c := range catalogue {
		for _, code := range c.Codes {
			found[c.Type+" "+code.Code] = true
		}
	}
	for _, c := range []string{typeForbidden + " " + codeServiceAccountDenied,
		typeUnauthenticated + " " + codeOIDCRecheckRequired, typeForbidden + " " + codeSessionRequired,
		typeUnauthenticated + " " + codeInvalidCredentials, typeConflict + " " + codeNameTaken,
		typeValidationFailed + " " + tokens.CodePermissionNotHeld, typeValidationFailed + " " + tokens.CodeOutOfRange,
		typeValidationFailed + " " + tokens.CodeTooLong, typeValidationFailed + " " + tokens.CodeInvalidFormat} {
		if !found[c] {
			t.Errorf("%s is not catalogued", c)
		}
	}
}

// TestRoleAssignmentByToken is the Role assignment rule of C-04.FR-7 at the API: a token narrowed to users:write
// cannot create an Admin (422 permission_not_held at /role) but can create a Viewer once it holds all of the Viewer's
// Permissions; the rule covers updating a user and creating and updating a Service account, and the web session is
// unaffected.
func TestRoleAssignmentByToken(t *testing.T) {
	x, ft, fa, _ := newTokensAPI(t)
	admin := ft.idents[fullToken].Session
	narrow := func(name string, perms ...auth.Permission) {
		ft.idents[name] = &auth.Identity{Session: admin, Permissions: perms, Transport: audit.TransportAPI,
			Token: &auth.Token{ID: int64(len(ft.idents) + 20), Name: name}}
	}
	narrow("mstr_pat_users", "users:write")
	narrow("mstr_pat_viewer", "alert-groups:read", "integrations:read", "users:write")
	narrow("mstr_pat_accounts", "service-accounts:write")
	refused := func(a answer) bool {
		return a.status == http.StatusUnprocessableEntity && strings.Contains(string(a.body), `"permission_not_held"`) &&
			strings.Contains(string(a.body), `"pointer":"/role"`)
	}
	created := len(fa.created)
	for _, c := range [][2]string{{"mstr_pat_users", "admin"}, {"mstr_pat_viewer", "admin"},
		{"mstr_pat_users", "viewer"}} {
		a := x.call(t, http.MethodPost, "/api/v1/users", `{"name":"Eve","login":"eve","role":"`+c[1]+`"}`,
			bearer(c[0])...)
		if !refused(a) {
			t.Errorf("%s creates a %s: %d %s", c[0], c[1], a.status, a.body)
		}
	}
	if len(fa.created) != created {
		t.Fatal("a refused user reached the domain")
	}
	if a := x.call(t, http.MethodPost, "/api/v1/users", `{"name":"Eve","login":"eve","role":"viewer"}`,
		bearer("mstr_pat_viewer")...); a.status != http.StatusCreated {
		t.Errorf("a token with the Viewer's Permissions creates a Viewer: %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodPut, "/api/v1/users/"+bobPublicID, `{"name":"Bob","role":"admin"}`,
		append(bearer("mstr_pat_viewer"), "If-Match", `"1"`)...); !refused(a) {
		t.Errorf("a token makes Bob an Admin: %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodPost, "/api/v1/service-accounts", `{"name":"tf","role":"viewer"}`,
		bearer("mstr_pat_accounts")...); !refused(a) {
		t.Errorf("a token creates a Viewer Service account without its Permissions: %d %s", a.status, a.body)
	}
	if a := x.call(t, http.MethodPut, "/api/v1/service-accounts/"+saPublicID, `{"name":"tf","role":"admin"}`,
		append(bearer("mstr_pat_accounts"), "If-Match", `"1"`)...); !refused(a) {
		t.Errorf("a token makes a Service account an Admin: %d %s", a.status, a.body)
	}
	if len(ft.inputs) != 0 {
		t.Error("a refused Service account reached the domain")
	}
	if a := x.mutate(t, adminCookie, http.MethodPost, "/api/v1/users", `{"name":"Ada","login":"ada","role":"admin"}`); a.status !=
		http.StatusCreated {
		t.Errorf("the Admin's session creates an Admin: %d %s", a.status, a.body)
	}
}
