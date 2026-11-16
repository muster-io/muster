// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/mentions"
	"github.com/muster-io/muster/internal/schedules/dbgen"
)

// TeamOnCall is a Team in "On call now" (C-27.FR-9): its main Schedule, nil for a Team without one, with the segment
// of its Result at the business clock's now and the one that follows within schedule.gap_view, nil when the current
// one lasts beyond it. A segment without a person is a gap. Unreachable are the Destinations of the Team that a
// Mention of the current On-call cannot reach (C-27.FR-8), empty in a gap and without a main Schedule.
type TeamOnCall struct {
	Team     TeamRef
	Schedule *Schedule
	Current  *Segment
	Next     *Segment
	// Until is the end of the current segment, nil when it lasts beyond schedule.gap_view.
	Until       *time.Time
	Unreachable []Unreachable
}

// OnCallNow lists every Team that is not deleted by name — or with memberID those of that User, "My Teams" — each with
// the On-call of its main Schedule now, until when, what follows and the Destinations of the Team that cannot mention
// them (C-27.FR-8, FR-9). The Results of the main Schedules are computed together, from the business clock's now over
// schedule.gap_view, and the reach of their On-calls is read once.
func (s *Service) OnCallNow(ctx context.Context, memberID int64) ([]TeamOnCall, error) {
	p := dbgen.ListOnCallTeamsParams{OrgID: s.orgID}
	if memberID != 0 {
		p.MemberID = pgtype.Int8{Int64: memberID, Valid: true}
	}
	rows, err := s.store.ListOnCallTeams(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("list the teams for on call now: %w", err)
	}
	out := make([]TeamOnCall, len(rows))
	for i := range out {
		out[i].Unreachable = []Unreachable{}
	}
	var mains []Schedule
	var at []int // the position in out of each main Schedule
	for i, r := range rows {
		team := TeamRef{ID: r.ID, PublicID: r.PublicID, Name: r.Name, Slug: r.Slug}
		out[i].Team = team
		if r.ScheduleID.Valid {
			mains = append(mains, Schedule{ID: r.ScheduleID.Int64, PublicID: r.SchedulePublicID.String,
				Name: r.ScheduleName.String, TimeZone: r.ScheduleTimeZone.String, Main: true, Team: team})
			at = append(at, i)
		}
	}
	if len(mains) == 0 {
		return out, nil
	}
	if err := s.withLayers(ctx, s.store, mains); err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	res, err := s.results(ctx, s.store, mains, now, now.Add(GapView))
	if err != nil {
		return nil, err
	}
	teams, people := map[int64]bool{}, map[int64]bool{}
	for j, sc := range mains {
		if u := res[j].Segments[0].User; u != nil {
			teams[sc.Team.ID], people[u.ID] = true, true
		}
	}
	r, err := s.readReach(ctx, s.store, teams, people)
	if err != nil {
		return nil, err
	}
	for j, i := range at {
		sc, segs := mains[j], res[j].Segments
		out[i].Schedule, out[i].Current = &sc, &segs[0]
		if u := segs[0].User; u != nil {
			out[i].Unreachable = r.unreachable(sc.Team.ID, u.ID)
		}
		cur := run(segs, 0)
		if cur == len(segs) {
			continue
		}
		until := segs[cur-1].End
		next := segs[cur]
		next.End = segs[run(segs, cur)-1].End
		out[i].Until, out[i].Next = &until, &next
	}
	return out, nil
}

// run is the end of the run of segments from i on with the person of segs[i], or of gaps: the index of the first
// segment with someone else, len(segs) when there is none. Segments of one person from different sources are one
// span of "On call now".
func run(segs []Segment, i int) int {
	j := i + 1
	for j < len(segs) && samePerson(segs[j].User, segs[i].User) {
		j++
	}
	return j
}

// TeamOnCallAt is the User on Shift in the main Schedule of the Team teamID at t, by id (C-27.FR-4), and false for a
// Team that is deleted, without a main Schedule, or in a gap of it at t: the On-call that a Mention entry mentions
// (C-28.FR-3). It reads through db, the transaction of the resolution, or the main pool for a nil one, so that a caller
// that holds a connection of the pool takes no second one. It satisfies the interface OnCalls that internal/mentions
// declares.
func (s *Service) TeamOnCallAt(ctx context.Context, db mentions.DBTX, teamID int64, t time.Time) (int64, bool,
	error) {
	q := Queries(s.store)
	if db != nil {
		q = s.store.On(db)
	}
	r, err := q.GetTeamMainSchedule(ctx, dbgen.GetTeamMainScheduleParams{OrgID: s.orgID, TeamID: teamID})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read the main schedule of the team %d: %w", teamID, err)
	}
	sc := Schedule{ID: r.ID, PublicID: r.PublicID, Name: r.Name, TimeZone: r.TimeZone, Main: r.Main,
		GapAlert: r.GapAlert, CreatedAt: r.CreatedAt.UTC(), Version: r.Version,
		Team: TeamRef{ID: r.TeamID, PublicID: r.TeamPublicID, Name: r.TeamName, Slug: r.TeamSlug}}
	list := []Schedule{sc}
	if err := s.withLayers(ctx, q, list); err != nil {
		return 0, false, err
	}
	res, err := s.result(ctx, q, list[0], t.UTC(), t.UTC().Add(time.Second))
	if err != nil {
		return 0, false, err
	}
	if len(res.Segments) == 0 || res.Segments[0].User == nil {
		return 0, false, nil
	}
	return res.Segments[0].User.ID, true, nil
}
