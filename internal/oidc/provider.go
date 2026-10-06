// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// The back channel (C-03.FR-8, ADR-0015): discovery, the code exchange and userinfo go through the interactive class,
// the key set through the background class, each with the OIDC proxy. Every call is outbound.Client.Do.
const (
	connectTimeout = 5 * time.Second
	callTimeout    = 10 * time.Second
	// discoveryMaxAge is how long a replica keeps the provider metadata it read.
	discoveryMaxAge = time.Hour
	// keysRefetchAfter is the shortest time between two fetches of the key set for an unknown key id.
	keysRefetchAfter = 30 * time.Second
	// leeway is the clock skew allowed on exp, nbf and iat, on the real clock.
	leeway = time.Minute
	// maxMetadataBytes bounds discovery, key set, token and userinfo answers.
	maxMetadataBytes = 1 << 20
	discoveryPath    = "/.well-known/openid-configuration"
)

// signatureAlgorithms are the ID-token algorithms Muster accepts: asymmetric only, never none or HMAC.
var signatureAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512, jose.PS256, jose.PS384, jose.PS512, jose.ES256, jose.ES384, jose.ES512,
}

// Doer sends a request through an outbound client.
type Doer interface {
	Do(ctx context.Context, req outbound.Request) (outbound.Result, error)
}

// Network is what the back channel needs to build its outbound clients.
type Network struct {
	Policy outbound.PolicySource
	Log    *logging.Logger
	// Real is the real clock: ID-token times and the outbound metrics.
	Real clock.Clock
	// Resolver resolves host names; nil is the system resolver.
	Resolver outbound.Resolver
}

func (n Network) client(class outbound.Class, proxy *outbound.Proxy, secrets []logging.Secret) (*outbound.Client,
	error) {
	return outbound.New(outbound.Config{
		Class: class, ConnectTimeout: connectTimeout, Timeout: callTimeout, Proxy: proxy, Secrets: secrets,
		Policy: n.Policy, Logger: n.Log, Clock: n.Real, Resolver: n.Resolver, MaxBodyBytes: maxMetadataBytes,
	})
}

// Discovery is the part of the provider metadata Muster uses (OpenID Connect Discovery 1.0).
type Discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ClaimsSupported       []string `json:"claims_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
}

// AdvertisesClaim reports whether discovery lists claim; a provider that lists no claims advertises none.
func (d Discovery) AdvertisesClaim(claim string) bool {
	return slices.Contains(d.ClaimsSupported, claim)
}

// errBackChannel is a failure of the identity provider or of the way to it.
var errBackChannel = errors.New("the identity provider failed")

// BackChannelError is a failed call to the identity provider; its text is masked and names no secret.
type BackChannelError struct {
	Step string
	msg  string
	// Blocked is the rule of the outbound address policy that refused the call, if one did.
	Blocked string
	// Status is the status code of the answer, 0 without one.
	Status int
	// Code is the OAuth error code of a token endpoint answer, such as invalid_grant.
	Code string
	// Outcome is the classified outcome of the outbound call.
	Outcome outbound.Outcome
}

func (e *BackChannelError) Error() string { return e.Step + ": " + e.msg }

func (e *BackChannelError) Unwrap() error { return errBackChannel }

// backChannelError turns the error of an outbound call into a BackChannelError.
func backChannelError(step string, err error) *BackChannelError {
	out := &BackChannelError{Step: step, msg: err.Error()}
	if oe, ok := errors.AsType[*outbound.Error](err); ok {
		out.Blocked, out.Status, out.Outcome = oe.Rule, oe.Status, oe.Outcome
	}
	return out
}

// FetchDiscovery reads the provider metadata of issuer through c and checks that it names the same issuer (OpenID
// Connect Discovery §4.3); a configured issuer with a trailing slash matches one without.
func FetchDiscovery(ctx context.Context, c Doer, issuer string) (Discovery, error) {
	target := strings.TrimSuffix(issuer, "/") + discoveryPath
	res, err := c.Do(ctx, outbound.Request{URL: target, Header: http.Header{"Accept": {"application/json"}}})
	if err != nil {
		return Discovery{}, backChannelError("discovery", err)
	}
	var d Discovery
	if err := json.Unmarshal(res.Body, &d); err != nil {
		return Discovery{}, &BackChannelError{Step: "discovery", msg: "the answer is not JSON provider metadata",
			Status: res.Status, Outcome: outbound.OutcomeOK}
	}
	switch {
	case d.Issuer != issuer && d.Issuer != strings.TrimSuffix(issuer, "/"):
		return Discovery{}, &BackChannelError{Step: "discovery", Status: res.Status, Outcome: outbound.OutcomeOK,
			msg: fmt.Sprintf("the provider names the issuer %q, not the configured one", d.Issuer)}
	case !absoluteURL(d.AuthorizationEndpoint) || !absoluteURL(d.TokenEndpoint) || !absoluteURL(d.JWKSURI):
		return Discovery{}, &BackChannelError{Step: "discovery", Status: res.Status, Outcome: outbound.OutcomeOK,
			msg: "the provider metadata lack an absolute authorization endpoint, token endpoint or jwks_uri"}
	case d.UserinfoEndpoint != "" && !absoluteURL(d.UserinfoEndpoint):
		d.UserinfoEndpoint = ""
	}
	return d, nil
}

func absoluteURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// Provider is the back channel to the identity provider of one version of the OIDC settings: its clients carry the
// OIDC proxy and the client secret, and it keeps the provider metadata and the last good key set.
type Provider struct {
	issuer       string
	clientID     string
	clientSecret logging.Secret
	interactive  Doer
	background   Doer
	real         clock.Clock

	mu         sync.Mutex
	discovery  *Discovery
	readAt     time.Time
	keys       []jose.JSONWebKey
	keysAt     time.Time
	keysFromAt string // the jwks_uri the keys came from
	keysErr    error  // the error of the last fetch of the key set
	// fetching lets one verification at a time look up or fetch the key set.
	fetching sync.Mutex
}

// NewProvider builds the back channel for the issuer and client, with the proxy (nil: direct).
func NewProvider(n Network, issuer, clientID string, clientSecret logging.Secret, proxy *outbound.Proxy) (*Provider,
	error) {
	secrets := []logging.Secret{clientSecret}
	interactive, err := n.client(outbound.ClassInteractive, proxy, secrets)
	if err != nil {
		return nil, err
	}
	background, err := n.client(outbound.ClassBackground, proxy, secrets)
	if err != nil {
		return nil, err
	}
	return newProvider(issuer, clientID, clientSecret, interactive, background, n.Real), nil
}

func newProvider(issuer, clientID string, clientSecret logging.Secret, interactive, background Doer,
	realClock clock.Clock) *Provider {
	return &Provider{issuer: issuer, clientID: clientID, clientSecret: clientSecret, interactive: interactive,
		background: background, real: realClock}
}

// Interactive is the interactive client, for the connection check.
func (p *Provider) Interactive() Doer { return p.interactive }

// Discover returns the provider metadata, read at most discoveryMaxAge ago; a failed read is not kept.
func (p *Provider) Discover(ctx context.Context) (Discovery, error) {
	p.mu.Lock()
	if p.discovery != nil && p.real.Now().Sub(p.readAt) < discoveryMaxAge {
		d := *p.discovery
		p.mu.Unlock()
		return d, nil
	}
	p.mu.Unlock()
	d, err := FetchDiscovery(ctx, p.interactive, p.issuer)
	if err != nil {
		return Discovery{}, err
	}
	p.mu.Lock()
	p.discovery, p.readAt = &d, p.real.Now()
	p.mu.Unlock()
	return d, nil
}

// AuthRequest is what the authorization URL carries.
type AuthRequest struct {
	RedirectURI string
	Scopes      []string
	State       string
	Nonce       string
	// Verifier is the PKCE code verifier; the URL carries its S256 challenge.
	Verifier string
}

// AuthURL is the authorization endpoint with the request: the authorization code flow with PKCE S256, state and
// nonce, and the scopes openid and offline_access before the configured ones.
func (p *Provider) AuthURL(d Discovery, r AuthRequest) (string, error) {
	u, err := url.Parse(d.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("the authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.clientID)
	q.Set("redirect_uri", r.RedirectURI)
	q.Set("scope", strings.Join(Scopes(r.Scopes), " "))
	q.Set("state", r.State)
	q.Set("nonce", r.Nonce)
	q.Set("code_challenge", Challenge(r.Verifier))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Scopes are the scopes a sign-in requests: openid and offline_access, then the configured ones without repeats.
func Scopes(configured []string) []string {
	out := []string{"openid", "offline_access"}
	for _, s := range configured {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// Challenge is the PKCE S256 challenge of a verifier (RFC 7636).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Tokens is a successful answer of the token endpoint. The tokens are Secrets.
type Tokens struct {
	IDToken      logging.Secret
	AccessToken  logging.Secret
	RefreshToken logging.Secret
	// Scope is the granted scope, when the answer says it.
	Scope []string
}

type tokenAnswer struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

// Exchange trades an authorization code and its PKCE verifier for tokens at the token endpoint.
func (p *Provider) Exchange(ctx context.Context, d Discovery, code, verifier, redirectURI string) (Tokens, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"redirect_uri": {redirectURI}}
	return p.token(ctx, p.interactive, d, form, "token exchange")
}

// Refresh redeems an offline (refresh) token at the token endpoint through the background class, which retries
// transient answers until ctx ends: the budget of a background re-check (C-03.FR-30).
func (p *Provider) Refresh(ctx context.Context, d Discovery, refreshToken logging.Secret) (Tokens, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {string(refreshToken)}}
	return p.token(ctx, p.background, d, form, "refresh")
}

// token posts form to the token endpoint, authenticating the client with client_secret_basic, or with
// client_secret_post when discovery offers only that, or as a public client without a secret.
func (p *Provider) token(ctx context.Context, c Doer, d Discovery, form url.Values, step string) (Tokens, error) {
	h := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Accept": {"application/json"}}
	form.Set("client_id", p.clientID)
	if p.clientSecret != "" {
		if len(d.TokenAuthMethods) > 0 && !slices.Contains(d.TokenAuthMethods, "client_secret_basic") &&
			slices.Contains(d.TokenAuthMethods, "client_secret_post") {
			form.Set("client_secret", string(p.clientSecret))
		} else {
			h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(
				[]byte(url.QueryEscape(p.clientID)+":"+url.QueryEscape(string(p.clientSecret)))))
		}
	}
	res, err := c.Do(ctx, outbound.Request{Method: http.MethodPost, URL: d.TokenEndpoint, Header: h,
		Body: []byte(form.Encode())})
	var answer tokenAnswer
	_ = json.Unmarshal(res.Body, &answer) // an answer that is not JSON has no error code and no tokens
	if err != nil {
		e := backChannelError(step, err)
		e.Code = oauthCode(answer.Error)
		if e.Code != "" {
			e.msg = fmt.Sprintf("the token endpoint answered %d with the error %s", res.Status, e.Code)
		}
		return Tokens{}, e
	}
	if answer.AccessToken == "" && answer.IDToken == "" {
		return Tokens{}, &BackChannelError{Step: step, Status: res.Status, Outcome: outbound.OutcomeOK,
			msg: "the token endpoint answered without tokens"}
	}
	return Tokens{IDToken: logging.Secret(answer.IDToken), AccessToken: logging.Secret(answer.AccessToken),
		RefreshToken: logging.Secret(answer.RefreshToken), Scope: strings.Fields(answer.Scope)}, nil
}

// oauthCode keeps an OAuth error code only when it has the form of one, since it comes from outside.
func oauthCode(code string) string {
	if code == "" || len(code) > 64 || strings.IndexFunc(code, func(r rune) bool {
		return (r < 'a' || r > 'z') && r != '_'
	}) >= 0 {
		return ""
	}
	return code
}

// UserInfo reads the claims of the userinfo endpoint with the access token.
func (p *Provider) UserInfo(ctx context.Context, d Discovery, accessToken logging.Secret) (map[string]any, error) {
	if d.UserinfoEndpoint == "" || accessToken == "" {
		return nil, nil
	}
	res, err := p.interactive.Do(ctx, outbound.Request{URL: d.UserinfoEndpoint, Header: http.Header{
		"Authorization": {"Bearer " + string(accessToken)}, "Accept": {"application/json"}}})
	if err != nil {
		return nil, backChannelError("userinfo", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(res.Body, &claims); err != nil {
		return nil, &BackChannelError{Step: "userinfo", msg: "the answer is not a JSON object", Status: res.Status,
			Outcome: outbound.OutcomeOK}
	}
	return claims, nil
}

// IDToken is a verified ID token: its standard claims and every claim by name.
type IDToken struct {
	Subject           string
	PreferredUsername string
	Email             string
	Name              string
	AMR               []string
	Claims            map[string]any
}

// ErrNonce is an ID token whose nonce is not the one of its request.
var ErrNonce = errors.New("the ID token carries another nonce")

// TokenError is an ID token that does not verify; it says why without the token.
type TokenError struct {
	Reason string
}

func (e *TokenError) Error() string { return "the ID token does not verify: " + e.Reason }

// Verify checks an ID token: an accepted algorithm and a signature of a key of the provider's key set, the issuer of
// discovery, the client among the audiences (and as azp when there are several or azp is given), exp and iat present,
// and exp, nbf and iat on the real clock with leeway; then the nonce, which must be the one of the request.
func (p *Provider) Verify(ctx context.Context, d Discovery, raw logging.Secret, nonce string) (IDToken, error) {
	out, err := p.verify(ctx, d, raw)
	if err != nil {
		return IDToken{}, err
	}
	if got, _ := out.Claims["nonce"].(string); nonce == "" || got != nonce {
		return IDToken{}, ErrNonce
	}
	return out, nil
}

// VerifyRefreshed checks the ID token of a refresh as Verify does, without a nonce: a refresh has no request of its
// own, and OpenID Connect Core §12.2 lets the token leave the nonce out.
func (p *Provider) VerifyRefreshed(ctx context.Context, d Discovery, raw logging.Secret) (IDToken, error) {
	return p.verify(ctx, d, raw)
}

func (p *Provider) verify(ctx context.Context, d Discovery, raw logging.Secret) (IDToken, error) {
	tok, err := jwt.ParseSigned(string(raw), signatureAlgorithms)
	if err != nil {
		return IDToken{}, &TokenError{Reason: "not a signed JWT with an accepted algorithm"}
	}
	if len(tok.Headers) != 1 {
		return IDToken{}, &TokenError{Reason: "not exactly one signature"}
	}
	hdr := tok.Headers[0]
	keys, err := p.keysFor(ctx, d, hdr.KeyID)
	if err != nil {
		return IDToken{}, err
	}
	var (
		std    jwt.Claims
		claims map[string]any
		ok     bool
	)
	for _, k := range keys {
		if k.Algorithm != "" && k.Algorithm != hdr.Algorithm {
			continue
		}
		if tok.Claims(k.Key, &std, &claims) == nil {
			ok = true
			break
		}
	}
	if !ok {
		return IDToken{}, &TokenError{Reason: "no key of the provider's key set verifies the signature"}
	}
	switch {
	case std.Expiry == nil || std.IssuedAt == nil:
		return IDToken{}, &TokenError{Reason: "exp or iat is missing"}
	case std.Subject == "":
		return IDToken{}, &TokenError{Reason: "sub is missing"}
	}
	if err := std.ValidateWithLeeway(jwt.Expected{Issuer: d.Issuer, AnyAudience: jwt.Audience{p.clientID},
		Time: p.real.Now()}, leeway); err != nil {
		return IDToken{}, &TokenError{Reason: err.Error()}
	}
	azp, _ := claims["azp"].(string)
	if (len(std.Audience) > 1 || azp != "") && azp != p.clientID {
		return IDToken{}, &TokenError{Reason: "azp is not the client"}
	}
	out := IDToken{Subject: std.Subject, Claims: claims}
	out.PreferredUsername, _ = claims["preferred_username"].(string)
	out.Email, _ = claims["email"].(string)
	out.Name, _ = claims["name"].(string)
	out.AMR = stringList(claims["amr"])
	return out, nil
}

// keysFor returns the keys that may have signed a token with the key id kid: the one with that id, or every signing
// key when the token names none. An unknown id fetches the key set again, at most once every keysRefetchAfter, failed
// attempts included, and one fetch at a time; a failed fetch keeps the last good keys.
func (p *Provider) keysFor(ctx context.Context, d Discovery, kid string) ([]jose.JSONWebKey, error) {
	p.fetching.Lock()
	defer p.fetching.Unlock()
	p.mu.Lock()
	keys, at, from, lastErr := p.keys, p.keysAt, p.keysFromAt, p.keysErr
	p.mu.Unlock()
	if from != d.JWKSURI {
		keys = nil
	}
	if found := selectKeys(keys, kid); len(found) > 0 {
		return found, nil
	}
	if !at.IsZero() && p.real.Now().Sub(at) < keysRefetchAfter {
		if keys == nil && lastErr != nil {
			return nil, lastErr
		}
		return nil, &TokenError{Reason: "the key id is not in the provider's key set"}
	}
	fetched, err := p.fetchKeys(ctx, d)
	p.mu.Lock()
	p.keysAt, p.keysErr = p.real.Now(), err
	if err == nil {
		p.keys, p.keysFromAt, keys = fetched, d.JWKSURI, fetched
	}
	p.mu.Unlock()
	if found := selectKeys(keys, kid); len(found) > 0 {
		return found, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, &TokenError{Reason: "the key id is not in the provider's key set"}
}

func (p *Provider) fetchKeys(ctx context.Context, d Discovery) ([]jose.JSONWebKey, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := p.background.Do(ctx, outbound.Request{URL: d.JWKSURI, Header: http.Header{
		"Accept": {"application/json"}}})
	if err != nil {
		return nil, backChannelError("key set", err)
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(res.Body, &set); err != nil {
		return nil, &BackChannelError{Step: "key set", msg: "the answer is not a JSON Web Key Set", Status: res.Status,
			Outcome: outbound.OutcomeOK}
	}
	var keys []jose.JSONWebKey
	for _, k := range set.Keys {
		if k.Valid() && k.IsPublic() && (k.Use == "" || k.Use == "sig") {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func selectKeys(keys []jose.JSONWebKey, kid string) []jose.JSONWebKey {
	if kid == "" {
		return keys
	}
	var out []jose.JSONWebKey
	for _, k := range keys {
		if k.KeyID == kid {
			out = append(out, k)
		}
	}
	return out
}

// stringList reads a claim that is a string or a list of strings; anything else is none.
func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
