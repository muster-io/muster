// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/routing"
)

// reachVector is one case of internal/routing/testdata/reach (S-121), the one set of cases of who an Alert Group
// reaches.
type reachVector struct {
	Name           string                     `json:"name"`
	Level          string                     `json:"level"`
	DeliverToTeams bool                       `json:"deliver_to_teams"`
	Route          []routing.Entry            `json:"route"`
	TeamEntries    map[string][]routing.Entry `json:"team_entries"`
	Teams          []int64                    `json:"teams"`
	Want           []routing.Reached          `json:"want"`
}

// destinationsOf are the Destinations of the expected reach an entry of the kind, and of the Team for a Team's, reaches.
func destinationsOf(want []routing.Reached, kind string, team int64) []int64 {
	var out []int64
	for _, r := range want {
		if slices.ContainsFunc(r.Entries, func(e routing.Reaching) bool {
			return e.Kind == kind && (kind == routing.EntryRoute || e.TeamID == team)
		}) {
			out = append(out, r.DestinationID)
		}
	}
	return out
}

// targetIDs are the Destinations of the targets.
func targetIDs(ts []groups.PageTarget) []int64 {
	var out []int64
	for _, t := range ts {
		out = append(out, t.DestinationID)
	}
	return out
}

// TestWhereChainsPage is C-32.FR-8 and C-32.AC-1, AC-16 over the vectors of the reach (S-121): with every reached
// Destination holding the Root message, a Route's chain Pages exactly in the Destinations the vectors reach by an entry
// of the Route, a Team's chain in those they reach by an entry of that Team — or, where the Team reaches none, in the
// Route's — and a chain of the Route and a Team in the union, the Route's as the source where it reaches them.
func TestWhereChainsPage(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "routing", "testdata", "reach", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var vs []reachVector
		if err := json.Unmarshal(raw, &vs); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, v := range vs {
			t.Run(filepath.Base(f)+": "+v.Name, func(t *testing.T) {
				var teams []routing.TeamEntries
				for _, id := range v.Teams {
					teams = append(teams, routing.TeamEntries{TeamID: id,
						Entries: v.TeamEntries[strconv.FormatInt(id, 10)]})
				}
				reached := routing.Reach(v.Level, v.Route, v.DeliverToTeams, teams)
				all := func(int64) bool { return true }
				routeDests := destinationsOf(v.Want, routing.EntryRoute, 0)
				got := chooseTargets([]groups.PageSource{{Kind: groups.PageSourceRoute}}, reached, all)
				if !slices.Equal(targetIDs(got), routeDests) {
					t.Errorf("the Route's chain Pages in %v, want %v", targetIDs(got), routeDests)
				}
				for _, team := range v.Teams {
					want := destinationsOf(v.Want, routing.EntryTeam, team)
					if len(want) == 0 {
						want = routeDests
					}
					got := chooseTargets([]groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: team}}, reached, all)
					if !slices.Equal(targetIDs(got), want) {
						t.Errorf("the chain of Team %d Pages in %v, want %v", team, targetIDs(got), want)
					}
					both := chooseTargets([]groups.PageSource{{Kind: groups.PageSourceRoute},
						{Kind: groups.PageSourceTeam, TeamID: team}}, reached, all)
					union := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(routeDests), want...))))
					if !slices.Equal(targetIDs(both), union) {
						t.Errorf("a chain of both Pages in %v, want %v", targetIDs(both), union)
					}
					for _, x := range both {
						if slices.Contains(routeDests, x.DestinationID) != (x.Source == groups.PageSourceRoute) {
							t.Errorf("%d has the source %s", x.DestinationID, x.Source)
						}
					}
				}
			})
		}
	}
}

// TestWhereChainsPageWithoutRoots: only the Destinations with the Root message carry a Page, and a Team's chain whose
// Team has none of them falls back to the Route's; without any, a chain Pages nowhere.
func TestWhereChainsPageWithoutRoots(t *testing.T) {
	reached := []routing.Reached{
		{DestinationID: 11, Entries: []routing.Reaching{{Kind: routing.EntryRoute}}},
		{DestinationID: 21, Entries: []routing.Reaching{{Kind: routing.EntryTeam, TeamID: 1}}},
	}
	team := []groups.PageSource{{Kind: groups.PageSourceTeam, TeamID: 1}}
	if got := targetIDs(chooseTargets(team, reached, func(id int64) bool { return id == 11 })); !slices.Equal(got,
		[]int64{11}) {
		t.Errorf("falls back to %v", got)
	}
	if got := chooseTargets(team, reached, func(int64) bool { return false }); len(got) != 0 {
		t.Errorf("without roots %v", got)
	}
	if _, ok := pageOf(groups.Recorded{Event: groups.EventPaged}, 11); ok {
		t.Error("a paged without its Page")
	}
}
