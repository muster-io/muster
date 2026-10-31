// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/muster-io/muster/internal/auth"
	"github.com/muster-io/muster/internal/fakes/fakeoidc"
	"github.com/muster-io/muster/internal/fakes/fakeserver"
	"github.com/muster-io/muster/internal/oidc/dbgen"
)

var (
	ownerClaim      = fakeoidc.RoleClaim(fakeoidc.RoleOwner)
	maintainerClaim = fakeoidc.RoleClaim(fakeoidc.RoleMaintainer)
)

// fakeMembership records the calls of the Membership.
type fakeMembership struct {
	mu     sync.Mutex
	claims []string
	calls  []membershipCall
	err    error
}

type membershipCall struct {
	userID      int64
	groupsClaim string
	claims      Claims
}

func (f *fakeMembership) EntryClaims(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims, f.err
}

func (f *fakeMembership) SyncMembership(_ context.Context, _ dbgen.DBTX, userID int64, groupsClaim string,
	claims Claims) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, membershipCall{userID: userID, groupsClaim: groupsClaim, claims: claims})
	return f.err
}

func (f *fakeMembership) taken() []membershipCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

// withMembership gives the Service a fake Membership whose Teams read claims.
func (e *env) withMembership(claims ...string) *fakeMembership {
	m := &fakeMembership{claims: claims}
	e.svc.cfg.Membership = m
	return m
}

// userinfoRequests counts the requests the fake IdP's userinfo endpoint got.
func (e *env) userinfoRequests() int {
	return len(slices.DeleteFunc(e.idp.Requests(), func(r fakeserver.Request) bool { return r.Path != "/userinfo" }))
}

func TestValidGroup(t *testing.T) {
	for entry, want := range map[string]bool{
		"a": true, "a/b": true, "a/b/*": true, "example/platform/*": true, "a b/c": true,
		"": false, "*": false, "/*": false, "a*": false, "a/*/b": false, "a/*/*": false, "*/a": false, "a/b*": false,
		"a/b/**": false, "**": false,
	} {
		if got := ValidGroup(entry); got != want {
			t.Errorf("ValidGroup(%q) = %v", entry, got)
		}
	}
}

// TestMatchGroup is C-03.FR-5 and C-25.FR-4: a subtree entry matches its group and every group below it at /
// boundaries only; any other entry matches exactly, case included; an entry that is not valid matches only itself.
func TestMatchGroup(t *testing.T) {
	for _, tc := range []struct {
		entry, group string
		want         bool
	}{
		{"a/b/*", "a/b", true},
		{"a/b/*", "a/b/c", true},
		{"a/b/*", "a/b/c/d", true},
		{"a/b/*", "a/bx", false},
		{"a/b/*", "a/bx/c", false},
		{"a/b/*", "a", false},
		{"a/b/*", "a/", false},
		{"a/b/*", "A/b", false},
		{"a/*", "a/b", true},
		{"a/*", "a/b/c", true},
		{"a/*", "ab", false},
		{"a/b", "a/b", true},
		{"a/b", "a/b/c", false},
		{"a/b", "a/B", false},
		{"a/b", "a/b ", false},
		{"*", "x", false},
		{"*", "*", true},
		{"/*", "/x", false},
		{"a/*/b", "a/x/b", false},
		{"a/*/b", "a/*/b", true},
	} {
		if got := MatchGroup(tc.entry, tc.group); got != tc.want {
			t.Errorf("MatchGroup(%q, %q) = %v", tc.entry, tc.group, got)
		}
	}
}

// TestClaimValues: a claim is a string or a list of strings; anything else, and the items of a list that are not
// strings, count as no values and never as a group, so that a crafted object cannot match an entry.
func TestClaimValues(t *testing.T) {
	var token map[string]any
	if err := json.Unmarshal([]byte(`{"s":"corp","l":["corp/a",7,null,{"corp":true},["corp"],"corp/b",""],
		"o":{"corp":"corp"},"n":42,"b":true,"z":null,"e":[]}`), &token); err != nil {
		t.Fatal(err)
	}
	idt := IDToken{Claims: token}
	claims := presentClaims(idt, []string{"s", "l", "o", "n", "b", "z", "e", "missing"})
	want := map[string]string{"s": "[corp]", "l": "[corp/a corp/b]", "o": "[]", "n": "[]", "b": "[]", "z": "[]",
		"e": "[]"}
	if len(claims) != len(want) {
		t.Errorf("claims = %v", claims)
	}
	for k, v := range want {
		if got := fmt.Sprint(claims[k]); got != v || claims[k] == nil {
			t.Errorf("%s = %s, want %s", k, got, v)
		}
	}
	if claims.Has("missing") || claims.Matches("o", "corp") || claims.Matches("n", "42") ||
		!claims.Matches("l", "corp/*") || claims.Matches("l", "corp") {
		t.Errorf("matches over %v", claims)
	}
}

// TestMapRoleOverClaims is C-03.FR-5: each entry reads its own claim, by default the groups claim, compared by its
// exact name; the highest Role over all entries wins; a claim that was not decided matches nothing.
func TestMapRoleOverClaims(t *testing.T) {
	entries := []GroupMapping{
		{Group: "corp/*", Role: auth.RoleResponder},
		{Claim: ownerClaim, Group: "corp", Role: auth.RoleAdmin},
		{Claim: "groups", Group: "viewers", Role: auth.RoleViewer},
	}
	for _, tc := range []struct {
		name   string
		claims Claims
		role   string
		ok     bool
	}{
		{"an Owner of corp", Claims{"groups": {"corp", "corp/team-a"}, ownerClaim: {"corp"}}, auth.RoleAdmin, true},
		{"a Developer of corp/team-a", Claims{"groups": {"corp", "corp/team-a"}, ownerClaim: {}}, auth.RoleResponder,
			true},
		{"an Owner of corp/team-a only", Claims{"groups": {"corp/team-a"}, ownerClaim: {"corp/team-a"}},
			auth.RoleResponder, true},
		{"outside corp", Claims{"groups": {"other"}, ownerClaim: {}}, "", false},
		{"corp in another claim", Claims{"groups": {"x"}, "Groups": {"corp"}, maintainerClaim: {"corp"}}, "", false},
		{"an explicit groups claim", Claims{"groups": {"viewers"}}, auth.RoleViewer, true},
		{"the owner claim not read", Claims{"groups": {"x"}}, "", false},
	} {
		if role, ok := MapRole(tc.claims, "groups", entries, UnmatchedNone); role != tc.role || ok != tc.ok {
			t.Errorf("%s: %s %v", tc.name, role, ok)
		}
	}
	if role, _ := MapRole(Claims{"roles": {"corp/x"}}, "roles", entries, UnmatchedNone); role != auth.RoleResponder {
		t.Errorf("an entry without a claim reads the groups claim of the settings: %s", role)
	}
}

// TestLegacyMappings is the rolling upgrade from 0.1 (C-03.FR-5 revised for L2): group_mappings keeps only the entries
// of the groups claim with an exact group, and a read takes group_mappings instead of role_mappings once a replica of
// 0.1 saved another mapping beside them.
func TestLegacyMappings(t *testing.T) {
	entries := []GroupMapping{
		{Claim: ownerClaim, Group: "example/platform/infra", Role: auth.RoleAdmin},
		{Group: "example/platform/*", Role: auth.RoleViewer},
		{Group: "oncall", Role: auth.RoleResponder},
		{Claim: "groups", Group: "admins", Role: auth.RoleAdmin},
		{Claim: "roles", Group: "admins", Role: auth.RoleAdmin},
	}
	want := []GroupMapping{{Group: "oncall", Role: auth.RoleResponder}, {Group: "admins", Role: auth.RoleAdmin}}
	if got := legacyMappings(entries, "groups"); !slices.Equal(got, want) {
		t.Errorf("legacyMappings = %v", got)
	}
	roles, _ := json.Marshal(entries)
	legacy, _ := json.Marshal(want)
	got, err := roleMappingsOf(dbgen.OidcSetting{GroupsClaim: "groups", RoleMappings: roles, GroupMappings: legacy})
	if err != nil || !slices.Equal(got, entries) {
		t.Errorf("a row 0.2 wrote = %v, %v", got, err)
	}
	saved := []byte(`[{"group":"oncall","role":"viewer"}]`)
	got, err = roleMappingsOf(dbgen.OidcSetting{GroupsClaim: "groups", RoleMappings: roles, GroupMappings: saved})
	if err != nil || !slices.Equal(got, []GroupMapping{{Group: "oncall", Role: auth.RoleViewer}}) {
		t.Errorf("a row a 0.1 replica saved = %v, %v", got, err)
	}
	got, err = roleMappingsOf(dbgen.OidcSetting{GroupsClaim: "idp-groups", RoleMappings: roles,
		GroupMappings: legacy})
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("a groups claim a 0.1 replica changed = %v, %v", got, err)
	}
	got, err = roleMappingsOf(dbgen.OidcSetting{GroupsClaim: "groups", GroupMappings: []byte(`[]`)})
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("an empty row = %#v, %v", got, err)
	}
	for _, bad := range []dbgen.OidcSetting{{RoleMappings: []byte(`{`), GroupMappings: []byte(`[]`)},
		{RoleMappings: []byte(`[]`), GroupMappings: []byte(`{`)}} {
		if _, err := roleMappingsOf(bad); err == nil {
			t.Errorf("%s / %s read", bad.RoleMappings, bad.GroupMappings)
		}
	}
}

func TestAuditClaims(t *testing.T) {
	claims, names := Claims{}, []string{}
	for i := range maxAuditClaims + 5 {
		n := fmt.Sprintf("c%02d", i)
		names, claims[n] = append(names, n), []string{strings.Repeat("g", 300)}
	}
	got := auditClaims(claims, names)
	if len(got) != maxAuditClaims || len(got["c00"][0]) != maxAuditGroupBytes {
		t.Errorf("auditClaims = %d claims", len(got))
	}
	if got := auditClaims(Claims{}, []string{"groups"}); got["groups"] == nil {
		t.Errorf("a claim not carried = %#v", got)
	}
}

// configureGitLab saves the entries of AC-28 against the fake IdP in its GitLab mode: the owner claim example/platform/*
// → Admin and the groups claim example/platform/* → Viewer, nobody else.
func (e *env) configureGitLab(t *testing.T) {
	t.Helper()
	e.useGitLab()
	e.configure(t, func(in *Input) {
		in.GroupMappings = []GroupMapping{{Claim: ownerClaim, Group: "example/platform/*", Role: auth.RoleAdmin},
			{Group: "example/platform/*", Role: auth.RoleViewer}}
	})
}

// TestRoleClaimsFromUserinfo is C-03.FR-5 and AC-28 against the fake IdP in its GitLab mode, which puts the role claims
// and groups in userinfo alone (F-117): an Owner of example/platform, whose direct-only claim lists just that group
// (F-118), and an Owner of example/platform/infra become Admins, a person in example/platform/apps a Viewer; a person in
// example/platformx is refused, and the refusal lists the values of both claims (C-03.FR-7).
func TestRoleClaimsFromUserinfo(t *testing.T) {
	e := newEnv(t)
	e.configureGitLab(t)
	for _, tc := range []struct {
		user fakeoidc.User
		role string
	}{
		{fakeoidc.User{Subject: "g-1", PreferredUsername: "ann", Groups: []string{"example/platform",
			"example/platform/infra"}, GroupsDirect: []string{"example/platform"},
			RoleClaims: map[string][]string{fakeoidc.RoleOwner: {"example/platform"}}}, auth.RoleAdmin},
		{fakeoidc.User{Subject: "g-2", PreferredUsername: "ben", Groups: []string{"example/platform/infra"},
			RoleClaims: map[string][]string{fakeoidc.RoleOwner: {"example/platform/infra"}}}, auth.RoleAdmin},
		{fakeoidc.User{Subject: "g-3", PreferredUsername: "cid", Groups: []string{"example/platform/apps"},
			RoleClaims: map[string][]string{fakeoidc.RoleDeveloper: {"example/platform/apps"}}}, auth.RoleViewer},
	} {
		if out := e.signIn(t, tc.user, ""); out.Session == nil {
			t.Fatalf("%s = %+v; log %s", tc.user.PreferredUsername, out, e.log)
		}
		if got := e.user(tc.user.PreferredUsername).role; got != tc.role {
			t.Errorf("%s = %s, want %s", tc.user.PreferredUsername, got, tc.role)
		}
	}
	out := e.signIn(t, fakeoidc.User{Subject: "g-4", PreferredUsername: "dan", Groups: []string{"example/platformx"},
		RoleClaims: map[string][]string{fakeoidc.RoleOwner: {"example/platformx"}}}, "")
	if out.Error != ErrorNoAccess || e.user("dan") != nil {
		t.Fatalf("example/platformx = %+v", out)
	}
	refused := e.store.audited(ActionSignInRefused)
	got, _ := json.Marshal(refused[len(refused)-1]["details"].(map[string]any)["claims"])
	if string(got) != `{"groups":["example/platformx"],"https://gitlab.org/claims/groups/owner":["example/platformx"]}` {
		t.Errorf("the refusal's claims = %s", got)
	}
	st, err := e.svc.Get(t.Context())
	if err != nil || !slices.Equal(st.ClaimsSeen, []string{"groups", ownerClaim}) || !st.GroupsClaimSeen {
		t.Errorf("claims seen = %v, %v", st.ClaimsSeen, err)
	}
}

// TestRoleClaimsFromIDToken: a claim the ID token carries, as the Keycloak flavor puts them, is read the same way and
// without a call of userinfo; a claim the ID token lacks is read from userinfo with one call.
func TestRoleClaimsFromIDToken(t *testing.T) {
	e := newEnv(t)
	e.configure(t, func(in *Input) {
		in.GroupMappings = []GroupMapping{{Claim: ownerClaim, Group: "example/platform/*", Role: auth.RoleAdmin},
			{Group: "example/platform/*", Role: auth.RoleViewer}}
	})
	e.idp.ResetRequests()
	e.signIn(t, fakeoidc.User{Subject: "k-1", PreferredUsername: "ann", Groups: []string{"example/platform"},
		RoleClaims: map[string][]string{fakeoidc.RoleOwner: {"example/platform"}}}, "")
	if e.user("ann").role != auth.RoleAdmin || e.userinfoRequests() != 0 {
		t.Errorf("claims in the ID token: %s, %d userinfo calls", e.user("ann").role, e.userinfoRequests())
	}
	e.signIn(t, fakeoidc.User{Subject: "k-2", PreferredUsername: "cid", Groups: []string{"example/platform/apps"}}, "")
	if e.user("cid").role != auth.RoleViewer || e.userinfoRequests() != 1 {
		t.Errorf("a claim the ID token lacks: %s, %d userinfo calls", e.user("cid").role, e.userinfoRequests())
	}
}
