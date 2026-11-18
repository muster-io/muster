// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package groups

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/groups/dbgen"
)

// The Assignees of an Alert Group (C-32.FR-12): until someone acknowledges, for each chain started on it, running or
// run out, the people its latest step Paging people by name reached by a delivered Thread reply that mentions them,
// each with the owning Team of the chain when its run started. A later step that reaches someone replaces the chain's
// Assignees, the later Mentions of that step's rules add to them, the later Mentions of an earlier step's rules change
// nothing, a step that reaches none of its people leaves them, and a step that Pages a Team or everyone in the chat
// changes nothing; a person a Page did not reach never becomes one. Only system
// transitions of the dispatcher change them — pageSettled, which delivery's SettlePage drives, and Acknowledge, Snooze
// and Resolve, which clear them in their transaction — each change recording the Quiet assignees_changed with the
// Assignees after it; nobody sets them by hand.

// assigneeQueries are the queries of the Assignees.
type assigneeQueries interface {
	ListRunAssignees(ctx context.Context, arg dbgen.ListRunAssigneesParams) ([]dbgen.ListRunAssigneesRow, error)
	UpsertAssignee(ctx context.Context, arg dbgen.UpsertAssigneeParams) error
	DeleteRunAssigneesExcept(ctx context.Context, arg dbgen.DeleteRunAssigneesExceptParams) (int64, error)
	DeleteGroupAssignees(ctx context.Context, arg dbgen.DeleteGroupAssigneesParams) (int64, error)
	ListGroupAssignees(ctx context.Context, arg dbgen.ListGroupAssigneesParams) ([]dbgen.ListGroupAssigneesRow, error)
}

// Assignee is a person a chain of an Alert Group Paged last and reached (Assignee): the User, the owning Team of the
// chain when its run started, nil for none, the chain, nil once it was deleted, and since when.
type Assignee struct {
	User  ActorRef
	Team  *TeamRef
	Chain *ChainRef
	Since time.Time
}

// assigneeRecord is an Assignee by internal ids, as the Timeline entry of assignees_changed keeps the Assignees after
// the change (timeline_entries.assignees).
type assigneeRecord struct {
	UserID int64     `json:"user_id"`
	TeamID int64     `json:"team_id,omitempty"`
	RunID  int64     `json:"run_id"`
	Since  time.Time `json:"since"`
}

// recordsOf are the Assignees of rows as records.
func recordsOf(rows []dbgen.ListGroupAssigneesRow) []assigneeRecord {
	out := make([]assigneeRecord, len(rows))
	for i, r := range rows {
		out[i] = assigneeRecord{UserID: r.UserID, TeamID: r.TeamID.Int64, RunID: r.EscalationRunID, Since: r.Since.UTC()}
	}
	return out
}

// assigneesChanged is the entry of assignees_changed with the Assignees of g after the change, read in its transaction.
func assigneesChanged(ctx context.Context, q Queries, orgID int64, g *Group) (entry, error) {
	rows, err := q.ListGroupAssignees(ctx, dbgen.ListGroupAssigneesParams{OrgID: orgID, AlertGroupIds: []int64{g.ID}})
	if err != nil {
		return entry{}, fmt.Errorf("read the assignees of alert group #%d: %w", g.Number, err)
	}
	recs := recordsOf(rows)
	return entry{Event: EventAssigneesChanged, Assignees: &recs}, nil
}

// reachAssignees updates, in the change c of the locked g, the Assignees of the run of the Page p by the people it
// newly reached, and records assignees_changed when they changed. Only a Page of people of a run of a firing Alert
// Group whose run was not stopped counts. Its run step — that of its first Mentions, or the one that started the rule
// run of a later Mention, p.RunStepSeq — replaces the run's Assignees when it is greater than the one that made them,
// and adds to them when it is that one: another reply of its first Mentions, or a later Mention of that step's rules.
// A reach by a smaller one, such as a late reply or a later Mention of the rule of a step that a later step replaced,
// changes nothing. A Page written before migration 0050 has no run step: it adds only to a run without Assignees, or
// whose Assignees have none either.
func reachAssignees(ctx context.Context, q Queries, orgID int64, g *Group, p dbgen.LockPageRow, reached []int64,
	now time.Time, c *change) error {
	if len(reached) == 0 || p.Team || !p.EscalationRunID.Valid || g.Status != StatusFiring || p.RunState == "stopped" {
		return nil
	}
	run := p.EscalationRunID.Int64
	rows, err := q.ListRunAssignees(ctx, dbgen.ListRunAssigneesParams{OrgID: orgID, AlertGroupID: g.ID,
		EscalationRunID: run})
	if err != nil {
		return fmt.Errorf("read the assignees of alert group #%d: %w", g.Number, err)
	}
	var pass pgtype.Int4
	have := make(map[int64]bool, len(rows))
	for _, r := range rows {
		have[r.UserID] = true
		if r.RunStepSeq.Valid && (!pass.Valid || r.RunStepSeq.Int32 > pass.Int32) {
			pass = r.RunStepSeq
		}
	}
	key := p.RunStepSeq
	replace := len(rows) == 0
	switch {
	case replace:
	case !key.Valid:
		if pass.Valid {
			return nil
		}
	case !pass.Valid || key.Int32 > pass.Int32:
		replace = true
	case key.Int32 < pass.Int32:
		return nil
	}
	changed := false
	if replace {
		n, err := q.DeleteRunAssigneesExcept(ctx, dbgen.DeleteRunAssigneesExceptParams{OrgID: orgID,
			AlertGroupID: g.ID, EscalationRunID: run, Keep: reached})
		if err != nil {
			return fmt.Errorf("replace the assignees of alert group #%d: %w", g.Number, err)
		}
		changed = n > 0
	}
	for _, u := range reached {
		if have[u] && !replace {
			continue
		}
		changed = changed || !have[u]
		if err := q.UpsertAssignee(ctx, dbgen.UpsertAssigneeParams{OrgID: orgID, AlertGroupID: g.ID,
			EscalationRunID: run, StepNumber: p.StepNumber, RunStepSeq: key, UserID: u, TeamID: p.OwningTeamID,
			Since: now}); err != nil {
			return fmt.Errorf("record the assignees of alert group #%d: %w", g.Number, err)
		}
	}
	if !changed {
		return nil
	}
	e, err := assigneesChanged(ctx, q, orgID, g)
	if err != nil {
		return err
	}
	c.add(e)
	return nil
}

// clearAssignees clears the Assignees of g in the change c that made it stop being firing — Acknowledge, Snooze or
// Resolve, by a person or the system — recording assignees_changed with none when it had any (C-32.FR-12,
// C-10.FR-1).
func (d *dispatcher) clearAssignees(ctx context.Context, q Queries, g *Group, from Status, c *change) error {
	if from != StatusFiring || g.Status == StatusFiring {
		return nil
	}
	n, err := q.DeleteGroupAssignees(ctx, dbgen.DeleteGroupAssigneesParams{OrgID: d.orgID, AlertGroupID: g.ID})
	if err != nil {
		return fmt.Errorf("clear the assignees of alert group #%d: %w", g.Number, err)
	}
	if n > 0 {
		none := []assigneeRecord{}
		c.add(entry{Event: EventAssigneesChanged, Assignees: &none})
	}
	return nil
}

// assigneesJSON is the column of the Assignees of an entry, null for an entry of another event.
func assigneesJSON(recs *[]assigneeRecord) []byte {
	if recs == nil {
		return nil
	}
	b, _ := json.Marshal(*recs) // ids and times always encode
	return b
}

// groupAssignees reads the Assignees of the Alert Groups ids, by id, with their names.
func (s *Service) groupAssignees(ctx context.Context, ids []int64) (map[int64][]Assignee, error) {
	out := map[int64][]Assignee{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.store.ListGroupAssignees(ctx, dbgen.ListGroupAssigneesParams{OrgID: s.orgID, AlertGroupIds: ids})
	if err != nil {
		return nil, fmt.Errorf("read the assignees of the alert groups: %w", err)
	}
	name, err := s.assigneeNames(ctx, recordsOf(rows))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.AlertGroupID] = append(out[r.AlertGroupID], name(assigneeRecord{UserID: r.UserID,
			TeamID: r.TeamID.Int64, RunID: r.EscalationRunID, Since: r.Since.UTC()}))
	}
	return out, nil
}

// assigneeNames reads the Users, Teams and chains of the records in one read each and returns what names a record: a
// User deleted since is deactivated, a chain deleted since nil.
func (s *Service) assigneeNames(ctx context.Context, recs []assigneeRecord) (func(assigneeRecord) Assignee, error) {
	var users, teams, runs []int64
	for _, r := range recs {
		users = append(users, r.UserID)
		if r.TeamID != 0 {
			teams = append(teams, r.TeamID)
		}
		runs = append(runs, r.RunID)
	}
	found, err := s.refs(ctx, users, nil)
	if err != nil {
		return nil, err
	}
	teamRefs := map[int64]TeamRef{}
	if len(teams) > 0 {
		slices.Sort(teams)
		rows, err := s.store.ListTeamRefs(ctx, dbgen.ListTeamRefsParams{OrgID: s.orgID, Ids: slices.Compact(teams)})
		if err != nil {
			return nil, fmt.Errorf("read the teams of the assignees: %w", err)
		}
		for _, r := range rows {
			teamRefs[r.ID] = TeamRef{PublicID: r.PublicID, Name: r.Name, Slug: r.Slug, Deleted: r.Deleted}
		}
	}
	chains := map[int64]*ChainRef{}
	if len(runs) > 0 {
		slices.Sort(runs)
		rows, err := s.store.ListTimelineChains(ctx, dbgen.ListTimelineChainsParams{OrgID: s.orgID,
			Ids: slices.Compact(runs)})
		if err != nil {
			return nil, fmt.Errorf("read the escalation chains of the assignees: %w", err)
		}
		for _, r := range rows {
			if !r.ChainPublicID.Valid {
				continue
			}
			ref := &ChainRef{PublicID: r.ChainPublicID.String, Name: r.ChainNameNow.String}
			if r.TeamPublicID.Valid {
				ref.OwningTeam = &TeamRef{PublicID: r.TeamPublicID.String, Name: r.TeamName.String,
					Slug: r.TeamSlug.String, Deleted: r.TeamDeleted}
			}
			chains[r.RunID] = ref
		}
	}
	return func(r assigneeRecord) Assignee {
		a := Assignee{User: cmp.Or(found.users[r.UserID], ActorRef{Kind: "user", Deactivated: true}),
			Chain: chains[r.RunID], Since: r.Since}
		if t, ok := teamRefs[r.TeamID]; ok {
			a.Team = &t
		}
		return a
	}, nil
}

// entryAssignees names the Assignees of the assignees_changed entries.
func (s *Service) entryAssignees(ctx context.Context, entries []TimelineEntry) error {
	var all []assigneeRecord
	for _, e := range entries {
		all = append(all, e.assignees...)
	}
	if len(all) == 0 {
		return nil
	}
	name, err := s.assigneeNames(ctx, all)
	if err != nil {
		return err
	}
	for i := range entries {
		for _, r := range entries[i].assignees {
			entries[i].Assignees = append(entries[i].Assignees, name(r))
		}
	}
	return nil
}
