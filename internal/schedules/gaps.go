// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/muster-io/muster/internal/schedules/dbgen"
)

// WarningGap is the kind of the warning of a gap of the Result (C-27.FR-7).
const WarningGap = "gap"

// Gap is a time in which nobody is On-call in a Schedule: no Override and no Rotation layer has a Shift with a person
// who may be Paged (C-27.FR-5).
type Gap struct {
	Start, End time.Time
}

// Gaps are the gaps of the Result in order, cut to its period: its segments without a person. Adjacent segments of
// nobody are one segment already, so two gaps never touch.
func (r Result) Gaps() []Gap {
	var out []Gap
	for _, seg := range r.Segments {
		if seg.User == nil {
			out = append(out, Gap{Start: seg.Start, End: seg.End})
		}
	}
	return out
}

// gapWarnings are the warnings gap of the gaps of the Result, one for each.
func gapWarnings(r Result) []Warning {
	gaps := r.Gaps()
	out := make([]Warning, len(gaps))
	for i, g := range gaps {
		out[i] = Warning{Kind: WarningGap, Start: g.Start, End: g.End}
	}
	return out
}

// withNextGaps sets the next gap of each Schedule: the first gap from the business clock's now within
// schedule.gap_view. A gap that has begun starts at now.
func (s *Service) withNextGaps(ctx context.Context, q Queries, list []Schedule) error {
	if len(list) == 0 {
		return nil
	}
	now := s.clock.Now().UTC()
	res, err := s.results(ctx, q, list, now, now.Add(GapView))
	if err != nil {
		return err
	}
	for i := range list {
		list[i].NextGap = nil
		if gaps := res[i].Gaps(); len(gaps) > 0 {
			list[i].NextGap = &gaps[0]
		}
	}
	return nil
}

// results are the Results of the Schedules from from to to, in their order, with whether their people may be Paged
// read once from their current state. However many Schedules there are, it reads the Overrides of the period once
// and the people once, so that a list, the gap scan and "On call now" make two reads, not two per Schedule.
func (s *Service) results(ctx context.Context, q Queries, list []Schedule, from, to time.Time) ([]Result, error) {
	if len(list) == 0 {
		return []Result{}, nil
	}
	ids := make([]int64, len(list))
	for i, sc := range list {
		ids[i] = sc.ID
	}
	rows, err := q.ListPeriodOverrides(ctx, dbgen.ListPeriodOverridesParams{OrgID: s.orgID, ScheduleIds: ids,
		PeriodStart: from, PeriodEnd: to})
	if err != nil {
		return nil, fmt.Errorf("list the overrides of the schedules: %w", err)
	}
	overrides := map[int64][]Override{}
	for _, r := range rows {
		overrides[r.ScheduleID] = append(overrides[r.ScheduleID], overrideOf(periodOverrideRow(r)))
	}
	named := make([][]int64, len(list)) // the people each Schedule names in the period
	teams, users := map[int64]bool{}, map[int64]bool{}
	for i, sc := range list {
		teams[sc.Team.ID] = true
		for _, l := range sc.RotationLayers {
			for _, u := range l.Users {
				named[i] = append(named[i], u.ID)
			}
		}
		for _, o := range overrides[sc.ID] {
			named[i] = append(named[i], o.User.ID)
		}
		for _, id := range named[i] {
			users[id] = true
		}
	}
	people, err := s.pageableInTeams(ctx, q, teams, users)
	if err != nil {
		return nil, err
	}
	out := make([]Result, len(list))
	for i, sc := range list {
		loc, err := location(sc.TimeZone)
		if err != nil {
			loc = time.UTC
		}
		skipped := make(map[int64]string, len(named[i]))
		for _, id := range named[i] {
			if p, ok := people[id]; ok {
				skipped[id] = skipReason(Person{Role: p.Role, Status: p.Status}, slices.Contains(p.TeamIds, sc.Team.ID))
			}
		}
		out[i] = compute(loc, sc.RotationLayers, overrides[sc.ID], skipped, from, to)
	}
	return out, nil
}

// pageableInTeams reads the people users with their Role, their status and the Teams among teams they are in, which
// decide whether they may be Paged in each, by User id.
func (s *Service) pageableInTeams(ctx context.Context, q Queries, teams, users map[int64]bool) (
	map[int64]dbgen.ListPageableInTeamsRow, error) {
	out := make(map[int64]dbgen.ListPageableInTeamsRow, len(users))
	if len(users) == 0 {
		return out, nil
	}
	rows, err := q.ListPageableInTeams(ctx, dbgen.ListPageableInTeamsParams{OrgID: s.orgID,
		TeamIds: sortedKeys(teams), UserIds: sortedKeys(users)})
	if err != nil {
		return nil, fmt.Errorf("read whether the people may be paged: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

func sortedKeys(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// periodOverrideRow is a row of the Overrides of a period as every read of an Override gives it.
func periodOverrideRow(r dbgen.ListPeriodOverridesRow) overrideRow {
	return overrideRow{ID: r.ID, PublicID: r.PublicID, UserID: r.UserID, StartsAt: r.StartsAt, EndsAt: r.EndsAt,
		Version: r.Version, CreatedAt: r.CreatedAt, UserPublicID: r.UserPublicID, UserName: r.UserName,
		UserLogin: r.UserLogin, UserStatus: r.UserStatus, ReplacesPublicID: r.ReplacesPublicID,
		ReplacesName: r.ReplacesName, ReplacesLogin: r.ReplacesLogin, ReplacesStatus: r.ReplacesStatus,
		ByUserPublicID: r.ByUserPublicID, ByUserName: r.ByUserName, ByUserLogin: r.ByUserLogin,
		ByUserStatus: r.ByUserStatus, ByServiceAccountPublicID: r.ByServiceAccountPublicID,
		ByServiceAccountName: r.ByServiceAccountName, ByServiceAccountStatus: r.ByServiceAccountStatus}
}
