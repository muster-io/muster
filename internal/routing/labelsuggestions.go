// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"context"
	"fmt"

	"github.com/muster-io/muster/internal/routing/dbgen"
)

// MaxLabelSuggestions is the most labels listRouteLabelSuggestions offers for "Labels in the message" (C-29.FR-2);
// suggestionGroups and suggestionAlerts bound what it reads: the most recent Alert Groups of the Route within
// routing.group_key_preview_period and the most recent of their Alerts.
const (
	MaxLabelSuggestions = 50
	suggestionGroups    = 200
	suggestionAlerts    = 5000
)

// LabelSuggestion is a label the Route form suggests for "Labels in the message" (C-29.FR-2): a label of the Alerts of
// the Route's recent Alert Groups, its most frequent value as an example, and how many of those Alert Groups carry it.
type LabelSuggestion struct {
	Label           string
	ExampleValue    string
	AlertGroupCount int64
}

// LabelSuggestions lists the labels of the Alerts of the Alert Groups of the Route publicID created within
// routing.group_key_preview_period of the business clock — at most the 200 most recent of them and 5,000 of their
// Alerts — most frequent first, at most MaxLabelSuggestions; a deleted or unknown Route is ErrNotFound.
func (s *Service) LabelSuggestions(ctx context.Context, publicID string) ([]LabelSuggestion, error) {
	r, err := s.Get(ctx, publicID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListRouteLabelSuggestions(ctx, dbgen.ListRouteLabelSuggestionsParams{OrgID: s.orgID,
		RouteID: r.ID, Since: s.clock.Now().UTC().Add(-PreviewPeriod), GroupLim: suggestionGroups,
		AlertLim: suggestionAlerts, Lim: MaxLabelSuggestions})
	if err != nil {
		return nil, fmt.Errorf("list the labels of the recent alert groups of the route %s: %w", r.PublicID, err)
	}
	out := make([]LabelSuggestion, 0, len(rows))
	for _, row := range rows {
		out = append(out, LabelSuggestion{Label: row.Label, ExampleValue: row.ExampleValue,
			AlertGroupCount: row.AlertGroupCount})
	}
	return out, nil
}
