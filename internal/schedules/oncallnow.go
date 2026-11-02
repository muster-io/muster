// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package schedules

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/muster-io/muster/internal/schedules/dbgen"
)

// TeamOnCall is a Team in "On call now" (C-27.FR-9): its main Schedule, nil for a Team without one, with the segment
// of its Result at the business clock's now and the one that follows within schedule.gap_view, nil when the current
// one lasts beyond it. A segment without a person is a gap.
type TeamOnCall struct {
	Team     TeamRef
	Schedule *Schedule
	Current  *Segment
	Next     *Segment
	// Until is the end of the current segment, nil when it lasts beyond schedule.gap_view.
	Until *time.Time
}

// OnCallNow lists every Team that is not deleted by name — or with memberID those of that User, "My Teams" — each with
// the On-call of its main Schedule now, until when and what follows (C-27.FR-9). The Results of the main Schedules are
// computed together, from the business clock's now over schedule.gap_view.
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
	for j, i := range at {
		sc, segs := mains[j], res[j].Segments
		out[i].Schedule, out[i].Current = &sc, &segs[0]
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
