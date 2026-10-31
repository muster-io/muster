// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/oidc"
	"github.com/muster-io/muster/internal/teams/dbgen"
)

const maintainer = "https://gitlab.org/claims/groups/maintainer"

// idpTeam creates a Team with the entries.
func idpTeamOf(t *testing.T, svc *Service, name string, entries ...IdpEntry) Team {
	t.Helper()
	team, err := svc.Create(t.Context(), by, Input{Name: name, Slug: strings.ToLower(name), IdpEntries: &entries})
	if err != nil {
		t.Fatal(err)
	}
	return team
}

// sources lists the sources of the User userID in the Team, nil when they are not in it, and the entries that matched.
func sources(s *fakeStore, teamID, userID int64) ([]string, string) {
	m := s.member(teamID, userID)
	if m == nil {
		return nil, ""
	}
	return sourcesOf(m.ByHand, m.ByIdp), string(m.IdpMatches)
}

// idpAudits are the Audit log entries of action by the system, as "<team public id> <user> <source> <claim> <group>
// <before>→<after>".
func idpAudits(t *testing.T, s *fakeStore, action string) []string {
	t.Helper()
	var out []string
	for _, a := range s.audit {
		if a.Action != action || a.ActorKind != string(audit.ActorSystem) {
			continue
		}
		var d struct {
			User   string            `json:"user"`
			Source string            `json:"source"`
			Entry  map[string]string `json:"entry"`
		}
		var diff []struct {
			Before *personView `json:"before"`
			After  *personView `json:"after"`
		}
		if err := json.Unmarshal(a.Details, &d); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(a.Diff, &diff); err != nil || len(diff) != 1 {
			t.Fatalf("diff %s, %v", a.Diff, err)
		}
		view := func(v *personView) string {
			if v == nil {
				return "-"
			}
			return strings.Join(v.Sources, ",")
		}
		out = append(out, fmt.Sprintf("%s %s %s %s %s %s→%s", a.ResourcePublicID.String, d.User, d.Source,
			d.Entry["claim"], d.Entry["group"], view(diff[0].Before), view(diff[0].After)))
	}
	return out
}

// TestSyncMembership is C-25.FR-4 and C-25.AC-3: teams/payments/* takes in a User with teams/payments and one with
// teams/payments/oncall, not one with teams/paymentsx; when the second User's groups change, the next sync takes them
// out; a person added by hand who also matches has both sources and keeps by_hand when the entry no longer matches;
// each change by the identity provider is recorded by the system with the source idp and the entry, with the hint team.
func TestSyncMembership(t *testing.T) {
	svc, s := newService(t)
	s.users["SR0000000000PP"] = dbgen.LockTeamUserRow{ID: 4, PublicID: "SR0000000000PP", Name: "petr", Login: "petr",
		Role: "responder", Status: "active"}
	pay := idpTeamOf(t, svc, "Payments", IdpEntry{Group: "teams/payments/*"})
	other := idpTeamOf(t, svc, "Other", IdpEntry{Claim: maintainer, Group: "teams/other"})
	sync := func(userID int64, claims oidc.Claims) {
		t.Helper()
		if err := svc.SyncMembership(t.Context(), nil, userID, "groups", claims); err != nil {
			t.Fatal(err)
		}
	}

	sync(2, oidc.Claims{"groups": {"teams/payments"}, maintainer: {}})
	sync(4, oidc.Claims{"groups": {"teams/payments/oncall"}, maintainer: {}})
	sync(3, oidc.Claims{"groups": {"teams/paymentsx"}, maintainer: {}})
	for id, want := range map[int64]string{2: "[idp]", 4: "[idp]", 3: "[]"} {
		if got, _ := sources(s, pay.ID, id); fmt.Sprint(got) != want {
			t.Errorf("user %d: %v, want %s", id, got, want)
		}
	}
	if _, matches := sources(s, pay.ID, 4); matches != `[{"group":"teams/payments/*"}]` {
		t.Errorf("the entries that matched = %s", matches)
	}
	people, err := svc.Users(t.Context(), pay.PublicID)
	if err != nil || len(people) != 2 || !slices.Equal(people[0].Sources, []string{SourceIdp}) ||
		!slices.Equal(people[0].IdpEntries, []IdpEntry{{Group: "teams/payments/*"}}) {
		t.Fatalf("listTeamUsers = %+v, %v", people, err)
	}
	added := idpAudits(t, s, ActionUserAdded)
	if !slices.Equal(added, []string{pay.PublicID + " SR0000000000BB idp groups teams/payments/* -→idp",
		pay.PublicID + " SR0000000000PP idp groups teams/payments/* -→idp"}) {
		t.Errorf("team.user_added = %q", added)
	}

	// A person added by hand who also matches has both sources; the identity provider never takes by_hand away.
	if _, _, err := svc.AddUser(t.Context(), by, other.PublicID, "SR0000000000PP"); err != nil {
		t.Fatal(err)
	}
	sync(4, oidc.Claims{"groups": {"teams/other"}, maintainer: {"teams/other"}})
	if got, _ := sources(s, other.ID, 4); !slices.Equal(got, []string{SourceByHand, SourceIdp}) {
		t.Errorf("by hand and matching = %v", got)
	}
	if got, _ := sources(s, pay.ID, 4); got != nil {
		t.Errorf("petr is still in Payments: %v", got)
	}
	sync(4, oidc.Claims{"groups": {}, maintainer: {}})
	if got, matches := sources(s, other.ID, 4); !slices.Equal(got, []string{SourceByHand}) || matches != "[]" {
		t.Errorf("by hand after the entry stopped matching = %v %s", got, matches)
	}
	removed := idpAudits(t, s, ActionUserRemoved)
	if !slices.Equal(removed, []string{pay.PublicID + " SR0000000000PP idp groups teams/payments/* idp→-",
		other.PublicID + " SR0000000000PP idp " + maintainer + " teams/other by_hand,idp→by_hand"}) {
		t.Errorf("team.user_removed = %q", removed)
	}
	if !slices.Contains(s.hints, db.Hint{OrgID: orgID, Type: Hint, ID: other.PublicID}) {
		t.Errorf("hints = %v", s.hints)
	}
}

// TestSyncUndecidedClaims is C-03.FR-30 at a re-check: an entry whose claim the refreshed ID token lacks is not
// decided, so a person it matched before stays in the Team with it; the entries that are decided still apply, and a
// person matched only by entries that no longer match leaves.
func TestSyncUndecidedClaims(t *testing.T) {
	svc, s := newService(t)
	both := idpTeamOf(t, svc, "Both", IdpEntry{Group: "oncall"}, IdpEntry{Claim: maintainer, Group: "teams/x"})
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups",
		oidc.Claims{"groups": {"oncall"}, maintainer: {"teams/x"}}); err != nil {
		t.Fatal(err)
	}
	if _, m := sources(s, both.ID, 2); m != `[{"group":"oncall"},{"claim":"`+maintainer+`","group":"teams/x"}]` {
		t.Fatalf("both entries = %s", m)
	}
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", oidc.Claims{"groups": {}}); err != nil {
		t.Fatal(err)
	}
	if got, m := sources(s, both.ID, 2); !slices.Equal(got, []string{SourceIdp}) ||
		m != `[{"claim":"`+maintainer+`","group":"teams/x"}]` {
		t.Errorf("the maintainer claim not read: %v %s", got, m)
	}
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", oidc.Claims{maintainer: {}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := sources(s, both.ID, 2); got != nil {
		t.Errorf("no entry matches any more: %v", got)
	}
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", oidc.Claims{"roles": {"oncall"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := sources(s, both.ID, 2); got != nil {
		t.Errorf("an undecided claim took the person in: %v", got)
	}
}

// TestSyncChangedEntries is C-25.FR-4: a change of a Team's entries applies at the next sync — a person whom no entry
// of the Team matches any more leaves it, one whom another entry matches stays with the new matches, and a deleted
// Team is left alone.
func TestSyncChangedEntries(t *testing.T) {
	svc, s := newService(t)
	team := idpTeamOf(t, svc, "Payments", IdpEntry{Group: "a"}, IdpEntry{Group: "b"})
	gone := idpTeamOf(t, svc, "Gone", IdpEntry{Group: "a"})
	claims := oidc.Claims{"groups": {"a", "b"}}
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", claims); err != nil {
		t.Fatal(err)
	}
	version := team.Version
	if _, err := svc.Update(t.Context(), by, team.PublicID, &version, Input{Name: team.Name, Slug: team.Slug,
		IdpEntries: &[]IdpEntry{{Group: "b"}}}); err != nil {
		t.Fatal(err)
	}
	if _, m := sources(s, team.ID, 2); m != `[{"group":"a"},{"group":"b"}]` {
		t.Errorf("a change of the entries changed a membership at once: %s", m)
	}
	if err := svc.Delete(t.Context(), by, gone.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	writes := len(s.audit)
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", claims); err != nil {
		t.Fatal(err)
	}
	if _, m := sources(s, team.ID, 2); m != `[{"group":"b"}]` || len(s.audit) != writes {
		t.Errorf("the next sync: %s, %d entries", m, len(s.audit)-writes)
	}
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", claims); err != nil || len(s.audit) != writes {
		t.Errorf("a sync that changes nothing wrote %d entries, %v", len(s.audit)-writes, err)
	}
	empty := []IdpEntry{}
	version = team.Version + 1
	if _, err := svc.Update(t.Context(), by, team.PublicID, &version, Input{Name: team.Name, Slug: team.Slug,
		IdpEntries: &empty}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", claims); err != nil {
		t.Fatal(err)
	}
	if got, _ := sources(s, team.ID, 2); got != nil {
		t.Errorf("a Team without entries kept the person: %v", got)
	}
}

// TestSyncFailures: a failing query fails the sync, so that the sign-in's transaction rolls back.
func TestSyncFailures(t *testing.T) {
	for _, q := range []string{"ListIdpEntries", "LockUserMemberships", "JoinThroughIdp", "GetMember",
		"InsertAuditEntry", "Notify"} {
		svc, s := newService(t)
		idpTeamOf(t, svc, "Payments", IdpEntry{Group: "a"})
		s.fail[q] = errBoom
		if err := svc.SyncMembership(t.Context(), nil, 2, "groups", oidc.Claims{"groups": {"a"}}); !errors.Is(err,
			errBoom) {
			t.Errorf("%s: %v", q, err)
		}
	}
	for _, q := range []string{"SetIdpMatches", "DeleteTeamUser", "LeaveThroughIdp"} {
		svc, s := newService(t)
		team := idpTeamOf(t, svc, "Payments", IdpEntry{Group: "a"}, IdpEntry{Group: "b"})
		if q == "LeaveThroughIdp" {
			if _, _, err := svc.AddUser(t.Context(), by, team.PublicID, "SR0000000000BB"); err != nil {
				t.Fatal(err)
			}
		}
		if err := svc.SyncMembership(t.Context(), nil, 2, "groups", oidc.Claims{"groups": {"a", "b"}}); err != nil {
			t.Fatal(err)
		}
		s.fail[q] = errBoom
		claims := oidc.Claims{"groups": {}}
		if q == "SetIdpMatches" {
			claims = oidc.Claims{"groups": {"a"}}
		}
		if err := svc.SyncMembership(t.Context(), nil, 2, "groups", claims); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", q, err)
		}
	}
	svc, s := newService(t)
	team := idpTeamOf(t, svc, "Payments", IdpEntry{Group: "a"})
	s.people[team.ID] = []dbgen.GetTeamUserRow{{ID: 2, PublicID: "SR0000000000BB", ByIdp: true,
		IdpMatches: []byte(`{`)}}
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", oidc.Claims{"groups": {}}); err == nil {
		t.Error("unreadable matches were read")
	}
	delete(s.users, "SR0000000000BB")
	s.people[team.ID] = nil
	if err := svc.SyncMembership(t.Context(), nil, 2, "groups", oidc.Claims{"groups": {"a"}}); err == nil {
		t.Error("a deleted user joined")
	}
	if got, err := svc.EntryClaims(t.Context()); err != nil || !slices.Equal(got, []string{""}) {
		t.Errorf("EntryClaims = %v, %v", got, err)
	}
	s.fail["ListIdpEntryClaims"] = errBoom
	if _, err := svc.EntryClaims(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("EntryClaims = %v", err)
	}
}
