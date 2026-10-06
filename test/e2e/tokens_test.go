// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/devmode"
)

// createToken creates a Personal access token narrowed to alert-groups:read from the agent's session and returns its
// value.
func (a *agent) createToken(name string) string {
	a.t.Helper()
	created := a.json(http.MethodPost, "/api/v1/me/personal-access-tokens",
		`{"name":"`+name+`","permissions":["alert-groups:read"]}`, http.StatusCreated)
	value, _ := created["value"].(string)
	if len(value) < 9 || value[:9] != "mstr_pat_" {
		a.t.Fatal("the created token has no mstr_pat_ value")
	}
	return value
}

// withToken sends GET /api/v1/me with the bearer token value and returns the answer.
func withToken(t *testing.T, base, value string) answer {
	t.Helper()
	return bearerGet(t, base+"/api/v1/me", value)
}

// bearerGet sends GET url with the bearer token value and returns the answer.
func bearerGet(t *testing.T, url, value string) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+value)
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{status: resp.StatusCode, header: resp.Header, body: b}
}

func codeOf(t *testing.T, a answer) string {
	t.Helper()
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	decode(t, a, &p)
	return p.Code
}

func (h *Harness) exec(t *testing.T, sql string) {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), h.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err := conn.Exec(t.Context(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// TestTokenOwnerLifecycle is C-04.AC-4 and C-03.FR-13 against `muster dev`: a disabled user's Personal access token
// answers 401 until the user is enabled again; deleting the user revokes it with the reason owner_deleted.
func TestTokenOwnerLifecycle(t *testing.T) {
	h := Start(t, DevProcess)
	base := h.Replicas[0].App
	admin := newAgent(t, base)
	if a := admin.signIn(devAdmin, devAdminPassword); a.status != http.StatusCreated {
		t.Fatalf("the Admin's sign-in = %d %s", a.status, a.body)
	}
	doraID := localUser(t, admin, "dora", "dora-password-1")
	dora := newAgent(t, base)
	if a := dora.signIn("dora", "dora-password-1"); a.status != http.StatusCreated {
		t.Fatalf("dora's sign-in = %d", a.status)
	}
	token := dora.createToken("dora-scripts")
	if a := withToken(t, base, token); a.status != http.StatusOK {
		t.Fatalf("dora's token = %d %s", a.status, a.body)
	}
	admin.json(http.MethodPost, "/api/v1/users/"+doraID+"/disable", "", http.StatusOK)
	if a := withToken(t, base, token); a.status != http.StatusUnauthorized {
		t.Errorf("the token of a disabled user = %d", a.status)
	}
	admin.json(http.MethodPost, "/api/v1/users/"+doraID+"/enable", "", http.StatusOK)
	if a := withToken(t, base, token); a.status != http.StatusOK {
		t.Errorf("the token of an enabled user = %d", a.status)
	}
	admin.json(http.MethodDelete, "/api/v1/users/"+doraID, "", http.StatusNoContent)
	if a := withToken(t, base, token); a.status != http.StatusUnauthorized || codeOf(t, a) != "invalid_credentials" {
		t.Errorf("the token of a deleted user = %d %s", a.status, a.body)
	}
	if n := h.count(t, `SELECT count(*) FROM api_tokens WHERE name = 'dora-scripts'
		AND revoked_reason = 'owner_deleted' AND revoked_at IS NOT NULL`); n != 1 {
		t.Errorf("%d tokens of the deleted user revoked with owner_deleted", n)
	}
}

// TestTokenOIDCRecheck is C-03.AC-22 and C-04.AC-9 against `muster dev`: an OIDC user with an offline token keeps a
// working token while the re-checks succeed; after the fake IdP disables her, the re-check that falls due refuses
// her and her token answers 401 oidc_recheck_required, and after her next OIDC sign-in the same token works. The
// development clock arrives with a later story, so the re-check is made due in the database.
func TestTokenOIDCRecheck(t *testing.T) {
	h := Start(t, DevProcess)
	base := h.Replicas[0].App
	idp := "http://" + devmode.OIDCAddr
	nextUser(t, "u-rita", "rita", "oncall")
	rita := newAgent(t, base)
	if to := rita.oidcSignIn(); to != "/" {
		t.Fatalf("rita's sign-in ended at %s", to)
	}
	token := rita.createToken("rita-scripts")
	if n := h.count(t, `SELECT count(*) FROM oidc_checks c JOIN users u ON u.id = c.user_id WHERE u.login = 'rita'`); n !=
		1 {
		t.Fatalf("%d re-checks of rita", n)
	}
	// A re-check that succeeds keeps the token working, however old the sign-in.
	h.exec(t, `UPDATE users SET oidc_last_contact_at = oidc_last_contact_at - interval '30 days' WHERE login = 'rita'`)
	if a := withToken(t, base, token); a.status != http.StatusOK {
		t.Errorf("with an offline token past the grace = %d %s", a.status, a.body)
	}
	if a := call(t, http.MethodPost, idp+"/_fake/users/u-rita/disable", ""); a.status != http.StatusNoContent {
		t.Fatalf("disable at the IdP = %d", a.status)
	}
	h.exec(t, `UPDATE oidc_checks SET deadline = now() - interval '1 second'
		WHERE user_id = (SELECT id FROM users WHERE login = 'rita')`)
	var a answer
	h.Replicas[0].waitFor(t, "the re-check to refuse rita", func() bool {
		a = withToken(t, base, token)
		return a.status == http.StatusUnauthorized
	})
	if codeOf(t, a) != "oidc_recheck_required" {
		t.Fatalf("after the refusal = %d %s", a.status, a.body)
	}
	if s := rita.do(http.MethodGet, "/api/v1/me", ""); s.status != http.StatusUnauthorized {
		t.Errorf("rita's session after the refusal = %d", s.status)
	}
	if n := h.count(t, `SELECT count(*) FROM api_tokens WHERE name = 'rita-scripts' AND revoked_at IS NULL`); n != 1 {
		t.Error("the token was revoked")
	}
	call(t, http.MethodPost, idp+"/_fake/users/u-rita/enable", "")
	nextUser(t, "u-rita", "rita", "oncall")
	if to := newAgent(t, base).oidcSignIn(); to != "/" {
		t.Fatalf("rita's next sign-in ended at %s", to)
	}
	if a := withToken(t, base, token); a.status != http.StatusOK {
		t.Errorf("after the next OIDC sign-in = %d %s", a.status, a.body)
	}
}

// TestTokenOIDCGrace is C-04.AC-9 and C-03.AC-24 against `muster dev`: without an offline token, a Personal access
// token answers 401 oidc_recheck_required once auth.oidc_token_grace has passed since the last OIDC sign-in, and works
// again after the next one without being reissued; a Service account token of the same age is not affected.
func TestTokenOIDCGrace(t *testing.T) {
	h := Start(t, DevProcess)
	base := h.Replicas[0].App
	config := "http://" + devmode.OIDCAddr + "/_fake/config"
	if a := call(t, http.MethodPost, config, `{"grant_offline_access":false}`); a.status != http.StatusNoContent {
		t.Fatalf("config = %d", a.status)
	}
	nextUser(t, "u-olga", "olga", "oncall")
	olga := newAgent(t, base)
	if to := olga.oidcSignIn(); to != "/" {
		t.Fatalf("olga's sign-in ended at %s", to)
	}
	token := olga.createToken("olga-scripts")
	admin := newAgent(t, base)
	admin.signIn(devAdmin, devAdminPassword)
	sa := admin.json(http.MethodPost, "/api/v1/service-accounts", `{"name":"terraform","role":"viewer"}`,
		http.StatusCreated)
	sat := admin.json(http.MethodPost, "/api/v1/service-accounts/"+sa["id"].(string)+"/tokens", `{"name":"ci"}`,
		http.StatusCreated)["value"].(string)
	h.exec(t, `UPDATE users SET oidc_last_contact_at = oidc_last_contact_at - interval '8 days' WHERE login = 'olga'`)
	a := withToken(t, base, token)
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	decode(t, a, &p)
	if a.status != http.StatusUnauthorized || p.Code != "oidc_recheck_required" ||
		p.Detail != "Sign in through OIDC to make your tokens work again." {
		t.Errorf("past the grace = %d %s", a.status, a.body)
	}
	if a := bearerGet(t, base+"/api/v1/organization", sat); a.status != http.StatusOK {
		t.Errorf("a Service account token past the grace = %d %s", a.status, a.body)
	}
	nextUser(t, "u-olga", "olga", "oncall")
	if to := newAgent(t, base).oidcSignIn(); to != "/" {
		t.Fatalf("olga's next sign-in ended at %s", to)
	}
	if a := withToken(t, base, token); a.status != http.StatusOK {
		t.Errorf("after the next OIDC sign-in = %d %s", a.status, a.body)
	}
}
