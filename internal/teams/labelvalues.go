// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"context"
	"fmt"
	"time"

	"github.com/muster-io/muster/internal/teams/dbgen"
)

// TeamValuesWindow is organization.team_values_window (P-75): the values of the Team label without a Team seen in the
// last 14 days, like organization.severity_values_window.
const TeamValuesWindow = 14 * 24 * time.Hour

// maxLabelValues bounds the list of values without a Team, which has no pagination: the largest page of the API.
const maxLabelValues = 500

// LabelValue is a value of the Team label that names no Team (C-25.FR-7): when an Alert that carries it was last seen,
// how many Alerts last seen within the window carry it, and whether it is a valid slug (team.slug_format), which a
// Team could take; one that is not reaches a Team only through a Team matcher.
type LabelValue struct {
	Value      string
	LastSeenAt time.Time
	Alerts     int64
	ValidSlug  bool
}

// LabelValues are the values of the Team label Label without a Team, seen within the window of WindowDays days.
type LabelValues struct {
	Label      string
	WindowDays int
	Items      []LabelValue
}

// LabelValues lists the values of the Organization's Team label, with the Static labels of their Integrations, that the
// Alerts last seen within TeamValuesWindow of the business clock carried and that name no Team that is not deleted, the
// most recently seen first (C-25.FR-7, AC-1).
func (s *Service) LabelValues(ctx context.Context) (LabelValues, error) {
	label, err := s.store.GetTeamLabel(ctx, s.orgID)
	if err != nil {
		return LabelValues{}, fmt.Errorf("read the team label: %w", err)
	}
	rows, err := s.store.ListTeamLabelValues(ctx, dbgen.ListTeamLabelValuesParams{OrgID: s.orgID, Label: label,
		Since: s.clock.Now().UTC().Add(-TeamValuesWindow), MaxValues: maxLabelValues})
	if err != nil {
		return LabelValues{}, fmt.Errorf("read the values of the team label without a team: %w", err)
	}
	out := LabelValues{Label: label, WindowDays: int(TeamValuesWindow / (24 * time.Hour)),
		Items: make([]LabelValue, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, LabelValue{Value: r.Value, LastSeenAt: r.LastSeenAt.UTC(), Alerts: r.Alerts,
			ValidSlug: slugFormat.MatchString(r.Value)})
	}
	return out, nil
}
