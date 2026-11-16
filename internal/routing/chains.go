// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

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
	"github.com/muster-io/muster/internal/routing/dbgen"
)

// ChainEntry is an Escalation chain of a Route with its Severity condition, by the chain's public_id (C-32.FR-2).
type ChainEntry struct {
	ChainID   string            `json:"chain_id"`
	Condition SeverityCondition `json:"severity_condition"`
}

// ChainRef is an Escalation chain of a Route as a read names it, with its owning Team, nil when none owns it.
type ChainRef struct {
	PublicID   string
	Name       string
	OwningTeam *TeamRef
}

// withChains sets the Escalation chains of each Route: its entries by public_id and its chains by name.
func (s *Service) withChains(ctx context.Context, q Queries, list []Route) error {
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, r := range list {
		ids[i], index[r.ID] = r.ID, i
		list[i].ChainEntries, list[i].Chains = []ChainEntry{}, []ChainRef{}
	}
	rows, err := q.ListRouteChains(ctx, dbgen.ListRouteChainsParams{OrgID: s.orgID, RouteIds: ids})
	if err != nil {
		return fmt.Errorf("list the escalation chains of the routes: %w", err)
	}
	for _, r := range rows {
		i, ok := index[r.RouteID]
		if !ok {
			continue
		}
		list[i].ChainEntries = append(list[i].ChainEntries, ChainEntry{ChainID: r.PublicID,
			Condition: SeverityCondition(r.SeverityCondition)})
		ref := ChainRef{PublicID: r.PublicID, Name: r.Name}
		if r.OwnerPublicID.Valid {
			ref.OwningTeam = &TeamRef{PublicID: r.OwnerPublicID.String, Name: r.OwnerName.String,
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

// chainSet is the Escalation chains of a Route as a save gives them, by public_id: internal id and condition.
type chainSet map[string]ChainEntry

// entries are the entries of the set, by public_id.
func (c chainSet) entries() []ChainEntry {
	out := make([]ChainEntry, 0, len(c))
	for _, id := range slices.Sorted(maps.Keys(c)) {
		out = append(out, ChainEntry{ChainID: id, Condition: c[id].Condition})
	}
	return out
}

// chainIDs maps the public_ids of a set to the chains' internal ids.
type chainIDs map[string]int64

// resolveChains reads the Escalation chains of the entries of a save FOR KEY SHARE, after the Route's row (the lock
// order of internal/escalation), each once with its condition, any by default (C-32.FR-2): an id that names no chain
// is an unknown_id FieldError at the entry's chain_id, one named twice a duplicate, and a condition outside the three
// an invalid_format one at its severity_condition.
func (s *Service) resolveChains(ctx context.Context, q Queries, entries []ChainEntry) (chainSet, chainIDs, error) {
	set, ids := chainSet{}, chainIDs{}
	if len(entries) == 0 {
		return set, ids, nil
	}
	pointer := func(i int, field string) string { return "/chain_entries/" + strconv.Itoa(i) + "/" + field }
	canonical := make([]string, len(entries))
	for i, e := range entries {
		id, err := publicid.Parse(publicid.EscalationChain, e.ChainID)
		if err != nil {
			return nil, nil, &FieldError{Pointer: pointer(i, "chain_id"), Code: CodeUnknownID,
				Detail: "No such Escalation chain."}
		}
		if _, seen := set[id]; seen {
			return nil, nil, &FieldError{Pointer: pointer(i, "chain_id"), Code: CodeDuplicate,
				Detail: "The Escalation chain is named twice."}
		}
		condition := cmp.Or(e.Condition, ConditionAny)
		if !slices.Contains(Conditions, condition) {
			return nil, nil, &FieldError{Pointer: pointer(i, "severity_condition"), Code: CodeInvalidFormat,
				Detail: "Choose any, critical or below_critical."}
		}
		set[id], canonical[i] = ChainEntry{ChainID: id, Condition: condition}, id
	}
	rows, err := q.ResolveRouteChains(ctx, dbgen.ResolveRouteChainsParams{OrgID: s.orgID, PublicIds: canonical})
	if err != nil {
		return nil, nil, fmt.Errorf("read the escalation chains of the route: %w", err)
	}
	for _, r := range rows {
		ids[r.PublicID] = r.ID
	}
	for i, id := range canonical {
		if _, ok := ids[id]; !ok {
			return nil, nil, &FieldError{Pointer: pointer(i, "chain_id"), Code: CodeUnknownID,
				Detail: "No such Escalation chain."}
		}
	}
	return set, ids, nil
}

// writeChains makes the set the Escalation chains of the Route routeID: those it no longer names are removed, the new
// ones added now and the kept ones keep the time they were added with the condition of the set. No chain runs until
// Escalation chains run.
func (s *Service) writeChains(ctx context.Context, q Queries, routeID int64, set chainSet, ids chainIDs,
	now time.Time) error {
	p := dbgen.UpsertRouteChainsParams{OrgID: s.orgID, RouteID: routeID, Now: now, ChainIds: []int64{},
		Conditions: []string{}}
	for _, id := range slices.Sorted(maps.Keys(set)) {
		p.ChainIds = append(p.ChainIds, ids[id])
		p.Conditions = append(p.Conditions, string(set[id].Condition))
	}
	if err := q.DeleteRouteChainsExcept(ctx, dbgen.DeleteRouteChainsExceptParams{OrgID: s.orgID, RouteID: routeID,
		KeepIds: p.ChainIds}); err != nil {
		return fmt.Errorf("remove escalation chains from the route: %w", err)
	}
	if len(p.ChainIds) == 0 {
		return nil
	}
	if err := q.UpsertRouteChains(ctx, p); err != nil {
		return fmt.Errorf("write the escalation chains of the route: %w", err)
	}
	return nil
}

// hintChain is the live hint of an Escalation chain, as internal/escalation names it.
const hintChain = "escalation-chain"

// notifyChains sends the live hint escalation-chain of every chain whose entry a save added, removed or changed —
// its referrers, Destinations and warnings read otherwise — in the order of their public_ids; with all, of every chain
// the save names before or after, for a save that changes what each of them reads of it, such as its name or its
// Destination entries.
func (s *Service) notifyChains(ctx context.Context, q Queries, before, after []ChainEntry, all bool) error {
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
	for _, id := range slices.Sorted(maps.Keys(touched)) {
		if err := q.Notify(ctx, db.Hint{OrgID: s.orgID, Type: hintChain, ID: id}); err != nil {
			return err
		}
	}
	return nil
}
