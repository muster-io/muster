// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/muster-io/muster/internal/audit"
	"github.com/muster-io/muster/internal/groups/dbgen"
	"github.com/muster-io/muster/internal/publicid"
	"github.com/muster-io/muster/internal/teams"
	teamsdb "github.com/muster-io/muster/internal/teams/dbgen"
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
	if _, err := joinTeams(ctx, q, e.s.orgID, g, brought, e.now, c); err != nil {
		return err
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

// joinTeams adds the Teams brought to the Teams of g, which only grow (C-25.FR-6): each Team g lacks joins it with one
// Quiet team_joined naming the Team and the rule that brought it, in the order of the Teams' ids; a Team g has keeps
// its rule and when it joined. It returns how many joined.
func joinTeams(ctx context.Context, q Queries, orgID int64, g *Group, brought map[int64]joiningTeam, now time.Time,
	c *change) (int, error) {
	if len(brought) == 0 {
		return 0, nil
	}
	p := dbgen.AddGroupTeamsParams{OrgID: orgID, AlertGroupID: g.ID, JoinedAt: now}
	for _, id := range slices.Sorted(maps.Keys(brought)) {
		t := brought[id]
		p.TeamIds = append(p.TeamIds, t.id)
		p.Rules = append(p.Rules, t.rule)
		p.Matchers = append(p.Matchers, t.matcher)
	}
	joined, err := q.AddGroupTeams(ctx, p)
	if err != nil {
		return 0, fmt.Errorf("add the teams of alert group #%d: %w", g.Number, err)
	}
	slices.Sort(joined)
	for _, id := range joined {
		t := brought[id]
		c.add(entry{Event: EventTeamJoined, TeamID: &t.id, TeamRule: t.rule, TeamMatcher: t.matcher})
	}
	return len(joined), nil
}

// classifiedAlert is a firing Alert of an open Alert Group with the Teams it belongs to by the current classification.
type classifiedAlert struct {
	id    int64
	teams []teams.Membership
}

// teamsEvaluated is the system transition TeamsEvaluated of an open Alert Group after a save of a Team or of the Team
// label (C-25.FR-13): the Teams its firing Alerts now belong to are added to those recorded on each Alert, and each
// Team the Alert Group lacks joins it with one Quiet team_joined, which re-renders its Root messages Quietly — by the
// label when any of the Alerts belongs to it by the label, else with the first Team matcher by Alert, as grouping
// records it. It only adds — a Team that an Alert no longer belongs to stays where it is — records no
// unknown_team_value, and leaves an Alert Group without a new Team as it is, so that a second run records nothing;
// joined says whether a Team joined.
type teamsEvaluated struct {
	orgID  int64
	alerts []classifiedAlert
	now    time.Time
	joined bool
}

func (*teamsEvaluated) precondition(g *Group) error {
	if g.Status == StatusResolved {
		return errNotOpen
	}
	return nil
}

func (t *teamsEvaluated) apply(ctx context.Context, q Queries, g *Group, c *change) error {
	p := dbgen.AddAlertTeamsParams{OrgID: t.orgID}
	brought := map[int64]joiningTeam{}
	for _, a := range t.alerts {
		for _, m := range a.teams {
			p.AlertIds = append(p.AlertIds, a.id)
			p.TeamIds = append(p.TeamIds, m.TeamID)
			p.Rules = append(p.Rules, m.Rule)
			p.Matchers = append(p.Matchers, m.Matcher)
			if b, ok := brought[m.TeamID]; ok && (b.rule == TeamRuleLabel || m.Rule != TeamRuleLabel) {
				continue
			}
			brought[m.TeamID] = joiningTeam{id: m.TeamID, rule: m.Rule, matcher: m.Matcher}
		}
	}
	if err := q.AddAlertTeams(ctx, p); err != nil {
		return fmt.Errorf("add the teams of the alerts of alert group #%d: %w", g.Number, err)
	}
	n, err := joinTeams(ctx, q, t.orgID, g, brought, t.now, c)
	t.joined, c.configuration = n > 0, true
	return err
}

// evaluateTeams evaluates the Teams of the Alert Groups ids again (C-25.FR-13), in the transaction of q: it locks them
// in id order, classifies the labels of the Alerts firing in each with the classification of the Teams as of the
// transaction, and runs TeamsEvaluated through the dispatcher on each open one that has an Alert of a Team, in the
// order (Route, id), as the re-render of a button set goes, after it took the membership locks of their Routes and then
// of their Teams — those they have and those the evaluation brings — each in id order (shareMemberships), so that
// no dispatch takes a Team's lock below one the batch holds. Of an
// Alert Group a person resolved that is still in its Grace period it only records the Teams on its firing tails,
// which the end of the Grace period groups again with them; the Alert Group itself does not change. Only an Alert
// whose own status is firing is classified: one that a Snapshot fires again is left to that Snapshot's routing, which
// records its Teams in its own transaction. The batch took the lock of the evaluation before it listed ids
// (updateBatch). It returns the Alert Groups a Team joined, by id; after collects what runs once the transaction
// committed.
func (s *Service) evaluateTeams(ctx context.Context, q Queries, ids []int64, after *committed) ([]int64, error) {
	ev, err := s.classifyTeams(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	if err := s.shareMemberships(ctx, q, ev.open, ev.teams()); err != nil {
		return nil, err
	}
	return s.joinTeams(ctx, q, ev, after)
}

// teamsBatch is a batch of the evaluation of the Teams: its open Alert Groups with classified firing Alerts, locked,
// in the order (Route, id), and the classified Alerts of each.
type teamsBatch struct {
	open   []*Group
	firing map[int64][]classifiedAlert
}

// teams are the Teams the classified Alerts of the batch belong to, in id order.
func (e teamsBatch) teams() []int64 {
	var out []int64
	for _, alerts := range e.firing {
		for _, a := range alerts {
			for _, m := range a.teams {
				out = append(out, m.TeamID)
			}
		}
	}
	return slices.Compact(slices.Sorted(slices.Values(out)))
}

// classifyTeams locks the Alert Groups ids in id order, classifies the labels of the Alerts firing in them with the
// classification of the Teams as of the transaction of q, adds the missing alert_teams rows of the firing tails of an
// Alert Group a person resolved that is still in its Grace period, and returns the batch of the open ones. The
// Teams the evaluation brings are known before any dispatch, so their membership locks can be taken up front, by id,
// after the Routes' (shareMemberships).
func (s *Service) classifyTeams(ctx context.Context, q Queries, ids []int64) (teamsBatch, error) {
	if len(ids) == 0 {
		return teamsBatch{}, nil
	}
	locked, err := lock(ctx, q, s.orgID, ids)
	if err != nil {
		return teamsBatch{}, err
	}
	st, err := q.GetTeamsStamp(ctx, s.orgID)
	if err != nil {
		return teamsBatch{}, fmt.Errorf("read the stamp of the teams: %w", err)
	}
	classes, err := s.classifier.At(ctx, s.classify(q), teams.Stamp{Label: st.TeamLabel, Version: st.TeamsVersion})
	if err != nil {
		return teamsBatch{}, err
	}
	rows, err := q.ListGroupFiringAlerts(ctx, dbgen.ListGroupFiringAlertsParams{OrgID: s.orgID, AlertGroupIds: ids})
	if err != nil {
		return teamsBatch{}, fmt.Errorf("read the alerts firing in the alert groups: %w", err)
	}
	firing := map[int64][]classifiedAlert{}
	for _, r := range rows {
		if r.Status != string(StatusFiring) {
			continue
		}
		var labels map[string]string
		if err := json.Unmarshal(r.Labels, &labels); err != nil {
			return teamsBatch{}, fmt.Errorf("read the labels of alert %d: %w", r.AlertID, err)
		}
		if c := classes.Classify(labels); len(c.Teams) > 0 {
			firing[r.AlertGroupID] = append(firing[r.AlertGroupID], classifiedAlert{id: r.AlertID, teams: c.Teams})
		}
	}
	open := make([]*Group, 0, len(firing))
	var tails dbgen.AddAlertTeamsParams
	for _, id := range slices.Sorted(maps.Keys(firing)) {
		switch g := locked[id]; {
		case g == nil:
		case g.Status != StatusResolved:
			open = append(open, g)
		case g.GraceDeadline != nil:
			for _, a := range sortedAlerts(firing[id]) {
				for _, m := range a.teams {
					tails.AlertIds = append(tails.AlertIds, a.id)
					tails.TeamIds = append(tails.TeamIds, m.TeamID)
					tails.Rules = append(tails.Rules, m.Rule)
					tails.Matchers = append(tails.Matchers, m.Matcher)
				}
			}
		}
	}
	if len(tails.AlertIds) > 0 {
		tails.OrgID = s.orgID
		if err := q.AddAlertTeams(ctx, tails); err != nil {
			return teamsBatch{}, fmt.Errorf("add the teams of the alerts in grace periods: %w", err)
		}
	}
	slices.SortFunc(open, func(a, b *Group) int {
		return cmp.Or(cmp.Compare(a.RouteID, b.RouteID), cmp.Compare(a.ID, b.ID))
	})
	return teamsBatch{open: open, firing: firing}, nil
}

// joinTeams changes each open Alert Group of the evaluation ev through the dispatcher as the system transition
// TeamsEvaluated, and returns those a Team joined; after collects what runs once the transaction committed.
func (s *Service) joinTeams(ctx context.Context, q Queries, ev teamsBatch, after *committed) ([]int64, error) {
	now := s.d.clock.Now().UTC()
	var joined []int64
	for _, g := range ev.open {
		t := &teamsEvaluated{orgID: s.orgID, alerts: sortedAlerts(ev.firing[g.ID]), now: now}
		if err := s.d.dispatch(ctx, q, g, System, t, after); errors.Is(err, errNotOpen) {
			continue
		} else if err != nil {
			return nil, err
		}
		if t.joined {
			joined = append(joined, g.ID)
		}
	}
	return joined, nil
}

// sortedAlerts are the classified Alerts of an Alert Group by id.
func sortedAlerts(alerts []classifiedAlert) []classifiedAlert {
	return slices.SortedFunc(slices.Values(alerts), func(a, b classifiedAlert) int { return cmp.Compare(a.id, b.id) })
}

// classifyQueries are the queries of the classification by Teams in the transaction of q.
func classifyQueries(q Queries) teams.ClassifyQueries { return teamsdb.New(q.DB()) }

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

// byTeam resolves the filter by Team of the list and the counts (C-09.FR-13, C-25.FR-2): whether it is set and the
// Teams it selects. TeamMine is the Teams of the calling User, none when they are in none; a Service account, which is
// in no Team, is a FieldError, as is an unknown Team. A deleted Team may be named: Alert Groups keep it.
func (s *Service) byTeam(ctx context.Context, team string, me audit.Actor) (bool, []int64, error) {
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
