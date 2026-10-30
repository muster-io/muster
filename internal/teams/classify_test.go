// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package teams

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/muster-io/muster/internal/teams/dbgen"
)

// fakeClassify is the database of the classification: Teams with their slugs, which deleted marks, and their Team
// matchers in their order.
type fakeClassify struct {
	teams    []dbgen.ListClassifiedTeamsRow
	deleted  map[int64]bool
	matchers []dbgen.ListClassifiedMatchersRow
	reads    int
	fail     map[string]error
}

func (f *fakeClassify) ListClassifiedTeams(_ context.Context, org int64) ([]dbgen.ListClassifiedTeamsRow, error) {
	f.reads++
	if err := f.fail["ListClassifiedTeams"]; err != nil || org != orgID {
		return nil, err
	}
	var out []dbgen.ListClassifiedTeamsRow
	for _, t := range f.teams {
		if !f.deleted[t.ID] {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeClassify) ListClassifiedMatchers(_ context.Context, org int64) ([]dbgen.ListClassifiedMatchersRow,
	error) {
	if err := f.fail["ListClassifiedMatchers"]; err != nil || org != orgID {
		return nil, err
	}
	var out []dbgen.ListClassifiedMatchersRow
	for _, m := range f.matchers {
		if !f.deleted[m.TeamID] {
			out = append(out, m)
		}
	}
	return out, nil
}

// The Teams of the tests: payments, checkout with the Team matchers of C-25.AC-2, and search, which one matcher of
// each operator but =~ names.
func newClassify() *fakeClassify {
	return &fakeClassify{teams: []dbgen.ListClassifiedTeamsRow{{ID: 1, PublicID: "TM0000000000PA", Name: "Payments",
		Slug: "payments"}, {ID: 2, PublicID: "TM0000000000CH", Name: "Checkout", Slug: "checkout-team"},
		{ID: 3, PublicID: "TM0000000000SE", Name: "Search", Slug: "search"}},
		deleted: map[int64]bool{}, fail: map[string]error{},
		matchers: []dbgen.ListClassifiedMatchersRow{
			{TeamID: 2, Label: "namespace", Op: "=~", Value: "checkout|billing"},
			{TeamID: 2, Label: "pod", Op: "=~", Value: "checkout-.*"},
			{TeamID: 3, Label: "service", Op: "=", Value: "search-api"},
			{TeamID: 3, Label: "cluster", Op: "!~", Value: "prod-.*|"},
			{TeamID: 3, Label: "app", Op: "!=", Value: ""},
		}}
}

// TestClassify is C-25.FR-5, C-25.AC-1 and C-25.AC-2: an Alert belongs to the Team whose slug equals the value of the
// Team label exactly, by the rule label, and to every Team one of whose Team matchers picks it, by the rule matcher
// with the first such matcher, each operator as in Route Matchers and a missing label counting as the empty value; a
// Team both pick belongs by the label, and the slug of one Team with a matcher of another gives both; a value of the
// Team label that names no Team is the unknown value, whatever the matchers give; a deleted Team is no Team.
func TestClassify(t *testing.T) {
	f := newClassify()
	c := NewClassifier(orgID)
	classes, err := c.At(t.Context(), f, Stamp{Label: "team", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	m := func(team int64, rule, matcher string) Membership {
		return Membership{TeamID: team, Rule: rule, Matcher: matcher}
	}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   Classification
	}{
		{"the slug exactly", map[string]string{"team": "payments", "cluster": "prod-1"},
			Classification{Teams: []Membership{m(1, RuleLabel, "")}}},
		{"another case is no Team", map[string]string{"team": "Payments", "pod": "x-1", "cluster": "prod-1"},
			Classification{UnknownValue: "Payments"}},
		{"the first matcher of checkout", map[string]string{"namespace": "billing", "cluster": "prod-1"},
			Classification{Teams: []Membership{m(2, RuleMatcher, `namespace=~"checkout|billing"`)}}},
		{"the second matcher of checkout", map[string]string{"pod": "checkout-api-1", "cluster": "prod-1"},
			Classification{Teams: []Membership{m(2, RuleMatcher, `pod=~"checkout-.*"`)}}},
		{"no matcher picks it", map[string]string{"namespace": "search", "cluster": "prod-1"}, Classification{}},
		{"the slug of one Team and a matcher of another", map[string]string{"team": "payments",
			"namespace": "checkout", "cluster": "prod-1"},
			Classification{Teams: []Membership{m(1, RuleLabel, ""), m(2, RuleMatcher, `namespace=~"checkout|billing"`)}}},
		{"both rules of one Team", map[string]string{"team": "checkout-team", "pod": "checkout-a", "cluster": "prod-1"},
			Classification{Teams: []Membership{m(2, RuleLabel, "")}}},
		{"=", map[string]string{"service": "search-api", "cluster": "prod-1"},
			Classification{Teams: []Membership{m(3, RuleMatcher, `service="search-api"`)}}},
		{"!~, a missing label counting as empty", map[string]string{"cluster": "staging"},
			Classification{Teams: []Membership{m(3, RuleMatcher, `cluster!~"prod-.*|"`)}}},
		{"!=", map[string]string{"app": "web", "cluster": "prod-1"},
			Classification{Teams: []Membership{m(3, RuleMatcher, `app!=""`)}}},
		{"an unknown value and a matcher", map[string]string{"team": "ops", "namespace": "billing",
			"cluster": "prod-1"}, Classification{Teams: []Membership{m(2, RuleMatcher, `namespace=~"checkout|billing"`)},
			UnknownValue: "ops"}},
		{"an empty value", map[string]string{"team": "", "cluster": "prod-1"}, Classification{}},
	} {
		if got := classes.Classify(tc.labels); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}

	f.deleted[1] = true
	classes, err = c.At(t.Context(), f, Stamp{Label: "team", Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := classes.Classify(map[string]string{"team": "payments", "cluster": "prod-1"}); !reflect.DeepEqual(got,
		Classification{UnknownValue: "payments"}) {
		t.Errorf("a deleted Team's slug: %+v", got)
	}
}

// TestClassifierCache is C-25.FR-5 on the cache of each replica: the Teams are read once per Stamp, read again when
// teams_version or the Team label differs, which a new Team label also changes, and a failed read leaves the cache.
func TestClassifierCache(t *testing.T) {
	f := newClassify()
	c := NewClassifier(orgID)
	at := func(st Stamp) *Classes {
		t.Helper()
		out, err := c.At(t.Context(), f, st)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := at(Stamp{Label: "team", Version: 4})
	if again := at(Stamp{Label: "team", Version: 4}); again != first || f.reads != 1 {
		t.Errorf("read %d times", f.reads)
	}
	f.teams = append(f.teams, dbgen.ListClassifiedTeamsRow{ID: 4, PublicID: "TM0000000000OP", Name: "Ops",
		Slug: "ops"})
	if got := first.Classify(map[string]string{"team": "ops"}); got.UnknownValue != "ops" {
		t.Errorf("the cached Teams: %+v", got)
	}
	if got := at(Stamp{Label: "team", Version: 5}).Classify(map[string]string{"team": "ops"}); f.reads != 2 ||
		len(got.Teams) != 1 || got.Teams[0].TeamID != 4 {
		t.Errorf("after a change of the Teams: %+v, %d reads", got, f.reads)
	}
	if got := at(Stamp{Label: "owner", Version: 5}).Classify(map[string]string{"team": "ops", "owner": "payments"}); f.reads != 3 ||
		len(got.Teams) != 1 || got.Teams[0].TeamID != 1 || got.Teams[0].Rule != RuleLabel {
		t.Errorf("after a new Team label: %+v, %d reads", got, f.reads)
	}
	for _, name := range []string{"ListClassifiedTeams", "ListClassifiedMatchers"} {
		f.fail[name] = errors.New("boom")
		if _, err := c.At(t.Context(), f, Stamp{Label: "team", Version: 6}); err == nil {
			t.Errorf("%s fails: no error", name)
		}
		delete(f.fail, name)
	}
	f.matchers = append(f.matchers, dbgen.ListClassifiedMatchersRow{TeamID: 4, Label: "pod", Op: "=~", Value: "("},
		dbgen.ListClassifiedMatchersRow{TeamID: 99, Label: "pod", Op: "=", Value: "x"})
	if _, err := c.At(t.Context(), f, Stamp{Label: "team", Version: 7}); err == nil {
		t.Error("a matcher that does not compile was taken")
	}
	if cached := at(Stamp{Label: "owner", Version: 5}); f.reads != 6 || cached == nil {
		t.Errorf("the cache after failures: %d reads", f.reads)
	}
}
