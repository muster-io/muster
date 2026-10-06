// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package fakeoidc is the fake OpenID Connect identity provider: discovery, a key set, an authorization endpoint that
// approves the scripted next user at once, a token endpoint that checks PKCE and rotates refresh tokens, and userinfo.
// Control endpoints under /_fake/ script the next user, disable and enable users, grant or withhold offline_access,
// leave the groups claim out of discovery and rotate the signing key. The same server runs in Go tests and in
// `muster dev`. It is a test double: it accepts any client secret and keeps everything in memory.
package fakeoidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/muster-io/muster/internal/fakes/fakeserver"
)

const (
	// GroupsClaim is the name of the groups claim the fake issues.
	GroupsClaim = "groups"
	// tokenLifetime is how long the ID tokens and access tokens the fake issues are valid.
	tokenLifetime = 5 * time.Minute
	keyBits       = 2048
)

// User is a person at the fake identity provider.
type User struct {
	Subject           string   `json:"sub"`
	PreferredUsername string   `json:"preferred_username,omitempty"`
	Email             string   `json:"email,omitempty"`
	Name              string   `json:"name,omitempty"`
	Groups            []string `json:"groups,omitempty"`
	AMR               []string `json:"amr,omitempty"`
	// GroupsInUserinfoOnly leaves the groups claim out of the ID token; userinfo still carries it.
	GroupsInUserinfoOnly bool `json:"groups_in_userinfo_only,omitempty"`
}

// Config is the behaviour of the fake that /_fake/config changes; a field that a request omits keeps its value.
type Config struct {
	// GrantOfflineAccess grants offline_access when it is requested, with a refresh token.
	GrantOfflineAccess *bool `json:"grant_offline_access,omitempty"`
	// OmitGroupsClaim leaves the groups claim out of claims_supported in discovery.
	OmitGroupsClaim *bool `json:"omit_groups_claim,omitempty"`
}

// grant is what an authorization code or a refresh token stands for.
type grant struct {
	user      User
	clientID  string
	nonce     string
	challenge string
	redirect  string
	scopes    []string
	expires   time.Time
}

// Fake is the fake identity provider.
type Fake struct {
	*fakeserver.Server

	mu       sync.Mutex
	key      *rsa.PrivateKey
	kid      string
	old      []jose.JSONWebKey // keys rotated out, still published
	next     *User
	disabled map[string]bool
	offline  bool
	omit     bool
	codes    map[string]grant
	refresh  map[string]grant
	access   map[string]User
	now      func() time.Time
}

// New returns a fake identity provider with a fresh signing key; it grants offline_access by default.
func New() *Fake {
	f := &Fake{disabled: map[string]bool{}, offline: true, codes: map[string]grant{}, refresh: map[string]grant{},
		access: map[string]User{}, now: time.Now}
	f.rotate()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("GET /jwks", f.jwks)
	mux.HandleFunc("GET /authorize", f.authorize)
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("GET /userinfo", f.userinfo)
	f.Server = fakeserver.New("OIDC", mux)
	f.HandleControl("POST /_fake/next-user", f.setNextUser)
	f.HandleControl("POST /_fake/users/{sub}/disable", func(w http.ResponseWriter, r *http.Request) {
		f.SetDisabled(r.PathValue("sub"), true)
		w.WriteHeader(http.StatusNoContent)
	})
	f.HandleControl("POST /_fake/users/{sub}/enable", func(w http.ResponseWriter, r *http.Request) {
		f.SetDisabled(r.PathValue("sub"), false)
		w.WriteHeader(http.StatusNoContent)
	})
	f.HandleControl("POST /_fake/config", f.setConfig)
	f.HandleControl("POST /_fake/rotate-keys", func(w http.ResponseWriter, _ *http.Request) {
		f.rotate()
		w.WriteHeader(http.StatusNoContent)
	})
	return f
}

// SetNextUser scripts the user the authorization endpoint approves, for every authorization until the next call.
func (f *Fake) SetNextUser(u User) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next = &u
}

// SetDisabled disables or enables a user: the refresh tokens of a disabled user answer invalid_grant, and the
// authorization endpoint refuses them.
func (f *Fake) SetDisabled(sub string, disabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disabled[sub] = disabled
}

// Configure changes what c sets.
func (f *Fake) Configure(c Config) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.GrantOfflineAccess != nil {
		f.offline = *c.GrantOfflineAccess
	}
	if c.OmitGroupsClaim != nil {
		f.omit = *c.OmitGroupsClaim
	}
}

// SetClock makes the fake issue tokens at the times now gives, for tests that check token times.
func (f *Fake) SetClock(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

// rotate makes a new signing key; the old public keys stay in the key set.
func (f *Fake) rotate() {
	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		panic("fakeoidc: generate a key: " + err.Error())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key != nil {
		f.old = append(f.old, jose.JSONWebKey{Key: &f.key.PublicKey, KeyID: f.kid, Algorithm: string(jose.RS256),
			Use: "sig"})
	}
	f.key, f.kid = key, rand.Text()[:12]
}

// Sign signs claims with the current key, for tests that need tokens the endpoints would not issue.
func (f *Fake) Sign(claims any) (string, error) {
	f.mu.Lock()
	key, kid := f.key, f.kid
	f.mu.Unlock()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}

// issuer is the base URL of the fake: its listen address, or the Host of the request before Start.
func (f *Fake) issuer(r *http.Request) string {
	if u := f.URL(); u != "" {
		return u
	}
	return "http://" + r.Host
}

func (f *Fake) discovery(w http.ResponseWriter, r *http.Request) {
	iss := f.issuer(r)
	claims := []string{"sub", "iss", "aud", "exp", "iat", "nonce", "preferred_username", "email", "name", "amr"}
	f.mu.Lock()
	if !f.omit {
		claims = append(claims, GroupsClaim)
	}
	f.mu.Unlock()
	fakeserver.WriteJSON(w, http.StatusOK, map[string]any{
		"issuer": iss, "authorization_endpoint": iss + "/authorize", "token_endpoint": iss + "/token",
		"userinfo_endpoint": iss + "/userinfo", "jwks_uri": iss + "/jwks",
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"scopes_supported":                      []string{"openid", "offline_access", "profile", "email"},
		"claims_supported":                      claims,
	})
}

func (f *Fake) jwks(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	keys := append(slices.Clone(f.old), jose.JSONWebKey{Key: &f.key.PublicKey, KeyID: f.kid,
		Algorithm: string(jose.RS256), Use: "sig"})
	f.mu.Unlock()
	fakeserver.WriteJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: keys})
}

// authorize approves the scripted next user at once and redirects back with a code, as an identity provider does
// after the person signed in; it answers the errors of RFC 6749 §4.1.2.1 in the redirect.
func (f *Fake) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || !redirect.IsAbs() {
		fakeserver.WriteError(w, http.StatusBadRequest, "redirect_uri is missing or not absolute")
		return
	}
	back := func(params url.Values) {
		rq := redirect.Query()
		for k, v := range params {
			rq[k] = v
		}
		if s := q.Get("state"); s != "" {
			rq.Set("state", s)
		}
		redirect.RawQuery = rq.Encode()
		//nolint:gosec // G710: the fake identity provider redirects to the redirect_uri of the request, as a real one does
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	}
	switch {
	case q.Get("response_type") != "code":
		back(url.Values{"error": {"unsupported_response_type"}})
		return
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		back(url.Values{"error": {"invalid_request"}, "error_description": {"PKCE S256 is required"}})
		return
	case !slices.Contains(strings.Fields(q.Get("scope")), "openid"):
		back(url.Values{"error": {"invalid_scope"}})
		return
	}
	f.mu.Lock()
	next := f.next
	disabled := next != nil && f.disabled[next.Subject]
	f.mu.Unlock()
	if next == nil || disabled {
		back(url.Values{"error": {"access_denied"}})
		return
	}
	code := rand.Text()
	f.mu.Lock()
	f.codes[code] = grant{user: *next, clientID: q.Get("client_id"), nonce: q.Get("nonce"),
		challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), scopes: strings.Fields(q.Get("scope")),
		expires: f.now().Add(time.Minute)}
	f.mu.Unlock()
	back(url.Values{"code": {code}})
}

// tokenError answers an error of the token endpoint (RFC 6749 §5.2).
func tokenError(w http.ResponseWriter, status int, code string) {
	fakeserver.WriteJSON(w, status, map[string]string{"error": code})
}

func (f *Fake) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	clientID := r.PostForm.Get("client_id")
	if id, _, ok := r.BasicAuth(); ok {
		if unescaped, err := url.QueryUnescape(id); err == nil {
			clientID = unescaped
		}
	}
	if clientID == "" {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		f.exchange(w, r, clientID)
	case "refresh_token":
		f.refreshTokens(w, r, clientID)
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func (f *Fake) exchange(w http.ResponseWriter, r *http.Request, clientID string) {
	code := r.PostForm.Get("code")
	f.mu.Lock()
	g, ok := f.codes[code]
	delete(f.codes, code)
	now := f.now()
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case !ok || now.After(g.expires) || g.clientID != clientID || g.redirect != r.PostForm.Get("redirect_uri"):
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	f.issue(w, r, g, true)
}

func (f *Fake) refreshTokens(w http.ResponseWriter, r *http.Request, clientID string) {
	token := r.PostForm.Get("refresh_token")
	f.mu.Lock()
	g, ok := f.refresh[token]
	delete(f.refresh, token) // rotation: a refresh token works once
	disabled := ok && f.disabled[g.user.Subject]
	f.mu.Unlock()
	if !ok || disabled || g.clientID != clientID {
		tokenError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	g.nonce = ""
	f.issue(w, r, g, false)
}

// issue answers the tokens of g: an ID token, an access token and, when offline_access is granted, a refresh token.
func (f *Fake) issue(w http.ResponseWriter, r *http.Request, g grant, withNonce bool) {
	f.mu.Lock()
	now := f.now()
	granted := slices.DeleteFunc(slices.Clone(g.scopes), func(s string) bool { return s == "offline_access" && !f.offline })
	f.mu.Unlock()
	claims := map[string]any{
		"iss": f.issuer(r), "sub": g.user.Subject, "aud": g.clientID, "azp": g.clientID,
		"exp": now.Add(tokenLifetime).Unix(), "iat": now.Unix(), "auth_time": now.Unix(),
	}
	if withNonce && g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	for k, v := range map[string]string{"preferred_username": g.user.PreferredUsername, "email": g.user.Email,
		"name": g.user.Name} {
		if v != "" {
			claims[k] = v
		}
	}
	if len(g.user.AMR) > 0 {
		claims["amr"] = g.user.AMR
	}
	if !g.user.GroupsInUserinfoOnly {
		claims[GroupsClaim] = groups(g.user)
	}
	idToken, err := f.Sign(claims)
	if err != nil {
		tokenError(w, http.StatusInternalServerError, "server_error")
		return
	}
	access := rand.Text()
	answer := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(tokenLifetime.Seconds()),
		"id_token": idToken, "scope": strings.Join(granted, " ")}
	f.mu.Lock()
	f.access[access] = g.user
	if slices.Contains(granted, "offline_access") {
		refresh := rand.Text()
		f.refresh[refresh] = g
		answer["refresh_token"] = refresh
	}
	f.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	fakeserver.WriteJSON(w, http.StatusOK, answer)
}

func groups(u User) []string {
	if u.Groups == nil {
		return []string{}
	}
	return u.Groups
}

func (f *Fake) userinfo(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	u, known := f.access[token]
	f.mu.Unlock()
	if !ok || !known {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		fakeserver.WriteError(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	claims := map[string]any{"sub": u.Subject, GroupsClaim: groups(u)}
	for k, v := range map[string]string{"preferred_username": u.PreferredUsername, "email": u.Email, "name": u.Name} {
		if v != "" {
			claims[k] = v
		}
	}
	fakeserver.WriteJSON(w, http.StatusOK, claims)
}

func (f *Fake) setNextUser(w http.ResponseWriter, r *http.Request) {
	var u User
	if err := fakeserver.DecodeJSON(w, r, &u); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if u.Subject == "" {
		fakeserver.WriteError(w, http.StatusBadRequest, "sub is required")
		return
	}
	f.SetNextUser(u)
	w.WriteHeader(http.StatusNoContent)
}

func (f *Fake) setConfig(w http.ResponseWriter, r *http.Request) {
	var c Config
	if err := fakeserver.DecodeJSON(w, r, &c); err != nil {
		fakeserver.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.Configure(c)
	w.WriteHeader(http.StatusNoContent)
}
