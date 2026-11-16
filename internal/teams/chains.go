// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/muster-io/muster/internal/db"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/teams/dbgen"
)

// HolderChain is the kind of an Escalation chain that its owning Team holds (C-25.FR-10).
const HolderChain = "escalation_chain"

// ChainEntry is an Escalation chain of the Team with its Severity condition, by the chain's public_id (C-32.FR-2).
type ChainEntry struct {
	ChainID   string `json:"chain_id"`
	Condition string `json:"severity_condition"`
}

// ChainRef is an Escalation chain of the Team as a read names it, with its owning Team, nil when none owns it.
type ChainRef struct {
	PublicID   string
	Name       string
	OwningTeam *ChainOwner
}

// ChainOwner is the Team that owns an Escalation chain, marked deleted once deleted.
type ChainOwner struct {
	PublicID string
	Name     string
	Slug     string
	Deleted  bool
}

// withChains sets the Escalation chains of each Team: its entries by public_id and its chains by name.
func (s *Service) withChains(ctx context.Context, q Queries, list []Team) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, t := range list {
		ids[i], index[t.ID] = t.ID, i
		list[i].ChainEntries, list[i].Chains = []ChainEntry{}, []ChainRef{}
	}
	rows, err := q.ListTeamChains(ctx, dbgen.ListTeamChainsParams{OrgID: s.orgID, TeamIds: ids})
	if err != nil {
		return fmt.Errorf("list the escalation chains of the teams: %w", err)
	}
	for _, r := range rows {
		i, ok := index[r.TeamID]
		if !ok {
			continue
		}
		list[i].ChainEntries = append(list[i].ChainEntries, ChainEntry{ChainID: r.PublicID,
			Condition: r.SeverityCondition})
		ref := ChainRef{PublicID: r.PublicID, Name: r.Name}
		if r.OwnerPublicID.Valid {
			ref.OwningTeam = &ChainOwner{PublicID: r.OwnerPublicID.String, Name: r.OwnerName.String,
				Slug: r.OwnerSlug.String, Deleted: r.OwnerDeleted}
		}
		list[i].Chains = append(list[i].Chains, ref)
	}
	for i := range list {
		slices.SortStableFunc(list[i].Chains, func(a, b ChainRef) int {
			return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)),
				cmp.Compare(a.PublicID, b.PublicID))
		})
	}
	return nil
}

// teamChain is an Escalation chain of the Team as a save gives it: its internal id and its condition.
type teamChain struct {
	id        int64
	condition string
}

// resolveChains reads the Escalation chains of the entries of a save FOR KEY SHARE, after the Team's row (the lock order
// of internal/escalation), each once with its condition, any by default (C-32.FR-2), and returns them by public_id: an
// id that names no chain is an unknown_id FieldError at the entry's chain_id, one named twice a duplicate, and a
// condition outside the three an invalid_format one at its severity_condition.
func (s *Service) resolveChains(ctx context.Context, q Queries, entries []ChainEntry) (map[string]teamChain, error) {
	out := map[string]teamChain{}
	if len(entries) == 0 {
		return out, nil
	}
	pointer := func(i int, field string) string { return "/chain_entries/" + strconv.Itoa(i) + "/" + field }
	canonical := make([]string, len(entries))
	seen := make(map[string]bool, len(entries))
	for i, e := range entries {
		id, err := publicid.Parse(publicid.EscalationChain, e.ChainID)
		if err != nil {
			return nil, &FieldError{Pointer: pointer(i, "chain_id"), Code: CodeUnknownID,
				Detail: "No such Escalation chain."}
		}
		if seen[id] {
			return nil, &FieldError{Pointer: pointer(i, "chain_id"), Code: CodeDuplicate,
				Detail: "The Escalation chain is named twice."}
		}
		switch e.Condition {
		case "", ConditionAny, ConditionCritical, ConditionBelowCritical:
		default:
			return nil, &FieldError{Pointer: pointer(i, "severity_condition"), Code: CodeInvalidFormat,
				Detail: "Choose any, critical or below_critical."}
		}
		seen[id], canonical[i] = true, id
	}
	rows, err := q.ResolveTeamChains(ctx, dbgen.ResolveTeamChainsParams{OrgID: s.orgID, PublicIds: canonical})
	if err != nil {
		return nil, fmt.Errorf("read the escalation chains of the team: %w", err)
	}
	found := make(map[string]int64, len(rows))
	for _, r := range rows {
		found[r.PublicID] = r.ID
	}
	for i, id := range canonical {
		internal, ok := found[id]
		if !ok {
			return nil, &FieldError{Pointer: pointer(i, "chain_id"), Code: CodeUnknownID,
				Detail: "No such Escalation chain."}
		}
		out[id] = teamChain{id: internal, condition: cmp.Or(entries[i].Condition, ConditionAny)}
	}
	return out, nil
}

// chainEntriesOf are the entries of the Escalation chains of a save, by public_id.
func chainEntriesOf(want map[string]teamChain) []ChainEntry {
	out := make([]ChainEntry, 0, len(want))
	for _, id := range slices.Sorted(maps.Keys(want)) {
		out = append(out, ChainEntry{ChainID: id, Condition: want[id].condition})
	}
	return out
}

// writeChains makes want the Escalation chains of the Team teamID: those it no longer names are removed, the new ones
// added now and the kept ones keep the time they were added with the condition of want. No chain runs until
// Escalation chains run.
func (s *Service) writeChains(ctx context.Context, q Queries, teamID int64, want map[string]teamChain,
	now time.Time) error {
	p := dbgen.UpsertTeamChainsParams{OrgID: s.orgID, TeamID: teamID, Now: now, ChainIds: []int64{},
		Conditions: []string{}}
	for _, id := range slices.Sorted(maps.Keys(want)) {
		p.ChainIds = append(p.ChainIds, want[id].id)
		p.Conditions = append(p.Conditions, want[id].condition)
	}
	if err := q.DeleteTeamChainsExcept(ctx, dbgen.DeleteTeamChainsExceptParams{OrgID: s.orgID, TeamID: teamID,
		KeepIds: p.ChainIds}); err != nil {
		return fmt.Errorf("remove escalation chains from the team: %w", err)
	}
	if len(p.ChainIds) == 0 {
		return nil
	}
	if err := q.UpsertTeamChains(ctx, p); err != nil {
		return fmt.Errorf("write the escalation chains of the team: %w", err)
	}
	return nil
}

// hintChain is the live hint of an Escalation chain, as internal/escalation names it.
const hintChain = "escalation-chain"

// notifyChains sends the live hint escalation-chain of every chain whose entry a save added, removed or changed —
// its referrers, Destinations and warnings read otherwise — in the order of their public_ids; with all, of every chain
// the save names before or after, for a save that changes what each of them reads of it, such as its name or its
// Destination entries; and of the chains also names, such as those the Team owns.
func (s *Service) notifyChains(ctx context.Context, q Queries, before, after []ChainEntry, all bool,
	also ...string) error {
	was := make(map[string]ChainEntry, len(before))
	for _, e := range before {
		was[e.ChainID] = e
	}
	touched := map[string]bool{}
	for _, e := range after {
		if old, ok := was[e.ChainID]; all || !ok || old != e {
			touched[e.ChainID] = true
		}
		delete(was, e.ChainID)
	}
	for id := range was {
		touched[id] = true
	}
	for _, id := range also {
		touched[id] = true
	}
	for _, id := range slices.Sorted(maps.Keys(touched)) {
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: hintChain, ID: id}); err != nil {
			return err
		}
	}
	return nil
}
