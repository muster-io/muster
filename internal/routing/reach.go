// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"cmp"
	"maps"
	"slices"
)

// SeverityCondition is the Severity condition of a Destination of a Route or a Team (C-26.FR-1), checked against the
// Severity level of an Alert Group.
type SeverityCondition string

// The Severity conditions: any level, the default, only critical, or every level below critical.
const (
	ConditionAny           SeverityCondition = "any"
	ConditionCritical      SeverityCondition = "critical"
	ConditionBelowCritical SeverityCondition = "below_critical"
)

// Conditions are the Severity conditions in the order the API lists them.
var Conditions = []SeverityCondition{ConditionAny, ConditionCritical, ConditionBelowCritical}

// Meets reports whether the Severity level of an Alert Group meets the condition; an empty condition is any.
func (c SeverityCondition) Meets(level string) bool {
	switch c {
	case ConditionCritical:
		return level == levelCritical
	case ConditionBelowCritical:
		return level != levelCritical
	default:
		return true
	}
}

// levelCritical is the critical Severity level (organization.SeverityCritical), the one the conditions tell apart.
const levelCritical = "critical"

// The kinds of the entries that reach a Destination: the Alert Group's Route, or one of its Teams.
const (
	EntryRoute = "route"
	EntryTeam  = "team"
)

// Entry is a Destination of a Route or a Team with its Severity condition, by the Destination's internal id.
type Entry struct {
	DestinationID int64             `json:"destination_id"`
	Condition     SeverityCondition `json:"severity_condition"`
}

// TeamEntries are the Destination entries of one Team of an Alert Group, by the Team's internal id.
type TeamEntries struct {
	TeamID  int64   `json:"team_id"`
	Entries []Entry `json:"entries"`
}

// Reaching is an entry that reaches a Destination: the Route's, or a Team's with its id, with its condition.
type Reaching struct {
	Kind      string            `json:"kind"`
	TeamID    int64             `json:"team_id,omitempty"`
	Condition SeverityCondition `json:"severity_condition"`
}

// Reached is a Destination an Alert Group reaches with the entries that reach it, the Route's first, then the Teams'
// by id.
type Reached struct {
	DestinationID int64      `json:"destination_id"`
	Entries       []Reaching `json:"entries"`
}

// Reach is the rule of the Destinations an Alert Group reaches (C-26.FR-2, FR-3, FR-7): the entries of its Route whose
// condition its Severity level meets and, with deliverToTeams, the entries of each of its Teams whose condition it
// meets, keyed by Destination with the entries that reach each, in the order of the Destinations' ids. A Destination
// that several entries reach is reached once; one whose entries none meets is not reached. The owning Team of the
// Route is no input: it adds nothing. The rule reads no store, so that the dispatcher, delivery and the preview apply
// the same rule to what each reads.
func Reach(level string, route []Entry, deliverToTeams bool, teams []TeamEntries) []Reached {
	by := map[int64][]Reaching{}
	for _, e := range route {
		if e.Condition.Meets(level) {
			by[e.DestinationID] = append(by[e.DestinationID], Reaching{Kind: EntryRoute, Condition: orAny(e.Condition)})
		}
	}
	if deliverToTeams {
		for _, t := range slices.SortedFunc(slices.Values(teams), func(a, b TeamEntries) int {
			return cmp.Compare(a.TeamID, b.TeamID)
		}) {
			for _, e := range t.Entries {
				if e.Condition.Meets(level) {
					by[e.DestinationID] = append(by[e.DestinationID], Reaching{Kind: EntryTeam, TeamID: t.TeamID,
						Condition: orAny(e.Condition)})
				}
			}
		}
	}
	out := make([]Reached, 0, len(by))
	for _, id := range slices.Sorted(maps.Keys(by)) {
		out = append(out, Reached{DestinationID: id, Entries: by[id]})
	}
	return out
}

// orAny is the condition, any for an empty one.
func orAny(c SeverityCondition) SeverityCondition {
	if c == "" {
		return ConditionAny
	}
	return c
}
