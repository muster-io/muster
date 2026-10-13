// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package organization

import (
	"context"
	"fmt"

	"github.com/muster-io/muster/internal/organization/dbgen"
)

// SeverityValuesWindow is organization.severity_values_window: the values of the severity label seen in the last 14
// days.
const SeverityValuesWindow = 14 * day

// maxSeverityValues bounds the list of values seen, which has no pagination: the largest page of the API.
const maxSeverityValues = 500

// SeverityValue is a value of the severity label seen: the number of Alerts that carry it and its level by the
// current mapping; an unmapped value counts as warning.
type SeverityValue struct {
	Value  string
	Count  int64
	Level  SeverityLevel
	Mapped bool
}

// SeverityValues are the values of the severity label seen within the window of WindowDays days.
type SeverityValues struct {
	Items      []SeverityValue
	WindowDays int
}

// SeverityValues lists the values of the Organization's severity label among the Alerts last seen within
// SeverityValuesWindow of the business clock, the most frequent first, each with its number of Alerts and the level of
// the current mapping (C-20.FR-1, AC-4).
func (s *Service) SeverityValues(ctx context.Context) (SeverityValues, error) {
	o, err := s.get(ctx, s.store)
	if err != nil {
		return SeverityValues{}, err
	}
	rows, err := s.store.ListSeverityValues(ctx, dbgen.ListSeverityValuesParams{
		OrgID: s.orgID, Label: o.SeverityLabel, Since: s.clock.Now().UTC().Add(-SeverityValuesWindow),
		MaxValues: maxSeverityValues,
	})
	if err != nil {
		return SeverityValues{}, fmt.Errorf("read the severity values seen: %w", err)
	}
	out := SeverityValues{Items: make([]SeverityValue, 0, len(rows)), WindowDays: int(SeverityValuesWindow / day)}
	for _, r := range rows {
		v := SeverityValue{Value: r.Value, Count: r.Alerts, Level: SeverityWarning}
		for _, m := range o.SeverityMapping {
			if m.Value == r.Value {
				v.Level, v.Mapped = m.Level, true
				break
			}
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}
