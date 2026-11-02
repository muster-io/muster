// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package routing

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/routing/dbgen"
)

// TestLabelSuggestions is C-29.FR-2: the suggestions of "Labels in the message" are the labels of the Route's Alert
// Groups of routing.group_key_preview_period, most frequent first, at most MaxLabelSuggestions, read for the Route by
// its id in the Organization from at most its 200 most recent Alert Groups and 5,000 of their memberships; an unknown
// Route is ErrNotFound and a failed read is returned.
func TestLabelSuggestions(t *testing.T) {
	svc, store, c := newService(t)
	r, err := svc.Create(t.Context(), by, input("infra"))
	if err != nil {
		t.Fatal(err)
	}
	store.suggestions = map[int64][]dbgen.ListRouteLabelSuggestionsRow{r.ID: {
		{Label: "pod", ExampleValue: "checkout-api-7d9f4", AlertGroupCount: 3},
		{Label: "cluster", ExampleValue: "prod", AlertGroupCount: 1},
	}}
	got, err := svc.LabelSuggestions(t.Context(), r.PublicID)
	want := []LabelSuggestion{{Label: "pod", ExampleValue: "checkout-api-7d9f4", AlertGroupCount: 3},
		{Label: "cluster", ExampleValue: "prod", AlertGroupCount: 1}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("suggestions %+v, %v", got, err)
	}
	if a := store.suggested[0]; a.OrgID != orgID || a.RouteID != r.ID || a.Lim != MaxLabelSuggestions ||
		a.GroupLim != 200 || a.AlertLim != 5000 || !a.Since.Equal(c.Now().Add(-24*time.Hour)) {
		t.Errorf("read %+v", a)
	}
	if got, err := svc.LabelSuggestions(t.Context(), "RT0000000000ZZ"); !errors.Is(err, ErrNotFound) || got != nil {
		t.Errorf("unknown route: %v, %v", got, err)
	}
	store.fail["ListRouteLabelSuggestions"] = errBoom
	if _, err := svc.LabelSuggestions(t.Context(), r.PublicID); !errors.Is(err, errBoom) {
		t.Errorf("failing read: %v", err)
	}
}
