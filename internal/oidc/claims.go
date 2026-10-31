// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package oidc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/oidc/dbgen"
)

// SubtreeSuffix ends an entry that matches a group and every group below it (C-03.FR-5).
const SubtreeSuffix = "/*"

// Claims are the values of the claims a sign-in, a link or a re-check read, by claim name. A claim that is a key was
// decided — at a sign-in every claim the entries name is, with no values when the tokens lack it, since identity
// providers leave a claim out for a person in no group — and one that is not a key was not, as at a re-check whose
// refreshed ID token lacks it.
type Claims map[string][]string

// Has reports whether the claim was decided.
func (c Claims) Has(claim string) bool {
	_, ok := c[claim]
	return ok
}

// Matches reports whether a value of claim matches the entry group.
func (c Claims) Matches(claim, group string) bool {
	return slices.ContainsFunc(c[claim], func(v string) bool { return MatchGroup(group, v) })
}

// ValidGroup reports whether entry may be saved as the group of an entry: not empty, with no * other than a trailing
// /* after a group that is not empty (C-03.FR-5, C-25.FR-4). There are no other globs and no regular expressions.
func ValidGroup(entry string) bool {
	prefix, _ := strings.CutSuffix(entry, SubtreeSuffix)
	return prefix != "" && !strings.Contains(prefix, "*")
}

// MatchGroup reports whether the entry matches the group a claim carries: an entry a/b/* matches a/b and every group
// below it at / boundaries — a/b/c, never a/bx — and any other entry only the same group, compared exactly. An entry
// that ValidGroup refuses, such as one an earlier version stored, matches only the same group.
func MatchGroup(entry, group string) bool {
	if prefix, ok := strings.CutSuffix(entry, SubtreeSuffix); ok && ValidGroup(entry) {
		return group == prefix || strings.HasPrefix(group, prefix+"/")
	}
	return group == entry
}

// claimOf is the claim an entry reads: its own, or the groups claim when it names none.
func claimOf(claim, groupsClaim string) string {
	if claim == "" {
		return groupsClaim
	}
	return claim
}

// mappingClaims are the claims the Role mapping reads: the groups claim, then the claims the entries name, each once.
func mappingClaims(st Settings) []string {
	out := []string{st.GroupsClaim}
	for _, m := range st.GroupMappings {
		if c := claimOf(m.Claim, st.GroupsClaim); !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// claimNames are the claims a sign-in, a link or a re-check reads: those of the Role mapping, then those the entries of
// the Teams name.
func (s *Service) claimNames(ctx context.Context, st Settings) ([]string, error) {
	out := mappingClaims(st)
	if s.cfg.Membership == nil {
		return out, nil
	}
	team, err := s.cfg.Membership.EntryClaims(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the claims of the team entries: %w", err)
	}
	for _, c := range team {
		if c = claimOf(c, st.GroupsClaim); !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// readClaims reads the claims names from the verified ID token and, for those it lacks, from userinfo with one call
// (D247): every name is decided, without values when neither carries it; found are the names either carried. A value
// that is not a string or a list of strings counts as none.
func (s *Service) readClaims(ctx context.Context, p *Provider, d Discovery, idt IDToken, access logging.Secret,
	names []string) (Claims, []string, error) {
	claims := make(Claims, len(names))
	var found, lacking []string
	for _, n := range names {
		v, ok := idt.Claims[n]
		if !ok {
			lacking = append(lacking, n)
			claims[n] = []string{}
			continue
		}
		claims[n], found = valuesOf(v), append(found, n)
	}
	if len(lacking) == 0 {
		return claims, found, nil
	}
	info, err := p.UserInfo(ctx, d, access)
	if err != nil {
		return nil, nil, err
	}
	if info == nil {
		return claims, found, nil
	}
	if sub, _ := info["sub"].(string); sub != idt.Subject {
		return nil, nil, &BackChannelError{Step: "userinfo", msg: "userinfo names another subject than the ID token"}
	}
	for _, n := range lacking {
		if v, ok := info[n]; ok {
			claims[n], found = valuesOf(v), append(found, n)
		}
	}
	return claims, found, nil
}

// presentClaims are the claims names a refreshed ID token carries: a re-check decides only those (D247).
func presentClaims(idt IDToken, names []string) Claims {
	claims := Claims{}
	for _, n := range names {
		if v, ok := idt.Claims[n]; ok {
			claims[n] = valuesOf(v)
		}
	}
	return claims
}

func valuesOf(v any) []string {
	if list := stringList(v); list != nil {
		return list
	}
	return []string{}
}

// decidesAll reports whether claims decided every one of names.
func (c Claims) decidesAll(names []string) bool {
	return !slices.ContainsFunc(names, func(n string) bool { return !c.Has(n) })
}

// MapRole maps the claims to a Role: the highest Role of the entries whose claim carries a group they match, each
// reading its own claim or groupsClaim, else the Role for unmatched users; ok is false when that is none.
func MapRole(claims Claims, groupsClaim string, entries []GroupMapping, unmatched string) (string, bool) {
	best := ""
	for _, m := range entries {
		if roleRank[m.Role] > roleRank[best] && claims.Matches(claimOf(m.Claim, groupsClaim), m.Group) {
			best = m.Role
		}
	}
	if best != "" {
		return best, true
	}
	if unmatched == UnmatchedNone || roleRank[unmatched] == 0 {
		return "", false
	}
	return unmatched, true
}

// The claims an Audit log entry lists are bounded, since their values come from outside.
const maxAuditClaims = 20

// auditClaims are the claims names as a refusal records them: the values of each, bounded as auditGroups bounds them,
// for at most maxAuditClaims claims.
func auditClaims(claims Claims, names []string) map[string][]string {
	out := make(map[string][]string, min(len(names), maxAuditClaims))
	for _, n := range names {
		if len(out) == maxAuditClaims {
			break
		}
		out[n] = auditGroups(claims[n])
	}
	return out
}

// Membership follows the identity provider entries of the Teams (C-25.FR-4); internal/teams implements it and the
// runtime sets it, so that this package does not import the Teams.
type Membership interface {
	// EntryClaims lists the claims the entries of the Teams read, the empty string for the groups claim.
	EntryClaims(ctx context.Context) ([]string, error)
	// SyncMembership puts the User into each Team with an entry that matches the claims and takes them out of each Team
	// they are in only through entries that no longer match, deciding only the entries whose claim is in claims, in
	// the transaction tx, which holds the row of the User locked. It never changes a Role.
	SyncMembership(ctx context.Context, tx dbgen.DBTX, userID int64, groupsClaim string, claims Claims) error
}

// syncMembership calls the Membership, when there is one, in the transaction of q.
func (s *Service) syncMembership(ctx context.Context, q Queries, userID int64, st Settings, claims Claims) error {
	if s.cfg.Membership == nil || len(claims) == 0 {
		return nil
	}
	if err := s.cfg.Membership.SyncMembership(ctx, q.DBTX(), userID, st.GroupsClaim, claims); err != nil {
		return fmt.Errorf("follow the team entries: %w", err)
	}
	return nil
}
