// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
)

// reachVector is one case of testdata/reach: the entries of the Route and of every Team of the Organization, the
// option deliver_to_teams, the owning Team of the Route, the Severity level and the Teams of the Alert Group, and the
// Destinations it reaches with their entries. Other packages that compute a part of the rule load the same files.
type reachVector struct {
	Name           string             `json:"name"`
	Level          string             `json:"level"`
	DeliverToTeams bool               `json:"deliver_to_teams"`
	OwningTeam     int64              `json:"owning_team"`
	Route          []Entry            `json:"route"`
	TeamEntries    map[string][]Entry `json:"team_entries"`
	Teams          []int64            `json:"teams"`
	Want           []Reached          `json:"want"`
}

// loadReachVectors reads every vector of testdata/reach.
func loadReachVectors(t *testing.T) []reachVector {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "reach", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors: %v", err)
	}
	var out []reachVector
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var vs []reachVector
		if err := json.Unmarshal(raw, &vs); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for i := range vs {
			vs[i].Name = filepath.Base(f) + ": " + vs[i].Name
		}
		out = append(out, vs...)
	}
	return out
}

// TestReach is C-26.FR-2, FR-3, FR-7, AC-3 and AC-10 over the vectors: only the entries of the Teams of the Alert
// Group are given to the rule, the owning Team's only when it is one of them.
func TestReach(t *testing.T) {
	for _, v := range loadReachVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			var teams []TeamEntries
			for _, id := range v.Teams {
				teams = append(teams, TeamEntries{TeamID: id, Entries: v.TeamEntries[strconv.FormatInt(id, 10)]})
			}
			got := Reach(v.Level, v.Route, v.DeliverToTeams, teams)
			want := v.Want
			if want == nil {
				want = []Reached{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Reach = %+v\nwant %+v", got, want)
			}
			if v.OwningTeam != 0 && !slices.Contains(v.Teams, v.OwningTeam) {
				for _, r := range got {
					for _, e := range r.Entries {
						if e.TeamID == v.OwningTeam {
							t.Errorf("the owning Team reaches %d", r.DestinationID)
						}
					}
				}
			}
		})
	}
}

// TestReachCoversEveryCondition: the vectors check every condition at every level, for the Route and for a Team, Teams
// with and without deliver_to_teams, a Destination named twice and the owning Team.
func TestReachCoversEveryCondition(t *testing.T) {
	seen := map[string]bool{}
	var without, twice, owning bool
	for _, v := range loadReachVectors(t) {
		for _, e := range v.Route {
			seen["route/"+string(e.Condition)+"/"+v.Level] = true
		}
		for _, id := range v.Teams {
			for _, e := range v.TeamEntries[strconv.FormatInt(id, 10)] {
				if v.DeliverToTeams {
					seen["team/"+string(e.Condition)+"/"+v.Level] = true
				}
			}
		}
		if !v.DeliverToTeams && len(v.Teams) > 0 {
			without = true
		}
		for _, r := range v.Want {
			if len(r.Entries) > 1 {
				twice = true
			}
		}
		owning = owning || v.OwningTeam != 0
	}
	for _, side := range []string{"route", "team"} {
		for _, c := range Conditions {
			for _, level := range []string{"info", "warning", "critical"} {
				if !seen[side+"/"+string(c)+"/"+level] {
					t.Errorf("no vector of a %s entry %s at %s", side, c, level)
				}
			}
		}
	}
	if !without || !twice || !owning {
		t.Errorf("vectors without deliver_to_teams %v, a Destination named twice %v, the owning Team %v", without, twice,
			owning)
	}
}

// TestConditionMeets: an empty condition is any.
func TestConditionMeets(t *testing.T) {
	if !SeverityCondition("").Meets("critical") || !SeverityCondition("").Meets("info") {
		t.Error("an empty condition does not meet every level")
	}
	got := Reach("info", []Entry{{DestinationID: 1}}, false, nil)
	if len(got) != 1 || got[0].Entries[0].Condition != ConditionAny {
		t.Errorf("Reach of an entry without a condition = %+v", got)
	}
}
