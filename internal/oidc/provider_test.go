// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/fakes/fakeoidc"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

func newProviderEnv(t *testing.T) (*Provider, *fakeoidc.Fake, *clock.Manual, Discovery) {
	t.Helper()
	idp := fakeoidc.New()
	if err := idp.Start(t.Context(), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idp.Close(t.Context()) })
	rc := clock.NewManual(time.Now().UTC().Truncate(time.Second))
	idp.SetClock(rc.Now)
	p, err := NewProvider(network(t, rc, logging.New(&bytes.Buffer{}, logging.LevelInfo)), idp.URL()+"/", "muster",
		"s3cr3t", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return p, idp, rc, d
}

func (c claims) with(k string, v any) claims {
	out := claims{}
	for kk, vv := range c {
		out[kk] = vv
	}
	if v == nil {
		delete(out, k)
	} else {
		out[k] = v
	}
	return out
}

type claims map[string]any

func baseClaims(iss string, now time.Time) claims {
	return claims{"iss": iss, "sub": "u-1", "aud": "muster", "exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(), "nonce": "n-1", "preferred_username": "olga", "amr": []string{"pwd", "otp"},
		"groups": []string{"oncall"}}
}

func sign(t *testing.T, idp *fakeoidc.Fake, c claims) logging.Secret {
	t.Helper()
	raw, err := idp.Sign(map[string]any(c))
	if err != nil {
		t.Fatal(err)
	}
	return logging.Secret(raw)
}

// TestVerify: only a token the provider signed for this client, with its issuer, times and nonce, verifies.
func TestVerify(t *testing.T) {
	p, idp, rc, d := newProviderEnv(t)
	now := rc.Now()
	good := baseClaims(d.Issuer, now)
	idt, err := p.Verify(t.Context(), d, sign(t, idp, good), "n-1")
	if err != nil || idt.Subject != "u-1" || idt.PreferredUsername != "olga" || len(idt.AMR) != 2 {
		t.Fatalf("a good token = %+v, %v", idt, err)
	}
	if _, err := p.Verify(t.Context(), d, sign(t, idp, good), ""); !errors.Is(err, ErrNonce) {
		t.Errorf("without a nonce to compare = %v, want ErrNonce", err)
	}
	if _, err := p.Verify(t.Context(), d, sign(t, idp, good.with("nonce", nil)), "n-1"); !errors.Is(err, ErrNonce) {
		t.Errorf("a token without a nonce = %v, want ErrNonce", err)
	}
	if _, err := p.Verify(t.Context(), d, sign(t, idp, good), "n-2"); !errors.Is(err, ErrNonce) {
		t.Errorf("another nonce = %v", err)
	}
	for name, c := range map[string]claims{
		"another issuer":      good.with("iss", "https://evil.example"),
		"another audience":    good.with("aud", "other"),
		"expired":             good.with("exp", now.Add(-2*time.Minute).Unix()),
		"issued in future":    good.with("iat", now.Add(2*time.Minute).Unix()),
		"not valid yet":       good.with("nbf", now.Add(2*time.Minute).Unix()),
		"no exp":              good.with("exp", nil),
		"no iat":              good.with("iat", nil),
		"no sub":              good.with("sub", nil),
		"azp of another":      good.with("azp", "other"),
		"audiences, no azp":   good.with("aud", []string{"muster", "other"}),
		"audience list wrong": good.with("aud", []string{"other"}),
	} {
		var te *TokenError
		if _, err := p.Verify(t.Context(), d, sign(t, idp, c), "n-1"); !errors.As(err, &te) {
			t.Errorf("%s: err = %v, want a TokenError", name, err)
		}
	}
	if _, err := p.Verify(t.Context(), d, sign(t, idp, good.with("exp", now.Add(-30*time.Second).Unix())), "n-1"); err != nil {
		t.Errorf("a token expired within the leeway: %v", err)
	}
	if _, err := p.Verify(t.Context(), d, sign(t, idp, good.with("aud", []string{"muster", "x"}).with("azp", "muster")),
		"n-1"); err != nil {
		t.Errorf("several audiences with azp: %v", err)
	}

	// Symmetric and unsigned tokens are refused whatever their claims.
	hs, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("muster-client-secret-0123456789")},
		nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := jwt.Signed(hs).Claims(map[string]any(good)).Serialize()
	payload, _ := json.Marshal(map[string]any(good))
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "."
	for name, tok := range map[string]string{"HS256": raw, "none": none, "garbage": "a.b.c"} {
		if _, err := p.Verify(t.Context(), d, logging.Secret(tok), "n-1"); err == nil {
			t.Errorf("%s verified", name)
		}
	}
}

// TestKeyRotation: a token signed with a new key fetches the key set again; an unknown key id fetches it at most once
// every keysRefetchAfter; a failed fetch keeps the last good keys.
func TestKeyRotation(t *testing.T) {
	p, idp, rc, d := newProviderEnv(t)
	good := baseClaims(d.Issuer, rc.Now())
	if _, err := p.Verify(t.Context(), d, sign(t, idp, good), "n-1"); err != nil {
		t.Fatal(err)
	}
	fetches := func() int {
		n := 0
		for _, r := range idp.Requests() {
			if r.Path == "/jwks" {
				n++
			}
		}
		return n
	}
	before := fetches()
	if _, err := p.Verify(t.Context(), d, sign(t, idp, good), "n-1"); err != nil || fetches() != before {
		t.Fatalf("a known key fetched again: %v", err)
	}
	if code := postControl(t, idp.URL()+"/_fake/rotate-keys"); code != http.StatusNoContent {
		t.Fatalf("rotate = %d", code)
	}
	rotated := sign(t, idp, good)
	if _, err := p.Verify(t.Context(), d, rotated, "n-1"); err == nil {
		t.Fatal("a new key within keysRefetchAfter verified without a fetch")
	}
	rc.Advance(keysRefetchAfter)
	if _, err := p.Verify(t.Context(), d, rotated, "n-1"); err != nil || fetches() != before+1 {
		t.Fatalf("after the rotation: %v, fetches %d", err, fetches()-before)
	}
	// The key set fails: the last good keys still verify, an unknown key does not.
	if err := idp.SetFault(fakeserver.Fault{Path: "/jwks", Status: http.StatusNotFound}); err != nil {
		t.Fatal(err)
	}
	rc.Advance(keysRefetchAfter)
	_ = postControl(t, idp.URL()+"/_fake/rotate-keys")
	var be *BackChannelError
	if _, err := p.Verify(t.Context(), d, sign(t, idp, good), "n-1"); !errors.As(err, &be) || be.Step != "key set" {
		t.Errorf("an unknown key while the key set fails = %v", err)
	}
	if _, err := p.Verify(t.Context(), d, rotated, "n-1"); err != nil {
		t.Errorf("the last good keys were dropped: %v", err)
	}
	// A failed fetch counts against keysRefetchAfter: the next unknown key does not fetch again at once.
	failed := fetches()
	_ = postControl(t, idp.URL()+"/_fake/rotate-keys")
	if _, err := p.Verify(t.Context(), d, sign(t, idp, good), "n-1"); err == nil || fetches() != failed {
		t.Errorf("a fetch right after a failed one: %v, fetches %d", err, fetches()-failed)
	}
}

// TestKeySetNeverRead: while the first fetch of the key set fails, verifications answer its error without fetching
// more than once every keysRefetchAfter.
func TestKeySetNeverRead(t *testing.T) {
	p, idp, rc, d := newProviderEnv(t)
	if err := idp.SetFault(fakeserver.Fault{Path: "/jwks", Status: http.StatusNotFound}); err != nil {
		t.Fatal(err)
	}
	tok := sign(t, idp, baseClaims(d.Issuer, rc.Now()))
	var be *BackChannelError
	for range 3 {
		if _, err := p.Verify(t.Context(), d, tok, "n-1"); !errors.As(err, &be) {
			t.Fatalf("err = %v", err)
		}
	}
	n := 0
	for _, r := range idp.Requests() {
		if r.Path == "/jwks" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the key set was fetched %d times", n)
	}
}

func postControl(t *testing.T, u string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// stubDoer answers every request with one result and records the requests.
type stubDoer struct {
	res  outbound.Result
	err  error
	reqs []outbound.Request
}

func (s *stubDoer) Do(_ context.Context, req outbound.Request) (outbound.Result, error) {
	s.reqs = append(s.reqs, req)
	return s.res, s.err
}

func TestFetchDiscoveryRefusesBadMetadata(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":       "<html>",
		"another issuer": `{"issuer":"https://evil.example","authorization_endpoint":"https://i/a","token_endpoint":"https://i/t","jwks_uri":"https://i/k"}`,
		"no token URL":   `{"issuer":"https://i","authorization_endpoint":"https://i/a","jwks_uri":"https://i/k"}`,
	} {
		_, err := FetchDiscovery(t.Context(), &stubDoer{res: outbound.Result{Status: 200, Body: []byte(body)}},
			"https://i")
		var be *BackChannelError
		if !errors.As(err, &be) || be.Step != "discovery" || !errors.Is(err, errBackChannel) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	d, err := FetchDiscovery(t.Context(), &stubDoer{res: outbound.Result{Status: 200, Body: []byte(
		`{"issuer":"https://i","authorization_endpoint":"https://i/a","token_endpoint":"https://i/t",` +
			`"jwks_uri":"https://i/k","userinfo_endpoint":"relative"}`)}}, "https://i/")
	if err != nil || d.UserinfoEndpoint != "" {
		t.Errorf("a relative userinfo endpoint = %+v, %v", d, err)
	}
}

// TestTokenClientAuthentication: client_secret_basic by default, client_secret_post when discovery offers only that,
// and the OAuth error code of a refusal.
func TestTokenClientAuthentication(t *testing.T) {
	d := Discovery{TokenEndpoint: "https://i/t"}
	stub := &stubDoer{res: outbound.Result{Status: 200, Body: []byte(`{"id_token":"x","access_token":"y","scope":"openid offline_access"}`)}}
	p := newProvider("https://i", "client:1", "s e/c", stub, stub, clock.NewManual(t0))
	tokens, err := p.Exchange(t.Context(), d, "code", "verifier", "https://m/cb")
	if err != nil || tokens.IDToken != "x" || len(tokens.Scope) != 2 {
		t.Fatalf("exchange = %+v, %v", tokens, err)
	}
	req := stub.reqs[0]
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape("client:1")+":"+url.QueryEscape("s e/c")))
	form, _ := url.ParseQuery(string(req.Body))
	if req.Header.Get("Authorization") != want || form.Get("client_secret") != "" || form.Get("code_verifier") != "verifier" ||
		form.Get("grant_type") != "authorization_code" || req.Method != http.MethodPost {
		t.Errorf("request = %+v %v", req, form)
	}
	d.TokenAuthMethods = []string{"client_secret_post"}
	if _, err := p.Exchange(t.Context(), d, "code", "verifier", "https://m/cb"); err != nil {
		t.Fatal(err)
	}
	form, _ = url.ParseQuery(string(stub.reqs[1].Body))
	if stub.reqs[1].Header.Get("Authorization") != "" || form.Get("client_secret") != "s e/c" {
		t.Errorf("client_secret_post = %v", form)
	}
	stub.res = outbound.Result{Status: 400, Body: []byte(`{"error":"invalid_grant","error_description":"secret s e/c"}`)}
	stub.err = &outbound.Error{Outcome: outbound.OutcomeFatal, Status: 400}
	_, err = p.Exchange(t.Context(), d, "code", "verifier", "https://m/cb")
	var be *BackChannelError
	if !errors.As(err, &be) || be.Code != "invalid_grant" || be.Status != 400 || strings.Contains(err.Error(), "s e/c") {
		t.Errorf("a refusal = %v", err)
	}
	stub.res, stub.err = outbound.Result{Status: 400, Body: []byte(`{"error":"<script>"}`)}, &outbound.Error{Status: 400}
	if _, err = p.Exchange(t.Context(), d, "c", "v", "u"); !errors.As(err, &be) || be.Code != "" {
		t.Errorf("an error code that is not one = %v", err)
	}
	stub.res, stub.err = outbound.Result{Status: 200, Body: []byte(`{}`)}, nil
	if _, err = p.Exchange(t.Context(), d, "c", "v", "u"); err == nil {
		t.Error("an answer without tokens was accepted")
	}
	public := newProvider("https://i", "public", "", stub, stub, clock.NewManual(t0))
	_, _ = public.Exchange(t.Context(), d, "c", "v", "u")
	last := stub.reqs[len(stub.reqs)-1]
	form, _ = url.ParseQuery(string(last.Body))
	if last.Header.Get("Authorization") != "" || form.Get("client_secret") != "" || form.Get("client_id") != "public" {
		t.Errorf("a public client = %+v", last)
	}
}

func TestUserInfo(t *testing.T) {
	stub := &stubDoer{res: outbound.Result{Status: 200, Body: []byte(`{"sub":"u-1","groups":"one"}`)}}
	p := newProvider("https://i", "c", "", stub, stub, clock.NewManual(t0))
	if info, err := p.UserInfo(t.Context(), Discovery{}, "tok"); info != nil || err != nil {
		t.Errorf("without a userinfo endpoint = %v, %v", info, err)
	}
	d := Discovery{UserinfoEndpoint: "https://i/u"}
	info, err := p.UserInfo(t.Context(), d, "tok")
	if err != nil || len(stringList(info["groups"])) != 1 || stub.reqs[0].Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("userinfo = %v, %v", info, err)
	}
	stub.res = outbound.Result{Status: 200, Body: []byte(`[]`)}
	if _, err := p.UserInfo(t.Context(), d, "tok"); err == nil {
		t.Error("a userinfo that is not an object was accepted")
	}
	stub.err = &outbound.Error{Status: 401}
	if _, err := p.UserInfo(t.Context(), d, "tok"); err == nil {
		t.Error("a refused userinfo was accepted")
	}
	if stringList(42) != nil || stringList("") != nil || len(stringList([]any{"a", 1, ""})) != 1 {
		t.Error("stringList")
	}
}

func TestAuthURLAndScopes(t *testing.T) {
	p := newProvider("https://i", "muster", "", nil, nil, clock.NewManual(t0))
	u, err := p.AuthURL(Discovery{AuthorizationEndpoint: "https://i/auth?kc_idp_hint=x"}, AuthRequest{
		RedirectURI: "https://m/cb", Scopes: []string{"email", "openid", " ", "email"}, State: "s", Nonce: "n",
		Verifier: "v"})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(u)
	got := q.Query()
	if got.Get("kc_idp_hint") != "x" || got.Get("scope") != "openid offline_access email" ||
		got.Get("code_challenge") != Challenge("v") || got.Get("response_type") != "code" || got.Get("client_id") != "muster" {
		t.Errorf("auth URL = %s", u)
	}
	// RFC 7636, appendix B.
	if Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Error("the S256 challenge does not match RFC 7636")
	}
	if _, err := p.AuthURL(Discovery{AuthorizationEndpoint: "%"}, AuthRequest{}); err == nil {
		t.Error("a broken authorization endpoint was accepted")
	}
}
