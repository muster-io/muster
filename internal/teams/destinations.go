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
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/teams/dbgen"
)

// The Severity conditions of the Destinations of a Team (C-26.FR-1), as routing names them: any level, only critical,
// or every level below critical.
const (
	ConditionAny           = "any"
	ConditionCritical      = "critical"
	ConditionBelowCritical = "below_critical"
)

// DestinationEntry is a Destination of the Team with its Severity condition, by the Destination's public_id
// (C-26.FR-1): where the Alert Groups of the Team arrive from the Routes that deliver to the Teams from the label.
type DestinationEntry struct {
	DestinationID string `json:"destination_id"`
	Condition     string `json:"severity_condition"`
}

// DestinationRef is a Destination of the Team as a read names it, with its type and health.
type DestinationRef struct {
	PublicID     string
	Name         string
	Type         string
	Health       string
	BrokenSince  *time.Time
	BrokenReason *string
}

// teamDestination is a Destination of the Team as a save gives it: its internal id and its condition.
type teamDestination struct {
	id        int64
	condition string
}

// withDestinations sets the Destinations of each Team that are not deleted: its entries by public_id and its
// Destinations by name.
func (s *Service) withDestinations(ctx context.Context, q Queries, list []Team) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	index := make(map[int64]int, len(list))
	for i, t := range list {
		ids[i], index[t.ID] = t.ID, i
		list[i].DestinationEntries, list[i].Destinations = []DestinationEntry{}, []DestinationRef{}
	}
	rows, err := q.ListTeamDestinations(ctx, dbgen.ListTeamDestinationsParams{OrgID: s.orgID, TeamIds: ids})
	if err != nil {
		return fmt.Errorf("list the destinations of the teams: %w", err)
	}
	for _, r := range rows {
		i, ok := index[r.TeamID]
		if !ok {
			continue
		}
		list[i].DestinationEntries = append(list[i].DestinationEntries, DestinationEntry{DestinationID: r.PublicID,
			Condition: r.SeverityCondition})
		ref := DestinationRef{PublicID: r.PublicID, Name: r.Name, Type: r.Type, Health: r.Health,
			BrokenReason: textOf(r.BrokenReason)}
		if r.BrokenSince.Valid {
			since := r.BrokenSince.Time.UTC()
			ref.BrokenSince = &since
		}
		list[i].Destinations = append(list[i].Destinations, ref)
	}
	for i := range list {
		slices.SortStableFunc(list[i].Destinations, func(a, b DestinationRef) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.PublicID, b.PublicID))
		})
	}
	return nil
}

// resolveDestinations reads the Destinations of the entries of a save, each once with its condition, any by default
// (C-26.FR-1), and returns them by public_id: an id that names no Destination, or a deleted one, is an unknown_id
// FieldError at the entry's destination_id, one named twice a duplicate, and a condition outside the three an
// invalid_format one at its severity_condition.
func (s *Service) resolveDestinations(ctx context.Context, q Queries, entries []DestinationEntry) (
	map[string]teamDestination, error) {
	out := map[string]teamDestination{}
	if len(entries) == 0 {
		return out, nil
	}
	pointer := func(i int, field string) string { return "/destination_entries/" + strconv.Itoa(i) + "/" + field }
	canonical := make([]string, len(entries))
	seen := make(map[string]bool, len(entries))
	for i, e := range entries {
		id, err := publicid.Parse(publicid.Destination, e.DestinationID)
		if err != nil {
			return nil, &FieldError{Pointer: pointer(i, "destination_id"), Code: CodeUnknownID,
				Detail: "No such Destination."}
		}
		if seen[id] {
			return nil, &FieldError{Pointer: pointer(i, "destination_id"), Code: CodeDuplicate,
				Detail: "The Destination is named twice."}
		}
		switch e.Condition {
		case "", ConditionAny, ConditionCritical, ConditionBelowCritical:
		default:
			return nil, &FieldError{Pointer: pointer(i, "severity_condition"), Code: CodeInvalidFormat,
				Detail: "Choose any, critical or below_critical."}
		}
		seen[id], canonical[i] = true, id
	}
	rows, err := q.ResolveTeamDestinations(ctx, dbgen.ResolveTeamDestinationsParams{OrgID: s.orgID,
		PublicIds: canonical})
	if err != nil {
		return nil, fmt.Errorf("read the destinations of the team: %w", err)
	}
	found := make(map[string]int64, len(rows))
	for _, r := range rows {
		found[r.PublicID] = r.ID
	}
	for i, id := range canonical {
		internal, ok := found[id]
		if !ok {
			return nil, &FieldError{Pointer: pointer(i, "destination_id"), Code: CodeUnknownID,
				Detail: "No such Destination."}
		}
		out[id] = teamDestination{id: internal, condition: cmp.Or(entries[i].Condition, ConditionAny)}
	}
	return out, nil
}

// entriesOf are the entries of the Destinations of a save, by public_id.
func entriesOf(want map[string]teamDestination) []DestinationEntry {
	out := make([]DestinationEntry, 0, len(want))
	for _, id := range slices.Sorted(maps.Keys(want)) {
		out = append(out, DestinationEntry{DestinationID: id, Condition: want[id].condition})
	}
	return out
}

// writeDestinations makes want the Destinations of the Team teamID, which had before: those it no longer names are
// removed, the new ones added now and the kept ones keep when they were added with the condition of want. The change
// reaches the open Alert Groups of the Team at their next change.
func (s *Service) writeDestinations(ctx context.Context, q Queries, teamID int64, before []DestinationEntry,
	want map[string]teamDestination, now time.Time) error {
	keep := map[string]string{}
	for _, e := range before {
		keep[e.DestinationID] = e.Condition
	}
	var removed []string
	for id := range keep {
		if _, ok := want[id]; !ok {
			removed = append(removed, id)
		}
	}
	if len(removed) > 0 {
		rows, err := q.ResolveTeamDestinations(ctx, dbgen.ResolveTeamDestinationsParams{OrgID: s.orgID,
			PublicIds: removed})
		if err != nil {
			return fmt.Errorf("read the destinations the team leaves: %w", err)
		}
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		if err := q.DeleteTeamDestinations(ctx, dbgen.DeleteTeamDestinationsParams{OrgID: s.orgID, TeamID: teamID,
			DestinationIds: ids}); err != nil {
			return fmt.Errorf("remove destinations from the team: %w", err)
		}
	}
	p := dbgen.UpsertTeamDestinationsParams{OrgID: s.orgID, TeamID: teamID, Now: now}
	for _, id := range slices.Sorted(maps.Keys(want)) {
		if c, ok := keep[id]; ok && c == want[id].condition {
			continue
		}
		p.DestinationIds = append(p.DestinationIds, want[id].id)
		p.Conditions = append(p.Conditions, want[id].condition)
	}
	if len(p.DestinationIds) == 0 {
		return nil
	}
	if err := q.UpsertTeamDestinations(ctx, p); err != nil {
		return fmt.Errorf("write the destinations of the team: %w", err)
	}
	return nil
}

func textOf(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}
