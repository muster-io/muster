// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/publicid"
)

// The rules by which an Alert belongs to a Team (C-25.FR-5), as routing records them.
const (
	TeamRuleLabel   = "label"
	TeamRuleMatcher = "matcher"
)

// TeamRef names a Team, deleted or not: a deleted Team keeps its name in the Alert Groups that had it (C-25.FR-10).
type TeamRef struct {
	PublicID string
	Name     string
	Slug     string
	Deleted  bool
}

// TeamMembership is a Team an Alert belongs to, with the rule that decided it when the Alert started firing and, for
// the rule matcher, the Team matcher.
type TeamMembership struct {
	Team    TeamRef
	Rule    string
	Matcher *string
}

// GroupTeam is a Team of an Alert Group (C-25.FR-6): the rule of the first Alert that brought it and when it joined.
type GroupTeam struct {
	TeamMembership
	JoinedAt time.Time
}

// TeamMine is the value of Filter.Team that selects the Alert Groups of the Teams of the caller ("My Teams").
const TeamMine = "mine"

// joiningTeam is a Team that Alerts joining an Alert Group bring, with the rule that brought it.
type joiningTeam struct {
	id      int64
	rule    string
	matcher string
}

// teams adds to the Teams of g those of the Alerts that join it, which only grow (C-25.FR-6): each Team it lacks joins
// with one Quiet team_joined naming the Team and the rule, by the label when any of the Alerts belongs to it by the
// label, and each value of the Team label that named no Team and that g has not recorded yet with one Quiet
// unknown_team_value (C-25.FR-7). The Teams are those routing recorded on each Alert when it started firing, deleted
// ones aside. g is locked, so the rows of its Teams change with it alone.
func (e *engine) teams(ctx context.Context, q Queries, g *Group, alerts []*alertRow, c *change) error {
	ids := make([]int64, len(alerts))
	for i, a := range alerts {
		ids[i] = a.ID
	}
	slices.Sort(ids)
	rows, err := q.ListAlertTeams(ctx, dbgen.ListAlertTeamsParams{OrgID: e.s.orgID, AlertIds: slices.Compact(ids)})
	if err != nil {
		return fmt.Errorf("read the teams of the alerts joining alert group #%d: %w", g.Number, err)
	}
	brought := map[int64]joiningTeam{}
	values := map[string]bool{}
	for _, r := range rows {
		if r.UnknownTeamValue != "" {
			values[r.UnknownTeamValue] = true
		}
		if r.TeamID == 0 {
			continue
		}
		if t, ok := brought[r.TeamID]; ok && (t.rule == TeamRuleLabel || r.Rule != TeamRuleLabel) {
			continue
		}
		brought[r.TeamID] = joiningTeam{id: r.TeamID, rule: r.Rule, matcher: r.Matcher}
	}
	if len(brought) > 0 {
		p := dbgen.AddGroupTeamsParams{OrgID: e.s.orgID, AlertGroupID: g.ID, JoinedAt: e.now}
		for _, id := range slices.Sorted(maps.Keys(brought)) {
			t := brought[id]
			p.TeamIds = append(p.TeamIds, t.id)
			p.Rules = append(p.Rules, t.rule)
			p.Matchers = append(p.Matchers, t.matcher)
		}
		joined, err := q.AddGroupTeams(ctx, p)
		if err != nil {
			return fmt.Errorf("add the teams of alert group #%d: %w", g.Number, err)
		}
		slices.Sort(joined)
		for _, id := range joined {
			t := brought[id]
			c.add(entry{Event: EventTeamJoined, TeamID: &t.id, TeamRule: t.rule, TeamMatcher: t.matcher})
		}
	}
	if len(values) == 0 {
		return nil
	}
	added, err := q.AddUnknownTeamValues(ctx, dbgen.AddUnknownTeamValuesParams{OrgID: e.s.orgID, ID: g.ID,
		TeamValues: slices.Sorted(maps.Keys(values))})
	if err != nil {
		return fmt.Errorf("record the unknown team values of alert group #%d: %w", g.Number, err)
	}
	for _, v := range added {
		c.add(entry{Event: EventUnknownTeamValue, System: SystemUnknownTeamValue, TeamValue: v})
	}
	return nil
}

// groupTeams reads the Teams of Alert Groups by id, each Alert Group's in the order they joined.
func (s *Service) groupTeams(ctx context.Context, ids []int64) (map[int64][]GroupTeam, error) {
	out := map[int64][]GroupTeam{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.store.ListGroupTeams(ctx, dbgen.ListGroupTeamsParams{OrgID: s.orgID, AlertGroupIds: ids})
	if err != nil {
		return nil, fmt.Errorf("read the teams of the alert groups: %w", err)
	}
	for _, r := range rows {
		out[r.AlertGroupID] = append(out[r.AlertGroupID], GroupTeam{TeamMembership: TeamMembership{
			Team: TeamRef{PublicID: r.PublicID, Name: r.Name, Slug: r.Slug, Deleted: r.Deleted}, Rule: r.Rule,
			Matcher: textOf(r.Matcher)}, JoinedAt: r.JoinedAt.UTC()})
	}
	return out, nil
}

// alertTeams reads the Teams of Alerts by id, as routing decided them when each last started firing.
func (s *Service) alertTeams(ctx context.Context, ids []int64) (map[int64][]TeamMembership, error) {
	out := map[int64][]TeamMembership{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.store.ListGroupAlertTeams(ctx, dbgen.ListGroupAlertTeamsParams{OrgID: s.orgID, AlertIds: ids})
	if err != nil {
		return nil, fmt.Errorf("read the teams of the alerts: %w", err)
	}
	for _, r := range rows {
		out[r.AlertID] = append(out[r.AlertID], TeamMembership{Team: TeamRef{PublicID: r.PublicID, Name: r.Name,
			Slug: r.Slug, Deleted: r.Deleted}, Rule: r.Rule, Matcher: textOf(r.Matcher)})
	}
	return out, nil
}

// entryTeams names the Teams of the team_joined entries.
func (s *Service) entryTeams(ctx context.Context, entries []TimelineEntry) error {
	var ids []int64
	for _, e := range entries {
		if e.teamID != 0 {
			ids = append(ids, e.teamID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	rows, err := s.store.ListTeamRefs(ctx, dbgen.ListTeamRefsParams{OrgID: s.orgID, Ids: slices.Compact(ids)})
	if err != nil {
		return fmt.Errorf("read the teams of the timeline: %w", err)
	}
	refs := make(map[int64]TeamRef, len(rows))
	for _, r := range rows {
		refs[r.ID] = TeamRef{PublicID: r.PublicID, Name: r.Name, Slug: r.Slug, Deleted: r.Deleted}
	}
	for i := range entries {
		if ref, ok := refs[entries[i].teamID]; ok {
			entries[i].Team = &ref
		}
	}
	return nil
}

// teamFilter resolves the Team filter of the list and the counts (C-09.FR-13, C-25.FR-2): whether it is set and the
// Teams it selects. TeamMine is the Teams of the calling User, none when they are in none; a Service account, which is
// in no Team, is a FieldError, as is an unknown Team. A deleted Team may be named: Alert Groups keep it.
func (s *Service) teamFilter(ctx context.Context, team string, me audit.Actor) (bool, []int64, error) {
	switch team {
	case "":
		return false, []int64{}, nil
	case TeamMine:
		if me.Kind != audit.ActorUser {
			return false, nil, &FieldError{Pointer: "/query/team", Code: CodeUnsupported,
				Detail: "A Service account is in no Team; name a Team instead of mine."}
		}
		ids, err := s.store.ListUserTeamIDs(ctx, dbgen.ListUserTeamIDsParams{OrgID: s.orgID, UserID: me.ID})
		if err != nil {
			return false, nil, fmt.Errorf("read the teams of the caller: %w", err)
		}
		if ids == nil {
			ids = []int64{}
		}
		return true, ids, nil
	}
	refs, err := lookup(publicid.Team, "team", []string{team}, "/query/team", func(ids []string) ([]idRef, error) {
		rows, err := s.store.ListTeamsByPublicID(ctx, dbgen.ListTeamsByPublicIDParams{OrgID: s.orgID, PublicIds: ids})
		out := make([]idRef, len(rows))
		for i, r := range rows {
			out[i] = idRef(r)
		}
		return out, err
	})
	if err != nil {
		return false, nil, err
	}
	return true, idsOf(refs), nil
}
