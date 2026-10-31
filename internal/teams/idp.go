// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/oidc"
	oidcdb "github.com/muster-io/muster/internal/oidc/dbgen"
	"github.com/muster-io/muster/internal/teams/dbgen"
)

// SourceIdp is how a person whom an identity provider entry of the Team matched is in it (C-25.FR-4).
const SourceIdp = "idp"

// The limits of the identity provider entries of a Team.
const (
	MaxIdpEntries     = 100
	MaxIdpEntryLength = 200
)

// IdpEntry is an identity provider entry of a Team: a group, or a subtree a/b/*, of a claim, by default the groups
// claim of the OIDC settings, matched as the entries of the Role mapping are (C-25.FR-4, C-03.FR-5).
type IdpEntry struct {
	Claim string `json:"claim,omitempty"`
	Group string `json:"group"`
}

// checkIdpEntries refuses entries that are not valid, at the pointer of the field, and returns them trimmed.
func checkIdpEntries(entries []IdpEntry) ([]IdpEntry, error) {
	if len(entries) > MaxIdpEntries {
		return nil, &FieldError{Pointer: "/idp_entries", Code: CodeTooLong,
			Detail: fmt.Sprintf("There are more than %d identity provider entries.", MaxIdpEntries)}
	}
	out := make([]IdpEntry, 0, len(entries))
	for i, e := range entries {
		e.Claim, e.Group = strings.TrimSpace(e.Claim), strings.TrimSpace(e.Group)
		pointer := fmt.Sprintf("/idp_entries/%d", i)
		switch {
		case len(e.Claim) > MaxIdpEntryLength:
			return nil, &FieldError{Pointer: pointer + "/claim", Code: CodeTooLong,
				Detail: fmt.Sprintf("The claim is longer than %d characters.", MaxIdpEntryLength)}
		case !oidc.ValidGroup(e.Group):
			return nil, &FieldError{Pointer: pointer + "/group", Code: CodeInvalidFormat,
				Detail: "The group is empty, or has a * other than a trailing /*."}
		case len(e.Group) > MaxIdpEntryLength:
			return nil, &FieldError{Pointer: pointer + "/group", Code: CodeTooLong,
				Detail: fmt.Sprintf("The group is longer than %d characters.", MaxIdpEntryLength)}
		case slices.Contains(out, e):
			return nil, &FieldError{Pointer: pointer, Code: CodeDuplicate, Detail: "The entry is listed twice."}
		}
		out = append(out, e)
	}
	return out, nil
}

// idpEntries reads the entries of the Teams ids, by Team.
func (s *Service) idpEntries(ctx context.Context, q Queries, ids []int64) (map[int64][]IdpEntry, error) {
	out := make(map[int64][]IdpEntry, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListTeamIdpEntries(ctx, dbgen.ListTeamIdpEntriesParams{OrgID: s.orgID, TeamIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list the identity provider entries of the teams: %w", err)
	}
	for _, r := range rows {
		out[r.TeamID] = append(out[r.TeamID], IdpEntry{Claim: r.Claim.String, Group: r.GroupName})
	}
	return out, nil
}

// replaceIdpEntries replaces the entries of the Team as a whole.
func (s *Service) replaceIdpEntries(ctx context.Context, q Queries, teamID int64, entries []IdpEntry) error {
	if err := q.DeleteTeamIdpEntries(ctx, dbgen.DeleteTeamIdpEntriesParams{OrgID: s.orgID, TeamID: teamID}); err != nil {
		return fmt.Errorf("remove the identity provider entries of the team: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	claims, groups := make([]string, len(entries)), make([]string, len(entries))
	for i, e := range entries {
		claims[i], groups[i] = e.Claim, e.Group
	}
	if err := q.InsertTeamIdpEntries(ctx, dbgen.InsertTeamIdpEntriesParams{OrgID: s.orgID, TeamID: teamID,
		Claims: claims, GroupNames: groups}); err != nil {
		return fmt.Errorf("store the identity provider entries of the team: %w", err)
	}
	return nil
}

// EntryClaims lists the claims the entries of the Teams read, the empty string for the groups claim; it is half of the
// Membership of internal/oidc.
func (s *Service) EntryClaims(ctx context.Context) ([]string, error) {
	claims, err := s.store.ListIdpEntryClaims(ctx, s.orgID)
	if err != nil {
		return nil, fmt.Errorf("list the claims of the identity provider entries: %w", err)
	}
	return claims, nil
}

// idpTeam is a Team with identity provider entries, or one the User is in through the identity provider: its entries,
// the row of the User in it, if any, and the entries that matched them at their last sign-in or re-check.
type idpTeam struct {
	publicID, name string
	entries        []IdpEntry
	member         *dbgen.LockUserMembershipsRow
	before         []IdpEntry
}

// SyncMembership follows the identity provider entries of the Teams at an OIDC sign-in, a link or a successful
// re-check (C-25.FR-4, C-03.FR-30), in the transaction tx of that success, which holds the row of the User locked; it
// is the other half of the Membership of internal/oidc. The User joins each Team with an entry that matches the claims,
// with the source idp beside by_hand for a person added by hand, and leaves each Team they are in through the identity
// provider when no entry matches any more: a person added by hand keeps by_hand. Only the entries whose claim is in
// claims are decided; an entry that matched before and whose claim was not read keeps the person in. Each join and
// leave is recorded as team.user_added or team.user_removed by the system with the source idp and the entry, with the
// live hint team. It never changes a Role.
func (s *Service) SyncMembership(ctx context.Context, tx oidcdb.DBTX, userID int64, groupsClaim string,
	claims oidc.Claims) error {
	q := s.store.On(tx)
	entries, err := q.ListIdpEntries(ctx, s.orgID)
	if err != nil {
		return fmt.Errorf("list the identity provider entries: %w", err)
	}
	rows, err := q.LockUserMemberships(ctx, dbgen.LockUserMembershipsParams{OrgID: s.orgID, UserID: userID})
	if err != nil {
		return fmt.Errorf("lock the teams of the user %d: %w", userID, err)
	}
	teams := map[int64]*idpTeam{}
	var order []int64
	team := func(id int64, publicID, name string) *idpTeam {
		t, ok := teams[id]
		if !ok {
			t = &idpTeam{publicID: publicID, name: name}
			teams[id] = t
			order = append(order, id)
		}
		return t
	}
	for _, e := range entries {
		t := team(e.TeamID, e.PublicID, e.Name)
		t.entries = append(t.entries, IdpEntry{Claim: e.Claim.String, Group: e.GroupName})
	}
	for i := range rows {
		r := &rows[i]
		if t, ok := teams[r.TeamID]; ok {
			t.member = r
		} else if r.ByIdp {
			team(r.TeamID, r.PublicID, r.Name).member = r
		}
	}
	slices.Sort(order)
	var user *dbgen.GetMemberRow
	for _, id := range order {
		t := teams[id]
		matches, err := t.matches(groupsClaim, claims)
		if err != nil {
			return err
		}
		if err := s.follow(ctx, q, id, t, userID, groupsClaim, matches, &user); err != nil {
			return err
		}
	}
	return nil
}

// matches are the entries of the Team that match the claims, and those that matched the person before and whose claim
// was not read, as long as the Team still has them, in the order of the Team.
func (t *idpTeam) matches(groupsClaim string, claims oidc.Claims) ([]IdpEntry, error) {
	t.before = []IdpEntry{}
	if t.member != nil && t.member.ByIdp {
		if err := json.Unmarshal(t.member.IdpMatches, &t.before); err != nil {
			return nil, fmt.Errorf("read the entries that matched in the team %s: %w", t.publicID, err)
		}
	}
	out := []IdpEntry{}
	for _, e := range t.entries {
		claim := claimOf(e, groupsClaim)
		if claims.Has(claim) && claims.Matches(claim, e.Group) || !claims.Has(claim) && slices.Contains(t.before, e) {
			out = append(out, e)
		}
	}
	return out, nil
}

// claimOf is the claim an entry reads.
func claimOf(e IdpEntry, groupsClaim string) string {
	if e.Claim == "" {
		return groupsClaim
	}
	return e.Claim
}

// follow makes the membership of the User in the Team t what matches says: in it through the identity provider with
// those entries, or not through it when there are none.
func (s *Service) follow(ctx context.Context, q Queries, teamID int64, t *idpTeam, userID int64, groupsClaim string,
	matches []IdpEntry, user **dbgen.GetMemberRow) error {
	m := t.member
	in := m != nil && m.ByIdp
	encoded, err := json.Marshal(matches)
	if err != nil {
		return fmt.Errorf("encode the entries that matched: %w", err)
	}
	switch {
	case len(matches) > 0 && in:
		if slices.Equal(matches, t.before) {
			return nil
		}
		if err := q.SetIdpMatches(ctx, dbgen.SetIdpMatchesParams{OrgID: s.orgID, TeamID: teamID, UserID: userID,
			IdpMatches: encoded}); err != nil {
			return fmt.Errorf("record the entries that matched in the team %s: %w", t.publicID, err)
		}
		return nil
	case len(matches) > 0:
		if err := q.JoinThroughIdp(ctx, dbgen.JoinThroughIdpParams{OrgID: s.orgID, TeamID: teamID, UserID: userID,
			IdpMatches: encoded, Now: s.clock.Now().UTC()}); err != nil {
			return fmt.Errorf("put the user %d into the team %s: %w", userID, t.publicID, err)
		}
		byHand := m != nil && m.ByHand
		return s.recordIdp(ctx, q, t, userID, groupsClaim, ActionUserAdded, matches[0], sourcesOf(byHand, false),
			sourcesOf(byHand, true), user)
	case !in:
		return nil
	}
	entry := IdpEntry{}
	if len(t.before) > 0 {
		entry = t.before[0]
	}
	if m.ByHand {
		if err := q.LeaveThroughIdp(ctx, dbgen.LeaveThroughIdpParams{OrgID: s.orgID, TeamID: teamID,
			UserID: userID}); err != nil {
			return fmt.Errorf("take the source idp of the user %d in the team %s: %w", userID, t.publicID, err)
		}
	} else if _, err := q.DeleteTeamUser(ctx, dbgen.DeleteTeamUserParams{OrgID: s.orgID, TeamID: teamID,
		UserID: userID}); err != nil {
		return fmt.Errorf("take the user %d out of the team %s: %w", userID, t.publicID, err)
	}
	return s.recordIdp(ctx, q, t, userID, groupsClaim, ActionUserRemoved, entry, sourcesOf(m.ByHand, true),
		sourcesOf(m.ByHand, false), user)
}

// recordIdp records a change the identity provider made to the membership of the User in the Team: the Audit log
// entry by the system with the sources before and after, the source idp and the entry, and the live hint team.
func (s *Service) recordIdp(ctx context.Context, q Queries, t *idpTeam, userID int64, groupsClaim, action string,
	entry IdpEntry, before, after []string, user **dbgen.GetMemberRow) error {
	if *user == nil {
		u, err := q.GetMember(ctx, dbgen.GetMemberParams{OrgID: s.orgID, ID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("the user %d is deleted", userID)
		}
		if err != nil {
			return fmt.Errorf("read the user %d: %w", userID, err)
		}
		*user = &u
	}
	u := *user
	change := audit.Change{Pointer: "/users/" + u.PublicID}
	if len(before) > 0 {
		change.Before = personView{Sources: before}
	}
	if len(after) > 0 {
		change.After = personView{Sources: after}
	}
	if err := s.audit.Record(ctx, q, audit.Entry{OrgID: s.orgID, Actor: audit.System, Transport: audit.TransportSystem,
		Action: action, Resource: audit.Resource{Type: ResourceTeam, PublicID: t.publicID, Name: t.name},
		Diff: []audit.Change{change},
		Details: map[string]any{"user": u.PublicID, "user_name": u.Name, "source": SourceIdp,
			"entry": map[string]string{"claim": claimOf(entry, groupsClaim), "group": entry.Group}}}); err != nil {
		return err
	}
	return q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: Hint, ID: t.publicID})
}

// sourcesOf are the sources of a person in a Team, in the order the API lists them.
func sourcesOf(byHand, byIdp bool) []string {
	out := []string{}
	if byHand {
		out = append(out, SourceByHand)
	}
	if byIdp {
		out = append(out, SourceIdp)
	}
	return out
}

// matchesOf reads the entries that matched a person, as a row stores them.
func matchesOf(raw []byte) []IdpEntry {
	var out []IdpEntry
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out) // the column holds what SyncMembership wrote; anything else reads as none
	}
	if out == nil {
		out = []IdpEntry{}
	}
	return out
}
