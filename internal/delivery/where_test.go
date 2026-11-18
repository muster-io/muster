// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package delivery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/delivery/dbgen"
	"github.com/muster-io/muster/internal/groups"
	"github.com/muster-io/muster/internal/mentions"
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

// teamMentionsOf has a Team mention of Team 3 for every Connection.
type teamMentionsOf struct{}

func (teamMentionsOf) ForConnection(_ context.Context, _ mentions.DBTX, team, _ int64) (mentions.TeamMention, bool,
	error) {
	return mentions.TeamMention{Group: "payments"}, team == 3, nil
}

// TestWherePageTeam is C-32.FR-9 and FR-11 for a step "Page the whole Team": Telegram mentions no group and no one
// for everyone in the chat, so it is never Paged; a Bridge only when its bridge declares group mentions — with a Team
// mention for its Connection — or the token in everyone_mentions, and only as a messenger; an outgoing webhook always,
// as data; Mattermost with its Team mention and its tokens.
func TestWherePageTeam(t *testing.T) {
	s := &Service{teamMentions: teamMentionsOf{}}
	conn := int64(5)
	root := func(typ string, c mentions.Connection) pageRoot {
		r := pageRoot{typ: typ, conn: c, replies: true}
		if typ != TypeWebhook {
			r.connection = &conn
		}
		return r
	}
	declares := mentions.Connection{GroupMentions: true, Messenger: true, Everyone: []string{"here"}}
	team, here, all := groups.PageRequest{Team: 3}, groups.PageRequest{Everyone: "here"},
		groups.PageRequest{Everyone: "all"}
	for _, c := range []struct {
		name string
		root pageRoot
		r    groups.PageRequest
		want bool
	}{
		{"Telegram, the Team", root(TypeTelegram, mentions.Connection{}), team, false},
		{"Telegram, everyone", root(TypeTelegram, mentions.Connection{}), here, false},
		{"a bridge that declares group mentions", root(TypeBridge, declares), team, true},
		{"a bridge without group mentions", root(TypeBridge, mentions.Connection{Messenger: true}), team, false},
		{"a bridge with a Team without a Team mention", root(TypeBridge, declares), groups.PageRequest{Team: 4},
			false},
		{"a bridge that declares the token", root(TypeBridge, declares), here, true},
		{"a bridge that does not declare the token", root(TypeBridge, declares), all, false},
		{"a gateway bridge", root(TypeBridge, mentions.Connection{GroupMentions: true,
			Everyone: []string{"here"}}), here, false},
		{"an outgoing webhook, the Team", root(TypeWebhook, mentions.Connection{}), team, true},
		{"an outgoing webhook, everyone", root(TypeWebhook, mentions.Connection{}), all, true},
		{"Mattermost, the Team", root(TypeMattermost, mentions.Connection{}), team, true},
		{"Slack without usergroups:read", root(TypeSlack, mentions.Connection{}), team, false},
		{"Slack with usergroups:read", root(TypeSlack, mentions.Connection{UsergroupsRead: true}), team, true},
	} {
		got, err := s.canMention(t.Context(), nil, c.root, c.r)
		if err != nil || got != c.want {
			t.Errorf("%s: %v %v, want %v", c.name, got, err, c.want)
		}
	}
	if ok, _ := (&Service{}).canMention(t.Context(), nil, root(TypeBridge, declares), team); ok {
		t.Error("a Team mention without Team mentions")
	}
}

// TestWherePageTypes is C-32.FR-11: a Page replies in Mattermost, Slack, Telegram and through Bridges, and in an
// outgoing webhook only with a "reply in thread" request; pageRootOf reads what the Connection can mention.
func TestWherePageTypes(t *testing.T) {
	for _, c := range []struct {
		row  dbgen.ListPageRootsRow
		want bool
	}{
		{dbgen.ListPageRootsRow{Type: TypeMattermost}, true},
		{dbgen.ListPageRootsRow{Type: TypeSlack}, true},
		{dbgen.ListPageRootsRow{Type: TypeTelegram}, true},
		{dbgen.ListPageRootsRow{Type: TypeBridge}, true},
		{dbgen.ListPageRootsRow{Type: TypeWebhook, WebhookReplies: true}, true},
		{dbgen.ListPageRootsRow{Type: TypeWebhook}, false},
		{dbgen.ListPageRootsRow{Type: "other"}, false},
	} {
		if got := pageRootOf(c.row).pages(); got != c.want {
			t.Errorf("%s replies %v: %v", c.row.Type, c.row.WebhookReplies, got)
		}
	}
	r := pageRootOf(dbgen.ListPageRootsRow{ID: 9, Type: TypeBridge, ConnectionID: pgtype.Int8{Int64: 5, Valid: true},
		Health: healthBroken, GroupMentions: true, NativeMentions: true, Messenger: true,
		EveryoneMentions: []string{"here"}})
	if r.id != 9 || r.connection == nil || *r.connection != 5 || !r.broken || r.conn.ID != 5 || !r.conn.GroupMentions ||
		!r.conn.NativeMentions || !r.conn.Messenger || !slices.Equal(r.conn.Everyone, []string{"here"}) {
		t.Errorf("root %+v", r)
	}
}
