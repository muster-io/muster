// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package fakeoidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	redirectURI = "http://localhost:8080/api/v1/sessions/oidc/callback"
	verifier    = "verifier-0123456789-0123456789-0123456789"
)

func start(t *testing.T) *Fake {
	t.Helper()
	f := New()
	if err := f.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close(context.WithoutCancel(t.Context())) })
	return f
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func getJSON(t *testing.T, u string, v any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if v != nil {
		_ = json.NewDecoder(resp.Body).Decode(v)
	}
}

func post(t *testing.T, u string, form url.Values, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if form != nil {
		req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, u, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, u, strings.NewReader(body))
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// authorize runs the authorization endpoint and returns the query of the redirect back.
func authorize(t *testing.T, f *Fake, scope string) url.Values {
	t.Helper()
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {"muster"}, "redirect_uri": {redirectURI},
		"scope": {scope}, "state": {"st"}, "nonce": {"n-1"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, f.URL()+"/authorize?"+q.Encode(), nil)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize answered %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if !strings.HasPrefix(loc.String(), redirectURI) || loc.Query().Get("state") != "st" {
		t.Fatalf("redirect = %s", loc)
	}
	return loc.Query()
}

func verify(t *testing.T, f *Fake, raw string) map[string]any {
	t.Helper()
	var set jose.JSONWebKeySet
	getJSON(t, f.URL()+"/jwks", &set)
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	keys := set.Key(tok.Headers[0].KeyID)
	if len(keys) != 1 {
		t.Fatalf("the key %s is not in the key set", tok.Headers[0].KeyID)
	}
	var claims map[string]any
	if err := tok.Claims(keys[0].Key, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestDiscoveryAndGroupsClaim(t *testing.T) {
	f := start(t)
	var d map[string]any
	getJSON(t, f.URL()+"/.well-known/openid-configuration", &d)
	if d["issuer"] != f.URL() || d["token_endpoint"] != f.URL()+"/token" || d["jwks_uri"] != f.URL()+"/jwks" {
		t.Fatalf("discovery = %v", d)
	}
	if !slices.Contains(d["claims_supported"].([]any), any(GroupsClaim)) {
		t.Fatal("discovery does not advertise the groups claim")
	}
	if code, _ := post(t, f.URL()+"/_fake/config", nil, `{"omit_groups_claim":true}`); code != http.StatusNoContent {
		t.Fatalf("config = %d", code)
	}
	getJSON(t, f.URL()+"/.well-known/openid-configuration", &d)
	if slices.Contains(d["claims_supported"].([]any), any(GroupsClaim)) {
		t.Fatal("the groups claim is still advertised")
	}
}

func TestCodeFlowWithPKCEAndRefreshRotation(t *testing.T) {
	f := start(t)
	if q := authorize(t, f, "openid"); q.Get("error") != "access_denied" {
		t.Fatalf("without a scripted user = %v", q)
	}
	code, _ := post(t, f.URL()+"/_fake/next-user", nil,
		`{"sub":"u-1","preferred_username":"olga","email":"olga@example.org","groups":["oncall"],"amr":["pwd","otp"]}`)
	if code != http.StatusNoContent {
		t.Fatalf("next-user = %d", code)
	}
	q := authorize(t, f, "openid offline_access profile")
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "client_id": {"muster"},
		"redirect_uri": {redirectURI}, "code_verifier": {"wrong"}}
	if status, body := post(t, f.URL()+"/token", exchange, ""); status != http.StatusBadRequest ||
		body["error"] != "invalid_grant" {
		t.Fatalf("a wrong verifier = %d %v", status, body)
	}
	q = authorize(t, f, "openid offline_access profile")
	exchange.Set("code", q.Get("code"))
	exchange.Set("code_verifier", verifier)
	status, body := post(t, f.URL()+"/token", exchange, "")
	if status != http.StatusOK || body["refresh_token"] == nil || !strings.Contains(body["scope"].(string), "offline_access") {
		t.Fatalf("exchange = %d %v", status, body)
	}
	claims := verify(t, f, body["id_token"].(string))
	if claims["sub"] != "u-1" || claims["nonce"] != "n-1" || claims["aud"] != "muster" || claims["iss"] != f.URL() ||
		claims["preferred_username"] != "olga" || len(claims["groups"].([]any)) != 1 {
		t.Fatalf("claims = %v", claims)
	}
	if status, _ := post(t, f.URL()+"/token", exchange, ""); status != http.StatusBadRequest {
		t.Fatalf("a code used twice = %d", status)
	}

	var info map[string]any
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, f.URL()+"/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+body["access_token"].(string))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&info)
	_ = resp.Body.Close()
	if info["sub"] != "u-1" || len(info["groups"].([]any)) != 1 {
		t.Fatalf("userinfo = %v", info)
	}

	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {body["refresh_token"].(string)},
		"client_id": {"muster"}}
	status, rotated := post(t, f.URL()+"/token", refresh, "")
	if status != http.StatusOK || rotated["refresh_token"] == body["refresh_token"] {
		t.Fatalf("refresh = %d %v", status, rotated)
	}
	if verify(t, f, rotated["id_token"].(string))["nonce"] != nil {
		t.Fatal("a refreshed ID token carries the nonce")
	}
	if status, _ := post(t, f.URL()+"/token", refresh, ""); status != http.StatusBadRequest {
		t.Fatalf("a rotated refresh token was accepted again: %d", status)
	}
	if code, _ := post(t, f.URL()+"/_fake/users/u-1/disable", nil, ""); code != http.StatusNoContent {
		t.Fatalf("disable = %d", code)
	}
	refresh.Set("refresh_token", rotated["refresh_token"].(string))
	if status, b := post(t, f.URL()+"/token", refresh, ""); status != http.StatusBadRequest || b["error"] != "invalid_grant" {
		t.Fatalf("refresh of a disabled user = %d %v", status, b)
	}
	if q := authorize(t, f, "openid"); q.Get("error") != "access_denied" {
		t.Fatalf("a disabled user was approved: %v", q)
	}
}

func TestWithheldOfflineAccessGroupsInUserinfoAndKeyRotation(t *testing.T) {
	f := start(t)
	f.Configure(Config{GrantOfflineAccess: new(false)})
	f.SetNextUser(User{Subject: "u-2", Groups: []string{"oncall"}, GroupsInUserinfoOnly: true})
	q := authorize(t, f, "openid offline_access")
	status, body := post(t, f.URL()+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier}, "client_id": {"muster"}}, "")
	if status != http.StatusOK || body["refresh_token"] != nil || strings.Contains(body["scope"].(string), "offline") {
		t.Fatalf("exchange without offline_access = %d %v", status, body)
	}
	first := body["id_token"].(string)
	if _, ok := verify(t, f, first)["groups"]; ok {
		t.Fatal("the ID token carries the groups claim")
	}
	if code, _ := post(t, f.URL()+"/_fake/rotate-keys", nil, ""); code != http.StatusNoContent {
		t.Fatalf("rotate = %d", code)
	}
	signed, err := f.Sign(map[string]any{"sub": "x"})
	if err != nil {
		t.Fatal(err)
	}
	verify(t, f, first) // the old key is still published
	verify(t, f, signed)
	if status, _ := post(t, f.URL()+"/token", url.Values{"grant_type": {"password"}, "client_id": {"m"}}, ""); status != http.StatusBadRequest {
		t.Fatalf("an unknown grant = %d", status)
	}
	if status, _ := post(t, f.URL()+"/token", url.Values{"grant_type": {"refresh_token"}}, ""); status != http.StatusUnauthorized {
		t.Fatalf("no client = %d", status)
	}
	if code, _ := post(t, f.URL()+"/_fake/next-user", nil, `{"preferred_username":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("a user without sub = %d", code)
	}
}
