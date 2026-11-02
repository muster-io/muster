// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/muster-io/muster/internal/teams/dbgen"
)

// TestLabelValues is C-25.FR-7 and AC-1 on the Service: the values of the Organization's Team label without a Team,
// read within organization.team_values_window of the business clock (14 days, P-75) and at most 500, in the order the
// query gives them, each with when it was last seen, its number of Alerts and whether it is a valid slug
// (team.slug_format) — `checkout` is one, `Payments` is not; the label and the window in days come with them. A
// failing read fails the list. The query itself — the window, the Static labels and the slugs of the Teams that are
// not deleted — is TestIntegrationLabelValues.
func TestLabelValues(t *testing.T) {
	svc, store := newService(t)
	ctx := t.Context()
	store.label = "team"
	seen := t0.Add(-time.Hour)
	store.values = []dbgen.ListTeamLabelValuesRow{{Value: "checkout", LastSeenAt: seen.In(time.FixedZone("x", 3600)),
		Alerts: 2}, {Value: "Payments", LastSeenAt: t0.Add(-2 * time.Hour), Alerts: 1}}
	got, err := svc.LabelValues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := LabelValues{Label: "team", WindowDays: 14, Items: []LabelValue{
		{Value: "checkout", LastSeenAt: seen, Alerts: 2, ValidSlug: true},
		{Value: "Payments", LastSeenAt: t0.Add(-2 * time.Hour), Alerts: 1}}}
	if got.Label != want.Label || got.WindowDays != want.WindowDays || !slices.Equal(got.Items, want.Items) ||
		got.Items[0].LastSeenAt.Location() != time.UTC {
		t.Errorf("values %+v, want %+v", got, want)
	}
	if a := store.valueArgs; len(a) != 1 || a[0] != (dbgen.ListTeamLabelValuesParams{OrgID: orgID, Label: "team",
		Since: t0.Add(-14 * 24 * time.Hour), MaxValues: 500}) {
		t.Errorf("query %+v", a)
	}
	store.values = nil
	if got, err := svc.LabelValues(ctx); err != nil || got.Items == nil || len(got.Items) != 0 {
		t.Errorf("no values = %+v, %v", got, err)
	}
	for _, name := range []string{"GetTeamLabel", "ListTeamLabelValues"} {
		store.fail[name] = errBoom
		if _, err := svc.LabelValues(ctx); !errors.Is(err, errBoom) {
			t.Errorf("%s fails = %v", name, err)
		}
		delete(store.fail, name)
	}
}
